package models

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	einoRuntimeSDKModel = "zhixu-eino-runtime-chat"
	maxRuntimeToolCalls = 16
	maxRuntimeToolID    = 256
	maxRuntimeToolName  = 128
)

// RuntimeChatCapability 暴露只供 Eino Agent/Stream adapter 使用的基础设施
// ChatModel；Eino 类型不会穿过 agent application 或 Workflow Port。
type RuntimeChatCapability struct {
	state    CapabilityState
	model    einoModel.ToolCallingChatModel
	contract ChatContract
}

// State 返回 capability 是否由 Eino runtime 构造。
func (capability RuntimeChatCapability) State() CapabilityState {
	if capability.state == CapabilityConfigured && capability.model != nil {
		return CapabilityConfigured
	}
	return CapabilityDisabled
}

// Model 返回 Eino ToolCallingChatModel；disabled 时为 nil。
func (capability RuntimeChatCapability) Model() einoModel.ToolCallingChatModel {
	if capability.State() != CapabilityConfigured {
		return nil
	}
	return capability.model
}

// Contract 返回不含 Secret/Endpoint 的模型绑定。
func (capability RuntimeChatCapability) Contract() (ChatContract, bool) {
	if capability.State() != CapabilityConfigured {
		return ChatContract{}, false
	}
	return capability.contract, true
}

func (capability RuntimeChatCapability) String() string {
	if capability.State() != CapabilityConfigured {
		return "RuntimeChatCapability{State:disabled}"
	}
	return fmt.Sprintf("RuntimeChatCapability{State:configured Provider:%q Model:%q}", capability.contract.Provider, capability.contract.Model.ModelID)
}

func (capability RuntimeChatCapability) GoString() string { return capability.String() }

// EinoRuntimeChatModel 是不带结构化 response_format 的 Eino OpenAI ChatModel。
// 它只负责 Provider transport；调用记录、预算和 Tool 权限由上层 Adapter 拥有。
type EinoRuntimeChatModel struct {
	http    chatHTTPConfig
	backend *einoopenai.ChatModel
}

// NewEinoRuntimeChatModel 创建支持 Generate/Stream/WithTools 的 Eino runtime
// ChatModel。每次调用仍由调用方决定是否绑定工具和禁止工具。
func NewEinoRuntimeChatModel(options OpenAIChatOptions) (*EinoRuntimeChatModel, error) {
	if err := validateEinoChatAPIStyle(options.APIStyle); err != nil {
		return nil, err
	}
	config, err := newChatHTTPConfig(chatHTTPOptions{
		client: options.Client, baseURL: options.BaseURL, apiKey: options.APIKey,
		model: options.Model, modelVersion: options.ModelVersion, adapterVersion: options.AdapterVersion,
		timeout: options.Timeout, maxRequestBytes: options.MaxRequestBytes, maxResponseBytes: options.MaxResponseBytes,
		apiStyle: options.APIStyle, provider: options.Provider,
	})
	if err != nil {
		return nil, err
	}
	if config.client.client.Timeout <= 0 || options.Timeout < config.client.client.Timeout {
		config.client.client.Timeout = options.Timeout
	}
	config.client.client.Transport = &einoRuntimeRoundTripper{
		base: config.client.client.Transport, model: config.contract.Model.ModelVersion,
		maxRequestBytes: config.maxRequestBytes, maxResponseBytes: config.maxResponseBytes,
	}
	// Agent/tool and final-answer runs must be reproducible enough for the
	// project's citation and faithfulness gates. Callers may still override this
	// per request through the provider-neutral Eino option when explicitly needed.
	runtimeTemperature := float32(0)
	backend, err := einoopenai.NewChatModel(context.Background(), &einoopenai.ChatModelConfig{
		APIKey: options.APIKey, BaseURL: einoChatSDKBaseURL(config.endpointURL), Model: einoRuntimeSDKModel,
		Temperature: &runtimeTemperature,
		HTTPClient:  config.client.client,
	})
	if err != nil {
		_ = config.closeModelResource()
		return nil, chatConfigErrorWithCause(errors.New("eino runtime chat initialization failed"))
	}
	return &EinoRuntimeChatModel{http: config, backend: backend}, nil
}

func (model *EinoRuntimeChatModel) closeModelResource() error {
	if model == nil {
		return nil
	}
	return model.http.closeModelResource()
}

