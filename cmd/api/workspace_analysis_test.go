package main

import (
	"context"
	"testing"

	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestAPIWorkspaceAnalysisRuntimeHooksComposeWithArtifactRuntime(t *testing.T) {
	pool := apiConstructorPool(t)
	events, err := eventspostgres.NewGORMStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	workspaceAnalysis, err := newAPIWorkspaceAnalysisRuntimeHooks(pool, events)
	if err != nil {
		t.Fatal(err)
	}
	if workspaceAnalysis == nil || workspaceAnalysis.terminal == nil || workspaceAnalysis.control == nil {
		t.Fatalf("workspace analysis runtime hooks=%#v", workspaceAnalysis)
	}

	var calls []string
	artifact := apiTerminalHookFunc(func(context.Context, foundation.TransactionScope, workflowapplication.WorkflowNodeTerminalEvent) error {
		calls = append(calls, "artifact")
		return nil
	})
	analysis := apiTerminalHookFunc(func(context.Context, foundation.TransactionScope, workflowapplication.WorkflowNodeTerminalEvent) error {
		calls = append(calls, "workspace-analysis")
		return nil
	})
	controlCalled := false
	control := apiControlHookFunc(func(context.Context, foundation.TransactionScope, workflowapplication.WorkflowControlEvent) error {
		controlCalled = true
		return nil
	})

	terminal, composedControl, err := composeAPIWorkflowRuntimeHooks(artifact, &apiWorkspaceAnalysisRuntimeHooks{
		terminal: analysis,
		control:  control,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := terminal.OnWorkflowNodeTerminalScoped(context.Background(), nil, workflowapplication.WorkflowNodeTerminalEvent{}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "artifact" || calls[1] != "workspace-analysis" {
		t.Fatalf("terminal hook calls=%v", calls)
	}
	if err := composedControl.OnWorkflowControlScoped(context.Background(), nil, workflowapplication.WorkflowControlEvent{}); err != nil {
		t.Fatal(err)
	}
	if !controlCalled {
		t.Fatal("workspace analysis control hook was not called")
	}
}

func TestAPIWorkflowRuntimeHooksKeepArtifactOnlyWhenWorkspaceAnalysisIsDisabled(t *testing.T) {
	var calls int
	artifact := apiTerminalHookFunc(func(context.Context, foundation.TransactionScope, workflowapplication.WorkflowNodeTerminalEvent) error {
		calls++
		return nil
	})
	terminal, control, err := composeAPIWorkflowRuntimeHooks(artifact, nil)
	if err != nil {
		t.Fatal(err)
	}
	if control != nil {
		t.Fatalf("control hook=%#v", control)
	}
	if err := terminal.OnWorkflowNodeTerminalScoped(context.Background(), nil, workflowapplication.WorkflowNodeTerminalEvent{}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("artifact terminal hook calls=%d", calls)
	}
}

type apiTerminalHookFunc func(context.Context, foundation.TransactionScope, workflowapplication.WorkflowNodeTerminalEvent) error

func (hook apiTerminalHookFunc) OnWorkflowNodeTerminalScoped(
	ctx context.Context,
	transaction foundation.TransactionScope,
	event workflowapplication.WorkflowNodeTerminalEvent,
) error {
	return hook(ctx, transaction, event)
}

type apiControlHookFunc func(context.Context, foundation.TransactionScope, workflowapplication.WorkflowControlEvent) error

func (hook apiControlHookFunc) OnWorkflowControlScoped(
	ctx context.Context,
	transaction foundation.TransactionScope,
	event workflowapplication.WorkflowControlEvent,
) error {
	return hook(ctx, transaction, event)
}
