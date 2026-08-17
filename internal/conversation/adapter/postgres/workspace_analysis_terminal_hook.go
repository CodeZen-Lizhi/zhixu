package postgres

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
)

// WorkspaceAnalysisCancellationTerminalHook 在 Runtime 持久化取消检查点后关闭待发布的 Workspace Analysis Answer。
// Runtime 持有事务，并且仍是唯一的提交方。
type WorkspaceAnalysisCancellationTerminalHook struct {
	events         eventsapplication.Appender
	ids            foundation.IDGenerator
	audit          WorkspaceAnalysisAuditRecorder
	workerActorRef string
}

// NewWorkspaceAnalysisCancellationTerminalHook 创建供 Workflow Runtime 使用的事务内取消终结 Hook。
func NewWorkspaceAnalysisCancellationTerminalHook(
	events eventsapplication.Appender,
	ids foundation.IDGenerator,
) (*WorkspaceAnalysisCancellationTerminalHook, error) {
	return newWorkspaceAnalysisCancellationTerminalHook(events, ids, nil, "")
}

// NewWorkspaceAnalysisCancellationTerminalHookWithAudit 创建带事务内取消终态审计的 Runtime Hook。
func NewWorkspaceAnalysisCancellationTerminalHookWithAudit(
	events eventsapplication.Appender,
	ids foundation.IDGenerator,
	audit WorkspaceAnalysisAuditRecorder,
	workerActorRef string,
) (*WorkspaceAnalysisCancellationTerminalHook, error) {
	if isNilInterface(audit) || !validWorkspaceAnalysisAuditActorRef(workerActorRef) {
		return nil, dependency(
			ErrorCodeWorkspaceAnalysisFinalizeUnavailable,
			errors.New("workspace analysis cancellation audit dependency is invalid"),
		)
	}
	return newWorkspaceAnalysisCancellationTerminalHook(events, ids, audit, workerActorRef)
}

func newWorkspaceAnalysisCancellationTerminalHook(
	events eventsapplication.Appender,
	ids foundation.IDGenerator,
	audit WorkspaceAnalysisAuditRecorder,
	workerActorRef string,
) (*WorkspaceAnalysisCancellationTerminalHook, error) {
	if isNilInterface(events) || isNilInterface(ids) {
		return nil, dependency(
			ErrorCodeWorkspaceAnalysisFinalizeUnavailable,
			errors.New("workspace analysis cancellation terminal dependencies are incomplete"),
		)
	}
	return &WorkspaceAnalysisCancellationTerminalHook{
		events: events, ids: ids, audit: audit, workerActorRef: workerActorRef,
	}, nil
}

