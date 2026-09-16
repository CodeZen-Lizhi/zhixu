package postgres

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
	"reflect"
	"time"
)

func (s *GORMSynthesisManuscriptSourceReviewStore) liveSourceReviewRecovery(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, e workflowapp.ExecutionContext, row sourceReviewRow) error {
	if e.WorkspaceID != foundation.ID(row.WorkspaceID) || e.NodeKind != app.SynthesisSourceReviewRecover || e.NodeKey != e.NodeKind || e.DefinitionVersion != 1 || e.InputSchemaVersion != 1 {
		return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
	}
	var recovery sourceReviewRecoveryRow
	if err := tx.Where("workspace_id=? AND review_id=? AND workflow_run_id=?", row.WorkspaceID, row.ID, string(e.RunID)).Take(&recovery).Error; err != nil {
		return err
	}
	var now time.Time
	if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return err
	}
	f, found, err := s.fence.LockWorkspaceAnalysisExecutionScoped(ctx, scope, workflowapp.WorkspaceAnalysisExecutionFenceRequest{WorkspaceID: e.WorkspaceID, WorkflowRunID: e.RunID, NodeRunID: e.NodeRunID, NodeAttemptID: e.NodeAttemptID})
	if err != nil {
		return err
	}
	if !found || f.DefinitionID != e.DefinitionID || f.DefinitionVersion != 1 || f.NodeKey != e.NodeKey || f.WorkflowStatus != workflowdomain.RunStatusRunning || f.CancelRequested || f.PauseRequested || f.NodeStatus != workflowdomain.NodeStatusRunning || f.AttemptStatus != workflowdomain.AttemptStatusRunning || f.AttemptNo != e.AttemptNo || f.NodeAttempt != f.AttemptNo || f.AttemptLeaseOwner != e.LeaseOwner || f.NodeLeaseOwner != f.AttemptLeaseOwner || !f.NodeLeaseUntil.After(now) || !f.AttemptLeaseUntil.After(now) || now.Sub(recovery.CreatedAt) > 30*time.Minute {
		return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
	}
	return nil
}
func (s *GORMSynthesisManuscriptSourceReviewStore) sourceReviewActions(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, row sourceReviewRow, out *app.SynthesisSourceReviewView) error {
	var latest int
	if err := tx.Model(&sourceReviewRow{}).Select("max(attempt_no)").Where("workspace_id=? AND origin_processing_id=? AND origin_workflow_run_id=?", row.WorkspaceID, row.OriginProcessingID, row.OriginWorkflowRunID).Scan(&latest).Error; err != nil {
		return err
	}
	out.Latest = latest == row.AttemptNo
	var run struct{ Status string }
	if err := tx.Raw("SELECT status FROM workflow.run WHERE workspace_id=? AND id=?", row.WorkspaceID, row.WorkflowRunID).Scan(&run).Error; err != nil {
		return err
	}
	var recovery struct{ WorkflowRunID, Status string }
	if err := tx.Raw("SELECT x.workflow_run_id,r.status FROM organizing.synthesis_manuscript_source_review_recovery x JOIN workflow.run r ON r.id=x.workflow_run_id WHERE x.workspace_id=? AND x.review_id=? ORDER BY x.created_at DESC,x.id DESC LIMIT 1", row.WorkspaceID, row.ID).Scan(&recovery).Error; err != nil {
		return err
	}
	out.RecoveryWorkflowRunID = foundation.ID(recovery.WorkflowRunID)
	out.RecoveryStatus = recovery.Status
	var persisted struct {
		ReceiptHash string
		CreatedAt   *time.Time
	}
	if err := tx.Raw("SELECT receipt_hash,created_at FROM organizing.synthesis_manuscript_source_review_recovery_receipt WHERE workspace_id=? AND review_id=?", row.WorkspaceID, row.ID).Scan(&persisted).Error; err != nil {
		return err
	}
	receipt := persisted.ReceiptHash
	if receipt != "" {
		out.RecoveryCompletedAt = persisted.CreatedAt
		out.ReceiptHash = receipt
	}
	terminal := run.Status == "succeeded" || run.Status == "failed" || run.Status == "cancelled"
	if !out.Latest || !terminal {
		return nil
	}
	if (row.Status == "REVIEWED" || row.Status == "STALE") && len(row.Output) > 0 && receipt == "" && (recovery.WorkflowRunID == "" || recovery.Status == "failed" || recovery.Status == "cancelled" || recovery.Status == "succeeded") {
		if _, err := s.verifyModel(ctx, scope, row, row.Output, true); err == nil {
			if _, err = s.recheck(ctx, scope, tx, row); err == nil {
				out.CanRecover = true
			}
		}
	}
	if recovery.WorkflowRunID != "" && recovery.Status != "succeeded" && recovery.Status != "failed" && recovery.Status != "cancelled" {
		return nil
	}
	if row.AttemptNo >= 10 {
		return nil
	}
	if row.Status == "FAILED" && row.Retryable {
		out.CanRecheck = true
		return nil
	}
	if row.Status == "STALE" || row.Status == "REJECTED" || row.Status == "SUCCEEDED" {
		prospective := row
		prospective.AttemptNo++
		current, err := s.current(ctx, scope, tx, prospective)
		if err != nil {
			return nil
		}
		old, err := row.project()
		if err != nil {
			return err
		}
		out.CanRecheck = old.Snapshot != nil && !reflect.DeepEqual(*old.Snapshot, current.Snapshot)
	}
	return nil
}

func lockSourceReviewExecutionRow(tx *gorm.DB, e workflowapp.ExecutionContext, id foundation.ID) (sourceReviewRow, error) {
	if err := tx.Exec("SELECT id FROM workflow.run WHERE workspace_id=? AND id=? FOR UPDATE", string(e.WorkspaceID), string(e.RunID)).Error; err != nil {
		return sourceReviewRow{}, err
	}
	return readSourceReview(tx, e.WorkspaceID, id, true)
}
