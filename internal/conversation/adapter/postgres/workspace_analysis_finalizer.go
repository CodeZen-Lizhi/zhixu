package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/jackc/pgx/v5"
)

const (
	ErrorCodeWorkspaceAnalysisFinalizeConflict    = "CONVERSATION_WORKSPACE_ANALYSIS_FINALIZE_CONFLICT"
	ErrorCodeWorkspaceAnalysisFinalizeCorrupt     = "CONVERSATION_WORKSPACE_ANALYSIS_FINALIZE_CORRUPT"
	ErrorCodeWorkspaceAnalysisFinalizeUnavailable = "CONVERSATION_WORKSPACE_ANALYSIS_FINALIZE_UNAVAILABLE"

	workspaceAnalysisCommitRecoveryTimeout = 5 * time.Second
)

// WorkspaceAnalysisFinalizer 在单个 PostgreSQL 事务中关闭工作区分析 proof、Answer 与通知事实。
type WorkspaceAnalysisFinalizer struct {
	db             DB
	events         eventsapplication.Appender
	ids            foundation.IDGenerator
	audit          WorkspaceAnalysisAuditRecorder
	workerActorRef string
}

// NewWorkspaceAnalysisFinalizer 构造只信任持久事实的工作区分析发布器。
func NewWorkspaceAnalysisFinalizer(db DB, events eventsapplication.Appender, ids foundation.IDGenerator) (*WorkspaceAnalysisFinalizer, error) {
	return newWorkspaceAnalysisFinalizer(db, events, ids, nil, "")
}

// NewWorkspaceAnalysisFinalizerWithAudit 构造带事务内终态审计的工作区分析发布器。
func NewWorkspaceAnalysisFinalizerWithAudit(
	db DB,
	events eventsapplication.Appender,
	ids foundation.IDGenerator,
	audit WorkspaceAnalysisAuditRecorder,
	workerActorRef string,
) (*WorkspaceAnalysisFinalizer, error) {
	if isNilInterface(audit) || !validWorkspaceAnalysisAuditActorRef(workerActorRef) {
		return nil, dependency(ErrorCodeWorkspaceAnalysisFinalizeUnavailable, errors.New("workspace analysis finalizer audit dependency is invalid"))
	}
	return newWorkspaceAnalysisFinalizer(db, events, ids, audit, workerActorRef)
}

func newWorkspaceAnalysisFinalizer(
	db DB,
	events eventsapplication.Appender,
	ids foundation.IDGenerator,
	audit WorkspaceAnalysisAuditRecorder,
	workerActorRef string,
) (*WorkspaceAnalysisFinalizer, error) {
	if isNilInterface(db) || isNilInterface(events) || isNilInterface(ids) {
		return nil, dependency(ErrorCodeWorkspaceAnalysisFinalizeUnavailable, errors.New("workspace analysis finalizer dependency is nil"))
	}
	return &WorkspaceAnalysisFinalizer{db: db, events: events, ids: ids, audit: audit, workerActorRef: workerActorRef}, nil
}

