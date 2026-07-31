//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capacity"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

const (
	retrievalCapacityDefaultSeed      = "m10-03-retrieval-capacity-v1"
	retrievalCapacityDefaultChunks    = capacity.DefaultChunkCount
	retrievalCapacityChunkWriteBatch  = int64(5_000)
	retrievalCapacityManifestBatch    = int64(5_000)
	retrievalCapacityProjectionBatch  = int64(5_000)
	retrievalCapacityWarmups          = 5
	retrievalCapacitySamples          = 30
	retrievalCapacityQueryTimeout     = 10 * time.Second
	retrievalCapacityExplainTimeout   = 30 * time.Second
	retrievalCapacityP95Limit         = capacity.DefaultRetrievalP95Limit
	retrievalCapacityDefaultDimension = int32(384)
	retrievalCapacityRecallLimit      = 0.95
	retrievalCapacityHNSWM            = 16
	retrievalCapacityHNSWEFBuild      = 64
	retrievalCapacityHNSWEFSearch     = 400
	retrievalCapacityIVFLists         = 500
	retrievalCapacityIVFProbes        = 50
	retrievalCapacityWorkspaceName    = "retrieval-capacity-m10-benchmark"
	retrievalCapacityWorkspaceRootPre = "retrieval-capacity-m10-benchmark"
	retrievalCapacityParserID         = "capacity"
	retrievalCapacityParserVersion    = "capacity-v1"
	retrievalCapacityChunkStrategy    = "capacity-chunk-v1"
	retrievalCapacitySchemaVersion    = "v1"
)

type retrievalCapacityFixture struct {
	Version         string
	Seed            string
	WorkspaceID     foundation.ID
	ArtifactID      foundation.ID
	SourceID        foundation.ID
	SourceVersionID foundation.ID
	ProjectionID    foundation.ID
	SpanID          foundation.ID
	AttemptID       foundation.ID
	IndexVersionID  foundation.ID
	ActivationID    foundation.ID
	Embedding       domain.EmbeddingVersion
	Contract        domain.EmbeddingContract
	ChunkCount      int64
}

type retrievalCapacitySample struct {
	Method               string  `json:"method"`
	Sample               int     `json:"sample"`
	DurationNanoseconds  int64   `json:"duration_nanoseconds"`
	DurationMilliseconds float64 `json:"duration_milliseconds"`
	ReturnedCandidates   int     `json:"returned_candidates"`
}

type retrievalCapacityFailure struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

type retrievalCapacityIndex struct {
	Method                   string  `json:"method"`
	Name                     string  `json:"name"`
	BuildMilliseconds        float64 `json:"build_milliseconds"`
	Bytes                    int64   `json:"bytes"`
	HNSWM                    int     `json:"hnsw_m,omitempty"`
	HNSWEFConstruction       int     `json:"hnsw_ef_construction,omitempty"`
	HNSWEFSearch             int     `json:"hnsw_ef_search,omitempty"`
	IVFFlatLists             int     `json:"ivfflat_lists,omitempty"`
	IVFFlatProbes            int     `json:"ivfflat_probes,omitempty"`
	IterativeScan            string  `json:"iterative_scan"`
	RecallAtK                float64 `json:"recall_at_k"`
	RecallThreshold          float64 `json:"recall_threshold"`
	ReturnedExactCandidates  int     `json:"returned_exact_candidates"`
	ReturnedApproxCandidates int     `json:"returned_approx_candidates"`
	RecallPassed             bool    `json:"recall_passed"`
}

type retrievalCapacityMethodSummary struct {
	Index   retrievalCapacityIndex `json:"index"`
	Latency struct {
		WarmupCount           int     `json:"warmup_count"`
		SampleCount           int     `json:"sample_count"`
		P50Milliseconds       float64 `json:"p50_milliseconds"`
		P95Milliseconds       float64 `json:"p95_milliseconds"`
		MaxMilliseconds       float64 `json:"max_milliseconds"`
		ThresholdMilliseconds float64 `json:"threshold_milliseconds"`
		Passed                bool    `json:"passed"`
	} `json:"latency"`
	Explain retrievalCapacityExplain  `json:"explain"`
	Passed  bool                      `json:"passed"`
	Failure *retrievalCapacityFailure `json:"failure,omitempty"`
}

type retrievalCapacityExplain struct {
	File                  string   `json:"file"`
	RequiredIndexes       []string `json:"required_indexes"`
	PlanningMilliseconds  float64  `json:"planning_milliseconds"`
	ExecutionMilliseconds float64  `json:"execution_milliseconds"`
	IndexNames            []string `json:"index_names"`
	SequentialScans       []string `json:"sequential_scans"`
}

type retrievalCapacitySummary struct {
	SchemaVersion   string                    `json:"schema_version"`
	GeneratedAt     time.Time                 `json:"generated_at"`
	Outcome         string                    `json:"outcome"`
	Formal          bool                      `json:"formal"`
	RunMilliseconds float64                   `json:"run_milliseconds"`
	Failure         *retrievalCapacityFailure `json:"failure,omitempty"`
	Fixture         struct {
		Version             string `json:"version"`
		Seed                string `json:"seed"`
		WorkspaceID         string `json:"workspace_id"`
		ChunkCount          int64  `json:"chunk_count"`
		EmbeddingVersionID  string `json:"embedding_version_id"`
		EmbeddingDimensions int32  `json:"embedding_dimensions"`
	} `json:"fixture"`
	PostgreSQL struct {
		Version            string            `json:"version"`
		VersionNumber      string            `json:"version_number"`
		VectorExtension    string            `json:"vector_extension"`
		DatabaseBytes      int64             `json:"database_bytes"`
		PoolMaxConnections int32             `json:"pool_max_connections"`
		Settings           map[string]string `json:"settings"`
	} `json:"postgresql"`
	Go struct {
		Version string `json:"version"`
		OS      string `json:"os"`
		Arch    string `json:"arch"`
	} `json:"go"`
	CPU struct {
		LogicalCPUs int `json:"logical_cpus"`
		GOMAXPROCS  int `json:"gomaxprocs"`
	} `json:"cpu"`
	Workload struct {
		Mode                     string  `json:"mode"`
		Query                    string  `json:"query"`
		ResultLimit              int32   `json:"result_limit"`
		LexicalCandidateLimit    int32   `json:"lexical_candidate_limit"`
		VectorCandidateLimit     int32   `json:"vector_candidate_limit"`
		FusedCandidateLimit      int32   `json:"fused_candidate_limit"`
		QueryTimeoutMilliseconds float64 `json:"query_timeout_milliseconds"`
	} `json:"workload"`
	RuntimeMemory struct {
		Before runtime.MemStats `json:"before"`
		After  runtime.MemStats `json:"after"`
	} `json:"runtime_memory"`
	LexicalExplain retrievalCapacityExplain         `json:"lexical_explain"`
	Methods        []retrievalCapacityMethodSummary `json:"methods"`
	Passed         bool                             `json:"passed"`
}

