package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMAnswerFinalizer 在同一 scope 终结 Model Run、Draft、Answer 和发布通知。
type GORMAnswerFinalizer struct {
	db     *gorm.DB
	uow    foundation.UnitOfWork
	agent  agentapplication.ScopedModelRunStore
	events eventsapplication.ScopedAppender
	clock  foundation.Clock
}

// NewGORMAnswerFinalizer 使用共享 Pool 构造原子 Answer 发布器。
func NewGORMAnswerFinalizer(pool *platformpostgres.Pool, agent agentapplication.ScopedModelRunStore, events eventsapplication.ScopedAppender, clock foundation.Clock) (*GORMAnswerFinalizer, error) {
	if isNilInterface(agent) || isNilInterface(events) || isNilInterface(clock) {
		return nil, dependency(ErrorCodeAnswerFinalizeUnavailable, errors.New("answer finalizer dependency is nil"))
	}
	db, uow, err := conversationGORMDependencies(pool, ErrorCodeAnswerFinalizeUnavailable)
	if err != nil {
		return nil, err
	}
	return &GORMAnswerFinalizer{db: db, uow: uow, agent: agent, events: events, clock: clock}, nil
}

// Lookup 在 Provider 调用前恢复完整终态，拒绝 pending Answer 与终态 Model Run 分裂。
func (finalizer *GORMAnswerFinalizer) Lookup(ctx context.Context, lookup conversationapplication.AnswerPublicationLookup) (conversationworkflow.OutputReceipt, bool, error) {
	if finalizer == nil || finalizer.db == nil || isNilInterface(finalizer.uow) || isNilInterface(finalizer.agent) {
		return conversationworkflow.OutputReceipt{}, false, dependency(ErrorCodeAnswerFinalizeUnavailable, errors.New("answer finalizer is unavailable"))
	}
	if err := validatePublicationLookup(ctx, lookup); err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	var receipt conversationworkflow.OutputReceipt
	var found bool
	err := withinConversationTransaction(ctx, finalizer.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
		var err error
		receipt, found, err = finalizer.lookupScoped(ctx, tx, scope, lookup)
		return err
	})
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	return receipt, found, nil
}

// Finalize 只在完整发布事务提交后返回 receipt；提交结果未知时要求 exact lookup。
func (finalizer *GORMAnswerFinalizer) Finalize(ctx context.Context, command conversationapplication.FinalizeAnswerCommand) (conversationworkflow.OutputReceipt, bool, error) {
	if finalizer == nil || finalizer.db == nil || isNilInterface(finalizer.uow) || isNilInterface(finalizer.agent) || isNilInterface(finalizer.events) || isNilInterface(finalizer.clock) {
		return conversationworkflow.OutputReceipt{}, false, dependency(ErrorCodeAnswerFinalizeUnavailable, errors.New("answer finalizer is unavailable"))
	}
	if command.Draft != nil {
		frozen := *command.Draft
		command.Draft = &frozen
	}
	if err := validateFinalizeCommand(ctx, command); err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	var receipt conversationworkflow.OutputReceipt
	var replayed, prepared bool
	err := withinConversationTransaction(ctx, finalizer.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
		var err error
		receipt, replayed, err = finalizer.finalizeScoped(ctx, tx, scope, command)
		prepared = err == nil
		return err
	})
	if err != nil {
		if prepared && !replayed {
			return conversationworkflow.OutputReceipt{}, false, foundation.NewError(foundation.ErrorManualRecoveryRequired, conversationapplication.ErrorCodeAnswerFinalizationUnknown, false, err)
		}
		return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	return receipt, replayed, nil
}

