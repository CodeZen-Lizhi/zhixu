package gitcli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	changecontrol "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const writebackTestWorkspaceID foundation.ID = "workspace"

type writebackWorkspaceRepository struct {
	workspace workspacedomain.Workspace
	err       error
}

type mismatchedWorkspaceRepository struct {
	workspace workspacedomain.Workspace
}

func (r mismatchedWorkspaceRepository) GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error) {
	return r.workspace, nil
}

func (r writebackWorkspaceRepository) GetWorkspaceByID(_ context.Context, id foundation.ID) (workspacedomain.Workspace, error) {
	if r.err != nil {
		return workspacedomain.Workspace{}, r.err
	}
	if id != r.workspace.ID {
		return workspacedomain.Workspace{}, errors.New("workspace not found")
	}
	return r.workspace, nil
}

type writebackTestRepository struct {
	root   string
	target string
	head   string
	client *WritebackClient
}

func workspaceForWritebackTest(root string) workspacedomain.Workspace {
	return workspacedomain.Workspace{ID: writebackTestWorkspaceID, RootPath: root}
}

func newWritebackTestRepository(t *testing.T, objectFormat, target string) writebackTestRepository {
	t.Helper()
	root := t.TempDir()
	args := []string{"init", "--initial-branch=main"}
	if objectFormat != "" {
		args = append(args, "--object-format="+objectFormat)
	}
	runWritebackGit(t, root, args...)
	runWritebackGit(t, root, "config", "user.name", "Zhixu Test")
	runWritebackGit(t, root, "config", "user.email", "test@example.invalid")
	if target == "" {
		target = "notes/target.md"
	}
	pathValue := filepath.Join(root, filepath.FromSlash(target))
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathValue, []byte("# baseline\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, root, "add", "--", target)
	runWritebackGit(t, root, "commit", "-m", "initial")
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewWritebackClient(New(""), writebackWorkspaceRepository{workspace: workspaceForWritebackTest(canonical)})
	if err != nil {
		t.Fatal(err)
	}
	return writebackTestRepository{
		root:   canonical,
		target: target,
		head:   strings.TrimSpace(runWritebackGit(t, canonical, "rev-parse", "HEAD")),
		client: client,
	}
}

func (r writebackTestRepository) writeTarget(t *testing.T, content []byte) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.root, filepath.FromSlash(r.target)), content, 0o640); err != nil {
		t.Fatal(err)
	}
	return changecontrol.ComputeWritebackResultHash(content)
}

func (r writebackTestRepository) diffRequest(resultHash string) changecontrol.GitDiffRequest {
	return changecontrol.GitDiffRequest{
		WorkspaceID:     writebackTestWorkspaceID,
		TargetPath:      r.target,
		ApprovedGitHead: r.head,
		ResultHash:      resultHash,
	}
}

func runWritebackGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	commandArgs := []string{"--no-pager", "-c", "core.hooksPath=" + os.DevNull, "-C", root}
	command := exec.Command("git", append(commandArgs, args...)...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func requireGitErrorCode(t *testing.T, err error, code string, kind foundation.ErrorKind) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code || classified.Kind != kind {
		t.Fatalf("error = %#v, want code=%s kind=%s", err, code, kind)
	}
}
