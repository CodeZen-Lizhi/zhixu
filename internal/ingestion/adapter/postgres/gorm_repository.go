package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GORMRepository is the staged GORM implementation. Production composition
// remains on Repository until the migration TODO 9 gate passes.
type GORMRepository struct{ database *gorm.DB }

// NewGORMRepository constructs an Ingestion repository from the shared GORM root.
func NewGORMRepository(database *gorm.DB) (*GORMRepository, error) {
	if !validIngestionGORMDatabase(database) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_DATABASE_UNAVAILABLE", true, errors.New("GORM database is unavailable"))
	}
	return &GORMRepository{database: database}, nil
}

func validIngestionGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func (r *GORMRepository) ready(ctx context.Context) error {
	if r == nil || !validIngestionGORMDatabase(r.database) || ctx == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_DATABASE_UNAVAILABLE", true, errors.New("GORM repository is unavailable"))
	}
	return nil
}

// CreateAttempt idempotently appends one attempt for its processing request identity.
func (r *GORMRepository) CreateAttempt(ctx context.Context, record domain.AttemptRecord) (domain.AttemptResult, error) {
	if err := r.ready(ctx); err != nil {
		return domain.AttemptResult{}, err
	}
	warnings, err := jsonbWarnings(record.Warnings)
	if err != nil {
		return domain.AttemptResult{}, invalid("INGESTION_WARNINGS_INVALID", err)
	}
	db := r.database.WithContext(ctx)
	persisted, err := gormScanAttempt(db, gormCreateAttemptSQL,
		string(record.ID), string(record.WorkspaceID), string(record.SourceVersionID), optionalID(record.WorkflowRunID), optionalID(record.ParseProjectionID),
		string(record.Status), string(record.SecurityStatus), record.FailureStage, record.ErrorCode, record.Retryable,
		record.ParserID, record.ParserVersion, record.ParserConfigHash, record.ChunkStrategyVersion,
		record.SchemaVersion, record.IdempotencyKey, record.AttemptNumber, warnings, record.StartedAt.UTC(), utcPointer(record.CompletedAt), record.Version)
	if err == nil {
		return domain.AttemptResult{Attempt: persisted, Created: true}, nil
	}
	if !gormNoRows(err) {
		return domain.AttemptResult{}, gormClassify(ctx, err, "INGESTION_ATTEMPT_CREATE_FAILED")
	}
	persisted, err = gormScanAttempt(db, gormGetAttemptByIdentitySQL,
		string(record.SourceVersionID), record.ParserID, record.ParserVersion, record.ParserConfigHash,
		record.ChunkStrategyVersion, record.SchemaVersion, record.IdempotencyKey)
	if err != nil {
		return domain.AttemptResult{}, gormClassify(ctx, err, "INGESTION_ATTEMPT_QUERY_FAILED")
	}
	if persisted.AttemptNumber != record.AttemptNumber || !sameOptionalID(persisted.WorkflowRunID, record.WorkflowRunID) {
		return domain.AttemptResult{}, foundation.NewError(foundation.ErrorVersionConflict, "INGESTION_IDEMPOTENCY_CONFLICT", false, errors.New("idempotency key was already used with different workflow or attempt metadata"))
	}
	return domain.AttemptResult{Attempt: persisted, Created: false}, nil
}

