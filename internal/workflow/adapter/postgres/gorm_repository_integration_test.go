//go:build integration

package workflowpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestGORMRepositoryListAndOutputRunWithPostgres(t *testing.T) {
	ctx := context.Background()
	platformPool, cleanup := newGORMRuntimeTestDatabase(t, ctx)
	defer cleanup()

	repository, err := NewGORMRepository(platformPool)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []string{
		"workflow.idx_workflow_run_workspace_updated_id",
		"workflow.idx_workflow_run_workspace_status_updated_id",
	} {
		var exists bool
		if err := platformPool.DB().QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, index).Scan(&exists); err != nil || !exists {
			t.Fatalf("target index %s exists=%v err=%v", index, exists, err)
		}
	}
	now := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	workspaceID := gormWorkflowIntegrationID(1)
	otherWorkspaceID := gormWorkflowIntegrationID(2)
	for _, workspace := range []struct {
		id     foundation.ID
		name   string
		status string
	}{
		{id: workspaceID, name: "gorm-workflow", status: "active"},
		{id: otherWorkspaceID, name: "gorm-workflow-other", status: "inactive"},
	} {
		if _, err := platformPool.DB().Exec(ctx, `
			INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
			VALUES($1,$2,$3,$3,$4,$5,1,$4,$4)`,
			string(workspace.id), workspace.name, "/tmp/"+workspace.name, now, workspace.status); err != nil {
			t.Fatal(err)
		}
	}

	completedRequest := gormRepositoryStartRequest(workspaceID, now, 10, "completed")
	completedRun, err := repository.Start(ctx, completedRequest)
	if err != nil {
		t.Fatalf("start error chain: %s", gormWorkflowIntegrationErrorChain(err))
	}
	loaded, err := repository.GetRun(ctx, completedRun.ID)
	if err != nil || loaded.ID != completedRun.ID || loaded.WorkspaceID != workspaceID {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	leaseUntil := now.Add(30 * time.Minute)
	claimed, err := repository.ClaimNode(ctx, completedRequest.FirstNode.ID, "worker-gorm", now.Add(time.Minute), leaseUntil)
	if err != nil || claimed.Status != domain.NodeStatusRunning || claimed.LeaseOwner != "worker-gorm" {
		t.Fatalf("claimed=%#v err=%s", claimed, gormWorkflowIntegrationErrorChain(err))
	}
	extendedUntil := leaseUntil.Add(15 * time.Minute)
	heartbeat, err := repository.HeartbeatNode(ctx, claimed.ID, "worker-gorm", now.Add(2*time.Minute), extendedUntil)
	if err != nil || heartbeat.LeaseUntil == nil || !heartbeat.LeaseUntil.Equal(extendedUntil) {
		t.Fatalf("heartbeat=%#v err=%v", heartbeat, err)
	}
	completionEvent := gormWorkflowEvent(workspaceID, completedRun.ID, now.Add(3*time.Minute), 20, "completed")
	completedNode, err := repository.CompleteNode(ctx, domain.Completion{
		NodeID: claimed.ID, LeaseOwner: "worker-gorm", Output: json.RawMessage(`{"digest":"ok"}`),
		At: now.Add(3 * time.Minute), Event: completionEvent,
	})
	if err != nil || completedNode.Status != domain.NodeStatusSucceeded {
		t.Fatalf("completed=%#v err=%v", completedNode, err)
	}
	if replayed, replayErr := repository.CompleteNode(ctx, domain.Completion{
		NodeID: claimed.ID, LeaseOwner: "worker-gorm", Output: json.RawMessage(`{"digest":"ok"}`),
		At: now.Add(4 * time.Minute), Event: completionEvent,
	}); replayErr != nil || replayed.ID != completedNode.ID {
		t.Fatalf("replayed=%#v err=%v", replayed, replayErr)
	}

	humanRequest := gormRepositoryStartRequest(workspaceID, now.Add(time.Hour), 30, "human")
	humanRun, err := repository.Start(ctx, humanRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ClaimNode(ctx, humanRequest.FirstNode.ID, "worker-human", now.Add(time.Hour+time.Minute), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	task := domain.HumanTask{
		ID: gormWorkflowIntegrationID(40), RunID: humanRun.ID, NodeRunID: humanRequest.FirstNode.ID,
		Status: domain.HumanTaskPending, ExpectedInputSchema: json.RawMessage(`{"type":"object"}`),
		TargetVersion: 1, CreatedAt: now.Add(time.Hour + 2*time.Minute),
	}
	humanEvent := gormWorkflowEvent(workspaceID, humanRun.ID, task.CreatedAt, 41, "human")
	persistedTask, err := repository.CreateHumanTask(ctx, task, humanEvent, task.CreatedAt)
	if err != nil || persistedTask.ID != task.ID {
		t.Fatalf("task=%#v err=%v", persistedTask, err)
	}
	pending, found, err := repository.GetPendingHumanTask(ctx, humanRun.ID)
	if err != nil || !found || pending.ID != task.ID {
		t.Fatalf("pending=%#v found=%v err=%v", pending, found, err)
	}

	items, hasMore, err := repository.ListRuns(ctx, domain.RunListQuery{WorkspaceID: workspaceID, Limit: 1})
	if err != nil || !hasMore || len(items) != 1 || items[0].ID != humanRun.ID || !items[0].WaitingForHuman {
		t.Fatalf("items=%#v hasMore=%v err=%v", items, hasMore, err)
	}
	cursorTime := items[0].UpdatedAt
	items, hasMore, err = repository.ListRuns(ctx, domain.RunListQuery{
		WorkspaceID: workspaceID, CursorTime: &cursorTime, CursorID: items[0].ID, Limit: 1,
	})
	if err != nil || hasMore || len(items) != 1 || items[0].ID != completedRun.ID {
		t.Fatalf("cursor items=%#v hasMore=%v err=%v", items, hasMore, err)
	}

	gormDatabase, err := platformPool.GORM()
	if err != nil {
		t.Fatal(err)
	}
	runtimeOutput := &GORMRuntimeRepository{database: gormDatabase}
	input, err := runtimeOutput.GetRunInput(ctx, workspaceID, completedRun.ID)
	if err != nil || !jsonEqual(input, json.RawMessage(`{"request":"completed"}`)) {
		t.Fatalf("input=%s err=%v", input, err)
	}
	output, err := runtimeOutput.GetSucceededNodeOutput(ctx, workspaceID, completedRun.ID, completedRequest.FirstNode.NodeKey)
	if err != nil || !jsonEqual(output, json.RawMessage(`{"digest":"ok"}`)) {
		t.Fatalf("output=%s err=%v", output, err)
	}
	definition, err := runtimeOutput.GetRunDefinition(ctx, workspaceID, completedRun.ID)
	if err != nil || definition.ID != completedRequest.Definition.ID || definition.GraphHash == "" {
		t.Fatalf("definition=%#v err=%v", definition, err)
	}
	humanNode, err := runtimeOutput.GetPendingHumanTaskNode(ctx, workspaceID, humanRun.ID, task.ID, task.NodeRunID)
	if err != nil || humanNode.ID != task.NodeRunID || humanNode.Status != domain.NodeStatusWaitingForHuman {
		t.Fatalf("human node=%#v err=%v", humanNode, err)
	}
	if _, err := runtimeOutput.GetRunInput(ctx, otherWorkspaceID, completedRun.ID); !hasCode(err, "WORKFLOW_RUN_INPUT_NOT_FOUND") {
		t.Fatalf("cross-workspace input err=%v", err)
	}
	corruptDefinitionID := gormWorkflowIntegrationID(70)
	corruptRunID := gormWorkflowIntegrationID(71)
	if _, err := platformPool.DB().Exec(ctx, `
		INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,'gorm-corrupt-graph',1,'{}'::jsonb,$3)`,
		string(corruptDefinitionID), string(workspaceID), now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := platformPool.DB().Exec(ctx, `
		INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
		VALUES($1,$2,$3,'pending','{}'::jsonb,1,$4,$4)`,
		string(corruptRunID), string(workspaceID), string(corruptDefinitionID), now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeOutput.GetRunDefinition(ctx, workspaceID, corruptRunID); !hasCode(err, "WORKFLOW_DEFINITION_GRAPH_INVALID") {
		t.Fatalf("corrupt definition error=%v", err)
	}

	invalidReference := gormRepositoryStartRequest(gormWorkflowIntegrationID(999), now.Add(2*time.Hour), 50, "invalid-reference")
	if _, err := repository.Start(ctx, invalidReference); !hasCode(err, "WORKFLOW_REFERENCE_INVALID") {
		t.Fatalf("reference error=%v", err)
	} else {
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != "23503" {
			t.Fatalf("reference cause=%T %#v", err, postgresError)
		}
	}

	for range 16 {
		if _, err := repository.GetRun(ctx, completedRun.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.ListRuns(ctx, domain.RunListQuery{WorkspaceID: workspaceID, Limit: 2}); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeOutput.GetRunInput(ctx, workspaceID, completedRun.ID); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for platformPool.DB().Stat().AcquiredConns() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired := platformPool.DB().Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("workflow GORM queries retained %d PostgreSQL connections", acquired)
	}
}

func gormRepositoryStartRequest(workspaceID foundation.ID, now time.Time, base int, suffix string) domain.StartRequest {
	definitionID := gormWorkflowIntegrationID(base + 1)
	runID := gormWorkflowIntegrationID(base + 2)
	nodeID := gormWorkflowIntegrationID(base + 3)
	runIDCopy := runID
	graph := domain.CanonicalGraph{Nodes: []domain.NodeDefinition{{
		Key: "first", Kind: application.CanonicalJSONHashNodeKind,
		InputSchemaVersion: 1, OutputSchemaVersion: 1,
	}}}
	encodedGraph, _ := json.Marshal(graph)
	return domain.StartRequest{
		Definition: domain.Definition{ID: definitionID, WorkspaceID: workspaceID, Key: "gorm-" + suffix, Version: 1, Graph: encodedGraph, CreatedAt: now},
		Run: domain.Run{
			ID: runID, WorkspaceID: workspaceID, Status: domain.RunStatusPending,
			Input: json.RawMessage(fmt.Sprintf(`{"request":%q}`, suffix)), IdempotencyKey: "run-" + suffix,
			RequestHash: fmt.Sprintf("%064x", base), Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		FirstNode: domain.NodeRun{
			ID: nodeID, RunID: runID, NodeKey: "first", NodeType: application.CanonicalJSONHashNodeKind,
			Status: domain.NodeStatusPending, Input: json.RawMessage(`{"value":1}`), IdempotencyKey: "node-" + suffix,
			InputSchemaVersion: 1, OutputSchemaVersion: 1, DispatchNo: 1,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		Event: domain.OutboxEvent{
			ID: gormWorkflowIntegrationID(base + 4), WorkspaceID: workspaceID, RunID: &runIDCopy,
			Type: "workflow.run.started", IdempotencyKey: "start-" + suffix, EventKey: "start-" + suffix,
			SchemaVersion: 1, EventVersion: 1, Payload: json.RawMessage(`{}`), OccurredAt: now,
		},
	}
}

func gormWorkflowEvent(workspaceID, runID foundation.ID, now time.Time, id int, suffix string) domain.OutboxEvent {
	runIDCopy := runID
	return domain.OutboxEvent{
		ID: gormWorkflowIntegrationID(id), WorkspaceID: workspaceID, RunID: &runIDCopy,
		Type: "workflow.node.changed", IdempotencyKey: "event-" + suffix, Payload: json.RawMessage(`{}`), OccurredAt: now,
	}
}

func gormWorkflowIntegrationID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("7a000000-0000-4000-8000-%012d", value))
}

func gormWorkflowIntegrationErrorChain(err error) string {
	chain := ""
	for current := err; current != nil; current = errors.Unwrap(current) {
		if chain != "" {
			chain += " -> "
		}
		chain += fmt.Sprintf("%T(%v)", current, current)
	}
	return chain
}
