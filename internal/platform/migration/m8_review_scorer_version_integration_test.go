//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM8ReviewScorerVersionMigrationBackfillsConstraints(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if err := provider.UpTo(ctx, 34); err != nil {
		t.Fatalf("prepare migrations through 00034: %v", err)
	}
	claimID := "95000000-0000-4000-8000-000000000003"
	seedLegacyReviewRows(t, ctx, pool, &claimID)
	if err := provider.UpTo(ctx, 55); err != nil {
		t.Fatalf("00055 legacy upgrade: %v", err)
	}
	if err := provider.UpTo(ctx, 55); err != nil {
		t.Fatalf("00055 repeated Up: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 55)
	assertM8ReviewScorerVersionShape(t, ctx, pool)

	var legacyVersion string
	if err := pool.QueryRow(ctx, `SELECT scorer_version FROM learning.review_answer
		WHERE id='72000000-0000-4000-8000-000000000006'`).Scan(&legacyVersion); err != nil {
		t.Fatal(err)
	}
	if legacyVersion != "legacy/unknown" {
		t.Fatalf("legacy scorer_version=%q want legacy/unknown", legacyVersion)
	}

	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	insertAnswer := func(id, key string, scorerVersion any) error {
		_, err := pool.Exec(ctx, `INSERT INTO learning.review_answer(
			id,workspace_id,session_id,card_id,question_ref,idempotency_key,user_answer,
			scorer_version,score,feedback,rating,schedule_snapshot,request_hash,created_at
		) VALUES(
			$1,'72000000-0000-4000-8000-000000000001',
			'72000000-0000-4000-8000-000000000005','72000000-0000-4000-8000-000000000004',
			'review:scorer-version',$2,'answer',$3,'{}','{}',3,'{}',$4,$5
		)`, id, key, scorerVersion, strings.Repeat("a", 64), now)
		return err
	}
	if err := insertAnswer("95000000-0000-4000-8000-000000000010", "valid-scorer-version", "review-scorer/v1"); err != nil {
		t.Fatalf("valid scorer version rejected: %v", err)
	}
	for _, testCase := range []struct {
		name    string
		id      string
		key     string
		version any
		code    string
	}{
		{name: "null", id: "95000000-0000-4000-8000-000000000011", key: "null-scorer-version", version: nil, code: "23502"},
		{name: "empty", id: "95000000-0000-4000-8000-000000000012", key: "empty-scorer-version", version: "", code: "23514"},
		{name: "blank", id: "95000000-0000-4000-8000-000000000013", key: "blank-scorer-version", version: "   ", code: "23514"},
		{name: "padded", id: "95000000-0000-4000-8000-000000000014", key: "padded-scorer-version", version: " review-scorer/v1 ", code: "23514"},
		{name: "overlong", id: "95000000-0000-4000-8000-000000000015", key: "overlong-scorer-version", version: strings.Repeat("v", 129), code: "23514"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assertPostgresCode(t, insertAnswer(testCase.id, testCase.key, testCase.version), testCase.code)
		})
	}
}

func TestM8ReviewScorerVersionMigrationSupportsRepeatedUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if err := provider.UpTo(ctx, 55); err != nil {
		t.Fatalf("empty 00055 Up: %v", err)
	}
	assertM8ReviewScorerVersionShape(t, ctx, pool)
	if err := provider.UpTo(ctx, 55); err != nil {
		t.Fatalf("00055 repeated Up: %v", err)
	}
	assertM8ReviewScorerVersionShape(t, ctx, pool)
}

func assertM8ReviewScorerVersionShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var nullable string
	var columnDefault *string
	if err := pool.QueryRow(ctx, `SELECT is_nullable,column_default FROM information_schema.columns
		WHERE table_schema='learning' AND table_name='review_answer' AND column_name='scorer_version'`).
		Scan(&nullable, &columnDefault); err != nil {
		t.Fatal(err)
	}
	if nullable != "NO" || columnDefault != nil {
		t.Fatalf("scorer_version nullable=%q default=%v", nullable, columnDefault)
	}

	var definition string
	var validated bool
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid),convalidated
		FROM pg_constraint
		WHERE conrelid='learning.review_answer'::regclass
		  AND conname='learning_review_answer_scorer_version_check'`).Scan(&definition, &validated); err != nil {
		t.Fatal(err)
	}
	if !validated || !strings.Contains(definition, "btrim(scorer_version)") ||
		!strings.Contains(definition, "octet_length(scorer_version) <= 128") {
		t.Fatalf("scorer_version constraint validated=%v definition=%q", validated, definition)
	}
}
