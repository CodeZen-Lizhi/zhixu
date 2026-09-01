package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	changecontroleventcontract "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/eventcontract"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// GORMApprovedRelationApplyRepository is the staged GORM implementation of
// the atomic approved Proposal to Knowledge Relation protocol. Production
// composition continues to use the legacy adapter until the PostgreSQL
// compatibility gate passes.
type GORMApprovedRelationApplyRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
	ids        foundation.IDGenerator
	clock      foundation.Clock
	events     eventsapplication.ScopedAppender
}

var _ knowledgeapplication.ApprovedRelationApplyPort = (*GORMApprovedRelationApplyRepository)(nil)
var _ knowledgeapplication.ApprovedRelationApprovalPort = (*GORMApprovedRelationApplyRepository)(nil)

// NewGORMApprovedRelationApplyRepository derives all local transaction state
// from one Pool. Scoped events must be built from the same Pool by composition.
func NewGORMApprovedRelationApplyRepository(
	pool *platformpostgres.Pool,
	ids foundation.IDGenerator,
	clock foundation.Clock,
	appenders ...eventsapplication.ScopedAppender,
) (*GORMApprovedRelationApplyRepository, error) {
	if pool == nil || isNilKnowledgeGORMDependency(ids) || isNilKnowledgeGORMDependency(clock) {
		return nil, relationApplyUnavailable(errors.New("relation apply GORM dependencies are unavailable"))
	}
	if len(appenders) > 1 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "RELATION_PROPOSAL_APPLY_EVENT_APPENDER_INVALID", false, errors.New("only one relation apply event appender may be configured"))
	}
	database, err := pool.GORM()
	if err != nil || !validKnowledgeGORMDatabase(database) {
		return nil, relationApplyUnavailable(errors.New("relation apply GORM database is unavailable"))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil || isNilKnowledgeGORMDependency(unitOfWork) {
		return nil, relationApplyUnavailable(errors.New("relation apply GORM unit of work is unavailable"))
	}
	var events eventsapplication.ScopedAppender
	if len(appenders) == 1 {
		if isNilKnowledgeGORMDependency(appenders[0]) {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, "RELATION_PROPOSAL_APPLY_EVENT_APPENDER_INVALID", false, errors.New("relation apply event appender is nil"))
		}
		events = appenders[0]
	}
	return &GORMApprovedRelationApplyRepository{database: database, unitOfWork: unitOfWork, ids: ids, clock: clock, events: events}, nil
}

func (repository *GORMApprovedRelationApplyRepository) ready(ctx context.Context) error {
	if repository == nil || !validKnowledgeGORMDatabase(repository.database) || isNilKnowledgeGORMDependency(repository.unitOfWork) ||
		isNilKnowledgeGORMDependency(repository.ids) || isNilKnowledgeGORMDependency(repository.clock) {
		return relationApplyUnavailable(errors.New("relation apply GORM repository is unavailable"))
	}
	if ctx == nil {
		return relationApplyInvalid(errors.New("relation apply context is nil"))
	}
	return nil
}

// ApproveAndApplyRelation atomically persists/replays Approval and applies its
// Relation. The scoped Event collaborator participates in exactly this scope.
func (repository *GORMApprovedRelationApplyRepository) ApproveAndApplyRelation(ctx context.Context, approval changecontroldomain.Approval) (changecontroldomain.Approval, knowledgeapplication.ApprovedRelationApplyResult, error) {
	if err := repository.ready(ctx); err != nil {
		return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if err := validateRelationApprovalInput(approval); err != nil {
		return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyInvalid(err)
	}
	var persisted changecontroldomain.Approval
	var result knowledgeapplication.ApprovedRelationApplyResult
	stale, committed, err := repository.withinRelationApply(ctx, "RELATION_PROPOSAL_APPROVAL_TRANSACTION_FAILED", "RELATION_PROPOSAL_APPROVAL_COMMIT_FAILED", "RELATION_PROPOSAL_APPROVAL_STALE_COMMIT_FAILED", func(callbackCtx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		target, targetErr := gormLockRelationApplyCandidateBeforeProposal(callbackCtx, tx, approval.ProposalID, approval.RevisionID)
		if targetErr != nil {
			return targetErr
		}
		binding, bindingErr := gormLoadRelationApprovalBinding(callbackCtx, tx, approval)
		if bindingErr != nil {
			return bindingErr
		}
		if target.workspaceID != binding.workspaceID {
			return relationApplyConsistency(errors.New("relation approval workspace binding changed"))
		}
		value, persistErr := repository.persistApprovalScoped(callbackCtx, scope, tx, approval, binding)
		if persistErr != nil {
			return persistErr
		}
		persisted = value
		command := knowledgeapplication.ApprovedRelationApplyCommand{WorkspaceID: binding.workspaceID, ProposalID: value.ProposalID, RevisionID: value.RevisionID, ApprovalID: value.ID}
		requestHash, hashErr := knowledgeapplication.RelationApplyRequestHash(command)
		if hashErr != nil {
			return relationApplyInvalid(hashErr)
		}
		valueResult, applyErr := repository.applyScoped(callbackCtx, scope, tx, command, knowledgeapplication.RelationApplyIdempotencyKey(command.ApprovalID), requestHash)
		if applyErr == nil {
			result = valueResult
		}
		return applyErr
	})
	if err != nil {
		return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if stale != nil {
		if !committed {
			return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, stale
		}
		return persisted, knowledgeapplication.ApprovedRelationApplyResult{}, foundation.NewError(foundation.ErrorVersionConflict, "RELATION_PROPOSAL_BASE_STALE", false, stale)
	}
	return persisted, result, nil
}

// ApplyApprovedRelation applies a durable Approval in one scope. A baseline
// change commits only the needs_revision transition and returns a conflict.
func (repository *GORMApprovedRelationApplyRepository) ApplyApprovedRelation(ctx context.Context, command knowledgeapplication.ApprovedRelationApplyCommand) (knowledgeapplication.ApprovedRelationApplyResult, error) {
	if err := repository.ready(ctx); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if err := knowledgeapplication.ValidateApprovedRelationApplyCommand(command); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	requestHash, err := knowledgeapplication.RelationApplyRequestHash(command)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyInvalid(err)
	}
	var result knowledgeapplication.ApprovedRelationApplyResult
	stale, committed, err := repository.withinRelationApply(ctx, "RELATION_PROPOSAL_APPLY_TRANSACTION_FAILED", "RELATION_PROPOSAL_APPLY_COMMIT_FAILED", "RELATION_PROPOSAL_APPLY_STALE_COMMIT_FAILED", func(callbackCtx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		target, targetErr := gormLockRelationApplyCandidateBeforeProposal(callbackCtx, tx, command.ProposalID, command.RevisionID)
		if targetErr != nil {
			return targetErr
		}
		if target.workspaceID != command.WorkspaceID {
			return relationApplyConsistency(errors.New("relation apply workspace binding changed"))
		}
		value, applyErr := repository.applyScoped(callbackCtx, scope, tx, command, knowledgeapplication.RelationApplyIdempotencyKey(command.ApprovalID), requestHash)
		if applyErr == nil {
			result = value
		}
		return applyErr
	})
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if stale != nil {
		if !committed {
			return knowledgeapplication.ApprovedRelationApplyResult{}, stale
		}
		return knowledgeapplication.ApprovedRelationApplyResult{}, foundation.NewError(foundation.ErrorVersionConflict, "RELATION_PROPOSAL_BASE_STALE", false, stale)
	}
	return result, nil
}

// withinRelationApply deliberately commits only relationApplyNeedsRevision.
// Every other callback failure returns to UnitOfWork and is rolled back.
func (repository *GORMApprovedRelationApplyRepository) withinRelationApply(ctx context.Context, transactionCode, commitCode, staleCommitCode string, work func(context.Context, foundation.TransactionScope, *gorm.DB) error) (*relationApplyNeedsRevision, bool, error) {
	var stale *relationApplyNeedsRevision
	callbackSucceeded := false
	err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		tx, unwrapErr := platformpostgres.GORMTransaction(scope)
		if unwrapErr != nil {
			return relationApplyUnavailable(unwrapErr)
		}
		workErr := work(callbackCtx, scope, tx.WithContext(callbackCtx))
		if errors.As(workErr, &stale) {
			callbackSucceeded = true
			return nil
		}
		callbackSucceeded = workErr == nil
		return workErr
	})
	if err != nil {
		code := transactionCode
		if callbackSucceeded {
			code = commitCode
			if stale != nil {
				code = staleCommitCode
			}
		}
		return nil, false, classifyGORMRelationApply(ctx, err, code)
	}
	return stale, true, nil
}

