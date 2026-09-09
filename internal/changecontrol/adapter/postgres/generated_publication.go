package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

var _ domain.GeneratedPublicationRetirementRepository = (*GORMRepository)(nil)

// RetireGeneratedPublicationScoped fences approval with the same Proposal/Revision
// locks and retires only an untouched ready candidate. It neither commits nor cancels work.
func (repository *GORMRepository) RetireGeneratedPublicationScoped(ctx context.Context, scope foundation.TransactionScope, request domain.GeneratedPublicationRetirement) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return err
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeGeneratedPublicationInvalid, false, err)
	}
	row, err := gormChangeRawRow(ctx, tx, `
		SELECT p.workspace_id::text,p.proposal_type,p.idempotency_key,p.status,p.version,p.updated_at,
		       COALESCE(p.current_revision_id::text,''),p.workflow_run_id::text,
		       r.target_path,r.target_mode,r.base_hash,r.content,r.change_hash
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.id=?
		WHERE p.id=? AND p.workspace_id=?
		FOR UPDATE OF p,r`, string(request.ProposalRevisionID), string(request.ProposalID), string(request.WorkspaceID))
	if err != nil {
		return classifyGORMChange(ctx, err, "PROPOSAL_GENERATED_PUBLICATION_QUERY_FAILED")
	}
	var workspace, proposalType, key, status, currentRevision, path, mode, base, content, changeHash string
	var workflowID *string
	var version int64
	var updatedAt time.Time
	if err := row.Scan(&workspace, &proposalType, &key, &status, &version, &updatedAt, &currentRevision, &workflowID,
		&path, &mode, &base, &content, &changeHash); gormChangeNoRows(err) {
		return foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_NOT_FOUND", false, err)
	} else if err != nil {
		return classifyGORMChange(ctx, err, "PROPOSAL_GENERATED_PUBLICATION_QUERY_FAILED")
	}
	expectedHash, hashErr := domain.ComputeChangeHashForTarget(request.WorkspaceID, path, domain.TargetMode(mode), base, content)
	if workspace != string(request.WorkspaceID) || proposalType != string(domain.ProposalTypeFilePatch) || key != request.ProposalIdempotencyKey ||
		currentRevision != string(request.ProposalRevisionID) || path != request.TargetPath || mode != string(request.TargetMode) ||
		base != request.BaseVersion || domain.ComputeContentHash([]byte(content)) != request.ContentHash || hashErr != nil || expectedHash != changeHash {
		return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeGeneratedPublicationConflict, false, errors.New("generated publication proposal binding changed"))
	}
	if status != string(domain.StatusReady) || workflowID != nil || version != request.ExpectedProposalVersion {
		return generatedRetirementBusy("generated publication is no longer an unapproved ready candidate")
	}
	// Read side facts only after the Proposal/Revision lock has been acquired.
	// No Authorization locks are needed or taken: ready retirement never revokes
	// credentials, and the approved/writeback lock ordering stays unchanged.
	row, err = gormChangeRawRow(ctx, tx, `SELECT
		EXISTS (SELECT 1 FROM change_control.approval WHERE proposal_id=?) OR
		EXISTS (SELECT 1 FROM change_control.proposal_revision_dispatch WHERE proposal_id=?) OR
		EXISTS (SELECT 1 FROM change_control.tool_authorization WHERE proposal_id=?) OR
		EXISTS (SELECT 1 FROM change_control.writeback_execution WHERE proposal_id=?) OR
		EXISTS (SELECT 1 FROM change_control.proposal_commit WHERE proposal_id=?)`,
		string(request.ProposalID), string(request.ProposalID), string(request.ProposalID), string(request.ProposalID), string(request.ProposalID))
	if err != nil {
		return classifyGORMChange(ctx, err, "PROPOSAL_GENERATED_PUBLICATION_FACTS_FAILED")
	}
	var started bool
	if err := row.Scan(&started); err != nil {
		return classifyGORMChange(ctx, err, "PROPOSAL_GENERATED_PUBLICATION_FACTS_FAILED")
	}
	if started {
		return generatedRetirementBusy("generated publication already has an approval or execution fact")
	}
	at := request.RetiredAt.UTC().Truncate(time.Microsecond)
	if at.Before(updatedAt) {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeGeneratedPublicationInvalid, false, errors.New("generated retirement time precedes its proposal"))
	}
	changed, err := gormChangeExec(ctx, tx, `UPDATE change_control.proposal
		SET status=?,version=version+1,updated_at=?
		WHERE id=? AND workspace_id=? AND current_revision_id=? AND version=? AND status=?`,
		string(domain.StatusNeedsRevision), at, string(request.ProposalID), string(request.WorkspaceID), string(request.ProposalRevisionID),
		request.ExpectedProposalVersion, string(domain.StatusReady))
	if err != nil {
		return classifyGORMChange(ctx, err, "PROPOSAL_GENERATED_PUBLICATION_UPDATE_FAILED")
	}
	if changed != 1 {
		return generatedRetirementBusy("generated publication retirement lost its exact ready version")
	}
	return nil
}

func generatedRetirementBusy(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeGeneratedPublicationBusy, false, errors.New(message))
}
