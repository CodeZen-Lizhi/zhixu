package gitcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	changecontrol "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// CommitApproved creates one fully-bound single-target commit, or returns the
// exact commit created by an earlier replay/unknown-result attempt.
func (c *WritebackClient) CommitApproved(ctx context.Context, request changecontrol.GitCommitRequest) (changecontrol.GitCommit, error) {
	if err := changecontrol.ValidateGitCommitRequest(request); err != nil {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorInvalidInput, "GIT_COMMIT_INPUT_INVALID", false, err)
	}
	lookup := lookupFromCommitRequest(request)
	if existing, err := c.FindWritebackCommit(ctx, lookup); err == nil {
		if changecontrol.NormalizeTargetMode(request.TargetMode) == changecontrol.TargetModeCreateOnly {
			root, resolveErr := c.resolveWritebackRoot(ctx, request.WorkspaceID)
			if resolveErr != nil {
				return changecontrol.GitCommit{}, resolveErr
			}
			if reconcileErr := c.reconcilePublishedCreateOnlyIndex(ctx, root, lookup, existing.GitCommit); reconcileErr != nil {
				return changecontrol.GitCommit{}, manualPublishedWriteError("GIT_COMMIT_POSTCONDITION_FAILED", reconcileErr)
			}
			if _, inspectErr := c.Inspect(ctx, request.WorkspaceID, existing.GitCommit); inspectErr != nil {
				return changecontrol.GitCommit{}, manualPublishedWriteError("GIT_COMMIT_POSTCONDITION_FAILED", inspectErr)
			}
		}
		existing.Replayed, existing.Recovered = true, false
		return existing, nil
	} else if !errors.Is(err, changecontrol.ErrGitNotFound) {
		return changecontrol.GitCommit{}, err
	}
	root, err := c.resolveWritebackRoot(ctx, request.WorkspaceID)
	if err != nil {
		return changecontrol.GitCommit{}, err
	}
	if err := c.ensureFilterUnspecified(ctx, root, request.TargetPath); err != nil {
		return changecontrol.GitCommit{}, err
	}

	diff, err := c.DiffApproved(ctx, changecontrol.GitDiffRequest{
		WorkspaceID: request.WorkspaceID, TargetPath: request.TargetPath,
		TargetMode: request.TargetMode, ApprovedGitHead: request.ApprovedGitHead, ResultHash: request.ResultHash,
	})
	if err != nil {
		return changecontrol.GitCommit{}, err
	}
	if !sameApprovedDiff(request, diff) {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorVersionConflict, "GIT_COMMIT_DIFF_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	indexFile := ""
	cleanupIndex := func() {}
	if changecontrol.NormalizeTargetMode(request.TargetMode) == changecontrol.TargetModeCreateOnly {
		indexFile, cleanupIndex, err = c.createPrivateIndex(ctx, root, request.ApprovedGitHead)
		if err == nil {
			err = c.stageCreateOnlyInIndex(ctx, root, indexFile, lookup)
		}
		if err == nil {
			err = c.verifyCreateOnlyPrivateIndex(ctx, root, indexFile, lookup)
		}
	} else {
		err = c.stageApprovedContent(ctx, root, lookup)
		if err == nil {
			err = c.verifyStagedTarget(ctx, root, request.ApprovedGitHead, request.TargetPath, request.TargetMode, request.BaseMode, request.ResultBlobID, request.DiffHash, "")
		}
	}
	if err != nil {
		cleanupIndex()
		return c.recoverApplyCommand(ctx, root, lookup, err, "GIT_STAGE_FAILED")
	}
	defer cleanupIndex()
	branchRef, commitID, err := c.createCommitObject(ctx, root, lookup, indexFile)
	if err != nil {
		return c.recoverApplyCommand(ctx, root, lookup, err, "GIT_COMMIT_OBJECT_FAILED")
	}
	if err := c.publishCommitCAS(ctx, root, branchRef, commitID, request.ApprovedGitHead); err != nil {
		return c.recoverApplyCommand(ctx, root, lookup, err, "GIT_COMMIT_FAILED")
	}
	head, err := c.readRepositoryHead(ctx, root)
	if err != nil {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_COMMIT_RESULT_UNKNOWN", false, errors.Join(changecontrol.ErrGitManualRecoveryRequired, err))
	}
	if !strings.EqualFold(head, commitID) {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_COMMIT_POSTCONDITION_FAILED", false, changecontrol.ErrGitManualRecoveryRequired)
	}
	if changecontrol.NormalizeTargetMode(request.TargetMode) == changecontrol.TargetModeCreateOnly {
		if err := c.reconcilePublishedCreateOnlyIndex(ctx, root, lookup, commitID); err != nil {
			return changecontrol.GitCommit{}, manualPublishedWriteError("GIT_COMMIT_POSTCONDITION_FAILED", err)
		}
	}
	commit, err := c.verifyLookupCommit(ctx, root, head, lookup)
	if err != nil {
		return changecontrol.GitCommit{}, manualPublishedWriteError("GIT_COMMIT_POSTCONDITION_FAILED", err)
	}
	commit.Replayed, commit.Recovered = false, false
	if _, err := c.Inspect(ctx, request.WorkspaceID, head); err != nil {
		return changecontrol.GitCommit{}, manualPublishedWriteError("GIT_COMMIT_POSTCONDITION_FAILED", err)
	}
	if err := changecontrol.ValidateGitCommitBinding(request, commit); err != nil {
		return changecontrol.GitCommit{}, manualPublishedWriteError("GIT_COMMIT_BINDING_INVALID", err)
	}
	return commit, nil
}

