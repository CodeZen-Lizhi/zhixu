package domain

import (
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeDeliveryInvalid 表示 Reindex Delivery 的身份、状态或持久字段组合非法。
	ErrorCodeDeliveryInvalid = "REINDEX_DELIVERY_INVALID"
	// ErrorCodeDeliveryAttemptInvalid 表示 Reindex Delivery Attempt 的绑定或终态组合非法。
	ErrorCodeDeliveryAttemptInvalid = "REINDEX_DELIVERY_ATTEMPT_INVALID"
	// ErrorCodeDeliveryFailureInvalid 表示失败分类、稳定错误码或安全摘要非法。
	ErrorCodeDeliveryFailureInvalid = "REINDEX_DELIVERY_FAILURE_INVALID"
	// ErrorCodeDeliveryFenceInvalid 表示旧 owner 隔离令牌不完整或不规范。
	ErrorCodeDeliveryFenceInvalid = "REINDEX_DELIVERY_FENCE_INVALID"
	// ErrorCodeDeliveryCheckpointInvalid 表示 Processor 检查点跳级或绑定不完整。
	ErrorCodeDeliveryCheckpointInvalid = "REINDEX_DELIVERY_CHECKPOINT_INVALID"
	// RegressionCodeSnapshotStructureV1 是 M6-B 冻结的结构回归契约。
	RegressionCodeSnapshotStructureV1 = "SNAPSHOT_STRUCTURE_V1"
	// RegressionCodeSnapshotStructureV2 是 M6-C 冻结的 Hybrid 结构回归契约。
	RegressionCodeSnapshotStructureV2 = "SNAPSHOT_STRUCTURE_V2"
	// MaxDeliveryFailureSummaryRunes 是数据库错误摘要允许的最大 Unicode 字符数。
	MaxDeliveryFailureSummaryRunes = 512
	// RedactedDeliveryFailureSummary 是命中敏感信息规则后的稳定替代摘要。
	RedactedDeliveryFailureSummary = "failure details redacted"
)

// DeliveryStatus 是与 migration 00015 完全一致的 Reindex Delivery 状态。
type DeliveryStatus string

const (
	// DeliveryStatusPending 表示 Delivery 已创建但尚未派发。
	DeliveryStatusPending DeliveryStatus = "pending"
	// DeliveryStatusDispatched 表示当前业务 generation 已写入 transport。
	DeliveryStatusDispatched DeliveryStatus = "dispatched"
	// DeliveryStatusProcessing 表示当前 Attempt 持有有效数据库租约。
	DeliveryStatusProcessing DeliveryStatus = "processing"
	// DeliveryStatusRetryWait 表示可重试业务失败已提交并等待下一 generation。
	DeliveryStatusRetryWait DeliveryStatus = "retry_wait"
	// DeliveryStatusSucceeded 表示 Reindex 完成事务已原子提交。
	DeliveryStatusSucceeded DeliveryStatus = "succeeded"
	// DeliveryStatusFailed 表示明确不可重试且无未知副作用的失败已提交。
	DeliveryStatusFailed DeliveryStatus = "failed"
	// DeliveryStatusManualRecovery 表示结果不确定或绑定损坏，需要人工恢复。
	DeliveryStatusManualRecovery DeliveryStatus = "manual_recovery"
)

// DeliveryFailureClass 是与 migration 00015 完全一致的业务失败分类。
type DeliveryFailureClass string

const (
	// DeliveryFailureRetryable 表示失败可由下一业务 generation 重试。
	DeliveryFailureRetryable DeliveryFailureClass = "retryable"
	// DeliveryFailureNonRetryable 表示失败明确不可自动重试。
	DeliveryFailureNonRetryable DeliveryFailureClass = "non_retryable"
	// DeliveryFailureManualRecovery 表示失败具有未知副作用或一致性风险。
	DeliveryFailureManualRecovery DeliveryFailureClass = "manual_recovery"
)

// DeliveryFailure 是允许持久化的稳定失败事实，不包含原始 error 或敏感上下文。
type DeliveryFailure struct {
	Class     DeliveryFailureClass
	ErrorKind foundation.ErrorKind
	Code      string
	Summary   string
}

