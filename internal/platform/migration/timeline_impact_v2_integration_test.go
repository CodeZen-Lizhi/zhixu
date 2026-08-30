//go:build integration

package migration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTimelineImpactV2MigrationPreservesV1AndVersionsReports(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 61); err != nil {
		t.Fatalf("00061 up: %v", err)
	}

	fixture := seedTimelineImpactV2UpgradeFixture(t, ctx, pool)
	if err := provider.UpTo(ctx, 62); err != nil {
		t.Fatalf("00062 up: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 62)
	assertTimelineImpactV1ReportUnchanged(t, ctx, pool, fixture)
	assertTimelineImpactV2SeekIndexes(t, ctx, pool, fixture)

	var markerStatus, highWaterRevisionID string
	var expectedRevisions, processedRevisions int64
	if err := pool.QueryRow(ctx, `SELECT status,high_water_revision_id::text,
		expected_revision_count,processed_revision_count
		FROM learning.artifact_citation_selector_backfill
		WHERE workspace_id=$1`, fixture.workspaceID).Scan(
		&markerStatus, &highWaterRevisionID, &expectedRevisions, &processedRevisions,
	); err != nil {
		t.Fatalf("read Artifact selector marker: %v", err)
	}
	if markerStatus != "PENDING" || highWaterRevisionID != fixture.revisionID || expectedRevisions != 1 || processedRevisions != 0 {
		t.Fatalf("marker status=%s high_water=%s expected=%d processed=%d",
			markerStatus, highWaterRevisionID, expectedRevisions, processedRevisions)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO ops.impact_report(
		id,workspace_id,source_event_id,status,objects,summary,generated_at,schema_version,
		source_event_version,fingerprint,version,created_at,analysis_version,supersedes_report_id
	) VALUES($1,$2,$3,'READY','[]','{}',$4,'impact-report/v2',1,$5,1,$4,
		'impact-analysis/v2',$6)`, fixture.crossSourceReportID, fixture.workspaceID, fixture.otherEventID,
		fixture.reportTime.Add(3*time.Minute), strings.Repeat("d", 64), fixture.v1ReportID); !isPostgresCode(err, "23503") {
		t.Fatalf("cross-source supersession error=%v", err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO ops.impact_report(
		id,workspace_id,source_event_id,status,objects,summary,generated_at,schema_version,
		source_event_version,fingerprint,version,created_at,analysis_version,supersedes_report_id
	) VALUES($1,$2,$3,'READY','[]','{}',$4,'impact-report/v2',1,$5,1,$4,
		'impact-analysis/v2',$6)`, fixture.v2ReportID, fixture.workspaceID, fixture.eventID,
		fixture.reportTime.Add(time.Minute), strings.Repeat("b", 64), fixture.v1ReportID); err != nil {
		t.Fatalf("insert v2 report: %v", err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO ops.impact_report(
		id,workspace_id,source_event_id,status,objects,summary,generated_at,schema_version,
		source_event_version,fingerprint,version,created_at,analysis_version,supersedes_report_id
	) VALUES($1,$2,$3,'READY','[]','{}',$4,'impact-report/v2',1,$5,1,$4,
		'impact-analysis/v2',$6)`, fixture.duplicateV2ReportID, fixture.workspaceID, fixture.eventID,
		fixture.reportTime.Add(2*time.Minute), strings.Repeat("c", 64), fixture.v1ReportID); !isPostgresCode(err, "23505") {
		t.Fatalf("duplicate v2 analysis error=%v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE ops.impact_report SET fingerprint=repeat('e',64) WHERE id=$1`, fixture.v2ReportID); !isPostgresCode(err, "55000") {
		t.Fatalf("v2 report append-only update error=%v", err)
	}
	if err := provider.UpTo(ctx, 62); err != nil {
		t.Fatalf("00062 re-up: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 62)
	assertTimelineImpactV1ReportUnchanged(t, ctx, pool, fixture)
}

type timelineImpactV2UpgradeFixture struct {
	workspaceID         string
	eventID             string
	otherEventID        string
	v1ReportID          string
	v2ReportID          string
	duplicateV2ReportID string
	crossSourceReportID string
	artifactID          string
	revisionID          string
	reportTime          time.Time
}

func seedTimelineImpactV2UpgradeFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) timelineImpactV2UpgradeFixture {
	t.Helper()
	fixture := timelineImpactV2UpgradeFixture{
		workspaceID:         "62000000-0000-4000-8000-000000000001",
		eventID:             "62000000-0000-4000-8000-000000000002",
		otherEventID:        "62000000-0000-4000-8000-000000000003",
		v1ReportID:          "62000000-0000-4000-8000-000000000004",
		v2ReportID:          "62000000-0000-4000-8000-000000000005",
		duplicateV2ReportID: "62000000-0000-4000-8000-000000000006",
		crossSourceReportID: "62000000-0000-4000-8000-000000000007",
		artifactID:          "62000000-0000-4a00-8000-000000000008",
		revisionID:          "62000000-0000-4b00-8000-000000000009",
		reportTime:          time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC),
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,'timeline-impact-v2-upgrade','/tmp/timeline-impact-v2-upgrade',
		'/tmp/timeline-impact-v2-upgrade',$2,'test',1,$2,$2)`, fixture.workspaceID, fixture.reportTime); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.knowledge_event(
		id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,
		event_version,schema_version,summary,payload,correlation,occurred_at,created_at
	) VALUES
		($1,$3,'CONFLICT_RESOLVED','CONFLICT',$1,'conflict:resolved:v1','conflict:v1',
		 1,'knowledge-event/v1','conflict resolved','{}','{}',$4,$4),
		($2,$3,'CORRECTIVE_EVENT','CONFLICT',$2,'conflict:corrective:v1','conflict:other',
		 1,'knowledge-event/v1','conflict corrected','{}','{}',$4,$4)`,
		fixture.eventID, fixture.otherEventID, fixture.workspaceID, fixture.reportTime); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.impact_report(
		id,workspace_id,source_event_id,status,objects,summary,generated_at,schema_version,
		source_event_version,fingerprint,version,created_at
	) VALUES($1,$2,$3,'READY','[]','{}',$4,'impact-report/v1',1,$5,1,$4)`,
		fixture.v1ReportID, fixture.workspaceID, fixture.eventID, fixture.reportTime, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}

	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO learning.artifact(
			id,workspace_id,artifact_type,title,scope,status,version,created_at,updated_at,
			domain_schema_version,scope_definition,source_coverage,current_revision_id
		) VALUES($1,$2,'CUSTOM','Timeline Impact upgrade Artifact','{}','PLANNING',1,$3,$3,
			'artifact/v1','migration fixture','[]',$4)`, fixture.artifactID, fixture.workspaceID, fixture.reportTime, fixture.revisionID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO learning.artifact_revision(
			id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,
			conflicts,content_markdown,provenance,created_at,domain_schema_version,content_hash,
			created_by_type,generation_metadata
		) VALUES($1,$2,$3,1,'SNAPSHOT','[]','[]','[]','[]','[]','','{}',$4,
			'artifact-revision/v1',$5,'HUMAN',NULL)`, fixture.revisionID, fixture.artifactID,
			fixture.workspaceID, fixture.reportTime, strings.Repeat("f", 64))
		return err
	}); err != nil {
		t.Fatalf("seed Artifact revision: %v", err)
	}
	return fixture
}

func assertTimelineImpactV1ReportUnchanged(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture timelineImpactV2UpgradeFixture) {
	t.Helper()
	var schemaVersion, analysisVersion, fingerprint string
	var generatedAt, createdAt time.Time
	var version int64
	query := `SELECT schema_version,`
	if migrationVersion(t, ctx, pool) >= 62 {
		query += `analysis_version,`
	} else {
		query += `'impact-analysis/v1',`
	}
	query += `fingerprint,generated_at,created_at,version FROM ops.impact_report WHERE id=$1`
	if err := pool.QueryRow(ctx, query, fixture.v1ReportID).Scan(
		&schemaVersion, &analysisVersion, &fingerprint, &generatedAt, &createdAt, &version,
	); err != nil {
		t.Fatal(err)
	}
	if schemaVersion != "impact-report/v1" || analysisVersion != "impact-analysis/v1" ||
		fingerprint != strings.Repeat("a", 64) || version != 1 ||
		!generatedAt.Equal(fixture.reportTime) || !createdAt.Equal(fixture.reportTime) {
		t.Fatalf("v1 report changed schema=%s analysis=%s fingerprint=%s version=%d generated=%s created=%s",
			schemaVersion, analysisVersion, fingerprint, version, generatedAt, createdAt)
	}
}

func assertTimelineImpactV2SeekIndexes(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture timelineImpactV2UpgradeFixture) {
	t.Helper()
	definitions := map[string][]string{
		"learning.idx_learning_artifact_revision_backfill_seek": {
			"ON learning.artifact_revision USING btree (workspace_id, domain_schema_version, created_at, id)",
		},
		"learning.idx_learning_artifact_citation_backfill_pending_seek": {
			"ON learning.artifact_citation_selector_backfill USING btree (((status = 'FAILED'::text)), created_at, workspace_id)",
			"status <> 'COMPLETED'::text",
		},
		"learning.idx_learning_review_card_claim_impact_seek": {
			"ON learning.review_card USING btree (workspace_id, claim_id, id)",
		},
	}
	for indexName, fragments := range definitions {
		var definition string
		if err := pool.QueryRow(ctx, `SELECT COALESCE(pg_get_indexdef(to_regclass($1)),'')`, indexName).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(definition, fragment) {
				t.Fatalf("index %s definition=%q missing %q", indexName, definition, fragment)
			}
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	plans := []struct {
		indexName string
		query     string
		args      []any
	}{
		{
			indexName: "idx_learning_artifact_revision_backfill_seek",
			query: `EXPLAIN (COSTS OFF) SELECT id FROM learning.artifact_revision
				WHERE workspace_id=$1 AND domain_schema_version='artifact-revision/v1'
				  AND (created_at,id)>($2,$3) AND (created_at,id)<=($4,$5)
				ORDER BY created_at,id LIMIT 32 FOR UPDATE SKIP LOCKED`,
			args: []any{fixture.workspaceID, fixture.reportTime.Add(-time.Minute), fixture.eventID,
				fixture.reportTime, fixture.revisionID},
		},
		{
			indexName: "idx_learning_artifact_citation_backfill_pending_seek",
			query: `EXPLAIN (COSTS OFF) SELECT workspace_id
				FROM learning.artifact_citation_selector_backfill
				WHERE status<>'COMPLETED'
				ORDER BY (status='FAILED'),created_at,workspace_id
				LIMIT 1 FOR UPDATE SKIP LOCKED`,
		},
		{
			indexName: "idx_learning_review_card_claim_impact_seek",
			query: `EXPLAIN (COSTS OFF) SELECT id FROM learning.review_card
				WHERE workspace_id=$1 AND claim_id=$2 ORDER BY id LIMIT 501`,
			args: []any{fixture.workspaceID, fixture.eventID},
		},
	}
	for _, expected := range plans {
		rows, err := tx.Query(ctx, expected.query, expected.args...)
		if err != nil {
			t.Fatal(err)
		}
		lines := make([]string, 0)
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			lines = append(lines, line)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		plan := strings.Join(lines, "\n")
		if !strings.Contains(plan, expected.indexName) {
			t.Fatalf("query plan does not use %s:\n%s", expected.indexName, plan)
		}
		if expected.indexName == "idx_learning_artifact_citation_backfill_pending_seek" && strings.Contains(plan, "Sort") {
			t.Fatalf("marker claim plan still sorts instead of seeking through %s:\n%s", expected.indexName, plan)
		}
	}
}

func migrationVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	version, err := migrationProvider(t, pool).GetDBVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func TestTimelineImpactV2MigrationRejectsIncompleteOwnerBindings(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 61); err != nil {
		t.Fatalf("prepare migrations through 00061: %v", err)
	}
	fixture := seedTimelineImpactV2UpgradeFixture(t, ctx, pool)
	if err := provider.UpTo(ctx, 62); err != nil {
		t.Fatalf("migrate through 00062: %v", err)
	}

	const (
		reviewCardID  = "62200000-0000-4000-8000-000000000001"
		outboxEventID = "62200000-0000-4000-8000-000000000002"
		knowledgeID   = "62200000-0000-4000-8000-000000000003"
	)
	artifactBindingValue := fmt.Sprintf(
		`{"artifact_id":%q,"artifact_version":1,"revision_id":%q,"revision_no":1,"content_hash":%q}`,
		fixture.artifactID, fixture.revisionID, strings.Repeat("f", 64),
	)
	artifactOwnerBinding := `{"artifact":` + artifactBindingValue + `}`
	artifactMissingHash := `{"artifact":{"artifact_id":"` + fixture.artifactID + `","artifact_version":1,"revision_id":"` + fixture.revisionID + `","revision_no":1}}`
	artifactUppercaseBinding := strings.ReplaceAll(
		strings.ReplaceAll(artifactOwnerBinding, fixture.artifactID, strings.ToUpper(fixture.artifactID)),
		fixture.revisionID, strings.ToUpper(fixture.revisionID),
	)
	artifactBracedBinding := strings.ReplaceAll(
		strings.ReplaceAll(artifactOwnerBinding, fixture.artifactID, "{"+fixture.artifactID+"}"),
		fixture.revisionID, "{"+fixture.revisionID+"}",
	)
	reviewMissingEvidenceFingerprint := `{"review_card":{"card_id":"` + reviewCardID + `","card_version":2,"status":"INVALIDATED","fingerprint":"` + strings.Repeat("9", 64) + `","claim_id":"` + fixture.otherEventID + `"}}`

	for _, test := range []struct {
		name          string
		eventType     string
		aggregateType string
		aggregateID   string
		binding       string
	}{
		{name: "artifact_missing_hash", eventType: "ARTIFACT_GENERATED", aggregateType: "ARTIFACT", aggregateID: fixture.artifactID, binding: artifactMissingHash},
		{name: "artifact_uppercase_uuid", eventType: "ARTIFACT_GENERATED", aggregateType: "ARTIFACT", aggregateID: fixture.artifactID, binding: artifactUppercaseBinding},
		{name: "artifact_braced_uuid", eventType: "ARTIFACT_GENERATED", aggregateType: "ARTIFACT", aggregateID: fixture.artifactID, binding: artifactBracedBinding},
		{name: "review_card", eventType: "REVIEW_CARD_INVALIDATED", aggregateType: "REVIEW_CARD", aggregateID: reviewCardID, binding: reviewMissingEvidenceFingerprint},
	} {
		t.Run("timeline_validator_"+test.name, func(t *testing.T) {
			var valid *bool
			if err := pool.QueryRow(ctx, `SELECT ops.valid_timeline_owner_binding($1,$2,$3,$4::jsonb)`,
				test.eventType, test.aggregateType, test.aggregateID, test.binding).Scan(&valid); err != nil {
				t.Fatal(err)
			}
			if valid == nil || *valid {
				t.Fatalf("incomplete %s Timeline owner binding validity=%v want=false", test.name, valid)
			}
		})
	}
	for _, test := range []struct {
		name          string
		targetType    string
		targetID      string
		targetVersion int64
		binding       string
	}{
		{name: "artifact_missing_hash", targetType: "ARTIFACT", targetID: fixture.artifactID, targetVersion: 1, binding: artifactMissingHash},
		{name: "artifact_uppercase_uuid", targetType: "ARTIFACT", targetID: fixture.artifactID, targetVersion: 1, binding: artifactUppercaseBinding},
		{name: "artifact_braced_uuid", targetType: "ARTIFACT", targetID: fixture.artifactID, targetVersion: 1, binding: artifactBracedBinding},
		{name: "review_card", targetType: "REVIEW_CARD", targetID: reviewCardID, targetVersion: 2, binding: reviewMissingEvidenceFingerprint},
	} {
		t.Run("impact_validator_"+test.name, func(t *testing.T) {
			var valid *bool
			if err := pool.QueryRow(ctx, `SELECT ops.valid_impact_owner_binding($1,$2,$3,$4::jsonb)`,
				test.targetType, test.targetID, test.targetVersion, test.binding).Scan(&valid); err != nil {
				t.Fatal(err)
			}
			if valid == nil || *valid {
				t.Fatalf("incomplete %s Impact owner binding validity=%v want=false", test.name, valid)
			}
		})
	}

	invalidAt := fixture.reportTime.Add(10 * time.Minute)
	for name, binding := range map[string]string{
		"missing_hash":   artifactMissingHash,
		"uppercase_uuid": artifactUppercaseBinding,
		"braced_uuid":    artifactBracedBinding,
	} {
		t.Run("event_rejects_"+name, func(t *testing.T) {
			assertTimelineImpactV2RejectedInsert(t, ctx, pool, `INSERT INTO ops.timeline_projection_outbox(
				event_id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,
				event_version,schema_version,summary,correlation,operator_type,operator_id,owner_binding,
				occurred_at,status,created_at,updated_at
			) VALUES($1,$2,'ARTIFACT_GENERATED','ARTIFACT',$3,'invalid-artifact-owner:v1',
				'artifact:' || $3::uuid::text,1,'knowledge-event/v2','invalid Artifact owner','{}',
				'SYSTEM',NULL,$4::jsonb,$5,'PENDING',$5,$5)`, outboxEventID, fixture.workspaceID,
				fixture.artifactID, binding, invalidAt)
			assertTimelineImpactV2RejectedInsert(t, ctx, pool, `INSERT INTO ops.knowledge_event(
				id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,
				schema_version,summary,payload,correlation,operator_type,operator_id,owner_binding,occurred_at,created_at
			) VALUES($1,$2,'ARTIFACT_GENERATED','ARTIFACT',$3,'invalid-artifact-event:v1',
				'artifact:' || $3::uuid::text,1,'knowledge-event/v2','invalid Artifact owner','{}','{}',
				'SYSTEM',NULL,$4::jsonb,$5,$5)`, knowledgeID, fixture.workspaceID, fixture.artifactID,
				binding, invalidAt)
		})
	}
	assertTimelineImpactV2RejectedInsert(t, ctx, pool, `INSERT INTO ops.knowledge_event(
		id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,
		schema_version,summary,payload,correlation,operator_type,operator_id,owner_binding,occurred_at,created_at
	) VALUES($1,$2,'REVIEW_CARD_INVALIDATED','REVIEW_CARD',$3,'invalid-review-owner:v2',
		'review-card:' || $3::uuid::text,2,'knowledge-event/v2','invalid Review owner','{}','{}',
		'SYSTEM',NULL,$4::jsonb,$5,$5)`, knowledgeID, fixture.workspaceID, reviewCardID,
		reviewMissingEvidenceFingerprint, invalidAt)

	assertTimelineImpactV2RejectedInsert(t, ctx, pool, `INSERT INTO ops.timeline_projection_outbox(
		event_id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,
		event_version,schema_version,summary,correlation,operator_type,operator_id,owner_binding,
		occurred_at,status,created_at,updated_at
	) VALUES($1,$2,'ARTIFACT_GENERATED','ARTIFACT',$3,'null-operator-outbox:v1',
		'artifact:' || $3::uuid::text,1,'knowledge-event/v2','null operator','{}',
		NULL,NULL,$4::jsonb,$5,'PENDING',$5,$5)`, outboxEventID, fixture.workspaceID,
		fixture.artifactID, artifactOwnerBinding, invalidAt)
	assertTimelineImpactV2RejectedInsert(t, ctx, pool, `INSERT INTO ops.knowledge_event(
		id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,
		schema_version,summary,payload,correlation,operator_type,operator_id,owner_binding,occurred_at,created_at
	) VALUES($1,$2,'ARTIFACT_GENERATED','ARTIFACT',$3,'null-operator-event:v1',
		'artifact:' || $3::uuid::text,1,'knowledge-event/v2','null operator','{}','{}',
		NULL,NULL,$4::jsonb,$5,$5)`, knowledgeID, fixture.workspaceID, fixture.artifactID,
		artifactOwnerBinding, invalidAt)
	assertTimelineImpactV2RejectedInsert(t, ctx, pool, `SELECT ops.enqueue_timeline_projection_v2(
		$1,'ARTIFACT_GENERATED','ARTIFACT',$2,'null-operator-enqueue:v1','artifact:' || $2::uuid::text,
		1,'null operator','{}',NULL::text,NULL,$3::jsonb,$4
	)`, fixture.workspaceID, fixture.artifactID, artifactOwnerBinding, invalidAt)

	const downstreamReason = "rebuild after impact analysis"
	artifactObjectValue := fmt.Sprintf(
		`{"type":"ARTIFACT","id":%q,"workspace_id":%q,"version":1,"action":"REGENERATE_ARTIFACT","reason":%q,"requires_proposal":true,"artifact_binding":%s}`,
		fixture.artifactID, fixture.workspaceID, downstreamReason, artifactBindingValue,
	)
	validObjects := `[` + artifactObjectValue + `]`
	duplicateObjects := `[` + artifactObjectValue + `,` + artifactObjectValue + `]`
	extraKeyObjects := `[` + strings.TrimSuffix(artifactObjectValue, `}`) + `,"unexpected":true}]`

	assertTimelineImpactV2ProposalRevision(t, ctx, pool, fixture, validObjects, artifactOwnerBinding,
		"ARTIFACT", "REGENERATE_ARTIFACT", false, true)
	assertTimelineImpactV2ProposalRevision(t, ctx, pool, fixture, `[]`, artifactOwnerBinding,
		"ARTIFACT", "REGENERATE_ARTIFACT", false, false)
	assertTimelineImpactV2ProposalRevision(t, ctx, pool, fixture, duplicateObjects, artifactOwnerBinding,
		"ARTIFACT", "REGENERATE_ARTIFACT", false, false)
	assertTimelineImpactV2ProposalRevision(t, ctx, pool, fixture, extraKeyObjects, artifactOwnerBinding,
		"ARTIFACT", "REGENERATE_ARTIFACT", false, false)
	assertTimelineImpactV2ProposalRevision(t, ctx, pool, fixture, validObjects, artifactOwnerBinding,
		nil, "REGENERATE_ARTIFACT", false, false)
	assertTimelineImpactV2ProposalRevision(t, ctx, pool, fixture, validObjects, artifactOwnerBinding,
		"ARTIFACT", nil, false, false)
	assertTimelineImpactV2ProposalRevision(t, ctx, pool, fixture, validObjects, artifactMissingHash,
		"ARTIFACT", "REGENERATE_ARTIFACT", false, false)
	assertTimelineImpactV2ProposalRevision(t, ctx, pool, fixture, validObjects, artifactUppercaseBinding,
		"ARTIFACT", "REGENERATE_ARTIFACT", false, false)
	assertTimelineImpactV2ProposalRevision(t, ctx, pool, fixture, validObjects, artifactBracedBinding,
		"ARTIFACT", "REGENERATE_ARTIFACT", false, false)
	assertTimelineImpactV2ProposalRevision(t, ctx, pool, fixture, validObjects, artifactOwnerBinding,
		"ARTIFACT", "REGENERATE_ARTIFACT", true, false)
}

func assertTimelineImpactV2RejectedInsert(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, insertErr := tx.Exec(ctx, query, args...)
	if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if !isPostgresCode(insertErr, "23514") {
		t.Fatalf("incomplete owner binding insert error=%v", insertErr)
	}
}

func assertTimelineImpactV2ProposalRevision(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture timelineImpactV2UpgradeFixture,
	reportObjects string,
	ownerBinding string,
	targetType any,
	action any,
	driftOwner bool,
	wantSuccess bool,
) {
	t.Helper()
	const (
		proposalID         = "62200000-0000-4000-8000-000000000004"
		proposalRevisionID = "62200000-0000-4000-8000-000000000005"
		downstreamReason   = "rebuild after impact analysis"
	)
	createdAt := fixture.reportTime.Add(10 * time.Minute)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.impact_report(
		id,workspace_id,source_event_id,status,objects,summary,generated_at,schema_version,
		source_event_version,fingerprint,version,created_at,analysis_version,supersedes_report_id
	) VALUES($1,$2,$3,'READY',$4,'{}',$5,'impact-report/v2',1,$6,1,$5,
		'impact-analysis/v2',$7)`, fixture.v2ReportID, fixture.workspaceID, fixture.eventID,
		reportObjects, createdAt, strings.Repeat("b", 64), fixture.v1ReportID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("seed current v2 Impact Report: %v", err)
	}
	if driftOwner {
		if _, err := tx.Exec(ctx, `UPDATE learning.artifact
			SET version=version+1,updated_at=$3
			WHERE workspace_id=$1 AND id=$2`, fixture.workspaceID, fixture.artifactID, createdAt.Add(time.Second)); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("drift Artifact owner: %v", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,created_at,updated_at
	) VALUES($1,$2,'downstream_update','HIGH','migration-downstream-owner',$3,
		'ready_for_review',1,$4,$4)`, proposalID, fixture.workspaceID, strings.Repeat("7", 64), createdAt); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("seed downstream Proposal: %v", err)
	}
	_, insertErr := tx.Exec(ctx, `INSERT INTO change_control.proposal_revision(
		id,proposal_id,revision_no,risk,rollback_plan,change_hash,
		downstream_workspace_id,downstream_report_id,downstream_analysis_version,
		downstream_report_fingerprint,downstream_source_event_id,downstream_source_event_version,
		downstream_target_type,downstream_target_id,downstream_base_version,downstream_action,
		downstream_owner_binding,downstream_reason,schema_version,created_at
	) VALUES($1,$2,1,'owner binding validation','no target write occurred',$3,$4,$5,
		'impact-analysis/v2',$6,$7,1,$8,$9,1,$10,$11::jsonb,$12,
		'impact-downstream-update/v1',$13)`, proposalRevisionID, proposalID, strings.Repeat("8", 64),
		fixture.workspaceID, fixture.v2ReportID, strings.Repeat("b", 64), fixture.eventID,
		targetType, fixture.artifactID, action, ownerBinding, downstreamReason, createdAt)
	if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if wantSuccess {
		if insertErr != nil {
			t.Fatalf("valid downstream Proposal revision error=%v", insertErr)
		}
		return
	}
	if !isPostgresCode(insertErr, "23514") {
		t.Fatalf("invalid downstream Proposal revision error=%v", insertErr)
	}
}

func TestTimelineImpactV2MigrationOwnerEventTriggersCommitOnceAndRollback(t *testing.T) {
	for _, test := range []struct {
		name     string
		upgraded bool
	}{
		{name: "fresh_00062"},
		{name: "upgrade_00061_to_00062", upgraded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			pool, cleanup := newMigrationTestDatabase(t, ctx)
			defer cleanup()
			provider := migrationProvider(t, pool)

			var fixture timelineImpactV2OwnerEventFixture
			if test.upgraded {
				if err := provider.UpTo(ctx, 61); err != nil {
					t.Fatalf("prepare migrations through 00061: %v", err)
				}
				fixture = seedTimelineImpactV2OwnerEventFixture(t, ctx, pool)
			}
			if err := provider.UpTo(ctx, 62); err != nil {
				t.Fatalf("migrate through 00062: %v", err)
			}
			assertMigrationVersion(t, ctx, pool, 62)
			if !test.upgraded {
				fixture = seedTimelineImpactV2OwnerEventFixture(t, ctx, pool)
			}

			assertTimelineImpactV2ArtifactOwnerEventTrigger(t, ctx, pool, fixture)
			assertTimelineImpactV2ReviewOwnerEventTrigger(t, ctx, pool, fixture)
		})
	}
}

type timelineImpactV2OwnerEventFixture struct {
	workspaceID             string
	artifactID              string
	humanRevisionID         string
	generatedRevisionID     string
	reverseRevisionID       string
	rollbackRevisionID      string
	contentArtifactID       string
	sourceID                string
	sourceVersionID         string
	parseProjectionID       string
	sourceSpanID            string
	claimID                 string
	claimSourceID           string
	deckID                  string
	cardID                  string
	rollbackCardID          string
	createdAt               time.Time
	generatedContentHash    string
	reverseContentHash      string
	rollbackContentHash     string
	sourceContentHash       string
	evidenceHash            string
	cardFingerprint         string
	rollbackCardFingerprint string
}

func seedTimelineImpactV2OwnerEventFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) timelineImpactV2OwnerEventFixture {
	t.Helper()
	fixture := timelineImpactV2OwnerEventFixture{
		workspaceID:             "62100000-0000-4000-8000-000000000001",
		artifactID:              "62100000-0000-4000-8000-000000000002",
		humanRevisionID:         "62100000-0000-4000-8000-000000000003",
		generatedRevisionID:     "62100000-0000-4000-8000-000000000004",
		reverseRevisionID:       "62100000-0000-4000-8000-000000000005",
		rollbackRevisionID:      "62100000-0000-4000-8000-000000000006",
		contentArtifactID:       "62100000-0000-4000-8000-000000000007",
		sourceID:                "62100000-0000-4000-8000-000000000008",
		sourceVersionID:         "62100000-0000-4000-8000-000000000009",
		parseProjectionID:       "62100000-0000-4000-8000-000000000010",
		sourceSpanID:            "62100000-0000-4000-8000-000000000011",
		claimID:                 "62100000-0000-4000-8000-000000000012",
		claimSourceID:           "62100000-0000-4000-8000-000000000013",
		deckID:                  "62100000-0000-4000-8000-000000000014",
		cardID:                  "62100000-0000-4000-8000-000000000015",
		rollbackCardID:          "62100000-0000-4000-8000-000000000016",
		createdAt:               time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
		generatedContentHash:    strings.Repeat("a", 64),
		reverseContentHash:      strings.Repeat("7", 64),
		rollbackContentHash:     strings.Repeat("b", 64),
		sourceContentHash:       strings.Repeat("2", 64),
		evidenceHash:            strings.Repeat("3", 64),
		cardFingerprint:         strings.Repeat("c", 64),
		rollbackCardFingerprint: strings.Repeat("d", 64),
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES($1,'timeline-impact-v2-owner-events','/tmp/timeline-impact-v2-owner-events',
			'/tmp/timeline-impact-v2-owner-events',$2,'test',1,$2,$2)`, fixture.workspaceID, fixture.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO core.content_artifact(
			id,workspace_id,content_hash,byte_size,managed_location,created_at
		) VALUES($1,$2,$3,4,'.knowledge/sources/' || $3,$4)`, fixture.contentArtifactID,
			fixture.workspaceID, fixture.sourceContentHash, fixture.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO core.source(
			id,workspace_id,type,logical_name,original_location,created_at
		) VALUES($1,$2,'text','timeline-owner-event.txt','timeline-owner-event.txt',$3)`, fixture.sourceID,
			fixture.workspaceID, fixture.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO core.source_version(
			id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,
			original_content_location,security_status,captured_at
		) VALUES($1,$2,$3,$4,$5,4,'text/plain','timeline-owner-event.txt','passed',$6)`, fixture.sourceVersionID,
			fixture.sourceID, fixture.workspaceID, fixture.contentArtifactID, fixture.sourceContentHash, fixture.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ingestion.parse_projection(
			id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,
			schema_version,normalized_content_hash,warnings,created_at
		) VALUES($1,$2,$3,'text','v1',$4,'v1',$5,'[]',$6)`, fixture.parseProjectionID,
			fixture.workspaceID, fixture.contentArtifactID, strings.Repeat("4", 64),
			strings.Repeat("5", 64), fixture.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ingestion.source_span(
			id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,
			start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at
		) VALUES($1,$2,$3,$4,'paragraph',1,1,0,4,'{}',$5,'v1','v1',$6)`, fixture.sourceSpanID,
			fixture.workspaceID, fixture.contentArtifactID, fixture.parseProjectionID,
			strings.Repeat("6", 64), fixture.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ingestion.source_version_projection(
			source_version_id,parse_projection_id,workspace_id,created_at
		) VALUES($1,$2,$3,$4)`, fixture.sourceVersionID, fixture.parseProjectionID,
			fixture.workspaceID, fixture.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO learning.artifact(
			id,workspace_id,artifact_type,title,scope,status,version,created_at,updated_at,
			domain_schema_version,scope_definition,source_coverage,current_revision_id
		) VALUES($1,$2,'CUSTOM','Timeline owner event Artifact','{}','GENERATING',1,$3,$3,
			'artifact/v1','owner event fixture','[]',$4)`, fixture.artifactID, fixture.workspaceID,
			fixture.createdAt, fixture.humanRevisionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO learning.artifact_revision(
			id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,
			conflicts,content_markdown,provenance,created_at,domain_schema_version,content_hash,
			created_by_type,generation_metadata
		) VALUES($1,$2,$3,1,'SNAPSHOT','[]','[]','[]','[]','[]','','{}',$4,
			'artifact-revision/v1',$5,'HUMAN',NULL)`, fixture.humanRevisionID, fixture.artifactID,
			fixture.workspaceID, fixture.createdAt, strings.Repeat("e", 64)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO core.claim(
			id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,
			applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at
		) VALUES($1,$2,'Timeline owner event claim','timeline owner event claim','{}',
			'knowledge-applicability/v1',$3,'SUGGESTED',0.9,'{}',$4,1,$5,$5)`, fixture.claimID,
			fixture.workspaceID, strings.Repeat("f", 64), strings.Repeat("1", 64), fixture.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO core.claim_source(
			id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at
		) VALUES($1,$2,$3,$4,$5,'SUPPORTS','timeline owner event fixture',$6,$7)`, fixture.claimSourceID,
			fixture.workspaceID, fixture.claimID, fixture.sourceVersionID, fixture.sourceSpanID,
			fixture.evidenceHash, fixture.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE core.claim
			SET status='CONFIRMED',version=2,updated_at=$3
			WHERE workspace_id=$1 AND id=$2 AND status='SUGGESTED' AND version=1`, fixture.workspaceID,
			fixture.claimID, fixture.createdAt.Add(time.Second)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO learning.review_deck(
			id,workspace_id,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at
		) VALUES($1,$2,'Timeline owner event deck','{}','ACTIVE',20,'fsrs/v1',1,$3,$3)`, fixture.deckID,
			fixture.workspaceID, fixture.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO learning.review_card(
			id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,
			status,fingerprint,model_version,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,'Which owner transaction emits this event?','["owner transaction"]',
			jsonb_build_array(jsonb_build_object(
				'schema_version','review-evidence/v1','claim_id',$4::uuid,
				'source_version_id',$5::uuid,'source_span_id',$6::uuid,'evidence_hash',$7::text
			)),'SHORT_ANSWER',0.5,'APPROVED',$8,'manual',1,$9,$9)`, fixture.cardID,
			fixture.workspaceID, fixture.deckID, fixture.claimID, fixture.sourceVersionID,
			fixture.sourceSpanID, fixture.evidenceHash, fixture.cardFingerprint, fixture.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO learning.review_schedule(
			card_id,workspace_id,due_at,interval_days,stability,difficulty,last_reviewed_at,scheduler_version,paused,version
		) VALUES($1,$2,$3,0,0,0.5,NULL,'fsrs/v1',false,1)`, fixture.cardID,
			fixture.workspaceID, fixture.createdAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO learning.review_card(
			id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,
			status,fingerprint,model_version,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,'Which rollback leaves no source?','["rollback"]',
			jsonb_build_array(jsonb_build_object(
				'schema_version','review-evidence/v1','claim_id',$4::uuid,
				'source_version_id',$5::uuid,'source_span_id',$6::uuid,'evidence_hash',$7::text
			)),'SHORT_ANSWER',0.5,'DRAFT',$8,'manual',1,$9,$9)`, fixture.rollbackCardID,
			fixture.workspaceID, fixture.deckID, fixture.claimID, fixture.sourceVersionID,
			fixture.sourceSpanID, fixture.evidenceHash, fixture.rollbackCardFingerprint, fixture.createdAt)
		return err
	}); err != nil {
		t.Fatalf("seed owner event fixture: %v", err)
	}
	return fixture
}

func assertTimelineImpactV2ArtifactOwnerEventTrigger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture timelineImpactV2OwnerEventFixture) {
	t.Helper()
	generatedAt := fixture.createdAt.Add(time.Minute)
	generatedRef := "artifact-generated:" + fixture.artifactID + ":" + fixture.generatedRevisionID + ":v2"
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := insertTimelineImpactV2AgentRevision(ctx, tx, fixture, fixture.generatedRevisionID, 2, fixture.generatedContentHash, generatedAt); err != nil {
			return err
		}
		if err := assertTimelineImpactV2ArtifactSelectorCount(ctx, tx, fixture, fixture.generatedRevisionID, 1); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE learning.artifact
			SET status='DRAFT',source_coverage='[]',current_revision_id=$3,version=2,updated_at=$4
			WHERE workspace_id=$1 AND id=$2 AND version=1 AND current_revision_id=$5`, fixture.workspaceID,
			fixture.artifactID, fixture.generatedRevisionID, generatedAt, fixture.humanRevisionID); err != nil {
			return err
		}
		return assertTimelineImpactV2OwnerSourceCountTx(ctx, tx, fixture.workspaceID, generatedRef, 0)
	}); err != nil {
		t.Fatalf("commit generated Artifact owner transition: %v", err)
	}
	assertTimelineImpactV2ArtifactGeneratedSource(t, ctx, pool, fixture, generatedRef, generatedAt,
		2, fixture.generatedRevisionID, 2, fixture.generatedContentHash)
	if err := assertTimelineImpactV2ArtifactSelectorCount(ctx, pool, fixture, fixture.generatedRevisionID, 1); err != nil {
		t.Fatal(err)
	}
	assertTimelineImpactV2OwnerEventCount(t, ctx, pool, fixture.workspaceID, "ARTIFACT_GENERATED", "ARTIFACT", fixture.artifactID, 1)

	reverseAt := generatedAt.Add(2 * time.Minute)
	reverseRef := "artifact-generated:" + fixture.artifactID + ":" + fixture.reverseRevisionID + ":v3"
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE learning.artifact
			SET current_revision_id=$3,version=3,updated_at=$4
			WHERE workspace_id=$1 AND id=$2 AND version=2 AND current_revision_id=$5`, fixture.workspaceID,
			fixture.artifactID, fixture.reverseRevisionID, reverseAt, fixture.generatedRevisionID); err != nil {
			return err
		}
		if err := assertTimelineImpactV2OwnerSourceCountTx(ctx, tx, fixture.workspaceID, reverseRef, 0); err != nil {
			return err
		}
		if err := insertTimelineImpactV2AgentRevision(ctx, tx, fixture, fixture.reverseRevisionID, 3, fixture.reverseContentHash, reverseAt); err != nil {
			return err
		}
		if err := assertTimelineImpactV2ArtifactSelectorCount(ctx, tx, fixture, fixture.reverseRevisionID, 1); err != nil {
			return err
		}
		return assertTimelineImpactV2OwnerSourceCountTx(ctx, tx, fixture.workspaceID, reverseRef, 0)
	}); err != nil {
		t.Fatalf("commit reverse-order Artifact owner transition: %v", err)
	}
	assertTimelineImpactV2ArtifactGeneratedSource(t, ctx, pool, fixture, reverseRef, reverseAt,
		3, fixture.reverseRevisionID, 3, fixture.reverseContentHash)
	assertTimelineImpactV2OwnerEventCount(t, ctx, pool, fixture.workspaceID, "ARTIFACT_GENERATED", "ARTIFACT", fixture.artifactID, 2)

	if _, err := pool.Exec(ctx, `UPDATE learning.artifact
		SET current_revision_id=current_revision_id,updated_at=$3
		WHERE workspace_id=$1 AND id=$2`, fixture.workspaceID, fixture.artifactID, reverseAt.Add(time.Minute)); err != nil {
		t.Fatalf("repeat generated Artifact owner update: %v", err)
	}
	assertTimelineImpactV2OwnerEventCount(t, ctx, pool, fixture.workspaceID, "ARTIFACT_GENERATED", "ARTIFACT", fixture.artifactID, 2)

	rollbackAt := reverseAt.Add(2 * time.Minute)
	rollbackRef := "artifact-generated:" + fixture.artifactID + ":" + fixture.rollbackRevisionID + ":v4"
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertTimelineImpactV2AgentRevision(ctx, tx, fixture, fixture.rollbackRevisionID, 4, fixture.rollbackContentHash, rollbackAt); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE learning.artifact
		SET current_revision_id=$3,version=4,updated_at=$4
		WHERE workspace_id=$1 AND id=$2 AND version=3 AND current_revision_id=$5`, fixture.workspaceID,
		fixture.artifactID, fixture.rollbackRevisionID, rollbackAt, fixture.reverseRevisionID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := assertTimelineImpactV2OwnerSourceCountTx(ctx, tx, fixture.workspaceID, rollbackRef, 0); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertTimelineImpactV2OwnerSourceCount(t, ctx, pool, fixture.workspaceID, rollbackRef, 0)
	if err := assertTimelineImpactV2ArtifactSelectorCount(ctx, pool, fixture, fixture.rollbackRevisionID, 0); err != nil {
		t.Fatal(err)
	}

	var currentRevisionID string
	if err := pool.QueryRow(ctx, `SELECT current_revision_id::text FROM learning.artifact
		WHERE workspace_id=$1 AND id=$2`, fixture.workspaceID, fixture.artifactID).Scan(&currentRevisionID); err != nil {
		t.Fatal(err)
	}
	if currentRevisionID != fixture.reverseRevisionID {
		t.Fatalf("rolled back Artifact current revision=%s want=%s", currentRevisionID, fixture.reverseRevisionID)
	}
}

