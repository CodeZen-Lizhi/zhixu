//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/graph/testfixture"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	graphBenchmarkWarmups       = 5
	graphBenchmarkSamples       = 30
	graphBenchmarkP95Limit      = 1500 * time.Millisecond
	graphBenchmarkQueryTimeout  = 2 * time.Second
	graphNeighborhoodStatements = 6
	defaultGraphBenchmarkSeed   = "m7-01-reference-v1"
)

type graphBenchmarkSample struct {
	Sample               int     `json:"sample"`
	DurationNanoseconds  int64   `json:"duration_nanoseconds"`
	DurationMilliseconds float64 `json:"duration_milliseconds"`
	ReturnedNodes        int     `json:"returned_nodes"`
	ReturnedEdges        int     `json:"returned_edges"`
	HasNextPage          bool    `json:"has_next_page"`
	DatabaseStatements   int64   `json:"database_statement_count"`
}

type graphQueryCounter struct{ count atomic.Int64 }

func (counter *graphQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	counter.count.Add(1)
	return ctx
}

func (*graphQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (counter *graphQueryCounter) reset() { counter.count.Store(0) }

func (counter *graphQueryCounter) value() int64 { return counter.count.Load() }

type graphBenchmarkMemory struct {
	AllocBytes      uint64 `json:"alloc_bytes"`
	TotalAllocBytes uint64 `json:"total_alloc_bytes"`
	SysBytes        uint64 `json:"sys_bytes"`
	HeapAllocBytes  uint64 `json:"heap_alloc_bytes"`
	HeapInuseBytes  uint64 `json:"heap_inuse_bytes"`
	HeapObjects     uint64 `json:"heap_objects"`
	NumGC           uint32 `json:"num_gc"`
}

type graphExplainMetadata struct {
	File                    string   `json:"file"`
	RequiredIndexes         []string `json:"required_indexes"`
	PlanningMilliseconds    float64  `json:"planning_milliseconds"`
	ExecutionMilliseconds   float64  `json:"execution_milliseconds"`
	RelationSequentialScans int      `json:"relation_sequential_scans"`
	EvidenceSequentialScans int      `json:"evidence_sequential_scans"`
}

type graphBenchmarkSummary struct {
	SchemaVersion string    `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Fixture       struct {
		Version            string  `json:"version"`
		Seed               string  `json:"seed"`
		WorkspaceID        string  `json:"workspace_id"`
		CenterTopicID      string  `json:"center_topic_id"`
		PathTargetTopicID  string  `json:"path_target_topic_id"`
		EvidenceRelationID string  `json:"evidence_relation_id"`
		SeedMilliseconds   float64 `json:"seed_milliseconds"`
	} `json:"fixture"`
	Counts struct {
		Nodes           int `json:"nodes"`
		Relations       int `json:"relations"`
		Evidence        int `json:"evidence"`
		HotCenterDegree int `json:"hot_center_degree"`
	} `json:"counts"`
	PostgreSQL struct {
		Version            string `json:"version"`
		VersionNumber      string `json:"version_number"`
		SharedBuffers      string `json:"shared_buffers"`
		WorkMemory         string `json:"work_memory"`
		EffectiveCacheSize string `json:"effective_cache_size"`
		PoolMaxConnections int32  `json:"pool_max_connections"`
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
	RuntimeMemory struct {
		Before graphBenchmarkMemory `json:"before"`
		After  graphBenchmarkMemory `json:"after"`
	} `json:"runtime_memory"`
	Workload struct {
		Concurrency                  int     `json:"concurrency"`
		Depth                        int     `json:"depth"`
		PageLimit                    int     `json:"page_limit"`
		MaxNodes                     int     `json:"max_nodes"`
		MaxEdges                     int     `json:"max_edges"`
		MaxFrontier                  int     `json:"max_frontier"`
		ExpectedWindow               int     `json:"expected_window_edges"`
		DatabaseStatements           int64   `json:"database_statement_count"`
		StatementCountInvariant      bool    `json:"statement_count_invariant"`
		StatementTimeoutMilliseconds float64 `json:"statement_timeout_milliseconds"`
		QueryTimeoutMilliseconds     float64 `json:"query_timeout_milliseconds"`
	} `json:"workload"`
	Latency struct {
		WarmupCount           int     `json:"warmup_count"`
		SampleCount           int     `json:"sample_count"`
		Percentile            int     `json:"percentile"`
		P50Nanoseconds        int64   `json:"p50_nanoseconds"`
		P50Milliseconds       float64 `json:"p50_milliseconds"`
		P95Nanoseconds        int64   `json:"p95_nanoseconds"`
		P95Milliseconds       float64 `json:"p95_milliseconds"`
		MaxNanoseconds        int64   `json:"max_nanoseconds"`
		MaxMilliseconds       float64 `json:"max_milliseconds"`
		ThresholdNanoseconds  int64   `json:"threshold_nanoseconds"`
		ThresholdMilliseconds float64 `json:"threshold_milliseconds"`
		Passed                bool    `json:"passed"`
	} `json:"latency"`
	Smoke struct {
		PathMilliseconds     float64 `json:"path_milliseconds"`
		PathHopCount         int     `json:"path_hop_count"`
		EvidenceMilliseconds float64 `json:"evidence_milliseconds"`
		EvidenceItems        int     `json:"evidence_items"`
	} `json:"smoke"`
	Explain map[string]graphExplainMetadata `json:"explain"`
}

// TestGraphCapacityBenchmark 验证 20k/100k Graph 一跳 p95 与生产 SQL 索引计划。
func TestGraphCapacityBenchmark(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	artifactDirectory := os.Getenv("ZHIXU_GRAPH_BENCHMARK_ARTIFACT_DIR")
	if databaseURL == "" || artifactDirectory == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL and ZHIXU_GRAPH_BENCHMARK_ARTIFACT_DIR to run the Graph capacity benchmark")
	}
	seed := os.Getenv("ZHIXU_GRAPH_BENCHMARK_SEED")
	if seed == "" {
		seed = defaultGraphBenchmarkSeed
	}
	if err := os.MkdirAll(artifactDirectory, 0o755); err != nil {
		t.Fatalf("create Graph benchmark artifact directory: %v", err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("parse Graph benchmark database configuration")
	}
	queryCounter := &graphQueryCounter{}
	poolConfig.ConnConfig.Tracer = queryCounter
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("open Graph benchmark database pool: %v", err)
	}
	t.Cleanup(pool.Close)

	seedStarted := time.Now()
	fixture, err := testfixture.SeedCapacity(ctx, pool, seed)
	if err != nil {
		t.Fatalf("seed Graph capacity fixture: %v", err)
	}
	seedDuration := time.Since(seedStarted)
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cleanupCancel()
		if cleanupErr := testfixture.CleanupCapacity(cleanupContext, pool, fixture.WorkspaceID); cleanupErr != nil {
			t.Errorf("cleanup Graph capacity fixture: %v", cleanupErr)
		}
	})

	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatalf("construct Graph benchmark repository: %v", err)
	}
	cursor, err := graphapp.NewCursorCodec(bytes.Repeat([]byte{0x67}, 32))
	if err != nil {
		t.Fatalf("construct Graph benchmark cursor: %v", err)
	}
	service, err := graphapp.NewService(repository, cursor)
	if err != nil {
		t.Fatalf("construct Graph benchmark service: %v", err)
	}
	request := graphdomain.NeighborhoodRequest{
		WorkspaceID: fixture.WorkspaceID,
		Center:      knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.CenterTopicID},
		Depth:       1,
		Limit:       graphdomain.MaxLimit,
		Direction:   graphdomain.TraversalBoth,
		Filter: graphdomain.GraphFilter{
			NodeTypes:        []knowledge.NodeType{knowledge.NodeTypeTopic},
			RelationTypes:    []knowledge.RelationType{knowledge.RelationImpacts},
			RelationStatuses: []knowledge.RelationStatus{knowledge.RelationStatusConfirmed},
		},
		MaxNodes:    graphdomain.MaxNodes,
		MaxEdges:    fixture.HotCenterDegree,
		MaxFrontier: graphdomain.MaxNodes,
	}

	var memoryBefore runtime.MemStats
	runtime.ReadMemStats(&memoryBefore)
	for warmup := 0; warmup < graphBenchmarkWarmups; warmup++ {
		result, _, statementCount, queryErr := runCapacityNeighborhood(ctx, service, queryCounter, request)
		if queryErr != nil {
			t.Fatalf("Graph capacity warmup %d: %v", warmup+1, queryErr)
		}
		assertCapacityNeighborhood(t, result)
		if statementCount != graphNeighborhoodStatements {
			t.Fatalf("Graph capacity warmup %d statements=%d want=%d", warmup+1, statementCount, graphNeighborhoodStatements)
		}
	}

	samples := make([]graphBenchmarkSample, 0, graphBenchmarkSamples)
	durations := make([]time.Duration, 0, graphBenchmarkSamples)
	for sampleIndex := 0; sampleIndex < graphBenchmarkSamples; sampleIndex++ {
		result, duration, statementCount, queryErr := runCapacityNeighborhood(ctx, service, queryCounter, request)
		if queryErr != nil {
			t.Fatalf("Graph capacity sample %d: %v", sampleIndex+1, queryErr)
		}
		assertCapacityNeighborhood(t, result)
		if statementCount != graphNeighborhoodStatements {
			t.Fatalf("Graph capacity sample %d statements=%d want=%d", sampleIndex+1, statementCount, graphNeighborhoodStatements)
		}
		durations = append(durations, duration)
		samples = append(samples, graphBenchmarkSample{
			Sample: sampleIndex + 1, DurationNanoseconds: duration.Nanoseconds(),
			DurationMilliseconds: durationMilliseconds(duration), ReturnedNodes: len(result.Nodes),
			ReturnedEdges: len(result.Edges), HasNextPage: result.Meta.NextCursor != "", DatabaseStatements: statementCount,
		})
	}
	var memoryAfter runtime.MemStats
	runtime.ReadMemStats(&memoryAfter)
	p50, p95, maximum := percentileNearestRank(durations, 50), percentileNearestRank(durations, 95), maximumDuration(durations)

	pathStarted := time.Now()
	pathContext, pathCancel := context.WithTimeout(ctx, graphBenchmarkQueryTimeout)
	path, err := service.FindPath(pathContext, graphdomain.PathRequest{
		WorkspaceID:   fixture.WorkspaceID,
		From:          knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.CenterTopicID},
		To:            knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.PathTargetTopicID},
		Direction:     graphdomain.TraversalBoth,
		RelationTypes: []knowledge.RelationType{knowledge.RelationImpacts},
		MaxDepth:      2,
		MaxVisited:    graphdomain.MaxNodes,
	})
	pathDuration := time.Since(pathStarted)
	pathCancel()
	if err != nil || path.Status != graphdomain.PathFound || path.HopCount != 1 {
		t.Fatalf("Graph capacity path=%#v err=%v", path, err)
	}
	evidenceStarted := time.Now()
	evidenceContext, evidenceCancel := context.WithTimeout(ctx, graphBenchmarkQueryTimeout)
	evidence, err := service.RelationEvidence(evidenceContext, fixture.WorkspaceID, fixture.EvidenceRelationID, graphdomain.DefaultEvidenceLimit)
	evidenceDuration := time.Since(evidenceStarted)
	evidenceCancel()
	if err != nil || len(evidence.Items) != 1 || evidence.Items[0].RelationID != fixture.EvidenceRelationID {
		t.Fatalf("Graph capacity evidence=%#v err=%v", evidence, err)
	}

	plans := captureCapacityPlans(t, ctx, pool, fixture)
	if err := writeBenchmarkSamples(filepath.Join(artifactDirectory, "samples.jsonl"), samples); err != nil {
		t.Fatalf("write Graph benchmark samples: %v", err)
	}
	for name, plan := range plans.raw {
		if err := writeArtifact(filepath.Join(artifactDirectory, name+".explain.json"), plan); err != nil {
			t.Fatalf("write Graph benchmark %s plan: %v", name, err)
		}
	}

	summary, err := newGraphBenchmarkSummary(ctx, fixture, seedDuration, pool, memoryBefore, memoryAfter, p50, p95, maximum, pathDuration, path.HopCount, evidenceDuration, len(evidence.Items), plans.metadata)
	if err != nil {
		t.Fatalf("read Graph benchmark environment metadata: %v", err)
	}
	if err := writeJSONArtifact(filepath.Join(artifactDirectory, "summary.json"), summary); err != nil {
		t.Fatalf("write Graph benchmark summary: %v", err)
	}
	if p95 > graphBenchmarkP95Limit {
		t.Fatalf("Graph capacity p95 %s exceeds %s", p95, graphBenchmarkP95Limit)
	}
}

func assertCapacityNeighborhood(t *testing.T, result graphdomain.Neighborhood) {
	t.Helper()
	if result.CompletedDepth != 1 || result.Meta.Truncated || len(result.Edges) != graphdomain.MaxLimit ||
		len(result.Nodes) != graphdomain.MaxLimit+1 || result.Meta.NextCursor == "" || len(result.LayerCounts) != 1 ||
		result.LayerCounts[0] != graphdomain.MaxLimit {
		t.Fatalf("unexpected Graph capacity neighborhood: nodes=%d edges=%d depth=%d meta=%#v layers=%v",
			len(result.Nodes), len(result.Edges), result.CompletedDepth, result.Meta, result.LayerCounts)
	}
}

func runCapacityNeighborhood(
	ctx context.Context,
	service *graphapp.Service,
	counter *graphQueryCounter,
	request graphdomain.NeighborhoodRequest,
) (graphdomain.Neighborhood, time.Duration, int64, error) {
	queryContext, cancel := context.WithTimeout(ctx, graphBenchmarkQueryTimeout)
	defer cancel()
	counter.reset()
	started := time.Now()
	result, err := service.Neighborhood(queryContext, request)
	return result, time.Since(started), counter.value(), err
}

type capacityPlans struct {
	raw      map[string]json.RawMessage
	metadata map[string]graphExplainMetadata
}

func captureCapacityPlans(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture testfixture.CapacityFixture) capacityPlans {
	t.Helper()
	queries := []struct {
		name            string
		query           string
		args            []any
		requiredIndexes []string
	}{
		{
			name: "neighborhood", query: `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF, SETTINGS) ` + neighborhoodDepthOneSQL,
			args:            []any{string(fixture.WorkspaceID), "TOPIC", string(fixture.CenterTopicID), "BOTH", []string{"CONFIRMED"}, []string{"IMPACTS"}, (*float64)(nil), (*time.Time)(nil), []string{"TOPIC"}, []string{}, []string{"CONFIRMED", "DISPUTED"}, (*float64)(nil), 500},
			requiredIndexes: []string{"idx_knowledge_relation_source", "idx_knowledge_relation_target", "idx_knowledge_relation_evidence_owner"},
		},
		{
			name: "path", query: `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF, SETTINGS) ` + pathFrontierSQL,
			args:            []any{string(fixture.WorkspaceID), []string{"TOPIC"}, []string{string(fixture.CenterTopicID)}, "BOTH", []string{"IMPACTS"}, 1001},
			requiredIndexes: []string{"idx_knowledge_relation_source", "idx_knowledge_relation_target"},
		},
		{
			name: "evidence", query: `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF, SETTINGS) ` + relationEvidenceWindowSQL,
			args:            []any{string(fixture.WorkspaceID), string(fixture.EvidenceRelationID), 501},
			requiredIndexes: []string{"idx_knowledge_relation_evidence_owner"},
		},
	}
	result := capacityPlans{raw: make(map[string]json.RawMessage, len(queries)), metadata: make(map[string]graphExplainMetadata, len(queries))}
	for _, query := range queries {
		var raw json.RawMessage
		if err := pool.QueryRow(ctx, query.query, query.args...).Scan(&raw); err != nil {
			t.Fatalf("explain Graph capacity %s: %v", query.name, err)
		}
		metadata, err := inspectCapacityPlan(raw, query.name+".explain.json", query.requiredIndexes)
		if err != nil {
			t.Fatalf("inspect Graph capacity %s plan: %v", query.name, err)
		}
		result.raw[query.name] = append(json.RawMessage(nil), raw...)
		result.metadata[query.name] = metadata
	}
	return result
}

func inspectCapacityPlan(raw json.RawMessage, file string, requiredIndexes []string) (graphExplainMetadata, error) {
	var documents []map[string]any
	if err := json.Unmarshal(raw, &documents); err != nil {
		return graphExplainMetadata{}, fmt.Errorf("invalid EXPLAIN JSON: %w", err)
	}
	if len(documents) != 1 {
		return graphExplainMetadata{}, fmt.Errorf("invalid EXPLAIN document count %d", len(documents))
	}
	found := make(map[string]bool, len(requiredIndexes))
	metadata := graphExplainMetadata{File: file, RequiredIndexes: append([]string(nil), requiredIndexes...)}
	if value, ok := documents[0]["Planning Time"].(float64); ok {
		metadata.PlanningMilliseconds = value
	}
	if value, ok := documents[0]["Execution Time"].(float64); ok {
		metadata.ExecutionMilliseconds = value
	}
	visitCapacityPlan(documents[0]["Plan"], func(node map[string]any) {
		if indexName, ok := node["Index Name"].(string); ok {
			found[indexName] = true
		}
		if node["Node Type"] != "Seq Scan" {
			return
		}
		switch node["Relation Name"] {
		case "relation":
			metadata.RelationSequentialScans++
		case "relation_evidence":
			metadata.EvidenceSequentialScans++
		}
	})
	for _, indexName := range requiredIndexes {
		if !found[indexName] {
			return graphExplainMetadata{}, fmt.Errorf("plan does not use %s", indexName)
		}
	}
	if metadata.RelationSequentialScans != 0 || metadata.EvidenceSequentialScans != 0 {
		return graphExplainMetadata{}, fmt.Errorf("plan has relation seq scans=%d evidence seq scans=%d", metadata.RelationSequentialScans, metadata.EvidenceSequentialScans)
	}
	return metadata, nil
}

func visitCapacityPlan(value any, visit func(map[string]any)) {
	node, ok := value.(map[string]any)
	if !ok {
		return
	}
	visit(node)
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		visitCapacityPlan(child, visit)
	}
}

func newGraphBenchmarkSummary(
	ctx context.Context,
	fixture testfixture.CapacityFixture,
	seedDuration time.Duration,
	pool *pgxpool.Pool,
	memoryBefore, memoryAfter runtime.MemStats,
	p50, p95, maximum, pathDuration time.Duration,
	pathHopCount int,
	evidenceDuration time.Duration,
	evidenceItems int,
	plans map[string]graphExplainMetadata,
) (graphBenchmarkSummary, error) {
	summary := graphBenchmarkSummary{SchemaVersion: "zhixu-graph-benchmark/v1", GeneratedAt: time.Now().UTC(), Explain: plans}
	summary.Fixture.Version, summary.Fixture.Seed = fixture.Version, fixture.Seed
	summary.Fixture.WorkspaceID, summary.Fixture.CenterTopicID = string(fixture.WorkspaceID), string(fixture.CenterTopicID)
	summary.Fixture.PathTargetTopicID, summary.Fixture.EvidenceRelationID = string(fixture.PathTargetTopicID), string(fixture.EvidenceRelationID)
	summary.Fixture.SeedMilliseconds = durationMilliseconds(seedDuration)
	summary.Counts.Nodes, summary.Counts.Relations = fixture.NodeCount, fixture.RelationCount
	summary.Counts.Evidence, summary.Counts.HotCenterDegree = fixture.EvidenceCount, fixture.HotCenterDegree
	summary.PostgreSQL.PoolMaxConnections = pool.Stat().MaxConns()
	if err := pool.QueryRow(ctx, `SELECT
		current_setting('server_version'),current_setting('server_version_num'),current_setting('shared_buffers'),
		current_setting('work_mem'),current_setting('effective_cache_size')`).Scan(
		&summary.PostgreSQL.Version, &summary.PostgreSQL.VersionNumber, &summary.PostgreSQL.SharedBuffers,
		&summary.PostgreSQL.WorkMemory, &summary.PostgreSQL.EffectiveCacheSize,
	); err != nil {
		return graphBenchmarkSummary{}, err
	}
	summary.Go.Version, summary.Go.OS, summary.Go.Arch = runtime.Version(), runtime.GOOS, runtime.GOARCH
	summary.CPU.LogicalCPUs, summary.CPU.GOMAXPROCS = runtime.NumCPU(), runtime.GOMAXPROCS(0)
	summary.RuntimeMemory.Before, summary.RuntimeMemory.After = benchmarkMemory(memoryBefore), benchmarkMemory(memoryAfter)
	summary.Workload.Concurrency, summary.Workload.Depth = 1, 1
	summary.Workload.PageLimit, summary.Workload.MaxNodes = graphdomain.MaxLimit, graphdomain.MaxNodes
	summary.Workload.MaxEdges, summary.Workload.MaxFrontier = fixture.HotCenterDegree, graphdomain.MaxNodes
	summary.Workload.ExpectedWindow = fixture.HotCenterDegree
	summary.Workload.DatabaseStatements = graphNeighborhoodStatements
	summary.Workload.StatementCountInvariant = true
	summary.Workload.StatementTimeoutMilliseconds = durationMilliseconds(defaultStatementTimeout)
	summary.Workload.QueryTimeoutMilliseconds = durationMilliseconds(graphBenchmarkQueryTimeout)
	summary.Latency.WarmupCount, summary.Latency.SampleCount, summary.Latency.Percentile = graphBenchmarkWarmups, graphBenchmarkSamples, 95
	summary.Latency.P50Nanoseconds, summary.Latency.P50Milliseconds = p50.Nanoseconds(), durationMilliseconds(p50)
	summary.Latency.P95Nanoseconds, summary.Latency.P95Milliseconds = p95.Nanoseconds(), durationMilliseconds(p95)
	summary.Latency.MaxNanoseconds, summary.Latency.MaxMilliseconds = maximum.Nanoseconds(), durationMilliseconds(maximum)
	summary.Latency.ThresholdNanoseconds = graphBenchmarkP95Limit.Nanoseconds()
	summary.Latency.ThresholdMilliseconds = durationMilliseconds(graphBenchmarkP95Limit)
	summary.Latency.Passed = p95 <= graphBenchmarkP95Limit
	summary.Smoke.PathMilliseconds, summary.Smoke.PathHopCount = durationMilliseconds(pathDuration), pathHopCount
	summary.Smoke.EvidenceMilliseconds, summary.Smoke.EvidenceItems = durationMilliseconds(evidenceDuration), evidenceItems
	return summary, nil
}

func benchmarkMemory(value runtime.MemStats) graphBenchmarkMemory {
	return graphBenchmarkMemory{
		AllocBytes: value.Alloc, TotalAllocBytes: value.TotalAlloc, SysBytes: value.Sys,
		HeapAllocBytes: value.HeapAlloc, HeapInuseBytes: value.HeapInuse,
		HeapObjects: value.HeapObjects, NumGC: value.NumGC,
	}
}

func percentileNearestRank(values []time.Duration, percentile int) time.Duration {
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
	rank := (percentile*len(ordered) + 99) / 100
	return ordered[rank-1]
}

func maximumDuration(values []time.Duration) time.Duration {
	maximum := time.Duration(0)
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

func writeBenchmarkSamples(path string, samples []graphBenchmarkSample) error {
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	for _, sample := range samples {
		if err := encoder.Encode(sample); err != nil {
			return err
		}
	}
	return writeArtifact(path, payload.Bytes())
}

func writeJSONArtifact(path string, value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeArtifact(path, payload)
}

func writeArtifact(path string, payload []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".graph-benchmark-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}
