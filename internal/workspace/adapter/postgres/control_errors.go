package workspacepostgres

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func classifyControl(err error, fallbackCode string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return controlCorrupt(err)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			switch postgresError.ConstraintName {
			case "workspace_root_path_key", "uq_workspace_root_fingerprint":
				return identityConflict(err)
			case "workspace_switch_idempotency":
				return switchIdempotencyConflict(err)
			case "uq_workspace_switch_inflight":
				return switchInProgress(err)
			case "uq_workspace_single_active":
				return controlStateConflict(err)
			default:
				return controlStateConflict(err)
			}
		case "55P03":
			return runtimeMutationConflict(err)
		case "40001", "40P01":
			return foundation.NewError(foundation.ErrorRetryableFailure, fallbackCode, true, err)
		case "23514", "22P02":
			return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeRegistryInvalid, false, err)
		case "23503", "55000":
			return controlCorrupt(err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, true, err)
}

func registryNotFound(cause error) error {
	return foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeWorkspaceNotFound, false, cause)
}

func switchNotFound(cause error) error {
	return foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeSwitchNotFound, false, cause)
}

func migrationRequired(cause error) error {
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, domain.ErrorCodeMigrationRequired, false, cause)
}

func identityConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeIdentityConflict, false, cause)
}

func rebindConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeBindingRebindConflict, false, cause)
}

func activeRemoveConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeActiveRemoveConflict, false, cause)
}

func workspaceVersionConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeWorkspaceVersionConflict, false, cause)
}

func switchInProgress(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeSwitchInProgress, false, cause)
}

func switchIdempotencyConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeSwitchIdempotencyConflict, false, cause)
}

func controlStateConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeControlStateConflict, false, cause)
}

func switchPhaseConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeSwitchPhaseConflict, false, cause)
}

func switchLeaseHeld(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeSwitchLeaseHeld, true, cause)
}

func switchLeaseExpired(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeSwitchLeaseExpired, true, cause)
}

func runtimeConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRuntimeConflict, false, cause)
}

func runtimeNotPrepared(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRuntimeNotPrepared, true, cause)
}

func runtimeMutationConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRuntimeMutationConflict, true, cause)
}

func controlCorrupt(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeControlCorrupt, false, cause)
}

func controlUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeControlDatabaseUnavailable, true, cause)
}
