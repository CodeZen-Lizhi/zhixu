// Package domain defines the durable Quick Capture and document profile contracts.
package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	maxDisplayNameBytes = 512
	maxLocationBytes    = 4096
	maxURLBytes         = 8192
	maxErrorCodeBytes   = 128
)

// Kind identifies the immutable input category of a Capture.
type Kind string

const (
	// KindText is an inline plain-text capture.
	KindText Kind = "TEXT"
	// KindURL is a URL whose content is fetched asynchronously.
	KindURL Kind = "URL"
	// KindFile is an uploaded document.
	KindFile Kind = "FILE"
	// KindImage is an uploaded or pasted image.
	KindImage Kind = "IMAGE"
)

// Status is the aggregate-level Capture lifecycle.
type Status string

const (
	// StatusReceived means the immutable Capture and Source facts are durable.
	StatusReceived Status = "RECEIVED"
	// StatusSourceSaved means an immutable Source Version is available.
	StatusSourceSaved Status = "SOURCE_SAVED"
	// StatusFetching means URL materialization is in progress.
	StatusFetching Status = "FETCHING"
	// StatusProcessing means parsing or indexing is in progress.
	StatusProcessing Status = "PROCESSING"
	// StatusReady means the source is available to its supported retrieval paths.
	StatusReady Status = "READY"
	// StatusReadyDegraded means the original is safe but a derived capability is unavailable.
	StatusReadyDegraded Status = "READY_DEGRADED"
	// StatusFetchFailed means URL materialization failed while the Source remains durable.
	StatusFetchFailed Status = "FETCH_FAILED"
	// StatusProcessingFailed means a supported derived processing stage failed.
	StatusProcessingFailed Status = "PROCESSING_FAILED"
)

// StageStatus is the independently observable state of one processing layer.
type StageStatus string

const (
	// StagePending has not started yet.
	StagePending StageStatus = "PENDING"
	// StageRunning is currently executing.
	StageRunning StageStatus = "RUNNING"
	// StageReady has durable successful output.
	StageReady StageStatus = "READY"
	// StageFailed has a durable error and may be retried according to the attempt.
	StageFailed StageStatus = "FAILED"
	// StageCapabilityUnavailable records an explicitly unavailable optional capability.
	StageCapabilityUnavailable StageStatus = "CAPABILITY_UNAVAILABLE"
	// StageStale keeps the last successful derived result readable while its dependencies changed.
	StageStale StageStatus = "STALE"
	// StageNotApplicable means this layer does not apply to the Capture kind.
	StageNotApplicable StageStatus = "NOT_APPLICABLE"
)

// Capture is the durable orchestration record for one immutable user input.
type Capture struct {
	ID                    foundation.ID
	WorkspaceID           foundation.ID
	Kind                  Kind
	DisplayName           string
	OriginalLocation      string
	OriginalURL           string
	OriginalInputHash     string
	SourceID              foundation.ID
	LatestSourceVersionID foundation.ID
	Status                Status
	FetchStatus           StageStatus
	IngestionStatus       StageStatus
	IndexStatus           StageStatus
	ProfileStatus         StageStatus
	FailureStage          string
	ErrorCode             string
	Retryable             bool
	Version               int64
	CapturedAt            time.Time
	UpdatedAt             time.Time
}

// Validate checks persisted Capture invariants without consulting external state.
func (capture Capture) Validate() error {
	if !validID(capture.ID) || !validID(capture.WorkspaceID) || !validID(capture.SourceID) ||
		!capture.Kind.Valid() || !capture.Status.Valid() || !capture.FetchStatus.Valid() ||
		!capture.IngestionStatus.Valid() || !capture.IndexStatus.Valid() || !capture.ProfileStatus.Valid() ||
		!validRequiredText(capture.DisplayName, maxDisplayNameBytes) ||
		!validRequiredText(capture.OriginalLocation, maxLocationBytes) ||
		!validOptionalText(capture.OriginalURL, maxURLBytes) ||
		(capture.OriginalInputHash != "" && !validHash(capture.OriginalInputHash)) ||
		!validOptionalText(capture.FailureStage, maxErrorCodeBytes) ||
		!validOptionalText(capture.ErrorCode, maxErrorCodeBytes) ||
		capture.Version < 1 || capture.CapturedAt.IsZero() || capture.UpdatedAt.Before(capture.CapturedAt) {
		return invalid("capture fields are invalid")
	}
	if capture.Kind == KindURL {
		if capture.OriginalURL == "" || capture.OriginalInputHash != "" {
			return invalid("URL capture provenance is invalid")
		}
	} else if capture.OriginalURL != "" || !validHash(capture.OriginalInputHash) || !validID(capture.LatestSourceVersionID) {
		return invalid("materialized capture provenance is invalid")
	}
	if capture.LatestSourceVersionID == "" && capture.Kind != KindURL {
		return invalid("non-URL capture requires a source version")
	}
	if capture.LatestSourceVersionID == "" && capture.Status != StatusReceived && capture.Status != StatusFetching && capture.Status != StatusFetchFailed {
		return invalid("capture without a source version has an invalid status")
	}
	if capture.FailureStage == "" != (capture.ErrorCode == "") {
		return invalid("capture failure binding is incomplete")
	}
	if capture.ErrorCode == "" && capture.Retryable {
		return invalid("successful capture cannot be retryable")
	}
	return nil
}

// Valid reports whether a Capture kind is supported.
func (kind Kind) Valid() bool {
	switch kind {
	case KindText, KindURL, KindFile, KindImage:
		return true
	default:
		return false
	}
}

// Valid reports whether an aggregate Capture status is supported.
func (status Status) Valid() bool {
	switch status {
	case StatusReceived, StatusSourceSaved, StatusFetching, StatusProcessing, StatusReady,
		StatusReadyDegraded, StatusFetchFailed, StatusProcessingFailed:
		return true
	default:
		return false
	}
}

// Valid reports whether a processing layer status is supported.
func (status StageStatus) Valid() bool {
	switch status {
	case StagePending, StageRunning, StageReady, StageFailed, StageCapabilityUnavailable, StageStale, StageNotApplicable:
		return true
	default:
		return false
	}
}

func validID(id foundation.ID) bool {
	_, err := foundation.ParseID(string(id))
	return err == nil
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validRequiredText(value string, maxBytes int) bool {
	return value == strings.TrimSpace(value) && value != "" && validOptionalText(value, maxBytes)
}

func validOptionalText(value string, maxBytes int) bool {
	return utf8.ValidString(value) && len(value) <= maxBytes && !strings.ContainsAny(value, "\r\n\x00")
}

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "CAPTURE_INVALID", false, errors.New(message))
}
