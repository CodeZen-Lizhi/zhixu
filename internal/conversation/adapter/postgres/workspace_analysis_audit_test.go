package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestWorkspaceAnalysisRunStartedAuditUsesStableSafeBinding(t *testing.T) {
	capture := &workspaceAnalysisAuditCapture{}
	run := workspaceAnalysisAuditTestRun()
	if err := appendScopedWorkspaceAnalysisRunStartedAudit(context.Background(), nil, capture, run); err != nil {
		t.Fatalf("appendScopedWorkspaceAnalysisRunStartedAudit() = %v", err)
	}
	if len(capture.events) != 1 {
		t.Fatalf("audit events=%#v", capture.events)
	}
	event := capture.events[0]
	expectedID, err := workspaceAnalysisAuditEventID(workspaceAnalysisRunStartedAuditAction, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != expectedID || event.WorkspaceID == nil || *event.WorkspaceID != run.WorkspaceID ||
		event.ActorType != auditdomain.ActorAgent || event.ActorRef != workspaceAnalysisAuditAgentRef ||
		event.Action != workspaceAnalysisRunStartedAuditAction || event.ResourceType != workspaceAnalysisAuditResourceType ||
		event.ResourceRef != "workspace_analysis:"+string(run.ID) || event.Outcome != auditdomain.OutcomeSucceeded ||
		event.ErrorCode != "" || event.IdempotencyKey != "workspace-analysis:run-started:"+string(run.ID)+":v1" ||
		!event.OccurredAt.Equal(run.CreatedAt) {
		t.Fatalf("started audit=%#v", event)
	}
	document := string(event.Correlation) + string(event.Metadata)
	for _, value := range []string{
		string(run.ID), string(run.WorkflowRunID), string(run.ConversationID), string(run.QuestionID), string(run.AnswerID),
		`"definition_key":"workspace-analysis"`, `"definition_version":1`, `"policy_version":1`,
		`"config_revision":7`, `"status":"queued"`,
	} {
		if !strings.Contains(document, value) {
			t.Fatalf("started audit omitted %q: %s", value, document)
		}
	}
	for _, forbidden := range []string{"prompt", "receipt", "private_binding", "request_hash", "tool_catalog_hash", "definition_hash"} {
		if strings.Contains(document, forbidden) {
			t.Fatalf("started audit leaked %q: %s", forbidden, document)
		}
	}

	capture.replayed = true
	if err := appendScopedWorkspaceAnalysisRunStartedAudit(context.Background(), nil, capture, run); err == nil {
		t.Fatal("fresh run accepted a replayed audit")
	}
	cause := errors.New("audit unavailable")
	capture.replayed = false
	capture.err = cause
	if err := appendScopedWorkspaceAnalysisRunStartedAudit(context.Background(), nil, capture, run); !errors.Is(err, cause) {
		t.Fatalf("audit failure=%v", err)
	}
}

func TestWorkspaceAnalysisTerminalAuditMapsStableOutcomesAndCounts(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 123456000, time.UTC)
	analysisRunID := workspaceAnalysisAuditTestID(20)
	answer := conversationdomain.Answer{
		ID: workspaceAnalysisAuditTestID(21), WorkspaceID: workspaceAnalysisAuditTestID(22),
		ConversationID: workspaceAnalysisAuditTestID(23), QuestionID: workspaceAnalysisAuditTestID(24),
		WorkflowRunID: workspaceAnalysisAuditTestID(25), Version: 2,
	}
	tests := []struct {
		name        string
		status      agentdomain.WorkspaceAnalysisRunStatus
		reason      agentdomain.WorkspaceAnalysisRunTerminationReason
		publication conversationdomain.AnswerPublicationStatus
		resultType  conversationdomain.AnswerResultType
		wantOutcome auditdomain.Outcome
		wantError   string
	}{
		{name: "completed", status: agentdomain.WorkspaceAnalysisRunSucceeded, reason: agentdomain.WorkspaceAnalysisRunCompleted, publication: conversationdomain.AnswerPublicationCompleted, resultType: conversationdomain.AnswerResultWorkspaceAnalysis, wantOutcome: auditdomain.OutcomeSucceeded},
		{name: "refused", status: agentdomain.WorkspaceAnalysisRunRefused, reason: agentdomain.WorkspaceAnalysisRunEvidenceInsufficient, publication: conversationdomain.AnswerPublicationRefused, resultType: conversationdomain.AnswerResultWorkspaceAnalysisRefusal, wantOutcome: auditdomain.OutcomeRejected, wantError: string(agentdomain.WorkspaceAnalysisRunEvidenceInsufficient)},
		{name: "failed", status: agentdomain.WorkspaceAnalysisRunFailed, reason: agentdomain.WorkspaceAnalysisRunToolFailed, publication: conversationdomain.WorkspaceAnalysisPublicationFailed, resultType: conversationdomain.AnswerResultWorkspaceAnalysisTermination, wantOutcome: auditdomain.OutcomeFailed, wantError: string(agentdomain.WorkspaceAnalysisRunToolFailed)},
		{name: "runtime failed", status: agentdomain.WorkspaceAnalysisRunFailed, reason: agentdomain.WorkspaceAnalysisRunRuntimeFailed, publication: conversationdomain.WorkspaceAnalysisPublicationFailed, resultType: conversationdomain.AnswerResultWorkspaceAnalysisTermination, wantOutcome: auditdomain.OutcomeFailed, wantError: string(agentdomain.WorkspaceAnalysisRunRuntimeFailed)},
		{name: "unknown", status: agentdomain.WorkspaceAnalysisRunFailed, reason: agentdomain.WorkspaceAnalysisRunResultUnknown, publication: conversationdomain.WorkspaceAnalysisPublicationFailed, resultType: conversationdomain.AnswerResultWorkspaceAnalysisTermination, wantOutcome: auditdomain.OutcomeUnknown, wantError: string(agentdomain.WorkspaceAnalysisRunResultUnknown)},
		{name: "cancelled", status: agentdomain.WorkspaceAnalysisRunCancelled, reason: agentdomain.WorkspaceAnalysisRunCancellation, publication: conversationdomain.WorkspaceAnalysisPublicationCancelled, resultType: conversationdomain.AnswerResultWorkspaceAnalysisTermination, wantOutcome: auditdomain.OutcomeRejected, wantError: string(agentdomain.WorkspaceAnalysisRunCancellation)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capture := &workspaceAnalysisAuditCapture{}
			terminalAnswer := answer
			terminalAnswer.PublicationStatus = test.publication
			terminalAnswer.ResultType = test.resultType
			if err := appendScopedWorkspaceAnalysisTerminalAudit(
				context.Background(), nil, capture, "workspace-analysis-worker-1", analysisRunID,
				test.status, test.reason, terminalAnswer, 2, now,
			); err != nil {
				t.Fatalf("appendScopedWorkspaceAnalysisTerminalAudit() = %v", err)
			}
			if len(capture.events) != 1 {
				t.Fatalf("audit events=%#v", capture.events)
			}
			event := capture.events[0]
			if event.ActorType != auditdomain.ActorWorker || event.ActorRef != "workspace-analysis-worker-1" ||
				event.Action != workspaceAnalysisTerminatedAuditAction || event.Outcome != test.wantOutcome ||
				event.ErrorCode != test.wantError || event.IdempotencyKey != "workspace-analysis:terminated:"+string(analysisRunID)+":v1" ||
				!strings.Contains(string(event.Metadata), `"citation_count":2`) ||
				!strings.Contains(string(event.Metadata), `"answer_version":2`) || !event.OccurredAt.Equal(now) {
				t.Fatalf("terminal audit=%#v", event)
			}
			document := string(event.Correlation) + string(event.Metadata)
			for _, forbidden := range []string{"answer_markdown", "prompt", "receipt", "private_binding", "result_hash"} {
				if strings.Contains(document, forbidden) {
					t.Fatalf("terminal audit leaked %q: %s", forbidden, document)
				}
			}
		})
	}
	if _, _, err := workspaceAnalysisTerminalAuditOutcome(
		agentdomain.WorkspaceAnalysisRunRefused, agentdomain.WorkspaceAnalysisRunCancellation,
	); err == nil {
		t.Fatal("terminal audit accepted a mismatched status and reason")
	}
}

