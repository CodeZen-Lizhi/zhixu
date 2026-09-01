package postgres

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/lib/pq"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

var _ application.VectorBuildStore = (*GORMRepository)(nil)

// SaveVectorBatch atomically writes vector cache and projection terminal facts.
func (repository *GORMRepository) SaveVectorBatch(ctx context.Context, batch domain.VectorProjectionBatch) (result domain.ProjectionBatchResult, err error) {
	err = repository.within(ctx, foundation.TransactionOptions{}, "RETRIEVAL_VECTOR_TRANSACTION_FAILED", "RETRIEVAL_VECTOR_COMMIT_FAILED",
		func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
			index, queryErr := gormRetrievalLoadIndex(callbackCtx, transaction, batch.WorkspaceID, batch.IndexVersionID, true)
			if gormRetrievalNoRows(queryErr) {
				return notFound("RETRIEVAL_INDEX_NOT_FOUND", queryErr)
			}
			if queryErr != nil {
				return classifyGORMRetrieval(callbackCtx, queryErr, "RETRIEVAL_INDEX_QUERY_FAILED")
			}
			embedding, queryErr := gormRetrievalLoadEmbedding(callbackCtx, transaction, batch.EmbeddingVersionID)
			if gormRetrievalNoRows(queryErr) {
				return notFound("RETRIEVAL_EMBEDDING_NOT_FOUND", queryErr)
			}
			if queryErr != nil {
				return classifyGORMRetrieval(callbackCtx, queryErr, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
			}
			if validateErr := domain.ValidateVectorProjectionBatch(index, embedding, batch); validateErr != nil {
				return validateErr
			}
			cacheAwareBatch, buildErr := gormRetrievalVectorBuildBatchFromProjectionBatch(callbackCtx, transaction, batch)
			if buildErr != nil {
				return buildErr
			}
			if validateErr := domain.ValidateVectorBuildBatch(index, embedding, cacheAwareBatch); validateErr != nil {
				return validateErr
			}
			if targetErr := gormRetrievalValidateVectorBuildTargets(callbackCtx, transaction, cacheAwareBatch); targetErr != nil {
				return targetErr
			}
			if cacheErr := gormRetrievalCommitEmbeddingCache(callbackCtx, transaction, cacheAwareBatch); cacheErr != nil {
				return cacheErr
			}
			var saveErr error
			result, saveErr = gormRetrievalSaveVectorProjectionBatch(callbackCtx, transaction, batch)
			return saveErr
		})
	if err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	return result, nil
}

