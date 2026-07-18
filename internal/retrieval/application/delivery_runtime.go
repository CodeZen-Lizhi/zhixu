package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	// MaxDeliveryRetryDelay 是可信 Processor 可请求的最长业务重试等待时间。
	MaxDeliveryRetryDelay = 24 * time.Hour
)

// DeliveryClaimDisposition 表示当前 River delivery 在 Reindex 事实源中的处理资格。
type DeliveryClaimDisposition string

const (
	// DeliveryClaimed 表示已取得或精确重放当前有效 Attempt。
	DeliveryClaimed DeliveryClaimDisposition = "claimed"
	// DeliveryClaimStale 表示旧 generation 或旧 owner 可安全 no-op。
	DeliveryClaimStale DeliveryClaimDisposition = "stale"
	// DeliveryClaimCommitted 表示该 generation 的业务结果已提交，transport 可返回成功。
	DeliveryClaimCommitted DeliveryClaimDisposition = "committed"
)

// DeliveryRuntimePort 隐藏 PostgreSQL DB-time lease 与 append-only Attempt 持久化实现。
type DeliveryRuntimePort interface {
	// Claim 使用数据库时间判断 generation 资格并追加或精确重放 Attempt。
	Claim(context.Context, DeliveryClaimCommand) (DeliveryClaimResult, error)
	// Heartbeat 使用数据库时间续租并执行完整 Delivery Fence CAS。
	Heartbeat(context.Context, DeliveryHeartbeatCommand) (DeliveryMutationResult, error)
	// Checkpoint 原子持久化一个不可逆 Processor 检查点。
	Checkpoint(context.Context, DeliveryCheckpointCommand) (DeliveryMutationResult, error)
	// Fail 使用数据库时间归约 retry_wait、failed 或 manual_recovery。
	Fail(context.Context, DeliveryFailureCommand) (DeliveryMutationResult, error)
}

// DeliveryClaimCommand 只携带稳定 transport 身份与租约时长，不允许调用方提交 now。
type DeliveryClaimCommand struct {
	DeliveryID    foundation.ID
	DispatchNo    int
	RiverJobID    int64
	RiverAttempt  int
	DeliveryKey   string
	LeaseOwner    string
	LeaseDuration time.Duration
}

// DeliveryClaimResult 返回 Claim、benign stale 或已提交业务结果的显式事实。
type DeliveryClaimResult struct {
	Disposition    DeliveryClaimDisposition
	Delivery       domain.Delivery
	Attempt        domain.DeliveryAttempt
	Fence          domain.DeliveryFence
	Replayed       bool
	LeaseReclaimed bool
}

// DeliveryMutationDisposition 表示 Fence 命令已应用、已失效或业务结果已提交。
type DeliveryMutationDisposition string

const (
	// DeliveryMutationApplied 表示命令已提交或精确重放。
	DeliveryMutationApplied DeliveryMutationDisposition = "applied"
	// DeliveryMutationStale 表示旧 Fence 可安全 no-op。
	DeliveryMutationStale DeliveryMutationDisposition = "stale"
	// DeliveryMutationCommitted 表示业务结果已归约，transport 可返回成功。
	DeliveryMutationCommitted DeliveryMutationDisposition = "committed"
)

// DeliveryHeartbeatCommand 使用完整 Fence 与租约时长请求数据库时间续租。
type DeliveryHeartbeatCommand struct {
	Fence         domain.DeliveryFence
	LeaseDuration time.Duration
}

// DeliveryMutationResult 返回 Fence 命令的持久化 Delivery、Attempt 与新 Fence。
type DeliveryMutationResult struct {
	Disposition DeliveryMutationDisposition
	Delivery    domain.Delivery
	Attempt     domain.DeliveryAttempt
	Fence       domain.DeliveryFence
	Replayed    bool
}

// DeliveryCheckpointCommand 使用完整 Fence 持久化一个完整重放绑定，不携带 now。
type DeliveryCheckpointCommand struct {
	Fence      domain.DeliveryFence
	Checkpoint domain.DeliveryCheckpoint
}

