//go:build integration

package migration

import (
	"context"
	"testing"
)

func TestM8ReviewSharedLearningPathOriginColumnsFollowMigrationDirection(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if err := provider.UpTo(ctx, 60); err != nil {
		t.Fatalf("00060 up: %v", err)
	}
	assertPathColumnNullable := func(table, column, want string) {
		t.Helper()
		var nullable string
		if err := pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns
			WHERE table_schema='learning' AND table_name=$1 AND column_name=$2`, table, column).Scan(&nullable); err != nil {
			t.Fatal(err)
		}
		if nullable != want {
			t.Fatalf("%s.%s is_nullable=%s want=%s", table, column, nullable, want)
		}
	}
	assertPathColumnNullable("learning_path", "interview_session_id", "YES")
	assertPathColumnNullable("learning_path", "interview_report_id", "YES")

	if err := provider.UpTo(ctx, 60); err != nil {
		t.Fatalf("00060 re-up: %v", err)
	}
	assertPathColumnNullable("learning_path", "interview_session_id", "YES")
	assertPathColumnNullable("learning_path", "interview_report_id", "YES")
}
