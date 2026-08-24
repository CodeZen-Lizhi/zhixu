//go:build integration

package workflowpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRuntimeRepositoryStartReplayConflictRollbackAndLegacyGuard(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	outer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outer.Rollback(ctx) }()
	workspaceID := foundation.ID("a0000000-0000-4000-8000-000000000001")
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	if _, err := outer.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'M4A Runtime',$2,$2,$3,'active',1,$3,$3)`, string(workspaceID), "/tmp/m4a-runtime", now); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(outer, inserter)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeStartFixture(workspaceID, now)
	first, err := repository.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.Job.Duplicate || first.Job.JobID < 1 {
		t.Fatalf("first job=%#v", first.Job)
	}
	replay := request
	replay.Definition.ID = foundation.ID("b0000000-0000-4000-8000-000000000011")
	replay.Run.ID = foundation.ID("b0000000-0000-4000-8000-000000000012")
	replay.Run.DefinitionID = replay.Definition.ID
	replay.FirstNode.ID = foundation.ID("b0000000-0000-4000-8000-000000000013")
	replay.FirstNode.RunID = replay.Run.ID
	replay.Event.ID = foundation.ID("b0000000-0000-4000-8000-000000000014")
	replay.Event.RunID = &replay.Run.ID
	second, err := repository.Start(ctx, replay)
	if err != nil {
		t.Fatal(err)
	}
	if second.Run.ID != first.Run.ID || second.FirstNode.ID != first.FirstNode.ID || second.Job.JobID != first.Job.JobID || !second.Job.Duplicate || !second.Replayed {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	conflict := replay
	conflict.Run.RequestHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	conflict.RequestHash = conflict.Run.RequestHash
	if _, err := repository.Start(ctx, conflict); !hasCode(err, "WORKFLOW_START_IDEMPOTENCY_CONFLICT") {
		t.Fatalf("idempotency conflict=%v", err)
	}

	failingRepository, err := NewRuntimeRepository(outer, failingJobInserter{})
	if err != nil {
		t.Fatal(err)
	}
	failed := runtimeStartFixture(workspaceID, now.Add(time.Minute))
	remapRuntimeStartIDs(&failed, "c")
	failed.Definition.Key = "m4a-failed"
	failed.Run.IdempotencyKey = "m4a-failed"
	failed.Event.IdempotencyKey = "workflow-start:m4a-failed"
	failed.Event.EventKey = "workflow.run.started:m4a-failed"
	failed.FirstNode.IdempotencyKey = "node-start:m4a-failed"
	if _, err := failingRepository.Start(ctx, failed); err == nil {
		t.Fatal("job insertion failure was ignored")
	}
	var failedRuns int
	if err := outer.QueryRow(ctx, `SELECT count(*) FROM workflow.run WHERE id=$1`, string(failed.Run.ID)).Scan(&failedRuns); err != nil || failedRuns != 0 {
		t.Fatalf("failed start persisted run count=%d err=%v", failedRuns, err)
	}

	legacy := runtimeStartFixture(workspaceID, now.Add(2*time.Minute))
	remapRuntimeStartIDs(&legacy, "d")
	legacy.Definition.Key = "m4a-legacy"
	if _, err := outer.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,$3,1,$4,$5)`, string(legacy.Definition.ID), string(workspaceID), legacy.Definition.Key, legacy.Definition.Graph, now); err != nil {
		t.Fatal(err)
	}
	if _, err := outer.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'pending','{}',1,$4,$4)`, string(legacy.Run.ID), string(workspaceID), string(legacy.Definition.ID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Start(ctx, legacy); !hasCode(err, "WORKFLOW_LEGACY_RUNTIME_UNSUPPORTED") {
		t.Fatalf("legacy active error=%v", err)
	}
}

func TestRuntimeRepositoryConcurrentDuplicateCreatesOneRunNodeOutboxAndJob(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("e0000000-0000-4000-8000-000000000001")
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'M4A Concurrent',$2,$2,$3,'active',1,$3,$3)`, string(workspaceID), "/tmp/m4a-concurrent", now); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	firstRequest := runtimeStartFixture(workspaceID, now)
	secondRequest := firstRequest
	remapRuntimeStartIDs(&secondRequest, "f")
	type outcome struct {
		result application.RuntimeStartResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for _, request := range []application.RuntimeStartRequest{firstRequest, secondRequest} {
		request := request
		go func() {
			result, err := repository.Start(ctx, request)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	left, right := <-outcomes, <-outcomes
	if left.err != nil || right.err != nil {
		t.Fatalf("left=%#v right=%#v", left, right)
	}
	if left.result.Run.ID != right.result.Run.ID || left.result.FirstNode.ID != right.result.FirstNode.ID || left.result.Job.JobID != right.result.Job.JobID || left.result.Job.Duplicate == right.result.Job.Duplicate || left.result.Replayed == right.result.Replayed || left.result.Replayed != left.result.Job.Duplicate || right.result.Replayed != right.result.Job.Duplicate {
		t.Fatalf("left=%#v right=%#v", left.result, right.result)
	}
	var runs, nodes, events, jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.run WHERE workspace_id=$1 AND idempotency_key='m4a-runtime'`, string(workspaceID)).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.node_run WHERE run_id=$1 AND idempotency_key='node-start:m4a-runtime'`, string(left.result.Run.ID)).Scan(&nodes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.outbox_event WHERE workspace_id=$1 AND event_key='workflow.run.started:m4a-runtime'`, string(workspaceID)).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job WHERE kind=$1 AND args->>'node_run_id'=$2`, riveradapter.NodeJobKind, string(left.result.FirstNode.ID)).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || nodes != 1 || events != 1 || jobs != 1 {
		t.Fatalf("runs=%d nodes=%d events=%d jobs=%d", runs, nodes, events, jobs)
	}
}

func TestRuntimeRepositoryRecoversCommitResponseLoss(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("90000000-0000-4000-8000-000000000001")
	now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'M4A Commit Loss',$2,$2,$3,'active',1,$3,$3)`, string(workspaceID), "/tmp/m4a-commit-loss", now); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(commitResponseLossDB{pool: pool}, inserter)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeStartFixture(workspaceID, now)
	result, err := repository.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.ID == "" || result.FirstNode.ID == "" || result.Job.JobID < 1 || result.Replayed {
		t.Fatalf("recovered result=%#v", result)
	}
	replay := request
	remapRuntimeStartIDs(&replay, "7")
	replayed, err := repository.Start(ctx, replay)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || !replayed.Job.Duplicate || replayed.Run.ID != result.Run.ID || replayed.FirstNode.ID != result.FirstNode.ID || replayed.Job.JobID != result.Job.JobID {
		t.Fatalf("initial=%#v replayed=%#v", result, replayed)
	}
}

func TestRuntimeRepositoryStartTxUsesCallerTransactionAndReportsReplay(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("91000000-0000-4000-8000-000000000001")
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'M4A StartTx',$2,$2,$3,'active',1,$3,$3)`, string(workspaceID), "/tmp/m4a-start-tx", now); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	request := runtimeStartFixture(workspaceID, now)
	first, err := repository.StartTx(ctx, tx, request)
	if err != nil {
		t.Fatal(err)
	}
	replay := request
	remapRuntimeStartIDs(&replay, "8")
	second, err := repository.StartTx(ctx, tx, replay)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.Job.Duplicate || !second.Replayed || !second.Job.Duplicate || second.Run.ID != first.Run.ID || second.FirstNode.ID != first.FirstNode.ID || second.Job.JobID != first.Job.JobID {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	var inTransaction int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM workflow.run WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), request.Run.IdempotencyKey).Scan(&inTransaction); err != nil || inTransaction != 1 {
		t.Fatalf("in-transaction runs=%d err=%v", inTransaction, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var runs, jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.run WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), request.Run.IdempotencyKey).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job WHERE kind=$1 AND args->>'node_run_id'=$2`, riveradapter.NodeJobKind, string(first.FirstNode.ID)).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || jobs != 0 {
		t.Fatalf("caller rollback left runs=%d jobs=%d", runs, jobs)
	}
}

func TestGORMToolExecutionPolicyAndRecoveryFences(t *testing.T) {
	ctx := context.Background()
	platformPool, cleanup := newGORMRuntimeTestDatabase(t, ctx)
	defer cleanup()
	pool := platformPool.DB()
	workspaceID := foundation.ID("f1000000-0000-4000-8000-000000000001")
	otherWorkspaceID := foundation.ID("f2000000-0000-4000-8000-000000000001")
	for _, workspace := range []struct {
		id, name, path, status string
	}{
		{id: string(workspaceID), name: "gorm-tool-fence", path: "/tmp/gorm-tool-fence", status: "active"},
		{id: string(otherWorkspaceID), name: "gorm-tool-fence-other", path: "/tmp/gorm-tool-fence-other", status: "inactive"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
			VALUES($1,$2,$3,$3,CURRENT_TIMESTAMP,$4,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, workspace.id, workspace.name, workspace.path, workspace.status); err != nil {
			t.Fatal(err)
		}
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	primary := seedGORMToolFenceExecution(t, ctx, runtime, workspaceID, "gorm-tool-fence-primary", "5")
	other := seedGORMToolFenceExecution(t, ctx, runtime, workspaceID, "gorm-tool-fence-other", "6")
	policy, err := NewGORMToolExecutionPolicySnapshot(platformPool)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := NewGORMToolCallRecoveryFence(platformPool)
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork, err := platformPool.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	policyRequest := application.ToolExecutionPolicySnapshotRequest{
		WorkspaceID: workspaceID, WorkflowRunID: primary.started.Run.ID,
		NodeRunID: primary.started.FirstNode.ID, NodeAttemptID: primary.claimed.Attempt.ID,
	}
	recoveryRequest := application.ToolCallRecoveryFenceRequest{
		WorkspaceID: workspaceID, WorkflowRunID: primary.started.Run.ID,
		NodeRunID: primary.started.FirstNode.ID, NodeAttemptID: primary.claimed.Attempt.ID,
	}

	t.Run("policy returns complete snapshot and holds shared locks", func(t *testing.T) {
		err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			transaction, unwrapErr := platformpostgres.GORMTransaction(scope)
			if unwrapErr != nil {
				return unwrapErr
			}
			var databaseBefore, databaseAfter time.Time
			if scanErr := transaction.Raw(`SELECT clock_timestamp()`).Row().Scan(&databaseBefore); scanErr != nil {
				return scanErr
			}
			snapshot, found, lockErr := policy.LockToolExecutionPolicyScoped(callbackCtx, scope, policyRequest)
			if lockErr != nil {
				return lockErr
			}
			if scanErr := transaction.Raw(`SELECT clock_timestamp()`).Row().Scan(&databaseAfter); scanErr != nil {
				return scanErr
			}
			if !found || snapshot.DefinitionID != primary.request.Definition.ID || snapshot.DefinitionKey != primary.request.Definition.Key ||
				snapshot.DefinitionVersion != primary.request.Definition.Version || strings.TrimSpace(snapshot.DefinitionGraph) == "" ||
				snapshot.WorkspaceID != workspaceID || snapshot.WorkflowRunID != primary.started.Run.ID || snapshot.WorkflowStatus != domain.RunStatusRunning ||
				snapshot.PauseRequested || snapshot.CancelRequested || snapshot.NodeRunID != primary.started.FirstNode.ID ||
				snapshot.NodeKey != primary.started.FirstNode.NodeKey || snapshot.NodeKind != primary.started.FirstNode.NodeType ||
				snapshot.NodeStatus != domain.NodeStatusRunning || snapshot.NodeAttempt != primary.claimed.Attempt.AttemptNo ||
				snapshot.NodeAttemptID != primary.claimed.Attempt.ID || snapshot.AttemptStatus != domain.AttemptStatusRunning ||
				snapshot.AttemptNo != primary.claimed.Attempt.AttemptNo || !snapshot.NodeLeaseOwnerSet || !snapshot.AttemptLeaseOwnerSet ||
				snapshot.NodeLeaseOwner != primary.claimed.Attempt.LeaseOwner || snapshot.AttemptLeaseOwner != primary.claimed.Attempt.LeaseOwner ||
				!snapshot.NodeLeaseUntilSet || !snapshot.AttemptLeaseUntilSet || !snapshot.NodeLeaseUntil.Equal(snapshot.AttemptLeaseUntil) ||
				!snapshot.NodeLeaseUntil.After(snapshot.DatabaseNow) {
				t.Fatalf("policy snapshot found=%t snapshot=%+v", found, snapshot)
			}
			if snapshot.DatabaseNow.Before(databaseBefore) || snapshot.DatabaseNow.After(databaseAfter) {
				t.Fatalf("database time before=%s snapshot=%s after=%s", databaseBefore, snapshot.DatabaseNow, databaseAfter)
			}
			for _, locked := range []struct {
				name, query, id string
			}{
				{name: "definition", query: `SELECT 1 FROM workflow.definition WHERE id=$1 FOR UPDATE NOWAIT`, id: string(primary.request.Definition.ID)},
				{name: "run", query: `SELECT 1 FROM workflow.run WHERE id=$1 FOR UPDATE NOWAIT`, id: string(primary.started.Run.ID)},
				{name: "node", query: `SELECT 1 FROM workflow.node_run WHERE id=$1 FOR UPDATE NOWAIT`, id: string(primary.started.FirstNode.ID)},
				{name: "attempt", query: `SELECT 1 FROM workflow.node_attempt WHERE id=$1 FOR UPDATE NOWAIT`, id: string(primary.claimed.Attempt.ID)},
			} {
				t.Run(locked.name, func(t *testing.T) {
					assertPGXWorkflowRowLockUnavailable(t, callbackCtx, pool, locked.query, locked.id)
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("binding mismatches are absent", func(t *testing.T) {
		mismatches := []struct {
			name     string
			policy   application.ToolExecutionPolicySnapshotRequest
			recovery application.ToolCallRecoveryFenceRequest
		}{
			{name: "workspace", policy: policyRequest, recovery: recoveryRequest},
			{name: "run", policy: policyRequest, recovery: recoveryRequest},
			{name: "node", policy: policyRequest, recovery: recoveryRequest},
			{name: "attempt", policy: policyRequest, recovery: recoveryRequest},
		}
		mismatches[0].policy.WorkspaceID, mismatches[0].recovery.WorkspaceID = otherWorkspaceID, otherWorkspaceID
		mismatches[1].policy.WorkflowRunID, mismatches[1].recovery.WorkflowRunID = other.started.Run.ID, other.started.Run.ID
		mismatches[2].policy.NodeRunID, mismatches[2].recovery.NodeRunID = other.started.FirstNode.ID, other.started.FirstNode.ID
		mismatches[3].policy.NodeAttemptID, mismatches[3].recovery.NodeAttemptID = other.claimed.Attempt.ID, other.claimed.Attempt.ID
		for _, mismatch := range mismatches {
			mismatch := mismatch
			t.Run(mismatch.name, func(t *testing.T) {
				err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
					if snapshot, found, policyErr := policy.LockToolExecutionPolicyScoped(callbackCtx, scope, mismatch.policy); policyErr != nil || found || snapshot != (application.ToolExecutionPolicySnapshot{}) {
						t.Fatalf("policy mismatch snapshot=%+v found=%t err=%v", snapshot, found, policyErr)
					}
					if result, recoveryErr := recovery.LockToolCallRecoveryScoped(callbackCtx, scope, mismatch.recovery); recoveryErr != nil || result != (application.ToolCallRecoveryFenceResult{}) {
						t.Fatalf("recovery mismatch result=%+v err=%v", result, recoveryErr)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	})

	t.Run("recovery returns healthy facts and locks only node and attempt", func(t *testing.T) {
		err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			result, lockErr := recovery.LockToolCallRecoveryScoped(callbackCtx, scope, recoveryRequest)
			if lockErr != nil {
				return lockErr
			}
			if !result.Found || result.Skipped || result.Stale || result.DatabaseNow.IsZero() ||
				result.WorkflowStatus != domain.RunStatusRunning || result.NodeStatus != domain.NodeStatusRunning ||
				result.AttemptStatus != domain.AttemptStatusRunning || result.NodeAttempt != result.AttemptNo ||
				!result.NodeLeaseUntil.After(result.DatabaseNow) {
				t.Fatalf("healthy recovery result=%+v", result)
			}
			assertPGXWorkflowRowLockAvailable(t, callbackCtx, pool, `SELECT 1 FROM workflow.run WHERE id=$1 FOR UPDATE NOWAIT`, string(primary.started.Run.ID))
			assertPGXWorkflowRowLockUnavailable(t, callbackCtx, pool, `SELECT 1 FROM workflow.node_run WHERE id=$1 FOR UPDATE NOWAIT`, string(primary.started.FirstNode.ID))
			assertPGXWorkflowRowLockUnavailable(t, callbackCtx, pool, `SELECT 1 FROM workflow.node_attempt WHERE id=$1 FOR UPDATE NOWAIT`, string(primary.claimed.Attempt.ID))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("recovery distinguishes skipped node and attempt", func(t *testing.T) {
		for _, locked := range []struct {
			name, query, id string
		}{
			{name: "node", query: `SELECT id::text FROM workflow.node_run WHERE id=$1 FOR UPDATE`, id: string(primary.started.FirstNode.ID)},
			{name: "attempt", query: `SELECT id::text FROM workflow.node_attempt WHERE id=$1 FOR UPDATE`, id: string(primary.claimed.Attempt.ID)},
		} {
			locked := locked
			t.Run(locked.name, func(t *testing.T) {
				release := lockPGXWorkflowRow(t, ctx, pool, locked.query, locked.id)
				defer release()
				err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
					result, recoveryErr := recovery.LockToolCallRecoveryScoped(callbackCtx, scope, recoveryRequest)
					if recoveryErr != nil {
						return recoveryErr
					}
					if !result.Found || !result.Skipped || result.Stale || !result.DatabaseNow.IsZero() {
						t.Fatalf("skipped recovery result=%+v", result)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	})

	t.Run("recovery marks an expired matched lease stale", func(t *testing.T) {
		tx, beginErr := pool.Begin(ctx)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if _, updateErr := tx.Exec(ctx, `UPDATE workflow.node_run SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, string(primary.started.FirstNode.ID)); updateErr != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(updateErr)
		}
		if _, updateErr := tx.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=(SELECT lease_until FROM workflow.node_run WHERE id=$2) WHERE id=$1`, string(primary.claimed.Attempt.ID), string(primary.started.FirstNode.ID)); updateErr != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(updateErr)
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			t.Fatal(commitErr)
		}
		err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			result, recoveryErr := recovery.LockToolCallRecoveryScoped(callbackCtx, scope, recoveryRequest)
			if recoveryErr != nil {
				return recoveryErr
			}
			if !result.Found || result.Skipped || !result.Stale || result.NodeLeaseUntil.After(result.DatabaseNow) {
				t.Fatalf("stale recovery result=%+v", result)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("scope context and SQLSTATE contracts", func(t *testing.T) {
		if _, _, err := policy.LockToolExecutionPolicyScoped(nil, nil, policyRequest); !hasCode(err, "WORKFLOW_TOOL_EXECUTION_POLICY_INVALID") {
			t.Fatalf("nil policy context error=%v", err)
		}
		if _, err := recovery.LockToolCallRecoveryScoped(nil, nil, recoveryRequest); !hasCode(err, "WORKFLOW_TOOL_CALL_RECOVERY_FENCE_INVALID") {
			t.Fatalf("nil recovery context error=%v", err)
		}
		invalidPolicyRequest := policyRequest
		invalidPolicyRequest.NodeRunID = foundation.ID("invalid")
		if _, _, err := policy.LockToolExecutionPolicyScoped(ctx, nil, invalidPolicyRequest); !hasCode(err, "WORKFLOW_TOOL_EXECUTION_POLICY_INVALID") {
			t.Fatalf("invalid policy request error=%v", err)
		}
		invalidRecoveryRequest := recoveryRequest
		invalidRecoveryRequest.NodeAttemptID = foundation.ID("invalid")
		if _, err := recovery.LockToolCallRecoveryScoped(ctx, nil, invalidRecoveryRequest); !hasCode(err, "WORKFLOW_TOOL_CALL_RECOVERY_FENCE_INVALID") {
			t.Fatalf("invalid recovery request error=%v", err)
		}
		if _, _, err := policy.LockToolExecutionPolicyScoped(ctx, foreignWorkflowTransactionScope{}, policyRequest); err == nil {
			t.Fatal("foreign policy scope was accepted")
		} else {
			assertGORMWorkflowError(t, err, foundation.ErrorDependencyUnavailable, "WORKFLOW_TOOL_EXECUTION_POLICY_UNAVAILABLE", true)
		}
		if _, err := recovery.LockToolCallRecoveryScoped(ctx, foreignWorkflowTransactionScope{}, recoveryRequest); err == nil {
			t.Fatal("foreign recovery scope was accepted")
		} else {
			assertGORMWorkflowError(t, err, foundation.ErrorDependencyUnavailable, "WORKFLOW_TOOL_CALL_RECOVERY_FENCE_UNAVAILABLE", true)
		}

		var staleScope foundation.TransactionScope
		if err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error {
			staleScope = scope
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := policy.LockToolExecutionPolicyScoped(ctx, staleScope, policyRequest); err == nil {
			t.Fatal("stale policy scope was accepted")
		} else {
			assertGORMWorkflowError(t, err, foundation.ErrorDependencyUnavailable, "WORKFLOW_TOOL_EXECUTION_POLICY_UNAVAILABLE", true)
		}
		if _, err := recovery.LockToolCallRecoveryScoped(ctx, staleScope, recoveryRequest); err == nil {
			t.Fatal("stale recovery scope was accepted")
		} else {
			assertGORMWorkflowError(t, err, foundation.ErrorDependencyUnavailable, "WORKFLOW_TOOL_CALL_RECOVERY_FENCE_UNAVAILABLE", true)
		}

		cancelCause := errors.New("tool fence caller stopped")
		canceledCtx, cancel := context.WithCancelCause(ctx)
		cancel(cancelCause)
		cancelErr := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error {
			_, _, policyErr := policy.LockToolExecutionPolicyScoped(canceledCtx, scope, policyRequest)
			return policyErr
		})
		assertGORMWorkflowError(t, cancelErr, foundation.ErrorNonRetryableFailure, "WORKFLOW_TOOL_EXECUTION_POLICY_UNAVAILABLE", false)
		if !errors.Is(cancelErr, context.Canceled) || !errors.Is(cancelErr, cancelCause) {
			t.Fatalf("policy cancellation cause=%v", cancelErr)
		}
		recoveryCancelCause := errors.New("tool recovery caller stopped")
		recoveryCanceledCtx, cancelRecovery := context.WithCancelCause(ctx)
		cancelRecovery(recoveryCancelCause)
		recoveryCancelErr := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error {
			_, recoveryErr := recovery.LockToolCallRecoveryScoped(recoveryCanceledCtx, scope, recoveryRequest)
			return recoveryErr
		})
		assertGORMWorkflowError(t, recoveryCancelErr, foundation.ErrorNonRetryableFailure, "WORKFLOW_TOOL_CALL_RECOVERY_FENCE_UNAVAILABLE", false)
		if !errors.Is(recoveryCancelErr, context.Canceled) || !errors.Is(recoveryCancelErr, recoveryCancelCause) {
			t.Fatalf("recovery cancellation cause=%v", recoveryCancelErr)
		}

		release := lockPGXWorkflowRow(t, ctx, pool, `SELECT id::text FROM workflow.node_run WHERE id=$1 FOR UPDATE`, string(primary.started.FirstNode.ID))
		defer release()
		lockErr := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			transaction, unwrapErr := platformpostgres.GORMTransaction(scope)
			if unwrapErr != nil {
				return unwrapErr
			}
			if execErr := transaction.Exec(`SET LOCAL lock_timeout = '100ms'`).Error; execErr != nil {
				return execErr
			}
			_, _, policyErr := policy.LockToolExecutionPolicyScoped(callbackCtx, scope, policyRequest)
			return policyErr
		})
		assertGORMWorkflowError(t, lockErr, foundation.ErrorDependencyUnavailable, "WORKFLOW_TOOL_EXECUTION_POLICY_UNAVAILABLE", true)
		var postgresError *pgconn.PgError
		if !errors.As(lockErr, &postgresError) || postgresError.Code != "55P03" {
			t.Fatalf("lock SQLSTATE error=%v postgres=%+v", lockErr, postgresError)
		}
	})
}

type failingJobInserter struct{}

type commitResponseLossDB struct{ pool *pgxpool.Pool }

func (d commitResponseLossDB) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	return d.pool.QueryRow(ctx, sql, arguments...)
}

func (d commitResponseLossDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return commitResponseLossTx{Tx: tx}, nil
}

type commitResponseLossTx struct{ pgx.Tx }

func (tx commitResponseLossTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("injected commit response loss")
}

func (failingJobInserter) InsertTx(context.Context, any, riveradapter.NodeJobArgs, riveradapter.InsertOptions) (application.JobReceipt, error) {
	return application.JobReceipt{}, foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_JOB_INSERT_FAILED", true, errors.New("injected job failure"))
}

func runtimeStartFixture(workspaceID foundation.ID, now time.Time) application.RuntimeStartRequest {
	definitionID := foundation.ID("a0000000-0000-4000-8000-000000000011")
	runID := foundation.ID("a0000000-0000-4000-8000-000000000012")
	nodeID := foundation.ID("a0000000-0000-4000-8000-000000000013")
	eventID := foundation.ID("a0000000-0000-4000-8000-000000000014")
	graph := json.RawMessage(`{"nodes":[]}`)
	runIDCopy := runID
	return application.RuntimeStartRequest{
		Definition:                   domain.Definition{ID: definitionID, WorkspaceID: workspaceID, Key: "m4a-runtime", Version: 1, Graph: graph, CreatedAt: now},
		DefinitionGraphHash:          "1111111111111111111111111111111111111111111111111111111111111111",
		DefinitionInputSchemaVersion: 1,
		Run:                          domain.Run{ID: runID, WorkspaceID: workspaceID, DefinitionID: definitionID, Status: domain.StatusPending, Input: json.RawMessage(`{"value":1}`), IdempotencyKey: "m4a-runtime", RequestHash: "2222222222222222222222222222222222222222222222222222222222222222", Version: 1, CreatedAt: now, UpdatedAt: now},
		FirstNode:                    domain.NodeRun{ID: nodeID, RunID: runID, NodeKey: "hash", NodeType: application.CanonicalJSONHashNodeKind, Status: domain.StatusPending, Input: json.RawMessage(`{"value":1}`), IdempotencyKey: "node-start:m4a-runtime", InputSchemaVersion: 1, OutputSchemaVersion: 1, DispatchNo: 1, Version: 1, CreatedAt: now, UpdatedAt: now},
		Event:                        domain.OutboxEvent{ID: eventID, WorkspaceID: workspaceID, RunID: &runIDCopy, Type: "workflow.run.started", IdempotencyKey: "workflow-start:m4a-runtime", EventKey: "workflow.run.started:m4a-runtime", SchemaVersion: 1, EventVersion: 1, Payload: json.RawMessage(`{}`), OccurredAt: now},
		RequestHash:                  "2222222222222222222222222222222222222222222222222222222222222222",
	}
}

var _ riveradapter.JobInserter = failingJobInserter{}

func remapRuntimeStartIDs(request *application.RuntimeStartRequest, prefix string) {
	request.Definition.ID = foundation.ID(prefix + string(request.Definition.ID)[1:])
	request.Run.ID = foundation.ID(prefix + string(request.Run.ID)[1:])
	request.Run.DefinitionID = request.Definition.ID
	request.FirstNode.ID = foundation.ID(prefix + string(request.FirstNode.ID)[1:])
	request.FirstNode.RunID = request.Run.ID
	request.Event.ID = foundation.ID(prefix + string(request.Event.ID)[1:])
	runID := request.Run.ID
	request.Event.RunID = &runID
}

func newRuntimeTestDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	t.Helper()
	platformPool, cleanup := newRuntimeTestPlatformDatabase(t, ctx)
	return platformPool.DB(), cleanup
}

func newGORMRuntimeTestDatabase(t *testing.T, ctx context.Context) (*platformpostgres.Pool, func()) {
	t.Helper()
	return newRuntimeTestPlatformDatabase(t, ctx)
}

func newRuntimeTestPlatformDatabase(t *testing.T, ctx context.Context) (*platformpostgres.Pool, func()) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Fatal("ZHIXU_TEST_DATABASE_URL is required for Runtime Start integration tests")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_runtime_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	databaseURL := parsed.String()
	migrationPool, err := platformpostgres.OpenMigration(ctx, databaseURL, 4, 0)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier)
		admin.Close()
		t.Fatal(err)
	}
	runner, err := platformmigration.NewRunner(migrationPool.DB(), projectmigrations.FS)
	if err != nil {
		migrationPool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		migrationPool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	migrationPool.Close()
	platformPool, err := platformpostgres.Open(ctx, databaseURL, 8, 0)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	if err := platformPool.Ping(ctx); err != nil {
		platformPool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	return platformPool, func() {
		platformPool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	}
}

type gormToolFenceExecution struct {
	request application.RuntimeStartRequest
	started application.RuntimeStartResult
	claimed application.ClaimResult
}

func seedGORMToolFenceExecution(
	t *testing.T,
	ctx context.Context,
	runtime *RuntimeRepository,
	workspaceID foundation.ID,
	key string,
	idPrefix string,
) gormToolFenceExecution {
	t.Helper()
	request := runtimeStateStartFixture(workspaceID, key, domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Second})
	remapRuntimeStartIDs(&request, idPrefix)
	started, err := runtime.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := runtime.Claim(ctx, application.ClaimCommand{
		NodeRunID: started.FirstNode.ID, DispatchNo: 1,
		DeliveryID: "tool-fence-" + key, RiverJobID: started.Job.JobID,
		LeaseOwner: "tool-fence-worker", LeaseDuration: time.Minute,
	})
	if err != nil || claimed.Disposition != application.ClaimDispositionClaimed {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	return gormToolFenceExecution{request: request, started: started, claimed: claimed}
}

type foreignWorkflowTransactionScope struct{}

func (foreignWorkflowTransactionScope) TransactionScope() {}

func lockPGXWorkflowRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, arguments ...any) func() {
	t.Helper()
	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := transaction.QueryRow(ctx, query, arguments...).Scan(&id); err != nil {
		_ = transaction.Rollback(ctx)
		t.Fatal(err)
	}
	return func() { _ = transaction.Rollback(ctx) }
}

func assertPGXWorkflowRowLockUnavailable(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, arguments ...any) {
	t.Helper()
	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var marker int
	err = transaction.QueryRow(ctx, query, arguments...).Scan(&marker)
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "55P03" {
		t.Fatalf("row lock error=%v postgres=%+v", err, postgresError)
	}
}

func assertPGXWorkflowRowLockAvailable(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, arguments ...any) {
	t.Helper()
	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var marker int
	if err := transaction.QueryRow(ctx, query, arguments...).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("available row lock marker=%d err=%v", marker, err)
	}
}
