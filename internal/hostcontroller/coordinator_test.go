package hostcontroller

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestResolveRegisteredTargetPersistsMissingRootAvailability(t *testing.T) {
	workspace := newAvailabilityWorkspace(t, "550e8400-e29b-41d4-a716-446655440020")
	if err := os.Remove(workspace.RootPath); err != nil {
		t.Fatalf("remove registered root: %v", err)
	}
	registry := &availabilityRegistryStore{workspace: workspace}
	snapshot := workspacedomain.ControlSnapshot{
		State:    workspacedomain.ControlState{StateVersion: 11},
		Registry: []workspacedomain.Workspace{workspace},
	}
	coordinator := newAvailabilityTestCoordinator(t, registry, snapshot)

	_, err := coordinator.resolveTarget(context.Background(), snapshot, SwitchCommand{TargetKind: "registered", WorkspaceID: string(workspace.ID)})
	fault, ok := AsFault(err)
	if !ok || fault.Code != "WORKSPACE_PATH_NOT_FOUND" {
		t.Fatalf("resolveTarget() error=%v", err)
	}
	requireUnavailableUpdate(t, registry.updates, workspace, "WORKSPACE_PATH_NOT_FOUND")
}

func TestResolveRegisteredTargetAcceptsMatchingCanonicalRoot(t *testing.T) {
	workspace := newAvailabilityWorkspace(t, "550e8400-e29b-41d4-a716-446655440021")
	registry := &availabilityRegistryStore{workspace: workspace}
	snapshot := workspacedomain.ControlSnapshot{
		State:    workspacedomain.ControlState{StateVersion: 11},
		Registry: []workspacedomain.Workspace{workspace},
	}
	coordinator := newAvailabilityTestCoordinator(t, registry, snapshot)

	resolved, err := coordinator.resolveTarget(context.Background(), snapshot, SwitchCommand{TargetKind: "registered", WorkspaceID: string(workspace.ID)})
	if err != nil {
		t.Fatalf("resolveTarget() error=%v", err)
	}
	if resolved.ID != workspace.ID || resolved.Availability != workspacedomain.WorkspaceAvailabilityAvailable {
		t.Fatalf("resolved Workspace=%#v", resolved)
	}
	if len(registry.updates) != 0 {
		t.Fatalf("available Workspace updates=%#v", registry.updates)
	}
	checked, err := coordinator.CheckAvailability(context.Background(), string(workspace.ID), snapshot.State.StateVersion)
	if err != nil {
		t.Fatalf("CheckAvailability() error=%v", err)
	}
	if checked.Availability != AvailabilityAvailable || checked.AvailabilityReason != "" {
		t.Fatalf("checked Workspace=%#v", checked)
	}
	if len(registry.updates) != 1 {
		t.Fatalf("availability updates=%#v", registry.updates)
	}
	update := registry.updates[0]
	if update.WorkspaceID != workspace.ID || update.ExpectedVersion != workspace.Version ||
		update.Availability != workspacedomain.WorkspaceAvailabilityAvailable || update.Reason != "" || update.CheckedAt.IsZero() {
		t.Fatalf("availability update=%#v", update)
	}
}

func TestResolveRegisteredTargetPersistsIdentityMismatch(t *testing.T) {
	workspace := newAvailabilityWorkspace(t, "550e8400-e29b-41d4-a716-446655440022")
	replacementPrefix := "0"
	if workspace.RootFingerprint[0] == '0' {
		replacementPrefix = "1"
	}
	workspace.RootFingerprint = replacementPrefix + workspace.RootFingerprint[1:]
	registry := &availabilityRegistryStore{workspace: workspace}
	snapshot := workspacedomain.ControlSnapshot{
		State:    workspacedomain.ControlState{StateVersion: 11},
		Registry: []workspacedomain.Workspace{workspace},
	}
	coordinator := newAvailabilityTestCoordinator(t, registry, snapshot)

	_, err := coordinator.resolveTarget(context.Background(), snapshot, SwitchCommand{TargetKind: "registered", WorkspaceID: string(workspace.ID)})
	fault, ok := AsFault(err)
	if !ok || fault.Code != "WORKSPACE_PATH_IDENTITY_CHANGED" {
		t.Fatalf("resolveTarget() error=%v", err)
	}
	requireUnavailableUpdate(t, registry.updates, workspace, "WORKSPACE_PATH_IDENTITY_CHANGED")
}

