package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestDeliveryRuntimeClaimUsesDurationAndReturnsCompleteFence(t *testing.T) {
	t.Parallel()

	delivery, attempt, fence := claimedRuntimeFacts()
	port := &fakeDeliveryRuntimePort{claimResult: DeliveryClaimResult{
		Disposition: DeliveryClaimed,
		Delivery:    delivery,
		Attempt:     attempt,
		Fence:       fence,
	}}
	runtime, err := NewDeliveryRuntime(port)
	if err != nil {
		t.Fatal(err)
	}
	command := DeliveryClaimCommand{
		DeliveryID: runtimeID(1), DispatchNo: 2, RiverJobID: 91, RiverAttempt: 1,
		DeliveryKey: " reindex-job-91 ", LeaseOwner: " worker-a ", LeaseDuration: 30 * time.Second,
	}
	result, err := runtime.Claim(context.Background(), command)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if result.Disposition != DeliveryClaimed || result.Fence != fence {
		t.Fatalf("Claim() result = %+v", result)
	}
	if port.claim.DeliveryKey != "reindex-job-91" || port.claim.LeaseOwner != "worker-a" || port.claim.LeaseDuration != 30*time.Second {
		t.Fatalf("port command = %+v", port.claim)
	}
}

func TestDeliveryRuntimeClaimReturnsExplicitStaleAndCommittedDispositions(t *testing.T) {
	t.Parallel()

	port := &fakeDeliveryRuntimePort{claimResult: DeliveryClaimResult{Disposition: DeliveryClaimStale}}
	runtime, _ := NewDeliveryRuntime(port)
	result, err := runtime.Claim(context.Background(), validDeliveryClaimCommand())
	if err != nil || result.Disposition != DeliveryClaimStale {
		t.Fatalf("stale Claim() = %+v, %v", result, err)
	}

	delivery, attempt := settledRetryFacts()
	port.claimResult = DeliveryClaimResult{Disposition: DeliveryClaimCommitted, Delivery: delivery, Attempt: attempt, Replayed: true}
	result, err = runtime.Claim(context.Background(), validDeliveryClaimCommand())
	if err != nil || result.Disposition != DeliveryClaimCommitted || !result.Replayed {
		t.Fatalf("committed Claim() = %+v, %v", result, err)
	}
}

func TestDeliveryRuntimeClaimRejectsInvalidInputBeforePort(t *testing.T) {
	t.Parallel()

	tests := []DeliveryClaimCommand{
		{},
		{DeliveryID: runtimeID(1), DispatchNo: 0, RiverJobID: 1, RiverAttempt: 1, DeliveryKey: "key", LeaseOwner: "owner", LeaseDuration: time.Second},
		{DeliveryID: runtimeID(1), DispatchNo: 1, RiverJobID: 0, RiverAttempt: 1, DeliveryKey: "key", LeaseOwner: "owner", LeaseDuration: time.Second},
		{DeliveryID: runtimeID(1), DispatchNo: 1, RiverJobID: 1, RiverAttempt: 0, DeliveryKey: "key", LeaseOwner: "owner", LeaseDuration: time.Second},
		{DeliveryID: runtimeID(1), DispatchNo: 1, RiverJobID: 1, RiverAttempt: 1, DeliveryKey: " ", LeaseOwner: "owner", LeaseDuration: time.Second},
		{DeliveryID: runtimeID(1), DispatchNo: 1, RiverJobID: 1, RiverAttempt: 1, DeliveryKey: "key", LeaseOwner: " ", LeaseDuration: time.Second},
		{DeliveryID: runtimeID(1), DispatchNo: 1, RiverJobID: 1, RiverAttempt: 1, DeliveryKey: "key", LeaseOwner: "owner", LeaseDuration: 0},
	}
	for index, command := range tests {
		port := &fakeDeliveryRuntimePort{}
		runtime, _ := NewDeliveryRuntime(port)
		_, err := runtime.Claim(context.Background(), command)
		if deliveryRuntimeErrorCode(err) != "REINDEX_DELIVERY_CLAIM_INVALID" {
			t.Errorf("case %d error=%v", index, err)
		}
		if port.claimCalls != 0 {
			t.Errorf("case %d called port", index)
		}
	}
}

