//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	healthdetector "github.com/CodeZen-Lizhi/zhixu/internal/health/detector"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFactReaderCancellationReleasesBlockedConnection(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	const applicationName = "health-detector-cancel-integration"
	config.ConnConfig.RuntimeParams["application_name"] = applicationName
	config.MaxConns = 1
	config.MinConns = 0
	readerPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readerPool.Close)
	blocker, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	blockerTx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blockerTx.Rollback(context.Background()) }()
	if _, err := blockerTx.Exec(ctx, `LOCK TABLE core.topic IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	reader, err := NewFactReader(readerPool)
	if err != nil {
		t.Fatal(err)
	}
	queryCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() {
		_, findErr := reader.Find(queryCtx, healthdetector.DetectorOrphan, healthapp.PageRequest{
			Scope: healthapp.Scope{
				WorkspaceID: "86000000-0000-4000-8000-000000000001",
				Type:        domain.ScanScopeTypeWorkspace,
				Ref:         "86000000-0000-4000-8000-000000000001",
			},
			BatchSize: 100,
			Descriptor: healthapp.Descriptor{
				ID: healthdetector.DetectorOrphan, Version: healthdetector.DefaultDetectorVersion,
				IssueType: domain.IssueTypeOrphan, SupportedScopes: healthdetector.DefaultSupportedScopes(healthdetector.DetectorOrphan),
				SupportedTarget: []domain.ObjectType{domain.ObjectTypeTopic, domain.ObjectTypeClaim},
				DefaultSeverity: domain.SeverityMedium, Available: true,
			},
		})
		result <- findErr
	}()
	waitForBlockedHealthDetectorQuery(t, ctx, admin, applicationName)
	started := time.Now()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked detector cancellation error=%v", err)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("blocked detector cancellation took %s", elapsed)
		}
		t.Logf("blocked detector cancellation returned in %s", time.Since(started))
	case <-time.After(3 * time.Second):
		t.Fatal("blocked detector query did not return after cancellation")
	}
	connectionDeadline := time.Now().Add(2 * time.Second)
	for readerPool.Stat().AcquiredConns() != 0 && time.Now().Before(connectionDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired := readerPool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("reader pool still has %d acquired connections after cancellation", acquired)
	}
	if err := blockerTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	reuseCtx, reuseCancel := context.WithTimeout(ctx, 2*time.Second)
	defer reuseCancel()
	var one int
	if err := readerPool.QueryRow(reuseCtx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("reader pool connection was not reusable: one=%d err=%v", one, err)
	}
	t.Logf("reader pool released and reused connection; total=%d idle=%d acquired=%d", readerPool.Stat().TotalConns(), readerPool.Stat().IdleConns(), readerPool.Stat().AcquiredConns())
}

func waitForBlockedHealthDetectorQuery(t *testing.T, ctx context.Context, admin *pgxpool.Pool, applicationName string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		err := admin.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE application_name=$1 AND wait_event_type='Lock' AND state='active'
		)`, applicationName).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("production detector SELECT did not block on the ACCESS EXCLUSIVE lock")
}
