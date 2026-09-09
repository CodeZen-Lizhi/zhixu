//go:build integration

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"reflect"
	"testing"
	"time"

	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	captureapplication "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/jackc/pgx/v5"
)

// This proves the production composition and deterministic Provider protocol,
// not the semantic quality of a real model or the approval/writeback UI.
func TestWorkerSynthesisCompositionImportsContinuouslyAndReplaysWithoutNewResults(t *testing.T) {
	pool := newMigratedWorkerTestPool(t, os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	root := t.TempDir()
	provider, cfg := newSynthesisCompositionProvider(t)
	models, err := modelRuntimeForComposition(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := models.Close(); err != nil {
			t.Errorf("close fixture model runtime: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	seedWorkspaceAnalysisGitRepository(t, ctx, root)
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	files := filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}}
	git := gitcli.New("")
	workspaceService := workspaceapplication.NewService(workspaceapplication.Dependencies{
		Repository: workspaces, Files: files, ManagedFiles: files, Git: git, GitInitializer: git,
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	createdWorkspace, err := workspaceService.CreateWorkspace(ctx, workspaceapplication.CreateWorkspaceRequest{
		Name: "Synthesis Worker integration", RootPath: root, InitializeGit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := createdWorkspace.Workspace.ID
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	// Keep the real static Models lifecycle while using exactly the constructor
	// called by newWorkerComponents. No registry, executor or owner is replaced.
	components, err := newWorkerComponentsWithModels(pool, cfg, models, workerModelRuntimeBinding{},
		workspaces, logger, observability.NewMemoryMetrics(), observability.NewNoopTracer())
	if err != nil {
		t.Fatal(err)
	}
	if !synthesisWorkflowReadiness(components) || !captureWorkflowReadiness(components) || !components.captureProfile.available {
		t.Fatal("production Capture and Synthesis composition is not ready")
	}
	for _, kind := range organizingworkflow.SynthesisExecutorNodeKinds() {
		executor, err := components.executors.Resolve(kind, organizingworkflow.SynthesisInputSchemaVersion)
		if err != nil || executor != components.synthesisExecutor {
			t.Fatalf("production synthesis executor %s: type=%T err=%v", kind, executor, err)
		}
	}
	captures, err := capturepostgres.NewGORMRepository(pool, workspaces)
	if err != nil {
		t.Fatal(err)
	}
	captureService, err := captureapplication.NewService(captureapplication.Dependencies{
		Repository: captures, Content: components.sourceProcessing.workspace,
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := components.runtimeClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// Registered last: River must be joined before Models, HTTP servers, the
	// temporary Workspace, or the isolated database are released.
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := components.runtimeClient.Stop(stopCtx); err != nil {
			t.Errorf("stop synthesis fixture River: %v", err)
			forceCtx, forceCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer forceCancel()
			if err := components.runtimeClient.StopAndCancel(forceCtx); err != nil {
				t.Errorf("cancel synthesis fixture River: %v", err)
			}
		}
		select {
		case <-components.runtimeClient.Stopped():
		default:
			t.Error("fixture River did not join before resource cleanup")
		}
	})

	commands := []captureapplication.TextCommand{
		{WorkspaceID: workspaceID, DisplayName: "Cache expiration first", Text: synthesisCompositionFirstText, IdempotencyKey: "synthesis-composition-first"},
		{WorkspaceID: workspaceID, DisplayName: "Cache expiration update", Text: synthesisCompositionSecondText, IdempotencyKey: "synthesis-composition-second"},
		{WorkspaceID: workspaceID, DisplayName: "Cache expiration repeated", Text: synthesisCompositionDuplicateText, IdempotencyKey: "synthesis-composition-duplicate"},
	}
	created := make([]captureapplication.CreateResult, len(commands))
	for index, command := range commands {
		result, err := captureService.CreateText(ctx, command)
		if err != nil || result.Replayed || result.Capture.LatestSourceVersionID == "" {
			t.Fatalf("import %d: capture=%s replayed=%t err=%v", index+1, result.Capture.ID, result.Replayed, err)
		}
		created[index] = result
		wantStatus := "SUCCEEDED"
		if index == 2 {
			wantStatus = "NO_CHANGE"
		}
		waitForSynthesisComposition(t, ctx, pool, components, captureService, provider, logger, result.Capture, wantStatus)
		assertSynthesisCompositionCounts(t, ctx, pool, workspaceID, index+1, min(index+1, 2))
		if calls, failure := provider.snapshot(); failure != "" || calls != (synthesisCompositionCalls{Profile: index + 1, Generate: index + 1, Validate: index + 1, NoChange: max(index-1, 0)}) {
			t.Fatalf("import %d provider calls=%+v failure=%s", index+1, calls, failure)
		}
	}
	assertSynthesisCompositionRevisions(t, ctx, pool, workspaceID, created[0].Capture, created[1].Capture, root)
	before := synthesisCompositionCounts(t, ctx, pool, workspaceID)
	beforeCalls, _ := provider.snapshot()
	for index, command := range commands {
		replayed, err := captureService.CreateText(ctx, command)
		if err != nil || !replayed.Replayed || replayed.Capture.ID != created[index].Capture.ID ||
			replayed.Capture.LatestSourceVersionID != created[index].Capture.LatestSourceVersionID {
			t.Fatalf("replay import %d: capture=%s replayed=%t err=%v", index+1, replayed.Capture.ID, replayed.Replayed, err)
		}
	}
	// Exercise the same periodic entry points after exact command replay. Empty
	// durable queues, not a timing delay, prove there is no new model work.
	captureBatch, err := dispatchCaptureOutbox(ctx, logger, components.captureOutbox, captureDispatchPeriodicPhase)
	if err != nil || captureBatch != (captureapplication.DispatchBatchResult{}) {
		t.Fatalf("Capture replay dispatch=%+v err=%v", captureBatch, err)
	}
	synthesisBatch, err := dispatchSynthesisSources(ctx, logger, components.synthesisSources, organizingDispatchPeriodicPhase)
	if err != nil || synthesisBatch != (organizingworkflow.SynthesisDispatchBatchResult{}) {
		t.Fatalf("Synthesis replay dispatch=%+v err=%v", synthesisBatch, err)
	}
	if after := synthesisCompositionCounts(t, ctx, pool, workspaceID); !reflect.DeepEqual(after, before) {
		t.Fatalf("replay changed durable counts: before=%v after=%v", before, after)
	}
	if calls, failure := provider.snapshot(); failure != "" || calls != beforeCalls {
		t.Fatalf("replay called Provider again: before=%+v after=%+v failure=%s", beforeCalls, calls, failure)
	}
}

func waitForSynthesisComposition(t *testing.T, ctx context.Context, pool *platformpostgres.Pool,
	components workerComponents, captures *captureapplication.Service, provider *synthesisCompositionProvider,
	logger *slog.Logger, imported capturedomain.Capture, wantStatus string,
) {
	t.Helper()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var processingStatus, workflowStatus, failure string
	var completedJobs int
	var capture capturedomain.Capture
	for {
		if _, failure := provider.snapshot(); failure != "" {
			t.Fatalf("fixture Provider rejected a request: %s", failure)
		}
		if _, err := dispatchCaptureOutbox(ctx, logger, components.captureOutbox, captureDispatchPeriodicPhase); err != nil {
			t.Fatal(err)
		}
		if _, err := dispatchSynthesisSources(ctx, logger, components.synthesisSources, organizingDispatchPeriodicPhase); err != nil {
			t.Fatal(err)
		}
		var err error
		capture, err = captures.Get(ctx, imported.WorkspaceID, imported.ID)
		if err != nil {
			t.Fatal(err)
		}
		if capture.Status == capturedomain.StatusProcessingFailed || capture.Status == capturedomain.StatusFetchFailed {
			t.Fatalf("Capture failed: status=%s stage=%s code=%s", capture.Status, capture.FailureStage, capture.ErrorCode)
		}
		err = pool.DB().QueryRow(ctx, `SELECT processing.status,run.status,COALESCE(processing.error_code,''),
			(SELECT count(*) FROM workflow.river_job job
			 JOIN workflow.node_run node ON job.args->>'node_run_id'=node.id::text
			 WHERE node.run_id=run.id AND job.state='completed')
			FROM organizing.synthesis_processing processing
			JOIN workflow.run run ON run.id=processing.workflow_run_id
			WHERE processing.workspace_id=$1 AND processing.source_version_id=$2`,
			string(imported.WorkspaceID), string(imported.LatestSourceVersionID),
		).Scan(&processingStatus, &workflowStatus, &failure, &completedJobs)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if processingStatus == "FAILED" || processingStatus == "RECOVERY_REQUIRED" || workflowStatus == "failed" {
			t.Fatalf("Synthesis failed: processing=%s workflow=%s code=%s", processingStatus, workflowStatus, failure)
		}
		if err == nil && processingStatus == wantStatus && workflowStatus == "succeeded" && completedJobs == 4 &&
			capture.IngestionStatus == capturedomain.StageReady && capture.IndexStatus == capturedomain.StageReady &&
			capture.ProfileStatus == capturedomain.StageReady {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for composition: capture=%s ingestion=%s index=%s profile=%s processing=%s workflow=%s jobs=%d: %v",
				capture.Status, capture.IngestionStatus, capture.IndexStatus, capture.ProfileStatus, processingStatus, workflowStatus, completedJobs, ctx.Err())
		case <-ticker.C:
		}
	}
}
