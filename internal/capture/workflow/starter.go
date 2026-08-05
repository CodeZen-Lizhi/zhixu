package workflow

import (
	"context"
	"errors"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// RuntimeStarter is the minimal registered Workflow service contract used by Capture dispatch.
type RuntimeStarter interface {
	Start(context.Context, workflowapp.StartCommand) (workflowdomain.Run, error)
}

// Starter adapts the shared Workflow runtime to the Capture-owned start receipt.
type Starter struct{ Runtime RuntimeStarter }

// StartCaptureWorkflow starts or exactly replays a registered Capture Workflow and returns its Run ID.
func (starter Starter) StartCaptureWorkflow(ctx context.Context, command workflowapp.StartCommand) (foundation.ID, error) {
	if nilPort(starter.Runtime) {
		return "", foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_WORKFLOW_START_UNAVAILABLE", true, errors.New("workflow runtime is unavailable"))
	}
	run, err := starter.Runtime.Start(ctx, command)
	if err != nil {
		return "", err
	}
	if _, err := foundation.ParseID(string(run.ID)); err != nil || run.WorkspaceID != command.WorkspaceID {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "CAPTURE_WORKFLOW_START_RESULT_INVALID", false, errors.New("workflow runtime returned an invalid capture binding"))
	}
	return run.ID, nil
}

var _ captureapp.WorkflowStartPort = Starter{}
