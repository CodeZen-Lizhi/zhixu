package application

import (
	"errors"
	"reflect"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func validateApprovedKnowledgeRelationResult(proposal domain.Proposal, approval domain.Approval, result knowledgeapplication.ApprovedRelationApplyResult) error {
	if approval.ProposalID != proposal.ID || approval.RevisionID != proposal.Revision.ID ||
		!strings.EqualFold(approval.ChangeHash, proposal.Revision.ChangeHash) || approval.Decision != domain.DecisionApproved ||
		approval.ApprovedGitHead != nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "KNOWLEDGE_RELATION_APPROVAL_BINDING_INVALID", false, errors.New("knowledge relation approval binding is inconsistent"))
	}
	parsedApprovalID, err := foundation.ParseID(string(approval.ID))
	if err != nil || parsedApprovalID != approval.ID {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "KNOWLEDGE_RELATION_APPROVAL_BINDING_INVALID", false, errors.New("knowledge relation approval identity is invalid"))
	}
	if proposal.Approval != nil && (approval.ID != proposal.Approval.ID || approval.ProposalID != proposal.Approval.ProposalID ||
		approval.RevisionID != proposal.Approval.RevisionID || !strings.EqualFold(approval.ChangeHash, proposal.Approval.ChangeHash) ||
		approval.Decision != proposal.Approval.Decision || !equalApprovalGitHead(approval.ApprovedGitHead, proposal.Approval.ApprovedGitHead)) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "KNOWLEDGE_RELATION_APPROVAL_BINDING_INVALID", false, errors.New("knowledge relation approval identity differs from persisted approval"))
	}
	expectedVersion, replayRequired, versionErr := expectedKnowledgeRelationApplyResult(proposal)
	if versionErr != nil || result.ProposalVersion != expectedVersion || (replayRequired && !result.Replayed) {
		if versionErr == nil {
			versionErr = errors.New("knowledge relation apply result version or replay state is inconsistent")
		}
		return foundation.NewError(foundation.ErrorConsistencyViolation, "KNOWLEDGE_RELATION_APPLY_RESULT_INVALID", false, versionErr)
	}
	change := proposal.Revision.KnowledgeChange
	if result.ProposalStatus != domain.StatusApplied ||
		result.Relation.WorkspaceID != proposal.WorkspaceID || result.Relation.Status != knowledgedomain.RelationStatusConfirmed ||
		change == nil || result.Relation.Source != change.ChangeSet.Source || result.Relation.Target != change.ChangeSet.Target || result.Relation.Type != change.ChangeSet.RelationType ||
		result.Relation.Confirmation == nil || result.Relation.Confirmation.Method != knowledgedomain.ConfirmationUserApproval ||
		result.Relation.Confirmation.Reference != string(approval.ID) || len(result.Evidence) == 0 {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "KNOWLEDGE_RELATION_APPLY_RESULT_INVALID", false, errors.New("knowledge relation apply result is inconsistent"))
	}
	if err := knowledgedomain.ValidateRelationAggregate(result.Relation, result.Evidence); err != nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "KNOWLEDGE_RELATION_APPLY_RESULT_INVALID", false, err)
	}
	matchedApprovalEvidence := false
	for _, evidence := range result.Evidence {
		if evidence.WorkspaceID != proposal.WorkspaceID || evidence.RelationID != result.Relation.ID {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "KNOWLEDGE_RELATION_APPLY_RESULT_INVALID", false, errors.New("knowledge relation evidence result is inconsistent"))
		}
		if evidence.Confirmation == nil {
			continue
		}
		if evidence.Confirmation.Method == knowledgedomain.ConfirmationUserApproval && evidence.Confirmation.Reference == string(approval.ID) {
			matchedApprovalEvidence = true
		}
	}
	if !matchedApprovalEvidence {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "KNOWLEDGE_RELATION_APPLY_RESULT_INVALID", false, errors.New("knowledge relation apply result has no approval evidence"))
	}
	return nil
}

func expectedKnowledgeRelationApplyResult(proposal domain.Proposal) (int64, bool, error) {
	switch proposal.Status {
	case domain.StatusReady:
		return proposal.Version + 3, false, nil
	case domain.StatusApproved:
		return proposal.Version + 2, false, nil
	case domain.StatusApplied:
		return proposal.Version, true, nil
	default:
		return 0, false, errors.New("knowledge relation proposal status cannot produce an applied result")
	}
}

func equalApprovalGitHead(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.EqualFold(*left, *right)
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

func isNilKnowledgeRelationApprovalApplier(applier knowledgeapplication.ApprovedRelationApprovalPort) bool {
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
