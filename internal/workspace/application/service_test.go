package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const testWorkspaceID foundation.ID = "123e4567-e89b-42d3-a456-426614174000"

func TestServiceCreateWorkspace(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	repository := &fakeRepository{}
	files := &fakeFileScanner{canonicalRoot: "/canonical/workspace"}
	git := &fakeGitStatusReader{status: domain.GitStatus{
		Present:        true,
		RepositoryPath: "/canonical/workspace",
		Branch:         "dev",
		Head:           "abc123",
	}}
	ids := &fakeIDGenerator{id: testWorkspaceID}
	service := NewService(Dependencies{
		Repository: repository,
		Files:      files,
		Git:        git,
		IDs:        ids,
		Clock:      foundation.FixedClock{Value: now},
	})

	result, err := service.CreateWorkspace(context.Background(), CreateWorkspaceRequest{
		Name:     "  Product Workspace  ",
		RootPath: "/input/workspace",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}

	wantTime := now.UTC()
	if repository.createCalls != 1 {
		t.Fatalf("CreateWorkspace repository calls = %d, want 1", repository.createCalls)
	}
	if !reflect.DeepEqual(files.canonicalPaths, []string{"/input/workspace"}) {
		t.Fatalf("CanonicalRoot paths = %#v", files.canonicalPaths)
	}
	if !reflect.DeepEqual(git.roots, []string{"/canonical/workspace"}) {
		t.Fatalf("Git status roots = %#v", git.roots)
	}
	if ids.calls != 1 {
		t.Fatalf("ID generator calls = %d, want 1", ids.calls)
	}
	if result.Workspace.ID != testWorkspaceID || result.Workspace.Name != "Product Workspace" || result.Workspace.RootPath != "/canonical/workspace" {
		t.Fatalf("Workspace = %#v", result.Workspace)
	}
	if result.Workspace.Status != domain.WorkspaceStatusActive || result.Workspace.Version != 1 {
		t.Fatalf("Workspace status/version = %q/%d", result.Workspace.Status, result.Workspace.Version)
	}
	if !result.Workspace.CreatedAt.Equal(wantTime) || !result.Workspace.UpdatedAt.Equal(wantTime) || !result.Workspace.Git.CheckedAt.Equal(wantTime) {
		t.Fatalf("Workspace timestamps = %#v", result.Workspace)
	}
	if result.Workspace.Git.RepositoryPath != git.status.RepositoryPath || result.Workspace.Git.Branch != git.status.Branch || result.Workspace.Git.Head != git.status.Head || result.Workspace.Git.Dirty {
		t.Fatalf("Git baseline = %#v", result.Workspace.Git)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("Warnings = %#v, want none", result.Warnings)
	}
	if !reflect.DeepEqual(repository.created, result.Workspace) {
		t.Fatalf("Persisted workspace = %#v, result = %#v", repository.created, result.Workspace)
	}
}

func TestServiceCreateWorkspaceRejectsDuplicateRoot(t *testing.T) {
	repository := &fakeRepository{roots: []string{"/canonical/workspace"}}
	git := &fakeGitStatusReader{status: domain.GitStatus{Present: true}}
	service := newTestService(repository, &fakeFileScanner{canonicalRoot: "/canonical/workspace"}, git)

	_, err := service.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Name: "Workspace", RootPath: "/canonical/workspace"})
	requireClassifiedError(t, err, foundation.ErrorVersionConflict, "WORKSPACE_ALREADY_EXISTS")
	if git.calls != 0 || repository.createCalls != 0 {
		t.Fatalf("rejected duplicate reached side effects: git calls=%d create calls=%d", git.calls, repository.createCalls)
	}
}

