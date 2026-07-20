package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

const (
	ErrorCodeAnswerFinalizeInvalid     = "CONVERSATION_ANSWER_FINALIZE_INVALID"
	ErrorCodeAnswerFinalizeConflict    = "CONVERSATION_ANSWER_FINALIZE_CONFLICT"
	ErrorCodeAnswerFinalizeCorrupt     = "CONVERSATION_ANSWER_FINALIZE_CORRUPT"
	ErrorCodeAnswerFinalizeUnavailable = "CONVERSATION_ANSWER_FINALIZE_UNAVAILABLE"
)

// AnswerFinalizer 在单个 PostgreSQL 事务中终结 Agent 与 Conversation 发布事实。
type AnswerFinalizer struct {
	db     DB
	agent  agentapplication.ModelRunTxFinalizer
	events eventsapplication.Appender
	clock  foundation.Clock
}

// NewAnswerFinalizer 构造原子 Answer 发布器。
func NewAnswerFinalizer(db DB, agent agentapplication.ModelRunTxFinalizer, events eventsapplication.Appender, clock foundation.Clock) (*AnswerFinalizer, error) {
	if isNilInterface(db) || isNilInterface(agent) || isNilInterface(events) || isNilInterface(clock) {
		return nil, dependency(ErrorCodeAnswerFinalizeUnavailable, errors.New("answer finalizer dependency is nil"))
	}
	return &AnswerFinalizer{db: db, agent: agent, events: events, clock: clock}, nil
}

// Lookup 在 Provider 调用前恢复完整终态；检测任何 split state 时 fail closed。
func (finalizer *AnswerFinalizer) Lookup(ctx context.Context, lookup conversationapplication.AnswerPublicationLookup) (conversationworkflow.OutputReceipt, bool, error) {
	if finalizer == nil || isNilInterface(finalizer.db) || isNilInterface(finalizer.agent) {
		return conversationworkflow.OutputReceipt{}, false, dependency(ErrorCodeAnswerFinalizeUnavailable, errors.New("answer finalizer is unavailable"))
	}
	if err := validatePublicationLookup(ctx, lookup); err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	tx, err := finalizer.db.Begin(ctx)
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	view, err := loadBoundAnswer(ctx, tx, lookup, " FOR SHARE OF a")
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	if view.Answer.PublicationStatus == conversationdomain.AnswerPublicationPending {
		run, found, runErr := finalizer.agent.GetModelRunByAttemptTx(ctx, tx, lookup.WorkspaceID, lookup.NodeAttemptID, false)
		if runErr != nil {
			return conversationworkflow.OutputReceipt{}, false, classify(runErr, ErrorCodeAnswerFinalizeUnavailable)
		}
		if found && (run.WorkflowRunID != lookup.WorkflowRunID || run.NodeRunID != lookup.NodeRunID || run.Status != agentdomain.ModelRunRunning) {
			return conversationworkflow.OutputReceipt{}, false, consistency(ErrorCodeAnswerFinalizeCorrupt, errors.New("pending answer model run binding is split or corrupt"))
		}
		if err := tx.Commit(ctx); err != nil {
			return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
		}
		return conversationworkflow.OutputReceipt{}, false, nil
	}
	if view.Answer.ModelRunID == nil {
		return conversationworkflow.OutputReceipt{}, false, consistency(ErrorCodeAnswerFinalizeCorrupt, errors.New("published answer has no model run"))
	}
	record, err := finalizer.agent.GetModelRunTx(ctx, tx, lookup.WorkspaceID, *view.Answer.ModelRunID, false)
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	if err := validateTerminalBinding(record, lookup, view.Answer); err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	receipt, err := receiptFromAnswer(view.Answer)
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	return receipt, true, nil
}

