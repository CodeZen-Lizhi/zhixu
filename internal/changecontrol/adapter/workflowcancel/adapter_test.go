package workflowcancel

import (
	"context"
	"errors"
	"testing"
	"time"

	changecontrol "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflow "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

type ownerFake struct {
	runs         []workflowdomain.Run
	getCalls     int
	cancelCalls  int
	cancelInputs []workflow.RunControlCommand
	cancelResult workflow.RunControlResult
	cancelErrors []error
}

func (owner *ownerFake) Get(context.Context, foundation.ID) (workflowdomain.Run, error) {
	index := owner.getCalls
	owner.getCalls++
	if index >= len(owner.runs) {
		index = len(owner.runs) - 1
	}
	return owner.runs[index], nil
}

func (owner *ownerFake) Cancel(_ context.Context, command workflow.RunControlCommand) (workflow.RunControlResult, error) {
	index := owner.cancelCalls
	owner.cancelCalls++
	owner.cancelInputs = append(owner.cancelInputs, command)
	if index < len(owner.cancelErrors) && owner.cancelErrors[index] != nil {
		return workflow.RunControlResult{}, owner.cancelErrors[index]
	}
	return owner.cancelResult, nil
}

func cancellationCommand() changecontrol.RevisionWorkflowCancellation {
	return changecontrol.RevisionWorkflowCancellation{
		WorkspaceID: "workspace", ProposalID: "proposal", RevisionID: "revision", WorkflowRunID: "run", IdempotencyKey: "revision-cancel",
	}
}

func TestRequestCancellationPersistsActiveRunRequest(t *testing.T) {
	owner := &ownerFake{
		runs:         []workflowdomain.Run{{ID: "run", WorkspaceID: "workspace", Status: workflowdomain.RunStatusRunning, Version: 4}},
		cancelResult: workflow.RunControlResult{WorkflowRunID: "run", Status: workflowdomain.RunStatusRunning, Version: 5, CancelRequested: true},
	}
	adapter, err := New(owner)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.RequestCancellation(context.Background(), cancellationCommand())
	if err != nil || result.Terminal || owner.cancelCalls != 1 {
		t.Fatalf("result=%#v cancel calls=%d error=%v", result, owner.cancelCalls, err)
	}
	command := owner.cancelInputs[0]
	if command.WorkflowRunID != "run" || command.ExpectedVersion != 4 || command.IdempotencyKey != "revision-cancel" || command.CallerCapabilities != nil {
		t.Fatalf("cancel command = %#v", command)
	}
}

func TestRequestCancellationRecognizesTerminalAndPendingRuns(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	tests := []struct {
		name     string
		run      workflowdomain.Run
		terminal bool
	}{
		{name: "terminal", run: workflowdomain.Run{ID: "run", WorkspaceID: "workspace", Status: workflowdomain.RunStatusCancelled, Version: 5}, terminal: true},
		{name: "pending cancellation", run: workflowdomain.Run{ID: "run", WorkspaceID: "workspace", Status: workflowdomain.RunStatusRunning, Version: 5, CancelRequestedAt: &now}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner := &ownerFake{runs: []workflowdomain.Run{test.run}}
			adapter, err := New(owner)
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.RequestCancellation(context.Background(), cancellationCommand())
			if err != nil || result.Terminal != test.terminal || owner.cancelCalls != 0 {
				t.Fatalf("result=%#v cancel calls=%d error=%v", result, owner.cancelCalls, err)
			}
		})
	}
}

func TestRequestCancellationRetriesVersionRaceWithFreshRun(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	owner := &ownerFake{
		runs: []workflowdomain.Run{
			{ID: "run", WorkspaceID: "workspace", Status: workflowdomain.RunStatusRunning, Version: 4},
			{ID: "run", WorkspaceID: "workspace", Status: workflowdomain.RunStatusRunning, Version: 5, CancelRequestedAt: &now},
		},
		cancelErrors: []error{foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_VERSION_CONFLICT", false, errors.New("version changed"))},
	}
	adapter, err := New(owner)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.RequestCancellation(context.Background(), cancellationCommand())
	if err != nil || result.Terminal || owner.getCalls != 2 || owner.cancelCalls != 1 {
		t.Fatalf("result=%#v get calls=%d cancel calls=%d error=%v", result, owner.getCalls, owner.cancelCalls, err)
	}
}

func TestRequestCancellationRejectsWorkflowBindingMismatch(t *testing.T) {
	owner := &ownerFake{runs: []workflowdomain.Run{{ID: "run", WorkspaceID: "other", Status: workflowdomain.RunStatusRunning, Version: 1}}}
	adapter, err := New(owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.RequestCancellation(context.Background(), cancellationCommand())
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "PROPOSAL_REVISION_WORKFLOW_BINDING_INVALID" || owner.cancelCalls != 0 {
		t.Fatalf("cancel calls=%d error=%v", owner.cancelCalls, err)
	}
}

func TestNewRejectsTypedNilWorkflowOwner(t *testing.T) {
	var owner *ownerFake
	_, err := New(owner)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "PROPOSAL_REVISION_WORKFLOW_CANCEL_UNAVAILABLE" || !classified.Retryable {
		t.Fatalf("error = %v", err)
	}
}
