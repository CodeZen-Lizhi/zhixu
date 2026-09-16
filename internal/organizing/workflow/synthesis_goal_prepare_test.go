package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestGoalPrepareFreezesKnowledgeAndReopensOnlySelectedOriginals(t *testing.T) {
	executor, execution, base, candidates, _ := synthesisExecutorFixture(t)
	base.value.GoalRequestID = synthesisWorkflowTestID(100)
	base.value.Input = nil
	base.value.Semantic = nil
	store := &goalFreezeFixture{synthesisExecutionFixtureStore: base}
	sources := &goalExactSourceFixture{views: map[domain.SynthesisSourceRef]app.SynthesisSourceView{}}
	prepared := app.SynthesisGoalPreparedInput{Progress: app.SynthesisGoalSelectionProgress{Request: app.SynthesisGoalRequest{ID: base.value.GoalRequestID, WorkspaceID: execution.WorkspaceID, Goal: "数据库复习", Status: app.SynthesisGoalCatalogReady, CatalogBatches: 1}, CatalogBatches: 1, PreparedBatches: 1, Selections: 2, Succeeded: 2}}
	for i := 0; i < 2; i++ {
		ref := domain.SynthesisSourceRef{Source: base.value.Processing.SourceEvent.Source, SourceSpanID: synthesisWorkflowTestID(120 + i), Title: "Selected original"}
		if i == 1 {
			ref.Source.SourceID = synthesisWorkflowTestID(130)
			ref.Source.SourceVersionID = synthesisWorkflowTestID(131)
			ref.Source.ContentArtifactID = synthesisWorkflowTestID(132)
			ref.Source.ParseProjectionID = synthesisWorkflowTestID(133)
		}
		text := []string{"SELECTED-ORIGINAL-ONE", "SELECTED-ORIGINAL-TWO"}[i]
		ref.ExcerptHash = hashBytes([]byte(text))
		prepared.Sources = append(prepared.Sources, app.SynthesisGoalPreparedSource{Excerpt: app.SynthesisSourceExcerpt{Reference: ref, Text: text}, Points: []app.GoalSourcePointBinding{{SelectionID: synthesisWorkflowTestID(140 + i), ModelRunID: synthesisWorkflowTestID(150 + i), Locator: app.KnowledgePointLocator{ProfileRevisionID: synthesisWorkflowTestID(160 + i), Kind: app.KnowledgePointKindKnowledgePoint, Index: 0}, Reason: "相关知识"}}})
		sources.views[ref] = app.SynthesisSourceView{Reference: ref, Availability: domain.MaterialAvailable, Text: text}
	}
	preparer := &goalPreparationFixture{value: prepared}
	executor.dependencies.Store, executor.dependencies.Goals, executor.dependencies.Sources = store, preparer, sources
	raw, err := json.Marshal(SynthesisStartInput{GoalRequestID: base.value.GoalRequestID, ProcessingID: base.value.Processing.ID, ExecutionNo: 1})
	if err != nil {
		t.Fatal(err)
	}
	executor.dependencies.Runs.(*synthesisRunFixture).value.Input = raw
	execution.NodeKey, execution.NodeKind = SynthesisPrepareNodeKind, SynthesisPrepareNodeKind
	if _, err := executor.Execute(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	if preparer.calls != 1 || base.value.Input == nil || base.value.Input.Goal == nil || base.value.Input.SemanticPromptVersion != app.SynthesisSourceIdentitySemanticPromptVersion || base.value.Input.GenerationPromptVersion != app.SynthesisGenerationFormatGoalPromptVersion {
		t.Fatal("goal not prepared/frozen")
	}
	if strings.Contains(string(store.document), "SELECTED-ORIGINAL") {
		t.Fatal("original text persisted in workflow input")
	}
	// 模拟重启，从 JSON 重建执行器和冻结快照。
	restarted, err := NewSynthesisExecutor(executor.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	var frozen SynthesisFrozenInput
	if err := json.Unmarshal(store.document, &frozen); err != nil {
		t.Fatal(err)
	}
	base.value.Input = &frozen
	if _, err := restarted.Execute(t.Context(), execution); err != nil || preparer.calls != 1 {
		t.Fatalf("prepare replay did work: %v", err)
	}
	input, err := restarted.openInput(t.Context(), base.value, execution.NodeRunID, execution.NodeAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if sources.reads != 0 || sources.opens != 2 || !EqualSynthesisFrozenGeneration(frozen, input) || !reflect.DeepEqual(input.Goal.Materials[1].Points, prepared.Sources[1].Points) {
		t.Fatal("restart lost exact goal knowledge/source binding")
	}
	for _, version := range []string{"", "v5", "v6", "v8"} {
		changed := frozen
		changed.SemanticPromptVersion = version
		if changed.Validate() == nil || EqualSynthesisFrozenGeneration(changed, input) {
			t.Fatal("semantic version drift accepted")
		}
		if version != "" {
			changed.RequestHash, _ = changed.ComputeHash()
			if changed.Validate() == nil {
				t.Fatal("forged semantic version accepted with recomputed hash")
			}
		}
	}
	changed := frozen
	changed.Goal = new(app.SynthesisGoalBinding)
	*changed.Goal = *frozen.Goal
	changed.Goal.Text = "另一个目标"
	if changed.Validate() == nil || EqualSynthesisFrozenGeneration(changed, input) {
		t.Fatal("goal text changed without invalidating frozen hash")
	}
	// 已保存的历史原文片段绝不能提升为当前证据。
	ref := prepared.Sources[1].Excerpt.Reference
	sources.views[ref] = app.SynthesisSourceView{Reference: ref, Availability: domain.MaterialStale, SnapshotText: prepared.Sources[1].Excerpt.Text}
	if _, err := restarted.openInput(t.Context(), base.value, execution.NodeRunID, execution.NodeAttemptID); err == nil {
		t.Fatal("historical snapshot accepted for generation")
	}
	// 成功准备和已生成增量都不等于语义审批通过。
	execution.NodeKey, execution.NodeKind = SynthesisApplyNodeKind, SynthesisApplyNodeKind
	if _, err := restarted.Execute(t.Context(), execution); err == nil || candidates.applications != 0 || base.prepares != 0 {
		t.Fatal("application bypassed independent semantic review")
	}
}

type goalFreezeFixture struct {
	*synthesisExecutionFixtureStore
	document []byte
}

func (s *goalFreezeFixture) FreezeSynthesisInput(_ context.Context, _ workflowapp.ExecutionContext, input SynthesisFrozenInput) (SynthesisFrozenInput, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return SynthesisFrozenInput{}, err
	}
	s.document = raw
	var value SynthesisFrozenInput
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, err
	}
	s.value.Input = &value
	return value, nil
}

type goalPreparationFixture struct {
	value app.SynthesisGoalPreparedInput
	calls int
}

func (p *goalPreparationFixture) Prepare(_ context.Context, w, id foundation.ID) (app.SynthesisGoalPreparedInput, error) {
	p.calls++
	if w != p.value.Progress.Request.WorkspaceID || id != p.value.Progress.Request.ID {
		return app.SynthesisGoalPreparedInput{}, errors.New("wrong goal lookup")
	}
	return p.value, nil
}

type goalExactSourceFixture struct {
	views        map[domain.SynthesisSourceRef]app.SynthesisSourceView
	reads, opens int
}

func (s *goalExactSourceFixture) ReadSynthesisSource(context.Context, domain.SynthesisSourceVersion) ([]app.SynthesisSourceExcerpt, error) {
	s.reads++
	return nil, errors.New("must not read unselected modules")
}
func (s *goalExactSourceFixture) OpenSynthesisSource(_ context.Context, ref domain.SynthesisSourceRef) (app.SynthesisSourceView, error) {
	s.opens++
	return s.views[ref], nil
}

func TestGoalStartInputRejectsExplicitNullAndForeignFrozenGoal(t *testing.T) {
	executor, execution, store, _, _ := synthesisExecutorFixture(t)
	for _, goal := range []string{`null`, `""`, `"invalid"`, `false`} {
		raw := `{"processing_id":"` + string(store.value.Processing.ID) + `","execution_no":1,"apply_recovery":false,"goal_request_id":` + goal + `}`
		if _, err := DecodeSynthesisStartInput([]byte(raw)); err == nil {
			t.Fatalf("invalid goal accepted: %s", goal)
		}
	}
	// 已排队目标不能使用普通处理账本执行，
	// 即使引用来源及其他全部运行时 ID 恰好一致也不例外。
	raw, err := json.Marshal(SynthesisStartInput{GoalRequestID: synthesisWorkflowTestID(900), ProcessingID: store.value.Processing.ID, ExecutionNo: 1})
	if err != nil {
		t.Fatal(err)
	}
	executor.dependencies.Runs.(*synthesisRunFixture).value.Input = raw
	if _, err := executor.Execute(t.Context(), execution); err == nil {
		t.Fatal("queued goal ignored missing durable goal identity")
	}
}

func TestGoalFrozenSnapshotRejectsOversizedKnowledgeBeforePersistence(t *testing.T) {
	_, _, base, _, _ := synthesisExecutorFixture(t)
	frozen := *base.value.Input
	frozen.Notes = []SynthesisFrozenNote{}
	frozen.Sources = frozen.Sources[:1]
	ref := frozen.Sources[0]
	frozen.Goal = &app.SynthesisGoalBinding{RequestID: synthesisWorkflowTestID(6000), Text: "完整整理知识", Materials: []app.GoalSourceMaterial{{Reference: ref}}}
	for i := 0; i < 512; i++ {
		kind := app.KnowledgePointKindKnowledgePoint
		if i >= 256 {
			kind = app.KnowledgePointKindExample
		}
		frozen.Goal.Materials[0].Points = append(frozen.Goal.Materials[0].Points, app.GoalSourcePointBinding{SelectionID: synthesisWorkflowTestID(6100 + i/32), ModelRunID: synthesisWorkflowTestID(6200 + i/32), Locator: app.KnowledgePointLocator{ProfileRevisionID: synthesisWorkflowTestID(6300), Kind: kind, Index: i % 256}, Reason: strings.Repeat("r", 2048)})
	}
	if err := frozen.Goal.Validate(ref.Source.WorkspaceID, frozen.Sources); err != nil {
		t.Fatalf("point bindings must be structurally valid: %v", err)
	}
	frozen.RequestHash, _ = frozen.ComputeHash()
	var classified *foundation.Error
	if err := frozen.Validate(); !errors.As(err, &classified) || classified.Code != app.ErrorCodeSynthesisModelInputTooLarge {
		t.Fatalf("oversized metadata must fail explicitly before persistence: %v", err)
	}
	frozen.Goal.Materials[0].Points = frozen.Goal.Materials[0].Points[:32]
	frozen.RequestHash, _ = frozen.ComputeHash()
	if err := frozen.Validate(); err != nil {
		t.Fatalf("bounded snapshot rejected: %v", err)
	}
}
