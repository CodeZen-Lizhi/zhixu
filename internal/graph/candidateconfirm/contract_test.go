package candidateconfirm

import (
	"strings"
	"testing"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	confirmWorkspace = foundation.ID("81000000-0000-4000-8000-000000000001")
	confirmCandidate = foundation.ID("81000000-0000-4000-8000-000000000002")
)

func TestCanonicalizeNormalizesTypedConfirmation(t *testing.T) {
	relationType := knowledge.RelationType(" conflicts_with ")
	got, err := Canonicalize(Command{
		WorkspaceID: confirmWorkspace, CandidateID: confirmCandidate, ExpectedVersion: 3,
		IdempotencyKey: "  confirm-key ", Action: graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType,
		RelationType: &relationType, RiskLevel: changecontroldomain.ProposalRiskLevelHigh,
		Risk: " medium  risk ", RollbackPlan: " restore  candidate ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.IdempotencyKey != "confirm-key" || got.RiskLevel != changecontroldomain.ProposalRiskLevelHigh || got.Risk != "medium risk" || got.RollbackPlan != "restore candidate" || got.RelationType == nil || *got.RelationType != knowledge.RelationConflictsWith {
		t.Fatalf("canonical command = %#v", got)
	}
}

func TestRequestHashBindsRiskRollbackAndTypedRelation(t *testing.T) {
	base := Command{
		WorkspaceID: confirmWorkspace, CandidateID: confirmCandidate, ExpectedVersion: 1,
		IdempotencyKey: "same-key", Action: graphdomain.SemanticLinkCandidateDecisionConfirm,
		RiskLevel: changecontroldomain.ProposalRiskLevelHigh, Risk: "medium", RollbackPlan: "restore relation",
	}
	first, err := RequestHash(base)
	if err != nil {
		t.Fatal(err)
	}
	if first != "f57d41fd90df2a77cc27e89b76302a49a6a0b57a5b9ea4341e502f33039aa52d" {
		t.Fatalf("v2 request hash = %s", first)
	}
	changed := base
	changed.RollbackPlan = "manual review"
	second, err := RequestHash(changed)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("rollback plan did not participate in request hash")
	}
	typed := base
	relationType := knowledge.RelationComplements
	typed.Action = graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType
	typed.RelationType = &relationType
	third, err := RequestHash(typed)
	if err != nil {
		t.Fatal(err)
	}
	if first == third {
		t.Fatal("typed relation did not participate in request hash")
	}
	legacy, err := LegacyRequestHash(base)
	if err != nil {
		t.Fatal(err)
	}
	if legacy != "3117698ef3dec0dbe727f2f94d2c9250c4be9d3c18480b23b53eb0e13c8919b9" {
		t.Fatalf("legacy request hash = %s", legacy)
	}
	if first == legacy {
		t.Fatal("v2 request hash must differ from the frozen v1 receipt hash")
	}
}

func TestCanonicalizeRejectsNonConfirmAndUnsafeFields(t *testing.T) {
	cases := []Command{
		{WorkspaceID: confirmWorkspace, CandidateID: confirmCandidate, ExpectedVersion: 1, IdempotencyKey: "k", Action: graphdomain.SemanticLinkCandidateDecisionIgnore, RiskLevel: changecontroldomain.ProposalRiskLevelHigh, Risk: "r", RollbackPlan: "p"},
		{WorkspaceID: confirmWorkspace, CandidateID: confirmCandidate, ExpectedVersion: 1, IdempotencyKey: strings.Repeat("k", 129), Action: graphdomain.SemanticLinkCandidateDecisionConfirm, RiskLevel: changecontroldomain.ProposalRiskLevelHigh, Risk: "r", RollbackPlan: "p"},
		{WorkspaceID: confirmWorkspace, CandidateID: confirmCandidate, ExpectedVersion: 1, IdempotencyKey: "k", Action: graphdomain.SemanticLinkCandidateDecisionConfirm, RiskLevel: changecontroldomain.ProposalRiskLevelHigh, Risk: "r\x00", RollbackPlan: "p"},
		{WorkspaceID: confirmWorkspace, CandidateID: confirmCandidate, ExpectedVersion: 1, IdempotencyKey: "k", Action: graphdomain.SemanticLinkCandidateDecisionConfirm, RiskLevel: changecontroldomain.ProposalRiskLevelMedium, Risk: "r", RollbackPlan: "p"},
		{WorkspaceID: confirmWorkspace, CandidateID: confirmCandidate, ExpectedVersion: 1, IdempotencyKey: "k", Action: graphdomain.SemanticLinkCandidateDecisionConfirm, Risk: "r", RollbackPlan: "p"},
	}
	for index, command := range cases {
		if _, err := Canonicalize(command); err == nil {
			t.Fatalf("case %d unexpectedly succeeded", index)
		}
	}
}
