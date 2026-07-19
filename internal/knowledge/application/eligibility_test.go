package application

import (
	"context"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestCheckEvidenceEligibilityCanonicalizesAndValidatesRepositoryResult(t *testing.T) {
	workspace := testID(1)
	first := testProvenance(20)
	second := testProvenance(30)
	repository := &fakeRepository{batchEligibility: func(_ context.Context, query domain.EvidenceEligibilityQuery) ([]domain.ProvenanceEligibility, error) {
		if query.WorkspaceID != workspace || query.Provenance[0] != first || query.Provenance[1] != second {
			t.Fatalf("query=%#v", query)
		}
		return []domain.ProvenanceEligibility{
			{Provenance: first, Eligibility: domain.EvidenceEligible, Bindings: []domain.EvidenceEligibilityBinding{{OwnerType: domain.EvidenceOwnerClaim, OwnerID: testID(40), EvidenceID: testID(41), ClaimStatus: domain.ClaimStatusConfirmed, SupportType: domain.ClaimSupportSupports}}},
			{Provenance: second, Eligibility: domain.EvidenceIneligible},
		}, nil
	}}
	service := mustService(t, testDependencies(repository))
	result, err := service.CheckEvidenceEligibility(context.Background(), domain.EvidenceEligibilityQuery{WorkspaceID: workspace, Provenance: []domain.ProvenanceRef{second, first}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || result[0].Provenance != first || result[1].Eligibility != domain.EvidenceIneligible {
		t.Fatalf("result=%#v", result)
	}
}

func TestEvidenceEligibilityServiceUsesReadOnlyRepositorySeam(t *testing.T) {
	workspace := testID(1)
	ref := testProvenance(20)
	repository := &fakeRepository{batchEligibility: func(_ context.Context, query domain.EvidenceEligibilityQuery) ([]domain.ProvenanceEligibility, error) {
		return []domain.ProvenanceEligibility{{Provenance: query.Provenance[0], Eligibility: domain.EvidenceIneligible}}, nil
	}}
	service, err := NewEvidenceEligibilityService(repository)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CheckEvidenceEligibility(context.Background(), domain.EvidenceEligibilityQuery{WorkspaceID: workspace, Provenance: []domain.ProvenanceRef{ref}})
	if err != nil || len(result) != 1 || result[0].Eligibility != domain.EvidenceIneligible {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err := service.CheckEvidenceEligibility(nil, domain.EvidenceEligibilityQuery{}); errorCode(err) != errorCodeRequestInvalid {
		t.Fatalf("nil context code=%s err=%v", errorCode(err), err)
	}
}

func TestCheckEvidenceEligibilityRejectsIncompleteOrInvalidRepositoryProjection(t *testing.T) {
	workspace := testID(1)
	ref := testProvenance(20)
	tests := []struct {
		name   string
		result []domain.ProvenanceEligibility
	}{
		{name: "missing"},
		{name: "wrong classification", result: []domain.ProvenanceEligibility{{Provenance: ref, Eligibility: domain.EvidenceEligible}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeRepository{batchEligibility: func(context.Context, domain.EvidenceEligibilityQuery) ([]domain.ProvenanceEligibility, error) {
				return test.result, nil
			}}
			service := mustService(t, testDependencies(repository))
			if _, err := service.CheckEvidenceEligibility(context.Background(), domain.EvidenceEligibilityQuery{WorkspaceID: workspace, Provenance: []domain.ProvenanceRef{ref}}); errorCode(err) != errorCodeResultConsistency {
				t.Fatalf("code=%s err=%v", errorCode(err), err)
			}
		})
	}
}
