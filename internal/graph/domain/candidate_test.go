package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestValidateSemanticLinkCandidateAcceptsAllDiscoveryMethods(t *testing.T) {
	candidate := validSemanticLinkCandidate(t)
	candidate.DiscoveryMethods = []SemanticLinkDiscoveryMethod{
		SemanticLinkDiscoveryMethodClaimSemanticSimilarity,
		SemanticLinkDiscoveryMethodCommonTopic,
		SemanticLinkDiscoveryMethodRAGCoRetrieval,
		SemanticLinkDiscoveryMethodSharedSource,
		SemanticLinkDiscoveryMethodTermMatch,
		SemanticLinkDiscoveryMethodTitleAlias,
	}
	candidate.Fingerprint = mustCandidateFingerprint(t, candidate)
	if err := ValidateSemanticLinkCandidate(candidate); err != nil {
		t.Fatalf("ValidateSemanticLinkCandidate() error = %v", err)
	}
}

func TestComputeSemanticLinkCandidateFingerprintCanonicalizesSymmetricEndpointsAndOrder(t *testing.T) {
	candidate := validSymmetricSemanticLinkCandidate(t)
	forward, err := ComputeSemanticLinkCandidateFingerprint(candidate)
	if err != nil {
		t.Fatal(err)
	}

	reversed := candidate
	reversed.Source, reversed.Target = candidate.Target, candidate.Source
	reversed.Evidence = []SemanticLinkCandidateEvidence{candidate.Evidence[1], candidate.Evidence[0]}
	reversed.DiscoveryMethods = []SemanticLinkDiscoveryMethod{candidate.DiscoveryMethods[1], candidate.DiscoveryMethods[0]}
	backward, err := ComputeSemanticLinkCandidateFingerprint(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if forward != backward {
		t.Fatalf("fingerprint mismatch after canonicalization: %q != %q", forward, backward)
	}
}

func TestComputeSemanticLinkCandidateFingerprintBindsSemanticInputs(t *testing.T) {
	base := validSymmetricSemanticLinkCandidate(t)
	original, err := ComputeSemanticLinkCandidateFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		mutate func(*SemanticLinkCandidate)
	}{
		{
			name: "source version",
			mutate: func(candidate *SemanticLinkCandidate) {
				candidate.Source.Version++
			},
		},
		{
			name: "target version",
			mutate: func(candidate *SemanticLinkCandidate) {
				candidate.Target.Version++
			},
		},
		{
			name: "relation type",
			mutate: func(candidate *SemanticLinkCandidate) {
				candidate.SuggestedRelationType = knowledge.RelationConflictsWith
			},
		},
		{
			name: "evidence hash",
			mutate: func(candidate *SemanticLinkCandidate) {
				candidate.Evidence[0].SemanticHash = strings.Repeat("3", 64)
			},
		},
		{
			name: "generation version",
			mutate: func(candidate *SemanticLinkCandidate) {
				candidate.Generation.RuleVersion = "rule-v2"
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := base
			candidate.Evidence = append([]SemanticLinkCandidateEvidence(nil), base.Evidence...)
			testCase.mutate(&candidate)
			value, err := ComputeSemanticLinkCandidateFingerprint(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if value == original {
				t.Fatalf("fingerprint did not change for %s", testCase.name)
			}
		})
	}
}

func TestValidateSemanticLinkCandidateTransitionMatrix(t *testing.T) {
	allowed := [][2]SemanticLinkCandidateStatus{
		{SemanticLinkCandidateStatusActive, SemanticLinkCandidateStatusDeferred},
		{SemanticLinkCandidateStatusActive, SemanticLinkCandidateStatusProposalCreated},
		{SemanticLinkCandidateStatusDeferred, SemanticLinkCandidateStatusActive},
		{SemanticLinkCandidateStatusDeferred, SemanticLinkCandidateStatusSuperseded},
		{SemanticLinkCandidateStatusProposalCreated, SemanticLinkCandidateStatusSuperseded},
	}
	for _, transition := range allowed {
		if err := ValidateSemanticLinkCandidateTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("ValidateSemanticLinkCandidateTransition(%s -> %s) error = %v", transition[0], transition[1], err)
		}
	}

	for _, transition := range [][2]SemanticLinkCandidateStatus{
		{SemanticLinkCandidateStatusIgnored, SemanticLinkCandidateStatusActive},
		{SemanticLinkCandidateStatusFalsePositive, SemanticLinkCandidateStatusDeferred},
		{SemanticLinkCandidateStatusProposalCreated, SemanticLinkCandidateStatusActive},
		{SemanticLinkCandidateStatusSuperseded, SemanticLinkCandidateStatusActive},
	} {
		assertCandidateCode(t, ValidateSemanticLinkCandidateTransition(transition[0], transition[1]), ErrorCodeSemanticLinkCandidateTransitionInvalid)
	}
}

