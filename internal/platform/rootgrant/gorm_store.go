package rootgrant

import (
	"context"
	"database/sql"
	"errors"

	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMAuthoritativeStore reads root authority through the platform-owned GORM
// root. It is staged only; the legacy pgx store remains the production path.
type GORMAuthoritativeStore struct {
	database *gorm.DB
	mode     RuntimeGrantMode
}

// NewGORMAuthoritativeStore creates a mode-specific authority reader from the
// complete platform pool, preventing an independent GORM root from diverging
// from the process database boundary.
func NewGORMAuthoritativeStore(pool *platformpostgres.Pool, mode RuntimeGrantMode) (*GORMAuthoritativeStore, error) {
	if pool == nil || (mode != RuntimeGrantManaged && mode != RuntimeGrantDirect) {
		return nil, staleDependencyError(false)
	}
	database, err := pool.GORM()
	if err != nil || !validGORMDatabase(database) {
		return nil, staleDependencyError(false)
	}
	return &GORMAuthoritativeStore{database: database, mode: mode}, nil
}

// CurrentRootGrant returns one atomic authority projection without falling
// back between managed and direct runtime modes.
func (store *GORMAuthoritativeStore) CurrentRootGrant(ctx context.Context) (AuthoritativeView, error) {
	if store == nil || !validGORMDatabase(store.database) {
		return AuthoritativeView{}, staleDependencyError(false)
	}
	if ctx == nil {
		return AuthoritativeView{}, staleCancellationError(context.Canceled)
	}
	if cause := gormRootGrantContextCause(ctx); cause != nil {
		return AuthoritativeView{}, staleCancellationError(cause)
	}
	switch store.mode {
	case RuntimeGrantManaged:
		return store.currentManagedGrant(ctx)
	case RuntimeGrantDirect:
		return store.currentDirectGrant(ctx)
	default:
		return AuthoritativeView{}, staleDependencyError(false)
	}
}

func (store *GORMAuthoritativeStore) currentManagedGrant(ctx context.Context) (AuthoritativeView, error) {
	row, err := gormAuthorityRow(store.database.WithContext(ctx), `
		SELECT COALESCE(state.active_workspace_id::text,''),
		       COALESCE(workspace.id::text,''),
		       COALESCE(workspace.status='active',false),
		       COALESCE(workspace.availability='available' AND workspace.removed_at IS NULL,false),
		       COALESCE(workspace.root_path,''),
		       state.grant_generation
		FROM ops.workspace_control_state AS state
		LEFT JOIN core.workspace AS workspace ON workspace.id=state.active_workspace_id
		WHERE state.singleton=true`)
	if err != nil {
		return AuthoritativeView{}, store.authorityFailure(ctx)
	}
	var activeID, workspaceID, root string
	var workspaceActive, workspaceAvailable bool
	var generation int64
	if err := row.Scan(&activeID, &workspaceID, &workspaceActive, &workspaceAvailable, &root, &generation); err != nil {
		return AuthoritativeView{}, store.authorityFailure(ctx)
	}
	view, err := authoritativeView(activeID, workspaceID, workspaceActive, workspaceAvailable, root, generation)
	if err != nil {
		return AuthoritativeView{}, staleConsistencyError()
	}
	return view, nil
}

func (store *GORMAuthoritativeStore) currentDirectGrant(ctx context.Context) (AuthoritativeView, error) {
	row, err := gormAuthorityRow(store.database.WithContext(ctx), `
		SELECT COALESCE(max(id::text),''),COALESCE(max(root_path),''),
		       true,count(*)
		FROM core.workspace
		WHERE status='active'`)
	if err != nil {
		return AuthoritativeView{}, store.authorityFailure(ctx)
	}
	var workspaceID, root string
	var available bool
	var count int64
	if err := row.Scan(&workspaceID, &root, &available, &count); err != nil {
		return AuthoritativeView{}, store.authorityFailure(ctx)
	}
	if count == 0 {
		return AuthoritativeView{}, nil
	}
	if count != 1 {
		return AuthoritativeView{}, staleConsistencyError()
	}
	view, err := authoritativeView(workspaceID, workspaceID, true, available, root, 1)
	if err != nil {
		return AuthoritativeView{}, staleConsistencyError()
	}
	return view, nil
}

func (store *GORMAuthoritativeStore) authorityFailure(ctx context.Context) error {
	if cause := gormRootGrantContextCause(ctx); cause != nil {
		return staleCancellationError(cause)
	}
	return staleDependencyError(true)
}

func gormRootGrantContextCause(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	cause := context.Cause(ctx)
	if cause == nil {
		return ctx.Err()
	}
	if !errors.Is(cause, ctx.Err()) {
		return errors.Join(ctx.Err(), cause)
	}
	return cause
}

func gormAuthorityRow(database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	statement := database.Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("root grant authority query returned no row handle")
	}
	return row, nil
}

func validGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

// NewGORMRuntimeResolver builds the staged process resolver from one shared
// Pool. Managed configuration never degrades to direct mode when authority is
// unavailable or malformed.
func NewGORMRuntimeResolver(pool *platformpostgres.Pool, lookup LookupEnv) (*RootGrantResolver, RuntimeGrantMode, ProcessGrant, error) {
	grant, mode, err := LoadRuntimeGrant(lookup)
	if err != nil {
		return nil, 0, ProcessGrant{}, err
	}
	store, err := NewGORMAuthoritativeStore(pool, mode)
	if err != nil {
		return nil, 0, ProcessGrant{}, err
	}
	if mode == RuntimeGrantManaged {
		resolver, resolveErr := NewRootGrantResolver(store, grant)
		return resolver, mode, grant, resolveErr
	}
	resolver, err := NewDirectRootGrantResolver(store)
	return resolver, mode, ProcessGrant{}, err
}

var _ AuthoritativeStore = (*GORMAuthoritativeStore)(nil)
