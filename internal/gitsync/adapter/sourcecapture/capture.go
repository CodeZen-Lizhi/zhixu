// Package sourcecapture bridges a Git fast-forward to Workspace-owned Source
// lifecycle writes and one Retrieval snapshot activation.
package sourcecapture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	gitsyncapplication "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	gitsyncdomain "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// SourcePreparer keeps content capture and Source metadata under Workspace ownership.
type SourcePreparer interface {
	PrepareCommittedSourceBlob(context.Context, workspaceapplication.CaptureCommittedSourceRequest, workspacedomain.CommittedBlob) (workspacedomain.SourceRegistration, error)
}

// BatchRefresher activates one Retrieval snapshot for the entire commit range.
type BatchRefresher interface {
	RefreshBatch(context.Context, retrievalapplication.BatchSourceRefreshRequest) (retrievalapplication.BatchSourceRefreshResult, error)
}

// Dependencies are the four owner boundaries used by Capture.
type Dependencies struct {
	Trees      workspacedomain.CommittedTreeReader
	Blobs      workspacedomain.CommittedBlobReader
	Sources    SourcePreparer
	Repository workspacedomain.GitCaptureRepository
	Refresher  BatchRefresher
	Clock      foundation.Clock
}

// Capture performs exact tree enumeration, atomic Source lifecycle changes,
// and one index activation before advancing the commit-order checkpoint.
type Capture struct{ dependencies Dependencies }

// New creates a fail-closed Git fast-forward capture adapter.
func New(dependencies Dependencies) (*Capture, error) {
	if dependencies.Trees == nil || dependencies.Blobs == nil || dependencies.Sources == nil ||
		dependencies.Repository == nil || dependencies.Refresher == nil || dependencies.Clock == nil {
		return nil, captureError(foundation.ErrorDependencyUnavailable, "GIT_CAPTURE_DEPENDENCY_UNAVAILABLE", true, "Git capture dependencies are incomplete")
	}
	return &Capture{dependencies: dependencies}, nil
}

