//go:build integration

package migration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestModelCallAgentAnswerPhaseMigration(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 77); err != nil {
		t.Fatalf("migrate to 00077: %v", err)
	}
	fixture := seedModelCallPhaseFixture(t, ctx, pool)

	legacyStructuredRun := fixture.newModelRun(t, ctx, pool)
	fixture.insertSequence(t, ctx, pool, legacyStructuredRun, []string{"INITIAL", "REPAIR", "REDUCED", "REVIEW"})
	legacyRAGRun := fixture.newModelRun(t, ctx, pool)
	fixture.insertSequence(t, ctx, pool, legacyRAGRun, []string{"PLAN", "INITIAL", "REVIEW"})

	if err := provider.UpTo(ctx, 83); err != nil {
		t.Fatalf("00083 up with legacy phase histories: %v", err)
	}
	assertModelCallPhaseMigrationShape(t, ctx, pool)
	if err := provider.UpTo(ctx, 83); err != nil {
		t.Fatalf("00083 re-up: %v", err)
	}

	fullAgentRun := fixture.newModelRun(t, ctx, pool)
	fixture.insertSequence(t, ctx, pool, fullAgentRun, []string{
		"PLAN", "AGENT", "AGENT", "ANSWER", "INITIAL", "REPAIR", "REDUCED", "REVIEW",
	})
	zeroAgentRun := fixture.newModelRun(t, ctx, pool)
	fixture.insertSequence(t, ctx, pool, zeroAgentRun, []string{"PLAN", "ANSWER", "INITIAL", "REVIEW"})
	directAgentRun := fixture.newModelRun(t, ctx, pool)
	fixture.insertSequence(t, ctx, pool, directAgentRun, []string{"AGENT", "AGENT", "ANSWER", "INITIAL", "REVIEW"})
	directAnswerRun := fixture.newModelRun(t, ctx, pool)
	fixture.insertSequence(t, ctx, pool, directAnswerRun, []string{"ANSWER", "INITIAL", "REVIEW"})

	for _, phase := range []string{"REPAIR", "REDUCED", "REVIEW"} {
		t.Run("rejects "+strings.ToLower(phase)+" as first phase", func(t *testing.T) {
			modelRunID := fixture.newModelRun(t, ctx, pool)
			err := fixture.insertStartedCall(ctx, pool, modelRunID, 1, phase)
			assertPostgresCode(t, err, "55000")
		})
	}
	t.Run("requires answer before generation after agent", func(t *testing.T) {
		modelRunID := fixture.newModelRun(t, ctx, pool)
		fixture.insertSequence(t, ctx, pool, modelRunID, []string{"PLAN", "AGENT"})
		err := fixture.insertStartedCall(ctx, pool, modelRunID, 3, "INITIAL")
		assertPostgresCode(t, err, "55000")
	})
	t.Run("requires initial after answer", func(t *testing.T) {
		modelRunID := fixture.newModelRun(t, ctx, pool)
		fixture.insertSequence(t, ctx, pool, modelRunID, []string{"PLAN", "ANSWER"})
		err := fixture.insertStartedCall(ctx, pool, modelRunID, 3, "REPAIR")
		assertPostgresCode(t, err, "55000")
	})
	t.Run("rejects duplicate answer", func(t *testing.T) {
		modelRunID := fixture.newModelRun(t, ctx, pool)
		fixture.insertSequence(t, ctx, pool, modelRunID, []string{"PLAN", "AGENT", "ANSWER"})
		err := fixture.insertStartedCall(ctx, pool, modelRunID, 4, "ANSWER")
		assertPostgresCode(t, err, "55000")
	})
	t.Run("requires a succeeded predecessor", func(t *testing.T) {
		for _, terminal := range []string{"FAILED", "UNKNOWN"} {
			t.Run(strings.ToLower(terminal), func(t *testing.T) {
				modelRunID := fixture.newModelRun(t, ctx, pool)
				if err := fixture.insertStartedCall(ctx, pool, modelRunID, 1, "PLAN"); err != nil {
					t.Fatal(err)
				}
				fixture.completeCurrentCallWithStatus(t, ctx, pool, terminal)
				err := fixture.insertStartedCall(ctx, pool, modelRunID, 2, "AGENT")
				assertPostgresCode(t, err, "55000")
			})
		}
	})
	t.Run("preserves contiguous call numbers", func(t *testing.T) {
		modelRunID := fixture.newModelRun(t, ctx, pool)
		fixture.insertSequence(t, ctx, pool, modelRunID, []string{"PLAN"})
		err := fixture.insertStartedCall(ctx, pool, modelRunID, 3, "ANSWER")
		assertPostgresCode(t, err, "55000")
	})
	t.Run("rejects calls after review", func(t *testing.T) {
		modelRunID := fixture.newModelRun(t, ctx, pool)
		fixture.insertSequence(t, ctx, pool, modelRunID, []string{"INITIAL", "REVIEW"})
		err := fixture.insertStartedCall(ctx, pool, modelRunID, 3, "REVIEW")
		assertPostgresCode(t, err, "55000")
	})
	t.Run("accepts call thirty two and rejects thirty three", func(t *testing.T) {
		modelRunID := fixture.newModelRun(t, ctx, pool)
		phases := make([]string, 32)
		for index := range phases {
			phases[index] = "AGENT"
		}
		fixture.insertSequence(t, ctx, pool, modelRunID, phases)
		err := fixture.insertStartedCall(ctx, pool, modelRunID, 33, "AGENT")
		assertPostgresCode(t, err, "23514")
	})

}

