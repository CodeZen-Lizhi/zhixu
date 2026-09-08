package postgres

import (
	"context"
	"testing"
	"time"

	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestWorkspaceAnalysisCancellationTerminalHookIgnoresUnownedAndNonCancellationEvents(t *testing.T) {
	var hook *GORMWorkspaceAnalysisCancellationTerminalHook
	if err := hook.OnWorkflowNodeTerminalScoped(context.Background(), nil, workflowapplication.WorkflowNodeTerminalEvent{
		NodeKind: "other.workflow.node", Outcome: workflowapplication.WorkflowTerminalOutcomeCancelled,
	}); err != nil {
		t.Fatalf("unowned event: %v", err)
	}
	if err := hook.OnWorkflowNodeTerminalScoped(context.Background(), nil, workflowapplication.WorkflowNodeTerminalEvent{
		NodeKind: conversationworkflow.RegisteredWorkspaceAnalysisDefinition().Graph.Nodes[0].Kind,
		Outcome:  workflowapplication.WorkflowTerminalOutcomeSucceeded,
	}); err != nil {
		t.Fatalf("non-cancellation event: %v", err)
	}
}

func TestNewWorkspaceAnalysisCancellationTerminalHookRejectsMissingDependencies(t *testing.T) {
	var events *workspaceAnalysisEventCapture
	if _, err := NewGORMWorkspaceAnalysisCancellationTerminalHook(events, foundation.NewUUIDGenerator(nil)); err == nil {
		t.Fatal("typed-nil event appender was accepted")
	}
	if _, err := NewGORMWorkspaceAnalysisCancellationTerminalHook(&workspaceAnalysisEventCapture{}, nil); err == nil {
		t.Fatal("nil ID generator was accepted")
	}
}

func TestValidateWorkspaceAnalysisCancellationEventRejectsPrecisionAndBindingDrift(t *testing.T) {
	event := workspaceAnalysisCancellationEventFixture()
	if err := validateWorkspaceAnalysisCancellationEvent(event); err != nil {
		t.Fatalf("valid event: %v", err)
	}

	precisionDrift := event
	precisionDrift.TerminalAt = precisionDrift.TerminalAt.Add(time.Nanosecond)
	if err := validateWorkspaceAnalysisCancellationEvent(precisionDrift); err == nil {
		t.Fatal("sub-microsecond terminal timestamp was accepted")
	}

	identityDrift := event
	identityDrift.NodeRunID = identityDrift.WorkflowRunID
	if err := validateWorkspaceAnalysisCancellationEvent(identityDrift); err == nil {
		t.Fatal("reused workflow/node identity was accepted")
	}
}

func TestValidateWorkspaceAnalysisRuntimeFailureEventRejectsMissingAttemptAndUnsafeShape(t *testing.T) {
	event := workspaceAnalysisCancellationEventFixture()
	event.Outcome = workflowapplication.WorkflowTerminalOutcomeFailed
	event.FailureClass = workflowdomain.FailureClassNonRetryable
	event.FailureCode = "WORKSPACE_ANALYSIS_INPUT_INVALID"
	event.FailureSummary = "workspace analysis input is invalid"
	event.NodeAttemptID = foundation.ID("87000000-0000-4000-8000-000000000004")
	if err := validateWorkspaceAnalysisRuntimeFailureEvent(event); err != nil {
		t.Fatalf("valid runtime failure event: %v", err)
	}

	missingAttempt := event
	missingAttempt.NodeAttemptID = ""
	if err := validateWorkspaceAnalysisRuntimeFailureEvent(missingAttempt); err == nil {
		t.Fatal("runtime failure event without attempt was accepted")
	}
	leakingSummary := event
	leakingSummary.FailureSummary = " private failure "
	if err := validateWorkspaceAnalysisRuntimeFailureEvent(leakingSummary); err == nil {
		t.Fatal("runtime failure event with non-canonical summary was accepted")
	}
}

func workspaceAnalysisCancellationEventFixture() workflowapplication.WorkflowNodeTerminalEvent {
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	return workflowapplication.WorkflowNodeTerminalEvent{
		WorkspaceID:    foundation.ID("87000000-0000-4000-8000-000000000001"),
		WorkflowRunID:  foundation.ID("87000000-0000-4000-8000-000000000002"),
		NodeRunID:      foundation.ID("87000000-0000-4000-8000-000000000003"),
		NodeKind:       definition.Graph.Nodes[0].Kind,
		Outcome:        workflowapplication.WorkflowTerminalOutcomeCancelled,
		FailureClass:   workflowdomain.FailureClassCancelled,
		FailureCode:    "WORKFLOW_CANCELLED",
		FailureSummary: "WORKFLOW_CANCELLED",
		TerminalAt:     time.Date(2026, 8, 16, 12, 0, 0, 123456000, time.UTC),
	}
}

var _ eventsapplication.ScopedAppender = (*workspaceAnalysisEventCapture)(nil)