// TestRetrievalCapacityBenchmark 验证 500k Chunk 的生产 Hybrid 检索、ANN recall、P95 与 EXPLAIN。
func TestRetrievalCapacityBenchmark(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	artifactDirectory := strings.TrimSpace(os.Getenv("ZHIXU_RETRIEVAL_BENCHMARK_ARTIFACT_DIR"))
	if databaseURL == "" || artifactDirectory == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL and ZHIXU_RETRIEVAL_BENCHMARK_ARTIFACT_DIR to run the Retrieval capacity benchmark")
	}
	seed := strings.TrimSpace(os.Getenv("ZHIXU_RETRIEVAL_BENCHMARK_SEED"))
	if seed == "" {
		seed = retrievalCapacityDefaultSeed
	}
	chunkCount := retrievalCapacityDefaultChunks
	if raw := strings.TrimSpace(os.Getenv("ZHIXU_RETRIEVAL_BENCHMARK_CHUNKS")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			t.Fatalf("parse ZHIXU_RETRIEVAL_BENCHMARK_CHUNKS: %v", err)
		}
		chunkCount = parsed
	}
	if chunkCount < 1 || chunkCount > capacity.DefaultChunkCount {
		t.Fatalf("retrieval capacity chunk count=%d outside [1,%d]", chunkCount, capacity.DefaultChunkCount)
	}
	dimensions := retrievalCapacityDefaultDimension
	if raw := strings.TrimSpace(os.Getenv("ZHIXU_RETRIEVAL_BENCHMARK_DIMENSIONS")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			t.Fatalf("parse ZHIXU_RETRIEVAL_BENCHMARK_DIMENSIONS: %v", err)
		}
		dimensions = int32(parsed)
	}
	if dimensions < 2 || dimensions > 2_000 {
		t.Fatalf("retrieval capacity embedding dimensions=%d outside [2,2000]", dimensions)
	}
	if err := capacity.PrepareArtifactDirectory(artifactDirectory); err != nil {
		t.Fatal(err)
	}

	runStarted := time.Now()
	summary := retrievalCapacitySummary{
		SchemaVersion: "zhixu-retrieval-capacity/v2", Outcome: "failed",
		Formal: chunkCount == retrievalCapacityDefaultChunks && dimensions == retrievalCapacityDefaultDimension,
	}
	summary.Fixture.Version, summary.Fixture.Seed = "retrieval-capacity/m10-hybrid-v2", seed
	summary.Fixture.ChunkCount, summary.Fixture.EmbeddingDimensions = chunkCount, dimensions
	summary.Go.Version, summary.Go.OS, summary.Go.Arch = runtime.Version(), runtime.GOOS, runtime.GOARCH
	summary.CPU.LogicalCPUs, summary.CPU.GOMAXPROCS = runtime.NumCPU(), runtime.GOMAXPROCS(0)
	summary.Workload.Mode, summary.Workload.Query, summary.Workload.ResultLimit = string(domain.SearchModeHybrid), "capacityneedle", 20
	summary.Workload.LexicalCandidateLimit, summary.Workload.VectorCandidateLimit = 40, 40
	summary.Workload.FusedCandidateLimit = 40
	summary.Workload.QueryTimeoutMilliseconds = durationMilliseconds(retrievalCapacityQueryTimeout)
	var pool *pgxpool.Pool
	samples := make([]retrievalCapacitySample, 0, retrievalCapacitySamples*2)
	plans := make(map[string]json.RawMessage, 3)
	defer func() {
		summary.GeneratedAt = time.Now().UTC()
		summary.RunMilliseconds = durationMilliseconds(time.Since(runStarted))
		writeRetrievalCapacityArtifacts(t, artifactDirectory, pool, samples, plans, &summary)
	}()
	fail := func(stage string, cause error) {
		summary.Outcome, summary.Passed = "failed", false
		summary.Failure = &retrievalCapacityFailure{Stage: stage, Message: cause.Error()}
		t.Fatalf("Retrieval capacity %s: %v", stage, cause)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	var err error
	pool, err = pgxpool.New(ctx, databaseURL)
	if err != nil {
		fail("open_database", err)
		return
	}
	t.Cleanup(pool.Close)

	fixture, err := seedRetrievalCapacity(ctx, pool, seed, chunkCount, dimensions)
	summary.Fixture.WorkspaceID = string(fixture.WorkspaceID)
	summary.Fixture.EmbeddingVersionID = string(fixture.Embedding.ID)
	if err != nil {
		fail("seed_fixture", err)
		return
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cleanupCancel()
		if cleanupErr := cleanupRetrievalCapacity(cleanupContext, pool, fixture); cleanupErr != nil {
			t.Errorf("cleanup Retrieval capacity fixture: %v", cleanupErr)
		}
	})

	lexicalRequest := application.LexicalSearchQuery{
		WorkspaceID: fixture.WorkspaceID, IndexVersionID: fixture.IndexVersionID,
		Query: summary.Workload.Query, Limit: summary.Workload.LexicalCandidateLimit,
	}
	lexicalPlan, lexicalExplain, err := explainRetrievalCapacity(ctx, pool, fixture, lexicalRequest)
	if len(lexicalPlan) > 0 {
		plans["lexical.explain.json"] = lexicalPlan
	}
	if err != nil && !summary.Formal && len(lexicalPlan) > 0 {
		// 缩小的本地重放可能因规划器成本模型不选 GIN；仍保留计划，正式 500k 继续严格门禁。
		lexicalExplain, err = inspectRetrievalCapacityPlanForIndexes(lexicalPlan, "lexical.explain.json", nil)
	}
	if err != nil {
		fail("explain_lexical", err)
		return
	}
	summary.LexicalExplain = lexicalExplain

	var memoryBefore runtime.MemStats
	runtime.ReadMemStats(&memoryBefore)
	queryVector := make([]float32, dimensions)
	queryVector[0] = 1
	request := domain.SearchRequest{
		WorkspaceID: fixture.WorkspaceID, Query: summary.Workload.Query,
		Mode: domain.SearchModeHybrid, Limit: summary.Workload.ResultLimit,
	}
	for _, method := range []string{"hnsw", "ivfflat"} {
		methodSummary, methodSamples, vectorPlan, benchmarkErr := runRetrievalCapacityMethod(
			ctx, databaseURL, pool, fixture, request, queryVector, method,
		)
		summary.Methods = append(summary.Methods, methodSummary)
		samples = append(samples, methodSamples...)
		if len(vectorPlan) > 0 {
			plans[method+".explain.json"] = vectorPlan
		}
		if benchmarkErr != nil {
			fail("benchmark_"+method, benchmarkErr)
			return
		}
	}
	var memoryAfter runtime.MemStats
	runtime.ReadMemStats(&memoryAfter)
	summary.RuntimeMemory.Before, summary.RuntimeMemory.After = memoryBefore, memoryAfter
	summary.Outcome, summary.Passed, summary.Failure = "passed", true, nil
}

type retrievalCapacityEmbedder struct {
	contract domain.EmbeddingContract
	vector   []float32
}

func (embedder *retrievalCapacityEmbedder) Contract() domain.EmbeddingContract {
	return embedder.contract
}

func (embedder *retrievalCapacityEmbedder) Embed(_ context.Context, request application.EmbedRequest) (application.EmbedResult, error) {
	result := application.EmbedResult{Model: embedder.contract.Model, Embeddings: make([][]float32, len(request.Inputs))}
	for index := range result.Embeddings {
		result.Embeddings[index] = append([]float32(nil), embedder.vector...)
	}
	return result, nil
}

