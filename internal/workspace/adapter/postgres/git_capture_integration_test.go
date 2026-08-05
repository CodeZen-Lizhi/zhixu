//go:build integration

package workspacepostgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryGitCaptureCheckpointReplayOrderAndSourceReappearance(t *testing.T) {
	repository, database, ctx := newGitCaptureTestRepository(t)
	now := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	workspaceID := foundation.ID("71000000-0000-4000-8000-000000000001")
	seedGitCaptureWorkspaceAndRuns(t, ctx, database.DB(), workspaceID, now)
	firstRunID := foundation.ID("72000000-0000-4000-8000-000000000001")
	firstBefore := strings.Repeat("a", 40)
	firstAfter := strings.Repeat("b", 40)
	first := domain.GitCaptureBatch{
		WorkspaceID: workspaceID, RunID: firstRunID, BeforeCommit: firstBefore, AfterCommit: firstAfter,
		RequestHash: strings.Repeat("c", 64), Registrations: []domain.SourceRegistration{gitCaptureRegistration(workspaceID, "73000000", "guide.md", strings.Repeat("d", 64), now)},
		ObservedAt: now,
	}
	applied, err := repository.ApplyGitCaptureBatch(ctx, first)
	if err != nil || applied.Replayed || len(applied.Registrations) != 1 || !applied.Registrations[0].Created {
		t.Fatalf("first capture = %#v, %v", applied, err)
	}
	if err := repository.CompleteGitCaptureBatch(ctx, workspaceID, firstRunID, firstBefore, firstAfter); err != nil {
		t.Fatalf("complete first capture: %v (cause: %+v)", err, errors.Unwrap(err))
	}
	replayed, err := repository.ApplyGitCaptureBatch(ctx, first)
	if err != nil || !replayed.Replayed || len(replayed.Registrations) != 1 || replayed.Registrations[0].Source.ID != applied.Registrations[0].Source.ID {
		t.Fatalf("completed replay = %#v, %v", replayed, err)
	}
	if err := repository.CompleteGitCaptureBatch(ctx, workspaceID, firstRunID, firstBefore, firstAfter); err != nil {
		t.Fatalf("completed replay complete = %v", err)
	}

	outOfOrder := first
	outOfOrder.RunID = foundation.ID("72000000-0000-4000-8000-000000000002")
	outOfOrder.AfterCommit = strings.Repeat("e", 40)
	outOfOrder.RequestHash = strings.Repeat("e", 64)
	_, err = repository.ApplyGitCaptureBatch(ctx, outOfOrder)
	requireGitCaptureErrorCode(t, err, "GIT_CAPTURE_OUT_OF_ORDER")

	deleteRunID := foundation.ID("72000000-0000-4000-8000-000000000003")
	deleted, err := repository.ApplyGitCaptureBatch(ctx, domain.GitCaptureBatch{
		WorkspaceID: workspaceID, RunID: deleteRunID, BeforeCommit: firstAfter, AfterCommit: strings.Repeat("c", 40),
		RequestHash: strings.Repeat("f", 64), RemovedPaths: []string{"guide.md"}, ObservedAt: now.Add(time.Minute),
	})
	if err != nil || len(deleted.RemovedSourceIDs) != 1 || deleted.RemovedSourceIDs[0] != applied.Registrations[0].Source.ID {
		t.Fatalf("deleted capture = %#v, %v", deleted, err)
	}
	if err := repository.CompleteGitCaptureBatch(ctx, workspaceID, deleteRunID, firstAfter, strings.Repeat("c", 40)); err != nil {
		t.Fatal(err)
	}

	reappearRunID := foundation.ID("72000000-0000-4000-8000-000000000004")
	reappeared, err := repository.ApplyGitCaptureBatch(ctx, domain.GitCaptureBatch{
		WorkspaceID: workspaceID, RunID: reappearRunID, BeforeCommit: strings.Repeat("c", 40), AfterCommit: strings.Repeat("d", 40),
		RequestHash: strings.Repeat("1", 64), Registrations: []domain.SourceRegistration{gitCaptureRegistration(workspaceID, "74000000", "guide.md", strings.Repeat("2", 64), now.Add(2*time.Minute))},
		ObservedAt: now.Add(2 * time.Minute),
	})
	if err != nil || len(reappeared.Registrations) != 1 || reappeared.Registrations[0].Source.ID != applied.Registrations[0].Source.ID || !reappeared.Registrations[0].Created {
		t.Fatalf("reappeared capture = %#v, %v", reappeared, err)
	}
	var removedAt *time.Time
	if err := database.QueryRow(ctx, `SELECT removed_at FROM core.source WHERE id=$1`, string(applied.Registrations[0].Source.ID)).Scan(&removedAt); err != nil {
		t.Fatal(err)
	}
	if removedAt != nil {
		t.Fatalf("reappeared source remained tombstoned at %s", removedAt)
	}
}

