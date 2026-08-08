package workspacecontrol

import (
	"context"
	"errors"
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

func TestWorkspaceAvailabilityReportsMissingRoot(t *testing.T) {
	workspace := newAvailabilityWorkspace(t, "550e8400-e29b-41d4-a716-446655440020")
	if err := os.Remove(workspace.RootPath); err != nil {
		t.Fatalf("remove registered root: %v", err)
	}
	coordinator := &Coordinator{validator: PathValidator{}}
	availability, reason := coordinator.workspaceAvailability(workspace)
	if availability != workspacedomain.WorkspaceAvailabilityUnavailable || reason != "WORKSPACE_PATH_NOT_FOUND" {
		t.Fatalf("workspaceAvailability() availability=%q reason=%q", availability, reason)
	}
}

func TestWorkspaceAvailabilityAcceptsMatchingCanonicalRoot(t *testing.T) {
	workspace := newAvailabilityWorkspace(t, "550e8400-e29b-41d4-a716-446655440021")
	coordinator := &Coordinator{validator: PathValidator{}}
	availability, reason := coordinator.workspaceAvailability(workspace)
	if availability != workspacedomain.WorkspaceAvailabilityAvailable || reason != "" {
		t.Fatalf("workspaceAvailability() availability=%q reason=%q", availability, reason)
	}
}

func TestWorkspaceAvailabilityReportsIdentityMismatch(t *testing.T) {
	workspace := newAvailabilityWorkspace(t, "550e8400-e29b-41d4-a716-446655440022")
	replacementPrefix := "0"
	if workspace.RootFingerprint[0] == '0' {
		replacementPrefix = "1"
	}
	workspace.RootFingerprint = replacementPrefix + workspace.RootFingerprint[1:]
	coordinator := &Coordinator{validator: PathValidator{}}
	availability, reason := coordinator.workspaceAvailability(workspace)
	if availability != workspacedomain.WorkspaceAvailabilityUnavailable || reason != "WORKSPACE_PATH_IDENTITY_CHANGED" {
		t.Fatalf("workspaceAvailability() availability=%q reason=%q", availability, reason)
	}
}

func TestSwitchRejectsInvalidRootBeforeRuntimeMutation(t *testing.T) {
	runtime := &recoveryRuntime{}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		Service: &workspaceapplication.ControlService{}, Runtime: runtime, Validator: PathValidator{},
		ControlInstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440023"),
		LeaseOwnerID:      foundation.ID("550e8400-e29b-41d4-a716-446655440024"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.Switch(context.Background(), SwitchCommand{
		RootPath: "relative", IdempotencyKey: "invalid-root",
	})
	fault, ok := AsFault(err)
	if !ok || fault.Code != "WORKSPACE_PATH_NOT_ABSOLUTE" {
		t.Fatalf("Switch() error=%v", err)
	}
	if runtime.prepareCalls != 0 || runtime.applyCalls != 0 || runtime.revokeCalls != 0 {
		t.Fatalf("invalid target mutated runtime: %#v", runtime)
	}
}

func TestSwitchRequestHashBindsNameAndGitIntent(t *testing.T) {
	workspace := newAvailabilityWorkspace(t, "550e8400-e29b-41d4-a716-446655440024")
	first, err := switchRequestHash(workspace, workspace.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := switchRequestHash(workspace, workspace.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("switchRequestHash() first=%q second=%q", first, second)
	}
	renamed, err := switchRequestHash(workspace, "Renamed", false)
	if err != nil {
		t.Fatal(err)
	}
	gitIntent, err := switchRequestHash(workspace, "Renamed", true)
	if err != nil {
		t.Fatal(err)
	}
	if renamed == first || gitIntent == renamed {
		t.Fatalf("request hash did not bind command identity: first=%q renamed=%q git=%q", first, renamed, gitIntent)
	}
}

func TestValidateSwitchCommandRejectsUnsafeName(t *testing.T) {
	for _, name := range []string{"unsafe\nname", string([]byte{0xff})} {
		err := validateSwitchCommand(SwitchCommand{Name: name, IdempotencyKey: "safe-key"})
		fault, ok := AsFault(err)
		if !ok || fault.Code != "WORKSPACE_NAME_INVALID" {
			t.Fatalf("validateSwitchCommand(%q) error=%v", name, err)
		}
	}
}

func TestSwitchCompletesFirstActivationSynchronously(t *testing.T) {
	store := &successfulSwitchStore{state: workspacedomain.ControlState{StateVersion: 1}}
	service, err := workspaceapplication.NewControlService(
		store, store, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &successfulSwitchRuntime{store: store}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		Service: service, Runtime: runtime, Validator: PathValidator{},
		ControlInstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440040"),
		LeaseOwnerID:      foundation.ID("550e8400-e29b-41d4-a716-446655440043"),
	})
	if err != nil {
		t.Fatal(err)
	}
	root := canonicalTestDirectory(t)

	outcome, err := coordinator.Switch(context.Background(), SwitchCommand{
		Name: "Knowledge", RootPath: root, IdempotencyKey: "first-activation",
	})
	if err != nil {
		t.Fatalf("Switch() error=%v", err)
	}
	if !outcome.Changed || outcome.Operation.Result != workspacedomain.SwitchResultSucceeded || outcome.GrantGeneration != 1 ||
		outcome.Operation.Phase != workspacedomain.SwitchPhaseActivating || outcome.Operation.GrantGeneration != 1 {
		t.Fatalf("Switch() outcome=%#v", outcome)
	}
	if store.active == nil || store.active.ID != outcome.Workspace.ID || store.state.ActiveWorkspaceID == nil ||
		*store.state.ActiveWorkspaceID != outcome.Workspace.ID || store.state.GrantGeneration != 1 {
		t.Fatalf("active state=%#v workspace=%#v", store.state, store.active)
	}
	if runtime.prepareCalls != 1 || runtime.applyCalls != 1 || runtime.revokeCalls != 1 {
		t.Fatalf("runtime calls prepare=%d apply=%d revoke=%d", runtime.prepareCalls, runtime.applyCalls, runtime.revokeCalls)
	}
	if store.operation.ControllerInstanceID != foundation.ID("550e8400-e29b-41d4-a716-446655440040") ||
		store.operation.LeaseOwnerID == nil || *store.operation.LeaseOwnerID != foundation.ID("550e8400-e29b-41d4-a716-446655440043") {
		t.Fatalf("operation identities=%#v", store.operation)
	}
}

func TestSwitchDoesNotReapplyPreviousWorkspaceBeforeTarget(t *testing.T) {
	rootA := canonicalTestDirectory(t)
	rootB := canonicalTestDirectory(t)
	store := newActiveSuccessfulSwitchStore(t, rootA, 4)
	service, err := workspaceapplication.NewControlService(
		store, store, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &successfulSwitchRuntime{store: store}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		Service: service, Runtime: runtime, Validator: PathValidator{},
		ControlInstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440044"),
		LeaseOwnerID:      foundation.ID("550e8400-e29b-41d4-a716-446655440045"),
	})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := coordinator.Switch(context.Background(), SwitchCommand{
		Name: "Workspace B", RootPath: rootB, IdempotencyKey: "switch-a-to-b",
	})
	if err != nil {
		t.Fatalf("Switch() error=%v", err)
	}
	if !outcome.Changed || outcome.Workspace.RootPath != rootB || outcome.Operation.Result != workspacedomain.SwitchResultSucceeded {
		t.Fatalf("Switch() outcome=%#v", outcome)
	}
	if len(runtime.appliedGrants) != 1 || runtime.appliedGrants[0].Root != rootB ||
		runtime.appliedGrants[0].WorkspaceID != string(outcome.Workspace.ID) {
		t.Fatalf("applied grants=%#v", runtime.appliedGrants)
	}
}

func TestSwitchAllowsValidTargetWhenPreviousRootIsMissing(t *testing.T) {
	rootA := canonicalTestDirectory(t)
	rootB := canonicalTestDirectory(t)
	store := newActiveSuccessfulSwitchStore(t, rootA, 2)
	if err := os.Remove(rootA); err != nil {
		t.Fatalf("remove previous root: %v", err)
	}
	service, err := workspaceapplication.NewControlService(
		store, store, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &successfulSwitchRuntime{store: store}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		Service: service, Runtime: runtime, Validator: PathValidator{},
		ControlInstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440046"),
		LeaseOwnerID:      foundation.ID("550e8400-e29b-41d4-a716-446655440047"),
	})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := coordinator.Switch(context.Background(), SwitchCommand{
		Name: "Workspace B", RootPath: rootB, IdempotencyKey: "switch-away-from-missing-a",
	})
	if err != nil {
		t.Fatalf("Switch() error=%v", err)
	}
	if !outcome.Changed || store.active == nil || store.active.RootPath != rootB {
		t.Fatalf("Switch() outcome=%#v active=%#v", outcome, store.active)
	}
}

