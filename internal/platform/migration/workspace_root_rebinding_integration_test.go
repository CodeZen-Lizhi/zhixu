//go:build integration

package migration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkspaceRootRebindingIsAuditedIdempotentAndFailClosed(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 81); err != nil {
		t.Fatal(err)
	}
	auditStore, err := auditpostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := workspacepostgres.NewRepository(pool, workspacepostgres.WithAuditAppender(auditStore))
	if err != nil {
		t.Fatal(err)
	}
	const (
		workspaceID  = "68100000-0000-4000-8000-000000000001"
		controllerID = "68100000-0000-4000-8000-000000000002"
		eventID      = "68100000-0000-4000-8000-000000000003"
		runtimeID    = "68100000-0000-4000-8000-000000000005"
		gateOwnerID  = "68100000-0000-4000-8000-000000000006"
		noAuditID    = "68100000-0000-4000-8000-000000000098"
		orphanID     = "68100000-0000-4000-8000-000000000099"
		root         = "/tmp/root-rebinding"
	)
	oldFingerprint := strings.Repeat("a", 64)
	newFingerprint := strings.Repeat("b", 64)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
status,availability,availability_reason,availability_checked_at,version,created_at,updated_at)
	VALUES($1,'rebind-fixture',$2,$3,1,$2,$4,'inactive','migration_required',
	'WORKSPACE_ROOT_IDENTITY_CHANGED',$4,7,$4,$4)`, workspaceID, root, oldFingerprint, now); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE core.workspace
SET root_fingerprint=$2,binding_version=2,availability='available',availability_reason=NULL,
    availability_checked_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
WHERE id=$1`, workspaceID, newFingerprint)
	assertPostgresCode(t, err, "55000")

	_, err = pool.Exec(ctx, `INSERT INTO ops.workspace_root_binding_history(
id,workspace_id,controller_instance_id,idempotency_key,canonical_root,
old_root_fingerprint,new_root_fingerprint,old_binding_version,new_binding_version,
old_workspace_version,new_workspace_version)
	VALUES($1,$2,$3,'orphan-history',$4,$5,$6,1,2,7,8)`,
		orphanID, workspaceID, controllerID, root, oldFingerprint, newFingerprint)
	assertPostgresCode(t, err, "55000")

	workspaceBeforeMissingAudit := workspaceRootRebindWorkspaceJSON(t, ctx, pool, workspaceID)
	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = transaction.Exec(ctx, `INSERT INTO ops.workspace_root_binding_history(
	id,workspace_id,controller_instance_id,idempotency_key,canonical_root,
	old_root_fingerprint,new_root_fingerprint,old_binding_version,new_binding_version,
	old_workspace_version,new_workspace_version)
	VALUES($1,$2,$3,'missing-audit',$4,$5,$6,1,2,7,8)`,
		noAuditID, workspaceID, controllerID, root, oldFingerprint, newFingerprint); err != nil {
		_ = transaction.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err = transaction.Exec(ctx, `UPDATE core.workspace
	SET root_fingerprint=$2,binding_version=2,availability='available',availability_reason=NULL,
	    availability_checked_at=clock_timestamp(),version=8,updated_at=clock_timestamp()
	WHERE id=$1`, workspaceID, newFingerprint); err != nil {
		_ = transaction.Rollback(ctx)
		t.Fatal(err)
	}
	err = transaction.Commit(ctx)
	assertPostgresCode(t, err, "55000")
	if workspaceAfterMissingAudit := workspaceRootRebindWorkspaceJSON(t, ctx, pool, workspaceID); workspaceAfterMissingAudit != workspaceBeforeMissingAudit {
		t.Fatalf("missing-audit transaction changed Workspace\nbefore=%s\nafter=%s", workspaceBeforeMissingAudit, workspaceAfterMissingAudit)
	}
	assertWorkspaceRootRebindCounts(t, ctx, pool, workspaceID, 0, 0)

	if _, err := pool.Exec(ctx, `INSERT INTO ops.workspace_runtime(
	role,instance_id,workspace_id,operation_id,grant_generation,root_fingerprint,binding_version,
	phase,started_at,heartbeat_at,version)
	VALUES('api',$1,$2,NULL,1,$3,1,'active',$4,$4,1)`, runtimeID, workspaceID, oldFingerprint, now); err != nil {
		t.Fatal(err)
	}

	migration := domain.WorkspaceBindingMigration{
		ID:                   foundation.ID(eventID),
		ControllerInstanceID: foundation.ID(controllerID),
		IdempotencyKey:       "root-rebind",
		WorkspaceID:          foundation.ID(workspaceID),
		CanonicalRoot:        root,
		OldRootFingerprint:   oldFingerprint,
		NewRootFingerprint:   newFingerprint,
	}
	result, err := repository.RebindWorkspace(ctx, migration)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Workspace.ID != foundation.ID(workspaceID) ||
		result.Workspace.RootFingerprint != newFingerprint || result.Workspace.BindingVersion != 2 ||
		result.Workspace.Version != 8 || result.Workspace.Availability != domain.WorkspaceAvailabilityAvailable {
		t.Fatalf("rebind result=%#v", result)
	}
	var runtimeFingerprint, runtimePhase string
	var runtimeBindingVersion, runtimeVersion, grantGeneration int64
	if err := pool.QueryRow(ctx, `SELECT root_fingerprint,binding_version,phase,version
	FROM ops.workspace_runtime WHERE role='api'`).Scan(
		&runtimeFingerprint, &runtimeBindingVersion, &runtimePhase, &runtimeVersion); err != nil {
		t.Fatal(err)
	}
	if runtimeFingerprint != oldFingerprint || runtimeBindingVersion != 1 ||
		runtimePhase != string(domain.RuntimePhaseUnavailable) || runtimeVersion != 2 {
		t.Fatalf("old runtime binding=(%s,%d,%s,%d)", runtimeFingerprint, runtimeBindingVersion, runtimePhase, runtimeVersion)
	}
	if err := pool.QueryRow(ctx, `SELECT grant_generation FROM ops.workspace_control_state WHERE singleton=true`).Scan(&grantGeneration); err != nil {
		t.Fatal(err)
	}
	if grantGeneration != 0 {
		t.Fatalf("rebind grant generation=%d want=0", grantGeneration)
	}
	assertWorkspaceRootRebindAudit(t, ctx, pool, workspaceID, controllerID, eventID, root,
		oldFingerprint, newFingerprint, 1, 2, 7, 8)
	assertWorkspaceRootRebindCounts(t, ctx, pool, workspaceID, 1, 1)
	_, err = repository.HeartbeatRuntime(ctx, application.RuntimeHeartbeat{
		Role: domain.RuntimeRoleAPI, InstanceID: foundation.ID(runtimeID), ExpectedRuntimeVersion: runtimeVersion,
	})
	assertWorkspaceRootGrantErrorOneOf(t, err, domain.ErrorCodeRuntimeBindingMismatch)
	_, err = repository.RegisterRuntime(ctx, application.RuntimeRegistration{
		Role: domain.RuntimeRoleAPI, InstanceID: foundation.ID(runtimeID), WorkspaceID: foundation.ID(workspaceID),
		GrantGeneration: 1, RootFingerprint: oldFingerprint, BindingVersion: 1, Phase: domain.RuntimePhaseUnavailable,
	})
	assertWorkspaceRootGrantErrorOneOf(t, err, domain.ErrorCodeRuntimeBindingMismatch)

	replay := migration
	replay.ID = "68100000-0000-4000-8000-000000000004"
	replay.IdempotencyKey = "root-rebind-response-loss"
	if _, err := pool.Exec(ctx, `UPDATE ops.runtime_mutation_gate
	SET owner_kind='model_settings',owner_id=$1,lease_expires_at=clock_timestamp()+interval '1 minute',
	    version=version+1,updated_at=clock_timestamp()
	WHERE singleton=true`, gateOwnerID); err != nil {
		t.Fatal(err)
	}
	_, err = repository.RebindWorkspace(ctx, replay)
	assertWorkspaceRootGrantErrorOneOf(t, err, domain.ErrorCodeBindingRebindConflict)
	if _, err := pool.Exec(ctx, `UPDATE ops.runtime_mutation_gate
	SET owner_kind=NULL,owner_id=NULL,lease_expires_at=NULL,version=version+1,updated_at=clock_timestamp()
	WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	replayed, err := repository.RebindWorkspace(ctx, replay)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Changed || replayed.Workspace.BindingVersion != 2 || replayed.Workspace.Version != 8 {
		t.Fatalf("replayed rebind=%#v", replayed)
	}
	assertWorkspaceRootRebindCounts(t, ctx, pool, workspaceID, 1, 1)
	_, err = pool.Exec(ctx, `UPDATE ops.workspace_root_binding_history
	SET idempotency_key='tampered' WHERE id=$1`, eventID)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `DELETE FROM ops.workspace_root_binding_history WHERE id=$1`, eventID)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `TRUNCATE ops.workspace_root_binding_history`)
	assertPostgresCode(t, err, "55000")

	const (
		auditFailureWorkspaceID = "68100000-0000-4000-8000-000000000010"
		auditFailureEventID     = "68100000-0000-4000-8000-000000000011"
		auditFailureRuntimeID   = "68100000-0000-4000-8000-000000000012"
		auditFailureRoot        = "/tmp/root-rebinding-audit-failure"
	)
	auditFailureOldFingerprint := strings.Repeat("c", 64)
	auditFailureNewFingerprint := strings.Repeat("d", 64)
	auditFailureObservedAt := now.Add(-time.Minute)
	insertWorkspaceRootRebindFixture(t, ctx, pool, auditFailureWorkspaceID, auditFailureRoot,
		auditFailureOldFingerprint, 5, auditFailureObservedAt)
	if _, err := pool.Exec(ctx, `INSERT INTO ops.workspace_runtime(
	role,instance_id,workspace_id,operation_id,grant_generation,root_fingerprint,binding_version,
	phase,started_at,heartbeat_at,version)
	VALUES('worker',$1,$2,NULL,1,$3,1,'active',$4,$4,1)`, auditFailureRuntimeID,
		auditFailureWorkspaceID, auditFailureOldFingerprint, auditFailureObservedAt); err != nil {
		t.Fatal(err)
	}
	injectedAuditFailure := errors.New("injected Workspace rebind audit failure")
	failingRepository, err := workspacepostgres.NewRepository(pool, workspacepostgres.WithAuditAppender(
		failAfterWorkspaceRootRebindAuditAppender{delegate: auditStore, err: injectedAuditFailure},
	))
	if err != nil {
		t.Fatal(err)
	}
	auditFailureWorkspaceBefore := workspaceRootRebindWorkspaceJSON(t, ctx, pool, auditFailureWorkspaceID)
	auditFailureRuntimeBefore := workspaceRootRebindRuntimeJSON(t, ctx, pool, string(domain.RuntimeRoleWorker))
	controlStateBeforeAuditFailure := workspaceRootRebindControlStateJSON(t, ctx, pool)
	_, err = failingRepository.RebindWorkspace(ctx, domain.WorkspaceBindingMigration{
		ID: foundation.ID(auditFailureEventID), ControllerInstanceID: foundation.ID(controllerID),
		IdempotencyKey: "root-rebind-audit-failure", WorkspaceID: foundation.ID(auditFailureWorkspaceID),
		CanonicalRoot: auditFailureRoot, OldRootFingerprint: auditFailureOldFingerprint,
		NewRootFingerprint: auditFailureNewFingerprint,
	})
	if !errors.Is(err, injectedAuditFailure) {
		t.Fatalf("audit failure rebind error=%v want injected failure", err)
	}
	if after := workspaceRootRebindWorkspaceJSON(t, ctx, pool, auditFailureWorkspaceID); after != auditFailureWorkspaceBefore {
		t.Fatalf("audit failure changed Workspace\nbefore=%s\nafter=%s", auditFailureWorkspaceBefore, after)
	}
	if after := workspaceRootRebindRuntimeJSON(t, ctx, pool, string(domain.RuntimeRoleWorker)); after != auditFailureRuntimeBefore {
		t.Fatalf("audit failure changed runtime\nbefore=%s\nafter=%s", auditFailureRuntimeBefore, after)
	}
	if after := workspaceRootRebindControlStateJSON(t, ctx, pool); after != controlStateBeforeAuditFailure {
		t.Fatalf("audit failure changed control state\nbefore=%s\nafter=%s", controlStateBeforeAuditFailure, after)
	}
	assertWorkspaceRootRebindCounts(t, ctx, pool, auditFailureWorkspaceID, 0, 0)

	const (
		gitCheckpointWorkspaceID = "68100000-0000-4000-8000-000000000020"
		gitCheckpointEventID     = "68100000-0000-4000-8000-000000000021"
		gitCheckpointRoot        = "/tmp/root-rebinding-git-checkpoint"
	)
	gitCheckpointOldFingerprint := strings.Repeat("e", 64)
	gitCheckpointNewFingerprint := strings.Repeat("f", 64)
	insertWorkspaceRootRebindFixture(t, ctx, pool, gitCheckpointWorkspaceID, gitCheckpointRoot,
		gitCheckpointOldFingerprint, 6, now.Add(2*time.Minute))
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace_git_capture_checkpoint(
	workspace_id,completed_head_oid,version,created_at,updated_at)
	VALUES($1,repeat('1',40),1,$2,$2)`, gitCheckpointWorkspaceID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	gitCheckpointWorkspaceBefore := workspaceRootRebindWorkspaceJSON(t, ctx, pool, gitCheckpointWorkspaceID)
	gitCheckpointBefore := workspaceRootRebindGitCheckpointJSON(t, ctx, pool, gitCheckpointWorkspaceID)
	controlStateBeforeGitCheckpoint := workspaceRootRebindControlStateJSON(t, ctx, pool)
	_, err = repository.RebindWorkspace(ctx, domain.WorkspaceBindingMigration{
		ID: foundation.ID(gitCheckpointEventID), ControllerInstanceID: foundation.ID(controllerID),
		IdempotencyKey: "root-rebind-git-checkpoint", WorkspaceID: foundation.ID(gitCheckpointWorkspaceID),
		CanonicalRoot: gitCheckpointRoot, OldRootFingerprint: gitCheckpointOldFingerprint,
		NewRootFingerprint: gitCheckpointNewFingerprint,
	})
	assertWorkspaceRootGrantErrorOneOf(t, err, domain.ErrorCodeBindingRebindConflict)
	if after := workspaceRootRebindWorkspaceJSON(t, ctx, pool, gitCheckpointWorkspaceID); after != gitCheckpointWorkspaceBefore {
		t.Fatalf("Git checkpoint rejection changed Workspace\nbefore=%s\nafter=%s", gitCheckpointWorkspaceBefore, after)
	}
	if after := workspaceRootRebindGitCheckpointJSON(t, ctx, pool, gitCheckpointWorkspaceID); after != gitCheckpointBefore {
		t.Fatalf("Git checkpoint rejection changed checkpoint\nbefore=%s\nafter=%s", gitCheckpointBefore, after)
	}
	if after := workspaceRootRebindControlStateJSON(t, ctx, pool); after != controlStateBeforeGitCheckpoint {
		t.Fatalf("Git checkpoint rejection changed control state\nbefore=%s\nafter=%s", controlStateBeforeGitCheckpoint, after)
	}
	assertWorkspaceRootRebindCounts(t, ctx, pool, gitCheckpointWorkspaceID, 0, 0)

	_, err = provider.DownTo(ctx, 80)
	assertPostgresCode(t, err, "55000")
}

func TestWorkspaceRootRebindingMigrationEmptyDownUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 81); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 80); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 81); err != nil {
		t.Fatal(err)
	}
}

