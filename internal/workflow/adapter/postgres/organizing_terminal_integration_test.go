//go:build integration

package workflowpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingpostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/postgres"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOrganizingTerminalHookBindsResultAfterSucceededStateInSameTransaction(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()

	workspaceID := foundation.ID("a8000000-0000-4000-8000-000000000001")
	snapshotID := foundation.ID("a8000000-0000-4000-8000-000000000002")
	draftID := foundation.ID("a8000000-0000-4000-8000-000000000003")
	outboxID := foundation.ID("a8000000-0000-4000-8000-000000000004")
	bindingID := foundation.ID("a8000000-0000-4000-8000-000000000005")
	artifactID := foundation.ID("a8000000-0000-4000-8000-000000000006")
	artifactRevisionID := foundation.ID("a8000000-0000-4000-8000-000000000007")
	resultID := foundation.ID("a8000000-0000-4000-8000-000000000008")
	resultHash := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'organizing-terminal',$2,$2,CURRENT_TIMESTAMP,'inactive',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
		string(workspaceID), "/tmp/organizing-terminal"); err != nil {
		t.Fatal(err)
	}
	organizingRepository, err := organizingpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := organizingRepository.EnsureBuiltIns(ctx, now); err != nil {
		t.Fatal(err)
	}
	seedOrganizingTerminalSnapshot(t, ctx, pool, workspaceID, draftID, snapshotID, outboxID, now)
	lease, found, err := organizingRepository.ClaimStart(ctx, "organizing-terminal-test", time.Minute)
	if err != nil || !found || lease.ID != outboxID {
		t.Fatalf("claim=%+v found=%v err=%v", lease, found, err)
	}

	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntimeRepositoryWithHooks(pool, inserter, RuntimeRepositoryHooks{Terminal: organizingworkflow.NewTerminalHook()})
	if err != nil {
		t.Fatal(err)
	}
	request := organizingTerminalStartRequest(t, workspaceID, snapshotID)
	started, err := runtime.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := organizingRepository.CompleteStart(ctx, organizingapp.CompleteStartRecord{
		Lease: lease, BindingID: bindingID, WorkflowRunID: started.Run.ID,
		DefinitionKey:     organizingworkflow.InterviewReviewDefinitionKey,
		DefinitionVersion: organizingworkflow.DefinitionVersion, StartedAt: now.Add(time.Second),
	}); err != nil || replayed {
		t.Fatalf("complete start replayed=%v err=%v", replayed, err)
	}
	seedOrganizingTerminalArtifact(t, ctx, pool, workspaceID, artifactID, artifactRevisionID, resultHash, now)

	claimed, err := runtime.Claim(ctx, application.ClaimCommand{
		NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "organizing-terminal-delivery",
		RiverJobID: started.Job.JobID, LeaseOwner: "organizing-terminal-worker", LeaseDuration: time.Minute,
	})
	if err != nil || claimed.Disposition != application.ClaimDispositionClaimed {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	prematureID := foundation.ID("a8000000-0000-4000-8000-000000000009")
	_, err = pool.Exec(ctx, `INSERT INTO organizing.run_result(
		id,workspace_id,run_binding_id,snapshot_id,workflow_run_id,node_run_id,result_kind,result_ref,result_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,'ARTIFACT',$7,$8,$9)`, string(prematureID), string(workspaceID),
		string(bindingID), string(snapshotID), string(started.Run.ID), string(started.FirstNode.ID),
		string(artifactID), resultHash, now.Add(2*time.Second))
	assertOrganizingTerminalPGCode(t, err, "23514")

	binding := application.DeliveryBinding{
		NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "organizing-terminal-delivery",
		Fence: domain.LeaseFence{Owner: "organizing-terminal-worker", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version},
	}
	badOutput := organizingTerminalReceipt(t, resultID, bindingID, snapshotID, artifactID,
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	if _, err := runtime.TransitionDelivery(ctx, application.DeliveryTransition{
		Binding: binding, Result: domain.AttemptResult{Output: badOutput, OutputSchemaVersion: 1},
	}); err == nil {
		t.Fatal("terminal result with an unowned hash unexpectedly committed")
	}
	var runStatus domain.RunStatus
	var nodeStatus domain.NodeStatus
	var resultCount int
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow.run WHERE id=$1`, string(started.Run.ID)).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow.node_run WHERE id=$1`, string(started.FirstNode.ID)).Scan(&nodeStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organizing.run_result WHERE workflow_run_id=$1`, string(started.Run.ID)).Scan(&resultCount); err != nil {
		t.Fatal(err)
	}
	if runStatus != domain.RunStatusRunning || nodeStatus != domain.NodeStatusRunning || resultCount != 0 {
		t.Fatalf("rolled back run=%s node=%s result_count=%d", runStatus, nodeStatus, resultCount)
	}

	completed, err := runtime.TransitionDelivery(ctx, application.DeliveryTransition{
		Binding: binding,
		Result: domain.AttemptResult{
			Output:              organizingTerminalReceipt(t, resultID, bindingID, snapshotID, artifactID, resultHash),
			OutputSchemaVersion: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var stored organizingdomain.RunResult
	if err := pool.QueryRow(ctx, `SELECT id::text,workspace_id::text,run_binding_id::text,snapshot_id::text,
		workflow_run_id::text,node_run_id::text,result_kind,result_ref::text,result_hash,created_at
		FROM organizing.run_result WHERE workflow_run_id=$1`, string(started.Run.ID)).
		Scan(&stored.ID, &stored.WorkspaceID, &stored.RunBindingID, &stored.SnapshotID, &stored.WorkflowRunID,
			&stored.NodeRunID, &stored.Kind, &stored.ResultRef, &stored.ResultHash, &stored.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if completed.Run.Status != domain.RunStatusSucceeded || completed.Node.Status != domain.NodeStatusSucceeded ||
		stored.ID != resultID || stored.WorkspaceID != workspaceID || stored.RunBindingID != bindingID ||
		stored.SnapshotID != snapshotID || stored.WorkflowRunID != started.Run.ID || stored.NodeRunID != started.FirstNode.ID ||
		stored.Kind != organizingdomain.ResultArtifact || stored.ResultRef != artifactID || stored.ResultHash != resultHash ||
		completed.Attempt.EndedAt == nil || !stored.CreatedAt.Equal(completed.Attempt.EndedAt.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("completed=%+v stored=%+v", completed, stored)
	}
}

func organizingTerminalStartRequest(t *testing.T, workspaceID, snapshotID foundation.ID) application.RuntimeStartRequest {
	t.Helper()
	retry := domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Nanosecond, MaxDelay: time.Second}
	request := runtimeStateStartFixture(workspaceID, organizingworkflow.InterviewReviewDefinitionKey, retry)
	graph := domain.CanonicalGraph{Nodes: []domain.NodeDefinition{{
		Key: organizingworkflow.InterviewReviewNodeKind, Kind: organizingworkflow.InterviewReviewNodeKind,
		InputSchemaVersion: organizingworkflow.InputSchemaVersion, OutputSchemaVersion: organizingworkflow.OutputSchemaVersion,
		RetryPolicy: retry,
	}}}
	encodedGraph, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	graphHash, err := application.ComputeCanonicalGraphHash(graph)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(organizingworkflow.StartInput{SnapshotID: snapshotID})
	if err != nil {
		t.Fatal(err)
	}
	request.Definition.Key = organizingworkflow.InterviewReviewDefinitionKey
	request.Definition.Version = organizingworkflow.DefinitionVersion
	request.Definition.Graph = encodedGraph
	request.DefinitionGraphHash = graphHash
	request.DefinitionInputSchemaVersion = organizingworkflow.InputSchemaVersion
	request.Run.Input = input
	request.Run.IdempotencyKey = organizingapp.StartIdempotencyKey(snapshotID)
	request.FirstNode.NodeKey = organizingworkflow.InterviewReviewNodeKind
	request.FirstNode.NodeType = organizingworkflow.InterviewReviewNodeKind
	request.FirstNode.Input = input
	request.FirstNode.InputSchemaVersion = organizingworkflow.InputSchemaVersion
	request.FirstNode.OutputSchemaVersion = organizingworkflow.OutputSchemaVersion
	request.FirstNode.IdempotencyKey = "organizing-terminal-node"
	return request
}

func seedOrganizingTerminalSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, draftID, snapshotID, outboxID foundation.ID, now time.Time) {
	t.Helper()
	var templateID, revisionID foundation.ID
	var templateHash string
	if err := pool.QueryRow(ctx, `SELECT template_id::text,id::text,declaration_hash FROM organizing.template_revision
		WHERE owner='BUILT_IN' AND kind='INTERVIEW_REVIEW' AND revision_no=1`).
		Scan(&templateID, &revisionID, &templateHash); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.draft(
		id,workspace_id,intent,status,template_revision_id,confirmed_snapshot_id,version,created_at,updated_at
	) VALUES($1,$2,'integration terminal result','EDITING',$3,NULL,1,$4,$4)`,
		string(draftID), string(workspaceID), string(revisionID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.draft_version(
		workspace_id,draft_id,version,intent,status,template_revision_id,confirmed_snapshot_id,draft_created_at,updated_at
	) VALUES($1,$2,1,'integration terminal result','EDITING',$3,NULL,$4,$4)`,
		string(workspaceID), string(draftID), string(revisionID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.workflow_input_snapshot(
		id,workspace_id,draft_id,draft_version,template_id,template_revision_id,template_hash,intent,snapshot_hash,created_at
	) VALUES($1,$2,$3,1,$4,$5,$6,'integration terminal result',$7,$8)`,
		string(snapshotID), string(workspaceID), string(draftID), string(templateID), string(revisionID), templateHash,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.draft_version(
		workspace_id,draft_id,version,intent,status,template_revision_id,confirmed_snapshot_id,draft_created_at,updated_at
	) VALUES($1,$2,2,'integration terminal result','CONFIRMED',$3,$4,$5,$5)`,
		string(workspaceID), string(draftID), string(revisionID), string(snapshotID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE organizing.draft
		SET status='CONFIRMED',confirmed_snapshot_id=$1,version=2,updated_at=$2
		WHERE id=$3 AND workspace_id=$4 AND version=1`,
		string(snapshotID), now, string(draftID), string(workspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.workflow_start_outbox(
		id,workspace_id,snapshot_id,template_revision_id,template_kind,status,available_at,attempt_count,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,'INTERVIEW_REVIEW','PENDING',$5,0,1,$5,$5)`,
		string(outboxID), string(workspaceID), string(snapshotID), string(revisionID), now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func seedOrganizingTerminalArtifact(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, artifactID, revisionID foundation.ID, resultHash string, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact(
		id,workspace_id,artifact_type,title,scope,status,version,created_at,updated_at
	) VALUES($1,$2,'SUMMARY','terminal artifact','{}'::jsonb,'DRAFT',1,$3,$3)`,
		string(artifactID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_revision(
		id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,
		content_markdown,provenance,content_hash,created_at
	) VALUES($1,$2,$3,1,'DRAFT','[]'::jsonb,'[]'::jsonb,'{}'::jsonb,'[]'::jsonb,'[]'::jsonb,
		'terminal artifact','{}'::jsonb,$4,$5)`, string(revisionID), string(artifactID), string(workspaceID), resultHash, now); err != nil {
		t.Fatal(err)
	}
}

func organizingTerminalReceipt(t *testing.T, resultID, bindingID, snapshotID, artifactID foundation.ID, resultHash string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(struct {
		SchemaVersion int           `json:"schema_version"`
		ResultID      foundation.ID `json:"result_id"`
		RunBindingID  foundation.ID `json:"run_binding_id"`
		SnapshotID    foundation.ID `json:"snapshot_id"`
		Kind          string        `json:"kind"`
		ResultRef     foundation.ID `json:"result_ref"`
		ResultHash    string        `json:"result_hash"`
	}{1, resultID, bindingID, snapshotID, "ARTIFACT", artifactID, resultHash})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertOrganizingTerminalPGCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("postgres error=%v want code=%s", err, code)
	}
}
