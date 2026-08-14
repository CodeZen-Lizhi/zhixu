package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsRemovedStructuredSchedulerSelectors(t *testing.T) {
	t.Parallel()
	selectors := []struct {
		yaml string
		env  string
	}{
		{yaml: "structured_scheduler_rag", env: "ZHIXU_STRUCTURED_SCHEDULER_RAG"},
		{yaml: "structured_scheduler_relation", env: "ZHIXU_STRUCTURED_SCHEDULER_RELATION"},
		{yaml: "structured_scheduler_artifact", env: "ZHIXU_STRUCTURED_SCHEDULER_ARTIFACT"},
		{yaml: "structured_scheduler_capture", env: "ZHIXU_STRUCTURED_SCHEDULER_CAPTURE"},
		{yaml: "structured_scheduler_organizing", env: "ZHIXU_STRUCTURED_SCHEDULER_ORGANIZING"},
	}
	for _, selector := range selectors {
		selector := selector
		t.Run(selector.yaml+"/yaml", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(selector.yaml+": eino\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadWithLookup(path, func(string) (string, bool) { return "", false })
			if err == nil || !strings.Contains(err.Error(), selector.yaml) {
				t.Fatalf("error=%v", err)
			}
		})
		t.Run(selector.yaml+"/environment", func(t *testing.T) {
			_, err := LoadWithLookup("", func(key string) (string, bool) {
				if key == selector.env {
					return "direct", true
				}
				return "", false
			})
			if err == nil || !strings.Contains(err.Error(), selector.env) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
