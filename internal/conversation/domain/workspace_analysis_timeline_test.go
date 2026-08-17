package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

func TestWorkspaceAnalysisTimelineCanonicalizesTypedSafeProjection(t *testing.T) {
	timeline := validWorkspaceAnalysisTimeline()
	raw, err := json.Marshal(timeline)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalizeWorkspaceAnalysisTimeline(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(canonical.Timeline, timeline) || string(canonical.Document) != string(raw) || len(canonical.Hash) != 64 {
		t.Fatalf("canonical=%#v raw=%s", canonical, raw)
	}
	if canonical.Hash != "84ea784346f65304247bd9ef30448b816314ffebe87e0cd389055ba7d88c9b10" {
		t.Fatalf("workspace analysis timeline golden hash=%s", canonical.Hash)
	}
	replayed, err := CanonicalizeWorkspaceAnalysisTimeline(canonical.Document)
	if err != nil || canonical.Hash != replayed.Hash || string(canonical.Document) != string(replayed.Document) {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	for _, forbidden := range []string{"path", "porcelain", "prompt", "excerpt", "arguments", "receipt", "provider", "candidate_body"} {
		if strings.Contains(string(canonical.Document), forbidden) {
			t.Fatalf("timeline leaked forbidden field %q: %s", forbidden, canonical.Document)
		}
	}
}

func TestWorkspaceAnalysisTimelineRequiresExplicitNullableFields(t *testing.T) {
	timeline := emptyWorkspaceAnalysisTimeline()
	timeline.Items = []WorkspaceAnalysisTimelineItem{{
		Sequence: 1, Kind: WorkspaceAnalysisTimelineItemNode, Phase: WorkspaceAnalysisPhaseInspectWorkspace,
		Status: WorkspaceAnalysisTimelineItemPending,
	}}
	raw, err := json.Marshal(timeline)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`,"termination_reason":null`, `,"tool_ref":null`, `,"duration_ms":null`, `,"error_code":null`,
		`,"summary":null`, `,"estimated_cost_microunits":null`,
	} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("fixture does not contain %s: %s", required, raw)
		}
		withoutField := strings.Replace(string(raw), required, "", 1)
		if _, err := CanonicalizeWorkspaceAnalysisTimeline([]byte(withoutField)); err == nil {
			t.Fatalf("missing required nullable field accepted: %s", withoutField)
		}
	}
}

func TestWorkspaceAnalysisTimelineRejectsNonCanonicalDocuments(t *testing.T) {
	raw, err := json.Marshal(validWorkspaceAnalysisTimeline())
	if err != nil {
		t.Fatal(err)
	}
	tests := []string{
		strings.TrimSuffix(string(raw), "}") + `,"workspace_path":"/private/repo"}`,
		strings.Replace(string(raw), `"schema_version":"v1"`, `"schema_version":"v1","schema_version":"v1"`, 1),
		string(raw) + `{}`,
		strings.Replace(string(raw), `"kind":"git"`, `"kind":"git","prompt":"leak"`, 1),
		strings.Replace(string(raw), `"truncated":false`, `"truncated":false,"excerpt":"leak"`, 1),
	}
	for _, candidate := range tests {
		if _, err := CanonicalizeWorkspaceAnalysisTimeline([]byte(candidate)); err == nil {
			t.Fatalf("non-canonical timeline accepted: %s", candidate)
		}
	}
}

