//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositorySourceRefreshLeaseBlocksSameWorkspaceUntilRelease(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 9, now)
	base := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 100, 1, true, now, now, 1)
	_ = mustCreateAndActivateSnapshot(t, ctx, repository,
		snapshotCommand(workspaceID, 300, base, "source-refresh-base", now.Add(time.Minute)),
		400, now.Add(2*time.Minute))
	firstSource := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 110, 1, true,
		now.Add(5*time.Minute), now.Add(5*time.Minute), 1)
	secondSource := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 120, 1, true,
		now.Add(6*time.Minute), now.Add(6*time.Minute), 1)

	first, err := repository.AcquireSourceRefresh(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Release(context.Background()) }()

	assertSourceRefreshKeyDoesNotBlockSnapshotKey(t, ctx, database.DB(), workspaceID)

	secondConfig := database.DB().Config().Copy()
	secondAttempted := make(chan struct{})
	var attemptedOnce sync.Once
	originalAfterRelease := secondConfig.AfterRelease
	secondConfig.AfterRelease = func(connection *pgx.Conn) bool {
		keep := true
		if originalAfterRelease != nil {
			keep = originalAfterRelease(connection)
		}
		attemptedOnce.Do(func() { close(secondAttempted) })
		return keep
	}
	secondPool, err := pgxpool.NewWithConfig(ctx, secondConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer secondPool.Close()
	secondRepository, err := NewRepository(secondPool)
	if err != nil {
		t.Fatal(err)
	}
	type acquisition struct {
		lease interface{ Release(context.Context) error }
		err   error
	}
	secondResult := make(chan acquisition, 1)
	secondCtx, cancelSecond := context.WithTimeout(ctx, 5*time.Second)
	defer cancelSecond()
	go func() {
		lease, acquireErr := secondRepository.AcquireSourceRefresh(secondCtx, workspaceID)
		secondResult <- acquisition{lease: lease, err: acquireErr}
	}()
	select {
	case <-secondAttempted:
	case <-time.After(2 * time.Second):
		t.Fatal("second source refresh lease never attempted the PostgreSQL advisory lock")
	}
	select {
	case result := <-secondResult:
		if result.lease != nil {
			_ = result.lease.Release(context.Background())
		}
		t.Fatalf("second source refresh lease did not block: %v", result.err)
	default:
	}

	firstActive := mustCreateAndActivateSnapshot(t, ctx, repository,
		snapshotCommand(workspaceID, 301, firstSource, "source-refresh-first", now.Add(7*time.Minute)),
		401, now.Add(8*time.Minute))
	assertSnapshotSource(t, ctx, database.DB(), firstActive.ID, base.SourceID, base.VersionID, base.ProjectionID, "included")
	assertSnapshotSource(t, ctx, database.DB(), firstActive.ID, firstSource.SourceID, firstSource.VersionID, firstSource.ProjectionID, "included")

	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-secondResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.lease == nil {
			t.Fatal("second source refresh lease is nil")
		}
		defer func() { _ = result.lease.Release(context.Background()) }()
		secondActive := mustCreateAndActivateSnapshot(t, ctx, repository,
			snapshotCommand(workspaceID, 302, secondSource, "source-refresh-second", now.Add(11*time.Minute)),
			402, now.Add(12*time.Minute))
		assertSnapshotSource(t, ctx, database.DB(), secondActive.ID, base.SourceID, base.VersionID, base.ProjectionID, "included")
		assertSnapshotSource(t, ctx, database.DB(), secondActive.ID, firstSource.SourceID, firstSource.VersionID, firstSource.ProjectionID, "included")
		assertSnapshotSource(t, ctx, database.DB(), secondActive.ID, secondSource.SourceID, secondSource.VersionID, secondSource.ProjectionID, "included")
		if err := result.lease.Release(ctx); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second source refresh lease remained blocked after release")
	}
}

func assertSourceRefreshKeyDoesNotBlockSnapshotKey(t *testing.T, ctx context.Context, database interface {
	Acquire(context.Context) (*pgxpool.Conn, error)
}, workspaceID foundation.ID) {
	t.Helper()
	connection, err := database.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	var acquired bool
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, string(workspaceID)).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("source refresh lease reused the snapshot advisory lock key")
	}
	var unlocked bool
	if err := connection.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, string(workspaceID)).Scan(&unlocked); err != nil || !unlocked {
		t.Fatalf("release snapshot test lock = %t, %v", unlocked, err)
	}
}
