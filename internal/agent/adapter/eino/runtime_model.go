package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	agentRuntimeUnavailableCode = "AGENT_EINO_RUNTIME_UNAVAILABLE"
	agentRuntimeRequestCode     = "AGENT_EINO_RUNTIME_REQUEST_INVALID"
	agentRuntimeOutputCode      = "AGENT_EINO_RUNTIME_OUTPUT_INVALID"
	agentRuntimeInvokeCode      = "AGENT_EINO_RUNTIME_INVOKE_FAILED"
	maxRuntimeStreamFrames      = 128 * 1024
)

type runtimeCallBinding struct {
	model           agentdomain.ModelRef
	profile         agentdomain.ModelProfileRef
	prompt          agentdomain.PromptRef
	schema          agentdomain.SchemaRef
	maxInputTokens  int64
	maxOutputTokens int
	phase           agentdomain.ModelCallPhase
	budget          *agentapplication.RunBudgetLedger
	recorder        *agentapplication.ModelCallRecorder
}

type runtimeCallState struct {
	mu           sync.Mutex
	calls        int
	usage        agentdomain.TokenUsage
	usageMissing bool
}

type recordingRuntimeModel struct {
	backend einomodel.ToolCallingChatModel
	binding runtimeCallBinding
	state   *runtimeCallState
}

type runtimeCall struct {
	authorization *agentapplication.RunBudgetAuthorization
	recording     *agentapplication.ModelCallRecording
	request       []byte
}

func newRecordingRuntimeModel(backend einomodel.ToolCallingChatModel, binding runtimeCallBinding) (*recordingRuntimeModel, error) {
	if nilEinoChatModel(backend) || binding.model.Validate() != nil || binding.profile.Validate() != nil ||
		binding.prompt.Validate() != nil || binding.schema.Validate() != nil || binding.maxInputTokens <= 0 ||
		binding.maxOutputTokens <= 0 || binding.budget == nil || binding.recorder == nil ||
		(binding.phase != agentdomain.ModelCallAgent && binding.phase != agentdomain.ModelCallAnswer) {
		return nil, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode, errors.New("eino runtime model binding is unavailable"))
	}
	return &recordingRuntimeModel{backend: backend, binding: binding, state: &runtimeCallState{}}, nil
}

