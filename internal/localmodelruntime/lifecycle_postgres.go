package localmodelruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

// DB is the minimal pgx contract used by the lifecycle adapter.
type DB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// PostgresStore implements LifecycleStore over the managed lifecycle tables.
// It owns no process/network side effects.
type PostgresStore struct{ db DB }

type txStore struct{ tx pgx.Tx }

// NewPostgresStore validates the database dependency without opening a
// connection or changing schema.
func NewPostgresStore(db DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("local model runtime database is nil")
	}
	return &PostgresStore{db: db}, nil
}

var _ LifecycleStore = (*PostgresStore)(nil)
var _ TxStore = (*txStore)(nil)
var _ TestPreparationStore = (*PostgresStore)(nil)

func (store *txStore) SeedActivationPreparation(ctx context.Context, command ActivationPreparationCommand) (ActivationPreparation, error) {
	if store == nil || ctx == nil || store.tx == nil {
		return ActivationPreparation{}, errors.New("local model runtime activation seed transaction is nil")
	}
	return seedActivationPreparation(ctx, store.tx, command)
}

func (store *txStore) ReadOperationByRollout(ctx context.Context, rolloutID foundation.ID) (OperationRecord, error) {
	if store == nil || ctx == nil || store.tx == nil || !validID(rolloutID) {
		return OperationRecord{}, errors.New("local model runtime rollout operation read is invalid")
	}
	return readActivationOperation(ctx, store.tx, `rollout_id=$1::uuid`, string(rolloutID))
}

func (store *txStore) ReadOperationByTarget(ctx context.Context, targetRevision int64) (OperationRecord, error) {
	if store == nil || ctx == nil || store.tx == nil || targetRevision < 0 {
		return OperationRecord{}, errors.New("local model runtime target operation read is invalid")
	}
	return readActivationOperation(ctx, store.tx, `target_revision=$1`, targetRevision)
}

func (store *txStore) CompleteActivationPreparation(ctx context.Context, rolloutID foundation.ID, errorCode string, retryable bool) (OperationRecord, error) {
	if store == nil || ctx == nil || store.tx == nil || !validID(rolloutID) {
		return OperationRecord{}, errors.New("local model runtime activation completion is invalid")
	}
	return completeActivationPreparation(ctx, store.tx, rolloutID, errorCode, retryable)
}

func (store *PostgresStore) SeedActivationPreparation(ctx context.Context, tx pgx.Tx, command ActivationPreparationCommand) (ActivationPreparation, error) {
	if ctx == nil || tx == nil {
		return ActivationPreparation{}, errors.New("local model runtime activation seed transaction is nil")
	}
	return seedActivationPreparation(ctx, tx, command)
}
func (store *PostgresStore) ReadOperationByRollout(ctx context.Context, tx pgx.Tx, rolloutID foundation.ID) (OperationRecord, error) {
	if ctx == nil || tx == nil || !validID(rolloutID) {
		return OperationRecord{}, errors.New("local model runtime rollout operation read is invalid")
	}
	return readActivationOperation(ctx, tx, `rollout_id=$1::uuid`, string(rolloutID))
}
func (store *PostgresStore) ReadOperationByTarget(ctx context.Context, tx pgx.Tx, targetRevision int64) (OperationRecord, error) {
	if ctx == nil || tx == nil || targetRevision < 0 {
		return OperationRecord{}, errors.New("local model runtime target operation read is invalid")
	}
	return readActivationOperation(ctx, tx, `target_revision=$1`, targetRevision)
}
func (store *PostgresStore) CompleteActivationPreparation(ctx context.Context, tx pgx.Tx, rolloutID foundation.ID, errorCode string, retryable bool) (OperationRecord, error) {
	if ctx == nil || tx == nil || !validID(rolloutID) {
		return OperationRecord{}, errors.New("local model runtime activation completion is invalid")
	}
	return completeActivationPreparation(ctx, tx, rolloutID, errorCode, retryable)
}

// SeedTestPreparation atomically creates or replays a queued test operation
// and its model-demand hold. The method owns a short transaction because the
// API test request does not otherwise have a model-settings transaction.
func (store *PostgresStore) SeedTestPreparation(ctx context.Context, command TestPreparationCommand) (TestPreparation, error) {
	if ctx == nil {
		return TestPreparation{}, errors.New("local model runtime test seed context is nil")
	}
	tx, err := store.db.Begin(ctx)
	if err != nil {
		return TestPreparation{}, err
	}
	defer rollback(ctx, tx)
	preparation, err := seedTestPreparation(ctx, tx, command)
	if err != nil {
		return TestPreparation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TestPreparation{}, err
	}
	return preparation, nil
}

// ReadTestOperation returns one exact durable test operation.
func (store *PostgresStore) ReadTestOperation(ctx context.Context, operationID foundation.ID) (OperationRecord, error) {
	if ctx == nil || !validID(operationID) {
		return OperationRecord{}, errors.New("local model runtime test operation read is invalid")
	}
	return scanOperation(store.db.QueryRow(ctx, `SELECT `+operationColumns+` FROM ops.managed_ollama_operations WHERE kind='test' AND operation_id=$1::uuid`, string(operationID)))
}

// ClaimTestProbe atomically assigns a ready production probe to one API
// request. A fresh probing lease is joined without changing ownership; an
// expired lease can be reclaimed using PostgreSQL time.
func (store *PostgresStore) ClaimTestProbe(ctx context.Context, command TestProbeClaimCommand) (OperationRecord, bool, error) {
	if ctx == nil {
		return OperationRecord{}, false, errors.New("local model runtime test probe claim context is nil")
	}
	if err := validateTestProbeClaim(command); err != nil {
		return OperationRecord{}, false, err
	}
	tx, err := store.db.Begin(ctx)
	if err != nil {
		return OperationRecord{}, false, err
	}
	defer rollback(ctx, tx)
	row := tx.QueryRow(ctx, `
UPDATE ops.managed_ollama_operations
SET phase='probing', claim_owner_id=$2::uuid, claim_owner_epoch=$3,
    claim_expires_at=clock_timestamp()+$5::interval,
    version=version+1, updated_at=clock_timestamp()
WHERE kind='test' AND operation_id=$1::uuid AND version=$4 AND terminal_at IS NULL
  AND (phase='ready' OR (phase='probing' AND claim_expires_at<=clock_timestamp()))
RETURNING `+operationColumns,
		string(command.OperationID), string(command.OwnerID), command.OwnerEpoch,
		command.ExpectedVersion, intervalArg(command.LeaseDuration))
	operation, err := scanOperation(row)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return OperationRecord{}, false, err
		}
		return operation, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return OperationRecord{}, false, err
	}
	operation, err = scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM ops.managed_ollama_operations WHERE kind='test' AND operation_id=$1::uuid FOR UPDATE`, string(command.OperationID)))
	if err != nil {
		return OperationRecord{}, false, err
	}
	owned := false
	if operation.Phase == OperationPhaseProbing && operation.ClaimOwnerID != nil &&
		*operation.ClaimOwnerID == command.OwnerID && operation.ClaimOwnerEpoch == command.OwnerEpoch {
		if err := tx.QueryRow(ctx, `SELECT claim_expires_at>clock_timestamp() FROM ops.managed_ollama_operations WHERE operation_id=$1::uuid`, string(command.OperationID)).Scan(&owned); err != nil {
			return OperationRecord{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return OperationRecord{}, false, err
	}
	return operation, owned, nil
}

// CompleteTestProbe closes or abandons one exact production-probe lease. A
// terminal completion releases the operation hold in the same transaction;
// abandoning returns the operation to ready and deliberately keeps the hold.
func (store *PostgresStore) CompleteTestProbe(ctx context.Context, command TestProbeCompletionCommand) (OperationRecord, error) {
	if ctx == nil {
		return OperationRecord{}, errors.New("local model runtime test probe completion context is nil")
	}
	if err := validateTestProbeCompletion(command); err != nil {
		return OperationRecord{}, err
	}
	tx, err := store.db.Begin(ctx)
	if err != nil {
		return OperationRecord{}, err
	}
	defer rollback(ctx, tx)

	phase := OperationPhaseReady
	terminal := !command.Abandon
	if !command.Abandon {
		phase = OperationPhaseSucceeded
		if command.ErrorCode != "" {
			phase = OperationPhaseFailed
		}
	}
	row := tx.QueryRow(ctx, `
