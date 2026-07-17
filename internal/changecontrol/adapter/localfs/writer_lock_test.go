package localfs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	writerLockTestWorkspaceID foundation.ID = "11111111-1111-4111-8111-111111111111"
	writerLockHelperEnv                     = "ZHIXU_WRITER_LOCK_HELPER"
	writerLockHelperRootEnv                 = "ZHIXU_WRITER_LOCK_ROOT"
	writerLockHelperTargetEnv               = "ZHIXU_WRITER_LOCK_TARGET"
)

func TestWriterRejectsInvalidTargetPaths(t *testing.T) {
	root := newWriterLockWorkspace(t)
	writer := newWriterLockWriter(t, root)
	for _, target := range []string{
		"/note.md",
		"../note.md",
		"docs/../../note.md",
		"docs\\note.md",
		"note.txt",
		"note.md\x00ignored",
	} {
		t.Run(fmt.Sprintf("%q", target), func(t *testing.T) {
			lock, err := writer.AcquireTarget(context.Background(), writerLockTestWorkspaceID, target)
			if lock != nil {
				_ = lock.Close()
				t.Fatalf("AcquireTarget(%q) returned a lock", target)
			}
			requireWriterLockError(t, err, foundation.ErrorInvalidInput, "WRITEBACK_TARGET_INVALID")
		})
	}
}

