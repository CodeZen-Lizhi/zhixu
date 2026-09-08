package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
)

// manifestDatabase 仅用于同事务写入 Index metadata 与 COPY 冻结 Manifest。
type manifestDatabase interface {
	Begin(context.Context) (pgx.Tx, error)
}

func beginIndexNative(ctx context.Context, database manifestDatabase, build domain.IndexBuild) (domain.IndexVersionResult, error) {
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
	tx, err := database.Begin(ctx)
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

func getIndexBuild(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, key string) (domain.IndexVersion, []domain.ManifestChunk, error) {
	index, err := scanIndex(tx.QueryRow(ctx, `SELECT `+indexColumns+` FROM retrieval.index_version WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(workspaceID), key))
	if err != nil {
		return domain.IndexVersion{}, nil, err
	}
	rows, err := tx.Query(ctx, `SELECT index_version_id::text,chunk_id::text,workspace_id::text,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at FROM retrieval.index_manifest_chunk WHERE index_version_id=$1 ORDER BY sequence,chunk_id`, string(index.ID))
	if err != nil {
		return domain.IndexVersion{}, nil, err
	}
	defer rows.Close()
	var result []domain.ManifestChunk
	for rows.Next() {
		var c domain.ManifestChunk
		if err := rows.Scan(&c.IndexVersionID, &c.ChunkID, &c.WorkspaceID, &c.ContentHash, &c.Sequence, &c.ParserVersion, &c.ChunkStrategyVersion, &c.SchemaVersion, &c.CreatedAt); err != nil {
			return domain.IndexVersion{}, nil, err
		}
		result = append(result, c)
	}
	return index, result, rows.Err()
}
