package workspacepostgres

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func classifyControl(err error, fallbackCode string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if gormWorkspaceNoRows(err) {
		return controlCorrupt(err)
	}
	switch platformpostgres.SQLState(err) {
	case "23505":
		switch platformpostgres.ConstraintName(err) {
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

func classify(err error, fallbackCode string) error {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if gormWorkspaceNoRows(err) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKSPACE_DATA_MISSING", false, err)
	}
	switch platformpostgres.SQLState(err) {
	case "23505":
		code := "WORKSPACE_CONFLICT"
		switch platformpostgres.ConstraintName(err) {
		case "workspace_root_path_key":
			code = "WORKSPACE_ROOT_EXISTS"
		case "uq_workspace_single_active":
			code = "ACTIVE_WORKSPACE_EXISTS"
		}
		return foundation.NewError(foundation.ErrorVersionConflict, code, false, err)
	case "23503":
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKSPACE_REFERENCE_INVALID", false, err)
	case "23514", "22P02":
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKSPACE_DATA_INVALID", false, err)
	case "40001", "40P01":
		return foundation.NewError(foundation.ErrorRetryableFailure, fallbackCode, true, err)
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, true, err)
}