func runRetrievalCapacityMethod(
	ctx context.Context,
	databaseURL string,
	seedPool *pgxpool.Pool,
	fixture retrievalCapacityFixture,
	request domain.SearchRequest,
	queryVector []float32,
	method string,
) (result retrievalCapacityMethodSummary, samples []retrievalCapacitySample, rawPlan json.RawMessage, returnErr error) {
	result.Index.Method = method
	result.Index.RecallThreshold = retrievalCapacityRecallLimit
	result.Index.IterativeScan = "strict_order"
	indexName := fmt.Sprintf("idx_capacity_retrieval_%s_%d", method, fixture.Embedding.Dimensions)
	result.Index.Name = indexName
	if method == "hnsw" {
		result.Index.HNSWM = retrievalCapacityHNSWM
		result.Index.HNSWEFConstruction = retrievalCapacityHNSWEFBuild
		result.Index.HNSWEFSearch = retrievalCapacityHNSWEFSearch
	} else if method == "ivfflat" {
		result.Index.IVFFlatLists = retrievalCapacityIVFLists
		result.Index.IVFFlatProbes = retrievalCapacityIVFProbes
	} else {
		return result, nil, nil, errors.New("Retrieval capacity ANN method is invalid")
	}
	markFailure := func(stage string, cause error) error {
		result.Passed = false
		result.Failure = &retrievalCapacityFailure{Stage: stage, Message: cause.Error()}
		return cause
	}

	if err := dropRetrievalCapacityIndex(ctx, seedPool, indexName); err != nil {
		return result, nil, nil, markFailure("drop_previous_index", err)
	}
	created := false
	defer func() {
		if !created {
			return
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := dropRetrievalCapacityIndex(cleanupContext, seedPool, indexName); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("drop Retrieval capacity %s index: %w", method, err))
		}
	}()
	buildStarted := time.Now()
	if err := createRetrievalCapacityIndex(ctx, seedPool, fixture, method, indexName); err != nil {
		return result, nil, nil, markFailure("build_index", err)
	}
	created = true
	result.Index.BuildMilliseconds = durationMilliseconds(time.Since(buildStarted))
	if err := seedPool.QueryRow(ctx, `SELECT pg_relation_size($1::regclass)`, "retrieval."+indexName).Scan(&result.Index.Bytes); err != nil {
		return result, nil, nil, markFailure("measure_index", err)
	}
	if _, err := seedPool.Exec(ctx, `ANALYZE retrieval.chunk_projection`); err != nil {
		return result, nil, nil, markFailure("analyze_index", err)
	}

	exact, err := queryRetrievalCapacityNearest(ctx, seedPool, fixture, queryVector, request.Limit, true)
	if err != nil {
		return result, nil, nil, markFailure("query_exact", err)
	}
	methodPool, err := newRetrievalCapacityMethodPool(ctx, databaseURL, method)
	if err != nil {
		return result, nil, nil, markFailure("open_method_pool", err)
	}
	defer methodPool.Close()
	approximate, err := queryRetrievalCapacityNearest(ctx, methodPool, fixture, queryVector, request.Limit, false)
	if err != nil {
		return result, nil, nil, markFailure("query_ann", err)
	}
	result.Index.ReturnedExactCandidates, result.Index.ReturnedApproxCandidates = len(exact), len(approximate)
	result.Index.RecallAtK = retrievalCapacityRecall(exact, approximate)
	result.Index.RecallPassed = len(exact) == int(request.Limit) && len(approximate) == int(request.Limit) &&
		result.Index.RecallAtK >= retrievalCapacityRecallLimit

	vectorRequest := application.VectorSearchQuery{
		WorkspaceID: fixture.WorkspaceID, IndexVersionID: fixture.IndexVersionID,
		EmbeddingVersion: fixture.Embedding, QueryEmbedding: queryVector,
		Limit: summaryVectorCandidateLimit(request),
	}
	rawPlan, result.Explain, err = explainRetrievalVectorCapacity(ctx, methodPool, vectorRequest, indexName, method+".explain.json")
	if err != nil {
		return result, nil, rawPlan, markFailure("explain_ann", err)
	}
	repository, err := NewSearchRepository(methodPool)
	if err != nil {
		return result, nil, rawPlan, markFailure("construct_repository", err)
	}
	service, err := application.NewSearchService(repository, &retrievalCapacityEmbedder{contract: fixture.Contract, vector: queryVector}, nil)
	if err != nil {
		return result, nil, rawPlan, markFailure("construct_service", err)
	}
	for warmup := 0; warmup < retrievalCapacityWarmups; warmup++ {
		queryContext, cancel := context.WithTimeout(ctx, retrievalCapacityQueryTimeout)
		searchResult, searchErr := service.Search(queryContext, request)
		cancel()
		if searchErr != nil {
			return result, nil, rawPlan, markFailure(fmt.Sprintf("warmup_%d", warmup+1), searchErr)
		}
		if searchResult.EffectiveMode != domain.SearchModeHybrid || len(searchResult.Items) == 0 {
			return result, nil, rawPlan, markFailure(fmt.Sprintf("warmup_%d", warmup+1), errors.New("Hybrid search returned an empty or degraded route"))
		}
	}
	durations := make([]time.Duration, 0, retrievalCapacitySamples)
	samples = make([]retrievalCapacitySample, 0, retrievalCapacitySamples)
	for sample := 0; sample < retrievalCapacitySamples; sample++ {
		queryContext, cancel := context.WithTimeout(ctx, retrievalCapacityQueryTimeout)
		started := time.Now()
		searchResult, searchErr := service.Search(queryContext, request)
		duration := time.Since(started)
		cancel()
		if searchErr != nil {
			return result, samples, rawPlan, markFailure(fmt.Sprintf("sample_%d", sample+1), searchErr)
		}
		if searchResult.EffectiveMode != domain.SearchModeHybrid || len(searchResult.Items) == 0 {
			return result, samples, rawPlan, markFailure(fmt.Sprintf("sample_%d", sample+1), errors.New("Hybrid search returned an empty or degraded route"))
		}
		durations = append(durations, duration)
		samples = append(samples, retrievalCapacitySample{
			Method: method, Sample: sample + 1, DurationNanoseconds: duration.Nanoseconds(),
			DurationMilliseconds: durationMilliseconds(duration), ReturnedCandidates: len(searchResult.Items),
		})
	}
	p50, err := capacity.PercentileNearestRank(durations, 50)
	if err != nil {
		return result, samples, rawPlan, markFailure("calculate_p50", err)
	}
	p95, err := capacity.PercentileNearestRank(durations, 95)
	if err != nil {
		return result, samples, rawPlan, markFailure("calculate_p95", err)
	}
	result.Latency.WarmupCount, result.Latency.SampleCount = retrievalCapacityWarmups, len(samples)
	result.Latency.P50Milliseconds, result.Latency.P95Milliseconds = durationMilliseconds(p50), durationMilliseconds(p95)
	result.Latency.MaxMilliseconds = durationMilliseconds(maximumCapacityDuration(durations))
	result.Latency.ThresholdMilliseconds = durationMilliseconds(retrievalCapacityP95Limit)
	result.Latency.Passed = p95 <= retrievalCapacityP95Limit
	result.Passed = result.Index.RecallPassed && result.Latency.Passed
	if !result.Index.RecallPassed {
		return result, samples, rawPlan, markFailure("recall_gate", fmt.Errorf("recall@%d %.4f is below %.2f", request.Limit, result.Index.RecallAtK, retrievalCapacityRecallLimit))
	}
	if !result.Latency.Passed {
		return result, samples, rawPlan, markFailure("latency_gate", fmt.Errorf("p95 %s exceeds %s", p95, retrievalCapacityP95Limit))
	}
	return result, samples, rawPlan, nil
}

