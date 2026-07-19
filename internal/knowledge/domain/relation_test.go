package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRelationCompatibilityMatrixIsExact(t *testing.T) {
	claimA := NodeRef{Type: NodeTypeClaim, ID: uuid(31)}
	claimB := NodeRef{Type: NodeTypeClaim, ID: uuid(32)}
	topicA := NodeRef{Type: NodeTypeTopic, ID: uuid(33)}
	topicB := NodeRef{Type: NodeTypeTopic, ID: uuid(34)}
	all := []NodeRef{claimA, claimB, topicA, topicB}
	types := []RelationType{RelationCites, RelationDerivedFrom, RelationBelongsTo, RelationSupports, RelationComplements, RelationDuplicates, RelationConflictsWith, RelationPrerequisiteOf, RelationVersionOf, RelationImpacts}

	want := func(relationType RelationType, source, target NodeRef) bool {
		sameType := source.Type == target.Type
		switch relationType {
		case RelationCites, RelationDerivedFrom, RelationSupports, RelationConflictsWith:
			return source.Type == NodeTypeClaim && target.Type == NodeTypeClaim
		case RelationBelongsTo:
			return source.Type == NodeTypeClaim && target.Type == NodeTypeTopic
		case RelationComplements, RelationDuplicates, RelationPrerequisiteOf, RelationVersionOf:
			return sameType
		case RelationImpacts:
			return true
		default:
			return false
		}
	}
	for _, relationType := range types {
		for _, source := range all {
			for _, target := range all {
				if source.ID == target.ID {
					continue
				}
				if got := RelationTypeCompatible(relationType, source.Type, target.Type); got != want(relationType, source, target) {
					t.Fatalf("compatibility mismatch: %s %s -> %s = %t", relationType, source.Type, target.Type, got)
				}
			}
		}
	}
	if RelationTypeCompatible(RelationType("UNKNOWN"), NodeTypeClaim, NodeTypeClaim) {
		t.Fatal("unknown relation type must not be compatible")
	}
}

func TestCanonicalizeSymmetricRelationEndpointsDeduplicatesReverseInput(t *testing.T) {
	claimA := NodeRef{Type: NodeTypeClaim, ID: uuid(41)}
	claimB := NodeRef{Type: NodeTypeClaim, ID: uuid(40)}
	for _, relationType := range []RelationType{RelationDuplicates, RelationConflictsWith} {
		forwardSource, forwardTarget, err := CanonicalizeRelationEndpoints(relationType, claimA, claimB)
		if err != nil {
			t.Fatal(err)
		}
		reverseSource, reverseTarget, err := CanonicalizeRelationEndpoints(relationType, claimB, claimA)
		if err != nil {
			t.Fatal(err)
		}
		if forwardSource != reverseSource || forwardTarget != reverseTarget || forwardSource.ID != uuid(40) {
			t.Fatalf("reverse input was not canonicalized: %#v %#v / %#v %#v", forwardSource, forwardTarget, reverseSource, reverseTarget)
		}
	}

	nonSymmetricSource, nonSymmetricTarget, err := CanonicalizeRelationEndpoints(RelationSupports, claimA, claimB)
	if err != nil || nonSymmetricSource != claimA || nonSymmetricTarget != claimB {
		t.Fatalf("directed relation order changed: %#v %#v %v", nonSymmetricSource, nonSymmetricTarget, err)
	}
}

