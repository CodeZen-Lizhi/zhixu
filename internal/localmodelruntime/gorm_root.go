package localmodelruntime

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

func (store *GORMStore) ClaimManager(ctx context.Context, command ManagerClaimCommand) (ManagerLease, error) {
	if err := store.ready(ctx); err != nil {
		return ManagerLease{}, err
	}
	if command.StaleAfter == 0 {
		command.StaleAfter = command.LeaseDuration
	}
	if err := validateManagerClaim(command); err != nil {
		return ManagerLease{}, err
	}
	row, err := gormRawRow(ctx, store.database, `SELECT owner_id::text,owner_epoch,requirement_version,version,heartbeat_at,lease_expires_at,mode
		FROM ops.managed_ollama_claim_manager(?::uuid,?::interval,?::interval)`,
		string(command.OwnerID), intervalArg(command.LeaseDuration), intervalArg(command.StaleAfter))
	if err != nil {
		return ManagerLease{}, gormStoreError(ctx, err)
	}
	lease, err := scanManagerLease(row)
	if gormNoRows(err) {
		return ManagerLease{}, conflict("local model runtime manager owner is fresh")
	}
	return lease, gormStoreError(ctx, err)
}

func (store *GORMStore) HeartbeatManager(ctx context.Context, command ManagerHeartbeatCommand) (ManagerLease, error) {
	if err := store.ready(ctx); err != nil {
		return ManagerLease{}, err
	}
	if err := validateManagerHeartbeat(command); err != nil {
		return ManagerLease{}, err
	}
	row, err := gormRawRow(ctx, store.database, `SELECT owner_id::text,owner_epoch,requirement_version,version,heartbeat_at,lease_expires_at,mode
		FROM ops.managed_ollama_heartbeat_manager(?::uuid,?,?,?::interval)`,
		string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion, intervalArg(command.LeaseDuration))
	if err != nil {
		return ManagerLease{}, gormStoreError(ctx, err)
	}
	lease, err := scanManagerLease(row)
	if gormNoRows(err) {
		return ManagerLease{}, conflict("local model runtime manager heartbeat CAS failed")
	}
	return lease, gormStoreError(ctx, err)
}

func (store *GORMStore) ReadDemand(ctx context.Context) (snapshot DemandSnapshot, err error) {
	if err = store.ready(ctx); err != nil {
		return DemandSnapshot{}, err
	}
	err = store.within(ctx, foundation.TransactionOptions{
		Isolation: foundation.TransactionIsolationRepeatableRead,
		ReadOnly:  true,
	}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		database, unwrapErr := platformpostgres.GORMTransaction(scope)
		if unwrapErr != nil {
			return unwrapErr
		}
		row, rowErr := gormRawRow(callbackCtx, database, runtimeSelect)
		if rowErr != nil {
			return rowErr
		}
		runtime, scanErr := scanRuntime(row)
		if scanErr != nil {
			return scanErr
		}
		stateRow, stateErr := gormRawRow(callbackCtx, database, `SELECT phase,active_revision,target_revision,previous_active_revision,version
			FROM ops.model_settings_state WHERE singleton=true`)
		if stateErr != nil {
			return stateErr
		}
		var rolloutPhase string
		var activeRevision int64
		var targetRevision, previousRevision *int64
		var settingsStateVersion int64
		if stateErr = stateRow.Scan(&rolloutPhase, &activeRevision, &targetRevision, &previousRevision, &settingsStateVersion); stateErr != nil {
			return stateErr
		}
		holds, holdErr := scanGORMHolds(callbackCtx, database)
		if holdErr != nil {
			return holdErr
		}
		operations, operationErr := scanGORMOperations(callbackCtx, database)
		if operationErr != nil {
			return operationErr
		}
		sources, sourceErr := readGORMSettingsDemandSources(callbackCtx, database, rolloutPhase, activeRevision, targetRevision, previousRevision)
		if sourceErr != nil {
			return sourceErr
		}
		for i := range holds {
			holdID := holds[i].HoldID
			sources = append(sources, DemandSource{Kind: "hold", HoldID: &holdID, Requirement: holds[i].Requirement})
		}
		for i := range operations {
			operationID := operations[i].OperationID
			sources = append(sources, DemandSource{Kind: "operation", OperationID: &operationID, Requirement: operations[i].Requirement})
		}
		requirement, requirementErr := unionRequirements(sources)
		if requirementErr != nil {
			return requirementErr
		}
		snapshot = DemandSnapshot{
			Requirement:          requirement,
			RequirementVersion:   runtime.RequirementVersion,
			SettingsStateVersion: settingsStateVersion,
			Sources:              sources,
			Holds:                holds,
			Operations:           operations,
			Runtime:              runtime,
			RolloutPhase:         rolloutPhase,
			MayStop:              len(requirement.Models) == 0 && len(holds) == 0 && len(operations) == 0 && (rolloutPhase == "" || rolloutPhase == "idle" || rolloutPhase == "failed"),
		}
		return nil
	})
	if err != nil {
		return DemandSnapshot{}, gormStoreError(ctx, err)
	}
	return snapshot, nil
}

