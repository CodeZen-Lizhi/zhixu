package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadWithLookupYAMLWireCompatibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		yaml        string
		wantErr     bool
		wantAppName string
		wantVersion string
		wantWorkers int
	}{
		{name: "known null is omitted", yaml: "app_name: null\n", wantAppName: "zhixu", wantVersion: defaultVersion},
		{name: "known empty string overrides default", yaml: "app_name: \"\"\n", wantErr: true},
		{name: "known empty map is rejected", yaml: "app_name: {}\n", wantErr: true},
		{name: "known bool empty map is rejected", yaml: "auth_secure_cookie: {}\n", wantErr: true},
		{name: "known list empty map is rejected", yaml: "auth_allowed_origins: {}\n", wantErr: true},
		{name: "unknown null is rejected", yaml: "unexpected: null\n", wantErr: true},
		{name: "unknown empty map is rejected", yaml: "unexpected: {}\n", wantErr: true},
		{name: "key case is exact", yaml: "App_Name: changed\n", wantErr: true},
		{name: "duplicate key is rejected", yaml: "app_name: first\napp_name: second\n", wantErr: true},
		{name: "additional document is ignored", yaml: "app_name: first\n---\napp_name: second\n", wantAppName: "first", wantVersion: defaultVersion},
		{name: "scalar alias is resolved", yaml: "app_name: &shared aliased\nversion: *shared\n", wantAppName: "aliased", wantVersion: "aliased"},
		{name: "merge key is resolved", yaml: "<<: {app_name: merged}\n", wantAppName: "merged", wantVersion: defaultVersion},
		{name: "non mapping root is rejected", yaml: "- app_name\n- value\n", wantErr: true},
		{name: "integral float coerces to integer", yaml: "worker_max_workers: 1.0\n", wantAppName: "zhixu", wantVersion: defaultVersion, wantWorkers: 1},
		{name: "scalar does not coerce to list", yaml: "auth_allowed_origins: http://127.0.0.1:8080\n", wantErr: true},
		{name: "empty list overrides default", yaml: "web_fetch_allowed_content_types: []\n", wantErr: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(test.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadWithLookup(path, func(string) (string, bool) { return "", false })
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected YAML %q to fail", test.yaml)
				}
				return
			}
			if err != nil {
				t.Fatalf("load YAML %q: %v", test.yaml, err)
			}
			if cfg.AppName != test.wantAppName || cfg.Version != test.wantVersion {
				t.Fatalf("unexpected YAML result: app_name=%q version=%q", cfg.AppName, cfg.Version)
			}
			if test.wantWorkers != 0 && cfg.WorkerMaxWorkers != test.wantWorkers {
				t.Fatalf("unexpected worker count: %d", cfg.WorkerMaxWorkers)
			}
		})
	}
}

func TestLoadWithLookupRejectsFractionalYAMLInteger(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"1.5", "1.0000000000000001"} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("worker_max_workers: "+value+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadWithLookup(path, func(string) (string, bool) { return "", false }); err == nil {
				t.Fatalf("expected fractional YAML number %s to fail integer decoding", value)
			}
		})
	}
}

func TestLoadWithLookupRejectsOverflowingYAMLInteger(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"2147483648",
		"-2147483649",
		"2147483648.0",
		"-2147483649.0",
		"4294967296",
	} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("database_max_conns: "+value+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadWithLookup(path, func(string) (string, bool) { return "", false }); err == nil {
				t.Fatalf("expected out-of-range YAML integer %s to fail", value)
			}
		})
	}
}

func TestLoadWithLookupAcceptsYAMLIntegerWidthBoundaries(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"2147483647", "2147483647.0"} {
		value := value
		t.Run("maximum_"+value, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("database_max_conns: "+value+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadWithLookup(path, func(string) (string, bool) { return "", false })
			if err != nil {
				t.Fatalf("load maximum int32 value %s: %v", value, err)
			}
			if cfg.DatabaseMaxConns != 2147483647 {
				t.Fatalf("maximum int32 value decoded as %d", cfg.DatabaseMaxConns)
			}
		})
	}

	for _, value := range []string{"-2147483648", "-2147483648.0"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("database_max_conns: "+value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadWithLookup(path, func(string) (string, bool) { return "", false })
		if err == nil || !strings.Contains(err.Error(), "database pool sizes must not be negative") {
			t.Fatalf("minimum int32 value %s should reach business validation, got %v", value, err)
		}
	}
}

