package application

import (
	"errors"
	"reflect"
	"strings"

	changedispatch "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/dispatch"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ApprovalDispatchStatus 是 Approval 自动投递在 API 边界可见的稳定状态。
type ApprovalDispatchStatus = changedispatch.Status

const (
	// DispatchStatusQueued 表示首次原子创建了待执行 River Job。
	DispatchStatusQueued = changedispatch.StatusQueued
	// DispatchStatusRunning 表示重放时对应 Workflow 已经开始执行。
	DispatchStatusRunning = changedispatch.StatusRunning
	// DispatchStatusReplayed 表示完整 Approval→Run→Node→Job 绑定被原样重放。
	DispatchStatusReplayed = changedispatch.StatusReplayed
)

// ApprovalDispatchCommand 是跨 Schema UoW 的应用层兼容别名。
type ApprovalDispatchCommand = changedispatch.Command

// ApprovalDispatchResult 是跨 Schema UoW 回执的应用层兼容别名。
type ApprovalDispatchResult = changedispatch.Result

// ApprovalDispatcher 是跨 Schema UoW 端口的应用层兼容别名。
type ApprovalDispatcher = changedispatch.Dispatcher

// ApprovalWorkflowDispatch 是 Approved 响应可见的 Workflow 投递摘要。
type ApprovalWorkflowDispatch struct {
	RunID  foundation.ID
	NodeID foundation.ID
	JobID  int64
	Status ApprovalDispatchStatus
}

// ApprovalDecisionResult 是 Approval HTTP/Application 的统一结果；Rejected 的 Workflow 必须为 nil。
type ApprovalDecisionResult struct {
	Approval domain.Approval
	Workflow *ApprovalWorkflowDispatch
	Replayed bool
}

func validateApprovalDispatchResult(command ApprovalDispatchCommand, result ApprovalDispatchResult) error {
	approval := result.Approval
	if approval.ID == "" || approval.ProposalID != command.Approval.ProposalID || approval.RevisionID != command.Approval.RevisionID || !strings.EqualFold(approval.ChangeHash, command.Approval.ChangeHash) || approval.Decision != command.Approval.Decision || !sameApprovalGitHead(approval.ApprovedGitHead, command.Approval.ApprovedGitHead) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_RESULT_INVALID", false, errors.New("approval dispatch result binding is invalid"))
	}
	if approval.Decision == domain.DecisionRejected {
		if result.WorkflowRunID != "" || result.NodeRunID != "" || result.JobID != 0 || result.DispatchStatus != "" {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_RESULT_INVALID", false, errors.New("rejected approval returned workflow facts"))
		}
		return nil
	}
	if result.WorkflowRunID == "" || result.NodeRunID == "" || result.JobID < 1 {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_RESULT_INVALID", false, errors.New("approved dispatch result is incomplete"))
	}
	switch result.DispatchStatus {
	case DispatchStatusQueued, DispatchStatusRunning, DispatchStatusReplayed:
		return nil
	default:
		return foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_RESULT_INVALID", false, errors.New("approved dispatch status is invalid"))
	}
}

func decisionResultFromDispatch(result ApprovalDispatchResult) ApprovalDecisionResult {
	decision := ApprovalDecisionResult{Approval: result.Approval, Replayed: result.Replayed}
	if result.Approval.Decision == domain.DecisionApproved {
		decision.Workflow = &ApprovalWorkflowDispatch{RunID: result.WorkflowRunID, NodeID: result.NodeRunID, JobID: result.JobID, Status: result.DispatchStatus}
	}
	return decision
}

func sameApprovalGitHead(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.EqualFold(strings.TrimSpace(*left), strings.TrimSpace(*right))
}

func isNilApprovalDispatcher(dispatcher ApprovalDispatcher) bool {
	if dispatcher == nil {
		return true
	}
	value := reflect.ValueOf(dispatcher)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