UPDATE ops.managed_ollama_operations
SET phase=$5, error_code=$6, error_retryable=$7,
    terminal_at=CASE WHEN $8 THEN clock_timestamp() ELSE NULL END,
    claim_owner_id=NULL, claim_owner_epoch=NULL, claim_expires_at=NULL,
    version=version+1, updated_at=clock_timestamp()
WHERE kind='test' AND operation_id=$1::uuid AND phase='probing'
  AND claim_owner_id=$2::uuid AND claim_owner_epoch=$3 AND version=$4
  AND claim_expires_at>clock_timestamp() AND terminal_at IS NULL
RETURNING `+operationColumns,
		string(command.OperationID), string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion,
		string(phase), nullableErrorCode(command.ErrorCode), command.Retryable, terminal)
	operation, err := scanOperation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperationRecord{}, conflict("local model runtime test probe completion CAS failed")
	}
	if err != nil {
		return OperationRecord{}, err
	}
	if !command.Abandon {
		tag, releaseErr := tx.Exec(ctx, `
UPDATE ops.managed_ollama_holds
SET released_at=clock_timestamp(), version=version+1, updated_at=clock_timestamp()
WHERE owner_kind='test' AND operation_id=$1::uuid AND released_at IS NULL`, string(command.OperationID))
		if releaseErr != nil {
			return OperationRecord{}, releaseErr
		}
		if tag.RowsAffected() != 1 {
			return OperationRecord{}, conflict("local model runtime test probe hold release failed")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return OperationRecord{}, err
	}
	return operation, nil
}

// CompleteTestPreparation is retained for migration compatibility. It still
// enters the production-probe state through a random, fenced claim before
// completing, so it cannot bypass probe ownership.
func (store *PostgresStore) CompleteTestPreparation(ctx context.Context, operationID foundation.ID, errorCode string, retryable bool) (OperationRecord, error) {
	operation, err := store.ReadTestOperation(ctx, operationID)
	if err != nil || operation.TerminalAt != nil {
		return operation, err
	}
	var rawHoldOwner string
	if err := store.db.QueryRow(ctx, `SELECT owner_id::text FROM ops.managed_ollama_holds WHERE owner_kind='test' AND operation_id=$1::uuid`, string(operationID)).Scan(&rawHoldOwner); err != nil {
		return OperationRecord{}, err
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
	claimed, owned, err := store.ClaimTestProbe(ctx, TestProbeClaimCommand{
		OperationID: operationID, OwnerID: ownerID, OwnerEpoch: 1,
		ExpectedVersion: operation.Version, LeaseDuration: 10 * time.Minute,
	})
	if err != nil {
		return OperationRecord{}, err
	}
	if !owned {
		if claimed.TerminalAt != nil {
			return claimed, nil
		}
		return OperationRecord{}, conflict("local model runtime test probe is already owned")
	}
	return store.CompleteTestProbe(ctx, TestProbeCompletionCommand{
		OperationID: operationID, OwnerID: ownerID, OwnerEpoch: 1,
		ExpectedVersion: claimed.Version, ErrorCode: errorCode, Retryable: retryable,
	})
}

func completeActivationPreparation(ctx context.Context, tx pgx.Tx, rolloutID foundation.ID, errorCode string, retryable bool) (OperationRecord, error) {
	row := tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM ops.managed_ollama_operations WHERE kind='activation' AND rollout_id=$1::uuid FOR UPDATE`, string(rolloutID))
	operation, err := scanOperation(row)
	if err != nil {
		return OperationRecord{}, err
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
			// The strict operation graph requires queued -> starting before a
			// terminal failure can be recorded.
			if _, err = tx.Exec(ctx, `UPDATE ops.managed_ollama_operations SET phase='starting',version=version+1,updated_at=clock_timestamp() WHERE operation_id=$1::uuid`, string(operation.OperationID)); err != nil {
				return OperationRecord{}, err
			}
		}
		row = tx.QueryRow(ctx, `UPDATE ops.managed_ollama_operations SET phase=$2,error_code=$3,error_retryable=$4,terminal_at=clock_timestamp(),claim_owner_id=NULL,claim_owner_epoch=NULL,claim_expires_at=NULL,version=version+1,updated_at=clock_timestamp() WHERE operation_id=$1::uuid AND terminal_at IS NULL RETURNING `+operationColumns, string(operation.OperationID), string(phase), nullableErrorCode(errorCode), retryable)
		operation, err = scanOperation(row)
		if err != nil {
			return OperationRecord{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.managed_ollama_holds SET released_at=COALESCE(released_at,clock_timestamp()),version=CASE WHEN released_at IS NULL THEN version+1 ELSE version END,updated_at=clock_timestamp() WHERE operation_id=$1::uuid AND released_at IS NULL`, string(operation.OperationID)); err != nil {
		return OperationRecord{}, err
	}
	return operation, nil
}

func seedActivationPreparation(ctx context.Context, tx pgx.Tx, command ActivationPreparationCommand) (ActivationPreparation, error) {
	if err := validateActivationPreparation(command); err != nil {
		return ActivationPreparation{}, err
	}
	models := marshalModelRefs(command.Requirement.Models)
	_, err := tx.Exec(ctx, `INSERT INTO ops.managed_ollama_operations(operation_id,kind,idempotency_key,request_hash,target_revision,rollout_id,requirement_hash,required_models,phase) VALUES($1::uuid,'activation',$2,$3,$4,$5::uuid,$6,$7::jsonb,'queued') ON CONFLICT(kind,idempotency_key) DO NOTHING`, string(command.OperationID), command.IdempotencyKey, command.RequestHash, command.TargetRevision, string(command.RolloutID), command.Requirement.Hash, string(models))
	if err != nil {
		return ActivationPreparation{}, err
	}
	operation, err := readActivationOperation(ctx, tx, `kind='activation' AND idempotency_key=$1`, command.IdempotencyKey)
	if err != nil {
		return ActivationPreparation{}, err
	}
	if operation.RequestHash != command.RequestHash || operation.Requirement.Hash != command.Requirement.Hash || operation.TargetRevision == nil || *operation.TargetRevision != command.TargetRevision || operation.RolloutID == nil || *operation.RolloutID != command.RolloutID {
		return ActivationPreparation{}, conflict("local model runtime activation idempotency key is bound to a different request")
	}
	var hold HoldRecord
	hold, err = scanHold(tx.QueryRow(ctx, `SELECT `+holdColumns+` FROM ops.managed_ollama_holds WHERE operation_id=$1::uuid ORDER BY created_at LIMIT 1`, string(operation.OperationID)))
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO ops.managed_ollama_holds(hold_id,owner_kind,owner_id,owner_epoch,revision,rollout_id,operation_id,requirement_hash,models,lease_expires_at) VALUES($1::uuid,'preparation',$2::uuid,$3,$4,$5::uuid,$6::uuid,$7,$8::jsonb,clock_timestamp()+$9::interval) ON CONFLICT(hold_id) DO NOTHING`, string(command.HoldID), string(command.OwnerID), command.OwnerEpoch, command.TargetRevision, string(command.RolloutID), string(operation.OperationID), command.Requirement.Hash, string(models), intervalArg(command.LeaseDuration))
		if err != nil {
			return ActivationPreparation{}, err
		}
		hold, err = scanHold(tx.QueryRow(ctx, `SELECT `+holdColumns+` FROM ops.managed_ollama_holds WHERE operation_id=$1::uuid ORDER BY created_at LIMIT 1`, string(operation.OperationID)))
	}
	if err != nil {
		return ActivationPreparation{}, err
	}
	if hold.HoldID != command.HoldID || hold.OwnerID != command.OwnerID || hold.OwnerEpoch != command.OwnerEpoch || hold.Requirement.Hash != command.Requirement.Hash || hold.RolloutID == nil || *hold.RolloutID != command.RolloutID || hold.OperationID == nil || *hold.OperationID != operation.OperationID {
		return ActivationPreparation{}, conflict("local model runtime preparation hold identity conflict")
	}
	return ActivationPreparation{Operation: operation, Hold: hold}, nil
}

