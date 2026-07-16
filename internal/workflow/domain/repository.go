package domain

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// StartRequest contains records atomically created when a run starts.
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
	Start(context.Context, StartRequest) (Run, error)
	GetRun(context.Context, foundation.ID) (Run, error)
	ClaimNode(context.Context, foundation.ID, string, time.Time, time.Time) (NodeRun, error)
	HeartbeatNode(context.Context, foundation.ID, string, time.Time, time.Time) (NodeRun, error)
	CompleteNode(context.Context, Completion) (NodeRun, error)
	CreateHumanTask(context.Context, HumanTask, OutboxEvent, time.Time) (HumanTask, error)
	SubmitHumanTask(context.Context, foundation.ID, int64, json.RawMessage, time.Time, OutboxEvent) (HumanTask, error)
}
