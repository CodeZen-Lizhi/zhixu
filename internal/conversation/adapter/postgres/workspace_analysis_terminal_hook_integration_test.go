//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkspaceAnalysisCancellationTerminalHookDirectRuntimeCancelClosesPublicationAndReplays(t *testing.T) {
	pool, ctx, dispatched := newWorkspaceAnalysisCancellationHookFixture(t, "direct")
	auditRecorder, auditStore := newWorkspaceAnalysisAuditIntegration(t, pool)
	hook, coordinator := newWorkspaceAnalysisCancellationHookRuntimeWithAudit(
		t, pool, auditRecorder, "workspace-analysis-runtime-integration",
	)

	cancelled, err := coordinator.Cancel(ctx, workflowapplication.RunControlCommand{
		WorkflowRunID: dispatched.WorkflowRunID, ExpectedVersion: dispatched.WorkflowVersion,
		IdempotencyKey: "workspace-analysis-direct-cancel",
	})
	if err != nil {
		t.Fatal(conversationErrorChain(err))
	}
	if cancelled.Status != workflowdomain.RunStatusCancelled || !cancelled.CancelRequested {
		t.Fatalf("cancelled workflow=%+v", cancelled)
	}
	assertWorkspaceAnalysisRuntimeCancellationBundle(t, ctx, pool, dispatched.AnswerID, false, true)

	event := loadWorkspaceAnalysisCancellationTerminalEvent(t, ctx, pool, dispatched.WorkflowRunID, dispatched.NodeRunID, "")
	hook.ids = workspaceAnalysisReplayFailingIDs{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := hook.OnWorkflowNodeTerminal(ctx, tx, event); err != nil {
		t.Fatalf("replay terminal hook: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceAnalysisRuntimeCancellationBundle(t, ctx, pool, dispatched.AnswerID, false, true)
	var analysisRunID foundation.ID
	if err := pool.QueryRow(ctx, `SELECT id::text FROM agent.workspace_analysis_run
		WHERE workspace_id=$1 AND workflow_run_id=$2`, string(dispatched.WorkspaceID), string(dispatched.WorkflowRunID)).Scan(
		&analysisRunID,
	); err != nil {
		t.Fatal(err)
	}
	auditEvent := requireWorkspaceAnalysisAuditEventIntegration(
		t, ctx, auditStore, dispatched.WorkspaceID, "workspace-analysis:terminated:"+string(analysisRunID)+":v1",
	)
	if auditEvent.WorkspaceID == nil || *auditEvent.WorkspaceID != dispatched.WorkspaceID ||
		string(auditEvent.ActorType) != "WORKER" || auditEvent.ActorRef != "workspace-analysis-runtime-integration" ||
		auditEvent.Action != workspaceAnalysisTerminatedAuditAction || string(auditEvent.Outcome) != "REJECTED" ||
		auditEvent.ErrorCode != string(agentdomain.WorkspaceAnalysisRunCancellation) ||
		auditEvent.ResourceType != workspaceAnalysisAuditResourceType ||
		auditEvent.ResourceRef != "workspace_analysis:"+string(analysisRunID) {
		t.Fatalf("workspace analysis runtime cancellation audit=%#v", auditEvent)
	}
	requireWorkspaceAnalysisAuditJSONObjectIntegration(t, auditEvent.Correlation, map[string]any{
		"analysis_run_id": string(analysisRunID), "workflow_run_id": string(dispatched.WorkflowRunID),
		"conversation_id": string(dispatched.ConversationID), "question_id": string(dispatched.QuestionID),
		"answer_id": string(dispatched.AnswerID),
	})
	requireWorkspaceAnalysisAuditJSONObjectIntegration(t, auditEvent.Metadata, map[string]any{
		"status": "cancelled", "termination_reason": string(agentdomain.WorkspaceAnalysisRunCancellation),
		"publication_status": "cancelled", "result_type": "workspace_analysis_termination",
		"citation_count": float64(0), "answer_version": float64(2),
	})
	requireWorkspaceAnalysisAuditSafeIntegration(t, auditEvent, "Inspect the current workspace.")
}

func TestWorkspaceAnalysisRuntimeFailureHookClosesPendingAnswerWithoutFabricatingCall(t *testing.T) {
	pool, ctx, dispatched := newWorkspaceAnalysisCancellationHookFixture(t, "runtime-failure")
	hook, coordinator := newWorkspaceAnalysisCancellationHookRuntime(t, pool)
	const deliveryID = "workspace-analysis-runtime-failure"
	claimed, err := coordinator.Claim(ctx, workflowapplication.ClaimCommand{
		NodeRunID: dispatched.NodeRunID, DispatchNo: 1, DeliveryID: deliveryID,
		RiverJobID: dispatched.JobID, LeaseOwner: "workspace-analysis-runtime-failure-worker",
		LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := coordinator.Fail(ctx, workflowapplication.FailDeliveryCommand{
		Binding: workflowapplication.DeliveryBinding{
			NodeRunID: dispatched.NodeRunID, DispatchNo: 1, DeliveryID: deliveryID,
			Fence: workflowdomain.LeaseFence{
				Owner: "workspace-analysis-runtime-failure-worker", AttemptNo: claimed.Attempt.AttemptNo,
				NodeVersion: claimed.Node.Version,
			},
		},
		Failure: workflowdomain.FailureInput{Explicit: &workflowdomain.FailureEnvelope{
			Class: workflowdomain.FailureClassNonRetryable, ErrorKind: foundation.ErrorNonRetryableFailure,
			Code: "WORKSPACE_ANALYSIS_INPUT_INVALID", Summary: "workspace analysis input is invalid",
		}},
	})
	if err != nil {
		t.Fatal(conversationErrorChain(err))
	}
	if failed.Run.Status != workflowdomain.RunStatusFailed || failed.Node.Status != workflowdomain.NodeStatusFailed ||
		failed.Attempt.Status != workflowdomain.AttemptStatusFailed {
		t.Fatalf("runtime failure=%+v", failed)
	}

	var workflowStatus, nodeStatus, attemptStatus, answerStatus, analysisStatus, reason, resultType string
	var runtimeTerminalAt *time.Time
	var proofCount, operationCount, reservationCount, answerEvents, analysisEvents int64
	if err := pool.QueryRow(ctx, `SELECT w.status,n.status,a2.status,a.publication_status,r.status,r.termination_reason,
		a.result_type,p.runtime_terminal_at,
		(SELECT count(*) FROM agent.workspace_analysis_termination_proof p2 WHERE p2.analysis_run_id=r.id),
		(SELECT count(*) FROM agent.workspace_analysis_operation o WHERE o.analysis_run_id=r.id),
		(SELECT count(*) FROM agent.workspace_analysis_budget_reservation b WHERE b.analysis_run_id=r.id),
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id
		 AND e.resource_ref='answer:'||a.id::text AND e.event_type='answer.failed'),
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id
		 AND e.resource_ref='workspace_analysis:'||r.id::text AND e.event_type='workspace_analysis.terminated')
		FROM workflow.run w
		JOIN workflow.node_run n ON n.run_id=w.id
		JOIN workflow.node_attempt a2 ON a2.node_run_id=n.id
		JOIN agent.answer a ON a.workflow_run_id=w.id AND a.workspace_id=w.workspace_id
		JOIN agent.workspace_analysis_run r ON r.workflow_run_id=w.id AND r.workspace_id=w.workspace_id
		JOIN agent.workspace_analysis_termination_proof p ON p.analysis_run_id=r.id
		WHERE w.id=$1`, string(dispatched.WorkflowRunID)).Scan(
		&workflowStatus, &nodeStatus, &attemptStatus, &answerStatus, &analysisStatus, &reason,
		&resultType, &runtimeTerminalAt, &proofCount, &operationCount, &reservationCount, &answerEvents, &analysisEvents,
	); err != nil {
		t.Fatal(err)
	}
	if workflowStatus != "failed" || nodeStatus != "failed" || attemptStatus != "failed" ||
		answerStatus != "failed" || analysisStatus != "failed" ||
		reason != string(agentdomain.WorkspaceAnalysisRunRuntimeFailed) || resultType != "workspace_analysis_termination" ||
		runtimeTerminalAt == nil || proofCount != 1 || operationCount != 0 || reservationCount != 0 ||
		answerEvents != 1 || analysisEvents != 1 {
		t.Fatalf("runtime failure closure workflow=%s node=%s attempt=%s answer=%s analysis=%s reason=%s result=%s runtime_at=%v proofs=%d operations=%d reservations=%d answer_events=%d analysis_events=%d",
			workflowStatus, nodeStatus, attemptStatus, answerStatus, analysisStatus, reason, resultType,
			runtimeTerminalAt, proofCount, operationCount, reservationCount, answerEvents, analysisEvents)
	}

	replayEvent := workflowapplication.WorkflowNodeTerminalEvent{
		WorkspaceID: dispatched.WorkspaceID, WorkflowRunID: dispatched.WorkflowRunID,
		NodeRunID: dispatched.NodeRunID, NodeKind: failed.Node.NodeType,
		NodeAttemptID: failed.Attempt.ID, Outcome: workflowapplication.WorkflowTerminalOutcomeFailed,
		FailureClass: failed.Node.FailureClass, FailureCode: failed.Node.ErrorCode,
		FailureSummary: failed.Node.ErrorSummary, TerminalAt: *failed.Node.CompletedAt,
	}
	hook.ids = workspaceAnalysisReplayFailingIDs{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := hook.OnWorkflowNodeTerminal(ctx, tx, replayEvent); err != nil {
		t.Fatalf("replay runtime failure hook: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.workspace_analysis_termination_proof p WHERE p.answer_id=$1),
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=$2
		 AND e.resource_ref='answer:'||$1::text AND e.event_type='answer.failed'),
		(SELECT count(*) FROM ops.server_event e JOIN agent.workspace_analysis_run r
		   ON e.workspace_id=r.workspace_id AND e.resource_ref='workspace_analysis:'||r.id::text
		  WHERE r.answer_id=$1 AND e.event_type='workspace_analysis.terminated')`,
		string(dispatched.AnswerID), string(dispatched.WorkspaceID),
	).Scan(&proofCount, &answerEvents, &analysisEvents); err != nil {
		t.Fatal(err)
	}
	if proofCount != 1 || answerEvents != 1 || analysisEvents != 1 {
		t.Fatalf("runtime failure replay proofs=%d answer_events=%d analysis_events=%d", proofCount, answerEvents, analysisEvents)
	}

}

func TestWorkspaceAnalysisCancellationTerminalHookAuditFailureRollsBackRuntimeAndPublication(t *testing.T) {
	pool, ctx, dispatched := newWorkspaceAnalysisCancellationHookFixture(t, "audit-rollback")
	cause := errors.New("injected workspace analysis cancellation audit failure")
	_, coordinator := newWorkspaceAnalysisCancellationHookRuntimeWithAudit(
		t, pool, &workspaceAnalysisAuditCapture{err: cause}, "workspace-analysis-runtime-integration",
	)
	if _, err := coordinator.Cancel(ctx, workflowapplication.RunControlCommand{
		WorkflowRunID: dispatched.WorkflowRunID, ExpectedVersion: dispatched.WorkflowVersion,
		IdempotencyKey: "workspace-analysis-audit-rollback-cancel",
	}); !errors.Is(err, cause) {
		t.Fatalf("Cancel(audit failure) err=%s", conversationErrorChain(err))
	}

	var workflowStatus, nodeStatus, answerStatus, analysisStatus string
	var cancelRequestedAt *time.Time
	var proofs, answerEvents, analysisEvents, audits int64
	if err := pool.QueryRow(ctx, `SELECT w.status,w.cancel_requested_at,n.status,a.publication_status,r.status,
		(SELECT count(*) FROM agent.workspace_analysis_termination_proof p WHERE p.analysis_run_id=r.id),
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id
		 AND e.resource_ref='answer:'||a.id::text AND e.event_type='answer.cancelled'),
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id
		 AND e.resource_ref='workspace_analysis:'||r.id::text AND e.event_type='workspace_analysis.terminated'),
		(SELECT count(*) FROM ops.audit_event e WHERE e.workspace_id=a.workspace_id)
		FROM workflow.run w
		JOIN workflow.node_run n ON n.run_id=w.id
		JOIN agent.answer a ON a.workflow_run_id=w.id AND a.workspace_id=w.workspace_id
		JOIN agent.workspace_analysis_run r ON r.workflow_run_id=w.id AND r.workspace_id=w.workspace_id
		WHERE w.id=$1`, string(dispatched.WorkflowRunID)).Scan(
		&workflowStatus, &cancelRequestedAt, &nodeStatus, &answerStatus, &analysisStatus,
		&proofs, &answerEvents, &analysisEvents, &audits,
	); err != nil {
		t.Fatal(err)
	}
	if workflowStatus != "pending" || cancelRequestedAt != nil || nodeStatus != "pending" || answerStatus != "pending" ||
		analysisStatus != "queued" || proofs != 0 || answerEvents != 0 || analysisEvents != 0 || audits != 0 {
		t.Fatalf(
			"audit rollback workflow=%s cancel_at=%v node=%s answer=%s analysis=%s proofs=%d answer_events=%d analysis_events=%d audits=%d",
			workflowStatus, cancelRequestedAt, nodeStatus, answerStatus, analysisStatus,
			proofs, answerEvents, analysisEvents, audits,
		)
	}
}

func TestWorkspaceAnalysisCancellationControlAuditIsSingleSafeAndRollbackAtomic(t *testing.T) {
	pool, ctx, dispatched := newWorkspaceAnalysisCancellationHookFixture(t, "control-audit")
	auditRecorder, auditStore := newWorkspaceAnalysisAuditIntegration(t, pool)
	_, coordinator := newWorkspaceAnalysisCancellationHookRuntimeWithAudit(
		t, pool, auditRecorder, "workspace-analysis-runtime-integration",
	)
	command := workflowapplication.RunControlCommand{
		WorkflowRunID: dispatched.WorkflowRunID, ExpectedVersion: dispatched.WorkflowVersion,
		IdempotencyKey: "workspace-analysis-control-audit-cancel",
	}
	first, err := coordinator.Cancel(ctx, command)
	if err != nil || !first.CancelRequested {
		t.Fatalf("Cancel() result=%+v err=%s", first, conversationErrorChain(err))
	}
	if replay, err := coordinator.Cancel(ctx, command); err != nil || replay.Version != first.Version {
		t.Fatalf("Cancel() replay=%+v err=%s", replay, conversationErrorChain(err))
	}
	var analysisRunID foundation.ID
	if err := pool.QueryRow(ctx, `SELECT id::text FROM agent.workspace_analysis_run
		WHERE workspace_id=$1 AND workflow_run_id=$2`, string(dispatched.WorkspaceID), string(dispatched.WorkflowRunID),
	).Scan(&analysisRunID); err != nil {
		t.Fatal(err)
	}
	event := requireWorkspaceAnalysisAuditEventIntegration(
		t, ctx, auditStore, dispatched.WorkspaceID, "workspace-analysis:cancel-requested:"+string(analysisRunID)+":v1",
	)
	if event.ActorType != "AGENT" || event.ActorRef != workspaceAnalysisAuditAgentRef ||
		event.Action != workspaceAnalysisCancelRequestedAuditAction || event.Outcome != "SUCCEEDED" ||
		event.ErrorCode != "" || event.ResourceType != workspaceAnalysisAuditResourceType ||
		event.ResourceRef != "workspace_analysis:"+string(analysisRunID) {
		t.Fatalf("workspace analysis cancel audit=%#v", event)
	}
	requireWorkspaceAnalysisAuditJSONObjectIntegration(t, event.Correlation, map[string]any{
		"analysis_run_id": string(analysisRunID), "workflow_run_id": string(dispatched.WorkflowRunID),
		"conversation_id": string(dispatched.ConversationID), "question_id": string(dispatched.QuestionID),
		"answer_id": string(dispatched.AnswerID),
	})
	requireWorkspaceAnalysisAuditJSONObjectIntegration(t, event.Metadata, map[string]any{
		"definition_key": "workspace-analysis", "definition_version": float64(1),
		"workflow_status": "cancelled", "cancel_requested": true,
	})
	requireWorkspaceAnalysisAuditSafeIntegration(t, event, command.IdempotencyKey, "Inspect the current workspace.")
	var cancels int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.audit_event
		WHERE workspace_id=$1 AND idempotency_key=$2`, string(dispatched.WorkspaceID),
		"workspace-analysis:cancel-requested:"+string(analysisRunID)+":v1",
	).Scan(&cancels); err != nil || cancels != 1 {
		t.Fatalf("cancel audit rows=%d err=%v", cancels, err)
	}

	rollbackPool, rollbackCtx, rollbackDispatched := newWorkspaceAnalysisCancellationHookFixture(t, "control-audit-rollback")
	events, err := eventspostgres.NewStore(rollbackPool)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := NewWorkspaceAnalysisCancellationTerminalHook(events, foundation.NewUUIDGenerator(nil))
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("injected workspace analysis control audit failure")
	controlAudit, err := NewWorkspaceAnalysisCancellationAuditHook(&workspaceAnalysisAuditCapture{err: cause})
	if err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(rollbackPool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewRuntimeRepositoryWithHooks(rollbackPool, inserter, workflowpostgres.RuntimeRepositoryHooks{
		Terminal: terminal, Control: controlAudit,
	})
	if err != nil {
		t.Fatal(err)
	}
	rollbackCoordinator, err := workflowapplication.NewRuntimeCoordinator(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rollbackCoordinator.Cancel(rollbackCtx, workflowapplication.RunControlCommand{
		WorkflowRunID: rollbackDispatched.WorkflowRunID, ExpectedVersion: rollbackDispatched.WorkflowVersion,
		IdempotencyKey: "workspace-analysis-control-audit-rollback",
	}); !errors.Is(err, cause) {
		t.Fatalf("Cancel(control audit failure) err=%s", conversationErrorChain(err))
	}
	var workflowStatus, nodeStatus, answerStatus, analysisStatus string
	var controlCommands, audits int64
	if err := rollbackPool.QueryRow(rollbackCtx, `SELECT w.status,n.status,a.publication_status,r.status,
		(SELECT count(*) FROM workflow.control_command c WHERE c.run_id=w.id),
		(SELECT count(*) FROM ops.audit_event e WHERE e.workspace_id=w.workspace_id)
		FROM workflow.run w
		JOIN workflow.node_run n ON n.run_id=w.id
		JOIN agent.answer a ON a.workflow_run_id=w.id AND a.workspace_id=w.workspace_id
		JOIN agent.workspace_analysis_run r ON r.workflow_run_id=w.id AND r.workspace_id=w.workspace_id
		WHERE w.id=$1`, string(rollbackDispatched.WorkflowRunID),
	).Scan(&workflowStatus, &nodeStatus, &answerStatus, &analysisStatus, &controlCommands, &audits); err != nil {
		t.Fatal(err)
	}
	if workflowStatus != "pending" || nodeStatus != "pending" || answerStatus != "pending" || analysisStatus != "queued" ||
		controlCommands != 0 || audits != 0 {
		t.Fatalf("control audit rollback workflow=%s node=%s answer=%s analysis=%s controls=%d audits=%d",
			workflowStatus, nodeStatus, answerStatus, analysisStatus, controlCommands, audits)
	}
}

func TestWorkspaceAnalysisCancellationTerminalHookCheckpointCancelClosesAttemptBoundPublication(t *testing.T) {
	pool, ctx, dispatched := newWorkspaceAnalysisCancellationHookFixture(t, "checkpoint")
	_, coordinator := newWorkspaceAnalysisCancellationHookRuntime(t, pool)
	const deliveryID = "workspace-analysis-cancellation-checkpoint"
	claimed, err := coordinator.Claim(ctx, workflowapplication.ClaimCommand{
		NodeRunID: dispatched.NodeRunID, DispatchNo: 1, DeliveryID: deliveryID,
		RiverJobID: dispatched.JobID, LeaseOwner: "workspace-analysis-cancellation-worker",
		LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := coordinator.Cancel(ctx, workflowapplication.RunControlCommand{
		WorkflowRunID: dispatched.WorkflowRunID, ExpectedVersion: claimed.Run.Version,
		IdempotencyKey: "workspace-analysis-checkpoint-cancel",
	})
	if err != nil || requested.Status != workflowdomain.RunStatusRunning || !requested.CancelRequested {
		t.Fatalf("request cancellation=%+v err=%v", requested, err)
	}
	completed, err := coordinator.Complete(ctx, workflowapplication.CompleteDeliveryCommand{
		Binding: workflowapplication.DeliveryBinding{
			NodeRunID: dispatched.NodeRunID, DispatchNo: 1, DeliveryID: deliveryID,
			Fence: workflowdomain.LeaseFence{
				Owner: "workspace-analysis-cancellation-worker", AttemptNo: claimed.Attempt.AttemptNo,
				NodeVersion: claimed.Node.Version,
			},
		},
		Output: json.RawMessage(`{"ignored":true}`), OutputSchemaVersion: 1,
	})
	if err != nil {
		t.Fatal(conversationErrorChain(err))
	}
	if completed.Run.Status != workflowdomain.RunStatusCancelled ||
		completed.Node.Status != workflowdomain.NodeStatusCancelled ||
		completed.Attempt.Status != workflowdomain.AttemptStatusCancelled {
		t.Fatalf("checkpoint cancellation=%+v", completed)
	}
	assertWorkspaceAnalysisRuntimeCancellationBundle(t, ctx, pool, dispatched.AnswerID, true, true)
}

func TestWorkspaceAnalysisCancellationTerminalHookAcceptsExactActiveLeasePublication(t *testing.T) {
	pool, ctx, dispatched := newWorkspaceAnalysisCancellationHookFixture(t, "active-publication")
	_, coordinator := newWorkspaceAnalysisCancellationHookRuntime(t, pool)
	const deliveryID = "workspace-analysis-active-cancellation-publication"
	claimed, err := coordinator.Claim(ctx, workflowapplication.ClaimCommand{
		NodeRunID: dispatched.NodeRunID, DispatchNo: 1, DeliveryID: deliveryID,
		RiverJobID: dispatched.JobID, LeaseOwner: "workspace-analysis-active-cancellation-worker",
		LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.workspace_analysis_run
		SET status='running',version=version+1,updated_at=clock_timestamp()
		WHERE workspace_id=$1 AND workflow_run_id=$2 AND status='queued'`,
		string(dispatched.WorkspaceID), string(dispatched.WorkflowRunID),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Cancel(ctx, workflowapplication.RunControlCommand{
		WorkflowRunID: dispatched.WorkflowRunID, ExpectedVersion: claimed.Run.Version,
		IdempotencyKey: "workspace-analysis-active-publication-cancel",
	}); err != nil {
		t.Fatal(err)
	}
	var analysisRunID foundation.ID
	if err := pool.QueryRow(ctx, `SELECT id::text FROM agent.workspace_analysis_run
		WHERE workspace_id=$1 AND workflow_run_id=$2`, string(dispatched.WorkspaceID), string(dispatched.WorkflowRunID)).Scan(
		&analysisRunID,
	); err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := NewWorkspaceAnalysisFinalizer(pool, events, foundation.NewUUIDGenerator(nil))
	if err != nil {
		t.Fatal(err)
	}
	_, replayed, err := finalizer.FinalizeTermination(ctx, conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: conversationapplication.WorkspaceAnalysisPublicationLookup{
			AnswerPublicationLookup: conversationapplication.AnswerPublicationLookup{
				WorkspaceID: dispatched.WorkspaceID, WorkflowRunID: dispatched.WorkflowRunID,
				NodeRunID: dispatched.NodeRunID, NodeAttemptID: claimed.Attempt.ID,
				ConversationID: dispatched.ConversationID, QuestionID: dispatched.QuestionID, AnswerID: dispatched.AnswerID,
			},
			AnalysisRunID: analysisRunID, ExpectedLeaseOwner: claimed.Attempt.LeaseOwner,
			ExpectedLeaseFence: int64(claimed.Attempt.AttemptNo),
		},
		ExpectedAnswerVersion: 1,
		Reason:                agentdomain.WorkspaceAnalysisRunCancellation,
	})
	if err != nil || replayed {
		t.Fatalf("active cancellation publication replayed=%t err=%s", replayed, conversationErrorChain(err))
	}
	completed, err := coordinator.Complete(ctx, workflowapplication.CompleteDeliveryCommand{
		Binding: workflowapplication.DeliveryBinding{
			NodeRunID: dispatched.NodeRunID, DispatchNo: 1, DeliveryID: deliveryID,
			Fence: workflowdomain.LeaseFence{
				Owner: claimed.Attempt.LeaseOwner, AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version,
			},
		},
		Output: json.RawMessage(`{"ignored":true}`), OutputSchemaVersion: 1,
	})
	if err != nil {
		t.Fatal(conversationErrorChain(err))
	}
	if completed.Run.Status != workflowdomain.RunStatusCancelled || completed.Node.Status != workflowdomain.NodeStatusCancelled {
		t.Fatalf("checkpoint after active publication=%+v", completed)
	}
	assertWorkspaceAnalysisRuntimeCancellationBundle(t, ctx, pool, dispatched.AnswerID, true, false)
}

func newWorkspaceAnalysisCancellationHookFixture(
	t *testing.T,
	suffix string,
) (*pgxpool.Pool, context.Context, cancellationHookDispatchResult) {
	t.Helper()
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceID := foundation.NewUUIDGenerator(nil)
	workspace, err := workspaceID.New()
	if err != nil {
		t.Fatal(err)
	}
	conversationID, err := workspaceID.New()
	if err != nil {
		t.Fatal(err)
	}
	seedConversationWorkspaces(t, ctx, pool, workspace)
	createdAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if _, err := repository.CreateConversation(ctx, conversationCreateRecord(
		t, workspace, conversationID, "Workspace analysis cancellation", "workspace-analysis-cancellation-"+suffix, createdAt,
	)); err != nil {
		t.Fatal(err)
	}
	dispatched, err := newWorkspaceAnalysisQuestionDispatcherIntegration(t, pool).SubmitQuestion(
		ctx,
		workspaceAnalysisQuestionDispatchRecord(
			t, workspace, conversationID, "Inspect the current workspace.", "workspace-analysis-cancellation-question-"+suffix,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	return pool, ctx, cancellationHookDispatchResult{
		WorkspaceID: workspace, ConversationID: conversationID, QuestionID: dispatched.Question.ID,
		AnswerID: dispatched.Answer.ID, WorkflowRunID: dispatched.Workflow.RunID,
		WorkflowVersion: dispatched.Workflow.Version, NodeRunID: dispatched.NodeRunID, JobID: dispatched.JobID,
	}
}

type cancellationHookDispatchResult struct {
	WorkspaceID     foundation.ID
	ConversationID  foundation.ID
	QuestionID      foundation.ID
	AnswerID        foundation.ID
	WorkflowRunID   foundation.ID
	WorkflowVersion int64
	NodeRunID       foundation.ID
	JobID           int64
}

func newWorkspaceAnalysisCancellationHookRuntime(
	t *testing.T,
	pool *pgxpool.Pool,
) (*WorkspaceAnalysisCancellationTerminalHook, *workflowapplication.RuntimeCoordinator) {
	return newWorkspaceAnalysisCancellationHookRuntimeConfigured(t, pool, nil, "")
}

func newWorkspaceAnalysisCancellationHookRuntimeWithAudit(
	t *testing.T,
	pool *pgxpool.Pool,
	audit WorkspaceAnalysisAuditRecorder,
	workerActorRef string,
) (*WorkspaceAnalysisCancellationTerminalHook, *workflowapplication.RuntimeCoordinator) {
	return newWorkspaceAnalysisCancellationHookRuntimeConfigured(t, pool, audit, workerActorRef)
}

func newWorkspaceAnalysisCancellationHookRuntimeConfigured(
	t *testing.T,
	pool *pgxpool.Pool,
	audit WorkspaceAnalysisAuditRecorder,
	workerActorRef string,
) (*WorkspaceAnalysisCancellationTerminalHook, *workflowapplication.RuntimeCoordinator) {
	t.Helper()
	events, err := eventspostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	var hook *WorkspaceAnalysisCancellationTerminalHook
	var control workflowapplication.WorkflowControlHook
	if isNilInterface(audit) {
		hook, err = NewWorkspaceAnalysisCancellationTerminalHook(events, foundation.NewUUIDGenerator(nil))
	} else {
		hook, err = NewWorkspaceAnalysisCancellationTerminalHookWithAudit(
			events, foundation.NewUUIDGenerator(nil), audit, workerActorRef,
		)
		control, err = NewWorkspaceAnalysisCancellationAuditHook(audit)
	}
	if err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewRuntimeRepositoryWithHooks(
		pool, inserter, workflowpostgres.RuntimeRepositoryHooks{Terminal: hook, Control: control},
	)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapplication.NewRuntimeCoordinator(runtime)
	if err != nil {
		t.Fatal(err)
	}
	return hook, coordinator
}

func assertWorkspaceAnalysisRuntimeCancellationBundle(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	answerID foundation.ID,
	wantAttempt bool,
	wantRuntime bool,
) {
	t.Helper()
	var answerStatus, answerResultType, runStatus, runReason, eventType, analysisEventType string
	var modelRunID, attemptID *string
	var runtimeTerminalAt *time.Time
	var checkedAt time.Time
	var proofCount, eventCount, analysisEventCount int64
	if err := pool.QueryRow(ctx, `SELECT
		a.publication_status,a.result_type,a.model_run_id::text,
		r.status,r.termination_reason,p.terminal_node_attempt_id::text,
		p.runtime_terminal_at,p.checked_at,
		(SELECT count(*) FROM agent.workspace_analysis_termination_proof WHERE analysis_run_id=r.id),
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id
		 AND e.resource_ref='answer:'||a.id::text AND e.event_type='answer.cancelled'),
		(SELECT event_type FROM ops.server_event e WHERE e.workspace_id=a.workspace_id
		 AND e.resource_ref='answer:'||a.id::text AND e.event_type='answer.cancelled'),
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id
		 AND e.resource_ref='workspace_analysis:'||r.id::text AND e.event_type='workspace_analysis.terminated'),
		(SELECT event_type FROM ops.server_event e WHERE e.workspace_id=a.workspace_id
		 AND e.resource_ref='workspace_analysis:'||r.id::text AND e.event_type='workspace_analysis.terminated')
		FROM agent.answer a
		JOIN agent.workspace_analysis_run r ON r.answer_id=a.id AND r.workspace_id=a.workspace_id
		JOIN agent.workspace_analysis_termination_proof p ON p.analysis_run_id=r.id
		WHERE a.id=$1`, string(answerID)).Scan(
		&answerStatus, &answerResultType, &modelRunID, &runStatus, &runReason, &attemptID,
		&runtimeTerminalAt, &checkedAt, &proofCount, &eventCount, &eventType, &analysisEventCount, &analysisEventType,
	); err != nil {
		t.Fatal(err)
	}
	if answerStatus != string(conversationdomain.WorkspaceAnalysisPublicationCancelled) ||
		answerResultType != string(conversationdomain.AnswerResultWorkspaceAnalysisTermination) ||
		modelRunID != nil || runStatus != "cancelled" || runReason != "WORKSPACE_ANALYSIS_CANCELLED" ||
		(attemptID != nil) != wantAttempt || (runtimeTerminalAt != nil) != wantRuntime ||
		(runtimeTerminalAt != nil && !runtimeTerminalAt.Equal(checkedAt)) ||
		proofCount != 1 || eventCount != 1 || eventType != "answer.cancelled" ||
		analysisEventCount != 1 || analysisEventType != "workspace_analysis.terminated" {
		t.Fatalf("answer=%s/%s model=%v run=%s/%s attempt=%v runtime=%s checked=%s proof=%d event=%d/%s analysis=%d/%s",
			answerStatus, answerResultType, modelRunID, runStatus, runReason, attemptID,
			runtimeTerminalAt, checkedAt, proofCount, eventCount, eventType, analysisEventCount, analysisEventType)
	}
}

func loadWorkspaceAnalysisCancellationTerminalEvent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workflowRunID, nodeRunID, nodeAttemptID foundation.ID,
) workflowapplication.WorkflowNodeTerminalEvent {
	t.Helper()
	var workspaceID foundation.ID
	var nodeKind string
	var terminalAt time.Time
	if err := pool.QueryRow(ctx, `SELECT r.workspace_id::text,n.node_type,n.completed_at
		FROM workflow.run r JOIN workflow.node_run n ON n.run_id=r.id
		WHERE r.id=$1 AND n.id=$2`, string(workflowRunID), string(nodeRunID)).Scan(
		&workspaceID, &nodeKind, &terminalAt,
	); err != nil {
		t.Fatal(err)
	}
	return workflowapplication.WorkflowNodeTerminalEvent{
		WorkspaceID: workspaceID, WorkflowRunID: workflowRunID, NodeRunID: nodeRunID,
		NodeKind: nodeKind, NodeAttemptID: nodeAttemptID,
		Outcome:      workflowapplication.WorkflowTerminalOutcomeCancelled,
		FailureClass: workflowdomain.FailureClassCancelled, FailureCode: "WORKFLOW_CANCELLED",
		FailureSummary: "WORKFLOW_CANCELLED", TerminalAt: terminalAt,
	}
}