func seedTestPreparation(ctx context.Context, tx pgx.Tx, command TestPreparationCommand) (TestPreparation, error) {
	if err := validateTestPreparation(command); err != nil {
		return TestPreparation{}, err
	}
	models := marshalModelRefs(command.Requirement.Models)
	_, err := tx.Exec(ctx, `INSERT INTO ops.managed_ollama_operations(operation_id,kind,idempotency_key,request_hash,target_revision,requirement_hash,required_models,phase) VALUES($1::uuid,'test',$2,$3,$4::bigint,$5,$6::jsonb,'queued') ON CONFLICT(kind,idempotency_key) DO NOTHING`, string(command.OperationID), command.IdempotencyKey, command.RequestHash, nullableInt64(command.TargetRevision), command.Requirement.Hash, string(models))
	if err != nil {
		return TestPreparation{}, err
	}
	operation, err := readOperation(ctx, tx, `kind='test' AND idempotency_key=$1`, command.IdempotencyKey)
	if err != nil {
		return TestPreparation{}, err
	}
	if operation.RequestHash != command.RequestHash || operation.Requirement.Hash != command.Requirement.Hash || !equalOptionalInt64(operation.TargetRevision, command.TargetRevision) {
		return TestPreparation{}, conflict("local model runtime test idempotency key is bound to a different request")
	}
	var hold HoldRecord
	hold, err = scanHold(tx.QueryRow(ctx, `SELECT `+holdColumns+` FROM ops.managed_ollama_holds WHERE operation_id=$1::uuid ORDER BY created_at LIMIT 1`, string(operation.OperationID)))
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO ops.managed_ollama_holds(hold_id,owner_kind,owner_id,owner_epoch,role,instance_id,revision,operation_id,requirement_hash,models,lease_expires_at) VALUES($1::uuid,'test',$2::uuid,$3,'api',$2::uuid,$4,$5::uuid,$6,$7::jsonb,clock_timestamp()+$8::interval) ON CONFLICT(hold_id) DO NOTHING`, string(command.HoldID), string(command.OwnerID), command.OwnerEpoch, nullableInt64(command.TargetRevision), string(operation.OperationID), command.Requirement.Hash, string(models), intervalArg(command.LeaseDuration))
		if err != nil {
			return TestPreparation{}, err
		}
		hold, err = scanHold(tx.QueryRow(ctx, `SELECT `+holdColumns+` FROM ops.managed_ollama_holds WHERE operation_id=$1::uuid ORDER BY created_at LIMIT 1`, string(operation.OperationID)))
	}
	if err != nil {
		return TestPreparation{}, err
	}
	if hold.HoldID != command.HoldID || hold.OwnerID != command.OwnerID || hold.OwnerEpoch != command.OwnerEpoch || hold.Requirement.Hash != command.Requirement.Hash || hold.OperationID == nil || *hold.OperationID != operation.OperationID {
		return TestPreparation{}, conflict("local model runtime test hold identity conflict")
	}
	return TestPreparation{Operation: operation, Hold: hold}, nil
}

func readActivationOperation(ctx context.Context, tx pgx.Tx, predicate string, arg any) (OperationRecord, error) {
	return readOperation(ctx, tx, `kind='activation' AND `+predicate, arg)
}

func readOperation(ctx context.Context, tx pgx.Tx, predicate string, arg any) (OperationRecord, error) {
	row := tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM ops.managed_ollama_operations WHERE `+predicate+` ORDER BY created_at DESC LIMIT 1`, arg)
	return scanOperation(row)
}

func (store *PostgresStore) ClaimManager(ctx context.Context, command ManagerClaimCommand) (ManagerLease, error) {
	if ctx == nil {
		return ManagerLease{}, errors.New("local model runtime manager context is nil")
	}
	if command.StaleAfter == 0 {
		command.StaleAfter = command.LeaseDuration
	}
	if err := validateManagerClaim(command); err != nil {
		return ManagerLease{}, err
	}
	row := store.db.QueryRow(ctx, `SELECT owner_id::text,owner_epoch,requirement_version,version,heartbeat_at,lease_expires_at,mode
		FROM ops.managed_ollama_claim_manager($1::uuid,$2::interval,$3::interval)`,
		string(command.OwnerID), intervalArg(command.LeaseDuration), intervalArg(command.StaleAfter))
	lease, err := scanManagerLease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagerLease{}, conflict("local model runtime manager owner is fresh")
	}
	return lease, err
}

func (store *PostgresStore) HeartbeatManager(ctx context.Context, command ManagerHeartbeatCommand) (ManagerLease, error) {
	if ctx == nil {
		return ManagerLease{}, errors.New("local model runtime manager heartbeat context is nil")
	}
	if err := validateManagerHeartbeat(command); err != nil {
		return ManagerLease{}, err
	}
	row := store.db.QueryRow(ctx, `SELECT owner_id::text,owner_epoch,requirement_version,version,heartbeat_at,lease_expires_at,mode
		FROM ops.managed_ollama_heartbeat_manager($1::uuid,$2,$3,$4::interval)`,
		string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion, intervalArg(command.LeaseDuration))
	lease, err := scanManagerLease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagerLease{}, conflict("local model runtime manager heartbeat CAS failed")
	}
	return lease, err
}

