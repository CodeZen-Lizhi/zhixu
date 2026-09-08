package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GORMGenerationRepository 在同一个 scope 中保存生成结果与 Agent Model Run 终态。
type GORMGenerationRepository struct {
	persistence *GORMRepository
	agent       agentapp.ScopedModelRunFinalizer
}

// NewGORMGenerationRepository 复用同池 UnitOfWork，注入 Agent 的稳定 scoped Port。
func NewGORMGenerationRepository(pool *platformpostgres.Pool, agent agentapp.ScopedModelRunFinalizer) (*GORMGenerationRepository, error) {
	if isNilInterface(agent) {
		return nil, generationUnavailable(errors.New("organizing generation agent store is unavailable"))
	}
	persistence, err := NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	return &GORMGenerationRepository{persistence: persistence, agent: agent}, nil
}

func (repository *GORMGenerationRepository) ready(ctx context.Context) bool {
	return repository != nil && !isNilInterface(repository.agent) && repository.persistence.validateReady(ctx) == nil
}

// LookupReady 在 Provider 调用前恢复原始严格 JSON 字节。
func (repository *GORMGenerationRepository) LookupReady(ctx context.Context, workspaceID, nodeRunID foundation.ID,
	kind organizingworkflow.GenerationKind, requestHash string,
) (organizingworkflow.GenerationRecord, bool, error) {
	if !repository.ready(ctx) || !postgresID(workspaceID) || !postgresID(nodeRunID) || !generationKind(kind) || !lowerHash(requestHash) {
		return organizingworkflow.GenerationRecord{}, false, generationInvalid(errors.New("organizing ready generation lookup is invalid"))
	}
	record, found, err := gormLoadGeneration(repository.persistence.database.WithContext(ctx).
		Where("workspace_id = ? AND node_run_id = ? AND status = 'READY'", string(workspaceID), string(nodeRunID)), false)
	if err != nil || !found {
		return organizingworkflow.GenerationRecord{}, found, err
	}
	if record.Kind != kind || record.RequestHash != requestHash {
		return organizingworkflow.GenerationRecord{}, false, generationInconsistent(errors.New("ready organizing generation is bound to another request"))
	}
	return record, true, nil
}

// Prepare 在节点锁下创建唯一 Attempt fence，并阻止未决 Provider 调用重入。
func (repository *GORMGenerationRepository) Prepare(ctx context.Context, command organizingworkflow.PrepareGenerationCommand) (organizingworkflow.GenerationRecord, bool, error) {
	record := command.Record
	if !repository.ready(ctx) || record.Validate() != nil || record.Status != organizingworkflow.GenerationRunning || record.Version != 1 || record.ModelRunID != "" {
		return organizingworkflow.GenerationRecord{}, false, generationInvalid(errors.New("organizing generation preparation is invalid"))
	}
	var result organizingworkflow.GenerationRecord
	var replayed bool
	err := repository.persistence.within(ctx, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, "organizing-generation:"+string(record.NodeRunID)).Error; err != nil {
			return classifyGORM(err)
		}
		existing, found, err := gormLoadGeneration(tx.Where("workspace_id = ? AND node_attempt_id = ?", string(record.WorkspaceID), string(record.NodeAttemptID)), true)
		if err != nil {
			return err
		}
		if found {
			if !sameGenerationCreate(existing, record) {
				return generationConflict(errors.New("organizing node attempt is bound to another generation request"))
			}
			result, replayed = existing, true
			return nil
		}
		other, found, err := gormLoadGeneration(tx.Where("workspace_id = ? AND node_run_id = ? AND status IN ('RUNNING','READY')", string(record.WorkspaceID), string(record.NodeRunID)).
			Order("CASE status WHEN 'READY' THEN 0 ELSE 1 END,created_at,id"), true)
		if err != nil {
			return err
		}
		if found {
			if other.Status == organizingworkflow.GenerationReady && other.Kind == record.Kind && other.RequestHash == record.RequestHash {
				result, replayed = other, true
				return nil
			}
			return generationRecovery(errors.New("another organizing generation for this node is not safely replayable"))
		}
		model := generationModel{ID: string(record.ID), WorkspaceID: string(record.WorkspaceID), SnapshotID: string(record.SnapshotID), WorkflowRunID: string(record.WorkflowRunID),
			NodeRunID: string(record.NodeRunID), NodeAttemptID: string(record.NodeAttemptID), GenerationKind: string(record.Kind), RequestHash: record.RequestHash,
			ModelSettingsRevision: record.ModelSettingsRevision, Status: "RUNNING", Version: 1, CreatedAt: record.CreatedAt.UTC(), UpdatedAt: record.CreatedAt.UTC()}
		if err := tx.Create(&model).Error; err != nil {
			return classifyGORM(err)
		}
		result = record
		return nil
	})
	if err != nil {
		return organizingworkflow.GenerationRecord{}, false, err
	}
	return result, replayed, nil
}