// GetAttempt returns one persisted Attempt by stable ID.
func (r *GORMRepository) GetAttempt(ctx context.Context, id foundation.ID) (domain.AttemptRecord, error) {
	if err := r.ready(ctx); err != nil {
		return domain.AttemptRecord{}, err
	}
	record, err := gormScanAttempt(r.database.WithContext(ctx), gormGetAttemptSQL, string(id))
	if gormNoRows(err) {
		return domain.AttemptRecord{}, foundation.NewError(foundation.ErrorNotFound, "INGESTION_ATTEMPT_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.AttemptRecord{}, gormClassify(ctx, err, "INGESTION_ATTEMPT_QUERY_FAILED")
	}
	return record, nil
}

// TransitionAttempt performs one optimistic, domain-validated state transition.
func (r *GORMRepository) TransitionAttempt(ctx context.Context, transition domain.AttemptTransition) (domain.AttemptRecord, error) {
	if err := r.ready(ctx); err != nil {
		return domain.AttemptRecord{}, err
	}
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
	warnings, err := jsonbWarnings(transition.Warnings)
	if err != nil {
		return domain.AttemptRecord{}, invalid("INGESTION_WARNINGS_INVALID", err)
	}
	record, err := gormScanAttempt(r.database.WithContext(ctx), gormTransitionAttemptSQL,
		string(transition.Status), string(transition.SecurityStatus), optionalID(transition.ParseProjectionID),
		transition.FailureStage, transition.ErrorCode, transition.Retryable, warnings,
		utcPointer(transition.CompletedAt), string(transition.ID), transition.ExpectedVersion)
	if gormNoRows(err) {
		return domain.AttemptRecord{}, foundation.NewError(foundation.ErrorVersionConflict, "INGESTION_ATTEMPT_VERSION_CONFLICT", false, err)
	}
	if err != nil {
		return domain.AttemptRecord{}, gormClassify(ctx, err, "INGESTION_ATTEMPT_TRANSITION_FAILED")
	}
	return record, nil
}

// GetProjection loads one immutable projection and its requested strategy.
func (r *GORMRepository) GetProjection(ctx context.Context, id foundation.ID, strategyVersion, schemaVersion string) (result domain.ProjectionResult, err error) {
	if err = r.ready(ctx); err != nil {
		return result, err
	}
	callbackSucceeded := false
	err = r.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		projection, scanErr := gormScanProjection(tx, gormGetProjectionSQL, string(id))
		if gormNoRows(scanErr) {
			return foundation.NewError(foundation.ErrorNotFound, "INGESTION_PROJECTION_NOT_FOUND", false, scanErr)
		}
		if scanErr != nil {
			return gormClassify(ctx, scanErr, "INGESTION_PROJECTION_QUERY_FAILED")
		}
		spans, listErr := gormListSpans(ctx, tx, projection.ID)
		if listErr != nil {
			return gormClassify(ctx, listErr, "INGESTION_SPAN_QUERY_FAILED")
		}
		chunks, listErr := gormListChunks(ctx, tx, projection.ID, strategyVersion, schemaVersion)
		if listErr != nil {
			return gormClassify(ctx, listErr, "INGESTION_CHUNK_QUERY_FAILED")
		}
		result = domain.ProjectionResult{Projection: projection, Spans: spans, Chunks: chunks, Created: false}
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		fallback := "INGESTION_PROJECTION_TRANSACTION_FAILED"
		if callbackSucceeded {
			fallback = "INGESTION_PROJECTION_COMMIT_FAILED"
		}
		return domain.ProjectionResult{}, gormClassify(ctx, err, fallback)
	}
	return result, nil
}

