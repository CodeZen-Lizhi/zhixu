package postgres

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const processorContextInvalidCode = "REINDEX_PROCESSOR_CONTEXT_INVALID"

func processorFenceIdentityMatches(fence domain.DeliveryFence, delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
	return delivery.ID == fence.DeliveryID && delivery.DispatchNo == fence.DispatchNo && delivery.AttemptNo == fence.AttemptNo &&
		delivery.CurrentAttemptID != nil && *delivery.CurrentAttemptID == fence.AttemptID && attempt.ID == fence.AttemptID &&
		attempt.DeliveryID == fence.DeliveryID && attempt.DispatchNo == fence.DispatchNo && attempt.AttemptNo == fence.AttemptNo &&
		attempt.LeaseOwner == fence.Owner
}

func processorDeliveryCommitted(status domain.DeliveryStatus) bool {
	return status == domain.DeliveryStatusRetryWait || status == domain.DeliveryStatusSucceeded ||
		status == domain.DeliveryStatusFailed || status == domain.DeliveryStatusManualRecovery
}