// LookupPublication 返回 exact 已发布 proof 回执；pending 只在当前 fence 完整时返回 found=false。
func (finalizer *WorkspaceAnalysisFinalizer) LookupPublication(
	ctx context.Context,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	if finalizer == nil || isNilInterface(finalizer.db) {
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
	tx, err := finalizer.db.Begin(ctx)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	fence, err := lockWorkspaceAnalysisFinalizationFence(ctx, tx, lookup)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := validateCurrentWorkspaceAnalysisLease(fence, lookup); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	var lockedConversation int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM agent.conversation
		WHERE workspace_id=$1 AND id=$2 FOR SHARE`, string(lookup.WorkspaceID), string(lookup.ConversationID)).Scan(&lockedConversation); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
			workspaceAnalysisFinalizerQueryError(err, "workspace analysis lookup conversation")
	}
	answerView, err := loadBoundAnswer(ctx, tx, lookup.AnswerPublicationLookup, " FOR SHARE OF a")
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if answerView.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending {
		if err := rejectActiveWorkspaceAnalysisDraft(ctx, tx, lookup.WorkspaceID, lookup.AnswerID); err != nil {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
		}
	}
	state, err := loadWorkspaceAnalysisProofState(ctx, tx, lookup, answerView.Answer)
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
			ctx, tx, lookup.AnalysisRunID, state.RunStatus, state.Answer, citationCount, *state.Answer.PublishedAt, true,
		); err != nil {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
		}
		switch {
		case state.Success != nil:
			if err := validateRetainedWorkspaceAnalysisDraft(ctx, tx, lookup.WorkspaceID, lookup.AnswerID, "PUBLISHED", &state.Success.CandidateID); err != nil {
				return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
			}
		case state.Termination != nil:
			if err := validateRetainedWorkspaceAnalysisDraft(ctx, tx, lookup.WorkspaceID, lookup.AnswerID, "ABORTED", nil); err != nil {
				return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return output, found, nil
}

// FinalizeSuccess 原子发布从 Candidate、receipt、Review 和 Run 重新派生的成功正文。
func (finalizer *WorkspaceAnalysisFinalizer) FinalizeSuccess(
	ctx context.Context,
	command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	if finalizer == nil || isNilInterface(finalizer.db) || isNilInterface(finalizer.events) || isNilInterface(finalizer.ids) {
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
	tx, err := finalizer.db.Begin(ctx)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	fence, err := lockWorkspaceAnalysisFinalizationFence(ctx, tx, command.WorkspaceAnalysisPublicationLookup)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := validateCurrentWorkspaceAnalysisLease(fence, command.WorkspaceAnalysisPublicationLookup); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if fence.RunStatus.Terminal() {
		output, replayErr := finalizer.replaySuccessTx(ctx, tx, command)
		if replayErr != nil {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, replayErr
		}
		if err := tx.Commit(ctx); err != nil {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
		}
		return output, true, nil
	}
	if err := validateActiveSuccessFence(fence, command.WorkspaceAnalysisPublicationLookup); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}

	reviewOperation, reviewAuthority, err := lockWorkspaceAnalysisSuccessAuthority(ctx, tx, command)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	facts, err := loadWorkspaceAnalysisSuccessFacts(ctx, tx, command, fence, reviewOperation, reviewAuthority)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	publication, err := buildWorkspaceAnalysisSuccessPublication(command.WorkspaceID, fence, facts)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	slot, err := lockWorkspaceAnalysisPublicationSlot(ctx, tx, command.WorkspaceAnalysisPublicationLookup, command.ExpectedAnswerVersion)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := validateSuccessDraft(slot.Draft, facts.Candidate); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	now, err := workspaceAnalysisPublicationTime(ctx, tx, fence, slot, facts.LatestFactAt)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	proofID, err := finalizer.ids.New()
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if err := insertWorkspaceAnalysisSuccessProof(ctx, tx, proofID, command, publication, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := publishWorkspaceAnalysisDraft(ctx, tx, slot.Draft, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := updateWorkspaceAnalysisRunSuccess(ctx, tx, fence, facts, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	answer, err := updateWorkspaceAnalysisAnswer(ctx, tx, slot.Answer, publication, now)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := updateWorkspaceAnalysisConversation(ctx, tx, slot, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := finalizer.appendWorkspaceAnalysisTerminalEvents(
		ctx, tx, command.AnalysisRunID, publication.RunStatus, answer, publication.CitationCount, now, false,
	); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := appendWorkspaceAnalysisTerminalAudit(
		ctx, tx, finalizer.audit, finalizer.workerActorRef, command.AnalysisRunID,
		publication.RunStatus, publication.Reason, answer, publication.CitationCount, now,
	); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := forceWorkspaceAnalysisPublicationConstraints(ctx, tx); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	output, err := workspaceAnalysisPublicationOutput(answer, proofID)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return finalizer.recoverWorkspaceAnalysisCommit(ctx, command.WorkspaceAnalysisPublicationLookup, proofID, err)
	}
	return output, false, nil
}

// FinalizeTermination 原子发布迁移证明支持的拒答、澄清、失败或取消终态。
func (finalizer *WorkspaceAnalysisFinalizer) FinalizeTermination(
	ctx context.Context,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	if finalizer == nil || isNilInterface(finalizer.db) || isNilInterface(finalizer.events) || isNilInterface(finalizer.ids) {
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
	tx, err := finalizer.db.Begin(ctx)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	fence, err := lockWorkspaceAnalysisFinalizationFence(ctx, tx, command.WorkspaceAnalysisPublicationLookup)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := validateCurrentWorkspaceAnalysisLease(fence, command.WorkspaceAnalysisPublicationLookup); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if fence.RunStatus.Terminal() {
		output, replayErr := finalizer.replayTerminationTx(ctx, tx, command)
		if replayErr != nil {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, replayErr
		}
		if err := tx.Commit(ctx); err != nil {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
		}
		return output, true, nil
	}
	if err := validateActiveTerminationFence(fence, command); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	authority, err := lockWorkspaceAnalysisTerminationAuthority(ctx, tx, command, fence)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	publication, latestFactAt, err := buildWorkspaceAnalysisTerminationPublication(ctx, tx, command, fence, authority)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	slot, err := lockWorkspaceAnalysisPublicationSlot(ctx, tx, command.WorkspaceAnalysisPublicationLookup, command.ExpectedAnswerVersion)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	now, err := workspaceAnalysisPublicationTime(ctx, tx, fence, slot, latestFactAt)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	proofID, err := finalizer.ids.New()
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if err := insertWorkspaceAnalysisTerminationProof(ctx, tx, proofID, command, publication, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := abortWorkspaceAnalysisDraft(ctx, tx, slot.Draft, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := updateWorkspaceAnalysisRunTermination(ctx, tx, fence, publication, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	answer, err := updateWorkspaceAnalysisAnswer(ctx, tx, slot.Answer, publication, now)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := updateWorkspaceAnalysisConversation(ctx, tx, slot, now); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := finalizer.appendWorkspaceAnalysisTerminalEvents(
		ctx, tx, command.AnalysisRunID, publication.RunStatus, answer, 0, now, false,
	); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := appendWorkspaceAnalysisTerminalAudit(
		ctx, tx, finalizer.audit, finalizer.workerActorRef, command.AnalysisRunID,
		publication.RunStatus, publication.Reason, answer, 0, now,
	); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := forceWorkspaceAnalysisPublicationConstraints(ctx, tx); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	output, err := workspaceAnalysisPublicationOutput(answer, proofID)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return finalizer.recoverWorkspaceAnalysisCommit(ctx, command.WorkspaceAnalysisPublicationLookup, proofID, err)
	}
	return output, false, nil
}

type workspaceAnalysisFinalizationFence struct {
	RunID                     foundation.ID
	WorkflowStatus            string
	WorkflowCancelRequestedAt *time.Time
	NodeStatus                string
	NodeKey                   string
	NodeAttemptNo             int
	NodeLeaseOwner            *string
	NodeLeaseUntil            *time.Time
	AttemptStatus             string
	AttemptNo                 int
	AttemptLeaseOwner         *string
	AttemptLeaseUntil         *time.Time
	RunStatus                 agentdomain.WorkspaceAnalysisRunStatus
	RunReason                 *agentdomain.WorkspaceAnalysisRunTerminationReason
	RunVersion                int64
	DeadlineAt                time.Time
	MaxModelCalls             int
	MaxToolCalls              int
	MaxSourceReads            int
	MaxInputTokens            int64
	MaxOutputTokens           int64
	ReservedModelCalls        int
	ReservedToolCalls         int
	ReservedSourceReads       int
	ReservedInputTokens       int64
	ReservedOutputTokens      int64
	SettledModelCalls         int
	SettledToolCalls          int
	SettledSourceReads        int
	SettledInputTokens        int64
	SettledOutputTokens       int64
	SettledCostMicrounits     *int64
	RunCreatedAt              time.Time
	RunUpdatedAt              time.Time
	DatabaseNow               time.Time
}

func lockWorkspaceAnalysisFinalizationFence(
	ctx context.Context,
	tx pgx.Tx,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) (workspaceAnalysisFinalizationFence, error) {
	fence := workspaceAnalysisFinalizationFence{RunID: lookup.AnalysisRunID}
	if err := tx.QueryRow(ctx, `SELECT status,cancel_requested_at
		FROM workflow.run WHERE id=$1 AND workspace_id=$2 FOR UPDATE`,
		string(lookup.WorkflowRunID), string(lookup.WorkspaceID),
	).Scan(&fence.WorkflowStatus, &fence.WorkflowCancelRequestedAt); err != nil {
		return workspaceAnalysisFinalizationFence{}, workspaceAnalysisFinalizerQueryError(err, "workflow run")
	}
	if err := tx.QueryRow(ctx, `SELECT status,node_key,attempt,lease_owner,lease_until
		FROM workflow.node_run WHERE id=$1 AND run_id=$2 FOR UPDATE`,
		string(lookup.NodeRunID), string(lookup.WorkflowRunID),
	).Scan(&fence.NodeStatus, &fence.NodeKey, &fence.NodeAttemptNo, &fence.NodeLeaseOwner, &fence.NodeLeaseUntil); err != nil {
		return workspaceAnalysisFinalizationFence{}, workspaceAnalysisFinalizerQueryError(err, "workflow node run")
	}
	if err := tx.QueryRow(ctx, `SELECT status,attempt_no,lease_owner,lease_until
		FROM workflow.node_attempt WHERE id=$1 AND node_run_id=$2 FOR UPDATE`,
		string(lookup.NodeAttemptID), string(lookup.NodeRunID),
	).Scan(&fence.AttemptStatus, &fence.AttemptNo, &fence.AttemptLeaseOwner, &fence.AttemptLeaseUntil); err != nil {
		return workspaceAnalysisFinalizationFence{}, workspaceAnalysisFinalizerQueryError(err, "workflow node attempt")
	}
	var runReason *string
	if err := tx.QueryRow(ctx, `SELECT
		status,termination_reason,version,deadline_at,
		max_model_calls,max_tool_calls,max_source_reads,max_input_tokens,max_output_tokens,
		reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,
		settled_model_calls,settled_tool_calls,settled_source_reads,settled_input_tokens,settled_output_tokens,
		settled_cost_microunits,created_at,updated_at,clock_timestamp()
		FROM agent.workspace_analysis_run
		WHERE id=$1 AND workspace_id=$2 AND conversation_id=$3 AND question_id=$4 AND answer_id=$5 AND workflow_run_id=$6
		FOR UPDATE`,
		string(lookup.AnalysisRunID), string(lookup.WorkspaceID), string(lookup.ConversationID),
		string(lookup.QuestionID), string(lookup.AnswerID), string(lookup.WorkflowRunID),
	).Scan(
		&fence.RunStatus, &runReason, &fence.RunVersion, &fence.DeadlineAt,
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
	return fence, nil
}

func validateActiveSuccessFence(
	fence workspaceAnalysisFinalizationFence,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) error {
	if err := validateActiveWorkspaceAnalysisFence(fence, lookup); err != nil {
		return err
	}
	if fence.NodeKey != conversationworkflow.WorkspaceAnalysisNodeReviewPublish || fence.WorkflowCancelRequestedAt != nil ||
		fence.DatabaseNow.After(fence.DeadlineAt) {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis success fence is no longer publishable"))
	}
	return nil
}

func validateActiveTerminationFence(
	fence workspaceAnalysisFinalizationFence,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
) error {
	if err := validateCurrentWorkspaceAnalysisLease(fence, command.WorkspaceAnalysisPublicationLookup); err != nil {
		return err
	}
	queuedPreoperationDeadline := command.Reason == agentdomain.WorkspaceAnalysisRunDeadlineExceeded &&
		command.OperationID == nil && fence.RunStatus == agentdomain.WorkspaceAnalysisRunQueued
	if fence.RunReason != nil || (fence.RunStatus != agentdomain.WorkspaceAnalysisRunRunning && !queuedPreoperationDeadline) {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis termination fence is not current"))
	}
	if command.Reason == agentdomain.WorkspaceAnalysisRunCancellation {
		if fence.WorkflowCancelRequestedAt == nil || fence.WorkflowCancelRequestedAt.After(fence.DatabaseNow) {
			return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis cancellation is not requested at this checkpoint"))
		}
	} else if fence.WorkflowCancelRequestedAt != nil {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis non-cancellation terminalization raced cancellation"))
	}
	if command.Reason != agentdomain.WorkspaceAnalysisRunDeadlineExceeded && fence.DatabaseNow.After(fence.DeadlineAt) {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis terminalization missed its run deadline"))
	}
	return nil
}

func validateActiveWorkspaceAnalysisFence(
	fence workspaceAnalysisFinalizationFence,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) error {
	if err := validateCurrentWorkspaceAnalysisLease(fence, lookup); err != nil {
		return err
	}
	if fence.RunStatus != agentdomain.WorkspaceAnalysisRunRunning || fence.RunReason != nil {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis finalization fence is not current"))
	}
	return nil
}

func validateCurrentWorkspaceAnalysisLease(
	fence workspaceAnalysisFinalizationFence,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) error {
	if fence.WorkflowStatus != "running" || fence.NodeStatus != "running" || fence.AttemptStatus != "running" ||
		fence.NodeAttemptNo != fence.AttemptNo || int64(fence.NodeAttemptNo) != lookup.ExpectedLeaseFence ||
		fence.NodeLeaseOwner == nil || *fence.NodeLeaseOwner != lookup.ExpectedLeaseOwner ||
		fence.AttemptLeaseOwner == nil || *fence.AttemptLeaseOwner != lookup.ExpectedLeaseOwner ||
		fence.NodeLeaseUntil == nil || fence.AttemptLeaseUntil == nil || !fence.NodeLeaseUntil.Equal(*fence.AttemptLeaseUntil) ||
		!fence.DatabaseNow.Before(*fence.AttemptLeaseUntil) {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis finalization lease does not match the caller fence"))
	}
	return nil
}

type workspaceAnalysisOperationRecord struct {
	ID          foundation.ID
	NodeRunID   foundation.ID
	Kind        agentdomain.WorkspaceAnalysisOperationKind
	CallKind    agentdomain.WorkspaceAnalysisOperationCallKind
	Status      agentdomain.WorkspaceAnalysisOperationStatus
	ModelCallID *foundation.ID
	ToolCallID  *foundation.ID
	ResultKind  *agentdomain.WorkspaceAnalysisOperationResultKind
	ResultID    *foundation.ID
	ResultHash  *string
	ErrorCode   *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
}

type workspaceAnalysisReservationRecord struct {
	Found     bool
	Status    agentdomain.WorkspaceAnalysisBudgetReservationStatus
	SettledAt *time.Time
}

type workspaceAnalysisCallAuthority struct {
	Reservation        workspaceAnalysisReservationRecord
	ModelRunID         *foundation.ID
	ModelRunStatus     *agentdomain.ModelRunStatus
	ModelFinalResult   *string
	ModelRunCompleted  *time.Time
	ModelCallStatus    *agentdomain.ModelCallStatus
	ModelCallCompleted *time.Time
	ToolCallStatus     *toolsdomain.CallStatus
	ToolCallCompleted  *time.Time
	LatestFactAt       time.Time
}

func lockWorkspaceAnalysisSuccessAuthority(
	ctx context.Context,
	tx pgx.Tx,
	command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
) (workspaceAnalysisOperationRecord, workspaceAnalysisCallAuthority, error) {
	operation, err := scanWorkspaceAnalysisOperation(tx.QueryRow(ctx, `SELECT
		id::text,node_run_id::text,operation_kind,call_kind,status,model_call_id::text,tool_call_id::text,
		result_kind,result_id::text,result_hash,error_code,created_at,updated_at,completed_at
		FROM agent.workspace_analysis_operation
		WHERE analysis_run_id=$1 AND node_run_id=$2 AND operation_kind='FAITHFULNESS_REVIEW'
		  AND result_id=$3 AND result_hash=$4 FOR UPDATE`,
		string(command.AnalysisRunID), string(command.NodeRunID), string(command.ReviewModelResultID), command.ReviewModelResultHash,
	))
	if err != nil {
		return workspaceAnalysisOperationRecord{}, workspaceAnalysisCallAuthority{}, workspaceAnalysisFinalizerQueryError(err, "faithfulness review operation")
	}
	authority, err := lockWorkspaceAnalysisOperationAuthority(ctx, tx, operation)
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

type workspaceAnalysisTerminationAuthority struct {
	Operation *workspaceAnalysisOperationRecord
	Call      workspaceAnalysisCallAuthority
}

func lockWorkspaceAnalysisTerminationAuthority(
	ctx context.Context,
	tx pgx.Tx,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
	fence workspaceAnalysisFinalizationFence,
) (workspaceAnalysisTerminationAuthority, error) {
	if command.OperationID == nil {
		return workspaceAnalysisTerminationAuthority{}, nil
	}
	operation, err := scanWorkspaceAnalysisOperation(tx.QueryRow(ctx, `SELECT
		id::text,node_run_id::text,operation_kind,call_kind,status,model_call_id::text,tool_call_id::text,
		result_kind,result_id::text,result_hash,error_code,created_at,updated_at,completed_at
		FROM agent.workspace_analysis_operation
		WHERE id=$1 AND analysis_run_id=$2 AND workspace_id=$3 AND workflow_run_id=$4
		  AND (
			($5='WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT' AND $6='read_evidence'
			 AND node_key='retrieve_evidence' AND operation_kind='KNOWLEDGE_SEARCH' AND ordinal=1)
			OR ($5='WORKSPACE_ANALYSIS_CITATION_INVALID' AND $6='review_publish'
			 AND node_key='validate_citations' AND operation_kind='CITATION_VALIDATION' AND ordinal=1)
			OR ($5 NOT IN ('WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT','WORKSPACE_ANALYSIS_CITATION_INVALID')
			 AND node_run_id=$7)
		  )
		FOR UPDATE`,
		string(*command.OperationID), string(command.AnalysisRunID), string(command.WorkspaceID),
		string(command.WorkflowRunID), string(command.Reason), fence.NodeKey, string(command.NodeRunID),
	))
	if err != nil {
		return workspaceAnalysisTerminationAuthority{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis termination operation")
	}
	authority, err := lockWorkspaceAnalysisOperationAuthority(ctx, tx, operation)
	if err != nil {
		return workspaceAnalysisTerminationAuthority{}, err
	}
	if err := validateWorkspaceAnalysisTerminationAuthority(command, fence, operation, authority); err != nil {
		return workspaceAnalysisTerminationAuthority{}, err
	}
	return workspaceAnalysisTerminationAuthority{Operation: &operation, Call: authority}, nil
}

func lockWorkspaceAnalysisOperationAuthority(
	ctx context.Context,
	tx pgx.Tx,
	operation workspaceAnalysisOperationRecord,
) (workspaceAnalysisCallAuthority, error) {
	authority := workspaceAnalysisCallAuthority{LatestFactAt: operation.UpdatedAt}
	err := tx.QueryRow(ctx, `SELECT status,settled_at FROM agent.workspace_analysis_budget_reservation
		WHERE operation_id=$1 FOR UPDATE`, string(operation.ID)).Scan(&authority.Reservation.Status, &authority.Reservation.SettledAt)
	switch {
	case err == nil:
		authority.Reservation.Found = true
		if authority.Reservation.SettledAt != nil {
			authority.LatestFactAt = laterWorkspaceAnalysisTime(authority.LatestFactAt, *authority.Reservation.SettledAt)
		}
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return workspaceAnalysisCallAuthority{}, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}

	if operation.ModelCallID != nil {
		var callModelRunID string
		var callStatus agentdomain.ModelCallStatus
		var callCompleted *time.Time
		if err := tx.QueryRow(ctx, `SELECT model_run_id::text,status,completed_at FROM agent.model_call
			WHERE id=$1 FOR UPDATE`, string(*operation.ModelCallID)).Scan(&callModelRunID, &callStatus, &callCompleted); err != nil {
			return workspaceAnalysisCallAuthority{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis model call")
		}
		parsedModelRunID, err := parseCanonicalID(callModelRunID)
		if err != nil {
			return workspaceAnalysisCallAuthority{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
		}
		var modelStatus agentdomain.ModelRunStatus
		var finalResult *string
		var modelCompleted *time.Time
		if err := tx.QueryRow(ctx, `SELECT status,final_result_type,completed_at FROM agent.model_run
			WHERE id=$1 FOR UPDATE`, string(parsedModelRunID)).Scan(&modelStatus, &finalResult, &modelCompleted); err != nil {
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
		if err := tx.QueryRow(ctx, `SELECT status,completed_at FROM workflow.tool_call
			WHERE id=$1 FOR UPDATE`, string(*operation.ToolCallID)).Scan(&callStatus, &callCompleted); err != nil {
			return workspaceAnalysisCallAuthority{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis tool call")
		}
		authority.ToolCallStatus, authority.ToolCallCompleted = &callStatus, callCompleted
		if callCompleted != nil {
			authority.LatestFactAt = laterWorkspaceAnalysisTime(authority.LatestFactAt, *callCompleted)
		}
	}
	return authority, nil
}

func scanWorkspaceAnalysisOperation(row pgx.Row) (workspaceAnalysisOperationRecord, error) {
	var (
		record                            workspaceAnalysisOperationRecord
		id, nodeRunID                     string
		modelCallID, toolCallID, resultID *string
		resultKind, resultHash, errorCode *string
	)
	if err := row.Scan(
		&id, &nodeRunID, &record.Kind, &record.CallKind, &record.Status, &modelCallID, &toolCallID,
		&resultKind, &resultID, &resultHash, &errorCode, &record.CreatedAt, &record.UpdatedAt, &record.CompletedAt,
	); err != nil {
		return workspaceAnalysisOperationRecord{}, err
	}
	parsedID, err := parseCanonicalID(id)
	if err != nil {
		return workspaceAnalysisOperationRecord{}, err
	}
	parsedNodeRunID, err := parseCanonicalID(nodeRunID)
	if err != nil {
		return workspaceAnalysisOperationRecord{}, err
	}
	record.ID, record.NodeRunID = parsedID, parsedNodeRunID
	if modelCallID != nil {
		value, parseErr := parseCanonicalID(*modelCallID)
		if parseErr != nil {
			return workspaceAnalysisOperationRecord{}, parseErr
		}
		record.ModelCallID = &value
	}
	if toolCallID != nil {
		value, parseErr := parseCanonicalID(*toolCallID)
		if parseErr != nil {
			return workspaceAnalysisOperationRecord{}, parseErr
		}
		record.ToolCallID = &value
	}
	if resultID != nil {
		value, parseErr := parseCanonicalID(*resultID)
		if parseErr != nil {
			return workspaceAnalysisOperationRecord{}, parseErr
		}
		record.ResultID = &value
	}
	if resultKind != nil {
		value := agentdomain.WorkspaceAnalysisOperationResultKind(*resultKind)
		record.ResultKind = &value
	}
	record.ResultHash, record.ErrorCode = resultHash, errorCode
	return record, nil
}

func validateWorkspaceAnalysisTerminationAuthority(
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
	fence workspaceAnalysisFinalizationFence,
	operation workspaceAnalysisOperationRecord,
	authority workspaceAnalysisCallAuthority,
) error {
	artifactMatches := func(kind conversationapplication.WorkspaceAnalysisTerminationArtifactKind) bool {
		return command.Artifact != nil && command.Artifact.Kind == kind && operation.ResultID != nil && operation.ResultHash != nil &&
			*operation.ResultID == command.Artifact.ID && *operation.ResultHash == command.Artifact.Hash
	}
	modelSucceeded := authority.ModelRunStatus != nil && *authority.ModelRunStatus == agentdomain.ModelRunSucceeded &&
		authority.ModelCallStatus != nil && *authority.ModelCallStatus == agentdomain.ModelCallSucceeded
	toolSucceeded := authority.ToolCallStatus != nil && *authority.ToolCallStatus == toolsdomain.CallSucceeded
	settled := authority.Reservation.Found && authority.Reservation.Status == agentdomain.WorkspaceAnalysisBudgetSettled
	switch command.Reason {
	case agentdomain.WorkspaceAnalysisRunEvidenceInsufficient:
		if operation.Kind != agentdomain.WorkspaceAnalysisOperationKnowledgeSearch || operation.Status != agentdomain.WorkspaceAnalysisOperationSucceeded ||
			operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallTool || !artifactMatches(conversationapplication.WorkspaceAnalysisTerminationArtifactToolReceipt) ||
			!toolSucceeded || !settled {
			return workspaceAnalysisTerminationAuthorityError("evidence-insufficient authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunCitationInvalid:
		if operation.Kind != agentdomain.WorkspaceAnalysisOperationCitationValidation || operation.Status != agentdomain.WorkspaceAnalysisOperationSucceeded ||
			operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallTool || !artifactMatches(conversationapplication.WorkspaceAnalysisTerminationArtifactToolReceipt) ||
			!toolSucceeded || !settled {
			return workspaceAnalysisTerminationAuthorityError("citation-invalid authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunFaithfulnessRejected:
		if operation.Kind != agentdomain.WorkspaceAnalysisOperationFaithfulnessReview || operation.Status != agentdomain.WorkspaceAnalysisOperationSucceeded ||
			operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallModel || !artifactMatches(conversationapplication.WorkspaceAnalysisTerminationArtifactModelResult) ||
			!modelSucceeded || !settled {
			return workspaceAnalysisTerminationAuthorityError("faithfulness rejection authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunModelRefused:
		if operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallModel || operation.Status != agentdomain.WorkspaceAnalysisOperationFailed ||
			operation.ErrorCode == nil || *operation.ErrorCode != string(agentdomain.WorkspaceAnalysisRunModelRefused) ||
			authority.ModelRunStatus == nil || *authority.ModelRunStatus != agentdomain.ModelRunRefused ||
			authority.ModelCallStatus == nil || *authority.ModelCallStatus != agentdomain.ModelCallSucceeded ||
			authority.ModelFinalResult == nil || *authority.ModelFinalResult != agentdomain.ResultTypeRefusal || !settled {
			return workspaceAnalysisTerminationAuthorityError("model refusal authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunNeedsClarification:
		if operation.Kind != agentdomain.WorkspaceAnalysisOperationRetrievalPlan || operation.Status != agentdomain.WorkspaceAnalysisOperationSucceeded ||
			operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallModel || !artifactMatches(conversationapplication.WorkspaceAnalysisTerminationArtifactModelResult) ||
			!modelSucceeded || !settled {
			return workspaceAnalysisTerminationAuthorityError("clarification authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunBudgetExhausted:
		if operation.Status != agentdomain.WorkspaceAnalysisOperationPending || authority.Reservation.Found || operation.ModelCallID != nil || operation.ToolCallID != nil ||
			command.BudgetRequest == nil || !workspaceAnalysisBudgetRequestMatchesOperation(*command.BudgetRequest, operation.Kind, fence) {
			return workspaceAnalysisTerminationAuthorityError("budget exhaustion authority is incomplete")
		}
		request := *command.BudgetRequest
		if request.ModelCalls <= fence.MaxModelCalls-fence.ReservedModelCalls-fence.SettledModelCalls &&
			request.ToolCalls <= fence.MaxToolCalls-fence.ReservedToolCalls-fence.SettledToolCalls &&
			request.SourceReads <= fence.MaxSourceReads-fence.ReservedSourceReads-fence.SettledSourceReads &&
			request.InputTokens <= fence.MaxInputTokens-fence.ReservedInputTokens-fence.SettledInputTokens &&
			request.OutputTokens <= fence.MaxOutputTokens-fence.ReservedOutputTokens-fence.SettledOutputTokens {
			return workspaceAnalysisTerminationAuthorityError("budget request still fits the authoritative remaining budget")
		}
	case agentdomain.WorkspaceAnalysisRunReceiptInvalid:
		if operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallTool || operation.Status != agentdomain.WorkspaceAnalysisOperationFailed ||
			operation.ErrorCode == nil || *operation.ErrorCode != string(agentdomain.WorkspaceAnalysisRunReceiptInvalid) || !toolSucceeded || !settled {
			return workspaceAnalysisTerminationAuthorityError("receipt-invalid authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunResultUnknown:
		callUnknown := operation.CallKind == agentdomain.WorkspaceAnalysisOperationCallModel && authority.ModelCallStatus != nil &&
			*authority.ModelCallStatus == agentdomain.ModelCallUnknown && authority.ModelRunStatus != nil && *authority.ModelRunStatus == agentdomain.ModelRunUnknown
		callUnknown = callUnknown || (operation.CallKind == agentdomain.WorkspaceAnalysisOperationCallTool && authority.ToolCallStatus != nil && *authority.ToolCallStatus == toolsdomain.CallUnknown)
		if operation.Status != agentdomain.WorkspaceAnalysisOperationUnknown || !authority.Reservation.Found ||
			authority.Reservation.Status != agentdomain.WorkspaceAnalysisBudgetUnknownCharged || !callUnknown {
			return workspaceAnalysisTerminationAuthorityError("unknown result authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunDeadlineExceeded:
		if operation.Status != agentdomain.WorkspaceAnalysisOperationPending && operation.Status != agentdomain.WorkspaceAnalysisOperationSucceeded {
			return workspaceAnalysisTerminationAuthorityError("deadline authority is not pending or successful")
		}
		if operation.Status == agentdomain.WorkspaceAnalysisOperationPending && authority.Reservation.Found {
			return workspaceAnalysisTerminationAuthorityError("pending deadline authority unexpectedly owns a reservation")
		}
	case agentdomain.WorkspaceAnalysisRunModelFailed:
		if operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallModel || operation.Status != agentdomain.WorkspaceAnalysisOperationFailed ||
			authority.ModelCallStatus == nil || *authority.ModelCallStatus != agentdomain.ModelCallFailed ||
			authority.ModelRunStatus == nil || *authority.ModelRunStatus != agentdomain.ModelRunFailed || !settled {
			return workspaceAnalysisTerminationAuthorityError("model failure authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunToolFailed:
		if operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallTool || operation.Status != agentdomain.WorkspaceAnalysisOperationFailed ||
			authority.ToolCallStatus == nil || *authority.ToolCallStatus != toolsdomain.CallFailed || !settled {
			return workspaceAnalysisTerminationAuthorityError("tool failure authority is incomplete")
		}
	default:
		return workspaceAnalysisTerminationAuthorityError("termination reason has no operation authority matrix")
	}
	return nil
}

func workspaceAnalysisBudgetRequestMatchesOperation(
	request conversationapplication.WorkspaceAnalysisBudgetRequest,
	kind agentdomain.WorkspaceAnalysisOperationKind,
	fence workspaceAnalysisFinalizationFence,
) bool {
	want := conversationapplication.WorkspaceAnalysisBudgetRequest{}
	switch kind {
	case agentdomain.WorkspaceAnalysisOperationRetrievalPlan:
		want.ModelCalls, want.InputTokens, want.OutputTokens = 1, agentdomain.WorkspaceAnalysisV1MaxInputTokensPerModelCall, agentdomain.WorkspaceAnalysisV1PlanMaxOutputTokens
	case agentdomain.WorkspaceAnalysisOperationAnswerSynthesis:
		want.ModelCalls, want.InputTokens = 1, agentdomain.WorkspaceAnalysisV1MaxInputTokensPerModelCall
		want.OutputTokens = fence.MaxOutputTokens - agentdomain.WorkspaceAnalysisV1PlanMaxOutputTokens - agentdomain.WorkspaceAnalysisV1ReviewMaxOutputTokens
	case agentdomain.WorkspaceAnalysisOperationFaithfulnessReview:
		want.ModelCalls, want.InputTokens, want.OutputTokens = 1, agentdomain.WorkspaceAnalysisV1MaxInputTokensPerModelCall, agentdomain.WorkspaceAnalysisV1ReviewMaxOutputTokens
	case agentdomain.WorkspaceAnalysisOperationSourceRead:
		want.ToolCalls, want.SourceReads = 1, 1
	case agentdomain.WorkspaceAnalysisOperationGitStatus, agentdomain.WorkspaceAnalysisOperationKnowledgeSearch,
		agentdomain.WorkspaceAnalysisOperationCitationValidation:
		want.ToolCalls = 1
	default:
		return false
	}
	return request.ModelCalls == want.ModelCalls && request.ToolCalls == want.ToolCalls && request.SourceReads == want.SourceReads &&
		request.InputTokens == want.InputTokens && request.OutputTokens == want.OutputTokens && request.RequestedCostMicrounits == nil
}

func workspaceAnalysisTerminationAuthorityError(message string) error {
	return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New(message))
}

type workspaceAnalysisSuccessFacts struct {
	Candidate         agentdomain.WorkspaceAnalysisCandidate
	GitReceipt        toolsdomain.ResultReceipt
	ValidationReceipt toolsdomain.ResultReceipt
	ReviewResult      agentdomain.WorkspaceAnalysisModelResult
	Review            agentdomain.FaithfulnessReviewResult
	LatestFactAt      time.Time
}

func loadWorkspaceAnalysisSuccessFacts(
	ctx context.Context,
	tx pgx.Tx,
	command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
	fence workspaceAnalysisFinalizationFence,
	reviewOperation workspaceAnalysisOperationRecord,
	reviewAuthority workspaceAnalysisCallAuthority,
) (workspaceAnalysisSuccessFacts, error) {
	candidate, err := loadWorkspaceAnalysisCandidate(ctx, tx, command.AnalysisRunID, command.CandidateID, command.CandidateHash)
	if err != nil {
		return workspaceAnalysisSuccessFacts{}, err
	}
	gitReceipt, err := loadWorkspaceAnalysisResultReceipt(ctx, tx, command.WorkspaceID, command.GitReceiptID, command.GitReceiptHash)
	if err != nil {
		return workspaceAnalysisSuccessFacts{}, err
	}
	validationReceipt, err := loadWorkspaceAnalysisResultReceipt(ctx, tx, command.WorkspaceID, command.ValidationReceiptID, command.ValidationReceiptHash)
	if err != nil {
		return workspaceAnalysisSuccessFacts{}, err
	}
	reviewResult, err := loadWorkspaceAnalysisModelResult(ctx, tx, command.AnalysisRunID, command.ReviewModelResultID, command.ReviewModelResultHash)
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
	if gitReceipt.WorkflowRunID != command.WorkflowRunID || validationReceipt.WorkflowRunID != command.WorkflowRunID {
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

func loadWorkspaceAnalysisCandidate(
	ctx context.Context,
	tx pgx.Tx,
	analysisRunID, candidateID foundation.ID,
	candidateHash string,
) (agentdomain.WorkspaceAnalysisCandidate, error) {
	var (
		candidate                                                            agentdomain.WorkspaceAnalysisCandidate
		id, workspaceID, runID, answerID, operationID, attemptID, modelRunID string
	)
	if err := tx.QueryRow(ctx, `SELECT
		id::text,workspace_id::text,analysis_run_id::text,answer_id::text,synthesis_operation_id::text,
		node_attempt_id::text,synthesis_model_run_id::text,schema_id,schema_version,document,document_hash,document_bytes,created_at
		FROM agent.workspace_analysis_candidate
		WHERE id=$1 AND analysis_run_id=$2 AND document_hash=$3 FOR SHARE`,
		string(candidateID), string(analysisRunID), candidateHash,
	).Scan(
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

func loadWorkspaceAnalysisModelResult(
	ctx context.Context,
	tx pgx.Tx,
	analysisRunID, resultID foundation.ID,
	resultHash string,
) (agentdomain.WorkspaceAnalysisModelResult, error) {
	var (
		result                                                                  agentdomain.WorkspaceAnalysisModelResult
		id, workspaceID, runID, operationID, attemptID, modelRunID, modelCallID string
		subjectCandidateID, subjectCandidateHash                                *string
	)
	if err := tx.QueryRow(ctx, `SELECT
		id::text,workspace_id::text,analysis_run_id::text,operation_id::text,node_attempt_id::text,
		model_run_id::text,model_call_id::text,operation_kind,schema_id,schema_version,
		subject_candidate_id::text,subject_candidate_hash,document,document_hash,document_bytes,created_at
		FROM agent.workspace_analysis_model_result
		WHERE id=$1 AND analysis_run_id=$2 AND document_hash=$3 FOR SHARE`,
		string(resultID), string(analysisRunID), resultHash,
	).Scan(
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

func loadWorkspaceAnalysisResultReceipt(
	ctx context.Context,
	tx pgx.Tx,
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
	if err := tx.QueryRow(ctx, `SELECT
		id::text,tool_call_id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,
		tool_name,tool_version,output_schema_id,output_schema_version,definition_hash,persistence_policy,
		max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,
		server_binding_schema_id,server_binding_schema_version,server_binding_document,server_binding_hash,server_binding_bytes,created_at
		FROM workflow.tool_result_receipt
		WHERE id=$1 AND workspace_id=$2 AND output_hash=$3 FOR SHARE`,
		string(receiptID), string(workspaceID), receiptHash,
	).Scan(
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

func validateWorkspaceAnalysisLoadedReceipt(receipt toolsdomain.ResultReceipt) error {
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(receipt.Tool)
	if !found || receipt.OutputSchema != contract.OutputSchema || receipt.PersistencePolicy != toolsdomain.ResultPersistenceCanonical ||
		receipt.MaxOutputBytes != contract.MaxOutputBytes || receipt.MaxPrivateBindingBytes != contract.MaxPrivateBindingBytes ||
		contract.RequiresPrivateBinding != (receipt.PrivateBinding != nil) && contract.RequiresPrivateBinding ||
		receipt.OutputBytes != int64(len(receipt.Output)) || receipt.OutputBytes < 1 || receipt.OutputBytes > contract.MaxOutputBytes ||
		workspaceAnalysisDocumentHash(receipt.Output) != receipt.OutputHash || receipt.CreatedAt.IsZero() {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis receipt envelope is invalid"))
	}
	if receipt.PrivateBinding != nil {
		if receipt.PrivateBinding.Schema != contract.PrivateBindingSchema || receipt.PrivateBinding.Bytes != int64(len(receipt.PrivateBinding.Document)) ||
			receipt.PrivateBinding.Bytes < 1 || receipt.PrivateBinding.Bytes > contract.MaxPrivateBindingBytes ||
			workspaceAnalysisDocumentHash(receipt.PrivateBinding.Document) != receipt.PrivateBinding.Hash {
			return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis receipt private binding is invalid"))
		}
	}
	return nil
}

type workspaceAnalysisPublication struct {
	Status        conversationdomain.AnswerPublicationStatus
	ResultType    conversationdomain.AnswerResultType
	ModelRunID    *foundation.ID
	Document      json.RawMessage
	ResultHash    string
	RunStatus     agentdomain.WorkspaceAnalysisRunStatus
	Reason        agentdomain.WorkspaceAnalysisRunTerminationReason
	CitationCount int64
}

func buildWorkspaceAnalysisSuccessPublication(
	workspaceID foundation.ID,
	fence workspaceAnalysisFinalizationFence,
	facts workspaceAnalysisSuccessFacts,
) (workspaceAnalysisPublication, error) {
	candidate, err := agentdomain.DecodeWorkspaceAnalysisCandidate(facts.Candidate.Document, agentdomain.DefaultDecodeLimits())
	if err != nil || candidate.ModelRunRef != facts.Candidate.SynthesisModelRunID {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis candidate document is invalid"))
	}
	git, err := conversationworkflow.DecodeWorkspaceAnalysisGitStatusSummary(facts.GitReceipt.Output)
	if err != nil || facts.GitReceipt.Tool != (toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 2}) {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis Git receipt is invalid"))
	}
	validationResults, err := toolsdomain.ValidateCitationV3ReceiptResults(
		facts.ValidationReceipt, facts.Candidate.ID, facts.Candidate.DocumentHash, candidate.Payload.CitationRefs,
	)
	if err != nil {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	for _, result := range validationResults {
		if !result.Valid || result.ReasonCode != "OK" {
			return workspaceAnalysisPublication{}, conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis citations are not all valid"))
		}
	}
	trustedCitations, err := toolsdomain.ValidateCitationV3ReceiptCitations(
		facts.ValidationReceipt, facts.Candidate.ID, facts.Candidate.DocumentHash, candidate.Payload.CitationRefs,
	)
	if err != nil {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	citations := make([]agentdomain.Citation, len(trustedCitations))
	citationIDByRef := make(map[string]string, len(trustedCitations))
	for index, trusted := range trustedCitations {
		citation := agentdomain.Citation{
			ID: trusted.CitationID, WorkspaceID: workspaceID, IndexVersionID: trusted.IndexVersionID,
			ChunkID: trusted.ChunkID, SourceVersionID: trusted.SourceVersionID, SourceSpanID: trusted.SourceSpanID,
		}
		if err := citation.Validate(); err != nil {
			return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
		}
		citations[index] = citation
		citationIDByRef[trusted.EvidenceRef] = trusted.CitationID
	}
	var proposal *conversationdomain.WorkspaceAnalysisProposalSuggestion
	if source := candidate.Payload.ProposalSuggestion; source != nil {
		citationIDs := make([]string, len(source.CitationRefs))
		for index, reference := range source.CitationRefs {
			citationID, found := citationIDByRef[reference]
			if !found {
				return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis proposal references an unpublished citation"))
			}
			citationIDs[index] = citationID
		}
		proposal = &conversationdomain.WorkspaceAnalysisProposalSuggestion{
			Summary: source.Summary, CitationIDs: citationIDs, Href: conversationdomain.WorkspaceAnalysisProposalHref,
		}
	}
	var settledCost *int64
	if fence.SettledCostMicrounits != nil {
		value := *fence.SettledCostMicrounits
		settledCost = &value
	}
	result := conversationdomain.WorkspaceAnalysisAnswerResult{
		ResultType:    conversationdomain.AnswerResultWorkspaceAnalysis,
		SchemaID:      conversationdomain.WorkspaceAnalysisAnswerSchemaID,
		SchemaVersion: conversationdomain.WorkspaceAnalysisResultSchemaVersionV1,
		ModelRunRef:   facts.Candidate.SynthesisModelRunID,
		Payload: conversationdomain.WorkspaceAnalysisAnswerPayload{
			AnswerMarkdown: candidate.Payload.AnswerMarkdown,
			Citations:      citations,
			GitStatus: conversationdomain.WorkspaceAnalysisGitStatus{
				Branch: git.Branch, Head: git.Head, Clean: git.Clean, StagedCount: git.StagedCount,
				UnstagedCount: git.UnstagedCount, UntrackedCount: git.UntrackedCount, ConflictCount: git.ConflictCount,
			},
			Budget: conversationdomain.WorkspaceAnalysisBudgetSummary{
				ModelCalls: fence.SettledModelCalls, ToolCalls: fence.SettledToolCalls,
				InputTokens: fence.SettledInputTokens, OutputTokens: fence.SettledOutputTokens,
				EstimatedCostMicrounits: settledCost,
			},
			ProposalSuggestion: proposal,
			TerminationReason:  conversationdomain.WorkspaceAnalysisCompleted,
		},
	}
	document, err := json.Marshal(result)
	if err != nil {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	published, err := conversationdomain.CanonicalizeWorkspaceAnalysisPublishedResult(
		conversationdomain.AnswerPublicationCompleted, conversationdomain.AnswerResultWorkspaceAnalysis, document,
	)
	if err != nil || published.ModelRunID == nil || *published.ModelRunID != facts.Candidate.SynthesisModelRunID {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return workspaceAnalysisPublication{
		Status: conversationdomain.AnswerPublicationCompleted, ResultType: published.Type,
		ModelRunID: copyWorkspaceAnalysisFinalizerID(published.ModelRunID), Document: published.Document, ResultHash: published.Hash,
		RunStatus: agentdomain.WorkspaceAnalysisRunSucceeded, Reason: agentdomain.WorkspaceAnalysisRunCompleted,
		CitationCount: int64(len(citations)),
	}, nil
}

type workspaceAnalysisReceiptFailureRecord struct {
	ID           foundation.ID
	OperationID  foundation.ID
	Code         conversationapplication.WorkspaceAnalysisReceiptFailureCode
	ExpectedHash string
	ActualHash   *string
	CreatedAt    time.Time
}

func buildWorkspaceAnalysisTerminationPublication(
	ctx context.Context,
	tx pgx.Tx,
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
		receipt, err := loadWorkspaceAnalysisResultReceipt(ctx, tx, command.WorkspaceID, command.Artifact.ID, command.Artifact.Hash)
		if err != nil {
			return workspaceAnalysisPublication{}, time.Time{}, err
		}
		selected, err := toolsdomain.SearchKnowledgeV2ReceiptSelectedRefs(receipt)
		if err != nil || receipt.Tool != (toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 2}) || len(selected) != 0 {
			return workspaceAnalysisPublication{}, time.Time{}, workspaceAnalysisTerminationAuthorityError("evidence-insufficient receipt is not an exact zero-hit search")
		}
		latestFactAt = laterWorkspaceAnalysisTime(latestFactAt, receipt.CreatedAt)
	case agentdomain.WorkspaceAnalysisRunCitationInvalid:
		receipt, err := loadWorkspaceAnalysisResultReceipt(ctx, tx, command.WorkspaceID, command.Artifact.ID, command.Artifact.Hash)
		if err != nil {
			return workspaceAnalysisPublication{}, time.Time{}, err
		}
		candidate, decoded, err := loadWorkspaceAnalysisCandidateForRun(ctx, tx, command.AnalysisRunID, command.AnswerID)
		if err != nil {
			return workspaceAnalysisPublication{}, time.Time{}, err
		}
		results, err := toolsdomain.ValidateCitationV3ReceiptResults(receipt, candidate.ID, candidate.DocumentHash, decoded.Payload.CitationRefs)
		if err != nil || receipt.Tool != (toolsdomain.ToolRef{Name: "ValidateCitation", Version: 3}) {
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
		result, err := loadWorkspaceAnalysisModelResult(ctx, tx, command.AnalysisRunID, command.Artifact.ID, command.Artifact.Hash)
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
		result, err := loadWorkspaceAnalysisModelResult(ctx, tx, command.AnalysisRunID, command.Artifact.ID, command.Artifact.Hash)
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
		failure, err := loadWorkspaceAnalysisReceiptFailure(ctx, tx, command.AnalysisRunID, authority.Operation.ID, command.ReceiptFailure.ID)
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

func loadWorkspaceAnalysisCandidateForRun(
	ctx context.Context,
	tx pgx.Tx,
	analysisRunID, answerID foundation.ID,
) (agentdomain.WorkspaceAnalysisCandidate, agentdomain.WorkspaceAnalysisCandidateResult, error) {
	var candidateID string
	var candidateHash string
	if err := tx.QueryRow(ctx, `SELECT id::text,document_hash FROM agent.workspace_analysis_candidate
		WHERE analysis_run_id=$1 AND answer_id=$2 FOR SHARE`, string(analysisRunID), string(answerID)).Scan(&candidateID, &candidateHash); err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, agentdomain.WorkspaceAnalysisCandidateResult{},
			workspaceAnalysisFinalizerQueryError(err, "workspace analysis candidate")
	}
	parsedID, err := parseCanonicalID(candidateID)
	if err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, agentdomain.WorkspaceAnalysisCandidateResult{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	candidate, err := loadWorkspaceAnalysisCandidate(ctx, tx, analysisRunID, parsedID, candidateHash)
	if err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, agentdomain.WorkspaceAnalysisCandidateResult{}, err
	}
	decoded, err := agentdomain.DecodeWorkspaceAnalysisCandidate(candidate.Document, agentdomain.DefaultDecodeLimits())
	if err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, agentdomain.WorkspaceAnalysisCandidateResult{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return candidate, decoded, nil
}

func loadWorkspaceAnalysisReceiptFailure(
	ctx context.Context,
	tx pgx.Tx,
	analysisRunID, operationID, failureID foundation.ID,
) (workspaceAnalysisReceiptFailureRecord, error) {
	var record workspaceAnalysisReceiptFailureRecord
	var id, storedOperationID string
	if err := tx.QueryRow(ctx, `SELECT id::text,operation_id::text,failure_code,expected_output_hash,observed_output_hash,created_at
		FROM workflow.tool_result_receipt_failure
		WHERE id=$1 AND analysis_run_id=$2 AND operation_id=$3 FOR SHARE`,
		string(failureID), string(analysisRunID), string(operationID),
	).Scan(&id, &storedOperationID, &record.Code, &record.ExpectedHash, &record.ActualHash, &record.CreatedAt); err != nil {
		return workspaceAnalysisReceiptFailureRecord{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis receipt failure")
	}
	parsed, err := parseWorkspaceAnalysisIDs(id, storedOperationID)
	if err != nil {
		return workspaceAnalysisReceiptFailureRecord{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	record.ID, record.OperationID = parsed[0], parsed[1]
	return record, nil
}

func canonicalWorkspaceAnalysisClarificationPublication(
	plan agentdomain.WorkspaceAnalysisPlanResult,
) (workspaceAnalysisPublication, error) {
	result := conversationdomain.ClarificationResult{
		ResultType:    agentdomain.ResultTypeClarification,
		SchemaID:      conversationdomain.ClarificationSchemaID,
		SchemaVersion: conversationdomain.ClarificationSchemaVersionV1,
		ModelRunRef:   plan.ModelRunRef,
		Payload: conversationdomain.ClarificationPayload{
			Reason: plan.Payload.ClarificationReason, Question: plan.Payload.ClarificationQuestion,
			SuggestedScopes: append([]string(nil), plan.Payload.SuggestedScopes...),
		},
	}
	document, err := json.Marshal(result)
	if err != nil {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	published, err := conversationdomain.CanonicalizeWorkspaceAnalysisPublishedResult(
		conversationdomain.AnswerPublicationClarificationRequired, conversationdomain.AnswerResultClarification, document,
	)
	if err != nil || published.ModelRunID == nil || *published.ModelRunID != plan.ModelRunRef {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return workspaceAnalysisPublication{
		Status: conversationdomain.AnswerPublicationClarificationRequired, ResultType: published.Type,
		ModelRunID: copyWorkspaceAnalysisFinalizerID(published.ModelRunID), Document: published.Document, ResultHash: published.Hash,
		RunStatus: agentdomain.WorkspaceAnalysisRunClarificationRequired, Reason: agentdomain.WorkspaceAnalysisRunNeedsClarification,
	}, nil
}

func canonicalWorkspaceAnalysisTerminationPublication(
	reason agentdomain.WorkspaceAnalysisRunTerminationReason,
	modelRunID *foundation.ID,
) (workspaceAnalysisPublication, error) {
	status, resultType, runStatus, err := workspaceAnalysisTerminationMatrix(reason)
	if err != nil {
		return workspaceAnalysisPublication{}, err
	}
	var document json.RawMessage
	if status == conversationdomain.AnswerPublicationRefused {
		result := conversationdomain.WorkspaceAnalysisRefusalResult{
			ResultType:    conversationdomain.AnswerResultWorkspaceAnalysisRefusal,
			SchemaID:      conversationdomain.WorkspaceAnalysisRefusalSchemaID,
			SchemaVersion: conversationdomain.WorkspaceAnalysisResultSchemaVersionV1,
			ModelRunRef:   copyWorkspaceAnalysisFinalizerID(modelRunID),
			Payload: conversationdomain.WorkspaceAnalysisRefusalPayload{
				ReasonCode: conversationdomain.WorkspaceAnalysisRefusalReason(reason),
				Summary:    workspaceAnalysisTerminationSummary(reason),
			},
		}
		document, err = json.Marshal(result)
	} else {
		result := conversationdomain.WorkspaceAnalysisTerminationResult{
			ResultType:    conversationdomain.AnswerResultWorkspaceAnalysisTermination,
			SchemaID:      conversationdomain.WorkspaceAnalysisTerminationSchemaID,
			SchemaVersion: conversationdomain.WorkspaceAnalysisResultSchemaVersionV1,
			ModelRunRef:   copyWorkspaceAnalysisFinalizerID(modelRunID),
			Payload: conversationdomain.WorkspaceAnalysisTerminationPayload{
				TerminationReason: conversationdomain.WorkspaceAnalysisTerminationReason(reason),
				Summary:           workspaceAnalysisTerminationSummary(reason),
			},
		}
		document, err = json.Marshal(result)
	}
	if err != nil {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	published, err := conversationdomain.CanonicalizeWorkspaceAnalysisPublishedResult(status, resultType, document)
	if err != nil || !equalOptionalWorkspaceAnalysisID(published.ModelRunID, modelRunID) {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return workspaceAnalysisPublication{
		Status: status, ResultType: published.Type, ModelRunID: copyWorkspaceAnalysisFinalizerID(published.ModelRunID),
		Document: published.Document, ResultHash: published.Hash, RunStatus: runStatus, Reason: reason,
	}, nil
}

func workspaceAnalysisTerminationMatrix(
	reason agentdomain.WorkspaceAnalysisRunTerminationReason,
) (conversationdomain.AnswerPublicationStatus, conversationdomain.AnswerResultType, agentdomain.WorkspaceAnalysisRunStatus, error) {
	switch reason {
	case agentdomain.WorkspaceAnalysisRunEvidenceInsufficient, agentdomain.WorkspaceAnalysisRunCitationInvalid,
		agentdomain.WorkspaceAnalysisRunFaithfulnessRejected, agentdomain.WorkspaceAnalysisRunModelRefused:
		return conversationdomain.AnswerPublicationRefused, conversationdomain.AnswerResultWorkspaceAnalysisRefusal,
			agentdomain.WorkspaceAnalysisRunRefused, nil
	case agentdomain.WorkspaceAnalysisRunBudgetExhausted, agentdomain.WorkspaceAnalysisRunReceiptInvalid,
		agentdomain.WorkspaceAnalysisRunResultUnknown, agentdomain.WorkspaceAnalysisRunDeadlineExceeded,
		agentdomain.WorkspaceAnalysisRunModelFailed, agentdomain.WorkspaceAnalysisRunToolFailed,
		agentdomain.WorkspaceAnalysisRunRuntimeFailed:
		return conversationdomain.WorkspaceAnalysisPublicationFailed, conversationdomain.AnswerResultWorkspaceAnalysisTermination,
			agentdomain.WorkspaceAnalysisRunFailed, nil
	case agentdomain.WorkspaceAnalysisRunCancellation:
		return conversationdomain.WorkspaceAnalysisPublicationCancelled, conversationdomain.AnswerResultWorkspaceAnalysisTermination,
			agentdomain.WorkspaceAnalysisRunCancelled, nil
	default:
		return "", "", "", invalid(conversationapplication.ErrorCodeWorkspaceAnalysisFinalizerInvalid, errors.New("workspace analysis termination reason is unsupported"))
	}
}

func workspaceAnalysisTerminationSummary(reason agentdomain.WorkspaceAnalysisRunTerminationReason) string {
	switch reason {
	case agentdomain.WorkspaceAnalysisRunEvidenceInsufficient:
		return "Available workspace evidence was insufficient to produce a grounded answer."
	case agentdomain.WorkspaceAnalysisRunCitationInvalid:
		return "The generated answer was not published because its citations could not be validated."
	case agentdomain.WorkspaceAnalysisRunFaithfulnessRejected:
		return "The generated answer was not published because its evidence review did not pass."
	case agentdomain.WorkspaceAnalysisRunModelRefused:
		return "The analysis could not produce a supported answer."
	case agentdomain.WorkspaceAnalysisRunBudgetExhausted:
		return "The workspace analysis stopped because its execution budget was exhausted."
	case agentdomain.WorkspaceAnalysisRunReceiptInvalid:
		return "The workspace analysis stopped because a required result could not be verified."
	case agentdomain.WorkspaceAnalysisRunResultUnknown:
		return "The workspace analysis stopped because an external call result could not be determined safely."
	case agentdomain.WorkspaceAnalysisRunDeadlineExceeded:
		return "The workspace analysis stopped because its execution deadline was reached."
	case agentdomain.WorkspaceAnalysisRunModelFailed:
		return "The workspace analysis stopped because a model call failed."
	case agentdomain.WorkspaceAnalysisRunToolFailed:
		return "The workspace analysis stopped because a required tool call failed."
	case agentdomain.WorkspaceAnalysisRunRuntimeFailed:
		return "The workspace analysis stopped because its runtime could not complete safely."
	case agentdomain.WorkspaceAnalysisRunCancellation:
		return "The workspace analysis was cancelled."
	default:
		return "The workspace analysis stopped."
	}
}

type workspaceAnalysisDraftRecord struct {
	ID            foundation.ID
	NodeAttemptID foundation.ID
	Status        string
}

type workspaceAnalysisPublicationSlot struct {
	Answer                conversationdomain.Answer
	ConversationVersion   int64
	ConversationCreatedAt time.Time
	ConversationUpdatedAt time.Time
	LastActivityAt        time.Time
	Draft                 *workspaceAnalysisDraftRecord
}

func lockWorkspaceAnalysisPublicationSlot(
	ctx context.Context,
	tx pgx.Tx,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
	expectedAnswerVersion int64,
) (workspaceAnalysisPublicationSlot, error) {
	var slot workspaceAnalysisPublicationSlot
	if err := tx.QueryRow(ctx, `SELECT version,created_at,updated_at,last_activity_at
		FROM agent.conversation WHERE workspace_id=$1 AND id=$2 FOR UPDATE`,
		string(lookup.WorkspaceID), string(lookup.ConversationID),
	).Scan(&slot.ConversationVersion, &slot.ConversationCreatedAt, &slot.ConversationUpdatedAt, &slot.LastActivityAt); err != nil {
		return workspaceAnalysisPublicationSlot{}, workspaceAnalysisFinalizerQueryError(err, "conversation publication slot")
	}
	view, err := loadBoundAnswer(ctx, tx, lookup.AnswerPublicationLookup, " FOR UPDATE OF a")
	if err != nil {
		return workspaceAnalysisPublicationSlot{}, err
	}
	if view.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending || view.Answer.Version != expectedAnswerVersion {
		return workspaceAnalysisPublicationSlot{}, conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis answer publication slot changed"))
	}
	slot.Answer = view.Answer
	var draftID, nodeAttemptID, status string
	err = tx.QueryRow(ctx, `SELECT id::text,node_attempt_id::text,status
		FROM agent.answer_draft_session
		WHERE workspace_id=$1 AND answer_id=$2 AND status IN ('ACTIVE','COMPLETED','DEGRADED')
		FOR UPDATE`, string(lookup.WorkspaceID), string(lookup.AnswerID)).Scan(&draftID, &nodeAttemptID, &status)
	switch {
	case err == nil:
		ids, parseErr := parseWorkspaceAnalysisIDs(draftID, nodeAttemptID)
		if parseErr != nil {
			return workspaceAnalysisPublicationSlot{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, parseErr)
		}
		slot.Draft = &workspaceAnalysisDraftRecord{ID: ids[0], NodeAttemptID: ids[1], Status: status}
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return workspaceAnalysisPublicationSlot{}, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return slot, nil
}

func validateSuccessDraft(draft *workspaceAnalysisDraftRecord, candidate agentdomain.WorkspaceAnalysisCandidate) error {
	if draft == nil {
		return nil
	}
	if draft.NodeAttemptID != candidate.NodeAttemptID || (draft.Status != "COMPLETED" && draft.Status != "DEGRADED") {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis candidate draft binding differs"))
	}
	return nil
}

func workspaceAnalysisPublicationTime(
	ctx context.Context,
	tx pgx.Tx,
	fence workspaceAnalysisFinalizationFence,
	slot workspaceAnalysisPublicationSlot,
	latestFactAt time.Time,
) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
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

func insertWorkspaceAnalysisSuccessProof(
	ctx context.Context,
	tx pgx.Tx,
	proofID foundation.ID,
	command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
	publication workspaceAnalysisPublication,
	now time.Time,
) error {
	_, err := tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_publication_proof(
		id,workspace_id,analysis_run_id,answer_id,workflow_run_id,finalization_node_run_id,finalization_node_attempt_id,
		candidate_id,candidate_hash,git_receipt_id,git_receipt_hash,validation_receipt_id,validation_receipt_hash,
		review_model_result_id,review_document_hash,published_document,published_result_hash,published_bytes,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		string(proofID), string(command.WorkspaceID), string(command.AnalysisRunID), string(command.AnswerID), string(command.WorkflowRunID),
		string(command.NodeRunID), string(command.NodeAttemptID), string(command.CandidateID), command.CandidateHash,
		string(command.GitReceiptID), command.GitReceiptHash, string(command.ValidationReceiptID), command.ValidationReceiptHash,
		string(command.ReviewModelResultID), command.ReviewModelResultHash, []byte(publication.Document), publication.ResultHash,
		int64(len(publication.Document)), now,
	)
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

func insertWorkspaceAnalysisTerminationProof(
	ctx context.Context,
	tx pgx.Tx,
	proofID foundation.ID,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
	publication workspaceAnalysisPublication,
	now time.Time,
) error {
	var operationID, artifactKind, artifactID, artifactHash any
	var requestedModelCalls, requestedToolCalls, requestedSourceReads, requestedInputTokens, requestedOutputTokens, requestedCost any
	var failureCode, failureID, expectedHash, actualHash any
	if command.OperationID != nil {
		operationID = string(*command.OperationID)
	}
	if command.Artifact != nil {
		artifactKind, artifactID, artifactHash = string(command.Artifact.Kind), string(command.Artifact.ID), command.Artifact.Hash
	}
	if command.BudgetRequest != nil {
		requestedModelCalls, requestedToolCalls, requestedSourceReads = command.BudgetRequest.ModelCalls, command.BudgetRequest.ToolCalls, command.BudgetRequest.SourceReads
		requestedInputTokens, requestedOutputTokens = command.BudgetRequest.InputTokens, command.BudgetRequest.OutputTokens
		if command.BudgetRequest.RequestedCostMicrounits != nil {
			requestedCost = *command.BudgetRequest.RequestedCostMicrounits
		}
	}
	if command.ReceiptFailure != nil {
		failureCode, failureID, expectedHash = string(command.ReceiptFailure.Code), string(command.ReceiptFailure.ID), command.ReceiptFailure.ExpectedHash
		if command.ReceiptFailure.ActualHash != nil {
			actualHash = *command.ReceiptFailure.ActualHash
		}
	}
	var publishedModelRunID any
	if publication.ModelRunID != nil {
		publishedModelRunID = string(*publication.ModelRunID)
	}
	_, err := tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_termination_proof(
		id,workspace_id,analysis_run_id,answer_id,workflow_run_id,terminal_node_run_id,terminal_node_attempt_id,reason,
		operation_id,artifact_kind,artifact_id,artifact_hash,
		requested_model_calls,requested_tool_calls,requested_source_reads,requested_input_tokens,requested_output_tokens,requested_cost_microunits,
		receipt_failure_code,receipt_failure_id,expected_hash,actual_hash,published_model_run_id,
		published_document,published_result_hash,published_bytes,checked_at,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28)`,
		string(proofID), string(command.WorkspaceID), string(command.AnalysisRunID), string(command.AnswerID), string(command.WorkflowRunID),
		string(command.NodeRunID), string(command.NodeAttemptID), string(command.Reason), operationID, artifactKind, artifactID, artifactHash,
		requestedModelCalls, requestedToolCalls, requestedSourceReads, requestedInputTokens, requestedOutputTokens, requestedCost,
		failureCode, failureID, expectedHash, actualHash, publishedModelRunID, []byte(publication.Document), publication.ResultHash,
		int64(len(publication.Document)), now, now,
	)
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

func publishWorkspaceAnalysisDraft(ctx context.Context, tx pgx.Tx, draft *workspaceAnalysisDraftRecord, now time.Time) error {
	if draft == nil {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.answer_draft_session
		SET status='PUBLISHED',updated_at=$2,completed_at=COALESCE(completed_at,$2)
		WHERE id=$1 AND status IN ('COMPLETED','DEGRADED')`, string(draft.ID), now)
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis draft publication CAS failed"))
	}
	return nil
}

func abortWorkspaceAnalysisDraft(ctx context.Context, tx pgx.Tx, draft *workspaceAnalysisDraftRecord, now time.Time) error {
	if draft == nil {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.answer_draft_session
		SET status='ABORTED',updated_at=$2,completed_at=COALESCE(completed_at,$2)
		WHERE id=$1 AND status IN ('ACTIVE','COMPLETED','DEGRADED')`, string(draft.ID), now)
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis draft abort CAS failed"))
	}
	return nil
}