func (store *PostgresStore) ReadDemand(ctx context.Context) (DemandSnapshot, error) {
	if ctx == nil {
		return DemandSnapshot{}, errors.New("local model runtime demand context is nil")
	}
	tx, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return DemandSnapshot{}, err
	}
	defer rollback(ctx, tx)
	runtime, err := scanRuntime(tx.QueryRow(ctx, runtimeSelect))
	if err != nil {
		return DemandSnapshot{}, err
	}
	var rolloutPhase string
	var activeRevision int64
	var targetRevision, previousRevision *int64
	var settingsStateVersion int64
	if err := tx.QueryRow(ctx, `SELECT phase,active_revision,target_revision,previous_active_revision,version FROM ops.model_settings_state WHERE singleton=true`).Scan(&rolloutPhase, &activeRevision, &targetRevision, &previousRevision, &settingsStateVersion); err != nil {
		return DemandSnapshot{}, err
	}
	holds, err := scanHolds(ctx, tx)
	if err != nil {
		return DemandSnapshot{}, err
	}
	operations, err := scanOperations(ctx, tx)
	if err != nil {
		return DemandSnapshot{}, err
	}
	sources, err := readSettingsDemandSources(ctx, tx, rolloutPhase, activeRevision, targetRevision, previousRevision)
	if err != nil {
		return DemandSnapshot{}, err
	}
	for i := range holds {
		holdID := holds[i].HoldID
		sources = append(sources, DemandSource{Kind: "hold", HoldID: &holdID, Requirement: holds[i].Requirement})
	}
	for i := range operations {
		operationID := operations[i].OperationID
		sources = append(sources, DemandSource{Kind: "operation", OperationID: &operationID, Requirement: operations[i].Requirement})
	}
	requirement, err := unionRequirements(sources)
	if err != nil {
		return DemandSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DemandSnapshot{}, err
	}
	return DemandSnapshot{
		Requirement:          requirement,
		RequirementVersion:   runtime.RequirementVersion,
		SettingsStateVersion: settingsStateVersion,
		Sources:              sources,
		Holds:                holds,
		Operations:           operations,
		Runtime:              runtime,
		RolloutPhase:         rolloutPhase,
		MayStop:              len(requirement.Models) == 0 && len(holds) == 0 && len(operations) == 0 && (rolloutPhase == "" || rolloutPhase == "idle" || rolloutPhase == "failed"),
	}, nil
}

func (store *PostgresStore) LoadEffectiveIntent(ctx context.Context, command IntentReadCommand) (IntentSnapshot, error) {
	if ctx == nil {
		return IntentSnapshot{}, errors.New("local model runtime intent context is nil")
	}
	if !validID(command.OwnerID) || command.OwnerEpoch <= 0 || command.ExpectedVersion <= 0 || command.FreshWithin <= 0 {
		return IntentSnapshot{}, errors.New("local model runtime intent read is invalid")
	}
	row := store.db.QueryRow(ctx, runtimeSelect+` WHERE singleton=true AND owner_id=$1::uuid AND owner_epoch=$2 AND version=$3 AND heartbeat_at>=clock_timestamp()-$4::interval`, string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion, intervalArg(command.FreshWithin))
	runtime, err := scanRuntime(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return IntentSnapshot{}, conflict("local model runtime manager lease is stale")
	}
	if err != nil {
		return IntentSnapshot{}, err
	}
	requirement, err := requirementFromRuntime(runtime)
	if err != nil {
		return IntentSnapshot{}, err
	}
	return IntentSnapshot{RequirementVersion: runtime.RequirementVersion, Requirement: requirement, Fresh: true, RuntimeVersion: runtime.Version}, nil
}

func (store *PostgresStore) PublishDemand(ctx context.Context, command DemandCASCommand) (RuntimeRecord, error) {
	if ctx == nil {
		return RuntimeRecord{}, errors.New("local model runtime demand context is nil")
	}
	if err := validateDemandCAS(command); err != nil {
		return RuntimeRecord{}, err
	}
	models := marshalModelRefs(command.Requirement.Models)
	row := store.db.QueryRow(ctx, `SELECT `+runtimeColumns+`
		FROM ops.managed_ollama_publish_demand($1::uuid,$2,$3,$4,$5,$6::jsonb,$7)`,
		string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion, command.ExpectedRequirementVersion,
		command.Requirement.Hash, string(models), command.ExpectedSettingsStateVersion)
	runtime, err := scanRuntime(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeRecord{}, conflict("local model runtime demand CAS failed")
	}
	return runtime, err
}

// SeedActiveRecovery creates or replays recovery work derived entirely from
// the current active revision inside PostgreSQL. It cannot introduce demand
// for a model supplied by the manager.
func (store *PostgresStore) SeedActiveRecovery(ctx context.Context, command ActiveRecoveryCommand) (OperationRecord, error) {
	if ctx == nil {
		return OperationRecord{}, errors.New("local model runtime active recovery context is nil")
	}
	if err := validateActiveRecovery(command); err != nil {
		return OperationRecord{}, err
	}
	row := store.db.QueryRow(ctx, `SELECT `+operationColumns+`
		FROM ops.managed_ollama_seed_active_recovery($1::uuid,$2,$3,$4::interval)`,
		string(command.OwnerID), command.OwnerEpoch, command.ExpectedSettingsStateVersion,
		intervalArg(command.LeaseDuration))
	operation, err := scanOperation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperationRecord{}, conflict("local model runtime active recovery settings or ownership conflict")
	}
	return operation, err
}