func TestWorkspaceAnalysisTimelineRunStatusReasonMatrix(t *testing.T) {
	refusal := string(WorkspaceAnalysisEvidenceInsufficient)
	clarification := WorkspaceAnalysisClarificationRequired
	failed := string(WorkspaceAnalysisToolFailed)
	cancelled := string(WorkspaceAnalysisCancelled)
	completed := WorkspaceAnalysisCompleted
	tests := []struct {
		name      string
		status    WorkspaceAnalysisTimelineRunStatus
		reason    *string
		wantError bool
	}{
		{name: "queued", status: WorkspaceAnalysisTimelineRunQueued},
		{name: "running", status: WorkspaceAnalysisTimelineRunRunning},
		{name: "succeeded", status: WorkspaceAnalysisTimelineRunSucceeded, reason: &completed},
		{name: "refused", status: WorkspaceAnalysisTimelineRunRefused, reason: &refusal},
		{name: "clarification", status: WorkspaceAnalysisTimelineRunClarificationRequired, reason: &clarification},
		{name: "failed", status: WorkspaceAnalysisTimelineRunFailed, reason: &failed},
		{name: "cancelled", status: WorkspaceAnalysisTimelineRunCancelled, reason: &cancelled},
		{name: "active with reason", status: WorkspaceAnalysisTimelineRunRunning, reason: &failed, wantError: true},
		{name: "success without reason", status: WorkspaceAnalysisTimelineRunSucceeded, wantError: true},
		{name: "refusal with failure", status: WorkspaceAnalysisTimelineRunRefused, reason: &failed, wantError: true},
		{name: "failure with unknown", status: WorkspaceAnalysisTimelineRunFailed, reason: pointerToWorkspaceAnalysisString("UNCLASSIFIED"), wantError: true},
		{name: "cancelled with generic failure", status: WorkspaceAnalysisTimelineRunCancelled, reason: &failed, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			timeline := emptyWorkspaceAnalysisTimeline()
			timeline.RunStatus, timeline.TerminationReason = test.status, test.reason
			if test.status == WorkspaceAnalysisTimelineRunSucceeded {
				success := validWorkspaceAnalysisTimeline()
				timeline.Items = success.Items
				timeline.Budget = success.Budget
			}
			err := timeline.Validate()
			if test.wantError != (err != nil) {
				t.Fatalf("status=%q reason=%v err=%v", test.status, test.reason, err)
			}
		})
	}
}

func TestWorkspaceAnalysisTimelineItemEnforcesKindStatusAndExactBinding(t *testing.T) {
	duration := int64(20)
	validModel := WorkspaceAnalysisTimelineItem{
		Sequence: 1, Kind: WorkspaceAnalysisTimelineItemModel, Phase: WorkspaceAnalysisPhaseRetrieveEvidence,
		Status: WorkspaceAnalysisTimelineItemSucceeded, DurationMS: &duration,
		Summary: &WorkspaceAnalysisTimelineSummary{
			Kind:       WorkspaceAnalysisTimelineSummaryModelUsage,
			ModelUsage: &WorkspaceAnalysisTimelineModelSummary{InputTokens: 100, OutputTokens: 50},
		},
	}
	validTool := WorkspaceAnalysisTimelineItem{
		Sequence: 1, Kind: WorkspaceAnalysisTimelineItemTool, Phase: WorkspaceAnalysisPhaseRetrieveEvidence,
		Status: WorkspaceAnalysisTimelineItemSucceeded, DurationMS: &duration,
		ToolRef: &WorkspaceAnalysisTimelineToolRef{Name: "SearchKnowledge", Version: 2},
		Summary: &WorkspaceAnalysisTimelineSummary{
			Kind:   WorkspaceAnalysisTimelineSummarySearch,
			Search: &WorkspaceAnalysisTimelineSearchSummary{HitCount: 2, DegradationCodes: []string{}},
		},
	}
	if err := validModel.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := validTool.Validate(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		item WorkspaceAnalysisTimelineItem
	}{
		{name: "model at tool-only phase", item: mutateTimelineItem(validModel, func(item *WorkspaceAnalysisTimelineItem) { item.Phase = WorkspaceAnalysisPhaseInspectWorkspace })},
		{name: "wrong tool version", item: mutateTimelineItem(validTool, func(item *WorkspaceAnalysisTimelineItem) { item.ToolRef.Version = 1 })},
		{name: "tool summary mismatch", item: mutateTimelineItem(validTool, func(item *WorkspaceAnalysisTimelineItem) {
			item.Summary = &WorkspaceAnalysisTimelineSummary{Kind: WorkspaceAnalysisTimelineSummaryModelUsage, ModelUsage: &WorkspaceAnalysisTimelineModelSummary{}}
		})},
		{name: "active has summary", item: mutateTimelineItem(validTool, func(item *WorkspaceAnalysisTimelineItem) {
			item.Status = WorkspaceAnalysisTimelineItemStarted
			item.DurationMS = nil
		})},
		{name: "plan output exceeds budget", item: mutateTimelineItem(validModel, func(item *WorkspaceAnalysisTimelineItem) {
			item.Summary.ModelUsage.OutputTokens = agentdomain.WorkspaceAnalysisV1PlanMaxOutputTokens + 1
		})},
		{name: "unknown has wrong code", item: func() WorkspaceAnalysisTimelineItem {
			code := string(WorkspaceAnalysisToolFailed)
			return WorkspaceAnalysisTimelineItem{Sequence: 1, Kind: WorkspaceAnalysisTimelineItemNode, Phase: WorkspaceAnalysisPhaseInspectWorkspace,
				Status: WorkspaceAnalysisTimelineItemUnknown, DurationMS: &duration, ErrorCode: &code}
		}()},
		{name: "failed has private code", item: func() WorkspaceAnalysisTimelineItem {
			code := "POSTGRES_CONNECTION_RESET"
			return WorkspaceAnalysisTimelineItem{Sequence: 1, Kind: WorkspaceAnalysisTimelineItemNode, Phase: WorkspaceAnalysisPhaseInspectWorkspace,
				Status: WorkspaceAnalysisTimelineItemFailed, DurationMS: &duration, ErrorCode: &code}
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.item.Validate(); err == nil {
				t.Fatalf("item unexpectedly accepted: %#v", test.item)
			}
		})
	}
}