// OnWorkflowNodeTerminal 终结 Workspace Analysis 的取消和无 Operation 运行时失败，其余事件保持 no-op。
func (hook *WorkspaceAnalysisCancellationTerminalHook) OnWorkflowNodeTerminal(
	ctx context.Context,
	transaction any,
	event workflowapplication.WorkflowNodeTerminalEvent,
) error {
	nodeKey, owned := workspaceAnalysisCancellationNodeKey(event.NodeKind)
	if !owned || (event.Outcome != workflowapplication.WorkflowTerminalOutcomeCancelled &&
		event.Outcome != workflowapplication.WorkflowTerminalOutcomeFailed) {
		return nil
	}
	if hook == nil || isNilInterface(hook.events) || isNilInterface(hook.ids) {
		return dependency(
			ErrorCodeWorkspaceAnalysisFinalizeUnavailable,
			errors.New("workspace analysis cancellation terminal hook is unavailable"),
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, ok := transaction.(pgx.Tx)
	if !ok || isNilInterface(tx) {
		return invalid(
			conversationapplication.ErrorCodeWorkspaceAnalysisFinalizerInvalid,
			errors.New("workspace analysis cancellation transaction is invalid"),
		)
	}
	if event.Outcome == workflowapplication.WorkflowTerminalOutcomeFailed {
		return hook.onWorkspaceAnalysisRuntimeFailure(ctx, tx, event, nodeKey)
	}
	if err := validateWorkspaceAnalysisCancellationEvent(event); err != nil {
		return err
	}
	event.TerminalAt = event.TerminalAt.UTC().Truncate(time.Microsecond)

	runtime, err := lockWorkspaceAnalysisCancellationRuntime(ctx, tx, event, nodeKey)
	if err != nil {
		return err
	}
	if runtime.WorkflowStatus != workflowdomain.RunStatusCancelled {
		if runtime.WorkflowCancelRequestedAt != nil &&
			!workflowdomain.IsTerminalRunStatus(runtime.WorkflowStatus) {
			return nil
		}
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis node cancellation is not bound to a cancelling workflow"),
		)
	}

	run, err := lockWorkspaceAnalysisCancellationRun(ctx, tx, event)
	if err != nil {
		return err
	}
	if err := lockWorkspaceAnalysisCancellationOperations(ctx, tx, run.ID, !run.Status.Terminal()); err != nil {
		return err
	}
	slot, err := lockWorkspaceAnalysisCancellationSlot(ctx, tx, run)
	if err != nil {
		return err
	}
	if run.Status.Terminal() || slot.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending {
		return hook.validateWorkspaceAnalysisCancellationReplay(ctx, tx, event, run, slot)
	}
	if run.Status != agentdomain.WorkspaceAnalysisRunQueued && run.Status != agentdomain.WorkspaceAnalysisRunRunning {
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis cancellation run is not active"),
		)
	}

	publication, err := canonicalWorkspaceAnalysisTerminationPublication(
		agentdomain.WorkspaceAnalysisRunCancellation,
		nil,
	)
	if err != nil {
		return err
	}
	now, err := workspaceAnalysisCancellationPublicationTime(ctx, tx, event.TerminalAt, run, slot)
	if err != nil {
		return err
	}
	proofID, err := hook.ids.New()
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if err := insertWorkspaceAnalysisRuntimeCancellationProof(
		ctx, tx, proofID, event, run, publication, now,
	); err != nil {
		return err
	}
	if err := abortWorkspaceAnalysisDraft(ctx, tx, slot.Draft, now); err != nil {
		return err
	}
	fence := workspaceAnalysisFinalizationFence{
		RunID: run.ID, RunStatus: run.Status, RunVersion: run.Version,
	}
	if err := updateWorkspaceAnalysisRunTermination(ctx, tx, fence, publication, now); err != nil {
		return err
	}
	answer, err := updateWorkspaceAnalysisAnswer(ctx, tx, slot.Answer, publication, now)
	if err != nil {
		return err
	}
	if err := updateWorkspaceAnalysisConversation(ctx, tx, slot, now); err != nil {
		return err
	}
	if err := hook.appendWorkspaceAnalysisCancellationEvents(ctx, tx, run.ID, publication.RunStatus, answer, now, false); err != nil {
		return err
	}
	if err := appendWorkspaceAnalysisTerminalAudit(
		ctx, tx, hook.audit, hook.workerActorRef, run.ID,
		publication.RunStatus, publication.Reason, answer, 0, now,
	); err != nil {
		return err
	}
	return forceWorkspaceAnalysisPublicationConstraints(ctx, tx)
}

