package models_test

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
)

const (
	einoLiveEnabledEnv      = "ZHIXU_EINO_LIVE_ENABLED"
	einoLiveAPIKeyEnv       = "ZHIXU_EINO_LIVE_API_KEY"
	einoLiveModelEnv        = "ZHIXU_EINO_LIVE_MODEL"
	einoLiveModelVersionEnv = "ZHIXU_EINO_LIVE_MODEL_VERSION"
	einoLiveBaseURLEnv      = "ZHIXU_EINO_LIVE_BASE_URL"
	einoLiveTimeoutEnv      = "ZHIXU_EINO_LIVE_TIMEOUT"
	einoLiveDefaultBaseURL  = "https://api.openai.com/v1"
	einoLiveDefaultTimeout  = 30 * time.Second
	einoLiveMaxOutputTokens = 512
)

// TestEinoOpenAIChatModelLiveSmoke exercises the production adapter only when
// the explicitly opt-in PoC live environment is complete. It never logs values
// from the provider configuration.
func TestEinoOpenAIChatModelLiveSmoke(t *testing.T) {
	enabledValue := strings.TrimSpace(os.Getenv(einoLiveEnabledEnv))
	enabled, err := strconv.ParseBool(enabledValue)
	if enabledValue == "" || (err == nil && !enabled) {
		t.Skipf("production Eino live smoke disabled; set %s=true to enable it", einoLiveEnabledEnv)
	}
	if err != nil {
		t.Fatalf("parse %s: expected a boolean", einoLiveEnabledEnv)
	}
	apiKey := strings.TrimSpace(os.Getenv(einoLiveAPIKeyEnv))
	modelID := strings.TrimSpace(os.Getenv(einoLiveModelEnv))
	if apiKey == "" || modelID == "" {
		t.Skip("production Eino live smoke enabled without complete credentials")
	}
	baseURL := strings.TrimSpace(os.Getenv(einoLiveBaseURLEnv))
	if baseURL == "" {
		baseURL = einoLiveDefaultBaseURL
	}
	modelVersion := strings.TrimSpace(os.Getenv(einoLiveModelVersionEnv))
	if modelVersion == "" {
		modelVersion = modelID
	}
	timeout := einoLiveDefaultTimeout
	if value := strings.TrimSpace(os.Getenv(einoLiveTimeoutEnv)); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			t.Fatalf("parse %s: invalid duration", einoLiveTimeoutEnv)
		}
		timeout = parsed
	}

	model, err := models.NewEinoOpenAIChatModel(models.OpenAIChatOptions{
		BaseURL: baseURL, APIKey: apiKey, Model: modelID, ModelVersion: modelVersion,
		AdapterVersion: "live-smoke", Timeout: timeout,
		MaxRequestBytes: 4 << 20, MaxResponseBytes: 4 << 20,
	})
	if err != nil {
		t.Fatalf("create production Eino adapter: %v", err)
	}
	request := validChatRequest(model.Contract().Model)
	// Reasoning-capable providers may spend part of this budget before emitting JSON.
	request.MaxOutputTokens = einoLiveMaxOutputTokens
	request.Messages = []agentapplication.ChatMessage{
		{Role: agentapplication.MessageRoleSystem, Content: "Return one JSON object that follows the response schema."},
		{Role: agentapplication.MessageRoleUser, Content: `Return {"ok":true}.`},
	}
	request.OutputSchema = []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	response, err := model.Chat(ctx, request)
	if err != nil {
		t.Fatalf("production Eino adapter smoke failed: %v", err)
	}
	var payload struct {
		OK bool `json:"ok"`
	}
	if !json.Valid(response.Content) || json.Unmarshal(response.Content, &payload) != nil || !payload.OK {
		t.Fatalf("production Eino adapter returned invalid JSON content")
	}
}