// Finalize 原子完成 Model Run、Answer、Conversation activity 与 terminal event。
func (finalizer *AnswerFinalizer) Finalize(ctx context.Context, command conversationapplication.FinalizeAnswerCommand) (conversationworkflow.OutputReceipt, bool, error) {
	if finalizer == nil || isNilInterface(finalizer.db) || isNilInterface(finalizer.agent) || isNilInterface(finalizer.events) || isNilInterface(finalizer.clock) {
		return conversationworkflow.OutputReceipt{}, false, dependency(ErrorCodeAnswerFinalizeUnavailable, errors.New("answer finalizer is unavailable"))
	}
	if err := validateFinalizeCommand(ctx, command); err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	tx, err := finalizer.db.Begin(ctx)
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 与 Question dispatch 保持 conversation -> answer -> model_run 的全局锁顺序。
	var conversationVersion int64
	var conversationCreatedAt, lastActivityAt time.Time
	if err := tx.QueryRow(ctx, `SELECT version,created_at,last_activity_at FROM agent.conversation
		WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(command.WorkspaceID), string(command.ConversationID)).Scan(
		&conversationVersion, &conversationCreatedAt, &lastActivityAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return conversationworkflow.OutputReceipt{}, false, notFound(ErrorCodeConversationNotFound, err)
		}
		return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	view, err := loadBoundAnswer(ctx, tx, command.AnswerPublicationLookup, " FOR UPDATE OF a")
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	runID := command.ModelRunID
	if view.Answer.ModelRunID != nil {
		runID = *view.Answer.ModelRunID
	}
	run, err := finalizer.agent.GetModelRunTx(ctx, tx, command.WorkspaceID, runID, true)
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	if run.WorkflowRunID != command.WorkflowRunID || run.NodeRunID != command.NodeRunID || run.NodeAttemptID != command.NodeAttemptID {
		return conversationworkflow.OutputReceipt{}, false, consistency(ErrorCodeAnswerFinalizeCorrupt, errors.New("model run is not bound to the requested workflow attempt"))
	}

	if view.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending {
		if run.Status == agentdomain.ModelRunRunning {
			return conversationworkflow.OutputReceipt{}, false, consistency(ErrorCodeAnswerFinalizeCorrupt, errors.New("published answer is bound to a running model run"))
		}
		candidate, buildErr := buildPublication(command, run, view.Answer, view.Answer.UpdatedAt)
		if buildErr != nil || !samePublishedProposal(view.Answer, candidate.answer) || !sameTerminalRun(run, candidate.modelRun) {
			return conversationworkflow.OutputReceipt{}, false, conflict(ErrorCodeAnswerFinalizeConflict, errors.New("answer already contains a different terminal proposal"))
		}
		receipt, receiptErr := receiptFromAnswer(view.Answer)
		if receiptErr != nil {
			return conversationworkflow.OutputReceipt{}, false, receiptErr
		}
		if err := tx.Commit(ctx); err != nil {
			return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
		}
		return receipt, true, nil
	}
	if run.Status != agentdomain.ModelRunRunning {
		return conversationworkflow.OutputReceipt{}, false, consistency(ErrorCodeAnswerFinalizeCorrupt, errors.New("pending answer is bound to a terminal model run"))
	}
	if command.ExpectedAnswerVersion != view.Answer.Version || command.ExpectedModelRunVersion != run.Version {
		return conversationworkflow.OutputReceipt{}, false, conflict(ErrorCodeAnswerFinalizeConflict, errors.New("answer or model run version changed"))
	}
	now := finalizer.clock.Now().UTC()
	for _, floor := range []time.Time{conversationCreatedAt, lastActivityAt, view.Answer.CreatedAt, run.CreatedAt, run.UpdatedAt} {
		if now.Before(floor) {
			now = floor.UTC()
		}
	}
	publication, err := buildPublication(command, run, view.Answer, now)
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	if _, replayed, err := finalizer.agent.FinalizeModelRunTx(ctx, tx, agentapplication.FinalizeModelRunCommand{
		ExpectedVersion: command.ExpectedModelRunVersion, Run: publication.modelRun,
	}); err != nil || replayed {
		if err == nil {
			err = errors.New("model run unexpectedly replayed while answer is pending")
		}
		return conversationworkflow.OutputReceipt{}, false, consistency(ErrorCodeAnswerFinalizeCorrupt, err)
	}
	summaryJSON, err := json.Marshal(publication.answer.RetrievalSummary)
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, invalid(ErrorCodeAnswerFinalizeInvalid, err)
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.answer SET
		model_run_id=$1,publication_status=$2,result_type=$3,result=$4::jsonb,result_hash=$5,retrieval_summary=$6::jsonb,
		version=version+1,updated_at=$7,published_at=$7
		WHERE workspace_id=$8 AND id=$9 AND publication_status='pending' AND version=$10`,
		string(command.ModelRunID), string(publication.answer.PublicationStatus), string(publication.answer.ResultType), string(publication.answer.Result),
		publication.answer.ResultHash, string(summaryJSON), now, string(command.WorkspaceID), string(command.AnswerID), command.ExpectedAnswerVersion)
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return conversationworkflow.OutputReceipt{}, false, conflict(ErrorCodeAnswerFinalizeConflict, errors.New("answer publication CAS failed"))
	}
	updatedConversation, err := tx.Exec(ctx, `UPDATE agent.conversation SET version=version+1,last_activity_at=$3,updated_at=$3
		WHERE workspace_id=$1 AND id=$2 AND version=$4`, string(command.WorkspaceID), string(command.ConversationID), now, conversationVersion)
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	if updatedConversation.RowsAffected() != 1 {
		return conversationworkflow.OutputReceipt{}, false, conflict(ErrorCodeAnswerFinalizeConflict, errors.New("conversation activity CAS failed"))
	}
	if err := finalizer.appendTerminalEvent(ctx, tx, publication.answer, now); err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationworkflow.OutputReceipt{}, false, foundation.NewError(foundation.ErrorManualRecoveryRequired, conversationapplication.ErrorCodeAnswerFinalizationUnknown, false, err)
	}
	receipt, err := receiptFromAnswer(publication.answer)
	return receipt, false, err
}

