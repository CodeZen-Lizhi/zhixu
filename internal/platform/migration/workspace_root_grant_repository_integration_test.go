//go:build integration

package migration_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

type workspaceRootGrantRepository interface {
	domain.Repository
	application.RegistryStore
	application.ControlStore
}

func runWorkspaceRootGrantIntegration(t *testing.T, test func(*testing.T, *platformpostgres.Pool, context.Context, workspaceRootGrantRepository)) {
	t.Helper()
	t.Run("gorm", func(t *testing.T) {
		fixture := testdb.Require(t, testdb.Config{
			ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
			Availability:     testdb.FailWhenUnavailable,
			MaxConns:         16,
		})
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		test(t, fixture.Pool(), ctx, openGORMWorkspaceRootGrantRepository(t, fixture.Pool()))
	})
}

func openGORMWorkspaceRootGrantRepository(t *testing.T, platform *platformpostgres.Pool) workspaceRootGrantRepository {
	t.Helper()
	repository, err := workspacepostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func TestWorkspaceRootGrantAuthoritativeStoresMatchManagedAndDirect(t *testing.T) {
	t.Run("gorm", func(t *testing.T) {
		fixture := testdb.Require(t, testdb.Config{
			ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
			Availability:     testdb.FailWhenUnavailable,
			MaxConns:         16,
		})
		platform := fixture.Pool()
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		const (
			workspaceID = "67400000-0000-4000-8000-000000000001"
			root        = "/tmp/root-authority"
		)
		now := time.Now().UTC().Truncate(time.Microsecond)
		if _, err := platform.DB().Exec(ctx, `INSERT INTO core.workspace(
				id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
				status,availability,availability_reason,availability_checked_at,version,created_at,updated_at)
				VALUES($1,'Root authority',$2,$3,1,$2,$4,'active','available',NULL,$4,1,$4,$4)`,
			workspaceID, root, strings.Repeat("a", 64), now); err != nil {
			t.Fatal(err)
		}
		if _, err := platform.DB().Exec(ctx, `UPDATE ops.workspace_control_state
				SET active_workspace_id=$1,grant_generation=7,state_version=state_version+1,updated_at=clock_timestamp()
				WHERE singleton=true`, workspaceID); err != nil {
			t.Fatal(err)
		}

		managed, err := rootgrant.NewGORMAuthoritativeStore(platform, rootgrant.RuntimeGrantManaged)
		if err != nil {
			t.Fatal(err)
		}
		managedView, err := managed.CurrentRootGrant(ctx)
		if err != nil {
			t.Fatal(err)
		}
		expectedManaged := rootgrant.AuthoritativeView{
			ActiveWorkspaceID: foundation.ID(workspaceID), WorkspaceID: foundation.ID(workspaceID),
			WorkspaceActive: true, WorkspaceAvailable: true, PersistedRoot: root, GrantGeneration: 7,
		}
		if managedView != expectedManaged {
			t.Fatalf("managed authority view=%#v want=%#v", managedView, expectedManaged)
		}

		direct, err := rootgrant.NewGORMAuthoritativeStore(platform, rootgrant.RuntimeGrantDirect)
		if err != nil {
			t.Fatal(err)
		}
		directView, err := direct.CurrentRootGrant(ctx)
		if err != nil {
			t.Fatal(err)
		}
		expectedDirect := expectedManaged
		expectedDirect.GrantGeneration = 1
		if directView != expectedDirect {
			t.Fatalf("direct authority view=%#v want=%#v", directView, expectedDirect)
		}

		cancelCause := errors.New("root authority caller stopped waiting")
		canceledCtx, cancelCauseFunc := context.WithCancelCause(ctx)
		cancelCauseFunc(cancelCause)
		canceledView, err := managed.CurrentRootGrant(canceledCtx)
		if err == nil || canceledView != (rootgrant.AuthoritativeView{}) || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled authority view=%#v error=%v", canceledView, err)
		}
		if !errors.Is(err, cancelCause) {
			t.Fatalf("GORM root authority lost caller cause: %v", err)
		}
		requireWorkspaceRootGrantPoolReleased(t, platform)
	})
}

func requireWorkspaceRootGrantPoolReleased(t *testing.T, platform *platformpostgres.Pool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for platform.DB().Stat().AcquiredConns() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired := platform.DB().Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("shared root grant pool acquired connections=%d want=0", acquired)
	}
}

func TestWorkspaceRootGrantRepositorySerializesBeginAndTakesOverExpiredLease(t *testing.T) {
	runWorkspaceRootGrantIntegration(t, testWorkspaceRootGrantRepositorySerializesBeginAndTakesOverExpiredLease)
}

