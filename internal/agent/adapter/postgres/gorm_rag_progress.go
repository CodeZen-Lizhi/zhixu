package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	gormRAGProgressLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended(?,0))`
	gormRAGProgressTimeSQL = `
		SELECT occurred_at
		FROM ops.server_event
		WHERE workspace_id=?::uuid AND source_event_ref=?`
)

// GORMRAGProgressStore is the staged scoped-transaction implementation of RAG progress persistence.
type GORMRAGProgressStore struct {
	unitOfWork foundation.UnitOfWork
	events     eventsapplication.ScopedAppender
}

// NewGORMRAGProgressStore derives its transaction boundary from the supplied shared platform Pool.
func NewGORMRAGProgressStore(
	pool *platformpostgres.Pool,
	events eventsapplication.ScopedAppender,
) (*GORMRAGProgressStore, error) {
	if pool == nil || nilAgentDependency(events) {
		return nil, gormRAGProgressUnavailable(nil, errors.New("rag progress dependencies are unavailable"))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, gormRAGProgressUnavailable(nil, err)
	}
	if nilAgentDependency(unitOfWork) {
		return nil, gormRAGProgressUnavailable(nil, errors.New("rag progress unit of work is unavailable"))
	}
	return &GORMRAGProgressStore{unitOfWork: unitOfWork, events: events}, nil
}

// RecordRAGProgress appends one exact-replay Server Event in a Store-owned shared transaction.
func (store *GORMRAGProgressStore) RecordRAGProgress(ctx context.Context, record agentapplication.RAGProgressRecord) error {
	if store == nil || nilAgentDependency(store.unitOfWork) || nilAgentDependency(store.events) {
		return gormRAGProgressUnavailable(ctx, errors.New("rag progress store is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(
			foundation.ErrorInvalidInput,
			agentapplication.ErrorCodeRAGProgressUnknown,
			false,
			errors.New("rag progress context is nil"),
		)
	}
	if _, err := validateRAGProgressRecord(record); err != nil {
		return err
	}

	sourceEventRef := "rag.progress:" + string(record.ModelRunID) + ":" + string(record.Update.Stage)
	callbackSucceeded := false
	err := store.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(
		callbackCtx context.Context,
		scope foundation.TransactionScope,
	) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return classifyGORMRAGProgressFailure(callbackCtx, err)
		}
		transaction = transaction.WithContext(callbackCtx)
		lockKey := string(record.WorkspaceID) + "\x1f" + sourceEventRef
		if result := transaction.Exec(gormRAGProgressLockSQL, lockKey); result.Error != nil {
			return classifyGORMRAGProgressFailure(callbackCtx, result.Error)
		}

		occurredAt := record.OccurredAt.UTC().Truncate(time.Microsecond)
		row, err := gormRawRow(transaction, gormRAGProgressTimeSQL, string(record.WorkspaceID), sourceEventRef)
		if err != nil {
			return classifyGORMRAGProgressFailure(callbackCtx, err)
		}
		if err := row.Scan(&occurredAt); err != nil && !gormNoRows(err) {
			return classifyGORMRAGProgressFailure(callbackCtx, err)
		}
		occurredAt = occurredAt.UTC().Truncate(time.Microsecond)

		conversationID, workflowRunID := record.ConversationID, record.WorkflowRunID
		rewriteCount := int64(record.Update.RewriteCount)
		candidateCount := int64(record.Update.CandidateCount)
		selectedCount := int64(record.Update.SelectedCount)
		conflictCount := int64(record.Update.ConflictCount)
		degradationCount := int64(record.Update.DegradationCount)
		_, _, err = store.events.AppendScoped(callbackCtx, scope, eventsdomain.AppendRequest{
			WorkspaceID:     record.WorkspaceID,
			ConversationID:  &conversationID,
			WorkflowRunID:   &workflowRunID,
			Type:            "rag." + string(record.Update.Stage),
			ResourceRef:     "answer:" + string(record.AnswerID),
			ResourceVersion: 1,
			PayloadSummary: eventsdomain.PayloadSummary{
				ConversationID:   &conversationID,
				WorkflowRunID:    &workflowRunID,
				QuestionID:       &record.QuestionID,
				AnswerID:         &record.AnswerID,
				ModelRunID:       &record.ModelRunID,
				Status:           "running",
				Stage:            string(record.Update.Stage),
				RewriteCount:     &rewriteCount,
				CandidateCount:   &candidateCount,
				SelectedCount:    &selectedCount,
				ConflictCount:    &conflictCount,
				DegradationCount: &degradationCount,
			},
			SchemaVersion:  1,
			SourceEventRef: sourceEventRef,
			OccurredAt:     occurredAt,
		})
		if err != nil {
			return err
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return nil
	}
	if callbackSucceeded {
		return foundation.NewError(
			foundation.ErrorManualRecoveryRequired,
			agentapplication.ErrorCodeRAGProgressUnknown,
			false,
			ragProgressCause(ctx, err),
		)
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	return classifyGORMRAGProgressFailure(ctx, err)
}

func classifyGORMRAGProgressFailure(ctx context.Context, cause error) error {
	if cause == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		return cause
	}
	if contextCause := agentContextCause(ctx, cause); contextCause != nil {
		if errors.Is(contextCause, context.Canceled) {
			return foundation.NewError(
				foundation.ErrorNonRetryableFailure,
				agentapplication.ErrorCodeRAGProgressUnknown,
				false,
				contextCause,
			)
		}
		return foundation.NewError(
			foundation.ErrorRetryableFailure,
			agentapplication.ErrorCodeRAGProgressUnknown,
			true,
			contextCause,
		)
	}
	var postgresError *pgconn.PgError
	if errors.As(cause, &postgresError) {
		switch postgresError.Code {
		case "40001", "40P01", "55P03":
			return foundation.NewError(
				foundation.ErrorRetryableFailure,
				agentapplication.ErrorCodeRAGProgressUnknown,
				true,
				cause,
			)
		case "23505":
			return foundation.NewError(
				foundation.ErrorVersionConflict,
				agentapplication.ErrorCodeRAGProgressUnknown,
				false,
				cause,
			)
		case "23503", "23514", "55000":
			return foundation.NewError(
				foundation.ErrorConsistencyViolation,
				agentapplication.ErrorCodeRAGProgressUnknown,
				false,
				cause,
			)
		}
	}
	if errors.Is(cause, sql.ErrTxDone) {
		return foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			agentapplication.ErrorCodeRAGProgressUnknown,
			true,
			cause,
		)
	}
	return gormRAGProgressUnavailable(ctx, cause)
}

func gormRAGProgressUnavailable(ctx context.Context, cause error) error {
	return foundation.NewError(
		foundation.ErrorDependencyUnavailable,
		agentapplication.ErrorCodeRAGProgressUnknown,
		true,
		ragProgressCause(ctx, cause),
	)
}

func ragProgressCause(ctx context.Context, cause error) error {
	if contextCause := agentContextCause(ctx, cause); contextCause != nil {
		return contextCause
	}
	if cause == nil {
		return errors.New("rag progress dependency is unavailable")
	}
	return cause
}

var _ agentapplication.RAGProgressRecorder = (*GORMRAGProgressStore)(nil)
