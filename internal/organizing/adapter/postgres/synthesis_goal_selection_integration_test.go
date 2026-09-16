//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingagent "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/agent"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"gorm.io/gorm"
)

func TestGoalSelectionPersistsActualModelProofAndRejectsLateResults(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 110)
	ctx := t.Context()
	source := f.generation(t, 189000, nil, "Redis expiration bounds cache lifetime.")
	seedGoalCatalogProfile(t, f, source, 189000, "Redis")
	runs, err := agentpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := capturepostgres.NewGORMProfileRepository(f.platform, runs)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := organizingowner.NewSynthesisGoalCatalogReader(profiles, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewGORMGoalSelectionStore(f.platform, f.store, directory, runs)
	if err != nil {
		t.Fatal(err)
	}
	catalog := agentapp.NewRuntimeCatalog()
	if err := organizingagent.RegisterGoalSelectionRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "goal-selection-test", Version: "v1"}, Model: agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "goal-selection", ModelVersion: "v1"}, Timeout: time.Second, MaxOutputTokens: 2048}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	scheduler, err := agenteino.NewStructuredPhaseScheduler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 刻意不提供原始产物字节。选择仅读取冻结元数据；后续生成须另行打开来源证据。
	f.artifacts.items = nil
	cases := []struct {
		name, raw string
		status    app.GoalSelectionStatus
	}{
		{"selected", " \n" + `{"selections":[{"point":"P001","reason":"Redis expiration belongs in Redis review."}],"explanation":"The source covers Redis."}` + "\n", app.GoalSelectionSucceeded},
		{"empty", `{"selections":[],"explanation":"No relevant point for this request."}`, app.GoalSelectionSucceeded},
		{"invented", `{"selections":[{"point":"P002","reason":"Invented point."}],"explanation":"Invalid result."}`, app.GoalSelectionFailed},
		{"cancelled", `{"selections":[{"point":"P001","reason":"Relevant Redis point."}],"explanation":"Late output."}`, app.GoalSelectionRecoveryRequired},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			created, err := f.store.CreateSynthesisGoal(ctx, app.CreateSynthesisGoalCommand{WorkspaceID: f.workspace, Goal: "整理 Redis 专项知识", IdempotencyKey: "selection-" + tc.name})
			if err != nil {
				t.Fatal(err)
			}
			page, err := directory.ReadSynthesisGoalCatalog(ctx, app.SynthesisGoalCatalogQuery{WorkspaceID: f.workspace, Limit: 32})
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := f.store.FreezeSynthesisGoalCatalog(ctx, created.Request, page)
			if err != nil {
				t.Fatal(err)
			}
			var prepared [2][]app.SynthesisGoalSelection
			var errs [2]error
			var wg sync.WaitGroup
			for j := range prepared {
				wg.Add(1)
				go func(j int) {
					defer wg.Done()
					prepared[j], errs[j] = store.PrepareGoalSelections(ctx, f.workspace, created.Request.ID, 1)
				}(j)
			}
			wg.Wait()
			for _, e := range errs {
				if e != nil {
					organizingIntegrationFatal(t, e)
				}
			}
			if errs[0] != nil || errs[1] != nil || len(prepared[0]) != 1 || !reflect.DeepEqual(prepared[0], prepared[1]) {
				t.Fatalf("manifest replay %+v %v", prepared, errs)
			}
			selection := prepared[0][0]
			if _, err := store.GetGoalSelection(ctx, f.otherWorkspace, selection.ID); !organizingIntegrationError(err, foundation.ErrorNotFound, "SYNTHESIS_GOAL_NOT_FOUND") {
				t.Fatalf("cross workspace %v", err)
			}
			if err := f.db.Exec(`UPDATE organizing.synthesis_goal_selection SET payload_hash=repeat('f',64),version=version+1 WHERE id=?`, string(selection.ID)).Error; err == nil {
				t.Fatal("frozen payload changed")
			}
			if err := f.db.Exec(`INSERT INTO organizing.synthesis_goal_selection(workspace_id,request_id,catalog_batch_id,source_ordinal,point_offset,point_count,payload_hash) VALUES(?,?,?,0,1,1,repeat('a',64))`, string(f.workspace), string(created.Request.ID), string(frozen.Batch.ID)).Error; err == nil {
				t.Fatal("sealed manifest accepted another slice")
			}
			execution := seedGoalSelectionExecution(t, f, store, selection, 190000+i*100)
			selection, err = store.GetGoalSelection(ctx, f.workspace, selection.ID)
			if err != nil {
				t.Fatal(err)
			}
			provider := agentapp.NewDeterministicChatModel(agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: profile.Model, Content: []byte(tc.raw), Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 10, TotalTokens: 20}}})
			controlled := &goalSelectionHookProvider{base: provider}
			if tc.name == "cancelled" {
				controlled.before = func() error {
					return f.db.Exec(`UPDATE workflow.run SET cancel_requested_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(execution.RunID)).Error
				}
			}
			proofStore := &goalSelectionTamperProbe{GORMGoalSelectionStore: store, probe: tc.name == "selected"}
			model, err := organizingagent.NewGoalSelectionModel(organizingagent.GoalSelectionModelDependencies{Model: controlled, ModelRuns: runs, Store: proofStore, Catalog: catalog, Scheduler: scheduler, ProfileRef: profile.Ref, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "selected" {
				// 首次请求已领取且提供方尚未返回时再次进入；同一持久化任务不得发起第二次模型调用。
				controlled.before = func() error {
					_, err := model.Select(ctx, execution, selection)
					if err == nil {
						return errors.New("running selection re-entered")
					}
					current, err := store.GetGoalSelection(ctx, f.workspace, selection.ID)
					if err != nil {
						return err
					}
					if current.Status != app.GoalSelectionRunning {
						return errors.New("second entrant changed claimed request")
					}
					return nil
				}
			}
			var completed app.SynthesisGoalSelection
			var modelErr error
			if tc.name == "selected" {
				proofStore.inputReady = make(chan struct{}, 2)
				proofStore.inputRelease = make(chan struct{})
				type outcome struct {
					selection app.SynthesisGoalSelection
					err       error
				}
				outcomes := make(chan outcome, 2)
				for j := 0; j < 2; j++ {
					go func() { value, err := model.Select(ctx, execution, selection); outcomes <- outcome{value, err} }()
				}
				<-proofStore.inputReady
				<-proofStore.inputReady
				close(proofStore.inputRelease)
				first, second := <-outcomes, <-outcomes
				if first.err == nil {
					completed, modelErr = first.selection, first.err
				} else {
					completed, modelErr = second.selection, second.err
				}
				if (first.err == nil) == (second.err == nil) {
					t.Fatalf("concurrent claim did not yield exactly one winner: %v %v", first.err, second.err)
				}
			} else {
				completed, modelErr = model.Select(ctx, execution, selection)
			}
			if (tc.status == app.GoalSelectionSucceeded) != (modelErr == nil) {
				t.Fatalf("model result %+v %v", completed, modelErr)
			}
			persisted, err := store.GetGoalSelection(ctx, f.workspace, selection.ID)
			if err != nil || persisted.Status != tc.status || provider.CallCount() != 1 {
				t.Fatalf("persisted %+v calls=%d err=%v modelerr=%v", persisted, provider.CallCount(), err, modelErr)
			}
			resultPage, err := store.ReadGoalSelectionResults(ctx, app.GoalSelectionResultQuery{WorkspaceID: f.workspace, RequestID: created.Request.ID, Limit: 1})
			if err != nil || resultPage.Progress.Ready() != (tc.status == app.GoalSelectionSucceeded) {
				t.Fatalf("selection result progress %+v %v", resultPage, err)
			}
			if tc.status != app.GoalSelectionSucceeded {
				if len(resultPage.Items) != 0 || resultPage.NextAfterSelectionID != "" {
					t.Fatal("failed or uncertain selection exposed generation input")
				}
				if len(persisted.ModelOutput) != 0 || persisted.ModelRunID != "" {
					t.Fatal("unaccepted output became selection")
				}
				if _, err := model.Select(ctx, execution, persisted); err == nil {
					t.Fatal("terminal failure reexecuted")
				}
				if provider.CallCount() != 1 {
					t.Fatal("failure replay invoked provider")
				}
				return
			}
			if !bytes.Equal(persisted.ModelOutput, []byte(tc.raw)) {
				t.Fatal("raw output bytes changed")
			}
			if tc.name == "selected" && !proofStore.rejected {
				t.Fatal("forged model output was not rejected")
			}
			record, err := runs.GetModelRun(ctx, f.workspace, persisted.ModelRunID)
			if err != nil || len(record.Calls) != 1 {
				t.Fatalf("audit %+v %v", record, err)
			}
			actual, _ := json.Marshal(provider.Calls()[0])
			if record.Run.FinalResultType != agentdomain.ResultTypeGoalSelection || record.Calls[0].RequestHash != sha256Hex(actual) || persisted.ModelInputHash != sha256Hex(actual) || record.Calls[0].ResponseHash != sha256Hex([]byte(tc.raw)) {
				t.Fatal("model proof did not bind actual bytes")
			}
			replay, err := model.Select(ctx, execution, persisted)
			if err != nil || replay.ModelRunID != persisted.ModelRunID || provider.CallCount() != 1 {
				t.Fatalf("success replay %+v %v", replay, err)
			}
			duplicate, err := store.CompleteGoalSelection(ctx, app.CompleteGoalSelectionCommand{WorkspaceID: f.workspace, SelectionID: selection.ID, ExpectedVersion: persisted.Version - 1, ModelRunID: persisted.ModelRunID, ModelOutput: persisted.ModelOutput})
			if err != nil || !reflect.DeepEqual(duplicate, persisted) {
				t.Fatalf("completion replay %+v %v", duplicate, err)
			}
			input, err := store.ReadGoalSelectionInput(ctx, f.workspace, selection.ID)
			if err != nil {
				t.Fatal(err)
			}
			points, err := app.BindGoalSelectionOutput(persisted.ModelOutput, input)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "selected" && (len(points) != 1 || points[0].Source != source.Input.SourceEvent.Source || points[0].Locator.ProfileRevisionID != frozen.Batch.Items[0].ProfileRevisionID || points[0].SourceSpanIDs[0] != source.Input.Sources[0].Reference.SourceSpanID) {
				t.Fatalf("wrong bound point %+v", points)
			}
		})
	}
	// 即使行数匹配，调用方也不能提交遗漏知识点的清单；失败事务不得留下部分清单。
	goal, err := f.store.CreateSynthesisGoal(ctx, app.CreateSynthesisGoalCommand{WorkspaceID: f.workspace, Goal: "完整覆盖测试", IdempotencyKey: "selection-incomplete"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := directory.ReadSynthesisGoalCatalog(ctx, app.SynthesisGoalCatalogQuery{WorkspaceID: f.workspace, Limit: 32})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := f.store.FreezeSynthesisGoalCatalog(ctx, goal.Request, page)
	if err != nil {
		t.Fatal(err)
	}
	err = store.within(ctx, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO organizing.synthesis_goal_selection_manifest(batch_id,workspace_id,selection_count) VALUES(?,?,1)`, string(frozen.Batch.ID), string(f.workspace)).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO organizing.synthesis_goal_selection(workspace_id,request_id,catalog_batch_id,source_ordinal,point_offset,point_count,payload_hash) VALUES(?,?,?,0,1,1,repeat('a',64))`, string(f.workspace), string(goal.Request.ID), string(frozen.Batch.ID)).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE organizing.synthesis_goal_selection_manifest SET sealed=true WHERE batch_id=?`, string(frozen.Batch.ID)).Error
	})
	if err == nil {
		t.Fatal("manifest omitted first point but committed")
	}
	f.count(t, "organizing.synthesis_goal_selection_manifest", "batch_id", string(frozen.Batch.ID), 0)
	repaired, err := store.PrepareGoalSelections(ctx, f.workspace, goal.Request.ID, 1)
	if err != nil || len(repaired) != 1 || repaired[0].PointOffset != 0 {
		t.Fatalf("rolled-back manifest could not recover %+v %v", repaired, err)
	}
	f.count(t, "organizing.synthesis_note", "workspace_id", string(f.workspace), 0)
}

