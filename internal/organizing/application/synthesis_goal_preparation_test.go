package application

import (
	"encoding/json"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func goalPreparedFixture(t *testing.T) (SynthesisGoalPreparedInput, SynthesisGenerationInput, SynthesisGenerationResult) {
	t.Helper()
	input, result := synthesisContractFixture()
	second := input.Sources[0]
	second.Reference.Source.SourceID = synthesisContractID(102)
	second.Reference.Source.SourceVersionID = synthesisContractID(103)
	second.Reference.Source.ContentArtifactID = synthesisContractID(104)
	second.Reference.Source.ParseProjectionID = synthesisContractID(105)
	second.Reference.Source.ContentHash = synthesisContractHash("second document")
	second.Reference.SourceSpanID = synthesisContractID(106)
	second.Text = "Redis 使用 TTL 控制缓存过期。"
	second.Reference.ExcerptHash = synthesisContractHash(second.Text)
	input.Sources = append(input.Sources, second)
	prepared := SynthesisGoalPreparedInput{Progress: SynthesisGoalSelectionProgress{Request: SynthesisGoalRequest{ID: synthesisContractID(200), WorkspaceID: input.SourceEvent.Source.WorkspaceID, Goal: "整理数据库知识", Status: SynthesisGoalCatalogReady, CatalogBatches: 1}, CatalogBatches: 1, PreparedBatches: 1, Selections: 2, Succeeded: 2}}
	for i, excerpt := range input.Sources {
		prepared.Sources = append(prepared.Sources, SynthesisGoalPreparedSource{Excerpt: excerpt, Points: []GoalSourcePointBinding{{SelectionID: synthesisContractID(210 + i), ModelRunID: synthesisContractID(220 + i), Locator: KnowledgePointLocator{ProfileRevisionID: synthesisContractID(230 + i), Kind: KnowledgePointKindKnowledgePoint, Index: 0}, Reason: "属于数据库相关知识"}}})
	}
	return prepared, input, result
}

func TestGoalBindingAdmitsExactMultipleSourcesAndRejectsMissingEvidence(t *testing.T) {
	prepared, input, result := goalPreparedFixture(t)
	goal, err := BuildSynthesisGoalBinding(prepared)
	if err != nil {
		t.Fatal(err)
	}
	input.Goal = goal
	if err := input.Validate(); err != nil {
		t.Fatalf("selected second source rejected: %v", err)
	}
	if err := result.Validate(input); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*SynthesisGenerationInput)
	}{
		{"removed selected source", func(i *SynthesisGenerationInput) { i.Sources = i.Sources[:1] }},
		{"replaced span", func(i *SynthesisGenerationInput) { i.Sources[1].Reference.SourceSpanID = synthesisContractID(999) }},
		{"changed original bytes", func(i *SynthesisGenerationInput) { i.Sources[0].Text += "tampered" }},
		{"missing point binding", func(i *SynthesisGenerationInput) { i.Goal.Materials[0].Points = nil }},
		{"foreign goal workspace", func(i *SynthesisGenerationInput) { i.SourceEvent.Source.WorkspaceID = synthesisContractID(999) }},
		{"mixed goal and fusion", func(i *SynthesisGenerationInput) { i.SourceEvent.Fusion = &domain.SynthesisFusionTrigger{} }},
		{"missing goal", func(i *SynthesisGenerationInput) { i.Goal = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var changed SynthesisGenerationInput
			raw, _ := json.Marshal(input)
			if err := json.Unmarshal(raw, &changed); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&changed)
			if err := changed.Validate(); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	result.Notes = append(result.Notes, result.Notes[0])
	if err := result.Validate(input); err == nil {
		t.Fatal("goal split into multiple main notes")
	}
	result.Notes = nil
	if err := result.Validate(input); err == nil {
		t.Fatal("goal produced no candidate")
	}
	// 模型提供方不能通过修改调用方共享的切片来选择被篡改的目标。
	prepared.Sources[0].Points[0].Reason = "changed"
	if goal.Materials[0].Points[0].Reason == "changed" {
		t.Fatal("goal binding aliases caller memory")
	}
}

func TestGoalBindingRejectsUnreadyAndEmptySelections(t *testing.T) {
	prepared, _, _ := goalPreparedFixture(t)
	prepared.Progress.Succeeded--
	prepared.Progress.Failed++
	if _, err := BuildSynthesisGoalBinding(prepared); err == nil {
		t.Fatal("incomplete selection admitted")
	}
	prepared.Progress.Succeeded++
	prepared.Progress.Failed--
	prepared.Sources = nil
	if _, err := BuildSynthesisGoalBinding(prepared); err == nil {
		t.Fatal("empty candidate admitted")
	}
}

func TestGoalBindingPreservesMoreThanOneSelectionSlicePerExcerpt(t *testing.T) {
	prepared, input, _ := goalPreparedFixture(t)
	for i := 1; i < 40; i++ {
		p := prepared.Sources[0].Points[0]
		p.Locator.Index = i
		p.SelectionID = synthesisContractID(300 + i/32)
		p.ModelRunID = synthesisContractID(400 + i/32)
		prepared.Sources[0].Points = append(prepared.Sources[0].Points, p)
	}
	binding, err := BuildSynthesisGoalBinding(prepared)
	if err != nil {
		t.Fatal(err)
	}
	input.Goal = binding
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(binding.Materials[0].Points) != 40 {
		t.Fatal("shared excerpt lost selected points")
	}
}
