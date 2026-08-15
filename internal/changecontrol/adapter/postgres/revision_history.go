package postgres

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

var _ domain.ProposalRevisionHistoryRepository = (*Repository)(nil)

// ListProposalRevisions returns newest-first immutable summaries. The list does
// not duplicate revision or snapshot bodies; callers load one exact detail.
func (r *Repository) ListProposalRevisions(ctx context.Context, query domain.ProposalRevisionHistoryQuery) ([]domain.ProposalRevisionHistoryItem, bool, error) {
	if err := domain.ValidateProposalRevisionHistoryQuery(query); err != nil {
		return nil, false, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_HISTORY_QUERY_INVALID", false, err)
	}
	arguments := []any{string(query.ProposalID)}
	statement := `
		SELECT r.id::text,r.revision_no,COALESCE(r.target_path,''),COALESCE(r.target_mode,'REPLACE'),
		       COALESCE(r.base_hash,''),r.change_hash,(s.revision_id IS NOT NULL),
		       COALESCE(r.id=p.current_revision_id,false),r.created_at,
		       a.id::text,a.change_hash,a.decision,a.approved_git_head,a.decided_at,d.workflow_run_id::text
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id
		LEFT JOIN change_control.proposal_revision_base_snapshot s
		  ON s.proposal_id=p.id AND s.revision_id=r.id
		LEFT JOIN change_control.approval a ON a.proposal_id=p.id AND a.revision_id=r.id
		LEFT JOIN change_control.proposal_revision_dispatch d
		  ON d.proposal_id=p.id AND d.revision_id=r.id AND d.approval_id=a.id
		WHERE p.id=$1 AND p.proposal_type='file_patch'`
	if query.BeforeRevisionNo != nil {
		arguments = append(arguments, *query.BeforeRevisionNo)
		statement += ` AND r.revision_no<$2`
	}
	arguments = append(arguments, query.Limit+1)
	statement += ` ORDER BY r.revision_no DESC,r.id DESC LIMIT $` + strconv.Itoa(len(arguments))
	rows, err := r.db.Query(ctx, statement, arguments...)
	if err != nil {
		return nil, false, classify(err, "PROPOSAL_REVISION_HISTORY_QUERY_FAILED")
	}
	defer rows.Close()
	items := make([]domain.ProposalRevisionHistoryItem, 0, query.Limit+1)
	for rows.Next() {
		var item domain.ProposalRevisionHistoryItem
		var revisionID, targetMode string
		var approvalID, approvalHash, approvalDecision, approvedGitHead, workflowRunID *string
		var approvalDecidedAt *time.Time
		if err := rows.Scan(
			&revisionID, &item.RevisionNo, &item.TargetPath, &targetMode, &item.BaseHash, &item.ChangeHash,
			&item.BaseAvailable, &item.Current, &item.CreatedAt,
			&approvalID, &approvalHash, &approvalDecision, &approvedGitHead, &approvalDecidedAt, &workflowRunID,
		); err != nil {
			return nil, false, classify(err, "PROPOSAL_REVISION_HISTORY_SCAN_FAILED")
		}
		item.ProposalID = query.ProposalID
		item.RevisionID = foundation.ID(revisionID)
		item.TargetMode = domain.NormalizeTargetMode(domain.TargetMode(targetMode))
		if err := bindRevisionHistoryDecision(&item.Approval, &item.WorkflowRunID, query.ProposalID, item.RevisionID, approvalID, approvalHash, approvalDecision, approvedGitHead, approvalDecidedAt, workflowRunID); err != nil {
			return nil, false, err
		}
		if item.RevisionID == "" || item.RevisionNo < 1 || !domain.ValidHash(item.ChangeHash) || item.Approval != nil && item.Approval.ChangeHash != item.ChangeHash {
			return nil, false, revisionHistoryBindingError()
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, classify(err, "PROPOSAL_REVISION_HISTORY_ROWS_FAILED")
	}
	hasMore := len(items) > query.Limit
	if hasMore {
		items = items[:query.Limit]
	}
	return items, hasMore, nil
}

// GetProposalRevision returns one exact file-patch Revision without consulting
// the mutable current Proposal projection first.
func (r *Repository) GetProposalRevision(ctx context.Context, proposalID, revisionID foundation.ID) (domain.ProposalRevisionHistoryDetail, error) {
	var detail domain.ProposalRevisionHistoryDetail
	var workspaceID, persistedRevisionID, targetMode string
	var currentRevisionID *string
	var snapshotProposalID, snapshotRevisionID, snapshotBaseHash, snapshotContent, snapshotSchemaVersion *string
	var snapshotByteSize *int
	var snapshotCreatedAt *time.Time
	var lineageProposalID, lineageRevisionID, sourceRevisionID, sourceChangeHash, lineageKind, mergeAlgorithm, mergeVersion, mergeFingerprint *string
	var lineageCreatedAt *time.Time
	var approvalID, approvalHash, approvalDecision, approvedGitHead, workflowRunID *string
	var approvalDecidedAt *time.Time
	err := r.db.QueryRow(ctx, `
		SELECT p.workspace_id::text,p.current_revision_id::text,
		       persisted.id::text,persisted.revision_no,persisted.target_path,persisted.target_mode,persisted.base_hash,
		       persisted.content,persisted.evidence_summary,persisted.risk,persisted.rollback_plan,persisted.change_hash,persisted.created_at,
		       snapshot.proposal_id::text,snapshot.revision_id::text,snapshot.base_hash,snapshot.content,
		       snapshot.base_byte_size,snapshot.schema_version,snapshot.created_at,
		       lineage.proposal_id::text,lineage.revision_id::text,lineage.source_revision_id::text,lineage.source_change_hash,
		       lineage.lineage_kind,lineage.merge_algorithm,lineage.merge_algorithm_version,lineage.merge_fingerprint,lineage.created_at,
		       approval.id::text,approval.change_hash,approval.decision,approval.approved_git_head,approval.decided_at,
		       dispatch.workflow_run_id::text
		FROM change_control.proposal p
		JOIN change_control.proposal_revision persisted ON persisted.proposal_id=p.id AND persisted.id=$2
		LEFT JOIN change_control.proposal_revision_base_snapshot snapshot
		  ON snapshot.proposal_id=p.id AND snapshot.revision_id=persisted.id
		LEFT JOIN change_control.proposal_revision_lineage lineage
		  ON lineage.proposal_id=p.id AND lineage.revision_id=persisted.id
		LEFT JOIN change_control.approval approval
		  ON approval.proposal_id=p.id AND approval.revision_id=persisted.id
		LEFT JOIN change_control.proposal_revision_dispatch dispatch
		  ON dispatch.proposal_id=p.id AND dispatch.revision_id=persisted.id AND dispatch.approval_id=approval.id
		WHERE p.id=$1 AND p.proposal_type='file_patch'`, string(proposalID), string(revisionID)).Scan(
		&workspaceID, &currentRevisionID,
		&persistedRevisionID, &detail.Revision.RevisionNo, &detail.Revision.TargetPath, &targetMode, &detail.Revision.BaseHash,
		&detail.Revision.Content, &detail.Revision.EvidenceSummary, &detail.Revision.Risk, &detail.Revision.RollbackPlan, &detail.Revision.ChangeHash, &detail.Revision.CreatedAt,
		&snapshotProposalID, &snapshotRevisionID, &snapshotBaseHash, &snapshotContent, &snapshotByteSize, &snapshotSchemaVersion, &snapshotCreatedAt,
		&lineageProposalID, &lineageRevisionID, &sourceRevisionID, &sourceChangeHash, &lineageKind, &mergeAlgorithm, &mergeVersion, &mergeFingerprint, &lineageCreatedAt,
		&approvalID, &approvalHash, &approvalDecision, &approvedGitHead, &approvalDecidedAt, &workflowRunID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ProposalRevisionHistoryDetail{}, foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_REVISION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.ProposalRevisionHistoryDetail{}, classify(err, "PROPOSAL_REVISION_DETAIL_QUERY_FAILED")
	}
	detail.WorkspaceID = foundation.ID(workspaceID)
	detail.ProposalID = proposalID
	if currentRevisionID != nil {
		detail.CurrentRevisionID = foundation.ID(*currentRevisionID)
	}
	detail.Revision.ID = foundation.ID(persistedRevisionID)
	detail.Revision.ProposalID = proposalID
	detail.Revision.TargetMode = domain.NormalizeTargetMode(domain.TargetMode(targetMode))
	if detail.WorkspaceID == "" || detail.Revision.ID != revisionID ||
		domain.ValidateProposalRevisionForType(domain.ProposalTypeFilePatch, detail.Revision) != nil {
		return domain.ProposalRevisionHistoryDetail{}, revisionHistoryBindingError()
	}
	if snapshotRevisionID != nil {
		if snapshotProposalID == nil || snapshotBaseHash == nil || snapshotContent == nil || snapshotByteSize == nil || snapshotSchemaVersion == nil || snapshotCreatedAt == nil {
			return domain.ProposalRevisionHistoryDetail{}, revisionHistoryBindingError()
		}
		detail.Revision.BaseSnapshot = &domain.RevisionBaseSnapshot{
			ProposalID: foundation.ID(*snapshotProposalID), RevisionID: foundation.ID(*snapshotRevisionID), BaseHash: *snapshotBaseHash,
			Content: *snapshotContent, ByteSize: *snapshotByteSize, SchemaVersion: *snapshotSchemaVersion, CreatedAt: *snapshotCreatedAt,
		}
		if detail.Revision.BaseSnapshot.ProposalID != proposalID || detail.Revision.BaseSnapshot.RevisionID != revisionID ||
			detail.Revision.BaseSnapshot.BaseHash != detail.Revision.BaseHash || domain.ValidateRevisionBaseSnapshot(*detail.Revision.BaseSnapshot) != nil {
			return domain.ProposalRevisionHistoryDetail{}, revisionHistoryBindingError()
		}
	}
	if lineageRevisionID != nil {
		if lineageProposalID == nil || sourceRevisionID == nil || sourceChangeHash == nil || lineageKind == nil || mergeAlgorithm == nil || mergeVersion == nil || mergeFingerprint == nil || lineageCreatedAt == nil {
			return domain.ProposalRevisionHistoryDetail{}, revisionHistoryBindingError()
		}
		detail.Revision.Lineage = &domain.RevisionLineage{
			ProposalID: foundation.ID(*lineageProposalID), RevisionID: foundation.ID(*lineageRevisionID), SourceRevisionID: foundation.ID(*sourceRevisionID),
			SourceChangeHash: *sourceChangeHash, Kind: domain.RevisionLineageKind(*lineageKind), MergeAlgorithm: *mergeAlgorithm,
			MergeVersion: *mergeVersion, MergeFingerprint: *mergeFingerprint, CreatedAt: *lineageCreatedAt,
		}
		if detail.Revision.Lineage.ProposalID != proposalID || detail.Revision.Lineage.RevisionID != revisionID ||
			detail.Revision.Lineage.SourceRevisionID == "" || detail.Revision.Lineage.SourceRevisionID == revisionID ||
			!domain.ValidHash(detail.Revision.Lineage.SourceChangeHash) || !domain.ValidHash(detail.Revision.Lineage.MergeFingerprint) ||
			detail.Revision.Lineage.Kind != domain.RevisionLineageDirectEdit && detail.Revision.Lineage.Kind != domain.RevisionLineageThreeWayMerge ||
			detail.Revision.Lineage.MergeAlgorithm == "" || detail.Revision.Lineage.MergeVersion == "" || detail.Revision.Lineage.CreatedAt.IsZero() {
			return domain.ProposalRevisionHistoryDetail{}, revisionHistoryBindingError()
		}
	}
	if err := bindRevisionHistoryDecision(&detail.Approval, &detail.WorkflowRunID, proposalID, detail.Revision.ID, approvalID, approvalHash, approvalDecision, approvedGitHead, approvalDecidedAt, workflowRunID); err != nil {
		return domain.ProposalRevisionHistoryDetail{}, err
	}
	if detail.Approval != nil && detail.Approval.ChangeHash != detail.Revision.ChangeHash {
		return domain.ProposalRevisionHistoryDetail{}, revisionHistoryBindingError()
	}
	return detail, nil
}

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
