package candidateconfirm

import (
	"strings"
	"testing"

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
		RelationType: &relationType, Risk: " medium  risk ", RollbackPlan: " restore  candidate ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.IdempotencyKey != "confirm-key" || got.Risk != "medium risk" || got.RollbackPlan != "restore candidate" || got.RelationType == nil || *got.RelationType != knowledge.RelationConflictsWith {
		t.Fatalf("canonical command = %#v", got)
	}
}

func TestRequestHashBindsRiskRollbackAndTypedRelation(t *testing.T) {
	base := Command{
		WorkspaceID: confirmWorkspace, CandidateID: confirmCandidate, ExpectedVersion: 1,
		IdempotencyKey: "same-key", Action: graphdomain.SemanticLinkCandidateDecisionConfirm,
		Risk: "medium", RollbackPlan: "restore relation",
	}
	first, err := RequestHash(base)
	if err != nil {
		t.Fatal(err)
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
}

func TestCanonicalizeRejectsNonConfirmAndUnsafeFields(t *testing.T) {
	cases := []Command{
		{WorkspaceID: confirmWorkspace, CandidateID: confirmCandidate, ExpectedVersion: 1, IdempotencyKey: "k", Action: graphdomain.SemanticLinkCandidateDecisionIgnore, Risk: "r", RollbackPlan: "p"},
		{WorkspaceID: confirmWorkspace, CandidateID: confirmCandidate, ExpectedVersion: 1, IdempotencyKey: strings.Repeat("k", 129), Action: graphdomain.SemanticLinkCandidateDecisionConfirm, Risk: "r", RollbackPlan: "p"},
		{WorkspaceID: confirmWorkspace, CandidateID: confirmCandidate, ExpectedVersion: 1, IdempotencyKey: "k", Action: graphdomain.SemanticLinkCandidateDecisionConfirm, Risk: "r\x00", RollbackPlan: "p"},
	}
	for index, command := range cases {
		if _, err := Canonicalize(command); err == nil {
			t.Fatalf("case %d unexpectedly succeeded", index)
		}
	}
}