func TestResumedRuntimesRequireProcessHeartbeatAfterControllerTransition(t *testing.T) {
	workspaceID := foundation.ID("550e8400-e29b-41d4-a716-446655440000")
	apiID := foundation.ID("550e8400-e29b-41d4-a716-446655440001")
	workerID := foundation.ID("550e8400-e29b-41d4-a716-446655440002")
	expected := map[workspacedomain.RuntimeRole]runtimeResumeExpectation{
		workspacedomain.RuntimeRoleAPI:    {InstanceID: apiID, Version: 10},
		workspacedomain.RuntimeRoleWorker: {InstanceID: workerID, Version: 20},
	}
	records := []workspacedomain.RuntimeRecord{
		{Role: workspacedomain.RuntimeRoleAPI, InstanceID: apiID, WorkspaceID: workspaceID, GrantGeneration: 4, Phase: workspacedomain.RuntimePhaseActive, Version: 10, Fresh: true},
		{Role: workspacedomain.RuntimeRoleWorker, InstanceID: workerID, WorkspaceID: workspaceID, GrantGeneration: 4, Phase: workspacedomain.RuntimePhaseActive, Version: 20, Fresh: true},
	}
	if resumedRuntimesReady(records, workspaceID, 4, expected) {
		t.Fatal("Controller-authored active transitions were treated as process acknowledgements")
	}
	records[0].Version++
	records[1].Version++
	if !resumedRuntimesReady(records, workspaceID, 4, expected) {
		t.Fatal("fresh API and Worker heartbeats were not accepted")
	}
}

func TestReconcileRevokesStrayRuntimeWhenNoActiveGrant(t *testing.T) {
	control := &recoveryControlStore{snapshot: workspacedomain.ControlSnapshot{
		State: workspacedomain.ControlState{StateVersion: 7},
	}}
	runtime := &recoveryRuntime{}
	coordinator := newRecoveryTestCoordinator(t, control, runtime)

	if err := coordinator.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error=%v", err)
	}
	if runtime.revokeCalls != 1 || runtime.applyCalls != 0 || runtime.prepareCalls != 0 {
		t.Fatalf("calls revoke=%d apply=%d prepare=%d", runtime.revokeCalls, runtime.applyCalls, runtime.prepareCalls)
	}
}

func TestRecoveryDoesNotPrepareOrApplyAfterRuntimeRevocationFails(t *testing.T) {
	operationID := foundation.ID("550e8400-e29b-41d4-a716-446655440010")
	control := &recoveryControlStore{operation: workspacedomain.SwitchOperation{
		ID: operationID, Phase: workspacedomain.SwitchPhaseApplyingGrant, Version: 4,
	}}
	runtime := &recoveryRuntime{revokeErr: &Fault{Code: "WORKSPACE_RUNTIME_REVOKE_FAILED"}}
	coordinator := newRecoveryTestCoordinator(t, control, runtime)

	err := coordinator.recoverSwitch(context.Background(), operationID, errors.New("target failed"))
	if fault, ok := AsFault(err); !ok || fault.Code != "WORKSPACE_RUNTIME_REVOKE_FAILED" {
		t.Fatalf("recoverSwitch() error=%v", err)
	}
	if runtime.revokeCalls != 1 || runtime.prepareCalls != 0 || runtime.applyCalls != 0 || control.advanceCalls != 0 {
		t.Fatalf("calls revoke=%d prepare=%d apply=%d advance=%d", runtime.revokeCalls, runtime.prepareCalls, runtime.applyCalls, control.advanceCalls)
	}
}

func TestRecoveryDoesNotTouchRuntimeAfterDatabaseRevocationFails(t *testing.T) {
	operationID := foundation.ID("550e8400-e29b-41d4-a716-446655440011")
	workspaceID := foundation.ID("550e8400-e29b-41d4-a716-446655440012")
	control := &recoveryControlStore{
		operation: workspacedomain.SwitchOperation{
			ID: operationID, Phase: workspacedomain.SwitchPhaseRevoking, Version: 4,
		},
		snapshot: workspacedomain.ControlSnapshot{State: workspacedomain.ControlState{
			StateVersion: 7, ActiveWorkspaceID: &workspaceID,
		}},
		revokeActiveErr: errors.New("fixture database revoke failure"),
	}
	runtime := &recoveryRuntime{}
	coordinator := newRecoveryTestCoordinator(t, control, runtime)

	if err := coordinator.recoverSwitch(context.Background(), operationID, errors.New("target failed")); err == nil {
		t.Fatal("recoverSwitch() succeeded after database revocation failed")
	}
	if runtime.revokeCalls != 0 || runtime.prepareCalls != 0 || runtime.applyCalls != 0 || control.advanceCalls != 0 {
		t.Fatalf("calls revoke=%d prepare=%d apply=%d advance=%d", runtime.revokeCalls, runtime.prepareCalls, runtime.applyCalls, control.advanceCalls)
	}
}

