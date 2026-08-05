//go:build integration

package gitoperation

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	lockerWorkspaceA foundation.ID = "79100000-0000-4000-8000-000000000001"
	lockerWorkspaceB foundation.ID = "79100000-0000-4000-8000-000000000002"
)

func TestPostgresLockerSerializesOnlyTheSameWorkspace(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("ZHIXU_TEST_DATABASE_URL is required for Git operation integration tests")
	}
	ctx := context.Background()
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	first, err := NewPostgresLocker(pool)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPostgresLocker(pool)
	if err != nil {
		t.Fatal(err)
	}

	leaseA, err := first.Acquire(ctx, lockerWorkspaceA)
	if err != nil {
		t.Fatal(err)
	}
	queryCtx, cancelQuery := context.WithTimeout(ctx, time.Second)
	defer cancelQuery()
	var marker int
	if err := pool.QueryRow(queryCtx, `SELECT 1`).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("business pool was starved by Git operation lease: marker=%d err=%v", marker, err)
	}
	leaseB, err := second.Acquire(ctx, lockerWorkspaceB)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaseB.Release(ctx); err != nil {
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, err = second.Acquire(waitCtx, lockerWorkspaceA)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != ErrorCodeBusy || !classified.Retryable {
		t.Fatalf("blocked acquire error=%v", err)
	}
	if err := leaseA.Release(ctx); err != nil {
		t.Fatal(err)
	}

	takenOver, err := second.Acquire(ctx, lockerWorkspaceA)
	if err != nil {
		t.Fatal(err)
	}
	if err := takenOver.Release(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresLockerBoundsDedicatedSessions(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("ZHIXU_TEST_DATABASE_URL is required for Git operation integration tests")
	}
	ctx := context.Background()
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	first, err := NewPostgresLocker(pool)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPostgresLocker(pool)
	if err != nil {
		t.Fatal(err)
	}
	workspaceIDs := []foundation.ID{
		"79200000-0000-4000-8000-000000000001",
		"79200000-0000-4000-8000-000000000002",
		"79200000-0000-4000-8000-000000000003",
		"79200000-0000-4000-8000-000000000004",
		"79200000-0000-4000-8000-000000000005",
	}
	leases := make([]Lease, 0, maxDedicatedLockSessions)
	lockers := []*PostgresLocker{first, second}
	for index, workspaceID := range workspaceIDs[:maxDedicatedLockSessions] {
		lease, acquireErr := lockers[index%len(lockers)].Acquire(ctx, workspaceID)
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		leases = append(leases, lease)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, err = second.Acquire(waitCtx, workspaceIDs[maxDedicatedLockSessions])
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != ErrorCodeBusy || !classified.Retryable {
		t.Fatalf("session limit error=%v", err)
	}
	for _, lease := range leases {
		if err := lease.Release(ctx); err != nil {
			t.Fatal(err)
		}
	}
}
