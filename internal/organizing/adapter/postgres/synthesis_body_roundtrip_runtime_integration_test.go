//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingagent "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/agent"
	synthesispostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/synthesispostgres"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
)

// 所有发布轮次共用一个真实运行时。因不存在人工手稿，只注册旧版执行图；v2 有独立集成测试组。
type bodyRoundtripRuntime struct {
	store      *synthesispostgres.Store
	dispatcher *organizingworkflow.BodyRefreshGenerationDispatcher
	provider   *bodyRoundtripProvider
}

func newBodyRoundtripRuntime(t *testing.T, f *synthesisGitFixture, anchors *GORMAnchorStore, outputs ...string) *bodyRoundtripRuntime {
	t.Helper()
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	modelRuns, err := agentpostgres.NewGORMRepository(f.platform)
	check(err)
	migrator, err := riveradapter.NewMigrator(f.platform.DB())
	check(err)
	check(migrator.Up(ctx))
	check(migrator.Validate(ctx))
	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(f.platform)
	check(err)
	bindings, err := workflowpostgres.NewGORMRuntimeBindingReader(f.platform)
	check(err)
	store, err := synthesispostgres.NewStore(f.platform, synthesispostgres.Dependencies{ModelRuns: modelRuns, WorkflowFence: fence, WorkflowBindings: bindings})
	check(err)
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(f.platform, riveradapter.DefaultOptions(), riveradapter.NewStaticScopedEnqueueFence(), workflowpostgres.GORMRuntimeRepositoryHooks{Terminal: store})
	check(err)
	runs, err := workflowpostgres.NewGORMRepository(f.platform)
	check(err)
	// 在任何刷新生成开始前，将既有所属模块服务从初始测试校验切换为生产模型日志校验。
	f.store.dependencies.Validated = store
	f.store.dependencies.Anchors = anchors
	modelRef := agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "body-refresh", ModelVersion: "v1"}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "body-refresh", Version: "v1"}, Model: modelRef, Timeout: time.Second, MaxOutputTokens: 2048}
	catalog := agentapp.NewRuntimeCatalog()
	check(organizingagent.RegisterSynthesisRuntimeCatalog(catalog))
	check(catalog.RegisterProfile(profile))
	check(catalog.Freeze())
	scheduler, err := agenteino.NewStructuredPhaseScheduler(ctx)
	check(err)
	steps := make([]agentapp.DeterministicChatStep, len(outputs))
	for i, output := range outputs {
		steps[i] = agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: modelRef, Content: []byte(output), Usage: agentdomain.TokenUsage{InputTokens: 8, OutputTokens: 4, TotalTokens: 12}}}
	}
	provider := &bodyRoundtripProvider{current: agentapp.NewDeterministicChatModel(steps...)}
	model, err := organizingagent.NewSynthesisModel(organizingagent.SynthesisModelDependencies{Model: provider, Scheduler: scheduler, Catalog: catalog, ModelRuns: modelRuns, Store: store, ProfileRef: profile.Ref, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	check(err)
	executor, err := organizingworkflow.NewSynthesisExecutor(organizingworkflow.SynthesisExecutorDependencies{Runs: runs, Store: store, Candidates: f.service, Sources: f.sources, Anchors: anchors, BodyRefresh: f.store, Model: model, Clock: foundation.SystemClock{}})
	check(err)
	validation, err := workflowapp.NewValidationCatalog([]int{1}, capability.All())
	check(err)
	executors, err := workflowapp.NewExecutorRegistry(validation)
	check(err)
	for _, kind := range organizingworkflow.SynthesisExecutorNodeKinds() {
		check(executors.Register(kind, 1, executor))
	}
	check(executors.Freeze())
	definitions, err := workflowapp.NewDefinitionRegistry(validation, executors)
	check(err)
	for _, definition := range organizingworkflow.SynthesisRegisteredDefinitions()[:1] {
		check(definitions.Register(definition))
	}
	check(definitions.Freeze())
	coordinator, err := workflowapp.NewRuntimeCoordinator(runtime)
	check(err)
	worker, err := riveradapter.NewRuntimeNodeWorker(executors, coordinator, "body-refresh-integration", 10*time.Second, time.Second)
	check(err)
	workers := riveradapter.NewWorkers()
	check(riveradapter.AddRuntimeWorkerSafely(workers, worker))
	client, err := riveradapter.NewClient(f.platform.DB(), workers)
	check(err)
	check(client.Start(ctx))
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Stop(stop); err != nil {
			t.Error(err)
		}
	})
	workspaces, err := workspacepostgres.NewGORMRepository(f.platform)
	check(err)
	dispatcher := &organizingworkflow.BodyRefreshGenerationDispatcher{Workspaces: workspaces, Store: store, UnitOfWork: f.uow, Starter: runtime, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}}
	return &bodyRoundtripRuntime{store: store, dispatcher: dispatcher, provider: provider}
}