func insertTimelineImpactV2AgentRevision(ctx context.Context, tx pgx.Tx, fixture timelineImpactV2OwnerEventFixture, revisionID string, revisionNo int, contentHash string, createdAt time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO learning.artifact_revision(
		id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,
		conflicts,content_markdown,provenance,created_at,domain_schema_version,content_hash,
		created_by_type,generation_metadata
	) VALUES($1,$2,$3,$4,'SNAPSHOT',
		jsonb_build_array(jsonb_build_object('Key','summary','Title','Summary')),
		jsonb_build_array(jsonb_build_object(
			'Key','summary','Title','Summary','Content','generated content',
			'Citations',jsonb_build_array(
				jsonb_build_object(
					'SourceVersionID',$7::uuid,'SourceSpanID',$8::uuid,
					'VerifiedContentHash',$9::text,'Excerpt','verified source','Verified',true
				),
				jsonb_build_object(
					'SourceVersionID',$7::uuid,'SourceSpanID',$8::uuid,
					'VerifiedContentHash',$9::text,'Excerpt','verified source','Verified',true
				)
			),
			'Coverage',jsonb_build_object('SectionKey','summary','Status','COVERED','Gaps','[]'::jsonb)
		)),
		jsonb_build_array(jsonb_build_object('SectionKey','summary','Status','COVERED','Gaps','[]'::jsonb)),
		'[]','[]','generated content','{}',$5,'artifact-revision/v1',$6,'AGENT','{}'
	)`, revisionID, fixture.artifactID, fixture.workspaceID, revisionNo, createdAt, contentHash,
		fixture.sourceVersionID, fixture.sourceSpanID, fixture.sourceContentHash)
	return err
}

func assertTimelineImpactV2ArtifactGeneratedSource(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture timelineImpactV2OwnerEventFixture,
	sourceEventRef string,
	occurredAt time.Time,
	wantArtifactVersion int64,
	wantRevisionID string,
	wantRevisionNo int64,
	wantContentHash string,
) {
	t.Helper()
	var eventType, aggregateType, aggregateID, sourceRef, schemaVersion, operatorType, summary, correlation, status string
	var operatorID *string
	var eventVersion, outboxVersion, artifactVersion, revisionNo int64
	var sourceOccurredAt time.Time
	var bindingArtifactID, bindingRevisionID, bindingContentHash string
	if err := pool.QueryRow(ctx, `SELECT event_type,aggregate_type,aggregate_id::text,source_ref,event_version,
		schema_version,operator_type,operator_id::text,summary,correlation::text,occurred_at,status,version,
		owner_binding->'artifact'->>'artifact_id',
		(owner_binding->'artifact'->>'artifact_version')::bigint,
		owner_binding->'artifact'->>'revision_id',
		(owner_binding->'artifact'->>'revision_no')::bigint,
		owner_binding->'artifact'->>'content_hash'
		FROM ops.timeline_projection_outbox
		WHERE workspace_id=$1 AND source_event_ref=$2`, fixture.workspaceID, sourceEventRef).Scan(
		&eventType, &aggregateType, &aggregateID, &sourceRef, &eventVersion,
		&schemaVersion, &operatorType, &operatorID, &summary, &correlation, &sourceOccurredAt, &status, &outboxVersion,
		&bindingArtifactID, &artifactVersion, &bindingRevisionID, &revisionNo, &bindingContentHash,
	); err != nil {
		t.Fatal(err)
	}
	if eventType != "ARTIFACT_GENERATED" || aggregateType != "ARTIFACT" || aggregateID != fixture.artifactID ||
		sourceRef != "artifact:"+fixture.artifactID || eventVersion != wantArtifactVersion || schemaVersion != "knowledge-event/v2" ||
		operatorType != "SYSTEM" || operatorID != nil || summary != "Artifact revision generated" || correlation != "{}" ||
		status != "PENDING" || outboxVersion != 1 || !sourceOccurredAt.Equal(occurredAt) || bindingArtifactID != fixture.artifactID ||
		artifactVersion != wantArtifactVersion || bindingRevisionID != wantRevisionID || revisionNo != wantRevisionNo ||
		bindingContentHash != wantContentHash {
		t.Fatalf("Artifact generated source binding type=%s aggregate=%s/%s source=%s event_version=%d schema=%s operator=%s/%v summary=%q correlation=%s status=%s occurred_at=%s outbox_version=%d binding=%s/%d/%s/%d/%s",
			eventType, aggregateType, aggregateID, sourceRef, eventVersion, schemaVersion, operatorType, operatorID,
			summary, correlation, status, sourceOccurredAt, outboxVersion, bindingArtifactID, artifactVersion,
			bindingRevisionID, revisionNo, bindingContentHash)
	}
}

type timelineImpactV2QueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func assertTimelineImpactV2ArtifactSelectorCount(
	ctx context.Context,
	db timelineImpactV2QueryRower,
	fixture timelineImpactV2OwnerEventFixture,
	revisionID string,
	want int,
) error {
	var count int
	var sourceVersionID, sourceSpanID string
	if err := db.QueryRow(ctx, `SELECT count(*),
		COALESCE(min(source_version_id::text),''),COALESCE(min(source_span_id::text),'')
		FROM learning.artifact_revision_citation_selector
		WHERE workspace_id=$1 AND artifact_id=$2 AND revision_id=$3`, fixture.workspaceID,
		fixture.artifactID, revisionID).Scan(&count, &sourceVersionID, &sourceSpanID); err != nil {
		return err
	}
	if count != want {
		return fmt.Errorf("Artifact revision %s selector count=%d want=%d", revisionID, count, want)
	}
	if want == 1 && (sourceVersionID != fixture.sourceVersionID || sourceSpanID != fixture.sourceSpanID) {
		return fmt.Errorf("Artifact revision %s selector=%s/%s want=%s/%s", revisionID,
			sourceVersionID, sourceSpanID, fixture.sourceVersionID, fixture.sourceSpanID)
	}
	return nil
}

func assertTimelineImpactV2ReviewOwnerEventTrigger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture timelineImpactV2OwnerEventFixture) {
	t.Helper()
	invalidatedAt := fixture.createdAt.Add(3 * time.Minute)
	invalidatedRef := "review-card-invalidated:" + fixture.cardID + ":v2"
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE learning.review_card
			SET status='INVALIDATED',invalidation_reason='M7_OWNER_EVENT_TEST',invalidated_at=$3,version=2,updated_at=$3
			WHERE workspace_id=$1 AND id=$2 AND status='APPROVED' AND version=1`, fixture.workspaceID, fixture.cardID, invalidatedAt); err != nil {
			return err
		}
		return assertTimelineImpactV2OwnerSourceCountTx(ctx, tx, fixture.workspaceID, invalidatedRef, 1)
	}); err != nil {
		t.Fatalf("commit invalidated Review Card owner transition: %v", err)
	}
	assertTimelineImpactV2ReviewCardInvalidatedSource(t, ctx, pool, fixture, invalidatedRef, invalidatedAt)
	assertTimelineImpactV2OwnerEventCount(t, ctx, pool, fixture.workspaceID, "REVIEW_CARD_INVALIDATED", "REVIEW_CARD", fixture.cardID, 1)

	if _, err := pool.Exec(ctx, `UPDATE learning.review_card
		SET status='INVALIDATED',version=version+1,updated_at=$3
		WHERE workspace_id=$1 AND id=$2`, fixture.workspaceID, fixture.cardID, invalidatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("repeat invalidated Review Card owner update: %v", err)
	}
	assertTimelineImpactV2OwnerEventCount(t, ctx, pool, fixture.workspaceID, "REVIEW_CARD_INVALIDATED", "REVIEW_CARD", fixture.cardID, 1)

	rollbackAt := invalidatedAt.Add(2 * time.Minute)
	rollbackRef := "review-card-invalidated:" + fixture.rollbackCardID + ":v2"
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE learning.review_card
		SET status='INVALIDATED',invalidation_reason='M7_OWNER_EVENT_ROLLBACK',invalidated_at=$3,version=2,updated_at=$3
		WHERE workspace_id=$1 AND id=$2 AND status='DRAFT' AND version=1`, fixture.workspaceID, fixture.rollbackCardID, rollbackAt); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := assertTimelineImpactV2OwnerSourceCountTx(ctx, tx, fixture.workspaceID, rollbackRef, 1); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertTimelineImpactV2OwnerSourceCount(t, ctx, pool, fixture.workspaceID, rollbackRef, 0)

	var status string
	var version int64
	if err := pool.QueryRow(ctx, `SELECT status,version FROM learning.review_card
		WHERE workspace_id=$1 AND id=$2`, fixture.workspaceID, fixture.rollbackCardID).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if status != "DRAFT" || version != 1 {
		t.Fatalf("rolled back Review Card status=%s version=%d", status, version)
	}
}

func assertTimelineImpactV2ReviewCardInvalidatedSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture timelineImpactV2OwnerEventFixture, sourceEventRef string, occurredAt time.Time) {
	t.Helper()
	evidenceBindingDigest := sha256.Sum256([]byte(fixture.sourceVersionID + ":" + fixture.sourceSpanID))
	expectedEvidenceBindingFingerprint := fmt.Sprintf("%x", evidenceBindingDigest)
	var eventType, aggregateType, aggregateID, sourceRef, schemaVersion, operatorType, summary, correlation, status string
	var operatorID *string
	var eventVersion, outboxVersion, cardVersion int64
	var sourceOccurredAt time.Time
	var bindingCardID, bindingStatus, bindingFingerprint, bindingClaimID, evidenceBindingFingerprint string
	if err := pool.QueryRow(ctx, `SELECT event_type,aggregate_type,aggregate_id::text,source_ref,event_version,
		schema_version,operator_type,operator_id::text,summary,correlation::text,occurred_at,status,version,
		owner_binding->'review_card'->>'card_id',
		(owner_binding->'review_card'->>'card_version')::bigint,
		owner_binding->'review_card'->>'status',
		owner_binding->'review_card'->>'fingerprint',
		owner_binding->'review_card'->>'claim_id',
		owner_binding->'review_card'->>'evidence_binding_fingerprint'
		FROM ops.timeline_projection_outbox
		WHERE workspace_id=$1 AND source_event_ref=$2`, fixture.workspaceID, sourceEventRef).Scan(
		&eventType, &aggregateType, &aggregateID, &sourceRef, &eventVersion,
		&schemaVersion, &operatorType, &operatorID, &summary, &correlation, &sourceOccurredAt, &status, &outboxVersion,
		&bindingCardID, &cardVersion, &bindingStatus, &bindingFingerprint, &bindingClaimID, &evidenceBindingFingerprint,
	); err != nil {
		t.Fatal(err)
	}
	if eventType != "REVIEW_CARD_INVALIDATED" || aggregateType != "REVIEW_CARD" || aggregateID != fixture.cardID ||
		sourceRef != "review-card:"+fixture.cardID || eventVersion != 2 || schemaVersion != "knowledge-event/v2" ||
		operatorType != "SYSTEM" || operatorID != nil || summary != "Review card invalidated" || correlation != "{}" ||
		status != "PENDING" || outboxVersion != 1 || !sourceOccurredAt.Equal(occurredAt) || bindingCardID != fixture.cardID ||
		cardVersion != 2 || bindingStatus != "INVALIDATED" || bindingFingerprint != fixture.cardFingerprint ||
		bindingClaimID != fixture.claimID || evidenceBindingFingerprint != expectedEvidenceBindingFingerprint {
		t.Fatalf("Review invalidated source binding type=%s aggregate=%s/%s source=%s event_version=%d schema=%s operator=%s/%v summary=%q correlation=%s status=%s occurred_at=%s outbox_version=%d binding=%s/%d/%s/%s/%s/%s",
			eventType, aggregateType, aggregateID, sourceRef, eventVersion, schemaVersion, operatorType, operatorID,
			summary, correlation, status, sourceOccurredAt, outboxVersion, bindingCardID, cardVersion,
			bindingStatus, bindingFingerprint, bindingClaimID, evidenceBindingFingerprint)
	}
}

func assertTimelineImpactV2OwnerSourceCountTx(ctx context.Context, tx pgx.Tx, workspaceID, sourceEventRef string, want int) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ops.timeline_projection_outbox
		WHERE workspace_id=$1 AND source_event_ref=$2`, workspaceID, sourceEventRef).Scan(&count); err != nil {
		return err
	}
	if count != want {
		return fmt.Errorf("Timeline owner source %s count=%d want=%d", sourceEventRef, count, want)
	}
	return nil
}

func assertTimelineImpactV2OwnerSourceCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, sourceEventRef string, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.timeline_projection_outbox
		WHERE workspace_id=$1 AND source_event_ref=$2`, workspaceID, sourceEventRef).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("Timeline owner source %s count=%d want=%d", sourceEventRef, count, want)
	}
}

func assertTimelineImpactV2OwnerEventCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, eventType, aggregateType, aggregateID string, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.timeline_projection_outbox
		WHERE workspace_id=$1 AND event_type=$2 AND aggregate_type=$3 AND aggregate_id=$4`, workspaceID,
		eventType, aggregateType, aggregateID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("Timeline owner event %s/%s/%s count=%d want=%d", eventType, aggregateType, aggregateID, count, want)
	}
}
