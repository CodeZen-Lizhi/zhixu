package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

const (
	agentName                  = "zhixu-rag-read-agent"
	agentDescription           = "Uses only the project-approved read-only evidence tools."
	agentToolReason            = "RAG Agent requested an approved read-only tool."
	agentToolSchemaCode        = "AGENT_EINO_TOOL_SCHEMA_INVALID"
	agentToolInvocationCode    = "AGENT_EINO_TOOL_INVOCATION_INVALID"
	agentIteratorOutputCode    = "AGENT_EINO_EVENT_OUTPUT_INVALID"
	agentUnexpectedActionCode  = "AGENT_EINO_ACTION_UNSUPPORTED"
	maxProviderToolCallIDBytes = 256
)

// AgentRuntime 是经典 schema.Message ChatModelAgent 的生产适配器。领域
// Workflow 只看到 application Port；ReAct transcript 与 provider call id
// 始终留在本次 Eino 运行内。
type AgentRuntime struct {
	model    einomodel.ToolCallingChatModel
	observer runtimeMetricObserver
}

type agentRunState struct {
	mu                sync.Mutex
	toolCalls         int
	providerToolCalls map[string]providerToolCallExecution
}

type providerToolCallExecution struct {
	toolName string
	callNo   int
}

type providerToolCallContextBinding struct {
	id       string
	toolName string
}

type providerToolCallAssociation struct {
	toolName string
	consumed bool
}

type providerToolCallContextKey struct{}

// NewAgentRuntime 构造 Eino ChatModelAgent runtime；模型 retry/failover 在
// 每次 Run 的配置中固定关闭。
func NewAgentRuntime(model einomodel.ToolCallingChatModel) (*AgentRuntime, error) {
	return NewAgentRuntimeWithMetrics(model, nil)
}

// NewAgentRuntimeWithMetrics 构造带发布观察指标的 Eino Agent runtime。
func NewAgentRuntimeWithMetrics(model einomodel.ToolCallingChatModel, metrics observability.Metrics) (*AgentRuntime, error) {
	if nilEinoChatModel(model) {
		return nil, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode, errors.New("eino agent model is unavailable"))
	}
	return &AgentRuntime{model: model, observer: newRuntimeMetricObserver(metrics)}, nil
}

