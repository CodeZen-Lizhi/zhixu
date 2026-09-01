package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

const gormMemorySnapshotResultUnknownCode = "AGENT_MEMORY_SNAPSHOT_RESULT_UNKNOWN"

// BeginRAGMemorySnapshot atomically creates a PREPARING reservation. Only the
// caller that inserted it is allowed to load the effective Memory context.
func (repository *GORMRepository) BeginRAGMemorySnapshot(
	ctx context.Context,
	snapshot domain.RAGMemorySnapshot,
) (domain.RAGMemorySnapshot, bool, error) {
	if err := repository.readyRAGMemorySnapshot(ctx); err != nil {
		return domain.RAGMemorySnapshot{}, false, err
	}
	if domain.ValidateRAGMemorySnapshot(snapshot) != nil || snapshot.Status != domain.RAGMemorySnapshotPreparing {
		return domain.RAGMemorySnapshot{}, false, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeMemorySnapshotInvalid,
			false,
			errors.New("rag memory snapshot begin is invalid"),
		)
	}
	result := repository.database.WithContext(ctx).Exec(`INSERT INTO agent.rag_memory_snapshot(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,claimant_id,
		owner_kind,owner_id,task_scope_id,status,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,'PREPARING',?,?)
	ON CONFLICT (workspace_id,node_attempt_id) DO NOTHING`,
		string(snapshot.ID), string(snapshot.WorkspaceID), string(snapshot.WorkflowRunID), string(snapshot.NodeRunID),
		string(snapshot.NodeAttemptID), string(snapshot.ClaimantID), snapshot.OwnerKind, string(snapshot.OwnerID),
		string(snapshot.TaskScopeID), snapshot.CreatedAt.UTC(), snapshot.UpdatedAt.UTC(),
	)
	if result.Error != nil {
		return domain.RAGMemorySnapshot{}, false, classifyGORM(ctx, result.Error)
	}
	if result.RowsAffected == 1 {
		persisted, err := loadGORMRAGMemorySnapshotByID(ctx, repository.database, snapshot.WorkspaceID, snapshot.ID, false)
		if err != nil {
			return domain.RAGMemorySnapshot{}, false, classifyGORM(ctx, err)
		}
		return persisted, true, nil
	}
	existing, err := loadGORMRAGMemorySnapshotByAttempt(ctx, repository.database, snapshot.WorkspaceID, snapshot.NodeAttemptID, false)
	if err != nil {
		return domain.RAGMemorySnapshot{}, false, classifyGORM(ctx, err)
	}
	if !sameRAGMemorySnapshotExecution(existing, snapshot) {
		return domain.RAGMemorySnapshot{}, false, consistency(errors.New("rag memory snapshot attempt binding differs"))
	}
	return existing, false, nil
}

// FinalizeRAGMemorySnapshotAndCreateModelRun closes a claimed snapshot and
// creates its initial RUNNING Model Run in one Store-owned transaction.
func (repository *GORMRepository) FinalizeRAGMemorySnapshotAndCreateModelRun(
	ctx context.Context,
	command application.FinalizeRAGMemorySnapshotCommand,
) (domain.ModelRun, error) {
	if err := repository.readyRAGMemorySnapshot(ctx); err != nil {
		return domain.ModelRun{}, err
	}
	run := command.Run
	if !validID(command.SnapshotID) || !validID(command.ClaimantID) || command.Context.Validate() != nil ||
		command.Context.SnapshotID != command.SnapshotID || run.MemoryContext != command.Context ||
		domain.ValidateModelRun(run) != nil || run.Status != domain.ModelRunRunning || run.Version != 1 {
		return domain.ModelRun{}, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeMemorySnapshotInvalid,
			false,
			errors.New("rag memory snapshot finalization is invalid"),
		)
	}

	var persistedRun domain.ModelRun
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(
		callbackCtx context.Context,
		transaction *gorm.DB,
		_ foundation.TransactionScope,
	) error {
		snapshot, err := loadGORMRAGMemorySnapshotByID(callbackCtx, transaction, run.WorkspaceID, command.SnapshotID, true)
		if err != nil {
			return classifyGORM(callbackCtx, err)
		}
		if snapshot.Status != domain.RAGMemorySnapshotPreparing || snapshot.ClaimantID != command.ClaimantID ||
			snapshot.WorkflowRunID != run.WorkflowRunID || snapshot.NodeRunID != run.NodeRunID ||
			snapshot.NodeAttemptID != run.NodeAttemptID {
			return consistency(errors.New("rag memory snapshot is not owned by this model run claimant"))
		}

		result := transaction.WithContext(callbackCtx).Exec(gormInsertModelRunSQL, modelRunArgs(run)...)
		if result.Error != nil {
			return classifyGORM(callbackCtx, result.Error)
		}
		if result.RowsAffected != 1 {
			return consistency(errors.New("rag memory model run already exists"))
		}

		result = transaction.WithContext(callbackCtx).Exec(`UPDATE agent.rag_memory_snapshot SET
			status='READY',context_schema_version=?,context_digest=?,context_item_count=?,context_bytes=?,
			model_run_id=?,updated_at=GREATEST(clock_timestamp(),created_at)
			WHERE workspace_id=? AND id=? AND claimant_id=? AND status='PREPARING'`,
			command.Context.SchemaVersion, command.Context.Digest, command.Context.ItemCount, command.Context.ByteCount,
			string(run.ID), string(run.WorkspaceID), string(command.SnapshotID), string(command.ClaimantID),
		)
		if result.Error != nil {
			return classifyGORM(callbackCtx, result.Error)
		}
		if result.RowsAffected != 1 {
			return consistency(errors.New("rag memory snapshot could not become ready"))
		}

		persistedRun, err = loadGORMModelRunByID(callbackCtx, transaction, run.WorkspaceID, run.ID, false)
		if err != nil {
			return classifyGORM(callbackCtx, err)
		}
		persistedSnapshot, err := loadGORMRAGMemorySnapshotByID(callbackCtx, transaction, run.WorkspaceID, command.SnapshotID, false)
		if err != nil {
			return classifyGORM(callbackCtx, err)
		}
		if persistedSnapshot.Status != domain.RAGMemorySnapshotReady || persistedSnapshot.ModelRunID != run.ID ||
			persistedSnapshot.Context != command.Context || !sameModelRunCreateBinding(persistedRun, run) {
			return consistency(errors.New("rag memory snapshot finalization readback differs"))
		}
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		if callbackSucceeded {
			return domain.ModelRun{}, foundation.NewError(
				foundation.ErrorManualRecoveryRequired,
				gormMemorySnapshotResultUnknownCode,
				false,
				gormMemorySnapshotCause(ctx, err),
			)
		}
		return domain.ModelRun{}, classifyGORM(ctx, err)
	}
	return persistedRun, nil
}