func TestSwitchSameWorkspaceReappliesCurrentGrant(t *testing.T) {
	root := canonicalTestDirectory(t)
	store := newActiveSuccessfulSwitchStore(t, root, 6)
	service, err := workspaceapplication.NewControlService(
		store, store, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &successfulSwitchRuntime{store: store}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		Service: service, Runtime: runtime, Validator: PathValidator{},
		ControlInstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440048"),
		LeaseOwnerID:      foundation.ID("550e8400-e29b-41d4-a716-446655440049"),
	})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := coordinator.Switch(context.Background(), SwitchCommand{
		Name: "Workspace A", RootPath: root, IdempotencyKey: "reapply-a",
	})
	if err != nil {
		t.Fatalf("Switch() error=%v", err)
	}
	if outcome.Changed || outcome.GrantGeneration != 6 || store.operation.ID != "" {
		t.Fatalf("Switch() outcome=%#v operation=%#v", outcome, store.operation)
	}
	if runtime.applyCalls != 1 || runtime.prepareCalls != 0 || runtime.revokeCalls != 0 ||
		len(runtime.appliedGrants) != 1 || runtime.appliedGrants[0].Root != root {
		t.Fatalf("runtime=%#v grants=%#v", runtime, runtime.appliedGrants)
	}
}

