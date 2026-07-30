//go:build integration

package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestRepositoryPersistsAndConstrainsEmbeddingModelSettingsProvenance(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	workspaceID, chunk := seedRetrievalChunk(t, ctx, database.DB(), "71100000", false, now)
	revision := int64(0)
	embedding := domain.EmbeddingVersion{
		ID: "71200000-0000-4000-8000-000000000001", Provider: "openai", AdapterName: "compatible",
		AdapterVersion: "v1", Model: "embed-provenance", Dimensions: 3,
		Normalization: domain.NormalizationL2, DistanceMetric: domain.DistanceCosine,
		ConfigHash: strings.Repeat("a", 64), ModelSettingsRevision: &revision, CreatedAt: now,
	}
	managed, err := repository.RegisterEmbeddingVersion(ctx, embedding)
	if err != nil || !managed.Created || managed.EmbeddingVersion.ModelSettingsRevision == nil ||
		*managed.EmbeddingVersion.ModelSettingsRevision != 0 {
		t.Fatalf("managed embedding=%#v err=%v", managed, err)
	}
	_, err = database.DB().Exec(ctx, `UPDATE retrieval.embedding_version
		SET model_settings_revision=NULL WHERE id=$1`, string(embedding.ID))
	assertExecutionProvenancePostgresCode(t, err, "55000")

	static := embedding
	static.ID = "71200000-0000-4000-8000-000000000002"
	static.ModelSettingsRevision = nil
	static.CreatedAt = now.Add(time.Second)
	staticResult, err := repository.RegisterEmbeddingVersion(ctx, static)
	if err != nil || !staticResult.Created || staticResult.EmbeddingVersion.ID != static.ID {
		t.Fatalf("static embedding sharing vector contract=%#v err=%v", staticResult, err)
	}

	missingRevision := int64(999)
	missing := embedding
	missing.ID = "71200000-0000-4000-8000-000000000003"
	missing.ModelSettingsRevision = &missingRevision
	missing.CreatedAt = now.Add(2 * time.Second)
	if _, err := repository.RegisterEmbeddingVersion(ctx, missing); !retrievalErrorKind(err, foundation.ErrorConsistencyViolation) {
		t.Fatalf("unknown settings revision error=%#v", err)
	}

	build := retrievalBuild(t, workspaceID, "71300000-0000-4000-8000-000000000001", &embedding.ID, "managed-index", chunk, now)
	build.IndexVersion.ModelSettingsRevision = &revision
	created, err := repository.BeginIndex(ctx, build)
	if err != nil || created.IndexVersion.ModelSettingsRevision == nil || *created.IndexVersion.ModelSettingsRevision != 0 {
		t.Fatalf("managed index=%#v err=%v", created, err)
	}

	mismatch := retrievalBuild(t, workspaceID, "71300000-0000-4000-8000-000000000002", &embedding.ID, "mismatched-index", chunk, now)
	if _, err := repository.BeginIndex(ctx, mismatch); !retrievalErrorKind(err, foundation.ErrorConsistencyViolation) {
		t.Fatalf("mismatched index revision error=%#v", err)
	}

	annotated, err := platformmigration.NewLegacyAnnotationFS(projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB := stdlib.OpenDBFromPool(database.DB())
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, annotated, goose.WithTableName("goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.DownTo(ctx, 65)
	assertExecutionProvenancePostgresCode(t, err, "55000")
}

func assertExecutionProvenancePostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if err == nil || !errors.As(err, &postgresError) || postgresError.Code != code {
		t.Fatalf("postgres error=%v want code=%s", err, code)
	}
}
