package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsRemovedEmbeddingImplementationSelector(t *testing.T) {
	t.Parallel()
	t.Run("yaml", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("embedding_implementation: eino\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadWithLookup(path, func(string) (string, bool) { return "", false })
		if err == nil || !strings.Contains(err.Error(), "embedding_implementation") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("environment", func(t *testing.T) {
		_, err := LoadWithLookup("", func(key string) (string, bool) {
			if key == "ZHIXU_EMBEDDING_IMPLEMENTATION" {
				return "direct", true
			}
			return "", false
		})
		if err == nil || !strings.Contains(err.Error(), "ZHIXU_EMBEDDING_IMPLEMENTATION") {
			t.Fatalf("error=%v", err)
		}
	})
}
