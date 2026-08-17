package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceAnalysisConfigurationDefaultsOff(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	if cfg.WorkspaceAnalysisAPIEnabled || cfg.WorkspaceAnalysisWorkerEnabled || cfg.WorkspaceAnalysisConfigRevision != 1 {
		t.Fatalf("workspace analysis defaults must stay off with revision one: %s", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default configuration must remain valid: %v", err)
	}
}

func TestWorkspaceAnalysisConfigurationLoadsIndependentYAMLAndEnvironmentValues(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("workspace_analysis_api_enabled: true\nworkspace_analysis_worker_enabled: false\nworkspace_analysis_config_revision: 7\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"ZHIXU_WORKSPACE_ANALYSIS_API_ENABLED":     "false",
		"ZHIXU_WORKSPACE_ANALYSIS_WORKER_ENABLED":  "true",
		"ZHIXU_WORKSPACE_ANALYSIS_CONFIG_REVISION": "9",
	}
	cfg, err := LoadWithLookup(path, func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkspaceAnalysisAPIEnabled || !cfg.WorkspaceAnalysisWorkerEnabled || cfg.WorkspaceAnalysisConfigRevision != 9 {
		t.Fatalf("workspace analysis environment overrides were not applied independently: %s", cfg)
	}
}

func TestWorkspaceAnalysisConfigurationRejectsNonPositiveRevision(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	cfg.WorkspaceAnalysisConfigRevision = 0
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "workspace_analysis_config_revision") {
		t.Fatalf("expected revision validation error, got %v", err)
	}
}

func TestWorkspaceAnalysisConfigurationStringExposesOnlySafeRolloutState(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	cfg.WorkspaceAnalysisAPIEnabled = true
	cfg.WorkspaceAnalysisWorkerEnabled = true
	cfg.WorkspaceAnalysisConfigRevision = 11
	summary := cfg.String()
	for _, marker := range []string{
		"WorkspaceAnalysisAPIEnabled:true",
		"WorkspaceAnalysisWorkerEnabled:true",
		"WorkspaceAnalysisConfigRevision:11",
	} {
		if !strings.Contains(summary, marker) {
			t.Fatalf("configuration summary is missing %q: %s", marker, summary)
		}
	}
}
