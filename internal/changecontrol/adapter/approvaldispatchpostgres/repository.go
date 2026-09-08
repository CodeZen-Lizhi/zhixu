package approvaldispatchpostgres

import (
	"encoding/json"
	"errors"
	changedispatch "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/dispatch"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"reflect"
	"strings"
)

const approvalDispatchNo = 1

func validateApprovalDispatchCommand(command changedispatch.Command) error {
	approval := command.Approval
	if command.WorkspaceID == "" || approval.ID == "" || approval.ProposalID == "" || approval.RevisionID == "" || !domain.ValidHash(approval.ChangeHash) || approval.DecidedAt.IsZero() || approval.Decision != domain.DecisionApproved && approval.Decision != domain.DecisionRejected {
		return foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_DISPATCH_INVALID", false, errors.New("approval dispatch command is invalid"))
	}
	return nil
}

func validateApprovedDispatchSafety(command changedispatch.Command, targetPath string, targetMode domain.TargetMode, persistedBaseHash string) error {
	mode, modeErr := domain.ValidateTargetMode(targetMode)
	if command.Approval.ApprovedGitHead == nil || modeErr != nil || domain.ValidateTargetBaseVersion(command.WorkspaceID, targetPath, mode, command.ObservedBaseHash) != nil || !domain.ValidGitHead(*command.Approval.ApprovedGitHead) || !domain.ValidGitHead(command.ObservedGitHead) || !strings.EqualFold(command.ObservedBaseHash, persistedBaseHash) || !strings.EqualFold(command.ObservedGitHead, *command.Approval.ApprovedGitHead) {
		return foundation.NewError(foundation.ErrorVersionConflict, "APPROVAL_DISPATCH_SAFETY_CONFLICT", false, errors.New("approval safety observations do not match persisted facts"))
	}
	return nil
}

func sameApprovalDecision(existing, requested domain.Approval) bool {
	return existing.ProposalID == requested.ProposalID && existing.RevisionID == requested.RevisionID && strings.EqualFold(existing.ChangeHash, requested.ChangeHash) && existing.Decision == requested.Decision && sameDispatchOptionalString(existing.ApprovedGitHead, requested.ApprovedGitHead)
}

func sameDispatchOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.EqualFold(strings.TrimSpace(*left), strings.TrimSpace(*right))
}

func dispatchResult(approval domain.Approval, runtime workflowapplication.RuntimeStartResult, status changedispatch.Status, replayed bool) changedispatch.Result {
	return changedispatch.Result{Approval: approval, WorkflowRunID: runtime.Run.ID, NodeRunID: runtime.FirstNode.ID, JobID: runtime.Job.JobID, DispatchStatus: status, Replayed: replayed}
}

func jsonEqualDispatch(left, right []byte) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}

func isNilDispatchDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func classifyDispatch(err error, code string) error {
	if err == nil {
		return nil
	}
	if sqlState := platformpostgres.SQLState(err); sqlState != "" {
		switch sqlState {
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		case "23503", "23505", "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}
