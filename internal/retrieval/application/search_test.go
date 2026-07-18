package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestSearchKeywordUsesRequestLimitAndPreservesEvidenceProvenance(t *testing.T) {
	t.Parallel()
	index, contract := hybridSearchIndex(t)
	provenances := []domain.EvidenceProvenance{
		searchProvenance(10, 11, "a.md"),
		searchProvenance(20, 21, "b.md"),
	}
	store := &searchStoreFake{index: index, lexical: []domain.SearchCandidate{
		searchCandidate(index, 1, 4, provenances, domain.SearchRankerLexical),
	}}
	service := mustSearchService(t, store, &queryEmbedderFake{contract: contract}, nil)
	request := domain.SearchRequest{
		WorkspaceID: index.Index.WorkspaceID, Query: "  exact evidence  ", Mode: domain.SearchModeKeyword, Limit: 2,
		Filter: domain.SearchFilter{SourceIDs: []foundation.ID{searchID(20), searchID(10)}, PathPrefixes: []string{" docs/api ", "docs"}},
	}
	result, err := service.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveMode != domain.SearchModeKeyword || len(result.Items) != 1 || result.Items[0].Lexical == nil ||
		result.Items[0].LexicalFTSScore == nil || *result.Items[0].LexicalFTSScore != 0.5 ||
		result.Items[0].LexicalTrigramScore == nil || *result.Items[0].LexicalTrigramScore != 0.25 ||
		len(result.Items[0].Provenances) != 2 || result.Items[0].Provenances[1].RelativePath != "b.md" {
		t.Fatalf("result=%#v", result)
	}
	if store.lexicalQuery.Limit != request.Limit || store.lexicalQuery.Query != "exact evidence" ||
		len(store.lexicalQuery.Filter.SourceIDs) != 2 || store.lexicalQuery.Filter.SourceIDs[0] != searchID(10) ||
		len(store.lexicalQuery.Filter.PathPrefixes) != 2 || store.vectorCalls != 0 {
		t.Fatalf("lexical query=%#v vector calls=%d", store.lexicalQuery, store.vectorCalls)
	}
	if request.Query != "  exact evidence  " || request.Filter.PathPrefixes[0] != " docs/api " {
		t.Fatal("Search mutated caller input")
	}
}

func TestSearchFTSOnlyCompatibilityMatrixAndRealZeroResults(t *testing.T) {
	t.Parallel()
	index := ftsOnlySearchIndex()
	store := &searchStoreFake{index: index, lexical: []domain.SearchCandidate{}}
	service := mustSearchService(t, store, nil, nil)

	hybrid := domain.SearchRequest{WorkspaceID: index.Index.WorkspaceID, Query: "nothing", Mode: domain.SearchModeHybrid, Limit: 3}
	result, err := service.Search(context.Background(), hybrid)
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveMode != domain.SearchModeKeyword || result.Items == nil || len(result.Items) != 0 ||
		len(result.Degradations) != 2 || result.Degradations[0].Capability != domain.SearchDegradationVector ||
		result.Degradations[1].Capability != domain.SearchDegradationRerank || store.lexicalQuery.Limit != hybrid.Limit || store.vectorCalls != 0 {
		t.Fatalf("result=%#v lexical=%#v vector calls=%d", result, store.lexicalQuery, store.vectorCalls)
	}

	semantic := hybrid
	semantic.Mode = domain.SearchModeSemantic
	_, err = service.Search(context.Background(), semantic)
	assertSearchError(t, err, foundation.ErrorDependencyUnavailable, semanticUnavailableCode, false)

	keyword := hybrid
	keyword.Mode = domain.SearchModeKeyword
	result, err = service.Search(context.Background(), keyword)
	if err != nil || result.Items == nil || len(result.Degradations) != 0 {
		t.Fatalf("keyword result=%#v err=%v", result, err)
	}
}

