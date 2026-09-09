package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"gorm.io/gorm"
)

// GORMWorkspaceAnalysisFinalizer 使用共享事务发布 proof、Answer、事件与审计。
type GORMWorkspaceAnalysisFinalizer struct {
	db             *gorm.DB
	uow            foundation.UnitOfWork
	events         eventsapplication.ScopedAppender
	ids            foundation.IDGenerator
	audit          ScopedWorkspaceAnalysisAuditRecorder
	workerActorRef string
}

// NewGORMWorkspaceAnalysisFinalizer 构造只信任持久事实的工作区分析发布器。
func NewGORMWorkspaceAnalysisFinalizer(pool *platformpostgres.Pool, events eventsapplication.ScopedAppender, ids foundation.IDGenerator) (*GORMWorkspaceAnalysisFinalizer, error) {
	return newGORMWorkspaceAnalysisFinalizer(pool, events, ids, nil, "")
}

// NewGORMWorkspaceAnalysisFinalizerWithAudit 构造带同事务终态审计的发布器。
func NewGORMWorkspaceAnalysisFinalizerWithAudit(pool *platformpostgres.Pool, events eventsapplication.ScopedAppender, ids foundation.IDGenerator, audit ScopedWorkspaceAnalysisAuditRecorder, workerActorRef string) (*GORMWorkspaceAnalysisFinalizer, error) {
	if isNilInterface(audit) || !validWorkspaceAnalysisAuditActorRef(workerActorRef) {
		return nil, dependency(ErrorCodeWorkspaceAnalysisFinalizeUnavailable, errors.New("workspace analysis finalizer audit dependency is invalid"))
	}
	return newGORMWorkspaceAnalysisFinalizer(pool, events, ids, audit, workerActorRef)
}

func newGORMWorkspaceAnalysisFinalizer(pool *platformpostgres.Pool, events eventsapplication.ScopedAppender, ids foundation.IDGenerator, audit ScopedWorkspaceAnalysisAuditRecorder, workerActorRef string) (*GORMWorkspaceAnalysisFinalizer, error) {
	if isNilInterface(events) || isNilInterface(ids) {
		return nil, dependency(ErrorCodeWorkspaceAnalysisFinalizeUnavailable, errors.New("workspace analysis finalizer dependency is nil"))
	}
	db, uow, err := conversationGORMDependencies(pool, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	if err != nil {
		return nil, err
	}
	return &GORMWorkspaceAnalysisFinalizer{db: db, uow: uow, events: events, ids: ids, audit: audit, workerActorRef: workerActorRef}, nil
}

// LookupPublication 在当前 lease fence 下读取完整发布回执。
func (finalizer *GORMWorkspaceAnalysisFinalizer) LookupPublication(ctx context.Context, lookup conversationapplication.WorkspaceAnalysisPublicationLookup) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	if finalizer == nil || finalizer.db == nil || isNilInterface(finalizer.uow) {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
			dependency(ErrorCodeWorkspaceAnalysisFinalizeUnavailable, errors.New("workspace analysis finalizer is unavailable"))
	}
	if ctx == nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
			invalid(conversationapplication.ErrorCodeWorkspaceAnalysisFinalizerInvalid, errors.New("workspace analysis finalizer context is nil"))
	}
	if err := lookup.Validate(); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	var output conversationworkflow.WorkspaceAnalysisPublicationOutput
	var found bool
	err := withinConversationTransaction(ctx, finalizer.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
		var err error
		output, found, err = finalizer.lookupPublicationScoped(ctx, tx, scope, lookup)
		return err
	})
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return output, found, nil
}

