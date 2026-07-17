package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

var runTransitions = map[RunStatus]map[RunStatus]struct{}{
	RunStatusPending: {
		RunStatusRunning: {}, RunStatusPaused: {}, RunStatusFailed: {}, RunStatusCancelled: {},
	},
	RunStatusRunning: {
		RunStatusWaitingForHuman: {}, RunStatusRetryWait: {}, RunStatusPaused: {}, RunStatusSucceeded: {}, RunStatusFailed: {}, RunStatusCancelled: {},
	},
	RunStatusWaitingForHuman: {
		RunStatusRunning: {}, RunStatusPaused: {}, RunStatusSucceeded: {}, RunStatusFailed: {}, RunStatusCancelled: {},
	},
	RunStatusRetryWait: {
		RunStatusRunning: {}, RunStatusPaused: {}, RunStatusFailed: {}, RunStatusCancelled: {},
	},
	RunStatusPaused: {
		RunStatusPending: {}, RunStatusRunning: {}, RunStatusWaitingForHuman: {}, RunStatusRetryWait: {}, RunStatusCancelled: {},
	},
}

var nodeTransitions = map[NodeStatus]map[NodeStatus]struct{}{
	NodeStatusPending: {
		NodeStatusRunning: {}, NodeStatusPaused: {}, NodeStatusCancelled: {},
	},
	NodeStatusRunning: {
		NodeStatusSucceeded: {}, NodeStatusRetryWait: {}, NodeStatusWaitingForHuman: {}, NodeStatusPaused: {}, NodeStatusFailed: {}, NodeStatusCancelled: {},
	},
	NodeStatusRetryWait: {
		NodeStatusRunning: {}, NodeStatusPaused: {}, NodeStatusCancelled: {},
	},
	NodeStatusWaitingForHuman: {
		NodeStatusSucceeded: {}, NodeStatusPaused: {}, NodeStatusCancelled: {},
	},
	NodeStatusPaused: {
		NodeStatusPending: {}, NodeStatusRetryWait: {}, NodeStatusWaitingForHuman: {}, NodeStatusCancelled: {},
	},
}

var attemptTransitions = map[AttemptStatus]map[AttemptStatus]struct{}{
	AttemptStatusRunning: {
		AttemptStatusSucceeded: {}, AttemptStatusWaitingForHuman: {}, AttemptStatusRetryScheduled: {}, AttemptStatusFailed: {}, AttemptStatusManualRecovery: {}, AttemptStatusLeaseLost: {}, AttemptStatusCancelled: {},
	},
}

// ValidateRunTransition 校验 Run 状态迁移是否属于冻结契约。
func ValidateRunTransition(from, to RunStatus) error {
	return validateStateTransition(runTransitions, from, to)
}

// ValidateNodeTransition 校验 Node 状态迁移是否属于冻结契约。
func ValidateNodeTransition(from, to NodeStatus) error {
	return validateStateTransition(nodeTransitions, from, to)
}

// ValidateAttemptTransition 校验 Attempt 是否只从 running 单向终结一次。
func ValidateAttemptTransition(from, to AttemptStatus) error {
	return validateStateTransition(attemptTransitions, from, to)
}

// ValidateTransition 保留 M4-A Node 状态校验调用方的兼容入口。
//
// Deprecated: 新代码使用 ValidateNodeTransition。
func ValidateTransition(from, to Status) error {
	return ValidateNodeTransition(from, to)
}

// IsTerminalRunStatus 判断 Run 是否已经不可重新打开。
func IsTerminalRunStatus(status RunStatus) bool {
	return status == RunStatusSucceeded || status == RunStatusFailed || status == RunStatusCancelled
}

// IsTerminalNodeStatus 判断 Node 是否已经不可重新打开。
func IsTerminalNodeStatus(status NodeStatus) bool {
	return status == NodeStatusSucceeded || status == NodeStatusFailed || status == NodeStatusCancelled
}

// IsTerminalAttemptStatus 判断 Attempt 是否已经结束且不可改写业务结果。
func IsTerminalAttemptStatus(status AttemptStatus) bool {
	return status != AttemptStatusRunning && validAttemptStatus(status)
}

func validateStateTransition[T ~string](allowed map[T]map[T]struct{}, from, to T) error {
	if targets, exists := allowed[from]; exists {
		if _, accepted := targets[to]; accepted {
			return nil
		}
	}
	return foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_INVALID_STATE_TRANSITION", false, errors.New("workflow state transition is not allowed"))
}

func validAttemptStatus(status AttemptStatus) bool {
	switch status {
	case AttemptStatusRunning, AttemptStatusSucceeded, AttemptStatusWaitingForHuman, AttemptStatusRetryScheduled, AttemptStatusFailed, AttemptStatusManualRecovery, AttemptStatusLeaseLost, AttemptStatusCancelled:
		return true
	default:
		return false
	}
}
