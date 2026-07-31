package application

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const WarningGitDirty = "GIT_WORKTREE_DIRTY"

// Dependencies are the explicit ports required by Workspace use cases.
type Dependencies struct {
	Repository     domain.Repository
	Files          domain.FileScanner
	CommittedFiles domain.CommittedContentStore
	CommittedGit   domain.CommittedBlobReader
	Git            domain.GitStatusReader
	GitInitializer domain.GitInitializer
	IDs            foundation.IDGenerator
	Clock          foundation.Clock
}

// Service coordinates Workspace creation, opening and read-only scanning.
type Service struct{ dependencies Dependencies }

// ListSourceVersions 返回 Inbox 需要的 Source Version 摘要页。
func (s *Service) ListSourceVersions(ctx context.Context, query domain.SourceVersionListQuery) ([]domain.SourceVersionListItem, bool, error) {
	if query.WorkspaceID == "" || query.Limit < 1 || query.Limit > 100 {
		return nil, false, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_LIST_INVALID", false, errors.New("invalid source version list scope"))
	}
	repository, ok := s.dependencies.Repository.(domain.SourceVersionListRepository)
	if !ok {
		return nil, false, foundation.NewError(foundation.ErrorDependencyUnavailable, "SOURCE_VERSION_LIST_UNAVAILABLE", true, errors.New("source version list repository is unavailable"))
	}
	return repository.ListSourceVersions(ctx, query)
}

// NewService creates a Workspace application service.
func NewService(dependencies Dependencies) *Service {
	return &Service{dependencies: dependencies}
}

// CreateWorkspaceRequest contains user choices required by Workspace creation.
type CreateWorkspaceRequest struct {
	Name          string
	RootPath      string
	InitializeGit bool
}

// WorkspaceResult includes the persisted Workspace and current Git warnings.
type WorkspaceResult struct {
	Workspace domain.Workspace
	Git       domain.GitStatus
	Warnings  []string
}

type rootSelectionAuthorizer interface {
	AuthorizeRootSelection(context.Context) error
}

// CreateWorkspace validates boundaries and persists one active Workspace.
func (s *Service) CreateWorkspace(ctx context.Context, request CreateWorkspaceRequest) (WorkspaceResult, error) {
	if s == nil || s.dependencies.Repository == nil || s.dependencies.Files == nil || s.dependencies.Git == nil || s.dependencies.IDs == nil || s.dependencies.Clock == nil {
		return WorkspaceResult{}, dependencyError("WORKSPACE_SERVICE_UNAVAILABLE")
	}
	if err := authorizeRootSelection(ctx, s.dependencies.Repository); err != nil {
		return WorkspaceResult{}, err
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return WorkspaceResult{}, invalidError("WORKSPACE_NAME_REQUIRED")
	}
	rootPath, err := s.dependencies.Files.CanonicalRoot(request.RootPath)
	if err != nil {
		return WorkspaceResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WORKSPACE_ROOT_INVALID", false, err)
	}
	configuredRoots, err := s.dependencies.Repository.ListWorkspaceRoots(ctx)
	if err != nil {
		return WorkspaceResult{}, err
	}
	if err := validateAvailableRoot(rootPath, configuredRoots); err != nil {
		return WorkspaceResult{}, err
	}
	gitStatus, err := s.dependencies.Git.Status(ctx, rootPath)
	if err != nil {
		return WorkspaceResult{}, err
	}
	action, err := DecideGitSetup(gitStatus, request.InitializeGit)
	if err != nil {
		return WorkspaceResult{}, err
	}
	if action == GitSetupInitialize {
		if s.dependencies.GitInitializer == nil {
			return WorkspaceResult{}, dependencyError("GIT_INITIALIZER_UNAVAILABLE")
		}
		gitStatus, err = s.dependencies.GitInitializer.Initialize(ctx, rootPath)
		if err != nil {
			return WorkspaceResult{}, err
		}
		if !gitStatus.Present {
			return WorkspaceResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "GIT_INITIALIZATION_INCOMPLETE", false, errors.New("git initializer returned no repository"))
		}
	}
	id, err := s.dependencies.IDs.New()
	if err != nil {
		return WorkspaceResult{}, err
	}
	now := s.dependencies.Clock.Now()
	workspace, err := s.dependencies.Repository.CreateWorkspace(ctx, domain.Workspace{
		ID:       id,
		Name:     name,
		RootPath: rootPath,
		Git: domain.GitBaseline{
			RepositoryPath: gitStatus.RepositoryPath,
			Branch:         gitStatus.Branch,
			Head:           gitStatus.Head,
			Dirty:          gitStatus.Dirty,
			CheckedAt:      now,
		},
		Status:    domain.WorkspaceStatusActive,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return WorkspaceResult{}, err
	}
	return result(workspace, gitStatus), nil
}

// OpenWorkspace reloads a persisted Workspace by its canonical root and reads
// the current Git status without modifying user files.
func (s *Service) OpenWorkspace(ctx context.Context, root string) (WorkspaceResult, error) {
	if s == nil || s.dependencies.Repository == nil || s.dependencies.Files == nil || s.dependencies.Git == nil {
		return WorkspaceResult{}, dependencyError("WORKSPACE_SERVICE_UNAVAILABLE")
	}
	if err := authorizeRootSelection(ctx, s.dependencies.Repository); err != nil {
		return WorkspaceResult{}, err
	}
	rootPath, err := s.dependencies.Files.CanonicalRoot(root)
	if err != nil {
		return WorkspaceResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WORKSPACE_ROOT_INVALID", false, err)
	}
	workspace, err := s.dependencies.Repository.GetWorkspaceByRootPath(ctx, rootPath)
	if err != nil {
		return WorkspaceResult{}, err
	}
	gitStatus, err := s.dependencies.Git.Status(ctx, rootPath)
	if err != nil {
		return WorkspaceResult{}, err
	}
	if !gitStatus.Present {
		return WorkspaceResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "GIT_REPOSITORY_MISSING", false, errors.New("persisted workspace repository is missing"))
	}
	return result(workspace, gitStatus), nil
}