// LoadVectorBuildPage loads one bounded pending page and only the content that
// can fit the caller's current provider limits.
func (repository *GORMRepository) LoadVectorBuildPage(ctx context.Context, command domain.VectorBuildPageCommand) (page domain.VectorBuildPage, err error) {
	if readyErr := repository.ready(ctx); readyErr != nil {
		return domain.VectorBuildPage{}, readyErr
	}
	if validateErr := domain.ValidateVectorBuildPageCommand(command); validateErr != nil {
		return domain.VectorBuildPage{}, validateErr
	}
	err = repository.within(ctx, foundation.TransactionOptions{ReadOnly: true}, "RETRIEVAL_VECTOR_PAGE_TRANSACTION_FAILED", "RETRIEVAL_VECTOR_PAGE_COMMIT_FAILED",
		func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
			index, queryErr := gormRetrievalLoadIndex(callbackCtx, transaction, command.WorkspaceID, command.IndexVersionID, false)
			if gormRetrievalNoRows(queryErr) {
				return notFound("RETRIEVAL_INDEX_NOT_FOUND", queryErr)
			}
			if queryErr != nil {
				return classifyGORMRetrieval(callbackCtx, queryErr, "RETRIEVAL_INDEX_QUERY_FAILED")
			}
			if index.Version != command.ExpectedIndexVersion {
				return conflict("RETRIEVAL_INDEX_VERSION_CONFLICT", errors.New("vector build index version is stale"))
			}
			if index.Status != domain.IndexStatusBuilding || index.EmbeddingVersionID == nil {
				return conflict(domain.ErrorCodeProjectionInvalid, errors.New("vector page requires a building hybrid index"))
			}
			embedding, queryErr := gormRetrievalLoadEmbedding(callbackCtx, transaction, *index.EmbeddingVersionID)
			if gormRetrievalNoRows(queryErr) {
				return notFound("RETRIEVAL_EMBEDDING_NOT_FOUND", queryErr)
			}
			if queryErr != nil {
				return classifyGORMRetrieval(callbackCtx, queryErr, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
			}

			candidates := make([]domain.VectorBuildInput, 0, command.Limit+1)
			rowsErr := withGORMRetrievalRows(callbackCtx, transaction,
				"RETRIEVAL_VECTOR_PAGE_QUERY_FAILED", "RETRIEVAL_VECTOR_PAGE_SCAN_FAILED",
				`SELECT manifest.chunk_id::text,manifest.content_hash,manifest.sequence,
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
					  ON cache.workspace_id=manifest.workspace_id AND cache.embedding_version_id=?
					 AND cache.content_hash=manifest.content_hash
					WHERE manifest.workspace_id=? AND manifest.index_version_id=?
					  AND projection.embedding_version_id=? AND projection.lexical_status='ready' AND projection.vector_status='pending'
					ORDER BY manifest.sequence,manifest.chunk_id LIMIT ?`,
				[]any{string(embedding.ID), string(command.WorkspaceID), string(command.IndexVersionID), string(embedding.ID), command.Limit + 1},
				func(rows *sql.Rows) error {
					for rows.Next() {
						var input domain.VectorBuildInput
						var declaredBytes int64
						var cached gormRetrievalNullableVector
						if err := rows.Scan(&input.ChunkID, &input.ContentHash, &input.Sequence, &input.TokenCount,
							&input.AtomicOversized, &declaredBytes, &input.InputBytes, &cached); err != nil {
							return err
						}
						if declaredBytes != input.InputBytes {
							return consistency("RETRIEVAL_VECTOR_INPUT_BYTES_INVALID", errors.New("canonical chunk byte count differs from content"))
						}
						if cached.valid {
							input.CachedEmbedding = append([]float32(nil), cached.values...)
						}
						candidates = append(candidates, input)
					}
					return nil
				})
			if rowsErr != nil {
				return rowsErr
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
				contents := make(map[foundation.ID]string, len(contentIDs))
				rowsErr = withGORMRetrievalRows(callbackCtx, transaction,
					"RETRIEVAL_VECTOR_CONTENT_QUERY_FAILED", "RETRIEVAL_VECTOR_CONTENT_SCAN_FAILED",
					`SELECT id::text,content FROM ingestion.canonical_chunk
						WHERE workspace_id=? AND id=ANY(?::uuid[]) ORDER BY id`,
					[]any{string(command.WorkspaceID), pq.Array(contentIDs)},
					func(rows *sql.Rows) error {
						for rows.Next() {
							var chunkID foundation.ID
							var content string
							if err := rows.Scan(&chunkID, &content); err != nil {
								return err
							}
							contents[chunkID] = content
						}
						return nil
					})
				if rowsErr != nil {
					return rowsErr
				}
				if len(contents) != len(contentIDs) {
					return consistency("RETRIEVAL_VECTOR_CONTENT_INVALID", errors.New("vector input content readback is incomplete"))
				}
				for position := range inputs {
					if content, ok := contents[inputs[position].ChunkID]; ok {
						if int64(len(content)) != inputs[position].InputBytes {
							return consistency("RETRIEVAL_VECTOR_INPUT_BYTES_INVALID", errors.New("canonical chunk content size changed"))
						}
						inputs[position].Content = content
					}
				}
			}

			page = domain.VectorBuildPage{Index: index, EmbeddingVersion: embedding, Inputs: inputs, HasMore: hasMore}
			return domain.ValidateVectorBuildPage(command, page)
		})
	if err != nil {
		return domain.VectorBuildPage{}, err
	}
	return page, nil
}

