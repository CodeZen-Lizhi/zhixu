package postgres

import (
	"errors"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type revisionFenceAuthorization struct {
	ID, WorkspaceID, ApprovalID, WorkflowRunID string
	Status                                     string
}

type revisionFenceApproval struct {
	ID, ChangeHash, Decision string
}

type revisionFenceDispatch struct {
	WorkspaceID, ApprovalID, WorkflowRunID string
}

type revisionFenceExecution struct {
	ID, ApprovalID, WorkflowRunID            string
	WriteAuthorizationID, GitAuthorizationID string
	Status                                   string
	ManualRecoveryRequired                   bool
}

type revisionSupersedeFacts struct {
	ProposalStatus string
	SourceHash     string
	Approval       *revisionFenceApproval
	Dispatch       *revisionFenceDispatch
	RunStatus      string
	NodeStatuses   []string
	Authorizations []revisionFenceAuthorization
	Execution      *revisionFenceExecution
	HasCommit      bool
}

func validateRevisionSupersedeFacts(command domain.AppendProposalRevision, facts revisionSupersedeFacts) error {
	if facts.ProposalStatus == string(domain.StatusReady) {
		if facts.Approval != nil || facts.Dispatch != nil || facts.Execution != nil || facts.HasCommit || len(facts.Authorizations) != 0 {
			return revisionNotEditable(errors.New("ready revision carries approval or execution facts"))
		}
		return nil
	}
	if facts.ProposalStatus != string(domain.StatusNeedsRevision) {
		return revisionNotEditable(domain.ErrProposalRevisionNotEditable)
	}
	if facts.HasCommit {
		return revisionSideEffectStarted(errors.New("source revision already has a proposal commit"))
	}
	if facts.Approval != nil && (facts.Approval.Decision != string(domain.DecisionApproved) || facts.Approval.ChangeHash != facts.SourceHash) {
		return revisionNotEditable(errors.New("source revision approval is not an exact approved decision"))
	}
	if facts.Dispatch != nil {
		if facts.Approval == nil || facts.Dispatch.WorkspaceID != string(command.WorkspaceID) || facts.Dispatch.ApprovalID != facts.Approval.ID {
			return revisionSideEffectStarted(errors.New("source revision dispatch binding is inconsistent"))
		}
		switch facts.RunStatus {
		case "failed", "cancelled":
		case "succeeded":
			return revisionSideEffectStarted(errors.New("source revision workflow already succeeded"))
		default:
			return revisionWorkflowActive(errors.New("source revision workflow is not terminal"))
		}
		if len(facts.NodeStatuses) == 0 {
			return revisionWorkflowActive(errors.New("source revision workflow has no terminal node"))
		}
		for _, status := range facts.NodeStatuses {
			if status != "succeeded" && status != "failed" && status != "cancelled" {
				return revisionWorkflowActive(errors.New("source revision workflow node is not terminal"))
			}
		}
	} else if facts.RunStatus != "" || len(facts.NodeStatuses) != 0 {
		return revisionSideEffectStarted(errors.New("workflow facts exist without a revision dispatch"))
	}

	authorizations := make(map[string]revisionFenceAuthorization, len(facts.Authorizations))
	for _, authorization := range facts.Authorizations {
		if facts.Dispatch == nil || authorization.WorkspaceID != string(command.WorkspaceID) || authorization.ApprovalID != facts.Dispatch.ApprovalID || authorization.WorkflowRunID != facts.Dispatch.WorkflowRunID {
			return revisionAuthorizationConflict(errors.New("source authorization is not bound to its revision dispatch"))
		}
		switch authorization.Status {
		case string(domain.AuthorizationIssued), string(domain.AuthorizationConsumed), string(domain.AuthorizationRevoked), string(domain.AuthorizationExpired):
		default:
			return revisionAuthorizationConflict(errors.New("source authorization has an unknown status"))
		}
		authorizations[authorization.ID] = authorization
	}
	if facts.Execution == nil {
		for _, authorization := range facts.Authorizations {
			if authorization.Status == string(domain.AuthorizationConsumed) {
				return revisionAuthorizationConflict(errors.New("consumed authorization has no exact writeback execution"))
			}
		}
		return nil
	}
	if facts.Dispatch == nil || facts.Execution.Status != string(domain.WritebackStatusNeedsRevision) || facts.Execution.ManualRecoveryRequired ||
		facts.Execution.ApprovalID != facts.Dispatch.ApprovalID || facts.Execution.WorkflowRunID != facts.Dispatch.WorkflowRunID {
		return revisionSideEffectStarted(errors.New("source writeback execution is not safely supersedable"))
	}
	if facts.Execution.WriteAuthorizationID == facts.Execution.GitAuthorizationID {
		return revisionAuthorizationConflict(errors.New("writeback execution authorization binding is invalid"))
	}
	for _, id := range []string{facts.Execution.WriteAuthorizationID, facts.Execution.GitAuthorizationID} {
		authorization, ok := authorizations[id]
		if !ok || authorization.Status != string(domain.AuthorizationConsumed) {
			return revisionAuthorizationConflict(errors.New("writeback execution lacks its consumed authorization"))
		}
	}
	for _, authorization := range facts.Authorizations {
		if authorization.Status == string(domain.AuthorizationConsumed) && authorization.ID != facts.Execution.WriteAuthorizationID && authorization.ID != facts.Execution.GitAuthorizationID {
			return revisionAuthorizationConflict(errors.New("consumed authorization is not owned by the exact writeback execution"))
		}
	}
	return nil
}

func revisionNotEditable(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_NOT_EDITABLE", false, cause)
}

func revisionWorkflowActive(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_WORKFLOW_ACTIVE", false, cause)
}

func revisionSideEffectStarted(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_SIDE_EFFECT_STARTED", false, cause)
}

func revisionAuthorizationConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_AUTHORIZATION_CONFLICT", false, cause)
}
