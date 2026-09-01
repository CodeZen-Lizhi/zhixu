//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestWorkspaceAnalysisConcurrentModelAuthorizationKeepsOneBudgetReservation
// exercises real PostgreSQL row locking for duplicate River delivery. One
// delivery may create the canonical model operation and the other must only
// reconcile it; neither may reserve a second model/input/output budget.
func TestWorkspaceAnalysisConcurrentModelAuthorizationKeepsOneBudgetReservation(t *testing.T) {
	testWorkspaceAnalysisModelRepositoryIntegrationVariants(t, testWorkspaceAnalysisConcurrentModelAuthorizationKeepsOneBudgetReservation)
}

func testWorkspaceAnalysisConcurrentModelAuthorizationKeepsOneBudgetReservation(
	t *testing.T,
	pool *pgxpool.Pool,
	ctx context.Context,
	repository workspaceAnalysisModelRepositoryIntegrationStore,
) {
	fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)

	start := make(chan struct{})
	results := make(chan workspaceAnalysisConcurrentAuthorizationResult, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			callCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			<-start
			result, authorizeErr := repository.AuthorizeWorkspaceAnalysisModelCall(callCtx, fixture.command)
			results <- workspaceAnalysisConcurrentAuthorizationResult{result: result, err: authorizeErr}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	created, reconciled := 0, 0
	for outcome := range results {
		if outcome.err != nil {
			t.Fatalf("concurrent model authorization: %v", outcome.err)
		}
		switch outcome.result.Disposition {
		case application.WorkspaceAnalysisModelAuthorizationCreated:
			created++
		case application.WorkspaceAnalysisModelAuthorizationReconcile:
			reconciled++
		default:
			t.Fatalf("unexpected concurrent authorization disposition=%s", outcome.result.Disposition)
		}
		if outcome.result.OperationID != fixture.command.OperationID || outcome.result.ReservationID != fixture.command.ReservationID ||
			outcome.result.Run.ID != fixture.command.Run.ID || outcome.result.Call.ID != fixture.command.Call.ID {
			t.Fatalf("concurrent authorization returned a different durable binding: %+v", outcome.result)
		}
	}
	if created != 1 || reconciled != 1 {
		t.Fatalf("authorization outcomes created=%d reconciled=%d", created, reconciled)
	}
	assertWorkspaceAnalysisConcurrentModelFacts(t, ctx, pool, fixture.command, true)
}

// TestWorkspaceAnalysisCancellationRacingModelAuthorizationHasNoOrphanFacts
// starts the real Runtime Cancel command and model authorization together. A
// cancellation that owns the workflow lock blocks the new call entirely; an
// authorization that wins leaves exactly one complete operation/call/reservation
// bundle, and subsequent authorization is rejected by the persisted cancel flag.
func TestWorkspaceAnalysisCancellationRacingModelAuthorizationHasNoOrphanFacts(t *testing.T) {
	testWorkspaceAnalysisModelRepositoryIntegrationVariants(t, testWorkspaceAnalysisCancellationRacingModelAuthorizationHasNoOrphanFacts)
}

func testWorkspaceAnalysisCancellationRacingModelAuthorizationHasNoOrphanFacts(
	t *testing.T,
	pool *pgxpool.Pool,
	ctx context.Context,
	repository workspaceAnalysisModelRepositoryIntegrationStore,
) {
	fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)
	cancel := newWorkspaceAnalysisConcurrentCancelCoordinator(t, pool)

	start := make(chan struct{})
	authorizationResult := make(chan workspaceAnalysisConcurrentAuthorizationResult, 1)
	cancellationResult := make(chan error, 1)
	go func() {
		callCtx, callCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer callCancel()
		<-start
		result, authorizeErr := repository.AuthorizeWorkspaceAnalysisModelCall(callCtx, fixture.command)
		authorizationResult <- workspaceAnalysisConcurrentAuthorizationResult{result: result, err: authorizeErr}
	}()
	go func() {
		callCtx, callCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer callCancel()
		<-start
		_, cancelErr := cancel.Cancel(callCtx, workflowapplication.RunControlCommand{
			WorkflowRunID: fixture.command.Identity.WorkflowRunID, ExpectedVersion: 1,
			IdempotencyKey: "wa-model-authorization-cancel-race",
		})
		cancellationResult <- cancelErr
	}()
	close(start)

	cancelErr := <-cancellationResult
	authorized := <-authorizationResult
	if cancelErr != nil {
		t.Fatalf("concurrent cancellation: %v", cancelErr)
	}

	if authorized.err == nil {
		if authorized.result.Disposition != application.WorkspaceAnalysisModelAuthorizationCreated {
			t.Fatalf("authorization after cancellation race disposition=%s", authorized.result.Disposition)
		}
		assertWorkspaceAnalysisConcurrentModelFacts(t, ctx, pool, fixture.command, true)
	} else if code := agentErrorCode(authorized.err); code != application.ErrorCodeWorkspaceAnalysisModelAuthorizationInvalid && code != ErrorCodeRuntimeConsistency {
		t.Fatalf("cancel-won authorization error code=%s err=%v", code, authorized.err)
	} else {
		assertWorkspaceAnalysisConcurrentModelFacts(t, ctx, pool, fixture.command, false)
	}

	if _, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command); err == nil {
		t.Fatal("authorization after persisted cancellation unexpectedly succeeded")
	} else if code := agentErrorCode(err); code != application.ErrorCodeWorkspaceAnalysisModelAuthorizationInvalid && code != ErrorCodeRuntimeConsistency {
		t.Fatalf("authorization after persisted cancellation code=%s err=%v", code, err)
	}
	assertWorkspaceAnalysisConcurrentModelFacts(t, ctx, pool, fixture.command, authorized.err == nil)

	var cancelRequested bool
	if err := pool.QueryRow(ctx, `SELECT cancel_requested_at IS NOT NULL
		FROM workflow.run WHERE id=$1`, string(fixture.command.Identity.WorkflowRunID)).Scan(&cancelRequested); err != nil {
		t.Fatal(err)
	}
	if !cancelRequested {
		t.Fatal("cancellation race did not persist the workflow cancellation fence")
	}
}

