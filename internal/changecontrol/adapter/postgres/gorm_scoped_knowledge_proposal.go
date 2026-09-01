package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

const gormInitialKnowledgeProposalSQL = `
	SELECT p.id::text,p.workspace_id::text,p.proposal_type,p.idempotency_key,p.request_hash,p.risk_level,
		(CASE WHEN p.current_revision_id IS NULL THEN p.workflow_run_id ELSE d.workflow_run_id END)::text,
		wr.status,p.status,p.version,p.created_at,p.updated_at,p.current_revision_id::text,
		r.id::text,r.revision_no,r.target_path,r.target_mode,r.base_hash,r.content,r.evidence_summary,r.risk,r.rollback_plan,r.change_hash,
		r.target_refs,r.base_versions,r.change_set,r.evidence_refs,
		r.artifact_id::text,r.artifact_revision_id::text,r.artifact_revision_no,r.artifact_version,r.artifact_content_hash,r.artifact_source_coverage,
		r.downstream_workspace_id::text,r.downstream_report_id::text,r.downstream_analysis_version,r.downstream_report_fingerprint,
		r.downstream_source_event_id::text,r.downstream_source_event_version,r.downstream_target_type,r.downstream_target_id::text,
		r.downstream_base_version,r.downstream_action,r.downstream_owner_binding,r.downstream_reason,r.schema_version,r.created_at,
		r.restore_workspace_id::text,r.restore_document_id::text,r.restore_target_commit,r.restore_expected_head,
		r.restore_expected_document_version,r.restore_preview_hash,r.restore_current_content_hash,r.restore_target_content_hash,
		a.id::text,a.change_hash,a.decision,a.approved_git_head,a.decided_at
	FROM change_control.proposal p
	JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.revision_no=1
	LEFT JOIN change_control.approval a ON a.revision_id=r.id
	LEFT JOIN change_control.proposal_revision_dispatch d ON d.proposal_id=p.id AND d.revision_id=r.id AND d.approval_id=a.id
	LEFT JOIN workflow.run wr ON wr.id=CASE WHEN p.current_revision_id IS NULL THEN p.workflow_run_id ELSE d.workflow_run_id END AND wr.workspace_id=p.workspace_id
	WHERE p.workspace_id=? AND p.id=? AND p.proposal_type='knowledge_change'
	FOR UPDATE OF p,r`

