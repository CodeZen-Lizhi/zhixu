package application

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestCanonicalSemanticLinkCandidateQueryHashIgnoresFilterOrderAndLimit(t *testing.T) {
	nodeRef := candidateApplicationRef(knowledge.NodeTypeClaim, 1)
	minConfidence := 0.7
	first := graphdomain.SemanticLinkCandidateQuery{
		WorkspaceID:     candidateApplicationID(900),
		NodeRef:         &nodeRef,
		Statuses:        []graphdomain.SemanticLinkCandidateStatus{graphdomain.SemanticLinkCandidateStatusDeferred, graphdomain.SemanticLinkCandidateStatusActive},
		RelationTypes:   []knowledge.RelationType{knowledge.RelationSupports, knowledge.RelationConflictsWith},
		ReopenedReasons: []graphdomain.SemanticLinkCandidateReopenedReason{graphdomain.SemanticLinkCandidateReopenedReasonContentChanged},
		MinConfidence:   &minConfidence,
		Limit:           10,
	}
	second := first
	second.Statuses = []graphdomain.SemanticLinkCandidateStatus{graphdomain.SemanticLinkCandidateStatusActive, graphdomain.SemanticLinkCandidateStatusDeferred}
	second.RelationTypes = []knowledge.RelationType{knowledge.RelationConflictsWith, knowledge.RelationSupports}
	second.Limit = 50

	firstHash, err := CanonicalSemanticLinkCandidateQueryHash(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := CanonicalSemanticLinkCandidateQueryHash(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("hash mismatch: %q != %q", firstHash, secondHash)
	}
}

func TestCanonicalizeSemanticLinkCandidateDecisionCommandNormalizesFields(t *testing.T) {
	resumeAt := time.Date(2026, 7, 20, 20, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	command := SemanticLinkCandidateDecisionCommand{
		WorkspaceID:     candidateApplicationID(900),
		CandidateID:     candidateApplicationID(1),
		ExpectedVersion: 3,
		IdempotencyKey:  "  idem-key  ",
		Decision: graphdomain.SemanticLinkCandidateDecision{
			Action:      graphdomain.SemanticLinkCandidateDecisionDefer,
			Reason:      "  review   later  ",
			ResumeAfter: &resumeAt,
		},
	}

	canonical, err := CanonicalizeSemanticLinkCandidateDecisionCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.IdempotencyKey != "idem-key" {
		t.Fatalf("IdempotencyKey = %q", canonical.IdempotencyKey)
	}
	if canonical.Decision.Reason != "review later" {
		t.Fatalf("Reason = %q", canonical.Decision.Reason)
	}
	if canonical.Decision.ResumeAfter == nil || canonical.Decision.ResumeAfter.Location() != time.UTC || canonical.Decision.ResumeAfter.Format(time.RFC3339) != "2026-07-20T12:00:00Z" {
		t.Fatalf("ResumeAfter = %#v", canonical.Decision.ResumeAfter)
	}

	padded := command
	padded.IdempotencyKey = " " + strings.Repeat("x", maxCandidateIdempotencyKeyLen) + " "
	padded.Decision.ResumeAfter = nil
	canonical, err = CanonicalizeSemanticLinkCandidateDecisionCommand(padded)
	if err != nil || canonical.IdempotencyKey != strings.Repeat("x", maxCandidateIdempotencyKeyLen) {
		t.Fatalf("padded idempotency key = %#v, err = %v", canonical, err)
	}
}

func TestComputeSemanticLinkCandidateDecisionRequestHashIgnoresIdempotencyKey(t *testing.T) {
	base := SemanticLinkCandidateDecisionCommand{
		WorkspaceID:     candidateApplicationID(900),
		CandidateID:     candidateApplicationID(1),
		ExpectedVersion: 2,
		IdempotencyKey:  "key-a",
		Decision: graphdomain.SemanticLinkCandidateDecision{
			Action: graphdomain.SemanticLinkCandidateDecisionIgnore,
			Reason: "canonical reason",
		},
	}
	other := base
	other.IdempotencyKey = "key-b"

	first, err := ComputeSemanticLinkCandidateDecisionRequestHash(base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ComputeSemanticLinkCandidateDecisionRequestHash(other)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("hash mismatch: %q != %q", first, second)
	}
}

func TestValidateSemanticLinkCandidateDecisionResultBindsCASAndActionOutcomes(t *testing.T) {
	proposalID := candidateApplicationID(700)
	cases := []struct {
		name    string
		command SemanticLinkCandidateDecisionCommand
		result  SemanticLinkCandidateDecisionResult
	}{
		{
			name: "confirm",
			command: SemanticLinkCandidateDecisionCommand{
				WorkspaceID:     candidateApplicationID(900),
				CandidateID:     candidateApplicationID(1),
				ExpectedVersion: 1,
				IdempotencyKey:  "confirm",
				Decision: graphdomain.SemanticLinkCandidateDecision{
					Action: graphdomain.SemanticLinkCandidateDecisionConfirm,
				},
			},
			result: SemanticLinkCandidateDecisionResult{
				Candidate:  applicationDecisionResultCandidate(t, graphdomain.SemanticLinkCandidateDecisionConfirm, 2, nil, &proposalID),
				ProposalID: &proposalID,
			},
		},
		{
			name: "typed confirm",
			command: SemanticLinkCandidateDecisionCommand{
				WorkspaceID:     candidateApplicationID(900),
				CandidateID:     candidateApplicationID(1),
				ExpectedVersion: 1,
				IdempotencyKey:  "typed-confirm",
				Decision: graphdomain.SemanticLinkCandidateDecision{
					Action:       graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType,
					RelationType: ptrApplicationRelationType(knowledge.RelationConflictsWith),
				},
			},
			result: SemanticLinkCandidateDecisionResult{
				Candidate:  applicationDecisionResultCandidate(t, graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType, 2, ptrApplicationRelationType(knowledge.RelationConflictsWith), &proposalID),
				ProposalID: &proposalID,
			},
		},
		{
			name: "ignore",
			command: SemanticLinkCandidateDecisionCommand{
				WorkspaceID:     candidateApplicationID(900),
				CandidateID:     candidateApplicationID(1),
				ExpectedVersion: 1,
				IdempotencyKey:  "ignore",
				Decision: graphdomain.SemanticLinkCandidateDecision{
					Action: graphdomain.SemanticLinkCandidateDecisionIgnore,
					Reason: "canonical reason",
				},
			},
			result: SemanticLinkCandidateDecisionResult{
				Candidate: applicationDecisionResultCandidate(t, graphdomain.SemanticLinkCandidateDecisionIgnore, 2, nil, nil),
			},
		},
		{
			name: "false positive",
			command: SemanticLinkCandidateDecisionCommand{
				WorkspaceID:     candidateApplicationID(900),
				CandidateID:     candidateApplicationID(1),
				ExpectedVersion: 1,
				IdempotencyKey:  "false-positive",
				Decision: graphdomain.SemanticLinkCandidateDecision{
					Action: graphdomain.SemanticLinkCandidateDecisionFalsePositive,
					Reason: "not applicable",
				},
			},
			result: SemanticLinkCandidateDecisionResult{
				Candidate: applicationDecisionResultCandidate(t, graphdomain.SemanticLinkCandidateDecisionFalsePositive, 2, nil, nil),
			},
		},
		{
			name: "defer",
			command: SemanticLinkCandidateDecisionCommand{
				WorkspaceID:     candidateApplicationID(900),
				CandidateID:     candidateApplicationID(1),
				ExpectedVersion: 1,
				IdempotencyKey:  "defer",
				Decision: graphdomain.SemanticLinkCandidateDecision{
					Action:      graphdomain.SemanticLinkCandidateDecisionDefer,
					Reason:      "review later",
					ResumeAfter: ptrApplicationTime(time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)),
				},
			},
			result: SemanticLinkCandidateDecisionResult{
				Candidate: applicationDecisionResultCandidate(t, graphdomain.SemanticLinkCandidateDecisionDefer, 2, nil, nil),
			},
		},
		{
			name: "resume",
			command: SemanticLinkCandidateDecisionCommand{
				WorkspaceID:     candidateApplicationID(900),
				CandidateID:     candidateApplicationID(1),
				ExpectedVersion: 3,
				IdempotencyKey:  "resume",
				Decision: graphdomain.SemanticLinkCandidateDecision{
					Action: graphdomain.SemanticLinkCandidateDecisionResume,
				},
			},
			result: SemanticLinkCandidateDecisionResult{
				Candidate: applicationDecisionResultCandidateFromDeferred(t, 4),
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := ValidateSemanticLinkCandidateDecisionResult(testCase.command, testCase.result); err != nil {
				t.Fatalf("ValidateSemanticLinkCandidateDecisionResult() error = %v", err)
			}
		})
	}

	t.Run("cas mismatch", func(t *testing.T) {
		command := SemanticLinkCandidateDecisionCommand{
			WorkspaceID:     candidateApplicationID(900),
			CandidateID:     candidateApplicationID(1),
			ExpectedVersion: 1,
			IdempotencyKey:  "ignore",
			Decision: graphdomain.SemanticLinkCandidateDecision{
				Action: graphdomain.SemanticLinkCandidateDecisionIgnore,
				Reason: "canonical reason",
			},
		}
		result := SemanticLinkCandidateDecisionResult{
			Candidate: applicationDecisionResultCandidate(t, graphdomain.SemanticLinkCandidateDecisionIgnore, 3, nil, nil),
		}
		assertCandidateApplicationCode(t, ValidateSemanticLinkCandidateDecisionResult(command, result), graphdomain.ErrorCodeSemanticLinkCandidateResultInvalid)
	})

	t.Run("typed confirm preserves candidate snapshot", func(t *testing.T) {
		command := SemanticLinkCandidateDecisionCommand{
			WorkspaceID:     candidateApplicationID(900),
			CandidateID:     candidateApplicationID(1),
			ExpectedVersion: 1,
			IdempotencyKey:  "typed-confirm",
			Decision: graphdomain.SemanticLinkCandidateDecision{
				Action:       graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType,
				RelationType: ptrApplicationRelationType(knowledge.RelationConflictsWith),
			},
		}
		proposalID := candidateApplicationID(700)
		result := SemanticLinkCandidateDecisionResult{
			Candidate:  applicationDecisionResultCandidate(t, graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType, 2, ptrApplicationRelationType(knowledge.RelationDuplicates), &proposalID),
			ProposalID: &proposalID,
		}
		if err := ValidateSemanticLinkCandidateDecisionResult(command, result); err != nil {
			t.Fatalf("typed confirm changed immutable candidate snapshot: %v", err)
		}
	})
}

func applicationDecisionResultCandidate(t *testing.T, action graphdomain.SemanticLinkCandidateDecisionAction, version int64, relationType *knowledge.RelationType, proposalID *foundation.ID) graphdomain.SemanticLinkCandidate {
	t.Helper()
	candidate := validApplicationSemanticLinkCandidate(t)
	candidate.Version = version
	candidate.UpdatedAt = candidate.UpdatedAt.Add(time.Duration(version) * time.Minute)
	candidate.ResumeAfter = nil
	switch action {
	case graphdomain.SemanticLinkCandidateDecisionConfirm:
		candidate.Status = graphdomain.SemanticLinkCandidateStatusProposalCreated
		candidate.ProposalID = proposalID
	case graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType:
		candidate.Status = graphdomain.SemanticLinkCandidateStatusProposalCreated
		candidate.ProposalID = proposalID
	case graphdomain.SemanticLinkCandidateDecisionIgnore:
		candidate.Status = graphdomain.SemanticLinkCandidateStatusIgnored
		candidate.ProposalID = nil
	case graphdomain.SemanticLinkCandidateDecisionFalsePositive:
		candidate.Status = graphdomain.SemanticLinkCandidateStatusFalsePositive
		candidate.ProposalID = nil
	case graphdomain.SemanticLinkCandidateDecisionDefer:
		candidate.Status = graphdomain.SemanticLinkCandidateStatusDeferred
		candidate.ProposalID = nil
		candidate.ResumeAfter = ptrApplicationTime(time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC))
	case graphdomain.SemanticLinkCandidateDecisionResume:
		candidate.Status = graphdomain.SemanticLinkCandidateStatusActive
		candidate.ProposalID = nil
	}
	candidate.Fingerprint = applicationCandidateFingerprint(t, candidate)
	return candidate
}

