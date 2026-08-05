package sourcecapture

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	gitsyncapplication "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	testWorkspaceID foundation.ID = "10000000-0000-4000-8000-000000000001"
	testRunID       foundation.ID = "20000000-0000-4000-8000-000000000001"
	testSourceID    foundation.ID = "30000000-0000-4000-8000-000000000001"
	testVersionID   foundation.ID = "40000000-0000-4000-8000-000000000001"
	testArtifactID  foundation.ID = "50000000-0000-4000-8000-000000000001"
	testRemovedID   foundation.ID = "60000000-0000-4000-8000-000000000001"
	testIndexID     foundation.ID = "70000000-0000-4000-8000-000000000001"
)

var testCommand = gitsyncapplication.ExternalChangeCommand{
	WorkspaceID: testWorkspaceID, RunID: testRunID,
	BeforeCommit:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	AfterCommit:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	IdempotencyKey: "git-sync-index:" + string(testRunID),
}

func TestCaptureGitFastForwardAppliesOneSourceBatchAndOneRefresh(t *testing.T) {
	trees := &treeFake{changes: []workspacedomain.CommittedTreeChange{
		{Kind: workspacedomain.CommittedTreeChangeUpsert, RelativePath: "notes/a.md"},
		{Kind: workspacedomain.CommittedTreeChangeDelete, RelativePath: "notes/old.txt"},
		{Kind: workspacedomain.CommittedTreeChangeUpsert, RelativePath: "static/logo.png"},
	}}
	blobs := &blobFake{blob: workspacedomain.CommittedBlob{
		WorkspaceID: testWorkspaceID, Commit: testCommand.AfterCommit, RelativePath: "notes/a.md", Bytes: []byte("# updated\n"),
	}}
	registration := workspacedomain.SourceRegistration{
		Source:   workspacedomain.Source{ID: testSourceID, WorkspaceID: testWorkspaceID, OriginalLocation: "notes/a.md"},
		Artifact: workspacedomain.ContentArtifact{ID: testArtifactID, WorkspaceID: testWorkspaceID},
		Version:  workspacedomain.SourceVersion{ID: testVersionID, SourceID: testSourceID, OriginalContentLocation: "notes/a.md"},
	}
	sources := &sourceFake{registration: registration}
	repository := &repositoryFake{result: workspacedomain.GitCaptureBatchResult{
		Registrations:    []workspacedomain.SourceRegistrationResult{{Source: registration.Source, Artifact: registration.Artifact, Version: registration.Version}},
		RemovedSourceIDs: []foundation.ID{testRemovedID},
	}}
	refresher := &refresherFake{result: retrievalapplication.BatchSourceRefreshResult{IndexVersionID: testIndexID, Reindexed: true}}
	capture := newCaptureFixture(t, trees, blobs, sources, repository, refresher)

	result, err := capture.CaptureGitFastForward(context.Background(), testCommand)
	if err != nil {
		t.Fatal(err)
	}
	if result.IndexVersionID != testIndexID || repository.completeCalls != 1 || refresher.calls != 1 {
		t.Fatalf("result=%#v complete=%d refresh=%d", result, repository.completeCalls, refresher.calls)
	}
	if len(repository.batch.Registrations) != 1 || len(repository.batch.RemovedPaths) != 1 || repository.batch.RemovedPaths[0] != "notes/old.txt" || len(repository.batch.RequestHash) != 64 {
		t.Fatalf("batch=%#v", repository.batch)
	}
	if sources.calls != 1 || blobs.calls != 1 || refresher.request.RequestID != testRunID || len(refresher.request.Upserts) != 1 || refresher.request.Upserts[0].SourceVersionID != testVersionID {
		t.Fatalf("source_calls=%d blob_calls=%d refresh=%#v", sources.calls, blobs.calls, refresher.request)
	}
}

func TestCaptureGitFastForwardCompletesUnsupportedOnlyBatchWithoutRefresh(t *testing.T) {
	trees := &treeFake{changes: []workspacedomain.CommittedTreeChange{{Kind: workspacedomain.CommittedTreeChangeUpsert, RelativePath: "static/logo.png"}}}
	repository := &repositoryFake{}
	refresher := &refresherFake{}
	capture := newCaptureFixture(t, trees, &blobFake{}, &sourceFake{}, repository, refresher)

	result, err := capture.CaptureGitFastForward(context.Background(), testCommand)
	if err != nil {
		t.Fatal(err)
	}
	if result.IndexVersionID != "" || !result.NoIndexRequired || repository.completeCalls != 1 || refresher.calls != 0 {
		t.Fatalf("result=%#v complete=%d refresh=%d", result, repository.completeCalls, refresher.calls)
	}
}

