// Package dispatch 定义 Change Control Application 与跨 Schema PostgreSQL UoW 的稳定契约。
package dispatch

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Status 是 Approval 自动投递在 API 边界可见的稳定状态。
type Status string

const (
	// StatusQueued 表示首次原子创建了待执行 River Job。
	StatusQueued Status = "queued"
	// StatusRunning 表示重放时对应 Workflow 已经开始执行。
	StatusRunning Status = "running"
	// StatusReplayed 表示完整 Approval→Run→Node→Job 绑定被原样重放。
	StatusReplayed Status = "replayed"
)

// Command 把外部安全门观察值与不可变 Approval 决定交给单一数据库 UoW。
// 完整绑定重放时 ObservedBaseHash/ObservedGitHead 必须为空，禁止重新读取或覆盖审批基线。
type Command struct {
	WorkspaceID      foundation.ID
	Approval         domain.Approval
	ObservedBaseHash string
	ObservedGitHead  string
}

// Result 是 Approval、Workflow 和 River 原子提交后的稳定回执。
type Result struct {
	Approval       domain.Approval
	WorkflowRunID  foundation.ID
	NodeRunID      foundation.ID
	JobID          int64
	DispatchStatus Status
	Replayed       bool
}

// Dispatcher 维护 Proposal→Approval→Workflow→River 的跨 Schema 原子不变量。
type Dispatcher interface {
	// DecideAndDispatch 原子创建或重放 Approval 及其唯一 Safe Writeback Workflow；Rejected 不创建 Workflow。
	DecideAndDispatch(context.Context, Command) (Result, error)
}
