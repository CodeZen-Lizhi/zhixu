//go:build integration

package migration

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestRAGStageProjectionIndexSupportsScopedLatestEventLookup(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var definition string
	if err := pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes
		WHERE schemaname='ops' AND indexname='idx_ops_server_event_rag_stage_lookup'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"workspace_id, resource_ref, seq DESC",
		"rag.plan.started",
		"rag.validation.completed",
	} {
		if !strings.Contains(definition, fragment) {
			t.Fatalf("index definition %q misses %q", definition, fragment)
		}
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
		SELECT CASE
			WHEN (event_type,payload_summary->>'stage') IN (
				('rag.plan.started','plan.started'),
				('rag.plan.completed','plan.completed'),
				('rag.retrieval.started','retrieval.started'),
				('rag.retrieval.completed','retrieval.completed'),
				('rag.validation.started','validation.started'),
				('rag.validation.completed','validation.completed')
			) THEN payload_summary->>'stage'
			ELSE '__invalid__'
		END
		FROM ops.server_event
		WHERE workspace_id='11111111-1111-4111-8111-111111111111'
		  AND resource_ref='answer:22222222-2222-4222-8222-222222222222'
		  AND payload_summary->>'answer_id'='22222222-2222-4222-8222-222222222222'
		  AND event_type IN (
			'rag.plan.started','rag.plan.completed',
			'rag.retrieval.started','rag.retrieval.completed',
			'rag.validation.started','rag.validation.completed'
		  )
		ORDER BY seq DESC
		LIMIT 1`)
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
	if !strings.Contains(plan.String(), "idx_ops_server_event_rag_stage_lookup") {
		t.Fatalf("stage lookup plan does not use scoped index:\n%s", plan.String())
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	provider := migrationProvider(t, pool)
	if err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
}
