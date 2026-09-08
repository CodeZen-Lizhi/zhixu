package runtimegrant

import (
	"context"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// GORMRepositoryPort is the stable Workspace boundary exposed by the process
// composition. It deliberately excludes the concrete GORM adapter type.
type GORMRepositoryPort interface {
	workspacedomain.Repository
	workspacedomain.ActiveWorkspaceRepository
	workspacedomain.SourceMaterialRepository
	workspacedomain.SourceVersionListRepository
	workspacedomain.GitCaptureRepository
	workspaceapplication.RegistryStore
	workspaceapplication.ControlStore
	workspaceapplication.ScopedSourceWriter
}

// GORMProcessComposition owns one GORM resolver, Workspace port, and
// optional managed runtime lease.
type GORMProcessComposition struct {
	Repository GORMRepositoryPort
	Control    *workspaceapplication.ControlService
	Resolver   *rootgrant.RootGrantResolver
	Mode       rootgrant.RuntimeGrantMode
	Grant      rootgrant.ProcessGrant
	Lease      *Lease
}

// NewGORMProcessComposition assembles all Workspace dependencies from
// the same complete platform Pool.
func NewGORMProcessComposition(
	ctx context.Context,
	pool *platformpostgres.Pool,
	lookup rootgrant.LookupEnv,
	role workspacedomain.RuntimeRole,
) (*GORMProcessComposition, error) {
	resolver, mode, grant, err := rootgrant.NewGORMRuntimeResolver(pool, lookup)
	if err != nil {
		return nil, err
	}
	closeResolver := true
	defer func() {
		if closeResolver {
			_ = resolver.Close()
		}
	}()

	auditStore, err := auditpostgres.NewGORMStore(pool)
	if err != nil {
		return nil, err
	}
	controlRepository, err := workspacepostgres.NewGORMRepository(
		pool,
		workspacepostgres.WithGORMScopedAuditAppender(auditStore),
	)
	if err != nil {
		return nil, err
	}
	control, err := workspaceapplication.NewControlService(
		controlRepository, controlRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		return nil, err
	}
	repository, err := workspacepostgres.NewGORMRepository(
		pool,
		workspacepostgres.WithGORMRootGrantResolver(resolver, mode == rootgrant.RuntimeGrantManaged),
		workspacepostgres.WithGORMScopedAuditAppender(auditStore),
	)
	if err != nil {
		return nil, err
	}
	composition := &GORMProcessComposition{
		Repository: repository,
		Control:    control,
		Resolver:   resolver,
		Mode:       mode,
		Grant:      grant,
	}
	if mode == rootgrant.RuntimeGrantManaged {
		composition.Lease, err = Start(ctx, control, grant, role)
		if err != nil {
			return nil, err
		}
	}
	closeResolver = false
	return composition, nil
}

// Errors reports managed runtime authority loss.
func (composition *GORMProcessComposition) Errors() <-chan error {
	if composition == nil || composition.Lease == nil {
		return nil
	}
	return composition.Lease.Errors()
}

// SetQuiescenceHooks attaches the API or Worker local drain implementation.
func (composition *GORMProcessComposition) SetQuiescenceHooks(hooks QuiescenceHooks) error {
	if composition == nil || composition.Mode != rootgrant.RuntimeGrantManaged || composition.Lease == nil {
		return nil
	}
	return composition.Lease.SetQuiescenceHooks(hooks)
}

// Close 先释放 runtime owner；心跳未退出或持久释放失败时保留 root anchor。
func (composition *GORMProcessComposition) Close(ctx context.Context) error {
	if composition == nil {
		return nil
	}
	if composition.Lease != nil {
		if err := composition.Lease.Close(ctx); err != nil {
			return err
		}
	}
	return composition.Resolver.Close()
}
