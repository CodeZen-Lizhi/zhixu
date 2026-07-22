//go:build integration

package retrievalhttp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/app"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformfilesystem "github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	retrievalworkspace "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/workspace"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	retrievalhttp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/http"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

func TestPostgresRouterSearchEvidenceCursorAndExplain(t *testing.T) {
	database, ctx := newHTTPIntegrationDatabase(t)
	repository, err := retrievalpostgres.NewRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	searchRepository, err := retrievalpostgres.NewSearchRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	workspaceRepository, err := workspacepostgres.NewRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC)
	contract := httpIntegrationEmbeddingContract(t)
	primary := seedHTTPWorkspace(t, ctx, database.DB(), 1, now, []string{
		"stable alpha evidence",
		"stable beta evidence",
		"stable gamma evidence",
	})
	primaryIndex := buildHTTPHybridIndex(t, ctx, repository, primary, contract, 100, now.Add(time.Minute))
	isolated := seedHTTPWorkspace(t, ctx, database.DB(), 2, now.Add(time.Hour), []string{"stable isolated evidence"})
	isolatedIndex := buildHTTPFTSIndex(t, ctx, repository, isolated, 200, now.Add(time.Hour+time.Minute))

	reader, err := retrievalworkspace.NewReader(workspaceRepository, platformfilesystem.Scanner{
		Options: platformfilesystem.ScanOptions{MaxBytes: platformfilesystem.DefaultMaxBytes},
	})
	if err != nil {
		t.Fatal(err)
	}
	evidenceService, err := retrievalapplication.NewEvidenceReferenceService(searchRepository, reader)
	if err != nil {
		t.Fatal(err)
	}
	searchService, err := retrievalapplication.NewSearchService(searchRepository, httpIntegrationEmbedder{contract: contract}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cursors, err := retrievalhttp.NewCursorCodec(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler := retrievalhttp.NewHandler(searchService, evidenceService, cursors)
	server := httptest.NewServer(app.NewRouter(app.Dependencies{
		Version: "integration", Database: database, Retrieval: handler,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(server.Close)

	filter := map[string]any{
		"source_ids":         []string{string(primary.sourceID)},
		"source_version_ids": []string{string(primary.sourceVersionID)},
		"path_prefixes":      []string{"docs"},
		"captured_at_from":   now.Add(-time.Minute).Format(time.RFC3339Nano),
		"captured_at_before": now.Add(time.Minute).Format(time.RFC3339Nano),
	}
	var expectedChunks map[string]struct{}
	for _, mode := range []string{"keyword", "semantic", "hybrid"} {
		t.Run("search_"+mode, func(t *testing.T) {
			response := postSearch(t, server.URL, map[string]any{
				"workspace_id": string(primary.workspaceID), "query": "stable", "retrieval_mode": mode,
				"filters": filter, "limit": 10,
			}, http.StatusOK)
			if response.WorkspaceID != string(primary.workspaceID) || response.IndexVersionID != string(primaryIndex.ID) ||
				response.RequestedMode != mode || response.EffectiveMode != mode || len(response.Items) != len(primary.chunkIDs) {
				t.Fatalf("unexpected %s response: %#v", mode, response)
			}
			actualChunks := responseChunkSet(t, response)
			if expectedChunks == nil {
				expectedChunks = actualChunks
			} else {
				assertSameStringSet(t, actualChunks, expectedChunks)
			}
			if mode == "keyword" && response.Items[0].Scores.Lexical == nil {
				t.Fatal("keyword response omitted lexical score")
			}
			if mode == "semantic" && response.Items[0].Scores.Vector == nil {
				t.Fatal("semantic response omitted vector distance")
			}
			if mode == "hybrid" {
				if response.Items[0].Scores.Lexical == nil || response.Items[0].Scores.Vector == nil {
					t.Fatal("hybrid response omitted one search route")
				}
				if len(response.Degradations) != 1 || response.Degradations[0].Capability != "rerank" {
					t.Fatalf("hybrid degradation = %#v", response.Degradations)
				}
			}
		})
	}
	assertSameStringSet(t, expectedChunks, idsToStringSet(primary.chunkIDs))

	filterCases := []struct {
		name   string
		filter map[string]any
	}{
		{name: "source", filter: map[string]any{"source_ids": []string{string(primary.sourceID)}}},
		{name: "source_version", filter: map[string]any{"source_version_ids": []string{string(primary.sourceVersionID)}}},
		{name: "path", filter: map[string]any{"path_prefixes": []string{"docs"}}},
		{name: "captured_from", filter: map[string]any{"captured_at_from": now.Add(-time.Minute).Format(time.RFC3339Nano)}},
		{name: "captured_before", filter: map[string]any{"captured_at_before": now.Add(time.Minute).Format(time.RFC3339Nano)}},
	}
	for _, test := range filterCases {
		t.Run("filter_equivalence_"+test.name, func(t *testing.T) {
			for _, mode := range []string{"keyword", "semantic", "hybrid"} {
				response := postSearch(t, server.URL, map[string]any{
					"workspace_id": string(primary.workspaceID), "query": "stable", "retrieval_mode": mode,
					"filters": test.filter, "limit": 10,
				}, http.StatusOK)
				assertSameStringSet(t, responseChunkSet(t, response), expectedChunks)
			}
		})
	}

	t.Run("default_hybrid_and_filter_miss", func(t *testing.T) {
		response := postSearch(t, server.URL, map[string]any{
			"workspace_id": string(primary.workspaceID), "query": "stable", "limit": 10,
		}, http.StatusOK)
		if response.RequestedMode != "hybrid" || response.EffectiveMode != "hybrid" {
			t.Fatalf("default mode response = %#v", response)
		}
		empty := postSearch(t, server.URL, map[string]any{
			"workspace_id": string(primary.workspaceID), "query": "stable", "retrieval_mode": "keyword",
			"filters": map[string]any{"path_prefixes": []string{"docs/missing"}},
		}, http.StatusOK)
		if len(empty.Items) != 0 || empty.NextCursor != "" {
			t.Fatalf("filter miss returned evidence: %#v", empty)
		}
	})

	var sourceVersionHref, sourceSpanHref string
	t.Run("evidence_hrefs_open_immutable_artifact", func(t *testing.T) {
		response := postSearch(t, server.URL, map[string]any{
			"workspace_id": string(primary.workspaceID), "query": "stable", "retrieval_mode": "keyword", "limit": 1,
		}, http.StatusOK)
		if len(response.Items) != 1 || len(response.Items[0].Provenances) != 1 {
			t.Fatalf("unexpected evidence response: %#v", response)
		}
		sourceVersionHref = response.Items[0].Provenances[0].SourceVersionHref
		sourceSpanHref = response.Items[0].Provenances[0].SourceSpanHref
		versionBody := getJSON(t, server.URL+sourceVersionHref, http.StatusOK)
		var version sourceVersionWire
		decodeJSON(t, versionBody, &version)
		if version.WorkspaceID != string(primary.workspaceID) || version.SourceID != string(primary.sourceID) ||
			version.SourceVersionID != string(primary.sourceVersionID) || version.RelativePath != primary.relativePath ||
			version.ContentHash != primary.artifactHash || version.ByteSize != int64(len(primary.artifactContent)) {
			t.Fatalf("source version = %#v", version)
		}
		assertBodyHidesInternalLocator(t, versionBody, primary)

		spanBody := getJSON(t, server.URL+sourceSpanHref, http.StatusOK)
		var span sourceSpanWire
		decodeJSON(t, spanBody, &span)
		if span.SourceVersion.SourceVersionID != string(primary.sourceVersionID) || span.SpanID != string(primary.spanID) ||
			span.ParseProjectionID != string(primary.projectionID) || span.Excerpt != string(primary.artifactContent) ||
			span.ExcerptHash != primary.artifactHash || span.ExcerptTruncated {
			t.Fatalf("source span binding mismatch: version=%s span=%s projection=%s excerpt_hash=%s excerpt_bytes=%d truncated=%v",
				span.SourceVersion.SourceVersionID, span.SpanID, span.ParseProjectionID, span.ExcerptHash, len(span.Excerpt), span.ExcerptTruncated)
		}
		assertBodyHidesInternalLocator(t, spanBody, primary)
	})

	t.Run("workspace_isolation", func(t *testing.T) {
		primaryResponse := postSearch(t, server.URL, map[string]any{
			"workspace_id": string(primary.workspaceID), "query": "stable", "retrieval_mode": "keyword", "limit": 10,
		}, http.StatusOK)
		assertSameStringSet(t, responseChunkSet(t, primaryResponse), idsToStringSet(primary.chunkIDs))
		isolatedResponse := postSearch(t, server.URL, map[string]any{
			"workspace_id": string(isolated.workspaceID), "query": "stable", "retrieval_mode": "keyword", "limit": 10,
		}, http.StatusOK)
		if isolatedResponse.IndexVersionID != string(isolatedIndex.ID) {
			t.Fatalf("isolated index = %s", isolatedResponse.IndexVersionID)
		}
		assertSameStringSet(t, responseChunkSet(t, isolatedResponse), idsToStringSet(isolated.chunkIDs))

		wrongVersionHref := strings.Replace(sourceVersionHref, string(primary.workspaceID), string(isolated.workspaceID), 1)
		assertProblemCode(t, getJSON(t, server.URL+wrongVersionHref, http.StatusNotFound), "RETRIEVAL_EVIDENCE_REFERENCE_NOT_FOUND")
		wrongSpanHref := strings.Replace(sourceSpanHref, string(primary.workspaceID), string(isolated.workspaceID), 1)
		assertProblemCode(t, getJSON(t, server.URL+wrongSpanHref, http.StatusNotFound), "RETRIEVAL_EVIDENCE_REFERENCE_NOT_FOUND")
	})

	t.Run("production_index_explain_baseline", func(t *testing.T) {
		assertHTTPExplainBaselines(t, ctx, database.DB(), primary, primaryIndex, contract)
	})

	t.Run("cursor_tamper_request_binding_and_stale_active", func(t *testing.T) {
		request := map[string]any{
			"workspace_id": string(primary.workspaceID), "query": "stable", "retrieval_mode": "hybrid", "limit": 1,
		}
		first := postSearch(t, server.URL, request, http.StatusOK)
		if len(first.Items) != 1 || first.NextCursor == "" {
			t.Fatalf("first cursor page = %#v", first)
		}
		secondRequest := cloneMap(request)
		secondRequest["cursor"] = first.NextCursor
		second := postSearch(t, server.URL, secondRequest, http.StatusOK)
		if len(second.Items) != 1 || second.Items[0].ChunkID == first.Items[0].ChunkID || second.NextCursor == "" {
			t.Fatalf("second cursor page = %#v", second)
		}
		tampered := cloneMap(request)
		tampered["cursor"] = tamperCursor(first.NextCursor)
		assertProblemCode(t, postJSONRaw(t, server.URL+"/api/v1/search", tampered, http.StatusBadRequest), retrievaldomain.ErrorCodeSearchCursorInvalid)

		crossRequest := cloneMap(request)
		crossRequest["query"] = "stable alpha"
		crossRequest["cursor"] = first.NextCursor
		assertProblemCode(t, postJSONRaw(t, server.URL+"/api/v1/search", crossRequest, http.StatusBadRequest), retrievaldomain.ErrorCodeSearchCursorInvalid)

		replacement := buildHTTPHybridIndex(t, ctx, repository, primary, contract, 101, now.Add(2*time.Hour))
		if replacement.ID == primaryIndex.ID {
			t.Fatal("replacement index did not change identity")
		}
		stale := cloneMap(request)
		stale["cursor"] = first.NextCursor
		assertProblemCode(t, postJSONRaw(t, server.URL+"/api/v1/search", stale, http.StatusConflict), retrievaldomain.ErrorCodeSearchCursorStale)
	})
}

type httpWorkspaceFixture struct {
	workspaceID     foundation.ID
	sourceID        foundation.ID
	sourceVersionID foundation.ID
	artifactID      foundation.ID
	projectionID    foundation.ID
	spanID          foundation.ID
	chunkIDs        []foundation.ID
	rootPath        string
	relativePath    string
	artifactHash    string
	artifactContent []byte
	capturedAt      time.Time
	processing      retrievaldomain.ProcessingContract
}

func seedHTTPWorkspace(
	t *testing.T,
	ctx context.Context,
	database *pgxpool.Pool,
	ordinal int,
	capturedAt time.Time,
	chunkContents []string,
) httpWorkspaceFixture {
	t.Helper()
	rootPath := t.TempDir()
	if output, err := exec.CommandContext(ctx, "git", "init", "--quiet", rootPath).CombinedOutput(); err != nil {
		t.Fatalf("initialize fixture git workspace: %v output=%s", err, output)
	}
	workspaceID := httpIntegrationID(ordinal*100 + 1)
	sourceID := httpIntegrationID(ordinal*100 + 2)
	sourceVersionID := httpIntegrationID(ordinal*100 + 3)
	artifactID := httpIntegrationID(ordinal*100 + 4)
	projectionID := httpIntegrationID(ordinal*100 + 5)
	spanID := httpIntegrationID(ordinal*100 + 6)
	attemptID := httpIntegrationID(ordinal*100 + 7)
	relativePath := fmt.Sprintf("docs/evidence-%d.md", ordinal)
	artifactContent := []byte(fmt.Sprintf("stable immutable evidence for workspace %d\n", ordinal))
	artifactHash := sha256Hex(artifactContent)
	files := platformfilesystem.Scanner{Options: platformfilesystem.ScanOptions{MaxBytes: platformfilesystem.DefaultMaxBytes}}
	capture, err := files.CaptureCommitted(ctx, rootPath, relativePath, artifactContent, artifactHash)
	if err != nil {
		t.Fatal(err)
	}
	processing := retrievaldomain.ProcessingContract{
		ParserID: "goldmark", ParserVersion: "goldmark-v1", ParserConfigHash: strings.Repeat("d", 64),
		ChunkStrategyVersion: "structure-v1", SchemaVersion: "schema-v1",
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		  VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, []any{string(workspaceID), fmt.Sprintf("http-integration-%d", ordinal), rootPath, capturedAt}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
		  VALUES($1,$2,'file',$3,$4,$5)`, []any{string(sourceID), string(workspaceID), fmt.Sprintf("Evidence %d", ordinal), relativePath, capturedAt}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
		  VALUES($1,$2,$3,$4,$5,$6)`, []any{string(artifactID), string(workspaceID), artifactHash, int64(len(artifactContent)), capture.ManagedLocation, capturedAt}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at)
		  VALUES($1,$2,$3,$4,$5,$6,'text/markdown',$7,'passed',$8)`, []any{string(sourceVersionID), string(sourceID), string(workspaceID), string(artifactID), artifactHash, int64(len(artifactContent)), relativePath, capturedAt}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at)
		  VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, []any{string(projectionID), string(workspaceID), string(artifactID), processing.ParserID, processing.ParserVersion, processing.ParserConfigHash, processing.SchemaVersion, artifactHash, capturedAt}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
		  VALUES($1,$2,$3,$4)`, []any{string(sourceVersionID), string(projectionID), string(workspaceID), capturedAt}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at)
		  VALUES($1,$2,$3,$4,'document',1,1,0,$5,'{"kind":"document"}',$6,$7,$8,$9)`, []any{string(spanID), string(workspaceID), string(artifactID), string(projectionID), int64(len(artifactContent)), artifactHash, processing.ParserVersion, processing.SchemaVersion, capturedAt}},
		{`INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,parse_projection_id,status,security_status,failure_stage,error_code,retryable,
		  parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at,version)
		  VALUES($1,$2,$3,$4,'chunked','passed','','',false,$5,$6,$7,$8,$9,$10,1,$11,$11,1)`, []any{string(attemptID), string(workspaceID), string(sourceVersionID), string(projectionID), processing.ParserID, processing.ParserVersion, processing.ParserConfigHash, processing.ChunkStrategyVersion, processing.SchemaVersion, fmt.Sprintf("http-attempt-%d", ordinal), capturedAt}},
	}
	for _, statement := range statements {
		if _, err := database.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	chunkIDs := make([]foundation.ID, len(chunkContents))
	for index, content := range chunkContents {
		chunkIDs[index] = httpIntegrationID(ordinal*1000 + index + 1)
		sequence := index * 2
		if _, err := database.Exec(ctx, `INSERT INTO ingestion.canonical_chunk(
			id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,
			parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,false,'active',$14)`,
			string(chunkIDs[index]), string(workspaceID), string(projectionID), sequence,
			fmt.Sprintf(`["Evidence %d"]`, index+1), content, sha256Hex([]byte(content)), string(spanID),
			int64(len(content)), int64(len([]rune(content))), processing.ParserVersion, processing.ChunkStrategyVersion,
			processing.SchemaVersion, capturedAt); err != nil {
			t.Fatal(err)
		}
	}
	return httpWorkspaceFixture{
		workspaceID: workspaceID, sourceID: sourceID, sourceVersionID: sourceVersionID,
		artifactID: artifactID, projectionID: projectionID, spanID: spanID, chunkIDs: chunkIDs,
		rootPath: rootPath, relativePath: relativePath, artifactHash: artifactHash,
		artifactContent: append([]byte(nil), artifactContent...), capturedAt: capturedAt, processing: processing,
	}
}

func buildHTTPHybridIndex(
	t *testing.T,
	ctx context.Context,
	repository *retrievalpostgres.Repository,
	fixture httpWorkspaceFixture,
	contract retrievaldomain.EmbeddingContract,
	ordinal int,
	at time.Time,
) retrievaldomain.IndexVersion {
	t.Helper()
	embedding := retrievaldomain.EmbeddingVersion{
		ID: httpIntegrationID(90), Provider: contract.Provider, AdapterName: contract.AdapterName,
		AdapterVersion: contract.AdapterVersion, Model: contract.Model, Dimensions: contract.Dimensions,
		Normalization: contract.Normalization, DistanceMetric: contract.DistanceMetric,
		ConfigHash: contract.ConfigHash, CreatedAt: at.Add(-time.Hour),
	}
	if _, err := repository.RegisterEmbeddingVersion(ctx, embedding); err != nil {
		t.Fatal(err)
	}
	command := httpSnapshotCommand(t, fixture, ordinal, at)
	command.IndexVersion.EmbeddingVersionID = &embedding.ID
	command.IndexVersion.DegradedCapabilities = nil
	command.IndexVersion.FusionConfig = httpRRFConfig(t)
	created, err := repository.BeginWorkspaceSnapshot(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BuildLexical(ctx, retrievaldomain.LexicalBuildCommand{
		WorkspaceID: fixture.workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedIndexVersion: 1, At: at.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	page, err := repository.LoadVectorBuildPage(ctx, retrievaldomain.VectorBuildPageCommand{
		WorkspaceID: fixture.workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedIndexVersion: 1,
		Limit: 100, MaxInputBytes: 1024 * 1024, MaxBatchInputBytes: 8 * 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	writes := make([]retrievaldomain.VectorBuildWrite, len(page.Inputs))
	for index, input := range page.Inputs {
		vector := []float32{1, 0, 0}
		if index%3 == 1 {
			vector = []float32{0, 1, 0}
		} else if index%3 == 2 {
			vector = []float32{0, 0, 1}
		}
		writes[index] = retrievaldomain.VectorBuildWrite{
			ChunkID: input.ChunkID, ContentHash: input.ContentHash, TokenCount: input.TokenCount,
			VectorStatus: retrievaldomain.VectorStatusReady, Embedding: vector,
		}
	}
	if _, err := repository.CommitVectorBuildBatch(ctx, retrievaldomain.VectorBuildBatch{
		WorkspaceID: fixture.workspaceID, IndexVersionID: created.IndexVersion.ID, EmbeddingVersionID: embedding.ID,
		ExpectedIndexVersion: 1, Writes: writes, At: at.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	return readyAndActivateHTTPIndex(t, ctx, repository, fixture.workspaceID, created.IndexVersion.ID, ordinal, at.Add(3*time.Minute))
}

func buildHTTPFTSIndex(
	t *testing.T,
	ctx context.Context,
	repository *retrievalpostgres.Repository,
	fixture httpWorkspaceFixture,
	ordinal int,
	at time.Time,
) retrievaldomain.IndexVersion {
	t.Helper()
	command := httpSnapshotCommand(t, fixture, ordinal, at)
	created, err := repository.BeginWorkspaceSnapshot(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BuildLexical(ctx, retrievaldomain.LexicalBuildCommand{
		WorkspaceID: fixture.workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedIndexVersion: 1, At: at.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	return readyAndActivateHTTPIndex(t, ctx, repository, fixture.workspaceID, created.IndexVersion.ID, ordinal, at.Add(2*time.Minute))
}

func readyAndActivateHTTPIndex(
	t *testing.T,
	ctx context.Context,
	repository *retrievalpostgres.Repository,
	workspaceID, indexID foundation.ID,
	ordinal int,
	at time.Time,
) retrievaldomain.IndexVersion {
	t.Helper()
	ready, err := repository.TransitionIndex(ctx, retrievaldomain.IndexTransition{
		WorkspaceID: workspaceID, IndexVersionID: indexID, ExpectedVersion: 1,
		Status: retrievaldomain.IndexStatusReady, At: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := retrievaldomain.ActivationCommand{
		ActivationID: httpIntegrationID(5000 + ordinal), WorkspaceID: workspaceID,
		TargetIndexVersionID: ready.ID, ExpectedTargetVersion: ready.Version,
		IdempotencyKey: fmt.Sprintf("http-activate-%d", ordinal), ReasonCode: "BUILD_VERIFIED", At: at.Add(time.Minute),
	}
	current, currentErr := repository.GetActive(ctx, workspaceID)
	if currentErr == nil {
		command.ExpectedCurrentIndexVersionID = &current.ID
		command.ExpectedCurrentVersion = &current.Version
	} else if !isFoundationKind(currentErr, foundation.ErrorNotFound) {
		t.Fatal(currentErr)
	}
	activated, err := repository.Activate(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	return activated.ActiveIndexVersion
}

func httpSnapshotCommand(t *testing.T, fixture httpWorkspaceFixture, ordinal int, at time.Time) retrievaldomain.WorkspaceSnapshotCommand {
	t.Helper()
	return retrievaldomain.WorkspaceSnapshotCommand{
		IndexVersion: retrievaldomain.IndexVersion{
			ID: httpIntegrationID(10000 + ordinal), WorkspaceID: fixture.workspaceID,
			TokenizerID: "postgres-simple", TokenizerVersion: "v1", TokenizerConfigHash: strings.Repeat("1", 64),
			FusionConfig: []byte(`{"method":"rrf","k":60}`), SourceSnapshotRef: "reindex-v1:" + string(httpIntegrationID(20000+ordinal)),
			IdempotencyKey: fmt.Sprintf("http-index-%d", ordinal), Status: retrievaldomain.IndexStatusBuilding,
			DegradedCapabilities: []retrievaldomain.DegradedCapability{retrievaldomain.DegradedVector},
			Version:              1, CreatedAt: at, UpdatedAt: at, ProcessingContract: &fixture.processing,
		},
		TargetSourceID: fixture.sourceID, TargetSourceVersionID: fixture.sourceVersionID,
		TargetParseProjectionID: fixture.projectionID, PageSize: 100,
		MaxSources: retrievaldomain.DefaultSnapshotMaxSources, MaxChunks: retrievaldomain.DefaultSnapshotMaxChunks,
	}
}

func httpRRFConfig(t *testing.T) []byte {
	t.Helper()
	value, err := retrievaldomain.CanonicalRRFConfig(retrievaldomain.RRFConfig{
		SchemaVersion: retrievaldomain.RRFFusionSchemaVersionV1, Method: retrievaldomain.FusionMethodRRF, K: 60,
		LexicalCandidateLimit: 20, VectorCandidateLimit: 20, FusedCandidateLimit: 20, RerankCandidateLimit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type httpIntegrationEmbedder struct {
	contract retrievaldomain.EmbeddingContract
}

func (e httpIntegrationEmbedder) Contract() retrievaldomain.EmbeddingContract { return e.contract }

func (e httpIntegrationEmbedder) Embed(_ context.Context, request retrievalapplication.EmbedRequest) (retrievalapplication.EmbedResult, error) {
	vectors := make([][]float32, len(request.Inputs))
	for index := range vectors {
		vectors[index] = []float32{1, 0, 0}
	}
	return retrievalapplication.EmbedResult{Model: e.contract.Model, Embeddings: vectors}, nil
}

func httpIntegrationEmbeddingContract(t *testing.T) retrievaldomain.EmbeddingContract {
	t.Helper()
	contract := retrievaldomain.EmbeddingContract{
		Provider: "test", AdapterName: "fixture", AdapterVersion: "v1", Model: "http-search-v1",
		Dimensions: 3, Normalization: retrievaldomain.NormalizationL2, DistanceMetric: retrievaldomain.DistanceCosine,
		EndpointIdentity: "test://retrieval-http", MaxBatchSize: 8, MaxInputBytes: 8192, MaxBatchInputBytes: 65536,
	}
	hash, err := retrievaldomain.ComputeEmbeddingConfigHash(contract)
	if err != nil {
		t.Fatal(err)
	}
	contract.ConfigHash = hash
	if err := retrievaldomain.ValidateEmbeddingContract(contract); err != nil {
		t.Fatal(err)
	}
	return contract
}

func assertHTTPExplainBaselines(
	t *testing.T,
	ctx context.Context,
	database *pgxpool.Pool,
	fixture httpWorkspaceFixture,
	index retrievaldomain.IndexVersion,
	contract retrievaldomain.EmbeddingContract,
) {
	t.Helper()
	baselines := []struct {
		name      string
		indexName string
		query     string
		args      []any
	}{
		{name: "active", indexName: "uq_retrieval_index_version_active", query: `SELECT id FROM retrieval.index_version WHERE workspace_id=$1 AND status='active'`, args: []any{string(fixture.workspaceID)}},
		{name: "manifest", indexName: "idx_retrieval_source_manifest_workspace_index_source", query: `SELECT source_id FROM retrieval.index_manifest_source WHERE workspace_id=$1 AND index_version_id=$2 ORDER BY source_id`, args: []any{string(fixture.workspaceID), string(index.ID)}},
		{name: "fts", indexName: "idx_retrieval_projection_search_vector", query: `SELECT index_version_id FROM retrieval.chunk_projection WHERE search_vector @@ websearch_to_tsquery('simple',$1)`, args: []any{"stable"}},
		{name: "trigram", indexName: "idx_ingestion_canonical_chunk_content_trgm", query: `SELECT id FROM ingestion.canonical_chunk WHERE content % $1`, args: []any{"stable"}},
	}
	for _, baseline := range baselines {
		t.Run("explain_"+baseline.name, func(t *testing.T) {
			plan := explainHTTPQueryJSON(t, ctx, database, baseline.query, baseline.args...)
			if !strings.Contains(plan, baseline.indexName) {
				t.Fatalf("EXPLAIN did not expose %s:\n%s", baseline.indexName, plan)
			}
		})
	}
	for _, operator := range []string{"<=>", "<#>", "<->"} {
		t.Run("explain_vector_"+operator, func(t *testing.T) {
			query := fmt.Sprintf(`SELECT chunk_id,(embedding %s $3::vector) AS distance
				FROM retrieval.chunk_projection
				WHERE workspace_id=$1 AND index_version_id=$2 AND embedding_version_id IS NOT NULL AND vector_status='ready'
				ORDER BY distance,chunk_id LIMIT 10`, operator)
			plan := explainHTTPQueryJSON(t, ctx, database, query, string(fixture.workspaceID), string(index.ID), pgvector.NewVector([]float32{1, 0, 0}))
			lower := strings.ToLower(plan)
			if !strings.Contains(plan, operator) || strings.Contains(lower, "hnsw") || strings.Contains(lower, "ivfflat") {
				t.Fatalf("unexpected exact-vector EXPLAIN for %s (contract=%s):\n%s", operator, contract.DistanceMetric, plan)
			}
		})
	}
}

func explainHTTPQueryJSON(t *testing.T, ctx context.Context, database *pgxpool.Pool, query string, args ...any) string {
	t.Helper()
	tx, err := database.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	var plan []byte
	if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON, COSTS OFF, VERBOSE) "+query, args...).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(plan) {
		t.Fatalf("EXPLAIN did not return JSON: %q", plan)
	}
	return string(plan)
}

type searchResponseWire struct {
	WorkspaceID    string                  `json:"workspace_id"`
	IndexVersionID string                  `json:"index_version_id"`
	RequestedMode  string                  `json:"requested_mode"`
	EffectiveMode  string                  `json:"effective_mode"`
	Degradations   []searchDegradationWire `json:"degradations"`
	Items          []evidenceWire          `json:"items"`
	NextCursor     string                  `json:"next_cursor"`
}

type searchDegradationWire struct {
	Capability string `json:"capability"`
	ErrorCode  string `json:"error_code"`
}

type evidenceWire struct {
	ChunkID     string                   `json:"chunk_id"`
	Provenances []evidenceProvenanceWire `json:"provenances"`
	Scores      evidenceScoresWire       `json:"scores"`
}

type evidenceProvenanceWire struct {
	SourceVersionHref string `json:"source_version_href"`
	SourceSpanHref    string `json:"source_span_href"`
}

type evidenceScoresWire struct {
	Lexical *json.RawMessage `json:"lexical"`
	Vector  *json.RawMessage `json:"vector"`
}

type sourceVersionWire struct {
	WorkspaceID     string `json:"workspace_id"`
	SourceID        string `json:"source_id"`
	SourceVersionID string `json:"source_version_id"`
	RelativePath    string `json:"relative_path"`
	ContentHash     string `json:"content_hash"`
	ByteSize        int64  `json:"byte_size"`
}

type sourceSpanWire struct {
	SourceVersion     sourceVersionWire `json:"source_version"`
	ParseProjectionID string            `json:"parse_projection_id"`
	SpanID            string            `json:"span_id"`
	ExcerptHash       string            `json:"excerpt_hash"`
	Excerpt           string            `json:"excerpt"`
	ExcerptTruncated  bool              `json:"excerpt_truncated"`
}

type problemWire struct {
	ErrorCode string `json:"error_code"`
}

func postSearch(t *testing.T, baseURL string, request map[string]any, wantStatus int) searchResponseWire {
	t.Helper()
	body := postJSONRaw(t, baseURL+"/api/v1/search", request, wantStatus)
	var response searchResponseWire
	decodeJSON(t, body, &response)
	return response
}

func postJSONRaw(t *testing.T, endpoint string, request map[string]any, wantStatus int) []byte {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(endpoint, "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("POST status=%d want=%d error_code=%s response_bytes=%d", response.StatusCode, wantStatus, responseProblemCode(body), len(body))
	}
	return body
}

func getJSON(t *testing.T, endpoint string, wantStatus int) []byte {
	t.Helper()
	response, err := http.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("GET status=%d want=%d error_code=%s response_bytes=%d", response.StatusCode, wantStatus, responseProblemCode(body), len(body))
	}
	return body
}

func decodeJSON(t *testing.T, body []byte, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode response: %v response_bytes=%d", err, len(body))
	}
}

func responseChunkSet(t *testing.T, response searchResponseWire) map[string]struct{} {
	t.Helper()
	result := make(map[string]struct{}, len(response.Items))
	for _, item := range response.Items {
		if item.ChunkID == "" {
			t.Fatal("response item omitted chunk id")
		}
		if _, duplicate := result[item.ChunkID]; duplicate {
			t.Fatalf("duplicate chunk %s", item.ChunkID)
		}
		result[item.ChunkID] = struct{}{}
	}
	return result
}

func idsToStringSet(values []foundation.ID) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[string(value)] = struct{}{}
	}
	return result
}

func assertSameStringSet(t *testing.T, actual, expected map[string]struct{}) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("set sizes actual=%d expected=%d actual=%v expected=%v", len(actual), len(expected), actual, expected)
	}
	for value := range expected {
		if _, ok := actual[value]; !ok {
			t.Fatalf("set is missing %s: actual=%v expected=%v", value, actual, expected)
		}
	}
}

func assertProblemCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var problem problemWire
	decodeJSON(t, body, &problem)
	if problem.ErrorCode != want {
		t.Fatalf("problem code=%q want=%q response_bytes=%d", problem.ErrorCode, want, len(body))
	}
}

func assertBodyHidesInternalLocator(t *testing.T, body []byte, fixture httpWorkspaceFixture) {
	t.Helper()
	for _, secret := range []string{fixture.rootPath, ".knowledge/sources/" + fixture.artifactHash} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatal("response exposed an internal locator")
		}
	}
}

func responseProblemCode(body []byte) string {
	var problem problemWire
	if json.Unmarshal(body, &problem) != nil || problem.ErrorCode == "" {
		return "INVALID_RESPONSE"
	}
	return problem.ErrorCode
}

func cloneMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value)+1)
	for key, item := range value {
		result[key] = item
	}
	return result
}

func tamperCursor(value string) string {
	last := value[len(value)-1]
	replacement := byte('A')
	if last == replacement {
		replacement = 'B'
	}
	return value[:len(value)-1] + string(replacement)
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func httpIntegrationID(ordinal int) foundation.ID {
	return foundation.ID(fmt.Sprintf("a7000000-0000-4000-8000-%012x", ordinal))
}

func isFoundationKind(err error, kind foundation.ErrorKind) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == kind
}

func newHTTPIntegrationDatabase(t *testing.T) (*platformpostgres.Pool, context.Context) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for Retrieval HTTP integration tests")
	}
	ctx := context.Background()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_retrieval_http_%d_%d", os.Getpid(), time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	databaseURL := parsed.String()
	migrationPool, err := platformpostgres.OpenMigration(ctx, databaseURL, 4, 0)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	runner, err := platformmigration.NewRunner(migrationPool.DB(), projectmigrations.FS)
	if err == nil {
		err = runner.Up(ctx)
	}
	migrationPool.Close()
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	database, err := platformpostgres.Open(ctx, databaseURL, 8, 0)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	if err := database.Ping(ctx); err != nil {
		database.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	return database, ctx
}