// SaveProjection atomically creates or reuses a parser projection and all
// immutable child facts. Every batch runs inside this one explicit transaction.
func (r *GORMRepository) SaveProjection(ctx context.Context, write domain.ProjectionWrite) (result domain.ProjectionResult, err error) {
	if err = r.ready(ctx); err != nil {
		return result, err
	}
	callbackSucceeded := false
	err = r.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		projection, created, insertErr := gormInsertOrGetProjection(ctx, tx, write.Projection)
		if insertErr != nil {
			return gormClassify(ctx, insertErr, "INGESTION_PROJECTION_CREATE_FAILED")
		}
		if execErr := gormInsertProvenance(ctx, tx, write, projection); execErr != nil {
			return gormClassify(ctx, execErr, "INGESTION_PROVENANCE_CREATE_FAILED")
		}
		if !created {
			spans, listErr := gormListSpans(ctx, tx, projection.ID)
			if listErr != nil {
				return gormClassify(ctx, listErr, "INGESTION_SPAN_QUERY_FAILED")
			}
			chunks, ensureErr := gormEnsureChunks(ctx, tx, projection, spans, write)
			if ensureErr != nil {
				return gormClassify(ctx, ensureErr, "INGESTION_CHUNK_CREATE_FAILED")
			}
			result = domain.ProjectionResult{Projection: projection, Spans: spans, Chunks: chunks, Created: false}
			callbackSucceeded = true
			return nil
		}
		if err := gormInsertSpans(ctx, tx, projection, write.Spans, write.CreatedAt); err != nil {
			return gormClassify(ctx, err, "INGESTION_SPAN_CREATE_FAILED")
		}
		spans, listErr := gormListSpans(ctx, tx, projection.ID)
		if listErr != nil {
			return gormClassify(ctx, listErr, "INGESTION_SPAN_QUERY_FAILED")
		}
		if err := gormInsertChunks(ctx, tx, projection, write.Spans, write.Chunks, write.CreatedAt, false); err != nil {
			return gormClassify(ctx, err, "INGESTION_CHUNK_CREATE_FAILED")
		}
		chunks, listErr := gormListChunks(ctx, tx, projection.ID, "", "")
		if listErr != nil {
			return gormClassify(ctx, listErr, "INGESTION_CHUNK_QUERY_FAILED")
		}
		result = domain.ProjectionResult{Projection: projection, Spans: spans, Chunks: chunks, Created: true}
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		fallback := "INGESTION_PROJECTION_TRANSACTION_FAILED"
		if callbackSucceeded {
			fallback = "INGESTION_PROJECTION_COMMIT_FAILED"
		}
		return domain.ProjectionResult{}, gormClassify(ctx, err, fallback)
	}
	return result, nil
}

func gormInsertOrGetProjection(ctx context.Context, tx *gorm.DB, projection domain.ParseProjection) (domain.ParseProjection, bool, error) {
	warnings, err := jsonbWarnings(projection.Warnings)
	if err != nil {
		return domain.ParseProjection{}, false, err
	}
	persisted, err := gormScanProjection(tx.WithContext(ctx), gormInsertProjectionSQL,
		string(projection.ID), string(projection.WorkspaceID), string(projection.ContentArtifactID), projection.ParserID,
		projection.ParserVersion, projection.ParserConfigHash, projection.SchemaVersion, projection.NormalizedContentHash,
		warnings, projection.CreatedAt.UTC())
	if err == nil {
		return persisted, true, nil
	}
	if !gormNoRows(err) {
		return domain.ParseProjection{}, false, err
	}
	persisted, err = gormScanProjection(tx.WithContext(ctx), gormGetProjectionByContractSQL,
		string(projection.ContentArtifactID), projection.ParserID, projection.ParserVersion, projection.ParserConfigHash, projection.SchemaVersion)
	return persisted, false, err
}

func gormInsertProvenance(ctx context.Context, tx *gorm.DB, write domain.ProjectionWrite, projection domain.ParseProjection) error {
	var sourceRevisionID *string
	if write.SourceRevisionID != nil && *write.SourceRevisionID != "" {
		value := string(*write.SourceRevisionID)
		sourceRevisionID = &value
	}
	record := provenanceGORMRecord{
		SourceVersionID: string(write.SourceVersionID), ParseProjectionID: string(projection.ID), WorkspaceID: string(projection.WorkspaceID),
		SourceRevisionID: sourceRevisionID, CreatedAt: write.CreatedAt.UTC(),
	}
	return tx.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source_version_id"}, {Name: "parse_projection_id"}}, DoNothing: true}).Create(&record).Error
}