func summaryVectorCandidateLimit(request domain.SearchRequest) int32 {
	if request.Limit < 40 {
		return 40
	}
	return request.Limit
}

func runRetrievalCapacityQuery(ctx context.Context, repository *SearchRepository, request application.LexicalSearchQuery) ([]domain.SearchCandidate, error) {
	queryContext, cancel := context.WithTimeout(ctx, retrievalCapacityQueryTimeout)
	defer cancel()
	return repository.SearchLexical(queryContext, request)
}

func maximumCapacityDuration(values []time.Duration) time.Duration {
	var maximum time.Duration
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}

func durationMilliseconds(value time.Duration) float64 {
	return float64(value.Nanoseconds()) / float64(time.Millisecond)
}

func createRetrievalCapacityIndex(
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture retrievalCapacityFixture,
	method, indexName string,
) error {
	identifier := pgx.Identifier{indexName}.Sanitize()
	dimensions := fixture.Embedding.Dimensions
	predicate := fmt.Sprintf(
		"vector_status='ready' AND embedding IS NOT NULL AND vector_dims(embedding)=%d",
		dimensions,
	)
	var statement string
	switch method {
	case "hnsw":
		statement = fmt.Sprintf(
			`CREATE INDEX %s ON retrieval.chunk_projection USING hnsw ((embedding::vector(%d)) vector_cosine_ops)
			 WITH (m=%d,ef_construction=%d) WHERE %s`,
			identifier, dimensions, retrievalCapacityHNSWM, retrievalCapacityHNSWEFBuild, predicate,
		)
	case "ivfflat":
		statement = fmt.Sprintf(
			`CREATE INDEX %s ON retrieval.chunk_projection USING ivfflat ((embedding::vector(%d)) vector_cosine_ops)
			 WITH (lists=%d) WHERE %s`,
			identifier, dimensions, retrievalCapacityIVFLists, predicate,
		)
	default:
		return errors.New("Retrieval capacity ANN index method is invalid")
	}
	_, err := pool.Exec(ctx, statement)
	return err
}

func dropRetrievalCapacityIndex(ctx context.Context, pool *pgxpool.Pool, indexName string) error {
	if pool == nil {
		return errors.New("Retrieval capacity index pool is nil")
	}
	_, err := pool.Exec(ctx, `DROP INDEX IF EXISTS `+pgx.Identifier{"retrieval", indexName}.Sanitize())
	return err
}

func newRetrievalCapacityMethodPool(ctx context.Context, databaseURL, method string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	config.MaxConns = 4
	config.AfterConnect = func(connectionContext context.Context, connection *pgx.Conn) error {
		settings := [][2]string{{"plan_cache_mode", "force_custom_plan"}}
		switch method {
		case "hnsw":
			settings = append(settings,
				[2]string{"hnsw.ef_search", strconv.Itoa(retrievalCapacityHNSWEFSearch)},
				[2]string{"hnsw.iterative_scan", "strict_order"},
			)
		case "ivfflat":
			settings = append(settings,
				[2]string{"ivfflat.probes", strconv.Itoa(retrievalCapacityIVFProbes)},
				[2]string{"ivfflat.iterative_scan", "strict_order"},
			)
		default:
			return errors.New("Retrieval capacity ANN pool method is invalid")
		}
		for _, setting := range settings {
			if _, err := connection.Exec(connectionContext, `SELECT set_config($1,$2,false)`, setting[0], setting[1]); err != nil {
				return err
			}
		}
		return nil
	}
	return pgxpool.NewWithConfig(ctx, config)
}

type retrievalCapacityQueryDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func queryRetrievalCapacityNearest(
	ctx context.Context,
	db retrievalCapacityQueryDB,
	fixture retrievalCapacityFixture,
	queryVector []float32,
	limit int32,
	exact bool,
) ([]string, error) {
	queryContext, cancel := context.WithTimeout(ctx, retrievalCapacityQueryTimeout)
	defer cancel()
	query := fmt.Sprintf(`SELECT projection.chunk_id::text
		FROM retrieval.chunk_projection projection
		WHERE projection.workspace_id=$1 AND projection.index_version_id=$2
		  AND projection.embedding_version_id=$3 AND projection.lexical_status='ready'
		  AND projection.vector_status='ready' AND projection.embedding IS NOT NULL
		  AND vector_dims(projection.embedding)=%d
		ORDER BY projection.embedding::vector(%d) <=> $4::vector(%d)
		LIMIT $5`, fixture.Embedding.Dimensions, fixture.Embedding.Dimensions, fixture.Embedding.Dimensions)
	arguments := []any{
		string(fixture.WorkspaceID), string(fixture.IndexVersionID), string(fixture.Embedding.ID),
		pgvector.NewVector(queryVector), limit,
	}
	if !exact {
		rows, err := db.Query(queryContext, query, arguments...)
		if err != nil {
			return nil, err
		}
		return scanRetrievalCapacityIDs(rows)
	}
	tx, err := db.Begin(queryContext)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, statement := range []string{`SET LOCAL enable_indexscan=off`, `SET LOCAL enable_bitmapscan=off`} {
		if _, err := tx.Exec(queryContext, statement); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(queryContext, query, arguments...)
	if err != nil {
		return nil, err
	}
	ids, err := scanRetrievalCapacityIDs(rows)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(queryContext); err != nil {
		return nil, err
	}
	return ids, nil
}

func scanRetrievalCapacityIDs(rows pgx.Rows) ([]string, error) {
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func retrievalCapacityRecall(exact, approximate []string) float64 {
	if len(exact) == 0 {
		return 0
	}
	expected := make(map[string]struct{}, len(exact))
	for _, value := range exact {
		expected[value] = struct{}{}
	}
	matches := 0
	for _, value := range approximate {
		if _, ok := expected[value]; ok {
			matches++
		}
	}
	return float64(matches) / float64(len(exact))
}

func explainRetrievalVectorCapacity(
	ctx context.Context,
	pool *pgxpool.Pool,
	request application.VectorSearchQuery,
	requiredIndex, file string,
) (json.RawMessage, retrievalCapacityExplain, error) {
	explainContext, cancel := context.WithTimeout(ctx, retrievalCapacityExplainTimeout)
	defer cancel()
	distanceExpression, ok := vectorDistanceExpression(request.EmbeddingVersion.DistanceMetric, request.EmbeddingVersion.Dimensions)
	if !ok {
		return nil, retrievalCapacityExplain{}, errors.New("Retrieval capacity vector distance is invalid")
	}
	arguments := []any{
		string(request.WorkspaceID), string(request.IndexVersionID), string(request.EmbeddingVersion.ID),
		pgvector.NewVector(request.QueryEmbedding), request.Limit, domain.MaxEvidenceProvenance,
	}
	query := fmt.Sprintf(
		vectorCandidateSQL, appendSearchFilter(&arguments, request.Filter), distanceExpression, request.EmbeddingVersion.Dimensions,
		"", distanceExpression,
		searchSnippetCharacterLimit, searchRerankCharacterLimit,
	)
	var raw json.RawMessage
	if err := pool.QueryRow(explainContext, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF, SETTINGS) `+query, arguments...).Scan(&raw); err != nil {
		return nil, retrievalCapacityExplain{}, err
	}
	metadata, err := inspectRetrievalCapacityPlanForIndexes(raw, file, []string{requiredIndex})
	return append(json.RawMessage(nil), raw...), metadata, err
}

func writeRetrievalCapacityArtifacts(
	t *testing.T,
	directory string,
	pool *pgxpool.Pool,
	samples []retrievalCapacitySample,
	plans map[string]json.RawMessage,
	summary *retrievalCapacitySummary,
) {
	t.Helper()
	if pool != nil {
		metadataContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := collectRetrievalCapacityEnvironment(metadataContext, pool, summary); err != nil {
			summary.Outcome, summary.Passed = "failed", false
			if summary.Failure == nil {
				summary.Failure = &retrievalCapacityFailure{Stage: "collect_environment", Message: err.Error()}
			}
			t.Errorf("collect Retrieval capacity environment: %v", err)
		}
		cancel()
	}
	if err := capacity.WriteAtomicArtifact(filepath.Join(directory, "retrieval-samples.jsonl"), func(writer io.Writer) error {
		encoder := json.NewEncoder(writer)
		for _, sample := range samples {
			if err := encoder.Encode(sample); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Errorf("write Retrieval capacity samples: %v", err)
	}
	for name, raw := range plans {
		if len(raw) == 0 {
			continue
		}
		if err := capacity.WriteAtomicArtifact(filepath.Join(directory, name), func(writer io.Writer) error {
			_, err := writer.Write(raw)
			return err
		}); err != nil {
			t.Errorf("write Retrieval capacity %s: %v", name, err)
		}
	}
	if err := capacity.WriteJSONArtifact(filepath.Join(directory, "retrieval-summary.json"), summary); err != nil {
		t.Errorf("write Retrieval capacity summary: %v", err)
	}
}

func collectRetrievalCapacityEnvironment(ctx context.Context, pool *pgxpool.Pool, summary *retrievalCapacitySummary) error {
	if summary == nil {
		return errors.New("Retrieval capacity summary is nil")
	}
	summary.PostgreSQL.PoolMaxConnections = pool.Stat().MaxConns()
	if err := pool.QueryRow(ctx, `SELECT current_setting('server_version'),current_setting('server_version_num'),
		COALESCE((SELECT extversion FROM pg_extension WHERE extname='vector'),''),pg_database_size(current_database())`).Scan(
		&summary.PostgreSQL.Version, &summary.PostgreSQL.VersionNumber,
		&summary.PostgreSQL.VectorExtension, &summary.PostgreSQL.DatabaseBytes,
	); err != nil {
		return err
	}
	settingNames := []string{
		"shared_buffers", "effective_cache_size", "work_mem", "maintenance_work_mem",
		"max_parallel_workers_per_gather", "random_page_cost", "effective_io_concurrency",
	}
	summary.PostgreSQL.Settings = make(map[string]string, len(settingNames))
	for _, name := range settingNames {
		var value string
		if err := pool.QueryRow(ctx, `SELECT current_setting($1)`, name).Scan(&value); err != nil {
			return err
		}
		summary.PostgreSQL.Settings[name] = value
	}
	return nil
}

func seedRetrievalCapacity(
	ctx context.Context,
	pool *pgxpool.Pool,
	seed string,
	chunkCount int64,
	dimensions int32,
) (retrievalCapacityFixture, error) {
	spec, err := capacity.DefaultSpec(seed)
	if err != nil {
		return retrievalCapacityFixture{}, err
	}
	spec.ChunkCount, spec.RelationCount = chunkCount, 1
	if err := spec.Validate(); err != nil {
		return retrievalCapacityFixture{}, err
	}
	now := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	fixture, err := newRetrievalCapacityFixture(seed, chunkCount, dimensions, now)
	if err != nil {
		return retrievalCapacityFixture{}, err
	}
	if err := cleanupRetrievalCapacity(ctx, pool, fixture); err != nil {
		return fixture, fmt.Errorf("remove previous Retrieval capacity fixture: %w", err)
	}
	if err := seedRetrievalCapacityFoundation(ctx, pool, fixture, now); err != nil {
		return fixture, cleanupRetrievalCapacityFailure(pool, fixture, err)
	}
	if err := insertRetrievalCapacityChunks(ctx, pool, fixture, now); err != nil {
		return fixture, cleanupRetrievalCapacityFailure(pool, fixture, err)
	}
	if err := insertRetrievalCapacityManifest(ctx, pool, fixture, now); err != nil {
		return fixture, cleanupRetrievalCapacityFailure(pool, fixture, err)
	}
	if err := insertRetrievalCapacityProjection(ctx, pool, fixture, now); err != nil {
		return fixture, cleanupRetrievalCapacityFailure(pool, fixture, err)
	}
	if err := activateRetrievalCapacityIndex(ctx, pool, fixture, now); err != nil {
		return fixture, cleanupRetrievalCapacityFailure(pool, fixture, err)
	}
	for _, statement := range []string{
		`ANALYZE ingestion.canonical_chunk`,
		`ANALYZE retrieval.index_manifest_chunk`,
		`ANALYZE retrieval.chunk_projection`,
		`ANALYZE retrieval.index_manifest_source`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return fixture, cleanupRetrievalCapacityFailure(pool, fixture, err)
		}
	}
	if err := verifyRetrievalCapacityFixture(ctx, pool, fixture); err != nil {
		return fixture, cleanupRetrievalCapacityFailure(pool, fixture, err)
	}
	return fixture, nil
}

func verifyRetrievalCapacityFixture(ctx context.Context, pool *pgxpool.Pool, fixture retrievalCapacityFixture) error {
	var chunks, manifestChunks, readyProjections, includedSources, activeIndexes int64
	err := pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*)::bigint FROM ingestion.canonical_chunk WHERE workspace_id=$1),
		(SELECT COUNT(*)::bigint FROM retrieval.index_manifest_chunk WHERE workspace_id=$1 AND index_version_id=$2),
		(SELECT COUNT(*)::bigint FROM retrieval.chunk_projection
			WHERE workspace_id=$1 AND index_version_id=$2 AND lexical_status='ready' AND search_vector IS NOT NULL
			  AND vector_status='ready' AND embedding_version_id=$4 AND embedding IS NOT NULL
			  AND vector_dims(embedding)=$5),
		(SELECT COUNT(*)::bigint FROM retrieval.index_manifest_source
			WHERE workspace_id=$1 AND index_version_id=$2 AND selection_status='included'),
		(SELECT COUNT(*)::bigint FROM retrieval.index_version
			WHERE workspace_id=$1 AND id=$2 AND status='active' AND version=3
			  AND embedding_version_id=$4 AND degraded_capabilities='[]'::jsonb
			  AND expected_chunk_count=$3 AND expected_source_count=1)`,
		string(fixture.WorkspaceID), string(fixture.IndexVersionID), fixture.ChunkCount,
		string(fixture.Embedding.ID), fixture.Embedding.Dimensions,
	).Scan(&chunks, &manifestChunks, &readyProjections, &includedSources, &activeIndexes)
	if err != nil {
		return fmt.Errorf("count Retrieval capacity fixture: %w", err)
	}
	if chunks != fixture.ChunkCount || manifestChunks != fixture.ChunkCount || readyProjections != fixture.ChunkCount ||
		includedSources != 1 || activeIndexes != 1 {
		return fmt.Errorf(
			"Retrieval capacity fixture counts chunks=%d manifest=%d ready=%d sources=%d active=%d",
			chunks, manifestChunks, readyProjections, includedSources, activeIndexes,
		)
	}
	return nil
}

func newRetrievalCapacityFixture(seed string, chunkCount int64, dimensions int32, now time.Time) (retrievalCapacityFixture, error) {
	ids := make([]foundation.ID, 10)
	for index, kind := range []string{
		"workspace", "source", "source-version", "projection", "span", "attempt", "index", "activation", "artifact", "embedding",
	} {
		parsed, err := foundation.ParseID(capacity.StableID(seed, "retrieval-"+kind, 0))
		if err != nil {
			return retrievalCapacityFixture{}, err
		}
		ids[index] = parsed
	}
	contract := domain.EmbeddingContract{
		Provider: "capacity", AdapterName: "deterministic", AdapterVersion: "v2", Model: "capacity-angle-v1",
		Dimensions: dimensions, Normalization: domain.NormalizationL2, DistanceMetric: domain.DistanceCosine,
		EndpointIdentity: "local://capacity-benchmark", MaxBatchSize: 1,
		MaxInputBytes: domain.MaxSearchQueryBytes, MaxBatchInputBytes: int64(domain.MaxSearchQueryBytes),
	}
	configHash, err := domain.ComputeEmbeddingConfigHash(contract)
	if err != nil {
		return retrievalCapacityFixture{}, err
	}
	contract.ConfigHash = configHash
	embedding := domain.EmbeddingVersion{
		ID: ids[9], Provider: contract.Provider, AdapterName: contract.AdapterName, AdapterVersion: contract.AdapterVersion,
		Model: contract.Model, Dimensions: contract.Dimensions, Normalization: contract.Normalization,
		DistanceMetric: contract.DistanceMetric, ConfigHash: contract.ConfigHash, CreatedAt: now,
	}
	return retrievalCapacityFixture{
		Version: "retrieval-capacity/m10-hybrid-v2", Seed: seed,
		WorkspaceID: ids[0], SourceID: ids[1], SourceVersionID: ids[2], ProjectionID: ids[3],
		SpanID: ids[4], AttemptID: ids[5], IndexVersionID: ids[6], ActivationID: ids[7], ArtifactID: ids[8],
		Embedding: embedding, Contract: contract, ChunkCount: chunkCount,
	}, nil
}

func seedRetrievalCapacityFoundation(
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture retrievalCapacityFixture,
	now time.Time,
) error {
	content := []byte(fixture.Version + "\n" + fixture.Seed)
	contentHash := retrievalCapacityHash(content)
	parserConfigHash := retrievalCapacityHash([]byte("retrieval-capacity-parser/v1"))
	root := retrievalCapacityWorkspaceRoot(fixture.WorkspaceID)
	return withRetrievalCapacityTransaction(ctx, pool, func(tx pgx.Tx) error {
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO core.workspace(
				id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
			) VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, []any{string(fixture.WorkspaceID), retrievalCapacityWorkspaceName, root, now}},
			{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
				VALUES($1,$2,$3,$4,$5,$6)`, []any{string(fixture.ArtifactID), string(fixture.WorkspaceID), contentHash, len(content), ".knowledge/sources/" + contentHash, now}},
			{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
				VALUES($1,$2,'text','capacity/retrieval.md','capacity/retrieval.md',$3)`, []any{string(fixture.SourceID), string(fixture.WorkspaceID), now}},
			{`INSERT INTO core.source_version(
				id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at
			) VALUES($1,$2,$3,$4,$5,$6,'text/markdown','capacity/retrieval.md','passed',$7)`, []any{
				string(fixture.SourceVersionID), string(fixture.SourceID), string(fixture.WorkspaceID), string(fixture.ArtifactID), contentHash, len(content), now,
			}},
			{`INSERT INTO ingestion.parse_projection(
				id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'[]',$9)`, []any{
				string(fixture.ProjectionID), string(fixture.WorkspaceID), string(fixture.ArtifactID), retrievalCapacityParserID,
				retrievalCapacityParserVersion, parserConfigHash, retrievalCapacitySchemaVersion, contentHash, now,
			}},
			{`INSERT INTO ingestion.source_span(
				id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,
				selector,excerpt_hash,parser_version,schema_version,created_at
			) VALUES($1,$2,$3,$4,'document',1,1,0,$5,'{}',$6,$7,$8,$9)`, []any{
				string(fixture.SpanID), string(fixture.WorkspaceID), string(fixture.ArtifactID), string(fixture.ProjectionID), len(content),
				contentHash, retrievalCapacityParserVersion, retrievalCapacitySchemaVersion, now,
			}},
			{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
				VALUES($1,$2,$3,$4)`, []any{string(fixture.SourceVersionID), string(fixture.ProjectionID), string(fixture.WorkspaceID), now}},
			{`INSERT INTO ingestion.attempt(
				id,workspace_id,source_version_id,parse_projection_id,status,security_status,parser_id,parser_version,
				parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,warnings,
				started_at,completed_at,version
			) VALUES($1,$2,$3,$4,'chunked','passed',$5,$6,$7,$8,$9,$10,1,'[]',$11,$11,1)`, []any{
				string(fixture.AttemptID), string(fixture.WorkspaceID), string(fixture.SourceVersionID), string(fixture.ProjectionID),
				retrievalCapacityParserID, retrievalCapacityParserVersion, parserConfigHash, retrievalCapacityChunkStrategy,
				retrievalCapacitySchemaVersion, "capacity-attempt-" + fixture.Seed, now,
			}},
			{`INSERT INTO retrieval.embedding_version(
				id,provider,adapter_name,adapter_version,model,dimensions,normalization,distance_metric,config_hash,created_at
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, []any{
				string(fixture.Embedding.ID), fixture.Embedding.Provider, fixture.Embedding.AdapterName,
				fixture.Embedding.AdapterVersion, fixture.Embedding.Model, fixture.Embedding.Dimensions,
				string(fixture.Embedding.Normalization), string(fixture.Embedding.DistanceMetric),
				fixture.Embedding.ConfigHash, fixture.Embedding.CreatedAt,
			}},
			{`INSERT INTO retrieval.index_version(
				id,workspace_id,embedding_version_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
				source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
				version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,
				source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version
			) VALUES($1,$2,$3,'simple','capacity-v1',$4,$5,$6,$7,$8,$9,'building','[]',
				1,$10,$10,$11,1,$12,$13,$14,$15,$16)`, []any{
				string(fixture.IndexVersionID), string(fixture.WorkspaceID), string(fixture.Embedding.ID),
				retrievalCapacityHash([]byte("capacity-tokenizer/v1")), retrievalCapacityFusionConfig(),
				"capacity:" + fixture.Seed, retrievalCapacityHash([]byte("manifest\x00" + fixture.Seed)), fixture.ChunkCount,
				"capacity-index-" + fixture.Seed, now, retrievalCapacityHash([]byte("source-manifest\x00" + fixture.Seed)),
				retrievalCapacityParserID, retrievalCapacityParserVersion, parserConfigHash,
				retrievalCapacityChunkStrategy, retrievalCapacitySchemaVersion,
			}},
			{`INSERT INTO retrieval.index_manifest_source(
				index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,exclusion_code,created_at
			) VALUES($1,$2,$3,$4,$5,'included',NULL,$6)`, []any{
				string(fixture.IndexVersionID), string(fixture.WorkspaceID), string(fixture.SourceID),
				string(fixture.SourceVersionID), string(fixture.ProjectionID), now,
			}},
		}
		for _, statement := range statements {
			if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		return nil
	})
}

func insertRetrievalCapacityChunks(ctx context.Context, pool *pgxpool.Pool, fixture retrievalCapacityFixture, now time.Time) error {
	for start := int64(0); start < fixture.ChunkCount; start += retrievalCapacityChunkWriteBatch {
		end := min(start+retrievalCapacityChunkWriteBatch, fixture.ChunkCount)
		if err := withRetrievalCapacityTransaction(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, insertRetrievalCapacityChunksSQL,
				fixture.Seed, string(fixture.WorkspaceID), start, end, string(fixture.ProjectionID), string(fixture.SpanID),
				retrievalCapacityParserVersion, retrievalCapacityChunkStrategy, retrievalCapacitySchemaVersion, now,
			)
			return err
		}); err != nil {
			return fmt.Errorf("commit Retrieval capacity chunks [%d,%d): %w", start, end, err)
		}
	}
	return nil
}

