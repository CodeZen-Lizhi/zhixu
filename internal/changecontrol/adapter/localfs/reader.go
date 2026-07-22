// Package localfs implements the read-only Change Control target boundary.
package localfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
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
	workspaceRoot, opened, err := r.openSafeTarget(ctx, workspaceID, targetPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = workspaceRoot.Close() }()
	defer func() { _ = opened.file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, opened.file); err != nil {
		return "", &domain.TargetUnavailableError{Cause: fmt.Errorf("hash workspace target: %w", err)}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// CurrentContent 在 Workspace 边界内读取有界 UTF-8 普通文件，并从同一字节快照计算 SHA-256。
func (r *Reader) CurrentContent(ctx context.Context, workspaceID foundation.ID, targetPath string, maxBytes int64) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if maxBytes <= 0 {
		return nil, "", foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_CURRENT_CONTENT_LIMIT_INVALID", false, errors.New("current content limit must be positive"))
	}
	workspaceRoot, opened, err := r.openSafeTarget(ctx, workspaceID, targetPath)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = workspaceRoot.Close() }()
	defer func() { _ = opened.file.Close() }()
	info, err := opened.file.Stat()
	if err != nil {
		return nil, "", &domain.TargetUnavailableError{Cause: err}
	}
	if info.Size() > maxBytes {
		return nil, "", foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_CURRENT_CONTENT_TOO_LARGE", false, errors.New("workspace target exceeds current content limit"))
	}
	content, err := io.ReadAll(io.LimitReader(opened.file, maxBytes+1))
	if err != nil {
		return nil, "", &domain.TargetUnavailableError{Cause: fmt.Errorf("read workspace target: %w", err)}
	}
	if int64(len(content)) > maxBytes {
		return nil, "", foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_CURRENT_CONTENT_TOO_LARGE", false, errors.New("workspace target exceeds current content limit"))
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if !utf8.Valid(content) {
		return nil, "", foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_CURRENT_CONTENT_NOT_UTF8", false, errors.New("workspace target is not valid UTF-8"))
	}
	digest := sha256.Sum256(content)
	return content, hex.EncodeToString(digest[:]), nil
}

// openSafeTarget 统一执行 Proposal 目标策略、Workspace 根目录和文件身份检查。
// 读取路径必须与 Safe Writeback 使用同一 canonical Markdown 边界，避免只读接口绕过写入安全策略。
func (r *Reader) openSafeTarget(ctx context.Context, workspaceID foundation.ID, targetPath string) (*os.Root, openedRegular, error) {
	if err := domain.ValidateWorkspaceTarget(workspaceID, targetPath); err != nil {
		return nil, openedRegular{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_TARGET_INVALID", false, err)
	}
	workspace, err := r.workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return nil, openedRegular{}, err
	}
	if workspace.ID != workspaceID {
		return nil, openedRegular{}, foundation.NewError(
			foundation.ErrorConsistencyViolation,
			"PROPOSAL_WORKSPACE_BINDING_INVALID",
			false,
			errors.New("workspace repository returned a different workspace"),
		)
	}
	root, err := filesystem.NewRoot(workspace.RootPath)
	if err != nil {
		return nil, openedRegular{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKSPACE_ROOT_UNAVAILABLE", false, err)
	}
	workspaceRoot, err := os.OpenRoot(root.Path())
	if err != nil {
		return nil, openedRegular{}, &domain.TargetUnavailableError{Cause: err}
	}
	rootIdentity, err := directoryIdentity(workspaceRoot, ".")
	if err != nil {
		_ = workspaceRoot.Close()
		return nil, openedRegular{}, &domain.TargetUnavailableError{Cause: err}
	}
	if err := validateTargetParents(workspaceRoot, targetPath, rootIdentity.Device); err != nil {
		_ = workspaceRoot.Close()
		return nil, openedRegular{}, &domain.TargetUnavailableError{Cause: err}
	}
	opened, err := openSafeRegular(workspaceRoot, targetPath, rootIdentity.Device)
	if err != nil {
		_ = workspaceRoot.Close()
		return nil, openedRegular{}, &domain.TargetUnavailableError{Cause: err}
	}
	if err := ctx.Err(); err != nil {
		_ = opened.file.Close()
		_ = workspaceRoot.Close()
		return nil, openedRegular{}, err
	}
	return workspaceRoot, opened, nil
}
