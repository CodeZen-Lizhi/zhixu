package postgres

import (
	"context"
	"errors"
	"time"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ artifactapp.CitationBackfillPort = (*Repository)(nil)

type citationBackfillState struct {
	workspaceID                foundation.ID
	highWaterCreatedAt         *time.Time
	highWaterRevisionID        *foundation.ID
	cursorCreatedAt            *time.Time
	cursorRevisionID           *foundation.ID
	expectedRevisionCount      int64
	processedRevisionCount     int64
	expectedSelectorCount      int64
	processedSelectorCount     int64
	validationCursorCreatedAt  *time.Time
	validationCursorRevisionID *foundation.ID
	validatedRevisionCount     int64
	validatedSelectorCount     int64
	status                     string
	version                    int64
}

type citationBackfillRevision struct {
	revision domain.Revision
}

// BackfillCitationSelectors 在一个短事务内推进一个 Workspace 的历史 selector 回填或复核。
func (repository *Repository) BackfillCitationSelectors(ctx context.Context, limit int) (artifactapp.CitationBackfillResult, bool, error) {
	if repository == nil || repository.db == nil || ctx == nil {
		return artifactapp.CitationBackfillResult{}, false, unavailable(errors.New("artifact citation backfill repository is unavailable"))
	}
	if limit < 1 || limit > artifactapp.MaxCitationBackfillRevisionBatch {
		return artifactapp.CitationBackfillResult{}, false, requestInvalid(errors.New("artifact citation backfill limit is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return artifactapp.CitationBackfillResult{}, false, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	state, found, err := claimCitationBackfillState(ctx, tx)
	if err != nil || !found {
		return artifactapp.CitationBackfillResult{}, found, err
	}
	result := artifactapp.CitationBackfillResult{WorkspaceID: state.workspaceID}
	if state.status == "PENDING" || state.status == "FAILED" {
		if err := resumeCitationBackfillState(ctx, tx, &state); err != nil {
			return artifactapp.CitationBackfillResult{}, true, err
		}
	}
	if state.processedRevisionCount < state.expectedRevisionCount {
		return processCitationBackfillBatch(ctx, tx, state, result, limit)
	}
	return validateCitationBackfillBatch(ctx, tx, state, result, limit)
}

func claimCitationBackfillState(ctx context.Context, tx pgx.Tx) (citationBackfillState, bool, error) {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM core.schema_meta WHERE key='timeline_impact' AND value='m7-v2'
	)`).Scan(&enabled); err != nil {
		return citationBackfillState{}, false, classify(err)
	}
	if !enabled {
		return citationBackfillState{}, false, unavailable(errors.New("artifact citation selector migration is unavailable"))
	}

	row := tx.QueryRow(ctx, `SELECT workspace_id::text,
		high_water_created_at,high_water_revision_id::text,
		cursor_created_at,cursor_revision_id::text,
		expected_revision_count,processed_revision_count,
		expected_selector_count,processed_selector_count,
		validation_cursor_created_at,validation_cursor_revision_id::text,
		validated_revision_count,validated_selector_count,status,version
		FROM learning.artifact_citation_selector_backfill
		WHERE status<>'COMPLETED'
		ORDER BY (status='FAILED'),created_at,workspace_id
		FOR UPDATE SKIP LOCKED
		LIMIT 1`)
	var (
		state                                            citationBackfillState
		workspaceID, highWaterID, cursorID, validationID *string
	)
	if err := row.Scan(
		&workspaceID, &state.highWaterCreatedAt, &highWaterID,
		&state.cursorCreatedAt, &cursorID,
		&state.expectedRevisionCount, &state.processedRevisionCount,
		&state.expectedSelectorCount, &state.processedSelectorCount,
		&state.validationCursorCreatedAt, &validationID,
		&state.validatedRevisionCount, &state.validatedSelectorCount,
		&state.status, &state.version,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return citationBackfillState{}, false, nil
		}
		return citationBackfillState{}, false, classify(err)
	}
	state.workspaceID = foundation.ID(*workspaceID)
	state.highWaterRevisionID = optionalFoundationID(highWaterID)
	state.cursorRevisionID = optionalFoundationID(cursorID)
	state.validationCursorRevisionID = optionalFoundationID(validationID)
	if err := validateCitationBackfillState(state); err != nil {
		return citationBackfillState{}, true, inconsistent(err)
	}
	return state, true, nil
}

func optionalFoundationID(value *string) *foundation.ID {
	if value == nil {
		return nil
	}
	id := foundation.ID(*value)
	return &id
}

func validateCitationBackfillState(state citationBackfillState) error {
	if !validID(state.workspaceID) || state.expectedRevisionCount < 1 || state.highWaterCreatedAt == nil || state.highWaterRevisionID == nil ||
		state.processedRevisionCount < 0 || state.processedRevisionCount > state.expectedRevisionCount ||
		state.processedSelectorCount < 0 || state.processedSelectorCount > state.expectedSelectorCount ||
		state.validatedRevisionCount < 0 || state.validatedRevisionCount > state.expectedRevisionCount ||
		state.validatedSelectorCount < 0 || state.validatedSelectorCount > state.expectedSelectorCount || state.version < 1 {
		return errors.New("artifact citation backfill marker is invalid")
	}
	if state.status != "PENDING" && state.status != "RUNNING" && state.status != "FAILED" {
		return errors.New("artifact citation backfill marker status is invalid")
	}
	if state.processedRevisionCount == 0 && (state.cursorCreatedAt != nil || state.cursorRevisionID != nil) ||
		state.processedRevisionCount > 0 && (state.cursorCreatedAt == nil || state.cursorRevisionID == nil) ||
		state.validatedRevisionCount == 0 && (state.validationCursorCreatedAt != nil || state.validationCursorRevisionID != nil) ||
		state.validatedRevisionCount > 0 && (state.validationCursorCreatedAt == nil || state.validationCursorRevisionID == nil) {
		return errors.New("artifact citation backfill marker cursor is invalid")
	}
	return nil
}

func resumeCitationBackfillState(ctx context.Context, tx pgx.Tx, state *citationBackfillState) error {
	tag, err := tx.Exec(ctx, `UPDATE learning.artifact_citation_selector_backfill
	SET status='RUNNING',error_code=NULL,updated_at=GREATEST(CURRENT_TIMESTAMP,updated_at),version=version+1
	WHERE workspace_id=$1 AND status=$2 AND version=$3`, string(state.workspaceID), state.status, state.version)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return inconsistent(errors.New("artifact citation backfill resume CAS did not match"))
	}
	state.status = "RUNNING"
	state.version++
	return nil
}

func processCitationBackfillBatch(
	ctx context.Context,
	tx pgx.Tx,
	state citationBackfillState,
	result artifactapp.CitationBackfillResult,
	limit int,
) (artifactapp.CitationBackfillResult, bool, error) {
	remaining := state.expectedRevisionCount - state.processedRevisionCount
	if int64(limit) > remaining {
		limit = int(remaining)
	}
	revisions, err := loadCitationBackfillRevisions(ctx, tx, state, state.cursorCreatedAt, state.cursorRevisionID, limit)
	if err != nil {
		if shouldPersistCitationBackfillFailure(err) {
			return failCitationBackfill(ctx, tx, state, result, err)
		}
		return artifactapp.CitationBackfillResult{}, true, err
	}
	if len(revisions) == 0 {
		return failCitationBackfill(ctx, tx, state, result, errors.New("artifact citation backfill reached the high water mark early"))
	}

	selectorCount, err := insertBackfilledCitationSelectors(ctx, tx, state.workspaceID, revisions)
	if err != nil {
		if !shouldPersistCitationBackfillFailure(err) {
			return artifactapp.CitationBackfillResult{}, true, err
		}
		return failCitationBackfill(ctx, tx, state, result, err)
	}
	last := revisions[len(revisions)-1].revision
	tag, err := tx.Exec(ctx, `UPDATE learning.artifact_citation_selector_backfill
	SET cursor_created_at=$1,cursor_revision_id=$2,
		processed_revision_count=processed_revision_count+$3,
		expected_selector_count=expected_selector_count+$4,
		processed_selector_count=processed_selector_count+$4,
		status='RUNNING',error_code=NULL,updated_at=GREATEST(CURRENT_TIMESTAMP,updated_at),version=version+1
	WHERE workspace_id=$5 AND status='RUNNING' AND version=$6`,
		last.CreatedAt.UTC(), string(last.ID), len(revisions), selectorCount, string(state.workspaceID), state.version)
	if err != nil {
		return artifactapp.CitationBackfillResult{}, true, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return artifactapp.CitationBackfillResult{}, true, inconsistent(errors.New("artifact citation backfill progress CAS did not match"))
	}
	if err := tx.Commit(ctx); err != nil {
		return artifactapp.CitationBackfillResult{}, true, classify(err)
	}
	result.ProcessedRevisions = len(revisions)
	result.ProcessedSelectors = selectorCount
	return result, true, nil
}

func loadCitationBackfillRevisions(
	ctx context.Context,
	tx pgx.Tx,
	state citationBackfillState,
	cursorCreatedAt *time.Time,
	cursorRevisionID *foundation.ID,
	limit int,
) ([]citationBackfillRevision, error) {
	var cursorID any
	if cursorRevisionID != nil {
		cursorID = string(*cursorRevisionID)
	}
	rows, err := tx.Query(ctx, revisionSelect+`
		WHERE r.workspace_id=$1 AND r.domain_schema_version=$2
		  AND (r.created_at,r.id) <= ($3,$4)
		  AND ($5::timestamptz IS NULL OR (r.created_at,r.id) > ($5,$6))
		ORDER BY r.created_at,r.id
		LIMIT $7`, string(state.workspaceID), artifactRevisionSchemaVersion,
		state.highWaterCreatedAt.UTC(), string(*state.highWaterRevisionID), cursorCreatedAt, cursorID, limit)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	revisions := make([]citationBackfillRevision, 0, limit)
	for rows.Next() {
		var persisted persistedRevision
		if err := rows.Scan(persisted.scanTargets()...); err != nil {
			return nil, classify(err)
		}
		revision, _, err := decodePersistedRevision(persisted)
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, citationBackfillRevision{revision: revision})
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	return revisions, nil
}

func insertBackfilledCitationSelectors(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, revisions []citationBackfillRevision) (int, error) {
	artifactIDs := make([]string, 0)
	revisionIDs := make([]string, 0)
	sourceVersionIDs := make([]string, 0)
	sourceSpanIDs := make([]string, 0)
	for _, item := range revisions {
		for _, selector := range revisionCitationSelectors(item.revision) {
			artifactIDs = append(artifactIDs, string(item.revision.ArtifactID))
			revisionIDs = append(revisionIDs, string(item.revision.ID))
			sourceVersionIDs = append(sourceVersionIDs, string(selector.sourceVersionID))
			sourceSpanIDs = append(sourceSpanIDs, string(selector.sourceSpanID))
		}
	}
	if len(revisionIDs) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT artifact_citation_backfill_insert`); err != nil {
		return 0, classify(err)
	}
	_, err := tx.Exec(ctx, `INSERT INTO learning.artifact_revision_citation_selector(
		workspace_id,artifact_id,revision_id,source_version_id,source_span_id
	)
	SELECT $1,selector.artifact_id,selector.revision_id,selector.source_version_id,selector.source_span_id
	FROM unnest($2::uuid[],$3::uuid[],$4::uuid[],$5::uuid[])
		AS selector(artifact_id,revision_id,source_version_id,source_span_id)
	ON CONFLICT (workspace_id,revision_id,source_version_id,source_span_id) DO NOTHING`,
		string(workspaceID), artifactIDs, revisionIDs, sourceVersionIDs, sourceSpanIDs)
	if err != nil {
		if _, rollbackErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT artifact_citation_backfill_insert`); rollbackErr != nil {
			return 0, classify(rollbackErr)
		}
		if _, releaseErr := tx.Exec(ctx, `RELEASE SAVEPOINT artifact_citation_backfill_insert`); releaseErr != nil {
			return 0, classify(releaseErr)
		}
		if citationBackfillDataError(err) {
			return 0, err
		}
		return 0, classify(err)
	}
	if _, err := tx.Exec(ctx, `RELEASE SAVEPOINT artifact_citation_backfill_insert`); err != nil {
		return 0, classify(err)
	}
	return len(revisionIDs), nil
}

func citationBackfillDataError(err error) bool {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return false
	}
	switch postgresError.Code {
	case "23502", "23503", "23514", "22P02":
		return true
	default:
		return false
	}
}

func validateCitationBackfillBatch(
	ctx context.Context,
	tx pgx.Tx,
	state citationBackfillState,
	result artifactapp.CitationBackfillResult,
	limit int,
) (artifactapp.CitationBackfillResult, bool, error) {
	remaining := state.expectedRevisionCount - state.validatedRevisionCount
	if int64(limit) > remaining {
		limit = int(remaining)
	}
	if limit < 1 {
		return failCitationBackfill(ctx, tx, state, result, errors.New("artifact citation backfill validation counts are inconsistent"))
	}
	revisions, err := loadCitationBackfillRevisions(ctx, tx, state, state.validationCursorCreatedAt, state.validationCursorRevisionID, limit)
	if err != nil {
		if shouldPersistCitationBackfillFailure(err) {
			return failCitationBackfill(ctx, tx, state, result, err)
		}
		return artifactapp.CitationBackfillResult{}, true, err
	}
	if len(revisions) == 0 {
		return failCitationBackfill(ctx, tx, state, result, errors.New("artifact citation backfill validation reached the high water mark early"))
	}
	validatedSelectors, err := validateBackfilledCitationSelectors(ctx, tx, state.workspaceID, revisions)
	if err != nil {
		if !shouldPersistCitationBackfillFailure(err) {
			return artifactapp.CitationBackfillResult{}, true, err
		}
		return failCitationBackfill(ctx, tx, state, result, err)
	}
	validatedRevisionCount := state.validatedRevisionCount + int64(len(revisions))
	validatedSelectorCount := state.validatedSelectorCount + int64(validatedSelectors)
	completed := validatedRevisionCount == state.expectedRevisionCount && validatedSelectorCount == state.expectedSelectorCount
	status := "RUNNING"
	if completed {
		status = "COMPLETED"
	} else if validatedRevisionCount == state.expectedRevisionCount {
		return failCitationBackfill(ctx, tx, state, result, errors.New("artifact citation backfill selector totals are inconsistent"))
	}
	last := revisions[len(revisions)-1].revision
	tag, err := tx.Exec(ctx, `UPDATE learning.artifact_citation_selector_backfill
	SET validation_cursor_created_at=$1,validation_cursor_revision_id=$2,
		validated_revision_count=$3,validated_selector_count=$4,
		status=$5,error_code=NULL,
		completed_at=CASE WHEN $5='COMPLETED' THEN CURRENT_TIMESTAMP ELSE NULL END,
		updated_at=GREATEST(CURRENT_TIMESTAMP,updated_at),version=version+1
	WHERE workspace_id=$6 AND status='RUNNING' AND version=$7`,
		last.CreatedAt.UTC(), string(last.ID), validatedRevisionCount, validatedSelectorCount,
		status, string(state.workspaceID), state.version)
	if err != nil {
		return artifactapp.CitationBackfillResult{}, true, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return artifactapp.CitationBackfillResult{}, true, inconsistent(errors.New("artifact citation backfill validation CAS did not match"))
	}
	if err := tx.Commit(ctx); err != nil {
		return artifactapp.CitationBackfillResult{}, true, classify(err)
	}
	result.ValidatedRevisions = len(revisions)
	result.Completed = completed
	return result, true, nil
}

func validateBackfilledCitationSelectors(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, revisions []citationBackfillRevision) (int, error) {
	domainRevisions := make([]domain.Revision, len(revisions))
	for index, item := range revisions {
		domainRevisions[index] = item.revision
	}
	return validateRevisionCitationSelectors(ctx, tx, workspaceID, domainRevisions)
}

func shouldPersistCitationBackfillFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return true
	}
	switch classified.Kind {
	case foundation.ErrorRetryableFailure, foundation.ErrorDependencyUnavailable:
		return false
	default:
		return true
	}
}

func failCitationBackfill(
	ctx context.Context,
	tx pgx.Tx,
	state citationBackfillState,
	result artifactapp.CitationBackfillResult,
	cause error,
) (artifactapp.CitationBackfillResult, bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE learning.artifact_citation_selector_backfill
	SET status='FAILED',error_code=$1,completed_at=NULL,
		updated_at=GREATEST(CURRENT_TIMESTAMP,updated_at),version=version+1
	WHERE workspace_id=$2 AND status='RUNNING' AND version=$3`,
		artifactapp.ErrorCodeCitationBackfillFailed, string(state.workspaceID), state.version)
	if err != nil {
		return artifactapp.CitationBackfillResult{}, true, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return artifactapp.CitationBackfillResult{}, true, inconsistent(errors.New("artifact citation backfill failure CAS did not match"))
	}
	if err := tx.Commit(ctx); err != nil {
		return artifactapp.CitationBackfillResult{}, true, classify(err)
	}
	return result, true, artifactapp.NewCitationBackfillFailure(cause)
}