func TestWorkspaceAnalysisTimelineSummaryRejectsUnsafeOrUncanonicalFacts(t *testing.T) {
	valid := []WorkspaceAnalysisTimelineSummary{
		{Kind: WorkspaceAnalysisTimelineSummaryGit, Git: &WorkspaceAnalysisGitStatus{Branch: "main", Head: strings.Repeat("a", 40), Clean: true}},
		{Kind: WorkspaceAnalysisTimelineSummarySearch, Search: &WorkspaceAnalysisTimelineSearchSummary{HitCount: 3, DegradationCodes: []string{"EMBEDDING_UNAVAILABLE", "RERANK_UNAVAILABLE"}}},
		{Kind: WorkspaceAnalysisTimelineSummarySource, Source: &WorkspaceAnalysisTimelineSourceSummary{EvidenceRef: "E1", ContentHash: strings.Repeat("b", 64)}},
		{Kind: WorkspaceAnalysisTimelineSummarySource, Source: &WorkspaceAnalysisTimelineSourceSummary{EvidenceRef: "E5", ContentHash: strings.Repeat("c", 64), Truncated: true}},
		{Kind: WorkspaceAnalysisTimelineSummaryCitation, Citation: &WorkspaceAnalysisTimelineCitationSummary{ValidCount: 1, ReasonCodes: []string{"OK"}}},
		{Kind: WorkspaceAnalysisTimelineSummaryModelUsage, ModelUsage: &WorkspaceAnalysisTimelineModelSummary{InputTokens: 10, OutputTokens: 5}},
	}
	for _, summary := range valid {
		if err := summary.Validate(); err != nil {
			t.Fatalf("valid summary=%#v err=%v", summary, err)
		}
	}

	invalid := []WorkspaceAnalysisTimelineSummary{
		{Kind: WorkspaceAnalysisTimelineSummaryGit, Git: valid[0].Git, Search: valid[1].Search},
		{Kind: WorkspaceAnalysisTimelineSummarySearch, Search: &WorkspaceAnalysisTimelineSearchSummary{HitCount: 6, DegradationCodes: []string{}}},
		{Kind: WorkspaceAnalysisTimelineSummarySearch, Search: &WorkspaceAnalysisTimelineSearchSummary{HitCount: 1, DegradationCodes: []string{"RERANK_UNAVAILABLE", "EMBEDDING_UNAVAILABLE"}}},
		{Kind: WorkspaceAnalysisTimelineSummarySource, Source: &WorkspaceAnalysisTimelineSourceSummary{EvidenceRef: "E6", ContentHash: strings.Repeat("b", 64)}},
		{Kind: WorkspaceAnalysisTimelineSummaryCitation, Citation: &WorkspaceAnalysisTimelineCitationSummary{InvalidCount: 1, ReasonCodes: []string{"RAW_PROVIDER_ERROR"}}},
		{Kind: WorkspaceAnalysisTimelineSummaryModelUsage, ModelUsage: &WorkspaceAnalysisTimelineModelSummary{InputTokens: agentdomain.WorkspaceAnalysisV1MaxInputTokensPerModelCall + 1}},
	}
	for _, summary := range invalid {
		if err := summary.Validate(); err == nil {
			t.Fatalf("unsafe summary unexpectedly accepted: %#v", summary)
		}
	}
}

