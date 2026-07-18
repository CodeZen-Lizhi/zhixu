package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

const (
	searchSnippetCharacterLimit = domain.MaxEvidenceSnippetBytes
	searchRerankCharacterLimit  = domain.MaxRerankTextBytes
	searchTrigramThreshold      = "0.3"
)

// SearchDB 是检索查询所需的最小只读 PostgreSQL 边界。
type SearchDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
	Begin(context.Context) (pgx.Tx, error)
}

// SearchRepository 只读取 Workspace 当前 Active Index 的检索投影。
type SearchRepository struct {
	db SearchDB
}

var _ application.SearchStore = (*SearchRepository)(nil)

// NewSearchRepository 创建独立于索引构建 Repository 的只读检索 Store。
func NewSearchRepository(db SearchDB) (*SearchRepository, error) {
	if db == nil {
		return nil, dependency("RETRIEVAL_SEARCH_DATABASE_UNAVAILABLE", errors.New("search database is nil"))
	}
	return &SearchRepository{db: db}, nil
}

// LoadActiveSearchIndex 读取指定 Workspace 当前唯一 Active Index 及其持久检索配置。
func (r *SearchRepository) LoadActiveSearchIndex(ctx context.Context, workspaceID foundation.ID) (application.SearchIndex, error) {
	canonicalWorkspaceID, err := foundation.ParseID(string(workspaceID))
	if err != nil || canonicalWorkspaceID != workspaceID {
		return application.SearchIndex{}, searchInvalid("RETRIEVAL_SEARCH_WORKSPACE_INVALID", errors.New("workspace id is invalid"))
	}
	index, err := scanIndex(r.db.QueryRow(ctx, `SELECT `+indexColumns+`
		FROM retrieval.index_version WHERE workspace_id=$1 AND status='active'`, string(workspaceID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.SearchIndex{}, notFound("RETRIEVAL_ACTIVE_SEARCH_INDEX_NOT_FOUND", err)
	}
	if err != nil {
		return application.SearchIndex{}, classify(err, "RETRIEVAL_ACTIVE_SEARCH_INDEX_QUERY_FAILED")
	}
	result := application.SearchIndex{Index: index}
	if index.EmbeddingVersionID == nil {
		return result, nil
	}
	embedding, err := scanEmbedding(r.db.QueryRow(ctx, `SELECT `+embeddingColumns+`
		FROM retrieval.embedding_version WHERE id=$1`, string(*index.EmbeddingVersionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.SearchIndex{}, consistency("RETRIEVAL_ACTIVE_SEARCH_EMBEDDING_MISSING", err)
	}
	if err != nil {
		return application.SearchIndex{}, classify(err, "RETRIEVAL_ACTIVE_SEARCH_EMBEDDING_QUERY_FAILED")
	}
	fusion, err := domain.DecodeRRFConfig(index.FusionConfig)
	if err != nil {
		return application.SearchIndex{}, consistency("RETRIEVAL_ACTIVE_SEARCH_FUSION_INVALID", err)
	}
	result.EmbeddingVersion = &embedding
	result.Fusion = &fusion
	return result, nil
}

// SearchLexical 使用同一过滤集合执行 websearch FTS 与 trigram 候选查询。
func (r *SearchRepository) SearchLexical(ctx context.Context, query application.LexicalSearchQuery) ([]domain.SearchCandidate, error) {
	canonicalFilter, err := validateSearchQuery(query.WorkspaceID, query.IndexVersionID, query.Query, query.Filter, query.Limit)
	if err != nil {
		return nil, err
	}
	arguments := []any{
		string(query.WorkspaceID), string(query.IndexVersionID), strings.TrimSpace(query.Query), query.Limit,
		domain.MaxEvidenceProvenance,
	}
	filterSQL := appendSearchFilter(&arguments, canonicalFilter)
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, classify(err, "RETRIEVAL_LEXICAL_SEARCH_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('pg_trgm.similarity_threshold',$1,true)`, searchTrigramThreshold); err != nil {
		return nil, classify(err, "RETRIEVAL_LEXICAL_SEARCH_CONFIG_FAILED")
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(
		lexicalCandidateSQL, filterSQL, searchSnippetCharacterLimit, searchRerankCharacterLimit,
	), arguments...)
	if err != nil {
		return nil, classify(err, "RETRIEVAL_LEXICAL_SEARCH_QUERY_FAILED")
	}
	candidates, err := scanLexicalCandidates(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, classify(err, "RETRIEVAL_LEXICAL_SEARCH_COMMIT_FAILED")
	}
	return candidates, nil
}

// SearchVector 使用 Embedding Version 持久 distance metric 对应的固定 operator 执行 exact scan。
func (r *SearchRepository) SearchVector(ctx context.Context, query application.VectorSearchQuery) ([]domain.SearchCandidate, error) {
	canonicalFilter, err := validateSearchQuery(query.WorkspaceID, query.IndexVersionID, "vector", query.Filter, query.Limit)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateEmbeddingVector(query.EmbeddingVersion, query.QueryEmbedding); err != nil {
		return nil, err
	}
	persistedEmbedding, err := scanEmbedding(r.db.QueryRow(ctx, `SELECT `+embeddingColumns+`
		FROM retrieval.embedding_version WHERE id=$1`, string(query.EmbeddingVersion.ID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, consistency("RETRIEVAL_VECTOR_EMBEDDING_MISSING", err)
	}
	if err != nil {
		return nil, classify(err, "RETRIEVAL_VECTOR_EMBEDDING_QUERY_FAILED")
	}
	if persistedEmbedding.ID != query.EmbeddingVersion.ID || !domain.SameEmbeddingBinding(persistedEmbedding, query.EmbeddingVersion) {
		return nil, consistency("RETRIEVAL_VECTOR_EMBEDDING_BINDING_INVALID", errors.New("query embedding version differs from persisted binding"))
	}
	distanceExpression, ok := vectorDistanceExpression(persistedEmbedding.DistanceMetric)
	if !ok {
		return nil, searchInvalid("RETRIEVAL_VECTOR_DISTANCE_INVALID", errors.New("unsupported vector distance metric"))
	}
	arguments := []any{
		string(query.WorkspaceID), string(query.IndexVersionID), string(query.EmbeddingVersion.ID),
		pgvector.NewVector(query.QueryEmbedding), query.Limit, domain.MaxEvidenceProvenance,
	}
	filterSQL := appendSearchFilter(&arguments, canonicalFilter)
	rows, err := r.db.Query(ctx, fmt.Sprintf(
		vectorCandidateSQL, filterSQL, searchSnippetCharacterLimit, searchRerankCharacterLimit, distanceExpression,
	), arguments...)
	if err != nil {
		return nil, classify(err, "RETRIEVAL_VECTOR_SEARCH_QUERY_FAILED")
	}
	defer rows.Close()
	return scanVectorCandidates(rows)
}

func validateSearchQuery(
	workspaceID foundation.ID,
	indexVersionID foundation.ID,
	query string,
	filter domain.SearchFilter,
	limit int32,
) (domain.SearchFilter, error) {
	canonicalIndexID, err := foundation.ParseID(string(indexVersionID))
	if err != nil || canonicalIndexID != indexVersionID || limit <= 0 || limit > domain.MaxFusionCandidateLimit {
		return domain.SearchFilter{}, searchInvalid("RETRIEVAL_SEARCH_QUERY_INVALID", errors.New("search index or candidate limit is invalid"))
	}
	canonical, err := domain.CanonicalizeSearchRequest(domain.SearchRequest{
		WorkspaceID: workspaceID,
		Query:       query,
		Mode:        domain.SearchModeKeyword,
		Filter:      filter,
		Limit:       1,
	})
	if err != nil {
		return domain.SearchFilter{}, err
	}
	return canonical.Filter, nil
}

func appendSearchFilter(arguments *[]any, filter domain.SearchFilter) string {
	clauses := make([]string, 0, 5)
	appendArgument := func(value any) int {
		*arguments = append(*arguments, value)
		return len(*arguments)
	}
	if len(filter.SourceIDs) > 0 {
		position := appendArgument(idsAsStrings(filter.SourceIDs))
		clauses = append(clauses, fmt.Sprintf("source_manifest.source_id = ANY($%d::uuid[])", position))
	}
	if len(filter.SourceVersionIDs) > 0 {
		position := appendArgument(idsAsStrings(filter.SourceVersionIDs))
		clauses = append(clauses, fmt.Sprintf("source_manifest.source_version_id = ANY($%d::uuid[])", position))
	}
	if len(filter.PathPrefixes) > 0 {
		position := appendArgument(filter.PathPrefixes)
		clauses = append(clauses, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM unnest($%d::text[]) AS path_filter(prefix)
			WHERE source_record.original_location = path_filter.prefix
			   OR left(source_record.original_location, length(path_filter.prefix) + 1) = path_filter.prefix || '/'
		)`, position))
	}
	if filter.CapturedAtFrom != nil {
		position := appendArgument(filter.CapturedAtFrom.UTC())
		clauses = append(clauses, fmt.Sprintf("source_version.captured_at >= $%d", position))
	}
	if filter.CapturedAtBefore != nil {
		position := appendArgument(filter.CapturedAtBefore.UTC())
		clauses = append(clauses, fmt.Sprintf("source_version.captured_at < $%d", position))
	}
	if len(clauses) == 0 {
		return ""
	}
	return " AND " + strings.Join(clauses, " AND ")
}

func idsAsStrings(values []foundation.ID) []string {
	result := make([]string, len(values))
	for index := range values {
		result[index] = string(values[index])
	}
	return result
}

func vectorDistanceExpression(metric domain.DistanceMetric) (string, bool) {
	switch metric {
	case domain.DistanceCosine:
		return "projection.embedding <=> $4::vector", true
	case domain.DistanceInnerProduct:
		return "projection.embedding <#> $4::vector", true
	case domain.DistanceEuclidean:
		return "projection.embedding <-> $4::vector", true
	default:
		return "", false
	}
}

type searchCandidateScan struct {
	candidate      domain.SearchCandidate
	embeddingID    *string
	headingPath    []byte
	provenances    []byte
	lexicalFTS     float64
	lexicalTrigram float64
	stageRank      int32
	stageScore     float64
}

func scanLexicalCandidates(rows pgx.Rows) ([]domain.SearchCandidate, error) {
	result := make([]domain.SearchCandidate, 0)
	for rows.Next() {
		var value searchCandidateScan
		if err := rows.Scan(
			&value.candidate.WorkspaceID, &value.candidate.IndexVersionID, &value.embeddingID,
			&value.candidate.ChunkID, &value.candidate.ParseProjectionID, &value.candidate.Sequence,
			&value.candidate.ContentHash, &value.headingPath, &value.candidate.Span.ID,
			&value.candidate.Span.StartLine, &value.candidate.Span.EndLine,
			&value.candidate.Span.StartByte, &value.candidate.Span.EndByte,
			&value.candidate.Snippet, &value.candidate.RerankText, &value.provenances,
			&value.candidate.ProvenanceTruncated, &value.stageRank, &value.stageScore,
			&value.lexicalFTS, &value.lexicalTrigram,
		); err != nil {
			return nil, classify(err, "RETRIEVAL_LEXICAL_SEARCH_SCAN_FAILED")
		}
		if err := finalizeSearchCandidate(&value); err != nil {
			return nil, err
		}
		value.candidate.Lexical = &domain.CandidateStageScore{Rank: value.stageRank, Score: value.stageScore}
		value.candidate.LexicalFTSScore = float64Pointer(value.lexicalFTS)
		value.candidate.LexicalTrigramScore = float64Pointer(value.lexicalTrigram)
		if err := domain.ValidateSearchCandidate(value.candidate); err != nil {
			return nil, consistency("RETRIEVAL_LEXICAL_SEARCH_CANDIDATE_INVALID", err)
		}
		result = append(result, value.candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, "RETRIEVAL_LEXICAL_SEARCH_QUERY_FAILED")
	}
	return result, nil
}

func scanVectorCandidates(rows pgx.Rows) ([]domain.SearchCandidate, error) {
	result := make([]domain.SearchCandidate, 0)
	for rows.Next() {
		var value searchCandidateScan
		if err := rows.Scan(
			&value.candidate.WorkspaceID, &value.candidate.IndexVersionID, &value.embeddingID,
			&value.candidate.ChunkID, &value.candidate.ParseProjectionID, &value.candidate.Sequence,
			&value.candidate.ContentHash, &value.headingPath, &value.candidate.Span.ID,
			&value.candidate.Span.StartLine, &value.candidate.Span.EndLine,
			&value.candidate.Span.StartByte, &value.candidate.Span.EndByte,
			&value.candidate.Snippet, &value.candidate.RerankText, &value.provenances,
			&value.candidate.ProvenanceTruncated, &value.stageRank, &value.stageScore,
		); err != nil {
			return nil, classify(err, "RETRIEVAL_VECTOR_SEARCH_SCAN_FAILED")
		}
		if err := finalizeSearchCandidate(&value); err != nil {
			return nil, err
		}
		value.candidate.Vector = &domain.CandidateStageScore{Rank: value.stageRank, Score: value.stageScore}
		if err := domain.ValidateSearchCandidate(value.candidate); err != nil {
			return nil, consistency("RETRIEVAL_VECTOR_SEARCH_CANDIDATE_INVALID", err)
		}
		result = append(result, value.candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, "RETRIEVAL_VECTOR_SEARCH_QUERY_FAILED")
	}
	return result, nil
}

func finalizeSearchCandidate(value *searchCandidateScan) error {
	if value.embeddingID != nil {
		id := foundation.ID(*value.embeddingID)
		value.candidate.EmbeddingVersionID = &id
	}
	if err := json.Unmarshal(value.headingPath, &value.candidate.HeadingPath); err != nil {
		return consistency("RETRIEVAL_SEARCH_HEADING_PATH_INVALID", err)
	}
	var provenances []searchProvenanceJSON
	if err := json.Unmarshal(value.provenances, &provenances); err != nil {
		return consistency("RETRIEVAL_SEARCH_PROVENANCE_INVALID", err)
	}
	value.candidate.Provenances = make([]domain.EvidenceProvenance, len(provenances))
	for index := range provenances {
		value.candidate.Provenances[index] = domain.EvidenceProvenance{
			SourceID: provenances[index].SourceID, SourceVersionID: provenances[index].SourceVersionID,
			RelativePath: provenances[index].RelativePath, CapturedAt: provenances[index].CapturedAt.UTC(),
		}
	}
	value.candidate.Snippet = truncateSearchText(value.candidate.Snippet, domain.MaxEvidenceSnippetBytes)
	value.candidate.RerankText = truncateSearchText(value.candidate.RerankText, domain.MaxRerankTextBytes)
	return nil
}

type searchProvenanceJSON struct {
	SourceID        foundation.ID `json:"source_id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
	RelativePath    string        `json:"relative_path"`
	CapturedAt      time.Time     `json:"captured_at"`
}

func truncateSearchText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func float64Pointer(value float64) *float64 {
	return &value
}

func searchInvalid(code string, err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, err)
}

const lexicalCandidateSQL = `
WITH active_index AS MATERIALIZED (
	SELECT id,workspace_id,embedding_version_id
	FROM retrieval.index_version
	WHERE workspace_id=$1 AND id=$2 AND status='active'
),
query_input AS MATERIALIZED (
	SELECT websearch_to_tsquery('simple',$3) AS query
),
lexical_match_chunks AS MATERIALIZED (
	SELECT projection.chunk_id
	FROM active_index
	JOIN retrieval.chunk_projection projection
	  ON projection.workspace_id=active_index.workspace_id
	 AND projection.index_version_id=active_index.id
	CROSS JOIN query_input
	WHERE projection.lexical_status='ready'
	  AND projection.search_vector @@ query_input.query
	UNION
	SELECT chunk.id AS chunk_id
	FROM active_index
	JOIN retrieval.index_manifest_chunk manifest
	  ON manifest.workspace_id=active_index.workspace_id
	 AND manifest.index_version_id=active_index.id
	JOIN ingestion.canonical_chunk chunk
	  ON chunk.workspace_id=active_index.workspace_id
	 AND chunk.id=manifest.chunk_id
	 AND chunk.status='active'
	WHERE chunk.content %% $3
),
filtered_provenance AS MATERIALIZED (
	SELECT source_manifest.index_version_id,chunk.id AS chunk_id,
		source_manifest.source_id,source_manifest.source_version_id,
		source_record.original_location AS relative_path,source_version.captured_at
	FROM active_index
	JOIN retrieval.index_manifest_source source_manifest
	  ON source_manifest.index_version_id=active_index.id
	 AND source_manifest.workspace_id=active_index.workspace_id
	 AND source_manifest.selection_status='included'
	JOIN core.source source_record
	  ON source_record.id=source_manifest.source_id
	 AND source_record.workspace_id=active_index.workspace_id
	JOIN core.source_version source_version
	  ON source_version.id=source_manifest.source_version_id
	 AND source_version.source_id=source_manifest.source_id
	JOIN ingestion.canonical_chunk chunk
	  ON chunk.workspace_id=active_index.workspace_id
	 AND chunk.parse_projection_id=source_manifest.parse_projection_id
	 AND chunk.status='active'
	JOIN retrieval.index_manifest_chunk manifest
	  ON manifest.index_version_id=active_index.id
	 AND manifest.workspace_id=active_index.workspace_id
	 AND manifest.chunk_id=chunk.id
	WHERE true%s
),
matched_chunks AS MATERIALIZED (
	SELECT index_version_id,chunk_id
	FROM filtered_provenance
	GROUP BY index_version_id,chunk_id
),
scored AS MATERIALIZED (
	SELECT active_index.workspace_id::text AS workspace_id,
		active_index.id::text AS index_version_id,
		active_index.embedding_version_id::text AS embedding_version_id,
		chunk.id AS chunk_uuid,chunk.id::text AS chunk_id,chunk.parse_projection_id::text AS parse_projection_id,
		chunk.sequence,chunk.content_hash,chunk.heading_path,
		span.id::text AS span_id,span.start_line,span.end_line,span.start_byte,span.end_byte,
		left(chunk.content,%d) AS snippet,left(chunk.content,%d) AS rerank_text,
		ts_rank_cd(projection.search_vector,query_input.query)::double precision AS fts_score,
		similarity(chunk.content,$3)::double precision AS trigram_score
	FROM active_index
	JOIN retrieval.chunk_projection projection
	  ON projection.index_version_id=active_index.id
	 AND projection.workspace_id=active_index.workspace_id
	 AND projection.lexical_status='ready'
	JOIN ingestion.canonical_chunk chunk
	  ON chunk.id=projection.chunk_id
	 AND chunk.workspace_id=active_index.workspace_id
	 AND chunk.status='active'
	JOIN ingestion.source_span span
	  ON span.id=chunk.source_span_id
	 AND span.workspace_id=active_index.workspace_id
	JOIN matched_chunks
	  ON matched_chunks.index_version_id=active_index.id
	 AND matched_chunks.chunk_id=chunk.id
	JOIN lexical_match_chunks
	  ON lexical_match_chunks.chunk_id=chunk.id
	CROSS JOIN query_input
),
ranked AS (
	SELECT scored.*,
		row_number() OVER (ORDER BY fts_score + trigram_score DESC,chunk_uuid)::integer AS stage_rank,
		(fts_score + trigram_score)::double precision AS stage_score
	FROM scored
),
limited AS MATERIALIZED (
	SELECT * FROM ranked WHERE stage_rank <= $4
),
ranked_provenance AS MATERIALIZED (
	SELECT filtered_provenance.*,
		row_number() OVER (
			PARTITION BY filtered_provenance.index_version_id,filtered_provenance.chunk_id
			ORDER BY filtered_provenance.source_id,filtered_provenance.source_version_id,filtered_provenance.relative_path
		) AS provenance_rank,
		count(*) OVER (
			PARTITION BY filtered_provenance.index_version_id,filtered_provenance.chunk_id
		) AS provenance_count
	FROM filtered_provenance
	JOIN limited
	  ON limited.chunk_uuid=filtered_provenance.chunk_id
),
provenance AS MATERIALIZED (
	SELECT index_version_id,chunk_id,
		jsonb_agg(jsonb_build_object(
			'source_id',source_id::text,
			'source_version_id',source_version_id::text,
			'relative_path',relative_path,
			'captured_at',captured_at
		) ORDER BY source_id,source_version_id,relative_path)
			FILTER (WHERE provenance_rank <= $5) AS provenances,
		max(provenance_count) > $5 AS provenance_truncated
	FROM ranked_provenance
	GROUP BY index_version_id,chunk_id
)
SELECT limited.workspace_id,limited.index_version_id,limited.embedding_version_id,limited.chunk_id,
	limited.parse_projection_id,limited.sequence,
	content_hash,heading_path,span_id,start_line,end_line,start_byte,end_byte,snippet,rerank_text,
	provenance.provenances,provenance.provenance_truncated,
	limited.stage_rank,limited.stage_score,limited.fts_score,limited.trigram_score
FROM limited
JOIN provenance
  ON provenance.index_version_id=limited.index_version_id::uuid
 AND provenance.chunk_id=limited.chunk_uuid
ORDER BY limited.stage_rank`

const vectorCandidateSQL = `
WITH active_index AS MATERIALIZED (
	SELECT id,workspace_id,embedding_version_id
	FROM retrieval.index_version
	WHERE workspace_id=$1 AND id=$2 AND embedding_version_id=$3 AND status='active'
),
filtered_provenance AS MATERIALIZED (
	SELECT source_manifest.index_version_id,chunk.id AS chunk_id,
		source_manifest.source_id,source_manifest.source_version_id,
		source_record.original_location AS relative_path,source_version.captured_at
	FROM active_index
	JOIN retrieval.index_manifest_source source_manifest
	  ON source_manifest.index_version_id=active_index.id
	 AND source_manifest.workspace_id=active_index.workspace_id
	 AND source_manifest.selection_status='included'
	JOIN core.source source_record
	  ON source_record.id=source_manifest.source_id
	 AND source_record.workspace_id=active_index.workspace_id
	JOIN core.source_version source_version
	  ON source_version.id=source_manifest.source_version_id
	 AND source_version.source_id=source_manifest.source_id
	JOIN ingestion.canonical_chunk chunk
	  ON chunk.workspace_id=active_index.workspace_id
	 AND chunk.parse_projection_id=source_manifest.parse_projection_id
	 AND chunk.status='active'
	JOIN retrieval.index_manifest_chunk manifest
	  ON manifest.index_version_id=active_index.id
	 AND manifest.workspace_id=active_index.workspace_id
	 AND manifest.chunk_id=chunk.id
	WHERE true%s
),
matched_chunks AS MATERIALIZED (
	SELECT index_version_id,chunk_id
	FROM filtered_provenance
	GROUP BY index_version_id,chunk_id
),
scored AS MATERIALIZED (
	SELECT active_index.workspace_id::text AS workspace_id,
		active_index.id::text AS index_version_id,
		active_index.embedding_version_id::text AS embedding_version_id,
		chunk.id AS chunk_uuid,chunk.id::text AS chunk_id,chunk.parse_projection_id::text AS parse_projection_id,
		chunk.sequence,chunk.content_hash,chunk.heading_path,
		span.id::text AS span_id,span.start_line,span.end_line,span.start_byte,span.end_byte,
		left(chunk.content,%d) AS snippet,left(chunk.content,%d) AS rerank_text,
		(%s)::double precision AS distance
	FROM active_index
	JOIN retrieval.chunk_projection projection
	  ON projection.index_version_id=active_index.id
	 AND projection.workspace_id=active_index.workspace_id
	 AND projection.embedding_version_id=$3
	 AND projection.lexical_status='ready'
	 AND projection.vector_status='ready'
	JOIN ingestion.canonical_chunk chunk
	  ON chunk.id=projection.chunk_id
	 AND chunk.workspace_id=active_index.workspace_id
	 AND chunk.status='active'
	JOIN ingestion.source_span span
	  ON span.id=chunk.source_span_id
	 AND span.workspace_id=active_index.workspace_id
	JOIN matched_chunks
	  ON matched_chunks.index_version_id=active_index.id
	 AND matched_chunks.chunk_id=chunk.id
),
ranked AS (
	SELECT scored.*,
		row_number() OVER (ORDER BY distance,chunk_uuid)::integer AS stage_rank
	FROM scored
),
limited AS MATERIALIZED (
	SELECT * FROM ranked WHERE stage_rank <= $5
),
ranked_provenance AS MATERIALIZED (
	SELECT filtered_provenance.*,
		row_number() OVER (
			PARTITION BY filtered_provenance.index_version_id,filtered_provenance.chunk_id
			ORDER BY filtered_provenance.source_id,filtered_provenance.source_version_id,filtered_provenance.relative_path
		) AS provenance_rank,
		count(*) OVER (
			PARTITION BY filtered_provenance.index_version_id,filtered_provenance.chunk_id
		) AS provenance_count
	FROM filtered_provenance
	JOIN limited
	  ON limited.chunk_uuid=filtered_provenance.chunk_id
),
provenance AS MATERIALIZED (
	SELECT index_version_id,chunk_id,
		jsonb_agg(jsonb_build_object(
			'source_id',source_id::text,
			'source_version_id',source_version_id::text,
			'relative_path',relative_path,
			'captured_at',captured_at
		) ORDER BY source_id,source_version_id,relative_path)
			FILTER (WHERE provenance_rank <= $6) AS provenances,
		max(provenance_count) > $6 AS provenance_truncated
	FROM ranked_provenance
	GROUP BY index_version_id,chunk_id
)
SELECT limited.workspace_id,limited.index_version_id,limited.embedding_version_id,limited.chunk_id,
	limited.parse_projection_id,limited.sequence,
	content_hash,heading_path,span_id,start_line,end_line,start_byte,end_byte,snippet,rerank_text,
	provenance.provenances,provenance.provenance_truncated,limited.stage_rank,limited.distance
FROM limited
JOIN provenance
  ON provenance.index_version_id=limited.index_version_id::uuid
 AND provenance.chunk_id=limited.chunk_uuid
ORDER BY limited.stage_rank`