func (model *recordingRuntimeModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	if model == nil || nilEinoChatModel(model.backend) {
		return nil, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode, errors.New("eino runtime model is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	call, callOpts, err := model.start(ctx, input, opts)
	if err != nil {
		return nil, err
	}
	output, invokeErr := model.backend.Generate(ctx, input, callOpts...)
	var response []byte
	usage, usageKnown := runtimeMessageUsage(output)
	if invokeErr == nil && output != nil && output.ResponseMeta != nil && output.ResponseMeta.Usage != nil && !usageKnown {
		invokeErr = einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("eino runtime returned invalid token usage"))
	}
	if invokeErr == nil {
		response, invokeErr = canonicalRuntimeResponse(output)
	}
	terminalErr := model.finish(ctx, call, response, usage, usageKnown, invokeErr)
	if terminalErr != nil {
		return nil, terminalErr
	}
	return output, nil
}

func (model *recordingRuntimeModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	if model == nil || nilEinoChatModel(model.backend) {
		return nil, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode, errors.New("eino runtime model is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	call, callOpts, err := model.start(ctx, input, opts)
	if err != nil {
		return nil, err
	}
	source, invokeErr := model.backend.Stream(ctx, input, callOpts...)
	if invokeErr != nil {
		return nil, model.finish(ctx, call, nil, agentdomain.TokenUsage{}, false, invokeErr)
	}
	if source == nil {
		invokeErr = einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("eino runtime returned a nil stream"))
		return nil, model.finish(ctx, call, nil, agentdomain.TokenUsage{}, false, invokeErr)
	}
	output, writer := schema.Pipe[*schema.Message](1)
	go model.forwardAndRecordStream(ctx, source, writer, call)
	return output, nil
}

func (model *recordingRuntimeModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	if model == nil || nilEinoChatModel(model.backend) {
		return nil, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode, errors.New("eino runtime model is unavailable"))
	}
	backend, err := model.backend.WithTools(tools)
	if err != nil {
		return nil, mapEinoRuntimeError(err)
	}
	return &recordingRuntimeModel{backend: backend, binding: model.binding, state: model.state}, nil
}

func (model *recordingRuntimeModel) start(ctx context.Context, input []*schema.Message, opts []einomodel.Option) (runtimeCall, []einomodel.Option, error) {
	callOpts := append(append([]einomodel.Option(nil), opts...), einomodel.WithMaxTokens(model.binding.maxOutputTokens))
	request, err := canonicalRuntimeRequest(input, callOpts)
	if err != nil {
		return runtimeCall{}, nil, err
	}
	maximum := agentapplication.RunBudgetReservation{
		ModelCalls: 1, ReservedInputTokens: model.binding.maxInputTokens,
		ReservedOutputTokens: int64(model.binding.maxOutputTokens),
	}
	var authorization *agentapplication.RunBudgetAuthorization
	if model.binding.phase == agentdomain.ModelCallAgent {
		authorization, err = model.binding.budget.AuthorizeAgentCall(maximum)
	} else {
		authorization, err = model.binding.budget.AuthorizeDownstreamCall(agentapplication.RunBudgetPhaseAnswer)
	}
	if err != nil {
		return runtimeCall{}, nil, err
	}
	if authorization.Maximum.ReservedInputTokens < model.binding.maxInputTokens ||
		authorization.Maximum.ReservedOutputTokens < int64(model.binding.maxOutputTokens) {
		_ = authorization.Settle(&agentapplication.RunBudgetUsage{})
		return runtimeCall{}, nil, einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeRequestCode, errors.New("runtime request exceeds its authorized token reservation"))
	}
	recording, err := model.binding.recorder.Start(ctx, agentapplication.ModelCallStart{
		CallNo: authorization.CallNo, Phase: model.binding.phase, Model: model.binding.model,
		Profile: model.binding.profile, Prompt: model.binding.prompt, Schema: model.binding.schema,
		MaxOutputTokens: model.binding.maxOutputTokens, Request: request,
	})
	if err != nil {
		_ = authorization.Settle(&agentapplication.RunBudgetUsage{})
		return runtimeCall{}, nil, err
	}
	return runtimeCall{authorization: authorization, recording: recording, request: request}, callOpts, nil
}

func (model *recordingRuntimeModel) finish(
	ctx context.Context,
	call runtimeCall,
	response []byte,
	usage agentdomain.TokenUsage,
	usageKnown bool,
	invokeErr error,
) error {
	mappedInvokeErr := mapEinoRuntimeError(invokeErr)
	terminal := agentapplication.ModelCallTerminal{Response: response, Usage: usage, Error: mappedInvokeErr}
	if invokeErr == nil {
		terminal.Status = agentdomain.ModelCallSucceeded
	} else {
		terminal.Status = agentdomain.ModelCallFailed
	}
	_, completeErr := call.recording.Complete(ctx, terminal)
	var budgetUsage *agentapplication.RunBudgetUsage
	if usageKnown {
		budgetUsage = &agentapplication.RunBudgetUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens}
	}
	settleErr := call.authorization.Settle(budgetUsage)
	if invokeErr == nil && completeErr == nil && settleErr == nil {
		model.state.addUsage(usage, usageKnown)
	}
	if completeErr != nil {
		return completeErr
	}
	if settleErr != nil {
		return settleErr
	}
	if mappedInvokeErr != nil {
		return mappedInvokeErr
	}
	return nil
}

