//go:build integration && testcontainers

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	healthdetector "github.com/CodeZen-Lizhi/zhixu/internal/health/detector"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFactReaderCancellationReleasesBlockedConnection(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	platform := requireHealthIntegrationPlatform(t)
	pool := platform.DB()
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	baselineAcquired := pool.Stat().AcquiredConns()
	blockerTx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blockerTx.Rollback(context.Background()) }()
	if _, err := blockerTx.Exec(ctx, `LOCK TABLE core.topic IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	reader, err := NewGORMFactReader(platform)
	if err != nil {
		t.Fatal(err)
	}
	queryCtx, cancel := context.WithCancelCause(ctx)
	cancelCause := errors.New("health detector shutdown")
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
	waitForBlockedHealthDetectorQuery(t, ctx, pool)
	started := time.Now()
	cancel(cancelCause)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cancelCause) {
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
	for pool.Stat().AcquiredConns() > baselineAcquired && time.Now().Before(connectionDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != baselineAcquired {
		t.Fatalf("shared pool acquired connections=%d after cancellation, want baseline %d", acquired, baselineAcquired)
	}
	if err := blockerTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	reuseCtx, reuseCancel := context.WithTimeout(ctx, 2*time.Second)
	defer reuseCancel()
	var one int
	if err := pool.QueryRow(reuseCtx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("shared pool connection was not reusable: one=%d err=%v", one, err)
	}
	t.Logf("shared pool released and reused connection; total=%d idle=%d acquired=%d", pool.Stat().TotalConns(), pool.Stat().IdleConns(), pool.Stat().AcquiredConns())
}

func waitForBlockedHealthDetectorQuery(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND pid <> pg_backend_pid()
				AND wait_event_type='Lock' AND state='active'
		)`).Scan(&blocked)
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
