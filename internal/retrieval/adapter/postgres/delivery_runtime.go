package postgres

import (
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func checkpointRebindsCurrentAttempt(delivery domain.Delivery, attempt domain.DeliveryAttempt, checkpoint domain.DeliveryCheckpoint) bool {
	return checkpoint.Stage == domain.DeliveryCheckpointIngested && attempt.IngestionAttemptID == nil &&
		delivery.SourceVersionID != nil && *delivery.SourceVersionID == checkpoint.SourceVersionID &&
		delivery.ParseProjectionID != nil && *delivery.ParseProjectionID == checkpoint.ParseProjectionID
}

type committedReplayMatcher func(domain.Delivery, domain.DeliveryAttempt) bool

func deliveryLeaseExpired() error {
	return foundation.NewError(foundation.ErrorRetryableFailure, "REINDEX_LEASE_EXPIRED", true, errors.New("delivery database lease has expired"))
}

func checkpointCanAdvance(delivery domain.Delivery, checkpoint domain.DeliveryCheckpoint) bool {
	switch checkpoint.Stage {
	case domain.DeliveryCheckpointSourceCaptured:
		return delivery.SourceVersionID == nil
	case domain.DeliveryCheckpointIngested:
		return delivery.SourceVersionID != nil && *delivery.SourceVersionID == checkpoint.SourceVersionID && delivery.ParseProjectionID == nil
	case domain.DeliveryCheckpointIndexBuilding:
		return delivery.SourceVersionID != nil && *delivery.SourceVersionID == checkpoint.SourceVersionID &&
			delivery.ParseProjectionID != nil && *delivery.ParseProjectionID == checkpoint.ParseProjectionID && delivery.IndexVersionID == nil
	case domain.DeliveryCheckpointRegressionPassed:
		return delivery.SourceVersionID != nil && *delivery.SourceVersionID == checkpoint.SourceVersionID &&
			delivery.ParseProjectionID != nil && *delivery.ParseProjectionID == checkpoint.ParseProjectionID &&
			delivery.IndexVersionID != nil && *delivery.IndexVersionID == checkpoint.IndexVersionID &&
			delivery.ExcludedSourceCount != nil && checkpoint.ExcludedSourceCount != nil && *delivery.ExcludedSourceCount == *checkpoint.ExcludedSourceCount && delivery.Regression == nil
	default:
		return false
	}
}

func claimedResult(delivery domain.Delivery, attempt domain.DeliveryAttempt, replayed, reclaimed bool) application.DeliveryClaimResult {
	return application.DeliveryClaimResult{Disposition: application.DeliveryClaimed, Delivery: delivery, Attempt: attempt,
		Fence: deliveryFence(delivery, attempt), Replayed: replayed, LeaseReclaimed: reclaimed}
}

func appliedMutation(delivery domain.Delivery, attempt domain.DeliveryAttempt, replayed bool) application.DeliveryMutationResult {
	return application.DeliveryMutationResult{Disposition: application.DeliveryMutationApplied, Delivery: delivery, Attempt: attempt,
		Fence: deliveryFence(delivery, attempt), Replayed: replayed}
}

func deliveryFence(delivery domain.Delivery, attempt domain.DeliveryAttempt) domain.DeliveryFence {
	return domain.DeliveryFence{DeliveryID: delivery.ID, DispatchNo: delivery.DispatchNo, AttemptID: attempt.ID,
		AttemptNo: attempt.AttemptNo, Owner: attempt.LeaseOwner, DeliveryVersion: delivery.Version}
}

func fenceMatches(fence domain.DeliveryFence, delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
	return committedFenceIdentityMatches(fence, delivery, attempt) && delivery.Version == fence.DeliveryVersion &&
		delivery.Status == domain.DeliveryStatusProcessing && attempt.Status == domain.DeliveryAttemptProcessing
}

func committedFenceIdentityMatches(fence domain.DeliveryFence, delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
	return delivery.ID == fence.DeliveryID && delivery.DispatchNo == fence.DispatchNo && delivery.AttemptNo == fence.AttemptNo &&
		delivery.CurrentAttemptID != nil && *delivery.CurrentAttemptID == fence.AttemptID && attempt.ID == fence.AttemptID &&
		attempt.DeliveryID == fence.DeliveryID && attempt.DispatchNo == fence.DispatchNo && attempt.AttemptNo == fence.AttemptNo && attempt.LeaseOwner == fence.Owner
}

func sameClaimTransport(attempt domain.DeliveryAttempt, command application.DeliveryClaimCommand) bool {
	return attempt.DispatchNo == command.DispatchNo && attempt.RiverJobID == command.RiverJobID && attempt.RiverAttempt == command.RiverAttempt &&
		attempt.DeliveryKey == command.DeliveryKey && attempt.LeaseOwner == command.LeaseOwner
}

func isCommittedDelivery(status domain.DeliveryStatus) bool {
	return status == domain.DeliveryStatusRetryWait || status == domain.DeliveryStatusSucceeded ||
		status == domain.DeliveryStatusFailed || status == domain.DeliveryStatusManualRecovery
}

func failureStatuses(class domain.DeliveryFailureClass) (domain.DeliveryAttemptStatus, domain.DeliveryStatus) {
	switch class {
	case domain.DeliveryFailureRetryable:
		return domain.DeliveryAttemptRetryWait, domain.DeliveryStatusRetryWait
	case domain.DeliveryFailureNonRetryable:
		return domain.DeliveryAttemptFailed, domain.DeliveryStatusFailed
	default:
		return domain.DeliveryAttemptManualRecovery, domain.DeliveryStatusManualRecovery
	}
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
