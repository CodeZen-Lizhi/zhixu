package postgres

import (
	"context"

	changedomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ReadHistoricalRepublishGitAuthority 仅读取不可变的操作绑定。
// 准备前单独检查当前发布的 CAS；最终化后的重放
// 仍必须恢复同一回执和提交。
func (r *GORMRepository) ReadHistoricalRepublishGitAuthority(ctx context.Context, e changedomain.WritebackExecution) (*changedomain.HistoricalRepublishGitAuthority, error) {
	if changedomain.NormalizeTargetMode(e.TargetMode) != changedomain.TargetModeReplace {
		return nil, nil
	}
	var receipt string
	result := r.database.WithContext(ctx).Raw(`SELECT reserved.historical_republish_id::text
 FROM authoring.document_publication_binding binding
 JOIN authoring.document_publication_reservation reserved ON reserved.id=binding.reservation_id AND reserved.workspace_id=binding.workspace_id
 JOIN organizing.synthesis_publication_merge_baseline proof ON proof.workspace_id=reserved.workspace_id AND proof.article_revision_id=reserved.article_revision_id AND proof.document_id=reserved.document_id AND proof.historical_republish_id=reserved.historical_republish_id AND proof.valid
 JOIN organizing.synthesis_historical_republish_event applied ON applied.id=reserved.historical_republish_id AND applied.workspace_id=reserved.workspace_id AND applied.kind='APPLY'
 JOIN change_control.writeback_execution execution ON execution.workspace_id=binding.workspace_id AND execution.proposal_id=binding.proposal_id AND execution.revision_id=binding.proposal_revision_id
 JOIN change_control.proposal_revision revision ON revision.id=execution.revision_id AND revision.proposal_id=execution.proposal_id
 WHERE execution.id=? AND execution.workspace_id=? AND execution.proposal_id=? AND execution.revision_id=? AND execution.approval_id=?
 AND execution.workflow_run_id=? AND execution.node_run_id=? AND execution.approved_git_head=? AND execution.approved_change_hash=?
 AND execution.target_path=? AND execution.target_mode='REPLACE' AND execution.base_hash=?
 AND reserved.target_path=execution.target_path AND reserved.base_version=execution.base_hash AND reserved.content_hash=execution.base_hash
 AND binding.content_hash=reserved.content_hash AND proof.file_base=reserved.base_version AND proof.content_hash=reserved.content_hash
 AND encode(sha256(convert_to(revision.content,'UTF8')),'hex')=reserved.content_hash`, string(e.ID), string(e.WorkspaceID), string(e.ProposalID), string(e.RevisionID), string(e.ApprovalID), string(e.WorkflowRunID), string(e.NodeRunID), e.ApprovedGitHead, e.ApprovedChangeHash, e.TargetPath, e.BaseHash).Scan(&receipt)
	if result.Error != nil {
		return nil, classifyGORM(ctx, result.Error, "AUTHORING_HISTORICAL_GIT_PROOF_FAILED")
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	a := changedomain.HistoricalRepublishGitAuthority{ReceiptID: foundation.ID(receipt), WorkspaceID: e.WorkspaceID, ExecutionID: e.ID, ProposalID: e.ProposalID, RevisionID: e.RevisionID, ApprovalID: e.ApprovalID, WorkflowRunID: e.WorkflowRunID, NodeRunID: e.NodeRunID, TargetPath: e.TargetPath, ApprovedGitHead: e.ApprovedGitHead, ContentHash: e.BaseHash}
	return &a, nil
}