func insertRetrievalCapacityManifest(ctx context.Context, pool *pgxpool.Pool, fixture retrievalCapacityFixture, now time.Time) error {
	for start := int64(0); start < fixture.ChunkCount; start += retrievalCapacityManifestBatch {
		end := min(start+retrievalCapacityManifestBatch, fixture.ChunkCount)
		if err := withRetrievalCapacityTransaction(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO retrieval.index_manifest_chunk(
				index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at
			) SELECT $1,chunk.id,chunk.workspace_id,chunk.content_hash,chunk.sequence,chunk.parser_version,
				chunk.chunk_strategy_version,chunk.schema_version,$5
			FROM ingestion.canonical_chunk chunk
			WHERE chunk.workspace_id=$2 AND chunk.sequence >= $3 AND chunk.sequence < $4
			ORDER BY chunk.sequence`, string(fixture.IndexVersionID), string(fixture.WorkspaceID), start, end, now)
			return err
		}); err != nil {
			return fmt.Errorf("commit Retrieval capacity manifest [%d,%d): %w", start, end, err)
		}
	}
	return nil
}

func insertRetrievalCapacityProjection(ctx context.Context, pool *pgxpool.Pool, fixture retrievalCapacityFixture, now time.Time) error {
	for start := int64(0); start < fixture.ChunkCount; start += retrievalCapacityProjectionBatch {
		end := min(start+retrievalCapacityProjectionBatch, fixture.ChunkCount)
		if err := withRetrievalCapacityTransaction(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `WITH lexical AS (
				SELECT manifest.index_version_id,manifest.chunk_id,manifest.workspace_id,manifest.sequence,
					setweight(to_tsvector('simple',coalesce(chunk.heading_path::text,'')),'A') ||
					setweight(to_tsvector('simple',chunk.content),'B') AS search_vector
				FROM retrieval.index_manifest_chunk manifest
				JOIN ingestion.canonical_chunk chunk ON chunk.id=manifest.chunk_id AND chunk.workspace_id=manifest.workspace_id
				WHERE manifest.index_version_id=$1 AND manifest.workspace_id=$2
				  AND manifest.sequence >= $3 AND manifest.sequence < $4
			), projected AS (
				SELECT index_version_id,chunk_id,workspace_id,search_vector,
					('[' || cos(((sequence + 1)::double precision / ($7::double precision + 1.0)) * (pi() / 3.0))::text || ',' ||
					 sin(((sequence + 1)::double precision / ($7::double precision + 1.0)) * (pi() / 3.0))::text ||
					 repeat(',0',$6::int - 2) || ']')::vector AS embedding
				FROM lexical
			)
			INSERT INTO retrieval.chunk_projection(
				index_version_id,chunk_id,workspace_id,embedding_version_id,search_vector,embedding,token_count,
				lexical_status,vector_status,failure_code,created_at,updated_at
			) SELECT index_version_id,chunk_id,workspace_id,$5,search_vector,embedding,length(search_vector),
				'ready','ready',NULL,$8,$8 FROM projected`,
				string(fixture.IndexVersionID), string(fixture.WorkspaceID), start, end,
				string(fixture.Embedding.ID), fixture.Embedding.Dimensions, fixture.ChunkCount, now)
			return err
		}); err != nil {
			return fmt.Errorf("commit Retrieval capacity projection [%d,%d): %w", start, end, err)
		}
	}
	return nil
}

func activateRetrievalCapacityIndex(ctx context.Context, pool *pgxpool.Pool, fixture retrievalCapacityFixture, now time.Time) error {
	return withRetrievalCapacityTransaction(ctx, pool, func(tx pgx.Tx) error {
		readyAt := now.Add(time.Minute)
		if _, err := tx.Exec(ctx, `UPDATE retrieval.index_version SET status='ready',version=2,built_at=$2,updated_at=$2
			WHERE id=$1 AND workspace_id=$3 AND status='building' AND version=1`,
			string(fixture.IndexVersionID), readyAt, string(fixture.WorkspaceID)); err != nil {
			return err
		}
		activeAt := readyAt.Add(time.Minute)
		if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_activation(
			id,kind,workspace_id,target_index_version_id,previous_index_version_id,target_version,previous_version,
			idempotency_key,reason_code,created_at
		) VALUES($1,'activate',$2,$3,NULL,3,NULL,$4,'CAPACITY_VERIFIED',$5)`,
			string(fixture.ActivationID), string(fixture.WorkspaceID), string(fixture.IndexVersionID),
			"capacity-activate-"+fixture.Seed, activeAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE retrieval.index_version SET status='active',version=3,activated_at=$2,updated_at=$2
			WHERE id=$1 AND workspace_id=$3 AND status='ready' AND version=2`,
			string(fixture.IndexVersionID), activeAt, string(fixture.WorkspaceID)); err != nil {
			return err
		}
		return nil
	})
}

func explainRetrievalCapacity(
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture retrievalCapacityFixture,
	request application.LexicalSearchQuery,
) (json.RawMessage, retrievalCapacityExplain, error) {
	explainContext, cancel := context.WithTimeout(ctx, retrievalCapacityExplainTimeout)
	defer cancel()
	arguments := []any{
		string(fixture.WorkspaceID), string(fixture.IndexVersionID), strings.TrimSpace(request.Query), request.Limit,
		domain.MaxEvidenceProvenance,
	}
	query := fmt.Sprintf(lexicalCandidateSQL, appendSearchFilter(&arguments, request.Filter), searchSnippetCharacterLimit, searchRerankCharacterLimit)
	tx, err := pool.Begin(explainContext)
	if err != nil {
		return nil, retrievalCapacityExplain{}, err
	}
	defer func() { _ = tx.Rollback(explainContext) }()
	if _, err := tx.Exec(explainContext, `SELECT set_config('pg_trgm.similarity_threshold',$1,true)`, searchTrigramThreshold); err != nil {
		return nil, retrievalCapacityExplain{}, err
	}
	var raw json.RawMessage
	if err := tx.QueryRow(explainContext, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF, SETTINGS) `+query, arguments...).Scan(&raw); err != nil {
		return nil, retrievalCapacityExplain{}, err
	}
	if err := tx.Commit(explainContext); err != nil {
		return nil, retrievalCapacityExplain{}, err
	}
	explain, err := inspectRetrievalCapacityPlan(raw)
	return append(json.RawMessage(nil), raw...), explain, err
}

func inspectRetrievalCapacityPlan(raw json.RawMessage) (retrievalCapacityExplain, error) {
	return inspectRetrievalCapacityPlanForIndexes(
		raw,
		"lexical.explain.json",
		[]string{"idx_retrieval_projection_search_vector", "idx_ingestion_canonical_chunk_content_trgm"},
	)
}

func inspectRetrievalCapacityPlanForIndexes(raw json.RawMessage, file string, required []string) (retrievalCapacityExplain, error) {
	var documents []map[string]any
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		return retrievalCapacityExplain{}, errors.New("Retrieval capacity EXPLAIN JSON is invalid")
	}
	found, sequential := map[string]struct{}{}, map[string]struct{}{}
	visitRetrievalCapacityPlan(documents[0]["Plan"], func(node map[string]any) {
		if indexName, ok := node["Index Name"].(string); ok {
			found[indexName] = struct{}{}
		}
		if node["Node Type"] == "Seq Scan" {
			if relationName, ok := node["Relation Name"].(string); ok {
				sequential[relationName] = struct{}{}
			}
		}
	})
	for _, indexName := range required {
		if _, ok := found[indexName]; !ok {
			return retrievalCapacityExplain{}, fmt.Errorf("Retrieval capacity plan does not use %s", indexName)
		}
	}
	metadata := retrievalCapacityExplain{File: file, RequiredIndexes: append([]string(nil), required...)}
	metadata.PlanningMilliseconds, _ = documents[0]["Planning Time"].(float64)
	metadata.ExecutionMilliseconds, _ = documents[0]["Execution Time"].(float64)
	for value := range found {
		metadata.IndexNames = append(metadata.IndexNames, value)
	}
	for value := range sequential {
		metadata.SequentialScans = append(metadata.SequentialScans, value)
	}
	sort.Strings(metadata.IndexNames)
	sort.Strings(metadata.SequentialScans)
	return metadata, nil
}

