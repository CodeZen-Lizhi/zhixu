package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
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

func TestServiceManagedRootSelectionFailsBeforeFilesystemOrGitAccess(t *testing.T) {
	repository := &deniedRootSelectionRepository{fakeRepository: &fakeRepository{}}
	files := &fakeFileScanner{canonicalRoot: "/container/private"}
	git := &fakeGitStatusReader{status: domain.GitStatus{Present: true}}
	service := newTestService(repository, files, git)

	for _, invoke := range []func() error{
		func() error {
			_, err := service.CreateWorkspace(context.Background(), CreateWorkspaceRequest{
				Name: "Forbidden", RootPath: "/container/private", InitializeGit: true,
			})
			return err
		},
		func() error {
			_, err := service.OpenWorkspace(context.Background(), "/container/private")
			return err
		},
	} {
		requireClassifiedError(t, invoke(), foundation.ErrorPermissionDenied, "WORKSPACE_ROOT_NOT_GRANTED")
	}
	if len(files.canonicalPaths) != 0 || git.calls != 0 || repository.createCalls != 0 || repository.requestedRoot != "" {
		t.Fatalf("managed root selection reached side effects: paths=%v git=%d create=%d root=%q",
			files.canonicalPaths, git.calls, repository.createCalls, repository.requestedRoot)
	}
}

