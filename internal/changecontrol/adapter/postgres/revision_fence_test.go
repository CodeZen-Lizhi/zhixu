package postgres

import (
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateRevisionSupersedeFacts(t *testing.T) {
	command := domain.AppendProposalRevision{WorkspaceID: "workspace"}
	sourceHash := "source-hash"
	approved := &revisionFenceApproval{ID: "approval", ChangeHash: sourceHash, Decision: string(domain.DecisionApproved)}
	dispatch := &revisionFenceDispatch{WorkspaceID: "workspace", ApprovalID: "approval", WorkflowRunID: "run"}
	consumed := []revisionFenceAuthorization{
		{ID: "write", WorkspaceID: "workspace", ApprovalID: "approval", WorkflowRunID: "run", Status: string(domain.AuthorizationConsumed)},
		{ID: "git", WorkspaceID: "workspace", ApprovalID: "approval", WorkflowRunID: "run", Status: string(domain.AuthorizationConsumed)},
	}
	execution := &revisionFenceExecution{
		ID: "execution", ApprovalID: "approval", WorkflowRunID: "run",
		WriteAuthorizationID: "write", GitAuthorizationID: "git", Status: string(domain.WritebackStatusNeedsRevision),
	}
	tests := []struct {
		name  string
		facts revisionSupersedeFacts
		code  string
	}{
		{name: "ready direct edit", facts: revisionSupersedeFacts{ProposalStatus: string(domain.StatusReady), SourceHash: sourceHash}},
		{name: "ready rejects historical facts", facts: revisionSupersedeFacts{ProposalStatus: string(domain.StatusReady), SourceHash: sourceHash, Approval: approved}, code: "PROPOSAL_REVISION_NOT_EDITABLE"},
		{name: "needs revision without prior workflow", facts: revisionSupersedeFacts{ProposalStatus: string(domain.StatusNeedsRevision), SourceHash: sourceHash}},
		{name: "active workflow", facts: revisionSupersedeFacts{ProposalStatus: string(domain.StatusNeedsRevision), SourceHash: sourceHash, Approval: approved, Dispatch: dispatch, RunStatus: "running", NodeStatuses: []string{"running"}}, code: "PROPOSAL_REVISION_WORKFLOW_ACTIVE"},
		{name: "cancelled workflow and issued authorization", facts: revisionSupersedeFacts{ProposalStatus: string(domain.StatusNeedsRevision), SourceHash: sourceHash, Approval: approved, Dispatch: dispatch, RunStatus: "cancelled", NodeStatuses: []string{"cancelled"}, Authorizations: []revisionFenceAuthorization{{ID: "issued", WorkspaceID: "workspace", ApprovalID: "approval", WorkflowRunID: "run", Status: string(domain.AuthorizationIssued)}}}},
		{name: "exact needs revision execution", facts: revisionSupersedeFacts{ProposalStatus: string(domain.StatusNeedsRevision), SourceHash: sourceHash, Approval: approved, Dispatch: dispatch, RunStatus: "failed", NodeStatuses: []string{"failed"}, Authorizations: consumed, Execution: execution}},
		{name: "orphaned consumed authorization", facts: revisionSupersedeFacts{ProposalStatus: string(domain.StatusNeedsRevision), SourceHash: sourceHash, Approval: approved, Dispatch: dispatch, RunStatus: "failed", NodeStatuses: []string{"failed"}, Authorizations: consumed}, code: "PROPOSAL_REVISION_AUTHORIZATION_CONFLICT"},
		{name: "execution side effect started", facts: revisionSupersedeFacts{ProposalStatus: string(domain.StatusNeedsRevision), SourceHash: sourceHash, Approval: approved, Dispatch: dispatch, RunStatus: "failed", NodeStatuses: []string{"failed"}, Authorizations: consumed, Execution: &revisionFenceExecution{ID: "execution", ApprovalID: "approval", WorkflowRunID: "run", WriteAuthorizationID: "write", GitAuthorizationID: "git", Status: string(domain.WritebackStatusFileApplied)}}, code: "PROPOSAL_REVISION_SIDE_EFFECT_STARTED"},
		{name: "proposal commit exists", facts: revisionSupersedeFacts{ProposalStatus: string(domain.StatusNeedsRevision), SourceHash: sourceHash, HasCommit: true}, code: "PROPOSAL_REVISION_SIDE_EFFECT_STARTED"},
		{name: "succeeded workflow cannot be superseded", facts: revisionSupersedeFacts{ProposalStatus: string(domain.StatusNeedsRevision), SourceHash: sourceHash, Approval: approved, Dispatch: dispatch, RunStatus: "succeeded", NodeStatuses: []string{"succeeded"}}, code: "PROPOSAL_REVISION_SIDE_EFFECT_STARTED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRevisionSupersedeFacts(command, test.facts)
			if code := revisionFenceErrorCode(err); code != test.code {
				t.Fatalf("error code=%q want=%q err=%v", code, test.code, err)
			}
		})
	}
}

func revisionFenceErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return "unclassified"
}