func (store *GORMStore) LoadEffectiveIntent(ctx context.Context, command IntentReadCommand) (IntentSnapshot, error) {
	if err := store.ready(ctx); err != nil {
		return IntentSnapshot{}, err
	}
	if !validID(command.OwnerID) || command.OwnerEpoch <= 0 || command.ExpectedVersion <= 0 || command.FreshWithin <= 0 {
		return IntentSnapshot{}, errors.New("local model runtime intent read is invalid")
	}
	row, err := gormRawRow(ctx, store.database, runtimeSelect+` WHERE singleton=true AND owner_id=?::uuid AND owner_epoch=? AND version=? AND heartbeat_at>=clock_timestamp()-?::interval`,
		string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion, intervalArg(command.FreshWithin))
	if err != nil {
		return IntentSnapshot{}, gormStoreError(ctx, err)
	}
	runtime, err := scanRuntime(row)
	if gormNoRows(err) {
		return IntentSnapshot{}, conflict("local model runtime manager lease is stale")
	}
	if err != nil {
		return IntentSnapshot{}, gormStoreError(ctx, err)
	}
	requirement, err := requirementFromRuntime(runtime)
	if err != nil {
		return IntentSnapshot{}, gormStoreError(ctx, err)
	}
	return IntentSnapshot{RequirementVersion: runtime.RequirementVersion, Requirement: requirement, Fresh: true, RuntimeVersion: runtime.Version}, nil
}

func (store *GORMStore) PublishDemand(ctx context.Context, command DemandCASCommand) (RuntimeRecord, error) {
	if err := store.ready(ctx); err != nil {
		return RuntimeRecord{}, err
	}
	if err := validateDemandCAS(command); err != nil {
		return RuntimeRecord{}, err
	}
	models := marshalModelRefs(command.Requirement.Models)
	row, err := gormRawRow(ctx, store.database, `SELECT `+runtimeColumns+`
		FROM ops.managed_ollama_publish_demand(?::uuid,?,?,?,?,?::jsonb,?)`,
		string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion, command.ExpectedRequirementVersion,
		command.Requirement.Hash, string(models), command.ExpectedSettingsStateVersion)
	if err != nil {
		return RuntimeRecord{}, gormStoreError(ctx, err)
	}
	runtime, err := scanRuntime(row)
	if gormNoRows(err) {
		return RuntimeRecord{}, conflict("local model runtime demand CAS failed")
	}
	return runtime, gormStoreError(ctx, err)
}