// FindWritebackCommit searches only the bounded current-branch history and
// accepts a candidate only after full object/message/content verification.
func (c *WritebackClient) FindWritebackCommit(ctx context.Context, lookup changecontrol.GitCommitLookup) (changecontrol.GitCommit, error) {
	if err := changecontrol.ValidateGitCommitLookup(lookup); err != nil {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorInvalidInput, "GIT_COMMIT_LOOKUP_INPUT_INVALID", false, err)
	}
	root, err := c.resolveWritebackRoot(ctx, lookup.WorkspaceID)
	if err != nil {
		return changecontrol.GitCommit{}, err
	}
	if err := c.ensureRepositoryTopLevel(ctx, root); err != nil {
		return changecontrol.GitCommit{}, err
	}
	if _, err := c.git.output(ctx, root, "symbolic-ref", "--quiet", "HEAD"); err != nil {
		if isExitCode(err, 1) {
			return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorVersionConflict, "GIT_REPOSITORY_DETACHED", false, changecontrol.ErrGitVersionConflict)
		}
		return changecontrol.GitCommit{}, classifyGitReadError("GIT_COMMIT_LOOKUP_BRANCH_FAILED", err)
	}
	limit := c.historyLimit
	if limit <= 0 || limit > changecontrol.GitCommitLookupLimit {
		limit = changecontrol.GitCommitLookupLimit
	}
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: gitStatusRecordLimit},
		"log", "--no-show-signature", "-z", "--max-count="+strconv.Itoa(limit), "--format=%H%x00%B", "HEAD")
	if err != nil {
		return changecontrol.GitCommit{}, classifyGitReadError("GIT_COMMIT_LOOKUP_FAILED", err)
	}
	parts := splitNUL(result.Stdout)
	if len(parts)%2 != 0 {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_HISTORY_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	candidates := make([]string, 0, 1)
	for i := 0; i < len(parts); i += 2 {
		message := string(parts[i+1])
		if hasTrailerValue(message, changecontrol.GitTrailerWritebackID, string(lookup.WritebackExecutionID)) &&
			hasTrailerValue(message, changecontrol.GitTrailerOperation, string(lookup.Operation)) &&
			(changecontrol.NormalizeTargetMode(lookup.TargetMode) != changecontrol.TargetModeCreateOnly ||
				hasTrailerValue(message, changecontrol.GitTrailerTargetMode, string(changecontrol.TargetModeCreateOnly))) {
			candidates = append(candidates, strings.ToLower(string(parts[i])))
		}
	}
	if len(candidates) == 0 {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorNotFound, "GIT_COMMIT_NOT_FOUND", false, changecontrol.ErrGitNotFound)
	}
	if len(candidates) != 1 {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_LOOKUP_CONFLICT", false, changecontrol.ErrGitConsistencyViolation)
	}
	commit, err := c.verifyLookupCommit(ctx, root, candidates[0], lookup)
	if err != nil {
		return changecontrol.GitCommit{}, err
	}
	commit.Replayed, commit.Recovered = true, false
	return commit, nil
}

// CreateReverseCommit reverts a system-generated commit only while that commit
// is still the clean attached HEAD. Conflict/unknown state is intentionally
// preserved for manual recovery; this adapter never resets or aborts it.
func (c *WritebackClient) CreateReverseCommit(ctx context.Context, request changecontrol.ReverseCommitRequest) (changecontrol.GitCommit, error) {
	if err := changecontrol.ValidateReverseCommitRequest(request); err != nil {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorInvalidInput, "GIT_REVERSE_INPUT_INVALID", false, err)
	}
	original := request.Commit
	root, err := c.resolveWritebackRoot(ctx, original.WorkspaceID)
	if err != nil {
		return changecontrol.GitCommit{}, err
	}
	if err := c.ensureFilterUnspecified(ctx, root, original.TargetPath); err != nil {
		return changecontrol.GitCommit{}, err
	}
	if err := c.ensureAttributeUnspecified(ctx, root, original.TargetPath, "merge", "GIT_TARGET_MERGE_UNSAFE"); err != nil {
		return changecontrol.GitCommit{}, err
	}
	if changecontrol.NormalizeTargetMode(original.TargetMode) == changecontrol.TargetModeCreateOnly {
		if err := c.ensureTreeTargetAbsent(ctx, root, original.ParentGitCommit, original.TargetPath); err != nil {
			return changecontrol.GitCommit{}, err
		}
	} else {
		baseHash, err := c.readBlobSHA256(ctx, root, original.ParentGitCommit, original.TargetPath)
		if err != nil {
			return changecontrol.GitCommit{}, err
		}
		if !strings.EqualFold(baseHash, request.ExpectedBaseHash) {
			return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_REVERSE_BASE_HASH_CONFLICT", false, changecontrol.ErrGitConsistencyViolation)
		}
	}
	reverseDiff, err := c.readStableDiff(ctx, root, original.GitCommit, original.ParentGitCommit, original.TargetPath)
	if err != nil {
		return changecontrol.GitCommit{}, err
	}
	lookup := reverseLookup(request, sha256Hex(reverseDiff))
	if existing, lookupErr := c.FindWritebackCommit(ctx, lookup); lookupErr == nil {
		existing, err = c.finalizePublishedReverse(ctx, root, lookup, existing.GitCommit)
		if err != nil {
			return changecontrol.GitCommit{}, err
		}
		existing.Replayed, existing.Recovered = true, false
		return existing, nil
	} else if !errors.Is(lookupErr, changecontrol.ErrGitNotFound) {
		return changecontrol.GitCommit{}, lookupErr
	}
	currentContent, err := readSafeWorkspaceFile(root, original.TargetPath)
	if err != nil {
		return changecontrol.GitCommit{}, err
	}
	if !strings.EqualFold(sha256Hex(currentContent), original.ResultHash) {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorVersionConflict, "GIT_REVERSE_RESULT_HASH_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	currentBlob, err := c.hashApprovedContent(ctx, root, currentContent)
	if err != nil {
		return changecontrol.GitCommit{}, err
	}
	if !strings.EqualFold(currentBlob, original.ResultBlobID) {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorVersionConflict, "GIT_REVERSE_RESULT_BLOB_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	if _, err := c.Inspect(ctx, original.WorkspaceID, original.GitCommit); err != nil {
		return changecontrol.GitCommit{}, err
	}
	if _, err := c.verifyLookupCommit(ctx, root, original.GitCommit, lookupFromExistingCommit(original)); err != nil {
		return changecontrol.GitCommit{}, err
	}
	if _, err := c.git.runCommand(ctx, root, commandOptions{}, revertNoCommitArgs(original.GitCommit)...); err != nil {
		return c.recoverReverseCommand(ctx, root, lookup, err, "GIT_REVERT_FAILED")
	}
	if err := c.verifyStagedTarget(ctx, root, original.GitCommit, original.TargetPath, original.TargetMode, original.BaseMode, original.BaseBlobID, lookup.DiffHash, original.GitCommit); err != nil {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_REVERT_STAGED_STATE_INVALID", false, errors.Join(changecontrol.ErrGitManualRecoveryRequired, err))
	}
	if changecontrol.NormalizeTargetMode(original.TargetMode) == changecontrol.TargetModeCreateOnly {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(original.TargetPath))); err == nil {
			return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_REVERT_RESULT_INVALID", false, changecontrol.ErrGitManualRecoveryRequired)
		} else if !errors.Is(err, os.ErrNotExist) {
			return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_REVERT_RESULT_INVALID", false, errors.Join(changecontrol.ErrGitManualRecoveryRequired, err))
		}
	} else {
		content, err := readSafeWorkspaceFile(root, original.TargetPath)
		if err != nil || !strings.EqualFold(sha256Hex(content), request.ExpectedBaseHash) {
			return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_REVERT_RESULT_INVALID", false, errors.Join(changecontrol.ErrGitManualRecoveryRequired, err))
		}
	}
	branchRef, commitID, err := c.createCommitObject(ctx, root, lookup, "")
	if err != nil {
		return c.recoverReverseCommand(ctx, root, lookup, err, "GIT_REVERSE_COMMIT_OBJECT_FAILED")
	}
	if err := c.publishCommitCAS(ctx, root, branchRef, commitID, original.GitCommit); err != nil {
		return c.recoverReverseCommand(ctx, root, lookup, err, "GIT_REVERSE_COMMIT_FAILED")
	}
	commit, err := c.finalizePublishedReverse(ctx, root, lookup, commitID)
	if err != nil {
		return changecontrol.GitCommit{}, err
	}
	commit.Replayed, commit.Recovered = false, false
	if err := changecontrol.ValidateReverseCommitBinding(request, commit); err != nil {
		return changecontrol.GitCommit{}, manualPublishedWriteError("GIT_REVERSE_BINDING_INVALID", err)
	}
	return commit, nil
}

