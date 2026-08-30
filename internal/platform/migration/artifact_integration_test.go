//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestArtifactMigrationsUpRepeat(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 41); err != nil {
		t.Fatalf("empty artifact migration Up failed: %v", err)
	}
	if err := provider.UpTo(ctx, 41); err != nil {
		t.Fatalf("repeated artifact migration Up failed: %v", err)
	}
	assertArtifactMigrationVersion(t, ctx, pool, 41)
}

func TestArtifactExternalTransitionReservationMigrationGuards(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 43); err != nil {
		t.Fatal(err)
	}
	if err := provider.UpTo(ctx, 43); err != nil {
		t.Fatal(err)
	}
	assertArtifactMigrationVersion(t, ctx, pool, 43)

	insertReservation := func(expectedVersion int64, commandType, idempotencyKey, requestHash, workspaceID, artifactID, revisionID string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO learning.artifact_external_transition_reservation(
				workspace_id,artifact_id,current_revision_id,expected_version,command_type,idempotency_key,request_hash,created_at
			) VALUES($1,$2,$3,$4,$5,$6,$7,CURRENT_TIMESTAMP)`,
			workspaceID, artifactID, revisionID, expectedVersion, commandType, idempotencyKey, requestHash)
		return err
	}

	seedArtifactMigrationHistory(t, ctx, pool)
	for _, testCase := range []struct {
		name            string
		expectedVersion int64
		commandType     string
		idempotencyKey  string
		requestHash     string
	}{
		{name: "invalid command type", expectedVersion: 1, commandType: "PLAN", idempotencyKey: "reservation-invalid-command", requestHash: strings.Repeat("c", 64)},
		{name: "non canonical key", expectedVersion: 1, commandType: "EXPORT_MARKDOWN", idempotencyKey: " reservation-noncanonical", requestHash: strings.Repeat("c", 64)},
		{name: "empty key", expectedVersion: 1, commandType: "EXPORT_MARKDOWN", idempotencyKey: "", requestHash: strings.Repeat("c", 64)},
		{name: "oversize key", expectedVersion: 1, commandType: "EXPORT_MARKDOWN", idempotencyKey: strings.Repeat("k", 129), requestHash: strings.Repeat("c", 64)},
		{name: "invalid request hash", expectedVersion: 1, commandType: "EXPORT_MARKDOWN", idempotencyKey: "reservation-invalid-hash", requestHash: strings.Repeat("C", 64)},
		{name: "zero expected version", expectedVersion: 0, commandType: "EXPORT_MARKDOWN", idempotencyKey: "reservation-zero-version", requestHash: strings.Repeat("c", 64)},
		{name: "negative expected version", expectedVersion: -1, commandType: "EXPORT_MARKDOWN", idempotencyKey: "reservation-negative-version", requestHash: strings.Repeat("c", 64)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := insertReservation(
				testCase.expectedVersion,
				testCase.commandType,
				testCase.idempotencyKey,
				testCase.requestHash,
				artifactMigrationWorkspaceID,
				artifactMigrationArtifactID,
				artifactMigrationRevisionID,
			)
			assertPostgresCode(t, err, "23514")
		})
	}

	const otherWorkspaceID = "93000000-0000-4000-8000-000000000006"
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'artifact-reservation-other-workspace','/tmp/artifact-reservation-other-workspace',
			'/tmp/artifact-reservation-other-workspace',CURRENT_TIMESTAMP,'test',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, otherWorkspaceID); err != nil {
		t.Fatal(err)
	}
	assertPostgresCode(t, insertReservation(1, "EXPORT_MARKDOWN", "reservation-cross-workspace", strings.Repeat("c", 64), otherWorkspaceID, artifactMigrationArtifactID, artifactMigrationRevisionID), "23503")
	const otherArtifactID = "93000000-0000-4000-8000-000000000007"
	if _, err := pool.Exec(ctx, `
		INSERT INTO learning.artifact(id,workspace_id,artifact_type,title,scope,status,version,created_at,updated_at)
		VALUES($1,$2,'CUSTOM','artifact reservation revision guard','{}','READY',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, otherArtifactID, artifactMigrationWorkspaceID); err != nil {
		t.Fatal(err)
	}
	assertPostgresCode(t, insertReservation(1, "EXPORT_MARKDOWN", "reservation-cross-revision", strings.Repeat("c", 64), artifactMigrationWorkspaceID, otherArtifactID, artifactMigrationRevisionID), "23503")

	if err := insertReservation(1, "EXPORT_MARKDOWN", "reservation-migration", strings.Repeat("c", 64), artifactMigrationWorkspaceID, artifactMigrationArtifactID, artifactMigrationRevisionID); err != nil {
		t.Fatal(err)
	}
	var reservationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_external_transition_reservation WHERE workspace_id=$1 AND artifact_id=$2`, artifactMigrationWorkspaceID, artifactMigrationArtifactID).Scan(&reservationCount); err != nil {
		t.Fatal(err)
	}
	if reservationCount != 1 {
		t.Fatalf("pending reservation count=%d want=1", reservationCount)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.artifact_external_transition_reservation SET expected_version=2 WHERE workspace_id=$1 AND artifact_id=$2`, artifactMigrationWorkspaceID, artifactMigrationArtifactID); err == nil {
		t.Fatal("reservation UPDATE accepted")
	} else {
		assertPostgresCode(t, err, "55000")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_external_transition_reservation WHERE workspace_id=$1 AND artifact_id=$2`, artifactMigrationWorkspaceID, artifactMigrationArtifactID).Scan(&reservationCount); err != nil {
		t.Fatal(err)
	}
	if reservationCount != 1 {
		t.Fatalf("reservation survived rejected UPDATE count=%d want=1", reservationCount)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM learning.artifact_external_transition_reservation WHERE workspace_id=$1 AND artifact_id=$2`, artifactMigrationWorkspaceID, artifactMigrationArtifactID); err == nil {
		t.Fatal("pending reservation DELETE accepted")
	} else {
		assertPostgresCode(t, err, "55000")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_external_transition_reservation WHERE workspace_id=$1 AND artifact_id=$2`, artifactMigrationWorkspaceID, artifactMigrationArtifactID).Scan(&reservationCount); err != nil {
		t.Fatal(err)
	}
	if reservationCount != 1 {
		t.Fatalf("reservation survived rejected DELETE count=%d want=1", reservationCount)
	}
}

func TestArtifactGenerationMigrationPreservesExistingAgentResultTypes(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 41); err != nil {
		t.Fatal(err)
	}
	assertAgentModelRunResultTypes(t, ctx, pool, true)

	if err := provider.UpTo(ctx, 41); err != nil {
		t.Fatal(err)
	}
	assertAgentModelRunResultTypes(t, ctx, pool, true)
}

func TestArtifactGenerationRecoveryMigrationUpRepeatAndLifecycle(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 42); err != nil {
		t.Fatalf("empty 00042 Up failed: %v", err)
	}
	if err := provider.UpTo(ctx, 42); err != nil {
		t.Fatalf("repeated 00042 Up failed: %v", err)
	}
	assertArtifactMigrationVersion(t, ctx, pool, 42)

	seedArtifactMigrationHistory(t, ctx, pool)
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	definitionID := "93000000-0000-4000-8000-000000000040"
	seedArtifactGenerationDefinition(t, ctx, pool, definitionID, now)
	workflows := [][2]string{
		{"93000000-0000-4000-8000-000000000041", "93000000-0000-4000-8000-000000000051"},
		{"93000000-0000-4000-8000-000000000042", "93000000-0000-4000-8000-000000000052"},
		{"93000000-0000-4000-8000-000000000043", "93000000-0000-4000-8000-000000000053"},
		{"93000000-0000-4000-8000-000000000044", "93000000-0000-4000-8000-000000000054"},
		{"93000000-0000-4000-8000-000000000045", "93000000-0000-4000-8000-000000000055"},
	}
	for _, workflow := range workflows {
		seedArtifactGenerationWorkflow(t, ctx, pool, definitionID, workflow[0], workflow[1], now, false)
	}

	const firstGenerationID = "93000000-0000-4000-8000-000000000061"
	if err := insertArtifactGenerationFixture(ctx, pool, firstGenerationID, workflows[0][0], workflows[0][1], "overview", "generation-recovery-1", now); err != nil {
		t.Fatal(err)
	}
	terminalAt := now.Add(time.Minute)
	_, err := pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='FAILED',failure_class='non_retryable',error_code='ARTIFACT_GENERATION_FAILED',
		error_summary=$2,recorded_base_revision_id=source_revision_id,recorded_revision_id=source_revision_id,
		recorded_artifact_version=2,content_hash=repeat('e',64),terminal_at=$3,version=2,updated_at=$3
		WHERE id=$1`, firstGenerationID, "failure receipt must not be forged", terminalAt)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='FAILED',failure_class='non_retryable',error_code='artifact_generation_failed',
		error_summary='invalid lowercase code',terminal_at=$2,version=2,updated_at=$2 WHERE id=$1`, firstGenerationID, terminalAt)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='FAILED',failure_class='non_retryable',error_code='ARTIFACT_GENERATION_FAILED',
		error_summary=$2,terminal_at=$3,version=2,updated_at=$3 WHERE id=$1`, firstGenerationID, strings.Repeat("界", 257), terminalAt)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='FAILED',failure_class=NULL,error_code='ARTIFACT_GENERATION_FAILED',
		error_summary='missing failure class',terminal_at=$2,version=2,updated_at=$2 WHERE id=$1`, firstGenerationID, terminalAt)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='FAILED',failure_class='non_retryable',error_code='ARTIFACT_GENERATION_FAILED',
		error_summary='missing terminal timestamp',terminal_at=NULL,version=2,updated_at=$2 WHERE id=$1`, firstGenerationID, terminalAt)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='FAILED',failure_class='non_retryable',error_code='ARTIFACT_GENERATION_FAILED',
		error_summary=$2,terminal_at=$3,version=2,updated_at=$3 WHERE id=$1`, firstGenerationID, "\u00a0not trimmed", terminalAt)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='FAILED',failure_class='non_retryable',error_code='ARTIFACT_GENERATION_FAILED',
		error_summary='generation failed safely',terminal_at=$2,version=2,updated_at=$2 WHERE id=$1`, firstGenerationID, terminalAt); err != nil {
		t.Fatalf("PENDING to FAILED transition failed: %v", err)
	}

	const retryGenerationID = "93000000-0000-4000-8000-000000000062"
	if err := insertArtifactGenerationFixture(ctx, pool, retryGenerationID, workflows[1][0], workflows[1][1], "overview", "generation-recovery-2", terminalAt); err != nil {
		t.Fatalf("new idempotency key could not retry a failed source slot: %v", err)
	}
	if err := insertArtifactGenerationFixture(ctx, pool, "93000000-0000-4000-8000-000000000063", workflows[2][0], workflows[2][1], "overview", "generation-recovery-3", terminalAt); err == nil {
		t.Fatal("partial unique index accepted two active source-section owners")
	} else {
		assertPostgresCode(t, err, "23505")
	}
	if err := insertArtifactGenerationFixture(ctx, pool, "93000000-0000-4000-8000-000000000064", workflows[3][0], workflows[3][1], "another-section", "generation-recovery-1", terminalAt); err == nil {
		t.Fatal("terminal generation released its idempotency key")
	} else {
		assertPostgresCode(t, err, "23505")
	}

	cancelledAt := terminalAt.Add(time.Minute)
	_, err = pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='CANCELLED',failure_class='non_retryable',error_code='ARTIFACT_GENERATION_CANCELLED',
		error_summary='wrong cancellation class',terminal_at=$2,version=2,updated_at=$2 WHERE id=$1`, retryGenerationID, cancelledAt)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='CANCELLED',failure_class='cancelled',error_code='ARTIFACT_GENERATION_CANCELLED',
		error_summary='generation cancelled safely',terminal_at=$2,version=2,updated_at=$2 WHERE id=$1`, retryGenerationID, cancelledAt); err != nil {
		t.Fatalf("PENDING to CANCELLED transition failed: %v", err)
	}

	const recoveryGenerationID = "93000000-0000-4000-8000-000000000063"
	if err := insertArtifactGenerationFixture(ctx, pool, recoveryGenerationID, workflows[2][0], workflows[2][1], "overview", "generation-recovery-3", cancelledAt); err != nil {
		t.Fatalf("new idempotency key could not retry a cancelled source slot: %v", err)
	}
	recoveryAt := cancelledAt.Add(time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='RECOVERY_REQUIRED',failure_class='manual_recovery',error_code='ARTIFACT_GENERATION_RESULT_UNKNOWN',
		error_summary='generation result requires recovery',terminal_at=$2,version=2,updated_at=$2 WHERE id=$1`, recoveryGenerationID, recoveryAt); err != nil {
		t.Fatalf("PENDING to RECOVERY_REQUIRED transition failed: %v", err)
	}
	if err := insertArtifactGenerationFixture(ctx, pool, "93000000-0000-4000-8000-000000000065", workflows[4][0], workflows[4][1], "overview", "generation-recovery-4", recoveryAt); err == nil {
		t.Fatal("RECOVERY_REQUIRED released its active source-section slot")
	} else {
		assertPostgresCode(t, err, "23505")
	}
	_, err = pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='FAILED',failure_class='non_retryable',error_code='ARTIFACT_GENERATION_FAILED',
		error_summary='illegal recovery release',terminal_at=$2,version=3,updated_at=$2 WHERE id=$1`, recoveryGenerationID, recoveryAt.Add(time.Minute))
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `DELETE FROM learning.artifact_section_generation WHERE id=$1`, recoveryGenerationID)
	assertPostgresCode(t, err, "55000")

	var indexDefinition string
	if err := pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes
		WHERE schemaname='learning' AND indexname='uq_learning_artifact_section_generation_active_section'`).Scan(&indexDefinition); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(indexDefinition, "source_revision_id") || !strings.Contains(indexDefinition, "artifact_id") || !strings.Contains(indexDefinition, "section_key") ||
		!strings.Contains(indexDefinition, "'PENDING'::text") || !strings.Contains(indexDefinition, "'RECOVERY_REQUIRED'::text") {
		t.Fatalf("active section index definition=%s", indexDefinition)
	}
}

func TestArtifactGenerationRecoveryMigrationCompletedReceiptIsImmutable(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 42); err != nil {
		t.Fatal(err)
	}
	seedArtifactMigrationHistory(t, ctx, pool)
	now := time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC)
	const (
		definitionID = "93000000-0000-4000-8000-000000000070"
		runID        = "93000000-0000-4000-8000-000000000071"
		nodeID       = "93000000-0000-4000-8000-000000000072"
		attemptID    = "93000000-0000-4000-8000-000000000073"
		indexID      = "93000000-0000-4000-8000-000000000074"
		modelRunID   = "93000000-0000-4000-8000-000000000075"
		modelCallID  = "93000000-0000-4000-8000-000000000076"
		revisionID   = "93000000-0000-4000-8000-000000000077"
		generationID = "93000000-0000-4000-8000-000000000078"
	)
	seedArtifactGenerationDefinition(t, ctx, pool, definitionID, now)
	seedArtifactGenerationWorkflow(t, ctx, pool, definitionID, runID, nodeID, now, true)
	seedArtifactGenerationModelReceipt(t, ctx, pool, runID, nodeID, attemptID, indexID, modelRunID, modelCallID, now)
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_revision(
		id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,content_markdown,provenance,
		domain_schema_version,content_hash,created_by_type,generation_metadata,created_at
	) VALUES($1,$2,$3,2,'SNAPSHOT','[]','[]','[]','[]','[]','generated','{}','artifact-revision/v1',repeat('e',64),'AGENT','{}',$4)`,
		revisionID, artifactMigrationArtifactID, artifactMigrationWorkspaceID, now); err != nil {
		t.Fatal(err)
	}
	if err := insertArtifactGenerationFixture(ctx, pool, generationID, runID, nodeID, "overview", "generation-completed", now); err != nil {
		t.Fatal(err)
	}
	completedAt := now.Add(2 * time.Minute)
	_, err := pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='COMPLETED',model_run_id=$2,recorded_base_revision_id=source_revision_id,recorded_revision_id=$3,
		recorded_artifact_version=2,content_hash=repeat('e',64),completed_at=$4,terminal_at=$5,version=2,updated_at=$4
		WHERE id=$1`, generationID, modelRunID, revisionID, completedAt, completedAt.Add(time.Second))
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='COMPLETED',model_run_id=$2,recorded_base_revision_id=source_revision_id,recorded_revision_id=$3,
		recorded_artifact_version=2,content_hash=repeat('e',64),completed_at=$4,terminal_at=$4,version=2,updated_at=$4
		WHERE id=$1`, generationID, modelRunID, revisionID, completedAt); err != nil {
		t.Fatalf("complete generation with full receipt: %v", err)
	}
	var status, contentHash string
	var version int64
	var storedModelRunID, baseRevisionID, storedRevisionID string
	var storedCompletedAt, storedTerminalAt, storedUpdatedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT status,model_run_id,recorded_base_revision_id,recorded_revision_id,
		content_hash,version,completed_at,terminal_at,updated_at
		FROM learning.artifact_section_generation WHERE id=$1`, generationID).Scan(
		&status, &storedModelRunID, &baseRevisionID, &storedRevisionID, &contentHash, &version,
		&storedCompletedAt, &storedTerminalAt, &storedUpdatedAt,
	); err != nil {
		t.Fatal(err)
	}
	if status != "COMPLETED" || storedModelRunID != modelRunID || baseRevisionID != artifactMigrationRevisionID ||
		storedRevisionID != revisionID || contentHash != strings.Repeat("e", 64) || version != 2 ||
		!storedCompletedAt.Equal(storedUpdatedAt) || !storedTerminalAt.Equal(storedUpdatedAt) {
		t.Fatalf("completed generation receipt status=%s model=%s base=%s revision=%s hash=%s version=%d completed=%s terminal=%s updated=%s",
			status, storedModelRunID, baseRevisionID, storedRevisionID, contentHash, version,
			storedCompletedAt, storedTerminalAt, storedUpdatedAt)
	}
	_, err = pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET content_hash=repeat('f',64),version=3 WHERE id=$1`, generationID)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `DELETE FROM learning.artifact_section_generation WHERE id=$1`, generationID)
	assertPostgresCode(t, err, "55000")
}

