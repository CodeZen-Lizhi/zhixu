package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestCompletionServiceOwnsActivationIdentityAndPreservesFence(t *testing.T) {
	store := &completionStoreFake{}
	service, err := NewCompletionService(store, &completionIDFake{id: "c2000000-0000-4000-8000-000000000004"}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request := CompleteReindexRequest{
		WorkspaceID: "c2000000-0000-4000-8000-000000000001",
		Fence: domain.DeliveryFence{
			DeliveryID: "c2000000-0000-4000-8000-000000000002", DispatchNo: 1,
			AttemptID: "c2000000-0000-4000-8000-000000000003", AttemptNo: 1,
			Owner: "worker", DeliveryVersion: 4,
		},
		ActivationIdempotencyKey: "complete:event",
		ActivationReasonCode:     "REINDEX_REGRESSION_PASSED",
	}
	if _, err := service.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if store.command.ActivationID != "c2000000-0000-4000-8000-000000000004" || store.command.Fence != request.Fence ||
		store.command.WorkspaceID != request.WorkspaceID || store.command.ActivationIdempotencyKey != request.ActivationIdempotencyKey {
		t.Fatalf("command=%#v", store.command)
	}
}

func TestCompletionServiceMapsOnlyTypedPendingPrerequisiteToDeliveryFailure(t *testing.T) {
	pending := NewCompletionPrerequisitePending(errors.New("workflow still running"))
	store := &completionStoreFake{err: pending}
	service, err := NewCompletionService(store, &completionIDFake{id: "c3000000-0000-4000-8000-000000000004"}, 7*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Complete(context.Background(), CompleteReindexRequest{
		WorkspaceID: "c3000000-0000-4000-8000-000000000001",
		Fence: domain.DeliveryFence{
			DeliveryID: "c3000000-0000-4000-8000-000000000002", DispatchNo: 1,
			AttemptID: "c3000000-0000-4000-8000-000000000003", AttemptNo: 1,
			Owner: "worker", DeliveryVersion: 4,
		},
		ActivationIdempotencyKey: "complete:event", ActivationReasonCode: "REINDEX_REGRESSION_PASSED",
	})
	var failure *DeliveryFailureError
	if !errors.As(err, &failure) || failure.Failure.Code != "REINDEX_COMPLETION_PREREQUISITE_PENDING" ||
		failure.Failure.Class != domain.DeliveryFailureRetryable || failure.RetryDelay != 7*time.Second || !errors.Is(err, pending) {
		t.Fatalf("failure=%#v err=%v", failure, err)
	}
}

func TestCompletionServicePassesThroughUnknownOrMisclassifiedStoreErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "unknown", err: errors.New("commit response unknown")},
		{name: "retryable commit", err: foundation.NewError(foundation.ErrorRetryableFailure, "REINDEX_COMPLETION_COMMIT_FAILED", true, errors.New("serialization failure"))},
		{name: "dependency commit", err: foundation.NewError(foundation.ErrorDependencyUnavailable, "REINDEX_COMPLETION_COMMIT_FAILED", true, errors.New("commit response unknown"))},
		{name: "stale fence", err: foundation.NewError(foundation.ErrorVersionConflict, "REINDEX_COMPLETION_FENCE_STALE", false, errors.New("old fence"))},
		{name: "unlisted consistency", err: foundation.NewError(foundation.ErrorConsistencyViolation, "REINDEX_COMPLETION_CLOCK_QUERY_FAILED", false, errors.New("clock invariant"))},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &completionStoreFake{err: test.err}
			service, err := NewCompletionService(store, &completionIDFake{id: foundation.ID("c4000000-0000-4000-8000-00000000000" + string(rune('1'+index)))}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Complete(context.Background(), validCompletionRequest("c4000000"))
			var failure *DeliveryFailureError
			if !errors.Is(err, test.err) || errors.As(err, &failure) {
				t.Fatalf("failure=%#v err=%v", failure, err)
			}
		})
	}
}

