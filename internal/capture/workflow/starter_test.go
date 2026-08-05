package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestStarterReturnsWorkspaceBoundRunID(t *testing.T) {
	workspaceID := workflowTestID(1)
	runtime := &starterRuntime{run: workflowdomain.Run{ID: workflowTestID(8), WorkspaceID: workspaceID}}
	id, err := (Starter{Runtime: runtime}).StartCaptureWorkflow(context.Background(), workflowapp.StartCommand{WorkspaceID: workspaceID})
	if err != nil || id != runtime.run.ID || runtime.calls != 1 {
		t.Fatalf("id=%q calls=%d err=%v", id, runtime.calls, err)
	}
}

func TestStarterRejectsMissingOrCrossWorkspaceRuntime(t *testing.T) {
	workspaceID := workflowTestID(1)
	if _, err := (Starter{}).StartCaptureWorkflow(context.Background(), workflowapp.StartCommand{WorkspaceID: workspaceID}); starterErrorCode(err) != "CAPTURE_WORKFLOW_START_UNAVAILABLE" {
		t.Fatalf("missing runtime error = %#v", err)
	}
	runtime := &starterRuntime{run: workflowdomain.Run{ID: workflowTestID(8), WorkspaceID: workflowTestID(2)}}
	if _, err := (Starter{Runtime: runtime}).StartCaptureWorkflow(context.Background(), workflowapp.StartCommand{WorkspaceID: workspaceID}); starterErrorCode(err) != "CAPTURE_WORKFLOW_START_RESULT_INVALID" {
		t.Fatalf("cross-workspace error = %#v", err)
	}
}

type starterRuntime struct {
	run   workflowdomain.Run
	err   error
	calls int
}

func (runtime *starterRuntime) Start(context.Context, workflowapp.StartCommand) (workflowdomain.Run, error) {
	runtime.calls++
	return runtime.run, runtime.err
}

func starterErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