// BindModelRun 在 Provider 调用前把 Generation 精确绑定到当前 Agent Model Run。
func (repository *GORMGenerationRepository) BindModelRun(ctx context.Context, command organizingworkflow.BindGenerationModelRunCommand) (organizingworkflow.GenerationRecord, error) {
	if !repository.ready(ctx) || !postgresID(command.WorkspaceID) || !postgresID(command.GenerationID) || !postgresID(command.ModelRunID) || command.ExpectedVersion < 1 {
		return organizingworkflow.GenerationRecord{}, generationInvalid(errors.New("organizing generation model run binding is invalid"))
	}
	var result organizingworkflow.GenerationRecord
	err := repository.persistence.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		record, err := gormGenerationByID(tx, command.WorkspaceID, command.GenerationID)
		if err != nil {
			return err
		}
		replayed := record.ModelRunID == command.ModelRunID && record.Status == organizingworkflow.GenerationRunning
		if !replayed && (record.Status != organizingworkflow.GenerationRunning || record.Version != command.ExpectedVersion || record.ModelRunID != "") {
			return generationConflict(errors.New("organizing generation cannot bind this model run"))
		}
		run, err := repository.agent.GetModelRunScoped(ctx, scope, command.WorkspaceID, command.ModelRunID, true)
		if err != nil {
			return err
		}
		if run.WorkflowRunID != record.WorkflowRunID || run.NodeRunID != record.NodeRunID || run.NodeAttemptID != record.NodeAttemptID ||
			run.Status != agentdomain.ModelRunRunning || run.Version != 1 || !sameOptionalRevision(run.ModelSettingsRevision, record.ModelSettingsRevision) || !generationRuntimeMatches(run, record.Kind) {
			return generationInconsistent(errors.New("agent model run differs from the organizing generation fence"))
		}
		if replayed {
			result = record
			return nil
		}
		updated := tx.Model(&generationModel{}).Where("id = ? AND workspace_id = ? AND status = 'RUNNING' AND model_run_id IS NULL AND version = ?", string(record.ID), string(record.WorkspaceID), record.Version).
			Updates(map[string]any{"model_run_id": string(command.ModelRunID), "version": record.Version + 1, "updated_at": gorm.Expr("GREATEST(updated_at,?)", run.UpdatedAt.UTC())})
		if updated.Error != nil {
			return classifyGORM(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return generationConflict(errors.New("organizing generation model run CAS failed"))
		}
		record.ModelRunID, record.Version = command.ModelRunID, record.Version+1
		if run.UpdatedAt.After(record.UpdatedAt) {
			record.UpdatedAt = run.UpdatedAt.UTC()
		}
		if err := record.Validate(); err != nil {
			return generationInconsistent(err)
		}
		result = record
		return nil
	})
	if err != nil {
		return organizingworkflow.GenerationRecord{}, err
	}
	return result, nil
}

