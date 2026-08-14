package models

import (
	"net/http"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	openAICompatibleProvider = "openai-compatible"
	ollamaProvider           = "ollama"
)

// OpenAIEmbeddingOptions 是 Eino OpenAI-Compatible Embedding Adapter 的 Composition Root 输入。
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

// OllamaEmbeddingOptions 是 Eino Ollama Embedding Adapter 的 Composition Root 输入。
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

type openAIEmbeddingResponse struct {
	Data  []openAIEmbeddingItem `json:"data"`
	Model string                `json:"model"`
}

type openAIEmbeddingItem struct {
	Embedding []float32 `json:"embedding"`
	Index     *int      `json:"index"`
}

type ollamaEmbeddingResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}
