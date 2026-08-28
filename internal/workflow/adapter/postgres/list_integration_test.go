//go:build integration

package workflowpostgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
)

func TestRepositoryListRunsWithPostgres(t *testing.T) {
	ctx := context.Background()
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 8})
	pool := fixture.Pool().DB()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	workspaceID := workflowListID(1)
	otherWorkspaceID := workflowListID(2)
	insertWorkflowListWorkspace(t, ctx, tx, workspaceID, "workflow-list", now)
	insertWorkflowListWorkspace(t, ctx, tx, otherWorkspaceID, "workflow-list-other", now)
	definitionID := workflowListID(3)
	otherDefinitionID := workflowListID(4)
	insertWorkflowListDefinition(t, ctx, tx, definitionID, workspaceID, "editorial-review", 3, now)
	insertWorkflowListDefinition(t, ctx, tx, otherDefinitionID, otherWorkspaceID, "other-review", 1, now)

	firstID := workflowListID(11)
	secondID := workflowListID(12)
	thirdID := workflowListID(13)
	fourthID := workflowListID(14)
	insertWorkflowListRun(t, ctx, tx, firstID, workspaceID, definitionID, domain.RunStatusRunning, now.Add(8*time.Hour), nil)
	insertWorkflowListRun(t, ctx, tx, secondID, workspaceID, definitionID, domain.RunStatusWaitingForHuman, now.Add(7*time.Hour), nil)
	insertWorkflowListRun(t, ctx, tx, thirdID, workspaceID, definitionID, domain.RunStatusSucceeded, now.Add(6*time.Hour), timePointer(now.Add(6*time.Hour)))
	insertWorkflowListRun(t, ctx, tx, fourthID, workspaceID, definitionID, domain.RunStatusPending, now.Add(5*time.Hour), nil)
	insertWorkflowListRun(t, ctx, tx, workflowListID(21), otherWorkspaceID, otherDefinitionID, domain.RunStatusRunning, now.Add(10*time.Hour), nil)
	insertWorkflowListHumanTask(t, ctx, tx, secondID, now.Add(7*time.Hour))
	planNow := now.Add(48 * time.Hour)
	seedWorkflowListPlanRuns(t, ctx, tx, otherWorkspaceID, otherDefinitionID, planNow)
	assertWorkflowListPlans(t, ctx, tx, otherWorkspaceID, planNow)

	firstPage, hasMore, err := repository.ListRuns(ctx, domain.RunListQuery{WorkspaceID: workspaceID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore || len(firstPage) != 2 || firstPage[0].ID != firstID || firstPage[1].ID != secondID || !firstPage[1].WaitingForHuman || firstPage[0].DefinitionKey != "editorial-review" || firstPage[0].DefinitionVersion != 3 {
		t.Fatalf("first page=%#v hasMore=%v", firstPage, hasMore)
	}
	cursorTime := firstPage[1].UpdatedAt
	secondPage, hasMore, err := repository.ListRuns(ctx, domain.RunListQuery{WorkspaceID: workspaceID, CursorTime: &cursorTime, CursorID: firstPage[1].ID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(secondPage) != 2 || secondPage[0].ID != thirdID || secondPage[1].ID != fourthID {
		t.Fatalf("second page=%#v hasMore=%v", secondPage, hasMore)
	}
	assertWorkflowListIDs(t, repository, ctx, domain.RunListQuery{WorkspaceID: workspaceID, Status: domain.RunStatusWaitingForHuman, Limit: 10}, secondID)
	assertWorkflowListIDs(t, repository, ctx, domain.RunListQuery{WorkspaceID: workspaceID, Status: domain.RunStatusPaused, Limit: 10})
}

func insertWorkflowListWorkspace(t *testing.T, ctx context.Context, tx pgx.Tx, id foundation.ID, suffix string, now time.Time) {
	t.Helper()
	root := "/tmp/zhixu-m9-" + suffix
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,$2,$3,$3,$4,'inactive',1,$4,$4)`, string(id), suffix, root, now); err != nil {
		t.Fatal(err)
	}
}

func insertWorkflowListDefinition(t *testing.T, ctx context.Context, tx pgx.Tx, id, workspaceID foundation.ID, key string, version int64, now time.Time) {
	t.Helper()
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,$3,$4,'{}',$5)`, string(id), string(workspaceID), key, version, now); err != nil {
		t.Fatal(err)
	}
}

func insertWorkflowListRun(t *testing.T, ctx context.Context, tx pgx.Tx, id, workspaceID, definitionID foundation.ID, status domain.RunStatus, updatedAt time.Time, completedAt *time.Time) {
	t.Helper()
	createdAt := updatedAt.Add(-time.Hour)
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at,completed_at) VALUES($1,$2,$3,$4,'{}',1,$5,$6,$7)`, string(id), string(workspaceID), string(definitionID), string(status), createdAt, updatedAt, completedAt); err != nil {
		t.Fatal(err)
	}
}

func insertWorkflowListHumanTask(t *testing.T, ctx context.Context, tx pgx.Tx, runID foundation.ID, now time.Time) {
	t.Helper()
	nodeID := workflowListID(31)
	taskID := workflowListID(32)
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,input,version,created_at,updated_at) VALUES($1,$2,'review','human','waiting_for_human','{}',1,$3,$3)`, string(nodeID), string(runID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.human_task(id,run_id,node_run_id,status,expected_input_schema,target_version,created_at) VALUES($1,$2,$3,'pending','{}',1,$4)`, string(taskID), string(runID), string(nodeID), now); err != nil {
		t.Fatal(err)
	}
}

func assertWorkflowListIDs(t *testing.T, repository *Repository, ctx context.Context, query domain.RunListQuery, want ...foundation.ID) {
	t.Helper()
	items, hasMore, err := repository.ListRuns(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(items) != len(want) {
		t.Fatalf("items=%#v hasMore=%v want=%#v", items, hasMore, want)
	}
	for index := range want {
		if items[index].ID != want[index] || items[index].WorkspaceID != query.WorkspaceID {
			t.Fatalf("items[%d]=%#v want=%s", index, items[index], want[index])
		}
	}
}

func seedWorkflowListPlanRuns(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, definitionID foundation.ID, newest time.Time) {
	t.Helper()
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.run(
		id,workspace_id,definition_id,status,input,version,created_at,updated_at
	)
	SELECT ('9a000000-0000-4000-8000-' || lpad(value::text,12,'0'))::uuid,
		$1,$2,CASE WHEN value <= 2 THEN 'running' ELSE 'pending' END,'{}',1,
		$3::timestamptz - value * interval '1 second' - interval '1 hour',
		$3::timestamptz - value * interval '1 second'
	FROM generate_series(1,16384) AS value`, string(workspaceID), string(definitionID), newest.UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE workflow.run, workflow.definition, workflow.human_task`); err != nil {
		t.Fatal(err)
	}
}

func assertWorkflowListPlans(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, newest time.Time) {
	t.Helper()
	performanceIndexesAvailable := workflowListIndexesAvailable(t, ctx, tx)
	if !performanceIndexesAvailable {
		t.Log("workflow list updated_at indexes are absent; validating the migration-29 compatibility plan separately from the M9 performance gate")
	}
	assertWorkflowListPlan(t, ctx, tx, "first page", domain.RunListQuery{
		WorkspaceID: workspaceID,
		Limit:       2,
	}, []string{"idx_workflow_run_workspace_updated_id"}, performanceIndexesAvailable)
	cursorTime := newest.Add(-256 * time.Second)
	assertWorkflowListPlan(t, ctx, tx, "cursor page", domain.RunListQuery{
		WorkspaceID: workspaceID,
		CursorTime:  &cursorTime,
		CursorID:    workflowListPlanID(256),
		Limit:       2,
	}, []string{"idx_workflow_run_workspace_updated_id"}, performanceIndexesAvailable)
	assertWorkflowListPlan(t, ctx, tx, "selective status", domain.RunListQuery{
		WorkspaceID: workspaceID,
		Status:      domain.RunStatusRunning,
		Limit:       2,
	}, []string{"idx_workflow_run_workspace_status_updated_id", "idx_workflow_run_control"}, performanceIndexesAvailable)
}

func workflowListIndexesAvailable(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	for _, index := range []string{
		"workflow.idx_workflow_run_workspace_updated_id",
		"workflow.idx_workflow_run_workspace_status_updated_id",
	} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, index).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			return false
		}
	}
	return true
}

func assertWorkflowListPlan(t *testing.T, ctx context.Context, tx pgx.Tx, label string, request domain.RunListQuery, allowedIndexes []string, performanceIndexesAvailable bool) {
	t.Helper()
	query, args := buildRunListQuery(request)
	var raw []byte
	if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var documents []struct {
		Plan workflowListExplainPlan `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid workflow list explain json: %v %s", err, raw)
	}
	if documents[0].Plan.NodeType != "Limit" {
		t.Fatalf("workflow %s plan is not bounded by Limit: %s", label, raw)
	}
	path, found := workflowRunAccessPath(documents[0].Plan, nil)
	if !found {
		t.Fatalf("workflow %s plan has no run access path: %s", label, raw)
	}
	runPlan := path[len(path)-1]
	if runPlan.RelationName != "run" || runPlan.Alias != "r" {
		t.Fatalf("workflow %s run path is not rooted at workflow.run: %s", label, raw)
	}
	if performanceIndexesAvailable {
		if runPlan.NodeType != "Index Scan" && runPlan.NodeType != "Index Only Scan" {
			t.Fatalf("workflow %s run path is not a direct bounded index scan: %s", label, raw)
		}
		if !workflowListPlanUsesAnyIndex(runPlan, allowedIndexes) {
			t.Fatalf("workflow %s run path misses allowed indexes %v: %s", label, allowedIndexes, raw)
		}
		for _, node := range path {
			if node.NodeType == "Seq Scan" && node.RelationName == "run" {
				t.Fatalf("workflow %s run path contains an unbounded workflow.run seq scan: %s", label, raw)
			}
		}
	}
	if request.CursorTime != nil && !workflowListPlanHasKeysetPredicate(path) {
		t.Fatalf("workflow %s plan is missing the updated_at/id keyset predicate: %s", label, raw)
	}
	t.Logf("workflow %s plan: %s", label, raw)
}

