//go:build integration

package migration_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	localmodelruntime "github.com/CodeZen-Lizhi/zhixu/internal/localmodelruntime"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	gormlogger "gorm.io/gorm/logger"
)

type managedOllamaIntegrationStore interface {
	localmodelruntime.LifecycleStore
	localmodelruntime.TestPreparationStore
	CompleteTestPreparation(context.Context, foundation.ID, string, bool) (localmodelruntime.OperationRecord, error)
}

type managedOllamaIntegrationVariant struct {
	name string
	open func(*testing.T, *platformpostgres.Pool) managedOllamaIntegrationStore
}

func managedOllamaTestPlatform(t *testing.T) *platformpostgres.Pool {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{
		ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
		Availability:     testdb.FailWhenUnavailable,
		MaxConns:         16,
	})
	platform := fixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("managed Ollama fixture did not provide a platform pool")
	}
	return platform
}

func runManagedOllamaIntegrationVariants(
	t *testing.T,
	scenario func(*testing.T, context.Context, *pgxpool.Pool, managedOllamaIntegrationStore),
) {
	t.Helper()
	variants := []managedOllamaIntegrationVariant{
		{name: "legacy-pgx", open: func(t *testing.T, platform *platformpostgres.Pool) managedOllamaIntegrationStore {
			store, err := localmodelruntime.NewPostgresStore(platform.DB())
			if err != nil {
				t.Fatal(err)
			}
			return store
		}},
		{name: "gorm", open: func(t *testing.T, platform *platformpostgres.Pool) managedOllamaIntegrationStore {
			store, err := localmodelruntime.NewGORMStore(platform)
			if err != nil {
				t.Fatal(err)
			}
			return store
		}},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			platform := managedOllamaTestPlatform(t)
			scenario(t, context.Background(), platform.DB(), variant.open(t, platform))
		})
	}
}

func assertPostgresCode(t *testing.T, err error, want string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if err == nil || !errors.As(err, &postgresError) || postgresError.Code != want {
		t.Fatalf("PostgreSQL error=%v code=%q want=%q", err, postgresErrorCode(postgresError), want)
	}
}

func postgresErrorCode(err *pgconn.PgError) string {
	if err == nil {
		return ""
	}
	return err.Code
}

var errManagedOllamaScopedRollback = errors.New("rollback managed Ollama scoped transaction")

type managedOllamaStatementCounter struct {
	count atomic.Int64
}

func (counter *managedOllamaStatementCounter) LogMode(gormlogger.LogLevel) gormlogger.Interface {
	return counter
}

func (*managedOllamaStatementCounter) Info(context.Context, string, ...any)  {}
func (*managedOllamaStatementCounter) Warn(context.Context, string, ...any)  {}
func (*managedOllamaStatementCounter) Error(context.Context, string, ...any) {}

func (counter *managedOllamaStatementCounter) Trace(context.Context, time.Time, func() (string, int64), error) {
	counter.count.Add(1)
}

func TestManagedOllamaMigrationConstraints(t *testing.T) {
	ctx := context.Background()
	pool := managedOllamaTestPlatform(t).DB()

	var phase, mode string
	var ownerEpoch, version int64
	if err := pool.QueryRow(ctx, `SELECT observed_phase,mode,owner_epoch,version FROM ops.managed_ollama_runtime WHERE singleton`).Scan(&phase, &mode, &ownerEpoch, &version); err != nil {
		t.Fatal(err)
	}
	if phase != "stopped" || mode != "managed" || ownerEpoch != 0 || version != 1 {
		t.Fatalf("runtime phase=%q mode=%q epoch=%d version=%d", phase, mode, ownerEpoch, version)
	}

	_, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_revisions(
		chat_provider,chat_api_style,chat_base_url,chat_model,chat_model_version,chat_adapter_version,
		chat_timeout_microseconds,chat_max_request_bytes,chat_max_response_bytes,
		embedding_provider,embedding_base_url,embedding_model,embedding_dimensions,
		embedding_normalization,embedding_distance_metric,embedding_max_batch_size,
		embedding_max_input_bytes,embedding_max_batch_input_bytes,embedding_timeout_microseconds,
		embedding_max_response_bytes,created_by
	) VALUES(
		'ollama','chat_completions','http://127.0.0.1:11434','qwen2.5:3b','qwen2.5:3b','v1',
		30000000,4194304,4194304,
		'disabled','','',0,'l2','cosine',128,65536,8388608,30000000,67108864,'migration-test'
	)`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = pool.Exec(ctx, `INSERT INTO ops.managed_ollama_holds(
		hold_id,owner_kind,owner_id,owner_epoch,requirement_hash,models,lease_expires_at
	) VALUES(
		'80000000-0000-4000-8000-000000000001','test','80000000-0000-4000-8000-000000000002',1,repeat('a',64),'[1]'::jsonb,clock_timestamp()+interval '1 minute'
	)`)
	assertPostgresCode(t, err, "23514")

	if _, err := pool.Exec(ctx, `INSERT INTO ops.managed_ollama_operations(
		operation_id,kind,idempotency_key,request_hash,requirement_hash,required_models
	) VALUES(
		'80000000-0000-4000-8000-000000000003','test','same-key',repeat('b',64),repeat('c',64),'["qwen2.5:3b"]'::jsonb
	)`); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO ops.managed_ollama_operations(
		operation_id,kind,idempotency_key,request_hash,requirement_hash,required_models
	) VALUES(
		'80000000-0000-4000-8000-000000000004','test','same-key',repeat('d',64),repeat('e',64),'["all-minilm:latest"]'::jsonb
	)`)
	assertPostgresCode(t, err, "23505")

	_, err = pool.Exec(ctx, `UPDATE ops.managed_ollama_operations
		SET phase='succeeded',terminal_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
		WHERE operation_id='80000000-0000-4000-8000-000000000003'`)
	assertPostgresCode(t, err, "55000")

	if _, err := pool.Exec(ctx, `INSERT INTO ops.managed_ollama_holds(
		hold_id,owner_kind,owner_id,owner_epoch,requirement_hash,models,lease_expires_at
	) VALUES(
		'80000000-0000-4000-8000-000000000001','test','80000000-0000-4000-8000-000000000002',1,repeat('a',64),'["qwen2.5:3b"]'::jsonb,clock_timestamp()+interval '1 minute'
	)`); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.managed_ollama_holds SET models='["other"]'::jsonb,version=version+1,updated_at=clock_timestamp() WHERE hold_id='80000000-0000-4000-8000-000000000001'`)
	assertPostgresCode(t, err, "55000")

	_, err = pool.Exec(ctx, `UPDATE ops.managed_ollama_runtime
		SET owner_id='80000000-0000-4000-8000-000000000010',owner_epoch=1,
			heartbeat_at=clock_timestamp(),lease_expires_at=clock_timestamp()+interval '1 minute',
			version=version+1,updated_at=clock_timestamp()
		WHERE singleton`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.managed_ollama_runtime
		SET owner_id='80000000-0000-4000-8000-000000000011',version=version+1,updated_at=clock_timestamp()
		WHERE singleton`)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `UPDATE ops.managed_ollama_runtime
		SET observed_phase='ready',version=version+1,updated_at=clock_timestamp()
		WHERE singleton`)
	assertPostgresCode(t, err, "55000")
}

