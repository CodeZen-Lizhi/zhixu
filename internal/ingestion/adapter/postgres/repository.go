// Package postgres persists immutable Ingestion attempts and parse projections.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the pgx-compatible transaction boundary required by Repository.
type DB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

// Repository is the PostgreSQL implementation of domain.Repository.
type Repository struct{ db DB }

// NewRepository constructs an Ingestion repository without exposing pgx to domain callers.
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_DATABASE_UNAVAILABLE", true, errors.New("database is nil"))
	}
	return &Repository{db: db}, nil
}

// CreateAttempt idempotently appends one attempt for its processing request identity.
func (r *Repository) CreateAttempt(ctx context.Context, record domain.AttemptRecord) (domain.AttemptResult, error) {
	warnings, err := marshalWarnings(record.Warnings)
	if err != nil {
		return domain.AttemptResult{}, invalid("INGESTION_WARNINGS_INVALID", err)
	}
	row := r.db.QueryRow(ctx, `
		INSERT INTO ingestion.attempt (
			id, workspace_id, source_version_id, workflow_run_id, parse_projection_id,
			status, security_status, failure_stage, error_code, retryable,
			parser_id, parser_version, parser_config_hash, chunk_strategy_version,
			schema_version, idempotency_key, attempt_number, warnings,
			started_at, completed_at, version
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		ON CONFLICT (source_version_id, parser_id, parser_version, parser_config_hash, chunk_strategy_version, schema_version, idempotency_key)
		DO NOTHING
		RETURNING id::text, workspace_id::text, source_version_id::text, workflow_run_id::text,
			parse_projection_id::text, status, security_status, failure_stage, error_code,
			retryable, parser_id, parser_version, parser_config_hash, chunk_strategy_version,
			schema_version, idempotency_key, attempt_number, warnings, started_at, completed_at, version`,
		string(record.ID), string(record.WorkspaceID), string(record.SourceVersionID), optionalID(record.WorkflowRunID), optionalID(record.ParseProjectionID),
		string(record.Status), string(record.SecurityStatus), record.FailureStage, record.ErrorCode, record.Retryable,
		record.ParserID, record.ParserVersion, record.ParserConfigHash, record.ChunkStrategyVersion,
		record.SchemaVersion, record.IdempotencyKey, record.AttemptNumber, warnings,
		record.StartedAt.UTC(), utcPointer(record.CompletedAt), record.Version,
	)
	persisted, err := scanAttempt(row)
	if err == nil {
		return domain.AttemptResult{Attempt: persisted, Created: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.AttemptResult{}, classify(err, "INGESTION_ATTEMPT_CREATE_FAILED")
	}
	persisted, err = scanAttempt(r.db.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, source_version_id::text, workflow_run_id::text,
			parse_projection_id::text, status, security_status, failure_stage, error_code,
			retryable, parser_id, parser_version, parser_config_hash, chunk_strategy_version,
			schema_version, idempotency_key, attempt_number, warnings, started_at, completed_at, version
		FROM ingestion.attempt
		WHERE source_version_id=$1 AND parser_id=$2 AND parser_version=$3 AND parser_config_hash=$4
		  AND chunk_strategy_version=$5 AND schema_version=$6 AND idempotency_key=$7`,
		string(record.SourceVersionID), record.ParserID, record.ParserVersion, record.ParserConfigHash,
		record.ChunkStrategyVersion, record.SchemaVersion, record.IdempotencyKey,
	))
	if err != nil {
		return domain.AttemptResult{}, classify(err, "INGESTION_ATTEMPT_QUERY_FAILED")
	}
	if persisted.AttemptNumber != record.AttemptNumber || !sameOptionalID(persisted.WorkflowRunID, record.WorkflowRunID) {
		return domain.AttemptResult{}, foundation.NewError(foundation.ErrorVersionConflict, "INGESTION_IDEMPOTENCY_CONFLICT", false, errors.New("idempotency key was already used with different workflow or attempt metadata"))
	}
	return domain.AttemptResult{Attempt: persisted, Created: false}, nil
}

// GetAttempt returns one persisted Attempt by stable ID.
func (r *Repository) GetAttempt(ctx context.Context, id foundation.ID) (domain.AttemptRecord, error) {
	record, err := scanAttempt(r.db.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, source_version_id::text, workflow_run_id::text,
			parse_projection_id::text, status, security_status, failure_stage, error_code,
			retryable, parser_id, parser_version, parser_config_hash, chunk_strategy_version,
			schema_version, idempotency_key, attempt_number, warnings, started_at, completed_at, version
		FROM ingestion.attempt WHERE id=$1`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AttemptRecord{}, foundation.NewError(foundation.ErrorNotFound, "INGESTION_ATTEMPT_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.AttemptRecord{}, classify(err, "INGESTION_ATTEMPT_QUERY_FAILED")
	}
	return record, nil
}

// GetProjection loads one immutable Parse Projection with only the requested
// Chunk strategy/schema pair. Parse Projection/Span identity is shared across
// strategies, while Canonical Chunk identity is strategy-scoped.
func (r *Repository) GetProjection(ctx context.Context, id foundation.ID, strategyVersion, schemaVersion string) (domain.ProjectionResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.ProjectionResult{}, classify(err, "INGESTION_PROJECTION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	projection, err := scanProjection(tx.QueryRow(ctx, `
		SELECT id::text,workspace_id::text,content_artifact_id::text,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at
		FROM ingestion.parse_projection WHERE id=$1`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ProjectionResult{}, foundation.NewError(foundation.ErrorNotFound, "INGESTION_PROJECTION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.ProjectionResult{}, classify(err, "INGESTION_PROJECTION_QUERY_FAILED")
	}
	spans, err := listSpans(ctx, tx, projection.ID)
	if err != nil {
		return domain.ProjectionResult{}, classify(err, "INGESTION_SPAN_QUERY_FAILED")
	}
	chunks, err := listChunks(ctx, tx, projection.ID, strategyVersion, schemaVersion)
	if err != nil {
		return domain.ProjectionResult{}, classify(err, "INGESTION_CHUNK_QUERY_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ProjectionResult{}, classify(err, "INGESTION_PROJECTION_COMMIT_FAILED")
	}
	return domain.ProjectionResult{Projection: projection, Spans: spans, Chunks: chunks, Created: false}, nil
}

// TransitionAttempt performs one optimistic state transition.
func (r *Repository) TransitionAttempt(ctx context.Context, transition domain.AttemptTransition) (domain.AttemptRecord, error) {
	current, err := r.GetAttempt(ctx, transition.ID)
	if err != nil {
		return domain.AttemptRecord{}, err
	}
	if current.Version != transition.ExpectedVersion {
		return domain.AttemptRecord{}, foundation.NewError(foundation.ErrorVersionConflict, "INGESTION_ATTEMPT_VERSION_CONFLICT", false, errors.New("attempt version changed"))
	}
	if err := domain.ValidateAttemptTransition(current.Status, transition.Status, transition.SecurityStatus); err != nil {
		return domain.AttemptRecord{}, invalid("INGESTION_ATTEMPT_TRANSITION_INVALID", err)
	}
	warnings, err := marshalWarnings(transition.Warnings)
	if err != nil {
		return domain.AttemptRecord{}, invalid("INGESTION_WARNINGS_INVALID", err)
	}
	record, err := scanAttempt(r.db.QueryRow(ctx, `
		UPDATE ingestion.attempt
		SET status=$1, security_status=$2, parse_projection_id=$3,
			failure_stage=$4, error_code=$5, retryable=$6, warnings=$7,
			completed_at=$8, version=version+1
		WHERE id=$9 AND version=$10
		RETURNING id::text, workspace_id::text, source_version_id::text, workflow_run_id::text,
			parse_projection_id::text, status, security_status, failure_stage, error_code,
			retryable, parser_id, parser_version, parser_config_hash, chunk_strategy_version,
			schema_version, idempotency_key, attempt_number, warnings, started_at, completed_at, version`,
		string(transition.Status), string(transition.SecurityStatus), optionalID(transition.ParseProjectionID),
		transition.FailureStage, transition.ErrorCode, transition.Retryable, warnings,
		utcPointer(transition.CompletedAt), string(transition.ID), transition.ExpectedVersion,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AttemptRecord{}, foundation.NewError(foundation.ErrorVersionConflict, "INGESTION_ATTEMPT_VERSION_CONFLICT", false, err)
	}
	if err != nil {
		return domain.AttemptRecord{}, classify(err, "INGESTION_ATTEMPT_TRANSITION_FAILED")
	}
	return record, nil
}

// SaveProjection atomically creates or reuses a parser projection and its provenance mapping.
func (r *Repository) SaveProjection(ctx context.Context, write domain.ProjectionWrite) (domain.ProjectionResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.ProjectionResult{}, classify(err, "INGESTION_PROJECTION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	projection, created, err := insertOrGetProjection(ctx, tx, write.Projection)
	if err != nil {
		return domain.ProjectionResult{}, classify(err, "INGESTION_PROJECTION_CREATE_FAILED")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion.source_version_projection
			(source_version_id, parse_projection_id, workspace_id, source_revision_id, created_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (source_version_id, parse_projection_id) DO NOTHING`,
		string(write.SourceVersionID), string(projection.ID), string(projection.WorkspaceID), optionalID(write.SourceRevisionID), write.CreatedAt.UTC(),
	); err != nil {
		return domain.ProjectionResult{}, classify(err, "INGESTION_PROVENANCE_CREATE_FAILED")
	}
	if !created {
		spans, err := listSpans(ctx, tx, projection.ID)
		if err != nil {
			return domain.ProjectionResult{}, classify(err, "INGESTION_SPAN_QUERY_FAILED")
		}
		chunks, err := ensureChunks(ctx, tx, projection, spans, write)
		if err != nil {
			return domain.ProjectionResult{}, classify(err, "INGESTION_CHUNK_CREATE_FAILED")
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.ProjectionResult{}, classify(err, "INGESTION_PROJECTION_COMMIT_FAILED")
		}
		return domain.ProjectionResult{Projection: projection, Spans: spans, Chunks: chunks, Created: false}, nil
	}
	spanIDs := make(map[foundation.ID]foundation.ID, len(write.Spans))
	spans := make([]domain.SourceSpan, 0, len(write.Spans))
	for _, span := range write.Spans {
		candidateID := span.ID
		span.WorkspaceID = projection.WorkspaceID
		span.ContentArtifactID = projection.ContentArtifactID
		span.ParseProjectionID = projection.ID
		persisted, err := insertSpan(ctx, tx, span, write.CreatedAt)
		if err != nil {
			return domain.ProjectionResult{}, classify(err, "INGESTION_SPAN_CREATE_FAILED")
		}
		spanIDs[candidateID] = persisted.ID
		spans = append(spans, persisted)
	}
	chunks := make([]domain.CanonicalChunk, 0, len(write.Chunks))
	for _, chunk := range write.Chunks {
		mappedSpanID, ok := spanIDs[chunk.SourceSpanID]
		if !ok {
			return domain.ProjectionResult{}, invalid("INGESTION_CHUNK_SPAN_INVALID", errors.New("chunk references a span outside the write"))
		}
		chunk.WorkspaceID = projection.WorkspaceID
		chunk.ParseProjectionID = projection.ID
		chunk.SourceSpanID = mappedSpanID
		persisted, err := insertOrGetChunk(ctx, tx, chunk, write.CreatedAt)
		if err != nil {
			return domain.ProjectionResult{}, classify(err, "INGESTION_CHUNK_CREATE_FAILED")
		}
		chunks = append(chunks, persisted)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ProjectionResult{}, classify(err, "INGESTION_PROJECTION_COMMIT_FAILED")
	}
	return domain.ProjectionResult{Projection: projection, Spans: spans, Chunks: chunks, Created: true}, nil
}

func insertOrGetProjection(ctx context.Context, tx pgx.Tx, projection domain.ParseProjection) (domain.ParseProjection, bool, error) {
	warnings, err := marshalWarnings(projection.Warnings)
	if err != nil {
		return domain.ParseProjection{}, false, err
	}
	persisted, err := scanProjection(tx.QueryRow(ctx, `
		INSERT INTO ingestion.parse_projection
			(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version) DO NOTHING
		RETURNING id::text,workspace_id::text,content_artifact_id::text,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at`,
		string(projection.ID), string(projection.WorkspaceID), string(projection.ContentArtifactID), projection.ParserID,
		projection.ParserVersion, projection.ParserConfigHash, projection.SchemaVersion, projection.NormalizedContentHash,
		warnings, projection.CreatedAt.UTC(),
	))
	if err == nil {
		return persisted, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.ParseProjection{}, false, err
	}
	persisted, err = scanProjection(tx.QueryRow(ctx, `
		SELECT id::text,workspace_id::text,content_artifact_id::text,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at
		FROM ingestion.parse_projection
		WHERE content_artifact_id=$1 AND parser_id=$2 AND parser_version=$3 AND parser_config_hash=$4 AND schema_version=$5`,
		string(projection.ContentArtifactID), projection.ParserID, projection.ParserVersion, projection.ParserConfigHash, projection.SchemaVersion,
	))
	return persisted, false, err
}

func insertSpan(ctx context.Context, tx pgx.Tx, span domain.SourceSpan, createdAt time.Time) (domain.SourceSpan, error) {
	selector, err := marshalObject(span.Selector)
	if err != nil {
		return domain.SourceSpan{}, err
	}
	return scanSpan(tx.QueryRow(ctx, `
		INSERT INTO ingestion.source_span
			(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING id::text,workspace_id::text,content_artifact_id::text,parse_projection_id::text,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version`,
		string(span.ID), string(span.WorkspaceID), string(span.ContentArtifactID), string(span.ParseProjectionID), span.SpanType,
		span.StartLine, span.EndLine, span.StartByte, span.EndByte, selector, span.ExcerptHash, span.ParserVersion, span.SchemaVersion, createdAt.UTC(),
	))
}

func insertOrGetChunk(ctx context.Context, tx pgx.Tx, chunk domain.CanonicalChunk, createdAt time.Time) (domain.CanonicalChunk, error) {
	heading, err := marshalStrings(chunk.HeadingPath)
	if err != nil {
		return domain.CanonicalChunk{}, err
	}
	query := `
		INSERT INTO ingestion.canonical_chunk
			(id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (parse_projection_id, chunk_strategy_version, schema_version, sequence) DO NOTHING
		RETURNING id::text,workspace_id::text,parse_projection_id::text,sequence,heading_path,content,content_hash,source_span_id::text,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status`
	persisted, err := scanChunk(tx.QueryRow(ctx, query,
		string(chunk.ID), string(chunk.WorkspaceID), string(chunk.ParseProjectionID), chunk.Sequence, heading, chunk.Content,
		chunk.ContentHash, string(chunk.SourceSpanID), chunk.ByteCount, chunk.RuneCount, chunk.ParserVersion,
		chunk.ChunkStrategyVersion, chunk.SchemaVersion, chunk.AtomicOversized, chunk.Status, createdAt.UTC(),
	))
	if err == nil {
		return persisted, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.CanonicalChunk{}, err
	}
	return scanChunk(tx.QueryRow(ctx, `
		SELECT id::text,workspace_id::text,parse_projection_id::text,sequence,heading_path,content,content_hash,source_span_id::text,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status
		FROM ingestion.canonical_chunk
		WHERE parse_projection_id=$1 AND chunk_strategy_version=$2 AND schema_version=$3 AND sequence=$4`,
		string(chunk.ParseProjectionID), chunk.ChunkStrategyVersion, chunk.SchemaVersion, chunk.Sequence))
}

func listSpans(ctx context.Context, tx pgx.Tx, projectionID foundation.ID) ([]domain.SourceSpan, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,workspace_id::text,content_artifact_id::text,parse_projection_id::text,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version FROM ingestion.source_span WHERE parse_projection_id=$1 ORDER BY start_byte,id`, string(projectionID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.SourceSpan, 0)
	for rows.Next() {
		span, err := scanSpan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, span)
	}
	return result, rows.Err()
}

func listChunks(ctx context.Context, tx pgx.Tx, projectionID foundation.ID, strategyVersion, schemaVersion string) ([]domain.CanonicalChunk, error) {
	var rows pgx.Rows
	var err error
	if strategyVersion == "" && schemaVersion == "" {
		rows, err = tx.Query(ctx, `SELECT id::text,workspace_id::text,parse_projection_id::text,sequence,heading_path,content,content_hash,source_span_id::text,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status FROM ingestion.canonical_chunk WHERE parse_projection_id=$1 ORDER BY sequence,id`, string(projectionID))
	} else {
		rows, err = tx.Query(ctx, `SELECT id::text,workspace_id::text,parse_projection_id::text,sequence,heading_path,content,content_hash,source_span_id::text,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status FROM ingestion.canonical_chunk WHERE parse_projection_id=$1 AND chunk_strategy_version=$2 AND schema_version=$3 ORDER BY sequence,id`, string(projectionID), strategyVersion, schemaVersion)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.CanonicalChunk, 0)
	for rows.Next() {
		chunk, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, chunk)
	}
	return result, rows.Err()
}

func ensureChunks(ctx context.Context, tx pgx.Tx, projection domain.ParseProjection, persistedSpans []domain.SourceSpan, write domain.ProjectionWrite) ([]domain.CanonicalChunk, error) {
	strategyVersion, schemaVersion := chunkVersions(write)
	existing, err := listChunks(ctx, tx, projection.ID, strategyVersion, schemaVersion)
	if err != nil {
		return nil, err
	}
	if len(existing) > len(write.Chunks) {
		return nil, errors.New("persisted chunk set is larger than the requested deterministic chunk set")
	}
	requestedBySequence := make(map[int32]domain.CanonicalChunk, len(write.Chunks))
	for _, chunk := range write.Chunks {
		requestedBySequence[chunk.Sequence] = chunk
	}
	for _, persisted := range existing {
		requested, ok := requestedBySequence[persisted.Sequence]
		if !ok || persisted.ContentHash != requested.ContentHash || persisted.ByteCount != requested.ByteCount || persisted.RuneCount != requested.RuneCount || persisted.ChunkStrategyVersion != requested.ChunkStrategyVersion || persisted.SchemaVersion != requested.SchemaVersion {
			return nil, errors.New("persisted chunk differs from deterministic chunk contract")
		}
	}
	if len(write.Chunks) == 0 || len(existing) == len(write.Chunks) {
		return existing, nil
	}
	spanByKey := make(map[string]foundation.ID, len(persistedSpans))
	for _, span := range persistedSpans {
		spanByKey[spanKey(span.SpanType, span.StartLine, span.EndLine, span.StartByte, span.EndByte, span.SchemaVersion)] = span.ID
	}
	inputSpans := make(map[foundation.ID]domain.SourceSpan, len(write.Spans))
	for _, span := range write.Spans {
		inputSpans[span.ID] = span
	}
	chunks := make([]domain.CanonicalChunk, 0, len(write.Chunks))
	for _, chunk := range write.Chunks {
		inputSpan, ok := inputSpans[chunk.SourceSpanID]
		if !ok {
			return nil, errors.New("chunk references a span outside the write")
		}
		mapped, ok := spanByKey[spanKey(inputSpan.SpanType, inputSpan.StartLine, inputSpan.EndLine, inputSpan.StartByte, inputSpan.EndByte, inputSpan.SchemaVersion)]
		if !ok {
			return nil, errors.New("chunk span does not belong to the projection")
		}
		chunk.WorkspaceID = projection.WorkspaceID
		chunk.ParseProjectionID = projection.ID
		chunk.SourceSpanID = mapped
		persisted, insertErr := insertOrGetChunk(ctx, tx, chunk, write.CreatedAt)
		if insertErr != nil {
			return nil, insertErr
		}
		chunks = append(chunks, persisted)
	}
	return listChunks(ctx, tx, projection.ID, strategyVersion, schemaVersion)
}

func chunkVersions(w domain.ProjectionWrite) (string, string) {
	if len(w.Chunks) == 0 {
		return "", ""
	}
	return w.Chunks[0].ChunkStrategyVersion, w.Chunks[0].SchemaVersion
}

func spanKey(spanType string, startLine, endLine int32, startByte, endByte int64, schemaVersion string) string {
	return fmt.Sprintf("%s|%d|%d|%d|%d|%s", spanType, startLine, endLine, startByte, endByte, schemaVersion)
}

type rowScanner interface{ Scan(...any) error }

func scanAttempt(row rowScanner) (domain.AttemptRecord, error) {
	var record domain.AttemptRecord
	var id, workspaceID, sourceVersionID string
	var workflowID, projectionID *string
	var status, security string
	var warnings []byte
	if err := row.Scan(&id, &workspaceID, &sourceVersionID, &workflowID, &projectionID, &status, &security,
		&record.FailureStage, &record.ErrorCode, &record.Retryable, &record.ParserID, &record.ParserVersion,
		&record.ParserConfigHash, &record.ChunkStrategyVersion, &record.SchemaVersion, &record.IdempotencyKey,
		&record.AttemptNumber, &warnings, &record.StartedAt, &record.CompletedAt, &record.Version); err != nil {
		return domain.AttemptRecord{}, err
	}
	var err error
	if record.ID, err = foundation.ParseID(id); err != nil {
		return domain.AttemptRecord{}, err
	}
	if record.WorkspaceID, err = foundation.ParseID(workspaceID); err != nil {
		return domain.AttemptRecord{}, err
	}
	if record.SourceVersionID, err = foundation.ParseID(sourceVersionID); err != nil {
		return domain.AttemptRecord{}, err
	}
	if workflowID != nil {
		parsed, parseErr := foundation.ParseID(*workflowID)
		if parseErr != nil {
			return domain.AttemptRecord{}, parseErr
		}
		record.WorkflowRunID = &parsed
	}
	if projectionID != nil {
		parsed, parseErr := foundation.ParseID(*projectionID)
		if parseErr != nil {
			return domain.AttemptRecord{}, parseErr
		}
		record.ParseProjectionID = &parsed
	}
	record.Status = domain.AttemptStatus(status)
	record.SecurityStatus = domain.SecurityStatus(security)
	if err := json.Unmarshal(warnings, &record.Warnings); err != nil {
		return domain.AttemptRecord{}, fmt.Errorf("decode attempt warnings: %w", err)
	}
	return record, nil
}

func scanProjection(row rowScanner) (domain.ParseProjection, error) {
	var projection domain.ParseProjection
	var id, workspaceID, artifactID string
	var warnings []byte
	if err := row.Scan(&id, &workspaceID, &artifactID, &projection.ParserID, &projection.ParserVersion,
		&projection.ParserConfigHash, &projection.SchemaVersion, &projection.NormalizedContentHash, &warnings, &projection.CreatedAt); err != nil {
		return domain.ParseProjection{}, err
	}
	var err error
	if projection.ID, err = foundation.ParseID(id); err != nil {
		return domain.ParseProjection{}, err
	}
	if projection.WorkspaceID, err = foundation.ParseID(workspaceID); err != nil {
		return domain.ParseProjection{}, err
	}
	if projection.ContentArtifactID, err = foundation.ParseID(artifactID); err != nil {
		return domain.ParseProjection{}, err
	}
	if err := json.Unmarshal(warnings, &projection.Warnings); err != nil {
		return domain.ParseProjection{}, err
	}
	return projection, nil
}

func scanSpan(row rowScanner) (domain.SourceSpan, error) {
	var span domain.SourceSpan
	var id, workspaceID, artifactID, projectionID string
	var selector []byte
	if err := row.Scan(&id, &workspaceID, &artifactID, &projectionID, &span.SpanType, &span.StartLine,
		&span.EndLine, &span.StartByte, &span.EndByte, &selector, &span.ExcerptHash, &span.ParserVersion, &span.SchemaVersion); err != nil {
		return domain.SourceSpan{}, err
	}
	var err error
	if span.ID, err = foundation.ParseID(id); err != nil {
		return domain.SourceSpan{}, err
	}
	if span.WorkspaceID, err = foundation.ParseID(workspaceID); err != nil {
		return domain.SourceSpan{}, err
	}
	if span.ContentArtifactID, err = foundation.ParseID(artifactID); err != nil {
		return domain.SourceSpan{}, err
	}
	if span.ParseProjectionID, err = foundation.ParseID(projectionID); err != nil {
		return domain.SourceSpan{}, err
	}
	if err := json.Unmarshal(selector, &span.Selector); err != nil {
		return domain.SourceSpan{}, err
	}
	return span, nil
}

func scanChunk(row rowScanner) (domain.CanonicalChunk, error) {
	var chunk domain.CanonicalChunk
	var id, workspaceID, projectionID, spanID string
	var heading []byte
	if err := row.Scan(&id, &workspaceID, &projectionID, &chunk.Sequence, &heading, &chunk.Content,
		&chunk.ContentHash, &spanID, &chunk.ByteCount, &chunk.RuneCount, &chunk.ParserVersion,
		&chunk.ChunkStrategyVersion, &chunk.SchemaVersion, &chunk.AtomicOversized, &chunk.Status); err != nil {
		return domain.CanonicalChunk{}, err
	}
	var err error
	if chunk.ID, err = foundation.ParseID(id); err != nil {
		return domain.CanonicalChunk{}, err
	}
	if chunk.WorkspaceID, err = foundation.ParseID(workspaceID); err != nil {
		return domain.CanonicalChunk{}, err
	}
	if chunk.ParseProjectionID, err = foundation.ParseID(projectionID); err != nil {
		return domain.CanonicalChunk{}, err
	}
	if chunk.SourceSpanID, err = foundation.ParseID(spanID); err != nil {
		return domain.CanonicalChunk{}, err
	}
	if err := json.Unmarshal(heading, &chunk.HeadingPath); err != nil {
		return domain.CanonicalChunk{}, err
	}
	return chunk, nil
}

func optionalID(id *foundation.ID) any {
	if id == nil || *id == "" {
		return nil
	}
	return string(*id)
}

func sameOptionalID(left, right *foundation.ID) bool {
	if left == nil || *left == "" {
		return right == nil || *right == ""
	}
	return right != nil && *right == *left
}

func marshalWarnings(value []domain.Warning) ([]byte, error) {
	if value == nil {
		value = []domain.Warning{}
	}
	return json.Marshal(value)
}

func marshalObject(value map[string]string) ([]byte, error) {
	if value == nil {
		value = map[string]string{}
	}
	return json.Marshal(value)
}

func marshalStrings(value []string) ([]byte, error) {
	if value == nil {
		value = []string{}
	}
	return json.Marshal(value)
}
func utcPointer(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}
func invalid(code string, err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, err)
}

func classify(err error, fallback string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorNotFound, "INGESTION_RECORD_NOT_FOUND", false, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, "INGESTION_RECORD_CONFLICT", false, err)
		case "23503", "23514", "22P02":
			return foundation.NewError(foundation.ErrorConsistencyViolation, "INGESTION_DATA_INVALID", false, err)
		case "40001", "40P01":
			return foundation.NewError(foundation.ErrorRetryableFailure, fallback, true, err)
		case "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, "INGESTION_IMMUTABLE", false, err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, fallback, true, err)
}

var _ domain.Repository = (*Repository)(nil)