func (finalizer *GORMAnswerFinalizer) lookupScoped(ctx context.Context, tx *gorm.DB, transaction foundation.TransactionScope, lookup conversationapplication.AnswerPublicationLookup) (conversationworkflow.OutputReceipt, bool, error) {
	if finalizer == nil || isNilInterface(finalizer.db) || isNilInterface(finalizer.agent) {
		return conversationworkflow.OutputReceipt{}, false, dependency(ErrorCodeAnswerFinalizeUnavailable, errors.New("answer finalizer is unavailable"))
	}
	if err := validatePublicationLookup(ctx, lookup); err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	view, err := gormLoadBoundAnswer(ctx, tx, lookup, " FOR SHARE OF a")
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	if view.Answer.PublicationStatus == conversationdomain.AnswerPublicationPending {
		run, found, runErr := finalizer.agent.GetModelRunByAttemptScoped(ctx, transaction, lookup.WorkspaceID, lookup.NodeAttemptID, false)
		if runErr != nil {
			return conversationworkflow.OutputReceipt{}, false, classify(runErr, ErrorCodeAnswerFinalizeUnavailable)
		}
		if found && (run.WorkflowRunID != lookup.WorkflowRunID || run.NodeRunID != lookup.NodeRunID || run.Status != agentdomain.ModelRunRunning) {
			return conversationworkflow.OutputReceipt{}, false, consistency(ErrorCodeAnswerFinalizeCorrupt, errors.New("pending answer model run binding is split or corrupt"))
		}
		return conversationworkflow.OutputReceipt{}, false, nil
	}
	if view.Answer.ModelRunID == nil {
		return conversationworkflow.OutputReceipt{}, false, consistency(ErrorCodeAnswerFinalizeCorrupt, errors.New("published answer has no model run"))
	}
	record, err := finalizer.agent.GetModelRunScoped(ctx, transaction, lookup.WorkspaceID, *view.Answer.ModelRunID, false)
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
	return receipt, true, nil
}

