package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	einoollama "github.com/cloudwego/eino-ext/components/embedding/ollama"
	einoopenai "github.com/cloudwego/eino-ext/components/embedding/openai"
	einoembedding "github.com/cloudwego/eino/components/embedding"
	ollamaenv "github.com/ollama/ollama/envconfig"
)

const maxEinoEmbeddingErrorBodyBytes = int64(4096)

// EinoEmbedder 通过 Eino provider component 实现项目 Embedding Port。
type EinoEmbedder struct {
	http    embeddingHTTPConfig
	backend einoembedding.Embedder
}

// NewEinoOpenAICompatibleEmbedder 创建保留项目 wire contract 的 Eino OpenAI-Compatible Adapter。
func NewEinoOpenAICompatibleEmbedder(options OpenAIEmbeddingOptions) (*EinoEmbedder, error) {
	if strings.TrimSpace(options.APIKey) == "" {
		return nil, embeddingConfigError()
	}
	config, err := newEmbeddingHTTPConfig(openAICompatibleProvider, "/v1/embeddings", false, embeddingHTTPOptions{
		client: options.Client, baseURL: options.BaseURL,
		model: options.Model, dimensions: options.Dimensions, normalization: options.Normalization,
		distanceMetric: options.DistanceMetric, maxBatchSize: options.MaxBatchSize,
		maxInputBytes: options.MaxInputBytes, maxBatchInputBytes: options.MaxBatchInputBytes,
		timeout: options.Timeout, maxResponseBytes: options.MaxResponseBytes,
	})
	if err != nil {
		return nil, err
	}
	moveEinoEmbeddingClientTimeoutToCallContext(&config)
	config.client.Transport = &einoEmbeddingRoundTripper{
		base: config.client.Transport, provider: openAICompatibleProvider, maxResponseBytes: config.maxResponseBytes,
	}
	dimensions := int(config.contract.Dimensions)
	encodingFormat := einoopenai.EmbeddingEncodingFormatFloat
	backend, err := einoopenai.NewEmbedder(context.Background(), &einoopenai.EmbeddingConfig{
		HTTPClient: config.client, APIKey: options.APIKey,
		BaseURL: strings.TrimSuffix(config.endpointURL, "/embeddings"), Model: config.contract.Model,
		EncodingFormat: &encodingFormat, Dimensions: &dimensions,
	})
	if err != nil {
		return nil, embeddingConfigError()
	}
	return &EinoEmbedder{http: config, backend: backend}, nil
}

// NewEinoOllamaEmbedder 创建强制 truncate=false 并保留项目 wire contract 的 Eino Ollama Adapter。
func NewEinoOllamaEmbedder(options OllamaEmbeddingOptions) (*EinoEmbedder, error) {
	baseURL, err := url.Parse(options.BaseURL)
	if err != nil || strings.EqualFold(baseURL.Hostname(), "ollama.com") || ollamaenv.UseAuth() {
		return nil, embeddingConfigError()
	}
	config, err := newEmbeddingHTTPConfig(ollamaProvider, "/api/embed", true, embeddingHTTPOptions{
		client: options.Client, baseURL: options.BaseURL, model: options.Model,
		dimensions: options.Dimensions, normalization: options.Normalization,
		distanceMetric: options.DistanceMetric, maxBatchSize: options.MaxBatchSize,
		maxInputBytes: options.MaxInputBytes, maxBatchInputBytes: options.MaxBatchInputBytes,
		timeout: options.Timeout, maxResponseBytes: options.MaxResponseBytes,
	})
	if err != nil {
		return nil, err
	}
	moveEinoEmbeddingClientTimeoutToCallContext(&config)
	config.client.Transport = &einoEmbeddingRoundTripper{
		base: config.client.Transport, provider: ollamaProvider, maxResponseBytes: config.maxResponseBytes,
	}
	truncate := false
	backend, err := einoollama.NewEmbedder(context.Background(), &einoollama.EmbeddingConfig{
		HTTPClient: config.client, BaseURL: strings.TrimSuffix(config.endpointURL, "/api/embed"),
		Model: config.contract.Model, Truncate: &truncate,
	})
	if err != nil {
		return nil, embeddingConfigError()
	}
	return &EinoEmbedder{http: config, backend: backend}, nil
}

// Contract 返回不含 Credential 的冻结项目契约。
func (embedder *EinoEmbedder) Contract() domain.EmbeddingContract {
	if embedder == nil {
		return domain.EmbeddingContract{}
	}
	return embedder.http.contractCopy()
}

