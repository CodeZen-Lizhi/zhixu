//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

func TestSearchRepositoryActiveFiltersAndBoundedProvenance(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	searchRepository, err := NewSearchRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 80, now)
	target := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 800, 1, true, now, now, 2)
	setSearchSourcePath(t, ctx, database.DB(), target.SourceID, "docs/api/a.md")

	sharedSources := make([]snapshotSourceFixture, 0, 9)
	for ordinal := 810; ordinal < 819; ordinal++ {
		shared := seedSnapshotSourceSharingProjection(t, ctx, database.DB(), workspaceID, ordinal, target, now)
		path := fmt.Sprintf("docs/api/shared-%d.md", ordinal)
		if ordinal == 818 {
			path = "docs/apis/adjacent.md"
		}
		setSearchSourcePath(t, ctx, database.DB(), shared.SourceID, path)
		sharedSources = append(sharedSources, shared)
	}
	other := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 900, 1, true, now.Add(2*time.Hour), now.Add(2*time.Hour), 1)
	setSearchSourcePath(t, ctx, database.DB(), other.SourceID, "docs/guide/b.md")
	excluded := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 901, 1, false, now, now, 1)
	setSearchSourcePath(t, ctx, database.DB(), excluded.SourceID, "docs/excluded.md")

	embedding := searchEmbedding(snapshotID(8000), domain.DistanceCosine, now)
	active := createHybridSearchIndex(t, ctx, repository, workspaceID, target, embedding, 8001, 8002, now.Add(3*time.Hour))
	loaded, err := searchRepository.LoadActiveSearchIndex(ctx, workspaceID)
	if err != nil || loaded.Index.ID != active.ID || loaded.EmbeddingVersion == nil || loaded.Fusion == nil ||
		loaded.EmbeddingVersion.ID != embedding.ID || loaded.Fusion.VectorCandidateLimit != 20 {
		t.Fatalf("LoadActiveSearchIndex()=%#v err=%v", loaded, err)
	}

	lexicalQuery := application.LexicalSearchQuery{
		WorkspaceID: workspaceID, IndexVersionID: active.ID, Query: "chunk", Limit: 20,
	}
	vectorQuery := application.VectorSearchQuery{
		WorkspaceID: workspaceID, IndexVersionID: active.ID, EmbeddingVersion: embedding,
		QueryEmbedding: []float32{1, 0, 0}, Limit: 20,
	}
	lexical, err := searchRepository.SearchLexical(ctx, lexicalQuery)
	if err != nil {
		t.Fatal(err)
	}
	vector, err := searchRepository.SearchVector(ctx, vectorQuery)
	if err != nil {
		t.Fatalf("%v cause=%v", err, errors.Unwrap(err))
	}
	if len(lexical) != 3 || len(vector) != 3 {
		t.Fatalf("all candidates lexical=%d vector=%d", len(lexical), len(vector))
	}
	lexicalArguments := []any{
		string(workspaceID), string(active.ID), strings.TrimSpace(lexicalQuery.Query), lexicalQuery.Limit,
		domain.MaxEvidenceProvenance,
	}
	lexicalPlan := explainSearchQuery(t, ctx, database.DB(), fmt.Sprintf(
		lexicalCandidateSQL, appendSearchFilter(&lexicalArguments, lexicalQuery.Filter), searchSnippetCharacterLimit, searchRerankCharacterLimit,
	), lexicalArguments...)
	for _, marker := range []string{
		"uq_retrieval_index_version_active",
		"idx_retrieval_projection_workspace_index_chunk",
		"idx_retrieval_source_manifest_workspace_index_source",
		"lexical_match_chunks",
		"ranked_provenance",
	} {
		if !strings.Contains(lexicalPlan, marker) {
			t.Fatalf("production lexical EXPLAIN did not expose %s", marker)
		}
	}
	replayedLexical, err := searchRepository.SearchLexical(ctx, lexicalQuery)
	if err != nil || len(replayedLexical) != len(lexical) {
		t.Fatalf("replayed lexical=%d err=%v", len(replayedLexical), err)
	}
	for index := range lexical {
		if lexical[index].ChunkID != replayedLexical[index].ChunkID || lexical[index].Lexical.Rank != replayedLexical[index].Lexical.Rank {
			t.Fatalf("unstable lexical replay at %d", index)
		}
	}
	connection, err := database.DB().Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	connectionRepository, err := NewSearchRepository(connection)
	if err != nil {
		t.Fatal(err)
	}
	trigramOnlyQuery := lexicalQuery
	trigramOnlyQuery.Query = "chunkk"
	var thresholdResults [][]domain.SearchCandidate
	for _, threshold := range []string{"0.99", "0.01"} {
		if _, err := connection.Exec(ctx, `SELECT set_config('pg_trgm.similarity_threshold',$1,false)`, threshold); err != nil {
			t.Fatal(err)
		}
		candidates, err := connectionRepository.SearchLexical(ctx, trigramOnlyQuery)
		if err != nil {
			t.Fatal(err)
		}
		thresholdResults = append(thresholdResults, candidates)
	}
	if len(thresholdResults[0]) == 0 || len(thresholdResults[0]) != len(thresholdResults[1]) {
		t.Fatalf("session trigram threshold changed candidate count: low=%d high=%d", len(thresholdResults[1]), len(thresholdResults[0]))
	}
	for index := range thresholdResults[0] {
		if thresholdResults[0][index].ChunkID != thresholdResults[1][index].ChunkID {
			t.Fatalf("session trigram threshold changed candidate order at %d", index)
		}
	}
	assertStableSearchRanks(t, lexical)
	assertStableSearchRanks(t, vector)
	assertSameSearchChunkSet(t, lexical, vector)
	var sharedCandidate *domain.SearchCandidate
	for index := range lexical {
		if lexical[index].ParseProjectionID == target.ProjectionID {
			sharedCandidate = &lexical[index]
			break
		}
	}
	if sharedCandidate == nil || len(sharedCandidate.Provenances) != domain.MaxEvidenceProvenance || !sharedCandidate.ProvenanceTruncated {
		t.Fatalf("bounded shared candidate=%#v", sharedCandidate)
	}
	for index := 1; index < len(sharedCandidate.Provenances); index++ {
		left, right := sharedCandidate.Provenances[index-1], sharedCandidate.Provenances[index]
		if left.SourceID > right.SourceID || left.SourceID == right.SourceID && left.SourceVersionID > right.SourceVersionID {
			t.Fatalf("provenance order=%#v", sharedCandidate.Provenances)
		}
	}

	filter := domain.SearchFilter{SourceIDs: []foundation.ID{sharedSources[0].SourceID}}
	lexicalQuery.Filter, vectorQuery.Filter = filter, filter
	filteredLexical, err := searchRepository.SearchLexical(ctx, lexicalQuery)
	if err != nil {
		t.Fatal(err)
	}
	filteredVector, err := searchRepository.SearchVector(ctx, vectorQuery)
	if err != nil {
		t.Fatal(err)
	}
	assertSameSearchChunkSet(t, filteredLexical, filteredVector)
	if len(filteredLexical) != 2 {
		t.Fatalf("source-filtered candidates=%d", len(filteredLexical))
	}
	for _, candidate := range filteredLexical {
		if len(candidate.Provenances) != 1 || candidate.Provenances[0].SourceID != sharedSources[0].SourceID || candidate.ProvenanceTruncated {
			t.Fatalf("source-filtered provenance=%#v", candidate)
		}
	}

	filter = domain.SearchFilter{SourceVersionIDs: []foundation.ID{sharedSources[1].VersionID}}
	lexicalQuery.Filter, vectorQuery.Filter = filter, filter
	versionLexical, err := searchRepository.SearchLexical(ctx, lexicalQuery)
	if err != nil {
		t.Fatal(err)
	}
	versionVector, err := searchRepository.SearchVector(ctx, vectorQuery)
	if err != nil {
		t.Fatal(err)
	}
	assertSameSearchChunkSet(t, versionLexical, versionVector)
	if len(versionLexical) != 2 {
		t.Fatalf("source-version-filtered candidates=%d", len(versionLexical))
	}
	for _, candidate := range versionLexical {
		if len(candidate.Provenances) != 1 || candidate.Provenances[0].SourceVersionID != sharedSources[1].VersionID {
			t.Fatalf("source-version-filtered provenance=%#v", candidate.Provenances)
		}
	}

	filter = domain.SearchFilter{PathPrefixes: []string{"docs/api"}}
	lexicalQuery.Filter, vectorQuery.Filter = filter, filter
	pathLexical, err := searchRepository.SearchLexical(ctx, lexicalQuery)
	if err != nil {
		t.Fatal(err)
	}
	pathVector, err := searchRepository.SearchVector(ctx, vectorQuery)
	if err != nil {
		t.Fatal(err)
	}
	assertSameSearchChunkSet(t, pathLexical, pathVector)
	if len(pathLexical) != 2 {
		t.Fatalf("path-filtered candidates=%d", len(pathLexical))
	}
	for _, candidate := range pathLexical {
		for _, provenance := range candidate.Provenances {
			if strings.HasPrefix(provenance.RelativePath, "docs/apis/") {
				t.Fatalf("adjacent path leaked into segment prefix: %#v", provenance)
			}
		}
	}
	lexicalQuery.Filter = domain.SearchFilter{PathPrefixes: []string{"docs/api/a"}}
	if candidates, queryErr := searchRepository.SearchLexical(ctx, lexicalQuery); queryErr != nil || len(candidates) != 0 {
		t.Fatalf("partial segment candidates=%d err=%v", len(candidates), queryErr)
	}

	before := now.Add(time.Hour)
	filter = domain.SearchFilter{CapturedAtBefore: &before}
	lexicalQuery.Filter, vectorQuery.Filter = filter, filter
	timeLexical, err := searchRepository.SearchLexical(ctx, lexicalQuery)
	if err != nil {
		t.Fatal(err)
	}
	timeVector, err := searchRepository.SearchVector(ctx, vectorQuery)
	if err != nil {
		t.Fatal(err)
	}
	assertSameSearchChunkSet(t, timeLexical, timeVector)
	if len(timeLexical) != 2 {
		t.Fatalf("time-filtered candidates=%d", len(timeLexical))
	}
	from := now.Add(2 * time.Hour)
	filter = domain.SearchFilter{CapturedAtFrom: &from}
	lexicalQuery.Filter, vectorQuery.Filter = filter, filter
	fromLexical, err := searchRepository.SearchLexical(ctx, lexicalQuery)
	if err != nil {
		t.Fatal(err)
	}
	fromVector, err := searchRepository.SearchVector(ctx, vectorQuery)
	if err != nil {
		t.Fatal(err)
	}
	assertSameSearchChunkSet(t, fromLexical, fromVector)
	if len(fromLexical) != 1 || fromLexical[0].ParseProjectionID != other.ProjectionID {
		t.Fatalf("inclusive-from candidates=%#v", fromLexical)
	}

	buildingCommand := snapshotCommand(workspaceID, 8010, other, "search-building", now.Add(4*time.Hour))
	buildingCommand.IndexVersion.EmbeddingVersionID = &embedding.ID
	buildingCommand.IndexVersion.DegradedCapabilities = nil
	buildingCommand.IndexVersion.FusionConfig = searchRRF(t)
	building, err := repository.BeginWorkspaceSnapshot(ctx, buildingCommand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BuildLexical(ctx, domain.LexicalBuildCommand{
		WorkspaceID: workspaceID, IndexVersionID: building.IndexVersion.ID,
		ExpectedIndexVersion: 1, At: now.Add(5 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	lexicalQuery.IndexVersionID = building.IndexVersion.ID
	lexicalQuery.Filter = domain.SearchFilter{}
	if candidates, queryErr := searchRepository.SearchLexical(ctx, lexicalQuery); queryErr != nil || len(candidates) != 0 {
		t.Fatalf("building lexical candidates=%d err=%v", len(candidates), queryErr)
	}
	vectorQuery.IndexVersionID = building.IndexVersion.ID
	vectorQuery.Filter = domain.SearchFilter{}
	if candidates, queryErr := searchRepository.SearchVector(ctx, vectorQuery); queryErr != nil || len(candidates) != 0 {
		t.Fatalf("building vector candidates=%d err=%v", len(candidates), queryErr)
	}
	loaded, err = searchRepository.LoadActiveSearchIndex(ctx, workspaceID)
	if err != nil || loaded.Index.ID != active.ID {
		t.Fatalf("active after building=%#v err=%v", loaded, err)
	}

	assertExplainUsesIndex(t, ctx, database.DB(), "idx_retrieval_projection_search_vector",
		`SELECT index_version_id FROM retrieval.chunk_projection
		 WHERE search_vector @@ websearch_to_tsquery('simple',$1)`, "chunk")
	assertExplainUsesIndex(t, ctx, database.DB(), "idx_ingestion_canonical_chunk_content_trgm",
		`SELECT id FROM ingestion.canonical_chunk WHERE content % $1`, "chunk")
}

func TestSearchRepositoryLoadsLegacyFTSOnlyWithoutInventingFusion(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	searchRepository, err := NewSearchRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 95, now)
	target := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 950, 1, true, now, now, 1)
	active := mustCreateAndActivateSnapshot(t, ctx, repository,
		snapshotCommand(workspaceID, 9500, target, "search-fts-only", now.Add(time.Hour)), 9501, now.Add(2*time.Hour))
	loaded, err := searchRepository.LoadActiveSearchIndex(ctx, workspaceID)
	if err != nil || loaded.Index.ID != active.ID || loaded.EmbeddingVersion != nil || loaded.Fusion != nil {
		t.Fatalf("legacy active=%#v err=%v", loaded, err)
	}
}

func TestSearchRepositoryDistanceOperatorsAndExactExplain(t *testing.T) {
	tests := []struct {
		name     string
		metric   domain.DistanceMetric
		operator string
		want     float64
	}{
		{name: "cosine", metric: domain.DistanceCosine, operator: "<=>", want: 1},
		{name: "inner product", metric: domain.DistanceInnerProduct, operator: "<#>", want: 0},
		{name: "euclidean", metric: domain.DistanceEuclidean, operator: "<->", want: math.Sqrt2},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, database, ctx := newRetrievalTestRepository(t)
			searchRepository, err := NewSearchRepository(database.DB())
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 7, 19, 12+index, 0, 0, 0, time.UTC)
			workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 100+index, now)
			target := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 1000+index, 1, true, now, now, 1)
			embedding := searchEmbedding(snapshotID(10_000+index), test.metric, now)
			active := createHybridSearchIndex(t, ctx, repository, workspaceID, target, embedding, 10_100+index, 10_200+index, now.Add(time.Hour))
			candidates, err := searchRepository.SearchVector(ctx, application.VectorSearchQuery{
				WorkspaceID: workspaceID, IndexVersionID: active.ID, EmbeddingVersion: embedding,
				QueryEmbedding: []float32{0, 1, 0}, Limit: 10,
			})
			if err != nil || len(candidates) != 1 || candidates[0].Vector == nil {
				t.Fatalf("SearchVector()=%#v err=%v cause=%v", candidates, err, errors.Unwrap(err))
			}
			if difference := math.Abs(candidates[0].Vector.Score - test.want); difference > 1e-6 {
				t.Fatalf("distance=%v want=%v", candidates[0].Vector.Score, test.want)
			}
			mismatchedEmbedding := embedding
			mismatchedEmbedding.DistanceMetric = domain.DistanceCosine
			if test.metric == domain.DistanceCosine {
				mismatchedEmbedding.DistanceMetric = domain.DistanceInnerProduct
			}
			_, mismatchErr := searchRepository.SearchVector(ctx, application.VectorSearchQuery{
				WorkspaceID: workspaceID, IndexVersionID: active.ID, EmbeddingVersion: mismatchedEmbedding,
				QueryEmbedding: []float32{0, 1, 0}, Limit: 10,
			})
			if !retrievalErrorKind(mismatchErr, foundation.ErrorConsistencyViolation) {
				t.Fatalf("persisted distance mismatch error=%#v", mismatchErr)
			}

			expression, ok := vectorDistanceExpression(test.metric)
			if !ok {
				t.Fatal("distance expression missing")
			}
			arguments := []any{
				string(workspaceID), string(active.ID), string(embedding.ID),
				pgvector.NewVector([]float32{0, 1, 0}), int32(10), domain.MaxEvidenceProvenance,
			}
			plan := explainSearchQuery(t, ctx, database.DB(), fmt.Sprintf(
				vectorCandidateSQL, "", searchSnippetCharacterLimit, searchRerankCharacterLimit, expression,
			), arguments...)
			if !strings.Contains(plan, test.operator) {
				t.Fatalf("EXPLAIN missing distance operator %s:\n%s", test.operator, plan)
			}
			if strings.Contains(strings.ToLower(plan), "hnsw") || strings.Contains(strings.ToLower(plan), "ivfflat") {
				t.Fatalf("exact baseline unexpectedly used ANN:\n%s", plan)
			}
		})
	}
}

