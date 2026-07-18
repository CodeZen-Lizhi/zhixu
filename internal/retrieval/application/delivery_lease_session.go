package application

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	deliveryLeaseSessionUnavailableCode = "REINDEX_DELIVERY_LEASE_SESSION_UNAVAILABLE"
	deliveryLeaseSessionInvalidCode     = "REINDEX_DELIVERY_LEASE_SESSION_INVALID"
)

// DeliveryLeaseRuntime 是共享 Session 使用的最小 heartbeat/checkpoint 端口。
type DeliveryLeaseRuntime interface {
	Heartbeat(context.Context, DeliveryHeartbeatCommand) (DeliveryMutationResult, error)
	Checkpoint(context.Context, DeliveryCheckpointCommand) (DeliveryMutationResult, error)
	Fail(context.Context, DeliveryFailureCommand) (DeliveryMutationResult, error)
}

// DeliveryLeaseDisposition 表示共享 Session 是否仍持有可变更 Delivery 的资格。
type DeliveryLeaseDisposition string

const (
	// DeliveryLeaseActive 表示 Session 持有最新可用 Fence。
	DeliveryLeaseActive DeliveryLeaseDisposition = "active"
	// DeliveryLeaseStale 表示 generation、Attempt 或 owner 已失效。
	DeliveryLeaseStale DeliveryLeaseDisposition = "stale"
	// DeliveryLeaseCommitted 表示业务结果已由其他已确认事务归约。
	DeliveryLeaseCommitted DeliveryLeaseDisposition = "committed"
)

// DeliveryLeaseSnapshot 是可安全复制的 Session 状态与最新 Fence。
type DeliveryLeaseSnapshot struct {
	Disposition DeliveryLeaseDisposition
	Fence       domain.DeliveryFence
}

// DeliveryLeaseSession 串行 heartbeat 与 checkpoint，确保两者总使用同一最新 Fence。
type DeliveryLeaseSession struct {
	mu          sync.Mutex
	runtime     DeliveryLeaseRuntime
	disposition DeliveryLeaseDisposition
	fence       domain.DeliveryFence
}

// NewDeliveryLeaseSession 使用 Claim 返回的初始 Fence 创建共享 Session。
func NewDeliveryLeaseSession(runtime DeliveryLeaseRuntime, fence domain.DeliveryFence) (*DeliveryLeaseSession, error) {
	if nilDeliveryLeaseRuntime(runtime) {
		return nil, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			deliveryLeaseSessionUnavailableCode,
			false,
			errors.New("delivery lease runtime is unavailable"),
		)
	}
	if err := domain.ValidateDeliveryFence(fence); err != nil {
		return nil, foundation.NewError(
			foundation.ErrorInvalidInput,
			deliveryLeaseSessionInvalidCode,
			false,
			errors.New("delivery lease initial fence is invalid"),
		)
	}
	return &DeliveryLeaseSession{runtime: runtime, disposition: DeliveryLeaseActive, fence: fence}, nil
}

