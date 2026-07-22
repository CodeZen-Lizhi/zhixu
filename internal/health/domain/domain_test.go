package domain

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestComputeIssueIdentityHashBindsWorkspaceTypeTargetAndDetector(t *testing.T) {
	base := validObservation(t)
	identity, err := ComputeIssueIdentityHash(testID(900), base)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		mutate func(*IssueObservation, *foundation.ID)
	}{
		{
			name: "workspace",
			mutate: func(_ *IssueObservation, workspaceID *foundation.ID) {
				*workspaceID = testID(901)
			},
		},
		{
			name: "issue type",
			mutate: func(observation *IssueObservation, _ *foundation.ID) {
				observation.Type = IssueTypeDuplicate
			},
		},
		{
			name: "target",
			mutate: func(observation *IssueObservation, _ *foundation.ID) {
				observation.Target.ID = testID(42)
			},
		},
		{
			name: "detector id",
			mutate: func(observation *IssueObservation, _ *foundation.ID) {
				observation.DetectorID = "health.detector.duplicate"
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			observation := validObservation(t)
			workspaceID := testID(900)
			testCase.mutate(&observation, &workspaceID)
			value, err := ComputeIssueIdentityHash(workspaceID, observation)
			if err != nil {
				t.Fatal(err)
			}
			if value == identity {
				t.Fatalf("identity hash did not change for %s", testCase.name)
			}
		})
	}
}

func TestComputeIssueFingerprintCanonicalizesEvidenceAndObjectVersions(t *testing.T) {
	base := validObservation(t)
	first, err := ComputeIssueFingerprint(testID(900), base)
	if err != nil {
		t.Fatal(err)
	}

	reordered := validObservation(t)
	reordered.Evidence = []IssueEvidence{reordered.Evidence[1], reordered.Evidence[0]}
	reordered.ObjectVersions = []ObjectVersion{reordered.ObjectVersions[1], reordered.ObjectVersions[0]}
	second, err := ComputeIssueFingerprint(testID(900), reordered)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("fingerprint mismatch after canonicalization: %q != %q", first, second)
	}

	cases := []struct {
		name   string
		mutate func(*IssueObservation)
	}{
		{
			name: "detector version",
			mutate: func(observation *IssueObservation) {
				observation.DetectorVersion = "detector/v2"
			},
		},
		{
			name: "evidence hash",
			mutate: func(observation *IssueObservation) {
				observation.Evidence[0].Hash = strings.Repeat("d", 64)
			},
		},
		{
			name: "object version",
			mutate: func(observation *IssueObservation) {
				observation.ObjectVersions[0].Version++
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			observation := validObservation(t)
			testCase.mutate(&observation)
			value, err := ComputeIssueFingerprint(testID(900), observation)
			if err != nil {
				t.Fatal(err)
			}
			if value == first {
				t.Fatalf("fingerprint did not change for %s", testCase.name)
			}
		})
	}
}

