package application

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

const (
	defaultOutboxLease  = 3 * time.Minute
	maxOutboxAttempts   = 20
	maxOutboxRetryDelay = time.Minute
)

// Worker 从 durable outbox 领取并重放 Git 或索引工作。
type Worker struct {
	outbox   OutboxStore
	runs     *RunExecutor
	followup *FollowupExecutor
	owner    string
	lease    time.Duration
}

// NewWorker 创建单次领取式 Worker；进程循环由 composition root 拥有。
func NewWorker(outbox OutboxStore, runs *RunExecutor, followup *FollowupExecutor, owner string, lease time.Duration) (*Worker, error) {
	if nilInterface(outbox) || runs == nil || followup == nil || !validText(owner, 256) {
		return nil, unavailable("Git sync worker dependency is unavailable")
	}
	if lease == 0 {
		lease = defaultOutboxLease
	}
	if lease < time.Second || lease > 10*time.Minute {
		return nil, invalid(domain.ErrorCodeInvalid, "Git sync outbox lease is invalid")
	}
	return &Worker{outbox: outbox, runs: runs, followup: followup, owner: owner, lease: lease}, nil
}

// RunOnce 最多处理一条 outbox，并报告是否领取到工作。
func (worker *Worker) RunOnce(ctx context.Context) (bool, error) {
	if worker == nil || nilInterface(worker.outbox) {
		return false, unavailable("Git sync worker is unavailable")
	}
	lease, found, err := worker.outbox.ClaimNext(ctx, worker.owner, worker.lease)
	if err != nil || !found {
		return found, err
	}
	var processErr error
	switch lease.Kind {
	case domain.OutboxExecuteRun:
		_, processErr = worker.runs.Execute(ctx, lease.WorkspaceID, lease.RunID, worker.owner)
	case domain.OutboxIndexFollowup:
		_, processErr = worker.followup.Execute(ctx, lease)
	default:
		processErr = foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, errors.New("Git sync outbox kind is invalid"))
	}
	if processErr == nil {
		return true, worker.outbox.MarkPublished(ctx, lease)
	}
	if interrupted := interruptedError(ctx, processErr); interrupted != nil {
		return true, interrupted
	}
	var classified *foundation.Error
	if errors.As(processErr, &classified) && classified.Retryable {
		if lease.AttemptCount < maxOutboxAttempts {
			return true, worker.outbox.Reschedule(ctx, lease, classified.Code, retryDelay(lease.AttemptCount))
		}
		return true, worker.outbox.Poison(ctx, lease, classified.Code)
	}
	code := domain.ErrorCodeUnavailable
	if errors.As(processErr, &classified) {
		code = classified.Code
	}
	return true, worker.outbox.Poison(ctx, lease, code)
}

func retryDelay(attemptCount int) time.Duration {
	delay := time.Second
	for current := 1; current < attemptCount && delay < maxOutboxRetryDelay; current++ {
		delay *= 2
	}
	if delay > maxOutboxRetryDelay {
		return maxOutboxRetryDelay
	}
	return delay
}
