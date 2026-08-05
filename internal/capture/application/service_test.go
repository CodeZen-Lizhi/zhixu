package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	workspaceapp "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

var captureTestWorkspaceID foundation.ID = "50000000-0000-4000-8000-000000000001"

func TestCreateTextStagesPersistsThenPublishesAtomicRecord(t *testing.T) {
	now := time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC)
	repository := &captureRepositoryFake{}
	content := &contentWriterFake{}
	service := newTestService(t, repository, content, now)
	result, err := service.CreateText(context.Background(), TextCommand{
		WorkspaceID: captureTestWorkspaceID, Text: " Java AI notes ", IdempotencyKey: "text-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Capture.Kind != domain.KindText || result.Capture.Status != domain.StatusSourceSaved || result.Capture.DisplayName != "Quick note" {
		t.Fatalf("capture = %#v", result.Capture)
	}
	if string(content.content) != " Java AI notes " || content.stageCalls != 1 || content.publishCalls != 1 ||
		repository.record.Registration == nil || repository.record.OutboxID == "" {
		t.Fatalf("content=%q record=%#v", content.content, repository.record)
	}
	if repository.record.Registration.Version.ContentHash != result.Capture.OriginalInputHash {
		t.Fatalf("version hash = %q, capture hash = %q", repository.record.Registration.Version.ContentHash, result.Capture.OriginalInputHash)
	}
	if repository.record.Registration.Version.MediaType != "text/plain" {
		t.Fatalf("version media type = %q", repository.record.Registration.Version.MediaType)
	}
}

func TestCreateURLPersistsSourceWithoutFetching(t *testing.T) {
	repository := &captureRepositoryFake{}
	content := &contentWriterFake{}
	service := newTestService(t, repository, content, time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC))
	result, err := service.CreateURL(context.Background(), URLCommand{
		WorkspaceID: captureTestWorkspaceID, URL: "https://example.com/java-ai", IdempotencyKey: "url-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Capture.OriginalURL == "" || result.Capture.LatestSourceVersionID != "" || repository.record.Registration != nil {
		t.Fatalf("capture=%#v record=%#v", result.Capture, repository.record)
	}
	if content.stageCalls != 0 || content.publishCalls != 0 {
		t.Fatalf("content writer stage=%d publish=%d", content.stageCalls, content.publishCalls)
	}
}

func TestCreateReplayRestagesAndPublishesToSelfHealContent(t *testing.T) {
	now := time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC)
	repository := &captureRepositoryFake{}
	content := &contentWriterFake{}
	service := newTestService(t, repository, content, now)
	first, err := service.CreateText(context.Background(), TextCommand{WorkspaceID: captureTestWorkspaceID, Text: "same", IdempotencyKey: "same-key"})
	if err != nil {
		t.Fatal(err)
	}
	repository.replay = first
	repository.replay.Capture = repository.record.Capture
	repository.replay.Replayed = true
	second, err := service.CreateText(context.Background(), TextCommand{WorkspaceID: captureTestWorkspaceID, Text: "same", IdempotencyKey: "same-key"})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || content.stageCalls != 2 || content.publishCalls != 2 {
		t.Fatalf("replayed=%v stage=%d publish=%d", second.Replayed, content.stageCalls, content.publishCalls)
	}
}

func TestCreateRepositoryRollbackDiscardsOnlyUnreferencedStage(t *testing.T) {
	repository := &captureRepositoryFake{createErr: foundation.NewError(
		foundation.ErrorDependencyUnavailable, "CAPTURE_OUTBOX_INSERT_FAILED", true, errors.New("rolled back before commit"),
	)}
	content := &contentWriterFake{}
	service := newTestService(t, repository, content, time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC))

	_, err := service.CreateText(context.Background(), TextCommand{
		WorkspaceID: captureTestWorkspaceID, Text: "rollback", IdempotencyKey: "rollback-key",
	})
	if captureApplicationErrorCode(err) != "CAPTURE_OUTBOX_INSERT_FAILED" {
		t.Fatalf("error=%#v", err)
	}
	if content.stageCalls != 1 || content.publishCalls != 0 || content.discardCalls != 1 {
		t.Fatalf("stage=%d publish=%d discard=%d", content.stageCalls, content.publishCalls, content.discardCalls)
	}
}