// Embed 通过 Eino 执行批量调用，再按项目 wire contract 恢复顺序并校验结果。
func (embedder *EinoEmbedder) Embed(ctx context.Context, request application.EmbedRequest) (application.EmbedResult, error) {
	if embedder == nil || embedder.backend == nil {
		return application.EmbedResult{}, embeddingConfigError()
	}
	if err := application.ValidateEmbedRequest(embedder.http.contract, request); err != nil {
		return application.EmbedResult{}, err
	}
	requestContext, cancel := context.WithTimeout(ctx, embedder.http.timeout)
	defer cancel()
	state := &einoEmbeddingCallState{model: embedder.http.contract.Model, inputCount: len(request.Inputs)}
	requestContext = context.WithValue(requestContext, einoEmbeddingCallStateContextKey{}, state)
	vectors, err := embedder.backend.EmbedStrings(requestContext, request.Inputs)
	if state.failure != nil {
		return application.EmbedResult{}, state.failure
	}
	if err != nil {
		if requestContext.Err() != nil {
			return application.EmbedResult{}, classifyEmbeddingTransportError(requestContext, err)
		}
		// wire 已成功读取后，SDK 解码失败属于响应一致性错误，不能误报为可重试网络故障。
		if state.wireValidated {
			return application.EmbedResult{}, embeddingResultError()
		}
		return application.EmbedResult{}, classifyEmbeddingTransportError(requestContext, err)
	}
	ordered, err := state.projectVectors(vectors)
	if err != nil {
		return application.EmbedResult{}, err
	}
	return validateEmbeddingResult(embedder.http.contract, request, application.EmbedResult{
		Model: state.responseModel, Embeddings: ordered,
	})
}

// String 返回不含 Endpoint、Credential 或输入正文的 Adapter 摘要。
func (embedder *EinoEmbedder) String() string {
	if embedder == nil {
		return "eino embedding adapter(unavailable)"
	}
	return "eino-backed " + safeEmbeddingAdapterString(embedder.http.contract.Provider, embedder.http.contract.Model)
}

// GoString 避免 %#v 展开 Eino client 与私有 HTTP 配置。
func (embedder *EinoEmbedder) GoString() string { return embedder.String() }

func moveEinoEmbeddingClientTimeoutToCallContext(config *embeddingHTTPConfig) {
	if config == nil || config.client == nil {
		return
	}
	if clientTimeout := config.client.Timeout; clientTimeout > 0 && clientTimeout < config.timeout {
		config.timeout = clientTimeout
	}
	config.client.Timeout = 0
}

type einoEmbeddingCallStateContextKey struct{}

type einoEmbeddingCallState struct {
	model         string
	responseModel string
	inputCount    int
	indices       []int
	wireVectors   [][]float32
	wireValidated bool
	failure       error
}

func (state *einoEmbeddingCallState) projectVectors(vectors [][]float64) ([][]float32, error) {
	if state == nil || !state.wireValidated || state.responseModel != state.model || len(vectors) != state.inputCount || len(state.wireVectors) != len(vectors) {
		return nil, embeddingResultError()
	}
	ordered := make([][]float32, len(vectors))
	for wireIndex, vector := range vectors {
		wireVector := state.wireVectors[wireIndex]
		if len(vector) != len(wireVector) {
			return nil, embeddingResultError()
		}
		converted := make([]float32, len(vector))
		for dimension, value := range vector {
			convertedValue := float32(value)
			if math.IsNaN(value) || math.IsInf(value, 0) || float64(convertedValue) != value || convertedValue != wireVector[dimension] {
				return nil, embeddingResultError()
			}
			converted[dimension] = convertedValue
		}
		targetIndex := wireIndex
		if state.indices != nil {
			if wireIndex >= len(state.indices) {
				return nil, embeddingResultError()
			}
			targetIndex = state.indices[wireIndex]
		}
		if targetIndex < 0 || targetIndex >= len(ordered) || ordered[targetIndex] != nil {
			return nil, embeddingResultError()
		}
		ordered[targetIndex] = converted
	}
	return ordered, nil
}

type einoEmbeddingRoundTripper struct {
	base             http.RoundTripper
	provider         string
	maxResponseBytes int64
}