func TestArtifactGenerationRecoveryMigrationBackfillsExistingCompletedTerminalTime(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 41); err != nil {
		t.Fatal(err)
	}
	seedArtifactMigrationHistory(t, ctx, pool)
	now := time.Date(2026, 7, 26, 11, 30, 0, 0, time.UTC)
	const (
		definitionID = "93000000-0000-4000-8000-0000000000a0"
		runID        = "93000000-0000-4000-8000-0000000000a1"
		nodeID       = "93000000-0000-4000-8000-0000000000a2"
		attemptID    = "93000000-0000-4000-8000-0000000000a3"
		indexID      = "93000000-0000-4000-8000-0000000000a4"
		modelRunID   = "93000000-0000-4000-8000-0000000000a5"
		modelCallID  = "93000000-0000-4000-8000-0000000000a6"
		revisionID   = "93000000-0000-4000-8000-0000000000a7"
		generationID = "93000000-0000-4000-8000-0000000000a8"
	)
	seedArtifactGenerationDefinition(t, ctx, pool, definitionID, now)
	seedArtifactGenerationWorkflow(t, ctx, pool, definitionID, runID, nodeID, now, true)
	seedArtifactGenerationModelReceipt(t, ctx, pool, runID, nodeID, attemptID, indexID, modelRunID, modelCallID, now)
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_revision(
		id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,content_markdown,provenance,
		domain_schema_version,content_hash,created_by_type,generation_metadata,created_at
	) VALUES($1,$2,$3,2,'SNAPSHOT','[]','[]','[]','[]','[]','generated before recovery migration','{}',
		'artifact-revision/v1',repeat('f',64),'AGENT','{}',$4)`,
		revisionID, artifactMigrationArtifactID, artifactMigrationWorkspaceID, now); err != nil {
		t.Fatal(err)
	}
	if err := insertArtifactGenerationFixture(ctx, pool, generationID, runID, nodeID, "overview", "generation-before-recovery-migration", now); err != nil {
		t.Fatal(err)
	}
	completedAt := now.Add(2 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE learning.artifact_section_generation SET
		status='COMPLETED',model_run_id=$2,recorded_base_revision_id=source_revision_id,recorded_revision_id=$3,
		recorded_artifact_version=2,content_hash=repeat('f',64),completed_at=$4,version=2,updated_at=$4
		WHERE id=$1`, generationID, modelRunID, revisionID, completedAt); err != nil {
		t.Fatal(err)
	}
	if err := provider.UpTo(ctx, 42); err != nil {
		t.Fatalf("00042 Up rejected an existing completed generation: %v", err)
	}
	var storedCompletedAt, storedTerminalAt time.Time
	if err := pool.QueryRow(ctx, `SELECT completed_at,terminal_at
		FROM learning.artifact_section_generation WHERE id=$1`, generationID).Scan(&storedCompletedAt, &storedTerminalAt); err != nil {
		t.Fatal(err)
	}
	if !storedTerminalAt.Equal(storedCompletedAt) || !storedTerminalAt.Equal(completedAt) {
		t.Fatalf("completed terminal backfill completed_at=%s terminal_at=%s want=%s", storedCompletedAt, storedTerminalAt, completedAt)
	}
	if err := provider.UpTo(ctx, 42); err != nil {
		t.Fatalf("repeated 00042 Up after completed backfill failed: %v", err)
	}
	seedArtifactGenerationWorkflow(t, ctx, pool, definitionID,
		"93000000-0000-4000-8000-0000000000b1", "93000000-0000-4000-8000-0000000000b2", completedAt, false)
	if err := insertArtifactGenerationFixture(ctx, pool,
		"93000000-0000-4000-8000-0000000000b3", "93000000-0000-4000-8000-0000000000b1",
		"93000000-0000-4000-8000-0000000000b2", "overview", "generation-after-completed", completedAt); err != nil {
		t.Fatalf("COMPLETED retained the active source-section slot: %v", err)
	}
}

