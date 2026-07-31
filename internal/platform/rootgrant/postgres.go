package rootgrant

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

// QueryRower is the narrow PostgreSQL boundary used for grant authority.
type QueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// PostgresAuthoritativeStore reads one explicit authority mode and never
// falls back from managed Docker state to native development state.
type PostgresAuthoritativeStore struct {
	db   QueryRower
	mode RuntimeGrantMode
}

// NewPostgresAuthoritativeStore creates a mode-specific authority reader.
func NewPostgresAuthoritativeStore(db QueryRower, mode RuntimeGrantMode) (*PostgresAuthoritativeStore, error) {
	if db == nil || (mode != RuntimeGrantManaged && mode != RuntimeGrantDirect) {
		return nil, staleDependencyError(false)
	}
	return &PostgresAuthoritativeStore{db: db, mode: mode}, nil
}

// CurrentRootGrant returns one atomic authority projection.
func (store *PostgresAuthoritativeStore) CurrentRootGrant(ctx context.Context) (AuthoritativeView, error) {
	if store == nil || store.db == nil {
		return AuthoritativeView{}, staleDependencyError(false)
	}
	if store.mode == RuntimeGrantDirect {
		return store.currentDirectGrant(ctx)
	}
	return store.currentManagedGrant(ctx)
}

func (store *PostgresAuthoritativeStore) currentManagedGrant(ctx context.Context) (AuthoritativeView, error) {
	var activeID, workspaceID, root string
	var workspaceActive, workspaceAvailable bool
	var generation int64
	err := store.db.QueryRow(ctx, `
		SELECT COALESCE(state.active_workspace_id::text,''),
		       COALESCE(workspace.id::text,''),
		       COALESCE(workspace.status='active',false),
		       COALESCE(workspace.availability='available' AND workspace.removed_at IS NULL,false),
		       COALESCE(workspace.root_path,''),
		       state.grant_generation
		FROM ops.workspace_control_state AS state
		LEFT JOIN core.workspace AS workspace ON workspace.id=state.active_workspace_id
		WHERE state.singleton=true`).Scan(
		&activeID, &workspaceID, &workspaceActive, &workspaceAvailable, &root, &generation,
	)
	if err != nil {
		return AuthoritativeView{}, err
	}
	return authoritativeView(activeID, workspaceID, workspaceActive, workspaceAvailable, root, generation)
}

func (store *PostgresAuthoritativeStore) currentDirectGrant(ctx context.Context) (AuthoritativeView, error) {
	var workspaceID, root string
	var available bool
	var count int64
	err := store.db.QueryRow(ctx, `
			SELECT COALESCE(max(id::text),''),COALESCE(max(root_path),''),
			       true,count(*)
			FROM core.workspace
			WHERE status='active'`).Scan(&workspaceID, &root, &available, &count)
	if err != nil {
		return AuthoritativeView{}, err
	}
	if count == 0 {
		return AuthoritativeView{}, nil
	}
	if count != 1 {
		return AuthoritativeView{}, errors.New("direct workspace authority is not unique")
	}
	return authoritativeView(workspaceID, workspaceID, true, available, root, 1)
}

func authoritativeView(activeID, workspaceID string, active, available bool, root string, generation int64) (AuthoritativeView, error) {
	view := AuthoritativeView{
		WorkspaceActive: active, WorkspaceAvailable: available,
		PersistedRoot: root, GrantGeneration: generation,
	}
	if activeID != "" {
		parsed, err := foundation.ParseID(activeID)
		if err != nil {
			return AuthoritativeView{}, err
		}
		view.ActiveWorkspaceID = parsed
	}
	if workspaceID != "" {
		parsed, err := foundation.ParseID(workspaceID)
		if err != nil {
			return AuthoritativeView{}, err
		}
		view.WorkspaceID = parsed
	}
	return view, nil
}

// NewRuntimeResolver builds the process resolver without a managed-to-direct fallback.
func NewRuntimeResolver(db QueryRower, lookup LookupEnv) (*RootGrantResolver, RuntimeGrantMode, ProcessGrant, error) {
	grant, mode, err := LoadRuntimeGrant(lookup)
	if err != nil {
		return nil, 0, ProcessGrant{}, err
	}
	store, err := NewPostgresAuthoritativeStore(db, mode)
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
