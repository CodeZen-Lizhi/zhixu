package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestFixtureWorkspaceAnalysisV2UsesProductionADKLoopAndActualToolResults(t *testing.T) {
	for _, branch := range []string{"simple", "extended"} {
		t.Run(branch, func(t *testing.T) {
			server, rejection := newProductionFixtureServer(t)
			model, err := platformmodels.NewEinoRuntimeChatModel(platformmodels.OpenAIChatOptions{Client: server.Client(), BaseURL: server.URL, APIKey: fixtureAPIKey,
				Model: "rag-smoke", ModelVersion: "rag-smoke-v1", AdapterVersion: "fixture-v1", Timeout: 5 * time.Second, MaxRequestBytes: maxRequestBytes, MaxResponseBytes: maxRequestBytes})
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := agenteino.NewWorkspaceAnalysisLoopRuntime(model, nil)
			if err != nil {
				t.Fatal(err)
			}
			input, _ := json.Marshal(map[string]any{"schema_version": 2, "untrusted_data": true, "question": branch + " approved recovery", "history": []any{}, "answer_depth": "brief", "output_format": "markdown"})
			authority := &fixtureWorkspaceAnalysisV2Ports{}
			result, err := runtime.RunWorkspaceAnalysisLoop(context.Background(), agentapplication.WorkspaceAnalysisLoopRequest{
				Messages: []agentapplication.AgentMessage{{Role: agentapplication.AgentMessageSystem, Content: "Select an approved tool or finish."}, {Role: agentapplication.AgentMessageUser, Content: string(input)}},
				Tools:    fixtureWorkspaceAnalysisV2ToolSpecs(), ModelCalls: authority, ToolInvoker: authority})
			if err != nil {
				t.Fatalf("production v2 loop failed: %v; fixture=%s", err, rejection())
			}
			want := []string{"git_status", "knowledge_search", "source_read", "citation_validation"}
			if branch == "extended" {
				want = []string{"knowledge_search", "source_read", "knowledge_search", "source_read", "citation_validation"}
			}
			if !reflect.DeepEqual(authority.actions, want) || result.Decisions != len(want)+1 {
				t.Fatalf("actions=%v decisions=%d", authority.actions, result.Decisions)
			}
			if branch == "extended" && !reflect.DeepEqual(authority.reads, []string{"E1", "E17"}) {
				t.Fatal("second Search result did not control the next source read")
			}
		})
	}
}