func (hook *WorkspaceAnalysisCancellationTerminalHook) onWorkspaceAnalysisRuntimeFailure(
	ctx context.Context,
	tx pgx.Tx,
	event workflowapplication.WorkflowNodeTerminalEvent,
	nodeKey string,
) error {
	if err := validateWorkspaceAnalysisRuntimeFailureEvent(event); err != nil {
		return err
	}
	event.TerminalAt = event.TerminalAt.UTC().Truncate(time.Microsecond)
	if err := lockWorkspaceAnalysisRuntimeFailure(ctx, tx, event, nodeKey); err != nil {
		return err
	}
	run, err := lockWorkspaceAnalysisCancellationRun(ctx, tx, event)
	if err != nil {
		return err
	}
	if err := lockWorkspaceAnalysisCancellationOperations(ctx, tx, run.ID, !run.Status.Terminal()); err != nil {
		return err
	}
	slot, err := lockWorkspaceAnalysisCancellationSlot(ctx, tx, run)
	if err != nil {
		return err
	}
	if run.Status.Terminal() || slot.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending {
		if !run.Status.Terminal() || slot.Answer.PublicationStatus == conversationdomain.AnswerPublicationPending {
			return consistency(
				ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
				errors.New("workspace analysis runtime failure replay is split"),
			)
		}
		return nil
	}
	if run.Status != agentdomain.WorkspaceAnalysisRunQueued && run.Status != agentdomain.WorkspaceAnalysisRunRunning {
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis runtime failure run is not active"),
		)
	}
	reason := agentdomain.WorkspaceAnalysisRunRuntimeFailed
	publication, err := canonicalWorkspaceAnalysisTerminationPublication(reason, nil)
	if err != nil {
		return err
	}
	now, err := workspaceAnalysisCancellationPublicationTime(ctx, tx, event.TerminalAt, run, slot)
	if err != nil {
		return err
	}
	proofID, err := hook.ids.New()
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if err := insertWorkspaceAnalysisRuntimeFailureProof(
		ctx, tx, proofID, event, run, publication, reason, now,
	); err != nil {
		return err
	}
	if err := abortWorkspaceAnalysisDraft(ctx, tx, slot.Draft, now); err != nil {
		return err
	}
	fence := workspaceAnalysisFinalizationFence{RunID: run.ID, RunStatus: run.Status, RunVersion: run.Version}
	if err := updateWorkspaceAnalysisRunTermination(ctx, tx, fence, publication, now); err != nil {
		return err
	}
	answer, err := updateWorkspaceAnalysisAnswer(ctx, tx, slot.Answer, publication, now)
	if err != nil {
		return err
	}
	if err := updateWorkspaceAnalysisConversation(ctx, tx, slot, now); err != nil {
		return err
	}
	if err := hook.appendWorkspaceAnalysisCancellationEvents(
		ctx, tx, run.ID, publication.RunStatus, answer, now, false,
	); err != nil {
		return err
	}
	if err := appendWorkspaceAnalysisTerminalAudit(
		ctx, tx, hook.audit, hook.workerActorRef, run.ID,
		publication.RunStatus, publication.Reason, answer, 0, now,
	); err != nil {
		return err
	}
	return forceWorkspaceAnalysisPublicationConstraints(ctx, tx)
}

type workspaceAnalysisCancellationRuntime struct {
	WorkflowStatus            workflowdomain.RunStatus
	WorkflowCancelRequestedAt *time.Time
	WorkflowCompletedAt       *time.Time
	NodeStatus                workflowdomain.NodeStatus
	NodeAttemptNo             int
	NodeLeaseOwner            *string
	NodeLeaseUntil            *time.Time
	NodeCompletedAt           *time.Time
}

type workspaceAnalysisCancellationRun struct {
	ID             foundation.ID
	WorkspaceID    foundation.ID
	ConversationID foundation.ID
	QuestionID     foundation.ID
	AnswerID       foundation.ID
	WorkflowRunID  foundation.ID
	Status         agentdomain.WorkspaceAnalysisRunStatus
	Reason         *agentdomain.WorkspaceAnalysisRunTerminationReason
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func workspaceAnalysisCancellationNodeKey(kind string) (string, bool) {
	for _, node := range conversationworkflow.RegisteredWorkspaceAnalysisDefinition().Graph.Nodes {
		if node.Kind == kind {
			return node.Key, true
		}
	}
	return "", false
}

func validateWorkspaceAnalysisCancellationEvent(event workflowapplication.WorkflowNodeTerminalEvent) error {
	values := []string{string(event.WorkspaceID), string(event.WorkflowRunID), string(event.NodeRunID)}
	if event.NodeAttemptID != "" {
		values = append(values, string(event.NodeAttemptID))
	}
	if _, err := parseWorkspaceAnalysisIDs(values...); err != nil || event.NodeKind == "" ||
		event.Outcome != workflowapplication.WorkflowTerminalOutcomeCancelled ||
		event.FailureClass != workflowdomain.FailureClassCancelled ||
		event.FailureCode != "WORKFLOW_CANCELLED" || event.FailureSummary != "WORKFLOW_CANCELLED" ||
		event.TerminalOutput != "" || event.TerminalAt.IsZero() ||
		!event.TerminalAt.Equal(event.TerminalAt.UTC().Truncate(time.Microsecond)) {
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis cancellation terminal event is invalid"),
		)
	}
	return nil
}

