package domain

import (
	"reflect"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

func TestWorkspaceAnalysisAnswerV2CanonicalizesBothGitBranches(t *testing.T) {
	t.Parallel()
	for _, withGit := range []bool{false, true} {
		result := validWorkspaceAnalysisAnswerResultV2()
		if withGit {
			gitStatus := validWorkspaceAnalysisAnswerResult().Payload.GitStatus
			result.Payload.GitStatus = &gitStatus
		}
		raw := mustJSON(t, result)
		canonical, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationCompleted, AnswerResultWorkspaceAnalysis, raw)
		if err != nil {
			t.Fatal(err)
		}
		projection, err := ProjectPublishedAnswer(AnswerResultWorkspaceAnalysis, canonical.Document)
		if err != nil || projection.Hash != canonical.Hash || !reflect.DeepEqual(projection.Citations, result.Payload.Citations) ||
			projection.ModelRunID != result.ModelRunRef || projection.AssistantText != result.Payload.AnswerMarkdown ||
			string(projection.Document) != string(raw) {
			t.Fatalf("v2 result did not preserve its canonical facts: err=%v", err)
		}
		if !withGit && !strings.Contains(string(raw), `"git_status":null`) {
			t.Fatal("uncalled Git must be explicit null")
		}
	}
}

func TestWorkspaceAnalysisAnswerV2RejectsVersionBudgetAndCitationDrift(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisAnswerResultV2)
	}{
		{"v1 relabel", func(value *WorkspaceAnalysisAnswerResultV2) {
			value.SchemaVersion = WorkspaceAnalysisResultSchemaVersionV1
		}},
		{"unknown version", func(value *WorkspaceAnalysisAnswerResultV2) { value.SchemaVersion = "v3" }},
		{"wrong result kind", func(value *WorkspaceAnalysisAnswerResultV2) { value.ResultType = AnswerResultRAGAnswer }},
		{"missing model run", func(value *WorkspaceAnalysisAnswerResultV2) { value.ModelRunRef = "" }},
		{"missing decision calls", func(value *WorkspaceAnalysisAnswerResultV2) { value.Payload.Budget.ModelCalls = 3 }},
		{"excess model calls", func(value *WorkspaceAnalysisAnswerResultV2) {
			value.Payload.Budget.ModelCalls = agentdomain.WorkspaceAnalysisV2MaxModelCalls + 1
		}},
		{"missing tool gate", func(value *WorkspaceAnalysisAnswerResultV2) { value.Payload.Budget.ToolCalls = 2 }},
		{"calls within caps but inconsistent", func(value *WorkspaceAnalysisAnswerResultV2) { value.Payload.Budget.ToolCalls = 4 }},
		{"input exceeds actual calls", func(value *WorkspaceAnalysisAnswerResultV2) {
			value.Payload.Budget.InputTokens = 6 * agentdomain.WorkspaceAnalysisV2MaxInputTokensPerModelCall
		}},
		{"output exceeds actual phases", func(value *WorkspaceAnalysisAnswerResultV2) { value.Payload.Budget.OutputTokens = 7000 }},
		{"excess tool calls", func(value *WorkspaceAnalysisAnswerResultV2) {
			value.Payload.Budget.ToolCalls = agentdomain.WorkspaceAnalysisV2MaxToolCalls + 1
		}},
		{"negative input", func(value *WorkspaceAnalysisAnswerResultV2) { value.Payload.Budget.InputTokens = -1 }},
		{"excess input", func(value *WorkspaceAnalysisAnswerResultV2) {
			value.Payload.Budget.InputTokens = agentdomain.WorkspaceAnalysisV2MaxRunInputTokens + 1
		}},
		{"excess output", func(value *WorkspaceAnalysisAnswerResultV2) {
			value.Payload.Budget.OutputTokens = agentdomain.WorkspaceAnalysisV2MaxRunOutputTokens + 1
		}},
		{"negative cost", func(value *WorkspaceAnalysisAnswerResultV2) {
			cost := int64(-1)
			value.Payload.Budget.EstimatedCostMicrounits = &cost
		}},
		{"no citation", func(value *WorkspaceAnalysisAnswerResultV2) { value.Payload.Citations = []agentdomain.Citation{} }},
		{"duplicate citation", func(value *WorkspaceAnalysisAnswerResultV2) {
			value.Payload.Citations = append(value.Payload.Citations, value.Payload.Citations[0])
		}},
		{"citation crosses workspace", func(value *WorkspaceAnalysisAnswerResultV2) {
			other := value.Payload.Citations[0]
			other.ID, other.WorkspaceID = "C2", workspaceAnalysisResultID(20)
			value.Payload.Citations = append(value.Payload.Citations, other)
		}},
		{"citation identity namespace", func(value *WorkspaceAnalysisAnswerResultV2) {
			value.Payload.Citations[0].ChunkID = value.Payload.Citations[0].WorkspaceID
		}},
		{"model reuses evidence identity", func(value *WorkspaceAnalysisAnswerResultV2) { value.ModelRunRef = value.Payload.Citations[0].ChunkID }},
		{"duplicate tuple with another public id", func(value *WorkspaceAnalysisAnswerResultV2) {
			other := value.Payload.Citations[0]
			other.ID = "C2"
			value.Payload.Citations = append(value.Payload.Citations, other)
		}},
		{"proposal unbound citation", func(value *WorkspaceAnalysisAnswerResultV2) {
			value.Payload.ProposalSuggestion.CitationIDs = []string{"C99"}
		}},
		{"proposal write URL", func(value *WorkspaceAnalysisAnswerResultV2) {
			value.Payload.ProposalSuggestion.Href = "/api/v1/proposals"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validWorkspaceAnalysisAnswerResultV2()
			test.mutate(&result)
			if _, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationCompleted, AnswerResultWorkspaceAnalysis, mustJSON(t, result)); err == nil {
				t.Fatal("corrupt v2 result accepted")
			}
		})
	}
}