type failAfterWorkspaceRootRebindAuditAppender struct {
	delegate auditapplication.Appender
	err      error
}

func (appender failAfterWorkspaceRootRebindAuditAppender) AppendTx(
	ctx context.Context,
	transaction any,
	event auditdomain.Event,
) (auditdomain.Event, bool, error) {
	created, replayed, err := appender.delegate.AppendTx(ctx, transaction, event)
	if err != nil {
		return auditdomain.Event{}, false, err
	}
	return created, replayed, appender.err
}

func insertWorkspaceRootRebindFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, root, fingerprint string,
	version int64,
	now time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
	id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
	status,availability,availability_reason,availability_checked_at,version,created_at,updated_at)
	VALUES($1,'rebind-fixture',$2,$3,1,$2,$4,'inactive','migration_required',
	'WORKSPACE_ROOT_IDENTITY_CHANGED',$4,$5,$4,$4)`, workspaceID, root, fingerprint, now, version); err != nil {
		t.Fatal(err)
	}
}

func assertWorkspaceRootRebindCounts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID string,
	wantHistory, wantAudit int,
) {
	t.Helper()
	var historyCount, auditCount int
	if err := pool.QueryRow(ctx, `SELECT
	(SELECT count(*) FROM ops.workspace_root_binding_history WHERE workspace_id=$1),
	(SELECT count(*) FROM ops.audit_event WHERE workspace_id=$1 AND action='workspace.root.rebound')`,
		workspaceID).Scan(&historyCount, &auditCount); err != nil {
		t.Fatal(err)
	}
	if historyCount != wantHistory || auditCount != wantAudit {
		t.Fatalf("Workspace %s rebind history=%d audit=%d want history=%d audit=%d",
			workspaceID, historyCount, auditCount, wantHistory, wantAudit)
	}
}

func assertWorkspaceRootRebindAudit(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, controllerID, eventID, root, oldFingerprint, newFingerprint string,
	oldBindingVersion, newBindingVersion, oldWorkspaceVersion, newWorkspaceVersion int64,
) {
	t.Helper()
	var (
		historyID, historyControllerID, historyIdempotencyKey, historyReason string
		auditID, persistedWorkspaceID, actorType, actorRef                   string
		action, resourceType, resourceRef, auditIdempotencyKey               string
		canonicalRoot, persistedOldFingerprint, persistedNewFingerprint      string
		correlation, payload                                                 string
		persistedOldBindingVersion, persistedNewBindingVersion               int64
		persistedOldWorkspaceVersion, persistedNewWorkspaceVersion           int64
		sameTimestamp, sameTransaction                                       bool
	)
	err := pool.QueryRow(ctx, `SELECT
	history.id::text,history.controller_instance_id::text,history.idempotency_key,history.reason,
	audit.id::text,audit.workspace_id::text,audit.actor_type,audit.actor_ref,
	audit.action,audit.resource_type,audit.resource_ref,audit.idempotency_key,
	history.canonical_root,history.old_root_fingerprint,history.new_root_fingerprint,
	history.old_binding_version,history.new_binding_version,
	history.old_workspace_version,history.new_workspace_version,
	audit.correlation::text,audit.payload::text,
	audit.occurred_at=history.created_at,audit.transaction_id=history.transaction_id
	FROM ops.workspace_root_binding_history AS history
	JOIN ops.audit_event AS audit ON audit.id=history.id
	WHERE history.workspace_id=$1`, workspaceID).Scan(
		&historyID, &historyControllerID, &historyIdempotencyKey, &historyReason,
		&auditID, &persistedWorkspaceID, &actorType, &actorRef,
		&action, &resourceType, &resourceRef, &auditIdempotencyKey,
		&canonicalRoot, &persistedOldFingerprint, &persistedNewFingerprint,
		&persistedOldBindingVersion, &persistedNewBindingVersion,
		&persistedOldWorkspaceVersion, &persistedNewWorkspaceVersion,
		&correlation, &payload, &sameTimestamp, &sameTransaction,
	)
	if err != nil {
		t.Fatal(err)
	}
	if historyID != eventID || historyControllerID != controllerID || historyIdempotencyKey != "root-rebind" ||
		historyReason != "WORKSPACE_ROOT_IDENTITY_CHANGED" || auditID != eventID || persistedWorkspaceID != workspaceID ||
		actorType != string(auditdomain.ActorSystem) || actorRef != controllerID ||
		action != "workspace.root.rebound" || resourceType != "workspace_root_binding" ||
		resourceRef != "workspace_root_binding:"+eventID || auditIdempotencyKey != "workspace.root.rebind:"+eventID ||
		canonicalRoot != root || persistedOldFingerprint != oldFingerprint || persistedNewFingerprint != newFingerprint ||
		persistedOldBindingVersion != oldBindingVersion || persistedNewBindingVersion != newBindingVersion ||
		persistedOldWorkspaceVersion != oldWorkspaceVersion || persistedNewWorkspaceVersion != newWorkspaceVersion ||
		!sameTimestamp || !sameTransaction {
		t.Fatalf("unexpected Workspace rebind history/audit binding: history=%s controller=%s key=%s reason=%s audit=%s workspace=%s actor=%s/%s action=%s resource=%s/%s audit_key=%s root=%s versions=%d/%d/%d/%d same_time=%t same_tx=%t",
			historyID, historyControllerID, historyIdempotencyKey, historyReason, auditID,
			persistedWorkspaceID, actorType, actorRef, action, resourceType, resourceRef,
			auditIdempotencyKey, canonicalRoot, persistedOldBindingVersion, persistedNewBindingVersion,
			persistedOldWorkspaceVersion, persistedNewWorkspaceVersion, sameTimestamp, sameTransaction)
	}
	var persistedCorrelation map[string]any
	if err := json.Unmarshal([]byte(correlation), &persistedCorrelation); err != nil {
		t.Fatal(err)
	}
	if len(persistedCorrelation) != 1 || persistedCorrelation["workspace_binding_history_id"] != eventID {
		t.Fatalf("unexpected rebind Audit correlation=%s", correlation)
	}
	var persistedPayload map[string]any
	if err := json.Unmarshal([]byte(payload), &persistedPayload); err != nil {
		t.Fatal(err)
	}
	expectedPayload := map[string]any{
		"old_root_fingerprint": oldFingerprint, "new_root_fingerprint": newFingerprint,
		"old_binding_version": float64(oldBindingVersion), "new_binding_version": float64(newBindingVersion),
		"old_workspace_version": float64(oldWorkspaceVersion), "new_workspace_version": float64(newWorkspaceVersion),
		"reason": "WORKSPACE_ROOT_IDENTITY_CHANGED", "audit_outcome": "SUCCEEDED",
		"audit_schema_version": auditdomain.SchemaVersion,
	}
	if len(persistedPayload) != len(expectedPayload) {
		t.Fatalf("unexpected rebind Audit payload=%s", payload)
	}
	for key, expected := range expectedPayload {
		if persistedPayload[key] != expected {
			t.Fatalf("rebind Audit payload[%s]=%v want=%v payload=%s", key, persistedPayload[key], expected, payload)
		}
	}
	for _, auditValue := range []string{actorRef, action, resourceType, resourceRef, auditIdempotencyKey, correlation, payload} {
		if strings.Contains(auditValue, root) {
			t.Fatalf("rebind Audit leaked canonical root %q in %q", root, auditValue)
		}
	}
	var auditDocument string
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(audit)::text FROM ops.audit_event AS audit WHERE id=$1`, eventID).Scan(&auditDocument); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditDocument, root) {
		t.Fatalf("rebind Audit row leaked canonical root %q: %s", root, auditDocument)
	}
}

