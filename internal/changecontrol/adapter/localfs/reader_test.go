package localfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

type fakeWorkspaces struct{ workspace workspacedomain.Workspace }

func (f fakeWorkspaces) GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error) {
	return f.workspace, nil
}

func TestReaderUsesWorkspaceBoundary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(fakeWorkspaces{workspace: workspacedomain.Workspace{ID: "workspace", RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := reader.CurrentHash(context.Background(), "workspace", "a.md")
	if err != nil || digest != "ed7002b439e9ac845f22357d822bac1444730fbdb6016d3ec9432297b9ec9f73" {
		t.Fatalf("CurrentHash() = %q, %v", digest, err)
	}
	if _, err := reader.CurrentHash(context.Background(), "workspace", "../outside.md"); err == nil {
		t.Fatal("CurrentHash() accepted traversal")
	}
}

func TestReaderRejectsWorkspaceRepositoryBindingMismatch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(fakeWorkspaces{workspace: workspacedomain.Workspace{ID: "other-workspace", RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.CurrentHash(context.Background(), "workspace", "a.md")
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation || classified.Code != "PROPOSAL_WORKSPACE_BINDING_INVALID" {
		t.Fatalf("CurrentHash() error = %#v, want workspace binding consistency error", err)
	}
	_, _, err = reader.CurrentContent(context.Background(), "workspace", "a.md", 64)
	classified = nil
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation || classified.Code != "PROPOSAL_WORKSPACE_BINDING_INVALID" {
		t.Fatalf("CurrentContent() error = %#v, want workspace binding consistency error", err)
	}
}

func TestReaderCurrentHashRejectsSensitiveAndUnsafeTargets(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".knowledge"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		".env":                  "SECRET=should-not-read",
		".git/config":           "[remote]\n",
		".git/README.md":        "private repository metadata",
		".knowledge/secret.md":  "private",
		"plain.txt":             "not markdown",
		"nested/valid.markdown": "valid",
		"nested/another.md":     "valid",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nested/valid.markdown", filepath.Join(root, "target.md")); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(fakeWorkspaces{workspace: workspacedomain.Workspace{ID: "workspace", RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.CurrentHash(context.Background(), "workspace", "nested/valid.markdown"); err != nil {
		t.Fatalf("CurrentHash() rejected nested Markdown: %v", err)
	}
	for _, target := range []string{".env", ".git/config", ".git/README.md", ".knowledge/secret.md", "plain.txt", "linked/secret.md", "target.md"} {
		t.Run(target, func(t *testing.T) {
			if _, err := reader.CurrentHash(context.Background(), "workspace", target); err == nil {
				t.Fatalf("CurrentHash() accepted unsafe target %q", target)
			}
		})
	}
}

func TestReaderCurrentContentRejectsUnsafeParentAndTargetSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "a.md"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "safe.md"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("safe.md", filepath.Join(root, "target.md")); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(fakeWorkspaces{workspace: workspacedomain.Workspace{ID: "workspace", RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"linked/a.md", "target.md"} {
		t.Run(target, func(t *testing.T) {
			if _, _, err := reader.CurrentContent(context.Background(), "workspace", target, 64); err == nil {
				t.Fatalf("CurrentContent() accepted unsafe target %q", target)
			}
		})
	}
}

func TestReaderCurrentContentReturnsBoundedUTF8Snapshot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("当前内容"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(fakeWorkspaces{workspace: workspacedomain.Workspace{ID: "workspace", RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	content, digest, err := reader.CurrentContent(context.Background(), "workspace", "a.md", 64)
	if err != nil || string(content) != "当前内容" || digest != "df492b6fdd3b8de31d32f97fefa6b3795f22fb139371fbe4c02d6c08cbf5508a" {
		t.Fatalf("CurrentContent() content=%q digest=%q err=%v", string(content), digest, err)
	}
	if _, _, err := reader.CurrentContent(context.Background(), "workspace", "a.md", 2); err == nil {
		t.Fatal("CurrentContent() accepted content above limit")
	}
	if err := os.WriteFile(filepath.Join(root, "invalid.md"), []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.CurrentContent(context.Background(), "workspace", "invalid.md", 64); err == nil {
		t.Fatal("CurrentContent() accepted invalid UTF-8")
	}
}

func TestReaderCurrentContentRejectsUnsafeTargetsWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "regular.md"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("regular.md", filepath.Join(root, "symlink.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(fakeWorkspaces{workspace: workspacedomain.Workspace{ID: "workspace", RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"symlink.md", "directory.md", "fifo.md"} {
		t.Run(target, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				_, _, readErr := reader.CurrentContent(context.Background(), "workspace", target, 64)
				done <- readErr
			}()
			select {
			case readErr := <-done:
				if readErr == nil {
					t.Fatalf("CurrentContent() accepted unsafe target %q", target)
				}
			case <-time.After(time.Second):
				t.Fatalf("CurrentContent() blocked on unsafe target %q", target)
			}
		})
	}
}
