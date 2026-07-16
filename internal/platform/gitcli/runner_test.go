package gitcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCommandUsesFixedArgumentsAndSanitizedEnvironment(t *testing.T) {
	executable := writeExecutable(t, `#!/bin/sh
printf 'arg=%s\n' "$@"
env | sort
`)
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "redirected.git"))
	t.Setenv("GIT_WORK_TREE", filepath.Join(t.TempDir(), "redirected-worktree"))
	t.Setenv("LC_ALL", "unsafe-locale")

	result, err := New(executable).runCommand(context.Background(), "/workspace", commandOptions{ReadOnly: true}, "status", "--porcelain=v1")
	if err != nil {
		t.Fatal(err)
	}
	output := string(result.Stdout)
	wantArguments := strings.Join([]string{
		"arg=--no-pager",
		"arg=--literal-pathspecs",
		"arg=-c",
		"arg=core.fsmonitor=false",
		"arg=-c",
		"arg=core.untrackedCache=false",
		"arg=-c",
		"arg=core.quotePath=true",
		"arg=-c",
		"arg=core.hooksPath=" + os.DevNull,
		"arg=-c",
		"arg=diff.compactionHeuristic=false",
		"arg=-c",
		"arg=diff.suppressBlankEmpty=false",
		"arg=-c",
		"arg=log.showSignature=false",
		"arg=-c",
		"arg=color.ui=false",
		"arg=-C",
		"arg=/workspace",
		"arg=status",
		"arg=--porcelain=v1",
	}, "\n")
	if !strings.Contains(output, wantArguments) {
		t.Fatalf("arguments = %q, want sequence %q", output, wantArguments)
	}
	for _, unsafe := range []string{"GIT_DIR=", "GIT_WORK_TREE="} {
		if strings.Contains(output, unsafe) {
			t.Fatalf("environment contains inherited %q: %q", unsafe, output)
		}
	}
	for _, safe := range []string{
		"LC_ALL=C",
		"LANG=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_EDITOR=:",
		"GIT_SEQUENCE_EDITOR=:",
		"GIT_OPTIONAL_LOCKS=0",
	} {
		if !strings.Contains(output, safe) {
			t.Fatalf("environment is missing %q: %q", safe, output)
		}
	}
}

func TestRunCommandWriteModeDoesNotDisableOptionalLocks(t *testing.T) {
	executable := writeExecutable(t, `#!/bin/sh
env
`)

	result, err := New(executable).runCommand(context.Background(), "/workspace", commandOptions{}, "init")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result.Stdout), "GIT_OPTIONAL_LOCKS=") {
		t.Fatalf("write environment unexpectedly disables optional locks: %q", result.Stdout)
	}
}

func TestRunCommandSupportsStdin(t *testing.T) {
	executable := writeExecutable(t, `#!/bin/sh
cat
`)
	input := []byte("100644 blob deadbeef\tpath.md\000")

	result, err := New(executable).runCommand(context.Background(), "/workspace", commandOptions{Stdin: bytes.NewReader(input)}, "update-index", "-z", "--index-info")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Stdout, input) {
		t.Fatalf("stdout = %q, want %q", result.Stdout, input)
	}
}

func TestRunCommandBoundsStdoutAndStderr(t *testing.T) {
	executable := writeExecutable(t, `#!/bin/sh
i=0
while [ "$i" -lt 128 ]; do
  printf x
  printf y >&2
  i=$((i + 1))
done
`)

	result, err := New(executable).runCommand(context.Background(), "/workspace", commandOptions{ReadOnly: true, MaxOutputBytes: 32}, "status")
	var commandErr *commandError
	if !errors.As(err, &commandErr) || !commandErr.OutputLimitExceeded() || !errors.Is(err, errCommandOutputLimit) {
		t.Fatalf("error = %#v", err)
	}
	if len(result.Stdout) > 32 || len(result.Stderr) > 32 {
		t.Fatalf("bounded output sizes = stdout %d, stderr %d", len(result.Stdout), len(result.Stderr))
	}
}

func TestRunCommandPreservesExitCodeAndBoundedDiagnostics(t *testing.T) {
	executable := writeExecutable(t, `#!/bin/sh
printf 'partial-output'
printf 'expected failure' >&2
exit 7
`)

	result, err := New(executable).runCommand(context.Background(), "/workspace", commandOptions{ReadOnly: true}, "status")
	var commandErr *commandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("error = %#v", err)
	}
	if result.ExitCode != 7 || commandErr.ExitCode() != 7 || !isExitCode(err, 7) {
		t.Fatalf("exit codes = result %d, error %d", result.ExitCode, commandErr.ExitCode())
	}
	if commandErr.Stdout() != "partial-output" || commandErr.Stderr() != "expected failure" {
		t.Fatalf("diagnostics = stdout %q, stderr %q", commandErr.Stdout(), commandErr.Stderr())
	}
}

func TestRunCommandClassifiesContextWithoutOperationName(t *testing.T) {
	executable := writeExecutable(t, `#!/bin/sh
exec sleep 10
`)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := New(executable).runCommand(ctx, "/workspace", commandOptions{ReadOnly: true}, "status")
	var commandErr *commandError
	if !errors.As(err, &commandErr) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(commandErr.ContextErr(), context.DeadlineExceeded) {
		t.Fatalf("error = %#v", err)
	}
	if commandErr.ExitCode() != -1 {
		t.Fatalf("exit code = %d, want -1", commandErr.ExitCode())
	}
	if commandContextCode("GIT_COMMIT_FAILED") != "GIT_COMMIT_TIMEOUT" || commandContextCode("OTHER") != "GIT_COMMAND_TIMEOUT" {
		t.Fatalf("context code mapping is not operation-safe")
	}
}

func TestRunCommandReportsUnavailableExecutable(t *testing.T) {
	_, err := New(filepath.Join(t.TempDir(), "missing-git")).runCommand(context.Background(), "/workspace", commandOptions{ReadOnly: true}, "status")
	var commandErr *commandError
	if !errors.As(err, &commandErr) || commandErr.ExitCode() != -1 {
		t.Fatalf("error = %#v", err)
	}
	if classified := classify("GIT_INSPECT_FAILED", err); !isFoundationError(classified, "GIT_COMMAND_UNAVAILABLE") {
		t.Fatalf("classified error = %#v", classified)
	}
}

func writeExecutable(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git-test-command")
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
