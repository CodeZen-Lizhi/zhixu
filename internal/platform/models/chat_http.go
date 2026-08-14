package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeChatConfigInvalid 表示 Chat Adapter 配置不满足安全边界。
	ErrorCodeChatConfigInvalid = "MODEL_CHAT_CONFIG_INVALID"
	// ErrorCodeChatCapabilityUnavailable 表示进程明确禁用了 Chat capability。
	ErrorCodeChatCapabilityUnavailable = "MODEL_CHAT_CAPABILITY_UNAVAILABLE"
	// ErrorCodeChatCancelled 表示调用方主动取消了 Provider 请求。
	ErrorCodeChatCancelled = "MODEL_CHAT_CANCELLED"
	// ErrorCodeChatTimeout 表示 Provider 调用超过有界 deadline。
	ErrorCodeChatTimeout = "MODEL_CHAT_TIMEOUT"
	// ErrorCodeChatRequestFailed 表示未被识别为可重试的传输失败。
	ErrorCodeChatRequestFailed = "MODEL_CHAT_REQUEST_FAILED"
	// ErrorCodeChatRateLimited 表示 Provider 返回 429。
	ErrorCodeChatRateLimited = "MODEL_CHAT_RATE_LIMITED"
	// ErrorCodeChatProviderUnavailable 表示 Provider 返回明确的临时网关或服务不可用状态。
	ErrorCodeChatProviderUnavailable = "MODEL_CHAT_PROVIDER_UNAVAILABLE"
	// ErrorCodeChatUnauthorized 表示 Provider 返回 401 或 403。
	ErrorCodeChatUnauthorized = "MODEL_CHAT_UNAUTHORIZED"
	// ErrorCodeChatRejected 表示 Provider 以不可重试状态拒绝请求。
	ErrorCodeChatRejected = "MODEL_CHAT_REJECTED"
	// ErrorCodeChatRedirectRejected 表示 Provider 尝试把模型请求重定向到其他地址。
	ErrorCodeChatRedirectRejected = "MODEL_CHAT_REDIRECT_REJECTED"
	// ErrorCodeChatRequestInvalid 表示调用请求或请求大小不符合 Adapter 契约。
	ErrorCodeChatRequestInvalid = "MODEL_CHAT_REQUEST_INVALID"
	// ErrorCodeChatResponseInvalid 表示 Provider 响应无法通过严格协议校验。
	ErrorCodeChatResponseInvalid = "MODEL_CHAT_RESPONSE_INVALID"
	// ErrorCodeChatResponseModelMismatch 表示 Provider 回显的模型与冻结模型不一致。
	ErrorCodeChatResponseModelMismatch = "MODEL_CHAT_RESPONSE_MODEL_MISMATCH"
)

const (
	openAIChatAdapterName = "openai-compatible-http"
	chatCompletionsPath   = "/v1/chat/completions"
	responsesPath         = "/v1/responses"
)

// ChatAPIStyle selects one explicit OpenAI-compatible Chat protocol.
type ChatAPIStyle string

const (
	ChatAPIStyleChatCompletions ChatAPIStyle = "chat_completions"
	ChatAPIStyleResponses       ChatAPIStyle = "responses"
)

var (
	errChatConfigInvalid   = errors.New("chat adapter configuration is invalid")
	errChatCapabilityOff   = errors.New("chat capability is disabled")
	errChatRequestInvalid  = errors.New("chat request is invalid")
	errChatRequestFailed   = errors.New("chat provider request failed")
	errChatResponseInvalid = errors.New("chat provider response is invalid")
)

// ChatContract 返回不含 Credential 和 Endpoint 的冻结 Adapter 契约。
type ChatContract struct {
	Provider         string
	APIStyle         ChatAPIStyle
	EndpointPath     string
	Model            agentdomain.ModelRef
	Timeout          time.Duration
	MaxRequestBytes  int64
	MaxResponseBytes int64
}

// ChatConnectionProber executes one bounded production-path request without
// exposing provider output to the activation controller.
type ChatConnectionProber interface {
	ProbeConnection(context.Context) error
}

type chatHTTPConfig struct {
	client           *modelHTTPClient
	endpointURL      string
	contract         ChatContract
	timeout          time.Duration
	maxRequestBytes  int64
	maxResponseBytes int64
}

type chatHTTPOptions struct {
	client           *http.Client
	baseURL          string
	apiKey           string
	model            string
	modelVersion     string
	adapterVersion   string
	timeout          time.Duration
	maxRequestBytes  int64
	maxResponseBytes int64
	apiStyle         ChatAPIStyle
	provider         string
}