// Complete 原子保存原始输出字节并终结 Model Run，任何一侧失败均回滚。
func (repository *GORMGenerationRepository) Complete(ctx context.Context, command organizingworkflow.CompleteGenerationCommand) (organizingworkflow.GenerationRecord, bool, error) {
	if !repository.ready(ctx) || !validCompleteGeneration(command) {
		return organizingworkflow.GenerationRecord{}, false, generationInvalid(errors.New("organizing generation completion is invalid"))
	}
	var result organizingworkflow.GenerationRecord
	var replayed bool
	err := repository.persistence.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		record, err := gormGenerationByID(tx, command.WorkspaceID, command.GenerationID)
		if err != nil {
			return err
		}
		if !generationResultMatches(record.Kind, command.ResultType) {
			return generationInconsistent(errors.New("organizing generation result type differs from its kind"))
		}
		if record.Status == organizingworkflow.GenerationReady {
			if record.ModelRunID != command.ModelRunID || record.OutputHash != command.OutputHash || !bytes.Equal(record.Output, command.Output) {
				return generationConflict(errors.New("organizing generation completion conflicts with the committed output"))
			}
			result, replayed = record, true
			return nil
		}
		if record.Status != organizingworkflow.GenerationRunning || record.Version != command.ExpectedVersion || record.ModelRunID != command.ModelRunID {
			return generationConflict(errors.New("organizing generation is not completable"))
		}
		modelRecord, err := repository.agent.GetModelRunRecordScoped(ctx, scope, command.WorkspaceID, command.ModelRunID, true)
		if err != nil {
			return err
		}
		if err := validateGenerationModelRecord(modelRecord, record, command.ExpectedModelRunVersion, command.OutputHash); err != nil {
			return err
		}
		finalRun := modelRecord.Run
		finalRun.Status, finalRun.FinalResultType, finalRun.FinalErrorCode = agentdomain.ModelRunSucceeded, command.ResultType, ""
		finalRun.Version++
		finalRun.UpdatedAt, finalRun.CompletedAt = command.CompletedAt.UTC(), timePointer(command.CompletedAt.UTC())
		if _, _, err := repository.agent.FinalizeModelRunScoped(ctx, scope, agentapp.FinalizeModelRunCommand{ExpectedVersion: command.ExpectedModelRunVersion, Run: finalRun}); err != nil {
			return err
		}
		next := record
		next.Status, next.Output, next.OutputHash, next.Retryable = organizingworkflow.GenerationReady, append(json.RawMessage(nil), command.Output...), command.OutputHash, false
		next.Version++
		next.UpdatedAt, next.CompletedAt = command.CompletedAt.UTC(), timePointer(command.CompletedAt.UTC())
		if err := next.Validate(); err != nil {
			return generationInconsistent(err)
		}
		updated := tx.Model(&generationModel{}).Where("id = ? AND workspace_id = ? AND status = 'RUNNING' AND model_run_id = ? AND version = ?",
			string(record.ID), string(record.WorkspaceID), string(command.ModelRunID), record.Version).
			Updates(map[string]any{"status": "READY", "output_document": []byte(command.Output), "output_hash": command.OutputHash, "error_code": nil,
				"retryable": false, "version": next.Version, "updated_at": command.CompletedAt.UTC(), "completed_at": command.CompletedAt.UTC()})
		if updated.Error != nil {
			return classifyGORM(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return generationConflict(errors.New("organizing generation completion CAS failed"))
		}
		result = next
		return nil
	})
	if err != nil {
		return organizingworkflow.GenerationRecord{}, false, err
	}
	return result, replayed, nil
}

