package gitcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	changecontrol "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	defaultWritebackRecoveryTimeout = 5 * time.Second
	gitStatusRecordLimit            = 16 << 20
	gitDiffOutputLimit              = 64 << 20
)

var inProgressGitPaths = []string{
	"MERGE_HEAD",
	"CHERRY_PICK_HEAD",
	"REVERT_HEAD",
	"BISECT_LOG",
	"rebase-apply",
	"rebase-merge",
	"sequencer",
}

// WorkspaceRepository 是 Git 写回通过服务端 Workspace ID 解析根目录的最小契约。
type WorkspaceRepository interface {
	GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error)
}

// WritebackClient 实现 Change Control 使用的受限 Git 写回边界。
// Commit 与恢复方法分文件实现，但共享同一 runner、Workspace 根和恢复上限。
type WritebackClient struct {
	git             Client
	workspaces      WorkspaceRepository
	recoveryTimeout time.Duration
	historyLimit    int
}

// NewWritebackClient 创建受 Workspace Repository 约束的 Git 写回 Client。
func NewWritebackClient(git Client, workspaces WorkspaceRepository) (*WritebackClient, error) {
	if workspaces == nil {
		return nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_WORKSPACE_REPOSITORY_UNAVAILABLE", false, changecontrol.ErrGitDependencyUnavailable)
	}
	if strings.TrimSpace(git.executable) == "" {
		git = New("")
	}
	return &WritebackClient{
		git:             git,
		workspaces:      workspaces,
		recoveryTimeout: defaultWritebackRecoveryTimeout,
		historyLimit:    changecontrol.GitCommitLookupLimit,
	}, nil
}

// CaptureApprovalSnapshot 从服务端 Workspace 捕获当前 attached、全仓 clean 的审批 Git 基线。
func (c *WritebackClient) CaptureApprovalSnapshot(ctx context.Context, workspaceID foundation.ID) (changecontrol.GitSnapshot, error) {
	if !validWritebackWorkspaceID(workspaceID) {
		return changecontrol.GitSnapshot{}, gitWritebackError(foundation.ErrorInvalidInput, "GIT_INSPECT_INPUT_INVALID", false, changecontrol.ErrGitInvalidInput)
	}
	return c.inspectCurrentSnapshot(ctx, workspaceID)
}