func TestResumedRuntimesRequireProcessHeartbeatAfterControlTransition(t *testing.T) {
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
		t.Fatal("control-authored active transitions were treated as process acknowledgements")
	}
	records[0].Version++
	records[1].Version++
	if !resumedRuntimesReady(records, workspaceID, 4, expected) {
		t.Fatal("fresh API and Worker heartbeats were not accepted")
	}
}

func TestCancelQuiescenceRestoresUnavailablePreviousRuntime(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		phase workspacedomain.RuntimePhase
	}{
		{name: "unavailable", phase: workspacedomain.RuntimePhaseUnavailable},
		{name: "stale active", phase: workspacedomain.RuntimePhaseActive},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			operationID := foundation.ID("550e8400-e29b-41d4-a716-446655440030")
			previous := newAvailabilityWorkspace(t, "550e8400-e29b-41d4-a716-446655440031")
			previous.Status = workspacedomain.WorkspaceStatusActive
			operation := workspacedomain.SwitchOperation{
				ID: operationID, PreviousWorkspaceID: &previous.ID,
				Phase: workspacedomain.SwitchPhaseQuiescing, GrantGeneration: 4, Version: 7,
			}
			control := &recoveryControlStore{
				operation: operation,
				snapshot: workspacedomain.ControlSnapshot{
					State: workspacedomain.ControlState{
						ActiveWorkspaceID: &previous.ID, GrantGeneration: 3, StateVersion: 11,
					},
					Active: &previous, Registry: []workspacedomain.Workspace{previous}, Operation: &operation,
					Runtimes: []workspacedomain.RuntimeRecord{
						{Role: workspacedomain.RuntimeRoleAPI, WorkspaceID: previous.ID, GrantGeneration: 3, Phase: testCase.phase},
						{Role: workspacedomain.RuntimeRoleWorker, WorkspaceID: previous.ID, GrantGeneration: 3, Phase: testCase.phase},
					},
				},
			}
			runtime := &recoveryRuntime{}
			runtime.applyHook = func(grant Grant) error {
				if grant.WorkspaceID != string(previous.ID) || grant.Root != previous.RootPath || grant.Generation != 3 {
					t.Fatalf("restored grant=%#v", grant)
				}
				control.snapshot.Runtimes = []workspacedomain.RuntimeRecord{
					{Role: workspacedomain.RuntimeRoleAPI, InstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440032"), WorkspaceID: previous.ID, GrantGeneration: 3, Phase: workspacedomain.RuntimePhaseActive, Fresh: true},
					{Role: workspacedomain.RuntimeRoleWorker, InstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440033"), WorkspaceID: previous.ID, GrantGeneration: 3, Phase: workspacedomain.RuntimePhaseActive, Fresh: true},
				}
				return nil
			}
			coordinator := newRecoveryTestCoordinator(t, control, runtime)

			if err := coordinator.cancelQuiescence(context.Background(), operation); err != nil {
				t.Fatalf("cancelQuiescence() error=%v", err)
			}
			if runtime.applyCalls != 1 || control.finishCalls != 1 || control.operation.Result != workspacedomain.SwitchResultCancelled {
				t.Fatalf("recovery apply=%d finish=%d operation=%#v", runtime.applyCalls, control.finishCalls, control.operation)
			}
		})
	}
}