// Workflow 行是有效持久化执行测试数据，不证明 River 调度；下方模型调用和所属模块完成操作均经过真实生产路径。
func seedGoalSelectionExecution(t *testing.T, f *synthesisDBFixture, store *GORMGoalSelectionStore, selection app.SynthesisGoalSelection, base int) workflowapp.ExecutionContext {
	t.Helper()
	ctx := t.Context()
	definition, run, node, attempt := organizingIntegrationID(base), organizingIntegrationID(base+1), organizingIntegrationID(base+2), organizingIntegrationID(base+3)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := f.db.Exec(`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES(?,?,'organizing.goal-point-selection',1,'{}',?) ON CONFLICT(workspace_id,key,version) DO NOTHING`, string(definition), string(f.workspace), now).Error; err != nil {
		t.Fatal(err)
	}
	var storedDefinition string
	if err := f.db.Raw(`SELECT id FROM workflow.definition WHERE workspace_id=? AND key='organizing.goal-point-selection' AND version=1`, string(f.workspace)).Scan(&storedDefinition).Error; err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"selection_id": selection.ID, "expected_version": selection.Version})
	if err := f.db.Exec(`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,created_at,updated_at) VALUES(?,?,?,'running',?::jsonb,?,?)`, string(run), string(f.workspace), storedDefinition, string(input), now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		return store.BindGoalSelectionWorkflowScoped(ctx, scope, f.workspace, selection.ID, run)
	}); err != nil {
		t.Fatal(err)
	}
	seedOrganizingRunningNodeAttempt(t, ctx, f.platform.DB(), run, node, attempt, "organizing.goal-point-selection.run", now.Add(time.Hour), now)
	return workflowapp.ExecutionContext{WorkspaceID: f.workspace, RunID: run, NodeRunID: node, NodeAttemptID: attempt}
}

