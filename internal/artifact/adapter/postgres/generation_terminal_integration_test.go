//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSectionGenerationTerminalHookClosesWorkflowOutcomesAtomically(t *testing.T) {
	ctx := context.Background()
	artifactRepository, pool := newArtifactIntegrationRepository(t, ctx)
	now := time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC)
	coordinator, agentRepository := newGenerationIntegrationCoordinator(t, pool, now, &generationCitationVerifierFake{})
	runtime := newGenerationTerminalRuntime(t, pool, agentRepository, generationIntegrationProfile())

	t.Run("non retryable failure releases the source slot", func(t *testing.T) {
		workspaceID := artifactIntegrationID(2001)
		generation := startTerminalGeneration(t, ctx, artifactRepository, coordinator, pool, workspaceID, 2010, "terminal-failed")
		claimed, deliveryID := claimTerminalGeneration(t, ctx, pool, runtime, generation)
		transition := terminalFailureTransition(generation, claimed, deliveryID, workflowdomain.FailureClassNonRetryable, "ARTIFACT_EXECUTION_FAILED")
		result, err := runtime.TransitionDelivery(ctx, transition)
		if err != nil || result.Run.Status != workflowdomain.RunStatusFailed || result.Node.Status != workflowdomain.NodeStatusFailed {
			t.Fatalf("failure result=%+v err=%v", result, err)
		}
		persisted := requireTerminalGeneration(t, ctx, pool, generation, artifactapplication.SectionGenerationFailed)
		if persisted.FailureClass != string(workflowdomain.FailureClassNonRetryable) || persisted.ErrorCode != "ARTIFACT_EXECUTION_FAILED" || persisted.ModelRunID != nil {
			t.Fatalf("failed generation=%+v", persisted)
		}
		replayed, err := runtime.TransitionDelivery(ctx, transition)
		if err != nil || replayed.Run.Status != workflowdomain.RunStatusFailed {
			t.Fatalf("failure replay=%+v err=%v", replayed, err)
		}
		retry := artifactapplication.StartSectionGenerationCommand{
			WorkspaceID: workspaceID, ArtifactID: generation.ArtifactID, ExpectedVersion: generation.SourceArtifactVersion,
			SectionKey: generation.SectionKey, IdempotencyKey: "terminal-failed-retry",
		}
		if restarted, err := coordinator.StartSectionGeneration(ctx, retry); err != nil || restarted.Replayed {
			t.Fatalf("restart=%+v err=%v", restarted, err)
		}
	})

	t.Run("successful workflow without receipt requires recovery", func(t *testing.T) {
		workspaceID := artifactIntegrationID(2101)
		generation := startTerminalGeneration(t, ctx, artifactRepository, coordinator, pool, workspaceID, 2110, "terminal-receipt-missing")
		claimed, deliveryID := claimTerminalGeneration(t, ctx, pool, runtime, generation)
		result, err := runtime.TransitionDelivery(ctx, workflowapplication.DeliveryTransition{
			Binding: terminalDeliveryBinding(generation, claimed, deliveryID),
			Result:  workflowdomain.AttemptResult{Output: json.RawMessage(`{"receipt":"missing"}`), OutputSchemaVersion: 1},
		})
		if err != nil || result.Run.Status != workflowdomain.RunStatusSucceeded {
			t.Fatalf("success result=%+v err=%v", result, err)
		}
		persisted := requireTerminalGeneration(t, ctx, pool, generation, artifactapplication.SectionGenerationRecoveryRequired)
		if persisted.ErrorCode != generationReceiptMissingCode || persisted.FailureClass != string(workflowdomain.FailureClassManualRecovery) {
			t.Fatalf("missing receipt generation=%+v", persisted)
		}
	})

	t.Run("direct and retry wait cancellations are safe", func(t *testing.T) {
		workspaceID := artifactIntegrationID(2201)
		pending := startTerminalGeneration(t, ctx, artifactRepository, coordinator, pool, workspaceID, 2210, "terminal-direct-cancel")
		control, err := workflowapplication.NewRuntimeCoordinator(runtime)
		if err != nil {
			t.Fatal(err)
		}
		version := terminalWorkflowVersion(t, ctx, pool, pending.WorkflowRunID)
		cancelled, err := control.Cancel(ctx, workflowapplication.RunControlCommand{
			WorkflowRunID: pending.WorkflowRunID, ExpectedVersion: version, IdempotencyKey: "terminal-direct-cancel-command",
		})
		if err != nil || cancelled.Status != workflowdomain.RunStatusCancelled {
			t.Fatalf("direct cancel=%+v err=%v", cancelled, err)
		}
		requireTerminalGeneration(t, ctx, pool, pending, artifactapplication.SectionGenerationCancelled)

		retryWorkspaceID := artifactIntegrationID(2202)
		retrying := startTerminalGeneration(t, ctx, artifactRepository, coordinator, pool, retryWorkspaceID, 2220, "terminal-retry-cancel")
		claimed, deliveryID := claimTerminalGeneration(t, ctx, pool, runtime, retrying)
		retried, err := runtime.TransitionDelivery(ctx, terminalFailureTransition(
			retrying, claimed, deliveryID, workflowdomain.FailureClassRetryable, "DEPENDENCY_BUSY",
		))
		if err != nil || retried.Run.Status != workflowdomain.RunStatusRetryWait || retried.Node.Status != workflowdomain.NodeStatusRetryWait {
			t.Fatalf("retry result=%+v err=%v", retried, err)
		}
		if current := requireTerminalGeneration(t, ctx, pool, retrying, artifactapplication.SectionGenerationPending); current.TerminalAt != nil {
			t.Fatalf("retry scheduled terminalized generation=%+v", current)
		}
		cancelled, err = control.Cancel(ctx, workflowapplication.RunControlCommand{
			WorkflowRunID: retrying.WorkflowRunID, ExpectedVersion: retried.Run.Version, IdempotencyKey: "terminal-retry-wait-cancel",
		})
		if err != nil || cancelled.Status != workflowdomain.RunStatusCancelled {
			t.Fatalf("retry wait cancel=%+v err=%v", cancelled, err)
		}
		requireTerminalGeneration(t, ctx, pool, retrying, artifactapplication.SectionGenerationCancelled)
	})

	t.Run("running model outcome retains exclusive recovery ownership", func(t *testing.T) {
		workspaceID := artifactIntegrationID(2301)
		generation := startTerminalGeneration(t, ctx, artifactRepository, coordinator, pool, workspaceID, 2310, "terminal-model-running")
		claimed, deliveryID := claimTerminalGeneration(t, ctx, pool, runtime, generation)
		run := seedGenerationRunningModelRun(t, ctx, pool, agentRepository, generation, claimed.Attempt.ID, artifactIntegrationID(2330), now.Add(time.Minute))
		result, err := runtime.TransitionDelivery(ctx, terminalFailureTransition(
			generation, claimed, deliveryID, workflowdomain.FailureClassNonRetryable, "EXECUTOR_ABORTED",
		))
		if err != nil || result.Run.Status != workflowdomain.RunStatusFailed {
			t.Fatalf("running model failure=%+v err=%v", result, err)
		}
		persisted := requireTerminalGeneration(t, ctx, pool, generation, artifactapplication.SectionGenerationRecoveryRequired)
		if persisted.ModelRunID == nil || *persisted.ModelRunID != run.ID || persisted.ErrorCode != generationModelOutcomeUnknownCode {
			t.Fatalf("running model generation=%+v", persisted)
		}
		blocked := artifactapplication.StartSectionGenerationCommand{
			WorkspaceID: workspaceID, ArtifactID: generation.ArtifactID, ExpectedVersion: generation.SourceArtifactVersion,
			SectionKey: generation.SectionKey, IdempotencyKey: "terminal-model-running-retry",
		}
		if _, err := coordinator.StartSectionGeneration(ctx, blocked); !artifactIntegrationErrorCode(err, artifactapplication.ErrorCodeIdempotencyConflict) {
			t.Fatalf("recovery restart error=%v", err)
		}
	})
}

