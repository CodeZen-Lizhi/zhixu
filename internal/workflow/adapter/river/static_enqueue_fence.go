package riveradapter

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

// NewStaticScopedEnqueueFence 保留静态 Env/YAML 配置的入队行为，不参与受管设置的 rollout。
// 仅由 Composition Root 在明确的 static 模式下选用；managed 模式必须注入真实 Model Settings fence。
func NewStaticScopedEnqueueFence() ScopedEnqueueFence {
	return staticScopedEnqueueFence{}
}

type staticScopedEnqueueFence struct{}

func (staticScopedEnqueueFence) CheckEnqueue(ctx context.Context, scope foundation.TransactionScope) error {
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_RIVER_CONTEXT_INVALID", false, errors.New("enqueue context is nil"))
	}
	if contextErr := ctx.Err(); contextErr != nil {
		cause := context.Cause(ctx)
		if cause == nil {
			cause = contextErr
		} else if !errors.Is(cause, contextErr) {
			cause = errors.Join(contextErr, cause)
		}
		if errors.Is(contextErr, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, "WORKFLOW_RIVER_JOB_INSERT_FAILED", false, cause)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_JOB_INSERT_FAILED", true, cause)
	}
	if _, err := platformpostgres.SQLTransaction(scope); err != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_TRANSACTION_INVALID", true, err)
	}
	return nil
}