func (model *recordingRuntimeModel) forwardAndRecordStream(
	ctx context.Context,
	source *schema.StreamReader[*schema.Message],
	writer *schema.StreamWriter[*schema.Message],
	call runtimeCall,
) {
	defer writer.Close()
	defer source.Close()
	accumulator := runtimeStreamAccumulator{}
	forwarding := true
	for {
		message, err := source.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			terminalErr := model.finish(ctx, call, nil, agentdomain.TokenUsage{}, false, err)
			if forwarding {
				writer.Send(nil, terminalErr)
			}
			return
		}
		if model.binding.phase == agentdomain.ModelCallAnswer && message != nil && len(message.ToolCalls) != 0 {
			err = einoRuntimeError(foundation.ErrorConsistencyViolation, agentapplication.ErrorCodeAnswerStreamInvalid, errors.New("eino answer stream emitted an unexpected tool call"))
			terminalErr := model.finish(ctx, call, nil, agentdomain.TokenUsage{}, false, err)
			if forwarding {
				writer.Send(nil, terminalErr)
			}
			return
		}
		if err = accumulator.add(message); err != nil {
			terminalErr := model.finish(ctx, call, nil, agentdomain.TokenUsage{}, false, err)
			if forwarding {
				writer.Send(nil, terminalErr)
			}
			return
		}
		if forwarding && writer.Send(message, nil) {
			forwarding = false
		}
	}
	combined := accumulator.message()
	var response []byte
	usage, usageKnown := runtimeMessageUsage(combined)
	response, err := canonicalRuntimeResponse(combined)
	terminalErr := model.finish(ctx, call, response, usage, usageKnown, err)
	if terminalErr != nil && forwarding {
		writer.Send(nil, terminalErr)
	}
}

// runtimeStreamAccumulator retains one bounded projection instead of every
// Provider frame. The original frames still flow to the sole consumer.
type runtimeStreamAccumulator struct {
	frames       int
	role         schema.RoleType
	name         string
	content      strings.Builder
	reasoning    strings.Builder
	finishReason string
	usage        *schema.TokenUsage
}

func (accumulator *runtimeStreamAccumulator) add(message *schema.Message) error {
	if accumulator == nil || message == nil || accumulator.frames >= maxRuntimeStreamFrames ||
		(message.Role != "" && message.Role != schema.Assistant) || len(message.ToolCalls) != 0 ||
		message.ToolCallID != "" || message.ToolName != "" || len(message.MultiContent) != 0 ||
		len(message.UserInputMultiContent) != 0 || len(message.AssistantGenMultiContent) != 0 ||
		!utf8.ValidString(message.Content) || !utf8.ValidString(message.ReasoningContent) ||
		!utf8.ValidString(message.Name) || !supportedRuntimeMessageExtra(message) || !supportedRuntimeStreamExtra(message.Extra) {
		return einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("eino runtime stream emitted an invalid bounded chunk"))
	}
	retainedBytes := accumulator.content.Len() + accumulator.reasoning.Len() + len(accumulator.name)
	if retainedBytes > agentapplication.MaxAgentResultBytes {
		return einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("eino runtime stream exceeded its bounded result"))
	}
	remainingBytes := agentapplication.MaxAgentResultBytes - retainedBytes
	for _, additionalBytes := range []int{len(message.Content), len(message.ReasoningContent)} {
		if additionalBytes > remainingBytes {
			return einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("eino runtime stream exceeded its bounded result"))
		}
		remainingBytes -= additionalBytes
	}
	if accumulator.name == "" && len(message.Name) > remainingBytes {
		return einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("eino runtime stream exceeded its bounded result"))
	}
	if message.Role != "" {
		if accumulator.role != "" && accumulator.role != message.Role {
			return einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("eino runtime stream changed message role"))
		}
		accumulator.role = message.Role
	}
	if message.Name != "" {
		if len(message.Name) > agentapplication.MaxAgentMessageBytes || accumulator.name != "" && accumulator.name != message.Name {
			return einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("eino runtime stream changed message name"))
		}
		accumulator.name = message.Name
	}
	if message.ResponseMeta != nil {
		if message.ResponseMeta.LogProbs != nil || !utf8.ValidString(message.ResponseMeta.FinishReason) || len(message.ResponseMeta.FinishReason) > 128 {
			return einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("eino runtime stream emitted unsupported response metadata"))
		}
		if message.ResponseMeta.FinishReason != "" {
			accumulator.finishReason = message.ResponseMeta.FinishReason
		}
		if message.ResponseMeta.Usage != nil {
			if accumulator.usage != nil {
				return einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("eino runtime stream repeated token usage"))
			}
			if _, known := runtimeMessageUsage(message); !known {
				return einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("eino runtime stream emitted invalid token usage"))
			}
			usage := *message.ResponseMeta.Usage
			accumulator.usage = &usage
		}
	}
	accumulator.frames++
	accumulator.content.WriteString(message.Content)
	accumulator.reasoning.WriteString(message.ReasoningContent)
	return nil
}