func TestSuccessfulWorkspaceAnalysisTimelineRequiresCompleteOperationProjection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisTimeline)
	}{
		{name: "empty operations", mutate: func(timeline *WorkspaceAnalysisTimeline) { timeline.Items = []WorkspaceAnalysisTimelineItem{} }},
		{name: "missing review", mutate: func(timeline *WorkspaceAnalysisTimeline) { timeline.Items = timeline.Items[:7] }},
		{name: "budget does not match projected tools", mutate: func(timeline *WorkspaceAnalysisTimeline) { timeline.Budget.ToolCalls.Used++ }},
		{name: "budget does not match projected source reads", mutate: func(timeline *WorkspaceAnalysisTimeline) { timeline.Budget.SourceReads.Used++ }},
		{name: "budget does not match model input", mutate: func(timeline *WorkspaceAnalysisTimeline) { timeline.Budget.InputTokens.Used++ }},
		{name: "budget does not match model output", mutate: func(timeline *WorkspaceAnalysisTimeline) { timeline.Budget.OutputTokens.Used++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			timeline := validWorkspaceAnalysisTimeline()
			test.mutate(&timeline)
			if err := timeline.Validate(); err == nil {
				t.Fatalf("incomplete successful timeline accepted: %#v", timeline)
			}
		})
	}
}

func TestWorkspaceAnalysisTimelineBudgetFreezesV1Maxima(t *testing.T) {
	budget := emptyWorkspaceAnalysisTimeline().Budget
	if err := budget.Validate(); err != nil {
		t.Fatal(err)
	}
	budget.ToolCalls.Max++
	if err := budget.Validate(); err == nil {
		t.Fatal("caller-controlled tool-call maximum was accepted")
	}
	budget = emptyWorkspaceAnalysisTimeline().Budget
	budget.SourceReads.Max++
	if err := budget.Validate(); err == nil {
		t.Fatal("caller-controlled source-read maximum was accepted")
	}
	budget = emptyWorkspaceAnalysisTimeline().Budget
	budget.OutputTokens.Used = budget.OutputTokens.Max + 1
	if err := budget.Validate(); err == nil {
		t.Fatal("over-budget output usage was accepted")
	}
	budget = emptyWorkspaceAnalysisTimeline().Budget
	budget.OutputTokens.Max = agentdomain.WorkspaceAnalysisV1PlanMaxOutputTokens + agentdomain.WorkspaceAnalysisV1ReviewMaxOutputTokens + 1
	if err := budget.Validate(); err != nil {
		t.Fatalf("narrow profile output budget was rejected: %v", err)
	}
	budget.OutputTokens.Max--
	if err := budget.Validate(); err == nil {
		t.Fatal("output budget below the v1 profile floor was accepted")
	}
}