func TestWorkspaceAnalysisCancelRequestedAuditUsesStableSafeBinding(t *testing.T) {
	analysis := workspaceAnalysisAuditTestRun()
	run := workspaceAnalysisCancellationAuditRun{
		ID: analysis.ID, WorkspaceID: analysis.WorkspaceID, ConversationID: analysis.ConversationID,
		QuestionID: analysis.QuestionID, AnswerID: analysis.AnswerID, WorkflowRunID: analysis.WorkflowRunID,
	}
	control := workflowapplication.WorkflowControlEvent{
		WorkspaceID: analysis.WorkspaceID, WorkflowRunID: analysis.WorkflowRunID,
		Action: workflowapplication.ControlActionCancel, IdempotencyKey: "user-supplied-key",
		ExpectedVersion: 1,
		PersistedControl: workflowapplication.ControlPersistenceResult{
			WorkflowRunID: analysis.WorkflowRunID, Version: 3, CancelRequested: true,
		},
		OccurredAt: time.Date(2026, 8, 17, 10, 0, 0, 123456000, time.UTC),
	}
	capture := &workspaceAnalysisAuditCapture{}
	if err := appendScopedWorkspaceAnalysisCancelRequestedAudit(context.Background(), nil, capture, run, control); err != nil {
		t.Fatalf("appendScopedWorkspaceAnalysisCancelRequestedAudit() = %v", err)
	}
	if len(capture.events) != 1 {
		t.Fatalf("audit events=%#v", capture.events)
	}
	event := capture.events[0]
	expectedID, err := workspaceAnalysisAuditEventID(workspaceAnalysisCancelRequestedAuditAction, analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != expectedID || event.ActorType != auditdomain.ActorAgent || event.ActorRef != workspaceAnalysisAuditAgentRef ||
		event.Action != workspaceAnalysisCancelRequestedAuditAction || event.Outcome != auditdomain.OutcomeSucceeded ||
		event.IdempotencyKey != "workspace-analysis:cancel-requested:"+string(analysis.ID)+":v1" ||
		!event.OccurredAt.Equal(control.OccurredAt) {
		t.Fatalf("cancel audit=%#v", event)
	}
	document := string(event.Correlation) + string(event.Metadata)
	for _, value := range []string{
		string(analysis.ID), string(analysis.WorkflowRunID), string(analysis.ConversationID), string(analysis.QuestionID), string(analysis.AnswerID),
		`"definition_key":"workspace-analysis"`, `"definition_version":1`, `"cancel_requested":true`,
	} {
		if !strings.Contains(document, value) {
			t.Fatalf("cancel audit omitted %q: %s", value, document)
		}
	}
	for _, forbidden := range []string{"user-supplied-key", "prompt", "receipt", "private_binding", "request_hash", "workspace_path"} {
		if strings.Contains(document, forbidden) {
			t.Fatalf("cancel audit leaked %q: %s", forbidden, document)
		}
	}
	capture.replayed = true
	if err := appendScopedWorkspaceAnalysisCancelRequestedAudit(context.Background(), nil, capture, run, control); err == nil {
		t.Fatal("fresh cancel accepted a replayed audit")
	}
}

func TestWorkspaceAnalysisAuditConstructorsRejectUnsafeDependencies(t *testing.T) {
	var audit *workspaceAnalysisAuditCapture
	if _, err := NewGORMQuestionDispatcherWithWorkspaceAnalysisAndAudit(
		questionConstructorPool(t), &questionConstructorRuntime{}, &questionConstructorAppender{},
		foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, &questionAnalysisRunStarter{}, audit,
	); err == nil {
		t.Fatal("dispatcher accepted a typed-nil audit recorder")
	}
	if _, err := NewGORMWorkspaceAnalysisFinalizerWithAudit(
		questionConstructorPool(t), &questionConstructorAppender{}, foundation.NewUUIDGenerator(nil),
		&workspaceAnalysisAuditCapture{}, "/tmp/worker-secret",
	); err == nil {
		t.Fatal("finalizer accepted an unsafe worker actor ref")
	}
	if _, err := NewGORMWorkspaceAnalysisCancellationTerminalHookWithAudit(
		&questionConstructorAppender{}, foundation.NewUUIDGenerator(nil), &workspaceAnalysisAuditCapture{}, "token=plain-secret",
	); err == nil {
		t.Fatal("terminal hook accepted a sensitive worker actor ref")
	}
	if _, err := NewGORMWorkspaceAnalysisCancellationTerminalHookWithAudit(
		&questionConstructorAppender{}, foundation.NewUUIDGenerator(nil), &workspaceAnalysisAuditCapture{}, "worker@example.test",
	); err == nil {
		t.Fatal("terminal hook accepted a PII worker actor ref")
	}
	if _, err := NewGORMWorkspaceAnalysisCancellationAuditHook(audit); err == nil {
		t.Fatal("control audit hook accepted a typed-nil audit recorder")
	}
}

type workspaceAnalysisAuditCapture struct {
	events   []auditdomain.Event
	replayed bool
	err      error
}

func (capture *workspaceAnalysisAuditCapture) RecordScoped(
	_ context.Context,
	_ foundation.TransactionScope,
	event auditdomain.Event,
) (auditdomain.Event, bool, error) {
	if capture.err != nil {
		return auditdomain.Event{}, false, capture.err
	}
	capture.events = append(capture.events, event)
	return event, capture.replayed, nil
}

func workspaceAnalysisAuditTestRun() agentdomain.WorkspaceAnalysisRun {
	return agentdomain.WorkspaceAnalysisRun{
		ID: workspaceAnalysisAuditTestID(1), WorkspaceID: workspaceAnalysisAuditTestID(2),
		ConversationID: workspaceAnalysisAuditTestID(3), QuestionID: workspaceAnalysisAuditTestID(4),
		AnswerID: workspaceAnalysisAuditTestID(5), WorkflowRunID: workspaceAnalysisAuditTestID(6),
		DefinitionKey: "workspace-analysis", DefinitionVersion: 1, PolicyVersion: 1, ConfigRevision: 7,
		Status: agentdomain.WorkspaceAnalysisRunQueued, CreatedAt: time.Date(2026, 8, 17, 9, 0, 0, 123456000, time.UTC),
	}
}

func workspaceAnalysisAuditTestID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("89000000-0000-4000-8000-%012d", value))
}

var _ ScopedWorkspaceAnalysisAuditRecorder = (*workspaceAnalysisAuditCapture)(nil)
