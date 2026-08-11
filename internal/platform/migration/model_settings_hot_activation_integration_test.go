//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
)

func TestModelSettingsHotActivationMigrationCompatibleUpgradeAndDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if _, err := provider.UpTo(ctx, 78); err != nil {
		t.Fatalf("prepare migrations through 00078: %v", err)
	}
	insertModelSettingsMigrationRevision(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET desired_revision=1,version=version+1,updated_at=clock_timestamp() WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	rolloutID := "79000000-0000-4000-8000-000000000001"
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET rollout_id=$1::uuid,target_revision=1,previous_active_revision=0,phase='validating',
    lease_expires_at=clock_timestamp()+interval '1 minute',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`, rolloutID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET phase='failed',lease_expires_at=NULL,last_error_code='MODEL_SETTINGS_TEST_FAILURE',
    version=version+1,updated_at=clock_timestamp() WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 79); err != nil {
		t.Fatalf("00079 compatible failed-state upgrade: %v", err)
	}
	if _, err := provider.UpTo(ctx, 79); err != nil {
		t.Fatalf("00079 repeat Up: %v", err)
	}
	var phase string
	var desired, active, target, previous, version int64
	var preservedRollout string
	if err := pool.QueryRow(ctx, `SELECT phase,desired_revision,active_revision,target_revision,
previous_active_revision,rollout_id::text,version FROM ops.model_settings_state WHERE singleton=true`).
		Scan(&phase, &desired, &active, &target, &previous, &preservedRollout, &version); err != nil {
		t.Fatal(err)
	}
	if phase != "failed" || desired != 1 || active != 0 || target != 1 || previous != 0 || preservedRollout != rolloutID || version != 4 {
		t.Fatalf("preserved state phase=%q desired=%d active=%d target=%d previous=%d rollout=%q version=%d",
			phase, desired, active, target, previous, preservedRollout, version)
	}
	if _, err := provider.DownTo(ctx, 78); err != nil {
		t.Fatalf("00079 compatible Down: %v", err)
	}
	if _, err := provider.UpTo(ctx, 79); err != nil {
		t.Fatalf("00079 Up after Down: %v", err)
	}
}

func TestModelSettingsHotActivationMigrationRejectsLegacyLiveState(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if _, err := provider.UpTo(ctx, 78); err != nil {
		t.Fatalf("prepare migrations through 00078: %v", err)
	}
	insertModelSettingsMigrationRevision(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET desired_revision=1,version=version+1,updated_at=clock_timestamp() WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET rollout_id='79000000-0000-4000-8000-000000000002'::uuid,target_revision=1,
    previous_active_revision=0,phase='validating',lease_expires_at=clock_timestamp()+interval '1 minute',
    version=version+1,updated_at=clock_timestamp() WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	_, err := provider.UpTo(ctx, 79)
	if err == nil || !strings.Contains(err.Error(), "legacy model settings rollout") {
		t.Fatalf("00079 legacy live error=%v", err)
	}
	assertModelSettingsPostgresCode(t, err, "55000")
	assertModelSettingsMigrationVersion(t, ctx, provider, 78)
}

func TestModelSettingsHotActivationMigrationCommitEdgeAndGuardedDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 79); err != nil {
		t.Fatalf("migrate through 00079: %v", err)
	}
	insertModelSettingsMigrationRevision(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET desired_revision=1,version=version+1,updated_at=clock_timestamp() WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ role, instance string }{
		{"api", "79000000-0000-4000-8000-000000000101"},
		{"worker", "79000000-0000-4000-8000-000000000102"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_runtime(
role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
VALUES($1,$2::uuid,0,NULL,'active',clock_timestamp(),clock_timestamp())`, fixture.role, fixture.instance); err != nil {
			t.Fatal(err)
		}
	}
	rolloutID := "79000000-0000-4000-8000-000000000003"
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET rollout_id=$1::uuid,target_revision=1,previous_active_revision=0,phase='preparing',
    lease_expires_at=clock_timestamp()+interval '1 minute',last_error_code=NULL,
    version=version+1,updated_at=clock_timestamp() WHERE singleton=true`, rolloutID); err != nil {
		t.Fatal(err)
	}
	var gateOwnerKind, gateOwnerID string
	if err := pool.QueryRow(ctx, `SELECT owner_kind,owner_id::text FROM ops.runtime_mutation_gate WHERE singleton=true`).
		Scan(&gateOwnerKind, &gateOwnerID); err != nil {
		t.Fatal(err)
	}
	if gateOwnerKind != "model_settings" || gateOwnerID != rolloutID {
		t.Fatalf("preparing mutation gate=%q/%q", gateOwnerKind, gateOwnerID)
	}
	insertModelSettingsWorkflowFixture(t, ctx, pool)
	workerInstance := "79000000-0000-4000-8000-000000000102"
	insertModelSettingsWorkflowAttempt(t, ctx, pool,
		"79000000-0000-4000-8000-000000000201", "79000000-0000-4000-8000-000000000202",
		int64Ptr(0), &workerInstance)
	for _, fixture := range []struct{ role, instance string }{
		{"api", "79000000-0000-4000-8000-000000000101"},
		{"worker", "79000000-0000-4000-8000-000000000102"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_rollout_participant(
rollout_id,role,instance_id,target_revision,phase,heartbeat_at,version)
VALUES($1::uuid,$2,$3::uuid,1,'preparing',clock_timestamp(),1)`, rolloutID, fixture.role, fixture.instance); err != nil {
			t.Fatal(err)
		}
		_, err := pool.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET heartbeat_at=clock_timestamp(),version=version+2
