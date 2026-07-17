package gitcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	changecontrol "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestNewWritebackClientRequiresWorkspaceRepository(t *testing.T) {
	client, err := NewWritebackClient(New(""), nil)
	if client != nil {
		t.Fatalf("client = %#v", client)
	}
	requireGitErrorCode(t, err, "GIT_WORKSPACE_REPOSITORY_UNAVAILABLE", foundation.ErrorDependencyUnavailable)
}

func TestWritebackClientRejectsWorkspaceRepositoryBindingMismatch(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	client, err := NewWritebackClient(New(""), mismatchedWorkspaceRepository{workspace: workspacedomain.Workspace{ID: "different", RootPath: repository.root}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Inspect(context.Background(), writebackTestWorkspaceID, repository.head)
	requireGitErrorCode(t, err, "GIT_WORKSPACE_BINDING_INVALID", foundation.ErrorConsistencyViolation)
}

func TestWritebackInspectReadsCleanAttachedBaseline(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	snapshot, err := repository.client.Inspect(context.Background(), writebackTestWorkspaceID, repository.head)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkspaceID != writebackTestWorkspaceID || snapshot.Branch != "main" || snapshot.Head != repository.head || snapshot.ObjectFormat != changecontrol.GitObjectFormatSHA1 || !snapshot.Clean {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestWritebackCaptureApprovalSnapshotReadsCurrentStrictBaseline(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	snapshot, err := repository.client.CaptureApprovalSnapshot(context.Background(), writebackTestWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkspaceID != writebackTestWorkspaceID || snapshot.Branch != "main" || snapshot.Head != repository.head || snapshot.ObjectFormat != changecontrol.GitObjectFormatSHA1 || !snapshot.Clean {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestWritebackCaptureApprovalSnapshotRejectsUnsafeCurrentRepository(t *testing.T) {
	t.Run("root mismatch", func(t *testing.T) {
		repository := newWritebackTestRepository(t, "", "")
		nested := filepath.Join(repository.root, "nested")
		if err := os.Mkdir(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		client, err := NewWritebackClient(New(""), writebackWorkspaceRepository{workspace: workspacedomain.Workspace{ID: writebackTestWorkspaceID, RootPath: nested}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.CaptureApprovalSnapshot(context.Background(), writebackTestWorkspaceID)
		requireGitErrorCode(t, err, "GIT_REPOSITORY_ROOT_MISMATCH", foundation.ErrorPermissionDenied)
	})
	t.Run("dirty", func(t *testing.T) {
		repository := newWritebackTestRepository(t, "", "")
		repository.writeTarget(t, []byte("changed\n"))
		_, err := repository.client.CaptureApprovalSnapshot(context.Background(), writebackTestWorkspaceID)
		requireGitErrorCode(t, err, "GIT_REPOSITORY_DIRTY", foundation.ErrorVersionConflict)
	})
	t.Run("detached", func(t *testing.T) {
		repository := newWritebackTestRepository(t, "", "")
		runWritebackGit(t, repository.root, "checkout", "--detach", repository.head)
		_, err := repository.client.CaptureApprovalSnapshot(context.Background(), writebackTestWorkspaceID)
		requireGitErrorCode(t, err, "GIT_REPOSITORY_DETACHED", foundation.ErrorVersionConflict)
	})
	t.Run("identity missing", func(t *testing.T) {
		repository := newWritebackTestRepository(t, "", "")
		runWritebackGit(t, repository.root, "config", "--local", "--unset-all", "user.name")
		runWritebackGit(t, repository.root, "config", "--local", "--unset-all", "user.email")
		t.Setenv("HOME", t.TempDir())
		_, err := repository.client.CaptureApprovalSnapshot(context.Background(), writebackTestWorkspaceID)
		requireGitErrorCode(t, err, "GIT_AUTHOR_IDENTITY_MISSING", foundation.ErrorPermissionDenied)
	})
}

func TestWritebackInspectRejectsRepositoryAndHeadPreconditions(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	tests := []struct {
		name     string
		mutate   func(*testing.T) *WritebackClient
		approved string
		code     string
		kind     foundation.ErrorKind
	}{
		{
			name: "missing repository",
			mutate: func(t *testing.T) *WritebackClient {
				client, err := NewWritebackClient(New(""), writebackWorkspaceRepository{workspace: workspacedomain.Workspace{ID: writebackTestWorkspaceID, RootPath: t.TempDir()}})
				if err != nil {
					t.Fatal(err)
				}
				return client
			},
			approved: repository.head, code: "GIT_REPOSITORY_NOT_FOUND", kind: foundation.ErrorNotFound,
		},
		{
			name: "root mismatch",
			mutate: func(t *testing.T) *WritebackClient {
				nested := filepath.Join(repository.root, "nested")
				if err := os.Mkdir(nested, 0o700); err != nil {
					t.Fatal(err)
				}
				client, err := NewWritebackClient(New(""), writebackWorkspaceRepository{workspace: workspacedomain.Workspace{ID: writebackTestWorkspaceID, RootPath: nested}})
				if err != nil {
					t.Fatal(err)
				}
				return client
			},
			approved: repository.head, code: "GIT_REPOSITORY_ROOT_MISMATCH", kind: foundation.ErrorPermissionDenied,
		},
		{
			name: "head drift", mutate: func(*testing.T) *WritebackClient { return repository.client },
			approved: strings.Repeat("a", len(repository.head)), code: "GIT_HEAD_CONFLICT", kind: foundation.ErrorVersionConflict,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.mutate(t).Inspect(context.Background(), writebackTestWorkspaceID, test.approved)
			requireGitErrorCode(t, err, test.code, test.kind)
		})
	}
}

func TestWritebackInspectRejectsUnbornDetachedAndOperationInProgress(t *testing.T) {
	t.Run("unborn", func(t *testing.T) {
		root := t.TempDir()
		runWritebackGit(t, root, "init", "--initial-branch=main")
		client, err := NewWritebackClient(New(""), writebackWorkspaceRepository{workspace: workspacedomain.Workspace{ID: writebackTestWorkspaceID, RootPath: root}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Inspect(context.Background(), writebackTestWorkspaceID, strings.Repeat("a", 40))
		requireGitErrorCode(t, err, "GIT_REPOSITORY_UNBORN", foundation.ErrorVersionConflict)
	})
	t.Run("detached", func(t *testing.T) {
		repository := newWritebackTestRepository(t, "", "")
		runWritebackGit(t, repository.root, "checkout", "--detach", repository.head)
		_, err := repository.client.Inspect(context.Background(), writebackTestWorkspaceID, repository.head)
		requireGitErrorCode(t, err, "GIT_REPOSITORY_DETACHED", foundation.ErrorVersionConflict)
	})
	t.Run("operation", func(t *testing.T) {
		repository := newWritebackTestRepository(t, "", "")
		if err := os.WriteFile(filepath.Join(repository.root, ".git", "MERGE_HEAD"), []byte(repository.head), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := repository.client.Inspect(context.Background(), writebackTestWorkspaceID, repository.head)
		requireGitErrorCode(t, err, "GIT_OPERATION_IN_PROGRESS", foundation.ErrorVersionConflict)
	})
}

func TestWritebackInspectRequiresAuthorAndCommitterIdentity(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	runWritebackGit(t, repository.root, "config", "--local", "--unset-all", "user.name")
	runWritebackGit(t, repository.root, "config", "--local", "--unset-all", "user.email")
	t.Setenv("HOME", t.TempDir())
	_, err := repository.client.Inspect(context.Background(), writebackTestWorkspaceID, repository.head)
	requireGitErrorCode(t, err, "GIT_AUTHOR_IDENTITY_MISSING", foundation.ErrorPermissionDenied)
}

func TestWritebackInspectDistinguishesDirtyStates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, writebackTestRepository)
		code   string
	}{
		{"unstaged", func(t *testing.T, r writebackTestRepository) { r.writeTarget(t, []byte("changed\n")) }, "GIT_REPOSITORY_DIRTY"},
		{"staged", func(t *testing.T, r writebackTestRepository) {
			r.writeTarget(t, []byte("changed\n"))
			runWritebackGit(t, r.root, "add", "--", r.target)
		}, "GIT_REPOSITORY_STAGED"},
		{"untracked", func(t *testing.T, r writebackTestRepository) {
			if err := os.WriteFile(filepath.Join(r.root, "untracked.md"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "GIT_REPOSITORY_UNTRACKED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newWritebackTestRepository(t, "", "")
			test.mutate(t, repository)
			_, err := repository.client.Inspect(context.Background(), writebackTestWorkspaceID, repository.head)
			requireGitErrorCode(t, err, test.code, foundation.ErrorVersionConflict)
		})
	}
}

func TestWritebackInspectDistinguishesConflictState(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	runWritebackGit(t, repository.root, "checkout", "-b", "other")
	repository.writeTarget(t, []byte("other\n"))
	runWritebackGit(t, repository.root, "add", "--", repository.target)
	runWritebackGit(t, repository.root, "commit", "-m", "other")
	runWritebackGit(t, repository.root, "checkout", "main")
	repository.writeTarget(t, []byte("main\n"))
	runWritebackGit(t, repository.root, "add", "--", repository.target)
	runWritebackGit(t, repository.root, "commit", "-m", "main")
	repository.head = strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD"))
	command := exec.Command("git", "-C", repository.root, "merge", "other")
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", output)
	}
	_, err := repository.client.Inspect(context.Background(), writebackTestWorkspaceID, repository.head)
	requireGitErrorCode(t, err, "GIT_REPOSITORY_CONFLICTED", foundation.ErrorVersionConflict)
}

func TestWritebackInspectRejectsHiddenIndexFlags(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			repository := newWritebackTestRepository(t, "", "")
			runWritebackGit(t, repository.root, "update-index", flag, "--", repository.target)
			_, err := repository.client.Inspect(context.Background(), writebackTestWorkspaceID, repository.head)
			requireGitErrorCode(t, err, "GIT_INDEX_FLAGS_UNSAFE", foundation.ErrorPermissionDenied)
		})
	}
}

func TestWritebackInspectRejectsLegacyGrafts(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	grafts := filepath.Join(repository.root, ".git", "info", "grafts")
	if err := os.WriteFile(grafts, []byte(repository.head+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := repository.client.Inspect(context.Background(), writebackTestWorkspaceID, repository.head)
	requireGitErrorCode(t, err, "GIT_OBJECT_OVERRIDES_UNSAFE", foundation.ErrorPermissionDenied)
}

func TestWritebackInspectRejectsTrackedContentFilterWithoutExecutingIt(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	marker := filepath.Join(repository.root, "filter-ran")
	filter := writeExecutable(t, "#!/bin/sh\nprintf ran > "+marker+"\ncat\n")
	if err := os.WriteFile(filepath.Join(repository.root, ".git", "info", "attributes"), []byte("*.md filter=evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, repository.root, "config", "filter.evil.clean", filter)
	runWritebackGit(t, repository.root, "config", "filter.evil.smudge", filter)
	runWritebackGit(t, repository.root, "config", "filter.evil.required", "true")

	_, err := repository.client.Inspect(context.Background(), writebackTestWorkspaceID, repository.head)
	requireGitErrorCode(t, err, "GIT_REPOSITORY_FILTER_UNSAFE", foundation.ErrorPermissionDenied)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("filter ran or cannot be inspected: %v", err)
	}
}

func TestWritebackDiffApprovedBindsSpecialPathContentAndStableDiff(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "notes/- 安全 写回.md")
	resultHash := repository.writeTarget(t, []byte("# approved\n\n内容\n"))
	diff, err := repository.client.DiffApproved(context.Background(), repository.diffRequest(resultHash))
	if err != nil {
		t.Fatal(err)
	}
	if diff.TargetPath != repository.target || diff.ResultHash != resultHash || diff.DiffHash == "" || diff.BaseBlobID == diff.ResultBlobID || diff.BaseMode != changecontrol.GitFileModeRegular {
		t.Fatalf("diff = %#v", diff)
	}
	stable, err := repository.client.readStableDiff(context.Background(), repository.root, repository.head, "", repository.target)
	if err != nil {
		t.Fatal(err)
	}
	if sha256Hex(stable) != diff.DiffHash {
		t.Fatalf("stable diff hash = %s, want %s", sha256Hex(stable), diff.DiffHash)
	}
	runWritebackGit(t, repository.root, "config", "core.quotePath", "false")
	unquotedConfigDiff, err := repository.client.readStableDiff(context.Background(), repository.root, repository.head, "", repository.target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stable, unquotedConfigDiff) {
		t.Fatal("stable diff changed with repository core.quotePath configuration")
	}
}

func TestWritebackDiffApprovedRejectsExecutableBitChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable bit is not a Windows Git worktree contract")
	}
	repository := newWritebackTestRepository(t, "", "")
	resultHash := repository.writeTarget(t, []byte("# approved\n"))
	if err := os.Chmod(filepath.Join(repository.root, filepath.FromSlash(repository.target)), 0o750); err != nil {
		t.Fatal(err)
	}
	_, err := repository.client.DiffApproved(context.Background(), repository.diffRequest(resultHash))
	requireGitErrorCode(t, err, "GIT_TARGET_MODE_CONFLICT", foundation.ErrorVersionConflict)
}

func TestWritebackDiffApprovedRejectsUnexpectedWorktreeState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, writebackTestRepository)
		code   string
	}{
		{"result hash", func(t *testing.T, r writebackTestRepository) {}, "GIT_RESULT_HASH_CONFLICT"},
		{"untracked", func(t *testing.T, r writebackTestRepository) {
			if err := os.WriteFile(filepath.Join(r.root, "other.md"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "GIT_REPOSITORY_UNTRACKED"},
		{"staged", func(t *testing.T, r writebackTestRepository) { runWritebackGit(t, r.root, "add", "--", r.target) }, "GIT_TARGET_DIFF_CONFLICT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newWritebackTestRepository(t, "", "")
			resultHash := repository.writeTarget(t, []byte("# approved\n"))
			test.mutate(t, repository)
			if test.name == "result hash" {
				resultHash = strings.Repeat("a", 64)
			}
			_, err := repository.client.DiffApproved(context.Background(), repository.diffRequest(resultHash))
			requireGitErrorCode(t, err, test.code, foundation.ErrorVersionConflict)
		})
	}
}

func TestWritebackDiffApprovedRejectsDetachedHeadBeforeGitPrepared(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	runWritebackGit(t, repository.root, "checkout", "--detach", repository.head)
	resultHash := repository.writeTarget(t, []byte("# approved\n"))
	_, err := repository.client.DiffApproved(context.Background(), repository.diffRequest(resultHash))
	requireGitErrorCode(t, err, "GIT_REPOSITORY_DETACHED", foundation.ErrorVersionConflict)
}

func TestWritebackDiffApprovedRejectsUnsafeRequestAndTrackedSymlink(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	_, err := repository.client.DiffApproved(context.Background(), changecontrol.GitDiffRequest{
		WorkspaceID:     writebackTestWorkspaceID,
		TargetPath:      "notes/bad\tpath.md",
		ApprovedGitHead: repository.head,
		ResultHash:      strings.Repeat("a", 64),
	})
	requireGitErrorCode(t, err, "GIT_DIFF_INPUT_INVALID", foundation.ErrorInvalidInput)

	root := t.TempDir()
	runWritebackGit(t, root, "init", "--initial-branch=main")
	runWritebackGit(t, root, "config", "user.name", "Zhixu Test")
	runWritebackGit(t, root, "config", "user.email", "test@example.invalid")
	if err := os.Symlink("first.md", filepath.Join(root, "target.md")); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, root, "add", "target.md")
	runWritebackGit(t, root, "commit", "-m", "symlink")
	head := strings.TrimSpace(runWritebackGit(t, root, "rev-parse", "HEAD"))
	if err := os.Remove(filepath.Join(root, "target.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("second.md", filepath.Join(root, "target.md")); err != nil {
		t.Fatal(err)
	}
	client, err := NewWritebackClient(New(""), writebackWorkspaceRepository{workspace: workspacedomain.Workspace{ID: writebackTestWorkspaceID, RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DiffApproved(context.Background(), changecontrol.GitDiffRequest{WorkspaceID: writebackTestWorkspaceID, TargetPath: "target.md", ApprovedGitHead: head, ResultHash: changecontrol.ComputeWritebackResultHash([]byte("second.md"))})
	requireGitErrorCode(t, err, "GIT_TARGET_OBJECT_UNSAFE", foundation.ErrorPermissionDenied)
}

func TestWritebackDiffApprovedRejectsAttributesAndWhitespaceErrors(t *testing.T) {
	t.Run("crlf conversion", func(t *testing.T) {
		repository := newWritebackTestRepository(t, "", "")
		if err := os.WriteFile(filepath.Join(repository.root, ".gitattributes"), []byte("*.md text eol=crlf\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runWritebackGit(t, repository.root, "add", ".gitattributes")
		runWritebackGit(t, repository.root, "commit", "-m", "attributes")
		repository.head = strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD"))
		resultHash := repository.writeTarget(t, []byte("# approved\r\n"))
		_, err := repository.client.DiffApproved(context.Background(), repository.diffRequest(resultHash))
		requireGitErrorCode(t, err, "GIT_TARGET_ATTRIBUTES_TRANSFORM_CONTENT", foundation.ErrorPermissionDenied)
	})
	t.Run("filter", func(t *testing.T) {
		repository := newWritebackTestRepository(t, "", "")
		if err := os.WriteFile(filepath.Join(repository.root, ".gitattributes"), []byte("*.md filter=malicious\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runWritebackGit(t, repository.root, "add", ".gitattributes")
		runWritebackGit(t, repository.root, "commit", "-m", "attributes")
		repository.head = strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD"))
		resultHash := repository.writeTarget(t, []byte("# approved\n"))
		_, err := repository.client.DiffApproved(context.Background(), repository.diffRequest(resultHash))
		requireGitErrorCode(t, err, "GIT_TARGET_FILTER_UNSAFE", foundation.ErrorPermissionDenied)
	})
	t.Run("whitespace", func(t *testing.T) {
		repository := newWritebackTestRepository(t, "", "")
		resultHash := repository.writeTarget(t, []byte("# approved   \n"))
		_, err := repository.client.DiffApproved(context.Background(), repository.diffRequest(resultHash))
		requireGitErrorCode(t, err, "GIT_DIFF_WHITESPACE_INVALID", foundation.ErrorInvalidInput)
	})
}

func TestReadSafeWorkspaceFileRejectsUnsafeParentsAndTargets(t *testing.T) {
	t.Run("parent symlink", func(t *testing.T) {
		root := t.TempDir()
		external := t.TempDir()
		if err := os.WriteFile(filepath.Join(external, "target.md"), []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(root, "notes")); err != nil {
			t.Fatal(err)
		}
		_, err := readSafeWorkspaceFile(root, "notes/target.md")
		requireGitErrorCode(t, err, "GIT_TARGET_PARENT_UNSAFE", foundation.ErrorPermissionDenied)
	})
	t.Run("target symlink", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.md")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "target.md")); err != nil {
			t.Fatal(err)
		}
		_, err := readSafeWorkspaceFile(root, "target.md")
		requireGitErrorCode(t, err, "GIT_TARGET_UNSAFE", foundation.ErrorPermissionDenied)
	})
}

func TestWritebackHelpersReadCommitDiffPathsAndBlob(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	resultHash := repository.writeTarget(t, []byte("# committed\n"))
	runWritebackGit(t, repository.root, "add", "--", repository.target)
	runWritebackGit(t, repository.root, "commit", "-m", "change")
	tip := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD"))
	paths, err := repository.client.readChangedPaths(context.Background(), repository.root, repository.head, tip)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != repository.target {
		t.Fatalf("paths = %#v", paths)
	}
	blobHash, err := repository.client.readBlobSHA256(context.Background(), repository.root, tip, repository.target)
	if err != nil {
		t.Fatal(err)
	}
	if blobHash != resultHash {
		t.Fatalf("blob hash = %s, want %s", blobHash, resultHash)
	}
	diff, err := repository.client.readStableDiff(context.Background(), repository.root, repository.head, tip, repository.target)
	if err != nil || len(diff) == 0 {
		t.Fatalf("diff length=%d error=%v", len(diff), err)
	}
	if err := validateStableDiffBlobBinding(diff, strings.Repeat("a", len(repository.head)), strings.Repeat("b", len(repository.head)), changecontrol.GitFileModeRegular); err == nil {
		t.Fatal("stable diff accepted unrelated blob binding")
	}
}

func TestStatusConflictParserSkipsRenameOriginalPath(t *testing.T) {
	oid := strings.Repeat("a", 40)
	status := []byte("2 R. N... 100644 100644 100644 " + oid + " " + oid + " R100 renamed.md\x00unsafe.md\x00")
	if statusHasConflict(status) {
		t.Fatal("rename original path beginning with u was classified as conflict")
	}
	err := classifyDirtyStatus(status)
	requireGitErrorCode(t, err, "GIT_REPOSITORY_STAGED", foundation.ErrorVersionConflict)
}

func TestClassifyGitReadErrorSeparatesCancellationAndOutputLimit(t *testing.T) {
	cancelled := classifyGitReadError("GIT_READ_FAILED", context.Canceled)
	requireGitErrorCode(t, cancelled, "GIT_COMMAND_CANCELLED", foundation.ErrorNonRetryableFailure)
	limited := classifyGitReadError("GIT_READ_FAILED", &commandError{err: errCommandOutputLimit, outputLimit: true})
	requireGitErrorCode(t, limited, "GIT_COMMAND_OUTPUT_TOO_LARGE", foundation.ErrorPermissionDenied)
}

func TestWritebackInspectSupportsSHA256RepositoryWhenAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git SHA-256 test is not required on Windows")
	}
	root := t.TempDir()
	commandOutput := runGitProbe(root, "init", "--object-format=sha256", "--initial-branch=main")
	if commandOutput.err != nil {
		t.Skipf("Git SHA-256 repositories unsupported: %v: %s", commandOutput.err, commandOutput.output)
	}
	runWritebackGit(t, root, "config", "user.name", "Zhixu Test")
	runWritebackGit(t, root, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "target.md"), []byte("baseline"), 0o600); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, root, "add", "target.md")
	runWritebackGit(t, root, "commit", "-m", "initial")
	head := strings.TrimSpace(runWritebackGit(t, root, "rev-parse", "HEAD"))
	client, err := NewWritebackClient(New(""), writebackWorkspaceRepository{workspace: workspacedomain.Workspace{ID: writebackTestWorkspaceID, RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Inspect(context.Background(), writebackTestWorkspaceID, head)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ObjectFormat != changecontrol.GitObjectFormatSHA256 || len(snapshot.Head) != 64 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

type gitProbeResult struct {
	output string
	err    error
}

func runGitProbe(root string, args ...string) gitProbeResult {
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	return gitProbeResult{output: string(output), err: err}
}

func TestGitWritebackErrorsKeepDomainSentinels(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	repository.writeTarget(t, []byte("changed"))
	_, err := repository.client.Inspect(context.Background(), writebackTestWorkspaceID, repository.head)
	if !errors.Is(err, changecontrol.ErrGitVersionConflict) {
		t.Fatalf("error = %#v", err)
	}
}