func TestValidateSemanticLinkCandidateDecisionAndNextStatus(t *testing.T) {
	candidate := validSemanticLinkCandidate(t)

	confirm := SemanticLinkCandidateDecision{Action: SemanticLinkCandidateDecisionConfirm}
	if err := ValidateSemanticLinkCandidateDecision(candidate, confirm); err != nil {
		t.Fatalf("confirm decision error = %v", err)
	}
	status, err := NextSemanticLinkCandidateStatus(candidate.Status, confirm)
	if err != nil || status != SemanticLinkCandidateStatusProposalCreated {
		t.Fatalf("confirm next status = %s, err = %v", status, err)
	}

	typedConfirm := SemanticLinkCandidateDecision{
		Action:       SemanticLinkCandidateDecisionConfirmWithRelationType,
		RelationType: ptrRelationType(knowledge.RelationSupports),
	}
	if err := ValidateSemanticLinkCandidateDecision(candidate, typedConfirm); err != nil {
		t.Fatalf("typed confirm error = %v", err)
	}

	ignore := SemanticLinkCandidateDecision{Action: SemanticLinkCandidateDecisionIgnore, Reason: "canonical reason"}
	if err := ValidateSemanticLinkCandidateDecision(candidate, ignore); err != nil {
		t.Fatalf("ignore decision error = %v", err)
	}

	resumeAt := time.Date(2026, 7, 20, 9, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	deferDecision := SemanticLinkCandidateDecision{Action: SemanticLinkCandidateDecisionDefer, Reason: "later review", ResumeAfter: &resumeAt}
	if err := ValidateSemanticLinkCandidateDecision(candidate, deferDecision); err != nil {
		t.Fatalf("defer decision error = %v", err)
	}

	deferred := candidate
	deferred.Status = SemanticLinkCandidateStatusDeferred
	deferred.ResumeAfter = ptrTimeCandidate(resumeAt.UTC())
	deferred.Fingerprint = mustCandidateFingerprint(t, deferred)
	resume := SemanticLinkCandidateDecision{Action: SemanticLinkCandidateDecisionResume}
	if err := ValidateSemanticLinkCandidateDecision(deferred, resume); err != nil {
		t.Fatalf("resume decision error = %v", err)
	}
	status, err = NextSemanticLinkCandidateStatus(deferred.Status, resume)
	if err != nil || status != SemanticLinkCandidateStatusActive {
		t.Fatalf("resume next status = %s, err = %v", status, err)
	}

	proposalCreated := candidate
	proposalCreated.Status = SemanticLinkCandidateStatusProposalCreated
	proposalCreated.ProposalID = ptrIDCandidate(candidateTestID(99))
	proposalCreated.Fingerprint = mustCandidateFingerprint(t, proposalCreated)
	assertCandidateCode(t, ValidateSemanticLinkCandidateDecision(proposalCreated, ignore), ErrorCodeSemanticLinkCandidateTransitionInvalid)

	nonCanonicalReason := SemanticLinkCandidateDecision{Action: SemanticLinkCandidateDecisionIgnore, Reason: "  not canonical   "}
	assertCandidateCode(t, ValidateSemanticLinkCandidateDecision(candidate, nonCanonicalReason), ErrorCodeSemanticLinkCandidateDecisionInvalid)

	badTyped := SemanticLinkCandidateDecision{
		Action:       SemanticLinkCandidateDecisionConfirmWithRelationType,
		RelationType: ptrRelationType(knowledge.RelationBelongsTo),
	}
	assertCandidateCode(t, ValidateSemanticLinkCandidateDecision(candidate, badTyped), ErrorCodeSemanticLinkCandidateDecisionInvalid)

	badResume := SemanticLinkCandidateDecision{Action: SemanticLinkCandidateDecisionResume, Reason: "unexpected"}
	assertCandidateCode(t, ValidateSemanticLinkCandidateDecision(deferred, badResume), ErrorCodeSemanticLinkCandidateDecisionInvalid)
}

func TestValidateSemanticLinkCandidateQueryAndPage(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	workspaceID := candidateTestID(900)
	nodeRef := candidateRef(knowledge.NodeTypeClaim, 1)
	minConfidence := 0.6
	query := SemanticLinkCandidateQuery{
		WorkspaceID:     workspaceID,
		NodeRef:         &nodeRef,
		Statuses:        []SemanticLinkCandidateStatus{SemanticLinkCandidateStatusActive},
		RelationTypes:   []knowledge.RelationType{knowledge.RelationSupports},
		ReopenedReasons: []SemanticLinkCandidateReopenedReason{SemanticLinkCandidateReopenedReasonContentChanged},
		MinConfidence:   &minConfidence,
		Limit:           2,
	}
	if err := ValidateSemanticLinkCandidateQuery(query); err != nil {
		t.Fatalf("ValidateSemanticLinkCandidateQuery() error = %v", err)
	}

	active := validSemanticLinkCandidateAt(t, now)
	active.WorkspaceID = workspaceID
	active.Confidence = 0.9
	active.ReopenedFromCandidateID = ptrIDCandidate(candidateTestID(777))
	active.ReopenedReason = SemanticLinkCandidateReopenedReasonContentChanged
	active.Fingerprint = mustCandidateFingerprint(t, active)

	second := validSemanticLinkCandidateAt(t, now.Add(-time.Minute))
	second.ID = candidateTestID(2)
	second.WorkspaceID = workspaceID
	second.Confidence = 0.7
	second.ReopenedFromCandidateID = ptrIDCandidate(candidateTestID(778))
	second.ReopenedReason = SemanticLinkCandidateReopenedReasonContentChanged
	second.Fingerprint = mustCandidateFingerprint(t, second)

	page := SemanticLinkCandidatePage{
		WorkspaceID: workspaceID,
		Items:       []SemanticLinkCandidate{active, second},
		Meta:        PageMeta{Fingerprint: strings.Repeat("a", 64), Complete: true},
	}
	if err := ValidateSemanticLinkCandidatePage(query, page); err != nil {
		t.Fatalf("ValidateSemanticLinkCandidatePage() error = %v", err)
	}

	badOrder := page
	badOrder.Items = []SemanticLinkCandidate{second, active}
	assertCandidateCode(t, ValidateSemanticLinkCandidatePage(query, badOrder), ErrorCodeSemanticLinkCandidateResultInvalid)

	belowThreshold := page
	belowThreshold.Items = []SemanticLinkCandidate{active, second}
	belowThreshold.Items[1].Confidence = 0.2
	belowThreshold.Items[1].Fingerprint = mustCandidateFingerprint(t, belowThreshold.Items[1])
	assertCandidateCode(t, ValidateSemanticLinkCandidatePage(query, belowThreshold), ErrorCodeSemanticLinkCandidateResultInvalid)

	duplicateFilter := query
	duplicateFilter.Statuses = []SemanticLinkCandidateStatus{SemanticLinkCandidateStatusActive, SemanticLinkCandidateStatusActive}
	assertCandidateCode(t, ValidateSemanticLinkCandidateQuery(duplicateFilter), ErrorCodeSemanticLinkCandidateRequestInvalid)
}

func validSemanticLinkCandidate(t *testing.T) SemanticLinkCandidate {
	t.Helper()
	return validSemanticLinkCandidateAt(t, time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC))
}