func TestSectionGenerationTerminalHookErrorRollsBackWorkflowAndGeneration(t *testing.T) {
	ctx := context.Background()
	artifactRepository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceID := artifactIntegrationID(2401)
	now := time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)
	coordinator, agentRepository := newGenerationIntegrationCoordinator(t, pool, now, &generationCitationVerifierFake{})
	generation := startTerminalGeneration(t, ctx, artifactRepository, coordinator, pool, workspaceID, 2410, "terminal-hook-rollback")
	wrongProfile := agentdomain.ModelProfileRef{ID: "artifact.wrong", Version: "v1"}
	runtime := newGenerationTerminalRuntime(t, pool, agentRepository, wrongProfile)
	claimed, deliveryID := claimTerminalGeneration(t, ctx, pool, runtime, generation)
	seedGenerationRunningModelRun(t, ctx, pool, agentRepository, generation, claimed.Attempt.ID, artifactIntegrationID(2430), now.Add(time.Minute))

	_, err := runtime.TransitionDelivery(ctx, terminalFailureTransition(
		generation, claimed, deliveryID, workflowdomain.FailureClassNonRetryable, "EXECUTOR_ABORTED",
	))
	if !artifactIntegrationErrorCode(err, "ARTIFACT_WORKFLOW_CONTEXT_INVALID") {
		t.Fatalf("terminal hook error=%v", err)
	}
	requireTerminalGeneration(t, ctx, pool, generation, artifactapplication.SectionGenerationPending)
	var nodeStatus workflowdomain.NodeStatus
	var attemptStatus workflowdomain.AttemptStatus
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow.node_run WHERE id=$1`, string(generation.NodeRunID)).Scan(&nodeStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow.node_attempt WHERE id=$1`, string(claimed.Attempt.ID)).Scan(&attemptStatus); err != nil {
		t.Fatal(err)
	}
	if nodeStatus != workflowdomain.NodeStatusRunning || attemptStatus != workflowdomain.AttemptStatusRunning {
		t.Fatalf("rollback node=%s attempt=%s", nodeStatus, attemptStatus)
	}
}