type goalSelectionHookProvider struct {
	base   agentapp.ChatModel
	before func() error
}

func (p *goalSelectionHookProvider) Chat(ctx context.Context, r agentapp.ChatRequest) (agentapp.ChatResponse, error) {
	if p.before != nil {
		if err := p.before(); err != nil {
			return agentapp.ChatResponse{}, err
		}
	}
	return p.base.Chat(ctx, r)
}

type goalSelectionTamperProbe struct {
	*GORMGoalSelectionStore
	probe, rejected bool
	inputReady      chan struct{}
	inputRelease    chan struct{}
}

func (s *goalSelectionTamperProbe) CompleteGoalSelection(ctx context.Context, c app.CompleteGoalSelectionCommand) (app.SynthesisGoalSelection, error) {
	if s.probe {
		forged := c
		forged.ModelOutput = []byte(`{"selections":[{"point":"P001","reason":"Forged reason."}],"explanation":"Forged output."}`)
		_, err := s.GORMGoalSelectionStore.CompleteGoalSelection(ctx, forged)
		if err == nil {
			return app.SynthesisGoalSelection{}, errors.New("forged output accepted")
		}
		s.rejected = organizingIntegrationError(err, foundation.ErrorVersionConflict, "SYNTHESIS_GOAL_CONFLICT")
	}
	return s.GORMGoalSelectionStore.CompleteGoalSelection(ctx, c)
}

