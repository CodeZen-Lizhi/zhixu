//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newHealthIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
	if fixture == nil || fixture.Pool() == nil || fixture.Pool().DB() == nil {
		t.Fatal("health PostgreSQL fixture did not provide a shared pool")
	}
	return fixture.Pool().DB()
}

// cleanupHealthIntegrationWorkspace 清理真实 PostgreSQL/River Health fixture。
// 测试库约束刻意禁止业务事实删除，因此 cleanup 在专用测试连接上临时关闭触发器。
func cleanupHealthIntegrationWorkspace(t *testing.T, pool *pgxpool.Pool, workspaceID foundation.ID) {
	t.Helper()
	ctx := context.Background()
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Errorf("acquire health fixture cleanup connection: %v", err)
		return
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `SET session_replication_role = replica`); err != nil {
		t.Errorf("disable health fixture triggers: %v", err)
		return
	}
	defer func() {
		if _, restoreErr := connection.Exec(ctx, `SET session_replication_role = origin`); restoreErr != nil {
			t.Errorf("restore health fixture triggers: %v", restoreErr)
		}
	}()
	statements := []string{
		`DELETE FROM workflow.river_job WHERE args->>'node_run_id' IN (
			SELECT node.id::text FROM workflow.node_run node
			JOIN workflow.run run ON run.id=node.run_id WHERE run.workspace_id=$1)`,
		`DELETE FROM ops.health_affected_change_outbox WHERE workspace_id=$1`,
		`DELETE FROM ops.health_issue_decision WHERE workspace_id=$1`,
		`DELETE FROM ops.health_issue_evidence WHERE workspace_id=$1`,
		`DELETE FROM ops.health_issue_observation WHERE workspace_id=$1`,
		`DELETE FROM ops.health_issue WHERE workspace_id=$1`,
		`DELETE FROM ops.health_scan_seen_identity WHERE workspace_id=$1`,
		`DELETE FROM ops.health_scan_detector WHERE workspace_id=$1`,
		`DELETE FROM ops.health_scan WHERE workspace_id=$1`,
		`DELETE FROM ops.health_schedule_command WHERE workspace_id=$1`,
		`DELETE FROM ops.health_schedule WHERE workspace_id=$1`,
		`DELETE FROM workflow.node_attempt WHERE node_run_id IN (
			SELECT node.id FROM workflow.node_run node JOIN workflow.run run ON run.id=node.run_id WHERE run.workspace_id=$1)`,
		`DELETE FROM workflow.human_task WHERE run_id IN (SELECT id FROM workflow.run WHERE workspace_id=$1)`,
		`DELETE FROM workflow.control_command WHERE run_id IN (SELECT id FROM workflow.run WHERE workspace_id=$1)`,
		`DELETE FROM ops.server_event WHERE workspace_id=$1`,
		`DELETE FROM workflow.outbox_event WHERE workspace_id=$1`,
		`DELETE FROM workflow.node_run WHERE run_id IN (SELECT id FROM workflow.run WHERE workspace_id=$1)`,
		`DELETE FROM workflow.run WHERE workspace_id=$1`,
		`DELETE FROM workflow.definition WHERE workspace_id=$1`,
		`DELETE FROM retrieval.chunk_projection WHERE workspace_id=$1`,
		`DELETE FROM retrieval.index_manifest_chunk WHERE workspace_id=$1`,
		`DELETE FROM retrieval.index_version WHERE workspace_id=$1`,
		`DELETE FROM core.knowledge_command_receipt WHERE workspace_id=$1`,
		`DELETE FROM core.topic WHERE workspace_id=$1`,
		`DELETE FROM core.workspace WHERE id=$1`,
	}
	for _, statement := range statements {
		if _, err := connection.Exec(ctx, statement, string(workspaceID)); err != nil {
			t.Errorf("cleanup health integration fixture: %v", err)
			return
		}
	}
}
