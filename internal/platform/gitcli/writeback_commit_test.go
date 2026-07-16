package gitcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	changecontrol "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWritebackCommitApprovedCreatesFixedVerifiedCommitAndReplays(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "notes/- 安全 写回.md")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n\n内容\n"))
	hookMarker := filepath.Join(repository.root, "hook-ran")
	hook := fmt.Sprintf("#!/bin/sh\nprintf ran > %q\nexit 97\n", hookMarker)
	for _, name := range []string{"pre-commit", "prepare-commit-msg", "commit-msg", "reference-transaction", "post-index-change"} {
		if err := os.WriteFile(filepath.Join(repository.root, ".git", "hooks", name), []byte(hook), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runWritebackGit(t, repository.root, "config", "commit.gpgsign", "true")
	runWritebackGit(t, repository.root, "config", "user.signingkey", "missing-test-key")

	commit, err := repository.client.CommitApproved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if commit.Recovered || commit.Replayed || commit.Operation != changecontrol.GitOperationApply || commit.ParentGitCommit != repository.head {
		t.Fatalf("commit = %#v", commit)
	}
	if head := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD")); head != commit.GitCommit {
		t.Fatalf("HEAD = %s, want %s", head, commit.GitCommit)
	}
	object, err := repository.client.readCommitObject(context.Background(), repository.root, commit.GitCommit)
	if err != nil {
		t.Fatal(err)
	}
	if object.message != fixedCommitMessage(lookupFromCommitRequest(request)) {
		t.Fatalf("message = %q", object.message)
	}
	if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hook marker exists or cannot be inspected: %v", err)
	}
	if status := runWritebackGit(t, repository.root, "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
		t.Fatalf("status = %q", status)
	}

	replayed, err := repository.client.CommitApproved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Recovered || replayed.GitCommit != commit.GitCommit {
		t.Fatalf("replayed = %#v", replayed)
	}
}

func TestWritebackCommitApprovedSupportsSHA256RepositoryWhenAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git SHA-256 test is not required on Windows")
	}
	root := t.TempDir()
	probe := runGitProbe(root, "init", "--object-format=sha256", "--initial-branch=main")
	if probe.err != nil {
		t.Skipf("Git SHA-256 repositories unsupported: %v: %s", probe.err, probe.output)
	}
	runWritebackGit(t, root, "config", "user.name", "Zhixu Test")
	runWritebackGit(t, root, "config", "user.email", "test@example.invalid")
	target := "target.md"
	if err := os.WriteFile(filepath.Join(root, target), []byte("# baseline\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, root, "add", "--", target)
	runWritebackGit(t, root, "commit", "-m", "initial")
	client, err := NewWritebackClient(New(""), writebackWorkspaceRepository{workspace: workspaceForWritebackTest(root)})
	if err != nil {
		t.Fatal(err)
	}
	repository := writebackTestRepository{root: root, target: target, head: strings.TrimSpace(runWritebackGit(t, root, "rev-parse", "HEAD")), client: client}
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	commit, err := client.CommitApproved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(commit.GitCommit) != 64 || len(commit.ResultBlobID) != 64 || len(commit.ParentGitCommit) != 64 {
		t.Fatalf("commit = %#v", commit)
	}
}

func TestWritebackCommitApprovedRecoversCommitAfterCommandError(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	faultClient := newCommitFaultClient(t, repository, true)

	commit, err := faultClient.CommitApproved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !commit.Recovered || commit.Replayed {
		t.Fatalf("commit = %#v", commit)
	}
	if count := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-list", "--count", "HEAD")); count != "2" {
		t.Fatalf("commit count = %s", count)
	}
	if status := runWritebackGit(t, repository.root, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("status = %q", status)
	}
}