func (store *GORMStore) SeedActiveRecovery(ctx context.Context, command ActiveRecoveryCommand) (OperationRecord, error) {
	if err := store.ready(ctx); err != nil {
		return OperationRecord{}, err
	}
	if err := validateActiveRecovery(command); err != nil {
		return OperationRecord{}, err
	}
	row, err := gormRawRow(ctx, store.database, `SELECT `+operationColumns+`
		FROM ops.managed_ollama_seed_active_recovery(?::uuid,?,?,?::interval)`,
		string(command.OwnerID), command.OwnerEpoch, command.ExpectedSettingsStateVersion, intervalArg(command.LeaseDuration))
	if err != nil {
		return OperationRecord{}, gormStoreError(ctx, err)
	}
	operation, err := scanOperation(row)
	if gormNoRows(err) {
		return OperationRecord{}, conflict("local model runtime active recovery settings or ownership conflict")
	}
	return operation, gormStoreError(ctx, err)
}

func (store *GORMStore) ActiveRecoveryCurrent(ctx context.Context, command ActiveRecoveryCheckCommand) (bool, error) {
	if err := store.ready(ctx); err != nil {
		return false, err
	}
	if err := validateActiveRecoveryCheck(command); err != nil {
		return false, err
	}
	row, err := gormRawRow(ctx, store.database, `SELECT EXISTS (
		SELECT 1
		FROM ops.managed_ollama_operations operation
		JOIN ops.model_settings_state state
		  ON state.singleton=true AND state.active_revision=operation.target_revision
		JOIN ops.managed_ollama_runtime runtime
		  ON runtime.singleton=true AND runtime.owner_id=?::uuid AND runtime.owner_epoch=?
		 AND runtime.lease_expires_at>clock_timestamp()
		WHERE operation.operation_id=?::uuid AND operation.kind='active_recovery'
		  AND operation.terminal_at IS NULL
	)`, string(command.OwnerID), command.OwnerEpoch, string(command.OperationID))
	if err != nil {
		return false, gormStoreError(ctx, err)
	}
	var current bool
	if err := row.Scan(&current); err != nil {
		return false, gormStoreError(ctx, err)
	}
	return current, nil
}

func (store *GORMStore) ClaimOperation(ctx context.Context, command OperationClaimCommand) (OperationRecord, error) {
	if err := store.ready(ctx); err != nil {
		return OperationRecord{}, err
	}
	if err := validateOperationClaim(command); err != nil {
		return OperationRecord{}, err
	}
	required := marshalModelRefs(command.Requirement.Models)
	row, err := gormRawRow(ctx, store.database, `SELECT `+operationColumns+`
		FROM ops.managed_ollama_claim_operation(?,?,?,?,?::bigint,?::uuid,?,?::jsonb,?::uuid,?,?::interval)`,
		string(command.OperationID), string(command.Kind), command.IdempotencyKey, command.RequestHash,
		nullableInt64(command.TargetRevision), nullableID(command.RolloutID), command.Requirement.Hash, string(required),
		string(command.OwnerID), command.OwnerEpoch, intervalArg(command.LeaseDuration))
	if err != nil {
		return OperationRecord{}, gormStoreError(ctx, err)
	}
	operation, err := scanOperation(row)
	if gormNoRows(err) {
		return OperationRecord{}, conflict("local model runtime operation idempotency or ownership conflict")
	}
	return operation, gormStoreError(ctx, err)
}

func (store *GORMStore) BeginPullAttempt(ctx context.Context, command PullAttemptCommand) (PullAttemptResult, error) {
	if err := store.ready(ctx); err != nil {
		return PullAttemptResult{}, err
	}
	if err := validatePullAttempt(command); err != nil {
		return PullAttemptResult{}, err
	}
	row, err := gormRawRow(ctx, store.database, `SELECT `+operationColumns+`,
		GREATEST(0,(EXTRACT(EPOCH FROM (created_at+interval '6 hours'-clock_timestamp()))*1000000)::bigint)
		FROM ops.managed_ollama_begin_pull_attempt(?::uuid,?::uuid,?,?,?::interval)`,
		string(command.OperationID), string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion, intervalArg(command.LeaseDuration))
	if err != nil {
		return PullAttemptResult{}, gormStoreError(ctx, err)
	}
	attempt, err := scanPullAttempt(row)
	if gormNoRows(err) {
		return PullAttemptResult{}, conflict("local model runtime pull attempt CAS failed")
	}
	return attempt, gormStoreError(ctx, err)
}

