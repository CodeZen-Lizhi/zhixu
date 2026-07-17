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
		(id,provider,adapter_name,adapter_version,model,dimensions,normalization,distance_metric,config_hash,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT DO NOTHING RETURNING `+embeddingColumns,
		string(value.ID), value.Provider, value.AdapterName, value.AdapterVersion, value.Model, value.Dimensions,
		string(value.Normalization), string(value.DistanceMetric), value.ConfigHash, value.CreatedAt.UTC())
	persisted, err := scanEmbedding(row)
	if err == nil {
		return domain.EmbeddingVersionResult{EmbeddingVersion: persisted, Created: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.EmbeddingVersionResult{}, classify(err, "RETRIEVAL_EMBEDDING_CREATE_FAILED")
	}
	persisted, err = scanEmbedding(r.db.QueryRow(ctx, `SELECT `+embeddingColumns+` FROM retrieval.embedding_version
		WHERE provider=$1 AND model=$2 AND dimensions=$3 AND config_hash=$4`, value.Provider, value.Model, value.Dimensions, value.ConfigHash))
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
		(id,workspace_id,embedding_version_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,failure_code,
		version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT(workspace_id,idempotency_key) DO NOTHING RETURNING id::text`, string(build.IndexVersion.ID),
		string(build.IndexVersion.WorkspaceID), optionalID(build.IndexVersion.EmbeddingVersionID), build.IndexVersion.TokenizerID,
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
	updates := &pgx.Batch{}
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
		var vector any
		if write.VectorStatus == domain.VectorStatusReady {
			vector = pgvector.NewVector(write.Embedding)
		}
		updates.Queue(`UPDATE retrieval.chunk_projection SET embedding=$1,vector_status=$2,failure_code=$3,updated_at=$4
			WHERE index_version_id=$5 AND chunk_id=$6 AND workspace_id=$7 AND lexical_status='ready' AND vector_status='pending'`, vector, string(write.VectorStatus), nullableString(write.FailureCode), batch.At.UTC(), string(batch.IndexVersionID), string(write.ChunkID), string(batch.WorkspaceID))
		inserted++
	}
	if inserted > 0 {
		results := tx.SendBatch(ctx, updates)
		for i := int64(0); i < inserted; i++ {
			tag, err := results.Exec()
			if err != nil {
				_ = results.Close()
				return domain.ProjectionBatchResult{}, classify(err, "RETRIEVAL_VECTOR_UPDATE_FAILED")
			}
			if tag.RowsAffected() != 1 {
				_ = results.Close()
				return domain.ProjectionBatchResult{}, conflict("RETRIEVAL_VECTOR_VERSION_CONFLICT", errors.New("projection changed during vector write"))
			}
		}
		if err := results.Close(); err != nil {
			return domain.ProjectionBatchResult{}, classify(err, "RETRIEVAL_VECTOR_UPDATE_FAILED")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ProjectionBatchResult{}, classify(err, "RETRIEVAL_VECTOR_COMMIT_FAILED")
	}
	return domain.ProjectionBatchResult{IndexVersionID: index.ID, AttemptedCount: int64(len(batch.Projections)), InsertedCount: inserted, ReplayedCount: replayed}, nil
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
	if existing, found, queryErr := getActivationByKey(ctx, tx, command.WorkspaceID, command.IdempotencyKey); queryErr != nil {
		return domain.ActivationResult{}, queryErr
	} else if found {
		expectedTargetNext := command.ExpectedTargetVersion + 1
		var expectedPreviousNext *int64
		if command.ExpectedCurrentVersion != nil {
			v := *command.ExpectedCurrentVersion + 1
			expectedPreviousNext = &v
		}
		if existing.Kind != kind || existing.TargetIndexVersionID != command.TargetIndexVersionID ||
			!sameOptionalID(existing.PreviousIndexVersionID, command.ExpectedCurrentIndexVersionID) ||
			existing.ReasonCode != command.ReasonCode || existing.TargetVersion != expectedTargetNext ||
			!sameOptionalInt64(existing.PreviousVersion, expectedPreviousNext) {
			return domain.ActivationResult{}, conflict("RETRIEVAL_ACTIVATION_IDEMPOTENCY_CONFLICT", errors.New("activation idempotency key has a different binding"))
		}
		activeCurrent, err := getIndexTx(ctx, tx, command.WorkspaceID, existing.TargetIndexVersionID)
		if err != nil {
			return domain.ActivationResult{}, err
		}
		active := activationIndexSnapshot(activeCurrent, domain.IndexStatusActive, existing.TargetVersion, existing.CreatedAt)
		var previous *domain.IndexVersion
		if existing.PreviousIndexVersionID != nil {
			currentPrevious, e := getIndexTx(ctx, tx, command.WorkspaceID, *existing.PreviousIndexVersionID)
			if e != nil {
				return domain.ActivationResult{}, e
			}
			v := activationIndexSnapshot(currentPrevious, domain.IndexStatusRetiring, *existing.PreviousVersion, existing.CreatedAt)
			previous = &v
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.ActivationResult{}, classify(err, "RETRIEVAL_ACTIVATION_COMMIT_FAILED")
		}
		return domain.ActivationResult{Activation: existing, ActiveIndexVersion: active, PreviousIndexVersion: previous, Replayed: true}, nil
	}
	target, err := getIndexForUpdate(ctx, tx, command.WorkspaceID, command.TargetIndexVersionID)
	if err != nil {
		return domain.ActivationResult{}, err
	}
	var current *domain.IndexVersion
	active, activeErr := scanIndex(tx.QueryRow(ctx, `SELECT `+indexColumns+` FROM retrieval.index_version WHERE workspace_id=$1 AND status='active' FOR UPDATE`, string(command.WorkspaceID)))
	if activeErr == nil {
		current = &active
	} else if !errors.Is(activeErr, pgx.ErrNoRows) {
		return domain.ActivationResult{}, classify(activeErr, "RETRIEVAL_ACTIVE_QUERY_FAILED")
	}
	if rollback {
		if current == nil {
			return domain.ActivationResult{}, notFound("RETRIEVAL_ACTIVE_NOT_FOUND", pgx.ErrNoRows)
		}
		rc := domain.RollbackActivationCommand{ActivationID: command.ActivationID, WorkspaceID: command.WorkspaceID, TargetIndexVersionID: command.TargetIndexVersionID, ExpectedTargetVersion: command.ExpectedTargetVersion, ExpectedCurrentIndexVersionID: *command.ExpectedCurrentIndexVersionID, ExpectedCurrentVersion: *command.ExpectedCurrentVersion, IdempotencyKey: command.IdempotencyKey, ReasonCode: command.ReasonCode, At: command.At}
		if err := domain.ValidateRollbackActivationCommand(*current, target, rc); err != nil {
			return domain.ActivationResult{}, err
		}
	} else if err := domain.ValidateActivationCommand(current, target, command); err != nil {
		return domain.ActivationResult{}, err
	}
	activation := domain.Activation{
		ID: command.ActivationID, Kind: kind, WorkspaceID: command.WorkspaceID,
		TargetIndexVersionID: command.TargetIndexVersionID, PreviousIndexVersionID: command.ExpectedCurrentIndexVersionID,
		TargetVersion: command.ExpectedTargetVersion + 1, IdempotencyKey: command.IdempotencyKey,
		ReasonCode: command.ReasonCode, CreatedAt: command.At,
	}
	var previousNext any
	if command.ExpectedCurrentVersion != nil {
		value := *command.ExpectedCurrentVersion + 1
		activation.PreviousVersion = &value
		previousNext = value
	}
	if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_activation(id,kind,workspace_id,target_index_version_id,previous_index_version_id,target_version,previous_version,idempotency_key,reason_code,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, string(command.ActivationID), string(kind), string(command.WorkspaceID), string(command.TargetIndexVersionID), optionalID(command.ExpectedCurrentIndexVersionID), command.ExpectedTargetVersion+1, previousNext, command.IdempotencyKey, command.ReasonCode, command.At.UTC()); err != nil {
		return domain.ActivationResult{}, classify(err, "RETRIEVAL_ACTIVATION_CREATE_FAILED")
	}
	var previous *domain.IndexVersion
	if current != nil {
		updated, e := scanIndex(tx.QueryRow(ctx, `UPDATE retrieval.index_version SET status='retiring',version=version+1,updated_at=$1,retired_at=$1 WHERE id=$2 AND workspace_id=$3 AND status='active' AND version=$4 RETURNING `+indexColumns, command.At.UTC(), string(current.ID), string(command.WorkspaceID), current.Version))
		if e != nil {
			return domain.ActivationResult{}, classify(e, "RETRIEVAL_PREVIOUS_RETIRE_FAILED")
		}
		previous = &updated
	}
	updatedTarget, err := scanIndex(tx.QueryRow(ctx, `UPDATE retrieval.index_version SET status='active',version=version+1,updated_at=$1,activated_at=$1 WHERE id=$2 AND workspace_id=$3 AND status=$4 AND version=$5 RETURNING `+indexColumns, command.At.UTC(), string(target.ID), string(command.WorkspaceID), string(target.Status), target.Version))
	if err != nil {
		return domain.ActivationResult{}, classify(err, "RETRIEVAL_TARGET_ACTIVATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ActivationResult{}, classify(err, "RETRIEVAL_ACTIVATION_COMMIT_FAILED")
	}
	return domain.ActivationResult{Activation: activation, ActiveIndexVersion: updatedTarget, PreviousIndexVersion: previous}, nil
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
	err := r.db.QueryRow(ctx, `SELECT i.workspace_id::text,i.id::text,i.status,i.version,i.expected_chunk_count,i.degraded_capabilities,
		(SELECT count(*) FROM retrieval.index_manifest_chunk m WHERE m.index_version_id=i.id),count(p.index_version_id),
		count(*) FILTER(WHERE p.lexical_status='pending'),count(*) FILTER(WHERE p.lexical_status='ready'),count(*) FILTER(WHERE p.lexical_status='failed'),
		count(*) FILTER(WHERE p.vector_status='disabled'),count(*) FILTER(WHERE p.vector_status='pending'),count(*) FILTER(WHERE p.vector_status='ready'),
		count(*) FILTER(WHERE p.vector_status='skipped_oversized'),count(*) FILTER(WHERE p.vector_status='failed')
		FROM retrieval.index_version i LEFT JOIN retrieval.chunk_projection p ON p.index_version_id=i.id
		WHERE i.workspace_id=$1 AND i.id=$2 GROUP BY i.id`, string(workspaceID), string(indexID)).Scan(&s.WorkspaceID, &s.IndexVersionID, &s.IndexStatus, &s.IndexVersion, &s.ExpectedChunkCount, &degraded, &s.ManifestChunkCount, &s.ProjectionCount, &s.LexicalPendingCount, &s.LexicalReadyCount, &s.LexicalFailedCount, &s.VectorDisabledCount, &s.VectorPendingCount, &s.VectorReadyCount, &s.VectorSkippedOversizedCount, &s.VectorFailedCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BuildStatus{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", err)
	}
	if err != nil {
		return domain.BuildStatus{}, classify(err, "RETRIEVAL_BUILD_STATUS_QUERY_FAILED")
	}
	if err := json.Unmarshal(degraded, &s.DegradedCapabilities); err != nil {
		return domain.BuildStatus{}, consistency("RETRIEVAL_DEGRADED_CAPABILITIES_INVALID", err)
	}
	return s, nil
}
