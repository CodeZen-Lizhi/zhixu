//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReadRepositoryHealthTrendAggregatesSevenUTCDaysFromTerminalScans(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	workspaceID := newHealthReadTestID(t)
	otherWorkspaceID := newHealthReadTestID(t)
	emptyWorkspaceID := newHealthReadTestID(t)
	for _, id := range []foundation.ID{workspaceID, otherWorkspaceID, emptyWorkspaceID} {
		cleanupHealthIntegrationWorkspace(t, pool, id)
		id := id
		t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, pool, id) })
		seedHealthReadWorkspace(t, ctx, pool, id)
	}

	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	now = now.UTC().Truncate(time.Microsecond)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	seedHealthTrendScan(t, ctx, pool, workspaceID, today.AddDate(0, 0, -5).Add(2*time.Hour), "SUCCEEDED", 2, 1, 1)
	seedHealthTrendScan(t, ctx, pool, workspaceID, today.AddDate(0, 0, -5).Add(20*time.Hour), "PARTIAL", 4, 2, 3)
	seedHealthTrendScan(t, ctx, pool, workspaceID, today.AddDate(0, 0, -3).Add(12*time.Hour), "FAILED", 1, 1, 0)
	seedHealthTrendScan(t, ctx, pool, workspaceID, today.AddDate(0, 0, -1).Add(8*time.Hour), "CANCELLED", 5, 0, 2)
	seedHealthTrendScan(t, ctx, pool, workspaceID, today.AddDate(0, 0, -7).Add(23*time.Hour), "SUCCEEDED", 50, 50, 50)
	seedActiveHealthTrendScan(t, ctx, pool, workspaceID, now, 99)
	seedHealthTrendScan(t, ctx, pool, otherWorkspaceID, now, "SUCCEEDED", 70, 7, 8)

	countingDB := &healthTrendCountingDB{Pool: pool}
	repository, err := NewReadRepository(countingDB)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := repository.GetHealthSummary(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if countingDB.trendQueries != 1 {
		t.Fatalf("trend queries=%d want=1", countingDB.trendQueries)
	}
	if countingDB.queries != 3 || countingDB.queryRows != 1 {
		t.Fatalf("summary query count Query=%d QueryRow=%d want Query=3 QueryRow=1", countingDB.queries, countingDB.queryRows)
	}
	if len(summary.Trend) != healthapp.HealthTrendDays {
		t.Fatalf("trend points=%d trend=%#v", len(summary.Trend), summary.Trend)
	}
	want := map[string][2]int64{
		today.AddDate(0, 0, -5).Format("2006-01-02"): {9, 4},
		today.AddDate(0, 0, -3).Format("2006-01-02"): {2, 0},
		today.AddDate(0, 0, -1).Format("2006-01-02"): {5, 2},
	}
	for index, point := range summary.Trend {
		wantDate := today.AddDate(0, 0, index-(healthapp.HealthTrendDays-1)).Format("2006-01-02")
		if point.Date != wantDate {
			t.Fatalf("trend[%d].date=%s want=%s", index, point.Date, wantDate)
		}
		counts := want[wantDate]
		if point.DetectedCount != counts[0] || point.ResolvedCount != counts[1] {
			t.Fatalf("trend[%d]=%#v want detected=%d resolved=%d", index, point, counts[0], counts[1])
		}
	}

	otherSummary, err := repository.GetHealthSummary(ctx, otherWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if point := otherSummary.Trend[len(otherSummary.Trend)-1]; point.DetectedCount != 77 || point.ResolvedCount != 8 {
		t.Fatalf("other workspace today=%#v", point)
	}
	emptySummary, err := repository.GetHealthSummary(ctx, emptyWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range emptySummary.Trend {
		if point.DetectedCount != 0 || point.ResolvedCount != 0 {
			t.Fatalf("empty workspace trend=%#v", emptySummary.Trend)
		}
	}
}

type healthTrendCountingDB struct {
	*pgxpool.Pool
	trendQueries int
	queries      int
	queryRows    int
}

func (database *healthTrendCountingDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	database.queries++
	if strings.Contains(query, "WITH utc_days AS") {
		database.trendQueries++
	}
	return database.Pool.Query(ctx, query, args...)
}

func (database *healthTrendCountingDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	database.queryRows++
	return database.Pool.QueryRow(ctx, query, args...)
}

func seedHealthReadWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	root := "/tmp/health-read-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
	VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, string(workspaceID), "health-read-"+string(workspaceID), root, now); err != nil {
		t.Fatal(err)
	}
	definitionID := newHealthReadTestID(t)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
	VALUES($1,$2,'health-read-test',1,'{"nodes":[]}',$3)`, string(definitionID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
}

func seedHealthTrendScan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, occurredAt time.Time, status string, created, reopened, resolved int64) {
	t.Helper()
	scanID := seedPendingHealthTrendScan(t, ctx, pool, workspaceID, occurredAt.Add(-time.Minute))
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan SET status='RUNNING',version=2,updated_at=$2 WHERE id=$1`, string(scanID), occurredAt.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	var failure any
	if status == "FAILED" {
		failure = `{"stage":"detector","code":"HEALTH_TREND_TEST_FAILURE","retryable":false}`
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan SET status=$2,created_count=$3,reopened_count=$4,resolved_count=$5,
	last_error=$6::jsonb,version=3,updated_at=$7,completed_at=$7 WHERE id=$1`, string(scanID), status, created, reopened, resolved, failure, occurredAt); err != nil {
		t.Fatal(err)
	}
}

func seedActiveHealthTrendScan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, updatedAt time.Time, created int64) {
	t.Helper()
	scanID := seedPendingHealthTrendScan(t, ctx, pool, workspaceID, updatedAt.Add(-time.Minute))
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan SET status='RUNNING',created_count=$2,version=2,updated_at=$3 WHERE id=$1`, string(scanID), created, updatedAt); err != nil {
		t.Fatal(err)
	}
}

func seedPendingHealthTrendScan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, createdAt time.Time) foundation.ID {
	t.Helper()
	scanID := newHealthReadTestID(t)
	runID := newHealthReadTestID(t)
	var definitionID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM workflow.definition WHERE workspace_id=$1 AND key='health-read-test' AND version=1`, string(workspaceID)).Scan(&definitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
	VALUES($1,$2,$3,'running','{}',1,$4,$4)`, string(runID), string(workspaceID), definitionID, createdAt); err != nil {
		t.Fatal(err)
	}
	fingerprintValue := sha256.Sum256([]byte("fingerprint:" + string(scanID)))
	requestHashValue := sha256.Sum256([]byte("request:" + string(scanID)))
	fingerprint := hex.EncodeToString(fingerprintValue[:])
	requestHash := hex.EncodeToString(requestHashValue[:])
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_scan(
	id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,fingerprint,idempotency_key,request_hash,
	workflow_run_id,max_items,status,version,created_at,updated_at)
	VALUES($1,$2,'WORKSPACE',$2,1,'health-scope/workspace/v1',$3,$4,$5,$6,100,'PENDING',1,$7,$7)`,
		string(scanID), string(workspaceID), fingerprint, "health-trend-"+string(scanID), requestHash, string(runID), createdAt); err != nil {
		t.Fatal(err)
	}
	return scanID
}

func newHealthReadTestID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
