//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCaptureProfileMigrationSupportsEmptyDownUpAndGuardsFacts(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 68); err != nil {
		t.Fatalf("migrate through 00068: %v", err)
	}
	assertCaptureProfileResultTypeConstraint(t, ctx, pool, true)
	assertSourceSpanDerivedEvidenceColumns(t, ctx, pool, true)
	for _, table := range []string{
		"core.capture", "core.capture_command", "ops.capture_outbox", "ops.capture_attempt",
		"learning.document_knowledge_profile", "learning.document_knowledge_profile_revision",
		"learning.document_knowledge_profile_attempt", "learning.document_knowledge_profile_evidence",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s exists=%v error=%v", table, exists, err)
		}
	}
	if _, err := provider.DownTo(ctx, 67); err != nil {
		t.Fatalf("empty 00068 down: %v", err)
	}
	assertCaptureProfileResultTypeConstraint(t, ctx, pool, false)
	assertSourceSpanDerivedEvidenceColumns(t, ctx, pool, false)
	if _, err := provider.UpTo(ctx, 68); err != nil {
		t.Fatalf("00068 re-up: %v", err)
	}
	assertCaptureProfileResultTypeConstraint(t, ctx, pool, true)

	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	const (
		workspaceID = "68000000-0000-4000-8000-000000000001"
		sourceID    = "68000000-0000-4000-8000-000000000002"
		captureID   = "68000000-0000-4000-8000-000000000003"
	)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