func (transport *einoEmbeddingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	state := einoEmbeddingState(request)
	if transport == nil || transport.base == nil || request == nil || state == nil {
		return nil, failEinoEmbeddingState(state, embeddingResultError())
	}
	if transport.provider == ollamaProvider && request.Header.Get("Authorization") != "" {
		return nil, failEinoEmbeddingState(state, embeddingConfigError())
	}
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		state.failure = classifyEmbeddingTransportError(request.Context(), err)
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, failEinoEmbeddingState(state, embeddingResultError())
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxEinoEmbeddingErrorBodyBytes))
		_ = response.Body.Close()
		state.failure = classifyEmbeddingStatus(response.StatusCode)
		sanitized := []byte(`{"error":"embedding provider rejected request"}`)
		response.Body = io.NopCloser(bytes.NewReader(sanitized))
		response.ContentLength = int64(len(sanitized))
		response.Header.Set("Content-Type", "application/json")
		return response, nil
	}
	originalBody := response.Body
	defer originalBody.Close()
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || response.ContentLength > transport.maxResponseBytes {
		return nil, failEinoEmbeddingState(state, embeddingResultError())
	}
	rawBody, err := io.ReadAll(io.LimitReader(originalBody, transport.maxResponseBytes+1))
	if err != nil {
		return nil, failEinoEmbeddingState(state, classifyEmbeddingTransportError(request.Context(), err))
	}
	if int64(len(rawBody)) > transport.maxResponseBytes || validateEinoEmbeddingWireResponse(transport.provider, rawBody, state) != nil {
		return nil, failEinoEmbeddingState(state, embeddingResultError())
	}
	response.Body = io.NopCloser(bytes.NewReader(rawBody))
	response.ContentLength = int64(len(rawBody))
	return response, nil
}

func einoEmbeddingState(request *http.Request) *einoEmbeddingCallState {
	if request == nil {
		return nil
	}
	state, _ := request.Context().Value(einoEmbeddingCallStateContextKey{}).(*einoEmbeddingCallState)
	return state
}

func failEinoEmbeddingState(state *einoEmbeddingCallState, failure error) error {
	if state != nil {
		state.failure = failure
	}
	return &einoEmbeddingWireError{cause: failure}
}

func validateEinoEmbeddingWireResponse(provider string, rawBody []byte, state *einoEmbeddingCallState) error {
	if state == nil {
		return errEmbeddingResponseInvalid
	}
	switch provider {
	case openAICompatibleProvider:
		var response openAIEmbeddingResponse
		if err := decodeSingleEmbeddingJSON(rawBody, &response); err != nil || response.Model != state.model || len(response.Data) != state.inputCount {
			return errEmbeddingResponseInvalid
		}
		indices := make([]int, len(response.Data))
		vectors := make([][]float32, len(response.Data))
		seen := make([]bool, len(response.Data))
		for wireIndex, item := range response.Data {
			if item.Index == nil || *item.Index < 0 || *item.Index >= len(response.Data) || seen[*item.Index] || item.Embedding == nil {
				return errEmbeddingResponseInvalid
			}
			seen[*item.Index] = true
			indices[wireIndex] = *item.Index
			vectors[wireIndex] = append([]float32(nil), item.Embedding...)
		}
		state.responseModel = response.Model
		state.indices = indices
		state.wireVectors = vectors
	case ollamaProvider:
		var response ollamaEmbeddingResponse
		if err := decodeSingleEmbeddingJSON(rawBody, &response); err != nil || response.Model != state.model || len(response.Embeddings) != state.inputCount {
			return errEmbeddingResponseInvalid
		}
		state.responseModel = response.Model
		state.wireVectors = cloneEmbeddingVectors(response.Embeddings)
	default:
		return errEmbeddingResponseInvalid
	}
	state.wireValidated = true
	return nil
}

func decodeSingleEmbeddingJSON(rawBody []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(rawBody))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errEmbeddingResponseInvalid
	}
	return nil
}

func cloneEmbeddingVectors(vectors [][]float32) [][]float32 {
	result := make([][]float32, len(vectors))
	for index, vector := range vectors {
		result[index] = append([]float32(nil), vector...)
	}
	return result
}

type einoEmbeddingWireError struct{ cause error }

func (err *einoEmbeddingWireError) Error() string {
	return "eino embedding provider response failed project validation"
}

func (err *einoEmbeddingWireError) Unwrap() error { return err.cause }

var _ application.Embedder = (*EinoEmbedder)(nil)
var _ http.RoundTripper = (*einoEmbeddingRoundTripper)(nil)
