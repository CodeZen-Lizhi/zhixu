package models

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestConfiguredModelRuntimeFreezesCapabilitiesAdaptersAndContracts(t *testing.T) {
	t.Parallel()

	cfg := configuredRuntimeTestConfig()
	runtime, err := NewConfiguredModelRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	chat := runtime.Chat()
	chatContract, ok := chat.Contract()
	if !ok || chat.State() != CapabilityConfigured || chat.Model() == nil {
		t.Fatalf("chat capability = %#v", chat)
	}
	if chat.Model() != runtime.Chat().Model() || chatContract.Model.ModelID != cfg.ChatModel ||
		chatContract.Provider != string(cfg.ChatProvider) || chatContract.APIStyle != ChatAPIStyleChatCompletions ||
		chatContract.EndpointPath != chatCompletionsPath ||
		chatContract.Timeout != cfg.ChatTimeout || chatContract.MaxRequestBytes != cfg.ChatMaxRequestBytes ||
		chatContract.MaxResponseBytes != cfg.ChatMaxResponseBytes {
		t.Fatalf("chat contract = %#v", chatContract)
	}
	runtimeChat := runtime.RuntimeChat()
	runtimeChatContract, ok := runtimeChat.Contract()
	if !ok || runtimeChat.State() != CapabilityConfigured || runtimeChat.Model() == nil ||
		runtimeChat.Model() != runtime.RuntimeChat().Model() || runtimeChatContract != chatContract {
		t.Fatalf("runtime chat capability = %#v", runtimeChat)
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

func TestConfiguredModelRuntimeRejectsResponsesWithoutFallback(t *testing.T) {
	t.Parallel()
	cfg := configuredRuntimeTestConfig()
	cfg.ChatAPIStyle = config.ChatAPIStyleResponses
	runtime, err := NewConfiguredModelRuntime(cfg)
	if runtime != nil {
		t.Fatalf("Responses style constructed runtime %#v", runtime)
	}
	if err == nil || !strings.Contains(err.Error(), "responses") {
		t.Fatalf("error=%v", err)
	}
}

func TestConfiguredModelRuntimeDisabledCapabilitiesAndSafeFormatting(t *testing.T) {
	t.Parallel()

	var zero ModelRuntime
	if zero.Chat().State() != CapabilityDisabled || zero.RuntimeChat().State() != CapabilityDisabled || zero.Embedding().State() != CapabilityDisabled {
		t.Fatalf("zero runtime capabilities = %#v / %#v / %#v", zero.Chat(), zero.RuntimeChat(), zero.Embedding())
	}

	disabled, err := NewConfiguredModelRuntime(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disabled.Close() })
	if disabled.Chat().State() != CapabilityDisabled || disabled.Chat().Model() != nil {
		t.Fatalf("chat = %#v", disabled.Chat())
	}
	if _, ok := disabled.Chat().Contract(); ok {
		t.Fatal("disabled Chat exposed a contract")
	}
	if disabled.RuntimeChat().State() != CapabilityDisabled || disabled.RuntimeChat().Model() != nil {
		t.Fatalf("runtime chat = %#v", disabled.RuntimeChat())
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
	t.Cleanup(func() { _ = runtime.Close() })
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
	t.Cleanup(func() { _ = runtime.Close() })
	wantChat := runtime.Chat().Model()
	wantRuntimeChat := runtime.RuntimeChat().Model()
	wantEmbedding := runtime.Embedding().Embedder()
	var wait sync.WaitGroup
	errors := make(chan string, 64)
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			chatContract, chatOK := runtime.Chat().Contract()
			embeddingContract, embeddingOK := runtime.Embedding().Contract()
			if !chatOK || !embeddingOK || runtime.Chat().Model() != wantChat || runtime.RuntimeChat().Model() != wantRuntimeChat || runtime.Embedding().Embedder() != wantEmbedding ||
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

func TestModelRuntimeCloseClosesOwnedTransportsExactlyOnce(t *testing.T) {
	t.Parallel()

	chatTransport := &countingIdleTransport{}
	runtimeChatTransport := &countingIdleTransport{}
	embeddingTransport := &countingIdleTransport{}
	runtime := modelRuntimeWithTransports(chatTransport, chatTransport, runtimeChatTransport, runtimeChatTransport, embeddingTransport, embeddingTransport)
	var wait sync.WaitGroup
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := runtime.Close(); err != nil {
				t.Errorf("Close() error = %v", err)
			}
		}()
	}
	wait.Wait()
	if got := chatTransport.closeCalls.Load(); got != 1 {
		t.Fatalf("chat transport close calls = %d, want 1", got)
	}
	if got := runtimeChatTransport.closeCalls.Load(); got != 1 {
		t.Fatalf("runtime chat transport close calls = %d, want 1", got)
	}
	if got := embeddingTransport.closeCalls.Load(); got != 1 {
		t.Fatalf("embedding transport close calls = %d, want 1", got)
	}
	if err := (&ModelRuntime{}).Close(); err != nil {
		t.Fatalf("disabled runtime Close() error = %v", err)
	}
}

func TestModelRuntimeCloseLeavesExternalTransportsOpen(t *testing.T) {
	t.Parallel()

	chatTransport := &countingIdleTransport{}
	runtimeChatTransport := &countingIdleTransport{}
	embeddingTransport := &countingIdleTransport{}
	runtime := modelRuntimeWithTransports(chatTransport, nil, runtimeChatTransport, nil, embeddingTransport, nil)
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if got := chatTransport.closeCalls.Load(); got != 0 {
		t.Fatalf("external chat transport close calls = %d, want 0", got)
	}
	if got := runtimeChatTransport.closeCalls.Load(); got != 0 {
		t.Fatalf("external runtime chat transport close calls = %d, want 0", got)
	}
	if got := embeddingTransport.closeCalls.Load(); got != 0 {
		t.Fatalf("external embedding transport close calls = %d, want 0", got)
	}
}

func TestConfiguredModelRuntimeCompensatesChatWhenEmbeddingBuildFails(t *testing.T) {
	t.Parallel()

	chatTransport := &countingIdleTransport{}
	runtimeChatTransport := &countingIdleTransport{}
	chat := &EinoOpenAIChatModel{http: chatHTTPConfig{
		client: &modelHTTPClient{client: &http.Client{Transport: chatTransport}, owned: chatTransport},
	}}
	runtimeChat := &EinoRuntimeChatModel{http: chatHTTPConfig{
		client: &modelHTTPClient{client: &http.Client{Transport: runtimeChatTransport}, owned: runtimeChatTransport},
	}}
	wantErr := errors.New("embedding build failed")
	_, err := newConfiguredModelRuntime(configuredRuntimeTestConfig(), modelRuntimeBuilders{
		chat: func(config.Config) (agentapplication.ChatModel, error) {
			return chat, nil
		},
		runtimeChat: func(config.Config) (*EinoRuntimeChatModel, error) {
			return runtimeChat, nil
		},
		embedding: func(config.Config) (retrievalapplication.Embedder, error) {
			return nil, wantErr
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("construction error = %v, want embedding build error", err)
	}
	if got := chatTransport.closeCalls.Load(); got != 1 {
		t.Fatalf("compensated chat transport close calls = %d, want 1", got)
	}
	if got := runtimeChatTransport.closeCalls.Load(); got != 1 {
		t.Fatalf("compensated runtime chat transport close calls = %d, want 1", got)
	}
}

func modelRuntimeWithTransports(
	chatTransport http.RoundTripper,
	chatOwned idleConnectionCloser,
	runtimeChatTransport http.RoundTripper,
	runtimeChatOwned idleConnectionCloser,
	embeddingTransport http.RoundTripper,
	embeddingOwned idleConnectionCloser,
) *ModelRuntime {
	chat := &EinoOpenAIChatModel{http: chatHTTPConfig{
		client: &modelHTTPClient{client: &http.Client{Transport: chatTransport}, owned: chatOwned},
	}}
	runtimeChat := &EinoRuntimeChatModel{http: chatHTTPConfig{
		client: &modelHTTPClient{client: &http.Client{Transport: runtimeChatTransport}, owned: runtimeChatOwned},
	}}
	embedder := &EinoEmbedder{http: embeddingHTTPConfig{
		client: &modelHTTPClient{client: &http.Client{Transport: embeddingTransport}, owned: embeddingOwned},
	}}
	return &ModelRuntime{
		chat:        ChatCapability{state: CapabilityConfigured, model: chat},
		runtimeChat: RuntimeChatCapability{state: CapabilityConfigured, model: runtimeChat},
		embedding: EmbeddingCapability{
			state: CapabilityConfigured, embedder: embedder,
		},
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
