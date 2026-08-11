//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestModelSettingsChatAPIStyleMigrationBackfillsConstrainsAndGuardsDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if _, err := provider.UpTo(ctx, 77); err != nil {
		t.Fatalf("prepare migrations through 00077: %v", err)
	}
	insertModelSettingsMigrationRevision(t, ctx, pool)
	if _, err := provider.UpTo(ctx, 78); err != nil {
		t.Fatalf("00078 legacy upgrade: %v", err)
	}
	assertModelSettingsMigrationVersion(t, ctx, provider, 78)

	var legacyStyle string
	if err := pool.QueryRow(ctx, `SELECT chat_api_style FROM ops.model_settings_revisions WHERE revision=1`).Scan(&legacyStyle); err != nil {
		t.Fatal(err)
	}
	if legacyStyle != "chat_completions" {
		t.Fatalf("legacy chat_api_style=%q want chat_completions", legacyStyle)
	}
	assertModelSettingsPostgresCode(t, insertModelSettingsChatAPIStyleRevision(ctx, pool, 2, "automatic"), "23514")

	if _, err := provider.DownTo(ctx, 77); err != nil {
		t.Fatalf("chat_completions-only 00078 Down: %v", err)
	}
	var columnCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='ops' AND table_name='model_settings_revisions' AND column_name='chat_api_style'`).Scan(&columnCount); err != nil {
		t.Fatal(err)
	}
	if columnCount != 0 {
		t.Fatalf("chat_api_style remains after compatible Down: %d", columnCount)
	}

	if _, err := provider.UpTo(ctx, 78); err != nil {
		t.Fatalf("00078 Up after compatible Down: %v", err)
	}
	if err := insertModelSettingsChatAPIStyleRevision(ctx, pool, 2, "responses"); err != nil {
		t.Fatalf("responses revision rejected: %v", err)
	}
	_, err := provider.DownTo(ctx, 77)
	if err == nil || !strings.Contains(err.Error(), "responses model settings revisions exist") {
		t.Fatalf("00078 guarded Down error=%v", err)
	}
	assertModelSettingsPostgresCode(t, err, "55000")
	assertModelSettingsMigrationVersion(t, ctx, provider, 78)
	var columnCountAfterGuard int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='ops' AND table_name='model_settings_revisions' AND column_name='chat_api_style'`).Scan(&columnCountAfterGuard); err != nil {
		t.Fatal(err)
	}
	if columnCountAfterGuard != 1 {
		t.Fatalf("chat_api_style column count after guarded Down=%d want 1", columnCountAfterGuard)
	}
}

func insertModelSettingsChatAPIStyleRevision(ctx context.Context, queryer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, revision int64, apiStyle string) error {
	_, err := queryer.Exec(ctx, `INSERT INTO ops.model_settings_revisions(
		revision,chat_provider,chat_base_url,chat_model,chat_model_version,chat_adapter_version,chat_api_style,
		chat_timeout_microseconds,chat_max_request_bytes,chat_max_response_bytes,
		embedding_provider,embedding_base_url,embedding_model,embedding_dimensions,
		embedding_normalization,embedding_distance_metric,embedding_max_batch_size,
		embedding_max_input_bytes,embedding_max_batch_input_bytes,embedding_timeout_microseconds,
		embedding_max_response_bytes,created_by)
	VALUES($1,'openai-compatible','https://models.example.test/v1','chat-model','2026-07','v1',$2,
		30000000,4194304,4194304,'ollama','http://127.0.0.1:11434','embed-model',768,
		'l2','cosine',128,65536,8388608,30000000,67108864,'migration-test')`, revision, apiStyle)
	return err
}