func (finalizer *GORMWorkspaceAnalysisFinalizer) lookupPublicationScoped(ctx context.Context, tx *gorm.DB, scope foundation.TransactionScope, lookup conversationapplication.WorkspaceAnalysisPublicationLookup) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	fence, err := gormLockWorkspaceAnalysisFinalizationFence(ctx, tx, lookup)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := validateCurrentWorkspaceAnalysisLease(fence, lookup); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	var lockedConversation int
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT 1 FROM agent.conversation
		WHERE workspace_id=? AND id=? FOR SHARE`, string(lookup.WorkspaceID), string(lookup.ConversationID))).Scan(&lockedConversation); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
			workspaceAnalysisFinalizerQueryError(err, "workspace analysis lookup conversation")
	}
	answerView, err := gormLoadBoundAnswer(ctx, tx, lookup.AnswerPublicationLookup, " FOR SHARE OF a")
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if answerView.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending {
		if err := gormRejectActiveWorkspaceAnalysisDraft(ctx, tx, lookup.WorkspaceID, lookup.AnswerID); err != nil {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
		}
	}
	state, err := gormLoadWorkspaceAnalysisProofState(ctx, tx, lookup, answerView.Answer)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	output, found, err := workspaceAnalysisOutputFromState(lookup, state)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if found {
		citationCount, citationErr := workspaceAnalysisTerminalCitationCount(state.Answer)
		if citationErr != nil {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, citationErr
		}
		if err := finalizer.appendWorkspaceAnalysisTerminalEvents(
			ctx, scope, lookup.AnalysisRunID, state.RunStatus, state.Answer, citationCount, *state.Answer.PublishedAt, true,
		); err != nil {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
		}
		switch {
		case state.Success != nil:
			if err := gormValidateRetainedWorkspaceAnalysisDraft(ctx, tx, lookup.WorkspaceID, lookup.AnswerID, "PUBLISHED", &state.Success.CandidateID); err != nil {
				return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
			}
		case state.Termination != nil:
			if err := gormValidateRetainedWorkspaceAnalysisDraft(ctx, tx, lookup.WorkspaceID, lookup.AnswerID, "ABORTED", nil); err != nil {
				return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
			}
		}
	}
	return output, found, nil
}

// FinalizeSuccess 原子发布成功正文，提交回执丢失时执行精确查证。
func (finalizer *GORMWorkspaceAnalysisFinalizer) FinalizeSuccess(ctx context.Context, command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	if finalizer == nil || finalizer.db == nil || isNilInterface(finalizer.uow) || isNilInterface(finalizer.events) || isNilInterface(finalizer.ids) {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
			dependency(ErrorCodeWorkspaceAnalysisFinalizeUnavailable, errors.New("workspace analysis finalizer is unavailable"))
	}
	if ctx == nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
			invalid(conversationapplication.ErrorCodeWorkspaceAnalysisFinalizerInvalid, errors.New("workspace analysis finalizer context is nil"))
	}
	if err := command.Validate(); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	var output conversationworkflow.WorkspaceAnalysisPublicationOutput
	var replayed bool
	var prepared bool
	err := withinConversationTransaction(ctx, finalizer.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
		var err error
		output, replayed, err = finalizer.finalizeSuccessScoped(ctx, tx, scope, command)
		prepared = err == nil
		return err
	})
	if err != nil {
		if prepared && !replayed {
			return finalizer.recoverWorkspaceAnalysisCommit(ctx, command.WorkspaceAnalysisPublicationLookup, output.ProofID, err)
		}
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return output, replayed, nil
}

func (finalizer *GORMWorkspaceAnalysisFinalizer) finalizeSuccessScoped(ctx context.Context, tx *gorm.DB, scope foundation.TransactionScope, command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	fence, err := gormLockWorkspaceAnalysisFinalizationFence(ctx, tx, command.WorkspaceAnalysisPublicationLookup)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := validateCurrentWorkspaceAnalysisLease(fence, command.WorkspaceAnalysisPublicationLookup); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if fence.RunStatus.Terminal() {
		output, replayErr := finalizer.replaySuccessScoped(ctx, tx, scope, command)
		if replayErr != nil {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, replayErr
		}
		return output, true, nil
	}
	if err := validateActiveSuccessFence(fence, command.WorkspaceAnalysisPublicationLookup); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}

	reviewOperation, reviewAuthority, err := gormLockWorkspaceAnalysisSuccessAuthority(ctx, tx, command)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	facts, err := gormLoadWorkspaceAnalysisSuccessFacts(ctx, tx, command, fence, reviewOperation, reviewAuthority)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	publication, err := buildWorkspaceAnalysisSuccessPublication(command.WorkspaceID, fence, facts)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	slot, err := gormLockWorkspaceAnalysisPublicationSlot(ctx, tx, command.WorkspaceAnalysisPublicationLookup, command.ExpectedAnswerVersion)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := validateSuccessDraft(slot.Draft, facts.Candidate); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	now, err := gormWorkspaceAnalysisPublicationTime(ctx, tx, fence, slot, facts.LatestFactAt)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	proofID, err := finalizer.ids.New()
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if err := gormInsertWorkspaceAnalysisSuccessProof(ctx, tx, proofID, command, publication, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := gormPublishWorkspaceAnalysisDraft(ctx, tx, slot.Draft, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := gormUpdateWorkspaceAnalysisRunSuccess(ctx, tx, fence, facts, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	answer, err := gormUpdateWorkspaceAnalysisAnswer(ctx, tx, slot.Answer, publication, now)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := gormUpdateWorkspaceAnalysisConversation(ctx, tx, slot, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := finalizer.appendWorkspaceAnalysisTerminalEvents(
		ctx, scope, command.AnalysisRunID, publication.RunStatus, answer, publication.CitationCount, now, false,
	); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := appendScopedWorkspaceAnalysisTerminalAudit(
		ctx, scope, finalizer.audit, finalizer.workerActorRef, command.AnalysisRunID,
		publication.RunStatus, publication.Reason, answer, publication.CitationCount, now,
	); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := gormForceWorkspaceAnalysisPublicationConstraints(ctx, tx); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	output, err := workspaceAnalysisPublicationOutputForVersion(answer, proofID, fence.DefinitionVersion)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	return output, false, nil
}

// FinalizeTermination 原子发布由持久 proof 支持的终止正文。
func (finalizer *GORMWorkspaceAnalysisFinalizer) FinalizeTermination(ctx context.Context, command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	if finalizer == nil || finalizer.db == nil || isNilInterface(finalizer.uow) || isNilInterface(finalizer.events) || isNilInterface(finalizer.ids) {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
			dependency(ErrorCodeWorkspaceAnalysisFinalizeUnavailable, errors.New("workspace analysis finalizer is unavailable"))
	}
	if ctx == nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
			invalid(conversationapplication.ErrorCodeWorkspaceAnalysisFinalizerInvalid, errors.New("workspace analysis finalizer context is nil"))
	}
	command = cloneWorkspaceAnalysisTerminationCommand(command)
	if err := command.Validate(); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	var output conversationworkflow.WorkspaceAnalysisPublicationOutput
	var replayed bool
	var prepared bool
	err := withinConversationTransaction(ctx, finalizer.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
		var err error
		output, replayed, err = finalizer.finalizeTerminationScoped(ctx, tx, scope, command)
		prepared = err == nil
		return err
	})
	if err != nil {
		if prepared && !replayed {
			return finalizer.recoverWorkspaceAnalysisCommit(ctx, command.WorkspaceAnalysisPublicationLookup, output.ProofID, err)
		}
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return output, replayed, nil
}

func (finalizer *GORMWorkspaceAnalysisFinalizer) finalizeTerminationScoped(ctx context.Context, tx *gorm.DB, scope foundation.TransactionScope, command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	fence, err := gormLockWorkspaceAnalysisFinalizationFence(ctx, tx, command.WorkspaceAnalysisPublicationLookup)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := validateCurrentWorkspaceAnalysisLease(fence, command.WorkspaceAnalysisPublicationLookup); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if fence.RunStatus.Terminal() {
		output, replayErr := finalizer.replayTerminationScoped(ctx, tx, scope, command)
		if replayErr != nil {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, replayErr
		}
		return output, true, nil
	}
	if err := validateActiveTerminationFence(fence, command); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := gormCloseWorkspaceAnalysisDecisionRuns(ctx, tx, fence.RunID, fence.DefinitionVersion, fence.DatabaseNow); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	authority, err := gormLockWorkspaceAnalysisTerminationAuthority(ctx, tx, command, fence)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	publication, latestFactAt, err := gormBuildWorkspaceAnalysisTerminationPublication(ctx, tx, command, fence, authority)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	slot, err := gormLockWorkspaceAnalysisPublicationSlot(ctx, tx, command.WorkspaceAnalysisPublicationLookup, command.ExpectedAnswerVersion)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	now, err := gormWorkspaceAnalysisPublicationTime(ctx, tx, fence, slot, latestFactAt)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	proofID, err := finalizer.ids.New()
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if err := gormInsertWorkspaceAnalysisTerminationProof(ctx, tx, proofID, command, publication, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := gormAbortWorkspaceAnalysisDraft(ctx, tx, slot.Draft, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := gormUpdateWorkspaceAnalysisRunTermination(ctx, tx, fence, publication, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	answer, err := gormUpdateWorkspaceAnalysisAnswer(ctx, tx, slot.Answer, publication, now)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := gormUpdateWorkspaceAnalysisConversation(ctx, tx, slot, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := finalizer.appendWorkspaceAnalysisTerminalEvents(
		ctx, scope, command.AnalysisRunID, publication.RunStatus, answer, 0, now, false,
	); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := appendScopedWorkspaceAnalysisTerminalAudit(
		ctx, scope, finalizer.audit, finalizer.workerActorRef, command.AnalysisRunID,
		publication.RunStatus, publication.Reason, answer, 0, now,
	); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := gormForceWorkspaceAnalysisPublicationConstraints(ctx, tx); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	output, err := workspaceAnalysisPublicationOutputForVersion(answer, proofID, fence.DefinitionVersion)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	return output, false, nil
}

func gormLockWorkspaceAnalysisFinalizationFence(
	ctx context.Context,
	tx *gorm.DB,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) (workspaceAnalysisFinalizationFence, error) {
	fence := workspaceAnalysisFinalizationFence{RunID: lookup.AnalysisRunID}
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT status,cancel_requested_at
		FROM workflow.run WHERE id=? AND workspace_id=? FOR UPDATE`, string(lookup.WorkflowRunID), string(lookup.WorkspaceID))).Scan(&fence.WorkflowStatus, &fence.WorkflowCancelRequestedAt); err != nil {
		return workspaceAnalysisFinalizationFence{}, workspaceAnalysisFinalizerQueryError(err, "workflow run")
	}
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT status,node_key,attempt,lease_owner,lease_until
		FROM workflow.node_run WHERE id=? AND run_id=? FOR UPDATE`, string(lookup.NodeRunID), string(lookup.WorkflowRunID))).Scan(&fence.NodeStatus, &fence.NodeKey, &fence.NodeAttemptNo, &fence.NodeLeaseOwner, &fence.NodeLeaseUntil); err != nil {
		return workspaceAnalysisFinalizationFence{}, workspaceAnalysisFinalizerQueryError(err, "workflow node run")
	}
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT status,attempt_no,lease_owner,lease_until
		FROM workflow.node_attempt WHERE id=? AND node_run_id=? FOR UPDATE`, string(lookup.NodeAttemptID), string(lookup.NodeRunID))).Scan(&fence.AttemptStatus, &fence.AttemptNo, &fence.AttemptLeaseOwner, &fence.AttemptLeaseUntil); err != nil {
		return workspaceAnalysisFinalizationFence{}, workspaceAnalysisFinalizerQueryError(err, "workflow node attempt")
	}
	var runReason *string
	var definitionKey, definitionHash string
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT
		status,termination_reason,version,deadline_at,definition_key,definition_version,definition_hash,policy_version,
		max_model_calls,max_tool_calls,max_source_reads,max_input_tokens,max_output_tokens,
		reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,
		settled_model_calls,settled_tool_calls,settled_source_reads,settled_input_tokens,settled_output_tokens,
		settled_cost_microunits,created_at,updated_at,clock_timestamp()
		FROM agent.workspace_analysis_run
		WHERE id=? AND workspace_id=? AND conversation_id=? AND question_id=? AND answer_id=? AND workflow_run_id=?
		FOR UPDATE`, string(lookup.AnalysisRunID), string(lookup.WorkspaceID), string(lookup.ConversationID), string(lookup.QuestionID), string(lookup.AnswerID), string(lookup.WorkflowRunID))).Scan(
		&fence.RunStatus, &runReason, &fence.RunVersion, &fence.DeadlineAt, &definitionKey, &fence.DefinitionVersion, &definitionHash, &fence.PolicyVersion,
		&fence.MaxModelCalls, &fence.MaxToolCalls, &fence.MaxSourceReads, &fence.MaxInputTokens, &fence.MaxOutputTokens,
		&fence.ReservedModelCalls, &fence.ReservedToolCalls, &fence.ReservedSourceReads, &fence.ReservedInputTokens, &fence.ReservedOutputTokens,
		&fence.SettledModelCalls, &fence.SettledToolCalls, &fence.SettledSourceReads, &fence.SettledInputTokens, &fence.SettledOutputTokens,
		&fence.SettledCostMicrounits, &fence.RunCreatedAt, &fence.RunUpdatedAt, &fence.DatabaseNow,
	); err != nil {
		return workspaceAnalysisFinalizationFence{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis run")
	}
	if runReason != nil {
		reason := agentdomain.WorkspaceAnalysisRunTerminationReason(*runReason)
		fence.RunReason = &reason
	}
	if definitionKey != conversationworkflow.WorkspaceAnalysisDefinitionKey || fence.DefinitionVersion != workspaceAnalysisPublicationVersion(lookup.DefinitionVersion) {
		return workspaceAnalysisFinalizationFence{}, workspaceAnalysisTerminationAuthorityError("workspace analysis publication version differs from its persisted run")
	}
	if err := gormValidateWorkspaceAnalysisPersistedDefinition(ctx, tx, lookup.WorkspaceID, lookup.WorkflowRunID, fence.DefinitionVersion, fence.PolicyVersion, definitionHash); err != nil {
		return workspaceAnalysisFinalizationFence{}, err
	}
	if fence.DefinitionVersion == 2 {
		if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT count(*) FROM agent.workspace_analysis_evidence WHERE analysis_run_id=?`, string(lookup.AnalysisRunID))).Scan(&fence.EvidenceCount); err != nil {
			return workspaceAnalysisFinalizationFence{}, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
		}
	}
	return fence, nil
}

