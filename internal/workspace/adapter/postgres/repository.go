// Package workspacepostgres persists the Workspace domain in PostgreSQL.
package workspacepostgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
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

// RootGrantResolver is the capability boundary required before a persisted
// Workspace root can leave this adapter.
type RootGrantResolver interface {
	Resolve(context.Context, foundation.ID) (*rootgrant.Capability, error)
}

// Repository is the PostgreSQL implementation of domain.Repository.
type Repository struct {
	db      DB
	grants  RootGrantResolver
	managed bool
}

// RepositoryOption configures process-local root authority.
type RepositoryOption func(*Repository) error

// WithRootGrantResolver gates every root-bearing read through resolver.
func WithRootGrantResolver(resolver RootGrantResolver, managed bool) RepositoryOption {
	return func(repository *Repository) error {
		if resolver == nil {
			return errors.New("workspace root grant resolver is nil")
		}
		repository.grants = resolver
		repository.managed = managed
		return nil
	}
}

const workspaceColumns = `id::text,name,root_path,root_fingerprint,binding_version,
	git_repository_path,git_branch,git_head,git_dirty,git_checked_at,
	status,availability,availability_reason,availability_checked_at,last_opened_at,removed_at,
	version,created_at,updated_at`

// NewRepository constructs a Workspace repository over a pgx-compatible pool.
func NewRepository(db DB, options ...RepositoryOption) (*Repository, error) {
	if db == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKSPACE_DATABASE_UNAVAILABLE", true, errors.New("database is nil"))
	}
	repository := &Repository{db: db}
	for _, option := range options {
		if option == nil {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKSPACE_REPOSITORY_OPTION_INVALID", false, errors.New("workspace repository option is nil"))
		}
		if err := option(repository); err != nil {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKSPACE_REPOSITORY_OPTION_INVALID", false, err)
		}
	}
	return repository, nil
}

// AuthorizeRootSelection reserves root creation and root-based opening to the
// Host Controller when this repository belongs to a managed runtime.
func (r *Repository) AuthorizeRootSelection(context.Context) error {
	if r != nil && r.managed {
		return foundation.NewError(foundation.ErrorPermissionDenied, rootgrant.ErrorCodeRootNotGranted, false, errors.New("managed Workspace roots are selected by the Host Controller"))
	}
	return nil
}