func TestSearchSemanticUsesPersistedVectorLimitWithoutRerank(t *testing.T) {
	t.Parallel()
	index, contract := hybridSearchIndex(t)
	candidate := searchCandidate(index, 1, 4, []domain.EvidenceProvenance{searchProvenance(10, 11, "a.md")}, domain.SearchRankerVector)
	store := &searchStoreFake{index: index, vector: []domain.SearchCandidate{candidate}}
	embedder := &queryEmbedderFake{contract: contract, result: EmbedResult{Model: contract.Model, Embeddings: [][]float32{{0, 3, 4}}}}
	reranker := &rerankerFake{err: errors.New("semantic search must not call reranker")}
	service := mustSearchService(t, store, embedder, reranker)
	request := domain.SearchRequest{WorkspaceID: index.Index.WorkspaceID, Query: "semantic", Mode: domain.SearchModeSemantic, Limit: 2}
	result, err := service.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveMode != domain.SearchModeSemantic || len(result.Items) != 1 || result.Items[0].Vector == nil ||
		result.Items[0].Fusion.Score != result.Items[0].Vector.Score || result.Items[0].Rerank != nil ||
		len(result.Degradations) != 0 || store.lexicalCalls != 0 || store.vectorQuery.Limit != index.Fusion.VectorCandidateLimit ||
		reranker.calls != 0 {
		t.Fatalf("result=%#v lexical calls=%d vector=%#v rerank calls=%d", result, store.lexicalCalls, store.vectorQuery, reranker.calls)
	}
}

func TestSearchHybridFusesDeduplicatesExactSourceVersionSetsAndReranks(t *testing.T) {
	t.Parallel()
	index, contract := hybridSearchIndex(t)
	versionOne := []domain.EvidenceProvenance{searchProvenance(10, 11, "a.md")}
	versionOneAndTwo := []domain.EvidenceProvenance{
		searchProvenance(10, 11, "a.md"),
		searchProvenance(20, 21, "b.md"),
	}
	chunkA := searchCandidate(index, 1, 1, versionOne, domain.SearchRankerLexical)
	chunkC := searchCandidate(index, 3, 3, versionOneAndTwo, domain.SearchRankerLexical)
	chunkB := searchCandidate(index, 2, 2, versionOne, domain.SearchRankerLexical)
	chunkA.Lexical.Rank = 1
	chunkC.Lexical.Rank = 2
	chunkB.Lexical.Rank = 3
	vectorB := searchCandidate(index, 2, 2, versionOne, domain.SearchRankerVector)
	vectorC := searchCandidate(index, 3, 3, versionOneAndTwo, domain.SearchRankerVector)
	vectorB.Vector.Rank = 1
	vectorC.Vector.Rank = 2
	store := &searchStoreFake{index: index, lexical: []domain.SearchCandidate{chunkA, chunkC, chunkB}, vector: []domain.SearchCandidate{vectorB, vectorC}}
	embedder := &queryEmbedderFake{contract: contract, result: EmbedResult{Model: contract.Model, Embeddings: [][]float32{{3, 4, 0}}}}
	reranker := &rerankerFake{result: RerankResult{ModelVersion: "rerank-v1", Items: []RerankItem{
		{ChunkID: chunkC.ChunkID, Score: 0.9},
		{ChunkID: chunkB.ChunkID, Score: 0.8},
	}}}
	service := mustSearchService(t, store, embedder, reranker)
	from := searchTime().Add(-time.Hour)
	before := searchTime().Add(time.Hour)
	request := domain.SearchRequest{
		WorkspaceID: index.Index.WorkspaceID, Query: "hybrid", Mode: domain.SearchModeHybrid, Limit: 5,
		Filter: domain.SearchFilter{PathPrefixes: []string{"docs"}, CapturedAtFrom: &from, CapturedAtBefore: &before},
	}
	result, err := service.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveMode != domain.SearchModeHybrid || len(result.Degradations) != 0 || len(result.Items) != 2 ||
		result.Items[0].ChunkID != chunkC.ChunkID || result.Items[0].Rerank == nil || result.Items[0].Rerank.Rank != 1 ||
		result.Items[0].RerankModelVersion != "rerank-v1" || len(result.Items[0].Provenances) != 2 ||
		result.Items[1].ChunkID != chunkB.ChunkID || result.Items[1].Lexical == nil || result.Items[1].Vector == nil {
		t.Fatalf("result=%#v", result)
	}
	if embedder.calls != 1 || len(embedder.request.Inputs) != 1 || store.lexicalQuery.Limit != index.Fusion.LexicalCandidateLimit ||
		store.vectorQuery.Limit != index.Fusion.VectorCandidateLimit || store.vectorQuery.EmbeddingVersion.ID != index.EmbeddingVersion.ID ||
		len(store.vectorQuery.QueryEmbedding) != 3 || store.vectorQuery.QueryEmbedding[0] != 0.6 ||
		!reflectSearchFilterEqual(store.lexicalQuery.Filter, store.vectorQuery.Filter) ||
		store.lexicalQuery.Filter.CapturedAtFrom == store.vectorQuery.Filter.CapturedAtFrom ||
		store.lexicalQuery.Filter.CapturedAtFrom == request.Filter.CapturedAtFrom {
		t.Fatalf("embed=%#v lexical=%#v vector=%#v", embedder.request, store.lexicalQuery, store.vectorQuery)
	}
	if len(reranker.request.Candidates) != 2 || reranker.request.Candidates[0].ChunkID != chunkB.ChunkID || reranker.request.Candidates[1].ChunkID != chunkC.ChunkID {
		t.Fatalf("rerank request=%#v", reranker.request)
	}
}

