package postgres

import (
	"bytes"
	"context"
	"errors"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GORMGoalSelectionStore struct {
	*GORMGoalSelectionResultReader
	goals app.SynthesisGoalRequestStore
	runs  agentapp.ScopedModelRunFinalizer
	fence workflowapp.ScopedWorkspaceAnalysisExecutionFence
}

func NewGORMGoalSelectionStore(pool *platformpostgres.Pool, goals app.SynthesisGoalRequestStore, snapshots app.SynthesisGoalCatalogSnapshotReader, runs agentapp.ScopedModelRunFinalizer) (*GORMGoalSelectionStore, error) {
	if pool == nil || isNilInterface(goals) || isNilInterface(snapshots) || isNilInterface(runs) {
		return nil, synthesisUnavailable(errors.New("goal selection dependencies are required"))
	}
	db, err := pool.GORM()
	if err != nil {
		return nil, err
	}
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(pool)
	if err != nil {
		return nil, err
	}
	return &GORMGoalSelectionStore{GORMGoalSelectionResultReader: &GORMGoalSelectionResultReader{db: db, uow: uow, snapshots: snapshots}, goals: goals, runs: runs, fence: fence}, nil
}

type goalSelectionManifestModel struct {
	BatchID, WorkspaceID string
	SelectionCount       int
	Sealed               bool
	CreatedAt            time.Time `gorm:"autoCreateTime:false"`
}

func (goalSelectionManifestModel) TableName() string {
	return "organizing.synthesis_goal_selection_manifest"
}

type goalSelectionModel struct {
	ID, WorkspaceID, RequestID, CatalogBatchID                   string
	SourceOrdinal, PointOffset, PointCount                       int
	PayloadHash, Status                                          string
	ScheduledWorkflowID, WorkflowRunID, NodeRunID, NodeAttemptID *string
	ModelInputHash, ModelRunID                                   *string
	ModelOutput                                                  []byte
	ErrorCode                                                    *string
	Retryable                                                    bool
	Version                                                      int64
	CreatedAt                                                    time.Time `gorm:"autoCreateTime:false"`
	UpdatedAt                                                    time.Time `gorm:"autoUpdateTime:false"`
}

func (goalSelectionModel) TableName() string { return "organizing.synthesis_goal_selection" }
func (r goalSelectionModel) selection() app.SynthesisGoalSelection {
	return app.SynthesisGoalSelection{ID: foundation.ID(r.ID), WorkspaceID: foundation.ID(r.WorkspaceID), RequestID: foundation.ID(r.RequestID), CatalogBatchID: foundation.ID(r.CatalogBatchID), SourceOrdinal: r.SourceOrdinal, PointOffset: r.PointOffset, PointCount: r.PointCount, PayloadHash: r.PayloadHash, Status: app.GoalSelectionStatus(r.Status), ScheduledWorkflowID: foundation.ID(stringValue(r.ScheduledWorkflowID)), WorkflowRunID: foundation.ID(stringValue(r.WorkflowRunID)), NodeRunID: foundation.ID(stringValue(r.NodeRunID)), NodeAttemptID: foundation.ID(stringValue(r.NodeAttemptID)), ModelInputHash: stringValue(r.ModelInputHash), ModelRunID: foundation.ID(stringValue(r.ModelRunID)), ModelOutput: bytes.Clone(r.ModelOutput), ErrorCode: stringValue(r.ErrorCode), Retryable: r.Retryable, Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}
func (s *GORMGoalSelectionStore) within(ctx context.Context, work func(context.Context, foundation.TransactionScope, *gorm.DB) error) error {
	if s == nil || s.db == nil || ctx == nil {
		return goalInvalid()
	}
	return synthesisDBError(ctx, s.uow.Within(ctx, foundation.TransactionOptions{}, func(c context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		return work(c, scope, tx.WithContext(c))
	}))
}
func (s *GORMGoalSelectionStore) PrepareGoalSelections(ctx context.Context, w, requestID foundation.ID, batchNo int64) ([]app.SynthesisGoalSelection, error) {
	if s == nil || ctx == nil || !validID(w) || !validID(requestID) || batchNo < 1 {
		return nil, goalInvalid()
	}
	request, err := s.goals.GetSynthesisGoal(ctx, w, requestID)
	if err != nil {
		return nil, err
	}
	batch, err := s.goals.GetSynthesisGoalCatalogBatch(ctx, w, requestID, batchNo)
	if err != nil {
		return nil, err
	}
	var manifest goalSelectionManifestModel
	err = s.db.WithContext(ctx).Where("batch_id=? AND workspace_id=?", string(batch.ID), string(w)).Take(&manifest).Error
	if err == nil {
		return readGoalSelections(s.db.WithContext(ctx), w, batch.ID, manifest.SelectionCount)
	}
	if !gormNoRows(err) {
		return nil, err
	}
	items, err := s.snapshots.ReadSynthesisGoalCatalogSnapshot(ctx, batch)
	if err != nil {
		return nil, err
	}
	inputs, err := app.BuildGoalSelectionInputs(request, batch, items)
	if err != nil {
		return nil, err
	}
	rows := make([]goalSelectionModel, 0, len(inputs))
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, input := range inputs {
		payload, err := app.BuildGoalSelectionPayload(input)
		if err != nil {
			return nil, err
		}
		id, err := foundation.NewUUIDGenerator(nil).New()
		if err != nil {
			return nil, err
		}
		rows = append(rows, goalSelectionModel{ID: string(id), WorkspaceID: string(w), RequestID: string(requestID), CatalogBatchID: string(batch.ID), SourceOrdinal: input.SourceOrdinal, PointOffset: input.PointOffset, PointCount: len(input.Points), PayloadHash: sha256Hex(payload), Status: string(app.GoalSelectionPending), Version: 1, CreatedAt: now, UpdatedAt: now})
	}
	var result []app.SynthesisGoalSelection
	err = s.within(ctx, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		// 同一个不可变批次行将竞争清单的操作串行化；加锁后重新读取。
		var locked synthesisGoalBatchModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND workspace_id=?", string(batch.ID), string(w)).Take(&locked).Error; err != nil {
			return err
		}
		var existing goalSelectionManifestModel
		err := tx.Where("batch_id=? AND workspace_id=?", string(batch.ID), string(w)).Take(&existing).Error
		if err == nil {
			result, err = readGoalSelections(tx, w, batch.ID, existing.SelectionCount)
			return err
		}
		if !gormNoRows(err) {
			return err
		}
		manifest := goalSelectionManifestModel{BatchID: string(batch.ID), WorkspaceID: string(w), SelectionCount: len(rows), CreatedAt: now}
		if err := tx.Create(&manifest).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 128).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&goalSelectionManifestModel{}).Where("batch_id=? AND workspace_id=?", string(batch.ID), string(w)).Update("sealed", true).Error; err != nil {
			return err
		}
		result, err = readGoalSelections(tx, w, batch.ID, len(rows))
		return err
	})
	return result, err
}
func readGoalSelections(tx *gorm.DB, w, batchID foundation.ID, count int) ([]app.SynthesisGoalSelection, error) {
	var rows []goalSelectionModel
	if err := tx.Where("workspace_id=? AND catalog_batch_id=?", string(w), string(batchID)).Order("source_ordinal,point_offset").Limit(16385).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != count || count > 16384 {
		return nil, goalConflict()
	}
	result := make([]app.SynthesisGoalSelection, len(rows))
	for i, row := range rows {
		result[i] = row.selection()
	}
	return result, nil
}
func loadGoalSelection(tx *gorm.DB, w, id foundation.ID, lock bool) (goalSelectionModel, error) {
	var row goalSelectionModel
	q := tx.Where("workspace_id=? AND id=?", string(w), string(id))
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := q.Take(&row).Error
	if gormNoRows(err) {
		err = goalNotFound()
	}
	return row, err
}
func (s *GORMGoalSelectionStore) GetGoalSelection(ctx context.Context, w, id foundation.ID) (app.SynthesisGoalSelection, error) {
	if s == nil || ctx == nil || !validID(w) || !validID(id) {
		return app.SynthesisGoalSelection{}, goalInvalid()
	}
	row, err := loadGoalSelection(s.db.WithContext(ctx), w, id, false)
	return row.selection(), err
}
func (s *GORMGoalSelectionStore) ReadGoalSelectionInput(ctx context.Context, w, id foundation.ID) (app.SynthesisGoalSelectionInput, error) {
	selection, err := s.GetGoalSelection(ctx, w, id)
	if err != nil {
		return app.SynthesisGoalSelectionInput{}, err
	}
	return s.input(ctx, selection)
}