// CommitVectorBuildBatch writes cache and projection facts in one transaction.
func (repository *GORMRepository) CommitVectorBuildBatch(ctx context.Context, batch domain.VectorBuildBatch) (result domain.VectorBuildCommitResult, err error) {
	err = repository.within(ctx, foundation.TransactionOptions{}, "RETRIEVAL_VECTOR_BUILD_TRANSACTION_FAILED", "RETRIEVAL_VECTOR_BUILD_COMMIT_FAILED",
		func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
			index, queryErr := gormRetrievalLoadIndex(callbackCtx, transaction, batch.WorkspaceID, batch.IndexVersionID, true)
			if gormRetrievalNoRows(queryErr) {
				return notFound("RETRIEVAL_INDEX_NOT_FOUND", queryErr)
			}
			if queryErr != nil {
				return classifyGORMRetrieval(callbackCtx, queryErr, "RETRIEVAL_INDEX_QUERY_FAILED")
			}
			embedding, queryErr := gormRetrievalLoadEmbedding(callbackCtx, transaction, batch.EmbeddingVersionID)
			if gormRetrievalNoRows(queryErr) {
				return notFound("RETRIEVAL_EMBEDDING_NOT_FOUND", queryErr)
			}
			if queryErr != nil {
				return classifyGORMRetrieval(callbackCtx, queryErr, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
			}
			if validateErr := domain.ValidateVectorBuildBatch(index, embedding, batch); validateErr != nil {
				return validateErr
			}
			if targetErr := gormRetrievalValidateVectorBuildTargets(callbackCtx, transaction, batch); targetErr != nil {
				return targetErr
			}
			if cacheErr := gormRetrievalCommitEmbeddingCache(callbackCtx, transaction, batch); cacheErr != nil {
				return cacheErr
			}
			projections := make([]domain.VectorProjectionWrite, len(batch.Writes))
			for position, write := range batch.Writes {
				projections[position] = domain.VectorProjectionWrite{
					ChunkID: write.ChunkID, Embedding: append([]float32(nil), write.Embedding...),
					TokenCount: write.TokenCount, VectorStatus: write.VectorStatus, FailureCode: write.FailureCode,
				}
			}
			projectionResult, saveErr := gormRetrievalSaveVectorProjectionBatch(callbackCtx, transaction, domain.VectorProjectionBatch{
				WorkspaceID: batch.WorkspaceID, IndexVersionID: batch.IndexVersionID,
				EmbeddingVersionID: batch.EmbeddingVersionID, ExpectedIndexVersion: batch.ExpectedIndexVersion,
				Projections: projections, At: batch.At,
			})
			if saveErr != nil {
				return saveErr
			}
			result = domain.VectorBuildCommitResult{
				IndexVersionID: projectionResult.IndexVersionID, AttemptedCount: projectionResult.AttemptedCount,
				InsertedCount: projectionResult.InsertedCount, ReplayedCount: projectionResult.ReplayedCount,
			}
			return nil
		})
	if err != nil {
		return domain.VectorBuildCommitResult{}, err
	}
	return result, nil
}