WHERE rollout_id=$1::uuid AND role=$2`, rolloutID, fixture.role)
		assertModelSettingsPostgresCode(t, err, "55000")
		if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET phase='prepared',prepared_at=clock_timestamp(),heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role=$2`, rolloutID, fixture.role); err != nil {
			t.Fatal(err)
		}
	}
	_, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET phase='failed',lease_expires_at=NULL,last_error_code='MODEL_SETTINGS_TEST_FAILURE',
    version=version+1,updated_at=clock_timestamp() WHERE singleton=true`)
	assertModelSettingsPostgresCode(t, err, "55000")
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET phase='arming',lease_expires_at=clock_timestamp()+interval '1 minute',
    version=version+1,updated_at=clock_timestamp() WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(
id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at)
VALUES('79000000-0000-4000-8000-000000000203','65000000-0000-4000-8000-000000000003',
'hot-activation-fenced','test','running','{}','worker',now()+interval '5 minutes',1,now(),now())`); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,
model_settings_revision,model_runtime_instance_id,started_at)
VALUES('79000000-0000-4000-8000-000000000204','79000000-0000-4000-8000-000000000203',
1,1,0,'hot-activation-fenced','worker',now()+interval '5 minutes','running',0,$1::uuid,now())`, workerInstance)
	assertModelSettingsPostgresCode(t, err, "55P03")
	for _, role := range []string{"api", "worker"} {
		if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET phase='armed',heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role=$2`, rolloutID, role); err != nil {
			t.Fatal(err)
		}
	}
	_, err = pool.Exec(ctx, `UPDATE ops.model_settings_state
SET active_revision=1,version=version+1,updated_at=clock_timestamp() WHERE singleton=true`)
	assertModelSettingsPostgresCode(t, err, "55000")
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET active_revision=target_revision,phase='activating',lease_expires_at=clock_timestamp()+interval '1 minute',
    version=version+1,updated_at=clock_timestamp() WHERE singleton=true`); err != nil {
		t.Fatalf("legal arming to activating edge: %v", err)
	}
	var active int64
	if err := pool.QueryRow(ctx, `SELECT active_revision FROM ops.model_settings_state WHERE singleton=true`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active revision=%d want=1", active)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_runtime
SET applied_revision=1,phase='active',applied_at=clock_timestamp(),heartbeat_at=clock_timestamp()
WHERE role='api'`); err != nil {
		t.Fatalf("legal participant-backed runtime activation: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_runtime
SET phase='unavailable',heartbeat_at=clock_timestamp() WHERE role='api'`); err != nil {
		t.Fatalf("legal runtime degradation: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_runtime
SET phase='active',heartbeat_at=clock_timestamp() WHERE role='api'`); err != nil {
		t.Fatalf("legal same-revision runtime availability restore: %v", err)
	}
	_, err = provider.DownTo(ctx, 78)
	if err == nil || !strings.Contains(err.Error(), "hot activation history") {
		t.Fatalf("00079 guarded Down error=%v", err)
	}
	assertModelSettingsPostgresCode(t, err, "55000")
	assertModelSettingsMigrationVersion(t, ctx, provider, 79)
}