func (finalizer *GORMAnswerFinalizer) finalizeScoped(ctx context.Context, tx *gorm.DB, transaction foundation.TransactionScope, command conversationapplication.FinalizeAnswerCommand) (conversationworkflow.OutputReceipt, bool, error) {
	if finalizer == nil || isNilInterface(finalizer.db) || isNilInterface(finalizer.agent) || isNilInterface(finalizer.events) || isNilInterface(finalizer.clock) {
		return conversationworkflow.OutputReceipt{}, false, dependency(ErrorCodeAnswerFinalizeUnavailable, errors.New("answer finalizer is unavailable"))
	}
	if command.Draft != nil {
		frozen := *command.Draft
		command.Draft = &frozen
	}
	if err := validateFinalizeCommand(ctx, command); err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}

	// 与 Question dispatch 保持 conversation -> answer -> model_run 的全局锁顺序。
	var conversationVersion int64
	var conversationCreatedAt, lastActivityAt time.Time
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT version,created_at,last_activity_at FROM agent.conversation
		WHERE workspace_id=? AND id=? FOR UPDATE`, string(command.WorkspaceID), string(command.ConversationID))).Scan(
		&conversationVersion, &conversationCreatedAt, &lastActivityAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return conversationworkflow.OutputReceipt{}, false, notFound(ErrorCodeConversationNotFound, err)
		}
		return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	view, err := gormLoadBoundAnswer(ctx, tx, command.AnswerPublicationLookup, " FOR UPDATE OF a")
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	runID := command.ModelRunID
	if view.Answer.ModelRunID != nil {
		runID = *view.Answer.ModelRunID
	}
	run, err := finalizer.agent.GetModelRunScoped(ctx, transaction, command.WorkspaceID, runID, true)
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
		if err := gormValidateTerminalDraft(ctx, tx, command); err != nil {
			return conversationworkflow.OutputReceipt{}, false, err
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
	draftLeaseUntil, err := gormTerminalizeDraft(ctx, tx, command, now)
	if err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	if _, replayed, err := finalizer.agent.FinalizeModelRunScoped(ctx, transaction, agentapplication.FinalizeModelRunCommand{
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
	tag := tx.Model(&answerModel{}).Where("workspace_id=? AND id=? AND publication_status='pending' AND version=?", string(command.WorkspaceID), string(command.AnswerID), command.ExpectedAnswerVersion).Updates(map[string]any{
		"model_run_id": string(command.ModelRunID), "publication_status": string(publication.answer.PublicationStatus), "result_type": string(publication.answer.ResultType),
		"result": conversationJSONB(publication.answer.Result), "result_hash": publication.answer.ResultHash, "retrieval_summary": conversationJSONB(summaryJSON),
		"version": gorm.Expr("version+1"), "updated_at": now, "published_at": now,
	})
	if err := tag.Error; err != nil {
		return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	if tag.RowsAffected != 1 {
		return conversationworkflow.OutputReceipt{}, false, conflict(ErrorCodeAnswerFinalizeConflict, errors.New("answer publication CAS failed"))
	}
	updatedConversation := tx.Model(&conversationModel{}).Where("workspace_id=? AND id=? AND version=?", string(command.WorkspaceID), string(command.ConversationID), conversationVersion).Updates(map[string]any{"version": gorm.Expr("version+1"), "last_activity_at": now, "updated_at": now})
	if err := updatedConversation.Error; err != nil {
		return conversationworkflow.OutputReceipt{}, false, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	if updatedConversation.RowsAffected != 1 {
		return conversationworkflow.OutputReceipt{}, false, conflict(ErrorCodeAnswerFinalizeConflict, errors.New("conversation activity CAS failed"))
	}
	if err := finalizer.appendTerminalEvent(ctx, tx, transaction, publication.answer, now); err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	if err := gormValidateActiveDraftClaimLease(ctx, tx, draftLeaseUntil); err != nil {
		return conversationworkflow.OutputReceipt{}, false, err
	}
	receipt, err := receiptFromAnswer(publication.answer)
	return receipt, false, err
}

func gormLoadBoundAnswer(ctx context.Context, db *gorm.DB, lookup conversationapplication.AnswerPublicationLookup, locking string) (conversationapplication.AnswerView, error) {
	if locking != "" && locking != " FOR SHARE OF a" && locking != " FOR UPDATE OF a" {
		return conversationapplication.AnswerView{}, invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer lock mode is invalid"))
	}
	view, err := scanAnswerView(gormScanRow(db.WithContext(ctx).Raw(`SELECT `+answerViewColumns+` FROM agent.answer a
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		JOIN workflow.definition d ON d.id=w.definition_id AND d.workspace_id=w.workspace_id
		WHERE a.workspace_id=? AND a.id=? AND a.conversation_id=? AND a.question_id=? AND a.workflow_run_id=?
		AND EXISTS (SELECT 1 FROM workflow.node_run n JOIN workflow.node_attempt na ON na.node_run_id=n.id
			WHERE n.id=? AND n.run_id=a.workflow_run_id AND na.id=?)`+locking, string(lookup.WorkspaceID), string(lookup.AnswerID), string(lookup.ConversationID), string(lookup.QuestionID), string(lookup.WorkflowRunID), string(lookup.NodeRunID), string(lookup.NodeAttemptID))))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return conversationapplication.AnswerView{}, notFound(ErrorCodeAnswerNotFound, err)
		}
		return conversationapplication.AnswerView{}, err
	}
	return view, nil
}

func gormTerminalizeDraft(ctx context.Context, tx *gorm.DB, command conversationapplication.FinalizeAnswerCommand, now time.Time) (time.Time, error) {
	if command.Draft == nil {
		return time.Time{}, nil
	}
	target, err := expectedDraftTerminalStatus(command.Proposal)
	if err != nil {
		return time.Time{}, err
	}
	draft := *command.Draft
	leaseUntil, err := gormLockActiveDraftClaim(ctx, tx, command, draft)
	if err != nil {
		return time.Time{}, err
	}
	tag := tx.WithContext(ctx).Exec(`UPDATE agent.answer_draft_session
		SET status=?,completed_at=COALESCE(completed_at,GREATEST(updated_at,?)),updated_at=GREATEST(updated_at,?)
		WHERE id=? AND workspace_id=? AND answer_id=? AND workflow_run_id=? AND node_run_id=?
		  AND node_attempt_id=? AND attempt_no=? AND lease_owner=? AND generation=?
		  AND ((?='PUBLISHED' AND status IN ('COMPLETED','DEGRADED'))
		    OR (?='ABORTED' AND status IN ('ACTIVE','COMPLETED','DEGRADED')))`, string(target), now, now, string(draft.SessionID), string(command.WorkspaceID), string(command.AnswerID), string(command.WorkflowRunID), string(command.NodeRunID), string(command.NodeAttemptID), draft.AttemptNo, draft.LeaseOwner, draft.Generation, string(target), string(target))
	if err := tag.Error; err != nil {
		return time.Time{}, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	if tag.RowsAffected != 1 {
		return time.Time{}, conflict(ErrorCodeAnswerFinalizeConflict, errors.New("answer draft terminal compare-and-swap failed"))
	}
	return leaseUntil, nil
}

func gormLockActiveDraftClaim(
	ctx context.Context,
	tx *gorm.DB,
	command conversationapplication.FinalizeAnswerCommand,
	draft conversationapplication.AnswerDraftTerminalBinding,
) (time.Time, error) {
	var nodeLeaseUntil time.Time
	err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT lease_until
		FROM workflow.node_run
		WHERE id=? AND run_id=? AND attempt=? AND status='running' AND lease_owner=?
		FOR UPDATE`, string(command.NodeRunID), string(command.WorkflowRunID), draft.AttemptNo, draft.LeaseOwner)).Scan(&nodeLeaseUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, conflict(ErrorCodeAnswerFinalizeConflict, errors.New("answer draft runtime claim is no longer active"))
	}
	if err != nil {
		return time.Time{}, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	var attemptLeaseUntil time.Time
	err = gormScanRow(tx.WithContext(ctx).Raw(`SELECT lease_until
		FROM workflow.node_attempt
		WHERE id=? AND node_run_id=? AND attempt_no=? AND status='running' AND lease_owner=?
		FOR UPDATE`, string(command.NodeAttemptID), string(command.NodeRunID), draft.AttemptNo, draft.LeaseOwner)).Scan(&attemptLeaseUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, conflict(ErrorCodeAnswerFinalizeConflict, errors.New("answer draft runtime claim is no longer active"))
	}
	if err != nil {
		return time.Time{}, classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	if attemptLeaseUntil.Before(nodeLeaseUntil) {
		nodeLeaseUntil = attemptLeaseUntil
	}
	if err := gormValidateActiveDraftClaimLease(ctx, tx, nodeLeaseUntil); err != nil {
		return time.Time{}, err
	}
	return nodeLeaseUntil, nil
}

func gormValidateActiveDraftClaimLease(ctx context.Context, tx *gorm.DB, leaseUntil time.Time) error {
	if leaseUntil.IsZero() {
		return nil
	}
	var now time.Time
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT clock_timestamp()`)).Scan(&now); err != nil {
		return classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	if !leaseUntil.After(now) {
		return conflict(ErrorCodeAnswerFinalizeConflict, errors.New("answer draft runtime claim lease has expired"))
	}
	return nil
}

func gormValidateTerminalDraft(ctx context.Context, tx *gorm.DB, command conversationapplication.FinalizeAnswerCommand) error {
	if command.Draft == nil {
		return nil
	}
	target, err := expectedDraftTerminalStatus(command.Proposal)
	if err != nil {
		return err
	}
	draft := *command.Draft
	var workspaceID, answerID, workflowRunID, nodeRunID, nodeAttemptID, leaseOwner, status string
	var attemptNo int
	var generation int64
	err = gormScanRow(tx.WithContext(ctx).Raw(`SELECT workspace_id::text,answer_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,
		attempt_no,lease_owner,generation,status
		FROM agent.answer_draft_session
		WHERE id=?`, string(draft.SessionID))).Scan(
		&workspaceID, &answerID, &workflowRunID, &nodeRunID, &nodeAttemptID, &attemptNo, &leaseOwner, &generation, &status,
	)
	if errors.Is(err, sql.ErrNoRows) {
		var conflictingProjection bool
		if queryErr := gormScanRow(tx.WithContext(ctx).Raw(`SELECT EXISTS(
			SELECT 1 FROM agent.answer_draft_session
			WHERE workspace_id=? AND answer_id=? AND status IN ('ACTIVE','COMPLETED','DEGRADED','PUBLISHED','ABORTED')
		)`, string(command.WorkspaceID), string(command.AnswerID))).Scan(&conflictingProjection); queryErr != nil {
			return classify(queryErr, ErrorCodeAnswerFinalizeUnavailable)
		}
		if conflictingProjection {
			return conflict(ErrorCodeAnswerFinalizeConflict, errors.New("terminal answer draft identity differs"))
		}
		// 草稿是带 TTL 的瞬态投影；清理后仍必须回放 canonical Answer 终态。
		return nil
	}
	if err != nil {
		return classify(err, ErrorCodeAnswerFinalizeUnavailable)
	}
	if foundation.ID(workspaceID) != command.WorkspaceID || foundation.ID(answerID) != command.AnswerID || foundation.ID(workflowRunID) != command.WorkflowRunID ||
		foundation.ID(nodeRunID) != command.NodeRunID || foundation.ID(nodeAttemptID) != command.NodeAttemptID || attemptNo != draft.AttemptNo ||
		leaseOwner != draft.LeaseOwner || generation != draft.Generation || status != string(target) {
		return conflict(ErrorCodeAnswerFinalizeConflict, errors.New("terminal answer draft binding differs"))
	}
	return nil
}

func (finalizer *GORMAnswerFinalizer) appendTerminalEvent(ctx context.Context, tx *gorm.DB, transaction foundation.TransactionScope, answer conversationdomain.Answer, now time.Time) error {
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
	_, replayed, err := finalizer.events.AppendScoped(ctx, transaction, eventsdomain.AppendRequest{WorkspaceID: answer.WorkspaceID,
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

var _ conversationapplication.AnswerFinalizer = (*GORMAnswerFinalizer)(nil)
