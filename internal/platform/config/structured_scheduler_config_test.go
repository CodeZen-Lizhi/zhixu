package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStructuredSchedulerDefaultsAreDirect(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	selectors := []StructuredSchedulerImplementation{
		cfg.StructuredSchedulerRAG,
		cfg.StructuredSchedulerRelation,
		cfg.StructuredSchedulerArtifact,
		cfg.StructuredSchedulerCapture,
		cfg.StructuredSchedulerOrganizing,
	}
	for index, selector := range selectors {
		if selector != StructuredSchedulerImplementationDirect {
			t.Fatalf("selector[%d]=%q", index, selector)
		}
	}
}

func TestLoadStructuredSchedulersFromYAMLAndEnvironment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`structured_scheduler_rag: direct
structured_scheduler_relation: eino
structured_scheduler_artifact: direct
structured_scheduler_capture: eino
structured_scheduler_organizing: direct
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		"ZHIXU_STRUCTURED_SCHEDULER_RAG":        "eino",
		"ZHIXU_STRUCTURED_SCHEDULER_RELATION":   "direct",
		"ZHIXU_STRUCTURED_SCHEDULER_ARTIFACT":   "eino",
		"ZHIXU_STRUCTURED_SCHEDULER_CAPTURE":    "direct",
		"ZHIXU_STRUCTURED_SCHEDULER_ORGANIZING": "eino",
	}
	cfg, err := LoadWithLookup(path, func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StructuredSchedulerRAG != StructuredSchedulerImplementationEino ||
		cfg.StructuredSchedulerRelation != StructuredSchedulerImplementationDirect ||
		cfg.StructuredSchedulerArtifact != StructuredSchedulerImplementationEino ||
		cfg.StructuredSchedulerCapture != StructuredSchedulerImplementationDirect ||
		cfg.StructuredSchedulerOrganizing != StructuredSchedulerImplementationEino {
		t.Fatalf("structured scheduler config=%s", cfg)
	}
	for _, expected := range []string{
		`StructuredSchedulerRAG:"eino"`,
		`StructuredSchedulerRelation:"direct"`,
		`StructuredSchedulerArtifact:"eino"`,
		`StructuredSchedulerCapture:"direct"`,
		`StructuredSchedulerOrganizing:"eino"`,
	} {
		if !strings.Contains(cfg.String(), expected) {
			t.Fatalf("config formatting omitted %q: %s", expected, cfg)
		}
	}
}

func TestValidateStructuredSchedulerSelectors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		field  string
		change func(*Config)
	}{
		{name: "rag", field: "structured_scheduler_rag", change: func(cfg *Config) { cfg.StructuredSchedulerRAG = "automatic" }},
		{name: "relation", field: "structured_scheduler_relation", change: func(cfg *Config) { cfg.StructuredSchedulerRelation = "automatic" }},
		{name: "artifact", field: "structured_scheduler_artifact", change: func(cfg *Config) { cfg.StructuredSchedulerArtifact = "automatic" }},
		{name: "capture", field: "structured_scheduler_capture", change: func(cfg *Config) { cfg.StructuredSchedulerCapture = "automatic" }},
		{name: "organizing", field: "structured_scheduler_organizing", change: func(cfg *Config) { cfg.StructuredSchedulerOrganizing = "automatic" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := Defaults()
			test.change(&cfg)
			for _, validate := range []func() error{cfg.Validate, cfg.ValidateModels} {
				err := validate()
				if err == nil || !strings.Contains(err.Error(), test.field) {
					t.Fatalf("want %s validation error, got %v", test.field, err)
				}
			}
		})
	}
}
