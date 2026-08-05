package domain

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// StartRequest contains records atomically created by the pre-Runtime repository path.
//
// Deprecated: Registered Workflow Start 使用 application.RuntimeStarter；该类型仅保留给迁移期 Adapter 兼容测试。
type StartRequest struct {
	Definition Definition
	Run        Run
	FirstNode  NodeRun
	Event      OutboxEvent
}

// Completion atomically completes a leased node and records its event.
type Completion struct {
	NodeID     foundation.ID
	LeaseOwner string
	Output     json.RawMessage
	At         time.Time
	Event      OutboxEvent
}

// Repository persists workflow state without exposing pgx types.
type Repository interface {
	GetRun(context.Context, foundation.ID) (Run, error)
	ClaimNode(context.Context, foundation.ID, string, time.Time, time.Time) (NodeRun, error)
	HeartbeatNode(context.Context, foundation.ID, string, time.Time, time.Time) (NodeRun, error)
	CompleteNode(context.Context, Completion) (NodeRun, error)
	CreateHumanTask(context.Context, HumanTask, OutboxEvent, time.Time) (HumanTask, error)
	SubmitHumanTask(context.Context, foundation.ID, int64, json.RawMessage, time.Time, OutboxEvent) (HumanTask, error)
}

// RunListItem 是 Workflow 列表的持久摘要。
type RunListItem struct {
	Run
	DefinitionKey     string
	DefinitionVersion int64
	WaitingForHuman   bool
}

// RunListQuery 描述 Workflow Run 列表的 Workspace 作用域、状态筛选和稳定分页边界。
type RunListQuery struct {
	WorkspaceID foundation.ID
	Status      RunStatus
	CursorTime  *time.Time
	CursorID    foundation.ID
	Limit       int
}

// RunListRepository 提供 Workspace 绑定的稳定 Run 分页。
type RunListRepository interface {
	ListRuns(context.Context, RunListQuery) ([]RunListItem, bool, error)
}

// PendingHumanTaskRepository reads the only actionable Human Task for a Run.
// Registered Workflow graphs are linear at each human checkpoint, so more than
// one pending task is a persistence consistency violation.
type PendingHumanTaskRepository interface {
	GetPendingHumanTask(context.Context, foundation.ID) (HumanTask, bool, error)
}
