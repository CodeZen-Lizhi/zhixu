package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitoperation"
)

const (
	executorWorkspaceID foundation.ID = "76100000-0000-4000-8000-000000000001"
	executorRunID       foundation.ID = "76100000-0000-4000-8000-000000000002"
	executorAttemptID   foundation.ID = "76100000-0000-4000-8000-000000000003"
)

func TestRunExecutorFastForwardsAndLeavesIndexPending(t *testing.T) {
	store := newExecutorStore(domain.RunPending)
	remote := &remoteStub{
		comparison: domain.Comparison{
			Attached: true, Branch: "main", HeadOID: oid("1"), RemoteOID: oid("2"), WorktreeClean: true,
			Relation: domain.RelationRemoteAhead, Behind: 1,
			ChangedFiles: []domain.FileChange{{Path: "docs/note.md", Kind: domain.FileModified}},
		},
		mutation:     domain.MutationResult{ResultKnown: true},
		verification: domain.Verification{HeadOID: oid("2"), RemoteOID: oid("2"), WorktreeClean: true},
	}
	executor := mustExecutor(t, store, remote)
	run, err := executor.Execute(context.Background(), executorWorkspaceID, executorRunID, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunSucceeded || run.Direction != domain.DirectionPull || run.IndexStatus != domain.IndexPending {
		t.Fatalf("run=%#v", run)
	}
	if remote.fetches != 1 || remote.fastForwards != 1 || remote.pushes != 0 || remote.verifies != 1 {
		t.Fatalf("remote calls fetch=%d ff=%d push=%d verify=%d", remote.fetches, remote.fastForwards, remote.pushes, remote.verifies)
	}
}

func TestRunExecutorRetriesTransientPostCheckWithoutRepeatingPull(t *testing.T) {
	store := newExecutorStore(domain.RunPending)
	remote := &remoteStub{
		comparison: domain.Comparison{
			Attached: true, Branch: "main", HeadOID: oid("1"), RemoteOID: oid("2"), WorktreeClean: true,
			Relation: domain.RelationRemoteAhead, Behind: 1,
			ChangedFiles: []domain.FileChange{{Path: "docs/note.md", Kind: domain.FileModified}},
		},
		mutation:  domain.MutationResult{ResultKnown: true},
		verifyErr: foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeOffline, true, errors.New("offline")),
	}
	executor := mustExecutor(t, store, remote)
	run, err := executor.Execute(context.Background(), executorWorkspaceID, executorRunID, "worker-1")
	if err == nil || run.Status != domain.RunVerifying {
		t.Fatalf("run=%#v err=%v", run, err)
	}
	if remote.fastForwards != 1 || remote.verifies != 1 {
		t.Fatalf("first delivery repeated calls: %#v", remote)
	}

	remote.verifyErr = nil
	remote.verification = domain.Verification{HeadOID: oid("2"), RemoteOID: oid("2"), WorktreeClean: true}
	run, err = executor.Execute(context.Background(), executorWorkspaceID, executorRunID, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunSucceeded || run.IndexStatus != domain.IndexPending {
		t.Fatalf("run=%#v", run)
	}
	if remote.fastForwards != 1 || remote.verifies != 2 {
		t.Fatalf("verification replayed mutation: %#v", remote)
	}
}

func TestRunExecutorStopsDirtyWithoutMutation(t *testing.T) {
	store := newExecutorStore(domain.RunPending)
	remote := &remoteStub{comparison: domain.Comparison{
		Attached: true, Branch: "main", HeadOID: oid("1"), RemoteOID: oid("2"), WorktreeClean: false,
		Relation: domain.RelationLocalAhead, Ahead: 1,
	}}
	executor := mustExecutor(t, store, remote)
	run, err := executor.Execute(context.Background(), executorWorkspaceID, executorRunID, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunConflict || run.FailureClass != domain.FailureDirty {
		t.Fatalf("run=%#v", run)
	}
	if remote.fastForwards != 0 || remote.pushes != 0 || remote.verifies != 0 {
		t.Fatal("dirty worktree reached a Git mutation")
	}
}

func TestRunExecutorReconcilesRestartWithoutRepeatingMutation(t *testing.T) {
	store := newExecutorStore(domain.RunVerifying)
	store.run.Direction = domain.DirectionPush
	store.run.ExpectedHeadOID = oid("2")
	store.run.ExpectedRemoteOID = oid("1")
	store.attempt.Phase = domain.RunVerifying
	remote := &remoteStub{verification: domain.Verification{HeadOID: oid("2"), RemoteOID: oid("2"), WorktreeClean: true}}
	executor := mustExecutor(t, store, remote)
	run, err := executor.Execute(context.Background(), executorWorkspaceID, executorRunID, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunSucceeded || run.Direction != domain.DirectionPush {
		t.Fatalf("run=%#v", run)
	}
	if remote.fetches != 0 || remote.compares != 0 || remote.fastForwards != 0 || remote.pushes != 0 || remote.verifies != 1 {
		t.Fatalf("restart repeated unsafe work: %#v", remote)
	}
}

func TestRunExecutorMarksUnknownMutationForManualRecovery(t *testing.T) {
	store := newExecutorStore(domain.RunPending)
	remote := &remoteStub{
		comparison: domain.Comparison{
			Attached: true, Branch: "main", HeadOID: oid("1"), RemoteOID: oid("2"), WorktreeClean: true,
			Relation: domain.RelationRemoteAhead, Behind: 1,
		},
		mutation:    domain.MutationResult{ResultKnown: false},
		mutationErr: foundation.NewError(foundation.ErrorManualRecoveryRequired, domain.ErrorCodeResultUnknown, false, errors.New("connection lost")),
		verifyErr:   foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeOffline, true, errors.New("offline")),
	}
	executor := mustExecutor(t, store, remote)
	run, err := executor.Execute(context.Background(), executorWorkspaceID, executorRunID, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunManualRecoveryRequired || run.FailureClass != domain.FailureResultUnknown || run.ErrorCode != domain.ErrorCodeResultUnknown {
		t.Fatalf("run=%#v", run)
	}
}

func TestRunExecutorCancellationLeavesMutationCheckpointForVerification(t *testing.T) {
	store := newExecutorStore(domain.RunPending)
	remote := &remoteStub{
		comparison: domain.Comparison{
			Attached: true, Branch: "main", HeadOID: oid("1"), RemoteOID: oid("2"), WorktreeClean: true,
			Relation: domain.RelationRemoteAhead, Behind: 1,
		},
		mutationErr: context.Canceled,
	}
	executor := mustExecutor(t, store, remote)
	run, err := executor.Execute(context.Background(), executorWorkspaceID, executorRunID, "worker-1")
	if !errors.Is(err, context.Canceled) || run.Status != domain.RunFastForwarding || remote.verifies != 0 {
		t.Fatalf("run=%+v err=%v verifies=%d", run, err, remote.verifies)
	}
	remote.mutationErr = nil
	remote.verification = domain.Verification{HeadOID: oid("2"), RemoteOID: oid("2"), WorktreeClean: true}
	run, err = executor.Execute(context.Background(), executorWorkspaceID, executorRunID, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunSucceeded || remote.fastForwards != 1 || remote.verifies != 1 {
		t.Fatalf("resume repeated mutation: run=%+v remote=%+v", run, remote)
	}
}

func TestRunExecutorTreatsInvalidComparisonAsPreMutationFailure(t *testing.T) {
	store := newExecutorStore(domain.RunPending)
	remote := &remoteStub{}
	executor := mustExecutor(t, store, remote)
	run, err := executor.Execute(context.Background(), executorWorkspaceID, executorRunID, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunFailed || run.FailureClass != domain.FailureInternal || run.ErrorCode != domain.ErrorCodeCorrupt {
		t.Fatalf("run=%+v", run)
	}
	if remote.fastForwards != 0 || remote.pushes != 0 || remote.verifies != 0 {
		t.Fatalf("invalid comparison reached mutation: %+v", remote)
	}
}

func TestRunExecutorDoesNotReportReadOnlyComparisonAsUnknownMutation(t *testing.T) {
	store := newExecutorStore(domain.RunPending)
	remote := &remoteStub{compareErr: foundation.NewError(
		foundation.ErrorConsistencyViolation,
		domain.ErrorCodeResultUnknown,
		false,
		errors.New("invalid read-only comparison"),
	)}
	executor := mustExecutor(t, store, remote)
	run, err := executor.Execute(context.Background(), executorWorkspaceID, executorRunID, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunFailed || run.FailureClass != domain.FailureInternal || run.ErrorCode != domain.ErrorCodeCorrupt {
		t.Fatalf("run=%+v", run)
	}
	if remote.fastForwards != 0 || remote.pushes != 0 || remote.verifies != 0 {
		t.Fatalf("read-only comparison failure reached mutation: %+v", remote)
	}
}

func mustExecutor(t *testing.T, store *executorStore, remote *remoteStub) *RunExecutor {
	t.Helper()
	executor, err := NewRunExecutor(configStoreStub{}, store, remote, immediateGitLocker{}, staticIDGenerator{value: executorAttemptID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func oid(character string) string { return strings.Repeat(character, 40) }

type staticIDGenerator struct{ value foundation.ID }

func (generator staticIDGenerator) New() (foundation.ID, error) { return generator.value, nil }

type immediateGitLocker struct{}

func (immediateGitLocker) Acquire(context.Context, foundation.ID) (gitoperation.Lease, error) {
	return immediateGitLease{}, nil
}

type immediateGitLease struct{}

func (immediateGitLease) Release(context.Context) error { return nil }

type configStoreStub struct{}

func (configStoreStub) GetConfig(context.Context, foundation.ID) (domain.RemoteConfig, error) {
	return domain.RemoteConfig{}, nil
}
func (configStoreStub) ReplayConfig(context.Context, ReplayConfigCommand) (ConfigReceipt, bool, error) {
	return ConfigReceipt{}, false, nil
}
func (configStoreStub) SaveConfig(context.Context, PersistConfigCommand) (ConfigReceipt, error) {
	return ConfigReceipt{}, nil
}
func (configStoreStub) DeleteConfig(context.Context, DeleteConfigCommand) (ConfigReceipt, error) {
	return ConfigReceipt{}, nil
}
func (configStoreStub) OpenCredential(context.Context, foundation.ID, int64) (domain.Token, error) {
	return domain.NewToken("test-token")
}

type executorStore struct {
	run     domain.SyncRun
	attempt domain.SyncAttempt
}

func newExecutorStore(status domain.RunStatus) *executorStore {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	run := domain.SyncRun{
		ID: executorRunID, WorkspaceID: executorWorkspaceID, ConfigRevision: 1,
		RemoteURL: "https://git.example.test/team/notes.git", Branch: "main", Trigger: domain.TriggerManual,
		IdempotencyKey: "manual-1", RequestHash: strings.Repeat("a", 64), Status: status,
		Direction: domain.DirectionUnknown, FailureClass: domain.FailureNone, IndexStatus: domain.IndexNotRequired,
		AttemptCount: 0, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	attempt := domain.SyncAttempt{
		ID: executorAttemptID, WorkspaceID: executorWorkspaceID, RunID: executorRunID,
		AttemptNo: 1, Status: domain.AttemptRunning, Phase: status,
		LeaseOwner: "worker-1", LeaseExpiresAt: now.Add(time.Minute), StartedAt: now, Version: 1,
	}
	return &executorStore{run: run, attempt: attempt}
}

func (store *executorStore) CreateRun(context.Context, CreateRunRecord) (domain.SyncRun, bool, error) {
	return domain.SyncRun{}, false, nil
}
func (store *executorStore) ReplayRun(context.Context, ReplayRunCommand) (domain.SyncRun, bool, error) {
	return domain.SyncRun{}, false, nil
}
func (store *executorStore) ReplayIndex(context.Context, RetryIndexCommand) (domain.SyncRun, bool, error) {
	return domain.SyncRun{}, false, nil
}
func (store *executorStore) GetRun(context.Context, foundation.ID, foundation.ID) (domain.SyncRun, error) {
	return store.run, nil
}
func (store *executorStore) GetCurrentRun(context.Context, foundation.ID) (domain.SyncRun, bool, error) {
	return store.run, true, nil
}
func (store *executorStore) ListRuns(context.Context, ListRunsQuery) (RunPage, error) {
	return RunPage{}, nil
}
func (store *executorStore) BeginAttempt(_ context.Context, command BeginAttemptCommand) (BeginAttemptResult, error) {
	if store.run.Status == domain.RunPending {
		store.run.Status = domain.RunFetching
		store.run.AttemptCount = 1
		store.run.Version++
		store.attempt.Phase = domain.RunFetching
	}
	store.attempt.LeaseOwner = command.Owner
	return BeginAttemptResult{Run: store.run, Attempt: store.attempt, Started: true}, nil
}
func (store *executorStore) TransitionRun(_ context.Context, command TransitionRunCommand) (domain.SyncRun, domain.SyncAttempt, error) {
	if store.run.Version != command.ExpectedRunVersion || store.run.Status != command.ExpectedStatus || store.attempt.Version != command.ExpectedAttemptVersion {
		return domain.SyncRun{}, domain.SyncAttempt{}, errors.New("fake transition CAS")
	}
	store.run.Status, store.run.Direction = command.NextStatus, command.Direction
	if command.ExpectedHeadOID != "" || command.ChangedFiles != nil {
		store.run.ExpectedHeadOID, store.run.ExpectedRemoteOID = command.ExpectedHeadOID, command.ExpectedRemoteOID
		store.run.ChangedFiles = append([]domain.FileChange(nil), command.ChangedFiles...)
	}
	store.run.Version++
	store.attempt.Phase = command.NextStatus
	if command.ResultKnown != nil {
		value := *command.ResultKnown
		store.attempt.ResultKnown = &value
	}
	store.attempt.Version++
	return store.run, store.attempt, nil
}
func (store *executorStore) CompleteRun(_ context.Context, command CompleteRunCommand) (domain.SyncRun, error) {
	if store.run.Version != command.ExpectedRunVersion || store.run.Status != command.ExpectedStatus || store.attempt.Version != command.ExpectedAttemptVersion {
		return domain.SyncRun{}, errors.New("fake completion CAS")
	}
	store.run.Status, store.run.Direction = command.Status, command.Direction
	store.run.FailureClass, store.run.ErrorCode, store.run.Retryable = command.FailureClass, command.ErrorCode, command.Retryable
	if command.ExpectedHeadOID != "" || command.ChangedFiles != nil {
		store.run.ExpectedHeadOID, store.run.ExpectedRemoteOID = command.ExpectedHeadOID, command.ExpectedRemoteOID
		store.run.ChangedFiles = append([]domain.FileChange(nil), command.ChangedFiles...)
	}
	store.run.VerifiedHeadOID, store.run.VerifiedRemoteOID = command.VerifiedHeadOID, command.VerifiedRemoteOID
	store.run.IndexStatus = command.IndexStatus
	completed := store.run.UpdatedAt.Add(time.Second)
	store.run.CompletedAt, store.run.UpdatedAt = &completed, completed
	store.run.Version++
	return store.run, nil
}
func (store *executorStore) RetryIndex(context.Context, RetryIndexCommand) (domain.SyncRun, bool, error) {
	return domain.SyncRun{}, false, nil
}

type remoteStub struct {
	comparison      domain.Comparison
	mutation        domain.MutationResult
	verification    domain.Verification
	mutationErr     error
	compareErr      error
	verifyErr       error
	fetches         int
	compares        int
	fastForwards    int
	pushes          int
	verifies        int
	testConnections int
	testAccess      GitAccess
}

func (remote *remoteStub) TestConnection(_ context.Context, access GitAccess) error {
	remote.testConnections++
	remote.testAccess = access
	return nil
}
func (remote *remoteStub) Fetch(context.Context, GitAccess) error {
	remote.fetches++
	return nil
}
func (remote *remoteStub) Compare(context.Context, GitAccess) (domain.Comparison, error) {
	remote.compares++
	return remote.comparison, remote.compareErr
}
func (remote *remoteStub) FastForward(context.Context, FastForwardCommand) (domain.MutationResult, error) {
	remote.fastForwards++
	return remote.mutation, remote.mutationErr
}
func (remote *remoteStub) Push(context.Context, PushCommand) (domain.MutationResult, error) {
	remote.pushes++
	return remote.mutation, remote.mutationErr
}
func (remote *remoteStub) Verify(context.Context, VerifyCommand) (domain.Verification, error) {
	remote.verifies++
	return remote.verification, remote.verifyErr
}
