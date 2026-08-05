package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

// FollowupExecutor 独立推进 Git fast-forward 后的批量捕获与索引状态。
type FollowupExecutor struct {
	store   FollowupStore
	capture ExternalChangeCapture
}

// NewFollowupExecutor 创建不会回滚 Git 结果的索引 follow-up 执行器。
func NewFollowupExecutor(store FollowupStore, capture ExternalChangeCapture) (*FollowupExecutor, error) {
	if nilInterface(store) || nilInterface(capture) {
		return nil, unavailable("Git sync index follow-up dependency is unavailable")
	}
	return &FollowupExecutor{store: store, capture: capture}, nil
}

// Execute 在当前 Outbox lease fence 下推进独立 follow-up，失败只更新 IndexStatus。
func (executor *FollowupExecutor) Execute(ctx context.Context, lease domain.OutboxLease) (domain.SyncRun, error) {
	if executor == nil || nilInterface(executor.store) || nilInterface(executor.capture) ||
		lease.Kind != domain.OutboxIndexFollowup || !validID(lease.WorkspaceID) || !validID(lease.RunID) {
		return domain.SyncRun{}, invalid(domain.ErrorCodeInvalid, "Git sync index follow-up request is invalid")
	}
	run, started, err := executor.store.BeginIndexFollowup(ctx, lease, 0)
	if err != nil || !started {
		return run, err
	}
	result, captureErr := executor.capture.CaptureGitFastForward(ctx, ExternalChangeCommand{
		WorkspaceID: lease.WorkspaceID, RunID: lease.RunID, BeforeCommit: run.ExpectedHeadOID,
		AfterCommit: run.VerifiedHeadOID, IdempotencyKey: "git-sync-index:" + string(lease.RunID),
	})
	if captureErr != nil {
		if interrupted := interruptedError(ctx, captureErr); interrupted != nil {
			return run, interrupted
		}
		return executor.fail(ctx, lease, run, captureErr)
	}
	if result.NoIndexRequired {
		if result.IndexVersionID != "" {
			return executor.fail(ctx, lease, run, corrupt("Git sync index follow-up returned conflicting no-index metadata"))
		}
	} else if !validID(result.IndexVersionID) {
		return executor.fail(ctx, lease, run, corrupt("Git sync index follow-up returned an invalid index identity"))
	}
	completed, completeErr := executor.store.CompleteIndexFollowup(
		ctx, lease, run.Version, result.IndexVersionID, result.NoIndexRequired,
	)
	if completeErr == nil {
		return completed, nil
	}
	if interrupted := interruptedError(ctx, completeErr); interrupted != nil {
		return run, interrupted
	}
	failed, failErr := executor.fail(ctx, lease, run, completeErr)
	if failErr != nil {
		return domain.SyncRun{}, errors.Join(completeErr, failErr)
	}
	return failed, nil
}

func (executor *FollowupExecutor) fail(ctx context.Context, lease domain.OutboxLease, run domain.SyncRun, cause error) (domain.SyncRun, error) {
	if interrupted := interruptedError(ctx, cause); interrupted != nil {
		return run, interrupted
	}
	var classified *foundation.Error
	code, retryable := domain.ErrorCodeIndexFailed, false
	if errors.As(cause, &classified) {
		code, retryable = classified.Code, classified.Retryable
	}
	if !validText(code, 128) {
		code, retryable = domain.ErrorCodeIndexFailed, false
	}
	return executor.store.FailIndexFollowup(ctx, lease, run.Version, code, retryable)
}
