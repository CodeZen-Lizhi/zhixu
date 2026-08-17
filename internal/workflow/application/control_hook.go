package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WorkflowControlEvent 是 Runtime 在首次持久化控制命令后、提交前发送的事务内事件。
// 精确幂等重放不会产生该事件。
type WorkflowControlEvent struct {
	WorkspaceID      foundation.ID
	WorkflowRunID    foundation.ID
	Action           ControlAction
	IdempotencyKey   string
	ExpectedVersion  int64
	PersistedControl ControlPersistenceResult
	OccurredAt       time.Time
}

// WorkflowControlHook 在 Runtime 当前事务内同步一个首次控制命令。
// 实现方只处理自己拥有的 Workflow，其余 Run 必须 no-op，且不得提交事务或执行事务外副作用。
type WorkflowControlHook interface {
	// OnWorkflowControl 使用调用方持有的事务同步一个首次持久化的控制命令。
	OnWorkflowControl(context.Context, any, WorkflowControlEvent) error
}