// DeliveryFence 将结果命令绑定到 Delivery generation、Attempt、owner 与乐观版本。
type DeliveryFence struct {
	DeliveryID      foundation.ID
	DispatchNo      int
	AttemptID       foundation.ID
	AttemptNo       int
	Owner           string
	DeliveryVersion int64
}

// DeliveryRegression 是已通过结构回归的不可变检查点。
type DeliveryRegression struct {
	Code     string
	Hash     string
	PassedAt time.Time
}

// DeliveryAttemptStatus 是与 migration 00015 完全一致的 append-only Attempt 状态。
type DeliveryAttemptStatus string

const (
	// DeliveryAttemptProcessing 表示 Attempt 正持有数据库租约。
	DeliveryAttemptProcessing DeliveryAttemptStatus = "processing"
	// DeliveryAttemptSucceeded 表示 Attempt 已产生完成事务所需的 Ingestion 证据。
	DeliveryAttemptSucceeded DeliveryAttemptStatus = "succeeded"
	// DeliveryAttemptRetryWait 表示可重试业务失败已归约。
	DeliveryAttemptRetryWait DeliveryAttemptStatus = "retry_wait"
	// DeliveryAttemptFailed 表示不可重试业务失败已归约。
	DeliveryAttemptFailed DeliveryAttemptStatus = "failed"
	// DeliveryAttemptManualRecovery 表示 Attempt 结果需要人工恢复。
	DeliveryAttemptManualRecovery DeliveryAttemptStatus = "manual_recovery"
	// DeliveryAttemptLeaseLost 表示数据库时间已证明旧 owner 租约失效。
	DeliveryAttemptLeaseLost DeliveryAttemptStatus = "lease_lost"
)

// DeliveryAttempt 是一次取得 Reindex Delivery 租约后追加的持久执行事实。
type DeliveryAttempt struct {
	ID                 foundation.ID
	DeliveryID         foundation.ID
	AttemptNo          int
	DispatchNo         int
	RiverJobID         int64
	RiverAttempt       int
	DeliveryKey        string
	LeaseOwner         string
	LeaseUntil         time.Time
	Status             DeliveryAttemptStatus
	IngestionAttemptID *foundation.ID
	Failure            *DeliveryFailure
	StartedAt          time.Time
	HeartbeatAt        time.Time
	EndedAt            *time.Time
}

// DeliveryCheckpointStage 是由持久字段推导出的不可逆 Processor 检查点。
type DeliveryCheckpointStage string

const (
	// DeliveryCheckpointSourceCaptured 表示 committed SourceVersion 已冻结。
	DeliveryCheckpointSourceCaptured DeliveryCheckpointStage = "source_captured"
	// DeliveryCheckpointIngested 表示成功 Ingestion Attempt 与 Projection 已冻结。
	DeliveryCheckpointIngested DeliveryCheckpointStage = "ingested"
	// DeliveryCheckpointIndexBuilding 表示完整 Workspace Snapshot Index 已创建。
	DeliveryCheckpointIndexBuilding DeliveryCheckpointStage = "index_building"
	// DeliveryCheckpointRegressionPassed 表示 SNAPSHOT_STRUCTURE_V1 已通过。
	DeliveryCheckpointRegressionPassed DeliveryCheckpointStage = "regression_passed"
)

// DeliveryCheckpoint 携带某一阶段的完整幂等重放绑定，不携带调用方时间。
type DeliveryCheckpoint struct {
	Stage               DeliveryCheckpointStage
	SourceVersionID     foundation.ID
	IngestionAttemptID  foundation.ID
	ParseProjectionID   foundation.ID
	IndexVersionID      foundation.ID
	ExcludedSourceCount *int64
	RegressionCode      string
	RegressionHash      string
}

// Delivery 是 Reindex Outbox 独立业务投递的持久领域投影。
type Delivery struct {
	ID                     foundation.ID
	ConsumerName           string
	OutboxEventID          foundation.ID
	WorkspaceID            foundation.ID
	WritebackExecutionID   foundation.ID
	Status                 DeliveryStatus
	DispatchNo             int
	AttemptNo              int
	CurrentAttemptID       *foundation.ID
	Version                int64
	NextAttemptAt          *time.Time
	SourceVersionID        *foundation.ID
	ParseProjectionID      *foundation.ID
	IndexVersionID         *foundation.ID
	ActivationID           *foundation.ID
	ExcludedSourceCount    *int64
	Regression             *DeliveryRegression
	Failure                *DeliveryFailure
	ManualRecoveryRequired bool
	CreatedAt              time.Time
	UpdatedAt              time.Time
	CompletedAt            *time.Time
}

