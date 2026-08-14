package models_test

import (
	"context"
	"math"
	"strconv"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
)

const (
	einoLiveOpenAIEmbeddingEnabledEnv    = "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED"
	einoLiveOpenAIEmbeddingAPIKeyEnv     = "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY"
	einoLiveOpenAIEmbeddingModelEnv      = "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL"
	einoLiveOpenAIEmbeddingBaseURLEnv    = "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL"
	einoLiveOpenAIEmbeddingDimensionsEnv = "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS"
	einoLiveOpenAIEmbeddingTimeoutEnv    = "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_TIMEOUT"

	einoLiveOllamaEmbeddingEnabledEnv    = "ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED"
	einoLiveOllamaEmbeddingModelEnv      = "ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_MODEL"
	einoLiveOllamaEmbeddingBaseURLEnv    = "ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL"
	einoLiveOllamaEmbeddingDimensionsEnv = "ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_DIMENSIONS"
	einoLiveOllamaEmbeddingTimeoutEnv    = "ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_TIMEOUT"
)

func TestEinoOpenAIEmbeddingLiveSmoke(t *testing.T) {
	if !einoLiveEnabled(t, einoLiveOpenAIEmbeddingEnabledEnv) {
		return
	}
	cfg := config.Defaults()
	cfg.EmbeddingProvider = config.EmbeddingProviderOpenAICompatible
	cfg.EmbeddingBaseURL = requiredEinoLiveEnv(t, einoLiveOpenAIEmbeddingBaseURLEnv)
	cfg.EmbeddingAPIKey = requiredEinoLiveEnv(t, einoLiveOpenAIEmbeddingAPIKeyEnv)
	cfg.EmbeddingModel = requiredEinoLiveEnv(t, einoLiveOpenAIEmbeddingModelEnv)
	cfg.EmbeddingDimensions = einoLiveDimensions(t, einoLiveOpenAIEmbeddingDimensionsEnv)
	cfg.EmbeddingTimeout = einoLiveTimeout(t, einoLiveOpenAIEmbeddingTimeoutEnv)
	runEinoEmbeddingLiveSmoke(t, cfg)
}

func TestEinoOllamaEmbeddingLiveSmoke(t *testing.T) {
	if !einoLiveEnabled(t, einoLiveOllamaEmbeddingEnabledEnv) {
		return
	}
	cfg := config.Defaults()
	cfg.EmbeddingProvider = config.EmbeddingProviderOllama
	cfg.EmbeddingBaseURL = requiredEinoLiveEnv(t, einoLiveOllamaEmbeddingBaseURLEnv)
	cfg.EmbeddingModel = requiredEinoLiveEnv(t, einoLiveOllamaEmbeddingModelEnv)
	cfg.EmbeddingDimensions = einoLiveDimensions(t, einoLiveOllamaEmbeddingDimensionsEnv)
	cfg.EmbeddingTimeout = einoLiveTimeout(t, einoLiveOllamaEmbeddingTimeoutEnv)
	runEinoEmbeddingLiveSmoke(t, cfg)
}

func runEinoEmbeddingLiveSmoke(t *testing.T, cfg config.Config) {
	t.Helper()
	runtime, err := models.NewConfiguredModelRuntime(cfg)
	if err != nil {
		t.Fatalf("create configured production model runtime: %v", err)
	}
	embedding := runtime.Embedding()
	if embedding.State() != models.CapabilityConfigured {
		t.Fatalf("embedding capability state=%s", embedding.State())
	}
	embedder, ok := embedding.Embedder().(*models.EinoEmbedder)
	if !ok {
		t.Fatalf("production embedding adapter=%T, want *models.EinoEmbedder", embedding.Embedder())
	}

	request := retrievalapplication.EmbedRequest{Inputs: []string{
		"Eino production embedding smoke input one.",
		"Eino production embedding smoke input two.",
	}}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.EmbeddingTimeout)
	defer cancel()
	result, err := embedder.Embed(ctx, request)
	if err != nil {
		t.Fatalf("production Eino embedding smoke failed: %v", err)
	}
	if result.Model != cfg.EmbeddingModel || len(result.Embeddings) != len(request.Inputs) {
		t.Fatalf("embedding result model=%q vectors=%d", result.Model, len(result.Embeddings))
	}
	for index, vector := range result.Embeddings {
		if len(vector) != int(cfg.EmbeddingDimensions) {
			t.Fatalf("embedding vector %d dimensions=%d, want %d", index, len(vector), cfg.EmbeddingDimensions)
		}
		for dimension, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				t.Fatalf("embedding vector %d dimension %d is not finite", index, dimension)
			}
		}
	}
}

func einoLiveDimensions(t *testing.T, name string) int32 {
	t.Helper()
	value := requiredEinoLiveEnv(t, name)
	dimensions, err := strconv.ParseInt(value, 10, 32)
	if err != nil || dimensions <= 0 {
		t.Fatalf("parse %s: expected a positive integer", name)
	}
	return int32(dimensions)
}
