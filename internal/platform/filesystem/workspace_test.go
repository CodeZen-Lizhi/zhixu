package filesystem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootResolveRejectsTraversalAndOutsideSymlink(t *testing.T) {
	rootPath := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "escape")); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret.txt", filepath.Join(rootPath, "secret.txt"), "escape/secret.txt"} {
		if _, err := root.Resolve(path); err == nil {
			t.Fatalf("Resolve(%q) succeeded", path)
		}
	}
}

func TestScanFiltersSortsAndHashesSupportedFiles(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"z.txt":        "same",
		"a.md":         "first",
		"ignore.bin":   "binary",
		".git/config":  "hidden",
		"tmp/cache.md": "temporary",
	}
	for path, content := range files {
		full := filepath.Join(rootPath, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := root.Scan(ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].RelativePath != "a.md" || got[1].RelativePath != "z.txt" {
		t.Fatalf("scan = %#v", got)
	}
	if got[0].SHA256 == "" || got[0].MediaType == "application/octet-stream" {
		t.Fatalf("metadata = %#v", got[0])
	}
	if got[0].SHA256 == got[1].SHA256 {
		t.Fatalf("different content has same hash: %#v", got)
	}
}

func TestScanRejectsOversizedFile(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "large.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Scan(ScanOptions{MaxBytes: 4}); err == nil {
		t.Fatal("scan succeeded for oversized file")
	}
}

func TestNewRootRejectsMissingOrFilePath(t *testing.T) {
	if _, err := NewRoot(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing root succeeded")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRoot(file); err == nil {
		t.Fatal("file root succeeded")
	}
}

func TestScanDoesNotSilentlyAcceptInvalidRoot(t *testing.T) {
	var root Root
	_, err := root.Scan(ScanOptions{})
	if err == nil || !strings.Contains(err.Error(), "workspace root is not initialized") {
		t.Fatalf("error = %v", err)
	}
}
