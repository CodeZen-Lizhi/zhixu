package eino

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestWorkspaceAnalysisLoopFollowsToolResultsAndReplays(t *testing.T) {
	for _, branch := range []string{"simple", "extended"} {
		t.Run(branch, func(t *testing.T) {
			model := &analysisBranchModel{}
			runtime, err := NewWorkspaceAnalysisLoopRuntime(model, nil)
			if err != nil {
				t.Fatal(err)
			}
			authority := &analysisLoopAuthority{calls: map[int]analysisLoopCall{}, tools: map[int]agentapplication.AgentToolResult{}}
			request := analysisLoopRequest(branch, authority)
			result, err := runtime.RunWorkspaceAnalysisLoop(context.Background(), request)
			if err != nil {
				t.Fatalf("loop failed: %v", err)
			}
			want := []string{"git_status", "knowledge_search", "source_read", "citation_validation"}
			if branch == "extended" {
				want = []string{"knowledge_search", "source_read", "knowledge_search", "source_read", "citation_validation"}
			}
			if !reflect.DeepEqual(authority.actions, want) || result.ToolCalls != len(want) || result.Decisions != len(want)+1 {
				t.Fatalf("actions=%v decisions=%d tools=%d", authority.actions, result.Decisions, result.ToolCalls)
			}
			if branch == "extended" {
				var transcript canonicalRuntimeRequestDocument
				if err := json.Unmarshal(authority.calls[4].request, &transcript); err != nil {
					t.Fatal(err)
				}
				if len(transcript.Messages) == 0 || !strings.Contains(transcript.Messages[len(transcript.Messages)-1].Content, `"E17"`) {
					t.Fatal("second search's actual source reference did not reach the next model decision")
				}
			}
			for _, call := range authority.calls {
				if bytes.Contains(call.request, []byte("native-call-")) || bytes.Contains(call.request, []byte("private-reasoning")) {
					t.Fatal("provider association or reasoning crossed the durable project port")
				}
			}
			providerCalls, toolCalls := model.calls, authority.externalTools
			replayed, err := runtime.RunWorkspaceAnalysisLoop(context.Background(), request)
			if err != nil {
				t.Fatalf("replay failed: %v", err)
			}
			if !replayed.Finish.Replayed || replayed.Decisions != result.Decisions || model.calls != providerCalls || authority.externalTools != toolCalls {
				t.Fatal("completed decision/tool replay performed an external call")
			}
		})
	}
}

func TestWorkspaceAnalysisLoopRejectsUntrustedToolRequestsBeforeExecution(t *testing.T) {
	tests := []struct {
		name, tool, arguments string
		mutate                func(*schema.Message)
	}{
		{name: "write", tool: "ApplyApprovedPatch", arguments: `{}`},
		{name: "unknown", tool: "RunShell", arguments: `{}`},
		{name: "workspace injection", tool: "SearchKnowledge", arguments: `{"query":"alpha","workspace_id":"other"}`},
		{name: "duplicate query", tool: "SearchKnowledge", arguments: `{"query":"alpha","query":"beta"}`},
		{name: "unbounded reference", tool: "ReadSource", arguments: `{"evidence_ref":"E33"}`},
		{name: "noncanonical reference", tool: "ReadSource", arguments: `{"evidence_ref":"E01"}`},
		{name: "unreturned reference", tool: "ReadSource", arguments: `{"evidence_ref":"E32"}`},
		{name: "unopened citation", tool: "ValidateCitation", arguments: `{"evidence_refs":["E1"]}`},
		{name: "citation identity injection", tool: "ValidateCitation", arguments: `{"evidence_refs":["E1"],"candidate_id":"made-up"}`},
		{name: "two tools", tool: "ReadGitStatus", arguments: `{}`, mutate: func(m *schema.Message) {
			m.ToolCalls = append(m.ToolCalls, schema.ToolCall{ID: "second", Type: "function", Function: schema.FunctionCall{Name: "ReadGitStatus", Arguments: `{}`}})
		}},
		{name: "empty provider ID", tool: "ReadGitStatus", arguments: `{}`, mutate: func(m *schema.Message) { m.ToolCalls[0].ID = "" }},
		{name: "missing usage", tool: "ReadGitStatus", arguments: `{}`, mutate: func(m *schema.Message) { m.ResponseMeta.Usage = nil }},
		{name: "unvalidated final answer", mutate: func(m *schema.Message) { m.ToolCalls = nil; m.Content = "The answer is definitely correct." }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := &scriptedRuntimeModel{generate: []func([]*schema.Message, ...einomodel.Option) (*schema.Message, error){func([]*schema.Message, ...einomodel.Option) (*schema.Message, error) {
				message := analysisNativeTool(test.tool, test.arguments, 1)
				if test.mutate != nil {
					test.mutate(message)
				}
				return message, nil
			}}}
			runtime, err := NewWorkspaceAnalysisLoopRuntime(model, nil)
			if err != nil {
				t.Fatal(err)
			}
			authority := &analysisLoopAuthority{calls: map[int]analysisLoopCall{}, tools: map[int]agentapplication.AgentToolResult{}}
			_, err = runtime.RunWorkspaceAnalysisLoop(context.Background(), analysisLoopRequest("invalid", authority))
			if err == nil || authority.externalTools != 0 || len(authority.calls) != 0 {
				t.Fatalf("invalid response reached a tool: error=%v tools=%d", err, authority.externalTools)
			}
		})
	}
}

