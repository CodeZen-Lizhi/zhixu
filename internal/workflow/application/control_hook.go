package application

import (
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