func (store *GORMStore) RecordOperationProgress(ctx context.Context, command OperationProgressCommand) (OperationRecord, error) {
	if err := store.ready(ctx); err != nil {
		return OperationRecord{}, err
	}
	if err := validateOperationProgress(command); err != nil {
		return OperationRecord{}, err
	}
	completed := marshalModelRefs(command.CompletedModels)
	resolved := marshalResolvedModels(command.ResolvedModels)
	row, err := gormRawRow(ctx, store.database, `SELECT `+operationColumns+`
		FROM ops.managed_ollama_record_operation_progress(?::uuid,?::uuid,?,?,?,?::jsonb,?::jsonb,?,?,?,?::interval,?)`,
		string(command.OperationID), string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion,
		string(command.NextPhase), string(completed), string(resolved), command.CompletedBytes,
		nullableInt64(command.TotalBytes), command.ProgressKnown, intervalArg(command.LeaseDuration), string(command.ExpectedPhase))
	if err != nil {
		return OperationRecord{}, gormStoreError(ctx, err)
	}
	operation, err := scanOperation(row)
	if gormNoRows(err) {
		return OperationRecord{}, conflict("local model runtime operation progress CAS failed")
	}
	return operation, gormStoreError(ctx, err)
}

func (store *GORMStore) CompleteOperation(ctx context.Context, command OperationTerminalCommand) (OperationRecord, error) {
	if err := store.ready(ctx); err != nil {
		return OperationRecord{}, err
	}
	if err := validateOperationTerminal(command); err != nil {
		return OperationRecord{}, err
	}
	completed := marshalModelRefs(command.CompletedModels)
	row, err := gormRawRow(ctx, store.database, `SELECT `+operationColumns+`
		FROM ops.managed_ollama_complete_operation(
			?::uuid,?::uuid,?::bigint,?::bigint,?::text,?::text,?::boolean,?::jsonb,?::bigint,?::bigint
		)`,
		string(command.OperationID), string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion,
		string(command.Phase), nullableErrorCode(command.ErrorCode), command.Retryable, string(completed),
		command.CompletedBytes, nullableInt64(command.TotalBytes))
	if err != nil {
		return OperationRecord{}, gormStoreError(ctx, err)
	}
	operation, err := scanOperation(row)
	if gormNoRows(err) {
		return OperationRecord{}, conflict("local model runtime operation terminal CAS failed")
	}
	return operation, gormStoreError(ctx, err)
}

func (store *GORMStore) SweepExpiredOperations(ctx context.Context, command OperationExpirySweepCommand) (int64, error) {
	if err := store.ready(ctx); err != nil {
		return 0, err
	}
	if err := validateOperationExpirySweep(command); err != nil {
		return 0, err
	}
	row, err := gormRawRow(ctx, store.database, `SELECT ops.managed_ollama_expire_operations(?::uuid,?)`, string(command.OwnerID), command.OwnerEpoch)
	if err != nil {
		return 0, gormStoreError(ctx, err)
	}
	var expired int64
	if err := row.Scan(&expired); err != nil {
		return 0, gormStoreError(ctx, err)
	}
	return expired, nil
}