func validateWorkspaceAnalysisRuntimeFailureEvent(event workflowapplication.WorkflowNodeTerminalEvent) error {
	values := []string{
		string(event.WorkspaceID), string(event.WorkflowRunID),
		string(event.NodeRunID), string(event.NodeAttemptID),
	}
	if _, err := parseWorkspaceAnalysisIDs(values...); err != nil || event.NodeKind == "" ||
		event.Outcome != workflowapplication.WorkflowTerminalOutcomeFailed ||
		event.FailureClass == "" || event.FailureClass == workflowdomain.FailureClassCancelled ||
		strings.TrimSpace(event.FailureCode) == "" || event.FailureCode != strings.TrimSpace(event.FailureCode) ||
		strings.TrimSpace(event.FailureSummary) == "" || event.FailureSummary != strings.TrimSpace(event.FailureSummary) ||
		event.TerminalOutput != "" || event.TerminalAt.IsZero() ||
		!event.TerminalAt.Equal(event.TerminalAt.UTC().Truncate(time.Microsecond)) {
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis runtime failure terminal event is invalid"),
		)
	}
	return nil
}

func lockWorkspaceAnalysisRuntimeFailure(
	ctx context.Context,
	tx pgx.Tx,
	event workflowapplication.WorkflowNodeTerminalEvent,
	nodeKey string,
) error {
	var workflowStatus workflowdomain.RunStatus
	var workflowCancelRequestedAt, workflowCompletedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT status,cancel_requested_at,completed_at
		FROM workflow.run WHERE id=$1 AND workspace_id=$2 FOR UPDATE`,
		string(event.WorkflowRunID), string(event.WorkspaceID),
	).Scan(&workflowStatus, &workflowCancelRequestedAt, &workflowCompletedAt); err != nil {
		return workspaceAnalysisFinalizerQueryError(err, "workspace analysis runtime failure workflow")
	}
	var (
		storedNodeKey, storedNodeKind string
		nodeStatus                    workflowdomain.NodeStatus
		nodeAttemptNo                 int
		nodeLeaseOwner                *string
		nodeLeaseUntil, nodeEndedAt   *time.Time
		failureClass                  workflowdomain.FailureClass
		failureCode, failureSummary   string
	)
	if err := tx.QueryRow(ctx, `SELECT node_key,node_type,status,attempt,lease_owner,lease_until,completed_at,
		failure_class,error_code,error_summary
		FROM workflow.node_run WHERE id=$1 AND run_id=$2 FOR UPDATE`,
		string(event.NodeRunID), string(event.WorkflowRunID),
	).Scan(
		&storedNodeKey, &storedNodeKind, &nodeStatus, &nodeAttemptNo, &nodeLeaseOwner, &nodeLeaseUntil,
		&nodeEndedAt, &failureClass, &failureCode, &failureSummary,
	); err != nil {
		return workspaceAnalysisFinalizerQueryError(err, "workspace analysis runtime failure node")
	}
	if workflowStatus != workflowdomain.RunStatusFailed || workflowCancelRequestedAt != nil || workflowCompletedAt == nil ||
		!workflowCompletedAt.Equal(event.TerminalAt) || storedNodeKey != nodeKey || storedNodeKind != event.NodeKind ||
		nodeStatus != workflowdomain.NodeStatusFailed || nodeLeaseOwner != nil || nodeLeaseUntil != nil ||
		nodeEndedAt == nil || !nodeEndedAt.Equal(event.TerminalAt) || failureClass != event.FailureClass ||
		failureCode != event.FailureCode || failureSummary != event.FailureSummary {
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis runtime failure fence is invalid"),
		)
	}
	var (
		attemptStatus                 workflowdomain.AttemptStatus
		attemptNo                     int
		attemptLeaseOwner             *string
		attemptLeaseUntil, attemptEnd *time.Time
	)
	if err := tx.QueryRow(ctx, `SELECT status,attempt_no,lease_owner,lease_until,ended_at
		FROM workflow.node_attempt WHERE id=$1 AND node_run_id=$2 FOR UPDATE`,
		string(event.NodeAttemptID), string(event.NodeRunID),
	).Scan(&attemptStatus, &attemptNo, &attemptLeaseOwner, &attemptLeaseUntil, &attemptEnd); err != nil {
		return workspaceAnalysisFinalizerQueryError(err, "workspace analysis runtime failure attempt")
	}
	if !workflowdomain.IsTerminalAttemptStatus(attemptStatus) || attemptStatus == workflowdomain.AttemptStatusCancelled ||
		attemptNo != nodeAttemptNo || attemptLeaseOwner != nil || attemptLeaseUntil != nil ||
		attemptEnd == nil || !attemptEnd.Equal(event.TerminalAt) {
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis runtime failure attempt binding is invalid"),
		)
	}
	return nil
}

func lockWorkspaceAnalysisCancellationRuntime(
	ctx context.Context,
	tx pgx.Tx,
	event workflowapplication.WorkflowNodeTerminalEvent,
	nodeKey string,
) (workspaceAnalysisCancellationRuntime, error) {
	var runtime workspaceAnalysisCancellationRuntime
	if err := tx.QueryRow(ctx, `SELECT status,cancel_requested_at,completed_at
		FROM workflow.run WHERE id=$1 AND workspace_id=$2 FOR UPDATE`,
		string(event.WorkflowRunID), string(event.WorkspaceID),
	).Scan(
		&runtime.WorkflowStatus,
		&runtime.WorkflowCancelRequestedAt,
		&runtime.WorkflowCompletedAt,
	); err != nil {
		return workspaceAnalysisCancellationRuntime{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis cancellation workflow")
	}
	var storedNodeKey, storedNodeKind string
	if err := tx.QueryRow(ctx, `SELECT node_key,node_type,status,attempt,lease_owner,lease_until,completed_at
		FROM workflow.node_run WHERE id=$1 AND run_id=$2 FOR UPDATE`,
		string(event.NodeRunID), string(event.WorkflowRunID),
	).Scan(
		&storedNodeKey, &storedNodeKind, &runtime.NodeStatus, &runtime.NodeAttemptNo,
		&runtime.NodeLeaseOwner, &runtime.NodeLeaseUntil, &runtime.NodeCompletedAt,
	); err != nil {
		return workspaceAnalysisCancellationRuntime{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis cancellation node")
	}
	if storedNodeKey != nodeKey || storedNodeKind != event.NodeKind ||
		runtime.NodeStatus != workflowdomain.NodeStatusCancelled ||
		runtime.NodeLeaseOwner != nil || runtime.NodeLeaseUntil != nil ||
		runtime.NodeCompletedAt == nil || !runtime.NodeCompletedAt.Equal(event.TerminalAt) {
		return workspaceAnalysisCancellationRuntime{}, consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis cancellation node binding is invalid"),
		)
	}
	if runtime.WorkflowStatus == workflowdomain.RunStatusCancelled {
		if runtime.WorkflowCancelRequestedAt == nil || runtime.WorkflowCompletedAt == nil ||
			runtime.WorkflowCancelRequestedAt.After(event.TerminalAt) ||
			!runtime.WorkflowCompletedAt.Equal(event.TerminalAt) {
			return workspaceAnalysisCancellationRuntime{}, consistency(
				ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
				errors.New("workspace analysis cancellation workflow fence is invalid"),
			)
		}
	}
	if err := validateWorkspaceAnalysisCancellationAttempt(ctx, tx, event, runtime.NodeAttemptNo); err != nil {
		return workspaceAnalysisCancellationRuntime{}, err
	}
	return runtime, nil
}

func validateWorkspaceAnalysisCancellationAttempt(
	ctx context.Context,
	tx pgx.Tx,
	event workflowapplication.WorkflowNodeTerminalEvent,
	nodeAttemptNo int,
) error {
	if event.NodeAttemptID == "" {
		var count int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM workflow.node_attempt WHERE node_run_id=$1`,
			string(event.NodeRunID),
		).Scan(&count); err != nil {
			return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
		}
		if nodeAttemptNo != 0 || count != 0 {
			return consistency(
				ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
				errors.New("workspace analysis direct cancellation unexpectedly owns an attempt"),
			)
		}
		return nil
	}
	var (
		status                  workflowdomain.AttemptStatus
		attemptNo               int
		leaseOwner              *string
		leaseUntil, completedAt *time.Time
	)
	if err := tx.QueryRow(ctx, `SELECT status,attempt_no,lease_owner,lease_until,ended_at
		FROM workflow.node_attempt WHERE id=$1 AND node_run_id=$2 FOR UPDATE`,
		string(event.NodeAttemptID), string(event.NodeRunID),
	).Scan(&status, &attemptNo, &leaseOwner, &leaseUntil, &completedAt); err != nil {
		return workspaceAnalysisFinalizerQueryError(err, "workspace analysis cancellation attempt")
	}
	if !workflowdomain.IsTerminalAttemptStatus(status) || attemptNo != nodeAttemptNo ||
		leaseOwner != nil || leaseUntil != nil || completedAt == nil || completedAt.After(event.TerminalAt) {
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis cancellation attempt binding is invalid"),
		)
	}
	return nil
}

