package postgres

import (
	"fmt"
	"strings"
	"testing"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestValidateApprovedRelationProposalBindingAcceptsRiskBoundV2(t *testing.T) {
	proposal, command := relationApplyProposalFixture(t, "material relation risk", changecontroldomain.ProposalRiskLevelHigh)
	requestHash, err := changecontroldomain.ComputeKnowledgeChangeRequestHashWithRiskLevel(
		proposal.WorkspaceID,
		proposal.change,
		proposal.RiskLevel,
		proposal.Revision.Risk,
		proposal.Revision.RollbackPlan,
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal.RequestHash = requestHash

	version, err := validateApprovedRelationProposalBinding(proposal, command)
	if err != nil {
		t.Fatal(err)
	}
	if version != relationProposalRequestHashV2 {
		t.Fatalf("request hash version = %q", version)
	}
}

func TestValidateApprovedRelationProposalBindingAcceptsLegacyV1WithIndependentRiskDescription(t *testing.T) {
	proposal, command := relationApplyProposalFixture(t, "MEDIUM", changecontroldomain.ProposalRiskLevelHigh)
	requestHash, err := changecontroldomain.ComputeKnowledgeChangeRequestHash(
		proposal.WorkspaceID,
		proposal.change,
		proposal.Revision.Risk,
		proposal.Revision.RollbackPlan,
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal.RequestHash = requestHash

	version, err := validateApprovedRelationProposalBinding(proposal, command)
	if err != nil {
		t.Fatal(err)
	}
	if version != relationProposalRequestHashV1 {
		t.Fatalf("request hash version = %q", version)
	}
}

func TestValidateApprovedRelationProposalBindingRejectsNonHighPersistedRiskLevel(t *testing.T) {
	tests := []struct {
		name      string
		version   relationProposalRequestHashVersion
		riskLevel changecontroldomain.ProposalRiskLevel
	}{
		{name: "v1 persisted medium", version: relationProposalRequestHashV1, riskLevel: changecontroldomain.ProposalRiskLevelMedium},
		{name: "v1 persisted low", version: relationProposalRequestHashV1, riskLevel: changecontroldomain.ProposalRiskLevelLow},
		{name: "v2 persisted medium", version: relationProposalRequestHashV2, riskLevel: changecontroldomain.ProposalRiskLevelMedium},
		{name: "v2 persisted low", version: relationProposalRequestHashV2, riskLevel: changecontroldomain.ProposalRiskLevelLow},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proposal, command := relationApplyProposalFixture(t, "MEDIUM", test.riskLevel)
			var requestHash string
			var err error
			switch test.version {
			case relationProposalRequestHashV1:
				requestHash, err = changecontroldomain.ComputeKnowledgeChangeRequestHash(
					proposal.WorkspaceID,
					proposal.change,
					proposal.Revision.Risk,
					proposal.Revision.RollbackPlan,
				)
			case relationProposalRequestHashV2:
				requestHash, err = changecontroldomain.ComputeKnowledgeChangeRequestHashWithRiskLevel(
					proposal.WorkspaceID,
					proposal.change,
					proposal.RiskLevel,
					proposal.Revision.Risk,
					proposal.Revision.RollbackPlan,
				)
			default:
				t.Fatalf("unsupported request hash version %q", test.version)
			}
			if err != nil {
				t.Fatal(err)
			}
			proposal.RequestHash = requestHash

			if _, err := validateApprovedRelationProposalBinding(proposal, command); err == nil || !strings.Contains(err.Error(), "risk level") {
				t.Fatalf("non-high persisted risk level error = %v", err)
			}
		})
	}
}

func TestValidateApprovedRelationProposalBindingRejectsInvalidPersistedRiskLevel(t *testing.T) {
	tests := []struct {
		name      string
		riskLevel changecontroldomain.ProposalRiskLevel
	}{
		{name: "missing", riskLevel: ""},
		{name: "lowercase", riskLevel: "high"},
		{name: "unknown", riskLevel: "UNKNOWN"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proposal, command := relationApplyProposalFixture(t, "material relation risk", test.riskLevel)
			requestHash, err := changecontroldomain.ComputeKnowledgeChangeRequestHash(
				proposal.WorkspaceID,
				proposal.change,
				proposal.Revision.Risk,
				proposal.Revision.RollbackPlan,
			)
			if err != nil {
				t.Fatal(err)
			}
			proposal.RequestHash = requestHash

			if _, err := validateApprovedRelationProposalBinding(proposal, command); err == nil || !strings.Contains(err.Error(), "risk level") {
				t.Fatalf("invalid persisted risk level error = %v", err)
			}
		})
	}
}