// Fail 原子保存失败/不确定状态并终结对应 Agent 事实。
func (repository *GORMGenerationRepository) Fail(ctx context.Context, command organizingworkflow.FailGenerationCommand) error {
	if !repository.ready(ctx) || !validFailGeneration(command) {
		return generationInvalid(errors.New("organizing generation failure is invalid"))
	}
	return repository.persistence.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		record, err := gormGenerationByID(tx, command.WorkspaceID, command.GenerationID)
		if err != nil {
			return err
		}
		expectedStatus := organizingworkflow.GenerationFailed
		if command.ModelRunStatus == agentdomain.ModelRunUnknown {
			expectedStatus = organizingworkflow.GenerationRecoveryRequired
		}
		if record.Status == expectedStatus && record.ModelRunID == command.ModelRunID && record.ErrorCode == command.ErrorCode && record.Retryable == command.Retryable {
			return nil
		}
		if record.Status != organizingworkflow.GenerationRunning || record.Version != command.ExpectedVersion || record.ModelRunID != command.ModelRunID {
			return generationConflict(errors.New("organizing generation is not fail-able"))
		}
		modelRecord, err := repository.agent.GetModelRunRecordScoped(ctx, scope, command.WorkspaceID, command.ModelRunID, true)
		if err != nil {
			return err
		}
		if !sameGenerationModelBinding(modelRecord.Run, record) || !generationRuntimeMatches(modelRecord.Run, record.Kind) || modelRecord.Run.Status != agentdomain.ModelRunRunning || modelRecord.Run.Version != command.ExpectedModelRunVersion {
			return generationInconsistent(errors.New("organizing failure model run binding is invalid"))
		}
		finalRun := modelRecord.Run
		finalRun.Status, finalRun.FinalResultType, finalRun.FinalErrorCode = command.ModelRunStatus, "", command.ErrorCode
		finalRun.Version++
		finalRun.UpdatedAt, finalRun.CompletedAt = command.CompletedAt.UTC(), timePointer(command.CompletedAt.UTC())
		if _, _, err := repository.agent.FinalizeModelRunScoped(ctx, scope, agentapp.FinalizeModelRunCommand{ExpectedVersion: command.ExpectedModelRunVersion, Run: finalRun}); err != nil {
			return err
		}
		updated := tx.Model(&generationModel{}).Where("id = ? AND workspace_id = ? AND status = 'RUNNING' AND model_run_id = ? AND version = ?",
			string(record.ID), string(record.WorkspaceID), string(command.ModelRunID), record.Version).
			Updates(map[string]any{"status": string(expectedStatus), "output_document": nil, "output_hash": nil, "error_code": command.ErrorCode,
				"retryable": command.Retryable, "version": gorm.Expr("version+1"), "updated_at": command.CompletedAt.UTC(), "completed_at": command.CompletedAt.UTC()})
		if updated.Error != nil {
			return classifyGORM(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return generationConflict(errors.New("organizing generation failure CAS failed"))
		}
		return nil
	})
}

func gormGenerationByID(tx *gorm.DB, workspaceID, generationID foundation.ID) (organizingworkflow.GenerationRecord, error) {
	record, found, err := gormLoadGeneration(tx.Where("workspace_id = ? AND id = ?", string(workspaceID), string(generationID)), true)
	if err != nil {
		return organizingworkflow.GenerationRecord{}, err
	}
	if !found {
		return organizingworkflow.GenerationRecord{}, generationNotFound(gorm.ErrRecordNotFound)
	}
	return record, nil
}

func gormLoadGeneration(query *gorm.DB, lock bool) (organizingworkflow.GenerationRecord, bool, error) {
	query = query.Select(gormGenerationColumns)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var model generationModel
	if err := query.Take(&model).Error; err != nil {
		if gormNoRows(err) {
			return organizingworkflow.GenerationRecord{}, false, nil
		}
		return organizingworkflow.GenerationRecord{}, false, classifyGORM(err)
	}
	record := organizingworkflow.GenerationRecord{ID: foundation.ID(model.ID), WorkspaceID: foundation.ID(model.WorkspaceID), SnapshotID: foundation.ID(model.SnapshotID),
		WorkflowRunID: foundation.ID(model.WorkflowRunID), NodeRunID: foundation.ID(model.NodeRunID), NodeAttemptID: foundation.ID(model.NodeAttemptID),
		Kind: organizingworkflow.GenerationKind(model.GenerationKind), RequestHash: model.RequestHash, ModelSettingsRevision: model.ModelSettingsRevision,
		ModelRunID: foundation.ID(stringValue(model.ModelRunID)), Status: organizingworkflow.GenerationStatus(model.Status), Output: append(json.RawMessage(nil), model.OutputDocument...),
		OutputHash: stringValue(model.OutputHash), ErrorCode: stringValue(model.ErrorCode), Retryable: model.Retryable, Version: model.Version,
		CreatedAt: model.CreatedAt.UTC(), UpdatedAt: model.UpdatedAt.UTC(), CompletedAt: model.CompletedAt}
	if record.CompletedAt != nil {
		record.CompletedAt = timePointer(record.CompletedAt.UTC())
	}
	if err := record.Validate(); err != nil {
		return organizingworkflow.GenerationRecord{}, false, generationInconsistent(err)
	}
	return record, true, nil
}

var _ organizingworkflow.GenerationStore = (*GORMGenerationRepository)(nil)