status,availability,availability_reason,availability_checked_at,version,created_at,updated_at)
VALUES($1,'capture-test','/tmp/capture-test',$2,1,'/tmp/capture-test',$3,
'inactive','available',NULL,$3,1,$3,$3)`, workspaceID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
VALUES($1,$2,'quick_capture_url','example.com',$3,$4)`, sourceID, workspaceID, "captures/"+captureID+"/input", now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.capture(
id,workspace_id,kind,display_name,original_location,original_url,source_id,status,
fetch_status,ingestion_status,index_status,profile_status,version,captured_at,updated_at)
VALUES($1,$2,'URL','example.com',$3,'https://example.com',$4,'RECEIVED',
'PENDING','PENDING','PENDING','PENDING',1,$5,$5)`, captureID, workspaceID, "captures/"+captureID+"/input", sourceID, now); err != nil {
		t.Fatal(err)
	}
	_, err := provider.DownTo(ctx, 67)
	assertPostgresCode(t, err, "55000")
}

func TestCaptureProfileCompositeSourceVersionBindingsRejectCrossWorkspaceWrites(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	if _, err := migrationProvider(t, pool).UpTo(ctx, 68); err != nil {
		t.Fatal(err)
	}

	for constraint, fragment := range map[string]string{
		"fk_capture_attempt_source_version":           "FOREIGN KEY (source_version_id, workspace_id) REFERENCES core.source_version(id, workspace_id)",
		"fk_document_profile_source_version":          "FOREIGN KEY (source_version_id, workspace_id) REFERENCES core.source_version(id, workspace_id)",
		"fk_document_profile_revision_source_version": "FOREIGN KEY (source_version_id, workspace_id) REFERENCES core.source_version(id, workspace_id)",
	} {
		var definition string
		if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname=$1`, constraint).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(definition, fragment) {
			t.Fatalf("constraint %s=%q does not contain %q", constraint, definition, fragment)
		}
	}

	now := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)
	const (
		workspaceA = "68100000-0000-4000-8000-000000000001"
		workspaceB = "68100000-0000-4000-8000-000000000002"
		artifactA  = "68100000-0000-4000-8000-000000000003"
		artifactB  = "68100000-0000-4000-8000-000000000004"
		sourceA    = "68100000-0000-4000-8000-000000000005"
		sourceB    = "68100000-0000-4000-8000-000000000006"
		versionA   = "68100000-0000-4000-8000-000000000007"
		versionB   = "68100000-0000-4000-8000-000000000008"
		captureA   = "68100000-0000-4000-8000-000000000009"
		definition = "68100000-0000-4000-8000-000000000010"
		workflow   = "68100000-0000-4000-8000-000000000011"
	)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.workspace(
			id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
			status,availability,availability_reason,availability_checked_at,version,created_at,updated_at)
		VALUES($1,'capture-fk-a','/tmp/capture-fk-a',repeat('1',64),1,'/tmp/capture-fk-a',$2,
			'inactive','available',NULL,$2,1,$2,$2)`, []any{workspaceA, now}},
		{`INSERT INTO core.workspace(
			id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
			status,availability,availability_reason,availability_checked_at,version,created_at,updated_at)
		VALUES($1,'capture-fk-b','/tmp/capture-fk-b',repeat('2',64),1,'/tmp/capture-fk-b',$2,
			'inactive','available',NULL,$2,1,$2,$2)`, []any{workspaceB, now}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
			VALUES($1,$2,repeat('a',64),1,'.knowledge/sources/'||repeat('a',64),$3)`, []any{artifactA, workspaceA, now}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
			VALUES($1,$2,repeat('b',64),1,'.knowledge/sources/'||repeat('b',64),$3)`, []any{artifactB, workspaceB, now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
			VALUES($1,$2,'quick_capture_text','A','captures/a/input',$3)`, []any{sourceA, workspaceA, now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
			VALUES($1,$2,'quick_capture_text','B','captures/b/input',$3)`, []any{sourceB, workspaceB, now}},
		{`INSERT INTO core.source_version(
			id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,
			original_content_location,security_status,captured_at
		) VALUES($1,$2,$3,$4,repeat('a',64),1,'text/plain','capture://a','pending',$5)`, []any{versionA, sourceA, workspaceA, artifactA, now}},
		{`INSERT INTO core.source_version(
			id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,
			original_content_location,security_status,captured_at
		) VALUES($1,$2,$3,$4,repeat('b',64),1,'text/plain','capture://b','pending',$5)`, []any{versionB, sourceB, workspaceB, artifactB, now}},
		{`INSERT INTO core.capture(
			id,workspace_id,kind,display_name,original_location,original_input_hash,source_id,latest_source_version_id,
			status,fetch_status,ingestion_status,index_status,profile_status,version,captured_at,updated_at
		) VALUES($1,$2,'TEXT','A','captures/a/input',repeat('a',64),$3,$4,'SOURCE_SAVED','NOT_APPLICABLE',
			'PENDING','PENDING','PENDING',1,$5,$5)`, []any{captureA, workspaceA, sourceA, versionA, now}},
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
			VALUES($1,$2,'capture-fk',1,'{"nodes":[]}',$3)`, []any{definition, workspaceA, now}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
			VALUES($1,$2,$3,'running','{}',1,$4,$4)`, []any{workflow, workspaceA, definition, now}},
	} {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	_, err := pool.Exec(ctx, `INSERT INTO learning.document_knowledge_profile(
		id,workspace_id,capture_id,source_version_id,status,error_code,retryable,version,created_at,updated_at
	) VALUES('68100000-0000-4000-8000-000000000012',$1,$2,$3,'PENDING','',false,1,$4,$4)`,
		workspaceA, captureA, versionB, now)
	assertPostgresCode(t, err, "23503")
	_, err = pool.Exec(ctx, `INSERT INTO ops.capture_attempt(
		id,workspace_id,capture_id,source_version_id,workflow_run_id,attempt_number,stage,status,
		error_code,retryable,started_at,version
	) VALUES('68100000-0000-4000-8000-000000000013',$1,$2,$3,$4,1,'INGESTION','RUNNING','',false,$5,1)`,
		workspaceA, captureA, versionB, workflow, now)
	assertPostgresCode(t, err, "23503")
}

func TestCaptureProfileDownGuardsOnlyProfileModelRunFacts(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 68); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)
	const (
		workspaceID  = "68200000-0000-4000-8000-000000000001"
		definitionID = "68200000-0000-4000-8000-000000000002"
		workflowID   = "68200000-0000-4000-8000-000000000003"
		nodeID       = "68200000-0000-4000-8000-000000000004"
		nodeAttempt  = "68200000-0000-4000-8000-000000000005"
		indexID      = "68200000-0000-4000-8000-000000000006"
		modelRunID   = "68200000-0000-4000-8000-000000000007"
		modelCallID  = "68200000-0000-4000-8000-000000000008"
	)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.workspace(
			id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
			status,availability,availability_reason,availability_checked_at,version,created_at,updated_at)
		VALUES($1,'capture-down-model','/tmp/capture-down-model',repeat('3',64),1,'/tmp/capture-down-model',$2,
			'inactive','available',NULL,$2,1,$2,$2)`, []any{workspaceID, now}},
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
			VALUES($1,$2,'capture-down-model',1,'{"nodes":[]}',$3)`, []any{definitionID, workspaceID, now}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
			VALUES($1,$2,$3,'running','{}',1,$4,$4)`, []any{workflowID, workspaceID, definitionID, now}},
		{`INSERT INTO workflow.node_run(
			id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at
		) VALUES($1,$2,'capture.profile','capture.profile','running','{}','capture-down',$3,1,$4,$4)`,
			[]any{nodeID, workflowID, now.Add(time.Minute), now}},
		{`INSERT INTO workflow.node_attempt(
			id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
		) VALUES($1,$2,1,1,0,'capture-down-delivery','capture-down',$3,'running',$4)`,
			[]any{nodeAttempt, nodeID, now.Add(time.Minute), now}},
		{`INSERT INTO retrieval.index_version(
			id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
			source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
			version,created_at,updated_at
		) VALUES($1,$2,'simple','v1',repeat('4',64),'{}','capture-down:index',repeat('5',64),0,
			'capture-down-index','building','["vector"]',1,$3,$3)`, []any{indexID, workspaceID, now}},
		{`INSERT INTO agent.model_run(
			id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,adapter_name,adapter_version,
			model_id,model_version,profile_id,profile_version,prompt_template_id,prompt_template_version,
			output_schema_id,output_schema_version,reduced_schema_id,reduced_schema_version,
			retrieval_index_version_id,status,version,started_at,updated_at
		) VALUES($1,$2,$3,$4,$5,'openai-compatible','v1','capture-profile','v1','capture.profile','v1',
			'capture.profile','v1','document-knowledge-profile','v1','document-knowledge-profile-reduced','v1',
			$6,'RUNNING',1,$7,$7)`, []any{modelRunID, workspaceID, workflowID, nodeID, nodeAttempt, indexID, now}},
		{`INSERT INTO agent.model_call(
			id,model_run_id,call_no,phase,adapter_name,adapter_version,model_id,model_version,profile_id,
			profile_version,prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
			max_output_tokens,request_hash,request_bytes,status,version,started_at
		) VALUES($1,$2,1,'INITIAL','openai-compatible','v1','capture-profile','v1','capture.profile','v1',
			'capture.profile','v1','document-knowledge-profile','v1',128,repeat('6',64),128,'STARTED',1,$3)`,
			[]any{modelCallID, modelRunID, now}},
		{`UPDATE agent.model_call SET status='SUCCEEDED',response_hash=repeat('7',64),response_bytes=64,
			input_tokens=10,output_tokens=5,latency_ms=20,version=2,completed_at=$2 WHERE id=$1`,
			[]any{modelCallID, now.Add(time.Second)}},
		{`UPDATE agent.model_run SET status='SUCCEEDED',final_result_type='document_knowledge_profile',
			version=2,updated_at=$2,completed_at=$2 WHERE id=$1`, []any{modelRunID, now.Add(2 * time.Second)}},
	} {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	var captureFacts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.capture`).Scan(&captureFacts); err != nil || captureFacts != 0 {
		t.Fatalf("capture facts=%d error=%v", captureFacts, err)
	}
	_, err := provider.DownTo(ctx, 67)
	assertPostgresCode(t, err, "55000")
}

func assertSourceSpanDerivedEvidenceColumns(t *testing.T, ctx context.Context, pool *pgxpool.Pool, expected bool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='ingestion' AND table_name='source_span'
		  AND column_name IN ('evidence_kind','derived_excerpt')`).Scan(&count); err != nil {
		t.Fatalf("read source span derived evidence columns: %v", err)
	}
	if got := count == 2; got != expected {
		t.Fatalf("derived evidence columns present=%v want=%v (count=%d)", got, expected, count)
	}
}

func assertCaptureProfileResultTypeConstraint(t *testing.T, ctx context.Context, pool *pgxpool.Pool, allowProfile bool) {
	t.Helper()
	var definition string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid='agent.model_run'::regclass AND conname='agent_model_run_final_result_type_check'`).Scan(&definition); err != nil {
		t.Fatalf("read agent model result constraint: %v", err)
	}
	if !strings.Contains(definition, "artifact_section") {
		t.Fatalf("constraint lost artifact_section: %s", definition)
	}
	if got := strings.Contains(definition, "document_knowledge_profile"); got != allowProfile {
		t.Fatalf("constraint profile type allowed=%v want=%v: %s", got, allowProfile, definition)
	}
}
