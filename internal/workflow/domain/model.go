// Package domain contains the durable workflow business model and invariants.
package domain

import (
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Status is a persisted Workflow Run or Node Run lifecycle state.
type Status string

const (
	StatusPending         Status = "pending"
	StatusRunning         Status = "running"
	StatusWaitingForHuman Status = "waiting_for_human"
	StatusSucceeded       Status = "succeeded"
	StatusFailed          Status = "failed"
	StatusCancelled       Status = "cancelled"
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
	CreatedAt       time.Time
}

// Run is one execution pinned to a Definition version.
type Run struct {
	ID, WorkspaceID, DefinitionID foundation.ID
	Status                        Status
	Input, Output                 json.RawMessage
	// IdempotencyKey 在 Workspace 内绑定一次 Runtime Start 请求。
	IdempotencyKey string
	// RequestHash 固化注册 Definition 与 canonical input 的请求身份。
	RequestHash          string
	Version              int64
	CreatedAt, UpdatedAt time.Time
	CompletedAt          *time.Time
}

// NodeRun is one durable execution of a logical workflow node.
type NodeRun struct {
	ID, RunID         foundation.ID
	NodeKey, NodeType string
	Status            Status
	Attempt           int
	Input, Output     json.RawMessage
	// IdempotencyKey 在同一 Run 内稳定标识逻辑节点启动。
	IdempotencyKey string
	// InputSchemaVersion 固化节点消费的输入契约版本。
	InputSchemaVersion int
	// OutputSchemaVersion 固化节点产出的输出契约版本。
	OutputSchemaVersion int
	// DispatchNo 是从 1 开始的持久投递序号。
	DispatchNo           int
	LeaseOwner           string
	LeaseUntil           *time.Time
	Version              int64
	CreatedAt, UpdatedAt time.Time
	CompletedAt          *time.Time
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