// 初始范围接受使用既有所属模块测试数据边界；自动生成的候选及其两份模型证明不使用该替代。
func acceptBodyRoundtripEvidence(t *testing.T, f *synthesisGitFixture, anchors *GORMAnchorStore, before app.SynthesisNoteDetail, seedModelID foundation.ID, evidence []domain.SynthesisSourceRef) {
	t.Helper()
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	modelRuns, err := agentpostgres.NewGORMRepository(f.platform)
	check(err)
	callID, err := (foundation.UUIDGenerator{}).New()
	check(err)
	created, err := anchors.CreateAnchor(ctx, app.CreateAnchorCommand{WorkspaceID: f.workspace, IdempotencyKey: "roundtrip-anchor:" + string(before.Note.ID), NoteID: before.Note.ID, ExpectedNoteVersion: before.Note.Version, BasisRevisionID: before.CurrentRevision.ID, Title: "Scheduling", Scope: domain.AnchorScope{Topics: []string{"Scheduling"}, Audiences: []string{"Application developers"}, Description: "Version-specific scheduling limits"}})
	check(err)
	modelRecord, err := modelRuns.GetModelRun(ctx, f.workspace, seedModelID)
	check(err)
	call := agentdomain.ModelCall{ID: callID, ModelRunID: seedModelID, CallNo: 1, Phase: agentdomain.ModelCallInitial, Model: modelRecord.Run.Model, Profile: modelRecord.Run.Profile, Prompt: modelRecord.Run.Prompt, Schema: modelRecord.Run.Schema, MaxOutputTokens: 128, Status: agentdomain.ModelCallStarted, RequestHash: organizingIntegrationHash("refresh-association"), RequestBytes: 128, Version: 1, StartedAt: modelRecord.Run.CreatedAt.Add(time.Second)}
	_, _, err = modelRuns.StartModelCall(ctx, f.workspace, call)
	check(err)
	completed := call.StartedAt.Add(time.Second)
	call.Status = agentdomain.ModelCallSucceeded
	call.ResponseHash = organizingIntegrationHash("refresh-association-response")
	call.ResponseBytes = 64
	call.Version = 2
	call.CompletedAt = &completed
	_, _, err = modelRuns.CompleteModelCall(ctx, agentapp.CompleteModelCallCommand{WorkspaceID: f.workspace, ExpectedVersion: 1, Call: call})
	check(err)
	modelRecord.Run.Status = agentdomain.ModelRunSucceeded
	modelRecord.Run.FinalResultType = agentdomain.ResultTypeRAGAnswer
	modelRecord.Run.Version++
	now := time.Now().UTC().Truncate(time.Microsecond)
	modelRecord.Run.UpdatedAt = now
	modelRecord.Run.CompletedAt = &now
	_, _, err = modelRuns.FinalizeModelRun(ctx, agentapp.FinalizeModelRunCommand{ExpectedVersion: 1, Run: modelRecord.Run})
	check(err)
	proposal, err := anchors.RecordAnchorRecommendation(ctx, app.RecordAnchorRecommendation{WorkspaceID: f.workspace, AnchorID: created.Anchor.ID, IdempotencyKey: "roundtrip-association:" + string(before.Note.ID), ExpectedScopeVersion: 1, Kind: domain.AnchorSourceAssociation, Reason: "Version-specific scheduling limits belong to this target", Evidence: evidence, ModelRunID: seedModelID})
	check(err)
	_, err = anchors.DecideAnchor(ctx, app.DecideAnchorCommand{WorkspaceID: f.workspace, AnchorID: created.Anchor.ID, IdempotencyKey: "roundtrip-accept:" + string(before.Note.ID), Kind: domain.AnchorSourceAssociation, ExpectedAnchorVersion: created.Anchor.Version, Decision: domain.AnchorAccepted, Items: []app.AnchorDecisionItem{{ProposalID: proposal.Proposal.ID, ExpectedVersion: proposal.Proposal.Version}}})
	check(err)
}

// 仅在上一次处理到达持久化终态后配置；响应仍须经过真实 RecordingChatModel 和两个评审器。
type bodyRoundtripProvider struct {
	mu      sync.Mutex
	current *agentapp.DeterministicChatModel
	calls   int
}

func (p *bodyRoundtripProvider) Chat(ctx context.Context, request agentapp.ChatRequest) (agentapp.ChatResponse, error) {
	p.mu.Lock()
	model := p.current
	p.calls++
	p.mu.Unlock()
	return model.Chat(ctx, request)
}
func (p *bodyRoundtripProvider) CallCount() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }
func (p *bodyRoundtripProvider) configure(outputs ...string) {
	steps := make([]agentapp.DeterministicChatStep, len(outputs))
	for i, output := range outputs {
		steps[i] = agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "body-refresh", ModelVersion: "v1"}, Content: []byte(output), Usage: agentdomain.TokenUsage{InputTokens: 8, OutputTokens: 4, TotalTokens: 12}}}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current = agentapp.NewDeterministicChatModel(steps...)
}
