package live

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigDisabledByDefault(t *testing.T) {
	config, enabled, err := loadConfig(mapLookup(nil))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if enabled {
		t.Fatal("live smoke is enabled without the explicit flag")
	}
	if config != (Config{}) {
		t.Fatalf("config = %#v, want zero value", config)
	}
}

func TestLoadConfigEnabledUsesValidatedDefaults(t *testing.T) {
	config, enabled, err := loadConfig(mapLookup(map[string]string{
		EnvEnabled: "true",
		EnvAPIKey:  "test-key",
		EnvModel:   "test-model",
		EnvBaseURL: "http://127.0.0.1:8080/v1",
	}))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !enabled {
		t.Fatal("live smoke is disabled")
	}
	if config.Timeout != defaultTimeout {
		t.Fatalf("timeout = %s, want %s", config.Timeout, defaultTimeout)
	}
}

func TestLoadConfigRejectsInvalidEnabledFlag(t *testing.T) {
	_, enabled, err := loadConfig(mapLookup(map[string]string{EnvEnabled: "sometimes"}))
	if enabled {
		t.Fatal("live smoke is enabled with an invalid flag")
	}
	if err == nil || !strings.Contains(err.Error(), EnvEnabled) {
		t.Fatalf("error = %v, want invalid enabled flag error", err)
	}
}

func TestLoadConfigMissingCredentialsIsExplicit(t *testing.T) {
	_, enabled, err := loadConfig(mapLookup(map[string]string{EnvEnabled: "true"}))
	if !enabled {
		t.Fatal("live smoke is disabled")
	}

	var missing *MissingRequiredConfigError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want *MissingRequiredConfigError", err)
	}
	if len(missing.EnvironmentVariables) != 2 {
		t.Fatalf("missing variables = %v, want key and model", missing.EnvironmentVariables)
	}
}

func TestLoadConfigRejectsInvalidTimeout(t *testing.T) {
	_, enabled, err := loadConfig(mapLookup(map[string]string{
		EnvEnabled: "true",
		EnvAPIKey:  "test-key",
		EnvModel:   "test-model",
		EnvTimeout: "not-a-duration",
	}))
	if !enabled {
		t.Fatal("live smoke is disabled")
	}
	if err == nil || !strings.Contains(err.Error(), EnvTimeout) {
		t.Fatalf("error = %v, want invalid timeout error", err)
	}
}

func TestConfigValidateBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{name: "provider default", baseURL: ""},
		{name: "https", baseURL: "https://api.example.com/v1"},
		{name: "local compatible endpoint", baseURL: "http://127.0.0.1:11434/v1"},
		{name: "remote plaintext endpoint", baseURL: "http://api.example.com/v1", wantErr: true},
		{name: "unsupported scheme", baseURL: "ftp://api.example.com/v1", wantErr: true},
		{name: "missing host", baseURL: "https:///v1", wantErr: true},
		{name: "embedded credentials", baseURL: "https://user:password@api.example.com/v1", wantErr: true},
		{name: "fragment", baseURL: "https://api.example.com/v1#models", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (Config{
				APIKey:  "test-key",
				Model:   "test-model",
				BaseURL: test.baseURL,
				Timeout: time.Second,
			}).Validate()
			if test.wantErr && err == nil {
				t.Fatal("Validate() succeeded, want failure")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestConfigValidateRejectsNonPositiveTimeout(t *testing.T) {
	err := (Config{
		APIKey: "test-key",
		Model:  "test-model",
	}).Validate()
	if err == nil {
		t.Fatal("Validate() succeeded, want timeout failure")
	}
}

func mapLookup(values map[string]string) lookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
