//go:build integration

package migration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
)

type captureRepositoryIntegrationRepository interface {
	captureapp.Repository
	captureapp.OutboxStore
	captureapp.ProcessingRepository
}

func withCaptureRepositoryIntegration(
	t *testing.T,
	test func(*testing.T, *platformpostgres.Pool, context.Context, captureRepositoryIntegrationRepository),
) {
	t.Helper()
	t.Run("gorm", func(t *testing.T) {
		fixture := testdb.Require(t, testdb.Config{
			Availability: testdb.FailWhenUnavailable,
			MaxConns:     12,
		})
		platform := fixture.Pool()
		test(t, platform, context.Background(), openGORMCaptureRepositoryIntegration(t, platform))
	})
}

func openGORMCaptureRepositoryIntegration(
	t *testing.T,
	platform *platformpostgres.Pool,
) captureRepositoryIntegrationRepository {
	t.Helper()
	workspaceRepository, err := workspacepostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := capturepostgres.NewGORMRepository(platform, workspaceRepository)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func TestCaptureRepositoryConcurrentCreateExactReplayAndAtomicFacts(t *testing.T) {
	withCaptureRepositoryIntegration(t, testCaptureRepositoryConcurrentCreateExactReplayAndAtomicFacts)
}

func testCaptureRepositoryConcurrentCreateExactReplayAndAtomicFacts(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	repository captureRepositoryIntegrationRepository,
) {
	pool := platform.DB()
	now := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)
	const workspaceID foundation.ID = "68100000-0000-4000-8000-000000000001"
	insertCaptureWorkspace(t, ctx, pool, workspaceID, now)

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
	withCaptureRepositoryIntegration(t, testCaptureRepositoryRetryExactReplayAndAtomicOutbox)
}

func testCaptureRepositoryRetryExactReplayAndAtomicOutbox(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	repository captureRepositoryIntegrationRepository,
) {
	pool := platform.DB()
	now := time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)
	const workspaceID foundation.ID = "68200000-0000-4000-8000-000000000001"
	insertCaptureWorkspace(t, ctx, pool, workspaceID, now)

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
	withCaptureRepositoryIntegration(t, testCaptureRepositoryAllowsAttemptOneInSuccessiveWorkflowRuns)
}

