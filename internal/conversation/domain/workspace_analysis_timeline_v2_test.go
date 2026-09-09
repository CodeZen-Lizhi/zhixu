package domain

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

func TestWorkspaceAnalysisTimelineV2PreservesRepeatedSearchAndReadOrder(t *testing.T) {
	t.Parallel()
	timeline := validWorkspaceAnalysisTimelineV2()
	raw := mustJSON(t, timeline)
	canonical, err := CanonicalizeWorkspaceAnalysisTimeline(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(canonical.Timeline, timeline) || string(canonical.Document) != string(raw) {
		t.Fatal("v2 canonicalization changed the journal projection")
	}
	var phases []WorkspaceAnalysisPhase
	for _, item := range canonical.Timeline.Items {
		if item.Kind == WorkspaceAnalysisTimelineItemTool {
			phases = append(phases, item.Phase)
		}
	}
	want := []WorkspaceAnalysisPhase{WorkspaceAnalysisPhaseRetrieveEvidence, WorkspaceAnalysisPhaseReadEvidence,
		WorkspaceAnalysisPhaseRetrieveEvidence, WorkspaceAnalysisPhaseReadEvidence, WorkspaceAnalysisPhaseValidateCitations}
	if !reflect.DeepEqual(phases, want) || canonical.Timeline.Items[7].Summary.Source.EvidenceRef != "E32" {
		t.Fatalf("dynamic order/global reference changed: %v", phases)
	}
	replayed, err := CanonicalizeWorkspaceAnalysisTimeline(canonical.Document)
	if err != nil || replayed.Hash != canonical.Hash || !reflect.DeepEqual(replayed.Timeline.Items, timeline.Items) {
		t.Fatalf("v2 timeline replay changed: %v", err)
	}
	for _, forbidden := range []string{"prompt", "arguments", "reasoning", "excerpt", "provider", "receipt", "search_evidence_ref"} {
		if strings.Contains(string(canonical.Document), forbidden) {
			t.Fatalf("v2 timeline leaked %s", forbidden)
		}
	}
}

func TestWorkspaceAnalysisTimelineV2AllowsOptionalGitAndLoopCitationChecks(t *testing.T) {
	t.Parallel()
	for _, choice := range []WorkspaceAnalysisTimelineItem{
		v2TimelineTool(WorkspaceAnalysisPhaseInspectWorkspace, &WorkspaceAnalysisTimelineSummary{
			Kind: WorkspaceAnalysisTimelineSummaryGit, Git: &WorkspaceAnalysisGitStatus{Branch: "dev", Head: strings.Repeat("a", 40), Clean: true},
		}),
		v2TimelineTool(WorkspaceAnalysisPhaseValidateCitations, &WorkspaceAnalysisTimelineSummary{
			Kind: WorkspaceAnalysisTimelineSummaryCitation, Citation: &WorkspaceAnalysisTimelineCitationSummary{
				ValidCount: 1, ReasonCodes: []string{"OK"},
			},
		}),
	} {
		timeline := validWorkspaceAnalysisTimelineV2()
		// The optional tool is chosen after a source read, not at a fixed first phase.
		items := append([]WorkspaceAnalysisTimelineItem{}, timeline.Items[:4]...)
		items = append(items, v2TimelineModel(WorkspaceAnalysisPhaseDecideNext), choice)
		timeline.Items = append(items, timeline.Items[4:]...)
		renumberWorkspaceAnalysisTimelineV2(&timeline)
		timeline.Budget.ModelCalls.Used, timeline.Budget.ToolCalls.Used = 8, 6
		timeline.Budget.InputTokens.Used, timeline.Budget.OutputTokens.Used = 800, 80
		if _, err := CanonicalizeWorkspaceAnalysisTimeline(mustJSON(t, timeline)); err != nil {
			t.Fatalf("valid optional %s rejected: %v", choice.Phase, err)
		}
	}
}

func TestWorkspaceAnalysisTimelineV2RejectsCorruptCountsBindingsAndOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisTimeline)
	}{
		{"unknown version", func(value *WorkspaceAnalysisTimeline) { value.SchemaVersion = "v3" }},
		{"v1 relabel", func(value *WorkspaceAnalysisTimeline) { value.SchemaVersion = WorkspaceAnalysisTimelineSchemaVersionV1 }},
		{"identity namespace", func(value *WorkspaceAnalysisTimeline) { value.AnalysisRunID = value.AnswerID }},
		{"journal gap", func(value *WorkspaceAnalysisTimeline) { value.Items[3].Sequence++ }},
		{"phase grouped", func(value *WorkspaceAnalysisTimeline) {
			sort.SliceStable(value.Items, func(i, j int) bool { return value.Items[i].Phase < value.Items[j].Phase })
			renumberWorkspaceAnalysisTimelineV2(value)
		}},
		{"tool without decision", func(value *WorkspaceAnalysisTimeline) {
			value.Items = append(value.Items[:2], value.Items[3:]...)
			renumberWorkspaceAnalysisTimelineV2(value)
		}},
		{"candidate without finish decision", func(value *WorkspaceAnalysisTimeline) {
			value.Items = append(value.Items[:8], value.Items[9:]...)
			renumberWorkspaceAnalysisTimelineV2(value)
		}},
		{"model usage exceeds decision cap", func(value *WorkspaceAnalysisTimeline) {
			value.Items[0].Summary.ModelUsage.OutputTokens = agentdomain.WorkspaceAnalysisV2DecisionMaxOutputTokens + 1
		}},
		{"model usage exceeds input cap", func(value *WorkspaceAnalysisTimeline) {
			value.Items[0].Summary.ModelUsage.InputTokens = agentdomain.WorkspaceAnalysisV2MaxInputTokensPerModelCall + 1
		}},
		{"wrong tool version", func(value *WorkspaceAnalysisTimeline) { value.Items[1].ToolRef.Version = 2 }},
		{"unapproved tool", func(value *WorkspaceAnalysisTimeline) { value.Items[1].ToolRef.Name = "WriteFile" }},
		{"model has tool identity", func(value *WorkspaceAnalysisTimeline) { value.Items[0].ToolRef = value.Items[1].ToolRef }},
		{"source ref outside namespace", func(value *WorkspaceAnalysisTimeline) { value.Items[7].Summary.Source.EvidenceRef = "E33" }},
		{"source ref noncanonical", func(value *WorkspaceAnalysisTimeline) { value.Items[7].Summary.Source.EvidenceRef = "E01" }},
		{"source ref rebound", func(value *WorkspaceAnalysisTimeline) { value.Items[7].Summary.Source.EvidenceRef = "E1" }},
		{"citation overflow", func(value *WorkspaceAnalysisTimeline) { value.Items[10].Summary.Citation.ValidCount = 33 }},
		{"citation unbound to read evidence", func(value *WorkspaceAnalysisTimeline) { value.Items[10].Summary.Citation.ValidCount = 3 }},
		{"citation count reason mismatch", func(value *WorkspaceAnalysisTimeline) {
			value.Items[10].Summary.Citation.ReasonCodes = []string{"EVIDENCE_INELIGIBLE"}
		}},
		{"review before validation", func(value *WorkspaceAnalysisTimeline) {
			value.Items[9], value.Items[10] = value.Items[10], value.Items[9]
			renumberWorkspaceAnalysisTimelineV2(value)
		}},
		{"success without review", func(value *WorkspaceAnalysisTimeline) { value.Items = value.Items[:11] }},
		{"model ledger differs", func(value *WorkspaceAnalysisTimeline) { value.Budget.ModelCalls.Used-- }},
		{"tool ledger differs", func(value *WorkspaceAnalysisTimeline) { value.Budget.ToolCalls.Used-- }},
		{"source ledger differs", func(value *WorkspaceAnalysisTimeline) { value.Budget.SourceReads.Used-- }},
		{"input ledger differs", func(value *WorkspaceAnalysisTimeline) { value.Budget.InputTokens.Used-- }},
		{"output ledger differs", func(value *WorkspaceAnalysisTimeline) { value.Budget.OutputTokens.Used-- }},
		{"maxima changed", func(value *WorkspaceAnalysisTimeline) { value.Budget.ModelCalls.Max++ }},
		{"source budget changed to ref capacity", func(value *WorkspaceAnalysisTimeline) { value.Budget.SourceReads.Max = 32 }},
		{"negative cost", func(value *WorkspaceAnalysisTimeline) {
			value.Budget.EstimatedCostMicrounits = &WorkspaceAnalysisTimelineCounter{Used: -1, Max: 100}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validWorkspaceAnalysisTimelineV2()
			test.mutate(&value)
			if _, err := CanonicalizeWorkspaceAnalysisTimeline(mustJSON(t, value)); err == nil {
				t.Fatal("corrupt dynamic timeline accepted")
			}
		})
	}
}