func TestValidateApprovedRelationProposalBindingRejectsRequestHashRiskMismatch(t *testing.T) {
	proposal, command := relationApplyProposalFixture(t, "material relation risk", changecontroldomain.ProposalRiskLevelHigh)
	requestHash, err := changecontroldomain.ComputeKnowledgeChangeRequestHashWithRiskLevel(
		proposal.WorkspaceID,
		proposal.change,
		changecontroldomain.ProposalRiskLevelLow,
		proposal.Revision.Risk,
		proposal.Revision.RollbackPlan,
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal.RequestHash = requestHash

	if _, err := validateApprovedRelationProposalBinding(proposal, command); err == nil || !strings.Contains(err.Error(), "request hash binding") {
		t.Fatalf("v2 risk mismatch error = %v", err)
	}
}

func TestValidateApprovedRelationProposalBindingRejectsApprovalIdentityMismatch(t *testing.T) {
	proposal, command := relationApplyProposalFixture(t, "material relation risk", changecontroldomain.ProposalRiskLevelHigh)
	requestHash, err := changecontroldomain.ComputeKnowledgeChangeRequestHashWithRiskLevel(
		proposal.WorkspaceID,
		proposal.change,
		proposal.RiskLevel,
		proposal.Revision.Risk,
		proposal.Revision.RollbackPlan,
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal.RequestHash = requestHash
	proposal.Approval.RevisionID = relationApplyTestID(99)

	if _, err := validateApprovedRelationProposalBinding(proposal, command); err == nil || !strings.Contains(err.Error(), "approval binding") {
		t.Fatalf("approval identity mismatch error = %v", err)
	}
}

func TestValidateRelationApplyReplayFactAcceptsLegacyV1AndRejectsRelationMismatch(t *testing.T) {
	proposal, command := relationApplyProposalFixture(t, "material relation risk", changecontroldomain.ProposalRiskLevelHigh)
	requestHash, err := changecontroldomain.ComputeKnowledgeChangeRequestHash(
		proposal.WorkspaceID,
		proposal.change,
		proposal.Revision.Risk,
		proposal.Revision.RollbackPlan,
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal.RequestHash = requestHash
	if version, err := validateApprovedRelationProposalBinding(proposal, command); err != nil || version != relationProposalRequestHashV1 {
		t.Fatalf("legacy proposal binding version=%q err=%v", version, err)
	}
	proposal.Status = changecontroldomain.StatusApplied
	proposal.Version = 4
	receipt := &commandReceipt{
		RequestHash:      strings.Repeat("3", 64),
		CommandType:      commandConfirmRelation,
		AggregateType:    aggregateRelation,
		AggregateID:      relationApplyTestID(9),
		AggregateVersion: 2,
	}
	relation := domain.Relation{
		ID:          receipt.AggregateID,
		WorkspaceID: proposal.WorkspaceID,
		Source:      proposal.change.ChangeSet.Source,
		Target:      proposal.change.ChangeSet.Target,
		Type:        proposal.change.ChangeSet.RelationType,
		Status:      domain.RelationStatusConfirmed,
		Confirmation: &domain.Confirmation{
			Method:    domain.ConfirmationUserApproval,
			Reference: string(command.ApprovalID),
		},
		Version: receipt.AggregateVersion,
	}
	if err := validateRelationApplyReplayFact(command, proposal, receipt, relation); err != nil {
		t.Fatal(err)
	}

	wrongAggregate := relation
	wrongAggregate.ID = relationApplyTestID(10)
	if err := validateRelationApplyReplayFact(command, proposal, receipt, wrongAggregate); err == nil {
		t.Fatal("receipt replay accepted a different aggregate identity")
	}

	relation.Target = domain.NodeRef{Type: domain.NodeTypeClaim, ID: relationApplyTestID(10)}
	if err := validateRelationApplyReplayFact(command, proposal, receipt, relation); err == nil {
		t.Fatal("receipt replay accepted a relation outside the approved change binding")
	}
}

func relationApplyProposalFixture(t *testing.T, risk string, riskLevel changecontroldomain.ProposalRiskLevel) (approvedRelationProposal, knowledgeapplication.ApprovedRelationApplyCommand) {
	t.Helper()
	workspaceID := relationApplyTestID(1)
	proposalID := relationApplyTestID(2)
	revisionID := relationApplyTestID(3)
	approvalID := relationApplyTestID(4)
	change, err := changecontroldomain.ValidateKnowledgeChange(changecontroldomain.KnowledgeChange{
		TargetRefs: []changecontroldomain.KnowledgeTargetRef{{
			Type:        changecontroldomain.KnowledgeTargetRefRelationCandidate,
			ID:          relationApplyTestID(5),
			Fingerprint: strings.Repeat("1", 64),
		}},
		BaseVersions: []changecontroldomain.KnowledgeBaseVersion{
			{NodeType: domain.NodeTypeClaim, NodeID: relationApplyTestID(6), Version: 2},
			{NodeType: domain.NodeTypeClaim, NodeID: relationApplyTestID(7), Version: 3},
		},
		ChangeSet: changecontroldomain.KnowledgeChangeSet{
			Operation:    changecontroldomain.KnowledgeChangeOperationCreateRelation,
			Source:       domain.NodeRef{Type: domain.NodeTypeClaim, ID: relationApplyTestID(6)},
			Target:       domain.NodeRef{Type: domain.NodeTypeClaim, ID: relationApplyTestID(7)},
			RelationType: domain.RelationImpacts,
		},
		EvidenceRefs: []changecontroldomain.KnowledgeEvidenceRef{{
			CandidateEvidenceID: relationApplyTestID(8),
			SemanticHash:        strings.Repeat("2", 64),
		}},
		SchemaVersion: changecontroldomain.KnowledgeChangeSchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	changeHash, err := changecontroldomain.ComputeKnowledgeChangeHash(change, risk, "create a corrective relation proposal")
	if err != nil {
		t.Fatal(err)
	}
	proposal := approvedRelationProposal{
		ID:          proposalID,
		WorkspaceID: workspaceID,
		Type:        changecontroldomain.ProposalTypeKnowledgeChange,
		RiskLevel:   riskLevel,
		Status:      changecontroldomain.StatusApproved,
		Version:     2,
		Revision: changecontroldomain.Revision{
			ID:              revisionID,
			ProposalID:      proposalID,
			RevisionNo:      1,
			Risk:            risk,
			RollbackPlan:    "create a corrective relation proposal",
			ChangeHash:      changeHash,
			KnowledgeChange: &change,
		},
		Approval: changecontroldomain.Approval{
			ID:         approvalID,
			ProposalID: proposalID,
			RevisionID: revisionID,
			ChangeHash: changeHash,
			Decision:   changecontroldomain.DecisionApproved,
		},
		change: change,
	}
	command := knowledgeapplication.ApprovedRelationApplyCommand{
		WorkspaceID: workspaceID,
		ProposalID:  proposalID,
		RevisionID:  revisionID,
		ApprovalID:  approvalID,
	}
	return proposal, command
}

func relationApplyTestID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("10000000-0000-4000-8000-%012d", value))
}
