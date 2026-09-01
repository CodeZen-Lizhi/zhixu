package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"gorm.io/gorm"
)

var _ application.RegressionStore = (*GORMRepository)(nil)

// RunSnapshotRegression observes and, on structural failure, fences the index
// in one writable Repeatable Read transaction.
func (repository *GORMRepository) RunSnapshotRegression(ctx context.Context, command domain.SnapshotRegressionCommand) (result domain.SnapshotRegressionResult, err error) {
	if readyErr := repository.ready(ctx); readyErr != nil {
		return domain.SnapshotRegressionResult{}, readyErr
	}
	if !domain.IsSnapshotRegressionCode(command.RegressionCode) {
		return domain.SnapshotRegressionResult{}, domain.SnapshotRegressionFailureFor(command.RegressionCode, "regression code is unsupported")
	}

	var postCommitErr error
	commitCode := "RETRIEVAL_REGRESSION_COMMIT_FAILED"
	err = repository.withinWithCommitCode(ctx, foundation.TransactionOptions{
		Isolation: foundation.TransactionIsolationRepeatableRead,
		ReadOnly:  false,
	}, "RETRIEVAL_REGRESSION_TRANSACTION_FAILED", func() string { return commitCode },
		func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
			index, queryErr := gormRetrievalLoadIndex(callbackCtx, transaction, command.WorkspaceID, command.IndexVersionID, true)
			if gormRetrievalNoRows(queryErr) {
				return domain.SnapshotRegressionFailureFor(command.RegressionCode, "regression index does not exist in workspace")
			}
			if queryErr != nil {
				return classifyGORMRetrieval(callbackCtx, queryErr, "RETRIEVAL_REGRESSION_INDEX_QUERY_FAILED")
			}
			if index.Status == domain.IndexStatusFailed && index.FailureCode == gormRetrievalSnapshotRegressionFailureCode(command.RegressionCode) {
				postCommitErr = domain.SnapshotRegressionFailureFor(command.RegressionCode, "snapshot regression previously failed")
				return nil
			}

			observation, structuralReason, observationErr := gormRetrievalLoadSnapshotRegressionObservation(callbackCtx, transaction, command, index)
			if observationErr != nil {
				return observationErr
			}
			if structuralReason != "" {
				if failErr := gormRetrievalMarkSnapshotRegressionFailed(callbackCtx, transaction, command.RegressionCode, index); failErr != nil {
					return failErr
				}
				commitCode = "RETRIEVAL_REGRESSION_FAILURE_COMMIT_FAILED"
				postCommitErr = domain.SnapshotRegressionFailureFor(command.RegressionCode, structuralReason)
				return nil
			}
			proof, validationErr := domain.ValidateSnapshotRegression(command, observation)
			if validationErr != nil {
				if failErr := gormRetrievalMarkSnapshotRegressionFailed(callbackCtx, transaction, command.RegressionCode, index); failErr != nil {
					return failErr
				}
				commitCode = "RETRIEVAL_REGRESSION_FAILURE_COMMIT_FAILED"
				postCommitErr = domain.SnapshotRegressionFailureFor(command.RegressionCode, "snapshot regression domain validation failed")
				return nil
			}
			row, rowErr := gormRetrievalRawRow(callbackCtx, transaction, `SELECT CURRENT_TIMESTAMP`)
			if rowErr != nil {
				return classifyGORMRetrieval(callbackCtx, rowErr, "RETRIEVAL_REGRESSION_TIME_QUERY_FAILED")
			}
			var passedAt time.Time
			if scanErr := row.Scan(&passedAt); scanErr != nil {
				return classifyGORMRetrieval(callbackCtx, scanErr, "RETRIEVAL_REGRESSION_TIME_QUERY_FAILED")
			}
			result = domain.SnapshotRegressionResult{Code: proof.Code, Hash: proof.Hash, PassedAt: passedAt.UTC()}
			return nil
		})
	if err != nil {
		return domain.SnapshotRegressionResult{}, err
	}
	if postCommitErr != nil {
		return domain.SnapshotRegressionResult{}, postCommitErr
	}
	return result, nil
}

