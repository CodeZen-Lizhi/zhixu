//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingagent "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/agent"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	synthesispostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/synthesispostgres"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// 验证生产队列边界：PENDING 初始请求绑定到一个 River 工作流，由真实模型执行器完成；第二次投递读取终态回执，不再调用提供方。
func TestAnchorRecommendationDispatchRunsThroughRiver(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 105)
	ctx := t.Context()
	migrator, err := riveradapter.NewMigrator(f.platform.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Validate(ctx); err != nil {
		t.Fatal(err)
	}

	generation := f.generation(t, 140000, nil, "Redis recommendation source.")
	generation.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(generation, "redis recommendation runtime")}
	applied, err := f.service.ApplyGeneration(ctx, generation.Input, generation.Generation)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.store.GetSynthesisNote(ctx, f.workspace, applied.Publications[0].NoteID)
	if err != nil {
		t.Fatal(err)
	}

	runs, err := agentpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewAnchorRecommendationModelVerifier(runs)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewGORMAnchorStore(f.platform, f.sources, verifier)
	if err != nil {
		t.Fatal(err)
	}
	modelRef := agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "anchor-runtime", ModelVersion: "v1"}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "anchor-runtime", Version: "v1"}, Model: modelRef, Timeout: 10 * time.Second, MaxOutputTokens: 2048}
	catalog := agentapp.NewRuntimeCatalog()
	if err := organizingagent.RegisterAnchorRecommendationRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
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
	provider := agentapp.NewDeterministicChatModel(
		agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: modelRef, Content: []byte(`{"recommendation":{"title":"Redis","kind":"INITIAL_SCOPE","scope":{"topics":["Redis"],"audiences":["review"],"description":"Redis review knowledge"},"reason":"The note covers Redis.","evidence":["S001"]},"no_recommendation":false}`), Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 10, TotalTokens: 20}}},
		agentapp.DeterministicChatStep{Err: foundation.NewError(foundation.ErrorDependencyUnavailable, "TEST_PROVIDER_UNAVAILABLE", true, errors.New("provider unavailable"))},
		agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: modelRef, Content: []byte(`{"recommendation":null,"no_recommendation":true}`), Usage: agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}},
		agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: modelRef, Content: []byte(`{"recommendation":{"title":"Redis","kind":"SOURCE_ASSOCIATION","scope":null,"reason":"The extracted module covers Redis expiration.","evidence":["S001"]},"no_recommendation":false}`), Usage: agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}},
	)
	controlledProvider := &anchorCancellationProvider{base: provider}
	model, err := organizingagent.NewAnchorModel(organizingagent.AnchorModelDependencies{Model: controlledProvider, ModelRuns: runs, Store: store, Catalog: catalog, Scheduler: scheduler, ProfileRef: profile.Ref, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
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
	sourceProcessing, err := synthesispostgres.NewStore(f.platform, synthesispostgres.Dependencies{ModelRuns: runs, WorkflowFence: fence, WorkflowBindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(f.platform, riveradapter.DefaultOptions(), riveradapter.NewStaticScopedEnqueueFence(), workflowpostgres.GORMRuntimeRepositoryHooks{Terminal: sourceProcessing})
	if err != nil {
		t.Fatal(err)
	}
	workflowRuns, err := workflowpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := organizingworkflow.NewAnchorRecommendationExecutor(organizingworkflow.AnchorRecommendationExecutorDependencies{Runs: workflowRuns, Requests: store, Notes: f.service, Sources: f.sources, Anchors: store, Model: model})
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
	if err := executors.Register(organizingworkflow.AnchorRecommendationNodeKind, organizingworkflow.AnchorRecommendationInputSchema, executor); err != nil {
		t.Fatal(err)
	}
	sourceExecutor, err := organizingworkflow.NewSynthesisExecutor(organizingworkflow.SynthesisExecutorDependencies{Runs: workflowRuns, Store: sourceProcessing, Candidates: f.service, Sources: f.sources, Anchors: store, Model: organizingagent.NewUnavailableSynthesisModel(), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range organizingworkflow.SynthesisExecutorNodeKinds() {
		if err := executors.Register(kind, 1, sourceExecutor); err != nil {
			t.Fatal(err)
		}
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := workflowapp.NewDefinitionRegistry(validation, executors)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range append(organizingworkflow.AnchorRecommendationDefinitions(), organizingworkflow.SynthesisRegisteredDefinitions()...) {
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
	worker, err := riveradapter.NewRuntimeNodeWorker(executors, coordinator, "anchor-recommendation-integration", 10*time.Second, time.Second)
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
			t.Errorf("stop River client: %v", err)
		}
	})
	uow, err := f.platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &organizingworkflow.AnchorRecommendationDispatcher{UnitOfWork: uow, Requests: store, Starter: runtime, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}}

	request, err := store.RequestInitialAnchorRecommendation(ctx, app.RequestInitialAnchorRecommendationCommand{WorkspaceID: f.workspace, NoteID: detail.Note.ID, ExpectedNoteVersion: detail.Note.Version, BasisRevisionID: detail.CurrentRevision.ID, IdempotencyKey: "river-success"})
	if err != nil {
		t.Fatal(err)
	}
	if started, err := dispatcher.DispatchBatch(ctx, 10); err != nil || started != 1 {
		t.Fatalf("initial dispatch started=%d err=%v", started, err)
	}
	if started, err := dispatcher.DispatchBatch(ctx, 10); err != nil || started != 0 {
		t.Fatalf("duplicate dispatch started=%d err=%v", started, err)
	}
	succeeded := waitAnchorRecommendation(t, store, f.workspace, request.ID, domain.AnchorRecommendationSucceeded)
	if succeeded.ProposalID != "" || succeeded.ModelRunID == "" || provider.CallCount() != 1 {
		t.Fatalf("success=%+v providerCalls=%d", succeeded, provider.CallCount())
	}
	anchors, err := store.ListAnchors(ctx, app.AnchorListQuery{WorkspaceID: f.workspace, NoteID: detail.Note.ID, Limit: 10})
	if err != nil || len(anchors.Items) != 0 {
		t.Fatalf("initial recommendation created anchors=%+v err=%v", anchors, err)
	}

	failedRequest, err := store.RequestInitialAnchorRecommendation(ctx, app.RequestInitialAnchorRecommendationCommand{WorkspaceID: f.workspace, NoteID: detail.Note.ID, ExpectedNoteVersion: detail.Note.Version, BasisRevisionID: detail.CurrentRevision.ID, IdempotencyKey: "river-provider-failure"})
	if err != nil {
		t.Fatal(err)
	}
	if started, err := dispatcher.DispatchBatch(ctx, 10); err != nil || started != 1 {
		t.Fatalf("failure dispatch started=%d err=%v", started, err)
	}
	failed := waitAnchorRecommendation(t, store, f.workspace, failedRequest.ID, domain.AnchorRecommendationFailed)
	if failed.ErrorCode != "TEST_PROVIDER_UNAVAILABLE" || !failed.Retryable || provider.CallCount() != 2 {
		t.Fatalf("failure=%+v providerCalls=%d", failed, provider.CallCount())
	}
	oldWorkflow, err := store.AnchorRecommendationScheduledWorkflow(ctx, f.workspace, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryAnchorRecommendation(ctx, app.RetryAnchorRecommendationCommand{WorkspaceID: f.workspace, RequestID: failed.ID, ExpectedVersion: failed.Version, IdempotencyKey: "river-retry"})
	if err != nil || retried.Status != domain.AnchorRecommendationPending {
		t.Fatalf("retry=%+v err=%v", retried, err)
	}
	if started, err := dispatcher.DispatchBatch(ctx, 10); err != nil || started != 1 {
		t.Fatalf("retry dispatch=%d err=%v", started, err)
	}
	newWorkflow, err := store.AnchorRecommendationScheduledWorkflow(ctx, f.workspace, failed.ID)
	if err != nil || newWorkflow == oldWorkflow {
		t.Fatalf("retry reused failed workflow: %s %v", newWorkflow, err)
	}
	if _, err := store.FailScheduledAnchorRecommendation(ctx, f.workspace, failed.ID, oldWorkflow, "OLD_DELIVERY_FAILURE", true); !organizingIntegrationError(err, foundation.ErrorVersionConflict, "ANCHOR_VERSION_CONFLICT") {
		t.Fatalf("old workflow changed retried request: %v", err)
	}
	waitAnchorRecommendation(t, store, f.workspace, failed.ID, domain.AnchorRecommendationNoRecommendation)
	if provider.CallCount() != 3 {
		t.Fatal("retry did not call provider exactly once", provider.CallCount())
	}
	if started, err := dispatcher.DispatchBatch(ctx, 10); err != nil || started != 0 {
		t.Fatalf("repeat dispatch=%d err=%v", started, err)
	}

	// 真实 source-ready 投递创建来源账本。此处元数据是明确的测试数据边界；原始片段解析、推荐请求、队列、提供方记录和审批隔离均使用生产实现。
	anchor, err := store.CreateAnchor(ctx, app.CreateAnchorCommand{WorkspaceID: f.workspace, NoteID: detail.Note.ID, BasisRevisionID: detail.CurrentRevision.ID, ExpectedNoteVersion: detail.Note.Version, IdempotencyKey: "discovery-anchor", Title: "Redis", Scope: domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"review"}, Description: "Redis cache review"}})
	if err != nil {
		t.Fatal(err)
	}
	redisModule := "Redis expiration controls cache lifetime."
	incoming := f.generation(t, 146000, nil, redisModule+"\n\nCooking: bake the bread until golden.")
	// 保留解析器的整文件片段，但让 Profile 仅标识 Redis 模块；发现流程不得将无关模块带入请求。
	selectedRef := incoming.Input.Sources[0].Reference
	selectedRef.SourceSpanID = organizingIntegrationID(146105)
	selectedRef.ExcerptHash = organizingIntegrationHash(redisModule)
	if err := f.db.Exec(`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,excerpt_hash,evidence_kind,derived_excerpt,parser_version,schema_version,created_at) VALUES(?,?,?,?,'paragraph',1,1,0,?,?,'raw_bytes','','v1','v1',clock_timestamp())`, selectedRef.SourceSpanID, f.workspace, selectedRef.Source.ContentArtifactID, selectedRef.Source.ParseProjectionID, len(redisModule), selectedRef.ExcerptHash).Error; err != nil {
		t.Fatal(err)
	}
	incoming.Input.Sources = []app.SynthesisSourceExcerpt{{Reference: selectedRef, Text: redisModule}}
	outbox, err := workflowpostgres.NewGORMSourceReadyOutbox(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	sourceDispatcher, err := organizingworkflow.NewSynthesisDispatcher(organizingworkflow.SynthesisDispatcherDependencies{UnitOfWork: uow, Outbox: outbox, Sources: f.sources, Processing: sourceProcessing, Starter: runtime, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	sourceDeadline := time.NewTimer(8 * time.Second)
	defer sourceDeadline.Stop()
	sourceTicker := time.NewTicker(20 * time.Millisecond)
	defer sourceTicker.Stop()
	for {
		if _, err := sourceDispatcher.DispatchBatch(ctx, 10); err != nil {
			t.Fatal(err)
		}
		var count int64
		if err := f.db.Table("organizing.synthesis_processing").Where("workspace_id=? AND source_version_id=?", string(f.workspace), string(incoming.Input.SourceEvent.Source.SourceVersionID)).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		select {
		case <-sourceDeadline.C:
			t.Fatal("source-ready did not create processing fact")
		case <-sourceTicker.C:
		}
	}
	incomingRef := incoming.Input.Sources[0].Reference
	seedDiscoveryRuntimeProfile(t, f, incoming)
	profiles, err := capturepostgres.NewGORMProfileRepository(f.platform, runs)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := organizingowner.NewKnowledgeDirectoryReader(profiles)
	if err != nil {
		t.Fatal(err)
	}
	discovery := organizingworkflow.AnchorDiscoveryDispatcher{Store: store, Directory: directory, Sources: f.sources, Requests: store}
	if n, err := discovery.DispatchBatch(ctx, 10); err != nil || n != 1 {
		t.Fatalf("discovery=%d %v", n, err)
	}
	if n, err := discovery.DispatchBatch(ctx, 10); err != nil || n != 0 {
		t.Fatalf("repeat discovery=%d %v", n, err)
	}
	recommendations, err := store.ListAnchorRecommendations(ctx, app.AnchorRecommendationListQuery{WorkspaceID: f.workspace, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var discovered app.AnchorRecommendationRequest
	for _, item := range recommendations.Items {
		if item.AnchorID == anchor.Anchor.ID {
			discovered = item
		}
	}
	if discovered.ID == "" || len(discovered.Evidence) != 1 || discovered.Evidence[0] != incomingRef {
		t.Fatalf("discovered request=%+v", discovered)
	}
	if n, err := dispatcher.DispatchBatch(ctx, 10); err != nil || n != 1 {
		t.Fatalf("discovered dispatch=%d %v", n, err)
	}
	suggested := waitAnchorRecommendation(t, store, f.workspace, discovered.ID, domain.AnchorRecommendationSucceeded)
	if suggested.ProposalID == "" || provider.CallCount() != 4 {
		t.Fatalf("missing automatic suggestion=%+v calls=%d", suggested, provider.CallCount())
	}
	var fused int64
	if err := f.db.Table("organizing.anchor_fusion_request").Where("workspace_id=?", string(f.workspace)).Count(&fused).Error; err != nil || fused != 0 {
		t.Fatalf("unapproved discovery fused=%d %v", fused, err)
	}
	afterDiscovery, err := f.store.GetSynthesisNote(ctx, f.workspace, detail.Note.ID)
	if err != nil || afterDiscovery.Note.CurrentRevisionID != detail.Note.CurrentRevisionID {
		t.Fatalf("discovery changed main note: %+v %v", afterDiscovery.Note, err)
	}

	for _, returnResult := range []bool{false, true} {
		key := "cancel-live-provider"
		if returnResult {
			key = "cancel-racing-success"
		}
		blocked := &anchorBlockedProviderCall{entered: make(chan struct{}), release: make(chan struct{})}
		controlledProvider.block.Store(blocked)
		pending, err := store.RequestInitialAnchorRecommendation(ctx, app.RequestInitialAnchorRecommendationCommand{WorkspaceID: f.workspace, NoteID: detail.Note.ID, ExpectedNoteVersion: detail.Note.Version, BasisRevisionID: detail.CurrentRevision.ID, IdempotencyKey: key})
		if err != nil {
			t.Fatal(err)
		}
		if count, err := dispatcher.DispatchBatch(ctx, 10); err != nil || count != 1 {
			t.Fatalf("live cancellation dispatch=%d %v", count, err)
		}
		select {
		case <-blocked.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("provider did not start")
		}
		runID, err := store.AnchorRecommendationScheduledWorkflow(ctx, f.workspace, pending.ID)
		if err != nil {
			t.Fatal(err)
		}
		run, err := workflowRuns.GetRun(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := coordinator.Cancel(ctx, workflowapp.RunControlCommand{WorkflowRunID: runID, ExpectedVersion: run.Version, IdempotencyKey: key}); err != nil {
			t.Fatal(err)
		}
		if returnResult {
			close(blocked.release)
		}
		recovered := waitAnchorRecommendation(t, store, f.workspace, pending.ID, domain.AnchorRecommendationRecoveryRequired)
		if recovered.Retryable || recovered.ProposalID != "" {
			t.Fatalf("cancelled provider produced result=%+v", recovered)
		}
		controlledProvider.block.Store(nil)
	}

	// 停止投递，以免并发消费者干扰取消和协调过程的观察。下方领取操作模拟持久化领取后、记录提供方结果前崩溃，不代表发生了提供方调用。
	if err := client.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	for _, claimed := range []bool{false, true} {
		key := "cancel-before-claim"
		if claimed {
			key = "cancel-after-claim"
		}
		pending, err := store.RequestInitialAnchorRecommendation(ctx, app.RequestInitialAnchorRecommendationCommand{WorkspaceID: f.workspace, NoteID: detail.Note.ID, ExpectedNoteVersion: detail.Note.Version, BasisRevisionID: detail.CurrentRevision.ID, IdempotencyKey: key})
		if err != nil {
			t.Fatal(err)
		}
		if count, err := dispatcher.DispatchBatch(ctx, 10); err != nil || count != 1 {
			t.Fatalf("cancel dispatch=%d %v", count, err)
		}
		runID, err := store.AnchorRecommendationScheduledWorkflow(ctx, f.workspace, pending.ID)
		if err != nil {
			t.Fatal(err)
		}
		if claimed {
			nodeID, attemptID := organizingIntegrationID(145001), organizingIntegrationID(145002)
			_, err = store.ClaimAnchorRecommendation(ctx, app.ClaimAnchorRecommendationCommand{WorkspaceID: f.workspace, RequestID: pending.ID, ExpectedVersion: pending.Version, WorkflowRunID: runID, NodeRunID: nodeID, NodeAttemptID: attemptID, ModelInputHash: organizingIntegrationHash("interrupted model input")})
			if err != nil {
				t.Fatal(err)
			}
		}
		if count, err := store.ReconcileAnchorRecommendationWorkflows(ctx, 10); err != nil || count != 0 {
			t.Fatalf("live workflow reconciled=%d %v", count, err)
		}
		run, err := workflowRuns.GetRun(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := coordinator.Cancel(ctx, workflowapp.RunControlCommand{WorkflowRunID: runID, ExpectedVersion: run.Version, IdempotencyKey: key}); err != nil {
			t.Fatal(err)
		}
		if count, err := store.ReconcileAnchorRecommendationWorkflows(ctx, 10); err != nil || count != 1 {
			t.Fatalf("cancel reconciliation=%d %v", count, err)
		}
		result, err := store.GetAnchorRecommendation(ctx, f.workspace, pending.ID)
		if err != nil {
			t.Fatal(err)
		}
		want, code := domain.AnchorRecommendationFailed, "ANCHOR_WORKFLOW_ENDED_BEFORE_ANALYSIS"
		if claimed {
			want, code = domain.AnchorRecommendationRecoveryRequired, "ANCHOR_MODEL_RECOVERY_REQUIRED"
		}
		if result.Status != want || result.ErrorCode != code || result.Retryable == claimed {
			t.Fatalf("cancel result=%+v", result)
		}
		if count, err := store.ReconcileAnchorRecommendationWorkflows(ctx, 10); err != nil || count != 0 {
			t.Fatalf("duplicate reconciliation=%d %v", count, err)
		}
	}
	if provider.CallCount() != 4 {
		t.Fatal("reconciliation repeated provider calls", provider.CallCount())
	}

}

func waitAnchorRecommendation(t *testing.T, store *GORMAnchorStore, workspaceID, requestID foundation.ID, want domain.AnchorRecommendationStatus) app.AnchorRecommendationRequest {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := store.GetAnchorRecommendation(ctx, workspaceID, requestID)
		if err != nil {
			var state []map[string]any
			diagnosticCtx, cancelDiagnostic := context.WithTimeout(context.Background(), time.Second)
			defer cancelDiagnostic()
			store.db.WithContext(diagnosticCtx).Raw(`SELECT q.status AS request_status,r.status AS run_status,n.status AS node_status,n.error_code FROM organizing.anchor_recommendation_request q LEFT JOIN workflow.run r ON r.id=q.scheduled_workflow_run_id LEFT JOIN workflow.node_run n ON n.run_id=r.id WHERE q.id=?`, string(requestID)).Scan(&state)
			t.Fatalf("read request: %v; runtime=%+v", err, state)
		}
		if request.Status == want {
			return request
		}
		if request.Status != domain.AnchorRecommendationPending && request.Status != domain.AnchorRecommendationRunning {
			t.Fatalf("request terminal=%s expected=%s request=%+v", request.Status, want, request)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("request did not finish: %+v", request)
		case <-ticker.C:
		}
	}
}

// 真实 River 取消同时验证可感知上下文的提供方，以及取消命令后、心跳未必触发前返回的结果。
type anchorBlockedProviderCall struct{ entered, release chan struct{} }
type anchorCancellationProvider struct {
	base  agentapp.ChatModel
	block atomic.Pointer[anchorBlockedProviderCall]
}

func (p *anchorCancellationProvider) Chat(ctx context.Context, request agentapp.ChatRequest) (agentapp.ChatResponse, error) {
	blocked := p.block.Load()
	if blocked == nil {
		return p.base.Chat(ctx, request)
	}
	close(blocked.entered)
	select {
	case <-ctx.Done():
		return agentapp.ChatResponse{}, ctx.Err()
	case <-blocked.release:
		return agentapp.ChatResponse{Model: request.Model, Content: []byte(`{"recommendation":null,"no_recommendation":true}`), Usage: agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
	}
}

// 在所属模块边界植入 Profile 生成数据；发现流程通过生产适配器读取真实持久化的不可变 Profile、证据和来源。
func seedDiscoveryRuntimeProfile(t *testing.T, f *synthesisDBFixture, record app.SynthesisApplyRecord) {
	t.Helper()
	source := record.Input.SourceEvent.Source
	span := record.Input.Sources[0].Reference.SourceSpanID
	captureID, profileID, revisionID := organizingIntegrationID(146100), organizingIntegrationID(146101), organizingIntegrationID(146102)
	content, err := capturedomain.NormalizeProfileContent(capturedomain.ProfileContent{Summary: "Redis expiration", Topics: []capturedomain.ProfileCandidate{{Label: "Redis", SourceSpanIDs: []foundation.ID{span}}}, KnowledgePoints: []capturedomain.ProfilePoint{{Text: "Redis expiration controls cache lifetime.", SourceSpanIDs: []foundation.ID{span}}}})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := capturedomain.ComputeProfileDigest(content)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.capture(id,workspace_id,kind,display_name,original_location,original_input_hash,source_id,latest_source_version_id,status,fetch_status,ingestion_status,index_status,profile_status,captured_at,updated_at) VALUES(?,?,'FILE','redis.md','discovery-redis.md',?,?,?,'READY','NOT_APPLICABLE','READY','READY','READY',clock_timestamp(),clock_timestamp())`, []any{captureID, source.WorkspaceID, source.ContentHash, source.SourceID, source.SourceVersionID}},
		{`INSERT INTO learning.document_knowledge_profile(id,workspace_id,capture_id,source_version_id,status,created_at,updated_at) VALUES(?,?,?,?,'PENDING',clock_timestamp(),clock_timestamp())`, []any{profileID, source.WorkspaceID, captureID, source.SourceVersionID}},
		{`INSERT INTO learning.document_knowledge_profile_revision(id,profile_id,workspace_id,source_version_id,parse_projection_id,index_version_id,model_run_id,prompt_version,schema_version,content,content_digest,created_at) VALUES(?,?,?,?,?,?,?,'v1','document-knowledge-profile/v1',?::jsonb,?,clock_timestamp())`, []any{revisionID, profileID, source.WorkspaceID, source.SourceVersionID, source.ParseProjectionID, organizingIntegrationID(146012), record.Generation.ModelRunID, string(raw), digest}},
		{`INSERT INTO learning.document_knowledge_profile_evidence(revision_id,workspace_id,source_version_id,source_span_id,created_at) VALUES(?,?,?,?,clock_timestamp())`, []any{revisionID, source.WorkspaceID, source.SourceVersionID, span}},
		{`UPDATE learning.document_knowledge_profile SET status='READY',current_revision_id=?,version=version+1,updated_at=clock_timestamp() WHERE id=?`, []any{revisionID, profileID}},
	}
	for _, statement := range statements {
		if err := f.db.Exec(statement.query, statement.args...).Error; err != nil {
			t.Fatal(err)
		}
	}
}
