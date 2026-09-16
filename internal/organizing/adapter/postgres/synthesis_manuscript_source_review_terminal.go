package postgres

import (
	"context"
	"errors"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

func (s *GORMSynthesisManuscriptSourceReviewStore) FailSourceReview(ctx context.Context, w, id, run foundation.ID, cause error) error {
	return s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		if err := tx.Exec("SELECT id FROM workflow.run WHERE workspace_id=? AND id=? FOR UPDATE", string(w), string(run)).Error; err != nil {
			return err
		}
		row, err := readSourceReview(tx, w, id, true)
		if err != nil {
			return err
		}
		if stringValue(row.WorkflowRunID) != string(run) {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
		}
		return s.reduceSourceReview(ctx, scope, tx, row, cause)
	})
}

// 成功 Call 的哈希本身不是已接受输出日志。只有同步校验器可判定该输出无效；协调流程绝不猜测成功响应是否会被接受。
func (s *GORMSynthesisManuscriptSourceReviewStore) reduceSourceReview(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, row sourceReviewRow, cause error) error {
	if row.Status == "SUCCEEDED" || row.Status == "REJECTED" || row.Status == "STALE" || row.Status == "FAILED" {
		return nil
	}
	if row.Status == "REVIEWED" {
		var failure *foundation.Error
		if errors.As(cause, &failure) && (sourceReviewStaleFailure(failure)) {
			return s.update(tx, row, map[string]any{"status": "STALE", "error_code": failure.Code, "retryable": false, "completed_at": canonicalTime(s.runtime.dependencies.Storage.Clock.Now())})
		}
		return nil
	}
	code, retryable := "SYNTHESIS_SOURCE_REVIEW_INTERRUPTED", false
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		code, retryable = classified.Code, classified.Retryable
	}
	status := "FAILED"
	if row.ModelRunID != nil {
		if err := tx.Exec("SELECT organizing.close_synthesis_source_review_abandoned_calls(?,?)", row.WorkspaceID, row.ID).Error; err != nil {
			return err
		}
		status, retryable = "RECOVERY_REQUIRED", false
		record, err := s.runs.GetModelRunRecordScoped(ctx, scope, foundation.ID(row.WorkspaceID), foundation.ID(*row.ModelRunID), true)
		if err != nil {
			var failure *foundation.Error
			if errors.Is(err, gorm.ErrRecordNotFound) || (errors.As(err, &failure) && failure.Kind == foundation.ErrorNotFound) {
				if row.Status == "RECOVERY_REQUIRED" {
					return nil
				}
				return s.update(tx, row, map[string]any{"status": "RECOVERY_REQUIRED", "error_code": "SYNTHESIS_SOURCE_REVIEW_RECOVERY_REQUIRED", "retryable": false, "completed_at": canonicalTime(s.runtime.dependencies.Storage.Clock.Now())})
			}
			return err
		}
		model := record.Run
		if model.WorkspaceID != foundation.ID(row.WorkspaceID) || model.WorkflowRunID != foundation.ID(stringValue(row.WorkflowRunID)) || model.NodeRunID != foundation.ID(stringValue(row.NodeRunID)) || model.NodeAttemptID != foundation.ID(stringValue(row.NodeAttemptID)) || model.Schema.ID != app.SynthesisSourceReviewSchema {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_MODEL_PROOF_INVALID")
		}
		known := cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded)
		if classified != nil && (classified.Kind == foundation.ErrorManualRecoveryRequired || classified.Code == agentapp.ErrorCodeModelCallPersistenceUnknown || classified.Code == agentapp.ErrorCodeModelCallReplayUnsafe) {
			known = false
		}
		if classified != nil && sourceReviewUnknownFailure(classified.Code) {
			known = false
		}
		if cause == nil && len(record.Calls) > 0 {
			last := record.Calls[len(record.Calls)-1]
			if last.Status == agentdomain.ModelCallFailed {
				definite, canRetry := sourceReviewKnownCallFailure(last.ErrorCode)
				if definite {
					known = true
					code = last.ErrorCode
					retryable = canRetry
				}
			}
		}
		active := false
		for _, call := range record.Calls {
			if agentdomain.ValidateModelCall(call) != nil || call.ModelRunID != model.ID {
				return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_MODEL_PROOF_INVALID")
			}
			if call.Status == agentdomain.ModelCallStarted {
				active = true
				known = false
			}
			if call.Status == agentdomain.ModelCallUnknown {
				known = false
			}
		}
		if model.Status == agentdomain.ModelRunFailed {
			known = true
		}
		if known {
			status = "FAILED"
			if classified != nil {
				retryable = classified.Retryable
			}
		}
		if model.Status == agentdomain.ModelRunRunning && !active {
			now := canonicalTime(s.runtime.dependencies.Storage.Clock.Now())
			if now.Before(model.UpdatedAt) {
				now = model.UpdatedAt
			}
			model.Status = agentdomain.ModelRunUnknown
			if known {
				model.Status = agentdomain.ModelRunFailed
			}
			model.FinalErrorCode = code
			model.UpdatedAt = now
			model.CompletedAt = &now
			model.Version++
			if _, _, err = s.runs.FinalizeModelRunScoped(ctx, scope, agentapp.FinalizeModelRunCommand{ExpectedVersion: model.Version - 1, Run: model}); err != nil {
				return err
			}
		}
		if !known {
			code = "SYNTHESIS_SOURCE_REVIEW_RECOVERY_REQUIRED"
		}
	}
	if row.Status == "RECOVERY_REQUIRED" && status != "FAILED" {
		return nil
	}
	return s.update(tx, row, map[string]any{"status": status, "error_code": code, "retryable": retryable, "completed_at": canonicalTime(s.runtime.dependencies.Storage.Clock.Now())})
}