func TestWorkspaceAnalysisTimelineV2UnknownCannotExposeResultsOrContinue(t *testing.T) {
	t.Parallel()
	timeline := validWorkspaceAnalysisTimelineV2()
	timeline.Items = timeline.Items[:2]
	timeline.RunStatus = WorkspaceAnalysisTimelineRunFailed
	reason := string(WorkspaceAnalysisResultUnknown)
	timeline.TerminationReason = &reason
	timeline.Items[1].Status = WorkspaceAnalysisTimelineItemUnknown
	timeline.Items[1].Summary = nil
	timeline.Items[1].ErrorCode = &reason
	timeline.Budget.ModelCalls.Used, timeline.Budget.ToolCalls.Used, timeline.Budget.SourceReads.Used = 1, 1, 0
	timeline.Budget.InputTokens.Used, timeline.Budget.OutputTokens.Used = 100, 10
	if _, err := CanonicalizeWorkspaceAnalysisTimeline(mustJSON(t, timeline)); err != nil {
		t.Fatal(err)
	}
	withSummary := cloneWorkspaceAnalysisTimelineV2(t, timeline)
	withSummary.Items[1].Summary = &WorkspaceAnalysisTimelineSummary{Kind: WorkspaceAnalysisTimelineSummarySearch, Search: &WorkspaceAnalysisTimelineSearchSummary{HitCount: 0, DegradationCodes: []string{}}}
	if _, err := CanonicalizeWorkspaceAnalysisTimeline(mustJSON(t, withSummary)); err == nil {
		t.Fatal("unknown operation exposed a fabricated successful summary")
	}
	continued := cloneWorkspaceAnalysisTimelineV2(t, timeline)
	continued.Items = append(continued.Items, v2TimelineModel(WorkspaceAnalysisPhaseDecideNext))
	renumberWorkspaceAnalysisTimelineV2(&continued)
	if _, err := CanonicalizeWorkspaceAnalysisTimeline(mustJSON(t, continued)); err == nil {
		t.Fatal("timeline continued after an unknown external result")
	}
	withoutUnknown := cloneWorkspaceAnalysisTimelineV2(t, timeline)
	withoutUnknown.Items = withoutUnknown.Items[:1]
	if _, err := CanonicalizeWorkspaceAnalysisTimeline(mustJSON(t, withoutUnknown)); err == nil {
		t.Fatal("unknown termination omitted its causal operation")
	}
}

