//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTimelineProjectionWorkerStartupAndRestart(t *testing.T) {
	testContext, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	databaseURL, pool := newTimelineWorkerTestDatabase(t, testContext)
	workspaceID := seedTimelineWorkerPendingProjection(t, testContext, pool)
	projectRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	workerBinary := buildTimelineWorkerBinary(t, testContext, projectRoot)

	first := startTimelineWorkerProcess(t, workerBinary, projectRoot, databaseURL, reserveTimelineWorkerAddress(t))
	defer first.ensureStopped()
	waitForTimelineWorkerProjectionState(t, first, pool, workspaceID, 1, 0, 1)
	requireTimelineWorkerDispatchLog(t, waitForTimelineWorkerLog(t, first, "timeline projection dispatch completed", timelineProjectionStartupPhase), timelineProjectionStartupPhase, 1, 1, 0, 0)

	seedTimelineWorkerPeriodicProjection(t, testContext, pool, workspaceID)
	waitForTimelineWorkerProjectionState(t, first, pool, workspaceID, 2, 0, 2)
	requireTimelineWorkerDispatchLog(t, waitForTimelineWorkerLog(t, first, "timeline projection dispatch completed", timelineProjectionPeriodicPhase), timelineProjectionPeriodicPhase, 1, 1, 0, 0)

	const privateBindingMarker = "timeline-worker-private-binding-secret"
	driftOutboxID := seedTimelineWorkerBindingDrift(t, testContext, pool, workspaceID, privateBindingMarker)
	waitForTimelineWorkerProjectionState(t, first, pool, workspaceID, 2, 1, 3)
	requireTimelineWorkerProjectionPoison(t, first, pool, workspaceID, driftOutboxID, privateBindingMarker)
	requireTimelineWorkerDispatchFailureLog(t, waitForTimelineWorkerLog(t, first, "timeline projection dispatch failed", timelineProjectionPeriodicPhase), timelineProjectionPeriodicPhase, 1, 0, 0, 1, privateBindingMarker)
	first.stop(t)

	seedTimelineWorkerExactReplay(t, testContext, pool, workspaceID)
	second := startTimelineWorkerProcess(t, workerBinary, projectRoot, databaseURL, reserveTimelineWorkerAddress(t))
	defer second.ensureStopped()
	waitForTimelineWorkerProjectionState(t, second, pool, workspaceID, 3, 1, 4)
	second.stop(t)
	requireTimelineWorkerDispatchLog(t, waitForTimelineWorkerLog(t, second, "timeline projection dispatch completed", timelineProjectionStartupPhase), timelineProjectionStartupPhase, 1, 0, 1, 0)

	var replayEvents int64
	if err := pool.QueryRow(testContext, `SELECT count(*) FROM ops.knowledge_event WHERE workspace_id=$1 AND source_event_ref='worker-smoke:exact-replay'`, string(workspaceID)).Scan(&replayEvents); err != nil {
		t.Fatal(err)
	}
	if replayEvents != 1 {
		t.Fatalf("exact replay event count=%d want=1", replayEvents)
	}
}

type timelineWorkerProcess struct {
	command *exec.Cmd
	output  lockedTimelineWorkerOutput
	done    chan error
	waited  bool
	exitErr error
}

type lockedTimelineWorkerOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (output *lockedTimelineWorkerOutput) Write(value []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.Write(value)
}

func (output *lockedTimelineWorkerOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.String()
}

func startTimelineWorkerProcess(t *testing.T, binary, projectRoot, databaseURL, healthAddress string) *timelineWorkerProcess {
	t.Helper()
	process := &timelineWorkerProcess{done: make(chan error, 1)}
	process.command = exec.Command(binary)
	process.command.Dir = projectRoot
	process.command.Env = timelineWorkerEnvironment(databaseURL, healthAddress)
	process.command.Stdout = &process.output
	process.command.Stderr = &process.output
	if err := process.command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		process.done <- process.command.Wait()
	}()
	return process
}

func (process *timelineWorkerProcess) stop(t *testing.T) {
	t.Helper()
	if process.waited {
		if process.exitErr != nil {
			t.Fatalf("worker exited before shutdown: %v\n%s", process.exitErr, process.output.String())
		}
		return
	}
	if err := process.command.Process.Signal(syscall.SIGTERM); err != nil {
		process.ensureStopped()
		t.Fatalf("signal worker: %v", err)
	}
	select {
	case process.exitErr = <-process.done:
		process.waited = true
	case <-time.After(20 * time.Second):
		process.ensureStopped()
		t.Fatalf("worker did not stop within hard deadline\n%s", process.output.String())
	}
	if process.exitErr != nil {
		t.Fatalf("worker shutdown failed: %v\n%s", process.exitErr, process.output.String())
	}
}

