package postgres

import (
	"context"
	"errors"
	"slices"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
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

// lockRevisionAuthorizations is intentionally called before the Proposal row
// lock. Atomic Begin uses the same Authorization -> Proposal lock order.
func lockRevisionAuthorizations(ctx context.Context, tx pgx.Tx, proposalID, revisionID foundation.ID) ([]revisionFenceAuthorization, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text,workspace_id::text,approval_id::text,workflow_run_id::text,status
		FROM change_control.tool_authorization
		WHERE proposal_id=$1 AND revision_id=$2
		ORDER BY id
		FOR UPDATE`, string(proposalID), string(revisionID))
	if err != nil {
		return nil, classify(err, "PROPOSAL_REVISION_AUTHORIZATION_QUERY_FAILED")
	}
	defer rows.Close()
	authorizations := make([]revisionFenceAuthorization, 0, 2)
	for rows.Next() {
		var authorization revisionFenceAuthorization
		if err := rows.Scan(&authorization.ID, &authorization.WorkspaceID, &authorization.ApprovalID, &authorization.WorkflowRunID, &authorization.Status); err != nil {
			return nil, classify(err, "PROPOSAL_REVISION_AUTHORIZATION_SCAN_FAILED")
		}
		authorizations = append(authorizations, authorization)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, "PROPOSAL_REVISION_AUTHORIZATION_ROWS_FAILED")
	}
	return authorizations, nil
}

func verifyRevisionAuthorizationSet(ctx context.Context, tx pgx.Tx, proposalID, revisionID foundation.ID, locked []revisionFenceAuthorization) error {
	rows, err := tx.Query(ctx, `
		SELECT id::text
		FROM change_control.tool_authorization
		WHERE proposal_id=$1 AND revision_id=$2
		ORDER BY id`, string(proposalID), string(revisionID))
	if err != nil {
		return classify(err, "PROPOSAL_REVISION_AUTHORIZATION_QUERY_FAILED")
	}
	defer rows.Close()
	observed := make([]string, 0, len(locked))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return classify(err, "PROPOSAL_REVISION_AUTHORIZATION_SCAN_FAILED")
		}
		observed = append(observed, id)
	}
	if err := rows.Err(); err != nil {
		return classify(err, "PROPOSAL_REVISION_AUTHORIZATION_ROWS_FAILED")
	}
	want := make([]string, len(locked))
	for index := range locked {
		want[index] = locked[index].ID
	}
	if !slices.Equal(observed, want) {
		return revisionAuthorizationConflict(errors.New("authorization set changed while the proposal was being fenced"))
	}
	return nil
}

func loadRevisionSupersedeFacts(ctx context.Context, tx pgx.Tx, command domain.AppendProposalRevision, proposalStatus, sourceHash string, authorizations []revisionFenceAuthorization) (revisionSupersedeFacts, error) {
	facts := revisionSupersedeFacts{
		ProposalStatus: proposalStatus,
		SourceHash:     sourceHash,
		Authorizations: authorizations,
	}
	var approval revisionFenceApproval
	err := tx.QueryRow(ctx, `
		SELECT id::text,change_hash,decision
		FROM change_control.approval
		WHERE proposal_id=$1 AND revision_id=$2
		FOR UPDATE`, string(command.ProposalID), string(command.SourceRevisionID)).Scan(&approval.ID, &approval.ChangeHash, &approval.Decision)
	if err == nil {
		facts.Approval = &approval
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return revisionSupersedeFacts{}, classify(err, "PROPOSAL_REVISION_APPROVAL_QUERY_FAILED")
	}

	var dispatch revisionFenceDispatch
	err = tx.QueryRow(ctx, `
		SELECT workspace_id::text,approval_id::text,workflow_run_id::text
		FROM change_control.proposal_revision_dispatch
		WHERE proposal_id=$1 AND revision_id=$2`, string(command.ProposalID), string(command.SourceRevisionID)).Scan(
		&dispatch.WorkspaceID, &dispatch.ApprovalID, &dispatch.WorkflowRunID,
	)
	if err == nil {
		facts.Dispatch = &dispatch
		if err := tx.QueryRow(ctx, `
			SELECT status
			FROM workflow.run
			WHERE id=$1 AND workspace_id=$2
			FOR UPDATE`, dispatch.WorkflowRunID, string(command.WorkspaceID)).Scan(&facts.RunStatus); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return revisionSupersedeFacts{}, revisionWorkflowActive(errors.New("revision dispatch workflow is unavailable"))
			}
			return revisionSupersedeFacts{}, classify(err, "PROPOSAL_REVISION_WORKFLOW_QUERY_FAILED")
		}
		nodes, err := tx.Query(ctx, `
			SELECT status
			FROM workflow.node_run
			WHERE run_id=$1
			ORDER BY node_key,id
			FOR UPDATE`, dispatch.WorkflowRunID)
		if err != nil {
			return revisionSupersedeFacts{}, classify(err, "PROPOSAL_REVISION_WORKFLOW_QUERY_FAILED")
		}
		for nodes.Next() {
			var status string
			if err := nodes.Scan(&status); err != nil {
				nodes.Close()
				return revisionSupersedeFacts{}, classify(err, "PROPOSAL_REVISION_WORKFLOW_SCAN_FAILED")
			}
			facts.NodeStatuses = append(facts.NodeStatuses, status)
		}
		nodes.Close()
		if err := nodes.Err(); err != nil {
			return revisionSupersedeFacts{}, classify(err, "PROPOSAL_REVISION_WORKFLOW_ROWS_FAILED")
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return revisionSupersedeFacts{}, classify(err, "PROPOSAL_REVISION_DISPATCH_QUERY_FAILED")
	}

	var execution revisionFenceExecution
	err = tx.QueryRow(ctx, `
		SELECT id::text,approval_id::text,workflow_run_id::text,
		       write_authorization_id::text,git_authorization_id::text,status,manual_recovery_required
		FROM change_control.writeback_execution
		WHERE proposal_id=$1 AND revision_id=$2
		FOR UPDATE`, string(command.ProposalID), string(command.SourceRevisionID)).Scan(
		&execution.ID, &execution.ApprovalID, &execution.WorkflowRunID,
		&execution.WriteAuthorizationID, &execution.GitAuthorizationID, &execution.Status, &execution.ManualRecoveryRequired,
	)
	if err == nil {
		facts.Execution = &execution
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return revisionSupersedeFacts{}, classify(err, "PROPOSAL_REVISION_EXECUTION_QUERY_FAILED")
	}
	var commitID string
	err = tx.QueryRow(ctx, `
		SELECT id::text
		FROM change_control.proposal_commit
		WHERE proposal_id=$1 AND revision_id=$2
		LIMIT 1`, string(command.ProposalID), string(command.SourceRevisionID)).Scan(&commitID)
	if err == nil {
		facts.HasCommit = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return revisionSupersedeFacts{}, classify(err, "PROPOSAL_REVISION_COMMIT_QUERY_FAILED")
	}
	return facts, nil
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

func revokeIssuedRevisionAuthorizations(ctx context.Context, tx pgx.Tx, command domain.AppendProposalRevision, authorizations []revisionFenceAuthorization) error {
	want := int64(0)
	for _, authorization := range authorizations {
		if authorization.Status == string(domain.AuthorizationIssued) {
			want++
		}
	}
	if want == 0 {
		return nil
	}
	result, err := tx.Exec(ctx, `
		UPDATE change_control.tool_authorization
		SET status='revoked',revoked_at=CURRENT_TIMESTAMP,version=version+1
		WHERE proposal_id=$1 AND revision_id=$2 AND status='issued'`, string(command.ProposalID), string(command.SourceRevisionID))
	if err != nil {
		return classify(err, "PROPOSAL_REVISION_AUTHORIZATION_REVOKE_FAILED")
	}
	if result.RowsAffected() != want {
		return revisionAuthorizationConflict(errors.New("issued authorization set changed before revocation"))
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