func updateWorkspaceAnalysisRunSuccess(
	ctx context.Context,
	tx pgx.Tx,
	fence workspaceAnalysisFinalizationFence,
	facts workspaceAnalysisSuccessFacts,
	now time.Time,
) error {
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
		status='succeeded',termination_reason='COMPLETED',validation_receipt_id=$1,review_model_run_id=$2,
		version=version+1,updated_at=$3,completed_at=$3
		WHERE id=$4 AND status='running' AND version=$5`,
		string(facts.ValidationReceipt.ID), string(facts.ReviewResult.ModelRunID), now,
		string(facts.Candidate.AnalysisRunID), fence.RunVersion,
	)
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis success run CAS failed"))
	}
	return nil
}

func updateWorkspaceAnalysisRunTermination(
	ctx context.Context,
	tx pgx.Tx,
	fence workspaceAnalysisFinalizationFence,
	publication workspaceAnalysisPublication,
	now time.Time,
) error {
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
		status=$1,termination_reason=$2,validation_receipt_id=NULL,review_model_run_id=NULL,
		version=version+1,updated_at=$3,completed_at=$3
		WHERE id=$4 AND status=$5 AND version=$6`,
		string(publication.RunStatus), string(publication.Reason), now,
		string(fence.RunID), string(fence.RunStatus), fence.RunVersion,
	)
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis termination run CAS failed"))
	}
	return nil
}