func newGenerationTerminalRuntime(
	t *testing.T,
	pool *pgxpool.Pool,
	agentRepository *agentpostgres.Repository,
	profile agentdomain.ModelProfileRef,
) *workflowpostgres.RuntimeRepository {
	t.Helper()
	hook, err := NewSectionGenerationTerminalHook(agentRepository, profile)
	if err != nil {
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
	runtime, err := workflowpostgres.NewRuntimeRepositoryWithHooks(pool, inserter, workflowpostgres.RuntimeRepositoryHooks{Terminal: hook})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func startTerminalGeneration(
	t *testing.T,
	ctx context.Context,
	artifactRepository *Repository,
	coordinator *SectionGenerationRepository,
	pool *pgxpool.Pool,
	workspaceID foundation.ID,
	idBase int,
	key string,
) artifactapplication.SectionGeneration {
	t.Helper()
	seedArtifactWorkspace(t, ctx, pool, workspaceID, key)
	source := seedGeneratingArtifact(t, artifactRepository, workspaceID, idBase, time.Date(2026, 7, 26, 19, 0, 0, 0, time.UTC))
	started, err := coordinator.StartSectionGeneration(ctx, artifactapplication.StartSectionGenerationCommand{
		WorkspaceID: workspaceID, ArtifactID: source.Artifact.ID, ExpectedVersion: source.Artifact.Version,
		SectionKey: "second", IdempotencyKey: key,
	})
	if err != nil || started.Replayed {
		t.Fatalf("start terminal generation=%+v err=%v", started, err)
	}
	return started.Generation
}

func claimTerminalGeneration(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	runtime *workflowpostgres.RuntimeRepository,
	generation artifactapplication.SectionGeneration,
) (workflowapplication.ClaimResult, string) {
	t.Helper()
	var riverJobID int64
	if err := pool.QueryRow(ctx, `
		SELECT id FROM workflow.river_job
		WHERE args->>'node_run_id'=$1 AND (args->>'dispatch_no')::integer=1
		ORDER BY id LIMIT 1`, string(generation.NodeRunID)).Scan(&riverJobID); err != nil {
		t.Fatal(err)
	}
	deliveryID := fmt.Sprintf("artifact-terminal-%d", riverJobID)
	claimed, err := runtime.Claim(ctx, workflowapplication.ClaimCommand{
		NodeRunID: generation.NodeRunID, DispatchNo: 1, DeliveryID: deliveryID,
		RiverJobID: riverJobID, LeaseOwner: "artifact-terminal-worker", LeaseDuration: time.Minute,
	})
	if err != nil || claimed.Disposition != workflowapplication.ClaimDispositionClaimed {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	return claimed, deliveryID
}

func terminalFailureTransition(
	generation artifactapplication.SectionGeneration,
	claimed workflowapplication.ClaimResult,
	deliveryID string,
	class workflowdomain.FailureClass,
	code string,
) workflowapplication.DeliveryTransition {
	kind := foundation.ErrorNonRetryableFailure
	if class == workflowdomain.FailureClassRetryable {
		kind = foundation.ErrorRetryableFailure
	}
	return workflowapplication.DeliveryTransition{
		Binding: terminalDeliveryBinding(generation, claimed, deliveryID),
		Result: workflowdomain.AttemptResult{Failure: &workflowdomain.FailureEnvelope{
			Class: class, ErrorKind: kind, Code: code, Summary: code,
		}},
	}
}

func terminalDeliveryBinding(
	generation artifactapplication.SectionGeneration,
	claimed workflowapplication.ClaimResult,
	deliveryID string,
) workflowapplication.DeliveryBinding {
	return workflowapplication.DeliveryBinding{
		NodeRunID: generation.NodeRunID, DispatchNo: 1, DeliveryID: deliveryID,
		Fence: workflowdomain.LeaseFence{
			Owner: "artifact-terminal-worker", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version,
		},
	}
}

func requireTerminalGeneration(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	generation artifactapplication.SectionGeneration,
	status artifactapplication.SectionGenerationStatus,
) artifactapplication.SectionGeneration {
	t.Helper()
	persisted, found, err := loadSectionGenerationByKey(ctx, pool, generation.WorkspaceID, generation.IdempotencyKey, false)
	if err != nil || !found || persisted.Status != status {
		t.Fatalf("generation=%+v found=%t err=%v want_status=%s", persisted, found, err, status)
	}
	return persisted
}

func terminalWorkflowVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID foundation.ID) int64 {
	t.Helper()
	var version int64
	if err := pool.QueryRow(ctx, `SELECT version FROM workflow.run WHERE id=$1`, string(runID)).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func terminalErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