// Generate delegates to eino-ext while validating the response at the wire
// transport and at the Eino message boundary.
func (model *EinoRuntimeChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.Message, error) {
	if model == nil || model.backend == nil {
		return nil, chatError(foundation.ErrorDependencyUnavailable, ErrorCodeChatCapabilityUnavailable, false, errChatCapabilityOff)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	output, err := model.backend.Generate(ctx, cloneEinoMessages(input), opts...)
	if err != nil {
		return nil, normalizeEinoRuntimeError(ctx, err)
	}
	if err := validateRuntimeGeneratedMessage(output); err != nil {
		return nil, err
	}
	return output, nil
}

// Stream returns the provider's real SSE-backed stream. The caller owns the
// returned reader and must consume it to EOF or Close it on every path.
func (model *EinoRuntimeChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	if model == nil || model.backend == nil {
		return nil, chatError(foundation.ErrorDependencyUnavailable, ErrorCodeChatCapabilityUnavailable, false, errChatCapabilityOff)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	toolCalls := newRuntimeStreamToolCallValidator()
	opts = append(opts, einoopenai.WithResponseChunkMessageModifier(model.runtimeChunkValidator(toolCalls)))
	stream, err := model.backend.Stream(ctx, cloneEinoMessages(input), opts...)
	if err != nil {
		return nil, normalizeEinoRuntimeError(ctx, err)
	}
	return validateRuntimeStream(stream, toolCalls), nil
}

// WithTools derives an immutable per-request Eino model. It never mutates the
// shared base model and is therefore safe for concurrent Agent runs.
func (model *EinoRuntimeChatModel) WithTools(tools []*schema.ToolInfo) (einoModel.ToolCallingChatModel, error) {
	if model == nil || model.backend == nil {
		return nil, chatError(foundation.ErrorDependencyUnavailable, ErrorCodeChatCapabilityUnavailable, false, errChatCapabilityOff)
	}
	if len(tools) > maxRuntimeToolCalls {
		return nil, chatResponseError(ErrorCodeChatRequestInvalid)
	}
	for _, tool := range tools {
		if tool == nil || strings.TrimSpace(tool.Name) == "" || len(tool.Name) > maxRuntimeToolName || len(tool.Name) > maxRuntimeToolID {
			return nil, chatResponseError(ErrorCodeChatRequestInvalid)
		}
	}
	backend, err := model.backend.WithTools(cloneToolInfos(tools))
	if err != nil {
		return nil, chatResponseError(ErrorCodeChatRequestInvalid)
	}
	return &einoRuntimeToolModel{parent: model, backend: backend}, nil
}

type einoRuntimeToolModel struct {
	parent  *EinoRuntimeChatModel
	backend einoModel.ToolCallingChatModel
}

func (model *einoRuntimeToolModel) Generate(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.Message, error) {
	return model.parent.generateWithBackend(ctx, model.backend, input, opts...)
}

func (model *einoRuntimeToolModel) Stream(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	return model.parent.streamWithBackend(ctx, model.backend, input, opts...)
}

func (model *einoRuntimeToolModel) WithTools(tools []*schema.ToolInfo) (einoModel.ToolCallingChatModel, error) {
	return model.parent.WithTools(tools)
}

func (model *EinoRuntimeChatModel) generateWithBackend(ctx context.Context, backend einoModel.BaseChatModel, input []*schema.Message, opts ...einoModel.Option) (*schema.Message, error) {
	if backend == nil {
		return nil, chatError(foundation.ErrorDependencyUnavailable, ErrorCodeChatCapabilityUnavailable, false, errChatCapabilityOff)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	output, err := backend.Generate(ctx, cloneEinoMessages(input), opts...)
	if err != nil {
		return nil, normalizeEinoRuntimeError(ctx, err)
	}
	if err := validateRuntimeGeneratedMessage(output); err != nil {
		return nil, err
	}
	return output, nil
}

func (model *EinoRuntimeChatModel) streamWithBackend(ctx context.Context, backend einoModel.BaseChatModel, input []*schema.Message, opts ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	if backend == nil {
		return nil, chatError(foundation.ErrorDependencyUnavailable, ErrorCodeChatCapabilityUnavailable, false, errChatCapabilityOff)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	toolCalls := newRuntimeStreamToolCallValidator()
	opts = append(opts, einoopenai.WithResponseChunkMessageModifier(model.runtimeChunkValidator(toolCalls)))
	stream, err := backend.Stream(ctx, cloneEinoMessages(input), opts...)
	if err != nil {
		return nil, normalizeEinoRuntimeError(ctx, err)
	}
	return validateRuntimeStream(stream, toolCalls), nil
}

func validateRuntimeStream(stream *schema.StreamReader[*schema.Message], toolCalls *runtimeStreamToolCallValidator) *schema.StreamReader[*schema.Message] {
	usageSeen := false
	return schema.StreamReaderWithConvert(stream, func(message *schema.Message) (*schema.Message, error) {
		if err := validateRuntimeStreamMessage(message); err != nil {
			return nil, err
		}
		if message.ResponseMeta != nil && message.ResponseMeta.FinishReason == "tool_calls" {
			if err := toolCalls.validateFinal(); err != nil {
				return nil, err
			}
		}
		if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
			if usageSeen {
				return nil, chatResponseError(ErrorCodeChatResponseInvalid)
			}
			usageSeen = true
		}
		return message, nil
	})
}

func (model *EinoRuntimeChatModel) runtimeChunkValidator(toolCalls *runtimeStreamToolCallValidator) einoopenai.ResponseChunkMessageModifier {
	return func(_ context.Context, message *schema.Message, raw []byte, end bool) (*schema.Message, error) {
		if end {
			if err := toolCalls.validateFinal(); err != nil {
				// eino-ext currently loses modifier errors raised at EOF. Return a
				// deliberately invalid chunk so the project stream boundary emits the
				// stable response-invalid error instead.
				return &schema.Message{Role: schema.System}, nil
			}
			return message, nil
		}
		var envelope struct {
			Model string `json:"model"`
		}
		if len(raw) == 0 || json.Unmarshal(raw, &envelope) != nil || envelope.Model != model.http.contract.Model.ModelVersion {
			if envelope.Model != "" && envelope.Model != model.http.contract.Model.ModelVersion {
				return nil, chatResponseError(ErrorCodeChatResponseModelMismatch)
			}
			return nil, chatResponseError(ErrorCodeChatResponseInvalid)
		}
		if err := toolCalls.add(message.ToolCalls); err != nil {
			return nil, err
		}
		return message, nil
	}
}

func cloneEinoMessages(input []*schema.Message) []*schema.Message {
	output := make([]*schema.Message, len(input))
	for index, message := range input {
		if message == nil {
			continue
		}
		cloned := *message
		cloned.ToolCalls = append([]schema.ToolCall(nil), message.ToolCalls...)
		output[index] = &cloned
	}
	return output
}

func cloneToolInfos(input []*schema.ToolInfo) []*schema.ToolInfo {
	output := make([]*schema.ToolInfo, len(input))
	for index, tool := range input {
		if tool == nil {
			continue
		}
		cloned := *tool
		output[index] = &cloned
	}
	return output
}

func validateRuntimeGeneratedMessage(message *schema.Message) error {
	if message == nil || message.Role != schema.Assistant || message.ResponseMeta == nil ||
		(message.Content == "" && len(message.ToolCalls) == 0) || len(message.ToolCalls) > maxRuntimeToolCalls {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	if message.ResponseMeta.FinishReason != "stop" && message.ResponseMeta.FinishReason != "tool_calls" && message.ResponseMeta.FinishReason != "" {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	if err := validateRuntimeToolCalls(message.ToolCalls); err != nil {
		return err
	}
	if len(message.ToolCalls) == 0 && message.Content == "" {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	return nil
}

func validateRuntimeStreamMessage(message *schema.Message) error {
	if message == nil {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	if message.Role != "" && message.Role != schema.Assistant {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	if !utf8.ValidString(message.Content) || len(message.Content) > agentapplication.MaxChatMessageBytes {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
		usage := message.ResponseMeta.Usage
		if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens <= 0 || usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
			return chatResponseError(ErrorCodeChatResponseInvalid)
		}
	}
	return nil
}

func validateRuntimeToolCalls(calls []schema.ToolCall) error {
	if len(calls) > maxRuntimeToolCalls {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" || len(call.ID) > maxRuntimeToolID || call.Type != "function" ||
			strings.TrimSpace(call.Function.Name) == "" || len(call.Function.Name) > maxRuntimeToolName ||
			!runtimeJSONObject([]byte(call.Function.Arguments)) || !json.Valid([]byte(call.Function.Arguments)) {
			return chatResponseError(ErrorCodeChatResponseInvalid)
		}
		if _, exists := seen[call.ID]; exists {
			return chatResponseError(ErrorCodeChatResponseInvalid)
		}
		seen[call.ID] = struct{}{}
	}
	return nil
}

type runtimeStreamToolCall struct {
	id        string
	typeName  string
	function  string
	arguments strings.Builder
}

type runtimeStreamToolCallValidator struct {
	mu    sync.Mutex
	calls map[int]*runtimeStreamToolCall
}

func newRuntimeStreamToolCallValidator() *runtimeStreamToolCallValidator {
	return &runtimeStreamToolCallValidator{calls: make(map[int]*runtimeStreamToolCall)}
}

func (validator *runtimeStreamToolCallValidator) add(calls []schema.ToolCall) error {
	validator.mu.Lock()
	defer validator.mu.Unlock()

	seen := make(map[int]struct{}, len(calls))
	for _, call := range calls {
		if call.Index == nil || *call.Index < 0 {
			return chatResponseError(ErrorCodeChatResponseInvalid)
		}
		index := *call.Index
		if _, duplicate := seen[index]; duplicate {
			return chatResponseError(ErrorCodeChatResponseInvalid)
		}
		seen[index] = struct{}{}

		current, exists := validator.calls[index]
		if !exists {
			if len(validator.calls) >= maxRuntimeToolCalls {
				return chatResponseError(ErrorCodeChatResponseInvalid)
			}
			current = &runtimeStreamToolCall{}
			validator.calls[index] = current
		}
		if err := current.add(call); err != nil {
			return err
		}
	}
	return nil
}

func (call *runtimeStreamToolCall) add(chunk schema.ToolCall) error {
	if chunk.ID != "" {
		if strings.TrimSpace(chunk.ID) == "" || len(chunk.ID) > maxRuntimeToolID || (call.id != "" && call.id != chunk.ID) {
			return chatResponseError(ErrorCodeChatResponseInvalid)
		}
		call.id = chunk.ID
	}
	if chunk.Type != "" {
		if chunk.Type != "function" || (call.typeName != "" && call.typeName != chunk.Type) {
			return chatResponseError(ErrorCodeChatResponseInvalid)
		}
		call.typeName = chunk.Type
	}
	if chunk.Function.Name != "" {
		if strings.TrimSpace(chunk.Function.Name) == "" || len(chunk.Function.Name) > maxRuntimeToolName ||
			(call.function != "" && call.function != chunk.Function.Name) {
			return chatResponseError(ErrorCodeChatResponseInvalid)
		}
		call.function = chunk.Function.Name
	}
	if !utf8.ValidString(chunk.Function.Arguments) || call.arguments.Len() > agentapplication.MaxChatMessageBytes-len(chunk.Function.Arguments) {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	call.arguments.WriteString(chunk.Function.Arguments)
	return nil
}

func (validator *runtimeStreamToolCallValidator) validateFinal() error {
	validator.mu.Lock()
	defer validator.mu.Unlock()

	calls := make([]schema.ToolCall, 0, len(validator.calls))
	for _, call := range validator.calls {
		calls = append(calls, schema.ToolCall{
			ID:   call.id,
			Type: call.typeName,
			Function: schema.FunctionCall{
				Name:      call.function,
				Arguments: call.arguments.String(),
			},
		})
	}
	return validateRuntimeToolCalls(calls)
}

type einoRuntimeRoundTripper struct {
	base             http.RoundTripper
	model            string
	maxRequestBytes  int64
	maxResponseBytes int64
}

func (transport *einoRuntimeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport == nil || transport.base == nil || request == nil || request.Body == nil {
		return nil, &einoRuntimeWireError{code: ErrorCodeChatRequestInvalid}
	}
	requestCopy := request.Clone(request.Context())
	body, err := io.ReadAll(io.LimitReader(request.Body, transport.maxRequestBytes+1))
	_ = request.Body.Close()
	if err != nil {
		return nil, &einoRuntimeReadError{cause: err}
	}
	if int64(len(body)) > transport.maxRequestBytes || !utf8.Valid(body) {
		return nil, &einoRuntimeWireError{code: ErrorCodeChatRequestInvalid}
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return nil, &einoRuntimeWireError{code: ErrorCodeChatRequestInvalid}
	}
	payload["model"], _ = json.Marshal(transport.model)
	body, err = json.Marshal(payload)
	if err != nil || int64(len(body)) > transport.maxRequestBytes {
		return nil, &einoRuntimeWireError{code: ErrorCodeChatRequestInvalid}
	}
	requestCopy.Body = io.NopCloser(bytes.NewReader(body))
	requestCopy.ContentLength = int64(len(body))
	requestCopy.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	response, err := transport.base.RoundTrip(requestCopy)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxEinoChatErrorBodyBytes))
		_ = response.Body.Close()
		return nil, &einoRuntimeStatusError{statusCode: response.StatusCode}
	}
	mediaType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil {
		_ = response.Body.Close()
		return nil, &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
	}
	streaming := isStreamingRequest(body)
	if streaming {
		if mediaType != "text/event-stream" {
			_ = response.Body.Close()
			return nil, &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
		}
		if response.ContentLength > transport.maxResponseBytes {
			_ = response.Body.Close()
			return nil, &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
		}
		bounded := &einoRuntimeBoundedBody{ReadCloser: response.Body, limit: transport.maxResponseBytes}
		response.Body = newEinoRuntimeSSEBody(bounded)
		return response, nil
	}
	if mediaType != "application/json" || response.ContentLength > transport.maxResponseBytes {
		_ = response.Body.Close()
		return nil, &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
	}
	original := response.Body
	defer original.Close()
	raw, err := io.ReadAll(io.LimitReader(original, transport.maxResponseBytes+1))
	if err != nil {
		return nil, &einoRuntimeReadError{cause: err}
	}
	if int64(len(raw)) > transport.maxResponseBytes || !utf8.Valid(raw) {
		return nil, &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
	}
	if err := validateRuntimeWireResponse(raw, transport.model); err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	response.ContentLength = int64(len(raw))
	return response, nil
}

func isStreamingRequest(body []byte) bool {
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	var stream bool
	_ = json.Unmarshal(payload["stream"], &stream)
	return stream
}

type runtimeWireResponse struct {
	Model   string              `json:"model"`
	Choices []runtimeWireChoice `json:"choices"`
	Usage   *runtimeWireUsage   `json:"usage"`
}

type runtimeWireChoice struct {
	Index        int                `json:"index"`
	Message      runtimeWireMessage `json:"message"`
	FinishReason string             `json:"finish_reason"`
}

type runtimeWireMessage struct {
	Role      string            `json:"role"`
	Content   *string           `json:"content"`
	ToolCalls []runtimeWireCall `json:"tool_calls"`
}

type runtimeWireCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function runtimeWireFunction `json:"function"`
}

type runtimeWireFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type runtimeWireUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

func validateRuntimeWireResponse(raw []byte, model string) error {
	var response runtimeWireResponse
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	if err := decoder.Decode(&response); err != nil {
		return &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
	}
	if response.Model != model || len(response.Choices) != 1 || response.Choices[0].Index != 0 || response.Choices[0].Message.Role != "assistant" {
		if response.Model != model {
			return &einoRuntimeWireError{code: ErrorCodeChatResponseModelMismatch}
		}
		return &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
	}
	choice := response.Choices[0]
	if choice.FinishReason != "stop" && choice.FinishReason != "tool_calls" && choice.FinishReason != "" {
		return &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
	}
	if choice.Message.Content == nil && len(choice.Message.ToolCalls) == 0 {
		return &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
	}
	seen := make(map[string]struct{}, len(choice.Message.ToolCalls))
	for _, call := range choice.Message.ToolCalls {
		if call.Type != "function" || call.ID == "" || len(call.ID) > maxRuntimeToolID || call.Function.Name == "" || len(call.Function.Name) > maxRuntimeToolName || !runtimeJSONObject([]byte(call.Function.Arguments)) || !json.Valid([]byte(call.Function.Arguments)) {
			return &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
		}
		if _, exists := seen[call.ID]; exists {
			return &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
		}
		seen[call.ID] = struct{}{}
	}
	if response.Usage != nil {
		if response.Usage.PromptTokens < 0 || response.Usage.CompletionTokens < 0 || response.Usage.TotalTokens <= 0 || response.Usage.TotalTokens != response.Usage.PromptTokens+response.Usage.CompletionTokens {
			return &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
		}
	}
	return nil
}

func runtimeJSONObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

type einoRuntimeStatusError struct{ statusCode int }

func (err *einoRuntimeStatusError) Error() string {
	return "eino runtime provider returned a rejected status"
}

type einoRuntimeWireError struct{ code string }

func (err *einoRuntimeWireError) Error() string {
	return "eino runtime provider response failed project validation"
}

type einoRuntimeReadError struct{ cause error }

func (err *einoRuntimeReadError) Error() string { return "eino runtime provider response read failed" }
func (err *einoRuntimeReadError) Unwrap() error { return err.cause }

type einoRuntimeBoundedBody struct {
	io.ReadCloser
	limit int64
	read  int64
}

type einoRuntimeSSEBody struct {
	source    io.ReadCloser
	reader    *bufio.Reader
	ready     bytes.Buffer
	event     bytes.Buffer
	data      []string
	usageSeen bool
	done      bool
}

func newEinoRuntimeSSEBody(source io.ReadCloser) *einoRuntimeSSEBody {
	return &einoRuntimeSSEBody{source: source, reader: bufio.NewReader(source)}
}

func (body *einoRuntimeSSEBody) Read(buffer []byte) (int, error) {
	if body == nil || body.source == nil || body.reader == nil {
		return 0, io.EOF
	}
	for body.ready.Len() == 0 && !body.done {
		line, readErr := body.reader.ReadString('\n')
		if line != "" {
			body.event.WriteString(line)
			value := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			switch {
			case value == "":
				if err := body.finishEvent(); err != nil {
					return 0, err
				}
			case strings.HasPrefix(value, "data:"):
				value = strings.TrimPrefix(value, "data:")
				value = strings.TrimPrefix(value, " ")
				body.data = append(body.data, value)
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return 0, &einoRuntimeReadError{cause: readErr}
			}
			if body.event.Len() != 0 {
				if err := body.finishEvent(); err != nil {
					return 0, err
				}
			}
			body.done = true
		}
	}
	if body.ready.Len() != 0 {
		return body.ready.Read(buffer)
	}
	return 0, io.EOF
}

func (body *einoRuntimeSSEBody) Close() error {
	if body == nil || body.source == nil {
		return nil
	}
	return body.source.Close()
}

func (body *einoRuntimeSSEBody) finishEvent() error {
	data := strings.TrimSpace(strings.Join(body.data, "\n"))
	if data != "" && data != "[DONE]" {
		var envelope map[string]json.RawMessage
		if json.Unmarshal([]byte(data), &envelope) != nil || envelope == nil {
			return chatResponseError(ErrorCodeChatResponseInvalid)
		}
		if raw, exists := envelope["usage"]; exists && string(bytes.TrimSpace(raw)) != "null" {
			var usage runtimeWireUsage
			if body.usageSeen || json.Unmarshal(raw, &usage) != nil || usage.PromptTokens < 0 ||
				usage.CompletionTokens < 0 || usage.TotalTokens <= 0 || usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
				return chatResponseError(ErrorCodeChatResponseInvalid)
			}
			body.usageSeen = true
		}
	}
	body.ready.Write(body.event.Bytes())
	body.event.Reset()
	body.data = body.data[:0]
	return nil
}

func (body *einoRuntimeBoundedBody) Read(buffer []byte) (int, error) {
	if body == nil || body.ReadCloser == nil {
		return 0, io.EOF
	}
	remainingWithProbe := body.limit - body.read + 1
	if remainingWithProbe <= 0 {
		return 0, &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
	}
	if int64(len(buffer)) > remainingWithProbe {
		buffer = buffer[:remainingWithProbe]
	}
	count, err := body.ReadCloser.Read(buffer)
	body.read += int64(count)
	if body.read > body.limit {
		return 0, &einoRuntimeWireError{code: ErrorCodeChatResponseInvalid}
	}
	return count, err
}

func normalizeEinoRuntimeError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return classifyChatTransportError(ctx, err)
	}
	var status *einoRuntimeStatusError
	if errors.As(err, &status) {
		return classifyChatStatus(status.statusCode)
	}
	var wire *einoRuntimeWireError
	if errors.As(err, &wire) {
		return chatResponseError(wire.code)
	}
	return classifyChatTransportError(ctx, err)
}

var _ einoModel.ToolCallingChatModel = (*EinoRuntimeChatModel)(nil)
var _ einoModel.ToolCallingChatModel = (*einoRuntimeToolModel)(nil)
var _ http.RoundTripper = (*einoRuntimeRoundTripper)(nil)
