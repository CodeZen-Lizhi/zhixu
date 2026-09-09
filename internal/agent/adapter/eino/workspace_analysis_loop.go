package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// WorkspaceAnalysisLoopRuntime keeps ADK's tool protocol transient. Before
// every provider call, the project caller authorizes or replays one durable
// decision; before every tool, the project invoker resolves that exact receipt.
type WorkspaceAnalysisLoopRuntime struct {
	model    einomodel.ToolCallingChatModel
	observer runtimeMetricObserver
}

func NewWorkspaceAnalysisLoopRuntime(model einomodel.ToolCallingChatModel, metrics observability.Metrics) (*WorkspaceAnalysisLoopRuntime, error) {
	if nilEinoChatModel(model) {
		return nil, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode,
			errors.New("workspace analysis Eino model is unavailable"))
	}
	return &WorkspaceAnalysisLoopRuntime{model: model, observer: newRuntimeMetricObserver(metrics)}, nil
}

func (runtime *WorkspaceAnalysisLoopRuntime) RunWorkspaceAnalysisLoop(ctx context.Context, request agentapplication.WorkspaceAnalysisLoopRequest) (result agentapplication.WorkspaceAnalysisLoopResult, runErr error) {
	if runtime == nil || nilEinoChatModel(runtime.model) {
		return result, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode,
			errors.New("workspace analysis Eino loop is unavailable"))
	}
	if err := agentapplication.ValidateWorkspaceAnalysisLoopRequest(request); err != nil {
		return result, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	state := &workspaceAnalysisLoopState{providerIDs: map[string]struct{}{}, searchedRefs: map[string]bool{}, readRefs: map[string]bool{}}
	defer func() {
		state.mu.Lock()
		decisions, toolCalls := state.decisions, state.toolCalls
		state.mu.Unlock()
		runtime.observer.observeAgent(ctx, decisions, toolCalls, runErr)
	}()
	tools := make([]einotool.BaseTool, len(request.Tools))
	for index, spec := range request.Tools {
		tools[index] = &workspaceAnalysisLoopTool{spec: cloneAgentToolSpec(spec), invoker: request.ToolInvoker, state: state, projectContext: runContext}
	}
	model := &workspaceAnalysisLoopModel{backend: runtime.model, caller: request.ModelCalls, state: state, projectContext: runContext}
	agent, err := adk.NewChatModelAgent(runContext, &adk.ChatModelAgentConfig{
		Name: "zhixu-workspace-analysis-v2", Description: "Collects evidence using the project-approved read-only tools.",
		Model:       model,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true}},
		// The last cycle can only obtain a durable budget refusal. It cannot
		// issue another provider request beyond the project's twelve decisions.
		MaxIterations:    agentdomain.WorkspaceAnalysisV2MaxDecisions + 1,
		ModelRetryConfig: nil, ModelFailoverConfig: nil,
	})
	if err != nil {
		return result, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode,
			errors.New("workspace analysis Eino loop could not be constructed"))
	}
	iterator := agent.Run(runContext, &adk.AgentInput{Messages: projectAgentMessages(request.Messages), EnableStreaming: false})
	finalText, err := consumeAgentEvents(iterator, false, cancel)
	if err != nil {
		return result, err
	}
	decision, err := agentdomain.DecodeWorkspaceAnalysisDecision([]byte(finalText))
	state.mu.Lock()
	defer state.mu.Unlock()
	if err != nil || decision.Action != agentdomain.WorkspaceAnalysisDecisionFinish || state.last == nil ||
		state.last.Decision == nil || state.last.Decision.Decision.Action != agentdomain.WorkspaceAnalysisDecisionFinish ||
		state.pending || state.decisions < 1 || state.decisions > agentdomain.WorkspaceAnalysisV2MaxDecisions {
		return result, workspaceAnalysisLoopOutputError("workspace analysis ended without a durable finish decision")
	}
	return agentapplication.WorkspaceAnalysisLoopResult{Finish: *state.last, Decisions: state.decisions, ToolCalls: state.toolCalls}, nil
}