// ValidateDelivery 校验 Delivery 的规范身份、时间和状态字段组合。
func ValidateDelivery(delivery Delivery) error {
	if !canonicalDeliveryID(delivery.ID) || !canonicalDeliveryID(delivery.OutboxEventID) ||
		!canonicalDeliveryID(delivery.WorkspaceID) || !canonicalDeliveryID(delivery.WritebackExecutionID) ||
		strings.TrimSpace(delivery.ConsumerName) != delivery.ConsumerName || delivery.ConsumerName == "" || utf8.RuneCountInString(delivery.ConsumerName) > 128 ||
		delivery.Version < 1 || delivery.CreatedAt.IsZero() || delivery.UpdatedAt.IsZero() || delivery.UpdatedAt.Before(delivery.CreatedAt) {
		return invalid(ErrorCodeDeliveryInvalid, "delivery identity or lifecycle metadata is invalid")
	}
	if !validDeliveryStatus(delivery.Status) || delivery.DispatchNo < 0 || delivery.AttemptNo < 0 || delivery.AttemptNo > 0 && delivery.DispatchNo < 1 {
		return invalid(ErrorCodeDeliveryInvalid, "delivery counters or status are invalid")
	}
	if delivery.CurrentAttemptID != nil && !canonicalDeliveryID(*delivery.CurrentAttemptID) ||
		delivery.SourceVersionID != nil && !canonicalDeliveryID(*delivery.SourceVersionID) ||
		delivery.ParseProjectionID != nil && !canonicalDeliveryID(*delivery.ParseProjectionID) ||
		delivery.IndexVersionID != nil && !canonicalDeliveryID(*delivery.IndexVersionID) ||
		delivery.ActivationID != nil && !canonicalDeliveryID(*delivery.ActivationID) {
		return invalid(ErrorCodeDeliveryInvalid, "delivery checkpoint identity is invalid")
	}
	if delivery.AttemptNo == 0 && delivery.CurrentAttemptID != nil || delivery.AttemptNo > 0 && delivery.CurrentAttemptID == nil {
		return invalid(ErrorCodeDeliveryInvalid, "delivery current attempt binding is invalid")
	}
	if delivery.NextAttemptAt != nil && (delivery.NextAttemptAt.IsZero() || delivery.NextAttemptAt.Before(delivery.CreatedAt)) ||
		delivery.CompletedAt != nil && (delivery.CompletedAt.IsZero() || delivery.CompletedAt.Before(delivery.CreatedAt)) {
		return invalid(ErrorCodeDeliveryInvalid, "delivery persisted time is invalid")
	}
	if err := validateDeliveryCheckpoints(delivery); err != nil {
		return err
	}
	if err := validateDeliveryStatusTuple(delivery); err != nil {
		return err
	}
	return nil
}