func gormRetrievalLoadSnapshotRegressionObservation(
	ctx context.Context,
	transaction *gorm.DB,
	command domain.SnapshotRegressionCommand,
	index domain.IndexVersion,
) (domain.SnapshotRegressionObservation, string, error) {
	row, err := gormRetrievalRawRow(ctx, transaction, `SELECT workspace_id::text,source_version_id::text,
		parse_projection_id::text,index_version_id::text,status
		FROM retrieval.reindex_delivery WHERE id=? FOR SHARE`, string(command.DeliveryID))
	if err != nil {
		return domain.SnapshotRegressionObservation{}, "", classifyGORMRetrieval(ctx, err, "RETRIEVAL_REGRESSION_DELIVERY_QUERY_FAILED")
	}
	var deliveryWorkspace foundation.ID
	var deliverySourceVersion, deliveryProjection, deliveryIndex *string
	var deliveryStatus string
	if err := row.Scan(&deliveryWorkspace, &deliverySourceVersion, &deliveryProjection, &deliveryIndex, &deliveryStatus); err != nil {
		if gormRetrievalNoRows(err) {
			return domain.SnapshotRegressionObservation{}, "delivery binding is missing", nil
		}
		return domain.SnapshotRegressionObservation{}, "", classifyGORMRetrieval(ctx, err, "RETRIEVAL_REGRESSION_DELIVERY_QUERY_FAILED")
	}
	if deliveryWorkspace != command.WorkspaceID || gormRetrievalOptionalRegressionID(deliverySourceVersion) != command.TargetSourceVersionID ||
		gormRetrievalOptionalRegressionID(deliveryProjection) != command.TargetParseProjectionID ||
		gormRetrievalOptionalRegressionID(deliveryIndex) != command.IndexVersionID || deliveryStatus != "processing" {
		return domain.SnapshotRegressionObservation{}, "delivery checkpoint binding is inconsistent", nil
	}

	row, err = gormRetrievalRawRow(ctx, transaction, `SELECT source_id::text,content_hash
		FROM core.source_version WHERE id=?`, string(command.TargetSourceVersionID))
	if err != nil {
		return domain.SnapshotRegressionObservation{}, "", classifyGORMRetrieval(ctx, err, "RETRIEVAL_REGRESSION_TARGET_QUERY_FAILED")
	}
	var targetSource foundation.ID
	var targetHash string
	if err := row.Scan(&targetSource, &targetHash); err != nil {
		if gormRetrievalNoRows(err) {
			return domain.SnapshotRegressionObservation{}, "target source version is missing", nil
		}
		return domain.SnapshotRegressionObservation{}, "", classifyGORMRetrieval(ctx, err, "RETRIEVAL_REGRESSION_TARGET_QUERY_FAILED")
	}
	if targetSource != command.TargetSourceID || targetHash != command.TargetResultHash {
		return domain.SnapshotRegressionObservation{}, "target source result binding is inconsistent", nil
	}

	targetManifest, found, err := gormRetrievalLoadTargetManifestSource(ctx, transaction, command)
	if err != nil {
		return domain.SnapshotRegressionObservation{}, "", err
	}
	if !found {
		return domain.SnapshotRegressionObservation{}, "target source is absent from source manifest", nil
	}
	sourceHash, sourceCount, err := gormRetrievalHashRegressionSources(ctx, transaction, command.RegressionCode, command.WorkspaceID, command.IndexVersionID)
	if err != nil {
		if gormRetrievalIsSnapshotRegressionFailure(err) {
			return domain.SnapshotRegressionObservation{}, "source manifest canonical form is invalid", nil
		}
		return domain.SnapshotRegressionObservation{}, "", err
	}
	chunkHash, chunkCount, err := gormRetrievalHashRegressionChunks(ctx, transaction, command.RegressionCode, command.WorkspaceID, command.IndexVersionID)
	if err != nil {
		if gormRetrievalIsSnapshotRegressionFailure(err) {
			return domain.SnapshotRegressionObservation{}, "chunk manifest canonical form is invalid", nil
		}
		return domain.SnapshotRegressionObservation{}, "", err
	}
	expectedUnion, missing, extra, err := gormRetrievalRegressionChunkClosure(ctx, transaction, command.IndexVersionID)
	if err != nil {
		return domain.SnapshotRegressionObservation{}, "", err
	}
	invalidIncluded, err := gormRetrievalRegressionInvalidIncludedSources(ctx, transaction, command.IndexVersionID)
	if err != nil {
		return domain.SnapshotRegressionObservation{}, "", err
	}
	status, err := gormRetrievalRegressionBuildStatus(ctx, transaction, command.WorkspaceID, command.IndexVersionID)
	if err != nil {
		return domain.SnapshotRegressionObservation{}, "", err
	}
	return domain.SnapshotRegressionObservation{
		Index: index, TargetManifestSource: targetManifest, TargetSourceResultHash: targetHash,
		SourceManifestHash: sourceHash, SourceManifestCount: sourceCount,
		ChunkManifestHash: chunkHash, ChunkManifestCount: chunkCount, ExpectedUnionChunkCount: expectedUnion,
		MissingChunkCount: missing, ExtraChunkCount: extra,
		InvalidIncludedSourceCount: invalidIncluded, BuildStatus: status,
	}, "", nil
}

