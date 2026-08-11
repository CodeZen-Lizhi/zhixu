// Package models 提供项目自有模型端口的直接 HTTP Adapter。
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

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	// ErrorCodeEmbeddingConfigInvalid 表示 Adapter 启动配置不满足安全或运行时契约。
	ErrorCodeEmbeddingConfigInvalid = "MODEL_EMBEDDING_CONFIG_INVALID"
	// ErrorCodeEmbeddingCancelled 表示调用方主动取消了尚未完成的 Provider 请求。
	ErrorCodeEmbeddingCancelled = "MODEL_EMBEDDING_CANCELLED"
	// ErrorCodeEmbeddingTimeout 表示 Provider 请求超过了调用边界的时限。
	ErrorCodeEmbeddingTimeout = "MODEL_EMBEDDING_TIMEOUT"
	// ErrorCodeEmbeddingRequestFailed 表示网络或连接层临时失败。
	ErrorCodeEmbeddingRequestFailed = "MODEL_EMBEDDING_REQUEST_FAILED"
	// ErrorCodeEmbeddingRateLimited 表示 Provider 返回限流状态。
	ErrorCodeEmbeddingRateLimited = "MODEL_EMBEDDING_RATE_LIMITED"
	// ErrorCodeEmbeddingUnavailable 表示 Provider 返回服务端故障状态。
	ErrorCodeEmbeddingUnavailable = "MODEL_EMBEDDING_UNAVAILABLE"
	// ErrorCodeEmbeddingUnauthorized 表示 Provider 拒绝当前 Credential。
	ErrorCodeEmbeddingUnauthorized = "MODEL_EMBEDDING_UNAUTHORIZED"
	// ErrorCodeEmbeddingRejected 表示 Provider 以不可重试的客户端状态拒绝请求。
	ErrorCodeEmbeddingRejected = "MODEL_EMBEDDING_REJECTED"

	defaultEmbeddingTimeout       = 30 * time.Second
	defaultEmbeddingResponseBytes = int64(64 << 20)
	maxEmbeddingResponseBytes     = int64(128 << 20)
)

var (
	errEmbeddingConfigInvalid   = errors.New("embedding adapter configuration is invalid")
	errEmbeddingRequestFailed   = errors.New("embedding provider request failed")
	errEmbeddingResponseInvalid = errors.New("embedding provider response is invalid")
)

type embeddingHTTPConfig struct {
	client           *modelHTTPClient
	endpointURL      string
	contract         domain.EmbeddingContract
	timeout          time.Duration
	maxResponseBytes int64
	authorization    string
}

type embeddingHTTPOptions struct {
	client             *http.Client
	baseURL            string
	authorization      string
	model              string
	dimensions         int32
	normalization      domain.EmbeddingNormalization
	distanceMetric     domain.DistanceMetric
	maxBatchSize       int32
	maxInputBytes      int32
	maxBatchInputBytes int64
	timeout            time.Duration
	maxResponseBytes   int64
}

func newEmbeddingHTTPConfig(provider, requestPath string, allowLoopbackHTTP bool, options embeddingHTTPOptions) (embeddingHTTPConfig, error) {
	baseURL, endpointIdentity, err := parseEmbeddingBaseURL(options.baseURL, allowLoopbackHTTP)
	if err != nil || strings.TrimSpace(options.model) != options.model || options.model == "" || options.timeout < 0 || options.maxResponseBytes < 0 {
		return embeddingHTTPConfig{}, embeddingConfigError()
	}
	timeout := options.timeout
	if timeout == 0 {
		timeout = defaultEmbeddingTimeout
	}
	maxResponseBytes := options.maxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultEmbeddingResponseBytes
	}
	if maxResponseBytes > maxEmbeddingResponseBytes {
		return embeddingHTTPConfig{}, embeddingConfigError()
	}
	contract := domain.EmbeddingContract{
		Provider:           provider,
		AdapterName:        "direct-http",
		AdapterVersion:     "v1",
		Model:              options.model,
		Dimensions:         options.dimensions,
		Normalization:      options.normalization,
		DistanceMetric:     options.distanceMetric,
		EndpointIdentity:   endpointIdentity,
		MaxBatchSize:       options.maxBatchSize,
		MaxInputBytes:      options.maxInputBytes,
		MaxBatchInputBytes: options.maxBatchInputBytes,
	}
	contract.ConfigHash, err = domain.ComputeEmbeddingConfigHash(contract)
	if err != nil || domain.ValidateEmbeddingContract(contract) != nil {
		return embeddingHTTPConfig{}, embeddingConfigError()
	}
	client, err := newModelHTTPClient(baseURL, options.client)
	if err != nil {
		return embeddingHTTPConfig{}, embeddingConfigError()
	}
	return embeddingHTTPConfig{
		client:           client,
		endpointURL:      appendEmbeddingPath(baseURL, requestPath),
		contract:         contract,
		timeout:          timeout,
		maxResponseBytes: maxResponseBytes,
		authorization:    options.authorization,
	}, nil
}