func lookupFromCommitRequest(request changecontrol.GitCommitRequest) changecontrol.GitCommitLookup {
	return changecontrol.GitCommitLookup{
		WorkspaceID: request.WorkspaceID, WorkflowRunID: request.WorkflowRunID, NodeRunID: request.NodeRunID,
		WritebackExecutionID: request.WritebackExecutionID, ProposalID: request.ProposalID,
		RevisionID: request.RevisionID, ApprovalID: request.ApprovalID, Operation: request.Operation,
		TargetPath: request.TargetPath, ApprovedGitHead: request.ApprovedGitHead,
		TargetMode: request.TargetMode, ResultHash: request.ResultHash, DiffHash: request.DiffHash, BaseBlobID: request.BaseBlobID,
		ResultBlobID: request.ResultBlobID, BaseMode: request.BaseMode,
	}
}

func lookupFromExistingCommit(commit changecontrol.GitCommit) changecontrol.GitCommitLookup {
	return changecontrol.GitCommitLookup{
		WorkspaceID: commit.WorkspaceID, WorkflowRunID: commit.WorkflowRunID, NodeRunID: commit.NodeRunID,
		WritebackExecutionID: commit.WritebackExecutionID, ProposalID: commit.ProposalID,
		RevisionID: commit.RevisionID, ApprovalID: commit.ApprovalID, Operation: commit.Operation,
		TargetPath: commit.TargetPath, ApprovedGitHead: commit.ApprovedGitHead,
		TargetMode: commit.TargetMode, ResultHash: commit.ResultHash, DiffHash: commit.DiffHash, BaseBlobID: commit.BaseBlobID,
		ResultBlobID: commit.ResultBlobID, BaseMode: commit.BaseMode, RevertsCommit: commit.RevertsCommit,
	}
}

func reverseLookup(request changecontrol.ReverseCommitRequest, diffHash string) changecontrol.GitCommitLookup {
	original := request.Commit
	return changecontrol.GitCommitLookup{
		WorkspaceID: original.WorkspaceID, WorkflowRunID: original.WorkflowRunID, NodeRunID: original.NodeRunID,
		WritebackExecutionID: original.WritebackExecutionID, ProposalID: original.ProposalID,
		RevisionID: original.RevisionID, ApprovalID: original.ApprovalID, Operation: changecontrol.GitOperationRevert,
		TargetPath: original.TargetPath, ApprovedGitHead: original.ApprovedGitHead,
		TargetMode: original.TargetMode, ResultHash: request.ExpectedBaseHash, DiffHash: diffHash, BaseBlobID: original.ResultBlobID,
		ResultBlobID: reverseResultBlobID(original), BaseMode: original.BaseMode, RevertsCommit: original.GitCommit,
	}
}

func reverseResultBlobID(original changecontrol.GitCommit) string {
	if changecontrol.NormalizeTargetMode(original.TargetMode) == changecontrol.TargetModeCreateOnly {
		return original.BaseBlobID
	}
	return original.BaseBlobID
}

func sameApprovedDiff(request changecontrol.GitCommitRequest, diff changecontrol.GitDiff) bool {
	return request.WorkspaceID == diff.WorkspaceID && request.TargetPath == diff.TargetPath &&
		changecontrol.NormalizeTargetMode(request.TargetMode) == changecontrol.NormalizeTargetMode(diff.TargetMode) &&
		strings.EqualFold(request.ApprovedGitHead, diff.ApprovedGitHead) && strings.EqualFold(request.ResultHash, diff.ResultHash) &&
		strings.EqualFold(request.DiffHash, diff.DiffHash) && strings.EqualFold(request.BaseBlobID, diff.BaseBlobID) &&
		strings.EqualFold(request.ResultBlobID, diff.ResultBlobID) && request.BaseMode == diff.BaseMode
}

func fixedCommitMessage(lookup changecontrol.GitCommitLookup) string {
	subject := changecontrol.GitCommitSubject
	if lookup.Operation == changecontrol.GitOperationRevert {
		subject = changecontrol.GitReverseCommitSubject
	}
	lines := []string{
		subject, "",
		changecontrol.GitTrailerOperation + ": " + string(lookup.Operation),
		changecontrol.GitTrailerWritebackID + ": " + string(lookup.WritebackExecutionID),
		changecontrol.GitTrailerProposalID + ": " + string(lookup.ProposalID),
		changecontrol.GitTrailerRevisionID + ": " + string(lookup.RevisionID),
		changecontrol.GitTrailerApprovalID + ": " + string(lookup.ApprovalID),
		changecontrol.GitTrailerWorkflowRunID + ": " + string(lookup.WorkflowRunID),
		changecontrol.GitTrailerWorkflowNodeID + ": " + string(lookup.NodeRunID),
		changecontrol.GitTrailerTargetPath + ": " + lookup.TargetPath,
		changecontrol.GitTrailerResultSHA256 + ": " + strings.ToLower(lookup.ResultHash),
		changecontrol.GitTrailerDiffSHA256 + ": " + strings.ToLower(lookup.DiffHash),
	}
	if changecontrol.NormalizeTargetMode(lookup.TargetMode) == changecontrol.TargetModeCreateOnly {
		lines = append(lines, changecontrol.GitTrailerTargetMode+": "+string(changecontrol.TargetModeCreateOnly))
	}
	if lookup.Operation == changecontrol.GitOperationRevert {
		lines = append(lines, changecontrol.GitTrailerRevertsCommit+": "+strings.ToLower(lookup.RevertsCommit))
	}
	return strings.Join(lines, "\n") + "\n"
}

func revertNoCommitArgs(commit string) []string {
	return []string{"-c", "commit.gpgsign=false", "revert", "--no-commit", "--no-edit", commit}
}

func hasTrailerValue(message, key, value string) bool {
	want := key + ": " + value
	for _, line := range strings.Split(strings.TrimSuffix(message, "\n"), "\n") {
		if line == want {
			return true
		}
	}
	return false
}