func validSemanticLinkCandidateAt(t *testing.T, now time.Time) SemanticLinkCandidate {
	t.Helper()
	candidate := SemanticLinkCandidate{
		ID:                    candidateTestID(1),
		WorkspaceID:           candidateTestID(900),
		Source:                candidateEndpoint(knowledge.NodeTypeClaim, 1, 2),
		Target:                candidateEndpoint(knowledge.NodeTypeClaim, 2, 3),
		SuggestedRelationType: knowledge.RelationSupports,
		Status:                SemanticLinkCandidateStatusActive,
		Reason:                "shared evidence supports a relation",
		Confidence:            0.85,
		DiscoveryMethods: []SemanticLinkDiscoveryMethod{
			SemanticLinkDiscoveryMethodCommonTopic,
			SemanticLinkDiscoveryMethodSharedSource,
		},
		Evidence: []SemanticLinkCandidateEvidence{
			candidateEvidence(1, "1111111111111111111111111111111111111111111111111111111111111111"),
			candidateEvidence(2, "2222222222222222222222222222222222222222222222222222222222222222"),
		},
		Generation: SemanticLinkCandidateGeneration{
			RuleID:      ptrIDCandidate(candidateTestID(700)),
			RuleVersion: "rule-v1",
		},
		Version:   1,
		CreatedAt: now.Add(-time.Minute),
		UpdatedAt: now,
	}
	candidate.Fingerprint = mustCandidateFingerprint(t, candidate)
	return candidate
}