func TestDeliveryRuntimeClaimRejectsContradictoryPortFacts(t *testing.T) {
	t.Parallel()

	delivery, attempt, fence := claimedRuntimeFacts()
	badResults := []DeliveryClaimResult{
		{Disposition: "unknown"},
		{Disposition: DeliveryClaimStale, Delivery: delivery},
		{Disposition: DeliveryClaimed, Delivery: delivery, Attempt: attempt, Fence: func() domain.DeliveryFence { fence.Owner = "other"; return fence }()},
		{Disposition: DeliveryClaimCommitted, Delivery: delivery, Attempt: attempt},
	}
	for index, result := range badResults {
		port := &fakeDeliveryRuntimePort{claimResult: result}
		runtime, _ := NewDeliveryRuntime(port)
		_, err := runtime.Claim(context.Background(), validDeliveryClaimCommand())
		if deliveryRuntimeErrorCode(err) != "REINDEX_DELIVERY_CLAIM_RESULT_INVALID" {
			t.Errorf("case %d error=%v", index, err)
		}
	}
}

func TestNewDeliveryRuntimeRejectsNilAndTypedNilPort(t *testing.T) {
	t.Parallel()

	if _, err := NewDeliveryRuntime(nil); deliveryRuntimeErrorCode(err) != "REINDEX_DELIVERY_RUNTIME_PORT_MISSING" {
		t.Fatalf("nil port error=%v", err)
	}
	var typedNil *fakeDeliveryRuntimePort
	if _, err := NewDeliveryRuntime(typedNil); deliveryRuntimeErrorCode(err) != "REINDEX_DELIVERY_RUNTIME_PORT_MISSING" {
		t.Fatalf("typed nil port error=%v", err)
	}
}

func TestDeliveryRuntimeHeartbeatCarriesFullFenceAndOnlyDuration(t *testing.T) {
	t.Parallel()

	delivery, attempt, fence := claimedRuntimeFacts()
	oldFence := fence
	delivery.Version++
	delivery.UpdatedAt = delivery.UpdatedAt.Add(time.Second)
	attempt.HeartbeatAt = attempt.HeartbeatAt.Add(time.Second)
	attempt.LeaseUntil = attempt.LeaseUntil.Add(time.Minute)
	fence.DeliveryVersion = delivery.Version
	port := &fakeDeliveryRuntimePort{heartbeatResult: DeliveryMutationResult{Disposition: DeliveryMutationApplied, Delivery: delivery, Attempt: attempt, Fence: fence}}
	runtime, _ := NewDeliveryRuntime(port)
	oldFence.Owner = " worker-a "
	result, err := runtime.Heartbeat(context.Background(), DeliveryHeartbeatCommand{Fence: oldFence, LeaseDuration: 45 * time.Second})
	if err != nil {
		t.Fatalf("Heartbeat() error=%v", err)
	}
	if result.Disposition != DeliveryMutationApplied || result.Fence.DeliveryVersion != 8 {
		t.Fatalf("Heartbeat() result=%+v", result)
	}
	if port.heartbeat.Fence.Owner != "worker-a" || port.heartbeat.LeaseDuration != 45*time.Second {
		t.Fatalf("port heartbeat=%+v", port.heartbeat)
	}
}

func TestDeliveryRuntimeHeartbeatReturnsExplicitStaleAndCommitted(t *testing.T) {
	t.Parallel()

	runtimePort := &fakeDeliveryRuntimePort{heartbeatResult: DeliveryMutationResult{Disposition: DeliveryMutationStale}}
	runtime, _ := NewDeliveryRuntime(runtimePort)
	_, _, fence := claimedRuntimeFacts()
	result, err := runtime.Heartbeat(context.Background(), DeliveryHeartbeatCommand{Fence: fence, LeaseDuration: time.Minute})
	if err != nil || result.Disposition != DeliveryMutationStale {
		t.Fatalf("stale Heartbeat()=%+v, %v", result, err)
	}

	delivery, attempt := settledRetryFacts()
	runtimePort.heartbeatResult = DeliveryMutationResult{Disposition: DeliveryMutationCommitted, Delivery: delivery, Attempt: attempt}
	result, err = runtime.Heartbeat(context.Background(), DeliveryHeartbeatCommand{Fence: fence, LeaseDuration: time.Minute})
	if err != nil || result.Disposition != DeliveryMutationCommitted {
		t.Fatalf("committed Heartbeat()=%+v, %v", result, err)
	}
}

