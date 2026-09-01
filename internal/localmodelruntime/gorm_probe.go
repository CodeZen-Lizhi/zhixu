package localmodelruntime

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

func (store *GORMStore) SeedTestPreparation(ctx context.Context, command TestPreparationCommand) (preparation TestPreparation, err error) {
	if err = store.ready(ctx); err != nil {
		return TestPreparation{}, err
	}
	err = store.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		database, unwrapErr := platformpostgres.GORMTransaction(scope)
		if unwrapErr != nil {
			return unwrapErr
		}
		preparation, err = seedGORMTestPreparation(callbackCtx, database, command)
		return err
	})
	if err != nil {
		return TestPreparation{}, gormStoreError(ctx, err)
	}
	return preparation, nil
}

func seedGORMTestPreparation(ctx context.Context, database *gorm.DB, command TestPreparationCommand) (TestPreparation, error) {
	if err := validateTestPreparation(command); err != nil {
		return TestPreparation{}, err
	}
	models := marshalModelRefs(command.Requirement.Models)
	result := database.WithContext(ctx).Exec(`INSERT INTO ops.managed_ollama_operations(operation_id,kind,idempotency_key,request_hash,target_revision,requirement_hash,required_models,phase)
		VALUES(?::uuid,'test',?,?,?::bigint,?,?::jsonb,'queued') ON CONFLICT(kind,idempotency_key) DO NOTHING`,
		string(command.OperationID), command.IdempotencyKey, command.RequestHash, nullableInt64(command.TargetRevision), command.Requirement.Hash, string(models))
	if result.Error != nil {
		return TestPreparation{}, gormStoreError(ctx, result.Error)
	}
	row, err := gormRawRow(ctx, database, `SELECT `+operationColumns+` FROM ops.managed_ollama_operations WHERE kind='test' AND idempotency_key=? ORDER BY created_at DESC LIMIT 1`, command.IdempotencyKey)
	if err != nil {
		return TestPreparation{}, gormStoreError(ctx, err)
	}
	operation, err := scanOperation(row)
	if err != nil {
		return TestPreparation{}, gormStoreError(ctx, err)
	}
	if operation.RequestHash != command.RequestHash || operation.Requirement.Hash != command.Requirement.Hash || !equalOptionalInt64(operation.TargetRevision, command.TargetRevision) {
		return TestPreparation{}, conflict("local model runtime test idempotency key is bound to a different request")
	}
	hold, err := readGORMHoldByOperation(ctx, database, operation.OperationID)
	if gormNoRows(err) {
		result = database.WithContext(ctx).Exec(`INSERT INTO ops.managed_ollama_holds(hold_id,owner_kind,owner_id,owner_epoch,role,instance_id,revision,operation_id,requirement_hash,models,lease_expires_at)
			VALUES(?::uuid,'test',?::uuid,?,'api',?::uuid,?,?::uuid,?,?::jsonb,clock_timestamp()+?::interval) ON CONFLICT(hold_id) DO NOTHING`,
			string(command.HoldID), string(command.OwnerID), command.OwnerEpoch, string(command.OwnerID), nullableInt64(command.TargetRevision), string(operation.OperationID), command.Requirement.Hash, string(models), intervalArg(command.LeaseDuration))
		if result.Error != nil {
			return TestPreparation{}, gormStoreError(ctx, result.Error)
		}
		hold, err = readGORMHoldByOperation(ctx, database, operation.OperationID)
	}
	if err != nil {
		return TestPreparation{}, gormStoreError(ctx, err)
	}
	if hold.HoldID != command.HoldID || hold.OwnerID != command.OwnerID || hold.OwnerEpoch != command.OwnerEpoch || hold.Requirement.Hash != command.Requirement.Hash || hold.OperationID == nil || *hold.OperationID != operation.OperationID {
		return TestPreparation{}, conflict("local model runtime test hold identity conflict")
	}
	return TestPreparation{Operation: operation, Hold: hold}, nil
}

