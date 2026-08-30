//go:build integration

package migration

import (
	"context"
	"testing"
)

func TestAgentRuntimeMigrationStateMachineWorkspace(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var tables, triggers, meta int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='agent' AND table_name IN ('model_run','model_call')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
		WHERE tgname IN ('agent_model_run_guard_mutation','agent_model_call_guard_mutation')`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='agent_runtime' AND value='m6-02'`).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if tables != 2 || triggers != 2 || meta != 1 {
		t.Fatalf("tables=%d triggers=%d meta=%d", tables, triggers, meta)
	}

	const (
		workspaceID = "a8100000-0000-4000-8000-000000000001"
		otherSpace  = "a8100000-0000-4000-8000-000000000002"
		definition  = "a8200000-0000-4000-8000-000000000001"
		runID       = "a8300000-0000-4000-8000-000000000001"
		nodeID      = "a8400000-0000-4000-8000-000000000001"
		attemptID   = "a8500000-0000-4000-8000-000000000001"
		indexID     = "a8600000-0000-4000-8000-000000000001"
		modelRunID  = "a8700000-0000-4000-8000-000000000001"
		modelCallID = "a8800000-0000-4000-8000-000000000001"
	)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at) VALUES
		($1,'agent-runtime','/tmp/agent-runtime','/tmp/agent-runtime',now(),'active',now(),now()),
		($2,'agent-runtime-other','/tmp/agent-runtime-other','/tmp/agent-runtime-other',now(),'inactive',now(),now())`, workspaceID, otherSpace); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,'agent-runtime',1,'{"nodes":[]}',now())`, definition, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
		VALUES($1,$2,$3,'running','{}',1,now(),now())`, runID, workspaceID, definition); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at)
		VALUES($1,$2,'agent','agent.relation-assessment','running','{}','worker-agent',now()+interval '5 minutes',1,now(),now())`, nodeID, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
	) VALUES($1,$2,1,1,0,'agent-delivery','worker-agent',now()+interval '5 minutes','running',now())`, attemptID, nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
		version,created_at,updated_at
	) VALUES($1,$2,'simple','v1',repeat('1',64),'{}','agent-runtime:index',repeat('2',64),0,
		'agent-runtime-index','building','["vector"]',1,now(),now())`, indexID, workspaceID); err != nil {
		t.Fatal(err)
	}

	insertRunSQL := `INSERT INTO agent.model_run(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
		reduced_schema_id,reduced_schema_version,
		retrieval_index_version_id,status,version,started_at,updated_at
	) VALUES($1,$2,$3,$4,$5,'openai-compatible','v1','model-test','2026-07-01',
		'default','v1','rag-answer','v1','agent.rag-answer','v1','agent.refusal','v1',$6,'RUNNING',1,now(),now())`
	if _, err := pool.Exec(ctx, insertRunSQL, modelRunID, workspaceID, runID, nodeID, attemptID, indexID); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, insertRunSQL, "a8700000-0000-4000-8000-000000000002", workspaceID, runID, nodeID, attemptID, indexID)
	assertPostgresCode(t, err, "23505")
	_, err = pool.Exec(ctx, insertRunSQL, "a8700000-0000-4000-8000-000000000003", otherSpace, runID, nodeID, "a8500000-0000-4000-8000-000000000003", indexID)
	assertPostgresCode(t, err, "23503")

	if _, err := pool.Exec(ctx, `INSERT INTO agent.model_call(
		id,model_run_id,call_no,phase,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
		request_hash,request_bytes,status,version,started_at
	) VALUES($1,$2,1,'INITIAL','openai-compatible','v1','model-test','2026-07-01','default','v1',
		'rag-answer','v1','agent.rag-answer','v1',128,repeat('a',64),128,'STARTED',1,now())`, modelCallID, modelRunID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO agent.model_call(
		id,model_run_id,call_no,phase,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
		request_hash,request_bytes,status,version,started_at
	) VALUES('a8800000-0000-4000-8000-000000000002',$1,3,'REPAIR',
		'openai-compatible','v1','model-test','2026-07-01','default','v1','rag-answer','v1',
		'agent.rag-answer','v1',128,repeat('b',64),64,'STARTED',1,now())`, modelRunID)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `UPDATE agent.model_call SET
		status='SUCCEEDED',prompt_template_version='v2',response_hash=repeat('c',64),response_bytes=64,
		input_tokens=10,output_tokens=5,latency_ms=20,version=2,completed_at=now() WHERE id=$1`, modelCallID)
	assertPostgresCode(t, err, "55000")
	if _, err := pool.Exec(ctx, `UPDATE agent.model_call SET
		status='SUCCEEDED',response_hash=repeat('c',64),response_bytes=64,input_tokens=10,output_tokens=5,
		latency_ms=20,version=2,completed_at=now() WHERE id=$1`, modelCallID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE agent.model_call SET latency_ms=21,version=3 WHERE id=$1`, modelCallID)
	assertPostgresCode(t, err, "55000")

	_, err = pool.Exec(ctx, `UPDATE agent.model_run SET
		status='SUCCEEDED',final_result_type='rag_answer',reduced_schema_id='agent.other-reduced',
		version=2,updated_at=now(),completed_at=now() WHERE id=$1`, modelRunID)
	assertPostgresCode(t, err, "55000")
	if _, err := pool.Exec(ctx, `UPDATE agent.model_run SET
		status='SUCCEEDED',final_result_type='rag_answer',version=2,updated_at=now(),completed_at=now()
		WHERE id=$1`, modelRunID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE agent.model_run SET final_result_type='refusal',version=3 WHERE id=$1`, modelRunID)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `DELETE FROM agent.model_run WHERE id=$1`, modelRunID)
	assertPostgresCode(t, err, "55000")
}