func (accumulator *runtimeStreamAccumulator) message() *schema.Message {
	if accumulator == nil {
		return nil
	}
	message := &schema.Message{
		Role: accumulator.role, Name: accumulator.name,
		Content: accumulator.content.String(), ReasoningContent: accumulator.reasoning.String(),
	}
	if accumulator.finishReason != "" || accumulator.usage != nil {
		message.ResponseMeta = &schema.ResponseMeta{FinishReason: accumulator.finishReason, Usage: accumulator.usage}
	}
	return message
}

func supportedRuntimeStreamExtra(extra map[string]any) bool {
	for key, value := range extra {
		if key != "_eino_msg_id" && key != "openai-request-id" && key != "reasoning-content" {
			return false
		}
		text, ok := runtimeExtraString(value)
		if !ok || !utf8.ValidString(text) || len(text) > agentapplication.MaxAgentMessageBytes {
			return false
		}
	}
	return true
}

func runtimeExtraString(value any) (string, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || reflected.Kind() != reflect.String {
		return "", false
	}
	return reflected.String(), true
}

func (state *runtimeCallState) addUsage(usage agentdomain.TokenUsage, known bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.calls++
	if !known {
		state.usageMissing = true
		return
	}
	state.usage.InputTokens += usage.InputTokens
	state.usage.OutputTokens += usage.OutputTokens
	state.usage.TotalTokens = state.usage.InputTokens + state.usage.OutputTokens
}

func (state *runtimeCallState) snapshot() (int, agentdomain.TokenUsage, bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.calls, state.usage, state.calls > 0 && !state.usageMissing
}

type canonicalRuntimeMessage struct {
	Role             string                 `json:"role"`
	Content          string                 `json:"content,omitempty"`
	Name             string                 `json:"name,omitempty"`
	ReasoningContent string                 `json:"reasoning_content,omitempty"`
	ToolCallID       string                 `json:"tool_call_id,omitempty"`
	ToolName         string                 `json:"tool_name,omitempty"`
	ToolCalls        []canonicalRuntimeTool `json:"tool_calls,omitempty"`
}

type canonicalRuntimeTool struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type canonicalRuntimeToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type canonicalRuntimeOptions struct {
	Temperature      *float32                   `json:"temperature,omitempty"`
	TopP             *float32                   `json:"top_p,omitempty"`
	MaxTokens        *int                       `json:"max_tokens,omitempty"`
	Stop             []string                   `json:"stop,omitempty"`
	Tools            []canonicalRuntimeToolInfo `json:"tools,omitempty"`
	ToolChoice       *string                    `json:"tool_choice,omitempty"`
	AllowedToolNames []string                   `json:"allowed_tool_names,omitempty"`
}

type canonicalRuntimeRequestDocument struct {
	Messages []canonicalRuntimeMessage `json:"messages"`
	Options  canonicalRuntimeOptions   `json:"options"`
}

