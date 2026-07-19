package domain

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCanonicalEvidenceEligibilityQuerySortsWithoutMutatingInput(t *testing.T) {
	workspace := uuid(1)
	second := ProvenanceRef{WorkspaceID: workspace, SourceVersionID: uuid(3), SourceSpanID: uuid(4)}
	first := ProvenanceRef{WorkspaceID: workspace, SourceVersionID: uuid(2), SourceSpanID: uuid(5)}
	input := EvidenceEligibilityQuery{WorkspaceID: workspace, Provenance: []ProvenanceRef{second, first}}
	canonical, err := CanonicalEvidenceEligibilityQuery(input)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Provenance[0] != first || canonical.Provenance[1] != second || input.Provenance[0] != second {
		t.Fatalf("canonical=%#v input=%#v", canonical.Provenance, input.Provenance)
	}
}

func TestEvidenceEligibilityValidationFailsClosed(t *testing.T) {
	workspace := uuid(1)
	ref := ProvenanceRef{WorkspaceID: workspace, SourceVersionID: uuid(2), SourceSpanID: uuid(3)}
	claim := EvidenceEligibilityBinding{OwnerType: EvidenceOwnerClaim, OwnerID: uuid(4), EvidenceID: uuid(5), ClaimStatus: ClaimStatusConfirmed, SupportType: ClaimSupportSupports}
	relation := EvidenceEligibilityBinding{OwnerType: EvidenceOwnerRelation, OwnerID: uuid(6), EvidenceID: uuid(7), RelationStatus: RelationStatusConfirmed, RelationType: RelationSupports}
	disputedApplicability, err := ParseApplicability(json.RawMessage(`{"environment":"prod"}`))
	if err != nil {
		t.Fatal(err)
	}
	disputed := EvidenceEligibilityBinding{
		OwnerType: EvidenceOwnerClaim, OwnerID: uuid(4), EvidenceID: uuid(5), ClaimStatus: ClaimStatusDisputed,
		SupportType: ClaimSupportRefutes, ConflictIDs: []foundation.ID{uuid(8)}, DisputedApplicability: disputedApplicability,
		DisputedClaimUpdatedAtUTC: time.Unix(1, 0).UTC(),
	}
	valid := []ProvenanceEligibility{
		{Provenance: ref, Eligibility: EvidenceIneligible},
		{Provenance: ref, Eligibility: EvidenceEligible, Bindings: []EvidenceEligibilityBinding{claim, relation}},
		{Provenance: ref, Eligibility: EvidenceEligibleWithConflict, Bindings: []EvidenceEligibilityBinding{disputed}},
	}
	for _, result := range valid {
		if err := ValidateProvenanceEligibility(result); err != nil {
			t.Fatalf("valid result rejected: %#v err=%v", result, err)
		}
	}
	invalid := []ProvenanceEligibility{
		{Provenance: ref, Eligibility: EvidenceEligible},
		{Provenance: ref, Eligibility: EvidenceEligible, Bindings: []EvidenceEligibilityBinding{{OwnerType: EvidenceOwnerClaim, OwnerID: uuid(4), EvidenceID: uuid(5), ClaimStatus: ClaimStatusDisputed, SupportType: ClaimSupportSupports}}},
		{Provenance: ref, Eligibility: EvidenceEligibleWithConflict, Bindings: []EvidenceEligibilityBinding{func() EvidenceEligibilityBinding {
			value := disputed
			value.DisputedClaimUpdatedAtUTC = time.Time{}
			return value
		}()}},
		{Provenance: ref, Eligibility: EvidenceEligible, Bindings: []EvidenceEligibilityBinding{relation, claim}},
	}
	for _, result := range invalid {
		err := ValidateProvenanceEligibility(result)
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Code != ErrorCodeEvidenceEligibilityInvalid {
			t.Fatalf("invalid result error=%v", err)
		}
	}
}

func TestEvidenceEligibilityQueryRejectsDuplicatesAndCrossWorkspace(t *testing.T) {
	workspace := uuid(1)
	ref := ProvenanceRef{WorkspaceID: workspace, SourceVersionID: uuid(2), SourceSpanID: uuid(3)}
	queries := []EvidenceEligibilityQuery{
		{WorkspaceID: workspace},
		{WorkspaceID: workspace, Provenance: []ProvenanceRef{ref, ref}},
		{WorkspaceID: workspace, Provenance: []ProvenanceRef{{WorkspaceID: uuid(9), SourceVersionID: uuid(2), SourceSpanID: uuid(3)}}},
	}
	for _, query := range queries {
		if err := ValidateEvidenceEligibilityQuery(query); err == nil {
			t.Fatalf("query accepted: %#v", query)
		}
	}
}