func TestCaptureCommittedSourceVersionRegistersExactCommitBytes(t *testing.T) {
	now := time.Date(2026, time.July, 18, 3, 0, 0, 0, time.UTC)
	content := []byte("# committed\n")
	hash := "f5f02ed4eafb1ac662a6d59553b89bfebe6db2f3c9c0ea7d1e5ca558e9ee8572"
	workspace := domain.Workspace{ID: testWorkspaceID, RootPath: "/workspace"}
	repository := &fakeRepository{workspace: workspace}
	git := &fakeCommittedBlobReader{blob: domain.CommittedBlob{
		WorkspaceID: testWorkspaceID, Commit: strings.Repeat("a", 40), RelativePath: "notes/a.md", Bytes: content,
	}}
	store := &fakeCommittedContentStore{capture: domain.ContentCapture{ContentHash: hash, ByteSize: int64(len(content)), ManagedLocation: ".knowledge/sources/" + hash, Created: true}}
	ids := &sequenceIDGenerator{ids: []foundation.ID{
		"91000000-0000-4000-8000-000000000001",
		"92000000-0000-4000-8000-000000000001",
		"93000000-0000-4000-8000-000000000001",
	}}
	service := NewService(Dependencies{
		Repository: repository, CommittedGit: git, CommittedFiles: store, IDs: ids,
		Clock: foundation.FixedClock{Value: now},
	})

	result, err := service.CaptureCommittedSourceVersion(context.Background(), CaptureCommittedSourceRequest{
		WorkspaceID: testWorkspaceID, GitCommit: strings.Repeat("a", 40), RelativePath: "notes/a.md", ExpectedHash: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if git.requestedCommit != strings.Repeat("a", 40) || git.requestedPath != "notes/a.md" {
		t.Fatalf("git request=%q %q", git.requestedCommit, git.requestedPath)
	}
	if store.root != "/workspace" || string(store.content) != string(content) || store.expectedHash != hash {
		t.Fatalf("content capture=%#v", store)
	}
	if result.Source.ID != "91000000-0000-4000-8000-000000000001" || result.Version.ID != "92000000-0000-4000-8000-000000000001" || result.Artifact.ID != "93000000-0000-4000-8000-000000000001" {
		t.Fatalf("result=%#v", result)
	}
	if repository.registration.Source.OriginalLocation != "notes/a.md" || repository.registration.Version.ContentHash != hash || repository.registration.Version.MediaType != "text/markdown" || !repository.registration.Version.CapturedAt.Equal(now) {
		t.Fatalf("registration=%#v", repository.registration)
	}
}

func TestCaptureCommittedSourceVersionRejectsHashMismatchBeforePublishing(t *testing.T) {
	workspace := domain.Workspace{ID: testWorkspaceID, RootPath: "/workspace"}
	repository := &fakeRepository{workspace: workspace}
	git := &fakeCommittedBlobReader{blob: domain.CommittedBlob{
		WorkspaceID: testWorkspaceID, Commit: strings.Repeat("a", 40), RelativePath: "notes/a.md", Bytes: []byte("different"),
	}}
	store := &fakeCommittedContentStore{}
	service := NewService(Dependencies{
		Repository: repository, CommittedGit: git, CommittedFiles: store,
		IDs:   &sequenceIDGenerator{ids: []foundation.ID{"91000000-0000-4000-8000-000000000001"}},
		Clock: foundation.FixedClock{Value: time.Now().UTC()},
	})
	_, err := service.CaptureCommittedSourceVersion(context.Background(), CaptureCommittedSourceRequest{
		WorkspaceID: testWorkspaceID, GitCommit: strings.Repeat("a", 40), RelativePath: "notes/a.md", ExpectedHash: strings.Repeat("b", 64),
	})
	if err == nil || store.calls != 0 || repository.registerCalls != 0 {
		t.Fatalf("err=%v store_calls=%d register_calls=%d", err, store.calls, repository.registerCalls)
	}
}

func TestPrepareCommittedSourceVersionDefersDatabaseRegistration(t *testing.T) {
	content := []byte("<p>committed</p>")
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	repository := &fakeRepository{workspace: domain.Workspace{ID: testWorkspaceID, RootPath: "/workspace"}}
	git := &fakeCommittedBlobReader{blob: domain.CommittedBlob{
		WorkspaceID: testWorkspaceID, Commit: strings.Repeat("a", 40), RelativePath: "notes/a.html", Bytes: content,
	}}
	store := &fakeCommittedContentStore{capture: domain.ContentCapture{
		ContentHash: hash, ByteSize: int64(len(content)), ManagedLocation: ".knowledge/sources/" + hash,
	}}
	service := NewService(Dependencies{
		Repository: repository, CommittedGit: git, CommittedFiles: store,
		IDs: &sequenceIDGenerator{ids: []foundation.ID{
			"91000000-0000-4000-8000-000000000001",
			"92000000-0000-4000-8000-000000000001",
			"93000000-0000-4000-8000-000000000001",
		}},
		Clock: foundation.FixedClock{Value: time.Date(2026, time.August, 3, 1, 0, 0, 0, time.UTC)},
	})

	registration, err := service.PrepareCommittedSourceVersion(context.Background(), CaptureCommittedSourceRequest{
		WorkspaceID: testWorkspaceID, GitCommit: strings.Repeat("a", 40), RelativePath: "notes/a.html", ExpectedHash: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repository.registerCalls != 0 || registration.Version.MediaType != "text/html" || registration.Version.ContentHash != hash {
		t.Fatalf("registration=%#v register_calls=%d", registration, repository.registerCalls)
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

func TestServiceCreateWorkspaceAllowsNestedRegistryRoot(t *testing.T) {
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

			result, err := service.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Name: "Workspace", RootPath: test.candidate})
			if err != nil {
				t.Fatalf("CreateWorkspace() error = %v", err)
			}
			if result.Workspace.RootPath != test.candidate || git.calls != 1 || repository.createCalls != 1 {
				t.Fatalf("nested registry result=%#v git calls=%d create calls=%d", result, git.calls, repository.createCalls)
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
		{RelativePath: "a.md", ByteSize: 12, ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MediaType: "text/markdown"},
		{RelativePath: "nested/b.txt", ByteSize: 8, ContentHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", MediaType: "text/plain"},
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
	if len(result) != 2 || result[0].SourceID == "" || result[0].SourceVersionID == "" || result[0].ContentArtifactID == "" || !result[0].ContentArtifactCreated {
		t.Fatalf("ScanWorkspace() = %#v", result)
	}
	if !reflect.DeepEqual(scanner.scanRoots, []string{"/workspace"}) {
		t.Fatalf("Scan roots = %#v", scanner.scanRoots)
	}
	if len(repository.registrations) != len(files) {
		t.Fatalf("registrations = %#v", repository.registrations)
	}
	if repository.registrations[0].Source.OriginalLocation != "a.md" || repository.registrations[0].Version.ContentHash != files[0].ContentHash || repository.registrations[0].Artifact.ManagedLocation == "" {
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
	roots          []string
	workspace      domain.Workspace
	created        domain.Workspace
	requestedRoot  string
	createCalls    int
	registrations  []domain.SourceRegistration
	registration   domain.SourceRegistration
	registerCalls  int
	registerResult domain.SourceRegistrationResult
}

type deniedRootSelectionRepository struct {
	*fakeRepository
}

func (*deniedRootSelectionRepository) AuthorizeRootSelection(context.Context) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, "WORKSPACE_ROOT_NOT_GRANTED", false, errors.New("managed root selection denied"))
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

func (f *fakeRepository) RegisterSourceVersion(_ context.Context, registration domain.SourceRegistration) (domain.SourceRegistrationResult, error) {
	f.registerCalls++
	f.registration = registration
	if f.registerResult.Source.ID != "" {
		return f.registerResult, nil
	}
	return domain.SourceRegistrationResult{Source: registration.Source, Artifact: registration.Artifact, Version: registration.Version, ArtifactCreated: true, Created: true}, nil
}

func (f *fakeRepository) RegisterSourceVersions(_ context.Context, registrations []domain.SourceRegistration) ([]domain.SourceRegistrationResult, error) {
	f.registrations = append(f.registrations, registrations...)
	results := make([]domain.SourceRegistrationResult, len(registrations))
	for index, registration := range registrations {
		results[index] = domain.SourceRegistrationResult{Source: registration.Source, Artifact: registration.Artifact, Version: registration.Version, ArtifactCreated: true, Created: true}
	}
	return results, nil
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

func (f *fakeFileScanner) Capture(_ context.Context, _ string, file domain.ScannedFile) (domain.ContentCapture, error) {
	return domain.ContentCapture{ContentHash: file.ContentHash, ByteSize: file.ByteSize, ManagedLocation: ".knowledge/sources/" + file.ContentHash, Created: true}, nil
}

func (f *fakeFileScanner) ReadArtifact(context.Context, string, domain.ContentArtifact) ([]byte, error) {
	return nil, nil
}

type fakeGitStatusReader struct {
	status domain.GitStatus
	roots  []string
	calls  int
}

type fakeCommittedBlobReader struct {
	blob            domain.CommittedBlob
	requestedCommit string
	requestedPath   string
}

func (f *fakeCommittedBlobReader) ReadCommittedBlob(_ context.Context, _ foundation.ID, commit, relativePath string) (domain.CommittedBlob, error) {
	f.requestedCommit = commit
	f.requestedPath = relativePath
	return f.blob, nil
}

type fakeCommittedContentStore struct {
	capture      domain.ContentCapture
	root         string
	relativePath string
	content      []byte
	expectedHash string
	calls        int
}

func (f *fakeCommittedContentStore) CaptureCommitted(_ context.Context, rootPath, relativePath string, content []byte, expectedHash string) (domain.ContentCapture, error) {
	f.calls++
	f.root = rootPath
	f.relativePath = relativePath
	f.content = append([]byte(nil), content...)
	f.expectedHash = expectedHash
	return f.capture, nil
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

type sequenceIDGenerator struct {
	ids []foundation.ID
	n   int
}

func (g *sequenceIDGenerator) New() (foundation.ID, error) {
	if g.n >= len(g.ids) {
		return "", errors.New("no id available")
	}
	id := g.ids[g.n]
	g.n++
	return id, nil
}

func (f *fakeIDGenerator) New() (foundation.ID, error) {
	f.calls++
	return f.id, nil
}

var (
	_ domain.Repository            = (*fakeRepository)(nil)
	_ domain.FileScanner           = (*fakeFileScanner)(nil)
	_ domain.GitStatusReader       = (*fakeGitStatusReader)(nil)
	_ domain.CommittedBlobReader   = (*fakeCommittedBlobReader)(nil)
	_ domain.CommittedContentStore = (*fakeCommittedContentStore)(nil)
	_ foundation.IDGenerator       = (*fakeIDGenerator)(nil)
	_ foundation.IDGenerator       = (*sequenceIDGenerator)(nil)
)