func TestRecoveryKeepsOperationOpenWhileRuntimeRevocationIsUnconfirmed(t *testing.T) {
	operationID := foundation.ID("550e8400-e29b-41d4-a716-446655440014")
	control := newRetryRecoveryControlStore(operationID, workspacedomain.SwitchPhaseApplyingGrant, 4)
	runtime := &retryRecoveryRuntime{
		revokeErr:   &Fault{Code: "WORKSPACE_RUNTIME_REVOKE_FAILED"},
		revokeCalls: make(chan struct{}, 8),
	}
	coordinator := newRetryRecoveryTestCoordinator(t, control, runtime)
	coordinator.recoveryInitialBackoff = time.Millisecond
	coordinator.recoveryMaximumBackoff = 2 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- coordinator.recoverSwitchUntilTerminal(ctx, operationID, errors.New("target failed"))
	}()
	for attempt := 0; attempt < 4; attempt++ {
		select {
		case <-runtime.revokeCalls:
		case <-time.After(time.Second):
			t.Fatal("recovery did not continue revocation retries")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("recoverSwitchUntilTerminal() error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery did not stop after cancellation")
	}
	operation, finishCalls, advanceCalls, _ := control.result()
	if operation.Result != "" || finishCalls != 0 || advanceCalls != 0 {
		t.Fatalf("unsafe recovery terminalization: operation=%#v finish=%d advance=%d", operation, finishCalls, advanceCalls)
	}
}

func TestRecoveryConvergesAfterTransientDatabaseFailuresWithoutRestart(t *testing.T) {
	operationID := foundation.ID("550e8400-e29b-41d4-a716-446655440015")
	control := newRetryRecoveryControlStore(operationID, workspacedomain.SwitchPhaseApplyingGrant, 4)
	control.renewFailures = 3
	runtime := &retryRecoveryRuntime{}
	coordinator := newRetryRecoveryTestCoordinator(t, control, runtime)
	coordinator.recoveryInitialBackoff = time.Millisecond
	coordinator.recoveryMaximumBackoff = 2 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.recoverSwitchUntilTerminal(ctx, operationID, errors.New("target failed")); err != nil {
		t.Fatalf("recoverSwitchUntilTerminal() error=%v", err)
	}
	operation, finishCalls, advanceCalls, renewCalls := control.result()
	if operation.Result != workspacedomain.SwitchResultFailed || finishCalls != 1 || advanceCalls != 2 || renewCalls < 4 {
		t.Fatalf("recovery did not converge: operation=%#v finish=%d advance=%d renew=%d", operation, finishCalls, advanceCalls, renewCalls)
	}
	if runtime.revokeCount() != 1 {
		t.Fatalf("runtime revoke calls=%d", runtime.revokeCount())
	}
}

func TestRecoveryFailureFinishesOnlyAfterCleanupRevocationSucceeds(t *testing.T) {
	operationID := foundation.ID("550e8400-e29b-41d4-a716-446655440029")
	control := newRetryRecoveryControlStore(operationID, workspacedomain.SwitchPhaseRecovering, 4)
	runtime := &retryRecoveryRuntime{revokeErr: &Fault{Code: "WORKSPACE_RUNTIME_REVOKE_FAILED"}}
	coordinator := newRetryRecoveryTestCoordinator(t, control, runtime)

	err := coordinator.finishFailedAfterRevocation(context.Background(), operationID, "WORKSPACE_SWITCH_FAILED")
	if fault, ok := AsFault(err); !ok || fault.Code != "WORKSPACE_RUNTIME_REVOKE_FAILED" {
		t.Fatalf("finishFailedAfterRevocation() error=%v", err)
	}
	operation, finishCalls, _, _ := control.result()
	if operation.Result != "" || finishCalls != 0 {
		t.Fatalf("cleanup failure terminalized operation: operation=%#v finish=%d", operation, finishCalls)
	}

	runtime.mu.Lock()
	runtime.revokeErr = nil
	runtime.mu.Unlock()
	if err := coordinator.finishFailedAfterRevocation(context.Background(), operationID, "WORKSPACE_SWITCH_FAILED"); err != nil {
		t.Fatalf("finishFailedAfterRevocation() retry error=%v", err)
	}
	operation, finishCalls, _, _ = control.result()
	if operation.Result != workspacedomain.SwitchResultFailed || finishCalls != 1 {
		t.Fatalf("successful cleanup did not terminalize operation: operation=%#v finish=%d", operation, finishCalls)
	}
}

