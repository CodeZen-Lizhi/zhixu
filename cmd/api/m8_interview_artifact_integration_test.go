//go:build integration

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	artifactpostgres "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/postgres"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	interviewapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInterviewProductionServiceCreatesVerifiedDraftsPostgreSQL(t *testing.T) {
	pool := newArtifactHTTPTestDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	workspace := seedArtifactHTTPWorkspace(t, ctx, pool, "interview-production-service")
	evidence := seedArtifactHTTPEvidenceIndex(t, ctx, pool, workspace, []artifactHTTPEvidenceSpec{{
		label: "interview-evidence", content: []byte("Confirmed local knowledge supports the Interview completion artifacts."),
	}})[0]
	claimID := seedArtifactHTTPConfirmedClaim(t, ctx, pool, evidence)

	workspaces, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	files := filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}}
	service, err := newInterviewService(pool, workspaces, files)
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(ctx, interviewapplication.StartCommand{
		WorkspaceID: workspace.id,
		Config: interviewdomain.Config{
			SchemaVersion:   interviewdomain.SchemaVersion,
			Role:            "backend engineer",
			Scope:           interviewdomain.Scope{ClaimIDs: []foundation.ID{claimID}},
			Difficulty:      interviewdomain.DifficultyIntermediate,
			DurationMinutes: 30,
			QuestionCount:   1,
			MaxFollowUps:    0,
		},
		IdempotencyKey: "interview-production-start",
	})
	if err != nil || len(started.Questions) != 1 {
		t.Fatalf("start=%+v err=%v", started, err)
	}
	rawAnswerCanary := "raw-answer-production-canary-9482"
	if _, err := service.SubmitTurn(ctx, interviewapplication.SubmitTurnCommand{
		WorkspaceID: workspace.id, SessionID: started.Session.ID, QuestionID: started.Questions[0].ID,
		UserAnswer:     rawAnswerCanary,
		IdempotencyKey: "interview-production-submit",
	}); err != nil {
		t.Fatal(err)
	}
	completeCommand := interviewapplication.CompleteCommand{
		WorkspaceID: workspace.id, SessionID: started.Session.ID, IdempotencyKey: "interview-production-complete",
	}
	repository, err := artifactpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	installInterviewCompleteReceiptFailure(t, ctx, pool)
	if _, err := service.Complete(ctx, completeCommand); err == nil {
		t.Fatal("injected Interview completion failure must be observable")
	}
	heldArtifacts := interviewHeldArtifacts(t, ctx, pool, workspace.id, started.Session.ID)
	if len(heldArtifacts) != 2 || heldArtifacts["REPORT"] == "" || heldArtifacts["PATH"] == "" {
		t.Fatalf("failed completion visibility holds=%+v", heldArtifacts)
	}
	for role, artifactID := range heldArtifacts {
		if _, err := repository.Get(ctx, workspace.id, artifactID); err == nil {
			t.Fatalf("held %s Artifact %s became publicly readable", role, artifactID)
		}
	}
	heldPage, err := repository.List(ctx, artifactapplication.ListQuery{WorkspaceID: workspace.id, Limit: 10})
	if err != nil || len(heldPage.Items) != 0 {
		t.Fatalf("held Artifact list=%+v err=%v", heldPage, err)
	}
	assertInterviewCompletionRolledBack(t, ctx, pool, workspace.id, started.Session.ID)
	artifactsBeforeRetry := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact WHERE workspace_id=$1`, string(workspace.id))
	revisionsBeforeRetry := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact_revision revision JOIN learning.artifact artifact ON artifact.id=revision.artifact_id WHERE artifact.workspace_id=$1`, string(workspace.id))
	artifactReceiptsBeforeRetry := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact_command WHERE workspace_id=$1`, string(workspace.id))
	if artifactsBeforeRetry != 2 || revisionsBeforeRetry != 8 || artifactReceiptsBeforeRetry != 8 {
		t.Fatalf("failed completion physical Artifact state=%d/%d/%d", artifactsBeforeRetry, revisionsBeforeRetry, artifactReceiptsBeforeRetry)
	}
	removeInterviewCompleteReceiptFailure(t, ctx, pool)

	completed, err := service.Complete(ctx, completeCommand)
	if err != nil || completed.Replayed {
		t.Fatalf("retry completion=%+v err=%v", completed, err)
	}
	if completed.Report.Artifact.ArtifactID != heldArtifacts["REPORT"] || completed.Path.Artifact.ArtifactID != heldArtifacts["PATH"] {
		t.Fatalf("retry replaced held Artifacts: held=%+v completed=%+v", heldArtifacts, completed)
	}
	if count := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact_visibility_hold WHERE workspace_id=$1 AND owner_id=$2`, string(workspace.id), string(started.Session.ID)); count != 0 {
		t.Fatalf("completed visibility hold count=%d want=0", count)
	}
	visiblePage, err := repository.List(ctx, artifactapplication.ListQuery{WorkspaceID: workspace.id, Limit: 10})
	if err != nil || len(visiblePage.Items) != 2 {
		t.Fatalf("completed Artifact list=%+v err=%v", visiblePage, err)
	}
	if artifacts := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact WHERE workspace_id=$1`, string(workspace.id)); artifacts != artifactsBeforeRetry {
		t.Fatalf("retry Artifact count=%d want=%d", artifacts, artifactsBeforeRetry)
	}
	if revisions := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact_revision revision JOIN learning.artifact artifact ON artifact.id=revision.artifact_id WHERE artifact.workspace_id=$1`, string(workspace.id)); revisions != revisionsBeforeRetry {
		t.Fatalf("retry revision count=%d want=%d", revisions, revisionsBeforeRetry)
	}
	if receipts := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact_command WHERE workspace_id=$1`, string(workspace.id)); receipts != artifactReceiptsBeforeRetry {
		t.Fatalf("retry Artifact receipt count=%d want=%d", receipts, artifactReceiptsBeforeRetry)
	}

	bindings := []interviewdomain.ArtifactBinding{completed.Report.Artifact, completed.Path.Artifact}
	kinds := []string{interviewapplication.ArtifactKindInterviewDocument, interviewapplication.ArtifactKindLearningPath}
	sectionKeys := []string{"report", "learning-path"}
	pathArtifactContent := ""
	for index, binding := range bindings {
		state, err := repository.Get(ctx, workspace.id, binding.ArtifactID)
		if err != nil {
			t.Fatalf("read %s draft: %v", kinds[index], err)
		}
		metadata := state.Revision.Metadata
		if binding.Kind != kinds[index] || binding.RevisionID != state.Revision.ID || binding.ArtifactVersion != 4 ||
			state.Artifact.Type != kinds[index] || state.Artifact.Status != artifactdomain.StatusDraft || state.Artifact.Version != 4 ||
			state.Artifact.CurrentRevisionID != state.Revision.ID || state.Revision.RevisionNo != 4 ||
			state.Revision.CreatedBy != artifactdomain.CreatorAgent || metadata == nil ||
			metadata.PromptVersion != "interview-completion-renderer/v1" || metadata.ModelVersion != "interview-deterministic-renderer/v1" ||
			metadata.WorkflowDefinitionVersion != "interview-completion/v1" || metadata.SchemaVersion != "interview-completion-artifact/v1" ||
			len(state.Revision.Sections) != 1 {
			t.Fatalf("invalid %s draft state: binding=%+v state=%+v", kinds[index], binding, state)
		}
		section := state.Revision.Sections[0]
		if section.Key != sectionKeys[index] || strings.TrimSpace(section.Content) == "" || strings.Contains(section.Content, rawAnswerCanary) ||
			section.Coverage.Status != artifactdomain.CoverageCovered || len(section.Coverage.Gaps) != 0 || len(section.Citations) != 1 {
			t.Fatalf("invalid %s draft section: %+v", kinds[index], section)
		}
		if binding.Kind == interviewapplication.ArtifactKindLearningPath {
			if strings.Contains(section.Content, "- Status:") {
				t.Fatalf("Learning Path Artifact contains mutable runtime progress: %s", section.Content)
			}
			pathArtifactContent = section.Content
		}
		citation := section.Citations[0]
		if !citation.Verified || citation.SourceVersionID != evidence.sourceVersionID || citation.SourceSpanID != evidence.sourceSpanID ||
			citation.VerifiedContentHash != evidence.contentHash || citation.Excerpt != string(evidence.content) {
			t.Fatalf("invalid %s verified citation: %+v", kinds[index], citation)
		}
	}
	if len(completed.Steps) == 0 || pathArtifactContent == "" {
		t.Fatalf("completion lacks a persisted Learning Path definition: %+v", completed)
	}
	paused, err := service.UpdatePathStatus(ctx, interviewapplication.UpdatePathStatusCommand{
		WorkspaceID: workspace.id, PathID: completed.Path.ID, ExpectedVersion: completed.Path.Version,
		Status: interviewdomain.PathStatusPaused, IdempotencyKey: "interview-production-path-pause",
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := service.UpdatePathStatus(ctx, interviewapplication.UpdatePathStatusCommand{
		WorkspaceID: workspace.id, PathID: completed.Path.ID, ExpectedVersion: paused.Path.Version,
		Status: interviewdomain.PathStatusActive, IdempotencyKey: "interview-production-path-resume",
	})
	if err != nil {
		t.Fatal(err)
	}
	progressed, err := service.UpdatePathStep(ctx, interviewapplication.UpdatePathStepCommand{
		WorkspaceID: workspace.id, PathID: completed.Path.ID, StepID: completed.Steps[0].ID, ExpectedVersion: resumed.Path.Version,
		Status: interviewdomain.StepStatusInProgress, IdempotencyKey: "interview-production-path-progress",
	})
	if err != nil || progressed.Path.Version != resumed.Path.Version+1 || progressed.Step.Status != interviewdomain.StepStatusInProgress {
		t.Fatalf("path progress=%+v err=%v", progressed, err)
	}
	pathArtifact, err := repository.Get(ctx, workspace.id, completed.Path.Artifact.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if pathArtifact.Artifact.Version != completed.Path.Artifact.ArtifactVersion || pathArtifact.Revision.ID != completed.Path.Artifact.RevisionID ||
		len(pathArtifact.Revision.Sections) != 1 || pathArtifact.Revision.Sections[0].Content != pathArtifactContent ||
		strings.Contains(pathArtifact.Revision.Sections[0].Content, "- Status:") {
		t.Fatalf("runtime progress rewrote the immutable Learning Path definition: %+v", pathArtifact)
	}

	replayed, err := service.Complete(ctx, completeCommand)
	if err != nil || !replayed.Replayed || replayed.Report.Artifact != completed.Report.Artifact || replayed.Path.Artifact != completed.Path.Artifact {
		t.Fatalf("complete replay=%+v original=%+v err=%v", replayed, completed, err)
	}
	assertInterviewArtifactCounts(t, ctx, pool, workspace.id)
}

func installInterviewCompleteReceiptFailure(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION learning.fail_interview_complete_receipt_for_visibility_test()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			IF NEW.command_type = 'COMPLETE' THEN
				RAISE EXCEPTION 'injected Interview completion receipt failure'
					USING ERRCODE = '23514';
			END IF;
			RETURN NEW;
		END;
		$$`); err != nil {
		t.Fatalf("install Interview completion failure function: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TRIGGER trg_fail_interview_complete_receipt_for_visibility_test
		BEFORE INSERT ON learning.interview_command
		FOR EACH ROW
		EXECUTE FUNCTION learning.fail_interview_complete_receipt_for_visibility_test()`); err != nil {
		t.Fatalf("install Interview completion failure trigger: %v", err)
	}
}

func removeInterviewCompleteReceiptFailure(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `DROP TRIGGER trg_fail_interview_complete_receipt_for_visibility_test ON learning.interview_command`); err != nil {
		t.Fatalf("remove Interview completion failure trigger: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION learning.fail_interview_complete_receipt_for_visibility_test()`); err != nil {
		t.Fatalf("remove Interview completion failure function: %v", err)
	}
}

