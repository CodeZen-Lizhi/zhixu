package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	changecontroleventcontract "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/eventcontract"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

var _ domain.ProposalRevisionRepository = (*GORMRepository)(nil)
var _ domain.ProposalRevisionHistoryRepository = (*GORMRepository)(nil)

const gormChangeListProposalRevisionsSQL = `
	SELECT r.id::text,r.revision_no,COALESCE(r.target_path,''),COALESCE(r.target_mode,'REPLACE'),
	       COALESCE(r.base_hash,''),r.change_hash,(s.revision_id IS NOT NULL),
	       COALESCE(r.id=p.current_revision_id,false),r.created_at,
	       a.id::text,a.change_hash,a.decision,a.approved_git_head,a.decided_at,d.workflow_run_id::text
	FROM change_control.proposal p
	JOIN change_control.proposal_revision r ON r.proposal_id=p.id
	LEFT JOIN change_control.proposal_revision_base_snapshot s ON s.proposal_id=p.id AND s.revision_id=r.id
	LEFT JOIN change_control.approval a ON a.proposal_id=p.id AND a.revision_id=r.id
	LEFT JOIN change_control.proposal_revision_dispatch d ON d.proposal_id=p.id AND d.revision_id=r.id AND d.approval_id=a.id
	WHERE p.id=? AND p.proposal_type='file_patch'
	ORDER BY r.revision_no DESC,r.id DESC
	LIMIT ?`

const gormChangeListProposalRevisionsBeforeSQL = `
	SELECT r.id::text,r.revision_no,COALESCE(r.target_path,''),COALESCE(r.target_mode,'REPLACE'),
	       COALESCE(r.base_hash,''),r.change_hash,(s.revision_id IS NOT NULL),
	       COALESCE(r.id=p.current_revision_id,false),r.created_at,
	       a.id::text,a.change_hash,a.decision,a.approved_git_head,a.decided_at,d.workflow_run_id::text
	FROM change_control.proposal p
	JOIN change_control.proposal_revision r ON r.proposal_id=p.id
	LEFT JOIN change_control.proposal_revision_base_snapshot s ON s.proposal_id=p.id AND s.revision_id=r.id
	LEFT JOIN change_control.approval a ON a.proposal_id=p.id AND a.revision_id=r.id
	LEFT JOIN change_control.proposal_revision_dispatch d ON d.proposal_id=p.id AND d.revision_id=r.id AND d.approval_id=a.id
	WHERE p.id=? AND p.proposal_type='file_patch' AND r.revision_no<?
	ORDER BY r.revision_no DESC,r.id DESC
	LIMIT ?`