func (process *timelineWorkerProcess) ensureStopped() {
	if process == nil || process.waited || process.command == nil || process.command.Process == nil {
		return
	}
	_ = process.command.Process.Kill()
	process.exitErr = <-process.done
	process.waited = true
}

func (process *timelineWorkerProcess) exited() bool {
	if process.waited {
		return true
	}
	select {
	case process.exitErr = <-process.done:
		process.waited = true
		return true
	default:
		return false
	}
}

func timelineWorkerEnvironment(databaseURL, healthAddress string) []string {
	environment := make([]string, 0, len(os.Environ())+16)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "ZHIXU_") || strings.HasPrefix(key, "OTEL_") {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment,
		"ZHIXU_VERSION=timeline-worker-smoke",
		"ZHIXU_ENVIRONMENT=test",
		"ZHIXU_DATABASE_URL="+databaseURL,
		"ZHIXU_DATABASE_MAX_CONNS=8",
		"ZHIXU_DATABASE_MIN_CONNS=0",
		"ZHIXU_DATABASE_PING_TIMEOUT=5s",
		"ZHIXU_WORKER_HEALTH_ADDR="+healthAddress,
		"ZHIXU_WORKER_MAX_WORKERS=1",
		"ZHIXU_HEALTH_INTERVAL=200ms",
		"ZHIXU_WORKER_SOFT_STOP_TIMEOUT=2s",
		"ZHIXU_WORKER_HARD_STOP_TIMEOUT=10s",
		"ZHIXU_REINDEX_DISPATCH_POLL_INTERVAL=1m",
		"ZHIXU_REINDEX_DISPATCH_BATCH_SIZE=1",
		"ZHIXU_AUTH_MODE=disabled",
		"ZHIXU_TOOL_RUNTIME_MODE=disabled",
		"ZHIXU_WEB_FETCH_MODE=disabled",
		"ZHIXU_CHAT_PROVIDER=disabled",
		"ZHIXU_EMBEDDING_PROVIDER=disabled",
		"ZHIXU_TELEMETRY_MODE=disabled",
	)
}

func waitForTimelineWorkerProjectionState(t *testing.T, process *timelineWorkerProcess, pool *pgxpool.Pool, workspaceID foundation.ID, wantProjected, wantPoisoned, wantEvents int64) {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	for {
		if process.exited() {
			t.Fatalf("worker exited before startup projection: %v\n%s", process.exitErr, process.output.String())
		}
		queryContext, cancel := context.WithTimeout(context.Background(), time.Second)
		var pending, projected, poisoned, events int64
		err := pool.QueryRow(queryContext, `SELECT
count(*) FILTER (WHERE status='PENDING'),
count(*) FILTER (WHERE status='PROJECTED'),
count(*) FILTER (WHERE status='POISONED')
FROM ops.timeline_projection_outbox WHERE workspace_id=$1`, string(workspaceID)).Scan(&pending, &projected, &poisoned)
		if err == nil {
			err = pool.QueryRow(queryContext, `SELECT count(*) FROM ops.knowledge_event WHERE workspace_id=$1`, string(workspaceID)).Scan(&events)
		}
		cancel()
		if err != nil {
			process.ensureStopped()
			t.Fatalf("read projection state: %v", err)
		}
		if pending == 0 && projected == wantProjected && poisoned == wantPoisoned && events == wantEvents {
			return
		}
		select {
		case <-deadline.C:
			process.ensureStopped()
			t.Fatalf("timeline projection timed out pending=%d projected=%d poisoned=%d events=%d\n%s", pending, projected, poisoned, events, process.output.String())
		case <-poll.C:
		}
	}
}

func waitForTimelineWorkerLog(t *testing.T, process *timelineWorkerProcess, message, phase string) string {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		output := process.output.String()
		if timelineWorkerLogExists(output, message, phase) {
			return output
		}
		if process.exited() {
			t.Fatalf("worker exited before %s/%s log: %v\n%s", message, phase, process.exitErr, output)
		}
		select {
		case <-deadline.C:
			process.ensureStopped()
			t.Fatalf("timeline %s/%s log timed out\n%s", message, phase, output)
		case <-poll.C:
		}
	}
}

