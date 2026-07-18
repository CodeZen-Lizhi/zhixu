package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateDeliveryAcceptsCanonicalPendingState(t *testing.T) {
	t.Parallel()

	delivery := validPendingDelivery()
	if err := ValidateDelivery(delivery); err != nil {
		t.Fatalf("ValidateDelivery() error = %v", err)
	}
	got := []DeliveryStatus{
		DeliveryStatusPending,
		DeliveryStatusDispatched,
		DeliveryStatusProcessing,
		DeliveryStatusRetryWait,
		DeliveryStatusSucceeded,
		DeliveryStatusFailed,
		DeliveryStatusManualRecovery,
	}
	want := []DeliveryStatus{
		"pending", "dispatched", "processing", "retry_wait", "succeeded", "failed", "manual_recovery",
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("status[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestNormalizeDeliveryFailurePersistsOnlyStableSafeFacts(t *testing.T) {
	t.Parallel()

	failure, err := NormalizeDeliveryFailure(DeliveryFailure{
		Class:     DeliveryFailureRetryable,
		ErrorKind: foundation.ErrorDependencyUnavailable,
		Code:      " REINDEX_DEPENDENCY_BUSY ",
		Summary:   " dependency   is temporarily unavailable ",
	})
	if err != nil {
		t.Fatalf("NormalizeDeliveryFailure() error = %v", err)
	}
	if failure.Code != "REINDEX_DEPENDENCY_BUSY" || failure.Summary != "dependency is temporarily unavailable" {
		t.Fatalf("normalized failure = %+v", failure)
	}

	redacted, err := NormalizeDeliveryFailure(DeliveryFailure{
		Class: DeliveryFailureManualRecovery, ErrorKind: foundation.ErrorManualRecoveryRequired,
		Code: "REINDEX_RESULT_UNKNOWN", Summary: "authorization: bearer secret",
	})
	if err != nil || redacted.Summary != RedactedDeliveryFailureSummary {
		t.Fatalf("redacted failure = %+v, %v", redacted, err)
	}

	bounded, err := NormalizeDeliveryFailure(DeliveryFailure{
		Class: DeliveryFailureNonRetryable, ErrorKind: foundation.ErrorNonRetryableFailure,
		Code: "REINDEX_FAILED", Summary: strings.Repeat("界", MaxDeliveryFailureSummaryRunes+20),
	})
	if err != nil || len([]rune(bounded.Summary)) != MaxDeliveryFailureSummaryRunes {
		t.Fatalf("bounded summary runes=%d err=%v", len([]rune(bounded.Summary)), err)
	}
}

func TestNormalizeDeliveryFailureRejectsClassKindAndCodeContradictions(t *testing.T) {
	t.Parallel()

	tests := []DeliveryFailure{
		{Class: DeliveryFailureRetryable, ErrorKind: foundation.ErrorInvalidInput, Code: "BAD_INPUT", Summary: "bad input"},
		{Class: DeliveryFailureNonRetryable, ErrorKind: foundation.ErrorNonRetryableFailure, Code: "bad-code", Summary: "bad code"},
		{Class: "unknown", ErrorKind: foundation.ErrorNonRetryableFailure, Code: "UNKNOWN", Summary: "unknown"},
	}
	for index, failure := range tests {
		var classified *foundation.Error
		err := func() error {
			_, normalizeErr := NormalizeDeliveryFailure(failure)
			return normalizeErr
		}()
		if !errors.As(err, &classified) || classified.Code != ErrorCodeDeliveryFailureInvalid {
			t.Errorf("case %d error=%v", index, err)
		}
	}
}

func TestValidateDeliveryFenceRequiresCompleteGenerationOwnerAndVersion(t *testing.T) {
	t.Parallel()

	fence := DeliveryFence{DeliveryID: deliveryTestID(1), DispatchNo: 2, AttemptID: deliveryTestID(10), AttemptNo: 3, Owner: "worker-a", DeliveryVersion: 7}
	if err := ValidateDeliveryFence(fence); err != nil {
		t.Fatalf("ValidateDeliveryFence() error = %v", err)
	}
	for index, invalidFence := range []DeliveryFence{
		{},
		{DeliveryID: fence.DeliveryID, DispatchNo: 0, AttemptID: fence.AttemptID, AttemptNo: 3, Owner: fence.Owner, DeliveryVersion: 7},
		{DeliveryID: fence.DeliveryID, DispatchNo: 2, AttemptID: fence.AttemptID, AttemptNo: 0, Owner: fence.Owner, DeliveryVersion: 7},
		{DeliveryID: fence.DeliveryID, DispatchNo: 2, AttemptID: fence.AttemptID, AttemptNo: 3, Owner: " worker-a ", DeliveryVersion: 7},
		{DeliveryID: fence.DeliveryID, DispatchNo: 2, AttemptID: fence.AttemptID, AttemptNo: 3, Owner: fence.Owner, DeliveryVersion: 0},
	} {
		var classified *foundation.Error
		err := ValidateDeliveryFence(invalidFence)
		if !errors.As(err, &classified) || classified.Code != ErrorCodeDeliveryFenceInvalid {
			t.Errorf("case %d error=%v", index, err)
		}
	}
}

func TestValidateAndMatchDeliveryCheckpointUsesFullReplayBinding(t *testing.T) {
	t.Parallel()

	excluded := int64(2)
	checkpoints := []DeliveryCheckpoint{
		{Stage: DeliveryCheckpointSourceCaptured, SourceVersionID: deliveryTestID(6)},
		{Stage: DeliveryCheckpointIngested, SourceVersionID: deliveryTestID(6), IngestionAttemptID: deliveryTestID(15), ParseProjectionID: deliveryTestID(7)},
		{Stage: DeliveryCheckpointIndexBuilding, SourceVersionID: deliveryTestID(6), IngestionAttemptID: deliveryTestID(15), ParseProjectionID: deliveryTestID(7), IndexVersionID: deliveryTestID(8), ExcludedSourceCount: &excluded},
		{Stage: DeliveryCheckpointRegressionPassed, SourceVersionID: deliveryTestID(6), IngestionAttemptID: deliveryTestID(15), ParseProjectionID: deliveryTestID(7), IndexVersionID: deliveryTestID(8), ExcludedSourceCount: &excluded, RegressionCode: RegressionCodeSnapshotStructureV1, RegressionHash: strings.Repeat("a", 64)},
	}
	for index, checkpoint := range checkpoints {
		if err := ValidateDeliveryCheckpoint(checkpoint); err != nil {
			t.Errorf("case %d error=%v", index, err)
		}
	}

	delivery := validPendingDelivery()
	delivery.Status, delivery.DispatchNo, delivery.AttemptNo = DeliveryStatusProcessing, 1, 1
	attemptID := deliveryTestID(10)
	delivery.CurrentAttemptID, delivery.SourceVersionID, delivery.ParseProjectionID = &attemptID, deliveryIDPointer(6), deliveryIDPointer(7)
	delivery.IndexVersionID, delivery.ExcludedSourceCount = deliveryIDPointer(8), &excluded
	delivery.Regression = &DeliveryRegression{Code: RegressionCodeSnapshotStructureV1, Hash: strings.Repeat("a", 64), PassedAt: delivery.CreatedAt.Add(time.Second)}
	attempt := validProcessingAttempt()
	attempt.IngestionAttemptID = deliveryIDPointer(15)
	if !DeliveryCheckpointMatches(delivery, attempt, checkpoints[len(checkpoints)-1]) {
		t.Fatal("full persisted checkpoint did not match exact replay binding")
	}
	wrong := checkpoints[len(checkpoints)-1]
	wrong.RegressionHash = strings.Repeat("b", 64)
	if DeliveryCheckpointMatches(delivery, attempt, wrong) {
		t.Fatal("mismatched regression hash was accepted")
	}
}

func TestValidateDeliveryCheckpointRejectsSkippedOrMixedStages(t *testing.T) {
	t.Parallel()

	excluded := int64(0)
	tests := []DeliveryCheckpoint{
		{},
		{Stage: DeliveryCheckpointSourceCaptured, SourceVersionID: deliveryTestID(6), ParseProjectionID: deliveryTestID(7)},
		{Stage: DeliveryCheckpointIngested, SourceVersionID: deliveryTestID(6), ParseProjectionID: deliveryTestID(7)},
		{Stage: DeliveryCheckpointIndexBuilding, SourceVersionID: deliveryTestID(6), IngestionAttemptID: deliveryTestID(15), ParseProjectionID: deliveryTestID(7), IndexVersionID: deliveryTestID(8)},
		{Stage: DeliveryCheckpointRegressionPassed, SourceVersionID: deliveryTestID(6), IngestionAttemptID: deliveryTestID(15), ParseProjectionID: deliveryTestID(7), IndexVersionID: deliveryTestID(8), ExcludedSourceCount: &excluded, RegressionCode: RegressionCodeSnapshotStructureV1, RegressionHash: "ABC"},
	}
	for index, checkpoint := range tests {
		var classified *foundation.Error
		err := ValidateDeliveryCheckpoint(checkpoint)
		if !errors.As(err, &classified) || classified.Code != ErrorCodeDeliveryCheckpointInvalid {
			t.Errorf("case %d error=%v", index, err)
		}
	}
}

func TestValidateDeliveryAcceptsEveryPersistedStatusWithStrictTuples(t *testing.T) {
	t.Parallel()

	now := validPendingDelivery().CreatedAt
	attemptID := deliveryTestID(5)
	sourceVersionID := deliveryTestID(6)
	projectionID := deliveryTestID(7)
	indexID := deliveryTestID(8)
	activationID := deliveryTestID(9)
	excluded := int64(2)
	retryAt := now.Add(time.Minute)
	completedAt := now.Add(2 * time.Minute)
	regressionAt := now.Add(90 * time.Second)
	retryFailure := &DeliveryFailure{Class: DeliveryFailureRetryable, ErrorKind: foundation.ErrorDependencyUnavailable, Code: "REINDEX_DEPENDENCY_BUSY", Summary: "dependency unavailable"}
	nonRetryableFailure := &DeliveryFailure{Class: DeliveryFailureNonRetryable, ErrorKind: foundation.ErrorInvalidInput, Code: "REINDEX_SOURCE_UNSUPPORTED", Summary: "source format is unsupported"}
	manualFailure := &DeliveryFailure{Class: DeliveryFailureManualRecovery, ErrorKind: foundation.ErrorManualRecoveryRequired, Code: "REINDEX_BINDING_UNKNOWN", Summary: "binding requires manual recovery"}

	tests := []Delivery{
		func() Delivery {
			delivery := validPendingDelivery()
			delivery.Status, delivery.DispatchNo = DeliveryStatusDispatched, 1
			return delivery
		}(),
		func() Delivery {
			delivery := validPendingDelivery()
			delivery.Status, delivery.DispatchNo, delivery.AttemptNo = DeliveryStatusProcessing, 1, 1
			delivery.CurrentAttemptID = &attemptID
			return delivery
		}(),
		func() Delivery {
			delivery := validPendingDelivery()
			delivery.Status, delivery.DispatchNo, delivery.AttemptNo = DeliveryStatusRetryWait, 1, 1
			delivery.CurrentAttemptID, delivery.NextAttemptAt, delivery.Failure = &attemptID, &retryAt, retryFailure
			return delivery
		}(),
		func() Delivery {
			delivery := validPendingDelivery()
			delivery.Status, delivery.DispatchNo, delivery.AttemptNo = DeliveryStatusFailed, 1, 1
			delivery.CurrentAttemptID, delivery.CompletedAt, delivery.Failure = &attemptID, &completedAt, nonRetryableFailure
			return delivery
		}(),
		func() Delivery {
			delivery := validPendingDelivery()
			delivery.Status, delivery.DispatchNo, delivery.AttemptNo = DeliveryStatusManualRecovery, 1, 1
			delivery.CurrentAttemptID, delivery.Failure, delivery.ManualRecoveryRequired = &attemptID, manualFailure, true
			return delivery
		}(),
		func() Delivery {
			delivery := validPendingDelivery()
			delivery.Status, delivery.DispatchNo, delivery.AttemptNo = DeliveryStatusSucceeded, 1, 1
			delivery.CurrentAttemptID, delivery.SourceVersionID, delivery.ParseProjectionID = &attemptID, &sourceVersionID, &projectionID
			delivery.IndexVersionID, delivery.ExcludedSourceCount, delivery.ActivationID = &indexID, &excluded, &activationID
			delivery.Regression = &DeliveryRegression{Code: RegressionCodeSnapshotStructureV1, Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PassedAt: regressionAt}
			delivery.CompletedAt = &completedAt
			return delivery
		}(),
	}

	for index, delivery := range tests {
		if err := ValidateDelivery(delivery); err != nil {
			t.Errorf("case %d status=%s error=%v", index, delivery.Status, err)
		}
	}
}

func TestValidateDeliveryRejectsStatusAndCheckpointContradictions(t *testing.T) {
	t.Parallel()

	attemptID := deliveryTestID(5)
	sourceVersionID := deliveryTestID(6)
	projectionID := deliveryTestID(7)
	now := validPendingDelivery().CreatedAt
	retryAt := now.Add(time.Minute)

	tests := []Delivery{
		func() Delivery {
			delivery := validPendingDelivery()
			delivery.Status = "unknown"
			return delivery
		}(),
		func() Delivery {
			delivery := validPendingDelivery()
			delivery.Status, delivery.DispatchNo, delivery.AttemptNo = DeliveryStatusRetryWait, 1, 1
			delivery.CurrentAttemptID, delivery.NextAttemptAt = &attemptID, &retryAt
			delivery.Failure = &DeliveryFailure{Class: DeliveryFailureNonRetryable, ErrorKind: foundation.ErrorInvalidInput, Code: "BAD", Summary: "bad"}
			return delivery
		}(),
		func() Delivery {
			delivery := validPendingDelivery()
			delivery.Status, delivery.DispatchNo, delivery.AttemptNo = DeliveryStatusManualRecovery, 1, 1
			delivery.CurrentAttemptID = &attemptID
			delivery.Failure = &DeliveryFailure{Class: DeliveryFailureManualRecovery, ErrorKind: foundation.ErrorManualRecoveryRequired, Code: "MANUAL", Summary: "manual"}
			return delivery
		}(),
		func() Delivery {
			delivery := validPendingDelivery()
			delivery.Status, delivery.DispatchNo, delivery.AttemptNo = DeliveryStatusSucceeded, 1, 1
			delivery.CurrentAttemptID = &attemptID
			completedAt := now.Add(time.Minute)
			delivery.CompletedAt = &completedAt
			return delivery
		}(),
		func() Delivery {
			delivery := validPendingDelivery()
			delivery.Status, delivery.DispatchNo, delivery.AttemptNo = DeliveryStatusProcessing, 1, 1
			delivery.CurrentAttemptID, delivery.ParseProjectionID = &attemptID, &projectionID
			return delivery
		}(),
		func() Delivery {
			delivery := validPendingDelivery()
			delivery.Status, delivery.DispatchNo, delivery.AttemptNo = DeliveryStatusProcessing, 1, 1
			delivery.CurrentAttemptID, delivery.SourceVersionID = &attemptID, &sourceVersionID
			excluded := int64(1)
			delivery.ExcludedSourceCount = &excluded
			return delivery
		}(),
		func() Delivery {
			delivery := validPendingDelivery()
			delivery.Status, delivery.DispatchNo, delivery.AttemptNo = DeliveryStatusProcessing, 1, 1
			delivery.CurrentAttemptID, delivery.SourceVersionID, delivery.ParseProjectionID = &attemptID, &sourceVersionID, &projectionID
			indexID, activationID, excluded := deliveryTestID(8), deliveryTestID(9), int64(0)
			delivery.IndexVersionID, delivery.ActivationID, delivery.ExcludedSourceCount = &indexID, &activationID, &excluded
			delivery.Regression = &DeliveryRegression{Code: RegressionCodeSnapshotStructureV1, Hash: strings.Repeat("a", 64), PassedAt: now.Add(time.Second)}
			return delivery
		}(),
	}

	for index, delivery := range tests {
		var classified *foundation.Error
		err := ValidateDelivery(delivery)
		if !errors.As(err, &classified) || classified.Code != ErrorCodeDeliveryInvalid {
			t.Errorf("case %d error=%v", index, err)
		}
	}
}

func TestValidateDeliveryAttemptMapsExactlyToMigrationStatuses(t *testing.T) {
	t.Parallel()

	base := validProcessingAttempt()
	endedAt := base.HeartbeatAt.Add(time.Second)
	ingestionAttemptID := deliveryTestID(15)
	retryFailure := &DeliveryFailure{Class: DeliveryFailureRetryable, ErrorKind: foundation.ErrorRetryableFailure, Code: "REINDEX_RETRY", Summary: "retry requested"}
	nonRetryableFailure := &DeliveryFailure{Class: DeliveryFailureNonRetryable, ErrorKind: foundation.ErrorNonRetryableFailure, Code: "REINDEX_FAILED", Summary: "reindex failed"}
	manualFailure := &DeliveryFailure{Class: DeliveryFailureManualRecovery, ErrorKind: foundation.ErrorConsistencyViolation, Code: "REINDEX_RESULT_UNKNOWN", Summary: "result requires reconciliation"}

	tests := []DeliveryAttempt{
		base,
		func() DeliveryAttempt {
			attempt := base
			attempt.Status, attempt.EndedAt, attempt.IngestionAttemptID = DeliveryAttemptSucceeded, &endedAt, &ingestionAttemptID
			return attempt
		}(),
		func() DeliveryAttempt {
			attempt := base
			attempt.Status, attempt.EndedAt, attempt.Failure = DeliveryAttemptRetryWait, &endedAt, retryFailure
			return attempt
		}(),
		func() DeliveryAttempt {
			attempt := base
			attempt.Status, attempt.EndedAt, attempt.Failure = DeliveryAttemptFailed, &endedAt, nonRetryableFailure
			return attempt
		}(),
		func() DeliveryAttempt {
			attempt := base
			attempt.Status, attempt.EndedAt, attempt.Failure = DeliveryAttemptManualRecovery, &endedAt, manualFailure
			return attempt
		}(),
		func() DeliveryAttempt {
			attempt := base
			attempt.Status, attempt.EndedAt, attempt.Failure = DeliveryAttemptLeaseLost, &endedAt, retryFailure
			return attempt
		}(),
	}
	want := []DeliveryAttemptStatus{"processing", "succeeded", "retry_wait", "failed", "manual_recovery", "lease_lost"}
	for index, attempt := range tests {
		if attempt.Status != want[index] {
			t.Fatalf("status[%d]=%q want=%q", index, attempt.Status, want[index])
		}
		if err := ValidateDeliveryAttempt(attempt); err != nil {
			t.Errorf("case %d status=%s error=%v", index, attempt.Status, err)
		}
	}
}

func TestValidateDeliveryAttemptRejectsAppendOnlyTupleContradictions(t *testing.T) {
	t.Parallel()

	base := validProcessingAttempt()
	endedAt := base.HeartbeatAt.Add(time.Second)
	tests := []DeliveryAttempt{
		func() DeliveryAttempt {
			attempt := base
			attempt.EndedAt = &endedAt
			return attempt
		}(),
		func() DeliveryAttempt {
			attempt := base
			attempt.Status, attempt.EndedAt = DeliveryAttemptSucceeded, &endedAt
			return attempt
		}(),
		func() DeliveryAttempt {
			attempt := base
			attempt.Status, attempt.EndedAt = DeliveryAttemptLeaseLost, &endedAt
			attempt.Failure = &DeliveryFailure{Class: DeliveryFailureManualRecovery, ErrorKind: foundation.ErrorManualRecoveryRequired, Code: "LEASE_UNKNOWN", Summary: "lease unknown"}
			return attempt
		}(),
		func() DeliveryAttempt {
			attempt := base
			attempt.RiverAttempt = 0
			return attempt
		}(),
		func() DeliveryAttempt {
			attempt := base
			attempt.LeaseUntil = attempt.HeartbeatAt.Add(-time.Second)
			return attempt
		}(),
	}
	for index, attempt := range tests {
		var classified *foundation.Error
		err := ValidateDeliveryAttempt(attempt)
		if !errors.As(err, &classified) || classified.Code != ErrorCodeDeliveryAttemptInvalid {
			t.Errorf("case %d error=%v", index, err)
		}
	}
}

func validProcessingAttempt() DeliveryAttempt {
	now := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)
	return DeliveryAttempt{
		ID:           deliveryTestID(10),
		DeliveryID:   deliveryTestID(1),
		AttemptNo:    1,
		DispatchNo:   1,
		RiverJobID:   101,
		RiverAttempt: 1,
		DeliveryKey:  "reindex-delivery-1-dispatch-1",
		LeaseOwner:   "worker-a",
		LeaseUntil:   now.Add(time.Minute),
		Status:       DeliveryAttemptProcessing,
		StartedAt:    now,
		HeartbeatAt:  now,
	}
}

func validPendingDelivery() Delivery {
	now := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)
	return Delivery{
		ID:                   deliveryTestID(1),
		ConsumerName:         "retrieval-reindex-v1",
		OutboxEventID:        deliveryTestID(2),
		WorkspaceID:          deliveryTestID(3),
		WritebackExecutionID: deliveryTestID(4),
		Status:               DeliveryStatusPending,
		Version:              1,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}

func deliveryTestID(value int) foundation.ID {
	return foundation.ID("d0000000-0000-4000-8000-" + deliveryTestDigits(value))
}

func deliveryIDPointer(value int) *foundation.ID {
	id := deliveryTestID(value)
	return &id
}

func deliveryTestDigits(value int) string {
	const digits = "000000000000"
	text := []byte(digits)
	for index := len(text) - 1; value > 0; index-- {
		text[index] = byte('0' + value%10)
		value /= 10
	}
	return string(text)
}
