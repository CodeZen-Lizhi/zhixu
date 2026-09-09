package domain

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// SourceReady is an owner-verified immutable source tuple. It is emitted only
// with a persisted, security-passed chunked Attempt and contains no source text.
type SourceReady struct {
	WorkspaceID        foundation.ID `json:"workspace_id"`
	SourceID           foundation.ID `json:"source_id"`
	SourceVersionID    foundation.ID `json:"source_version_id"`
	ContentArtifactID  foundation.ID `json:"content_artifact_id"`
	ParseProjectionID  foundation.ID `json:"parse_projection_id"`
	ContentHash        string        `json:"content_hash"`
	IngestionAttemptID foundation.ID `json:"ingestion_attempt_id"`
	OccurredAt         time.Time     `json:"occurred_at"`
}

// Validate checks the transport shape. The Ingestion repository separately
// verifies owner/provenance bindings in the Attempt transition transaction.
func (ready SourceReady) Validate() error {
	for _, id := range []foundation.ID{
		ready.WorkspaceID, ready.SourceID, ready.SourceVersionID,
		ready.ContentArtifactID, ready.ParseProjectionID, ready.IngestionAttemptID,
	} {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return errors.New("source-ready identity is invalid")
		}
	}
	if len(ready.ContentHash) != 64 || strings.ToLower(ready.ContentHash) != ready.ContentHash {
		return errors.New("source-ready content hash is invalid")
	}
	if _, err := hex.DecodeString(ready.ContentHash); err != nil {
		return errors.New("source-ready content hash is invalid")
	}
	if ready.OccurredAt.IsZero() || ready.OccurredAt.Year() < 1 || ready.OccurredAt.Year() > 9999 {
		return errors.New("source-ready occurrence time is invalid")
	}
	return nil
}

// SourceReadyAppender participates in the caller's live transaction. It must
// not commit, open another transaction, or invoke downstream processing.
type SourceReadyAppender interface {
	AppendSourceReadyScoped(context.Context, foundation.TransactionScope, SourceReady) error
}
