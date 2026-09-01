package workspacepostgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// AuthorizeRootSelection reserves root creation and root-based opening to the
// local Workspace control command in a managed runtime.
func (repository *GORMRepository) AuthorizeRootSelection(ctx context.Context) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if repository.managed {
		return foundation.NewError(foundation.ErrorPermissionDenied, rootgrant.ErrorCodeRootNotGranted, false, errors.New("managed Workspace roots are selected by the local Workspace control command"))
	}
	return nil
}

// CreateWorkspace inserts one Workspace mapping and returns database timestamps.
func (repository *GORMRepository) CreateWorkspace(ctx context.Context, workspace domain.Workspace) (domain.Workspace, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Workspace{}, err
	}
	if repository.managed {
		return domain.Workspace{}, foundation.NewError(foundation.ErrorPermissionDenied, rootgrant.ErrorCodeRootNotGranted, false, errors.New("managed Workspace identities are created by the local Workspace control command"))
	}
	availability := workspace.Availability
	availabilityReason := nullableText(workspace.AvailabilityReason)
	availabilityCheckedAt := nullableTime(workspace.AvailabilityCheckedAt)
	if availability == "" {
		availability = domain.WorkspaceAvailabilityMigrationRequired
		availabilityReason = "WORKSPACE_BINDING_LEGACY_DIRECT"
		availabilityCheckedAt = workspace.UpdatedAt.UTC()
	}
	row, err := gormWorkspaceRawRow(repository.database.WithContext(ctx), `
		INSERT INTO core.workspace (
			id,name,root_path,root_fingerprint,binding_version,
			git_repository_path,git_branch,git_head,git_dirty,git_checked_at,
			status,availability,availability_reason,availability_checked_at,last_opened_at,removed_at,
			version,created_at,updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		RETURNING `+workspaceColumns,
		string(workspace.ID), workspace.Name, workspace.RootPath, nullableText(workspace.RootFingerprint), workspace.BindingVersion,
		workspace.Git.RepositoryPath, workspace.Git.Branch, workspace.Git.Head,
		workspace.Git.Dirty, workspace.Git.CheckedAt.UTC(), string(workspace.Status), string(availability),
		availabilityReason, availabilityCheckedAt, nullableTime(workspace.LastOpenedAt), nullableTime(workspace.RemovedAt),
		workspace.Version, workspace.CreatedAt.UTC(), workspace.UpdatedAt.UTC(),
	)
	if err != nil {
		return domain.Workspace{}, classifyGORMWorkspace(ctx, err, "WORKSPACE_CREATE_FAILED")
	}
	persisted, err := scanWorkspace(row)
	if err != nil {
		return domain.Workspace{}, classifyGORMWorkspace(ctx, err, "WORKSPACE_CREATE_FAILED")
	}
	return persisted, nil
}

// GetWorkspaceByID returns one Workspace by stable ID.
func (repository *GORMRepository) GetWorkspaceByID(ctx context.Context, id foundation.ID) (domain.Workspace, error) {
	workspace, err := repository.gormGetWorkspace(ctx, "id = ?", string(id))
	if err != nil {
		return domain.Workspace{}, err
	}
	return repository.gormAuthorizeWorkspace(ctx, workspace)
}

// GetWorkspaceByRootPath returns one Workspace by canonical root path.
func (repository *GORMRepository) GetWorkspaceByRootPath(ctx context.Context, rootPath string) (domain.Workspace, error) {
	workspace, err := repository.gormGetWorkspace(ctx, "root_path = ?", rootPath)
	if err != nil {
		return domain.Workspace{}, err
	}
	return repository.gormAuthorizeWorkspace(ctx, workspace)
}

func (repository *GORMRepository) gormGetWorkspace(ctx context.Context, predicate string, argument any) (domain.Workspace, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Workspace{}, err
	}
	row, err := gormWorkspaceRawRow(repository.database.WithContext(ctx), `SELECT `+workspaceColumns+` FROM core.workspace WHERE `+predicate, argument)
	if err != nil {
		return domain.Workspace{}, classifyGORMWorkspace(ctx, err, "WORKSPACE_QUERY_FAILED")
	}
	workspace, err := scanWorkspace(row)
	if gormWorkspaceNoRows(err) {
		return domain.Workspace{}, foundation.NewError(foundation.ErrorNotFound, "WORKSPACE_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Workspace{}, classifyGORMWorkspace(ctx, err, "WORKSPACE_QUERY_FAILED")
	}
	return workspace, nil
}

