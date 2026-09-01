package localmodelruntime

import (
	"context"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

func (store *gormTxStore) SeedActivationPreparation(ctx context.Context, command ActivationPreparationCommand) (ActivationPreparation, error) {
	database, err := store.databaseFor(ctx)
	if err != nil {
		return ActivationPreparation{}, err
	}
	return seedGORMActivationPreparation(ctx, database, command)
}

func (store *gormTxStore) ReadOperationByRollout(ctx context.Context, rolloutID foundation.ID) (OperationRecord, error) {
	database, err := store.databaseFor(ctx)
	if err != nil || !validID(rolloutID) {
		if err == nil {
			err = errors.New("local model runtime rollout operation read is invalid")
		}
		return OperationRecord{}, err
	}
	return readGORMActivationOperation(ctx, database, `rollout_id=?::uuid`, string(rolloutID))
}

func (store *gormTxStore) ReadOperationByTarget(ctx context.Context, targetRevision int64) (OperationRecord, error) {
	database, err := store.databaseFor(ctx)
	if err != nil || targetRevision < 0 {
		if err == nil {
			err = errors.New("local model runtime target operation read is invalid")
		}
		return OperationRecord{}, err
	}
	return readGORMActivationOperation(ctx, database, `target_revision=?`, targetRevision)
}

func (store *gormTxStore) CompleteActivationPreparation(ctx context.Context, rolloutID foundation.ID, errorCode string, retryable bool) (OperationRecord, error) {
	database, err := store.databaseFor(ctx)
	if err != nil || !validID(rolloutID) {
		if err == nil {
			err = errors.New("local model runtime activation completion is invalid")
		}
		return OperationRecord{}, err
	}
	return completeGORMActivationPreparation(ctx, database, rolloutID, errorCode, retryable)
}

func seedGORMActivationPreparation(ctx context.Context, database *gorm.DB, command ActivationPreparationCommand) (ActivationPreparation, error) {
	if err := validateActivationPreparation(command); err != nil {
		return ActivationPreparation{}, err
	}
	models := marshalModelRefs(command.Requirement.Models)
	result := database.WithContext(ctx).Exec(`INSERT INTO ops.managed_ollama_operations(operation_id,kind,idempotency_key,request_hash,target_revision,rollout_id,requirement_hash,required_models,phase)
		VALUES(?::uuid,'activation',?,?,?,?::uuid,?,?::jsonb,'queued') ON CONFLICT(kind,idempotency_key) DO NOTHING`,
		string(command.OperationID), command.IdempotencyKey, command.RequestHash, command.TargetRevision, string(command.RolloutID), command.Requirement.Hash, string(models))
	if result.Error != nil {
		return ActivationPreparation{}, gormStoreError(ctx, result.Error)
	}
	operation, err := readGORMActivationOperation(ctx, database, `idempotency_key=?`, command.IdempotencyKey)
	if err != nil {
		return ActivationPreparation{}, err
	}
	if operation.RequestHash != command.RequestHash || operation.Requirement.Hash != command.Requirement.Hash || operation.TargetRevision == nil || *operation.TargetRevision != command.TargetRevision || operation.RolloutID == nil || *operation.RolloutID != command.RolloutID {
		return ActivationPreparation{}, conflict("local model runtime activation idempotency key is bound to a different request")
	}
	hold, err := readGORMHoldByOperation(ctx, database, operation.OperationID)
	if gormNoRows(err) {
		result = database.WithContext(ctx).Exec(`INSERT INTO ops.managed_ollama_holds(hold_id,owner_kind,owner_id,owner_epoch,revision,rollout_id,operation_id,requirement_hash,models,lease_expires_at)
			VALUES(?::uuid,'preparation',?::uuid,?,?,?::uuid,?::uuid,?,?::jsonb,clock_timestamp()+?::interval) ON CONFLICT(hold_id) DO NOTHING`,
			string(command.HoldID), string(command.OwnerID), command.OwnerEpoch, command.TargetRevision, string(command.RolloutID), string(operation.OperationID), command.Requirement.Hash, string(models), intervalArg(command.LeaseDuration))
		if result.Error != nil {
			return ActivationPreparation{}, gormStoreError(ctx, result.Error)
		}
		hold, err = readGORMHoldByOperation(ctx, database, operation.OperationID)
	}
	if err != nil {
		return ActivationPreparation{}, gormStoreError(ctx, err)
	}
	if hold.HoldID != command.HoldID || hold.OwnerID != command.OwnerID || hold.OwnerEpoch != command.OwnerEpoch || hold.Requirement.Hash != command.Requirement.Hash || hold.RolloutID == nil || *hold.RolloutID != command.RolloutID || hold.OperationID == nil || *hold.OperationID != operation.OperationID {
		return ActivationPreparation{}, conflict("local model runtime preparation hold identity conflict")
	}
	return ActivationPreparation{Operation: operation, Hold: hold}, nil
}

func readGORMActivationOperation(ctx context.Context, database *gorm.DB, predicate string, arg any) (OperationRecord, error) {
	row, err := gormRawRow(ctx, database, `SELECT `+operationColumns+` FROM ops.managed_ollama_operations WHERE kind='activation' AND `+predicate+` ORDER BY created_at DESC LIMIT 1`, arg)
	if err != nil {
		return OperationRecord{}, gormStoreError(ctx, err)
	}
	operation, err := scanOperation(row)
	return operation, gormStoreError(ctx, err)
}

func readGORMHoldByOperation(ctx context.Context, database *gorm.DB, operationID foundation.ID) (HoldRecord, error) {
	row, err := gormRawRow(ctx, database, `SELECT `+holdColumns+` FROM ops.managed_ollama_holds WHERE operation_id=?::uuid ORDER BY created_at LIMIT 1`, string(operationID))
	if err != nil {
		return HoldRecord{}, gormStoreError(ctx, err)
	}
	hold, err := scanHold(row)
	return hold, gormStoreError(ctx, err)
}

func completeGORMActivationPreparation(ctx context.Context, database *gorm.DB, rolloutID foundation.ID, errorCode string, retryable bool) (OperationRecord, error) {
	row, err := gormRawRow(ctx, database, `SELECT `+operationColumns+` FROM ops.managed_ollama_operations WHERE kind='activation' AND rollout_id=?::uuid FOR UPDATE`, string(rolloutID))
	if err != nil {
		return OperationRecord{}, gormStoreError(ctx, err)
	}
	operation, err := scanOperation(row)
	if err != nil {
		return OperationRecord{}, gormStoreError(ctx, err)
	}
	success := strings.TrimSpace(errorCode) == ""
	if operation.TerminalAt == nil {
		phase := OperationPhaseFailed
		if success {
			if operation.Phase != OperationPhaseReady {
				return OperationRecord{}, conflict("local model activation preparation is not ready")
			}
			phase = OperationPhaseSucceeded
		} else if operation.Phase == OperationPhaseQueued {
			result := database.WithContext(ctx).Exec(`UPDATE ops.managed_ollama_operations SET phase='starting',version=version+1,updated_at=clock_timestamp() WHERE operation_id=?::uuid`, string(operation.OperationID))
			if result.Error != nil {
				return OperationRecord{}, gormStoreError(ctx, result.Error)
			}
		}
		row, err = gormRawRow(ctx, database, `UPDATE ops.managed_ollama_operations SET phase=?,error_code=?,error_retryable=?,terminal_at=clock_timestamp(),claim_owner_id=NULL,claim_owner_epoch=NULL,claim_expires_at=NULL,version=version+1,updated_at=clock_timestamp()
			WHERE operation_id=?::uuid AND terminal_at IS NULL RETURNING `+operationColumns,
			string(phase), nullableErrorCode(errorCode), retryable, string(operation.OperationID))
		if err != nil {
			return OperationRecord{}, gormStoreError(ctx, err)
		}
		operation, err = scanOperation(row)
		if err != nil {
			return OperationRecord{}, gormStoreError(ctx, err)
		}
	}
	result := database.WithContext(ctx).Exec(`UPDATE ops.managed_ollama_holds SET released_at=COALESCE(released_at,clock_timestamp()),version=CASE WHEN released_at IS NULL THEN version+1 ELSE version END,updated_at=clock_timestamp()
		WHERE operation_id=?::uuid AND released_at IS NULL`, string(operation.OperationID))
	if result.Error != nil {
		return OperationRecord{}, gormStoreError(ctx, result.Error)
	}
	return operation, nil
}