func parseEmbeddingBaseURL(raw string, allowLoopbackHTTP bool) (*url.URL, string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 2048 {
		return nil, "", errEmbeddingConfigInvalid
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return nil, "", errEmbeddingConfigInvalid
	}
	if parsed.Scheme != "https" {
		if parsed.Scheme != "http" || !allowLoopbackHTTP || !isLoopbackHostname(parsed.Hostname()) {
			return nil, "", errEmbeddingConfigInvalid
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed, parsed.String(), nil
}

func isLoopbackHostname(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	address := net.ParseIP(hostname)
	return address != nil && address.IsLoopback()
}

func appendEmbeddingPath(baseURL *url.URL, requestPath string) string {
	requestURL := *baseURL
	cleaned := strings.TrimRight(requestURL.Path, "/")
	switch {
	case strings.HasSuffix(cleaned, "/v1/embeddings"):
		requestURL.Path = cleaned
	case strings.HasSuffix(cleaned, "/v1"):
		requestURL.Path = path.Join(cleaned, "embeddings")
	default:
		requestURL.Path = path.Join(cleaned, requestPath)
	}
	return requestURL.String()
}

func (config embeddingHTTPConfig) contractCopy() domain.EmbeddingContract {
	return config.contract
}

func (config embeddingHTTPConfig) closeModelResource() error {
	if config.client == nil {
		return nil
	}
	return config.client.Close()
}

func (config embeddingHTTPConfig) embed(ctx context.Context, request application.EmbedRequest, payload, result any) error {
	if err := application.ValidateEmbedRequest(config.contract, request); err != nil {
		return err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbedResultInvalid, false, errEmbeddingRequestFailed)
	}
	requestContext, cancel := context.WithTimeout(ctx, config.timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost, config.endpointURL, bytes.NewReader(encoded))
	if err != nil {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, ErrorCodeEmbeddingRejected, false, &ConnectionDiagnostic{
			Stage:          ConnectionStageRequest,
			TransportError: "request construction failed",
			cause:          err,
		})
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	if config.authorization != "" {
		httpRequest.Header.Set("Authorization", config.authorization)
	}
	response, err := config.client.Do(httpRequest)
	if err != nil {
		return classifyEmbeddingTransportError(requestContext, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		diagnostic := providerResponseDiagnostic(response, config.authorization, config.endpointURL)
		return classifyEmbeddingStatus(response.StatusCode, diagnostic)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return embeddingResultError(responseValidationDiagnostic())
	}
	encodedResponse, err := io.ReadAll(io.LimitReader(response.Body, config.maxResponseBytes+1))
	if err != nil {
		return classifyEmbeddingTransportErrorWithStage(requestContext, err, ConnectionStageResponseRead)
	}
	if int64(len(encodedResponse)) > config.maxResponseBytes {
		return embeddingResultError(responseValidationDiagnostic())
	}
	decoder := json.NewDecoder(bytes.NewReader(encodedResponse))
	if err := decoder.Decode(result); err != nil {
		return embeddingResultError(responseValidationDiagnostic())
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return embeddingResultError(responseValidationDiagnostic())
	}
	return nil
}

func validateEmbeddingResult(contract domain.EmbeddingContract, request application.EmbedRequest, result application.EmbedResult) (application.EmbedResult, error) {
	normalized, err := application.ValidateEmbedResult(contract, request, result)
	if err != nil {
		return application.EmbedResult{}, withResponseValidationDiagnostic(err)
	}
	return application.EmbedResult{Model: contract.Model, Embeddings: normalized}, nil
}

func classifyEmbeddingTransportError(ctx context.Context, cause error) error {
	return classifyEmbeddingTransportErrorWithStage(ctx, cause, ConnectionStageConnect)
}

func classifyEmbeddingTransportErrorWithStage(ctx context.Context, cause error, fallbackStage ConnectionStage) error {
	diagnostic := transportDiagnostic(ctx, cause, fallbackStage)
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return foundation.NewError(foundation.ErrorNonRetryableFailure, ErrorCodeEmbeddingCancelled, false, diagnostic)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeEmbeddingTimeout, true, diagnostic)
	default:
		var networkError net.Error
		if errors.As(cause, &networkError) && networkError.Timeout() {
			return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeEmbeddingTimeout, true, diagnostic)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeEmbeddingRequestFailed, true, diagnostic)
	}
}

func classifyEmbeddingStatus(statusCode int, diagnostic ...error) error {
	cause := error(errEmbeddingRequestFailed)
	if len(diagnostic) > 0 && diagnostic[0] != nil {
		cause = diagnostic[0]
	}
	switch {
	case statusCode == http.StatusRequestTimeout:
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeEmbeddingTimeout, true, cause)
	case statusCode == http.StatusTooManyRequests:
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeEmbeddingRateLimited, true, cause)
	case statusCode >= http.StatusInternalServerError:
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeEmbeddingUnavailable, true, cause)
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return foundation.NewError(foundation.ErrorNonRetryableFailure, ErrorCodeEmbeddingUnauthorized, false, cause)
	default:
		return foundation.NewError(foundation.ErrorNonRetryableFailure, ErrorCodeEmbeddingRejected, false, cause)
	}
}

func embeddingConfigError() error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeEmbeddingConfigInvalid, false, errEmbeddingConfigInvalid)
}

func embeddingResultError(diagnostic ...error) error {
	cause := error(responseValidationDiagnostic(errEmbeddingResponseInvalid))
	if len(diagnostic) > 0 && diagnostic[0] != nil {
		cause = diagnostic[0]
	}
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbedResultInvalid, false, cause)
}

func safeEmbeddingAdapterString(provider, model string) string {
	return fmt.Sprintf("%s embedding adapter(model=%q)", provider, model)
}
