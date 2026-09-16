package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

// 复用 Workflow 的限定作用域执行投影；持锁直到推荐提交，使发布与取消、租约丢失相互串行。
func (s *GORMAnchorStore) verifyLiveAnchorRecommendation(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, row anchorRecommendationRequestModel) error {
	if row.WorkflowRunID == nil || row.NodeRunID == nil || row.NodeAttemptID == nil {
		return anchorExecutionRecovery()
	}
	state, found, err := s.executionFence.LockWorkspaceAnalysisExecutionScoped(ctx, scope, workflowapp.WorkspaceAnalysisExecutionFenceRequest{
		WorkspaceID: foundation.ID(row.WorkspaceID), WorkflowRunID: foundation.ID(*row.WorkflowRunID), NodeRunID: foundation.ID(*row.NodeRunID), NodeAttemptID: foundation.ID(*row.NodeAttemptID),
	})
	if err != nil {
		return err
	}
	var now time.Time
	if err := tx.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return err
	}
	if !found || state.WorkflowStatus != workflowdomain.RunStatusRunning || state.CancelRequested || state.PauseRequested ||
		state.NodeStatus != workflowdomain.NodeStatusRunning || state.AttemptStatus != workflowdomain.AttemptStatusRunning ||
		state.NodeAttempt != state.AttemptNo || !state.NodeLeaseOwnerSet || !state.AttemptLeaseOwnerSet || state.NodeLeaseOwner != state.AttemptLeaseOwner ||
		!state.NodeLeaseUntilSet || !state.AttemptLeaseUntilSet || !state.NodeLeaseUntil.After(now) || !state.AttemptLeaseUntil.After(now) {
		return anchorExecutionRecovery()
	}
	return nil
}

func anchorExecutionRecovery() error {
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, "ANCHOR_MODEL_RECOVERY_REQUIRED", false, errors.New("anchor recommendation execution is no longer active"))
}
