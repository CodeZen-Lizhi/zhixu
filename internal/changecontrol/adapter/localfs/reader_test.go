package localfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
