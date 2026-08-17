package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisPublicationMatrixIsExact(t *testing.T) {
	tests := []struct {
		status      AnswerPublicationStatus
		resultType  AnswerResultType
		requirement WorkspaceAnalysisModelRunRequirement
	}{
		{AnswerPublicationCompleted, AnswerResultWorkspaceAnalysis, WorkspaceAnalysisModelRunRequired},
		{AnswerPublicationRefused, AnswerResultWorkspaceAnalysisRefusal, WorkspaceAnalysisModelRunConditional},
		{AnswerPublicationClarificationRequired, AnswerResultClarification, WorkspaceAnalysisModelRunRequired},
		{WorkspaceAnalysisPublicationFailed, AnswerResultWorkspaceAnalysisTermination, WorkspaceAnalysisModelRunOptional},
		{WorkspaceAnalysisPublicationCancelled, AnswerResultWorkspaceAnalysisTermination, WorkspaceAnalysisModelRunOptional},
	}
	for _, test := range tests {
		rule, err := WorkspaceAnalysisRuleForPublication(test.status)
		if err != nil || rule.Status != test.status || rule.ResultType != test.resultType || rule.ModelRunRequirement != test.requirement {
			t.Fatalf("status %q rule=%#v err=%v", test.status, rule, err)
		}
	}
	for _, status := range []AnswerPublicationStatus{"", AnswerPublicationPending, "timed_out"} {
		if _, err := WorkspaceAnalysisRuleForPublication(status); err == nil {
			t.Fatalf("status %q unexpectedly accepted", status)
		}
	}
}

func TestWorkspaceAnalysisCompletedResultCanonicalizesSafeFacts(t *testing.T) {
	result := validWorkspaceAnalysisAnswerResult()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationCompleted, AnswerResultWorkspaceAnalysis, raw)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.ModelRunID == nil || *canonical.ModelRunID != result.ModelRunRef || len(canonical.Hash) != 64 || string(canonical.Document) != string(raw) {
		t.Fatalf("canonical=%#v raw=%s", canonical, raw)
	}
	if canonical.Hash != "b0176a43baf1b7bae2bfbd5e640f4d0f46ef0db1f963ed02865834a987db6d00" {
		t.Fatalf("workspace analysis answer golden hash=%s", canonical.Hash)
	}
	replay, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationCompleted, AnswerResultWorkspaceAnalysis, canonical.Document)
	if err != nil || canonical.Type != replay.Type || !reflect.DeepEqual(canonical.ModelRunID, replay.ModelRunID) ||
		string(canonical.Document) != string(replay.Document) || canonical.Hash != replay.Hash {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	for _, forbidden := range []string{"path", "porcelain", "prompt", "receipt", "server_binding"} {
		if strings.Contains(string(canonical.Document), forbidden) {
			t.Fatalf("completed document leaked forbidden field %q: %s", forbidden, canonical.Document)
		}
	}
}

func TestWorkspaceAnalysisCompletedResultRequiresExplicitNullableFields(t *testing.T) {
	result := validWorkspaceAnalysisAnswerResult()
	result.Payload.Budget.EstimatedCostMicrounits = nil
	result.Payload.ProposalSuggestion = nil
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"estimated_cost_microunits":null`) || !strings.Contains(string(raw), `"proposal_suggestion":null`) {
		t.Fatalf("required nullable fields are absent: %s", raw)
	}
	if _, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationCompleted, AnswerResultWorkspaceAnalysis, raw); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`,"estimated_cost_microunits":null`, `,"proposal_suggestion":null`} {
		withoutField := strings.Replace(string(raw), field, "", 1)
		if withoutField == string(raw) {
			t.Fatalf("fixture does not contain %s: %s", field, raw)
		}
		if _, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationCompleted, AnswerResultWorkspaceAnalysis, []byte(withoutField)); err == nil {
			t.Fatalf("missing required nullable field accepted: %s", withoutField)
		}
	}
}

func TestWorkspaceAnalysisCompletedResultRejectsUntrustedOrInconsistentFacts(t *testing.T) {
	valid := validWorkspaceAnalysisAnswerResult()
	validRaw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisAnswerResult)
		raw    string
	}{
		{name: "wrong result type", mutate: func(value *WorkspaceAnalysisAnswerResult) { value.ResultType = AnswerResultRAGAnswer }},
		{name: "missing model", mutate: func(value *WorkspaceAnalysisAnswerResult) { value.ModelRunRef = "" }},
		{name: "dirty mismatch", mutate: func(value *WorkspaceAnalysisAnswerResult) { value.Payload.GitStatus.Clean = true }},
		{name: "uppercase head", mutate: func(value *WorkspaceAnalysisAnswerResult) { value.Payload.GitStatus.Head = strings.Repeat("A", 40) }},
		{name: "too many model calls", mutate: func(value *WorkspaceAnalysisAnswerResult) { value.Payload.Budget.ModelCalls = 4 }},
		{name: "unknown proposal citation", mutate: func(value *WorkspaceAnalysisAnswerResult) {
			value.Payload.ProposalSuggestion.CitationIDs = []string{"C99"}
		}},
		{name: "wrong proposal href", mutate: func(value *WorkspaceAnalysisAnswerResult) {
			value.Payload.ProposalSuggestion.Href = "/api/v1/proposals/create"
		}},
		{name: "unknown field", raw: strings.TrimSuffix(string(validRaw), "}") + `,"workspace_path":"/private/repo"}`},
		{name: "duplicate field", raw: strings.Replace(string(validRaw), `"schema_version":"v1"`, `"schema_version":"v1","schema_version":"v1"`, 1)},
		{name: "trailing document", raw: string(validRaw) + `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(test.raw)
			if test.mutate != nil {
				candidate := validWorkspaceAnalysisAnswerResult()
				test.mutate(&candidate)
				raw, err = json.Marshal(candidate)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationCompleted, AnswerResultWorkspaceAnalysis, raw); err == nil {
				t.Fatalf("document unexpectedly accepted: %s", raw)
			}
		})
	}
	if _, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationRefused, AnswerResultWorkspaceAnalysis, validRaw); err == nil {
		t.Fatal("completed result accepted under refused status")
	}
}

