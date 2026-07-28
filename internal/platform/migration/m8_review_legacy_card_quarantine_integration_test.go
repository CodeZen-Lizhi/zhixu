//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM8ReviewLegacyCardQuarantineMigration(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 51); err != nil {
		t.Fatalf("prepare through 00051: %v", err)
	}

	if _, err := provider.UpTo(ctx, 52); err != nil {
		t.Fatalf("00052 empty up: %v", err)
	}
	if _, err := provider.UpTo(ctx, 52); err != nil {
		t.Fatalf("00052 repeated up: %v", err)
	}
	if _, err := provider.DownTo(ctx, 51); err != nil {
		t.Fatalf("00052 empty down: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 51)

	fixture := seedM8ReviewLegacyQuarantineFixture(t, ctx, pool)
	if _, err := provider.UpTo(ctx, 52); err != nil {
		t.Fatalf("00052 legacy quarantine up: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 52)

	assertM8ReviewQuarantinedCard(t, ctx, pool, fixture.workspaceID, fixture.malformedCardID, "LEGACY_EVIDENCE_UNVERIFIABLE")
	assertM8ReviewQuarantinedCard(t, ctx, pool, fixture.workspaceID, fixture.disputedCardID, "CLAIM_DISPUTED")
	assertM8ReviewQuarantineProjection(t, ctx, pool, fixture.workspaceID, fixture.malformedCardID, fixture.malformedClaimID)
	assertM8ReviewQuarantineProjection(t, ctx, pool, fixture.workspaceID, fixture.disputedCardID, fixture.disputedClaimID)

	openM8ReviewLowConflict(t, ctx, pool, fixture.workspaceID, fixture.futureClaimID, fixture.futurePeerClaimID, "7c000000-0000-4000-8000-000000000030", "future-low-conflict", fixture.now.Add(5*time.Minute))
	assertM8ReviewQuarantinedCard(t, ctx, pool, fixture.workspaceID, fixture.futureCardID, "CLAIM_DISPUTED")
	assertM8ReviewQuarantineProjection(t, ctx, pool, fixture.workspaceID, fixture.futureCardID, fixture.futureClaimID)

	if _, err := provider.DownTo(ctx, 51); err == nil {
		t.Fatal("00052 down discarded explicit Review quarantine history")
	} else {
		assertPostgresCode(t, err, "55000")
	}
	assertMigrationVersion(t, ctx, pool, 52)
}

type m8ReviewLegacyQuarantineFixture struct {
	workspaceID       string
	malformedClaimID  string
	malformedCardID   string
	disputedClaimID   string
	disputedCardID    string
	futureClaimID     string
	futurePeerClaimID string
	futureCardID      string
	now               time.Time
}

func seedM8ReviewLegacyQuarantineFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) m8ReviewLegacyQuarantineFixture {
	t.Helper()
	fixture := m8ReviewLegacyQuarantineFixture{
		workspaceID:       "7c000000-0000-4000-8000-000000000001",
		malformedClaimID:  "7c000000-0000-4000-8000-000000000010",
		malformedCardID:   "7c000000-0000-4000-8000-000000000020",
		disputedClaimID:   "7c000000-0000-4000-8000-000000000011",
		disputedCardID:    "7c000000-0000-4000-8000-000000000021",
		futureClaimID:     "7c000000-0000-4000-8000-000000000013",
		futurePeerClaimID: "7c000000-0000-4000-8000-000000000014",
		futureCardID:      "7c000000-0000-4000-8000-000000000022",
		now:               time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC),
	}
	const (
		artifactID      = "7c000000-0000-4000-8000-000000000002"
		sourceID        = "7c000000-0000-4000-8000-000000000003"
		sourceVersionID = "7c000000-0000-4000-8000-000000000004"
		projectionID    = "7c000000-0000-4000-8000-000000000005"
		spanID          = "7c000000-0000-4000-8000-000000000006"
		deckID          = "7c000000-0000-4000-8000-000000000007"
	)
	contentHash := strings.Repeat("a", 64)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
			VALUES($1,'m8-review-legacy-quarantine','/tmp/m8-review-legacy-quarantine','/tmp/m8-review-legacy-quarantine',$2,'test',1,$2,$2)`, []any{fixture.workspaceID, fixture.now}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
			VALUES($1,$2,$3,4,'.knowledge/sources/' || $3,$4)`, []any{artifactID, fixture.workspaceID, contentHash, fixture.now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
			VALUES($1,$2,'text','m8-review.txt','m8-review.txt',$3)`, []any{sourceID, fixture.workspaceID, fixture.now}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at)
			VALUES($1,$2,$3,$4,$5,4,'text/plain','m8-review.txt','passed',$6)`, []any{sourceVersionID, sourceID, fixture.workspaceID, artifactID, contentHash, fixture.now}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at)
			VALUES($1,$2,$3,'text','v1',repeat('b',64),'v1',repeat('c',64),'[]',$4)`, []any{projectionID, fixture.workspaceID, artifactID, fixture.now}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at)
			VALUES($1,$2,$3,$4,'paragraph',1,1,0,4,'{}',repeat('d',64),'v1','v1',$5)`, []any{spanID, fixture.workspaceID, artifactID, projectionID, fixture.now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
			VALUES($1,$2,$3,$4)`, []any{sourceVersionID, projectionID, fixture.workspaceID, fixture.now}},
		{`INSERT INTO learning.review_deck(id,workspace_id,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at)
			VALUES($1,$2,'M8 legacy quarantine','{}','ACTIVE',20,'fsrs/v1',1,$3,$3)`, []any{deckID, fixture.workspaceID, fixture.now}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	claimIDs := []string{
		fixture.malformedClaimID,
		fixture.disputedClaimID,
		"7c000000-0000-4000-8000-000000000012",
		fixture.futureClaimID,
		fixture.futurePeerClaimID,
	}
	for index, claimID := range claimIDs {
		seedM8ReviewQuarantineClaim(t, ctx, pool, fixture.workspaceID, claimID, sourceVersionID, spanID, index, fixture.now)
	}
	openM8ReviewLowConflict(t, ctx, pool, fixture.workspaceID, fixture.disputedClaimID, claimIDs[2], "7c000000-0000-4000-8000-000000000031", "legacy-low-conflict", fixture.now.Add(time.Minute))

	malformedEvidence := `[{"schema_version":"review-evidence/v1","claim_id":"` + fixture.malformedClaimID + `","source_version_id":"not-a-uuid","source_span_id":"` + spanID + `","evidence_hash":"` + strings.Repeat("1", 64) + `"}]`
	disputedEvidence := m8ReviewQuarantineEvidence(fixture.disputedClaimID, sourceVersionID, spanID, strings.Repeat("2", 64))
	futureEvidence := m8ReviewQuarantineEvidence(fixture.futureClaimID, sourceVersionID, spanID, strings.Repeat("4", 64))
	seedM8ReviewApprovedCard(t, ctx, pool, fixture.workspaceID, deckID, fixture.malformedClaimID, fixture.malformedCardID, malformedEvidence, strings.Repeat("6", 64), fixture.now.Add(2*time.Minute))
	seedM8ReviewApprovedCard(t, ctx, pool, fixture.workspaceID, deckID, fixture.disputedClaimID, fixture.disputedCardID, disputedEvidence, strings.Repeat("7", 64), fixture.now.Add(2*time.Minute))
	seedM8ReviewApprovedCard(t, ctx, pool, fixture.workspaceID, deckID, fixture.futureClaimID, fixture.futureCardID, futureEvidence, strings.Repeat("8", 64), fixture.now.Add(2*time.Minute))
	return fixture
}

func seedM8ReviewQuarantineClaim(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, claimID, sourceVersionID, spanID string, index int, now time.Time) {
	t.Helper()
	evidenceHash := strings.Repeat(string(rune('1'+index)), 64)
	claimSourceID := "7c000000-0000-4000-8000-" + string(rune('1'+index)) + "0000000000" + string(rune('1'+index))
	if _, err := pool.Exec(ctx, `INSERT INTO core.claim(
		id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,
		applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,'{}','knowledge-applicability/v1',repeat('e',64),'SUGGESTED',0.9,'{}',$4,1,$5,$5)`,
		claimID, workspaceID, "M8 Review claim "+claimID, strings.Repeat(string(rune('a'+index)), 64), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.claim_source(
		id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at
	) VALUES($1,$2,$3,$4,$5,'SUPPORTS','M8 Review migration fixture',$6,$7)`,
		claimSourceID, workspaceID, claimID, sourceVersionID, spanID, evidenceHash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.claim SET status='CONFIRMED',version=2,updated_at=$3
		WHERE workspace_id=$1 AND id=$2`, workspaceID, claimID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func seedM8ReviewApprovedCard(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, deckID, claimID, cardID, evidence, fingerprint string, now time.Time) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `INSERT INTO learning.review_card(
		id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,
		status,fingerprint,model_version,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,'Which binding is trustworthy?','["Only verified evidence"]',$5,
			'SHORT_ANSWER',0.5,'APPROVED',$6,'legacy',1,$7,$7)`, cardID, workspaceID, deckID, claimID, evidence, fingerprint, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO learning.review_schedule(
		card_id,workspace_id,due_at,interval_days,stability,difficulty,last_reviewed_at,scheduler_version,paused,version
	) VALUES($1,$2,$3,0,0,0.5,NULL,'fsrs/v1',false,1)`, cardID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func openM8ReviewLowConflict(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, firstClaimID, secondClaimID, conflictID, fingerprintSeed string, now time.Time) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `INSERT INTO core.conflict(
		id,workspace_id,status,severity,summary,applicability_assessment,applicability_hash,
		fingerprint,version,created_at,updated_at
	) VALUES($1,$2,'OPEN','LOW','M8 Review low conflict','EXACT',repeat('e',64),md5($3) || md5($3 || ':conflict'),1,$4,$4)`,
		conflictID, workspaceID, fingerprintSeed, now); err != nil {
		t.Fatal(err)
	}
	for index, claimID := range []string{firstClaimID, secondClaimID} {
		if _, err := tx.Exec(ctx, `INSERT INTO core.conflict_member(
			conflict_id,claim_id,workspace_id,applicability,applicability_schema_version,
			applicability_hash,position_summary,created_at
		) VALUES($1,$2,$3,'{}','knowledge-applicability/v1',repeat('e',64),$4,$5)`,
			conflictID, claimID, workspaceID, "Position "+string(rune('A'+index)), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE core.claim
		SET status='DISPUTED',version=version+1,updated_at=$3
		WHERE workspace_id=$1 AND id=ANY($2::uuid[]) AND status='CONFIRMED'`,
		workspaceID, []string{firstClaimID, secondClaimID}, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func m8ReviewQuarantineEvidence(claimID, sourceVersionID, spanID, evidenceHash string) string {
	return `[{"schema_version":"review-evidence/v1","claim_id":"` + claimID + `","source_version_id":"` + sourceVersionID + `","source_span_id":"` + spanID + `","evidence_hash":"` + evidenceHash + `"}]`
}

func assertM8ReviewQuarantinedCard(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, cardID, wantReason string) {
	t.Helper()
	var status, reason string
	var version, scheduleCount int
	if err := pool.QueryRow(ctx, `SELECT card.status,card.invalidation_reason,card.version,
		(SELECT count(*) FROM learning.review_schedule AS schedule
		 WHERE schedule.workspace_id=card.workspace_id AND schedule.card_id=card.id)
		FROM learning.review_card AS card WHERE card.workspace_id=$1 AND card.id=$2`, workspaceID, cardID).
		Scan(&status, &reason, &version, &scheduleCount); err != nil {
		t.Fatal(err)
	}
	if status != "INVALIDATED" || reason != wantReason || version != 2 || scheduleCount != 0 {
		t.Fatalf("card %s status=%s reason=%s version=%d schedules=%d", cardID, status, reason, version, scheduleCount)
	}
}

func assertM8ReviewQuarantineProjection(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, cardID, claimID string) {
	t.Helper()
	var cardEvents, scheduleEvents, healthIssues int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND event_type='review.card.updated' AND resource_ref='review_card:' || $2),
		(SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND event_type='review.schedule.deleted' AND resource_ref='review_schedule:' || $2),
		(SELECT count(*) FROM ops.health_issue WHERE workspace_id=$1 AND type='REVIEW_INVALIDATED' AND target_type='CLAIM' AND target_id=$3)`,
		workspaceID, cardID, claimID).Scan(&cardEvents, &scheduleEvents, &healthIssues); err != nil {
		t.Fatal(err)
	}
	if cardEvents != 1 || scheduleEvents != 1 || healthIssues != 1 {
		t.Fatalf("card %s projections card_events=%d schedule_events=%d health_issues=%d", cardID, cardEvents, scheduleEvents, healthIssues)
	}
}
