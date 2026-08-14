package localmodelruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestReconcilerDatabaseOutageDoesNotStopExistingChild(t *testing.T) {
	store := &reconcilerStore{readErr: errors.New("database offline")}
	child := &reconcilerChild{snapshot: Snapshot{State: StateRunning, Generation: 1, Ready: true}}
	reconciler := newFixtureReconciler(t, store, child, &reconcilerOllama{})
	if err := reconciler.ReconcileOnce(context.Background()); err == nil {
		t.Fatal("ReconcileOnce unexpectedly succeeded during database outage")
	}
	if child.stopCalls != 0 || !child.snapshot.Ready {
		t.Fatalf("child changed during outage: %+v stopCalls=%d", child.snapshot, child.stopCalls)
	}
}

func TestReconcilerEmptyAuthoritativeDemandStopsChild(t *testing.T) {
	store := &reconcilerStore{snapshot: DemandSnapshot{Runtime: RuntimeRecord{Mode: RuntimeModeManaged, Phase: RuntimePhaseReady, ChildEpoch: 7, Version: 1}, RolloutPhase: "idle", RequirementVersion: 0, MayStop: true}}
	child := &reconcilerChild{snapshot: Snapshot{State: StateRunning, Generation: 7, Ready: true}}
	reconciler := newFixtureReconciler(t, store, child, &reconcilerOllama{})
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce() error = %v", err)
	}
	if child.stopCalls != 1 || child.snapshot.Ready {
		t.Fatalf("child stop/readiness = %d/%t", child.stopCalls, child.snapshot.Ready)
	}
	if len(store.phases) != 2 || store.phases[0] != RuntimePhaseStopping || store.phases[1] != RuntimePhaseStopped {
		t.Fatalf("phase transitions = %v", store.phases)
	}
	if len(store.commands) != 2 || store.commands[0].ChildEpoch != 7 || store.commands[1].ChildEpoch != 7 {
		t.Fatalf("child epochs after stop = %+v", store.commands)
	}
}

func TestReconcilerDemandStartsPullsAndOpensReadyGate(t *testing.T) {
	requirement, err := NewRequirement([]ModelRef{"smollm2:135m"})
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	store := &reconcilerStore{snapshot: DemandSnapshot{
		Requirement:        requirement,
		Runtime:            RuntimeRecord{Mode: RuntimeModeManaged, Phase: RuntimePhaseStopped, ChildEpoch: 7, Version: 1},
		RequirementVersion: 0,
		Operations: []OperationRecord{{
			OperationID: operationID, Kind: OperationKindTest, IdempotencyKey: "download",
			RequestHash: strings.Repeat("a", 64), Requirement: requirement, Phase: OperationPhaseQueued,
			Version: 1, CreatedAt: time.Now(),
		}},
	}}
	child := &reconcilerChild{snapshot: Snapshot{State: StateStopped}}
	ollama := &reconcilerOllama{missing: true}
	reconciler := newFixtureReconciler(t, store, child, ollama)
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce() error = %v", err)
	}
	if child.ensureCalls != 1 || !child.snapshot.Ready || ollama.pullCalls != 1 {
		t.Fatalf("ensure/ready/pull = %d/%t/%d", child.ensureCalls, child.snapshot.Ready, ollama.pullCalls)
	}
	if store.published == nil || len(store.phases) == 0 || store.phases[len(store.phases)-1] != RuntimePhaseReady {
		t.Fatalf("published=%+v phases=%v", store.published, store.phases)
	}
	if store.snapshot.Runtime.ChildEpoch != 8 || len(child.generationFloors) != 1 || child.generationFloors[0] != 7 {
		t.Fatalf("durable child epoch/floors = %d/%v, want 8/[7]", store.snapshot.Runtime.ChildEpoch, child.generationFloors)
	}
}

func TestReconcilerSeedsRecoveryForMissingActiveModels(t *testing.T) {
	requirement, err := NewRequirement([]ModelRef{"smollm2:135m"})
	if err != nil {
		t.Fatal(err)
	}
	store := &reconcilerStore{snapshot: DemandSnapshot{
		Requirement:          requirement,
		SettingsStateVersion: 3,
		Runtime:              RuntimeRecord{Mode: RuntimeModeManaged, Phase: RuntimePhaseStopped, Version: 1},
		Sources:              []DemandSource{{Kind: "active", Revision: 7, Requirement: requirement}},
	}}
	child := &reconcilerChild{snapshot: Snapshot{State: StateStopped}}
	ollama := &reconcilerOllama{missing: true}
	if err := newFixtureReconciler(t, store, child, ollama).ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce() error = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.activeRecoverySeeds != 1 || ollama.pullCalls != 1 || !child.snapshot.Ready {
		t.Fatalf("recovery seeds/pulls/ready = %d/%d/%t", store.activeRecoverySeeds, ollama.pullCalls, child.snapshot.Ready)
	}
	if len(store.snapshot.Operations) != 1 || store.snapshot.Operations[0].Kind != OperationKindActiveRecovery ||
		store.snapshot.Operations[0].Phase != OperationPhaseSucceeded || store.snapshot.Operations[0].TerminalAt == nil {
		t.Fatalf("active recovery operation = %+v", store.snapshot.Operations)
	}
}

