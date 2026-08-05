//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/adapter/security"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
)

const (
	postgresWorkspaceA foundation.ID = "71000000-0000-4000-8000-000000000001"
	postgresWorkspaceB foundation.ID = "71000000-0000-4000-8000-000000000002"
	postgresWorkspaceC foundation.ID = "71000000-0000-4000-8000-000000000003"
	postgresRunA       foundation.ID = "72000000-0000-4000-8000-000000000001"
	postgresRunB       foundation.ID = "72000000-0000-4000-8000-000000000002"
	postgresRunC       foundation.ID = "72000000-0000-4000-8000-000000000003"
	postgresAttemptA   foundation.ID = "73000000-0000-4000-8000-000000000001"
	postgresAttemptB   foundation.ID = "73000000-0000-4000-8000-000000000002"
)

func TestRepositoryConfigReplayFencingAndActiveRunUniqueness(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newGitSyncTestDatabase(t, ctx)
	defer cleanup()
	seedWorkspace(t, ctx, pool, postgresWorkspaceA, "git-sync-a")

	sealer, err := security.NewCredentialSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := postgres.NewRepository(pool, sealer)
	if err != nil {
		t.Fatal(err)
	}

	first := saveConfig(t, ctx, repository, postgresWorkspaceA, 0, "save-1", strings.Repeat("a", 64), domain.SecretActionReplace, "initial-token")
	if first.Config.Revision != 1 || !first.Config.TokenConfigured {
		t.Fatalf("first config=%+v", first.Config)
	}
	opened, err := repository.OpenCredential(ctx, postgresWorkspaceA, 1)
	if err != nil {
		t.Fatal(err)
	}
	openedBytes := opened.Bytes()
	opened.Destroy()
	if string(openedBytes) != "initial-token" {
		clear(openedBytes)
		t.Fatalf("opened token=%q", openedBytes)
	}
	clear(openedBytes)

	for _, test := range []struct {
		name      string
		remoteURL string
		branch    string
		key       string
		hash      string
	}{
		{
			name: "remote URL", remoteURL: "https://git.example.com/team/other.git", branch: "main",
			key: "save-keep-other-remote", hash: strings.Repeat("8", 64),
		},
		{
			name: "branch", remoteURL: "https://git.example.com/team/repo.git", branch: "release",
			key: "save-keep-other-branch", hash: strings.Repeat("9", 64),
		},
	} {
		t.Run("keep rejects changed "+test.name, func(t *testing.T) {
			_, saveErr := repository.SaveConfig(ctx, application.PersistConfigCommand{
				WorkspaceID: postgresWorkspaceA, ExpectedRevision: 1,
				RemoteURL: test.remoteURL, Branch: test.branch, SecretAction: domain.KeepSecret(),
				IdempotencyKey: test.key, RequestHash: test.hash, Actor: "test:integration",
			})
			requireCode(t, saveErr, domain.ErrorCodeConfigRevisionConflict)
			unchanged, getErr := repository.GetConfig(ctx, postgresWorkspaceA)
			if getErr != nil || unchanged.Revision != 1 || unchanged.RemoteURL != "https://git.example.com/team/repo.git" || unchanged.Branch != "main" {
				t.Fatalf("changed binding modified config=%+v err=%v", unchanged, getErr)
			}
		})
	}

	replayed, found, err := repository.ReplayConfig(ctx, application.ReplayConfigCommand{
		WorkspaceID: postgresWorkspaceA, IdempotencyKey: "save-1", RequestHash: strings.Repeat("a", 64), CommandType: "SAVE",
	})
	if err != nil || !found || !replayed.Config.Configured || replayed.Config.Revision != 1 {
		t.Fatalf("replay found=%t receipt=%+v err=%v", found, replayed, err)
	}
	_, _, err = repository.ReplayConfig(ctx, application.ReplayConfigCommand{
		WorkspaceID: postgresWorkspaceA, IdempotencyKey: "save-1", RequestHash: strings.Repeat("b", 64), CommandType: "SAVE",
	})
	requireCode(t, err, domain.ErrorCodeIdempotencyConflict)

	second := saveConfig(t, ctx, repository, postgresWorkspaceA, 1, "save-2", strings.Repeat("b", 64), domain.SecretActionKeep, "")
	if second.Config.Revision != 2 || !second.Config.TokenConfigured || second.Config.CreatedAt != first.Config.CreatedAt {
		t.Fatalf("kept config first=%+v second=%+v", first.Config, second.Config)
	}
	staleCandidate := pendingRun(postgresRunC, postgresWorkspaceA, first.Config.Revision, "run-stale-config", "f")
	if _, _, err := repository.CreateRun(ctx, application.CreateRunRecord{Run: staleCandidate}); err == nil {
		t.Fatal("run created from a configuration snapshot that changed after service validation")
	} else {
		requireCode(t, err, domain.ErrorCodeConfigStale)
	}
	automatic := pendingRun(postgresRunC, postgresWorkspaceA, 2, "auto-disabled", "f")
	automatic.Trigger = domain.TriggerAutomatic
	if _, _, err := repository.CreateRun(ctx, application.CreateRunRecord{Run: automatic}); err == nil {
		t.Fatal("automatic run was created while auto_sync was disabled")
	} else {
		requireCode(t, err, domain.ErrorCodeAutoSyncDisabled)
	}
	created, replay, err := repository.CreateRun(ctx, application.CreateRunRecord{Run: pendingRun(postgresRunA, postgresWorkspaceA, 2, "run-1", "c")})
	if err != nil || replay || created.Status != domain.RunPending {
		t.Fatalf("created=%+v replay=%t err=%v", created, replay, err)
	}

	third := saveConfig(t, ctx, repository, postgresWorkspaceA, 2, "save-3", strings.Repeat("d", 64), domain.SecretActionKeep, "")
	if third.Config.Revision != 3 {
		t.Fatalf("third config=%+v", third.Config)
	}
	stale, err := repository.GetRun(ctx, postgresWorkspaceA, postgresRunA)
	if err != nil || stale.Status != domain.RunStale || stale.FailureClass != domain.FailureStaleConfig {
		t.Fatalf("stale run=%+v err=%v", stale, err)
	}

	type createResult struct {
		run domain.SyncRun
		err error
	}
	start := make(chan struct{})
	results := make(chan createResult, 2)
	for _, candidate := range []domain.SyncRun{
		pendingRun(postgresRunB, postgresWorkspaceA, 3, "run-2", "e"),
		pendingRun(postgresRunC, postgresWorkspaceA, 3, "run-3", "f"),
	} {
		candidate := candidate
		go func() {
			<-start
			value, _, createErr := repository.CreateRun(ctx, application.CreateRunRecord{Run: candidate})
			results <- createResult{run: value, err: createErr}
		}()
	}
	close(start)
	var winner domain.SyncRun
	conflicts := 0
	for range 2 {
		result := <-results
		if result.err == nil {
			winner = result.run
			continue
		}
		requireCode(t, result.err, domain.ErrorCodeRunActive)
		conflicts++
	}
	if winner.ID == "" || conflicts != 1 {
		t.Fatalf("winner=%+v conflicts=%d", winner, conflicts)
	}

	claimed, err := repository.BeginAttempt(ctx, application.BeginAttemptCommand{
		WorkspaceID: postgresWorkspaceA, RunID: winner.ID, AttemptID: postgresAttemptA,
		Owner: "integration-worker", LeaseDuration: time.Minute,
	})
	if err != nil || !claimed.Started || claimed.Run.Status != domain.RunFetching {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	action := domain.KeepSecret()
	_, err = repository.SaveConfig(ctx, application.PersistConfigCommand{
		WorkspaceID: postgresWorkspaceA, ExpectedRevision: 3,
		RemoteURL: "https://git.example.com/team/repo.git", Branch: "main", SecretAction: action,
		IdempotencyKey: "save-active", RequestHash: strings.Repeat("1", 64), Actor: "test:integration",
	})
	requireCode(t, err, domain.ErrorCodeRunActive)

	if _, err := pool.Exec(ctx, `UPDATE ops.git_remote_config SET revision=revision+1,updated_at=clock_timestamp() WHERE workspace_id=$1`, string(postgresWorkspaceA)); err == nil {
		t.Fatal("direct current configuration mutation bypassed revision projection guard")
	} else {
		requirePostgresCode(t, err, "55000")
	}
	var validChangedFiles bool
	if err := pool.QueryRow(ctx, `SELECT ops.valid_git_sync_changed_files($1::jsonb)`, `[{"path":"../secret","kind":"MODIFIED"}]`).Scan(&validChangedFiles); err != nil {
		t.Fatal(err)
	}
	if validChangedFiles {
		t.Fatal("unsafe changed-file metadata passed the database constraint function")
	}
}

func TestRepositoryAutoSyncCandidateQueryIsBoundedOnEmptyFacts(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newGitSyncTestDatabase(t, ctx)
	defer cleanup()
	sealer, err := security.NewCredentialSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := postgres.NewRepository(pool, sealer)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := repository.ListAutoSyncCandidates(ctx, application.MaxAutoSyncBatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates=%+v", candidates)
	}
}

func TestRepositoryAutoSyncCandidatesRespectCompletionTimeAndRunReplay(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newGitSyncTestDatabase(t, ctx)
	defer cleanup()
	seedWorkspace(t, ctx, pool, postgresWorkspaceA, "git-sync-auto")

	sealer, err := security.NewCredentialSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := postgres.NewRepository(pool, sealer)
	if err != nil {
		t.Fatal(err)
	}
	first := saveConfig(t, ctx, repository, postgresWorkspaceA, 0, "save-auto-off", strings.Repeat("a", 64), domain.SecretActionReplace, "auto-token")
	var firstConfiguredAt time.Time
	if err := pool.QueryRow(ctx, `SELECT created_at FROM ops.git_remote_config_revision WHERE workspace_id=$1 AND revision=$2`, string(postgresWorkspaceA), first.Config.Revision).Scan(&firstConfiguredAt); err != nil {
		t.Fatal(err)
	}
	lateCommitID := foundation.ID("74000000-0000-4000-8000-000000000001")
	seedAutoSyncWriteback(t, ctx, pool, postgresWorkspaceA, foundation.ID("74100000-0000-4000-8000-000000000001"), lateCommitID, firstConfiguredAt.Add(time.Microsecond))

	if _, err := pool.Exec(ctx, `SELECT pg_sleep(0.005)`); err != nil {
		t.Fatal(err)
	}
	second, err := repository.SaveConfig(ctx, application.PersistConfigCommand{
		WorkspaceID: postgresWorkspaceA, ExpectedRevision: first.Config.Revision,
		RemoteURL: "https://git.example.com/team/repo.git", Branch: "main", AutoSync: true,
		SecretAction: domain.KeepSecret(), IdempotencyKey: "save-auto-on", RequestHash: strings.Repeat("b", 64), Actor: "test:integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	var secondConfiguredAt time.Time
	if err := pool.QueryRow(ctx, `SELECT created_at FROM ops.git_remote_config_revision WHERE workspace_id=$1 AND revision=$2`, string(postgresWorkspaceA), second.Config.Revision).Scan(&secondConfiguredAt); err != nil {
		t.Fatal(err)
	}
	eligibleCommitID := foundation.ID("75000000-0000-4000-8000-000000000002")
	seedAutoSyncWriteback(t, ctx, pool, postgresWorkspaceA, foundation.ID("75100000-0000-4000-8000-000000000002"), eligibleCommitID, secondConfiguredAt.Add(time.Microsecond))

	candidates, err := repository.ListAutoSyncCandidates(ctx, application.MaxAutoSyncBatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].WorkspaceID != postgresWorkspaceA || candidates[0].ProposalCommitID != eligibleCommitID ||
		candidates[0].ConfigRevision != second.Config.Revision {
		t.Fatalf("candidates=%+v", candidates)
	}
	automaticKey := "auto-writeback:" + string(eligibleCommitID) + ":config:" + strconv.FormatInt(second.Config.Revision, 10)
	automatic := pendingRun(postgresRunA, postgresWorkspaceA, second.Config.Revision, automaticKey, "c")
	automatic.Trigger = domain.TriggerAutomatic
	if _, replayed, err := repository.CreateRun(ctx, application.CreateRunRecord{Run: automatic}); err != nil || replayed {
		t.Fatalf("automatic run replayed=%t err=%v", replayed, err)
	}
	candidates, err = repository.ListAutoSyncCandidates(ctx, application.MaxAutoSyncBatch)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("post-run candidates=%+v err=%v", candidates, err)
	}
}

func seedAutoSyncWriteback(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, executionID, commitID foundation.ID, completedAt time.Time) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	prefix := strings.ReplaceAll(string(commitID), "-", "")[:8]
	gitOID := strings.Repeat(string(prefix[1]), 40)
	proposalID := prefix + "-0000-4000-8000-000000000011"
	revisionID := prefix + "-0000-4000-8000-000000000012"
	approvalID := prefix + "-0000-4000-8000-000000000013"
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.writeback_execution(
		id,workspace_id,workflow_run_id,node_run_id,proposal_id,revision_id,approval_id,write_authorization_id,git_authorization_id,
		target_path,base_hash,result_hash,approved_change_hash,approved_git_head,git_commit,parent_git_commit,diff_hash,status,
		idempotency_key,version,created_at,updated_at,completed_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'notes/auto.md',repeat('a',64),repeat('b',64),repeat('c',64),repeat('d',40),
		$13,repeat('d',40),repeat('f',64),'completed',$10,1,$11,$12,$12)`,
		string(executionID), string(workspaceID), prefix+"-0000-4000-8000-000000000003", prefix+"-0000-4000-8000-000000000004",
		proposalID, revisionID, approvalID, prefix+"-0000-4000-8000-000000000008", prefix+"-0000-4000-8000-000000000009",
		"auto-writeback-fixture-"+prefix, completedAt.Add(-time.Minute), completedAt, gitOID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal_commit(
		id,workspace_id,writeback_execution_id,proposal_id,revision_id,approval_id,git_commit,parent_git_commit,target_path,diff_hash,result_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$8,repeat('d',40),'notes/auto.md',repeat('f',64),repeat('b',64),$7)`,
		string(commitID), string(workspaceID), string(executionID), proposalID, revisionID, approvalID, completedAt, gitOID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryPublishesWorkspaceScopedServerEventsWithoutRemoteSecrets(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newGitSyncTestDatabase(t, ctx)
	defer cleanup()
	seedWorkspace(t, ctx, pool, postgresWorkspaceA, "git-sync-events")

	sealer, err := security.NewCredentialSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := postgres.NewRepository(pool, sealer)
	if err != nil {
		t.Fatal(err)
	}
	configReceipt := saveConfig(t, ctx, repository, postgresWorkspaceA, 0, "save-events", strings.Repeat("a", 64), domain.SecretActionReplace, "server-event-secret")
	run, replayed, err := repository.CreateRun(ctx, application.CreateRunRecord{
		Run: pendingRun(postgresRunA, postgresWorkspaceA, configReceipt.Config.Revision, "run-events", "b"),
	})
	if err != nil || replayed {
		t.Fatalf("run=%+v replayed=%t err=%v", run, replayed, err)
	}
	started, err := repository.BeginAttempt(ctx, application.BeginAttemptCommand{
		WorkspaceID: postgresWorkspaceA, RunID: postgresRunA, AttemptID: postgresAttemptA,
		Owner: "integration-worker", LeaseDuration: time.Minute,
	})
	if err != nil || !started.Started || started.Run.Version != 2 {
		t.Fatalf("started=%+v err=%v", started, err)
	}

	assertGitSyncServerEvent(t, ctx, pool,
		"git.remote.updated:"+string(postgresWorkspaceA)+":v1",
		"git.remote.updated", "git_remote:"+string(postgresWorkspaceA), 1, "configured", "",
	)
	assertGitSyncServerEvent(t, ctx, pool,
		"git.sync.run.updated:"+string(postgresRunA)+":v1",
		"git.sync.run.updated", "git_sync_run:"+string(postgresRunA), 1, "pending", "not-required",
	)
	assertGitSyncServerEvent(t, ctx, pool,
		"git.sync.run.updated:"+string(postgresRunA)+":v2",
		"git.sync.run.updated", "git_sync_run:"+string(postgresRunA), 2, "fetching", "not-required",
	)
}

func assertGitSyncServerEvent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	sourceEventRef, expectedType, expectedResource string,
	expectedVersion int64,
	expectedStatus, expectedStage string,
) {
	t.Helper()
	var eventType, resourceRef, status, stage, payload string
	var version int64
	err := pool.QueryRow(ctx, `SELECT event_type,resource_ref,resource_version,
		payload_summary->>'status',COALESCE(payload_summary->>'stage',''),payload_summary::text
		FROM ops.server_event WHERE workspace_id=$1 AND source_event_ref=$2`,
		string(postgresWorkspaceA), sourceEventRef).Scan(&eventType, &resourceRef, &version, &status, &stage, &payload)
	if err != nil {
		t.Fatal(err)
	}
	if eventType != expectedType || resourceRef != expectedResource || version != expectedVersion ||
		status != expectedStatus || stage != expectedStage {
		t.Fatalf("event type=%q resource=%q version=%d status=%q stage=%q", eventType, resourceRef, version, status, stage)
	}
	for _, forbidden := range []string{"server-event-secret", "git.example.com", "team/repo.git", strings.Repeat("a", 40)} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("server event payload leaked remote material: %s", payload)
		}
	}
}

