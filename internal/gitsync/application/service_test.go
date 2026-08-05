package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

type replayConfigStore struct {
	configStoreStub
	receipt   ConfigReceipt
	replayErr error
	replays   int
	gets      int
}

func (store *replayConfigStore) ReplayConfig(_ context.Context, _ ReplayConfigCommand) (ConfigReceipt, bool, error) {
	store.replays++
	return store.receipt, store.replayErr == nil, store.replayErr
}

func (store *replayConfigStore) GetConfig(context.Context, foundation.ID) (domain.RemoteConfig, error) {
	store.gets++
	return domain.RemoteConfig{}, errors.New("current configuration must not be read during replay")
}

type replayRunStore struct {
	*executorStore
	replayed domain.SyncRun
	err      error
	replays  int
	gets     int
	commands []ReplayRunCommand
}

type replayIndexStore struct {
	*executorStore
	replayed domain.SyncRun
	replays  int
	gets     int
}

func (store *replayIndexStore) ReplayIndex(_ context.Context, _ RetryIndexCommand) (domain.SyncRun, bool, error) {
	store.replays++
	return store.replayed, true, nil
}

func (store *replayIndexStore) GetRun(context.Context, foundation.ID, foundation.ID) (domain.SyncRun, error) {
	store.gets++
	return domain.SyncRun{}, errors.New("current run must not be read during index retry replay")
}

func (store *replayRunStore) ReplayRun(_ context.Context, command ReplayRunCommand) (domain.SyncRun, bool, error) {
	store.replays++
	store.commands = append(store.commands, command)
	return store.replayed, store.err == nil, store.err
}

func (store *replayRunStore) GetRun(context.Context, foundation.ID, foundation.ID) (domain.SyncRun, error) {
	store.gets++
	return domain.SyncRun{}, errors.New("current run must not be read during Git retry replay")
}

type countingPolicy struct{ calls int }

func (policy *countingPolicy) NormalizeHTTPSRemote(context.Context, string) (string, error) {
	policy.calls++
	return "", errors.New("DNS policy must not run during replay")
}

type passthroughPolicy struct{}

func (passthroughPolicy) NormalizeHTTPSRemote(_ context.Context, value string) (string, error) {
	return value, nil
}

type testConfigStore struct {
	configStoreStub
	config domain.RemoteConfig
	opens  int
}

func (store *testConfigStore) GetConfig(context.Context, foundation.ID) (domain.RemoteConfig, error) {
	return store.config, nil
}

func (store *testConfigStore) OpenCredential(context.Context, foundation.ID, int64) (domain.Token, error) {
	store.opens++
	return domain.NewToken("test-token")
}

type countingIDGenerator struct{ calls int }

func (generator *countingIDGenerator) New() (foundation.ID, error) {
	generator.calls++
	return executorAttemptID, nil
}

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