func (s *GORMSynthesisManuscriptSourceReviewStore) OnWorkflowNodeTerminalScoped(ctx context.Context, scope foundation.TransactionScope, event workflowapp.WorkflowNodeTerminalEvent) error {
	if event.NodeKind != app.SynthesisSourceReviewPrepare && event.NodeKind != app.SynthesisSourceReviewModel && event.NodeKind != app.SynthesisSourceReviewApply {
		return nil
	}
	if event.Outcome == workflowapp.WorkflowTerminalOutcomeSucceeded {
		return nil
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	tx = tx.WithContext(ctx)
	var row sourceReviewRow
	q := tx.Where("workspace_id=? AND workflow_run_id=?", string(event.WorkspaceID), string(event.WorkflowRunID)).Take(&row)
	if errors.Is(q.Error, gorm.ErrRecordNotFound) {
		return nil
	}
	if q.Error != nil {
		return q.Error
	}
	// 运行时持有运行和节点锁。在访问评审所属模块前，先验证持久化终态事件，包括迟到的尝试通知。
	var valid bool
	if err = tx.Raw(`SELECT EXISTS(SELECT 1 FROM workflow.node_run n WHERE n.id=? AND n.run_id=? AND n.node_type=? AND n.status IN ('failed','cancelled') AND (?='' OR EXISTS(SELECT 1 FROM workflow.node_attempt a WHERE a.id::text=? AND a.node_run_id=n.id AND a.attempt_no=n.attempt)))`, string(event.NodeRunID), string(event.WorkflowRunID), event.NodeKind, string(event.NodeAttemptID), string(event.NodeAttemptID)).Scan(&valid).Error; err != nil {
		return err
	}
	if !valid {
		return nil
	}
	row, err = readSourceReview(tx, event.WorkspaceID, foundation.ID(row.ID), true)
	if err != nil {
		return err
	}
	return s.reduceSourceReview(ctx, scope, tx, row, nil)
}

func (s *GORMSynthesisManuscriptSourceReviewStore) ReconcileSourceReviews(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_LIMIT_INVALID")
	}
	if s.control != nil {
		var expired []struct {
			ID      string
			Version int64
		}
		if err := s.runtime.dependencies.Candidates.database.WithContext(ctx).Raw(`SELECT r.id,r.version FROM workflow.run r JOIN organizing.synthesis_manuscript_source_review v ON v.workflow_run_id=r.id WHERE r.status NOT IN ('succeeded','failed','cancelled') AND r.cancel_requested_at IS NULL AND v.created_at<clock_timestamp()-interval '30 minutes' ORDER BY v.created_at,v.id LIMIT ?`, limit).Scan(&expired).Error; err != nil {
			return 0, err
		}
		for _, run := range expired {
			if _, err := s.control.Cancel(ctx, workflowapp.RunControlCommand{WorkflowRunID: foundation.ID(run.ID), ExpectedVersion: run.Version, IdempotencyKey: "source-review-deadline:" + run.ID}); err != nil {
				return 0, err
			}
		}
	}
	count := 0
	err := s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		var rows []sourceReviewRow
		if err := tx.Raw(`SELECT v.* FROM workflow.run r JOIN organizing.synthesis_manuscript_source_review v ON v.workflow_run_id=r.id WHERE (v.status IN ('PENDING','PREPARED','RUNNING') OR (v.status='RECOVERY_REQUIRED' AND EXISTS(SELECT 1 FROM agent.model_run m WHERE m.id=v.model_run_id AND m.status='RUNNING'))) AND (r.status IN ('failed','cancelled','succeeded') OR r.cancel_requested_at IS NOT NULL OR v.created_at<clock_timestamp()-interval '30 minutes' OR EXISTS(SELECT 1 FROM workflow.node_attempt a WHERE a.id=v.node_attempt_id AND (a.status<>'running' OR a.lease_until<=clock_timestamp()))) ORDER BY v.created_at,v.id LIMIT ? FOR UPDATE OF r SKIP LOCKED`, limit).Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			locked, err := readSourceReview(tx, foundation.ID(row.WorkspaceID), foundation.ID(row.ID), true)
			if err != nil {
				return err
			}
			if err = s.reduceSourceReview(ctx, scope, tx, locked, nil); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	return count, err
}

