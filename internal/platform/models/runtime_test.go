package models

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestConfiguredModelRuntimeFreezesCapabilitiesAdaptersAndContracts(t *testing.T) {
	t.Parallel()

	cfg := configuredRuntimeTestConfig()
	runtime, err := NewConfiguredModelRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	chat := runtime.Chat()
	chatContract, ok := chat.Contract()
	if !ok || chat.State() != CapabilityConfigured || chat.Model() == nil {
		t.Fatalf("chat capability = %#v", chat)
	}
	if chat.Model() != runtime.Chat().Model() || chatContract.Model.ModelID != cfg.ChatModel ||
		chatContract.Timeout != cfg.ChatTimeout || chatContract.MaxRequestBytes != cfg.ChatMaxRequestBytes ||
		chatContract.MaxResponseBytes != cfg.ChatMaxResponseBytes {
		t.Fatalf("chat contract = %#v", chatContract)
	}
	embedding := runtime.Embedding()
	embeddingContract, ok := embedding.Contract()
	if !ok || embedding.State() != CapabilityConfigured || embedding.Embedder() == nil {
		t.Fatalf("embedding capability = %#v", embedding)
	}
	if embedding.Embedder() != runtime.Embedding().Embedder() || embeddingContract.Binding() != embedding.Embedder().Contract() ||
		embeddingContract.Timeout() != cfg.EmbeddingTimeout || embeddingContract.MaxResponseBytes() != cfg.EmbeddingMaxResponseBytes {
		t.Fatalf("embedding contract = %#v", embeddingContract)
	}

	// Mutating the source config cannot alter the already-constructed runtime.
	cfg.ChatModel = "mutated-chat"
	cfg.EmbeddingModel = "mutated-embedding"
	if frozen, _ := runtime.Chat().Contract(); frozen.Model.ModelID == cfg.ChatModel {
		t.Fatal("chat contract followed source config mutation")
	}
	if frozen, _ := runtime.Embedding().Contract(); frozen.Binding().Model == cfg.EmbeddingModel {
		t.Fatal("embedding contract followed source config mutation")
	}
}

func TestConfiguredModelRuntimeDisabledCapabilitiesAndSafeFormatting(t *testing.T) {
	t.Parallel()

	var zero ModelRuntime
	if zero.Chat().State() != CapabilityDisabled || zero.Embedding().State() != CapabilityDisabled {
		t.Fatalf("zero runtime capabilities = %#v / %#v", zero.Chat(), zero.Embedding())
	}

	disabled, err := NewConfiguredModelRuntime(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Chat().State() != CapabilityDisabled || disabled.Chat().Model() != nil {
		t.Fatalf("chat = %#v", disabled.Chat())
	}
	if _, ok := disabled.Chat().Contract(); ok {
		t.Fatal("disabled Chat exposed a contract")
	}
	if disabled.Embedding().State() != CapabilityDisabled || disabled.Embedding().Embedder() != nil {
		t.Fatalf("embedding = %#v", disabled.Embedding())
	}
	if _, ok := disabled.Embedding().Contract(); ok {
		t.Fatal("disabled Embedding exposed a contract")
	}

	cfg := configuredRuntimeTestConfig()
	runtime, err := NewConfiguredModelRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	formatted := fmt.Sprintf("%v %#v %v %#v", runtime, runtime, runtime.Chat(), runtime.Embedding())
	for _, forbidden := range []string{cfg.ChatAPIKey, cfg.EmbeddingAPIKey, cfg.ChatBaseURL, cfg.EmbeddingBaseURL} {
		if strings.Contains(formatted, forbidden) {
			t.Fatalf("runtime formatting leaked %q: %s", forbidden, formatted)
		}
	}
}

func TestConfiguredModelRuntimeCapabilitiesAreConcurrentReadSafe(t *testing.T) {
	t.Parallel()

	runtime, err := NewConfiguredModelRuntime(configuredRuntimeTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	wantChat := runtime.Chat().Model()
	wantEmbedding := runtime.Embedding().Embedder()
	var wait sync.WaitGroup
	errors := make(chan string, 64)
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			chatContract, chatOK := runtime.Chat().Contract()
			embeddingContract, embeddingOK := runtime.Embedding().Contract()
			if !chatOK || !embeddingOK || runtime.Chat().Model() != wantChat || runtime.Embedding().Embedder() != wantEmbedding ||
				chatContract.Model.ModelID == "" || embeddingContract.Binding().Model == "" {
				errors <- "runtime capability changed during concurrent access"
			}
		}()
	}
	wait.Wait()
	close(errors)
	for failure := range errors {
		t.Fatal(failure)
	}
}

func configuredRuntimeTestConfig() config.Config {
	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	cfg.ChatBaseURL = "https://chat-runtime.example.test/gateway"
	cfg.ChatAPIKey = "runtime-chat-secret-canary"
	cfg.ChatModel = "chat-runtime-v1"
	cfg.ChatModelVersion = "chat-runtime-v1"
	cfg.ChatAdapterVersion = "v3"
	cfg.EmbeddingProvider = config.EmbeddingProviderOpenAICompatible
	cfg.EmbeddingBaseURL = "https://embedding-runtime.example.test/gateway"
	cfg.EmbeddingAPIKey = "runtime-embedding-secret-canary"
	cfg.EmbeddingModel = "embedding-runtime-v1"
	cfg.EmbeddingDimensions = 3
	cfg.EmbeddingNormalization = domain.NormalizationL2
	cfg.EmbeddingDistanceMetric = domain.DistanceCosine
	return cfg
}