// DeliveryFailureCommand 只携带稳定失败事实与重试时长，不携带原始 error 或 now。
type DeliveryFailureCommand struct {
	Fence      domain.DeliveryFence
	Failure    domain.DeliveryFailure
	RetryDelay time.Duration
}

// DeliveryRuntime 负责 Reindex Delivery Runtime 的 Application 输入与返回契约校验。
type DeliveryRuntime struct {
	port DeliveryRuntimePort
}

// NewDeliveryRuntime 构造不依赖 pgx 或 River 类型的 Delivery Runtime。
func NewDeliveryRuntime(port DeliveryRuntimePort) (*DeliveryRuntime, error) {
	if isNilDeliveryRuntimePort(port) {
		return nil, deliveryRuntimeError(foundation.ErrorDependencyUnavailable, "REINDEX_DELIVERY_RUNTIME_PORT_MISSING")
	}
	return &DeliveryRuntime{port: port}, nil
}

// Claim 规范化稳定文本后把 DB-time Claim 委托给端口，并校验完整 Fence。
func (runtime *DeliveryRuntime) Claim(ctx context.Context, command DeliveryClaimCommand) (DeliveryClaimResult, error) {
	if runtime == nil || isNilDeliveryRuntimePort(runtime.port) {
		return DeliveryClaimResult{}, deliveryRuntimeError(foundation.ErrorDependencyUnavailable, "REINDEX_DELIVERY_RUNTIME_PORT_MISSING")
	}
	command.DeliveryKey = strings.TrimSpace(command.DeliveryKey)
	command.LeaseOwner = strings.TrimSpace(command.LeaseOwner)
	if !validDeliveryRuntimeID(command.DeliveryID) || command.DispatchNo < 1 || command.RiverJobID < 1 || command.RiverAttempt < 1 ||
		!validDeliveryRuntimeText(command.DeliveryKey, 256) || !validDeliveryRuntimeText(command.LeaseOwner, 256) || command.LeaseDuration <= 0 {
		return DeliveryClaimResult{}, deliveryRuntimeError(foundation.ErrorInvalidInput, "REINDEX_DELIVERY_CLAIM_INVALID")
	}
	result, err := runtime.port.Claim(ctx, command)
	if err != nil {
		return DeliveryClaimResult{}, err
	}
	if !validDeliveryClaimResult(command, result) {
		return DeliveryClaimResult{}, deliveryRuntimeError(foundation.ErrorConsistencyViolation, "REINDEX_DELIVERY_CLAIM_RESULT_INVALID")
	}
	return result, nil
}

// Heartbeat 只传租约时长，由端口使用数据库时间续租并返回更新后的完整 Fence。
func (runtime *DeliveryRuntime) Heartbeat(ctx context.Context, command DeliveryHeartbeatCommand) (DeliveryMutationResult, error) {
	if runtime == nil || isNilDeliveryRuntimePort(runtime.port) {
		return DeliveryMutationResult{}, deliveryRuntimeError(foundation.ErrorDependencyUnavailable, "REINDEX_DELIVERY_RUNTIME_PORT_MISSING")
	}
	command.Fence.Owner = strings.TrimSpace(command.Fence.Owner)
	if domain.ValidateDeliveryFence(command.Fence) != nil || command.LeaseDuration <= 0 {
		return DeliveryMutationResult{}, deliveryRuntimeError(foundation.ErrorInvalidInput, "REINDEX_DELIVERY_HEARTBEAT_INVALID")
	}
	result, err := runtime.port.Heartbeat(ctx, command)
	if err != nil {
		return DeliveryMutationResult{}, err
	}
	if !validHeartbeatResult(command, result) {
		return DeliveryMutationResult{}, deliveryRuntimeError(foundation.ErrorConsistencyViolation, "REINDEX_DELIVERY_HEARTBEAT_RESULT_INVALID")
	}
	return result, nil
}

