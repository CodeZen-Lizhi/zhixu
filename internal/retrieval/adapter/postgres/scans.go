package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
)

const embeddingColumns = `id::text,provider,adapter_name,adapter_version,model,dimensions,normalization,distance_metric,config_hash,created_at`
const indexColumns = `id::text,workspace_id::text,embedding_version_id::text,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,failure_code,version,created_at,updated_at`

func scanEmbedding(row pgx.Row) (domain.EmbeddingVersion, error) {
	var v domain.EmbeddingVersion
	err := row.Scan(&v.ID, &v.Provider, &v.AdapterName, &v.AdapterVersion, &v.Model, &v.Dimensions, &v.Normalization, &v.DistanceMetric, &v.ConfigHash, &v.CreatedAt)
	return v, err
}

func scanIndex(row pgx.Row) (domain.IndexVersion, error) {
	var v domain.IndexVersion
	var embedding *string
	var degraded []byte
	var failure *string
	err := row.Scan(&v.ID, &v.WorkspaceID, &embedding, &v.TokenizerID, &v.TokenizerVersion, &v.TokenizerConfigHash, &v.FusionConfig, &v.SourceSnapshotRef, &v.ManifestHash, &v.ExpectedChunkCount, &v.IdempotencyKey, &v.Status, &degraded, &failure, &v.Version, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	if embedding != nil {
		id := foundation.ID(*embedding)
		v.EmbeddingVersionID = &id
	}
	if failure != nil {
		v.FailureCode = *failure
	}
	if err = json.Unmarshal(degraded, &v.DegradedCapabilities); err != nil {
		return v, err
	}
	return v, nil
}

func getIndexForUpdate(ctx context.Context, tx pgx.Tx, workspaceID, indexID foundation.ID) (domain.IndexVersion, error) {
	value, err := scanIndex(tx.QueryRow(ctx, `SELECT `+indexColumns+` FROM retrieval.index_version WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(workspaceID), string(indexID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.IndexVersion{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", err)
	}
	if err != nil {
		return domain.IndexVersion{}, classify(err, "RETRIEVAL_INDEX_QUERY_FAILED")
	}
	return value, nil
}
func getIndexTx(ctx context.Context, tx pgx.Tx, workspaceID, indexID foundation.ID) (domain.IndexVersion, error) {
	value, err := scanIndex(tx.QueryRow(ctx, `SELECT `+indexColumns+` FROM retrieval.index_version WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(indexID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.IndexVersion{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", err)
	}
	if err != nil {
		return domain.IndexVersion{}, classify(err, "RETRIEVAL_INDEX_QUERY_FAILED")
	}
	return value, nil
}
func getEmbedding(ctx context.Context, tx pgx.Tx, id foundation.ID) (domain.EmbeddingVersion, error) {
	value, err := scanEmbedding(tx.QueryRow(ctx, `SELECT `+embeddingColumns+` FROM retrieval.embedding_version WHERE id=$1`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EmbeddingVersion{}, notFound("RETRIEVAL_EMBEDDING_NOT_FOUND", err)
	}
	if err != nil {
		return domain.EmbeddingVersion{}, classify(err, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
	}
	return value, nil
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

func sameManifest(left, right []domain.ManifestChunk) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		a, b := left[i], right[i]
		if a.ChunkID != b.ChunkID || a.WorkspaceID != b.WorkspaceID || a.ContentHash != b.ContentHash || a.Sequence != b.Sequence || a.ParserVersion != b.ParserVersion || a.ChunkStrategyVersion != b.ChunkStrategyVersion || a.SchemaVersion != b.SchemaVersion {
			return false
		}
	}
	return true
}

func getActivationByKey(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, key string) (domain.Activation, bool, error) {
	var a domain.Activation
	var previous *string
	err := tx.QueryRow(ctx, `SELECT id::text,kind,workspace_id::text,target_index_version_id::text,previous_index_version_id::text,target_version,previous_version,idempotency_key,reason_code,created_at
		FROM retrieval.index_activation WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), key).Scan(
		&a.ID, &a.Kind, &a.WorkspaceID, &a.TargetIndexVersionID, &previous, &a.TargetVersion, &a.PreviousVersion,
		&a.IdempotencyKey, &a.ReasonCode, &a.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, false, nil
	}
	if err != nil {
		return a, false, classify(err, "RETRIEVAL_ACTIVATION_QUERY_FAILED")
	}
	if previous != nil {
		id := foundation.ID(*previous)
		a.PreviousIndexVersionID = &id
	}
	return a, true, nil
}

func activationIndexSnapshot(current domain.IndexVersion, status domain.IndexStatus, version int64, at time.Time) domain.IndexVersion {
	current.Status = status
	current.Version = version
	current.UpdatedAt = at
	current.FailureCode = ""
	return current
}

func optionalID(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}
func sameOptionalID(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func marshalDegradedCapabilities(capabilities []domain.DegradedCapability) []byte {
	if len(capabilities) == 0 {
		return []byte("[]")
	}
	encoded, _ := json.Marshal(capabilities)
	return encoded
}

func vectorWriteEqual(status string, current []float32, token int32, failure *string, write domain.VectorProjectionWrite) bool {
	return status == string(write.VectorStatus) && token == write.TokenCount && reflect.DeepEqual(current, write.Embedding) && ((failure == nil && write.FailureCode == "") || (failure != nil && *failure == write.FailureCode))
}
