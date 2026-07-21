package application

import (
	"context"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// applyApprovedKnowledgeRelation 从持久 Proposal/Approval 绑定构造正式 Relation apply 命令。
// Knowledge adapter 会在同一事务内重新读取不可变 Revision、Candidate、端点和 Evidence。
func (s *Service) applyApprovedKnowledgeRelation(ctx context.Context, approval domain.Approval) (knowledgeapplication.ApprovedRelationApplyResult, error) {
	if s == nil || isNilKnowledgeRelationApplier(s.knowledgeApplier) {
		return knowledgeapplication.ApprovedRelationApplyResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "KNOWLEDGE_RELATION_APPLIER_UNAVAILABLE", false, errors.New("knowledge relation apply seam is not configured"))
	}
	proposal, err := s.repo.GetProposal(ctx, approval.ProposalID)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if proposalType(proposal) != domain.ProposalTypeKnowledgeChange || proposal.Approval == nil ||
		proposal.Approval.ID != approval.ID || proposal.Approval.ProposalID != approval.ProposalID ||
		proposal.Approval.RevisionID != approval.RevisionID || proposal.Approval.ChangeHash != approval.ChangeHash ||
		proposal.Approval.Decision != domain.DecisionApproved {
		return knowledgeapplication.ApprovedRelationApplyResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "KNOWLEDGE_RELATION_APPROVAL_BINDING_INVALID", false, errors.New("knowledge relation approval binding is inconsistent"))
	}
	if proposal.Status == domain.StatusNeedsRevision {
		return knowledgeapplication.ApprovedRelationApplyResult{}, foundation.NewError(foundation.ErrorVersionConflict, "RELATION_PROPOSAL_BASE_STALE", false, errors.New("knowledge relation proposal requires revision"))
	}
	if proposal.Status != domain.StatusApproved && proposal.Status != domain.StatusApplied {
		return knowledgeapplication.ApprovedRelationApplyResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "KNOWLEDGE_RELATION_PROPOSAL_STATUS_INVALID", false, errors.New("knowledge relation proposal is not applicable"))
	}
	result, err := s.knowledgeApplier.ApplyApprovedRelation(ctx, knowledgeapplication.ApprovedRelationApplyCommand{
		WorkspaceID: proposal.WorkspaceID,
		ProposalID:  proposal.ID,
		RevisionID:  proposal.Revision.ID,
		ApprovalID:  approval.ID,
	})
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if result.ProposalStatus != domain.StatusApplied || result.ProposalVersion < proposal.Version ||
		result.Relation.WorkspaceID != proposal.WorkspaceID || result.Relation.Status != knowledgedomain.RelationStatusConfirmed ||
		result.Relation.Confirmation == nil || result.Relation.Confirmation.Method != knowledgedomain.ConfirmationUserApproval ||
		result.Relation.Confirmation.Reference != string(approval.ID) || len(result.Evidence) == 0 {
		return knowledgeapplication.ApprovedRelationApplyResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "KNOWLEDGE_RELATION_APPLY_RESULT_INVALID", false, errors.New("knowledge relation apply result is inconsistent"))
	}
	return result, nil
}

func isNilKnowledgeRelationApplier(applier knowledgeapplication.ApprovedRelationApplyPort) bool {
	if applier == nil {
		return true
	}
	value := reflect.ValueOf(applier)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
