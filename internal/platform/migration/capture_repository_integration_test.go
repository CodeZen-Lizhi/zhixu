//go:build integration

package migration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCaptureRepositoryConcurrentCreateExactReplayAndAtomicFacts(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabaseWithMaxConns(t, ctx, 12)
	defer cleanup()
	if _, err := migrationProvider(t, pool).UpTo(ctx, 68); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)
	const workspaceID foundation.ID = "68100000-0000-4000-8000-000000000001"
	insertCaptureWorkspace(t, ctx, pool, workspaceID, now)

	repository, err := capturepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := captureapp.NewService(captureapp.Dependencies{
		Repository: repository, Content: captureIntegrationContent{}, IDs: foundation.NewUUIDGenerator(nil),
		Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	results := make(chan captureapp.CreateResult, workers)
	errorsChannel := make(chan error, workers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, createErr := service.CreateText(ctx, captureapp.TextCommand{
				WorkspaceID: workspaceID, DisplayName: "Java AI", Text: "Spring AI knowledge",
				IdempotencyKey: "capture-concurrent-create",
			})
			if createErr != nil {
				errorsChannel <- createErr
				return
			}
			results <- result
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	for createErr := range errorsChannel {
		t.Fatalf("concurrent create: %v", createErr)
	}
	var captureID foundation.ID
	createdCount := 0
	for result := range results {
		if captureID == "" {
			captureID = result.Capture.ID
		}
		if result.Capture.ID != captureID {
			t.Fatalf("capture ids differ: got %s want %s", result.Capture.ID, captureID)
		}
		if !result.Replayed {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created responses = %d, want 1", createdCount)
	}
	for table, want := range map[string]int{
		"core.capture": 1, "core.capture_command": 1, "core.source": 1,
		"core.source_version": 1, "core.content_artifact": 1, "ops.capture_outbox": 1,
	} {
		var count int
		if err := pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE workspace_id=$1", table), string(workspaceID)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s count=%d want=%d", table, count, want)
		}
	}

	_, err = service.CreateText(ctx, captureapp.TextCommand{
		WorkspaceID: workspaceID, DisplayName: "Java AI", Text: "different content",
		IdempotencyKey: "capture-concurrent-create",
	})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "CAPTURE_IDEMPOTENCY_CONFLICT" || classified.Kind != foundation.ErrorVersionConflict {
		t.Fatalf("idempotency conflict = %#v", err)
	}
	persisted, err := repository.Get(ctx, workspaceID, captureID)
	if err != nil || persisted.DisplayName != "Java AI" {
		t.Fatalf("Get() capture=%#v error=%v", persisted, err)
	}
	page, err := repository.List(ctx, captureapp.ListQuery{WorkspaceID: workspaceID, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != captureID {
		t.Fatalf("List() page=%#v error=%v", page, err)
	}
}

func TestCaptureRepositoryRetryExactReplayAndAtomicOutbox(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabaseWithMaxConns(t, ctx, 8)
	defer cleanup()
	if _, err := migrationProvider(t, pool).UpTo(ctx, 68); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)
	const workspaceID foundation.ID = "68200000-0000-4000-8000-000000000001"
	insertCaptureWorkspace(t, ctx, pool, workspaceID, now)

	repository, err := capturepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := captureapp.NewService(captureapp.Dependencies{
		Repository: repository, Content: captureIntegrationContent{}, IDs: foundation.NewUUIDGenerator(nil),
		Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateURL(ctx, captureapp.URLCommand{
		WorkspaceID: workspaceID, DisplayName: "Retry URL", URL: "https://example.com/retry",
		IdempotencyKey: "capture-retry-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.capture SET
		status='FETCH_FAILED',fetch_status='FAILED',failure_stage='FETCH',error_code='CAPTURE_FETCH_TIMEOUT',
		retryable=true,version=version+1,updated_at=$3 WHERE workspace_id=$1 AND id=$2`,
		string(workspaceID), string(created.Capture.ID), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	command := captureapp.RetryCommand{
		WorkspaceID: workspaceID, CaptureID: created.Capture.ID, ExpectedVersion: 2,
		IdempotencyKey: "capture-retry-command",
	}
	retryService, err := captureapp.NewService(captureapp.Dependencies{
		Repository: repository, Content: captureIntegrationContent{}, IDs: foundation.NewUUIDGenerator(nil),
		Clock: foundation.FixedClock{Value: now.Add(2 * time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	retried, err := retryService.Retry(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Replayed || retried.Capture.Status != "RECEIVED" || retried.Capture.FetchStatus != "PENDING" ||
		retried.Capture.Version != 3 || retried.Capture.Retryable {
		t.Fatalf("retry result=%#v", retried)
	}
	replay, err := retryService.Retry(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Capture.ID != retried.Capture.ID ||
		replay.Capture.Version != retried.Capture.Version || replay.Capture.Status != retried.Capture.Status ||
		!replay.Capture.CapturedAt.Equal(retried.Capture.CapturedAt) || !replay.Capture.UpdatedAt.Equal(retried.Capture.UpdatedAt) {
		t.Fatalf("replay=%#v want=%#v", replay, retried)
	}

	for table, want := range map[string]int{
		"core.capture": 1, "core.capture_command": 2, "ops.capture_outbox": 2,
	} {
		var count int
		if err := pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE workspace_id=$1", table), string(workspaceID)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s count=%d want=%d", table, count, want)
		}
	}

	_, err = retryService.Retry(ctx, captureapp.RetryCommand{
		WorkspaceID: workspaceID, CaptureID: created.Capture.ID, ExpectedVersion: 3,
		IdempotencyKey: command.IdempotencyKey,
	})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "CAPTURE_IDEMPOTENCY_CONFLICT" || classified.Kind != foundation.ErrorVersionConflict {
		t.Fatalf("idempotency conflict=%#v", err)
	}
}

func TestCaptureRepositoryAllowsAttemptOneInSuccessiveWorkflowRuns(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabaseWithMaxConns(t, ctx, 8)
	defer cleanup()
	if _, err := migrationProvider(t, pool).UpTo(ctx, 68); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC)
	const (
		workspaceID     foundation.ID = "68300000-0000-4000-8000-000000000001"
		definitionID                  = "68300000-0000-4000-8000-000000000002"
		firstRunID                    = "68300000-0000-4000-8000-000000000003"
		secondRunID                   = "68300000-0000-4000-8000-000000000004"
		firstAttemptID                = "68300000-0000-4000-8000-000000000005"
		secondAttemptID               = "68300000-0000-4000-8000-000000000006"
	)
	insertCaptureWorkspace(t, ctx, pool, workspaceID, now)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,'capture-process',1,'{"nodes":[]}',$3)`, definitionID, string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
		VALUES($1,$3,$2,'running','{}',1,$5,$5),($4,$3,$2,'running','{}',1,$5,$5)`,
		firstRunID, definitionID, string(workspaceID), secondRunID, now); err != nil {
		t.Fatal(err)
	}

	repository, err := capturepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := captureapp.NewService(captureapp.Dependencies{
		Repository: repository, Content: captureIntegrationContent{}, IDs: foundation.NewUUIDGenerator(nil),
		Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateText(ctx, captureapp.TextCommand{
		WorkspaceID: workspaceID, DisplayName: "Attempt scope", Text: "workflow scoped attempt",
		IdempotencyKey: "capture-attempt-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstCapture, firstAttempt, err := repository.BeginAttempt(ctx, captureapp.BeginAttemptRequest{
		ID: firstAttemptID, WorkspaceID: workspaceID, CaptureID: created.Capture.ID,
		WorkflowRunID: firstRunID, AttemptNumber: 1, StartedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	failedCapture, _, err := repository.FailAttempt(ctx, captureapp.AttemptFailure{
		Attempt: firstAttempt, ExpectedCaptureVersion: firstCapture.Version,
		Stage: capturedomain.AttemptStageIngestion, Code: "CAPTURE_INGESTION_TEMPORARY_FAILURE",
		Retryable: true, FailedAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	retryService, err := captureapp.NewService(captureapp.Dependencies{
		Repository: repository, Content: captureIntegrationContent{}, IDs: foundation.NewUUIDGenerator(nil),
		Clock: foundation.FixedClock{Value: now.Add(3 * time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	retried, err := retryService.Retry(ctx, captureapp.RetryCommand{
		WorkspaceID: workspaceID, CaptureID: created.Capture.ID, ExpectedVersion: failedCapture.Version,
		IdempotencyKey: "capture-attempt-retry",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondCapture, secondAttempt, err := repository.BeginAttempt(ctx, captureapp.BeginAttemptRequest{
		ID: secondAttemptID, WorkspaceID: workspaceID, CaptureID: created.Capture.ID,
		WorkflowRunID: secondRunID, AttemptNumber: 1, StartedAt: now.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondAttempt.AttemptNumber != 1 || secondAttempt.WorkflowRunID != secondRunID || secondCapture.Version != retried.Capture.Version+1 {
		t.Fatalf("second workflow attempt=%#v capture=%#v", secondAttempt, secondCapture)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.capture_attempt WHERE workspace_id=$1 AND capture_id=$2`,
		string(workspaceID), string(created.Capture.ID)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("capture attempt count=%d want=2", count)
	}
}

type captureIntegrationContent struct{}

func (captureIntegrationContent) StageManagedBytes(_ context.Context, _ foundation.ID, sourceRef string, content []byte, hash string) (workspacedomain.ManagedContentStage, error) {
	return workspacedomain.ManagedContentStage{
		ContentHash: hash, ByteSize: int64(len(content)), ManagedLocation: ".knowledge/sources/" + hash,
		StagingLocation: ".knowledge/sources/.staging/" + sourceRef,
	}, nil
}

func (captureIntegrationContent) PublishManagedBytes(_ context.Context, _ foundation.ID, stage workspacedomain.ManagedContentStage) (workspacedomain.ContentCapture, error) {
	return workspacedomain.ContentCapture{
		ContentHash: stage.ContentHash, ByteSize: stage.ByteSize,
		ManagedLocation: stage.ManagedLocation, Created: true,
	}, nil
}

func (captureIntegrationContent) DiscardManagedBytes(context.Context, foundation.ID, workspacedomain.ManagedContentStage) error {
	return nil
}

func insertCaptureWorkspace(t *testing.T, ctx context.Context, database *pgxpool.Pool, workspaceID foundation.ID, now time.Time) {
	t.Helper()
	if _, err := database.Exec(ctx, `INSERT INTO core.workspace(
id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
status,availability,availability_reason,availability_checked_at,version,created_at,updated_at)
VALUES($1,'capture-repository-test','/tmp/capture-repository-test',$2,1,
'/tmp/capture-repository-test',$3,'inactive','available',NULL,$3,1,$3,$3)`,
		string(workspaceID), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", now); err != nil {
		t.Fatal(err)
	}
}
