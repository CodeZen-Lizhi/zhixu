package postgres

import (
	"context"
	"errors"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

type ragProgressDB interface {
	Begin(context.Context) (pgx.Tx, error)
}

// RAGProgressStore 把真实 RAG 阶段边界持久化为可重放 Server Event。
type RAGProgressStore struct {
	db     ragProgressDB
	events eventsapplication.Appender
}

// NewRAGProgressStore 创建不保存正文的阶段事件 Store。
func NewRAGProgressStore(db ragProgressDB, events eventsapplication.Appender) (*RAGProgressStore, error) {
	if db == nil || events == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, agentapplication.ErrorCodeRAGProgressUnknown, true, errors.New("rag progress dependencies are unavailable"))
	}
	return &RAGProgressStore{db: db, events: events}, nil
}

// RecordRAGProgress 在独立短事务中追加一个幂等阶段事件。
func (store *RAGProgressStore) RecordRAGProgress(ctx context.Context, record agentapplication.RAGProgressRecord) error {
	if store == nil || store.db == nil || store.events == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, agentapplication.ErrorCodeRAGProgressUnknown, true, errors.New("rag progress store is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, agentapplication.ErrorCodeRAGProgressUnknown, false, errors.New("rag progress context is nil"))
	}
	_, err := validateRAGProgressRecord(record)
	if err != nil {
		return err
	}
	tx, err := store.db.Begin(ctx)
	if err != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, agentapplication.ErrorCodeRAGProgressUnknown, true, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	sourceEventRef := "rag.progress:" + string(record.ModelRunID) + ":" + string(record.Update.Stage)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(record.WorkspaceID)+"\x1f"+sourceEventRef); err != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, agentapplication.ErrorCodeRAGProgressUnknown, true, err)
	}
	occurredAt := record.OccurredAt.UTC().Truncate(time.Microsecond)
	if err := tx.QueryRow(ctx, `SELECT occurred_at FROM ops.server_event WHERE workspace_id=$1 AND source_event_ref=$2`,
		string(record.WorkspaceID), sourceEventRef).Scan(&occurredAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, agentapplication.ErrorCodeRAGProgressUnknown, true, err)
	}
	conversationID, workflowRunID := record.ConversationID, record.WorkflowRunID
	rewriteCount, candidateCount := int64(record.Update.RewriteCount), int64(record.Update.CandidateCount)
	selectedCount, conflictCount := int64(record.Update.SelectedCount), int64(record.Update.ConflictCount)
	degradationCount := int64(record.Update.DegradationCount)
	eventType := "rag." + string(record.Update.Stage)
	_, replayed, err := store.events.AppendTx(ctx, tx, eventsdomain.AppendRequest{
		WorkspaceID: record.WorkspaceID, ConversationID: &conversationID, WorkflowRunID: &workflowRunID,
		Type: eventType, ResourceRef: "answer:" + string(record.AnswerID), ResourceVersion: 1,
		PayloadSummary: eventsdomain.PayloadSummary{
			ConversationID: &conversationID, WorkflowRunID: &workflowRunID, QuestionID: &record.QuestionID,
			AnswerID: &record.AnswerID, ModelRunID: &record.ModelRunID, Status: "running", Stage: string(record.Update.Stage),
			RewriteCount: &rewriteCount, CandidateCount: &candidateCount, SelectedCount: &selectedCount,
			ConflictCount: &conflictCount, DegradationCount: &degradationCount,
		},
		SchemaVersion:  1,
		SourceEventRef: sourceEventRef,
		OccurredAt:     occurredAt,
	})
	if err != nil {
		return err
	}
	_ = replayed
	if err := tx.Commit(ctx); err != nil {
		return foundation.NewError(foundation.ErrorManualRecoveryRequired, agentapplication.ErrorCodeRAGProgressUnknown, false, err)
	}
	return nil
}

func validateRAGProgressRecord(record agentapplication.RAGProgressRecord) (int, error) {
	ids := []foundation.ID{record.WorkspaceID, record.WorkflowRunID, record.ConversationID, record.QuestionID, record.AnswerID, record.ModelRunID}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return 0, foundation.NewError(foundation.ErrorInvalidInput, agentapplication.ErrorCodeRAGProgressUnknown, false, errors.New("rag progress identity is invalid"))
		}
		if _, duplicate := seen[id]; duplicate {
			return 0, foundation.NewError(foundation.ErrorInvalidInput, agentapplication.ErrorCodeRAGProgressUnknown, false, errors.New("rag progress identity is reused"))
		}
		seen[id] = struct{}{}
	}
	if record.OccurredAt.IsZero() || record.Update.RewriteCount < 0 || record.Update.RewriteCount > 3 ||
		record.Update.CandidateCount < 0 || record.Update.SelectedCount < 0 || record.Update.ConflictCount < 0 || record.Update.DegradationCount < 0 {
		return 0, foundation.NewError(foundation.ErrorInvalidInput, agentapplication.ErrorCodeRAGProgressUnknown, false, errors.New("rag progress counts or time are invalid"))
	}
	stages := map[agentapplication.RAGProgressStage]int{
		agentapplication.RAGProgressPlanStarted: 1, agentapplication.RAGProgressPlanCompleted: 2,
		agentapplication.RAGProgressRetrievalStarted: 3, agentapplication.RAGProgressRetrievalCompleted: 4,
		agentapplication.RAGProgressValidationStarted: 5, agentapplication.RAGProgressValidationCompleted: 6,
	}
	order, ok := stages[record.Update.Stage]
	if !ok {
		return 0, foundation.NewError(foundation.ErrorInvalidInput, agentapplication.ErrorCodeRAGProgressUnknown, false, errors.New("rag progress stage is invalid"))
	}
	return order, nil
}

var _ agentapplication.RAGProgressRecorder = (*RAGProgressStore)(nil)
