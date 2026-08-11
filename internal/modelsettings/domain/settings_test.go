package domain

import (
	"errors"
	"fmt"
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