func TestValidateRelationEndpointsRejectsSelfCrossWorkspaceAndRetiredNodes(t *testing.T) {
	workspaceID := uuid(2)
	claimA := NodeDescriptor{Ref: NodeRef{Type: NodeTypeClaim, ID: uuid(51)}, WorkspaceID: workspaceID, Lifecycle: NodeLifecycleConfirmed}
	claimB := NodeDescriptor{Ref: NodeRef{Type: NodeTypeClaim, ID: uuid(52)}, WorkspaceID: workspaceID, Lifecycle: NodeLifecycleSuggested}
	if _, _, err := ValidateRelationEndpoints(workspaceID, RelationSupports, claimA, claimB); err != nil {
		t.Fatalf("valid endpoints rejected: %v", err)
	}
	self := claimA
	if _, _, err := ValidateRelationEndpoints(workspaceID, RelationSupports, claimA, self); err == nil {
		t.Fatal("self relation must be rejected")
	}
	crossWorkspace := claimB
	crossWorkspace.WorkspaceID = uuid(99)
	if _, _, err := ValidateRelationEndpoints(workspaceID, RelationSupports, claimA, crossWorkspace); err == nil {
		t.Fatal("cross workspace relation must be rejected")
	}
	retired := claimB
	retired.Lifecycle = NodeLifecycleDeprecated
	if _, _, err := ValidateRelationEndpoints(workspaceID, RelationSupports, claimA, retired); err == nil {
		t.Fatal("retired relation endpoint must be rejected")
	}
	topicWithClaimLifecycle := NodeDescriptor{Ref: NodeRef{Type: NodeTypeTopic, ID: uuid(53)}, WorkspaceID: workspaceID, Lifecycle: NodeLifecycleConfirmed}
	if _, _, err := ValidateRelationEndpoints(workspaceID, RelationBelongsTo, claimA, topicWithClaimLifecycle); err == nil {
		t.Fatal("topic endpoint with claim lifecycle must be rejected")
	}
}

func TestRelationStateMachineAndRejectedReopenRequireNewEvidence(t *testing.T) {
	allowed := map[[2]RelationStatus]bool{
		{RelationStatusSuggested, RelationStatusConfirmed}:  true,
		{RelationStatusSuggested, RelationStatusRejected}:   true,
		{RelationStatusConfirmed, RelationStatusStale}:      true,
		{RelationStatusConfirmed, RelationStatusDeprecated}: true,
		{RelationStatusStale, RelationStatusConfirmed}:      true,
		{RelationStatusStale, RelationStatusRejected}:       true,
		{RelationStatusStale, RelationStatusDeprecated}:     true,
		{RelationStatusRejected, RelationStatusSuggested}:   true,
	}
	statuses := []RelationStatus{RelationStatusSuggested, RelationStatusConfirmed, RelationStatusRejected, RelationStatusStale, RelationStatusDeprecated, RelationStatus("unknown")}
	for _, from := range statuses {
		for _, to := range statuses {
			err := ValidateRelationTransition(from, to)
			if allowed[[2]RelationStatus{from, to}] != (err == nil) {
				t.Fatalf("transition %s -> %s mismatch: %v", from, to, err)
			}
		}
	}
	current := validRelation(t)
	current.Status = RelationStatusRejected
	current.EvidenceFingerprint = strings.Repeat("a", 64)
	if err := ValidateRelationTransitionCommand(current, RelationStatusSuggested, strings.Repeat("a", 64)); err == nil {
		t.Fatal("rejected relation cannot reopen with same evidence fingerprint")
	}
	if err := ValidateRelationTransitionCommand(current, RelationStatusSuggested, "not-a-hash"); err == nil {
		t.Fatal("rejected relation cannot reopen with malformed evidence fingerprint")
	}
	if err := ValidateRelationTransitionCommand(current, RelationStatusSuggested, strings.Repeat("b", 64)); err != nil {
		t.Fatalf("new evidence should reopen rejected relation: %v", err)
	}
}

func TestConfirmedRelationRequiresEvidenceAndBoundConfirmation(t *testing.T) {
	relation := validRelation(t)
	relation.Status = RelationStatusConfirmed
	confirmation := Confirmation{Method: ConfirmationUserApproval, Reference: "approval:42"}
	relation.Confirmation = &confirmation
	if err := ValidateRelationAggregate(relation, nil); err == nil {
		t.Fatal("confirmed relation without evidence must fail closed")
	}
	evidence := validRelationEvidence(t, relation, confirmation)
	relation.EvidenceFingerprint = ComputeRelationEvidenceFingerprint([]RelationEvidence{evidence})
	if err := ValidateRelationAggregate(relation, []RelationEvidence{evidence}); err != nil {
		t.Fatalf("confirmed relation with evidence rejected: %v", err)
	}
	tampered := evidence
	tampered.Confirmation = nil
	tampered.EvidenceHash = ComputeRelationEvidenceHash(tampered)
	relation.EvidenceFingerprint = ComputeRelationEvidenceFingerprint([]RelationEvidence{tampered})
	if err := ValidateRelationAggregate(relation, []RelationEvidence{tampered}); err == nil {
		t.Fatal("confirmed relation evidence must carry matching confirmation")
	}
}