func TestWorkspaceAnalysisGitStatusFixturesCoverCleanAndDirtyAggregates(t *testing.T) {
	tests := []struct {
		name   string
		status WorkspaceAnalysisGitStatus
	}{
		{name: "clean", status: WorkspaceAnalysisGitStatus{Branch: "main", Head: strings.Repeat("a", 40), Clean: true}},
		{name: "dirty", status: WorkspaceAnalysisGitStatus{
			Branch: "dev", Head: strings.Repeat("b", 40), StagedCount: 1, UnstagedCount: 2, UntrackedCount: 3, ConflictCount: 4,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.status.Validate(); err != nil {
				t.Fatalf("Git aggregate fixture rejected: %#v: %v", test.status, err)
			}
		})
	}
}

func TestWorkspaceAnalysisRefusalModelRunRuleIsReasonBound(t *testing.T) {
	modelRunID := workspaceAnalysisResultID(1)
	tests := []struct {
		name      string
		reason    WorkspaceAnalysisRefusalReason
		modelRun  *foundation.ID
		wantError bool
	}{
		{name: "deterministic", reason: WorkspaceAnalysisEvidenceInsufficient},
		{name: "model authored", reason: WorkspaceAnalysisModelRefused, modelRun: &modelRunID},
		{name: "deterministic claims model", reason: WorkspaceAnalysisCitationInvalid, modelRun: &modelRunID, wantError: true},
		{name: "model authored missing model", reason: WorkspaceAnalysisModelRefused, wantError: true},
		{name: "unknown reason", reason: "WORKSPACE_ANALYSIS_OTHER", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := WorkspaceAnalysisRefusalResult{
				ResultType: AnswerResultWorkspaceAnalysisRefusal, SchemaID: WorkspaceAnalysisRefusalSchemaID,
				SchemaVersion: WorkspaceAnalysisResultSchemaVersionV1, ModelRunRef: test.modelRun,
				Payload: WorkspaceAnalysisRefusalPayload{ReasonCode: test.reason, Summary: "当前证据不足，无法安全回答。"},
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationRefused, AnswerResultWorkspaceAnalysisRefusal, raw)
			if test.wantError {
				if err == nil {
					t.Fatalf("result unexpectedly accepted: %s", raw)
				}
				return
			}
			if err != nil || (test.modelRun == nil) != (canonical.ModelRunID == nil) {
				t.Fatalf("canonical=%#v err=%v", canonical, err)
			}
		})
	}
	missingModelField := `{"result_type":"workspace_analysis_refusal","schema_id":"conversation.workspace_analysis_refusal","schema_version":"v1","payload":{"reason_code":"WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT","summary":"证据不足"}}`
	if _, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationRefused, AnswerResultWorkspaceAnalysisRefusal, []byte(missingModelField)); err == nil {
		t.Fatal("refusal omitted required nullable model_run_ref field")
	}
}

