package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestionapplication "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

var (
	refreshTestRequestID       foundation.ID = "10000000-0000-4000-8000-000000000001"
	refreshTestWorkspaceID     foundation.ID = "20000000-0000-4000-8000-000000000001"
	refreshTestSourceID        foundation.ID = "30000000-0000-4000-8000-000000000001"
	refreshTestSourceVersionID foundation.ID = "40000000-0000-4000-8000-000000000001"
	refreshTestAttemptID       foundation.ID = "50000000-0000-4000-8000-000000000001"
	refreshTestProjectionID    foundation.ID = "60000000-0000-4000-8000-000000000001"
	refreshTestIndexID         foundation.ID = "70000000-0000-4000-8000-000000000001"
	refreshTestActivationID    foundation.ID = "80000000-0000-4000-8000-000000000001"
)

func TestSourceRefresherHoldsWorkspaceLeaseAcrossSnapshotBuildAndActivation(t *testing.T) {
	fixture := newSourceRefresherFixture(t)

	result, err := fixture.refresher.Refresh(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.lease.held {
		t.Fatal("source refresh lease remained held after Refresh")
	}
	if result.IngestionAttemptID != refreshTestAttemptID || result.ParseProjectionID != refreshTestProjectionID ||
		result.IndexVersionID != refreshTestIndexID || result.ActivationID != refreshTestActivationID || !result.VectorDegraded {
		t.Fatalf("result = %#v", result)
	}
	want := []string{"ingestion", "lock", "snapshot", "lexical", "ready", "active", "activate", "release"}
	if !reflect.DeepEqual(fixture.events, want) {
		t.Fatalf("events = %#v, want %#v", fixture.events, want)
	}
}

func TestSourceRefresherReleasesWorkspaceLeaseOnBuildFailure(t *testing.T) {
	fixture := newSourceRefresherFixture(t)
	buildErr := errors.New("lexical build failed")
	fixture.retrieval.failAt = "lexical"
	fixture.retrieval.err = buildErr

	_, err := fixture.refresher.Refresh(context.Background(), fixture.request)
	if !errors.Is(err, buildErr) {
		t.Fatalf("Refresh() error = %v", err)
	}
	if fixture.lease.held {
		t.Fatal("source refresh lease remained held after failure")
	}
	want := []string{"ingestion", "lock", "snapshot", "lexical", "release"}
	if !reflect.DeepEqual(fixture.events, want) {
		t.Fatalf("events = %#v, want %#v", fixture.events, want)
	}
}

func TestSourceRefresherReturnsLeaseReleaseFailure(t *testing.T) {
	fixture := newSourceRefresherFixture(t)
	releaseErr := errors.New("lease release failed")
	fixture.lease.releaseErr = releaseErr

	_, err := fixture.refresher.Refresh(context.Background(), fixture.request)
	if !errors.Is(err, releaseErr) {
		t.Fatalf("Refresh() error = %v", err)
	}
	if fixture.lease.held {
		t.Fatal("test lease remained held after release failure")
	}
}

func TestNewSourceRefresherRejectsMissingWorkspaceLocker(t *testing.T) {
	fixture := newSourceRefresherFixture(t)

	_, err := NewSourceRefresher(SourceRefresherDependencies{
		Ingestion: fixture.ingestion,
		Retrieval: fixture.retrieval,
	}, sourceRefresherTestOptions())
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "SOURCE_REFRESH_UNAVAILABLE" ||
		classified.Kind != foundation.ErrorDependencyUnavailable {
		t.Fatalf("NewSourceRefresher() error = %v", err)
	}
}

func TestSourceRefresherRefreshBatchBuildsOneSnapshotAndActivation(t *testing.T) {
	fixture := newSourceRefresherFixture(t)
	secondSourceID := foundation.ID("30000000-0000-4000-8000-000000000002")
	secondVersionID := foundation.ID("40000000-0000-4000-8000-000000000002")
	removedSourceID := foundation.ID("30000000-0000-4000-8000-000000000003")

	result, err := fixture.refresher.RefreshBatch(context.Background(), BatchSourceRefreshRequest{
		RequestID: refreshTestRequestID, WorkspaceID: refreshTestWorkspaceID,
		Upserts: []BatchSourceRefreshEntry{
			{SourceID: refreshTestSourceID, SourceVersionID: refreshTestSourceVersionID, AttemptNumber: 1},
			{SourceID: secondSourceID, SourceVersionID: secondVersionID, AttemptNumber: 1},
		},
		RemovedSourceIDs: []foundation.ID{removedSourceID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reindexed || result.IndexVersionID != refreshTestIndexID || result.ActivationID != refreshTestActivationID ||
		len(result.IngestionAttemptIDs) != 2 || len(result.ParseProjectionIDs) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if len(fixture.retrieval.snapshotRequests) != 1 {
		t.Fatalf("snapshot calls = %d, want 1", len(fixture.retrieval.snapshotRequests))
	}
	snapshot := fixture.retrieval.snapshotRequests[0]
	if snapshot.SourceSnapshotRef != "git-fast-forward-v1:"+string(refreshTestRequestID) ||
		len(snapshot.Targets) != 2 || snapshot.Targets[0].SourceID != refreshTestSourceID ||
		snapshot.Targets[1].SourceID != secondSourceID ||
		!reflect.DeepEqual(snapshot.RemovedSourceIDs, []foundation.ID{removedSourceID}) {
		t.Fatalf("snapshot request = %#v", snapshot)
	}
	want := []string{"ingestion", "ingestion", "lock", "snapshot", "lexical", "ready", "active", "activate", "release"}
	if !reflect.DeepEqual(fixture.events, want) {
		t.Fatalf("events = %#v, want %#v", fixture.events, want)
	}
}

func TestSourceRefresherRefreshBatchDeletionOnlyWithoutActiveIndexIsNoOp(t *testing.T) {
	fixture := newSourceRefresherFixture(t)

	result, err := fixture.refresher.RefreshBatch(context.Background(), BatchSourceRefreshRequest{
		RequestID: refreshTestRequestID, WorkspaceID: refreshTestWorkspaceID,
		RemovedSourceIDs: []foundation.ID{refreshTestSourceID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reindexed || result.IndexVersionID != "" || len(fixture.retrieval.snapshotRequests) != 0 {
		t.Fatalf("deletion-only result = %#v, snapshots=%d", result, len(fixture.retrieval.snapshotRequests))
	}
	want := []string{"lock", "active", "release"}
	if !reflect.DeepEqual(fixture.events, want) {
		t.Fatalf("events = %#v, want %#v", fixture.events, want)
	}
}

type sourceRefresherFixture struct {
	refresher *SourceRefresher
	request   SourceRefreshRequest
	ingestion *sourceRefreshIngestionFake
	retrieval *sourceRefreshRetrievalFake
	lease     *sourceRefreshLeaseFake
	events    []string
}

func newSourceRefresherFixture(t *testing.T) *sourceRefresherFixture {
	t.Helper()
	fixture := &sourceRefresherFixture{}
	fixture.lease = &sourceRefreshLeaseFake{events: &fixture.events}
	fixture.ingestion = &sourceRefreshIngestionFake{events: &fixture.events, lease: fixture.lease}
	fixture.retrieval = &sourceRefreshRetrievalFake{events: &fixture.events, lease: fixture.lease}
	locker := &sourceRefreshLockerFake{events: &fixture.events, lease: fixture.lease}
	refresher, err := NewSourceRefresher(SourceRefresherDependencies{
		Ingestion: fixture.ingestion, Retrieval: fixture.retrieval, Locker: locker,
	}, sourceRefresherTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	fixture.refresher = refresher
	fixture.request = SourceRefreshRequest{
		RequestID: refreshTestRequestID, WorkspaceID: refreshTestWorkspaceID,
		SourceID: refreshTestSourceID, SourceVersionID: refreshTestSourceVersionID, AttemptNumber: 1,
	}
	return fixture
}

func sourceRefresherTestOptions() SourceRefresherOptions {
	base := DefaultFTSOnlyProcessorOptions(time.Second)
	return SourceRefresherOptions{
		TokenizerID: base.TokenizerID, TokenizerVersion: base.TokenizerVersion,
		TokenizerConfigHash: base.TokenizerConfigHash, FusionConfig: base.FusionConfig,
	}
}

type sourceRefreshLeaseFake struct {
	events     *[]string
	held       bool
	releaseErr error
}

func (lease *sourceRefreshLeaseFake) Release(context.Context) error {
	*lease.events = append(*lease.events, "release")
	lease.held = false
	return lease.releaseErr
}

type sourceRefreshLockerFake struct {
	events *[]string
	lease  *sourceRefreshLeaseFake
}

func (locker *sourceRefreshLockerFake) AcquireSourceRefresh(_ context.Context, workspaceID foundation.ID) (SourceRefreshLease, error) {
	if workspaceID != refreshTestWorkspaceID || locker.lease.held {
		return nil, errors.New("invalid source refresh lock request")
	}
	*locker.events = append(*locker.events, "lock")
	locker.lease.held = true
	return locker.lease, nil
}

type sourceRefreshIngestionFake struct {
	events *[]string
	lease  *sourceRefreshLeaseFake
}

func (fake *sourceRefreshIngestionFake) Process(_ context.Context, request ingestionapplication.ProcessRequest) (ingestionapplication.ProcessResult, error) {
	if fake.lease.held {
		return ingestionapplication.ProcessResult{}, errors.New("ingestion ran while source refresh lease was held")
	}
	*fake.events = append(*fake.events, "ingestion")
	return ingestionapplication.ProcessResult{Attempt: ingestiondomain.AttemptRecord{
		Attempt: ingestiondomain.Attempt{
			ID: refreshTestAttemptID, WorkspaceID: refreshTestWorkspaceID, SourceVersionID: request.SourceVersionID,
			Status: ingestiondomain.AttemptChunked, SecurityStatus: ingestiondomain.SecurityPassed,
			ParserID: "markdown", ParserVersion: "v1", ParserConfigHash: "parser-config",
			ChunkStrategyVersion: "structure-v1", SchemaVersion: "parse-v1", AttemptNumber: request.AttemptNumber,
		},
		ParseProjectionID: &refreshTestProjectionID,
	}}, nil
}

type sourceRefreshRetrievalFake struct {
	events           *[]string
	lease            *sourceRefreshLeaseFake
	failAt           string
	err              error
	snapshotRequests []BeginWorkspaceSnapshotRequest
}

func (fake *sourceRefreshRetrievalFake) BeginWorkspaceSnapshot(_ context.Context, request BeginWorkspaceSnapshotRequest) (domain.WorkspaceSnapshotResult, error) {
	if err := fake.recordHeld("snapshot"); err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	if fake.failAt == "snapshot" {
		return domain.WorkspaceSnapshotResult{}, fake.err
	}
	fake.snapshotRequests = append(fake.snapshotRequests, request)
	contract := request.ProcessingContract
	return domain.WorkspaceSnapshotResult{IndexVersion: domain.IndexVersion{
		ID: refreshTestIndexID, WorkspaceID: request.WorkspaceID, ProcessingContract: &contract,
		Status: domain.IndexStatusBuilding, Version: 1, DegradedCapabilities: []domain.DegradedCapability{domain.DegradedVector},
	}}, nil
}

func (fake *sourceRefreshRetrievalFake) BuildLexical(_ context.Context, _ TransitionRequest) (domain.ProjectionBatchResult, error) {
	if err := fake.recordHeld("lexical"); err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	if fake.failAt == "lexical" {
		return domain.ProjectionBatchResult{}, fake.err
	}
	return domain.ProjectionBatchResult{IndexVersionID: refreshTestIndexID}, nil
}

func (fake *sourceRefreshRetrievalFake) Ready(_ context.Context, _ TransitionRequest) (domain.IndexVersion, error) {
	if err := fake.recordHeld("ready"); err != nil {
		return domain.IndexVersion{}, err
	}
	contract := domain.ProcessingContract{
		ParserID: "markdown", ParserVersion: "v1", ParserConfigHash: "parser-config",
		ChunkStrategyVersion: "structure-v1", SchemaVersion: "parse-v1",
	}
	return domain.IndexVersion{
		ID: refreshTestIndexID, WorkspaceID: refreshTestWorkspaceID, ProcessingContract: &contract,
		Status: domain.IndexStatusReady, Version: 2, DegradedCapabilities: []domain.DegradedCapability{domain.DegradedVector},
	}, nil
}

func (fake *sourceRefreshRetrievalFake) GetActive(context.Context, foundation.ID) (domain.IndexVersion, error) {
	if err := fake.recordHeld("active"); err != nil {
		return domain.IndexVersion{}, err
	}
	return domain.IndexVersion{}, foundation.NewError(foundation.ErrorNotFound, "RETRIEVAL_ACTIVE_NOT_FOUND", false, errors.New("not found"))
}

func (fake *sourceRefreshRetrievalFake) Activate(_ context.Context, _ ActivateRequest) (domain.ActivationResult, error) {
	if err := fake.recordHeld("activate"); err != nil {
		return domain.ActivationResult{}, err
	}
	return domain.ActivationResult{
		Activation: domain.Activation{ID: refreshTestActivationID},
		ActiveIndexVersion: domain.IndexVersion{
			ID: refreshTestIndexID, WorkspaceID: refreshTestWorkspaceID,
			Status: domain.IndexStatusActive, Version: 3, DegradedCapabilities: []domain.DegradedCapability{domain.DegradedVector},
		},
	}, nil
}

func (fake *sourceRefreshRetrievalFake) recordHeld(event string) error {
	*fake.events = append(*fake.events, event)
	if !fake.lease.held {
		return errors.New(event + " ran without source refresh lease")
	}
	return nil
}