// Inspect 验证 Workspace 当前严格快照仍与批准 HEAD 完全一致。
func (c *WritebackClient) Inspect(ctx context.Context, workspaceID foundation.ID, approvedHead string) (changecontrol.GitSnapshot, error) {
	if !validWritebackWorkspaceID(workspaceID) || !changecontrol.ValidGitObjectID(approvedHead) {
		return changecontrol.GitSnapshot{}, gitWritebackError(foundation.ErrorInvalidInput, "GIT_INSPECT_INPUT_INVALID", false, changecontrol.ErrGitInvalidInput)
	}
	snapshot, err := c.inspectCurrentSnapshot(ctx, workspaceID)
	if err != nil {
		return changecontrol.GitSnapshot{}, err
	}
	if !strings.EqualFold(snapshot.Head, approvedHead) {
		return changecontrol.GitSnapshot{}, gitWritebackError(foundation.ErrorVersionConflict, "GIT_HEAD_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	if err := changecontrol.ValidateGitSnapshotBinding(workspaceID, approvedHead, snapshot); err != nil {
		return changecontrol.GitSnapshot{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_SNAPSHOT_BINDING_INVALID", false, err)
	}
	return snapshot, nil
}

// EnsureTargetAbsentAt 证明批准 Git tree 中没有 CREATE_ONLY 目标条目。
func (c *WritebackClient) EnsureTargetAbsentAt(ctx context.Context, workspaceID foundation.ID, approvedHead, targetPath string) error {
	if !validWritebackWorkspaceID(workspaceID) || !changecontrol.ValidGitHead(approvedHead) || unsafeGitPath(targetPath) {
		return gitWritebackError(foundation.ErrorInvalidInput, "CREATE_ONLY_GIT_ABSENCE_INPUT_INVALID", false, changecontrol.ErrGitInvalidInput)
	}
	root, err := c.resolveWritebackRoot(ctx, workspaceID)
	if err != nil {
		return err
	}
	if err := c.ensureRepositoryTopLevel(ctx, root); err != nil {
		return err
	}
	if err := c.ensureTreeTargetAbsent(ctx, root, approvedHead, targetPath); err != nil {
		return err
	}
	return nil
}

func (c *WritebackClient) inspectCurrentSnapshot(ctx context.Context, workspaceID foundation.ID) (changecontrol.GitSnapshot, error) {
	root, err := c.resolveWritebackRoot(ctx, workspaceID)
	if err != nil {
		return changecontrol.GitSnapshot{}, err
	}
	if err := c.ensureRepositoryTopLevel(ctx, root); err != nil {
		return changecontrol.GitSnapshot{}, err
	}
	objectFormat, err := c.readObjectFormat(ctx, root)
	if err != nil {
		return changecontrol.GitSnapshot{}, err
	}
	head, err := c.readRepositoryHead(ctx, root)
	if err != nil {
		return changecontrol.GitSnapshot{}, err
	}
	branch, err := c.readAttachedBranch(ctx, root)
	if err != nil {
		return changecontrol.GitSnapshot{}, err
	}
	if err := c.ensureNoTrackedContentFilters(ctx, root); err != nil {
		return changecontrol.GitSnapshot{}, err
	}
	status, err := c.readPorcelainStatus(ctx, root)
	if err != nil {
		return changecontrol.GitSnapshot{}, err
	}
	if statusHasConflict(status) {
		return changecontrol.GitSnapshot{}, gitWritebackError(foundation.ErrorVersionConflict, "GIT_REPOSITORY_CONFLICTED", false, changecontrol.ErrGitVersionConflict)
	}
	if err := c.ensureNoOperationInProgress(ctx, root); err != nil {
		return changecontrol.GitSnapshot{}, err
	}
	if _, err := c.git.output(ctx, root, "var", "GIT_AUTHOR_IDENT"); err != nil {
		return changecontrol.GitSnapshot{}, gitWritebackError(foundation.ErrorPermissionDenied, "GIT_AUTHOR_IDENTITY_MISSING", false, errors.Join(changecontrol.ErrGitPermissionDenied, err))
	}
	if _, err := c.git.output(ctx, root, "var", "GIT_COMMITTER_IDENT"); err != nil {
		return changecontrol.GitSnapshot{}, gitWritebackError(foundation.ErrorPermissionDenied, "GIT_COMMITTER_IDENTITY_MISSING", false, errors.Join(changecontrol.ErrGitPermissionDenied, err))
	}
	if len(status) != 0 {
		return changecontrol.GitSnapshot{}, classifyDirtyStatus(status)
	}
	if err := c.ensureNoHiddenIndexFlags(ctx, root); err != nil {
		return changecontrol.GitSnapshot{}, err
	}

	snapshot := changecontrol.GitSnapshot{
		WorkspaceID:  workspaceID,
		Branch:       branch,
		Head:         head,
		ObjectFormat: objectFormat,
		Clean:        true,
	}
	if err := changecontrol.ValidateGitSnapshotBinding(workspaceID, head, snapshot); err != nil {
		return changecontrol.GitSnapshot{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_SNAPSHOT_BINDING_INVALID", false, err)
	}
	return snapshot, nil
}

// DiffApproved 验证文件 CAS 后全仓只有目标修改，并绑定原始字节、Git blob 与稳定 Diff。
func (c *WritebackClient) DiffApproved(ctx context.Context, request changecontrol.GitDiffRequest) (changecontrol.GitDiff, error) {
	if err := changecontrol.ValidateGitDiffRequest(request); err != nil {
		return changecontrol.GitDiff{}, gitWritebackError(foundation.ErrorInvalidInput, "GIT_DIFF_INPUT_INVALID", false, err)
	}
	root, err := c.resolveWritebackRoot(ctx, request.WorkspaceID)
	if err != nil {
		return changecontrol.GitDiff{}, err
	}
	if err := c.ensureRepositoryTopLevel(ctx, root); err != nil {
		return changecontrol.GitDiff{}, err
	}
	head, err := c.readRepositoryHead(ctx, root)
	if err != nil {
		return changecontrol.GitDiff{}, err
	}
	if !strings.EqualFold(head, request.ApprovedGitHead) {
		return changecontrol.GitDiff{}, gitWritebackError(foundation.ErrorVersionConflict, "GIT_HEAD_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	if _, err := c.readAttachedBranch(ctx, root); err != nil {
		return changecontrol.GitDiff{}, err
	}
	if err := c.ensureNoOperationInProgress(ctx, root); err != nil {
		return changecontrol.GitDiff{}, err
	}
	if err := c.ensureNoHiddenIndexFlags(ctx, root); err != nil {
		return changecontrol.GitDiff{}, err
	}
	if err := c.ensureFilterUnspecified(ctx, root, request.TargetPath); err != nil {
		return changecontrol.GitDiff{}, err
	}
	if err := c.ensureNoTrackedContentFilters(ctx, root); err != nil {
		return changecontrol.GitDiff{}, err
	}
	status, err := c.readPorcelainStatus(ctx, root)
	if err != nil {
		return changecontrol.GitDiff{}, err
	}
	targetMode := changecontrol.NormalizeTargetMode(request.TargetMode)
	baseMode, baseBlobID := changecontrol.GitFileModeRegular, ""
	if targetMode == changecontrol.TargetModeCreateOnly {
		if err := c.ensureTreeTargetAbsent(ctx, root, request.ApprovedGitHead, request.TargetPath); err != nil {
			return changecontrol.GitDiff{}, err
		}
		if err := validateOnlyTargetCreation(status, request.TargetPath); err != nil {
			return changecontrol.GitDiff{}, err
		}
	} else {
		baseMode, baseBlobID, err = c.readTrackedBlob(ctx, root, request.ApprovedGitHead, request.TargetPath)
		if err != nil {
			return changecontrol.GitDiff{}, err
		}
		if err := validateOnlyTargetModification(status, request.TargetPath, baseMode); err != nil {
			return changecontrol.GitDiff{}, err
		}
	}
	content, err := readSafeWorkspaceFile(root, request.TargetPath)
	if err != nil {
		return changecontrol.GitDiff{}, err
	}
	resultHash := sha256Hex(content)
	if !strings.EqualFold(resultHash, request.ResultHash) {
		return changecontrol.GitDiff{}, gitWritebackError(foundation.ErrorVersionConflict, "GIT_RESULT_HASH_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	if err := c.ensureFilterUnspecified(ctx, root, request.TargetPath); err != nil {
		return changecontrol.GitDiff{}, err
	}
	resultBlobID, err := c.hashApprovedContent(ctx, root, content)
	if err != nil {
		return changecontrol.GitDiff{}, err
	}
	var stableDiff []byte
	if targetMode == changecontrol.TargetModeCreateOnly {
		stableDiff, err = c.readCreateOnlyPrivateDiff(ctx, root, request, resultBlobID, baseMode, content)
	} else {
		if _, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true}, "diff", "--check", "--no-ext-diff", "--no-textconv", request.ApprovedGitHead, "--", request.TargetPath); err != nil {
			if isExitCode(err, 2) {
				return changecontrol.GitDiff{}, gitWritebackError(foundation.ErrorInvalidInput, "GIT_DIFF_WHITESPACE_INVALID", false, errors.Join(changecontrol.ErrGitInvalidInput, err))
			}
			return changecontrol.GitDiff{}, classifyGitReadError("GIT_DIFF_CHECK_FAILED", err)
		}
		stableDiff, err = c.readStableDiff(ctx, root, request.ApprovedGitHead, "", request.TargetPath)
	}
	if err != nil {
		return changecontrol.GitDiff{}, err
	}
	if len(stableDiff) == 0 {
		return changecontrol.GitDiff{}, gitWritebackError(foundation.ErrorVersionConflict, "GIT_TARGET_DIFF_MISSING", false, changecontrol.ErrGitVersionConflict)
	}
	if err := validateStableDiffForTargetMode(stableDiff, targetMode, baseBlobID, resultBlobID, baseMode); err != nil {
		return changecontrol.GitDiff{}, err
	}
	if err := c.ensureFilterUnspecified(ctx, root, request.TargetPath); err != nil {
		return changecontrol.GitDiff{}, err
	}
	finalContent, err := readSafeWorkspaceFile(root, request.TargetPath)
	if err != nil {
		return changecontrol.GitDiff{}, err
	}
	if !bytes.Equal(finalContent, content) {
		return changecontrol.GitDiff{}, gitWritebackError(foundation.ErrorVersionConflict, "GIT_TARGET_CHANGED_DURING_DIFF", false, changecontrol.ErrGitVersionConflict)
	}
	finalBlobID, err := c.hashApprovedContent(ctx, root, finalContent)
	if err != nil {
		return changecontrol.GitDiff{}, err
	}
	if !strings.EqualFold(finalBlobID, resultBlobID) {
		return changecontrol.GitDiff{}, gitWritebackError(foundation.ErrorVersionConflict, "GIT_TARGET_CHANGED_DURING_DIFF", false, changecontrol.ErrGitVersionConflict)
	}
	diff := changecontrol.GitDiff{
		WorkspaceID:     request.WorkspaceID,
		TargetPath:      request.TargetPath,
		TargetMode:      targetMode,
		ApprovedGitHead: request.ApprovedGitHead,
		ResultHash:      resultHash,
		DiffHash:        sha256Hex(stableDiff),
		BaseBlobID:      baseBlobID,
		ResultBlobID:    resultBlobID,
		BaseMode:        baseMode,
	}
	if err := changecontrol.ValidateGitDiffBinding(request, diff); err != nil {
		return changecontrol.GitDiff{}, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_DIFF_BINDING_INVALID", false, err)
	}
	return diff, nil
}

func (c *WritebackClient) resolveWritebackRoot(ctx context.Context, workspaceID foundation.ID) (string, error) {
	if !validWritebackWorkspaceID(workspaceID) {
		return "", gitWritebackError(foundation.ErrorInvalidInput, "GIT_WORKSPACE_ID_INVALID", false, changecontrol.ErrGitInvalidInput)
	}
	if err := contextError(ctx); err != nil {
		return "", classifyGitReadError("GIT_WORKSPACE_RESOLVE_FAILED", err)
	}
	workspace, err := c.workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	if workspace.ID != workspaceID {
		return "", gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_WORKSPACE_BINDING_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	root := strings.TrimSpace(workspace.RootPath)
	if root == "" {
		return "", gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_WORKSPACE_ROOT_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_WORKSPACE_ROOT_UNAVAILABLE", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_WORKSPACE_ROOT_UNAVAILABLE", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_WORKSPACE_ROOT_UNAVAILABLE", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	return filepath.Clean(root), nil
}

func (c *WritebackClient) ensureRepositoryTopLevel(ctx context.Context, root string) error {
	topLevel, err := c.git.output(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		if isMissingRepository(err) {
			return gitWritebackError(foundation.ErrorNotFound, "GIT_REPOSITORY_NOT_FOUND", false, errors.Join(changecontrol.ErrGitNotFound, err))
		}
		return classifyGitReadError("GIT_REPOSITORY_INSPECT_FAILED", err)
	}
	canonical, err := filepath.Abs(topLevel)
	if err == nil {
		canonical, err = filepath.EvalSymlinks(canonical)
	}
	if err != nil {
		return gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_REPOSITORY_ROOT_UNAVAILABLE", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	if filepath.Clean(canonical) != filepath.Clean(root) {
		return gitWritebackError(foundation.ErrorPermissionDenied, "GIT_REPOSITORY_ROOT_MISMATCH", false, changecontrol.ErrGitPermissionDenied)
	}
	graftsPath, err := c.git.output(ctx, root, "rev-parse", "--git-path", "info/grafts")
	if err != nil {
		return classifyGitReadError("GIT_OBJECT_OVERRIDES_INSPECT_FAILED", err)
	}
	if !filepath.IsAbs(graftsPath) {
		graftsPath = filepath.Join(root, graftsPath)
	}
	if _, err := os.Lstat(graftsPath); err == nil {
		return gitWritebackError(foundation.ErrorPermissionDenied, "GIT_OBJECT_OVERRIDES_UNSAFE", false, changecontrol.ErrGitPermissionDenied)
	} else if !errors.Is(err, os.ErrNotExist) {
		return gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_OBJECT_OVERRIDES_INSPECT_FAILED", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	return nil
}

func (c *WritebackClient) readObjectFormat(ctx context.Context, root string) (changecontrol.GitObjectFormat, error) {
	value, err := c.git.output(ctx, root, "rev-parse", "--show-object-format")
	if err != nil {
		return "", classifyGitReadError("GIT_OBJECT_FORMAT_FAILED", err)
	}
	format := changecontrol.GitObjectFormat(value)
	if format != changecontrol.GitObjectFormatSHA1 && format != changecontrol.GitObjectFormatSHA256 {
		return "", gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_OBJECT_FORMAT_UNSUPPORTED", false, changecontrol.ErrGitConsistencyViolation)
	}
	return format, nil
}

func (c *WritebackClient) readRepositoryHead(ctx context.Context, root string) (string, error) {
	head, err := c.git.output(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		if isUnknownRevision(err) {
			return "", gitWritebackError(foundation.ErrorVersionConflict, "GIT_REPOSITORY_UNBORN", false, errors.Join(changecontrol.ErrGitVersionConflict, err))
		}
		return "", classifyGitReadError("GIT_HEAD_INSPECT_FAILED", err)
	}
	if !changecontrol.ValidGitObjectID(head) {
		return "", gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_HEAD_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	return strings.ToLower(head), nil
}

func (c *WritebackClient) readAttachedBranch(ctx context.Context, root string) (string, error) {
	branch, err := c.git.output(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if isExitCode(err, 1) {
			return "", gitWritebackError(foundation.ErrorVersionConflict, "GIT_REPOSITORY_DETACHED", false, changecontrol.ErrGitVersionConflict)
		}
		return "", classifyGitReadError("GIT_BRANCH_INSPECT_FAILED", err)
	}
	branch = strings.TrimSpace(branch)
	if branch == "" || strings.ContainsFunc(branch, unicode.IsControl) {
		return "", gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_BRANCH_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	return branch, nil
}

func (c *WritebackClient) ensureNoOperationInProgress(ctx context.Context, root string) error {
	for _, name := range inProgressGitPaths {
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

func (c *WritebackClient) readPorcelainStatus(ctx context.Context, root string) ([]byte, error) {
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: gitStatusRecordLimit}, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		return nil, classifyGitReadError("GIT_STATUS_INSPECT_FAILED", err)
	}
	return result.Stdout, nil
}

func (c *WritebackClient) ensureNoHiddenIndexFlags(ctx context.Context, root string) error {
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: gitStatusRecordLimit}, "ls-files", "-v", "-z")
	if err != nil {
		return classifyGitReadError("GIT_INDEX_FLAGS_INSPECT_FAILED", err)
	}
	for _, entry := range splitNUL(result.Stdout) {
		if len(entry) < 3 || entry[1] != ' ' {
			return gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_INDEX_FLAGS_INVALID", false, changecontrol.ErrGitConsistencyViolation)
		}
		tag := entry[0]
		if tag == 'S' || tag >= 'a' && tag <= 'z' {
			return gitWritebackError(foundation.ErrorPermissionDenied, "GIT_INDEX_FLAGS_UNSAFE", false, changecontrol.ErrGitPermissionDenied)
		}
	}
	return nil
}

func (c *WritebackClient) readTrackedBlob(ctx context.Context, root, commit, target string) (string, string, error) {
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true}, "ls-tree", "-z", commit, "--", target)
	if err != nil {
		return "", "", classifyGitReadError("GIT_TARGET_OBJECT_INSPECT_FAILED", err)
	}
	entries := splitNUL(result.Stdout)
	if len(entries) != 1 {
		return "", "", gitWritebackError(foundation.ErrorPermissionDenied, "GIT_TARGET_NOT_TRACKED", false, changecontrol.ErrGitPermissionDenied)
	}
	metadata, pathValue, found := strings.Cut(string(entries[0]), "\t")
	fields := strings.Fields(metadata)
	if !found || len(fields) != 3 || pathValue != target || fields[1] != "blob" || !changecontrol.ValidGitFileMode(fields[0]) || !changecontrol.ValidGitObjectID(fields[2]) {
		return "", "", gitWritebackError(foundation.ErrorPermissionDenied, "GIT_TARGET_OBJECT_UNSAFE", false, changecontrol.ErrGitPermissionDenied)
	}
	return fields[0], strings.ToLower(fields[2]), nil
}

func (c *WritebackClient) ensureTreeTargetAbsent(ctx context.Context, root, commit, target string) error {
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true}, "ls-tree", "-z", commit, "--", target)
	if err != nil {
		return classifyGitReadError("CREATE_ONLY_GIT_TREE_INSPECT_FAILED", err)
	}
	entries := splitNUL(result.Stdout)
	if len(entries) == 0 {
		return nil
	}
	return gitWritebackError(foundation.ErrorVersionConflict, "CREATE_ONLY_GIT_TARGET_EXISTS", false, changecontrol.ErrGitVersionConflict)
}

func (c *WritebackClient) ensureFilterUnspecified(ctx context.Context, root, target string) error {
	attributes := []string{"filter", "ident", "text", "eol", "working-tree-encoding", "diff"}
	args := append([]string{"check-attr", "-z"}, attributes...)
	args = append(args, "--", target)
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true}, args...)
	if err != nil {
		return classifyGitReadError("GIT_TARGET_ATTRIBUTE_INSPECT_FAILED", err)
	}
	parts := splitNUL(result.Stdout)
	if len(parts) != len(attributes)*3 {
		return gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_TARGET_ATTRIBUTE_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	for index, attribute := range attributes {
		offset := index * 3
		if string(parts[offset]) != target || string(parts[offset+1]) != attribute {
			return gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_TARGET_ATTRIBUTE_INVALID", false, changecontrol.ErrGitConsistencyViolation)
		}
		value := string(parts[offset+2])
		if value == "unspecified" || value == "unset" && attribute != "diff" {
			continue
		}
		if attribute == "filter" {
			return gitWritebackError(foundation.ErrorPermissionDenied, "GIT_TARGET_FILTER_UNSAFE", false, changecontrol.ErrGitPermissionDenied)
		}
		return gitWritebackError(foundation.ErrorPermissionDenied, "GIT_TARGET_ATTRIBUTES_TRANSFORM_CONTENT", false, changecontrol.ErrGitPermissionDenied)
	}
	return nil
}

func (c *WritebackClient) ensureNoTrackedContentFilters(ctx context.Context, root string) error {
	unsafe, err := c.git.hasTrackedContentFilter(ctx, root)
	if err != nil {
		return classifyGitReadError("GIT_REPOSITORY_FILTER_INSPECT_FAILED", err)
	}
	if unsafe {
		return gitWritebackError(foundation.ErrorPermissionDenied, "GIT_REPOSITORY_FILTER_UNSAFE", false, changecontrol.ErrGitPermissionDenied)
	}
	return nil
}

func (c Client) hasTrackedContentFilter(ctx context.Context, root string) (bool, error) {
	paths, err := c.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: gitStatusRecordLimit}, "ls-files", "-z", "--cached")
	if err != nil {
		return false, err
	}
	if len(paths.Stdout) == 0 {
		return false, nil
	}
	attributes, err := c.runCommand(ctx, root, commandOptions{ReadOnly: true, Stdin: bytes.NewReader(paths.Stdout), MaxOutputBytes: gitDiffOutputLimit}, "check-attr", "-z", "--stdin", "filter")
	if err != nil {
		return false, err
	}
	parts := splitNUL(attributes.Stdout)
	if len(parts)%3 != 0 {
		return false, errors.New("git filter attribute output is invalid")
	}
	for index := 0; index < len(parts); index += 3 {
		if string(parts[index+1]) != "filter" {
			return false, errors.New("git filter attribute output is invalid")
		}
		value := string(parts[index+2])
		if value != "unspecified" && value != "unset" {
			return true, nil
		}
	}
	return false, nil
}