// Run 执行一个有界、顺序只读的 ReAct loop，并完整消费 ADK iterator。
func (runtime *AgentRuntime) Run(ctx context.Context, request agentapplication.AgentRunRequest) (result agentapplication.AgentRunResult, runErr error) {
	if runtime == nil || nilEinoChatModel(runtime.model) {
		return agentapplication.AgentRunResult{}, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode, errors.New("eino agent runtime is unavailable"))
	}
	if err := agentapplication.ValidateAgentRunRequest(request); err != nil {
		return agentapplication.AgentRunResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runContext, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	recordingModel, err := newRecordingRuntimeModel(runtime.model, runtimeCallBinding{
		model: request.Model, profile: request.Profile, prompt: request.Prompt, schema: request.Schema,
		maxInputTokens: request.MaxInputTokens, maxOutputTokens: request.MaxOutputTokens,
		phase: agentdomain.ModelCallAgent, budget: request.Budget, recorder: request.Recorder,
	})
	if err != nil {
		return agentapplication.AgentRunResult{}, err
	}
	runState := &agentRunState{}
	iterations := 0
	toolCalls := 0
	defer func() { runtime.observer.observeAgent(ctx, iterations, toolCalls, runErr) }()
	tools, err := buildAgentTools(ctx, request.Tools, request.ToolInvoker, request.Budget, runState)
	if err != nil {
		return agentapplication.AgentRunResult{}, err
	}
	returnDirectly := make(map[string]bool, len(request.ReturnDirectlyTools))
	for _, name := range request.ReturnDirectlyTools {
		returnDirectly[name] = true
	}
	agent, err := adk.NewChatModelAgent(runContext, &adk.ChatModelAgentConfig{
		Name: agentName, Description: agentDescription, Model: recordingModel,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: tools, ExecuteSequentially: true,
			ToolCallMiddlewares: []compose.ToolMiddleware{newProviderToolCallMiddleware(runState)},
		}, ReturnDirectly: returnDirectly},
		MaxIterations: request.MaxIterations,
		// Workflow owns retry and failover facts. ADK must issue exactly one
		// provider request for each ModelCall authorization.
		ModelRetryConfig: nil, ModelFailoverConfig: nil,
	})
	if err != nil {
		return agentapplication.AgentRunResult{}, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode, errors.New("eino chat model agent could not be constructed"))
	}
	iterator := agent.Run(runContext, &adk.AgentInput{Messages: projectAgentMessages(request.Messages), EnableStreaming: false})
	finalText, consumeErr := consumeAgentEvents(iterator, len(returnDirectly) > 0, cancelRun)
	calls, usage, usageComplete := recordingModel.state.snapshot()
	iterations = calls
	toolCalls = runState.count()
	if consumeErr != nil {
		return agentapplication.AgentRunResult{}, consumeErr
	}
	if !usageComplete {
		usage = agentdomain.TokenUsage{}
	}
	result = agentapplication.AgentRunResult{FinalText: finalText, Iterations: calls, ToolCalls: toolCalls, Usage: usage}
	if strings.TrimSpace(result.FinalText) == "" || len(result.FinalText) > agentapplication.MaxAgentResultBytes || !utf8.ValidString(result.FinalText) ||
		result.Iterations < 1 || result.Iterations > request.MaxIterations || result.ToolCalls < 0 || result.Usage.Validate() != nil {
		return agentapplication.AgentRunResult{}, einoRuntimeError(foundation.ErrorConsistencyViolation, agentapplication.ErrorCodeAgentRuntimeOutputInvalid, errors.New("eino agent returned an invalid bounded result"))
	}
	return result, nil
}

type executionTool struct {
	spec           agentapplication.AgentToolSpec
	invoker        agentapplication.AgentToolInvoker
	budget         *agentapplication.RunBudgetLedger
	state          *agentRunState
	projectContext context.Context
}

func buildAgentTools(
	projectContext context.Context,
	specs []agentapplication.AgentToolSpec,
	invoker agentapplication.AgentToolInvoker,
	budget *agentapplication.RunBudgetLedger,
	state *agentRunState,
) ([]einotool.BaseTool, error) {
	if len(specs) == 0 {
		return []einotool.BaseTool{}, nil
	}
	if projectContext == nil || invoker == nil || budget == nil || state == nil {
		return nil, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode, errors.New("eino tool bridge is unavailable"))
	}
	tools := make([]einotool.BaseTool, len(specs))
	for index := range specs {
		candidate := &executionTool{
			spec: cloneAgentToolSpec(specs[index]), invoker: invoker, budget: budget, state: state,
			projectContext: projectContext,
		}
		if _, err := candidate.Info(context.Background()); err != nil {
			return nil, err
		}
		tools[index] = candidate
	}
	return tools, nil
}

func (tool *executionTool) Info(context.Context) (*schema.ToolInfo, error) {
	if tool == nil || tool.spec.Ref.Validate() != nil || tool.spec.Name != tool.spec.Ref.Name ||
		strings.TrimSpace(tool.spec.Description) == "" || len(tool.spec.InputSchema) == 0 {
		return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentToolSchemaCode, errors.New("eino tool specification is invalid"))
	}
	var document jsonschema.Schema
	decoder := json.NewDecoder(bytes.NewReader(tool.spec.InputSchema))
	if err := decoder.Decode(&document); err != nil {
		return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentToolSchemaCode, errors.New("eino tool json schema could not be decoded"))
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentToolSchemaCode, errors.New("eino tool json schema contains trailing data"))
	}
	params := schema.NewParamsOneOfByJSONSchema(&document)
	if _, err := params.ToJSONSchema(); err != nil {
		return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentToolSchemaCode, errors.New("eino tool json schema is unsupported"))
	}
	return &schema.ToolInfo{Name: tool.spec.Name, Desc: tool.spec.Description, ParamsOneOf: params}, nil
}

