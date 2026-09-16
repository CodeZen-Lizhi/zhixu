//go:build integration

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	captureapplication "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	captureprofile "github.com/CodeZen-Lizhi/zhixu/internal/capture/profile"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
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

// 验证真实目录扫描器及生产 Capture/ModelRun/River
// 组装。Provider 的输出固定；不预置画像记录。
func TestWorkerScannedSourcesBackfillProfilesAndPreserveHistory(t *testing.T) {
	pool := newMigratedWorkerTestPool(t, os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	root := t.TempDir()
	provider, cfg := newSynthesisCompositionProvider(t)
	models, err := modelRuntimeForComposition(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := models.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seedWorkspaceAnalysisGitRepository(t, ctx, root)
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	files := filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}}
	git := gitcli.New("")
	service := workspaceapplication.NewService(workspaceapplication.Dependencies{
		Repository: workspaces, Files: files, ManagedFiles: files, Git: git, GitInitializer: git,
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	created, err := service.CreateWorkspace(ctx, workspaceapplication.CreateWorkspaceRequest{Name: "Profile backfill", RootPath: root, InitializeGit: true})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := created.Workspace.ID
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	components, err := newWorkerComponentsWithModels(pool, cfg, models, workerModelRuntimeBinding{}, workspaces, logger, observability.NewMemoryMetrics(), observability.NewNoopTracer())
	if err != nil {
		t.Fatal(err)
	}
	modelRuns, err := agentpostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := capturepostgres.NewGORMProfileRepository(pool, modelRuns)
	if err != nil {
		t.Fatal(err)
	}
	captures, err := capturepostgres.NewGORMRepository(pool, workspaces)
	if err != nil {
		t.Fatal(err)
	}
	if err := components.runtimeClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := components.runtimeClient.Stop(stopCtx); err != nil {
			t.Error(err)
		}
	})
	var previous captureapplication.ProfileView
	var sourceID, captureID, initialVersion, initialArtifact foundation.ID
	for index, body := range []string{synthesisCompositionFirstText, synthesisCompositionSecondText, synthesisCompositionFirstText} {
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		components.localSourceDiscovery.nextPass = time.Time{}
		page, err := components.localSourceDiscovery.DiscoverBatch(ctx)
		if err != nil || len(page.Files) != 1 || !page.Done || page.Failed != 0 {
			t.Fatalf("discovery=%+v error=%v", page, err)
		}
		source := page.Files[0]
		if index == 0 {
			initialVersion = source.SourceVersionID
			initialArtifact = source.ContentArtifactID
		}
		if index == 2 && (source.SourceVersionID == initialVersion || source.ContentArtifactID != initialArtifact) {
			t.Fatalf("returning content lost occurrence/artifact identity: %+v", source)
		}
		if index > 0 && source.SourceID != sourceID {
			t.Fatal("file edit replaced source identity")
		}
		sourceID = source.SourceID
		// 交错执行的 Worker 扫描必须原子地创建唯一持久请求。
		type outcome struct {
			count int
			err   error
		}
		results := make(chan outcome, 2)
		for n := 0; n < 2; n++ {
			go func() {
				count, err := components.captureBackfill.BackfillSources(ctx, 25)
				results <- outcome{count, err}
			}()
		}
		total := 0
		for n := 0; n < 2; n++ {
			result := <-results
			if result.err != nil {
				t.Fatal(result.err)
			}
			total += result.count
		}
		if total != 1 {
			t.Fatalf("concurrent backfill scheduled=%d", total)
		}
		var current captureapplication.ProfileView
		ticker := time.NewTicker(25 * time.Millisecond)
		for {
			if _, err := dispatchCaptureOutbox(ctx, logger, components.captureOutbox, captureDispatchPeriodicPhase); err != nil {
				ticker.Stop()
				t.Fatal(err)
			}
			current, err = profiles.GetProfile(ctx, captureapplication.ProfileQuery{WorkspaceID: workspaceID, SourceVersionID: source.SourceVersionID})
			if err != nil {
				ticker.Stop()
				t.Fatal(err)
			}
			capture, err := captures.Get(ctx, workspaceID, current.Profile.CaptureID)
			if err != nil {
				ticker.Stop()
				t.Fatal(err)
			}
			if capture.Status == capturedomain.StatusProcessingFailed {
				ticker.Stop()
				t.Fatalf("capture failed: %+v", capture)
			}
			var active int
			err = pool.DB().QueryRow(ctx, `SELECT count(*) FROM ops.capture_outbox event LEFT JOIN workflow.run run ON run.id::text=event.payload->>'workflow_run_id' WHERE event.capture_id=$1 AND (event.published_at IS NULL OR run.status NOT IN ('succeeded','failed','cancelled'))`, string(capture.ID)).Scan(&active)
			if err != nil {
				ticker.Stop()
				t.Fatal(err)
			}
			if current.Profile.Status == capturedomain.ProfileStatusReady && capture.ProfileStatus == capturedomain.StageReady && active == 0 {
				break
			}
			select {
			case <-ctx.Done():
				ticker.Stop()
				t.Fatalf("profile=%+v: %v", current.Profile, ctx.Err())
			case <-ticker.C:
			}
		}
		ticker.Stop()
		if current.Revision == nil || current.Revision.Content.Summary == "" || len(current.Revision.Content.Topics) == 0 || len(current.Revision.Content.KnowledgePoints) == 0 || len(current.Evidence) == 0 {
			t.Fatalf("missing generated metadata: %+v", current)
		}
		if index > 0 {
			if current.Profile.CaptureID != captureID {
				t.Fatal("new version replaced stable capture")
			}
			old, err := profiles.GetProfile(ctx, captureapplication.ProfileQuery{WorkspaceID: workspaceID, SourceVersionID: previous.Profile.SourceVersionID})
			if err != nil || !reflect.DeepEqual(old, previous) {
				t.Fatalf("historical profile changed: %v", err)
			}
		}
		latest, err := profiles.ListProfileDirectory(ctx, captureapplication.ProfileDirectoryQuery{WorkspaceID: workspaceID, Limit: 10})
		if err != nil || len(latest.Items) != 1 || latest.Items[0].View.Profile.SourceVersionID != source.SourceVersionID {
			t.Fatalf("directory did not select observed version: %+v %v", latest, err)
		}
		captureID = current.Profile.CaptureID
		previous = current
		if count, err := components.captureBackfill.BackfillSources(ctx, 25); err != nil || count != 0 {
			t.Fatalf("replay count=%d error=%v", count, err)
		}
		if calls, failure := provider.snapshot(); failure != "" || calls.Profile != index+1 || calls.Generate != 0 {
			t.Fatalf("provider=%+v failure=%s", calls, failure)
		}
	}
	// 删除后原样恢复的文件复用原来源、版本及其
	// 已完成的画像，不生成第二次模型请求。
	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	reconcileLocalSourcePresence(ctx, logger, components.localSourcePresence, 5*time.Second)
	var removed bool
	if err := pool.DB().QueryRow(ctx, `SELECT removed_at IS NOT NULL FROM core.source WHERE id=$1`, string(sourceID)).Scan(&removed); err != nil || !removed {
		t.Fatalf("missing file was not marked: %v %v", removed, err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(synthesisCompositionFirstText), 0600); err != nil {
		t.Fatal(err)
	}
	components.localSourceDiscovery.nextPass = time.Time{}
	if page, err := components.localSourceDiscovery.DiscoverBatch(ctx); err != nil || len(page.Files) != 1 || page.Files[0].SourceVersionID != previous.Profile.SourceVersionID {
		t.Fatalf("restoration changed source/version: %+v %v", page, err)
	}
	if err := pool.DB().QueryRow(ctx, `SELECT removed_at IS NOT NULL FROM core.source WHERE id=$1`, string(sourceID)).Scan(&removed); err != nil || removed {
		t.Fatalf("restored file remained removed: %v %v", removed, err)
	}
	if count, err := components.captureBackfill.BackfillSources(ctx, 25); err != nil || count != 0 {
		t.Fatalf("restoration duplicated profile: %d %v", count, err)
	}
	if err := os.WriteFile(filepath.Join(root, "000-oversized.md"), make([]byte, filesystem.DefaultMaxBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	// 已完成和已排队的来源不能让后续文件始终无法进入有界分页。
	for _, name := range []string{"second.md", "third.md", "fourth.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(synthesisCompositionFirstText), 0600); err != nil {
			t.Fatal(err)
		}
	}
	components.localSourceDiscovery.nextPass = time.Time{}
	if page, err := components.localSourceDiscovery.DiscoverBatch(ctx); err != nil || len(page.Files) != 4 || !page.Done || page.Failed != 1 {
		t.Fatalf("new-file discovery: %+v %v", page, err)
	}
	for _, want := range []int{2, 1, 0} {
		if count, err := components.captureBackfill.BackfillSources(ctx, 2); err != nil || count != want {
			t.Fatalf("bounded page count=%d want=%d error=%v", count, want, err)
		}
	}

}
