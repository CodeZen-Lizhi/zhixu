//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"reflect"
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
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
)

// 初始发布和关联设置使用既有所属模块测试数据；从刷新调度开始，所有 Workflow、River、模型及应用证明均为真实实现。
func runBodyRefreshThroughRiver(t *testing.T, f *synthesisGitFixture, request app.SynthesisBodyRefreshRequest, before app.SynthesisNoteDetail, incoming domain.SynthesisSourceRef, seedModelID foundation.ID, mode string) {
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
	anchors, err := NewGORMAnchorStore(f.platform, f.sources, anchorModelProofFixture{})
	check(err)
	created, err := anchors.CreateAnchor(ctx, app.CreateAnchorCommand{WorkspaceID: f.workspace, IdempotencyKey: "refresh-anchor", NoteID: before.Note.ID, ExpectedNoteVersion: before.Note.Version, BasisRevisionID: before.CurrentRevision.ID, Title: "Scheduling", Scope: domain.AnchorScope{Topics: []string{"Scheduling"}, Audiences: []string{"Application developers"}, Description: "Version-specific scheduling limits"}})
	check(err)
	modelRecord, err := modelRuns.GetModelRun(ctx, f.workspace, seedModelID)
	check(err)
	call := agentdomain.ModelCall{ID: organizingIntegrationID(89001), ModelRunID: seedModelID, CallNo: 1, Phase: agentdomain.ModelCallInitial, Model: modelRecord.Run.Model, Profile: modelRecord.Run.Profile, Prompt: modelRecord.Run.Prompt, Schema: modelRecord.Run.Schema, MaxOutputTokens: 128, Status: agentdomain.ModelCallStarted, RequestHash: organizingIntegrationHash("refresh-association"), RequestBytes: 128, Version: 1, StartedAt: modelRecord.Run.CreatedAt.Add(time.Second)}
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
	preparedEvidence, err := f.store.PrepareSynthesisBodyRefresh(ctx, f.workspace, request.ID)
	check(err)
	var evidence []domain.SynthesisSourceRef
	for _, item := range preparedEvidence.Items {
		for _, updated := range item.Updated.Items {
			if updated.ID == item.Impact.UpstreamItemID {
				evidence = append(evidence, updated.SourceReferences()...)
			}
		}
	}
	proposal, err := anchors.RecordAnchorRecommendation(ctx, app.RecordAnchorRecommendation{WorkspaceID: f.workspace, AnchorID: created.Anchor.ID, IdempotencyKey: "refresh-source-association", ExpectedScopeVersion: 1, Kind: domain.AnchorSourceAssociation, Reason: "Version-specific scheduling limits belong to this target", Evidence: evidence, ModelRunID: seedModelID})
	check(err)
	_, err = anchors.DecideAnchor(ctx, app.DecideAnchorCommand{WorkspaceID: f.workspace, AnchorID: created.Anchor.ID, IdempotencyKey: "refresh-source-accept", Kind: domain.AnchorSourceAssociation, ExpectedAnchorVersion: created.Anchor.Version, Decision: domain.AnchorAccepted, Items: []app.AnchorDecisionItem{{ProposalID: proposal.Proposal.ID, ExpectedVersion: proposal.Proposal.Version}}})
	check(err)
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
	f.store.dependencies.Anchors = bodyRefreshAdmissionMutation{owner: anchors, mode: mode, source: incoming.Source}
	modelRef := agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "body-refresh", ModelVersion: "v1"}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "body-refresh", Version: "v1"}, Model: modelRef, Timeout: time.Second, MaxOutputTokens: 2048}
	catalog := agentapp.NewRuntimeCatalog()
	check(organizingagent.RegisterSynthesisRuntimeCatalog(catalog))
	check(catalog.RegisterProfile(profile))
	check(catalog.Freeze())
	scheduler, err := agenteino.NewStructuredPhaseScheduler(ctx)
	check(err)
	output := `{"notes":[{"note":"N001","operations":[{"kind":"REFRESH_ITEM","target":"I002"}]}]}`
	review := `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"}]},{"index":2,"verdict":"SUPPORTED","sources":[{"source":"S002","verdict":"SUPPORTED"}]},{"index":3,"verdict":"SUPPORTED","sources":[]}]}`
	provider := agentapp.NewDeterministicChatModel(agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: modelRef, Content: []byte(output), Usage: agentdomain.TokenUsage{InputTokens: 8, OutputTokens: 4, TotalTokens: 12}}}, agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: modelRef, Content: []byte(review), Usage: agentdomain.TokenUsage{InputTokens: 8, OutputTokens: 4, TotalTokens: 12}}})
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
	for _, definition := range organizingworkflow.SynthesisRegisteredDefinitions() {
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
	type dispatchOutcome struct {
		result organizingworkflow.SynthesisDispatchBatchResult
		err    error
	}
	dispatches := make(chan dispatchOutcome, 2)
	for range 2 {
		go func() { result, err := dispatcher.DispatchBatch(ctx, 10); dispatches <- dispatchOutcome{result, err} }()
	}
	started := 0
	for range 2 {
		dispatched := <-dispatches
		check(dispatched.err)
		started += dispatched.result.Started
	}
	if started != 1 {
		t.Fatalf("concurrent dispatch started=%d", started)
	}
	var processing app.SynthesisProcessing
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var id string
		check(f.db.Raw("SELECT id FROM organizing.synthesis_processing WHERE body_refresh_request_id=?", string(request.ID)).Scan(&id).Error)
		processing, err = store.GetSynthesisProcessing(ctx, f.workspace, foundation.ID(id))
		check(err)
		if processing.Status != app.SynthesisProcessingPending && processing.Status != app.SynthesisProcessingRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if mode != "admitted" {
		if processing.Status != app.SynthesisProcessingFailed || processing.Failure == nil || processing.Failure.Code != "ANCHOR_VERSION_CONFLICT" || provider.CallCount() != 2 {
			t.Fatalf("changed admission was not rejected at apply: status=%s failure=%+v calls=%d", processing.Status, processing.Failure, provider.CallCount())
		}
		after, err := f.service.GetNote(ctx, f.workspace, before.Note.ID)
		check(err)
		if after.CurrentRevision.ID != before.CurrentRevision.ID || after.PublishedRevision.ID != before.PublishedRevision.ID || len(processing.RevisionIDs) != 0 {
			t.Fatal("rejected refresh appended or published a revision")
		}
		return
	}
	if processing.Status != app.SynthesisProcessingSucceeded || len(processing.RevisionIDs) != 1 || provider.CallCount() != 2 {
		t.Fatalf("refresh status=%s failure=%+v calls=%d", processing.Status, processing.Failure, provider.CallCount())
	}
	execution, err := store.LoadSynthesisExecution(ctx, f.workspace, processing.ID, processing.WorkflowRunID)
	check(err)
	if execution.Input == nil || execution.Input.BodyRefresh == nil || execution.Generation == nil || execution.Semantic == nil || !execution.Semantic.Semantic.Accepted || execution.Generation.ModelRunID == execution.Semantic.ModelRunID {
		t.Fatal("refresh lost separate persisted model proofs")
	}
	if execution.Input.SourceEvent.Source == incoming.Source {
		t.Fatal("fixture must use provenance A distinct from evidence B")
	}
	if len(execution.Input.Sources) != len(evidence) || !reflect.DeepEqual(execution.Input.Notes[0].Anchor.AllowedSources, execution.Input.Sources) {
		t.Fatal("frozen admission does not exactly match refreshed evidence")
	}
	for _, ref := range execution.Input.Sources {
		if ref.Source == execution.Input.SourceEvent.Source {
			t.Fatal("provenance seed leaked into refresh evidence")
		}
	}
	frozen, _ := json.Marshal(execution.Input)
	if len(frozen) == 0 {
		t.Fatal("missing frozen input")
	}
	after, err := f.service.GetNote(ctx, f.workspace, before.Note.ID)
	check(err)
	if after.CurrentRevision.RevisionNo != before.CurrentRevision.RevisionNo+1 || after.CurrentRevision.Items[1].Gap.Resolution == nil || !reflect.DeepEqual(after.CurrentRevision.Items[0], before.CurrentRevision.Items[0]) || !reflect.DeepEqual(after.CurrentRevision.Items[2:], before.CurrentRevision.Items[2:]) || after.PublishedRevision.ID != before.PublishedRevision.ID {
		t.Fatalf("local candidate or published pointer changed incorrectly: %+v", after)
	}
	repeated, err := dispatcher.DispatchBatch(ctx, 10)
	check(err)
	if repeated.Started != 0 || provider.CallCount() != 2 {
		t.Fatal("same publication generated twice")
	}
}