func interviewHeldArtifacts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, sessionID foundation.ID,
) map[string]foundation.ID {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT owner_role,artifact_id
		FROM learning.artifact_visibility_hold
		WHERE workspace_id=$1 AND owner_type='INTERVIEW_COMPLETE' AND owner_id=$2
		ORDER BY owner_role`, string(workspaceID), string(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	held := make(map[string]foundation.ID, 2)
	for rows.Next() {
		var role, rawArtifactID string
		if err := rows.Scan(&role, &rawArtifactID); err != nil {
			t.Fatal(err)
		}
		artifactID, err := foundation.ParseID(rawArtifactID)
		if err != nil {
			t.Fatal(err)
		}
		held[role] = artifactID
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return held
}

func assertInterviewCompletionRolledBack(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, sessionID foundation.ID,
) {
	t.Helper()
	if count := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact_visibility_hold WHERE workspace_id=$1 AND owner_id=$2`, string(workspaceID), string(sessionID)); count != 2 {
		t.Fatalf("failed completion visibility hold count=%d want=2", count)
	}
	if count := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.interview_report WHERE workspace_id=$1 AND session_id=$2`, string(workspaceID), string(sessionID)); count != 0 {
		t.Fatalf("failed completion report count=%d want=0", count)
	}
	if count := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.interview_learning_path WHERE workspace_id=$1 AND session_id=$2`, string(workspaceID), string(sessionID)); count != 0 {
		t.Fatalf("failed completion path count=%d want=0", count)
	}
	if count := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.interview_command WHERE workspace_id=$1 AND session_id=$2 AND command_type='COMPLETE'`, string(workspaceID), string(sessionID)); count != 0 {
		t.Fatalf("failed completion receipt count=%d want=0", count)
	}
	var shellStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM learning.review_session WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(sessionID)).Scan(&shellStatus); err != nil {
		t.Fatal(err)
	}
	if shellStatus != "ACTIVE" {
		t.Fatalf("failed completion review shell status=%s want=ACTIVE", shellStatus)
	}
	var sessionVersion int64
	if err := pool.QueryRow(ctx, `SELECT version FROM learning.interview_session WHERE workspace_id=$1 AND session_id=$2`, string(workspaceID), string(sessionID)).Scan(&sessionVersion); err != nil {
		t.Fatal(err)
	}
	if sessionVersion != 2 {
		t.Fatalf("failed completion Interview version=%d want=2", sessionVersion)
	}
}

func assertInterviewArtifactCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID) {
	t.Helper()
	if count := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact WHERE workspace_id=$1`, string(workspaceID)); count != 2 {
		t.Fatalf("artifact count=%d", count)
	}
	if count := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact_revision revision JOIN learning.artifact artifact ON artifact.id=revision.artifact_id WHERE artifact.workspace_id=$1`, string(workspaceID)); count != 8 {
		t.Fatalf("revision count=%d", count)
	}
	if count := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact_command WHERE workspace_id=$1`, string(workspaceID)); count != 8 {
		t.Fatalf("Artifact command receipt count=%d", count)
	}
	if count := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.interview_command WHERE workspace_id=$1`, string(workspaceID)); count != 6 {
		t.Fatalf("Interview command receipt count=%d", count)
	}
}
