package models_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
)

func TestNewConfiguredChatModelReturnsUnavailableWhenDisabled(t *testing.T) {
	t.Parallel()
	model, err := models.NewConfiguredChatModel(config.Defaults())
	if model != nil {
		t.Fatalf("disabled provider constructed %T", model)
	}
	assertChatError(t, err, foundation.ErrorDependencyUnavailable, models.ErrorCodeChatCapabilityUnavailable, false)
}

func TestNewConfiguredChatModelDefaultsToEinoOpenAICompatibleAdapter(t *testing.T) {
	t.Parallel()
	cfg := configuredChatTestConfig()
	model, err := models.NewConfiguredChatModel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configured, ok := model.(*models.EinoOpenAIChatModel)
	if !ok {
		t.Fatalf("model type=%T", model)
	}
	if configured.Contract().Provider != string(cfg.ChatProvider) || configured.Contract().APIStyle != models.ChatAPIStyleChatCompletions ||
		configured.Contract().EndpointPath != "/v1/chat/completions" || configured.Contract().Model.ModelID != cfg.ChatModel ||
		configured.Contract().Model.ModelVersion != cfg.ChatModelVersion || configured.Contract().Model.AdapterVersion != cfg.ChatAdapterVersion {
		t.Fatalf("contract=%#v", configured.Contract())
	}
	formatted := fmt.Sprintf("%v %#v %#v", configured, configured, configured.Contract())
	for _, secret := range []string{cfg.ChatAPIKey, cfg.ChatBaseURL} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("factory output leaked %q", secret)
		}
	}
}

func TestNewConfiguredChatModelPreservesOllamaContractIdentity(t *testing.T) {
	t.Parallel()
	cfg := configuredChatTestConfig()
	cfg.ChatProvider = config.ChatProviderOllama
	cfg.ChatBaseURL = "http://127.0.0.1:11434/v1"
	cfg.ChatAPIKey = ""
	model, err := models.NewConfiguredChatModel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configured := model.(*models.EinoOpenAIChatModel)
	contract := configured.Contract()
	if contract.Provider != string(config.ChatProviderOllama) || contract.APIStyle != models.ChatAPIStyleChatCompletions ||
		contract.EndpointPath != "/v1/chat/completions" || contract.Model.AdapterName != "ollama-chat-completions-http" {
		t.Fatalf("contract=%#v", contract)
	}
}

func TestNewConfiguredChatModelRejectsResponsesWithoutNetwork(t *testing.T) {
	t.Parallel()
	cfg := configuredChatTestConfig()
	cfg.ChatAPIStyle = config.ChatAPIStyleResponses
	model, err := models.NewConfiguredChatModel(cfg)
	if model != nil {
		t.Fatalf("Responses style constructed %T", model)
	}
	assertChatError(t, err, foundation.ErrorInvalidInput, models.ErrorCodeChatConfigInvalid, false)
	formatted := fmt.Sprintf("%v %#v", err, err)
	for _, secret := range []string{cfg.ChatAPIKey, cfg.ChatBaseURL} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("factory error leaked %q", secret)
		}
	}
}

func TestEinoChatConstructorsRejectResponsesWithoutNetwork(t *testing.T) {
	t.Parallel()
	requests := 0
	options := chatOptions("https://responses-endpoint-secret.example.test/v1", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("network must not be used")
	})})
	options.APIStyle = models.ChatAPIStyleResponses
	structured, structuredErr := models.NewEinoOpenAIChatModel(options)
	if structured != nil {
		t.Fatalf("Responses style constructed structured model %T", structured)
	}
	assertChatError(t, structuredErr, foundation.ErrorInvalidInput, models.ErrorCodeChatConfigInvalid, false)
	runtime, runtimeErr := models.NewEinoRuntimeChatModel(options)
	if runtime != nil {
		t.Fatalf("Responses style constructed runtime model %T", runtime)
	}
	assertChatError(t, runtimeErr, foundation.ErrorInvalidInput, models.ErrorCodeChatConfigInvalid, false)
	if requests != 0 {
		t.Fatalf("Responses rejection sent %d network requests", requests)
	}
	formatted := fmt.Sprintf("%v %#v %v %#v", structuredErr, structuredErr, runtimeErr, runtimeErr)
	for _, secret := range []string{options.APIKey, options.BaseURL} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("constructor error leaked %q", secret)
		}
	}
}

func TestNewConfiguredChatModelRejectsUnknownProvider(t *testing.T) {
	t.Parallel()
	cfg := configuredChatTestConfig()
	cfg.ChatProvider = "native-ollama"
	model, err := models.NewConfiguredChatModel(cfg)
	if model != nil {
		t.Fatalf("unsupported provider constructed %T", model)
	}
	assertChatError(t, err, foundation.ErrorInvalidInput, models.ErrorCodeChatConfigInvalid, false)
	if strings.Contains(fmt.Sprintf("%v %#v", err, err), cfg.ChatAPIKey) {
		t.Fatal("factory error leaked API key")
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error=%T", err)
	}
}

func configuredChatTestConfig() config.Config {
	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	cfg.ChatBaseURL = "https://chat-endpoint-secret.example.test/base"
	cfg.ChatAPIKey = "factory-chat-secret-canary"
	cfg.ChatModel = "chat-v1"
	cfg.ChatModelVersion = "chat-v1"
	cfg.ChatAdapterVersion = "v7"
	return cfg
}
