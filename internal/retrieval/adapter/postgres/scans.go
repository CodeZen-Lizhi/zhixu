package postgres

import (
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const embeddingColumns = `id::text,provider,adapter_name,adapter_version,model,dimensions,normalization,distance_metric,config_hash,model_settings_revision,created_at`

const indexColumns = `id::text,workspace_id::text,embedding_version_id::text,model_settings_revision,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,source_manifest_hash,expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version,idempotency_key,status,degraded_capabilities,failure_code,version,created_at,updated_at`

type retrievalRowScanner interface {
	Scan(...any) error
}

func scanEmbedding(row retrievalRowScanner) (domain.EmbeddingVersion, error) {
	var v domain.EmbeddingVersion
	err := row.Scan(&v.ID, &v.Provider, &v.AdapterName, &v.AdapterVersion, &v.Model, &v.Dimensions, &v.Normalization, &v.DistanceMetric, &v.ConfigHash, &v.ModelSettingsRevision, &v.CreatedAt)
	return v, err
}

func scanIndex(row retrievalRowScanner) (domain.IndexVersion, error) {
	var v domain.IndexVersion
	var embedding *string
	var sourceManifestHash *string
	var parserID, parserVersion, parserConfigHash, chunkStrategyVersion, schemaVersion *string
	var degraded []byte
	var failure *string
	err := row.Scan(&v.ID, &v.WorkspaceID, &embedding, &v.ModelSettingsRevision, &v.TokenizerID, &v.TokenizerVersion, &v.TokenizerConfigHash, &v.FusionConfig, &v.SourceSnapshotRef, &v.ManifestHash, &v.ExpectedChunkCount, &sourceManifestHash, &v.ExpectedSourceCount, &parserID, &parserVersion, &parserConfigHash, &chunkStrategyVersion, &schemaVersion, &v.IdempotencyKey, &v.Status, &degraded, &failure, &v.Version, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	if embedding != nil {
		id := foundation.ID(*embedding)
		v.EmbeddingVersionID = &id
	}
	if sourceManifestHash != nil {
		v.SourceManifestHash = *sourceManifestHash
	}
	if parserID != nil || parserVersion != nil || parserConfigHash != nil || chunkStrategyVersion != nil || schemaVersion != nil {
		v.ProcessingContract = &domain.ProcessingContract{
			ParserID: optionalScannedString(parserID), ParserVersion: optionalScannedString(parserVersion),
			ParserConfigHash: optionalScannedString(parserConfigHash), ChunkStrategyVersion: optionalScannedString(chunkStrategyVersion),
			SchemaVersion: optionalScannedString(schemaVersion),
		}
	}
	if failure != nil {
		v.FailureCode = *failure
	}
	if err = json.Unmarshal(degraded, &v.DegradedCapabilities); err != nil {
		return v, err
	}
	return v, nil
}

func optionalScannedString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
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