func TestFixtureWorkspaceAnalysisV2CandidateStreamsGlobalReference(t *testing.T) {
	server, rejection := newProductionFixtureServer(t)
	model, err := platformmodels.NewEinoRuntimeChatModel(platformmodels.OpenAIChatOptions{Client: server.Client(), BaseURL: server.URL, APIKey: fixtureAPIKey,
		Model: "rag-smoke", ModelVersion: "rag-smoke-v1", AdapterVersion: "fixture-v1", Timeout: 5 * time.Second, MaxRequestBytes: maxRequestBytes, MaxResponseBytes: maxRequestBytes})
	if err != nil {
		t.Fatal(err)
	}
	modelRef := agentdomain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "fixture-v1", ModelID: "rag-smoke", ModelVersion: "rag-smoke-v1"}
	catalog, err := agentworkflow.NewRuntimeCatalog(agentworkflow.CatalogOptions{Model: modelRef, Timeout: 5 * time.Second, MaxOutputTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	ref := agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: "2"}
	snapshot, err := catalog.Snapshot(agentworkflow.WorkspaceAnalysisSynthesisPromptRefV2(), ref, ref, agentworkflow.DefaultProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	input := fixtureWorkspaceAnalysisV2CandidateInput()
	raw, _ := json.Marshal(input)
	runtime, err := agenteino.NewWorkspaceAnalysisCandidateStreamRuntime(model)
	if err != nil {
		t.Fatal(err)
	}
	response, err := runtime.Stream(context.Background(), agentapplication.ChatRequest{Phase: agentdomain.ModelCallAnswer, ProfileRef: snapshot.Profile.Ref, PromptRef: snapshot.Prompt.Ref,
		SchemaRef: ref, Model: modelRef, Messages: []agentapplication.ChatMessage{{Role: agentapplication.MessageRoleSystem, Content: snapshot.Prompt.System}, {Role: agentapplication.MessageRoleUser, Content: string(raw)}},
		OutputSchema: snapshot.Schema.JSONSchema, MaxOutputTokens: 4096}, &fixtureCandidateSink{})
	if err != nil {
		t.Fatalf("v2 candidate stream: %v; fixture=%s", err, rejection())
	}
	output, err := agentdomain.DecodeWorkspaceAnalysisCandidateProvider(response.Content, agentdomain.DefaultDecodeLimits())
	if err != nil || output.SchemaVersion != "2" || !reflect.DeepEqual(output.Payload.CitationRefs, []string{"E17"}) {
		t.Fatalf("v2 candidate reference drift: %v", err)
	}
}

func TestFixtureWorkspaceAnalysisV2RejectsMixedCandidateVersions(t *testing.T) {
	server, _ := newProductionFixtureServer(t)
	_, catalog := newProductionStructuredRuntime(t, server)
	for _, version := range []string{"1", "2"} {
		t.Run(version, func(t *testing.T) {
			prompt := agentworkflow.WorkspaceAnalysisSynthesisPromptRef()
			if version == "2" {
				prompt = agentworkflow.WorkspaceAnalysisSynthesisPromptRefV2()
			}
			ref := agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: version}
			snapshot, err := catalog.Snapshot(prompt, ref, ref, agentworkflow.DefaultProfileRef())
			if err != nil {
				t.Fatal(err)
			}
			input := fixtureWorkspaceAnalysisV2CandidateInput()
			if version == "2" {
				input = fixtureWorkspaceAnalysisSynthesisInput()
			}
			var request chatRequest
			if err := json.Unmarshal(fixtureWorkspaceAnalysisCandidateStreamRequest(t, snapshot.Schema.JSONSchema, input), &request); err != nil {
				t.Fatal(err)
			}
			if version == "2" {
				request.ResponseFormat.JSONSchema.Name = workspaceAnalysisCandidateResponseSchemaNameV2
			}
			raw, _ := json.Marshal(request)
			if status := fixtureStatus(t, newHandler("fixture-model-v2", fixtureAPIKey), raw); status != http.StatusUnprocessableEntity {
				t.Fatalf("mismatched candidate input/schema accepted: %d", status)
			}
		})
	}
}

func TestFixtureWorkspaceAnalysisV2CandidateRejectsMalformedEvidenceWithoutWritingStream(t *testing.T) {
	for _, value := range []any{nil, "invalid", []any{}, []any{map[string]any{"evidence_ref": "E17", "excerpt": 1, "truncated": false}}} {
		input := fixtureWorkspaceAnalysisV2CandidateInput()
		input["evidence"] = value
		raw, _ := json.Marshal(input)
		content, _ := json.Marshal(string(raw))
		response := httptest.NewRecorder()
		err := serveWorkspaceAnalysisCandidateStreamV2(response, chatRequest{Messages: []message{{Role: "user", Content: content}}}, "fixture-v2", []string{"E17"})
		if err == nil || response.Body.Len() != 0 {
			t.Fatal("invalid evidence started a candidate stream")
		}
	}
}

func TestFixtureWorkspaceAnalysisV2UsesStableDecisionStage(t *testing.T) {
	for _, question := range []string{"simple", "extended", "budget-loop"} {
		raw, _ := json.Marshal(map[string]any{"schema_version": 2, "untrusted_data": true, "question": question, "history": []any{}, "answer_depth": "brief", "output_format": "markdown"})
		content, _ := json.Marshal(string(raw))
		want := "workspace_analysis_decision"
		if question == "budget-loop" {
			want = "workspace_analysis_budget_decision"
		}
		if stage := fixtureRequestStage(chatRequest{Messages: []message{{Role: "user", Content: content}}}); stage != want {
			t.Fatalf("dynamic request stage=%s", stage)
		}
	}
}

func fixtureWorkspaceAnalysisV2CandidateInput() map[string]any {
	return map[string]any{"schema_version": 2, "untrusted_data": true, "question": "approved recovery", "history": []any{}, "answer_depth": "brief", "output_format": "markdown",
		"git_status": nil, "searches": []any{map[string]any{"effective_mode": "KEYWORD", "hit_count": 1, "degradation_codes": []any{}}},
		"evidence": []any{map[string]any{"evidence_ref": "E17", "excerpt": "Replay avoids duplicate work.", "truncated": false}}}
}

