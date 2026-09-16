package postgres

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 只在启动对应 Workflow 的事务内执行绑定。
func (s *GORMGoalSelectionStore) BindGoalSelectionWorkflowScoped(ctx context.Context, scope foundation.TransactionScope, w, id, workflowID foundation.ID) error {
	if ctx == nil || !validID(w) || !validID(id) || !validID(workflowID) {
		return goalInvalid()
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	changed := tx.WithContext(ctx).Model(&goalSelectionModel{}).Where("workspace_id=? AND id=? AND status='PENDING' AND scheduled_workflow_id IS NULL", string(w), string(id)).Updates(map[string]any{"scheduled_workflow_id": string(workflowID), "updated_at": gorm.Expr("clock_timestamp()")})
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != 1 {
		return goalConflict()
	}
	return nil
}

func (s *GORMGoalSelectionStore) ClaimPendingGoalSelectionScoped(ctx context.Context, scope foundation.TransactionScope, w foundation.ID) (app.SynthesisGoalSelection, bool, error) {
	if ctx == nil || !validID(w) {
		return app.SynthesisGoalSelection{}, false, goalInvalid()
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return app.SynthesisGoalSelection{}, false, err
	}
	var row goalSelectionModel
	err = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("workspace_id=? AND status='PENDING' AND scheduled_workflow_id IS NULL", string(w)).Order("created_at,id").Take(&row).Error
	if gormNoRows(err) {
		return app.SynthesisGoalSelection{}, false, nil
	}
	return row.selection(), err == nil, err
}

func (s *GORMGoalSelectionStore) ListUnpreparedGoalSelectionBatches(ctx context.Context, w foundation.ID, limit int) ([]app.GoalSelectionPreparation, error) {
	if ctx == nil || !validID(w) || limit < 1 || limit > 32 {
		return nil, goalInvalid()
	}
	var rows []struct {
		WorkspaceID, RequestID, BatchID string
		BatchNo                         int64
	}
	err := s.db.WithContext(ctx).Raw(`SELECT b.workspace_id,b.request_id,b.id batch_id,b.batch_no FROM organizing.synthesis_goal_catalog_batch b
 LEFT JOIN organizing.synthesis_goal_selection_manifest m ON m.batch_id=b.id
 LEFT JOIN organizing.synthesis_goal_selection_preparation p ON p.batch_id=b.id
 WHERE b.workspace_id=? AND m.batch_id IS NULL AND (p.batch_id IS NULL OR p.next_check_at<=clock_timestamp())
 ORDER BY b.created_at,b.id LIMIT ?`, string(w), limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]app.GoalSelectionPreparation, len(rows))
	for i, r := range rows {
		result[i] = app.GoalSelectionPreparation{WorkspaceID: foundation.ID(r.WorkspaceID), RequestID: foundation.ID(r.RequestID), BatchID: foundation.ID(r.BatchID), BatchNo: r.BatchNo}
	}
	return result, nil
}
func (s *GORMGoalSelectionStore) DeferGoalSelectionPreparation(ctx context.Context, p app.GoalSelectionPreparation, code string) error {
	if ctx == nil || !validID(p.WorkspaceID) || !validID(p.RequestID) || !validID(p.BatchID) || p.BatchNo < 1 || !synthesisGoalErrorPattern.MatchString(code) {
		return goalInvalid()
	}
	return s.db.WithContext(ctx).Exec(`INSERT INTO organizing.synthesis_goal_selection_preparation(batch_id,workspace_id,next_check_at,error_code)
 SELECT id,workspace_id,clock_timestamp()+interval '30 seconds',? FROM organizing.synthesis_goal_catalog_batch b
 WHERE id=? AND workspace_id=? AND request_id=? AND batch_no=? AND NOT EXISTS(SELECT 1 FROM organizing.synthesis_goal_selection_manifest WHERE batch_id=b.id)
 ON CONFLICT(batch_id) DO UPDATE SET next_check_at=clock_timestamp()+interval '30 seconds',error_code=excluded.error_code,version=synthesis_goal_selection_preparation.version+1`, code, string(p.BatchID), string(p.WorkspaceID), string(p.RequestID), p.BatchNo).Error
}
func (s *GORMGoalSelectionStore) ReconcileGoalSelectionWorkflows(ctx context.Context, w foundation.ID, limit int) (int, error) {
	if ctx == nil || !validID(w) || limit < 1 || limit > 100 {
		return 0, goalInvalid()
	}
	updated := s.db.WithContext(ctx).Exec(`WITH orphan AS (
 SELECT s.id FROM organizing.synthesis_goal_selection s JOIN workflow.run r ON r.id=s.scheduled_workflow_id AND r.workspace_id=s.workspace_id
 WHERE s.workspace_id=? AND s.status IN ('PENDING','RUNNING') AND r.status IN ('succeeded','failed','cancelled')
 ORDER BY s.updated_at,s.id LIMIT ? FOR UPDATE OF s SKIP LOCKED
 ) UPDATE organizing.synthesis_goal_selection s SET status=CASE WHEN s.status='PENDING' THEN 'FAILED' ELSE 'RECOVERY_REQUIRED' END,
 error_code=CASE WHEN s.status='PENDING' THEN 'SYNTHESIS_GOAL_WORKFLOW_ENDED_BEFORE_SELECTION' ELSE 'SYNTHESIS_GOAL_SELECTION_RECOVERY_REQUIRED' END,
 retryable=(s.status='PENDING'),version=s.version+1,updated_at=clock_timestamp() FROM orphan WHERE s.id=orphan.id`, string(w), limit)
	return int(updated.RowsAffected), updated.Error
}
func (s *GORMGoalSelectionStore) FailScheduledGoalSelection(ctx context.Context, w, id, runID foundation.ID, code string, retryable bool) (app.SynthesisGoalSelection, error) {
	var result app.SynthesisGoalSelection
	if ctx == nil || !validID(w) || !validID(id) || !validID(runID) || !synthesisGoalErrorPattern.MatchString(code) {
		return result, goalInvalid()
	}
	err := s.within(ctx, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		row, err := loadGoalSelection(tx, w, id, true)
		if err != nil {
			return err
		}
		if stringValue(row.ScheduledWorkflowID) != string(runID) {
			return goalConflict()
		}
		if row.Status != string(app.GoalSelectionPending) {
			result = row.selection()
			return nil
		}
		return updateGoalSelection(tx, row, map[string]any{"status": string(app.GoalSelectionFailed), "error_code": code, "retryable": retryable}, &result)
	})
	return result, err
}
func (s *GORMGoalSelectionStore) RetryGoalSelection(ctx context.Context, c app.RetryGoalSelectionCommand) (app.SynthesisGoalSelection, error) {
	var result app.SynthesisGoalSelection
	if ctx == nil || app.ValidateAnchorCommand(c.WorkspaceID, c.IdempotencyKey) != nil || !validID(c.SelectionID) || c.ExpectedVersion < 1 {
		return result, goalInvalid()
	}
	err := s.within(ctx, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		// 稳定命令键先串行化冲突身份，再进入聚合对象。
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, "goal-selection-retry:"+string(c.WorkspaceID)+":"+c.IdempotencyKey).Error; err != nil {
			return err
		}
		var receipt struct {
			SelectionID                    string
			ExpectedVersion, ResultVersion int64
		}
		found := tx.Table("organizing.synthesis_goal_selection_retry").Where("workspace_id=? AND idempotency_key=?", string(c.WorkspaceID), c.IdempotencyKey).Take(&receipt).Error
		if found != nil && !gormNoRows(found) {
			return found
		}
		row, err := loadGoalSelection(tx, c.WorkspaceID, c.SelectionID, true)
		if err != nil {
			return err
		}
		if found == nil {
			if receipt.SelectionID != string(c.SelectionID) || receipt.ExpectedVersion != c.ExpectedVersion {
				return goalConflict()
			}
			result = row.selection()
			return nil
		}
		if row.Status != string(app.GoalSelectionFailed) || !row.Retryable || row.Version != c.ExpectedVersion {
			return goalConflict()
		}
		if row.ScheduledWorkflowID != nil {
			var terminal int64
			if err := tx.Table("workflow.run").Where("id=? AND workspace_id=? AND status IN ('succeeded','failed','cancelled')", *row.ScheduledWorkflowID, row.WorkspaceID).Count(&terminal).Error; err != nil {
				return err
			}
			if terminal != 1 {
				return goalConflict()
			}
		}
		if err := tx.Exec(`INSERT INTO organizing.synthesis_goal_selection_retry(workspace_id,idempotency_key,selection_id,expected_version,result_version) VALUES(?,?,?,?,?)`, row.WorkspaceID, c.IdempotencyKey, row.ID, row.Version, row.Version+1).Error; err != nil {
			return err
		}
		return updateGoalSelection(tx, row, map[string]any{"status": string(app.GoalSelectionPending), "scheduled_workflow_id": nil, "workflow_run_id": nil, "node_run_id": nil, "node_attempt_id": nil, "model_input_hash": nil, "model_run_id": nil, "model_output": nil, "error_code": nil, "retryable": false}, &result)
	})
	return result, err
}

var _ app.GoalSelectionDispatchStore = (*GORMGoalSelectionStore)(nil)