// ValidateDeliveryAttempt 校验 Attempt 身份、DB-time lease 投影和 append-only 状态元组。
func ValidateDeliveryAttempt(attempt DeliveryAttempt) error {
	if !canonicalDeliveryID(attempt.ID) || !canonicalDeliveryID(attempt.DeliveryID) || attempt.AttemptNo < 1 || attempt.DispatchNo < 1 ||
		attempt.RiverJobID < 1 || attempt.RiverAttempt < 1 || !canonicalBoundedDeliveryText(attempt.DeliveryKey, 256) || !canonicalBoundedDeliveryText(attempt.LeaseOwner, 256) ||
		attempt.StartedAt.IsZero() || attempt.HeartbeatAt.IsZero() || attempt.LeaseUntil.IsZero() || attempt.HeartbeatAt.Before(attempt.StartedAt) || attempt.LeaseUntil.Before(attempt.HeartbeatAt) {
		return invalid(ErrorCodeDeliveryAttemptInvalid, "delivery attempt identity, lease, or counters are invalid")
	}
	if attempt.IngestionAttemptID != nil && !canonicalDeliveryID(*attempt.IngestionAttemptID) {
		return invalid(ErrorCodeDeliveryAttemptInvalid, "ingestion attempt identity is invalid")
	}
	if attempt.EndedAt != nil && (attempt.EndedAt.IsZero() || attempt.EndedAt.Before(attempt.HeartbeatAt)) {
		return invalid(ErrorCodeDeliveryAttemptInvalid, "delivery attempt end time is invalid")
	}
	switch attempt.Status {
	case DeliveryAttemptProcessing:
		if attempt.EndedAt != nil || attempt.Failure != nil {
			return invalid(ErrorCodeDeliveryAttemptInvalid, "processing attempt cannot carry a terminal result")
		}
	case DeliveryAttemptSucceeded:
		if attempt.EndedAt == nil || attempt.IngestionAttemptID == nil || attempt.Failure != nil {
			return invalid(ErrorCodeDeliveryAttemptInvalid, "succeeded attempt requires ingestion evidence")
		}
	case DeliveryAttemptRetryWait:
		if attempt.EndedAt == nil || !failureHasClass(attempt.Failure, DeliveryFailureRetryable) {
			return invalid(ErrorCodeDeliveryAttemptInvalid, "retry-wait attempt failure is invalid")
		}
	case DeliveryAttemptFailed:
		if attempt.EndedAt == nil || !failureHasClass(attempt.Failure, DeliveryFailureNonRetryable) {
			return invalid(ErrorCodeDeliveryAttemptInvalid, "failed attempt failure is invalid")
		}
	case DeliveryAttemptManualRecovery:
		if attempt.EndedAt == nil || !failureHasClass(attempt.Failure, DeliveryFailureManualRecovery) {
			return invalid(ErrorCodeDeliveryAttemptInvalid, "manual-recovery attempt failure is invalid")
		}
	case DeliveryAttemptLeaseLost:
		if attempt.EndedAt == nil || !failureHasClass(attempt.Failure, DeliveryFailureRetryable) {
			return invalid(ErrorCodeDeliveryAttemptInvalid, "lease-lost attempt failure is invalid")
		}
	default:
		return invalid(ErrorCodeDeliveryAttemptInvalid, "delivery attempt status is invalid")
	}
	if attempt.Failure != nil {
		if err := validatePersistedDeliveryFailure(*attempt.Failure); err != nil {
			return invalid(ErrorCodeDeliveryAttemptInvalid, "delivery attempt failure tuple is invalid")
		}
	}
	return nil
}

// NormalizeDeliveryFailure 规范化稳定错误码与安全摘要，并拒绝分类和 ErrorKind 错配。
func NormalizeDeliveryFailure(failure DeliveryFailure) (DeliveryFailure, error) {
	failure.Code = strings.TrimSpace(failure.Code)
	failure.Summary = sanitizeDeliveryFailureSummary(failure.Summary, failure.Code)
	if !validDeliveryFailureClass(failure.Class) || !validDeliveryFailureKind(failure.Class, failure.ErrorKind) || !canonicalDeliveryCode(failure.Code) || failure.Summary == "" || utf8.RuneCountInString(failure.Summary) > MaxDeliveryFailureSummaryRunes {
		return DeliveryFailure{}, invalid(ErrorCodeDeliveryFailureInvalid, "delivery failure is invalid")
	}
	return failure, nil
}

// ValidateDeliveryFence 校验完整 generation、Attempt、owner 与 Delivery version 隔离令牌。
func ValidateDeliveryFence(fence DeliveryFence) error {
	if !canonicalDeliveryID(fence.DeliveryID) || fence.DispatchNo < 1 || !canonicalDeliveryID(fence.AttemptID) || fence.AttemptNo < 1 || !canonicalBoundedDeliveryText(fence.Owner, 256) || fence.DeliveryVersion < 1 {
		return invalid(ErrorCodeDeliveryFenceInvalid, "delivery fence is invalid")
	}
	return nil
}

