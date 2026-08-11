package runtime

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

type runtimeHostTestGeneration struct {
	revision  int64
	embedding retrievaldomain.EmbeddingContract
	secret    string
	closed    atomic.Bool
}

type runtimeHostTestFactory struct {
	mu sync.Mutex

	buildCalls map[int64]int
	probeCalls map[int64]int
	closeCalls map[*runtimeHostTestGeneration]int
	contracts  map[int64]retrievaldomain.EmbeddingContract
	buildGates map[int64]<-chan struct{}
	started    map[int64]chan<- struct{}
	buildErrs  map[int64]error
	probeErrs  map[int64]error
}

func newRuntimeHostTestFactory() *runtimeHostTestFactory {
	return &runtimeHostTestFactory{
		buildCalls: make(map[int64]int),
		probeCalls: make(map[int64]int),
		closeCalls: make(map[*runtimeHostTestGeneration]int),
		contracts:  make(map[int64]retrievaldomain.EmbeddingContract),
		buildGates: make(map[int64]<-chan struct{}),
		started:    make(map[int64]chan<- struct{}),
		buildErrs:  make(map[int64]error),
		probeErrs:  make(map[int64]error),
	}
}

func (factory *runtimeHostTestFactory) Build(ctx context.Context, revision int64) (*runtimeHostTestGeneration, error) {
	factory.mu.Lock()
	factory.buildCalls[revision]++
	gate := factory.buildGates[revision]
	started := factory.started[revision]
	buildErr := factory.buildErrs[revision]
	contract := factory.contracts[revision]
	factory.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-gate:
		}
	}
	if buildErr != nil {
		return nil, buildErr
	}
	return &runtimeHostTestGeneration{revision: revision, embedding: contract}, nil
}

func (factory *runtimeHostTestFactory) Probe(ctx context.Context, generation *runtimeHostTestGeneration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	factory.mu.Lock()
	factory.probeCalls[generation.revision]++
	err := factory.probeErrs[generation.revision]
	factory.mu.Unlock()
	return err
}

func (factory *runtimeHostTestFactory) Close(generation *runtimeHostTestGeneration) {
	if generation == nil {
		return
	}
	generation.closed.Store(true)
	factory.mu.Lock()
	factory.closeCalls[generation]++
	factory.mu.Unlock()
}

func (factory *runtimeHostTestFactory) counts(revision int64) (builds, probes int) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.buildCalls[revision], factory.probeCalls[revision]
}

func (factory *runtimeHostTestFactory) closeCount(generation *runtimeHostTestGeneration) int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.closeCalls[generation]
}

func (factory *runtimeHostTestFactory) compatible(generation *runtimeHostTestGeneration, version retrievaldomain.EmbeddingVersion) error {
	if generation == nil {
		return errors.New("generation is unavailable")
	}
	return retrievaldomain.ValidateEmbeddingContractBinding(generation.embedding, version)
}