func workspaceRootRebindWorkspaceJSON(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID string) string {
	t.Helper()
	return workspaceRootRebindRowJSON(t, ctx, pool, `SELECT to_jsonb(workspace)::text FROM core.workspace AS workspace WHERE id=$1`, workspaceID)
}

func workspaceRootRebindRuntimeJSON(t *testing.T, ctx context.Context, pool *pgxpool.Pool, role string) string {
	t.Helper()
	return workspaceRootRebindRowJSON(t, ctx, pool, `SELECT to_jsonb(runtime)::text FROM ops.workspace_runtime AS runtime WHERE role=$1`, role)
}

func workspaceRootRebindControlStateJSON(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	return workspaceRootRebindRowJSON(t, ctx, pool, `SELECT to_jsonb(state)::text FROM ops.workspace_control_state AS state WHERE singleton=true`)
}

func workspaceRootRebindGitCheckpointJSON(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID string) string {
	t.Helper()
	return workspaceRootRebindRowJSON(t, ctx, pool, `SELECT to_jsonb(checkpoint)::text FROM core.workspace_git_capture_checkpoint AS checkpoint WHERE workspace_id=$1`, workspaceID)
}

func workspaceRootRebindRowJSON(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, arguments ...any) string {
	t.Helper()
	var value string
	if err := pool.QueryRow(ctx, query, arguments...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