func lockWorkspaceAnalysisCancellationRun(
	ctx context.Context,
	tx pgx.Tx,
	event workflowapplication.WorkflowNodeTerminalEvent,
) (workspaceAnalysisCancellationRun, error) {
	var (
		run                                         workspaceAnalysisCancellationRun
		id, workspaceID, conversationID, questionID string
		answerID, workflowRunID                     string
		reasonText                                  *string
	)
	if err := tx.QueryRow(ctx, `SELECT
		id::text,workspace_id::text,conversation_id::text,question_id::text,answer_id::text,workflow_run_id::text,
		status,termination_reason,version,created_at,updated_at
		FROM agent.workspace_analysis_run
		WHERE workspace_id=$1 AND workflow_run_id=$2 FOR UPDATE`,
		string(event.WorkspaceID), string(event.WorkflowRunID),
	).Scan(
		&id, &workspaceID, &conversationID, &questionID, &answerID, &workflowRunID,
		&run.Status, &reasonText, &run.Version, &run.CreatedAt, &run.UpdatedAt,
	); err != nil {
		return workspaceAnalysisCancellationRun{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis cancellation run")
	}
	ids, err := parseWorkspaceAnalysisIDs(id, workspaceID, conversationID, questionID, answerID, workflowRunID)
	if err != nil {
		return workspaceAnalysisCancellationRun{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	run.ID, run.WorkspaceID, run.ConversationID = ids[0], ids[1], ids[2]
	run.QuestionID, run.AnswerID, run.WorkflowRunID = ids[3], ids[4], ids[5]
	if reasonText != nil {
		reason := agentdomain.WorkspaceAnalysisRunTerminationReason(*reasonText)
		run.Reason = &reason
	}
	if run.WorkspaceID != event.WorkspaceID || run.WorkflowRunID != event.WorkflowRunID {
		return workspaceAnalysisCancellationRun{}, consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis cancellation run scope drifted"),
		)
	}
	return run, nil
}

func lockWorkspaceAnalysisCancellationOperations(
	ctx context.Context,
	tx pgx.Tx,
	analysisRunID foundation.ID,
	requireSucceeded bool,
) error {
	rows, err := tx.Query(ctx, `SELECT status FROM agent.workspace_analysis_operation
		WHERE analysis_run_id=$1 ORDER BY id FOR UPDATE`, string(analysisRunID))
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	defer rows.Close()
	for rows.Next() {
		var status agentdomain.WorkspaceAnalysisOperationStatus
		if err := rows.Scan(&status); err != nil {
			return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
		}
		if requireSucceeded && status != agentdomain.WorkspaceAnalysisOperationSucceeded {
			return conflict(
				ErrorCodeWorkspaceAnalysisFinalizeConflict,
				errors.New("workspace analysis cancellation cannot mask unfinished or failed work"),
			)
		}
		terminal := status == agentdomain.WorkspaceAnalysisOperationSucceeded ||
			status == agentdomain.WorkspaceAnalysisOperationFailed ||
			status == agentdomain.WorkspaceAnalysisOperationUnknown
		if !requireSucceeded && !terminal {
			return consistency(
				ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
				errors.New("workspace analysis cancellation replay retains active work"),
			)
		}
	}
	if err := rows.Err(); err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	rows.Close()
	reservations, err := tx.Query(ctx, `SELECT status FROM agent.workspace_analysis_budget_reservation
		WHERE analysis_run_id=$1 ORDER BY id FOR UPDATE`, string(analysisRunID))
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	defer reservations.Close()
	for reservations.Next() {
		var status agentdomain.WorkspaceAnalysisBudgetReservationStatus
		if err := reservations.Scan(&status); err != nil {
			return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
		}
		if status == agentdomain.WorkspaceAnalysisBudgetReserved {
			return conflict(
				ErrorCodeWorkspaceAnalysisFinalizeConflict,
				errors.New("workspace analysis cancellation has an active budget reservation"),
			)
		}
	}
	if err := reservations.Err(); err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

func lockWorkspaceAnalysisCancellationSlot(
	ctx context.Context,
	tx pgx.Tx,
	run workspaceAnalysisCancellationRun,
) (workspaceAnalysisPublicationSlot, error) {
	var slot workspaceAnalysisPublicationSlot
	if err := tx.QueryRow(ctx, `SELECT version,created_at,updated_at,last_activity_at
		FROM agent.conversation WHERE workspace_id=$1 AND id=$2 FOR UPDATE`,
		string(run.WorkspaceID), string(run.ConversationID),
	).Scan(
		&slot.ConversationVersion, &slot.ConversationCreatedAt,
		&slot.ConversationUpdatedAt, &slot.LastActivityAt,
	); err != nil {
		return workspaceAnalysisPublicationSlot{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis cancellation conversation")
	}
	view, err := scanAnswerView(tx.QueryRow(ctx, `SELECT `+answerViewColumns+` FROM agent.answer a
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		WHERE a.workspace_id=$1 AND a.id=$2 AND a.conversation_id=$3 AND a.question_id=$4 AND a.workflow_run_id=$5
		FOR UPDATE OF a`,
		string(run.WorkspaceID), string(run.AnswerID), string(run.ConversationID),
		string(run.QuestionID), string(run.WorkflowRunID),
	))
	if err != nil {
		return workspaceAnalysisPublicationSlot{}, workspaceAnalysisFinalizerQueryError(err, "workspace analysis cancellation answer")
	}
	slot.Answer = view.Answer
	var draftID, nodeAttemptID, status string
	err = tx.QueryRow(ctx, `SELECT id::text,node_attempt_id::text,status
		FROM agent.answer_draft_session
		WHERE workspace_id=$1 AND answer_id=$2 AND status IN ('ACTIVE','COMPLETED','DEGRADED')
		FOR UPDATE`, string(run.WorkspaceID), string(run.AnswerID)).Scan(&draftID, &nodeAttemptID, &status)
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

func workspaceAnalysisCancellationPublicationTime(
	ctx context.Context,
	tx pgx.Tx,
	terminalAt time.Time,
	run workspaceAnalysisCancellationRun,
	slot workspaceAnalysisPublicationSlot,
) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	now = now.UTC().Truncate(time.Microsecond)
	floors := []time.Time{
		terminalAt, run.CreatedAt, run.UpdatedAt, slot.ConversationCreatedAt,
		slot.ConversationUpdatedAt, slot.LastActivityAt, slot.Answer.CreatedAt, slot.Answer.UpdatedAt,
	}
	for _, floor := range floors {
		if now.Before(floor) {
			return time.Time{}, consistency(
				ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
				errors.New("workspace analysis cancellation chronology is in the future"),
			)
		}
	}
	return now, nil
}

func insertWorkspaceAnalysisRuntimeCancellationProof(
	ctx context.Context,
	tx pgx.Tx,
	proofID foundation.ID,
	event workflowapplication.WorkflowNodeTerminalEvent,
	run workspaceAnalysisCancellationRun,
	publication workspaceAnalysisPublication,
	now time.Time,
) error {
	var attemptID any
	if event.NodeAttemptID != "" {
		attemptID = string(event.NodeAttemptID)
	}
	_, err := tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_termination_proof(
		id,workspace_id,analysis_run_id,answer_id,workflow_run_id,
		terminal_node_run_id,terminal_node_attempt_id,reason,
		published_document,published_result_hash,published_bytes,
		checked_at,created_at,runtime_terminal_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,'WORKSPACE_ANALYSIS_CANCELLED',$8,$9,$10,$11,$12,$11)`,
		string(proofID), string(run.WorkspaceID), string(run.ID), string(run.AnswerID), string(run.WorkflowRunID),
		string(event.NodeRunID), attemptID, []byte(publication.Document), publication.ResultHash,
		int64(len(publication.Document)), event.TerminalAt, now,
	)
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

func insertWorkspaceAnalysisRuntimeFailureProof(
	ctx context.Context,
	tx pgx.Tx,
	proofID foundation.ID,
	event workflowapplication.WorkflowNodeTerminalEvent,
	run workspaceAnalysisCancellationRun,
	publication workspaceAnalysisPublication,
	reason agentdomain.WorkspaceAnalysisRunTerminationReason,
	now time.Time,
) error {
	_, err := tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_termination_proof(
		id,workspace_id,analysis_run_id,answer_id,workflow_run_id,
		terminal_node_run_id,terminal_node_attempt_id,reason,
		published_document,published_result_hash,published_bytes,
		checked_at,created_at,runtime_terminal_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$12)`,
		string(proofID), string(run.WorkspaceID), string(run.ID), string(run.AnswerID), string(run.WorkflowRunID),
		string(event.NodeRunID), string(event.NodeAttemptID), string(reason), []byte(publication.Document),
		publication.ResultHash, int64(len(publication.Document)), event.TerminalAt, now,
	)
	if err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

func (hook *WorkspaceAnalysisCancellationTerminalHook) validateWorkspaceAnalysisCancellationReplay(
	ctx context.Context,
	tx pgx.Tx,
	event workflowapplication.WorkflowNodeTerminalEvent,
	run workspaceAnalysisCancellationRun,
	slot workspaceAnalysisPublicationSlot,
) error {
	if run.Status != agentdomain.WorkspaceAnalysisRunCancelled || run.Reason == nil ||
		*run.Reason != agentdomain.WorkspaceAnalysisRunCancellation ||
		slot.Answer.PublicationStatus != conversationdomain.WorkspaceAnalysisPublicationCancelled ||
		slot.Answer.ResultType != conversationdomain.AnswerResultWorkspaceAnalysisTermination ||
		slot.Answer.ModelRunID != nil || slot.Answer.PublishedAt == nil {
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis cancellation replay is split"),
		)
	}
	lookup := conversationapplication.WorkspaceAnalysisPublicationLookup{
		AnswerPublicationLookup: conversationapplication.AnswerPublicationLookup{
			WorkspaceID: run.WorkspaceID, WorkflowRunID: run.WorkflowRunID,
			ConversationID: run.ConversationID, QuestionID: run.QuestionID, AnswerID: run.AnswerID,
		},
		AnalysisRunID: run.ID,
	}
	proof, found, err := loadWorkspaceAnalysisTerminationProof(ctx, tx, lookup)
	if err != nil {
		return err
	}
	if !found || proof.Reason != agentdomain.WorkspaceAnalysisRunCancellation || proof.OperationID != nil ||
		proof.ArtifactKind != nil || proof.ArtifactID != nil || proof.ArtifactHash != nil ||
		proof.PublishedModelRunID != nil || proof.PublishedResultHash != slot.Answer.ResultHash ||
		proof.NodeRunID != event.NodeRunID || proof.NodeAttemptID != event.NodeAttemptID ||
		!bytes.Equal(proof.PublishedDocument, slot.Answer.Result) {
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis cancellation proof is split"),
		)
	}
	var runtimeTerminalAt *time.Time
	var checkedAt time.Time
	if err := tx.QueryRow(ctx, `SELECT runtime_terminal_at,checked_at
		FROM agent.workspace_analysis_termination_proof
		WHERE id=$1 AND analysis_run_id=$2 FOR SHARE`,
		string(proof.ID), string(run.ID),
	).Scan(&runtimeTerminalAt, &checkedAt); err != nil {
		return workspaceAnalysisFinalizerQueryError(err, "workspace analysis runtime cancellation proof")
	}
	if runtimeTerminalAt != nil && (!runtimeTerminalAt.Equal(event.TerminalAt) || !checkedAt.Equal(*runtimeTerminalAt)) {
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis Runtime cancellation proof checkpoint drifted"),
		)
	}
	if runtimeTerminalAt == nil && (event.NodeAttemptID == "" || checkedAt.After(event.TerminalAt)) {
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis active cancellation proof is not bound to the Runtime checkpoint"),
		)
	}
	if err := validateRetainedWorkspaceAnalysisDraft(ctx, tx, run.WorkspaceID, run.AnswerID, "ABORTED", nil); err != nil {
		return err
	}
	return hook.appendWorkspaceAnalysisCancellationEvents(
		ctx, tx, run.ID, run.Status, slot.Answer, *slot.Answer.PublishedAt, true,
	)
}

func (hook *WorkspaceAnalysisCancellationTerminalHook) appendWorkspaceAnalysisCancellationEvents(
	ctx context.Context,
	tx pgx.Tx,
	analysisRunID foundation.ID,
	runStatus agentdomain.WorkspaceAnalysisRunStatus,
	answer conversationdomain.Answer,
	now time.Time,
	wantReplay bool,
) error {
	if err := appendWorkspaceAnalysisTerminalEvents(
		ctx, tx, hook.events, analysisRunID, runStatus, answer, 0, now, wantReplay,
	); err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

var _ workflowapplication.WorkflowTerminalHook = (*WorkspaceAnalysisCancellationTerminalHook)(nil)