func applicationDecisionResultCandidateFromDeferred(t *testing.T, version int64) graphdomain.SemanticLinkCandidate {
	t.Helper()
	candidate := validApplicationSemanticLinkCandidate(t)
	candidate.Status = graphdomain.SemanticLinkCandidateStatusActive
	candidate.Version = version
	candidate.UpdatedAt = candidate.UpdatedAt.Add(time.Duration(version) * time.Minute)
	candidate.ResumeAfter = nil
	candidate.Fingerprint = applicationCandidateFingerprint(t, candidate)
	return candidate
}

func validApplicationSemanticLinkCandidate(t *testing.T) graphdomain.SemanticLinkCandidate {
	t.Helper()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	candidate := graphdomain.SemanticLinkCandidate{
		ID:                    candidateApplicationID(1),
		WorkspaceID:           candidateApplicationID(900),
		Source:                applicationCandidateEndpoint(knowledge.NodeTypeClaim, 1, 2),
		Target:                applicationCandidateEndpoint(knowledge.NodeTypeClaim, 2, 3),
		SuggestedRelationType: knowledge.RelationSupports,
		Status:                graphdomain.SemanticLinkCandidateStatusActive,
		Reason:                "shared evidence supports a relation",
		Confidence:            0.9,
		DiscoveryMethods: []graphdomain.SemanticLinkDiscoveryMethod{
			graphdomain.SemanticLinkDiscoveryMethodCommonTopic,
			graphdomain.SemanticLinkDiscoveryMethodSharedSource,
		},
		Evidence: []graphdomain.SemanticLinkCandidateEvidence{
			applicationCandidateEvidence(1, strings.Repeat("1", 64)),
			applicationCandidateEvidence(2, strings.Repeat("2", 64)),
		},
		Generation: graphdomain.SemanticLinkCandidateGeneration{
			RuleID:      ptrApplicationID(candidateApplicationID(700)),
			RuleVersion: "rule-v1",
		},
		Version:   1,
		CreatedAt: now.Add(-time.Minute),
		UpdatedAt: now,
	}
	candidate.Fingerprint = applicationCandidateFingerprint(t, candidate)
	return candidate
}

