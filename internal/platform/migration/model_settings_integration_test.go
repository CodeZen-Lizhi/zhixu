//go:build integration

package migration

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestModelSettingsMigrationConstraintsAndGuardedDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 64); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 64); err != nil {
		t.Fatalf("repeat model settings migration: %v", err)
	}

	var desired, active, version int64
	var phase string
	if err := pool.QueryRow(ctx, `SELECT desired_revision,active_revision,phase,version
FROM ops.model_settings_state WHERE singleton=true`).Scan(&desired, &active, &phase, &version); err != nil {
		t.Fatal(err)
	}
	if desired != 0 || active != 0 || phase != "idle" || version != 1 {
		t.Fatalf("bootstrap state desired=%d active=%d phase=%q version=%d", desired, active, phase, version)
	}

	insertModelSettingsMigrationRevision(t, ctx, pool)
	_, err := pool.Exec(ctx, `UPDATE ops.model_settings_revisions SET chat_model='changed' WHERE revision=1`)
	assertModelSettingsPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `DELETE FROM ops.model_settings_revisions WHERE revision=1`)
	assertModelSettingsPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `TRUNCATE ops.model_settings_revisions`)
	assertModelSettingsPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `UPDATE ops.model_settings_state
SET desired_revision=999,version=version+1,updated_at=clock_timestamp() WHERE singleton=true`)
	assertModelSettingsPostgresCode(t, err, "23503")
	_, err = pool.Exec(ctx, `DELETE FROM ops.model_settings_state WHERE singleton=true`)
	assertModelSettingsPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `TRUNCATE ops.model_settings_state`)
	assertModelSettingsPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `UPDATE ops.model_settings_state
		SET active_revision=1,version=version+1,updated_at=clock_timestamp() WHERE singleton=true`)
	assertModelSettingsPostgresCode(t, err, "55000")
	for _, statement := range []string{
		`UPDATE ops.model_settings_state
		 SET desired_revision=1,version=version+1,updated_at=clock_timestamp() WHERE singleton=true`,
		`UPDATE ops.model_settings_state
		 SET rollout_id='64000000-0000-4000-8000-000000000010',target_revision=desired_revision,
		     previous_active_revision=active_revision,phase='validating',
		     lease_expires_at=clock_timestamp()+interval '1 minute',version=version+1,updated_at=clock_timestamp()
		 WHERE singleton=true`,
		`UPDATE ops.model_settings_state
		 SET phase='draining',version=version+1,updated_at=clock_timestamp() WHERE singleton=true`,
		`UPDATE ops.model_settings_state
		 SET phase='applying',version=version+1,updated_at=clock_timestamp() WHERE singleton=true`,
		`UPDATE ops.model_settings_state
		 SET phase='verifying',version=version+1,updated_at=clock_timestamp() WHERE singleton=true`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("prepare verifying model settings state: %v", err)
		}
	}
	_, err = pool.Exec(ctx, `UPDATE ops.model_settings_state
		SET rollout_id=NULL,target_revision=NULL,previous_active_revision=NULL,phase='idle',
		    lease_expires_at=NULL,last_error_code=NULL,version=version+1,updated_at=clock_timestamp()
		WHERE singleton=true`)
	assertModelSettingsPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `INSERT INTO ops.model_settings_runtime(
	role,instance_id,applied_revision,phase,applied_at,heartbeat_at)
	VALUES('api','64000000-0000-4000-8000-000000000001',999,'active',clock_timestamp(),clock_timestamp())`)
	assertModelSettingsPostgresCode(t, err, "23503")
	_, err = pool.Exec(ctx, `INSERT INTO ops.model_settings_runtime(
	role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
	VALUES('api','64000000-0000-4000-8000-000000000001',0,NULL,'prepared',clock_timestamp(),clock_timestamp())`)
	assertModelSettingsPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `INSERT INTO ops.model_settings_runtime(
	role,instance_id,applied_revision,phase,applied_at,heartbeat_at)
	VALUES('api','64000000-0000-4000-8000-000000000001',0,'active',clock_timestamp(),clock_timestamp())`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.model_settings_runtime SET applied_revision=1 WHERE role='api'`)
	assertModelSettingsPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `DELETE FROM ops.model_settings_runtime WHERE role='api'`)
	assertModelSettingsPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `TRUNCATE ops.model_settings_runtime`)
	assertModelSettingsPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `INSERT INTO ops.model_settings_revisions(
	revision,chat_provider,chat_base_url,chat_model,chat_model_version,chat_adapter_version,
	chat_timeout_microseconds,chat_max_request_bytes,chat_max_response_bytes,
	chat_secret_key_id,embedding_provider,embedding_base_url,embedding_model,embedding_dimensions,
	embedding_normalization,embedding_distance_metric,embedding_max_batch_size,
	embedding_max_input_bytes,embedding_max_batch_input_bytes,embedding_timeout_microseconds,
	embedding_max_response_bytes,created_by)
	VALUES(2,'openai-compatible','https://models.example.test/v1','chat-model','2026-07','v1',
	30000000,4194304,4194304,repeat('a',64),'ollama','http://127.0.0.1:11434','embed-model',768,
	'l2','cosine',128,65536,8388608,30000000,67108864,'migration-test')`)
	assertModelSettingsPostgresCode(t, err, "23514")

	_, err = provider.DownTo(ctx, 63)
	assertModelSettingsPostgresCode(t, err, "55000")
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.model_settings_revisions WHERE revision=1`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("guarded down preserved revisions=%d want=1", rows)
	}
}

func TestModelSettingsMigrationEmptyDownUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 64); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 63); err != nil {
		t.Fatalf("empty model settings down: %v", err)
	}
	if _, err := provider.UpTo(ctx, 64); err != nil {
		t.Fatalf("model settings up after down: %v", err)
	}
	if _, err := provider.UpTo(ctx, 66); err != nil {
		t.Fatalf("empty model settings 66 up: %v", err)
	}
	if _, err := provider.DownTo(ctx, 64); err != nil {
		t.Fatalf("empty 66 down to 64: %v", err)
	}
	if _, err := provider.UpTo(ctx, 66); err != nil {
		t.Fatalf("66 up after empty down: %v", err)
	}
	assertModelSettingsMigrationVersion(t, ctx, provider, 66)
}

func TestModelSettingsWorkflowProvenanceMigrationPathsAndNullDistinct(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 64); err != nil {
		t.Fatal(err)
	}
	insertModelSettingsWorkflowFixture(t, ctx, pool)
	insertLegacyModelSettingsWorkflowAttempt(t, ctx, pool, "65000000-0000-4000-8000-000000000004", "65000000-0000-4000-8000-000000000005")
	if _, err := provider.UpTo(ctx, 66); err != nil {
		t.Fatalf("legacy static 64 to 66: %v", err)
	}
	assertModelSettingsMigrationVersion(t, ctx, provider, 66)
	var revisionIsNull, instanceIsNull bool
	if err := pool.QueryRow(ctx, `SELECT model_settings_revision IS NULL,model_runtime_instance_id IS NULL
		FROM workflow.node_attempt WHERE id='65000000-0000-4000-8000-000000000005'`).Scan(&revisionIsNull, &instanceIsNull); err != nil {
		t.Fatal(err)
	}
	if !revisionIsNull || !instanceIsNull {
		t.Fatalf("legacy static attempt revision_null=%t instance_null=%t", revisionIsNull, instanceIsNull)
	}

	insertModelSettingsEmbeddingVersion(t, ctx, pool, "65000000-0000-4000-8000-000000000006", nil)
	_, err := pool.Exec(ctx, `INSERT INTO retrieval.embedding_version(
		id,provider,adapter_name,adapter_version,model,dimensions,normalization,distance_metric,config_hash,created_at)
		VALUES('65000000-0000-4000-8000-000000000007','migration','migration','v1','migration-model',3,'l2','cosine',repeat('6',64),now())`)
	assertModelSettingsPostgresCode(t, err, "23505")
	insertModelSettingsEmbeddingVersion(t, ctx, pool, "65000000-0000-4000-8000-000000000008", int64Ptr(0))
	var nullsNotDistinct bool
	if err := pool.QueryRow(ctx, `SELECT index_definition.indnullsnotdistinct
		FROM pg_constraint AS contract
		JOIN pg_index AS index_definition ON index_definition.indexrelid=contract.conindid
		WHERE contract.conname='uq_retrieval_embedding_version_contract'`).Scan(&nullsNotDistinct); err != nil {
		t.Fatal(err)
	}
	if !nullsNotDistinct {
		t.Fatal("embedding contract unique index does not use NULLS NOT DISTINCT")
	}
}

