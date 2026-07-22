package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

func TestScanServiceStartCanonicalizesCoverageAndBindsStatusURL(t *testing.T) {
	workspaceID := testID(90)
	now := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	starter := &scanStartFake{}
	service, err := NewScanService(starter, &scanStateFake{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Start(context.Background(), ScanStartCommand{
		WorkspaceID: workspaceID,
		Scope:       domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: workspaceID, Version: 1, SchemaVersion: "health-scope/workspace/v1"},
		Coverage: []domain.DetectorCoverage{
			{DetectorID: "health.detector.z", DetectorVersion: "detector/v1", Status: domain.DetectorCoverageStatusPending},
			{DetectorID: "health.detector.a", DetectorVersion: "detector/v1", Status: domain.DetectorCoverageStatusUnavailable, UnavailableReason: "owner is unavailable"},
		},
		MaxItems: 100, IdempotencyKey: "  scan-1 ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if starter.request.IdempotencyKey != "scan-1" || starter.request.Coverage[0].DetectorID != "health.detector.a" || result.StatusURL != HealthScanStatusURL(workspaceID, result.Scan.ID) {
		t.Fatalf("request=%#v result=%#v", starter.request, result)
	}
	_ = now
}

func TestSmartCollectionScanStartBindsMembershipAndRegistryCoverage(t *testing.T) {
	workspaceID, collectionID := testID(96), testID(97)
	binding := SmartCollectionBinding{
		WorkspaceID: workspaceID, CollectionID: collectionID, CollectionVersion: 3,
		QueryHash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64), ExactCount: 2,
	}
	descriptor := Descriptor{
		ID: "health.detector.collection", Version: "detector/v1", IssueType: domain.IssueTypeOrphan,
		SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeSmartCollection},
		SupportedTarget: []domain.ObjectType{domain.ObjectTypeTopic}, DefaultSeverity: domain.SeverityLow,
	}
	registry, err := NewRegistry([]Detector{descriptorDetector{descriptor}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	starter := &scanStartFake{}
	membership := &smartCollectionMembershipFake{binding: binding}
	service, err := NewSmartCollectionScanService(starter, &scanStateFake{}, registry, membership)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Start(context.Background(), ScanStartCommand{
		WorkspaceID: workspaceID,
		Scope: domain.ScanScope{
			Type: domain.ScanScopeTypeSmartCollection, Ref: collectionID, Version: binding.CollectionVersion,
			SchemaVersion: "health-scope/smart-collection/v1", Hash: binding.QueryHash, ReadModelRevision: binding.ReadModelRevision,
			ExactCount: binding.ExactCount,
		},
		Coverage: []domain.DetectorCoverage{{DetectorID: "client.supplied", DetectorVersion: "wrong", Status: domain.DetectorCoverageStatusPending}},
		MaxItems: 100, IdempotencyKey: "collection-scan",
	})
	if err != nil {
		t.Fatal(err)
	}
	if membership.planCalls != 1 || len(starter.request.Coverage) != 1 || starter.request.Coverage[0].DetectorID != descriptor.ID || result.Scan.Scope.ReadModelRevision != binding.ReadModelRevision || result.Scan.Scope.ExactCount != binding.ExactCount {
		t.Fatalf("membership=%#v request=%#v result=%#v", membership, starter.request, result)
	}
}

func TestSmartCollectionScanStartFailsClosedOnStaleOrMissingMembership(t *testing.T) {
	workspaceID, collectionID := testID(98), testID(99)
	scope := domain.ScanScope{
		Type: domain.ScanScopeTypeSmartCollection, Ref: collectionID, Version: 2,
		SchemaVersion: "health-scope/smart-collection/v1", Hash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64), ExactCount: 1,
	}
	starter := &scanStartFake{}
	service, err := NewScanService(starter, &scanStateFake{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(context.Background(), ScanStartCommand{WorkspaceID: workspaceID, Scope: scope, MaxItems: 100, IdempotencyKey: "missing-membership"}); !hasScanCode(err, domain.ErrorCodeScanScopeUnavailable) {
		t.Fatalf("missing membership error = %v", err)
	}

	descriptor := Descriptor{ID: "health.detector.collection", Version: "detector/v1", IssueType: domain.IssueTypeOrphan, SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeSmartCollection}, SupportedTarget: []domain.ObjectType{domain.ObjectTypeTopic}, DefaultSeverity: domain.SeverityLow}
	registry, _ := NewRegistry([]Detector{descriptorDetector{descriptor}}, nil)
	membership := &smartCollectionMembershipFake{binding: SmartCollectionBinding{WorkspaceID: workspaceID, CollectionID: collectionID, CollectionVersion: 2, QueryHash: scope.Hash, ReadModelRevision: strings.Repeat("c", 64), ExactCount: 1}}
	service, _ = NewSmartCollectionScanService(starter, &scanStateFake{}, registry, membership)
	if _, err := service.Start(context.Background(), ScanStartCommand{WorkspaceID: workspaceID, Scope: scope, MaxItems: 100, IdempotencyKey: "stale-membership"}); !hasScanCode(err, domain.ErrorCodeScanScopeStale) {
		t.Fatalf("stale membership error = %v", err)
	}
	if starter.request.IdempotencyKey != "" {
		t.Fatalf("stale start reached persistence: %#v", starter.request)
	}

	membership.binding.ReadModelRevision = scope.ReadModelRevision
	membership.binding.ExactCount = 2
	if _, err := service.Start(context.Background(), ScanStartCommand{WorkspaceID: workspaceID, Scope: scope, MaxItems: 100, IdempotencyKey: "stale-count"}); !hasScanCode(err, domain.ErrorCodeScanScopeStale) {
		t.Fatalf("stale exact-count error = %v", err)
	}

	scope.ExactCount = 2
	if _, err := service.Start(context.Background(), ScanStartCommand{WorkspaceID: workspaceID, Scope: scope, MaxItems: 1, IdempotencyKey: "count-over-budget"}); !hasScanCode(err, domain.ErrorCodeScanInvalid) {
		t.Fatalf("exact-count budget error = %v", err)
	}
}

type smartCollectionMembershipFake struct {
	binding   SmartCollectionBinding
	planCalls int
}

func (fake *smartCollectionMembershipFake) Plan(context.Context, foundation.ID, foundation.ID) (SmartCollectionBinding, error) {
	fake.planCalls++
	return fake.binding, nil
}

func (fake *smartCollectionMembershipFake) Load(context.Context, SmartCollectionBinding, int) (SmartCollectionMembership, error) {
	return SmartCollectionMembership{}, nil
}

func hasScanCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}

func TestScanServiceAdvanceAndFinishValidateReturnedCASProjection(t *testing.T) {
	workspaceID, scanID := testID(91), testID(92)
	checkpoint := domain.ScanCheckpoint{Cursor: "next", Page: 1}
	state := &scanStateFake{}
	service, err := NewScanStateService(state)
	if err != nil {
		t.Fatal(err)
	}
	state.advance = validScanProjection(workspaceID, scanID, domain.ScanStatusRunning, 2, []domain.DetectorCoverage{{DetectorID: "health.detector.a", DetectorVersion: "detector/v1", Status: domain.DetectorCoverageStatusRunning, Checkpoint: checkpoint, Counters: domain.ScanCounters{Processed: 2}}})
	updated, err := service.Advance(context.Background(), ScanProgress{
		ScanID: scanID, WorkspaceID: workspaceID, ExpectedVersion: 1, DetectorID: "health.detector.a",
		Status: domain.DetectorCoverageStatusRunning, Checkpoint: checkpoint, CountersDelta: domain.ScanCounters{Processed: 2},
	})
	if err != nil || updated.Version != 2 {
		t.Fatalf("advance=%#v err=%v", updated, err)
	}
	finishedAt := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	state.finish = validScanProjection(workspaceID, scanID, domain.ScanStatusSucceeded, 3, []domain.DetectorCoverage{{DetectorID: "health.detector.a", DetectorVersion: "detector/v1", Status: domain.DetectorCoverageStatusSucceeded, Checkpoint: checkpoint, Counters: domain.ScanCounters{Processed: 2}}})
	state.finish.CompletedAt = &finishedAt
	if _, err := service.Finish(context.Background(), ScanTerminal{ScanID: scanID, WorkspaceID: workspaceID, ExpectedVersion: 2, Status: domain.ScanStatusSucceeded}); err != nil {
		t.Fatal(err)
	}
}

type scanStartFake struct{ request ScanStartRequest }

func (fake *scanStartFake) StartOrReplay(_ context.Context, request ScanStartRequest) (ScanStartResult, error) {
	fake.request = request
	now := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	return ScanStartResult{Scan: domain.Scan{ID: testID(93), WorkspaceID: request.WorkspaceID, WorkflowRunID: testID(94), Scope: request.Scope, Fingerprint: request.Fingerprint, IdempotencyKey: request.IdempotencyKey, RequestHash: request.RequestHash, MaxItems: request.MaxItems, Status: domain.ScanStatusPending, Coverage: request.Coverage, Version: 1, CreatedAt: now, UpdatedAt: now}}, nil
}

type scanStateFake struct {
	advance domain.Scan
	finish  domain.Scan
}

func (fake *scanStateFake) Get(context.Context, foundation.ID, foundation.ID) (domain.Scan, error) {
	return domain.Scan{}, nil
}
func (fake *scanStateFake) GetByWorkflowRun(context.Context, foundation.ID, foundation.ID) (domain.Scan, error) {
	return domain.Scan{}, nil
}
func (fake *scanStateFake) Advance(context.Context, ScanProgress) (domain.Scan, error) {
	return fake.advance, nil
}
func (fake *scanStateFake) Finish(context.Context, ScanTerminal) (domain.Scan, error) {
	return fake.finish, nil
}

func validScanProjection(workspaceID, scanID foundation.ID, status domain.ScanStatus, version int64, coverage []domain.DetectorCoverage) domain.Scan {
	now := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	var counters domain.ScanCounters
	for _, item := range coverage {
		counters.Processed += item.Counters.Processed
		counters.Created += item.Counters.Created
		counters.Reopened += item.Counters.Reopened
		counters.Resolved += item.Counters.Resolved
		counters.Unchanged += item.Counters.Unchanged
		counters.Failed += item.Counters.Failed
	}
	scan := domain.Scan{ID: scanID, WorkspaceID: workspaceID, WorkflowRunID: testID(95), Scope: domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: workspaceID, Version: 1, SchemaVersion: "health-scope/workspace/v1"}, Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", IdempotencyKey: "scan", RequestHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", MaxItems: 100, Status: status, Coverage: coverage, Counters: counters, Checkpoint: coverage[0].Checkpoint, Version: version, CreatedAt: now, UpdatedAt: now}
	if status == domain.ScanStatusSucceeded {
		completed := now.Add(time.Minute)
		scan.CompletedAt = &completed
	}
	return scan
}
