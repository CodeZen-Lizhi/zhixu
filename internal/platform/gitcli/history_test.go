package gitcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	documenthistory "github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const historyGitWorkspaceID foundation.ID = "40000000-0000-4000-8000-000000000001"

func TestHistoryClientReadsBoundedPathHistoryAndWorktree(t *testing.T) {
	repository := newHistoryGitRepository(t, "sha1")
	ctx := context.Background()

	state, err := repository.client.Inspect(ctx, historyGitWorkspaceID, repository.target)
	if err != nil {
		t.Fatal(err)
	}
	if state.Head != repository.head || state.Branch != "main" || state.ObjectFormat != "sha1" || state.Dirty || state.PathDirty {
		t.Fatalf("state=%+v", state)
	}
	page, err := repository.client.List(ctx, historyGitWorkspaceID, repository.target, 0, 1)
	if err != nil {
		t.Fatalf("List: %v: %v", err, errors.Unwrap(err))
	}
	if len(page.Commits) != 1 || page.Commits[0].ID != repository.currentCommit || !page.HasMore || page.State != state {
		t.Fatalf("page=%+v", page)
	}
	page, err = repository.client.List(ctx, historyGitWorkspaceID, repository.target, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Commits) != 1 || page.Commits[0].ID != repository.initialCommit || page.HasMore {
		t.Fatalf("second page=%+v", page)
	}
	diff, err := repository.client.Compare(ctx, historyGitWorkspaceID, repository.target, repository.initialCommit, repository.currentCommit)
	if err != nil {
		t.Fatal(err)
	}
	if diff.LeftContent != "# initial\n" || diff.RightContent != "# current\n" || diff.Patch == "" || diff.DiffHash != documenthistory.ComputeHash([]byte(diff.Patch)) {
		t.Fatalf("commit diff=%+v", diff)
	}

	if err := os.WriteFile(filepath.Join(repository.root, filepath.FromSlash(repository.target)), []byte("# worktree\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	state, err = repository.client.Inspect(ctx, historyGitWorkspaceID, repository.target)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Dirty || !state.PathDirty {
		t.Fatalf("dirty state=%+v", state)
	}
	diff, err = repository.client.Compare(ctx, historyGitWorkspaceID, repository.target, repository.currentCommit, documenthistory.WorktreeRef)
	if err != nil {
		t.Fatal(err)
	}
	if diff.LeftContent != "# current\n" || diff.RightContent != "# worktree\n" || diff.Head != repository.head || diff.Patch == "" || diff.DiffHash != documenthistory.ComputeHash([]byte(diff.Patch)) {
		t.Fatalf("diff=%+v", diff)
	}
}

func TestHistoryClientDistinguishesGlobalAndPathDirtyState(t *testing.T) {
	repository := newHistoryGitRepository(t, "sha1")
	if err := os.WriteFile(filepath.Join(repository.root, "untracked.txt"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := repository.client.Inspect(context.Background(), historyGitWorkspaceID, repository.target)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Dirty || state.PathDirty {
		t.Fatalf("state=%+v", state)
	}
}

func TestHistoryClientRejectsDetachedHeadAndCommitsOutsidePathHistory(t *testing.T) {
	repository := newHistoryGitRepository(t, "sha1")
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repository.root, filepath.FromSlash(repository.target)), []byte("# latest\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, repository.root, "add", "--", repository.target)
	runWritebackGit(t, repository.root, "commit", "-m", "latest target")
	repository.head = strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD"))
	_, err := repository.client.ReadBlob(ctx, historyGitWorkspaceID, repository.unrelatedCommit, repository.target)
	requireHistoryGitErrorCode(t, err, "DOCUMENT_HISTORY_NOT_FOUND", foundation.ErrorNotFound)

	runWritebackGit(t, repository.root, "switch", "--detach", repository.head)
	_, err = repository.client.Inspect(ctx, historyGitWorkspaceID, repository.target)
	requireHistoryGitErrorCode(t, err, "DOCUMENT_HISTORY_DETACHED_HEAD", foundation.ErrorVersionConflict)
}

func TestHistoryClientRejectsCommitOutsideCurrentBranchAncestry(t *testing.T) {
	repository := newHistoryGitRepository(t, "sha1")
	runWritebackGit(t, repository.root, "switch", "-c", "side", repository.initialCommit)
	if err := os.WriteFile(filepath.Join(repository.root, filepath.FromSlash(repository.target)), []byte("# side\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, repository.root, "add", "--", repository.target)
	runWritebackGit(t, repository.root, "commit", "-m", "side target")
	sideCommit := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD"))
	runWritebackGit(t, repository.root, "switch", "main")

	_, err := repository.client.ReadBlob(context.Background(), historyGitWorkspaceID, sideCommit, repository.target)
	requireHistoryGitErrorCode(t, err, "DOCUMENT_HISTORY_NOT_FOUND", foundation.ErrorNotFound)
}

func TestHistoryClientRejectsUnsafePathsBeforeRunningGitHistory(t *testing.T) {
	repository := newHistoryGitRepository(t, "sha1")
	for _, target := range []string{".", ".git/config.md", ".knowledge/a.md", "notes/a.markdown", "notes/../a.md", "notes\\a.md"} {
		t.Run(strings.ReplaceAll(target, "/", "_"), func(t *testing.T) {
			_, err := repository.client.Inspect(context.Background(), historyGitWorkspaceID, target)
			requireHistoryGitErrorCode(t, err, "DOCUMENT_HISTORY_INVALID", foundation.ErrorInvalidInput)
		})
	}
}

func TestHistoryClientRejectsGraftsFiltersAttributesAndHiddenIndexFlags(t *testing.T) {
	t.Run("grafts", func(t *testing.T) {
		repository := newHistoryGitRepository(t, "sha1")
		grafts := filepath.Join(repository.root, ".git", "info", "grafts")
		if err := os.WriteFile(grafts, []byte(repository.head+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := repository.client.Inspect(context.Background(), historyGitWorkspaceID, repository.target)
		requireHistoryGitErrorCode(t, err, "DOCUMENT_HISTORY_GIT_UNSAFE", foundation.ErrorPermissionDenied)
	})

	t.Run("filter never executes", func(t *testing.T) {
		repository := newHistoryGitRepository(t, "sha1")
		marker := filepath.Join(repository.root, "filter-ran")
		filter := writeExecutable(t, fmt.Sprintf("#!/bin/sh\nprintf ran > %q\ncat\n", marker))
		if err := os.WriteFile(filepath.Join(repository.root, ".git", "info", "attributes"), []byte("*.md filter=evil\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runWritebackGit(t, repository.root, "config", "filter.evil.clean", filter)
		runWritebackGit(t, repository.root, "config", "filter.evil.smudge", filter)
		runWritebackGit(t, repository.root, "config", "filter.evil.required", "true")
		_, err := repository.client.Inspect(context.Background(), historyGitWorkspaceID, repository.target)
		requireHistoryGitErrorCode(t, err, "DOCUMENT_HISTORY_GIT_UNSAFE", foundation.ErrorPermissionDenied)
		if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("content filter ran or marker cannot be inspected: %v", statErr)
		}
	})

	t.Run("transforming attribute", func(t *testing.T) {
		repository := newHistoryGitRepository(t, "sha1")
		if err := os.WriteFile(filepath.Join(repository.root, ".git", "info", "attributes"), []byte("*.md text eol=crlf\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := repository.client.Inspect(context.Background(), historyGitWorkspaceID, repository.target)
		requireHistoryGitErrorCode(t, err, "DOCUMENT_HISTORY_GIT_UNSAFE", foundation.ErrorPermissionDenied)
	})

	t.Run("hidden index flag", func(t *testing.T) {
		repository := newHistoryGitRepository(t, "sha1")
		runWritebackGit(t, repository.root, "update-index", "--assume-unchanged", "--", repository.target)
		_, err := repository.client.Inspect(context.Background(), historyGitWorkspaceID, repository.target)
		requireHistoryGitErrorCode(t, err, "DOCUMENT_HISTORY_GIT_UNSAFE", foundation.ErrorPermissionDenied)
	})
}

func TestHistoryClientRejectsSymlinkWorktreeTarget(t *testing.T) {
	repository := newHistoryGitRepository(t, "sha1")
	target := filepath.Join(repository.root, filepath.FromSlash(repository.target))
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	_, err := repository.client.Compare(context.Background(), historyGitWorkspaceID, repository.target, repository.currentCommit, documenthistory.WorktreeRef)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorPermissionDenied {
		t.Fatalf("error=%v", err)
	}
}

func TestHistoryClientRejectsWorkspaceRootSwitchDuringRead(t *testing.T) {
	first := newHistoryGitRepository(t, "sha1")
	second := newHistoryGitRepository(t, "sha1")
	repository := &switchingHistoryWorkspaceRepository{roots: []string{first.root, second.root}}
	client, err := NewHistoryClient(New(""), repository)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.List(context.Background(), historyGitWorkspaceID, first.target, 0, 10)
	requireHistoryGitErrorCode(t, err, "DOCUMENT_HISTORY_CURSOR_STALE", foundation.ErrorVersionConflict)
}

func TestHistoryClientSupportsRepositoryObjectFormats(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			repository := newHistoryGitRepository(t, format)
			state, err := repository.client.Inspect(context.Background(), historyGitWorkspaceID, repository.target)
			if err != nil {
				t.Fatal(err)
			}
			wantLength := 40
			if format == "sha256" {
				wantLength = 64
			}
			if state.ObjectFormat != format || len(state.Head) != wantLength {
				t.Fatalf("state=%+v", state)
			}
			blob, err := repository.client.ReadBlob(context.Background(), historyGitWorkspaceID, repository.currentCommit, repository.target)
			if err != nil || string(blob.Content) != "# current\n" {
				t.Fatalf("blob=(%+v,%v)", blob, err)
			}
		})
	}
}

func TestHistoryClientRejectsOversizedBlobAndDiff(t *testing.T) {
	repository := newHistoryGitRepository(t, "sha1")
	large := []byte(strings.Repeat("before-0123456789\n", (documenthistory.MaxDiffBytes/18)+65536))
	if len(large) > documenthistory.MaxBlobBytes {
		t.Fatalf("diff fixture unexpectedly exceeds blob limit: %d", len(large))
	}
	path := filepath.Join(repository.root, filepath.FromSlash(repository.target))
	if err := os.WriteFile(path, large, 0o640); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, repository.root, "add", "--", repository.target)
	runWritebackGit(t, repository.root, "commit", "-m", "large before")
	left := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD"))
	large = []byte(strings.Repeat("after--0123456789\n", len(large)/19+1))
	if err := os.WriteFile(path, large, 0o640); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, repository.root, "add", "--", repository.target)
	runWritebackGit(t, repository.root, "commit", "-m", "large after")
	right := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD"))
	_, err := repository.client.Compare(context.Background(), historyGitWorkspaceID, repository.target, left, right)
	requireHistoryGitErrorCode(t, err, "DOCUMENT_HISTORY_OUTPUT_TOO_LARGE", foundation.ErrorPermissionDenied)

	oversized := []byte(strings.Repeat("x", documenthistory.MaxBlobBytes+1))
	if err := os.WriteFile(path, oversized, 0o640); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, repository.root, "add", "--", repository.target)
	runWritebackGit(t, repository.root, "commit", "-m", "oversized blob")
	commit := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD"))
	_, err = repository.client.ReadBlob(context.Background(), historyGitWorkspaceID, commit, repository.target)
	requireHistoryGitErrorCode(t, err, "DOCUMENT_HISTORY_OUTPUT_TOO_LARGE", foundation.ErrorPermissionDenied)
}

type historyGitRepository struct {
	root            string
	target          string
	initialCommit   string
	currentCommit   string
	unrelatedCommit string
	head            string
	client          *HistoryClient
}

func newHistoryGitRepository(t *testing.T, objectFormat string) historyGitRepository {
	t.Helper()
	root := t.TempDir()
	args := []string{"init", "--initial-branch=main", "--object-format=" + objectFormat}
	command := exec.Command("git", append([]string{"--no-pager", "-c", "core.hooksPath=" + os.DevNull, "-C", root}, args...)...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		if objectFormat == "sha256" {
			t.Skipf("git does not support sha256 repositories: %v: %s", err, output)
		}
		t.Fatalf("git init: %v: %s", err, output)
	}
	runWritebackGit(t, root, "config", "user.name", "Zhixu History Test")
	runWritebackGit(t, root, "config", "user.email", "history@example.invalid")
	target := "notes/target.md"
	path := filepath.Join(root, filepath.FromSlash(target))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# initial\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, root, "add", "--", target)
	runWritebackGit(t, root, "commit", "-m", "initial target")
	initial := strings.TrimSpace(runWritebackGit(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(path, []byte("# current\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, root, "add", "--", target)
	runWritebackGit(t, root, "commit", "-m", "update target")
	current := strings.TrimSpace(runWritebackGit(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("repository\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, root, "add", "--", "README.md")
	runWritebackGit(t, root, "commit", "-m", "unrelated change")
	unrelated := strings.TrimSpace(runWritebackGit(t, root, "rev-parse", "HEAD"))
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace := historyWorkspace(canonical)
	client, err := NewHistoryClient(New(""), &staticHistoryWorkspaceRepository{workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	return historyGitRepository{root: canonical, target: target, initialCommit: initial, currentCommit: current, unrelatedCommit: unrelated, head: unrelated, client: client}
}

type staticHistoryWorkspaceRepository struct{ workspace workspacedomain.Workspace }

func (repository *staticHistoryWorkspaceRepository) GetWorkspaceByID(_ context.Context, id foundation.ID) (workspacedomain.Workspace, error) {
	if id != historyGitWorkspaceID {
		return workspacedomain.Workspace{}, errors.New("workspace not found")
	}
	return repository.workspace, nil
}

type switchingHistoryWorkspaceRepository struct {
	roots []string
	calls int
}

func (repository *switchingHistoryWorkspaceRepository) GetWorkspaceByID(_ context.Context, id foundation.ID) (workspacedomain.Workspace, error) {
	if id != historyGitWorkspaceID || len(repository.roots) == 0 {
		return workspacedomain.Workspace{}, errors.New("workspace not found")
	}
	index := repository.calls
	if index >= len(repository.roots) {
		index = len(repository.roots) - 1
	}
	repository.calls++
	return historyWorkspace(repository.roots[index]), nil
}

func historyWorkspace(root string) workspacedomain.Workspace {
	return workspacedomain.Workspace{
		ID: historyGitWorkspaceID, RootPath: root,
		Status: workspacedomain.WorkspaceStatusActive, Availability: workspacedomain.WorkspaceAvailabilityAvailable,
	}
}

func requireHistoryGitErrorCode(t *testing.T, err error, code string, kind foundation.ErrorKind) {
	t.Helper()
	var classified *foundation.Error
	if err == nil || !errors.As(err, &classified) || classified.Code != code || classified.Kind != kind {
		t.Fatalf("error=%v, want code=%s kind=%s", err, code, kind)
	}
}
