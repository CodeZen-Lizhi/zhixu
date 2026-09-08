package postgres

import (
	"context"
	"errors"
	"strings"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func requestInvalid(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, artifactapp.ErrorCodeRequestInvalid, false, err)
}

func notFound(err error) error {
	return foundation.NewError(foundation.ErrorNotFound, artifactapp.ErrorCodeNotFound, false, err)
}

func idempotencyConflict(err error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, artifactapp.ErrorCodeIdempotencyConflict, false, err)
}

func versionConflict(err error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, artifactapp.ErrorCodeVersionConflict, false, err)
}

func unavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, artifactapp.ErrorCodeDependencyUnavailable, true, err)
}

func inconsistent(err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, artifactapp.ErrorCodeResultInconsistent, false, err)
}

func validHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, artifactapp.ErrorCodeDependencyUnavailable, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return unavailable(err)
	}
	switch platformpostgres.SQLState(err) {
	case "23505":
		return idempotencyConflict(err)
	case "23503", "23514", "23502", "22P02":
		return inconsistent(err)
	case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01", "57014":
		return foundation.NewError(foundation.ErrorRetryableFailure, artifactapp.ErrorCodeDependencyUnavailable, true, err)
	case "55000":
		return inconsistent(err)
	}
	return unavailable(err)
}
