package application

import (
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRelationApplyRequestHashBindsAllApprovalIdentity(t *testing.T) {
	base := ApprovedRelationApplyCommand{
		WorkspaceID: "10000000-0000-4000-8000-000000000001",
		ProposalID:  "10000000-0000-4000-8000-000000000002",
		RevisionID:  "10000000-0000-4000-8000-000000000003",
		ApprovalID:  "10000000-0000-4000-8000-000000000004",
	}
	first, err := RelationApplyRequestHash(base)
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.ApprovalID = foundation.ID("10000000-0000-4000-8000-000000000005")
	second, err := RelationApplyRequestHash(changed)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 64 || strings.ToLower(first) != first {
		t.Fatalf("hash binding = %q, %q", first, second)
	}
}

func TestValidateApprovedRelationApplyCommandRejectsMalformedIDs(t *testing.T) {
	command := ApprovedRelationApplyCommand{WorkspaceID: "not-an-id"}
	if err := ValidateApprovedRelationApplyCommand(command); err == nil {
		t.Fatal("malformed command was accepted")
	}
}

func TestRelationApplyIdempotencyKeyUsesApprovalID(t *testing.T) {
	approvalID := foundation.ID("10000000-0000-4000-8000-000000000004")
	key := RelationApplyIdempotencyKey(approvalID)
	if !strings.HasSuffix(key, string(approvalID)) || !strings.HasPrefix(key, RelationApplyIdempotencyPrefix) {
		t.Fatalf("idempotency key = %q", key)
	}
}