type modelCallPhaseFixture struct {
	workspaceID string
	workflowID  string
	workflowRun string
	indexID     string
	nextRun     int
	nextCall    int
	startedAt   time.Time
}

func seedModelCallPhaseFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *modelCallPhaseFixture {
	t.Helper()
	fixture := &modelCallPhaseFixture{
		workspaceID: "78000000-0000-4000-8000-000000000001",
		workflowID:  "78000000-0000-4000-8000-000000000002",
		workflowRun: "78000000-0000-4000-8000-000000000003",
		indexID:     "78000000-0000-4000-8000-000000000004",
		startedAt:   time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC),
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,'model-call-phases','/tmp/model-call-phases','/tmp/model-call-phases',now(),'active',1,now(),now())`, fixture.workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,'model-call-phases',1,'{"nodes":[]}',now())`, fixture.workflowID, fixture.workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
		VALUES($1,$2,$3,'running','{}',1,now(),now())`, fixture.workflowRun, fixture.workspaceID, fixture.workflowID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
		version,created_at,updated_at
	) VALUES($1,$2,'simple','v1',repeat('1',64),'{}','model-call-phases:index',repeat('2',64),0,
		'model-call-phases-index','building','["vector"]',1,now(),now())`, fixture.indexID, fixture.workspaceID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *modelCallPhaseFixture) newModelRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	fixture.nextRun++
	ordinal := fixture.nextRun
	nodeRunID := fmt.Sprintf("78100000-0000-4000-8000-%012d", ordinal)
	nodeAttemptID := fmt.Sprintf("78200000-0000-4000-8000-%012d", ordinal)
	modelRunID := fmt.Sprintf("78300000-0000-4000-8000-%012d", ordinal)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at
	) VALUES($1,$2,$3,'agent.relation-assessment','running','{}','model-call-worker',now()+interval '5 minutes',1,now(),now())`,
		nodeRunID, fixture.workflowRun, fmt.Sprintf("model-call-phases-%d", ordinal)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
	) VALUES($1,$2,1,1,0,$3,'model-call-worker',now()+interval '5 minutes','running',now())`,
		nodeAttemptID, nodeRunID, fmt.Sprintf("model-call-delivery-%d", ordinal)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent.model_run(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
		reduced_schema_id,reduced_schema_version,retrieval_index_version_id,
		status,version,started_at,updated_at
	) VALUES($1,$2,$3,$4,$5,'openai-compatible','v1','model-test','2026-08-08','default','v1',
		'relation-assessment','v1','agent.relation-assessment','v1','agent.refusal','v1',$6,
		'RUNNING',1,now(),now())`,
		modelRunID, fixture.workspaceID, fixture.workflowRun, nodeRunID, nodeAttemptID, fixture.indexID); err != nil {
		t.Fatal(err)
	}
	return modelRunID
}

func (fixture *modelCallPhaseFixture) insertSequence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, modelRunID string, phases []string) {
	t.Helper()
	for index, phase := range phases {
		callNo := index + 1
		if err := fixture.insertStartedCall(ctx, pool, modelRunID, callNo, phase); err != nil {
			t.Fatalf("insert phase=%s call_no=%d: %v", phase, callNo, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE agent.model_call SET
			status='SUCCEEDED',response_hash=repeat('b',64),response_bytes=32,input_tokens=4,output_tokens=2,
			latency_ms=5,version=2,completed_at=$2 WHERE id=$1`, fixture.currentCallID(), fixture.callTime(callNo).Add(time.Millisecond)); err != nil {
			t.Fatalf("complete phase=%s call_no=%d: %v", phase, callNo, err)
		}
	}
}