func loadBoundAnswer(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, lookup conversationapplication.AnswerPublicationLookup, locking string) (conversationapplication.AnswerView, error) {
	if locking != "" && locking != " FOR SHARE OF a" && locking != " FOR UPDATE OF a" {
		return conversationapplication.AnswerView{}, invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer lock mode is invalid"))
	}
	view, err := scanAnswerView(db.QueryRow(ctx, `SELECT `+answerViewColumns+` FROM agent.answer a
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		WHERE a.workspace_id=$1 AND a.id=$2 AND a.conversation_id=$3 AND a.question_id=$4 AND a.workflow_run_id=$5
		AND EXISTS (SELECT 1 FROM workflow.node_run n JOIN workflow.node_attempt na ON na.node_run_id=n.id
			WHERE n.id=$6 AND n.run_id=a.workflow_run_id AND na.id=$7)`+locking,
		string(lookup.WorkspaceID), string(lookup.AnswerID), string(lookup.ConversationID), string(lookup.QuestionID),
		string(lookup.WorkflowRunID), string(lookup.NodeRunID), string(lookup.NodeAttemptID)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return conversationapplication.AnswerView{}, notFound(ErrorCodeAnswerNotFound, err)
		}
		return conversationapplication.AnswerView{}, err
	}
	return view, nil
}

type builtPublication struct {
	answer   conversationdomain.Answer
	modelRun agentdomain.ModelRun
}