func TestReconcilerCancelsActiveRecoveryPullWhenActiveSourceChanges(t *testing.T) {
	requirement, err := NewRequirement([]ModelRef{"smollm2:135m"})
	if err != nil {
		t.Fatal(err)
	}
	deadlines := make(chan time.Duration, 2)
	store := &reconcilerStore{activeRecoveryCheckErrors: []error{errors.New("temporary database error")}, activeRecoveryCheckDeadlines: deadlines, snapshot: DemandSnapshot{
		Requirement:          requirement,
		SettingsStateVersion: 3,
		Runtime:              RuntimeRecord{Mode: RuntimeModeManaged, Phase: RuntimePhaseStopped, Version: 1},
		Sources:              []DemandSource{{Kind: "active", Revision: 7, Requirement: requirement}},
	}}
	ollama := &blockingRecoveryOllama{started: make(chan struct{})}
	reconciler := newFixtureReconciler(t, store, &reconcilerChild{snapshot: Snapshot{State: StateStopped}}, ollama)
	reconciler.options.RecoveryCheckEvery = 20 * time.Millisecond
	reconciler.options.RecoveryCheckLimit = 10 * time.Millisecond
	reported := make(chan error, 1)
	reconciler.options.OnError = func(err error) { reported <- err }
	done := make(chan error, 1)
	go func() { done <- reconciler.ReconcileOnce(context.Background()) }()
	select {
	case <-ollama.started:
	case <-time.After(time.Second):
		t.Fatal("active recovery pull did not start")
	}
	select {
	case remaining := <-deadlines:
		if remaining <= 0 || remaining > reconciler.options.RecoveryCheckLimit {
			t.Fatalf("active recovery check deadline = %v, limit %v", remaining, reconciler.options.RecoveryCheckLimit)
		}
	case <-time.After(time.Second):
		t.Fatal("active recovery currency check did not run")
	}
	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "temporary database error") {
			t.Fatalf("reported error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("temporary active recovery check error was not reported")
	}
	select {
	case err := <-done:
		t.Fatalf("temporary active recovery check error stopped pull: %v", err)
	default:
	}
	store.mu.Lock()
	store.snapshot.Sources = nil
	store.mu.Unlock()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ReconcileOnce unexpectedly succeeded after active source changed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active recovery pull was not cancelled")
	}
	if ollama.PullCalls() != 1 {
		t.Fatalf("active recovery pull calls = %d, want 1", ollama.PullCalls())
	}
}

