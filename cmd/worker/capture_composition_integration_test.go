//go:build integration

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	captureapplication "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	captureprofile "github.com/CodeZen-Lizhi/zhixu/internal/capture/profile"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestWorkerCaptureCompositionRegistersExecutorDefinitionAndOutbox(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	pool := newMigratedWorkerTestPool(t, databaseURL)
	components, err := newWorkerComponents(
		pool,
		config.Defaults(),
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
		observability.NewMemoryMetrics(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !captureWorkflowReadiness(components) {
		t.Fatal("Capture worker composition is not ready")
	}
	if components.captureProfile.available || components.captureProfile.code != captureprofile.ErrorCodeCapabilityUnavailable {
		t.Fatalf("disabled Capture Profile capability=%+v", components.captureProfile)
	}
	modelRuns, err := agentpostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	disabledGenerator, disabledCapability, err := newCaptureProfileGenerator(
		pool, nil, platformmodels.ChatContract{}, modelRuns,
		nil,
		foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil || disabledGenerator == nil || disabledCapability.available ||
		disabledCapability.code != captureprofile.ErrorCodeCapabilityUnavailable {
		t.Fatalf("disabled generator=%T capability=%+v err=%v", disabledGenerator, disabledCapability, err)
	}
	executor, err := components.executors.Resolve(
		captureapplication.ProcessingNodeKind,
		captureapplication.ProcessingInputSchemaVersion,
	)
	if err != nil || executor != components.captureExecutor {
		t.Fatalf("Capture executor=%T err=%v", executor, err)
	}
	definition, err := components.definitions.Resolve(
		captureapplication.ProcessingDefinitionKey,
		captureapplication.ProcessingDefinitionVersion,
	)
	if err != nil || len(definition.Graph.Nodes) != 1 || definition.Graph.Nodes[0].Kind != captureapplication.ProcessingNodeKind {
		t.Fatalf("Capture definition=%+v err=%v", definition, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	workspaceID := foundation.ID("53000000-0000-4000-8000-000000000001")
	now := time.Now().UTC()
	if _, err := pool.DB().Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,'Capture Worker Composition',$2,$2,$3,'active',1,$3,$3)`, string(workspaceID), t.TempDir(), now); err != nil {
		t.Fatal(err)
	}
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	captureRepository, err := capturepostgres.NewGORMRepository(pool, workspaces)
	if err != nil {
		t.Fatal(err)
	}
	captureService, err := captureapplication.NewService(captureapplication.Dependencies{
		Repository: captureRepository, Content: unusedCaptureContentWriter{},
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := captureService.CreateURL(ctx, captureapplication.URLCommand{
		WorkspaceID: workspaceID, URL: "https://example.com/java-ai",
		IdempotencyKey: "capture-worker-composition",
	})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	first, err := dispatchCaptureOutbox(ctx, logger, components.captureOutbox, captureDispatchStartupPhase)
	if err != nil || first.Claimed != 1 || first.Started != 1 || first.Retried != 0 || first.Poisoned != 0 {
		t.Fatalf("first Capture outbox batch=%+v err=%v", first, err)
	}
	second, err := dispatchCaptureOutbox(ctx, logger, components.captureOutbox, captureDispatchPeriodicPhase)
	if err != nil || second != (captureapplication.DispatchBatchResult{}) {
		t.Fatalf("replayed Capture outbox batch=%+v err=%v", second, err)
	}

	var published, runs, nodes, jobs int
	err = pool.DB().QueryRow(ctx, `SELECT
		(SELECT count(*) FROM ops.capture_outbox
		 WHERE workspace_id=$1 AND capture_id=$2 AND published_at IS NOT NULL),
		(SELECT count(*) FROM workflow.run AS run
		 JOIN workflow.definition AS definition ON definition.id=run.definition_id
		 WHERE run.workspace_id=$1 AND definition.key=$3 AND definition.version=$4),
		(SELECT count(*) FROM workflow.node_run AS node
		 JOIN workflow.run AS run ON run.id=node.run_id
		 WHERE run.workspace_id=$1 AND node.node_type=$5),
		(SELECT count(*) FROM workflow.river_job AS job
		 JOIN workflow.node_run AS node ON job.args->>'node_run_id'=node.id::text
		 JOIN workflow.run AS run ON run.id=node.run_id
		 WHERE run.workspace_id=$1 AND node.node_type=$5)`,
		string(workspaceID), string(created.Capture.ID), captureapplication.ProcessingDefinitionKey,
		captureapplication.ProcessingDefinitionVersion, captureapplication.ProcessingNodeKind,
	).Scan(&published, &runs, &nodes, &jobs)
	if err != nil {
		t.Fatal(err)
	}
	if published != 1 || runs != 1 || nodes != 1 || jobs != 1 {
		t.Fatalf("Capture durable dispatch facts published=%d runs=%d nodes=%d jobs=%d", published, runs, nodes, jobs)
	}
}

type unusedCaptureContentWriter struct{}

func (unusedCaptureContentWriter) StageManagedBytes(context.Context, foundation.ID, string, []byte, string) (workspacedomain.ManagedContentStage, error) {
	return workspacedomain.ManagedContentStage{}, errors.New("URL Capture must not stage bytes before the Worker fetch stage")
}

func (unusedCaptureContentWriter) PublishManagedBytes(context.Context, foundation.ID, workspacedomain.ManagedContentStage) (workspacedomain.ContentCapture, error) {
	return workspacedomain.ContentCapture{}, errors.New("URL Capture must not publish bytes before the Worker fetch stage")
}

func (unusedCaptureContentWriter) DiscardManagedBytes(context.Context, foundation.ID, workspacedomain.ManagedContentStage) error {
	return errors.New("URL Capture must not discard bytes before the Worker fetch stage")
}
