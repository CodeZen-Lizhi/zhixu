package postgres

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMGoalSelectionResultReader 仅读取不可变目标、选择和 Profile 事实，可在合成运行时之前构造，避免经由笔记写入器、模型执行器或文件系统读取器形成依赖环。
type GORMGoalSelectionResultReader struct {
	db        *gorm.DB
	uow       foundation.UnitOfWork
	snapshots app.SynthesisGoalCatalogSnapshotReader
}

func NewGORMGoalSelectionResultReader(pool *platformpostgres.Pool, snapshots app.SynthesisGoalCatalogSnapshotReader) (*GORMGoalSelectionResultReader, error) {
	if pool == nil || isNilInterface(snapshots) {
		return nil, goalInvalid()
	}
	db, err := pool.GORM()
	if err != nil {
		return nil, err
	}
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	return &GORMGoalSelectionResultReader{db: db, uow: uow, snapshots: snapshots}, nil
}

func (s *GORMGoalSelectionResultReader) ReadGoalSelectionProgress(ctx context.Context, w, id foundation.ID) (app.SynthesisGoalSelectionProgress, error) {
	var result app.SynthesisGoalSelectionProgress
	if s == nil || ctx == nil || !validID(w) || !validID(id) {
		return result, goalInvalid()
	}
	err := s.uow.Within(ctx, foundation.TransactionOptions{ReadOnly: true, Isolation: foundation.TransactionIsolationRepeatableRead}, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		result, err = readGoalSelectionProgress(tx.WithContext(ctx), w, id)
		return err
	})
	return result, synthesisDBError(ctx, err)
}
func readGoalSelectionProgress(tx *gorm.DB, w, id foundation.ID) (app.SynthesisGoalSelectionProgress, error) {
	var result app.SynthesisGoalSelectionProgress
	request, err := loadSynthesisGoal(tx, w, id, false)
	if err != nil {
		return result, err
	}
	result.Request, err = request.request()
	if err != nil {
		return result, err
	}
	var counts struct{ CatalogBatches, PreparedBatches, Selections, Pending, Running, Succeeded, Failed, RecoveryRequired int64 }
	err = tx.Raw(`WITH batches AS (
 SELECT count(*) catalog_batches,count(m.batch_id) prepared_batches FROM organizing.synthesis_goal_catalog_batch b
 LEFT JOIN organizing.synthesis_goal_selection_manifest m ON m.batch_id=b.id AND m.workspace_id=b.workspace_id AND m.sealed
 WHERE b.workspace_id=? AND b.request_id=?), selections AS (
 SELECT count(*) selections,count(*) FILTER(WHERE status='PENDING') pending,count(*) FILTER(WHERE status='RUNNING') running,
 count(*) FILTER(WHERE status='SUCCEEDED') succeeded,count(*) FILTER(WHERE status='FAILED') failed,count(*) FILTER(WHERE status='RECOVERY_REQUIRED') recovery_required
 FROM organizing.synthesis_goal_selection WHERE workspace_id=? AND request_id=?) SELECT * FROM batches CROSS JOIN selections`, string(w), string(id), string(w), string(id)).Scan(&counts).Error
	if err != nil {
		return result, err
	}
	result.CatalogBatches, result.PreparedBatches, result.Selections, result.Pending, result.Running, result.Succeeded, result.Failed, result.RecoveryRequired = counts.CatalogBatches, counts.PreparedBatches, counts.Selections, counts.Pending, counts.Running, counts.Succeeded, counts.Failed, counts.RecoveryRequired
	if result.CatalogBatches != result.Request.CatalogBatches || result.PreparedBatches > result.CatalogBatches || result.Selections != result.Pending+result.Running+result.Succeeded+result.Failed+result.RecoveryRequired {
		return result, goalConflict()
	}
	return result, nil
}

