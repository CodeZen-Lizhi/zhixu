package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

// BeginRAGMemorySnapshot 原子创建 PREPARING reservation；只有 inserted=true 的调用方是 loader claimant。
func (r *Repository) BeginRAGMemorySnapshot(ctx context.Context, snapshot domain.RAGMemorySnapshot) (domain.RAGMemorySnapshot, bool, error) {
	if domain.ValidateRAGMemorySnapshot(snapshot) != nil || snapshot.Status != domain.RAGMemorySnapshotPreparing {
		return domain.RAGMemorySnapshot{}, false, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeMemorySnapshotInvalid, false, errors.New("rag memory snapshot begin is invalid"))
	}
	tag, err := r.db.Exec(ctx, `INSERT INTO agent.rag_memory_snapshot(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,claimant_id,
		owner_kind,owner_id,task_scope_id,status,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'PREPARING',$10,$11)
	ON CONFLICT (workspace_id,node_attempt_id) DO NOTHING`,
		string(snapshot.ID), string(snapshot.WorkspaceID), string(snapshot.WorkflowRunID), string(snapshot.NodeRunID),
		string(snapshot.NodeAttemptID), string(snapshot.ClaimantID), snapshot.OwnerKind, string(snapshot.OwnerID),
		string(snapshot.TaskScopeID), snapshot.CreatedAt.UTC(), snapshot.UpdatedAt.UTC())
	if err != nil {
		return domain.RAGMemorySnapshot{}, false, classify(err)
	}
	if tag.RowsAffected() == 1 {
		persisted, loadErr := loadRAGMemorySnapshotByID(ctx, r.db, snapshot.WorkspaceID, snapshot.ID, false)
		if loadErr != nil {
			return domain.RAGMemorySnapshot{}, false, classify(loadErr)
		}
		return persisted, true, nil
	}
	existing, err := loadRAGMemorySnapshotByAttempt(ctx, r.db, snapshot.WorkspaceID, snapshot.NodeAttemptID, false)
	if err != nil {
		return domain.RAGMemorySnapshot{}, false, classify(err)
	}
	if !sameRAGMemorySnapshotExecution(existing, snapshot) {
		return domain.RAGMemorySnapshot{}, false, consistency(errors.New("rag memory snapshot attempt binding differs"))
	}
	return existing, false, nil
}

