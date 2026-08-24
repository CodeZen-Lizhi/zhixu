//go:build integration

package workflowpostgres

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestGORMWorkspaceAnalysisExecutionFenceLocksExecutionWithPostgres(t *testing.T) {
	ctx := context.Background()
	platformPool, cleanup := newGORMRuntimeTestDatabase(t, ctx)
	defer cleanup()
	pool := platformPool.DB()
	workspaceID := foundation.ID("f3000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'gorm-execution-fence','/tmp/gorm-execution-fence','/tmp/gorm-execution-fence',CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
		string(workspaceID)); err != nil {
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
	runtime, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	primary := seedGORMToolFenceExecution(t, ctx, runtime, workspaceID, "gorm-execution-fence-primary", "9")
	other := seedGORMToolFenceExecution(t, ctx, runtime, workspaceID, "gorm-execution-fence-other", "a")
	fence, err := NewGORMWorkspaceAnalysisExecutionFence(platformPool)
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork, err := platformPool.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	request := application.WorkspaceAnalysisExecutionFenceRequest{
		WorkspaceID: workspaceID, WorkflowRunID: primary.started.Run.ID,
		NodeRunID: primary.started.FirstNode.ID, NodeAttemptID: primary.claimed.Attempt.ID,
	}

	t.Run("snapshot and run node attempt lock order", func(t *testing.T) {
		err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			snapshot, found, lockErr := fence.LockWorkspaceAnalysisExecutionScoped(callbackCtx, scope, request)
			if lockErr != nil {
				return lockErr
			}
			if !found || snapshot.WorkspaceID != workspaceID || snapshot.WorkflowRunID != primary.started.Run.ID ||
				snapshot.NodeRunID != primary.started.FirstNode.ID || snapshot.NodeAttemptID != primary.claimed.Attempt.ID ||
				snapshot.DefinitionID != primary.started.Run.DefinitionID || snapshot.DefinitionKey == "" || snapshot.DefinitionGraph == "" ||
				snapshot.WorkflowStatus != primary.claimed.Run.Status || snapshot.NodeStatus != primary.claimed.Node.Status ||
				snapshot.AttemptStatus != primary.claimed.Attempt.Status || snapshot.NodeKey != primary.started.FirstNode.NodeKey ||
				snapshot.NodeAttempt != snapshot.AttemptNo || !snapshot.NodeLeaseOwnerSet || !snapshot.NodeLeaseUntilSet ||
				!snapshot.AttemptLeaseOwnerSet || !snapshot.AttemptLeaseUntilSet ||
				snapshot.NodeLeaseOwner != snapshot.AttemptLeaseOwner || !snapshot.NodeLeaseUntil.Equal(snapshot.AttemptLeaseUntil) {
				t.Fatalf("snapshot=%+v found=%v", snapshot, found)
			}
			assertPGXWorkflowRowLockUnavailable(t, callbackCtx, pool, `SELECT 1 FROM workflow.run WHERE id=$1 FOR UPDATE NOWAIT`, string(request.WorkflowRunID))
			assertPGXWorkflowRowLockUnavailable(t, callbackCtx, pool, `SELECT 1 FROM workflow.node_run WHERE id=$1 FOR UPDATE NOWAIT`, string(request.NodeRunID))
			assertPGXWorkflowRowLockUnavailable(t, callbackCtx, pool, `SELECT 1 FROM workflow.node_attempt WHERE id=$1 FOR UPDATE NOWAIT`, string(request.NodeAttemptID))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		assertPGXWorkflowRowLockAvailable(t, ctx, pool, `SELECT 1 FROM workflow.run WHERE id=$1 FOR UPDATE NOWAIT`, string(request.WorkflowRunID))
		assertPGXWorkflowRowLockAvailable(t, ctx, pool, `SELECT 1 FROM workflow.node_run WHERE id=$1 FOR UPDATE NOWAIT`, string(request.NodeRunID))
		assertPGXWorkflowRowLockAvailable(t, ctx, pool, `SELECT 1 FROM workflow.node_attempt WHERE id=$1 FOR UPDATE NOWAIT`, string(request.NodeAttemptID))
	})

	t.Run("binding mismatches are absent", func(t *testing.T) {
		mismatches := []struct {
			name    string
			request application.WorkspaceAnalysisExecutionFenceRequest
		}{
			{name: "workspace", request: application.WorkspaceAnalysisExecutionFenceRequest{WorkspaceID: foundation.ID("f4000000-0000-4000-8000-000000000001"), WorkflowRunID: request.WorkflowRunID, NodeRunID: request.NodeRunID, NodeAttemptID: request.NodeAttemptID}},
			{name: "run", request: application.WorkspaceAnalysisExecutionFenceRequest{WorkspaceID: workspaceID, WorkflowRunID: other.started.Run.ID, NodeRunID: request.NodeRunID, NodeAttemptID: request.NodeAttemptID}},
			{name: "node", request: application.WorkspaceAnalysisExecutionFenceRequest{WorkspaceID: workspaceID, WorkflowRunID: request.WorkflowRunID, NodeRunID: other.started.FirstNode.ID, NodeAttemptID: request.NodeAttemptID}},
			{name: "attempt", request: application.WorkspaceAnalysisExecutionFenceRequest{WorkspaceID: workspaceID, WorkflowRunID: request.WorkflowRunID, NodeRunID: request.NodeRunID, NodeAttemptID: other.claimed.Attempt.ID}},
		}
		for _, mismatch := range mismatches {
			mismatch := mismatch
			t.Run(mismatch.name, func(t *testing.T) {
				err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
					snapshot, found, lockErr := fence.LockWorkspaceAnalysisExecutionScoped(callbackCtx, scope, mismatch.request)
					if lockErr != nil {
						return lockErr
					}
					if found || snapshot != (application.WorkspaceAnalysisExecutionFenceSnapshot{}) {
						t.Fatalf("snapshot=%+v found=%v", snapshot, found)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	})

	t.Run("scope and SQLSTATE contracts", func(t *testing.T) {
		if _, _, err := fence.LockWorkspaceAnalysisExecutionScoped(nil, nil, request); !hasCode(err, "WORKFLOW_WORKSPACE_ANALYSIS_EXECUTION_FENCE_INVALID") {
			t.Fatalf("nil context error=%v", err)
		}
		if _, _, err := fence.LockWorkspaceAnalysisExecutionScoped(ctx, foreignWorkflowTransactionScope{}, request); err == nil {
			t.Fatal("foreign scope was accepted")
		} else {
			assertGORMWorkflowError(t, err, foundation.ErrorDependencyUnavailable, "WORKFLOW_WORKSPACE_ANALYSIS_EXECUTION_FENCE_UNAVAILABLE", true)
		}
		var staleScope foundation.TransactionScope
		if err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error {
			staleScope = scope
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := fence.LockWorkspaceAnalysisExecutionScoped(ctx, staleScope, request); err == nil {
			t.Fatal("stale scope was accepted")
		}
		cancelCause := errors.New("workspace execution fence caller stopped")
		canceledCtx, cancel := context.WithCancelCause(ctx)
		cancel(cancelCause)
		cancelErr := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error {
			_, _, fenceErr := fence.LockWorkspaceAnalysisExecutionScoped(canceledCtx, scope, request)
			return fenceErr
		})
		assertGORMWorkflowError(t, cancelErr, foundation.ErrorNonRetryableFailure, "WORKFLOW_WORKSPACE_ANALYSIS_EXECUTION_FENCE_UNAVAILABLE", false)
		if !errors.Is(cancelErr, context.Canceled) || !errors.Is(cancelErr, cancelCause) {
			t.Fatalf("cancellation cause=%v", cancelErr)
		}

		release := lockPGXWorkflowRow(t, ctx, pool, `SELECT id::text FROM workflow.run WHERE id=$1 FOR UPDATE`, string(request.WorkflowRunID))
		defer release()
		lockErr := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			transaction, unwrapErr := platformpostgres.GORMTransaction(scope)
			if unwrapErr != nil {
				return unwrapErr
			}
			if execErr := transaction.Exec(`SET LOCAL lock_timeout = '100ms'`).Error; execErr != nil {
				return execErr
			}
			_, _, fenceErr := fence.LockWorkspaceAnalysisExecutionScoped(callbackCtx, scope, request)
			return fenceErr
		})
		assertGORMWorkflowError(t, lockErr, foundation.ErrorDependencyUnavailable, "WORKFLOW_WORKSPACE_ANALYSIS_EXECUTION_FENCE_UNAVAILABLE", true)
		var postgresError *pgconn.PgError
		if !errors.As(lockErr, &postgresError) || postgresError.Code != "55P03" {
			t.Fatalf("lock SQLSTATE error=%v postgres=%+v", lockErr, postgresError)
		}
	})
}