func TestCreateUnknownCommitWithoutVisibleReceiptKeepsRecoverableStage(t *testing.T) {
	repository := &captureRepositoryFake{createErr: foundation.NewError(
		foundation.ErrorDependencyUnavailable, "CAPTURE_COMMIT_FAILED", true, errors.New("commit outcome unknown"),
	)}
	content := &contentWriterFake{}
	service := newTestService(t, repository, content, time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC))

	_, err := service.CreateText(context.Background(), TextCommand{
		WorkspaceID: captureTestWorkspaceID, Text: "unknown", IdempotencyKey: "unknown-no-receipt",
	})
	if captureApplicationErrorCode(err) != "CAPTURE_CONTENT_FINALIZATION_UNKNOWN" {
		t.Fatalf("error=%#v", err)
	}
	if content.stageCalls != 1 || content.publishCalls != 0 || content.discardCalls != 0 {
		t.Fatalf("stage=%d publish=%d discard=%d", content.stageCalls, content.publishCalls, content.discardCalls)
	}
	repository.createErr = nil
	repository.replay = CreateResult{Capture: repository.record.Capture, Replayed: true}
	result, err := service.CreateText(context.Background(), TextCommand{
		WorkspaceID: captureTestWorkspaceID, Text: "unknown", IdempotencyKey: "unknown-no-receipt",
	})
	if err != nil || !result.Replayed || content.stageCalls != 2 || content.publishCalls != 1 || content.discardCalls != 0 {
		t.Fatalf("recovery result=%#v err=%v stage=%d publish=%d discard=%d", result, err, content.stageCalls, content.publishCalls, content.discardCalls)
	}
}

func TestCreateUnknownCommitQueriesReceiptBeforePublishing(t *testing.T) {
	repository := &captureRepositoryFake{
		createErr:           foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_COMMIT_FAILED", true, errors.New("response lost")),
		commitOnCreateError: true,
	}
	content := &contentWriterFake{}
	service := newTestService(t, repository, content, time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC))

	result, err := service.CreateText(context.Background(), TextCommand{
		WorkspaceID: captureTestWorkspaceID, Text: "committed", IdempotencyKey: "unknown-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replayed || repository.replayCalls != 2 || content.publishCalls != 1 || content.discardCalls != 0 {
		t.Fatalf("result=%#v replay=%d publish=%d discard=%d", result, repository.replayCalls, content.publishCalls, content.discardCalls)
	}
}

func TestCreateUnknownCommitRejectsMismatchedReceiptBeforePublishing(t *testing.T) {
	repository := &captureRepositoryFake{
		createErr:           foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_COMMIT_FAILED", true, errors.New("response lost")),
		commitOnCreateError: true,
		mutateCommittedReplay: func(result *CreateResult) {
			result.Capture.OriginalInputHash = strings.Repeat("f", 64)
		},
	}
	content := &contentWriterFake{}
	service := newTestService(t, repository, content, time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC))

	_, err := service.CreateText(context.Background(), TextCommand{
		WorkspaceID: captureTestWorkspaceID, Text: "committed", IdempotencyKey: "mismatched-receipt-key",
	})
	if captureApplicationErrorCode(err) != "CAPTURE_RESULT_INVALID" {
		t.Fatalf("error=%#v", err)
	}
	if content.stageCalls != 1 || content.publishCalls != 0 || content.discardCalls != 0 {
		t.Fatalf("stage=%d publish=%d discard=%d", content.stageCalls, content.publishCalls, content.discardCalls)
	}
}