// Checkpoint 校验阶段完整性后提交不可逆检查点，并验证端口返回的精确重放绑定。
func (runtime *DeliveryRuntime) Checkpoint(ctx context.Context, command DeliveryCheckpointCommand) (DeliveryMutationResult, error) {
	if runtime == nil || isNilDeliveryRuntimePort(runtime.port) {
		return DeliveryMutationResult{}, deliveryRuntimeError(foundation.ErrorDependencyUnavailable, "REINDEX_DELIVERY_RUNTIME_PORT_MISSING")
	}
	command.Fence.Owner = strings.TrimSpace(command.Fence.Owner)
	if domain.ValidateDeliveryFence(command.Fence) != nil || domain.ValidateDeliveryCheckpoint(command.Checkpoint) != nil {
		return DeliveryMutationResult{}, deliveryRuntimeError(foundation.ErrorInvalidInput, "REINDEX_DELIVERY_CHECKPOINT_INVALID")
	}
	result, err := runtime.port.Checkpoint(ctx, command)
	if err != nil {
		return DeliveryMutationResult{}, err
	}
	if !validCheckpointResult(command, result) {
		return DeliveryMutationResult{}, deliveryRuntimeError(foundation.ErrorConsistencyViolation, "REINDEX_DELIVERY_CHECKPOINT_RESULT_INVALID")
	}
	return result, nil
}

// Fail 规范化安全失败摘要后提交业务归约，并显式返回 stale 或 committed。
func (runtime *DeliveryRuntime) Fail(ctx context.Context, command DeliveryFailureCommand) (DeliveryMutationResult, error) {
	if runtime == nil || isNilDeliveryRuntimePort(runtime.port) {
		return DeliveryMutationResult{}, deliveryRuntimeError(foundation.ErrorDependencyUnavailable, "REINDEX_DELIVERY_RUNTIME_PORT_MISSING")
	}
	command.Fence.Owner = strings.TrimSpace(command.Fence.Owner)
	failure, err := domain.NormalizeDeliveryFailure(command.Failure)
	if domain.ValidateDeliveryFence(command.Fence) != nil || err != nil || !validDeliveryRetryDelay(failure.Class, command.RetryDelay) {
		return DeliveryMutationResult{}, deliveryRuntimeError(foundation.ErrorInvalidInput, "REINDEX_DELIVERY_FAILURE_INVALID")
	}
	command.Failure = failure
	result, err := runtime.port.Fail(ctx, command)
	if err != nil {
		return DeliveryMutationResult{}, err
	}
	if !validFailureResult(command, result) {
		return DeliveryMutationResult{}, deliveryRuntimeError(foundation.ErrorConsistencyViolation, "REINDEX_DELIVERY_FAILURE_RESULT_INVALID")
	}
	return result, nil
}

func validCheckpointResult(command DeliveryCheckpointCommand, result DeliveryMutationResult) bool {
	switch result.Disposition {
	case DeliveryMutationStale:
		return zeroDeliveryMutationResult(result)
	case DeliveryMutationCommitted:
		return result.Fence == (domain.DeliveryFence{}) && validCommittedFenceFacts(command.Fence, result.Delivery, result.Attempt) && domain.DeliveryCheckpointMatches(result.Delivery, result.Attempt, command.Checkpoint)
	case DeliveryMutationApplied:
		return validAppliedCheckpointMutation(command.Fence, result) && domain.DeliveryCheckpointMatches(result.Delivery, result.Attempt, command.Checkpoint)
	default:
		return false
	}
}

func validAppliedCheckpointMutation(request domain.DeliveryFence, result DeliveryMutationResult) bool {
	if !validAppliedDeliveryMutationIdentity(request, result) {
		return false
	}
	if result.Replayed {
		return result.Delivery.Version >= request.DeliveryVersion
	}
	return result.Delivery.Version > request.DeliveryVersion
}