func testWorkspaceRootGrantRepositorySerializesBeginAndTakesOverExpiredLease(t *testing.T, _ *platformpostgres.Pool, ctx context.Context, repository workspaceRootGrantRepository) {
	t.Helper()
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
	_, err := repository.BeginSwitch(ctx, conflictingReplay)
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
	runWorkspaceRootGrantIntegration(t, testWorkspaceRootGrantRepositorySwitchAndRollback)
}

func testWorkspaceRootGrantRepositorySwitchAndRollback(t *testing.T, platform *platformpostgres.Pool, ctx context.Context, repository workspaceRootGrantRepository) {
	t.Helper()
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
	reused := reserveWorkspaceRoot(t, ctx, repository,
		"67100000-0000-4000-8000-000000000098", "/tmp/root-grant-first", strings.Repeat("a", 64), now.Add(time.Second))
	if !reused.Reused || reused.Workspace.ID != first.Workspace.ID || reused.Workspace.Version != first.Workspace.Version+1 {
		t.Fatalf("reused Registry identity=%#v first=%#v", reused, first)
	}
	unavailable, err := repository.SetWorkspaceAvailability(ctx, domain.AvailabilityUpdate{
		WorkspaceID: reused.Workspace.ID, ExpectedVersion: reused.Workspace.Version,
		Availability: domain.WorkspaceAvailabilityUnavailable, Reason: "WORKSPACE_ROOT_TEMPORARILY_UNAVAILABLE",
		CheckedAt: now.Add(2 * time.Second),
	})
	if err != nil || unavailable.Availability != domain.WorkspaceAvailabilityUnavailable || unavailable.Version != reused.Workspace.Version+1 {
		t.Fatalf("unavailable Registry identity=%#v error=%v", unavailable, err)
	}
	available, err := repository.SetWorkspaceAvailability(ctx, domain.AvailabilityUpdate{
		WorkspaceID: unavailable.ID, ExpectedVersion: unavailable.Version,
		Availability: domain.WorkspaceAvailabilityAvailable, CheckedAt: now.Add(3 * time.Second),
	})
	if err != nil || available.Availability != domain.WorkspaceAvailabilityAvailable || available.Version != unavailable.Version+1 {
		t.Fatalf("available Registry identity=%#v error=%v", available, err)
	}
	first = domain.WorkspaceResolution{Workspace: available, Reused: true}

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
	if tag, err := platform.DB().Exec(ctx, `UPDATE ops.workspace_runtime
		SET heartbeat_at=clock_timestamp()+interval '2 seconds',version=version+1
		WHERE role='api' AND instance_id=$1 AND version=$2`, string(api.InstanceID), api.Version); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("inject future runtime heartbeat rows=%d error=%v", tag.RowsAffected(), err)
	}
	api.Version++
	_, err = repository.CommitTargetWorkspace(ctx, application.CommitTargetCommand{
		OperationID: operation.ID, LeaseOwnerID: leaseOwnerID,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: time.Minute, RuntimeFreshWithin: time.Minute,
	})
	assertWorkspaceRootGrantErrorOneOf(t, err, domain.ErrorCodeRuntimeNotPrepared)
	time.Sleep(2100 * time.Millisecond)
	api, err = repository.HeartbeatRuntime(ctx, application.RuntimeHeartbeat{
		Role: api.Role, InstanceID: api.InstanceID, OperationID: api.OperationID, ExpectedRuntimeVersion: api.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
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

func TestWorkspaceRootGrantRepositoryRuntimeFences(t *testing.T) {
	runWorkspaceRootGrantIntegration(t, testWorkspaceRootGrantRepositoryRuntimeFences)
}

func testWorkspaceRootGrantRepositoryRuntimeFences(t *testing.T, platform *platformpostgres.Pool, ctx context.Context, repository workspaceRootGrantRepository) {
	t.Helper()
	emptySnapshot, err := repository.ControlSnapshot(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if emptySnapshot.Registry == nil || len(emptySnapshot.Registry) != 0 ||
		emptySnapshot.Runtimes == nil || len(emptySnapshot.Runtimes) != 0 {
		t.Fatalf("empty control snapshot registry=%#v runtimes=%#v", emptySnapshot.Registry, emptySnapshot.Runtimes)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	resolution := reserveWorkspaceRoot(t, ctx, repository,
		"67500000-0000-4000-8000-000000000001", "/tmp/root-grant-runtime-fence", strings.Repeat("b", 64), now)
	workspace := resolution.Workspace
	if _, err := platform.DB().Exec(ctx, `UPDATE core.workspace
		SET status='active',version=version+1,updated_at=clock_timestamp() WHERE id=$1`, string(workspace.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.DB().Exec(ctx, `UPDATE ops.workspace_control_state
		SET active_workspace_id=$1,grant_generation=1,state_version=state_version+1,updated_at=clock_timestamp()
		WHERE singleton=true`, string(workspace.ID)); err != nil {
		t.Fatal(err)
	}
	registration := application.RuntimeRegistration{
		Role: domain.RuntimeRoleAPI, InstanceID: workspaceRootGrantID(t, "67500000-0000-4000-8000-000000000002"),
		WorkspaceID: workspace.ID, GrantGeneration: 1, RootFingerprint: workspace.RootFingerprint,
		BindingVersion: workspace.BindingVersion, Phase: domain.RuntimePhaseActive,
	}
	record, err := repository.RegisterRuntime(ctx, registration)
	if err != nil || record.Version != 1 {
		t.Fatalf("registered runtime=%#v error=%v", record, err)
	}

	wrongBinding := registration
	wrongBinding.RootFingerprint = strings.Repeat("c", 64)
	_, err = repository.RegisterRuntime(ctx, wrongBinding)
	assertWorkspaceRootGrantErrorOneOf(t, err, domain.ErrorCodeRuntimeBindingMismatch)
	wrongGrant := registration
	wrongGrant.GrantGeneration++
	_, err = repository.RegisterRuntime(ctx, wrongGrant)
	assertWorkspaceRootGrantErrorOneOf(t, err, domain.ErrorCodeRuntimeConflict)
	_, err = repository.HeartbeatRuntime(ctx, application.RuntimeHeartbeat{
		Role: record.Role, InstanceID: workspaceRootGrantID(t, "67500000-0000-4000-8000-000000000099"),
		ExpectedRuntimeVersion: record.Version,
	})
	assertWorkspaceRootGrantErrorOneOf(t, err, domain.ErrorCodeRuntimeConflict)
	_, err = repository.HeartbeatRuntime(ctx, application.RuntimeHeartbeat{
		Role: record.Role, InstanceID: record.InstanceID, ExpectedRuntimeVersion: record.Version + 1,
	})
	assertWorkspaceRootGrantErrorOneOf(t, err, domain.ErrorCodeRuntimeConflict)
	record, err = repository.HeartbeatRuntime(ctx, application.RuntimeHeartbeat{
		Role: record.Role, InstanceID: record.InstanceID, ExpectedRuntimeVersion: record.Version,
	})
	if err != nil || record.Version != 2 {
		t.Fatalf("heartbeat runtime=%#v error=%v", record, err)
	}
	_, err = repository.SetRuntimePhase(ctx, application.RuntimePhaseCommand{
		Role: record.Role, InstanceID: record.InstanceID, ExpectedPhase: domain.RuntimePhaseUnavailable,
		NextPhase: domain.RuntimePhaseActive, ExpectedRuntimeVersion: record.Version,
	})
	assertWorkspaceRootGrantErrorOneOf(t, err, domain.ErrorCodeRuntimeConflict)

	if tag, err := platform.DB().Exec(ctx, `UPDATE ops.workspace_runtime
		SET heartbeat_at=clock_timestamp()+interval '10 minutes',version=version+1
		WHERE role=$1 AND instance_id=$2 AND version=$3`, string(record.Role), string(record.InstanceID), record.Version); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("inject future heartbeat rows=%d error=%v", tag.RowsAffected(), err)
	}
	snapshot, err := repository.ControlSnapshot(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Runtimes) != 1 || snapshot.Runtimes[0].Fresh {
		t.Fatalf("future heartbeat snapshot runtimes=%#v", snapshot.Runtimes)
	}
	requireWorkspaceRootGrantPoolReleased(t, platform)
}

func reserveWorkspaceRoot(
	t *testing.T,
	ctx context.Context,
	repository workspaceRootGrantRepository,
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
	repository workspaceRootGrantRepository,
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
	repository workspaceRootGrantRepository,
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
	repository workspaceRootGrantRepository,
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

func workspaceRootGrantSnapshot(t *testing.T, ctx context.Context, repository workspaceRootGrantRepository) domain.ControlSnapshot {
	t.Helper()
	snapshot, err := repository.ControlSnapshot(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func workspaceRootGrantOperation(t *testing.T, ctx context.Context, repository workspaceRootGrantRepository, id foundation.ID) domain.SwitchOperation {
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