func gormLockWorkspaceAnalysisSuccessAuthority(
	ctx context.Context,
	tx *gorm.DB,
	command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
) (workspaceAnalysisOperationRecord, workspaceAnalysisCallAuthority, error) {
	operation, err := scanWorkspaceAnalysisOperation(gormScanRow(tx.WithContext(ctx).Raw(`SELECT
		id::text,node_run_id::text,node_key,ordinal,operation_kind,call_kind,status,model_call_id::text,tool_call_id::text,
		result_kind,result_id::text,result_hash,error_code,created_at,updated_at,completed_at
		FROM agent.workspace_analysis_operation
		WHERE analysis_run_id=? AND node_run_id=? AND operation_kind='FAITHFULNESS_REVIEW'
		  AND result_id=? AND result_hash=? FOR UPDATE`, string(command.AnalysisRunID), string(command.NodeRunID), string(command.ReviewModelResultID), command.ReviewModelResultHash)))
	if err != nil {
		return workspaceAnalysisOperationRecord{}, workspaceAnalysisCallAuthority{}, workspaceAnalysisFinalizerQueryError(err, "faithfulness review operation")
	}
	authority, err := gormLockWorkspaceAnalysisOperationAuthority(ctx, tx, operation)
	if err != nil {
		return workspaceAnalysisOperationRecord{}, workspaceAnalysisCallAuthority{}, err
	}
	if operation.Status != agentdomain.WorkspaceAnalysisOperationSucceeded ||
		operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallModel || operation.ModelCallID == nil ||
		operation.ResultKind == nil || *operation.ResultKind != agentdomain.WorkspaceAnalysisOperationResultModelCall ||
		!authority.Reservation.Found || authority.Reservation.Status != agentdomain.WorkspaceAnalysisBudgetSettled ||
		authority.ModelRunStatus == nil || *authority.ModelRunStatus != agentdomain.ModelRunSucceeded ||
		authority.ModelCallStatus == nil || *authority.ModelCallStatus != agentdomain.ModelCallSucceeded {
		return workspaceAnalysisOperationRecord{}, workspaceAnalysisCallAuthority{},
			consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("faithfulness review publication authority is incomplete"))
	}
	return operation, authority, nil
}

func gormLockWorkspaceAnalysisTerminationAuthority(
	ctx context.Context,
	tx *gorm.DB,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
	fence workspaceAnalysisFinalizationFence,
) (workspaceAnalysisTerminationAuthority, error) {
	if command.OperationID == nil {
		return workspaceAnalysisTerminationAuthority{}, nil
	}
	operation, err := scanWorkspaceAnalysisOperation(gormScanRow(tx.WithContext(ctx).Raw(`SELECT
		id::text,node_run_id::text,node_key,ordinal,operation_kind,call_kind,status,model_call_id::text,tool_call_id::text,
		result_kind,result_id::text,result_hash,error_code,created_at,updated_at,completed_at
		FROM agent.workspace_analysis_operation
		WHERE id=? AND analysis_run_id=? AND workspace_id=? AND workflow_run_id=?
		FOR UPDATE`, string(*command.OperationID), string(command.AnalysisRunID), string(command.WorkspaceID), string(command.WorkflowRunID))))
	if err != nil {
		return workspaceAnalysisTerminationAuthority{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis termination operation")
	}
	if !workspaceAnalysisTerminationOperationBinding(command, fence, operation) {
		return workspaceAnalysisTerminationAuthority{}, workspaceAnalysisTerminationAuthorityError("workspace analysis termination node binding drifted")
	}
	authority, err := gormLockWorkspaceAnalysisOperationAuthority(ctx, tx, operation)
	if err != nil {
		return workspaceAnalysisTerminationAuthority{}, err
	}
	if err := validateWorkspaceAnalysisTerminationAuthority(command, fence, operation, authority); err != nil {
		return workspaceAnalysisTerminationAuthority{}, err
	}
	return workspaceAnalysisTerminationAuthority{Operation: &operation, Call: authority}, nil
}

func gormLockWorkspaceAnalysisOperationAuthority(
	ctx context.Context,
	tx *gorm.DB,
	operation workspaceAnalysisOperationRecord,
) (workspaceAnalysisCallAuthority, error) {
	authority := workspaceAnalysisCallAuthority{LatestFactAt: operation.UpdatedAt}
	err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT status,settled_at FROM agent.workspace_analysis_budget_reservation
		WHERE operation_id=? FOR UPDATE`, string(operation.ID))).Scan(&authority.Reservation.Status, &authority.Reservation.SettledAt)
	switch {
	case err == nil:
		authority.Reservation.Found = true
		if authority.Reservation.SettledAt != nil {
			authority.LatestFactAt = laterWorkspaceAnalysisTime(authority.LatestFactAt, *authority.Reservation.SettledAt)
		}
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return workspaceAnalysisCallAuthority{}, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}

	if operation.ModelCallID != nil {
		var callModelRunID string
		var callStatus agentdomain.ModelCallStatus
		var callCompleted *time.Time
		if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT model_run_id::text,status,completed_at FROM agent.model_call
			WHERE id=? FOR UPDATE`, string(*operation.ModelCallID))).Scan(&callModelRunID, &callStatus, &callCompleted); err != nil {
			return workspaceAnalysisCallAuthority{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis model call")
		}
		parsedModelRunID, err := parseCanonicalID(callModelRunID)
		if err != nil {
			return workspaceAnalysisCallAuthority{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
		}
		var modelStatus agentdomain.ModelRunStatus
		var finalResult *string
		var modelCompleted *time.Time
		if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT status,final_result_type,completed_at FROM agent.model_run
			WHERE id=? FOR UPDATE`, string(parsedModelRunID))).Scan(&modelStatus, &finalResult, &modelCompleted); err != nil {
			return workspaceAnalysisCallAuthority{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis model run")
		}
		authority.ModelRunID, authority.ModelRunStatus = &parsedModelRunID, &modelStatus
		authority.ModelFinalResult, authority.ModelRunCompleted = finalResult, modelCompleted
		authority.ModelCallStatus, authority.ModelCallCompleted = &callStatus, callCompleted
		if modelCompleted != nil {
			authority.LatestFactAt = laterWorkspaceAnalysisTime(authority.LatestFactAt, *modelCompleted)
		}
		if callCompleted != nil {
			authority.LatestFactAt = laterWorkspaceAnalysisTime(authority.LatestFactAt, *callCompleted)
		}
	}
	if operation.ToolCallID != nil {
		var callStatus toolsdomain.CallStatus
		var callCompleted *time.Time
		if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT status,completed_at FROM workflow.tool_call
			WHERE id=? FOR UPDATE`, string(*operation.ToolCallID))).Scan(&callStatus, &callCompleted); err != nil {
			return workspaceAnalysisCallAuthority{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis tool call")
		}
		authority.ToolCallStatus, authority.ToolCallCompleted = &callStatus, callCompleted
		if callCompleted != nil {
			authority.LatestFactAt = laterWorkspaceAnalysisTime(authority.LatestFactAt, *callCompleted)
		}
	}
	return authority, nil
}

