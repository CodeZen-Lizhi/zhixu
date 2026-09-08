package runtimegrant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const runtimeTestWorkspaceID foundation.ID = "550e8400-e29b-41d4-a716-446655440000"

type runtimeServiceStub struct {
	mu           sync.Mutex
	snapshot     workspacedomain.ControlSnapshot
	registration workspaceapplication.RuntimeRegistration
	record       workspacedomain.RuntimeRecord
	phase        workspaceapplication.RuntimePhaseCommand
}

func (stub *runtimeServiceStub) Snapshot(context.Context, time.Duration) (workspacedomain.ControlSnapshot, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	snapshot := stub.snapshot
	if stub.record.Version > 0 {
		snapshot.Runtimes = []workspacedomain.RuntimeRecord{stub.record}
	}
	return snapshot, nil
}

func (stub *runtimeServiceStub) RegisterRuntime(_ context.Context, registration workspaceapplication.RuntimeRegistration) (workspacedomain.RuntimeRecord, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.registration = registration
	stub.record = workspacedomain.RuntimeRecord{
		Role: registration.Role, InstanceID: registration.InstanceID, WorkspaceID: registration.WorkspaceID,
		GrantGeneration: registration.GrantGeneration, RootFingerprint: registration.RootFingerprint,
		BindingVersion: registration.BindingVersion, Phase: registration.Phase, Version: 1,
	}
	return stub.record, nil
}

func (stub *runtimeServiceStub) HeartbeatRuntime(context.Context, workspaceapplication.RuntimeHeartbeat) (workspacedomain.RuntimeRecord, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.record.Version++
	return stub.record, nil
}

func (stub *runtimeServiceStub) SetRuntimePhase(_ context.Context, command workspaceapplication.RuntimePhaseCommand) (workspacedomain.RuntimeRecord, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.phase = command
	stub.record.Phase = command.NextPhase
	if command.NextPhase == workspacedomain.RuntimePhaseActive || command.NextPhase == workspacedomain.RuntimePhaseUnavailable {
		stub.record.OperationID = nil
	} else {
		stub.record.OperationID = command.OperationID
	}
	stub.record.Version++
	return stub.record, nil
}

func TestStartRegistersExactManagedGrantAndClosesUnavailable(t *testing.T) {
	grant, err := rootgrant.NewProcessGrant(runtimeTestWorkspaceID, "/tmp/runtime-workspace", 7)
	if err != nil {
		t.Fatal(err)
	}
	activeID := runtimeTestWorkspaceID
	service := &runtimeServiceStub{snapshot: workspacedomain.ControlSnapshot{
		State: workspacedomain.ControlState{ActiveWorkspaceID: &activeID, GrantGeneration: 7},
		Active: &workspacedomain.Workspace{
			ID: runtimeTestWorkspaceID, RootPath: "/tmp/runtime-workspace",
			RootFingerprint: strings.Repeat("a", 64), BindingVersion: 1,
			Availability: workspacedomain.WorkspaceAvailabilityAvailable,
		},
	}}
	lease, err := Start(context.Background(), service, grant, workspacedomain.RuntimeRoleAPI)
	if err != nil {
		t.Fatal(err)
	}
	if service.registration.WorkspaceID != runtimeTestWorkspaceID || service.registration.GrantGeneration != 7 ||
		service.registration.Role != workspacedomain.RuntimeRoleAPI || service.registration.Phase != workspacedomain.RuntimePhaseActive {
		t.Fatalf("registration=%#v", service.registration)
	}
	if err := lease.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.phase.ExpectedPhase != workspacedomain.RuntimePhaseActive || service.phase.NextPhase != workspacedomain.RuntimePhaseUnavailable {
		t.Fatalf("close phase=%#v", service.phase)
	}

	t.Run("shutdown deadline retains an unfinished heartbeat", func(t *testing.T) {
		processContext, cancelProcess := context.WithCancel(context.Background())
		defer cancelProcess()
		stopped := make(chan struct{})
		service := &runtimeServiceStub{record: workspacedomain.RuntimeRecord{
			Role: workspacedomain.RuntimeRoleAPI, WorkspaceID: runtimeTestWorkspaceID,
			Phase: workspacedomain.RuntimePhaseActive, Version: 1,
		}}
		lease := &Lease{service: service, record: service.record, cancel: cancelProcess, done: stopped}
		composition := &GORMProcessComposition{Lease: lease}
		expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancelExpired()
		closeResult := make(chan error, 1)
		go func() { closeResult <- composition.Close(expired) }()
		defer func() {
			select {
			case <-stopped:
			default:
				close(stopped)
			}
		}()
		select {
		case err := <-closeResult:
			if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(processContext.Err(), context.Canceled) {
				t.Fatalf("unfinished heartbeat close=%v process=%v", err, processContext.Err())
			}
		case <-time.After(time.Second):
			t.Fatal("workspace cleanup exceeded the caller deadline")
		}
		service.mu.Lock()
		phase, command := service.record.Phase, service.phase
		service.mu.Unlock()
		if phase != workspacedomain.RuntimePhaseActive || command.NextPhase != "" {
			t.Fatal("unfinished heartbeat was declared unavailable")
		}
		close(stopped)
		if err := composition.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if service.record.Phase != workspacedomain.RuntimePhaseUnavailable {
			t.Fatal("completed heartbeat did not release runtime ownership")
		}
	})
}

