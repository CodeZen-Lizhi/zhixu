package application

import (
	"context"
	"errors"
	"strconv"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

const (
	// MaxAutoSyncBatch 限制单次维护扫描，避免旧写回积压独占 Worker。
	MaxAutoSyncBatch  = 100
	autoSyncKeyPrefix = "auto-writeback:"
)

// AutoSyncBatchResult 是一次有界自动调度的可观测结果。
type AutoSyncBatchResult struct {
	Scanned   int
	Scheduled int
	Replayed  int
	Deferred  int
}

// AutoSyncScheduler 将已完成写回转换为标准、可重放的 Git SyncRun。
type AutoSyncScheduler struct {
	candidates AutoSyncCandidateSource
	runs       AutomaticRunCreator
}

// NewAutoSyncScheduler 创建不直接执行 Git 的自动同步调度器。
func NewAutoSyncScheduler(candidates AutoSyncCandidateSource, runs AutomaticRunCreator) (*AutoSyncScheduler, error) {
	if nilInterface(candidates) || nilInterface(runs) {
		return nil, unavailable("Git automatic sync scheduler dependency is unavailable")
	}
	return &AutoSyncScheduler{candidates: candidates, runs: runs}, nil
}

// ScheduleBatch 扫描并创建 Run；活动运行或配置竞态会留待下一轮，不污染写回事实。
func (scheduler *AutoSyncScheduler) ScheduleBatch(ctx context.Context, limit int) (AutoSyncBatchResult, error) {
	if scheduler == nil || nilInterface(scheduler.candidates) || nilInterface(scheduler.runs) || limit < 1 || limit > MaxAutoSyncBatch {
		return AutoSyncBatchResult{}, invalid(domain.ErrorCodeInvalid, "Git automatic sync batch is invalid")
	}
	candidates, err := scheduler.candidates.ListAutoSyncCandidates(ctx, limit)
	if err != nil {
		return AutoSyncBatchResult{}, err
	}
	if len(candidates) > limit {
		return AutoSyncBatchResult{}, corrupt("Git automatic sync source exceeded the requested batch")
	}
	result := AutoSyncBatchResult{Scanned: len(candidates)}
	for _, candidate := range candidates {
		if !validID(candidate.WorkspaceID) || !validID(candidate.ProposalCommitID) || candidate.ConfigRevision < 1 {
			return result, corrupt("Git automatic sync source returned an invalid candidate")
		}
		receipt, createErr := scheduler.runs.CreateRun(ctx, CreateRunCommand{
			WorkspaceID:            candidate.WorkspaceID,
			Trigger:                domain.TriggerAutomatic,
			ExpectedConfigRevision: candidate.ConfigRevision,
			IdempotencyKey:         autoSyncKeyPrefix + string(candidate.ProposalCommitID) + ":config:" + strconv.FormatInt(candidate.ConfigRevision, 10),
		})
		if createErr == nil {
			if receipt.Replayed {
				result.Replayed++
			} else {
				result.Scheduled++
			}
			continue
		}
		if deferredAutoSync(createErr) {
			result.Deferred++
			continue
		}
		if interrupted := interruptedError(ctx, createErr); interrupted != nil {
			return result, interrupted
		}
		return result, createErr
	}
	return result, nil
}

func deferredAutoSync(err error) bool {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return false
	}
	switch classified.Code {
	case domain.ErrorCodeRunActive, domain.ErrorCodeAutoSyncDisabled, domain.ErrorCodeConfigNotFound,
		domain.ErrorCodeSecretUnavailable, domain.ErrorCodeConfigStale:
		return true
	default:
		return false
	}
}
