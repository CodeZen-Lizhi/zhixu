package domain

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// AttemptStage identifies the last durable Capture processing checkpoint.
type AttemptStage string

const (
	// AttemptStageFetch materializes an HTTP response into a Source Version.
	AttemptStageFetch AttemptStage = "FETCH"
	// AttemptStageIngestion parses and chunks immutable source bytes.
	AttemptStageIngestion AttemptStage = "INGESTION"
	// AttemptStageIndex updates and activates the Workspace retrieval index.
	AttemptStageIndex AttemptStage = "INDEX"
	// AttemptStageProfile creates a derived document knowledge profile.
	AttemptStageProfile AttemptStage = "PROFILE"
	// AttemptStageComplete is the successful terminal checkpoint.
	AttemptStageComplete AttemptStage = "COMPLETE"
)

// AttemptStatus is the durable result of one Workflow node attempt.
type AttemptStatus string

const (
	// AttemptStatusRunning means the Workflow attempt still owns processing.
	AttemptStatusRunning AttemptStatus = "RUNNING"
	// AttemptStatusSucceeded means all supported stages reached a durable terminal state.
	AttemptStatusSucceeded AttemptStatus = "SUCCEEDED"
	// AttemptStatusFailed means processing stopped with a stable failure.
	AttemptStatusFailed AttemptStatus = "FAILED"
	// AttemptStatusCancelled means the Workflow attempt was explicitly cancelled.
	AttemptStatusCancelled AttemptStatus = "CANCELLED"
)

// ProcessingAttempt records orchestration checkpoints without copying downstream facts.
type ProcessingAttempt struct {
	ID                 foundation.ID
	WorkspaceID        foundation.ID
	CaptureID          foundation.ID
	SourceVersionID    foundation.ID
	WorkflowRunID      foundation.ID
	IngestionAttemptID foundation.ID
	IndexVersionID     foundation.ID
	ProfileID          foundation.ID
	AttemptNumber      int
	Stage              AttemptStage
	Status             AttemptStatus
	ErrorCode          string
	Retryable          bool
	StartedAt          time.Time
	CompletedAt        *time.Time
	Version            int64
}

// Validate checks persisted Capture processing attempt invariants.
func (attempt ProcessingAttempt) Validate() error {
	if !validID(attempt.ID) || !validID(attempt.WorkspaceID) || !validID(attempt.CaptureID) ||
		!validID(attempt.WorkflowRunID) || !attempt.Stage.Valid() || !attempt.Status.Valid() ||
		attempt.AttemptNumber < 1 || attempt.Version < 1 || attempt.StartedAt.IsZero() ||
		!validOptionalText(attempt.ErrorCode, maxErrorCodeBytes) {
		return invalidAttempt("capture processing attempt fields are invalid")
	}
	for _, optional := range []foundation.ID{attempt.SourceVersionID, attempt.IngestionAttemptID, attempt.IndexVersionID, attempt.ProfileID} {
		if optional != "" && !validID(optional) {
			return invalidAttempt("capture processing attempt binding is invalid")
		}
	}
	terminal := attempt.Status != AttemptStatusRunning
	if terminal != (attempt.CompletedAt != nil) || (attempt.CompletedAt != nil && attempt.CompletedAt.Before(attempt.StartedAt)) {
		return invalidAttempt("capture processing attempt completion is invalid")
	}
	if attempt.Status == AttemptStatusRunning || attempt.Status == AttemptStatusSucceeded {
		if attempt.ErrorCode != "" || attempt.Retryable {
			return invalidAttempt("successful capture processing attempt has a failure")
		}
	} else if attempt.ErrorCode == "" {
		return invalidAttempt("failed capture processing attempt requires an error")
	}
	return nil
}

// Valid reports whether a processing checkpoint is supported.
func (stage AttemptStage) Valid() bool {
	switch stage {
	case AttemptStageFetch, AttemptStageIngestion, AttemptStageIndex, AttemptStageProfile, AttemptStageComplete:
		return true
	default:
		return false
	}
}

// Valid reports whether a processing attempt status is supported.
func (status AttemptStatus) Valid() bool {
	switch status {
	case AttemptStatusRunning, AttemptStatusSucceeded, AttemptStatusFailed, AttemptStatusCancelled:
		return true
	default:
		return false
	}
}

func invalidAttempt(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "CAPTURE_ATTEMPT_INVALID", false, errors.New(message))
}