func gormLoadWorkspaceAnalysisSuccessFacts(
	ctx context.Context,
	tx *gorm.DB,
	command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
	fence workspaceAnalysisFinalizationFence,
	reviewOperation workspaceAnalysisOperationRecord,
	reviewAuthority workspaceAnalysisCallAuthority,
) (workspaceAnalysisSuccessFacts, error) {
	candidate, err := gormLoadWorkspaceAnalysisCandidate(ctx, tx, command.AnalysisRunID, command.CandidateID, command.CandidateHash)
	if err != nil {
		return workspaceAnalysisSuccessFacts{}, err
	}
	var gitReceipt toolsdomain.ResultReceipt
	if command.GitReceiptID != "" {
		gitReceipt, err = gormLoadWorkspaceAnalysisResultReceipt(ctx, tx, command.WorkspaceID, command.GitReceiptID, command.GitReceiptHash)
		if err != nil {
			return workspaceAnalysisSuccessFacts{}, err
		}
	}
	if candidate.SchemaVersion != fence.DefinitionVersion || candidate.WorkspaceID != command.WorkspaceID {
		return workspaceAnalysisSuccessFacts{}, workspaceAnalysisTerminationAuthorityError("workspace analysis candidate version or workspace differs")
	}
	if fence.DefinitionVersion == 2 {
		if err := gormValidateWorkspaceAnalysisV2SuccessFacts(ctx, tx, command); err != nil {
			return workspaceAnalysisSuccessFacts{}, err
		}
	}
	validationReceipt, err := gormLoadWorkspaceAnalysisResultReceipt(ctx, tx, command.WorkspaceID, command.ValidationReceiptID, command.ValidationReceiptHash)
	if err != nil {
		return workspaceAnalysisSuccessFacts{}, err
	}
	reviewResult, err := gormLoadWorkspaceAnalysisModelResult(ctx, tx, command.AnalysisRunID, command.ReviewModelResultID, command.ReviewModelResultHash)
	if err != nil {
		return workspaceAnalysisSuccessFacts{}, err
	}
	if reviewAuthority.ModelRunID == nil || reviewOperation.ResultID == nil || reviewOperation.ResultHash == nil ||
		reviewResult.OperationID != reviewOperation.ID || reviewResult.ModelRunID != *reviewAuthority.ModelRunID ||
		reviewResult.ModelCallID != *reviewOperation.ModelCallID || reviewResult.SubjectCandidateID == nil ||
		*reviewResult.SubjectCandidateID != candidate.ID || reviewResult.SubjectCandidateHash != candidate.DocumentHash ||
		reviewResult.ModelRunID == candidate.SynthesisModelRunID || candidate.AnalysisRunID != command.AnalysisRunID ||
		candidate.AnswerID != command.AnswerID {
		return workspaceAnalysisSuccessFacts{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis review and synthesis facts are split"))
	}
	review, err := agentdomain.DecodeFaithfulnessReview(reviewResult.Document, agentdomain.DefaultDecodeLimits())
	if err != nil || !review.Payload.Passed || review.ModelRunRef != reviewResult.ModelRunID {
		return workspaceAnalysisSuccessFacts{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis review did not produce a publishable decision"))
	}
	if (gitReceipt.ID != "" && gitReceipt.WorkflowRunID != command.WorkflowRunID) || validationReceipt.WorkflowRunID != command.WorkflowRunID {
		return workspaceAnalysisSuccessFacts{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis receipt workflow binding drifted"))
	}
	latest := laterWorkspaceAnalysisTime(fence.RunUpdatedAt, candidate.CreatedAt)
	latest = laterWorkspaceAnalysisTime(latest, gitReceipt.CreatedAt)
	latest = laterWorkspaceAnalysisTime(latest, validationReceipt.CreatedAt)
	latest = laterWorkspaceAnalysisTime(latest, reviewResult.CreatedAt)
	latest = laterWorkspaceAnalysisTime(latest, reviewAuthority.LatestFactAt)
	return workspaceAnalysisSuccessFacts{
		Candidate: candidate, GitReceipt: gitReceipt, ValidationReceipt: validationReceipt,
		ReviewResult: reviewResult, Review: review, LatestFactAt: latest,
	}, nil
}

func gormLoadWorkspaceAnalysisCandidate(
	ctx context.Context,
	tx *gorm.DB,
	analysisRunID, candidateID foundation.ID,
	candidateHash string,
) (agentdomain.WorkspaceAnalysisCandidate, error) {
	var (
		candidate                                                            agentdomain.WorkspaceAnalysisCandidate
		id, workspaceID, runID, answerID, operationID, attemptID, modelRunID string
	)
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT
		id::text,workspace_id::text,analysis_run_id::text,answer_id::text,synthesis_operation_id::text,
		node_attempt_id::text,synthesis_model_run_id::text,schema_id,schema_version,document,document_hash,document_bytes,created_at
		FROM agent.workspace_analysis_candidate
		WHERE id=? AND analysis_run_id=? AND document_hash=? FOR SHARE`, string(candidateID), string(analysisRunID), candidateHash)).Scan(
		&id, &workspaceID, &runID, &answerID, &operationID, &attemptID, &modelRunID,
		&candidate.SchemaID, &candidate.SchemaVersion, &candidate.Document, &candidate.DocumentHash, &candidate.DocumentBytes, &candidate.CreatedAt,
	); err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis candidate")
	}
	parsed, err := parseWorkspaceAnalysisIDs(id, workspaceID, runID, answerID, operationID, attemptID, modelRunID)
	if err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	candidate.ID, candidate.WorkspaceID, candidate.AnalysisRunID, candidate.AnswerID = parsed[0], parsed[1], parsed[2], parsed[3]
	candidate.SynthesisOperationID, candidate.NodeAttemptID, candidate.SynthesisModelRunID = parsed[4], parsed[5], parsed[6]
	if err := agentdomain.ValidateWorkspaceAnalysisCandidate(candidate); err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return candidate, nil
}

func gormLoadWorkspaceAnalysisModelResult(
	ctx context.Context,
	tx *gorm.DB,
	analysisRunID, resultID foundation.ID,
	resultHash string,
) (agentdomain.WorkspaceAnalysisModelResult, error) {
	var (
		result                                                                  agentdomain.WorkspaceAnalysisModelResult
		id, workspaceID, runID, operationID, attemptID, modelRunID, modelCallID string
		subjectCandidateID, subjectCandidateHash                                *string
	)
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT
		id::text,workspace_id::text,analysis_run_id::text,operation_id::text,node_attempt_id::text,
		model_run_id::text,model_call_id::text,operation_kind,schema_id,schema_version,
		subject_candidate_id::text,subject_candidate_hash,document,document_hash,document_bytes,created_at
		FROM agent.workspace_analysis_model_result
		WHERE id=? AND analysis_run_id=? AND document_hash=? FOR SHARE`, string(resultID), string(analysisRunID), resultHash)).Scan(
		&id, &workspaceID, &runID, &operationID, &attemptID, &modelRunID, &modelCallID,
		&result.OperationKind, &result.Schema.ID, &result.Schema.Version, &subjectCandidateID, &subjectCandidateHash,
		&result.Document, &result.DocumentHash, &result.DocumentBytes, &result.CreatedAt,
	); err != nil {
		return agentdomain.WorkspaceAnalysisModelResult{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis model result")
	}
	parsed, err := parseWorkspaceAnalysisIDs(id, workspaceID, runID, operationID, attemptID, modelRunID, modelCallID)
	if err != nil {
		return agentdomain.WorkspaceAnalysisModelResult{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	result.ID, result.WorkspaceID, result.AnalysisRunID, result.OperationID = parsed[0], parsed[1], parsed[2], parsed[3]
	result.NodeAttemptID, result.ModelRunID, result.ModelCallID = parsed[4], parsed[5], parsed[6]
	if subjectCandidateID != nil {
		value, parseErr := parseCanonicalID(*subjectCandidateID)
		if parseErr != nil {
			return agentdomain.WorkspaceAnalysisModelResult{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, parseErr)
		}
		result.SubjectCandidateID = &value
	}
	if subjectCandidateHash != nil {
		result.SubjectCandidateHash = *subjectCandidateHash
	}
	if err := agentdomain.ValidateWorkspaceAnalysisModelResult(result); err != nil {
		return agentdomain.WorkspaceAnalysisModelResult{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return result, nil
}

func gormLoadWorkspaceAnalysisResultReceipt(
	ctx context.Context,
	tx *gorm.DB,
	workspaceID, receiptID foundation.ID,
	receiptHash string,
) (toolsdomain.ResultReceipt, error) {
	var (
		receipt                                          toolsdomain.ResultReceipt
		id, toolCallID, storedWorkspaceID, workflowRunID string
		nodeRunID, nodeAttemptID                         string
		toolName, outputSchemaID, persistencePolicy      string
		privateSchemaID, privateHash                     *string
		privateSchemaVersion, privateBytes               *int64
		privateDocument                                  []byte
	)
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT
		id::text,tool_call_id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,
		tool_name,tool_version,output_schema_id,output_schema_version,definition_hash,persistence_policy,
		max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,
		server_binding_schema_id,server_binding_schema_version,server_binding_document,server_binding_hash,server_binding_bytes,created_at
		FROM workflow.tool_result_receipt
		WHERE id=? AND workspace_id=? AND output_hash=? FOR SHARE`, string(receiptID), string(workspaceID), receiptHash)).Scan(
		&id, &toolCallID, &storedWorkspaceID, &workflowRunID, &nodeRunID, &nodeAttemptID,
		&toolName, &receipt.Tool.Version, &outputSchemaID, &receipt.OutputSchema.Version, &receipt.DefinitionHash, &persistencePolicy,
		&receipt.MaxOutputBytes, &receipt.MaxPrivateBindingBytes, &receipt.Output, &receipt.OutputHash, &receipt.OutputBytes,
		&privateSchemaID, &privateSchemaVersion, &privateDocument, &privateHash, &privateBytes, &receipt.CreatedAt,
	); err != nil {
		return toolsdomain.ResultReceipt{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis result receipt")
	}
	parsed, err := parseWorkspaceAnalysisIDs(id, toolCallID, storedWorkspaceID, workflowRunID, nodeRunID, nodeAttemptID)
	if err != nil {
		return toolsdomain.ResultReceipt{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	receipt.ID, receipt.ToolCallID, receipt.WorkspaceID = parsed[0], parsed[1], parsed[2]
	receipt.WorkflowRunID, receipt.NodeRunID, receipt.NodeAttemptID = parsed[3], parsed[4], parsed[5]
	receipt.Tool.Name, receipt.OutputSchema.ID = toolName, outputSchemaID
	receipt.PersistencePolicy = toolsdomain.ResultPersistencePolicy(persistencePolicy)
	privatePresent := privateSchemaID != nil || privateSchemaVersion != nil || privateDocument != nil || privateHash != nil || privateBytes != nil
	if privatePresent {
		if privateSchemaID == nil || privateSchemaVersion == nil || privateDocument == nil || privateHash == nil || privateBytes == nil {
			return toolsdomain.ResultReceipt{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis receipt private binding is partially null"))
		}
		receipt.PrivateBinding = &toolsdomain.ResultReceiptPrivateBinding{
			Schema:   toolsdomain.SchemaRef{ID: *privateSchemaID, Version: *privateSchemaVersion},
			Document: append(json.RawMessage(nil), privateDocument...), Hash: *privateHash, Bytes: *privateBytes,
		}
	}
	if err := validateWorkspaceAnalysisLoadedReceipt(receipt); err != nil {
		return toolsdomain.ResultReceipt{}, err
	}
	return receipt, nil
}

func gormBuildWorkspaceAnalysisTerminationPublication(
	ctx context.Context,
	tx *gorm.DB,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
	fence workspaceAnalysisFinalizationFence,
	authority workspaceAnalysisTerminationAuthority,
) (workspaceAnalysisPublication, time.Time, error) {
	latestFactAt := fence.RunUpdatedAt
	if authority.Operation != nil {
		latestFactAt = laterWorkspaceAnalysisTime(latestFactAt, authority.Operation.UpdatedAt)
		latestFactAt = laterWorkspaceAnalysisTime(latestFactAt, authority.Call.LatestFactAt)
	}
	var modelRunID *foundation.ID
	switch command.Reason {
	case agentdomain.WorkspaceAnalysisRunEvidenceInsufficient:
		if fence.DefinitionVersion == 2 {
			createdAt, err := gormValidateWorkspaceAnalysisV2EvidenceInsufficient(ctx, tx, command, authority)
			if err != nil {
				return workspaceAnalysisPublication{}, time.Time{}, err
			}
			latestFactAt = laterWorkspaceAnalysisTime(latestFactAt, createdAt)
			break
		}
		receipt, err := gormLoadWorkspaceAnalysisResultReceipt(ctx, tx, command.WorkspaceID, command.Artifact.ID, command.Artifact.Hash)
		if err != nil {
			return workspaceAnalysisPublication{}, time.Time{}, err
		}
		selected, err := toolsdomain.SearchKnowledgeV2ReceiptSelectedRefs(receipt)
		if err != nil || receipt.Tool != (toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 2}) || len(selected) != 0 {
			return workspaceAnalysisPublication{}, time.Time{}, workspaceAnalysisTerminationAuthorityError("evidence-insufficient receipt is not an exact zero-hit search")
		}
		latestFactAt = laterWorkspaceAnalysisTime(latestFactAt, receipt.CreatedAt)
	case agentdomain.WorkspaceAnalysisRunCitationInvalid:
		receipt, err := gormLoadWorkspaceAnalysisResultReceipt(ctx, tx, command.WorkspaceID, command.Artifact.ID, command.Artifact.Hash)
		if err != nil {
			return workspaceAnalysisPublication{}, time.Time{}, err
		}
		candidate, decoded, err := gormLoadWorkspaceAnalysisCandidateForRun(ctx, tx, command.AnalysisRunID, command.AnswerID)
		if err != nil {
			return workspaceAnalysisPublication{}, time.Time{}, err
		}
		if candidate.SchemaVersion != fence.DefinitionVersion {
			return workspaceAnalysisPublication{}, time.Time{}, workspaceAnalysisTerminationAuthorityError("citation-invalid candidate version differs")
		}
		var results []toolsdomain.ValidateCitationV3ReceiptResult
		if fence.DefinitionVersion == 2 {
			results, err = toolsdomain.ValidateCitationV4ReceiptResults(receipt, candidate.ID, candidate.DocumentHash, decoded.Payload.CitationRefs)
		} else {
			results, err = toolsdomain.ValidateCitationV3ReceiptResults(receipt, candidate.ID, candidate.DocumentHash, decoded.Payload.CitationRefs)
		}
		if err != nil {
			return workspaceAnalysisPublication{}, time.Time{}, workspaceAnalysisTerminationAuthorityError("citation-invalid receipt binding is incomplete")
		}
		invalidResult := false
		for _, result := range results {
			invalidResult = invalidResult || !result.Valid
		}
		if !invalidResult {
			return workspaceAnalysisPublication{}, time.Time{}, conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("citation-invalid termination has no invalid citation"))
		}
		latestFactAt = laterWorkspaceAnalysisTime(latestFactAt, candidate.CreatedAt)
		latestFactAt = laterWorkspaceAnalysisTime(latestFactAt, receipt.CreatedAt)
	case agentdomain.WorkspaceAnalysisRunFaithfulnessRejected:
		result, err := gormLoadWorkspaceAnalysisModelResult(ctx, tx, command.AnalysisRunID, command.Artifact.ID, command.Artifact.Hash)
		if err != nil {
			return workspaceAnalysisPublication{}, time.Time{}, err
		}
		review, err := agentdomain.DecodeFaithfulnessReview(result.Document, agentdomain.DefaultDecodeLimits())
		if err != nil || review.Payload.Passed || review.ModelRunRef != result.ModelRunID || authority.Call.ModelRunID == nil ||
			result.ModelRunID != *authority.Call.ModelRunID || result.OperationID != authority.Operation.ID {
			return workspaceAnalysisPublication{}, time.Time{}, workspaceAnalysisTerminationAuthorityError("faithfulness rejection result is invalid")
		}
		latestFactAt = laterWorkspaceAnalysisTime(latestFactAt, result.CreatedAt)
	case agentdomain.WorkspaceAnalysisRunModelRefused:
		modelRunID = copyWorkspaceAnalysisFinalizerID(authority.Call.ModelRunID)
	case agentdomain.WorkspaceAnalysisRunNeedsClarification:
		result, err := gormLoadWorkspaceAnalysisModelResult(ctx, tx, command.AnalysisRunID, command.Artifact.ID, command.Artifact.Hash)
		if err != nil {
			return workspaceAnalysisPublication{}, time.Time{}, err
		}
		plan, err := agentdomain.DecodeWorkspaceAnalysisPlan(result.Document, agentdomain.DefaultDecodeLimits())
		if err != nil || !plan.Payload.RequiresClarification || plan.ModelRunRef != result.ModelRunID || authority.Call.ModelRunID == nil ||
			result.ModelRunID != *authority.Call.ModelRunID || result.OperationID != authority.Operation.ID {
			return workspaceAnalysisPublication{}, time.Time{}, workspaceAnalysisTerminationAuthorityError("clarification plan result is invalid")
		}
		modelRunID = copyWorkspaceAnalysisFinalizerID(&result.ModelRunID)
		publication, err := canonicalWorkspaceAnalysisClarificationPublication(plan)
		if err != nil {
			return workspaceAnalysisPublication{}, time.Time{}, err
		}
		latestFactAt = laterWorkspaceAnalysisTime(latestFactAt, result.CreatedAt)
		return publication, latestFactAt, nil
	case agentdomain.WorkspaceAnalysisRunReceiptInvalid:
		failure, err := gormLoadWorkspaceAnalysisReceiptFailure(ctx, tx, command.AnalysisRunID, authority.Operation.ID, command.ReceiptFailure.ID)
		if err != nil {
			return workspaceAnalysisPublication{}, time.Time{}, err
		}
		if failure.Code != command.ReceiptFailure.Code || failure.ExpectedHash != command.ReceiptFailure.ExpectedHash ||
			!equalOptionalWorkspaceAnalysisString(failure.ActualHash, command.ReceiptFailure.ActualHash) {
			return workspaceAnalysisPublication{}, time.Time{}, conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("receipt failure command differs from the durable failure"))
		}
		latestFactAt = laterWorkspaceAnalysisTime(latestFactAt, failure.CreatedAt)
	case agentdomain.WorkspaceAnalysisRunResultUnknown, agentdomain.WorkspaceAnalysisRunModelFailed:
		if authority.Operation != nil && authority.Operation.CallKind == agentdomain.WorkspaceAnalysisOperationCallModel {
			modelRunID = copyWorkspaceAnalysisFinalizerID(authority.Call.ModelRunID)
		}
	case agentdomain.WorkspaceAnalysisRunBudgetExhausted, agentdomain.WorkspaceAnalysisRunDeadlineExceeded,
		agentdomain.WorkspaceAnalysisRunToolFailed, agentdomain.WorkspaceAnalysisRunCancellation:
		// 这些原因不拥有公开 authoring Model Run。
	default:
		return workspaceAnalysisPublication{}, time.Time{}, invalid(conversationapplication.ErrorCodeWorkspaceAnalysisFinalizerInvalid, errors.New("workspace analysis termination reason is unsupported"))
	}
	publication, err := canonicalWorkspaceAnalysisTerminationPublication(command.Reason, modelRunID)
	if err != nil {
		return workspaceAnalysisPublication{}, time.Time{}, err
	}
	return publication, latestFactAt, nil
}

func gormLoadWorkspaceAnalysisCandidateForRun(
	ctx context.Context,
	tx *gorm.DB,
	analysisRunID, answerID foundation.ID,
) (agentdomain.WorkspaceAnalysisCandidate, agentdomain.WorkspaceAnalysisCandidateResult, error) {
	var candidateID string
	var candidateHash string
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT id::text,document_hash FROM agent.workspace_analysis_candidate
		WHERE analysis_run_id=? AND answer_id=? FOR SHARE`, string(analysisRunID), string(answerID))).Scan(&candidateID, &candidateHash); err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, agentdomain.WorkspaceAnalysisCandidateResult{},
			workspaceAnalysisFinalizerQueryError(err, "workspace analysis candidate")
	}
	parsedID, err := parseCanonicalID(candidateID)
	if err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, agentdomain.WorkspaceAnalysisCandidateResult{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	candidate, err := gormLoadWorkspaceAnalysisCandidate(ctx, tx, analysisRunID, parsedID, candidateHash)
	if err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, agentdomain.WorkspaceAnalysisCandidateResult{}, err
	}
	decoded, err := agentdomain.DecodeWorkspaceAnalysisCandidate(candidate.Document, agentdomain.DefaultDecodeLimits())
	if err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, agentdomain.WorkspaceAnalysisCandidateResult{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return candidate, decoded, nil
}

func gormLoadWorkspaceAnalysisReceiptFailure(
	ctx context.Context,
	tx *gorm.DB,
	analysisRunID, operationID, failureID foundation.ID,
) (workspaceAnalysisReceiptFailureRecord, error) {
	var record workspaceAnalysisReceiptFailureRecord
	var id, storedOperationID string
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT id::text,operation_id::text,failure_code,expected_output_hash,observed_output_hash,created_at
		FROM workflow.tool_result_receipt_failure
		WHERE id=? AND analysis_run_id=? AND operation_id=? FOR SHARE`, string(failureID), string(analysisRunID), string(operationID))).Scan(&id, &storedOperationID, &record.Code, &record.ExpectedHash, &record.ActualHash, &record.CreatedAt); err != nil {
		return workspaceAnalysisReceiptFailureRecord{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis receipt failure")
	}
	parsed, err := parseWorkspaceAnalysisIDs(id, storedOperationID)
	if err != nil {
		return workspaceAnalysisReceiptFailureRecord{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	record.ID, record.OperationID = parsed[0], parsed[1]
	return record, nil
}

func gormLockWorkspaceAnalysisPublicationSlot(
	ctx context.Context,
	tx *gorm.DB,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
	expectedAnswerVersion int64,
) (workspaceAnalysisPublicationSlot, error) {
	var slot workspaceAnalysisPublicationSlot
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT version,created_at,updated_at,last_activity_at
		FROM agent.conversation WHERE workspace_id=? AND id=? FOR UPDATE`, string(lookup.WorkspaceID), string(lookup.ConversationID))).Scan(&slot.ConversationVersion, &slot.ConversationCreatedAt, &slot.ConversationUpdatedAt, &slot.LastActivityAt); err != nil {
		return workspaceAnalysisPublicationSlot{}, workspaceAnalysisFinalizerQueryError(err, "conversation publication slot")
	}
	view, err := gormLoadBoundAnswer(ctx, tx, lookup.AnswerPublicationLookup, " FOR UPDATE OF a")
	if err != nil {
		return workspaceAnalysisPublicationSlot{}, err
	}
	if view.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending || view.Answer.Version != expectedAnswerVersion {
		return workspaceAnalysisPublicationSlot{}, conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis answer publication slot changed"))
	}
	slot.Answer = view.Answer
	var draftID, nodeAttemptID, status string
	err = gormScanRow(tx.WithContext(ctx).Raw(`SELECT id::text,node_attempt_id::text,status
		FROM agent.answer_draft_session
		WHERE workspace_id=? AND answer_id=? AND status IN ('ACTIVE','COMPLETED','DEGRADED')
		FOR UPDATE`, string(lookup.WorkspaceID), string(lookup.AnswerID))).Scan(&draftID, &nodeAttemptID, &status)
	switch {
	case err == nil:
		ids, parseErr := parseWorkspaceAnalysisIDs(draftID, nodeAttemptID)
		if parseErr != nil {
			return workspaceAnalysisPublicationSlot{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, parseErr)
		}
		slot.Draft = &workspaceAnalysisDraftRecord{ID: ids[0], NodeAttemptID: ids[1], Status: status}
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return workspaceAnalysisPublicationSlot{}, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return slot, nil
}

func gormWorkspaceAnalysisPublicationTime(
	ctx context.Context,
	tx *gorm.DB,
	fence workspaceAnalysisFinalizationFence,
	slot workspaceAnalysisPublicationSlot,
	latestFactAt time.Time,
) (time.Time, error) {
	var now time.Time
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT clock_timestamp()`)).Scan(&now); err != nil {
		return time.Time{}, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	floors := []time.Time{
		fence.RunCreatedAt, fence.RunUpdatedAt, latestFactAt, slot.ConversationCreatedAt,
		slot.ConversationUpdatedAt, slot.LastActivityAt, slot.Answer.CreatedAt, slot.Answer.UpdatedAt,
	}
	for _, floor := range floors {
		if now.Before(floor) {
			return time.Time{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis publication chronology is in the future"))
		}
	}
	if fence.AttemptLeaseUntil == nil || !now.Before(*fence.AttemptLeaseUntil) {
		return time.Time{}, conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis finalization lease expired before proof insertion"))
	}
	return now.UTC(), nil
}

func gormInsertWorkspaceAnalysisSuccessProof(ctx context.Context, tx *gorm.DB, proofID foundation.ID, command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand, publication workspaceAnalysisPublication, now time.Time) error {
	model := workspaceAnalysisSuccessProofModel{
		ID: string(proofID), WorkspaceID: string(command.WorkspaceID), AnalysisRunID: string(command.AnalysisRunID),
		AnswerID: string(command.AnswerID), WorkflowRunID: string(command.WorkflowRunID),
		FinalizationNodeRunID: string(command.NodeRunID), FinalizationNodeAttemptID: string(command.NodeAttemptID),
		CandidateID: string(command.CandidateID), CandidateHash: command.CandidateHash,
		ValidationReceiptID: string(command.ValidationReceiptID), ValidationReceiptHash: command.ValidationReceiptHash,
		ReviewModelResultID: string(command.ReviewModelResultID), ReviewDocumentHash: command.ReviewModelResultHash,
		PublishedDocument: []byte(publication.Document), PublishedResultHash: publication.ResultHash,
		PublishedBytes: int64(len(publication.Document)), CreatedAt: now,
	}
	if command.GitReceiptID != "" {
		id, hash := string(command.GitReceiptID), command.GitReceiptHash
		model.GitReceiptID, model.GitReceiptHash = &id, &hash
	}
	if err := tx.WithContext(ctx).Create(&model).Error; err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

func gormInsertWorkspaceAnalysisTerminationProof(ctx context.Context, tx *gorm.DB, proofID foundation.ID, command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand, publication workspaceAnalysisPublication, now time.Time) error {
	attemptID := string(command.NodeAttemptID)
	model := workspaceAnalysisTerminationProofModel{
		ID: string(proofID), WorkspaceID: string(command.WorkspaceID), AnalysisRunID: string(command.AnalysisRunID),
		AnswerID: string(command.AnswerID), WorkflowRunID: string(command.WorkflowRunID),
		TerminalNodeRunID: string(command.NodeRunID), TerminalNodeAttemptID: &attemptID, Reason: string(command.Reason),
		PublishedDocument: []byte(publication.Document), PublishedResultHash: publication.ResultHash,
		PublishedBytes: int64(len(publication.Document)), CheckedAt: now, CreatedAt: now,
	}
	if command.OperationID != nil {
		value := string(*command.OperationID)
		model.OperationID = &value
	}
	if command.Artifact != nil {
		kind, id := string(command.Artifact.Kind), string(command.Artifact.ID)
		model.ArtifactKind, model.ArtifactID, model.ArtifactHash = &kind, &id, &command.Artifact.Hash
	}
	if command.BudgetRequest != nil {
		request := command.BudgetRequest
		model.RequestedModelCalls, model.RequestedToolCalls, model.RequestedSourceReads = &request.ModelCalls, &request.ToolCalls, &request.SourceReads
		model.RequestedInputTokens, model.RequestedOutputTokens = &request.InputTokens, &request.OutputTokens
		model.RequestedCostMicrounits = request.RequestedCostMicrounits
	}
	if command.ReceiptFailure != nil {
		failure := command.ReceiptFailure
		code, id := string(failure.Code), string(failure.ID)
		model.ReceiptFailureCode, model.ReceiptFailureID = &code, &id
		model.ExpectedHash, model.ActualHash = &failure.ExpectedHash, failure.ActualHash
	}
	if publication.ModelRunID != nil {
		value := string(*publication.ModelRunID)
		model.PublishedModelRunID = &value
	}
	if err := tx.WithContext(ctx).Create(&model).Error; err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

func gormPublishWorkspaceAnalysisDraft(ctx context.Context, tx *gorm.DB, draft *workspaceAnalysisDraftRecord, now time.Time) error {
	if draft == nil {
		return nil
	}
	tag := tx.WithContext(ctx).Exec(`UPDATE agent.answer_draft_session
		SET status='PUBLISHED',updated_at=?,completed_at=COALESCE(completed_at,?)
		WHERE id=? AND status IN ('COMPLETED','DEGRADED')`, now, now, string(draft.ID))
	if err := tag.Error; err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if tag.RowsAffected != 1 {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis draft publication CAS failed"))
	}
	return nil
}

func gormAbortWorkspaceAnalysisDraft(ctx context.Context, tx *gorm.DB, draft *workspaceAnalysisDraftRecord, now time.Time) error {
	if draft == nil {
		return nil
	}
	tag := tx.WithContext(ctx).Exec(`UPDATE agent.answer_draft_session
		SET status='ABORTED',updated_at=?,completed_at=COALESCE(completed_at,?)
		WHERE id=? AND status IN ('ACTIVE','COMPLETED','DEGRADED')`, now, now, string(draft.ID))
	if err := tag.Error; err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if tag.RowsAffected != 1 {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis draft abort CAS failed"))
	}
	return nil
}

func gormUpdateWorkspaceAnalysisRunSuccess(
	ctx context.Context,
	tx *gorm.DB,
	fence workspaceAnalysisFinalizationFence,
	facts workspaceAnalysisSuccessFacts,
	now time.Time,
) error {
	tag := tx.WithContext(ctx).Exec(`UPDATE agent.workspace_analysis_run SET
		status='succeeded',termination_reason='COMPLETED',validation_receipt_id=?,review_model_run_id=?,
		version=version+1,updated_at=?,completed_at=?
		WHERE id=? AND status='running' AND version=?`, string(facts.ValidationReceipt.ID), string(facts.ReviewResult.ModelRunID), now, now, string(facts.Candidate.AnalysisRunID), fence.RunVersion)
	if err := tag.Error; err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if tag.RowsAffected != 1 {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis success run CAS failed"))
	}
	return nil
}

func gormUpdateWorkspaceAnalysisRunTermination(
	ctx context.Context,
	tx *gorm.DB,
	fence workspaceAnalysisFinalizationFence,
	publication workspaceAnalysisPublication,
	now time.Time,
) error {
	tag := tx.WithContext(ctx).Exec(`UPDATE agent.workspace_analysis_run SET
		status=?,termination_reason=?,validation_receipt_id=NULL,review_model_run_id=NULL,
		version=version+1,updated_at=?,completed_at=?
		WHERE id=? AND status=? AND version=?`, string(publication.RunStatus), string(publication.Reason), now, now, string(fence.RunID), string(fence.RunStatus), fence.RunVersion)
	if err := tag.Error; err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if tag.RowsAffected != 1 {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis termination run CAS failed"))
	}
	return nil
}

func gormUpdateWorkspaceAnalysisAnswer(
	ctx context.Context,
	tx *gorm.DB,
	pending conversationdomain.Answer,
	publication workspaceAnalysisPublication,
	now time.Time,
) (conversationdomain.Answer, error) {
	answer := pending
	answer.ModelRunID = copyWorkspaceAnalysisFinalizerID(publication.ModelRunID)
	answer.PublicationStatus, answer.ResultType = publication.Status, publication.ResultType
	answer.Result = append(json.RawMessage(nil), publication.Document...)
	answer.ResultHash, answer.RetrievalSummary = publication.ResultHash, nil
	answer.Version++
	answer.UpdatedAt, answer.PublishedAt = now, &now
	if err := conversationdomain.ValidateAnswer(answer); err != nil {
		return conversationdomain.Answer{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	var modelRunID any
	if answer.ModelRunID != nil {
		modelRunID = string(*answer.ModelRunID)
	}
	tag := tx.WithContext(ctx).Model(&answerModel{}).
		Where("workspace_id=? AND id=? AND publication_status='pending' AND version=?", string(answer.WorkspaceID), string(answer.ID), pending.Version).
		Updates(map[string]any{
			"model_run_id": modelRunID, "publication_status": string(answer.PublicationStatus),
			"result_type": string(answer.ResultType), "result": conversationJSONB(answer.Result),
			"result_hash": answer.ResultHash, "retrieval_summary": nil, "version": gorm.Expr("version + 1"),
			"updated_at": now, "published_at": now,
		})
	if err := tag.Error; err != nil {
		return conversationdomain.Answer{}, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if tag.RowsAffected != 1 {
		return conversationdomain.Answer{}, conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis answer publication CAS failed"))
	}
	return answer, nil
}

func gormUpdateWorkspaceAnalysisConversation(
	ctx context.Context,
	tx *gorm.DB,
	slot workspaceAnalysisPublicationSlot,
	now time.Time,
) error {
	tag := tx.WithContext(ctx).Model(&conversationModel{}).
		Where("workspace_id=? AND id=? AND version=?", string(slot.Answer.WorkspaceID), string(slot.Answer.ConversationID), slot.ConversationVersion).
		Updates(map[string]any{"version": gorm.Expr("version + 1"), "last_activity_at": now, "updated_at": now})
	if err := tag.Error; err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if tag.RowsAffected != 1 {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis conversation activity CAS failed"))
	}
	return nil
}

func gormForceWorkspaceAnalysisPublicationConstraints(ctx context.Context, tx *gorm.DB) error {
	if err := tx.WithContext(ctx).Exec(`SET CONSTRAINTS ALL IMMEDIATE`).Error; err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

func gormLoadWorkspaceAnalysisProofState(
	ctx context.Context,
	tx *gorm.DB,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
	answer conversationdomain.Answer,
) (workspaceAnalysisProofState, error) {
	state := workspaceAnalysisProofState{Answer: answer}
	var runReason *string
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT status,termination_reason FROM agent.workspace_analysis_run
		WHERE id=? AND workspace_id=? AND conversation_id=? AND question_id=? AND answer_id=? AND workflow_run_id=?`, string(lookup.AnalysisRunID), string(lookup.WorkspaceID), string(lookup.ConversationID), string(lookup.QuestionID), string(lookup.AnswerID), string(lookup.WorkflowRunID))).Scan(&state.RunStatus, &runReason); err != nil {
		return workspaceAnalysisProofState{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis publication state")
	}
	if runReason != nil {
		reason := agentdomain.WorkspaceAnalysisRunTerminationReason(*runReason)
		state.RunReason = &reason
	}
	success, found, err := gormLoadWorkspaceAnalysisSuccessProof(ctx, tx, lookup)
	if err != nil {
		return workspaceAnalysisProofState{}, err
	}
	if found {
		state.Success = &success
	}
	termination, found, err := gormLoadWorkspaceAnalysisTerminationProof(ctx, tx, lookup)
	if err != nil {
		return workspaceAnalysisProofState{}, err
	}
	if found {
		state.Termination = &termination
	}
	return state, nil
}

func gormLoadWorkspaceAnalysisSuccessProof(
	ctx context.Context,
	tx *gorm.DB,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) (workspaceAnalysisSuccessProofRecord, bool, error) {
	var record workspaceAnalysisSuccessProofRecord
	var id, nodeRunID, nodeAttemptID, candidateID, validationReceiptID, reviewResultID string
	var gitReceiptID, gitReceiptHash *string
	err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT
		id::text,finalization_node_run_id::text,finalization_node_attempt_id::text,
		candidate_id::text,candidate_hash,git_receipt_id::text,git_receipt_hash,
		validation_receipt_id::text,validation_receipt_hash,review_model_result_id::text,review_document_hash,
		published_document,published_result_hash
		FROM agent.workspace_analysis_publication_proof
		WHERE analysis_run_id=? AND workspace_id=? AND answer_id=? AND workflow_run_id=?`, string(lookup.AnalysisRunID), string(lookup.WorkspaceID), string(lookup.AnswerID), string(lookup.WorkflowRunID))).Scan(
		&id, &nodeRunID, &nodeAttemptID, &candidateID, &record.CandidateHash, &gitReceiptID, &gitReceiptHash,
		&validationReceiptID, &record.ValidationReceiptHash, &reviewResultID, &record.ReviewResultHash,
		&record.PublishedDocument, &record.PublishedResultHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return workspaceAnalysisSuccessProofRecord{}, false, nil
	}
	if err != nil {
		return workspaceAnalysisSuccessProofRecord{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	ids, err := parseWorkspaceAnalysisIDs(id, nodeRunID, nodeAttemptID, candidateID, validationReceiptID, reviewResultID)
	if err != nil {
		return workspaceAnalysisSuccessProofRecord{}, false, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	record.ID, record.NodeRunID, record.NodeAttemptID, record.CandidateID = ids[0], ids[1], ids[2], ids[3]
	record.ValidationReceiptID, record.ReviewResultID = ids[4], ids[5]
	if (gitReceiptID == nil) != (gitReceiptHash == nil) || (gitReceiptID == nil && lookup.DefinitionVersion != 2) {
		return workspaceAnalysisSuccessProofRecord{}, false, workspaceAnalysisTerminationAuthorityError("workspace analysis Git proof nullable pair differs")
	}
	if gitReceiptID != nil {
		parsed, err := parseCanonicalID(*gitReceiptID)
		if err != nil || len(*gitReceiptHash) != 64 {
			return workspaceAnalysisSuccessProofRecord{}, false, workspaceAnalysisTerminationAuthorityError("workspace analysis Git proof is invalid")
		}
		record.GitReceiptID, record.GitReceiptHash = parsed, *gitReceiptHash
	}
	return record, true, nil
}

func gormLoadWorkspaceAnalysisTerminationProof(
	ctx context.Context,
	tx *gorm.DB,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) (workspaceAnalysisTerminationProofRecord, bool, error) {
	var (
		record                                                  workspaceAnalysisTerminationProofRecord
		id, nodeRunID                                           string
		nodeAttemptID                                           *string
		operationID, artifactID, failureID, publishedModelRunID *string
		artifactKind, failureCode                               *string
	)
	err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT
		id::text,terminal_node_run_id::text,terminal_node_attempt_id::text,reason,operation_id::text,
		artifact_kind,artifact_id::text,artifact_hash,
		requested_model_calls,requested_tool_calls,requested_source_reads,requested_input_tokens,requested_output_tokens,requested_cost_microunits,
		receipt_failure_code,receipt_failure_id::text,expected_hash,actual_hash,published_model_run_id::text,
		published_document,published_result_hash
		FROM agent.workspace_analysis_termination_proof
		WHERE analysis_run_id=? AND workspace_id=? AND answer_id=? AND workflow_run_id=?`, string(lookup.AnalysisRunID), string(lookup.WorkspaceID), string(lookup.AnswerID), string(lookup.WorkflowRunID))).Scan(
		&id, &nodeRunID, &nodeAttemptID, &record.Reason, &operationID,
		&artifactKind, &artifactID, &record.ArtifactHash,
		&record.RequestedModelCalls, &record.RequestedToolCalls, &record.RequestedSourceReads,
		&record.RequestedInputTokens, &record.RequestedOutputTokens, &record.RequestedCost,
		&failureCode, &failureID, &record.ExpectedHash, &record.ActualHash, &publishedModelRunID,
		&record.PublishedDocument, &record.PublishedResultHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return workspaceAnalysisTerminationProofRecord{}, false, nil
	}
	if err != nil {
		return workspaceAnalysisTerminationProofRecord{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	ids, err := parseWorkspaceAnalysisIDs(id, nodeRunID)
	if err != nil {
		return workspaceAnalysisTerminationProofRecord{}, false, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	record.ID, record.NodeRunID = ids[0], ids[1]
	if nodeAttemptID != nil {
		value, parseErr := parseCanonicalID(*nodeAttemptID)
		if parseErr != nil {
			return workspaceAnalysisTerminationProofRecord{}, false, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, parseErr)
		}
		record.NodeAttemptID = value
	}
	if operationID != nil {
		value, parseErr := parseCanonicalID(*operationID)
		if parseErr != nil {
			return workspaceAnalysisTerminationProofRecord{}, false, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, parseErr)
		}
		record.OperationID = &value
	}
	if artifactID != nil {
		value, parseErr := parseCanonicalID(*artifactID)
		if parseErr != nil {
			return workspaceAnalysisTerminationProofRecord{}, false, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, parseErr)
		}
		record.ArtifactID = &value
	}
	if artifactKind != nil {
		value := conversationapplication.WorkspaceAnalysisTerminationArtifactKind(*artifactKind)
		record.ArtifactKind = &value
	}
	if failureID != nil {
		value, parseErr := parseCanonicalID(*failureID)
		if parseErr != nil {
			return workspaceAnalysisTerminationProofRecord{}, false, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, parseErr)
		}
		record.FailureID = &value
	}
	if failureCode != nil {
		value := conversationapplication.WorkspaceAnalysisReceiptFailureCode(*failureCode)
		record.FailureCode = &value
	}
	if publishedModelRunID != nil {
		value, parseErr := parseCanonicalID(*publishedModelRunID)
		if parseErr != nil {
			return workspaceAnalysisTerminationProofRecord{}, false, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, parseErr)
		}
		record.PublishedModelRunID = &value
	}
	return record, true, nil
}

