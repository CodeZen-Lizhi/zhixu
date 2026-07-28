//go:build integration

package migration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM8InterviewMemoryIntegrityGuardedDownPreservesBusinessDataAndRecovers(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if _, err := provider.UpTo(ctx, 59); err != nil {
		t.Fatalf("00059 Up: %v", err)
	}
	fixture := seedM8InterviewMemoryGuardFixture(t, ctx, pool)
	assertM8InterviewMemoryGuardFixture(t, ctx, pool, fixture, true)

	_, err := provider.DownTo(ctx, 58)
	assertPostgresCode(t, err, "55000")
	assertMigrationVersion(t, ctx, pool, 59)
	assertM8InterviewMemoryGuardFixture(t, ctx, pool, fixture, true)

	cleanupM8InterviewMemoryGuardFixture(t, ctx, pool, fixture)
	assertM8InterviewMemoryGuardFixture(t, ctx, pool, fixture, false)
	if _, err := provider.DownTo(ctx, 58); err != nil {
		t.Fatalf("00059 empty Down after fixture cleanup: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 58)
	if _, err := provider.UpTo(ctx, 59); err != nil {
		t.Fatalf("00059 re-Up after fixture cleanup: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 59)
}

func TestM8ReviewSharedLearningPathGuardedDownPreservesBusinessDataAndRecovers(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if _, err := provider.UpTo(ctx, 60); err != nil {
		t.Fatalf("00060 Up: %v", err)
	}
	fixture := seedM8ReviewLearningPathGuardFixture(t, ctx, pool)
	assertM8ReviewLearningPathGuardFixture(t, ctx, pool, fixture, true)

	_, err := provider.DownTo(ctx, 59)
	assertPostgresCode(t, err, "55000")
	assertMigrationVersion(t, ctx, pool, 60)
	assertM8ReviewLearningPathGuardFixture(t, ctx, pool, fixture, true)

	cleanupM8ReviewLearningPathGuardFixture(t, ctx, pool, fixture)
	assertM8ReviewLearningPathGuardFixture(t, ctx, pool, fixture, false)
	if _, err := provider.DownTo(ctx, 59); err != nil {
		t.Fatalf("00060 empty Down after fixture cleanup: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 59)
	if _, err := provider.UpTo(ctx, 60); err != nil {
		t.Fatalf("00060 re-Up after fixture cleanup: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 60)
}

type m8InterviewMemoryGuardFixture struct {
	workspaceID    string
	memoryID       string
	auditID        int64
	serverEventSeq []int64
}

func seedM8InterviewMemoryGuardFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) m8InterviewMemoryGuardFixture {
	t.Helper()
	fixture := m8InterviewMemoryGuardFixture{
		workspaceID: "59000000-0000-4000-8000-000000000001",
		memoryID:    "59000000-0000-4000-8000-000000000002",
	}
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	ownerID := "00000000-0000-5000-8000-000000000001"

	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,'m8-memory-guard','/tmp/m8-memory-guard','/tmp/m8-memory-guard',$2,'test',1,$2,$2)`, fixture.workspaceID, now); err != nil {
		t.Fatalf("seed 00059 workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.memory(
		id,workspace_id,owner_principal_kind,owner_principal_id,memory_type,content,
		source_type,source_ref,status,version,created_at,updated_at
	) VALUES($1,$2,'USER',$3,'PREFERENCE','{"style":"concise"}'::jsonb,
		'USER','user:m8-memory-guard','CANDIDATE',1,$4,$4)`, fixture.memoryID, fixture.workspaceID, ownerID, now); err != nil {
		t.Fatalf("seed 00059 Memory: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO learning.memory_audit(
		workspace_id,memory_id,owner_principal_kind,owner_principal_id,
		actor_principal_kind,actor_principal_id,action,from_status,to_status,memory_version,occurred_at
	) VALUES($1,$2,'USER',$3,'USER',$3,'CANDIDATE_CREATED',NULL,'CANDIDATE',1,$4)
	RETURNING id`, fixture.workspaceID, fixture.memoryID, ownerID, now).Scan(&fixture.auditID); err != nil {
		t.Fatalf("seed 00059 Memory Audit: %v", err)
	}
	fixture.serverEventSeq = loadM8GuardServerEventSeqs(t, ctx, pool, fixture.workspaceID)
	if len(fixture.serverEventSeq) != 1 {
		t.Fatalf("00059 fixture server events=%v want one Memory projection", fixture.serverEventSeq)
	}
	return fixture
}

func assertM8InterviewMemoryGuardFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture m8InterviewMemoryGuardFixture,
	wantPresent bool,
) {
	t.Helper()
	var workspaceCount, memoryCount, auditCount, eventCount, otherGuardCount int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM core.workspace WHERE id=$1),
		(SELECT count(*) FROM learning.memory WHERE workspace_id=$1 AND id=$2),
		(SELECT count(*) FROM learning.memory_audit WHERE workspace_id=$1 AND id=$3 AND memory_id=$2),
		(SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND seq=ANY($4::bigint[])),
		(SELECT count(*) FROM learning.interview_session)
			+ (SELECT count(*) FROM learning.memory WHERE source_type='INTERVIEW')
			+ (SELECT count(*) FROM learning.interview_completion_reservation)`,
		fixture.workspaceID, fixture.memoryID, fixture.auditID, fixture.serverEventSeq,
	).Scan(&workspaceCount, &memoryCount, &auditCount, &eventCount, &otherGuardCount); err != nil {
		t.Fatalf("read 00059 guard fixture: %v", err)
	}
	if otherGuardCount != 0 {
		t.Fatalf("00059 fixture unexpectedly populated another guard branch: rows=%d", otherGuardCount)
	}
	want := 0
	wantEvents := 0
	if wantPresent {
		want = 1
		wantEvents = len(fixture.serverEventSeq)
	}
	if workspaceCount != want || memoryCount != want || auditCount != want || eventCount != wantEvents {
		t.Fatalf("00059 guard fixture workspace=%d Memory=%d Audit=%d events=%d want=%d/%d/%d/%d",
			workspaceCount, memoryCount, auditCount, eventCount, want, want, want, wantEvents)
	}
	if !wantPresent {
		return
	}
	var memoryStatus, auditAction, auditStatus string
	var memoryVersion, auditVersion int64
	if err := pool.QueryRow(ctx, `SELECT memory.status,memory.version,audit.action,audit.to_status,audit.memory_version
		FROM learning.memory AS memory
		JOIN learning.memory_audit AS audit
		  ON audit.workspace_id=memory.workspace_id AND audit.memory_id=memory.id
		WHERE memory.workspace_id=$1 AND memory.id=$2 AND audit.id=$3`,
		fixture.workspaceID, fixture.memoryID, fixture.auditID,
	).Scan(&memoryStatus, &memoryVersion, &auditAction, &auditStatus, &auditVersion); err != nil {
		t.Fatalf("read 00059 protected Memory fact: %v", err)
	}
	if memoryStatus != "CANDIDATE" || memoryVersion != 1 ||
		auditAction != "CANDIDATE_CREATED" || auditStatus != memoryStatus || auditVersion != memoryVersion {
		t.Fatalf("00059 protected Memory fact status=%s version=%d audit=%s/%s/%d",
			memoryStatus, memoryVersion, auditAction, auditStatus, auditVersion)
	}
}

func cleanupM8InterviewMemoryGuardFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture m8InterviewMemoryGuardFixture,
) {
	t.Helper()
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `ALTER TABLE learning.memory_audit
			DISABLE TRIGGER trg_learning_memory_audit_append_only`); err != nil {
			return fmt.Errorf("disable 00059 Memory Audit history trigger: %w", err)
		}
		if err := execM8GuardCleanup(ctx, tx, "00059 Memory Audit", 1,
			`DELETE FROM learning.memory_audit WHERE workspace_id=$1 AND id=$2 AND memory_id=$3`,
			fixture.workspaceID, fixture.auditID, fixture.memoryID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE learning.memory_audit
			ENABLE TRIGGER trg_learning_memory_audit_append_only`); err != nil {
			return fmt.Errorf("restore 00059 Memory Audit history trigger: %w", err)
		}
		if err := execM8GuardCleanup(ctx, tx, "00059 Memory", 1,
			`DELETE FROM learning.memory WHERE workspace_id=$1 AND id=$2`,
			fixture.workspaceID, fixture.memoryID); err != nil {
			return err
		}
		if err := execM8GuardCleanup(ctx, tx, "00059 SSE projections", int64(len(fixture.serverEventSeq)),
			`DELETE FROM ops.server_event WHERE workspace_id=$1 AND seq=ANY($2::bigint[])`,
			fixture.workspaceID, fixture.serverEventSeq); err != nil {
			return err
		}
		return execM8GuardCleanup(ctx, tx, "00059 Workspace", 1,
			`DELETE FROM core.workspace WHERE id=$1`, fixture.workspaceID)
	})
	if err != nil {
		t.Fatalf("cleanup 00059 guard fixture: %v", err)
	}
}

type m8ReviewLearningPathGuardFixture struct {
	workspaceID          string
	claimID              string
	deckID               string
	cardID               string
	sessionID            string
	answerID             string
	reservationKey       string
	sourceSnapshotDigest string
	knowledgeRevision    int64
	serverEventSeq       []int64
}

func seedM8ReviewLearningPathGuardFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) m8ReviewLearningPathGuardFixture {
	t.Helper()
	fixture := m8ReviewLearningPathGuardFixture{
		workspaceID:          "60000000-0000-4000-8000-000000000001",
		claimID:              "60000000-0000-4000-8000-000000000002",
		deckID:               "60000000-0000-4000-8000-000000000003",
		cardID:               "60000000-0000-4000-8000-000000000004",
		sessionID:            "60000000-0000-4000-8000-000000000005",
		answerID:             "60000000-0000-4000-8000-000000000006",
		reservationKey:       "m8-review-path-guard",
		sourceSnapshotDigest: strings.Repeat("6", 64),
	}
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)

	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,'m8-review-path-guard','/tmp/m8-review-path-guard','/tmp/m8-review-path-guard',$2,'test',1,$2,$2)`, fixture.workspaceID, now); err != nil {
		t.Fatalf("seed 00060 workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.claim(
		id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,
		applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at
	) VALUES($1,$2,'review guard claim','review guard claim','{}','knowledge-applicability/v1',
		repeat('1',64),'SUGGESTED',0.9,'{}',repeat('2',64),1,$3,$3)`, fixture.claimID, fixture.workspaceID, now); err != nil {
		t.Fatalf("seed 00060 Claim: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT knowledge_revision
		FROM core.workspace_read_model_revision WHERE workspace_id=$1`, fixture.workspaceID).
		Scan(&fixture.knowledgeRevision); err != nil {
		t.Fatalf("read 00060 Claim revision projection: %v", err)
	}
	if fixture.knowledgeRevision != 1 {
		t.Fatalf("00060 Claim revision projection=%d want=1", fixture.knowledgeRevision)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_deck(
		id,workspace_id,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at
	) VALUES($1,$2,'review guard deck','{}','ACTIVE',20,'fsrs/v1',1,$3,$3)`, fixture.deckID, fixture.workspaceID, now); err != nil {
		t.Fatalf("seed 00060 Review Deck: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_card(
		id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,
		status,fingerprint,model_version,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,'What is the guarded rollback invariant?','["preserve facts"]',
		'[{"fixture":"review-path-guard"}]','SHORT_ANSWER',0.5,'DRAFT',repeat('3',64),'manual',1,$5,$5)`,
		fixture.cardID, fixture.workspaceID, fixture.deckID, fixture.claimID, now); err != nil {
		t.Fatalf("seed 00060 Review Card: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_session(
		id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at,ended_at
	) VALUES($1,$2,$3,'REVIEW','ACTIVE','{}',$4,repeat('4',64),$5,NULL)`,
		fixture.sessionID, fixture.workspaceID, fixture.deckID, "m8-review-path-session", now); err != nil {
		t.Fatalf("seed 00060 Review Session: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_answer(
		id,workspace_id,session_id,card_id,question_ref,idempotency_key,user_answer,scorer_version,
		score,feedback,rating,schedule_snapshot,request_hash,created_at
	) VALUES($1,$2,$3,$4,'review:guard:1','m8-review-path-answer','The facts must survive.',
		'review-scorer/v1','{}','{}',3,'{}',repeat('5',64),$5)`,
		fixture.answerID, fixture.workspaceID, fixture.sessionID, fixture.cardID, now); err != nil {
		t.Fatalf("seed 00060 Review Answer: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.learning_path_creation_reservation(
		workspace_id,review_answer_id,idempotency_key,request_hash,source_snapshot,
		source_snapshot_digest,status,created_at,updated_at
	) VALUES($1,$2,$3,repeat('7',64),jsonb_build_object(
		'schema_version','review-gap/v1','answer_id',($2::uuid)::text
	),$4,'PENDING',$5,$5)`, fixture.workspaceID, fixture.answerID, fixture.reservationKey, fixture.sourceSnapshotDigest, now); err != nil {
		t.Fatalf("seed 00060 Path reservation: %v", err)
	}
	fixture.serverEventSeq = loadM8GuardServerEventSeqs(t, ctx, pool, fixture.workspaceID)
	if len(fixture.serverEventSeq) != 4 {
		t.Fatalf("00060 fixture server events=%v want Review Deck/Card/Session/Answer projections", fixture.serverEventSeq)
	}
	return fixture
}

func assertM8ReviewLearningPathGuardFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture m8ReviewLearningPathGuardFixture,
	wantPresent bool,
) {
	t.Helper()
	var workspaceCount, claimCount, revisionCount, deckCount, cardCount, sessionCount, answerCount, reservationCount, eventCount, otherGuardCount int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM core.workspace WHERE id=$1),
		(SELECT count(*) FROM core.claim WHERE workspace_id=$1 AND id=$2),
		(SELECT count(*) FROM core.workspace_read_model_revision
			WHERE workspace_id=$1 AND knowledge_revision=$9),
		(SELECT count(*) FROM learning.review_deck WHERE workspace_id=$1 AND id=$3),
		(SELECT count(*) FROM learning.review_card WHERE workspace_id=$1 AND id=$4),
		(SELECT count(*) FROM learning.review_session WHERE workspace_id=$1 AND id=$5),
		(SELECT count(*) FROM learning.review_answer WHERE workspace_id=$1 AND id=$6),
		(SELECT count(*) FROM learning.learning_path_creation_reservation
			WHERE workspace_id=$1 AND review_answer_id=$6 AND idempotency_key=$7),
		(SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND seq=ANY($8::bigint[])),
		(SELECT count(*) FROM learning.learning_path WHERE origin_type='REVIEW')
			+ (SELECT count(*) FROM learning.learning_path_command)
			+ (SELECT count(*) FROM learning.artifact_visibility_hold WHERE owner_type='LEARNING_PATH_CREATE')`,
		fixture.workspaceID, fixture.claimID, fixture.deckID, fixture.cardID, fixture.sessionID,
		fixture.answerID, fixture.reservationKey, fixture.serverEventSeq, fixture.knowledgeRevision,
	).Scan(&workspaceCount, &claimCount, &revisionCount, &deckCount, &cardCount, &sessionCount, &answerCount, &reservationCount, &eventCount, &otherGuardCount); err != nil {
		t.Fatalf("read 00060 guard fixture: %v", err)
	}
	if otherGuardCount != 0 {
		t.Fatalf("00060 fixture unexpectedly populated another guard branch: rows=%d", otherGuardCount)
	}
	want := 0
	wantEvents := 0
	if wantPresent {
		want = 1
		wantEvents = len(fixture.serverEventSeq)
	}
	if workspaceCount != want || claimCount != want || revisionCount != want || deckCount != want || cardCount != want ||
		sessionCount != want || answerCount != want || reservationCount != want || eventCount != wantEvents {
		t.Fatalf("00060 guard fixture workspace=%d Claim=%d revision=%d Deck=%d Card=%d Session=%d Answer=%d reservation=%d events=%d want=%d/%d",
			workspaceCount, claimCount, revisionCount, deckCount, cardCount, sessionCount, answerCount, reservationCount, eventCount, want, wantEvents)
	}
	if !wantPresent {
		return
	}
	var status, snapshotDigest string
	var attemptNo int64
	if err := pool.QueryRow(ctx, `SELECT status,attempt_no,source_snapshot_digest
		FROM learning.learning_path_creation_reservation
		WHERE workspace_id=$1 AND review_answer_id=$2 AND idempotency_key=$3`,
		fixture.workspaceID, fixture.answerID, fixture.reservationKey,
	).Scan(&status, &attemptNo, &snapshotDigest); err != nil {
		t.Fatalf("read 00060 protected Path reservation: %v", err)
	}
	if status != "PENDING" || attemptNo != 1 || snapshotDigest != fixture.sourceSnapshotDigest {
		t.Fatalf("00060 protected Path reservation status=%s attempt=%d digest=%s",
			status, attemptNo, snapshotDigest)
	}
}

func cleanupM8ReviewLearningPathGuardFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture m8ReviewLearningPathGuardFixture,
) {
	t.Helper()
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := execM8GuardCleanup(ctx, tx, "00060 Path reservation", 1,
			`DELETE FROM learning.learning_path_creation_reservation
			WHERE workspace_id=$1 AND review_answer_id=$2 AND idempotency_key=$3`,
			fixture.workspaceID, fixture.answerID, fixture.reservationKey); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE learning.review_answer
			DISABLE TRIGGER review_answer_reject_update_delete`); err != nil {
			return fmt.Errorf("disable 00060 Review Answer history trigger: %w", err)
		}
		if err := execM8GuardCleanup(ctx, tx, "00060 Review Answer", 1,
			`DELETE FROM learning.review_answer WHERE workspace_id=$1 AND id=$2`,
			fixture.workspaceID, fixture.answerID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE learning.review_answer
			ENABLE TRIGGER review_answer_reject_update_delete`); err != nil {
			return fmt.Errorf("restore 00060 Review Answer history trigger: %w", err)
		}
		if err := execM8GuardCleanup(ctx, tx, "00060 Review Session", 1,
			`DELETE FROM learning.review_session WHERE workspace_id=$1 AND id=$2`,
			fixture.workspaceID, fixture.sessionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE learning.review_card
			DISABLE TRIGGER review_card_project_server_event`); err != nil {
			return fmt.Errorf("disable 00060 Review Card SSE trigger: %w", err)
		}
		if err := execM8GuardCleanup(ctx, tx, "00060 Review Card", 1,
			`DELETE FROM learning.review_card WHERE workspace_id=$1 AND id=$2`,
			fixture.workspaceID, fixture.cardID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE learning.review_card
			ENABLE TRIGGER review_card_project_server_event`); err != nil {
			return fmt.Errorf("restore 00060 Review Card SSE trigger: %w", err)
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE learning.review_deck
			DISABLE TRIGGER review_deck_project_server_event`); err != nil {
			return fmt.Errorf("disable 00060 Review Deck SSE trigger: %w", err)
		}
		if err := execM8GuardCleanup(ctx, tx, "00060 Review Deck", 1,
			`DELETE FROM learning.review_deck WHERE workspace_id=$1 AND id=$2`,
			fixture.workspaceID, fixture.deckID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE learning.review_deck
			ENABLE TRIGGER review_deck_project_server_event`); err != nil {
			return fmt.Errorf("restore 00060 Review Deck SSE trigger: %w", err)
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE core.claim
			DISABLE TRIGGER knowledge_claim_reject_delete`); err != nil {
			return fmt.Errorf("disable 00060 Claim history trigger: %w", err)
		}
		if err := execM8GuardCleanup(ctx, tx, "00060 Claim", 1,
			`DELETE FROM core.claim WHERE workspace_id=$1 AND id=$2`,
			fixture.workspaceID, fixture.claimID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE core.claim
			ENABLE TRIGGER knowledge_claim_reject_delete`); err != nil {
			return fmt.Errorf("restore 00060 Claim history trigger: %w", err)
		}
		if err := execM8GuardCleanup(ctx, tx, "00060 Claim revision projection", 1,
			`DELETE FROM core.workspace_read_model_revision WHERE workspace_id=$1`,
			fixture.workspaceID); err != nil {
			return err
		}
		if err := execM8GuardCleanup(ctx, tx, "00060 SSE projections", int64(len(fixture.serverEventSeq)),
			`DELETE FROM ops.server_event WHERE workspace_id=$1 AND seq=ANY($2::bigint[])`,
			fixture.workspaceID, fixture.serverEventSeq); err != nil {
			return err
		}
		return execM8GuardCleanup(ctx, tx, "00060 Workspace", 1,
			`DELETE FROM core.workspace WHERE id=$1`, fixture.workspaceID)
	})
	if err != nil {
		t.Fatalf("cleanup 00060 guard fixture: %v", err)
	}
}

func loadM8GuardServerEventSeqs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID string) []int64 {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT seq FROM ops.server_event WHERE workspace_id=$1 ORDER BY seq`, workspaceID)
	if err != nil {
		t.Fatalf("read M8 guard fixture SSE projections: %v", err)
	}
	defer rows.Close()
	seqs := make([]int64, 0, 4)
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatalf("scan M8 guard fixture SSE projection: %v", err)
		}
		seqs = append(seqs, seq)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate M8 guard fixture SSE projections: %v", err)
	}
	return seqs
}

func execM8GuardCleanup(
	ctx context.Context,
	tx pgx.Tx,
	label string,
	wantRows int64,
	statement string,
	args ...any,
) error {
	tag, err := tx.Exec(ctx, statement, args...)
	if err != nil {
		return fmt.Errorf("delete %s: %w", label, err)
	}
	if tag.RowsAffected() != wantRows {
		return fmt.Errorf("delete %s rows=%d want=%d", label, tag.RowsAffected(), wantRows)
	}
	return nil
}
