package workflowpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryLeaseCompletionAndHumanSubmission(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	workspaceID := testID(1)
	_, err = tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'Workflow Test',$2,$2,$3,'active',1,$3,$3)`, string(workspaceID), "/tmp/workflow-"+string(workspaceID), now)
	if err != nil {
		t.Fatal(err)
	}

	run, node := startFixture(t, ctx, repository, workspaceID, now, 1)
	claimed, err := repository.ClaimNode(ctx, node.ID, "worker-a", now, now.Add(time.Minute))
	if err != nil || claimed.Attempt != 1 {
		t.Fatalf("claim=%#v err=%v cause=%v", claimed, err, errors.Unwrap(err))
	}
	if _, err = repository.ClaimNode(ctx, node.ID, "worker-b", now.Add(30*time.Second), now.Add(2*time.Minute)); !hasCode(err, "WORKFLOW_NODE_NOT_CLAIMABLE") {
		t.Fatalf("active lease claim err=%v", err)
	}
	reclaimed, err := repository.ClaimNode(ctx, node.ID, "worker-b", now.Add(2*time.Minute), now.Add(3*time.Minute))
	if err != nil || reclaimed.Attempt != 2 || reclaimed.LeaseOwner != "worker-b" {
		t.Fatalf("reclaim=%#v err=%v", reclaimed, err)
	}
	event := domain.OutboxEvent{ID: testID(10), WorkspaceID: workspaceID, RunID: &run.ID, Type: "workflow.node.succeeded", IdempotencyKey: "complete-one", Payload: json.RawMessage(`{}`), OccurredAt: now.Add(150 * time.Second)}
	completion := domain.Completion{NodeID: node.ID, LeaseOwner: "worker-b", Output: json.RawMessage(`{"ok":true}`), At: now.Add(150 * time.Second), Event: event}
	completion.Event.WorkspaceID = testID(99)
	if _, err = repository.CompleteNode(ctx, completion); !hasCode(err, "WORKFLOW_EVENT_SCOPE_INVALID") {
		t.Fatalf("cross-workspace completion err=%v", err)
	}
	completion.Event.WorkspaceID = workspaceID
	completed, err := repository.CompleteNode(ctx, completion)
	if err != nil || completed.Status != domain.StatusSucceeded {
		t.Fatalf("complete=%#v err=%v", completed, err)
	}
	if _, err = repository.CompleteNode(ctx, completion); err != nil {
		t.Fatalf("idempotent completion err=%v", err)
	}
	completion.Output = json.RawMessage(`{"ok":false}`)
	if _, err = repository.CompleteNode(ctx, completion); !hasCode(err, "WORKFLOW_NODE_ALREADY_COMPLETED") {
		t.Fatalf("different completion err=%v", err)
	}

	run2, node2 := startFixture(t, ctx, repository, workspaceID, now.Add(time.Hour), 20)
	if _, err = repository.ClaimNode(ctx, node2.ID, "worker-a", now.Add(time.Hour), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	expires := now.Add(3 * time.Hour)
	task := domain.HumanTask{ID: testID(30), RunID: run2.ID, NodeRunID: node2.ID, Status: domain.HumanTaskPending, ExpectedInputSchema: json.RawMessage(`{"type":"object"}`), TargetVersion: 2, ExpiresAt: &expires, CreatedAt: now.Add(time.Hour)}
	humanEvent := domain.OutboxEvent{ID: testID(31), WorkspaceID: workspaceID, RunID: &run2.ID, Type: "workflow.human.requested", IdempotencyKey: "human-request", Payload: json.RawMessage(`{}`), OccurredAt: now.Add(time.Hour)}
	if _, err = repository.CreateHumanTask(ctx, task, humanEvent, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	submitEvent := domain.OutboxEvent{ID: testID(32), WorkspaceID: workspaceID, RunID: &run2.ID, Type: "workflow.human.submitted", IdempotencyKey: "human-submit", Payload: json.RawMessage(`{}`), OccurredAt: now.Add(90 * time.Minute)}
	if _, err = repository.SubmitHumanTask(ctx, task.ID, 1, json.RawMessage(`{"approved":true}`), now.Add(90*time.Minute), submitEvent); !hasCode(err, "HUMAN_TASK_VERSION_CONFLICT") {
		t.Fatalf("version err=%v", err)
	}
	submitted, err := repository.SubmitHumanTask(ctx, task.ID, 2, json.RawMessage(`{"approved":true}`), now.Add(90*time.Minute), submitEvent)
	if err != nil || submitted.Status != domain.HumanTaskSubmitted {
		t.Fatalf("submitted=%#v err=%v", submitted, err)
	}
	if _, err = repository.SubmitHumanTask(ctx, task.ID, 2, json.RawMessage(`{"approved":true}`), now.Add(100*time.Minute), submitEvent); !hasCode(err, "HUMAN_TASK_ALREADY_RESOLVED") {
		t.Fatalf("duplicate submit err=%v", err)
	}

	run3, node3 := startFixture(t, ctx, repository, workspaceID, now.Add(4*time.Hour), 40)
	if _, err = repository.ClaimNode(ctx, node3.ID, "worker-a", now.Add(4*time.Hour), now.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	expiredAt := now.Add(4*time.Hour + time.Minute)
	expiredTask := domain.HumanTask{ID: testID(50), RunID: run3.ID, NodeRunID: node3.ID, Status: domain.HumanTaskPending, ExpectedInputSchema: json.RawMessage(`{}`), TargetVersion: 1, ExpiresAt: &expiredAt, CreatedAt: now.Add(4 * time.Hour)}
	expiredEvent := domain.OutboxEvent{ID: testID(51), WorkspaceID: workspaceID, RunID: &run3.ID, Type: "workflow.human.requested", IdempotencyKey: "human-expired-request", Payload: json.RawMessage(`{}`), OccurredAt: now.Add(4 * time.Hour)}
	if _, err = repository.CreateHumanTask(ctx, expiredTask, expiredEvent, now.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.SubmitHumanTask(ctx, expiredTask.ID, 1, json.RawMessage(`{"approved":true}`), now.Add(5*time.Hour), domain.OutboxEvent{}); !hasCode(err, "HUMAN_TASK_EXPIRED") {
		t.Fatalf("expired submit err=%v", err)
	}
}

func startFixture(t *testing.T, ctx context.Context, r *Repository, workspaceID foundation.ID, now time.Time, offset byte) (domain.Run, domain.NodeRun) {
	t.Helper()
	runID := testID(offset + 2)
	node := domain.NodeRun{ID: testID(offset + 3), RunID: runID, NodeKey: "first", NodeType: "deterministic", Status: domain.StatusPending, Input: json.RawMessage(`{}`), Version: 1, CreatedAt: now, UpdatedAt: now}
	request := domain.StartRequest{Definition: domain.Definition{ID: testID(offset + 1), WorkspaceID: workspaceID, Key: "flow-" + string(testID(offset)), Version: 1, Graph: json.RawMessage(`{"nodes":[]}`), CreatedAt: now}, Run: domain.Run{ID: runID, WorkspaceID: workspaceID, Status: domain.StatusPending, Input: json.RawMessage(`{}`), Version: 1, CreatedAt: now, UpdatedAt: now}, FirstNode: node, Event: domain.OutboxEvent{ID: testID(offset + 4), WorkspaceID: workspaceID, RunID: &runID, Type: "workflow.run.started", IdempotencyKey: "start-" + string(runID), Payload: json.RawMessage(`{}`), OccurredAt: now}}
	run, err := r.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return run, node
}

func testID(n byte) foundation.ID {
	return foundation.ID([]byte{hex(n >> 4), hex(n & 15), hex(n >> 4), hex(n & 15), hex(n >> 4), hex(n & 15), hex(n >> 4), hex(n & 15), '-', '0', '0', '0', '0', '-', '4', '0', '0', '0', '-', '8', '0', '0', '0', '-', '0', '0', '0', '0', '0', '0', '0', '0', '0', '0', '0', '0'})
}
func hex(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'a' + n - 10
}
func hasCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