func (tool *executionTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...einotool.Option) (string, error) {
	if tool == nil || tool.invoker == nil || tool.budget == nil || tool.state == nil || tool.projectContext == nil {
		return "", einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode, errors.New("eino tool invocation bridge is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	arguments := []byte(argumentsInJSON)
	trimmed := bytes.TrimSpace(arguments)
	if len(arguments) == 0 || len(arguments) > toolsdomain.MaxToolDocumentBytes || !utf8.Valid(arguments) ||
		len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' || !json.Valid(trimmed) {
		return "", einoRuntimeError(foundation.ErrorInvalidInput, agentToolInvocationCode, errors.New("eino tool arguments are not one bounded json object"))
	}
	binding, ok := providerToolCallBindingFromContext(ctx)
	if !ok || binding.toolName != tool.spec.Name || compose.GetToolCallID(ctx) != binding.id {
		return "", einoRuntimeError(foundation.ErrorConsistencyViolation, agentToolInvocationCode, errors.New("eino tool invocation is missing its provider call association"))
	}
	if err := tool.budget.AuthorizeToolCall(); err != nil {
		return "", err
	}
	callNo, err := tool.state.bindProjectToolCall(binding)
	if err != nil {
		return "", err
	}
	projectContext, releaseProjectContext := newProjectToolInvocationContext(ctx, tool.projectContext)
	defer releaseProjectContext()
	result, err := tool.invoker.Invoke(projectContext, agentapplication.AgentToolInvocation{
		Ref: tool.spec.Ref, Arguments: append([]byte(nil), trimmed...), Reason: agentToolReason, CallNo: callNo,
	})
	if err != nil {
		return "", err
	}
	if len(result.Output) == 0 || len(result.Output) > agentapplication.MaxAgentResultBytes || !utf8.Valid(result.Output) || !json.Valid(result.Output) {
		return "", einoRuntimeError(foundation.ErrorConsistencyViolation, agentToolInvocationCode, errors.New("project tool returned an invalid bounded json result"))
	}
	return string(result.Output), nil
}

func newProjectToolInvocationContext(executionContext, projectContext context.Context) (context.Context, context.CancelFunc) {
	cleanContext := projectContext
	releaseDeadline := context.CancelFunc(func() {})
	if deadline, ok := executionContext.Deadline(); ok {
		cleanContext, releaseDeadline = context.WithDeadline(projectContext, deadline)
	}
	cleanContext, cancel := context.WithCancel(cleanContext)
	stopCancellationPropagation := context.AfterFunc(executionContext, cancel)
	if executionContext.Err() != nil {
		cancel()
	}
	return cleanContext, func() {
		stopCancellationPropagation()
		cancel()
		releaseDeadline()
	}
}

func newProviderToolCallMiddleware(state *agentRunState) compose.ToolMiddleware {
	return compose.ToolMiddleware{Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			if next == nil || state == nil || input == nil || !validProviderToolCallID(input.CallID) || !validProviderToolName(input.Name) {
				return nil, einoRuntimeError(foundation.ErrorConsistencyViolation, agentToolInvocationCode, errors.New("eino tool invocation has an invalid provider call association"))
			}
			binding := providerToolCallContextBinding{id: input.CallID, toolName: input.Name}
			if err := state.registerProviderToolCall(binding); err != nil {
				return nil, err
			}
			if ctx == nil {
				ctx = context.Background()
			}
			return next(context.WithValue(ctx, providerToolCallContextKey{}, binding), input)
		}
	}}
}