type workflowListExplainPlan struct {
	NodeType     string                    `json:"Node Type"`
	RelationName string                    `json:"Relation Name"`
	Alias        string                    `json:"Alias"`
	IndexName    string                    `json:"Index Name"`
	IndexCond    string                    `json:"Index Cond"`
	Filter       string                    `json:"Filter"`
	Plans        []workflowListExplainPlan `json:"Plans"`
}

func workflowListPlanHasKeysetPredicate(path []workflowListExplainPlan) bool {
	for _, node := range path {
		condition := strings.ToLower(node.IndexCond + " " + node.Filter)
		if strings.Contains(condition, "updated_at") && strings.Contains(condition, "id") && strings.Contains(condition, "<") {
			return true
		}
	}
	return false
}

func workflowRunAccessPath(plan workflowListExplainPlan, path []workflowListExplainPlan) ([]workflowListExplainPlan, bool) {
	path = append(path, plan)
	if plan.RelationName == "run" && plan.Alias == "r" {
		return path, true
	}
	for _, child := range plan.Plans {
		if foundPath, found := workflowRunAccessPath(child, path); found {
			return foundPath, true
		}
	}
	return nil, false
}

func workflowListPlanUsesAnyIndex(plan workflowListExplainPlan, indexNames []string) bool {
	for _, indexName := range indexNames {
		if plan.IndexName == indexName {
			return true
		}
	}
	for _, child := range plan.Plans {
		if workflowListPlanUsesAnyIndex(child, indexNames) {
			return true
		}
	}
	return false
}

func workflowListID(n int) foundation.ID {
	return foundation.ID(fmt.Sprintf("92000000-0000-4000-8000-%012d", n))
}

func workflowListPlanID(n int) foundation.ID {
	return foundation.ID(fmt.Sprintf("9a000000-0000-4000-8000-%012d", n))
}

func timePointer(value time.Time) *time.Time {
	return &value
}