// FailRAGMemorySnapshot persists an exact terminal loader failure for a
// claimant, so later attempts cannot silently reload a different context.
func (repository *GORMRepository) FailRAGMemorySnapshot(
	ctx context.Context,
	command application.FailRAGMemorySnapshotCommand,
) (domain.RAGMemorySnapshot, error) {
	if err := repository.readyRAGMemorySnapshot(ctx); err != nil {
		return domain.RAGMemorySnapshot{}, err
	}
	if !validID(command.SnapshotID) || !validID(command.WorkspaceID) || !validID(command.ClaimantID) ||
		!validStableErrorCode(command.ErrorCode) {
		return domain.RAGMemorySnapshot{}, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeMemorySnapshotInvalid,
			false,
			errors.New("rag memory snapshot failure is invalid"),
		)
	}
	row, err := gormRawRow(repository.database.WithContext(ctx), `UPDATE agent.rag_memory_snapshot SET
		status='FAILED',error_code=?,updated_at=GREATEST(clock_timestamp(),created_at)
		WHERE workspace_id=? AND id=? AND claimant_id=? AND status='PREPARING'
		RETURNING `+ragMemorySnapshotColumns,
		command.ErrorCode, string(command.WorkspaceID), string(command.SnapshotID), string(command.ClaimantID),
	)
	if err != nil {
		return domain.RAGMemorySnapshot{}, classifyGORM(ctx, err)
	}
	failed, err := scanRAGMemorySnapshot(row)
	if err == nil {
		return failed, nil
	}
	if !gormNoRows(err) {
		return domain.RAGMemorySnapshot{}, classifyGORM(ctx, err)
	}
	existing, err := loadGORMRAGMemorySnapshotByID(ctx, repository.database, command.WorkspaceID, command.SnapshotID, false)
	if err != nil {
		return domain.RAGMemorySnapshot{}, classifyGORM(ctx, err)
	}
	if existing.Status == domain.RAGMemorySnapshotFailed && existing.ClaimantID == command.ClaimantID && existing.ErrorCode == command.ErrorCode {
		return existing, nil
	}
	return domain.RAGMemorySnapshot{}, consistency(errors.New("rag memory snapshot failure binding differs"))
}

func loadGORMRAGMemorySnapshotByID(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	snapshotID foundation.ID,
	forUpdate bool,
) (domain.RAGMemorySnapshot, error) {
	query := gormRAGMemorySnapshotSelect + ` WHERE workspace_id=? AND id=?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormRawRow(database.WithContext(ctx), query, string(workspaceID), string(snapshotID))
	if err != nil {
		return domain.RAGMemorySnapshot{}, err
	}
	return scanRAGMemorySnapshot(row)
}

func loadGORMRAGMemorySnapshotByAttempt(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	attemptID foundation.ID,
	forUpdate bool,
) (domain.RAGMemorySnapshot, error) {
	query := gormRAGMemorySnapshotSelect + ` WHERE workspace_id=? AND node_attempt_id=?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormRawRow(database.WithContext(ctx), query, string(workspaceID), string(attemptID))
	if err != nil {
		return domain.RAGMemorySnapshot{}, err
	}
	return scanRAGMemorySnapshot(row)
}

const gormRAGMemorySnapshotSelect = `SELECT ` + ragMemorySnapshotColumns + ` FROM agent.rag_memory_snapshot`

func gormMemorySnapshotCause(ctx context.Context, cause error) error {
	if contextCause := agentContextCause(ctx, cause); contextCause != nil {
		return contextCause
	}
	return cause
}

func (repository *GORMRepository) readyRAGMemorySnapshot(ctx context.Context) error {
	if repository == nil || !validAgentGORMDatabase(repository.database) || nilAgentDependency(repository.unitOfWork) {
		return gormUnavailable(errors.New("agent GORM repository is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeMemorySnapshotInvalid,
			false,
			errors.New("rag memory snapshot context is nil"),
		)
	}
	return nil
}

var _ application.RAGMemorySnapshotRepository = (*GORMRepository)(nil)