func TestCreatePublishFailureLeavesStageForExactReplayHealing(t *testing.T) {
	repository := &captureRepositoryFake{}
	content := &contentWriterFake{publishErrors: []error{errors.New("publish interrupted")}}
	service := newTestService(t, repository, content, time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC))
	command := TextCommand{WorkspaceID: captureTestWorkspaceID, Text: "healable", IdempotencyKey: "heal-key"}

	if _, err := service.CreateText(context.Background(), command); captureApplicationErrorCode(err) != "CAPTURE_CONTENT_FINALIZATION_UNKNOWN" {
		t.Fatalf("first error=%#v", err)
	}
	repository.replay = CreateResult{Capture: repository.record.Capture, Replayed: true}
	result, err := service.CreateText(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replayed || content.stageCalls != 2 || content.publishCalls != 2 || content.discardCalls != 0 {
		t.Fatalf("result=%#v stage=%d publish=%d discard=%d", result, content.stageCalls, content.publishCalls, content.discardCalls)
	}
}

func TestCreateRollbackNeverPublishesRealContentArtifact(t *testing.T) {
	rootPath := captureFilesystemWorkspace(t)
	content := []byte("database rollback")
	hash := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hash[:])
	repository := &captureRepositoryFake{createErr: foundation.NewError(
		foundation.ErrorDependencyUnavailable, "CAPTURE_OUTBOX_INSERT_FAILED", true, errors.New("rolled back before commit"),
	)}
	service := newFilesystemBackedCaptureService(t, repository, rootPath)

	_, err := service.CreateText(context.Background(), TextCommand{
		WorkspaceID: captureTestWorkspaceID, Text: string(content), IdempotencyKey: "real-rollback",
	})
	if captureApplicationErrorCode(err) != "CAPTURE_OUTBOX_INSERT_FAILED" {
		t.Fatalf("error=%#v", err)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, ".knowledge", "sources", contentHash)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback published final artifact: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, ".knowledge", "sources", ".staging", contentHash)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback retained unreferenced stage: %v", err)
	}
}

func TestCreateUnknownCommitRealStageHealsWhenReceiptBecomesVisible(t *testing.T) {
	rootPath := captureFilesystemWorkspace(t)
	content := []byte("late receipt")
	hash := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hash[:])
	repository := &captureRepositoryFake{createErr: foundation.NewError(
		foundation.ErrorDependencyUnavailable, "CAPTURE_COMMIT_FAILED", true, errors.New("commit outcome unknown"),
	)}
	service := newFilesystemBackedCaptureService(t, repository, rootPath)
	command := TextCommand{WorkspaceID: captureTestWorkspaceID, Text: string(content), IdempotencyKey: "real-unknown"}

	if _, err := service.CreateText(context.Background(), command); captureApplicationErrorCode(err) != "CAPTURE_CONTENT_FINALIZATION_UNKNOWN" {
		t.Fatalf("first error=%#v", err)
	}
	finalPath := filepath.Join(rootPath, ".knowledge", "sources", contentHash)
	stagingDirectory := filepath.Join(rootPath, ".knowledge", "sources", ".staging", contentHash)
	if _, err := os.Lstat(finalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown commit published before confirmation: %v", err)
	}
	entries, err := os.ReadDir(stagingDirectory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging entries=%#v err=%v", entries, err)
	}
	repository.createErr = nil
	repository.replay = CreateResult{Capture: repository.record.Capture, Replayed: true}
	result, err := service.CreateText(context.Background(), command)
	if err != nil || !result.Replayed {
		t.Fatalf("replay result=%#v err=%v", result, err)
	}
	got, err := os.ReadFile(finalPath)
	if err != nil || string(got) != string(content) {
		t.Fatalf("final content=%q err=%v", got, err)
	}
	if _, err := os.Lstat(stagingDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replay left staging directory: %v", err)
	}
}