// Snapshot 返回当前状态；调用方不得缓存 Active Fence 后绕过 Session 执行 heartbeat/checkpoint。
func (session *DeliveryLeaseSession) Snapshot() DeliveryLeaseSnapshot {
	if session == nil {
		return DeliveryLeaseSnapshot{Disposition: DeliveryLeaseStale}
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.snapshotLocked()
}

// Heartbeat 串行续租并原子替换 Session 内的最新 Fence。
func (session *DeliveryLeaseSession) Heartbeat(ctx context.Context, leaseDuration time.Duration) (DeliveryLeaseSnapshot, error) {
	if leaseDuration <= 0 {
		return DeliveryLeaseSnapshot{}, leaseSessionInvalid(errors.New("heartbeat lease duration must be positive"))
	}
	return session.mutate(func(fence domain.DeliveryFence) (DeliveryMutationResult, error) {
		return session.runtime.Heartbeat(ctx, DeliveryHeartbeatCommand{Fence: fence, LeaseDuration: leaseDuration})
	})
}

// Checkpoint 串行提交不可逆 Processor 检查点并原子替换最新 Fence。
func (session *DeliveryLeaseSession) Checkpoint(ctx context.Context, checkpoint domain.DeliveryCheckpoint) (DeliveryLeaseSnapshot, error) {
	if err := domain.ValidateDeliveryCheckpoint(checkpoint); err != nil {
		return DeliveryLeaseSnapshot{}, leaseSessionInvalid(errors.New("delivery checkpoint is invalid"))
	}
	return session.mutate(func(fence domain.DeliveryFence) (DeliveryMutationResult, error) {
		return session.runtime.Checkpoint(ctx, DeliveryCheckpointCommand{Fence: fence, Checkpoint: checkpoint})
	})
}

// Fail 串行归约已证明安全的业务失败，成功后 Session 进入 committed。
func (session *DeliveryLeaseSession) Fail(ctx context.Context, failure domain.DeliveryFailure, retryDelay time.Duration) (DeliveryLeaseSnapshot, error) {
	normalized, err := domain.NormalizeDeliveryFailure(failure)
	if err != nil || normalized != failure ||
		failure.Class == domain.DeliveryFailureRetryable && (retryDelay <= 0 || retryDelay > MaxDeliveryRetryDelay) ||
		failure.Class != domain.DeliveryFailureRetryable && retryDelay != 0 {
		return DeliveryLeaseSnapshot{}, leaseSessionInvalid(errors.New("delivery failure reduction is invalid"))
	}
	return session.mutate(func(fence domain.DeliveryFence) (DeliveryMutationResult, error) {
		return session.runtime.Fail(ctx, DeliveryFailureCommand{Fence: fence, Failure: failure, RetryDelay: retryDelay})
	})
}

func (session *DeliveryLeaseSession) mutate(operation func(domain.DeliveryFence) (DeliveryMutationResult, error)) (DeliveryLeaseSnapshot, error) {
	if session == nil || nilDeliveryLeaseRuntime(session.runtime) {
		return DeliveryLeaseSnapshot{}, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			deliveryLeaseSessionUnavailableCode,
			false,
			errors.New("delivery lease session is unavailable"),
		)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.disposition != DeliveryLeaseActive {
		return session.snapshotLocked(), nil
	}
	result, err := operation(session.fence)
	if err != nil {
		return DeliveryLeaseSnapshot{}, err
	}
	switch result.Disposition {
	case DeliveryMutationApplied:
		if !validNextLeaseFence(session.fence, result.Fence) {
			return DeliveryLeaseSnapshot{}, leaseSessionInvalid(errors.New("delivery runtime returned an invalid next fence"))
		}
		session.fence = result.Fence
	case DeliveryMutationStale:
		session.disposition = DeliveryLeaseStale
		session.fence = domain.DeliveryFence{}
	case DeliveryMutationCommitted:
		session.disposition = DeliveryLeaseCommitted
		session.fence = domain.DeliveryFence{}
	default:
		return DeliveryLeaseSnapshot{}, leaseSessionInvalid(errors.New("delivery runtime returned an unknown disposition"))
	}
	return session.snapshotLocked(), nil
}

func (session *DeliveryLeaseSession) snapshotLocked() DeliveryLeaseSnapshot {
	return DeliveryLeaseSnapshot{Disposition: session.disposition, Fence: session.fence}
}

func validNextLeaseFence(current, next domain.DeliveryFence) bool {
	return domain.ValidateDeliveryFence(next) == nil &&
		next.DeliveryID == current.DeliveryID && next.DispatchNo == current.DispatchNo &&
		next.AttemptID == current.AttemptID && next.AttemptNo == current.AttemptNo &&
		next.Owner == current.Owner && next.DeliveryVersion > current.DeliveryVersion
}

func leaseSessionInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, deliveryLeaseSessionInvalidCode, false, cause)
}

func nilDeliveryLeaseRuntime(runtime DeliveryLeaseRuntime) bool {
	if runtime == nil {
		return true
	}
	value := reflect.ValueOf(runtime)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