func (fixture *modelCallPhaseFixture) completeCurrentCallWithStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, status string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE agent.model_call SET
		status=$2,error_code='TEST_MODEL_FAILURE',latency_ms=5,version=2,completed_at=$3
		WHERE id=$1`, fixture.currentCallID(), status, fixture.callTime(1).Add(time.Millisecond)); err != nil {
		t.Fatalf("complete model call status=%s: %v", status, err)
	}
}

func (fixture *modelCallPhaseFixture) insertStartedCall(ctx context.Context, pool *pgxpool.Pool, modelRunID string, callNo int, phase string) error {
	fixture.nextCall++
	callID := fixture.currentCallID()
	_, err := pool.Exec(ctx, `INSERT INTO agent.model_call(
		id,model_run_id,call_no,phase,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
		request_hash,request_bytes,status,version,started_at
	) VALUES($1,$2,$3,$4,'openai-compatible','v1','model-test','2026-08-08','default','v1',
		'model-call-phase','v1','agent.relation-assessment','v1',128,
		repeat('a',64),32,'STARTED',1,$5)`, callID, modelRunID, callNo, phase, fixture.callTime(callNo))
	return err
}

func (fixture *modelCallPhaseFixture) currentCallID() string {
	return fmt.Sprintf("78400000-0000-4000-8000-%012d", fixture.nextCall)
}

func (fixture *modelCallPhaseFixture) callTime(callNo int) time.Time {
	return fixture.startedAt.Add(time.Duration(fixture.nextCall+callNo) * time.Millisecond)
}

func assertModelCallPhaseMigrationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var phaseCheck, phaseOrder, singletonIndex, guardFunction string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid='agent.model_call'::regclass AND conname='agent_model_call_phase_check'`).Scan(&phaseCheck); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid='agent.model_call'::regclass AND conname='agent_model_call_phase_order'`).Scan(&phaseOrder); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes
		WHERE schemaname='agent' AND indexname='uq_agent_model_call_generation_phase'`).Scan(&singletonIndex); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT pg_get_functiondef('agent.guard_model_call_phase_sequence()'::regprocedure)`).Scan(&guardFunction); err != nil {
		t.Fatal(err)
	}

	for _, phase := range []string{"PLAN", "AGENT", "ANSWER", "INITIAL", "REPAIR", "REDUCED", "REVIEW"} {
		if !strings.Contains(phaseCheck, phase) {
			t.Fatalf("phase check=%q missing %s", phaseCheck, phase)
		}
	}
	for _, phase := range []string{"ANSWER", "INITIAL", "REPAIR", "REDUCED"} {
		if !strings.Contains(singletonIndex, phase) {
			t.Fatalf("singleton index=%q missing %s", singletonIndex, phase)
		}
	}
	for _, repeatableOrSequenceBound := range []string{"AGENT", "PLAN", "REVIEW"} {
		if strings.Contains(singletonIndex, "'"+repeatableOrSequenceBound+"'") {
			t.Fatalf("singleton index=%q unexpectedly contains %s", singletonIndex, repeatableOrSequenceBound)
		}
	}
	if !strings.Contains(phaseOrder, "AGENT") ||
		!strings.Contains(guardFunction, "previous_phase = 'AGENT'") ||
		!strings.Contains(guardFunction, "previous_status IS DISTINCT FROM 'SUCCEEDED'") {
		t.Fatalf("expanded phase contract is incomplete: order=%q index=%q function=%q", phaseOrder, singletonIndex, guardFunction)
	}
}