// ValidateDeliveryCheckpoint 校验每个 Processor 阶段都携带从 Source 起的完整重放绑定。
func ValidateDeliveryCheckpoint(checkpoint DeliveryCheckpoint) error {
	if !canonicalDeliveryID(checkpoint.SourceVersionID) {
		return invalid(ErrorCodeDeliveryCheckpointInvalid, "checkpoint source version is invalid")
	}
	switch checkpoint.Stage {
	case DeliveryCheckpointSourceCaptured:
		if checkpoint.IngestionAttemptID != "" || checkpoint.ParseProjectionID != "" || checkpoint.IndexVersionID != "" || checkpoint.ExcludedSourceCount != nil || checkpoint.RegressionCode != "" || checkpoint.RegressionHash != "" {
			return invalid(ErrorCodeDeliveryCheckpointInvalid, "source checkpoint contains later-stage fields")
		}
	case DeliveryCheckpointIngested:
		if !canonicalDeliveryID(checkpoint.IngestionAttemptID) || !canonicalDeliveryID(checkpoint.ParseProjectionID) || checkpoint.IndexVersionID != "" || checkpoint.ExcludedSourceCount != nil || checkpoint.RegressionCode != "" || checkpoint.RegressionHash != "" {
			return invalid(ErrorCodeDeliveryCheckpointInvalid, "ingestion checkpoint is incomplete")
		}
	case DeliveryCheckpointIndexBuilding:
		if !validIndexCheckpoint(checkpoint) || checkpoint.RegressionCode != "" || checkpoint.RegressionHash != "" {
			return invalid(ErrorCodeDeliveryCheckpointInvalid, "index checkpoint is incomplete")
		}
	case DeliveryCheckpointRegressionPassed:
		if !validIndexCheckpoint(checkpoint) || !validDeliveryRegressionCode(checkpoint.RegressionCode) || !canonicalDeliveryHash(checkpoint.RegressionHash) {
			return invalid(ErrorCodeDeliveryCheckpointInvalid, "regression checkpoint is incomplete")
		}
	default:
		return invalid(ErrorCodeDeliveryCheckpointInvalid, "checkpoint stage is invalid")
	}
	return nil
}

// DeliveryCheckpointMatches 判断持久 Delivery/Attempt 是否精确覆盖指定检查点重放绑定。
func DeliveryCheckpointMatches(delivery Delivery, attempt DeliveryAttempt, checkpoint DeliveryCheckpoint) bool {
	if ValidateDelivery(delivery) != nil || ValidateDeliveryAttempt(attempt) != nil || ValidateDeliveryCheckpoint(checkpoint) != nil ||
		delivery.ID != attempt.DeliveryID || delivery.CurrentAttemptID == nil || *delivery.CurrentAttemptID != attempt.ID ||
		delivery.SourceVersionID == nil || *delivery.SourceVersionID != checkpoint.SourceVersionID {
		return false
	}
	if checkpoint.Stage == DeliveryCheckpointSourceCaptured {
		return true
	}
	if delivery.ParseProjectionID == nil || *delivery.ParseProjectionID != checkpoint.ParseProjectionID || attempt.IngestionAttemptID == nil || *attempt.IngestionAttemptID != checkpoint.IngestionAttemptID {
		return false
	}
	if checkpoint.Stage == DeliveryCheckpointIngested {
		return true
	}
	if delivery.IndexVersionID == nil || *delivery.IndexVersionID != checkpoint.IndexVersionID || delivery.ExcludedSourceCount == nil || checkpoint.ExcludedSourceCount == nil || *delivery.ExcludedSourceCount != *checkpoint.ExcludedSourceCount {
		return false
	}
	if checkpoint.Stage == DeliveryCheckpointIndexBuilding {
		return true
	}
	return delivery.Regression != nil && delivery.Regression.Code == checkpoint.RegressionCode && delivery.Regression.Hash == checkpoint.RegressionHash
}