func TestRuntimeHostActivationPinsOldLeaseAndGatesNewAcquisition(t *testing.T) {
	t.Parallel()

	factory := newRuntimeHostTestFactory()
	initial := &runtimeHostTestGeneration{revision: 1}
	host := newManagedRuntimeHost(t, factory, initial, nil)
	oldLease, err := host.Acquire(context.Background(), CurrentRuntime())
	if err != nil {
		t.Fatal(err)
	}
	oldValue := oldLease.Value()
	if err := host.prepare(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	beforeArm, err := host.Acquire(context.Background(), CurrentRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if beforeArm.Binding().Revision != 1 {
		t.Fatalf("preparing current revision = %d, want 1", beforeArm.Binding().Revision)
	}
	beforeArm.Release()
	if err := host.arm(2); err != nil {
		t.Fatal(err)
	}

	result := make(chan RuntimeLease[*runtimeHostTestGeneration], 1)
	failure := make(chan error, 1)
	go func() {
		lease, acquireErr := host.Acquire(context.Background(), CurrentRuntime())
		if acquireErr != nil {
			failure <- acquireErr
			return
		}
		result <- lease
	}()
	assertNoRuntimeHostResult(t, result, failure)
	if err := host.activate(2); err != nil {
		t.Fatal(err)
	}
	if err := host.activate(2); err != nil {
		t.Fatalf("duplicate activation error = %v", err)
	}
	if err := host.prepare(context.Background(), 2); err != nil {
		t.Fatalf("duplicate post-activation prepare error = %v", err)
	}
	if err := host.arm(2); err != nil {
		t.Fatalf("duplicate post-activation arm error = %v", err)
	}
	if err := host.abort(2); err == nil {
		t.Fatal("stale abort unexpectedly reopened committed activation")
	}
	assertNoRuntimeHostResult(t, result, failure)
	if oldValue.closed.Load() {
		t.Fatal("old generation closed while its lease was held")
	}
	if err := host.reopen(2); err != nil {
		t.Fatal(err)
	}
	if err := host.activate(2); err != nil {
		t.Fatalf("duplicate finalized activation error = %v", err)
	}

	var newLease RuntimeLease[*runtimeHostTestGeneration]
	select {
	case err := <-failure:
		t.Fatal(err)
	case newLease = <-result:
	case <-time.After(time.Second):
		t.Fatal("new current acquisition did not resume")
	}
	if newLease.Binding().Revision != 2 || newLease.Value().revision != 2 {
		t.Fatalf("new lease = %v revision=%d", newLease.Binding(), newLease.Value().revision)
	}

	var releases sync.WaitGroup
	for range 64 {
		releases.Add(1)
		go func() {
			defer releases.Done()
			oldLease.Release()
		}()
	}
	releases.Wait()
	if factory.closeCount(oldValue) != 1 || !oldValue.closed.Load() {
		t.Fatalf("old generation close count = %d", factory.closeCount(oldValue))
	}
	newValue := newLease.Value()
	newLease.Release()
	host.close()
	host.close()
	if factory.closeCount(newValue) != 1 {
		t.Fatalf("new generation close count = %d", factory.closeCount(newValue))
	}
}

func TestRuntimeHostPrepareFailureAndAbortKeepCurrentServing(t *testing.T) {
	t.Parallel()

	factory := newRuntimeHostTestFactory()
	factory.probeErrs[2] = errors.New("provider probe failed")
	initial := &runtimeHostTestGeneration{revision: 1}
	host := newManagedRuntimeHost(t, factory, initial, nil)
	defer host.close()

	err := host.prepare(context.Background(), 2)
	assertRuntimeHostCode(t, err, "MODEL_SETTINGS_ACTIVATION_PREPARE_FAILED")
	failedCandidate := findClosedRuntimeHostGeneration(t, factory, 2)
	if factory.closeCount(failedCandidate) != 1 {
		t.Fatalf("failed candidate close count = %d", factory.closeCount(failedCandidate))
	}
	lease, err := host.Acquire(context.Background(), CurrentRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if lease.Binding().Revision != 1 {
		t.Fatalf("current revision after prepare failure = %d", lease.Binding().Revision)
	}
	lease.Release()

	factory.mu.Lock()
	delete(factory.probeErrs, 2)
	factory.mu.Unlock()
	if err := host.prepare(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	candidate := host.candidate.generation.value
	host.mu.Unlock()
	if err := host.arm(2); err != nil {
		t.Fatal(err)
	}
	if err := host.abort(2); err != nil {
		t.Fatal(err)
	}
	if err := host.abort(2); err != nil {
		t.Fatal(err)
	}
	if factory.closeCount(candidate) != 1 {
		t.Fatalf("aborted candidate close count = %d", factory.closeCount(candidate))
	}
	lease, err = host.Acquire(context.Background(), CurrentRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if lease.Binding().Revision != 1 {
		t.Fatalf("current revision after abort = %d", lease.Binding().Revision)
	}
	lease.Release()
}

func TestRuntimeHostAbortPreventsInflightPreparationFromInstallingCandidate(t *testing.T) {
	t.Parallel()

	factory := newRuntimeHostTestFactory()
	buildGate := make(chan struct{})
	started := make(chan struct{}, 1)
	factory.buildGates[2] = buildGate
	factory.started[2] = started
	host := newManagedRuntimeHost(t, factory, &runtimeHostTestGeneration{revision: 1}, nil)
	defer host.close()
	result := make(chan error, 1)
	go func() {
		result <- host.prepare(context.Background(), 2)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("preparation build did not start")
	}
	if err := host.abort(2); err != nil {
		t.Fatal(err)
	}
	close(buildGate)
	select {
	case err := <-result:
		assertRuntimeHostCode(t, err, "MODEL_SETTINGS_ACTIVATION_CONFLICT")
	case <-time.After(time.Second):
		t.Fatal("aborted preparation did not finish")
	}
	host.mu.Lock()
	candidate := host.candidate
	host.mu.Unlock()
	if candidate != nil {
		t.Fatal("aborted in-flight preparation installed a candidate")
	}
	aborted := findClosedRuntimeHostGeneration(t, factory, 2)
	if got := factory.closeCount(aborted); got != 1 {
		t.Fatalf("aborted in-flight generation close count = %d, want 1", got)
	}
	lease, err := host.Acquire(context.Background(), CurrentRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if lease.Binding().Revision != 1 {
		t.Fatalf("current revision after in-flight abort = %d, want 1", lease.Binding().Revision)
	}
	lease.Release()
}

func TestRuntimeHostHistoricalBuildIsSingleflightAndWaitersCancelIndependently(t *testing.T) {
	t.Parallel()

	factory := newRuntimeHostTestFactory()
	buildGate := make(chan struct{})
	started := make(chan struct{}, 1)
	factory.buildGates[7] = buildGate
	factory.started[7] = started
	host := newManagedRuntimeHost(t, factory, &runtimeHostTestGeneration{revision: 1}, nil)
	defer host.close()

	const successfulAcquires = 64
	leases := make(chan RuntimeLease[*runtimeHostTestGeneration], successfulAcquires)
	failures := make(chan error, successfulAcquires)
	for range successfulAcquires {
		go func() {
			lease, err := host.Acquire(context.Background(), RevisionRuntime(7))
			if err != nil {
				failures <- err
				return
			}
			leases <- lease
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("historical build did not start")
	}

	cancelContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := host.Acquire(cancelContext, RevisionRuntime(7))
	assertRuntimeHostCode(t, err, ErrorCodeRuntimeRevisionUnavailable)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled waiter error = %v", err)
	}
	if builds, _ := factory.counts(7); builds != 1 {
		t.Fatalf("build calls before release = %d, want 1", builds)
	}
	close(buildGate)

	var first *runtimeHostTestGeneration
	collected := make([]RuntimeLease[*runtimeHostTestGeneration], 0, successfulAcquires)
	for range successfulAcquires {
		select {
		case failure := <-failures:
			t.Fatal(failure)
		case lease := <-leases:
			if first == nil {
				first = lease.Value()
			}
			if lease.Value() != first || lease.Binding().Revision != 7 {
				t.Fatalf("singleflight lease = %p %v, want %p revision 7", lease.Value(), lease.Binding(), first)
			}
			collected = append(collected, lease)
		case <-time.After(time.Second):
			t.Fatal("historical acquisition did not finish")
		}
	}
	if builds, probes := factory.counts(7); builds != 1 || probes != 1 {
		t.Fatalf("build/probe calls = %d/%d, want 1/1", builds, probes)
	}
	for _, lease := range collected {
		lease.Release()
		lease.Release()
	}
	if factory.closeCount(first) != 0 {
		t.Fatal("cached historical generation closed before host shutdown")
	}
	host.close()
	if factory.closeCount(first) != 1 {
		t.Fatalf("historical close count = %d", factory.closeCount(first))
	}
}

func TestRuntimeHostTargetsValidateAttemptAndEmbeddingCompatibility(t *testing.T) {
	t.Parallel()

	contractA := runtimeHostEmbeddingContract(t, "embedding-a", 3)
	contractB := runtimeHostEmbeddingContract(t, "embedding-b", 4)
	factory := newRuntimeHostTestFactory()
	factory.contracts[3] = contractB
	initial := &runtimeHostTestGeneration{revision: 1, embedding: contractA}
	host := newManagedRuntimeHost(t, factory, initial, factory.compatible)
	defer host.close()
	if err := host.prepare(context.Background(), 0); err == nil {
		t.Fatal("managed host prepared revision 0")
	} else {
		assertRuntimeHostCode(t, err, ErrorCodeRuntimeBindingMismatch)
	}
	if builds, _ := factory.counts(0); builds != 0 {
		t.Fatalf("invalid revision 0 build calls = %d, want 0", builds)
	}

	attemptLease, err := host.Acquire(context.Background(), AttemptRuntime(managedRuntimeBinding(1)))
	if err != nil {
		t.Fatal(err)
	}
	if attemptLease.Binding() != managedRuntimeBinding(1) {
		t.Fatalf("attempt binding = %v", attemptLease.Binding())
	}
	attemptLease.Release()
	foreign := managedRuntimeBinding(1)
	foreign.InstanceID = foundation.ID("20000000-0000-4000-8000-000000000099")
	_, err = host.Acquire(context.Background(), AttemptRuntime(foreign))
	assertRuntimeHostCode(t, err, ErrorCodeRuntimeBindingMismatch)
	zeroRevision := managedRuntimeBinding(0)
	_, err = host.Acquire(context.Background(), AttemptRuntime(zeroRevision))
	assertRuntimeHostCode(t, err, ErrorCodeRuntimeRevisionUnavailable)
	if builds, _ := factory.counts(0); builds != 0 {
		t.Fatalf("retired canonical revision build calls = %d, want 0", builds)
	}
	_, err = host.Acquire(context.Background(), RevisionRuntime(0))
	assertRuntimeHostCode(t, err, ErrorCodeRuntimeBindingMismatch)
	_, err = host.Acquire(context.Background(), RuntimeTarget{})
	assertRuntimeHostCode(t, err, ErrorCodeRuntimeBindingMismatch)

	compatibleVersion := runtimeHostEmbeddingVersion(contractA, 99, "30000000-0000-4000-8000-000000000001")
	compatibleTarget := EmbeddingRuntime(compatibleVersion)
	*compatibleVersion.ModelSettingsRevision = 100 // The sealed target owns its revision copy.
	compatibleLease, err := host.Acquire(context.Background(), compatibleTarget)
	if err != nil {
		t.Fatal(err)
	}
	if compatibleLease.Binding().Revision != 1 || compatibleLease.Value() != initial {
		t.Fatalf("compatible lease = %v %p, want active revision 1", compatibleLease.Binding(), compatibleLease.Value())
	}
	compatibleLease.Release()
	if builds, _ := factory.counts(99); builds != 0 {
		t.Fatalf("compatible different-revision build calls = %d, want 0", builds)
	}

	historicalVersion := runtimeHostEmbeddingVersion(contractB, 3, "30000000-0000-4000-8000-000000000002")
	historicalLease, err := host.Acquire(context.Background(), EmbeddingRuntime(historicalVersion))
	if err != nil {
		t.Fatal(err)
	}
	if historicalLease.Binding().Revision != 3 || historicalLease.Value().embedding != contractB {
		t.Fatalf("historical compatible lease = %v contract=%v", historicalLease.Binding(), historicalLease.Value().embedding)
	}
	historicalLease.Release()
	if builds, probes := factory.counts(3); builds != 1 || probes != 1 {
		t.Fatalf("historical build/probe calls = %d/%d", builds, probes)
	}

	nonReconstructable := runtimeHostEmbeddingVersion(contractB, 0, "30000000-0000-4000-8000-000000000003")
	nonReconstructableLease, err := host.Acquire(context.Background(), EmbeddingRuntime(nonReconstructable))
	if err != nil {
		// A compatible historical generation now exists, so revision 0 provenance can safely reuse it.
		t.Fatal(err)
	}
	nonReconstructableLease.Release()
}

func TestRuntimeHostManagedCanonicalAttemptPinsOnlyResidentRevisionZero(t *testing.T) {
	t.Parallel()

	factory := newRuntimeHostTestFactory()
	initial := &runtimeHostTestGeneration{revision: 0}
	host := newManagedRuntimeHost(t, factory, initial, nil)
	defer host.close()

	zeroLease, err := host.Acquire(context.Background(), AttemptRuntime(managedRuntimeBinding(0)))
	if err != nil {
		t.Fatal(err)
	}
	if zeroLease.Value() != initial || zeroLease.Binding().Revision != 0 {
		t.Fatalf("canonical attempt lease = %p / %v", zeroLease.Value(), zeroLease.Binding())
	}
	if err := host.prepare(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := host.arm(1); err != nil {
		t.Fatal(err)
	}
	if err := host.activate(1); err != nil {
		t.Fatal(err)
	}
	if factory.closeCount(initial) != 0 {
		t.Fatal("resident canonical generation closed while attempt lease was active")
	}
	zeroLease.Release()
	if factory.closeCount(initial) != 1 {
		t.Fatalf("canonical generation close count = %d, want 1", factory.closeCount(initial))
	}
	_, err = host.Acquire(context.Background(), AttemptRuntime(managedRuntimeBinding(0)))
	assertRuntimeHostCode(t, err, ErrorCodeRuntimeRevisionUnavailable)
	if builds, _ := factory.counts(0); builds != 0 {
		t.Fatalf("canonical revision reconstruction calls = %d, want 0", builds)
	}
}

func TestRuntimeHostGateWaiterCanCancelWithoutOpeningAdmission(t *testing.T) {
	t.Parallel()

	factory := newRuntimeHostTestFactory()
	host := newManagedRuntimeHost(t, factory, &runtimeHostTestGeneration{revision: 1}, nil)
	defer host.close()
	if err := host.prepare(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if err := host.arm(2); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := host.Acquire(ctx, CurrentRuntime())
	assertRuntimeHostCode(t, err, ErrorCodeRuntimeSwitching)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("gate waiter error = %v, want deadline cause", err)
	}
	if err := host.abort(2); err != nil {
		t.Fatal(err)
	}
	lease, err := host.Acquire(context.Background(), CurrentRuntime())
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
}

func TestRuntimeHostAdmissionPinsEntryAndResolvesDurableTargetAfterGateCloses(t *testing.T) {
	t.Parallel()

	contract1 := runtimeHostEmbeddingContract(t, "embedding-v1", 3)
	contract2 := runtimeHostEmbeddingContract(t, "embedding-v2", 4)
	contract3 := runtimeHostEmbeddingContract(t, "embedding-v3", 5)
	factory := newRuntimeHostTestFactory()
	factory.contracts[2] = contract2
	factory.contracts[3] = contract3
	initial := &runtimeHostTestGeneration{revision: 1, embedding: contract1}
	host := newManagedRuntimeHost(t, factory, initial, factory.compatible)
	defer host.close()

	admission, err := host.Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := host.prepare(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if err := host.arm(2); err != nil {
		t.Fatal(err)
	}
	if err := host.activate(2); err != nil {
		t.Fatal(err)
	}

	blockedContext, cancelBlocked := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelBlocked()
	_, err = host.Admit(blockedContext)
	assertRuntimeHostCode(t, err, ErrorCodeRuntimeSwitching)

	entryLease, err := admission.Acquire(context.Background(), CurrentRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if entryLease.Binding().Revision != 1 || entryLease.Value() != initial {
		t.Fatalf("admitted current = %v/%p, want revision 1/%p", entryLease.Binding(), entryLease.Value(), initial)
	}
	version3 := runtimeHostEmbeddingVersion(contract3, 3, "30000000-0000-4000-8000-000000000003")
	historicalLease, err := admission.Acquire(context.Background(), EmbeddingRuntime(version3))
	if err != nil {
		t.Fatal(err)
	}
	if historicalLease.Binding().Revision != 3 || historicalLease.Value().embedding != contract3 {
		t.Fatalf("admitted historical = %v/%+v, want revision 3 contract", historicalLease.Binding(), historicalLease.Value().embedding)
	}
	if builds, probes := factory.counts(3); builds != 1 || probes != 1 {
		t.Fatalf("admitted historical build/probe calls = %d/%d, want 1/1", builds, probes)
	}

	entryLease.Release()
	if initial.closed.Load() {
		t.Fatal("entry generation closed while admission token was held")
	}
	var releases sync.WaitGroup
	for range 32 {
		releases.Add(1)
		go func() {
			defer releases.Done()
			admission.Release()
		}()
	}
	releases.Wait()
	if factory.closeCount(initial) != 1 || !initial.closed.Load() {
		t.Fatalf("entry generation close count = %d, want 1", factory.closeCount(initial))
	}
	if _, err := admission.Acquire(context.Background(), CurrentRuntime()); err == nil {
		t.Fatal("released admission acquired a runtime")
	}
	historicalLease.Release()
}

func TestRuntimeHostAdmissionKeepsCanonicalAttemptAcquirableAcrossFirstActivation(t *testing.T) {
	t.Parallel()

	factory := newRuntimeHostTestFactory()
	initial := &runtimeHostTestGeneration{revision: 0}
	host := newManagedRuntimeHost(t, factory, initial, nil)
	defer host.close()
	admission, err := host.Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := host.prepare(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := host.arm(1); err != nil {
		t.Fatal(err)
	}
	if err := host.activate(1); err != nil {
		t.Fatal(err)
	}

	lease, err := admission.Acquire(context.Background(), AttemptRuntime(managedRuntimeBinding(0)))
	if err != nil {
		t.Fatal(err)
	}
	if lease.Binding().Revision != 0 || lease.Value() != initial {
		t.Fatalf("canonical attempt lease = %v/%p, want revision 0/%p", lease.Binding(), lease.Value(), initial)
	}
	admission.Release()
	if initial.closed.Load() {
		t.Fatal("canonical generation closed while Attempt lease was active")
	}
	lease.Release()
	if factory.closeCount(initial) != 1 {
		t.Fatalf("canonical generation close count = %d, want 1", factory.closeCount(initial))
	}
}

func TestRuntimeHostAttemptBindingAcquiresDuringGateAndNeverUsesPreCommitCandidate(t *testing.T) {
	t.Parallel()

	factory := newRuntimeHostTestFactory()
	host := newManagedRuntimeHost(t, factory, &runtimeHostTestGeneration{revision: 1}, nil)
	defer host.close()
	oldBinding := managedRuntimeBinding(1)
	newBinding := managedRuntimeBinding(2)
	if err := host.prepare(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if err := host.arm(2); err != nil {
		t.Fatal(err)
	}

	// The claim already persisted oldBinding before the gate. It must not wait.
	oldLease, err := host.Acquire(context.Background(), AttemptRuntime(oldBinding))
	if err != nil {
		t.Fatal(err)
	}
	if oldLease.Binding() != oldBinding {
		t.Fatalf("gated old attempt binding = %v, want %v", oldLease.Binding(), oldBinding)
	}
	oldLease.Release()

	// A target binding is not admitted while it is only a prepared candidate.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = host.Acquire(ctx, AttemptRuntime(newBinding))
	assertRuntimeHostCode(t, err, ErrorCodeRuntimeSwitching)
	if err := host.activate(2); err != nil {
		t.Fatal(err)
	}
	rebuiltOldLease, err := host.Acquire(context.Background(), AttemptRuntime(oldBinding))
	if err != nil {
		t.Fatal(err)
	}
	if rebuiltOldLease.Binding() != oldBinding {
		t.Fatalf("rebuilt old attempt binding = %v, want %v", rebuiltOldLease.Binding(), oldBinding)
	}
	rebuiltOldLease.Release()
	if builds, probes := factory.counts(1); builds != 1 || probes != 1 {
		t.Fatalf("old attempt rebuild/probe calls = %d/%d, want 1/1", builds, probes)
	}
	// After the local swap, the same persisted target binding may pass while
	// the current/default admission remains fenced until durable reopen.
	newLease, err := host.Acquire(context.Background(), AttemptRuntime(newBinding))
	if err != nil {
		t.Fatal(err)
	}
	if newLease.Binding() != newBinding {
		t.Fatalf("gated new attempt binding = %v, want %v", newLease.Binding(), newBinding)
	}
	newLease.Release()
	if _, err := host.Acquire(ctx, CurrentRuntime()); err == nil {
		t.Fatal("current acquisition passed before reopen")
	}
	if err := host.reopen(2); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeHostHistoricalCacheEvictsOnlyZeroReferenceGenerations(t *testing.T) {
	t.Parallel()

	factory := newRuntimeHostTestFactory()
	host := newManagedRuntimeHostWithOptions(t, factory, &runtimeHostTestGeneration{revision: 1}, nil, RuntimeHostOptions[*runtimeHostTestGeneration]{HistoricalLimit: 2})
	defer host.close()

	lease2, err := host.Acquire(context.Background(), RevisionRuntime(2))
	if err != nil {
		t.Fatal(err)
	}
	value2 := lease2.Value()
	lease2.Release()
	lease3, err := host.Acquire(context.Background(), RevisionRuntime(3))
	if err != nil {
		t.Fatal(err)
	}
	lease3.Release()
	lease4, err := host.Acquire(context.Background(), RevisionRuntime(4))
	if err != nil {
		t.Fatal(err)
	}
	lease4.Release()
	if factory.closeCount(value2) != 1 {
		t.Fatalf("oldest historical close count = %d, want 1", factory.closeCount(value2))
	}
	host.mu.Lock()
	historyCount := len(host.historical)
	host.mu.Unlock()
	if historyCount != 2 {
		t.Fatalf("historical cache size = %d, want 2", historyCount)
	}
}

func TestRuntimeHostConcurrentAcquireAcrossRepeatedActivationNeverReturnsClosedValue(t *testing.T) {
	factory := newRuntimeHostTestFactory()
	host := newManagedRuntimeHost(t, factory, &runtimeHostTestGeneration{revision: 1}, nil)
	defer host.close()

	stop := make(chan struct{})
	failures := make(chan error, 64)
	var wait sync.WaitGroup
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				lease, err := host.Acquire(context.Background(), CurrentRuntime())
				if err != nil {
					failures <- err
					return
				}
				value := lease.Value()
				runtime.Gosched()
				if value == nil || value.closed.Load() {
					failures <- errors.New("acquired generation was already closed")
					lease.Release()
					return
				}
				lease.Release()
			}
		}()
	}
	for revision := int64(2); revision <= 8; revision++ {
		if err := host.prepare(context.Background(), revision); err != nil {
			t.Fatal(err)
		}
		if err := host.arm(revision); err != nil {
			t.Fatal(err)
		}
		if err := host.activate(revision); err != nil {
			t.Fatal(err)
		}
		if err := host.reopen(revision); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Fatal(failure)
	}
}

func TestRuntimeHostStaticRevisionZeroAndCloseWithOutstandingLease(t *testing.T) {
	t.Parallel()

	factory := newRuntimeHostTestFactory()
	initial := &runtimeHostTestGeneration{revision: 0}
	host, err := NewRuntimeHost(RuntimeHostOptions[*runtimeHostTestGeneration]{
		Initial: InitialRuntime[*runtimeHostTestGeneration]{
			Binding: RuntimeBinding{Mode: RuntimeModeStatic, Role: RuntimeRoleAPI},
			Value:   initial,
		},
		Factory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := host.Acquire(context.Background(), RevisionRuntime(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Acquire(context.Background(), RevisionRuntime(1))
	assertRuntimeHostCode(t, err, ErrorCodeRuntimeRevisionUnavailable)
	staticAttempt, err := host.Acquire(context.Background(), AttemptRuntime(RuntimeBinding{Mode: RuntimeModeStatic, Role: RuntimeRoleAPI}))
	if err != nil {
		t.Fatal(err)
	}
	if staticAttempt.Binding().Mode != RuntimeModeStatic || staticAttempt.Binding().Revision != 0 || staticAttempt.Value() != initial {
		t.Fatalf("static attempt lease = %v %p", staticAttempt.Binding(), staticAttempt.Value())
	}
	staticAttempt.Release()
	host.close()
	if initial.closed.Load() {
		t.Fatal("static generation closed before outstanding lease release")
	}
	_, err = host.Acquire(context.Background(), CurrentRuntime())
	assertRuntimeHostCode(t, err, ErrorCodeRuntimeNotReady)
	lease.Release()
	if factory.closeCount(initial) != 1 {
		t.Fatalf("static generation close count = %d", factory.closeCount(initial))
	}
}

func newManagedRuntimeHost(
	t *testing.T,
	factory *runtimeHostTestFactory,
	initial *runtimeHostTestGeneration,
	compatible func(*runtimeHostTestGeneration, retrievaldomain.EmbeddingVersion) error,
) *RuntimeHost[*runtimeHostTestGeneration] {
	t.Helper()
	return newManagedRuntimeHostWithOptions(t, factory, initial, compatible, RuntimeHostOptions[*runtimeHostTestGeneration]{})
}

func newManagedRuntimeHostWithOptions(
	t *testing.T,
	factory *runtimeHostTestFactory,
	initial *runtimeHostTestGeneration,
	compatible func(*runtimeHostTestGeneration, retrievaldomain.EmbeddingVersion) error,
	overrides RuntimeHostOptions[*runtimeHostTestGeneration],
) *RuntimeHost[*runtimeHostTestGeneration] {
	t.Helper()
	overrides.Initial = InitialRuntime[*runtimeHostTestGeneration]{Binding: managedRuntimeBinding(initial.revision), Value: initial}
	overrides.Factory = factory
	overrides.EmbeddingCompatible = compatible
	host, err := NewRuntimeHost(overrides)
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func managedRuntimeBinding(revision int64) RuntimeBinding {
	return RuntimeBinding{
		Mode: RuntimeModeManaged, Role: RuntimeRoleWorker, Revision: revision,
		InstanceID: foundation.ID("20000000-0000-4000-8000-000000000001"),
	}
}

func assertNoRuntimeHostResult(
	t *testing.T,
	result <-chan RuntimeLease[*runtimeHostTestGeneration],
	failure <-chan error,
) {
	t.Helper()
	select {
	case lease := <-result:
		lease.Release()
		t.Fatal("acquisition passed a closed gate")
	case err := <-failure:
		t.Fatalf("gated acquisition failed early: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
}

func assertRuntimeHostCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func findClosedRuntimeHostGeneration(t *testing.T, factory *runtimeHostTestFactory, revision int64) *runtimeHostTestGeneration {
	t.Helper()
	factory.mu.Lock()
	defer factory.mu.Unlock()
	for generation, closes := range factory.closeCalls {
		if generation.revision == revision && closes > 0 {
			return generation
		}
	}
	t.Fatalf("no closed generation for revision %d", revision)
	return nil
}

func runtimeHostEmbeddingContract(t *testing.T, model string, dimensions int32) retrievaldomain.EmbeddingContract {
	t.Helper()
	contract := retrievaldomain.EmbeddingContract{
		Provider: "openai-compatible", AdapterName: "direct-http", AdapterVersion: "v1",
		Model: model, Dimensions: dimensions, Normalization: retrievaldomain.NormalizationL2,
		DistanceMetric: retrievaldomain.DistanceCosine, EndpointIdentity: "https://models.example.test/v1",
		MaxBatchSize: 32, MaxInputBytes: 4096, MaxBatchInputBytes: 32768,
	}
	hash, err := retrievaldomain.ComputeEmbeddingConfigHash(contract)
	if err != nil {
		t.Fatal(err)
	}
	contract.ConfigHash = hash
	return contract
}

func runtimeHostEmbeddingVersion(contract retrievaldomain.EmbeddingContract, revision int64, id string) retrievaldomain.EmbeddingVersion {
	return retrievaldomain.EmbeddingVersion{
		ID: foundation.ID(id), Provider: contract.Provider, AdapterName: contract.AdapterName,
		AdapterVersion: contract.AdapterVersion, Model: contract.Model, Dimensions: contract.Dimensions,
		Normalization: contract.Normalization, DistanceMetric: contract.DistanceMetric, ConfigHash: contract.ConfigHash,
		ModelSettingsRevision: &revision, CreatedAt: time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC),
	}
}

func TestRuntimeHostSafeFormattingOmitsInstanceAndCanaries(t *testing.T) {
	t.Parallel()

	binding := managedRuntimeBinding(7)
	factory := newRuntimeHostTestFactory()
	factory.buildErrs[9] = errors.New("secret-canary")
	host := newManagedRuntimeHost(t, factory, &runtimeHostTestGeneration{revision: 7, secret: "secret-canary"}, nil)
	lease, err := host.Acquire(context.Background(), CurrentRuntime())
	if err != nil {
		t.Fatal(err)
	}
	_, buildErr := host.Acquire(context.Background(), RevisionRuntime(9))
	if buildErr == nil {
		t.Fatal("historical build error is nil")
	}
	formatted := fmt.Sprintf(
		"%v %#v %v %#v %v %#v %v %#v %v",
		binding,
		binding,
		AttemptRuntime(binding),
		AttemptRuntime(binding),
		host,
		host,
		lease,
		lease,
		buildErr,
	)
	lease.Release()
	host.close()
	for _, forbidden := range []string{string(binding.InstanceID), "secret-canary", "https://models.example.test/private"} {
		if strings.Contains(formatted, forbidden) {
			t.Fatalf("runtime binding formatting leaked %q: %s", forbidden, formatted)
		}
	}
}