func TestServiceCreateWorkspaceRejectsNestedRoot(t *testing.T) {
	tests := []struct {
		name      string
		candidate string
		existing  string
	}{
		{name: "candidate inside existing", candidate: "/workspace/child", existing: "/workspace"},
		{name: "existing inside candidate", candidate: "/workspace", existing: "/workspace/child"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeRepository{roots: []string{test.existing}}
			git := &fakeGitStatusReader{status: domain.GitStatus{Present: true}}
			service := newTestService(repository, &fakeFileScanner{canonicalRoot: test.candidate}, git)

			_, err := service.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Name: "Workspace", RootPath: test.candidate})
			requireClassifiedError(t, err, foundation.ErrorInvalidInput, "WORKSPACE_ROOT_NESTED")
			if git.calls != 0 || repository.createCalls != 0 {
				t.Fatalf("rejected nested root reached side effects: git calls=%d create calls=%d", git.calls, repository.createCalls)
			}
		})
	}
}

func TestServiceCreateWorkspaceRejectsMissingGitWhenInitializationDeclined(t *testing.T) {
	repository := &fakeRepository{}
	service := newTestService(repository, &fakeFileScanner{canonicalRoot: "/workspace"}, &fakeGitStatusReader{})

	_, err := service.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Name: "Workspace", RootPath: "/workspace"})
	requireClassifiedError(t, err, foundation.ErrorInvalidInput, "GIT_REQUIRED")
	if repository.createCalls != 0 {
		t.Fatalf("repository create calls = %d, want 0", repository.createCalls)
	}
}

func TestServiceCreateWorkspaceReportsDirtyGitWarning(t *testing.T) {
	git := &fakeGitStatusReader{status: domain.GitStatus{
		Present:        true,
		RepositoryPath: "/workspace",
		Branch:         "dev",
		Head:           "abc123",
		Dirty:          true,
	}}
	service := newTestService(&fakeRepository{}, &fakeFileScanner{canonicalRoot: "/workspace"}, git)

	result, err := service.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Name: "Workspace", RootPath: "/workspace"})
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	if !result.Workspace.Git.Dirty || !reflect.DeepEqual(result.Warnings, []string{WarningGitDirty}) {
		t.Fatalf("dirty result = %#v", result)
	}
}

func TestServiceOpenWorkspace(t *testing.T) {
	workspace := domain.Workspace{ID: testWorkspaceID, Name: "Workspace", RootPath: "/canonical/workspace"}
	repository := &fakeRepository{workspace: workspace}
	files := &fakeFileScanner{canonicalRoot: workspace.RootPath}
	git := &fakeGitStatusReader{status: domain.GitStatus{Present: true, RepositoryPath: workspace.RootPath, Dirty: true}}
	service := newTestService(repository, files, git)

	result, err := service.OpenWorkspace(context.Background(), "/input/workspace")
	if err != nil {
		t.Fatalf("OpenWorkspace() error = %v", err)
	}
	if repository.requestedRoot != workspace.RootPath || !reflect.DeepEqual(git.roots, []string{workspace.RootPath}) {
		t.Fatalf("open roots: repository=%q git=%#v", repository.requestedRoot, git.roots)
	}
	if result.Workspace.ID != testWorkspaceID || !reflect.DeepEqual(result.Warnings, []string{WarningGitDirty}) {
		t.Fatalf("OpenWorkspace() result = %#v", result)
	}
}

func TestServiceScanWorkspace(t *testing.T) {
	files := []domain.ScannedFile{
		{RelativePath: "a.md", ByteSize: 12, ContentHash: "hash-a", MediaType: "text/markdown"},
		{RelativePath: "nested/b.txt", ByteSize: 8, ContentHash: "hash-b", MediaType: "text/plain"},
	}
	repository := &fakeRepository{workspace: domain.Workspace{ID: testWorkspaceID, RootPath: "/workspace"}}
	scanner := &fakeFileScanner{scanFiles: files}
	service := NewService(Dependencies{
		Repository: repository,
		Files:      scanner,
		IDs:        &fakeIDGenerator{id: testWorkspaceID},
		Clock:      foundation.FixedClock{Value: time.Date(2026, time.July, 16, 4, 30, 0, 0, time.UTC)},
	})

	result, err := service.ScanWorkspace(context.Background(), testWorkspaceID)
	if err != nil {
		t.Fatalf("ScanWorkspace() error = %v", err)
	}
	if !reflect.DeepEqual(result, files) {
		t.Fatalf("ScanWorkspace() = %#v, want %#v", result, files)
	}
	if !reflect.DeepEqual(scanner.scanRoots, []string{"/workspace"}) {
		t.Fatalf("Scan roots = %#v", scanner.scanRoots)
	}
	if len(repository.registrations) != len(files) {
		t.Fatalf("registrations = %#v", repository.registrations)
	}
	if repository.registrations[0].Source.OriginalLocation != "a.md" || repository.registrations[0].Version.ContentHash != "hash-a" {
		t.Fatalf("first registration = %#v", repository.registrations[0])
	}
}