func TestWorkspaceAnalysisTimelineV2RetainsBudgetDeniedPendingDecision(t *testing.T) {
	t.Parallel()
	timeline := emptyWorkspaceAnalysisTimelineV2()
	for i := 0; i < 12; i++ {
		timeline.Items = append(timeline.Items, v2TimelineModel(WorkspaceAnalysisPhaseDecideNext), v2TimelineSearch())
	}
	timeline.Items = append(timeline.Items, WorkspaceAnalysisTimelineItem{Kind: WorkspaceAnalysisTimelineItemModel,
		Phase: WorkspaceAnalysisPhaseDecideNext, Status: WorkspaceAnalysisTimelineItemPending})
	renumberWorkspaceAnalysisTimelineV2(&timeline)
	timeline.RunStatus = WorkspaceAnalysisTimelineRunFailed
	reason := string(WorkspaceAnalysisBudgetExhausted)
	timeline.TerminationReason = &reason
	timeline.Budget.ModelCalls.Used, timeline.Budget.ToolCalls.Used = 12, 12
	timeline.Budget.InputTokens.Used, timeline.Budget.OutputTokens.Used = 1200, 120
	if _, err := CanonicalizeWorkspaceAnalysisTimeline(mustJSON(t, timeline)); err != nil {
		t.Fatal(err)
	}
	// Charging the denied decision as another successful call violates the separate decision cap.
	timeline.Items[len(timeline.Items)-1] = v2TimelineModel(WorkspaceAnalysisPhaseDecideNext)
	renumberWorkspaceAnalysisTimelineV2(&timeline)
	if err := timeline.Validate(); err == nil {
		t.Fatal("thirteenth authorized decision accepted")
	}
}