func TestWritebackCommitApprovedRestoresIndexWhenCommitDidNotOccur(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "-comma,name.md")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	hookMarker := filepath.Join(repository.root, "index-hook-ran")
	hook := fmt.Sprintf("#!/bin/sh\nprintf ran > %q\nexit 97\n", hookMarker)
	if err := os.WriteFile(filepath.Join(repository.root, ".git", "hooks", "post-index-change"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	faultClient := newCommitFaultClient(t, repository, false)

	_, err := faultClient.CommitApproved(context.Background(), request)
	requireGitErrorCode(t, err, "GIT_COMMIT_FAILED", foundation.ErrorNonRetryableFailure)
	if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("post-index-change hook ran or cannot be inspected: %v", err)
	}
	if head := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD")); head != repository.head {
		t.Fatalf("HEAD = %s, want %s", head, repository.head)
	}
	if staged := runWritebackGit(t, repository.root, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("staged paths = %q", staged)
	}
	if worktree := strings.TrimSpace(runWritebackGit(t, repository.root, "diff", "--name-only")); worktree != repository.target {
		t.Fatalf("worktree paths = %q", worktree)
	}
}

func TestFindWritebackCommitRejectsDuplicateExecutionTrailers(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	if _, err := repository.client.CommitApproved(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	conflictBody := changecontrol.GitTrailerOperation + ": apply\n" + changecontrol.GitTrailerWritebackID + ": " + string(request.WritebackExecutionID)
	runWritebackGit(t, repository.root, "commit", "--allow-empty", "--no-gpg-sign", "-m", "conflicting writeback", "-m", conflictBody)

	_, err := repository.client.FindWritebackCommit(context.Background(), lookupFromCommitRequest(request))
	requireGitErrorCode(t, err, "GIT_COMMIT_LOOKUP_CONFLICT", foundation.ErrorConsistencyViolation)
}

func TestFindWritebackCommitIgnoresReplaceObjects(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	forward, err := repository.client.CommitApproved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	tree := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", forward.GitCommit+"^{tree}"))
	fake := strings.TrimSpace(runWritebackGitInput(t, repository.root, "fake replacement\n", "commit-tree", tree, "-p", forward.ParentGitCommit))
	runWritebackGit(t, repository.root, "-c", "core.hooksPath=/dev/null", "replace", forward.GitCommit, fake)

	found, err := repository.client.FindWritebackCommit(context.Background(), lookupFromCommitRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	if found.GitCommit != forward.GitCommit {
		t.Fatalf("found = %#v", found)
	}
}

func TestFindWritebackCommitDoesNotExecuteSignatureProgram(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	forward, err := repository.client.CommitApproved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	tree := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", forward.GitCommit+"^{tree}"))
	unsigned := strings.TrimSpace(runWritebackGitInput(t, repository.root, "signed descendant\n", "commit-tree", tree, "-p", forward.GitCommit))
	raw := runWritebackGit(t, repository.root, "cat-file", "commit", unsigned)
	headers, message, found := strings.Cut(raw, "\n\n")
	if !found {
		t.Fatal("unsigned commit object has no message separator")
	}
	signedRaw := headers + "\ngpgsig -----BEGIN PGP SIGNATURE-----\n fake\n -----END PGP SIGNATURE-----\n\n" + message
	signed := strings.TrimSpace(runWritebackGitInput(t, repository.root, signedRaw, "hash-object", "-t", "commit", "-w", "--stdin"))
	runWritebackGit(t, repository.root, "-c", "core.hooksPath=/dev/null", "update-ref", "refs/heads/main", signed, forward.GitCommit)
	marker := filepath.Join(repository.root, "gpg-program-ran")
	gpgProgram := writeExecutable(t, fmt.Sprintf("#!/bin/sh\nprintf ran > %q\nexit 1\n", marker))
	runWritebackGit(t, repository.root, "config", "log.showSignature", "true")
	runWritebackGit(t, repository.root, "config", "gpg.program", gpgProgram)

	foundCommit, err := repository.client.FindWritebackCommit(context.Background(), lookupFromCommitRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	if foundCommit.GitCommit != forward.GitCommit {
		t.Fatalf("found = %#v", foundCommit)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("signature program ran or cannot be inspected: %v", err)
	}
}

func TestWritebackImmutableTreeRejectsInjectedStagedPath(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	runWritebackGit(t, repository.root, "add", "--", repository.target)
	if err := os.WriteFile(filepath.Join(repository.root, "other.md"), []byte("injected"), 0o600); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, repository.root, "add", "--", "other.md")
	tree := strings.TrimSpace(runWritebackGit(t, repository.root, "write-tree"))

	err := repository.client.verifyImmutableTree(context.Background(), repository.root, tree, lookupFromCommitRequest(request))
	requireGitErrorCode(t, err, "GIT_COMMIT_TREE_PATH_CONFLICT", foundation.ErrorVersionConflict)
	if head := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD")); head != repository.head {
		t.Fatalf("HEAD = %s, want %s", head, repository.head)
	}
}

func TestWritebackPublishCommitUsesExpectedOldHeadCAS(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	runWritebackGit(t, repository.root, "add", "--", repository.target)
	if err := repository.client.verifyStagedTarget(context.Background(), repository.root, request.ApprovedGitHead, request.TargetPath, request.BaseMode, request.ResultBlobID, request.DiffHash, ""); err != nil {
		t.Fatal(err)
	}
	branchRef, commitID, err := repository.client.createCommitObject(context.Background(), repository.root, lookupFromCommitRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	baseTree := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", repository.head+"^{tree}"))
	drift := strings.TrimSpace(runWritebackGitInput(t, repository.root, "drift\n", "commit-tree", baseTree, "-p", repository.head))
	runWritebackGit(t, repository.root, "-c", "core.hooksPath=/dev/null", "update-ref", branchRef, drift, repository.head)

	if err := repository.client.publishCommitCAS(context.Background(), repository.root, branchRef, commitID, repository.head); err == nil {
		t.Fatal("update-ref CAS accepted a drifted branch")
	}
	if head := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD")); head != drift {
		t.Fatalf("HEAD = %s, want drift %s", head, drift)
	}
	contains := exec.Command("git", "-C", repository.root, "merge-base", "--is-ancestor", commitID, "HEAD")
	if err := contains.Run(); err == nil {
		t.Fatal("unpublished system commit became reachable after CAS failure")
	}
}

func TestWritebackIndexRecoveryDoesNotOverwriteUserStagedBlob(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	runWritebackGit(t, repository.root, "add", "--", repository.target)
	repository.writeTarget(t, []byte("# user staged after failure\n"))
	runWritebackGit(t, repository.root, "add", "--", repository.target)
	_, userBlobBefore, err := repository.client.readIndexEntry(context.Background(), repository.root, repository.target)
	if err != nil {
		t.Fatal(err)
	}

	if err := repository.client.restoreApprovedIndex(context.Background(), repository.root, lookupFromCommitRequest(request)); err == nil {
		t.Fatal("index recovery overwrote a user-staged target")
	}
	_, userBlobAfter, err := repository.client.readIndexEntry(context.Background(), repository.root, repository.target)
	if err != nil {
		t.Fatal(err)
	}
	if userBlobAfter != userBlobBefore || userBlobAfter == request.BaseBlobID || userBlobAfter == request.ResultBlobID {
		t.Fatalf("user staged blob changed: before=%s after=%s", userBlobBefore, userBlobAfter)
	}
}

func TestWritebackCreateReverseCommitRestoresBaseAndReplays(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	forward, err := repository.client.CommitApproved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reverseRequest := changecontrol.ReverseCommitRequest{
		Commit:           forward,
		ExpectedBaseHash: changecontrol.ComputeWritebackResultHash([]byte("# baseline\n")),
	}
	reverse, err := repository.client.CreateReverseCommit(context.Background(), reverseRequest)
	if err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) {
			var inner *foundation.Error
			if errors.As(classified.Cause, &inner) {
				t.Fatalf("%v inner=%s/%s status=%q", err, inner.Code, inner.Kind, runWritebackGit(t, repository.root, "status", "--porcelain=v2"))
			}
			t.Fatalf("%v cause=%#v", err, classified.Cause)
		}
		t.Fatal(err)
	}
	if reverse.Operation != changecontrol.GitOperationRevert || reverse.ParentGitCommit != forward.GitCommit || reverse.RevertsCommit != forward.GitCommit || reverse.Recovered || reverse.Replayed {
		t.Fatalf("reverse = %#v", reverse)
	}
	content, err := os.ReadFile(filepath.Join(repository.root, filepath.FromSlash(repository.target)))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "# baseline\n" {
		t.Fatalf("content = %q", content)
	}
	if status := runWritebackGit(t, repository.root, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("status = %q", status)
	}
	replayed, err := repository.client.CreateReverseCommit(context.Background(), reverseRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.GitCommit != reverse.GitCommit {
		t.Fatalf("replayed reverse = %#v", replayed)
	}
}

