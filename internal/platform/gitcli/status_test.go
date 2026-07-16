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

func TestClientStatusIgnoresInheritedGitRepositoryRedirection(t *testing.T) {
	root := initRepository(t)
	redirected := initRepository(t)
	t.Setenv("GIT_DIR", filepath.Join(redirected, ".git"))
	t.Setenv("GIT_WORK_TREE", redirected)

	status, err := New("").Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if status.RepositoryPath != root {
		t.Fatalf("repository path = %q, want %q", status.RepositoryPath, root)
	}
}

func TestClientInitializeIgnoresInheritedGitRepositoryRedirection(t *testing.T) {
	root := t.TempDir()
	redirected := initRepository(t)
	t.Setenv("GIT_DIR", filepath.Join(redirected, ".git"))
	t.Setenv("GIT_WORK_TREE", redirected)

	status, err := New("").Initialize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Present || status.RepositoryPath != canonicalRoot {
		t.Fatalf("status = %#v", status)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatalf("initialized repository metadata: %v", err)
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

func TestClientStatusRejectsTrackedFilterWithoutExecutingIt(t *testing.T) {
	root := initRepository(t)
	marker := filepath.Join(root, "filter-ran")
	filter := writeExecutable(t, "#!/bin/sh\nprintf ran > "+marker+"\ncat\n")
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte("*.md filter=evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "config", "filter.evil.clean", filter)
	runGit(t, root, "config", "filter.evil.smudge", filter)
	runGit(t, root, "config", "filter.evil.required", "true")

	_, err := New("").Status(context.Background(), root)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorPermissionDenied || classified.Code != "GIT_REPOSITORY_FILTER_UNSAFE" {
		t.Fatalf("error = %#v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("filter ran or cannot be inspected: %v", err)
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

func isFoundationError(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
