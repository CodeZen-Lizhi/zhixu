package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	localmodelruntime "github.com/CodeZen-Lizhi/zhixu/internal/localmodelruntime"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

func TestDurableTestPreparationSurvivesCallerWaitCancellation(t *testing.T) {
	preparations := &testPreparationRecorder{}
	lease := &testHoldLeaseAdapter{preparation: preparations}

	lease.releaseAfterWaitFailure()

	if len(preparations.completions) != 0 {
		t.Fatalf("completion calls = %d, want 0", len(preparations.completions))
	}
}

type testPreparationRecorder struct {
	completions []localmodelruntime.TestProbeCompletionCommand
}

func (*testPreparationRecorder) SeedTestPreparation(context.Context, localmodelruntime.TestPreparationCommand) (localmodelruntime.TestPreparation, error) {
	return localmodelruntime.TestPreparation{}, nil
}

func (*testPreparationRecorder) ReadTestOperation(context.Context, foundation.ID) (localmodelruntime.OperationRecord, error) {
	return localmodelruntime.OperationRecord{}, nil
}

func (*testPreparationRecorder) ClaimTestProbe(context.Context, localmodelruntime.TestProbeClaimCommand) (localmodelruntime.OperationRecord, bool, error) {
	return localmodelruntime.OperationRecord{}, false, nil
}

func (recorder *testPreparationRecorder) CompleteTestProbe(_ context.Context, command localmodelruntime.TestProbeCompletionCommand) (localmodelruntime.OperationRecord, error) {
	recorder.completions = append(recorder.completions, command)
	return localmodelruntime.OperationRecord{}, nil
}