type canonicalRuntimeResponseDocument struct {
	Message      canonicalRuntimeMessage `json:"message"`
	FinishReason string                  `json:"finish_reason,omitempty"`
	Usage        *agentdomain.TokenUsage `json:"usage,omitempty"`
}

func canonicalRuntimeRequest(messages []*schema.Message, opts []einomodel.Option) ([]byte, error) {
	canonical, err := canonicalRuntimeMessages(messages, false)
	if err != nil {
		return nil, err
	}
	options := einomodel.GetCommonOptions(nil, opts...)
	canonicalOptions, err := projectRuntimeOptions(options)
	if err != nil {
		return nil, err
	}
	document := canonicalRuntimeRequestDocument{Messages: canonical, Options: canonicalOptions}
	encoded, err := json.Marshal(document)
	if err != nil || int64(len(encoded)) > agentapplication.MaxModelCallRequestBytes {
		return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime request could not be canonically encoded"))
	}
	return encoded, nil
}

func projectRuntimeOptions(options *einomodel.Options) (canonicalRuntimeOptions, error) {
	if options == nil || options.Model != nil || len(options.DeferredTools) != 0 ||
		options.ToolSearchTool != nil || options.AgenticToolChoice != nil {
		return canonicalRuntimeOptions{}, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime request contains unsupported model or tool options"))
	}
	tools, toolNames, err := projectRuntimeTools(options.Tools)
	if err != nil {
		return canonicalRuntimeOptions{}, err
	}
	toolChoice, err := projectRuntimeToolChoice(options.ToolChoice)
	if err != nil {
		return canonicalRuntimeOptions{}, err
	}
	allowedToolNames := make([]string, len(options.AllowedToolNames))
	allowedSeen := make(map[string]struct{}, len(options.AllowedToolNames))
	for index, name := range options.AllowedToolNames {
		if !validProviderToolName(name) {
			return canonicalRuntimeOptions{}, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime request contains an invalid allowed tool name"))
		}
		if _, found := toolNames[name]; !found {
			return canonicalRuntimeOptions{}, einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeRequestCode, errors.New("runtime request allows a tool that is not defined"))
		}
		if _, duplicate := allowedSeen[name]; duplicate {
			return canonicalRuntimeOptions{}, einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeRequestCode, errors.New("runtime request repeats an allowed tool name"))
		}
		allowedSeen[name] = struct{}{}
		allowedToolNames[index] = name
	}
	return canonicalRuntimeOptions{
		Temperature: cloneFloat32(options.Temperature), TopP: cloneFloat32(options.TopP), MaxTokens: cloneInt(options.MaxTokens),
		Stop: append([]string(nil), options.Stop...), Tools: tools, ToolChoice: toolChoice, AllowedToolNames: allowedToolNames,
	}, nil
}

func projectRuntimeTools(tools []*schema.ToolInfo) ([]canonicalRuntimeToolInfo, map[string]struct{}, error) {
	if len(tools) > agentapplication.MaxAgentTools {
		return nil, nil, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime request contains too many tools"))
	}
	output := make([]canonicalRuntimeToolInfo, len(tools))
	names := make(map[string]struct{}, len(tools))
	for index, tool := range tools {
		if tool == nil || !validProviderToolName(tool.Name) || strings.TrimSpace(tool.Desc) == "" ||
			len(tool.Desc) > toolsdomain.MaxToolDescriptionBytes || !utf8.ValidString(tool.Desc) || len(tool.Extra) != 0 {
			return nil, nil, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime request contains an invalid tool definition"))
		}
		if _, duplicate := names[tool.Name]; duplicate {
			return nil, nil, einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeRequestCode, errors.New("runtime request repeats a tool definition"))
		}
		parameters, err := projectRuntimeToolParameters(tool.ParamsOneOf)
		if err != nil {
			return nil, nil, err
		}
		names[tool.Name] = struct{}{}
		output[index] = canonicalRuntimeToolInfo{Name: tool.Name, Description: tool.Desc, Parameters: parameters}
	}
	return output, names, nil
}

