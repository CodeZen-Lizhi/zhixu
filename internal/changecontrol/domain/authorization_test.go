package domain

import (
	"testing"
	"time"
)

func TestValidateAuthorizationIssue(t *testing.T) {
	valid := AuthorizationIssue{
		WorkspaceID: "workspace", WorkflowRunID: "run", NodeRunID: "node", ProposalID: "proposal", RevisionID: "revision", ApprovalID: "approval",
		ToolName: "ApplyApprovedPatch", Capability: CapabilityWriteKnowledge, Scope: "target:a.md", IdempotencyKey: "auth-1", TTL: time.Minute,
	}
	if err := ValidateAuthorizationIssue(valid, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := ValidateToolBinding("ApplyApprovedPatch", CapabilityWriteKnowledge); err != nil {
		t.Fatal(err)
	}
	if err := ValidateToolBinding("CreateGitCommit", CapabilityGitWrite); err != nil {
		t.Fatal(err)
	}
	if err := ValidateToolBinding("CreateGitCommit", CapabilityWriteKnowledge); err == nil {
		t.Fatal("mismatched tool capability accepted")
	}
	for _, invalid := range []AuthorizationIssue{
		{WorkspaceID: "workspace", WorkflowRunID: "run", NodeRunID: "node", ProposalID: "proposal", RevisionID: "revision", ApprovalID: "approval", ToolName: "ApplyApprovedPatch", Capability: "ADMIN", Scope: "target:a.md", IdempotencyKey: "auth-1", TTL: time.Minute},
		{WorkspaceID: "workspace", WorkflowRunID: "run", NodeRunID: "node", ProposalID: "proposal", RevisionID: "revision", ApprovalID: "approval", ToolName: "ApplyApprovedPatch", Capability: CapabilityWriteKnowledge, Scope: "target:a.md", IdempotencyKey: "auth-1", TTL: 6 * time.Minute},
	} {
		if &invalid == &valid {
			continue
		}
		if err := ValidateAuthorizationIssue(invalid, 5*time.Minute); err == nil {
			t.Fatalf("invalid authorization accepted: %#v", invalid)
		}
	}
}

func TestValidateAuthorizationConsumeBindingChecksEveryImmutableField(t *testing.T) {
	authorization := ToolAuthorization{
		TokenHash: "token-hash", IdempotencyKey: "auth-1", WorkspaceID: "workspace", WorkflowRunID: "run", NodeRunID: "node",
		ProposalID: "proposal", RevisionID: "revision", ApprovalID: "approval", ToolName: "ApplyApprovedPatch",
		Capability: CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: "ABC", TargetVersion: "DEF",
	}
	request := AuthorizationConsume{
		Credential: "token-hash", IdempotencyKey: "auth-1", WorkspaceID: "workspace", WorkflowRunID: "run", NodeRunID: "node",
		ProposalID: "proposal", RevisionID: "revision", ApprovalID: "approval", ToolName: "ApplyApprovedPatch",
		Capability: CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: "abc", TargetVersion: "def",
	}
	if err := ValidateAuthorizationConsumeBinding(authorization, request); err != nil {
		t.Fatal(err)
	}
	request.ProposalID = "other-proposal"
	if err := ValidateAuthorizationConsumeBinding(authorization, request); err == nil {
		t.Fatal("proposal binding mismatch accepted")
	}
	request.ProposalID = authorization.ProposalID
	authorization.TargetMode = TargetModeCreateOnly
	if err := ValidateAuthorizationConsumeBinding(authorization, request); err == nil {
		t.Fatal("CREATE_ONLY authorization accepted a legacy REPLACE consume request")
	}
	request.TargetMode = TargetModeCreateOnly
	if err := ValidateAuthorizationConsumeBinding(authorization, request); err != nil {
		t.Fatalf("CREATE_ONLY authorization binding = %v", err)
	}
}