func TestSaveConfigReplaysBeforeDNSPolicy(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	historical := domain.RemoteConfig{
		WorkspaceID: executorWorkspaceID, Configured: true,
		RemoteURL: "https://git.example.test/team/notes.git", Branch: "main", TokenConfigured: true,
		Revision: 2, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	configs := &replayConfigStore{receipt: ConfigReceipt{Config: historical, Replayed: true}}
	policy := &countingPolicy{}
	service := &Service{configs: configs, policy: policy}
	action, err := domain.ReplaceSecret("replay-token")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.SaveConfig(context.Background(), SaveConfigCommand{
		WorkspaceID: executorWorkspaceID, ExpectedRevision: 1,
		RemoteURL: "https://git.example.test/team/notes.git", Branch: "main",
		SecretAction: action, IdempotencyKey: "save-replay", Actor: "test:actor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Replayed || receipt.Config.Revision != 2 || configs.replays != 1 || policy.calls != 0 {
		t.Fatalf("receipt=%+v replay_calls=%d policy_calls=%d", receipt, configs.replays, policy.calls)
	}
}

func TestCreateRunReplaysBeforeCurrentConfigAndIDAllocation(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	completed := now.Add(time.Second)
	replayed := domain.SyncRun{
		ID: executorRunID, WorkspaceID: executorWorkspaceID, ConfigRevision: 1,
		RemoteURL: "https://git.example.test/team/notes.git", Branch: "main", Trigger: domain.TriggerManual,
		IdempotencyKey: "manual-replay", RequestHash: strings.Repeat("a", 64), Status: domain.RunFailed,
		Direction: domain.DirectionUnknown, FailureClass: domain.FailureOffline, ErrorCode: domain.ErrorCodeOffline,
		Retryable: true, IndexStatus: domain.IndexNotRequired, Version: 2,
		CreatedAt: now, UpdatedAt: completed, CompletedAt: &completed,
	}
	configs := &replayConfigStore{}
	runs := &replayRunStore{executorStore: newExecutorStore(domain.RunPending), replayed: replayed}
	ids := &countingIDGenerator{}
	service := &Service{configs: configs, runs: runs, ids: ids, clock: fixedClock{value: now}}
	receipt, err := service.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceID: executorWorkspaceID, Trigger: domain.TriggerManual, IdempotencyKey: "manual-replay",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Replayed || receipt.Run.ID != executorRunID || runs.replays != 1 || configs.gets != 0 || ids.calls != 0 {
		t.Fatalf("receipt=%+v replay_calls=%d config_gets=%d id_calls=%d", receipt, runs.replays, configs.gets, ids.calls)
	}
}

func TestCreateAutomaticRunRequestHashBindsExpectedConfigRevision(t *testing.T) {
	runs := &replayRunStore{executorStore: newExecutorStore(domain.RunPending)}
	service := &Service{configs: &replayConfigStore{}, runs: runs, ids: &countingIDGenerator{}, clock: fixedClock{}}
	for _, revision := range []int64{4, 5} {
		_, err := service.CreateRun(context.Background(), CreateRunCommand{
			WorkspaceID: executorWorkspaceID, Trigger: domain.TriggerAutomatic,
			ExpectedConfigRevision: revision, IdempotencyKey: "auto-writeback:commit",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(runs.commands) != 2 || len(runs.commands[0].RequestHash) != 64 || len(runs.commands[1].RequestHash) != 64 ||
		runs.commands[0].RequestHash == runs.commands[1].RequestHash {
		t.Fatalf("replay commands=%+v", runs.commands)
	}
}

func TestCreateAutomaticRunRequiresCurrentAutoSyncOptIn(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	configs := configuredRunConfigStore{config: domain.RemoteConfig{
		WorkspaceID: executorWorkspaceID, Configured: true,
		RemoteURL: "https://git.example.test/team/notes.git", Branch: "main", TokenConfigured: true,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}}
	runs := &createCountingRunStore{executorStore: newExecutorStore(domain.RunPending)}
	service := &Service{configs: configs, runs: runs, ids: staticIDGenerator{value: executorRunID}, clock: fixedClock{value: now}}
	_, err := service.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceID: executorWorkspaceID, Trigger: domain.TriggerAutomatic,
		ExpectedConfigRevision: 1, IdempotencyKey: autoSyncKeyPrefix + string(autoCommitA),
	})
	if code, ok := errorCode(err); !ok || code != domain.ErrorCodeAutoSyncDisabled {
		t.Fatalf("err=%v code=%q", err, code)
	}
	if runs.creates != 0 {
		t.Fatalf("disabled automatic run reached persistence: %d", runs.creates)
	}
}

func TestCreateAutomaticRunRejectsStaleCandidateRevisionBeforeIDAllocation(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	configs := configuredRunConfigStore{config: domain.RemoteConfig{
		WorkspaceID: executorWorkspaceID, Configured: true,
		RemoteURL: "https://git.example.test/team/notes.git", Branch: "main", TokenConfigured: true, AutoSync: true,
		Revision: 5, CreatedAt: now, UpdatedAt: now,
	}}
	runs := &createCountingRunStore{executorStore: newExecutorStore(domain.RunPending)}
	ids := &countingIDGenerator{}
	service := &Service{configs: configs, runs: runs, ids: ids, clock: fixedClock{value: now}}
	_, err := service.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceID: executorWorkspaceID, Trigger: domain.TriggerAutomatic,
		ExpectedConfigRevision: 4, IdempotencyKey: autoSyncKeyPrefix + string(autoCommitA) + ":config:4",
	})
	if code, ok := errorCode(err); !ok || code != domain.ErrorCodeConfigStale {
		t.Fatalf("err=%v code=%q", err, code)
	}
	if ids.calls != 0 || runs.creates != 0 {
		t.Fatalf("stale automatic run allocated or persisted: id_calls=%d creates=%d", ids.calls, runs.creates)
	}
}

type configuredRunConfigStore struct {
	configStoreStub
	config domain.RemoteConfig
}

func (store configuredRunConfigStore) GetConfig(context.Context, foundation.ID) (domain.RemoteConfig, error) {
	return store.config, nil
}

type createCountingRunStore struct {
	*executorStore
	creates int
}

func (store *createCountingRunStore) CreateRun(context.Context, CreateRunRecord) (domain.SyncRun, bool, error) {
	store.creates++
	return domain.SyncRun{}, false, nil
}

func TestRetryRunReplaysIndexReceiptBeforeCurrentState(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	completed := now.Add(time.Second)
	replayed := domain.SyncRun{
		ID: executorRunID, WorkspaceID: executorWorkspaceID, ConfigRevision: 1,
		RemoteURL: "https://git.example.test/team/notes.git", Branch: "main", Trigger: domain.TriggerManual,
		IdempotencyKey: "manual-1", RequestHash: strings.Repeat("a", 64), Status: domain.RunSucceeded,
		Direction: domain.DirectionPull, FailureClass: domain.FailureNone, IndexStatus: domain.IndexPending,
		Version: 7, CreatedAt: now, UpdatedAt: completed, CompletedAt: &completed,
		VerifiedHeadOID: oid("2"), VerifiedRemoteOID: oid("2"),
	}
	runs := &replayIndexStore{executorStore: newExecutorStore(domain.RunPending), replayed: replayed}
	service := &Service{runs: runs}
	receipt, err := service.RetryRun(context.Background(), RetryRunCommand{
		WorkspaceID: executorWorkspaceID, RunID: executorRunID, ExpectedVersion: 5, IdempotencyKey: "index-retry-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Replayed || receipt.Run.Version != 7 || runs.replays != 1 || runs.gets != 0 {
		t.Fatalf("receipt=%+v replay_calls=%d get_calls=%d", receipt, runs.replays, runs.gets)
	}
}

func TestRetryRunReplaysGitRunBeforeCurrentState(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	replayed := domain.SyncRun{
		ID: executorRunID, WorkspaceID: executorWorkspaceID, ConfigRevision: 1,
		RemoteURL: "https://git.example.test/team/notes.git", Branch: "main", Trigger: domain.TriggerRetry,
		RetryOfRunID:   "76100000-0000-4000-8000-000000000004",
		IdempotencyKey: "git-retry-1", RequestHash: strings.Repeat("b", 64), Status: domain.RunPending,
		Direction: domain.DirectionUnknown, FailureClass: domain.FailureNone, IndexStatus: domain.IndexNotRequired,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	runs := &replayRunStore{executorStore: newExecutorStore(domain.RunPending), replayed: replayed}
	service := &Service{runs: runs}
	receipt, err := service.RetryRun(context.Background(), RetryRunCommand{
		WorkspaceID: executorWorkspaceID, RunID: replayed.RetryOfRunID, ExpectedVersion: 4, IdempotencyKey: "git-retry-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Replayed || receipt.Run.ID != executorRunID || runs.replays != 1 || runs.gets != 0 {
		t.Fatalf("receipt=%+v replay_calls=%d get_calls=%d", receipt, runs.replays, runs.gets)
	}
}

func TestTestConfigRejectsInvalidBranchBeforeAdapters(t *testing.T) {
	policy := &countingPolicy{}
	remote := &remoteStub{}
	service := &Service{configs: configStoreStub{}, policy: policy, remote: remote}
	action, err := domain.ReplaceSecret("test-token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.TestConfig(context.Background(), TestConfigCommand{
		WorkspaceID: executorWorkspaceID, RemoteURL: "https://git.example.test/team/notes.git",
		Branch: "feature..invalid", SecretAction: action,
	})
	if code, ok := errorCode(err); !ok || code != domain.ErrorCodeBranchInvalid {
		t.Fatalf("err=%v code=%q", err, code)
	}
	if policy.calls != 0 || remote.fetches != 0 || remote.compares != 0 {
		t.Fatalf("invalid branch reached adapters: policy=%d remote=%+v", policy.calls, remote)
	}
}

func TestTestConfigUsesTemporaryPositiveRevisionForUnconfiguredWorkspace(t *testing.T) {
	remote := &remoteStub{}
	service := &Service{configs: configStoreStub{}, policy: passthroughPolicy{}, remote: remote}
	action, err := domain.ReplaceSecret("test-token")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.TestConfig(context.Background(), TestConfigCommand{
		WorkspaceID: executorWorkspaceID, ExpectedRevision: 0,
		RemoteURL: "https://git.example.test/team/notes.git", Branch: "main", SecretAction: action,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RemoteURL != "https://git.example.test/team/notes.git" || remote.testConnections != 1 ||
		remote.testAccess.WorkspaceID != executorWorkspaceID || remote.testAccess.ConfigRevision != 1 {
		t.Fatalf("result=%+v calls=%d access=%+v", result, remote.testConnections, remote.testAccess)
	}
}

func TestTestConfigKeepRejectsCredentialBindingChanges(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	current := domain.RemoteConfig{
		WorkspaceID: executorWorkspaceID, Configured: true,
		RemoteURL: "https://git.example.test/team/notes.git", Branch: "main", TokenConfigured: true,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	for _, test := range []struct {
		name      string
		remoteURL string
		branch    string
	}{
		{name: "remote URL", remoteURL: "https://git.example.test/team/other.git", branch: "main"},
		{name: "branch", remoteURL: current.RemoteURL, branch: "release"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configs := &testConfigStore{config: current}
			service := &Service{configs: configs, policy: passthroughPolicy{}, remote: &remoteStub{}}
			_, err := service.TestConfig(context.Background(), TestConfigCommand{
				WorkspaceID: executorWorkspaceID, ExpectedRevision: current.Revision,
				RemoteURL: test.remoteURL, Branch: test.branch, SecretAction: domain.KeepSecret(),
			})
			if code, ok := errorCode(err); !ok || code != domain.ErrorCodeConfigRevisionConflict {
				t.Fatalf("err=%v code=%q", err, code)
			}
			if configs.opens != 0 {
				t.Fatalf("changed credential binding opened the stored token %d times", configs.opens)
			}
		})
	}
}