func TestModelSettingsHotActivationMigrationRejectsRawCommitWithoutReadyRoles(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 79); err != nil {
		t.Fatalf("migrate through 00079: %v", err)
	}
	insertModelSettingsMigrationRevision(t, ctx, pool)
	for _, fixture := range []struct{ role, instance string }{
		{"api", "79000000-0000-4000-8000-000000000311"},
		{"worker", "79000000-0000-4000-8000-000000000312"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_runtime(
role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
VALUES($1,$2::uuid,0,NULL,'active',clock_timestamp(),clock_timestamp())`, fixture.role, fixture.instance); err != nil {
			t.Fatal(err)
		}
	}
	rolloutID := "79000000-0000-4000-8000-000000000313"
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET desired_revision=1,rollout_id=$1::uuid,target_revision=1,previous_active_revision=0,phase='preparing',
    lease_expires_at=clock_timestamp()+interval '1 minute',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`, rolloutID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET phase='arming',version=version+1,updated_at=clock_timestamp() WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET active_revision=target_revision,phase='activating',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`)
	assertModelSettingsPostgresCode(t, err, "55000")

	for _, fixture := range []struct{ role, instance string }{
		{"api", "79000000-0000-4000-8000-000000000311"},
		{"worker", "79000000-0000-4000-8000-000000000312"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_rollout_participant(
rollout_id,role,instance_id,target_revision,phase,heartbeat_at,version)
VALUES($1::uuid,$2,$3::uuid,1,'preparing',clock_timestamp(),1)`, rolloutID, fixture.role, fixture.instance); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET phase='prepared',prepared_at=clock_timestamp(),heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role=$2`, rolloutID, fixture.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET phase='armed',heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role='api'`, rolloutID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.model_settings_state
SET active_revision=target_revision,phase='activating',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`)
	assertModelSettingsPostgresCode(t, err, "55000")
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET phase='armed',heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role='worker'`, rolloutID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_runtime
SET heartbeat_at=clock_timestamp()+interval '1 second' WHERE role='api'`); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.model_settings_state
SET active_revision=target_revision,phase='activating',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`)
	assertModelSettingsPostgresCode(t, err, "55000")
}

func TestModelSettingsHotActivationMigrationRejectsRawCommitOwnerMismatch(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 79); err != nil {
		t.Fatalf("migrate through 00079: %v", err)
	}
	insertModelSettingsMigrationRevision(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_runtime(
role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
VALUES('api','79000000-0000-4000-8000-000000000321'::uuid,0,NULL,'active',clock_timestamp(),clock_timestamp()),
      ('worker','79000000-0000-4000-8000-000000000322'::uuid,0,NULL,'active',
       clock_timestamp()-interval '21 seconds',clock_timestamp()-interval '21 seconds')`); err != nil {
		t.Fatal(err)
	}
	rolloutID := "79000000-0000-4000-8000-000000000323"
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET desired_revision=1,rollout_id=$1::uuid,target_revision=1,previous_active_revision=0,phase='preparing',
    lease_expires_at=clock_timestamp()+interval '1 minute',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`, rolloutID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET phase='arming',version=version+1,updated_at=clock_timestamp() WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ role, instance string }{
		{"api", "79000000-0000-4000-8000-000000000321"},
		{"worker", "79000000-0000-4000-8000-000000000322"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_rollout_participant(
rollout_id,role,instance_id,target_revision,phase,heartbeat_at,version)
VALUES($1::uuid,$2,$3::uuid,1,'preparing',clock_timestamp(),1)`, rolloutID, fixture.role, fixture.instance); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET phase='prepared',prepared_at=clock_timestamp(),heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role=$2`, rolloutID, fixture.role); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET phase='armed',heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role=$2`, rolloutID, fixture.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_runtime
SET instance_id='79000000-0000-4000-8000-000000000324'::uuid,applied_at=clock_timestamp(),heartbeat_at=clock_timestamp()
WHERE role='worker'`); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET active_revision=target_revision,phase='activating',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`)
	assertModelSettingsPostgresCode(t, err, "55000")
}

func TestModelSettingsHotActivationMigrationRejectsRawRuntimeOwnershipBypass(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 79); err != nil {
		t.Fatalf("migrate through 00079: %v", err)
	}
	insertModelSettingsMigrationRevision(t, ctx, pool)
	_, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_runtime(
role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
VALUES('api','79000000-0000-4000-8000-000000000331'::uuid,1,NULL,'active',clock_timestamp(),clock_timestamp())`)
	assertModelSettingsPostgresCode(t, err, "55000")
	if _, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_runtime(
role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
VALUES('api','79000000-0000-4000-8000-000000000331'::uuid,0,NULL,'active',clock_timestamp(),clock_timestamp()),
      ('worker','79000000-0000-4000-8000-000000000332'::uuid,0,NULL,'active',
       clock_timestamp()-interval '21 seconds',clock_timestamp()-interval '21 seconds')`); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.model_settings_runtime
SET instance_id='79000000-0000-4000-8000-000000000333'::uuid,applied_at=clock_timestamp(),heartbeat_at=clock_timestamp()
WHERE role='api'`)
	assertModelSettingsPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `UPDATE ops.model_settings_runtime
SET instance_id='79000000-0000-4000-8000-000000000334'::uuid,applied_revision=1,
    applied_at=clock_timestamp(),heartbeat_at=clock_timestamp()
WHERE role='worker'`)
	assertModelSettingsPostgresCode(t, err, "55000")
}

