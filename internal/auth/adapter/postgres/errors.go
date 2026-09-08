package postgres

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

func readError(err error) error {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return unauthorized(errors.New("credential is missing, expired, or revoked"))
	}
	if platformpostgres.SQLState(err) == "23505" {
		return writeError(err)
	}
	return unavailable(fmt.Errorf("read authentication credential: %w", err))
}

func writeError(err error) error {
	if err == nil {
		return nil
	}
	if platformpostgres.SQLState(err) == "23505" {
		return foundation.NewError(foundation.ErrorVersionConflict, "AUTH_CREDENTIAL_CONFLICT", false, errors.New("credential identity already exists"))
	}
	return unavailable(fmt.Errorf("write authentication credential: %w", err))
}

func invalid(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, application.ErrorCodeInvalid, false, err)
}

func unauthorized(err error) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeUnauthorized, false, err)
}

func notFound(err error) error {
	return foundation.NewError(foundation.ErrorNotFound, application.ErrorCodeAPITokenNotFound, false, err)
}

func unavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, application.ErrorCodeUnavailable, true, err)
}

func corrupt(err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, application.ErrorCodeUnavailable, false, err)
}