func TestWorkspaceAnalysisTerminationSeparatesFailureAndCancellation(t *testing.T) {
	modelRunID := workspaceAnalysisResultID(1)
	for _, test := range []struct {
		status    AnswerPublicationStatus
		reason    WorkspaceAnalysisTerminationReason
		modelRun  *foundation.ID
		wantError bool
	}{
		{status: WorkspaceAnalysisPublicationFailed, reason: WorkspaceAnalysisResultUnknown},
		{status: WorkspaceAnalysisPublicationFailed, reason: WorkspaceAnalysisModelFailed, modelRun: &modelRunID},
		{status: WorkspaceAnalysisPublicationFailed, reason: WorkspaceAnalysisRuntimeFailed},
		{status: WorkspaceAnalysisPublicationFailed, reason: WorkspaceAnalysisRuntimeFailed, modelRun: &modelRunID, wantError: true},
		{status: WorkspaceAnalysisPublicationCancelled, reason: WorkspaceAnalysisCancelled},
		{status: WorkspaceAnalysisPublicationFailed, reason: WorkspaceAnalysisCancelled, wantError: true},
		{status: WorkspaceAnalysisPublicationCancelled, reason: WorkspaceAnalysisBudgetExhausted, wantError: true},
	} {
		result := WorkspaceAnalysisTerminationResult{
			ResultType: AnswerResultWorkspaceAnalysisTermination, SchemaID: WorkspaceAnalysisTerminationSchemaID,
			SchemaVersion: WorkspaceAnalysisResultSchemaVersionV1, ModelRunRef: test.modelRun,
			Payload: WorkspaceAnalysisTerminationPayload{TerminationReason: test.reason, Summary: "分析已安全终止。"},
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := CanonicalizeWorkspaceAnalysisPublishedResult(test.status, AnswerResultWorkspaceAnalysisTermination, raw)
		if test.wantError != (err != nil) {
			t.Fatalf("status=%q reason=%q err=%v", test.status, test.reason, err)
		}
		if !test.wantError && test.reason == WorkspaceAnalysisRuntimeFailed && canonical.ModelRunID != nil {
			t.Fatalf("runtime failure model run = %s, want nil", *canonical.ModelRunID)
		}
		if !test.wantError && test.reason == WorkspaceAnalysisRuntimeFailed && string(canonical.Document) != string(raw) {
			t.Fatalf("runtime failure document changed during canonicalization: %s", canonical.Document)
		}
	}
}

func TestWorkspaceAnalysisTerminalDocumentsHaveStableGoldenHashes(t *testing.T) {
	modelRunID := workspaceAnalysisResultID(1)
	tests := []struct {
		name       string
		status     AnswerPublicationStatus
		resultType AnswerResultType
		document   any
		wantHash   string
	}{
		{
			name: "deterministic refusal", status: AnswerPublicationRefused, resultType: AnswerResultWorkspaceAnalysisRefusal,
			document: WorkspaceAnalysisRefusalResult{
				ResultType: AnswerResultWorkspaceAnalysisRefusal, SchemaID: WorkspaceAnalysisRefusalSchemaID,
				SchemaVersion: WorkspaceAnalysisResultSchemaVersionV1,
				Payload:       WorkspaceAnalysisRefusalPayload{ReasonCode: WorkspaceAnalysisEvidenceInsufficient, Summary: "当前证据不足，无法安全回答。"},
			},
			wantHash: "1f868de82c9b16937e59530fd1cd36cdd7093d55f98d6987411723dad5ecacb6",
		},
		{
			name: "clarification", status: AnswerPublicationClarificationRequired, resultType: AnswerResultClarification,
			document: ClarificationResult{
				ResultType: string(AnswerResultClarification), SchemaID: ClarificationSchemaID, SchemaVersion: ClarificationSchemaVersionV1,
				ModelRunRef: modelRunID,
				Payload:     ClarificationPayload{Reason: "缺少分析范围。", Question: "需要分析哪个模块？", SuggestedScopes: []string{"internal/agent"}},
			},
			wantHash: "a47c283ec4284bbff29b3f37d030986a1714e0d026dd733601ea88dac138c106",
		},
		{
			name: "unknown failure", status: WorkspaceAnalysisPublicationFailed, resultType: AnswerResultWorkspaceAnalysisTermination,
			document: WorkspaceAnalysisTerminationResult{
				ResultType: AnswerResultWorkspaceAnalysisTermination, SchemaID: WorkspaceAnalysisTerminationSchemaID,
				SchemaVersion: WorkspaceAnalysisResultSchemaVersionV1,
				Payload:       WorkspaceAnalysisTerminationPayload{TerminationReason: WorkspaceAnalysisResultUnknown, Summary: "调用结果无法安全确认。"},
			},
			wantHash: "e4ef7f2f0323a47b8a54e4d78c9c929841cd940aaefb937471ea70e736311939",
		},
		{
			name: "cancelled", status: WorkspaceAnalysisPublicationCancelled, resultType: AnswerResultWorkspaceAnalysisTermination,
			document: WorkspaceAnalysisTerminationResult{
				ResultType: AnswerResultWorkspaceAnalysisTermination, SchemaID: WorkspaceAnalysisTerminationSchemaID,
				SchemaVersion: WorkspaceAnalysisResultSchemaVersionV1,
				Payload:       WorkspaceAnalysisTerminationPayload{TerminationReason: WorkspaceAnalysisCancelled, Summary: "分析已取消。"},
			},
			wantHash: "5ef134a7a040af2800ea5eb530d2744a4cb54ace53ea52331fd9bc8724e8f47d",
		},
		{
			name: "budget exhausted", status: WorkspaceAnalysisPublicationFailed, resultType: AnswerResultWorkspaceAnalysisTermination,
			document: WorkspaceAnalysisTerminationResult{
				ResultType: AnswerResultWorkspaceAnalysisTermination, SchemaID: WorkspaceAnalysisTerminationSchemaID,
				SchemaVersion: WorkspaceAnalysisResultSchemaVersionV1,
				Payload:       WorkspaceAnalysisTerminationPayload{TerminationReason: WorkspaceAnalysisBudgetExhausted, Summary: "分析预算已耗尽。"},
			},
			wantHash: "ed1a43a81da290113b0ad539e6e59043b2d2a216feac25eb4f207b875ea0eb95",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.document)
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := CanonicalizeWorkspaceAnalysisPublishedResult(test.status, test.resultType, raw)
			if err != nil {
				t.Fatal(err)
			}
			if canonical.Hash != test.wantHash || string(canonical.Document) != string(raw) {
				t.Fatalf("hash=%s document=%s", canonical.Hash, canonical.Document)
			}
		})
	}
}