func TestBeginSwitchDoesNotStartUntrackedWorkAfterClose(t *testing.T) {
	workspace := newAvailabilityWorkspace(t, "550e8400-e29b-41d4-a716-446655440024")
	control := &closingBeginControlStore{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		snapshot: workspacedomain.ControlSnapshot{
			State:    workspacedomain.ControlState{StateVersion: 11},
			Registry: []workspacedomain.Workspace{workspace},
		},
	}
	service, err := workspaceapplication.NewControlService(
		&recoveryRegistryStore{}, control, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		Service: service, Runtime: &recoveryRuntime{}, Validator: PathValidator{},
		OwnerID: foundation.ID("550e8400-e29b-41d4-a716-446655440025"),
	})
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, switchErr := coordinator.BeginSwitch(context.Background(), SwitchCommand{
			ExpectedStateVersion: 11,
			ControllerInstanceID: "550e8400-e29b-41d4-a716-446655440026",
			IdempotencyKey:       "close-race-operation",
			RequestHash:          strings.Repeat("a", 64),
			TargetKind:           "registered",
			WorkspaceID:          string(workspace.ID),
		})
		result <- switchErr
	}()
	select {
	case <-control.entered:
	case <-time.After(time.Second):
		t.Fatal("BeginSwitch did not reach durable creation")
	}
	coordinator.Close()
	close(control.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("BeginSwitch() error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BeginSwitch did not return after durable creation")
	}
	time.Sleep(10 * time.Millisecond)
	if control.getCallCount() != 0 {
		t.Fatalf("closed coordinator started switch work: GetSwitchOperation calls=%d", control.getCallCount())
	}
}