func gormLockRelationApplyCandidateBeforeProposal(ctx context.Context, tx *gorm.DB, proposalID, revisionID foundation.ID) (relationApplyLockTarget, error) {
	var target relationApplyLockTarget
	var workspaceID string
	var refsRaw knowledgeJSONB
	row, err := gormKnowledgeRawRow(ctx, tx, `
		SELECT p.workspace_id::text,r.target_refs
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.id=?::uuid
		WHERE p.id=?::uuid
		  AND (p.current_revision_id=r.id OR p.current_revision_id IS NULL)`, string(revisionID), string(proposalID))
	if err != nil {
		return target, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_LOCK_TARGET_QUERY_FAILED")
	}
	if err := row.Scan(&workspaceID, &refsRaw); gormKnowledgeNoRows(err) {
		return target, relationApplyNotFound(err)
	} else if err != nil {
		return target, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_LOCK_TARGET_QUERY_FAILED")
	}
	var refs []changecontroldomain.KnowledgeTargetRef
	if json.Unmarshal(refsRaw, &refs) != nil || len(refs) != 1 || refs[0].Type != changecontroldomain.KnowledgeTargetRefRelationCandidate {
		return target, relationApplyConsistency(errors.New("knowledge relation proposal candidate target is invalid"))
	}
	parsed, parseErr := foundation.ParseID(string(refs[0].ID))
	if parseErr != nil || parsed != refs[0].ID {
		return target, relationApplyConsistency(errors.New("knowledge relation proposal candidate identity is invalid"))
	}
	target.workspaceID, target.candidateID = foundation.ID(workspaceID), refs[0].ID
	row, err = gormKnowledgeRawRow(ctx, tx, `
		SELECT id::text FROM graph.semantic_link_candidate
		WHERE workspace_id=?::uuid AND id=?::uuid FOR UPDATE`, string(target.workspaceID), string(target.candidateID))
	if err != nil {
		return relationApplyLockTarget{}, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_CANDIDATE_LOCK_FAILED")
	}
	var lockedID string
	if err := row.Scan(&lockedID); gormKnowledgeNoRows(err) {
		return target, nil
	} else if err != nil {
		return relationApplyLockTarget{}, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_CANDIDATE_LOCK_FAILED")
	}
	return target, nil
}

func gormLoadRelationApprovalBinding(ctx context.Context, tx *gorm.DB, approval changecontroldomain.Approval) (relationApprovalBinding, error) {
	var binding relationApprovalBinding
	var workspaceID, status, revisionHash string
	row, err := gormKnowledgeRawRow(ctx, tx, `
		SELECT p.workspace_id::text,p.status,p.version,r.change_hash
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.id=?::uuid
		WHERE p.id=?::uuid
		  AND (p.current_revision_id=r.id OR p.current_revision_id IS NULL)
		FOR UPDATE OF p,r`, string(approval.RevisionID), string(approval.ProposalID))
	if err != nil {
		return binding, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPROVAL_BINDING_QUERY_FAILED")
	}
	if err := row.Scan(&workspaceID, &status, &binding.proposalVersion, &revisionHash); gormKnowledgeNoRows(err) {
		return binding, relationApplyNotFound(err)
	} else if err != nil {
		return binding, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPROVAL_BINDING_QUERY_FAILED")
	}
	binding.workspaceID, binding.status, binding.revisionHash = foundation.ID(workspaceID), changecontroldomain.ProposalStatus(status), strings.ToLower(revisionHash)
	row, err = gormKnowledgeRawRow(ctx, tx, `
		SELECT id::text,proposal_id::text,revision_id::text,change_hash,decision,approved_git_head,decided_at
		FROM change_control.approval WHERE revision_id=?::uuid FOR UPDATE`, string(approval.RevisionID))
	if err != nil {
		return binding, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPROVAL_QUERY_FAILED")
	}
	var existingID, proposalID, revisionID, changeHash, decision string
	var gitHead *string
	var decidedAt time.Time
	if err := row.Scan(&existingID, &proposalID, &revisionID, &changeHash, &decision, &gitHead, &decidedAt); gormKnowledgeNoRows(err) {
		return binding, nil
	} else if err != nil {
		return binding, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPROVAL_QUERY_FAILED")
	}
	binding.existing = &changecontroldomain.Approval{ID: foundation.ID(existingID), ProposalID: foundation.ID(proposalID), RevisionID: foundation.ID(revisionID), ChangeHash: strings.ToLower(changeHash), Decision: changecontroldomain.Decision(decision), ApprovedGitHead: gitHead, DecidedAt: decidedAt}
	return binding, nil
}