func TestCreateUnknownCommitWithoutReceiptReusesAndCleansStableStage(t *testing.T) {
	rootPath := captureFilesystemWorkspace(t)
	content := []byte("not committed on first attempt")
	hash := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hash[:])
	repository := &captureRepositoryFake{createErr: foundation.NewError(
		foundation.ErrorDependencyUnavailable, "CAPTURE_COMMIT_FAILED", true, errors.New("commit outcome unknown"),
	)}
	service := newFilesystemBackedCaptureService(t, repository, rootPath)
	command := TextCommand{WorkspaceID: captureTestWorkspaceID, Text: string(content), IdempotencyKey: "real-not-committed"}

	if _, err := service.CreateText(context.Background(), command); captureApplicationErrorCode(err) != "CAPTURE_CONTENT_FINALIZATION_UNKNOWN" {
		t.Fatalf("first error=%#v", err)
	}
	stagingDirectory := filepath.Join(rootPath, ".knowledge", "sources", ".staging", contentHash)
	entries, err := os.ReadDir(stagingDirectory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("first staging entries=%#v err=%v", entries, err)
	}
	firstStageName := entries[0].Name()

	repository.createErr = nil
	result, err := service.CreateText(context.Background(), command)
	if err != nil || result.Replayed {
		t.Fatalf("retry result=%#v err=%v", result, err)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, ".knowledge", "sources", contentHash)); err != nil {
		t.Fatalf("retry did not publish content: %v", err)
	}
	if _, err := os.Lstat(stagingDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry left stable stage %q: %v", firstStageName, err)
	}
}

func TestCreateUploadRejectsUnsupportedMediaType(t *testing.T) {
	service := newTestService(t, &captureRepositoryFake{}, &contentWriterFake{}, time.Now())
	_, err := service.CreateUpload(context.Background(), UploadCommand{
		WorkspaceID: captureTestWorkspaceID, Kind: domain.KindFile, FileName: "run.exe",
		MediaType: "application/octet-stream", Content: []byte("bytes"), IdempotencyKey: "file-1",
	})
	if captureApplicationErrorCode(err) != "CAPTURE_MEDIA_TYPE_UNSUPPORTED" {
		t.Fatalf("error = %#v", err)
	}
}

type sequenceIDs struct{ next byte }

func (generator *sequenceIDs) New() (foundation.ID, error) {
	generator.next++
	return foundation.ParseID("60000000-0000-4000-8000-0000000000" + string([]byte{'0' + generator.next/10, '0' + generator.next%10}))
}

type captureRepositoryFake struct {
	record                CreateRecord
	replay                CreateResult
	createErr             error
	commitOnCreateError   bool
	mutateCommittedReplay func(*CreateResult)
	replayCalls           int
}

func (repository *captureRepositoryFake) ReplayCreate(_ context.Context, binding CommandBinding) (CreateResult, bool, error) {
	repository.replayCalls++
	if repository.replay.Capture.ID == "" {
		return CreateResult{}, false, nil
	}
	return repository.replay, true, nil
}

func (repository *captureRepositoryFake) Create(_ context.Context, record CreateRecord) (CreateResult, error) {
	repository.record = record
	if repository.createErr != nil {
		if repository.commitOnCreateError {
			repository.replay = CreateResult{Capture: record.Capture, Replayed: true}
			if repository.mutateCommittedReplay != nil {
				repository.mutateCommittedReplay(&repository.replay)
			}
		}
		return CreateResult{}, repository.createErr
	}
	return CreateResult{Capture: record.Capture}, nil
}

func (*captureRepositoryFake) Get(context.Context, foundation.ID, foundation.ID) (domain.Capture, error) {
	return domain.Capture{}, errors.New("not implemented")
}

func (*captureRepositoryFake) List(context.Context, ListQuery) (Page, error) {
	return Page{}, errors.New("not implemented")
}