func TestWorkspaceAnalysisLoopRejectsCitationUntilSourceIsRead(t *testing.T) {
	model := &scriptedRuntimeModel{generate: []func([]*schema.Message, ...einomodel.Option) (*schema.Message, error){
		func([]*schema.Message, ...einomodel.Option) (*schema.Message, error) {
			return analysisNativeTool("SearchKnowledge", `{"query":"alpha"}`, 1), nil
		},
		func([]*schema.Message, ...einomodel.Option) (*schema.Message, error) {
			return analysisNativeTool("ValidateCitation", `{"evidence_refs":["E1"]}`, 2), nil
		},
	}}
	runtime, err := NewWorkspaceAnalysisLoopRuntime(model, nil)
	if err != nil {
		t.Fatal(err)
	}
	authority := &analysisLoopAuthority{calls: map[int]analysisLoopCall{}, tools: map[int]agentapplication.AgentToolResult{}}
	_, err = runtime.RunWorkspaceAnalysisLoop(context.Background(), analysisLoopRequest("unopened", authority))
	if err == nil || len(authority.calls) != 1 || authority.externalTools != 1 || !reflect.DeepEqual(authority.actions, []string{agentdomain.WorkspaceAnalysisDecisionKnowledgeSearch}) {
		t.Fatalf("unread Search hit reached Citation: %v", err)
	}
}

func TestWorkspaceAnalysisLoopStopsAtDurableBudgetRefusal(t *testing.T) {
	model := &analysisBranchModel{repeat: true}
	runtime, err := NewWorkspaceAnalysisLoopRuntime(model, nil)
	if err != nil {
		t.Fatal(err)
	}
	authority := &analysisLoopAuthority{calls: map[int]analysisLoopCall{}, tools: map[int]agentapplication.AgentToolResult{}}
	_, err = runtime.RunWorkspaceAnalysisLoop(context.Background(), analysisLoopRequest("repeat", authority))
	if errorCode(err) != "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED" || model.calls != agentdomain.WorkspaceAnalysisV2MaxDecisions || authority.externalTools != agentdomain.WorkspaceAnalysisV2MaxDecisions {
		t.Fatalf("error=%v calls=%d tools=%d", err, model.calls, authority.externalTools)
	}
}

func TestWorkspaceAnalysisLoopRejectsCatalogDriftBeforeModel(t *testing.T) {
	model := &analysisBranchModel{}
	runtime, err := NewWorkspaceAnalysisLoopRuntime(model, nil)
	if err != nil {
		t.Fatal(err)
	}
	authority := &analysisLoopAuthority{calls: map[int]analysisLoopCall{}, tools: map[int]agentapplication.AgentToolResult{}}
	request := analysisLoopRequest("simple", authority)
	request.Tools[0].Ref.Version = 2
	_, err = runtime.RunWorkspaceAnalysisLoop(context.Background(), request)
	if err == nil || model.calls != 0 || len(authority.calls) != 0 {
		t.Fatal("catalog drift reached the model")
	}
}

// This fixture chooses from the actual transcript rather than a global request
// counter. The two questions follow different paths through the same ADK loop.
type analysisBranchModel struct {
	mu     sync.Mutex
	calls  int
	repeat bool
}