func (repository *GORMApprovedRelationApplyRepository) persistApprovalScoped(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, requested changecontroldomain.Approval, binding relationApprovalBinding) (changecontroldomain.Approval, error) {
	if binding.existing != nil {
		existing := *binding.existing
		if existing.ProposalID != requested.ProposalID || existing.RevisionID != requested.RevisionID || !strings.EqualFold(existing.ChangeHash, requested.ChangeHash) || existing.Decision != requested.Decision || existing.ApprovedGitHead != nil {
			return changecontroldomain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_DECISION_CONFLICT", false, errors.New("proposal already has a different approval decision"))
		}
		if binding.status == changecontroldomain.StatusNeedsRevision {
			return existing, foundation.NewError(foundation.ErrorVersionConflict, "RELATION_PROPOSAL_BASE_STALE", false, errors.New("relation proposal requires revision"))
		}
		if binding.status != changecontroldomain.StatusApproved && binding.status != changecontroldomain.StatusApplied {
			return changecontroldomain.Approval{}, relationApplyConsistency(errors.New("persisted relation approval has an invalid proposal status"))
		}
		return existing, nil
	}
	if binding.status != changecontroldomain.StatusReady {
		return changecontroldomain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal is not ready for review"))
	}
	if !strings.EqualFold(binding.revisionHash, requested.ChangeHash) {
		return changecontroldomain.Approval{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CHANGE_HASH_MISMATCH", false, errors.New("change hash mismatch"))
	}
	if _, err := gormKnowledgeExec(ctx, tx, `
		INSERT INTO change_control.approval(id,proposal_id,revision_id,change_hash,decision,approved_git_head,decided_at)
		VALUES(?::uuid,?::uuid,?::uuid,?,?,?,?)`, string(requested.ID), string(requested.ProposalID), string(requested.RevisionID), strings.ToLower(requested.ChangeHash), string(requested.Decision), requested.ApprovedGitHead, requested.DecidedAt.UTC()); err != nil {
		return changecontroldomain.Approval{}, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPROVAL_CREATE_FAILED")
	}
	version, err := gormTransitionRelationApplyProposal(ctx, tx, requested.ProposalID, requested.RevisionID, binding.proposalVersion, changecontroldomain.StatusReady, changecontroldomain.StatusApproved, requested.DecidedAt)
	if err != nil {
		return changecontroldomain.Approval{}, err
	}
	if repository.events != nil {
		_, _, err = repository.events.AppendScoped(ctx, scope, changecontroleventcontract.ProposalStatusRequest(binding.workspaceID, requested.ProposalID, requested.ID, changecontroleventcontract.ProposalApprovedEventType, string(changecontroldomain.StatusApproved), version, requested.DecidedAt))
		if err != nil {
			return changecontroldomain.Approval{}, err
		}
	}
	requested.ChangeHash, requested.DecidedAt = strings.ToLower(requested.ChangeHash), requested.DecidedAt.UTC()
	return requested, nil
}

func gormLoadApprovedRelationProposal(ctx context.Context, tx *gorm.DB, command knowledgeapplication.ApprovedRelationApplyCommand) (approvedRelationProposal, error) {
	var proposal approvedRelationProposal
	var proposalID, workspaceID, proposalType, riskLevel, status, revisionID, revisionProposalID string
	var approvalID, approvalProposalID, approvalRevisionID, decision string
	var targetRefs, baseVersions, changeSet, evidenceRefs knowledgeJSONB
	var schemaVersion string
	var gitHead *string
	row, err := gormKnowledgeRawRow(ctx, tx, `
		SELECT p.id::text,p.workspace_id::text,p.proposal_type,p.risk_level,p.status,p.version,p.request_hash,p.updated_at,
		 r.id::text,r.proposal_id::text,r.revision_no,r.risk,r.rollback_plan,r.change_hash,
		 r.target_refs,r.base_versions,r.change_set,r.evidence_refs,r.schema_version,r.created_at,
		 a.id::text,a.proposal_id::text,a.revision_id::text,a.change_hash,a.decision,a.approved_git_head,a.decided_at
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.id=?::uuid
		JOIN change_control.approval a ON a.proposal_id=p.id AND a.revision_id=r.id AND a.id=?::uuid
		WHERE p.id=?::uuid AND p.workspace_id=?::uuid
		  AND (p.current_revision_id=r.id OR p.current_revision_id IS NULL)
		FOR UPDATE OF p,r,a`,
		string(command.RevisionID), string(command.ApprovalID), string(command.ProposalID), string(command.WorkspaceID))
	if err != nil {
		return proposal, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_BINDING_QUERY_FAILED")
	}
	err = row.Scan(&proposalID, &workspaceID, &proposalType, &riskLevel, &status, &proposal.Version, &proposal.RequestHash, &proposal.UpdatedAt,
		&revisionID, &revisionProposalID, &proposal.Revision.RevisionNo, &proposal.Revision.Risk, &proposal.Revision.RollbackPlan, &proposal.Revision.ChangeHash,
		&targetRefs, &baseVersions, &changeSet, &evidenceRefs, &schemaVersion, &proposal.Revision.CreatedAt,
		&approvalID, &approvalProposalID, &approvalRevisionID, &proposal.Approval.ChangeHash, &decision, &gitHead, &proposal.Approval.DecidedAt)
	if gormKnowledgeNoRows(err) {
		return proposal, relationApplyNotFound(err)
	}
	if err != nil {
		return proposal, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_BINDING_QUERY_FAILED")
	}
	proposal.ID, proposal.WorkspaceID, proposal.Type = foundation.ID(proposalID), foundation.ID(workspaceID), changecontroldomain.ProposalType(proposalType)
	var riskErr error
	proposal.RiskLevel, riskErr = changecontroldomain.ValidateProposalRiskLevelForType(proposal.Type, changecontroldomain.ProposalRiskLevel(riskLevel))
	if riskErr != nil {
		return proposal, relationApplyConsistency(riskErr)
	}
	proposal.Status = changecontroldomain.ProposalStatus(status)
	proposal.Revision.ID, proposal.Revision.ProposalID = foundation.ID(revisionID), foundation.ID(revisionProposalID)
	proposal.Approval.ID, proposal.Approval.ProposalID, proposal.Approval.RevisionID = foundation.ID(approvalID), foundation.ID(approvalProposalID), foundation.ID(approvalRevisionID)
	proposal.Approval.Decision, proposal.Approval.ApprovedGitHead = changecontroldomain.Decision(decision), gitHead
	proposal.change.SchemaVersion = schemaVersion
	if json.Unmarshal(targetRefs, &proposal.change.TargetRefs) != nil || json.Unmarshal(baseVersions, &proposal.change.BaseVersions) != nil || json.Unmarshal(changeSet, &proposal.change.ChangeSet) != nil || json.Unmarshal(evidenceRefs, &proposal.change.EvidenceRefs) != nil {
		return proposal, relationApplyConsistency(errors.New("knowledge relation revision payload cannot be decoded"))
	}
	canonical, validateErr := changecontroldomain.ValidateKnowledgeChange(proposal.change)
	if validateErr != nil || len(canonical.TargetRefs) != 1 {
		return proposal, relationApplyConsistency(errors.New("knowledge relation revision payload is invalid"))
	}
	proposal.change = canonical
	proposal.Revision.KnowledgeChange = &proposal.change
	if _, bindingErr := validateApprovedRelationProposalBinding(proposal, command); bindingErr != nil {
		return proposal, relationApplyConsistency(bindingErr)
	}
	return proposal, nil
}

func gormLoadRelationApplyCandidate(ctx context.Context, tx *gorm.DB, workspaceID, candidateID foundation.ID) (graphdomain.SemanticLinkCandidate, error) {
	var candidate graphdomain.SemanticLinkCandidate
	var id, persistedWorkspace, sourceType, sourceID, targetType, targetID, relationType, status, fingerprintSchema string
	var reopenedReason, reopenedFrom, proposalID *string
	var confidence *float64
	var discovery, hashes, generation knowledgeJSONB
	row, err := gormKnowledgeRawRow(ctx, tx, `
		SELECT id::text,workspace_id::text,source_node_type,source_node_id::text,source_node_version,
		 target_node_type,target_node_id::text,target_node_version,relation_type,status,reopened_reason,
		 reopened_from_candidate_id::text,current_proposal_id::text,confidence_score,reason,
		 source_summary,source_excerpt,target_summary,target_excerpt,discovery_methods,evidence_semantic_hashes,
		 generation,deferred_until,fingerprint_schema_version,fingerprint,version,created_at,updated_at
		FROM graph.semantic_link_candidate WHERE workspace_id=?::uuid AND id=?::uuid FOR UPDATE`, string(workspaceID), string(candidateID))
	if err != nil {
		return candidate, err
	}
	err = row.Scan(&id, &persistedWorkspace, &sourceType, &sourceID, &candidate.Source.Version, &targetType, &targetID, &candidate.Target.Version, &relationType, &status, &reopenedReason, &reopenedFrom, &proposalID, &confidence, &candidate.Reason, &candidate.Source.Summary, &candidate.Source.Excerpt, &candidate.Target.Summary, &candidate.Target.Excerpt, &discovery, &hashes, &generation, &candidate.ResumeAfter, &fingerprintSchema, &candidate.Fingerprint, &candidate.Version, &candidate.CreatedAt, &candidate.UpdatedAt)
	if err != nil {
		return candidate, err
	}
	candidate.ID, candidate.WorkspaceID = foundation.ID(id), foundation.ID(persistedWorkspace)
	candidate.Source.Ref, candidate.Target.Ref = domain.NodeRef{Type: domain.NodeType(sourceType), ID: foundation.ID(sourceID)}, domain.NodeRef{Type: domain.NodeType(targetType), ID: foundation.ID(targetID)}
	candidate.SuggestedRelationType, candidate.Status = domain.RelationType(relationType), graphdomain.SemanticLinkCandidateStatus(status)
	if confidence != nil {
		candidate.Confidence = *confidence
	}
	if reopenedReason != nil {
		candidate.ReopenedReason = graphdomain.SemanticLinkCandidateReopenedReason(*reopenedReason)
	}
	if reopenedFrom != nil {
		value := foundation.ID(*reopenedFrom)
		candidate.ReopenedFromCandidateID = &value
	}
	if proposalID != nil {
		value := foundation.ID(*proposalID)
		candidate.ProposalID = &value
	}
	var evidenceHashes []string
	if fingerprintSchema != candidateFingerprintSchemaV1 || json.Unmarshal(discovery, &candidate.DiscoveryMethods) != nil || json.Unmarshal(generation, &candidate.Generation) != nil || json.Unmarshal(hashes, &evidenceHashes) != nil {
		return candidate, baselineChanged("candidate stored payload is invalid")
	}
	rows, err := gormKnowledgeRawRows(ctx, tx, `
		SELECT id::text,source_version_id::text,source_span_id::text,semantic_hash,summary,excerpt
		FROM graph.semantic_link_candidate_evidence WHERE workspace_id=?::uuid AND candidate_id=?::uuid ORDER BY semantic_hash,id`, string(workspaceID), string(candidateID))
	if err != nil {
		return candidate, err
	}
	defer rows.Close()
	for rows.Next() {
		var evidence graphdomain.SemanticLinkCandidateEvidence
		var evidenceID, versionID, spanID string
		if err := rows.Scan(&evidenceID, &versionID, &spanID, &evidence.SemanticHash, &evidence.Reason, &evidence.Excerpt); err != nil {
			return candidate, err
		}
		evidence.ID, evidence.Provenance = foundation.ID(evidenceID), domain.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: foundation.ID(versionID), SourceSpanID: foundation.ID(spanID)}
		candidate.Evidence = append(candidate.Evidence, evidence)
	}
	if err := rows.Err(); err != nil {
		return candidate, err
	}
	actual := make([]string, len(candidate.Evidence))
	for index := range candidate.Evidence {
		actual[index] = candidate.Evidence[index].SemanticHash
	}
	sort.Strings(actual)
	sort.Strings(evidenceHashes)
	if !reflect.DeepEqual(actual, evidenceHashes) {
		return candidate, baselineChanged("candidate evidence hash projection is inconsistent")
	}
	if err := graphdomain.ValidateSemanticLinkCandidate(candidate); err != nil {
		return candidate, baselineChanged("candidate fingerprint or snapshot is invalid")
	}
	return candidate, nil
}