func TestRepositoryClearSecretPreservesConfigButBlocksRuns(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newGitSyncTestDatabase(t, ctx)
	defer cleanup()
	seedWorkspace(t, ctx, pool, postgresWorkspaceB, "git-sync-b")
	sealer, err := security.NewCredentialSealer(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := postgres.NewRepository(pool, sealer)
	if err != nil {
		t.Fatal(err)
	}
	saveConfig(t, ctx, repository, postgresWorkspaceB, 0, "save-b-1", strings.Repeat("a", 64), domain.SecretActionReplace, "temporary-token")
	cleared := saveConfig(t, ctx, repository, postgresWorkspaceB, 1, "save-b-2", strings.Repeat("b", 64), domain.SecretActionClear, "")
	if !cleared.Config.Configured || cleared.Config.TokenConfigured || cleared.Config.Revision != 2 {
		t.Fatalf("cleared config=%+v", cleared.Config)
	}
	_, err = repository.OpenCredential(ctx, postgresWorkspaceB, 2)
	requireCode(t, err, domain.ErrorCodeConfigStale)
	if _, err := pool.Exec(ctx, `INSERT INTO ops.git_remote_credential(
		workspace_id,config_revision,key_id,nonce,ciphertext,aad_digest,created_at,updated_at
	) VALUES($1,2,'test-key',$2,$3,$4,clock_timestamp(),clock_timestamp())`,
		string(postgresWorkspaceB), bytes.Repeat([]byte{1}, 12), bytes.Repeat([]byte{2}, 17), strings.Repeat("a", 64)); err == nil {
		t.Fatal("credential was accepted while token_configured was false")
	} else {
		requirePostgresCode(t, err, "55000")
	}
	_, _, err = repository.CreateRun(ctx, application.CreateRunRecord{Run: pendingRun(postgresRunA, postgresWorkspaceB, 2, "run-b-1", "c")})
	requireCode(t, err, domain.ErrorCodeSecretUnavailable)
}

func TestRepositoryPoisonConvergesRunAndReleasesActiveSlot(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newGitSyncTestDatabase(t, ctx)
	defer cleanup()
	seedWorkspace(t, ctx, pool, postgresWorkspaceB, "git-sync-poison")
	sealer, err := security.NewCredentialSealer(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := postgres.NewRepository(pool, sealer)
	if err != nil {
		t.Fatal(err)
	}
	saveConfig(t, ctx, repository, postgresWorkspaceB, 0, "save-poison-1", strings.Repeat("a", 64), domain.SecretActionReplace, "temporary-token")

	_, _, err = repository.CreateRun(ctx, application.CreateRunRecord{Run: pendingRun(postgresRunA, postgresWorkspaceB, 1, "run-poison-1", "b")})
	if err != nil {
		t.Fatal(err)
	}
	lease, found, err := repository.ClaimNext(ctx, "integration-worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("lease=%+v found=%t err=%v", lease, found, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.git_sync_outbox SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, string(lease.ID)); err != nil {
		t.Fatal(err)
	}
	requireCode(t, repository.Reschedule(ctx, lease, domain.ErrorCodeOffline, time.Second), domain.ErrorCodeLeaseLost)
	requireCode(t, repository.Poison(ctx, lease, domain.ErrorCodeCorrupt), domain.ErrorCodeLeaseLost)
	unchanged, err := repository.GetRun(ctx, postgresWorkspaceB, postgresRunA)
	if err != nil || unchanged.Status != domain.RunPending {
		t.Fatalf("expired lease changed run=%+v err=%v", unchanged, err)
	}
	lease, found, err = repository.ClaimNext(ctx, "integration-worker-2", time.Minute)
	if err != nil || !found {
		t.Fatalf("reclaimed lease=%+v found=%t err=%v", lease, found, err)
	}
	if err := repository.Poison(ctx, lease, domain.ErrorCodeCorrupt); err != nil {
		t.Fatal(err)
	}
	failed, err := repository.GetRun(ctx, postgresWorkspaceB, postgresRunA)
	if err != nil || failed.Status != domain.RunFailed || failed.FailureClass != domain.FailureInternal || failed.Retryable {
		t.Fatalf("poisoned pending run=%+v err=%v", failed, err)
	}

	second, _, err := repository.CreateRun(ctx, application.CreateRunRecord{Run: pendingRun(postgresRunB, postgresWorkspaceB, 1, "run-poison-2", "c")})
	if err != nil {
		t.Fatal(err)
	}
	lease, found, err = repository.ClaimNext(ctx, "integration-worker", time.Minute)
	if err != nil || !found || lease.RunID != second.ID {
		t.Fatalf("lease=%+v found=%t err=%v", lease, found, err)
	}
	begin, err := repository.BeginAttempt(ctx, application.BeginAttemptCommand{
		WorkspaceID: postgresWorkspaceB, RunID: second.ID, AttemptID: postgresAttemptA,
		Owner: "integration-worker", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, attempt := begin.Run, begin.Attempt
	run, attempt, err = repository.TransitionRun(ctx, application.TransitionRunCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: "integration-worker",
		ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version,
		ExpectedStatus: domain.RunFetching, NextStatus: domain.RunComparing,
		Direction: domain.DirectionUnknown, LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err = repository.TransitionRun(ctx, application.TransitionRunCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: "integration-worker",
		ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version,
		ExpectedStatus: domain.RunComparing, NextStatus: domain.RunPushing, Direction: domain.DirectionPush,
		ExpectedHeadOID: strings.Repeat("2", 40), ExpectedRemoteOID: strings.Repeat("1", 40),
		ChangedFiles: []domain.FileChange{}, LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Poison(ctx, lease, domain.ErrorCodeCorrupt); err != nil {
		t.Fatal(err)
	}
	manual, err := repository.GetRun(ctx, postgresWorkspaceB, postgresRunB)
	if err != nil || manual.Status != domain.RunManualRecoveryRequired || manual.FailureClass != domain.FailureResultUnknown || manual.ErrorCode != domain.ErrorCodeResultUnknown {
		t.Fatalf("poisoned mutation run=%+v err=%v", manual, err)
	}
	var attemptStatus, attemptPhase, attemptError string
	var attemptOwner *string
	if err := pool.QueryRow(ctx, `SELECT status,phase,error_code,lease_owner FROM ops.git_sync_attempt WHERE id=$1`, string(postgresAttemptA)).Scan(
		&attemptStatus, &attemptPhase, &attemptError, &attemptOwner,
	); err != nil {
		t.Fatal(err)
	}
	if attemptStatus != string(domain.AttemptFailed) || attemptPhase != string(domain.RunManualRecoveryRequired) ||
		attemptError != domain.ErrorCodeResultUnknown || attemptOwner != nil {
		t.Fatalf("attempt status=%s phase=%s error=%s owner=%v", attemptStatus, attemptPhase, attemptError, attemptOwner)
	}
	if _, _, err := repository.CreateRun(ctx, application.CreateRunRecord{Run: pendingRun(postgresRunC, postgresWorkspaceB, 1, "run-poison-3", "d")}); err != nil {
		t.Fatalf("poisoned run kept active slot: %v", err)
	}
}

func TestRepositoryPersistsAttemptsOutboxAndIndependentIndexFailure(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newGitSyncTestDatabase(t, ctx)
	defer cleanup()
	seedWorkspace(t, ctx, pool, postgresWorkspaceC, "git-sync-c")
	sealer, err := security.NewCredentialSealer(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := postgres.NewRepository(pool, sealer)
	if err != nil {
		t.Fatal(err)
	}
	saveConfig(t, ctx, repository, postgresWorkspaceC, 0, "save-c-1", strings.Repeat("a", 64), domain.SecretActionReplace, "sync-token")
	run, _, err := repository.CreateRun(ctx, application.CreateRunRecord{Run: pendingRun(postgresRunA, postgresWorkspaceC, 1, "run-c-1", "b")})
	if err != nil {
		t.Fatal(err)
	}
	executeLease, found, err := repository.ClaimNext(ctx, "integration-worker", time.Minute)
	if err != nil || !found || executeLease.Kind != domain.OutboxExecuteRun || executeLease.RunID != run.ID {
		t.Fatalf("execute lease=%+v found=%t err=%v", executeLease, found, err)
	}
	begin, err := repository.BeginAttempt(ctx, application.BeginAttemptCommand{
		WorkspaceID: postgresWorkspaceC, RunID: run.ID, AttemptID: postgresAttemptB,
		Owner: "integration-worker", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, attempt := begin.Run, begin.Attempt
	run, attempt, err = repository.TransitionRun(ctx, application.TransitionRunCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: "integration-worker",
		ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version,
		ExpectedStatus: domain.RunFetching, NextStatus: domain.RunComparing,
		Direction: domain.DirectionUnknown, LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	headOID := strings.Repeat("1", 40)
	remoteOID := strings.Repeat("2", 40)
	changes := []domain.FileChange{{Path: "notes/new.md", OldPath: "notes/old.md", Kind: domain.FileRenamed}}
	run, attempt, err = repository.TransitionRun(ctx, application.TransitionRunCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: "integration-worker",
		ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version,
		ExpectedStatus: domain.RunComparing, NextStatus: domain.RunFastForwarding, Direction: domain.DirectionPull,
		ExpectedHeadOID: headOID, ExpectedRemoteOID: remoteOID, ChangedFiles: changes, LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	known := true
	run, attempt, err = repository.TransitionRun(ctx, application.TransitionRunCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: "integration-worker",
		ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version,
		ExpectedStatus: domain.RunFastForwarding, NextStatus: domain.RunVerifying, Direction: domain.DirectionPull,
		ExpectedHeadOID: headOID, ExpectedRemoteOID: remoteOID, ChangedFiles: changes,
		ResultKnown: &known, LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err = repository.CompleteRun(ctx, application.CompleteRunCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: "integration-worker",
		ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version, ExpectedStatus: domain.RunVerifying,
		Status: domain.RunSucceeded, Direction: domain.DirectionPull,
		ExpectedHeadOID: headOID, ExpectedRemoteOID: remoteOID, ChangedFiles: changes,
		FailureClass: domain.FailureNone, VerifiedHeadOID: remoteOID, VerifiedRemoteOID: remoteOID,
		IndexStatus: domain.IndexPending, ResultKnown: &known,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunSucceeded || run.IndexStatus != domain.IndexPending || len(run.ChangedFiles) != 1 || run.ChangedFiles[0] != changes[0] {
		t.Fatalf("completed run=%+v", run)
	}
	if err := repository.MarkPublished(ctx, executeLease); err != nil {
		t.Fatal(err)
	}
	indexLease, found, err := repository.ClaimNext(ctx, "integration-worker", time.Minute)
	if err != nil || !found || indexLease.Kind != domain.OutboxIndexFollowup || indexLease.RunID != run.ID {
		t.Fatalf("index lease=%+v found=%t err=%v", indexLease, found, err)
	}
	run, started, err := repository.BeginIndexFollowup(ctx, indexLease, run.Version)
	if err != nil || !started || run.IndexStatus != domain.IndexRunning {
		t.Fatalf("running follow-up=%+v started=%t err=%v", run, started, err)
	}
	run, err = repository.FailIndexFollowup(ctx, indexLease, run.Version, domain.ErrorCodeIndexFailed, true)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunSucceeded || run.IndexStatus != domain.IndexFailed || !run.IndexRetryable {
		t.Fatalf("failed follow-up rewrote Git result: %+v", run)
	}
	blockedRetry := application.RetryIndexCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, ExpectedVersion: run.Version,
		IdempotencyKey: "index-retry-1", RequestHash: strings.Repeat("c", 64),
	}
	_, _, err = repository.RetryIndex(ctx, blockedRetry)
	requireCode(t, err, domain.ErrorCodeRunTransition)
	unchanged, err := repository.GetRun(ctx, run.WorkspaceID, run.ID)
	if err != nil || unchanged.IndexStatus != domain.IndexFailed || unchanged.Version != run.Version {
		t.Fatalf("blocked retry changed run=%+v err=%v", unchanged, err)
	}
	if err := repository.MarkPublished(ctx, indexLease); err != nil {
		t.Fatal(err)
	}
	retried, replayed, err := repository.RetryIndex(ctx, application.RetryIndexCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, ExpectedVersion: run.Version,
		IdempotencyKey: "index-retry-1", RequestHash: strings.Repeat("c", 64),
	})
	if err != nil || replayed || retried.Status != domain.RunSucceeded || retried.IndexStatus != domain.IndexPending {
		t.Fatalf("retried=%+v replayed=%t err=%v", retried, replayed, err)
	}
	replayedRun, replayed, err := repository.RetryIndex(ctx, application.RetryIndexCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, ExpectedVersion: run.Version,
		IdempotencyKey: "index-retry-1", RequestHash: strings.Repeat("c", 64),
	})
	if err != nil || !replayed || replayedRun.ID != run.ID {
		t.Fatalf("retry replay=%+v replayed=%t err=%v", replayedRun, replayed, err)
	}
	retryLease, found, err := repository.ClaimNext(ctx, "integration-worker", time.Minute)
	if err != nil || !found || retryLease.Kind != domain.OutboxIndexFollowup {
		t.Fatalf("retry lease=%+v found=%t err=%v", retryLease, found, err)
	}
	run, started, err = repository.BeginIndexFollowup(ctx, retryLease, retried.Version)
	if err != nil || !started || run.IndexStatus != domain.IndexRunning {
		t.Fatalf("retried follow-up=%+v started=%t err=%v", run, started, err)
	}
	run, err = repository.CompleteIndexFollowup(ctx, retryLease, run.Version, "", true)
	if err != nil || run.IndexStatus != domain.IndexSucceeded || run.IndexVersionID != "" {
		t.Fatalf("no-index completion=%+v err=%v", run, err)
	}
	if err := repository.MarkPublished(ctx, retryLease); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.BeginIndexFollowup(ctx, retryLease, run.Version); err == nil {
		t.Fatal("published index lease was accepted for a second follow-up")
	}
}

func saveConfig(t *testing.T, ctx context.Context, repository *postgres.Repository, workspaceID foundation.ID, expected int64, key, hash string, kind domain.SecretActionKind, tokenValue string) application.ConfigReceipt {
	t.Helper()
	var action domain.SecretAction
	switch kind {
	case domain.SecretActionKeep:
		action = domain.KeepSecret()
	case domain.SecretActionClear:
		action = domain.ClearSecret()
	case domain.SecretActionReplace:
		var err error
		action, err = domain.ReplaceSecret(tokenValue)
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unsupported secret action %q", kind)
	}
	receipt, err := repository.SaveConfig(ctx, application.PersistConfigCommand{
		WorkspaceID: workspaceID, ExpectedRevision: expected,
		RemoteURL: "https://git.example.com/team/repo.git", Branch: "main", SecretAction: action,
		IdempotencyKey: key, RequestHash: hash, Actor: "test:integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func pendingRun(id, workspaceID foundation.ID, revision int64, key, hashCharacter string) domain.SyncRun {
	now := time.Now().UTC()
	return domain.SyncRun{
		ID: id, WorkspaceID: workspaceID, ConfigRevision: revision,
		RemoteURL: "https://git.example.com/team/repo.git", Branch: "main", Trigger: domain.TriggerManual,
		IdempotencyKey: key, RequestHash: strings.Repeat(hashCharacter, 64), Status: domain.RunPending,
		Direction: domain.DirectionUnknown, FailureClass: domain.FailureNone, IndexStatus: domain.IndexNotRequired,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func seedWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id foundation.ID, name string) {
	t.Helper()
	root := "/tmp/" + name
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,clock_timestamp(),'active',1,clock_timestamp(),clock_timestamp())`, string(id), name, root); err != nil {
		t.Fatal(err)
	}
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%v code=%q, want %q", err, func() string {
			if classified == nil {
				return ""
			}
			return classified.Code
		}(), code)
	}
}

func requirePostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	var postgresError interface{ SQLState() string }
	if !errors.As(err, &postgresError) || postgresError.SQLState() != code {
		t.Fatalf("PostgreSQL error=%v, want SQLSTATE %s", err, code)
	}
}

func newGitSyncTestDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("ZHIXU_TEST_DATABASE_URL is required for Git sync integration tests")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_gitsync_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	runner, err := platformmigration.NewRunner(pool, projectmigrations.FS)
	if err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	}
}
