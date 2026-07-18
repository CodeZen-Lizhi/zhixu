package application

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	completionServiceUnavailableCode  = "REINDEX_COMPLETION_SERVICE_UNAVAILABLE"
	completionOptionsInvalidCode      = "REINDEX_COMPLETION_OPTIONS_INVALID"
	completionPrerequisitePendingCode = "REINDEX_COMPLETION_PREREQUISITE_PENDING"
)

// CompletionStore 隐藏 Reindex 最终激活与跨域完成的 PostgreSQL 事务。
type CompletionStore interface {
	CompleteReindexTx(context.Context, domain.CompleteReindexCommand) (domain.CompleteReindexResult, error)
}

// CompleteReindexRequest 描述 Processor 持有的最新 Fence 与稳定 Activation 幂等身份。
type CompleteReindexRequest struct {
	WorkspaceID              foundation.ID
	Fence                    domain.DeliveryFence
	ActivationIdempotencyKey string
	ActivationReasonCode     string
}

// NewCompletionPrerequisitePending 创建 Store 到 Application 的明确可重试前置状态。
func NewCompletionPrerequisitePending(cause error) error {
	if cause == nil {
		cause = errors.New("completion prerequisite is pending")
	}
	return foundation.NewError(foundation.ErrorRetryableFailure, completionPrerequisitePendingCode, true, cause)
}

// CompletionService 生成 Activation 身份并委托 Store 执行单一完成事务。
type CompletionService struct {
	store      CompletionStore
	ids        foundation.IDGenerator
	retryDelay time.Duration
}

// NewCompletionService 创建 Reindex 完成服务；依赖不完整时拒绝启动。
func NewCompletionService(store CompletionStore, ids foundation.IDGenerator, prerequisiteRetryDelay time.Duration) (*CompletionService, error) {
	if nilDispatcherDependency(store) || nilDispatcherDependency(ids) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, completionServiceUnavailableCode, false, errors.New("retrieval completion dependencies are incomplete"))
	}
	if !validDeliveryRetryDelay(domain.DeliveryFailureRetryable, prerequisiteRetryDelay) {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, completionOptionsInvalidCode, false, errors.New("completion prerequisite retry delay is invalid"))
	}
	return &CompletionService{store: store, ids: ids, retryDelay: prerequisiteRetryDelay}, nil
}

// Complete 使用新候选 Activation ID 执行或精确重放同一逻辑完成。
func (service *CompletionService) Complete(ctx context.Context, request CompleteReindexRequest) (domain.CompleteReindexResult, error) {
	if service == nil || nilDispatcherDependency(service.store) || nilDispatcherDependency(service.ids) {
		return domain.CompleteReindexResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, completionServiceUnavailableCode, false, errors.New("retrieval completion service is unavailable"))
	}
	id, err := service.ids.New()
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	command := domain.CompleteReindexCommand{
		WorkspaceID: request.WorkspaceID, Fence: request.Fence, ActivationID: id,
		ActivationIdempotencyKey: request.ActivationIdempotencyKey,
		ActivationReasonCode:     request.ActivationReasonCode,
	}
	if err := domain.ValidateCompleteReindexCommand(command); err != nil {
		return domain.CompleteReindexResult{}, err
	}
	result, err := service.store.CompleteReindexTx(ctx, command)
	if err == nil {
		return result, nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Code == completionPrerequisitePendingCode &&
		classified.Kind == foundation.ErrorRetryableFailure && classified.Retryable {
		return domain.CompleteReindexResult{}, NewDeliveryFailure(domain.DeliveryFailure{
			Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorRetryableFailure,
			Code: completionPrerequisitePendingCode, Summary: "completion prerequisites are pending",
		}, service.retryDelay, err)
	}
	if failure := completionManualRecoveryFailure(err); failure != nil {
		return domain.CompleteReindexResult{}, failure
	}
	return domain.CompleteReindexResult{}, err
}

func completionManualRecoveryFailure(cause error) error {
	var classified *foundation.Error
	if !errors.As(cause, &classified) || !completionManualRecoveryCode(classified.Code) {
		return nil
	}
	kind := classified.Kind
	switch kind {
	case foundation.ErrorConsistencyViolation, foundation.ErrorVersionConflict, foundation.ErrorManualRecoveryRequired:
	case foundation.ErrorNotFound:
		// Claim 已证明 Delivery 存在；Completion 再观察到缺失属于一致性损坏。
		kind = foundation.ErrorConsistencyViolation
	case foundation.ErrorInvalidInput:
		if classified.Code != domain.ErrorCodeActivationInvalid {
			return nil
		}
		kind = foundation.ErrorConsistencyViolation
	default:
		return nil
	}
	return NewDeliveryFailure(domain.DeliveryFailure{
		Class: domain.DeliveryFailureManualRecovery, ErrorKind: kind,
		Code: classified.Code, Summary: "reindex completion binding requires manual recovery",
	}, 0, cause)
}

func completionManualRecoveryCode(code string) bool {
	switch code {
	case "REINDEX_COMPLETION_ATTEMPT_MISSING",
		"REINDEX_COMPLETION_BINDING_CONFLICT",
		"REINDEX_COMPLETION_COMMIT_FAILED",
		"REINDEX_COMPLETION_DELIVERY_NOT_FOUND",
		"REINDEX_COMPLETION_GATE_FAILED",
		"REINDEX_COMPLETION_INDEX_MISSING",
		"REINDEX_COMPLETION_REPLAY_CONFLICT",
		"REINDEX_COMPLETION_ATTEMPT_UPDATE_FAILED",
		"REINDEX_COMPLETION_DELIVERY_UPDATE_FAILED",
		"REINDEX_COMPLETION_EXECUTION_STALE",
		"REINDEX_COMPLETION_EXECUTION_UPDATE_FAILED",
		"REINDEX_COMPLETION_PROPOSAL_STALE",
		"REINDEX_COMPLETION_PROPOSAL_UPDATE_FAILED",
		"REINDEX_COMPLETION_FACT_QUERY_FAILED",
		"RETRIEVAL_ACTIVATION_IDEMPOTENCY_CONFLICT",
		"RETRIEVAL_ACTIVATION_INVALID",
		"RETRIEVAL_INDEX_NOT_FOUND",
		"RETRIEVAL_PREVIOUS_RETIRE_FAILED",
		"RETRIEVAL_TARGET_ACTIVATE_FAILED":
		return true
	default:
		return false
	}
}
