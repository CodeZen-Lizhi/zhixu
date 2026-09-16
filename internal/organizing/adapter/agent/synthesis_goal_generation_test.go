package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

func TestGoalGenerationAndIndependentReviewReceiveGoalAndTwoSources(t *testing.T) {
	output := strings.Replace(synthesisFactOutput, `["S001"]`, `["S001","S002"]`, 1)
	review := `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"},{"source":"S002","verdict":"SUPPORTED"}]}]}`
	harness := newSynthesisHarness(t, output, review)
	input, generate, validate := synthesisFixtureInput(t)
	input.Sources = append(input.Sources, synthesisFixtureSource(200, "SECOND-ORIGINAL: Redis entries expire after five minutes."))
	prepared := app.SynthesisGoalPreparedInput{Progress: app.SynthesisGoalSelectionProgress{Request: app.SynthesisGoalRequest{ID: synthesisTestID(500), WorkspaceID: generate.WorkspaceID, Goal: "整理数据库缓存的过期机制", Status: app.SynthesisGoalCatalogReady, CatalogBatches: 1}, CatalogBatches: 1, PreparedBatches: 1, Selections: 2, Succeeded: 2}}
	for i, source := range input.Sources {
		prepared.Sources = append(prepared.Sources, app.SynthesisGoalPreparedSource{Excerpt: source, Points: []app.GoalSourcePointBinding{{SelectionID: synthesisTestID(510 + i), ModelRunID: synthesisTestID(520 + i), Locator: app.KnowledgePointLocator{ProfileRevisionID: synthesisTestID(530 + i), Kind: app.KnowledgePointKindKnowledgePoint, Index: 0}, Reason: "相关知识"}}})
	}
	var err error
	input.Goal, err = app.BuildSynthesisGoalBinding(prepared)
	if err != nil {
		t.Fatal(err)
	}
	result, err := harness.model.GenerateSynthesisForExecution(t.Context(), generate, input)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := harness.model.ValidateSynthesisSemanticsForExecution(t.Context(), validate, input, result)
	if err != nil || !receipt.Accepted || receipt.ModelRunID == result.ModelRunID {
		t.Fatalf("independent review %+v %v", receipt, err)
	}
	if len(result.Notes) != 1 || len(result.Notes[0].Delta.Operations[0].Item.Fact.Sources) != 2 || harness.provider.CallCount() != 2 {
		t.Fatal("multi-source single main note was not generated/reviewed")
	}
	for _, request := range harness.provider.Calls() {
		var content strings.Builder
		for _, message := range request.Messages {
			content.WriteString(message.Content)
		}
		if !strings.Contains(content.String(), input.Goal.Text) || !strings.Contains(content.String(), "SECOND-ORIGINAL") || !strings.Contains(content.String(), "RAW-EXCERPT-ONLY") {
			t.Fatal("model omitted goal or selected evidence")
		}
		if strings.Contains(content.String(), string(input.Goal.RequestID)) || strings.Contains(content.String(), string(input.Goal.Materials[0].Points[0].SelectionID)) {
			t.Fatal("model received authoritative goal/selection identities")
		}
	}
	for _, id := range []foundation.ID{result.ModelRunID, receipt.ModelRunID} {
		record, ok := harness.store.runs[id]
		if !ok || record.Prompt.Version != app.SynthesisGoalPromptVersion {
			t.Fatal("goal model run missing or used a legacy prompt")
		}
	}

	// 持久化的目标身份不能被面向提供方的文本替换。
	raw, err := json.Marshal(synthesisInput(input))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "profile_revision_id") || !strings.Contains(string(raw), "user_goal") {
		t.Fatal("goal provider projection is invalid")
	}
}