func gormLockWorkspaceAnalysisTerminalPublication(
	ctx context.Context,
	tx *gorm.DB,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) (workspaceAnalysisProofState, error) {
	var conversationVersion int64
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT version FROM agent.conversation
		WHERE workspace_id=? AND id=? FOR UPDATE`, string(lookup.WorkspaceID), string(lookup.ConversationID))).Scan(&conversationVersion); err != nil {
		return workspaceAnalysisProofState{}, workspaceAnalysisFinalizerQueryError(err, "terminal conversation publication slot")
	}
	view, err := gormLoadBoundAnswer(ctx, tx, lookup.AnswerPublicationLookup, " FOR UPDATE OF a")
	if err != nil {
		return workspaceAnalysisProofState{}, err
	}
	if err := gormRejectActiveWorkspaceAnalysisDraft(ctx, tx, lookup.WorkspaceID, lookup.AnswerID); err != nil {
		return workspaceAnalysisProofState{}, err
	}
	return gormLoadWorkspaceAnalysisProofState(ctx, tx, lookup, view.Answer)
}

func gormRejectActiveWorkspaceAnalysisDraft(
	ctx context.Context,
	tx *gorm.DB,
	workspaceID, answerID foundation.ID,
) error {
	var activeDraftID string
	err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT id::text FROM agent.answer_draft_session
		WHERE workspace_id=? AND answer_id=? AND status IN ('ACTIVE','COMPLETED','DEGRADED') FOR UPDATE`, string(workspaceID), string(answerID))).Scan(&activeDraftID)
	if err == nil {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("terminal workspace analysis retained an active draft"))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