func TestWritebackCreateReverseCommitRecoversPublishedCommitAfterCommandError(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	forward, err := repository.client.CommitApproved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	faultClient := newCommitFaultClient(t, repository, true)
	reverse, err := faultClient.CreateReverseCommit(context.Background(), changecontrol.ReverseCommitRequest{
		Commit:           forward,
		ExpectedBaseHash: changecontrol.ComputeWritebackResultHash([]byte("# baseline\n")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reverse.Recovered || reverse.Replayed || reverse.RevertsCommit != forward.GitCommit {
		t.Fatalf("reverse = %#v", reverse)
	}
	if status := runWritebackGit(t, repository.root, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("status = %q", status)
	}
}

func TestWritebackCreateReverseCommitPreservesUnknownRevertState(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	forward, err := repository.client.CommitApproved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	faultClient := newRevertFaultClient(t, repository)
	_, err = faultClient.CreateReverseCommit(context.Background(), changecontrol.ReverseCommitRequest{
		Commit:           forward,
		ExpectedBaseHash: changecontrol.ComputeWritebackResultHash([]byte("# baseline\n")),
	})
	requireGitErrorCode(t, err, "GIT_REVERSE_RESULT_UNKNOWN", foundation.ErrorManualRecoveryRequired)
	if head := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD")); head != forward.GitCommit {
		t.Fatalf("HEAD = %s, want %s", head, forward.GitCommit)
	}
	if revertHead := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "--verify", "REVERT_HEAD")); revertHead != forward.GitCommit {
		t.Fatalf("REVERT_HEAD = %s, want %s", revertHead, forward.GitCommit)
	}
	if staged := strings.TrimSpace(runWritebackGit(t, repository.root, "diff", "--cached", "--name-only")); staged != repository.target {
		t.Fatalf("staged = %q", staged)
	}
}

