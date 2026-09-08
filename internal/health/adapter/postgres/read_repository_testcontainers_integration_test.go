//go:build integration && testcontainers

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
)

func TestReadRepositoryHealthTrendAggregatesSevenUTCDaysFromTerminalScans(t *testing.T) {
	ctx := context.Background()
	platform := requireHealthIntegrationPlatform(t)
	pool := platform.DB()

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

	repository, err := NewGORMReadRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	countingDB := &healthTrendCountingDB{healthReadDB: repository.database}
	repository.core.db = countingDB
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