func validSymmetricSemanticLinkCandidate(t *testing.T) SemanticLinkCandidate {
	t.Helper()
	candidate := validSemanticLinkCandidate(t)
	candidate.Source = candidateEndpoint(knowledge.NodeTypeClaim, 3, 2)
	candidate.Target = candidateEndpoint(knowledge.NodeTypeClaim, 4, 3)
	candidate.SuggestedRelationType = knowledge.RelationDuplicates
	candidate.DiscoveryMethods = []SemanticLinkDiscoveryMethod{
		SemanticLinkDiscoveryMethodRAGCoRetrieval,
		SemanticLinkDiscoveryMethodTermMatch,
	}
	candidate.Fingerprint = mustCandidateFingerprint(t, candidate)
	return candidate
}

func candidateEndpoint(nodeType knowledge.NodeType, n int, version int64) SemanticLinkCandidateEndpoint {
	return SemanticLinkCandidateEndpoint{
		Ref:     candidateRef(nodeType, n),
		Version: version,
		Summary: fmt.Sprintf("summary %d", n),
		Excerpt: fmt.Sprintf("excerpt %d", n),
	}
}

func candidateEvidence(n int, hash string) SemanticLinkCandidateEvidence {
	workspaceID := candidateTestID(900)
	return SemanticLinkCandidateEvidence{
		ID: candidateTestID(100 + n),
		Provenance: knowledge.ProvenanceRef{
			WorkspaceID:     workspaceID,
			SourceVersionID: candidateTestID(200 + n),
			SourceSpanID:    candidateTestID(300 + n),
		},
		SemanticHash: hash,
		Reason:       fmt.Sprintf("reason %d", n),
		Excerpt:      fmt.Sprintf("excerpt %d", n),
	}
}

func mustCandidateFingerprint(t *testing.T, candidate SemanticLinkCandidate) string {
	t.Helper()
	value, err := ComputeSemanticLinkCandidateFingerprint(candidate)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func candidateRef(nodeType knowledge.NodeType, n int) knowledge.NodeRef {
	return knowledge.NodeRef{Type: nodeType, ID: candidateTestID(n)}
}

func candidateTestID(n int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", n))
}

func ptrIDCandidate(value foundation.ID) *foundation.ID {
	return &value
}

func ptrRelationType(value knowledge.RelationType) *knowledge.RelationType {
	return &value
}

func ptrTimeCandidate(value time.Time) *time.Time {
	return &value
}

func assertCandidateCode(t *testing.T, err error, want string) {
	t.Helper()
	var target *foundation.Error
	if !errors.As(err, &target) || target.Code != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}