func createHybridSearchIndex(
	t *testing.T,
	ctx context.Context,
	repository *Repository,
	workspaceID foundation.ID,
	target snapshotSourceFixture,
	embedding domain.EmbeddingVersion,
	indexOrdinal, activationOrdinal int,
	at time.Time,
) domain.IndexVersion {
	t.Helper()
	if _, err := repository.RegisterEmbeddingVersion(ctx, embedding); err != nil {
		t.Fatal(err)
	}
	command := snapshotCommand(workspaceID, indexOrdinal, target, fmt.Sprintf("search-index-%d", indexOrdinal), at)
	command.IndexVersion.EmbeddingVersionID = &embedding.ID
	command.IndexVersion.DegradedCapabilities = nil
	command.IndexVersion.FusionConfig = searchRRF(t)
	created, err := repository.BeginWorkspaceSnapshot(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BuildLexical(ctx, domain.LexicalBuildCommand{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID,
		ExpectedIndexVersion: 1, At: at.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	page, err := repository.LoadVectorBuildPage(ctx, domain.VectorBuildPageCommand{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID,
		ExpectedIndexVersion: 1, Limit: 500, MaxInputBytes: 1024 * 1024, MaxBatchInputBytes: 8 * 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	writes := make([]domain.VectorBuildWrite, len(page.Inputs))
	for index, input := range page.Inputs {
		vector := []float32{1, 0, 0}
		if strings.Contains(input.Content, "900/") {
			vector = []float32{0, 1, 0}
		}
		writes[index] = domain.VectorBuildWrite{
			ChunkID: input.ChunkID, ContentHash: input.ContentHash, TokenCount: input.TokenCount,
			VectorStatus: domain.VectorStatusReady, Embedding: vector,
		}
	}
	if _, err := repository.CommitVectorBuildBatch(ctx, domain.VectorBuildBatch{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, EmbeddingVersionID: embedding.ID,
		ExpectedIndexVersion: 1, Writes: writes, At: at.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	ready, err := repository.TransitionIndex(ctx, domain.IndexTransition{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedVersion: 1,
		Status: domain.IndexStatusReady, At: at.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	activation := domain.ActivationCommand{
		ActivationID: snapshotID(activationOrdinal), WorkspaceID: workspaceID, TargetIndexVersionID: ready.ID,
		ExpectedTargetVersion: ready.Version, IdempotencyKey: fmt.Sprintf("search-activate-%d", activationOrdinal),
		ReasonCode: "BUILD_VERIFIED", At: at.Add(4 * time.Minute),
	}
	current, currentErr := repository.GetActive(ctx, workspaceID)
	if currentErr == nil {
		activation.ExpectedCurrentIndexVersionID = &current.ID
		activation.ExpectedCurrentVersion = &current.Version
	} else if !retrievalErrorKind(currentErr, foundation.ErrorNotFound) {
		t.Fatal(currentErr)
	}
	activated, err := repository.Activate(ctx, activation)
	if err != nil {
		t.Fatal(err)
	}
	return activated.ActiveIndexVersion
}

func searchEmbedding(id foundation.ID, metric domain.DistanceMetric, now time.Time) domain.EmbeddingVersion {
	return domain.EmbeddingVersion{
		ID: id, Provider: "test", AdapterName: "fake", AdapterVersion: "v1", Model: "search-v1",
		Dimensions: 3, Normalization: domain.NormalizationL2, DistanceMetric: metric,
		ConfigHash: snapshotHash("search-embedding-"+string(metric), int(now.Unix())), CreatedAt: now,
	}
}

func searchRRF(t *testing.T) []byte {
	t.Helper()
	value, err := domain.CanonicalRRFConfig(domain.RRFConfig{
		SchemaVersion: domain.RRFFusionSchemaVersionV1, Method: domain.FusionMethodRRF, K: 60,
		LexicalCandidateLimit: 20, VectorCandidateLimit: 20, FusedCandidateLimit: 20, RerankCandidateLimit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func setSearchSourcePath(t *testing.T, ctx context.Context, database *pgxpool.Pool, sourceID foundation.ID, path string) {
	t.Helper()
	if _, err := database.Exec(ctx, `UPDATE core.source SET logical_name=$1,original_location=$1 WHERE id=$2`, path, string(sourceID)); err != nil {
		t.Fatal(err)
	}
}

func assertSameSearchChunkSet(t *testing.T, lexical, vector []domain.SearchCandidate) {
	t.Helper()
	lexicalChunks := make(map[foundation.ID]struct{}, len(lexical))
	for _, candidate := range lexical {
		lexicalChunks[candidate.ChunkID] = struct{}{}
	}
	if len(lexicalChunks) != len(vector) {
		t.Fatalf("candidate set sizes lexical=%d vector=%d", len(lexicalChunks), len(vector))
	}
	for _, candidate := range vector {
		if _, ok := lexicalChunks[candidate.ChunkID]; !ok {
			t.Fatalf("vector chunk %s absent from lexical set", candidate.ChunkID)
		}
	}
}

func assertStableSearchRanks(t *testing.T, candidates []domain.SearchCandidate) {
	t.Helper()
	for index, candidate := range candidates {
		stage := candidate.Lexical
		if stage == nil {
			stage = candidate.Vector
		}
		if stage == nil || stage.Rank != int32(index+1) {
			t.Fatalf("candidate %d rank=%#v", index, stage)
		}
		if index > 0 {
			previous := candidates[index-1].Lexical
			if previous == nil {
				previous = candidates[index-1].Vector
			}
			if previous.Score == stage.Score && candidates[index-1].ChunkID > candidate.ChunkID {
				t.Fatalf("unstable chunk tie break at %d", index)
			}
		}
	}
}

func explainSearchQuery(t *testing.T, ctx context.Context, database *pgxpool.Pool, query string, arguments ...any) string {
	t.Helper()
	tx, err := database.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('pg_trgm.similarity_threshold',$1,true)`, searchTrigramThreshold); err != nil {
		t.Fatal(err)
	}
	var plan []byte
	if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON, COSTS OFF, VERBOSE) "+query, arguments...).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(plan) {
		t.Fatal("EXPLAIN did not return valid JSON")
	}
	return string(plan)
}