func gormLoadAndValidateRelationApplyEndpoints(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, baseVersions []changecontroldomain.KnowledgeBaseVersion) (domain.Applicability, error) {
	claims, topics := make([]foundation.ID, 0, 2), make([]foundation.ID, 0, 2)
	for _, base := range baseVersions {
		if base.NodeType == domain.NodeTypeClaim {
			claims = append(claims, base.NodeID)
		} else {
			topics = append(topics, base.NodeID)
		}
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i] < claims[j] })
	sort.Slice(topics, func(i, j int) bool { return topics[i] < topics[j] })
	snapshots := make(map[string]relationApplyEndpoint, len(baseVersions))
	if err := gormLoadRelationApplyClaims(ctx, tx, workspaceID, claims, snapshots); err != nil {
		return domain.Applicability{}, err
	}
	if err := gormLoadRelationApplyTopics(ctx, tx, workspaceID, topics, snapshots); err != nil {
		return domain.Applicability{}, err
	}
	if len(snapshots) != len(baseVersions) {
		return domain.Applicability{}, baselineChanged("relation endpoint is missing or cross-workspace")
	}
	var derived *domain.Applicability
	for _, base := range baseVersions {
		ref := domain.NodeRef{Type: base.NodeType, ID: base.NodeID}
		snapshot, ok := snapshots[relationApplyNodeKey(ref)]
		if !ok || snapshot.Version != base.Version || !relationApplyLifecycleActive(ref.Type, snapshot.Lifecycle) {
			return domain.Applicability{}, baselineChanged("relation endpoint version or lifecycle changed")
		}
		if snapshot.Applicability == nil {
			continue
		}
		if derived == nil {
			value := *snapshot.Applicability
			derived = &value
			continue
		}
		if derived.Hash != snapshot.Applicability.Hash {
			return domain.Applicability{}, baselineChanged("relation endpoint applicability differs and requires a new revision")
		}
	}
	if derived != nil {
		return *derived, nil
	}
	return domain.ParseApplicability(json.RawMessage(`{}`))
}