func TestWorkspaceAnalysisTimelineV2RejectsUnknownAndMissingNullableFields(t *testing.T) {
	t.Parallel()
	raw := string(mustJSON(t, validWorkspaceAnalysisTimelineV2()))
	for _, candidate := range []string{
		strings.Replace(raw, `,"tool_ref":null`, "", 1),
		strings.Replace(raw, `,"estimated_cost_microunits":null`, "", 1),
		strings.Replace(raw, `"schema_version":"v2"`, `"schema_version":"v2","schema_version":"v2"`, 1),
		strings.Replace(raw, `"kind":"model_usage"`, `"kind":"model_usage","reasoning":"private"`, 1),
		strings.Replace(raw, `"evidence_ref":"E32"`, `"evidence_ref":"E32","search_evidence_ref":"E1"`, 1),
		raw + `{}`,
	} {
		if candidate == raw {
			t.Fatal("strict wire mutation did not change the fixture")
		}
		if _, err := CanonicalizeWorkspaceAnalysisTimeline([]byte(candidate)); err == nil {
			t.Fatal("unsafe or incomplete v2 wire document accepted")
		}
	}
}

func emptyWorkspaceAnalysisTimelineV2() WorkspaceAnalysisTimeline {
	value := emptyWorkspaceAnalysisTimeline()
	value.SchemaVersion = WorkspaceAnalysisTimelineSchemaVersionV2
	value.Budget = WorkspaceAnalysisTimelineBudget{
		ModelCalls:   WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV2MaxModelCalls},
		ToolCalls:    WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV2MaxToolCalls},
		SourceReads:  WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV2MaxSourceReads},
		InputTokens:  WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV2MaxRunInputTokens},
		OutputTokens: WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV2MaxRunOutputTokens},
	}
	return value
}

func validWorkspaceAnalysisTimelineV2() WorkspaceAnalysisTimeline {
	value := emptyWorkspaceAnalysisTimelineV2()
	value.Items = []WorkspaceAnalysisTimelineItem{
		v2TimelineModel(WorkspaceAnalysisPhaseDecideNext), v2TimelineSearch(),
		v2TimelineModel(WorkspaceAnalysisPhaseDecideNext), v2TimelineSource("E1", "b"),
		v2TimelineModel(WorkspaceAnalysisPhaseDecideNext), v2TimelineSearch(),
		v2TimelineModel(WorkspaceAnalysisPhaseDecideNext), v2TimelineSource("E32", "c"),
		v2TimelineModel(WorkspaceAnalysisPhaseDecideNext), v2TimelineModel(WorkspaceAnalysisPhaseSynthesizeAnswer),
		v2TimelineTool(WorkspaceAnalysisPhaseValidateCitations, &WorkspaceAnalysisTimelineSummary{
			Kind: WorkspaceAnalysisTimelineSummaryCitation, Citation: &WorkspaceAnalysisTimelineCitationSummary{ValidCount: 2, ReasonCodes: []string{"OK"}},
		}),
		v2TimelineModel(WorkspaceAnalysisPhaseReviewPublish),
	}
	renumberWorkspaceAnalysisTimelineV2(&value)
	value.RunStatus = WorkspaceAnalysisTimelineRunSucceeded
	reason := WorkspaceAnalysisCompleted
	value.TerminationReason = &reason
	value.Budget.ModelCalls.Used, value.Budget.ToolCalls.Used, value.Budget.SourceReads.Used = 7, 5, 2
	value.Budget.InputTokens.Used, value.Budget.OutputTokens.Used = 700, 70
	return value
}

