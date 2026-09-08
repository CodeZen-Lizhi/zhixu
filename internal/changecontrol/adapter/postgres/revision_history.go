package postgres

import (
	"errors"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"time"
)

func bindRevisionHistoryDecision(approval **domain.Approval, workflow **foundation.ID, proposalID, revisionID foundation.ID, approvalID, approvalHash, approvalDecision, approvedGitHead *string, approvalDecidedAt *time.Time, workflowRunID *string) error {
	present := approvalID != nil || approvalHash != nil || approvalDecision != nil || approvedGitHead != nil || approvalDecidedAt != nil
	complete := approvalID != nil && approvalHash != nil && approvalDecision != nil && approvalDecidedAt != nil
	if present && !complete || workflowRunID != nil && !complete {
		return revisionHistoryBindingError()
	}
	if complete {
		if !domain.ValidHash(*approvalHash) || domain.Decision(*approvalDecision) != domain.DecisionApproved && domain.Decision(*approvalDecision) != domain.DecisionRejected {
			return revisionHistoryBindingError()
		}
		*approval = &domain.Approval{
			ID: foundation.ID(*approvalID), ProposalID: proposalID, RevisionID: revisionID, ChangeHash: *approvalHash,
			Decision: domain.Decision(*approvalDecision), ApprovedGitHead: approvedGitHead, DecidedAt: *approvalDecidedAt,
		}
	}
	if workflowRunID != nil {
		if *approval == nil || (*approval).Decision != domain.DecisionApproved {
			return revisionHistoryBindingError()
		}
		value := foundation.ID(*workflowRunID)
		*workflow = &value
	}
	return nil
}

func revisionHistoryBindingError() error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_HISTORY_BINDING_INVALID", false, errors.New("proposal revision history binding is incomplete"))
}