func authorizeRootSelection(ctx context.Context, repository domain.Repository) error {
	authorizer, ok := repository.(rootSelectionAuthorizer)
	if !ok {
		return nil
	}
	return authorizer.AuthorizeRootSelection(ctx)
}

// GetWorkspace returns a persisted Workspace and refreshes its read-only Git
// status before returning it to the caller.
func (s *Service) GetWorkspace(ctx context.Context, workspaceID foundation.ID) (WorkspaceResult, error) {
	if s == nil || s.dependencies.Repository == nil || s.dependencies.Git == nil {
		return WorkspaceResult{}, dependencyError("WORKSPACE_SERVICE_UNAVAILABLE")
	}
	workspace, err := s.dependencies.Repository.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return WorkspaceResult{}, err
	}
	gitStatus, err := s.dependencies.Git.Status(ctx, workspace.RootPath)
	if err != nil {
		return WorkspaceResult{}, err
	}
	if !gitStatus.Present {
		return WorkspaceResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "GIT_REPOSITORY_MISSING", false, errors.New("persisted workspace repository is missing"))
	}
	return result(workspace, gitStatus), nil
}

// ScanWorkspace safely captures immutable content and atomically registers
// Source, ContentArtifact and SourceVersion metadata. It does not parse or index files.
func (s *Service) ScanWorkspace(ctx context.Context, workspaceID foundation.ID) ([]domain.ScannedFile, error) {
	if s == nil || s.dependencies.Repository == nil || s.dependencies.Files == nil || s.dependencies.IDs == nil || s.dependencies.Clock == nil {
		return nil, dependencyError("WORKSPACE_SERVICE_UNAVAILABLE")
	}
	workspace, err := s.dependencies.Repository.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	files, err := s.dependencies.Files.Scan(ctx, workspace.RootPath)
	if err != nil {
		return nil, err
	}
	registrations := make([]domain.SourceRegistration, 0, len(files))
	capturedAt := s.dependencies.Clock.Now()
	for _, file := range files {
		capture, err := s.dependencies.Files.Capture(ctx, workspace.RootPath, file)
		if err != nil {
			return nil, err
		}
		sourceID, err := s.dependencies.IDs.New()
		if err != nil {
			return nil, err
		}
		versionID, err := s.dependencies.IDs.New()
		if err != nil {
			return nil, err
		}
		artifactID, err := s.dependencies.IDs.New()
		if err != nil {
			return nil, err
		}
		registrations = append(registrations, domain.SourceRegistration{
			Source: domain.Source{
				ID: sourceID, WorkspaceID: workspace.ID, Type: sourceType(file.MediaType),
				LogicalName: filepath.Base(file.RelativePath), OriginalLocation: file.RelativePath, CreatedAt: capturedAt,
			},
			Artifact: domain.ContentArtifact{
				ID: artifactID, WorkspaceID: workspace.ID, ContentHash: capture.ContentHash,
				ByteSize: capture.ByteSize, ManagedLocation: capture.ManagedLocation, CreatedAt: capturedAt,
			},
			Version: domain.SourceVersion{
				ID: versionID, ContentArtifactID: artifactID, ContentHash: file.ContentHash, ByteSize: file.ByteSize,
				MediaType: file.MediaType, OriginalContentLocation: file.RelativePath,
				SecurityStatus: "pending", CapturedAt: capturedAt,
			},
		})
	}
	results, err := s.dependencies.Repository.RegisterSourceVersions(ctx, registrations)
	if err != nil {
		return nil, err
	}
	if len(results) != len(files) {
		return nil, foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_VERSION_RESULT_INCOMPLETE", false, errors.New("repository returned an incomplete registration result"))
	}
	for index := range files {
		files[index].SourceID = results[index].Source.ID
		files[index].SourceVersionID = results[index].Version.ID
		files[index].ContentArtifactID = results[index].Artifact.ID
		files[index].ContentArtifactCreated = results[index].ArtifactCreated
	}
	return files, nil
}

func sourceType(mediaType string) string {
	switch mediaType {
	case "text/markdown":
		return "markdown"
	case "text/plain":
		return "text"
	case "application/pdf":
		return "pdf"
	case "text/html":
		return "html"
	default:
		return "file"
	}
}

func validateAvailableRoot(candidate string, configured []string) error {
	for _, existing := range configured {
		if candidate == existing {
			return foundation.NewError(foundation.ErrorVersionConflict, "WORKSPACE_ALREADY_EXISTS", false, errors.New("workspace root is already configured"))
		}
	}
	return nil
}

func result(workspace domain.Workspace, gitStatus domain.GitStatus) WorkspaceResult {
	warnings := []string(nil)
	if gitStatus.Dirty {
		warnings = append(warnings, WarningGitDirty)
	}
	return WorkspaceResult{Workspace: workspace, Git: gitStatus, Warnings: warnings}
}

func invalidError(code string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New("invalid workspace input"))
}

func dependencyError(code string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, false, errors.New("workspace dependency is unavailable"))
}
