package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// gormCitationBackfillRunner isolates the staged bounded backfill flow from
// the main Artifact repository methods. The legacy pgx Repository remains the
// production implementation until the migration parity gate is complete.
type gormCitationBackfillRunner struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

var _ artifactapp.CitationBackfillPort = (*GORMRepository)(nil)

// BackfillCitationSelectors lets the staged Artifact GORM repository expose
// the additive backfill port without changing its production constructor.
func (repository *GORMRepository) BackfillCitationSelectors(ctx context.Context, limit int) (artifactapp.CitationBackfillResult, bool, error) {
	if repository == nil {
		return artifactapp.CitationBackfillResult{}, false, unavailable(errors.New("artifact citation backfill GORM repository is unavailable"))
	}
	return (&gormCitationBackfillRunner{database: repository.database, unitOfWork: repository.unitOfWork}).backfill(ctx, limit)
}

// BackfillCitationSelectors advances exactly one Workspace marker in one
// bounded transaction. A callback-success/commit-failure never returns an
// in-memory progress result because the durable outcome is unknown.
func (repository *gormCitationBackfillRunner) backfill(ctx context.Context, limit int) (artifactapp.CitationBackfillResult, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return artifactapp.CitationBackfillResult{}, false, err
	}
	if limit < 1 || limit > artifactapp.MaxCitationBackfillRevisionBatch {
		return artifactapp.CitationBackfillResult{}, false, requestInvalid(errors.New("artifact citation backfill limit is invalid"))
	}

	var (
		result            artifactapp.CitationBackfillResult
		found             bool
		durableFailure    error
		callbackSucceeded bool
	)
	err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, transactionErr := platformpostgres.GORMTransaction(scope)
		if transactionErr != nil {
			return unavailable(transactionErr)
		}
		transaction = transaction.WithContext(callbackCtx)
		state, claimed, claimErr := gormClaimCitationBackfillState(callbackCtx, transaction)
		if claimErr != nil {
			return claimErr
		}
		if !claimed {
			callbackSucceeded = true
			return nil
		}
		found = true
		result = artifactapp.CitationBackfillResult{WorkspaceID: state.workspaceID}
		if state.status == "PENDING" || state.status == "FAILED" {
			if resumeErr := gormResumeCitationBackfillState(callbackCtx, transaction, &state); resumeErr != nil {
				return resumeErr
			}
		}

		var processErr error
		if state.processedRevisionCount < state.expectedRevisionCount {
			processErr = gormProcessCitationBackfillBatch(callbackCtx, transaction, state, &result, limit)
		} else {
			processErr = gormValidateCitationBackfillBatch(callbackCtx, transaction, state, &result, limit)
		}
		if processErr == nil {
			callbackSucceeded = true
			return nil
		}
		if !shouldPersistCitationBackfillFailure(processErr) {
			return processErr
		}
		if failErr := gormFailCitationBackfill(callbackCtx, transaction, state, processErr); failErr != nil {
			return failErr
		}
		durableFailure = artifactapp.NewCitationBackfillFailure(processErr)
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		if callbackSucceeded {
			return artifactapp.CitationBackfillResult{}, found, unavailable(fmt.Errorf("artifact citation backfill transaction commit outcome is unknown: %w", classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_COMMIT_FAILED")))
		}
		return artifactapp.CitationBackfillResult{}, found, classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_FAILED")
	}
	if durableFailure != nil {
		return result, true, durableFailure
	}
	return result, found, nil
}

func (repository *gormCitationBackfillRunner) ready(ctx context.Context) error {
	if repository == nil || !validGORMArtifactDatabase(repository.database) || nilGORMArtifactDependency(repository.unitOfWork) {
		return unavailable(errors.New("artifact citation backfill GORM repository is unavailable"))
	}
	if ctx == nil {
		return requestInvalid(errors.New("artifact citation backfill context is nil"))
	}
	return nil
}

