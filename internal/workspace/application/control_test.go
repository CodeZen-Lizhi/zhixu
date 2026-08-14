package application

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestControlServiceResolvesParentAndChildAsSeparateIdentities(t *testing.T) {
	store := &controlContractStore{}
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	service, err := NewControlService(store, store, &sequenceIDGenerator{ids: []foundation.ID{
		"67200000-0000-4000-8000-000000000001",
		"67200000-0000-4000-8000-000000000002",
	}}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		root        string
		fingerprint string
	}{
		{root: "/tmp/workspace-parent", fingerprint: strings.Repeat("a", 64)},
		{root: "/tmp/workspace-parent/child", fingerprint: strings.Repeat("b", 64)},
	} {
		if _, err := service.ResolveWorkspace(context.Background(), RegisterWorkspaceCommand{
			Name: "Workspace", CanonicalRoot: fixture.root, GitRepositoryPath: fixture.root,
			RootFingerprint: fixture.fingerprint, BindingVersion: 1, GitCheckedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.reservations) != 2 || store.reservations[0].ID == store.reservations[1].ID ||
		store.reservations[0].Binding.CanonicalPath != "/tmp/workspace-parent" ||
		store.reservations[1].Binding.CanonicalPath != "/tmp/workspace-parent/child" {
		t.Fatalf("Registry reservations=%#v", store.reservations)
	}
}

func TestControlServiceBeginSwitchForwardsIdempotencyAndStateVersion(t *testing.T) {
	store := &controlContractStore{}
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	operationID := foundation.ID("67200000-0000-4000-8000-000000000010")
	service, err := NewControlService(store, store, &sequenceIDGenerator{ids: []foundation.ID{operationID}}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	command := BeginSwitchCommand{
		ControllerInstanceID: "67200000-0000-4000-8000-000000000011",
		IdempotencyKey:       "switch-request", RequestHash: strings.Repeat("c", 64),
		TargetWorkspaceID:    "67200000-0000-4000-8000-000000000012",
		ExpectedStateVersion: 7, LeaseOwnerID: "67200000-0000-4000-8000-000000000013",
		LeaseDuration: time.Minute, Deadline: now.Add(5 * time.Minute),
	}
	operation, err := service.BeginSwitch(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if operation.ID != operationID || store.begin.OperationID != operationID ||
		store.begin.ExpectedStateVersion != command.ExpectedStateVersion ||
		store.begin.IdempotencyKey != command.IdempotencyKey || store.begin.RequestHash != command.RequestHash {
		t.Fatalf("operation=%#v persisted command=%#v", operation, store.begin)
	}
}

func TestControlServiceRejectsInvalidBindingBeforePersistence(t *testing.T) {
	store := &controlContractStore{}
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	service, err := NewControlService(store, store, &sequenceIDGenerator{ids: []foundation.ID{"67200000-0000-4000-8000-000000000020"}}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ResolveWorkspace(context.Background(), RegisterWorkspaceCommand{
		Name: "Workspace", CanonicalRoot: "relative/path", GitRepositoryPath: "relative/path",
		RootFingerprint: strings.Repeat("d", 64), BindingVersion: 1, GitCheckedAt: now,
	})
	requireClassifiedError(t, err, foundation.ErrorInvalidInput, domain.ErrorCodeRegistryInvalid)
	if len(store.reservations) != 0 {
		t.Fatalf("invalid binding reached persistence: %#v", store.reservations)
	}
}

func TestControlServiceRebindsExactWorkspaceIdentity(t *testing.T) {
	store := &controlContractStore{}
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	eventID := foundation.ID("67200000-0000-4000-8000-000000000030")
	service, err := NewControlService(store, store, &sequenceIDGenerator{ids: []foundation.ID{eventID}}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	command := RebindWorkspaceCommand{
		ControllerInstanceID: "67200000-0000-4000-8000-000000000031",
		WorkspaceID:          "67200000-0000-4000-8000-000000000032",
		CanonicalRoot:        "/tmp/workspace-rebound",
		OldRootFingerprint:   strings.Repeat("a", 64),
		NewRootFingerprint:   strings.Repeat("b", 64),
		IdempotencyKey:       "workspace-rebind",
	}
	result, err := service.RebindWorkspace(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Workspace.ID != command.WorkspaceID || store.rebind.ID != eventID ||
		store.rebind.ControllerInstanceID != command.ControllerInstanceID {
		t.Fatalf("result=%#v migration=%#v", result, store.rebind)
	}
}

func TestControlServiceRejectsUnsafeRebindBeforePersistence(t *testing.T) {
	store := &controlContractStore{}
	service, err := NewControlService(store, store, &sequenceIDGenerator{}, foundation.FixedClock{Value: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.RebindWorkspace(context.Background(), RebindWorkspaceCommand{
		ControllerInstanceID: "67200000-0000-4000-8000-000000000041",
		WorkspaceID:          "67200000-0000-4000-8000-000000000042",
		CanonicalRoot:        "/tmp/workspace-rebound",
		OldRootFingerprint:   strings.Repeat("a", 64),
		NewRootFingerprint:   strings.Repeat("a", 64),
		IdempotencyKey:       "workspace-rebind",
	})
	requireClassifiedError(t, err, foundation.ErrorInvalidInput, domain.ErrorCodeBindingRebindInvalid)
	if store.rebind.ID != "" {
		t.Fatalf("invalid rebind reached persistence: %#v", store.rebind)
	}
}

type controlContractStore struct {
	reservations []domain.WorkspaceReservation
	begin        BeginSwitchStoreCommand
	rebind       domain.WorkspaceBindingMigration
}

func (store *controlContractStore) RebindWorkspace(_ context.Context, migration domain.WorkspaceBindingMigration) (domain.WorkspaceBindingMigrationResult, error) {
	store.rebind = migration
	return domain.WorkspaceBindingMigrationResult{Workspace: domain.Workspace{
		ID: migration.WorkspaceID, RootPath: migration.CanonicalRoot,
		RootFingerprint: migration.NewRootFingerprint, BindingVersion: 2,
	}, Changed: true}, nil
}

func (store *controlContractStore) ResolveWorkspace(_ context.Context, reservation domain.WorkspaceReservation) (domain.WorkspaceResolution, error) {
	store.reservations = append(store.reservations, reservation)
	return domain.WorkspaceResolution{Workspace: domain.Workspace{
		ID: reservation.ID, Name: reservation.Name, RootPath: reservation.Binding.CanonicalPath,
		RootFingerprint: reservation.Binding.Fingerprint, BindingVersion: reservation.Binding.BindingVersion,
	}}, nil
}

func (*controlContractStore) ListRegistry(context.Context, bool) ([]domain.Workspace, error) {
	return nil, nil
}

func (*controlContractStore) SetWorkspaceAvailability(_ context.Context, update domain.AvailabilityUpdate) (domain.Workspace, error) {
	return domain.Workspace{ID: update.WorkspaceID, Availability: update.Availability}, nil
}

func (*controlContractStore) RemoveWorkspace(_ context.Context, removal domain.WorkspaceRemoval) (domain.Workspace, error) {
	return domain.Workspace{ID: removal.WorkspaceID, RemovedAt: removal.RemovedAt}, nil
}

func (*controlContractStore) ControlSnapshot(context.Context, time.Duration) (domain.ControlSnapshot, error) {
	return domain.ControlSnapshot{}, nil
}

func (*controlContractStore) GetSwitchOperation(_ context.Context, id foundation.ID) (domain.SwitchOperation, error) {
	return domain.SwitchOperation{ID: id}, nil
}

func (store *controlContractStore) BeginSwitch(_ context.Context, command BeginSwitchStoreCommand) (domain.SwitchOperation, error) {
	store.begin = command
	return domain.SwitchOperation{ID: command.OperationID}, nil
}

func (*controlContractStore) RenewSwitch(_ context.Context, command RenewSwitchCommand) (domain.SwitchOperation, error) {
	return domain.SwitchOperation{ID: command.OperationID}, nil
}

func (*controlContractStore) TakeOverSwitch(_ context.Context, command TakeOverSwitchCommand) (domain.SwitchOperation, error) {
	return domain.SwitchOperation{ID: command.OperationID}, nil
}

func (*controlContractStore) AdvanceSwitch(_ context.Context, command AdvanceSwitchCommand) (domain.SwitchOperation, error) {
	return domain.SwitchOperation{ID: command.OperationID, Phase: command.NextPhase}, nil
}

func (*controlContractStore) RevokeActiveWorkspace(context.Context, RevokeActiveCommand) (domain.ControlState, error) {
	return domain.ControlState{}, nil
}

func (*controlContractStore) CommitTargetWorkspace(context.Context, CommitTargetCommand) (domain.ControlState, error) {
	return domain.ControlState{}, nil
}

func (*controlContractStore) RestorePreviousWorkspace(context.Context, RestorePreviousCommand) (domain.ControlState, error) {
	return domain.ControlState{}, nil
}

func (*controlContractStore) FinishSwitch(_ context.Context, command FinishSwitchCommand) (domain.SwitchOperation, error) {
	return domain.SwitchOperation{ID: command.OperationID, Result: command.Result}, nil
}

func (*controlContractStore) RegisterRuntime(_ context.Context, registration RuntimeRegistration) (domain.RuntimeRecord, error) {
	return domain.RuntimeRecord{Role: registration.Role, InstanceID: registration.InstanceID}, nil
}

func (*controlContractStore) HeartbeatRuntime(_ context.Context, heartbeat RuntimeHeartbeat) (domain.RuntimeRecord, error) {
	return domain.RuntimeRecord{Role: heartbeat.Role, InstanceID: heartbeat.InstanceID}, nil
}

func (*controlContractStore) SetRuntimePhase(_ context.Context, command RuntimePhaseCommand) (domain.RuntimeRecord, error) {
	return domain.RuntimeRecord{Role: command.Role, InstanceID: command.InstanceID, Phase: command.NextPhase}, nil
}

var _ RegistryStore = (*controlContractStore)(nil)
var _ ControlStore = (*controlContractStore)(nil)