func validFailureResult(command DeliveryFailureCommand, result DeliveryMutationResult) bool {
	if result.Disposition == DeliveryMutationStale {
		return zeroDeliveryMutationResult(result)
	}
	if result.Disposition != DeliveryMutationCommitted || result.Fence != (domain.DeliveryFence{}) || !validCommittedFenceFacts(command.Fence, result.Delivery, result.Attempt) ||
		result.Delivery.Failure == nil || result.Attempt.Failure == nil || *result.Delivery.Failure != command.Failure || *result.Attempt.Failure != command.Failure {
		return false
	}
	if command.Failure.Class == domain.DeliveryFailureRetryable {
		return result.Delivery.Status == domain.DeliveryStatusRetryWait && result.Attempt.Status == domain.DeliveryAttemptRetryWait && result.Delivery.NextAttemptAt != nil && result.Delivery.NextAttemptAt.After(result.Delivery.UpdatedAt)
	}
	if command.Failure.Class == domain.DeliveryFailureNonRetryable {
		return result.Delivery.Status == domain.DeliveryStatusFailed && result.Attempt.Status == domain.DeliveryAttemptFailed
	}
	return result.Delivery.Status == domain.DeliveryStatusManualRecovery && result.Attempt.Status == domain.DeliveryAttemptManualRecovery
}

func validHeartbeatResult(command DeliveryHeartbeatCommand, result DeliveryMutationResult) bool {
	switch result.Disposition {
	case DeliveryMutationStale:
		return zeroDeliveryMutationResult(result)
	case DeliveryMutationCommitted:
		return result.Fence == (domain.DeliveryFence{}) && validCommittedFenceFacts(command.Fence, result.Delivery, result.Attempt)
	case DeliveryMutationApplied:
		return validAppliedDeliveryMutation(command.Fence, result)
	default:
		return false
	}
}

func validCommittedFenceFacts(fence domain.DeliveryFence, delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
	return validCommittedDeliveryFacts(fence.DeliveryID, fence.DispatchNo, delivery, attempt) && delivery.Version > fence.DeliveryVersion &&
		delivery.CurrentAttemptID != nil && *delivery.CurrentAttemptID == fence.AttemptID && delivery.AttemptNo == fence.AttemptNo &&
		attempt.ID == fence.AttemptID && attempt.AttemptNo == fence.AttemptNo && attempt.LeaseOwner == fence.Owner
}

func validDeliveryRetryDelay(class domain.DeliveryFailureClass, delay time.Duration) bool {
	if class == domain.DeliveryFailureRetryable {
		return delay > 0 && delay <= MaxDeliveryRetryDelay
	}
	return delay == 0
}

func validAppliedDeliveryMutation(request domain.DeliveryFence, result DeliveryMutationResult) bool {
	return validAppliedDeliveryMutationIdentity(request, result) && result.Delivery.Version > request.DeliveryVersion
}

func validAppliedDeliveryMutationIdentity(request domain.DeliveryFence, result DeliveryMutationResult) bool {
	if domain.ValidateDelivery(result.Delivery) != nil || domain.ValidateDeliveryAttempt(result.Attempt) != nil || domain.ValidateDeliveryFence(result.Fence) != nil ||
		result.Delivery.ID != request.DeliveryID || result.Delivery.Status != domain.DeliveryStatusProcessing || result.Delivery.DispatchNo != request.DispatchNo ||
		result.Delivery.CurrentAttemptID == nil || *result.Delivery.CurrentAttemptID != request.AttemptID ||
		result.Delivery.AttemptNo != request.AttemptNo || result.Attempt.ID != request.AttemptID || result.Attempt.DeliveryID != request.DeliveryID ||
		result.Attempt.DispatchNo != request.DispatchNo || result.Attempt.AttemptNo != request.AttemptNo || result.Attempt.LeaseOwner != request.Owner || result.Attempt.Status != domain.DeliveryAttemptProcessing {
		return false
	}
	return result.Fence.DeliveryID == result.Delivery.ID && result.Fence.DispatchNo == result.Delivery.DispatchNo && result.Fence.AttemptID == result.Attempt.ID &&
		result.Fence.AttemptNo == result.Attempt.AttemptNo && result.Fence.Owner == result.Attempt.LeaseOwner && result.Fence.DeliveryVersion == result.Delivery.Version
}

func zeroDeliveryMutationResult(result DeliveryMutationResult) bool {
	return result.Delivery == (domain.Delivery{}) && result.Attempt == (domain.DeliveryAttempt{}) && result.Fence == (domain.DeliveryFence{}) && !result.Replayed
}

