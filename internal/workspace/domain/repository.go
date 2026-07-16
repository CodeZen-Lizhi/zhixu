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
