package gitcli

import (
	"context"
	"errors"
	"strings"

	changecontrol "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// ReadCommittedBlob 从服务端 Workspace 的指定 Commit 读取确切 tracked Blob，不观察工作树。
func (c *WritebackClient) ReadCommittedBlob(ctx context.Context, workspaceID foundation.ID, commit, relativePath string) (workspacedomain.CommittedBlob, error) {
	if c == nil || !validWritebackWorkspaceID(workspaceID) || !canonicalGitObjectID(commit) || unsafeGitPath(relativePath) || strings.Contains(relativePath, "\\") {
		return workspacedomain.CommittedBlob{}, gitWritebackError(foundation.ErrorInvalidInput, "GIT_COMMITTED_BLOB_INPUT_INVALID", false, changecontrol.ErrGitInvalidInput)
	}
	root, err := c.resolveWritebackRoot(ctx, workspaceID)
	if err != nil {
		return workspacedomain.CommittedBlob{}, err
	}
	if err := c.ensureRepositoryTopLevel(ctx, root); err != nil {
		return workspacedomain.CommittedBlob{}, err
	}
	format, err := c.readObjectFormat(ctx, root)
	if err != nil {
		return workspacedomain.CommittedBlob{}, err
	}
	if (format == changecontrol.GitObjectFormatSHA1 && len(commit) != 40) ||
		(format == changecontrol.GitObjectFormatSHA256 && len(commit) != 64) {
		return workspacedomain.CommittedBlob{}, gitWritebackError(foundation.ErrorInvalidInput, "GIT_COMMITTED_BLOB_OBJECT_FORMAT_INVALID", false, changecontrol.ErrGitInvalidInput)
	}
	if _, err := c.readCommitObject(ctx, root, commit); err != nil {
		return workspacedomain.CommittedBlob{}, err
	}
	if _, _, err := c.readTrackedBlob(ctx, root, commit, relativePath); err != nil {
		return workspacedomain.CommittedBlob{}, err
	}
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: int(workspacedomain.MaxCommittedSourceBytes + 1)}, "cat-file", "blob", commit+":"+relativePath)
	if err != nil {
		var commandErr *commandError
		if errors.As(err, &commandErr) && commandErr.OutputLimitExceeded() {
			return workspacedomain.CommittedBlob{}, gitWritebackError(foundation.ErrorPermissionDenied, "GIT_BLOB_TOO_LARGE", false, errors.New("committed blob exceeds the supported size"))
		}
		return workspacedomain.CommittedBlob{}, classifyGitReadError("GIT_COMMITTED_BLOB_READ_FAILED", err)
	}
	if int64(len(result.Stdout)) > workspacedomain.MaxCommittedSourceBytes {
		return workspacedomain.CommittedBlob{}, gitWritebackError(foundation.ErrorPermissionDenied, "GIT_BLOB_TOO_LARGE", false, errors.New("committed blob exceeds the supported size"))
	}
	return workspacedomain.CommittedBlob{
		WorkspaceID: workspaceID, Commit: commit, RelativePath: relativePath,
		Bytes: append([]byte(nil), result.Stdout...),
	}, nil
}

func canonicalGitObjectID(value string) bool {
	return (len(value) == 40 || len(value) == 64) && strings.ToLower(value) == value && changecontrol.ValidGitObjectID(value)
}

var _ workspacedomain.CommittedBlobReader = (*WritebackClient)(nil)