func validWorkspaceAnalysisTimeline() WorkspaceAnalysisTimeline {
	completed := WorkspaceAnalysisCompleted
	durations := []int64{10, 15, 20, 25, 30, 35, 40, 45}
	cost := WorkspaceAnalysisTimelineCounter{Used: 125, Max: 1_000}
	return WorkspaceAnalysisTimeline{
		SchemaID: WorkspaceAnalysisTimelineSchemaID, SchemaVersion: WorkspaceAnalysisTimelineSchemaVersionV1,
		WorkspaceID: workspaceAnalysisResultID(11), AnswerID: workspaceAnalysisResultID(12), AnalysisRunID: workspaceAnalysisResultID(13),
		RunStatus: WorkspaceAnalysisTimelineRunSucceeded, TerminationReason: &completed,
		Items: []WorkspaceAnalysisTimelineItem{
			{Sequence: 1, Kind: WorkspaceAnalysisTimelineItemNode, Phase: WorkspaceAnalysisPhaseInspectWorkspace,
				Status: WorkspaceAnalysisTimelineItemSucceeded, DurationMS: &durations[0]},
			{Sequence: 2, Kind: WorkspaceAnalysisTimelineItemTool, Phase: WorkspaceAnalysisPhaseInspectWorkspace,
				Status: WorkspaceAnalysisTimelineItemSucceeded, DurationMS: &durations[1],
				ToolRef: &WorkspaceAnalysisTimelineToolRef{Name: "ReadGitStatus", Version: 2},
				Summary: &WorkspaceAnalysisTimelineSummary{Kind: WorkspaceAnalysisTimelineSummaryGit,
					Git: &WorkspaceAnalysisGitStatus{Branch: "dev", Head: strings.Repeat("a", 40), StagedCount: 1}}},
			{Sequence: 3, Kind: WorkspaceAnalysisTimelineItemModel, Phase: WorkspaceAnalysisPhaseRetrieveEvidence,
				Status: WorkspaceAnalysisTimelineItemSucceeded, DurationMS: &durations[2],
				Summary: &WorkspaceAnalysisTimelineSummary{Kind: WorkspaceAnalysisTimelineSummaryModelUsage,
					ModelUsage: &WorkspaceAnalysisTimelineModelSummary{InputTokens: 100, OutputTokens: 50}}},
			{Sequence: 4, Kind: WorkspaceAnalysisTimelineItemTool, Phase: WorkspaceAnalysisPhaseRetrieveEvidence,
				Status: WorkspaceAnalysisTimelineItemSucceeded, DurationMS: &durations[3],
				ToolRef: &WorkspaceAnalysisTimelineToolRef{Name: "SearchKnowledge", Version: 2},
				Summary: &WorkspaceAnalysisTimelineSummary{Kind: WorkspaceAnalysisTimelineSummarySearch,
					Search: &WorkspaceAnalysisTimelineSearchSummary{HitCount: 3, DegradationCodes: []string{}}}},
			{Sequence: 5, Kind: WorkspaceAnalysisTimelineItemTool, Phase: WorkspaceAnalysisPhaseReadEvidence,
				Status: WorkspaceAnalysisTimelineItemSucceeded, DurationMS: &durations[4],
				ToolRef: &WorkspaceAnalysisTimelineToolRef{Name: "ReadSource", Version: 3},
				Summary: &WorkspaceAnalysisTimelineSummary{Kind: WorkspaceAnalysisTimelineSummarySource,
					Source: &WorkspaceAnalysisTimelineSourceSummary{EvidenceRef: "E1", ContentHash: strings.Repeat("b", 64)}}},
			{Sequence: 6, Kind: WorkspaceAnalysisTimelineItemModel, Phase: WorkspaceAnalysisPhaseSynthesizeAnswer,
				Status: WorkspaceAnalysisTimelineItemSucceeded, DurationMS: &durations[5],
				Summary: &WorkspaceAnalysisTimelineSummary{Kind: WorkspaceAnalysisTimelineSummaryModelUsage,
					ModelUsage: &WorkspaceAnalysisTimelineModelSummary{InputTokens: 200, OutputTokens: 500}}},
			{Sequence: 7, Kind: WorkspaceAnalysisTimelineItemTool, Phase: WorkspaceAnalysisPhaseValidateCitations,
				Status: WorkspaceAnalysisTimelineItemSucceeded, DurationMS: &durations[6],
				ToolRef: &WorkspaceAnalysisTimelineToolRef{Name: "ValidateCitation", Version: 3},
				Summary: &WorkspaceAnalysisTimelineSummary{Kind: WorkspaceAnalysisTimelineSummaryCitation,
					Citation: &WorkspaceAnalysisTimelineCitationSummary{ValidCount: 1, ReasonCodes: []string{"OK"}}}},
			{Sequence: 8, Kind: WorkspaceAnalysisTimelineItemModel, Phase: WorkspaceAnalysisPhaseReviewPublish,
				Status: WorkspaceAnalysisTimelineItemSucceeded, DurationMS: &durations[7],
				Summary: &WorkspaceAnalysisTimelineSummary{Kind: WorkspaceAnalysisTimelineSummaryModelUsage,
					ModelUsage: &WorkspaceAnalysisTimelineModelSummary{InputTokens: 300, OutputTokens: 100}}},
		},
		Budget: WorkspaceAnalysisTimelineBudget{
			ModelCalls:              WorkspaceAnalysisTimelineCounter{Used: 3, Max: agentdomain.WorkspaceAnalysisV1MaxModelCalls},
			ToolCalls:               WorkspaceAnalysisTimelineCounter{Used: 4, Max: agentdomain.WorkspaceAnalysisV1MaxToolCalls},
			SourceReads:             WorkspaceAnalysisTimelineCounter{Used: 1, Max: agentdomain.WorkspaceAnalysisV1MaxSourceReads},
			InputTokens:             WorkspaceAnalysisTimelineCounter{Used: 600, Max: agentdomain.WorkspaceAnalysisV1MaxRunInputTokens},
			OutputTokens:            WorkspaceAnalysisTimelineCounter{Used: 650, Max: agentdomain.WorkspaceAnalysisV1MaxRunOutputTokens},
			EstimatedCostMicrounits: &cost,
		},
		LatestServerEventSequence: 19,
	}
}

