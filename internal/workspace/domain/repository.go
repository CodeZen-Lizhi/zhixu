package domain

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// SourceVersionListItem 是 Inbox 使用的不可变 Source Version 摘要。
type SourceVersionListItem struct {
	ID              foundation.ID
	SourceID        foundation.ID
	WorkspaceID     foundation.ID
	Path            string
	MimeType        string
	ByteSize        int64
	CapturedAt      time.Time
	ContentHash     string
	SecurityStatus  string
	IngestionStatus string
	WorkflowStatus  string
	IndexStatus     string
}

// SourceVersionListQuery 描述 Inbox 列表的 Workspace 作用域、筛选和稳定分页边界。
type SourceVersionListQuery struct {
	WorkspaceID     foundation.ID
	SecurityStatus  string
	IngestionStatus string
	WorkflowStatus  string
	IndexStatus     string
	MimeType        string
	CursorTime      *time.Time
	CursorID        foundation.ID
	Limit           int
}

// SourceVersionListRepository 提供 Workspace 绑定的稳定 Source Version 分页。
type SourceVersionListRepository interface {
	ListSourceVersions(context.Context, SourceVersionListQuery) ([]SourceVersionListItem, bool, error)
}

// Repository persists Workspace mappings and immutable Source Versions without
// exposing database-specific types to the application layer.
type Repository interface {
	CreateWorkspace(context.Context, Workspace) (Workspace, error)
	GetWorkspaceByID(context.Context, foundation.ID) (Workspace, error)
	GetWorkspaceByRootPath(context.Context, string) (Workspace, error)
	ListWorkspaceRoots(context.Context) ([]string, error)
	RegisterSourceVersion(context.Context, SourceRegistration) (SourceRegistrationResult, error)
	RegisterSourceVersions(context.Context, []SourceRegistration) ([]SourceRegistrationResult, error)
}

// SourceMaterialRepository 按 SourceVersion 读取 Parser 所需的不可变内容引用。
// 它与 Workspace 写入 Repository 分离，避免只读消费者依赖不需要的命令能力。
type SourceMaterialRepository interface {
	GetSourceMaterial(context.Context, foundation.ID) (SourceMaterial, error)
}