func (c *WritebackClient) ensureAttributeUnspecified(ctx context.Context, root, target, attribute, code string) error {
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true}, "check-attr", "-z", attribute, "--", target)
	if err != nil {
		return classifyGitReadError("GIT_TARGET_ATTRIBUTE_INSPECT_FAILED", err)
	}
	parts := splitNUL(result.Stdout)
	if len(parts) != 3 || string(parts[0]) != target || string(parts[1]) != attribute {
		return gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_TARGET_ATTRIBUTE_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	if string(parts[2]) != "unspecified" {
		return gitWritebackError(foundation.ErrorPermissionDenied, code, false, changecontrol.ErrGitPermissionDenied)
	}
	return nil
}

func (c *WritebackClient) stageApprovedContent(ctx context.Context, root string, lookup changecontrol.GitCommitLookup) error {
	if changecontrol.NormalizeTargetMode(lookup.TargetMode) == changecontrol.TargetModeCreateOnly {
		return gitWritebackError(foundation.ErrorInvalidInput, "GIT_CREATE_ONLY_PRIVATE_INDEX_REQUIRED", false, changecontrol.ErrGitInvalidInput)
	}
	content, err := readSafeWorkspaceFile(root, lookup.TargetPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sha256Hex(content), lookup.ResultHash) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_RESULT_HASH_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	result, err := c.git.runCommand(ctx, root, commandOptions{Stdin: bytes.NewReader(content)}, "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		return err
	}
	blobID := strings.TrimSpace(string(result.Stdout))
	if !changecontrol.ValidGitObjectID(blobID) || !strings.EqualFold(blobID, lookup.ResultBlobID) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_RESULT_BLOB_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	mode, currentBlob, err := c.readIndexEntry(ctx, root, lookup.TargetPath)
	if err != nil {
		return err
	}
	if mode != lookup.BaseMode || !strings.EqualFold(currentBlob, lookup.BaseBlobID) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_INDEX_BASE_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	record := fmt.Sprintf("%s %s\t%s\x00", lookup.BaseMode, strings.ToLower(lookup.ResultBlobID), lookup.TargetPath)
	_, err = c.git.runCommand(ctx, root, commandOptions{Stdin: strings.NewReader(record)}, "update-index", "-z", "--index-info")
	return err
}