func newTestService(repository domain.Repository, files domain.FileScanner, git domain.GitStatusReader) *Service {
	return NewService(Dependencies{
		Repository: repository,
		Files:      files,
		Git:        git,
		IDs:        &fakeIDGenerator{id: testWorkspaceID},
		Clock:      foundation.FixedClock{Value: time.Date(2026, time.July, 16, 4, 30, 0, 0, time.UTC)},
	})
}

func requireClassifiedError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error = %v, want classified error", err)
	}
	if classified.Kind != kind || classified.Code != code || classified.Retryable {
		t.Fatalf("classified error = %#v, want kind=%q code=%q retryable=false", classified, kind, code)
	}
}

type fakeRepository struct {
	roots         []string
	workspace     domain.Workspace
	created       domain.Workspace
	requestedRoot string
	createCalls   int
	registrations []domain.SourceRegistration
}

func (f *fakeRepository) CreateWorkspace(_ context.Context, workspace domain.Workspace) (domain.Workspace, error) {
	f.createCalls++
	f.created = workspace
	f.workspace = workspace
	return workspace, nil
}

func (f *fakeRepository) GetWorkspaceByID(_ context.Context, _ foundation.ID) (domain.Workspace, error) {
	return f.workspace, nil
}

func (f *fakeRepository) GetWorkspaceByRootPath(_ context.Context, rootPath string) (domain.Workspace, error) {
	f.requestedRoot = rootPath
	return f.workspace, nil
}

func (f *fakeRepository) ListWorkspaceRoots(context.Context) ([]string, error) {
	return append([]string(nil), f.roots...), nil
}

func (f *fakeRepository) RegisterSourceVersion(context.Context, domain.SourceRegistration) (domain.SourceRegistrationResult, error) {
	return domain.SourceRegistrationResult{}, nil
}

func (f *fakeRepository) RegisterSourceVersions(_ context.Context, registrations []domain.SourceRegistration) ([]domain.SourceRegistrationResult, error) {
	f.registrations = append(f.registrations, registrations...)
	return make([]domain.SourceRegistrationResult, len(registrations)), nil
}

type fakeFileScanner struct {
	canonicalRoot  string
	canonicalPaths []string
	scanFiles      []domain.ScannedFile
	scanRoots      []string
}

func (f *fakeFileScanner) CanonicalRoot(path string) (string, error) {
	f.canonicalPaths = append(f.canonicalPaths, path)
	return f.canonicalRoot, nil
}

func (f *fakeFileScanner) Scan(_ context.Context, rootPath string) ([]domain.ScannedFile, error) {
	f.scanRoots = append(f.scanRoots, rootPath)
	return append([]domain.ScannedFile(nil), f.scanFiles...), nil
}

type fakeGitStatusReader struct {
	status domain.GitStatus
	roots  []string
	calls  int
}

func (f *fakeGitStatusReader) Status(_ context.Context, rootPath string) (domain.GitStatus, error) {
	f.calls++
	f.roots = append(f.roots, rootPath)
	return f.status, nil
}

type fakeIDGenerator struct {
	id    foundation.ID
	calls int
}

func (f *fakeIDGenerator) New() (foundation.ID, error) {
	f.calls++
	return f.id, nil
}

var (
	_ domain.Repository      = (*fakeRepository)(nil)
	_ domain.FileScanner     = (*fakeFileScanner)(nil)
	_ domain.GitStatusReader = (*fakeGitStatusReader)(nil)
	_ foundation.IDGenerator = (*fakeIDGenerator)(nil)
)