func updateWorkspaceAnalysisAnswer(
	ctx context.Context,
	tx pgx.Tx,
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
	tag, err := tx.Exec(ctx, `UPDATE agent.answer SET
		model_run_id=$1,publication_status=$2,result_type=$3,result=$4::jsonb,result_hash=$5,retrieval_summary=NULL,
		version=version+1,updated_at=$6,published_at=$6
		WHERE workspace_id=$7 AND id=$8 AND publication_status='pending' AND version=$9`,
		modelRunID, string(answer.PublicationStatus), string(answer.ResultType), string(answer.Result), answer.ResultHash, now,
		string(answer.WorkspaceID), string(answer.ID), pending.Version,
	)
	if err != nil {
		return conversationdomain.Answer{}, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return conversationdomain.Answer{}, conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis answer publication CAS failed"))
	}
	return answer, nil
}

func updateWorkspaceAnalysisConversation(
	ctx context.Context,
	tx pgx.Tx,
	slot workspaceAnalysisPublicationSlot,
	now time.Time,
) error {
	tag, err := tx.Exec(ctx, `UPDATE agent.conversation SET version=version+1,last_activity_at=$3,updated_at=$3
		WHERE workspace_id=$1 AND id=$2 AND version=$4`,
		string(slot.Answer.WorkspaceID), string(slot.Answer.ConversationID), now, slot.ConversationVersion,
	)
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis conversation activity CAS failed"))
	}
	return nil
}