func visitRetrievalCapacityPlan(value any, visit func(map[string]any)) {
	node, ok := value.(map[string]any)
	if !ok {
		return
	}
	visit(node)
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		visitRetrievalCapacityPlan(child, visit)
	}
}

func cleanupRetrievalCapacityFailure(pool *pgxpool.Pool, fixture retrievalCapacityFixture, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if cleanupErr := cleanupRetrievalCapacity(ctx, pool, fixture); cleanupErr != nil {
		return errors.Join(cause, fmt.Errorf("cleanup partial Retrieval capacity fixture: %w", cleanupErr))
	}
	return cause
}

func cleanupRetrievalCapacity(ctx context.Context, pool *pgxpool.Pool, fixture retrievalCapacityFixture) error {
	if pool == nil {
		return errors.New("Retrieval capacity cleanup pool is nil")
	}
	for _, method := range []string{"hnsw", "ivfflat"} {
		indexName := fmt.Sprintf("idx_capacity_retrieval_%s_%d", method, fixture.Embedding.Dimensions)
		if err := dropRetrievalCapacityIndex(ctx, pool, indexName); err != nil {
			return err
		}
	}
	root := retrievalCapacityWorkspaceRoot(fixture.WorkspaceID)
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	var name, rootPath, repositoryPath, status string
	var version int64
	err = tx.QueryRow(ctx, `SELECT name,root_path,git_repository_path,status,version
		FROM core.workspace WHERE id=$1 FOR UPDATE`, string(fixture.WorkspaceID)).Scan(&name, &rootPath, &repositoryPath, &status, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		committed = true
		return nil
	}
	if err != nil {
		return err
	}
	if name != retrievalCapacityWorkspaceName || rootPath != root || repositoryPath != root || status != "test" || version != 1 {
		return errors.New("Retrieval capacity cleanup workspace marker does not match")
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		return err
	}
	for _, statement := range []string{
		`DELETE FROM retrieval.chunk_projection WHERE workspace_id=$1`,
		`DELETE FROM retrieval.index_manifest_chunk WHERE workspace_id=$1`,
		`DELETE FROM retrieval.index_manifest_source WHERE workspace_id=$1`,
		`DELETE FROM retrieval.index_activation WHERE workspace_id=$1`,
		`DELETE FROM retrieval.index_version WHERE workspace_id=$1`,
		`DELETE FROM ingestion.canonical_chunk WHERE workspace_id=$1`,
		`DELETE FROM ingestion.attempt WHERE workspace_id=$1`,
		`DELETE FROM ingestion.source_version_projection WHERE workspace_id=$1`,
		`DELETE FROM ingestion.source_span WHERE workspace_id=$1`,
		`DELETE FROM ingestion.parse_projection WHERE workspace_id=$1`,
		`DELETE FROM core.source_version WHERE source_id IN (SELECT id FROM core.source WHERE workspace_id=$1)`,
		`DELETE FROM core.source WHERE workspace_id=$1`,
		`DELETE FROM core.content_artifact WHERE workspace_id=$1`,
		`DELETE FROM core.workspace WHERE id=$1`,
	} {
		if _, err := tx.Exec(ctx, statement, string(fixture.WorkspaceID)); err != nil {
			return err
		}
	}
	if fixture.Embedding.ID != "" {
		if _, err := tx.Exec(ctx, `DELETE FROM retrieval.embedding_version WHERE id=$1`, string(fixture.Embedding.ID)); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

func withRetrievalCapacityTransaction(ctx context.Context, pool *pgxpool.Pool, operation func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err := operation(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

func retrievalCapacityWorkspaceRoot(workspaceID foundation.ID) string {
	return "/tmp/" + retrievalCapacityWorkspaceRootPre + "-" + string(workspaceID)
}

func retrievalCapacityHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func retrievalCapacityFusionConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":1,"method":"rrf","k":60,"lexical_candidate_limit":40,"vector_candidate_limit":40,"fused_candidate_limit":40,"rerank_candidate_limit":20}`)
}

const insertRetrievalCapacityChunksSQL = `
WITH generated AS (
	SELECT ordinal,
		CASE WHEN ordinal % 1000 = 0
			THEN 'capacityneedle deterministic retrieval benchmark chunk ' || to_char(ordinal,'FM000000')
			ELSE 'background deterministic retrieval corpus shard ' || to_char(ordinal,'FM000000')
		END AS content
	FROM generate_series($3::bigint,$4::bigint-1) AS series(ordinal)
), hashed AS (
	SELECT ordinal,content,encode(sha256(convert_to(content,'UTF8')),'hex') AS content_hash
	FROM generated
)
INSERT INTO ingestion.canonical_chunk(
	id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,
	byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at
)
SELECT md5($1 || ':chunk:' || ordinal::text)::uuid,$2::uuid,$5::uuid,ordinal::int,
	'["M10 Capacity"]'::jsonb,content,content_hash,$6::uuid,octet_length(content),char_length(content),
	$7,$8,$9,false,'active',$10
FROM hashed`