func TestDurableTestProbeUsesRandomOwnerAndFencedCompletion(t *testing.T) {
	store := &testProbeStore{}
	probeOwner := foundation.ID("11111111-1111-4111-8111-111111111111")
	adapter := &TestLifecycleAdapter{store: store, ids: &testIDSequence{ids: []foundation.ID{probeOwner}}}
	settings := localChatTestSettings()

	lease, err := adapter.BeginTest(context.Background(), modelsettingsapplication.TestLifecycleCommand{
		ExpectedRevision: 3, Settings: settings, Target: modelsettingsapplication.ConnectionTargetChat,
		IdempotencyKey: "same-local-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.claims) != 1 {
		t.Fatalf("probe claims = %d, want 1", len(store.claims))
	}
	claim := store.claims[0]
	if claim.OwnerID != probeOwner || claim.OwnerID == store.seed.OwnerID {
		t.Fatalf("probe owner=%s hold owner=%s", claim.OwnerID, store.seed.OwnerID)
	}
	completion, ok := lease.(modelsettingsapplication.TestLifecycleCompletion)
	if !ok {
		t.Fatal("durable test lease does not expose completion")
	}
	if err := completion.Complete(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	if len(store.completions) != 1 {
		t.Fatalf("probe completions = %d, want 1", len(store.completions))
	}
	completed := store.completions[0]
	if completed.OwnerID != claim.OwnerID || completed.OwnerEpoch != claim.OwnerEpoch || completed.ExpectedVersion != store.claimed.Version || completed.Abandon {
		t.Fatalf("completion fence = %+v, claim = %+v", completed, store.claimed)
	}
}

func TestDurableTestProbeCancellationAbandonsWithoutTerminalResult(t *testing.T) {
	store := &testProbeStore{}
	adapter := &TestLifecycleAdapter{store: store, ids: &testIDSequence{ids: []foundation.ID{"22222222-2222-4222-8222-222222222222"}}}
	lease, err := adapter.BeginTest(context.Background(), modelsettingsapplication.TestLifecycleCommand{
		ExpectedRevision: 3, Settings: localChatTestSettings(), Target: modelsettingsapplication.ConnectionTargetChat,
		IdempotencyKey: "cancel-local-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	completion := lease.(modelsettingsapplication.TestLifecycleCompletion)
	if err := completion.Abandon(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.completions) != 1 || !store.completions[0].Abandon || store.completions[0].ErrorCode != "" || store.completions[0].Retryable {
		t.Fatalf("abandon completion = %+v", store.completions)
	}
}

func TestDurableTestProbeFreshJoinerWaitsForOwnerResult(t *testing.T) {
	store := &joiningTestProbeStore{}
	adapter := &TestLifecycleAdapter{store: store, ids: &testIDSequence{ids: []foundation.ID{"33333333-3333-4333-8333-333333333333"}}}
	lease, err := adapter.BeginTest(context.Background(), modelsettingsapplication.TestLifecycleCommand{
		ExpectedRevision: 3, Settings: localChatTestSettings(), Target: modelsettingsapplication.ConnectionTargetChat,
		IdempotencyKey: "join-local-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	replay, ok := lease.(modelsettingsapplication.TestLifecycleReplay)
	if !ok || !replay.ReplayedSuccess() {
		t.Fatal("fresh probe joiner did not replay the owner result")
	}
	if store.claimCalls != 2 {
		t.Fatalf("probe claim calls = %d, want 2", store.claimCalls)
	}
}

func TestDurableTestProbeClaimResponseLossRecoversSameOwner(t *testing.T) {
	store := &responseLossTestProbeStore{}
	probeOwner := foundation.ID("55555555-5555-4555-8555-555555555555")
	adapter := &TestLifecycleAdapter{store: store, ids: &testIDSequence{ids: []foundation.ID{probeOwner}}}
	lease, err := adapter.BeginTest(context.Background(), modelsettingsapplication.TestLifecycleCommand{
		ExpectedRevision: 3, Settings: localChatTestSettings(), Target: modelsettingsapplication.ConnectionTargetChat,
		IdempotencyKey: "response-loss-local-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.claimCalls != 2 {
		t.Fatalf("probe claim calls = %d, want 2", store.claimCalls)
	}
	if err := lease.(modelsettingsapplication.TestLifecycleCompletion).Complete(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	if len(store.completions) != 1 || store.completions[0].OwnerID != probeOwner {
		t.Fatalf("probe completion = %+v", store.completions)
	}
}

type testProbeStore struct {
	localmodelruntime.LifecycleStore
	seed        localmodelruntime.TestPreparationCommand
	operation   localmodelruntime.OperationRecord
	claimed     localmodelruntime.OperationRecord
	claims      []localmodelruntime.TestProbeClaimCommand
	completions []localmodelruntime.TestProbeCompletionCommand
}

type joiningTestProbeStore struct {
	testProbeStore
	claimCalls int
}

type responseLossTestProbeStore struct {
	testProbeStore
	claimCalls int
}

func (store *responseLossTestProbeStore) ClaimTestProbe(_ context.Context, command localmodelruntime.TestProbeClaimCommand) (localmodelruntime.OperationRecord, bool, error) {
	store.claimCalls++
	if store.claimCalls == 1 {
		store.operation.Phase = localmodelruntime.OperationPhaseProbing
		store.operation.Version++
		owner := command.OwnerID
		store.operation.ClaimOwnerID = &owner
		store.operation.ClaimOwnerEpoch = command.OwnerEpoch
		return localmodelruntime.OperationRecord{}, false, errors.New("probe claim response lost")
	}
	store.claimed = store.operation
	return store.operation, true, nil
}

func (store *joiningTestProbeStore) ClaimTestProbe(_ context.Context, command localmodelruntime.TestProbeClaimCommand) (localmodelruntime.OperationRecord, bool, error) {
	store.claimCalls++
	if store.claimCalls == 1 {
		store.operation.Phase = localmodelruntime.OperationPhaseProbing
		store.operation.Version++
		owner := foundation.ID("44444444-4444-4444-8444-444444444444")
		store.operation.ClaimOwnerID = &owner
		store.operation.ClaimOwnerEpoch = 1
		return store.operation, false, nil
	}
	store.operation.Phase = localmodelruntime.OperationPhaseSucceeded
	store.operation.Version++
	store.operation.TerminalAt = new(time.Time)
	store.operation.ClaimOwnerID = nil
	return store.operation, false, nil
}

func (store *testProbeStore) SeedTestPreparation(_ context.Context, command localmodelruntime.TestPreparationCommand) (localmodelruntime.TestPreparation, error) {
	store.seed = command
	store.operation = localmodelruntime.OperationRecord{
		OperationID: command.OperationID, Kind: localmodelruntime.OperationKindTest,
		IdempotencyKey: command.IdempotencyKey, RequestHash: command.RequestHash,
		Requirement: command.Requirement, Phase: localmodelruntime.OperationPhaseReady, Version: 7,
	}
	return localmodelruntime.TestPreparation{
		Operation: store.operation,
		Hold: localmodelruntime.HoldRecord{
			HoldID: command.HoldID, Kind: localmodelruntime.HoldKindTest,
			OwnerID: command.OwnerID, OwnerEpoch: command.OwnerEpoch,
			OperationID: &command.OperationID, Requirement: command.Requirement, Version: 1,
		},
	}, nil
}

func (store *testProbeStore) ReadDemand(context.Context) (localmodelruntime.DemandSnapshot, error) {
	return localmodelruntime.DemandSnapshot{
		Requirement: store.operation.Requirement,
		Operations:  []localmodelruntime.OperationRecord{store.operation},
		Runtime: localmodelruntime.RuntimeRecord{
			Phase: localmodelruntime.RuntimePhaseReady, ReadyHash: store.operation.Requirement.Hash,
		},
	}, nil
}

func (store *testProbeStore) ReadTestOperation(context.Context, foundation.ID) (localmodelruntime.OperationRecord, error) {
	return store.operation, nil
}

func (store *testProbeStore) ClaimTestProbe(_ context.Context, command localmodelruntime.TestProbeClaimCommand) (localmodelruntime.OperationRecord, bool, error) {
	store.claims = append(store.claims, command)
	store.operation.Phase = localmodelruntime.OperationPhaseProbing
	store.operation.Version++
	owner := command.OwnerID
	store.operation.ClaimOwnerID = &owner
	store.operation.ClaimOwnerEpoch = command.OwnerEpoch
	store.claimed = store.operation
	return store.operation, true, nil
}

func (store *testProbeStore) CompleteTestProbe(_ context.Context, command localmodelruntime.TestProbeCompletionCommand) (localmodelruntime.OperationRecord, error) {
	store.completions = append(store.completions, command)
	return store.operation, nil
}

type testIDSequence struct {
	ids []foundation.ID
}

func (sequence *testIDSequence) New() (foundation.ID, error) {
	if len(sequence.ids) == 0 {
		return "", errors.New("test id sequence exhausted")
	}
	id := sequence.ids[0]
	sequence.ids = sequence.ids[1:]
	return id, nil
}

func localChatTestSettings() modelsettingsdomain.Settings {
	settings := modelsettingsdomain.CanonicalDisabledSettings()
	settings.Chat.Provider = modelsettingsdomain.ChatProviderOllama
	settings.Chat.BaseURL = modelsettingsdomain.ManagedOllamaBaseURL
	settings.Chat.Model = "qwen2.5:3b"
	settings.Chat.ModelVersion = "qwen2.5:3b"
	settings.Chat.Timeout = time.Second
	return settings
}
