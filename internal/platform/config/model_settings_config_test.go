package config

import (
	"strings"
	"testing"
)

func TestManagedModelSettingsConfig(t *testing.T) {
	t.Parallel()
	cfg, err := LoadWithLookup("", mapLookup(map[string]string{
		"ZHIXU_MODEL_SETTINGS_MODE":       "managed",
		"ZHIXU_MODEL_SETTINGS_KEY_FILE":   "/run/secrets/model-settings.key",
		"ZHIXU_MODEL_SETTINGS_ROLLOUT_ID": "rollout-1",
		"ZHIXU_MODEL_SETTINGS_PREPARED":   "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelSettingsMode != ModelSettingsModeManaged || !cfg.ModelSettingsPrepared {
		t.Fatalf("managed settings not loaded: %#v", cfg)
	}
	for _, sensitive := range []string{cfg.ModelSettingsKeyFile, cfg.ModelSettingsRolloutID} {
		if strings.Contains(cfg.String(), sensitive) {
			t.Fatalf("config summary leaked managed setting %q", sensitive)
		}
	}
}

func TestManagedModelSettingsConfigValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(*Config)
	}{
		{name: "unknown mode", edit: func(cfg *Config) { cfg.ModelSettingsMode = "unknown" }},
		{name: "managed without key", edit: func(cfg *Config) { cfg.ModelSettingsMode = ModelSettingsModeManaged }},
		{name: "relative key", edit: func(cfg *Config) { cfg.ModelSettingsMode = ModelSettingsModeManaged; cfg.ModelSettingsKeyFile = "key" }},
		{name: "prepared without rollout", edit: func(cfg *Config) {
			cfg.ModelSettingsMode = ModelSettingsModeManaged
			cfg.ModelSettingsKeyFile = "/run/key"
			cfg.ModelSettingsPrepared = true
		}},
		{name: "static managed fields", edit: func(cfg *Config) { cfg.ModelSettingsKeyFile = "/run/key" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := Defaults()
			test.edit(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
