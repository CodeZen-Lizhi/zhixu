package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCanonicalDisabledSettingsDefaultsChatAPIStyle(t *testing.T) {
	settings := CanonicalDisabledSettings()
	if settings.Chat.APIStyle != ChatAPIStyleChatCompletions {
		t.Fatalf("chat API style = %q", settings.Chat.APIStyle)
	}
	settings.Chat.APIStyle = ChatAPIStyle("automatic")
	if err := settings.ValidateStructural(); err == nil {
		t.Fatal("unknown chat API style was accepted")
	}
}

func TestCanonicalizeSettingsNormalizesEndpointIdentity(t *testing.T) {
	settings := CanonicalDisabledSettings()
	settings.Chat.Provider = ChatProviderOpenAICompatible
	settings.Chat.BaseURL = "HTTPS://Models.Example.Test:443/v1/"
	settings.Chat.Model = "chat-model"
	settings.Chat.ModelVersion = "v1"
	canonical, err := CanonicalizeSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Chat.BaseURL != "https://models.example.test/v1" {
		t.Fatalf("canonical base URL=%q", canonical.Chat.BaseURL)
	}
	if err := canonical.ValidateStructural(); err != nil {
		t.Fatal(err)
	}
	if err := settings.ValidateStructural(); err == nil {
		t.Fatal("non-canonical settings passed structural validation")
	}
}

func TestRequiresManagedOllamaUsesExplicitProvidersAndLegacyRelay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		settings Settings
		models   []string
	}{
		{name: "disabled", settings: CanonicalDisabledSettings()},
		{name: "chat", settings: func() Settings {
			value := CanonicalDisabledSettings()
			value.Chat.Provider = ChatProviderOllama
			value.Chat.Model = "qwen2.5:3b"
			return value
		}(), models: []string{"qwen2.5:3b"}},
		{name: "embedding", settings: func() Settings {
			value := CanonicalDisabledSettings()
			value.Embedding.Provider = EmbeddingProviderOllama
			value.Embedding.Model = "all-minilm:latest"
			return value
		}(), models: []string{"all-minilm:latest"}},
		{name: "deduplicated", settings: func() Settings {
			value := CanonicalDisabledSettings()
			value.Chat.Provider = ChatProviderOllama
			value.Chat.Model = "shared:model"
			value.Embedding.Provider = EmbeddingProviderOllama
			value.Embedding.Model = "shared:model"
			return value
		}(), models: []string{"shared:model"}},
		{name: "legacy chat", settings: func() Settings {
			value := CanonicalDisabledSettings()
			value.Chat.Provider = ChatProviderOpenAICompatible
			value.Chat.BaseURL = ManagedOllamaBaseURL
			value.Chat.Model = "legacy:model"
			return value
		}(), models: []string{"legacy:model"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := RequiresManagedOllama(test.settings)
			if !slices.Equal(got.Models, test.models) || got.Required != (len(test.models) != 0) || got.Required && len(got.Hash) != 64 {
				t.Fatalf("requirement=%#v", got)
			}
		})
	}
}

func TestOllamaChatRequiresFixedChatCompletionsShape(t *testing.T) {
	t.Parallel()
	settings := CanonicalDisabledSettings()
	settings.Chat.Provider = ChatProviderOllama
	settings.Chat.BaseURL = ManagedOllamaBaseURL
	settings.Chat.Model = "qwen2.5:3b"
	settings.Chat.ModelVersion = "qwen2.5:3b"
	if err := settings.ValidateStructural(); err != nil {
		t.Fatal(err)
	}
	settings.Chat.APIStyle = ChatAPIStyleResponses
	assertDomainCode(t, settings.ValidateStructural(), ErrorCodeInvalid)
}

func TestSettingsRejectDurationsThatCannotRoundTripThroughPostgres(t *testing.T) {
	settings := CanonicalDisabledSettings()
	settings.Chat.Timeout = time.Nanosecond
	assertDomainCode(t, settings.ValidateStructural(), ErrorCodeInvalid)
	settings = CanonicalDisabledSettings()
	settings.Embedding.Timeout = time.Second + time.Nanosecond
	assertDomainCode(t, settings.ValidateStructural(), ErrorCodeInvalid)
}

func TestValidateSecretChangeAllowsEquivalentEndpointAndRejectsRebind(t *testing.T) {
	if err := ValidateSecretChange(
		"openai-compatible", "HTTPS://Models.Example.Test:443/v1/", true,
		"openai-compatible", "https://models.example.test/v1", KeepSecret(),
	); err != nil {
		t.Fatal(err)
	}
	err := ValidateSecretChange(
		"openai-compatible", "https://models.example.test/v1", true,
		"openai-compatible", "https://other.example.test/v1", KeepSecret(),
	)
	assertDomainCode(t, err, ErrorCodeSecretTargetChanged)
}

func TestValidateWritableSettingsRequiresExplicitManagedProvider(t *testing.T) {
	t.Parallel()

	legacy := CanonicalDisabledSettings()
	legacy.Chat.Provider = ChatProviderOpenAICompatible
	legacy.Chat.BaseURL = ManagedOllamaBaseURL
	legacy.Chat.Model = "qwen2.5:3b"
	legacy.Chat.ModelVersion = "qwen2.5:3b"
	assertDomainCode(t, ValidateWritableSettings(legacy), ErrorCodeInvalid)

	legacy.Chat.Provider = ChatProviderOllama
	if err := ValidateWritableSettings(legacy); err != nil {
		t.Fatal(err)
	}

	legacy.Chat.BaseURL = "https://ollama.example.test"
	assertDomainCode(t, ValidateWritableSettings(legacy), ErrorCodeInvalid)
}

func TestSecretAndActionsNeverFormatPlaintext(t *testing.T) {
	action, err := ReplaceSecret("format-canary-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer action.Value.Destroy()
	for _, formatted := range []string{fmt.Sprint(action.Value), fmt.Sprintf("%#v", action.Value), fmt.Sprint(action), fmt.Sprintf("%+v", action)} {
		if strings.Contains(formatted, "format-canary-secret") {
			t.Fatalf("secret leaked through formatting: %q", formatted)
		}
	}
}

func TestRuntimeAndRolloutTransitionsRejectSkippedPhases(t *testing.T) {
	if err := ValidateRolloutTransition(RolloutPhaseValidating, RolloutPhaseDraining); err != nil {
		t.Fatal(err)
	}
	assertDomainCode(t, ValidateRolloutTransition(RolloutPhaseValidating, RolloutPhaseApplying), ErrorCodeRolloutConflict)
	if err := ValidateRuntimeTransition(RuntimePhaseActive, RuntimePhaseQuiescing); err != nil {
		t.Fatal(err)
	}
	assertDomainCode(t, ValidateRuntimeTransition(RuntimePhaseActive, RuntimePhaseQuiesced), ErrorCodeRuntimeConflict)
}

func assertDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if err == nil || !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%v want code=%s", err, code)
	}
}