// GetActiveWorkspace returns the only active Workspace and verifies its root
// grant before any root-bearing data leaves the adapter.
func (repository *GORMRepository) GetActiveWorkspace(ctx context.Context) (domain.Workspace, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Workspace{}, err
	}
	var activeCount int64
	row, err := gormWorkspaceRawRow(repository.database.WithContext(ctx), `SELECT `+workspaceColumns+`,count(*) OVER ()
		FROM core.workspace
		WHERE status = ?
		ORDER BY id
		LIMIT 2`, string(domain.WorkspaceStatusActive))
	if err != nil {
		return domain.Workspace{}, classifyGORMWorkspace(ctx, err, "ACTIVE_WORKSPACE_QUERY_FAILED")
	}
	workspace, err := scanWorkspace(gormActiveWorkspaceRow{row: row, count: &activeCount})
	if gormWorkspaceNoRows(err) {
		return domain.Workspace{}, foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeActiveWorkspaceNotFound, false, err)
	}
	if err != nil {
		return domain.Workspace{}, classifyGORMWorkspace(ctx, err, "ACTIVE_WORKSPACE_QUERY_FAILED")
	}
	if activeCount != 1 {
		return domain.Workspace{}, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeActiveWorkspaceNotUnique, false, errors.New("active Workspace projection is not unique"))
	}
	return repository.gormAuthorizeWorkspace(ctx, workspace)
}

type gormActiveWorkspaceRow struct {
	row   *sql.Row
	count *int64
}

func (row gormActiveWorkspaceRow) Scan(destinations ...any) error {
	return row.row.Scan(append(destinations, row.count)...)
}

// ListWorkspaceRoots returns canonical roots in stable lexical order.
func (repository *GORMRepository) ListWorkspaceRoots(ctx context.Context) ([]string, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if err := repository.AuthorizeRootSelection(ctx); err != nil {
		return nil, err
	}
	rows, err := gormWorkspaceRows(repository.database.WithContext(ctx), `SELECT root_path FROM core.workspace ORDER BY root_path, id`)
	if err != nil {
		return nil, classifyGORMWorkspace(ctx, err, "WORKSPACE_ROOTS_QUERY_FAILED")
	}
	defer rows.Close()
	roots := make([]string, 0)
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return nil, classifyGORMWorkspace(ctx, err, "WORKSPACE_ROOTS_QUERY_FAILED")
		}
		roots = append(roots, root)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORMWorkspace(ctx, err, "WORKSPACE_ROOTS_QUERY_FAILED")
	}
	return roots, nil
}

// ListSourceVersions returns a stable, keyset-paginated Source Version page.
func (repository *GORMRepository) ListSourceVersions(ctx context.Context, request domain.SourceVersionListQuery) ([]domain.SourceVersionListItem, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, false, err
	}
	if request.WorkspaceID == "" || request.Limit < 1 || request.Limit > 100 {
		return nil, false, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_LIST_INVALID", false, errors.New("invalid source version list scope"))
	}
	query, args := buildGORMSourceVersionListQuery(request)
	rows, err := gormWorkspaceRows(repository.database.WithContext(ctx), query, args...)
	if err != nil {
		return nil, false, classifyGORMWorkspace(ctx, err, "SOURCE_VERSION_LIST_QUERY_FAILED")
	}
	defer rows.Close()
	items := make([]domain.SourceVersionListItem, 0, request.Limit+1)
	for rows.Next() {
		var id, sourceID, scopeID, path, mime, hash, security, ingestion, workflow, index string
		var size int64
		var captured time.Time
		if err := rows.Scan(&id, &sourceID, &scopeID, &path, &mime, &size, &captured, &hash, &security, &ingestion, &workflow, &index); err != nil {
			return nil, false, classifyGORMWorkspace(ctx, err, "SOURCE_VERSION_LIST_SCAN_FAILED")
		}
		versionID, sourceVersionIDErr := foundation.ParseID(id)
		persistedSourceID, sourceIDErr := foundation.ParseID(sourceID)
		workspaceID, workspaceIDErr := foundation.ParseID(scopeID)
		if sourceVersionIDErr != nil || sourceIDErr != nil || workspaceIDErr != nil || workspaceID != request.WorkspaceID {
			return nil, false, foundation.NewError(
				foundation.ErrorConsistencyViolation,
				"SOURCE_VERSION_LIST_RESULT_INVALID",
				false,
				errors.New("source version list row identity is invalid"),
			)
		}
		items = append(items, domain.SourceVersionListItem{ID: versionID, SourceID: persistedSourceID, WorkspaceID: workspaceID, Path: path, MimeType: mime, ByteSize: size, CapturedAt: captured, ContentHash: hash, SecurityStatus: security, IngestionStatus: ingestion, WorkflowStatus: workflow, IndexStatus: index})
	}
	if err := rows.Err(); err != nil {
		return nil, false, classifyGORMWorkspace(ctx, err, "SOURCE_VERSION_LIST_ROWS_FAILED")
	}
	hasMore := len(items) > request.Limit
	if hasMore {
		items = items[:request.Limit]
	}
	return items, hasMore, nil
}

