//go:build integration

package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type rejectedSeedOutput struct{}

func (rejectedSeedOutput) Write([]byte) (int, error) {
	return 0, errors.New("seed output rejected")
}

func TestSeedOutputFailureCleansCommittedFixture(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	countFixtures := func() int {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.workspace WHERE name='graph-http-integration'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	before := countFixtures()
	if err := run([]string{"seed"}, rejectedSeedOutput{}); err == nil {
		t.Fatal("seed with rejected output unexpectedly succeeded")
	}
	if after := countFixtures(); after != before {
		t.Fatalf("seed output failure leaked a fixture Workspace: before=%d after=%d", before, after)
	}
}