type workspaceAnalysisLoopState struct {
	mu           sync.Mutex
	decisions    int
	toolCalls    int
	pending      bool
	modelActive  bool
	toolActive   bool
	last         *agentapplication.WorkspaceAnalysisDecisionMutationResult
	providerIDs  map[string]struct{}
	searchedRefs map[string]bool
	readRefs     map[string]bool
}

type workspaceAnalysisLoopModel struct {
	backend        einomodel.ToolCallingChatModel
	caller         agentapplication.WorkspaceAnalysisLoopModelCaller
	state          *workspaceAnalysisLoopState
	projectContext context.Context
	tools          []*schema.ToolInfo
}

func (model *workspaceAnalysisLoopModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	if model == nil || nilEinoChatModel(model.backend) {
		return nil, workspaceAnalysisLoopOutputError("workspace analysis model binding is unavailable")
	}
	if _, _, err := projectRuntimeTools(tools); err != nil {
		return nil, err
	}
	backend, err := model.backend.WithTools(tools)
	if err != nil {
		return nil, mapEinoRuntimeError(err)
	}
	return &workspaceAnalysisLoopModel{backend: backend, caller: model.caller, state: model.state,
		projectContext: model.projectContext, tools: append([]*schema.ToolInfo(nil), tools...)}, nil
}

func (*workspaceAnalysisLoopModel) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, workspaceAnalysisLoopOutputError("workspace analysis decisions cannot enter the answer stream")
}

func (model *workspaceAnalysisLoopModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	model.state.mu.Lock()
	if model.state.pending || model.state.modelActive || model.state.toolActive ||
		model.state.last != nil && model.state.last.Decision.Decision.Action == agentdomain.WorkspaceAnalysisDecisionFinish {
		model.state.mu.Unlock()
		return nil, workspaceAnalysisLoopOutputError("workspace analysis model call is out of order")
	}
	ordinal := model.state.decisions + 1
	model.state.modelActive = true
	model.state.mu.Unlock()
	defer func() { model.state.mu.Lock(); model.state.modelActive = false; model.state.mu.Unlock() }()

	callOptions := append([]einomodel.Option(nil), opts...)
	if len(einomodel.GetCommonOptions(nil, callOptions...).Tools) == 0 && len(model.tools) > 0 {
		callOptions = append(callOptions, einomodel.WithTools(model.tools))
	}
	callOptions = append(callOptions, einomodel.WithMaxTokens(int(agentdomain.WorkspaceAnalysisV2DecisionMaxOutputTokens)), einomodel.WithTemperature(0))
	canonical, err := canonicalRuntimeRequest(input, callOptions)
	if err != nil {
		return nil, err
	}
	projectContext, release := newProjectToolInvocationContext(ctx, model.projectContext)
	defer release()
	result, err := model.caller.CallWorkspaceAnalysisDecision(projectContext, agentapplication.WorkspaceAnalysisLoopModelCall{
		Ordinal: ordinal, RequestDocument: canonical,
		Invoke: func(callContext context.Context) (agentapplication.WorkspaceAnalysisDecisionProviderResult, error) {
			response, invokeErr := model.backend.Generate(callContext, input, callOptions...)
			usage, known := runtimeMessageUsage(response)
			projection := agentapplication.WorkspaceAnalysisDecisionProviderResult{Usage: usage}
			if invokeErr != nil {
				return projection, mapEinoRuntimeError(invokeErr)
			}
			if !known {
				return projection, einoRuntimeError(foundation.ErrorManualRecoveryRequired, agentapplication.ErrorCodeModelCallPersistenceUnknown,
					errors.New("workspace analysis model omitted valid token usage"))
			}
			projection.Decision, invokeErr = model.decodeDecision(response)
			if invokeErr == nil {
				invokeErr = model.state.validateDecisionEvidence(projection.Decision)
			}
			return projection, invokeErr
		},
	})
	if err != nil {
		return nil, err
	}
	if result.Decision == nil || result.Decision.Validate() != nil || result.Decision.Ordinal != ordinal ||
		result.Decision.OperationID != result.Operation.ID || result.Decision.ModelRunID != result.Run.ID ||
		result.Decision.ModelCallID != result.Call.ID || result.Call.Status != agentdomain.ModelCallSucceeded {
		return nil, workspaceAnalysisLoopOutputError("workspace analysis decision receipt is invalid")
	}
	if err := model.state.validateDecisionEvidence(result.Decision.Decision); err != nil {
		return nil, err
	}
	message, err := workspaceAnalysisDecisionMessage(result.Decision.Decision, ordinal)
	if err != nil {
		return nil, err
	}
	model.state.mu.Lock()
	model.state.decisions = ordinal
	model.state.last = &result
	model.state.pending = result.Decision.Decision.Action != agentdomain.WorkspaceAnalysisDecisionFinish
	model.state.mu.Unlock()
	return message, nil
}