func buildGORMSourceVersionListQuery(request domain.SourceVersionListQuery) (string, []any) {
	query := `SELECT sv.id::text,sv.source_id::text,sv.workspace_id::text,s.original_location,sv.mime_type,sv.byte_size,sv.captured_at,sv.content_hash,COALESCE(a.security_status,sv.security_status),COALESCE(a.status,''),COALESCE(wr.status,''),COALESCE(im.selection_status,'') FROM core.source_version sv JOIN core.source s ON s.id=sv.source_id AND s.workspace_id=sv.workspace_id AND s.removed_at IS NULL LEFT JOIN LATERAL (SELECT ia.status,ia.security_status,ia.workflow_run_id FROM ingestion.attempt ia WHERE ia.source_version_id=sv.id AND ia.workspace_id=sv.workspace_id ORDER BY ia.started_at DESC,ia.id DESC LIMIT 1) a ON true LEFT JOIN workflow.run wr ON wr.id=a.workflow_run_id AND wr.workspace_id=sv.workspace_id LEFT JOIN retrieval.index_version active_index ON active_index.workspace_id=sv.workspace_id AND active_index.status='active' LEFT JOIN retrieval.index_manifest_source im ON im.index_version_id=active_index.id AND im.workspace_id=sv.workspace_id AND im.source_id=sv.source_id AND (im.selection_status='excluded' OR im.source_version_id=sv.id) WHERE sv.workspace_id=?`
	args := []any{string(request.WorkspaceID)}
	appendFilter := func(column, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		query += ` AND ` + column + `=?`
	}
	appendFilter("COALESCE(a.security_status,sv.security_status)", request.SecurityStatus)
	appendFilter("COALESCE(a.status,'')", request.IngestionStatus)
	appendFilter("COALESCE(wr.status,'')", request.WorkflowStatus)
	appendFilter("COALESCE(im.selection_status,'')", request.IndexStatus)
	appendFilter("sv.mime_type", request.MimeType)
	if request.CursorTime != nil {
		args = append(args, request.CursorTime.UTC(), string(request.CursorID))
		query += ` AND (sv.captured_at,sv.id)<(?,?)`
	}
	query += ` ORDER BY sv.captured_at DESC,sv.id DESC LIMIT ?`
	args = append(args, request.Limit+1)
	return query, args
}