func projectRuntimeToolParameters(params *schema.ParamsOneOf) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	document, err := params.ToJSONSchema()
	if err != nil || document == nil {
		return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime tool parameters are invalid"))
	}
	encoded, err := json.Marshal(document)
	if err != nil || len(encoded) == 0 || len(encoded) > toolsdomain.MaxToolSchemaBytes {
		return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime tool parameters could not be encoded"))
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err = decoder.Decode(&value); err != nil {
		return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime tool parameters could not be decoded"))
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime tool parameters contain trailing data"))
	}
	if _, object := value.(map[string]any); !object {
		return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime tool parameters are not an object schema"))
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) > toolsdomain.MaxToolSchemaBytes {
		return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime tool parameters could not be canonically encoded"))
	}
	return canonical, nil
}

func projectRuntimeToolChoice(choice *schema.ToolChoice) (*string, error) {
	if choice == nil {
		return nil, nil
	}
	switch *choice {
	case schema.ToolChoiceForbidden, schema.ToolChoiceAllowed, schema.ToolChoiceForced:
		value := string(*choice)
		return &value, nil
	default:
		return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime request contains an invalid tool choice"))
	}
}

func cloneFloat32(value *float32) *float32 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func canonicalRuntimeResponse(message *schema.Message) ([]byte, error) {
	canonical, err := canonicalRuntimeMessages([]*schema.Message{message}, true)
	if err != nil || len(canonical) != 1 {
		return nil, einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("runtime response is invalid"))
	}
	usage, known := runtimeMessageUsage(message)
	var usageValue *agentdomain.TokenUsage
	if known {
		usageValue = &usage
	}
	finishReason := ""
	if message.ResponseMeta != nil {
		finishReason = message.ResponseMeta.FinishReason
	}
	encoded, err := json.Marshal(canonicalRuntimeResponseDocument{Message: canonical[0], FinishReason: finishReason, Usage: usageValue})
	if err != nil || int64(len(encoded)) > agentapplication.MaxModelCallResponseBytes {
		return nil, einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("runtime response could not be canonically encoded"))
	}
	return encoded, nil
}

func canonicalRuntimeMessages(messages []*schema.Message, response bool) ([]canonicalRuntimeMessage, error) {
	if len(messages) == 0 || len(messages) > agentapplication.MaxAgentMessages {
		return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime message list is invalid"))
	}
	output := make([]canonicalRuntimeMessage, len(messages))
	toolCallRefs := canonicalRuntimeToolCallRefs{values: make(map[string]string), response: response}
	for index, message := range messages {
		maxContentBytes := agentapplication.MaxAgentMessageBytes
		if response {
			maxContentBytes = agentapplication.MaxAgentResultBytes
		}
		if message == nil || !utf8.ValidString(message.Content) || !utf8.ValidString(message.Name) ||
			!utf8.ValidString(message.ReasoningContent) || len(message.Content) > maxContentBytes ||
			len(message.Name) > agentapplication.MaxAgentMessageBytes || len(message.ReasoningContent) > maxContentBytes ||
			len(message.MultiContent) != 0 || len(message.UserInputMultiContent) != 0 || len(message.AssistantGenMultiContent) != 0 ||
			!supportedRuntimeMessageExtra(message) {
			return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime message contains unsupported content"))
		}
		if response && message.Role != schema.Assistant {
			return nil, einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New("runtime response role is invalid"))
		}
		item := canonicalRuntimeMessage{
			Role: string(message.Role), Content: message.Content, Name: message.Name, ReasoningContent: message.ReasoningContent,
			ToolName: message.ToolName,
		}
		for _, call := range message.ToolCalls {
			callRef, refErr := toolCallRefs.declare(call.ID)
			if refErr != nil {
				return nil, refErr
			}
			item.ToolCalls = append(item.ToolCalls, canonicalRuntimeTool{
				ID: callRef, Type: call.Type, Name: call.Function.Name, Arguments: call.Function.Arguments,
			})
		}
		if message.ToolCallID != "" {
			callRef, refErr := toolCallRefs.resolve(message.ToolCallID)
			if refErr != nil {
				return nil, refErr
			}
			item.ToolCallID = callRef
		}
		if strings.TrimSpace(item.Role) == "" || (item.Content == "" && len(item.ToolCalls) == 0 && item.ToolCallID == "") {
			return nil, einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New("runtime message content is incomplete"))
		}
		output[index] = item
	}
	return output, nil
}

