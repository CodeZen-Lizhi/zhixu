package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
)

var _ application.RegressionStore = (*Repository)(nil)

// RunSnapshotRegression 在单一 repeatable-read 事务中验证 Building Snapshot；结构失败会原子标记 Index failed。
func (r *Repository) RunSnapshotRegression(ctx context.Context, command domain.SnapshotRegressionCommand) (domain.SnapshotRegressionResult, error) {
	if r == nil || r.db == nil {
		return domain.SnapshotRegressionResult{}, dependency("RETRIEVAL_REGRESSION_DATABASE_UNAVAILABLE", errors.New("database is nil"))
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.SnapshotRegressionResult{}, classify(err, "RETRIEVAL_REGRESSION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
		return domain.SnapshotRegressionResult{}, classify(err, "RETRIEVAL_REGRESSION_SNAPSHOT_FAILED")
	}

	index, err := scanIndex(tx.QueryRow(ctx, `SELECT `+indexColumns+` FROM retrieval.index_version
		WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(command.WorkspaceID), string(command.IndexVersionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SnapshotRegressionResult{}, domain.SnapshotRegressionFailure("regression index does not exist in workspace")
	}
	if err != nil {
		return domain.SnapshotRegressionResult{}, classify(err, "RETRIEVAL_REGRESSION_INDEX_QUERY_FAILED")
	}
	if index.Status == domain.IndexStatusFailed && index.FailureCode == domain.ErrorCodeSnapshotStructureRegressionFailed {
		if err := tx.Commit(ctx); err != nil {
			return domain.SnapshotRegressionResult{}, classify(err, "RETRIEVAL_REGRESSION_COMMIT_FAILED")
		}
		return domain.SnapshotRegressionResult{}, domain.SnapshotRegressionFailure("snapshot regression previously failed")
	}

	observation, structuralReason, err := loadSnapshotRegressionObservation(ctx, tx, command, index)
	if err != nil {
		return domain.SnapshotRegressionResult{}, err
	}
	if structuralReason != "" {
		return failSnapshotRegression(ctx, tx, index, structuralReason)
	}
	proof, err := domain.ValidateSnapshotRegression(command, observation)
	if err != nil {
		return failSnapshotRegression(ctx, tx, index, "snapshot regression domain validation failed")
	}
	var passedAt time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&passedAt); err != nil {
		return domain.SnapshotRegressionResult{}, classify(err, "RETRIEVAL_REGRESSION_TIME_QUERY_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SnapshotRegressionResult{}, classify(err, "RETRIEVAL_REGRESSION_COMMIT_FAILED")
	}
	return domain.SnapshotRegressionResult{Code: proof.Code, Hash: proof.Hash, PassedAt: passedAt.UTC()}, nil
}

func loadSnapshotRegressionObservation(
	ctx context.Context,
	tx pgx.Tx,
	command domain.SnapshotRegressionCommand,
	index domain.IndexVersion,
) (domain.SnapshotRegressionObservation, string, error) {
	var deliveryWorkspace foundation.ID
	var deliverySourceVersion, deliveryProjection, deliveryIndex *string
	var deliveryStatus string
	err := tx.QueryRow(ctx, `SELECT workspace_id::text,source_version_id::text,parse_projection_id::text,index_version_id::text,status
		FROM retrieval.reindex_delivery WHERE id=$1 FOR SHARE`, string(command.DeliveryID)).Scan(
		&deliveryWorkspace, &deliverySourceVersion, &deliveryProjection, &deliveryIndex, &deliveryStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SnapshotRegressionObservation{}, "delivery binding is missing", nil
	}
	if err != nil {
		return domain.SnapshotRegressionObservation{}, "", classify(err, "RETRIEVAL_REGRESSION_DELIVERY_QUERY_FAILED")
	}
	if deliveryWorkspace != command.WorkspaceID || optionalRegressionID(deliverySourceVersion) != command.TargetSourceVersionID ||
		optionalRegressionID(deliveryProjection) != command.TargetParseProjectionID || optionalRegressionID(deliveryIndex) != command.IndexVersionID ||
		deliveryStatus != "processing" {
		return domain.SnapshotRegressionObservation{}, "delivery checkpoint binding is inconsistent", nil
	}

	var targetSource foundation.ID
	var targetHash string
	err = tx.QueryRow(ctx, `SELECT source_id::text,content_hash FROM core.source_version WHERE id=$1`,
		string(command.TargetSourceVersionID)).Scan(&targetSource, &targetHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SnapshotRegressionObservation{}, "target source version is missing", nil
	}
	if err != nil {
		return domain.SnapshotRegressionObservation{}, "", classify(err, "RETRIEVAL_REGRESSION_TARGET_QUERY_FAILED")
	}
	if targetSource != command.TargetSourceID || targetHash != command.TargetResultHash {
		return domain.SnapshotRegressionObservation{}, "target source result binding is inconsistent", nil
	}

	targetManifest, found, err := loadTargetManifestSource(ctx, tx, command)
	if err != nil {
		return domain.SnapshotRegressionObservation{}, "", err
	}
	if !found {
		return domain.SnapshotRegressionObservation{}, "target source is absent from source manifest", nil
	}
	sourceHash, sourceCount, err := hashRegressionSources(ctx, tx, command.WorkspaceID, command.IndexVersionID)
	if err != nil {
		if isSnapshotRegressionFailure(err) {
			return domain.SnapshotRegressionObservation{}, "source manifest canonical form is invalid", nil
		}
		return domain.SnapshotRegressionObservation{}, "", err
	}
	chunkHash, chunkCount, err := hashRegressionChunks(ctx, tx, command.WorkspaceID, command.IndexVersionID)
	if err != nil {
		if isSnapshotRegressionFailure(err) {
			return domain.SnapshotRegressionObservation{}, "chunk manifest canonical form is invalid", nil
		}
		return domain.SnapshotRegressionObservation{}, "", err
	}
	expectedUnion, missing, extra, err := regressionChunkClosure(ctx, tx, command.IndexVersionID)
	if err != nil {
		return domain.SnapshotRegressionObservation{}, "", err
	}
	invalidIncluded, err := regressionInvalidIncludedSources(ctx, tx, command.IndexVersionID)
	if err != nil {
		return domain.SnapshotRegressionObservation{}, "", err
	}
	status, err := regressionBuildStatus(ctx, tx, command.WorkspaceID, command.IndexVersionID)
	if err != nil {
		return domain.SnapshotRegressionObservation{}, "", err
	}
	return domain.SnapshotRegressionObservation{
		Index: index, TargetManifestSource: targetManifest, TargetSourceResultHash: targetHash,
		SourceManifestHash: sourceHash, SourceManifestCount: sourceCount,
		ChunkManifestHash: chunkHash, ChunkManifestCount: chunkCount, ExpectedUnionChunkCount: expectedUnion,
		MissingChunkCount: missing, ExtraChunkCount: extra, InvalidIncludedSourceCount: invalidIncluded, BuildStatus: status,
	}, "", nil
}

func loadTargetManifestSource(ctx context.Context, tx pgx.Tx, command domain.SnapshotRegressionCommand) (domain.ManifestSource, bool, error) {
	var source domain.ManifestSource
	var version, projection *string
	var exclusion *string
	err := tx.QueryRow(ctx, `SELECT index_version_id::text,workspace_id::text,source_id::text,source_version_id::text,
		parse_projection_id::text,selection_status,exclusion_code,created_at
		FROM retrieval.index_manifest_source WHERE index_version_id=$1 AND source_id=$2`,
		string(command.IndexVersionID), string(command.TargetSourceID)).Scan(
		&source.IndexVersionID, &source.WorkspaceID, &source.SourceID, &version, &projection,
		&source.SelectionStatus, &exclusion, &source.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ManifestSource{}, false, nil
	}
	if err != nil {
		return domain.ManifestSource{}, false, classify(err, "RETRIEVAL_REGRESSION_TARGET_MANIFEST_QUERY_FAILED")
	}
	source.SourceVersionID = optionalRegressionID(version)
	source.ParseProjectionID = optionalRegressionID(projection)
	if exclusion != nil {
		source.ExclusionCode = domain.SourceExclusionCode(*exclusion)
	}
	return source, true, nil
}

func hashRegressionSources(ctx context.Context, tx pgx.Tx, workspaceID, indexID foundation.ID) (string, int64, error) {
	hasher, err := domain.NewSourceManifestHasher(workspaceID, indexID)
	if err != nil {
		return "", 0, err
	}
	rows, err := tx.Query(ctx, `SELECT index_version_id::text,workspace_id::text,source_id::text,source_version_id::text,
		parse_projection_id::text,selection_status,exclusion_code,created_at
		FROM retrieval.index_manifest_source WHERE index_version_id=$1 ORDER BY source_id`, string(indexID))
	if err != nil {
		return "", 0, classify(err, "RETRIEVAL_REGRESSION_SOURCE_MANIFEST_QUERY_FAILED")
	}
	defer rows.Close()
	for rows.Next() {
		var source domain.ManifestSource
		var version, projection, exclusion *string
		if err := rows.Scan(&source.IndexVersionID, &source.WorkspaceID, &source.SourceID, &version, &projection,
			&source.SelectionStatus, &exclusion, &source.CreatedAt); err != nil {
			return "", 0, classify(err, "RETRIEVAL_REGRESSION_SOURCE_MANIFEST_SCAN_FAILED")
		}
		source.SourceVersionID = optionalRegressionID(version)
		source.ParseProjectionID = optionalRegressionID(projection)
		if exclusion != nil {
			source.ExclusionCode = domain.SourceExclusionCode(*exclusion)
		}
		if err := hasher.Add(source); err != nil {
			return "", 0, domain.SnapshotRegressionFailure("source manifest canonical form is invalid")
		}
	}
	if err := rows.Err(); err != nil {
		return "", 0, classify(err, "RETRIEVAL_REGRESSION_SOURCE_MANIFEST_SCAN_FAILED")
	}
	hashValue, count, _, err := hasher.Result()
	if err != nil {
		return "", 0, domain.SnapshotRegressionFailure("source manifest is empty")
	}
	return hashValue, count, nil
}

func hashRegressionChunks(ctx context.Context, tx pgx.Tx, workspaceID, indexID foundation.ID) (string, int64, error) {
	hasher, err := domain.NewManifestHasher(workspaceID, indexID)
	if err != nil {
		return "", 0, err
	}
	rows, err := tx.Query(ctx, `SELECT index_version_id::text,chunk_id::text,workspace_id::text,content_hash,sequence,
		parser_version,chunk_strategy_version,schema_version,created_at
		FROM retrieval.index_manifest_chunk WHERE index_version_id=$1 ORDER BY sequence,chunk_id`, string(indexID))
	if err != nil {
		return "", 0, classify(err, "RETRIEVAL_REGRESSION_CHUNK_MANIFEST_QUERY_FAILED")
	}
	defer rows.Close()
	for rows.Next() {
		var chunk domain.ManifestChunk
		if err := rows.Scan(&chunk.IndexVersionID, &chunk.ChunkID, &chunk.WorkspaceID, &chunk.ContentHash,
			&chunk.Sequence, &chunk.ParserVersion, &chunk.ChunkStrategyVersion, &chunk.SchemaVersion, &chunk.CreatedAt); err != nil {
			return "", 0, classify(err, "RETRIEVAL_REGRESSION_CHUNK_MANIFEST_SCAN_FAILED")
		}
		if err := hasher.Add(chunk); err != nil {
			return "", 0, domain.SnapshotRegressionFailure("chunk manifest canonical form is invalid")
		}
	}
	if err := rows.Err(); err != nil {
		return "", 0, classify(err, "RETRIEVAL_REGRESSION_CHUNK_MANIFEST_SCAN_FAILED")
	}
	hashValue, count, err := hasher.Result()
	return hashValue, count, err
}

func regressionChunkClosure(ctx context.Context, tx pgx.Tx, indexID foundation.ID) (int64, int64, int64, error) {
	var expected, missing, extra int64
	err := tx.QueryRow(ctx, `WITH expected AS (
		SELECT DISTINCT chunk.id,chunk.workspace_id,chunk.content_hash,chunk.sequence,chunk.parser_version,
			chunk.chunk_strategy_version,chunk.schema_version
		FROM retrieval.index_version index_version
		JOIN retrieval.index_manifest_source source_manifest
		  ON source_manifest.index_version_id=index_version.id AND source_manifest.selection_status='included'
		JOIN ingestion.canonical_chunk chunk
		  ON chunk.parse_projection_id=source_manifest.parse_projection_id
		 AND chunk.workspace_id=source_manifest.workspace_id
		 AND chunk.parser_version=index_version.source_parser_version
		 AND chunk.chunk_strategy_version=index_version.source_chunk_strategy_version
		 AND chunk.schema_version=index_version.source_schema_version
		 AND chunk.status='active'
		WHERE index_version.id=$1
	), missing AS (
		SELECT count(*) AS value FROM expected
		LEFT JOIN retrieval.index_manifest_chunk manifest
		  ON manifest.index_version_id=$1 AND manifest.chunk_id=expected.id AND manifest.workspace_id=expected.workspace_id
		 AND manifest.content_hash=expected.content_hash AND manifest.sequence=expected.sequence
		 AND manifest.parser_version=expected.parser_version
		 AND manifest.chunk_strategy_version=expected.chunk_strategy_version AND manifest.schema_version=expected.schema_version
		WHERE manifest.chunk_id IS NULL
	), extra AS (
		SELECT count(*) AS value FROM retrieval.index_manifest_chunk manifest
		LEFT JOIN expected
		  ON expected.id=manifest.chunk_id AND expected.workspace_id=manifest.workspace_id
		 AND expected.content_hash=manifest.content_hash AND expected.sequence=manifest.sequence
		 AND expected.parser_version=manifest.parser_version
		 AND expected.chunk_strategy_version=manifest.chunk_strategy_version AND expected.schema_version=manifest.schema_version
		WHERE manifest.index_version_id=$1 AND expected.id IS NULL
	)
	SELECT (SELECT count(*) FROM expected),missing.value,extra.value FROM missing CROSS JOIN extra`, string(indexID)).Scan(&expected, &missing, &extra)
	if err != nil {
		return 0, 0, 0, classify(err, "RETRIEVAL_REGRESSION_CHUNK_CLOSURE_QUERY_FAILED")
	}
	return expected, missing, extra, nil
}

func regressionInvalidIncludedSources(ctx context.Context, tx pgx.Tx, indexID foundation.ID) (int64, error) {
	var invalid int64
	err := tx.QueryRow(ctx, `SELECT count(*)
		FROM retrieval.index_manifest_source source_manifest
		JOIN retrieval.index_version index_version ON index_version.id=source_manifest.index_version_id
		WHERE source_manifest.index_version_id=$1 AND source_manifest.selection_status='included'
		  AND NOT EXISTS (
			SELECT 1 FROM ingestion.parse_projection projection
			JOIN ingestion.attempt attempt
			  ON attempt.parse_projection_id=projection.id
			 AND attempt.source_version_id=source_manifest.source_version_id
			 AND attempt.workspace_id=source_manifest.workspace_id
			 AND attempt.status='chunked' AND attempt.security_status='passed'
			 AND attempt.parser_id=index_version.source_parser_id
			 AND attempt.parser_version=index_version.source_parser_version
			 AND attempt.parser_config_hash=index_version.source_parser_config_hash
			 AND attempt.chunk_strategy_version=index_version.source_chunk_strategy_version
			 AND attempt.schema_version=index_version.source_schema_version
			WHERE projection.id=source_manifest.parse_projection_id
			  AND projection.workspace_id=source_manifest.workspace_id
			  AND projection.parser_id=index_version.source_parser_id
			  AND projection.parser_version=index_version.source_parser_version
			  AND projection.parser_config_hash=index_version.source_parser_config_hash
			  AND projection.schema_version=index_version.source_schema_version
		  )`, string(indexID)).Scan(&invalid)
	if err != nil {
		return 0, classify(err, "RETRIEVAL_REGRESSION_SOURCE_EVIDENCE_QUERY_FAILED")
	}
	return invalid, nil
}

func regressionBuildStatus(ctx context.Context, tx pgx.Tx, workspaceID, indexID foundation.ID) (domain.BuildStatus, error) {
	var status domain.BuildStatus
	var degraded []byte
	var sourceManifestHash *string
	var expectedSourceCount *int64
	err := tx.QueryRow(ctx, `SELECT i.workspace_id::text,i.id::text,i.status,i.version,i.expected_chunk_count,
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
		&status.WorkspaceID, &status.IndexVersionID, &status.IndexStatus, &status.IndexVersion, &status.ExpectedChunkCount,
		&sourceManifestHash, &expectedSourceCount, &degraded, &status.ManifestChunkCount,
		&status.SourceManifestCount, &status.IncludedSourceCount, &status.ExcludedSourceCount,
		&status.ProjectionCount, &status.LexicalPendingCount, &status.LexicalReadyCount, &status.LexicalFailedCount,
		&status.VectorDisabledCount, &status.VectorPendingCount, &status.VectorReadyCount,
		&status.VectorSkippedOversizedCount, &status.VectorFailedCount,
	)
	if err != nil {
		return domain.BuildStatus{}, classify(err, "RETRIEVAL_REGRESSION_BUILD_STATUS_QUERY_FAILED")
	}
	if sourceManifestHash != nil {
		status.SourceManifestHash = *sourceManifestHash
	}
	status.ExpectedSourceCount = expectedSourceCount
	if err := json.Unmarshal(degraded, &status.DegradedCapabilities); err != nil {
		return domain.BuildStatus{}, consistency("RETRIEVAL_REGRESSION_DEGRADED_CAPABILITIES_INVALID", err)
	}
	return status, nil
}

func failSnapshotRegression(ctx context.Context, tx pgx.Tx, index domain.IndexVersion, reason string) (domain.SnapshotRegressionResult, error) {
	if index.Status == domain.IndexStatusBuilding {
		tag, err := tx.Exec(ctx, `UPDATE retrieval.index_version SET status='failed',failure_code=$1,version=version+1,
			updated_at=GREATEST(CURRENT_TIMESTAMP,updated_at,created_at),
			failed_at=GREATEST(CURRENT_TIMESTAMP,updated_at,created_at)
			WHERE id=$2 AND workspace_id=$3 AND status='building' AND version=$4`,
			domain.ErrorCodeSnapshotStructureRegressionFailed, string(index.ID), string(index.WorkspaceID), index.Version)
		if err != nil {
			return domain.SnapshotRegressionResult{}, classify(err, "RETRIEVAL_REGRESSION_FAIL_INDEX_FAILED")
		}
		if tag.RowsAffected() != 1 {
			return domain.SnapshotRegressionResult{}, consistency("RETRIEVAL_REGRESSION_INDEX_FENCE_LOST", errors.New("building index changed during regression"))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SnapshotRegressionResult{}, classify(err, "RETRIEVAL_REGRESSION_FAILURE_COMMIT_FAILED")
	}
	return domain.SnapshotRegressionResult{}, domain.SnapshotRegressionFailure(reason)
}

func optionalRegressionID(value *string) foundation.ID {
	if value == nil {
		return ""
	}
	return foundation.ID(*value)
}

func isSnapshotRegressionFailure(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == domain.ErrorCodeSnapshotStructureRegressionFailed
}