func buildPublication(command conversationapplication.FinalizeAnswerCommand, run agentdomain.ModelRun, pending conversationdomain.Answer, now time.Time) (builtPublication, error) {
	proposal := command.Proposal
	terminalCount := 0
	if proposal.Answer != nil {
		terminalCount++
	}
	if proposal.Refusal != nil {
		terminalCount++
	}
	if proposal.Clarification != nil {
		terminalCount++
	}
	if terminalCount != 1 || proposal.Retrieval == nil || proposal.ModelRunRef != command.ModelRunID || run.ID != command.ModelRunID {
		return builtPublication{}, invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("terminal proposal is incomplete or ambiguous"))
	}
	summary, err := canonicalRetrievalSummary(command.WorkspaceID, *proposal.Retrieval)
	if err != nil {
		return builtPublication{}, invalid(ErrorCodeAnswerFinalizeInvalid, err)
	}
	var resultType conversationdomain.AnswerResultType
	var status conversationdomain.AnswerPublicationStatus
	var document []byte
	terminal := run
	terminal.FinalErrorCode = ""
	switch {
	case proposal.Answer != nil:
		resultType, status = conversationdomain.AnswerResultRAGAnswer, conversationdomain.AnswerPublicationCompleted
		document, err = json.Marshal(proposal.Answer)
		terminal.Status, terminal.FinalResultType = agentdomain.ModelRunSucceeded, agentdomain.ResultTypeRAGAnswer
	case proposal.Refusal != nil:
		resultType, status = conversationdomain.AnswerResultRefusal, conversationdomain.AnswerPublicationRefused
		document, err = json.Marshal(proposal.Refusal)
		terminal.Status, terminal.FinalResultType = agentdomain.ModelRunRefused, agentdomain.ResultTypeRefusal
		terminal.FinalErrorCode = string(proposal.Refusal.Payload.ReasonCode)
	case proposal.Clarification != nil:
		resultType, status = conversationdomain.AnswerResultClarification, conversationdomain.AnswerPublicationClarificationRequired
		suggestedScopes := make([]string, len(proposal.Clarification.SuggestedScopes))
		copy(suggestedScopes, proposal.Clarification.SuggestedScopes)
		result := conversationdomain.ClarificationResult{ResultType: agentdomain.ResultTypeClarification,
			SchemaID: conversationdomain.ClarificationSchemaID, SchemaVersion: conversationdomain.ClarificationSchemaVersionV1,
			ModelRunRef: command.ModelRunID, Payload: conversationdomain.ClarificationPayload{Reason: proposal.Clarification.Reason,
				Question: proposal.Clarification.Question, SuggestedScopes: suggestedScopes}}
		document, err = json.Marshal(result)
		terminal.Status, terminal.FinalResultType = agentdomain.ModelRunSucceeded, agentdomain.ResultTypeClarification
	}
	if err != nil {
		return builtPublication{}, invalid(ErrorCodeAnswerFinalizeInvalid, err)
	}
	published, err := conversationdomain.CanonicalizePublishedResult(resultType, document)
	if err != nil || published.ModelRunID != command.ModelRunID {
		return builtPublication{}, invalid(ErrorCodeAnswerFinalizeInvalid, err)
	}
	if summary.IndexVersionID != nil {
		terminal.Retrieval = agentdomain.RetrievalRef{IndexVersionID: *summary.IndexVersionID, EmbeddingVersionID: summary.EmbeddingVersionID}
	}
	terminal.Version = command.ExpectedModelRunVersion + 1
	terminal.UpdatedAt, terminal.CompletedAt = now, &now
	answer := pending
	answer.ModelRunID = &command.ModelRunID
	answer.PublicationStatus, answer.ResultType = status, resultType
	answer.Result, answer.ResultHash, answer.RetrievalSummary = published.Document, published.Hash, &summary
	answer.Version = command.ExpectedAnswerVersion + 1
	answer.UpdatedAt, answer.PublishedAt = now, &now
	if err := agentdomain.ValidateModelRun(terminal); err != nil {
		return builtPublication{}, invalid(ErrorCodeAnswerFinalizeInvalid, err)
	}
	if err := conversationdomain.ValidateAnswer(answer); err != nil {
		return builtPublication{}, invalid(ErrorCodeAnswerFinalizeInvalid, err)
	}
	return builtPublication{answer: answer, modelRun: terminal}, nil
}