func (state *agentRunState) registerProviderToolCall(binding providerToolCallContextBinding) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.providerToolCalls == nil {
		state.providerToolCalls = make(map[string]providerToolCallExecution)
	}
	if _, exists := state.providerToolCalls[binding.id]; exists {
		return einoRuntimeError(foundation.ErrorConsistencyViolation, agentToolInvocationCode, errors.New("eino tool invocation reused a provider call id"))
	}
	state.providerToolCalls[binding.id] = providerToolCallExecution{toolName: binding.toolName}
	return nil
}

func (state *agentRunState) bindProjectToolCall(binding providerToolCallContextBinding) (int, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	association, exists := state.providerToolCalls[binding.id]
	if !exists || association.toolName != binding.toolName || association.callNo != 0 {
		return 0, einoRuntimeError(foundation.ErrorConsistencyViolation, agentToolInvocationCode, errors.New("eino tool invocation provider association is invalid"))
	}
	state.toolCalls++
	association.callNo = state.toolCalls
	state.providerToolCalls[binding.id] = association
	return association.callNo, nil
}

func (state *agentRunState) count() int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.toolCalls
}

func providerToolCallBindingFromContext(ctx context.Context) (providerToolCallContextBinding, bool) {
	if ctx == nil {
		return providerToolCallContextBinding{}, false
	}
	binding, ok := ctx.Value(providerToolCallContextKey{}).(providerToolCallContextBinding)
	return binding, ok && validProviderToolCallID(binding.id) && validProviderToolName(binding.toolName)
}

func consumeAgentEvents(
	iterator *adk.AsyncIterator[*adk.AgentEvent],
	allowDirectToolResult bool,
	cancelOnError context.CancelFunc,
) (result string, consumeErr error) {
	defer func() {
		if consumeErr == nil {
			return
		}
		if cancelOnError != nil {
			cancelOnError()
		}
		drainAgentEvents(iterator)
	}()
	if iterator == nil {
		return "", einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent returned a nil iterator"))
	}
	finalText := ""
	directToolText := ""
	providerToolCalls := make(map[string]providerToolCallAssociation)
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event == nil {
			return "", einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent emitted a nil event"))
		}
		if event.Err != nil {
			closeAgentEventMessageStream(event)
			return "", mapEinoRuntimeError(event.Err)
		}
		if event.Action != nil && (event.Action.Exit || event.Action.Interrupted != nil || event.Action.TransferToAgent != nil || event.Action.BreakLoop != nil || event.Action.CustomizedAction != nil) {
			closeAgentEventMessageStream(event)
			return "", einoRuntimeError(foundation.ErrorNonRetryableFailure, agentUnexpectedActionCode, errors.New("eino agent emitted an unsupported control action"))
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		variant := event.Output.MessageOutput
		if variant.IsStreaming && variant.MessageStream == nil {
			return "", einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent emitted an incomplete message stream"))
		}
		message, err := variant.GetMessage()
		if err != nil {
			return "", mapEinoRuntimeError(err)
		}
		if message == nil || !utf8.ValidString(message.Content) {
			return "", einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent emitted an invalid message"))
		}
		switch message.Role {
		case schema.Assistant:
			if len(message.Content) > agentapplication.MaxAgentResultBytes {
				return "", einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent assistant output exceeded its bound"))
			}
			if err := registerProviderToolCalls(providerToolCalls, message.ToolCalls); err != nil {
				return "", err
			}
			if len(message.ToolCalls) == 0 && strings.TrimSpace(message.Content) != "" {
				finalText = message.Content
			}
		case schema.Tool:
			if len(message.Content) > agentapplication.MaxAgentResultBytes || strings.TrimSpace(message.ToolCallID) == "" {
				return "", einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent tool event is invalid"))
			}
			if err := consumeProviderToolResult(providerToolCalls, variant.ToolName, message); err != nil {
				return "", err
			}
			if allowDirectToolResult && strings.TrimSpace(message.Content) != "" {
				directToolText = message.Content
			}
		default:
			return "", einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent emitted an unsupported message role"))
		}
	}
	if unresolvedProviderToolCalls(providerToolCalls) {
		return "", einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent ended with unresolved tool calls"))
	}
	if strings.TrimSpace(finalText) == "" && allowDirectToolResult {
		finalText = directToolText
	}
	if strings.TrimSpace(finalText) == "" {
		return "", einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent did not emit a final assistant message"))
	}
	return finalText, nil
}

