package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

func TestHealthScanCheckpointRoundTrip(t *testing.T) {
	last := domain.ObjectRef{Type: domain.ObjectTypeClaim, ID: foundation.ID("10000000-0000-4000-8000-000000000003")}
	want := domain.ScanCheckpoint{Cursor: "claim-3", Page: 2, LastItem: &last}
	encoded, err := encodeCheckpoint(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeCheckpoint(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !sameCheckpoint(got, want) {
		t.Fatalf("checkpoint=%#v want=%#v", got, want)
	}
	if _, err := decodeCheckpoint(append(encoded, []byte(` {}`)...)); err == nil {
		t.Fatal("expected trailing checkpoint JSON rejection")
	}
}

func TestHealthScanReplayAcceptsProgressedCoverageAndRejectsBindingDrift(t *testing.T) {
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	request := healthapp.ScanStartRequest{
		WorkspaceID: workspaceID,
		Scope:       domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: workspaceID, Version: 1, SchemaVersion: "health-scope/workspace/v1"},
		Coverage:    []domain.DetectorCoverage{{DetectorID: "health.detector.orphan", DetectorVersion: "detector/v1", Status: domain.DetectorCoverageStatusPending}},
		Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RequestHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MaxItems:    100, IdempotencyKey: "scan-1",
	}
	now := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	scan := domain.Scan{
		ID: foundation.ID("10000000-0000-4000-8000-000000000002"), WorkspaceID: workspaceID,
		WorkflowRunID: foundation.ID("10000000-0000-4000-8000-000000000004"), Scope: request.Scope,
		Fingerprint: request.Fingerprint, RequestHash: request.RequestHash, IdempotencyKey: request.IdempotencyKey,
		MaxItems: request.MaxItems, Status: domain.ScanStatusRunning,
		Coverage: []domain.DetectorCoverage{{DetectorID: "health.detector.orphan", DetectorVersion: "detector/v1", Status: domain.DetectorCoverageStatusRunning}},
		Version:  2, CreatedAt: now, UpdatedAt: now,
	}
	result, err := replayStart(request, scan)
	if err != nil || !result.Replayed {
		t.Fatalf("replay=%#v err=%v", result, err)
	}
	drifted := request
	drifted.Coverage[0].DetectorVersion = "detector/v2"
	if _, err := replayStart(drifted, scan); err == nil {
		t.Fatal("expected detector version drift rejection")
	}
	drifted = request
	drifted.Scope = domain.ScanScope{Type: domain.ScanScopeTypeSmartCollection, Ref: foundation.ID("10000000-0000-4000-8000-000000000005"), Version: 1, SchemaVersion: "health-scope/smart-collection/v1", Hash: strings.Repeat("c", 64), ReadModelRevision: strings.Repeat("d", 64), ExactCount: 0}
	scan.Scope = drifted.Scope
	scan.Scope.ExactCount = 1
	if _, err := replayStart(drifted, scan); err == nil {
		t.Fatal("expected exact-count replay drift rejection")
	}
}

func TestHealthScanSmartCollectionVerifierMapsStaleAndPreservesExactCount(t *testing.T) {
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	request := healthapp.ScanStartRequest{WorkspaceID: workspaceID, Scope: domain.ScanScope{
		Type: domain.ScanScopeTypeSmartCollection, Ref: foundation.ID("10000000-0000-4000-8000-000000000005"), Version: 2,
		SchemaVersion: "health-scope/smart-collection/v1", Hash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64), ExactCount: 0,
	}}
	repository := &ScanRepository{}
	if err := repository.verifySmartCollectionBinding(context.Background(), healthTransaction{}, request); !hasRepositoryCode(err, domain.ErrorCodeScanScopeUnavailable) {
		t.Fatalf("missing verifier error = %v", err)
	}

	verifier := &smartCollectionVerifierFake{err: foundation.NewError(foundation.ErrorVersionConflict, "COLLECTION_CURSOR_STALE", false, errors.New("drift"))}
	repository.verifier = verifier
	if err := repository.verifySmartCollectionBinding(context.Background(), healthTransaction{}, request); !hasRepositoryCode(err, domain.ErrorCodeScanScopeStale) {
		t.Fatalf("stale verifier error = %v", err)
	}
	verifier.err = nil
	if err := repository.verifySmartCollectionBinding(context.Background(), healthTransaction{}, request); err != nil {
		t.Fatal(err)
	}
	if verifier.binding.ExactCount != 0 || verifier.binding.ReadModelRevision != request.Scope.ReadModelRevision {
		t.Fatalf("binding=%#v", verifier.binding)
	}
}

