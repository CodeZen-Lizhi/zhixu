package models_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestNewConfiguredEmbedderSupportsDisabledOpenAIAndOllama(t *testing.T) {
	disabled, err := models.NewConfiguredEmbedder(config.Defaults())
	if err != nil || disabled != nil {
		t.Fatalf("disabled embedder=%#v err=%v", disabled, err)
	}

	base := configuredEmbeddingTestConfig()
	tests := []struct {
		name       string
		configure  func(*config.Config)
		newDirect  func(config.Config) (retrievaldomain.EmbeddingContract, error)
		wantType   any
		wantSecret string
	}{
		{
			name: "openai-compatible",
			configure: func(cfg *config.Config) {
				cfg.EmbeddingProvider = config.EmbeddingProviderOpenAICompatible
				cfg.EmbeddingBaseURL = "https://models.example.test/base"
				cfg.EmbeddingAPIKey = "factory-secret-canary"
			},
			newDirect: func(cfg config.Config) (retrievaldomain.EmbeddingContract, error) {
				embedder, err := models.NewOpenAICompatibleEmbedder(models.OpenAIEmbeddingOptions{
					BaseURL: cfg.EmbeddingBaseURL, APIKey: cfg.EmbeddingAPIKey, Model: cfg.EmbeddingModel,
					Dimensions: cfg.EmbeddingDimensions, Normalization: cfg.EmbeddingNormalization,
					DistanceMetric: cfg.EmbeddingDistanceMetric, MaxBatchSize: cfg.EmbeddingMaxBatchSize,
					MaxInputBytes: cfg.EmbeddingMaxInputBytes, MaxBatchInputBytes: cfg.EmbeddingMaxBatchInputBytes,
					Timeout: cfg.EmbeddingTimeout, MaxResponseBytes: cfg.EmbeddingMaxResponseBytes,
				})
				if err != nil {
					return retrievaldomain.EmbeddingContract{}, err
				}
				return embedder.Contract(), nil
			},
			wantType:   (*models.OpenAICompatibleEmbedder)(nil),
			wantSecret: "factory-secret-canary",
		},
		{
			name: "ollama",
			configure: func(cfg *config.Config) {
				cfg.EmbeddingProvider = config.EmbeddingProviderOllama
				cfg.EmbeddingBaseURL = "http://127.0.0.1:11434/models"
			},
			newDirect: func(cfg config.Config) (retrievaldomain.EmbeddingContract, error) {
				embedder, err := models.NewOllamaEmbedder(models.OllamaEmbeddingOptions{
					BaseURL: cfg.EmbeddingBaseURL, Model: cfg.EmbeddingModel, Dimensions: cfg.EmbeddingDimensions,
					Normalization: cfg.EmbeddingNormalization, DistanceMetric: cfg.EmbeddingDistanceMetric,
					MaxBatchSize: cfg.EmbeddingMaxBatchSize, MaxInputBytes: cfg.EmbeddingMaxInputBytes,
					MaxBatchInputBytes: cfg.EmbeddingMaxBatchInputBytes,
					Timeout:            cfg.EmbeddingTimeout, MaxResponseBytes: cfg.EmbeddingMaxResponseBytes,
				})
				if err != nil {
					return retrievaldomain.EmbeddingContract{}, err
				}
				return embedder.Contract(), nil
			},
			wantType: (*models.OllamaEmbedder)(nil),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.configure(&cfg)
			embedder, err := models.NewConfiguredEmbedder(cfg)
			if err != nil {
				t.Fatal("configured embedder factory rejected a valid provider configuration")
			}
			if reflect.TypeOf(embedder) != reflect.TypeOf(test.wantType) {
				t.Fatalf("embedder type=%T want=%T", embedder, test.wantType)
			}
			directContract, err := test.newDirect(cfg)
			if err != nil {
				t.Fatal("direct embedder constructor rejected a valid provider configuration")
			}
			if contract := embedder.Contract(); !reflect.DeepEqual(contract, directContract) || contract.ConfigHash == "" {
				t.Fatalf("factory contract=%#v direct contract=%#v", contract, directContract)
			}
			formatted := fmt.Sprintf("%v %#v %#v", embedder, embedder, embedder.Contract())
			if test.wantSecret != "" && strings.Contains(formatted, test.wantSecret) {
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
	if err == nil || embedder != nil {
		t.Fatalf("embedder=%#v err=%v", embedder, err)
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