func validateDeliveryStatusTuple(delivery Delivery) error {
	switch delivery.Status {
	case DeliveryStatusPending:
		if delivery.DispatchNo != 0 || delivery.AttemptNo != 0 || delivery.CurrentAttemptID != nil || delivery.NextAttemptAt != nil || delivery.CompletedAt != nil || delivery.Failure != nil || delivery.ManualRecoveryRequired || hasDeliveryCheckpoint(delivery) {
			return invalid(ErrorCodeDeliveryInvalid, "pending delivery state is invalid")
		}
	case DeliveryStatusDispatched:
		if delivery.DispatchNo < 1 || delivery.NextAttemptAt != nil || delivery.CompletedAt != nil || delivery.Failure != nil || delivery.ManualRecoveryRequired {
			return invalid(ErrorCodeDeliveryInvalid, "dispatched delivery state is invalid")
		}
	case DeliveryStatusProcessing:
		if delivery.DispatchNo < 1 || delivery.AttemptNo < 1 || delivery.CurrentAttemptID == nil || delivery.NextAttemptAt != nil || delivery.CompletedAt != nil || delivery.Failure != nil || delivery.ManualRecoveryRequired {
			return invalid(ErrorCodeDeliveryInvalid, "processing delivery state is invalid")
		}
	case DeliveryStatusRetryWait:
		if delivery.DispatchNo < 1 || delivery.AttemptNo < 1 || delivery.CurrentAttemptID == nil || delivery.NextAttemptAt == nil || delivery.CompletedAt != nil || delivery.ManualRecoveryRequired || !failureHasClass(delivery.Failure, DeliveryFailureRetryable) {
			return invalid(ErrorCodeDeliveryInvalid, "retry-wait delivery state is invalid")
		}
	case DeliveryStatusSucceeded:
		if delivery.DispatchNo < 1 || delivery.AttemptNo < 1 || delivery.CurrentAttemptID == nil || delivery.NextAttemptAt != nil || delivery.CompletedAt == nil || delivery.Failure != nil || delivery.ManualRecoveryRequired || delivery.SourceVersionID == nil || delivery.ParseProjectionID == nil || delivery.IndexVersionID == nil || delivery.ActivationID == nil || delivery.ExcludedSourceCount == nil || delivery.Regression == nil {
			return invalid(ErrorCodeDeliveryInvalid, "succeeded delivery state is incomplete")
		}
	case DeliveryStatusFailed:
		if delivery.DispatchNo < 1 || delivery.AttemptNo < 1 || delivery.CurrentAttemptID == nil || delivery.NextAttemptAt != nil || delivery.CompletedAt == nil || delivery.ManualRecoveryRequired || !failureHasClass(delivery.Failure, DeliveryFailureNonRetryable) {
			return invalid(ErrorCodeDeliveryInvalid, "failed delivery state is invalid")
		}
	case DeliveryStatusManualRecovery:
		if delivery.DispatchNo < 1 || delivery.AttemptNo < 1 || delivery.CurrentAttemptID == nil || delivery.NextAttemptAt != nil || delivery.CompletedAt != nil || !delivery.ManualRecoveryRequired || !failureHasClass(delivery.Failure, DeliveryFailureManualRecovery) {
			return invalid(ErrorCodeDeliveryInvalid, "manual-recovery delivery state is invalid")
		}
	}
	if delivery.Failure != nil {
		if err := validatePersistedDeliveryFailure(*delivery.Failure); err != nil {
			return err
		}
	}
	return nil
}

func validateDeliveryCheckpoints(delivery Delivery) error {
	if delivery.ParseProjectionID != nil && delivery.SourceVersionID == nil {
		return invalid(ErrorCodeDeliveryInvalid, "parse projection requires a source version checkpoint")
	}
	if (delivery.IndexVersionID == nil) != (delivery.ExcludedSourceCount == nil) || delivery.IndexVersionID != nil && delivery.ParseProjectionID == nil {
		return invalid(ErrorCodeDeliveryInvalid, "index and excluded-source checkpoints must be persisted together")
	}
	if delivery.ExcludedSourceCount != nil && *delivery.ExcludedSourceCount < 0 {
		return invalid(ErrorCodeDeliveryInvalid, "excluded source count cannot be negative")
	}
	if delivery.Regression != nil {
		if delivery.IndexVersionID == nil || !validDeliveryRegressionCode(delivery.Regression.Code) || !canonicalDeliveryHash(delivery.Regression.Hash) || delivery.Regression.PassedAt.IsZero() || delivery.Regression.PassedAt.Before(delivery.CreatedAt) {
			return invalid(ErrorCodeDeliveryInvalid, "regression checkpoint is invalid")
		}
	}
	if delivery.ActivationID != nil && delivery.Regression == nil {
		return invalid(ErrorCodeDeliveryInvalid, "activation requires a passed regression checkpoint")
	}
	if (delivery.ActivationID != nil) != (delivery.Status == DeliveryStatusSucceeded) {
		return invalid(ErrorCodeDeliveryInvalid, "activation is exclusive to succeeded delivery")
	}
	return nil
}

