package postgres

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AnchorRecommendationDispatchFields = anchorRecommendationRequestModel

type anchorRecommendationDispatchRow struct {
	AnchorRecommendationDispatchFields `gorm:"embedded"`
	ScheduledWorkflowRunID             *string `gorm:"column:scheduled_workflow_run_id"`
}

func (anchorRecommendationDispatchRow) TableName() string {
	return "organizing.anchor_recommendation_request"
}

var _ app.AnchorRecommendationDispatchStore = (*GORMAnchorStore)(nil)

func (s *GORMAnchorStore) ClaimPendingAnchorRecommendationScoped(ctx context.Context, scope foundation.TransactionScope) (app.AnchorRecommendationRequest, bool, error) {
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return app.AnchorRecommendationRequest{}, false, err
	}
	var row anchorRecommendationDispatchRow
	err = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("status=? AND scheduled_workflow_run_id IS NULL", "PENDING").Order("created_at,id").Limit(1).Take(&row).Error
	if gormNoRows(err) {
		return app.AnchorRecommendationRequest{}, false, nil
	}
	if err != nil {
		return app.AnchorRecommendationRequest{}, false, err
	}
	request, err := row.anchorRecommendationRequest()
	return request, err == nil, err
}

func (s *GORMAnchorStore) BindAnchorRecommendationWorkflowScoped(ctx context.Context, scope foundation.TransactionScope, requestID, workflowRunID foundation.ID) error {
	if !validID(requestID) || !validID(workflowRunID) {
		return app.AnchorInvalid()
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	updated := tx.WithContext(ctx).Model(&anchorRecommendationDispatchRow{}).
		Where("id=? AND status='PENDING' AND scheduled_workflow_run_id IS NULL", string(requestID)).
		Updates(map[string]any{"scheduled_workflow_run_id": string(workflowRunID), "updated_at": gorm.Expr("clock_timestamp()")})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return app.AnchorConflict()
	}
	return nil
}

func (s *GORMAnchorStore) AnchorRecommendationScheduledWorkflow(ctx context.Context, workspaceID, requestID foundation.ID) (foundation.ID, error) {
	if !validID(workspaceID) || !validID(requestID) {
		return "", app.AnchorInvalid()
	}
	var row anchorRecommendationDispatchRow
	if err := s.db.WithContext(ctx).Select("scheduled_workflow_run_id").Where("workspace_id=? AND id=?", string(workspaceID), string(requestID)).Take(&row).Error; err != nil {
		if gormNoRows(err) {
			return "", anchorNotFound()
		}
		return "", err
	}
	if row.ScheduledWorkflowRunID == nil || !validID(foundation.ID(*row.ScheduledWorkflowRunID)) {
		return "", app.AnchorConflict()
	}
	return foundation.ID(*row.ScheduledWorkflowRunID), nil
}

// FailScheduledAnchorRecommendation 在尝试调用提供方前记录过期范围或来源失败；工作流身份使旧 River 任务无法覆盖已重试的请求。
func (s *GORMAnchorStore) FailScheduledAnchorRecommendation(ctx context.Context, workspaceID, requestID, workflowRunID foundation.ID, code string, retryable bool) (app.AnchorRecommendationRequest, error) {
	if !validID(workspaceID) || !validID(requestID) || !validID(workflowRunID) || code == "" {
		return app.AnchorRecommendationRequest{}, app.AnchorInvalid()
	}
	var result app.AnchorRecommendationRequest
	err := s.within(ctx, true, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var row anchorRecommendationDispatchRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("workspace_id=? AND id=?", string(workspaceID), string(requestID)).Take(&row).Error; err != nil {
			return err
		}
		if row.ScheduledWorkflowRunID == nil || foundation.ID(*row.ScheduledWorkflowRunID) != workflowRunID {
			return app.AnchorConflict()
		}
		if row.Status != string(domain.AnchorRecommendationPending) {
			var err error
			result, err = row.anchorRecommendationRequest()
			return err
		}
		updated := tx.Model(&anchorRecommendationDispatchRow{}).Where("workspace_id=? AND id=? AND status='PENDING' AND scheduled_workflow_run_id=?", string(workspaceID), string(requestID), string(workflowRunID)).Updates(map[string]any{
			"status": string(domain.AnchorRecommendationFailed), "error_code": code, "retryable": retryable,
			"version": gorm.Expr("version + 1"), "updated_at": gorm.Expr("clock_timestamp()"),
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return app.AnchorConflict()
		}
		if err := tx.Where("workspace_id=? AND id=?", string(workspaceID), string(requestID)).Take(&row).Error; err != nil {
			return err
		}
		var decodeErr error
		result, decodeErr = row.anchorRecommendationRequest()
		return decodeErr
	})
	if err != nil {
		return app.AnchorRecommendationRequest{}, err
	}
	return result, nil
}

// ReconcileAnchorRecommendationWorkflows 仅在所绑定工作流进入终态后关闭孤立推荐状态。正在执行的提供方尝试结果不确定，须恢复；从未领取的请求则可重试。
func (s *GORMAnchorStore) ReconcileAnchorRecommendationWorkflows(ctx context.Context, limit int) (int, error) {
	if ctx == nil || limit < 1 || limit > 100 {
		return 0, app.AnchorInvalid()
	}
	count := 0
	err := s.within(ctx, true, func(ctx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`WITH orphan AS (
 SELECT q.id FROM organizing.anchor_recommendation_request q
 JOIN workflow.run r ON r.id=q.scheduled_workflow_run_id AND r.workspace_id=q.workspace_id
 WHERE q.status IN ('PENDING','RUNNING') AND r.status IN ('succeeded','failed','cancelled')
 ORDER BY q.updated_at,q.id LIMIT ? FOR UPDATE OF q SKIP LOCKED
 ) UPDATE organizing.anchor_recommendation_request q SET
 status=CASE WHEN q.status='PENDING' THEN 'FAILED' ELSE 'RECOVERY_REQUIRED' END,
 error_code=CASE WHEN q.status='PENDING' THEN 'ANCHOR_WORKFLOW_ENDED_BEFORE_ANALYSIS' ELSE 'ANCHOR_MODEL_RECOVERY_REQUIRED' END,
 retryable=(q.status='PENDING'), version=q.version+1,updated_at=clock_timestamp()
 FROM orphan WHERE q.id=orphan.id`, limit)
		count = int(result.RowsAffected)
		return result.Error
	})
	return count, err
}