func applicationCandidateEndpoint(nodeType knowledge.NodeType, n int, version int64) graphdomain.SemanticLinkCandidateEndpoint {
	return graphdomain.SemanticLinkCandidateEndpoint{
		Ref:     candidateApplicationRef(nodeType, n),
		Version: version,
		Summary: fmt.Sprintf("summary %d", n),
		Excerpt: fmt.Sprintf("excerpt %d", n),
	}
}

func applicationCandidateEvidence(n int, hash string) graphdomain.SemanticLinkCandidateEvidence {
	workspaceID := candidateApplicationID(900)
	return graphdomain.SemanticLinkCandidateEvidence{
		ID: candidateApplicationID(100 + n),
		Provenance: knowledge.ProvenanceRef{
			WorkspaceID:     workspaceID,
			SourceVersionID: candidateApplicationID(200 + n),
			SourceSpanID:    candidateApplicationID(300 + n),
		},
		SemanticHash: hash,
		Reason:       fmt.Sprintf("reason %d", n),
		Excerpt:      fmt.Sprintf("excerpt %d", n),
	}
}

func applicationCandidateFingerprint(t *testing.T, candidate graphdomain.SemanticLinkCandidate) string {
	t.Helper()
	value, err := graphdomain.ComputeSemanticLinkCandidateFingerprint(candidate)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func candidateApplicationRef(nodeType knowledge.NodeType, n int) knowledge.NodeRef {
	return knowledge.NodeRef{Type: nodeType, ID: candidateApplicationID(n)}
}

func candidateApplicationID(n int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", n))
}

func ptrApplicationTime(value time.Time) *time.Time {
	return &value
}

func ptrApplicationID(value foundation.ID) *foundation.ID {
	return &value
}

func ptrApplicationRelationType(value knowledge.RelationType) *knowledge.RelationType {
	return &value
}

func assertCandidateApplicationCode(t *testing.T, err error, want string) {
	t.Helper()
	var target *foundation.Error
	if !errors.As(err, &target) || target.Code != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}
