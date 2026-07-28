//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInterviewRepositoryAbandonsStaleCompletionAndRecoversSameAttempt(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newInterviewTestDatabase(t, ctx)
	defer cleanup()
	seedInterviewPersistence(t, ctx, pool)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}

	workspaceID := interviewIntegrationID(1)
	sessionID := interviewIntegrationID(60)
	claimID := interviewIntegrationID(2)
	reportArtifactID := interviewIntegrationID(40)
	pathArtifactID := interviewIntegrationID(42)
	attemptDigest := strings.Repeat("5", 64)
	requestHash := strings.Repeat("6", 64)
	commandKey := "interview-maintenance-complete"
	staleAt := time.Now().UTC().Add(-25 * time.Hour).Truncate(time.Second)
	configRaw, err := json.Marshal(domain.Config{
		SchemaVersion:   domain.SchemaVersion,
		Role:            "backend engineer",
		Scope:           domain.Scope{ClaimIDs: []foundation.ID{claimID}},
		Difficulty:      domain.DifficultyIntermediate,
		DurationMinutes: 30,
		QuestionCount:   1,
		MaxFollowUps:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_session(
		id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at,ended_at
	) VALUES($1,$2,NULL,'INTERVIEW','ACTIVE',$3,$4,repeat('7',64),$5,NULL)`,
		string(sessionID), string(workspaceID), configRaw, "maintenance-session-"+string(sessionID), staleAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.interview_session(
		session_id,workspace_id,domain_schema_version,version,follow_up_count,created_at,updated_at
	) VALUES($1,$2,'interview/v1',1,0,$3,$3)`, string(sessionID), string(workspaceID), staleAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.interview_completion_reservation(
		workspace_id,session_id,idempotency_key,request_hash,manual_end,snapshot_version,
		artifact_digest,status,created_at,prepared_at,updated_at
	) VALUES($1,$2,$3,$4,false,1,$5,'PENDING',$6,$6,$6)`,
		string(workspaceID), string(sessionID), commandKey, requestHash, attemptDigest, staleAt); err != nil {
		t.Fatal(err)
	}
	seedInterviewCompletionPlanReceipt(t, ctx, pool, workspaceID, sessionID, reportArtifactID, "INTERVIEW_DOC", attemptDigest, staleAt)
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_visibility_hold(
		workspace_id,artifact_id,owner_type,owner_id,owner_role,attempt_digest,created_at
	) VALUES($1,$2,'INTERVIEW_COMPLETE',$3,'REPORT',$4,$5)`,
		string(workspaceID), string(reportArtifactID), string(sessionID), attemptDigest, staleAt); err != nil {
		t.Fatal(err)
	}

	maintenance, err := repository.AbandonStaleCompletions(ctx, 10)
	if err != nil || maintenance.AbandonedReservations != 1 || maintenance.OrphanedHolds != 1 {
		t.Fatalf("maintenance=%+v err=%v", maintenance, err)
	}
	var status, disposition, orphanDigest string
	if err := pool.QueryRow(ctx, `SELECT reservation.status,hold.disposition,hold.attempt_digest
		FROM learning.interview_completion_reservation AS reservation
		JOIN learning.artifact_visibility_hold AS hold
		  ON hold.workspace_id=reservation.workspace_id AND hold.owner_id=reservation.session_id
		WHERE reservation.workspace_id=$1 AND reservation.session_id=$2`,
		string(workspaceID), string(sessionID)).Scan(&status, &disposition, &orphanDigest); err != nil {
		t.Fatal(err)
	}
	if status != "ABANDONED" || disposition != "ORPHANED" || orphanDigest != attemptDigest {
		t.Fatalf("status=%s disposition=%s digest=%s", status, disposition, orphanDigest)
	}

	seedInterviewCompletionPlanReceipt(t, ctx, pool, workspaceID, sessionID, pathArtifactID, "LEARNING_PATH", attemptDigest, staleAt)
	_, err = pool.Exec(ctx, `INSERT INTO learning.artifact_visibility_hold(
		workspace_id,artifact_id,owner_type,owner_id,owner_role,attempt_digest,created_at
	) VALUES($1,$2,'INTERVIEW_COMPLETE',$3,'PATH',NULL,$4)`,
		string(workspaceID), string(pathArtifactID), string(sessionID), staleAt)
	assertInterviewPostgresCode(t, err, "23514")

	begin, err := repository.BeginComplete(ctx, interviewapp.BeginCompleteRecord{
		WorkspaceID: workspaceID, SessionID: sessionID, IdempotencyKey: commandKey, RequestHash: requestHash,
	})
	if err != nil || begin.Terminal != nil || begin.Reservation.Status != interviewapp.CompletionReservationPending ||
		begin.Reservation.ArtifactDigest != "" || begin.Reservation.SnapshotVersion != 1 {
		t.Fatalf("reactivated begin=%+v err=%v", begin, err)
	}
	prepared, err := repository.PrepareComplete(ctx, interviewapp.PrepareCompleteRecord{
		WorkspaceID: workspaceID, SessionID: sessionID, IdempotencyKey: commandKey, RequestHash: requestHash,
		SnapshotVersion: 1, ArtifactDigest: attemptDigest,
	})
	if err != nil || prepared.Terminal != nil || prepared.Reservation.ArtifactDigest != attemptDigest {
		t.Fatalf("reactivated prepare=%+v err=%v", prepared, err)
	}
	if err := pool.QueryRow(ctx, `SELECT disposition,attempt_digest
		FROM learning.artifact_visibility_hold
		WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), string(reportArtifactID)).Scan(&disposition, &orphanDigest); err != nil {
		t.Fatal(err)
	}
	if disposition != "ACTIVE" || orphanDigest != attemptDigest {
		t.Fatalf("restored report hold disposition=%s digest=%s", disposition, orphanDigest)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_visibility_hold(
		workspace_id,artifact_id,owner_type,owner_id,owner_role,attempt_digest,created_at
	) VALUES($1,$2,'INTERVIEW_COMPLETE',$3,'PATH',NULL,$4)`,
		string(workspaceID), string(pathArtifactID), string(sessionID), staleAt); err != nil {
		t.Fatalf("new attempt PATH hold: %v", err)
	}
	if _, err := repository.BeginComplete(ctx, interviewapp.BeginCompleteRecord{
		WorkspaceID: workspaceID, SessionID: sessionID, IdempotencyKey: "another-completion-key", RequestHash: strings.Repeat("8", 64),
	}); err == nil {
		t.Fatal("different idempotency key acquired a pending Completion reservation")
	}
}

func seedInterviewCompletionPlanReceipt(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, sessionID, artifactID foundation.ID,
	artifactType, attemptDigest string,
	createdAt time.Time,
) {
	t.Helper()
	planKey := "iv1:" + string(sessionID) + ":" + artifactType + ":" + attemptDigest + ":p"
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_command(
		workspace_id,idempotency_key,request_hash,command_type,artifact_id,artifact_version,response,created_at
	) VALUES($1,$2,repeat('9',64),'PLAN',$3,1,'{}'::jsonb,$4)`,
		string(workspaceID), planKey, string(artifactID), createdAt); err != nil {
		t.Fatal(err)
	}
}
