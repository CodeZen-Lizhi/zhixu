//go:build integration

package migration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestWorkspaceRootGrantRepositorySerializesBeginAndTakesOverExpiredLease(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 67); err != nil {
		t.Fatal(err)
	}
	repository, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	first := reserveWorkspaceRoot(t, ctx, repository,
		"67300000-0000-4000-8000-000000000001", "/tmp/root-grant-concurrent-one", strings.Repeat("d", 64), now)
	second := reserveWorkspaceRoot(t, ctx, repository,
		"67300000-0000-4000-8000-000000000002", "/tmp/root-grant-concurrent-two", strings.Repeat("e", 64), now)
	controllerID := workspaceRootGrantID(t, "67300000-0000-4000-8000-000000000010")
	initial := workspaceRootGrantSnapshot(t, ctx, repository)
	type beginResult struct {
		operation domain.SwitchOperation
		command   application.BeginSwitchStoreCommand
		err       error
	}
	commands := []application.BeginSwitchStoreCommand{
		{
			OperationID:          workspaceRootGrantID(t, "67300000-0000-4000-8000-000000000011"),
			ControllerInstanceID: controllerID, IdempotencyKey: "concurrent-one", RequestHash: strings.Repeat("3", 64),
			TargetWorkspaceID: first.Workspace.ID, ExpectedStateVersion: initial.State.StateVersion,
			LeaseOwnerID:  workspaceRootGrantID(t, "67300000-0000-4000-8000-000000000021"),
			LeaseDuration: time.Second, Deadline: now.Add(500 * time.Millisecond),
		},
		{
			OperationID:          workspaceRootGrantID(t, "67300000-0000-4000-8000-000000000012"),
			ControllerInstanceID: controllerID, IdempotencyKey: "concurrent-two", RequestHash: strings.Repeat("4", 64),
			TargetWorkspaceID: second.Workspace.ID, ExpectedStateVersion: initial.State.StateVersion,
			LeaseOwnerID:  workspaceRootGrantID(t, "67300000-0000-4000-8000-000000000022"),
			LeaseDuration: time.Second, Deadline: now.Add(500 * time.Millisecond),
		},
	}
	start := make(chan struct{})
	results := make(chan beginResult, len(commands))
	for _, command := range commands {
		command := command
		go func() {
			<-start
			operation, beginErr := repository.BeginSwitch(ctx, command)
			results <- beginResult{operation: operation, command: command, err: beginErr}
		}()
	}
	close(start)
	var winner beginResult
	failures := 0
	for range commands {
		result := <-results
		if result.err == nil {
			winner = result
			continue
		}
		failures++
		assertWorkspaceRootGrantErrorOneOf(t, result.err, domain.ErrorCodeSwitchInProgress, domain.ErrorCodeControlStateConflict)
	}
	if winner.operation.ID == "" || failures != 1 {
		t.Fatalf("concurrent begin winner=%#v failures=%d", winner, failures)
	}

	conflictingReplay := winner.command
	conflictingReplay.OperationID = workspaceRootGrantID(t, "67300000-0000-4000-8000-000000000099")
	conflictingReplay.RequestHash = strings.Repeat("f", 64)
	_, err = repository.BeginSwitch(ctx, conflictingReplay)
	assertWorkspaceRootGrantErrorOneOf(t, err, domain.ErrorCodeSwitchIdempotencyConflict)

	time.Sleep(1100 * time.Millisecond)
	snapshot := workspaceRootGrantSnapshot(t, ctx, repository)
	_, err = repository.RenewSwitch(ctx, application.RenewSwitchCommand{
		OperationID: winner.operation.ID, LeaseOwnerID: winner.command.LeaseOwnerID,
		ExpectedPhase:            domain.SwitchPhaseValidating,
		ExpectedOperationVersion: winner.operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: time.Minute,
	})
	assertWorkspaceRootGrantErrorOneOf(t, err, domain.ErrorCodeSwitchLeaseExpired)
	newOwner := workspaceRootGrantID(t, "67300000-0000-4000-8000-000000000030")
	takenOver, err := repository.TakeOverSwitch(ctx, application.TakeOverSwitchCommand{
		OperationID: winner.operation.ID, NewLeaseOwnerID: newOwner,
		ExpectedPhase:            domain.SwitchPhaseValidating,
		ExpectedOperationVersion: winner.operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if takenOver.LeaseOwnerID == nil || *takenOver.LeaseOwnerID != newOwner || takenOver.Version != winner.operation.Version+1 {
		t.Fatalf("taken over operation=%#v", takenOver)
	}
	snapshot = workspaceRootGrantSnapshot(t, ctx, repository)
	if _, err := repository.FinishSwitch(ctx, application.FinishSwitchCommand{
		OperationID: takenOver.ID, LeaseOwnerID: newOwner,
		Result: domain.SwitchResultRejected, ErrorCode: "WORKSPACE_VALIDATION_REJECTED",
		ExpectedOperationVersion: takenOver.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		RuntimeFreshWithin: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = repository.BeginSwitch(ctx, application.BeginSwitchStoreCommand{
		OperationID:          workspaceRootGrantID(t, "67300000-0000-4000-8000-000000000040"),
		ControllerInstanceID: controllerID, IdempotencyKey: "stale-state", RequestHash: strings.Repeat("5", 64),
		TargetWorkspaceID: first.Workspace.ID, ExpectedStateVersion: initial.State.StateVersion,
		LeaseOwnerID: newOwner, LeaseDuration: time.Minute, Deadline: now.Add(10 * time.Minute),
	})
	assertWorkspaceRootGrantErrorOneOf(t, err, domain.ErrorCodeControlStateConflict)
}

func TestWorkspaceRootGrantRepositorySwitchAndRollback(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 67); err != nil {
		t.Fatal(err)
	}
	repository, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	legacyDirect, err := repository.CreateWorkspace(ctx, domain.Workspace{
		ID:   workspaceRootGrantID(t, "67100000-0000-4000-8000-000000000000"),
		Name: "legacy-direct", RootPath: "/tmp/root-grant-legacy-direct",
		Git:    domain.GitBaseline{RepositoryPath: "/tmp/root-grant-legacy-direct", CheckedAt: now},
		Status: domain.WorkspaceStatusInactive, Version: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacyDirect.Availability != domain.WorkspaceAvailabilityMigrationRequired ||
		legacyDirect.AvailabilityReason != "WORKSPACE_BINDING_LEGACY_DIRECT" ||
		legacyDirect.BindingVersion != 0 || legacyDirect.RootFingerprint != "" {
		t.Fatalf("legacy direct workspace=%#v", legacyDirect)
	}
	removedLegacy, err := repository.RemoveWorkspace(ctx, domain.WorkspaceRemoval{
		WorkspaceID: legacyDirect.ID, ExpectedVersion: legacyDirect.Version, RemovedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if removedLegacy.RemovedAt.IsZero() || removedLegacy.Availability != domain.WorkspaceAvailabilityMigrationRequired {
		t.Fatalf("removed legacy workspace=%#v", removedLegacy)
	}
	first := reserveWorkspaceRoot(t, ctx, repository,
		"67100000-0000-4000-8000-000000000001", "/tmp/root-grant-first", strings.Repeat("a", 64), now)
	if first.Workspace.Status != domain.WorkspaceStatusInactive || first.Workspace.Availability != domain.WorkspaceAvailabilityAvailable {
		t.Fatalf("first Registry identity=%#v", first)
	}

	controllerID := workspaceRootGrantID(t, "67100000-0000-4000-8000-000000000010")
	leaseOwnerID := workspaceRootGrantID(t, "67100000-0000-4000-8000-000000000011")
	operationID := workspaceRootGrantID(t, "67100000-0000-4000-8000-000000000012")
	snapshot := workspaceRootGrantSnapshot(t, ctx, repository)
	operation, err := repository.BeginSwitch(ctx, application.BeginSwitchStoreCommand{
		OperationID: operationID, ControllerInstanceID: controllerID,
		IdempotencyKey: "first-switch", RequestHash: strings.Repeat("1", 64),
		TargetWorkspaceID: first.Workspace.ID, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseOwnerID: leaseOwnerID, LeaseDuration: time.Minute, Deadline: now.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := repository.BeginSwitch(ctx, application.BeginSwitchStoreCommand{
		OperationID:          workspaceRootGrantID(t, "67100000-0000-4000-8000-000000000099"),
		ControllerInstanceID: controllerID, IdempotencyKey: "first-switch", RequestHash: strings.Repeat("1", 64),
		TargetWorkspaceID: first.Workspace.ID, ExpectedStateVersion: 1,
		LeaseOwnerID: leaseOwnerID, LeaseDuration: time.Minute, Deadline: now.Add(10 * time.Minute),
	})
	if err != nil || replayed.ID != operation.ID {
		t.Fatalf("idempotent replay=%#v error=%v", replayed, err)
	}
	operation = advanceWorkspaceSwitch(t, ctx, repository, operation, leaseOwnerID, domain.SwitchPhaseQuiescing)
	operation = advanceWorkspaceSwitch(t, ctx, repository, operation, leaseOwnerID, domain.SwitchPhaseRevoking)
	snapshot = workspaceRootGrantSnapshot(t, ctx, repository)
	if _, err := repository.RevokeActiveWorkspace(ctx, application.RevokeActiveCommand{
		OperationID: operation.ID, LeaseOwnerID: leaseOwnerID,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	operation = workspaceRootGrantOperation(t, ctx, repository, operation.ID)
	operation = advanceWorkspaceSwitch(t, ctx, repository, operation, leaseOwnerID, domain.SwitchPhaseApplyingGrant)
	operation = advanceWorkspaceSwitch(t, ctx, repository, operation, leaseOwnerID, domain.SwitchPhasePreparing)
	api := registerWorkspaceRuntime(t, ctx, repository, operation, first.Workspace,
		domain.RuntimeRoleAPI, "67100000-0000-4000-8000-000000000020", operation.GrantGeneration)
	worker := registerWorkspaceRuntime(t, ctx, repository, operation, first.Workspace,
		domain.RuntimeRoleWorker, "67100000-0000-4000-8000-000000000021", operation.GrantGeneration)
	operation = advanceWorkspaceSwitch(t, ctx, repository, operation, leaseOwnerID, domain.SwitchPhaseVerifying)
	api = setWorkspaceRuntimePhase(t, ctx, repository, api, operation.ID, domain.RuntimePhaseVerifying)
	worker = setWorkspaceRuntimePhase(t, ctx, repository, worker, operation.ID, domain.RuntimePhaseVerifying)
	operation = advanceWorkspaceSwitch(t, ctx, repository, operation, leaseOwnerID, domain.SwitchPhaseCommitting)
	snapshot = workspaceRootGrantSnapshot(t, ctx, repository)
	if _, err := repository.CommitTargetWorkspace(ctx, application.CommitTargetCommand{
		OperationID: operation.ID, LeaseOwnerID: leaseOwnerID,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: time.Minute, RuntimeFreshWithin: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	operation = workspaceRootGrantOperation(t, ctx, repository, operation.ID)
	operation = advanceWorkspaceSwitch(t, ctx, repository, operation, leaseOwnerID, domain.SwitchPhaseActivating)
	api = setWorkspaceRuntimePhase(t, ctx, repository, api, operation.ID, domain.RuntimePhaseActive)
	worker = setWorkspaceRuntimePhase(t, ctx, repository, worker, operation.ID, domain.RuntimePhaseActive)
	snapshot = workspaceRootGrantSnapshot(t, ctx, repository)
	if _, err := repository.FinishSwitch(ctx, application.FinishSwitchCommand{
		OperationID: operation.ID, LeaseOwnerID: leaseOwnerID, Result: domain.SwitchResultSucceeded,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		RuntimeFreshWithin: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot = workspaceRootGrantSnapshot(t, ctx, repository)
	if snapshot.Active == nil || snapshot.Active.ID != first.Workspace.ID ||
		snapshot.State.GrantGeneration != operation.GrantGeneration || snapshot.State.OperationID != nil {
		t.Fatalf("committed snapshot=%#v", snapshot)
	}

	second := reserveWorkspaceRoot(t, ctx, repository,
		"67100000-0000-4000-8000-000000000002", "/tmp/root-grant-second", strings.Repeat("b", 64), now.Add(time.Second))
	rollbackOperationID := workspaceRootGrantID(t, "67100000-0000-4000-8000-000000000030")
	snapshot = workspaceRootGrantSnapshot(t, ctx, repository)
	operation, err = repository.BeginSwitch(ctx, application.BeginSwitchStoreCommand{
		OperationID: rollbackOperationID, ControllerInstanceID: controllerID,
		IdempotencyKey: "rollback-switch", RequestHash: strings.Repeat("2", 64),
		TargetWorkspaceID: second.Workspace.ID, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseOwnerID: leaseOwnerID, LeaseDuration: time.Minute, Deadline: now.Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation = advanceWorkspaceSwitch(t, ctx, repository, operation, leaseOwnerID, domain.SwitchPhaseQuiescing)
	api = setWorkspaceRuntimePhase(t, ctx, repository, api, operation.ID, domain.RuntimePhaseQuiescing)
	worker = setWorkspaceRuntimePhase(t, ctx, repository, worker, operation.ID, domain.RuntimePhaseQuiescing)
	api = setWorkspaceRuntimePhase(t, ctx, repository, api, operation.ID, domain.RuntimePhaseQuiesced)
	worker = setWorkspaceRuntimePhase(t, ctx, repository, worker, operation.ID, domain.RuntimePhaseQuiesced)
	operation = advanceWorkspaceSwitch(t, ctx, repository, operation, leaseOwnerID, domain.SwitchPhaseRevoking)
	snapshot = workspaceRootGrantSnapshot(t, ctx, repository)
	if _, err := repository.RevokeActiveWorkspace(ctx, application.RevokeActiveCommand{
		OperationID: operation.ID, LeaseOwnerID: leaseOwnerID,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	operation = workspaceRootGrantOperation(t, ctx, repository, operation.ID)
	operation = advanceWorkspaceSwitch(t, ctx, repository, operation, leaseOwnerID, domain.SwitchPhaseApplyingGrant)
	operation = advanceWorkspaceSwitch(t, ctx, repository, operation, leaseOwnerID, domain.SwitchPhasePreparing)
	operation = advanceWorkspaceSwitch(t, ctx, repository, operation, leaseOwnerID, domain.SwitchPhaseRollingBack)
	operation = advanceWorkspaceSwitch(t, ctx, repository, operation, leaseOwnerID, domain.SwitchPhaseRecovering)
	api = registerWorkspaceRuntime(t, ctx, repository, operation, first.Workspace,
		domain.RuntimeRoleAPI, "67100000-0000-4000-8000-000000000040", operation.RecoveryGeneration)
	worker = registerWorkspaceRuntime(t, ctx, repository, operation, first.Workspace,
		domain.RuntimeRoleWorker, "67100000-0000-4000-8000-000000000041", operation.RecoveryGeneration)
	snapshot = workspaceRootGrantSnapshot(t, ctx, repository)
	if _, err := repository.RestorePreviousWorkspace(ctx, application.RestorePreviousCommand{
		OperationID: operation.ID, LeaseOwnerID: leaseOwnerID,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: time.Minute, RuntimeFreshWithin: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	operation = workspaceRootGrantOperation(t, ctx, repository, operation.ID)
	api = setWorkspaceRuntimePhase(t, ctx, repository, api, operation.ID, domain.RuntimePhaseActive)
	worker = setWorkspaceRuntimePhase(t, ctx, repository, worker, operation.ID, domain.RuntimePhaseActive)
	snapshot = workspaceRootGrantSnapshot(t, ctx, repository)
	if _, err := repository.FinishSwitch(ctx, application.FinishSwitchCommand{
		OperationID: operation.ID, LeaseOwnerID: leaseOwnerID, Result: domain.SwitchResultRolledBack,
		ErrorCode:                "WORKSPACE_TARGET_PREPARE_FAILED",
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		RuntimeFreshWithin: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot = workspaceRootGrantSnapshot(t, ctx, repository)
	if snapshot.Active == nil || snapshot.Active.ID != first.Workspace.ID ||
		snapshot.State.GrantGeneration != operation.RecoveryGeneration || snapshot.State.OperationID != nil {
		t.Fatalf("rolled back snapshot=%#v", snapshot)
	}
}

func reserveWorkspaceRoot(
	t *testing.T,
	ctx context.Context,
	repository *workspacepostgres.Repository,
	id, root, fingerprint string,
	now time.Time,
) domain.WorkspaceResolution {
	t.Helper()
	result, err := repository.ResolveWorkspace(ctx, domain.WorkspaceReservation{
		ID: workspaceRootGrantID(t, id), Name: root,
		Binding: domain.RootBinding{
			CanonicalPath: root, GitRepositoryPath: root, Fingerprint: fingerprint, BindingVersion: 1,
		},
		Git: domain.GitBaseline{RepositoryPath: root, Branch: "main", Head: strings.Repeat("c", 40), CheckedAt: now},
		Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func advanceWorkspaceSwitch(
	t *testing.T,
	ctx context.Context,
	repository *workspacepostgres.Repository,
	operation domain.SwitchOperation,
	leaseOwnerID foundation.ID,
	next domain.SwitchPhase,
) domain.SwitchOperation {
	t.Helper()
	snapshot := workspaceRootGrantSnapshot(t, ctx, repository)
	updated, err := repository.AdvanceSwitch(ctx, application.AdvanceSwitchCommand{
		OperationID: operation.ID, LeaseOwnerID: leaseOwnerID,
		ExpectedPhase: operation.Phase, NextPhase: next,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func registerWorkspaceRuntime(
	t *testing.T,
	ctx context.Context,
	repository *workspacepostgres.Repository,
	operation domain.SwitchOperation,
	workspace domain.Workspace,
	role domain.RuntimeRole,
	instanceID string,
	generation int64,
) domain.RuntimeRecord {
	t.Helper()
	record, err := repository.RegisterRuntime(ctx, application.RuntimeRegistration{
		Role: role, InstanceID: workspaceRootGrantID(t, instanceID), WorkspaceID: workspace.ID,
		OperationID: &operation.ID, GrantGeneration: generation,
		RootFingerprint: workspace.RootFingerprint, BindingVersion: workspace.BindingVersion,
		Phase: domain.RuntimePhasePrepared,
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func setWorkspaceRuntimePhase(
	t *testing.T,
	ctx context.Context,
	repository *workspacepostgres.Repository,
	runtime domain.RuntimeRecord,
	operationID foundation.ID,
	next domain.RuntimePhase,
) domain.RuntimeRecord {
	t.Helper()
	updated, err := repository.SetRuntimePhase(ctx, application.RuntimePhaseCommand{
		Role: runtime.Role, InstanceID: runtime.InstanceID, OperationID: &operationID,
		ExpectedPhase: runtime.Phase, NextPhase: next, ExpectedRuntimeVersion: runtime.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func workspaceRootGrantSnapshot(t *testing.T, ctx context.Context, repository *workspacepostgres.Repository) domain.ControlSnapshot {
	t.Helper()
	snapshot, err := repository.ControlSnapshot(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func workspaceRootGrantOperation(t *testing.T, ctx context.Context, repository *workspacepostgres.Repository, id foundation.ID) domain.SwitchOperation {
	t.Helper()
	operation, err := repository.GetSwitchOperation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func workspaceRootGrantID(t *testing.T, value string) foundation.ID {
	t.Helper()
	id, err := foundation.ParseID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func assertWorkspaceRootGrantErrorOneOf(t *testing.T, err error, codes ...string) {
	t.Helper()
	var classified *foundation.Error
	if err == nil || !errors.As(err, &classified) {
		t.Fatalf("error=%v want one of %v", err, codes)
	}
	for _, code := range codes {
		if classified.Code == code {
			return
		}
	}
	t.Fatalf("error code=%s want one of %v", classified.Code, codes)
}