func emptyWorkspaceAnalysisTimeline() WorkspaceAnalysisTimeline {
	return WorkspaceAnalysisTimeline{
		SchemaID: WorkspaceAnalysisTimelineSchemaID, SchemaVersion: WorkspaceAnalysisTimelineSchemaVersionV1,
		WorkspaceID: workspaceAnalysisResultID(21), AnswerID: workspaceAnalysisResultID(22), AnalysisRunID: workspaceAnalysisResultID(23),
		RunStatus: WorkspaceAnalysisTimelineRunQueued, Items: []WorkspaceAnalysisTimelineItem{},
		Budget: WorkspaceAnalysisTimelineBudget{
			ModelCalls:   WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV1MaxModelCalls},
			ToolCalls:    WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV1MaxToolCalls},
			SourceReads:  WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV1MaxSourceReads},
			InputTokens:  WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV1MaxRunInputTokens},
			OutputTokens: WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV1MaxRunOutputTokens},
		},
	}
}

func mutateTimelineItem(value WorkspaceAnalysisTimelineItem, mutate func(*WorkspaceAnalysisTimelineItem)) WorkspaceAnalysisTimelineItem {
	if value.ToolRef != nil {
		copy := *value.ToolRef
		value.ToolRef = &copy
	}
	if value.Summary != nil {
		copy := *value.Summary
		value.Summary = &copy
		if copy.ModelUsage != nil {
			model := *copy.ModelUsage
			value.Summary.ModelUsage = &model
		}
	}
	mutate(&value)
	return value
}

func pointerToWorkspaceAnalysisString(value string) *string {
	return &value
}
