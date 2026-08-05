// Package application coordinates durable Capture commands without owning Workspace content facts.
package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// CommandBinding is the canonical identity of an idempotent Capture create command.
type CommandBinding struct {
	WorkspaceID    foundation.ID
	IdempotencyKey string
	RequestHash    string
	CommandType    string
}

// CreateRecord contains every database fact that must commit with a Capture receipt.
type CreateRecord struct {
	Binding      CommandBinding
	Capture      domain.Capture
	Source       workspacedomain.Source
	Registration *workspacedomain.SourceRegistration
	OutboxID     foundation.ID
	EventKey     string
	CreatedAt    time.Time
}

// CreateResult is the exact response replayed for a repeated create command.
type CreateResult struct {
	Capture  domain.Capture
	Replayed bool
}

// Cursor is a stable Capture list boundary ordered by captured time and ID.
type Cursor struct {
	CapturedAt time.Time
	ID         foundation.ID
}

// ListQuery defines a Workspace-bound Capture page.
type ListQuery struct {
	WorkspaceID foundation.ID
	Kind        domain.Kind
	Status      domain.Status
	After       *Cursor
	Limit       int
}

// Page is a stable keyset page of Capture records.
type Page struct {
	Items []domain.Capture
	Next  *Cursor
}

// Repository persists Capture facts and exact command receipts.
type Repository interface {
	ReplayCreate(context.Context, CommandBinding) (CreateResult, bool, error)
	Create(context.Context, CreateRecord) (CreateResult, error)
	Get(context.Context, foundation.ID, foundation.ID) (domain.Capture, error)
	List(context.Context, ListQuery) (Page, error)
}

// ManagedContentWriter stages exact bytes and promotes them only after the
// corresponding database facts are confirmed.
type ManagedContentWriter interface {
	StageManagedBytes(context.Context, foundation.ID, string, []byte, string) (workspacedomain.ManagedContentStage, error)
	PublishManagedBytes(context.Context, foundation.ID, workspacedomain.ManagedContentStage) (workspacedomain.ContentCapture, error)
	DiscardManagedBytes(context.Context, foundation.ID, workspacedomain.ManagedContentStage) error
}

// URLFetchResult is a bounded public-web response ready to become an immutable Source Version.
type URLFetchResult struct {
	Content   []byte
	MediaType string
	FinalURL  string
}

// URLFetcher retrieves public HTML under a fail-closed network policy.
type URLFetcher interface {
	Fetch(context.Context, string) (URLFetchResult, error)
}

// Dependencies are the explicit ports required by the Capture command service.
type Dependencies struct {
	Repository Repository
	Content    ManagedContentWriter
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
}

// TextCommand creates an immutable inline text Capture.
type TextCommand struct {
	WorkspaceID    foundation.ID
	DisplayName    string
	Text           string
	IdempotencyKey string
}

// URLCommand creates a durable URL Capture before any network access occurs.
type URLCommand struct {
	WorkspaceID    foundation.ID
	DisplayName    string
	URL            string
	IdempotencyKey string
}

// UploadCommand creates an immutable file or image Capture from bounded bytes.
type UploadCommand struct {
	WorkspaceID    foundation.ID
	Kind           domain.Kind
	DisplayName    string
	FileName       string
	MediaType      string
	Content        []byte
	IdempotencyKey string
}

// RetryCommand schedules a new durable processing run for a failed Capture version.
type RetryCommand struct {
	WorkspaceID     foundation.ID
	CaptureID       foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

// RetryRecord contains every fact that must commit with a Capture retry receipt.
type RetryRecord struct {
	Binding         CommandBinding
	CaptureID       foundation.ID
	ExpectedVersion int64
	OutboxID        foundation.ID
	EventKey        string
	CreatedAt       time.Time
}

// RetryResult is the exact response replayed for a repeated retry command.
type RetryResult struct {
	Capture  domain.Capture
	Replayed bool
}

// RetryScheduler owns the atomic Capture reset, outbox event, and retry receipt.
type RetryScheduler interface {
	ReplayRetry(context.Context, CommandBinding) (RetryResult, bool, error)
	ScheduleRetry(context.Context, RetryRecord) (RetryResult, error)
}