func gitCaptureRegistration(workspaceID foundation.ID, prefix, location, contentHash string, now time.Time) domain.SourceRegistration {
	return domain.SourceRegistration{
		Source:   domain.Source{ID: foundation.ID(prefix + "-0000-4000-8000-000000000001"), WorkspaceID: workspaceID, Type: "markdown", LogicalName: "guide", OriginalLocation: location, CreatedAt: now},
		Artifact: domain.ContentArtifact{ID: foundation.ID(prefix + "-0000-4000-8000-000000000002"), WorkspaceID: workspaceID, ContentHash: contentHash, ByteSize: 5, ManagedLocation: ".knowledge/sources/" + contentHash, CreatedAt: now},
		Version:  domain.SourceVersion{ID: foundation.ID(prefix + "-0000-4000-8000-000000000003"), ContentHash: contentHash, ByteSize: 5, MediaType: "text/markdown", OriginalContentLocation: location, SecurityStatus: "pending", CapturedAt: now},
	}
}

func seedGitCaptureWorkspaceAndRuns(t *testing.T, ctx context.Context, database *pgxpool.Pool, workspaceID foundation.ID, now time.Time) {
	t.Helper()
	if _, err := database.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'Git capture','/tmp/git-capture','/tmp/git-capture',$2,'active',1,$2,$2)`, string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO ops.git_remote_config_revision(
		workspace_id,revision,configured,normalized_url,branch,auto_sync,token_configured,actor,config_created_at,created_at
	) VALUES($1,1,true,'https://git.example.com/acme/guide.git','main',false,false,'test',$2,$2)`, string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO ops.git_remote_config(
		workspace_id,configured,normalized_url,branch,auto_sync,token_configured,revision,created_at,updated_at
	) VALUES($1,true,'https://git.example.com/acme/guide.git','main',false,false,1,$2,$2)`, string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	for ordinal, runID := range []foundation.ID{
		"72000000-0000-4000-8000-000000000001", "72000000-0000-4000-8000-000000000002",
		"72000000-0000-4000-8000-000000000003", "72000000-0000-4000-8000-000000000004",
	} {
		oid := strings.Repeat(string(rune('a'+ordinal)), 40)
		if _, err := database.Exec(ctx, `INSERT INTO ops.git_sync_run(
			id,workspace_id,config_revision,remote_url,branch,trigger,idempotency_key,request_hash,status,direction,failure_class,
			verified_head_oid,verified_remote_oid,index_status,version,created_at,updated_at,completed_at
		) VALUES($1,$2,1,'https://git.example.com/acme/guide.git','main','MANUAL',$3,$4,'SUCCEEDED','PULL','NONE',$5,$5,'PENDING',1,$6,$6,$6)`,
			string(runID), string(workspaceID), fmt.Sprintf("git-capture-%d", ordinal), strings.Repeat(fmt.Sprintf("%x", ordinal+1), 64), oid, now.Add(time.Duration(ordinal)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
}

func requireGitCaptureErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error = %#v, want code %q", err, code)
	}
}

func newGitCaptureTestRepository(t *testing.T) (*Repository, *platformpostgres.Pool, context.Context) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for Git capture integration tests")
	}
	ctx := context.Background()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_git_capture_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	databaseURL := parsed.String()
	migrationPool, err := platformpostgres.OpenMigration(ctx, databaseURL, 4, 0)
	if err == nil {
		runner, runnerErr := platformmigration.NewRunner(migrationPool.DB(), projectmigrations.FS)
		if runnerErr == nil {
			runnerErr = runner.Up(ctx)
		}
		migrationPool.Close()
		err = runnerErr
	}
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	database, err := platformpostgres.Open(ctx, databaseURL, 4, 0)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	repository, err := NewRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	return repository, database, ctx
}