func (model *analysisBranchModel) Generate(_ context.Context, messages []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	model.mu.Lock()
	model.calls++
	model.mu.Unlock()
	options := einomodel.GetCommonOptions(nil, opts...)
	if len(options.Tools) != 4 || options.MaxTokens == nil || *options.MaxTokens != 512 {
		return nil, errors.New("fixture received a drifted tool catalog or output limit")
	}
	if model.repeat {
		return analysisNativeTool("ReadGitStatus", `{}`, len(messages)), nil
	}
	var last *schema.Message
	searches := 0
	refs := []string{}
	for _, message := range messages {
		if message.Role == schema.Tool {
			last = message
			if message.ToolName == "SearchKnowledge" {
				searches++
			}
			if message.ToolName == "ReadSource" {
				var output struct {
					EvidenceRef string `json:"evidence_ref"`
				}
				if json.Unmarshal([]byte(message.Content), &output) != nil {
					return nil, errors.New("invalid source result")
				}
				refs = append(refs, output.EvidenceRef)
			}
		}
	}
	if last == nil {
		if messages[1].Content == "simple" {
			return analysisNativeTool("ReadGitStatus", `{}`, len(messages)), nil
		}
		return analysisNativeTool("SearchKnowledge", `{"query":"alpha"}`, len(messages)), nil
	}
	switch last.ToolName {
	case "ReadGitStatus":
		return analysisNativeTool("SearchKnowledge", `{"query":"alpha"}`, len(messages)), nil
	case "SearchKnowledge":
		var output struct {
			Items []struct {
				EvidenceRef string `json:"evidence_ref"`
			} `json:"items"`
		}
		if json.Unmarshal([]byte(last.Content), &output) != nil || len(output.Items) != 1 {
			return nil, errors.New("invalid search result")
		}
		return analysisNativeTool("ReadSource", `{"evidence_ref":`+strconv.Quote(output.Items[0].EvidenceRef)+`}`, len(messages)), nil
	case "ReadSource":
		if messages[1].Content == "extended" && searches == 1 {
			return analysisNativeTool("SearchKnowledge", `{"query":"beta"}`, len(messages)), nil
		}
		arguments, _ := json.Marshal(struct {
			EvidenceRefs []string `json:"evidence_refs"`
		}{refs})
		return analysisNativeTool("ValidateCitation", string(arguments), len(messages)), nil
	case "ValidateCitation":
		document, _ := (agentdomain.WorkspaceAnalysisDecision{Action: agentdomain.WorkspaceAnalysisDecisionFinish}).Canonical()
		return &schema.Message{Role: schema.Assistant, Content: string(document), ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}}}, nil
	default:
		return nil, errors.New("unsupported preceding tool")
	}
}
func (*analysisBranchModel) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("fixture decision streaming is forbidden")
}
func (model *analysisBranchModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return model, nil
}

func analysisNativeTool(name, arguments string, sequence int) *schema.Message {
	return &schema.Message{Role: schema.Assistant, Content: "private-reasoning", ReasoningContent: "private-reasoning",
		ToolCalls:    []schema.ToolCall{{ID: "native-call-" + strconv.Itoa(sequence), Type: "function", Function: schema.FunctionCall{Name: name, Arguments: arguments}}},
		ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls", Usage: &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}}}
}

type analysisLoopCall struct {
	request []byte
	result  agentapplication.WorkspaceAnalysisDecisionMutationResult
}
type analysisLoopAuthority struct {
	calls         map[int]analysisLoopCall
	tools         map[int]agentapplication.AgentToolResult
	actions       []string
	externalTools int
}

func (authority *analysisLoopAuthority) CallWorkspaceAnalysisDecision(ctx context.Context, request agentapplication.WorkspaceAnalysisLoopModelCall) (agentapplication.WorkspaceAnalysisDecisionMutationResult, error) {
	if compose.GetToolCallID(ctx) != "" {
		return agentapplication.WorkspaceAnalysisDecisionMutationResult{}, errors.New("Eino context leaked into model authority")
	}
	if saved, found := authority.calls[request.Ordinal]; found {
		if !bytes.Equal(saved.request, request.RequestDocument) {
			return agentapplication.WorkspaceAnalysisDecisionMutationResult{}, errors.New("replay request differs")
		}
		saved.result.Replayed = true
		return saved.result, nil
	}
	if request.Ordinal > agentdomain.WorkspaceAnalysisV2MaxDecisions {
		return agentapplication.WorkspaceAnalysisDecisionMutationResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED", false, errors.New("durable decision budget exhausted"))
	}
	response, err := request.Invoke(ctx)
	if err != nil {
		return agentapplication.WorkspaceAnalysisDecisionMutationResult{}, err
	}
	document, err := response.Decision.Canonical()
	if err != nil {
		return agentapplication.WorkspaceAnalysisDecisionMutationResult{}, err
	}
	hash := sha256.Sum256(document)
	id := func(offset int) foundation.ID {
		return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", request.Ordinal*10+offset))
	}
	receipt := agentdomain.WorkspaceAnalysisDecisionReceipt{ID: id(1), WorkspaceID: id(2), AnalysisRunID: id(3), OperationID: id(4), NodeAttemptID: id(5), ModelRunID: id(6), ModelCallID: id(7), Ordinal: request.Ordinal, Decision: response.Decision, DocumentHash: hex.EncodeToString(hash[:]), DocumentBytes: int64(len(document)), CreatedAt: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)}
	result := agentapplication.WorkspaceAnalysisDecisionMutationResult{Decision: &receipt, Run: agentdomain.ModelRun{ID: receipt.ModelRunID}, Call: agentdomain.ModelCall{ID: receipt.ModelCallID, Status: agentdomain.ModelCallSucceeded}, Operation: agentdomain.WorkspaceAnalysisOperation{ID: receipt.OperationID}}
	authority.calls[request.Ordinal] = analysisLoopCall{request: append([]byte(nil), request.RequestDocument...), result: result}
	return result, nil
}