func TestRuntimeMutationGateSerializesAndHonorsCancellation(t *testing.T) {
	coordinator, err := NewCoordinator(CoordinatorOptions{
		Service: &workspaceapplication.ControlService{}, Runtime: &recoveryRuntime{},
		OwnerID: foundation.ID("550e8400-e29b-41d4-a716-446655440027"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	if err := coordinator.lockRuntime(context.Background()); err != nil {
		t.Fatalf("lockRuntime() error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- coordinator.lockRuntime(ctx) }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("contending lockRuntime() error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("contending runtime mutation did not honor cancellation")
	}
	coordinator.unlockRuntime()
}

type recoveryRegistryStore struct {
	workspaceapplication.RegistryStore
}

type availabilityRegistryStore struct {
	workspaceapplication.RegistryStore
	workspace workspacedomain.Workspace
	updates   []workspacedomain.AvailabilityUpdate
}

func (store *availabilityRegistryStore) SetWorkspaceAvailability(_ context.Context, update workspacedomain.AvailabilityUpdate) (workspacedomain.Workspace, error) {
	store.updates = append(store.updates, update)
	store.workspace.Availability = update.Availability
	store.workspace.AvailabilityReason = update.Reason
	store.workspace.AvailabilityCheckedAt = update.CheckedAt
	store.workspace.Version++
	return store.workspace, nil
}

type recoveryControlStore struct {
	workspaceapplication.ControlStore
	operation       workspacedomain.SwitchOperation
	snapshot        workspacedomain.ControlSnapshot
	revokeActiveErr error
	advanceCalls    int
}

func (store *recoveryControlStore) GetSwitchOperation(context.Context, foundation.ID) (workspacedomain.SwitchOperation, error) {
	return store.operation, nil
}

func (store *recoveryControlStore) ControlSnapshot(context.Context, time.Duration) (workspacedomain.ControlSnapshot, error) {
	return store.snapshot, nil
}

func (store *recoveryControlStore) RevokeActiveWorkspace(context.Context, workspaceapplication.RevokeActiveCommand) (workspacedomain.ControlState, error) {
	return workspacedomain.ControlState{}, store.revokeActiveErr
}

func (store *recoveryControlStore) AdvanceSwitch(_ context.Context, command workspaceapplication.AdvanceSwitchCommand) (workspacedomain.SwitchOperation, error) {
	store.advanceCalls++
	store.operation.Phase = command.NextPhase
	store.operation.Version++
	return store.operation, nil
}

type recoveryRuntime struct {
	prepareCalls int
	applyCalls   int
	revokeCalls  int
	revokeErr    error
}

func (runtime *recoveryRuntime) PrepareGrant(context.Context, foundation.ID, Grant, bool) error {
	runtime.prepareCalls++
	return nil
}

func (runtime *recoveryRuntime) ApplyGrant(context.Context, Grant) (*url.URL, error) {
	runtime.applyCalls++
	return nil, nil
}

func (runtime *recoveryRuntime) RevokeGrant(context.Context) error {
	runtime.revokeCalls++
	return runtime.revokeErr
}

func (*recoveryRuntime) CurrentBackend(context.Context) (*url.URL, error) { return nil, nil }

type retryRecoveryControlStore struct {
	workspaceapplication.ControlStore

	mu            sync.Mutex
	operation     workspacedomain.SwitchOperation
	snapshot      workspacedomain.ControlSnapshot
	renewFailures int
	renewCalls    int
	advanceCalls  int
	finishCalls   int
}

type closingBeginControlStore struct {
	workspaceapplication.ControlStore
	mu       sync.Mutex
	snapshot workspacedomain.ControlSnapshot
	entered  chan struct{}
	release  chan struct{}
	getCalls int
}

func (store *closingBeginControlStore) ControlSnapshot(context.Context, time.Duration) (workspacedomain.ControlSnapshot, error) {
	return store.snapshot, nil
}

func (store *closingBeginControlStore) BeginSwitch(_ context.Context, command workspaceapplication.BeginSwitchStoreCommand) (workspacedomain.SwitchOperation, error) {
	close(store.entered)
	<-store.release
	now := time.Now().UTC()
	return workspacedomain.SwitchOperation{
		ID: command.OperationID, ControllerInstanceID: command.ControllerInstanceID,
		IdempotencyKey: command.IdempotencyKey, RequestHash: command.RequestHash,
		TargetWorkspaceID: command.TargetWorkspaceID, Phase: workspacedomain.SwitchPhaseValidating,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (store *closingBeginControlStore) GetSwitchOperation(context.Context, foundation.ID) (workspacedomain.SwitchOperation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.getCalls++
	return workspacedomain.SwitchOperation{}, errors.New("unexpected switch execution")
}

func (store *closingBeginControlStore) getCallCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.getCalls
}

func newRetryRecoveryControlStore(operationID foundation.ID, phase workspacedomain.SwitchPhase, version int64) *retryRecoveryControlStore {
	return &retryRecoveryControlStore{
		operation: workspacedomain.SwitchOperation{ID: operationID, Phase: phase, Version: version},
		snapshot:  workspacedomain.ControlSnapshot{State: workspacedomain.ControlState{StateVersion: 7}},
	}
}

func (store *retryRecoveryControlStore) GetSwitchOperation(context.Context, foundation.ID) (workspacedomain.SwitchOperation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.operation, nil
}

func (store *retryRecoveryControlStore) ControlSnapshot(context.Context, time.Duration) (workspacedomain.ControlSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	snapshot := store.snapshot
	operation := store.operation
	snapshot.Operation = &operation
	return snapshot, nil
}

func (store *retryRecoveryControlStore) RenewSwitch(_ context.Context, command workspaceapplication.RenewSwitchCommand) (workspacedomain.SwitchOperation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.renewCalls++
	if store.renewFailures > 0 {
		store.renewFailures--
		return workspacedomain.SwitchOperation{}, foundation.NewError(
			foundation.ErrorDependencyUnavailable, workspacedomain.ErrorCodeControlDatabaseUnavailable, true,
			errors.New("fixture database unavailable"),
		)
	}
	store.operation.Version++
	store.operation.LeaseOwnerID = &command.LeaseOwnerID
	store.operation.LeaseExpiresAt = time.Now().Add(command.LeaseDuration)
	store.snapshot.State.StateVersion++
	return store.operation, nil
}

func (store *retryRecoveryControlStore) AdvanceSwitch(_ context.Context, command workspaceapplication.AdvanceSwitchCommand) (workspacedomain.SwitchOperation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.advanceCalls++
	store.operation.Phase = command.NextPhase
	store.operation.Version++
	store.snapshot.State.StateVersion++
	return store.operation, nil
}

func (store *retryRecoveryControlStore) FinishSwitch(_ context.Context, command workspaceapplication.FinishSwitchCommand) (workspacedomain.SwitchOperation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.finishCalls++
	store.operation.Result = command.Result
	store.operation.ErrorCode = command.ErrorCode
	store.operation.Version++
	store.snapshot.State.StateVersion++
	return store.operation, nil
}

func (store *retryRecoveryControlStore) result() (workspacedomain.SwitchOperation, int, int, int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.operation, store.finishCalls, store.advanceCalls, store.renewCalls
}

type retryRecoveryRuntime struct {
	mu          sync.Mutex
	revokeErr   error
	revokeCalls chan struct{}
	revokes     int
}

func (*retryRecoveryRuntime) PrepareGrant(context.Context, foundation.ID, Grant, bool) error {
	return nil
}

func (*retryRecoveryRuntime) ApplyGrant(context.Context, Grant) (*url.URL, error) { return nil, nil }

func (runtime *retryRecoveryRuntime) RevokeGrant(context.Context) error {
	runtime.mu.Lock()
	runtime.revokes++
	err := runtime.revokeErr
	calls := runtime.revokeCalls
	runtime.mu.Unlock()
	if calls != nil {
		calls <- struct{}{}
	}
	return err
}

func (*retryRecoveryRuntime) CurrentBackend(context.Context) (*url.URL, error) { return nil, nil }

func (runtime *retryRecoveryRuntime) revokeCount() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.revokes
}

func newRecoveryTestCoordinator(t *testing.T, control *recoveryControlStore, runtime *recoveryRuntime) *Coordinator {
	t.Helper()
	service, err := workspaceapplication.NewControlService(
		&recoveryRegistryStore{}, control, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		Service: service, Runtime: runtime, Validator: PathValidator{},
		OwnerID: foundation.ID("550e8400-e29b-41d4-a716-446655440013"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func newRetryRecoveryTestCoordinator(t *testing.T, control *retryRecoveryControlStore, runtime *retryRecoveryRuntime) *Coordinator {
	t.Helper()
	service, err := workspaceapplication.NewControlService(
		&recoveryRegistryStore{}, control, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		Service: service, Runtime: runtime, Validator: PathValidator{},
		OwnerID: foundation.ID("550e8400-e29b-41d4-a716-446655440016"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(coordinator.Close)
	return coordinator
}

func newAvailabilityWorkspace(t *testing.T, id foundation.ID) workspacedomain.Workspace {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create registered root: %v", err)
	}
	validated, err := (PathValidator{}).Validate(root)
	if err != nil {
		t.Fatalf("validate registered root: %v", err)
	}
	return workspacedomain.Workspace{
		ID: id, Name: "Workspace", RootPath: validated.CanonicalPath,
		RootFingerprint: validated.Fingerprint.Digest(), BindingVersion: validated.Fingerprint.BindingVersion,
		Status: workspacedomain.WorkspaceStatusInactive, Availability: workspacedomain.WorkspaceAvailabilityAvailable,
		Version: 7,
	}
}

func newAvailabilityTestCoordinator(t *testing.T, registry *availabilityRegistryStore, snapshot workspacedomain.ControlSnapshot) *Coordinator {
	t.Helper()
	service, err := workspaceapplication.NewControlService(
		registry, &recoveryControlStore{snapshot: snapshot}, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		Service: service, Runtime: &recoveryRuntime{}, Validator: PathValidator{},
		OwnerID: foundation.ID("550e8400-e29b-41d4-a716-446655440023"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func requireUnavailableUpdate(t *testing.T, updates []workspacedomain.AvailabilityUpdate, workspace workspacedomain.Workspace, reason string) {
	t.Helper()
	if len(updates) != 1 {
		t.Fatalf("availability updates=%#v", updates)
	}
	update := updates[0]
	if update.WorkspaceID != workspace.ID || update.ExpectedVersion != workspace.Version ||
		update.Availability != workspacedomain.WorkspaceAvailabilityUnavailable || update.Reason != reason || update.CheckedAt.IsZero() {
		t.Fatalf("availability update=%#v", update)
	}
}
