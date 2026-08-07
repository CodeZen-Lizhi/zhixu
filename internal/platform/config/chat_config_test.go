package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsDisableChatWithBoundedRuntimeSettings(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	if cfg.ChatProvider != ChatProviderDisabled || cfg.ChatImplementation != ChatImplementationDirect || cfg.ChatBaseURL != "" || cfg.ChatAPIKey != "" || cfg.ChatModel != "" || cfg.ChatModelVersion != "" ||
		cfg.ChatAdapterVersion != "v1" || cfg.ChatTimeout != 30*time.Second || cfg.ChatMaxRequestBytes != 4<<20 || cfg.ChatMaxResponseBytes != 4<<20 {
		t.Fatalf("chat defaults=%s", cfg)
	}
}

func TestLoadWithLookupChatYAMLAndEnvironment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`chat_provider: openai-compatible
chat_implementation: direct
chat_base_url: https://yaml.example.test/base
chat_api_key: yaml-secret
chat_model: yaml-model
chat_model_version: yaml-model-v1
chat_adapter_version: yaml-v1
chat_timeout: 45s
chat_max_request_bytes: 1000000
chat_max_response_bytes: 2000000
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"ZHIXU_CHAT_IMPLEMENTATION":    "eino",
		"ZHIXU_CHAT_BASE_URL":          "http://127.0.0.1:11434/compatible",
		"ZHIXU_CHAT_API_KEY":           "env-secret",
		"ZHIXU_CHAT_MODEL":             "env-model",
		"ZHIXU_CHAT_MODEL_VERSION":     "env-model-v2",
		"ZHIXU_CHAT_ADAPTER_VERSION":   "env-v2",
		"ZHIXU_CHAT_TIMEOUT":           "50s",
		"ZHIXU_CHAT_MAX_REQUEST_BYTES": "3000000",
	}
	cfg, err := LoadWithLookup(path, func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ChatProvider != ChatProviderOpenAICompatible || cfg.ChatImplementation != ChatImplementationEino || cfg.ChatBaseURL != values["ZHIXU_CHAT_BASE_URL"] ||
		cfg.ChatAPIKey != values["ZHIXU_CHAT_API_KEY"] || cfg.ChatModel != "env-model" || cfg.ChatModelVersion != "env-model-v2" || cfg.ChatAdapterVersion != "env-v2" ||
		cfg.ChatTimeout != 50*time.Second || cfg.ChatMaxRequestBytes != 3_000_000 || cfg.ChatMaxResponseBytes != 2_000_000 {
		t.Fatalf("chat config=%s", cfg)
	}
}

func TestLoadWithLookupDisabledChatDoesNotConsumeSecretEnvironment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("chat_provider: openai-compatible\nchat_base_url: https://yaml.example.test\nchat_api_key: yaml-secret\nchat_model: yaml-model\nchat_model_version: yaml-v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithLookup(path, func(key string) (string, bool) {
		switch key {
		case "ZHIXU_CHAT_PROVIDER":
			return "disabled", true
		case "ZHIXU_CHAT_BASE_URL", "ZHIXU_CHAT_API_KEY", "ZHIXU_CHAT_MODEL", "ZHIXU_CHAT_MODEL_VERSION":
			return "must-not-be-consumed-secret", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ChatBaseURL != "" || cfg.ChatAPIKey != "" || cfg.ChatModel != "" || cfg.ChatModelVersion != "" {
		t.Fatalf("disabled chat retained provider settings: %s", cfg)
	}
}

func TestValidateChatProviderAndSecurityBoundaries(t *testing.T) {
	t.Parallel()
	enabled := func() Config {
		cfg := Defaults()
		cfg.ChatProvider = ChatProviderOpenAICompatible
		cfg.ChatBaseURL = "https://models.example.test/base"
		cfg.ChatAPIKey = "optional-secret"
		cfg.ChatModel = "chat-v1"
		cfg.ChatModelVersion = "chat-v1"
		return cfg
	}
	valid := []Config{Defaults(), enabled(), func() Config {
		cfg := enabled()
		cfg.ChatBaseURL = "http://localhost:11434/v1"
		cfg.ChatAPIKey = ""
		return cfg
	}()}
	for _, cfg := range valid {
		if err := cfg.Validate(); err != nil {
			t.Fatalf("valid chat config rejected: %s: %v", cfg, err)
		}
	}
	tests := []struct {
		name   string
		change func(*Config)
		want   string
	}{
		{name: "unknown provider", change: func(cfg *Config) { cfg.ChatProvider = "ollama" }, want: "chat_provider"},
		{name: "unknown implementation", change: func(cfg *Config) { cfg.ChatImplementation = "automatic" }, want: "chat_implementation"},
		{name: "disabled endpoint", change: func(cfg *Config) { cfg.ChatBaseURL = "https://secret.example.test" }, want: "must be empty"},
		{name: "remote http", change: func(cfg *Config) { *cfg = enabled(); cfg.ChatBaseURL = "http://models.example.test" }, want: "https"},
		{name: "userinfo", change: func(cfg *Config) { *cfg = enabled(); cfg.ChatBaseURL = "https://user:secret@models.example.test" }, want: "chat_base_url"},
		{name: "query", change: func(cfg *Config) { *cfg = enabled(); cfg.ChatBaseURL = "https://models.example.test?secret=1" }, want: "chat_base_url"},
		{name: "fragment", change: func(cfg *Config) { *cfg = enabled(); cfg.ChatBaseURL = "https://models.example.test#secret" }, want: "chat_base_url"},
		{name: "empty model", change: func(cfg *Config) { *cfg = enabled(); cfg.ChatModel = "" }, want: "chat_model"},
		{name: "model whitespace", change: func(cfg *Config) { *cfg = enabled(); cfg.ChatModel = " chat " }, want: "chat_model"},
		{name: "empty model version", change: func(cfg *Config) { *cfg = enabled(); cfg.ChatModelVersion = "" }, want: "chat_model_version"},
		{name: "bad adapter version", change: func(cfg *Config) { cfg.ChatAdapterVersion = " v1 " }, want: "chat_adapter_version"},
		{name: "bad key", change: func(cfg *Config) { *cfg = enabled(); cfg.ChatAPIKey = "secret\nheader" }, want: "chat_api_key"},
		{name: "zero timeout", change: func(cfg *Config) { cfg.ChatTimeout = 0 }, want: "chat_timeout"},
		{name: "large timeout", change: func(cfg *Config) { cfg.ChatTimeout = 5*time.Minute + 1 }, want: "chat_timeout"},
		{name: "zero request", change: func(cfg *Config) { cfg.ChatMaxRequestBytes = 0 }, want: "chat_max_request_bytes"},
		{name: "large request", change: func(cfg *Config) { cfg.ChatMaxRequestBytes = 16<<20 + 1 }, want: "chat_max_request_bytes"},
		{name: "zero response", change: func(cfg *Config) { cfg.ChatMaxResponseBytes = 0 }, want: "chat_max_response_bytes"},
		{name: "large response", change: func(cfg *Config) { cfg.ChatMaxResponseBytes = 16<<20 + 1 }, want: "chat_max_response_bytes"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := Defaults()
			test.change(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
}

func TestChatConfigFormattingAndParseErrorsDoNotLeakPrivateValues(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	cfg.ChatProvider = ChatProviderOpenAICompatible
	cfg.ChatBaseURL = "https://chat-endpoint-secret.example.test/private"
	cfg.ChatAPIKey = "chat-api-key-secret"
	cfg.ChatModel = "chat-v1"
	cfg.ChatModelVersion = "chat-v1"
	for _, formatted := range []string{fmt.Sprint(cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		for _, private := range []string{cfg.ChatBaseURL, cfg.ChatAPIKey, "chat-endpoint-secret"} {
			if strings.Contains(formatted, private) {
				t.Fatalf("formatted config exposed %q: %s", private, formatted)
			}
		}
		for _, expected := range []string{"ChatProvider:\"openai-compatible\"", "ChatImplementation:\"direct\"", "ChatConfigured:true", "ChatModel:\"chat-v1\"", "ChatModelVersion:\"chat-v1\"", "ChatAdapterVersion:\"v1\""} {
			if !strings.Contains(formatted, expected) {
				t.Fatalf("formatted config omitted %q: %s", expected, formatted)
			}
		}
	}

	const secret = "numeric-chat-secret-canary"
	for _, key := range []string{"ZHIXU_CHAT_TIMEOUT", "ZHIXU_CHAT_MAX_REQUEST_BYTES", "ZHIXU_CHAT_MAX_RESPONSE_BYTES"} {
		_, err := LoadWithLookup("", func(candidate string) (string, bool) {
			if candidate == key {
				return secret, true
			}
			return "", false
		})
		if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), key) {
			t.Fatalf("key=%s error=%v", key, err)
		}
	}
}