func v2TimelineModel(phase WorkspaceAnalysisPhase) WorkspaceAnalysisTimelineItem {
	duration := int64(10)
	return WorkspaceAnalysisTimelineItem{Kind: WorkspaceAnalysisTimelineItemModel, Phase: phase,
		Status: WorkspaceAnalysisTimelineItemSucceeded, DurationMS: &duration,
		Summary: &WorkspaceAnalysisTimelineSummary{Kind: WorkspaceAnalysisTimelineSummaryModelUsage,
			ModelUsage: &WorkspaceAnalysisTimelineModelSummary{InputTokens: 100, OutputTokens: 10}},
	}
}

func v2TimelineSearch() WorkspaceAnalysisTimelineItem {
	return v2TimelineTool(WorkspaceAnalysisPhaseRetrieveEvidence, &WorkspaceAnalysisTimelineSummary{
		Kind: WorkspaceAnalysisTimelineSummarySearch, Search: &WorkspaceAnalysisTimelineSearchSummary{HitCount: 5, DegradationCodes: []string{}},
	})
}

func v2TimelineSource(ref, hashCharacter string) WorkspaceAnalysisTimelineItem {
	return v2TimelineTool(WorkspaceAnalysisPhaseReadEvidence, &WorkspaceAnalysisTimelineSummary{
		Kind: WorkspaceAnalysisTimelineSummarySource, Source: &WorkspaceAnalysisTimelineSourceSummary{EvidenceRef: ref, ContentHash: strings.Repeat(hashCharacter, 64)},
	})
}

func v2TimelineTool(phase WorkspaceAnalysisPhase, summary *WorkspaceAnalysisTimelineSummary) WorkspaceAnalysisTimelineItem {
	duration := int64(10)
	refs := map[WorkspaceAnalysisPhase]WorkspaceAnalysisTimelineToolRef{
		WorkspaceAnalysisPhaseInspectWorkspace:  {Name: "ReadGitStatus", Version: 3},
		WorkspaceAnalysisPhaseRetrieveEvidence:  {Name: "SearchKnowledge", Version: 3},
		WorkspaceAnalysisPhaseReadEvidence:      {Name: "ReadSource", Version: 4},
		WorkspaceAnalysisPhaseValidateCitations: {Name: "ValidateCitation", Version: 4},
	}
	ref := refs[phase]
	return WorkspaceAnalysisTimelineItem{Kind: WorkspaceAnalysisTimelineItemTool, Phase: phase, ToolRef: &ref,
		Status: WorkspaceAnalysisTimelineItemSucceeded, DurationMS: &duration, Summary: summary}
}

func renumberWorkspaceAnalysisTimelineV2(value *WorkspaceAnalysisTimeline) {
	for index := range value.Items {
		value.Items[index].Sequence = index + 1
	}
}

func cloneWorkspaceAnalysisTimelineV2(t *testing.T, value WorkspaceAnalysisTimeline) WorkspaceAnalysisTimeline {
	t.Helper()
	var copy WorkspaceAnalysisTimeline
	if err := json.Unmarshal(mustJSON(t, value), &copy); err != nil {
		t.Fatal(err)
	}
	return copy
}