func TestRejectedRelationMayRetainPreviouslyConfirmedHistory(t *testing.T) {
	relation := validRelation(t)
	confirmation := Confirmation{Method: ConfirmationUserApproval, Reference: "approval:historical"}
	relation.Status = RelationStatusRejected
	relation.Confirmation = &confirmation
	evidence := validRelationEvidence(t, relation, confirmation)
	relation.EvidenceFingerprint = ComputeRelationEvidenceFingerprint([]RelationEvidence{evidence})
	if err := ValidateRelationAggregate(relation, []RelationEvidence{evidence}); err != nil {
		t.Fatalf("rejected relation lost valid confirmation history: %v", err)
	}

	relation.Confirmation = nil
	evidence.Confirmation = nil
	evidence.EvidenceHash = ComputeRelationEvidenceHash(evidence)
	relation.EvidenceFingerprint = ComputeRelationEvidenceFingerprint([]RelationEvidence{evidence})
	if err := ValidateRelationAggregate(relation, []RelationEvidence{evidence}); err != nil {
		t.Fatalf("never-confirmed rejected relation should remain valid: %v", err)
	}
}

func TestMapAssessmentKeepsAnalysisOutcomeSeparateFromPersistedRelation(t *testing.T) {
	claimA := NodeRef{Type: NodeTypeClaim, ID: uuid(71)}
	claimB := NodeRef{Type: NodeTypeClaim, ID: uuid(72)}
	for _, assessment := range []RelationAssessment{AssessmentNew, AssessmentLowConfidence} {
		action, err := MapAssessment(assessment, NodeRef{}, NodeRef{}, false)
		if err != nil {
			t.Fatalf("map %s: %v", assessment, err)
		}
		if action.RelationType != nil || action.Decision == AssessmentDecisionSuggestRelation || action.OpenConflict {
			t.Fatalf("%s must never create formal relation/conflict: %#v", assessment, action)
		}
	}
	for assessment, wantType := range map[RelationAssessment]RelationType{
		AssessmentComplementary: RelationComplements,
		AssessmentDuplicate:     RelationDuplicates,
	} {
		action, err := MapAssessment(assessment, claimA, claimB, false)
		if err != nil || action.RelationType == nil || *action.RelationType != wantType || action.Decision != AssessmentDecisionSuggestRelation {
			t.Fatalf("unexpected %s action: %#v err=%v", assessment, action, err)
		}
	}
	conflict, err := MapAssessment(AssessmentConflict, claimA, claimB, true)
	if err != nil || !conflict.OpenConflict || conflict.RelationType == nil || *conflict.RelationType != RelationConflictsWith {
		t.Fatalf("conflict mapping mismatch: %#v err=%v", conflict, err)
	}
	if _, err := MapAssessment(AssessmentConflict, claimA, NodeRef{Type: NodeTypeTopic, ID: uuid(73)}, false); err == nil {
		t.Fatal("conflict assessment must require two claims")
	}
}

func validRelation(t *testing.T) Relation {
	t.Helper()
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	source := NodeRef{Type: NodeTypeClaim, ID: uuid(61)}
	target := NodeRef{Type: NodeTypeClaim, ID: uuid(62)}
	relation := Relation{
		ID: uuid(60), WorkspaceID: uuid(2), Source: source, Target: target, Type: RelationSupports,
		Status: RelationStatusSuggested, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	relation.Fingerprint = ComputeRelationFingerprint(relation.WorkspaceID, relation.Type, relation.Source, relation.Target)
	return relation
}

func validRelationEvidence(t *testing.T, relation Relation, confirmation Confirmation) RelationEvidence {
	t.Helper()
	app, err := ParseApplicability(json.RawMessage(`{"region":"cn"}`))
	if err != nil {
		t.Fatal(err)
	}
	evidence := RelationEvidence{
		ID: uuid(63), WorkspaceID: relation.WorkspaceID, RelationID: relation.ID,
		Provenance: ProvenanceRef{WorkspaceID: relation.WorkspaceID, SourceVersionID: uuid(64), SourceSpanID: uuid(65)},
		Reason:     "原文支持该关系", Applicability: app, Confirmation: &confirmation, CreatedAt: relation.CreatedAt,
	}
	evidence.EvidenceHash = ComputeRelationEvidenceHash(evidence)
	return evidence
}