func TestWritebackCreateReverseCommitReplaysReachableHistoryAfterLaterCommit(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	forward, err := repository.client.CommitApproved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reverseRequest := changecontrol.ReverseCommitRequest{Commit: forward, ExpectedBaseHash: changecontrol.ComputeWritebackResultHash([]byte("# baseline\n"))}
	reverse, err := repository.client.CreateReverseCommit(context.Background(), reverseRequest)
	if err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, repository.root, "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "--no-gpg-sign", "-m", "later commit")

	replayed, err := repository.client.CreateReverseCommit(context.Background(), reverseRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.GitCommit != reverse.GitCommit {
		t.Fatalf("replayed = %#v", replayed)
	}
}

func TestWritebackCreateReverseCommitRejectsHeadDriftAndDirtyRepository(t *testing.T) {
	t.Run("head drift", func(t *testing.T) {
		repository := newWritebackTestRepository(t, "", "")
		request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
		forward, err := repository.client.CommitApproved(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		runWritebackGit(t, repository.root, "commit", "--allow-empty", "--no-gpg-sign", "-m", "drift")
		_, err = repository.client.CreateReverseCommit(context.Background(), changecontrol.ReverseCommitRequest{Commit: forward, ExpectedBaseHash: changecontrol.ComputeWritebackResultHash([]byte("# baseline\n"))})
		requireGitErrorCode(t, err, "GIT_HEAD_CONFLICT", foundation.ErrorVersionConflict)
	})
	t.Run("dirty", func(t *testing.T) {
		repository := newWritebackTestRepository(t, "", "")
		request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
		forward, err := repository.client.CommitApproved(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repository.root, "other.md"), []byte("dirty"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = repository.client.CreateReverseCommit(context.Background(), changecontrol.ReverseCommitRequest{Commit: forward, ExpectedBaseHash: changecontrol.ComputeWritebackResultHash([]byte("# baseline\n"))})
		requireGitErrorCode(t, err, "GIT_REPOSITORY_UNTRACKED", foundation.ErrorVersionConflict)
	})
}

func TestWritebackCreateReverseCommitRejectsContentFilterWithoutExecutingIt(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "")
	request := newApprovedCommitRequest(t, repository, []byte("# approved\n"))
	forward, err := repository.client.CommitApproved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repository.root, "filter-ran")
	filter := writeExecutable(t, fmt.Sprintf("#!/bin/sh\nprintf ran > %q\ncat\n", marker))
	if err := os.WriteFile(filepath.Join(repository.root, ".git", "info", "attributes"), []byte("*.md filter=evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, repository.root, "config", "filter.evil.clean", filter)
	runWritebackGit(t, repository.root, "config", "filter.evil.smudge", filter)
	runWritebackGit(t, repository.root, "config", "filter.evil.required", "true")

	_, err = repository.client.CreateReverseCommit(context.Background(), changecontrol.ReverseCommitRequest{
		Commit:           forward,
		ExpectedBaseHash: changecontrol.ComputeWritebackResultHash([]byte("# baseline\n")),
	})
	requireGitErrorCode(t, err, "GIT_TARGET_FILTER_UNSAFE", foundation.ErrorPermissionDenied)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("filter ran or cannot be inspected: %v", err)
	}
}

func newApprovedCommitRequest(t *testing.T, repository writebackTestRepository, content []byte) changecontrol.GitCommitRequest {
	t.Helper()
	resultHash := repository.writeTarget(t, content)
	diff, err := repository.client.DiffApproved(context.Background(), repository.diffRequest(resultHash))
	if err != nil {
		t.Fatal(err)
	}
	return changecontrol.GitCommitRequest{
		WorkspaceID: repository.diffRequest(resultHash).WorkspaceID, WorkflowRunID: "workflow-run", NodeRunID: "node-run",
		WritebackExecutionID: "execution", ProposalID: "proposal", RevisionID: "revision", ApprovalID: "approval",
		Operation: changecontrol.GitOperationApply, TargetPath: diff.TargetPath, ApprovedGitHead: diff.ApprovedGitHead,
		ResultHash: diff.ResultHash, DiffHash: diff.DiffHash, BaseBlobID: diff.BaseBlobID, ResultBlobID: diff.ResultBlobID, BaseMode: diff.BaseMode,
	}
}

func newCommitFaultClient(t *testing.T, repository writebackTestRepository, publishBeforeError bool) *WritebackClient {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	action := "exit 73"
	if publishBeforeError {
		action = fmt.Sprintf("%q \"$@\"\nexit 73", gitPath)
	}
	script := fmt.Sprintf(`#!/bin/sh
is_publish=0
for arg in "$@"; do
  if [ "$arg" = "update-ref" ]; then
    is_publish=1
  fi
done
if [ "$is_publish" = "1" ]; then
  %s
fi
exec %q "$@"
`, action, gitPath)
	executable := writeExecutable(t, script)
	client, err := NewWritebackClient(New(executable), repository.client.workspaces)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newRevertFaultClient(t *testing.T, repository writebackTestRepository) *WritebackClient {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
is_revert=0
for arg in "$@"; do
  if [ "$arg" = "--no-commit" ]; then
    is_revert=1
  fi
done
if [ "$is_revert" = "1" ]; then
  %q "$@"
  exit 73
fi
exec %q "$@"
`, gitPath, gitPath)
	executable := writeExecutable(t, script)
	client, err := NewWritebackClient(New(executable), repository.client.workspaces)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func runWritebackGitInput(t *testing.T, root, input string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0")
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
