package models

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
)

// NewConfiguredEmbedder 按进程配置构造 API 与 Worker 共用的 Embedding Adapter。
func NewConfiguredEmbedder(cfg config.Config) (application.Embedder, error) {
	switch cfg.EmbeddingProvider {
	case config.EmbeddingProviderDisabled:
		return nil, nil
	case config.EmbeddingProviderOpenAICompatible:
		return NewOpenAICompatibleEmbedder(OpenAIEmbeddingOptions{
			BaseURL: cfg.EmbeddingBaseURL, APIKey: cfg.EmbeddingAPIKey, Model: cfg.EmbeddingModel,
			Dimensions: cfg.EmbeddingDimensions, Normalization: cfg.EmbeddingNormalization,
			DistanceMetric: cfg.EmbeddingDistanceMetric, MaxBatchSize: cfg.EmbeddingMaxBatchSize,
			MaxInputBytes: cfg.EmbeddingMaxInputBytes, MaxBatchInputBytes: cfg.EmbeddingMaxBatchInputBytes,
			Timeout:          cfg.EmbeddingTimeout,
			MaxResponseBytes: cfg.EmbeddingMaxResponseBytes,
		})
	case config.EmbeddingProviderOllama:
		return NewOllamaEmbedder(OllamaEmbeddingOptions{
			BaseURL: cfg.EmbeddingBaseURL, Model: cfg.EmbeddingModel, Dimensions: cfg.EmbeddingDimensions,
			Normalization: cfg.EmbeddingNormalization, DistanceMetric: cfg.EmbeddingDistanceMetric,
			MaxBatchSize: cfg.EmbeddingMaxBatchSize, MaxInputBytes: cfg.EmbeddingMaxInputBytes,
			MaxBatchInputBytes: cfg.EmbeddingMaxBatchInputBytes,
			Timeout:            cfg.EmbeddingTimeout, MaxResponseBytes: cfg.EmbeddingMaxResponseBytes,
		})
	default:
		return nil, errors.New("embedding provider is unsupported")
	}
}