type workspaceAnalysisConcurrentAuthorizationResult struct {
	result application.WorkspaceAnalysisModelAuthorizationResult
	err    error
}

func newWorkspaceAnalysisConcurrentCancelCoordinator(t *testing.T, pool *pgxpool.Pool) *workflowapplication.RuntimeCoordinator {
	t.Helper()
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapplication.NewRuntimeCoordinator(runtime)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func assertWorkspaceAnalysisConcurrentModelFacts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
	wantAuthorized bool,
) {
	t.Helper()
	var operations, reservations, modelRuns, modelCalls int
	var reservedModelCalls, reservedInputTokens, reservedOutputTokens int64
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.workspace_analysis_operation
		 WHERE analysis_run_id=$1 AND node_key=$2 AND operation_kind=$3 AND ordinal=$4),
		(SELECT count(*) FROM agent.workspace_analysis_budget_reservation
		 WHERE analysis_run_id=$1 AND operation_id=$5),
		(SELECT count(*) FROM agent.model_run WHERE id=$6),
		(SELECT count(*) FROM agent.model_call WHERE id=$7),
		reserved_model_calls,reserved_input_tokens,reserved_output_tokens
		FROM agent.workspace_analysis_run WHERE id=$1`,
		string(command.OperationKey.AnalysisRunID), string(command.OperationKey.NodeKey), string(command.OperationKey.Kind),
		command.OperationKey.Ordinal, string(command.OperationID), string(command.Run.ID), string(command.Call.ID),
	).Scan(&operations, &reservations, &modelRuns, &modelCalls, &reservedModelCalls, &reservedInputTokens, &reservedOutputTokens); err != nil {
		t.Fatal(err)
	}
	if !wantAuthorized {
		if operations != 0 || reservations != 0 || modelRuns != 0 || modelCalls != 0 ||
			reservedModelCalls != 0 || reservedInputTokens != 0 || reservedOutputTokens != 0 {
			t.Fatalf("cancelled authorization left runtime facts operations=%d reservations=%d model_runs=%d model_calls=%d budget=(%d,%d,%d)",
				operations, reservations, modelRuns, modelCalls, reservedModelCalls, reservedInputTokens, reservedOutputTokens)
		}
		return
	}
	if operations != 1 || reservations != 1 || modelRuns != 1 || modelCalls != 1 ||
		reservedModelCalls != 1 || reservedInputTokens != domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall ||
		reservedOutputTokens != int64(command.Call.MaxOutputTokens) {
		t.Fatalf("authorized runtime facts operations=%d reservations=%d model_runs=%d model_calls=%d budget=(%d,%d,%d)",
			operations, reservations, modelRuns, modelCalls, reservedModelCalls, reservedInputTokens, reservedOutputTokens)
	}

	var operationStatus, reservationStatus, callStatus string
	if err := pool.QueryRow(ctx, `SELECT operation.status,reservation.status,call.status
		FROM agent.workspace_analysis_operation AS operation
		JOIN agent.workspace_analysis_budget_reservation AS reservation ON reservation.operation_id=operation.id
		JOIN agent.model_call AS call ON call.id=operation.model_call_id
		WHERE operation.id=$1`, string(command.OperationID)).Scan(&operationStatus, &reservationStatus, &callStatus); err != nil {
		t.Fatal(err)
	}
	if operationStatus != "STARTED" || reservationStatus != "RESERVED" || callStatus != "STARTED" {
		t.Fatalf("authorized runtime closure operation=%s reservation=%s call=%s", operationStatus, reservationStatus, callStatus)
	}
}