type smartCollectionVerifierFake struct {
	binding healthapp.SmartCollectionBinding
	err     error
}

func (fake *smartCollectionVerifierFake) VerifyBindingScoped(_ context.Context, _ foundation.TransactionScope, binding healthapp.SmartCollectionBinding) error {
	fake.binding = binding
	return fake.err
}

func hasRepositoryCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}

func TestHealthScanCoverageTransitionsAreBounded(t *testing.T) {
	if !validCoverageTransition(domain.DetectorCoverageStatusPending, domain.DetectorCoverageStatusRunning) ||
		!validCoverageTransition(domain.DetectorCoverageStatusRunning, domain.DetectorCoverageStatusSucceeded) ||
		validCoverageTransition(domain.DetectorCoverageStatusSucceeded, domain.DetectorCoverageStatusRunning) {
		t.Fatal("health scan coverage transition matrix drifted")
	}
}

func TestHealthScanCompletedEventUsesStableMinimalBindingForEveryTerminalStatus(t *testing.T) {
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	scanID := foundation.ID("10000000-0000-4000-8000-000000000002")
	workflowRunID := foundation.ID("10000000-0000-4000-8000-000000000003")
	occurredAt := time.Date(2026, 7, 22, 8, 9, 10, 123000, time.UTC)
	for _, status := range []domain.ScanStatus{
		domain.ScanStatusSucceeded,
		domain.ScanStatusPartial,
		domain.ScanStatusFailed,
		domain.ScanStatusCancelled,
	} {
		t.Run(strings.ToLower(string(status)), func(t *testing.T) {
			appender := &healthScanEventAppenderFake{}
			scan := domain.Scan{ID: scanID, WorkspaceID: workspaceID, WorkflowRunID: workflowRunID, Status: status, Version: 7}
			if err := appendHealthScanCompletedEvent(context.Background(), healthTransaction{}, appender, scan, occurredAt); err != nil {
				t.Fatal(err)
			}
			request := appender.request
			if request.WorkspaceID != workspaceID || request.WorkflowRunID == nil || *request.WorkflowRunID != workflowRunID ||
				request.ConversationID != nil || request.Type != "health.scan.completed" || request.ResourceRef != "health_scan:"+string(scanID) ||
				request.ResourceVersion != 7 || request.SchemaVersion != 1 ||
				request.SourceEventRef != "health.scan.completed:"+string(scanID)+":v7" || !request.OccurredAt.Equal(occurredAt) {
				t.Fatalf("append request=%#v", request)
			}
			if request.PayloadSummary != (eventsdomain.PayloadSummary{Status: strings.ToLower(string(status))}) {
				t.Fatalf("payload summary=%#v", request.PayloadSummary)
			}
		})
	}
}

func TestHealthScanCompletedEventRejectsUnexpectedReplayAndReturnsAppendFailure(t *testing.T) {
	scan := domain.Scan{
		ID:            foundation.ID("10000000-0000-4000-8000-000000000002"),
		WorkspaceID:   foundation.ID("10000000-0000-4000-8000-000000000001"),
		WorkflowRunID: foundation.ID("10000000-0000-4000-8000-000000000003"),
		Status:        domain.ScanStatusSucceeded, Version: 2,
	}
	appender := &healthScanEventAppenderFake{replayed: true}
	if err := appendHealthScanCompletedEvent(context.Background(), healthTransaction{}, appender, scan, time.Now().UTC()); !hasRepositoryCode(err, domain.ErrorCodeScanInvalid) {
		t.Fatalf("unexpected replay error=%v", err)
	}
	injected := errors.New("injected SSE append failure")
	appender = &healthScanEventAppenderFake{err: injected}
	if err := appendHealthScanCompletedEvent(context.Background(), healthTransaction{}, appender, scan, time.Now().UTC()); !errors.Is(err, injected) {
		t.Fatalf("append failure=%v", err)
	}
}

type healthScanEventAppenderFake struct {
	request  eventsdomain.AppendRequest
	replayed bool
	err      error
}

func (fake *healthScanEventAppenderFake) AppendScoped(_ context.Context, _ foundation.TransactionScope, request eventsdomain.AppendRequest) (eventsdomain.ServerEvent, bool, error) {
	fake.request = request
	return eventsdomain.ServerEvent{}, fake.replayed, fake.err
}
