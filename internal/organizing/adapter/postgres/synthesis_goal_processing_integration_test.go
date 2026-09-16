//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	synthesispostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/synthesispostgres"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	domain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	ow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	wa "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	wd "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// 仅在真实不可变选择 ModelRun 证明完成后调用。此处 Workflow 行是有效数据库测试数据，不证明 River 调度。
func checkGoalProcessingPersistence(t *testing.T, f *synthesisDBFixture, snapshots *organizingowner.SynthesisGoalCatalogReader, binding *app.SynthesisGoalBinding, events []domain.SynthesisSourceReady) {
	t.Helper()
	ctx := t.Context()
	results, err := NewGORMGoalSelectionResultReader(f.platform, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := app.NewSynthesisGoalBindingProof(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := proof.VerifySynthesisGoalBinding(ctx, f.workspace, binding); err != nil {
		t.Fatalf("real selection proof: %v", err)
	}
	runs, err := agentpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	runtimeBindings, err := workflowpostgres.NewGORMRuntimeBindingReader(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := synthesispostgres.NewStore(f.platform, synthesispostgres.Dependencies{ModelRuns: runs, WorkflowFence: fence, WorkflowBindings: runtimeBindings, Goals: proof})
	if err != nil {
		t.Fatal(err)
	}
	var seed domain.SynthesisSourceReady
	for _, event := range events {
		if event.Source == binding.Materials[0].Reference.Source {
			seed = event
		}
	}
	if seed.Validate() != nil {
		t.Fatal("selected seed is missing")
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	definitionID := organizingIntegrationID(204000)
	graph, err := json.Marshal(ow.SynthesisRegisteredDefinitions()[0].Graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES(?,?,'organizing.synthesis-note',1,?::jsonb,?)`, string(definitionID), string(f.workspace), string(graph), at).Error; err != nil {
		t.Fatal(err)
	}
	key, err := app.SynthesisProcessingKey(seed, binding.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	processing := app.SynthesisProcessing{ID: organizingIntegrationID(204001), GoalRequestID: binding.RequestID, SourceEvent: seed, WorkflowRunID: organizingIntegrationID(204002), RequestHash: strings.TrimPrefix(key, "synthesis-goal:"), Status: app.SynthesisProcessingPending, RevisionIDs: []foundation.ID{}, Version: 1, CreatedAt: at, UpdatedAt: at}
	startInput, _ := json.Marshal(ow.SynthesisStartInput{GoalRequestID: binding.RequestID, ProcessingID: processing.ID, ExecutionNo: 1})
	start := wa.RuntimeStartResult{Run: wd.Run{ID: processing.WorkflowRunID, WorkspaceID: f.workspace, Input: startInput}, Job: wa.JobReceipt{JobID: 1}}
	if err := f.db.Exec(`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,created_at,updated_at) VALUES(?,?,?,'running',?::jsonb,?,?)`, string(processing.WorkflowRunID), string(f.workspace), string(definitionID), string(startInput), at, at).Error; err != nil {
		t.Fatal(err)
	}
	create := func(p app.SynthesisProcessing, s wa.RuntimeStartResult) error {
		return f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			return runtime.CreateSynthesisProcessingScoped(ctx, scope, ow.SynthesisCreateProcessing{Processing: p, Start: s})
		})
	}
	wrong := start
	wrong.Run.Input = []byte(`{"processing_id":"` + string(processing.ID) + `","execution_no":1,"apply_recovery":false}`)
	if err := create(processing, wrong); err == nil {
		t.Fatal("queued goal was dropped")
	}
	if err := create(processing, start); err != nil {
		t.Fatalf("create goal processing: %v", err)
	}
	if err := create(processing, start); err == nil {
		t.Fatal("duplicate goal processing inserted")
	}
	loaded, err := runtime.LoadSynthesisExecution(ctx, f.workspace, processing.ID, processing.WorkflowRunID)
	if err != nil || loaded.GoalRequestID != binding.RequestID || loaded.Processing.GoalRequestID != binding.RequestID {
		t.Fatalf("loaded goal lost: %+v %v", loaded, err)
	}
	if err := f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		found, ok, err := runtime.FindSynthesisGoalProcessingScoped(ctx, scope, f.workspace, binding.RequestID)
		if err != nil {
			return err
		}
		if !ok || found.ID != processing.ID {
			t.Fatal("goal processing lookup lost stable identity")
		}
		_, ok, err = runtime.FindSynthesisGoalProcessingScoped(ctx, scope, f.otherWorkspace, binding.RequestID)
		if err != nil {
			return err
		}
		if ok {
			t.Fatal("goal processing crossed workspace")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	node, attempt := organizingIntegrationID(204003), organizingIntegrationID(204004)
	seedOrganizingRunningNodeAttempt(t, ctx, f.platform.DB(), processing.WorkflowRunID, node, attempt, ow.SynthesisPrepareNodeKind, at.Add(time.Hour), at)
	execution := wa.ExecutionContext{WorkspaceID: f.workspace, RunID: processing.WorkflowRunID, NodeRunID: node, NodeAttemptID: attempt, NodeKind: ow.SynthesisPrepareNodeKind}
	refs := make([]domain.SynthesisSourceRef, len(binding.Materials))
	for i, m := range binding.Materials {
		refs[i] = m.Reference
	}
	frozen := ow.SynthesisFrozenInput{Goal: binding, ProcessingID: processing.ID, WorkflowRunID: processing.WorkflowRunID, SourceEvent: seed, Notes: []ow.SynthesisFrozenNote{}, Sources: refs}
	frozen.RequestHash, err = frozen.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	// 替换为结构合法的理由后，重新计算完全有效的哈希；拒绝应来自持久化证明，而非语法或哈希校验。
	raw, _ := json.Marshal(frozen)
	var forged ow.SynthesisFrozenInput
	_ = json.Unmarshal(raw, &forged)
	forged.Goal.Materials[0].Points[0].Reason = "invented selection rationale"
	forged.RequestHash, _ = forged.ComputeHash()
	if err := forged.Validate(); err != nil {
		t.Fatalf("forgery must be structurally valid: %v", err)
	}
	if _, err := runtime.FreezeSynthesisInput(ctx, execution, forged); err == nil {
		t.Fatal("forged selection proof frozen")
	}
	var oversized ow.SynthesisFrozenInput
	if err := json.Unmarshal(raw, &oversized); err != nil {
		t.Fatal(err)
	}
	oversized.Goal.Materials[0].Points = nil
	for i := 0; i < 512; i++ {
		kind := app.KnowledgePointKindKnowledgePoint
		if i >= 256 {
			kind = app.KnowledgePointKindExample
		}
		oversized.Goal.Materials[0].Points = append(oversized.Goal.Materials[0].Points, app.GoalSourcePointBinding{SelectionID: organizingIntegrationID(205000 + i/32), ModelRunID: organizingIntegrationID(205100 + i/32), Locator: app.KnowledgePointLocator{ProfileRevisionID: binding.Materials[0].Points[0].Locator.ProfileRevisionID, Kind: kind, Index: i % 256}, Reason: strings.Repeat("r", 2048)})
	}
	oversized.RequestHash, _ = oversized.ComputeHash()
	var tooLarge *foundation.Error
	if _, err := runtime.FreezeSynthesisInput(ctx, execution, oversized); !errors.As(err, &tooLarge) || tooLarge.Code != app.ErrorCodeSynthesisModelInputTooLarge || tooLarge.Retryable {
		t.Fatalf("oversized goal must return explicit permanent budget error: %v", err)
	}
	accepted, err := runtime.FreezeSynthesisInput(ctx, execution, frozen)
	if err != nil {
		t.Fatalf("freeze goal: %v", err)
	}
	loaded, err = runtime.LoadSynthesisExecution(ctx, f.workspace, processing.ID, processing.WorkflowRunID)
	if err != nil || loaded.Input == nil || !reflect.DeepEqual(loaded.Input.Goal, binding) || loaded.Input.RequestHash != accepted.RequestHash {
		t.Fatalf("durable goal snapshot lost: %+v %v", loaded, err)
	}
	replay, err := runtime.FreezeSynthesisInput(ctx, execution, frozen)
	if err != nil || !reflect.DeepEqual(replay, accepted) {
		t.Fatalf("goal freeze replay: %v", err)
	}
	if err := f.db.Exec(`UPDATE organizing.synthesis_processing SET goal_request_id=NULL,version=version+1 WHERE id=?`, string(processing.ID)).Error; err == nil {
		t.Fatal("goal identity cleared")
	}
	// 在独立生成、语义步骤及其有效应用预留齐备前，即使候选表面合法，也不能应用。
	f.count(t, "organizing.synthesis_note", "workspace_id", string(f.workspace), 0)
}