func gormRetrievalLoadTargetManifestSource(ctx context.Context, transaction *gorm.DB, command domain.SnapshotRegressionCommand) (domain.ManifestSource, bool, error) {
	row, err := gormRetrievalRawRow(ctx, transaction, `SELECT index_version_id::text,workspace_id::text,source_id::text,
		source_version_id::text,parse_projection_id::text,selection_status,exclusion_code,created_at
		FROM retrieval.index_manifest_source WHERE index_version_id=? AND source_id=?`,
		string(command.IndexVersionID), string(command.TargetSourceID))
	if err != nil {
		return domain.ManifestSource{}, false, classifyGORMRetrieval(ctx, err, "RETRIEVAL_REGRESSION_TARGET_MANIFEST_QUERY_FAILED")
	}
	var source domain.ManifestSource
	var version, projection, exclusion *string
	if err := row.Scan(&source.IndexVersionID, &source.WorkspaceID, &source.SourceID, &version, &projection,
		&source.SelectionStatus, &exclusion, &source.CreatedAt); err != nil {
		if gormRetrievalNoRows(err) {
			return domain.ManifestSource{}, false, nil
		}
		return domain.ManifestSource{}, false, classifyGORMRetrieval(ctx, err, "RETRIEVAL_REGRESSION_TARGET_MANIFEST_QUERY_FAILED")
	}
	source.SourceVersionID = gormRetrievalOptionalRegressionID(version)
	source.ParseProjectionID = gormRetrievalOptionalRegressionID(projection)
	if exclusion != nil {
		source.ExclusionCode = domain.SourceExclusionCode(*exclusion)
	}
	source.CreatedAt = source.CreatedAt.UTC()
	return source, true, nil
}

