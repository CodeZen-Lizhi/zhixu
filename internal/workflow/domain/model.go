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
	Version                       int64
	CreatedAt, UpdatedAt          time.Time
	CompletedAt                   *time.Time
}

// NodeRun is one durable execution of a logical workflow node.
type NodeRun struct {
	ID, RunID            foundation.ID
	NodeKey, NodeType    string
	Status               Status
	Attempt              int
	Input, Output        json.RawMessage
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
	Payload              json.RawMessage
	OccurredAt           time.Time
}