func (s *GORMGoalSelectionResultReader) ReadGoalSelectionResults(ctx context.Context, q app.GoalSelectionResultQuery) (app.GoalSelectionResultPage, error) {
	result := app.GoalSelectionResultPage{Items: []app.GoalSelectionResult{}}
	if s == nil || ctx == nil || !validID(q.WorkspaceID) || !validID(q.RequestID) || q.AfterSelectionID != "" && !validID(q.AfterSelectionID) || q.Limit < 1 || q.Limit > 32 {
		return result, goalInvalid()
	}
	var rows []goalSelectionModel
	err := s.uow.Within(ctx, foundation.TransactionOptions{ReadOnly: true, Isolation: foundation.TransactionIsolationRepeatableRead}, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		tx = tx.WithContext(ctx)
		result.Progress, err = readGoalSelectionProgress(tx, q.WorkspaceID, q.RequestID)
		if err != nil {
			return err
		}
		// 不对变化中的选择结果分页。一旦就绪，所有行及提供方输出均为终态且不可变，后续分页也保持一致。
		if !result.Progress.Ready() {
			return nil
		}
		if q.AfterSelectionID != "" {
			var cursor goalSelectionModel
			if err := tx.Where("workspace_id=? AND request_id=? AND id=? AND status='SUCCEEDED'", string(q.WorkspaceID), string(q.RequestID), string(q.AfterSelectionID)).Take(&cursor).Error; err != nil {
				if gormNoRows(err) {
					return goalInvalid()
				}
				return err
			}
		}
		query := tx.Where("workspace_id=? AND request_id=? AND status='SUCCEEDED'", string(q.WorkspaceID), string(q.RequestID)).Order("id").Limit(q.Limit + 1)
		if q.AfterSelectionID != "" {
			query = query.Where("id>?", string(q.AfterSelectionID))
		}
		return query.Find(&rows).Error
	})
	if err != nil {
		return result, synthesisDBError(ctx, err)
	}
	if len(rows) > q.Limit {
		result.NextAfterSelectionID = foundation.ID(rows[q.Limit-1].ID)
		rows = rows[:q.Limit]
	}
	for _, row := range rows {
		selection := row.selection()
		input, err := s.input(ctx, selection)
		if err != nil {
			return app.GoalSelectionResultPage{}, err
		}
		points, err := app.BindGoalSelectionOutput(selection.ModelOutput, input)
		if err != nil {
			return app.GoalSelectionResultPage{}, err
		}
		result.Items = append(result.Items, app.GoalSelectionResult{SelectionID: selection.ID, ModelRunID: selection.ModelRunID, Points: points})
	}
	return result, nil
}

var _ app.GoalSelectionResultReader = (*GORMGoalSelectionResultReader)(nil)

func (s *GORMGoalSelectionResultReader) input(ctx context.Context, selection app.SynthesisGoalSelection) (app.SynthesisGoalSelectionInput, error) {
	var empty app.SynthesisGoalSelectionInput
	requestRow, err := loadSynthesisGoal(s.db.WithContext(ctx), selection.WorkspaceID, selection.RequestID, false)
	if err != nil {
		return empty, err
	}
	request, err := requestRow.request()
	if err != nil {
		return empty, err
	}
	var header synthesisGoalBatchModel
	if err := s.db.WithContext(ctx).Where("id=? AND workspace_id=? AND request_id=?", string(selection.CatalogBatchID), string(selection.WorkspaceID), string(selection.RequestID)).Take(&header).Error; err != nil {
		return empty, err
	}
	batch, err := readSynthesisGoalBatch(s.db.WithContext(ctx), header)
	if err != nil {
		return empty, err
	}
	if selection.SourceOrdinal < 0 || selection.SourceOrdinal >= len(batch.Items) {
		return empty, goalConflict()
	}
	batch.Items = []app.SynthesisGoalCatalogBinding{batch.Items[selection.SourceOrdinal]}
	items, err := s.snapshots.ReadSynthesisGoalCatalogSnapshot(ctx, batch)
	if err != nil {
		return empty, err
	}
	inputs, err := app.BuildGoalSelectionInputs(request, batch, items)
	if err != nil {
		return empty, err
	}
	for _, input := range inputs {
		input.SourceOrdinal = selection.SourceOrdinal
		if input.PointOffset == selection.PointOffset {
			payload, err := app.BuildGoalSelectionPayload(input)
			if err != nil {
				return empty, err
			}
			if len(input.Points) != selection.PointCount || sha256Hex(payload) != selection.PayloadHash {
				return empty, goalConflict()
			}
			return input, nil
		}
	}
	return empty, goalConflict()
}
