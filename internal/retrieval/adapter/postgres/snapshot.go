package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const snapshotCursorID foundation.ID = "00000000-0000-0000-0000-000000000000"

type snapshotConnectionPool interface {
	Acquire(context.Context) (*pgxpool.Conn, error)
}

// BeginWorkspaceSnapshot 在 repeatable-read 与 Workspace advisory lock 内分页物化完整 Source/Chunk Snapshot。
func (r *Repository) BeginWorkspaceSnapshot(ctx context.Context, command domain.WorkspaceSnapshotCommand) (domain.WorkspaceSnapshotResult, error) {
	if err := domain.ValidateWorkspaceSnapshotCommand(command); err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	pool, ok := r.db.(snapshotConnectionPool)
	if !ok {
		return domain.WorkspaceSnapshotResult{}, dependency("REINDEX_SNAPSHOT_CONNECTION_UNAVAILABLE", errors.New("database does not support dedicated snapshot connections"))
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return domain.WorkspaceSnapshotResult{}, classify(err, "REINDEX_SNAPSHOT_CONNECTION_FAILED")
	}
	locked := false
	defer func() { releaseSnapshotConnection(connection, command.IndexVersion.WorkspaceID, locked) }()
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, string(command.IndexVersion.WorkspaceID)); err != nil {
		return domain.WorkspaceSnapshotResult{}, classify(err, "REINDEX_SNAPSHOT_LOCK_FAILED")
	}
	locked = true
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return domain.WorkspaceSnapshotResult{}, classify(err, "REINDEX_SNAPSHOT_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if replay, found, err := replayWorkspaceSnapshot(ctx, tx, command); err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return domain.WorkspaceSnapshotResult{}, classify(err, "REINDEX_SNAPSHOT_COMMIT_FAILED")
		}
		return replay, nil
	}
	if err := createSnapshotStages(ctx, tx); err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	if err := validateTargetProjection(ctx, tx, command); err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	activeID, incremental, err := snapshotActiveBase(ctx, tx, command.IndexVersion.WorkspaceID, command.IndexVersion.ProcessingContract)
	if err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	if incremental {
		err = stageIncrementalSources(ctx, tx, command, activeID)
	} else {
		err = stageFirstBuildSources(ctx, tx, command)
	}
	if err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	if err := stageTargetSource(ctx, tx, command); err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	sourceHash, sourceCount, excludedCount, err := hashStagedSources(ctx, tx, command)
	if err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	if sourceCount > command.MaxSources {
		return domain.WorkspaceSnapshotResult{}, snapshotCapacityError("source count exceeds configured maximum")
	}
	if err := stageSnapshotChunks(ctx, tx, command); err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	chunkHash, chunkCount, err := hashStagedChunks(ctx, tx, command)
	if err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	if chunkCount > command.MaxChunks {
		return domain.WorkspaceSnapshotResult{}, snapshotCapacityError("chunk count exceeds configured maximum")
	}
	index := command.IndexVersion
	index.ManifestHash = chunkHash
	index.ExpectedChunkCount = chunkCount
	index.SourceManifestHash = sourceHash
	index.ExpectedSourceCount = &sourceCount
	if err := domain.ValidateIndexVersion(index); err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	degradedCapabilities := index.DegradedCapabilities
	if degradedCapabilities == nil {
		degradedCapabilities = []domain.DegradedCapability{}
	}
	degraded, err := json.Marshal(degradedCapabilities)
	if err != nil {
		return domain.WorkspaceSnapshotResult{}, consistency("REINDEX_INDEX_DEGRADATION_INVALID", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_version(
        id,workspace_id,embedding_version_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
        source_snapshot_ref,manifest_hash,expected_chunk_count,source_manifest_hash,expected_source_count,
		source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version,
        idempotency_key,status,degraded_capabilities,failure_code,version,created_at,updated_at
    ) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,'building',$19::jsonb,NULL,1,$20,$20)`,
		string(index.ID), string(index.WorkspaceID), optionalID(index.EmbeddingVersionID), index.TokenizerID, index.TokenizerVersion,
		index.TokenizerConfigHash, index.FusionConfig, index.SourceSnapshotRef, index.ManifestHash,
		index.ExpectedChunkCount, index.SourceManifestHash, sourceCount, index.ProcessingContract.ParserID,
		index.ProcessingContract.ParserVersion, index.ProcessingContract.ParserConfigHash,
		index.ProcessingContract.ChunkStrategyVersion, index.ProcessingContract.SchemaVersion,
		index.IdempotencyKey, degraded, index.CreatedAt.UTC()); err != nil {
		return domain.WorkspaceSnapshotResult{}, classify(err, "REINDEX_INDEX_CREATE_FAILED")
	}
	if err := copyStagedSources(ctx, tx, command); err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	if err := copyStagedChunks(ctx, tx, command); err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.WorkspaceSnapshotResult{}, classify(err, "REINDEX_SNAPSHOT_COMMIT_FAILED")
	}
	return domain.WorkspaceSnapshotResult{
		IndexVersion: index, SourceCount: sourceCount, ChunkCount: chunkCount,
		ExcludedSourceCount: excludedCount, Created: true,
	}, nil
}

func releaseSnapshotConnection(connection *pgxpool.Conn, workspaceID foundation.ID, locked bool) {
	if connection == nil {
		return
	}
	if !locked {
		connection.Release()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var unlocked bool
	if err := connection.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, string(workspaceID)).Scan(&unlocked); err == nil && unlocked {
		connection.Release()
		return
	}
	raw := connection.Hijack()
	_ = raw.Close(context.Background())
}

func createSnapshotStages(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE reindex_source_stage(
        source_id uuid PRIMARY KEY,workspace_id uuid NOT NULL,source_version_id uuid,parse_projection_id uuid,
        selection_status text NOT NULL,exclusion_code text,created_at timestamptz NOT NULL
    ) ON COMMIT DROP`); err != nil {
		return classify(err, "REINDEX_SNAPSHOT_STAGE_CREATE_FAILED")
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE reindex_chunk_stage(
        chunk_id uuid PRIMARY KEY,workspace_id uuid NOT NULL,content_hash text NOT NULL,sequence integer NOT NULL,
        parser_version text NOT NULL,chunk_strategy_version text NOT NULL,schema_version text NOT NULL,created_at timestamptz NOT NULL
    ) ON COMMIT DROP`); err != nil {
		return classify(err, "REINDEX_SNAPSHOT_STAGE_CREATE_FAILED")
	}
	return nil
}

func validateTargetProjection(ctx context.Context, tx pgx.Tx, command domain.WorkspaceSnapshotCommand) error {
	contract := *command.IndexVersion.ProcessingContract
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
        SELECT 1
          FROM core.source source
          JOIN core.source_version version ON version.source_id=source.id
          JOIN ingestion.source_version_projection mapping
            ON mapping.source_version_id=version.id AND mapping.workspace_id=source.workspace_id
          JOIN ingestion.attempt attempt
            ON attempt.source_version_id=version.id AND attempt.parse_projection_id=mapping.parse_projection_id
         WHERE source.id=$1 AND source.workspace_id=$2 AND version.id=$3 AND mapping.parse_projection_id=$4
           AND attempt.status='chunked' AND attempt.security_status='passed'
           AND attempt.parser_id=$5 AND attempt.parser_version=$6 AND attempt.parser_config_hash=$7
           AND attempt.chunk_strategy_version=$8 AND attempt.schema_version=$9
    )`, string(command.TargetSourceID), string(command.IndexVersion.WorkspaceID), string(command.TargetSourceVersionID),
		string(command.TargetParseProjectionID), contract.ParserID, contract.ParserVersion, contract.ParserConfigHash,
		contract.ChunkStrategyVersion, contract.SchemaVersion).Scan(&valid)
	if err != nil {
		return classify(err, "REINDEX_TARGET_PROJECTION_QUERY_FAILED")
	}
	if !valid {
		return conflict("REINDEX_TARGET_PROJECTION_INVALID", errors.New("target source version has no successful current projection"))
	}
	return nil
}

