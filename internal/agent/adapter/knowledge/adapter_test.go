package knowledge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestAdapterDelegatesEligibilityAndUsesSafeDomainMapping(t *testing.T) {
	workspace := adapterTestID(1)
	provenance := knowledgedomain.ProvenanceRef{WorkspaceID: workspace, SourceVersionID: adapterTestID(2), SourceSpanID: adapterTestID(3)}
	want := []knowledgedomain.ProvenanceEligibility{{Provenance: provenance, Eligibility: knowledgedomain.EvidenceIneligible}}
	service := &eligibilityServiceFake{result: want}
	claims := &formalClaimServiceFake{}
	adapter, err := NewAdapter(service, claims)
	if err != nil {
		t.Fatal(err)
	}
	query := knowledgedomain.EvidenceEligibilityQuery{WorkspaceID: workspace, Provenance: []knowledgedomain.ProvenanceRef{provenance}}
	got, err := adapter.CheckEvidenceEligibility(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if service.calls != 1 || service.query.WorkspaceID != workspace || len(got) != 1 || got[0].Eligibility != knowledgedomain.EvidenceIneligible {
		t.Fatalf("eligibility delegation service=%#v got=%#v", service, got)
	}
	if _, err := adapter.LoadFormalClaims(context.Background(), workspace, []foundation.ID{adapterTestID(9)}); err != nil || claims.calls != 1 {
		t.Fatalf("formal claim delegation calls=%d err=%v", claims.calls, err)
	}

	source := knowledgedomain.NodeRef{Type: knowledgedomain.NodeTypeClaim, ID: adapterTestID(10)}
	target := knowledgedomain.NodeRef{Type: knowledgedomain.NodeTypeClaim, ID: adapterTestID(11)}
	conflict, err := adapter.MapAssessment(knowledgedomain.AssessmentConflict, source, target)
	if err != nil {
		t.Fatal(err)
	}
	if conflict.Decision != knowledgedomain.AssessmentDecisionOpenConflict || !conflict.OpenConflict || conflict.RelationType != nil {
		t.Fatalf("conflict mapping can confirm a relation: %#v", conflict)
	}
	low, err := adapter.MapAssessment(knowledgedomain.AssessmentLowConfidence, source, target)
	if err != nil {
		t.Fatal(err)
	}
	if low.Decision != knowledgedomain.AssessmentDecisionRequireHumanReview || low.RelationType != nil {
		t.Fatalf("low confidence mapping can create relation: %#v", low)
	}
}

func TestAdapterRejectsMissingServiceAndInvalidConflictEndpoints(t *testing.T) {
	if _, err := NewAdapter(nil, &formalClaimServiceFake{}); adapterTestErrorCode(err) != errorCodeAdapterUnavailable {
		t.Fatalf("nil service code = %q, err=%v", adapterTestErrorCode(err), err)
	}
	if _, err := NewAdapter(&eligibilityServiceFake{}, nil); adapterTestErrorCode(err) != errorCodeAdapterUnavailable {
		t.Fatalf("nil claim service code = %q, err=%v", adapterTestErrorCode(err), err)
	}
	adapter, err := NewAdapter(&eligibilityServiceFake{}, &formalClaimServiceFake{})
	if err != nil {
		t.Fatal(err)
	}
	claim := knowledgedomain.NodeRef{Type: knowledgedomain.NodeTypeClaim, ID: adapterTestID(10)}
	if _, err := adapter.MapAssessment(knowledgedomain.AssessmentConflict, claim, claim); err == nil {
		t.Fatal("conflict mapping accepted a self-referential endpoint")
	}
}

type formalClaimServiceFake struct {
	calls  int
	result []knowledgedomain.ClaimWithSources
}

func (fake *formalClaimServiceFake) Load(_ context.Context, _ foundation.ID, _ []foundation.ID) ([]knowledgedomain.ClaimWithSources, error) {
	fake.calls++
	return fake.result, nil
}

type eligibilityServiceFake struct {
	calls  int
	query  knowledgedomain.EvidenceEligibilityQuery
	result []knowledgedomain.ProvenanceEligibility
}

func (fake *eligibilityServiceFake) CheckEvidenceEligibility(_ context.Context, query knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error) {
	fake.calls++
	fake.query = query
	return fake.result, nil
}

func adapterTestID(seed int) foundation.ID {
	return foundation.ID(fmt.Sprintf("%08d-0000-4000-8000-%012d", seed, seed))
}

func adapterTestErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return ""
	}
	return classified.Code
}
