package gitcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	changecontrol "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func (c *WritebackClient) createPrivateIndex(ctx context.Context, root, base string) (string, func(), error) {
	if !changecontrol.ValidGitObjectID(base) {
		return "", nil, gitWritebackError(foundation.ErrorInvalidInput, "GIT_PRIVATE_INDEX_BASE_INVALID", false, changecontrol.ErrGitInvalidInput)
	}
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true}, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", nil, classifyGitReadError("GIT_PRIVATE_INDEX_PATH_FAILED", err)
	}
	indexPath := strings.TrimSpace(string(result.Stdout))
	if indexPath == "" || strings.ContainsAny(indexPath, "\x00\r\n") {
		return "", nil, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_PRIVATE_INDEX_PATH_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(root, indexPath)
	}
	indexPath, err = filepath.Abs(indexPath)
	if err != nil {
		return "", nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_PRIVATE_INDEX_PATH_FAILED", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	indexDir, err := filepath.EvalSymlinks(filepath.Dir(indexPath))
	if err != nil {
		return "", nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_PRIVATE_INDEX_PATH_FAILED", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	info, err := os.Stat(indexDir)
	if err != nil || !info.IsDir() {
		return "", nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_PRIVATE_INDEX_PATH_FAILED", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	file, err := os.CreateTemp(indexDir, ".zhixu-create-only-index-*")
	if err != nil {
		return "", nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_PRIVATE_INDEX_CREATE_FAILED", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	privatePath := file.Name()
	cleanup := func() {
		_ = os.Remove(privatePath + ".lock")
		_ = os.Remove(privatePath)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_PRIVATE_INDEX_CREATE_FAILED", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_PRIVATE_INDEX_CREATE_FAILED", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	if _, err := c.git.runCommand(ctx, root, commandOptions{IndexFile: privatePath}, "read-tree", base); err != nil {
		cleanup()
		return "", nil, classifyGitReadError("GIT_PRIVATE_INDEX_INITIALIZE_FAILED", err)
	}
	return privatePath, cleanup, nil
}

func (c *WritebackClient) stageCreateOnlyInIndex(ctx context.Context, root, indexFile string, lookup changecontrol.GitCommitLookup) error {
	if err := c.ensureTreeTargetAbsent(ctx, root, lookup.ApprovedGitHead, lookup.TargetPath); err != nil {
		return err
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
		return classifyGitReadError("GIT_RESULT_BLOB_WRITE_FAILED", err)
	}
	blobID := strings.TrimSpace(string(result.Stdout))
	if !changecontrol.ValidGitObjectID(blobID) || !strings.EqualFold(blobID, lookup.ResultBlobID) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_RESULT_BLOB_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	if _, _, found, err := c.readIndexEntryOptionalFromIndex(ctx, root, lookup.TargetPath, indexFile); err != nil {
		return err
	} else if found {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_INDEX_TARGET_EXISTS", false, changecontrol.ErrGitVersionConflict)
	}
	record := fmt.Sprintf("%s %s\t%s\x00", lookup.BaseMode, strings.ToLower(lookup.ResultBlobID), lookup.TargetPath)
	_, err = c.git.runCommand(ctx, root, commandOptions{Stdin: strings.NewReader(record), IndexFile: indexFile}, "update-index", "--add", "-z", "--index-info")
	return err
}

func (c *WritebackClient) verifyCreateOnlyPrivateIndex(ctx context.Context, root, indexFile string, lookup changecontrol.GitCommitLookup) error {
	head, err := c.readRepositoryHead(ctx, root)
	if err != nil {
		return err
	}
	if !strings.EqualFold(head, lookup.ApprovedGitHead) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_HEAD_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	if _, err := c.readAttachedBranch(ctx, root); err != nil {
		return err
	}
	for _, identity := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		if _, err := c.git.output(ctx, root, "var", identity); err != nil {
			return gitWritebackError(foundation.ErrorPermissionDenied, "GIT_AUTHOR_IDENTITY_MISSING", false, errors.Join(changecontrol.ErrGitPermissionDenied, err))
		}
	}
	if err := c.ensureNoOperationInProgress(ctx, root); err != nil {
		return err
	}
	if err := c.ensureNoHiddenIndexFlags(ctx, root); err != nil {
		return err
	}
	status, err := c.readPorcelainStatus(ctx, root)
	if err != nil {
		return err
	}
	if err := validateOnlyTargetCreation(status, lookup.TargetPath); err != nil {
		return err
	}
	paths, err := c.readStagedPathsFromIndex(ctx, root, lookup.ApprovedGitHead, "A", indexFile)
	if err != nil {
		return err
	}
	if len(paths) != 1 || paths[0] != lookup.TargetPath {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_STAGED_PATH_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	mode, blobID, found, err := c.readIndexEntryOptionalFromIndex(ctx, root, lookup.TargetPath, indexFile)
	if err != nil {
		return err
	}
	if !found || mode != lookup.BaseMode || !strings.EqualFold(blobID, lookup.ResultBlobID) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_STAGED_BLOB_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	if _, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, IndexFile: indexFile}, "diff", "--cached", "--check", "--no-ext-diff", "--no-textconv", lookup.ApprovedGitHead, "--", lookup.TargetPath); err != nil {
		return gitWritebackError(foundation.ErrorInvalidInput, "GIT_STAGED_DIFF_INVALID", false, errors.Join(changecontrol.ErrGitInvalidInput, err))
	}
	stableDiff, err := c.readCachedStableDiffFromIndex(ctx, root, lookup.ApprovedGitHead, lookup.TargetPath, indexFile)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sha256Hex(stableDiff), lookup.DiffHash) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_STAGED_DIFF_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	return nil
}

func (c *WritebackClient) readCreateOnlyPrivateDiff(ctx context.Context, root string, request changecontrol.GitDiffRequest, resultBlobID, baseMode string, content []byte) ([]byte, error) {
	indexFile, cleanup, err := c.createPrivateIndex(ctx, root, request.ApprovedGitHead)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	result, err := c.git.runCommand(ctx, root, commandOptions{Stdin: bytes.NewReader(content)}, "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		return nil, classifyGitReadError("GIT_RESULT_BLOB_WRITE_FAILED", err)
	}
	if written := strings.TrimSpace(string(result.Stdout)); !strings.EqualFold(written, resultBlobID) {
		return nil, gitWritebackError(foundation.ErrorVersionConflict, "GIT_RESULT_BLOB_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	record := fmt.Sprintf("%s %s\t%s\x00", baseMode, strings.ToLower(resultBlobID), request.TargetPath)
	if _, err := c.git.runCommand(ctx, root, commandOptions{Stdin: strings.NewReader(record), IndexFile: indexFile}, "update-index", "--add", "-z", "--index-info"); err != nil {
		return nil, classifyGitReadError("GIT_PRIVATE_INDEX_STAGE_FAILED", err)
	}
	paths, err := c.readStagedPathsFromIndex(ctx, root, request.ApprovedGitHead, "A", indexFile)
	if err != nil {
		return nil, err
	}
	if len(paths) != 1 || paths[0] != request.TargetPath {
		return nil, gitWritebackError(foundation.ErrorVersionConflict, "GIT_STAGED_PATH_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	if _, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, IndexFile: indexFile}, "diff", "--cached", "--check", "--no-ext-diff", "--no-textconv", request.ApprovedGitHead, "--", request.TargetPath); err != nil {
		return nil, gitWritebackError(foundation.ErrorInvalidInput, "GIT_DIFF_WHITESPACE_INVALID", false, errors.Join(changecontrol.ErrGitInvalidInput, err))
	}
	return c.readCachedStableDiffFromIndex(ctx, root, request.ApprovedGitHead, request.TargetPath, indexFile)
}

func (c *WritebackClient) readIndexTree(ctx context.Context, root, indexFile string) (string, error) {
	result, err := c.git.runCommand(ctx, root, commandOptions{IndexFile: indexFile}, "write-tree")
	if err != nil {
		return "", err
	}
	treeID := strings.TrimSpace(string(result.Stdout))
	if !changecontrol.ValidGitObjectID(treeID) {
		return "", gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_INDEX_TREE_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	return strings.ToLower(treeID), nil
}

func (c *WritebackClient) readCommitTree(ctx context.Context, root, commitID string) (string, error) {
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true}, "rev-parse", "--verify", commitID+"^{tree}")
	if err != nil {
		return "", classifyGitReadError("GIT_COMMIT_TREE_READ_FAILED", err)
	}
	treeID := strings.TrimSpace(string(result.Stdout))
	if !changecontrol.ValidGitObjectID(treeID) {
		return "", gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMIT_TREE_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	return strings.ToLower(treeID), nil
}

func (c *WritebackClient) reconcilePublishedCreateOnlyIndex(ctx context.Context, root string, lookup changecontrol.GitCommitLookup, commitID string) error {
	head, err := c.readRepositoryHead(ctx, root)
	if err != nil {
		return err
	}
	if !strings.EqualFold(head, commitID) {
		return gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_COMMIT_POSTCONDITION_FAILED", false, changecontrol.ErrGitManualRecoveryRequired)
	}
	commitTree, err := c.readCommitTree(ctx, root, commitID)
	if err != nil {
		return err
	}
	indexTree, err := c.readIndexTree(ctx, root, "")
	if err != nil {
		return err
	}
	if strings.EqualFold(indexTree, commitTree) {
		return nil
	}
	approvedTree, err := c.readCommitTree(ctx, root, lookup.ApprovedGitHead)
	if err != nil {
		return err
	}
	if !strings.EqualFold(indexTree, approvedTree) {
		return gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_INDEX_RECOVERY_FAILED", false, changecontrol.ErrGitManualRecoveryRequired)
	}
	if _, err := c.git.runCommand(ctx, root, commandOptions{}, "read-tree", commitID); err != nil {
		return err
	}
	indexTree, err = c.readIndexTree(ctx, root, "")
	if err != nil {
		return err
	}
	if !strings.EqualFold(indexTree, commitTree) {
		return gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_INDEX_RECOVERY_FAILED", false, changecontrol.ErrGitManualRecoveryRequired)
	}
	mode, blobID, found, err := c.readIndexEntryOptional(ctx, root, lookup.TargetPath)
	if err != nil {
		return err
	}
	if !found || mode != lookup.BaseMode || !strings.EqualFold(blobID, lookup.ResultBlobID) {
		return gitWritebackError(foundation.ErrorManualRecoveryRequired, "GIT_INDEX_RECOVERY_FAILED", false, changecontrol.ErrGitManualRecoveryRequired)
	}
	return nil
}
