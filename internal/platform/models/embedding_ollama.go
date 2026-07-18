package models

import (
	"context"
	"net/http"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const ollamaProvider = "ollama"

// OllamaEmbeddingOptions 是 Ollama Native Adapter 的 Composition Root 输入。
type OllamaEmbeddingOptions struct {
	Client             *http.Client
	BaseURL            string
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

// OllamaEmbedder 通过直接 HTTP 调用原生 /api/embed。
type OllamaEmbedder struct {
	http embeddingHTTPConfig
}

// NewOllamaEmbedder 创建强制 truncate=false 的原生批量 Adapter。
func NewOllamaEmbedder(options OllamaEmbeddingOptions) (*OllamaEmbedder, error) {
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
	return &OllamaEmbedder{http: config}, nil
}

// Contract 返回不含 Credential 的不可变运行时绑定。
func (embedder *OllamaEmbedder) Contract() domain.EmbeddingContract {
	return embedder.http.contractCopy()
}

// Embed 按 Ollama embeddings 数组顺序校验批量结果，并执行契约校验与归一化。
func (embedder *OllamaEmbedder) Embed(ctx context.Context, request application.EmbedRequest) (application.EmbedResult, error) {
	payload := ollamaEmbeddingRequest{Input: request.Inputs, Model: embedder.http.contract.Model, Truncate: false}
	var response ollamaEmbeddingResponse
	if err := embedder.http.embed(ctx, request, payload, &response); err != nil {
		return application.EmbedResult{}, err
	}
	if response.Model != embedder.http.contract.Model || len(response.Embeddings) != len(request.Inputs) {
		return application.EmbedResult{}, embeddingResultError()
	}
	return validateEmbeddingResult(embedder.http.contract, request, application.EmbedResult{
		Model: response.Model, Embeddings: response.Embeddings,
	})
}

// String 返回可安全记录的 Adapter 摘要，不包含 Endpoint 或请求正文。
func (embedder *OllamaEmbedder) String() string {
	return safeEmbeddingAdapterString(ollamaProvider, embedder.http.contract.Model)
}

// GoString 避免 %#v 调试输出展开完整 Endpoint。
func (embedder *OllamaEmbedder) GoString() string {
	return embedder.String()
}

type ollamaEmbeddingRequest struct {
	Input    []string `json:"input"`
	Model    string   `json:"model"`
	Truncate bool     `json:"truncate"`
}

type ollamaEmbeddingResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}

var _ application.Embedder = (*OllamaEmbedder)(nil)
