// Package workspacepostgres persists the Workspace domain in PostgreSQL.
package workspacepostgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the pgx-compatible boundary required by Repository.
type DB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

// Repository is the PostgreSQL implementation of domain.Repository.
type Repository struct{ db DB }

// NewRepository constructs a Workspace repository over a pgx-compatible pool.
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKSPACE_DATABASE_UNAVAILABLE", true, errors.New("database is nil"))
	}
	return &Repository{db: db}, nil
}

// CreateWorkspace inserts one Workspace mapping and returns database timestamps.
func (r *Repository) CreateWorkspace(ctx context.Context, workspace domain.Workspace) (domain.Workspace, error) {
	row := r.db.QueryRow(ctx, `
		INSERT INTO core.workspace (
			id, name, root_path, git_repository_path, git_branch, git_head,
			git_dirty, git_checked_at, status, version, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id::text, name, root_path, git_repository_path, git_branch, git_head,
			git_dirty, git_checked_at, status, version, created_at, updated_at`,
		string(workspace.ID), workspace.Name, workspace.RootPath,
		workspace.Git.RepositoryPath, workspace.Git.Branch, workspace.Git.Head,
		workspace.Git.Dirty, workspace.Git.CheckedAt.UTC(), string(workspace.Status),
		workspace.Version, workspace.CreatedAt.UTC(), workspace.UpdatedAt.UTC(),
	)
	persisted, err := scanWorkspace(row)
	if err != nil {
		return domain.Workspace{}, classify(err, "WORKSPACE_CREATE_FAILED")
	}
	return persisted, nil
}

// GetWorkspaceByID returns one Workspace by stable ID.
func (r *Repository) GetWorkspaceByID(ctx context.Context, id foundation.ID) (domain.Workspace, error) {
	return r.getWorkspace(ctx, "id = $1", string(id))
}

// GetWorkspaceByRootPath returns one Workspace by canonical root path.
func (r *Repository) GetWorkspaceByRootPath(ctx context.Context, rootPath string) (domain.Workspace, error) {
	return r.getWorkspace(ctx, "root_path = $1", rootPath)
}

func (r *Repository) getWorkspace(ctx context.Context, predicate string, argument any) (domain.Workspace, error) {
	query := `SELECT id::text, name, root_path, git_repository_path, git_branch, git_head,
		git_dirty, git_checked_at, status, version, created_at, updated_at
		FROM core.workspace WHERE ` + predicate
	workspace, err := scanWorkspace(r.db.QueryRow(ctx, query, argument))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Workspace{}, foundation.NewError(foundation.ErrorNotFound, "WORKSPACE_NOT_FOUND", false, err)
		}
		return domain.Workspace{}, classify(err, "WORKSPACE_QUERY_FAILED")
	}
	return workspace, nil
}

// ListWorkspaceRoots returns canonical roots in stable lexical order.
func (r *Repository) ListWorkspaceRoots(ctx context.Context) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT root_path FROM core.workspace ORDER BY root_path, id`)
	if err != nil {
		return nil, classify(err, "WORKSPACE_ROOTS_QUERY_FAILED")
	}
	defer rows.Close()
	roots := make([]string, 0)
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return nil, classify(err, "WORKSPACE_ROOTS_QUERY_FAILED")
		}
		roots = append(roots, root)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, "WORKSPACE_ROOTS_QUERY_FAILED")
	}
	return roots, nil
}

// RegisterSourceVersion reuses a Source by stable location and a SourceVersion
// by source/content hash. New content at the same location creates a new version.
func (r *Repository) RegisterSourceVersion(ctx context.Context, registration domain.SourceRegistration) (domain.SourceRegistrationResult, error) {
	results, err := r.RegisterSourceVersions(ctx, []domain.SourceRegistration{registration})
	if err != nil {
		return domain.SourceRegistrationResult{}, err
	}
	return results[0], nil
}

// RegisterSourceVersions 在一个事务中幂等注册一批扫描结果。
func (r *Repository) RegisterSourceVersions(ctx context.Context, registrations []domain.SourceRegistration) ([]domain.SourceRegistrationResult, error) {
	if len(registrations) == 0 {
		return []domain.SourceRegistrationResult{}, nil
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, classify(err, "SOURCE_VERSION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	results := make([]domain.SourceRegistrationResult, 0, len(registrations))
	for _, registration := range registrations {
		source, err := insertOrGetSource(ctx, tx, registration.Source)
		if err != nil {
			return nil, classify(err, "SOURCE_REGISTER_FAILED")
		}
		registration.Version.SourceID = source.ID
		version, created, err := insertOrGetSourceVersion(ctx, tx, registration.Version)
		if err != nil {
			return nil, classify(err, "SOURCE_VERSION_REGISTER_FAILED")
		}
		results = append(results, domain.SourceRegistrationResult{Source: source, Version: version, Created: created})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, classify(err, "SOURCE_VERSION_COMMIT_FAILED")
	}
	return results, nil
}

func insertOrGetSource(ctx context.Context, tx pgx.Tx, source domain.Source) (domain.Source, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO core.source (id, workspace_id, type, logical_name, original_location, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (workspace_id, original_location) DO NOTHING
		RETURNING id::text, workspace_id::text, type, logical_name, original_location, created_at`,
		string(source.ID), string(source.WorkspaceID), source.Type, source.LogicalName,
		source.OriginalLocation, source.CreatedAt.UTC(),
	)
	persisted, err := scanSource(row)
	if err == nil {
		return persisted, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Source{}, err
	}
	return scanSource(tx.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, type, logical_name, original_location, created_at
		FROM core.source WHERE workspace_id = $1 AND original_location = $2`,
		string(source.WorkspaceID), source.OriginalLocation,
	))
}

func insertOrGetSourceVersion(ctx context.Context, tx pgx.Tx, version domain.SourceVersion) (domain.SourceVersion, bool, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO core.source_version (
			id, source_id, content_hash, byte_size, mime_type, original_content_location,
			security_status, parser_version, captured_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9)
		ON CONFLICT (source_id, content_hash) DO NOTHING
		RETURNING id::text, source_id::text, content_hash, byte_size, mime_type,
			original_content_location, security_status, COALESCE(parser_version, ''), captured_at`,
		string(version.ID), string(version.SourceID), version.ContentHash, version.ByteSize,
		version.MediaType, version.OriginalContentLocation, version.SecurityStatus,
		version.ParserVersion, version.CapturedAt.UTC(),
	)
	persisted, err := scanSourceVersion(row)
	if err == nil {
		return persisted, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.SourceVersion{}, false, err
	}
	persisted, err = scanSourceVersion(tx.QueryRow(ctx, `
		SELECT id::text, source_id::text, content_hash, byte_size, mime_type,
			original_content_location, security_status, COALESCE(parser_version, ''), captured_at
		FROM core.source_version WHERE source_id = $1 AND content_hash = $2`,
		string(version.SourceID), version.ContentHash,
	))
	return persisted, false, err
}