func (c *WritebackClient) createCommitObject(ctx context.Context, root string, lookup changecontrol.GitCommitLookup, indexFile string) (string, string, error) {
	branchRef, err := c.readFullBranchRef(ctx, root)
	if err != nil {
		return "", "", err
	}
	treeResult, err := c.git.runCommand(ctx, root, commandOptions{IndexFile: indexFile}, "write-tree")
	if err != nil {
		return "", "", err
	}
	treeID := strings.TrimSpace(string(treeResult.Stdout))
	if !changecontrol.ValidGitObjectID(treeID) || len(treeID) != len(lookup.ApprovedGitHead) {
		return "", "", gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_TREE_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	if err := c.verifyImmutableTree(ctx, root, strings.ToLower(treeID), lookup); err != nil {
		return "", "", err
	}
	parent := lookup.ApprovedGitHead
	if lookup.Operation == changecontrol.GitOperationRevert {
		parent = lookup.RevertsCommit
	}
	result, err := c.git.runCommand(ctx, root, commandOptions{Stdin: strings.NewReader(fixedCommitMessage(lookup))},
		"-c", "commit.gpgsign=false", "commit-tree", treeID, "-p", parent, "-F", "-")
	if err != nil {
		return "", "", err
	}
	commitID := strings.TrimSpace(string(result.Stdout))
	if !changecontrol.ValidGitObjectID(commitID) || len(commitID) != len(parent) {
		return "", "", gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_ID_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	if _, err := c.verifyLookupCommit(ctx, root, strings.ToLower(commitID), lookup); err != nil {
		return "", "", err
	}
	return branchRef, strings.ToLower(commitID), nil
}

func (c *WritebackClient) readFullBranchRef(ctx context.Context, root string) (string, error) {
	ref, err := c.git.output(ctx, root, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		if isExitCode(err, 1) {
			return "", gitWritebackError(foundation.ErrorVersionConflict, "GIT_REPOSITORY_DETACHED", false, changecontrol.ErrGitVersionConflict)
		}
		return "", classifyGitReadError("GIT_BRANCH_INSPECT_FAILED", err)
	}
	if !strings.HasPrefix(ref, "refs/heads/") || strings.TrimSpace(ref) != ref || strings.ContainsAny(ref, "\x00\r\n") {
		return "", gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_BRANCH_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	return ref, nil
}

func (c *WritebackClient) verifyImmutableTree(ctx context.Context, root, treeID string, lookup changecontrol.GitCommitLookup) error {
	parent := lookup.ApprovedGitHead
	if lookup.Operation == changecontrol.GitOperationRevert {
		parent = lookup.RevertsCommit
	}
	paths, err := c.readChangedPaths(ctx, root, parent, treeID)
	if err != nil {
		return err
	}
	if len(paths) != 1 || paths[0] != lookup.TargetPath {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_COMMIT_TREE_PATH_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	if changecontrol.NormalizeTargetMode(lookup.TargetMode) == changecontrol.TargetModeCreateOnly {
		if lookup.Operation == changecontrol.GitOperationApply {
			if err := c.ensureTreeTargetAbsent(ctx, root, parent, lookup.TargetPath); err != nil {
				return err
			}
			mode, blobID, err := c.readTrackedBlob(ctx, root, treeID, lookup.TargetPath)
			if err != nil {
				return err
			}
			if mode != lookup.BaseMode || !strings.EqualFold(blobID, lookup.ResultBlobID) {
				return gitWritebackError(foundation.ErrorVersionConflict, "GIT_COMMIT_TREE_BLOB_CONFLICT", false, changecontrol.ErrGitVersionConflict)
			}
			resultHash, err := c.readBlobSHA256(ctx, root, treeID, lookup.TargetPath)
			if err != nil {
				return err
			}
			if !strings.EqualFold(resultHash, lookup.ResultHash) {
				return gitWritebackError(foundation.ErrorVersionConflict, "GIT_COMMIT_TREE_RESULT_CONFLICT", false, changecontrol.ErrGitVersionConflict)
			}
		} else {
			mode, blobID, err := c.readTrackedBlob(ctx, root, parent, lookup.TargetPath)
			if err != nil {
				return err
			}
			if mode != lookup.BaseMode || !strings.EqualFold(blobID, lookup.BaseBlobID) {
				return gitWritebackError(foundation.ErrorVersionConflict, "GIT_COMMIT_TREE_BLOB_CONFLICT", false, changecontrol.ErrGitVersionConflict)
			}
			if err := c.ensureTreeTargetAbsent(ctx, root, treeID, lookup.TargetPath); err != nil {
				return err
			}
		}
		diffBytes, err := c.readStableDiff(ctx, root, parent, treeID, lookup.TargetPath)
		if err != nil {
			return err
		}
		if !strings.EqualFold(sha256Hex(diffBytes), lookup.DiffHash) {
			return gitWritebackError(foundation.ErrorVersionConflict, "GIT_COMMIT_TREE_DIFF_CONFLICT", false, changecontrol.ErrGitVersionConflict)
		}
		return nil
	}
	mode, blobID, err := c.readTrackedBlob(ctx, root, treeID, lookup.TargetPath)
	if err != nil {
		return err
	}
	if mode != lookup.BaseMode || !strings.EqualFold(blobID, lookup.ResultBlobID) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_COMMIT_TREE_BLOB_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	resultHash, err := c.readBlobSHA256(ctx, root, treeID, lookup.TargetPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(resultHash, lookup.ResultHash) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_COMMIT_TREE_RESULT_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	diffBytes, err := c.readStableDiff(ctx, root, parent, treeID, lookup.TargetPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sha256Hex(diffBytes), lookup.DiffHash) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_COMMIT_TREE_DIFF_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	return nil
}

func (c *WritebackClient) publishCommitCAS(ctx context.Context, root, branchRef, commitID, expectedHead string) error {
	currentRef, err := c.readFullBranchRef(ctx, root)
	if err != nil {
		return err
	}
	if currentRef != branchRef {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_BRANCH_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	_, err = c.git.runCommand(ctx, root, commandOptions{}, "update-ref", "-m", "ZHIXU approved writeback", branchRef, commitID, expectedHead)
	return err
}

func (c *WritebackClient) verifyStagedTarget(ctx context.Context, root, base, target string, targetMode changecontrol.TargetMode, mode, blobID, diffHash, expectedRevert string) error {
	branch, err := c.git.output(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if isExitCode(err, 1) {
			return gitWritebackError(foundation.ErrorVersionConflict, "GIT_REPOSITORY_DETACHED", false, changecontrol.ErrGitVersionConflict)
		}
		return classifyGitReadError("GIT_BRANCH_INSPECT_FAILED", err)
	}
	if strings.TrimSpace(branch) == "" {
		return gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_BRANCH_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	for _, identity := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		if _, err := c.git.output(ctx, root, "var", identity); err != nil {
			return gitWritebackError(foundation.ErrorPermissionDenied, "GIT_AUTHOR_IDENTITY_MISSING", false, errors.Join(changecontrol.ErrGitPermissionDenied, err))
		}
	}
	head, err := c.readRepositoryHead(ctx, root)
	if err != nil {
		return err
	}
	if !strings.EqualFold(head, base) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_HEAD_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	if err := c.ensureExpectedOperationState(ctx, root, expectedRevert); err != nil {
		return err
	}
	if err := c.ensureNoHiddenIndexFlags(ctx, root); err != nil {
		return err
	}
	status, err := c.readPorcelainStatus(ctx, root)
	if err != nil {
		return err
	}
	expectedChange := "M"
	if changecontrol.NormalizeTargetMode(targetMode) == changecontrol.TargetModeCreateOnly {
		if expectedRevert == "" {
			expectedChange = "A"
		} else {
			expectedChange = "D"
		}
	}
	if err := validateOnlyStagedTarget(status, target, expectedChange); err != nil {
		return err
	}
	paths, err := c.readStagedPaths(ctx, root, base, expectedChange)
	if err != nil {
		return err
	}
	if len(paths) != 1 || paths[0] != target {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_STAGED_PATH_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	stagedMode, stagedBlob, found, err := c.readIndexEntryOptional(ctx, root, target)
	if err != nil {
		return err
	}
	if expectedChange == "D" {
		if found {
			return gitWritebackError(foundation.ErrorVersionConflict, "GIT_STAGED_BLOB_CONFLICT", false, changecontrol.ErrGitVersionConflict)
		}
	} else if !found || stagedMode != mode || !strings.EqualFold(stagedBlob, blobID) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_STAGED_BLOB_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	if _, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true}, "diff", "--cached", "--check", "--no-ext-diff", "--no-textconv", base, "--", target); err != nil {
		return gitWritebackError(foundation.ErrorInvalidInput, "GIT_STAGED_DIFF_INVALID", false, errors.Join(changecontrol.ErrGitInvalidInput, err))
	}
	stagedDiff, err := c.readCachedStableDiff(ctx, root, base, target)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sha256Hex(stagedDiff), diffHash) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_STAGED_DIFF_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	return nil
}

func (c *WritebackClient) ensureExpectedOperationState(ctx context.Context, root, expectedRevert string) error {
	if expectedRevert == "" {
		return c.ensureNoOperationInProgress(ctx, root)
	}
	revertHead, err := c.git.output(ctx, root, "rev-parse", "--verify", "REVERT_HEAD")
	if err != nil || !strings.EqualFold(revertHead, expectedRevert) {
		return gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_REVERT_STATE_INVALID", false, errors.Join(changecontrol.ErrGitConsistencyViolation, err))
	}
	for _, name := range inProgressGitPaths {
		if name == "REVERT_HEAD" {
			continue
		}
		marker, err := c.git.output(ctx, root, "rev-parse", "--git-path", name)
		if err != nil {
			return classifyGitReadError("GIT_OPERATION_INSPECT_FAILED", err)
		}
		if !filepath.IsAbs(marker) {
			marker = filepath.Join(root, marker)
		}
		if _, err := os.Lstat(marker); err == nil {
			return gitWritebackError(foundation.ErrorVersionConflict, "GIT_OPERATION_IN_PROGRESS", false, changecontrol.ErrGitVersionConflict)
		} else if !errors.Is(err, os.ErrNotExist) {
			return gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_OPERATION_INSPECT_FAILED", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
		}
	}
	return nil
}

func (c *WritebackClient) quitExpectedRevertState(ctx context.Context, root, expectedRevert string) error {
	if err := c.ensureExpectedOperationState(ctx, root, expectedRevert); err != nil {
		return err
	}
	_, err := c.git.runCommand(ctx, root, commandOptions{}, "revert", "--quit")
	return err
}

func (c *WritebackClient) finalizePublishedReverse(ctx context.Context, root string, lookup changecontrol.GitCommitLookup, commitID string) (changecontrol.GitCommit, error) {
	head, err := c.readRepositoryHead(ctx, root)
	if err != nil {
		return changecontrol.GitCommit{}, manualPublishedWriteError("GIT_REVERSE_POSTCONDITION_FAILED", err)
	}
	if err := c.cleanupPublishedReverseState(ctx, root, lookup.RevertsCommit, commitID, head); err != nil {
		return changecontrol.GitCommit{}, err
	}
	if !strings.EqualFold(head, commitID) {
		_, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true}, "merge-base", "--is-ancestor", commitID, head)
		if err != nil {
			if isExitCode(err, 1) {
				return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorVersionConflict, "GIT_HEAD_CONFLICT", false, changecontrol.ErrGitVersionConflict)
			}
			return changecontrol.GitCommit{}, manualPublishedWriteError("GIT_REVERSE_POSTCONDITION_FAILED", err)
		}
	}
	commit, err := c.verifyLookupCommit(ctx, root, commitID, lookup)
	if err != nil {
		return changecontrol.GitCommit{}, manualPublishedWriteError("GIT_REVERSE_POSTCONDITION_FAILED", err)
	}
	if _, err := c.Inspect(ctx, lookup.WorkspaceID, head); err != nil {
		return changecontrol.GitCommit{}, manualPublishedWriteError("GIT_REVERSE_POSTCONDITION_FAILED", err)
	}
	return commit, nil
}

func (c *WritebackClient) cleanupPublishedReverseState(ctx context.Context, root, expectedRevert, commitID, head string) error {
	marker, err := c.git.output(ctx, root, "rev-parse", "--git-path", "REVERT_HEAD")
	if err != nil {
		return manualPublishedWriteError("GIT_REVERSE_POSTCONDITION_FAILED", err)
	}
	if !filepath.IsAbs(marker) {
		marker = filepath.Join(root, marker)
	}
	if _, err := os.Lstat(marker); errors.Is(err, os.ErrNotExist) {
		if err := c.ensureNoOperationInProgress(ctx, root); err != nil {
			return manualPublishedWriteError("GIT_REVERSE_POSTCONDITION_FAILED", err)
		}
		return nil
	} else if err != nil {
		return manualPublishedWriteError("GIT_REVERSE_POSTCONDITION_FAILED", err)
	}
	if !strings.EqualFold(head, commitID) {
		return manualPublishedWriteError("GIT_REVERSE_POSTCONDITION_FAILED", errors.New("revert state exists after branch head advanced"))
	}
	if err := c.quitExpectedRevertState(ctx, root, expectedRevert); err != nil {
		return manualPublishedWriteError("GIT_REVERSE_POSTCONDITION_FAILED", err)
	}
	return nil
}

func validateOnlyStagedTarget(status []byte, target, expectedChange string) error {
	records := splitNUL(status)
	if len(records) != 1 {
		return classifyDirtyStatus(status)
	}
	fields := strings.SplitN(string(records[0]), " ", 9)
	if len(fields) != 9 || fields[0] != "1" || fields[1] != expectedChange+"." || fields[8] != target {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_STAGED_STATE_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	return nil
}

func (c *WritebackClient) readStagedPaths(ctx context.Context, root, base, expectedChange string) ([]string, error) {
	return c.readStagedPathsFromIndex(ctx, root, base, expectedChange, "")
}

func (c *WritebackClient) readStagedPathsFromIndex(ctx context.Context, root, base, expectedChange, indexFile string) ([]string, error) {
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: gitStatusRecordLimit, IndexFile: indexFile},
		"diff", "--cached", "--name-status", "-z", "--no-renames", base, "--")
	if err != nil {
		return nil, classifyGitReadError("GIT_STAGED_PATHS_FAILED", err)
	}
	parts := splitNUL(result.Stdout)
	if len(parts)%2 != 0 {
		return nil, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_STAGED_PATHS_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	paths := make([]string, 0, len(parts)/2)
	for i := 0; i < len(parts); i += 2 {
		if string(parts[i]) != expectedChange || unsafeGitPath(string(parts[i+1])) {
			return nil, gitWritebackError(foundation.ErrorVersionConflict, "GIT_STAGED_PATH_CONFLICT", false, changecontrol.ErrGitVersionConflict)
		}
		paths = append(paths, string(parts[i+1]))
	}
	return paths, nil
}

func (c *WritebackClient) readIndexEntry(ctx context.Context, root, target string) (string, string, error) {
	mode, blobID, found, err := c.readIndexEntryOptional(ctx, root, target)
	if err != nil {
		return "", "", err
	}
	if !found {
		return "", "", gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_INDEX_ENTRY_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	return mode, blobID, nil
}

func (c *WritebackClient) readIndexEntryOptional(ctx context.Context, root, target string) (string, string, bool, error) {
	return c.readIndexEntryOptionalFromIndex(ctx, root, target, "")
}

func (c *WritebackClient) readIndexEntryOptionalFromIndex(ctx context.Context, root, target, indexFile string) (string, string, bool, error) {
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, IndexFile: indexFile}, "ls-files", "--stage", "-z", "--", target)
	if err != nil {
		return "", "", false, classifyGitReadError("GIT_INDEX_ENTRY_FAILED", err)
	}
	entries := splitNUL(result.Stdout)
	if len(entries) == 0 {
		return "", "", false, nil
	}
	if len(entries) != 1 {
		return "", "", false, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_INDEX_ENTRY_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	metadata, pathValue, found := strings.Cut(string(entries[0]), "\t")
	fields := strings.Fields(metadata)
	if !found || pathValue != target || len(fields) != 3 || fields[2] != "0" || !changecontrol.ValidGitFileMode(fields[0]) || !changecontrol.ValidGitObjectID(fields[1]) {
		return "", "", false, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_INDEX_ENTRY_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	return fields[0], strings.ToLower(fields[1]), true, nil
}

func (c *WritebackClient) readCachedStableDiff(ctx context.Context, root, base, target string) ([]byte, error) {
	return c.readCachedStableDiffFromIndex(ctx, root, base, target, "")
}

func (c *WritebackClient) readCachedStableDiffFromIndex(ctx context.Context, root, base, target, indexFile string) ([]byte, error) {
	args := []string{
		"diff", "--cached", "--no-ext-diff", "--no-textconv", "--no-color", "--full-index", "--binary", "--no-renames",
		"--diff-algorithm=myers", "--no-indent-heuristic", "--inter-hunk-context=0",
		"--src-prefix=a/", "--dst-prefix=b/", "--unified=3", base, "--", target,
	}
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: gitDiffOutputLimit, IndexFile: indexFile}, args...)
	if err != nil {
		return nil, classifyGitReadError("GIT_STAGED_DIFF_FAILED", err)
	}
	return result.Stdout, nil
}