func (store *GORMStore) ReadTestOperation(ctx context.Context, operationID foundation.ID) (OperationRecord, error) {
	if err := store.ready(ctx); err != nil {
		return OperationRecord{}, err
	}
	if !validID(operationID) {
		return OperationRecord{}, errors.New("local model runtime test operation read is invalid")
	}
	row, err := gormRawRow(ctx, store.database, `SELECT `+operationColumns+` FROM ops.managed_ollama_operations WHERE kind='test' AND operation_id=?::uuid`, string(operationID))
	if err != nil {
		return OperationRecord{}, gormStoreError(ctx, err)
	}
	operation, err := scanOperation(row)
	return operation, gormStoreError(ctx, err)
}

func (store *GORMStore) ClaimTestProbe(ctx context.Context, command TestProbeClaimCommand) (operation OperationRecord, owned bool, err error) {
	if err = store.ready(ctx); err != nil {
		return OperationRecord{}, false, err
	}
	if err = validateTestProbeClaim(command); err != nil {
		return OperationRecord{}, false, err
	}
	err = store.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		database, unwrapErr := platformpostgres.GORMTransaction(scope)
		if unwrapErr != nil {
			return unwrapErr
		}
		row, queryErr := gormRawRow(callbackCtx, database, `UPDATE ops.managed_ollama_operations
			SET phase='probing',claim_owner_id=?::uuid,claim_owner_epoch=?,claim_expires_at=clock_timestamp()+?::interval,
			version=version+1,updated_at=clock_timestamp()
			WHERE kind='test' AND operation_id=?::uuid AND version=? AND terminal_at IS NULL
			  AND (phase='ready' OR (phase='probing' AND claim_expires_at<=clock_timestamp()))
			RETURNING `+operationColumns,
			string(command.OwnerID), command.OwnerEpoch, intervalArg(command.LeaseDuration), string(command.OperationID), command.ExpectedVersion)
		if queryErr == nil {
			operation, err = scanOperation(row)
			if err == nil {
				owned = true
				return nil
			}
			if !gormNoRows(err) {
				return err
			}
		} else if !gormNoRows(queryErr) {
			return queryErr
		}
		row, queryErr = gormRawRow(callbackCtx, database, `SELECT `+operationColumns+` FROM ops.managed_ollama_operations WHERE kind='test' AND operation_id=?::uuid FOR UPDATE`, string(command.OperationID))
		if queryErr != nil {
			return queryErr
		}
		operation, err = scanOperation(row)
		if err != nil {
			return err
		}
		if operation.Phase == OperationPhaseProbing && operation.ClaimOwnerID != nil && *operation.ClaimOwnerID == command.OwnerID && operation.ClaimOwnerEpoch == command.OwnerEpoch {
			row, queryErr = gormRawRow(callbackCtx, database, `SELECT claim_expires_at>clock_timestamp() FROM ops.managed_ollama_operations WHERE operation_id=?::uuid`, string(command.OperationID))
			if queryErr != nil {
				return queryErr
			}
			if queryErr = row.Scan(&owned); queryErr != nil {
				return queryErr
			}
		}
		return nil
	})
	if err != nil {
		return OperationRecord{}, false, gormStoreError(ctx, err)
	}
	return operation, owned, nil
}