func (model *workspaceAnalysisLoopModel) decodeDecision(message *schema.Message) (agentdomain.WorkspaceAnalysisDecision, error) {
	if message == nil || message.Role != schema.Assistant || message.ResponseMeta == nil || len(message.ToolCalls) > 1 ||
		message.ToolCallID != "" || message.ToolName != "" || len(message.MultiContent) != 0 ||
		len(message.UserInputMultiContent) != 0 || len(message.AssistantGenMultiContent) != 0 ||
		!utf8.ValidString(message.Content) || !utf8.ValidString(message.ReasoningContent) ||
		len(message.Content)+len(message.ReasoningContent) > agentapplication.MaxAgentMessageBytes ||
		!supportedRuntimeMessageExtra(message) {
		return agentdomain.WorkspaceAnalysisDecision{}, workspaceAnalysisLoopOutputError("workspace analysis model returned an invalid decision")
	}
	if len(message.ToolCalls) == 0 {
		if message.ResponseMeta.FinishReason != "stop" {
			return agentdomain.WorkspaceAnalysisDecision{}, workspaceAnalysisLoopOutputError("workspace analysis finish is not complete")
		}
		decision, err := agentdomain.DecodeWorkspaceAnalysisDecision([]byte(message.Content))
		if err != nil || decision.Action != agentdomain.WorkspaceAnalysisDecisionFinish {
			return agentdomain.WorkspaceAnalysisDecision{}, workspaceAnalysisLoopOutputError("workspace analysis model did not select a tool or finish")
		}
		return decision, nil
	}
	call := message.ToolCalls[0]
	if message.ResponseMeta.FinishReason != "tool_calls" || !validProviderToolCallID(call.ID) || call.Type != "function" {
		return agentdomain.WorkspaceAnalysisDecision{}, workspaceAnalysisLoopOutputError("workspace analysis tool association is invalid")
	}
	model.state.mu.Lock()
	_, duplicate := model.state.providerIDs[call.ID]
	model.state.providerIDs[call.ID] = struct{}{}
	model.state.mu.Unlock()
	if duplicate {
		return agentdomain.WorkspaceAnalysisDecision{}, workspaceAnalysisLoopOutputError("workspace analysis model reused a tool association")
	}
	return decodeWorkspaceAnalysisDecisionTool(call.Function.Name, []byte(call.Function.Arguments))
}

func decodeWorkspaceAnalysisDecisionTool(name string, arguments []byte) (agentdomain.WorkspaceAnalysisDecision, error) {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes, limits.MaxStringBytes, limits.MaxObjectFields, limits.MaxArrayItems, limits.MaxDepth = 4096, 1024, 1, agentdomain.WorkspaceAnalysisV2MaxSourceReads, 3
	decision := agentdomain.WorkspaceAnalysisDecision{}
	switch name {
	case "ReadGitStatus":
		if _, err := strictjson.DecodeObject[struct{}](arguments, limits, nil); err != nil {
			return decision, workspaceAnalysisLoopOutputError("workspace analysis Git arguments are invalid")
		}
		decision.Action = agentdomain.WorkspaceAnalysisDecisionGitStatus
	case "SearchKnowledge":
		input, err := strictjson.DecodeObject[struct {
			Query string `json:"query"`
		}](arguments, limits, nil)
		if err != nil {
			return decision, workspaceAnalysisLoopOutputError("workspace analysis search arguments are invalid")
		}
		decision.Action, decision.Query = agentdomain.WorkspaceAnalysisDecisionKnowledgeSearch, &input.Query
	case "ReadSource":
		input, err := strictjson.DecodeObject[struct {
			EvidenceRef string `json:"evidence_ref"`
		}](arguments, limits, nil)
		if err != nil {
			return decision, workspaceAnalysisLoopOutputError("workspace analysis source arguments are invalid")
		}
		decision.Action, decision.EvidenceRef = agentdomain.WorkspaceAnalysisDecisionSourceRead, &input.EvidenceRef
	case "ValidateCitation":
		input, err := strictjson.DecodeObject[struct {
			EvidenceRefs []string `json:"evidence_refs"`
		}](arguments, limits, nil)
		if err != nil {
			return decision, workspaceAnalysisLoopOutputError("workspace analysis citation arguments are invalid")
		}
		decision.Action, decision.EvidenceRefs = agentdomain.WorkspaceAnalysisDecisionCitationValidation, input.EvidenceRefs
	default:
		return decision, workspaceAnalysisLoopOutputError("workspace analysis model selected a tool outside the read-only catalog")
	}
	if err := decision.Validate(); err != nil {
		return agentdomain.WorkspaceAnalysisDecision{}, err
	}
	return decision, nil
}

