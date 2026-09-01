//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func TestRepositoryModelRunMatchesAttemptModelSettingsRevisionAndRecoversIt(t *testing.T) {
	testAgentRepositoryIntegrationVariants(t, testRepositoryModelRunMatchesAttemptModelSettingsRevisionAndRecoversIt)
}

func testRepositoryModelRunMatchesAttemptModelSettingsRevisionAndRecoversIt(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	repository agentRepositoryIntegrationStore,
) {
	pool := platform.DB()
	seedAgentRuntime(t, ctx, pool)
	nodeID := testAgentID(91)
	attemptID := testAgentID(92)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at)
		VALUES($1,$2,'agent-managed','agent.relation-assessment','running','{}','worker',
			now()+interval '5 minutes',1,now(),now())`, nodeID, testAgentID(4)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_runtime(
		role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
		VALUES('worker',$1::uuid,0,NULL,'active',now(),now())`, testAgentID(93)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,
		model_settings_revision,model_runtime_instance_id,started_at)
		VALUES($1,$2,1,1,0,'agent-managed','worker',now()+interval '5 minutes','running',
			0,$3,now())`, attemptID, nodeID, testAgentID(93)); err != nil {
		t.Fatal(err)
	}

	started := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
	run := testModelRun(testAgentID(94), nodeID, started)
	run.NodeAttemptID = attemptID
	if _, _, err := repository.CreateModelRun(ctx, run); agentErrorCode(err) != ErrorCodeRuntimeConsistency {
		t.Fatalf("static model run accepted for managed attempt: code=%s err=%v", agentErrorCode(err), err)
	}
	revision := int64(0)
	run.ModelSettingsRevision = &revision
	created, replayed, err := repository.CreateModelRun(ctx, run)
	if err != nil || replayed || created.ModelSettingsRevision == nil || *created.ModelSettingsRevision != 0 {
		t.Fatalf("managed model run=%#v replayed=%t err=%v", created, replayed, err)
	}

	recoveryAt := time.Now().UTC().Truncate(time.Microsecond)
	recovered, err := repository.MarkStaleModelRunsUnknown(ctx, application.UnknownRecoveryQuery{
		Before: recoveryAt.Add(-time.Minute), At: recoveryAt, Limit: 10,
	})
	if err != nil || len(recovered) != 1 || recovered[0].ID != run.ID ||
		recovered[0].ModelSettingsRevision == nil || *recovered[0].ModelSettingsRevision != 0 {
		t.Fatalf("recovered runs=%#v err=%v", recovered, err)
	}
}