func TestWorkspaceAnalysisTerminationRequiresNullableModelRunField(t *testing.T) {
	missingModelField := `{"result_type":"workspace_analysis_termination","schema_id":"conversation.workspace_analysis_termination","schema_version":"v1","payload":{"termination_reason":"WORKSPACE_ANALYSIS_RESULT_UNKNOWN","summary":"结果未知"}}`
	if _, err := CanonicalizeWorkspaceAnalysisPublishedResult(WorkspaceAnalysisPublicationFailed, AnswerResultWorkspaceAnalysisTermination, []byte(missingModelField)); err == nil {
		t.Fatal("termination omitted required nullable model_run_ref field")
	}
}

func validWorkspaceAnalysisAnswerResult() WorkspaceAnalysisAnswerResult {
	cost := int64(125)
	return WorkspaceAnalysisAnswerResult{
		ResultType: AnswerResultWorkspaceAnalysis, SchemaID: WorkspaceAnalysisAnswerSchemaID,
		SchemaVersion: WorkspaceAnalysisResultSchemaVersionV1, ModelRunRef: workspaceAnalysisResultID(1),
		Payload: WorkspaceAnalysisAnswerPayload{
			AnswerMarkdown: "运行时通过持久化 lease 恢复。[C1]",
			Citations: []agentdomain.Citation{{
				ID: "C1", WorkspaceID: workspaceAnalysisResultID(2), IndexVersionID: workspaceAnalysisResultID(3),
				ChunkID: workspaceAnalysisResultID(4), SourceVersionID: workspaceAnalysisResultID(5), SourceSpanID: workspaceAnalysisResultID(6),
			}},
			GitStatus: WorkspaceAnalysisGitStatus{
				Branch: "dev", Head: strings.Repeat("a", 40), StagedCount: 1,
			},
			Budget: WorkspaceAnalysisBudgetSummary{
				ModelCalls: 3, ToolCalls: 4, InputTokens: 1_024, OutputTokens: 512, EstimatedCostMicrounits: &cost,
			},
			ProposalSuggestion: &WorkspaceAnalysisProposalSuggestion{
				Summary: "可在 Proposal 工作台评估这项修改。", CitationIDs: []string{"C1"}, Href: WorkspaceAnalysisProposalHref,
			},
			TerminationReason: WorkspaceAnalysisCompleted,
		},
	}
}

func workspaceAnalysisResultID(value int) foundation.ID {
	return foundation.ID("76000000-0000-4000-8000-" + workspaceAnalysisLeftPad12(value))
}

func workspaceAnalysisLeftPad12(value int) string {
	const zeros = "000000000000"
	raw := ""
	for value > 0 {
		raw = string(rune('0'+value%10)) + raw
		value /= 10
	}
	return zeros[:len(zeros)-len(raw)] + raw
}