func (c *WritebackClient) hashApprovedContent(ctx context.Context, root string, content []byte) (string, error) {
	raw, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, Stdin: bytes.NewReader(content)}, "hash-object", "--no-filters", "--stdin")
	if err != nil {
		return "", classifyGitReadError("GIT_RAW_BLOB_HASH_FAILED", err)
	}
	rawID := strings.TrimSpace(string(raw.Stdout))
	if !changecontrol.ValidGitObjectID(rawID) {
		return "", gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_RESULT_BLOB_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	return strings.ToLower(rawID), nil
}

func (c *WritebackClient) readStableDiff(ctx context.Context, root, base, tip, target string) ([]byte, error) {
	if !changecontrol.ValidGitObjectID(base) || tip != "" && !changecontrol.ValidGitObjectID(tip) || target != "" && unsafeGitPath(target) {
		return nil, gitWritebackError(foundation.ErrorInvalidInput, "GIT_DIFF_RANGE_INVALID", false, changecontrol.ErrGitInvalidInput)
	}
	args := []string{
		"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--full-index", "--binary", "--no-renames",
		"--diff-algorithm=myers", "--no-indent-heuristic", "--inter-hunk-context=0",
		"--src-prefix=a/", "--dst-prefix=b/", "--unified=3", base,
	}
	if tip != "" {
		args = append(args, tip)
	}
	args = append(args, "--")
	if target != "" {
		args = append(args, target)
	}
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: gitDiffOutputLimit}, args...)
	if err != nil {
		return nil, classifyGitReadError("GIT_STABLE_DIFF_FAILED", err)
	}
	return result.Stdout, nil
}

