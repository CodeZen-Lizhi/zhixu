package application

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

func TestReconcileObservationCreatesIssueAndDeduplicatesUnchanged(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	workspaceID := testID(1)
	observation := observation()
	created, err := ReconcileObservation(workspaceID, testID(2), nil, observation, nil, now)
	if err != nil || !created.Created || created.Issue.Status != domain.IssueStatusOpen {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	unchanged, err := ReconcileObservation(workspaceID, testID(3), &created.Issue, observation, nil, now.Add(time.Minute))
	if err != nil || unchanged.Created || unchanged.Outcome != domain.ObservationOutcomeUnchanged || unchanged.Issue.ID != created.Issue.ID {
		t.Fatalf("unchanged=%#v err=%v", unchanged, err)
	}
	if unchanged.Issue.Version != created.Issue.Version || !unchanged.Issue.LastDetectedAt.Equal(created.Issue.LastDetectedAt) || !unchanged.Issue.UpdatedAt.Equal(created.Issue.UpdatedAt) || !unchanged.Issue.LastVerifiedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("unchanged issue mutated lifecycle fields: before=%+v after=%+v", created.Issue, unchanged.Issue)
	}
	metadataChanged := observation
	metadataChanged.Severity = domain.SeverityMedium
	if _, err := ReconcileObservation(workspaceID, created.Issue.ID, &created.Issue, metadataChanged, nil, now.Add(2*time.Minute)); err == nil {
		t.Fatal("same fingerprint with changed severity unexpectedly succeeded")
	}
}

func TestReconcileObservationReopensAndPreservesCriticalSeverity(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	workspaceID := testID(1)
	base := observation()
	created, err := ReconcileObservation(workspaceID, testID(2), nil, base, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	created.Issue.Severity = domain.SeverityCritical
	// Recompute the issue fingerprint because severity belongs to the observation hash.
	critical := base
	critical.Severity = domain.SeverityCritical
	created, err = ReconcileObservation(workspaceID, created.Issue.ID, nil, critical, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	changed := critical
	changed.DetectorVersion = "detector/v2"
	changed.Severity = domain.SeverityLow
	updated, err := ReconcileObservation(workspaceID, testID(3), &created.Issue, changed, nil, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Outcome != domain.ObservationOutcomeReopened || updated.Issue.Status != domain.IssueStatusReopened || updated.Issue.Severity != domain.SeverityCritical {
		t.Fatalf("updated=%#v", updated)
	}
}

func TestReconcileMissingIssueResolvesOnlyCompleteCoverage(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	issue := issueFixture(t, now)
	scan := scanFixture(now)
	partial := scan
	partial.Coverage = append([]domain.DetectorCoverage(nil), scan.Coverage...)
	partial.Status = domain.ScanStatusPartial
	partial.Coverage[0].Status = domain.DetectorCoverageStatusPartial
	partial.Coverage[0].LastError = &domain.FailureSummary{Stage: "detector", Code: "DETECTOR_PARTIAL", Retryable: true}
	partial.CompletedAt = timePtr(now.Add(time.Minute))
	if _, resolved, err := ReconcileMissingIssue(issue, partial, map[string]struct{}{}, now.Add(2*time.Minute)); err != nil || resolved {
		t.Fatalf("partial resolved=%v err=%#v scan=%#v", resolved, err, partial)
	}
	updated, resolved, err := ReconcileMissingIssue(issue, scan, map[string]struct{}{}, now.Add(2*time.Minute))
	if err != nil || !resolved || updated.Status != domain.IssueStatusResolved {
		t.Fatalf("updated=%#v resolved=%v err=%v", updated, resolved, err)
	}
}

func observation() domain.IssueObservation {
	return domain.IssueObservation{Type: domain.IssueTypeLowConfidence, Target: domain.ObjectRef{Type: domain.ObjectTypeClaim, ID: testID(10)}, DetectorID: "health.detector.low_confidence", DetectorVersion: "detector/v1", Severity: domain.SeverityLow, EvidenceSummary: "claim confidence is low", Evidence: []domain.IssueEvidence{{Ref: domain.ObjectRef{Type: domain.ObjectTypeClaim, ID: testID(10)}, Hash: strings.Repeat("a", 64), Summary: "confidence below threshold"}}, ObjectVersions: []domain.ObjectVersion{{Ref: domain.ObjectRef{Type: domain.ObjectTypeClaim, ID: testID(10)}, Version: 1}}}
}

func issueFixture(t *testing.T, now time.Time) domain.Issue {
	t.Helper()
	result, err := ReconcileObservation(testID(1), testID(2), nil, observation(), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	return result.Issue
}

func scanFixture(now time.Time) domain.Scan {
	return domain.Scan{ID: testID(20), WorkspaceID: testID(1), WorkflowRunID: testID(21), Scope: domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: testID(1), Version: 1, SchemaVersion: "health-scope/workspace/v1"}, Fingerprint: strings.Repeat("b", 64), IdempotencyKey: "scan-1", RequestHash: strings.Repeat("c", 64), MaxItems: 100, Version: 1, Status: domain.ScanStatusSucceeded, Coverage: []domain.DetectorCoverage{{DetectorID: "health.detector.low_confidence", DetectorVersion: "detector/v1", Status: domain.DetectorCoverageStatusSucceeded}}, CreatedAt: now, UpdatedAt: now, CompletedAt: timePtr(now.Add(time.Minute))}
}

func testID(value byte) foundation.ID {
	return foundation.ID(fmt.Sprintf("10000000-0000-4000-8000-0000000000%02d", value))
}

func timePtr(value time.Time) *time.Time { return &value }