func gormRetrievalHashRegressionSources(ctx context.Context, transaction *gorm.DB, regressionCode string, workspaceID, indexID foundation.ID) (string, int64, error) {
	hasher, err := domain.NewSourceManifestHasher(workspaceID, indexID)
	if err != nil {
		return "", 0, err
	}
	err = withGORMRetrievalRows(ctx, transaction,
		"RETRIEVAL_REGRESSION_SOURCE_MANIFEST_QUERY_FAILED", "RETRIEVAL_REGRESSION_SOURCE_MANIFEST_SCAN_FAILED",
		`SELECT index_version_id::text,workspace_id::text,source_id::text,source_version_id::text,
			parse_projection_id::text,selection_status,exclusion_code,created_at
			FROM retrieval.index_manifest_source WHERE index_version_id=? ORDER BY source_id`,
		[]any{string(indexID)}, func(rows *sql.Rows) error {
			for rows.Next() {
				var source domain.ManifestSource
				var version, projection, exclusion *string
				if err := rows.Scan(&source.IndexVersionID, &source.WorkspaceID, &source.SourceID, &version, &projection,
					&source.SelectionStatus, &exclusion, &source.CreatedAt); err != nil {
					return err
				}
				source.SourceVersionID = gormRetrievalOptionalRegressionID(version)
				source.ParseProjectionID = gormRetrievalOptionalRegressionID(projection)
				if exclusion != nil {
					source.ExclusionCode = domain.SourceExclusionCode(*exclusion)
				}
				source.CreatedAt = source.CreatedAt.UTC()
				if err := hasher.Add(source); err != nil {
					return domain.SnapshotRegressionFailureFor(regressionCode, "source manifest canonical form is invalid")
				}
			}
			return nil
		})
	if err != nil {
		return "", 0, err
	}
	hashValue, count, _, err := hasher.Result()
	if err != nil {
		return "", 0, domain.SnapshotRegressionFailureFor(regressionCode, "source manifest is empty")
	}
	return hashValue, count, nil
}

func gormRetrievalHashRegressionChunks(ctx context.Context, transaction *gorm.DB, regressionCode string, workspaceID, indexID foundation.ID) (string, int64, error) {
	hasher, err := domain.NewManifestHasher(workspaceID, indexID)
	if err != nil {
		return "", 0, err
	}
	err = withGORMRetrievalRows(ctx, transaction,
		"RETRIEVAL_REGRESSION_CHUNK_MANIFEST_QUERY_FAILED", "RETRIEVAL_REGRESSION_CHUNK_MANIFEST_SCAN_FAILED",
		`SELECT index_version_id::text,chunk_id::text,workspace_id::text,content_hash,sequence,
			parser_version,chunk_strategy_version,schema_version,created_at
			FROM retrieval.index_manifest_chunk WHERE index_version_id=? ORDER BY sequence,chunk_id`,
		[]any{string(indexID)}, func(rows *sql.Rows) error {
			for rows.Next() {
				var chunk domain.ManifestChunk
				if err := rows.Scan(&chunk.IndexVersionID, &chunk.ChunkID, &chunk.WorkspaceID, &chunk.ContentHash,
					&chunk.Sequence, &chunk.ParserVersion, &chunk.ChunkStrategyVersion, &chunk.SchemaVersion, &chunk.CreatedAt); err != nil {
					return err
				}
				chunk.CreatedAt = chunk.CreatedAt.UTC()
				if err := hasher.Add(chunk); err != nil {
					return domain.SnapshotRegressionFailureFor(regressionCode, "chunk manifest canonical form is invalid")
				}
			}
			return nil
		})
	if err != nil {
		return "", 0, err
	}
	hashValue, count, err := hasher.Result()
	return hashValue, count, err
}

func gormRetrievalRegressionChunkClosure(ctx context.Context, transaction *gorm.DB, indexID foundation.ID) (int64, int64, int64, error) {
	row, err := gormRetrievalRawRow(ctx, transaction, `WITH expected AS (
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
			WHERE index_version.id=?
		), missing AS (
			SELECT count(*) AS value FROM expected
			LEFT JOIN retrieval.index_manifest_chunk manifest
			  ON manifest.index_version_id=? AND manifest.chunk_id=expected.id AND manifest.workspace_id=expected.workspace_id
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
			WHERE manifest.index_version_id=? AND expected.id IS NULL
		)
		SELECT (SELECT count(*) FROM expected),missing.value,extra.value FROM missing CROSS JOIN extra`,
		string(indexID), string(indexID), string(indexID))
	if err != nil {
		return 0, 0, 0, classifyGORMRetrieval(ctx, err, "RETRIEVAL_REGRESSION_CHUNK_CLOSURE_QUERY_FAILED")
	}
	var expected, missing, extra int64
	if err := row.Scan(&expected, &missing, &extra); err != nil {
		return 0, 0, 0, classifyGORMRetrieval(ctx, err, "RETRIEVAL_REGRESSION_CHUNK_CLOSURE_QUERY_FAILED")
	}
	return expected, missing, extra, nil
}