var _ workflowapp.ScopedWorkflowTerminalHook = (*GORMSynthesisManuscriptSourceReviewStore)(nil)

func sourceReviewStaleFailure(f *foundation.Error) bool {
	if f.Kind == foundation.ErrorPermissionDenied {
		return true
	}
	switch f.Code {
	case "SYNTHESIS_SOURCE_REVIEW_STALE_TEXT", "SYNTHESIS_SOURCE_REVIEW_STALE_SOURCE", "SYNTHESIS_SOURCE_REVIEW_STALE_MANUSCRIPT_BASELINE", "SYNTHESIS_SOURCE_REVIEW_STALE_SCOPE", "SYNTHESIS_SOURCE_STALE", "SYNTHESIS_SOURCE_NOT_FOUND", "ANCHOR_CONFLICT":
		return true
	}
	return false
}
func sourceReviewUnknownFailure(code string) bool {
	switch code {
	case "MODEL_CHAT_TIMEOUT", "MODEL_CHAT_CANCELLED", "MODEL_CHAT_REQUEST_FAILED", agentapp.ErrorCodeModelCallPersistenceUnknown, agentapp.ErrorCodeModelCallReplayUnsafe:
		return true
	}
	return false
}

// 只有稳定且明确的响应或配置失败，才能关闭历史 RUNNING 账本；普通调用失败也可能包含传输超时。
func sourceReviewKnownCallFailure(code string) (bool, bool) {
	switch code {
	case "MODEL_CHAT_RATE_LIMITED", "MODEL_CHAT_PROVIDER_UNAVAILABLE":
		return true, true
	case "MODEL_CHAT_UNAUTHORIZED", "MODEL_CHAT_REJECTED", "MODEL_CHAT_REDIRECT_REJECTED", "MODEL_CHAT_REQUEST_INVALID", "MODEL_CHAT_CONFIG_INVALID", "MODEL_CHAT_CAPABILITY_UNAVAILABLE", "MODEL_CHAT_RESPONSE_INVALID", "MODEL_CHAT_RESPONSE_MODEL_MISMATCH", "AGENT_CHAT_RESPONSE_INVALID", "AGENT_CHAT_RESPONSE_MODEL_MISMATCH":
		return true, false
	}
	return false, false
}