func gormRetrievalVectorBuildBatchFromProjectionBatch(ctx context.Context, transaction *gorm.DB, batch domain.VectorProjectionBatch) (domain.VectorBuildBatch, error) {
	chunkIDs := make([]string, len(batch.Projections))
	for position, write := range batch.Projections {
		chunkIDs[position] = string(write.ChunkID)
	}
	hashes := make(map[foundation.ID]string, len(batch.Projections))
	err := withGORMRetrievalRows(ctx, transaction,
		"RETRIEVAL_VECTOR_MANIFEST_QUERY_FAILED", "RETRIEVAL_VECTOR_MANIFEST_SCAN_FAILED",
		`SELECT chunk_id::text,content_hash FROM retrieval.index_manifest_chunk
			WHERE index_version_id=? AND workspace_id=? AND chunk_id=ANY(?::uuid[]) ORDER BY chunk_id`,
		[]any{string(batch.IndexVersionID), string(batch.WorkspaceID), pq.Array(chunkIDs)},
		func(rows *sql.Rows) error {
			for rows.Next() {
				var chunkID foundation.ID
				var contentHash string
				if err := rows.Scan(&chunkID, &contentHash); err != nil {
					return err
				}
				hashes[chunkID] = contentHash
			}
			return nil
		})
	if err != nil {
		return domain.VectorBuildBatch{}, err
	}
	if len(hashes) != len(batch.Projections) {
		return domain.VectorBuildBatch{}, notFound("RETRIEVAL_VECTOR_TARGET_NOT_FOUND", errors.New("vector batch does not exactly cover manifest chunks"))
	}
	writes := make([]domain.VectorBuildWrite, len(batch.Projections))
	for position, projection := range batch.Projections {
		writes[position] = domain.VectorBuildWrite{
			ChunkID: projection.ChunkID, ContentHash: hashes[projection.ChunkID],
			Embedding: append([]float32(nil), projection.Embedding...), TokenCount: projection.TokenCount,
			VectorStatus: projection.VectorStatus, FailureCode: projection.FailureCode,
		}
	}
	return domain.VectorBuildBatch{
		WorkspaceID: batch.WorkspaceID, IndexVersionID: batch.IndexVersionID,
		EmbeddingVersionID: batch.EmbeddingVersionID, ExpectedIndexVersion: batch.ExpectedIndexVersion,
		Writes: writes, At: batch.At,
	}, nil
}