func newChatHTTPConfig(options chatHTTPOptions) (chatHTTPConfig, error) {
	provider := options.provider
	if provider == "" {
		provider = openAICompatibleProvider
	}
	if provider != openAICompatibleProvider && provider != ollamaProvider {
		return chatHTTPConfig{}, chatConfigErrorWithCause(errors.New("chat provider is invalid"))
	}
	apiStyle := options.apiStyle
	if apiStyle == "" {
		apiStyle = ChatAPIStyleChatCompletions
	}
	endpointPath, ok := chatEndpointPath(apiStyle)
	if !ok {
		return chatHTTPConfig{}, chatConfigErrorWithCause(errors.New("chat API style is invalid"))
	}
	baseURL, err := parseChatBaseURL(options.baseURL)
	model := agentdomain.ModelRef{
		AdapterName:    chatAdapterName(provider),
		AdapterVersion: options.adapterVersion,
		ModelID:        options.model,
		ModelVersion:   options.modelVersion,
	}
	if err != nil {
		return chatHTTPConfig{}, chatConfigErrorWithCause(errors.New("chat base url is invalid"))
	}
	cleanedPath := strings.TrimRight(baseURL.Path, "/")
	if (endpointPath == responsesPath && strings.HasSuffix(cleanedPath, chatCompletionsPath)) ||
		(endpointPath == chatCompletionsPath && strings.HasSuffix(cleanedPath, responsesPath)) {
		return chatHTTPConfig{}, chatConfigErrorWithCause(errors.New("chat base url targets another API style"))
	}
	if model.Validate() != nil {
		return chatHTTPConfig{}, chatConfigErrorWithCause(errors.New("chat model identity is invalid"))
	}
	if !validChatAPIKey(options.apiKey) {
		return chatHTTPConfig{}, chatConfigErrorWithCause(errors.New("chat api key format is invalid"))
	}
	if options.timeout <= 0 || options.timeout > 5*time.Minute {
		return chatHTTPConfig{}, chatConfigErrorWithCause(errors.New("chat timeout is invalid"))
	}
	if options.maxRequestBytes <= 0 || options.maxRequestBytes > 16<<20 {
		return chatHTTPConfig{}, chatConfigErrorWithCause(errors.New("chat request byte limit is invalid"))
	}
	if options.maxResponseBytes <= 0 || options.maxResponseBytes > 16<<20 {
		return chatHTTPConfig{}, chatConfigErrorWithCause(errors.New("chat response byte limit is invalid"))
	}
	client, err := newModelHTTPClient(baseURL, options.client)
	if err != nil {
		return chatHTTPConfig{}, chatConfigErrorWithCause(err)
	}
	return chatHTTPConfig{
		client:           client,
		endpointURL:      appendChatPath(baseURL, endpointPath),
		contract:         ChatContract{Provider: provider, APIStyle: apiStyle, EndpointPath: endpointPath, Model: model, Timeout: options.timeout, MaxRequestBytes: options.maxRequestBytes, MaxResponseBytes: options.maxResponseBytes},
		timeout:          options.timeout,
		maxRequestBytes:  options.maxRequestBytes,
		maxResponseBytes: options.maxResponseBytes,
	}, nil
}

func chatAdapterName(provider string) string {
	if provider == ollamaProvider {
		return "ollama-chat-completions-http"
	}
	return openAIChatAdapterName
}