func TestDeduplicateAdjacentCandidatesKeepsTruncatedProvenance(t *testing.T) {
	t.Parallel()
	index, _ := hybridSearchIndex(t)
	provenances := make([]domain.EvidenceProvenance, domain.MaxEvidenceProvenance)
	for position := range provenances {
		provenances[position] = searchProvenance(100+position, 200+position, fmt.Sprintf("source-%d.md", position))
	}
	first := searchCandidate(index, 91, 10, provenances, domain.SearchRankerLexical)
	second := searchCandidate(index, 92, 11, provenances, domain.SearchRankerLexical)
	first.ProvenanceTruncated = true
	second.ProvenanceTruncated = true
	kept := deduplicateAdjacentCandidates([]domain.SearchCandidate{first, second})
	if len(kept) != 2 {
		t.Fatalf("truncated provenance candidates were deduplicated: %#v", kept)
	}
}

func TestSearchHybridQueryEmbedRetryableFallsBackButContractAndStoreDamageFailClosed(t *testing.T) {
	t.Parallel()
	index, contract := hybridSearchIndex(t)
	lexical := []domain.SearchCandidate{searchCandidate(index, 1, 1, []domain.EvidenceProvenance{searchProvenance(10, 11, "a.md")}, domain.SearchRankerLexical)}

	t.Run("retryable query embed", func(t *testing.T) {
		providerErr := foundation.NewError(foundation.ErrorRetryableFailure, "EMBEDDING_RATE_LIMITED", true, errors.New("retry"))
		store := &searchStoreFake{index: index, lexical: lexical}
		service := mustSearchService(t, store, &queryEmbedderFake{contract: contract, err: providerErr}, nil)
		result, err := service.Search(context.Background(), domain.SearchRequest{WorkspaceID: index.Index.WorkspaceID, Query: "fallback", Mode: domain.SearchModeHybrid, Limit: 5})
		if err != nil || result.EffectiveMode != domain.SearchModeKeyword || len(result.Degradations) != 2 ||
			result.Degradations[0].Code != "EMBEDDING_RATE_LIMITED" || !result.Degradations[0].Retryable || store.vectorCalls != 0 {
			t.Fatalf("result=%#v err=%v vector calls=%d", result, err, store.vectorCalls)
		}
	})

	t.Run("contract mismatch", func(t *testing.T) {
		mismatch := contract
		mismatch.Model = "other-model"
		mismatch.ConfigHash = searchContractHash(t, mismatch)
		store := &searchStoreFake{index: index, lexical: lexical}
		embedder := &queryEmbedderFake{contract: mismatch}
		service := mustSearchService(t, store, embedder, nil)
		_, err := service.Search(context.Background(), domain.SearchRequest{WorkspaceID: index.Index.WorkspaceID, Query: "damage", Mode: domain.SearchModeHybrid, Limit: 5})
		assertSearchError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbeddingContractInvalid, false)
		if embedder.calls != 0 || store.vectorCalls != 0 {
			t.Fatalf("embed calls=%d vector calls=%d", embedder.calls, store.vectorCalls)
		}
	})

	t.Run("damaged embed output", func(t *testing.T) {
		store := &searchStoreFake{index: index, lexical: lexical}
		embedder := &queryEmbedderFake{contract: contract, result: EmbedResult{Model: contract.Model}}
		service := mustSearchService(t, store, embedder, nil)
		_, err := service.Search(context.Background(), domain.SearchRequest{WorkspaceID: index.Index.WorkspaceID, Query: "damage", Mode: domain.SearchModeHybrid, Limit: 5})
		assertSearchError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbedResultInvalid, false)
		if embedder.calls != 1 || store.vectorCalls != 0 {
			t.Fatalf("embed calls=%d vector calls=%d", embedder.calls, store.vectorCalls)
		}
	})

	t.Run("retryable vector store", func(t *testing.T) {
		storeErr := foundation.NewError(foundation.ErrorRetryableFailure, "VECTOR_QUERY_RETRY", true, errors.New("database retry"))
		store := &searchStoreFake{index: index, lexical: lexical, vectorErr: storeErr}
		embedder := &queryEmbedderFake{contract: contract, result: EmbedResult{Model: contract.Model, Embeddings: [][]float32{{1, 0, 0}}}}
		service := mustSearchService(t, store, embedder, nil)
		_, err := service.Search(context.Background(), domain.SearchRequest{WorkspaceID: index.Index.WorkspaceID, Query: "store", Mode: domain.SearchModeHybrid, Limit: 5})
		if !errors.Is(err, storeErr) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestSearchHybridRerankerNilRetryableAndDamagedOutput(t *testing.T) {
	t.Parallel()
	index, contract := hybridSearchIndex(t)
	lexical := []domain.SearchCandidate{searchCandidate(index, 1, 1, []domain.EvidenceProvenance{searchProvenance(10, 11, "a.md")}, domain.SearchRankerLexical)}
	vector := []domain.SearchCandidate{searchCandidate(index, 1, 1, []domain.EvidenceProvenance{searchProvenance(10, 11, "a.md")}, domain.SearchRankerVector)}
	request := domain.SearchRequest{WorkspaceID: index.Index.WorkspaceID, Query: "rerank", Mode: domain.SearchModeHybrid, Limit: 5}
	newEmbedder := func() *queryEmbedderFake {
		return &queryEmbedderFake{contract: contract, result: EmbedResult{Model: contract.Model, Embeddings: [][]float32{{1, 0, 0}}}}
	}

	t.Run("nil", func(t *testing.T) {
		service := mustSearchService(t, &searchStoreFake{index: index, lexical: lexical, vector: vector}, newEmbedder(), nil)
		result, err := service.Search(context.Background(), request)
		if err != nil || len(result.Degradations) != 1 || result.Degradations[0].Capability != domain.SearchDegradationRerank ||
			result.Items[0].Rerank != nil {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("retryable", func(t *testing.T) {
		rerankErr := foundation.NewError(foundation.ErrorRetryableFailure, "RERANK_RATE_LIMITED", true, errors.New("retry"))
		service := mustSearchService(t, &searchStoreFake{index: index, lexical: lexical, vector: vector}, newEmbedder(), &rerankerFake{err: rerankErr})
		result, err := service.Search(context.Background(), request)
		if err != nil || len(result.Degradations) != 1 || result.Degradations[0].Code != "RERANK_RATE_LIMITED" || !result.Degradations[0].Retryable {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("duplicate output", func(t *testing.T) {
		chunkID := lexical[0].ChunkID
		service := mustSearchService(t, &searchStoreFake{index: index, lexical: lexical, vector: vector}, newEmbedder(), &rerankerFake{result: RerankResult{
			ModelVersion: "rerank-v1", Items: []RerankItem{{ChunkID: chunkID, Score: 1}, {ChunkID: chunkID, Score: 0.5}},
		}})
		_, err := service.Search(context.Background(), request)
		assertSearchError(t, err, foundation.ErrorConsistencyViolation, rerankResultInvalidCode, false)
	})
}

func TestSearchRejectsCorruptActiveAndCandidateBindings(t *testing.T) {
	t.Parallel()
	index, contract := hybridSearchIndex(t)
	request := domain.SearchRequest{WorkspaceID: index.Index.WorkspaceID, Query: "corrupt", Mode: domain.SearchModeKeyword, Limit: 5}

	nonActive := index
	nonActive.Index.Status = domain.IndexStatusReady
	service := mustSearchService(t, &searchStoreFake{index: nonActive}, nil, nil)
	_, err := service.Search(context.Background(), request)
	assertSearchError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeSearchCandidateInvalid, false)

	invalidIdentity := index
	invalidIdentity.Index.ID = "not-a-uuid"
	service = mustSearchService(t, &searchStoreFake{index: invalidIdentity}, nil, nil)
	_, err = service.Search(context.Background(), request)
	assertSearchError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeSearchCandidateInvalid, false)

	badCandidate := searchCandidate(index, 1, 1, []domain.EvidenceProvenance{searchProvenance(10, 11, "a.md")}, domain.SearchRankerLexical)
	badCandidate.IndexVersionID = searchID(999)
	service = mustSearchService(t, &searchStoreFake{index: index, lexical: []domain.SearchCandidate{badCandidate}}, &queryEmbedderFake{contract: contract}, nil)
	_, err = service.Search(context.Background(), request)
	assertSearchError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeSearchCandidateInvalid, false)
}

func TestSearchHybridWaitsForVectorWorkerAfterLexicalFailure(t *testing.T) {
	t.Parallel()
	index, contract := hybridSearchIndex(t)
	lexicalErr := errors.New("lexical failed")
	store := &searchStoreFake{index: index, lexicalErr: lexicalErr}
	embedder := &cancellingQueryEmbedder{contract: contract, started: make(chan struct{}), exited: make(chan struct{})}
	service := mustSearchService(t, store, embedder, nil)

	_, err := service.Search(context.Background(), domain.SearchRequest{
		WorkspaceID: index.Index.WorkspaceID, Query: "cancel", Mode: domain.SearchModeHybrid, Limit: 5,
	})
	if !errors.Is(err, lexicalErr) {
		t.Fatalf("error=%v", err)
	}
	select {
	case <-embedder.started:
	default:
		t.Fatal("vector worker did not start")
	}
	select {
	case <-embedder.exited:
	default:
		t.Fatal("Search returned before the vector worker observed cancellation and exited")
	}
}

type searchStoreFake struct {
	mu sync.Mutex

	index      SearchIndex
	indexErr   error
	lexical    []domain.SearchCandidate
	lexicalErr error
	vector     []domain.SearchCandidate
	vectorErr  error

	lexicalQuery LexicalSearchQuery
	vectorQuery  VectorSearchQuery
	lexicalCalls int
	vectorCalls  int
}

func (fake *searchStoreFake) LoadActiveSearchIndex(context.Context, foundation.ID) (SearchIndex, error) {
	return fake.index, fake.indexErr
}

func (fake *searchStoreFake) SearchLexical(_ context.Context, query LexicalSearchQuery) ([]domain.SearchCandidate, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.lexicalCalls++
	fake.lexicalQuery = query
	return append([]domain.SearchCandidate(nil), fake.lexical...), fake.lexicalErr
}

func (fake *searchStoreFake) SearchVector(_ context.Context, query VectorSearchQuery) ([]domain.SearchCandidate, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.vectorCalls++
	fake.vectorQuery = query
	return append([]domain.SearchCandidate(nil), fake.vector...), fake.vectorErr
}

type queryEmbedderFake struct {
	mu sync.Mutex

	contract domain.EmbeddingContract
	request  EmbedRequest
	result   EmbedResult
	err      error
	calls    int
}

type cancellingQueryEmbedder struct {
	contract domain.EmbeddingContract
	started  chan struct{}
	exited   chan struct{}
}

func (fake *cancellingQueryEmbedder) Contract() domain.EmbeddingContract { return fake.contract }

func (fake *cancellingQueryEmbedder) Embed(ctx context.Context, _ EmbedRequest) (EmbedResult, error) {
	close(fake.started)
	<-ctx.Done()
	close(fake.exited)
	return EmbedResult{}, ctx.Err()
}

func (fake *queryEmbedderFake) Contract() domain.EmbeddingContract { return fake.contract }

func (fake *queryEmbedderFake) Embed(_ context.Context, request EmbedRequest) (EmbedResult, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	fake.request = request
	return fake.result, fake.err
}

type rerankerFake struct {
	mu sync.Mutex

	request RerankRequest
	result  RerankResult
	err     error
	calls   int
}

func (fake *rerankerFake) Rerank(_ context.Context, request RerankRequest) (RerankResult, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	fake.request = request
	return fake.result, fake.err
}

func mustSearchService(t *testing.T, store SearchStore, embedder QueryEmbedder, reranker Reranker) *SearchService {
	t.Helper()
	service, err := NewSearchService(store, embedder, reranker)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func hybridSearchIndex(t *testing.T) (SearchIndex, domain.EmbeddingContract) {
	t.Helper()
	contract := domain.EmbeddingContract{
		Provider: "test", AdapterName: "fake", AdapterVersion: "v1", Model: "embed-v1", Dimensions: 3,
		Normalization: domain.NormalizationL2, DistanceMetric: domain.DistanceCosine,
		EndpointIdentity: "https://models.example.test", MaxBatchSize: 8, MaxInputBytes: 1024, MaxBatchInputBytes: 8 * 1024,
	}
	contract.ConfigHash = searchContractHash(t, contract)
	embedding := domain.EmbeddingVersion{
		ID: searchID(90), Provider: contract.Provider, AdapterName: contract.AdapterName, AdapterVersion: contract.AdapterVersion,
		Model: contract.Model, Dimensions: contract.Dimensions, Normalization: contract.Normalization,
		DistanceMetric: contract.DistanceMetric, ConfigHash: contract.ConfigHash, CreatedAt: searchTime(),
	}
	fusion := domain.RRFConfig{
		SchemaVersion: 1, Method: domain.FusionMethodRRF, K: 60,
		LexicalCandidateLimit: 5, VectorCandidateLimit: 5, FusedCandidateLimit: 5, RerankCandidateLimit: 2,
	}
	raw, err := domain.CanonicalRRFConfig(fusion)
	if err != nil {
		t.Fatal(err)
	}
	embeddingID := embedding.ID
	index := domain.IndexVersion{
		ID: searchID(91), WorkspaceID: searchID(92), EmbeddingVersionID: &embeddingID,
		TokenizerID: "postgres-simple", TokenizerVersion: "v1", TokenizerConfigHash: strings.Repeat("a", 64),
		FusionConfig: raw, SourceSnapshotRef: "reindex-v2:event", ManifestHash: strings.Repeat("b", 64),
		ExpectedChunkCount: 10, IdempotencyKey: "search-hybrid", Status: domain.IndexStatusActive, Version: 2,
		CreatedAt: searchTime(), UpdatedAt: searchTime(),
	}
	return SearchIndex{Index: index, EmbeddingVersion: &embedding, Fusion: &fusion}, contract
}

func ftsOnlySearchIndex() SearchIndex {
	index := domain.IndexVersion{
		ID: searchID(91), WorkspaceID: searchID(92), TokenizerID: "postgres-simple", TokenizerVersion: "v1",
		TokenizerConfigHash: strings.Repeat("a", 64), FusionConfig: []byte(`{"method":"rrf","k":60}`),
		SourceSnapshotRef: "reindex-v1:event", ManifestHash: strings.Repeat("b", 64), ExpectedChunkCount: 10,
		IdempotencyKey: "search-fts", Status: domain.IndexStatusActive,
		DegradedCapabilities: []domain.DegradedCapability{domain.DegradedVector}, Version: 2,
		CreatedAt: searchTime(), UpdatedAt: searchTime(),
	}
	return SearchIndex{Index: index}
}

func searchCandidate(index SearchIndex, ordinal, sequence int, provenances []domain.EvidenceProvenance, ranker domain.SearchRanker) domain.SearchCandidate {
	candidate := domain.SearchCandidate{
		WorkspaceID: index.Index.WorkspaceID, IndexVersionID: index.Index.ID,
		EmbeddingVersionID: cloneOptionalID(index.Index.EmbeddingVersionID), ChunkID: searchID(100 + ordinal),
		ParseProjectionID: searchID(200 + ordinal), Sequence: int32(sequence), ContentHash: strings.Repeat(fmt.Sprintf("%x", ordinal%15+1), 64),
		HeadingPath: []string{"Retrieval"}, Span: domain.EvidenceSpan{ID: searchID(300 + ordinal), StartLine: 1, EndLine: 2, StartByte: 0, EndByte: 12},
		Snippet: fmt.Sprintf("snippet-%d", ordinal), RerankText: fmt.Sprintf("rerank text %d", ordinal),
		Provenances: append([]domain.EvidenceProvenance(nil), provenances...),
	}
	if ranker == domain.SearchRankerLexical {
		fts, trigram := 0.5, 0.25
		candidate.Lexical = &domain.CandidateStageScore{Rank: 1, Score: 1 / float64(ordinal+1)}
		candidate.LexicalFTSScore = &fts
		candidate.LexicalTrigramScore = &trigram
	} else {
		candidate.Vector = &domain.CandidateStageScore{Rank: 1, Score: float64(ordinal) / 10}
	}
	return candidate
}

func searchProvenance(sourceOrdinal, versionOrdinal int, path string) domain.EvidenceProvenance {
	return domain.EvidenceProvenance{SourceID: searchID(sourceOrdinal), SourceVersionID: searchID(versionOrdinal), RelativePath: path, CapturedAt: searchTime()}
}

func searchID(ordinal int) foundation.ID {
	return foundation.ID(fmt.Sprintf("a1000000-0000-4000-8000-%012d", ordinal))
}

func searchTime() time.Time { return time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC) }

func searchContractHash(t *testing.T, contract domain.EmbeddingContract) string {
	t.Helper()
	hash, err := domain.ComputeEmbeddingConfigHash(contract)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func reflectSearchFilterEqual(left, right domain.SearchFilter) bool {
	return fmt.Sprintf("%#v", left) == fmt.Sprintf("%#v", right)
}

func assertSearchError(t *testing.T, err error, kind foundation.ErrorKind, code string, retryable bool) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error=%T %v", err, err)
	}
	if classified.Kind != kind || classified.Code != code || classified.Retryable != retryable {
		t.Fatalf("classified=%#v", classified)
	}
}