func TestWorkspaceAnalysisAnswerV2RequiresStrictNullableDocuments(t *testing.T) {
	t.Parallel()
	result := validWorkspaceAnalysisAnswerResultV2()
	result.Payload.ProposalSuggestion = nil
	result.Payload.Budget.EstimatedCostMicrounits = nil
	raw := string(mustJSON(t, result))
	for _, field := range []string{`,"git_status":null`, `,"proposal_suggestion":null`, `,"estimated_cost_microunits":null`} {
		candidate := strings.Replace(raw, field, "", 1)
		if candidate == raw {
			t.Fatalf("fixture is missing %s", field)
		}
		if _, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationCompleted, AnswerResultWorkspaceAnalysis, []byte(candidate)); err == nil {
			t.Fatalf("accepted missing required-nullable %s", field)
		}
	}
	for _, candidate := range []string{
		strings.Replace(raw, `"schema_version":"v2"`, `"schema_version":"v2","schema_version":"v1"`, 1),
		strings.Replace(raw, `"git_status":null`, `"git_status":{"branch":"main"}`, 1),
		strings.Replace(raw, `"git_status":null`, `"git_status":false`, 1),
		strings.TrimSuffix(raw, "}") + `,"provider_request":"private"}`,
		strings.Replace(raw, `"model_calls":5`, `"model_calls":5,"prompt":"private"`, 1),
		raw + `{}`,
	} {
		if _, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationCompleted, AnswerResultWorkspaceAnalysis, []byte(candidate)); err == nil {
			t.Fatal("accepted noncanonical v2 document")
		}
	}
}

func TestWorkspaceAnalysisAnswerV2BundleBindsWorkspaceAndAuthoringRun(t *testing.T) {
	t.Parallel()
	result := validWorkspaceAnalysisAnswerResultV2()
	published, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationCompleted, AnswerResultWorkspaceAnalysis, mustJSON(t, result))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	answer := Answer{
		ID: workspaceAnalysisResultID(101), WorkspaceID: result.Payload.Citations[0].WorkspaceID,
		ConversationID: workspaceAnalysisResultID(102), QuestionID: workspaceAnalysisResultID(103), WorkflowRunID: workspaceAnalysisResultID(104),
		ModelRunID: published.ModelRunID, PublicationStatus: AnswerPublicationCompleted, ResultType: AnswerResultWorkspaceAnalysis,
		Result: published.Document, ResultHash: published.Hash, Version: 2, CreatedAt: now, UpdatedAt: now, PublishedAt: &now,
	}
	if err := ValidateAnswer(answer); err != nil {
		t.Fatal(err)
	}
	wrongWorkspace := answer
	wrongWorkspace.WorkspaceID = workspaceAnalysisResultID(105)
	if err := ValidateAnswer(wrongWorkspace); err == nil {
		t.Fatal("v2 bundle accepted citations from another workspace")
	}
	wrongAuthor := answer
	plannerRun := workspaceAnalysisResultID(106)
	wrongAuthor.ModelRunID = &plannerRun
	if err := ValidateAnswer(wrongAuthor); err == nil {
		t.Fatal("v2 bundle accepted a planner run in place of the candidate author")
	}
	reusedIdentity := answer
	reusedIdentity.ID = *answer.ModelRunID
	if err := ValidateAnswer(reusedIdentity); err == nil {
		t.Fatal("v2 bundle accepted Model Run identity in the Answer namespace")
	}
}

func TestWorkspaceAnalysisV2DoesNotUpgradeRefusalSchema(t *testing.T) {
	t.Parallel()
	refusal := WorkspaceAnalysisRefusalResult{
		ResultType: AnswerResultWorkspaceAnalysisRefusal, SchemaID: WorkspaceAnalysisRefusalSchemaID,
		SchemaVersion: WorkspaceAnalysisResultSchemaVersionV1,
		Payload:       WorkspaceAnalysisRefusalPayload{ReasonCode: WorkspaceAnalysisEvidenceInsufficient, Summary: "证据不足。"},
	}
	if _, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationRefused, refusal.ResultType, mustJSON(t, refusal)); err != nil {
		t.Fatal(err)
	}
	refusal.SchemaVersion = WorkspaceAnalysisResultSchemaVersionV2
	if _, err := CanonicalizeWorkspaceAnalysisPublishedResult(AnswerPublicationRefused, refusal.ResultType, mustJSON(t, refusal)); err == nil {
		t.Fatal("refusal wire schema was implicitly upgraded")
	}
}

func validWorkspaceAnalysisAnswerResultV2() WorkspaceAnalysisAnswerResultV2 {
	v1 := validWorkspaceAnalysisAnswerResult()
	return WorkspaceAnalysisAnswerResultV2{
		ResultType: AnswerResultWorkspaceAnalysis, SchemaID: WorkspaceAnalysisAnswerSchemaID,
		SchemaVersion: WorkspaceAnalysisResultSchemaVersionV2, ModelRunRef: v1.ModelRunRef,
		Payload: WorkspaceAnalysisAnswerPayloadV2{
			AnswerMarkdown: v1.Payload.AnswerMarkdown, Citations: v1.Payload.Citations,
			Budget:             WorkspaceAnalysisBudgetSummaryV2{ModelCalls: 5, ToolCalls: 3, InputTokens: 1024, OutputTokens: 512},
			ProposalSuggestion: v1.Payload.ProposalSuggestion, TerminationReason: WorkspaceAnalysisCompleted,
		},
	}
}