func (c *WritebackClient) verifyLookupCommit(ctx context.Context, root, commitID string, lookup changecontrol.GitCommitLookup) (changecontrol.GitCommit, error) {
	if !changecontrol.ValidGitObjectID(commitID) {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_ID_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	object, err := c.readCommitObject(ctx, root, commitID)
	if err != nil {
		return changecontrol.GitCommit{}, err
	}
	expectedParent := lookup.ApprovedGitHead
	if lookup.Operation == changecontrol.GitOperationRevert {
		expectedParent = lookup.RevertsCommit
	}
	if len(object.parents) != 1 || !strings.EqualFold(object.parents[0], expectedParent) || object.message != fixedCommitMessage(lookup) {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_OBJECT_CONFLICT", false, changecontrol.ErrGitConsistencyViolation)
	}
	paths, err := c.readChangedPaths(ctx, root, expectedParent, commitID)
	if err != nil {
		return changecontrol.GitCommit{}, err
	}
	if len(paths) != 1 || paths[0] != lookup.TargetPath {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_PATH_CONFLICT", false, changecontrol.ErrGitConsistencyViolation)
	}
	if changecontrol.NormalizeTargetMode(lookup.TargetMode) == changecontrol.TargetModeCreateOnly {
		if lookup.Operation == changecontrol.GitOperationApply {
			if err := c.ensureTreeTargetAbsent(ctx, root, expectedParent, lookup.TargetPath); err != nil {
				return changecontrol.GitCommit{}, err
			}
			resultMode, resultBlob, err := c.readTrackedBlob(ctx, root, commitID, lookup.TargetPath)
			if err != nil {
				return changecontrol.GitCommit{}, err
			}
			if resultMode != lookup.BaseMode || !strings.EqualFold(resultBlob, lookup.ResultBlobID) {
				return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_BLOB_CONFLICT", false, changecontrol.ErrGitConsistencyViolation)
			}
			resultHash, err := c.readBlobSHA256(ctx, root, commitID, lookup.TargetPath)
			if err != nil {
				return changecontrol.GitCommit{}, err
			}
			if !strings.EqualFold(resultHash, lookup.ResultHash) {
				return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_RESULT_HASH_CONFLICT", false, changecontrol.ErrGitConsistencyViolation)
			}
		} else {
			baseMode, baseBlob, err := c.readTrackedBlob(ctx, root, expectedParent, lookup.TargetPath)
			if err != nil {
				return changecontrol.GitCommit{}, err
			}
			if baseMode != lookup.BaseMode || !strings.EqualFold(baseBlob, lookup.BaseBlobID) {
				return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_BLOB_CONFLICT", false, changecontrol.ErrGitConsistencyViolation)
			}
			if err := c.ensureTreeTargetAbsent(ctx, root, commitID, lookup.TargetPath); err != nil {
				return changecontrol.GitCommit{}, err
			}
		}
	} else {
		baseMode, baseBlob, err := c.readTrackedBlob(ctx, root, expectedParent, lookup.TargetPath)
		if err != nil {
			return changecontrol.GitCommit{}, err
		}
		resultMode, resultBlob, err := c.readTrackedBlob(ctx, root, commitID, lookup.TargetPath)
		if err != nil {
			return changecontrol.GitCommit{}, err
		}
		if baseMode != lookup.BaseMode || resultMode != lookup.BaseMode || !strings.EqualFold(baseBlob, lookup.BaseBlobID) || !strings.EqualFold(resultBlob, lookup.ResultBlobID) {
			return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_BLOB_CONFLICT", false, changecontrol.ErrGitConsistencyViolation)
		}
		resultHash, err := c.readBlobSHA256(ctx, root, commitID, lookup.TargetPath)
		if err != nil {
			return changecontrol.GitCommit{}, err
		}
		if !strings.EqualFold(resultHash, lookup.ResultHash) {
			return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_RESULT_HASH_CONFLICT", false, changecontrol.ErrGitConsistencyViolation)
		}
	}
	diffBytes, err := c.readStableDiff(ctx, root, expectedParent, commitID, lookup.TargetPath)
	if err != nil {
		return changecontrol.GitCommit{}, err
	}
	if !strings.EqualFold(sha256Hex(diffBytes), lookup.DiffHash) {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_DIFF_HASH_CONFLICT", false, changecontrol.ErrGitConsistencyViolation)
	}
	commit := changecontrol.GitCommit{
		WorkspaceID: lookup.WorkspaceID, WorkflowRunID: lookup.WorkflowRunID, NodeRunID: lookup.NodeRunID,
		WritebackExecutionID: lookup.WritebackExecutionID, ProposalID: lookup.ProposalID,
		RevisionID: lookup.RevisionID, ApprovalID: lookup.ApprovalID, Operation: lookup.Operation,
		TargetPath: lookup.TargetPath, ApprovedGitHead: strings.ToLower(lookup.ApprovedGitHead),
		TargetMode: changecontrol.NormalizeTargetMode(lookup.TargetMode),
		GitCommit:  strings.ToLower(commitID), ParentGitCommit: strings.ToLower(expectedParent),
		ResultHash: strings.ToLower(lookup.ResultHash), DiffHash: strings.ToLower(lookup.DiffHash),
		BaseBlobID: strings.ToLower(lookup.BaseBlobID), ResultBlobID: strings.ToLower(lookup.ResultBlobID),
		BaseMode: lookup.BaseMode, RevertsCommit: strings.ToLower(lookup.RevertsCommit), Replayed: true,
	}
	if err := changecontrol.ValidateGitCommitLookupBinding(lookup, commit); err != nil {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_BINDING_INVALID", false, err)
	}
	return commit, nil
}

type parsedCommitObject struct {
	parents []string
	message string
}

func (c *WritebackClient) readCommitObject(ctx context.Context, root, commitID string) (parsedCommitObject, error) {
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true}, "cat-file", "commit", commitID)
	if err != nil {
		return parsedCommitObject{}, classifyGitReadError("GIT_COMMIT_OBJECT_READ_FAILED", err)
	}
	headers, message, found := bytes.Cut(result.Stdout, []byte("\n\n"))
	if !found {
		return parsedCommitObject{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_OBJECT_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	parsed := parsedCommitObject{message: string(message)}
	for _, line := range strings.Split(string(headers), "\n") {
		if strings.HasPrefix(line, "parent ") {
			parent := strings.TrimPrefix(line, "parent ")
			if !changecontrol.ValidGitObjectID(parent) {
				return parsedCommitObject{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_PARENT_INVALID", false, changecontrol.ErrGitConsistencyViolation)
			}
			parsed.parents = append(parsed.parents, strings.ToLower(parent))
		}
	}
	return parsed, nil
}

func (c *WritebackClient) recoverApplyCommand(ctx context.Context, root string, lookup changecontrol.GitCommitLookup, commandErr error, code string) (changecontrol.GitCommit, error) {
	recoveryCtx, cancel := c.newRecoveryContext(ctx)
	defer cancel()
	commit, lookupErr := c.FindWritebackCommit(recoveryCtx, lookup)
	if lookupErr == nil {
		head, err := c.readRepositoryHead(recoveryCtx, root)
		if err != nil || !strings.EqualFold(head, commit.GitCommit) {
			return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_COMMIT_RESULT_UNKNOWN", false, errors.Join(changecontrol.ErrGitManualRecoveryRequired, err))
		}
		if changecontrol.NormalizeTargetMode(lookup.TargetMode) == changecontrol.TargetModeCreateOnly {
			if err := c.reconcilePublishedCreateOnlyIndex(recoveryCtx, root, lookup, commit.GitCommit); err != nil {
				return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_COMMIT_RESULT_UNKNOWN", false, errors.Join(changecontrol.ErrGitManualRecoveryRequired, err))
			}
		}
		if _, err := c.Inspect(recoveryCtx, lookup.WorkspaceID, head); err != nil {
			return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_COMMIT_RESULT_UNKNOWN", false, errors.Join(changecontrol.ErrGitManualRecoveryRequired, err))
		}
		commit.Replayed, commit.Recovered = false, true
		return commit, nil
	}
	if !errors.Is(lookupErr, changecontrol.ErrGitNotFound) {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_COMMIT_RESULT_UNKNOWN", false, errors.Join(changecontrol.ErrGitManualRecoveryRequired, lookupErr))
	}
	head, err := c.readRepositoryHead(recoveryCtx, root)
	if err != nil || !strings.EqualFold(head, lookup.ApprovedGitHead) {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_COMMIT_RESULT_UNKNOWN", false, errors.Join(changecontrol.ErrGitManualRecoveryRequired, err))
	}
	if err := c.restoreApprovedIndex(recoveryCtx, root, lookup); err != nil {
		return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_INDEX_RECOVERY_FAILED", false, errors.Join(changecontrol.ErrGitManualRecoveryRequired, err))
	}
	return changecontrol.GitCommit{}, classifyConfirmedWriteFailure(code, commandErr)
}

func (c *WritebackClient) recoverReverseCommand(ctx context.Context, root string, lookup changecontrol.GitCommitLookup, commandErr error, code string) (changecontrol.GitCommit, error) {
	recoveryCtx, cancel := c.newRecoveryContext(ctx)
	defer cancel()
	commit, lookupErr := c.FindWritebackCommit(recoveryCtx, lookup)
	if lookupErr == nil {
		commit, err := c.finalizePublishedReverse(recoveryCtx, root, lookup, commit.GitCommit)
		if err != nil {
			return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_REVERSE_RESULT_UNKNOWN", false, errors.Join(changecontrol.ErrGitManualRecoveryRequired, err))
		}
		commit.Replayed, commit.Recovered = false, true
		return commit, nil
	}
	return changecontrol.GitCommit{}, gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_REVERSE_RESULT_UNKNOWN", false,
		errors.Join(changecontrol.ErrGitManualRecoveryRequired, commandErr, lookupErr, errors.New(code)))
}

func (c *WritebackClient) newRecoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := c.recoveryTimeout
	if timeout <= 0 || timeout > defaultWritebackRecoveryTimeout {
		timeout = defaultWritebackRecoveryTimeout
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func (c *WritebackClient) restoreApprovedIndex(ctx context.Context, root string, lookup changecontrol.GitCommitLookup) error {
	head, err := c.readRepositoryHead(ctx, root)
	if err != nil {
		return err
	}
	if !strings.EqualFold(head, lookup.ApprovedGitHead) {
		return errors.New("repository head changed before index recovery")
	}
	if changecontrol.NormalizeTargetMode(lookup.TargetMode) == changecontrol.TargetModeCreateOnly {
		return c.restoreCreateOnlyIndex(ctx, root, lookup)
	}
	mode, blob, err := c.readIndexEntry(ctx, root, lookup.TargetPath)
	if err != nil {
		return err
	}
	if mode != lookup.BaseMode {
		return errors.New("target index mode changed before recovery")
	}
	if strings.EqualFold(blob, lookup.BaseBlobID) {
		paths, err := c.readStagedPaths(ctx, root, lookup.ApprovedGitHead, "M")
		if err != nil {
			return err
		}
		if len(paths) != 0 {
			return errors.New("index contains unrelated staged changes")
		}
		return nil
	}
	if !strings.EqualFold(blob, lookup.ResultBlobID) {
		return errors.New("target index blob changed before recovery")
	}
	record := fmt.Sprintf("%s %s\t%s\x00", lookup.BaseMode, strings.ToLower(lookup.BaseBlobID), lookup.TargetPath)
	if _, err := c.git.runCommand(ctx, root, commandOptions{Stdin: strings.NewReader(record)}, "update-index", "-z", "--index-info"); err != nil {
		return err
	}
	mode, blob, err = c.readIndexEntry(ctx, root, lookup.TargetPath)
	if err != nil {
		return err
	}
	if mode != lookup.BaseMode || !strings.EqualFold(blob, lookup.BaseBlobID) {
		return errors.New("restored index entry does not match approved base")
	}
	paths, err := c.readStagedPaths(ctx, root, lookup.ApprovedGitHead, "M")
	if err != nil {
		return err
	}
	if len(paths) != 0 {
		return errors.New("index remains staged after recovery")
	}
	return nil
}

func (c *WritebackClient) restoreCreateOnlyIndex(ctx context.Context, root string, lookup changecontrol.GitCommitLookup) error {
	_, blob, found, err := c.readIndexEntryOptional(ctx, root, lookup.TargetPath)
	if err != nil {
		return err
	}
	if !found {
		paths, err := c.readStagedPaths(ctx, root, lookup.ApprovedGitHead, "A")
		if err != nil {
			return err
		}
		if len(paths) != 0 {
			return errors.New("index contains unrelated staged changes")
		}
		return nil
	}
	if !strings.EqualFold(blob, lookup.ResultBlobID) {
		return errors.New("target index blob changed before recovery")
	}
	if _, err := c.git.runCommand(ctx, root, commandOptions{Stdin: strings.NewReader(lookup.TargetPath + "\x00")}, "update-index", "--force-remove", "-z", "--stdin"); err != nil {
		return err
	}
	if _, _, found, err = c.readIndexEntryOptional(ctx, root, lookup.TargetPath); err != nil {
		return err
	} else if found {
		return errors.New("restored create-only index entry remains")
	}
	paths, err := c.readStagedPaths(ctx, root, lookup.ApprovedGitHead, "A")
	if err != nil {
		return err
	}
	if len(paths) != 0 {
		return errors.New("create-only index remains staged after recovery")
	}
	return nil
}

func classifyConfirmedWriteFailure(code string, err error) error {
	if errors.Is(err, context.Canceled) {
		return gitWritebackError(foundation.ErrorNonRetryableFailure, "GIT_COMMAND_CANCELLED", false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return gitWritebackError(foundation.ErrorRetryableFailure, commandContextCode(code), true, errors.Join(changecontrol.ErrGitRetryableFailure, err))
	}
	return gitWritebackError(foundation.ErrorNonRetryableFailure, code, false, err)
}

func manualPublishedWriteError(code string, err error) error {
	return gitWritebackError(foundation.ErrorManualRecoveryRequired, code, false, errors.Join(changecontrol.ErrGitManualRecoveryRequired, err))
}

var _ changecontrol.GitRepository = (*WritebackClient)(nil)