const (
	artifactMigrationWorkspaceID = "93000000-0000-4000-8000-000000000001"
	artifactMigrationArtifactID  = "93000000-0000-4000-8000-000000000002"
	artifactMigrationRevisionID  = "93000000-0000-4000-8000-000000000003"
)

func seedArtifactGenerationDefinition(t *testing.T, ctx context.Context, pool *pgxpool.Pool, definitionID string, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,$3,1,'{"nodes":[]}'::jsonb,$4)`, definitionID, artifactMigrationWorkspaceID, "artifact-generation-"+definitionID, now); err != nil {
		t.Fatal(err)
	}
}

func seedArtifactGenerationWorkflow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, definitionID, runID, nodeID string, now time.Time, running bool) {
	t.Helper()
	runStatus := "pending"
	nodeStatus := "pending"
	var leaseOwner any
	var leaseUntil any
	if running {
		runStatus = "running"
		nodeStatus = "running"
		leaseOwner = "artifact-migration-worker"
		leaseUntil = now.Add(5 * time.Minute)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(
		id,workspace_id,definition_id,status,input,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,'{}'::jsonb,1,$5,$5)`, runID, artifactMigrationWorkspaceID, definitionID, runStatus, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,version,created_at,updated_at
	) VALUES($1,$2,'generate-section','artifact.generate-section',$3,0,'{}'::jsonb,$4,$5,1,$6,$6)`,
		nodeID, runID, nodeStatus, leaseOwner, leaseUntil, now); err != nil {
		t.Fatal(err)
	}
}

func insertArtifactGenerationFixture(ctx context.Context, pool *pgxpool.Pool, generationID, runID, nodeID, sectionKey, idempotencyKey string, now time.Time) error {
	_, err := pool.Exec(ctx, `INSERT INTO learning.artifact_section_generation(
		id,workspace_id,artifact_id,source_revision_id,source_revision_no,source_artifact_version,
		section_key,idempotency_key,request_hash,workflow_run_id,node_run_id,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,1,1,$5,$6,repeat('d',64),$7,$8,'PENDING',1,$9,$9)`,
		generationID, artifactMigrationWorkspaceID, artifactMigrationArtifactID, artifactMigrationRevisionID,
		sectionKey, idempotencyKey, runID, nodeID, now)
	return err
}

func seedArtifactGenerationModelReceipt(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	runID, nodeID, attemptID, indexID, modelRunID, modelCallID string,
	now time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
	) VALUES($1,$2,1,1,0,'artifact-generation-delivery','artifact-migration-worker',$3,'running',$4)`,
		attemptID, nodeID, now.Add(5*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
		version,created_at,updated_at
	) VALUES($1,$2,'simple','v1',repeat('1',64),'{}','artifact-generation:index',repeat('2',64),0,
		'artifact-generation-index','building','["vector"]',1,$3,$3)`, indexID, artifactMigrationWorkspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent.model_run(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
		reduced_schema_id,reduced_schema_version,retrieval_index_version_id,status,version,started_at,updated_at
	) VALUES($1,$2,$3,$4,$5,'openai-compatible','v1','artifact-model','2026-07-01','default','v1',
		'artifact-section','v1','artifact.section','v1','artifact.gap','v1',$6,'RUNNING',1,$7,$7)`,
		modelRunID, artifactMigrationWorkspaceID, runID, nodeID, attemptID, indexID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent.model_call(
		id,model_run_id,call_no,phase,adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
		request_hash,request_bytes,status,version,started_at
	) VALUES($1,$2,1,'INITIAL','openai-compatible','v1','artifact-model','2026-07-01','default','v1',
		'artifact-section','v1','artifact.section','v1',1024,repeat('a',64),128,'STARTED',1,$3)`, modelCallID, modelRunID, now); err != nil {
		t.Fatal(err)
	}
	callCompletedAt := now.Add(time.Second)
	if _, err := pool.Exec(ctx, `UPDATE agent.model_call SET
		status='SUCCEEDED',response_hash=repeat('b',64),response_bytes=64,input_tokens=10,output_tokens=5,
		latency_ms=20,version=2,completed_at=$2 WHERE id=$1`, modelCallID, callCompletedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.model_run SET
		status='SUCCEEDED',final_result_type='artifact_section',version=2,updated_at=$2,completed_at=$2 WHERE id=$1`,
		modelRunID, callCompletedAt); err != nil {
		t.Fatal(err)
	}
}