func validDeliveryClaimResult(command DeliveryClaimCommand, result DeliveryClaimResult) bool {
	switch result.Disposition {
	case DeliveryClaimStale:
		return result.Delivery == (domain.Delivery{}) && result.Attempt == (domain.DeliveryAttempt{}) && result.Fence == (domain.DeliveryFence{}) && !result.Replayed && !result.LeaseReclaimed
	case DeliveryClaimCommitted:
		return result.Replayed && !result.LeaseReclaimed && result.Fence == (domain.DeliveryFence{}) && validCommittedDeliveryFacts(command.DeliveryID, command.DispatchNo, result.Delivery, result.Attempt)
	case DeliveryClaimed:
		// Continue below with the full active lease binding.
	default:
		return false
	}
	if domain.ValidateDelivery(result.Delivery) != nil || domain.ValidateDeliveryAttempt(result.Attempt) != nil || domain.ValidateDeliveryFence(result.Fence) != nil {
		return false
	}
	return result.Delivery.ID == command.DeliveryID && result.Delivery.Status == domain.DeliveryStatusProcessing && result.Delivery.DispatchNo == command.DispatchNo &&
		result.Delivery.CurrentAttemptID != nil && *result.Delivery.CurrentAttemptID == result.Attempt.ID && result.Delivery.AttemptNo == result.Attempt.AttemptNo &&
		result.Attempt.DeliveryID == command.DeliveryID && result.Attempt.DispatchNo == command.DispatchNo && result.Attempt.RiverJobID == command.RiverJobID &&
		result.Attempt.RiverAttempt == command.RiverAttempt && result.Attempt.DeliveryKey == command.DeliveryKey && result.Attempt.LeaseOwner == command.LeaseOwner &&
		result.Attempt.Status == domain.DeliveryAttemptProcessing && result.Fence.DeliveryID == result.Delivery.ID && result.Fence.DispatchNo == result.Delivery.DispatchNo &&
		result.Fence.AttemptID == result.Attempt.ID && result.Fence.AttemptNo == result.Attempt.AttemptNo && result.Fence.Owner == result.Attempt.LeaseOwner &&
		result.Fence.DeliveryVersion == result.Delivery.Version
}

func validCommittedDeliveryFacts(deliveryID foundation.ID, dispatchNo int, delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
	if domain.ValidateDelivery(delivery) != nil || domain.ValidateDeliveryAttempt(attempt) != nil || delivery.ID != deliveryID || delivery.DispatchNo != dispatchNo ||
		delivery.CurrentAttemptID == nil || *delivery.CurrentAttemptID != attempt.ID || delivery.AttemptNo != attempt.AttemptNo || attempt.DeliveryID != deliveryID || attempt.DispatchNo != dispatchNo {
		return false
	}
	switch delivery.Status {
	case domain.DeliveryStatusRetryWait:
		return attempt.Status == domain.DeliveryAttemptRetryWait && sameCommittedFailure(delivery, attempt)
	case domain.DeliveryStatusSucceeded:
		return attempt.Status == domain.DeliveryAttemptSucceeded
	case domain.DeliveryStatusFailed:
		return attempt.Status == domain.DeliveryAttemptFailed && sameCommittedFailure(delivery, attempt)
	case domain.DeliveryStatusManualRecovery:
		return attempt.Status == domain.DeliveryAttemptManualRecovery && sameCommittedFailure(delivery, attempt)
	default:
		return false
	}
}

func sameCommittedFailure(delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
	return delivery.Failure != nil && attempt.Failure != nil && *delivery.Failure == *attempt.Failure
}

func validDeliveryRuntimeID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validDeliveryRuntimeText(value string, maximum int) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.RuneCountInString(value) <= maximum
}

func isNilDeliveryRuntimePort(port DeliveryRuntimePort) bool {
	if port == nil {
		return true
	}
	value := reflect.ValueOf(port)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func deliveryRuntimeError(kind foundation.ErrorKind, code string) error {
	return foundation.NewError(kind, code, false, errors.New("reindex delivery runtime application contract is invalid"))
}
