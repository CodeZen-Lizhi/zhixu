package models_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestNewConfiguredEmbedderUsesEinoForEveryEnabledProvider(t *testing.T) {
	disabled, err := models.NewConfiguredEmbedder(config.Defaults())
	if err != nil || disabled != nil {
		t.Fatalf("disabled embedder=%#v err=%v", disabled, err)
	}

	tests := []struct {
		name      string
		configure func(*config.Config)
		secret    string
	}{
		{
			name: "openai-compatible",
			configure: func(cfg *config.Config) {
				cfg.EmbeddingProvider = config.EmbeddingProviderOpenAICompatible
				cfg.EmbeddingBaseURL = "https://models.example.test/base"
				cfg.EmbeddingAPIKey = "factory-secret-canary"
			},
			secret: "factory-secret-canary",
		},
		{
			name: "ollama",
			configure: func(cfg *config.Config) {
				cfg.EmbeddingProvider = config.EmbeddingProviderOllama
				cfg.EmbeddingBaseURL = "http://127.0.0.1:11434/models"
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			cfg := configuredEmbeddingTestConfig()
			test.configure(&cfg)
			embedder, err := models.NewConfiguredEmbedder(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := embedder.(*models.EinoEmbedder); !ok {
				t.Fatalf("embedder type=%T", embedder)
			}
			if contract := embedder.Contract(); contract.Provider != string(cfg.EmbeddingProvider) || contract.Model != cfg.EmbeddingModel || contract.ConfigHash == "" {
				t.Fatalf("contract=%#v", contract)
			}
			formatted := fmt.Sprintf("%v %#v %#v", embedder, embedder, embedder.Contract())
			if test.secret != "" && strings.Contains(formatted, test.secret) {
				t.Fatal("factory output leaked the configured secret")
			}
		})
	}
}

func TestNewConfiguredEmbedderRejectsUnsupportedProviderWithoutSecretLeak(t *testing.T) {
	cfg := configuredEmbeddingTestConfig()
	cfg.EmbeddingProvider = "unsupported"
	cfg.EmbeddingAPIKey = "unsupported-provider-secret-canary"
	embedder, err := models.NewConfiguredEmbedder(cfg)
	if embedder != nil {
		t.Fatalf("embedder=%T", embedder)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != models.ErrorCodeEmbeddingConfigInvalid {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(fmt.Sprintf("%v", err), cfg.EmbeddingAPIKey) {
		t.Fatal("factory error leaked the configured secret")
	}
}

func configuredEmbeddingTestConfig() config.Config {
	cfg := config.Defaults()
	cfg.EmbeddingModel = "embed-v1"
	cfg.EmbeddingDimensions = 3
	cfg.EmbeddingNormalization = retrievaldomain.NormalizationL2
	cfg.EmbeddingDistanceMetric = retrievaldomain.DistanceCosine
	cfg.EmbeddingMaxBatchSize = 8
	cfg.EmbeddingMaxInputBytes = 1024
	cfg.EmbeddingMaxBatchInputBytes = 8192
	cfg.EmbeddingTimeout = time.Second
	cfg.EmbeddingMaxResponseBytes = 1 << 20
	return cfg
}
