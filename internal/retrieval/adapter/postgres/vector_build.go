package postgres

import (
	"context"
	"errors"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

var _ application.VectorBuildStore = (*Repository)(nil)

// LoadVectorBuildPage 在短事务中批量读取 pending Projection、缓存命中和有界正文。
func (r *Repository) LoadVectorBuildPage(ctx context.Context, command domain.VectorBuildPageCommand) (domain.VectorBuildPage, error) {
	if err := domain.ValidateVectorBuildPageCommand(command); err != nil {
		return domain.VectorBuildPage{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.VectorBuildPage{}, classify(err, "RETRIEVAL_VECTOR_PAGE_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	index, err := getIndexTx(ctx, tx, command.WorkspaceID, command.IndexVersionID)
	if err != nil {
		return domain.VectorBuildPage{}, err
	}
	if index.Version != command.ExpectedIndexVersion {
		return domain.VectorBuildPage{}, conflict("RETRIEVAL_INDEX_VERSION_CONFLICT", errors.New("vector build index version is stale"))
	}
	if index.Status != domain.IndexStatusBuilding || index.EmbeddingVersionID == nil {
		return domain.VectorBuildPage{}, conflict(domain.ErrorCodeProjectionInvalid, errors.New("vector page requires a building hybrid index"))
	}
	embedding, err := getEmbedding(ctx, tx, *index.EmbeddingVersionID)
	if err != nil {
		return domain.VectorBuildPage{}, err
	}
	rows, err := tx.Query(ctx, `SELECT manifest.chunk_id::text,manifest.content_hash,manifest.sequence,
		projection.token_count,chunk.atomic_oversized,chunk.byte_count,octet_length(chunk.content),cache.embedding
		FROM retrieval.index_manifest_chunk manifest
		JOIN retrieval.chunk_projection projection
		  ON projection.index_version_id=manifest.index_version_id AND projection.chunk_id=manifest.chunk_id
		JOIN ingestion.canonical_chunk chunk
		  ON chunk.id=manifest.chunk_id AND chunk.workspace_id=manifest.workspace_id
		 AND chunk.content_hash=manifest.content_hash AND chunk.sequence=manifest.sequence
		 AND chunk.parser_version=manifest.parser_version
		 AND chunk.chunk_strategy_version=manifest.chunk_strategy_version AND chunk.schema_version=manifest.schema_version
		LEFT JOIN retrieval.embedding_cache cache
		  ON cache.workspace_id=manifest.workspace_id AND cache.embedding_version_id=$3
		 AND cache.content_hash=manifest.content_hash
		WHERE manifest.workspace_id=$1 AND manifest.index_version_id=$2
		  AND projection.embedding_version_id=$3 AND projection.lexical_status='ready' AND projection.vector_status='pending'
		ORDER BY manifest.sequence,manifest.chunk_id LIMIT $4`, string(command.WorkspaceID), string(command.IndexVersionID),
		string(embedding.ID), command.Limit+1)
	if err != nil {
		return domain.VectorBuildPage{}, classify(err, "RETRIEVAL_VECTOR_PAGE_QUERY_FAILED")
	}
	defer rows.Close()
	candidates := make([]domain.VectorBuildInput, 0, command.Limit+1)
	for rows.Next() {
		var input domain.VectorBuildInput
		var declaredBytes int64
		var cached *pgvector.Vector
		if err := rows.Scan(&input.ChunkID, &input.ContentHash, &input.Sequence, &input.TokenCount,
			&input.AtomicOversized, &declaredBytes, &input.InputBytes, &cached); err != nil {
			return domain.VectorBuildPage{}, classify(err, "RETRIEVAL_VECTOR_PAGE_SCAN_FAILED")
		}
		if declaredBytes != input.InputBytes {
			return domain.VectorBuildPage{}, consistency("RETRIEVAL_VECTOR_INPUT_BYTES_INVALID", errors.New("canonical chunk byte count differs from content"))
		}
		if cached != nil {
			input.CachedEmbedding = cached.Slice()
		}
		candidates = append(candidates, input)
	}
	if err := rows.Err(); err != nil {
		return domain.VectorBuildPage{}, classify(err, "RETRIEVAL_VECTOR_PAGE_SCAN_FAILED")
	}
	inputs := make([]domain.VectorBuildInput, 0, min(len(candidates), int(command.Limit)))
	var inputBytes int64
	hasMore := false
	for _, input := range candidates {
		if len(inputs) == int(command.Limit) {
			hasMore = true
			break
		}
		loadedBytes := int64(0)
		if len(input.CachedEmbedding) == 0 && input.InputBytes <= int64(command.MaxInputBytes) {
			loadedBytes = input.InputBytes
		}
		if inputBytes+loadedBytes > command.MaxBatchInputBytes {
			hasMore = true
			break
		}
		inputBytes += loadedBytes
		inputs = append(inputs, input)
	}
	if len(inputs) < len(candidates) {
		hasMore = true
	}
	contentIDs := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if len(input.CachedEmbedding) == 0 && input.InputBytes <= int64(command.MaxInputBytes) {
			contentIDs = append(contentIDs, string(input.ChunkID))
		}
	}
	if len(contentIDs) > 0 {
		contentRows, queryErr := tx.Query(ctx, `SELECT id::text,content FROM ingestion.canonical_chunk
			WHERE workspace_id=$1 AND id=ANY($2::uuid[]) ORDER BY id`, string(command.WorkspaceID), contentIDs)
		if queryErr != nil {
			return domain.VectorBuildPage{}, classify(queryErr, "RETRIEVAL_VECTOR_CONTENT_QUERY_FAILED")
		}
		contents := make(map[foundation.ID]string, len(contentIDs))
		for contentRows.Next() {
			var chunkID foundation.ID
			var content string
			if scanErr := contentRows.Scan(&chunkID, &content); scanErr != nil {
				contentRows.Close()
				return domain.VectorBuildPage{}, classify(scanErr, "RETRIEVAL_VECTOR_CONTENT_SCAN_FAILED")
			}
			contents[chunkID] = content
		}
		if rowsErr := contentRows.Err(); rowsErr != nil {
			contentRows.Close()
			return domain.VectorBuildPage{}, classify(rowsErr, "RETRIEVAL_VECTOR_CONTENT_SCAN_FAILED")
		}
		contentRows.Close()
		if len(contents) != len(contentIDs) {
			return domain.VectorBuildPage{}, consistency("RETRIEVAL_VECTOR_CONTENT_INVALID", errors.New("vector input content readback is incomplete"))
		}
		for index := range inputs {
			if content, ok := contents[inputs[index].ChunkID]; ok {
				if int64(len(content)) != inputs[index].InputBytes {
					return domain.VectorBuildPage{}, consistency("RETRIEVAL_VECTOR_INPUT_BYTES_INVALID", errors.New("canonical chunk content size changed"))
				}
				inputs[index].Content = content
			}
		}
	}
	page := domain.VectorBuildPage{Index: index, EmbeddingVersion: embedding, Inputs: inputs, HasMore: hasMore}
	if err := domain.ValidateVectorBuildPage(command, page); err != nil {
		return domain.VectorBuildPage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.VectorBuildPage{}, classify(err, "RETRIEVAL_VECTOR_PAGE_COMMIT_FAILED")
	}
	return page, nil
}

// CommitVectorBuildBatch 在一个事务中精确写入/重放缓存并更新 Projection 终态。
func (r *Repository) CommitVectorBuildBatch(ctx context.Context, batch domain.VectorBuildBatch) (domain.VectorBuildCommitResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.VectorBuildCommitResult{}, classify(err, "RETRIEVAL_VECTOR_BUILD_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	index, err := getIndexForUpdate(ctx, tx, batch.WorkspaceID, batch.IndexVersionID)
	if err != nil {
		return domain.VectorBuildCommitResult{}, err
	}
	embedding, err := getEmbedding(ctx, tx, batch.EmbeddingVersionID)
	if err != nil {
		return domain.VectorBuildCommitResult{}, err
	}
	if err := domain.ValidateVectorBuildBatch(index, embedding, batch); err != nil {
		return domain.VectorBuildCommitResult{}, err
	}
	if err := validateVectorBuildTargets(ctx, tx, batch); err != nil {
		return domain.VectorBuildCommitResult{}, err
	}
	if err := commitEmbeddingCache(ctx, tx, batch); err != nil {
		return domain.VectorBuildCommitResult{}, err
	}
	projections := make([]domain.VectorProjectionWrite, len(batch.Writes))
	for index := range batch.Writes {
		write := batch.Writes[index]
		projections[index] = domain.VectorProjectionWrite{
			ChunkID: write.ChunkID, Embedding: append([]float32(nil), write.Embedding...), TokenCount: write.TokenCount,
			VectorStatus: write.VectorStatus, FailureCode: write.FailureCode,
		}
	}
	projectionResult, err := saveVectorProjectionBatchTx(ctx, tx, domain.VectorProjectionBatch{
		WorkspaceID: batch.WorkspaceID, IndexVersionID: batch.IndexVersionID, EmbeddingVersionID: batch.EmbeddingVersionID,
		ExpectedIndexVersion: batch.ExpectedIndexVersion, Projections: projections, At: batch.At,
	})
	if err != nil {
		return domain.VectorBuildCommitResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.VectorBuildCommitResult{}, classify(err, "RETRIEVAL_VECTOR_BUILD_COMMIT_FAILED")
	}
	return domain.VectorBuildCommitResult{
		IndexVersionID: projectionResult.IndexVersionID, AttemptedCount: projectionResult.AttemptedCount,
		InsertedCount: projectionResult.InsertedCount, ReplayedCount: projectionResult.ReplayedCount,
	}, nil
}

func validateVectorBuildTargets(ctx context.Context, tx pgx.Tx, batch domain.VectorBuildBatch) error {
	chunkIDs := make([]string, len(batch.Writes))
	writes := make(map[foundation.ID]domain.VectorBuildWrite, len(batch.Writes))
	for index, write := range batch.Writes {
		chunkIDs[index] = string(write.ChunkID)
		writes[write.ChunkID] = write
	}
	rows, err := tx.Query(ctx, `SELECT projection.chunk_id::text,manifest.content_hash,projection.token_count
		FROM retrieval.chunk_projection projection
		JOIN retrieval.index_manifest_chunk manifest
		  ON manifest.index_version_id=projection.index_version_id AND manifest.chunk_id=projection.chunk_id
		JOIN ingestion.canonical_chunk chunk
		  ON chunk.id=manifest.chunk_id AND chunk.workspace_id=manifest.workspace_id
		 AND chunk.content_hash=manifest.content_hash AND chunk.sequence=manifest.sequence
		 AND chunk.parser_version=manifest.parser_version
		 AND chunk.chunk_strategy_version=manifest.chunk_strategy_version AND chunk.schema_version=manifest.schema_version
		WHERE projection.index_version_id=$1 AND projection.workspace_id=$2
		  AND projection.embedding_version_id=$3 AND projection.chunk_id=ANY($4::uuid[])
		ORDER BY projection.chunk_id FOR UPDATE OF projection`, string(batch.IndexVersionID), string(batch.WorkspaceID),
		string(batch.EmbeddingVersionID), chunkIDs)
	if err != nil {
		return classify(err, "RETRIEVAL_VECTOR_TARGET_QUERY_FAILED")
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var chunkID foundation.ID
		var contentHash string
		var tokenCount int32
		if err := rows.Scan(&chunkID, &contentHash, &tokenCount); err != nil {
			return classify(err, "RETRIEVAL_VECTOR_TARGET_SCAN_FAILED")
		}
		write, ok := writes[chunkID]
		if !ok || write.ContentHash != contentHash {
			return consistency("RETRIEVAL_VECTOR_TARGET_BINDING_INVALID", errors.New("vector write does not match frozen manifest"))
		}
		if write.TokenCount != tokenCount {
			return conflict("RETRIEVAL_VECTOR_TOKEN_COUNT_CONFLICT", errors.New("vector token count differs from deterministic lexical token count"))
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return classify(err, "RETRIEVAL_VECTOR_TARGET_SCAN_FAILED")
	}
	if count != len(batch.Writes) {
		return notFound("RETRIEVAL_VECTOR_TARGET_NOT_FOUND", errors.New("vector batch does not exactly cover persisted targets"))
	}
	return nil
}

func commitEmbeddingCache(ctx context.Context, tx pgx.Tx, batch domain.VectorBuildBatch) error {
	ready := make(map[string][]float32)
	for _, write := range batch.Writes {
		if write.VectorStatus != domain.VectorStatusReady {
			continue
		}
		ready[write.ContentHash] = write.Embedding
	}
	if len(ready) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(ready))
	for hash := range ready {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	vectors := make([]string, len(hashes))
	for index, hash := range hashes {
		vectors[index] = pgvector.NewVector(ready[hash]).String()
	}
	if _, err := tx.Exec(ctx, `INSERT INTO retrieval.embedding_cache(
			workspace_id,embedding_version_id,content_hash,embedding,created_at
		)
		SELECT $1,$2,input.content_hash,input.embedding_text::vector,$5
		FROM unnest($3::text[],$4::text[]) AS input(content_hash,embedding_text)
		ON CONFLICT DO NOTHING`, string(batch.WorkspaceID), string(batch.EmbeddingVersionID), hashes, vectors, batch.At.UTC()); err != nil {
		return classify(err, "RETRIEVAL_EMBEDDING_CACHE_INSERT_FAILED")
	}
	rows, err := tx.Query(ctx, `SELECT content_hash,embedding FROM retrieval.embedding_cache
		WHERE workspace_id=$1 AND embedding_version_id=$2 AND content_hash=ANY($3::text[]) ORDER BY content_hash`,
		string(batch.WorkspaceID), string(batch.EmbeddingVersionID), hashes)
	if err != nil {
		return classify(err, "RETRIEVAL_EMBEDDING_CACHE_QUERY_FAILED")
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var hash string
		var vector pgvector.Vector
		if err := rows.Scan(&hash, &vector); err != nil {
			return classify(err, "RETRIEVAL_EMBEDDING_CACHE_SCAN_FAILED")
		}
		if expected, ok := ready[hash]; !ok || !sameVectorValues(expected, vector.Slice()) {
			return consistency("RETRIEVAL_EMBEDDING_CACHE_REPLAY_CONFLICT", errors.New("embedding cache contains a different vector"))
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return classify(err, "RETRIEVAL_EMBEDDING_CACHE_SCAN_FAILED")
	}
	if seen != len(hashes) {
		return consistency("RETRIEVAL_EMBEDDING_CACHE_REPLAY_CONFLICT", errors.New("embedding cache readback is incomplete"))
	}
	return nil
}

func sameVectorValues(left, right []float32) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
