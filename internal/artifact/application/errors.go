package application

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	ErrorCodeRequestInvalid        = "ARTIFACT_REQUEST_INVALID"
	ErrorCodeDependencyUnavailable = "ARTIFACT_DEPENDENCY_UNAVAILABLE"
	ErrorCodeNotFound              = "ARTIFACT_NOT_FOUND"
	ErrorCodeIdempotencyConflict   = "ARTIFACT_IDEMPOTENCY_CONFLICT"
	ErrorCodeVersionConflict       = "ARTIFACT_VERSION_CONFLICT"
	ErrorCodeResultInconsistent    = "ARTIFACT_RESULT_INCONSISTENT"
	ErrorCodeEvidenceInvalid       = "ARTIFACT_EVIDENCE_INVALID"
	ErrorCodeExportInvalid         = "ARTIFACT_EXPORT_INVALID"
)

func requestInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeRequestInvalid, false, errors.New(message))
}

func unavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDependencyUnavailable, true, errors.New(message))
}

func resultInconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New(message))
}

func evidenceInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeEvidenceInvalid, false, errors.New(message))
}

func versionConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeVersionConflict, false, errors.New(message))
}