func TestModelSettingsWorkflowProvenanceRuntimeBindingAndGuardedDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 66); err != nil {
		t.Fatal(err)
	}
	insertModelSettingsMigrationRevision(t, ctx, pool)
	insertModelSettingsWorkflowFixture(t, ctx, pool)
	const (
		positiveInstanceID = "66000000-0000-4000-8000-000000000001"
		zeroInstanceID     = "66000000-0000-4000-8000-000000000002"
		positiveNodeID     = "66000000-0000-4000-8000-000000000003"
		positiveAttemptID  = "66000000-0000-4000-8000-000000000004"
		zeroNodeID         = "66000000-0000-4000-8000-000000000005"
		zeroAttemptID      = "66000000-0000-4000-8000-000000000006"
	)
	if _, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_runtime(
		role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
		VALUES('worker',$1::uuid,1,NULL,'active',now(),now())`, positiveInstanceID); err != nil {
		t.Fatal(err)
	}
	insertModelSettingsWorkflowAttempt(t, ctx, pool, positiveNodeID, positiveAttemptID, int64Ptr(1), stringPtr(positiveInstanceID))
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_runtime
		SET instance_id=$1::uuid,applied_revision=0,rollout_id=NULL,phase='active',applied_at=now(),heartbeat_at=now()
		WHERE role='worker'`, zeroInstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt
		SET status='succeeded',lease_owner=NULL,lease_until=NULL,ended_at=now()
		WHERE id=$1`, positiveAttemptID); err != nil {
		t.Fatalf("terminal update after worker ownership changed: %v", err)
	}
	insertModelSettingsWorkflowAttempt(t, ctx, pool, zeroNodeID, zeroAttemptID, int64Ptr(0), stringPtr(zeroInstanceID))

	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at)
		VALUES('66000000-0000-4000-8000-000000000007','65000000-0000-4000-8000-000000000003',
		'worker-mismatch','test','running','{}','worker',now()+interval '5 minutes',1,now(),now())`); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,
		model_settings_revision,model_runtime_instance_id,started_at)
		VALUES('66000000-0000-4000-8000-000000000008','66000000-0000-4000-8000-000000000007',1,1,0,
		'worker-mismatch','worker',now()+interval '5 minutes','running',0,'66000000-0000-4000-8000-000000000009',now())`)
	assertModelSettingsPostgresCode(t, err, "23514")

	_, err = provider.DownTo(ctx, 64)
	assertModelSettingsPostgresCode(t, err, "55000")
}

func insertModelSettingsWorkflowFixture(t *testing.T, ctx context.Context, queryer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}) {
	t.Helper()
	_, err := queryer.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
		VALUES('65000000-0000-4000-8000-000000000001','model-settings-migration','/tmp/model-settings-migration','/tmp/model-settings-migration',now(),'active',now(),now())`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = queryer.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES('65000000-0000-4000-8000-000000000002','65000000-0000-4000-8000-000000000001','model-settings-migration',1,'{}',now())`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = queryer.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
		VALUES('65000000-0000-4000-8000-000000000003','65000000-0000-4000-8000-000000000001','65000000-0000-4000-8000-000000000002','running','{}',1,now(),now())`)
	if err != nil {
		t.Fatal(err)
	}
}

func insertModelSettingsWorkflowAttempt(t *testing.T, ctx context.Context, queryer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, nodeID, attemptID string, revision *int64, instanceID *string) {
	t.Helper()
	_, err := queryer.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at)
		VALUES($1,'65000000-0000-4000-8000-000000000003',$2,'test','running','{}','worker',now()+interval '5 minutes',1,now(),now())`, nodeID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = queryer.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,
		model_settings_revision,model_runtime_instance_id,started_at)
		VALUES($2,$1,1,1,0,$3,'worker',now()+interval '5 minutes','running',$4,$5::uuid,now())`, nodeID, attemptID, attemptID, revision, instanceID)
	if err != nil {
		t.Fatal(err)
	}
}

func insertLegacyModelSettingsWorkflowAttempt(t *testing.T, ctx context.Context, queryer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, nodeID, attemptID string) {
	t.Helper()
	_, err := queryer.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at)
		VALUES($1,'65000000-0000-4000-8000-000000000003',$2,'test','running','{}','worker',now()+interval '5 minutes',1,now(),now())`, nodeID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = queryer.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at)
		VALUES($2,$1,1,1,0,$3,'worker',now()+interval '5 minutes','running',now())`, nodeID, attemptID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
}

func insertModelSettingsEmbeddingVersion(t *testing.T, ctx context.Context, queryer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, id string, revision *int64) {
	t.Helper()
	_, err := queryer.Exec(ctx, `INSERT INTO retrieval.embedding_version(
		id,provider,adapter_name,adapter_version,model,dimensions,normalization,distance_metric,config_hash,model_settings_revision,created_at)
		VALUES($1,'migration','migration','v1','migration-model',3,'l2','cosine',repeat('6',64),$2,now())`, id, revision)
	if err != nil {
		t.Fatal(err)
	}
}

func assertModelSettingsMigrationVersion(t *testing.T, ctx context.Context, provider interface {
	GetDBVersion(context.Context) (int64, error)
}, want int64) {
	t.Helper()
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != want {
		t.Fatalf("model settings migration version=%d want=%d", version, want)
	}
}

func int64Ptr(value int64) *int64 { return &value }

func stringPtr(value string) *string { return &value }

func insertModelSettingsMigrationRevision(t *testing.T, ctx context.Context, queryer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}) {
	t.Helper()
	_, err := queryer.Exec(ctx, `INSERT INTO ops.model_settings_revisions(
revision,chat_provider,chat_base_url,chat_model,chat_model_version,chat_adapter_version,
chat_timeout_microseconds,chat_max_request_bytes,chat_max_response_bytes,
embedding_provider,embedding_base_url,embedding_model,embedding_dimensions,
embedding_normalization,embedding_distance_metric,embedding_max_batch_size,
embedding_max_input_bytes,embedding_max_batch_input_bytes,embedding_timeout_microseconds,
embedding_max_response_bytes,created_by)
VALUES(1,'openai-compatible','https://models.example.test/v1','chat-model','2026-07','v1',
30000000,4194304,4194304,'ollama','http://127.0.0.1:11434','embed-model',768,
'l2','cosine',128,65536,8388608,30000000,67108864,'migration-test')`)
	if err != nil {
		t.Fatal(err)
	}
}

func assertModelSettingsPostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if err == nil || !errors.As(err, &postgresError) || postgresError.Code != code {
		t.Fatalf("postgres error=%v want code=%s", err, code)
	}
}
