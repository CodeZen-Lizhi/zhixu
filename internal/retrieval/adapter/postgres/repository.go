// Package postgres 实现 Retrieval 索引事实源的 PostgreSQL Adapter。
package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

// DB 是 Repository 所需的最小 pgx 事务边界。
type DB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

// Repository 持久化不可变 Index Manifest、Projection 与 Activation。
type Repository struct{ db DB }

var _ application.Store = (*Repository)(nil)

// NewRepository 创建 Retrieval PostgreSQL Repository。
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, dependency("RETRIEVAL_DATABASE_UNAVAILABLE", errors.New("database is nil"))
	}
	return &Repository{db: db}, nil
}

// RegisterEmbeddingVersion 精确幂等注册不可变 Embedding Version。
func (r *Repository) RegisterEmbeddingVersion(ctx context.Context, value domain.EmbeddingVersion) (domain.EmbeddingVersionResult, error) {
	if err := domain.ValidateEmbeddingVersion(value); err != nil {
		return domain.EmbeddingVersionResult{}, err
	}
	byID, idErr := scanEmbedding(r.db.QueryRow(ctx, `SELECT `+embeddingColumns+` FROM retrieval.embedding_version WHERE id=$1`, string(value.ID)))
	if idErr == nil {
		if !domain.SameEmbeddingBinding(byID, value) {
			return domain.EmbeddingVersionResult{}, consistency("RETRIEVAL_EMBEDDING_IDENTITY_CONFLICT", errors.New("embedding id is bound to a different contract"))
		}
		return domain.EmbeddingVersionResult{EmbeddingVersion: byID, Replayed: true}, nil
	}
	if !errors.Is(idErr, pgx.ErrNoRows) {
		return domain.EmbeddingVersionResult{}, classify(idErr, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
	}
	row := r.db.QueryRow(ctx, `INSERT INTO retrieval.embedding_version
		(id,provider,adapter_name,adapter_version,model,dimensions,normalization,distance_metric,config_hash,model_settings_revision,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT DO NOTHING RETURNING `+embeddingColumns,
		string(value.ID), value.Provider, value.AdapterName, value.AdapterVersion, value.Model, value.Dimensions,
		string(value.Normalization), string(value.DistanceMetric), value.ConfigHash, nullableInt64(value.ModelSettingsRevision), value.CreatedAt.UTC())
	persisted, err := scanEmbedding(row)
	if err == nil {
		return domain.EmbeddingVersionResult{EmbeddingVersion: persisted, Created: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.EmbeddingVersionResult{}, classify(err, "RETRIEVAL_EMBEDDING_CREATE_FAILED")
	}
	persisted, err = scanEmbedding(r.db.QueryRow(ctx, `SELECT `+embeddingColumns+` FROM retrieval.embedding_version
		WHERE provider=$1 AND model=$2 AND dimensions=$3 AND config_hash=$4
		  AND model_settings_revision IS NOT DISTINCT FROM $5`,
		value.Provider, value.Model, value.Dimensions, value.ConfigHash, nullableInt64(value.ModelSettingsRevision)))
	if errors.Is(err, pgx.ErrNoRows) {
		byID, queryErr := scanEmbedding(r.db.QueryRow(ctx, `SELECT `+embeddingColumns+` FROM retrieval.embedding_version WHERE id=$1`, string(value.ID)))
		if queryErr != nil {
			return domain.EmbeddingVersionResult{}, classify(queryErr, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
		}
		if !domain.SameEmbeddingBinding(byID, value) {
			return domain.EmbeddingVersionResult{}, consistency("RETRIEVAL_EMBEDDING_IDENTITY_CONFLICT", errors.New("embedding id is bound to a different contract"))
		}
		return domain.EmbeddingVersionResult{EmbeddingVersion: byID, Replayed: true}, nil
	}
	if err != nil {
		return domain.EmbeddingVersionResult{}, classify(err, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
	}
	if !domain.SameEmbeddingBinding(persisted, value) {
		return domain.EmbeddingVersionResult{}, conflict("RETRIEVAL_EMBEDDING_IDEMPOTENCY_CONFLICT", errors.New("embedding identity is bound to different adapter settings"))
	}
	if persisted.ID != value.ID {
		byID, idErr = scanEmbedding(r.db.QueryRow(ctx, `SELECT `+embeddingColumns+` FROM retrieval.embedding_version WHERE id=$1`, string(value.ID)))
		if idErr == nil {
			if !domain.SameEmbeddingBinding(byID, value) {
				return domain.EmbeddingVersionResult{}, consistency("RETRIEVAL_EMBEDDING_IDENTITY_CONFLICT", errors.New("embedding id is bound to a different contract"))
			}
			return domain.EmbeddingVersionResult{EmbeddingVersion: byID, Replayed: true}, nil
		}
		if !errors.Is(idErr, pgx.ErrNoRows) {
			return domain.EmbeddingVersionResult{}, classify(idErr, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
		}
	}
	return domain.EmbeddingVersionResult{EmbeddingVersion: persisted, Replayed: true}, nil
}

// GetEmbeddingVersion 返回一个不可变 Embedding Version。
func (r *Repository) GetEmbeddingVersion(ctx context.Context, id foundation.ID) (domain.EmbeddingVersion, error) {
	value, err := scanEmbedding(r.db.QueryRow(ctx, `SELECT `+embeddingColumns+` FROM retrieval.embedding_version WHERE id=$1`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EmbeddingVersion{}, notFound("RETRIEVAL_EMBEDDING_NOT_FOUND", err)
	}
	if err != nil {
		return domain.EmbeddingVersion{}, classify(err, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
	}
	return value, nil
}

// BeginIndex 在一个事务内创建 Building Index 并冻结完整 Manifest。
func (r *Repository) BeginIndex(ctx context.Context, build domain.IndexBuild) (domain.IndexVersionResult, error) {
	canonical, hash, err := domain.CanonicalizeManifest(build.IndexVersion.WorkspaceID, build.IndexVersion.ID, build.Manifest)
	if err != nil {
		return domain.IndexVersionResult{}, err
	}
	if hash != build.IndexVersion.ManifestHash || int64(len(canonical)) != build.IndexVersion.ExpectedChunkCount {
		return domain.IndexVersionResult{}, consistency("RETRIEVAL_MANIFEST_HASH_MISMATCH", errors.New("manifest hash or count differs from index binding"))
	}
	build.Manifest = canonical
	if err := domain.ValidateIndexBuild(build); err != nil {
		return domain.IndexVersionResult{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.IndexVersionResult{}, classify(err, "RETRIEVAL_INDEX_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	degraded := marshalDegradedCapabilities(build.IndexVersion.DegradedCapabilities)
	var inserted string
	err = tx.QueryRow(ctx, `INSERT INTO retrieval.index_version
		(id,workspace_id,embedding_version_id,model_settings_revision,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,failure_code,
		version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT(workspace_id,idempotency_key) DO NOTHING RETURNING id::text`, string(build.IndexVersion.ID),
		string(build.IndexVersion.WorkspaceID), optionalID(build.IndexVersion.EmbeddingVersionID), nullableInt64(build.IndexVersion.ModelSettingsRevision), build.IndexVersion.TokenizerID,
		build.IndexVersion.TokenizerVersion, build.IndexVersion.TokenizerConfigHash, build.IndexVersion.FusionConfig,
		build.IndexVersion.SourceSnapshotRef, build.IndexVersion.ManifestHash, build.IndexVersion.ExpectedChunkCount,
		build.IndexVersion.IdempotencyKey, string(build.IndexVersion.Status), degraded, nullableString(build.IndexVersion.FailureCode),
		build.IndexVersion.Version, build.IndexVersion.CreatedAt.UTC(), build.IndexVersion.UpdatedAt.UTC()).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, manifest, queryErr := getIndexBuild(ctx, tx, build.IndexVersion.WorkspaceID, build.IndexVersion.IdempotencyKey)
		if queryErr != nil {
			return domain.IndexVersionResult{}, classify(queryErr, "RETRIEVAL_INDEX_REPLAY_QUERY_FAILED")
		}
		if !domain.SameIndexBuildBinding(domain.IndexBuild{IndexVersion: existing, Manifest: manifest}, build) || !sameManifest(manifest, canonical) {
			return domain.IndexVersionResult{}, conflict("RETRIEVAL_INDEX_IDEMPOTENCY_CONFLICT", errors.New("index idempotency key is bound to a different build"))
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.IndexVersionResult{}, classify(err, "RETRIEVAL_INDEX_COMMIT_FAILED")
		}
		return domain.IndexVersionResult{IndexVersion: existing, Manifest: manifest, Replayed: true}, nil
	}
	if err != nil {
		return domain.IndexVersionResult{}, classify(err, "RETRIEVAL_INDEX_CREATE_FAILED")
	}
	if len(canonical) > 0 {
		copied, err := tx.CopyFrom(ctx,
			pgx.Identifier{"retrieval", "index_manifest_chunk"},
			[]string{"index_version_id", "chunk_id", "workspace_id", "content_hash", "sequence", "parser_version", "chunk_strategy_version", "schema_version", "created_at"},
			pgx.CopyFromSlice(len(canonical), func(index int) ([]any, error) {
				chunk := canonical[index]
				return []any{
					string(chunk.IndexVersionID), string(chunk.ChunkID), string(chunk.WorkspaceID), chunk.ContentHash,
					chunk.Sequence, chunk.ParserVersion, chunk.ChunkStrategyVersion, chunk.SchemaVersion, chunk.CreatedAt.UTC(),
				}, nil
			}),
		)
		if err != nil {
			return domain.IndexVersionResult{}, classify(err, "RETRIEVAL_MANIFEST_CREATE_FAILED")
		}
		if copied != int64(len(canonical)) {
			return domain.IndexVersionResult{}, consistency("RETRIEVAL_MANIFEST_INCOMPLETE", errors.New("manifest copy did not persist every chunk"))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.IndexVersionResult{}, classify(err, "RETRIEVAL_INDEX_COMMIT_FAILED")
	}
	return domain.IndexVersionResult{IndexVersion: build.IndexVersion, Manifest: canonical, Created: true}, nil
}

// BuildLexical 从冻结 Manifest 批量生成 lexical-ready Projection，不接收调用方正文。
func (r *Repository) BuildLexical(ctx context.Context, command domain.LexicalBuildCommand) (domain.ProjectionBatchResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.ProjectionBatchResult{}, classify(err, "RETRIEVAL_LEXICAL_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	index, err := getIndexForUpdate(ctx, tx, command.WorkspaceID, command.IndexVersionID)
	if err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	if err := domain.ValidateLexicalBuildCommand(index, command); err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	tag, err := tx.Exec(ctx, `WITH lexical AS (
		SELECT m.index_version_id,m.chunk_id,m.workspace_id,i.embedding_version_id,
		setweight(to_tsvector('simple',coalesce(c.heading_path::text,'')),'A') || setweight(to_tsvector('simple',c.content),'B') AS search_vector
		FROM retrieval.index_manifest_chunk m JOIN retrieval.index_version i ON i.id=m.index_version_id
		JOIN ingestion.canonical_chunk c ON c.id=m.chunk_id AND c.workspace_id=m.workspace_id
		WHERE m.index_version_id=$1 AND m.workspace_id=$2 AND c.content_hash=m.content_hash
		  AND c.sequence=m.sequence AND c.parser_version=m.parser_version
		  AND c.chunk_strategy_version=m.chunk_strategy_version AND c.schema_version=m.schema_version
		  AND NOT EXISTS(SELECT 1 FROM retrieval.chunk_projection p WHERE p.index_version_id=m.index_version_id AND p.chunk_id=m.chunk_id)
	)
		INSERT INTO retrieval.chunk_projection
		(index_version_id,chunk_id,workspace_id,embedding_version_id,search_vector,embedding,token_count,lexical_status,vector_status,failure_code,created_at,updated_at)
		SELECT index_version_id,chunk_id,workspace_id,embedding_version_id,search_vector,NULL,length(search_vector),
		'ready',CASE WHEN embedding_version_id IS NULL THEN 'disabled' ELSE 'pending' END,NULL,$3,$3 FROM lexical
		ON CONFLICT(index_version_id,chunk_id) DO NOTHING`, string(command.IndexVersionID), string(command.WorkspaceID), command.At.UTC())
	if err != nil {
		return domain.ProjectionBatchResult{}, classify(err, "RETRIEVAL_LEXICAL_BUILD_FAILED")
	}
	var total, exact int64
	err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE p.lexical_status='ready' AND p.search_vector=(
		setweight(to_tsvector('simple',coalesce(c.heading_path::text,'')),'A') || setweight(to_tsvector('simple',c.content),'B'))
		AND p.token_count=length(p.search_vector) AND p.embedding_version_id IS NOT DISTINCT FROM $3
		AND (($3::uuid IS NULL AND p.vector_status='disabled') OR ($3::uuid IS NOT NULL AND p.vector_status IN ('pending','ready','skipped_oversized','failed'))))
		FROM retrieval.chunk_projection p JOIN retrieval.index_manifest_chunk m ON m.index_version_id=p.index_version_id AND m.chunk_id=p.chunk_id
		JOIN ingestion.canonical_chunk c ON c.id=m.chunk_id AND c.workspace_id=m.workspace_id
		WHERE p.index_version_id=$1 AND p.workspace_id=$2`, string(command.IndexVersionID), string(command.WorkspaceID), optionalID(index.EmbeddingVersionID)).Scan(&total, &exact)
	if err != nil {
		return domain.ProjectionBatchResult{}, classify(err, "RETRIEVAL_LEXICAL_VERIFY_FAILED")
	}
	if total != index.ExpectedChunkCount || exact != index.ExpectedChunkCount {
		return domain.ProjectionBatchResult{}, consistency("RETRIEVAL_LEXICAL_INCOMPLETE", errors.New("lexical projection does not exactly cover manifest"))
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ProjectionBatchResult{}, classify(err, "RETRIEVAL_LEXICAL_COMMIT_FAILED")
	}
	inserted := tag.RowsAffected()
	return domain.ProjectionBatchResult{IndexVersionID: index.ID, AttemptedCount: index.ExpectedChunkCount, InsertedCount: inserted, ReplayedCount: index.ExpectedChunkCount - inserted}, nil
}

// SaveVectorBatch 只更新既有 lexical-ready Projection 的向量结果，并保持 search_vector 不变。
func (r *Repository) SaveVectorBatch(ctx context.Context, batch domain.VectorProjectionBatch) (domain.ProjectionBatchResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.ProjectionBatchResult{}, classify(err, "RETRIEVAL_VECTOR_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	index, err := getIndexForUpdate(ctx, tx, batch.WorkspaceID, batch.IndexVersionID)
	if err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	embedding, err := getEmbedding(ctx, tx, batch.EmbeddingVersionID)
	if err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	if err := domain.ValidateVectorProjectionBatch(index, embedding, batch); err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	cacheAwareBatch, err := vectorBuildBatchFromProjectionBatch(ctx, tx, batch)
	if err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	if err := domain.ValidateVectorBuildBatch(index, embedding, cacheAwareBatch); err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	if err := validateVectorBuildTargets(ctx, tx, cacheAwareBatch); err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	if err := commitEmbeddingCache(ctx, tx, cacheAwareBatch); err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	result, err := saveVectorProjectionBatchTx(ctx, tx, batch)
	if err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ProjectionBatchResult{}, classify(err, "RETRIEVAL_VECTOR_COMMIT_FAILED")
	}
	return result, nil
}

func vectorBuildBatchFromProjectionBatch(ctx context.Context, tx pgx.Tx, batch domain.VectorProjectionBatch) (domain.VectorBuildBatch, error) {
	chunkIDs := make([]string, len(batch.Projections))
	for index, write := range batch.Projections {
		chunkIDs[index] = string(write.ChunkID)
	}
	rows, err := tx.Query(ctx, `SELECT chunk_id::text,content_hash FROM retrieval.index_manifest_chunk
		WHERE index_version_id=$1 AND workspace_id=$2 AND chunk_id=ANY($3::uuid[]) ORDER BY chunk_id`,
		string(batch.IndexVersionID), string(batch.WorkspaceID), chunkIDs)
	if err != nil {
		return domain.VectorBuildBatch{}, classify(err, "RETRIEVAL_VECTOR_MANIFEST_QUERY_FAILED")
	}
	defer rows.Close()
	hashes := make(map[foundation.ID]string, len(batch.Projections))
	for rows.Next() {
		var chunkID foundation.ID
		var contentHash string
		if err := rows.Scan(&chunkID, &contentHash); err != nil {
			return domain.VectorBuildBatch{}, classify(err, "RETRIEVAL_VECTOR_MANIFEST_SCAN_FAILED")
		}
		hashes[chunkID] = contentHash
	}
	if err := rows.Err(); err != nil {
		return domain.VectorBuildBatch{}, classify(err, "RETRIEVAL_VECTOR_MANIFEST_SCAN_FAILED")
	}
	if len(hashes) != len(batch.Projections) {
		return domain.VectorBuildBatch{}, notFound("RETRIEVAL_VECTOR_TARGET_NOT_FOUND", errors.New("vector batch does not exactly cover manifest chunks"))
	}
	writes := make([]domain.VectorBuildWrite, len(batch.Projections))
	for index, projection := range batch.Projections {
		writes[index] = domain.VectorBuildWrite{
			ChunkID: projection.ChunkID, ContentHash: hashes[projection.ChunkID], Embedding: append([]float32(nil), projection.Embedding...),
			TokenCount: projection.TokenCount, VectorStatus: projection.VectorStatus, FailureCode: projection.FailureCode,
		}
	}
	return domain.VectorBuildBatch{
		WorkspaceID: batch.WorkspaceID, IndexVersionID: batch.IndexVersionID, EmbeddingVersionID: batch.EmbeddingVersionID,
		ExpectedIndexVersion: batch.ExpectedIndexVersion, Writes: writes, At: batch.At,
	}, nil
}

func saveVectorProjectionBatchTx(ctx context.Context, tx pgx.Tx, batch domain.VectorProjectionBatch) (domain.ProjectionBatchResult, error) {
	chunkIDs := make([]string, len(batch.Projections))
	for i, write := range batch.Projections {
		chunkIDs[i] = string(write.ChunkID)
	}
	rows, err := tx.Query(ctx, `SELECT chunk_id::text,lexical_status,vector_status,embedding,token_count,failure_code FROM retrieval.chunk_projection
		WHERE index_version_id=$1 AND workspace_id=$2 AND chunk_id=ANY($3::uuid[]) ORDER BY chunk_id FOR UPDATE`, string(batch.IndexVersionID), string(batch.WorkspaceID), chunkIDs)
	if err != nil {
		return domain.ProjectionBatchResult{}, classify(err, "RETRIEVAL_PROJECTION_QUERY_FAILED")
	}
	defer rows.Close()
	type existingVector struct {
		status  string
		vector  []float32
		token   int32
		failure *string
	}
	existing := make(map[foundation.ID]existingVector, len(batch.Projections))
	for rows.Next() {
		var id foundation.ID
		var lexical, status string
		var vector *pgvector.Vector
		var token int32
		var failure *string
		if err := rows.Scan(&id, &lexical, &status, &vector, &token, &failure); err != nil {
			return domain.ProjectionBatchResult{}, classify(err, "RETRIEVAL_PROJECTION_QUERY_FAILED")
		}
		if lexical != "ready" {
			return domain.ProjectionBatchResult{}, conflict("RETRIEVAL_LEXICAL_NOT_READY", errors.New("vector write requires lexical-ready projection"))
		}
		var values []float32
		if vector != nil {
			values = vector.Slice()
		}
		existing[id] = existingVector{status: status, vector: values, token: token, failure: failure}
	}
	if err := rows.Err(); err != nil {
		return domain.ProjectionBatchResult{}, classify(err, "RETRIEVAL_PROJECTION_QUERY_FAILED")
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
		if vectorWriteEqual(current.status, current.vector, current.token, current.failure, write) {
			replayed++
			continue
		}
		if current.status != "pending" {
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
		var updated int64
		if err := tx.QueryRow(ctx, `WITH input AS (
				SELECT * FROM unnest($1::uuid[],$2::text[],$3::text[],$4::text[])
					AS value(chunk_id,embedding_text,vector_status,failure_code)
			), updated AS (
				UPDATE retrieval.chunk_projection projection
				SET embedding=CASE WHEN input.vector_status='ready' THEN input.embedding_text::vector ELSE NULL END,
					vector_status=input.vector_status,
					failure_code=NULLIF(input.failure_code,''),
					updated_at=$5
				FROM input
				WHERE projection.index_version_id=$6 AND projection.workspace_id=$7
				  AND projection.chunk_id=input.chunk_id AND projection.lexical_status='ready'
				  AND projection.vector_status='pending'
				RETURNING projection.chunk_id
			)
			SELECT count(*) FROM updated`, updateChunkIDs, updateEmbeddings, updateStatuses, updateFailures,
			batch.At.UTC(), string(batch.IndexVersionID), string(batch.WorkspaceID)).Scan(&updated); err != nil {
			return domain.ProjectionBatchResult{}, classify(err, "RETRIEVAL_VECTOR_UPDATE_FAILED")
		}
		if updated != inserted {
			return domain.ProjectionBatchResult{}, conflict("RETRIEVAL_VECTOR_VERSION_CONFLICT", errors.New("projection changed during vector write"))
		}
	}
	return domain.ProjectionBatchResult{IndexVersionID: batch.IndexVersionID, AttemptedCount: int64(len(batch.Projections)), InsertedCount: inserted, ReplayedCount: replayed}, nil
}

// TransitionIndex 以乐观锁执行不包含进入 Active 的通用状态迁移。
func (r *Repository) TransitionIndex(ctx context.Context, command domain.IndexTransition) (domain.IndexVersion, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.IndexVersion{}, classify(err, "RETRIEVAL_INDEX_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := getIndexForUpdate(ctx, tx, command.WorkspaceID, command.IndexVersionID)
	if err != nil {
		return domain.IndexVersion{}, err
	}
	if err := domain.ValidateIndexTransitionCommand(current, command); err != nil {
		return domain.IndexVersion{}, err
	}
	capabilities := command.DegradedCapabilities
	if capabilities == nil {
		capabilities = current.DegradedCapabilities
	}
	degraded := marshalDegradedCapabilities(capabilities)
	row := tx.QueryRow(ctx, `UPDATE retrieval.index_version SET status=$1,degraded_capabilities=$2,failure_code=$3,version=version+1,updated_at=$4,
		built_at=CASE WHEN $1='ready' THEN $4 ELSE built_at END,failed_at=CASE WHEN $1='failed' THEN $4 ELSE failed_at END,
		retired_at=CASE WHEN $1='retiring' THEN $4 ELSE retired_at END,archived_at=CASE WHEN $1='archived' THEN $4 ELSE archived_at END
		WHERE id=$5 AND workspace_id=$6 AND version=$7 RETURNING `+indexColumns, string(command.Status), degraded, nullableString(command.FailureCode), command.At.UTC(), string(command.IndexVersionID), string(command.WorkspaceID), command.ExpectedVersion)
	updated, err := scanIndex(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.IndexVersion{}, conflict("RETRIEVAL_INDEX_VERSION_CONFLICT", err)
	}
	if err != nil {
		return domain.IndexVersion{}, classify(err, "RETRIEVAL_INDEX_TRANSITION_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.IndexVersion{}, classify(err, "RETRIEVAL_INDEX_COMMIT_FAILED")
	}
	return updated, nil
}

// Activate 原子追加 Activation 并将 Ready Index 切换为唯一 Active。
func (r *Repository) Activate(ctx context.Context, command domain.ActivationCommand) (domain.ActivationResult, error) {
	return r.activate(ctx, command, false)
}

// RollbackActivate 原子退役当前 Active 并恢复指定 Retiring Index。
func (r *Repository) RollbackActivate(ctx context.Context, command domain.RollbackActivationCommand) (domain.ActivationResult, error) {
	converted := domain.ActivationCommand{ActivationID: command.ActivationID, WorkspaceID: command.WorkspaceID, TargetIndexVersionID: command.TargetIndexVersionID,
		ExpectedTargetVersion: command.ExpectedTargetVersion, ExpectedCurrentIndexVersionID: &command.ExpectedCurrentIndexVersionID,
		ExpectedCurrentVersion: &command.ExpectedCurrentVersion, IdempotencyKey: command.IdempotencyKey, ReasonCode: command.ReasonCode, At: command.At}
	return r.activate(ctx, converted, true)
}

func (r *Repository) activate(ctx context.Context, command domain.ActivationCommand, rollback bool) (domain.ActivationResult, error) {
	kind := domain.ActivationKindActivate
	if rollback {
		kind = domain.ActivationKindRollback
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.ActivationResult{}, classify(err, "RETRIEVAL_ACTIVATION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(command.WorkspaceID)); err != nil {
		return domain.ActivationResult{}, classify(err, "RETRIEVAL_ACTIVATION_LOCK_FAILED")
	}
	result, err := activateTx(ctx, tx, command, kind, nil)
	if err != nil {
		return domain.ActivationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ActivationResult{}, classify(err, "RETRIEVAL_ACTIVATION_COMMIT_FAILED")
	}
	return result, nil
}

// GetIndex 按 Workspace 与稳定 ID 返回 Index Version。
func (r *Repository) GetIndex(ctx context.Context, workspaceID, indexID foundation.ID) (domain.IndexVersion, error) {
	value, err := scanIndex(r.db.QueryRow(ctx, `SELECT `+indexColumns+` FROM retrieval.index_version WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(indexID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.IndexVersion{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", err)
	}
	if err != nil {
		return domain.IndexVersion{}, classify(err, "RETRIEVAL_INDEX_QUERY_FAILED")
	}
	return value, nil
}

// GetIndexByIdempotencyKey 按 Workspace 与幂等键恢复 Index Build。
func (r *Repository) GetIndexByIdempotencyKey(ctx context.Context, workspaceID foundation.ID, key string) (domain.IndexVersion, error) {
	value, err := scanIndex(r.db.QueryRow(ctx, `SELECT `+indexColumns+` FROM retrieval.index_version WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), key))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.IndexVersion{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", err)
	}
	if err != nil {
		return domain.IndexVersion{}, classify(err, "RETRIEVAL_INDEX_QUERY_FAILED")
	}
	return value, nil
}

// GetActive 只返回 Workspace 当前唯一 Active Index，不做状态降级回退。
func (r *Repository) GetActive(ctx context.Context, workspaceID foundation.ID) (domain.IndexVersion, error) {
	value, err := scanIndex(r.db.QueryRow(ctx, `SELECT `+indexColumns+` FROM retrieval.index_version WHERE workspace_id=$1 AND status='active'`, string(workspaceID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.IndexVersion{}, notFound("RETRIEVAL_ACTIVE_NOT_FOUND", err)
	}
	if err != nil {
		return domain.IndexVersion{}, classify(err, "RETRIEVAL_ACTIVE_QUERY_FAILED")
	}
	return value, nil
}

// GetBuildStatus 返回 Manifest 与 Projection 状态的单查询聚合快照。
func (r *Repository) GetBuildStatus(ctx context.Context, workspaceID, indexID foundation.ID) (domain.BuildStatus, error) {
	var s domain.BuildStatus
	var degraded []byte
	var sourceManifestHash *string
	var expectedSourceCount *int64
	err := r.db.QueryRow(ctx, `SELECT i.workspace_id::text,i.id::text,i.status,i.version,i.expected_chunk_count,
		i.source_manifest_hash,i.expected_source_count,i.degraded_capabilities,
		(SELECT count(*) FROM retrieval.index_manifest_chunk m WHERE m.index_version_id=i.id),
		(SELECT count(*) FROM retrieval.index_manifest_source s WHERE s.index_version_id=i.id),
		(SELECT count(*) FROM retrieval.index_manifest_source s WHERE s.index_version_id=i.id AND s.selection_status='included'),
		(SELECT count(*) FROM retrieval.index_manifest_source s WHERE s.index_version_id=i.id AND s.selection_status='excluded'),
		count(p.index_version_id),
		count(*) FILTER(WHERE p.lexical_status='pending'),count(*) FILTER(WHERE p.lexical_status='ready'),count(*) FILTER(WHERE p.lexical_status='failed'),
		count(*) FILTER(WHERE p.vector_status='disabled'),count(*) FILTER(WHERE p.vector_status='pending'),count(*) FILTER(WHERE p.vector_status='ready'),
		count(*) FILTER(WHERE p.vector_status='skipped_oversized'),count(*) FILTER(WHERE p.vector_status='failed')
		FROM retrieval.index_version i LEFT JOIN retrieval.chunk_projection p ON p.index_version_id=i.id
		WHERE i.workspace_id=$1 AND i.id=$2 GROUP BY i.id`, string(workspaceID), string(indexID)).Scan(
		&s.WorkspaceID, &s.IndexVersionID, &s.IndexStatus, &s.IndexVersion, &s.ExpectedChunkCount,
		&sourceManifestHash, &expectedSourceCount, &degraded, &s.ManifestChunkCount,
		&s.SourceManifestCount, &s.IncludedSourceCount, &s.ExcludedSourceCount,
		&s.ProjectionCount, &s.LexicalPendingCount, &s.LexicalReadyCount, &s.LexicalFailedCount,
		&s.VectorDisabledCount, &s.VectorPendingCount, &s.VectorReadyCount,
		&s.VectorSkippedOversizedCount, &s.VectorFailedCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BuildStatus{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", err)
	}
	if err != nil {
		return domain.BuildStatus{}, classify(err, "RETRIEVAL_BUILD_STATUS_QUERY_FAILED")
	}
	if sourceManifestHash != nil {
		s.SourceManifestHash = *sourceManifestHash
	}
	s.ExpectedSourceCount = expectedSourceCount
	if err := json.Unmarshal(degraded, &s.DegradedCapabilities); err != nil {
		return domain.BuildStatus{}, consistency("RETRIEVAL_DEGRADED_CAPABILITIES_INVALID", err)
	}
	return s, nil
}