func (finalizer *WorkspaceAnalysisFinalizer) appendWorkspaceAnalysisTerminalEvents(
	ctx context.Context,
	tx pgx.Tx,
	analysisRunID foundation.ID,
	runStatus agentdomain.WorkspaceAnalysisRunStatus,
	answer conversationdomain.Answer,
	citationCount int64,
	now time.Time,
	wantReplay bool,
) error {
	if err := appendWorkspaceAnalysisTerminalEvents(
		ctx, tx, finalizer.events, analysisRunID, runStatus, answer, citationCount, now, wantReplay,
	); err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

// appendWorkspaceAnalysisTerminalEvents 保留既有 Answer 事件，并同一事务追加 Analysis Run 的安全失效通知。
// 任一事件的缺失或绑定漂移都会使整笔事务失败，不能把事件当成可自行修复的第二事实源。
func appendWorkspaceAnalysisTerminalEvents(
	ctx context.Context,
	tx pgx.Tx,
	appender eventsapplication.Appender,
	analysisRunID foundation.ID,
	runStatus agentdomain.WorkspaceAnalysisRunStatus,
	answer conversationdomain.Answer,
	citationCount int64,
	now time.Time,
	wantReplay bool,
) error {
	for _, request := range workspaceAnalysisTerminalEventRequests(answer, analysisRunID, runStatus, citationCount, now) {
		_, replayed, err := appender.AppendTx(ctx, tx, request)
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

func workspaceAnalysisTerminalEventRequests(
	answer conversationdomain.Answer,
	analysisRunID foundation.ID,
	runStatus agentdomain.WorkspaceAnalysisRunStatus,
	citationCount int64,
	now time.Time,
) []eventsdomain.AppendRequest {
	answerID, conversationID, workflowRunID := answer.ID, answer.ConversationID, answer.WorkflowRunID
	questionID := answer.QuestionID
	eventType := "answer." + string(answer.PublicationStatus)
	summary := eventsdomain.PayloadSummary{
		ConversationID: &conversationID, WorkflowRunID: &workflowRunID, QuestionID: &questionID, AnswerID: &answerID,
		PublicationStatus: string(answer.PublicationStatus), ResultType: string(answer.ResultType), CitationCount: &citationCount,
	}
	if answer.ModelRunID != nil {
		modelRunID := *answer.ModelRunID
		summary.ModelRunID = &modelRunID
	}
	answerEvent := eventsdomain.AppendRequest{
		WorkspaceID: answer.WorkspaceID, ConversationID: &conversationID, WorkflowRunID: &workflowRunID,
		Type: eventType, ResourceRef: "answer:" + string(answer.ID), ResourceVersion: answer.Version,
		PayloadSummary: summary, SchemaVersion: 1,
		SourceEventRef: eventType + ":" + string(answer.ID) + ":v2", OccurredAt: now,
	}
	analysisSummary := eventsdomain.PayloadSummary{
		ConversationID: &conversationID, WorkflowRunID: &workflowRunID, QuestionID: &questionID, AnswerID: &answerID,
		Status: string(runStatus), PublicationStatus: string(answer.PublicationStatus),
		ResultType: string(answer.ResultType), CitationCount: &citationCount,
	}
	analysisEvent := eventsdomain.AppendRequest{
		WorkspaceID: answer.WorkspaceID, ConversationID: &conversationID, WorkflowRunID: &workflowRunID,
		Type: "workspace_analysis.terminated", ResourceRef: "workspace_analysis:" + string(analysisRunID),
		ResourceVersion: answer.Version, PayloadSummary: analysisSummary, SchemaVersion: 1,
		SourceEventRef: "workspace_analysis.terminated:" + string(analysisRunID) + ":v1", OccurredAt: now,
	}
	return []eventsdomain.AppendRequest{answerEvent, analysisEvent}
}

func forceWorkspaceAnalysisPublicationConstraints(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

func workspaceAnalysisPublicationOutput(
	answer conversationdomain.Answer,
	proofID foundation.ID,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, error) {
	if err := conversationdomain.ValidateAnswer(answer); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	output := conversationworkflow.WorkspaceAnalysisPublicationOutput{
		SchemaVersion: conversationworkflow.WorkspaceAnalysisOutputSchemaVersion,
		AnswerID:      answer.ID, PublicationStatus: answer.PublicationStatus, ResultType: answer.ResultType,
		ModelRunID: copyWorkspaceAnalysisFinalizerID(answer.ModelRunID), ResultHash: answer.ResultHash, ProofID: proofID,
	}
	if _, err := conversationworkflow.EncodeWorkspaceAnalysisPublicationOutput(output); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return output, nil
}

type workspaceAnalysisSuccessProofRecord struct {
	ID                    foundation.ID
	NodeRunID             foundation.ID
	NodeAttemptID         foundation.ID
	CandidateID           foundation.ID
	CandidateHash         string
	GitReceiptID          foundation.ID
	GitReceiptHash        string
	ValidationReceiptID   foundation.ID
	ValidationReceiptHash string
	ReviewResultID        foundation.ID
	ReviewResultHash      string
	PublishedDocument     json.RawMessage
	PublishedResultHash   string
}

type workspaceAnalysisTerminationProofRecord struct {
	ID                    foundation.ID
	NodeRunID             foundation.ID
	NodeAttemptID         foundation.ID
	Reason                agentdomain.WorkspaceAnalysisRunTerminationReason
	OperationID           *foundation.ID
	ArtifactKind          *conversationapplication.WorkspaceAnalysisTerminationArtifactKind
	ArtifactID            *foundation.ID
	ArtifactHash          *string
	RequestedModelCalls   *int
	RequestedToolCalls    *int
	RequestedSourceReads  *int
	RequestedInputTokens  *int64
	RequestedOutputTokens *int64
	RequestedCost         *int64
	FailureCode           *conversationapplication.WorkspaceAnalysisReceiptFailureCode
	FailureID             *foundation.ID
	ExpectedHash          *string
	ActualHash            *string
	PublishedModelRunID   *foundation.ID
	PublishedDocument     json.RawMessage
	PublishedResultHash   string
}

type workspaceAnalysisProofState struct {
	Answer      conversationdomain.Answer
	RunStatus   agentdomain.WorkspaceAnalysisRunStatus
	RunReason   *agentdomain.WorkspaceAnalysisRunTerminationReason
	Success     *workspaceAnalysisSuccessProofRecord
	Termination *workspaceAnalysisTerminationProofRecord
}

func loadWorkspaceAnalysisProofState(
	ctx context.Context,
	tx pgx.Tx,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
	answer conversationdomain.Answer,
) (workspaceAnalysisProofState, error) {
	state := workspaceAnalysisProofState{Answer: answer}
	var runReason *string
	if err := tx.QueryRow(ctx, `SELECT status,termination_reason FROM agent.workspace_analysis_run
		WHERE id=$1 AND workspace_id=$2 AND conversation_id=$3 AND question_id=$4 AND answer_id=$5 AND workflow_run_id=$6`,
		string(lookup.AnalysisRunID), string(lookup.WorkspaceID), string(lookup.ConversationID), string(lookup.QuestionID),
		string(lookup.AnswerID), string(lookup.WorkflowRunID),
	).Scan(&state.RunStatus, &runReason); err != nil {
		return workspaceAnalysisProofState{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis publication state")
	}
	if runReason != nil {
		reason := agentdomain.WorkspaceAnalysisRunTerminationReason(*runReason)
		state.RunReason = &reason
	}
	success, found, err := loadWorkspaceAnalysisSuccessProof(ctx, tx, lookup)
	if err != nil {
		return workspaceAnalysisProofState{}, err
	}
	if found {
		state.Success = &success
	}
	termination, found, err := loadWorkspaceAnalysisTerminationProof(ctx, tx, lookup)
	if err != nil {
		return workspaceAnalysisProofState{}, err
	}
	if found {
		state.Termination = &termination
	}
	return state, nil
}

func loadWorkspaceAnalysisSuccessProof(
	ctx context.Context,
	tx pgx.Tx,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) (workspaceAnalysisSuccessProofRecord, bool, error) {
	var record workspaceAnalysisSuccessProofRecord
	var id, nodeRunID, nodeAttemptID, candidateID, gitReceiptID, validationReceiptID, reviewResultID string
	err := tx.QueryRow(ctx, `SELECT
		id::text,finalization_node_run_id::text,finalization_node_attempt_id::text,
		candidate_id::text,candidate_hash,git_receipt_id::text,git_receipt_hash,
		validation_receipt_id::text,validation_receipt_hash,review_model_result_id::text,review_document_hash,
		published_document,published_result_hash
		FROM agent.workspace_analysis_publication_proof
		WHERE analysis_run_id=$1 AND workspace_id=$2 AND answer_id=$3 AND workflow_run_id=$4`,
		string(lookup.AnalysisRunID), string(lookup.WorkspaceID), string(lookup.AnswerID), string(lookup.WorkflowRunID),
	).Scan(
		&id, &nodeRunID, &nodeAttemptID, &candidateID, &record.CandidateHash, &gitReceiptID, &record.GitReceiptHash,
		&validationReceiptID, &record.ValidationReceiptHash, &reviewResultID, &record.ReviewResultHash,
		&record.PublishedDocument, &record.PublishedResultHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return workspaceAnalysisSuccessProofRecord{}, false, nil
	}
	if err != nil {
		return workspaceAnalysisSuccessProofRecord{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	ids, err := parseWorkspaceAnalysisIDs(id, nodeRunID, nodeAttemptID, candidateID, gitReceiptID, validationReceiptID, reviewResultID)
	if err != nil {
		return workspaceAnalysisSuccessProofRecord{}, false, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	record.ID, record.NodeRunID, record.NodeAttemptID, record.CandidateID = ids[0], ids[1], ids[2], ids[3]
	record.GitReceiptID, record.ValidationReceiptID, record.ReviewResultID = ids[4], ids[5], ids[6]
	return record, true, nil
}

func loadWorkspaceAnalysisTerminationProof(
	ctx context.Context,
	tx pgx.Tx,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) (workspaceAnalysisTerminationProofRecord, bool, error) {
	var (
		record                                                  workspaceAnalysisTerminationProofRecord
		id, nodeRunID                                           string
		nodeAttemptID                                           *string
		operationID, artifactID, failureID, publishedModelRunID *string
		artifactKind, failureCode                               *string
	)
	err := tx.QueryRow(ctx, `SELECT
		id::text,terminal_node_run_id::text,terminal_node_attempt_id::text,reason,operation_id::text,
		artifact_kind,artifact_id::text,artifact_hash,
		requested_model_calls,requested_tool_calls,requested_source_reads,requested_input_tokens,requested_output_tokens,requested_cost_microunits,
		receipt_failure_code,receipt_failure_id::text,expected_hash,actual_hash,published_model_run_id::text,
		published_document,published_result_hash
		FROM agent.workspace_analysis_termination_proof
		WHERE analysis_run_id=$1 AND workspace_id=$2 AND answer_id=$3 AND workflow_run_id=$4`,
		string(lookup.AnalysisRunID), string(lookup.WorkspaceID), string(lookup.AnswerID), string(lookup.WorkflowRunID),
	).Scan(
		&id, &nodeRunID, &nodeAttemptID, &record.Reason, &operationID,
		&artifactKind, &artifactID, &record.ArtifactHash,
		&record.RequestedModelCalls, &record.RequestedToolCalls, &record.RequestedSourceReads,
		&record.RequestedInputTokens, &record.RequestedOutputTokens, &record.RequestedCost,
		&failureCode, &failureID, &record.ExpectedHash, &record.ActualHash, &publishedModelRunID,
		&record.PublishedDocument, &record.PublishedResultHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
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

func workspaceAnalysisOutputFromState(
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
	state workspaceAnalysisProofState,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	answer := state.Answer
	if answer.PublicationStatus == conversationdomain.AnswerPublicationPending {
		if state.Success != nil || state.Termination != nil || !state.RunStatus.Active() || state.RunReason != nil {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
				consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("pending workspace analysis publication is split"))
		}
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, nil
	}
	if err := conversationdomain.ValidateAnswer(answer); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	if state.Success != nil && state.Termination != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
			consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis answer has two publication proofs"))
	}
	var proofID foundation.ID
	if state.Success != nil {
		proof := state.Success
		if proof.NodeRunID != lookup.NodeRunID || answer.PublicationStatus != conversationdomain.AnswerPublicationCompleted ||
			answer.ResultType != conversationdomain.AnswerResultWorkspaceAnalysis || state.RunStatus != agentdomain.WorkspaceAnalysisRunSucceeded ||
			state.RunReason == nil || *state.RunReason != agentdomain.WorkspaceAnalysisRunCompleted ||
			answer.ResultHash != proof.PublishedResultHash || !bytes.Equal(answer.Result, proof.PublishedDocument) {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
				consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis success proof is split"))
		}
		proofID = proof.ID
	} else if state.Termination != nil {
		proof := state.Termination
		status, resultType, runStatus, matrixErr := workspaceAnalysisTerminationMatrix(proof.Reason)
		if proof.Reason == agentdomain.WorkspaceAnalysisRunNeedsClarification {
			status, resultType, runStatus = conversationdomain.AnswerPublicationClarificationRequired,
				conversationdomain.AnswerResultClarification, agentdomain.WorkspaceAnalysisRunClarificationRequired
			matrixErr = nil
		}
		if matrixErr != nil || proof.NodeRunID != lookup.NodeRunID || answer.PublicationStatus != status || answer.ResultType != resultType ||
			state.RunStatus != runStatus || state.RunReason == nil || *state.RunReason != proof.Reason ||
			answer.ResultHash != proof.PublishedResultHash || !bytes.Equal(answer.Result, proof.PublishedDocument) ||
			!equalOptionalWorkspaceAnalysisID(answer.ModelRunID, proof.PublishedModelRunID) {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
				consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis termination proof is split"))
		}
		proofID = proof.ID
	} else {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
			consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("terminal workspace analysis answer has no publication proof"))
	}
	output, err := workspaceAnalysisPublicationOutput(answer, proofID)
	return output, err == nil, err
}

func (finalizer *WorkspaceAnalysisFinalizer) replaySuccessTx(
	ctx context.Context,
	tx pgx.Tx,
	command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, error) {
	state, err := lockWorkspaceAnalysisTerminalPublication(ctx, tx, command.WorkspaceAnalysisPublicationLookup)
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
	if err := validateRetainedWorkspaceAnalysisDraft(ctx, tx, command.WorkspaceID, command.AnswerID, "PUBLISHED", &proof.CandidateID); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	citationCount, err := workspaceAnalysisTerminalCitationCount(state.Answer)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	if err := finalizer.appendWorkspaceAnalysisTerminalEvents(
		ctx, tx, command.AnalysisRunID, state.RunStatus, state.Answer, citationCount, *state.Answer.PublishedAt, true,
	); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	return output, nil
}

func (finalizer *WorkspaceAnalysisFinalizer) replayTerminationTx(
	ctx context.Context,
	tx pgx.Tx,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, error) {
	state, err := lockWorkspaceAnalysisTerminalPublication(ctx, tx, command.WorkspaceAnalysisPublicationLookup)
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
	if err := validateRetainedWorkspaceAnalysisDraft(ctx, tx, command.WorkspaceID, command.AnswerID, "ABORTED", nil); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	citationCount, err := workspaceAnalysisTerminalCitationCount(state.Answer)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	if err := finalizer.appendWorkspaceAnalysisTerminalEvents(
		ctx, tx, command.AnalysisRunID, state.RunStatus, state.Answer, citationCount, *state.Answer.PublishedAt, true,
	); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}
	return output, nil
}

func workspaceAnalysisTerminalCitationCount(answer conversationdomain.Answer) (int64, error) {
	published, err := conversationdomain.CanonicalizeWorkspaceAnalysisPublishedResult(
		answer.PublicationStatus, answer.ResultType, answer.Result,
	)
	if err != nil {
		return 0, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	if published.Type != conversationdomain.AnswerResultWorkspaceAnalysis {
		return 0, nil
	}
	var result conversationdomain.WorkspaceAnalysisAnswerResult
	if err := json.Unmarshal(published.Document, &result); err != nil || result.Validate() != nil {
		return 0, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis replay citation projection is invalid"))
	}
	return int64(len(result.Payload.Citations)), nil
}

func lockWorkspaceAnalysisTerminalPublication(
	ctx context.Context,
	tx pgx.Tx,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) (workspaceAnalysisProofState, error) {
	var conversationVersion int64
	if err := tx.QueryRow(ctx, `SELECT version FROM agent.conversation
		WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(lookup.WorkspaceID), string(lookup.ConversationID)).Scan(&conversationVersion); err != nil {
		return workspaceAnalysisProofState{}, workspaceAnalysisFinalizerQueryError(err, "terminal conversation publication slot")
	}
	view, err := loadBoundAnswer(ctx, tx, lookup.AnswerPublicationLookup, " FOR UPDATE OF a")
	if err != nil {
		return workspaceAnalysisProofState{}, err
	}
	if err := rejectActiveWorkspaceAnalysisDraft(ctx, tx, lookup.WorkspaceID, lookup.AnswerID); err != nil {
		return workspaceAnalysisProofState{}, err
	}
	return loadWorkspaceAnalysisProofState(ctx, tx, lookup, view.Answer)
}

func rejectActiveWorkspaceAnalysisDraft(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID, answerID foundation.ID,
) error {
	var activeDraftID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM agent.answer_draft_session
		WHERE workspace_id=$1 AND answer_id=$2 AND status IN ('ACTIVE','COMPLETED','DEGRADED') FOR UPDATE`,
		string(workspaceID), string(answerID)).Scan(&activeDraftID)
	if err == nil {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("terminal workspace analysis retained an active draft"))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

func validateRetainedWorkspaceAnalysisDraft(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID, answerID foundation.ID,
	expectedStatus string,
	candidateID *foundation.ID,
) error {
	var publishedCount, matchingPublishedCount int64
	if candidateID == nil {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM agent.answer_draft_session
			WHERE workspace_id=$1 AND answer_id=$2 AND status='PUBLISHED'`,
			string(workspaceID), string(answerID),
		).Scan(&publishedCount); err != nil {
			return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
		}
	} else {
		if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (
			WHERE node_attempt_id=(SELECT node_attempt_id FROM agent.workspace_analysis_candidate WHERE id=$3)
		) FROM agent.answer_draft_session
		WHERE workspace_id=$1 AND answer_id=$2 AND status='PUBLISHED'`,
			string(workspaceID), string(answerID), string(*candidateID),
		).Scan(&publishedCount, &matchingPublishedCount); err != nil {
			return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
		}
	}
	if (expectedStatus == "ABORTED" && publishedCount != 0) ||
		(expectedStatus == "PUBLISHED" && publishedCount != matchingPublishedCount) || publishedCount > 1 {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis draft publication is split"))
	}

	query := `SELECT draft.status
		FROM agent.answer_draft_session AS draft
		WHERE draft.workspace_id=$1 AND draft.answer_id=$2 AND draft.status IN ('PUBLISHED','ABORTED')`
	arguments := []any{string(workspaceID), string(answerID)}
	if candidateID != nil {
		query += ` AND draft.node_attempt_id=(SELECT node_attempt_id FROM agent.workspace_analysis_candidate WHERE id=$3)`
		arguments = append(arguments, string(*candidateID))
	}
	query += ` ORDER BY draft.generation DESC LIMIT 1 FOR SHARE OF draft`
	var status string
	err := tx.QueryRow(ctx, query, arguments...).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
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

func workspaceAnalysisTerminationArtifactMatchesProof(
	artifact *conversationapplication.WorkspaceAnalysisTerminationArtifact,
	proof *workspaceAnalysisTerminationProofRecord,
) bool {
	if artifact == nil {
		return proof.ArtifactKind == nil && proof.ArtifactID == nil && proof.ArtifactHash == nil
	}
	return proof.ArtifactKind != nil && *proof.ArtifactKind == artifact.Kind && proof.ArtifactID != nil && *proof.ArtifactID == artifact.ID &&
		proof.ArtifactHash != nil && *proof.ArtifactHash == artifact.Hash
}

func workspaceAnalysisBudgetRequestMatchesProof(
	request *conversationapplication.WorkspaceAnalysisBudgetRequest,
	proof *workspaceAnalysisTerminationProofRecord,
) bool {
	if request == nil {
		return proof.RequestedModelCalls == nil && proof.RequestedToolCalls == nil && proof.RequestedSourceReads == nil &&
			proof.RequestedInputTokens == nil && proof.RequestedOutputTokens == nil && proof.RequestedCost == nil
	}
	return proof.RequestedModelCalls != nil && *proof.RequestedModelCalls == request.ModelCalls &&
		proof.RequestedToolCalls != nil && *proof.RequestedToolCalls == request.ToolCalls &&
		proof.RequestedSourceReads != nil && *proof.RequestedSourceReads == request.SourceReads &&
		proof.RequestedInputTokens != nil && *proof.RequestedInputTokens == request.InputTokens &&
		proof.RequestedOutputTokens != nil && *proof.RequestedOutputTokens == request.OutputTokens &&
		request.RequestedCostMicrounits == nil && proof.RequestedCost == nil
}

func workspaceAnalysisReceiptFailureMatchesProof(
	failure *conversationapplication.WorkspaceAnalysisReceiptFailure,
	proof *workspaceAnalysisTerminationProofRecord,
) bool {
	if failure == nil {
		return proof.FailureCode == nil && proof.FailureID == nil && proof.ExpectedHash == nil && proof.ActualHash == nil
	}
	return proof.FailureCode != nil && *proof.FailureCode == failure.Code && proof.FailureID != nil && *proof.FailureID == failure.ID &&
		proof.ExpectedHash != nil && *proof.ExpectedHash == failure.ExpectedHash && equalOptionalWorkspaceAnalysisString(proof.ActualHash, failure.ActualHash)
}

func (finalizer *WorkspaceAnalysisFinalizer) recoverWorkspaceAnalysisCommit(
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

func cloneWorkspaceAnalysisTerminationCommand(
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
) conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand {
	if command.OperationID != nil {
		value := *command.OperationID
		command.OperationID = &value
	}
	if command.Artifact != nil {
		value := *command.Artifact
		command.Artifact = &value
	}
	if command.BudgetRequest != nil {
		value := *command.BudgetRequest
		if value.RequestedCostMicrounits != nil {
			cost := *value.RequestedCostMicrounits
			value.RequestedCostMicrounits = &cost
		}
		command.BudgetRequest = &value
	}
	if command.ReceiptFailure != nil {
		value := *command.ReceiptFailure
		if value.ActualHash != nil {
			hash := *value.ActualHash
			value.ActualHash = &hash
		}
		command.ReceiptFailure = &value
	}
	return command
}

func parseWorkspaceAnalysisIDs(values ...string) ([]foundation.ID, error) {
	parsed := make([]foundation.ID, len(values))
	seen := make(map[foundation.ID]struct{}, len(values))
	for index, value := range values {
		id, err := parseCanonicalID(value)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, errors.New("workspace analysis persisted identity is reused")
		}
		seen[id] = struct{}{}
		parsed[index] = id
	}
	return parsed, nil
}

func workspaceAnalysisDocumentHash(document []byte) string {
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:])
}

func laterWorkspaceAnalysisTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func copyWorkspaceAnalysisFinalizerID(source *foundation.ID) *foundation.ID {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func equalOptionalWorkspaceAnalysisID(left, right *foundation.ID) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func equalOptionalWorkspaceAnalysisString(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func workspaceAnalysisFinalizerQueryError(err error, fact string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, fmt.Errorf("%s is missing", fact))
	}
	return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
}

var _ conversationapplication.WorkspaceAnalysisFinalizer = (*WorkspaceAnalysisFinalizer)(nil)
