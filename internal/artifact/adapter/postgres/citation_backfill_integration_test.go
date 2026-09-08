//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

func TestArtifactRevisionWritesCitationSelectorsInOwnerTransaction(t *testing.T) {
	ctx := context.Background()
	repository, platformPool := newArtifactIntegrationGORMRepository(t, ctx)
	pool := platformPool.DB()
	workspaceID := artifactIntegrationID(3001)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-selector-owner")
	provenance := seedArtifactCitationProvenance(t, ctx, pool, workspaceID, 3010)
	revision := insertArtifactCitationRevision(t, ctx, repository, workspaceID, 3020, provenance, time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC))

	var artifactID, sourceVersionID, sourceSpanID string
	if err := pool.QueryRow(ctx, `SELECT artifact_id::text,source_version_id::text,source_span_id::text
		FROM learning.artifact_revision_citation_selector
		WHERE workspace_id=$1 AND revision_id=$2`, string(workspaceID), string(revision.ID)).Scan(
		&artifactID, &sourceVersionID, &sourceSpanID,
	); err != nil {
		t.Fatal(err)
	}
	if artifactID != string(revision.ArtifactID) || sourceVersionID != string(provenance.sourceVersionID) || sourceSpanID != string(provenance.sourceSpanID) {
		t.Fatalf("selector artifact=%s source_version=%s source_span=%s", artifactID, sourceVersionID, sourceSpanID)
	}
	if err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB) error {
		return gormInsertRevisionSelectors(callbackCtx, tx, workspaceID, revision)
	}, "ARTIFACT_CITATION_SELECTOR_WRITE_FAILED"); err != nil {
		t.Fatalf("replay exact owner selector write: %v", err)
	}

	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `ALTER TABLE learning.artifact_revision_citation_selector DISABLE TRIGGER artifact_citation_selector_append_only`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM learning.artifact_revision_citation_selector WHERE workspace_id=$1 AND revision_id=$2`, string(workspaceID), string(revision.ID)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `ALTER TABLE learning.artifact_revision_citation_selector ENABLE TRIGGER artifact_citation_selector_append_only`)
		return err
	}); err != nil {
		t.Fatalf("remove selector for owner repair: %v", err)
	}
	if err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB) error {
		return gormInsertRevisionSelectors(callbackCtx, tx, workspaceID, revision)
	}, "ARTIFACT_CITATION_SELECTOR_WRITE_FAILED"); err != nil {
		t.Fatalf("repair missing owner selector: %v", err)
	}
	var repairedSelectors int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_revision_citation_selector
		WHERE workspace_id=$1 AND artifact_id=$2 AND revision_id=$3 AND source_version_id=$4 AND source_span_id=$5`,
		string(workspaceID), string(revision.ArtifactID), string(revision.ID), string(provenance.sourceVersionID), string(provenance.sourceSpanID),
	).Scan(&repairedSelectors); err != nil {
		t.Fatal(err)
	}
	if repairedSelectors != 1 {
		t.Fatalf("repaired selector count=%d want 1", repairedSelectors)
	}

	driftedRevision := revision
	driftedRevision.ArtifactID = artifactIntegrationID(3099)
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB) error {
		return gormInsertRevisionSelectors(callbackCtx, tx, workspaceID, driftedRevision)
	}, "ARTIFACT_CITATION_SELECTOR_WRITE_FAILED")
	if !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeResultInconsistent) {
		t.Fatalf("drifted owner binding error=%v", err)
	}

	extraProvenance := seedArtifactCitationProvenance(t, ctx, pool, workspaceID, 3050)
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_revision_citation_selector(
		workspace_id,artifact_id,revision_id,source_version_id,source_span_id
	) VALUES($1,$2,$3,$4,$5)`, string(workspaceID), string(revision.ArtifactID), string(revision.ID),
		string(extraProvenance.sourceVersionID), string(extraProvenance.sourceSpanID)); err != nil {
		t.Fatal(err)
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB) error {
		return gormInsertRevisionSelectors(callbackCtx, tx, workspaceID, revision)
	}, "ARTIFACT_CITATION_SELECTOR_WRITE_FAILED")
	if !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeResultInconsistent) {
		t.Fatalf("extra owner selector error=%v", err)
	}

	var markerStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM learning.artifact_citation_selector_backfill WHERE workspace_id=$1`, string(workspaceID)).Scan(&markerStatus); err != nil {
		t.Fatal(err)
	}
	if markerStatus != "COMPLETED" {
		t.Fatalf("new workspace marker status=%s", markerStatus)
	}
	if _, found, err := repository.BackfillCitationSelectors(ctx, 10); err != nil || found {
		t.Fatalf("completed workspace backfill found=%t err=%v", found, err)
	}
}