func testCaptureRepositoryAllowsAttemptOneInSuccessiveWorkflowRuns(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	repository captureRepositoryIntegrationRepository,
) {
	pool := platform.DB()
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

func TestCaptureRepositoryOutboxLeaseLifecycle(t *testing.T) {
	withCaptureRepositoryIntegration(t, testCaptureRepositoryOutboxLeaseLifecycle)
}

func testCaptureRepositoryOutboxLeaseLifecycle(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	repository captureRepositoryIntegrationRepository,
) {
	pool := platform.DB()
	now := time.Date(2026, 8, 2, 17, 0, 0, 0, time.UTC)
	const workspaceID foundation.ID = "68400000-0000-4000-8000-000000000001"
	insertCaptureWorkspace(t, ctx, pool, workspaceID, now)

	service, err := captureapp.NewService(captureapp.Dependencies{
		Repository: repository, Content: captureIntegrationContent{}, IDs: foundation.NewUUIDGenerator(nil),
		Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateURL(ctx, captureapp.URLCommand{
		WorkspaceID: workspaceID, DisplayName: "Lease publication", URL: "https://example.com/outbox-publish",
		IdempotencyKey: "capture-outbox-publish",
	})
	if err != nil {
		t.Fatal(err)
	}

	firstLease, found, err := repository.ClaimNext(ctx, "worker-a", 5*time.Minute)
	if err != nil || !found || firstLease.CaptureID != first.Capture.ID || firstLease.AttemptCount != 1 {
		t.Fatalf("first ClaimNext() lease=%#v found=%t error=%v", firstLease, found, err)
	}
	if lease, found, err := repository.ClaimNext(ctx, "worker-b", 5*time.Minute); err != nil || found {
		t.Fatalf("concurrent ClaimNext() lease=%#v found=%t error=%v", lease, found, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.capture_outbox SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, string(firstLease.ID)); err != nil {
		t.Fatal(err)
	}
	reclaimed, found, err := repository.ClaimNext(ctx, "worker-b", 5*time.Minute)
	if err != nil || !found || reclaimed.ID != firstLease.ID || reclaimed.AttemptCount != 2 || reclaimed.Version != firstLease.Version+1 {
		t.Fatalf("reclaimed ClaimNext() lease=%#v found=%t error=%v", reclaimed, found, err)
	}
	requireCaptureFoundationCode(t, repository.MarkPublished(ctx, firstLease, "68400000-0000-4000-8000-000000000002"), "CAPTURE_OUTBOX_LEASE_LOST")
	const workflowRunID foundation.ID = "68400000-0000-4000-8000-000000000003"
	if err := repository.MarkPublished(ctx, reclaimed, workflowRunID); err != nil {
		t.Fatal(err)
	}
	var published, terminalClean, leaseReleased bool
	var persistedRunID string
	var attemptCount int
	if err := pool.QueryRow(ctx, `SELECT published_at IS NOT NULL,poisoned_at IS NULL,
		payload->>'workflow_run_id',attempt_count,lease_owner IS NULL AND lease_expires_at IS NULL
		FROM ops.capture_outbox WHERE id=$1`, string(firstLease.ID)).Scan(
		&published, &terminalClean, &persistedRunID, &attemptCount, &leaseReleased,
	); err != nil {
		t.Fatal(err)
	}
	if !published || !terminalClean || !leaseReleased || persistedRunID != string(workflowRunID) || attemptCount != 2 {
		t.Fatalf("published outbox state published=%t terminal_clean=%t run=%q attempts=%d released=%t",
			published, terminalClean, persistedRunID, attemptCount, leaseReleased)
	}

	retryService, err := captureapp.NewService(captureapp.Dependencies{
		Repository: repository, Content: captureIntegrationContent{}, IDs: foundation.NewUUIDGenerator(nil),
		Clock: foundation.FixedClock{Value: now.Add(time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := retryService.CreateURL(ctx, captureapp.URLCommand{
		WorkspaceID: workspaceID, DisplayName: "Lease poison", URL: "https://example.com/outbox-poison",
		IdempotencyKey: "capture-outbox-poison",
	})
	if err != nil {
		t.Fatal(err)
	}
	retryLease, found, err := repository.ClaimNext(ctx, "worker-a", 5*time.Minute)
	if err != nil || !found || retryLease.CaptureID != second.Capture.ID {
		t.Fatalf("retry ClaimNext() lease=%#v found=%t error=%v", retryLease, found, err)
	}
	if err := repository.Reschedule(ctx, retryLease, "CAPTURE_WORKFLOW_TEMPORARY_FAILURE", 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	requireCaptureFoundationCode(t, repository.Poison(ctx, retryLease, "CAPTURE_WORKFLOW_STALE_FAILURE"), "CAPTURE_OUTBOX_LEASE_LOST")
	if lease, found, err := repository.ClaimNext(ctx, "worker-b", 5*time.Minute); err != nil || found {
		t.Fatalf("early retry ClaimNext() lease=%#v found=%t error=%v", lease, found, err)
	}
	var retryScheduled, retryLeaseReleased bool
	var retryCode string
	if err := pool.QueryRow(ctx, `SELECT available_at>clock_timestamp(),last_error_code,
		lease_owner IS NULL AND lease_expires_at IS NULL FROM ops.capture_outbox WHERE id=$1`, string(retryLease.ID)).Scan(
		&retryScheduled, &retryCode, &retryLeaseReleased,
	); err != nil {
		t.Fatal(err)
	}
	if !retryScheduled || retryCode != "CAPTURE_WORKFLOW_TEMPORARY_FAILURE" || !retryLeaseReleased {
		t.Fatalf("rescheduled outbox future=%t code=%q released=%t", retryScheduled, retryCode, retryLeaseReleased)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.capture_outbox SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1`, string(retryLease.ID)); err != nil {
		t.Fatal(err)
	}
	poisonLease, found, err := repository.ClaimNext(ctx, "worker-b", 5*time.Minute)
	if err != nil || !found || poisonLease.ID != retryLease.ID || poisonLease.AttemptCount != 2 {
		t.Fatalf("poison ClaimNext() lease=%#v found=%t error=%v", poisonLease, found, err)
	}
	if err := repository.Poison(ctx, poisonLease, "CAPTURE_WORKFLOW_TERMINAL_FAILURE"); err != nil {
		t.Fatal(err)
	}
	var poisoned, manualRecovery, poisonLeaseReleased bool
	var poisonCode string
	if err := pool.QueryRow(ctx, `SELECT poisoned_at IS NOT NULL,manual_recovery_required,last_error_code,
		lease_owner IS NULL AND lease_expires_at IS NULL FROM ops.capture_outbox WHERE id=$1`, string(poisonLease.ID)).Scan(
		&poisoned, &manualRecovery, &poisonCode, &poisonLeaseReleased,
	); err != nil {
		t.Fatal(err)
	}
	if !poisoned || !manualRecovery || poisonCode != "CAPTURE_WORKFLOW_TERMINAL_FAILURE" || !poisonLeaseReleased {
		t.Fatalf("poisoned outbox poisoned=%t manual=%t code=%q released=%t",
			poisoned, manualRecovery, poisonCode, poisonLeaseReleased)
	}
	if lease, found, err := repository.ClaimNext(ctx, "worker-c", 5*time.Minute); err != nil || found {
		t.Fatalf("terminal ClaimNext() lease=%#v found=%t error=%v", lease, found, err)
	}
	requireCapturePoolReleased(t, platform)
}

func TestCaptureRepositoryMaterializeURLAtomicRollbackAndStageAdvance(t *testing.T) {
	withCaptureRepositoryIntegration(t, testCaptureRepositoryMaterializeURLAtomicRollbackAndStageAdvance)
}

func testCaptureRepositoryMaterializeURLAtomicRollbackAndStageAdvance(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	repository captureRepositoryIntegrationRepository,
) {
	pool := platform.DB()
	now := time.Date(2026, 8, 2, 18, 0, 0, 0, time.UTC)
	const (
		workspaceID         foundation.ID = "68500000-0000-4000-8000-000000000001"
		definitionID                      = "68500000-0000-4000-8000-000000000002"
		workflowRunID                     = "68500000-0000-4000-8000-000000000003"
		attemptID                         = "68500000-0000-4000-8000-000000000004"
		collisionSourceID                 = "68500000-0000-4000-8000-000000000005"
		collisionArtifactID               = "68500000-0000-4000-8000-000000000006"
		collisionVersionID                = "68500000-0000-4000-8000-000000000007"
		rollbackArtifactID                = "68500000-0000-4000-8000-000000000008"
		successArtifactID                 = "68500000-0000-4000-8000-000000000009"
		successVersionID                  = "68500000-0000-4000-8000-000000000010"
	)
	insertCaptureWorkspace(t, ctx, pool, workspaceID, now)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,'capture-process',1,'{"nodes":[]}',$3)`, definitionID, string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
		VALUES($1,$2,$3,'running','{}',1,$4,$4)`, workflowRunID, string(workspaceID), definitionID, now); err != nil {
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
		WorkspaceID: workspaceID, DisplayName: "Atomic URL", URL: "https://example.com/materialize",
		IdempotencyKey: "capture-materialize-url",
	})
	if err != nil {
		t.Fatal(err)
	}
	processingCapture, processingAttempt, err := repository.BeginAttempt(ctx, captureapp.BeginAttemptRequest{
		ID: attemptID, WorkspaceID: workspaceID, CaptureID: created.Capture.ID,
		WorkflowRunID: workflowRunID, AttemptNumber: 1, StartedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if processingCapture.Status != capturedomain.StatusFetching || processingCapture.FetchStatus != capturedomain.StageRunning ||
		processingAttempt.Stage != capturedomain.AttemptStageFetch || processingAttempt.Status != capturedomain.AttemptStatusRunning {
		t.Fatalf("BeginAttempt() capture=%#v attempt=%#v", processingCapture, processingAttempt)
	}

	collisionHash := strings.Repeat("a", 64)
	if _, err := pool.Exec(ctx, `INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
		VALUES($1,$2,'quick_capture_url','Collision source','https://example.com/collision',$3)`, collisionSourceID, string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
		VALUES($1,$2,$3,4,$4,$5)`, collisionArtifactID, string(workspaceID), collisionHash, ".knowledge/sources/"+collisionHash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,
			original_content_location,security_status,captured_at)
		VALUES($1,$2,$3,$4,$5,4,'text/plain','collision.txt','pending',$6)`, collisionVersionID, collisionSourceID,
		string(workspaceID), collisionArtifactID, collisionHash, now); err != nil {
		t.Fatal(err)
	}

	rollbackHash := strings.Repeat("b", 64)
	_, _, materializeErr := repository.MaterializeURL(ctx, captureapp.MaterializeURLRequest{
		Attempt: processingAttempt, ExpectedCaptureVersion: processingCapture.Version,
		ArtifactID: rollbackArtifactID, SourceVersionID: collisionVersionID,
		ContentHash: rollbackHash, ByteSize: 7, MediaType: "text/html",
		OriginalContentLocation: "https://example.com/materialize", ManagedLocation: ".knowledge/sources/" + rollbackHash,
		CapturedAt: now.Add(2 * time.Minute),
	})
	if materializeErr == nil {
		t.Fatal("MaterializeURL() collision error is nil")
	}
	persistedCapture, err := repository.Get(ctx, workspaceID, created.Capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedCapture.Version != processingCapture.Version || persistedCapture.Status != processingCapture.Status ||
		persistedCapture.FetchStatus != processingCapture.FetchStatus || persistedCapture.LatestSourceVersionID != "" {
		t.Fatalf("capture changed after rollback: got=%#v want=%#v", persistedCapture, processingCapture)
	}
	var sourceVersionID, stage, status string
	var attemptVersion int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(source_version_id::text,''),stage,status,version
		FROM ops.capture_attempt WHERE id=$1`, attemptID).Scan(&sourceVersionID, &stage, &status, &attemptVersion); err != nil {
		t.Fatal(err)
	}
	if sourceVersionID != "" || stage != string(processingAttempt.Stage) || status != string(processingAttempt.Status) || attemptVersion != processingAttempt.Version {
		t.Fatalf("attempt changed after rollback source_version=%q stage=%q status=%q version=%d", sourceVersionID, stage, status, attemptVersion)
	}
	var rollbackFacts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.content_artifact
		WHERE workspace_id=$1 AND (id=$2 OR content_hash=$3)`, string(workspaceID), rollbackArtifactID, rollbackHash).Scan(&rollbackFacts); err != nil {
		t.Fatal(err)
	}
	if rollbackFacts != 0 {
		t.Fatalf("rolled-back content artifact count=%d want=0", rollbackFacts)
	}

	successHash := strings.Repeat("c", 64)
	materializedCapture, materializedAttempt, err := repository.MaterializeURL(ctx, captureapp.MaterializeURLRequest{
		Attempt: processingAttempt, ExpectedCaptureVersion: processingCapture.Version,
		ArtifactID: successArtifactID, SourceVersionID: successVersionID,
		ContentHash: successHash, ByteSize: 9, MediaType: "text/html",
		OriginalContentLocation: "https://example.com/materialize", ManagedLocation: ".knowledge/sources/" + successHash,
		CapturedAt: now.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if materializedCapture.Version != processingCapture.Version+1 || materializedCapture.Status != capturedomain.StatusProcessing ||
		materializedCapture.FetchStatus != capturedomain.StageReady || materializedCapture.IngestionStatus != capturedomain.StageRunning ||
		materializedCapture.LatestSourceVersionID != successVersionID || materializedAttempt.Version != processingAttempt.Version+1 ||
		materializedAttempt.Stage != capturedomain.AttemptStageIngestion || materializedAttempt.SourceVersionID != successVersionID {
		t.Fatalf("MaterializeURL() capture=%#v attempt=%#v", materializedCapture, materializedAttempt)
	}
	var materializedFacts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.source_version AS version
		JOIN core.content_artifact AS artifact ON artifact.id=version.content_artifact_id AND artifact.workspace_id=version.workspace_id
		WHERE version.workspace_id=$1 AND version.id=$2 AND version.source_id=$3 AND artifact.id=$4
			AND version.content_hash=$5 AND artifact.content_hash=$5`, string(workspaceID), successVersionID,
		string(materializedCapture.SourceID), successArtifactID, successHash).Scan(&materializedFacts); err != nil {
		t.Fatal(err)
	}
	if materializedFacts != 1 {
		t.Fatalf("materialized SourceVersion/Artifact count=%d want=1", materializedFacts)
	}

	refreshCapture, refreshAttempt, err := repository.MarkRefreshRunning(
		ctx, materializedAttempt, materializedCapture.Version, now.Add(4*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if refreshCapture.Version != materializedCapture.Version+1 || refreshCapture.IngestionStatus != capturedomain.StageRunning ||
		refreshAttempt.Version != materializedAttempt.Version+1 || refreshAttempt.Stage != capturedomain.AttemptStageIngestion {
		t.Fatalf("MarkRefreshRunning() capture=%#v attempt=%#v", refreshCapture, refreshAttempt)
	}
	_, _, staleErr := repository.MaterializeURL(ctx, captureapp.MaterializeURLRequest{
		Attempt: processingAttempt, ExpectedCaptureVersion: processingCapture.Version,
		ArtifactID: rollbackArtifactID, SourceVersionID: "68500000-0000-4000-8000-000000000011",
		ContentHash: rollbackHash, ByteSize: 7, MediaType: "text/html",
		OriginalContentLocation: "https://example.com/materialize", ManagedLocation: ".knowledge/sources/" + rollbackHash,
		CapturedAt: now.Add(5 * time.Minute),
	})
	if staleErr == nil {
		t.Fatal("stale MaterializeURL() error is nil")
	}
	requireCapturePoolReleased(t, platform)
}

func requireCaptureFoundationCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%#v code=%q want=%q", err, classifiedCode(classified), code)
	}
}

func classifiedCode(err *foundation.Error) string {
	if err == nil {
		return ""
	}
	return err.Code
}

func requireCapturePoolReleased(t *testing.T, platform *platformpostgres.Pool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for platform.DB().Stat().AcquiredConns() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired := platform.DB().Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("shared pool acquired connections=%d want=0", acquired)
	}
}

func TestCaptureRepositoryPreservesContextCauseAndReusesConnection(t *testing.T) {
	withCaptureRepositoryIntegration(t, testCaptureRepositoryPreservesContextCauseAndReusesConnection)
}

func testCaptureRepositoryPreservesContextCauseAndReusesConnection(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	repository captureRepositoryIntegrationRepository,
) {
	pool := platform.DB()
	now := time.Date(2026, 8, 2, 19, 0, 0, 0, time.UTC)
	const workspaceID foundation.ID = "68600000-0000-4000-8000-000000000001"
	insertCaptureWorkspace(t, ctx, pool, workspaceID, now)
	service, err := captureapp.NewService(captureapp.Dependencies{
		Repository: repository, Content: captureIntegrationContent{}, IDs: foundation.NewUUIDGenerator(nil),
		Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateText(ctx, captureapp.TextCommand{
		WorkspaceID: workspaceID, DisplayName: "Context capture", Text: "context cancellation",
		IdempotencyKey: "capture-context-capture",
	})
	if err != nil {
		t.Fatal(err)
	}

	cancelCause := errors.New("capture caller stopped waiting")
	canceledCtx, cancel := context.WithCancelCause(ctx)
	cancel(cancelCause)
	var canceledResult capturedomain.Capture
	canceledResult, err = repository.Get(canceledCtx, workspaceID, created.Capture.ID)
	if err == nil || canceledResult != (capturedomain.Capture{}) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Get() result=%#v error=%v", canceledResult, err)
	}
	if _, isGORM := repository.(*capturepostgres.GORMRepository); isGORM && !errors.Is(err, cancelCause) {
		t.Fatalf("GORM canceled error lost caller cause: %v", err)
	}

	deadlineCause := errors.New("capture caller deadline budget expired")
	deadlineCtx, deadlineCancel := context.WithDeadlineCause(ctx, time.Unix(0, 0), deadlineCause)
	defer deadlineCancel()
	deadlineResult, err := repository.List(deadlineCtx, captureapp.ListQuery{WorkspaceID: workspaceID, Limit: 10})
	if err == nil || len(deadlineResult.Items) != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline List() result=%#v error=%v", deadlineResult, err)
	}
	if _, isGORM := repository.(*capturepostgres.GORMRepository); isGORM && !errors.Is(err, deadlineCause) {
		t.Fatalf("GORM deadline error lost caller cause: %v", err)
	}
	var one int
	if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("pool unusable after canceled operations value=%d error=%v", one, err)
	}
	requireCapturePoolReleased(t, platform)
}

func TestCaptureRepositoryRejectsCorruptRowsWithoutPartialEntity(t *testing.T) {
	withCaptureRepositoryIntegration(t, testCaptureRepositoryRejectsCorruptRowsWithoutPartialEntity)
}

func testCaptureRepositoryRejectsCorruptRowsWithoutPartialEntity(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	repository captureRepositoryIntegrationRepository,
) {
	pool := platform.DB()
	now := time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC)
	const workspaceID foundation.ID = "68700000-0000-4000-8000-000000000001"
	insertCaptureWorkspace(t, ctx, pool, workspaceID, now)
	service, err := captureapp.NewService(captureapp.Dependencies{
		Repository: repository, Content: captureIntegrationContent{}, IDs: foundation.NewUUIDGenerator(nil),
		Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateText(ctx, captureapp.TextCommand{
		WorkspaceID: workspaceID, DisplayName: "Corrupt capture", Text: "corrupt row",
		IdempotencyKey: "capture-corrupt-row",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE core.capture DROP CONSTRAINT capture_status_check`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE core.capture DISABLE TRIGGER capture_validate_write`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.capture SET status='CORRUPT' WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(created.Capture.ID)); err != nil {
		t.Fatal(err)
	}
	corrupt, err := repository.Get(ctx, workspaceID, created.Capture.ID)
	if err == nil || corrupt != (capturedomain.Capture{}) {
		t.Fatalf("corrupt Get() result=%#v error=%v", corrupt, err)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("corrupt Get() error is not classified: %v", err)
	}
	page, pageErr := repository.List(ctx, captureapp.ListQuery{WorkspaceID: workspaceID, Limit: 10})
	if pageErr == nil || len(page.Items) != 0 {
		t.Fatalf("corrupt List() page=%#v error=%v", page, pageErr)
	}
	var one int
	if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("pool unusable after corrupt row value=%d error=%v", one, err)
	}
	requireCapturePoolReleased(t, platform)
}

func TestCaptureRepositoryTargetQueryPlansUseDeclaredIndexes(t *testing.T) {
	withCaptureRepositoryIntegration(t, testCaptureRepositoryTargetQueryPlansUseDeclaredIndexes)
}

func testCaptureRepositoryTargetQueryPlansUseDeclaredIndexes(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	repository captureRepositoryIntegrationRepository,
) {
	pool := platform.DB()
	now := time.Date(2026, 8, 2, 21, 0, 0, 0, time.UTC)
	const workspaceID foundation.ID = "68800000-0000-4000-8000-000000000001"
	insertCaptureWorkspace(t, ctx, pool, workspaceID, now)
	service, err := captureapp.NewService(captureapp.Dependencies{
		Repository: repository, Content: captureIntegrationContent{}, IDs: foundation.NewUUIDGenerator(nil),
		Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateURL(ctx, captureapp.URLCommand{
		WorkspaceID: workspaceID, DisplayName: "Plan capture", URL: "https://example.com/capture-plan",
		IdempotencyKey: "capture-plan-capture",
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	queries := []struct {
		name    string
		sql     string
		args    []any
		indexes []string
	}{
		{
			name:    "capture get",
			sql:     `SELECT id FROM core.capture WHERE workspace_id=$1 AND id=$2`,
			args:    []any{string(workspaceID), string(created.Capture.ID)},
			indexes: []string{"uq_capture_id_workspace", "idx_capture_source"},
		},
		{
			name:    "capture keyset list",
			sql:     `SELECT id FROM core.capture WHERE workspace_id=$1 ORDER BY captured_at DESC,id DESC LIMIT $2`,
			args:    []any{string(workspaceID), 11},
			indexes: []string{"idx_capture_workspace_captured"},
		},
		{
			name: "outbox claim candidate",
			sql: `SELECT id FROM ops.capture_outbox
				WHERE published_at IS NULL AND poisoned_at IS NULL AND available_at<=clock_timestamp()
				ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1`,
			args:    nil,
			indexes: []string{"idx_capture_outbox_pending"},
		},
	}
	for _, query := range queries {
		t.Run(query.name, func(t *testing.T) {
			rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+query.sql, query.args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var lines []string
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				lines = append(lines, line)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			plan := strings.Join(lines, "\n")
			matched := false
			for _, index := range query.indexes {
				if strings.Contains(plan, index) {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("query plan missing one of indexes %v:\n%s", query.indexes, plan)
			}
		})
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	requireCapturePoolReleased(t, platform)
}