// LookupRevisionCommandReceipt reads only immutable receipt-bound facts. It
// intentionally does not consult the current Proposal state so a response-loss
// retry remains replayable after subsequent proposal transitions.
func (repository *GORMRepository) LookupRevisionCommandReceipt(ctx context.Context, lookup domain.RevisionCommandReceiptLookup) (domain.AppendProposalRevisionResult, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.AppendProposalRevisionResult{}, false, err
	}
	if err := domain.ValidateRevisionCommandReceiptLookup(lookup); err != nil {
		return domain.AppendProposalRevisionResult{}, false, revisionCommandError(err)
	}

	var storedHash, proposalID, revisionID string
	var revisionNo int
	var proposalVersion int64
	var receiptCreatedAt time.Time
	row, err := gormChangeRawRow(ctx, repository.database, `
		SELECT request_hash,proposal_id::text,result_revision_id::text,result_revision_no,result_proposal_version,created_at
		FROM change_control.proposal_revision_command
		WHERE workspace_id=? AND idempotency_key=?`, string(lookup.WorkspaceID), lookup.IdempotencyKey)
	if err != nil {
		return domain.AppendProposalRevisionResult{}, false, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_RECEIPT_QUERY_FAILED")
	}
	if err := row.Scan(&storedHash, &proposalID, &revisionID, &revisionNo, &proposalVersion, &receiptCreatedAt); gormChangeNoRows(err) {
		return domain.AppendProposalRevisionResult{}, false, nil
	} else if err != nil {
		return domain.AppendProposalRevisionResult{}, false, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_RECEIPT_QUERY_FAILED")
	}
	if storedHash != lookup.RequestHash || foundation.ID(proposalID) != lookup.ProposalID {
		return domain.AppendProposalRevisionResult{}, false, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("revision idempotency key is bound to another request"))
	}

	var workspaceID, proposalType, riskLevel, proposalIdempotencyKey, proposalRequestHash string
	var proposalCreatedAt, revisionCreatedAt time.Time
	var loadedRevisionNo int
	var targetPath, targetMode, baseHash, content, evidenceSummary, risk, rollbackPlan, changeHash string
	var snapshotProposalID, snapshotRevisionID, snapshotBaseHash, snapshotContent, snapshotSchemaVersion *string
	var snapshotByteSize *int
	var snapshotCreatedAt *time.Time
	row, err = gormChangeRawRow(ctx, repository.database, `
		SELECT p.workspace_id::text,p.proposal_type,p.risk_level,p.idempotency_key,p.request_hash,p.created_at,
		       r.revision_no,r.target_path,r.target_mode,r.base_hash,r.content,r.evidence_summary,r.risk,r.rollback_plan,r.change_hash,r.created_at,
		       s.proposal_id::text,s.revision_id::text,s.base_hash,s.content,s.base_byte_size,s.schema_version,s.created_at
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.id=?
		LEFT JOIN change_control.proposal_revision_base_snapshot s ON s.proposal_id=p.id AND s.revision_id=r.id
		WHERE p.id=?`, revisionID, proposalID)
	if err != nil {
		return domain.AppendProposalRevisionResult{}, false, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_RECEIPT_RESULT_QUERY_FAILED")
	}
	if err := row.Scan(
		&workspaceID, &proposalType, &riskLevel, &proposalIdempotencyKey, &proposalRequestHash, &proposalCreatedAt,
		&loadedRevisionNo, &targetPath, &targetMode, &baseHash, &content, &evidenceSummary, &risk, &rollbackPlan, &changeHash, &revisionCreatedAt,
		&snapshotProposalID, &snapshotRevisionID, &snapshotBaseHash, &snapshotContent, &snapshotByteSize, &snapshotSchemaVersion, &snapshotCreatedAt,
	); gormChangeNoRows(err) {
		return domain.AppendProposalRevisionResult{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_RECEIPT_INVALID", false, errors.New("receipt result revision is unavailable"))
	} else if err != nil {
		return domain.AppendProposalRevisionResult{}, false, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_RECEIPT_RESULT_QUERY_FAILED")
	}
	parsedRiskLevel, riskErr := domain.ValidateProposalRiskLevelForType(domain.ProposalType(proposalType), domain.ProposalRiskLevel(riskLevel))
	if foundation.ID(workspaceID) != lookup.WorkspaceID || domain.NormalizeProposalType(domain.ProposalType(proposalType)) != domain.ProposalTypeFilePatch ||
		riskErr != nil || loadedRevisionNo != revisionNo || proposalVersion <= 0 {
		return domain.AppendProposalRevisionResult{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_RECEIPT_INVALID", false, errors.New("receipt result binding is inconsistent"))
	}
	revision := domain.Revision{
		ID: foundation.ID(revisionID), ProposalID: foundation.ID(proposalID), RevisionNo: loadedRevisionNo,
		TargetPath: targetPath, TargetMode: domain.TargetMode(targetMode), BaseHash: baseHash, Content: content,
		EvidenceSummary: evidenceSummary, Risk: risk, RollbackPlan: rollbackPlan, ChangeHash: changeHash, CreatedAt: revisionCreatedAt,
	}
	if snapshotRevisionID == nil || snapshotProposalID == nil || snapshotBaseHash == nil || snapshotContent == nil || snapshotByteSize == nil || snapshotSchemaVersion == nil || snapshotCreatedAt == nil {
		return domain.AppendProposalRevisionResult{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_RECEIPT_INVALID", false, errors.New("receipt result snapshot is unavailable"))
	}
	snapshot := domain.RevisionBaseSnapshot{
		ProposalID: foundation.ID(*snapshotProposalID), RevisionID: foundation.ID(*snapshotRevisionID), BaseHash: *snapshotBaseHash,
		Content: *snapshotContent, ByteSize: *snapshotByteSize, SchemaVersion: *snapshotSchemaVersion, CreatedAt: *snapshotCreatedAt,
	}
	if snapshot.ProposalID != revision.ProposalID || snapshot.RevisionID != revision.ID || snapshot.BaseHash != revision.BaseHash ||
		domain.ValidateRevisionBaseSnapshot(snapshot) != nil || domain.ValidateProposalRevisionForType(domain.ProposalTypeFilePatch, revision) != nil {
		return domain.AppendProposalRevisionResult{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_RECEIPT_INVALID", false, errors.New("receipt result revision is inconsistent"))
	}
	revision.BaseSnapshot = &snapshot
	proposal := domain.Proposal{
		ID: foundation.ID(proposalID), WorkspaceID: foundation.ID(workspaceID), Type: domain.ProposalTypeFilePatch,
		RiskLevel: parsedRiskLevel, TargetPath: targetPath, IdempotencyKey: proposalIdempotencyKey, RequestHash: proposalRequestHash,
		Status: domain.StatusReady, Version: proposalVersion, CurrentRevisionID: revision.ID,
		CreatedAt: proposalCreatedAt, UpdatedAt: receiptCreatedAt, Revision: revision,
	}
	return domain.AppendProposalRevisionResult{Proposal: proposal, Revision: revision, Replayed: true}, true, nil
}

func (repository *GORMRepository) GetRevisionBaseSnapshot(ctx context.Context, proposalID, revisionID foundation.ID) (domain.RevisionBaseSnapshot, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.RevisionBaseSnapshot{}, false, err
	}
	if proposalID == "" || revisionID == "" {
		return domain.RevisionBaseSnapshot{}, false, revisionCommandError(domain.ErrProposalRevisionBindingMismatch)
	}
	var storedProposalID, storedRevisionID string
	var snapshot domain.RevisionBaseSnapshot
	row, err := gormChangeRawRow(ctx, repository.database, `
		SELECT proposal_id::text,revision_id::text,base_hash,content,base_byte_size,schema_version,created_at
		FROM change_control.proposal_revision_base_snapshot
		WHERE proposal_id=? AND revision_id=?`, string(proposalID), string(revisionID))
	if err != nil {
		return domain.RevisionBaseSnapshot{}, false, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_SNAPSHOT_QUERY_FAILED")
	}
	if err := row.Scan(&storedProposalID, &storedRevisionID, &snapshot.BaseHash, &snapshot.Content, &snapshot.ByteSize, &snapshot.SchemaVersion, &snapshot.CreatedAt); gormChangeNoRows(err) {
		return domain.RevisionBaseSnapshot{}, false, nil
	} else if err != nil {
		return domain.RevisionBaseSnapshot{}, false, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_SNAPSHOT_QUERY_FAILED")
	}
	snapshot.ProposalID, snapshot.RevisionID = foundation.ID(storedProposalID), foundation.ID(storedRevisionID)
	if snapshot.ProposalID != proposalID || snapshot.RevisionID != revisionID || domain.ValidateRevisionBaseSnapshot(snapshot) != nil {
		return domain.RevisionBaseSnapshot{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_BASE_SNAPSHOT_INVALID", false, errors.New("base snapshot is inconsistent"))
	}
	return snapshot, true, nil
}

type gormRevisionReplay struct {
	revisionID foundation.ID
}

func (replay *gormRevisionReplay) Error() string { return "proposal revision receipt replay" }

// AppendRevision preserves the legacy receipt -> authorization -> Proposal
// lock order. A replay signal aborts the UoW before durable root lookup, so no
// transaction remains active while the immutable result is reconstructed.
func (repository *GORMRepository) AppendRevision(ctx context.Context, command domain.AppendProposalRevision) (domain.AppendProposalRevisionResult, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.AppendProposalRevisionResult{}, err
	}
	if err := domain.ValidateAppendProposalRevision(command); err != nil {
		return domain.AppendProposalRevisionResult{}, revisionCommandError(err)
	}
	expectedRequestHash := domain.ComputeRevisionRequestHash(command)
	if command.RequestHash == "" {
		command.RequestHash = expectedRequestHash
	} else if command.RequestHash != expectedRequestHash {
		return domain.AppendProposalRevisionResult{}, revisionCommandError(domain.ErrProposalRevisionBindingMismatch)
	}

	err := repository.within(ctx, foundation.TransactionOptions{},
		"PROPOSAL_REVISION_TRANSACTION_FAILED", "PROPOSAL_REVISION_COMMIT_FAILED", classifyGORMChange,
		func(callbackCtx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
			if _, err := gormChangeExec(callbackCtx, tx, `SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, string(command.WorkspaceID)+"\x1f"+command.IdempotencyKey); err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_REVISION_RECEIPT_LOCK_FAILED")
			}
			var storedHash, storedRevisionID string
			row, err := gormChangeRawRow(callbackCtx, tx, `
			SELECT request_hash,result_revision_id::text
			FROM change_control.proposal_revision_command
			WHERE workspace_id=? AND idempotency_key=?
			FOR UPDATE`, string(command.WorkspaceID), command.IdempotencyKey)
			if err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_REVISION_RECEIPT_QUERY_FAILED")
			}
			if err := row.Scan(&storedHash, &storedRevisionID); err == nil {
				if storedHash != command.RequestHash {
					return foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("revision idempotency key is bound to another request"))
				}
				return &gormRevisionReplay{revisionID: foundation.ID(storedRevisionID)}
			} else if !gormChangeNoRows(err) {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_REVISION_RECEIPT_QUERY_FAILED")
			}

			authorizations, err := gormLockRevisionAuthorizations(callbackCtx, tx, command.ProposalID, command.SourceRevisionID)
			if err != nil {
				return err
			}
			var workspaceID, proposalType, status string
			var currentRevisionID *string
			var version int64
			var sourceNo int
			var targetPath, targetMode, sourceBaseHash, sourceChangeHash string
			row, err = gormChangeRawRow(callbackCtx, tx, `
			SELECT p.workspace_id::text,p.proposal_type,p.status,p.version,p.current_revision_id::text,
			       source.revision_no,source.target_path,source.target_mode,source.base_hash,source.change_hash
			FROM change_control.proposal p
			JOIN change_control.proposal_revision source ON source.proposal_id=p.id AND source.id=?
			WHERE p.id=?
			FOR UPDATE OF p,source`, string(command.SourceRevisionID), string(command.ProposalID))
			if err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_REVISION_QUERY_FAILED")
			}
			if err := row.Scan(&workspaceID, &proposalType, &status, &version, &currentRevisionID,
				&sourceNo, &targetPath, &targetMode, &sourceBaseHash, &sourceChangeHash); gormChangeNoRows(err) {
				return foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_REVISION_NOT_FOUND", false, err)
			} else if err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_REVISION_QUERY_FAILED")
			}
			if foundation.ID(workspaceID) != command.WorkspaceID || proposalType != string(domain.ProposalTypeFilePatch) ||
				domain.NormalizeTargetMode(domain.TargetMode(targetMode)) != domain.TargetModeReplace ||
				(status != string(domain.StatusReady) && status != string(domain.StatusNeedsRevision)) {
				return revisionNotEditable(domain.ErrProposalRevisionNotEditable)
			}
			if version != command.ExpectedProposalVersion || currentRevisionID == nil || *currentRevisionID != string(command.SourceRevisionID) || sourceChangeHash != command.SourceChangeHash {
				return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_STALE", false, domain.ErrProposalRevisionStale)
			}
			if err := gormVerifyRevisionAuthorizationSet(callbackCtx, tx, command.ProposalID, command.SourceRevisionID, authorizations); err != nil {
				return err
			}
			fence, err := gormLoadRevisionSupersedeFacts(callbackCtx, tx, command, status, sourceChangeHash, authorizations)
			if err != nil {
				return err
			}
			if err := validateRevisionSupersedeFacts(command, fence); err != nil {
				return err
			}
			if err := gormRevokeIssuedRevisionAuthorizations(callbackCtx, tx, command, authorizations); err != nil {
				return err
			}

			createdAt := command.CreatedAt.UTC()
			if createdAt.IsZero() {
				return foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_INVALID", false, errors.New("revision creation time is required"))
			}
			changeHash := domain.ComputeChangeHash(targetPath, command.ExpectedCurrentHash, command.FinalContent)
			newNo := sourceNo + 1
			if sourceBaseHash == command.ExpectedCurrentHash {
				if _, err := gormChangeExec(callbackCtx, tx, `
				INSERT INTO change_control.proposal_revision_base_snapshot(
					proposal_id,revision_id,base_hash,content,base_byte_size,schema_version,created_at
				) VALUES(?,?,?,?,octet_length(?),?,?)
				ON CONFLICT(revision_id) DO NOTHING`,
					string(command.ProposalID), string(command.SourceRevisionID), sourceBaseHash, command.CurrentContent, command.CurrentContent,
					domain.ProposalRevisionBaseSnapshotSchemaVersion, createdAt); err != nil {
					return classifyGORMChange(callbackCtx, err, "PROPOSAL_REVISION_SOURCE_SNAPSHOT_CREATE_FAILED")
				}
			}
			if _, err := gormChangeExec(callbackCtx, tx, `
			INSERT INTO change_control.proposal_revision(
				id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
			) VALUES(?,?,?,?,'REPLACE',?,?,?,?,?,?,?)`,
				string(command.NewRevisionID), string(command.ProposalID), newNo, targetPath, command.ExpectedCurrentHash,
				command.FinalContent, command.EvidenceSummary, command.Risk, command.RollbackPlan, changeHash, createdAt); err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_REVISION_CREATE_FAILED")
			}
			if _, err := gormChangeExec(callbackCtx, tx, `
			INSERT INTO change_control.proposal_revision_base_snapshot(
				proposal_id,revision_id,base_hash,content,base_byte_size,schema_version,created_at
			) VALUES(?,?,?,?,octet_length(?),?,?)`,
				string(command.ProposalID), string(command.NewRevisionID), command.ExpectedCurrentHash, command.CurrentContent, command.CurrentContent,
				domain.ProposalRevisionBaseSnapshotSchemaVersion, createdAt); err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_REVISION_SNAPSHOT_CREATE_FAILED")
			}
			lineageKind := domain.RevisionLineageThreeWayMerge
			if sourceBaseHash == command.ExpectedCurrentHash {
				lineageKind = domain.RevisionLineageDirectEdit
			}
			if _, err := gormChangeExec(callbackCtx, tx, `
			INSERT INTO change_control.proposal_revision_lineage(
				proposal_id,revision_id,source_revision_id,source_change_hash,lineage_kind,merge_algorithm,merge_algorithm_version,merge_fingerprint,created_at
			) VALUES(?,?,?,?,?,?,?,?,?)`,
				string(command.ProposalID), string(command.NewRevisionID), string(command.SourceRevisionID), command.SourceChangeHash,
				string(lineageKind), command.MergeAlgorithm, command.MergeAlgorithmVersion, command.PreviewFingerprint, createdAt); err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_REVISION_LINEAGE_CREATE_FAILED")
			}
			changed, err := gormChangeExec(callbackCtx, tx, `
			UPDATE change_control.proposal
			SET current_revision_id=?,status='ready_for_review',updated_at=?,version=version+1
			WHERE id=? AND version=? AND current_revision_id=?`,
				string(command.NewRevisionID), createdAt, string(command.ProposalID), command.ExpectedProposalVersion, string(command.SourceRevisionID))
			if err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_REVISION_POINTER_UPDATE_FAILED")
			}
			if changed != 1 {
				return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_STALE", false, domain.ErrProposalRevisionStale)
			}
			if _, err := gormChangeExec(callbackCtx, tx, `
			INSERT INTO change_control.proposal_revision_command(
				workspace_id,idempotency_key,request_hash,proposal_id,source_revision_id,source_change_hash,expected_proposal_version,
				expected_current_hash,preview_hash,merge_algorithm,merge_algorithm_version,result_revision_id,result_revision_no,result_proposal_version,created_at
			) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				string(command.WorkspaceID), command.IdempotencyKey, command.RequestHash, string(command.ProposalID), string(command.SourceRevisionID),
				command.SourceChangeHash, command.ExpectedProposalVersion, command.ExpectedCurrentHash, command.PreviewFingerprint,
				command.MergeAlgorithm, command.MergeAlgorithmVersion, string(command.NewRevisionID), newNo, command.ExpectedProposalVersion+1, createdAt); err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_REVISION_RECEIPT_CREATE_FAILED")
			}
			if !isNilChangeControlDependency(repository.events) {
				request := changecontroleventcontract.ProposalStatusRequest(
					command.WorkspaceID, command.ProposalID, command.NewRevisionID,
					changecontroleventcontract.ProposalRevisedEventType, string(domain.StatusReady), command.ExpectedProposalVersion+1, createdAt,
				)
				if _, _, err := repository.events.AppendScoped(callbackCtx, scope, request); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		var replay *gormRevisionReplay
		if !errors.As(err, &replay) {
			return domain.AppendProposalRevisionResult{}, err
		}
		result, found, lookupErr := repository.LookupRevisionCommandReceipt(ctx, domain.RevisionCommandReceiptLookup{
			WorkspaceID: command.WorkspaceID, ProposalID: command.ProposalID, IdempotencyKey: command.IdempotencyKey, RequestHash: command.RequestHash,
		})
		if lookupErr != nil {
			return domain.AppendProposalRevisionResult{}, lookupErr
		}
		if !found || result.Revision.ID != replay.revisionID {
			return domain.AppendProposalRevisionResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_RECEIPT_INVALID", false, errors.New("receipt result is unavailable"))
		}
		return result, nil
	}
	result, found, err := repository.LookupRevisionCommandReceipt(ctx, domain.RevisionCommandReceiptLookup{
		WorkspaceID: command.WorkspaceID, ProposalID: command.ProposalID, IdempotencyKey: command.IdempotencyKey, RequestHash: command.RequestHash,
	})
	if err != nil {
		return domain.AppendProposalRevisionResult{}, err
	}
	if !found {
		return domain.AppendProposalRevisionResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_RECEIPT_INVALID", false, errors.New("committed receipt result is unavailable"))
	}
	result.Replayed = false
	return result, nil
}

func (repository *GORMRepository) ListProposalRevisions(ctx context.Context, query domain.ProposalRevisionHistoryQuery) ([]domain.ProposalRevisionHistoryItem, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, false, err
	}
	if err := domain.ValidateProposalRevisionHistoryQuery(query); err != nil {
		return nil, false, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_HISTORY_QUERY_INVALID", false, err)
	}
	statement, arguments := gormChangeListProposalRevisionsSQL, []any{string(query.ProposalID), query.Limit + 1}
	if query.BeforeRevisionNo != nil {
		statement = gormChangeListProposalRevisionsBeforeSQL
		arguments = []any{string(query.ProposalID), *query.BeforeRevisionNo, query.Limit + 1}
	}
	rows, err := gormChangeRawRows(ctx, repository.database, statement, arguments...)
	if err != nil {
		return nil, false, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_HISTORY_QUERY_FAILED")
	}
	defer rows.Close()
	items := make([]domain.ProposalRevisionHistoryItem, 0, query.Limit+1)
	for rows.Next() {
		var item domain.ProposalRevisionHistoryItem
		var revisionID, targetMode string
		var approvalID, approvalHash, approvalDecision, approvedGitHead, workflowRunID *string
		var approvalDecidedAt *time.Time
		if err := rows.Scan(&revisionID, &item.RevisionNo, &item.TargetPath, &targetMode, &item.BaseHash, &item.ChangeHash,
			&item.BaseAvailable, &item.Current, &item.CreatedAt,
			&approvalID, &approvalHash, &approvalDecision, &approvedGitHead, &approvalDecidedAt, &workflowRunID); err != nil {
			return nil, false, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_HISTORY_SCAN_FAILED")
		}
		item.ProposalID, item.RevisionID = query.ProposalID, foundation.ID(revisionID)
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
		return nil, false, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_HISTORY_ROWS_FAILED")
	}
	hasMore := len(items) > query.Limit
	if hasMore {
		items = items[:query.Limit]
	}
	return items, hasMore, nil
}

func (repository *GORMRepository) GetProposalRevision(ctx context.Context, proposalID, revisionID foundation.ID) (domain.ProposalRevisionHistoryDetail, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.ProposalRevisionHistoryDetail{}, err
	}
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
	row, err := gormChangeRawRow(ctx, repository.database, `
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
		JOIN change_control.proposal_revision persisted ON persisted.proposal_id=p.id AND persisted.id=?
		LEFT JOIN change_control.proposal_revision_base_snapshot snapshot ON snapshot.proposal_id=p.id AND snapshot.revision_id=persisted.id
		LEFT JOIN change_control.proposal_revision_lineage lineage ON lineage.proposal_id=p.id AND lineage.revision_id=persisted.id
		LEFT JOIN change_control.approval approval ON approval.proposal_id=p.id AND approval.revision_id=persisted.id
		LEFT JOIN change_control.proposal_revision_dispatch dispatch ON dispatch.proposal_id=p.id AND dispatch.revision_id=persisted.id AND dispatch.approval_id=approval.id
		WHERE p.id=? AND p.proposal_type='file_patch'`, string(revisionID), string(proposalID))
	if err != nil {
		return domain.ProposalRevisionHistoryDetail{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_DETAIL_QUERY_FAILED")
	}
	if err := row.Scan(
		&workspaceID, &currentRevisionID,
		&persistedRevisionID, &detail.Revision.RevisionNo, &detail.Revision.TargetPath, &targetMode, &detail.Revision.BaseHash,
		&detail.Revision.Content, &detail.Revision.EvidenceSummary, &detail.Revision.Risk, &detail.Revision.RollbackPlan, &detail.Revision.ChangeHash, &detail.Revision.CreatedAt,
		&snapshotProposalID, &snapshotRevisionID, &snapshotBaseHash, &snapshotContent, &snapshotByteSize, &snapshotSchemaVersion, &snapshotCreatedAt,
		&lineageProposalID, &lineageRevisionID, &sourceRevisionID, &sourceChangeHash, &lineageKind, &mergeAlgorithm, &mergeVersion, &mergeFingerprint, &lineageCreatedAt,
		&approvalID, &approvalHash, &approvalDecision, &approvedGitHead, &approvalDecidedAt, &workflowRunID,
	); gormChangeNoRows(err) {
		return domain.ProposalRevisionHistoryDetail{}, foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_REVISION_NOT_FOUND", false, err)
	} else if err != nil {
		return domain.ProposalRevisionHistoryDetail{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_DETAIL_QUERY_FAILED")
	}
	detail.WorkspaceID, detail.ProposalID = foundation.ID(workspaceID), proposalID
	if currentRevisionID != nil {
		detail.CurrentRevisionID = foundation.ID(*currentRevisionID)
	}
	detail.Revision.ID, detail.Revision.ProposalID = foundation.ID(persistedRevisionID), proposalID
	detail.Revision.TargetMode = domain.NormalizeTargetMode(domain.TargetMode(targetMode))
	if detail.WorkspaceID == "" || detail.Revision.ID != revisionID || domain.ValidateProposalRevisionForType(domain.ProposalTypeFilePatch, detail.Revision) != nil {
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
