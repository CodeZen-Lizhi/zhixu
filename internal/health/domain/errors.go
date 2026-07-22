// Package domain 定义知识健康问题、扫描与定时任务的稳定领域契约和纯规则。
package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeIssueInvalid 表示 Health Issue 聚合或 Observation 不合法。
	ErrorCodeIssueInvalid = "HEALTH_ISSUE_INVALID"
	// ErrorCodeIssueDecisionInvalid 表示 Health Issue 决策输入不合法。
	ErrorCodeIssueDecisionInvalid = "HEALTH_ISSUE_DECISION_INVALID"
	// ErrorCodeIssueTransitionInvalid 表示 Health Issue 状态迁移或 CAS 不合法。
	ErrorCodeIssueTransitionInvalid = "HEALTH_ISSUE_TRANSITION_INVALID"
	// ErrorCodeScanInvalid 表示 Health Scan 事实或 coverage/checkpoint 不合法。
	ErrorCodeScanInvalid = "HEALTH_SCAN_INVALID"
	// ErrorCodeScanTransitionInvalid 表示 Health Scan 状态迁移不合法。
	ErrorCodeScanTransitionInvalid = "HEALTH_SCAN_TRANSITION_INVALID"
	// ErrorCodeScanScopeUnavailable 表示请求的 Scan Scope 在当前版本不可用。
	ErrorCodeScanScopeUnavailable = "HEALTH_SCAN_SCOPE_UNAVAILABLE"
	// ErrorCodeScanScopeStale 表示 Smart Collection scope 的版本、查询或 read model 修订已变化。
	ErrorCodeScanScopeStale = "HEALTH_SCAN_SCOPE_STALE"
	// ErrorCodeScheduleInvalid 表示 Health Schedule 契约不合法。
	ErrorCodeScheduleInvalid = "HEALTH_SCHEDULE_INVALID"
)

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func versionConflict(code, message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, errors.New(message))
}

func inconsistent(code, message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, errors.New(message))
}

func unavailable(code, message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, false, errors.New(message))
}
