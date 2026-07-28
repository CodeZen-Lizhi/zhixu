//go:build integration

package migration

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestM8ReviewAnswerDailyIndexSupportsDueQuotaLookup(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 51); err != nil {
		t.Fatalf("00051 up: %v", err)
	}
	if _, err := provider.UpTo(ctx, 51); err != nil {
		t.Fatalf("00051 repeated up: %v", err)
	}

	var valid, ready bool
	var definition string
	if err := pool.QueryRow(ctx, `SELECT index_state.indisvalid,index_state.indisready,pg_get_indexdef(index_state.indexrelid)
		FROM pg_index AS index_state
		WHERE index_state.indexrelid='learning.idx_learning_review_answer_workspace_created'::regclass`).
		Scan(&valid, &ready, &definition); err != nil {
		t.Fatal(err)
	}
	if !valid || !ready || !strings.Contains(definition, "(workspace_id, created_at) INCLUDE (card_id, session_id)") {
		t.Fatalf("00051 index valid=%v ready=%v definition=%q", valid, ready, definition)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN (COSTS OFF)
		SELECT card_id,session_id
		FROM learning.review_answer
		WHERE workspace_id='11111111-1111-4111-8111-111111111111'
		  AND created_at >= TIMESTAMPTZ '2026-07-27 00:00:00+00'
		  AND created_at < TIMESTAMPTZ '2026-07-28 00:00:00+00'`)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		fmt.Fprintln(&plan, line)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if !strings.Contains(plan.String(), "idx_learning_review_answer_workspace_created") {
		t.Fatalf("daily quota lookup does not use 00051 index:\n%s", plan.String())
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.DownTo(ctx, 50); err != nil {
		t.Fatalf("00051 down: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
		WHERE schemaname='learning' AND indexname='idx_learning_review_answer_workspace_created'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("00051 index remains after down: %d", count)
	}
	if _, err := provider.UpTo(ctx, 51); err != nil {
		t.Fatalf("00051 re-up: %v", err)
	}
}