// CaptureGitFastForward implements the Git Sync external-change follow-up.
func (capture *Capture) CaptureGitFastForward(ctx context.Context, command gitsyncapplication.ExternalChangeCommand) (gitsyncapplication.ExternalChangeResult, error) {
	if capture == nil || !validCommand(command) {
		return gitsyncapplication.ExternalChangeResult{}, captureError(foundation.ErrorInvalidInput, gitsyncdomain.ErrorCodeInvalid, false, "Git capture command is invalid")
	}
	changes, err := capture.dependencies.Trees.ReadCommittedTreeChanges(ctx, command.WorkspaceID, command.BeforeCommit, command.AfterCommit)
	if err != nil {
		return gitsyncapplication.ExternalChangeResult{}, err
	}
	registrations := make([]workspacedomain.SourceRegistration, 0, len(changes))
	removedPaths := make([]string, 0, len(changes))
	for _, change := range changes {
		if _, supported := workspaceapplication.CommittedSourceMediaType(change.RelativePath); !supported {
			continue
		}
		switch change.Kind {
		case workspacedomain.CommittedTreeChangeDelete:
			removedPaths = append(removedPaths, change.RelativePath)
		case workspacedomain.CommittedTreeChangeUpsert:
			blob, readErr := capture.dependencies.Blobs.ReadCommittedBlob(ctx, command.WorkspaceID, command.AfterCommit, change.RelativePath)
			if readErr != nil {
				return gitsyncapplication.ExternalChangeResult{}, readErr
			}
			digest := sha256.Sum256(blob.Bytes)
			contentHash := hex.EncodeToString(digest[:])
			registration, prepareErr := capture.dependencies.Sources.PrepareCommittedSourceBlob(ctx, workspaceapplication.CaptureCommittedSourceRequest{
				WorkspaceID: command.WorkspaceID, GitCommit: command.AfterCommit,
				RelativePath: change.RelativePath, ExpectedHash: contentHash,
			}, blob)
			if prepareErr != nil {
				return gitsyncapplication.ExternalChangeResult{}, prepareErr
			}
			registrations = append(registrations, registration)
		default:
			return gitsyncapplication.ExternalChangeResult{}, captureError(foundation.ErrorConsistencyViolation, "GIT_CAPTURE_TREE_CHANGE_INVALID", false, "Git tree reader returned an unknown change kind")
		}
	}
	requestHash, err := captureRequestHash(command, changes)
	if err != nil {
		return gitsyncapplication.ExternalChangeResult{}, captureError(foundation.ErrorDependencyUnavailable, "GIT_CAPTURE_HASH_UNAVAILABLE", true, "Git capture request could not be hashed")
	}
	observedAt := capture.dependencies.Clock.Now().UTC()
	if observedAt.IsZero() {
		return gitsyncapplication.ExternalChangeResult{}, captureError(foundation.ErrorConsistencyViolation, "GIT_CAPTURE_CLOCK_INVALID", false, "Git capture clock returned zero time")
	}
	persisted, err := capture.dependencies.Repository.ApplyGitCaptureBatch(ctx, workspacedomain.GitCaptureBatch{
		WorkspaceID: command.WorkspaceID, RunID: command.RunID,
		BeforeCommit: command.BeforeCommit, AfterCommit: command.AfterCommit,
		RequestHash: requestHash, Registrations: registrations, RemovedPaths: removedPaths, ObservedAt: observedAt,
	})
	if err != nil {
		return gitsyncapplication.ExternalChangeResult{}, err
	}
	if len(persisted.Registrations) != len(registrations) {
		return gitsyncapplication.ExternalChangeResult{}, captureError(foundation.ErrorConsistencyViolation, "GIT_CAPTURE_REGISTRATION_INCOMPLETE", false, "Git capture repository returned an incomplete batch")
	}
	if len(persisted.Registrations) == 0 && len(persisted.RemovedSourceIDs) == 0 {
		if err := capture.dependencies.Repository.CompleteGitCaptureBatch(ctx, command.WorkspaceID, command.RunID, command.BeforeCommit, command.AfterCommit); err != nil {
			return gitsyncapplication.ExternalChangeResult{}, err
		}
		return gitsyncapplication.ExternalChangeResult{NoIndexRequired: true}, nil
	}
	upserts := make([]retrievalapplication.BatchSourceRefreshEntry, 0, len(persisted.Registrations))
	for _, registration := range persisted.Registrations {
		upserts = append(upserts, retrievalapplication.BatchSourceRefreshEntry{
			SourceID: registration.Source.ID, SourceVersionID: registration.Version.ID, AttemptNumber: 1,
		})
	}
	sort.Slice(upserts, func(left, right int) bool { return upserts[left].SourceID < upserts[right].SourceID })
	refreshed, err := capture.dependencies.Refresher.RefreshBatch(ctx, retrievalapplication.BatchSourceRefreshRequest{
		RequestID: command.RunID, WorkspaceID: command.WorkspaceID,
		Upserts: upserts, RemovedSourceIDs: append([]foundation.ID(nil), persisted.RemovedSourceIDs...),
	})
	if err != nil {
		return gitsyncapplication.ExternalChangeResult{}, err
	}
	if refreshed.Reindexed && !validID(refreshed.IndexVersionID) {
		return gitsyncapplication.ExternalChangeResult{}, captureError(foundation.ErrorConsistencyViolation, "GIT_CAPTURE_INDEX_RESULT_INVALID", false, "Git capture refresh returned an invalid index identity")
	}
	if err := capture.dependencies.Repository.CompleteGitCaptureBatch(ctx, command.WorkspaceID, command.RunID, command.BeforeCommit, command.AfterCommit); err != nil {
		return gitsyncapplication.ExternalChangeResult{}, err
	}
	return gitsyncapplication.ExternalChangeResult{
		IndexVersionID:  refreshed.IndexVersionID,
		NoIndexRequired: !refreshed.Reindexed,
	}, nil
}

func captureRequestHash(command gitsyncapplication.ExternalChangeCommand, changes []workspacedomain.CommittedTreeChange) (string, error) {
	encoded, err := json.Marshal(struct {
		WorkspaceID    foundation.ID                         `json:"workspace_id"`
		RunID          foundation.ID                         `json:"run_id"`
		BeforeCommit   string                                `json:"before_commit"`
		AfterCommit    string                                `json:"after_commit"`
		IdempotencyKey string                                `json:"idempotency_key"`
		Changes        []workspacedomain.CommittedTreeChange `json:"changes"`
	}{
		WorkspaceID: command.WorkspaceID, RunID: command.RunID, BeforeCommit: command.BeforeCommit,
		AfterCommit: command.AfterCommit, IdempotencyKey: command.IdempotencyKey, Changes: changes,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validCommand(command gitsyncapplication.ExternalChangeCommand) bool {
	return validID(command.WorkspaceID) && validID(command.RunID) && validOID(command.BeforeCommit) &&
		validOID(command.AfterCommit) && len(command.BeforeCommit) == len(command.AfterCommit) &&
		command.BeforeCommit != command.AfterCommit && validText(command.IdempotencyKey, 128)
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validOID(value string) bool {
	if len(value) != 40 && len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validText(value string, limit int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= limit && !strings.ContainsAny(value, "\r\n\x00")
}

func captureError(kind foundation.ErrorKind, code string, retryable bool, message string) error {
	return foundation.NewError(kind, code, retryable, errors.New(message))
}

var _ gitsyncapplication.ExternalChangeCapture = (*Capture)(nil)
