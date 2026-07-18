package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestVectorBuilderUsesCacheDeduplicatesMissesAndClassifiesOversized(t *testing.T) {
	t.Parallel()
	contract := vectorBuilderContract(t)
	embedding := vectorBuilderEmbedding(contract)
	index := vectorBuilderIndex(embedding.ID)
	store := &vectorBuildStoreFake{page: domain.VectorBuildPage{Index: index, EmbeddingVersion: embedding, Inputs: []domain.VectorBuildInput{
		{ChunkID: vectorBuilderID(1), ContentHash: strings.Repeat("a", 64), InputBytes: 6, Sequence: 0, TokenCount: 1, CachedEmbedding: []float32{1, 0, 0}},
		{ChunkID: vectorBuilderID(2), ContentHash: strings.Repeat("b", 64), Content: "miss", InputBytes: 4, Sequence: 1, TokenCount: 1},
		{ChunkID: vectorBuilderID(3), ContentHash: strings.Repeat("b", 64), Content: "miss", InputBytes: 4, Sequence: 2, TokenCount: 1},
		{ChunkID: vectorBuilderID(4), ContentHash: strings.Repeat("c", 64), InputBytes: 15, Sequence: 3, TokenCount: 2, AtomicOversized: true},
		{ChunkID: vectorBuilderID(5), ContentHash: strings.Repeat("d", 64), InputBytes: 15, Sequence: 4, TokenCount: 2},
	}}}
	embedder := &vectorEmbedderFake{contract: contract, result: EmbedResult{Model: contract.Model, Embeddings: [][]float32{{0, 3, 4}}}}
	builder, err := NewVectorBuilder(VectorBuilderDependencies{Store: store, Embedder: embedder, Clock: foundation.FixedClock{Value: time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := builder.BuildNextVectorBatch(context.Background(), BuildNextVectorBatchRequest{WorkspaceID: index.WorkspaceID, IndexVersionID: index.ID, ExpectedIndexVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if embedder.calls != 1 || len(embedder.request.Inputs) != 1 || embedder.request.Inputs[0] != "miss" {
		t.Fatalf("embed calls=%d request=%#v", embedder.calls, embedder.request)
	}
	if result.ProcessedCount != 5 || result.ProviderInputs != 1 || result.CacheHitCount != 1 || result.ReadyCount != 3 ||
		result.SkippedCount != 1 || result.FailedCount != 1 || !result.Done {
		t.Fatalf("result=%#v", result)
	}
	if len(store.batch.Writes) != 5 || store.batch.Writes[1].Embedding[1] != 0.6 || store.batch.Writes[2].Embedding[2] != 0.8 ||
		store.batch.Writes[3].FailureCode != domain.ErrorCodeEmbeddingInputOversized ||
		store.batch.Writes[4].FailureCode != domain.ErrorCodeEmbeddingInputContractMismatch {
		t.Fatalf("batch=%#v", store.batch)
	}
}

func TestVectorBuilderReturnsDoneWithoutProviderOrCommit(t *testing.T) {
	t.Parallel()
	contract := vectorBuilderContract(t)
	embedding := vectorBuilderEmbedding(contract)
	index := vectorBuilderIndex(embedding.ID)
	store := &vectorBuildStoreFake{page: domain.VectorBuildPage{Index: index, EmbeddingVersion: embedding}}
	embedder := &vectorEmbedderFake{contract: contract}
	builder, err := NewVectorBuilder(VectorBuilderDependencies{Store: store, Embedder: embedder, Clock: foundation.FixedClock{Value: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := builder.BuildNextVectorBatch(context.Background(), BuildNextVectorBatchRequest{WorkspaceID: index.WorkspaceID, IndexVersionID: index.ID, ExpectedIndexVersion: 1})
	if err != nil || !result.Done || embedder.calls != 0 || store.commitCalls != 0 {
		t.Fatalf("result=%#v err=%v embed=%d commits=%d", result, err, embedder.calls, store.commitCalls)
	}
}

func TestVectorBuilderCompletesTerminalHistoricalIndexBeforeContractBinding(t *testing.T) {
	t.Parallel()
	contract := vectorBuilderContract(t)
	persisted := vectorBuilderEmbedding(contract)
	index := vectorBuilderIndex(persisted.ID)
	configured := contract
	configured.Model = "embed-v2"
	configured.ConfigHash, _ = domain.ComputeEmbeddingConfigHash(configured)
	store := &vectorBuildStoreFake{page: domain.VectorBuildPage{Index: index, EmbeddingVersion: persisted}}
	builder, err := NewVectorBuilder(VectorBuilderDependencies{
		Store: store, Embedder: &vectorEmbedderFake{contract: configured}, Clock: foundation.FixedClock{Value: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := builder.BuildNextVectorBatch(context.Background(), BuildNextVectorBatchRequest{
		WorkspaceID: index.WorkspaceID, IndexVersionID: index.ID, ExpectedIndexVersion: index.Version,
	})
	if err != nil || !result.Done || store.commitCalls != 0 {
		t.Fatalf("result=%#v err=%v commits=%d", result, err, store.commitCalls)
	}
}

func TestVectorRecoveryBuilderRequiresMatchingProviderOnlyWhenPendingRemains(t *testing.T) {
	t.Parallel()
	contract := vectorBuilderContract(t)
	embedding := vectorBuilderEmbedding(contract)
	index := vectorBuilderIndex(embedding.ID)
	clock := foundation.FixedClock{Value: time.Now()}

	emptyStore := &vectorBuildStoreFake{page: domain.VectorBuildPage{Index: index, EmbeddingVersion: embedding}}
	recovery, err := NewVectorRecoveryBuilder(emptyStore, clock)
	if err != nil {
		t.Fatal(err)
	}
	result, err := recovery.BuildNextVectorBatch(context.Background(), BuildNextVectorBatchRequest{
		WorkspaceID: index.WorkspaceID, IndexVersionID: index.ID, ExpectedIndexVersion: index.Version,
	})
	if err != nil || !result.Done {
		t.Fatalf("terminal recovery result=%#v err=%v", result, err)
	}

	pendingStore := &vectorBuildStoreFake{page: domain.VectorBuildPage{Index: index, EmbeddingVersion: embedding, Inputs: []domain.VectorBuildInput{{
		ChunkID: vectorBuilderID(1), ContentHash: strings.Repeat("a", 64), Content: "pending", InputBytes: 7, TokenCount: 1,
	}}}}
	recovery, err = NewVectorRecoveryBuilder(pendingStore, clock)
	if err != nil {
		t.Fatal(err)
	}
	_, err = recovery.BuildNextVectorBatch(context.Background(), BuildNextVectorBatchRequest{
		WorkspaceID: index.WorkspaceID, IndexVersionID: index.ID, ExpectedIndexVersion: index.Version,
	})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorDependencyUnavailable ||
		classified.Code != vectorEmbedderVersionUnavailableCode || !classified.Retryable {
		t.Fatalf("pending recovery error=%#v", err)
	}
}

func TestVectorBuilderPreservesProviderFailure(t *testing.T) {
	t.Parallel()
	contract := vectorBuilderContract(t)
	embedding := vectorBuilderEmbedding(contract)
	index := vectorBuilderIndex(embedding.ID)
	want := foundation.NewError(foundation.ErrorRetryableFailure, "EMBEDDING_PROVIDER_UNAVAILABLE", true, errors.New("unavailable"))
	store := &vectorBuildStoreFake{page: domain.VectorBuildPage{Index: index, EmbeddingVersion: embedding, Inputs: []domain.VectorBuildInput{{ChunkID: vectorBuilderID(1), ContentHash: strings.Repeat("a", 64), Content: "miss", InputBytes: 4, TokenCount: 1}}}}
	embedder := &vectorEmbedderFake{contract: contract, err: want}
	builder, err := NewVectorBuilder(VectorBuilderDependencies{Store: store, Embedder: embedder, Clock: foundation.FixedClock{Value: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	_, got := builder.BuildNextVectorBatch(context.Background(), BuildNextVectorBatchRequest{WorkspaceID: index.WorkspaceID, IndexVersionID: index.ID, ExpectedIndexVersion: 1})
	if !errors.Is(got, want) || store.commitCalls != 0 {
		t.Fatalf("error=%#v commits=%d", got, store.commitCalls)
	}
}

type vectorBuildStoreFake struct {
	page        domain.VectorBuildPage
	err         error
	batch       domain.VectorBuildBatch
	commitCalls int
}

func (fake *vectorBuildStoreFake) LoadVectorBuildPage(context.Context, domain.VectorBuildPageCommand) (domain.VectorBuildPage, error) {
	return fake.page, fake.err
}

func (fake *vectorBuildStoreFake) CommitVectorBuildBatch(_ context.Context, batch domain.VectorBuildBatch) (domain.VectorBuildCommitResult, error) {
	fake.commitCalls++
	fake.batch = batch
	return domain.VectorBuildCommitResult{IndexVersionID: batch.IndexVersionID, AttemptedCount: int64(len(batch.Writes)), InsertedCount: int64(len(batch.Writes))}, fake.err
}

type vectorEmbedderFake struct {
	contract domain.EmbeddingContract
	request  EmbedRequest
	result   EmbedResult
	err      error
	calls    int
}

func (fake *vectorEmbedderFake) Contract() domain.EmbeddingContract { return fake.contract }
func (fake *vectorEmbedderFake) Embed(_ context.Context, request EmbedRequest) (EmbedResult, error) {
	fake.calls++
	fake.request = request
	return fake.result, fake.err
}

func vectorBuilderContract(t *testing.T) domain.EmbeddingContract {
	t.Helper()
	contract := domain.EmbeddingContract{Provider: "test", AdapterName: "fake", AdapterVersion: "v1", Model: "embed-v1", Dimensions: 3, Normalization: domain.NormalizationL2, DistanceMetric: domain.DistanceCosine, EndpointIdentity: "https://models.example.test", MaxBatchSize: 8, MaxInputBytes: 8, MaxBatchInputBytes: 32}
	hash, err := domain.ComputeEmbeddingConfigHash(contract)
	if err != nil {
		t.Fatal(err)
	}
	contract.ConfigHash = hash
	return contract
}

func vectorBuilderEmbedding(contract domain.EmbeddingContract) domain.EmbeddingVersion {
	return domain.EmbeddingVersion{ID: vectorBuilderID(90), Provider: contract.Provider, AdapterName: contract.AdapterName, AdapterVersion: contract.AdapterVersion, Model: contract.Model, Dimensions: contract.Dimensions, Normalization: contract.Normalization, DistanceMetric: contract.DistanceMetric, ConfigHash: contract.ConfigHash, CreatedAt: time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)}
}

func vectorBuilderIndex(embeddingID foundation.ID) domain.IndexVersion {
	return domain.IndexVersion{ID: vectorBuilderID(91), WorkspaceID: vectorBuilderID(92), EmbeddingVersionID: &embeddingID, TokenizerID: "postgres-simple", TokenizerVersion: "v1", TokenizerConfigHash: strings.Repeat("a", 64), FusionConfig: jsonRaw(`{"schema_version":1,"method":"rrf","k":60,"lexical_candidate_limit":8,"vector_candidate_limit":8,"fused_candidate_limit":8,"rerank_candidate_limit":4}`), SourceSnapshotRef: "reindex-v1:event", ManifestHash: strings.Repeat("b", 64), ExpectedChunkCount: 5, IdempotencyKey: "vector-build", Status: domain.IndexStatusBuilding, Version: 1, CreatedAt: time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)}
}

func vectorBuilderID(ordinal int) foundation.ID {
	return foundation.ID(fmt.Sprintf("e1000000-0000-4000-8000-%012d", ordinal))
}

func jsonRaw(value string) []byte { return []byte(value) }
