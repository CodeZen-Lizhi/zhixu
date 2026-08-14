package models

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	maxEinoChatErrorBodyBytes = int64(4096)
	einoChatSDKModel          = "zhixu-openai-compatible-chat"
)

// EinoOpenAIChatModel 通过 Eino OpenAI extension 实现项目 ChatModel Port。
type EinoOpenAIChatModel struct {
	http      chatHTTPConfig
	backend   *einoopenai.ChatModel
	telemetry ModelTelemetry
}

// NewEinoOpenAIChatModel 创建复用项目安全传输、严格响应合同和可选项目 telemetry 的 Eino Chat Adapter。
func NewEinoOpenAIChatModel(options OpenAIChatOptions, telemetry ...ModelTelemetry) (*EinoOpenAIChatModel, error) {
	config, err := newChatHTTPConfig(chatHTTPOptions{
		client: options.Client, baseURL: options.BaseURL, apiKey: options.APIKey,
		model: options.Model, modelVersion: options.ModelVersion, adapterVersion: options.AdapterVersion, timeout: options.Timeout,
		maxRequestBytes: options.MaxRequestBytes, maxResponseBytes: options.MaxResponseBytes,
	})
	if err != nil {
		return nil, err
	}
	config.client.Transport = &einoChatRoundTripper{
		base:             config.client.Transport,
		model:            config.contract.Model,
		maxResponseBytes: config.maxResponseBytes,
	}
	backend, err := einoopenai.NewChatModel(context.Background(), &einoopenai.ChatModelConfig{
		APIKey:  options.APIKey,
		BaseURL: einoChatSDKBaseURL(config.endpointURL),
		// The project payload modifier owns the wire model. A neutral SDK model
		// avoids go-openai's endpoint-specific legacy-model denylist.
		Model:      einoChatSDKModel,
		HTTPClient: config.client,
	})
	if err != nil {
		return nil, chatConfigErrorWithCause(errors.New("eino chat adapter initialization failed"))
	}
	return &EinoOpenAIChatModel{http: config, backend: backend, telemetry: resolveModelTelemetry(telemetry)}, nil
}

// Contract 返回不含 Credential/Endpoint 的冻结项目契约。
func (model *EinoOpenAIChatModel) Contract() ChatContract {
	if model == nil {
		return ChatContract{}
	}
	return model.http.contractCopy()
}

// Chat 执行一次 Eino Generate；Schema 和响应捕获均为请求级状态。
func (model *EinoOpenAIChatModel) Chat(ctx context.Context, request agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	if model == nil || model.backend == nil {
		return agentapplication.ChatResponse{}, chatError(foundation.ErrorDependencyUnavailable, ErrorCodeChatCapabilityUnavailable, false, errChatCapabilityOff)
	}
	if err := model.http.validateRequest(request); err != nil {
		return agentapplication.ChatResponse{}, err
	}
	payload, err := model.http.encodeRequest(buildOpenAIChatRequest(model.http.contract.Model.ModelID, request))
	if err != nil {
		return agentapplication.ChatResponse{}, err
	}
	messages := make([]*schema.Message, len(request.Messages))
	for index, message := range request.Messages {
		messages[index] = &schema.Message{Role: einoChatRole(message.Role), Content: message.Content}
	}

	requestContext, cancel := model.http.requestContext(ctx)
	defer cancel()
	state := &einoChatCallState{}
	requestContext = context.WithValue(requestContext, einoChatCallStateContextKey{}, state)
	callbackCall := newEinoChatCallbackCall(model.telemetry, request.Phase)
	requestContext = callbackCall.contextWithHandler(requestContext)
	output, err := model.backend.Generate(
		requestContext,
		messages,
		einomodel.WithMaxTokens(request.MaxOutputTokens),
		einoopenai.WithExtraHeader(map[string]string{"Accept": "application/json"}),
		einoopenai.WithRequestPayloadModifier(func(context.Context, []*schema.Message, []byte) ([]byte, error) {
			return append([]byte(nil), payload...), nil
		}),
		einoopenai.WithResponseMessageModifier(func(_ context.Context, message *schema.Message, rawBody []byte) (*schema.Message, error) {
			if err := validateEinoChatMessage(message, rawBody, state); err != nil {
				return nil, err
			}
			state.modifierValidated = true
			return message, nil
		}),
	)
	if err != nil {
		classified := classifyEinoChatError(requestContext, err, state.wireValidated)
		callbackCall.finishError(requestContext, classified)
		return agentapplication.ChatResponse{}, classified
	}
	if output == nil || !state.wireValidated || !state.modifierValidated {
		classified := chatResponseError(ErrorCodeChatResponseInvalid)
		callbackCall.finishError(requestContext, classified)
		return agentapplication.ChatResponse{}, classified
	}
	if err := agentapplication.ValidateChatResponse(request, state.response); err != nil {
		callbackCall.finishError(requestContext, err)
		return agentapplication.ChatResponse{}, err
	}
	callbackCall.finishSuccess(requestContext)
	return state.response, nil
}

// String 返回不含 Endpoint、Credential、Prompt 或原始响应的 Adapter 摘要。
func (model *EinoOpenAIChatModel) String() string {
	if model == nil {
		return "eino openai chat adapter(unavailable)"
	}
	return "eino-backed " + safeChatAdapterString(model.http.contract.Model)
}

// GoString 避免 `%#v` 展开 Eino client 与私有 HTTP 配置。
func (model *EinoOpenAIChatModel) GoString() string { return model.String() }