func validDeliveryRegressionCode(code string) bool {
	return code == RegressionCodeSnapshotStructureV1 || code == RegressionCodeSnapshotStructureV2
}

func validDeliveryStatus(status DeliveryStatus) bool {
	switch status {
	case DeliveryStatusPending, DeliveryStatusDispatched, DeliveryStatusProcessing, DeliveryStatusRetryWait, DeliveryStatusSucceeded, DeliveryStatusFailed, DeliveryStatusManualRecovery:
		return true
	default:
		return false
	}
}

func failureHasClass(failure *DeliveryFailure, class DeliveryFailureClass) bool {
	return failure != nil && failure.Class == class
}

func validatePersistedDeliveryFailure(failure DeliveryFailure) error {
	normalized, err := NormalizeDeliveryFailure(failure)
	if err != nil || normalized != failure {
		return invalid(ErrorCodeDeliveryInvalid, "delivery failure tuple is invalid")
	}
	return nil
}

func validDeliveryFailureClass(class DeliveryFailureClass) bool {
	return class == DeliveryFailureRetryable || class == DeliveryFailureNonRetryable || class == DeliveryFailureManualRecovery
}

func validDeliveryFailureKind(class DeliveryFailureClass, kind foundation.ErrorKind) bool {
	switch class {
	case DeliveryFailureRetryable:
		return kind == foundation.ErrorDependencyUnavailable || kind == foundation.ErrorRetryableFailure
	case DeliveryFailureNonRetryable:
		return kind == foundation.ErrorInvalidInput || kind == foundation.ErrorNotFound || kind == foundation.ErrorPermissionDenied || kind == foundation.ErrorNonRetryableFailure
	case DeliveryFailureManualRecovery:
		return kind == foundation.ErrorVersionConflict || kind == foundation.ErrorConsistencyViolation || kind == foundation.ErrorManualRecoveryRequired
	default:
		return false
	}
}

func canonicalDeliveryCode(value string) bool {
	if value == "" || len(value) > 128 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func canonicalDeliveryHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func hasDeliveryCheckpoint(delivery Delivery) bool {
	return delivery.SourceVersionID != nil || delivery.ParseProjectionID != nil || delivery.IndexVersionID != nil || delivery.ActivationID != nil || delivery.ExcludedSourceCount != nil || delivery.Regression != nil
}

func canonicalDeliveryID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validIndexCheckpoint(checkpoint DeliveryCheckpoint) bool {
	return canonicalDeliveryID(checkpoint.IngestionAttemptID) && canonicalDeliveryID(checkpoint.ParseProjectionID) && canonicalDeliveryID(checkpoint.IndexVersionID) && checkpoint.ExcludedSourceCount != nil && *checkpoint.ExcludedSourceCount >= 0
}

func sanitizeDeliveryFailureSummary(summary, fallback string) string {
	summary = strings.TrimSpace(strings.Join(strings.Fields(summary), " "))
	if summary == "" {
		summary = fallback
	}
	lower := strings.ToLower(summary)
	for _, marker := range []string{"token=", "token:", "secret=", "secret:", "password=", "password:", "api_key=", "api-key=", "authorization:", "bearer "} {
		if strings.Contains(lower, marker) {
			return RedactedDeliveryFailureSummary
		}
	}
	for _, field := range strings.Fields(summary) {
		if strings.HasPrefix(field, "/") || len(field) >= 3 && field[1] == ':' && (field[2] == '\\' || field[2] == '/') {
			return RedactedDeliveryFailureSummary
		}
	}
	if utf8.RuneCountInString(summary) <= MaxDeliveryFailureSummaryRunes {
		return summary
	}
	runes := []rune(summary)
	return string(runes[:MaxDeliveryFailureSummaryRunes])
}

func canonicalBoundedDeliveryText(value string, maximum int) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.RuneCountInString(value) <= maximum
}