type canonicalRuntimeToolCallRefs struct {
	values   map[string]string
	next     int
	response bool
}

func (refs *canonicalRuntimeToolCallRefs) declare(providerID string) (string, error) {
	if refs == nil || !validProviderToolCallID(providerID) {
		return "", refs.invalid("runtime message contains an invalid provider tool call association")
	}
	if _, duplicate := refs.values[providerID]; duplicate {
		return "", refs.invalid("runtime message repeats a provider tool call association")
	}
	refs.next++
	value := "tool-call-" + strconv.Itoa(refs.next)
	refs.values[providerID] = value
	return value, nil
}

func (refs *canonicalRuntimeToolCallRefs) resolve(providerID string) (string, error) {
	if refs == nil || !validProviderToolCallID(providerID) {
		return "", refs.invalid("runtime message contains an invalid provider tool result association")
	}
	value, found := refs.values[providerID]
	if !found {
		return "", refs.invalid("runtime message contains an unknown provider tool result association")
	}
	return value, nil
}

func (refs *canonicalRuntimeToolCallRefs) invalid(message string) error {
	if refs != nil && refs.response {
		return einoRuntimeError(foundation.ErrorConsistencyViolation, agentRuntimeOutputCode, errors.New(message))
	}
	return einoRuntimeError(foundation.ErrorInvalidInput, agentRuntimeRequestCode, errors.New(message))
}

// Eino and the OpenAI extension attach execution-only identifiers to Extra.
// The OpenAI request converter does not serialize these keys. Reasoning content
// is accepted only when the duplicated extension value agrees with the actual
// provider-visible Message field recorded above.
func supportedRuntimeMessageExtra(message *schema.Message) bool {
	for key, value := range message.Extra {
		switch key {
		case "_eino_msg_id", "openai-request-id":
			if _, ok := runtimeExtraString(value); !ok {
				return false
			}
		case "reasoning-content":
			reasoning, ok := value.(string)
			if !ok || reasoning != message.ReasoningContent {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func runtimeMessageUsage(message *schema.Message) (agentdomain.TokenUsage, bool) {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		return agentdomain.TokenUsage{}, false
	}
	provider := message.ResponseMeta.Usage
	usage := agentdomain.TokenUsage{
		InputTokens: int64(provider.PromptTokens), OutputTokens: int64(provider.CompletionTokens), TotalTokens: int64(provider.TotalTokens),
	}
	return usage, usage.Validate() == nil && usage.TotalTokens > 0
}

func nilEinoChatModel(value einomodel.ToolCallingChatModel) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Ptr && reflected.IsNil()
}

func mapEinoRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) && classified != nil {
		return classified
	}
	if errors.Is(err, context.Canceled) {
		return einoRuntimeError(foundation.ErrorNonRetryableFailure, agentapplication.ErrorCodeOperationCancelled, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return einoRuntimeError(foundation.ErrorRetryableFailure, agentapplication.ErrorCodeOperationDeadline, context.DeadlineExceeded)
	}
	return einoRuntimeError(foundation.ErrorNonRetryableFailure, agentRuntimeInvokeCode, errors.New("eino runtime invocation failed"))
}

func einoRuntimeError(kind foundation.ErrorKind, code string, cause error) error {
	return foundation.NewError(kind, code, kind == foundation.ErrorRetryableFailure, cause)
}

var _ einomodel.ToolCallingChatModel = (*recordingRuntimeModel)(nil)
