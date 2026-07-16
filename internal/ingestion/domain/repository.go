package domain

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// AttemptRecord 为受乐观锁状态机保护的 Ingestion Attempt 增加持久化元数据。
type AttemptRecord struct {
	Attempt
	ParseProjectionID *foundation.ID
	StartedAt         time.Time
	CompletedAt       *time.Time
	Version           int64
}

// AttemptResult reports whether an idempotent create inserted a new Attempt.
type AttemptResult struct {
	Attempt AttemptRecord
	Created bool
}

// AttemptTransition is one optimistic, validated lifecycle transition.
type AttemptTransition struct {
	ID                foundation.ID
	ExpectedVersion   int64
	Status            AttemptStatus
	SecurityStatus    SecurityStatus
	ParseProjectionID *foundation.ID
	FailureStage      string
	ErrorCode         string
	Retryable         bool
	Warnings          []Warning
	CompletedAt       *time.Time
}

// ParseProjection is a shared immutable parser output identity.
type ParseProjection struct {
	ID                    foundation.ID
	WorkspaceID           foundation.ID
	ContentArtifactID     foundation.ID
	ParserID              string
	ParserVersion         string
	ParserConfigHash      string
	SchemaVersion         string
	NormalizedContentHash string
	Warnings              []Warning
	CreatedAt             time.Time
}

// ProjectionWrite atomically creates or reuses a projection and its spans/chunks.
type ProjectionWrite struct {
	SourceVersionID  foundation.ID
	SourceRevisionID *foundation.ID
	Projection       ParseProjection
	Spans            []SourceSpan
	Chunks           []CanonicalChunk
	CreatedAt        time.Time
}

// ProjectionResult returns persisted identities after idempotent reuse.
type ProjectionResult struct {
	Projection ParseProjection
	Spans      []SourceSpan
	Chunks     []CanonicalChunk
	Created    bool
}

// Repository persists immutable ingestion attempts and parser projections.
type Repository interface {
	CreateAttempt(context.Context, AttemptRecord) (AttemptResult, error)
	GetAttempt(context.Context, foundation.ID) (AttemptRecord, error)
	TransitionAttempt(context.Context, AttemptTransition) (AttemptRecord, error)
	GetProjection(context.Context, foundation.ID, string, string) (ProjectionResult, error)
	SaveProjection(context.Context, ProjectionWrite) (ProjectionResult, error)
}
