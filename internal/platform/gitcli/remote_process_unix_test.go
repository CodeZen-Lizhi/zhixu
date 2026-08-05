//go:build unix

package gitcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestRemoteClientCancellationKillsProcessGroupAndReleasesConfigLock(t *testing.T) {
	repository := newRemoteTestRepository(t)
	stateDirectory := t.TempDir()
	executable := writeExecutable(t, `#!/bin/sh
case "$*" in
*fetch*)
  (trap '' HUP INT TERM; sleep 300) &
  printf '%s\n' "$!" > "$ZHIXU_TEST_REMOTE_CHILD_PID"
  : > "$ZHIXU_TEST_REMOTE_READY"
  wait
  ;;
*) exec git "$@" ;;
esac
`)
	t.Setenv("ZHIXU_TEST_REMOTE_CHILD_PID", filepath.Join(stateDirectory, "child.pid"))
	t.Setenv("ZHIXU_TEST_REMOTE_READY", filepath.Join(stateDirectory, "ready"))
	client, err := newRemoteClientForTests(New(executable), remoteWorkspaceRepository{workspace: workspacedomain.Workspace{
		ID: remoteTestWorkspaceID, RootPath: repository.root,
	}})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- client.Fetch(ctx, repository.access) }()
	waitForRemoteProcessFile(t, filepath.Join(stateDirectory, "ready"))
	pidBytes, err := os.ReadFile(filepath.Join(stateDirectory, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid child pid %q: %v", pidBytes, err)
	}

	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Fetch() error=%v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Fetch() did not return after cancellation")
	}
	waitForRemoteProcessExit(t, pid)
	if _, err := os.Stat(filepath.Join(repository.root, ".git", "config.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git config lock remains after cancellation: %v", err)
	}
}

func waitForRemoteProcessFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func waitForRemoteProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Git credential child process %d remains after cancellation", pid)
}