func TestCaptureGitFastForwardMarksDeletionWithoutActiveIndexAsNoIndexRequired(t *testing.T) {
	trees := &treeFake{changes: []workspacedomain.CommittedTreeChange{{Kind: workspacedomain.CommittedTreeChangeDelete, RelativePath: "notes/old.txt"}}}
	repository := &repositoryFake{result: workspacedomain.GitCaptureBatchResult{RemovedSourceIDs: []foundation.ID{testRemovedID}}}
	refresher := &refresherFake{result: retrievalapplication.BatchSourceRefreshResult{Reindexed: false}}
	capture := newCaptureFixture(t, trees, &blobFake{}, &sourceFake{}, repository, refresher)

	result, err := capture.CaptureGitFastForward(context.Background(), testCommand)
	if err != nil {
		t.Fatal(err)
	}
	if result.IndexVersionID != "" || !result.NoIndexRequired || repository.completeCalls != 1 || refresher.calls != 1 {
		t.Fatalf("result=%#v complete=%d refresh=%d", result, repository.completeCalls, refresher.calls)
	}
}

func TestCaptureGitFastForwardLeavesCheckpointOpenWhenRefreshFails(t *testing.T) {
	registration := workspacedomain.SourceRegistration{
		Source:   workspacedomain.Source{ID: testSourceID, WorkspaceID: testWorkspaceID, OriginalLocation: "notes/a.md"},
		Artifact: workspacedomain.ContentArtifact{ID: testArtifactID, WorkspaceID: testWorkspaceID},
		Version:  workspacedomain.SourceVersion{ID: testVersionID, SourceID: testSourceID, OriginalContentLocation: "notes/a.md"},
	}
	repository := &repositoryFake{result: workspacedomain.GitCaptureBatchResult{
		Registrations: []workspacedomain.SourceRegistrationResult{{Source: registration.Source, Artifact: registration.Artifact, Version: registration.Version}},
	}}
	cause := errors.New("index unavailable")
	refresher := &refresherFake{err: cause}
	capture := newCaptureFixture(t,
		&treeFake{changes: []workspacedomain.CommittedTreeChange{{Kind: workspacedomain.CommittedTreeChangeUpsert, RelativePath: "notes/a.md"}}},
		&blobFake{blob: workspacedomain.CommittedBlob{WorkspaceID: testWorkspaceID, Commit: testCommand.AfterCommit, RelativePath: "notes/a.md", Bytes: []byte("body")}},
		&sourceFake{registration: registration}, repository, refresher,
	)

	if _, err := capture.CaptureGitFastForward(context.Background(), testCommand); !errors.Is(err, cause) {
		t.Fatalf("error=%v", err)
	}
	if repository.completeCalls != 0 {
		t.Fatalf("checkpoint completed after refresh failure: %d", repository.completeCalls)
	}
}

func newCaptureFixture(t *testing.T, trees *treeFake, blobs *blobFake, sources *sourceFake, repository *repositoryFake, refresher *refresherFake) *Capture {
	t.Helper()
	capture, err := New(Dependencies{
		Trees: trees, Blobs: blobs, Sources: sources, Repository: repository, Refresher: refresher,
		Clock: foundation.FixedClock{Value: time.Date(2026, time.August, 3, 3, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return capture
}

type treeFake struct {
	changes []workspacedomain.CommittedTreeChange
}

func (fake *treeFake) ReadCommittedTreeChanges(context.Context, foundation.ID, string, string) ([]workspacedomain.CommittedTreeChange, error) {
	return append([]workspacedomain.CommittedTreeChange(nil), fake.changes...), nil
}

type blobFake struct {
	blob  workspacedomain.CommittedBlob
	calls int
}

func (fake *blobFake) ReadCommittedBlob(context.Context, foundation.ID, string, string) (workspacedomain.CommittedBlob, error) {
	fake.calls++
	return fake.blob, nil
}

type sourceFake struct {
	registration workspacedomain.SourceRegistration
	calls        int
}

func (fake *sourceFake) PrepareCommittedSourceBlob(context.Context, workspaceapplication.CaptureCommittedSourceRequest, workspacedomain.CommittedBlob) (workspacedomain.SourceRegistration, error) {
	fake.calls++
	return fake.registration, nil
}

type repositoryFake struct {
	batch         workspacedomain.GitCaptureBatch
	result        workspacedomain.GitCaptureBatchResult
	completeCalls int
}

func (fake *repositoryFake) ApplyGitCaptureBatch(_ context.Context, batch workspacedomain.GitCaptureBatch) (workspacedomain.GitCaptureBatchResult, error) {
	fake.batch = batch
	return fake.result, nil
}

func (fake *repositoryFake) CompleteGitCaptureBatch(context.Context, foundation.ID, foundation.ID, string, string) error {
	fake.completeCalls++
	return nil
}

type refresherFake struct {
	request retrievalapplication.BatchSourceRefreshRequest
	result  retrievalapplication.BatchSourceRefreshResult
	err     error
	calls   int
}

func (fake *refresherFake) RefreshBatch(_ context.Context, request retrievalapplication.BatchSourceRefreshRequest) (retrievalapplication.BatchSourceRefreshResult, error) {
	fake.calls++
	fake.request = request
	return fake.result, fake.err
}
