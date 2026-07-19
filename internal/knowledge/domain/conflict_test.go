package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestConflictStateMachineCoversFrozenMatrix(t *testing.T) {
	allowed := map[[2]ConflictStatus]bool{
		{ConflictStatusOpen, ConflictStatusInvestigating}:                    true,
		{ConflictStatusOpen, ConflictStatusDeferred}:                         true,
		{ConflictStatusInvestigating, ConflictStatusResolutionProposed}:      true,
		{ConflictStatusInvestigating, ConflictStatusDeferred}:                true,
		{ConflictStatusDeferred, ConflictStatusInvestigating}:                true,
		{ConflictStatusResolutionProposed, ConflictStatusResolved}:           true,
		{ConflictStatusResolutionProposed, ConflictStatusAcceptedDivergence}: true,
		{ConflictStatusResolutionProposed, ConflictStatusInvestigating}:      true,
	}
	statuses := []ConflictStatus{ConflictStatusOpen, ConflictStatusInvestigating, ConflictStatusResolutionProposed, ConflictStatusResolved, ConflictStatusAcceptedDivergence, ConflictStatusDeferred, ConflictStatus("unknown")}
	for _, from := range statuses {
		for _, to := range statuses {
			err := ValidateConflictTransition(from, to)
			if allowed[[2]ConflictStatus{from, to}] != (err == nil) {
				t.Fatalf("transition %s -> %s mismatch: %v", from, to, err)
			}
		}
	}
}

func TestExactConflictRequiresTwoDistinctClaimsWithEqualApplicability(t *testing.T) {
	conflict, members := validConflict(t, ApplicabilityAssessmentExact)
	if err := ValidateConflictAggregate(conflict, members); err != nil {
		t.Fatalf("valid exact conflict rejected: %v", err)
	}
	if err := ValidateConflictAggregate(conflict, members[:1]); err == nil {
		t.Fatal("single-member conflict must be rejected")
	}
	duplicate := append([]ConflictMember(nil), members...)
	duplicate[1].ClaimID = duplicate[0].ClaimID
	if err := ValidateConflictAggregate(conflict, duplicate); err == nil {
		t.Fatal("duplicate claim membership must be rejected")
	}
	different, err := ParseApplicability(json.RawMessage(`{"region":"us"}`))
	if err != nil {
		t.Fatal(err)
	}
	mismatch := append([]ConflictMember(nil), members...)
	mismatch[1].Applicability = different
	mismatch[1].ApplicabilityHash = different.Hash
	conflict.Fingerprint = ComputeConflictFingerprint(conflict.WorkspaceID, conflict.ApplicabilityAssessment, mismatch)
	if err := ValidateConflictAggregate(conflict, mismatch); err == nil {
		t.Fatal("exact conflict with different applicability must be rejected")
	}
}

func TestReviewedOverlapRequiresVersionedReasonAndDifferentApplicability(t *testing.T) {
	conflict, members := validConflict(t, ApplicabilityAssessmentReviewedOverlap)
	if err := ValidateConflictAggregate(conflict, members); err != nil {
		t.Fatalf("valid reviewed overlap rejected: %v", err)
	}
	missingReason := conflict
	missingReason.ReviewedOverlapReason = ""
	if err := ValidateConflictAggregate(missingReason, members); err == nil {
		t.Fatal("reviewed overlap without analysis reason must be rejected")
	}
	same := append([]ConflictMember(nil), members...)
	same[1].Applicability = same[0].Applicability
	same[1].ApplicabilityHash = same[0].ApplicabilityHash
	conflict.Fingerprint = ComputeConflictFingerprint(conflict.WorkspaceID, conflict.ApplicabilityAssessment, same)
	if err := ValidateConflictAggregate(conflict, same); err == nil {
		t.Fatal("reviewed overlap must not be used for equal applicability hashes")
	}
}

func TestAcceptedDivergenceRequiresResolutionAndPreservesFingerprint(t *testing.T) {
	conflict, members := validConflict(t, ApplicabilityAssessmentReviewedOverlap)
	conflict.Status = ConflictStatusAcceptedDivergence
	if err := ValidateConflictAggregate(conflict, members); err == nil {
		t.Fatal("accepted divergence without resolution must be rejected")
	}
	resolution := "不同地区条件下两项主张分别成立"
	reference := "resolution:v1:manual-42"
	conflict.Resolution = &resolution
	conflict.ResolutionReference = &reference
	if err := ValidateConflictAggregate(conflict, members); err != nil {
		t.Fatalf("accepted divergence with resolution rejected: %v", err)
	}
	reversed := []ConflictMember{members[1], members[0]}
	if ComputeConflictFingerprint(conflict.WorkspaceID, conflict.ApplicabilityAssessment, reversed) != conflict.Fingerprint {
		t.Fatal("conflict fingerprint must be independent of member order")
	}
}

func validConflict(t *testing.T, assessment ApplicabilityAssessment) (Conflict, []ConflictMember) {
	t.Helper()
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	firstApp, err := ParseApplicability(json.RawMessage(`{"region":"cn"}`))
	if err != nil {
		t.Fatal(err)
	}
	secondApp := firstApp
	reason := ""
	if assessment == ApplicabilityAssessmentReviewedOverlap {
		secondApp, err = ParseApplicability(json.RawMessage(`{"region":"us"}`))
		if err != nil {
			t.Fatal(err)
		}
		reason = "人工核对 v1：两个地区条件存在业务重叠"
	}
	conflict := Conflict{
		ID: uuid(80), WorkspaceID: uuid(2), Status: ConflictStatusOpen, Severity: ConflictSeverityHigh,
		Summary: "两个主张不能同时成立", ApplicabilityAssessment: assessment, ReviewedOverlapReason: reason,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	members := []ConflictMember{
		{ConflictID: conflict.ID, WorkspaceID: conflict.WorkspaceID, ClaimID: uuid(81), Applicability: firstApp, ApplicabilityHash: firstApp.Hash, PositionSummary: "主张 A 成立", CreatedAt: now},
		{ConflictID: conflict.ID, WorkspaceID: conflict.WorkspaceID, ClaimID: uuid(82), Applicability: secondApp, ApplicabilityHash: secondApp.Hash, PositionSummary: "主张 B 成立", CreatedAt: now},
	}
	conflict.Fingerprint = ComputeConflictFingerprint(conflict.WorkspaceID, conflict.ApplicabilityAssessment, members)
	return conflict, members
}