// GetSourceMaterial reads SourceVersion, Source, Workspace and ContentArtifact
// and verifies their immutable scope and content metadata before returning.
func (repository *GORMRepository) GetSourceMaterial(ctx context.Context, sourceVersionID foundation.ID) (domain.SourceMaterial, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SourceMaterial{}, err
	}
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
	row, err := gormWorkspaceRawRow(repository.database.WithContext(ctx), `
		SELECT sv.id::text,sv.source_id::text,COALESCE(sv.content_artifact_id::text,''),sv.content_hash,sv.byte_size,sv.mime_type,sv.original_content_location,sv.security_status,COALESCE(sv.parser_version,''),sv.captured_at,s.id::text,s.workspace_id::text,w.id::text,w.root_path,COALESCE(ca.id::text,''),COALESCE(ca.workspace_id::text,''),COALESCE(ca.content_hash,''),COALESCE(ca.byte_size,-1),COALESCE(ca.managed_location,''),ca.created_at FROM core.source_version sv JOIN core.source s ON s.id=sv.source_id JOIN core.workspace w ON w.id=s.workspace_id LEFT JOIN core.content_artifact ca ON ca.id=sv.content_artifact_id WHERE sv.id=?`, string(sourceVersionID))
	if err != nil {
		return domain.SourceMaterial{}, classifyGORMWorkspace(ctx, err, "SOURCE_MATERIAL_QUERY_FAILED")
	}
	err = row.Scan(&versionID, &versionSourceID, &versionArtifactID, &version.ContentHash, &version.ByteSize, &version.MediaType, &version.OriginalContentLocation, &version.SecurityStatus, &version.ParserVersion, &version.CapturedAt, &sourceID, &sourceWorkspaceID, &workspaceID, &workspaceRootPath, &artifactID, &artifactWorkspaceID, &artifact.ContentHash, &artifact.ByteSize, &artifact.ManagedLocation, &artifactCreatedAt)
	if gormWorkspaceNoRows(err) {
		return domain.SourceMaterial{}, foundation.NewError(foundation.ErrorNotFound, "SOURCE_VERSION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.SourceMaterial{}, classifyGORMWorkspace(ctx, err, "SOURCE_MATERIAL_QUERY_FAILED")
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
	version.ID, version.SourceID, version.ContentArtifactID = parsedVersionID, parsedVersionSourceID, parsedVersionArtifactID
	artifact.ID, artifact.WorkspaceID, artifact.CreatedAt = parsedArtifactID, parsedArtifactWorkspaceID, artifactCreatedAt.Time
	if parsedVersionID != sourceVersionID || parsedVersionSourceID != parsedSourceID || parsedSourceWorkspaceID != parsedWorkspaceID || parsedVersionArtifactID != parsedArtifactID || parsedArtifactWorkspaceID != parsedWorkspaceID {
		return domain.SourceMaterial{}, foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_MATERIAL_SCOPE_INVALID", false, errors.New("source material crosses workspace or source scope"))
	}
	if version.ContentHash != artifact.ContentHash || version.ByteSize != artifact.ByteSize {
		return domain.SourceMaterial{}, foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_MATERIAL_METADATA_CONFLICT", false, errors.New("source version and content artifact metadata differ"))
	}
	authorizedWorkspace, err := repository.gormAuthorizeWorkspace(ctx, domain.Workspace{ID: parsedWorkspaceID, RootPath: workspaceRootPath})
	if err != nil {
		return domain.SourceMaterial{}, err
	}
	return domain.SourceMaterial{WorkspaceID: parsedWorkspaceID, WorkspaceRootPath: authorizedWorkspace.RootPath, SourceID: parsedSourceID, SourceVersion: version, ContentArtifact: artifact}, nil
}

func (repository *GORMRepository) gormAuthorizeWorkspace(ctx context.Context, workspace domain.Workspace) (domain.Workspace, error) {
	if repository.grants == nil {
		if repository.managed {
			return domain.Workspace{}, foundation.NewError(foundation.ErrorDependencyUnavailable, rootgrant.ErrorCodeGrantStale, false, errors.New("managed workspace root grant resolver is unavailable"))
		}
		return workspace, nil
	}
	capability, err := repository.grants.Resolve(ctx, workspace.ID)
	if err != nil {
		return domain.Workspace{}, err
	}
	if capability == nil {
		return domain.Workspace{}, foundation.NewError(foundation.ErrorConsistencyViolation, rootgrant.ErrorCodeGrantStale, false, errors.New("workspace root grant resolver returned no capability"))
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

var _ domain.Repository = (*GORMRepository)(nil)
var _ domain.ActiveWorkspaceRepository = (*GORMRepository)(nil)
var _ domain.SourceMaterialRepository = (*GORMRepository)(nil)
var _ domain.SourceVersionListRepository = (*GORMRepository)(nil)