func gormRetrievalRegressionInvalidIncludedSources(ctx context.Context, transaction *gorm.DB, indexID foundation.ID) (int64, error) {
	row, err := gormRetrievalRawRow(ctx, transaction, `SELECT count(*)
		FROM retrieval.index_manifest_source source_manifest
		JOIN retrieval.index_version index_version ON index_version.id=source_manifest.index_version_id
		WHERE source_manifest.index_version_id=? AND source_manifest.selection_status='included'
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
		  )`, string(indexID))
	if err != nil {
		return 0, classifyGORMRetrieval(ctx, err, "RETRIEVAL_REGRESSION_SOURCE_EVIDENCE_QUERY_FAILED")
	}
	var invalid int64
	if err := row.Scan(&invalid); err != nil {
		return 0, classifyGORMRetrieval(ctx, err, "RETRIEVAL_REGRESSION_SOURCE_EVIDENCE_QUERY_FAILED")
	}
	return invalid, nil
}

func gormRetrievalRegressionBuildStatus(ctx context.Context, transaction *gorm.DB, workspaceID, indexID foundation.ID) (domain.BuildStatus, error) {
	row, err := gormRetrievalRawRow(ctx, transaction, gormRetrievalBuildStatusSQL, string(workspaceID), string(indexID))
	if err != nil {
		return domain.BuildStatus{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_REGRESSION_BUILD_STATUS_QUERY_FAILED")
	}
	status, err := scanGORMRetrievalBuildStatus(row, "RETRIEVAL_REGRESSION_DEGRADED_CAPABILITIES_INVALID")
	if err != nil {
		return domain.BuildStatus{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_REGRESSION_BUILD_STATUS_QUERY_FAILED")
	}
	return status, nil
}

func gormRetrievalMarkSnapshotRegressionFailed(ctx context.Context, transaction *gorm.DB, regressionCode string, index domain.IndexVersion) error {
	if index.Status != domain.IndexStatusBuilding {
		return nil
	}
	updated, err := gormRetrievalExec(ctx, transaction, `UPDATE retrieval.index_version
		SET status='failed',failure_code=?,version=version+1,
		updated_at=GREATEST(CURRENT_TIMESTAMP,updated_at,created_at),
		failed_at=GREATEST(CURRENT_TIMESTAMP,updated_at,created_at)
		WHERE id=? AND workspace_id=? AND status='building' AND version=?`,
		gormRetrievalSnapshotRegressionFailureCode(regressionCode), string(index.ID), string(index.WorkspaceID), index.Version)
	if err != nil {
		return classifyGORMRetrieval(ctx, err, "RETRIEVAL_REGRESSION_FAIL_INDEX_FAILED")
	}
	if updated != 1 {
		return consistency("RETRIEVAL_REGRESSION_INDEX_FENCE_LOST", errors.New("building index changed during regression"))
	}
	return nil
}

func gormRetrievalSnapshotRegressionFailureCode(regressionCode string) string {
	if regressionCode == domain.SnapshotStructureRegressionV2 {
		return domain.ErrorCodeSnapshotStructureV2RegressionFailed
	}
	return domain.ErrorCodeSnapshotStructureRegressionFailed
}

func gormRetrievalOptionalRegressionID(value *string) foundation.ID {
	if value == nil {
		return ""
	}
	return foundation.ID(*value)
}

func gormRetrievalIsSnapshotRegressionFailure(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) &&
		(classified.Code == domain.ErrorCodeSnapshotStructureRegressionFailed ||
			classified.Code == domain.ErrorCodeSnapshotStructureV2RegressionFailed)
}