func (store *GORMStore) CompleteTestProbe(ctx context.Context, command TestProbeCompletionCommand) (operation OperationRecord, err error) {
	if err = store.ready(ctx); err != nil {
		return OperationRecord{}, err
	}
	if err = validateTestProbeCompletion(command); err != nil {
		return OperationRecord{}, err
	}
	err = store.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		database, unwrapErr := platformpostgres.GORMTransaction(scope)
		if unwrapErr != nil {
			return unwrapErr
		}
		phase := OperationPhaseReady
		terminal := !command.Abandon
		if !command.Abandon {
			phase = OperationPhaseSucceeded
			if command.ErrorCode != "" {
				phase = OperationPhaseFailed
			}
		}
		row, queryErr := gormRawRow(callbackCtx, database, `UPDATE ops.managed_ollama_operations
			SET phase=?,error_code=?,error_retryable=?,terminal_at=CASE WHEN ? THEN clock_timestamp() ELSE NULL END,
			claim_owner_id=NULL,claim_owner_epoch=NULL,claim_expires_at=NULL,version=version+1,updated_at=clock_timestamp()
			WHERE kind='test' AND operation_id=?::uuid AND phase='probing' AND claim_owner_id=?::uuid AND claim_owner_epoch=? AND version=?
			  AND claim_expires_at>clock_timestamp() AND terminal_at IS NULL RETURNING `+operationColumns,
			string(phase), nullableErrorCode(command.ErrorCode), command.Retryable, terminal, string(command.OperationID), string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion)
		if queryErr != nil {
			if gormNoRows(queryErr) {
				return conflict("local model runtime test probe completion CAS failed")
			}
			return queryErr
		}
		operation, queryErr = scanOperation(row)
		if queryErr != nil {
			if gormNoRows(queryErr) {
				return conflict("local model runtime test probe completion CAS failed")
			}
			return queryErr
		}
		if !command.Abandon {
			result := database.WithContext(callbackCtx).Exec(`UPDATE ops.managed_ollama_holds SET released_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
				WHERE owner_kind='test' AND operation_id=?::uuid AND released_at IS NULL`, string(command.OperationID))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return conflict("local model runtime test probe hold release failed")
			}
		}
		return nil
	})
	if err != nil {
		return OperationRecord{}, gormStoreError(ctx, err)
	}
	return operation, nil
}

// CompleteTestPreparation is a compatibility helper retained for existing
// callers. It deliberately enters the same fenced probe path as legacy code.
func (store *GORMStore) CompleteTestPreparation(ctx context.Context, operationID foundation.ID, errorCode string, retryable bool) (OperationRecord, error) {
	operation, err := store.ReadTestOperation(ctx, operationID)
	if err != nil || operation.TerminalAt != nil {
		return operation, err
	}
	if !validID(operationID) {
		return OperationRecord{}, errors.New("local model runtime test preparation operation is invalid")
	}
	row, err := gormRawRow(ctx, store.database, `SELECT owner_id::text FROM ops.managed_ollama_holds WHERE owner_kind='test' AND operation_id=?::uuid`, string(operationID))
	if err != nil {
		return OperationRecord{}, gormStoreError(ctx, err)
	}
	var rawHoldOwner string
	if err := row.Scan(&rawHoldOwner); err != nil {
		return OperationRecord{}, gormStoreError(ctx, err)
	}
	holdOwner, err := foundation.ParseID(rawHoldOwner)
	if err != nil {
		return OperationRecord{}, err
	}
	var ownerID foundation.ID
	for range 4 {
		ownerID, err = foundation.NewUUIDGenerator(nil).New()
		if err != nil {
			return OperationRecord{}, err
		}
		if ownerID != holdOwner {
			break
		}
	}
	if ownerID == holdOwner {
		return OperationRecord{}, errors.New("local model runtime test probe owner identity is unavailable")
	}
	claimed, owned, err := store.ClaimTestProbe(ctx, TestProbeClaimCommand{OperationID: operationID, OwnerID: ownerID, OwnerEpoch: 1, ExpectedVersion: operation.Version, LeaseDuration: 10 * time.Minute})
	if err != nil {
		return OperationRecord{}, err
	}
	if !owned {
		if claimed.TerminalAt != nil {
			return claimed, nil
		}
		return OperationRecord{}, conflict("local model runtime test probe is already owned")
	}
	return store.CompleteTestProbe(ctx, TestProbeCompletionCommand{OperationID: operationID, OwnerID: ownerID, OwnerEpoch: 1, ExpectedVersion: claimed.Version, ErrorCode: errorCode, Retryable: retryable})
}