func einoChatSDKBaseURL(endpointURL string) string {
	return strings.TrimSuffix(endpointURL, "/chat/completions")
}

func einoChatRole(role agentapplication.MessageRole) schema.RoleType {
	switch role {
	case agentapplication.MessageRoleSystem:
		return schema.System
	case agentapplication.MessageRoleAssistant:
		return schema.Assistant
	default:
		return schema.User
	}
}

type einoChatCallStateContextKey struct{}

type einoChatCallState struct {
	response          agentapplication.ChatResponse
	bodyDigest        [sha256.Size]byte
	bodyBytes         int
	wireValidated     bool
	modifierValidated bool
}

type einoChatRoundTripper struct {
	base             http.RoundTripper
	model            agentdomain.ModelRef
	maxResponseBytes int64
}

func (transport *einoChatRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport == nil || transport.base == nil || request == nil {
		return nil, &einoChatWireError{code: ErrorCodeChatResponseInvalid}
	}
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, &einoChatWireError{code: ErrorCodeChatResponseInvalid}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxEinoChatErrorBodyBytes))
		_ = response.Body.Close()
		return nil, &einoChatStatusError{statusCode: response.StatusCode}
	}
	originalBody := response.Body
	defer originalBody.Close()
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, &einoChatWireError{code: ErrorCodeChatResponseInvalid}
	}
	if response.ContentLength > transport.maxResponseBytes {
		return nil, &einoChatWireError{code: ErrorCodeChatResponseInvalid}
	}
	rawBody, err := io.ReadAll(io.LimitReader(originalBody, transport.maxResponseBytes+1))
	if err != nil {
		return nil, &einoChatReadError{cause: err}
	}
	if int64(len(rawBody)) > transport.maxResponseBytes || !utf8.Valid(rawBody) {
		return nil, &einoChatWireError{code: ErrorCodeChatResponseInvalid}
	}
	var providerResponse openAIChatResponse
	if err := decodeChatJSONResponse(rawBody, transport.maxResponseBytes, &providerResponse); err != nil {
		return nil, newEinoChatWireError(err)
	}
	projectResponse, err := validateOpenAIChatResponse(transport.model, providerResponse)
	if err != nil {
		return nil, newEinoChatWireError(err)
	}
	state, ok := request.Context().Value(einoChatCallStateContextKey{}).(*einoChatCallState)
	if !ok || state == nil {
		return nil, &einoChatWireError{code: ErrorCodeChatResponseInvalid}
	}
	normalizedBody := bytes.TrimSpace(rawBody)
	state.response = projectResponse
	state.bodyDigest = sha256.Sum256(normalizedBody)
	state.bodyBytes = len(normalizedBody)
	state.wireValidated = true
	response.Body = io.NopCloser(bytes.NewReader(rawBody))
	response.ContentLength = int64(len(rawBody))
	return response, nil
}

func validateEinoChatMessage(message *schema.Message, rawBody []byte, state *einoChatCallState) error {
	normalizedBody := bytes.TrimSpace(rawBody)
	if message == nil || state == nil || !state.wireValidated || len(normalizedBody) != state.bodyBytes || sha256.Sum256(normalizedBody) != state.bodyDigest ||
		message.Role != schema.Assistant || message.Content != string(state.response.Content) || len(message.ToolCalls) != 0 || message.ResponseMeta == nil ||
		message.ResponseMeta.FinishReason != "stop" || message.ResponseMeta.Usage == nil {
		return &einoChatWireError{code: ErrorCodeChatResponseInvalid}
	}
	usage := message.ResponseMeta.Usage
	if int64(usage.PromptTokens) != state.response.Usage.InputTokens || int64(usage.CompletionTokens) != state.response.Usage.OutputTokens ||
		int64(usage.TotalTokens) != state.response.Usage.TotalTokens {
		return &einoChatWireError{code: ErrorCodeChatResponseInvalid}
	}
	return nil
}

type einoChatStatusError struct {
	statusCode int
}

func (err *einoChatStatusError) Error() string {
	return "eino chat provider returned a rejected status"
}

type einoChatWireError struct {
	code string
}

func (err *einoChatWireError) Error() string {
	return "eino chat provider response failed project validation"
}

type einoChatReadError struct {
	cause error
}

func (err *einoChatReadError) Error() string { return "eino chat provider response read failed" }

func (err *einoChatReadError) Unwrap() error { return err.cause }

func newEinoChatWireError(err error) error {
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Code == ErrorCodeChatResponseModelMismatch {
		return &einoChatWireError{code: ErrorCodeChatResponseModelMismatch}
	}
	return &einoChatWireError{code: ErrorCodeChatResponseInvalid}
}

func classifyEinoChatError(ctx context.Context, err error, wireValidated bool) error {
	if ctx != nil && ctx.Err() != nil {
		return classifyChatTransportError(ctx, err)
	}
	var statusError *einoChatStatusError
	if errors.As(err, &statusError) {
		return classifyChatStatus(statusError.statusCode)
	}
	var wireError *einoChatWireError
	if errors.As(err, &wireError) {
		return chatResponseError(wireError.code)
	}
	if wireValidated {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	return classifyChatTransportError(ctx, err)
}

var _ agentapplication.ChatModel = (*EinoOpenAIChatModel)(nil)
var _ http.RoundTripper = (*einoChatRoundTripper)(nil)
