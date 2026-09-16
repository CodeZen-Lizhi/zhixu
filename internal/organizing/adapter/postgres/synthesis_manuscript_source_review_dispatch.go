package postgres

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func (s *GORMSynthesisManuscriptSourceReviewStore) DispatchSourceReviewScoped(ctx context.Context, scope foundation.TransactionScope, w foundation.ID) (app.SynthesisManuscriptSourceReview, bool, error) {
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return app.SynthesisManuscriptSourceReview{}, false, err
	}
	tx = tx.WithContext(ctx)
	if _, err = s.runtime.dependencies.Roots.ReadSynthesisManuscriptRootScoped(ctx, scope, w); err != nil {
		return app.SynthesisManuscriptSourceReview{}, false, err
	}
	var origin struct{ ID, WorkflowRunID string }
	q := tx.Raw(`SELECT p.id,p.workflow_run_id FROM organizing.synthesis_processing p JOIN workflow.run r ON r.id=p.workflow_run_id WHERE p.workspace_id=? AND p.status='RECOVERY_REQUIRED' AND p.error_code='SYNTHESIS_MANUSCRIPT_SOURCE_REVIEW_REQUIRED' AND r.status='failed' AND NOT EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review v WHERE v.workspace_id=p.workspace_id AND v.origin_processing_id=p.id AND v.origin_workflow_run_id=p.workflow_run_id) ORDER BY p.created_at,p.id LIMIT 1 FOR UPDATE OF p SKIP LOCKED`, string(w)).Scan(&origin)
	if q.Error != nil {
		return app.SynthesisManuscriptSourceReview{}, false, q.Error
	}
	if q.RowsAffected == 0 {
		return app.SynthesisManuscriptSourceReview{}, false, nil
	}
	id, err := s.runtime.dependencies.Storage.IDs.New()
	if err != nil {
		return app.SynthesisManuscriptSourceReview{}, false, err
	}
	now := canonicalTime(s.runtime.dependencies.Storage.Clock.Now())
	row := sourceReviewRow{ID: string(id), WorkspaceID: string(w), OriginProcessingID: origin.ID, OriginWorkflowRunID: origin.WorkflowRunID, Status: "PENDING", AttemptNo: 1, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err = tx.Create(&row).Error; err != nil {
		return app.SynthesisManuscriptSourceReview{}, false, err
	}
	out, err := row.project()
	return out, true, err
}
func (s *GORMSynthesisManuscriptSourceReviewStore) BindSourceReviewWorkflowScoped(ctx context.Context, scope foundation.TransactionScope, w, id, run foundation.ID) error {
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	tx = tx.WithContext(ctx)
	row, err := readSourceReview(tx, w, id, true)
	if err != nil {
		return err
	}
	if row.WorkflowRunID != nil {
		return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_CONFLICT")
	}
	return s.update(tx, row, map[string]any{"workflow_run_id": string(run)})
}