func gormLoadRelationApplyClaims(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, ids []foundation.ID, snapshots map[string]relationApplyEndpoint) error {
	if len(ids) == 0 {
		return nil
	}
	values := make([]string, len(ids))
	for i := range ids {
		values[i] = string(ids[i])
	}
	rows, err := gormKnowledgeRawRows(ctx, tx, `
		SELECT id::text,version,status,applicability,applicability_schema_version,applicability_hash
		FROM core.claim WHERE workspace_id=?::uuid AND id=ANY(?::uuid[]) ORDER BY id FOR SHARE`, string(workspaceID), pq.Array(values))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, lifecycle, schemaVersion, hash string
		var version int64
		var raw knowledgeJSONB
		if err := rows.Scan(&id, &version, &lifecycle, &raw, &schemaVersion, &hash); err != nil {
			return err
		}
		applicability, err := storedApplicability(raw, schemaVersion, hash)
		if err != nil {
			return baselineChanged("relation endpoint applicability snapshot is invalid")
		}
		ref := domain.NodeRef{Type: domain.NodeTypeClaim, ID: foundation.ID(id)}
		snapshots[relationApplyNodeKey(ref)] = relationApplyEndpoint{Ref: ref, Version: version, Lifecycle: lifecycle, Applicability: &applicability}
	}
	return rows.Err()
}

func gormLoadRelationApplyTopics(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, ids []foundation.ID, snapshots map[string]relationApplyEndpoint) error {
	if len(ids) == 0 {
		return nil
	}
	values := make([]string, len(ids))
	for i := range ids {
		values[i] = string(ids[i])
	}
	rows, err := gormKnowledgeRawRows(ctx, tx, `
		SELECT id::text,version,status FROM core.topic
		WHERE workspace_id=?::uuid AND id=ANY(?::uuid[]) ORDER BY id FOR SHARE`, string(workspaceID), pq.Array(values))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, lifecycle string
		var version int64
		if err := rows.Scan(&id, &version, &lifecycle); err != nil {
			return err
		}
		ref := domain.NodeRef{Type: domain.NodeTypeTopic, ID: foundation.ID(id)}
		snapshots[relationApplyNodeKey(ref)] = relationApplyEndpoint{Ref: ref, Version: version, Lifecycle: lifecycle}
	}
	return rows.Err()
}

func gormValidateRelationApplyProvenance(ctx context.Context, tx *gorm.DB, candidate graphdomain.SemanticLinkCandidate) error {
	row, err := gormKnowledgeRawRow(ctx, tx, `
		SELECT count(*)::int,COALESCE(bool_and(core.knowledge_validate_provenance_binding(e.workspace_id,e.source_version_id,e.source_span_id)),false)
		FROM graph.semantic_link_candidate_evidence e WHERE e.workspace_id=?::uuid AND e.candidate_id=?::uuid`, string(candidate.WorkspaceID), string(candidate.ID))
	if err != nil {
		return err
	}
	var count int
	var reachable bool
	if err := row.Scan(&count, &reachable); err != nil {
		return err
	}
	if count != len(candidate.Evidence) || !reachable {
		return baselineChanged("candidate evidence provenance is no longer reachable")
	}
	return nil
}

func gormLockRelationApplyReceipt(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, idempotencyKey string) error {
	row, err := gormKnowledgeRawRow(ctx, tx, `SELECT id::text FROM core.workspace WHERE id=?::uuid FOR KEY SHARE`, string(workspaceID))
	if err != nil {
		return classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_WORKSPACE_QUERY_FAILED")
	}
	var id string
	if err := row.Scan(&id); gormKnowledgeNoRows(err) {
		return relationApplyNotFound(err)
	} else if err != nil {
		return classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_WORKSPACE_QUERY_FAILED")
	}
	if _, err := gormKnowledgeExec(ctx, tx, `SELECT pg_advisory_xact_lock(hashtextextended(? || chr(31) || ?, 0))`, string(workspaceID), idempotencyKey); err != nil {
		return classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_RECEIPT_LOCK_FAILED")
	}
	return nil
}