func TestWriterRejectsSymlinksAndSpecialTargets(t *testing.T) {
	t.Run("parent symlink", func(t *testing.T) {
		root := newWriterLockWorkspace(t)
		writeWriterLockTarget(t, root, "real/note.md")
		if err := os.Symlink("real", filepath.Join(root, "linked")); err != nil {
			t.Fatal(err)
		}
		assertWriterLockRejected(t, root, "linked/note.md", foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_PARENT_UNSAFE")
	})

	t.Run("target symlink", func(t *testing.T) {
		root := newWriterLockWorkspace(t)
		writeWriterLockTarget(t, root, "real.md")
		if err := os.Symlink("real.md", filepath.Join(root, "alias.md")); err != nil {
			t.Fatal(err)
		}
		assertWriterLockRejected(t, root, "alias.md", foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_UNSAFE")
	})

	t.Run("directory", func(t *testing.T) {
		root := newWriterLockWorkspace(t)
		if err := os.Mkdir(filepath.Join(root, "directory.md"), 0o700); err != nil {
			t.Fatal(err)
		}
		assertWriterLockRejected(t, root, "directory.md", foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_UNSAFE")
	})

	t.Run("fifo", func(t *testing.T) {
		root := newWriterLockWorkspace(t)
		fifoPath := filepath.Join(root, "fifo.md")
		if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
			t.Skipf("FIFO is unavailable on this platform/filesystem: %v", err)
		}
		assertWriterLockRejected(t, root, "fifo.md", foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_UNSAFE")
	})

	t.Run("unix socket", func(t *testing.T) {
		root := newWriterLockWorkspace(t)
		listener, err := net.Listen("unix", filepath.Join(root, "socket.md"))
		if err != nil {
			t.Skipf("Unix sockets are unavailable on this platform/filesystem: %v", err)
		}
		defer listener.Close()
		assertWriterLockRejected(t, root, "socket.md", foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_UNSAFE")
	})

	t.Run("oversized target", func(t *testing.T) {
		root := newWriterLockWorkspace(t)
		writeWriterLockTarget(t, root, "large.md")
		if err := os.Truncate(filepath.Join(root, "large.md"), int64(domain.MaxWritebackContentBytes)+1); err != nil {
			t.Fatal(err)
		}
		assertWriterLockRejected(t, root, "large.md", foundation.ErrorInvalidInput, "WRITEBACK_TARGET_TOO_LARGE")
	})
}

func TestWriterAllowsDifferentTargetsWhileOneIsLocked(t *testing.T) {
	root := newWriterLockWorkspace(t)
	writeWriterLockTarget(t, root, "first.md")
	writeWriterLockTarget(t, root, "second.md")
	writer := newWriterLockWriter(t, root)

	first, err := writer.AcquireTarget(context.Background(), writerLockTestWorkspaceID, "first.md")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	second, err := writer.AcquireTarget(ctx, writerLockTestWorkspaceID, "second.md")
	if err != nil {
		t.Fatalf("different target was blocked: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterCloseAllowsReacquireAndPreservesLockFile(t *testing.T) {
	root := newWriterLockWorkspace(t)
	writeWriterLockTarget(t, root, "note.md")
	writer := newWriterLockWriter(t, root)

	first, err := writer.AcquireTarget(context.Background(), writerLockTestWorkspaceID, "note.md")
	if err != nil {
		t.Fatal(err)
	}
	lockPaths := writerLockFiles(t, root)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second Close() failed: %v", err)
	}
	assertWriterLockFiles(t, lockPaths)

	second, err := writer.AcquireTarget(context.Background(), writerLockTestWorkspaceID, "note.md")
	if err != nil {
		t.Fatalf("AcquireTarget() after Close() failed: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if got := writerLockFiles(t, root); !equalStrings(got, lockPaths) {
		t.Fatalf("lock files changed across reacquire: got %q want %q", got, lockPaths)
	}
	assertWriterLockFiles(t, lockPaths)
}

func TestWriterTargetLockTimesOutAcrossProcessesAndProcessExitReleasesIt(t *testing.T) {
	root := newWriterLockWorkspace(t)
	writeWriterLockTarget(t, root, "note.md")
	helper := startWriterLockHelper(t, root, "note.md")
	writer := newWriterLockWriter(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	lock, err := writer.AcquireTarget(ctx, writerLockTestWorkspaceID, "note.md")
	cancel()
	if lock != nil {
		_ = lock.Close()
		t.Fatal("same target lock was acquired while helper process held it")
	}
	requireWriterLockError(t, err, foundation.ErrorRetryableFailure, "WRITEBACK_TARGET_LOCK_TIMEOUT")
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, domain.ErrTargetLockUnavailable) {
		t.Fatalf("timeout causes were not preserved: %v", err)
	}

	helper.exitWithoutClose(t)
	reacquireCtx, reacquireCancel := context.WithTimeout(context.Background(), time.Second)
	defer reacquireCancel()
	reacquired, err := writer.AcquireTarget(reacquireCtx, writerLockTestWorkspaceID, "note.md")
	if err != nil {
		t.Fatalf("kernel did not release flock after helper exit: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterTargetLockHonorsCancellationWhileWaiting(t *testing.T) {
	root := newWriterLockWorkspace(t)
	writeWriterLockTarget(t, root, "note.md")
	startWriterLockHelper(t, root, "note.md")
	writer := newWriterLockWriter(t, root)

	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(30*time.Millisecond, cancel)
	lock, err := writer.AcquireTarget(ctx, writerLockTestWorkspaceID, "note.md")
	timer.Stop()
	cancel()
	if lock != nil {
		_ = lock.Close()
		t.Fatal("same target lock was acquired while helper process held it")
	}
	requireWriterLockError(t, err, foundation.ErrorNonRetryableFailure, "WRITEBACK_TARGET_LOCK_CANCELLED")
	if !errors.Is(err, context.Canceled) || !errors.Is(err, domain.ErrTargetLockUnavailable) {
		t.Fatalf("cancellation causes were not preserved: %v", err)
	}
}

func TestWriterTargetLockUsesFileIdentityAcrossHardLinks(t *testing.T) {
	root := newWriterLockWorkspace(t)
	writeWriterLockTarget(t, root, "note.md")
	if err := os.Link(filepath.Join(root, "note.md"), filepath.Join(root, "alias.md")); err != nil {
		t.Skipf("hard links are unavailable on this filesystem: %v", err)
	}
	startWriterLockHelper(t, root, "note.md")
	writer := newWriterLockWriter(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	lock, err := writer.AcquireTarget(ctx, writerLockTestWorkspaceID, "alias.md")
	if lock != nil {
		_ = lock.Close()
		t.Fatal("hard-link alias acquired a distinct lock for the same inode")
	}
	requireWriterLockError(t, err, foundation.ErrorRetryableFailure, "WRITEBACK_TARGET_LOCK_TIMEOUT")
}

func TestWriterPathLockRemainsHeldAcrossAtomicRename(t *testing.T) {
	root := newWriterLockWorkspace(t)
	targetPath := "note.md"
	base := []byte("# target\n")
	result := []byte("# updated\n")
	writeWriterLockTarget(t, root, targetPath)
	writer := newWriterLockWriter(t, root)

	first, err := writer.AcquireTarget(context.Background(), writerLockTestWorkspaceID, targetPath)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := first.Prepare(context.Background(), prepareCommand(targetPath, base, result))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.CommitCAS(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	second, err := writer.AcquireTarget(ctx, writerLockTestWorkspaceID, targetPath)
	cancel()
	if second != nil {
		_ = second.Close()
		t.Fatal("same path acquired a new inode lock while the pre-rename writer was active")
	}
	requireWriterLockError(t, err, foundation.ErrorRetryableFailure, "WRITEBACK_TARGET_LOCK_TIMEOUT")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reacquired, err := writer.AcquireTarget(context.Background(), writerLockTestWorkspaceID, targetPath)
	if err != nil {
		t.Fatalf("same path was not released after Close: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterLockHelperProcess(t *testing.T) {
	if os.Getenv(writerLockHelperEnv) != "1" {
		return
	}
	root := os.Getenv(writerLockHelperRootEnv)
	target := os.Getenv(writerLockHelperTargetEnv)
	writer, err := newWriterWithOperations(
		writerLockWorkspaceRepository{root: root},
		writerLockContentValidator{},
		writerOperations{lockPollInterval: time.Millisecond},
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	lock, err := writer.AcquireTarget(context.Background(), writerLockTestWorkspaceID, target)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
		os.Exit(2)
	}
	var command [1]byte
	if _, err := io.ReadFull(os.Stdin, command[:]); err != nil {
		os.Exit(2)
	}
	if command[0] == 'c' {
		if err := lock.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	// Intentionally skip Close: kernel process teardown must release flock.
	os.Exit(0)
}

type writerLockWorkspaceRepository struct{ root string }

func (r writerLockWorkspaceRepository) GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error) {
	return workspacedomain.Workspace{ID: writerLockTestWorkspaceID, RootPath: r.root}, nil
}

type writerLockContentValidator struct{}

func (writerLockContentValidator) Validate(context.Context, []byte) error { return nil }

type writerLockHelper struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stderr  *bytes.Buffer
	exited  bool
}

func startWriterLockHelper(t *testing.T, root, target string) *writerLockHelper {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestWriterLockHelperProcess$")
	command.Env = append(os.Environ(),
		writerLockHelperEnv+"=1",
		writerLockHelperRootEnv+"="+root,
		writerLockHelperTargetEnv+"="+target,
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	helper := &writerLockHelper{command: command, stdin: stdin, stderr: stderr}
	t.Cleanup(func() {
		if helper.exited {
			return
		}
		_, _ = helper.stdin.Write([]byte{'x'})
		_ = helper.stdin.Close()
		_ = helper.command.Wait()
		helper.exited = true
	})

	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- scanner.Text()
			return
		}
		ready <- ""
	}()
	select {
	case line := <-ready:
		if line != "locked" {
			_ = command.Process.Kill()
			_ = command.Wait()
			helper.exited = true
			t.Fatalf("lock helper did not become ready (line=%q, stderr=%q)", line, stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		helper.exited = true
		t.Fatalf("timed out waiting for lock helper (stderr=%q)", stderr.String())
	}
	return helper
}

func (h *writerLockHelper) exitWithoutClose(t *testing.T) {
	t.Helper()
	if h.exited {
		return
	}
	if _, err := h.stdin.Write([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	if err := h.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.command.Wait(); err != nil {
		t.Fatalf("lock helper exit failed: %v (stderr=%q)", err, h.stderr.String())
	}
	h.exited = true
}

func newWriterLockWriter(t *testing.T, root string) *Writer {
	t.Helper()
	writer, err := newWriterWithOperations(
		writerLockWorkspaceRepository{root: root},
		writerLockContentValidator{},
		writerOperations{lockPollInterval: time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	return writer
}

func newWriterLockWorkspace(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "zhixu-writer-lock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.MkdirAll(filepath.Join(root, ".git", "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(writebackExcludePattern+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, writebackLockDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeWriterLockTarget(t *testing.T, root, relative string) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte("# target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertWriterLockRejected(t *testing.T, root, target string, kind foundation.ErrorKind, code string) {
	t.Helper()
	lock, err := newWriterLockWriter(t, root).AcquireTarget(context.Background(), writerLockTestWorkspaceID, target)
	if lock != nil {
		_ = lock.Close()
		t.Fatalf("AcquireTarget(%q) returned a lock", target)
	}
	requireWriterLockError(t, err, kind, code)
}

func requireWriterLockError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error = %#v, want kind=%q code=%q", err, kind, code)
	}
}

func writerLockFiles(t *testing.T, root string) []string {
	t.Helper()
	directory := filepath.Join(root, filepath.FromSlash(writebackLockDirectory))
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("lock file count = %d, want 2", len(entries))
	}
	return []string{filepath.Join(directory, entries[0].Name()), filepath.Join(directory, entries[1].Name())}
}

func assertWriterLockFiles(t *testing.T, lockPaths []string) {
	t.Helper()
	for _, lockPath := range lockPaths {
		info, err := os.Lstat(lockPath)
		if err != nil {
			t.Fatalf("persistent lock file is missing: %v", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("lock file mode = %v, want regular 0600", info.Mode())
		}
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