func timelineWorkerLogExists(output, message, phase string) bool {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		var entry struct {
			Message string `json:"msg"`
			Phase   string `json:"phase"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.Message == message && entry.Phase == phase {
			return true
		}
	}
	return false
}

func buildTimelineWorkerBinary(t *testing.T, ctx context.Context, projectRoot string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "zhixu-worker")
	command := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, "./cmd/worker")
	command.Dir = projectRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build worker: %v\n%s", err, output)
	}
	return binary
}

func reserveTimelineWorkerAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func newTimelineWorkerTestDatabase(t *testing.T, ctx context.Context) (string, *pgxpool.Pool) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Fatal("ZHIXU_TEST_DATABASE_URL is required for the Timeline Worker process smoke")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	databaseName := fmt.Sprintf("zhixu_timeline_worker_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	parsed.Path = "/" + databaseName
	databaseURL := parsed.String()
	migrationPool, err := platformpostgres.OpenMigration(ctx, databaseURL, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer migrationPool.Close()
	if err := platformmigration.MigrateAtlas(ctx, migrationPool.DB()); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return databaseURL, pool
}

func seedTimelineWorkerPendingProjection(t *testing.T, ctx context.Context, pool *pgxpool.Pool) foundation.ID {
	t.Helper()
	workspaceID := mustTimelineWorkerID(t)
	eventID := mustTimelineWorkerID(t)
	aggregateID := mustTimelineWorkerID(t)
	outboxID := mustTimelineWorkerID(t)
	now := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Second)
	root := "/tmp/zhixu-timeline-worker-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
VALUES($1,'timeline worker smoke',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), root, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.timeline_projection_outbox(
id,event_id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,
summary,correlation,occurred_at,status,created_at,updated_at,version)
VALUES($1,$2,$3,'PROPOSAL_CREATED','PROPOSAL',$4,'worker-smoke:new-event',$5,1,
'worker startup projection','{}'::jsonb,$6,'PENDING',$6,$6,1)`,
		string(outboxID), string(eventID), string(workspaceID), string(aggregateID), "proposal:"+string(aggregateID), now); err != nil {
		t.Fatal(err)
	}
	return workspaceID
}