func snapshotActiveBase(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, requestedContract *domain.ProcessingContract) (foundation.ID, bool, error) {
	var id foundation.ID
	var sourceHash *string
	var expected *int64
	var parserID, parserVersion, parserConfigHash, chunkStrategyVersion, schemaVersion *string
	err := tx.QueryRow(ctx, `SELECT id::text,source_manifest_hash,expected_source_count,source_parser_id,
		source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version
		FROM retrieval.index_version WHERE workspace_id=$1 AND status='active'`, string(workspaceID)).Scan(
		&id, &sourceHash, &expected, &parserID, &parserVersion, &parserConfigHash, &chunkStrategyVersion, &schemaVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, classify(err, "REINDEX_ACTIVE_QUERY_FAILED")
	}
	if sourceHash == nil && expected == nil {
		return id, false, nil
	}
	if sourceHash == nil || expected == nil || parserID == nil || parserVersion == nil || parserConfigHash == nil || chunkStrategyVersion == nil || schemaVersion == nil {
		return "", false, consistency("REINDEX_ACTIVE_SOURCE_MANIFEST_INVALID", errors.New("active source manifest binding is partial"))
	}
	activeContract := &domain.ProcessingContract{
		ParserID: *parserID, ParserVersion: *parserVersion, ParserConfigHash: *parserConfigHash,
		ChunkStrategyVersion: *chunkStrategyVersion, SchemaVersion: *schemaVersion,
	}
	if !domain.SameProcessingContract(activeContract, requestedContract) {
		return id, false, nil
	}
	var actual int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM retrieval.index_manifest_source WHERE index_version_id=$1`, string(id)).Scan(&actual); err != nil {
		return "", false, classify(err, "REINDEX_ACTIVE_SOURCE_MANIFEST_QUERY_FAILED")
	}
	if actual != *expected {
		return "", false, consistency("REINDEX_ACTIVE_SOURCE_MANIFEST_INVALID", errors.New("active source manifest count differs from index"))
	}
	return id, true, nil
}

func stageIncrementalSources(ctx context.Context, tx pgx.Tx, command domain.WorkspaceSnapshotCommand, activeID foundation.ID) error {
	cursor := snapshotCursorID
	var staged int64
	for {
		rows, err := tx.Query(ctx, `SELECT source_id::text,workspace_id::text,source_version_id::text,parse_projection_id::text,
            selection_status,exclusion_code,created_at
            FROM retrieval.index_manifest_source
            WHERE index_version_id=$1 AND source_id>$2 AND source_id<>$3
            ORDER BY source_id LIMIT $4`, string(activeID), string(cursor), string(command.TargetSourceID), command.PageSize)
		if err != nil {
			return classify(err, "REINDEX_ACTIVE_SOURCE_PAGE_FAILED")
		}
		page, last, err := scanSourcePage(rows)
		rows.Close()
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		staged += int64(len(page))
		if staged+1 > command.MaxSources {
			return snapshotCapacityError("source count exceeds configured maximum")
		}
		if err := copySourceStage(ctx, tx, page, command.IndexVersion.CreatedAt); err != nil {
			return err
		}
		cursor = last
		if len(page) < int(command.PageSize) {
			return nil
		}
	}
}

func stageFirstBuildSources(ctx context.Context, tx pgx.Tx, command domain.WorkspaceSnapshotCommand) error {
	cursor := snapshotCursorID
	var staged int64
	contract := *command.IndexVersion.ProcessingContract
	for {
		rows, err := tx.Query(ctx, `SELECT source.id::text,source.workspace_id::text,
            choice.source_version_id::text,choice.parse_projection_id::text
            FROM core.source source
            LEFT JOIN LATERAL (
                SELECT version.id AS source_version_id,attempt.parse_projection_id
                  FROM core.source_version version
                  JOIN LATERAL (
                    SELECT candidate.parse_projection_id
			      FROM ingestion.attempt candidate
			      JOIN ingestion.source_version_projection mapping
			        ON mapping.source_version_id=candidate.source_version_id
			       AND mapping.parse_projection_id=candidate.parse_projection_id
			       AND mapping.workspace_id=$1
			     WHERE candidate.source_version_id=version.id
                       AND candidate.status='chunked' AND candidate.security_status='passed'
                       AND candidate.parser_id=$4 AND candidate.parser_version=$5 AND candidate.parser_config_hash=$6
                       AND candidate.chunk_strategy_version=$7 AND candidate.schema_version=$8
                     ORDER BY candidate.started_at DESC,candidate.id DESC LIMIT 1
                  ) attempt ON true
                 WHERE version.source_id=source.id
                 ORDER BY version.captured_at DESC,version.id DESC LIMIT 1
            ) choice ON true
            WHERE source.workspace_id=$1 AND source.id>$2 AND source.id<>$3
            ORDER BY source.id LIMIT $9`, string(command.IndexVersion.WorkspaceID), string(cursor), string(command.TargetSourceID),
			contract.ParserID, contract.ParserVersion, contract.ParserConfigHash, contract.ChunkStrategyVersion,
			contract.SchemaVersion, command.PageSize)
		if err != nil {
			return classify(err, "REINDEX_SOURCE_SELECTION_PAGE_FAILED")
		}
		page := make([]sourceStageRow, 0, command.PageSize)
		var last foundation.ID
		for rows.Next() {
			var row sourceStageRow
			var version, projection *string
			if err := rows.Scan(&row.SourceID, &row.WorkspaceID, &version, &projection); err != nil {
				rows.Close()
				return classify(err, "REINDEX_SOURCE_SELECTION_SCAN_FAILED")
			}
			if version == nil || projection == nil {
				row.SelectionStatus = string(domain.SourceSelectionExcluded)
				row.ExclusionCode = string(domain.SourceExclusionNoCurrentSuccessfulProjection)
			} else {
				row.SourceVersionID, row.ParseProjectionID = version, projection
				row.SelectionStatus = string(domain.SourceSelectionIncluded)
			}
			last = row.SourceID
			page = append(page, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return classify(err, "REINDEX_SOURCE_SELECTION_SCAN_FAILED")
		}
		if len(page) == 0 {
			return nil
		}
		staged += int64(len(page))
		if staged+1 > command.MaxSources {
			return snapshotCapacityError("source count exceeds configured maximum")
		}
		if err := copySourceStage(ctx, tx, page, command.IndexVersion.CreatedAt); err != nil {
			return err
		}
		cursor = last
		if len(page) < int(command.PageSize) {
			return nil
		}
	}
}

func stageTargetSource(ctx context.Context, tx pgx.Tx, command domain.WorkspaceSnapshotCommand) error {
	_, err := tx.Exec(ctx, `INSERT INTO reindex_source_stage(
        source_id,workspace_id,source_version_id,parse_projection_id,selection_status,exclusion_code,created_at
    ) VALUES($1,$2,$3,$4,'included',NULL,$5)`, string(command.TargetSourceID), string(command.IndexVersion.WorkspaceID),
		string(command.TargetSourceVersionID), string(command.TargetParseProjectionID), command.IndexVersion.CreatedAt.UTC())
	if err != nil {
		return classify(err, "REINDEX_TARGET_SOURCE_STAGE_FAILED")
	}
	return nil
}

type sourceStageRow struct {
	SourceID, WorkspaceID foundation.ID
	SourceVersionID       *string
	ParseProjectionID     *string
	SelectionStatus       string
	ExclusionCode         string
}

func scanSourcePage(rows pgx.Rows) ([]sourceStageRow, foundation.ID, error) {
	page := make([]sourceStageRow, 0)
	var last foundation.ID
	for rows.Next() {
		var row sourceStageRow
		var version, projection, exclusion *string
		var createdAt time.Time
		if err := rows.Scan(&row.SourceID, &row.WorkspaceID, &version, &projection, &row.SelectionStatus, &exclusion, &createdAt); err != nil {
			return nil, "", classify(err, "REINDEX_SOURCE_PAGE_SCAN_FAILED")
		}
		row.SourceVersionID, row.ParseProjectionID = version, projection
		if exclusion != nil {
			row.ExclusionCode = *exclusion
		}
		last = row.SourceID
		page = append(page, row)
	}
	return page, last, rows.Err()
}

func copySourceStage(ctx context.Context, tx pgx.Tx, page []sourceStageRow, createdAt time.Time) error {
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"reindex_source_stage"},
		[]string{"source_id", "workspace_id", "source_version_id", "parse_projection_id", "selection_status", "exclusion_code", "created_at"},
		pgx.CopyFromSlice(len(page), func(index int) ([]any, error) {
			row := page[index]
			return []any{string(row.SourceID), string(row.WorkspaceID), row.SourceVersionID, row.ParseProjectionID, row.SelectionStatus, nullableString(row.ExclusionCode), createdAt}, nil
		}))
	if err != nil {
		return classify(err, "REINDEX_SOURCE_STAGE_COPY_FAILED")
	}
	return nil
}

func stageSnapshotChunks(ctx context.Context, tx pgx.Tx, command domain.WorkspaceSnapshotCommand) error {
	contract := *command.IndexVersion.ProcessingContract
	var count int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM (
		SELECT DISTINCT chunk.id
		  FROM reindex_source_stage source_manifest
		  JOIN ingestion.parse_projection projection
		    ON projection.id=source_manifest.parse_projection_id
		   AND projection.workspace_id=source_manifest.workspace_id
		   AND projection.parser_id=$2 AND projection.parser_version=$3
		   AND projection.parser_config_hash=$4 AND projection.schema_version=$6
		  JOIN ingestion.canonical_chunk chunk
		    ON chunk.parse_projection_id=source_manifest.parse_projection_id
		   AND chunk.workspace_id=source_manifest.workspace_id
		   AND chunk.status='active' AND chunk.parser_version=$3
		   AND chunk.chunk_strategy_version=$5 AND chunk.schema_version=$6
		 WHERE source_manifest.selection_status='included' AND source_manifest.workspace_id=$1
	) selected`, string(command.IndexVersion.WorkspaceID), contract.ParserID, contract.ParserVersion,
		contract.ParserConfigHash, contract.ChunkStrategyVersion, contract.SchemaVersion).Scan(&count); err != nil {
		return classify(err, "REINDEX_CHUNK_STAGE_COUNT_FAILED")
	}
	if count > command.MaxChunks {
		return snapshotCapacityError("chunk count exceeds configured maximum")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO reindex_chunk_stage(
		chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at
	) SELECT DISTINCT chunk.id,chunk.workspace_id,chunk.content_hash,chunk.sequence,chunk.parser_version,
		chunk.chunk_strategy_version,chunk.schema_version,$7::timestamptz
	  FROM reindex_source_stage source_manifest
	  JOIN ingestion.parse_projection projection
	    ON projection.id=source_manifest.parse_projection_id
	   AND projection.workspace_id=source_manifest.workspace_id
	   AND projection.parser_id=$2 AND projection.parser_version=$3
	   AND projection.parser_config_hash=$4 AND projection.schema_version=$6
	  JOIN ingestion.canonical_chunk chunk
	    ON chunk.parse_projection_id=source_manifest.parse_projection_id
	   AND chunk.workspace_id=source_manifest.workspace_id
	   AND chunk.status='active' AND chunk.parser_version=$3
	   AND chunk.chunk_strategy_version=$5 AND chunk.schema_version=$6
	 WHERE source_manifest.selection_status='included' AND source_manifest.workspace_id=$1`,
		string(command.IndexVersion.WorkspaceID), contract.ParserID, contract.ParserVersion,
		contract.ParserConfigHash, contract.ChunkStrategyVersion, contract.SchemaVersion,
		command.IndexVersion.CreatedAt.UTC())
	if err != nil {
		return classify(err, "REINDEX_CHUNK_STAGE_FAILED")
	}
	if tag.RowsAffected() != count {
		return consistency("REINDEX_CHUNK_STAGE_INCOMPLETE", errors.New("chunk stage count differs from selected union"))
	}
	return nil
}

func hashStagedSources(ctx context.Context, tx pgx.Tx, command domain.WorkspaceSnapshotCommand) (string, int64, int64, error) {
	hasher, err := domain.NewSourceManifestHasher(command.IndexVersion.WorkspaceID, command.IndexVersion.ID)
	if err != nil {
		return "", 0, 0, err
	}
	cursor := snapshotCursorID
	for {
		rows, err := tx.Query(ctx, `SELECT source_id::text,workspace_id::text,source_version_id::text,parse_projection_id::text,
            selection_status,exclusion_code,created_at FROM reindex_source_stage
            WHERE source_id>$1 ORDER BY source_id LIMIT $2`, string(cursor), command.PageSize)
		if err != nil {
			return "", 0, 0, classify(err, "REINDEX_SOURCE_HASH_PAGE_FAILED")
		}
		count := 0
		for rows.Next() {
			var source domain.ManifestSource
			var version, projection, exclusion *string
			if err := rows.Scan(&source.SourceID, &source.WorkspaceID, &version, &projection, &source.SelectionStatus, &exclusion, &source.CreatedAt); err != nil {
				rows.Close()
				return "", 0, 0, classify(err, "REINDEX_SOURCE_HASH_SCAN_FAILED")
			}
			source.IndexVersionID = command.IndexVersion.ID
			if version != nil {
				source.SourceVersionID = foundation.ID(*version)
			}
			if projection != nil {
				source.ParseProjectionID = foundation.ID(*projection)
			}
			if exclusion != nil {
				source.ExclusionCode = domain.SourceExclusionCode(*exclusion)
			}
			if err := hasher.Add(source); err != nil {
				rows.Close()
				return "", 0, 0, err
			}
			cursor = source.SourceID
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", 0, 0, classify(err, "REINDEX_SOURCE_HASH_SCAN_FAILED")
		}
		if count < int(command.PageSize) {
			return hasher.Result()
		}
	}
}

func hashStagedChunks(ctx context.Context, tx pgx.Tx, command domain.WorkspaceSnapshotCommand) (string, int64, error) {
	hasher, err := domain.NewManifestHasher(command.IndexVersion.WorkspaceID, command.IndexVersion.ID)
	if err != nil {
		return "", 0, err
	}
	var sequence int32 = -1
	chunkID := snapshotCursorID
	for {
		rows, err := tx.Query(ctx, `SELECT chunk_id::text,workspace_id::text,content_hash,sequence,parser_version,
            chunk_strategy_version,schema_version,created_at FROM reindex_chunk_stage
            WHERE sequence>$1 OR (sequence=$1 AND chunk_id>$2)
            ORDER BY sequence,chunk_id LIMIT $3`, sequence, string(chunkID), command.PageSize)
		if err != nil {
			return "", 0, classify(err, "REINDEX_CHUNK_HASH_PAGE_FAILED")
		}
		count := 0
		for rows.Next() {
			var chunk domain.ManifestChunk
			if err := rows.Scan(&chunk.ChunkID, &chunk.WorkspaceID, &chunk.ContentHash, &chunk.Sequence, &chunk.ParserVersion, &chunk.ChunkStrategyVersion, &chunk.SchemaVersion, &chunk.CreatedAt); err != nil {
				rows.Close()
				return "", 0, classify(err, "REINDEX_CHUNK_HASH_SCAN_FAILED")
			}
			chunk.IndexVersionID = command.IndexVersion.ID
			if err := hasher.Add(chunk); err != nil {
				rows.Close()
				return "", 0, err
			}
			sequence, chunkID = chunk.Sequence, chunk.ChunkID
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", 0, classify(err, "REINDEX_CHUNK_HASH_SCAN_FAILED")
		}
		if count < int(command.PageSize) {
			return hasher.Result()
		}
	}
}

func copyStagedSources(ctx context.Context, tx pgx.Tx, command domain.WorkspaceSnapshotCommand) error {
	cursor := snapshotCursorID
	for {
		rows, err := tx.Query(ctx, `SELECT source_id::text,workspace_id::text,source_version_id::text,parse_projection_id::text,
            selection_status,exclusion_code,created_at FROM reindex_source_stage
            WHERE source_id>$1 ORDER BY source_id LIMIT $2`, string(cursor), command.PageSize)
		if err != nil {
			return classify(err, "REINDEX_SOURCE_COPY_PAGE_FAILED")
		}
		page := make([]domain.ManifestSource, 0, command.PageSize)
		for rows.Next() {
			var source domain.ManifestSource
			var version, projection, exclusion *string
			if err := rows.Scan(&source.SourceID, &source.WorkspaceID, &version, &projection, &source.SelectionStatus, &exclusion, &source.CreatedAt); err != nil {
				rows.Close()
				return classify(err, "REINDEX_SOURCE_COPY_SCAN_FAILED")
			}
			source.IndexVersionID = command.IndexVersion.ID
			if version != nil {
				source.SourceVersionID = foundation.ID(*version)
			}
			if projection != nil {
				source.ParseProjectionID = foundation.ID(*projection)
			}
			if exclusion != nil {
				source.ExclusionCode = domain.SourceExclusionCode(*exclusion)
			}
			cursor = source.SourceID
			page = append(page, source)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return classify(err, "REINDEX_SOURCE_COPY_SCAN_FAILED")
		}
		if len(page) == 0 {
			return nil
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"retrieval", "index_manifest_source"},
			[]string{"index_version_id", "workspace_id", "source_id", "source_version_id", "parse_projection_id", "selection_status", "exclusion_code", "created_at"},
			pgx.CopyFromSlice(len(page), func(i int) ([]any, error) {
				s := page[i]
				return []any{string(s.IndexVersionID), string(s.WorkspaceID), string(s.SourceID), optionalStringID(s.SourceVersionID), optionalStringID(s.ParseProjectionID), string(s.SelectionStatus), nullableString(string(s.ExclusionCode)), s.CreatedAt.UTC()}, nil
			})); err != nil {
			return classify(err, "REINDEX_SOURCE_MANIFEST_COPY_FAILED")
		}
		if len(page) < int(command.PageSize) {
			return nil
		}
	}
}

func copyStagedChunks(ctx context.Context, tx pgx.Tx, command domain.WorkspaceSnapshotCommand) error {
	var sequence int32 = -1
	chunkID := snapshotCursorID
	for {
		rows, err := tx.Query(ctx, `SELECT chunk_id::text,workspace_id::text,content_hash,sequence,parser_version,
            chunk_strategy_version,schema_version,created_at FROM reindex_chunk_stage
            WHERE sequence>$1 OR (sequence=$1 AND chunk_id>$2) ORDER BY sequence,chunk_id LIMIT $3`, sequence, string(chunkID), command.PageSize)
		if err != nil {
			return classify(err, "REINDEX_CHUNK_COPY_PAGE_FAILED")
		}
		page := make([]domain.ManifestChunk, 0, command.PageSize)
		for rows.Next() {
			var chunk domain.ManifestChunk
			if err := rows.Scan(&chunk.ChunkID, &chunk.WorkspaceID, &chunk.ContentHash, &chunk.Sequence, &chunk.ParserVersion, &chunk.ChunkStrategyVersion, &chunk.SchemaVersion, &chunk.CreatedAt); err != nil {
				rows.Close()
				return classify(err, "REINDEX_CHUNK_COPY_SCAN_FAILED")
			}
			chunk.IndexVersionID = command.IndexVersion.ID
			sequence, chunkID = chunk.Sequence, chunk.ChunkID
			page = append(page, chunk)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return classify(err, "REINDEX_CHUNK_COPY_SCAN_FAILED")
		}
		if len(page) == 0 {
			return nil
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"retrieval", "index_manifest_chunk"}, []string{"index_version_id", "chunk_id", "workspace_id", "content_hash", "sequence", "parser_version", "chunk_strategy_version", "schema_version", "created_at"}, pgx.CopyFromSlice(len(page), func(i int) ([]any, error) {
			c := page[i]
			return []any{string(c.IndexVersionID), string(c.ChunkID), string(c.WorkspaceID), c.ContentHash, c.Sequence, c.ParserVersion, c.ChunkStrategyVersion, c.SchemaVersion, c.CreatedAt.UTC()}, nil
		})); err != nil {
			return classify(err, "REINDEX_CHUNK_MANIFEST_COPY_FAILED")
		}
		if len(page) < int(command.PageSize) {
			return nil
		}
	}
}

func replayWorkspaceSnapshot(ctx context.Context, tx pgx.Tx, command domain.WorkspaceSnapshotCommand) (domain.WorkspaceSnapshotResult, bool, error) {
	index, err := scanIndex(tx.QueryRow(ctx, `SELECT `+indexColumns+` FROM retrieval.index_version WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(command.IndexVersion.WorkspaceID), command.IndexVersion.IdempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkspaceSnapshotResult{}, false, nil
	}
	if err != nil {
		return domain.WorkspaceSnapshotResult{}, false, classify(err, "REINDEX_SNAPSHOT_REPLAY_QUERY_FAILED")
	}
	if !sameSnapshotIndexConfig(index, command.IndexVersion) || index.ExpectedSourceCount == nil {
		return domain.WorkspaceSnapshotResult{}, false, conflict("REINDEX_SNAPSHOT_IDEMPOTENCY_CONFLICT", errors.New("snapshot idempotency key is bound to different configuration"))
	}
	if err := validateTargetProjection(ctx, tx, command); err != nil {
		return domain.WorkspaceSnapshotResult{}, false, err
	}
	var sourceCount, chunkCount, excluded, targetCount int64
	if err := tx.QueryRow(ctx, `SELECT
        (SELECT count(*) FROM retrieval.index_manifest_source WHERE index_version_id=$1),
        (SELECT count(*) FROM retrieval.index_manifest_chunk WHERE index_version_id=$1),
        (SELECT count(*) FROM retrieval.index_manifest_source WHERE index_version_id=$1 AND selection_status='excluded'),
        (SELECT count(*) FROM retrieval.index_manifest_source WHERE index_version_id=$1 AND source_id=$2 AND source_version_id=$3 AND parse_projection_id=$4 AND selection_status='included')`, string(index.ID), string(command.TargetSourceID), string(command.TargetSourceVersionID), string(command.TargetParseProjectionID)).Scan(&sourceCount, &chunkCount, &excluded, &targetCount); err != nil {
		return domain.WorkspaceSnapshotResult{}, false, classify(err, "REINDEX_SNAPSHOT_REPLAY_MANIFEST_QUERY_FAILED")
	}
	if sourceCount != *index.ExpectedSourceCount || chunkCount != index.ExpectedChunkCount || targetCount != 1 {
		return domain.WorkspaceSnapshotResult{}, false, consistency("REINDEX_SNAPSHOT_REPLAY_INVALID", errors.New("persisted snapshot is incomplete"))
	}
	return domain.WorkspaceSnapshotResult{IndexVersion: index, SourceCount: sourceCount, ChunkCount: chunkCount, ExcludedSourceCount: excluded, Replayed: true}, true, nil
}

func sameSnapshotIndexConfig(existing, requested domain.IndexVersion) bool {
	return existing.WorkspaceID == requested.WorkspaceID && sameSnapshotEmbeddingID(existing.EmbeddingVersionID, requested.EmbeddingVersionID) &&
		existing.TokenizerID == requested.TokenizerID && existing.TokenizerVersion == requested.TokenizerVersion &&
		existing.TokenizerConfigHash == requested.TokenizerConfigHash && domain.SameJSONValue(existing.FusionConfig, requested.FusionConfig) &&
		existing.SourceSnapshotRef == requested.SourceSnapshotRef && existing.IdempotencyKey == requested.IdempotencyKey &&
		domain.SameProcessingContract(existing.ProcessingContract, requested.ProcessingContract) &&
		sameSnapshotDegradations(existing.DegradedCapabilities, requested.DegradedCapabilities)
}

func sameSnapshotEmbeddingID(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameSnapshotDegradations(left, right []domain.DegradedCapability) bool {
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

func optionalStringID(value foundation.ID) any {
	if value == "" {
		return nil
	}
	return string(value)
}

func snapshotCapacityError(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "REINDEX_SNAPSHOT_CAPACITY_EXCEEDED", false, fmt.Errorf("%s", message))
}
