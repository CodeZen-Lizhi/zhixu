//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationpostgres "github.com/CodeZen-Lizhi/zhixu/internal/conversation/adapter/postgres"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestWorkspaceAnalysisDynamicRuntimeCancellationClosesDecisionPrefixIntegration(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "successful_prefix"
		if pending {
			name = "pending_after_prefix"
		}
		t.Run(name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			var command application.AuthorizeWorkspaceAnalysisModelCallCommand
			items, models := 2, int64(1)
			if pending {
				command, _ = testWorkspaceAnalysisDynamicDecisionBudgetDenial(t, platform, ctx)
				items, models = 25, 12
			} else {
				h := newDynamicToolsIntegration(t, ctx, platform)
				h.execute(t, ctx, 1, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionGitStatus}, "ReadGitStatus", `{}`)
				command = h.command
			}
			hook, coordinator := dynamicTerminalRuntime(t, platform)
			requested, err := coordinator.Cancel(ctx, workflowapplication.RunControlCommand{
				WorkflowRunID: command.Identity.WorkflowRunID, ExpectedVersion: 1, IdempotencyKey: "dynamic-cancel-prefix",
			})
			if err != nil || requested.Status != workflowdomain.RunStatusRunning || !requested.CancelRequested {
				t.Fatalf("dynamic cancellation request: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
			}
			completed, err := coordinator.Complete(ctx, workflowapplication.CompleteDeliveryCommand{
				Binding: dynamicTerminalDeliveryBinding(command), Output: json.RawMessage(`{"ignored":true}`), OutputSchemaVersion: 2,
			})
			if err != nil || completed.Run.Status != workflowdomain.RunStatusCancelled || completed.Node.Status != workflowdomain.NodeStatusCancelled ||
				completed.Attempt.Status != workflowdomain.AttemptStatusCancelled {
				t.Fatalf("dynamic cancellation checkpoint: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
			}
			lookup := dynamicFinalizerLookup(command)
			_, timelines := dynamicFinalizerDependencies(t, platform)
			assertDynamicFinalizationHasNoLiveModel(t, ctx, platform, lookup.AnalysisRunID)
			assertDynamicTerminalTimeline(t, ctx, timelines, lookup, conversationdomain.WorkspaceAnalysisTimelineRunCancelled,
				domain.WorkspaceAnalysisRunCancellation, items, models, models)
			dynamicReplayTerminalHook(t, ctx, platform, hook, workflowapplication.WorkflowNodeTerminalEvent{
				WorkspaceID: command.Identity.WorkspaceID, WorkflowRunID: command.Identity.WorkflowRunID,
				NodeRunID: completed.Node.ID, NodeAttemptID: completed.Attempt.ID, NodeKind: completed.Node.NodeType,
				Outcome: workflowapplication.WorkflowTerminalOutcomeCancelled, TerminalAt: *completed.Node.CompletedAt,
				FailureClass: workflowdomain.FailureClassCancelled, FailureCode: "WORKFLOW_CANCELLED", FailureSummary: "WORKFLOW_CANCELLED",
			})
			assertDynamicFinalizationHasNoLiveModel(t, ctx, platform, lookup.AnalysisRunID)
		})
	}
}

func TestWorkspaceAnalysisDynamicRuntimeFailureClosesDecisionPrefixIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	h := newDynamicToolsIntegration(t, ctx, platform)
	h.execute(t, ctx, 1, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionGitStatus}, "ReadGitStatus", `{}`)
	hook, coordinator := dynamicTerminalRuntime(t, platform)
	failed, err := coordinator.Fail(ctx, workflowapplication.FailDeliveryCommand{
		Binding: dynamicTerminalDeliveryBinding(h.command),
		Failure: workflowdomain.FailureInput{Explicit: &workflowdomain.FailureEnvelope{
			Class: workflowdomain.FailureClassNonRetryable, ErrorKind: foundation.ErrorNonRetryableFailure,
			Code: "WORKSPACE_ANALYSIS_INPUT_INVALID", Summary: "workspace analysis input is invalid",
		}},
	})
	if err != nil || failed.Run.Status != workflowdomain.RunStatusFailed || failed.Node.Status != workflowdomain.NodeStatusFailed ||
		failed.Attempt.Status != workflowdomain.AttemptStatusFailed {
		t.Fatalf("dynamic runtime failure checkpoint: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	lookup := dynamicFinalizerLookup(h.command)
	_, timelines := dynamicFinalizerDependencies(t, platform)
	assertDynamicFinalizationHasNoLiveModel(t, ctx, platform, lookup.AnalysisRunID)
	assertDynamicTerminalTimeline(t, ctx, timelines, lookup, conversationdomain.WorkspaceAnalysisTimelineRunFailed,
		domain.WorkspaceAnalysisRunRuntimeFailed, 2, 1, 1)
	dynamicReplayTerminalHook(t, ctx, platform, hook, workflowapplication.WorkflowNodeTerminalEvent{
		WorkspaceID: h.command.Identity.WorkspaceID, WorkflowRunID: h.command.Identity.WorkflowRunID,
		NodeRunID: failed.Node.ID, NodeAttemptID: failed.Attempt.ID, NodeKind: failed.Node.NodeType,
		Outcome: workflowapplication.WorkflowTerminalOutcomeFailed, TerminalAt: *failed.Node.CompletedAt,
		FailureClass: failed.Node.FailureClass, FailureCode: failed.Node.ErrorCode, FailureSummary: failed.Node.ErrorSummary,
	})
	assertDynamicFinalizationHasNoLiveModel(t, ctx, platform, lookup.AnalysisRunID)
	if h.counts["ReadGitStatus"].Load() != 1 {
		t.Fatal("runtime failure checkpoint repeated a tool")
	}
}

func dynamicTerminalRuntime(t *testing.T, platform *platformpostgres.Pool) (*conversationpostgres.GORMWorkspaceAnalysisCancellationTerminalHook, *workflowapplication.RuntimeCoordinator) {
	t.Helper()
	events, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := conversationpostgres.NewGORMWorkspaceAnalysisCancellationTerminalHook(events, foundation.NewUUIDGenerator(nil))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(platform, riveradapter.DefaultOptions(), riveradapter.NewStaticScopedEnqueueFence(),
		workflowpostgres.GORMRuntimeRepositoryHooks{Terminal: hook})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapplication.NewRuntimeCoordinator(runtime)
	if err != nil {
		t.Fatal(err)
	}
	return hook, coordinator
}

func dynamicTerminalDeliveryBinding(command application.AuthorizeWorkspaceAnalysisModelCallCommand) workflowapplication.DeliveryBinding {
	return workflowapplication.DeliveryBinding{
		NodeRunID: command.Identity.NodeRunID, DispatchNo: 1, DeliveryID: "wa-model-plan-1",
		Fence: workflowdomain.LeaseFence{Owner: command.Identity.LeaseOwner, AttemptNo: 1, NodeVersion: 1},
	}
}

func dynamicReplayTerminalHook(t *testing.T, ctx context.Context, platform *platformpostgres.Pool, hook *conversationpostgres.GORMWorkspaceAnalysisCancellationTerminalHook, event workflowapplication.WorkflowNodeTerminalEvent) {
	t.Helper()
	uow, err := platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	if err := uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		return hook.OnWorkflowNodeTerminalScoped(ctx, scope, event)
	}); err != nil {
		t.Fatalf("dynamic terminal hook replay: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
}
