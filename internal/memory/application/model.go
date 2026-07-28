// Package application orchestrates Memory commands without exposing storage details.
package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
)

// CommandType identifies a durable, idempotent Memory mutation.
type CommandType string

const (
	// CommandCreateCandidate persists a non-effective candidate.
	CommandCreateCandidate CommandType = "CREATE_CANDIDATE"
	// CommandConfirm is the only user confirmation command.
	CommandConfirm CommandType = "CONFIRM"
	// CommandUpdate updates a nonterminal Memory payload.
	CommandUpdate CommandType = "UPDATE"
	// CommandPause pauses confirmed active Memory.
	CommandPause CommandType = "PAUSE"
	// CommandResume resumes confirmed paused Memory.
	CommandResume CommandType = "RESUME"
	// CommandDelete writes a retained tombstone.
	CommandDelete CommandType = "DELETE"
)

// CommandBinding is the immutable receipt identity for a Memory mutation.
type CommandBinding struct {
	WorkspaceID     foundation.ID
	Owner           domain.Principal
	MemoryID        foundation.ID
	IdempotencyKey  string
	RequestHash     string
	CommandType     CommandType
	ExpectedVersion int64
}

// CommandResult is the exact Memory snapshot returned by a durable command.
type CommandResult struct {
	Memory   domain.Memory
	Replayed bool
}

// CandidateRecord atomically creates one candidate and its receipt/audit fact.
type CandidateRecord struct {
	Binding CommandBinding
	Memory  domain.Memory
}

// MutationRecord atomically CAS-updates Memory, appends an audit fact, and stores a receipt.
type MutationRecord struct {
	Binding CommandBinding
	Current domain.Memory
	Next    domain.Memory
	Action  domain.AuditAction
	Actor   *domain.Principal
}

// Scope restricts normal owner-visible queries.
type Scope struct {
	WorkspaceID foundation.ID
	Owner       domain.Principal
}

// Cursor is a stable keyset boundary for owner-visible Memory lists.
type Cursor struct {
	UpdatedAt time.Time
	ID        foundation.ID
}

// ListQuery returns one bounded owner-visible Memory page.
type ListQuery struct {
	Scope    Scope
	Types    []domain.Type
	Statuses []domain.Status
	Limit    int
	After    *Cursor
}

// ListPage is an owner-visible, stable Memory page.
type ListPage struct {
	Items []domain.Memory
	Next  *Cursor
}

// EffectiveQuery restricts agent/service context reads to one principal and optional task.
type EffectiveQuery struct {
	Scope       Scope
	TaskScopeID *foundation.ID
	Limit       int
}

// Repository is the Memory persistence, idempotency, audit, and query boundary.
type Repository interface {
	FindCommand(context.Context, CommandBinding) (CommandResult, bool, error)
	CreateCandidate(context.Context, CandidateRecord) (CommandResult, error)
	Mutate(context.Context, MutationRecord) (CommandResult, error)
	Get(context.Context, Scope, foundation.ID) (domain.Memory, error)
	List(context.Context, ListQuery) (ListPage, error)
	LoadEffective(context.Context, EffectiveQuery) ([]domain.Memory, error)
	ExpireDue(context.Context, int) (int, error)
}

// Dependencies contains the only mutable dependencies used by the service.
type Dependencies struct {
	Repository Repository
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
}

// CreateCandidateCommand describes a candidate suggestion for a target owner.
type CreateCandidateCommand struct {
	WorkspaceID    foundation.ID
	Owner          domain.Principal
	Type           domain.Type
	Content        json.RawMessage
	Source         domain.Source
	TaskScopeID    *foundation.ID
	ExpiresAt      *time.Time
	IdempotencyKey string
}

// EditCommand describes a content, scope, or expiry edit without changing type or source provenance.
type EditCommand struct {
	Scope           Scope
	MemoryID        foundation.ID
	ExpectedVersion int64
	Content         json.RawMessage
	TaskScopeID     *foundation.ID
	ExpiresAt       *time.Time
	IdempotencyKey  string
}

// TransitionCommand describes an owner lifecycle mutation with optimistic versioning.
type TransitionCommand struct {
	Scope           Scope
	MemoryID        foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
}
