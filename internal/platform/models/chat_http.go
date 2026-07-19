package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

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
	Model            agentdomain.ModelRef
	Timeout          time.Duration
	MaxRequestBytes  int64
	MaxResponseBytes int64
}

type chatHTTPConfig struct {
	client           *http.Client
	endpointURL      string
	contract         ChatContract
	timeout          time.Duration
	maxRequestBytes  int64
	maxResponseBytes int64
	authorization    string
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
}

func newChatHTTPConfig(options chatHTTPOptions) (chatHTTPConfig, error) {
	baseURL, err := parseChatBaseURL(options.baseURL)
	model := agentdomain.ModelRef{
		AdapterName:    openAIChatAdapterName,
		AdapterVersion: options.adapterVersion,
		ModelID:        options.model,
		ModelVersion:   options.modelVersion,
	}
	if err != nil {
		return chatHTTPConfig{}, chatConfigErrorWithCause(errors.New("chat base url is invalid"))
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
	client := options.client
	if client == nil {
		client = http.DefaultClient
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	authorization := ""
	if options.apiKey != "" {
		authorization = "Bearer " + options.apiKey
	}
	return chatHTTPConfig{
		client:           &clientCopy,
		endpointURL:      appendChatPath(baseURL),
		contract:         ChatContract{Provider: openAICompatibleProvider, Model: model, Timeout: options.timeout, MaxRequestBytes: options.maxRequestBytes, MaxResponseBytes: options.maxResponseBytes},
		timeout:          options.timeout,
		maxRequestBytes:  options.maxRequestBytes,
		maxResponseBytes: options.maxResponseBytes,
		authorization:    authorization,
	}, nil
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

func appendChatPath(baseURL *url.URL) string {
	requestURL := *baseURL
	cleaned := strings.TrimRight(requestURL.Path, "/")
	switch {
	case strings.HasSuffix(cleaned, chatCompletionsPath):
		requestURL.Path = cleaned
	case strings.HasSuffix(cleaned, "/v1"):
		requestURL.Path = path.Join(cleaned, "chat/completions")
	default:
		requestURL.Path = path.Join(cleaned, chatCompletionsPath)
	}
	return requestURL.String()
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

func (config chatHTTPConfig) chat(ctx context.Context, request agentapplication.ChatRequest, payload any, result any) error {
	if err := agentapplication.ValidateChatRequest(request); err != nil {
		return err
	}
	if request.Model != config.contract.Model {
		return chatError(foundation.ErrorConsistencyViolation, ErrorCodeChatRequestInvalid, false, errChatRequestInvalid)
	}
	encoded, err := json.Marshal(payload)
	if err != nil || int64(len(encoded)) > config.maxRequestBytes {
		return chatError(foundation.ErrorInvalidInput, ErrorCodeChatRequestInvalid, false, errChatRequestInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestContext, cancel := context.WithTimeout(ctx, config.timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost, config.endpointURL, bytes.NewReader(encoded))
	if err != nil {
		return chatError(foundation.ErrorNonRetryableFailure, ErrorCodeChatRequestFailed, false, errChatRequestFailed)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	if config.authorization != "" {
		httpRequest.Header.Set("Authorization", config.authorization)
	}
	response, err := config.client.Do(httpRequest)
	if err != nil {
		return classifyChatTransportError(requestContext, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return classifyChatStatus(response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	encodedResponse, err := io.ReadAll(io.LimitReader(response.Body, config.maxResponseBytes+1))
	if err != nil {
		return classifyChatTransportError(requestContext, err)
	}
	if int64(len(encodedResponse)) > config.maxResponseBytes || !utf8.Valid(encodedResponse) {
		return chatResponseError(ErrorCodeChatResponseInvalid)
	}
	limits := agentdomain.DecodeLimits{
		MaxDocumentBytes: int(config.maxResponseBytes),
		MaxDepth:         16,
		MaxStringBytes:   int(config.maxResponseBytes),
		MaxArrayItems:    128,
		MaxObjectFields:  128,
	}
	decoded, err := agentdomain.DecodeStrict(encodedResponse, limits, func(value json.RawMessage) error { return nil })
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
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return chatError(foundation.ErrorNonRetryableFailure, ErrorCodeChatCancelled, false, context.Canceled)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return chatError(foundation.ErrorRetryableFailure, ErrorCodeChatTimeout, true, context.DeadlineExceeded)
	default:
		var networkError net.Error
		if errors.As(cause, &networkError) && networkError.Timeout() {
			return chatError(foundation.ErrorRetryableFailure, ErrorCodeChatTimeout, true, errChatRequestFailed)
		}
		return chatError(foundation.ErrorNonRetryableFailure, ErrorCodeChatRequestFailed, false, errChatRequestFailed)
	}
}

func classifyChatStatus(statusCode int) error {
	switch statusCode {
	case http.StatusTooManyRequests:
		return chatError(foundation.ErrorRetryableFailure, ErrorCodeChatRateLimited, true, errChatRequestFailed)
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return chatError(foundation.ErrorRetryableFailure, ErrorCodeChatProviderUnavailable, true, errChatRequestFailed)
	case http.StatusUnauthorized, http.StatusForbidden:
		return chatError(foundation.ErrorNonRetryableFailure, ErrorCodeChatUnauthorized, false, errChatRequestFailed)
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return chatError(foundation.ErrorNonRetryableFailure, ErrorCodeChatRedirectRejected, false, errChatRequestFailed)
	default:
		return chatError(foundation.ErrorNonRetryableFailure, ErrorCodeChatRejected, false, errChatRequestFailed)
	}
}

func chatConfigErrorWithCause(cause error) error {
	if cause == nil {
		cause = errChatConfigInvalid
	}
	return chatError(foundation.ErrorInvalidInput, ErrorCodeChatConfigInvalid, false, cause)
}

func chatResponseError(code string) error {
	return chatError(foundation.ErrorConsistencyViolation, code, false, errChatResponseInvalid)
}

func chatError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}

func safeChatAdapterString(model agentdomain.ModelRef) string {
	return fmt.Sprintf("openai-compatible chat adapter(model=%q model_version=%q adapter_version=%q)", model.ModelID, model.ModelVersion, model.AdapterVersion)
}