func TestGORMCitationBackfillPostgreSQLResumesAndValidatesSelectors(t *testing.T) {
	ctx := context.Background()
	repository, platformPool := newArtifactIntegrationGORMRepository(t, ctx)
	pool := platformPool.DB()
	cancelCause := errors.New("artifact citation backfill canceled by caller")
	canceledCtx, cancel := context.WithCancelCause(ctx)
	cancel(cancelCause)
	if canceled, found, err := repository.BackfillCitationSelectors(canceledCtx, 1); found || canceled != (artifactapp.CitationBackfillResult{}) ||
		!errors.Is(err, context.Canceled) || !errors.Is(err, cancelCause) {
		t.Fatalf("gorm canceled backfill result=%#v found=%t err=%v", canceled, found, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.schema_meta SET value='m7-04',updated_at=now() WHERE key='timeline_impact'`); err != nil {
		t.Fatal(err)
	}
	gateRestored := false
	t.Cleanup(func() {
		if !gateRestored {
			if _, cleanupErr := pool.Exec(context.Background(), `UPDATE core.schema_meta SET value='m7-v2',updated_at=now() WHERE key='timeline_impact'`); cleanupErr != nil {
				t.Errorf("restore citation backfill feature gate: %v", cleanupErr)
			}
		}
	})
	blocked, found, err := repository.BackfillCitationSelectors(ctx, 1)
	var classified *foundation.Error
	if found || blocked != (artifactapp.CitationBackfillResult{}) || !errors.As(err, &classified) ||
		classified.Kind != foundation.ErrorDependencyUnavailable || classified.Code != artifactapp.ErrorCodeDependencyUnavailable || !classified.Retryable {
		t.Fatalf("gorm disabled gate result=%#v found=%t err=%v", blocked, found, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.schema_meta SET value='m7-v2',updated_at=now() WHERE key='timeline_impact'`); err != nil {
		t.Fatal(err)
	}
	gateRestored = true

	workspaceID := artifactIntegrationID(3901)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-gorm-backfill")
	provenance := seedArtifactCitationProvenance(t, ctx, pool, workspaceID, 3910)
	revision := insertArtifactCitationRevision(t, ctx, repository, workspaceID, 3920, provenance, time.Now().UTC().Truncate(time.Microsecond))
	if _, err := pool.Exec(ctx, `ALTER TABLE learning.artifact_revision_citation_selector DISABLE TRIGGER artifact_citation_selector_append_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM learning.artifact_revision_citation_selector WHERE workspace_id=$1 AND revision_id=$2`, string(workspaceID), string(revision.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE learning.artifact_revision_citation_selector ENABLE TRIGGER artifact_citation_selector_append_only`); err != nil {
		t.Fatal(err)
	}
	resetArtifactCitationBackfillMarker(t, ctx, pool, workspaceID, revision)
	progress, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if err != nil || !found || progress.WorkspaceID != workspaceID || progress.ProcessedRevisions != 1 || progress.ProcessedSelectors != 1 {
		t.Fatalf("gorm backfill progress=%#v found=%t err=%v", progress, found, err)
	}
	validated, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if err != nil || !found || !validated.Completed || validated.ValidatedRevisions != 1 {
		t.Fatalf("gorm backfill validation=%#v found=%t err=%v", validated, found, err)
	}
	var selectors int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_revision_citation_selector WHERE workspace_id=$1 AND revision_id=$2`, string(workspaceID), string(revision.ID)).Scan(&selectors); err != nil || selectors != 1 {
		t.Fatalf("gorm selector count=%d err=%v", selectors, err)
	}
}

func TestGORMCitationBackfillPostgreSQLSkipsLockedMarkerAndResumesSavepointFailure(t *testing.T) {
	ctx := context.Background()
	repository, platformPool := newArtifactIntegrationGORMRepository(t, ctx)
	pool := platformPool.DB()
	damagedWorkspaceID := artifactIntegrationID(3951)
	healthyWorkspaceID := artifactIntegrationID(3952)
	seedArtifactWorkspace(t, ctx, pool, damagedWorkspaceID, "artifact-gorm-backfill-damaged")
	seedArtifactWorkspace(t, ctx, pool, healthyWorkspaceID, "artifact-gorm-backfill-healthy")
	damagedProvenance := seedArtifactCitationProvenance(t, ctx, pool, damagedWorkspaceID, 3960)
	healthyProvenance := seedArtifactCitationProvenance(t, ctx, pool, healthyWorkspaceID, 3970)
	if _, err := pool.Exec(ctx, `UPDATE core.schema_meta SET value='m7-04',updated_at=now() WHERE key='timeline_impact'`); err != nil {
		t.Fatal(err)
	}
	setArtifactRevisionCitationProjection(t, ctx, pool, false)
	firstRevision := insertArtifactCitationRevision(t, ctx, repository, damagedWorkspaceID, 3980, damagedProvenance, time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC))
	damagedRevision := insertArtifactCitationRevision(t, ctx, repository, damagedWorkspaceID, 3990, damagedProvenance, time.Date(2026, 7, 27, 14, 1, 0, 0, time.UTC))
	healthyRevision := insertArtifactCitationRevision(t, ctx, repository, healthyWorkspaceID, 4000, healthyProvenance, time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC))
	setArtifactRevisionCitationProjection(t, ctx, pool, true)
	if _, err := pool.Exec(ctx, `UPDATE core.schema_meta SET value='m7-v2',updated_at=now() WHERE key='timeline_impact'`); err != nil {
		t.Fatal(err)
	}
	resetArtifactCitationBackfillMarker(t, ctx, pool, damagedWorkspaceID, firstRevision, damagedRevision)
	resetArtifactCitationBackfillMarker(t, ctx, pool, healthyWorkspaceID, healthyRevision)

	markerLock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_ = markerLock.Rollback(context.Background())
		}
	}()
	if _, err := markerLock.Exec(ctx, `SELECT workspace_id FROM learning.artifact_citation_selector_backfill WHERE workspace_id=$1 FOR UPDATE`, string(damagedWorkspaceID)); err != nil {
		t.Fatal(err)
	}
	healthyProcessed, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if err != nil || !found || healthyProcessed.WorkspaceID != healthyWorkspaceID || healthyProcessed.ProcessedRevisions != 1 {
		t.Fatalf("GORM skip-locked process=%#v found=%t err=%v", healthyProcessed, found, err)
	}
	healthyCompleted, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if err != nil || !found || healthyCompleted.WorkspaceID != healthyWorkspaceID || !healthyCompleted.Completed || healthyCompleted.ValidatedRevisions != 1 {
		t.Fatalf("GORM skip-locked validation=%#v found=%t err=%v", healthyCompleted, found, err)
	}
	if err := markerLock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	locked = false

	firstBatch, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if err != nil || !found || firstBatch.WorkspaceID != damagedWorkspaceID || firstBatch.ProcessedRevisions != 1 || firstBatch.ProcessedSelectors != 1 {
		t.Fatalf("GORM damaged first batch=%#v found=%t err=%v", firstBatch, found, err)
	}
	var originalSections string
	if err := pool.QueryRow(ctx, `SELECT sections::text FROM learning.artifact_revision WHERE workspace_id=$1 AND id=$2`, string(damagedWorkspaceID), string(damagedRevision.ID)).Scan(&originalSections); err != nil {
		t.Fatal(err)
	}
	missingProvenance := artifactdomain.CloneRevision(damagedRevision)
	missingProvenance.Sections[0].Citations[0].SourceVersionID = artifactIntegrationID(4010)
	missingProvenance.Sections[0].Citations[0].SourceSpanID = artifactIntegrationID(4011)
	missingHash, err := artifactdomain.ComputeRevisionContentHash(missingProvenance)
	if err != nil {
		t.Fatal(err)
	}
	replaceArtifactRevisionPayload(t, ctx, pool, damagedWorkspaceID, damagedRevision.ID, string(marshalJSON(missingProvenance.Sections)), missingHash)
	failed, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if !found || failed.WorkspaceID != damagedWorkspaceID || !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeCitationBackfillFailed) {
		t.Fatalf("GORM damaged failure=%#v found=%t err=%v", failed, found, err)
	}
	artifactIntegrationPostgresCode(t, err, "23514")
	var status, errorCode, cursorRevisionID string
	var cursorCreatedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT status,error_code,cursor_created_at,cursor_revision_id::text
		FROM learning.artifact_citation_selector_backfill WHERE workspace_id=$1`, string(damagedWorkspaceID)).Scan(
		&status, &errorCode, &cursorCreatedAt, &cursorRevisionID,
	); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || errorCode != artifactapp.ErrorCodeCitationBackfillFailed ||
		!cursorCreatedAt.Equal(firstRevision.CreatedAt) || cursorRevisionID != string(firstRevision.ID) {
		t.Fatalf("GORM failed marker status=%s error=%s cursor=(%s,%s)", status, errorCode, cursorCreatedAt, cursorRevisionID)
	}
	replaceArtifactRevisionPayload(t, ctx, pool, damagedWorkspaceID, damagedRevision.ID, originalSections, damagedRevision.ContentHash)
	resumed, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if err != nil || !found || resumed.WorkspaceID != damagedWorkspaceID || resumed.ProcessedRevisions != 1 || resumed.ProcessedSelectors != 1 {
		t.Fatalf("GORM resumed batch=%#v found=%t err=%v", resumed, found, err)
	}
	completed, found, err := repository.BackfillCitationSelectors(ctx, 2)
	if err != nil || !found || completed.WorkspaceID != damagedWorkspaceID || !completed.Completed || completed.ValidatedRevisions != 2 {
		t.Fatalf("GORM completed damaged=%#v found=%t err=%v", completed, found, err)
	}
	if _, found, err := repository.BackfillCitationSelectors(ctx, 2); err != nil || found {
		t.Fatalf("GORM completed queue found=%t err=%v", found, err)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("GORM backfill retained %d PostgreSQL connections", acquired)
	}
}

func TestArtifactCitationBackfillPersistsFailureAndResumesExactValidation(t *testing.T) {
	ctx := context.Background()
	repository, platformPool := newArtifactIntegrationGORMRepository(t, ctx)
	pool := platformPool.DB()
	workspaceID := artifactIntegrationID(3101)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-selector-backfill")
	provenance := seedArtifactCitationProvenance(t, ctx, pool, workspaceID, 3110)

	if _, err := pool.Exec(ctx, `UPDATE core.schema_meta SET value='m7-04',updated_at=now() WHERE key='timeline_impact'`); err != nil {
		t.Fatal(err)
	}
	setArtifactRevisionCitationProjection(t, ctx, pool, false)
	revision := insertArtifactCitationRevision(t, ctx, repository, workspaceID, 3120, provenance, time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC))
	setArtifactRevisionCitationProjection(t, ctx, pool, true)
	if _, err := pool.Exec(ctx, `UPDATE core.schema_meta SET value='m7-v2',updated_at=now() WHERE key='timeline_impact'`); err != nil {
		t.Fatal(err)
	}
	var selectors int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_revision_citation_selector WHERE revision_id=$1`, string(revision.ID)).Scan(&selectors); err != nil {
		t.Fatal(err)
	}
	if selectors != 0 {
		t.Fatalf("historical revision selectors=%d before backfill", selectors)
	}

	resetArtifactCitationBackfillMarker(t, ctx, pool, workspaceID, revision)
	processed, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if err != nil || !found || processed.ProcessedRevisions != 1 || processed.ProcessedSelectors != 1 || processed.Completed {
		t.Fatalf("processed=%+v found=%t err=%v", processed, found, err)
	}

	if _, err := pool.Exec(ctx, `ALTER TABLE learning.artifact_revision_citation_selector DISABLE TRIGGER artifact_citation_selector_append_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM learning.artifact_revision_citation_selector WHERE revision_id=$1`, string(revision.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE learning.artifact_revision_citation_selector ENABLE TRIGGER artifact_citation_selector_append_only`); err != nil {
		t.Fatal(err)
	}
	failed, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if !found || failed.WorkspaceID != workspaceID || !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeCitationBackfillFailed) {
		t.Fatalf("failed=%+v found=%t err=%v", failed, found, err)
	}
	var status, errorCode string
	if err := pool.QueryRow(ctx, `SELECT status,error_code FROM learning.artifact_citation_selector_backfill WHERE workspace_id=$1`, string(workspaceID)).Scan(&status, &errorCode); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || errorCode != artifactapp.ErrorCodeCitationBackfillFailed {
		t.Fatalf("failed marker status=%s error_code=%s", status, errorCode)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_revision_citation_selector(
		workspace_id,artifact_id,revision_id,source_version_id,source_span_id
	) VALUES($1,$2,$3,$4,$5)`, string(workspaceID), string(revision.ArtifactID), string(revision.ID),
		string(provenance.sourceVersionID), string(provenance.sourceSpanID)); err != nil {
		t.Fatal(err)
	}
	validated, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if err != nil || !found || !validated.Completed || validated.ValidatedRevisions != 1 {
		t.Fatalf("validated=%+v found=%t err=%v cause=%v", validated, found, err, errors.Unwrap(err))
	}
	var expectedRevisions, processedRevisions, expectedSelectors, processedSelectors, validatedRevisions, validatedSelectors int64
	if err := pool.QueryRow(ctx, `SELECT status,expected_revision_count,processed_revision_count,
		expected_selector_count,processed_selector_count,validated_revision_count,validated_selector_count
		FROM learning.artifact_citation_selector_backfill WHERE workspace_id=$1`, string(workspaceID)).Scan(
		&status, &expectedRevisions, &processedRevisions, &expectedSelectors, &processedSelectors, &validatedRevisions, &validatedSelectors,
	); err != nil {
		t.Fatal(err)
	}
	if status != "COMPLETED" || expectedRevisions != 1 || processedRevisions != 1 || expectedSelectors != 1 || processedSelectors != 1 || validatedRevisions != 1 || validatedSelectors != 1 {
		t.Fatalf("completed marker status=%s revisions=%d/%d selectors=%d/%d validated=%d/%d",
			status, processedRevisions, expectedRevisions, processedSelectors, expectedSelectors, validatedRevisions, validatedSelectors)
	}

	damagedWorkspaceID := artifactIntegrationID(3201)
	healthyWorkspaceID := artifactIntegrationID(3301)
	seedArtifactWorkspace(t, ctx, pool, damagedWorkspaceID, "artifact-selector-damaged-backfill")
	seedArtifactWorkspace(t, ctx, pool, healthyWorkspaceID, "artifact-selector-healthy-backfill")
	damagedProvenance := seedArtifactCitationProvenance(t, ctx, pool, damagedWorkspaceID, 3210)
	healthyProvenance := seedArtifactCitationProvenance(t, ctx, pool, healthyWorkspaceID, 3310)

	if _, err := pool.Exec(ctx, `UPDATE core.schema_meta SET value='m7-04',updated_at=now() WHERE key='timeline_impact'`); err != nil {
		t.Fatal(err)
	}
	setArtifactRevisionCitationProjection(t, ctx, pool, false)
	firstRevision := insertArtifactCitationRevision(t, ctx, repository, damagedWorkspaceID, 3220, damagedProvenance, time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC))
	damagedRevision := insertArtifactCitationRevision(t, ctx, repository, damagedWorkspaceID, 3230, damagedProvenance, time.Date(2026, 7, 27, 12, 1, 0, 0, time.UTC))
	healthyRevision := insertArtifactCitationRevision(t, ctx, repository, healthyWorkspaceID, 3320, healthyProvenance, time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC))
	setArtifactRevisionCitationProjection(t, ctx, pool, true)
	if _, err := pool.Exec(ctx, `UPDATE core.schema_meta SET value='m7-v2',updated_at=now() WHERE key='timeline_impact'`); err != nil {
		t.Fatal(err)
	}

	resetArtifactCitationBackfillMarker(t, ctx, pool, damagedWorkspaceID, firstRevision, damagedRevision)
	resetArtifactCitationBackfillMarker(t, ctx, pool, healthyWorkspaceID, healthyRevision)
	lockedRevisionTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockedRevisionTx.Rollback(context.Background()) }()
	if _, err := lockedRevisionTx.Exec(ctx, `SELECT id FROM learning.artifact_revision WHERE workspace_id=$1 AND id=$2 FOR KEY SHARE`,
		string(damagedWorkspaceID), string(firstRevision.ID)); err != nil {
		t.Fatal(err)
	}
	firstBatch, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if err != nil || !found || firstBatch.WorkspaceID != damagedWorkspaceID || firstBatch.ProcessedRevisions != 1 || firstBatch.ProcessedSelectors != 1 {
		t.Fatalf("first damaged workspace batch=%+v found=%t err=%v", firstBatch, found, err)
	}
	if err := lockedRevisionTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	var originalSections string
	if err := pool.QueryRow(ctx, `SELECT sections::text FROM learning.artifact_revision WHERE workspace_id=$1 AND id=$2`,
		string(damagedWorkspaceID), string(damagedRevision.ID)).Scan(&originalSections); err != nil {
		t.Fatal(err)
	}
	replaceArtifactRevisionPayload(t, ctx, pool, damagedWorkspaceID, damagedRevision.ID, `[{"invalid":"sections"}]`, damagedRevision.ContentHash)
	failed, found, err = repository.BackfillCitationSelectors(ctx, 1)
	if !found || failed.WorkspaceID != damagedWorkspaceID || !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeCitationBackfillFailed) {
		t.Fatalf("damaged revision failure=%+v found=%t err=%v", failed, found, err)
	}
	var cursorCreatedAt time.Time
	var cursorRevisionID string
	if err := pool.QueryRow(ctx, `SELECT status,error_code,cursor_created_at,cursor_revision_id::text
		FROM learning.artifact_citation_selector_backfill WHERE workspace_id=$1`, string(damagedWorkspaceID)).Scan(
		&status, &errorCode, &cursorCreatedAt, &cursorRevisionID,
	); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || errorCode != artifactapp.ErrorCodeCitationBackfillFailed ||
		!cursorCreatedAt.Equal(firstRevision.CreatedAt) || cursorRevisionID != string(firstRevision.ID) {
		t.Fatalf("damaged marker status=%s error=%s cursor=(%s,%s) want=(%s,%s)",
			status, errorCode, cursorCreatedAt, cursorRevisionID, firstRevision.CreatedAt, firstRevision.ID)
	}

	healthyBatch, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if err != nil || !found || healthyBatch.WorkspaceID != healthyWorkspaceID || healthyBatch.ProcessedRevisions != 1 || healthyBatch.ProcessedSelectors != 1 {
		t.Fatalf("healthy workspace batch=%+v found=%t err=%v", healthyBatch, found, err)
	}
	healthyValidation, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if err != nil || !found || healthyValidation.WorkspaceID != healthyWorkspaceID || !healthyValidation.Completed || healthyValidation.ValidatedRevisions != 1 {
		t.Fatalf("healthy workspace validation=%+v found=%t err=%v", healthyValidation, found, err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM learning.artifact_citation_selector_backfill WHERE workspace_id=$1`, string(healthyWorkspaceID)).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "COMPLETED" {
		t.Fatalf("healthy marker status=%s", status)
	}

	replaceArtifactRevisionPayload(t, ctx, pool, damagedWorkspaceID, damagedRevision.ID, originalSections, damagedRevision.ContentHash)
	resumed, found, err := repository.BackfillCitationSelectors(ctx, 1)
	if err != nil || !found || resumed.WorkspaceID != damagedWorkspaceID || resumed.ProcessedRevisions != 1 || resumed.ProcessedSelectors != 1 {
		t.Fatalf("resumed damaged workspace=%+v found=%t err=%v", resumed, found, err)
	}
	completed, found, err := repository.BackfillCitationSelectors(ctx, 2)
	if err != nil || !found || completed.WorkspaceID != damagedWorkspaceID || !completed.Completed || completed.ValidatedRevisions != 2 {
		t.Fatalf("completed damaged workspace=%+v found=%t err=%v", completed, found, err)
	}
	if err := pool.QueryRow(ctx, `SELECT status,processed_revision_count,validated_revision_count,cursor_created_at,cursor_revision_id::text
		FROM learning.artifact_citation_selector_backfill WHERE workspace_id=$1`, string(damagedWorkspaceID)).Scan(
		&status, &processedRevisions, &validatedRevisions, &cursorCreatedAt, &cursorRevisionID,
	); err != nil {
		t.Fatal(err)
	}
	if status != "COMPLETED" || processedRevisions != 2 || validatedRevisions != 2 ||
		!cursorCreatedAt.Equal(damagedRevision.CreatedAt) || cursorRevisionID != string(damagedRevision.ID) {
		t.Fatalf("recovered marker status=%s processed=%d validated=%d cursor=(%s,%s)",
			status, processedRevisions, validatedRevisions, cursorCreatedAt, cursorRevisionID)
	}
	if _, found, err := repository.BackfillCitationSelectors(ctx, 2); err != nil || found {
		t.Fatalf("all workspaces completed found=%t err=%v", found, err)
	}
}

type artifactCitationProvenance struct {
	sourceVersionID foundation.ID
	sourceSpanID    foundation.ID
	contentHash     string
}

func seedArtifactCitationProvenance(t *testing.T, ctx context.Context, db *pgxpool.Pool, workspaceID foundation.ID, base int) artifactCitationProvenance {
	t.Helper()
	contentArtifactID := artifactIntegrationID(base)
	sourceID := artifactIntegrationID(base + 1)
	sourceVersionID := artifactIntegrationID(base + 2)
	projectionID := artifactIntegrationID(base + 3)
	sourceSpanID := artifactIntegrationID(base + 4)
	contentHash := artifactIntegrationHash(byte('a' + base%6))
	sourceLocation := "artifact-selector-" + string(sourceID) + ".txt"
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,4,$4,$5)`, []any{string(contentArtifactID), string(workspaceID), contentHash, ".knowledge/sources/" + contentHash, now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'text',$3,$3,$4)`, []any{string(sourceID), string(workspaceID), sourceLocation, now}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES($1,$2,$3,$4,$5,4,'text/plain',$6,'passed',$7)`, []any{string(sourceVersionID), string(sourceID), string(workspaceID), string(contentArtifactID), contentHash, sourceLocation, now}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at) VALUES($1,$2,$3,'text','v1',$4,'v1',$5,'[]',$6)`, []any{string(projectionID), string(workspaceID), string(contentArtifactID), artifactIntegrationHash('d'), artifactIntegrationHash('e'), now}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES($1,$2,$3,$4,'paragraph',1,1,0,4,'{}',$5,'v1','v1',$6)`, []any{string(sourceSpanID), string(workspaceID), string(contentArtifactID), string(projectionID), artifactIntegrationHash('f'), now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES($1,$2,$3,$4)`, []any{string(sourceVersionID), string(projectionID), string(workspaceID), now}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return artifactCitationProvenance{sourceVersionID: sourceVersionID, sourceSpanID: sourceSpanID, contentHash: contentHash}
}

func insertArtifactCitationRevision(t *testing.T, ctx context.Context, repository *GORMRepository, workspaceID foundation.ID, base int, provenance artifactCitationProvenance, createdAt time.Time) artifactdomain.Revision {
	t.Helper()
	revision := artifactdomain.Revision{
		ID: artifactIntegrationID(base + 1), ArtifactID: artifactIntegrationID(base), RevisionNo: 1,
		Outline: []artifactdomain.OutlineSection{{Key: "scope", Title: "Scope"}},
		Sections: []artifactdomain.Section{{
			Key: "scope", Title: "Scope", Content: "Verified selector evidence.",
			Citations: []artifactdomain.Citation{{
				SourceVersionID: provenance.sourceVersionID, SourceSpanID: provenance.sourceSpanID,
				VerifiedContentHash: provenance.contentHash, Excerpt: "Verified selector evidence.", Verified: true,
			}},
			Coverage: artifactdomain.Coverage{SectionKey: "scope", Status: artifactdomain.CoverageCovered, Gaps: []artifactdomain.Gap{}},
		}},
		CreatedBy: artifactdomain.CreatorHuman, CreatedAt: createdAt,
	}
	hash, err := artifactdomain.ComputeRevisionContentHash(revision)
	if err != nil {
		t.Fatal(err)
	}
	revision.ContentHash = hash
	if err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := tx.WithContext(callbackCtx).Exec(`INSERT INTO learning.artifact(
			id,workspace_id,artifact_type,title,scope,scope_definition,source_coverage,current_revision_id,
			status,version,domain_schema_version,created_at,updated_at
		) VALUES(?,?,'study-guide','Selector Artifact','{}','selector test',?::jsonb,?,
			'DRAFT',1,'artifact/v1',?,?)`, string(revision.ArtifactID), string(workspaceID),
			string(marshalJSON(coverageFromSections(revision.Sections))), string(revision.ID), createdAt.UTC(), createdAt.UTC()).Error; err != nil {
			return err
		}
		return gormInsertRevision(callbackCtx, tx, workspaceID, revision)
	}, "ARTIFACT_CITATION_REVISION_INSERT_FAILED"); err != nil {
		t.Fatalf("insert Artifact citation Revision: %v (cause: %v)", err, errors.Unwrap(err))
	}
	return revision
}

func resetArtifactCitationBackfillMarker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, revisions ...artifactdomain.Revision) {
	t.Helper()
	if len(revisions) == 0 {
		t.Fatal("reset Artifact citation backfill marker requires at least one Revision")
	}
	highWater := revisions[0]
	createdAt := revisions[0].CreatedAt
	createdID := revisions[0].ID
	for _, revision := range revisions[1:] {
		if revision.CreatedAt.Before(createdAt) || revision.CreatedAt.Equal(createdAt) && string(revision.ID) < string(createdID) {
			createdAt = revision.CreatedAt
			createdID = revision.ID
		}
		if revision.CreatedAt.After(highWater.CreatedAt) || revision.CreatedAt.Equal(highWater.CreatedAt) && string(revision.ID) > string(highWater.ID) {
			highWater = revision
		}
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE learning.artifact_citation_selector_backfill DISABLE TRIGGER artifact_citation_selector_backfill_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM learning.artifact_citation_selector_backfill WHERE workspace_id=$1`, string(workspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE learning.artifact_citation_selector_backfill ENABLE TRIGGER artifact_citation_selector_backfill_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_citation_selector_backfill(
		workspace_id,high_water_created_at,high_water_revision_id,
		expected_revision_count,processed_revision_count,expected_selector_count,processed_selector_count,
		validated_revision_count,validated_selector_count,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,0,0,0,0,0,'PENDING',1,$5,$5)`,
		string(workspaceID), highWater.CreatedAt.UTC(), string(highWater.ID), len(revisions), createdAt.UTC()); err != nil {
		t.Fatal(err)
	}
}

func setArtifactRevisionCitationProjection(t *testing.T, ctx context.Context, pool *pgxpool.Pool, enabled bool) {
	t.Helper()
	statement := `ALTER TABLE learning.artifact_revision DISABLE TRIGGER artifact_revision_project_citation_selectors`
	if enabled {
		statement = `ALTER TABLE learning.artifact_revision ENABLE TRIGGER artifact_revision_project_citation_selectors`
	}
	if _, err := pool.Exec(ctx, statement); err != nil {
		t.Fatalf("set Artifact Revision citation projection enabled=%t: %v", enabled, err)
	}
}

func replaceArtifactRevisionPayload(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, revisionID foundation.ID, sections, contentHash string) {
	t.Helper()
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `ALTER TABLE learning.artifact_revision DISABLE TRIGGER trg_learning_artifact_revision_v1_immutable`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE learning.artifact_revision SET sections=$3::jsonb,content_hash=$4 WHERE workspace_id=$1 AND id=$2`,
			string(workspaceID), string(revisionID), sections, contentHash); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `ALTER TABLE learning.artifact_revision ENABLE TRIGGER trg_learning_artifact_revision_v1_immutable`)
		return err
	}); err != nil {
		t.Fatalf("replace Artifact Revision sections: %v", err)
	}
}