func TestApplyIssueObservationPreservesSilencedStatusesWhenFingerprintUnchanged(t *testing.T) {
	now := fixedTime()
	base := validIssue(t, now)
	base.Status = IssueStatusIgnored
	base.StatusReason = "confirmed false alarm"
	base.Version = 4

	updated, outcome, err := ApplyIssueObservation(base, validObservation(t), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if outcome != ObservationOutcomeUnchanged {
		t.Fatalf("unexpected outcome: %s", outcome)
	}
	if updated.Status != IssueStatusIgnored {
		t.Fatalf("status = %s", updated.Status)
	}
	if updated.StatusReason != base.StatusReason {
		t.Fatalf("status reason = %q", updated.StatusReason)
	}
	if updated.DeferredUntil != nil {
		t.Fatalf("deferred_until = %v", updated.DeferredUntil)
	}
	if updated.LastVerifiedAt != now.Add(time.Hour) {
		t.Fatalf("last_verified_at = %s", updated.LastVerifiedAt)
	}
	if updated.Version != base.Version {
		t.Fatalf("version = %d", updated.Version)
	}
}

func TestApplyIssueObservationUnchangedOnlyRefreshesLastVerifiedAt(t *testing.T) {
	now := fixedTime()
	base := validIssue(t, now)
	base.Status = IssueStatusFalsePositive
	base.StatusReason = "confirmed acceptable exception"
	base.Version = 4
	base.LastDetectedAt = now.Add(30 * time.Minute)
	base.UpdatedAt = now.Add(45 * time.Minute)

	updated, outcome, err := ApplyIssueObservation(base, validObservation(t), now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if outcome != ObservationOutcomeUnchanged {
		t.Fatalf("unexpected outcome: %s", outcome)
	}
	if updated.LastVerifiedAt != now.Add(2*time.Hour) {
		t.Fatalf("last_verified_at = %s", updated.LastVerifiedAt)
	}
	updated.LastVerifiedAt = base.LastVerifiedAt
	if !reflect.DeepEqual(updated, base) {
		t.Fatalf("unchanged observation mutated issue: before=%+v after=%+v", base, updated)
	}
}

func TestApplyIssueObservationRejectsNonFingerprintChanges(t *testing.T) {
	now := fixedTime()
	base := validIssue(t, now)
	observation := validObservation(t)
	observation.Severity = SeverityMedium

	if _, outcome, err := ApplyIssueObservation(base, observation, now.Add(time.Hour)); err == nil {
		t.Fatalf("expected non-fingerprint severity change to fail closed, outcome=%s", outcome)
	}
}

func TestApplyIssueObservationPreservesResolvedStatusWhenFingerprintUnchanged(t *testing.T) {
	now := fixedTime()
	base := validIssue(t, now)
	base.Status = IssueStatusResolved
	resolvedAt := now.Add(time.Minute)
	base.ResolvedAt = &resolvedAt
	if err := ValidateIssue(base); err != nil {
		t.Fatal(err)
	}

	updated, outcome, err := ApplyIssueObservation(base, validObservation(t), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if outcome != ObservationOutcomeUnchanged || updated.Status != IssueStatusResolved || updated.ResolvedAt == nil || !updated.ResolvedAt.Equal(resolvedAt) {
		t.Fatalf("resolved unchanged observation reopened: outcome=%s issue=%+v", outcome, updated)
	}
}

func TestApplyIssueObservationReopensChangedFingerprintAndClearsBindings(t *testing.T) {
	now := fixedTime()
	base := validIssue(t, now)
	base.Status = IssueStatusProposalCreated
	base.Proposal = &ProposalBinding{
		ProposalID:       testID(950),
		RepairOptionCode: "health.repair.verify-provenance",
		Fingerprint:      base.Fingerprint,
		ObjectVersions:   cloneObjectVersions(base.ObjectVersions),
		CreatedAt:        now.Add(-time.Hour),
	}
	base.Version = 8

	observation := validObservation(t)
	observation.DetectorVersion = "detector/v2"

	updated, outcome, err := ApplyIssueObservation(base, observation, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if outcome != ObservationOutcomeReopened {
		t.Fatalf("unexpected outcome: %s", outcome)
	}
	if updated.ID != base.ID {
		t.Fatalf("issue id changed: %s != %s", updated.ID, base.ID)
	}
	if updated.Status != IssueStatusReopened {
		t.Fatalf("status = %s", updated.Status)
	}
	if updated.StatusReason != "" || updated.DeferredUntil != nil || updated.Proposal != nil {
		t.Fatalf("reopened issue kept stale bindings: %+v", updated)
	}
	if updated.FirstDetectedAt != base.FirstDetectedAt {
		t.Fatalf("first_detected_at = %s", updated.FirstDetectedAt)
	}
	if updated.Fingerprint == base.Fingerprint {
		t.Fatalf("fingerprint did not change")
	}
	if updated.Version != base.Version+1 {
		t.Fatalf("version = %d", updated.Version)
	}
}

func TestValidateIssueRequiresStatusSpecificBindings(t *testing.T) {
	now := fixedTime()
	validProposal := func() Issue {
		issue := validIssue(t, now)
		updated, err := ApplyIssueDecision(issue, IssueDecision{
			ExpectedVersion:  issue.Version,
			IdempotencyKey:   "proposal-binding",
			Action:           IssueDecisionCreateRepairProposal,
			ProposalID:       idPtr(testID(951)),
			RepairOptionCode: "health.repair.verify-provenance",
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		return updated
	}

	tests := []struct {
		name   string
		mutate func(*Issue)
	}{
		{
			name: "ignored cannot carry deferred time",
			mutate: func(issue *Issue) {
				issue.Status = IssueStatusIgnored
				issue.StatusReason = "confirmed false alarm"
				issue.DeferredUntil = timePtr(now.Add(time.Hour))
			},
		},
		{
			name: "open cannot carry reason",
			mutate: func(issue *Issue) {
				issue.StatusReason = "not applicable"
			},
		},
		{
			name: "ignored requires reason",
			mutate: func(issue *Issue) {
				issue.Status = IssueStatusIgnored
			},
		},
		{
			name: "deferred requires time",
			mutate: func(issue *Issue) {
				issue.Status = IssueStatusDeferred
				issue.StatusReason = "wait for next import"
			},
		},
		{
			name: "non proposal status cannot carry proposal",
			mutate: func(issue *Issue) {
				proposal := validProposal().Proposal
				issue.Proposal = proposal
			},
		},
		{
			name: "deferred cannot carry proposal",
			mutate: func(issue *Issue) {
				proposal := validProposal().Proposal
				issue.Status = IssueStatusDeferred
				issue.StatusReason = "wait for next import"
				issue.DeferredUntil = timePtr(now.Add(time.Hour))
				issue.Proposal = proposal
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			issue := validIssue(t, now)
			testCase.mutate(&issue)
			if err := ValidateIssue(issue); !hasCode(err, ErrorCodeIssueInvalid) {
				t.Fatalf("expected status binding rejection, got %v", err)
			}
		})
	}

	proposal := validProposal()
	proposal.DeferredUntil = timePtr(now.Add(time.Hour))
	if err := ValidateIssue(proposal); !hasCode(err, ErrorCodeIssueInvalid) {
		t.Fatalf("expected proposal/defer binding rejection, got %v", err)
	}
}

func TestIssueOperationsRejectZeroAndBackwardTimes(t *testing.T) {
	now := fixedTime()
	issue := validIssue(t, now)
	observation := validObservation(t)
	scan := validScan(t, now)
	decision := IssueDecision{
		ExpectedVersion: issue.Version,
		IdempotencyKey:  "ack-time",
		Action:          IssueDecisionAcknowledge,
	}

	for _, at := range []time.Time{{}, issue.UpdatedAt.Add(-time.Nanosecond)} {
		if _, _, err := ApplyIssueObservation(issue, observation, at); !hasCode(err, ErrorCodeIssueInvalid) {
			t.Fatalf("expected observation time rejection for %s, got %v", at, err)
		}
		if _, err := ApplyIssueDecision(issue, decision, at); !hasCode(err, ErrorCodeIssueDecisionInvalid) {
			t.Fatalf("expected decision time rejection for %s, got %v", at, err)
		}
		if _, _, err := ResolveIssueFromScan(issue, scan, map[string]struct{}{}, at); !hasCode(err, ErrorCodeIssueTransitionInvalid) {
			t.Fatalf("expected resolution time rejection for %s, got %v", at, err)
		}
	}

	issue.LastVerifiedAt = issue.UpdatedAt.Add(time.Minute)
	between := issue.UpdatedAt.Add(30 * time.Second)
	if _, _, err := ApplyIssueObservation(issue, observation, between); !hasCode(err, ErrorCodeIssueInvalid) {
		t.Fatalf("expected last-verified time rejection, got %v", err)
	}
}

func TestValidateIssueObservationRejectsUnavailableReviewAndUnsupportedTargets(t *testing.T) {
	review := validObservation(t)
	review.Type = IssueTypeReviewInvalidated
	if err := ValidateIssueObservation(testID(900), review); !hasCode(err, ErrorCodeIssueInvalid) {
		t.Fatalf("expected review-invalidated observation rejection, got %v", err)
	}

	unsupported := validObservation(t)
	unsupported.Target.Type = ObjectType("REVIEW_CARD")
	if err := ValidateIssueObservation(testID(900), unsupported); !hasCode(err, ErrorCodeIssueInvalid) {
		t.Fatalf("expected review-card target rejection, got %v", err)
	}

	indexVersion := validObservation(t)
	indexVersion.Target.Type = ObjectTypeIndexVersion
	indexVersion.Evidence[0].Ref.Type = ObjectTypeIndexVersion
	indexVersion.ObjectVersions[0].Ref.Type = ObjectTypeIndexVersion
	if err := ValidateIssueObservation(testID(900), indexVersion); err != nil {
		t.Fatalf("index-version target rejected: %v", err)
	}
}

func TestApplyIssueDecisionValidatesCASReasonAndFutureDefer(t *testing.T) {
	now := fixedTime()
	issue := validIssue(t, now)

	ignored, err := ApplyIssueDecision(issue, IssueDecision{
		ExpectedVersion: issue.Version,
		IdempotencyKey:  "ignore-1",
		Action:          IssueDecisionIgnore,
		Reason:          "duplicate detector noise",
	}, now)
	if err != nil {
		t.Fatalf("ignore decision error = %v", err)
	}
	if ignored.Status != IssueStatusIgnored || ignored.StatusReason != "duplicate detector noise" {
		t.Fatalf("ignored issue = %+v", ignored)
	}

	deferred, err := ApplyIssueDecision(issue, IssueDecision{
		ExpectedVersion: issue.Version,
		IdempotencyKey:  "defer-1",
		Action:          IssueDecisionDefer,
		Reason:          "wait for next import",
		DeferredUntil:   timePtr(now.Add(2 * time.Hour)),
	}, now)
	if err != nil {
		t.Fatalf("defer decision error = %v", err)
	}
	if deferred.Status != IssueStatusDeferred || deferred.DeferredUntil == nil || !deferred.DeferredUntil.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("deferred issue = %+v", deferred)
	}

	acknowledged, err := ApplyIssueDecision(issue, IssueDecision{
		ExpectedVersion: issue.Version,
		IdempotencyKey:  "ack-1",
		Action:          IssueDecisionAcknowledge,
	}, now)
	if err != nil {
		t.Fatalf("ack decision error = %v", err)
	}
	if acknowledged.Status != IssueStatusAcknowledged {
		t.Fatalf("status = %s", acknowledged.Status)
	}

	bound, err := ApplyIssueDecision(issue, IssueDecision{
		ExpectedVersion:  issue.Version,
		IdempotencyKey:   "proposal-1",
		Action:           IssueDecisionCreateRepairProposal,
		ProposalID:       idPtr(testID(951)),
		RepairOptionCode: "health.repair.verify-provenance",
	}, now)
	if err != nil {
		t.Fatalf("proposal decision error = %v", err)
	}
	if bound.Status != IssueStatusProposalCreated || bound.Proposal == nil || bound.Proposal.ProposalID != testID(951) {
		t.Fatalf("proposal binding = %+v", bound.Proposal)
	}

	if _, err := ApplyIssueDecision(issue, IssueDecision{
		ExpectedVersion: issue.Version + 1,
		IdempotencyKey:  "stale",
		Action:          IssueDecisionIgnore,
		Reason:          "reason",
	}, now); !hasCode(err, ErrorCodeIssueTransitionInvalid) {
		t.Fatalf("expected version conflict, got %v", err)
	}

	if _, err := ApplyIssueDecision(issue, IssueDecision{
		ExpectedVersion: issue.Version,
		IdempotencyKey:  "bad-reason",
		Action:          IssueDecisionIgnore,
		Reason:          "  not canonical  ",
	}, now); !hasCode(err, ErrorCodeIssueDecisionInvalid) {
		t.Fatalf("expected canonical reason rejection, got %v", err)
	}

	if _, err := ApplyIssueDecision(issue, IssueDecision{
		ExpectedVersion: issue.Version,
		IdempotencyKey:  "past-defer",
		Action:          IssueDecisionDefer,
		Reason:          "later",
		DeferredUntil:   timePtr(now.Add(-time.Minute)),
	}, now); !hasCode(err, ErrorCodeIssueDecisionInvalid) {
		t.Fatalf("expected future defer rejection, got %v", err)
	}
}

func TestResolveIssueFromCompleteScanRequiresFullCoverage(t *testing.T) {
	now := fixedTime()
	issue := validIssue(t, now)

	partial := validScan(t, now)
	partial.Status = ScanStatusPartial
	partial.Coverage[0].Status = DetectorCoverageStatusUnavailable
	partial.Coverage[0].UnavailableReason = "review capability unavailable"
	partial.CompletedAt = timePtr(now.Add(10 * time.Minute))
	if _, resolved, err := ResolveIssueFromScan(issue, partial, map[string]struct{}{}, now.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	} else if resolved {
		t.Fatal("partial scan must not resolve issue")
	}

	succeeded := validScan(t, now)
	seen := map[string]struct{}{issue.IdentityHash: {}}
	unchanged, resolved, err := ResolveIssueFromScan(issue, succeeded, seen, now.Add(15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if resolved || unchanged.ID != issue.ID || unchanged.Status != issue.Status || unchanged.Version != issue.Version {
		t.Fatal("seen identity must not be resolved")
	}

	updated, resolved, err := ResolveIssueFromScan(issue, succeeded, map[string]struct{}{}, now.Add(15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !resolved {
		t.Fatal("expected resolve on full coverage")
	}
	if updated.Status != IssueStatusResolved || updated.ResolvedAt == nil {
		t.Fatalf("resolved issue = %+v", updated)
	}

	if _, _, err := ResolveIssueFromScan(issue, succeeded, nil, now.Add(15*time.Minute)); !hasCode(err, ErrorCodeScanInvalid) {
		t.Fatalf("expected missing seen-set rejection, got %v", err)
	}
}

func TestResolveIssueFromCompleteScanPreservesUserDecisions(t *testing.T) {
	now := fixedTime()
	scan := validScan(t, now)
	base := validIssue(t, now)
	cases := []struct {
		name     string
		decision IssueDecision
	}{
		{name: "deferred", decision: IssueDecision{Action: IssueDecisionDefer, Reason: "review later", DeferredUntil: timePtr(now.Add(time.Hour))}},
		{name: "ignored", decision: IssueDecision{Action: IssueDecisionIgnore, Reason: "accepted exception"}},
		{name: "false positive", decision: IssueDecision{Action: IssueDecisionFalsePositive, Reason: "not applicable"}},
		{name: "proposal created", decision: IssueDecision{Action: IssueDecisionCreateRepairProposal, ProposalID: ptrID(testID(922)), RepairOptionCode: "health.repair.verify-provenance"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			issue := base
			decision := testCase.decision
			decision.ExpectedVersion = issue.Version
			decision.IdempotencyKey = "preserve-" + testCase.name
			updated, err := ApplyIssueDecision(issue, decision, now)
			if err != nil {
				t.Fatal(err)
			}
			result, resolved, err := ResolveIssueFromScan(updated, scan, map[string]struct{}{}, now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if resolved || result.Status != updated.Status || result.Version != updated.Version {
				t.Fatalf("user decision was changed: resolved=%v result=%+v updated=%+v", resolved, result, updated)
			}
		})
	}
}

func ptrID(value foundation.ID) *foundation.ID { return &value }

func TestValidateScanScopeSupportsWorkspaceTopicAndSmartCollection(t *testing.T) {
	workspace := ScanScope{Type: ScanScopeTypeWorkspace, Ref: testID(900), Version: 1, SchemaVersion: "health-scope/workspace/v1"}
	if err := ValidateScanScope(workspace); err != nil {
		t.Fatal(err)
	}
	topic := ScanScope{Type: ScanScopeTypeTopic, Ref: testID(901), Version: 3, SchemaVersion: "health-scope/topic/v1"}
	if err := ValidateScanScope(topic); err != nil {
		t.Fatal(err)
	}
	collection := ScanScope{
		Type:              ScanScopeTypeSmartCollection,
		Ref:               testID(902),
		Version:           4,
		SchemaVersion:     "health-scope/smart-collection/v1",
		Hash:              strings.Repeat("a", 64),
		ReadModelRevision: strings.Repeat("b", 64),
		ExactCount:        2,
	}
	if err := ValidateScanScope(collection); err != nil {
		t.Fatal(err)
	}
	collection.ReadModelRevision = ""
	if err := ValidateScanScope(collection); !hasCode(err, ErrorCodeScanInvalid) {
		t.Fatalf("expected missing smart-collection revision rejection, got %v", err)
	}
	topic.ReadModelRevision = strings.Repeat("b", 64)
	if err := ValidateScanScope(topic); !hasCode(err, ErrorCodeScanInvalid) {
		t.Fatalf("expected non-collection revision rejection, got %v", err)
	}
	collection.ReadModelRevision = ""
	collection.ExactCount = 0
	if err := ValidateScheduleScope(collection); err != nil {
		t.Fatalf("smart-collection schedule scope rejected: %v", err)
	}
	collection.ReadModelRevision = strings.Repeat("b", 64)
	if err := ValidateScheduleScope(collection); !hasCode(err, ErrorCodeScheduleInvalid) {
		t.Fatalf("expected persisted schedule revision rejection, got %v", err)
	}
	collection.ReadModelRevision = ""
	collection.ExactCount = 1
	if err := ValidateScheduleScope(collection); !hasCode(err, ErrorCodeScheduleInvalid) {
		t.Fatalf("expected persisted schedule exact-count rejection, got %v", err)
	}
	topic.ReadModelRevision = ""
	topic.ExactCount = 1
	if err := ValidateScanScope(topic); !hasCode(err, ErrorCodeScanInvalid) {
		t.Fatalf("expected non-collection exact-count rejection, got %v", err)
	}

	err := ValidateScanScope(ScanScope{
		Type:          ScanScopeTypeDirectory,
		Ref:           testID(903),
		Version:       1,
		SchemaVersion: "health-scope/directory/v1",
	})
	if !hasCode(err, ErrorCodeScanScopeUnavailable) {
		t.Fatalf("expected unavailable directory scope, got %v", err)
	}
}

func TestValidateScanRequiresConsistentCoverageAndTerminalState(t *testing.T) {
	now := fixedTime()
	scan := validScan(t, now)
	if err := ValidateScan(scan); err != nil {
		t.Fatalf("ValidateScan() error = %v", err)
	}
	if !CoverageComplete(scan.Coverage) {
		t.Fatal("expected complete coverage")
	}

	bad := validScan(t, now)
	bad.Status = ScanStatusSucceeded
	bad.Coverage[1].Status = DetectorCoverageStatusUnavailable
	bad.Coverage[1].UnavailableReason = "review capability unavailable"
	if err := ValidateScan(bad); !hasCode(err, ErrorCodeScanInvalid) {
		t.Fatalf("expected inconsistent coverage rejection, got %v", err)
	}

	failed := validScan(t, now)
	failed.Status = ScanStatusFailed
	failed.LastError = &FailureSummary{Stage: "detector", Code: "TIMEOUT", Retryable: true}
	if err := ValidateScan(failed); err != nil {
		t.Fatalf("failed scan validation error = %v", err)
	}

	succeededWithError := validScan(t, now)
	succeededWithError.LastError = &FailureSummary{Stage: "detector", Code: "UNEXPECTED"}
	if err := ValidateScan(succeededWithError); !hasCode(err, ErrorCodeScanInvalid) {
		t.Fatalf("expected succeeded scan error-summary rejection, got %v", err)
	}

	partialCoverage := validScan(t, now)
	partialCoverage.Status = ScanStatusPartial
	partialCoverage.Coverage[0].Status = DetectorCoverageStatusPartial
	partialCoverage.CompletedAt = timePtr(now.Add(5 * time.Minute))
	if err := ValidateScan(partialCoverage); !hasCode(err, ErrorCodeScanInvalid) {
		t.Fatalf("expected partial coverage summary requirement, got %v", err)
	}
	partialCoverage.Coverage[0].LastError = &FailureSummary{Stage: "detector", Code: "PAGE_TIMEOUT", Retryable: true}
	partialCoverage.LastError = &FailureSummary{Stage: "detector", Code: "PARTIAL_COVERAGE", Retryable: true}
	if err := ValidateScan(partialCoverage); err != nil {
		t.Fatalf("partial coverage with stable summary rejected: %v", err)
	}

	succeededCoverageWithError := validScan(t, now)
	succeededCoverageWithError.Coverage[0].LastError = &FailureSummary{Stage: "detector", Code: "UNEXPECTED"}
	if err := ValidateScan(succeededCoverageWithError); !hasCode(err, ErrorCodeScanInvalid) {
		t.Fatalf("expected succeeded coverage error-summary rejection, got %v", err)
	}

	unavailable := validScan(t, now)
	unavailable.Status = ScanStatusPartial
	unavailable.Coverage[0].Status = DetectorCoverageStatusUnavailable
	unavailable.CompletedAt = timePtr(now.Add(5 * time.Minute))
	if err := ValidateScan(unavailable); !hasCode(err, ErrorCodeScanInvalid) {
		t.Fatalf("expected unavailable coverage reason rejection, got %v", err)
	}
	unavailable.Coverage[0].UnavailableReason = "  review capability unavailable  "
	if err := ValidateScan(unavailable); !hasCode(err, ErrorCodeScanInvalid) {
		t.Fatalf("expected non-canonical unavailable reason rejection, got %v", err)
	}

	withoutCoverage := validScan(t, now)
	withoutCoverage.Status = ScanStatusPending
	withoutCoverage.Coverage = nil
	withoutCoverage.CompletedAt = nil
	if err := ValidateScan(withoutCoverage); !hasCode(err, ErrorCodeScanInvalid) {
		t.Fatalf("expected counters without coverage rejection, got %v", err)
	}
	withoutCoverage.Counters = ScanCounters{}
	if err := ValidateScan(withoutCoverage); err != nil {
		t.Fatalf("zero counters without coverage rejected: %v", err)
	}

	overLimit := validScan(t, now)
	overLimit.MaxItems = MaxScanItems + 1
	if err := ValidateScan(overLimit); !hasCode(err, ErrorCodeScanInvalid) {
		t.Fatalf("expected scan max-items rejection, got %v", err)
	}
}

func TestValidateScheduleDisabledByDefaultAndCadenceRules(t *testing.T) {
	now := fixedTime()
	disabled := validSchedule(now)
	disabled.Cadence = ScheduleCadenceDisabled
	disabled.NextRunAt = nil
	disabled.CronExpression = ""
	if err := ValidateSchedule(disabled); err != nil {
		t.Fatalf("disabled schedule error = %v", err)
	}

	daily := validSchedule(now)
	daily.Cadence = ScheduleCadenceDaily
	daily.CronExpression = ""
	if err := ValidateSchedule(daily); err != nil {
		t.Fatalf("daily schedule error = %v", err)
	}

	weekly := validSchedule(now)
	weekly.Cadence = ScheduleCadenceWeekly
	weekly.CronExpression = ""
	if err := ValidateSchedule(weekly); err != nil {
		t.Fatalf("weekly schedule error = %v", err)
	}

	cron := validSchedule(now)
	cron.Cadence = ScheduleCadenceCron
	cron.CronExpression = "0 3 * * 1-5"
	if err := ValidateSchedule(cron); err != nil {
		t.Fatalf("cron schedule error = %v", err)
	}

	badCron := validSchedule(now)
	badCron.Cadence = ScheduleCadenceCron
	badCron.CronExpression = "bad cron"
	if err := ValidateSchedule(badCron); !hasCode(err, ErrorCodeScheduleInvalid) {
		t.Fatalf("expected cron validation error, got %v", err)
	}

	for _, expression := range []string{
		"99 3 * * *",
		"0 99 * * *",
		"0 3 32 * *",
		"0 3 * 13 *",
		"0 3 * * 8",
		"0 3 * * */0",
		"0 3 * * 5-1",
	} {
		t.Run(expression, func(t *testing.T) {
			invalidCron := validSchedule(now)
			invalidCron.Cadence = ScheduleCadenceCron
			invalidCron.CronExpression = expression
			if err := ValidateSchedule(invalidCron); !hasCode(err, ErrorCodeScheduleInvalid) {
				t.Fatalf("expected cron range rejection for %q, got %v", expression, err)
			}
		})
	}

	badTimezone := validSchedule(now)
	badTimezone.Cadence = ScheduleCadenceDaily
	badTimezone.Timezone = "Mars/Olympus_Mons"
	if err := ValidateSchedule(badTimezone); !hasCode(err, ErrorCodeScheduleInvalid) {
		t.Fatalf("expected timezone rejection, got %v", err)
	}

	overLimit := validSchedule(now)
	overLimit.Cadence = ScheduleCadenceDaily
	overLimit.MaxItems = MaxScheduleItems + 1
	if err := ValidateSchedule(overLimit); !hasCode(err, ErrorCodeScheduleInvalid) {
		t.Fatalf("expected schedule max-items rejection, got %v", err)
	}

	if DefaultDetectorBatchSize < 1 || DefaultDetectorBatchSize > MaxDetectorBatchSize {
		t.Fatalf("detector batch bounds are inconsistent: default=%d max=%d", DefaultDetectorBatchSize, MaxDetectorBatchSize)
	}
}

func validObservation(t *testing.T) IssueObservation {
	t.Helper()
	observation := IssueObservation{
		Type:            IssueTypeMissingSource,
		Target:          ObjectRef{Type: ObjectTypeClaim, ID: testID(100)},
		DetectorID:      "health.detector.missing_source",
		DetectorVersion: "detector/v1",
		Severity:        SeverityHigh,
		EvidenceSummary: "claim is missing a required supporting source",
		Evidence: []IssueEvidence{
			{Ref: ObjectRef{Type: ObjectTypeClaim, ID: testID(100)}, Hash: strings.Repeat("a", 64), Summary: "claim has no active provenance"},
			{Ref: ObjectRef{Type: ObjectTypeSourceVersion, ID: testID(200)}, Hash: strings.Repeat("b", 64), Summary: "expected source version is not linked"},
		},
		ObjectVersions: []ObjectVersion{
			{Ref: ObjectRef{Type: ObjectTypeClaim, ID: testID(100)}, Version: 3},
			{Ref: ObjectRef{Type: ObjectTypeSourceVersion, ID: testID(200)}, Version: 7},
		},
	}
	if err := ValidateIssueObservation(testID(900), observation); err != nil {
		t.Fatalf("valid observation rejected: %v", err)
	}
	return observation
}

func validIssue(t *testing.T, now time.Time) Issue {
	t.Helper()
	observation := validObservation(t)
	identity, err := ComputeIssueIdentityHash(testID(900), observation)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := ComputeIssueFingerprint(testID(900), observation)
	if err != nil {
		t.Fatal(err)
	}
	issue := Issue{
		ID:              testID(910),
		WorkspaceID:     testID(900),
		Type:            observation.Type,
		Target:          observation.Target,
		DetectorID:      observation.DetectorID,
		DetectorVersion: observation.DetectorVersion,
		IdentityHash:    identity,
		Fingerprint:     fingerprint,
		Severity:        observation.Severity,
		EvidenceSummary: observation.EvidenceSummary,
		Evidence:        cloneEvidence(observation.Evidence),
		ObjectVersions:  cloneObjectVersions(observation.ObjectVersions),
		RepairOptions: []RepairOption{
			{Code: "health.repair.verify-provenance", Title: "绑定来源并重新验证", Available: true},
			{Code: "health.repair.review-owner", Title: "等待 Review owner", Available: false, UnavailableReason: "review capability unavailable"},
		},
		Status:          IssueStatusOpen,
		FirstDetectedAt: now.Add(-2 * time.Hour),
		LastDetectedAt:  now.Add(-2 * time.Hour),
		LastVerifiedAt:  now.Add(-2 * time.Hour),
		Version:         3,
		CreatedAt:       now.Add(-2 * time.Hour),
		UpdatedAt:       now.Add(-2 * time.Hour),
	}
	if err := ValidateIssue(issue); err != nil {
		t.Fatalf("valid issue rejected: %v", err)
	}
	return issue
}

func validScan(t *testing.T, now time.Time) Scan {
	t.Helper()
	scan := Scan{
		ID:             testID(920),
		WorkspaceID:    testID(900),
		WorkflowRunID:  testID(921),
		Scope:          ScanScope{Type: ScanScopeTypeWorkspace, Ref: testID(900), Version: 1, SchemaVersion: "health-scope/workspace/v1"},
		Fingerprint:    strings.Repeat("c", 64),
		IdempotencyKey: "scan-1",
		RequestHash:    strings.Repeat("d", 64),
		MaxItems:       100,
		Status:         ScanStatusSucceeded,
		Counters:       ScanCounters{Processed: 12, Created: 1, Reopened: 0, Resolved: 1, Unchanged: 10, Failed: 0},
		Coverage: []DetectorCoverage{
			{
				DetectorID:      "health.detector.missing_source",
				DetectorVersion: "detector/v1",
				Status:          DetectorCoverageStatusSucceeded,
				Counters:        ScanCounters{Processed: 10, Created: 1, Reopened: 0, Resolved: 1, Unchanged: 8, Failed: 0},
			},
			{
				DetectorID:      "health.detector.low_confidence",
				DetectorVersion: "detector/v1",
				Status:          DetectorCoverageStatusSucceeded,
				Counters:        ScanCounters{Processed: 2, Created: 0, Reopened: 0, Resolved: 0, Unchanged: 2, Failed: 0},
			},
		},
		Version:     2,
		CreatedAt:   now,
		UpdatedAt:   now.Add(5 * time.Minute),
		CompletedAt: timePtr(now.Add(5 * time.Minute)),
	}
	if err := ValidateScan(scan); err != nil {
		t.Fatalf("valid scan rejected: %v", err)
	}
	return scan
}

func validSchedule(now time.Time) Schedule {
	schedule := Schedule{
		ID:          testID(930),
		WorkspaceID: testID(900),
		Scope:       ScanScope{Type: ScanScopeTypeWorkspace, Ref: testID(900), Version: 1, SchemaVersion: "health-scope/workspace/v1"},
		Cadence:     ScheduleCadenceDisabled,
		Timezone:    "Asia/Shanghai",
		MaxItems:    100,
		NextRunAt:   timePtr(now.Add(24 * time.Hour)),
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	return schedule
}

func cloneEvidence(values []IssueEvidence) []IssueEvidence {
	cloned := make([]IssueEvidence, len(values))
	copy(cloned, values)
	return cloned
}

func cloneObjectVersions(values []ObjectVersion) []ObjectVersion {
	cloned := make([]ObjectVersion, len(values))
	copy(cloned, values)
	return cloned
}

func testID(number int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", number))
}

func fixedTime() time.Time {
	return time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
}

func timePtr(value time.Time) *time.Time { return &value }

func idPtr(value foundation.ID) *foundation.ID { return &value }

func hasCode(err error, code string) bool {
	var target *foundation.Error
	if !errors.As(err, &target) {
		return false
	}
	return target.Code == code
}
