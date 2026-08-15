package workflowcancel

import (
	"context"
	"errors"
	"reflect"
	"strings"

	changecontrol "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflow "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const cancellationRaceAttempts = 3

// Owner is the Workflow-owned query/control surface needed to cancel an old
// Proposal Revision run without exposing Workflow types to Change Control.
type Owner interface {
	Get(context.Context, foundation.ID) (workflowdomain.Run, error)
	Cancel(context.Context, workflow.RunControlCommand) (workflow.RunControlResult, error)
}

// Adapter translates the Change Control cancellation port to Workflow
// optimistic control commands.
type Adapter struct {
	owner Owner
}

func New(owner Owner) (*Adapter, error) {
	if isNilOwner(owner) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_REVISION_WORKFLOW_CANCEL_UNAVAILABLE", true, errors.New("workflow owner is missing"))
	}
	return &Adapter{owner: owner}, nil
}

func (adapter *Adapter) RequestCancellation(ctx context.Context, command changecontrol.RevisionWorkflowCancellation) (changecontrol.RevisionWorkflowCancellationResult, error) {
	if adapter == nil || isNilOwner(adapter.owner) {
		return changecontrol.RevisionWorkflowCancellationResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_REVISION_WORKFLOW_CANCEL_UNAVAILABLE", true, errors.New("workflow owner is missing"))
	}
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	if command.WorkspaceID == "" || command.ProposalID == "" || command.RevisionID == "" || command.WorkflowRunID == "" ||
		command.IdempotencyKey == "" || len(command.IdempotencyKey) > workflow.MaxIdempotencyKeyLength {
		return changecontrol.RevisionWorkflowCancellationResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_WORKFLOW_CANCEL_INVALID", false, errors.New("revision workflow cancellation binding is invalid"))
	}

	var lastConflict error
	for attempt := 0; attempt < cancellationRaceAttempts; attempt++ {
		run, err := adapter.owner.Get(ctx, command.WorkflowRunID)
		if err != nil {
			return changecontrol.RevisionWorkflowCancellationResult{}, err
		}
		if run.ID != command.WorkflowRunID || run.WorkspaceID != command.WorkspaceID || run.Version < 1 || !knownRunStatus(run.Status) {
			return changecontrol.RevisionWorkflowCancellationResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_WORKFLOW_BINDING_INVALID", false, errors.New("workflow run does not match the revision cancellation binding"))
		}
		if workflowdomain.IsTerminalRunStatus(run.Status) {
			return changecontrol.RevisionWorkflowCancellationResult{Terminal: true}, nil
		}
		if run.CancelRequestedAt != nil {
			return changecontrol.RevisionWorkflowCancellationResult{}, nil
		}

		result, err := adapter.owner.Cancel(ctx, workflow.RunControlCommand{
			WorkflowRunID: run.ID, ExpectedVersion: run.Version, IdempotencyKey: command.IdempotencyKey,
		})
		if err != nil {
			if cancellationRace(err) {
				lastConflict = err
				continue
			}
			return changecontrol.RevisionWorkflowCancellationResult{}, err
		}
		if result.WorkflowRunID != run.ID || result.Version <= run.Version || !knownRunStatus(result.Status) {
			return changecontrol.RevisionWorkflowCancellationResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_WORKFLOW_CANCEL_RESULT_INVALID", false, errors.New("workflow cancellation result is invalid"))
		}
		if workflowdomain.IsTerminalRunStatus(result.Status) {
			return changecontrol.RevisionWorkflowCancellationResult{Terminal: true}, nil
		}
		if !result.CancelRequested {
			return changecontrol.RevisionWorkflowCancellationResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_WORKFLOW_CANCEL_RESULT_INVALID", false, errors.New("workflow cancellation request was not persisted"))
		}
		return changecontrol.RevisionWorkflowCancellationResult{}, nil
	}
	return changecontrol.RevisionWorkflowCancellationResult{}, lastConflict
}

func isNilOwner(owner Owner) bool {
	if owner == nil {
		return true
	}
	value := reflect.ValueOf(owner)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func cancellationRace(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && (classified.Code == "WORKFLOW_VERSION_CONFLICT" || classified.Code == "WORKFLOW_CONTROL_CONFLICT")
}

func knownRunStatus(status workflowdomain.RunStatus) bool {
	switch status {
	case workflowdomain.RunStatusPending, workflowdomain.RunStatusRunning, workflowdomain.RunStatusWaitingForHuman,
		workflowdomain.RunStatusRetryWait, workflowdomain.RunStatusPaused, workflowdomain.RunStatusSucceeded,
		workflowdomain.RunStatusFailed, workflowdomain.RunStatusCancelled:
		return true
	default:
		return false
	}
}

var _ changecontrol.RevisionWorkflowCanceller = (*Adapter)(nil)