func gormValidateRetainedWorkspaceAnalysisDraft(
	ctx context.Context,
	tx *gorm.DB,
	workspaceID, answerID foundation.ID,
	expectedStatus string,
	candidateID *foundation.ID,
) error {
	var publishedCount, matchingPublishedCount int64
	if candidateID == nil {
		if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT count(*) FROM agent.answer_draft_session
			WHERE workspace_id=? AND answer_id=? AND status='PUBLISHED'`, string(workspaceID), string(answerID))).Scan(&publishedCount); err != nil {
			return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
		}
	} else {
		if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT count(*),count(*) FILTER (
			WHERE node_attempt_id=(SELECT node_attempt_id FROM agent.workspace_analysis_candidate WHERE id=?)
		) FROM agent.answer_draft_session
		WHERE workspace_id=? AND answer_id=? AND status='PUBLISHED'`, string(*candidateID), string(workspaceID), string(answerID))).Scan(&publishedCount, &matchingPublishedCount); err != nil {
			return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
		}
	}
	if (expectedStatus == "ABORTED" && publishedCount != 0) ||
		(expectedStatus == "PUBLISHED" && publishedCount != matchingPublishedCount) || publishedCount > 1 {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis draft publication is split"))
	}

	query := `SELECT draft.status
		FROM agent.answer_draft_session AS draft
		WHERE draft.workspace_id=? AND draft.answer_id=? AND draft.status IN ('PUBLISHED','ABORTED')`
	arguments := []any{string(workspaceID), string(answerID)}
	if candidateID != nil {
		query += ` AND draft.node_attempt_id=(SELECT node_attempt_id FROM agent.workspace_analysis_candidate WHERE id=?)`
		arguments = append(arguments, string(*candidateID))
	}
	query += ` ORDER BY draft.generation DESC LIMIT 1 FOR SHARE OF draft`
	var status string
	err := gormScanRow(tx.WithContext(ctx).Raw(query, arguments...)).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if status != expectedStatus {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("retained workspace analysis draft has a split terminal status"))
	}
	return nil
}