func TestDeliveryRuntimeHeartbeatRejectsInvalidInputAndPortContradictions(t *testing.T) {
	t.Parallel()

	_, _, fence := claimedRuntimeFacts()
	for index, command := range []DeliveryHeartbeatCommand{
		{},
		{Fence: fence},
		{Fence: func() domain.DeliveryFence { value := fence; value.AttemptNo = 0; return value }(), LeaseDuration: time.Second},
	} {
		port := &fakeDeliveryRuntimePort{}
		runtime, _ := NewDeliveryRuntime(port)
		_, err := runtime.Heartbeat(context.Background(), command)
		if deliveryRuntimeErrorCode(err) != "REINDEX_DELIVERY_HEARTBEAT_INVALID" {
			t.Errorf("input case %d error=%v", index, err)
		}
		if port.heartbeatCalls != 0 {
			t.Errorf("input case %d called port", index)
		}
	}

	delivery, attempt, returnedFence := claimedRuntimeFacts()
	delivery.Version++
	returnedFence.DeliveryVersion = delivery.Version
	attempt.LeaseOwner = "other-worker"
	port := &fakeDeliveryRuntimePort{heartbeatResult: DeliveryMutationResult{Disposition: DeliveryMutationApplied, Delivery: delivery, Attempt: attempt, Fence: returnedFence}}
	runtime, _ := NewDeliveryRuntime(port)
	_, err := runtime.Heartbeat(context.Background(), DeliveryHeartbeatCommand{Fence: fence, LeaseDuration: time.Second})
	if deliveryRuntimeErrorCode(err) != "REINDEX_DELIVERY_HEARTBEAT_RESULT_INVALID" {
		t.Fatalf("contradictory result error=%v", err)
	}
}

func TestDeliveryRuntimeCheckpointValidatesFullReplayBinding(t *testing.T) {
	t.Parallel()

	delivery, attempt, oldFence := claimedRuntimeFacts()
	sourceVersionID := runtimeID(20)
	delivery.SourceVersionID = &sourceVersionID
	delivery.Version++
	delivery.UpdatedAt = delivery.UpdatedAt.Add(time.Second)
	newFence := oldFence
	newFence.DeliveryVersion = delivery.Version
	checkpoint := domain.DeliveryCheckpoint{Stage: domain.DeliveryCheckpointSourceCaptured, SourceVersionID: sourceVersionID}
	port := &fakeDeliveryRuntimePort{checkpointResult: DeliveryMutationResult{Disposition: DeliveryMutationApplied, Delivery: delivery, Attempt: attempt, Fence: newFence, Replayed: true}}
	runtime, _ := NewDeliveryRuntime(port)
	result, err := runtime.Checkpoint(context.Background(), DeliveryCheckpointCommand{Fence: oldFence, Checkpoint: checkpoint})
	if err != nil || !result.Replayed {
		t.Fatalf("Checkpoint()=%+v, %v", result, err)
	}
	if port.checkpoint.Checkpoint != checkpoint {
		t.Fatalf("port checkpoint=%+v", port.checkpoint)
	}
}

func TestDeliveryRuntimeCheckpointReturnsExplicitStaleOrMatchingCommitted(t *testing.T) {
	t.Parallel()

	_, _, fence := claimedRuntimeFacts()
	checkpoint := domain.DeliveryCheckpoint{Stage: domain.DeliveryCheckpointSourceCaptured, SourceVersionID: runtimeID(20)}
	port := &fakeDeliveryRuntimePort{checkpointResult: DeliveryMutationResult{Disposition: DeliveryMutationStale}}
	runtime, _ := NewDeliveryRuntime(port)
	result, err := runtime.Checkpoint(context.Background(), DeliveryCheckpointCommand{Fence: fence, Checkpoint: checkpoint})
	if err != nil || result.Disposition != DeliveryMutationStale {
		t.Fatalf("stale Checkpoint()=%+v, %v", result, err)
	}

	delivery, attempt := settledRetryFacts()
	delivery.SourceVersionID = runtimeIDPointer(20)
	port.checkpointResult = DeliveryMutationResult{Disposition: DeliveryMutationCommitted, Delivery: delivery, Attempt: attempt}
	result, err = runtime.Checkpoint(context.Background(), DeliveryCheckpointCommand{Fence: fence, Checkpoint: checkpoint})
	if err != nil || result.Disposition != DeliveryMutationCommitted {
		t.Fatalf("committed Checkpoint()=%+v, %v", result, err)
	}
}

