// Package localfs implements the read-only Change Control target boundary.
package localfs

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// WorkspaceRepository 是读取受控 Workspace 根路径所需的最小契约。
type WorkspaceRepository interface {
	GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error)
}

// Reader 从服务端配置的 Workspace 内读取目标文件哈希。
type Reader struct{ workspaces WorkspaceRepository }

// NewReader 创建只读目标检查器。
func NewReader(workspaces WorkspaceRepository) (*Reader, error) {
	if workspaces == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "CHANGE_CONTROL_TARGET_READER_UNAVAILABLE", false, errors.New("workspace repository is required"))
	}
	return &Reader{workspaces: workspaces}, nil
}

// CurrentHash 安全解析相对路径并读取真实文件 SHA-256。
func (r *Reader) CurrentHash(ctx context.Context, workspaceID foundation.ID, targetPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	workspace, err := r.workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	root, err := filesystem.NewRoot(workspace.RootPath)
	if err != nil {
		return "", foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKSPACE_ROOT_UNAVAILABLE", false, err)
	}
	digest, err := root.Hash(targetPath)
	if err != nil {
		return "", foundation.NewError(foundation.ErrorVersionConflict, "TARGET_FILE_UNAVAILABLE", false, err)
	}
	return digest, nil
}
