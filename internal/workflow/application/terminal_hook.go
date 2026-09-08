package application

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// WorkflowTerminalOutcome 表示 Workflow Node 已持久化的稳定终态结果。
type WorkflowTerminalOutcome string

const (
	// WorkflowTerminalOutcomeSucceeded 表示 Node 成功终结。
	WorkflowTerminalOutcomeSucceeded WorkflowTerminalOutcome = "SUCCEEDED"
	// WorkflowTerminalOutcomeFailed 表示 Node 失败终结。
	WorkflowTerminalOutcomeFailed WorkflowTerminalOutcome = "FAILED"
	// WorkflowTerminalOutcomeCancelled 表示 Node 取消终结。
	WorkflowTerminalOutcomeCancelled WorkflowTerminalOutcome = "CANCELLED"
)

// WorkflowNodeTerminalEvent 是 Runtime 在提交 Node 终态前发送给领域适配器的事务内事件。
type WorkflowNodeTerminalEvent struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	NodeKind      string
	// NodeAttemptID 在 delivery/checkpoint 终态中绑定当前 Attempt；尚未尝试的直接取消可为空。
	NodeAttemptID  foundation.ID
	Outcome        WorkflowTerminalOutcome
	FailureClass   domain.FailureClass
	FailureCode    string
	FailureSummary string
	// TerminalOutput 是成功 Node 已持久化的原始 JSON；失败或取消时为空。
	// 使用 string 保持事件可比较，便于精确验证 Hook 契约。
	TerminalOutput string
	TerminalAt     time.Time
}