func TestDeliveryRuntimeCheckpointRejectsInvalidInputAndMismatchedResult(t *testing.T) {
	t.Parallel()

	_, _, fence := claimedRuntimeFacts()
	port := &fakeDeliveryRuntimePort{}
	runtime, _ := NewDeliveryRuntime(port)
	_, err := runtime.Checkpoint(context.Background(), DeliveryCheckpointCommand{Fence: fence, Checkpoint: domain.DeliveryCheckpoint{}})
	if deliveryRuntimeErrorCode(err) != "REINDEX_DELIVERY_CHECKPOINT_INVALID" || port.checkpointCalls != 0 {
		t.Fatalf("invalid checkpoint error=%v calls=%d", err, port.checkpointCalls)
	}

	delivery, attempt, newFence := claimedRuntimeFacts()
	delivery.Version++
	newFence.DeliveryVersion = delivery.Version
	wrongSource := runtimeID(21)
	delivery.SourceVersionID = &wrongSource
	port.checkpointResult = DeliveryMutationResult{Disposition: DeliveryMutationApplied, Delivery: delivery, Attempt: attempt, Fence: newFence}
	wanted := domain.DeliveryCheckpoint{Stage: domain.DeliveryCheckpointSourceCaptured, SourceVersionID: runtimeID(20)}
	_, err = runtime.Checkpoint(context.Background(), DeliveryCheckpointCommand{Fence: fence, Checkpoint: wanted})
	if deliveryRuntimeErrorCode(err) != "REINDEX_DELIVERY_CHECKPOINT_RESULT_INVALID" {
		t.Fatalf("mismatched result error=%v", err)
	}
}

func TestDeliveryRuntimeFailPersistsOnlyNormalizedStableFactsAndRetryDelay(t *testing.T) {
	t.Parallel()

	_, _, fence := claimedRuntimeFacts()
	delivery, attempt := settledRetryFacts()
	port := &fakeDeliveryRuntimePort{failureResult: DeliveryMutationResult{Disposition: DeliveryMutationCommitted, Delivery: delivery, Attempt: attempt}}
	runtime, _ := NewDeliveryRuntime(port)
	result, err := runtime.Fail(context.Background(), DeliveryFailureCommand{
		Fence: fence,
		Failure: domain.DeliveryFailure{
			Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorDependencyUnavailable,
			Code: " REINDEX_DEPENDENCY_BUSY ", Summary: " dependency   unavailable ",
		},
		RetryDelay: time.Minute,
	})
	if err != nil || result.Disposition != DeliveryMutationCommitted {
		t.Fatalf("Fail()=%+v, %v", result, err)
	}
	if port.failure.Failure.Code != "REINDEX_DEPENDENCY_BUSY" || port.failure.Failure.Summary != "dependency unavailable" || port.failure.RetryDelay != time.Minute {
		t.Fatalf("port failure=%+v", port.failure)
	}
}

func TestDeliveryRuntimeFailReturnsExplicitStaleAndEveryCommittedOutcome(t *testing.T) {
	t.Parallel()

	_, _, fence := claimedRuntimeFacts()
	port := &fakeDeliveryRuntimePort{failureResult: DeliveryMutationResult{Disposition: DeliveryMutationStale}}
	runtime, _ := NewDeliveryRuntime(port)
	result, err := runtime.Fail(context.Background(), DeliveryFailureCommand{
		Fence: fence, Failure: domain.DeliveryFailure{Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorRetryableFailure, Code: "REINDEX_RETRY", Summary: "retry"}, RetryDelay: time.Second,
	})
	if err != nil || result.Disposition != DeliveryMutationStale {
		t.Fatalf("stale Fail()=%+v, %v", result, err)
	}

	tests := []struct {
		failure domain.DeliveryFailure
		delay   time.Duration
		status  domain.DeliveryStatus
		attempt domain.DeliveryAttemptStatus
	}{
		{failure: domain.DeliveryFailure{Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorDependencyUnavailable, Code: "REINDEX_RETRY", Summary: "retry"}, delay: time.Second, status: domain.DeliveryStatusRetryWait, attempt: domain.DeliveryAttemptRetryWait},
		{failure: domain.DeliveryFailure{Class: domain.DeliveryFailureNonRetryable, ErrorKind: foundation.ErrorInvalidInput, Code: "REINDEX_INVALID", Summary: "invalid"}, status: domain.DeliveryStatusFailed, attempt: domain.DeliveryAttemptFailed},
		{failure: domain.DeliveryFailure{Class: domain.DeliveryFailureManualRecovery, ErrorKind: foundation.ErrorConsistencyViolation, Code: "REINDEX_UNKNOWN", Summary: "unknown"}, status: domain.DeliveryStatusManualRecovery, attempt: domain.DeliveryAttemptManualRecovery},
	}
	for index, test := range tests {
		delivery, attempt := settledFailureFacts(test.failure)
		if delivery.Status != test.status || attempt.Status != test.attempt {
			t.Fatalf("fixture case %d status mismatch", index)
		}
		port.failureResult = DeliveryMutationResult{Disposition: DeliveryMutationCommitted, Delivery: delivery, Attempt: attempt, Replayed: true}
		result, err = runtime.Fail(context.Background(), DeliveryFailureCommand{Fence: fence, Failure: test.failure, RetryDelay: test.delay})
		if err != nil || result.Disposition != DeliveryMutationCommitted || !result.Replayed {
			t.Errorf("case %d Fail()=%+v, %v", index, result, err)
		}
	}
}