func TestReconcilerPersistsPullBudgetAndFailsAfterThirdAttempt(t *testing.T) {
	requirement, err := NewRequirement([]ModelRef{"smollm2:135m"})
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	store := &reconcilerStore{snapshot: DemandSnapshot{
		Requirement: requirement,
		Runtime:     RuntimeRecord{Mode: RuntimeModeManaged, Phase: RuntimePhaseStopped, Version: 1},
		Operations: []OperationRecord{{
			OperationID: operationID, Kind: OperationKindTest, IdempotencyKey: "budget",
			RequestHash: strings.Repeat("b", 64), Requirement: requirement, Phase: OperationPhaseQueued,
			Version: 1, CreatedAt: time.Now(),
		}},
	}}
	child := &reconcilerChild{snapshot: Snapshot{State: StateStopped}}
	reconciler := newFixtureReconciler(t, store, child, &candidatePullFailureOllama{})
	if err := reconciler.ReconcileOnce(context.Background()); err == nil {
		t.Fatal("ReconcileOnce unexpectedly succeeded")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operation := store.snapshot.Operations[0]
	if operation.AttemptNo != 3 || operation.Phase != OperationPhaseFailed || operation.ErrorCode != "LOCAL_MODEL_RUNTIME_PULL_ATTEMPTS_EXHAUSTED" || operation.Retryable {
		t.Fatalf("operation after exhausted budget = %+v", operation)
	}
	if store.snapshot.Runtime.ErrorCode != "LOCAL_MODEL_RUNTIME_PULL_ATTEMPTS_EXHAUSTED" || store.snapshot.Runtime.Retryable {
		t.Fatalf("runtime after exhausted budget = %+v", store.snapshot.Runtime)
	}
}

func TestReconcilerExpiresProbingOperationBeforeReadingDemand(t *testing.T) {
	requirement, err := NewRequirement([]ModelRef{"smollm2:135m"})
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	store := &reconcilerStore{snapshot: DemandSnapshot{
		Requirement: requirement,
		Runtime:     RuntimeRecord{Mode: RuntimeModeManaged, Phase: RuntimePhaseReady, Requirement: requirement, ReadyHash: requirement.Hash, Version: 1},
		Operations: []OperationRecord{{
			OperationID: operationID, Kind: OperationKindTest, IdempotencyKey: "probe-timeout",
			RequestHash: strings.Repeat("c", 64), Requirement: requirement, Phase: OperationPhaseProbing,
			Version: 1, CreatedAt: time.Now().Add(-6*time.Hour - time.Second),
		}},
	}}
	if err := newFixtureReconciler(t, store, &reconcilerChild{snapshot: Snapshot{State: StateRunning, Generation: 1, Ready: true}}, &reconcilerOllama{}).ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce() error = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operation := store.snapshot.Operations[0]
	if operation.Phase != OperationPhaseFailed || operation.ErrorCode != "LOCAL_MODEL_RUNTIME_PREPARATION_TIMEOUT" || operation.TerminalAt == nil {
		t.Fatalf("expired probing operation = %+v", operation)
	}
}

func TestReconcilerProcessesQueuedOperationsWhileRuntimeIsAlreadyReady(t *testing.T) {
	requirement, err := NewRequirement([]ModelRef{"smollm2:135m"})
	if err != nil {
		t.Fatal(err)
	}
	ids := foundation.NewUUIDGenerator(nil)
	firstID, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	store := &reconcilerStore{snapshot: DemandSnapshot{
		Requirement: requirement,
		Runtime: RuntimeRecord{
			Mode: RuntimeModeManaged, Phase: RuntimePhaseReady, Requirement: requirement,
			ReadyHash: requirement.Hash, ChildEpoch: 1, Version: 1,
		},
		Operations: []OperationRecord{
			{OperationID: firstID, Kind: OperationKindTest, IdempotencyKey: "first", RequestHash: "first", Requirement: requirement, Phase: OperationPhaseQueued, Version: 1, CreatedAt: now},
			{OperationID: secondID, Kind: OperationKindTest, IdempotencyKey: "second", RequestHash: "second", Requirement: requirement, Phase: OperationPhaseQueued, Version: 1, CreatedAt: now.Add(time.Millisecond)},
		},
	}}
	child := &reconcilerChild{snapshot: Snapshot{State: StateRunning, Generation: 1, Ready: true}}
	reconciler := newFixtureReconciler(t, store, child, &reconcilerOllama{})

	for i := 0; i < 2; i++ {
		if err := reconciler.ReconcileOnce(context.Background()); err != nil {
			t.Fatalf("ReconcileOnce() #%d error = %v", i+1, err)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, operation := range store.snapshot.Operations {
		if operation.Phase != OperationPhaseReady {
			t.Fatalf("operation %s phase = %s, want %s", operation.OperationID, operation.Phase, OperationPhaseReady)
		}
	}
	if store.completedOperations != 2 {
		t.Fatalf("completed operations = %d, want 2", store.completedOperations)
	}
}

func TestReconcilerChargesOnlyTheClaimedOperationsModels(t *testing.T) {
	firstRequirement, err := NewRequirement([]ModelRef{"first:latest"})
	if err != nil {
		t.Fatal(err)
	}
	secondRequirement, err := NewRequirement([]ModelRef{"second:latest"})
	if err != nil {
		t.Fatal(err)
	}
	union, err := NewRequirement([]ModelRef{"first:latest", "second:latest"})
	if err != nil {
		t.Fatal(err)
	}
	ids := foundation.NewUUIDGenerator(nil)
	firstID, _ := ids.New()
	secondID, _ := ids.New()
	now := time.Now()
	store := &reconcilerStore{snapshot: DemandSnapshot{
		Requirement: union,
		Runtime:     RuntimeRecord{Mode: RuntimeModeManaged, Phase: RuntimePhaseStopped, Version: 1},
		Operations: []OperationRecord{
			{OperationID: firstID, Kind: OperationKindTest, IdempotencyKey: "first-model", RequestHash: strings.Repeat("d", 64), Requirement: firstRequirement, Phase: OperationPhaseQueued, Version: 1, CreatedAt: now},
			{OperationID: secondID, Kind: OperationKindTest, IdempotencyKey: "second-model", RequestHash: strings.Repeat("e", 64), Requirement: secondRequirement, Phase: OperationPhaseQueued, Version: 1, CreatedAt: now.Add(time.Millisecond)},
		},
	}}
	ollama := &recordingPullOllama{}
	reconciler := newFixtureReconciler(t, store, &reconcilerChild{snapshot: Snapshot{State: StateStopped}}, ollama)

	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("first ReconcileOnce() error = %v", err)
	}
	if len(ollama.pulled) != 1 || ollama.pulled[0] != "first:latest" {
		t.Fatalf("first reconcile pulled models = %v", ollama.pulled)
	}
	store.mu.Lock()
	if store.snapshot.Operations[0].Phase != OperationPhaseReady || store.snapshot.Operations[1].Phase != OperationPhaseQueued {
		phases := []OperationPhase{store.snapshot.Operations[0].Phase, store.snapshot.Operations[1].Phase}
		store.mu.Unlock()
		t.Fatalf("operation phases after first reconcile = %v", phases)
	}
	store.mu.Unlock()

	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("second ReconcileOnce() error = %v", err)
	}
	if len(ollama.pulled) != 2 || ollama.pulled[1] != "second:latest" {
		t.Fatalf("all pulled models = %v", ollama.pulled)
	}
}

func TestPendingOperationsExcludeProductionProbes(t *testing.T) {
	operations := []OperationRecord{
		{Phase: OperationPhaseProbing},
		{Phase: OperationPhaseReady},
		{Phase: OperationPhaseQueued},
	}
	pending := pendingOperations(operations)
	if len(pending) != 1 || pending[0].Phase != OperationPhaseQueued {
		t.Fatalf("pending operations = %+v", pending)
	}
}

func TestReconcilerKeepsPreviousReadyGenerationServingDuringCandidatePull(t *testing.T) {
	previous, err := NewRequirement([]ModelRef{"old:latest"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewRequirement([]ModelRef{"new:latest"})
	if err != nil {
		t.Fatal(err)
	}
	store := &reconcilerStore{snapshot: DemandSnapshot{
		Requirement:        target,
		Runtime:            RuntimeRecord{Mode: RuntimeModeManaged, Phase: RuntimePhaseReady, Requirement: previous, ReadyHash: previous.Hash, Version: 1},
		RequirementVersion: 1,
	}}
	child := &reconcilerChild{snapshot: Snapshot{State: StateRunning, Generation: 1, Ready: true}}
	ollama := &candidatePullFailureOllama{}
	reconciler := newFixtureReconciler(t, store, child, ollama)
	if err := reconciler.ReconcileOnce(context.Background()); err == nil {
		t.Fatal("ReconcileOnce() unexpectedly succeeded")
	}
	if !child.snapshot.Ready {
		t.Fatal("candidate pull revoked the previous ready generation")
	}
}

func TestReconcilerPersistsMetadataChangesWithinSamePhase(t *testing.T) {
	requirement, err := NewRequirement([]ModelRef{"smollm2:135m"})
	if err != nil {
		t.Fatal(err)
	}
	store := &reconcilerStore{snapshot: DemandSnapshot{
		Requirement:          requirement,
		Runtime:              RuntimeRecord{Mode: RuntimeModeManaged, Phase: RuntimePhaseChecking, Requirement: requirement, ReadyHash: "old-ready", ChildEpoch: 1, Version: 1},
		SettingsStateVersion: 7,
	}}
	reconciler := newFixtureReconciler(t, store, &reconcilerChild{snapshot: Snapshot{State: StateRunning, Generation: 2}}, &reconcilerOllama{})
	reconciler.setSettingsStateVersion(7)
	updated, err := reconciler.transitionRuntimeRecord(context.Background(), store.snapshot.Runtime, RuntimePhaseChecking, requirement, "new-ready", 2, "", false)
	if err != nil {
		t.Fatalf("transitionRuntimeRecord() error = %v", err)
	}
	if updated.ReadyHash != "new-ready" || updated.ChildEpoch != 2 {
		t.Fatalf("updated metadata = %+v", updated)
	}
	if len(store.phases) != 1 || store.phases[0] != RuntimePhaseChecking {
		t.Fatalf("same-phase transition was not persisted: %v", store.phases)
	}
	if store.commands[0].ExpectedSettingsStateVersion != 7 {
		t.Fatalf("settings-state fence = %d, want 7", store.commands[0].ExpectedSettingsStateVersion)
	}
}

func TestReconcilerRunRetriesInitialClaimUntilItSucceeds(t *testing.T) {
	claimFailed := errors.New("postgres temporarily unavailable")
	store := &reconcilerStore{
		claimErrors: []error{claimFailed},
		snapshot: DemandSnapshot{
			Runtime:      RuntimeRecord{Mode: RuntimeModeManaged, Phase: RuntimePhaseStopped, Version: 1},
			RolloutPhase: "idle",
			MayStop:      true,
		},
		claimAttempts: make(chan struct{}, 4),
	}
	reconciler := newFixtureReconciler(t, store, &reconcilerChild{snapshot: Snapshot{State: StateStopped}}, &reconcilerOllama{})
	reconciler.options.ErrorBackoff = time.Millisecond
	reconciler.options.ReconcileInterval = time.Hour
	errorsReported := make(chan error, 2)
	reconciler.options.OnError = func(err error) { errorsReported <- err }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- reconciler.Run(ctx) }()

	select {
	case err := <-errorsReported:
		if !errors.Is(err, ErrReconcilerLeaseLost) || !errors.Is(err, claimFailed) {
			t.Fatalf("initial claim error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initial claim failure was not reported")
	}
	for attempts := 0; attempts < 2; attempts++ {
		select {
		case <-store.claimAttempts:
		case <-time.After(time.Second):
			t.Fatal("initial claim was not retried")
		}
	}
	if got := store.ClaimCalls(); got < 2 {
		t.Fatalf("claim calls = %d, want at least 2", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not exit after cancellation")
	}
}

func TestReconcilerRunCancellationInterruptsInitialClaimBackoff(t *testing.T) {
	store := &reconcilerStore{claimErrors: []error{errors.New("postgres temporarily unavailable")}, claimAttempts: make(chan struct{}, 1)}
	reconciler := newFixtureReconciler(t, store, &reconcilerChild{}, &reconcilerOllama{})
	reconciler.options.ErrorBackoff = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- reconciler.Run(ctx) }()
	select {
	case <-store.claimAttempts:
	case <-time.After(time.Second):
		t.Fatal("initial claim was not attempted")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not exit while waiting to retry initial claim")
	}
}

func TestReconcilerRunReturnsModeMismatchWithoutRetry(t *testing.T) {
	store := &reconcilerStore{claimLease: ManagerLease{OwnerEpoch: 1, Version: 1, Mode: RuntimeModeExternalStatic}}
	reconciler := newFixtureReconciler(t, store, &reconcilerChild{}, &reconcilerOllama{})
	reconciler.options.OnError = func(error) { t.Fatal("mode mismatch should not be retried") }
	if err := reconciler.Run(context.Background()); !errors.Is(err, ErrRuntimeModeMismatch) {
		t.Fatalf("Run() error = %v, want %v", err, ErrRuntimeModeMismatch)
	}
	if got := store.ClaimCalls(); got != 1 {
		t.Fatalf("claim calls = %d, want 1", got)
	}
}

func newFixtureReconciler(t *testing.T, store *reconcilerStore, child *reconcilerChild, ollama OllamaControlClient) *Reconciler {
	t.Helper()
	owner, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewReconciler(store, child, ollama, ReconcilerOptions{OwnerID: owner, HealthWait: 20 * time.Millisecond, ProgressMinInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	reconciler.setLease(ManagerLease{OwnerID: owner, OwnerEpoch: 1, Version: 1, Mode: RuntimeModeManaged})
	return reconciler
}

type reconcilerChild struct {
	mu               sync.Mutex
	snapshot         Snapshot
	generationFloors []uint64
	ensureCalls      int
	stopCalls        int
}

func (child *reconcilerChild) SetGenerationFloor(floor uint64) error {
	child.mu.Lock()
	defer child.mu.Unlock()
	if child.snapshot.State != StateStopped {
		return errors.New("generation floor requires stopped child")
	}
	child.generationFloors = append(child.generationFloors, floor)
	if floor > child.snapshot.Generation {
		child.snapshot.Generation = floor
	}
	return nil
}
func (child *reconcilerChild) Ensure(context.Context) error {
	child.mu.Lock()
	defer child.mu.Unlock()
	child.ensureCalls++
	if child.snapshot.State != StateRunning {
		child.snapshot.Generation++
	}
	child.snapshot.State = StateRunning
	return nil
}
func (child *reconcilerChild) Stop(context.Context) error {
	child.mu.Lock()
	defer child.mu.Unlock()
	child.stopCalls++
	child.snapshot.State = StateStopped
	child.snapshot.Ready = false
	return nil
}
func (child *reconcilerChild) Snapshot() Snapshot {
	child.mu.Lock()
	defer child.mu.Unlock()
	return child.snapshot
}
func (child *reconcilerChild) SetInferenceReady(ready bool) error {
	child.mu.Lock()
	defer child.mu.Unlock()
	child.snapshot.Ready = ready
	return nil
}

type reconcilerOllama struct {
	missing   bool
	pullCalls int
}

type candidatePullFailureOllama struct{}

type blockingRecoveryOllama struct {
	mu        sync.Mutex
	started   chan struct{}
	pullCalls int
}

type recordingPullOllama struct {
	pulled    []ModelRef
	installed map[ModelRef]bool
}

func (ollama *recordingPullOllama) Health(context.Context) error { return nil }
func (ollama *recordingPullOllama) Tags(context.Context) ([]OllamaModel, error) {
	models := make([]OllamaModel, 0, len(ollama.installed))
	for model := range ollama.installed {
		models = append(models, OllamaModel{Name: model, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Bytes: 2})
	}
	return models, nil
}
func (ollama *recordingPullOllama) Show(_ context.Context, model ModelRef) (OllamaModel, error) {
	if !ollama.installed[model] {
		return OllamaModel{}, ErrOllamaModelMissing
	}
	return OllamaModel{Name: model, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Bytes: 2}, nil
}
func (ollama *recordingPullOllama) Pull(_ context.Context, model ModelRef, _ func(PullProgress) error) (OllamaModel, error) {
	if ollama.installed == nil {
		ollama.installed = make(map[ModelRef]bool)
	}
	ollama.pulled = append(ollama.pulled, model)
	ollama.installed[model] = true
	return OllamaModel{Name: model, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Bytes: 2}, nil
}

func (*candidatePullFailureOllama) Health(context.Context) error { return nil }
func (*candidatePullFailureOllama) Tags(context.Context) ([]OllamaModel, error) {
	return []OllamaModel{{Name: "old:latest", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Bytes: 2}}, nil
}
func (*candidatePullFailureOllama) Show(_ context.Context, model ModelRef) (OllamaModel, error) {
	if model != "old:latest" {
		return OllamaModel{}, ErrOllamaModelMissing
	}
	return OllamaModel{Name: model, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Bytes: 2}, nil
}
func (*candidatePullFailureOllama) Pull(context.Context, ModelRef, func(PullProgress) error) (OllamaModel, error) {
	return OllamaModel{}, errors.New("candidate pull failed")
}

func (*blockingRecoveryOllama) Health(context.Context) error { return nil }
func (*blockingRecoveryOllama) Tags(context.Context) ([]OllamaModel, error) {
	return nil, nil
}
func (*blockingRecoveryOllama) Show(context.Context, ModelRef) (OllamaModel, error) {
	return OllamaModel{}, ErrOllamaModelMissing
}
func (ollama *blockingRecoveryOllama) Pull(ctx context.Context, _ ModelRef, _ func(PullProgress) error) (OllamaModel, error) {
	ollama.mu.Lock()
	ollama.pullCalls++
	if ollama.pullCalls == 1 {
		close(ollama.started)
	}
	ollama.mu.Unlock()
	<-ctx.Done()
	return OllamaModel{}, ctx.Err()
}
func (ollama *blockingRecoveryOllama) PullCalls() int {
	ollama.mu.Lock()
	defer ollama.mu.Unlock()
	return ollama.pullCalls
}

func (ollama *reconcilerOllama) Health(context.Context) error { return nil }
func (ollama *reconcilerOllama) Tags(context.Context) ([]OllamaModel, error) {
	if ollama.missing {
		return nil, nil
	}
	return []OllamaModel{{Name: "smollm2:135m", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Bytes: 2}}, nil
}
func (ollama *reconcilerOllama) Show(context.Context, ModelRef) (OllamaModel, error) {
	if ollama.missing {
		return OllamaModel{}, ErrOllamaModelMissing
	}
	return OllamaModel{Name: "smollm2:135m", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Bytes: 2}, nil
}
func (ollama *reconcilerOllama) Pull(context.Context, ModelRef, func(PullProgress) error) (OllamaModel, error) {
	ollama.pullCalls++
	ollama.missing = false
	return OllamaModel{Name: "smollm2:135m", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Bytes: 2}, nil
}

type reconcilerStore struct {
	mu                           sync.Mutex
	snapshot                     DemandSnapshot
	readErr                      error
	published                    *RuntimeRecord
	phases                       []RuntimePhase
	commands                     []RuntimePhaseCommand
	claimErrors                  []error
	claimLease                   ManagerLease
	claimCalls                   int
	claimAttempts                chan struct{}
	completedOperations          int
	activeRecoverySeeds          int
	activeRecoveryCheckErrors    []error
	activeRecoveryCheckDeadlines chan time.Duration
}

func (store *reconcilerStore) ClaimManager(context.Context, ManagerClaimCommand) (ManagerLease, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claimCalls++
	if store.claimAttempts != nil {
		select {
		case store.claimAttempts <- struct{}{}:
		default:
		}
	}
	if len(store.claimErrors) > 0 {
		err := store.claimErrors[0]
		store.claimErrors = store.claimErrors[1:]
		return ManagerLease{}, err
	}
	if store.claimLease.Mode != "" {
		return store.claimLease, nil
	}
	return ManagerLease{OwnerEpoch: 1, Version: 1, Mode: RuntimeModeManaged}, nil
}

func (store *reconcilerStore) ClaimCalls() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.claimCalls
}
func (store *reconcilerStore) HeartbeatManager(context.Context, ManagerHeartbeatCommand) (ManagerLease, error) {
	return ManagerLease{OwnerEpoch: 1, Version: 1, Mode: RuntimeModeManaged}, nil
}
func (store *reconcilerStore) ReadDemand(context.Context) (DemandSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.snapshot, store.readErr
}
func (store *reconcilerStore) PublishDemand(_ context.Context, command DemandCASCommand) (RuntimeRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	runtime := store.snapshot.Runtime
	runtime.Requirement = command.Requirement
	runtime.RequirementVersion++
	runtime.Version++
	store.snapshot.Runtime = runtime
	store.snapshot.RequirementVersion = runtime.RequirementVersion
	store.published = &runtime
	return runtime, nil
}
func (store *reconcilerStore) LoadEffectiveIntent(context.Context, IntentReadCommand) (IntentSnapshot, error) {
	return IntentSnapshot{}, nil
}
func (store *reconcilerStore) SeedActiveRecovery(_ context.Context, command ActiveRecoveryCommand) (OperationRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, operation := range store.snapshot.Operations {
		if operation.Kind == OperationKindActiveRecovery {
			return operation, nil
		}
	}
	for _, source := range store.snapshot.Sources {
		if source.Kind != "active" || len(source.Requirement.Models) == 0 || command.ExpectedSettingsStateVersion != store.snapshot.SettingsStateVersion {
			continue
		}
		operationID, _ := foundation.ParseID("80000000-0000-4000-8000-000000000099")
		target := source.Revision
		operation := OperationRecord{
			OperationID: operationID, Kind: OperationKindActiveRecovery,
			IdempotencyKey: "active-recovery", RequestHash: strings.Repeat("f", 64),
			TargetRevision: &target, Requirement: source.Requirement,
			Phase: OperationPhaseQueued, Version: 1, CreatedAt: time.Now(),
		}
		store.snapshot.Operations = append(store.snapshot.Operations, operation)
		store.activeRecoverySeeds++
		return operation, nil
	}
	return OperationRecord{}, errors.New("active recovery unavailable")
}
func (store *reconcilerStore) ActiveRecoveryCurrent(ctx context.Context, command ActiveRecoveryCheckCommand) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.activeRecoveryCheckDeadlines != nil {
		remaining := time.Duration(-1)
		if deadline, ok := ctx.Deadline(); ok {
			remaining = time.Until(deadline)
		}
		store.activeRecoveryCheckDeadlines <- remaining
	}
	if len(store.activeRecoveryCheckErrors) > 0 {
		err := store.activeRecoveryCheckErrors[0]
		store.activeRecoveryCheckErrors = store.activeRecoveryCheckErrors[1:]
		return false, err
	}
	for _, operation := range store.snapshot.Operations {
		if operation.OperationID != command.OperationID || operation.Kind != OperationKindActiveRecovery || operation.TerminalAt != nil {
			continue
		}
		for _, source := range store.snapshot.Sources {
			if source.Kind == "active" && operation.TargetRevision != nil && source.Revision == *operation.TargetRevision {
				return true, nil
			}
		}
	}
	return false, nil
}
func (store *reconcilerStore) ClaimOperation(_ context.Context, command OperationClaimCommand) (OperationRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.snapshot.Operations {
		operation := &store.snapshot.Operations[index]
		if operation.OperationID != command.OperationID {
			continue
		}
		ownerID := command.OwnerID
		expiresAt := time.Now().Add(command.LeaseDuration)
		operation.ClaimOwnerID = &ownerID
		operation.ClaimOwnerEpoch = command.OwnerEpoch
		operation.ClaimExpiresAt = &expiresAt
		operation.Version++
		return *operation, nil
	}
	return OperationRecord{}, errors.New("operation not found")
}
func (store *reconcilerStore) BeginPullAttempt(_ context.Context, command PullAttemptCommand) (PullAttemptResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.snapshot.Operations {
		operation := &store.snapshot.Operations[index]
		if operation.OperationID != command.OperationID {
			continue
		}
		if operation.Kind == OperationKindActiveRecovery {
			current := false
			for _, source := range store.snapshot.Sources {
				if source.Kind == "active" && operation.TargetRevision != nil && source.Revision == *operation.TargetRevision {
					current = true
					break
				}
			}
			if !current {
				return PullAttemptResult{}, errors.New("active recovery is stale")
			}
		}
		if operation.AttemptNo >= 3 {
			operation.Phase = OperationPhaseFailed
			operation.ErrorCode = "LOCAL_MODEL_RUNTIME_PULL_ATTEMPTS_EXHAUSTED"
			operation.Retryable = false
			now := time.Now()
			operation.TerminalAt = &now
			operation.Version++
			return PullAttemptResult{Operation: *operation, Remaining: 6 * time.Hour}, nil
		}
		operation.AttemptNo++
		operation.Version++
		return PullAttemptResult{Operation: *operation, Remaining: 6 * time.Hour}, nil
	}
	return PullAttemptResult{}, errors.New("operation not found")
}
func (store *reconcilerStore) RecordOperationProgress(_ context.Context, command OperationProgressCommand) (OperationRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.snapshot.Operations {
		operation := &store.snapshot.Operations[index]
		if operation.OperationID != command.OperationID {
			continue
		}
		operation.Phase = command.NextPhase
		operation.CompletedModels = append([]ModelRef(nil), command.CompletedModels...)
		operation.CompletedBytes = command.CompletedBytes
		operation.TotalBytes = cloneInt64(command.TotalBytes)
		operation.ProgressKnown = command.ProgressKnown
		operation.ResolvedModels = append([]ResolvedModel(nil), command.ResolvedModels...)
		operation.Version++
		return *operation, nil
	}
	return OperationRecord{}, errors.New("operation not found")
}
func (store *reconcilerStore) CompleteOperation(_ context.Context, command OperationTerminalCommand) (OperationRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.snapshot.Operations {
		operation := &store.snapshot.Operations[index]
		if operation.OperationID != command.OperationID {
			continue
		}
		operation.Phase = command.Phase
		operation.ErrorCode = command.ErrorCode
		operation.Retryable = command.Retryable
		operation.CompletedModels = append([]ModelRef(nil), command.CompletedModels...)
		operation.CompletedBytes = command.CompletedBytes
		operation.TotalBytes = cloneInt64(command.TotalBytes)
		if operation.Kind == OperationKindActiveRecovery && command.Phase == OperationPhaseReady {
			operation.Phase = OperationPhaseSucceeded
			now := time.Now()
			operation.TerminalAt = &now
		}
		operation.Version++
		store.completedOperations++
		return *operation, nil
	}
	return OperationRecord{}, errors.New("operation not found")
}
func (store *reconcilerStore) SweepExpiredOperations(_ context.Context, command OperationExpirySweepCommand) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var expired int64
	now := time.Now()
	for index := range store.snapshot.Operations {
		operation := &store.snapshot.Operations[index]
		if operation.TerminalAt != nil || operation.CreatedAt.IsZero() || operation.CreatedAt.Add(6*time.Hour).After(now) {
			continue
		}
		operation.Phase = OperationPhaseFailed
		operation.ErrorCode = "LOCAL_MODEL_RUNTIME_PREPARATION_TIMEOUT"
		operation.Retryable = false
		operation.TerminalAt = &now
		operation.ClaimOwnerID = nil
		operation.ClaimOwnerEpoch = 0
		operation.ClaimExpiresAt = nil
		operation.Version++
		expired++
	}
	return expired, nil
}
func (store *reconcilerStore) AcquireHold(context.Context, HoldAcquireCommand) (HoldRecord, error) {
	return HoldRecord{}, nil
}
func (store *reconcilerStore) RenewHold(context.Context, HoldRenewCommand) (HoldRecord, error) {
	return HoldRecord{}, nil
}
func (store *reconcilerStore) ReleaseHold(context.Context, HoldReleaseCommand) (HoldRecord, error) {
	return HoldRecord{}, nil
}
func (store *reconcilerStore) CompareAndSetRuntimePhase(_ context.Context, command RuntimePhaseCommand) (RuntimeRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.phases = append(store.phases, command.NextPhase)
	store.commands = append(store.commands, command)
	runtime := store.snapshot.Runtime
	runtime.Phase = command.NextPhase
	runtime.Requirement.Hash = command.RequirementHash
	runtime.ReadyHash = command.ReadyHash
	runtime.ChildEpoch = command.ChildEpoch
	runtime.ErrorCode = command.ErrorCode
	runtime.Retryable = command.Retryable
	runtime.Version++
	store.snapshot.Runtime = runtime
	return runtime, nil
}