func (store *GORMStore) AcquireHold(ctx context.Context, command HoldAcquireCommand) (HoldRecord, error) {
	if err := store.ready(ctx); err != nil {
		return HoldRecord{}, err
	}
	if err := validateHoldAcquire(command); err != nil {
		return HoldRecord{}, err
	}
	models := marshalModelRefs(command.Requirement.Models)
	row, err := gormRawRow(ctx, store.database, `INSERT INTO ops.managed_ollama_holds(hold_id,owner_kind,owner_id,owner_epoch,role,instance_id,revision,rollout_id,operation_id,requirement_hash,models,lease_expires_at)
		VALUES(?::uuid,?,?::uuid,?,?,?,?,?::uuid,?::uuid,?,?::jsonb,clock_timestamp()+?::interval)
		ON CONFLICT(hold_id) DO UPDATE SET lease_expires_at=clock_timestamp()+?::interval,version=ops.managed_ollama_holds.version+1,updated_at=clock_timestamp()
		WHERE ops.managed_ollama_holds.owner_id=?::uuid AND ops.managed_ollama_holds.owner_epoch=? AND ops.managed_ollama_holds.requirement_hash=? AND ops.managed_ollama_holds.released_at IS NULL
		RETURNING `+holdColumns,
		string(command.HoldID), string(command.Kind), string(command.OwnerID), command.OwnerEpoch,
		nullableText(command.Role), nullableID(command.InstanceID), nullableInt64(command.Revision), nullableID(command.RolloutID), nullableID(command.OperationID), command.Requirement.Hash, string(models), intervalArg(command.LeaseDuration),
		intervalArg(command.LeaseDuration), string(command.OwnerID), command.OwnerEpoch, command.Requirement.Hash)
	if err != nil {
		return HoldRecord{}, gormStoreError(ctx, err)
	}
	hold, err := scanHold(row)
	if gormNoRows(err) {
		return HoldRecord{}, conflict("local model runtime hold ownership or identity conflict")
	}
	return hold, gormStoreError(ctx, err)
}

func (store *GORMStore) RenewHold(ctx context.Context, command HoldRenewCommand) (HoldRecord, error) {
	if err := store.ready(ctx); err != nil {
		return HoldRecord{}, err
	}
	if err := validateHoldRenew(command); err != nil {
		return HoldRecord{}, err
	}
	row, err := gormRawRow(ctx, store.database, `UPDATE ops.managed_ollama_holds SET lease_expires_at=clock_timestamp()+?::interval,version=version+1,updated_at=clock_timestamp()
		WHERE hold_id=?::uuid AND owner_id=?::uuid AND owner_epoch=? AND version=? AND released_at IS NULL RETURNING `+holdColumns,
		intervalArg(command.LeaseDuration), string(command.HoldID), string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion)
	if err != nil {
		return HoldRecord{}, gormStoreError(ctx, err)
	}
	hold, err := scanHold(row)
	if gormNoRows(err) {
		return HoldRecord{}, conflict("local model runtime hold renewal CAS failed")
	}
	return hold, gormStoreError(ctx, err)
}

func (store *GORMStore) ReleaseHold(ctx context.Context, command HoldReleaseCommand) (HoldRecord, error) {
	if err := store.ready(ctx); err != nil {
		return HoldRecord{}, err
	}
	if !validID(command.HoldID) || !validID(command.OwnerID) || command.OwnerEpoch <= 0 {
		return HoldRecord{}, errors.New("local model runtime hold release is invalid")
	}
	row, err := gormRawRow(ctx, store.database, `UPDATE ops.managed_ollama_holds SET released_at=COALESCE(released_at,clock_timestamp()),version=CASE WHEN released_at IS NULL THEN version+1 ELSE version END,updated_at=clock_timestamp()
		WHERE hold_id=?::uuid AND owner_id=?::uuid AND owner_epoch=? AND (?=0 OR version=?) RETURNING `+holdColumns,
		string(command.HoldID), string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion, command.ExpectedVersion)
	if err != nil {
		return HoldRecord{}, gormStoreError(ctx, err)
	}
	hold, err := scanHold(row)
	if gormNoRows(err) {
		return HoldRecord{}, conflict("local model runtime hold release CAS failed")
	}
	return hold, gormStoreError(ctx, err)
}