func gormRetrievalSaveVectorProjectionBatch(ctx context.Context, transaction *gorm.DB, batch domain.VectorProjectionBatch) (domain.ProjectionBatchResult, error) {
	type existingVector struct {
		status  string
		vector  []float32
		token   int32
		failure *string
	}
	chunkIDs := make([]string, len(batch.Projections))
	for position, write := range batch.Projections {
		chunkIDs[position] = string(write.ChunkID)
	}
	existing := make(map[foundation.ID]existingVector, len(batch.Projections))
	err := withGORMRetrievalRows(ctx, transaction,
		"RETRIEVAL_PROJECTION_QUERY_FAILED", "RETRIEVAL_PROJECTION_QUERY_FAILED",
		`SELECT chunk_id::text,lexical_status,vector_status,embedding,token_count,failure_code
			FROM retrieval.chunk_projection
			WHERE index_version_id=? AND workspace_id=? AND chunk_id=ANY(?::uuid[]) ORDER BY chunk_id FOR UPDATE`,
		[]any{string(batch.IndexVersionID), string(batch.WorkspaceID), pq.Array(chunkIDs)},
		func(rows *sql.Rows) error {
			for rows.Next() {
				var id foundation.ID
				var lexical, status string
				var vector gormRetrievalNullableVector
				var token int32
				var failure *string
				if err := rows.Scan(&id, &lexical, &status, &vector, &token, &failure); err != nil {
					return err
				}
				if lexical != "ready" {
					return conflict("RETRIEVAL_LEXICAL_NOT_READY", errors.New("vector write requires lexical-ready projection"))
				}
				if status == string(domain.VectorStatusReady) && !vector.valid {
					return consistency("RETRIEVAL_PROJECTION_DATA_INVALID", errors.New("ready vector projection has no vector"))
				}
				existing[id] = existingVector{status: status, vector: append([]float32(nil), vector.values...), token: token, failure: failure}
			}
			return nil
		})
	if err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	if len(existing) != len(batch.Projections) {
		return domain.ProjectionBatchResult{}, notFound("RETRIEVAL_PROJECTION_NOT_FOUND", errors.New("vector batch does not exactly match persisted projections"))
	}

	var inserted, replayed int64
	updateChunkIDs := make([]string, 0, len(batch.Projections))
	updateEmbeddings := make([]string, 0, len(batch.Projections))
	updateStatuses := make([]string, 0, len(batch.Projections))
	updateFailures := make([]string, 0, len(batch.Projections))
	for _, write := range batch.Projections {
		current := existing[write.ChunkID]
		if current.token != write.TokenCount {
			return domain.ProjectionBatchResult{}, conflict("RETRIEVAL_VECTOR_TOKEN_COUNT_CONFLICT", errors.New("vector token count differs from deterministic lexical token count"))
		}
		if gormRetrievalVectorWriteEqual(current.status, current.vector, current.token, current.failure, write) {
			replayed++
			continue
		}
		if current.status != string(domain.VectorStatusPending) {
			return domain.ProjectionBatchResult{}, conflict("RETRIEVAL_VECTOR_REPLAY_CONFLICT", errors.New("vector projection already has a different terminal result"))
		}
		embedding := ""
		if write.VectorStatus == domain.VectorStatusReady {
			embedding = pgvector.NewVector(write.Embedding).String()
		}
		updateChunkIDs = append(updateChunkIDs, string(write.ChunkID))
		updateEmbeddings = append(updateEmbeddings, embedding)
		updateStatuses = append(updateStatuses, string(write.VectorStatus))
		updateFailures = append(updateFailures, write.FailureCode)
		inserted++
	}
	if inserted > 0 {
		row, err := gormRetrievalRawRow(ctx, transaction, `WITH input AS (
				SELECT * FROM unnest(?::uuid[],?::text[],?::text[],?::text[])
					AS value(chunk_id,embedding_text,vector_status,failure_code)
			), updated AS (
				UPDATE retrieval.chunk_projection projection
				SET embedding=CASE WHEN input.vector_status='ready' THEN input.embedding_text::vector ELSE NULL END,
					vector_status=input.vector_status,failure_code=NULLIF(input.failure_code,''),updated_at=?
				FROM input
				WHERE projection.index_version_id=? AND projection.workspace_id=?
				  AND projection.chunk_id=input.chunk_id AND projection.lexical_status='ready'
				  AND projection.vector_status='pending'
				RETURNING projection.chunk_id
			) SELECT count(*) FROM updated`,
			pq.Array(updateChunkIDs), pq.Array(updateEmbeddings), pq.Array(updateStatuses), pq.Array(updateFailures),
			batch.At.UTC(), string(batch.IndexVersionID), string(batch.WorkspaceID))
		if err != nil {
			return domain.ProjectionBatchResult{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_VECTOR_UPDATE_FAILED")
		}
		var updated int64
		if err := row.Scan(&updated); err != nil {
			return domain.ProjectionBatchResult{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_VECTOR_UPDATE_FAILED")
		}
		if updated != inserted {
			return domain.ProjectionBatchResult{}, conflict("RETRIEVAL_VECTOR_VERSION_CONFLICT", errors.New("projection changed during vector write"))
		}
	}
	return domain.ProjectionBatchResult{
		IndexVersionID: batch.IndexVersionID, AttemptedCount: int64(len(batch.Projections)),
		InsertedCount: inserted, ReplayedCount: replayed,
	}, nil
}

func gormRetrievalValidateVectorBuildTargets(ctx context.Context, transaction *gorm.DB, batch domain.VectorBuildBatch) error {
	chunkIDs := make([]string, len(batch.Writes))
	writes := make(map[foundation.ID]domain.VectorBuildWrite, len(batch.Writes))
	for position, write := range batch.Writes {
		chunkIDs[position] = string(write.ChunkID)
		writes[write.ChunkID] = write
	}
	count := 0
	err := withGORMRetrievalRows(ctx, transaction,
		"RETRIEVAL_VECTOR_TARGET_QUERY_FAILED", "RETRIEVAL_VECTOR_TARGET_SCAN_FAILED",
		`SELECT projection.chunk_id::text,manifest.content_hash,projection.token_count
			FROM retrieval.chunk_projection projection
			JOIN retrieval.index_manifest_chunk manifest
			  ON manifest.index_version_id=projection.index_version_id AND manifest.chunk_id=projection.chunk_id
			JOIN ingestion.canonical_chunk chunk
			  ON chunk.id=manifest.chunk_id AND chunk.workspace_id=manifest.workspace_id
			 AND chunk.content_hash=manifest.content_hash AND chunk.sequence=manifest.sequence
			 AND chunk.parser_version=manifest.parser_version
			 AND chunk.chunk_strategy_version=manifest.chunk_strategy_version AND chunk.schema_version=manifest.schema_version
			WHERE projection.index_version_id=? AND projection.workspace_id=?
			  AND projection.embedding_version_id=? AND projection.chunk_id=ANY(?::uuid[])
			ORDER BY projection.chunk_id FOR UPDATE OF projection`,
		[]any{string(batch.IndexVersionID), string(batch.WorkspaceID), string(batch.EmbeddingVersionID), pq.Array(chunkIDs)},
		func(rows *sql.Rows) error {
			for rows.Next() {
				var chunkID foundation.ID
				var contentHash string
				var tokenCount int32
				if err := rows.Scan(&chunkID, &contentHash, &tokenCount); err != nil {
					return err
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
			return nil
		})
	if err != nil {
		return err
	}
	if count != len(batch.Writes) {
		return notFound("RETRIEVAL_VECTOR_TARGET_NOT_FOUND", errors.New("vector batch does not exactly cover persisted targets"))
	}
	return nil
}

func gormRetrievalCommitEmbeddingCache(ctx context.Context, transaction *gorm.DB, batch domain.VectorBuildBatch) error {
	ready := make(map[string][]float32)
	for _, write := range batch.Writes {
		if write.VectorStatus == domain.VectorStatusReady {
			ready[write.ContentHash] = append([]float32(nil), write.Embedding...)
		}
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
	for position, hash := range hashes {
		vectors[position] = pgvector.NewVector(ready[hash]).String()
	}
	if _, err := gormRetrievalExec(ctx, transaction, `INSERT INTO retrieval.embedding_cache(
			workspace_id,embedding_version_id,content_hash,embedding,created_at)
			SELECT ?,?,input.content_hash,input.embedding_text::vector,?
			FROM unnest(?::text[],?::text[]) AS input(content_hash,embedding_text)
			ON CONFLICT DO NOTHING`, string(batch.WorkspaceID), string(batch.EmbeddingVersionID), batch.At.UTC(), pq.Array(hashes), pq.Array(vectors)); err != nil {
		return classifyGORMRetrieval(ctx, err, "RETRIEVAL_EMBEDDING_CACHE_INSERT_FAILED")
	}
	seen := 0
	err := withGORMRetrievalRows(ctx, transaction,
		"RETRIEVAL_EMBEDDING_CACHE_QUERY_FAILED", "RETRIEVAL_EMBEDDING_CACHE_SCAN_FAILED",
		`SELECT content_hash,embedding FROM retrieval.embedding_cache
			WHERE workspace_id=? AND embedding_version_id=? AND content_hash=ANY(?::text[]) ORDER BY content_hash`,
		[]any{string(batch.WorkspaceID), string(batch.EmbeddingVersionID), pq.Array(hashes)},
		func(rows *sql.Rows) error {
			for rows.Next() {
				var hash string
				var vector pgvector.Vector
				if err := rows.Scan(&hash, &vector); err != nil {
					return err
				}
				if expected, ok := ready[hash]; !ok || !slices.Equal(expected, vector.Slice()) {
					return consistency("RETRIEVAL_EMBEDDING_CACHE_REPLAY_CONFLICT", errors.New("embedding cache contains a different vector"))
				}
				seen++
			}
			return nil
		})
	if err != nil {
		return err
	}
	if seen != len(hashes) {
		return consistency("RETRIEVAL_EMBEDDING_CACHE_REPLAY_CONFLICT", errors.New("embedding cache readback is incomplete"))
	}
	return nil
}

func gormRetrievalVectorWriteEqual(status string, current []float32, token int32, failure *string, write domain.VectorProjectionWrite) bool {
	failureEqual := failure == nil && write.FailureCode == "" || failure != nil && *failure == write.FailureCode
	return status == string(write.VectorStatus) && token == write.TokenCount && slices.Equal(current, write.Embedding) && failureEqual
}