func gormLoadRelationApplyReceipt(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, key string) (*commandReceipt, error) {
	row, err := gormKnowledgeRawRow(ctx, tx, `
		SELECT request_hash,command_type,aggregate_type,aggregate_id::text,aggregate_version
		FROM core.knowledge_command_receipt WHERE workspace_id=?::uuid AND idempotency_key=?`, string(workspaceID), key)
	if err != nil {
		return nil, err
	}
	var receipt commandReceipt
	var commandType, aggregateType, aggregateID string
	if err := row.Scan(&receipt.RequestHash, &commandType, &aggregateType, &aggregateID, &receipt.AggregateVersion); gormKnowledgeNoRows(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	receipt.CommandType, receipt.AggregateType, receipt.AggregateID = domain.CommandType(commandType), domain.AggregateType(aggregateType), foundation.ID(aggregateID)
	return &receipt, nil
}

func gormInsertRelationApplyReceipt(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, key, requestHash string, relation domain.Relation, at time.Time) error {
	_, err := gormKnowledgeExec(ctx, tx, `
		INSERT INTO core.knowledge_command_receipt(workspace_id,idempotency_key,request_hash,command_type,aggregate_type,aggregate_id,aggregate_version,created_at)
		VALUES(?::uuid,?,?,?,?,?::uuid,?,?)`, string(workspaceID), key, requestHash, string(commandConfirmRelation), string(aggregateRelation), string(relation.ID), relation.Version, at.UTC())
	return err
}

func (repository *GORMApprovedRelationApplyRepository) applyScoped(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, command knowledgeapplication.ApprovedRelationApplyCommand, idempotencyKey, requestHash string) (knowledgeapplication.ApprovedRelationApplyResult, error) {
	proposal, err := gormLoadApprovedRelationProposal(ctx, tx, command)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if err := gormLockRelationApplyReceipt(ctx, tx, command.WorkspaceID, idempotencyKey); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	receipt, err := gormLoadRelationApplyReceipt(ctx, tx, command.WorkspaceID, idempotencyKey)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_RECEIPT_QUERY_FAILED")
	}
	if receipt != nil {
		return repository.replayScoped(ctx, scope, tx, command, requestHash, proposal, receipt)
	}
	if proposal.Status != changecontroldomain.StatusApproved {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyApprovalRequired(errors.New("knowledge relation proposal is not approved"))
	}
	candidate, err := gormLoadRelationApplyCandidate(ctx, tx, command.WorkspaceID, proposal.change.TargetRefs[0].ID)
	if gormKnowledgeNoRows(err) {
		return repository.markNeedsRevisionScoped(ctx, scope, tx, proposal, "relation candidate is missing")
	}
	if err != nil {
		var baseline *relationApplyBaselineError
		if errors.As(err, &baseline) {
			return repository.markNeedsRevisionScoped(ctx, scope, tx, proposal, baseline.Error())
		}
		return knowledgeapplication.ApprovedRelationApplyResult{}, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_CANDIDATE_QUERY_FAILED")
	}
	if err := validateRelationApplyBinding(candidate, proposal); err != nil {
		return repository.markNeedsRevisionScoped(ctx, scope, tx, proposal, err.Error())
	}
	applicability, err := gormLoadAndValidateRelationApplyEndpoints(ctx, tx, command.WorkspaceID, proposal.change.BaseVersions)
	if err != nil {
		var baseline *relationApplyBaselineError
		if errors.As(err, &baseline) {
			return repository.markNeedsRevisionScoped(ctx, scope, tx, proposal, baseline.Error())
		}
		return knowledgeapplication.ApprovedRelationApplyResult{}, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_ENDPOINT_QUERY_FAILED")
	}
	if err := gormValidateRelationApplyProvenance(ctx, tx, candidate); err != nil {
		var baseline *relationApplyBaselineError
		if errors.As(err, &baseline) {
			return repository.markNeedsRevisionScoped(ctx, scope, tx, proposal, baseline.Error())
		}
		return knowledgeapplication.ApprovedRelationApplyResult{}, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_PROVENANCE_QUERY_FAILED")
	}
	fingerprint := domain.ComputeRelationFingerprint(command.WorkspaceID, proposal.change.ChangeSet.RelationType, proposal.change.ChangeSet.Source, proposal.change.ChangeSet.Target)
	if fingerprint == "" {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(errors.New("relation fingerprint cannot be computed"))
	}
	if _, err := gormKnowledgeExec(ctx, tx, `SELECT pg_advisory_xact_lock(hashtextextended(? || chr(31) || ?, 0))`, string(command.WorkspaceID), fingerprint); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_RELATION_LOCK_FAILED")
	}
	existing, err := gormLoadRelationByFingerprint(ctx, tx, command.WorkspaceID, fingerprint)
	var existingResult *domain.RelationResult
	if err == nil {
		if reuseErr := validateReusableSuggestedRelation(existing, command.WorkspaceID, proposal.change.ChangeSet, fingerprint); reuseErr != nil {
			return repository.markNeedsRevisionScoped(ctx, scope, tx, proposal, "canonical relation already exists in a non-reusable state")
		}
		existingResult = &existing
	} else if !gormKnowledgeNoRows(err) {
		return knowledgeapplication.ApprovedRelationApplyResult{}, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_RELATION_QUERY_FAILED")
	}
	atValues := []time.Time{repository.clock.Now().UTC(), proposal.UpdatedAt, proposal.Approval.DecidedAt, candidate.UpdatedAt}
	if existingResult != nil {
		atValues = append(atValues, existingResult.Relation.UpdatedAt)
	}
	at := latestRelationApplyTime(atValues...)
	if at.IsZero() {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyUnavailable(errors.New("relation apply clock returned zero time"))
	}
	applyingVersion, err := gormTransitionRelationApplyProposal(ctx, tx, proposal.ID, proposal.Revision.ID, proposal.Version, changecontroldomain.StatusApproved, changecontroldomain.StatusApplying, at)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	confirmation := domain.Confirmation{Method: domain.ConfirmationUserApproval, Reference: string(command.ApprovalID)}
	relationID := foundation.ID("")
	if existingResult != nil {
		relationID = existingResult.Relation.ID
	} else if relationID, err = repository.ids.New(); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	evidence, err := repository.buildEvidence(candidate, relationID, confirmation, applicability, at)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	allEvidence := evidence
	if existingResult != nil {
		allEvidence = append(append([]domain.RelationEvidence(nil), existingResult.Evidence...), evidence...)
	} else {
		confidence := candidate.Confidence
		relation := domain.Relation{ID: relationID, WorkspaceID: command.WorkspaceID, Source: proposal.change.ChangeSet.Source, Target: proposal.change.ChangeSet.Target, Type: proposal.change.ChangeSet.RelationType, Status: domain.RelationStatusSuggested, ConfidenceScore: &confidence, Fingerprint: fingerprint, EvidenceFingerprint: domain.ComputeRelationEvidenceFingerprint(allEvidence), Version: 1, CreatedAt: at, UpdatedAt: at}
		if err := domain.ValidateRelationAggregate(relation, allEvidence); err != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(err)
		}
		inserted, insertErr := gormInsertRelation(ctx, tx, relation)
		if gormKnowledgeNoRows(insertErr) {
			winner, loadErr := gormLoadRelationByFingerprint(ctx, tx, command.WorkspaceID, fingerprint)
			if loadErr != nil {
				return knowledgeapplication.ApprovedRelationApplyResult{}, classifyGORMRelationApply(ctx, loadErr, "RELATION_PROPOSAL_APPLY_RELATION_QUERY_FAILED")
			}
			if reuseErr := validateReusableSuggestedRelation(winner, command.WorkspaceID, proposal.change.ChangeSet, fingerprint); reuseErr != nil {
				return repository.markNeedsRevisionAtVersionScoped(ctx, scope, tx, proposal, applyingVersion, changecontroldomain.StatusApplying, "canonical relation was created concurrently in a non-reusable state")
			}
			existingResult, relationID, at = &winner, winner.Relation.ID, latestRelationApplyTime(at, winner.Relation.UpdatedAt)
			evidence, err = repository.buildEvidence(candidate, relationID, confirmation, applicability, at)
			if err != nil {
				return knowledgeapplication.ApprovedRelationApplyResult{}, err
			}
			allEvidence = append(append([]domain.RelationEvidence(nil), winner.Evidence...), evidence...)
		} else if insertErr != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, classifyGORMRelationApply(ctx, insertErr, "RELATION_PROPOSAL_APPLY_RELATION_CREATE_FAILED")
		} else {
			relationID = inserted.ID
		}
	}
	for _, item := range evidence {
		if err := gormInsertRelationEvidence(ctx, tx, item); err != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_EVIDENCE_CREATE_FAILED")
		}
	}
	confirmed, err := gormConfirmRelation(ctx, tx, command.WorkspaceID, relationID, relationVersionBeforeConfirm(existingResult), confirmation, domain.ComputeRelationEvidenceFingerprint(allEvidence), at)
	if gormKnowledgeNoRows(err) {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(errors.New("relation confirmation CAS was lost"))
	}
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_RELATION_CONFIRM_FAILED")
	}
	if err := domain.ValidateRelationAggregate(confirmed, allEvidence); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(err)
	}
	if err := gormInsertRelationApplyReceipt(ctx, tx, command.WorkspaceID, idempotencyKey, requestHash, confirmed, at); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_RECEIPT_CREATE_FAILED")
	}
	appliedVersion, err := gormTransitionRelationApplyProposal(ctx, tx, proposal.ID, proposal.Revision.ID, applyingVersion, changecontroldomain.StatusApplying, changecontroldomain.StatusApplied, at)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if repository.events != nil {
		if _, _, err := repository.events.AppendScoped(ctx, scope, changecontroleventcontract.ProposalStatusRequest(command.WorkspaceID, proposal.ID, command.ApprovalID, changecontroleventcontract.ProposalAppliedEventType, string(changecontroldomain.StatusApplied), appliedVersion, at)); err != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, err
		}
	}
	return knowledgeapplication.ApprovedRelationApplyResult{Relation: confirmed, Evidence: allEvidence, ProposalStatus: changecontroldomain.StatusApplied, ProposalVersion: appliedVersion}, nil
}