func TestLoadWithLookupPreservesExplicitEmptyEnvironmentOverride(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("app_name: from-yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadWithLookup(path, func(key string) (string, bool) {
		if key == "ZHIXU_APP_NAME" {
			return "", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "app_name must not be empty") {
		t.Fatalf("explicit empty environment value did not override YAML: %v", err)
	}
}

func TestLoadWithLookupPreservesExplicitEmptyYAMLReviewKey(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("review_question_ref_key: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadWithLookup(path, func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "review_question_ref_key") {
		t.Fatalf("explicit empty YAML Review key was silently materialized: %v", err)
	}
}

func TestLoadNonAPIClearsYAMLAPISecretsWithoutEnvironmentLookup(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := strings.Join([]string{
		"auth_bootstrap_token: yaml-bootstrap-secret",
		"review_question_ref_key: yaml-review-secret-with-at-least-32-bytes",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	secretLookups := map[string]int{
		"ZHIXU_AUTH_BOOTSTRAP_TOKEN":    0,
		"ZHIXU_REVIEW_QUESTION_REF_KEY": 0,
	}
	cfg, err := loadNonAPIWithLookup(path, func(key string) (string, bool) {
		if _, secret := secretLookups[key]; secret {
			secretLookups[key]++
			return "environment-secret-must-not-be-read", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, count := range secretLookups {
		if count != 0 {
			t.Errorf("non-API profile looked up %s %d times", key, count)
		}
	}
	if cfg.AuthBootstrapToken != "" || cfg.ReviewQuestionRefKey != "" {
		t.Fatalf("non-API profile retained YAML API Secret: %s", cfg)
	}
}

func TestLoadWithLookupUsesIsolatedViperInstances(t *testing.T) {
	t.Parallel()
	first, err := LoadWithLookup("", func(key string) (string, bool) {
		if key == "ZHIXU_APP_NAME" {
			return "first-load", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadWithLookup("", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if first.AppName != "first-load" || second.AppName != "zhixu" {
		t.Fatalf("configuration leaked across loads: first=%q second=%q", first.AppName, second.AppName)
	}
	first.AuthAllowedOrigins[0] = "http://mutated.invalid"
	third, err := LoadWithLookup("", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if third.AuthAllowedOrigins[0] != "http://127.0.0.1:8080" {
		t.Fatalf("default slice leaked across loads: %v", third.AuthAllowedOrigins)
	}
}

func TestLoadWithLookupDisabledCapabilitiesDoNotLookupDependentSecrets(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := strings.Join([]string{
		"chat_provider: openai-compatible",
		"chat_base_url: https://chat.example.test",
		"chat_api_key: yaml-chat-secret",
		"chat_model: yaml-chat-model",
		"chat_model_version: yaml-chat-v1",
		"embedding_provider: openai-compatible",
		"embedding_base_url: https://embedding.example.test",
		"embedding_api_key: yaml-embedding-secret",
		"embedding_model: yaml-embedding-model",
		"embedding_dimensions: 3",
		"telemetry_mode: optional",
		"telemetry_endpoint: https://telemetry.example.test",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	protectedLookups := map[string]int{
		"ZHIXU_CHAT_API_KEY":          0,
		"ZHIXU_EMBEDDING_API_KEY":     0,
		"OTEL_EXPORTER_OTLP_ENDPOINT": 0,
	}
	cfg, err := LoadWithLookup(path, func(key string) (string, bool) {
		if _, protected := protectedLookups[key]; protected {
			protectedLookups[key]++
			return "must-not-be-read", true
		}
		switch key {
		case "ZHIXU_CHAT_PROVIDER", "ZHIXU_EMBEDDING_PROVIDER", "ZHIXU_TELEMETRY_MODE":
			return "disabled", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, count := range protectedLookups {
		if count != 0 {
			t.Errorf("disabled capability looked up %s %d times", key, count)
		}
	}
	if cfg.ChatAPIKey != "" || cfg.EmbeddingAPIKey != "" || cfg.TelemetryEndpoint != "" {
		t.Fatalf("disabled capability retained dependent configuration: %s", cfg)
	}
}

func TestLoadWithLookupYAMLDisabledCapabilitiesClearValuesWithoutSecretLookup(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := strings.Join([]string{
		"chat_provider: disabled",
		"chat_base_url: https://chat.example.test",
		"chat_api_key: yaml-chat-secret",
		"chat_model: yaml-chat-model",
		"chat_model_version: yaml-chat-v1",
		"embedding_provider: disabled",
		"embedding_base_url: https://embedding.example.test",
		"embedding_api_key: yaml-embedding-secret",
		"embedding_model: yaml-embedding-model",
		"embedding_dimensions: 3",
		"telemetry_mode: disabled",
		"telemetry_endpoint: https://telemetry.example.test",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	protectedLookups := map[string]int{
		"ZHIXU_CHAT_API_KEY":          0,
		"ZHIXU_EMBEDDING_API_KEY":     0,
		"OTEL_EXPORTER_OTLP_ENDPOINT": 0,
	}
	cfg, err := LoadWithLookup(path, func(key string) (string, bool) {
		if _, protected := protectedLookups[key]; protected {
			protectedLookups[key]++
			return "must-not-be-read", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, count := range protectedLookups {
		if count != 0 {
			t.Errorf("YAML-disabled capability looked up %s %d times", key, count)
		}
	}
	if cfg.ChatBaseURL != "" || cfg.ChatAPIKey != "" || cfg.ChatModel != "" || cfg.ChatModelVersion != "" ||
		cfg.EmbeddingBaseURL != "" || cfg.EmbeddingAPIKey != "" || cfg.EmbeddingModel != "" || cfg.EmbeddingDimensions != 0 ||
		cfg.TelemetryEndpoint != "" {
		t.Fatalf("YAML-disabled capability retained dependent configuration: %s", cfg)
	}
}

func TestConfigLoaderEnvironmentRegistryIsValid(t *testing.T) {
	t.Parallel()
	fields, err := configFieldTypes()
	if err != nil {
		t.Fatal(err)
	}
	seenEnvironment := make(map[string]string)
	for _, specs := range [][]envSpec{selectorEnvSpecs, valueEnvSpecs} {
		for _, spec := range specs {
			if _, ok := fields[spec.configKey]; !ok {
				t.Errorf("environment %s refers to unknown config key %s", spec.envKey, spec.configKey)
			}
			if previous, duplicate := seenEnvironment[spec.envKey]; duplicate {
				t.Errorf("environment %s is registered for both %s and %s", spec.envKey, previous, spec.configKey)
			}
			seenEnvironment[spec.envKey] = spec.configKey
		}
	}
	for _, key := range []string{"ZHIXU_AUTH_BOOTSTRAP_TOKEN", "ZHIXU_REVIEW_QUESTION_REF_KEY"} {
		configKey, ok := seenEnvironment[key]
		if !ok {
			t.Errorf("API-only environment %s is not registered", key)
			continue
		}
		for _, spec := range valueEnvSpecs {
			if spec.configKey == configKey && !spec.apiOnly {
				t.Errorf("API-only environment %s is not profile-restricted", key)
			}
		}
	}
}

func TestLoadWithLookupYAMLDecodeErrorsDoNotExposeValues(t *testing.T) {
	t.Parallel()
	const secret = "yaml-decode-secret-canary"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("database_password: ["+secret+"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadWithLookup(path, func(string) (string, bool) { return "", false })
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "database_password") {
		t.Fatalf("expected field-specific secret-safe decode error, got %v", err)
	}
}

func TestLoadWithLookupCommonEnvironmentRegistry(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"ZHIXU_VERSION":               "v-test",
		"ZHIXU_DATABASE_HOST":         "database.internal",
		"ZHIXU_DATABASE_PORT":         "5433",
		"ZHIXU_DATABASE_NAME":         "knowledge",
		"ZHIXU_DATABASE_USER":         "app",
		"ZHIXU_DATABASE_PASSWORD":     "database-password",
		"ZHIXU_DATABASE_MAX_CONNS":    "12",
		"ZHIXU_DATABASE_MIN_CONNS":    "2",
		"ZHIXU_DATABASE_PING_TIMEOUT": "3s",
		"ZHIXU_SHUTDOWN_TIMEOUT":      "14s",
		"ZHIXU_WEB_ASSETS_DIR":        "/srv/zhixu/web",
	}
	cfg, err := LoadWithLookup("", func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != "v-test" || cfg.DatabaseHost != "database.internal" || cfg.DatabasePort != "5433" ||
		cfg.DatabaseName != "knowledge" || cfg.DatabaseUser != "app" || cfg.DatabasePassword != "database-password" ||
		cfg.DatabaseMaxConns != 12 || cfg.DatabaseMinConns != 2 || cfg.DatabasePingTimeout != 3*time.Second ||
		cfg.ShutdownTimeout != 14*time.Second || cfg.WebAssetsDir != "/srv/zhixu/web" {
		t.Fatalf("common environment registry did not apply all values: %s", cfg)
	}
}

func TestLoadNonAPIKeepsSharedValidationScope(t *testing.T) {
	t.Parallel()
	_, err := loadNonAPIWithLookup("", func(key string) (string, bool) {
		if key == "ZHIXU_WORKER_MAX_WORKERS" {
			return "0", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "worker_max_workers") {
		t.Fatalf("non-API profile skipped shared worker validation: %v", err)
	}
}

func TestLoadWithLookupDoesNotAddModelSettingsModeLookupGates(t *testing.T) {
	t.Parallel()
	t.Run("static still looks up managed fields", func(t *testing.T) {
		t.Parallel()
		lookups := 0
		_, err := LoadWithLookup("", func(key string) (string, bool) {
			if key == "ZHIXU_MODEL_SETTINGS_KEY_FILE" {
				lookups++
				return "/run/secrets/model-settings.key", true
			}
			return "", false
		})
		if lookups != 1 || err == nil || !strings.Contains(err.Error(), "managed model settings fields must be empty") {
			t.Fatalf("static mode changed managed-field lookup semantics: lookups=%d err=%v", lookups, err)
		}
	})

	t.Run("managed still looks up static provider fields", func(t *testing.T) {
		t.Parallel()
		values := map[string]string{
			"ZHIXU_MODEL_SETTINGS_MODE":     "managed",
			"ZHIXU_MODEL_SETTINGS_KEY_FILE": "/run/secrets/model-settings.key",
			"ZHIXU_CHAT_PROVIDER":           "openai-compatible",
			"ZHIXU_CHAT_BASE_URL":           "https://chat.example.test",
			"ZHIXU_CHAT_API_KEY":            "static-chat-secret",
			"ZHIXU_CHAT_MODEL":              "chat-model",
			"ZHIXU_CHAT_MODEL_VERSION":      "chat-v1",
		}
		secretLookups := 0
		cfg, err := LoadWithLookup("", func(key string) (string, bool) {
			if key == "ZHIXU_CHAT_API_KEY" {
				secretLookups++
			}
			value, ok := values[key]
			return value, ok
		})
		if err != nil {
			t.Fatal(err)
		}
		if secretLookups != 1 || cfg.ChatAPIKey != values["ZHIXU_CHAT_API_KEY"] {
			t.Fatalf("managed mode changed static-provider lookup semantics: lookups=%d configured=%t", secretLookups, cfg.ChatAPIKey != "")
		}
	})
}
