//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	authoringchange "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingagent "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/agent"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	synthesispostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/synthesispostgres"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizinghttp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/http"
	ow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	wa "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"github.com/gin-gonic/gin"
)

func TestGoalGenerationDispatchesThroughRiverAndRetriesOneCandidate(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 134)
	ctx := t.Context()
	var promotedSourceVersion foundation.ID
	for i := 0; i < 2; i++ {
		base := 206000 + i*1000
		record := f.generation(t, base, nil, fmt.Sprintf("Redis expiration bounds cache lifetime. Source %d.", i))
		seedGoalCatalogProfile(t, f, record, base, "Redis")
		if i == 0 {
			promotedSourceVersion = record.Input.SourceEvent.Source.SourceVersionID
		}
	}
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
	results, err := NewGORMGoalSelectionResultReader(f.platform, directory)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := app.NewSynthesisGoalBindingProof(results)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := workflowpostgres.NewGORMRuntimeBindingReader(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	processing, err := synthesispostgres.NewStore(f.platform, synthesispostgres.Dependencies{ModelRuns: runs, WorkflowFence: fence, WorkflowBindings: bindings, Goals: proof})
	if err != nil {
		t.Fatal(err)
	}
	retirer, err := authoringchange.NewGeneratedPublicationRetirer(f.proposals)
	if err != nil {
		t.Fatal(err)
	}
	notesStore, err := NewGORMSynthesisStore(f.platform, SynthesisStoreDependencies{Authoring: f.authoring, Retirer: retirer, Sources: f.sources, Validated: processing, Anchors: synthesisNoAnchorFence{}})
	if err != nil {
		t.Fatal(err)
	}
	targets := synthesisTargetFixture{}
	proposalService, err := changecontrolapp.NewService(f.proposals, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, targets, targets)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := authoringchange.NewProposalCreator(proposalService, targets)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := authoringapp.NewService(authoringapp.Dependencies{Repository: f.authoring, Proposals: creator, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	notes, err := app.NewSynthesisService(app.SynthesisDependencies{Store: notesStore, Sources: f.sources, Publications: publisher, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	selections, err := NewGORMGoalSelectionStore(f.platform, notesStore, directory, runs)
	if err != nil {
		t.Fatal(err)
	}
	materials, err := organizingowner.NewSynthesisGoalMaterialReader(selections, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := app.NewSynthesisGoalPreparer(materials, f.sources)
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
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(f.platform, riveradapter.DefaultOptions(), riveradapter.NewStaticScopedEnqueueFence(), workflowpostgres.GORMRuntimeRepositoryHooks{Terminal: processing})
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
	if err := organizingagent.RegisterSynthesisRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "goal-generation-river", Version: "v1"}, Model: agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "goal-generation", ModelVersion: "v1"}, Timeout: time.Second, MaxOutputTokens: 2048}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	raw := []string{
		`{"selections":[],"explanation":"No relevant Oracle knowledge."}`,
		`{"selections":[],"explanation":"No relevant Oracle knowledge."}`,
		`{"selections":[{"point":"P001","reason":"Redis expiration is relevant."}],"explanation":"Relevant Redis point."}`,
		`{"selections":[{"point":"P001","reason":"Redis expiration is relevant."}],"explanation":"Relevant Redis point."}`,
		`{"notes":[{"note":"","topic_key":"redis goal","title":"Redis 专项","aliases":[],"operations":[{"op":"ADD_FACT","statement":{"text":"Redis expiration bounds cache lifetime.","applicability":"","sources":["S001","S002"]}}]}]}`,
		`{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"},{"source":"S002","verdict":"SUPPORTED"}]}]}`,
		`{"selections":[{"point":"P001","reason":"This original Redis note is relevant."}],"explanation":"Relevant source point."}`,
		`{"notes":[{"note":"","topic_key":"redis source promotion","title":"Redis 单来源主笔记","aliases":[],"operations":[{"op":"ADD_FACT","statement":{"text":"Redis expiration bounds cache lifetime.","applicability":"","sources":["S001"]}}]}]}`,
		`{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"}]}]}`,
	}
	steps := make([]agentapp.DeterministicChatStep, len(raw))
	for i, output := range raw {
		steps[i] = agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: profile.Model, Content: []byte(output), Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 10, TotalTokens: 20}}}
	}
	provider := agentapp.NewDeterministicChatModel(steps...)
	scheduler, err := agenteino.NewStructuredPhaseScheduler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selectionModel, err := organizingagent.NewGoalSelectionModel(organizingagent.GoalSelectionModelDependencies{Model: provider, ModelRuns: runs, Store: selections, Catalog: catalog, Scheduler: scheduler, ProfileRef: profile.Ref, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	selectionExecutor, err := ow.NewGoalSelectionExecutor(workflowRuns, selections, selectionModel)
	if err != nil {
		t.Fatal(err)
	}
	synthesisModel, err := organizingagent.NewSynthesisModel(organizingagent.SynthesisModelDependencies{Model: provider, ModelRuns: runs, Store: processing, Catalog: catalog, Scheduler: scheduler, ProfileRef: profile.Ref, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	switchModel := &goalGenerationSwitch{SynthesisExecutionModel: synthesisModel}
	switchModel.disabled.Store(true)
	synthesisExecutor, err := ow.NewSynthesisExecutor(ow.SynthesisExecutorDependencies{Runs: workflowRuns, Store: processing, Candidates: notes, Sources: f.sources, Goals: preparer, Model: switchModel, Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	validation, err := wa.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := wa.NewExecutorRegistry(validation)
	if err != nil {
		t.Fatal(err)
	}
	if err := executors.Register(ow.GoalSelectionNodeKind, 1, selectionExecutor); err != nil {
		t.Fatal(err)
	}
	for _, kind := range ow.SynthesisExecutorNodeKinds() {
		if err := executors.Register(kind, 1, synthesisExecutor); err != nil {
			t.Fatal(err)
		}
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := wa.NewDefinitionRegistry(validation, executors)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range append(ow.SynthesisRegisteredDefinitions(), ow.GoalSelectionDefinitions()...) {
		if err := definitions.Register(definition); err != nil {
			t.Fatal(err)
		}
	}
	if err := definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	coordinator, err := wa.NewRuntimeCoordinator(runtime)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := riveradapter.NewRuntimeNodeWorker(executors, coordinator, "goal-generation-river", 10*time.Second, time.Second)
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
	service, err := ow.NewSynthesisProcessingService(ow.SynthesisProcessingServiceDependencies{Queries: processing, Retries: processing, Applied: notesStore, UnitOfWork: f.uow, Starter: runtime, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	goalViews, err := NewGORMGoalViewReader(f.platform, processing)
	if err != nil {
		t.Fatal(err)
	}
	handler := organizinghttp.NewSynthesisHandlerWithGoals(notes, service, nil, notesStore, goalViews, selections, 5*time.Second)
	router := gin.New()
	handler.Routes(router.Group("/api/v1"))
	baseURL := "/api/v1/workspaces/" + string(f.workspace) + "/synthesis"
	requestHTTP := func(method, path, body, key string, expected int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != expected {
			t.Fatalf("goal HTTP %s %s: %d %s", method, path, response.Code, response.Body.String())
		}
		return response.Body.Bytes()
	}
	createHTTP := func(goalText, key string) foundation.ID {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"goal": goalText})
		raw := requestHTTP(http.MethodPost, baseURL+"/goals", string(body), key, http.StatusAccepted)
		var result struct {
			Request struct {
				ID foundation.ID `json:"id"`
			}
			Replayed bool
		}
		if err := json.Unmarshal(raw, &result); err != nil || result.Request.ID == "" {
			t.Fatalf("goal create response %s %v", raw, err)
		}
		repeated := requestHTTP(http.MethodPost, baseURL+"/goals", string(body), key, http.StatusAccepted)
		var replay struct {
			Request struct {
				ID foundation.ID `json:"id"`
			}
			Replayed bool
		}
		if err := json.Unmarshal(repeated, &replay); err != nil || !replay.Replayed || replay.Request.ID != result.Request.ID {
			t.Fatalf("goal create replay %s %v", repeated, err)
		}
		return result.Request.ID
	}
	catalogDispatcher := ow.SynthesisGoalCatalogDispatcher{Workspaces: workspaces, Store: notesStore, Catalog: directory}
	selectionDispatcher := ow.GoalSelectionDispatcher{Workspaces: workspaces, Store: selections, UnitOfWork: f.uow, Starter: runtime, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}}
	generationDispatcher := ow.GoalGenerationDispatcher{Workspaces: workspaces, Store: processing, UnitOfWork: f.uow, Starter: runtime, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}}
	waitSelections := func(goalID foundation.ID) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			progress, err := selections.ReadGoalSelectionProgress(ctx, f.workspace, goalID)
			if err != nil {
				t.Fatal(err)
			}
			if progress.Ready() {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("selections did not complete: %+v", progress)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	emptyGoal, err := notesStore.CreateSynthesisGoal(ctx, app.CreateSynthesisGoalCommand{WorkspaceID: f.workspace, Goal: "整理 Oracle 知识", IdempotencyKey: "goal-generation-empty"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalogDispatcher.DispatchBatch(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if n, err := selectionDispatcher.DispatchBatch(ctx, 4); err != nil || n != 2 {
		t.Fatalf("empty goal selection dispatch %d %v", n, err)
	}
	waitSelections(emptyGoal.Request.ID)
	if batch, err := generationDispatcher.DispatchBatch(ctx, 4); err != nil || batch.Started != 0 {
		t.Fatalf("empty selection generated a note: %+v %v", batch, err)
	}
	f.count(t, "organizing.synthesis_processing", "goal_request_id", string(emptyGoal.Request.ID), 0)
	goalID := createHTTP("整理 Redis 专项知识", "goal-generation-river")
	goalRequest, err := notesStore.GetSynthesisGoal(ctx, f.workspace, goalID)
	if err != nil {
		t.Fatal(err)
	}
	goal := app.SynthesisGoalCreateResult{Request: goalRequest}
	if _, err := catalogDispatcher.DispatchBatch(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if batch, err := generationDispatcher.DispatchBatch(ctx, 4); err != nil || batch.Started != 0 {
		t.Fatalf("generation started before selections: %+v %v", batch, err)
	}
	if n, err := selectionDispatcher.DispatchBatch(ctx, 4); err != nil || n != 2 {
		t.Fatalf("selection dispatch: %d %v", n, err)
	}
	waitSelections(goal.Request.ID)
	// 来源消费者先锁定 outbox，再进入工作区接收锁。目标处理应跳过该事件，避免处理记录外键获取键共享锁时按相反顺序等待。
	outboxLocked, releaseOutbox := make(chan struct{}), make(chan struct{})
	outboxDone := make(chan error, 1)
	go func() {
		outboxDone <- f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			tx, err := platformpostgres.GORMTransaction(scope)
			if err != nil {
				return err
			}
			var ids []string
			if err := tx.WithContext(ctx).Raw(`SELECT id FROM workflow.outbox_event WHERE workspace_id=? AND event_type='ingestion.source.ready' ORDER BY id FOR UPDATE`, string(f.workspace)).Scan(&ids).Error; err != nil {
				return err
			}
			close(outboxLocked)
			select {
			case <-releaseOutbox:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-outboxLocked:
	case err := <-outboxDone:
		t.Fatalf("outbox lock fixture failed: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	lockedCtx, cancelLocked := context.WithTimeout(ctx, time.Second)
	lockedBatch, lockedErr := generationDispatcher.DispatchBatch(lockedCtx, 4)
	cancelLocked()
	close(releaseOutbox)
	if err := <-outboxDone; err != nil {
		t.Fatal(err)
	}
	if lockedErr != nil || lockedBatch.Started != 0 {
		t.Fatalf("goal waited for locked provenance event: %+v %v", lockedBatch, lockedErr)
	}
	type dispatchOutcome struct {
		batch ow.SynthesisDispatchBatchResult
		err   error
	}
	outcomes := make(chan dispatchOutcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			batch, err := generationDispatcher.DispatchBatch(ctx, 4)
			outcomes <- dispatchOutcome{batch, err}
		}()
	}
	started := 0
	for i := 0; i < 2; i++ {
		out := <-outcomes
		if out.err != nil {
			t.Fatal(out.err)
		}
		started += out.batch.Started
	}
	if started != 1 {
		t.Fatalf("concurrent generation dispatch started %d workflows", started)
	}
	lookup := func(goalID foundation.ID) app.SynthesisProcessing {
		var out app.SynthesisProcessing
		if err := f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			var found bool
			var err error
			out, found, err = processing.FindSynthesisGoalProcessingScoped(ctx, scope, f.workspace, goalID)
			if err == nil && !found {
				t.Fatal("missing goal processing")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	wait := func(goalID foundation.ID, want app.SynthesisProcessingStatus) app.SynthesisProcessing {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			value := lookup(goalID)
			if value.Status == want {
				return value
			}
			if value.Status == app.SynthesisProcessingFailed && want != app.SynthesisProcessingFailed {
				t.Fatalf("goal unexpectedly failed: %+v", value)
			}
			if time.Now().After(deadline) {
				t.Fatalf("goal did not reach %s: %+v", want, value)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	failed := wait(goal.Request.ID, app.SynthesisProcessingFailed)
	failedRaw := requestHTTP(http.MethodGet, baseURL+"/goals/"+string(goal.Request.ID), "", "", http.StatusOK)
	var failedView struct {
		Processing struct {
			ID     foundation.ID
			Status string
		}
		Candidate *json.RawMessage
	}
	if err := json.Unmarshal(failedRaw, &failedView); err != nil || failedView.Processing.ID != failed.ID || failedView.Processing.Status != "FAILED" || failedView.Candidate != nil {
		t.Fatalf("failed goal view %s %v", failedRaw, err)
	}
	if failed.GoalRequestID != goal.Request.ID || failed.Failure == nil || !failed.Failure.Retryable || provider.CallCount() != 4 {
		t.Fatalf("missing capability state: %+v calls=%d", failed, provider.CallCount())
	}
	if batch, err := generationDispatcher.DispatchBatch(ctx, 4); err != nil || batch.Started != 0 {
		t.Fatalf("failed goal silently restarted: %+v %v", batch, err)
	}
	switchModel.disabled.Store(false)

	retry := app.RetrySynthesisCommand{WorkspaceID: f.workspace, ProcessingID: failed.ID, ExpectedVersion: failed.Version, IdempotencyKey: "goal-generation-retry"}
	retryBody := fmt.Sprintf(`{"expected_version":%d}`, failed.Version)
	requestHTTP(http.MethodPost, baseURL+"/processing/"+string(failed.ID)+"/retry", retryBody, retry.IdempotencyKey, http.StatusAccepted)
	retried, err := service.RetryProcessing(ctx, retry)
	if err != nil || retried.Processing.GoalRequestID != goal.Request.ID || retried.Processing.ID != failed.ID {
		t.Fatalf("goal retry identity: %+v %v", retried, err)
	}
	replay, err := service.RetryProcessing(ctx, retry)
	if err != nil || !replay.Replayed || replay.Processing.WorkflowRunID != retried.Processing.WorkflowRunID {
		t.Fatalf("retry replay changed run: %+v %v", replay, err)
	}
	completed := wait(goal.Request.ID, app.SynthesisProcessingSucceeded)
	if len(completed.RevisionIDs) != 1 || provider.CallCount() != 6 {
		t.Fatalf("goal candidate/calls: %+v %d", completed, provider.CallCount())
	}
	execution, err := processing.LoadSynthesisExecution(ctx, f.workspace, completed.ID, completed.WorkflowRunID)
	if err != nil || execution.Input == nil || execution.Input.Goal == nil || len(execution.Input.Goal.Materials) != 2 || execution.Generation == nil || execution.Semantic == nil || !execution.Semantic.Semantic.Accepted || execution.Generation.ModelRunID == execution.Semantic.ModelRunID {
		t.Fatalf("goal lost independent evidence: %+v %v", execution, err)
	}
	candidates, err := notes.ListCandidates(ctx, f.workspace)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidate list: %+v %v", candidates, err)
	}
	detail, err := notes.GetNote(ctx, f.workspace, candidates[0].Note.ID)
	if err != nil || detail.Publication == nil || detail.PublishedRevision != nil || len(detail.CurrentRevision.Items) != 1 || len(detail.CurrentRevision.Items[0].Fact.Sources) != 2 {
		t.Fatalf("candidate not traceable/unpublished: %+v %v", detail, err)
	}
	for i := 0; i < 2; i++ {
		raw := requestHTTP(http.MethodGet, baseURL+"/goals/"+string(goal.Request.ID), "", "", http.StatusOK)
		var view struct {
			Progress struct {
				Ready          bool
				SelectedPoints int64 `json:"selected_points"`
			}
			Candidate struct {
				NoteID     foundation.ID `json:"note_id"`
				RevisionID foundation.ID `json:"revision_id"`
			}
			Processing struct {
				ID     foundation.ID
				Status string
			}
		}
		if err := json.Unmarshal(raw, &view); err != nil || !view.Progress.Ready || view.Progress.SelectedPoints != 2 || view.Candidate.NoteID != detail.Note.ID || view.Candidate.RevisionID != completed.RevisionIDs[0] || view.Processing.ID != completed.ID || view.Processing.Status != "SUCCEEDED" {
			t.Fatalf("completed goal HTTP %s %v", raw, err)
		}
	}
	selectionRaw := requestHTTP(http.MethodGet, baseURL+"/goals/"+string(goal.Request.ID)+"/selections?limit=1", "", "", http.StatusOK)
	var first struct {
		Items []struct{ ID foundation.ID }
		Next  *string `json:"next_after_id"`
	}
	if err := json.Unmarshal(selectionRaw, &first); err != nil || len(first.Items) != 1 || first.Next == nil {
		t.Fatalf("selection page %s %v", selectionRaw, err)
	}
	secondRaw := requestHTTP(http.MethodGet, baseURL+"/goals/"+string(goal.Request.ID)+"/selections?limit=1&after_id="+*first.Next, "", "", http.StatusOK)
	var second struct {
		Items []struct{ ID foundation.ID }
		Next  *string `json:"next_after_id"`
	}
	if err := json.Unmarshal(secondRaw, &second); err != nil || len(second.Items) != 1 || second.Next != nil || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("selection continuation %s %v", secondRaw, err)
	}
	requestHTTP(http.MethodGet, "/api/v1/workspaces/"+string(f.otherWorkspace)+"/synthesis/goals/"+string(goal.Request.ID), "", "", http.StatusNotFound)
	requestHTTP(http.MethodPost, baseURL+"/goals/"+string(emptyGoal.Request.ID)+"/selections/"+string(first.Items[0].ID)+"/retry", `{"expected_version":1}`, "wrong-goal", http.StatusNotFound)
	requestHTTP(http.MethodGet, baseURL+"/goals?limit=1", "", "", http.StatusOK)
	for _, secret := range []string{"model_output", "model_run_id", "node_attempt_id", "payload_hash"} {
		if strings.Contains(string(selectionRaw), secret) {
			t.Fatalf("selection progress leaked %s", secret)
		}
	}
	for i := 0; i < 2; i++ {
		if batch, err := generationDispatcher.DispatchBatch(ctx, 4); err != nil || batch.Started != 0 {
			t.Fatalf("completed goal redispatched: %+v %v", batch, err)
		}
	}
	f.count(t, "organizing.synthesis_processing", "goal_request_id", string(goal.Request.ID), 1)
	f.count(t, "organizing.synthesis_apply_receipt", "processing_id", string(completed.ID), 1)
	if provider.CallCount() != 6 {
		t.Fatal("reads or redispatch reran models")
	}
	var unpublished int64
	if err := f.db.Table("workflow.outbox_event").Where("workspace_id=? AND event_type='ingestion.source.ready' AND published_at IS NULL", string(f.workspace)).Count(&unpublished).Error; err != nil || unpublished != 2 {
		t.Fatalf("goal consumed original ingestion events: %d %v", unpublished, err)
	}

	promoter, err := app.NewSynthesisSourcePromotionService(notesStore, directory, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	promotionCommand := app.PromoteSynthesisSourceCommand{WorkspaceID: f.workspace, SourceVersionID: promotedSourceVersion, IdempotencyKey: "goal-generation-source-promotion"}
	promotion, err := promoter.PromoteSynthesisSource(ctx, promotionCommand)
	if err != nil || promotion.Replayed || promotion.SourceVersionID != promotionCommand.SourceVersionID || promotion.Request.Status != app.SynthesisGoalCatalogReady || promotion.Request.CatalogBatches != 1 {
		t.Fatalf("source promotion: %+v %v", promotion, err)
	}
	promotionReplay, err := promoter.PromoteSynthesisSource(ctx, promotionCommand)
	if err != nil || !promotionReplay.Replayed || promotionReplay.Request.ID != promotion.Request.ID || provider.CallCount() != 6 {
		t.Fatalf("source promotion replay: %+v calls=%d %v", promotionReplay, provider.CallCount(), err)
	}
	promotedBatch, err := notesStore.GetSynthesisGoalCatalogBatch(ctx, f.workspace, promotion.Request.ID, 1)
	if err != nil || len(promotedBatch.Items) != 1 || promotedBatch.Items[0].Source.SourceVersionID != promotionCommand.SourceVersionID {
		t.Fatalf("source promotion catalog: %+v %v", promotedBatch, err)
	}
	if n, err := selectionDispatcher.DispatchBatch(ctx, 4); err != nil || n != 1 {
		t.Fatalf("source promotion selection dispatch: %d %v", n, err)
	}
	waitSelections(promotion.Request.ID)
	promotionProgress, err := results.ReadGoalSelectionProgress(ctx, f.workspace, promotion.Request.ID)
	if err != nil || !promotionProgress.Ready() || promotionProgress.Selections != 1 || promotionProgress.Succeeded != 1 {
		t.Fatalf("source promotion selection progress: %+v %v", promotionProgress, err)
	}
	promotedMaterials, err := materials.ReadGoalSourceMaterials(ctx, app.GoalSelectionResultQuery{WorkspaceID: f.workspace, RequestID: promotion.Request.ID, Limit: 1})
	if err != nil || len(promotedMaterials.Items) != 1 || promotedMaterials.Items[0].Reference.Source.SourceVersionID != promotionCommand.SourceVersionID || len(promotedMaterials.Items[0].Points) != 1 {
		t.Fatalf("source promotion material binding: %+v %v", promotedMaterials, err)
	}
	if batch, err := generationDispatcher.DispatchBatch(ctx, 4); err != nil || batch.Started != 1 {
		t.Fatalf("source promotion generation dispatch: %+v %v", batch, err)
	}
	promotedProcessing := wait(promotion.Request.ID, app.SynthesisProcessingSucceeded)
	if promotedProcessing.GoalRequestID != promotion.Request.ID || len(promotedProcessing.RevisionIDs) != 1 || provider.CallCount() != 9 {
		t.Fatalf("source promotion processing: %+v calls=%d", promotedProcessing, provider.CallCount())
	}
	promotedExecution, err := processing.LoadSynthesisExecution(ctx, f.workspace, promotedProcessing.ID, promotedProcessing.WorkflowRunID)
	if err != nil || promotedExecution.Input == nil || promotedExecution.Input.Goal == nil || promotedExecution.Input.Goal.RequestID != promotion.Request.ID || len(promotedExecution.Input.Goal.Materials) != 1 || promotedExecution.Input.Goal.Materials[0].Reference.Source.SourceVersionID != promotionCommand.SourceVersionID {
		t.Fatalf("source promotion execution binding: %+v %v", promotedExecution, err)
	}
	candidates, err = notes.ListCandidates(ctx, f.workspace)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("source promotion candidate list: %+v %v", candidates, err)
	}
	var promotedDetail app.SynthesisNoteDetail
	for _, candidate := range candidates {
		detail, err := notes.GetNote(ctx, f.workspace, candidate.Note.ID)
		if err != nil {
			t.Fatal(err)
		}
		if detail.CurrentRevision != nil && detail.CurrentRevision.ID == promotedProcessing.RevisionIDs[0] {
			promotedDetail = detail
			break
		}
	}
	if promotedDetail.CurrentRevision == nil || promotedDetail.PublishedRevision != nil || promotedDetail.Publication == nil || promotedDetail.CurrentRevision.SourceEventID != promotedProcessing.SourceEvent.ID || len(promotedDetail.CurrentRevision.Items) != 1 || promotedDetail.CurrentRevision.Items[0].Fact == nil || len(promotedDetail.CurrentRevision.Items[0].Fact.Sources) != 1 || promotedDetail.CurrentRevision.Items[0].Fact.Sources[0].Source.SourceVersionID != promotionCommand.SourceVersionID {
		t.Fatalf("source promotion candidate binding: %+v", promotedDetail)
	}
	for i := 0; i < 2; i++ {
		if batch, err := generationDispatcher.DispatchBatch(ctx, 4); err != nil || batch.Started != 0 {
			t.Fatalf("completed source promotion redispatched: %+v %v", batch, err)
		}
	}
	if provider.CallCount() != 9 {
		t.Fatalf("source promotion replay or redispatch reran models: %d", provider.CallCount())
	}

}

type goalGenerationSwitch struct {
	ow.SynthesisExecutionModel
	disabled atomic.Bool
}

func (s *goalGenerationSwitch) GenerateSynthesisForExecution(ctx context.Context, execution wa.ExecutionContext, input app.SynthesisGenerationInput) (app.SynthesisGenerationResult, error) {
	if s.disabled.Load() {
		return organizingagent.NewUnavailableSynthesisModel().GenerateSynthesisForExecution(ctx, execution, input)
	}
	return s.SynthesisExecutionModel.GenerateSynthesisForExecution(ctx, execution, input)
}