func TestCancelQuiescenceFailsClosedWhenPreviousIdentityChanged(t *testing.T) {
	operationID := foundation.ID("550e8400-e29b-41d4-a716-446655440034")
	previous := newAvailabilityWorkspace(t, "550e8400-e29b-41d4-a716-446655440035")
	previous.Status = workspacedomain.WorkspaceStatusActive
	previous.RootFingerprint = strings.Repeat("0", 64)
	operation := workspacedomain.SwitchOperation{
		ID: operationID, PreviousWorkspaceID: &previous.ID,
		TargetWorkspaceID: foundation.ID("550e8400-e29b-41d4-a716-446655440036"),
		Phase:             workspacedomain.SwitchPhaseQuiescing, GrantGeneration: 4, Version: 7,
	}
	control := &recoveryControlStore{
		operation: operation,
		snapshot: workspacedomain.ControlSnapshot{
			State: workspacedomain.ControlState{
				ActiveWorkspaceID: &previous.ID, GrantGeneration: 3, StateVersion: 11,
			},
			Active: &previous, Registry: []workspacedomain.Workspace{previous}, Operation: &operation,
			Runtimes: []workspacedomain.RuntimeRecord{
				{Role: workspacedomain.RuntimeRoleAPI, WorkspaceID: previous.ID, GrantGeneration: 3, Phase: workspacedomain.RuntimePhaseUnavailable},
				{Role: workspacedomain.RuntimeRoleWorker, WorkspaceID: previous.ID, GrantGeneration: 3, Phase: workspacedomain.RuntimePhaseUnavailable},
			},
		},
	}
	registry := &recoveryRegistryStore{workspaces: []workspacedomain.Workspace{previous}}
	runtime := &recoveryRuntime{}
	coordinator := newRecoveryTestCoordinatorWithRegistry(t, registry, control, runtime)

	if err := coordinator.cancelQuiescence(context.Background(), operation); err != nil {
		t.Fatalf("cancelQuiescence() error=%v", err)
	}
	if runtime.applyCalls != 0 || runtime.revokeCalls != 1 || control.revokeActiveCalls != 1 ||
		control.advanceCalls != 3 || control.finishCalls != 1 {
		t.Fatalf(
			"calls apply=%d revoke=%d revoke_active=%d advance=%d finish=%d",
			runtime.applyCalls, runtime.revokeCalls, control.revokeActiveCalls, control.advanceCalls, control.finishCalls,
		)
	}
	if control.snapshot.State.ActiveWorkspaceID != nil || control.snapshot.Active != nil {
		t.Fatalf("active Workspace was not cleared: snapshot=%#v", control.snapshot)
	}
	if control.operation.Result != workspacedomain.SwitchResultFailed || control.operation.ErrorCode != "WORKSPACE_PATH_IDENTITY_CHANGED" {
		t.Fatalf("terminal operation=%#v", control.operation)
	}
	if len(registry.updates) != 1 || registry.updates[0].Availability != workspacedomain.WorkspaceAvailabilityUnavailable ||
		registry.updates[0].Reason != "WORKSPACE_PATH_IDENTITY_CHANGED" {
		t.Fatalf("availability updates=%#v", registry.updates)
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

func TestReconcileFailsClosedWhenActiveRootIdentityChanged(t *testing.T) {
	active := newAvailabilityWorkspace(t, "550e8400-e29b-41d4-a716-446655440037")
	active.Status = workspacedomain.WorkspaceStatusActive
	active.RootFingerprint = strings.Repeat("0", 64)
	control := &recoveryControlStore{snapshot: workspacedomain.ControlSnapshot{
		State: workspacedomain.ControlState{
			ActiveWorkspaceID: &active.ID, GrantGeneration: 3, StateVersion: 7,
		},
		Active: &active, Registry: []workspacedomain.Workspace{active},
	}}
	registry := &recoveryRegistryStore{workspaces: []workspacedomain.Workspace{active}}
	runtime := &recoveryRuntime{}
	coordinator := newRecoveryTestCoordinatorWithRegistry(t, registry, control, runtime)

	err := coordinator.Reconcile(context.Background())
	fault, ok := AsFault(err)
	if !ok || fault.Code != "WORKSPACE_PATH_IDENTITY_CHANGED" {
		t.Fatalf("Reconcile() error=%v", err)
	}
	if runtime.revokeCalls != 1 || runtime.applyCalls != 0 || runtime.prepareCalls != 0 {
		t.Fatalf("calls revoke=%d apply=%d prepare=%d", runtime.revokeCalls, runtime.applyCalls, runtime.prepareCalls)
	}
	if len(registry.updates) != 1 || registry.updates[0].WorkspaceID != active.ID ||
		registry.updates[0].Availability != workspacedomain.WorkspaceAvailabilityUnavailable ||
		registry.updates[0].Reason != "WORKSPACE_PATH_IDENTITY_CHANGED" {
		t.Fatalf("availability updates=%#v", registry.updates)
	}
}

func TestControlFaultPreservesCancellationCause(t *testing.T) {
	err := controlFault(context.Canceled, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("controlFault() error=%v", err)
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

func TestRuntimeMutationGateSerializesAndHonorsCancellation(t *testing.T) {
	coordinator, err := NewCoordinator(CoordinatorOptions{
		Service: &workspaceapplication.ControlService{}, Runtime: &recoveryRuntime{},
		ControlInstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440027"),
		LeaseOwnerID:      foundation.ID("550e8400-e29b-41d4-a716-446655440028"),
	})
	if err != nil {
		t.Fatal(err)
	}
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
	workspaces []workspacedomain.Workspace
	updates    []workspacedomain.AvailabilityUpdate
}

type successfulSwitchStore struct {
	workspaceapplication.RegistryStore
	workspaceapplication.ControlStore

	state     workspacedomain.ControlState
	active    *workspacedomain.Workspace
	target    workspacedomain.Workspace
	operation workspacedomain.SwitchOperation
	runtimes  []workspacedomain.RuntimeRecord
}

func newActiveSuccessfulSwitchStore(t *testing.T, root string, generation int64) *successfulSwitchStore {
	t.Helper()
	validated, err := (PathValidator{}).Validate(root)
	if err != nil {
		t.Fatalf("validate active Workspace: %v", err)
	}
	active := workspacedomain.Workspace{
		ID: foundation.ID("550e8400-e29b-41d4-a716-446655440050"), Name: "Workspace A",
		RootPath: validated.CanonicalPath, RootFingerprint: validated.Fingerprint.Digest(),
		BindingVersion: validated.Fingerprint.BindingVersion, Status: workspacedomain.WorkspaceStatusActive,
		Availability: workspacedomain.WorkspaceAvailabilityAvailable, Version: 1,
	}
	activeID := active.ID
	return &successfulSwitchStore{
		state:  workspacedomain.ControlState{ActiveWorkspaceID: &activeID, GrantGeneration: generation, StateVersion: 1},
		active: &active,
		runtimes: []workspacedomain.RuntimeRecord{
			{Role: workspacedomain.RuntimeRoleAPI, InstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440051"), WorkspaceID: active.ID, GrantGeneration: generation, Phase: workspacedomain.RuntimePhaseActive, Version: 1, Fresh: true},
			{Role: workspacedomain.RuntimeRoleWorker, InstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440052"), WorkspaceID: active.ID, GrantGeneration: generation, Phase: workspacedomain.RuntimePhaseActive, Version: 1, Fresh: true},
		},
	}
}

func (store *successfulSwitchStore) ResolveWorkspace(
	_ context.Context,
	reservation workspacedomain.WorkspaceReservation,
) (workspacedomain.WorkspaceResolution, error) {
	if store.active != nil && store.active.RootPath == reservation.Binding.CanonicalPath &&
		store.active.RootFingerprint == reservation.Binding.Fingerprint &&
		store.active.BindingVersion == reservation.Binding.BindingVersion {
		store.target = *store.active
		return workspacedomain.WorkspaceResolution{Workspace: store.target}, nil
	}
	store.target = workspacedomain.Workspace{
		ID: reservation.ID, Name: reservation.Name, RootPath: reservation.Binding.CanonicalPath,
		RootFingerprint: reservation.Binding.Fingerprint, BindingVersion: reservation.Binding.BindingVersion,
		Status: workspacedomain.WorkspaceStatusInactive, Availability: workspacedomain.WorkspaceAvailabilityAvailable,
		Version: 1,
	}
	return workspacedomain.WorkspaceResolution{Workspace: store.target}, nil
}

func (store *successfulSwitchStore) ControlSnapshot(context.Context, time.Duration) (workspacedomain.ControlSnapshot, error) {
	registry := make([]workspacedomain.Workspace, 0, 2)
	if store.active != nil {
		registry = append(registry, *store.active)
	}
	if store.target.ID != "" && (store.active == nil || store.target.ID != store.active.ID) {
		registry = append(registry, store.target)
	}
	snapshot := workspacedomain.ControlSnapshot{
		State: store.state, Active: store.active,
		Registry: registry,
		Runtimes: append([]workspacedomain.RuntimeRecord(nil), store.runtimes...),
	}
	if store.operation.ID != "" {
		operation := store.operation
		snapshot.Operation = &operation
	}
	return snapshot, nil
}

func (store *successfulSwitchStore) BeginSwitch(
	_ context.Context,
	command workspaceapplication.BeginSwitchStoreCommand,
) (workspacedomain.SwitchOperation, error) {
	leaseOwner := command.LeaseOwnerID
	store.operation = workspacedomain.SwitchOperation{
		ID: command.OperationID, ControllerInstanceID: command.ControllerInstanceID,
		IdempotencyKey: command.IdempotencyKey, RequestHash: command.RequestHash,
		TargetWorkspaceID: store.target.ID, TargetRootFingerprint: store.target.RootFingerprint,
		TargetBindingVersion: store.target.BindingVersion, GrantGeneration: store.state.GrantGeneration + 1,
		RecoveryGeneration: store.state.GrantGeneration, Phase: workspacedomain.SwitchPhaseValidating,
		LeaseOwnerID: &leaseOwner, LeaseExpiresAt: time.Now().Add(command.LeaseDuration),
		DeadlineAt: command.Deadline, Version: 1,
	}
	if store.active != nil {
		previousID := store.active.ID
		store.operation.PreviousWorkspaceID = &previousID
		store.state.PreviousActiveWorkspaceID = &previousID
	}
	store.state.StateVersion++
	store.state.OperationID = &store.operation.ID
	store.state.OperationPhase = store.operation.Phase
	store.state.TargetWorkspaceID = &store.target.ID
	return store.operation, nil
}

func (store *successfulSwitchStore) GetSwitchOperation(
	context.Context,
	foundation.ID,
) (workspacedomain.SwitchOperation, error) {
	return store.operation, nil
}

func (store *successfulSwitchStore) AdvanceSwitch(
	_ context.Context,
	command workspaceapplication.AdvanceSwitchCommand,
) (workspacedomain.SwitchOperation, error) {
	store.operation.Phase = command.NextPhase
	if command.NextPhase == workspacedomain.SwitchPhaseQuiescing && store.operation.PreviousWorkspaceID != nil {
		operationID := store.operation.ID
		for index := range store.runtimes {
			if store.runtimes[index].WorkspaceID != *store.operation.PreviousWorkspaceID {
				continue
			}
			store.runtimes[index].OperationID = &operationID
			store.runtimes[index].Phase = workspacedomain.RuntimePhaseQuiesced
			store.runtimes[index].Version++
			store.runtimes[index].Fresh = true
		}
	}
	store.operation.Version++
	store.state.StateVersion++
	store.state.OperationPhase = command.NextPhase
	return store.operation, nil
}

func (store *successfulSwitchStore) RevokeActiveWorkspace(
	context.Context,
	workspaceapplication.RevokeActiveCommand,
) (workspacedomain.ControlState, error) {
	store.active = nil
	store.state.ActiveWorkspaceID = nil
	store.operation.Version++
	store.state.StateVersion++
	return store.state, nil
}

func (store *successfulSwitchStore) CommitTargetWorkspace(
	context.Context,
	workspaceapplication.CommitTargetCommand,
) (workspacedomain.ControlState, error) {
	active := store.target
	active.Status = workspacedomain.WorkspaceStatusActive
	store.active = &active
	store.state.ActiveWorkspaceID = &store.active.ID
	store.state.GrantGeneration = store.operation.GrantGeneration
	store.operation.Version++
	store.state.StateVersion++
	return store.state, nil
}

func (store *successfulSwitchStore) SetRuntimePhase(
	_ context.Context,
	command workspaceapplication.RuntimePhaseCommand,
) (workspacedomain.RuntimeRecord, error) {
	for index := range store.runtimes {
		if store.runtimes[index].Role != command.Role {
			continue
		}
		store.runtimes[index].Phase = command.NextPhase
		store.runtimes[index].Version++
		return store.runtimes[index], nil
	}
	return workspacedomain.RuntimeRecord{}, errors.New("runtime fixture not found")
}

func (store *successfulSwitchStore) FinishSwitch(
	_ context.Context,
	command workspaceapplication.FinishSwitchCommand,
) (workspacedomain.SwitchOperation, error) {
	store.operation.Result = command.Result
	store.operation.ErrorCode = command.ErrorCode
	store.operation.Version++
	store.state.StateVersion++
	store.state.OperationID = nil
	return store.operation, nil
}

func (store *successfulSwitchStore) prepareRuntimes(operationID foundation.ID, grant Grant) {
	operation := operationID
	store.runtimes = []workspacedomain.RuntimeRecord{
		{
			Role: workspacedomain.RuntimeRoleAPI, InstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440041"),
			WorkspaceID: store.target.ID, OperationID: &operation, GrantGeneration: grant.Generation,
			RootFingerprint: grant.RootFingerprint, BindingVersion: grant.BindingVersion,
			Phase: workspacedomain.RuntimePhasePrepared, Version: 1, Fresh: true,
		},
		{
			Role: workspacedomain.RuntimeRoleWorker, InstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440042"),
			WorkspaceID: store.target.ID, OperationID: &operation, GrantGeneration: grant.Generation,
			RootFingerprint: grant.RootFingerprint, BindingVersion: grant.BindingVersion,
			Phase: workspacedomain.RuntimePhasePrepared, Version: 1, Fresh: true,
		},
	}
}

func (store *successfulSwitchStore) activateRuntimes(grant Grant) {
	for index := range store.runtimes {
		store.runtimes[index].WorkspaceID = foundation.ID(grant.WorkspaceID)
		store.runtimes[index].OperationID = nil
		store.runtimes[index].GrantGeneration = grant.Generation
		store.runtimes[index].Phase = workspacedomain.RuntimePhaseActive
		store.runtimes[index].Version++
		store.runtimes[index].Fresh = true
	}
}

type successfulSwitchRuntime struct {
	store         *successfulSwitchStore
	prepareCalls  int
	applyCalls    int
	revokeCalls   int
	appliedGrants []Grant
}

func (runtime *successfulSwitchRuntime) PrepareGrant(
	_ context.Context,
	operationID foundation.ID,
	grant Grant,
	_ bool,
) error {
	runtime.prepareCalls++
	runtime.store.prepareRuntimes(operationID, grant)
	return nil
}

func (runtime *successfulSwitchRuntime) ApplyGrant(_ context.Context, grant Grant) error {
	runtime.applyCalls++
	runtime.appliedGrants = append(runtime.appliedGrants, grant)
	runtime.store.activateRuntimes(grant)
	return nil
}

func (runtime *successfulSwitchRuntime) RevokeGrant(context.Context) error {
	runtime.revokeCalls++
	return nil
}

func (store *recoveryRegistryStore) ListRegistry(context.Context, bool) ([]workspacedomain.Workspace, error) {
	return append([]workspacedomain.Workspace(nil), store.workspaces...), nil
}

func (store *recoveryRegistryStore) SetWorkspaceAvailability(_ context.Context, update workspacedomain.AvailabilityUpdate) (workspacedomain.Workspace, error) {
	store.updates = append(store.updates, update)
	for index := range store.workspaces {
		if store.workspaces[index].ID != update.WorkspaceID {
			continue
		}
		store.workspaces[index].Availability = update.Availability
		store.workspaces[index].AvailabilityReason = update.Reason
		store.workspaces[index].Version++
		return store.workspaces[index], nil
	}
	return workspacedomain.Workspace{}, errors.New("workspace fixture not found")
}

type recoveryControlStore struct {
	workspaceapplication.ControlStore
	operation         workspacedomain.SwitchOperation
	snapshot          workspacedomain.ControlSnapshot
	revokeActiveErr   error
	revokeActiveCalls int
	advanceCalls      int
	finishCalls       int
}

func (store *recoveryControlStore) GetSwitchOperation(context.Context, foundation.ID) (workspacedomain.SwitchOperation, error) {
	return store.operation, nil
}

func (store *recoveryControlStore) ControlSnapshot(context.Context, time.Duration) (workspacedomain.ControlSnapshot, error) {
	return store.snapshot, nil
}

func (store *recoveryControlStore) RevokeActiveWorkspace(context.Context, workspaceapplication.RevokeActiveCommand) (workspacedomain.ControlState, error) {
	store.revokeActiveCalls++
	if store.revokeActiveErr != nil {
		return workspacedomain.ControlState{}, store.revokeActiveErr
	}
	store.snapshot.State.ActiveWorkspaceID = nil
	store.snapshot.Active = nil
	return store.snapshot.State, nil
}

func (store *recoveryControlStore) AdvanceSwitch(_ context.Context, command workspaceapplication.AdvanceSwitchCommand) (workspacedomain.SwitchOperation, error) {
	store.advanceCalls++
	store.operation.Phase = command.NextPhase
	store.operation.Version++
	return store.operation, nil
}

func (store *recoveryControlStore) FinishSwitch(_ context.Context, command workspaceapplication.FinishSwitchCommand) (workspacedomain.SwitchOperation, error) {
	store.finishCalls++
	store.operation.Result = command.Result
	store.operation.ErrorCode = command.ErrorCode
	store.operation.Version++
	return store.operation, nil
}

type recoveryRuntime struct {
	prepareCalls int
	applyCalls   int
	revokeCalls  int
	revokeErr    error
	applyHook    func(Grant) error
}

func (runtime *recoveryRuntime) PrepareGrant(context.Context, foundation.ID, Grant, bool) error {
	runtime.prepareCalls++
	return nil
}

func (runtime *recoveryRuntime) ApplyGrant(_ context.Context, grant Grant) error {
	runtime.applyCalls++
	if runtime.applyHook != nil {
		return runtime.applyHook(grant)
	}
	return nil
}

func (runtime *recoveryRuntime) RevokeGrant(context.Context) error {
	runtime.revokeCalls++
	return runtime.revokeErr
}

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

func (*retryRecoveryRuntime) ApplyGrant(context.Context, Grant) error { return nil }

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

func (runtime *retryRecoveryRuntime) revokeCount() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.revokes
}

func newRecoveryTestCoordinator(t *testing.T, control *recoveryControlStore, runtime *recoveryRuntime) *Coordinator {
	t.Helper()
	return newRecoveryTestCoordinatorWithRegistry(t, &recoveryRegistryStore{}, control, runtime)
}

func newRecoveryTestCoordinatorWithRegistry(
	t *testing.T,
	registry *recoveryRegistryStore,
	control *recoveryControlStore,
	runtime *recoveryRuntime,
) *Coordinator {
	t.Helper()
	service, err := workspaceapplication.NewControlService(
		registry, control, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		Service: service, Runtime: runtime, Validator: PathValidator{},
		ControlInstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440013"),
		LeaseOwnerID:      foundation.ID("550e8400-e29b-41d4-a716-446655440014"),
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
		ControlInstanceID: foundation.ID("550e8400-e29b-41d4-a716-446655440016"),
		LeaseOwnerID:      foundation.ID("550e8400-e29b-41d4-a716-446655440017"),
	})
	if err != nil {
		t.Fatal(err)
	}
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