// ActiveRecoveryCurrent is the narrow read used to cancel an already-issued
// Ollama pull when its active settings binding disappears.
func (store *PostgresStore) ActiveRecoveryCurrent(ctx context.Context, command ActiveRecoveryCheckCommand) (bool, error) {
	if ctx == nil {
		return false, errors.New("local model runtime active recovery check context is nil")
	}
	if err := validateActiveRecoveryCheck(command); err != nil {
		return false, err
	}
	var current bool
	err := store.db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1
		FROM ops.managed_ollama_operations operation
		JOIN ops.model_settings_state state
		  ON state.singleton=true AND state.active_revision=operation.target_revision
		JOIN ops.managed_ollama_runtime runtime
		  ON runtime.singleton=true AND runtime.owner_id=$2::uuid AND runtime.owner_epoch=$3
		 AND runtime.lease_expires_at>clock_timestamp()
		WHERE operation.operation_id=$1::uuid AND operation.kind='active_recovery'
		  AND operation.terminal_at IS NULL
	)`, string(command.OperationID), string(command.OwnerID), command.OwnerEpoch).Scan(&current)
	return current, err
}

func (store *PostgresStore) ClaimOperation(ctx context.Context, command OperationClaimCommand) (OperationRecord, error) {
	if ctx == nil {
		return OperationRecord{}, errors.New("local model runtime operation context is nil")
	}
	if err := validateOperationClaim(command); err != nil {
		return OperationRecord{}, err
	}
	required := marshalModelRefs(command.Requirement.Models)
	row := store.db.QueryRow(ctx, `SELECT `+operationColumns+`
		FROM ops.managed_ollama_claim_operation(
			$1::uuid,$2,$3,$4,$5::bigint,$6::uuid,$7,$8::jsonb,$9::uuid,$10,$11::interval
		)`,
		string(command.OperationID), string(command.Kind), command.IdempotencyKey, command.RequestHash,
		nullableInt64(command.TargetRevision), nullableID(command.RolloutID), command.Requirement.Hash, string(required),
		string(command.OwnerID), command.OwnerEpoch, intervalArg(command.LeaseDuration))
	operation, err := scanOperation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperationRecord{}, conflict("local model runtime operation idempotency or ownership conflict")
	}
	if err != nil {
		return OperationRecord{}, err
	}
	return operation, nil
}

func (store *PostgresStore) BeginPullAttempt(ctx context.Context, command PullAttemptCommand) (PullAttemptResult, error) {
	if ctx == nil {
		return PullAttemptResult{}, errors.New("local model runtime pull attempt context is nil")
	}
	if err := validatePullAttempt(command); err != nil {
		return PullAttemptResult{}, err
	}
	row := store.db.QueryRow(ctx, `SELECT `+operationColumns+`,
		GREATEST(0, (EXTRACT(EPOCH FROM (created_at+interval '6 hours'-clock_timestamp()))*1000000)::bigint)
		FROM ops.managed_ollama_begin_pull_attempt($1::uuid,$2::uuid,$3,$4,$5::interval)`,
		string(command.OperationID), string(command.OwnerID), command.OwnerEpoch,
		command.ExpectedVersion, intervalArg(command.LeaseDuration))
	attempt, err := scanPullAttempt(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return PullAttemptResult{}, conflict("local model runtime pull attempt CAS failed")
	}
	return attempt, err
}

func (store *PostgresStore) RecordOperationProgress(ctx context.Context, command OperationProgressCommand) (OperationRecord, error) {
	if ctx == nil {
		return OperationRecord{}, errors.New("local model runtime operation progress context is nil")
	}
	if err := validateOperationProgress(command); err != nil {
		return OperationRecord{}, err
	}
	completed := marshalModelRefs(command.CompletedModels)
	resolved := marshalResolvedModels(command.ResolvedModels)
	row := store.db.QueryRow(ctx, `SELECT `+operationColumns+`
		FROM ops.managed_ollama_record_operation_progress(
			$1::uuid,$2::uuid,$3,$4,$5,$6::jsonb,$7::jsonb,$8,$9,$10,$11::interval,$12
		)`,
		string(command.OperationID), string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion,
		string(command.NextPhase), string(completed), string(resolved), command.CompletedBytes,
		nullableInt64(command.TotalBytes), command.ProgressKnown, intervalArg(command.LeaseDuration), string(command.ExpectedPhase))
	operation, err := scanOperation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperationRecord{}, conflict("local model runtime operation progress CAS failed")
	}
	return operation, err
}

func (store *PostgresStore) CompleteOperation(ctx context.Context, command OperationTerminalCommand) (OperationRecord, error) {
	if ctx == nil {
		return OperationRecord{}, errors.New("local model runtime operation terminal context is nil")
	}
	if err := validateOperationTerminal(command); err != nil {
		return OperationRecord{}, err
	}
	completed := marshalModelRefs(command.CompletedModels)
	row := store.db.QueryRow(ctx, `SELECT `+operationColumns+`
		FROM ops.managed_ollama_complete_operation(
			$1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8::jsonb,$9,$10
		)`,
		string(command.OperationID), string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion,
		string(command.Phase), nullableErrorCode(command.ErrorCode), command.Retryable, string(completed),
		command.CompletedBytes, nullableInt64(command.TotalBytes))
	operation, err := scanOperation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperationRecord{}, conflict("local model runtime operation terminal CAS failed")
	}
	return operation, err
}

func (store *PostgresStore) SweepExpiredOperations(ctx context.Context, command OperationExpirySweepCommand) (int64, error) {
	if ctx == nil {
		return 0, errors.New("local model runtime operation expiry context is nil")
	}
	if err := validateOperationExpirySweep(command); err != nil {
		return 0, err
	}
	var expired int64
	err := store.db.QueryRow(ctx, `SELECT ops.managed_ollama_expire_operations($1::uuid,$2)`, string(command.OwnerID), command.OwnerEpoch).Scan(&expired)
	return expired, err
}

func (store *PostgresStore) AcquireHold(ctx context.Context, command HoldAcquireCommand) (HoldRecord, error) {
	if ctx == nil {
		return HoldRecord{}, errors.New("local model runtime hold context is nil")
	}
	if err := validateHoldAcquire(command); err != nil {
		return HoldRecord{}, err
	}
	models := marshalModelRefs(command.Requirement.Models)
	row := store.db.QueryRow(ctx, `