func canonicalRetrievalSummary(workspaceID foundation.ID, source agentapplication.RAGRetrievalSummary) (conversationdomain.RetrievalSummary, error) {
	if source.Rewrites == nil || source.Degradations == nil {
		return conversationdomain.RetrievalSummary{}, errors.New("retrieval proposal requires explicit rewrite and degradation lists")
	}
	rewrites := make([]string, len(source.Rewrites))
	copy(rewrites, source.Rewrites)
	indexID := source.IndexVersionID
	var index *foundation.ID
	if indexID != "" {
		index = &indexID
	}
	degradations := make([]conversationdomain.RetrievalDegradation, len(source.Degradations))
	for i, degradation := range source.Degradations {
		degradations[i] = conversationdomain.RetrievalDegradation{Capability: degradation.Capability, Code: degradation.Code, Retryable: degradation.Retryable}
	}
	return conversationdomain.CanonicalizeRetrievalSummary(workspaceID, conversationdomain.RetrievalSummary{
		Rewrites: rewrites, RequestedMode: source.RequestedMode, EffectiveMode: source.EffectiveMode,
		Scope: conversationdomain.RetrievalScopeSummary{SourceIDs: source.Filter.SourceIDs, SourceVersionIDs: source.Filter.SourceVersionIDs,
			PathPrefixes: source.Filter.PathPrefixes, CapturedAtFrom: source.Filter.CapturedAtFrom, CapturedAtBefore: source.Filter.CapturedAtBefore,
			AllowOriginalSources: source.AllowOriginalSources, AllowWeb: source.AllowWeb},
		IndexVersionID: index, EmbeddingVersionID: source.EmbeddingVersionID, CandidateCount: source.CandidateCount,
		SelectedCount: source.SelectedCount, ConflictCount: source.ConflictCount, Degradations: degradations,
	})
}