func TestStartRejectsGrantThatIsNotAuthoritative(t *testing.T) {
	grant, err := rootgrant.NewProcessGrant(runtimeTestWorkspaceID, "/tmp/runtime-workspace", 7)
	if err != nil {
		t.Fatal(err)
	}
	service := &runtimeServiceStub{snapshot: workspacedomain.ControlSnapshot{}}
	if _, err := Start(context.Background(), service, grant, workspacedomain.RuntimeRoleWorker); err == nil {
		t.Fatal("expected inactive grant rejection")
	}
}

func TestLeaseQuiescesBeforeRevokeAndResumesCancelledSwitch(t *testing.T) {
	grant, err := rootgrant.NewProcessGrant(runtimeTestWorkspaceID, "/tmp/runtime-workspace", 7)
	if err != nil {
		t.Fatal(err)
	}
	activeID := runtimeTestWorkspaceID
	service := &runtimeServiceStub{snapshot: workspacedomain.ControlSnapshot{
		State: workspacedomain.ControlState{ActiveWorkspaceID: &activeID, GrantGeneration: 7},
		Active: &workspacedomain.Workspace{
			ID: runtimeTestWorkspaceID, RootPath: "/tmp/runtime-workspace",
			RootFingerprint: strings.Repeat("a", 64), BindingVersion: 1,
			Availability: workspacedomain.WorkspaceAvailabilityAvailable,
		},
	}}
	lease, err := Start(context.Background(), service, grant, workspacedomain.RuntimeRoleWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close(context.Background()) }()
	operationID := foundation.ID("660e8400-e29b-41d4-a716-446655440000")
	previousID := runtimeTestWorkspaceID
	service.snapshot.Operation = &workspacedomain.SwitchOperation{
		ID: operationID, PreviousWorkspaceID: &previousID, GrantGeneration: 8,
		Phase: workspacedomain.SwitchPhaseQuiescing,
	}
	quiesced := false
	beginCalls := 0
	resumeCalls := 0
	if err := lease.SetQuiescenceHooks(QuiescenceHooks{
		Begin:      func(context.Context) error { beginCalls++; return nil },
		IsQuiesced: func(context.Context) (bool, error) { return quiesced, nil },
		Resume:     func(context.Context) error { resumeCalls++; return nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := lease.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.record.Phase != workspacedomain.RuntimePhaseQuiescing || beginCalls != 1 {
		t.Fatalf("first drain record=%#v begin=%d", service.record, beginCalls)
	}
	quiesced = true
	if err := lease.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.record.Phase != workspacedomain.RuntimePhaseQuiesced || service.record.OperationID == nil {
		t.Fatalf("quiesced record=%#v", service.record)
	}

	service.mu.Lock()
	service.snapshot.Operation = nil
	service.record.Phase = workspacedomain.RuntimePhaseActive
	service.record.OperationID = nil
	service.record.Version++
	service.mu.Unlock()
	if err := lease.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resumeCalls != 1 || service.record.Phase != workspacedomain.RuntimePhaseActive {
		t.Fatalf("cancelled drain record=%#v resume=%d", service.record, resumeCalls)
	}
}