func TestCompletionServiceMapsDeterministicCompletionDamageToManualRecovery(t *testing.T) {
	tests := []struct {
		code         string
		kind         foundation.ErrorKind
		expectedKind foundation.ErrorKind
	}{
		{code: "REINDEX_COMPLETION_ATTEMPT_MISSING", kind: foundation.ErrorConsistencyViolation, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "REINDEX_COMPLETION_BINDING_CONFLICT", kind: foundation.ErrorConsistencyViolation, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "REINDEX_COMPLETION_COMMIT_FAILED", kind: foundation.ErrorConsistencyViolation, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "REINDEX_COMPLETION_DELIVERY_NOT_FOUND", kind: foundation.ErrorNotFound, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "REINDEX_COMPLETION_GATE_FAILED", kind: foundation.ErrorConsistencyViolation, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "REINDEX_COMPLETION_INDEX_MISSING", kind: foundation.ErrorConsistencyViolation, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "REINDEX_COMPLETION_REPLAY_CONFLICT", kind: foundation.ErrorVersionConflict, expectedKind: foundation.ErrorVersionConflict},
		{code: "REINDEX_COMPLETION_ATTEMPT_UPDATE_FAILED", kind: foundation.ErrorConsistencyViolation, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "REINDEX_COMPLETION_DELIVERY_UPDATE_FAILED", kind: foundation.ErrorConsistencyViolation, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "REINDEX_COMPLETION_EXECUTION_UPDATE_FAILED", kind: foundation.ErrorConsistencyViolation, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "REINDEX_COMPLETION_PROPOSAL_UPDATE_FAILED", kind: foundation.ErrorConsistencyViolation, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "REINDEX_COMPLETION_FACT_QUERY_FAILED", kind: foundation.ErrorConsistencyViolation, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "REINDEX_COMPLETION_EXECUTION_STALE", kind: foundation.ErrorVersionConflict, expectedKind: foundation.ErrorVersionConflict},
		{code: "REINDEX_COMPLETION_PROPOSAL_STALE", kind: foundation.ErrorVersionConflict, expectedKind: foundation.ErrorVersionConflict},
		{code: "RETRIEVAL_ACTIVATION_IDEMPOTENCY_CONFLICT", kind: foundation.ErrorVersionConflict, expectedKind: foundation.ErrorVersionConflict},
		{code: "RETRIEVAL_ACTIVATION_INVALID", kind: foundation.ErrorInvalidInput, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "RETRIEVAL_INDEX_NOT_FOUND", kind: foundation.ErrorNotFound, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "RETRIEVAL_PREVIOUS_RETIRE_FAILED", kind: foundation.ErrorNotFound, expectedKind: foundation.ErrorConsistencyViolation},
		{code: "RETRIEVAL_TARGET_ACTIVATE_FAILED", kind: foundation.ErrorConsistencyViolation, expectedKind: foundation.ErrorConsistencyViolation},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			storeErr := foundation.NewError(test.kind, test.code, false, errors.New("deterministic completion damage"))
			service, err := NewCompletionService(&completionStoreFake{err: storeErr},
				&completionIDFake{id: "c5000000-0000-4000-8000-000000000004"}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Complete(context.Background(), validCompletionRequest("c5000000"))
			var failure *DeliveryFailureError
			if !errors.As(err, &failure) || failure.Failure.Class != domain.DeliveryFailureManualRecovery ||
				failure.Failure.ErrorKind != test.expectedKind || failure.Failure.Code != test.code ||
				failure.RetryDelay != 0 || !errors.Is(err, storeErr) {
				t.Fatalf("failure=%#v err=%v", failure, err)
			}
		})
	}
}

func TestCompletionServiceRejectsIncompleteDependenciesAndInvalidRequest(t *testing.T) {
	if _, err := NewCompletionService(nil, &completionIDFake{}, time.Second); err == nil {
		t.Fatal("missing store must fail")
	}
	var typedNilStore *completionStoreFake
	if _, err := NewCompletionService(typedNilStore, &completionIDFake{}, time.Second); err == nil {
		t.Fatal("typed nil store must fail")
	}
	if _, err := NewCompletionService(&completionStoreFake{}, &completionIDFake{}, 0); err == nil {
		t.Fatal("invalid retry delay must fail")
	}
	service, err := NewCompletionService(&completionStoreFake{}, &completionIDFake{id: "bad"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Complete(context.Background(), CompleteReindexRequest{}); err == nil {
		t.Fatal("invalid request must fail before store")
	}
}

func validCompletionRequest(prefix string) CompleteReindexRequest {
	return CompleteReindexRequest{
		WorkspaceID: foundation.ID(prefix + "-0000-4000-8000-000000000001"),
		Fence: domain.DeliveryFence{
			DeliveryID: foundation.ID(prefix + "-0000-4000-8000-000000000002"), DispatchNo: 1,
			AttemptID: foundation.ID(prefix + "-0000-4000-8000-000000000003"), AttemptNo: 1,
			Owner: "worker", DeliveryVersion: 4,
		},
		ActivationIdempotencyKey: "complete:event", ActivationReasonCode: "REINDEX_REGRESSION_PASSED",
	}
}

type completionStoreFake struct {
	command domain.CompleteReindexCommand
	err     error
}

func (store *completionStoreFake) CompleteReindexTx(_ context.Context, command domain.CompleteReindexCommand) (domain.CompleteReindexResult, error) {
	store.command = command
	return domain.CompleteReindexResult{}, store.err
}

type completionIDFake struct {
	id  foundation.ID
	err error
}

func (ids *completionIDFake) New() (foundation.ID, error) {
	if ids.err != nil {
		return "", ids.err
	}
	return ids.id, nil
}