func drainAgentEvents(iterator *adk.AsyncIterator[*adk.AgentEvent]) {
	if iterator == nil {
		return
	}
	for {
		event, ok := iterator.Next()
		if !ok {
			return
		}
		closeAgentEventMessageStream(event)
	}
}

func closeAgentEventMessageStream(event *adk.AgentEvent) {
	if event == nil || event.Output == nil || event.Output.MessageOutput == nil {
		return
	}
	variant := event.Output.MessageOutput
	if variant.IsStreaming && variant.MessageStream != nil {
		variant.MessageStream.Close()
	}
}

func registerProviderToolCalls(associations map[string]providerToolCallAssociation, calls []schema.ToolCall) error {
	for _, call := range calls {
		if !validProviderToolCallID(call.ID) || !validProviderToolName(call.Function.Name) {
			return einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent emitted an invalid provider tool call"))
		}
		if _, exists := associations[call.ID]; exists {
			return einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent reused a provider tool call id"))
		}
		associations[call.ID] = providerToolCallAssociation{toolName: call.Function.Name}
	}
	return nil
}

func consumeProviderToolResult(associations map[string]providerToolCallAssociation, eventToolName string, message *schema.Message) error {
	if message == nil || !validProviderToolCallID(message.ToolCallID) || !validProviderToolName(eventToolName) ||
		!validProviderToolName(message.ToolName) || eventToolName != message.ToolName {
		return einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent emitted an invalid tool result association"))
	}
	association, exists := associations[message.ToolCallID]
	if !exists {
		return einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent emitted an unknown tool result association"))
	}
	if association.consumed {
		return einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent emitted a duplicate tool result association"))
	}
	if association.toolName != eventToolName {
		return einoRuntimeError(foundation.ErrorConsistencyViolation, agentIteratorOutputCode, errors.New("eino agent emitted a mismatched tool result association"))
	}
	association.consumed = true
	associations[message.ToolCallID] = association
	return nil
}

func unresolvedProviderToolCalls(associations map[string]providerToolCallAssociation) bool {
	for _, association := range associations {
		if !association.consumed {
			return true
		}
	}
	return false
}

func validProviderToolCallID(id string) bool {
	return strings.TrimSpace(id) != "" && len(id) <= maxProviderToolCallIDBytes && utf8.ValidString(id)
}

func validProviderToolName(name string) bool {
	return strings.TrimSpace(name) != "" && len(name) <= toolsdomain.MaxToolNameBytes && utf8.ValidString(name)
}

func projectAgentMessages(messages []agentapplication.AgentMessage) []*schema.Message {
	output := make([]*schema.Message, len(messages))
	for index, message := range messages {
		switch message.Role {
		case agentapplication.AgentMessageSystem:
			output[index] = schema.SystemMessage(message.Content)
		case agentapplication.AgentMessageUser:
			output[index] = schema.UserMessage(message.Content)
		case agentapplication.AgentMessageAssistant:
			output[index] = schema.AssistantMessage(message.Content, nil)
		}
	}
	return output
}

func cloneAgentToolSpec(spec agentapplication.AgentToolSpec) agentapplication.AgentToolSpec {
	spec.InputSchema = append([]byte(nil), spec.InputSchema...)
	return spec
}

var _ agentapplication.AgentRuntime = (*AgentRuntime)(nil)
var _ einotool.InvokableTool = (*executionTool)(nil)