func (s *goalSelectionTamperProbe) ReadGoalSelectionInput(ctx context.Context, w, id foundation.ID) (app.SynthesisGoalSelectionInput, error) {
	value, err := s.GORMGoalSelectionStore.ReadGoalSelectionInput(ctx, w, id)
	if err == nil && s.inputReady != nil {
		s.inputReady <- struct{}{}
		select {
		case <-s.inputRelease:
		case <-ctx.Done():
			return value, ctx.Err()
		}
	}
	return value, err
}

func TestGoalSelectionDispatchRunsThroughRiverAndRetriesExplicitly(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 111)
	ctx := t.Context()
	source := f.generation(t, 192000, nil, "Redis eviction and expiration.")
	seedGoalCatalogProfile(t, f, source, 192000, "Redis")
	if err := f.db.Exec(`UPDATE core.workspace SET status='active',version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(f.workspace)).Error; err != nil {
		t.Fatal(err)
	}
	workspaces, err := workspacepostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := agentpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := capturepostgres.NewGORMProfileRepository(f.platform, runs)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := organizingowner.NewSynthesisGoalCatalogReader(profiles, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewGORMGoalSelectionStore(f.platform, f.store, directory, runs)
	if err != nil {
		t.Fatal(err)
	}
	migrator, err := riveradapter.NewMigrator(f.platform.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(f.platform, riveradapter.DefaultOptions(), riveradapter.NewStaticScopedEnqueueFence(), workflowpostgres.GORMRuntimeRepositoryHooks{})
	if err != nil {
		t.Fatal(err)
	}
	workflowRuns, err := workflowpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	catalog := agentapp.NewRuntimeCatalog()
	if err := organizingagent.RegisterGoalSelectionRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "goal-river-test", Version: "v1"}, Model: agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "goal-river", ModelVersion: "v1"}, Timeout: time.Second, MaxOutputTokens: 2048}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	provider := agentapp.NewDeterministicChatModel(
		agentapp.DeterministicChatStep{Err: foundation.NewError(foundation.ErrorDependencyUnavailable, "TEST_GOAL_PROVIDER_UNAVAILABLE", true, nil)},
		agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: profile.Model, Content: []byte(`{"selections":[{"point":"P001","reason":"Redis eviction supports the goal."}],"explanation":"Relevant Redis point."}`), Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 10, TotalTokens: 20}}})
	scheduler, err := agenteino.NewStructuredPhaseScheduler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := organizingagent.NewGoalSelectionModel(organizingagent.GoalSelectionModelDependencies{Model: provider, ModelRuns: runs, Store: store, Catalog: catalog, Scheduler: scheduler, ProfileRef: profile.Ref, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := organizingworkflow.NewGoalSelectionExecutor(workflowRuns, store, &cancelFirstGoalSelection{model: model})
	if err != nil {
		t.Fatal(err)
	}
	validation, err := workflowapp.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapp.NewExecutorRegistry(validation)
	if err != nil {
		t.Fatal(err)
	}
	if err := executors.Register(organizingworkflow.GoalSelectionNodeKind, 1, executor); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := workflowapp.NewDefinitionRegistry(validation, executors)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range organizingworkflow.GoalSelectionDefinitions() {
		if err := definitions.Register(definition); err != nil {
			t.Fatal(err)
		}
	}
	if err := definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapp.NewRuntimeCoordinator(runtime)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := riveradapter.NewRuntimeNodeWorker(executors, coordinator, "goal-selection-integration", 10*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	workers := riveradapter.NewWorkers()
	if err := riveradapter.AddRuntimeWorkerSafely(workers, worker); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(f.platform.DB(), workers)
	if err != nil {
		t.Fatal(err)
	}
	uow, err := f.platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := organizingworkflow.GoalSelectionDispatcher{Workspaces: workspaces, Store: store, UnitOfWork: uow, Starter: runtime, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}}
	createGoal := func(key string) app.SynthesisGoalRequest {
		t.Helper()
		goal, err := f.store.CreateSynthesisGoal(ctx, app.CreateSynthesisGoalCommand{WorkspaceID: f.workspace, Goal: "整理 Redis 专项", IdempotencyKey: key})
		if err != nil {
			t.Fatal(err)
		}
		catalogDispatcher := organizingworkflow.SynthesisGoalCatalogDispatcher{Workspaces: workspaces, Store: f.store, Catalog: directory}
		if _, err := catalogDispatcher.DispatchBatch(ctx, 4); err != nil {
			t.Fatal(err)
		}
		return goal.Request
	}
	goal := createGoal("river-goal-cancel-before-start")
	if count, err := dispatcher.DispatchBatch(ctx, 4); err != nil || count != 1 {
		if err != nil {
			organizingIntegrationFatal(t, err)
		}
		t.Fatalf("dispatch count=%d", count)
	}
	selections, err := store.PrepareGoalSelections(ctx, f.workspace, goal.ID, 1)
	if err != nil || len(selections) != 1 {
		t.Fatalf("prepared %+v %v", selections, err)
	}
	selection := selections[0]
	run, err := workflowRuns.GetRun(ctx, selection.ScheduledWorkflowID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Cancel(ctx, workflowapp.RunControlCommand{WorkflowRunID: run.ID, ExpectedVersion: run.Version, IdempotencyKey: "cancel-goal-before-start"}); err != nil {
		t.Fatal(err)
	}
	if count, err := store.ReconcileGoalSelectionWorkflows(ctx, f.workspace, 4); err != nil || count != 1 {
		t.Fatalf("reconcile cancelled=%d %v", count, err)
	}
	failed, err := store.GetGoalSelection(ctx, f.workspace, selection.ID)
	if err != nil || failed.Status != app.GoalSelectionFailed || !failed.Retryable || provider.CallCount() != 0 {
		t.Fatalf("cancelled selection %+v %v", failed, err)
	}
	retry := app.RetryGoalSelectionCommand{WorkspaceID: f.workspace, SelectionID: failed.ID, ExpectedVersion: failed.Version, IdempotencyKey: "retry-cancelled-goal"}
	reset, err := store.RetryGoalSelection(ctx, retry)
	if err != nil || reset.Status != app.GoalSelectionPending || reset.ScheduledWorkflowID != "" {
		t.Fatalf("retry reset %+v %v", reset, err)
	}
	replay, err := store.RetryGoalSelection(ctx, retry)
	if err != nil || replay.Version != reset.Version {
		t.Fatalf("retry replay %+v %v", replay, err)
	}
	changed := retry
	changed.ExpectedVersion++
	if _, err := store.RetryGoalSelection(ctx, changed); err == nil {
		t.Fatal("same retry key changed expected version")
	}
	if count, err := dispatcher.DispatchBatch(ctx, 4); err != nil || count != 1 {
		t.Fatalf("redispatch %d %v", count, err)
	}
	if _, err := store.FailScheduledGoalSelection(ctx, f.workspace, failed.ID, selection.ScheduledWorkflowID, "OLD_WORKFLOW_FAILURE", true); err == nil {
		t.Fatal("old workflow overwrote retry")
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Stop(stopCtx); err != nil {
			t.Error(err)
		}
	})
	wait := func(want app.GoalSelectionStatus) app.SynthesisGoalSelection {
		t.Helper()
		deadline := time.NewTimer(12 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			value, err := store.GetGoalSelection(ctx, f.workspace, selection.ID)
			if err != nil {
				t.Fatal(err)
			}
			run, err := workflowRuns.GetRun(ctx, value.ScheduledWorkflowID)
			if err != nil {
				t.Fatal(err)
			}
			if value.Status == want && (run.Status == workflowdomain.RunStatusFailed || run.Status == workflowdomain.RunStatusSucceeded) {
				return value
			}
			select {
			case <-deadline.C:
				t.Fatalf("selection=%+v run=%+v", value, run)
			case <-ticker.C:
			}
		}
	}
	failed = wait(app.GoalSelectionFailed)
	if !failed.Retryable || failed.ModelRunID != "" || provider.CallCount() != 0 {
		t.Fatalf("pre-claim cancellation stranded selection %+v calls=%d", failed, provider.CallCount())
	}
	retry = app.RetryGoalSelectionCommand{WorkspaceID: f.workspace, SelectionID: failed.ID, ExpectedVersion: failed.Version, IdempotencyKey: "retry-pre-claim-cancel"}
	if _, err := store.RetryGoalSelection(ctx, retry); err != nil {
		t.Fatal(err)
	}
	if count, err := dispatcher.DispatchBatch(ctx, 4); err != nil || count != 1 {
		t.Fatalf("pre-claim cancellation retry %d %v", count, err)
	}
	failed = wait(app.GoalSelectionFailed)
	if failed.ErrorCode != "TEST_GOAL_PROVIDER_UNAVAILABLE" || !failed.Retryable || provider.CallCount() != 1 {
		t.Fatalf("provider failure %+v calls=%d", failed, provider.CallCount())
	}
	if count, err := dispatcher.DispatchBatch(ctx, 4); err != nil || count != 0 {
		t.Fatalf("failure implicitly retried %d %v", count, err)
	}
	retry = app.RetryGoalSelectionCommand{WorkspaceID: f.workspace, SelectionID: failed.ID, ExpectedVersion: failed.Version, IdempotencyKey: "retry-provider-goal"}
	if _, err := store.RetryGoalSelection(ctx, retry); err != nil {
		t.Fatal(err)
	}
	if count, err := dispatcher.DispatchBatch(ctx, 4); err != nil || count != 1 {
		t.Fatalf("provider retry %d %v", count, err)
	}
	succeeded := wait(app.GoalSelectionSucceeded)
	if succeeded.ModelRunID == "" || provider.CallCount() != 2 {
		t.Fatalf("success %+v calls=%d", succeeded, provider.CallCount())
	}
	if count, err := dispatcher.DispatchBatch(ctx, 4); err != nil || count != 0 {
		t.Fatalf("success rescheduled %d %v", count, err)
	}
	if replay, err := store.RetryGoalSelection(ctx, retry); err != nil || replay.ModelRunID != succeeded.ModelRunID || provider.CallCount() != 2 {
		t.Fatalf("retry response loss changed success %+v %v", replay, err)
	}
	f.count(t, "organizing.synthesis_note", "workspace_id", string(f.workspace), 0)
}

// 在持久化领取前取消真实模型的首次元数据读取；尚未发起提供方请求时，执行器必须保留显式重试能力。
type cancelFirstGoalSelection struct {
	model organizingworkflow.GoalSelectionModel
	once  sync.Once
}

func (m *cancelFirstGoalSelection) Select(ctx context.Context, execution workflowapp.ExecutionContext, selection app.SynthesisGoalSelection) (app.SynthesisGoalSelection, error) {
	m.once.Do(func() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		cancel()
	})
	return m.model.Select(ctx, execution, selection)
}