func (repository *GORMApprovedRelationApplyRepository) buildEvidence(candidate graphdomain.SemanticLinkCandidate, relationID foundation.ID, confirmation domain.Confirmation, applicability domain.Applicability, at time.Time) ([]domain.RelationEvidence, error) {
	result := make([]domain.RelationEvidence, len(candidate.Evidence))
	var modelRunRef *string
	if candidate.Generation.ModelRunID != nil {
		value := string(*candidate.Generation.ModelRunID)
		modelRunRef = &value
	}
	for index, source := range candidate.Evidence {
		id, err := repository.ids.New()
		if err != nil {
			return nil, err
		}
		value := domain.RelationEvidence{ID: id, WorkspaceID: candidate.WorkspaceID, RelationID: relationID, Provenance: source.Provenance, Reason: source.Reason, Applicability: applicability, ModelRunRef: modelRunRef, Confirmation: &confirmation, CreatedAt: at}
		value.EvidenceHash = domain.ComputeRelationEvidenceHash(value)
		if err := domain.ValidateRelationEvidence(value); err != nil {
			return nil, relationApplyConsistency(err)
		}
		result[index] = value
	}
	return result, nil
}

func gormLoadRelationResult(ctx context.Context, tx *gorm.DB, workspaceID, relationID foundation.ID, forUpdate bool) (domain.RelationResult, error) {
	query := relationSelect + ` WHERE workspace_id=?::uuid AND id=?::uuid`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormKnowledgeRawRow(ctx, tx, query, string(workspaceID), string(relationID))
	if err != nil {
		return domain.RelationResult{}, err
	}
	relation, err := scanRelation(row)
	if err != nil {
		return domain.RelationResult{}, err
	}
	evidence, err := gormLoadRelationEvidence(ctx, tx, workspaceID, relation.ID)
	if err != nil {
		return domain.RelationResult{}, err
	}
	if err := domain.ValidateRelationAggregate(relation, evidence); err != nil {
		return domain.RelationResult{}, consistency(domain.ErrorCodeRelationInvalid, err)
	}
	return domain.RelationResult{Relation: relation, Evidence: evidence}, nil
}

func gormLoadRelationByFingerprint(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, fingerprint string) (domain.RelationResult, error) {
	row, err := gormKnowledgeRawRow(ctx, tx, relationSelect+` WHERE workspace_id=?::uuid AND fingerprint=? FOR UPDATE`, string(workspaceID), fingerprint)
	if err != nil {
		return domain.RelationResult{}, err
	}
	relation, err := scanRelation(row)
	if err != nil {
		return domain.RelationResult{}, err
	}
	evidence, err := gormLoadRelationEvidence(ctx, tx, workspaceID, relation.ID)
	if err != nil {
		return domain.RelationResult{}, err
	}
	if err := domain.ValidateRelationAggregate(relation, evidence); err != nil {
		return domain.RelationResult{}, consistency(domain.ErrorCodeRelationInvalid, err)
	}
	return domain.RelationResult{Relation: relation, Evidence: evidence}, nil
}

