package domain

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

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