func seedTimelineWorkerExactReplay(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID) {
	t.Helper()
	eventID := mustTimelineWorkerID(t)
	aggregateID := mustTimelineWorkerID(t)
	outboxID := mustTimelineWorkerID(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `INSERT INTO ops.knowledge_event(
id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,schema_version,
summary,payload,correlation,occurred_at,created_at)
VALUES($1,$2,'PROPOSAL_CREATED','PROPOSAL',$3,'worker-smoke:exact-replay',$4,1,'knowledge-event/v1',
'worker restart replay','{}'::jsonb,'{}'::jsonb,$5,$5)`,
		string(eventID), string(workspaceID), string(aggregateID), "proposal:"+string(aggregateID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.timeline_projection_outbox(
id,event_id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,
summary,correlation,occurred_at,status,created_at,updated_at,version)
VALUES($1,$2,$3,'PROPOSAL_CREATED','PROPOSAL',$4,'worker-smoke:exact-replay',$5,1,
'worker restart replay','{}'::jsonb,$6,'PENDING',$6,$6,1)`,
		string(outboxID), string(eventID), string(workspaceID), string(aggregateID), "proposal:"+string(aggregateID), now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func seedTimelineWorkerPeriodicProjection(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID) {
	t.Helper()
	eventID := mustTimelineWorkerID(t)
	aggregateID := mustTimelineWorkerID(t)
	outboxID := mustTimelineWorkerID(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO ops.timeline_projection_outbox(
id,event_id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,
summary,correlation,occurred_at,status,created_at,updated_at,version)
VALUES($1,$2,$3,'APPROVAL_GRANTED','APPROVAL',$4,'worker-smoke:periodic',$5,1,
'worker periodic projection','{}'::jsonb,$6,'PENDING',$6,$6,1)`,
		string(outboxID), string(eventID), string(workspaceID), string(aggregateID), "approval:"+string(aggregateID), now); err != nil {
		t.Fatal(err)
	}
}

func seedTimelineWorkerBindingDrift(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, privateMarker string) foundation.ID {
	t.Helper()
	existingEventID := mustTimelineWorkerID(t)
	driftEventID := mustTimelineWorkerID(t)
	aggregateID := mustTimelineWorkerID(t)
	outboxID := mustTimelineWorkerID(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	const sourceEventRef = "worker-smoke:stable-binding-drift"
	if _, err := pool.Exec(ctx, `INSERT INTO ops.knowledge_event(
id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,schema_version,
summary,payload,correlation,occurred_at,created_at)
VALUES($1,$2,'CONFLICT_OPENED','CONFLICT',$3,$4,$5,1,'knowledge-event/v1',
'existing stable binding','{}'::jsonb,'{}'::jsonb,$6,$6)`,
		string(existingEventID), string(workspaceID), string(aggregateID), sourceEventRef, "conflict:"+string(aggregateID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.timeline_projection_outbox(
id,event_id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,
summary,correlation,occurred_at,status,created_at,updated_at,version)
VALUES($1,$2,$3,'CONFLICT_OPENED','CONFLICT',$4,$5,$6,1,
$7,'{}'::jsonb,$8,'PENDING',$8,$8,1)`,
		string(outboxID), string(driftEventID), string(workspaceID), string(aggregateID), sourceEventRef, "conflict:"+string(aggregateID), privateMarker, now); err != nil {
		t.Fatal(err)
	}
	return outboxID
}

func requireTimelineWorkerProjectionPoison(t *testing.T, process *timelineWorkerProcess, pool *pgxpool.Pool, workspaceID, outboxID foundation.ID, privateMarker string) {
	t.Helper()
	queryContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var status, errorCode string
	var version int64
	if err := pool.QueryRow(queryContext, `SELECT status,error_code,version FROM ops.timeline_projection_outbox WHERE id=$1`, string(outboxID)).Scan(&status, &errorCode, &version); err != nil {
		t.Fatal(err)
	}
	if status != "POISONED" || errorCode != "KNOWLEDGE_TIMELINE_PROJECTION_POISONED" || version != 2 {
		t.Fatalf("poison state status=%s error_code=%s version=%d", status, errorCode, version)
	}
	if err := pool.QueryRow(queryContext, `SELECT count(*) FROM ops.knowledge_event WHERE workspace_id=$1 AND source_event_ref='worker-smoke:stable-binding-drift'`, string(workspaceID)).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("stable binding drift event count=%d want=1", version)
	}
	time.Sleep(500 * time.Millisecond)
	if process.exited() {
		t.Fatalf("worker exited after poison: %v\n%s", process.exitErr, process.output.String())
	}
	if err := pool.QueryRow(queryContext, `SELECT version FROM ops.timeline_projection_outbox WHERE id=$1`, string(outboxID)).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("poisoned projection was retried version=%d", version)
	}
	if strings.Contains(process.output.String(), privateMarker) {
		t.Fatalf("private drift marker leaked into worker log: %s", process.output.String())
	}
}

func requireTimelineWorkerDispatchLog(t *testing.T, output, phase string, processed, projected, replayed, poisoned int) {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		var entry struct {
			Message        string `json:"msg"`
			Phase          string `json:"phase"`
			ProcessedCount int    `json:"processed_count"`
			ProjectedCount int    `json:"projected_count"`
			ReplayedCount  int    `json:"replayed_count"`
			PoisonedCount  int    `json:"poisoned_count"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Message != "timeline projection dispatch completed" || entry.Phase != phase {
			continue
		}
		if entry.ProcessedCount != processed || entry.ProjectedCount != projected || entry.ReplayedCount != replayed || entry.PoisonedCount != poisoned {
			t.Fatalf("timeline dispatch log=%+v", entry)
		}
		return
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("timeline %s dispatch log not found\n%s", phase, output)
}

func requireTimelineWorkerDispatchFailureLog(t *testing.T, output, phase string, processed, projected, replayed, poisoned int, privateMarker string) {
	t.Helper()
	if strings.Contains(output, privateMarker) {
		t.Fatalf("private drift marker leaked into worker log: %s", output)
	}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		var entry struct {
			Message        string `json:"msg"`
			ErrorCode      string `json:"error_code"`
			Retryable      bool   `json:"retryable"`
			Phase          string `json:"phase"`
			ProcessedCount int    `json:"processed_count"`
			ProjectedCount int    `json:"projected_count"`
			ReplayedCount  int    `json:"replayed_count"`
			PoisonedCount  int    `json:"poisoned_count"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Message != "timeline projection dispatch failed" || entry.Phase != phase {
			continue
		}
		if entry.ErrorCode != "KNOWLEDGE_TIMELINE_PROJECTION_POISONED" || entry.Retryable || entry.ProcessedCount != processed || entry.ProjectedCount != projected || entry.ReplayedCount != replayed || entry.PoisonedCount != poisoned {
			t.Fatalf("timeline failure log=%+v", entry)
		}
		return
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("timeline %s failure log not found\n%s", phase, output)
}

func mustTimelineWorkerID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
