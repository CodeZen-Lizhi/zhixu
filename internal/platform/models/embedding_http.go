// Package models 提供 Eino 模型组件所需的项目安全传输与协议合同。
package models

import (
	"context"
	"errors"
	"fmt"
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
	// embeddingAdapterIdentity 已进入持久 EmbeddingContract/ConfigHash；
	// 字符串值必须保持历史兼容，它是协议身份而非当前实现选择器。
	embeddingAdapterIdentity = "direct-http"
)

var (
	errEmbeddingConfigInvalid   = errors.New("embedding adapter configuration is invalid")
	errEmbeddingRequestFailed   = errors.New("embedding provider request failed")
	errEmbeddingResponseInvalid = errors.New("embedding provider response is invalid")
)

type embeddingHTTPConfig struct {
	client           *http.Client
	endpointURL      string
	contract         domain.EmbeddingContract
	timeout          time.Duration
	maxResponseBytes int64
}

type embeddingHTTPOptions struct {
	client             *http.Client
	baseURL            string
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
		AdapterName:        embeddingAdapterIdentity,
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
	case strings.HasSuffix(cleaned, requestPath):
		requestURL.Path = path.Clean(cleaned)
	case requestPath == "/v1/embeddings" && strings.HasSuffix(cleaned, "/v1"):
		requestURL.Path = path.Join(cleaned, "embeddings")
	default:
		requestURL.Path = path.Join(cleaned, requestPath)
	}
	return requestURL.String()
}

func (config embeddingHTTPConfig) contractCopy() domain.EmbeddingContract {
	return config.contract
}

func validateEmbeddingResult(contract domain.EmbeddingContract, request application.EmbedRequest, result application.EmbedResult) (application.EmbedResult, error) {
	normalized, err := application.ValidateEmbedResult(contract, request, result)
	if err != nil {
		return application.EmbedResult{}, err
	}
	return application.EmbedResult{Model: contract.Model, Embeddings: normalized}, nil
}

func classifyEmbeddingTransportError(ctx context.Context, cause error) error {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return foundation.NewError(foundation.ErrorNonRetryableFailure, ErrorCodeEmbeddingCancelled, false, context.Canceled)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeEmbeddingTimeout, true, context.DeadlineExceeded)
	default:
		var networkError net.Error
		if errors.As(cause, &networkError) && networkError.Timeout() {
			return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeEmbeddingTimeout, true, errEmbeddingRequestFailed)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeEmbeddingRequestFailed, true, errEmbeddingRequestFailed)
	}
}

func classifyEmbeddingStatus(statusCode int) error {
	switch {
	case statusCode == http.StatusRequestTimeout:
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeEmbeddingTimeout, true, errEmbeddingRequestFailed)
	case statusCode == http.StatusTooManyRequests:
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeEmbeddingRateLimited, true, errEmbeddingRequestFailed)
	case statusCode >= http.StatusInternalServerError:
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeEmbeddingUnavailable, true, errEmbeddingRequestFailed)
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return foundation.NewError(foundation.ErrorNonRetryableFailure, ErrorCodeEmbeddingUnauthorized, false, errEmbeddingRequestFailed)
	default:
		return foundation.NewError(foundation.ErrorNonRetryableFailure, ErrorCodeEmbeddingRejected, false, errEmbeddingRequestFailed)
	}
}

func embeddingConfigError() error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeEmbeddingConfigInvalid, false, errEmbeddingConfigInvalid)
}

func embeddingResultError() error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbedResultInvalid, false, errEmbeddingResponseInvalid)
}

func safeEmbeddingAdapterString(provider, model string) string {
	return fmt.Sprintf("%s embedding adapter(model=%q)", provider, model)
}
