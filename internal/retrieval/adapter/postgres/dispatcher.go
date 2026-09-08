package postgres

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	// ReindexConsumerName 是 v1 Dispatcher 持久化 Delivery 使用的稳定消费者身份。
	ReindexConsumerName   = "retrieval-reindex-v1"
	outboxContractInvalid = "REINDEX_OUTBOX_CONTRACT_INVALID"
)

type selectedOutbox struct {
	ID          foundation.ID
	WorkspaceID foundation.ID
	RunID       *foundation.ID
	Payload     string
}

type writebackBinding struct {
	Binding         reindexcontract.Binding
	ExecutionStatus string
	ProposalStatus  string
	Commit          reindexcontract.Binding
}

func validateOutboxBinding(event selectedOutbox, request reindexcontract.RequestV1, value writebackBinding) error {
	if event.RunID == nil || event.ID == "" || event.WorkspaceID != request.WorkspaceID || *event.RunID != request.WorkflowRunID ||
		value.ExecutionStatus != "verifying" || value.ProposalStatus != "verifying" {
		return errors.New("outbox identity or writeback lifecycle is not dispatchable")
	}
	if err := reindexcontract.ValidateBinding(request, value.Binding); err != nil {
		return err
	}
	if err := reindexcontract.ValidateBinding(request, value.Commit); err != nil {
		return err
	}
	return nil
}

func validatePendingDeliveryReplay(delivery domain.Delivery, event selectedOutbox, executionID foundation.ID) error {
	if domain.ValidateDelivery(delivery) != nil || delivery.WorkspaceID != event.WorkspaceID || delivery.WritebackExecutionID != executionID ||
		delivery.Status != domain.DeliveryStatusPending || delivery.DispatchNo != 0 || delivery.AttemptNo != 0 ||
		delivery.CurrentAttemptID != nil || delivery.Version != 1 || delivery.NextAttemptAt != nil || delivery.Failure != nil ||
		delivery.SourceVersionID != nil || delivery.ParseProjectionID != nil || delivery.IndexVersionID != nil || delivery.ActivationID != nil ||
		delivery.ExcludedSourceCount != nil || delivery.Regression != nil || delivery.ManualRecoveryRequired || delivery.CompletedAt != nil {
		return consistency("REINDEX_DELIVERY_REPLAY_CONFLICT", errors.New("existing delivery is not the exact pending binding"))
	}
	return nil
}

func poisonedOutbox(cause error) error {
	return foundation.NewError(foundation.ErrorNonRetryableFailure, outboxContractInvalid, false, cause)
}