func gormClaimCitationBackfillState(ctx context.Context, database *gorm.DB) (citationBackfillState, bool, error) {
	var enabled bool
	gateRow, err := gormArtifactBackfillRow(database.WithContext(ctx), `SELECT EXISTS(
		SELECT 1 FROM core.schema_meta WHERE key='timeline_impact' AND value='m7-v2'
	)`)
	if err != nil {
		return citationBackfillState{}, false, classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_GATE_FAILED")
	}
	if err := gateRow.Scan(&enabled); err != nil {
		return citationBackfillState{}, false, classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_GATE_FAILED")
	}
	if !enabled {
		return citationBackfillState{}, false, unavailable(errors.New("artifact citation selector migration is unavailable"))
	}

	row, err := gormArtifactBackfillRow(database.WithContext(ctx), `SELECT workspace_id::text,
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
	if err != nil {
		return citationBackfillState{}, false, classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_CLAIM_FAILED")
	}
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
		if errors.Is(err, sql.ErrNoRows) {
			return citationBackfillState{}, false, nil
		}
		return citationBackfillState{}, false, classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_CLAIM_FAILED")
	}
	if workspaceID == nil {
		return citationBackfillState{}, true, inconsistent(errors.New("artifact citation backfill marker workspace is null"))
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

func gormResumeCitationBackfillState(ctx context.Context, database *gorm.DB, state *citationBackfillState) error {
	result := database.Exec(`UPDATE learning.artifact_citation_selector_backfill
		SET status='RUNNING',error_code=NULL,updated_at=GREATEST(CURRENT_TIMESTAMP,updated_at),version=version+1
		WHERE workspace_id=? AND status=? AND version=?`, string(state.workspaceID), state.status, state.version)
	if result.Error != nil {
		return classifyGORM(ctx, result.Error, "ARTIFACT_CITATION_BACKFILL_RESUME_FAILED")
	}
	if result.RowsAffected != 1 {
		return inconsistent(errors.New("artifact citation backfill resume CAS did not match"))
	}
	state.status = "RUNNING"
	state.version++
	return nil
}

func gormProcessCitationBackfillBatch(ctx context.Context, database *gorm.DB, state citationBackfillState, result *artifactapp.CitationBackfillResult, limit int) error {
	remaining := state.expectedRevisionCount - state.processedRevisionCount
	if int64(limit) > remaining {
		limit = int(remaining)
	}
	revisions, err := gormLoadCitationBackfillRevisions(ctx, database, state, state.cursorCreatedAt, state.cursorRevisionID, limit)
	if err != nil {
		return err
	}
	if len(revisions) == 0 {
		return inconsistent(errors.New("artifact citation backfill reached the high water mark early"))
	}
	selectorCount, err := gormInsertBackfilledCitationSelectors(ctx, database, state.workspaceID, revisions)
	if err != nil {
		return err
	}
	last := revisions[len(revisions)-1].revision
	progress := database.Exec(`UPDATE learning.artifact_citation_selector_backfill
		SET cursor_created_at=?,cursor_revision_id=?,
		processed_revision_count=processed_revision_count+?,
		expected_selector_count=expected_selector_count+?,
		processed_selector_count=processed_selector_count+?,
		status='RUNNING',error_code=NULL,updated_at=GREATEST(CURRENT_TIMESTAMP,updated_at),version=version+1
		WHERE workspace_id=? AND status='RUNNING' AND version=?`,
		last.CreatedAt.UTC(), string(last.ID), len(revisions), selectorCount, selectorCount, string(state.workspaceID), state.version)
	if progress.Error != nil {
		return classifyGORM(ctx, progress.Error, "ARTIFACT_CITATION_BACKFILL_PROGRESS_FAILED")
	}
	if progress.RowsAffected != 1 {
		return inconsistent(errors.New("artifact citation backfill progress CAS did not match"))
	}
	result.ProcessedRevisions = len(revisions)
	result.ProcessedSelectors = selectorCount
	return nil
}

func gormLoadCitationBackfillRevisions(ctx context.Context, database *gorm.DB, state citationBackfillState, cursorCreatedAt *time.Time, cursorRevisionID *foundation.ID, limit int) ([]citationBackfillRevision, error) {
	var cursorID any
	if cursorRevisionID != nil {
		cursorID = string(*cursorRevisionID)
	}
	rows, err := gormArtifactBackfillRows(database, revisionSelect+`
		WHERE r.workspace_id=? AND r.domain_schema_version=?
		  AND (r.created_at,r.id) <= (?,?)
		  AND (?::timestamptz IS NULL OR (r.created_at,r.id) > (?,?))
		ORDER BY r.created_at,r.id
		LIMIT ?`, string(state.workspaceID), artifactRevisionSchemaVersion, state.highWaterCreatedAt.UTC(), string(*state.highWaterRevisionID), cursorCreatedAt, cursorCreatedAt, cursorID, limit)
	if err != nil {
		return nil, classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_REVISION_QUERY_FAILED")
	}
	revisions := make([]citationBackfillRevision, 0, limit)
	for rows.Next() {
		var persisted persistedRevision
		if err := rows.Scan(persisted.scanTargets()...); err != nil {
			return nil, gormArtifactBackfillCloseRows(rows, classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_REVISION_QUERY_FAILED"))
		}
		revision, _, err := decodePersistedRevision(persisted)
		if err != nil {
			return nil, gormArtifactBackfillCloseRows(rows, err)
		}
		revisions = append(revisions, citationBackfillRevision{revision: revision})
	}
	if err := rows.Err(); err != nil {
		return nil, gormArtifactBackfillCloseRows(rows, classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_REVISION_QUERY_FAILED"))
	}
	if err := gormArtifactBackfillCloseRows(rows, nil); err != nil {
		return nil, err
	}
	domainRevisions := make([]domain.Revision, len(revisions))
	for index := range revisions {
		domainRevisions[index] = revisions[index].revision
	}
	if err := gormValidateRevisionDocumentSourcesBatch(ctx, database, state.workspaceID, domainRevisions); err != nil {
		return nil, err
	}
	return revisions, nil
}

func gormInsertBackfilledCitationSelectors(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, revisions []citationBackfillRevision) (int, error) {
	artifactIDs, revisionIDs, sourceVersionIDs, sourceSpanIDs := make([]string, 0), make([]string, 0), make([]string, 0), make([]string, 0)
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
	if err := database.Exec(`SAVEPOINT artifact_citation_backfill_insert`).Error; err != nil {
		return 0, classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_SAVEPOINT_FAILED")
	}
	insert := database.Exec(`INSERT INTO learning.artifact_revision_citation_selector(
		workspace_id,artifact_id,revision_id,source_version_id,source_span_id
	)
	SELECT ?,selector.artifact_id,selector.revision_id,selector.source_version_id,selector.source_span_id
	FROM unnest(?::uuid[],?::uuid[],?::uuid[],?::uuid[])
		AS selector(artifact_id,revision_id,source_version_id,source_span_id)
	ON CONFLICT (workspace_id,revision_id,source_version_id,source_span_id) DO NOTHING`,
		string(workspaceID), pq.Array(artifactIDs), pq.Array(revisionIDs), pq.Array(sourceVersionIDs), pq.Array(sourceSpanIDs))
	if insert.Error != nil {
		if rollbackErr := database.Exec(`ROLLBACK TO SAVEPOINT artifact_citation_backfill_insert`).Error; rollbackErr != nil {
			return 0, classifyGORM(ctx, rollbackErr, "ARTIFACT_CITATION_BACKFILL_SAVEPOINT_FAILED")
		}
		if releaseErr := database.Exec(`RELEASE SAVEPOINT artifact_citation_backfill_insert`).Error; releaseErr != nil {
			return 0, classifyGORM(ctx, releaseErr, "ARTIFACT_CITATION_BACKFILL_SAVEPOINT_FAILED")
		}
		if citationBackfillDataError(insert.Error) {
			return 0, inconsistent(insert.Error)
		}
		return 0, classifyGORM(ctx, insert.Error, "ARTIFACT_CITATION_BACKFILL_INSERT_FAILED")
	}
	if err := database.Exec(`RELEASE SAVEPOINT artifact_citation_backfill_insert`).Error; err != nil {
		return 0, classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_SAVEPOINT_FAILED")
	}
	return len(revisionIDs), nil
}

func gormValidateCitationBackfillBatch(ctx context.Context, database *gorm.DB, state citationBackfillState, result *artifactapp.CitationBackfillResult, limit int) error {
	remaining := state.expectedRevisionCount - state.validatedRevisionCount
	if int64(limit) > remaining {
		limit = int(remaining)
	}
	if limit < 1 {
		return inconsistent(errors.New("artifact citation backfill validation counts are inconsistent"))
	}
	revisions, err := gormLoadCitationBackfillRevisions(ctx, database, state, state.validationCursorCreatedAt, state.validationCursorRevisionID, limit)
	if err != nil {
		return err
	}
	if len(revisions) == 0 {
		return inconsistent(errors.New("artifact citation backfill validation reached the high water mark early"))
	}
	validatedSelectors, err := gormValidateBackfilledCitationSelectors(ctx, database, state.workspaceID, revisions)
	if err != nil {
		return err
	}
	validatedRevisionCount := state.validatedRevisionCount + int64(len(revisions))
	validatedSelectorCount := state.validatedSelectorCount + int64(validatedSelectors)
	completed := validatedRevisionCount == state.expectedRevisionCount && validatedSelectorCount == state.expectedSelectorCount
	status := "RUNNING"
	if completed {
		status = "COMPLETED"
	} else if validatedRevisionCount == state.expectedRevisionCount {
		return inconsistent(errors.New("artifact citation backfill selector totals are inconsistent"))
	}
	last := revisions[len(revisions)-1].revision
	update := database.Exec(`UPDATE learning.artifact_citation_selector_backfill
		SET validation_cursor_created_at=?,validation_cursor_revision_id=?,
		validated_revision_count=?,validated_selector_count=?,
		status=?,error_code=NULL,
		completed_at=CASE WHEN ?='COMPLETED' THEN CURRENT_TIMESTAMP ELSE NULL END,
		updated_at=GREATEST(CURRENT_TIMESTAMP,updated_at),version=version+1
		WHERE workspace_id=? AND status='RUNNING' AND version=?`,
		last.CreatedAt.UTC(), string(last.ID), validatedRevisionCount, validatedSelectorCount,
		status, status, string(state.workspaceID), state.version)
	if update.Error != nil {
		return classifyGORM(ctx, update.Error, "ARTIFACT_CITATION_BACKFILL_VALIDATION_PROGRESS_FAILED")
	}
	if update.RowsAffected != 1 {
		return inconsistent(errors.New("artifact citation backfill validation CAS did not match"))
	}
	result.ValidatedRevisions = len(revisions)
	result.Completed = completed
	return nil
}

func gormValidateBackfilledCitationSelectors(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, revisions []citationBackfillRevision) (int, error) {
	domainRevisions := make([]domain.Revision, len(revisions))
	for index, item := range revisions {
		domainRevisions[index] = item.revision
	}
	expected := make(map[string]struct{})
	revisionIDs := make([]string, len(domainRevisions))
	for index, revision := range domainRevisions {
		revisionIDs[index] = string(revision.ID)
		for _, selector := range revisionCitationSelectors(revision) {
			expected[citationSelectorIdentity(revision.ArtifactID, revision.ID, selector.sourceVersionID, selector.sourceSpanID)] = struct{}{}
		}
	}
	rows, err := gormArtifactBackfillRows(database, `SELECT artifact_id::text,revision_id::text,source_version_id::text,source_span_id::text
		FROM learning.artifact_revision_citation_selector
		WHERE workspace_id=? AND revision_id=ANY(?::uuid[])
		ORDER BY artifact_id,revision_id,source_version_id,source_span_id`, string(workspaceID), pq.Array(revisionIDs))
	if err != nil {
		return 0, classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_VALIDATION_QUERY_FAILED")
	}
	actual := make(map[string]struct{}, len(expected))
	for rows.Next() {
		var artifactID, revisionID, sourceVersionID, sourceSpanID string
		if err := rows.Scan(&artifactID, &revisionID, &sourceVersionID, &sourceSpanID); err != nil {
			return 0, gormArtifactBackfillCloseRows(rows, classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_VALIDATION_QUERY_FAILED"))
		}
		key := citationSelectorIdentity(foundation.ID(artifactID), foundation.ID(revisionID), foundation.ID(sourceVersionID), foundation.ID(sourceSpanID))
		if _, duplicate := actual[key]; duplicate {
			return 0, gormArtifactBackfillCloseRows(rows, inconsistent(errors.New("artifact citation selector validation found a duplicate")))
		}
		actual[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return 0, gormArtifactBackfillCloseRows(rows, classifyGORM(ctx, err, "ARTIFACT_CITATION_BACKFILL_VALIDATION_QUERY_FAILED"))
	}
	if len(actual) != len(expected) {
		return 0, gormArtifactBackfillCloseRows(rows, inconsistent(fmt.Errorf("artifact citation selector validation count mismatch: expected %d, got %d", len(expected), len(actual))))
	}
	for key := range expected {
		if _, found := actual[key]; !found {
			return 0, gormArtifactBackfillCloseRows(rows, inconsistent(errors.New("artifact citation selector validation found a binding mismatch")))
		}
	}
	return len(expected), gormArtifactBackfillCloseRows(rows, nil)
}

func gormFailCitationBackfill(ctx context.Context, database *gorm.DB, state citationBackfillState, _ error) error {
	update := database.Exec(`UPDATE learning.artifact_citation_selector_backfill
		SET status='FAILED',error_code=?,completed_at=NULL,
		updated_at=GREATEST(CURRENT_TIMESTAMP,updated_at),version=version+1
		WHERE workspace_id=? AND status='RUNNING' AND version=?`,
		artifactapp.ErrorCodeCitationBackfillFailed, string(state.workspaceID), state.version)
	if update.Error != nil {
		return classifyGORM(ctx, update.Error, "ARTIFACT_CITATION_BACKFILL_FAILURE_MARKER_FAILED")
	}
	if update.RowsAffected != 1 {
		return inconsistent(errors.New("artifact citation backfill failure CAS did not match"))
	}
	return nil
}

func gormArtifactBackfillRow(database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	return gormRow(database, query, arguments...)
}

func gormArtifactBackfillRows(database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	statement := database.Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("artifact citation backfill query returned nil rows")
	}
	return rows, nil
}

func gormArtifactBackfillCloseRows(rows *sql.Rows, cause error) error {
	if rows == nil {
		return cause
	}
	if closeErr := rows.Close(); closeErr != nil {
		return errors.Join(cause, classify(closeErr))
	}
	return cause
}