func TestModelSettingsHotActivationMigrationRejectsRawParticipantOwnershipBypass(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 79); err != nil {
		t.Fatalf("migrate through 00079: %v", err)
	}
	insertModelSettingsMigrationRevision(t, ctx, pool)
	staleAgeMicros := (application.DefaultRuntimeFreshWithin + time.Second).Microseconds()
	for _, fixture := range []struct {
		role        string
		oldInstance string
	}{
		{"api", "79000000-0000-4000-8000-000000000341"},
		{"worker", "79000000-0000-4000-8000-000000000342"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_runtime(
role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
VALUES($1,$2::uuid,0,NULL,'active',
       clock_timestamp()-($3::bigint*interval '1 microsecond'),
       clock_timestamp()-($3::bigint*interval '1 microsecond'))`,
			fixture.role, fixture.oldInstance, staleAgeMicros); err != nil {
			t.Fatal(err)
		}
	}
	rolloutID := "79000000-0000-4000-8000-000000000343"
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET desired_revision=1,rollout_id=$1::uuid,target_revision=1,previous_active_revision=0,phase='preparing',
    lease_expires_at=clock_timestamp()+interval '1 minute',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`, rolloutID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_rollout_participant(
rollout_id,role,instance_id,target_revision,phase,heartbeat_at,version)
VALUES($1::uuid,'api','79000000-0000-4000-8000-000000000341'::uuid,1,'preparing',clock_timestamp(),1),
      ($1::uuid,'worker','79000000-0000-4000-8000-000000000342'::uuid,1,'preparing',
       clock_timestamp()-($2::bigint*interval '1 microsecond'),1)`, rolloutID, staleAgeMicros); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		role        string
		newInstance string
	}{
		{"api", "79000000-0000-4000-8000-000000000344"},
		{"worker", "79000000-0000-4000-8000-000000000345"},
	} {
		if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_runtime
SET instance_id=$1::uuid,applied_at=clock_timestamp(),heartbeat_at=clock_timestamp()
WHERE role=$2`, fixture.newInstance, fixture.role); err != nil {
			t.Fatalf("take over stale %s runtime: %v", fixture.role, err)
		}
	}

	_, err := pool.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET instance_id='79000000-0000-4000-8000-000000000344'::uuid,
    heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role='api'`, rolloutID)
	assertModelSettingsPostgresCode(t, err, "55000")

	_, err = pool.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET instance_id='79000000-0000-4000-8000-000000000345'::uuid,
    heartbeat_at=clock_timestamp()+interval '1 second',version=version+1
WHERE rollout_id=$1::uuid AND role='worker'`, rolloutID)
	assertModelSettingsPostgresCode(t, err, "55000")

	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET instance_id='79000000-0000-4000-8000-000000000345'::uuid,
    heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role='worker'`, rolloutID); err != nil {
		t.Fatalf("take over stale worker participant: %v", err)
	}
}