func (store *GORMStore) CompareAndSetRuntimePhase(ctx context.Context, command RuntimePhaseCommand) (RuntimeRecord, error) {
	if err := store.ready(ctx); err != nil {
		return RuntimeRecord{}, err
	}
	if err := validateRuntimeCommand(command); err != nil {
		return RuntimeRecord{}, err
	}
	row, err := gormRawRow(ctx, store.database, `SELECT `+runtimeColumns+`
		FROM ops.managed_ollama_compare_runtime_phase(?,?,?,?,?,?,?,?,?,?,?)`,
		string(command.OwnerID), command.OwnerEpoch, command.ExpectedVersion, string(command.ExpectedPhase), string(command.NextPhase), command.RequirementHash, command.ReadyHash, command.ChildEpoch, nullableErrorCode(command.ErrorCode), command.Retryable, command.ExpectedSettingsStateVersion)
	if err != nil {
		return RuntimeRecord{}, gormStoreError(ctx, err)
	}
	runtime, err := scanRuntime(row)
	if gormNoRows(err) {
		return RuntimeRecord{}, conflict("local model runtime phase CAS failed")
	}
	return runtime, gormStoreError(ctx, err)
}

func scanGORMHolds(ctx context.Context, database *gorm.DB) ([]HoldRecord, error) {
	rows, err := gormRawRows(ctx, database, `SELECT `+holdColumns+`
		FROM ops.managed_ollama_holds hold
		WHERE hold.released_at IS NULL AND hold.lease_expires_at>clock_timestamp()
		  AND NOT EXISTS (
		      SELECT 1 FROM ops.managed_ollama_operations operation
		      WHERE operation.operation_id=hold.operation_id AND operation.kind='active_recovery'
		        AND NOT EXISTS (SELECT 1 FROM ops.model_settings_state state
		                        WHERE state.singleton=true AND state.active_revision=operation.target_revision)
		  )
		ORDER BY hold.created_at,hold.hold_id`)
	if err != nil {
		return nil, err
	}
	var out []HoldRecord
	for rows.Next() {
		hold, scanErr := scanHold(rows)
		if scanErr != nil {
			closeErr := rows.Close()
			return nil, errors.Join(scanErr, closeErr)
		}
		out = append(out, hold)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil || closeErr != nil {
		return nil, errors.Join(rowsErr, closeErr)
	}
	return out, nil
}

func scanGORMOperations(ctx context.Context, database *gorm.DB) ([]OperationRecord, error) {
	rows, err := gormRawRows(ctx, database, `SELECT `+operationColumns+`
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
	var out []OperationRecord
	for rows.Next() {
		operation, scanErr := scanOperation(rows)
		if scanErr != nil {
			closeErr := rows.Close()
			return nil, errors.Join(scanErr, closeErr)
		}
		out = append(out, operation)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil || closeErr != nil {
		return nil, errors.Join(rowsErr, closeErr)
	}
	return out, nil
}

func readGORMSettingsDemandSources(ctx context.Context, database *gorm.DB, phase string, active int64, target, previous *int64) ([]DemandSource, error) {
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
		requirement, err := readGORMRevisionRequirement(ctx, database, candidate.revision)
		if err != nil {
			return nil, err
		}
		if len(requirement.Models) > 0 {
			sources = append(sources, DemandSource{Kind: candidate.kind, Revision: candidate.revision, Requirement: requirement})
		}
	}
	return sources, nil
}

func readGORMRevisionRequirement(ctx context.Context, database *gorm.DB, revision int64) (Requirement, error) {
	if revision == 0 {
		return NewRequirement(nil)
	}
	row, err := gormRawRow(ctx, database, `SELECT local_chat_model,local_embedding_model
		FROM ops.managed_ollama_revision_requirements WHERE revision=?`, revision)
	if err != nil {
		return Requirement{}, err
	}
	var chatModel, embeddingModel *string
	if err := row.Scan(&chatModel, &embeddingModel); err != nil {
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