func (authority *analysisLoopAuthority) InvokeWorkspaceAnalysisDecisionTool(ctx context.Context, result agentapplication.WorkspaceAnalysisDecisionMutationResult) (agentapplication.AgentToolResult, error) {
	if compose.GetToolCallID(ctx) != "" || strings.Contains(fmt.Sprint(ctx), "decision-") {
		return agentapplication.AgentToolResult{}, errors.New("Eino association leaked into tool authority")
	}
	ordinal, decision := result.Decision.Ordinal, result.Decision.Decision
	if saved, found := authority.tools[ordinal]; found {
		return saved, nil
	}
	var output string
	switch decision.Action {
	case agentdomain.WorkspaceAnalysisDecisionGitStatus:
		output = `{"clean":true,"staged_count":0}`
	case agentdomain.WorkspaceAnalysisDecisionKnowledgeSearch:
		ref := "E1"
		if *decision.Query == "beta" {
			ref = "E17"
		}
		output = `{"items":[{"evidence_ref":"` + ref + `","snippet":"approved evidence"}]}`
	case agentdomain.WorkspaceAnalysisDecisionSourceRead:
		output = `{"evidence_ref":"` + *decision.EvidenceRef + `","excerpt":"actual immutable evidence","truncated":false}`
	case agentdomain.WorkspaceAnalysisDecisionCitationValidation:
		output = `{"results":[{"evidence_ref":"E1","valid":true,"reason_code":"OK"}]}`
	default:
		return agentapplication.AgentToolResult{}, errors.New("invalid fixture decision")
	}
	saved := agentapplication.AgentToolResult{Output: []byte(output)}
	authority.tools[ordinal] = saved
	authority.externalTools++
	authority.actions = append(authority.actions, decision.Action)
	return saved, nil
}

func analysisLoopRequest(branch string, authority *analysisLoopAuthority) agentapplication.WorkspaceAnalysisLoopRequest {
	tools := []agentapplication.AgentToolSpec{}
	for _, ref := range []toolsdomain.ToolRef{{Name: "ReadGitStatus", Version: 3}, {Name: "SearchKnowledge", Version: 3}, {Name: "ReadSource", Version: 4}, {Name: "ValidateCitation", Version: 4}} {
		tools = append(tools, agentapplication.AgentToolSpec{Ref: ref, Name: ref.Name, Description: "Approved read-only fixture tool.", InputSchema: []byte(`{"type":"object"}`)})
	}
	return agentapplication.WorkspaceAnalysisLoopRequest{Messages: []agentapplication.AgentMessage{{Role: agentapplication.AgentMessageSystem, Content: "Select an approved tool or finish."}, {Role: agentapplication.AgentMessageUser, Content: branch}}, Tools: tools, ModelCalls: authority, ToolInvoker: authority}
}
