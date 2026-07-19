package postgres

import (
	"strings"
	"testing"
)

func TestEvidenceEligibilitySQLScopesConflictsToRequestedDisputedClaims(t *testing.T) {
	t.Parallel()

	normalized := strings.Join(strings.Fields(evidenceEligibilitySQL), " ")
	requiredFragments := []string{
		"requested_disputed_claims AS ( SELECT DISTINCT bindings.owner_id AS claim_id FROM bindings WHERE bindings.owner_type='CLAIM' AND bindings.owner_status='DISPUTED' )",
		"FROM requested_disputed_claims requested_claim JOIN core.conflict_member member ON member.workspace_id=$1 AND member.claim_id=requested_claim.claim_id",
		"JOIN core.conflict conflict ON conflict.id=member.conflict_id AND conflict.workspace_id=member.workspace_id",
	}
	for _, fragment := range requiredFragments {
		if !strings.Contains(normalized, fragment) {
			t.Fatalf("evidence eligibility SQL must scope conflict aggregation through requested disputed claim bindings: missing %q", fragment)
		}
	}
	if strings.Contains(normalized, "FROM core.conflict_member member JOIN core.conflict conflict") {
		t.Fatal("evidence eligibility SQL must not start conflict aggregation from all workspace conflict members")
	}
}