func seedArtifactMigrationHistory(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	const (
		workspaceID = "93000000-0000-4000-8000-000000000001"
		artifactID  = "93000000-0000-4000-8000-000000000002"
		revisionID  = "93000000-0000-4000-8000-000000000003"
		exportID    = "93000000-0000-4000-8000-000000000004"
		proposalID  = "93000000-0000-4000-8000-000000000005"
	)
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'artifact-migration','/tmp/artifact-migration','/tmp/artifact-migration',$2,'test',1,$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO learning.artifact(
			id,workspace_id,artifact_type,title,scope,scope_definition,source_coverage,current_revision_id,
			status,version,domain_schema_version,created_at,updated_at
		) VALUES($1,$2,'CUSTOM','artifact migration fixture','{}','migration scope','[]',$3,'PLANNING',1,'artifact/v1',$4,$4)`, artifactID, workspaceID, revisionID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO learning.artifact_revision(
			id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,content_markdown,provenance,
			domain_schema_version,content_hash,created_by_type,generation_metadata,created_at
		) VALUES($1,$2,$3,1,'SNAPSHOT','[]','[]','[]','[]','[]','','{}','artifact-revision/v1',repeat('a',64),'HUMAN',NULL,$4)`, revisionID, artifactID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO learning.artifact_command(workspace_id,idempotency_key,request_hash,command_type,artifact_id,artifact_version,response,created_at)
		VALUES($1,'artifact-migration-command',repeat('b',64),'PLAN',$2,1,'{}',$3)`, workspaceID, artifactID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO learning.artifact_export(id,workspace_id,artifact_id,revision_id,artifact_version,revision_no,revision_hash,output_path,output_hash,output_size,exported_at)
		VALUES($1,$2,$3,$4,1,1,repeat('a',64),$5,repeat('c',64),10,$6)`, exportID, workspaceID, artifactID, revisionID, ".knowledge/exports/artifacts/"+artifactID+"/"+exportID+".md", now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO learning.artifact_publication(artifact_id,workspace_id,revision_id,artifact_version,revision_no,content_hash,proposal_id,idempotency_key,created_at)
		VALUES($1,$2,$3,1,1,repeat('a',64),$4,'artifact-migration-publication',$5)`, artifactID, workspaceID, revisionID, proposalID, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertArtifactMigrationVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int64) {
	t.Helper()
	version, err := migrationProvider(t, pool).GetDBVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != want {
		t.Fatalf("artifact migration version=%d want=%d", version, want)
	}
}

func assertAgentModelRunResultTypes(t *testing.T, ctx context.Context, pool *pgxpool.Pool, artifactSectionAllowed bool) {
	t.Helper()
	var definition string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid = 'agent.model_run'::regclass
		  AND conname = 'agent_model_run_final_result_type_check'`,
	).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(definition, "'tool_request'::text") {
		t.Fatalf("model run result constraint lost tool_request: %s", definition)
	}
	hasArtifactSection := strings.Contains(definition, "'artifact_section'::text")
	if hasArtifactSection != artifactSectionAllowed {
		t.Fatalf("artifact_section allowed=%t want=%t: %s", hasArtifactSection, artifactSectionAllowed, definition)
	}
}