func (finalizer *GORMWorkspaceAnalysisFinalizer) replaySuccessScoped(
	ctx context.Context,
	tx *gorm.DB,
	scope foundation.TransactionScope,
	command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, error) {
	state, err := gormLockWorkspaceAnalysisTerminalPublication(ctx, tx, command.WorkspaceAnalysisPublicationLookup)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	output, found, err := workspaceAnalysisOutputFromState(command.WorkspaceAnalysisPublicationLookup, state)
	if err != nil || !found || state.Success == nil || state.Termination != nil {
		if err == nil {
			err = errors.New("workspace analysis success replay has no success proof")
		}
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, err)
	}
	proof := state.Success
	if state.Answer.Version != command.ExpectedAnswerVersion+1 || proof.CandidateID != command.CandidateID ||
		proof.CandidateHash != command.CandidateHash || proof.GitReceiptID != command.GitReceiptID ||
		proof.GitReceiptHash != command.GitReceiptHash || proof.ValidationReceiptID != command.ValidationReceiptID ||
		proof.ValidationReceiptHash != command.ValidationReceiptHash || proof.ReviewResultID != command.ReviewModelResultID ||
		proof.ReviewResultHash != command.ReviewModelResultHash {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{},
			conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis success replay differs from the durable proof"))
	}
	if err := gormValidateRetainedWorkspaceAnalysisDraft(ctx, tx, command.WorkspaceID, command.AnswerID, "PUBLISHED", &proof.CandidateID); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	citationCount, err := workspaceAnalysisTerminalCitationCount(state.Answer)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	if err := finalizer.appendWorkspaceAnalysisTerminalEvents(
		ctx, scope, command.AnalysisRunID, state.RunStatus, state.Answer, citationCount, *state.Answer.PublishedAt, true,
	); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	return output, nil
}

