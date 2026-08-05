package domain

import (
	"context"
	"encoding/hex"
	"errors"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// GitCaptureBatch atomically registers all supported files observed in one
// fast-forward range and tombstones removed logical Sources.
type GitCaptureBatch struct {
	WorkspaceID   foundation.ID
	RunID         foundation.ID
	BeforeCommit  string
	AfterCommit   string
	RequestHash   string
	Registrations []SourceRegistration
	RemovedPaths  []string
	ObservedAt    time.Time
}

// GitCaptureBatchResult is the exact persisted Source set used by batch ingestion.
type GitCaptureBatchResult struct {
	Registrations    []SourceRegistrationResult
	RemovedSourceIDs []foundation.ID
	Replayed         bool
}

// GitCaptureRepository owns commit-order fencing and Source lifecycle writes.
type GitCaptureRepository interface {
	ApplyGitCaptureBatch(context.Context, GitCaptureBatch) (GitCaptureBatchResult, error)
	CompleteGitCaptureBatch(context.Context, foundation.ID, foundation.ID, string, string) error
}

// ValidateGitCaptureBatch verifies stable identities and deterministic ordering.
func ValidateGitCaptureBatch(batch GitCaptureBatch) error {
	if !validWorkspaceID(batch.WorkspaceID) || !validWorkspaceID(batch.RunID) || !validGitOID(batch.BeforeCommit) ||
		!validGitOID(batch.AfterCommit) || batch.BeforeCommit == batch.AfterCommit || !validDigest(batch.RequestHash) || batch.ObservedAt.IsZero() {
		return gitCaptureInvalid("Git capture batch identity is invalid")
	}
	locations := make(map[string]struct{}, len(batch.Registrations)+len(batch.RemovedPaths))
	lastLocation := ""
	for _, registration := range batch.Registrations {
		location := registration.Source.OriginalLocation
		if !validCapturePath(location) || location <= lastLocation || registration.Source.WorkspaceID != batch.WorkspaceID ||
			registration.Artifact.WorkspaceID != batch.WorkspaceID || registration.Version.OriginalContentLocation != location {
			return gitCaptureInvalid("Git capture registrations must be Workspace-bound and sorted")
		}
		locations[location] = struct{}{}
		lastLocation = location
	}
	if !sort.StringsAreSorted(batch.RemovedPaths) {
		return gitCaptureInvalid("Git capture removed paths must be sorted")
	}
	for index, location := range batch.RemovedPaths {
		if !validCapturePath(location) || (index > 0 && batch.RemovedPaths[index-1] == location) {
			return gitCaptureInvalid("Git capture removed paths must be unique")
		}
		if _, exists := locations[location]; exists {
			return gitCaptureInvalid("Git capture path cannot be updated and removed")
		}
	}
	return nil
}

func validWorkspaceID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validGitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validCapturePath(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\\\x00\r\n") &&
		!strings.HasPrefix(value, "/") && path.Clean(value) == value && value != "." && !strings.HasPrefix(value, "../")
}

func gitCaptureInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "GIT_CAPTURE_BATCH_INVALID", false, errors.New(message))
}
