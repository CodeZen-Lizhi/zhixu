//go:build integration

package workflowpostgres

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestRuntimeHumanSchemaRejectsWithoutTransitionAndReplays(t *testing.T) {
	ctx := t.Context()
	platformPool, cleanup := newGORMRuntimeTestDatabase(t, ctx)
	defer cleanup()
	pool := platformPool.DB()
	workspaceID := foundation.ID("a7100000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-human-schema',$2,$2,CURRENT_TIMESTAMP,'inactive',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-human-schema"); err != nil {
		t.Fatal(err)
	}
	repository := newGORMRuntimeTestRepository(t, platformPool, GORMRuntimeRepositoryHooks{})
	request := runtimeStateStartFixtureWithPermissions(t, workspaceID, "human-schema", domain.RetryPolicy{}, capability.ReadLocal, capability.WriteProposal)
	var graph domain.CanonicalGraph
	if err := json.Unmarshal(request.Definition.Graph, &graph); err != nil {
		t.Fatal(err)
	}
	graph.Nodes = append(graph.Nodes, domain.NodeDefinition{Key: "after", Kind: application.CanonicalJSONHashNodeKind, Dependencies: []string{"hash"}, InputSchemaVersion: 1, OutputSchemaVersion: 1})
	var err error
	request.Definition.Graph, err = json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	started, err := repository.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "schema-delivery", RiverJobID: started.Job.JobID, LeaseOwner: "schema-worker", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	human, err := application.NewRuntimeHumanCoordinator(repository)
	if err != nil {
		t.Fatal(err)
	}
	waited, err := human.WaitForHuman(ctx, application.HumanWaitCommand{TaskID: foundation.ID("a7100000-0000-4000-8000-000000000015"), RunID: started.Run.ID, NodeRunID: started.FirstNode.ID, Fence: domain.LeaseFence{Owner: "schema-worker", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version}, ExpectedInputSchema: json.RawMessage(humanIdentitySchema), TargetVersion: 1, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	command := application.HumanDecisionCommand{RunID: started.Run.ID, TaskID: waited.Task.ID, TargetVersion: 1, CallerCapabilities: []capability.Capability{capability.ReadLocal, capability.WriteProposal}}
	for _, field := range []string{"processing_id", "result_hash"} {
		decision := humanIdentityDecision()
		decision[field] = "wrong"
		command.Decision, _ = json.Marshal(decision)
		if _, err := human.SubmitHuman(ctx, command); !hasCode(err, "HUMAN_DECISION_SCHEMA_INVALID") {
			t.Fatalf("%s: %v", field, err)
		}
		var taskStatus, nodeStatus string
		var successorCount int
		if err := pool.QueryRow(ctx, `SELECT status FROM workflow.human_task WHERE id=$1`, string(waited.Task.ID)).Scan(&taskStatus); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT status FROM workflow.node_run WHERE id=$1`, string(waited.Node.ID)).Scan(&nodeStatus); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.node_run WHERE run_id=$1 AND node_key='after'`, string(started.Run.ID)).Scan(&successorCount); err != nil {
			t.Fatal(err)
		}
		if taskStatus != string(domain.HumanTaskPending) || nodeStatus != string(domain.NodeStatusWaitingForHuman) || successorCount != 0 {
			t.Fatalf("rejected submit mutated task=%s node=%s successors=%d", taskStatus, nodeStatus, successorCount)
		}
	}
	command.Decision, _ = json.Marshal(humanIdentityDecision())
	submitted, err := human.SubmitHuman(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Task.Status != domain.HumanTaskSubmitted || submitted.Node.Status != domain.NodeStatusSucceeded {
		t.Fatalf("submitted=%+v", submitted)
	}
	var successorID string
	if err := pool.QueryRow(ctx, `SELECT id FROM workflow.node_run WHERE run_id=$1 AND node_key='after' AND status='pending'`, string(started.Run.ID)).Scan(&successorID); err != nil {
		t.Fatal(err)
	}
	replayed, err := human.SubmitHuman(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Task.ID != submitted.Task.ID || !jsonEqual(replayed.Task.Decision, command.Decision) || replayed.Node.Version != submitted.Node.Version {
		t.Fatalf("replay=%+v", replayed)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.node_run WHERE run_id=$1 AND node_key='after' AND id=$2`, string(started.Run.ID), successorID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("successor replay count=%d err=%v", count, err)
	}
	// 不同的决策即使符合 Schema，仍属于冲突重放。
	decision := humanIdentityDecision()
	decision["result_hash"] = strings.Repeat("b", 64)
	command.Decision, _ = json.Marshal(decision)
	if _, err := human.SubmitHuman(ctx, command); !hasCode(err, "HUMAN_DECISION_CONFLICT") {
		t.Fatalf("conflicting replay=%v", err)
	}
}
