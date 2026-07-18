package gitcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestReadCommittedBlobIgnoresWorktreeDrift(t *testing.T) {
	repository := newWritebackTestRepository(t, "sha1", "notes/target.md")
	if err := os.WriteFile(filepath.Join(repository.root, "notes", "target.md"), []byte("drifted worktree\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	blob, err := repository.client.ReadCommittedBlob(context.Background(), writebackTestWorkspaceID, repository.head, repository.target)
	if err != nil {
		t.Fatal(err)
	}
	if blob.WorkspaceID != writebackTestWorkspaceID || blob.Commit != repository.head || blob.RelativePath != repository.target || string(blob.Bytes) != "# baseline\n" {
		t.Fatalf("blob=%#v", blob)
	}
}

func TestReadCommittedBlobRejectsUnsafeBindingAndNonCommitObject(t *testing.T) {
	repository := newWritebackTestRepository(t, "sha1", "notes/target.md")
	tests := []struct {
		name   string
		commit string
		path   string
	}{
		{name: "uppercase commit", commit: strings.ToUpper(repository.head), path: repository.target},
		{name: "escaped path", commit: repository.head, path: "notes/../target.md"},
		{name: "backslash path", commit: repository.head, path: `notes\target.md`},
		{name: "missing path", commit: repository.head, path: "notes/missing.md"},
		{name: "tree object", commit: strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD^{tree}")), path: repository.target},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := repository.client.ReadCommittedBlob(context.Background(), writebackTestWorkspaceID, test.commit, test.path); err == nil {
				t.Fatal("unsafe committed blob binding accepted")
			}
		})
	}
}

func TestReadCommittedBlobHonorsCancelledContext(t *testing.T) {
	repository := newWritebackTestRepository(t, "sha1", "notes/target.md")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := repository.client.ReadCommittedBlob(ctx, writebackTestWorkspaceID, repository.head, repository.target)
	requireGitErrorCode(t, err, "GIT_COMMAND_CANCELLED", foundation.ErrorNonRetryableFailure)
}