func TestDeliveryRuntimeFailRejectsInvalidInputAndContradictoryResult(t *testing.T) {
	t.Parallel()

	_, _, fence := claimedRuntimeFacts()
	tests := []DeliveryFailureCommand{
		{},
		{Fence: fence, Failure: domain.DeliveryFailure{Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorDependencyUnavailable, Code: "RETRY", Summary: "retry"}},
		{Fence: fence, Failure: domain.DeliveryFailure{Class: domain.DeliveryFailureNonRetryable, ErrorKind: foundation.ErrorInvalidInput, Code: "INVALID", Summary: "invalid"}, RetryDelay: time.Second},
		{Fence: fence, Failure: domain.DeliveryFailure{Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorInvalidInput, Code: "INVALID", Summary: "invalid"}, RetryDelay: time.Second},
		{Fence: fence, Failure: domain.DeliveryFailure{Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorDependencyUnavailable, Code: "RETRY", Summary: "retry"}, RetryDelay: MaxDeliveryRetryDelay + time.Second},
	}
	for index, command := range tests {
		port := &fakeDeliveryRuntimePort{}
		runtime, _ := NewDeliveryRuntime(port)
		_, err := runtime.Fail(context.Background(), command)
		if deliveryRuntimeErrorCode(err) != "REINDEX_DELIVERY_FAILURE_INVALID" {
			t.Errorf("input case %d error=%v", index, err)
		}
		if port.failureCalls != 0 {
			t.Errorf("input case %d called port", index)
		}
	}

	requested := domain.DeliveryFailure{Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorDependencyUnavailable, Code: "REINDEX_RETRY", Summary: "retry"}
	different := domain.DeliveryFailure{Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorDependencyUnavailable, Code: "REINDEX_OTHER", Summary: "other"}
	delivery, attempt := settledFailureFacts(different)
	port := &fakeDeliveryRuntimePort{failureResult: DeliveryMutationResult{Disposition: DeliveryMutationCommitted, Delivery: delivery, Attempt: attempt}}
	runtime, _ := NewDeliveryRuntime(port)
	_, err := runtime.Fail(context.Background(), DeliveryFailureCommand{Fence: fence, Failure: requested, RetryDelay: time.Second})
	if deliveryRuntimeErrorCode(err) != "REINDEX_DELIVERY_FAILURE_RESULT_INVALID" {
		t.Fatalf("contradictory result error=%v", err)
	}
}

func claimedRuntimeFacts() (domain.Delivery, domain.DeliveryAttempt, domain.DeliveryFence) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	attemptID := runtimeID(10)
	delivery := domain.Delivery{
		ID: runtimeID(1), ConsumerName: "retrieval-reindex-v1", OutboxEventID: runtimeID(2),
		WorkspaceID: runtimeID(3), WritebackExecutionID: runtimeID(4), Status: domain.DeliveryStatusProcessing,
		DispatchNo: 2, AttemptNo: 3, CurrentAttemptID: &attemptID, Version: 7, CreatedAt: now, UpdatedAt: now,
	}
	attempt := domain.DeliveryAttempt{
		ID: attemptID, DeliveryID: delivery.ID, AttemptNo: 3, DispatchNo: 2, RiverJobID: 91, RiverAttempt: 1,
		DeliveryKey: "reindex-job-91", LeaseOwner: "worker-a", LeaseUntil: now.Add(time.Minute),
		Status: domain.DeliveryAttemptProcessing, StartedAt: now, HeartbeatAt: now,
	}
	fence := domain.DeliveryFence{DeliveryID: delivery.ID, DispatchNo: 2, AttemptID: attempt.ID, AttemptNo: 3, Owner: "worker-a", DeliveryVersion: 7}
	return delivery, attempt, fence
}