type rowScanner interface{ Scan(...any) error }

func scanWorkspace(row rowScanner) (domain.Workspace, error) {
	var workspace domain.Workspace
	var id, status string
	err := row.Scan(&id, &workspace.Name, &workspace.RootPath,
		&workspace.Git.RepositoryPath, &workspace.Git.Branch, &workspace.Git.Head,
		&workspace.Git.Dirty, &workspace.Git.CheckedAt, &status, &workspace.Version,
		&workspace.CreatedAt, &workspace.UpdatedAt)
	if err != nil {
		return domain.Workspace{}, err
	}
	parsed, err := foundation.ParseID(id)
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("parse workspace id: %w", err)
	}
	workspace.ID = parsed
	workspace.Status = domain.WorkspaceStatus(status)
	return workspace, nil
}

func scanSource(row rowScanner) (domain.Source, error) {
	var source domain.Source
	var id, workspaceID string
	if err := row.Scan(&id, &workspaceID, &source.Type, &source.LogicalName, &source.OriginalLocation, &source.CreatedAt); err != nil {
		return domain.Source{}, err
	}
	parsedID, err := foundation.ParseID(id)
	if err != nil {
		return domain.Source{}, fmt.Errorf("parse source id: %w", err)
	}
	parsedWorkspaceID, err := foundation.ParseID(workspaceID)
	if err != nil {
		return domain.Source{}, fmt.Errorf("parse source workspace id: %w", err)
	}
	source.ID = parsedID
	source.WorkspaceID = parsedWorkspaceID
	return source, nil
}

func scanSourceVersion(row rowScanner) (domain.SourceVersion, error) {
	var version domain.SourceVersion
	var id, sourceID string
	if err := row.Scan(&id, &sourceID, &version.ContentHash, &version.ByteSize,
		&version.MediaType, &version.OriginalContentLocation, &version.SecurityStatus,
		&version.ParserVersion, &version.CapturedAt); err != nil {
		return domain.SourceVersion{}, err
	}
	parsedID, err := foundation.ParseID(id)
	if err != nil {
		return domain.SourceVersion{}, fmt.Errorf("parse source version id: %w", err)
	}
	parsedSourceID, err := foundation.ParseID(sourceID)
	if err != nil {
		return domain.SourceVersion{}, fmt.Errorf("parse source version source id: %w", err)
	}
	version.ID = parsedID
	version.SourceID = parsedSourceID
	return version, nil
}

func classify(err error, fallbackCode string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKSPACE_DATA_MISSING", false, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			code := "WORKSPACE_CONFLICT"
			switch pgErr.ConstraintName {
			case "workspace_root_path_key":
				code = "WORKSPACE_ROOT_EXISTS"
			case "uq_workspace_single_active":
				code = "ACTIVE_WORKSPACE_EXISTS"
			}
			return foundation.NewError(foundation.ErrorVersionConflict, code, false, err)
		case "23503":
			return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKSPACE_REFERENCE_INVALID", false, err)
		case "23514", "22P02":
			return foundation.NewError(foundation.ErrorInvalidInput, "WORKSPACE_DATA_INVALID", false, err)
		case "40001", "40P01":
			return foundation.NewError(foundation.ErrorRetryableFailure, fallbackCode, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, true, err)
}

var _ domain.Repository = (*Repository)(nil)