func parseChatBaseURL(raw string) (*url.URL, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 2048 {
		return nil, errChatConfigInvalid
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return nil, errChatConfigInvalid
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopbackHostname(parsed.Hostname())) {
		return nil, errChatConfigInvalid
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed, nil
}

func appendChatPath(baseURL *url.URL, endpointPath string) string {
	requestURL := *baseURL
	cleaned := strings.TrimRight(requestURL.Path, "/")
	switch {
	case strings.HasSuffix(cleaned, endpointPath):
		requestURL.Path = cleaned
	case strings.HasSuffix(cleaned, "/v1"):
		requestURL.Path = path.Join(cleaned, strings.TrimPrefix(endpointPath, "/v1/"))
	default:
		requestURL.Path = path.Join(cleaned, endpointPath)
	}
	return requestURL.String()
}

func chatEndpointPath(style ChatAPIStyle) (string, bool) {
	switch style {
	case ChatAPIStyleChatCompletions:
		return chatCompletionsPath, true
	case ChatAPIStyleResponses:
		return responsesPath, true
	default:
		return "", false
	}
}

func validateEinoChatAPIStyle(style ChatAPIStyle) error {
	if style == "" || style == ChatAPIStyleChatCompletions {
		return nil
	}
	if style == ChatAPIStyleResponses {
		return chatConfigErrorWithCause(errors.New("responses API style is unavailable in the Eino runtime"))
	}
	return chatConfigErrorWithCause(errors.New("chat API style is invalid"))
}

func validChatAPIKey(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 8192 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func (config chatHTTPConfig) contractCopy() ChatContract {
	return config.contract
}

func (config chatHTTPConfig) closeModelResource() error {
	if config.client == nil {
		return nil
	}
	return config.client.Close()
}

func (config chatHTTPConfig) validateRequest(request agentapplication.ChatRequest) error {
	if err := agentapplication.ValidateChatRequest(request); err != nil {
		return err
	}
	if request.Model != config.contract.Model {
		return chatError(foundation.ErrorConsistencyViolation, ErrorCodeChatRequestInvalid, false, errChatRequestInvalid)
	}
	return nil
}

func (config chatHTTPConfig) encodeRequest(payload any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil || int64(len(encoded)) > config.maxRequestBytes {
		return nil, chatError(foundation.ErrorInvalidInput, ErrorCodeChatRequestInvalid, false, errChatRequestInvalid)
	}
	return encoded, nil
}

func (config chatHTTPConfig) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, config.timeout)
}

func decodeChatJSONResponse(encoded []byte, maxResponseBytes int64, result any) error {
	limits := agentdomain.DecodeLimits{
		MaxDocumentBytes: int(maxResponseBytes),
		MaxDepth:         16,
		MaxStringBytes:   int(maxResponseBytes),
		MaxArrayItems:    128,
		MaxObjectFields:  128,
	}
	decoded, err := agentdomain.DecodeStrict(encoded, limits, func(value json.RawMessage) error { return nil })
	if err != nil {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	return nil
}

func classifyChatTransportError(ctx context.Context, cause error) error {
	return classifyChatTransportErrorWithStage(ctx, cause, ConnectionStageConnect)
}

func classifyChatTransportErrorWithStage(ctx context.Context, cause error, fallbackStage ConnectionStage) error {
	diagnostic := transportDiagnostic(ctx, cause, fallbackStage)
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return chatError(foundation.ErrorNonRetryableFailure, ErrorCodeChatCancelled, false, diagnostic)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return chatError(foundation.ErrorRetryableFailure, ErrorCodeChatTimeout, true, diagnostic)
	default:
		var networkError net.Error
		if errors.As(cause, &networkError) && networkError.Timeout() {
			return chatError(foundation.ErrorRetryableFailure, ErrorCodeChatTimeout, true, diagnostic)
		}
		return chatError(foundation.ErrorNonRetryableFailure, ErrorCodeChatRequestFailed, false, diagnostic)
	}
}

func classifyChatStatus(statusCode int, diagnostic ...error) error {
	cause := error(errChatRequestFailed)
	if len(diagnostic) > 0 && diagnostic[0] != nil {
		cause = diagnostic[0]
	}
	switch statusCode {
	case http.StatusTooManyRequests:
		return chatError(foundation.ErrorRetryableFailure, ErrorCodeChatRateLimited, true, cause)
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return chatError(foundation.ErrorRetryableFailure, ErrorCodeChatProviderUnavailable, true, cause)
	case http.StatusUnauthorized, http.StatusForbidden:
		return chatError(foundation.ErrorNonRetryableFailure, ErrorCodeChatUnauthorized, false, cause)
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return chatError(foundation.ErrorNonRetryableFailure, ErrorCodeChatRedirectRejected, false, cause)
	default:
		return chatError(foundation.ErrorNonRetryableFailure, ErrorCodeChatRejected, false, cause)
	}
}

func chatConfigErrorWithCause(cause error) error {
	if cause == nil {
		cause = errChatConfigInvalid
	}
	return chatError(foundation.ErrorInvalidInput, ErrorCodeChatConfigInvalid, false, cause)
}

func chatResponseError(code string, diagnostic ...error) error {
	cause := error(responseValidationDiagnostic(errChatResponseInvalid))
	if len(diagnostic) > 0 && diagnostic[0] != nil {
		cause = diagnostic[0]
	}
	return chatError(foundation.ErrorConsistencyViolation, code, false, cause)
}

func chatError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}

func safeChatAdapterString(contract ChatContract) string {
	return fmt.Sprintf("chat adapter(provider=%q api_style=%q model=%q model_version=%q adapter_version=%q)",
		contract.Provider, contract.APIStyle, contract.Model.ModelID, contract.Model.ModelVersion, contract.Model.AdapterVersion)
}
