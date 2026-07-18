package application

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

// DeliveryFailureError 表示 Application 已证明可安全归约到 Delivery Failure 的业务错误。
// 普通 error 表示事务结果未知，Worker 必须让同一 dispatch 由 transport 重投。
type DeliveryFailureError struct {
	Failure    domain.DeliveryFailure
	RetryDelay time.Duration
	cause      error
}

// Error 返回稳定错误码，不包含底层路径、正文或数据库错误。
func (failure *DeliveryFailureError) Error() string {
	if failure == nil {
		return "<nil>"
	}
	return failure.Failure.Code + ": reindex delivery failure"
}

// Unwrap 保留内部错误链供日志分类，持久摘要仍只使用 Failure。
func (failure *DeliveryFailureError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

// NewDeliveryFailure 创建 Processor/Completion 共用的安全业务归约错误。
// 非法 Failure/延迟返回普通 consistency error，不能被 Worker 当作可归约结果。
func NewDeliveryFailure(failure domain.DeliveryFailure, retryDelay time.Duration, cause error) error {
	normalized, err := domain.NormalizeDeliveryFailure(failure)
	if err != nil || normalized != failure ||
		failure.Class == domain.DeliveryFailureRetryable && (retryDelay <= 0 || retryDelay > MaxDeliveryRetryDelay) ||
		failure.Class != domain.DeliveryFailureRetryable && retryDelay != 0 {
		return foundation.NewError(
			foundation.ErrorConsistencyViolation,
			"REINDEX_DELIVERY_FAILURE_REDUCTION_INVALID",
			false,
			errors.New("delivery failure reduction is invalid"),
		)
	}
	return &DeliveryFailureError{Failure: normalized, RetryDelay: retryDelay, cause: cause}
}