type contentWriterFake struct {
	stageCalls    int
	publishCalls  int
	discardCalls  int
	content       []byte
	publishErrors []error
	stageRefs     []string
}

func (writer *contentWriterFake) StageManagedBytes(_ context.Context, _ foundation.ID, sourceRef string, content []byte, hash string) (workspacedomain.ManagedContentStage, error) {
	writer.stageCalls++
	writer.content = append([]byte(nil), content...)
	writer.stageRefs = append(writer.stageRefs, sourceRef)
	return workspacedomain.ManagedContentStage{
		ContentHash: hash, ByteSize: int64(len(content)), ManagedLocation: ".knowledge/sources/" + hash,
		StagingLocation: ".knowledge/sources/.staging/" + sourceRef,
	}, nil
}

func (writer *contentWriterFake) PublishManagedBytes(_ context.Context, _ foundation.ID, stage workspacedomain.ManagedContentStage) (workspacedomain.ContentCapture, error) {
	writer.publishCalls++
	if len(writer.publishErrors) > 0 {
		err := writer.publishErrors[0]
		writer.publishErrors = writer.publishErrors[1:]
		return workspacedomain.ContentCapture{}, err
	}
	return workspacedomain.ContentCapture{ContentHash: stage.ContentHash, ByteSize: stage.ByteSize, ManagedLocation: stage.ManagedLocation, Created: true}, nil
}

func (writer *contentWriterFake) DiscardManagedBytes(_ context.Context, _ foundation.ID, _ workspacedomain.ManagedContentStage) error {
	writer.discardCalls++
	return nil
}

func newTestService(t *testing.T, repository Repository, content ManagedContentWriter, now time.Time) *Service {
	t.Helper()
	service, err := NewService(Dependencies{Repository: repository, Content: content, IDs: &sequenceIDs{}, Clock: foundation.FixedClock{Value: now}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newFilesystemBackedCaptureService(t *testing.T, repository Repository, rootPath string) *Service {
	t.Helper()
	workspaceService := workspaceapp.NewService(workspaceapp.Dependencies{
		Repository:   captureWorkspaceRepository{workspace: workspacedomain.Workspace{ID: captureTestWorkspaceID, RootPath: rootPath}},
		ManagedFiles: filesystem.Scanner{},
	})
	return newTestService(t, repository, workspaceService, time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC))
}

func captureFilesystemWorkspace(t *testing.T) string {
	t.Helper()
	rootPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootPath, ".git", "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	return rootPath
}

type captureWorkspaceRepository struct{ workspace workspacedomain.Workspace }

func (repository captureWorkspaceRepository) GetWorkspaceByID(_ context.Context, id foundation.ID) (workspacedomain.Workspace, error) {
	if id != repository.workspace.ID {
		return workspacedomain.Workspace{}, errors.New("workspace not found")
	}
	return repository.workspace, nil
}

func (captureWorkspaceRepository) CreateWorkspace(context.Context, workspacedomain.Workspace) (workspacedomain.Workspace, error) {
	return workspacedomain.Workspace{}, errors.New("not implemented")
}

func (captureWorkspaceRepository) GetWorkspaceByRootPath(context.Context, string) (workspacedomain.Workspace, error) {
	return workspacedomain.Workspace{}, errors.New("not implemented")
}

func (captureWorkspaceRepository) ListWorkspaceRoots(context.Context) ([]string, error) {
	return nil, errors.New("not implemented")
}

func (captureWorkspaceRepository) RegisterSourceVersion(context.Context, workspacedomain.SourceRegistration) (workspacedomain.SourceRegistrationResult, error) {
	return workspacedomain.SourceRegistrationResult{}, errors.New("not implemented")
}

func (captureWorkspaceRepository) RegisterSourceVersions(context.Context, []workspacedomain.SourceRegistration) ([]workspacedomain.SourceRegistrationResult, error) {
	return nil, errors.New("not implemented")
}

func captureApplicationErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
