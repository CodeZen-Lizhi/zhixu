// Package domain contains the durable workflow business model and invariants.
package domain

import (
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// RunStatus 是持久化 Workflow Run 的生命周期状态。
type RunStatus string

// NodeStatus 是持久化 Node Run 的生命周期状态。
type NodeStatus string

// AttemptStatus 是一次租约执行尝试的单向生命周期状态。
type AttemptStatus string

const (
	// RunStatusPending 表示 Run 已创建但尚未开始执行。
	RunStatusPending RunStatus = "pending"
	// RunStatusRunning 表示 Run 存在可运行或正在运行的节点。
	RunStatusRunning RunStatus = "running"
	// RunStatusWaitingForHuman 表示 Run 正在等待人工决策。
	RunStatusWaitingForHuman RunStatus = "waiting_for_human"
	// RunStatusRetryWait 表示 Run 仅剩等待业务重试的工作。
	RunStatusRetryWait RunStatus = "retry_wait"
	// RunStatusPaused 表示 Run 已到达安全暂停点。
	RunStatusPaused RunStatus = "paused"
	// RunStatusSucceeded 表示 Run 已成功终结。
	RunStatusSucceeded RunStatus = "succeeded"
	// RunStatusFailed 表示 Run 已失败终结。
	RunStatusFailed RunStatus = "failed"
	// RunStatusCancelled 表示 Run 已取消终结。
	RunStatusCancelled RunStatus = "cancelled"
)

const (
	// NodeStatusPending 表示节点已创建且等待首次 Claim。
	NodeStatusPending NodeStatus = "pending"
	// NodeStatusRunning 表示节点持有有效执行租约。
	NodeStatusRunning NodeStatus = "running"
	// NodeStatusWaitingForHuman 表示节点已释放租约并等待人工决策。
	NodeStatusWaitingForHuman NodeStatus = "waiting_for_human"
	// NodeStatusRetryWait 表示节点等待下一次业务重试调度。
	NodeStatusRetryWait NodeStatus = "retry_wait"
	// NodeStatusPaused 表示节点因 Run 暂停而不可 Claim。
	NodeStatusPaused NodeStatus = "paused"
	// NodeStatusSucceeded 表示节点已成功终结。
	NodeStatusSucceeded NodeStatus = "succeeded"
	// NodeStatusFailed 表示节点已失败终结。
	NodeStatusFailed NodeStatus = "failed"
	// NodeStatusCancelled 表示节点已取消终结。
	NodeStatusCancelled NodeStatus = "cancelled"
)

const (
	// AttemptStatusRunning 表示 Attempt 当前持有节点租约。
	AttemptStatusRunning AttemptStatus = "running"
	// AttemptStatusSucceeded 表示 Attempt 成功产出结果。
	AttemptStatusSucceeded AttemptStatus = "succeeded"
	// AttemptStatusWaitingForHuman 表示 Attempt 已创建人工任务并释放租约。
	AttemptStatusWaitingForHuman AttemptStatus = "waiting_for_human"
	// AttemptStatusRetryScheduled 表示 Attempt 已原子安排业务重试。
	AttemptStatusRetryScheduled AttemptStatus = "retry_scheduled"
	// AttemptStatusFailed 表示 Attempt 以不可重试失败结束。
	AttemptStatusFailed AttemptStatus = "failed"
	// AttemptStatusManualRecovery 表示 Attempt 需要人工恢复。
	AttemptStatusManualRecovery AttemptStatus = "manual_recovery"
	// AttemptStatusLeaseLost 表示 Attempt 的租约已过期并被回收。
	AttemptStatusLeaseLost AttemptStatus = "lease_lost"
	// AttemptStatusCancelled 表示 Attempt 在已证明的取消语义下结束。
	AttemptStatusCancelled AttemptStatus = "cancelled"
)

// Status 是旧 Repository 的 NodeStatus 兼容别名。
//
// Deprecated: 新代码必须显式使用 RunStatus 或 NodeStatus。
type Status = NodeStatus

// 旧状态常量保持为无类型字符串，使 M4-A 的 Run/Node 结构字面量在迁移期兼容。
const (
	StatusPending         = "pending"
	StatusRunning         = "running"
	StatusWaitingForHuman = "waiting_for_human"
	StatusRetryWait       = "retry_wait"
	StatusPaused          = "paused"
	StatusSucceeded       = "succeeded"
	StatusFailed          = "failed"
	StatusCancelled       = "cancelled"
)

// HumanTaskStatus is the one-shot lifecycle of a human decision.
type HumanTaskStatus string

const (
	HumanTaskPending   HumanTaskStatus = "pending"
	HumanTaskSubmitted HumanTaskStatus = "submitted"
	HumanTaskExpired   HumanTaskStatus = "expired"
	HumanTaskCancelled HumanTaskStatus = "cancelled"
)

// Definition is an immutable versioned workflow graph.
type Definition struct {
	ID, WorkspaceID foundation.ID
	Key             string
	Version         int64
	Graph           json.RawMessage
	// GraphHash 是从不可变持久 Graph 重新计算的 canonical SHA-256。
	GraphHash string
	CreatedAt time.Time
}

// Run is one execution pinned to a Definition version.
type Run struct {
	ID, WorkspaceID, DefinitionID foundation.ID
	Status                        RunStatus
	Input, Output                 json.RawMessage
	// IdempotencyKey 在 Workspace 内绑定一次 Runtime Start 请求。
	IdempotencyKey string
	// RequestHash 固化注册 Definition 与 canonical input 的请求身份。
	RequestHash          string
	Version              int64
	CreatedAt, UpdatedAt time.Time
	CompletedAt          *time.Time
	// PauseRequestedAt/CancelRequestedAt are database-owned control projections.
	PauseRequestedAt, CancelRequestedAt *time.Time
}

// NodeRun is one durable execution of a logical workflow node.
type NodeRun struct {
	ID, RunID         foundation.ID
	NodeKey, NodeType string
	Status            NodeStatus
	// Attempt 是数据库旧列 node_run.attempt 的 latest-attempt 兼容投影。
	// 新 Attempt 历史只以 NodeAttempt 为事实源。
	Attempt       int
	Input, Output json.RawMessage
	// IdempotencyKey 在同一 Run 内稳定标识逻辑节点启动。
	IdempotencyKey string
	// InputSchemaVersion 固化节点消费的输入契约版本。
	InputSchemaVersion int
	// OutputSchemaVersion 固化节点产出的输出契约版本。
	OutputSchemaVersion int
	// DispatchNo 是从 1 开始的持久投递序号。
	DispatchNo int
	LeaseOwner string
	LeaseUntil *time.Time
	// RetryNo counts committed business retries; transport delivery does not change it.
	RetryNo              int
	NextAttemptAt        *time.Time
	FailureClass         FailureClass
	ErrorKind            foundation.ErrorKind
	ErrorCode            string
	ErrorSummary         string
	Version              int64
	CreatedAt, UpdatedAt time.Time
	CompletedAt          *time.Time
}

// NodeAttempt 是一次成功取得 Workflow lease 后追加的持久执行事实。
type NodeAttempt struct {
	ID, NodeRunID                  foundation.ID
	AttemptNo, DispatchNo, RetryNo int
	RiverJobID                     int64
	RiverJobAttempt                int
	DeliveryID, LeaseOwner         string
	LeaseUntil                     time.Time
	Status                         AttemptStatus
	OutputSchemaVersion            int
	OutputHash                     string
	FailureClass                   FailureClass
	ErrorKind                      foundation.ErrorKind
	ErrorCode, ErrorSummary        string
	NextAttemptAt                  *time.Time
	StartedAt, HeartbeatAt         time.Time
	EndedAt                        *time.Time
}

// DeliveryIdentity 将一次 River delivery 绑定到稳定节点 generation。
type DeliveryIdentity struct {
	NodeRunID  foundation.ID
	DispatchNo int
	DeliveryID string
}

// LeaseFence 是 Heartbeat 和结果事务必须同时校验的旧 owner 隔离令牌。
type LeaseFence struct {
	Owner       string
	AttemptNo   int
	NodeVersion int64
}

// HumanTask represents a versioned decision that can be submitted once.
type HumanTask struct {
	ID, RunID, NodeRunID   foundation.ID
	Status                 HumanTaskStatus
	ExpectedInputSchema    json.RawMessage
	TargetVersion          int64
	Decision               json.RawMessage
	ExpiresAt, SubmittedAt *time.Time
	CreatedAt              time.Time
}

// OutboxEvent is an event persisted atomically with workflow state.
type OutboxEvent struct {
	ID, WorkspaceID      foundation.ID
	RunID                *foundation.ID
	Type, IdempotencyKey string
	// EventKey 是事件实例的稳定业务键。
	EventKey string
	// SchemaVersion 是事件 Payload 的契约版本。
	SchemaVersion int
	// EventVersion 是同一 EventKey 下从 1 开始的版本。
	EventVersion int64
	Payload      json.RawMessage
	OccurredAt   time.Time
}