func (c *WritebackClient) readBlobSHA256(ctx context.Context, root, commit, target string) (string, error) {
	if !changecontrol.ValidGitObjectID(commit) || unsafeGitPath(target) {
		return "", gitWritebackError(foundation.ErrorInvalidInput, "GIT_BLOB_READ_INPUT_INVALID", false, changecontrol.ErrGitInvalidInput)
	}
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: changecontrol.MaxWritebackContentBytes + 1}, "cat-file", "blob", commit+":"+target)
	if err != nil {
		return "", classifyGitReadError("GIT_BLOB_READ_FAILED", err)
	}
	if len(result.Stdout) > changecontrol.MaxWritebackContentBytes {
		return "", gitWritebackError(foundation.ErrorPermissionDenied, "GIT_BLOB_TOO_LARGE", false, changecontrol.ErrGitPermissionDenied)
	}
	return sha256Hex(result.Stdout), nil
}

func (c *WritebackClient) readChangedPaths(ctx context.Context, root, base, tip string) ([]string, error) {
	if !changecontrol.ValidGitObjectID(base) || !changecontrol.ValidGitObjectID(tip) {
		return nil, gitWritebackError(foundation.ErrorInvalidInput, "GIT_CHANGED_PATHS_RANGE_INVALID", false, changecontrol.ErrGitInvalidInput)
	}
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: gitStatusRecordLimit}, "diff", "--name-status", "-z", "--no-renames", base, tip, "--")
	if err != nil {
		return nil, classifyGitReadError("GIT_CHANGED_PATHS_FAILED", err)
	}
	parts := splitNUL(result.Stdout)
	if len(parts)%2 != 0 {
		return nil, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_CHANGED_PATHS_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	paths := make([]string, 0, len(parts)/2)
	for i := 0; i < len(parts); i += 2 {
		status, pathValue := string(parts[i]), string(parts[i+1])
		if len(status) != 1 || (status != "M" && status != "A" && status != "D") || unsafeGitPath(pathValue) {
			return nil, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_CHANGED_PATHS_INVALID", false, changecontrol.ErrGitConsistencyViolation)
		}
		paths = append(paths, pathValue)
	}
	return paths, nil
}