func gormListSpans(ctx context.Context, tx *gorm.DB, projectionID foundation.ID) ([]domain.SourceSpan, error) {
	rows, err := gormRawRows(tx.WithContext(ctx), gormListSpansSQL, string(projectionID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.SourceSpan, 0)
	for rows.Next() {
		span, scanErr := scanSpan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, span)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func gormListChunks(ctx context.Context, tx *gorm.DB, projectionID foundation.ID, strategyVersion, schemaVersion string) ([]domain.CanonicalChunk, error) {
	query := gormListChunksSQL
	args := []any{string(projectionID)}
	if strategyVersion != "" || schemaVersion != "" {
		query = gormListChunksByStrategySQL
		args = append(args, strategyVersion, schemaVersion)
	}
	rows, err := gormRawRows(tx.WithContext(ctx), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.CanonicalChunk, 0)
	for rows.Next() {
		chunk, scanErr := scanChunk(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func gormInsertSpans(ctx context.Context, tx *gorm.DB, projection domain.ParseProjection, input []domain.SourceSpan, createdAt time.Time) error {
	if len(input) == 0 {
		return nil
	}
	records := make([]spanGORMRecord, 0, len(input))
	for _, span := range input {
		selector, err := jsonbSelector(span.Selector)
		if err != nil {
			return err
		}
		evidenceKind := string(span.EvidenceKind)
		if evidenceKind == "" {
			evidenceKind = string(domain.EvidenceRawBytes)
		}
		records = append(records, spanGORMRecord{
			ID: string(span.ID), WorkspaceID: string(projection.WorkspaceID), ContentArtifactID: string(projection.ContentArtifactID), ParseProjectionID: string(projection.ID),
			SpanType: span.SpanType, StartLine: span.StartLine, EndLine: span.EndLine, StartByte: span.StartByte, EndByte: span.EndByte,
			Selector: selector, ExcerptHash: span.ExcerptHash, EvidenceKind: evidenceKind, DerivedExcerpt: span.DerivedExcerpt,
			ParserVersion: span.ParserVersion, SchemaVersion: span.SchemaVersion, CreatedAt: createdAt.UTC(),
		})
	}
	return tx.WithContext(ctx).CreateInBatches(&records, projectionBatchSize).Error
}

func gormInsertChunks(ctx context.Context, tx *gorm.DB, projection domain.ParseProjection, inputSpans []domain.SourceSpan, input []domain.CanonicalChunk, createdAt time.Time, ignoreConflicts bool) error {
	if len(input) == 0 {
		return nil
	}
	spanIDs := make(map[foundation.ID]foundation.ID, len(inputSpans))
	for _, span := range inputSpans {
		spanIDs[span.ID] = span.ID
	}
	records := make([]chunkGORMRecord, 0, len(input))
	for _, chunk := range input {
		spanID, ok := spanIDs[chunk.SourceSpanID]
		if !ok {
			err := errors.New("chunk references a span outside the write")
			if !ignoreConflicts {
				return invalid("INGESTION_CHUNK_SPAN_INVALID", err)
			}
			return err
		}
		heading, err := jsonbHeading(chunk.HeadingPath)
		if err != nil {
			return err
		}
		records = append(records, chunkGORMRecord{
			ID: string(chunk.ID), WorkspaceID: string(projection.WorkspaceID), ParseProjectionID: string(projection.ID), Sequence: chunk.Sequence,
			HeadingPath: heading, Content: chunk.Content, ContentHash: chunk.ContentHash, SourceSpanID: string(spanID), ByteCount: chunk.ByteCount,
			RuneCount: chunk.RuneCount, ParserVersion: chunk.ParserVersion, ChunkStrategyVersion: chunk.ChunkStrategyVersion, SchemaVersion: chunk.SchemaVersion,
			AtomicOversized: chunk.AtomicOversized, Status: chunk.Status, CreatedAt: createdAt.UTC(),
		})
	}
	query := tx.WithContext(ctx)
	if ignoreConflicts {
		query = query.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "parse_projection_id"}, {Name: "chunk_strategy_version"}, {Name: "schema_version"}, {Name: "sequence"}}, DoNothing: true})
	}
	return query.CreateInBatches(&records, projectionBatchSize).Error
}

func gormEnsureChunks(ctx context.Context, tx *gorm.DB, projection domain.ParseProjection, persistedSpans []domain.SourceSpan, write domain.ProjectionWrite) ([]domain.CanonicalChunk, error) {
	strategyVersion, schemaVersion := chunkVersions(write)
	existing, err := gormListChunks(ctx, tx, projection.ID, strategyVersion, schemaVersion)
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
		spanByKey[spanKey(span)] = span.ID
	}
	inputSpans, chunksToInsert, remapErr := remapChunksToPersistedSpans(write.Spans, write.Chunks, spanByKey)
	if remapErr != nil {
		return nil, remapErr
	}
	if err := gormInsertChunks(ctx, tx, projection, inputSpans, chunksToInsert, write.CreatedAt, true); err != nil {
		return nil, err
	}
	final, err := gormListChunks(ctx, tx, projection.ID, strategyVersion, schemaVersion)
	if err != nil {
		return nil, err
	}
	if len(final) != len(write.Chunks) {
		return nil, errors.New("persisted chunk set is incomplete after concurrent insert")
	}
	for _, persisted := range final {
		requested, ok := requestedBySequence[persisted.Sequence]
		if !ok || persisted.ContentHash != requested.ContentHash || persisted.ByteCount != requested.ByteCount || persisted.RuneCount != requested.RuneCount || persisted.ChunkStrategyVersion != requested.ChunkStrategyVersion || persisted.SchemaVersion != requested.SchemaVersion {
			return nil, errors.New("persisted chunk differs from deterministic chunk contract")
		}
	}
	return final, nil
}