// CreateWorkspace inserts one Workspace mapping and returns database timestamps.
func (r *Repository) CreateWorkspace(ctx context.Context, workspace domain.Workspace) (domain.Workspace, error) {
	if r.managed {
		return domain.Workspace{}, foundation.NewError(foundation.ErrorPermissionDenied, rootgrant.ErrorCodeRootNotGranted, false, errors.New("managed Workspace identities are created by the Host Controller"))
	}
	availability := workspace.Availability
	availabilityReason := nullableText(workspace.AvailabilityReason)
	availabilityCheckedAt := nullableTime(workspace.AvailabilityCheckedAt)
	if availability == "" {
		availability = domain.WorkspaceAvailabilityMigrationRequired
		availabilityReason = "WORKSPACE_BINDING_LEGACY_DIRECT"
		availabilityCheckedAt = workspace.UpdatedAt.UTC()
	}
	row := r.db.QueryRow(ctx, `
		INSERT INTO core.workspace (
			id,name,root_path,root_fingerprint,binding_version,
			git_repository_path,git_branch,git_head,git_dirty,git_checked_at,
			status,availability,availability_reason,availability_checked_at,last_opened_at,removed_at,
			version,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		RETURNING `+workspaceColumns,
		string(workspace.ID), workspace.Name, workspace.RootPath, nullableText(workspace.RootFingerprint), workspace.BindingVersion,
		workspace.Git.RepositoryPath, workspace.Git.Branch, workspace.Git.Head,
		workspace.Git.Dirty, workspace.Git.CheckedAt.UTC(), string(workspace.Status), string(availability),
		availabilityReason, availabilityCheckedAt, nullableTime(workspace.LastOpenedAt), nullableTime(workspace.RemovedAt),
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
	workspace, err := r.getWorkspace(ctx, "id = $1", string(id))
	if err != nil {
		return domain.Workspace{}, err
	}
	return r.authorizeWorkspace(ctx, workspace)
}

// GetWorkspaceByRootPath returns one Workspace by canonical root path.
func (r *Repository) GetWorkspaceByRootPath(ctx context.Context, rootPath string) (domain.Workspace, error) {
	workspace, err := r.getWorkspace(ctx, "root_path = $1", rootPath)
	if err != nil {
		return domain.Workspace{}, err
	}
	return r.authorizeWorkspace(ctx, workspace)
}

// ListSourceVersions 返回按捕获时间和 ID 倒序排列的 Source Version 摘要。
func (r *Repository) ListSourceVersions(ctx context.Context, request domain.SourceVersionListQuery) ([]domain.SourceVersionListItem, bool, error) {
	if request.WorkspaceID == "" || request.Limit < 1 || request.Limit > 100 {
		return nil, false, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_LIST_INVALID", false, errors.New("invalid source version list scope"))
	}
	query, args := buildSourceVersionListQuery(request)
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, false, classify(err, "SOURCE_VERSION_LIST_QUERY_FAILED")
	}
	defer rows.Close()
	items := make([]domain.SourceVersionListItem, 0, request.Limit)
	for rows.Next() {
		var id, sourceID, scopeID, path, mime, hash, security, ingestion, workflow, index string
		var size int64
		var captured time.Time
		if err := rows.Scan(&id, &sourceID, &scopeID, &path, &mime, &size, &captured, &hash, &security, &ingestion, &workflow, &index); err != nil {
			return nil, false, classify(err, "SOURCE_VERSION_LIST_SCAN_FAILED")
		}
		items = append(items, domain.SourceVersionListItem{ID: foundation.ID(id), SourceID: foundation.ID(sourceID), WorkspaceID: foundation.ID(scopeID), Path: path, MimeType: mime, ByteSize: size, CapturedAt: captured, ContentHash: hash, SecurityStatus: security, IngestionStatus: ingestion, WorkflowStatus: workflow, IndexStatus: index})
	}
	if err := rows.Err(); err != nil {
		return nil, false, classify(err, "SOURCE_VERSION_LIST_ROWS_FAILED")
	}
	hasMore := len(items) > request.Limit
	if hasMore {
		items = items[:request.Limit]
	}
	return items, hasMore, nil
}