INSERT INTO ops.managed_ollama_holds(hold_id,owner_kind,owner_id,owner_epoch,role,instance_id,revision,rollout_id,operation_id,requirement_hash,models,lease_expires_at)
VALUES($1::uuid,$2,$3::uuid,$4,$5,$6::uuid,$7,$8::uuid,$9::uuid,$10,$11::jsonb,clock_timestamp()+$12::interval)
ON CONFLICT(hold_id) DO UPDATE SET lease_expires_at=clock_timestamp()+$12::interval, version=ops.managed_ollama_holds.version+1, updated_at=clock_timestamp()
WHERE ops.managed_ollama_holds.owner_id=$3::uuid AND ops.managed_ollama_holds.owner_epoch=$4 AND ops.managed_ollama_holds.requirement_hash=$10 AND ops.managed_ollama_holds.released_at IS NULL
RETURNING `+holdColumns,
		string(command.HoldID), string(command.Kind), string(command.OwnerID), command.OwnerEpoch,
		nullableText(command.Role), nullableID(command.InstanceID), nullableInt64(command.Revision), nullableID(command.RolloutID), nullableID(command.OperationID), command.Requirement.Hash, string(models), intervalArg(command.LeaseDuration))
	hold, err := scanHold(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return HoldRecord{}, conflict("local model runtime hold ownership or identity conflict")
	}
	return hold, err
}

func (store *PostgresStore) RenewHold(ctx context.Context, command HoldRenewCommand) (HoldRecord, error) {
	if ctx == nil {
		return HoldRecord{}, errors.New("local model runtime hold renewal context is nil")
	}
	if err := validateHoldRenew(command); err != nil {
		return HoldRecord{}, err
	}
	row := store.db.QueryRow(ctx, `UPDATE ops.managed_ollama_holds SET lease_expires_at=clock_timestamp()+$5::interval,version=version+1,updated_at=clock_timestamp() WHERE hold_id=$1::uuid AND owner_id=$2::uuid AND owner_epoch=$3 AND version=$4 AND released_at IS NULL RETURNING `+holdColumns, string(command.HoldID), string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion, intervalArg(command.LeaseDuration))
	hold, err := scanHold(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return HoldRecord{}, conflict("local model runtime hold renewal CAS failed")
	}
	return hold, err
}

func (store *PostgresStore) ReleaseHold(ctx context.Context, command HoldReleaseCommand) (HoldRecord, error) {
	if ctx == nil {
		return HoldRecord{}, errors.New("local model runtime hold release context is nil")
	}
	if !validID(command.HoldID) || !validID(command.OwnerID) || command.OwnerEpoch <= 0 {
		return HoldRecord{}, errors.New("local model runtime hold release is invalid")
	}
	row := store.db.QueryRow(ctx, `UPDATE ops.managed_ollama_holds SET released_at=COALESCE(released_at,clock_timestamp()),version=CASE WHEN released_at IS NULL THEN version+1 ELSE version END,updated_at=clock_timestamp() WHERE hold_id=$1::uuid AND owner_id=$2::uuid AND owner_epoch=$3 AND ($4=0 OR version=$4) RETURNING `+holdColumns, string(command.HoldID), string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion)
	hold, err := scanHold(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return HoldRecord{}, conflict("local model runtime hold release CAS failed")
	}
	return hold, err
}

func (store *PostgresStore) CompareAndSetRuntimePhase(ctx context.Context, command RuntimePhaseCommand) (RuntimeRecord, error) {
	if ctx == nil {
		return RuntimeRecord{}, errors.New("local model runtime phase context is nil")
	}
	if err := validateRuntimeCommand(command); err != nil {
		return RuntimeRecord{}, err
	}
	row := store.db.QueryRow(ctx, `SELECT `+runtimeColumns+`
		FROM ops.managed_ollama_compare_runtime_phase(
			$1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11
		)`, string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion, string(command.ExpectedPhase), string(command.NextPhase), command.RequirementHash, command.ReadyHash, command.ChildEpoch, nullableErrorCode(command.ErrorCode), command.Retryable, command.ExpectedSettingsStateVersion)
	runtime, err := scanRuntime(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeRecord{}, conflict("local model runtime phase CAS failed")
	}
	return runtime, err
}

const runtimeColumns = `mode,owner_id::text,owner_epoch,heartbeat_at,lease_expires_at,requirement_version,requirement_hash,required_models,observed_phase,ready_requirement_hash,child_epoch,last_error_code,last_error_retryable,version,updated_at`
const runtimeSelect = `SELECT ` + runtimeColumns + ` FROM ops.managed_ollama_runtime`
const holdColumns = `hold_id::text,owner_kind,owner_id::text,owner_epoch,role,instance_id::text,revision,rollout_id::text,operation_id::text,requirement_hash,models,lease_expires_at,version,released_at,created_at,updated_at`
const operationColumns = `operation_id::text,kind,idempotency_key,request_hash,target_revision,rollout_id::text,requirement_hash,required_models,phase,claim_owner_id::text,claim_owner_epoch,claim_expires_at,version,completed_models,resolved_models,completed_bytes,total_bytes,progress_known,attempt_no,last_progress_at,error_code,error_retryable,created_at,updated_at,terminal_at`

func scanRuntime(row interface{ Scan(...any) error }) (RuntimeRecord, error) {
	var r RuntimeRecord
	var owner, errCode *string
	var heartbeatAt, expiryAt *time.Time
	var rawModels []byte
	if err := row.Scan(&r.Mode, &owner, &r.OwnerEpoch, &heartbeatAt, &expiryAt,
		&r.RequirementVersion, &r.Requirement.Hash, &rawModels, &r.Phase, &r.ReadyHash,
		&r.ChildEpoch, &errCode, &r.Retryable, &r.Version, &r.UpdatedAt); err != nil {
		return RuntimeRecord{}, err
	}
	var refs []ModelRef
	if err := json.Unmarshal(rawModels, &refs); err != nil {
		return RuntimeRecord{}, err
	}
	requirement, err := ParseRequirement(refs, r.Requirement.Hash)
	if err != nil {
		return RuntimeRecord{}, err
	}
	r.Requirement = requirement
	if owner != nil {
		id, parseErr := foundation.ParseID(*owner)
		if parseErr != nil {
			return RuntimeRecord{}, parseErr
		}
		r.OwnerID = &id
	}
	r.HeartbeatAt = heartbeatAt
	r.LeaseExpiresAt = expiryAt
	if errCode != nil {
		r.ErrorCode = *errCode
	}
	return r, nil
}

func scanManagerLease(row interface{ Scan(...any) error }) (ManagerLease, error) {
	var l ManagerLease
	var owner string
	if err := row.Scan(&owner, &l.OwnerEpoch, &l.RequirementVersion, &l.Version, &l.HeartbeatAt, &l.LeaseExpiresAt, &l.Mode); err != nil {
		return ManagerLease{}, err
	}
	id, err := foundation.ParseID(owner)
	if err != nil {
		return ManagerLease{}, err
	}
	l.OwnerID = id
	return l, nil
}

func scanHold(row interface{ Scan(...any) error }) (HoldRecord, error) {
	var h HoldRecord
	var id, owner string
	var instance, rollout, operation, role *string
	var models []byte
	var released *time.Time
	var revision *int64
	if err := row.Scan(&id, &h.Kind, &owner, &h.OwnerEpoch, &role, &instance, &revision, &rollout, &operation, &h.Requirement.Hash, &models, &h.LeaseExpiresAt, &h.Version, &released, &h.CreatedAt, &h.UpdatedAt); err != nil {
		return HoldRecord{}, err
	}
	parsed, err := foundation.ParseID(id)
	if err != nil {
		return HoldRecord{}, err
	}
	h.HoldID = parsed
	ownerID, err := foundation.ParseID(owner)
	if err != nil {
		return HoldRecord{}, err
	}
	h.OwnerID = ownerID
	if role != nil {
		h.Role = *role
	}
	h.Revision = revision
	h.InstanceID = parseOptionalID(instance)
	h.RolloutID = parseOptionalID(rollout)
	h.OperationID = parseOptionalID(operation)
	var refs []ModelRef
	if err := json.Unmarshal(models, &refs); err != nil {
		return HoldRecord{}, err
	}
	h.Requirement, err = ParseRequirement(refs, h.Requirement.Hash)
	if err != nil {
		return HoldRecord{}, err
	}
	h.ReleasedAt = released
	return h, nil
}

func scanOperation(row interface{ Scan(...any) error }) (OperationRecord, error) {
	return scanOperationRow(row, nil)
}

func scanPullAttempt(row interface{ Scan(...any) error }) (PullAttemptResult, error) {
	var remainingMicros int64
	operation, err := scanOperationRow(row, &remainingMicros)
	if err != nil {
		return PullAttemptResult{}, err
	}
	return PullAttemptResult{
		Operation: operation,
		Remaining: time.Duration(remainingMicros) * time.Microsecond,
	}, nil
}

func scanOperationRow(row interface{ Scan(...any) error }, remaining *int64) (OperationRecord, error) {
	var o OperationRecord
	var id string
	var rollout, owner, errorCode *string
	var claimOwnerEpoch *int64
	var models, completed, resolved []byte
	var claimExpiry, lastProgress, terminal *time.Time
	var target *int64
	args := []any{&id, &o.Kind, &o.IdempotencyKey, &o.RequestHash, &target, &rollout,
		&o.Requirement.Hash, &models, &o.Phase, &owner, &claimOwnerEpoch, &claimExpiry,
		&o.Version, &completed, &resolved, &o.CompletedBytes, &o.TotalBytes, &o.ProgressKnown,
		&o.AttemptNo, &lastProgress, &errorCode, &o.Retryable, &o.CreatedAt, &o.UpdatedAt, &terminal}
	if remaining != nil {
		args = append(args, remaining)
	}
	if err := row.Scan(args...); err != nil {
		return OperationRecord{}, err
	}
	parsed, err := foundation.ParseID(id)
	if err != nil {
		return OperationRecord{}, err
	}
	o.OperationID = parsed
	o.TargetRevision = target
	o.RolloutID = parseOptionalID(rollout)
	o.ClaimOwnerID = parseOptionalID(owner)
	if claimOwnerEpoch != nil {
		o.ClaimOwnerEpoch = *claimOwnerEpoch
	}
	if errorCode != nil {
		o.ErrorCode = *errorCode
	}
	o.ClaimExpiresAt = claimExpiry
	o.LastProgressAt = lastProgress
	o.TerminalAt = terminal
	var refs []ModelRef
	if err := json.Unmarshal(models, &refs); err != nil {
		return OperationRecord{}, err
	}
	if err := json.Unmarshal(completed, &o.CompletedModels); err != nil {
		return OperationRecord{}, err
	}
	if err := json.Unmarshal(resolved, &o.ResolvedModels); err != nil {
		return OperationRecord{}, err
	}
	o.Requirement, err = ParseRequirement(refs, o.Requirement.Hash)
	if err != nil {
		return OperationRecord{}, err
	}
	return o, nil
}

func scanHolds(ctx context.Context, tx pgx.Tx) ([]HoldRecord, error) {
	rows, err := tx.Query(ctx, `SELECT `+holdColumns+`
		FROM ops.managed_ollama_holds hold
		WHERE hold.released_at IS NULL AND hold.lease_expires_at>clock_timestamp()
		  AND NOT EXISTS (
		      SELECT 1 FROM ops.managed_ollama_operations operation
		      WHERE operation.operation_id=hold.operation_id AND operation.kind='active_recovery'
		        AND NOT EXISTS (
		            SELECT 1 FROM ops.model_settings_state state
		            WHERE state.singleton=true AND state.active_revision=operation.target_revision
		        )
		  )
		ORDER BY hold.created_at,hold.hold_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HoldRecord
	for rows.Next() {
		h, e := scanHold(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
func scanOperations(ctx context.Context, tx pgx.Tx) ([]OperationRecord, error) {
	rows, err := tx.Query(ctx, `SELECT `+operationColumns+`
		FROM ops.managed_ollama_operations operation
		WHERE operation.terminal_at IS NULL
		  AND (operation.kind<>'active_recovery' OR EXISTS (
		      SELECT 1 FROM ops.model_settings_state state
		      WHERE state.singleton=true AND state.active_revision=operation.target_revision
		  ))
		ORDER BY operation.created_at,operation.operation_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OperationRecord
	for rows.Next() {
		o, e := scanOperation(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func readSettingsDemandSources(
	ctx context.Context,
	tx pgx.Tx,
	phase string,
	active int64,
	target, previous *int64,
) ([]DemandSource, error) {
	revisions := []struct {
		kind     string
		revision int64
	}{{kind: "active", revision: active}}
	if phase == "preparing" || phase == "arming" || phase == "activating" {
		if previous == nil || target == nil {
			return nil, errors.New("local model runtime rollout demand is malformed")
		}
		revisions = append(revisions, struct {
			kind     string
			revision int64
		}{kind: "previous", revision: *previous}, struct {
			kind     string
			revision int64
		}{kind: "target", revision: *target})
	}
	seen := make(map[int64]struct{}, len(revisions))
	var sources []DemandSource
	for _, candidate := range revisions {
		if _, ok := seen[candidate.revision]; ok {
			continue
		}
		seen[candidate.revision] = struct{}{}
		requirement, err := readRevisionRequirement(ctx, tx, candidate.revision)
		if err != nil {
			return nil, err
		}
		if len(requirement.Models) == 0 {
			continue
		}
		sources = append(sources, DemandSource{Kind: candidate.kind, Revision: candidate.revision, Requirement: requirement})
	}
	return sources, nil
}

func readRevisionRequirement(ctx context.Context, tx pgx.Tx, revision int64) (Requirement, error) {
	if revision == 0 {
		return NewRequirement(nil)
	}
	var chatModel, embeddingModel *string
	if err := tx.QueryRow(ctx, `SELECT local_chat_model,local_embedding_model
		FROM ops.managed_ollama_revision_requirements WHERE revision=$1`, revision).Scan(
		&chatModel, &embeddingModel,
	); err != nil {
		return Requirement{}, err
	}
	var models []ModelRef
	if chatModel != nil {
		if *chatModel == "" {
			return Requirement{}, errors.New("local chat model requirement is empty")
		}
		models = append(models, ModelRef(*chatModel))
	}
	if embeddingModel != nil {
		if *embeddingModel == "" {
			return Requirement{}, errors.New("local embedding model requirement is empty")
		}
		models = append(models, ModelRef(*embeddingModel))
	}
	return NewRequirement(models)
}

func unionRequirements(sources []DemandSource) (Requirement, error) {
	var models []ModelRef
	for _, source := range sources {
		models = append(models, source.Requirement.Models...)
	}
	return NewRequirement(models)
}

func requirementFromRuntime(r RuntimeRecord) (Requirement, error) { return r.Requirement, nil }
func parseOptionalID(value *string) *foundation.ID {
	if value == nil || *value == "" {
		return nil
	}
	id, err := foundation.ParseID(*value)
	if err != nil {
		return nil
	}
	return &id
}
func rollback(ctx context.Context, tx pgx.Tx) { _ = tx.Rollback(ctx) }
func conflict(message string) error           { return fmt.Errorf("LOCAL_MODEL_RUNTIME_CONFLICT: %s", message) }
func intervalArg(d time.Duration) string      { return fmt.Sprintf("%d microseconds", d.Microseconds()) }
func nullableID(id *foundation.ID) any {
	if id == nil {
		return nil
	}
	return string(*id)
}
func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
func nullableText(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
func nullableErrorCode(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func marshalModelRefs(models []ModelRef) []byte {
	if models == nil {
		models = []ModelRef{}
	}
	encoded, _ := json.Marshal(models)
	return encoded
}

func marshalResolvedModels(models []ResolvedModel) []byte {
	if models == nil {
		models = []ResolvedModel{}
	}
	encoded, _ := json.Marshal(models)
	return encoded
}

func validateManagerClaim(c ManagerClaimCommand) error {
	if !validID(c.OwnerID) || c.LeaseDuration <= 0 || c.LeaseDuration > 10*time.Minute || c.StaleAfter < c.LeaseDuration || c.StaleAfter > 10*time.Minute {
		return errors.New("invalid manager claim")
	}
	return nil
}
func validateManagerHeartbeat(c ManagerHeartbeatCommand) error {
	if !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || c.LeaseDuration <= 0 || c.LeaseDuration > 10*time.Minute {
		return errors.New("invalid manager heartbeat")
	}
	return nil
}
func validateDemandCAS(c DemandCASCommand) error {
	if !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || c.ExpectedRequirementVersion < 0 || c.ExpectedSettingsStateVersion <= 0 {
		return errors.New("invalid demand CAS")
	}
	if _, err := ParseRequirement(c.Requirement.Models, c.Requirement.Hash); err != nil {
		return err
	}
	return nil
}
func validateOperationClaim(c OperationClaimCommand) error {
	if !validID(c.OperationID) || !ValidOperationKind(c.Kind) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.LeaseDuration <= 0 || c.LeaseDuration > time.Hour || strings.TrimSpace(c.IdempotencyKey) != c.IdempotencyKey || c.IdempotencyKey == "" || len(c.IdempotencyKey) > 256 || strings.IndexFunc(c.IdempotencyKey, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 || len(c.RequestHash) != 64 || !isLowerHex(c.RequestHash) {
		return errors.New("invalid operation claim")
	}
	if _, err := ParseRequirement(c.Requirement.Models, c.Requirement.Hash); err != nil {
		return err
	}
	if len(c.Requirement.Models) == 0 {
		return errors.New("operation requirement is empty")
	}
	return nil
}

func validatePullAttempt(c PullAttemptCommand) error {
	if !validID(c.OperationID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || c.LeaseDuration <= 0 || c.LeaseDuration > time.Hour {
		return errors.New("invalid pull attempt")
	}
	return nil
}

func validateOperationExpirySweep(c OperationExpirySweepCommand) error {
	if !validID(c.OwnerID) || c.OwnerEpoch <= 0 {
		return errors.New("invalid operation expiry sweep")
	}
	return nil
}

func validateActiveRecovery(c ActiveRecoveryCommand) error {
	if !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedSettingsStateVersion <= 0 || c.LeaseDuration <= 0 || c.LeaseDuration > time.Hour {
		return errors.New("invalid active recovery command")
	}
	return nil
}

func validateActiveRecoveryCheck(c ActiveRecoveryCheckCommand) error {
	if !validID(c.OperationID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 {
		return errors.New("invalid active recovery check")
	}
	return nil
}

func validateActivationPreparation(c ActivationPreparationCommand) error {
	if !validID(c.OperationID) || !validID(c.HoldID) || !validID(c.RolloutID) || !validID(c.OwnerID) || c.TargetRevision < 0 || c.OwnerEpoch <= 0 || c.LeaseDuration <= 0 || strings.TrimSpace(c.IdempotencyKey) != c.IdempotencyKey || c.IdempotencyKey == "" || len(c.IdempotencyKey) > 256 || strings.IndexFunc(c.IdempotencyKey, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 || len(c.RequestHash) != 64 || !isLowerHex(c.RequestHash) {
		return errors.New("invalid activation preparation")
	}
	if _, err := ParseRequirement(c.Requirement.Models, c.Requirement.Hash); err != nil {
		return err
	}
	if len(c.Requirement.Models) == 0 {
		return errors.New("activation preparation requirement is empty")
	}
	return nil
}

func validateTestPreparation(c TestPreparationCommand) error {
	if !validID(c.OperationID) || !validID(c.HoldID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.LeaseDuration <= 0 || strings.TrimSpace(c.IdempotencyKey) != c.IdempotencyKey || c.IdempotencyKey == "" || len(c.IdempotencyKey) > 256 || strings.IndexFunc(c.IdempotencyKey, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 || len(c.RequestHash) != 64 || !isLowerHex(c.RequestHash) {
		return errors.New("invalid test preparation")
	}
	if c.TargetRevision != nil && *c.TargetRevision < 0 {
		return errors.New("invalid test preparation target revision")
	}
	if _, err := ParseRequirement(c.Requirement.Models, c.Requirement.Hash); err != nil {
		return err
	}
	if len(c.Requirement.Models) == 0 {
		return errors.New("test preparation requirement is empty")
	}
	return nil
}
func validateTestProbeClaim(c TestProbeClaimCommand) error {
	if !validID(c.OperationID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || c.LeaseDuration <= 0 {
		return errors.New("invalid test probe claim")
	}
	return nil
}
func validateTestProbeCompletion(c TestProbeCompletionCommand) error {
	if !validID(c.OperationID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 {
		return errors.New("invalid test probe completion")
	}
	if c.Abandon {
		if c.ErrorCode != "" || c.Retryable {
			return errors.New("abandoned test probe cannot record a terminal error")
		}
		return nil
	}
	if c.ErrorCode == "" {
		if c.Retryable {
			return errors.New("successful test probe cannot be retryable")
		}
		return nil
	}
	if len(c.ErrorCode) > 128 || strings.IndexFunc(c.ErrorCode, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_'
	}) >= 0 {
		return errors.New("invalid test probe error code")
	}
	return nil
}

func equalOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
func validateOperationProgress(c OperationProgressCommand) error {
	if !validID(c.OperationID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || !ValidOperationPhase(c.ExpectedPhase) || !ValidOperationPhase(c.NextPhase) || c.LeaseDuration <= 0 || c.LeaseDuration > time.Hour || c.CompletedBytes < 0 {
		return errors.New("invalid operation progress")
	}
	if c.TotalBytes != nil && *c.TotalBytes < c.CompletedBytes {
		return errors.New("operation progress exceeds total")
	}
	return nil
}
func validateOperationTerminal(c OperationTerminalCommand) error {
	if !validID(c.OperationID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || (c.Phase != OperationPhaseReady && c.Phase != OperationPhaseFailed) || c.LeaseDuration < 0 || c.CompletedBytes < 0 {
		return errors.New("invalid operation terminal")
	}
	if c.TotalBytes != nil && *c.TotalBytes < c.CompletedBytes {
		return errors.New("operation terminal exceeds total")
	}
	return nil
}
func validateHoldAcquire(c HoldAcquireCommand) error {
	if !validID(c.HoldID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || !ValidHoldKind(c.Kind) || c.LeaseDuration <= 0 {
		return errors.New("invalid hold acquire")
	}
	if _, err := ParseRequirement(c.Requirement.Models, c.Requirement.Hash); err != nil {
		return err
	}
	if len(c.Requirement.Models) == 0 {
		return errors.New("hold requirement is empty")
	}
	return nil
}
func validateHoldRenew(c HoldRenewCommand) error {
	if !validID(c.HoldID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || c.LeaseDuration <= 0 {
		return errors.New("invalid hold renewal")
	}
	return nil
}
func validateRuntimeCommand(c RuntimePhaseCommand) error {
	if !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || c.ExpectedSettingsStateVersion <= 0 || c.ChildEpoch < 0 || !ValidRuntimePhase(c.ExpectedPhase) || !ValidRuntimePhase(c.NextPhase) {
		return errors.New("invalid runtime phase command")
	}
	if c.RequirementHash != "" && !isLowerHex(c.RequirementHash) {
		return errors.New("invalid runtime requirement hash")
	}
	if c.ReadyHash != "" && !isLowerHex(c.ReadyHash) {
		return errors.New("invalid runtime ready hash")
	}
	if c.NextPhase == RuntimePhaseReady && (c.RequirementHash == "" || c.ReadyHash != c.RequirementHash) {
		return errors.New("ready runtime phase must bind requirement")
	}
	if c.NextPhase != RuntimePhaseReady && c.ReadyHash != "" {
		return errors.New("non-ready runtime phase cannot retain ready hash")
	}
	return nil
}

func isLowerHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