func readSafeWorkspaceFile(root, target string) ([]byte, error) {
	if unsafeGitPath(target) {
		return nil, gitWritebackError(foundation.ErrorInvalidInput, "GIT_TARGET_PATH_INVALID", false, changecontrol.ErrGitInvalidInput)
	}
	current := root
	parts := strings.Split(filepath.FromSlash(target), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_TARGET_PARENT_UNAVAILABLE", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, gitWritebackError(foundation.ErrorPermissionDenied, "GIT_TARGET_PARENT_UNSAFE", false, changecontrol.ErrGitPermissionDenied)
		}
	}
	pathValue := filepath.Join(root, filepath.FromSlash(target))
	before, err := os.Lstat(pathValue)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, gitWritebackError(foundation.ErrorVersionConflict, "GIT_TARGET_CHANGED_DURING_DIFF", false, errors.Join(changecontrol.ErrGitVersionConflict, err))
		}
		return nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_TARGET_UNAVAILABLE", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, gitWritebackError(foundation.ErrorPermissionDenied, "GIT_TARGET_UNSAFE", false, changecontrol.ErrGitPermissionDenied)
	}
	if before.Size() > changecontrol.MaxWritebackContentBytes {
		return nil, gitWritebackError(foundation.ErrorInvalidInput, "GIT_TARGET_TOO_LARGE", false, changecontrol.ErrGitInvalidInput)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_WORKSPACE_ROOT_OPEN_FAILED", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	defer rootHandle.Close()
	file, err := rootHandle.OpenFile(filepath.ToSlash(target), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, gitWritebackError(foundation.ErrorPermissionDenied, "GIT_TARGET_UNSAFE", false, errors.Join(changecontrol.ErrGitPermissionDenied, err))
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, gitWritebackError(foundation.ErrorVersionConflict, "GIT_TARGET_CHANGED_DURING_DIFF", false, errors.Join(changecontrol.ErrGitVersionConflict, err))
		}
		return nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_TARGET_OPEN_FAILED", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_TARGET_STAT_FAILED", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() != before.Size() {
		return nil, gitWritebackError(foundation.ErrorVersionConflict, "GIT_TARGET_IDENTITY_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	content, err := io.ReadAll(io.LimitReader(file, changecontrol.MaxWritebackContentBytes+1))
	if err != nil {
		return nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_TARGET_READ_FAILED", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	if len(content) > changecontrol.MaxWritebackContentBytes {
		return nil, gitWritebackError(foundation.ErrorInvalidInput, "GIT_TARGET_TOO_LARGE", false, changecontrol.ErrGitInvalidInput)
	}
	final, err := file.Stat()
	if err != nil {
		return nil, gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_TARGET_FINAL_STAT_FAILED", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	if !final.Mode().IsRegular() || !os.SameFile(before, final) || final.Size() != int64(len(content)) {
		return nil, gitWritebackError(foundation.ErrorVersionConflict, "GIT_TARGET_IDENTITY_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	return content, nil
}

func validateOnlyTargetModification(status []byte, target, baseMode string) error {
	records := splitNUL(status)
	if len(records) != 1 {
		return classifyDirtyStatus(status)
	}
	record := string(records[0])
	fields := strings.SplitN(record, " ", 9)
	if len(fields) != 9 || fields[0] != "1" || fields[1] != ".M" || fields[8] != target {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_TARGET_DIFF_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	if fields[3] != baseMode || fields[4] != baseMode || fields[5] != baseMode {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_TARGET_MODE_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	return nil
}

func validateOnlyTargetCreation(status []byte, target string) error {
	records := splitNUL(status)
	if len(records) != 1 || string(records[0]) != "? "+target {
		return classifyDirtyStatus(status)
	}
	return nil
}

func validateStableDiffForTargetMode(diff []byte, targetMode changecontrol.TargetMode, baseBlobID, resultBlobID, baseMode string) error {
	if changecontrol.NormalizeTargetMode(targetMode) != changecontrol.TargetModeCreateOnly {
		return validateStableDiffBlobBinding(diff, baseBlobID, resultBlobID, baseMode)
	}
	var indexLine string
	var newFileMode string
	for _, line := range bytes.Split(diff, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("index ")) {
			if indexLine != "" {
				return gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_STABLE_DIFF_INDEX_INVALID", false, changecontrol.ErrGitConsistencyViolation)
			}
			indexLine = string(line)
		}
		if bytes.HasPrefix(line, []byte("new file mode ")) {
			newFileMode = strings.TrimPrefix(string(line), "new file mode ")
		}
	}
	fields := strings.Fields(indexLine)
	if len(fields) != 2 || fields[0] != "index" || newFileMode != baseMode {
		return gitWritebackError(foundation.ErrorConsistencyViolation, "CREATE_ONLY_STABLE_DIFF_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	base, result, found := strings.Cut(fields[1], "..")
	if !found || len(base) != len(resultBlobID) || strings.Trim(base, "0") != "" || !strings.EqualFold(result, resultBlobID) {
		return gitWritebackError(foundation.ErrorVersionConflict, "CREATE_ONLY_STABLE_DIFF_BLOB_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	return nil
}

func validateStableDiffBlobBinding(diff []byte, baseBlobID, resultBlobID, baseMode string) error {
	var indexLine string
	for _, line := range bytes.Split(diff, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("index ")) {
			if indexLine != "" {
				return gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_STABLE_DIFF_INDEX_INVALID", false, changecontrol.ErrGitConsistencyViolation)
			}
			indexLine = string(line)
		}
	}
	fields := strings.Fields(indexLine)
	if len(fields) != 3 || fields[0] != "index" || fields[2] != baseMode {
		return gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_STABLE_DIFF_INDEX_INVALID", false, changecontrol.ErrGitConsistencyViolation)
	}
	base, result, found := strings.Cut(fields[1], "..")
	if !found || !strings.EqualFold(base, baseBlobID) || !strings.EqualFold(result, resultBlobID) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_STABLE_DIFF_BLOB_CONFLICT", false, changecontrol.ErrGitVersionConflict)
	}
	return nil
}

func classifyDirtyStatus(status []byte) error {
	code := "GIT_REPOSITORY_DIRTY"
	records := splitNUL(status)
	for index := 0; index < len(records); index++ {
		record := records[index]
		if len(record) == 0 {
			continue
		}
		switch record[0] {
		case '?':
			code = "GIT_REPOSITORY_UNTRACKED"
		case 'u':
			return gitWritebackError(foundation.ErrorVersionConflict, "GIT_REPOSITORY_CONFLICTED", false, changecontrol.ErrGitVersionConflict)
		case '1', '2':
			fields := strings.SplitN(string(record), " ", 3)
			if len(fields) >= 2 && len(fields[1]) == 2 && fields[1][0] != '.' {
				return gitWritebackError(foundation.ErrorVersionConflict, "GIT_REPOSITORY_STAGED", false, changecontrol.ErrGitVersionConflict)
			}
			if record[0] == '2' && index+1 < len(records) {
				index++
			}
		}
	}
	return gitWritebackError(foundation.ErrorVersionConflict, code, false, changecontrol.ErrGitVersionConflict)
}

func statusHasConflict(status []byte) bool {
	records := splitNUL(status)
	for index := 0; index < len(records); index++ {
		record := records[index]
		if len(record) > 0 && record[0] == 'u' {
			return true
		}
		if len(record) > 0 && record[0] == '2' && index+1 < len(records) {
			index++
		}
	}
	return false
}

func splitNUL(value []byte) [][]byte {
	if len(value) == 0 {
		return nil
	}
	parts := strings.Split(string(value), "\x00")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	result := make([][]byte, len(parts))
	for i := range parts {
		result[i] = []byte(parts[i])
	}
	return result
}

func unsafeGitPath(value string) bool {
	if value == "" || filepath.IsAbs(value) || filepath.Clean(filepath.FromSlash(value)) != filepath.FromSlash(value) {
		return true
	}
	first, _, _ := strings.Cut(value, "/")
	return first == ".." || strings.ContainsRune(value, '\x00') || strings.ContainsFunc(value, unicode.IsControl)
}

func validWritebackWorkspaceID(value foundation.ID) bool {
	text := string(value)
	return text != "" && len(text) <= 128 && strings.TrimSpace(text) == text && !strings.ContainsFunc(text, unicode.IsSpace) && !strings.ContainsFunc(text, unicode.IsControl)
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func gitWritebackError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}

func classifyGitReadError(code string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return gitWritebackError(foundation.ErrorRetryableFailure, commandContextCode(code), true, errors.Join(changecontrol.ErrGitRetryableFailure, err))
	}
	if errors.Is(err, context.Canceled) {
		return gitWritebackError(foundation.ErrorNonRetryableFailure, "GIT_COMMAND_CANCELLED", false, err)
	}
	var commandErr *commandError
	if errors.As(err, &commandErr) && commandErr.OutputLimitExceeded() {
		return gitWritebackError(foundation.ErrorPermissionDenied, "GIT_COMMAND_OUTPUT_TOO_LARGE", false, errors.Join(changecontrol.ErrGitPermissionDenied, err))
	}
	var executableError *exec.Error
	if errors.As(err, &executableError) || errors.Is(err, os.ErrNotExist) {
		return gitWritebackError(foundation.ErrorDependencyUnavailable, "GIT_COMMAND_UNAVAILABLE", false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
	}
	return gitWritebackError(foundation.ErrorDependencyUnavailable, code, false, errors.Join(changecontrol.ErrGitDependencyUnavailable, err))
}
