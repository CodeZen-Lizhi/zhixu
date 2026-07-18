package models

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const openAICompatibleProvider = "openai-compatible"

// OpenAIEmbeddingOptions 是 OpenAI-Compatible Adapter 的 Composition Root 输入。
type OpenAIEmbeddingOptions struct {
	Client             *http.Client
	BaseURL            string
	APIKey             string
	Model              string
	Dimensions         int32
	Normalization      domain.EmbeddingNormalization
	DistanceMetric     domain.DistanceMetric
	MaxBatchSize       int32
	MaxInputBytes      int32
	MaxBatchInputBytes int64
	Timeout            time.Duration
	MaxResponseBytes   int64
}

// OpenAICompatibleEmbedder 通过直接 HTTP 调用 /v1/embeddings。
type OpenAICompatibleEmbedder struct {
	http embeddingHTTPConfig
}

// NewOpenAICompatibleEmbedder 创建强制 float 编码、显式维度和 Bearer Credential 的 Adapter。
func NewOpenAICompatibleEmbedder(options OpenAIEmbeddingOptions) (*OpenAICompatibleEmbedder, error) {
	if strings.TrimSpace(options.APIKey) == "" {
		return nil, embeddingConfigError()
	}
	config, err := newEmbeddingHTTPConfig(openAICompatibleProvider, "/v1/embeddings", false, embeddingHTTPOptions{
		client: options.Client, baseURL: options.BaseURL, authorization: "Bearer " + options.APIKey,
		model: options.Model, dimensions: options.Dimensions, normalization: options.Normalization,
		distanceMetric: options.DistanceMetric, maxBatchSize: options.MaxBatchSize,
		maxInputBytes: options.MaxInputBytes, maxBatchInputBytes: options.MaxBatchInputBytes,
		timeout: options.Timeout, maxResponseBytes: options.MaxResponseBytes,
	})
	if err != nil {
		return nil, err
	}
	return &OpenAICompatibleEmbedder{http: config}, nil
}

// Contract 返回不含 API Key 的不可变运行时绑定。
func (embedder *OpenAICompatibleEmbedder) Contract() domain.EmbeddingContract {
	return embedder.http.contractCopy()
}

// Embed 批量请求 Provider，并按 data.index 恢复输入顺序后执行契约校验与归一化。
func (embedder *OpenAICompatibleEmbedder) Embed(ctx context.Context, request application.EmbedRequest) (application.EmbedResult, error) {
	payload := openAIEmbeddingRequest{
		Input:          request.Inputs,
		Model:          embedder.http.contract.Model,
		EncodingFormat: "float",
		Dimensions:     embedder.http.contract.Dimensions,
	}
	var response openAIEmbeddingResponse
	if err := embedder.http.embed(ctx, request, payload, &response); err != nil {
		return application.EmbedResult{}, err
	}
	if response.Model != embedder.http.contract.Model || len(response.Data) != len(request.Inputs) {
		return application.EmbedResult{}, embeddingResultError()
	}
	embeddings := make([][]float32, len(response.Data))
	seen := make([]bool, len(response.Data))
	for _, item := range response.Data {
		if item.Index == nil || *item.Index < 0 || *item.Index >= len(response.Data) || seen[*item.Index] || item.Embedding == nil {
			return application.EmbedResult{}, embeddingResultError()
		}
		seen[*item.Index] = true
		embeddings[*item.Index] = item.Embedding
	}
	return validateEmbeddingResult(embedder.http.contract, request, application.EmbedResult{Model: response.Model, Embeddings: embeddings})
}

// String 返回可安全记录的 Adapter 摘要，不包含 Endpoint、Credential 或请求正文。
func (embedder *OpenAICompatibleEmbedder) String() string {
	return safeEmbeddingAdapterString(openAICompatibleProvider, embedder.http.contract.Model)
}

// GoString 避免 %#v 调试输出展开私有 Credential。
func (embedder *OpenAICompatibleEmbedder) GoString() string {
	return embedder.String()
}

type openAIEmbeddingRequest struct {
	Input          []string `json:"input"`
	Model          string   `json:"model"`
	EncodingFormat string   `json:"encoding_format"`
	Dimensions     int32    `json:"dimensions"`
}

type openAIEmbeddingResponse struct {
	Data  []openAIEmbeddingItem `json:"data"`
	Model string                `json:"model"`
}

type openAIEmbeddingItem struct {
	Embedding []float32 `json:"embedding"`
	Index     *int      `json:"index"`
}

var _ application.Embedder = (*OpenAICompatibleEmbedder)(nil)