func (s *GORMGoalSelectionStore) ClaimGoalSelection(ctx context.Context, c app.ClaimGoalSelectionCommand) (app.SynthesisGoalSelection, error) {
	var result app.SynthesisGoalSelection
	if !validID(c.WorkspaceID) || !validID(c.SelectionID) || c.ExpectedVersion < 1 || !validID(c.WorkflowRunID) || !validID(c.NodeRunID) || !validID(c.NodeAttemptID) || !validHash(c.ModelInputHash) || c.WorkflowRunID == c.NodeRunID || c.WorkflowRunID == c.NodeAttemptID || c.NodeRunID == c.NodeAttemptID {
		return result, goalInvalid()
	}
	err := s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		row, err := loadGoalSelection(tx, c.WorkspaceID, c.SelectionID, true)
		if err != nil {
			return err
		}
		if row.Status != string(app.GoalSelectionPending) || row.Version != c.ExpectedVersion || stringValue(row.ScheduledWorkflowID) != string(c.WorkflowRunID) {
			return goalConflict()
		}
		row.WorkflowRunID = stringPointer(string(c.WorkflowRunID))
		row.NodeRunID = stringPointer(string(c.NodeRunID))
		row.NodeAttemptID = stringPointer(string(c.NodeAttemptID))
		row.ModelInputHash = stringPointer(c.ModelInputHash)
		if err := s.verifyLive(ctx, scope, tx, row); err != nil {
			return err
		}
		var count int64
		if err := tx.Table("agent.model_run").Where("workspace_id=? AND node_attempt_id=?", string(c.WorkspaceID), string(c.NodeAttemptID)).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return goalConflict()
		}
		return updateGoalSelection(tx, row, map[string]any{"status": string(app.GoalSelectionRunning), "workflow_run_id": row.WorkflowRunID, "node_run_id": row.NodeRunID, "node_attempt_id": row.NodeAttemptID, "model_input_hash": row.ModelInputHash}, &result)
	})
	return result, err
}
func (s *GORMGoalSelectionStore) CompleteGoalSelection(ctx context.Context, c app.CompleteGoalSelectionCommand) (app.SynthesisGoalSelection, error) {
	var result app.SynthesisGoalSelection
	if !validID(c.WorkspaceID) || !validID(c.SelectionID) || c.ExpectedVersion < 1 || !validID(c.ModelRunID) || len(c.ModelOutput) < 1 || len(c.ModelOutput) > 128*1024 {
		return result, goalInvalid()
	}
	// 在写事务外读取不可变输入。仅请求状态可能并发变化；下方带锁 CAS 防止基于过期状态完成操作。
	input, err := s.ReadGoalSelectionInput(ctx, c.WorkspaceID, c.SelectionID)
	if err != nil {
		return result, err
	}
	if _, err = app.BindGoalSelectionOutput(c.ModelOutput, input); err != nil {
		return result, err
	}
	err = s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		row, err := loadGoalSelection(tx, c.WorkspaceID, c.SelectionID, true)
		if err != nil {
			return err
		}
		if row.Status == string(app.GoalSelectionSucceeded) && row.Version == c.ExpectedVersion+1 && stringValue(row.ModelRunID) == string(c.ModelRunID) && bytes.Equal(row.ModelOutput, c.ModelOutput) {
			result = row.selection()
			return nil
		}
		if row.Status != string(app.GoalSelectionRunning) || row.Version != c.ExpectedVersion {
			return goalConflict()
		}
		if err := s.verifyLive(ctx, scope, tx, row); err != nil {
			return err
		}
		if err := s.verifyModel(ctx, scope, row, c); err != nil {
			return err
		}
		return updateGoalSelection(tx, row, map[string]any{"status": string(app.GoalSelectionSucceeded), "model_run_id": string(c.ModelRunID), "model_output": bytes.Clone(c.ModelOutput)}, &result)
	})
	return result, err
}
func (s *GORMGoalSelectionStore) FailGoalSelection(ctx context.Context, c app.FailGoalSelectionCommand) (app.SynthesisGoalSelection, error) {
	var result app.SynthesisGoalSelection
	if !validID(c.WorkspaceID) || !validID(c.SelectionID) || c.ExpectedVersion < 1 || (c.Status != app.GoalSelectionFailed && c.Status != app.GoalSelectionRecoveryRequired) || !synthesisGoalErrorPattern.MatchString(c.ErrorCode) || (c.Status == app.GoalSelectionRecoveryRequired && c.Retryable) {
		return result, goalInvalid()
	}
	err := s.within(ctx, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		row, err := loadGoalSelection(tx, c.WorkspaceID, c.SelectionID, true)
		if err != nil {
			return err
		}
		if row.Status != string(app.GoalSelectionRunning) || row.Version != c.ExpectedVersion {
			return goalConflict()
		}
		return updateGoalSelection(tx, row, map[string]any{"status": string(c.Status), "error_code": c.ErrorCode, "retryable": c.Retryable}, &result)
	})
	return result, err
}
func updateGoalSelection(tx *gorm.DB, row goalSelectionModel, values map[string]any, result *app.SynthesisGoalSelection) error {
	values["version"] = row.Version + 1
	values["updated_at"] = gorm.Expr("clock_timestamp()")
	updated := tx.Model(&goalSelectionModel{}).Where("workspace_id=? AND id=? AND version=?", row.WorkspaceID, row.ID, row.Version).Updates(values)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return goalConflict()
	}
	reloaded, err := loadGoalSelection(tx, foundation.ID(row.WorkspaceID), foundation.ID(row.ID), false)
	*result = reloaded.selection()
	return err
}

var _ app.SynthesisGoalSelectionStore = (*GORMGoalSelectionStore)(nil)
