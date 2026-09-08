package postgres

import (
	"errors"
	"reflect"
	"strings"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/graph/candidateconfirm"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func buildKnowledgeChange(candidate graphdomain.SemanticLinkCandidate, relationType knowledge.RelationType) (changecontroldomain.KnowledgeChange, error) {
	change := changecontroldomain.KnowledgeChange{
		TargetRefs: []changecontroldomain.KnowledgeTargetRef{{
			Type: changecontroldomain.KnowledgeTargetRefRelationCandidate, ID: candidate.ID, Fingerprint: candidate.Fingerprint,
		}},
		BaseVersions: []changecontroldomain.KnowledgeBaseVersion{
			{NodeType: candidate.Source.Ref.Type, NodeID: candidate.Source.Ref.ID, Version: candidate.Source.Version},
			{NodeType: candidate.Target.Ref.Type, NodeID: candidate.Target.Ref.ID, Version: candidate.Target.Version},
		},
		ChangeSet: changecontroldomain.KnowledgeChangeSet{
			Operation: changecontroldomain.KnowledgeChangeOperationCreateRelation,
			Source:    candidate.Source.Ref, Target: candidate.Target.Ref, RelationType: relationType,
		},
		SchemaVersion: changecontroldomain.KnowledgeChangeSchemaVersion,
	}
	for _, evidence := range candidate.Evidence {
		change.EvidenceRefs = append(change.EvidenceRefs, changecontroldomain.KnowledgeEvidenceRef{CandidateEvidenceID: evidence.ID, SemanticHash: evidence.SemanticHash})
	}
	canonical, err := changecontroldomain.ValidateKnowledgeChange(change)
	if err != nil {
		return changecontroldomain.KnowledgeChange{}, candidateConfirmInvalid(err)
	}
	return canonical, nil
}

func validateCandidateConfirmProposalBinding(candidate graphdomain.SemanticLinkCandidate, command candidateconfirm.Command, requestHash string, proposal changecontroldomain.Proposal, legacy bool) error {
	if proposal.WorkspaceID != candidate.WorkspaceID || proposal.WorkspaceID != command.WorkspaceID || proposal.Type != changecontroldomain.ProposalTypeKnowledgeChange || proposal.Revision.KnowledgeChange == nil {
		return candidateConfirmConsistency(errors.New("relation proposal does not bind candidate workspace or type"))
	}
	if proposal.RiskLevel != candidateconfirm.ProposalRiskLevel {
		return candidateConfirmConsistency(errors.New("relation proposal risk level must be HIGH"))
	}
	if strings.TrimSpace(string(command.RiskLevel)) != "" && command.RiskLevel != proposal.RiskLevel {
		return candidateConfirmConsistency(errors.New("relation proposal command risk level binding differs"))
	}
	if candidate.ProposalID != nil && *candidate.ProposalID != proposal.ID {
		return candidateConfirmConsistency(errors.New("candidate projection and relation proposal differ"))
	}
	effectiveRelationType := candidate.SuggestedRelationType
	if command.RelationType != nil {
		effectiveRelationType = *command.RelationType
	}
	expectedChange, err := buildKnowledgeChange(candidate, effectiveRelationType)
	if err != nil {
		return err
	}
	expectedHash, err := changecontroldomain.ComputeKnowledgeChangeHash(expectedChange, command.Risk, command.RollbackPlan)
	if err != nil {
		return candidateConfirmInvalid(err)
	}
	proposalKeyPrefix := "semantic-link-confirm/v2:"
	expectedRequestHash, err := changecontroldomain.ComputeKnowledgeChangeRequestHashWithRiskLevel(candidate.WorkspaceID, expectedChange, command.RiskLevel, command.Risk, command.RollbackPlan)
	if legacy {
		proposalKeyPrefix = "semantic-link-confirm/v1:"
		expectedRequestHash, err = changecontroldomain.ComputeKnowledgeChangeRequestHash(candidate.WorkspaceID, expectedChange, command.Risk, command.RollbackPlan)
	}
	if err != nil {
		return candidateConfirmInvalid(err)
	}
	if proposal.IdempotencyKey != proposalKeyPrefix+requestHash || proposal.RequestHash != expectedRequestHash || proposal.Revision.ChangeHash != expectedHash || proposal.Revision.Risk != command.Risk || proposal.Revision.RollbackPlan != command.RollbackPlan {
		return candidateConfirmConsistency(errors.New("relation proposal command binding differs"))
	}
	return nil
}

func sameOptionalRelationType(left, right *knowledge.RelationType) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func isNilCandidateConfirmDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func candidateConfirmInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "RELATION_PROPOSAL_CONFIRM_INVALID", false, cause)
}

func candidateConfirmVersionConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "RELATION_PROPOSAL_CONFIRM_CONFLICT", false, errors.New(message))
}

func candidateConfirmConsistency(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "RELATION_PROPOSAL_CONFIRM_CONSISTENCY", false, cause)
}

func candidateConfirmUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "RELATION_PROPOSAL_CONFIRM_UNAVAILABLE", true, cause)
}
