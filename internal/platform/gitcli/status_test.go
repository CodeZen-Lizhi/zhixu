package gitcli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestClientStatusReadsRepositoryBaseline(t *testing.T) {
	root := initRepository(t)
	client := New("")

	status, err := client.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Present || status.RepositoryPath != root || status.Head == "" || status.Branch == "" || status.Dirty {
		t.Fatalf("status = %#v", status)
	}

	if err := os.WriteFile(filepath.Join(root, "untracked.md"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err = client.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Dirty {
		t.Fatalf("dirty status = %#v", status)
	}
}

func TestClientStatusReportsMissingRepository(t *testing.T) {
	status, err := New("").Status(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if status.Present {
		t.Fatalf("status = %#v", status)
	}
}

func TestClientStatusReportsContainingRepositoryRoot(t *testing.T) {
	root := initRepository(t)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	status, err := New("").Status(context.Background(), nested)
	if err != nil {
		t.Fatal(err)
	}
	if status.RepositoryPath != root {
		t.Fatalf("repository path = %q, want %q", status.RepositoryPath, root)
	}
}

func TestClientStatusClassifiesUnavailableCommand(t *testing.T) {
	_, err := New(filepath.Join(t.TempDir(), "missing-git")).Status(context.Background(), t.TempDir())
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorDependencyUnavailable || classified.Code != "GIT_COMMAND_UNAVAILABLE" || classified.Retryable {
		t.Fatalf("error = %#v", err)
	}
}

func TestClientStatusRejectsEmptyRoot(t *testing.T) {
	_, err := New("").Status(context.Background(), "  ")
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != "GIT_ROOT_REQUIRED" {
		t.Fatalf("error = %#v", err)
	}
}

func TestClientStatusClassifiesCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New("").Status(ctx, t.TempDir())
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorRetryableFailure || classified.Code != "GIT_STATUS_TIMEOUT" || !classified.Retryable {
		t.Fatalf("error = %#v", err)
	}
}

func initRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "--initial-branch=main")
	runGit(t, root, "config", "user.name", "Zhixu Test")
	runGit(t, root, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("baseline"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "initial")
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