func (finalizer *GORMWorkspaceAnalysisFinalizer) replayTerminationScoped(
	ctx context.Context,
	tx *gorm.DB,
	scope foundation.TransactionScope,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, error) {
	state, err := gormLockWorkspaceAnalysisTerminalPublication(ctx, tx, command.WorkspaceAnalysisPublicationLookup)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	output, found, err := workspaceAnalysisOutputFromState(command.WorkspaceAnalysisPublicationLookup, state)
	if err != nil || !found || state.Termination == nil || state.Success != nil {
		if err == nil {
			err = errors.New("workspace analysis termination replay has no termination proof")
		}
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, err)
	}
	proof := state.Termination
	if state.Answer.Version != command.ExpectedAnswerVersion+1 || proof.Reason != command.Reason ||
		!equalOptionalWorkspaceAnalysisID(proof.OperationID, command.OperationID) || !workspaceAnalysisTerminationArtifactMatchesProof(command.Artifact, proof) ||
		!workspaceAnalysisBudgetRequestMatchesProof(command.BudgetRequest, proof) || !workspaceAnalysisReceiptFailureMatchesProof(command.ReceiptFailure, proof) {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{},
			conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis termination replay differs from the durable proof"))
	}
	if err := gormValidateRetainedWorkspaceAnalysisDraft(ctx, tx, command.WorkspaceID, command.AnswerID, "ABORTED", nil); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	citationCount, err := workspaceAnalysisTerminalCitationCount(state.Answer)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	if err := finalizer.appendWorkspaceAnalysisTerminalEvents(
		ctx, scope, command.AnalysisRunID, state.RunStatus, state.Answer, citationCount, *state.Answer.PublishedAt, true,
	); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	return output, nil
}

func (finalizer *GORMWorkspaceAnalysisFinalizer) appendWorkspaceAnalysisTerminalEvents(
	ctx context.Context,
	scope foundation.TransactionScope,
	analysisRunID foundation.ID,
	runStatus agentdomain.WorkspaceAnalysisRunStatus,
	answer conversationdomain.Answer,
	citationCount int64,
	now time.Time,
	wantReplay bool,
) error {
	if err := appendScopedWorkspaceAnalysisTerminalEvents(
		ctx, scope, finalizer.events, analysisRunID, runStatus, answer, citationCount, now, wantReplay,
	); err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

func appendScopedWorkspaceAnalysisTerminalEvents(
	ctx context.Context,
	scope foundation.TransactionScope,
	appender eventsapplication.ScopedAppender,
	analysisRunID foundation.ID,
	runStatus agentdomain.WorkspaceAnalysisRunStatus,
	answer conversationdomain.Answer,
	citationCount int64,
	now time.Time,
	wantReplay bool,
) error {
	for _, request := range workspaceAnalysisTerminalEventRequests(answer, analysisRunID, runStatus, citationCount, now) {
		_, replayed, err := appender.AppendScoped(ctx, scope, request)
		if err != nil {
			return err
		}
		if replayed != wantReplay {
			return consistency(
				ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
				fmt.Errorf("workspace analysis terminal event %q replay=%t, want %t", request.Type, replayed, wantReplay),
			)
		}
	}
	return nil
}

func (finalizer *GORMWorkspaceAnalysisFinalizer) recoverWorkspaceAnalysisCommit(
	ctx context.Context,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
	expectedProofID foundation.ID,
	commitErr error,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceAnalysisCommitRecoveryTimeout)
	defer cancel()
	output, found, lookupErr := finalizer.LookupPublication(recoveryCtx, lookup)
	if lookupErr == nil && found && output.ProofID == expectedProofID {
		return output, false, nil
	}
	cause := fmt.Errorf("workspace analysis commit acknowledgement is unknown: %w", commitErr)
	if lookupErr != nil {
		cause = fmt.Errorf("%w; exact publication lookup failed: %v", cause, lookupErr)
	} else if found {
		cause = fmt.Errorf("%w; exact publication lookup found a different proof", cause)
	} else {
		cause = fmt.Errorf("%w; exact publication lookup found no terminal proof", cause)
	}
	return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
		foundation.NewError(foundation.ErrorManualRecoveryRequired, conversationapplication.ErrorCodeWorkspaceAnalysisFinalizationUnknown, false, cause)
}