func workspaceAnalysisDecisionMessage(decision agentdomain.WorkspaceAnalysisDecision, ordinal int) (*schema.Message, error) {
	if err := decision.Validate(); err != nil {
		return nil, err
	}
	if decision.Action == agentdomain.WorkspaceAnalysisDecisionFinish {
		document, err := decision.Canonical()
		return schema.AssistantMessage(string(document), nil), err
	}
	var name string
	var input any
	switch decision.Action {
	case agentdomain.WorkspaceAnalysisDecisionGitStatus:
		name, input = "ReadGitStatus", struct{}{}
	case agentdomain.WorkspaceAnalysisDecisionKnowledgeSearch:
		name, input = "SearchKnowledge", struct {
			Query string `json:"query"`
		}{Query: *decision.Query}
	case agentdomain.WorkspaceAnalysisDecisionSourceRead:
		name, input = "ReadSource", struct {
			EvidenceRef string `json:"evidence_ref"`
		}{EvidenceRef: *decision.EvidenceRef}
	case agentdomain.WorkspaceAnalysisDecisionCitationValidation:
		name, input = "ValidateCitation", struct {
			EvidenceRefs []string `json:"evidence_refs"`
		}{EvidenceRefs: append([]string(nil), decision.EvidenceRefs...)}
	}
	arguments, err := json.Marshal(input)
	if err != nil {
		return nil, workspaceAnalysisLoopOutputError("workspace analysis tool projection cannot be encoded")
	}
	// This association exists only in this ADK run. Provider IDs and reasoning
	// are intentionally discarded before the next model request is recorded.
	return schema.AssistantMessage("", []schema.ToolCall{{ID: "decision-" + strconv.Itoa(ordinal), Type: "function",
		Function: schema.FunctionCall{Name: name, Arguments: string(arguments)}}}), nil
}

type workspaceAnalysisLoopTool struct {
	spec           agentapplication.AgentToolSpec
	invoker        agentapplication.WorkspaceAnalysisLoopToolInvoker
	state          *workspaceAnalysisLoopState
	projectContext context.Context
}

func (tool *workspaceAnalysisLoopTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return (&executionTool{spec: tool.spec}).Info(ctx)
}

