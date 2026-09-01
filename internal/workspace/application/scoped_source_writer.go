package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// ScopedSourceWriter persists Workspace-owned Source facts in a caller-owned
// transaction without exposing a database driver through the application port.
type ScopedSourceWriter interface {
	RegisterSourceScoped(context.Context, foundation.TransactionScope, domain.Source) (domain.Source, error)
	RegisterSourceVersionScoped(context.Context, foundation.TransactionScope, domain.SourceRegistration) (domain.SourceRegistrationResult, error)
}
