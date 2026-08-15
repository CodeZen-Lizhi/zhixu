package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	changecontroleventcontract "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/eventcontract"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

var _ domain.ProposalRevisionRepository = (*Repository)(nil)

// LookupRevisionCommandReceipt resolves the exact immutable result without
// consulting Proposal status or its current Revision pointer. This allows a
// response-loss retry to replay after later revisions or state transitions.
func (r *Repository) LookupRevisionCommandReceipt(ctx context.Context, lookup domain.RevisionCommandReceiptLookup) (domain.AppendProposalRevisionResult, bool, error) {
	if err := domain.ValidateRevisionCommandReceiptLookup(lookup); err != nil {
		return domain.AppendProposalRevisionResult{}, false, revisionCommandError(err)
	}
	var storedHash, proposalID, revisionID string
	var revisionNo int
	var proposalVersion int64
	var receiptCreatedAt time.Time
	err := r.db.QueryRow(ctx, `
		SELECT request_hash,proposal_id::text,result_revision_id::text,result_revision_no,result_proposal_version,created_at
		FROM change_control.proposal_revision_command
		WHERE workspace_id=$1 AND idempotency_key=$2`, string(lookup.WorkspaceID), lookup.IdempotencyKey).Scan(
		&storedHash, &proposalID, &revisionID, &revisionNo, &proposalVersion, &receiptCreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AppendProposalRevisionResult{}, false, nil
	}
	if err != nil {
		return domain.AppendProposalRevisionResult{}, false, classify(err, "PROPOSAL_REVISION_RECEIPT_QUERY_FAILED")
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
	err = r.db.QueryRow(ctx, `
		SELECT p.workspace_id::text,p.proposal_type,p.risk_level,p.idempotency_key,p.request_hash,p.created_at,
		       r.revision_no,r.target_path,r.target_mode,r.base_hash,r.content,r.evidence_summary,r.risk,r.rollback_plan,r.change_hash,r.created_at,
		       s.proposal_id::text,s.revision_id::text,s.base_hash,s.content,s.base_byte_size,s.schema_version,s.created_at
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.id=$2
		LEFT JOIN change_control.proposal_revision_base_snapshot s ON s.proposal_id=p.id AND s.revision_id=r.id
		WHERE p.id=$1`, proposalID, revisionID).Scan(
		&workspaceID, &proposalType, &riskLevel, &proposalIdempotencyKey, &proposalRequestHash, &proposalCreatedAt,
		&loadedRevisionNo, &targetPath, &targetMode, &baseHash, &content, &evidenceSummary, &risk, &rollbackPlan, &changeHash, &revisionCreatedAt,
		&snapshotProposalID, &snapshotRevisionID, &snapshotBaseHash, &snapshotContent, &snapshotByteSize, &snapshotSchemaVersion, &snapshotCreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AppendProposalRevisionResult{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_RECEIPT_INVALID", false, errors.New("receipt result revision is unavailable"))
	}
	if err != nil {
		return domain.AppendProposalRevisionResult{}, false, classify(err, "PROPOSAL_REVISION_RECEIPT_RESULT_QUERY_FAILED")
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

// GetRevisionBaseSnapshot reads the immutable base bound to one exact
// Proposal Revision. Legacy rows are represented by found=false.
func (r *Repository) GetRevisionBaseSnapshot(ctx context.Context, proposalID, revisionID foundation.ID) (domain.RevisionBaseSnapshot, bool, error) {
	if proposalID == "" || revisionID == "" {
		return domain.RevisionBaseSnapshot{}, false, revisionCommandError(domain.ErrProposalRevisionBindingMismatch)
	}
	var proposalIDText, revisionIDText string
	var snapshot domain.RevisionBaseSnapshot
	err := r.db.QueryRow(ctx, `
		SELECT proposal_id::text,revision_id::text,base_hash,content,base_byte_size,schema_version,created_at
		FROM change_control.proposal_revision_base_snapshot
		WHERE proposal_id=$1 AND revision_id=$2`, string(proposalID), string(revisionID)).Scan(
		&proposalIDText, &revisionIDText, &snapshot.BaseHash, &snapshot.Content, &snapshot.ByteSize, &snapshot.SchemaVersion, &snapshot.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RevisionBaseSnapshot{}, false, nil
	}
	if err != nil {
		return domain.RevisionBaseSnapshot{}, false, classify(err, "PROPOSAL_REVISION_SNAPSHOT_QUERY_FAILED")
	}
	snapshot.ProposalID = foundation.ID(proposalIDText)
	snapshot.RevisionID = foundation.ID(revisionIDText)
	if snapshot.ProposalID != proposalID || snapshot.RevisionID != revisionID || domain.ValidateRevisionBaseSnapshot(snapshot) != nil {
		return domain.RevisionBaseSnapshot{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_BASE_SNAPSHOT_INVALID", false, errors.New("base snapshot is inconsistent"))
	}
	return snapshot, true, nil
}

// AppendRevision atomically appends one immutable REPLACE revision, its base
// snapshot, lineage and idempotency receipt, then advances the explicit
// current-revision pointer. Workspace I/O and merge recomputation belong to
// Application; this method only persists already server-verified facts.
func (r *Repository) AppendRevision(ctx context.Context, command domain.AppendProposalRevision) (domain.AppendProposalRevisionResult, error) {
	if err := domain.ValidateAppendProposalRevision(command); err != nil {
		return domain.AppendProposalRevisionResult{}, revisionCommandError(err)
	}
	expectedRequestHash := domain.ComputeRevisionRequestHash(command)
	if command.RequestHash == "" {
		command.RequestHash = expectedRequestHash
	} else if command.RequestHash != expectedRequestHash {
		return domain.AppendProposalRevisionResult{}, revisionCommandError(domain.ErrProposalRevisionBindingMismatch)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.AppendProposalRevisionResult{}, classify(err, "PROPOSAL_REVISION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// This lock serializes first writers for one receipt key while allowing
	// different proposals in the same workspace to proceed independently.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(command.WorkspaceID)+"\x1f"+command.IdempotencyKey); err != nil {
		return domain.AppendProposalRevisionResult{}, classify(err, "PROPOSAL_REVISION_RECEIPT_LOCK_FAILED")
	}
	var storedHash, storedRevisionID string
	err = tx.QueryRow(ctx, `
		SELECT request_hash,result_revision_id::text
		FROM change_control.proposal_revision_command
		WHERE workspace_id=$1 AND idempotency_key=$2
		FOR UPDATE`, string(command.WorkspaceID), command.IdempotencyKey).Scan(&storedHash, &storedRevisionID)
	if err == nil {
		if storedHash != command.RequestHash {
			return domain.AppendProposalRevisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("revision idempotency key is bound to another request"))
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.AppendProposalRevisionResult{}, classify(err, "PROPOSAL_REVISION_REPLAY_COMMIT_FAILED")
		}
		result, found, err := r.LookupRevisionCommandReceipt(ctx, domain.RevisionCommandReceiptLookup{
			WorkspaceID: command.WorkspaceID, ProposalID: command.ProposalID, IdempotencyKey: command.IdempotencyKey, RequestHash: command.RequestHash,
		})
		if err != nil {
			return domain.AppendProposalRevisionResult{}, err
		}
		if !found || result.Revision.ID != foundation.ID(storedRevisionID) {
			return domain.AppendProposalRevisionResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_RECEIPT_INVALID", false, errors.New("receipt result is unavailable"))
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.AppendProposalRevisionResult{}, classify(err, "PROPOSAL_REVISION_RECEIPT_QUERY_FAILED")
	}
	authorizations, err := lockRevisionAuthorizations(ctx, tx, command.ProposalID, command.SourceRevisionID)
	if err != nil {
		return domain.AppendProposalRevisionResult{}, err
	}

	var workspaceID, proposalType, status string
	var currentRevisionID *string
	var version int64
	var sourceNo int
	var targetPath, targetMode, sourceBaseHash, sourceChangeHash string
	err = tx.QueryRow(ctx, `
			SELECT p.workspace_id::text,p.proposal_type,p.status,p.version,p.current_revision_id::text,
			       source.revision_no,source.target_path,source.target_mode,source.base_hash,source.change_hash
		FROM change_control.proposal p
		JOIN change_control.proposal_revision source
		  ON source.proposal_id=p.id AND source.id=$2
		WHERE p.id=$1
		FOR UPDATE OF p,source`, string(command.ProposalID), string(command.SourceRevisionID)).Scan(
		&workspaceID, &proposalType, &status, &version, &currentRevisionID,
		&sourceNo, &targetPath, &targetMode, &sourceBaseHash, &sourceChangeHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AppendProposalRevisionResult{}, foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_REVISION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.AppendProposalRevisionResult{}, classify(err, "PROPOSAL_REVISION_QUERY_FAILED")
	}
	if foundation.ID(workspaceID) != command.WorkspaceID || proposalType != string(domain.ProposalTypeFilePatch) ||
		domain.NormalizeTargetMode(domain.TargetMode(targetMode)) != domain.TargetModeReplace ||
		(status != string(domain.StatusReady) && status != string(domain.StatusNeedsRevision)) {
		return domain.AppendProposalRevisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_NOT_EDITABLE", false, domain.ErrProposalRevisionNotEditable)
	}
	if version != command.ExpectedProposalVersion || currentRevisionID == nil || *currentRevisionID != string(command.SourceRevisionID) || sourceChangeHash != command.SourceChangeHash {
		return domain.AppendProposalRevisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_STALE", false, domain.ErrProposalRevisionStale)
	}
	if err := verifyRevisionAuthorizationSet(ctx, tx, command.ProposalID, command.SourceRevisionID, authorizations); err != nil {
		return domain.AppendProposalRevisionResult{}, err
	}
	fence, err := loadRevisionSupersedeFacts(ctx, tx, command, status, sourceChangeHash, authorizations)
	if err != nil {
		return domain.AppendProposalRevisionResult{}, err
	}
	if err := validateRevisionSupersedeFacts(command, fence); err != nil {
		return domain.AppendProposalRevisionResult{}, err
	}
	if err := revokeIssuedRevisionAuthorizations(ctx, tx, command, authorizations); err != nil {
		return domain.AppendProposalRevisionResult{}, err
	}

	changeHash := domain.ComputeChangeHash(targetPath, command.ExpectedCurrentHash, command.FinalContent)
	createdAt := command.CreatedAt.UTC()
	if createdAt.IsZero() {
		return domain.AppendProposalRevisionResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_INVALID", false, errors.New("revision creation time is required"))
	}
	newNo := sourceNo + 1
	if sourceBaseHash == command.ExpectedCurrentHash {
		if _, err := tx.Exec(ctx, `
			INSERT INTO change_control.proposal_revision_base_snapshot(
				proposal_id,revision_id,base_hash,content,base_byte_size,schema_version,created_at
			) VALUES($1,$2,$3,$4,octet_length($4),$5,$6)
			ON CONFLICT(revision_id) DO NOTHING`,
			string(command.ProposalID), string(command.SourceRevisionID), sourceBaseHash, command.CurrentContent,
			domain.ProposalRevisionBaseSnapshotSchemaVersion, createdAt); err != nil {
			return domain.AppendProposalRevisionResult{}, classify(err, "PROPOSAL_REVISION_SOURCE_SNAPSHOT_CREATE_FAILED")
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
		) VALUES($1,$2,$3,$4,'REPLACE',$5,$6,$7,$8,$9,$10,$11)`,
		string(command.NewRevisionID), string(command.ProposalID), newNo, targetPath, command.ExpectedCurrentHash,
		command.FinalContent, command.EvidenceSummary, command.Risk, command.RollbackPlan, changeHash, createdAt); err != nil {
		return domain.AppendProposalRevisionResult{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.proposal_revision_base_snapshot(
			proposal_id,revision_id,base_hash,content,base_byte_size,schema_version,created_at
			) VALUES($1,$2,$3,$4,octet_length($4),$5,$6)`,
		string(command.ProposalID), string(command.NewRevisionID), command.ExpectedCurrentHash, command.CurrentContent,
		domain.ProposalRevisionBaseSnapshotSchemaVersion, createdAt); err != nil {
		return domain.AppendProposalRevisionResult{}, classify(err, "PROPOSAL_REVISION_SNAPSHOT_CREATE_FAILED")
	}
	lineageKind := domain.RevisionLineageThreeWayMerge
	if sourceBaseHash == command.ExpectedCurrentHash {
		lineageKind = domain.RevisionLineageDirectEdit
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.proposal_revision_lineage(
			proposal_id,revision_id,source_revision_id,source_change_hash,lineage_kind,merge_algorithm,merge_algorithm_version,merge_fingerprint,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		string(command.ProposalID), string(command.NewRevisionID), string(command.SourceRevisionID), command.SourceChangeHash,
		string(lineageKind), command.MergeAlgorithm, command.MergeAlgorithmVersion, command.PreviewFingerprint, createdAt); err != nil {
		return domain.AppendProposalRevisionResult{}, classify(err, "PROPOSAL_REVISION_LINEAGE_CREATE_FAILED")
	}
	commandResult, err := tx.Exec(ctx, `
		UPDATE change_control.proposal
		SET current_revision_id=$1,status='ready_for_review',updated_at=$2,version=version+1
		WHERE id=$3 AND version=$4 AND current_revision_id=$5`,
		string(command.NewRevisionID), createdAt, string(command.ProposalID), command.ExpectedProposalVersion, string(command.SourceRevisionID))
	if err != nil {
		return domain.AppendProposalRevisionResult{}, classify(err, "PROPOSAL_REVISION_POINTER_UPDATE_FAILED")
	}
	if commandResult.RowsAffected() != 1 {
		return domain.AppendProposalRevisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_STALE", false, domain.ErrProposalRevisionStale)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.proposal_revision_command(
			workspace_id,idempotency_key,request_hash,proposal_id,source_revision_id,source_change_hash,expected_proposal_version,
			expected_current_hash,preview_hash,merge_algorithm,merge_algorithm_version,result_revision_id,result_revision_no,result_proposal_version,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		string(command.WorkspaceID), command.IdempotencyKey, command.RequestHash, string(command.ProposalID), string(command.SourceRevisionID),
		command.SourceChangeHash, command.ExpectedProposalVersion, command.ExpectedCurrentHash, command.PreviewFingerprint,
		command.MergeAlgorithm, command.MergeAlgorithmVersion, string(command.NewRevisionID), newNo, command.ExpectedProposalVersion+1, createdAt); err != nil {
		return domain.AppendProposalRevisionResult{}, classify(err, "PROPOSAL_REVISION_RECEIPT_CREATE_FAILED")
	}
	if !isNilChangeControlDependency(r.events) {
		request := changecontroleventcontract.ProposalStatusRequest(
			command.WorkspaceID, command.ProposalID, command.NewRevisionID,
			changecontroleventcontract.ProposalRevisedEventType, string(domain.StatusReady), command.ExpectedProposalVersion+1, createdAt,
		)
		if _, _, err := r.events.AppendTx(ctx, tx, request); err != nil {
			return domain.AppendProposalRevisionResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AppendProposalRevisionResult{}, classify(err, "PROPOSAL_REVISION_COMMIT_FAILED")
	}
	result, found, err := r.LookupRevisionCommandReceipt(ctx, domain.RevisionCommandReceiptLookup{
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

func revisionCommandError(err error) error {
	switch {
	case errors.Is(err, domain.ErrProposalRevisionInputTooLarge):
		return foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_INPUT_TOO_LARGE", false, err)
	case errors.Is(err, domain.ErrProposalRevisionStale):
		return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_STALE", false, err)
	default:
		return foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_INVALID", false, err)
	}
}