func remapChunksToPersistedSpans(spans []domain.SourceSpan, chunks []domain.CanonicalChunk, persistedByKey map[string]foundation.ID) ([]domain.SourceSpan, []domain.CanonicalChunk, error) {
	inputByID := make(map[foundation.ID]domain.SourceSpan, len(spans))
	for _, span := range spans {
		inputByID[span.ID] = span
	}
	mappedByInputID := make(map[foundation.ID]foundation.ID, len(chunks))
	resultSpans := make([]domain.SourceSpan, 0, len(chunks))
	resultChunks := make([]domain.CanonicalChunk, 0, len(chunks))
	for _, chunk := range chunks {
		inputSpan, ok := inputByID[chunk.SourceSpanID]
		if !ok {
			return nil, nil, errors.New("chunk references a span outside the write")
		}
		persistedID, seen := mappedByInputID[chunk.SourceSpanID]
		if !seen {
			persistedID, ok = persistedByKey[spanKey(inputSpan)]
			if !ok {
				return nil, nil, errors.New("chunk span does not belong to the projection")
			}
			mappedByInputID[chunk.SourceSpanID] = persistedID
			inputSpan.ID = persistedID
			resultSpans = append(resultSpans, inputSpan)
		}
		chunk.SourceSpanID = persistedID
		resultChunks = append(resultChunks, chunk)
	}
	return resultSpans, resultChunks, nil
}

func gormScanAttempt(database *gorm.DB, query string, args ...any) (domain.AttemptRecord, error) {
	row, err := gormRawRow(database, query, args...)
	if err != nil {
		return domain.AttemptRecord{}, err
	}
	return scanAttempt(row)
}

func gormScanProjection(database *gorm.DB, query string, args ...any) (domain.ParseProjection, error) {
	row, err := gormRawRow(database, query, args...)
	if err != nil {
		return domain.ParseProjection{}, err
	}
	return scanProjection(row)
}

func gormRawRow(database *gorm.DB, query string, args ...any) (rowScanner, error) {
	if database == nil {
		return nil, errors.New("GORM database is nil")
	}
	statement := database.Raw(query, args...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("GORM query returned a nil row")
	}
	return row, nil
}

func gormRawRows(database *gorm.DB, query string, args ...any) (*sql.Rows, error) {
	if database == nil {
		return nil, errors.New("GORM database is nil")
	}
	statement := database.Raw(query, args...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("GORM query returned nil rows")
	}
	return rows, nil
}

func gormNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func gormClassify(ctx context.Context, err error, fallback string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrTxDone) && ctx != nil && ctx.Err() != nil {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, fallback, false, gormCancellationCause(ctx))
	}
	if ctx != nil && ctx.Err() != nil {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, fallback, false, gormCancellationCause(ctx))
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, fallback, false, err)
	}
	if gormNoRows(err) {
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

// gormCancellationCause 保留取消 sentinel 与调用方经 WithCancelCause 设置的
// 自定义 cause，供 errors.Is 同时匹配两者。
func gormCancellationCause(ctx context.Context) error {
	cause := context.Cause(ctx)
	if cause == nil {
		return ctx.Err()
	}
	if !errors.Is(cause, ctx.Err()) {
		return errors.Join(ctx.Err(), cause)
	}
	return cause
}

var _ domain.Repository = (*GORMRepository)(nil)