func validatePublicationLookup(ctx context.Context, lookup conversationapplication.AnswerPublicationLookup) error {
	if ctx == nil {
		return invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer finalizer context is nil"))
	}
	ids := []foundation.ID{lookup.WorkspaceID, lookup.WorkflowRunID, lookup.NodeRunID, lookup.NodeAttemptID, lookup.ConversationID, lookup.QuestionID, lookup.AnswerID}
	seen := map[foundation.ID]struct{}{}
	for _, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer finalizer identity is invalid"))
		}
		if _, exists := seen[id]; exists {
			return invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer finalizer identity is reused"))
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateFinalizeCommand(ctx context.Context, command conversationapplication.FinalizeAnswerCommand) error {
	if err := validatePublicationLookup(ctx, command.AnswerPublicationLookup); err != nil {
		return err
	}
	parsed, err := foundation.ParseID(string(command.ModelRunID))
	if err != nil || parsed != command.ModelRunID || command.ExpectedAnswerVersion < 1 || command.ExpectedModelRunVersion < 1 || command.Proposal.ModelRunRef != command.ModelRunID {
		return invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer finalization command is invalid"))
	}
	return nil
}

func validateTerminalBinding(run agentdomain.ModelRun, lookup conversationapplication.AnswerPublicationLookup, answer conversationdomain.Answer) error {
	if run.ID != *answer.ModelRunID || run.WorkspaceID != lookup.WorkspaceID || run.WorkflowRunID != lookup.WorkflowRunID || run.NodeRunID != lookup.NodeRunID || run.NodeAttemptID != lookup.NodeAttemptID || run.Status == agentdomain.ModelRunRunning {
		return consistency(ErrorCodeAnswerFinalizeCorrupt, errors.New("published answer and model run are inconsistent"))
	}
	return nil
}

func receiptFromAnswer(answer conversationdomain.Answer) (conversationworkflow.OutputReceipt, error) {
	if err := conversationdomain.ValidateAnswer(answer); err != nil || answer.ModelRunID == nil {
		return conversationworkflow.OutputReceipt{}, consistency(ErrorCodeAnswerFinalizeCorrupt, err)
	}
	receipt := conversationworkflow.OutputReceipt{SchemaVersion: conversationworkflow.OutputSchemaVersion, AnswerID: answer.ID,
		PublicationStatus: conversationworkflow.PublicationStatus(answer.PublicationStatus), ResultType: conversationworkflow.ResultType(answer.ResultType), ModelRunID: *answer.ModelRunID, ResultHash: answer.ResultHash}
	if _, err := conversationworkflow.EncodeOutputReceipt(receipt); err != nil {
		return conversationworkflow.OutputReceipt{}, consistency(ErrorCodeAnswerFinalizeCorrupt, err)
	}
	return receipt, nil
}

func samePublishedProposal(left, right conversationdomain.Answer) bool {
	return left.ID == right.ID && left.ModelRunID != nil && right.ModelRunID != nil && *left.ModelRunID == *right.ModelRunID && left.PublicationStatus == right.PublicationStatus && left.ResultType == right.ResultType && left.ResultHash == right.ResultHash && reflect.DeepEqual(left.RetrievalSummary, right.RetrievalSummary)
}

func sameTerminalRun(left, right agentdomain.ModelRun) bool {
	return left.ID == right.ID && left.Status == right.Status && left.FinalResultType == right.FinalResultType && left.FinalErrorCode == right.FinalErrorCode && reflect.DeepEqual(left.Retrieval, right.Retrieval)
}

func (finalizer *AnswerFinalizer) appendTerminalEvent(ctx context.Context, tx pgx.Tx, answer conversationdomain.Answer, now time.Time) error {
	answerID, conversationID, workflowRunID, modelRunID := answer.ID, answer.ConversationID, answer.WorkflowRunID, *answer.ModelRunID
	citationCount := int64(0)
	if answer.ResultType == conversationdomain.AnswerResultRAGAnswer {
		projection, err := conversationdomain.ProjectPublishedAnswer(answer.ResultType, answer.Result)
		if err != nil {
			return consistency(ErrorCodeAnswerFinalizeCorrupt, err)
		}
		citationCount = int64(len(projection.Citations))
	}
	candidateCount, selectedCount, conflictCount := int64(answer.RetrievalSummary.CandidateCount), int64(answer.RetrievalSummary.SelectedCount), int64(answer.RetrievalSummary.ConflictCount)
	degradationCount, rewriteCount := int64(len(answer.RetrievalSummary.Degradations)), int64(len(answer.RetrievalSummary.Rewrites))
	eventType := "answer." + string(answer.PublicationStatus)
	_, replayed, err := finalizer.events.AppendTx(ctx, tx, eventsdomain.AppendRequest{WorkspaceID: answer.WorkspaceID,
		ConversationID: &conversationID, WorkflowRunID: &workflowRunID, Type: eventType,
		ResourceRef: "answer:" + string(answer.ID), ResourceVersion: answer.Version,
		PayloadSummary: eventsdomain.PayloadSummary{ConversationID: &conversationID, WorkflowRunID: &workflowRunID,
			AnswerID: &answerID, ModelRunID: &modelRunID, PublicationStatus: string(answer.PublicationStatus), ResultType: string(answer.ResultType),
			CandidateCount: &candidateCount, SelectedCount: &selectedCount, ConflictCount: &conflictCount,
			DegradationCount: &degradationCount, RewriteCount: &rewriteCount, CitationCount: &citationCount},
		SchemaVersion: 1, SourceEventRef: eventType + ":" + string(answer.ID) + ":v2", OccurredAt: now})
	if err != nil {
		return err
	}
	if replayed {
		return consistency(ErrorCodeAnswerFinalizeCorrupt, errors.New("new answer publication reused a terminal event"))
	}
	return nil
}

var _ conversationapplication.AnswerFinalizer = (*AnswerFinalizer)(nil)