// 仅在测试中于真实应用事务内注入故障，此时输入已经冻结，且两个模型证明均已完成。回滚恢复所有行；是否允许追加候选仍由生产所属模块校验决定。
type bodyRefreshAdmissionMutation struct {
	owner  *GORMAnchorStore
	mode   string
	source domain.SynthesisSourceVersion
}

func (f bodyRefreshAdmissionMutation) VerifySynthesisAnchorAdmissionScoped(ctx context.Context, scope foundation.TransactionScope, workspace, note foundation.ID, source domain.SynthesisSourceVersion, binding *app.SynthesisAnchorBinding) error {
	if f.mode != "admitted" && source == f.source {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		// 当前已接受决定不可变；模拟其精确证据投影丢失，不为此功能增加撤销 API。
		if err := tx.Exec("SET LOCAL session_replication_role = replica").Error; err != nil {
			return err
		}
		if f.mode == "revoked" {
			err = tx.Exec("DELETE FROM organizing.anchor_proposal_evidence WHERE workspace_id=? AND anchor_id=? AND source_id=?", string(workspace), string(binding.AnchorID), string(source.SourceID)).Error
		} else {
			err = tx.Exec("UPDATE organizing.knowledge_anchor SET scope_version=scope_version+1 WHERE workspace_id=? AND id=?", string(workspace), string(binding.AnchorID)).Error
		}
		if err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL session_replication_role = origin").Error; err != nil {
			return err
		}
	}
	return f.owner.VerifySynthesisAnchorAdmissionScoped(ctx, scope, workspace, note, source, binding)
}
