package runtimegrant

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// ProcessComposition owns one process resolver, gated business repository and
// optional managed runtime lease.
type ProcessComposition struct {
	Repository *workspacepostgres.Repository
	Control    *workspaceapplication.ControlService
	Resolver   *rootgrant.RootGrantResolver
	Mode       rootgrant.RuntimeGrantMode
	Grant      rootgrant.ProcessGrant
	Lease      *Lease
}

// NewProcessComposition builds a fail-closed Workspace boundary for API or Worker.
func NewProcessComposition(
	ctx context.Context,
	db workspacepostgres.DB,
	lookup rootgrant.LookupEnv,
	role workspacedomain.RuntimeRole,
) (*ProcessComposition, error) {
	resolver, mode, grant, err := rootgrant.NewRuntimeResolver(db, lookup)
	if err != nil {
		return nil, err
	}
	closeResolver := true
	defer func() {
		if closeResolver {
			_ = resolver.Close()
		}
	}()
	controlRepository, err := workspacepostgres.NewRepository(db)
	if err != nil {
		return nil, err
	}
	control, err := workspaceapplication.NewControlService(
		controlRepository, controlRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		return nil, err
	}
	repository, err := workspacepostgres.NewRepository(
		db, workspacepostgres.WithRootGrantResolver(resolver, mode == rootgrant.RuntimeGrantManaged),
	)
	if err != nil {
		return nil, err
	}
	composition := &ProcessComposition{
		Repository: repository, Control: control, Resolver: resolver, Mode: mode, Grant: grant,
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
func (composition *ProcessComposition) Errors() <-chan error {
	if composition == nil || composition.Lease == nil {
		return nil
	}
	return composition.Lease.Errors()
}

// SetQuiescenceHooks attaches the API or Worker local drain implementation.
func (composition *ProcessComposition) SetQuiescenceHooks(hooks QuiescenceHooks) error {
	if composition == nil || composition.Mode != rootgrant.RuntimeGrantManaged || composition.Lease == nil {
		return nil
	}
	return composition.Lease.SetQuiescenceHooks(hooks)
}

// Close releases runtime ownership before the opened root anchor.
func (composition *ProcessComposition) Close(ctx context.Context) error {
	if composition == nil {
		return nil
	}
	var leaseErr error
	if composition.Lease != nil {
		leaseErr = composition.Lease.Close(ctx)
	}
	resolverErr := composition.Resolver.Close()
	if leaseErr != nil {
		return leaseErr
	}
	return resolverErr
}