func fixtureWorkspaceAnalysisV2ToolSpecs() []agentapplication.AgentToolSpec {
	var result []agentapplication.AgentToolSpec
	for _, definition := range []struct {
		name    string
		version int64
		schema  string
	}{
		{"ReadGitStatus", 3, `{"type":"object","additionalProperties":false,"required":[],"properties":{}}`},
		{"SearchKnowledge", 3, `{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string","minLength":1,"maxLength":1024}}}`},
		{"ReadSource", 4, `{"type":"object","additionalProperties":false,"required":["evidence_ref"],"properties":{"evidence_ref":{"type":"string"}}}`},
		{"ValidateCitation", 4, `{"type":"object","additionalProperties":false,"required":["evidence_refs"],"properties":{"evidence_refs":{"type":"array","items":{"type":"string"}}}}`},
	} {
		result = append(result, agentapplication.AgentToolSpec{Ref: toolsdomain.ToolRef{Name: definition.name, Version: definition.version}, Name: definition.name, Description: "Approved read-only fixture tool.", InputSchema: []byte(definition.schema)})
	}
	return result
}

type fixtureWorkspaceAnalysisV2Ports struct {
	actions, reads []string
	searches       int
}

func (*fixtureWorkspaceAnalysisV2Ports) CallWorkspaceAnalysisDecision(ctx context.Context, request agentapplication.WorkspaceAnalysisLoopModelCall) (agentapplication.WorkspaceAnalysisDecisionMutationResult, error) {
	response, err := request.Invoke(ctx)
	if err != nil {
		return agentapplication.WorkspaceAnalysisDecisionMutationResult{}, err
	}
	raw, err := response.Decision.Canonical()
	if err != nil {
		return agentapplication.WorkspaceAnalysisDecisionMutationResult{}, err
	}
	id := func(n int) foundation.ID {
		return foundation.ID(fmt.Sprintf("60000000-0000-4000-8000-%012d", request.Ordinal*10+n))
	}
	digest := sha256.Sum256(raw)
	receipt := agentdomain.WorkspaceAnalysisDecisionReceipt{ID: id(1), WorkspaceID: id(2), AnalysisRunID: id(3), OperationID: id(4), NodeAttemptID: id(5), ModelRunID: id(6), ModelCallID: id(7),
		Ordinal: request.Ordinal, Decision: response.Decision, DocumentHash: hex.EncodeToString(digest[:]), DocumentBytes: int64(len(raw)), CreatedAt: time.Now().UTC()}
	return agentapplication.WorkspaceAnalysisDecisionMutationResult{Decision: &receipt, Operation: agentdomain.WorkspaceAnalysisOperation{ID: receipt.OperationID}, Run: agentdomain.ModelRun{ID: receipt.ModelRunID}, Call: agentdomain.ModelCall{ID: receipt.ModelCallID, Status: agentdomain.ModelCallSucceeded}}, nil
}

func (ports *fixtureWorkspaceAnalysisV2Ports) InvokeWorkspaceAnalysisDecisionTool(_ context.Context, result agentapplication.WorkspaceAnalysisDecisionMutationResult) (agentapplication.AgentToolResult, error) {
	decision := result.Decision.Decision
	ports.actions = append(ports.actions, decision.Action)
	var output any
	switch decision.Action {
	case agentdomain.WorkspaceAnalysisDecisionGitStatus:
		output = map[string]any{"clean": true, "staged_count": 0, "unstaged_count": 0, "untracked_count": 0, "conflict_count": 0}
	case agentdomain.WorkspaceAnalysisDecisionKnowledgeSearch:
		ports.searches++
		ref := "E1"
		if ports.searches > 1 {
			ref = "E17"
		}
		output = map[string]any{"effective_mode": "KEYWORD", "hit_count": 1, "degradation_codes": []string{}, "items": []any{map[string]any{"evidence_ref": ref, "snippet": "approved evidence"}}}
	case agentdomain.WorkspaceAnalysisDecisionSourceRead:
		ports.reads = append(ports.reads, *decision.EvidenceRef)
		output = map[string]any{"evidence_ref": *decision.EvidenceRef, "excerpt": "Approved evidence " + strconv.Itoa(len(ports.reads)), "truncated": false}
	case agentdomain.WorkspaceAnalysisDecisionCitationValidation:
		items := []any{}
		for _, ref := range decision.EvidenceRefs {
			items = append(items, map[string]any{"evidence_ref": ref, "valid": true, "reason_code": "OK"})
		}
		output = map[string]any{"results": items}
	}
	raw, err := json.Marshal(output)
	return agentapplication.AgentToolResult{Output: raw}, err
}