func gormLoadRelationEvidence(ctx context.Context, tx *gorm.DB, workspaceID, relationID foundation.ID) ([]domain.RelationEvidence, error) {
	rows, err := gormKnowledgeRawRows(ctx, tx, `
		SELECT id::text,workspace_id::text,relation_id::text,source_version_id::text,source_span_id::text,reason,
		 applicability,applicability_schema_version,applicability_hash,evidence_hash,model_run_ref,confirmation_method,confirmed_by,created_at
		FROM core.relation_evidence WHERE workspace_id=?::uuid AND relation_id=?::uuid ORDER BY created_at,id`, string(workspaceID), string(relationID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.RelationEvidence, 0)
	for rows.Next() {
		value, scanErr := scanRelationEvidence(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func gormInsertRelation(ctx context.Context, tx *gorm.DB, relation domain.Relation) (domain.Relation, error) {
	confirmationMethod, confirmationRef := confirmationValues(relation.Confirmation)
	row, err := gormKnowledgeRawRow(ctx, tx, `
		INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,valid_from,valid_to,version,created_at,updated_at)
		VALUES(?::uuid,?::uuid,?,?::uuid,?,?::uuid,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(workspace_id,fingerprint) DO NOTHING
		RETURNING id::text,workspace_id::text,source_node_type,source_node_id::text,target_node_type,target_node_id::text,relation_type,status,confirmation_method,confirmation_ref,confidence_score,valid_from,valid_to,fingerprint,evidence_fingerprint,version,created_at,updated_at`,
		string(relation.ID), string(relation.WorkspaceID), string(relation.Source.Type), string(relation.Source.ID), string(relation.Target.Type), string(relation.Target.ID), string(relation.Type), string(relation.Status), relation.ConfidenceScore, relation.Fingerprint, nullableEvidenceFingerprint(relation.EvidenceFingerprint), confirmationMethod, confirmationRef, timePointer(relation.ValidFrom), timePointer(relation.ValidTo), relation.Version, relation.CreatedAt.UTC(), relation.UpdatedAt.UTC())
	if err != nil {
		return domain.Relation{}, err
	}
	return scanRelation(row)
}

func gormInsertRelationEvidence(ctx context.Context, tx *gorm.DB, evidence domain.RelationEvidence) error {
	var method, by any
	if evidence.Confirmation != nil {
		method, by = string(evidence.Confirmation.Method), evidence.Confirmation.Reference
	}
	_, err := gormKnowledgeExec(ctx, tx, `
		INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,model_run_ref,confirmation_method,confirmed_by,created_at)
		VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?::uuid,?,?,?::jsonb,?,?,?,?,?,?)`,
		string(evidence.ID), string(evidence.WorkspaceID), string(evidence.RelationID), string(evidence.Provenance.SourceVersionID), string(evidence.Provenance.SourceSpanID), evidence.Reason, evidence.EvidenceHash, knowledgeJSONB(evidence.Applicability.CanonicalJSON), evidence.Applicability.SchemaVersion, evidence.Applicability.Hash, pointerString(evidence.ModelRunRef), method, by, evidence.CreatedAt.UTC())
	return err
}

func gormConfirmRelation(ctx context.Context, tx *gorm.DB, workspaceID, relationID foundation.ID, expectedVersion int64, confirmation domain.Confirmation, evidenceFingerprint string, at time.Time) (domain.Relation, error) {
	row, err := gormKnowledgeRawRow(ctx, tx, `
		UPDATE core.relation SET status=?,confirmation_method=?,confirmation_ref=?,evidence_fingerprint=?,version=version+1,updated_at=?
		WHERE workspace_id=?::uuid AND id=?::uuid AND version=? AND status=?
		RETURNING id::text,workspace_id::text,source_node_type,source_node_id::text,target_node_type,target_node_id::text,relation_type,status,confirmation_method,confirmation_ref,confidence_score,valid_from,valid_to,fingerprint,evidence_fingerprint,version,created_at,updated_at`,
		string(domain.RelationStatusConfirmed), string(confirmation.Method), confirmation.Reference, evidenceFingerprint, at.UTC(), string(workspaceID), string(relationID), expectedVersion, string(domain.RelationStatusSuggested))
	if err != nil {
		return domain.Relation{}, err
	}
	return scanRelation(row)
}

func (repository *GORMApprovedRelationApplyRepository) replayScoped(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, command knowledgeapplication.ApprovedRelationApplyCommand, requestHash string, proposal approvedRelationProposal, receipt *commandReceipt) (knowledgeapplication.ApprovedRelationApplyResult, error) {
	if receipt.RequestHash != requestHash || receipt.CommandType != commandConfirmRelation || receipt.AggregateType != aggregateRelation {
		return knowledgeapplication.ApprovedRelationApplyResult{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeIdempotencyConflict, false, errors.New("relation apply idempotency binding differs"))
	}
	result, err := gormLoadRelationResult(ctx, tx, command.WorkspaceID, receipt.AggregateID, false)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(err)
	}
	if err := validateRelationApplyReplayFact(command, proposal, receipt, result.Relation); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(err)
	}
	if repository.events != nil {
		if _, _, err := repository.events.AppendScoped(ctx, scope, changecontroleventcontract.ProposalStatusRequest(command.WorkspaceID, proposal.ID, command.ApprovalID, changecontroleventcontract.ProposalAppliedEventType, string(changecontroldomain.StatusApplied), proposal.Version, proposal.UpdatedAt)); err != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, err
		}
	}
	return knowledgeapplication.ApprovedRelationApplyResult{Relation: result.Relation, Evidence: result.Evidence, ProposalStatus: proposal.Status, ProposalVersion: proposal.Version, Replayed: true}, nil
}

func gormTransitionRelationApplyProposal(ctx context.Context, tx *gorm.DB, proposalID, revisionID foundation.ID, expectedVersion int64, from, to changecontroldomain.ProposalStatus, at time.Time) (int64, error) {
	if err := changecontroldomain.ValidateProposalTransition(from, to); err != nil {
		return 0, relationApplyConsistency(err)
	}
	row, err := gormKnowledgeRawRow(ctx, tx, `
		UPDATE change_control.proposal SET status=?,updated_at=?,version=version+1
		WHERE id=?::uuid AND status=? AND version=?
		  AND (current_revision_id=?::uuid OR current_revision_id IS NULL)
		RETURNING version`, string(to), at.UTC(), string(proposalID), string(from), expectedVersion, string(revisionID))
	if err != nil {
		return 0, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_STATE_UPDATE_FAILED")
	}
	var version int64
	if err := row.Scan(&version); gormKnowledgeNoRows(err) {
		return 0, foundation.NewError(foundation.ErrorVersionConflict, "RELATION_PROPOSAL_APPLY_STATE_CONFLICT", false, err)
	} else if err != nil {
		return 0, classifyGORMRelationApply(ctx, err, "RELATION_PROPOSAL_APPLY_STATE_UPDATE_FAILED")
	}
	return version, nil
}

func (repository *GORMApprovedRelationApplyRepository) markNeedsRevisionScoped(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, proposal approvedRelationProposal, reason string) (knowledgeapplication.ApprovedRelationApplyResult, error) {
	return repository.markNeedsRevisionAtVersionScoped(ctx, scope, tx, proposal, proposal.Version, proposal.Status, reason)
}

func (repository *GORMApprovedRelationApplyRepository) markNeedsRevisionAtVersionScoped(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, proposal approvedRelationProposal, version int64, status changecontroldomain.ProposalStatus, reason string) (knowledgeapplication.ApprovedRelationApplyResult, error) {
	at := latestRelationApplyTime(repository.clock.Now().UTC(), proposal.UpdatedAt, proposal.Approval.DecidedAt)
	newVersion, err := gormTransitionRelationApplyProposal(ctx, tx, proposal.ID, proposal.Revision.ID, version, status, changecontroldomain.StatusNeedsRevision, at)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if repository.events != nil {
		if _, _, err := repository.events.AppendScoped(ctx, scope, changecontroleventcontract.ProposalStatusRequest(proposal.WorkspaceID, proposal.ID, proposal.Approval.ID, changecontroleventcontract.ProposalNeedsRevisionEventType, string(changecontroldomain.StatusNeedsRevision), newVersion, at)); err != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, err
		}
	}
	return knowledgeapplication.ApprovedRelationApplyResult{}, &relationApplyNeedsRevision{reason: reason}
}
