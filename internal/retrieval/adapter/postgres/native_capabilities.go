package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These interfaces are the complete pgx allowlist for the staged Retrieval
// repository. Each operation depends on either COPY or physical session
// identity and therefore cannot safely run through database/sql.
type retrievalManifestCopier interface {
	BeginIndex(context.Context, domain.IndexBuild) (domain.IndexVersionResult, error)
}

type retrievalSnapshotBuilder interface {
	BeginWorkspaceSnapshot(context.Context, domain.WorkspaceSnapshotCommand) (domain.WorkspaceSnapshotResult, error)
}

type retrievalSourceRefreshLocker interface {
	AcquireSourceRefresh(context.Context, foundation.ID) (application.SourceRefreshLease, error)
}

type retrievalNativeCapabilities struct {
	manifest retrievalManifestCopier
	snapshot retrievalSnapshotBuilder
	refresh  retrievalSourceRefreshLocker
}

type retrievalPGXNativeCapabilities struct {
	database *pgxpool.Pool
}

var (
	_ retrievalManifestCopier      = (*retrievalPGXNativeCapabilities)(nil)
	_ retrievalSnapshotBuilder     = (*retrievalPGXNativeCapabilities)(nil)
	_ retrievalSourceRefreshLocker = (*retrievalPGXNativeCapabilities)(nil)
)

func newRetrievalNativeCapabilities(pool *platformpostgres.Pool) (retrievalNativeCapabilities, error) {
	if pool == nil || pool.DB() == nil {
		return retrievalNativeCapabilities{}, dependency("RETRIEVAL_NATIVE_DATABASE_UNAVAILABLE", errors.New("PostgreSQL pool is not initialized"))
	}
	native := &retrievalPGXNativeCapabilities{database: pool.DB()}
	return retrievalNativeCapabilities{manifest: native, snapshot: native, refresh: native}, nil
}

func (native retrievalNativeCapabilities) BeginIndex(
	ctx context.Context,
	build domain.IndexBuild,
) (domain.IndexVersionResult, error) {
	if native.manifest == nil {
		return domain.IndexVersionResult{}, dependency("RETRIEVAL_NATIVE_DATABASE_UNAVAILABLE", errors.New("manifest copier is unavailable"))
	}
	return native.manifest.BeginIndex(ctx, build)
}

func (native retrievalNativeCapabilities) BeginWorkspaceSnapshot(
	ctx context.Context,
	command domain.WorkspaceSnapshotCommand,
) (domain.WorkspaceSnapshotResult, error) {
	if native.snapshot == nil {
		return domain.WorkspaceSnapshotResult{}, dependency("REINDEX_SNAPSHOT_CONNECTION_UNAVAILABLE", errors.New("snapshot builder is unavailable"))
	}
	return native.snapshot.BeginWorkspaceSnapshot(ctx, command)
}

func (native retrievalNativeCapabilities) AcquireSourceRefresh(
	ctx context.Context,
	workspaceID foundation.ID,
) (application.SourceRefreshLease, error) {
	if native.refresh == nil {
		return nil, dependency("SOURCE_REFRESH_LOCK_CONNECTION_UNAVAILABLE", errors.New("source refresh locker is unavailable"))
	}
	return native.refresh.AcquireSourceRefresh(ctx, workspaceID)
}

func (native *retrievalPGXNativeCapabilities) BeginIndex(
	ctx context.Context,
	build domain.IndexBuild,
) (domain.IndexVersionResult, error) {
	if native == nil || native.database == nil {
		return domain.IndexVersionResult{}, dependency("RETRIEVAL_NATIVE_DATABASE_UNAVAILABLE", errors.New("manifest copier is not initialized"))
	}
	return beginIndexNative(ctx, native.database, build)
}

func (native *retrievalPGXNativeCapabilities) BeginWorkspaceSnapshot(
	ctx context.Context,
	command domain.WorkspaceSnapshotCommand,
) (domain.WorkspaceSnapshotResult, error) {
	if native == nil || native.database == nil {
		return domain.WorkspaceSnapshotResult{}, dependency("REINDEX_SNAPSHOT_CONNECTION_UNAVAILABLE", errors.New("snapshot builder is not initialized"))
	}
	return beginWorkspaceSnapshotNative(ctx, native.database, command)
}

func (native *retrievalPGXNativeCapabilities) AcquireSourceRefresh(
	ctx context.Context,
	workspaceID foundation.ID,
) (application.SourceRefreshLease, error) {
	if native == nil || native.database == nil {
		return nil, dependency("SOURCE_REFRESH_LOCK_CONNECTION_UNAVAILABLE", errors.New("source refresh locker is not initialized"))
	}
	return acquireSourceRefreshNative(ctx, native.database, workspaceID)
}