func (tool *workspaceAnalysisLoopTool) InvokableRun(ctx context.Context, raw string, _ ...einotool.Option) (string, error) {
	decision, err := decodeWorkspaceAnalysisDecisionTool(tool.spec.Name, []byte(raw))
	if err != nil {
		return "", err
	}
	tool.state.mu.Lock()
	if !tool.state.pending || tool.state.modelActive || tool.state.toolActive || tool.state.last == nil ||
		compose.GetToolCallID(ctx) != "decision-"+strconv.Itoa(tool.state.decisions) {
		tool.state.mu.Unlock()
		return "", workspaceAnalysisLoopOutputError("workspace analysis tool does not bind the accepted decision")
	}
	accepted := *tool.state.last
	expected, expectedErr := accepted.Decision.Decision.Canonical()
	actual, actualErr := decision.Canonical()
	if expectedErr != nil || actualErr != nil || !bytes.Equal(expected, actual) {
		tool.state.mu.Unlock()
		return "", workspaceAnalysisLoopOutputError("workspace analysis tool arguments differ from the accepted decision")
	}
	tool.state.toolActive = true
	tool.state.mu.Unlock()
	defer func() { tool.state.mu.Lock(); tool.state.toolActive = false; tool.state.mu.Unlock() }()
	projectContext, release := newProjectToolInvocationContext(ctx, tool.projectContext)
	defer release()
	result, err := tool.invoker.InvokeWorkspaceAnalysisDecisionTool(projectContext, accepted)
	if err != nil {
		return "", err
	}
	if len(result.Output) == 0 || len(result.Output) > agentapplication.MaxAgentMessageBytes || !utf8.Valid(result.Output) || !json.Valid(result.Output) {
		return "", workspaceAnalysisLoopOutputError("workspace analysis tool result is not bounded JSON")
	}
	if err := tool.state.observeEvidence(decision, result.Output); err != nil {
		return "", err
	}
	tool.state.mu.Lock()
	tool.state.pending = false
	tool.state.toolCalls++
	tool.state.mu.Unlock()
	return string(result.Output), nil
}

// These transient sets are rebuilt from the project's receipt projections on
// every replay. They reject invented references before accepting a model result;
// the tool repository still owns the authoritative tuple and access checks.
func (state *workspaceAnalysisLoopState) validateDecisionEvidence(decision agentdomain.WorkspaceAnalysisDecision) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	switch decision.Action {
	case agentdomain.WorkspaceAnalysisDecisionSourceRead:
		if decision.EvidenceRef == nil || !state.searchedRefs[*decision.EvidenceRef] {
			return workspaceAnalysisLoopOutputError("workspace analysis model selected an unreturned source reference")
		}
	case agentdomain.WorkspaceAnalysisDecisionCitationValidation:
		for _, ref := range decision.EvidenceRefs {
			if !state.readRefs[ref] {
				return workspaceAnalysisLoopOutputError("workspace analysis model selected an unopened citation reference")
			}
		}
	}
	return nil
}

func (state *workspaceAnalysisLoopState) observeEvidence(decision agentdomain.WorkspaceAnalysisDecision, raw []byte) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	switch decision.Action {
	case agentdomain.WorkspaceAnalysisDecisionKnowledgeSearch:
		var projection struct {
			Items []struct {
				EvidenceRef string `json:"evidence_ref"`
			} `json:"items"`
		}
		if json.Unmarshal(raw, &projection) != nil || projection.Items == nil || len(projection.Items) > 5 {
			return workspaceAnalysisLoopOutputError("workspace analysis Search reference projection is invalid")
		}
		for _, item := range projection.Items {
			if !toolsdomain.ValidDynamicEvidenceRef(item.EvidenceRef) || state.searchedRefs[item.EvidenceRef] {
				return workspaceAnalysisLoopOutputError("workspace analysis Search reference projection was rebound")
			}
			state.searchedRefs[item.EvidenceRef] = true
		}
	case agentdomain.WorkspaceAnalysisDecisionSourceRead:
		var projection struct {
			EvidenceRef string `json:"evidence_ref"`
		}
		if json.Unmarshal(raw, &projection) != nil || decision.EvidenceRef == nil || projection.EvidenceRef != *decision.EvidenceRef || !state.searchedRefs[projection.EvidenceRef] {
			return workspaceAnalysisLoopOutputError("workspace analysis Read reference projection drifted")
		}
		state.readRefs[projection.EvidenceRef] = true
	}
	return nil
}

func workspaceAnalysisLoopOutputError(message string) error {
	return einoRuntimeError(foundation.ErrorConsistencyViolation, agentdomain.ErrorCodeWorkspaceAnalysisDecisionInvalid, errors.New(message))
}

var _ agentapplication.WorkspaceAnalysisLoopRuntime = (*WorkspaceAnalysisLoopRuntime)(nil)
var _ einomodel.ToolCallingChatModel = (*workspaceAnalysisLoopModel)(nil)
var _ einotool.InvokableTool = (*workspaceAnalysisLoopTool)(nil)
