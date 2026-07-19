package models_test

import (
	"errors"
	"fmt"
	"reflect"
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

func TestNewConfiguredChatModelBuildsOnlyOpenAICompatibleAdapter(t *testing.T) {
	t.Parallel()
	cfg := configuredChatTestConfig()
	model, err := models.NewConfiguredChatModel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.TypeOf(model) != reflect.TypeOf((*models.OpenAICompatibleChatModel)(nil)) {
		t.Fatalf("model type=%T", model)
	}
	configured := model.(*models.OpenAICompatibleChatModel)
	if configured.Contract().Model.ModelID != cfg.ChatModel || configured.Contract().Model.ModelVersion != cfg.ChatModelVersion || configured.Contract().Model.AdapterVersion != cfg.ChatAdapterVersion {
		t.Fatalf("contract=%#v", configured.Contract())
	}
	formatted := fmt.Sprintf("%v %#v %#v", configured, configured, configured.Contract())
	for _, secret := range []string{cfg.ChatAPIKey, cfg.ChatBaseURL} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("factory output leaked %q", secret)
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