// FinalizeRAGMemorySnapshotAndCreateModelRun 在一个事务内写 READY tuple、Model Run 与双向 binding。
func (r *Repository) FinalizeRAGMemorySnapshotAndCreateModelRun(ctx context.Context, command application.FinalizeRAGMemorySnapshotCommand) (domain.ModelRun, error) {
	run := command.Run
	if !validID(command.SnapshotID) || !validID(command.ClaimantID) || command.Context.Validate() != nil ||
		command.Context.SnapshotID != command.SnapshotID || run.MemoryContext != command.Context ||
		domain.ValidateModelRun(run) != nil || run.Status != domain.ModelRunRunning || run.Version != 1 {
		return domain.ModelRun{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeMemorySnapshotInvalid, false, errors.New("rag memory snapshot finalization is invalid"))
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.ModelRun{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	snapshot, err := loadRAGMemorySnapshotByID(ctx, tx, run.WorkspaceID, command.SnapshotID, true)
	if err != nil {
		return domain.ModelRun{}, classify(err)
	}
	if snapshot.Status != domain.RAGMemorySnapshotPreparing || snapshot.ClaimantID != command.ClaimantID ||
		snapshot.WorkflowRunID != run.WorkflowRunID || snapshot.NodeRunID != run.NodeRunID ||
		snapshot.NodeAttemptID != run.NodeAttemptID {
		return domain.ModelRun{}, consistency(errors.New("rag memory snapshot is not owned by this model run claimant"))
	}
	tag, err := tx.Exec(ctx, insertModelRunSQL, modelRunArgs(run)...)
	if err != nil {
		return domain.ModelRun{}, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ModelRun{}, consistency(errors.New("rag memory model run already exists"))
	}
	tag, err = tx.Exec(ctx, `UPDATE agent.rag_memory_snapshot SET
		status='READY',context_schema_version=$1,context_digest=$2,context_item_count=$3,context_bytes=$4,
		model_run_id=$5,updated_at=GREATEST(clock_timestamp(),created_at)
		WHERE workspace_id=$6 AND id=$7 AND claimant_id=$8 AND status='PREPARING'`,
		command.Context.SchemaVersion, command.Context.Digest, command.Context.ItemCount, command.Context.ByteCount,
		string(run.ID), string(run.WorkspaceID), string(command.SnapshotID), string(command.ClaimantID))
	if err != nil {
		return domain.ModelRun{}, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ModelRun{}, consistency(errors.New("rag memory snapshot could not become ready"))
	}
	persistedRun, err := loadModelRunByID(ctx, tx, run.WorkspaceID, run.ID)
	if err != nil {
		return domain.ModelRun{}, classify(err)
	}
	persistedSnapshot, err := loadRAGMemorySnapshotByID(ctx, tx, run.WorkspaceID, command.SnapshotID, false)
	if err != nil {
		return domain.ModelRun{}, classify(err)
	}
	if persistedSnapshot.Status != domain.RAGMemorySnapshotReady || persistedSnapshot.ModelRunID != run.ID ||
		persistedSnapshot.Context != command.Context || !sameModelRunCreateBinding(persistedRun, run) {
		return domain.ModelRun{}, consistency(errors.New("rag memory snapshot finalization readback differs"))
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ModelRun{}, classify(err)
	}
	return persistedRun, nil
}

// FailRAGMemorySnapshot 持久化 loader/canonicalization 的稳定失败，禁止同一 attempt 再次加载。
func (r *Repository) FailRAGMemorySnapshot(ctx context.Context, command application.FailRAGMemorySnapshotCommand) (domain.RAGMemorySnapshot, error) {
	if !validID(command.SnapshotID) || !validID(command.WorkspaceID) || !validID(command.ClaimantID) || !validStableErrorCode(command.ErrorCode) {
		return domain.RAGMemorySnapshot{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeMemorySnapshotInvalid, false, errors.New("rag memory snapshot failure is invalid"))
	}
	row := r.db.QueryRow(ctx, `UPDATE agent.rag_memory_snapshot SET
		status='FAILED',error_code=$1,updated_at=GREATEST(clock_timestamp(),created_at)
		WHERE workspace_id=$2 AND id=$3 AND claimant_id=$4 AND status='PREPARING'
		RETURNING `+ragMemorySnapshotColumns,
		command.ErrorCode, string(command.WorkspaceID), string(command.SnapshotID), string(command.ClaimantID))
	failed, err := scanRAGMemorySnapshot(row)
	if err == nil {
		return failed, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.RAGMemorySnapshot{}, classify(err)
	}
	existing, loadErr := loadRAGMemorySnapshotByID(ctx, r.db, command.WorkspaceID, command.SnapshotID, false)
	if loadErr != nil {
		return domain.RAGMemorySnapshot{}, classify(loadErr)
	}
	if existing.Status == domain.RAGMemorySnapshotFailed && existing.ClaimantID == command.ClaimantID && existing.ErrorCode == command.ErrorCode {
		return existing, nil
	}
	return domain.RAGMemorySnapshot{}, consistency(errors.New("rag memory snapshot failure binding differs"))
}

func scanRAGMemorySnapshot(row rowScanner) (domain.RAGMemorySnapshot, error) {
	var snapshot domain.RAGMemorySnapshot
	var id, workspaceID, workflowRunID, nodeRunID, nodeAttemptID, claimantID, ownerID, taskScopeID string
	var status string
	var schemaVersion, digest, modelRunID, errorCode *string
	var itemCount *int
	var byteCount *int64
	if err := row.Scan(
		&id, &workspaceID, &workflowRunID, &nodeRunID, &nodeAttemptID, &claimantID,
		&snapshot.OwnerKind, &ownerID, &taskScopeID, &status, &schemaVersion, &digest, &itemCount, &byteCount,
		&modelRunID, &errorCode, &snapshot.CreatedAt, &snapshot.UpdatedAt,
	); err != nil {
		return domain.RAGMemorySnapshot{}, err
	}
	snapshot.ID, snapshot.WorkspaceID = foundation.ID(id), foundation.ID(workspaceID)
	snapshot.WorkflowRunID, snapshot.NodeRunID = foundation.ID(workflowRunID), foundation.ID(nodeRunID)
	snapshot.NodeAttemptID, snapshot.ClaimantID = foundation.ID(nodeAttemptID), foundation.ID(claimantID)
	snapshot.OwnerID, snapshot.TaskScopeID = foundation.ID(ownerID), foundation.ID(taskScopeID)
	snapshot.Status = domain.RAGMemorySnapshotStatus(status)
	if schemaVersion != nil {
		snapshot.Context.SchemaVersion = *schemaVersion
		snapshot.Context.SnapshotID = snapshot.ID
	}
	if digest != nil {
		snapshot.Context.Digest = *digest
	}
	if itemCount != nil {
		snapshot.Context.ItemCount = *itemCount
	}
	if byteCount != nil {
		snapshot.Context.ByteCount = *byteCount
	}
	if modelRunID != nil {
		snapshot.ModelRunID = foundation.ID(*modelRunID)
	}
	if errorCode != nil {
		snapshot.ErrorCode = *errorCode
	}
	if err := domain.ValidateRAGMemorySnapshot(snapshot); err != nil {
		return domain.RAGMemorySnapshot{}, consistency(err)
	}
	return snapshot, nil
}

func loadRAGMemorySnapshotByID(ctx context.Context, queryer queryRower, workspaceID, snapshotID foundation.ID, forUpdate bool) (domain.RAGMemorySnapshot, error) {
	query := ragMemorySnapshotSelect + ` WHERE workspace_id=$1 AND id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanRAGMemorySnapshot(queryer.QueryRow(ctx, query, string(workspaceID), string(snapshotID)))
}

func loadRAGMemorySnapshotByAttempt(ctx context.Context, queryer queryRower, workspaceID, attemptID foundation.ID, forUpdate bool) (domain.RAGMemorySnapshot, error) {
	query := ragMemorySnapshotSelect + ` WHERE workspace_id=$1 AND node_attempt_id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanRAGMemorySnapshot(queryer.QueryRow(ctx, query, string(workspaceID), string(attemptID)))
}

func sameRAGMemorySnapshotExecution(left, right domain.RAGMemorySnapshot) bool {
	return left.WorkspaceID == right.WorkspaceID && left.WorkflowRunID == right.WorkflowRunID &&
		left.NodeRunID == right.NodeRunID && left.NodeAttemptID == right.NodeAttemptID &&
		left.OwnerKind == right.OwnerKind && left.OwnerID == right.OwnerID && left.TaskScopeID == right.TaskScopeID
}

func validStableErrorCode(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

const ragMemorySnapshotColumns = `
	id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,claimant_id::text,
	owner_kind,owner_id::text,task_scope_id::text,status,context_schema_version,context_digest,context_item_count,context_bytes,
	model_run_id::text,error_code,created_at,updated_at`

const ragMemorySnapshotSelect = `SELECT ` + ragMemorySnapshotColumns + ` FROM agent.rag_memory_snapshot`

var _ application.RAGMemorySnapshotRepository = (*Repository)(nil)
