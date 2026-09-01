package postgres

import (
	"context"
	"errors"
	"slices"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

// gormLockRevisionAuthorizations must run before locking the Proposal. Atomic
// Begin uses this same Authorization -> Proposal ordering.
func gormLockRevisionAuthorizations(ctx context.Context, database *gorm.DB, proposalID, revisionID foundation.ID) ([]revisionFenceAuthorization, error) {
	rows, err := gormChangeRawRows(ctx, database, `
		SELECT id::text,workspace_id::text,approval_id::text,workflow_run_id::text,status
		FROM change_control.tool_authorization
		WHERE proposal_id=? AND revision_id=?
		ORDER BY id
		FOR UPDATE`, string(proposalID), string(revisionID))
	if err != nil {
		return nil, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_AUTHORIZATION_QUERY_FAILED")
	}
	defer rows.Close()

	authorizations := make([]revisionFenceAuthorization, 0, 2)
	for rows.Next() {
		var authorization revisionFenceAuthorization
		if err := rows.Scan(&authorization.ID, &authorization.WorkspaceID, &authorization.ApprovalID, &authorization.WorkflowRunID, &authorization.Status); err != nil {
			return nil, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_AUTHORIZATION_SCAN_FAILED")
		}
		authorizations = append(authorizations, authorization)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_AUTHORIZATION_ROWS_FAILED")
	}
	return authorizations, nil
}

func gormVerifyRevisionAuthorizationSet(ctx context.Context, database *gorm.DB, proposalID, revisionID foundation.ID, locked []revisionFenceAuthorization) error {
	rows, err := gormChangeRawRows(ctx, database, `
		SELECT id::text
		FROM change_control.tool_authorization
		WHERE proposal_id=? AND revision_id=?
		ORDER BY id`, string(proposalID), string(revisionID))
	if err != nil {
		return classifyGORMChange(ctx, err, "PROPOSAL_REVISION_AUTHORIZATION_QUERY_FAILED")
	}
	defer rows.Close()

	observed := make([]string, 0, len(locked))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return classifyGORMChange(ctx, err, "PROPOSAL_REVISION_AUTHORIZATION_SCAN_FAILED")
		}
		observed = append(observed, id)
	}
	if err := rows.Err(); err != nil {
		return classifyGORMChange(ctx, err, "PROPOSAL_REVISION_AUTHORIZATION_ROWS_FAILED")
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

func gormLoadRevisionSupersedeFacts(ctx context.Context, database *gorm.DB, command domain.AppendProposalRevision, proposalStatus, sourceHash string, authorizations []revisionFenceAuthorization) (revisionSupersedeFacts, error) {
	facts := revisionSupersedeFacts{
		ProposalStatus: proposalStatus,
		SourceHash:     sourceHash,
		Authorizations: authorizations,
	}

	var approval revisionFenceApproval
	row, err := gormChangeRawRow(ctx, database, `
		SELECT id::text,change_hash,decision
		FROM change_control.approval
		WHERE proposal_id=? AND revision_id=?
		FOR UPDATE`, string(command.ProposalID), string(command.SourceRevisionID))
	if err != nil {
		return revisionSupersedeFacts{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_APPROVAL_QUERY_FAILED")
	}
	if err := row.Scan(&approval.ID, &approval.ChangeHash, &approval.Decision); err == nil {
		facts.Approval = &approval
	} else if !gormChangeNoRows(err) {
		return revisionSupersedeFacts{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_APPROVAL_QUERY_FAILED")
	}

	var dispatch revisionFenceDispatch
	row, err = gormChangeRawRow(ctx, database, `
		SELECT workspace_id::text,approval_id::text,workflow_run_id::text
		FROM change_control.proposal_revision_dispatch
		WHERE proposal_id=? AND revision_id=?`, string(command.ProposalID), string(command.SourceRevisionID))
	if err != nil {
		return revisionSupersedeFacts{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_DISPATCH_QUERY_FAILED")
	}
	if err := row.Scan(&dispatch.WorkspaceID, &dispatch.ApprovalID, &dispatch.WorkflowRunID); err == nil {
		facts.Dispatch = &dispatch
		row, err = gormChangeRawRow(ctx, database, `
			SELECT status
			FROM workflow.run
			WHERE id=? AND workspace_id=?
			FOR UPDATE`, dispatch.WorkflowRunID, string(command.WorkspaceID))
		if err != nil {
			return revisionSupersedeFacts{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_WORKFLOW_QUERY_FAILED")
		}
		if err := row.Scan(&facts.RunStatus); gormChangeNoRows(err) {
			return revisionSupersedeFacts{}, revisionWorkflowActive(errors.New("revision dispatch workflow is unavailable"))
		} else if err != nil {
			return revisionSupersedeFacts{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_WORKFLOW_QUERY_FAILED")
		}

		nodes, queryErr := gormChangeRawRows(ctx, database, `
			SELECT status
			FROM workflow.node_run
			WHERE run_id=?
			ORDER BY node_key,id
			FOR UPDATE`, dispatch.WorkflowRunID)
		if queryErr != nil {
			return revisionSupersedeFacts{}, classifyGORMChange(ctx, queryErr, "PROPOSAL_REVISION_WORKFLOW_QUERY_FAILED")
		}
		for nodes.Next() {
			var status string
			if err := nodes.Scan(&status); err != nil {
				nodes.Close()
				return revisionSupersedeFacts{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_WORKFLOW_SCAN_FAILED")
			}
			facts.NodeStatuses = append(facts.NodeStatuses, status)
		}
		nodes.Close()
		if err := nodes.Err(); err != nil {
			return revisionSupersedeFacts{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_WORKFLOW_ROWS_FAILED")
		}
	} else if !gormChangeNoRows(err) {
		return revisionSupersedeFacts{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_DISPATCH_QUERY_FAILED")
	}

	var execution revisionFenceExecution
	row, err = gormChangeRawRow(ctx, database, `
		SELECT id::text,approval_id::text,workflow_run_id::text,
		       write_authorization_id::text,git_authorization_id::text,status,manual_recovery_required
		FROM change_control.writeback_execution
		WHERE proposal_id=? AND revision_id=?
		FOR UPDATE`, string(command.ProposalID), string(command.SourceRevisionID))
	if err != nil {
		return revisionSupersedeFacts{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_EXECUTION_QUERY_FAILED")
	}
	if err := row.Scan(&execution.ID, &execution.ApprovalID, &execution.WorkflowRunID,
		&execution.WriteAuthorizationID, &execution.GitAuthorizationID, &execution.Status, &execution.ManualRecoveryRequired); err == nil {
		facts.Execution = &execution
	} else if !gormChangeNoRows(err) {
		return revisionSupersedeFacts{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_EXECUTION_QUERY_FAILED")
	}

	var commitID string
	row, err = gormChangeRawRow(ctx, database, `
		SELECT id::text
		FROM change_control.proposal_commit
		WHERE proposal_id=? AND revision_id=?
		LIMIT 1`, string(command.ProposalID), string(command.SourceRevisionID))
	if err != nil {
		return revisionSupersedeFacts{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_COMMIT_QUERY_FAILED")
	}
	if err := row.Scan(&commitID); err == nil {
		facts.HasCommit = true
	} else if !gormChangeNoRows(err) {
		return revisionSupersedeFacts{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_COMMIT_QUERY_FAILED")
	}
	return facts, nil
}

func gormRevokeIssuedRevisionAuthorizations(ctx context.Context, database *gorm.DB, command domain.AppendProposalRevision, authorizations []revisionFenceAuthorization) error {
	want := int64(0)
	for _, authorization := range authorizations {
		if authorization.Status == string(domain.AuthorizationIssued) {
			want++
		}
	}
	if want == 0 {
		return nil
	}
	changed, err := gormChangeExec(ctx, database, `
		UPDATE change_control.tool_authorization
		SET status='revoked',revoked_at=CURRENT_TIMESTAMP,version=version+1
		WHERE proposal_id=? AND revision_id=? AND status='issued'`, string(command.ProposalID), string(command.SourceRevisionID))
	if err != nil {
		return classifyGORMChange(ctx, err, "PROPOSAL_REVISION_AUTHORIZATION_REVOKE_FAILED")
	}
	if changed != want {
		return revisionAuthorizationConflict(errors.New("issued authorization set changed before revocation"))
	}
	return nil
}