func settledRetryFacts() (domain.Delivery, domain.DeliveryAttempt) {
	failure := domain.DeliveryFailure{Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorDependencyUnavailable, Code: "REINDEX_DEPENDENCY_BUSY", Summary: "dependency unavailable"}
	return settledFailureFacts(failure)
}

func settledFailureFacts(failure domain.DeliveryFailure) (domain.Delivery, domain.DeliveryAttempt) {
	delivery, attempt, _ := claimedRuntimeFacts()
	delivery.Version++
	delivery.UpdatedAt = delivery.UpdatedAt.Add(time.Second)
	retryAt := delivery.CreatedAt.Add(time.Minute)
	endedAt := attempt.HeartbeatAt.Add(time.Second)
	delivery.Failure, attempt.Failure, attempt.EndedAt = &failure, &failure, &endedAt
	switch failure.Class {
	case domain.DeliveryFailureRetryable:
		delivery.Status, delivery.NextAttemptAt, attempt.Status = domain.DeliveryStatusRetryWait, &retryAt, domain.DeliveryAttemptRetryWait
	case domain.DeliveryFailureNonRetryable:
		delivery.Status, attempt.Status, delivery.CompletedAt = domain.DeliveryStatusFailed, domain.DeliveryAttemptFailed, &endedAt
	case domain.DeliveryFailureManualRecovery:
		delivery.Status, attempt.Status, delivery.ManualRecoveryRequired = domain.DeliveryStatusManualRecovery, domain.DeliveryAttemptManualRecovery, true
	}
	return delivery, attempt
}

func validDeliveryClaimCommand() DeliveryClaimCommand {
	return DeliveryClaimCommand{DeliveryID: runtimeID(1), DispatchNo: 2, RiverJobID: 91, RiverAttempt: 1, DeliveryKey: "reindex-job-91", LeaseOwner: "worker-a", LeaseDuration: 30 * time.Second}
}

func deliveryRuntimeErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func runtimeID(value int) foundation.ID {
	return foundation.ID("e0000000-0000-4000-8000-" + runtimeDigits(value))
}

func runtimeIDPointer(value int) *foundation.ID {
	id := runtimeID(value)
	return &id
}

func runtimeDigits(value int) string {
	text := []byte("000000000000")
	for index := len(text) - 1; value > 0; index-- {
		text[index] = byte('0' + value%10)
		value /= 10
	}
	return string(text)
}

type fakeDeliveryRuntimePort struct {
	claim            DeliveryClaimCommand
	claimResult      DeliveryClaimResult
	claimCalls       int
	heartbeat        DeliveryHeartbeatCommand
	heartbeatResult  DeliveryMutationResult
	heartbeatCalls   int
	checkpoint       DeliveryCheckpointCommand
	checkpointResult DeliveryMutationResult
	checkpointCalls  int
	failure          DeliveryFailureCommand
	failureResult    DeliveryMutationResult
	failureCalls     int
}

func (f *fakeDeliveryRuntimePort) Claim(_ context.Context, command DeliveryClaimCommand) (DeliveryClaimResult, error) {
	f.claim, f.claimCalls = command, f.claimCalls+1
	return f.claimResult, nil
}

func (f *fakeDeliveryRuntimePort) Heartbeat(_ context.Context, command DeliveryHeartbeatCommand) (DeliveryMutationResult, error) {
	f.heartbeat, f.heartbeatCalls = command, f.heartbeatCalls+1
	return f.heartbeatResult, nil
}

func (f *fakeDeliveryRuntimePort) Checkpoint(_ context.Context, command DeliveryCheckpointCommand) (DeliveryMutationResult, error) {
	f.checkpoint, f.checkpointCalls = command, f.checkpointCalls+1
	return f.checkpointResult, nil
}

func (f *fakeDeliveryRuntimePort) Fail(_ context.Context, command DeliveryFailureCommand) (DeliveryMutationResult, error) {
	f.failure, f.failureCalls = command, f.failureCalls+1
	return f.failureResult, nil
}