func buildSourceVersionListQuery(request domain.SourceVersionListQuery) (string, []any) {
	// included 绑定具体 Source Version；excluded 的 source_version_id 按 Schema 必须为空，表示整个 Source 未进入 Active Index。
	query := `SELECT sv.id::text,sv.source_id::text,sv.workspace_id::text,s.original_location,sv.mime_type,sv.byte_size,sv.captured_at,sv.content_hash,COALESCE(a.security_status,sv.security_status),COALESCE(a.status,''),COALESCE(wr.status,''),COALESCE(im.selection_status,'') FROM core.source_version sv JOIN core.source s ON s.id=sv.source_id AND s.workspace_id=sv.workspace_id LEFT JOIN LATERAL (SELECT ia.status,ia.security_status,ia.workflow_run_id FROM ingestion.attempt ia WHERE ia.source_version_id=sv.id AND ia.workspace_id=sv.workspace_id ORDER BY ia.started_at DESC,ia.id DESC LIMIT 1) a ON true LEFT JOIN workflow.run wr ON wr.id=a.workflow_run_id AND wr.workspace_id=sv.workspace_id LEFT JOIN retrieval.index_version active_index ON active_index.workspace_id=sv.workspace_id AND active_index.status='active' LEFT JOIN retrieval.index_manifest_source im ON im.index_version_id=active_index.id AND im.workspace_id=sv.workspace_id AND im.source_id=sv.source_id AND (im.selection_status='excluded' OR im.source_version_id=sv.id) WHERE sv.workspace_id=$1`
	args := []any{string(request.WorkspaceID)}
	appendFilter := func(column string, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		query += ` AND ` + column + `=$` + fmt.Sprint(len(args))
	}
	appendFilter("COALESCE(a.security_status,sv.security_status)", request.SecurityStatus)
	appendFilter("COALESCE(a.status,'')", request.IngestionStatus)
	appendFilter("COALESCE(wr.status,'')", request.WorkflowStatus)
	appendFilter("COALESCE(im.selection_status,'')", request.IndexStatus)
	appendFilter("sv.mime_type", request.MimeType)
	if request.CursorTime != nil {
		args = append(args, request.CursorTime.UTC(), string(request.CursorID))
		query += ` AND (sv.captured_at,sv.id)<($` + fmt.Sprint(len(args)-1) + `,$` + fmt.Sprint(len(args)) + `)`
	}
	query += ` ORDER BY sv.captured_at DESC,sv.id DESC LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, request.Limit+1)
	return query, args
}

func (r *Repository) getWorkspace(ctx context.Context, predicate string, argument any) (domain.Workspace, error) {
	query := `SELECT ` + workspaceColumns + ` FROM core.workspace WHERE ` + predicate
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
	if err := r.AuthorizeRootSelection(ctx); err != nil {
		return nil, err
	}
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

// GetSourceMaterial 联合读取 SourceVersion、Source、Workspace 与 ContentArtifact，
// 并在离开 PostgreSQL Adapter 前校验作用域和不可变内容元数据。
func (r *Repository) GetSourceMaterial(ctx context.Context, sourceVersionID foundation.ID) (domain.SourceMaterial, error) {
	if sourceVersionID == "" {
		return domain.SourceMaterial{}, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_ID_INVALID", false, errors.New("source version id is required"))
	}
	var (
		versionID, versionSourceID, versionArtifactID string
		version                                       domain.SourceVersion
		sourceID, sourceWorkspaceID                   string
		workspaceID, workspaceRootPath                string
		artifactID, artifactWorkspaceID               string
		artifact                                      domain.ContentArtifact
		artifactCreatedAt                             sql.NullTime
	)
	err := r.db.QueryRow(ctx, `
		SELECT
			sv.id::text,
			sv.source_id::text,
			COALESCE(sv.content_artifact_id::text, ''),
			sv.content_hash,
			sv.byte_size,
			sv.mime_type,
			sv.original_content_location,
			sv.security_status,
			COALESCE(sv.parser_version, ''),
			sv.captured_at,
			s.id::text,
			s.workspace_id::text,
			w.id::text,
			w.root_path,
			COALESCE(ca.id::text, ''),
			COALESCE(ca.workspace_id::text, ''),
			COALESCE(ca.content_hash, ''),
			COALESCE(ca.byte_size, -1),
			COALESCE(ca.managed_location, ''),
			ca.created_at
		FROM core.source_version sv
		JOIN core.source s ON s.id = sv.source_id
		JOIN core.workspace w ON w.id = s.workspace_id
		LEFT JOIN core.content_artifact ca ON ca.id = sv.content_artifact_id
		WHERE sv.id = $1`, string(sourceVersionID)).Scan(
		&versionID, &versionSourceID, &versionArtifactID,
		&version.ContentHash, &version.ByteSize, &version.MediaType,
		&version.OriginalContentLocation, &version.SecurityStatus, &version.ParserVersion, &version.CapturedAt,
		&sourceID, &sourceWorkspaceID, &workspaceID, &workspaceRootPath,
		&artifactID, &artifactWorkspaceID, &artifact.ContentHash, &artifact.ByteSize,
		&artifact.ManagedLocation, &artifactCreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SourceMaterial{}, foundation.NewError(foundation.ErrorNotFound, "SOURCE_VERSION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.SourceMaterial{}, classify(err, "SOURCE_MATERIAL_QUERY_FAILED")
	}
	if versionArtifactID == "" {
		return domain.SourceMaterial{}, foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_VERSION_ARTIFACT_MISSING", false, errors.New("legacy source version has no content artifact"))
	}
	if artifactID == "" || !artifactCreatedAt.Valid {
		return domain.SourceMaterial{}, foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_MATERIAL_ARTIFACT_MISSING", false, errors.New("source version content artifact is missing"))
	}
	parsedVersionID, err := parseMaterialID(versionID, "source version")
	if err != nil {
		return domain.SourceMaterial{}, err
	}
	parsedVersionSourceID, err := parseMaterialID(versionSourceID, "source version source")
	if err != nil {
		return domain.SourceMaterial{}, err
	}
	parsedVersionArtifactID, err := parseMaterialID(versionArtifactID, "source version artifact")
	if err != nil {
		return domain.SourceMaterial{}, err
	}
	parsedSourceID, err := parseMaterialID(sourceID, "source")
	if err != nil {
		return domain.SourceMaterial{}, err
	}
	parsedSourceWorkspaceID, err := parseMaterialID(sourceWorkspaceID, "source workspace")
	if err != nil {
		return domain.SourceMaterial{}, err
	}
	parsedWorkspaceID, err := parseMaterialID(workspaceID, "workspace")
	if err != nil {
		return domain.SourceMaterial{}, err
	}
	parsedArtifactID, err := parseMaterialID(artifactID, "content artifact")
	if err != nil {
		return domain.SourceMaterial{}, err
	}
	parsedArtifactWorkspaceID, err := parseMaterialID(artifactWorkspaceID, "content artifact workspace")
	if err != nil {
		return domain.SourceMaterial{}, err
	}
	version.ID = parsedVersionID
	version.SourceID = parsedVersionSourceID
	version.ContentArtifactID = parsedVersionArtifactID
	artifact.ID = parsedArtifactID
	artifact.WorkspaceID = parsedArtifactWorkspaceID
	artifact.CreatedAt = artifactCreatedAt.Time
	if parsedVersionID != sourceVersionID || parsedVersionSourceID != parsedSourceID || parsedSourceWorkspaceID != parsedWorkspaceID || parsedVersionArtifactID != parsedArtifactID || parsedArtifactWorkspaceID != parsedWorkspaceID {
		return domain.SourceMaterial{}, foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_MATERIAL_SCOPE_INVALID", false, errors.New("source material crosses workspace or source scope"))
	}
	if version.ContentHash != artifact.ContentHash || version.ByteSize != artifact.ByteSize {
		return domain.SourceMaterial{}, foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_MATERIAL_METADATA_CONFLICT", false, errors.New("source version and content artifact metadata differ"))
	}
	authorizedWorkspace, err := r.authorizeWorkspace(ctx, domain.Workspace{ID: parsedWorkspaceID, RootPath: workspaceRootPath})
	if err != nil {
		return domain.SourceMaterial{}, err
	}
	return domain.SourceMaterial{
		WorkspaceID: parsedWorkspaceID, WorkspaceRootPath: authorizedWorkspace.RootPath, SourceID: parsedSourceID,
		SourceVersion: version, ContentArtifact: artifact,
	}, nil
}

func (r *Repository) authorizeWorkspace(ctx context.Context, workspace domain.Workspace) (domain.Workspace, error) {
	if r.grants == nil {
		return workspace, nil
	}
	capability, err := r.grants.Resolve(ctx, workspace.ID)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer func() { _ = capability.Close() }()
	if capability.WorkspaceID() != workspace.ID || capability.CanonicalRoot() != workspace.RootPath {
		return domain.Workspace{}, foundation.NewError(foundation.ErrorConsistencyViolation, rootgrant.ErrorCodeGrantStale, false, errors.New("workspace row differs from root grant"))
	}
	if err := capability.Revalidate(ctx); err != nil {
		return domain.Workspace{}, err
	}
	workspace.RootPath = capability.CanonicalRoot()
	return workspace, nil
}

func parseMaterialID(value, field string) (foundation.ID, error) {
	parsed, err := foundation.ParseID(value)
	if err != nil {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_MATERIAL_ID_INVALID", false, fmt.Errorf("parse %s id: %w", field, err))
	}
	return parsed, nil
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
		artifact, artifactCreated, err := insertOrGetContentArtifact(ctx, tx, registration.Artifact)
		if err != nil {
			return nil, classify(err, "CONTENT_ARTIFACT_REGISTER_FAILED")
		}
		registration.Version.SourceID = source.ID
		registration.Version.ContentArtifactID = artifact.ID
		version, created, err := insertOrGetSourceVersion(ctx, tx, source.WorkspaceID, registration.Version)
		if err != nil {
			return nil, classify(err, "SOURCE_VERSION_REGISTER_FAILED")
		}
		results = append(results, domain.SourceRegistrationResult{Source: source, Artifact: artifact, Version: version, ArtifactCreated: artifactCreated, Created: created})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, classify(err, "SOURCE_VERSION_COMMIT_FAILED")
	}
	return results, nil
}

func insertOrGetContentArtifact(ctx context.Context, tx pgx.Tx, artifact domain.ContentArtifact) (domain.ContentArtifact, bool, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO core.content_artifact (id, workspace_id, content_hash, byte_size, managed_location, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (workspace_id, content_hash) DO NOTHING
		RETURNING id::text, workspace_id::text, content_hash, byte_size, managed_location, created_at`,
		string(artifact.ID), string(artifact.WorkspaceID), artifact.ContentHash, artifact.ByteSize,
		artifact.ManagedLocation, artifact.CreatedAt.UTC(),
	)
	persisted, err := scanContentArtifact(row)
	if err == nil {
		return persisted, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.ContentArtifact{}, false, err
	}
	persisted, err = scanContentArtifact(tx.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, content_hash, byte_size, managed_location, created_at
		FROM core.content_artifact WHERE workspace_id = $1 AND content_hash = $2`,
		string(artifact.WorkspaceID), artifact.ContentHash,
	))
	if err != nil {
		return domain.ContentArtifact{}, false, err
	}
	if persisted.ByteSize != artifact.ByteSize || persisted.ManagedLocation != artifact.ManagedLocation {
		return domain.ContentArtifact{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "CONTENT_ARTIFACT_METADATA_CONFLICT", false, errors.New("content artifact metadata does not match existing hash"))
	}
	return persisted, false, nil
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

func insertOrGetSourceVersion(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, version domain.SourceVersion) (domain.SourceVersion, bool, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO core.source_version (
			id, source_id, workspace_id, content_artifact_id, content_hash, byte_size, mime_type, original_content_location,
			security_status, parser_version, captured_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11)
		ON CONFLICT (source_id, content_hash) DO NOTHING
		RETURNING id::text, source_id::text, content_artifact_id::text, content_hash, byte_size, mime_type,
			original_content_location, security_status, COALESCE(parser_version, ''), captured_at`,
		string(version.ID), string(version.SourceID), string(workspaceID), string(version.ContentArtifactID), version.ContentHash, version.ByteSize,
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
		UPDATE core.source_version AS sv
		SET content_artifact_id = $4
		WHERE sv.workspace_id = $1 AND sv.source_id = $2 AND sv.content_hash = $3 AND sv.content_artifact_id IS NULL
		RETURNING id::text, source_id::text, content_artifact_id::text, content_hash, byte_size, mime_type,
			original_content_location, security_status, COALESCE(parser_version, ''), captured_at`,
		string(workspaceID), string(version.SourceID), version.ContentHash, string(version.ContentArtifactID),
	))
	if err == nil {
		return persisted, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.SourceVersion{}, false, err
	}
	persisted, err = scanSourceVersion(tx.QueryRow(ctx, `
		SELECT id::text, source_id::text, content_artifact_id::text, content_hash, byte_size, mime_type,
			original_content_location, security_status, COALESCE(parser_version, ''), captured_at
		FROM core.source_version sv
		WHERE sv.workspace_id = $1 AND sv.source_id = $2 AND sv.content_hash = $3`,
		string(workspaceID), string(version.SourceID), version.ContentHash,
	))
	if err == nil && !sameSourceVersionMetadata(persisted, version) {
		return domain.SourceVersion{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_VERSION_METADATA_CONFLICT", false, errors.New("existing source version metadata differs from the requested registration"))
	}
	return persisted, false, err
}

func sameSourceVersionMetadata(existing, requested domain.SourceVersion) bool {
	return existing.SourceID == requested.SourceID &&
		existing.ContentArtifactID == requested.ContentArtifactID &&
		existing.ContentHash == requested.ContentHash &&
		existing.ByteSize == requested.ByteSize &&
		existing.MediaType == requested.MediaType &&
		existing.OriginalContentLocation == requested.OriginalContentLocation &&
		existing.SecurityStatus == requested.SecurityStatus &&
		existing.ParserVersion == requested.ParserVersion
}

func scanContentArtifact(row rowScanner) (domain.ContentArtifact, error) {
	var artifact domain.ContentArtifact
	var id, workspaceID string
	if err := row.Scan(&id, &workspaceID, &artifact.ContentHash, &artifact.ByteSize, &artifact.ManagedLocation, &artifact.CreatedAt); err != nil {
		return domain.ContentArtifact{}, err
	}
	parsedID, err := foundation.ParseID(id)
	if err != nil {
		return domain.ContentArtifact{}, fmt.Errorf("parse content artifact id: %w", err)
	}
	parsedWorkspaceID, err := foundation.ParseID(workspaceID)
	if err != nil {
		return domain.ContentArtifact{}, fmt.Errorf("parse content artifact workspace id: %w", err)
	}
	artifact.ID = parsedID
	artifact.WorkspaceID = parsedWorkspaceID
	return artifact, nil
}

type rowScanner interface{ Scan(...any) error }

func scanWorkspace(row rowScanner) (domain.Workspace, error) {
	var workspace domain.Workspace
	var id, status, availability string
	var fingerprint, availabilityReason sql.NullString
	var availabilityCheckedAt, lastOpenedAt, removedAt sql.NullTime
	err := row.Scan(&id, &workspace.Name, &workspace.RootPath, &fingerprint, &workspace.BindingVersion,
		&workspace.Git.RepositoryPath, &workspace.Git.Branch, &workspace.Git.Head,
		&workspace.Git.Dirty, &workspace.Git.CheckedAt, &status, &availability,
		&availabilityReason, &availabilityCheckedAt, &lastOpenedAt, &removedAt, &workspace.Version,
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
	workspace.Availability = domain.WorkspaceAvailability(availability)
	workspace.RootFingerprint = fingerprint.String
	workspace.AvailabilityReason = availabilityReason.String
	if availabilityCheckedAt.Valid {
		workspace.AvailabilityCheckedAt = availabilityCheckedAt.Time
	}
	if lastOpenedAt.Valid {
		workspace.LastOpenedAt = lastOpenedAt.Time
	}
	if removedAt.Valid {
		workspace.RemovedAt = removedAt.Time
	}
	return workspace, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
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
	var id, sourceID, artifactID string
	if err := row.Scan(&id, &sourceID, &artifactID, &version.ContentHash, &version.ByteSize,
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
	parsedArtifactID, err := foundation.ParseID(artifactID)
	if err != nil {
		return domain.SourceVersion{}, fmt.Errorf("parse source version content artifact id: %w", err)
	}
	version.ContentArtifactID = parsedArtifactID
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
var _ domain.SourceMaterialRepository = (*Repository)(nil)
