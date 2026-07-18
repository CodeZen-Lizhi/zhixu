//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/pgvector/pgvector-go"
)

func TestRepositoryVectorBuildPageCacheCommitAndResponseLossReplay(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 50, now)
	target := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 500, 1, true, now, now, 3)
	embedding := domain.EmbeddingVersion{
		ID: snapshotID(5000), Provider: "test", AdapterName: "fake", AdapterVersion: "v1", Model: "embed-v1",
		Dimensions: 3, Normalization: domain.NormalizationL2, DistanceMetric: domain.DistanceCosine,
		ConfigHash: strings.Repeat("8", 64), CreatedAt: now,
	}
	if _, err := repository.RegisterEmbeddingVersion(ctx, embedding); err != nil {
		t.Fatal(err)
	}
	command := snapshotCommand(workspaceID, 5001, target, "vector-build-snapshot", now.Add(time.Minute))
	command.IndexVersion.EmbeddingVersionID = &embedding.ID
	command.IndexVersion.DegradedCapabilities = nil
	command.IndexVersion.FusionConfig = vectorBuildRRF(t)
	created, err := repository.BeginWorkspaceSnapshot(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BuildLexical(ctx, domain.LexicalBuildCommand{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedIndexVersion: 1, At: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	var firstHash, searchVectorBefore string
	if err := database.QueryRow(ctx, `SELECT manifest.content_hash,projection.search_vector::text
		FROM retrieval.index_manifest_chunk manifest JOIN retrieval.chunk_projection projection
		  ON projection.index_version_id=manifest.index_version_id AND projection.chunk_id=manifest.chunk_id
		WHERE manifest.index_version_id=$1 ORDER BY manifest.sequence,manifest.chunk_id LIMIT 1`, string(created.IndexVersion.ID)).Scan(&firstHash, &searchVectorBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(ctx, `INSERT INTO retrieval.embedding_cache(workspace_id,embedding_version_id,content_hash,embedding,created_at)
		VALUES($1,$2,$3,$4,$5)`, string(workspaceID), string(embedding.ID), firstHash, pgvector.NewVector([]float32{1, 0, 0}), now); err != nil {
		t.Fatal(err)
	}
	boundedPage, err := repository.LoadVectorBuildPage(ctx, domain.VectorBuildPageCommand{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedIndexVersion: 1,
		Limit: 10, MaxInputBytes: 24, MaxBatchInputBytes: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	var loadedBytes int64
	for _, input := range boundedPage.Inputs {
		loadedBytes += int64(len(input.Content))
	}
	if !boundedPage.HasMore || len(boundedPage.Inputs) != 2 || loadedBytes > 24 {
		t.Fatalf("bounded page inputs=%d bytes=%d has_more=%v", len(boundedPage.Inputs), loadedBytes, boundedPage.HasMore)
	}
	pageCommand := domain.VectorBuildPageCommand{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedIndexVersion: 1,
		Limit: 10, MaxInputBytes: 1024, MaxBatchInputBytes: 10 * 1024,
	}
	page, err := repository.LoadVectorBuildPage(ctx, pageCommand)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Inputs) != 3 || page.HasMore || len(page.Inputs[0].CachedEmbedding) != 3 || page.Inputs[0].Content != "" {
		t.Fatalf("page=%#v", page)
	}
	writes := make([]domain.VectorBuildWrite, len(page.Inputs))
	for index, input := range page.Inputs {
		writes[index] = domain.VectorBuildWrite{
			ChunkID: input.ChunkID, ContentHash: input.ContentHash, TokenCount: input.TokenCount,
			VectorStatus: domain.VectorStatusReady, Embedding: []float32{1, 0, 0},
		}
	}
	batch := domain.VectorBuildBatch{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, EmbeddingVersionID: embedding.ID,
		ExpectedIndexVersion: 1, Writes: writes, At: now.Add(3 * time.Minute),
	}
	committed, err := repository.CommitVectorBuildBatch(ctx, batch)
	if err != nil || committed.InsertedCount != 3 || committed.ReplayedCount != 0 {
		t.Fatalf("commit=%#v err=%v", committed, err)
	}
	replayed, err := repository.CommitVectorBuildBatch(ctx, batch)
	if err != nil || replayed.InsertedCount != 0 || replayed.ReplayedCount != 3 {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	empty, err := repository.LoadVectorBuildPage(ctx, pageCommand)
	if err != nil || len(empty.Inputs) != 0 || empty.HasMore {
		t.Fatalf("empty page=%#v err=%v", empty, err)
	}
	var readyCount, cacheCount int64
	var searchVectorAfter string
	if err := database.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM retrieval.chunk_projection WHERE index_version_id=$1 AND vector_status='ready'),
		(SELECT count(*) FROM retrieval.embedding_cache WHERE workspace_id=$2 AND embedding_version_id=$3),
		(SELECT projection.search_vector::text FROM retrieval.index_manifest_chunk manifest
		 JOIN retrieval.chunk_projection projection ON projection.index_version_id=manifest.index_version_id AND projection.chunk_id=manifest.chunk_id
		 WHERE manifest.index_version_id=$1 ORDER BY manifest.sequence,manifest.chunk_id LIMIT 1)`,
		string(created.IndexVersion.ID), string(workspaceID), string(embedding.ID)).Scan(&readyCount, &cacheCount, &searchVectorAfter); err != nil {
		t.Fatal(err)
	}
	if readyCount != 3 || cacheCount != 3 || searchVectorAfter != searchVectorBefore {
		t.Fatalf("ready=%d cache=%d search before=%q after=%q", readyCount, cacheCount, searchVectorBefore, searchVectorAfter)
	}
}

func TestRepositoryVectorBuildCacheConflictRollsBackProjection(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 19, 3, 0, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 51, now)
	target := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 510, 1, true, now, now, 1)
	embedding := domain.EmbeddingVersion{ID: snapshotID(5100), Provider: "test", AdapterName: "fake", AdapterVersion: "v1", Model: "embed-v1", Dimensions: 3, Normalization: domain.NormalizationL2, DistanceMetric: domain.DistanceCosine, ConfigHash: strings.Repeat("7", 64), CreatedAt: now}
	if _, err := repository.RegisterEmbeddingVersion(ctx, embedding); err != nil {
		t.Fatal(err)
	}
	command := snapshotCommand(workspaceID, 5101, target, "vector-build-conflict", now.Add(time.Minute))
	command.IndexVersion.EmbeddingVersionID = &embedding.ID
	command.IndexVersion.DegradedCapabilities = nil
	command.IndexVersion.FusionConfig = vectorBuildRRF(t)
	created, err := repository.BeginWorkspaceSnapshot(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BuildLexical(ctx, domain.LexicalBuildCommand{WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedIndexVersion: 1, At: now.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	pageCommand := domain.VectorBuildPageCommand{WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedIndexVersion: 1, Limit: 10, MaxInputBytes: 1024, MaxBatchInputBytes: 10 * 1024}
	page, err := repository.LoadVectorBuildPage(ctx, pageCommand)
	if err != nil {
		t.Fatal(err)
	}
	input := page.Inputs[0]
	if _, err := database.DB().Exec(ctx, `INSERT INTO retrieval.embedding_cache(workspace_id,embedding_version_id,content_hash,embedding,created_at)
		VALUES($1,$2,$3,$4,$5)`, string(workspaceID), string(embedding.ID), input.ContentHash, pgvector.NewVector([]float32{0, 1, 0}), now); err != nil {
		t.Fatal(err)
	}
	_, err = repository.CommitVectorBuildBatch(ctx, domain.VectorBuildBatch{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, EmbeddingVersionID: embedding.ID, ExpectedIndexVersion: 1,
		Writes: []domain.VectorBuildWrite{{ChunkID: input.ChunkID, ContentHash: input.ContentHash, TokenCount: input.TokenCount, VectorStatus: domain.VectorStatusReady, Embedding: []float32{1, 0, 0}}},
		At:     now.Add(3 * time.Minute),
	})
	if !retrievalErrorKind(err, foundation.ErrorConsistencyViolation) {
		t.Fatalf("cache conflict error=%#v", err)
	}
	var pending int64
	if err := database.QueryRow(ctx, `SELECT count(*) FROM retrieval.chunk_projection WHERE index_version_id=$1 AND vector_status='pending'`, string(created.IndexVersion.ID)).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending after rollback=%d", pending)
	}
}

func TestVectorBuilderRepositoryCommitResponseLossDoesNotRepeatProvider(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 19, 4, 0, 0, 0, time.UTC)
	contract := domain.EmbeddingContract{
		Provider: "test", AdapterName: "fake", AdapterVersion: "v1", Model: "embed-v1", Dimensions: 3,
		Normalization: domain.NormalizationL2, DistanceMetric: domain.DistanceCosine,
		EndpointIdentity: "https://models.example.test", MaxBatchSize: 10, MaxInputBytes: 1024, MaxBatchInputBytes: 10 * 1024,
	}
	configHash, err := domain.ComputeEmbeddingConfigHash(contract)
	if err != nil {
		t.Fatal(err)
	}
	contract.ConfigHash = configHash
	embedding := domain.EmbeddingVersion{
		ID: snapshotID(5200), Provider: contract.Provider, AdapterName: contract.AdapterName, AdapterVersion: contract.AdapterVersion,
		Model: contract.Model, Dimensions: contract.Dimensions, Normalization: contract.Normalization,
		DistanceMetric: contract.DistanceMetric, ConfigHash: contract.ConfigHash, CreatedAt: now,
	}
	if _, err := repository.RegisterEmbeddingVersion(ctx, embedding); err != nil {
		t.Fatal(err)
	}
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 52, now)
	target := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 520, 1, true, now, now, 2)
	command := snapshotCommand(workspaceID, 5201, target, "vector-builder-response-loss", now.Add(time.Minute))
	command.IndexVersion.EmbeddingVersionID = &embedding.ID
	command.IndexVersion.DegradedCapabilities = nil
	command.IndexVersion.FusionConfig = vectorBuildRRF(t)
	created, err := repository.BeginWorkspaceSnapshot(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BuildLexical(ctx, domain.LexicalBuildCommand{WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedIndexVersion: 1, At: now.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	embedder := &integrationEmbedder{contract: contract}
	builder, err := application.NewVectorBuilder(application.VectorBuilderDependencies{Store: repository, Embedder: embedder, Clock: foundation.FixedClock{Value: now.Add(3 * time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	request := application.BuildNextVectorBatchRequest{WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedIndexVersion: 1}
	first, err := builder.BuildNextVectorBatch(ctx, request)
	if err != nil || !first.Done || first.ProviderInputs != 2 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	// 模拟调用方丢弃第一次成功响应；重试只能从数据库终态恢复。
	second, err := builder.BuildNextVectorBatch(ctx, request)
	if err != nil || !second.Done || second.ProcessedCount != 0 || embedder.calls != 1 {
		t.Fatalf("second=%#v err=%v provider calls=%d", second, err, embedder.calls)
	}
}

type integrationEmbedder struct {
	contract domain.EmbeddingContract
	calls    int
}

func (embedder *integrationEmbedder) Contract() domain.EmbeddingContract { return embedder.contract }

func (embedder *integrationEmbedder) Embed(_ context.Context, request application.EmbedRequest) (application.EmbedResult, error) {
	embedder.calls++
	vectors := make([][]float32, len(request.Inputs))
	for index := range vectors {
		vectors[index] = []float32{1, 0, 0}
	}
	return application.EmbedResult{Model: embedder.contract.Model, Embeddings: vectors}, nil
}

func vectorBuildRRF(t *testing.T) []byte {
	t.Helper()
	value, err := domain.CanonicalRRFConfig(domain.RRFConfig{SchemaVersion: domain.RRFFusionSchemaVersionV1, Method: domain.FusionMethodRRF, K: 60, LexicalCandidateLimit: 200, VectorCandidateLimit: 200, FusedCandidateLimit: 200, RerankCandidateLimit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