func TestManagedOllamaMigrationRepeatedUp(t *testing.T) {
	ctx := context.Background()
	pool := managedOllamaTestPlatform(t).DB()
	if err := platformmigration.MigrateAtlas(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := platformmigration.MigrateAtlas(ctx, pool); err != nil {
		t.Fatal(err)
	}
}

func TestManagedOllamaRuntimeRoleLeastPrivilege(t *testing.T) {
	ctx := context.Background()
	pool := managedOllamaTestPlatform(t).DB()

	const role = "zhixu_local_model_runtime"
	for _, table := range []string{
		"ops.managed_ollama_runtime",
		"ops.managed_ollama_holds",
		"ops.managed_ollama_operations",
	} {
		var canRead, canInsert, canUpdate bool
		if err := pool.QueryRow(ctx, `SELECT
			has_table_privilege($1,$2,'SELECT'),
			has_table_privilege($1,$2,'INSERT'),
			has_table_privilege($1,$2,'UPDATE')`, role, table).Scan(&canRead, &canInsert, &canUpdate); err != nil {
			t.Fatal(err)
		}
		if !canRead || canInsert || canUpdate {
			t.Fatalf("runtime role privileges for %s = select:%t insert:%t update:%t", table, canRead, canInsert, canUpdate)
		}
	}

	var canReadBaseURL, canReadProjection bool
	if err := pool.QueryRow(ctx, `SELECT
		has_column_privilege($1,'ops.model_settings_revisions','chat_base_url','SELECT'),
		has_table_privilege($1,'ops.managed_ollama_revision_requirements','SELECT')`, role).Scan(
		&canReadBaseURL, &canReadProjection,
	); err != nil {
		t.Fatal(err)
	}
	if canReadBaseURL || !canReadProjection {
		t.Fatalf("runtime role projection privileges = base_url:%t projection:%t", canReadBaseURL, canReadProjection)
	}

	var commandCount int
	if err := pool.QueryRow(ctx, `SELECT count(*)
		FROM unnest(ARRAY[
			'ops.managed_ollama_claim_manager(uuid,interval,interval)'::regprocedure,
			'ops.managed_ollama_heartbeat_manager(uuid,bigint,bigint,interval)'::regprocedure,
			'ops.managed_ollama_publish_demand(uuid,bigint,bigint,bigint,text,jsonb,bigint)'::regprocedure,
			'ops.managed_ollama_seed_active_recovery(uuid,bigint,bigint,interval)'::regprocedure,
			'ops.managed_ollama_claim_operation(uuid,text,text,text,bigint,uuid,text,jsonb,uuid,bigint,interval)'::regprocedure,
			'ops.managed_ollama_begin_pull_attempt(uuid,uuid,bigint,bigint,interval)'::regprocedure,
			'ops.managed_ollama_record_operation_progress(uuid,uuid,bigint,bigint,text,jsonb,jsonb,bigint,bigint,boolean,interval,text)'::regprocedure,
			'ops.managed_ollama_complete_operation(uuid,uuid,bigint,bigint,text,text,boolean,jsonb,bigint,bigint)'::regprocedure,
			'ops.managed_ollama_expire_operations(uuid,bigint)'::regprocedure,
			'ops.managed_ollama_compare_runtime_phase(uuid,bigint,bigint,text,text,text,text,bigint,text,boolean,bigint)'::regprocedure
		]::oid[]) command(oid)
		WHERE has_function_privilege($1, command.oid, 'EXECUTE')`, role).Scan(&commandCount); err != nil {
		t.Fatal(err)
	}
	if commandCount != 10 {
		t.Fatalf("runtime role command function privileges = %d, want 10", commandCount)
	}

	var publicExecute bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid=p.pronamespace
		CROSS JOIN LATERAL aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) privilege
		WHERE n.nspname='ops' AND p.prosecdef
		  AND privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
	)`).Scan(&publicExecute); err != nil {
		t.Fatal(err)
	}
	if publicExecute {
		t.Fatal("ops SECURITY DEFINER function still grants PUBLIC execute")
	}

	var roleStateSafe bool
	if err := pool.QueryRow(ctx, `SELECT rolconfig IS NULL AND rolconnlimit=-1
		AND (rolvaliduntil IS NULL OR rolvaliduntil='infinity'::timestamptz)
		FROM pg_roles WHERE rolname=$1`, role).Scan(&roleStateSafe); err != nil {
		t.Fatal(err)
	}
	if !roleStateSafe {
		t.Fatal("runtime role retains global settings, a connection limit, or an expiry")
	}
}

func TestManagedOllamaPersistentPullBudgetAndDeadline(t *testing.T) {
	runManagedOllamaIntegrationVariants(t, testManagedOllamaPersistentPullBudgetAndDeadline)
}

func testManagedOllamaPersistentPullBudgetAndDeadline(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store managedOllamaIntegrationStore,
) {
	owner, _ := foundation.ParseID("80000000-0000-4000-8000-000000000020")
	requirement, err := localmodelruntime.NewRequirement([]localmodelruntime.ModelRef{"qwen2.5:3b"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimManager(ctx, localmodelruntime.ManagerClaimCommand{
		OwnerID: owner, LeaseDuration: time.Minute, StaleAfter: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	operationID, _ := foundation.ParseID("80000000-0000-4000-8000-000000000021")
	holdID, _ := foundation.ParseID("80000000-0000-4000-8000-000000000022")
	const requestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := store.SeedTestPreparation(ctx, localmodelruntime.TestPreparationCommand{
		OperationID: operationID, HoldID: holdID, IdempotencyKey: "persistent-pull-budget",
		RequestHash: requestHash, Requirement: requirement, OwnerID: owner,
		OwnerEpoch: lease.OwnerEpoch, LeaseDuration: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	operation, err := store.ClaimOperation(ctx, localmodelruntime.OperationClaimCommand{
		OperationID: operationID, Kind: localmodelruntime.OperationKindTest,
		IdempotencyKey: "persistent-pull-budget", RequestHash: requestHash,
		Requirement: requirement, OwnerID: owner, OwnerEpoch: lease.OwnerEpoch,
		LeaseDuration: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	operation, err = store.RecordOperationProgress(ctx, localmodelruntime.OperationProgressCommand{
		OperationID: operationID, OwnerID: owner, OwnerEpoch: lease.OwnerEpoch,
		ExpectedVersion: operation.Version, ExpectedPhase: operation.Phase,
		NextPhase: localmodelruntime.OperationPhaseStarting, LeaseDuration: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	for wantAttempt := 1; wantAttempt <= 3; wantAttempt++ {
		attempt, err := store.BeginPullAttempt(ctx, localmodelruntime.PullAttemptCommand{
			OperationID: operationID, OwnerID: owner, OwnerEpoch: lease.OwnerEpoch,
			ExpectedVersion: operation.Version, LeaseDuration: time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
		operation = attempt.Operation
		if operation.AttemptNo != wantAttempt || operation.Phase == localmodelruntime.OperationPhaseFailed {
			t.Fatalf("pull attempt %d returned operation %+v", wantAttempt, operation)
		}
		if attempt.Remaining <= 0 || attempt.Remaining > 6*time.Hour {
			t.Fatalf("pull attempt remaining budget = %s", attempt.Remaining)
		}
	}
	exhausted, err := store.BeginPullAttempt(ctx, localmodelruntime.PullAttemptCommand{
		OperationID: operationID, OwnerID: owner, OwnerEpoch: lease.OwnerEpoch,
		ExpectedVersion: operation.Version, LeaseDuration: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exhausted.Operation.AttemptNo != 3 || exhausted.Operation.Phase != localmodelruntime.OperationPhaseFailed ||
		exhausted.Operation.ErrorCode != "LOCAL_MODEL_RUNTIME_PULL_ATTEMPTS_EXHAUSTED" || exhausted.Operation.Retryable {
		t.Fatalf("exhausted operation = %+v", exhausted.Operation)
	}
	assertManagedOllamaHoldReleased(t, ctx, pool, operationID)

	expiredOperationID := "80000000-0000-4000-8000-000000000023"
	expiredHoldID := "80000000-0000-4000-8000-000000000024"
	if _, err := pool.Exec(ctx, `INSERT INTO ops.managed_ollama_operations(
		operation_id,kind,idempotency_key,request_hash,requirement_hash,required_models,created_at,updated_at
	) VALUES($1::uuid,'test','persistent-deadline',$2,$3,$4::jsonb,
		clock_timestamp()-interval '6 hours 1 second',clock_timestamp()-interval '6 hours 1 second')`,
		expiredOperationID, requestHash, requirement.Hash, `["qwen2.5:3b"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.managed_ollama_holds(
		hold_id,owner_kind,owner_id,owner_epoch,role,instance_id,operation_id,requirement_hash,models,lease_expires_at,created_at,updated_at
	) VALUES($1::uuid,'test',$2::uuid,$3,'api',$2::uuid,$4::uuid,$5,$6::jsonb,
		clock_timestamp()+interval '1 hour',clock_timestamp()-interval '6 hours 1 second',clock_timestamp()-interval '6 hours 1 second')`,
		expiredHoldID, string(owner), lease.OwnerEpoch, expiredOperationID, requirement.Hash, `["qwen2.5:3b"]`); err != nil {
		t.Fatal(err)
	}
	expired, err := store.SweepExpiredOperations(ctx, localmodelruntime.OperationExpirySweepCommand{
		OwnerID: owner, OwnerEpoch: lease.OwnerEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if expired != 1 {
		t.Fatalf("expired operation count=%d, want 1", expired)
	}
	expiredID, _ := foundation.ParseID(expiredOperationID)
	expiredOperation, err := store.ReadTestOperation(ctx, expiredID)
	if err != nil {
		t.Fatal(err)
	}
	if expiredOperation.Phase != localmodelruntime.OperationPhaseFailed || expiredOperation.TerminalAt == nil ||
		expiredOperation.ErrorCode != "LOCAL_MODEL_RUNTIME_PREPARATION_TIMEOUT" || expiredOperation.Retryable {
		t.Fatalf("expired operation = %+v", expiredOperation)
	}
	assertManagedOllamaHoldReleased(t, ctx, pool, expiredID)
}

func assertManagedOllamaHoldReleased(t *testing.T, ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, operationID foundation.ID) {
	t.Helper()
	var released bool
	if err := pool.QueryRow(ctx, `SELECT released_at IS NOT NULL FROM ops.managed_ollama_holds WHERE operation_id=$1::uuid`, string(operationID)).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatalf("operation %s hold is still active", operationID)
	}
}

func TestManagedOllamaActiveRecoverySeed(t *testing.T) {
	runManagedOllamaIntegrationVariants(t, testManagedOllamaActiveRecoverySeed)
}

func testManagedOllamaActiveRecoverySeed(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store managedOllamaIntegrationStore,
) {
	var revision int64
	if err := pool.QueryRow(ctx, `INSERT INTO ops.model_settings_revisions(
		chat_provider,chat_api_style,chat_base_url,chat_model,chat_model_version,chat_adapter_version,
		chat_timeout_microseconds,chat_max_request_bytes,chat_max_response_bytes,
		embedding_provider,embedding_base_url,embedding_model,embedding_dimensions,
		embedding_normalization,embedding_distance_metric,embedding_max_batch_size,
		embedding_max_input_bytes,embedding_max_batch_input_bytes,embedding_timeout_microseconds,
		embedding_max_response_bytes,created_by
	) VALUES(
		'ollama','chat_completions','http://local-model-runtime:11434','qwen2.5:3b','qwen2.5:3b','v1',
		30000000,4194304,4194304,
		'disabled','','',0,'l2','cosine',128,65536,8388608,30000000,67108864,'active-recovery-test'
	) RETURNING revision`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	setup, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Rollback(ctx) //nolint:errcheck -- cleanup after either path
	if _, err := setup.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(ctx, `UPDATE ops.model_settings_state
		SET desired_revision=$1,active_revision=$1,version=version+1,updated_at=clock_timestamp()
		WHERE singleton=true`, revision); err != nil {
		t.Fatal(err)
	}
	if err := setup.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var settingsVersion int64
	if err := pool.QueryRow(ctx, `SELECT version FROM ops.model_settings_state WHERE singleton=true`).Scan(&settingsVersion); err != nil {
		t.Fatal(err)
	}
	owner, _ := foundation.ParseID("80000000-0000-4000-8000-000000000030")
	lease, err := store.ClaimManager(ctx, localmodelruntime.ManagerClaimCommand{
		OwnerID: owner, LeaseDuration: time.Minute, StaleAfter: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := localmodelruntime.ActiveRecoveryCommand{
		OwnerID: owner, OwnerEpoch: lease.OwnerEpoch,
		ExpectedSettingsStateVersion: settingsVersion, LeaseDuration: time.Hour,
	}
	seeded, err := store.SeedActiveRecovery(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.SeedActiveRecovery(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if seeded.OperationID != replayed.OperationID || seeded.Kind != localmodelruntime.OperationKindActiveRecovery ||
		seeded.TargetRevision == nil || *seeded.TargetRevision != revision ||
		len(seeded.Requirement.Models) != 1 || seeded.Requirement.Models[0] != "qwen2.5:3b" {
		t.Fatalf("active recovery seed/replay = %+v / %+v", seeded, replayed)
	}
	current, err := store.ActiveRecoveryCurrent(ctx, localmodelruntime.ActiveRecoveryCheckCommand{
		OperationID: seeded.OperationID, OwnerID: owner, OwnerEpoch: lease.OwnerEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !current {
		t.Fatal("active recovery was not current")
	}
	var holdCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.managed_ollama_holds
		WHERE operation_id=$1::uuid AND released_at IS NULL`, string(seeded.OperationID)).Scan(&holdCount); err != nil {
		t.Fatal(err)
	}
	if holdCount != 1 {
		t.Fatalf("active recovery hold count=%d, want 1", holdCount)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
		SET active_revision=0,desired_revision=0,version=version+1,updated_at=clock_timestamp()
		WHERE singleton=true`); err == nil {
		t.Fatal("direct active revision reset unexpectedly bypassed activation guard")
	}
	operation, err := store.ClaimOperation(ctx, localmodelruntime.OperationClaimCommand{
		OperationID: seeded.OperationID, Kind: seeded.Kind,
		IdempotencyKey: seeded.IdempotencyKey, RequestHash: seeded.RequestHash,
		TargetRevision: seeded.TargetRevision, Requirement: seeded.Requirement,
		OwnerID: owner, OwnerEpoch: lease.OwnerEpoch, LeaseDuration: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []localmodelruntime.OperationPhase{
		localmodelruntime.OperationPhaseStarting,
		localmodelruntime.OperationPhaseChecking,
		localmodelruntime.OperationPhaseVerifying,
	} {
		operation, err = store.RecordOperationProgress(ctx, localmodelruntime.OperationProgressCommand{
			OperationID: operation.OperationID, OwnerID: owner, OwnerEpoch: lease.OwnerEpoch,
			ExpectedVersion: operation.Version, ExpectedPhase: operation.Phase,
			NextPhase: phase, LeaseDuration: time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	operation, err = store.CompleteOperation(ctx, localmodelruntime.OperationTerminalCommand{
		OperationID: operation.OperationID, OwnerID: owner, OwnerEpoch: lease.OwnerEpoch,
		ExpectedVersion: operation.Version, Phase: localmodelruntime.OperationPhaseReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	if operation.Phase != localmodelruntime.OperationPhaseSucceeded || operation.TerminalAt == nil {
		t.Fatalf("completed active recovery = %+v", operation)
	}
	assertManagedOllamaHoldReleased(t, ctx, pool, operation.OperationID)
	nextGeneration, err := store.SeedActiveRecovery(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if nextGeneration.OperationID == operation.OperationID || nextGeneration.Phase != localmodelruntime.OperationPhaseQueued {
		t.Fatalf("next active recovery generation = %+v", nextGeneration)
	}
	replayedGeneration, err := store.SeedActiveRecovery(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if replayedGeneration.OperationID != nextGeneration.OperationID {
		t.Fatalf("active recovery generation replay = %s, want %s", replayedGeneration.OperationID, nextGeneration.OperationID)
	}
}

func TestManagedOllamaSupersedesStaleActiveRecovery(t *testing.T) {
	runManagedOllamaIntegrationVariants(t, testManagedOllamaSupersedesStaleActiveRecovery)
}

func testManagedOllamaSupersedesStaleActiveRecovery(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store managedOllamaIntegrationStore,
) {
	requirement, _ := localmodelruntime.NewRequirement([]localmodelruntime.ModelRef{"qwen2.5:3b"})
	const operationID = "80000000-0000-4000-8000-000000000031"
	const holdID = "80000000-0000-4000-8000-000000000032"
	if _, err := pool.Exec(ctx, `INSERT INTO ops.managed_ollama_operations(
		operation_id,kind,idempotency_key,request_hash,target_revision,requirement_hash,required_models
	) VALUES($1::uuid,'active_recovery','stale-active-recovery',repeat('a',64),1,$2,$3::jsonb)`,
		operationID, requirement.Hash, `["qwen2.5:3b"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.managed_ollama_holds(
		hold_id,owner_kind,owner_id,owner_epoch,revision,operation_id,requirement_hash,models,lease_expires_at
	) VALUES($1::uuid,'preparation',$2::uuid,1,1,$2::uuid,$3,$4::jsonb,clock_timestamp()+interval '1 hour')`,
		holdID, operationID, requirement.Hash, `["qwen2.5:3b"]`); err != nil {
		t.Fatal(err)
	}
	owner, _ := foundation.ParseID("80000000-0000-4000-8000-000000000033")
	lease, err := store.ClaimManager(ctx, localmodelruntime.ManagerClaimCommand{
		OwnerID: owner, LeaseDuration: time.Minute, StaleAfter: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ReadDemand(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Requirement.Models) != 0 || len(snapshot.Operations) != 0 || len(snapshot.Holds) != 0 || !snapshot.MayStop {
		t.Fatalf("stale recovery remained in online demand: %+v", snapshot)
	}
	staleOperationID, _ := foundation.ParseID(operationID)
	staleRevision := int64(1)
	current, err := store.ActiveRecoveryCurrent(ctx, localmodelruntime.ActiveRecoveryCheckCommand{
		OperationID: staleOperationID, OwnerID: owner, OwnerEpoch: lease.OwnerEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if current {
		t.Fatal("stale active recovery remained current")
	}
	_, err = store.ClaimOperation(ctx, localmodelruntime.OperationClaimCommand{
		OperationID: staleOperationID, Kind: localmodelruntime.OperationKindActiveRecovery,
		IdempotencyKey: "stale-active-recovery", RequestHash: strings.Repeat("a", 64),
		TargetRevision: &staleRevision, Requirement: requirement,
		OwnerID: owner, OwnerEpoch: lease.OwnerEpoch, LeaseDuration: time.Minute,
	})
	if err == nil {
		t.Fatal("stale active recovery claim unexpectedly succeeded")
	}
	count, err := store.SweepExpiredOperations(ctx, localmodelruntime.OperationExpirySweepCommand{
		OwnerID: owner, OwnerEpoch: lease.OwnerEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("superseded operation count=%d, want 1", count)
	}
	var phase string
	if err := pool.QueryRow(ctx, `SELECT phase FROM ops.managed_ollama_operations WHERE operation_id=$1::uuid`, operationID).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != "superseded" {
		t.Fatalf("stale active recovery phase=%q, want superseded", phase)
	}
	assertManagedOllamaHoldReleased(t, ctx, pool, staleOperationID)
}

func TestManagedOllamaLifecycleStoreCAS(t *testing.T) {
	runManagedOllamaIntegrationVariants(t, testManagedOllamaLifecycleStoreCAS)
}

func TestManagedOllamaScopedActivationTransaction(t *testing.T) {
	ctx := context.Background()
	platform := managedOllamaTestPlatform(t)
	store, err := localmodelruntime.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork, err := platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := localmodelruntime.NewRequirement([]localmodelruntime.ModelRef{"qwen2.5:3b"})
	if err != nil {
		t.Fatal(err)
	}
	ownerID, _ := foundation.ParseID("80000000-0000-4000-8000-000000000040")

	committed := localmodelruntime.ActivationPreparationCommand{
		OperationID:    mustManagedOllamaID(t, "80000000-0000-4000-8000-000000000041"),
		HoldID:         mustManagedOllamaID(t, "80000000-0000-4000-8000-000000000042"),
		RolloutID:      mustManagedOllamaID(t, "80000000-0000-4000-8000-000000000043"),
		TargetRevision: 41,
		IdempotencyKey: "managed-ollama-scoped-commit",
		RequestHash:    strings.Repeat("b", 64),
		Requirement:    requirement,
		OwnerID:        ownerID,
		OwnerEpoch:     1,
		LeaseDuration:  time.Minute,
	}
	var stale localmodelruntime.TxStore
	if err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		scoped, bindErr := store.WithScope(scope)
		if bindErr != nil {
			return bindErr
		}
		stale = scoped
		preparation, seedErr := scoped.SeedActivationPreparation(callbackCtx, committed)
		if seedErr != nil {
			return seedErr
		}
		if preparation.Operation.RolloutID == nil || *preparation.Operation.RolloutID != committed.RolloutID || preparation.Hold.OperationID == nil || *preparation.Hold.OperationID != committed.OperationID {
			return errors.New("managed Ollama scoped seed identity mismatch")
		}
		byRollout, readErr := scoped.ReadOperationByRollout(callbackCtx, committed.RolloutID)
		if readErr != nil || byRollout.OperationID != committed.OperationID {
			return errors.New("managed Ollama scoped rollout read mismatch")
		}
		byTarget, readErr := scoped.ReadOperationByTarget(callbackCtx, committed.TargetRevision)
		if readErr != nil || byTarget.OperationID != committed.OperationID {
			return errors.New("managed Ollama scoped target read mismatch")
		}
		if count := managedOllamaOperationCount(t, ctx, platform.DB(), committed.OperationID); count != 0 {
			return errors.New("managed Ollama scoped insert became visible before commit")
		}
		completed, completeErr := scoped.CompleteActivationPreparation(callbackCtx, committed.RolloutID, "SCOPED_TEST_FAILURE", false)
		if completeErr != nil {
			return completeErr
		}
		if completed.Phase != localmodelruntime.OperationPhaseFailed || completed.TerminalAt == nil {
			return errors.New("managed Ollama scoped completion did not become terminal")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count := managedOllamaOperationCount(t, ctx, platform.DB(), committed.OperationID); count != 1 {
		t.Fatalf("committed scoped operation count=%d, want 1", count)
	}
	var releasedAt *time.Time
	if err := platform.DB().QueryRow(ctx, `SELECT released_at FROM ops.managed_ollama_holds WHERE hold_id=$1::uuid`, string(committed.HoldID)).Scan(&releasedAt); err != nil {
		t.Fatal(err)
	}
	if releasedAt == nil {
		t.Fatal("committed scoped completion did not release preparation hold")
	}
	if _, err := stale.ReadOperationByRollout(ctx, committed.RolloutID); err == nil {
		t.Fatal("stale managed Ollama scope unexpectedly remained usable")
	}

	rolledBack := committed
	rolledBack.OperationID = mustManagedOllamaID(t, "80000000-0000-4000-8000-000000000044")
	rolledBack.HoldID = mustManagedOllamaID(t, "80000000-0000-4000-8000-000000000045")
	rolledBack.RolloutID = mustManagedOllamaID(t, "80000000-0000-4000-8000-000000000046")
	rolledBack.TargetRevision = 42
	rolledBack.IdempotencyKey = "managed-ollama-scoped-rollback"
	rolledBack.RequestHash = strings.Repeat("c", 64)
	err = unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		scoped, bindErr := store.WithScope(scope)
		if bindErr != nil {
			return bindErr
		}
		if _, seedErr := scoped.SeedActivationPreparation(callbackCtx, rolledBack); seedErr != nil {
			return seedErr
		}
		if _, completeErr := scoped.CompleteActivationPreparation(callbackCtx, rolledBack.RolloutID, "SCOPED_TEST_FAILURE", true); completeErr != nil {
			return completeErr
		}
		return errManagedOllamaScopedRollback
	})
	if !errors.Is(err, errManagedOllamaScopedRollback) {
		t.Fatalf("scoped rollback error=%v, want sentinel", err)
	}
	if count := managedOllamaOperationCount(t, ctx, platform.DB(), rolledBack.OperationID); count != 0 {
		t.Fatalf("rolled-back scoped operation count=%d, want 0", count)
	}
}

func TestManagedOllamaReadDemandCancellationAndConnectionRelease(t *testing.T) {
	runManagedOllamaIntegrationVariants(t, func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, store managedOllamaIntegrationStore) {
		lock, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Rollback(context.Background()) }()
		if _, err := lock.Exec(ctx, `LOCK TABLE ops.managed_ollama_runtime IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		baseline := pool.Stat().AcquiredConns()
		cause := errors.New("cancel blocked managed Ollama demand read")
		blockedCtx, cancel := context.WithCancelCause(ctx)
		done := make(chan error, 1)
		go func() {
			_, readErr := store.ReadDemand(blockedCtx)
			done <- readErr
		}()
		deadline := time.Now().Add(5 * time.Second)
		for pool.Stat().AcquiredConns() <= baseline && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		cancel(cause)
		select {
		case readErr := <-done:
			if !errors.Is(readErr, context.Canceled) {
				t.Fatalf("canceled demand read error=%v, want context cancellation", readErr)
			}
			if _, isGORM := store.(*localmodelruntime.GORMStore); isGORM && !errors.Is(readErr, cause) {
				t.Fatalf("GORM canceled demand read error=%v, want custom cause", readErr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("canceled managed Ollama demand read did not return")
		}
		deadline = time.Now().Add(5 * time.Second)
		for pool.Stat().AcquiredConns() > baseline && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if acquired := pool.Stat().AcquiredConns(); acquired != baseline {
			t.Fatalf("acquired connections after cancellation=%d, want %d", acquired, baseline)
		}
	})
}

func TestManagedOllamaGORMStatementCountAndExplain(t *testing.T) {
	ctx := context.Background()
	platform := managedOllamaTestPlatform(t)
	store, err := localmodelruntime.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	database, err := platform.GORM()
	if err != nil {
		t.Fatal(err)
	}
	original := database.Config.Logger
	counter := &managedOllamaStatementCounter{}
	database.Config.Logger = counter
	t.Cleanup(func() { database.Config.Logger = original })
	if _, err := store.ReadDemand(ctx); err != nil {
		t.Fatal(err)
	}
	if statements := counter.count.Load(); statements != 4 {
		t.Fatalf("GORM ReadDemand statements=%d, want 4", statements)
	}

	queries := []struct {
		name  string
		query string
		args  []any
		index string
	}{
		{name: "operation idempotency", query: `SELECT operation_id FROM ops.managed_ollama_operations WHERE kind=$1 AND idempotency_key=$2`, args: []any{"test", "missing"}, index: "ops_managed_ollama_operations_idempotency_idx"},
		{name: "hold operation", query: `SELECT hold_id FROM ops.managed_ollama_holds WHERE operation_id=$1::uuid`, args: []any{"80000000-0000-4000-8000-000000000047"}, index: "ops_managed_ollama_holds_operation_idx"},
	}
	tx, err := platform.DB().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	for _, query := range queries {
		t.Run(query.name, func(t *testing.T) {
			rows, explainErr := tx.Query(ctx, `EXPLAIN (COSTS OFF) `+query.query, query.args...)
			if explainErr != nil {
				t.Fatal(explainErr)
			}
			defer rows.Close()
			var lines []string
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				lines = append(lines, line)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			plan := strings.Join(lines, "\n")
			if !strings.Contains(plan, query.index) {
				t.Fatalf("managed Ollama EXPLAIN missing %s:\n%s", query.index, plan)
			}
		})
	}
}

func mustManagedOllamaID(t *testing.T, raw string) foundation.ID {
	t.Helper()
	id, err := foundation.ParseID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func managedOllamaOperationCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, operationID foundation.ID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.managed_ollama_operations WHERE operation_id=$1::uuid`, string(operationID)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func testManagedOllamaLifecycleStoreCAS(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store managedOllamaIntegrationStore,
) {
	owner, err := foundation.ParseID("80000000-0000-4000-8000-000000000010")
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := localmodelruntime.NewRequirement([]localmodelruntime.ModelRef{"qwen2.5:3b"})
	if err != nil {
		t.Fatal(err)
	}

	lease, err := store.ClaimManager(ctx, localmodelruntime.ManagerClaimCommand{
		OwnerID: owner, LeaseDuration: time.Minute, StaleAfter: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.OwnerEpoch != 1 {
		t.Fatalf("initial manager epoch=%d, want 1", lease.OwnerEpoch)
	}
	// A same-owner claim is a renewal, not a fencing takeover.
	renewed, err := store.ClaimManager(ctx, localmodelruntime.ManagerClaimCommand{
		OwnerID: owner, LeaseDuration: time.Minute, StaleAfter: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.OwnerEpoch != lease.OwnerEpoch {
		t.Fatalf("same-owner claim changed epoch from %d to %d", lease.OwnerEpoch, renewed.OwnerEpoch)
	}
	heartbeat, err := store.HeartbeatManager(ctx, localmodelruntime.ManagerHeartbeatCommand{
		OwnerID: owner, OwnerEpoch: renewed.OwnerEpoch, ExpectedVersion: renewed.Version,
		LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.ReadDemand(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SettingsStateVersion <= 0 {
		t.Fatalf("settings state version=%d, want positive", snapshot.SettingsStateVersion)
	}
	updatedRuntime, err := store.PublishDemand(ctx, localmodelruntime.DemandCASCommand{
		OwnerID: owner, OwnerEpoch: heartbeat.OwnerEpoch, ExpectedVersion: heartbeat.Version,
		ExpectedRequirementVersion:   heartbeat.RequirementVersion,
		ExpectedSettingsStateVersion: snapshot.SettingsStateVersion,
		Requirement:                  requirement,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updatedRuntime.Requirement.Hash != requirement.Hash {
		t.Fatalf("published requirement hash=%q, want %q", updatedRuntime.Requirement.Hash, requirement.Hash)
	}

	holdID, _ := foundation.ParseID("80000000-0000-4000-8000-000000000011")
	hold, err := store.AcquireHold(ctx, localmodelruntime.HoldAcquireCommand{
		HoldID: holdID, Kind: localmodelruntime.HoldKindTest, OwnerID: owner, OwnerEpoch: heartbeat.OwnerEpoch,
		Requirement: requirement, LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	hold, err = store.RenewHold(ctx, localmodelruntime.HoldRenewCommand{
		HoldID: hold.HoldID, OwnerID: owner, OwnerEpoch: hold.OwnerEpoch,
		ExpectedVersion: hold.Version, LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseHold(ctx, localmodelruntime.HoldReleaseCommand{
		HoldID: hold.HoldID, OwnerID: owner, OwnerEpoch: hold.OwnerEpoch,
		ExpectedVersion: hold.Version,
	}); err != nil {
		t.Fatal(err)
	}

	operationID, _ := foundation.ParseID("80000000-0000-4000-8000-000000000012")
	_, err = store.ClaimOperation(ctx, localmodelruntime.OperationClaimCommand{
		OperationID: operationID, Kind: localmodelruntime.OperationKindTest,
		IdempotencyKey: "migration-store-cas", RequestHash: strings.Repeat("a", 64),
		Requirement: requirement, OwnerID: owner, OwnerEpoch: heartbeat.OwnerEpoch, LeaseDuration: time.Minute,
	})
	if err == nil {
		t.Fatal("unseeded operation claim unexpectedly succeeded")
	}
	var unseededCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.managed_ollama_operations WHERE operation_id=$1::uuid`, string(operationID)).Scan(&unseededCount); err != nil {
		t.Fatal(err)
	}
	if unseededCount != 0 {
		t.Fatalf("unseeded operation count=%d, want 0", unseededCount)
	}
	operationHoldID, _ := foundation.ParseID("80000000-0000-4000-8000-000000000013")
	preparation, err := store.SeedTestPreparation(ctx, localmodelruntime.TestPreparationCommand{
		OperationID: operationID, HoldID: operationHoldID,
		IdempotencyKey: "migration-store-cas", RequestHash: strings.Repeat("a", 64),
		Requirement: requirement, OwnerID: owner, OwnerEpoch: heartbeat.OwnerEpoch, LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preparation.Operation.OperationID != operationID || preparation.Hold.OperationID == nil || *preparation.Hold.OperationID != operationID {
		t.Fatalf("seeded preparation identity mismatch: %#v", preparation)
	}
	operation, err := store.ClaimOperation(ctx, localmodelruntime.OperationClaimCommand{
		OperationID: operationID, Kind: localmodelruntime.OperationKindTest,
		IdempotencyKey: "migration-store-cas", RequestHash: strings.Repeat("a", 64),
		Requirement: requirement, OwnerID: owner, OwnerEpoch: heartbeat.OwnerEpoch, LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	total := int64(2)
	completed := int64(1)
	operation, err = store.RecordOperationProgress(ctx, localmodelruntime.OperationProgressCommand{
		OperationID: operation.OperationID, OwnerID: owner, OwnerEpoch: operation.ClaimOwnerEpoch,
		ExpectedVersion: operation.Version, ExpectedPhase: operation.Phase, NextPhase: localmodelruntime.OperationPhaseStarting,
		LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	operation, err = store.RecordOperationProgress(ctx, localmodelruntime.OperationProgressCommand{
		OperationID: operation.OperationID, OwnerID: owner, OwnerEpoch: operation.ClaimOwnerEpoch,
		ExpectedVersion: operation.Version, ExpectedPhase: operation.Phase, NextPhase: localmodelruntime.OperationPhasePulling,
		CompletedModels: []localmodelruntime.ModelRef{requirement.Models[0]}, CompletedBytes: completed,
		TotalBytes: &total, ProgressKnown: true, LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	operation, err = store.RecordOperationProgress(ctx, localmodelruntime.OperationProgressCommand{
		OperationID: operation.OperationID, OwnerID: owner, OwnerEpoch: operation.ClaimOwnerEpoch,
		ExpectedVersion: operation.Version, ExpectedPhase: operation.Phase, NextPhase: localmodelruntime.OperationPhaseVerifying,
		CompletedModels: []localmodelruntime.ModelRef{requirement.Models[0]}, CompletedBytes: completed,
		TotalBytes: &total, ProgressKnown: true, LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	operation, err = store.CompleteOperation(ctx, localmodelruntime.OperationTerminalCommand{
		OperationID: operation.OperationID, OwnerID: owner, OwnerEpoch: operation.ClaimOwnerEpoch,
		ExpectedVersion: operation.Version, Phase: localmodelruntime.OperationPhaseReady,
		CompletedModels: []localmodelruntime.ModelRef{requirement.Models[0]}, CompletedBytes: total,
		TotalBytes: &total,
	})
	if err != nil {
		t.Fatal(err)
	}
	if operation.Phase != localmodelruntime.OperationPhaseReady || operation.TerminalAt != nil {
		t.Fatalf("prepared operation phase=%q terminal_at=%v", operation.Phase, operation.TerminalAt)
	}
	operation, err = store.CompleteTestPreparation(ctx, operation.OperationID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Phase != localmodelruntime.OperationPhaseSucceeded || operation.TerminalAt == nil {
		t.Fatalf("probed operation phase=%q terminal_at=%v", operation.Phase, operation.TerminalAt)
	}

	phase, err := store.CompareAndSetRuntimePhase(ctx, localmodelruntime.RuntimePhaseCommand{
		OwnerID: owner, OwnerEpoch: heartbeat.OwnerEpoch, ExpectedVersion: updatedRuntime.Version,
		ExpectedPhase: localmodelruntime.RuntimePhaseStopped, NextPhase: localmodelruntime.RuntimePhaseStarting,
		RequirementHash: requirement.Hash, ChildEpoch: 1,
		ExpectedSettingsStateVersion: snapshot.SettingsStateVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if phase.Phase != localmodelruntime.RuntimePhaseStarting {
		t.Fatalf("runtime phase=%q, want starting", phase.Phase)
	}

	// A settings-state change fences a phase transition based on an old demand
	// projection. The zero value remains an explicit opt-out for callers that
	// already hold an equivalent settings fence.
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
		SET version=version+1,updated_at=clock_timestamp() WHERE singleton`); err != nil {
		t.Fatal(err)
	}
	_, err = store.CompareAndSetRuntimePhase(ctx, localmodelruntime.RuntimePhaseCommand{
		OwnerID: owner, OwnerEpoch: heartbeat.OwnerEpoch, ExpectedVersion: phase.Version,
		ExpectedPhase: localmodelruntime.RuntimePhaseStarting, NextPhase: localmodelruntime.RuntimePhaseChecking,
		RequirementHash: requirement.Hash, ChildEpoch: 1,
		ExpectedSettingsStateVersion: snapshot.SettingsStateVersion,
	})
	if err == nil {
		t.Fatal("stale settings-state phase CAS unexpectedly succeeded")
	}
	var settingsVersion int64
	if err := pool.QueryRow(ctx, `SELECT version FROM ops.model_settings_state WHERE singleton`).Scan(&settingsVersion); err != nil {
		t.Fatal(err)
	}
	phase, err = store.CompareAndSetRuntimePhase(ctx, localmodelruntime.RuntimePhaseCommand{
		OwnerID: owner, OwnerEpoch: heartbeat.OwnerEpoch, ExpectedVersion: phase.Version,
		ExpectedPhase: localmodelruntime.RuntimePhaseStarting, NextPhase: localmodelruntime.RuntimePhaseChecking,
		RequirementHash: requirement.Hash, ChildEpoch: 1,
		ExpectedSettingsStateVersion: settingsVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if phase.Phase != localmodelruntime.RuntimePhaseChecking {
		t.Fatalf("runtime phase after fresh settings CAS=%q, want checking", phase.Phase)
	}
}