// CreateKnowledgeChangeProposalScoped creates or exactly loads the immutable
// initial Knowledge Proposal inside the caller-owned live transaction.
func (repository *GORMRepository) CreateKnowledgeChangeProposalScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	proposal domain.Proposal,
) (domain.Proposal, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Proposal{}, err
	}
	canonical, currentHash, legacyHash, err := gormValidateScopedKnowledgeProposal(proposal)
	if err != nil {
		return domain.Proposal{}, err
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return domain.Proposal{}, changeControlGORMUnavailable(fmt.Errorf("change control scoped proposal transaction is unavailable: %w", err))
	}
	transaction = transaction.WithContext(ctx)

	if canonical.RequestHash == legacyHash && legacyHash != currentHash {
		persisted, found, loadErr := gormLoadScopedKnowledgeProposalByKey(ctx, transaction, canonical)
		if loadErr != nil {
			return domain.Proposal{}, loadErr
		}
		if !found {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("legacy knowledge change request hash is only valid for persisted replay"))
		}
		return persisted, nil
	}

	row, err := gormChangeRawRow(ctx, transaction, `
		INSERT INTO change_control.proposal(
			id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,
			status,version,created_at,updated_at,current_revision_id
		) VALUES(?,?,'knowledge_change',?,?,?,?,?,?,?,?)
		ON CONFLICT(workspace_id,idempotency_key) DO NOTHING
		RETURNING id::text`,
		string(canonical.ID), string(canonical.WorkspaceID), string(canonical.RiskLevel), canonical.IdempotencyKey,
		canonical.RequestHash, string(canonical.Status), canonical.Version, canonical.CreatedAt.UTC(), canonical.UpdatedAt.UTC(),
		string(canonical.Revision.ID),
	)
	if err != nil {
		return domain.Proposal{}, classifyGORMChange(ctx, err, "PROPOSAL_CREATE_FAILED")
	}
	var insertedID string
	if err := row.Scan(&insertedID); gormChangeNoRows(err) {
		persisted, found, loadErr := gormLoadScopedKnowledgeProposalByKey(ctx, transaction, canonical)
		if loadErr != nil {
			return domain.Proposal{}, loadErr
		}
		if !found {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED", false, errors.New("proposal idempotency winner is missing"))
		}
		return persisted, nil
	} else if err != nil {
		return domain.Proposal{}, classifyGORMChange(ctx, err, "PROPOSAL_CREATE_FAILED")
	}
	if foundation.ID(insertedID) != canonical.ID {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_CREATE_FAILED", false, errors.New("created proposal identity is inconsistent"))
	}

	change := *canonical.Revision.KnowledgeChange
	targets, err := json.Marshal(change.TargetRefs)
	if err != nil {
		return domain.Proposal{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	bases, err := json.Marshal(change.BaseVersions)
	if err != nil {
		return domain.Proposal{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	changeSet, err := json.Marshal(change.ChangeSet)
	if err != nil {
		return domain.Proposal{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	evidence, err := json.Marshal(change.EvidenceRefs)
	if err != nil {
		return domain.Proposal{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	changed, err := gormChangeExec(ctx, transaction, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,
			risk,rollback_plan,change_hash,target_refs,base_versions,change_set,evidence_refs,schema_version,created_at
		) VALUES(?,?,?,NULL,'REPLACE',NULL,NULL,NULL,?,?,?,?,?,?,?,?,?)`,
		string(canonical.Revision.ID), string(canonical.ID), canonical.Revision.RevisionNo,
		canonical.Revision.Risk, canonical.Revision.RollbackPlan, canonical.Revision.ChangeHash,
		changeControlJSONB(targets), changeControlJSONB(bases), changeControlJSONB(changeSet), changeControlJSONB(evidence),
		change.SchemaVersion, canonical.Revision.CreatedAt.UTC(),
	)
	if err != nil {
		return domain.Proposal{}, classifyGORMChange(ctx, err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	if changed != 1 {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_CREATE_FAILED", false, errors.New("proposal revision insert affected an unexpected number of rows"))
	}
	return gormLoadInitialKnowledgeProposalScoped(ctx, transaction, canonical.WorkspaceID, canonical.ID)
}

// GetInitialKnowledgeChangeProposalScoped locks and returns Revision 1 from the
// caller-owned transaction, independently of the mutable current pointer.
func (repository *GORMRepository) GetInitialKnowledgeChangeProposalScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	workspaceID foundation.ID,
	proposalID foundation.ID,
) (domain.Proposal, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Proposal{}, err
	}
	if !gormScopedKnowledgeCanonicalID(workspaceID) || !gormScopedKnowledgeCanonicalID(proposalID) {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("knowledge proposal query identity is invalid"))
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return domain.Proposal{}, changeControlGORMUnavailable(fmt.Errorf("change control scoped proposal transaction is unavailable: %w", err))
	}
	return gormLoadInitialKnowledgeProposalScoped(ctx, transaction.WithContext(ctx), workspaceID, proposalID)
}

func gormValidateScopedKnowledgeProposal(proposal domain.Proposal) (domain.Proposal, string, string, error) {
	invalid := func(cause error) (domain.Proposal, string, string, error) {
		return domain.Proposal{}, "", "", foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, cause)
	}
	if proposal.Type != domain.ProposalTypeKnowledgeChange || proposal.Revision.KnowledgeChange == nil {
		return invalid(domain.ErrProposalTypeInvalid)
	}
	if !gormScopedKnowledgeCanonicalID(proposal.ID) || !gormScopedKnowledgeCanonicalID(proposal.WorkspaceID) ||
		!gormScopedKnowledgeCanonicalID(proposal.Revision.ID) || proposal.Revision.ProposalID != proposal.ID ||
		!gormScopedKnowledgeCanonicalID(proposal.Revision.ProposalID) {
		return invalid(errors.New("knowledge proposal identity is invalid"))
	}
	if proposal.IdempotencyKey == "" || strings.TrimSpace(proposal.IdempotencyKey) != proposal.IdempotencyKey ||
		len(proposal.IdempotencyKey) > 128 || strings.ContainsAny(proposal.IdempotencyKey, "\r\n") ||
		!domain.ValidHash(proposal.RequestHash) || proposal.RequestHash != strings.ToLower(proposal.RequestHash) {
		return invalid(errors.New("knowledge proposal idempotency binding is invalid"))
	}
	if proposal.Status != domain.StatusReady || proposal.Version != 1 || proposal.Revision.RevisionNo != 1 ||
		proposal.CreatedAt.IsZero() || proposal.UpdatedAt.IsZero() || proposal.Revision.CreatedAt.IsZero() ||
		proposal.UpdatedAt.Before(proposal.CreatedAt) || proposal.CurrentRevisionID != "" && proposal.CurrentRevisionID != proposal.Revision.ID ||
		proposal.WorkflowRunID != nil || proposal.WorkflowRunStatus != "" || proposal.Approval != nil || strings.TrimSpace(proposal.TargetPath) != "" {
		return invalid(errors.New("knowledge proposal initial state is invalid"))
	}
	risk, err := domain.ValidateProposalRiskLevelForType(proposal.Type, proposal.RiskLevel)
	if err != nil {
		return invalid(err)
	}
	proposal.RiskLevel = risk
	if err := domain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision); err != nil {
		return invalid(err)
	}
	canonicalChange, err := domain.ValidateKnowledgeChange(*proposal.Revision.KnowledgeChange)
	if err != nil {
		return invalid(err)
	}
	proposal.Revision.TargetMode = domain.TargetModeReplace
	proposal.Revision.KnowledgeChange = &canonicalChange
	if err := domain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision); err != nil {
		return invalid(err)
	}
	currentHash, err := domain.ComputeKnowledgeChangeRequestHashWithRiskLevel(
		proposal.WorkspaceID, canonicalChange, risk, proposal.Revision.Risk, proposal.Revision.RollbackPlan,
	)
	if err != nil {
		return invalid(err)
	}
	legacyHash, err := domain.ComputeKnowledgeChangeRequestHash(
		proposal.WorkspaceID, canonicalChange, proposal.Revision.Risk, proposal.Revision.RollbackPlan,
	)
	if err != nil || proposal.RequestHash != currentHash && proposal.RequestHash != legacyHash {
		if err == nil {
			err = errors.New("knowledge proposal request hash mismatch")
		}
		return invalid(err)
	}
	return proposal, currentHash, legacyHash, nil
}

func gormLoadScopedKnowledgeProposalByKey(
	ctx context.Context,
	transaction *gorm.DB,
	requested domain.Proposal,
) (domain.Proposal, bool, error) {
	row, err := gormChangeRawRow(ctx, transaction, `
		SELECT id::text,proposal_type,request_hash,risk_level
		FROM change_control.proposal
		WHERE workspace_id=? AND idempotency_key=?
		FOR UPDATE`, string(requested.WorkspaceID), requested.IdempotencyKey)
	if err != nil {
		return domain.Proposal{}, false, classifyGORMChange(ctx, err, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
	}
	var proposalID, proposalType, requestHash, riskLevel string
	if err := row.Scan(&proposalID, &proposalType, &requestHash, &riskLevel); gormChangeNoRows(err) {
		return domain.Proposal{}, false, nil
	} else if err != nil {
		return domain.Proposal{}, false, classifyGORMChange(ctx, err, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
	}
	storedType := domain.NormalizeProposalType(domain.ProposalType(proposalType))
	storedRisk, err := domain.ValidateProposalRiskLevelForType(storedType, domain.ProposalRiskLevel(riskLevel))
	if err != nil {
		return domain.Proposal{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_RISK_LEVEL_INVALID", false, err)
	}
	if storedType != requested.Type || storedRisk != requested.RiskLevel || !gormChangeProposalHashMatches(requested, requestHash) {
		return domain.Proposal{}, false, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
	}
	persisted, err := gormLoadInitialKnowledgeProposalScoped(ctx, transaction, requested.WorkspaceID, foundation.ID(proposalID))
	if err != nil {
		return domain.Proposal{}, false, err
	}
	if persisted.Revision.ChangeHash != requested.Revision.ChangeHash || persisted.IdempotencyKey != requested.IdempotencyKey ||
		!gormChangeProposalHashMatches(requested, persisted.RequestHash) {
		return domain.Proposal{}, false, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal revision"))
	}
	return persisted, true, nil
}

func gormLoadInitialKnowledgeProposalScoped(
	ctx context.Context,
	transaction *gorm.DB,
	workspaceID foundation.ID,
	proposalID foundation.ID,
) (domain.Proposal, error) {
	row, err := gormChangeRawRow(ctx, transaction, gormInitialKnowledgeProposalSQL, string(workspaceID), string(proposalID))
	if err != nil {
		return domain.Proposal{}, classifyGORMChange(ctx, err, "PROPOSAL_QUERY_FAILED")
	}
	proposal, err := scanProposal(row)
	if gormChangeNoRows(err) {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Proposal{}, classifyGORMChange(ctx, err, "PROPOSAL_QUERY_FAILED")
	}
	if proposal.WorkspaceID != workspaceID || proposal.ID != proposalID || proposal.Type != domain.ProposalTypeKnowledgeChange ||
		proposal.Revision.ProposalID != proposal.ID || proposal.Revision.RevisionNo != 1 {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_QUERY_FAILED", false, errors.New("initial knowledge proposal binding is inconsistent"))
	}
	return proposal, nil
}

func gormScopedKnowledgeCanonicalID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
