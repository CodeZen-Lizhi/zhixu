//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryPostgreSQLVisibilityHoldHidesPublicReadsAndKeepsCommandsRecoverable(t *testing.T) {
	ctx := context.Background()
	repository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceID := artifactIntegrationID(600)
	sessionID := artifactIntegrationID(601)
	attemptDigest := strings.Repeat("b", 64)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-visibility-hold")
	seedArtifactVisibilityInterviewSession(t, ctx, pool, workspaceID, sessionID, attemptDigest)
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	service := artifactCommandServiceForConcurrency(t, repository, nil, nil, now)

	heldCommand := artifactapp.PlanCommand{
		WorkspaceID: workspaceID, Type: "INTERVIEW_DOC", Title: "Held interview report",
		ScopeDefinition: `{"schema_version":"interview-report/v1"}`,
		IdempotencyKey:  "iv1:" + string(sessionID) + ":INTERVIEW_DOC:" + attemptDigest + ":p",
		VisibilityHold: &artifactapp.VisibilityHold{
			OwnerType:     artifactapp.VisibilityHoldOwnerInterviewComplete,
			OwnerID:       sessionID,
			OwnerRole:     artifactapp.VisibilityHoldRoleReport,
			AttemptDigest: attemptDigest,
		},
	}
	held, err := service.Plan(ctx, heldCommand)
	if err != nil {
		t.Fatalf("plan held Artifact: %v", err)
	}
	withoutHold := heldCommand
	withoutHold.VisibilityHold = nil
	if _, err := service.Plan(ctx, withoutHold); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeIdempotencyConflict) {
		t.Fatalf("same plan key without its hold error=%v", err)
	}
	if _, err := repository.Get(ctx, workspaceID, held.State.Artifact.ID); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeNotFound) {
		t.Fatalf("public Get exposed held Artifact: %v", err)
	}
	raw, err := repository.GetCommandState(ctx, workspaceID, held.State.Artifact.ID)
	if err != nil || raw.Artifact.ID != held.State.Artifact.ID || raw.Artifact.Version != 1 {
		t.Fatalf("command state=%+v err=%v", raw, err)
	}

	ordinary, err := service.Plan(ctx, artifactapp.PlanCommand{
		WorkspaceID: workspaceID, Type: "CUSTOM", Title: "Visible Artifact",
		ScopeDefinition: "ordinary artifact", IdempotencyKey: "ordinary-visible-artifact",
	})
	if err != nil {
		t.Fatalf("plan ordinary Artifact: %v", err)
	}
	if visible, err := repository.Get(ctx, workspaceID, ordinary.State.Artifact.ID); err != nil || visible.Artifact.ID != ordinary.State.Artifact.ID {
		t.Fatalf("ordinary Get=%+v err=%v", visible, err)
	}
	page, err := repository.List(ctx, artifactapp.ListQuery{WorkspaceID: workspaceID, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Artifact.ID != ordinary.State.Artifact.ID {
		t.Fatalf("visible page=%+v err=%v", page, err)
	}

	outlined, err := service.SubmitOutline(ctx, artifactapp.SubmitOutlinePersistentCommand{
		WorkspaceID: workspaceID, ArtifactID: held.State.Artifact.ID, ExpectedVersion: 1,
		Outline: []artifactdomain.OutlineSection{{Key: "report", Title: "Report"}}, IdempotencyKey: "held-interview-outline",
	})
	if err != nil || outlined.State.Artifact.Version != 2 {
		t.Fatalf("continue held Artifact=%+v err=%v", outlined, err)
	}
	replayed, err := service.Plan(ctx, heldCommand)
	if err != nil || !replayed.Replayed || replayed.State.Artifact.ID != held.State.Artifact.ID || replayed.State.Artifact.Version != 1 || replayed.RequestHash != held.RequestHash {
		t.Fatalf("held plan replay=%+v err=%v", replayed, err)
	}
	if _, err := repository.Get(ctx, workspaceID, held.State.Artifact.ID); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeNotFound) {
		t.Fatalf("transition exposed held Artifact: %v", err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM learning.artifact_visibility_hold WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), string(held.State.Artifact.ID)); err != nil {
		t.Fatal(err)
	}
	visible, err := repository.Get(ctx, workspaceID, held.State.Artifact.ID)
	if err != nil || visible.Artifact.Version != 2 || visible.Revision.ID != outlined.State.Revision.ID {
		t.Fatalf("released Artifact=%+v err=%v", visible, err)
	}

	failed := artifactIntegrationPlan(t, workspaceID, 610, 611, now.Add(time.Minute))
	failed.Artifact.Type = "LEARNING_PATH"
	missingOwnerID := artifactIntegrationID(699)
	missingOwnerKey := "iv1:" + string(missingOwnerID) + ":LEARNING_PATH:" + attemptDigest + ":p"
	if _, err := repository.Create(ctx, artifactapp.CreateRecord{
		Binding: artifactIntegrationBinding(workspaceID, failed.Artifact.ID, missingOwnerKey, 'f', artifactapp.CommandPlan, 0),
		State:   failed,
		VisibilityHold: &artifactapp.VisibilityHold{
			OwnerType:     artifactapp.VisibilityHoldOwnerInterviewComplete,
			OwnerID:       missingOwnerID,
			OwnerRole:     artifactapp.VisibilityHoldRolePath,
			AttemptDigest: attemptDigest,
		},
	}); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeResultInconsistent) {
		t.Fatalf("missing hold owner error=%v", err)
	}
	var artifacts, revisions, receipts, holds int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM learning.artifact WHERE workspace_id=$1 AND id=$2),
			(SELECT count(*) FROM learning.artifact_revision WHERE workspace_id=$1 AND artifact_id=$2),
			(SELECT count(*) FROM learning.artifact_command WHERE workspace_id=$1 AND artifact_id=$2),
			(SELECT count(*) FROM learning.artifact_visibility_hold WHERE workspace_id=$1 AND artifact_id=$2)`,
		string(workspaceID), string(failed.Artifact.ID)).Scan(&artifacts, &revisions, &receipts, &holds); err != nil {
		t.Fatal(err)
	}
	if artifacts != 0 || revisions != 0 || receipts != 0 || holds != 0 {
		t.Fatalf("failed held create left artifacts=%d revisions=%d receipts=%d holds=%d", artifacts, revisions, receipts, holds)
	}
}

func seedArtifactVisibilityInterviewSession(t *testing.T, ctx context.Context, db *pgxpool.Pool, workspaceID, sessionID foundation.ID, attemptDigest string) {
	t.Helper()
	now := time.Date(2026, 7, 28, 9, 55, 0, 0, time.UTC)
	if _, err := db.Exec(ctx, `
		INSERT INTO learning.review_session(
			id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at,ended_at
		) VALUES($1,$2,NULL,'INTERVIEW','ACTIVE','{"schema_version":"interview/v1"}'::jsonb,$3,repeat('a',64),$4,NULL)`,
		string(sessionID), string(workspaceID), "visibility-session-"+string(sessionID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO learning.interview_session(
			session_id,workspace_id,domain_schema_version,version,follow_up_count,created_at,updated_at
		) VALUES($1,$2,'interview/v1',1,0,$3,$3)`, string(sessionID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO learning.interview_completion_reservation(
			workspace_id,session_id,idempotency_key,request_hash,manual_end,snapshot_version,
			artifact_digest,status,created_at,prepared_at,updated_at
		) VALUES($1,$2,$3,repeat('c',64),false,1,$4,'PENDING',$5,$5,$5)`,
		string(workspaceID), string(sessionID), "visibility-complete-"+string(sessionID), attemptDigest, now); err != nil {
		t.Fatal(err)
	}
}
