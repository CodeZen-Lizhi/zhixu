package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/graph/candidateconfirm"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// CandidateConfirmDB is deliberately narrower than the Graph read repository.
// The Confirm adapter owns one write transaction across Graph and Change
// Control, so callers cannot accidentally split the operation.
type CandidateConfirmDB interface {
	Begin(context.Context) (pgx.Tx, error)
}

// CandidateConfirmRepository is the PostgreSQL adapter for the Candidate
// confirmation seam. It never writes core.relation and never dispatches a
// knowledge_change Proposal to the file Safe Writeback workflow.
type CandidateConfirmRepository struct {
	db    CandidateConfirmDB
	ids   foundation.IDGenerator
	clock foundation.Clock
}

var _ candidateconfirm.Port = (*CandidateConfirmRepository)(nil)

// NewCandidateConfirmRepository constructs the cross-schema Candidate Confirm
// adapter with explicit identity and clock dependencies.
func NewCandidateConfirmRepository(db CandidateConfirmDB, ids foundation.IDGenerator, clock foundation.Clock) (*CandidateConfirmRepository, error) {
	if isNilCandidateConfirmDependency(db) || isNilCandidateConfirmDependency(ids) || isNilCandidateConfirmDependency(clock) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "RELATION_PROPOSAL_CONFIRM_DEPENDENCY_MISSING", false, errors.New("candidate confirm dependency is missing"))
	}
	return &CandidateConfirmRepository{db: db, ids: ids, clock: clock}, nil
}

// Confirm atomically creates (or exactly replays) a typed Relation Proposal,
// its Candidate Decision receipt and the Candidate PROPOSAL_CREATED state.
func (repository *CandidateConfirmRepository) Confirm(ctx context.Context, command candidateconfirm.Command) (candidateconfirm.Result, error) {
	if repository == nil || isNilCandidateConfirmDependency(repository.db) {
		return candidateconfirm.Result{}, candidateConfirmUnavailable(errors.New("candidate confirm repository is unavailable"))
	}
	canonical, err := candidateconfirm.Canonicalize(command)
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	requestHash, err := candidateconfirm.RequestHash(canonical)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmInvalid(err)
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmClassify(err, "RELATION_PROPOSAL_CONFIRM_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	result, err := repository.confirmTx(ctx, tx, canonical, requestHash)
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return candidateconfirm.Result{}, candidateConfirmClassify(err, "RELATION_PROPOSAL_CONFIRM_COMMIT_FAILED")
	}
	return result, nil
}

func (repository *CandidateConfirmRepository) confirmTx(ctx context.Context, tx pgx.Tx, command candidateconfirm.Command, requestHash string) (candidateconfirm.Result, error) {
	receipt, found, err := loadDecisionByIdempotency(ctx, tx, command.WorkspaceID, command.IdempotencyKey)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmClassify(err, "RELATION_PROPOSAL_CONFIRM_RECEIPT_QUERY_FAILED")
	}
	if found {
		return repository.replayConfirmTx(ctx, tx, command, requestHash, receipt)
	}

	base, err := loadCandidate(ctx, tx, command.WorkspaceID, command.CandidateID, "id", true)
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	if base.candidate.Version != command.ExpectedVersion {
		// A concurrent Confirm can win the row lock after the initial receipt
		// lookup. Resolve the receipt before returning a stale-version error.
		if winner, winnerFound, receiptErr := loadDecisionByIdempotency(ctx, tx, command.WorkspaceID, command.IdempotencyKey); receiptErr != nil {
			return candidateconfirm.Result{}, candidateConfirmClassify(receiptErr, "RELATION_PROPOSAL_CONFIRM_RECEIPT_QUERY_FAILED")
		} else if winnerFound {
			return repository.replayConfirmTx(ctx, tx, command, requestHash, winner)
		}
		return candidateconfirm.Result{}, candidateConfirmVersionConflict("candidate expected version does not match")
	}

	decision := graphdomain.SemanticLinkCandidateDecision{Action: command.Action, RelationType: command.RelationType}
	if err := graphdomain.ValidateSemanticLinkCandidateDecision(base.candidate, decision); err != nil {
		return candidateconfirm.Result{}, err
	}
	effectiveRelationType := base.candidate.SuggestedRelationType
	if command.RelationType != nil {
		effectiveRelationType = *command.RelationType
	}
	canonicalSource, canonicalTarget, err := knowledge.CanonicalizeRelationEndpoints(effectiveRelationType, base.candidate.Source.Ref, base.candidate.Target.Ref)
	if err != nil || canonicalSource != base.candidate.Source.Ref || canonicalTarget != base.candidate.Target.Ref {
		return candidateconfirm.Result{}, candidateConfirmInvalid(errors.New("confirmation relation type is incompatible with candidate endpoint order"))
	}

	change, err := buildKnowledgeChange(base.candidate, effectiveRelationType)
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	changeHash, err := changecontroldomain.ComputeKnowledgeChangeHash(change, command.Risk, command.RollbackPlan)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmInvalid(err)
	}
	proposalRequestHash, err := changecontroldomain.ComputeKnowledgeChangeRequestHash(command.WorkspaceID, change, command.Risk, command.RollbackPlan)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmInvalid(err)
	}
	proposalKey := "semantic-link-confirm/v1:" + requestHash
	now := repository.clock.Now().UTC()
	if now.IsZero() {
		return candidateconfirm.Result{}, candidateConfirmUnavailable(errors.New("candidate confirm clock returned zero time"))
	}
	if now.Before(base.candidate.UpdatedAt) {
		now = base.candidate.UpdatedAt
	}
	proposalID, err := repository.ids.New()
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	revisionID, err := repository.ids.New()
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	decisionID, err := repository.ids.New()
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	proposal := changecontroldomain.Proposal{
		ID: proposalID, WorkspaceID: command.WorkspaceID, Type: changecontroldomain.ProposalTypeKnowledgeChange,
		IdempotencyKey: proposalKey, RequestHash: proposalRequestHash, Status: changecontroldomain.StatusReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: changecontroldomain.Revision{
			ID: revisionID, ProposalID: proposalID, RevisionNo: 1, Risk: command.Risk,
			RollbackPlan: command.RollbackPlan, ChangeHash: changeHash, KnowledgeChange: &change, CreatedAt: now,
		},
	}
	if err := changecontroldomain.ValidateProposalRevisionForType(changecontroldomain.ProposalTypeKnowledgeChange, proposal.Revision); err != nil {
		return candidateconfirm.Result{}, candidateConfirmInvalid(err)
	}

	persistedProposal, _, err := insertOrLoadKnowledgeProposal(ctx, tx, proposal)
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	proposal = persistedProposal
	proposalPointer := proposal.ID
	receipt = candidateDecisionRow{
		id: decisionID, workspaceID: command.WorkspaceID, candidateID: command.CandidateID,
		candidateVersion: command.ExpectedVersion, proposalID: &proposalPointer,
		idempotencyKey: command.IdempotencyKey, requestHash: requestHash,
		action: command.Action, relationType: command.RelationType, createdAt: now,
	}
	inserted, err := insertDecision(ctx, tx, receipt)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmClassify(err, "RELATION_PROPOSAL_CONFIRM_DECISION_CREATE_FAILED")
	}
	if !inserted {
		winner, winnerFound, lookupErr := loadDecisionByIdempotency(ctx, tx, command.WorkspaceID, command.IdempotencyKey)
		if lookupErr != nil {
			return candidateconfirm.Result{}, candidateConfirmClassify(lookupErr, "RELATION_PROPOSAL_CONFIRM_RECEIPT_QUERY_FAILED")
		}
		if !winnerFound {
			return candidateconfirm.Result{}, candidateConfirmConsistency(errors.New("candidate confirmation receipt disappeared"))
		}
		return repository.replayConfirmTx(ctx, tx, command, requestHash, winner)
	}

	updated, err := tx.Exec(ctx, `
		UPDATE graph.semantic_link_candidate
		SET status='PROPOSAL_CREATED',current_proposal_id=$3,deferred_until=NULL,version=version+1,updated_at=$4
		WHERE workspace_id=$1 AND id=$2 AND version=$5 AND current_proposal_id IS NULL`,
		string(command.WorkspaceID), string(command.CandidateID), string(proposal.ID), now, command.ExpectedVersion)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmClassify(err, "RELATION_PROPOSAL_CONFIRM_CANDIDATE_UPDATE_FAILED")
	}
	if updated.RowsAffected() != 1 {
		return candidateconfirm.Result{}, candidateConfirmVersionConflict("candidate confirmation CAS was lost")
	}
	resultCandidate, err := candidateAtDecision(base.candidate, receipt)
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	resultCandidate.Status = graphdomain.SemanticLinkCandidateStatusProposalCreated
	resultCandidate.Version = command.ExpectedVersion + 1
	resultCandidate.UpdatedAt = now
	resultCandidate.ProposalID = &proposalPointer
	if err := graphdomain.ValidateSemanticLinkCandidate(resultCandidate); err != nil {
		return candidateconfirm.Result{}, err
	}
	if err := validateCandidateConfirmProposalBinding(base.candidate, command, requestHash, proposal); err != nil {
		return candidateconfirm.Result{}, err
	}
	return candidateconfirm.Result{Candidate: resultCandidate, Proposal: proposal, DecisionID: decisionID}, nil
}

func (repository *CandidateConfirmRepository) replayConfirmTx(ctx context.Context, tx pgx.Tx, command candidateconfirm.Command, requestHash string, receipt candidateDecisionRow) (candidateconfirm.Result, error) {
	if receipt.requestHash != requestHash || receipt.candidateID != command.CandidateID || receipt.workspaceID != command.WorkspaceID || receipt.proposalID == nil || receipt.action != command.Action || !sameOptionalRelationType(receipt.relationType, command.RelationType) {
		return candidateconfirm.Result{}, candidateConfirmVersionConflict("candidate confirmation idempotency binding differs")
	}
	current, err := loadCandidate(ctx, tx, command.WorkspaceID, command.CandidateID, "id", true)
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	resultCandidate, err := candidateAtDecision(current.candidate, receipt)
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	proposal, err := loadKnowledgeProposalForConfirm(ctx, tx, *receipt.proposalID)
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	if err := validateCandidateConfirmProposalBinding(current.candidate, command, requestHash, proposal); err != nil {
		return candidateconfirm.Result{}, err
	}
	resultCandidate.ProposalID = receipt.proposalID
	resultCandidate.Status = graphdomain.SemanticLinkCandidateStatusProposalCreated
	if err := graphdomain.ValidateSemanticLinkCandidate(resultCandidate); err != nil {
		return candidateconfirm.Result{}, err
	}
	return candidateconfirm.Result{Candidate: resultCandidate, Proposal: proposal, DecisionID: receipt.id, Replayed: true}, nil
}

func buildKnowledgeChange(candidate graphdomain.SemanticLinkCandidate, relationType knowledge.RelationType) (changecontroldomain.KnowledgeChange, error) {
	change := changecontroldomain.KnowledgeChange{
		TargetRefs: []changecontroldomain.KnowledgeTargetRef{{
			Type: changecontroldomain.KnowledgeTargetRefRelationCandidate, ID: candidate.ID, Fingerprint: candidate.Fingerprint,
		}},
		BaseVersions: []changecontroldomain.KnowledgeBaseVersion{
			{NodeType: candidate.Source.Ref.Type, NodeID: candidate.Source.Ref.ID, Version: candidate.Source.Version},
			{NodeType: candidate.Target.Ref.Type, NodeID: candidate.Target.Ref.ID, Version: candidate.Target.Version},
		},
		ChangeSet: changecontroldomain.KnowledgeChangeSet{
			Operation: changecontroldomain.KnowledgeChangeOperationCreateRelation,
			Source:    candidate.Source.Ref, Target: candidate.Target.Ref, RelationType: relationType,
		},
		SchemaVersion: changecontroldomain.KnowledgeChangeSchemaVersion,
	}
	for _, evidence := range candidate.Evidence {
		change.EvidenceRefs = append(change.EvidenceRefs, changecontroldomain.KnowledgeEvidenceRef{CandidateEvidenceID: evidence.ID, SemanticHash: evidence.SemanticHash})
	}
	canonical, err := changecontroldomain.ValidateKnowledgeChange(change)
	if err != nil {
		return changecontroldomain.KnowledgeChange{}, candidateConfirmInvalid(err)
	}
	return canonical, nil
}

func insertOrLoadKnowledgeProposal(ctx context.Context, tx pgx.Tx, proposal changecontroldomain.Proposal) (changecontroldomain.Proposal, bool, error) {
	var insertedID string
	err := tx.QueryRow(ctx, `
		INSERT INTO change_control.proposal(id,workspace_id,proposal_type,idempotency_key,request_hash,status,version,created_at,updated_at)
		VALUES($1,$2,'knowledge_change',$3,$4,$5,$6,$7,$8)
		ON CONFLICT(workspace_id,idempotency_key) DO NOTHING
		RETURNING id::text`,
		string(proposal.ID), string(proposal.WorkspaceID), proposal.IdempotencyKey, proposal.RequestHash,
		string(proposal.Status), proposal.Version, proposal.CreatedAt.UTC(), proposal.UpdatedAt.UTC()).Scan(&insertedID)
	if err == nil {
		change := proposal.Revision.KnowledgeChange
		if change == nil {
			return changecontroldomain.Proposal{}, false, candidateConfirmConsistency(errors.New("knowledge proposal payload is missing"))
		}
		targetRefs, marshalErr := json.Marshal(change.TargetRefs)
		if marshalErr != nil {
			return changecontroldomain.Proposal{}, false, candidateConfirmInvalid(marshalErr)
		}
		baseVersions, marshalErr := json.Marshal(change.BaseVersions)
		if marshalErr != nil {
			return changecontroldomain.Proposal{}, false, candidateConfirmInvalid(marshalErr)
		}
		changeSet, marshalErr := json.Marshal(change.ChangeSet)
		if marshalErr != nil {
			return changecontroldomain.Proposal{}, false, candidateConfirmInvalid(marshalErr)
		}
		evidenceRefs, marshalErr := json.Marshal(change.EvidenceRefs)
		if marshalErr != nil {
			return changecontroldomain.Proposal{}, false, candidateConfirmInvalid(marshalErr)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO change_control.proposal_revision(
				id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,
				target_refs,base_versions,change_set,evidence_refs,schema_version,created_at
			) VALUES($1,$2,$3,NULL,NULL,NULL,NULL,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			string(proposal.Revision.ID), string(proposal.ID), proposal.Revision.RevisionNo, proposal.Revision.Risk,
			proposal.Revision.RollbackPlan, proposal.Revision.ChangeHash, targetRefs, baseVersions, changeSet,
			evidenceRefs, change.SchemaVersion, proposal.Revision.CreatedAt.UTC()); err != nil {
			return changecontroldomain.Proposal{}, false, candidateConfirmClassify(err, "RELATION_PROPOSAL_CONFIRM_REVISION_CREATE_FAILED")
		}
		persisted, loadErr := loadKnowledgeProposalForConfirm(ctx, tx, proposal.ID)
		if loadErr != nil {
			return changecontroldomain.Proposal{}, false, loadErr
		}
		return persisted, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return changecontroldomain.Proposal{}, false, candidateConfirmClassify(err, "RELATION_PROPOSAL_CONFIRM_PROPOSAL_CREATE_FAILED")
	}
	existing, loadErr := loadKnowledgeProposalByKey(ctx, tx, proposal.WorkspaceID, proposal.IdempotencyKey)
	if loadErr != nil {
		return changecontroldomain.Proposal{}, false, loadErr
	}
	if existing.RequestHash != proposal.RequestHash || existing.Type != changecontroldomain.ProposalTypeKnowledgeChange || existing.WorkspaceID != proposal.WorkspaceID || existing.Status != changecontroldomain.StatusReady {
		return changecontroldomain.Proposal{}, false, candidateConfirmVersionConflict("relation proposal idempotency binding differs")
	}
	if existing.Revision.ChangeHash != proposal.Revision.ChangeHash {
		return changecontroldomain.Proposal{}, false, candidateConfirmVersionConflict("relation proposal revision binding differs")
	}
	return existing, false, nil
}

func loadKnowledgeProposalByKey(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, key string) (changecontroldomain.Proposal, error) {
	var proposalID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM change_control.proposal WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(workspaceID), key).Scan(&proposalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return changecontroldomain.Proposal{}, candidateConfirmConsistency(errors.New("proposal idempotency winner is missing"))
		}
		return changecontroldomain.Proposal{}, candidateConfirmClassify(err, "RELATION_PROPOSAL_CONFIRM_PROPOSAL_QUERY_FAILED")
	}
	return loadKnowledgeProposalForConfirm(ctx, tx, foundation.ID(proposalID))
}

func loadKnowledgeProposalForConfirm(ctx context.Context, tx pgx.Tx, proposalID foundation.ID) (changecontroldomain.Proposal, error) {
	var id, workspaceID, proposalType, idempotencyKey, requestHash, status string
	var workflowRunID *string
	var version int64
	var createdAt, updatedAt, revisionCreatedAt time.Time
	var revisionID string
	var revisionNo int
	var risk, rollback, changeHash, schemaVersion *string
	var targetRefsRaw, baseVersionsRaw, changeSetRaw, evidenceRefsRaw []byte
	var approvalID, approvalHash, approvalDecision, approvedGitHead *string
	var approvalDecidedAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT p.id::text,p.workspace_id::text,p.proposal_type,p.idempotency_key,p.request_hash,p.workflow_run_id::text,p.status,p.version,p.created_at,p.updated_at,
		       r.id::text,r.revision_no,r.risk,r.rollback_plan,r.change_hash,r.target_refs,r.base_versions,r.change_set,r.evidence_refs,r.schema_version,r.created_at,
		       a.id::text,a.change_hash,a.decision,a.approved_git_head,a.decided_at
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.revision_no=1
		LEFT JOIN change_control.approval a ON a.revision_id=r.id
		WHERE p.id=$1 FOR UPDATE OF p,r`, string(proposalID)).Scan(
		&id, &workspaceID, &proposalType, &idempotencyKey, &requestHash, &workflowRunID, &status, &version, &createdAt, &updatedAt,
		&revisionID, &revisionNo, &risk, &rollback, &changeHash, &targetRefsRaw, &baseVersionsRaw, &changeSetRaw, &evidenceRefsRaw, &schemaVersion, &revisionCreatedAt,
		&approvalID, &approvalHash, &approvalDecision, &approvedGitHead, &approvalDecidedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return changecontroldomain.Proposal{}, foundation.NewError(foundation.ErrorNotFound, "RELATION_PROPOSAL_NOT_FOUND", false, err)
	}
	if err != nil {
		return changecontroldomain.Proposal{}, candidateConfirmClassify(err, "RELATION_PROPOSAL_CONFIRM_PROPOSAL_QUERY_FAILED")
	}
	if proposalType != string(changecontroldomain.ProposalTypeKnowledgeChange) || risk == nil || rollback == nil || changeHash == nil || schemaVersion == nil || targetRefsRaw == nil || baseVersionsRaw == nil || changeSetRaw == nil || evidenceRefsRaw == nil {
		return changecontroldomain.Proposal{}, candidateConfirmConsistency(errors.New("relation proposal typed payload is incomplete"))
	}
	change := changecontroldomain.KnowledgeChange{SchemaVersion: *schemaVersion}
	if err := json.Unmarshal(targetRefsRaw, &change.TargetRefs); err != nil {
		return changecontroldomain.Proposal{}, candidateConfirmConsistency(err)
	}
	if err := json.Unmarshal(baseVersionsRaw, &change.BaseVersions); err != nil {
		return changecontroldomain.Proposal{}, candidateConfirmConsistency(err)
	}
	if err := json.Unmarshal(changeSetRaw, &change.ChangeSet); err != nil {
		return changecontroldomain.Proposal{}, candidateConfirmConsistency(err)
	}
	if err := json.Unmarshal(evidenceRefsRaw, &change.EvidenceRefs); err != nil {
		return changecontroldomain.Proposal{}, candidateConfirmConsistency(err)
	}
	canonicalChange, err := changecontroldomain.ValidateKnowledgeChange(change)
	if err != nil {
		return changecontroldomain.Proposal{}, candidateConfirmConsistency(err)
	}
	proposal := changecontroldomain.Proposal{
		ID: foundation.ID(id), WorkspaceID: foundation.ID(workspaceID), Type: changecontroldomain.ProposalTypeKnowledgeChange,
		IdempotencyKey: idempotencyKey, RequestHash: requestHash, Status: changecontroldomain.ProposalStatus(status), Version: version,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
		Revision: changecontroldomain.Revision{ID: foundation.ID(revisionID), ProposalID: foundation.ID(id), RevisionNo: revisionNo,
			Risk: *risk, RollbackPlan: *rollback, ChangeHash: *changeHash, KnowledgeChange: &canonicalChange, CreatedAt: revisionCreatedAt},
	}
	if workflowRunID != nil {
		value := foundation.ID(*workflowRunID)
		proposal.WorkflowRunID = &value
	}
	if approvalID != nil || approvalHash != nil || approvalDecision != nil || approvalDecidedAt != nil || approvedGitHead != nil {
		if approvalID == nil || approvalHash == nil || approvalDecision == nil || approvalDecidedAt == nil {
			return changecontroldomain.Proposal{}, candidateConfirmConsistency(errors.New("relation proposal approval payload is incomplete"))
		}
		proposal.Approval = &changecontroldomain.Approval{
			ID: foundation.ID(*approvalID), ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
			ChangeHash: *approvalHash, Decision: changecontroldomain.Decision(*approvalDecision),
			ApprovedGitHead: approvedGitHead, DecidedAt: *approvalDecidedAt,
		}
	}
	if err := changecontroldomain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision); err != nil {
		return changecontroldomain.Proposal{}, candidateConfirmConsistency(err)
	}
	return proposal, nil
}

func validateCandidateConfirmProposalBinding(candidate graphdomain.SemanticLinkCandidate, command candidateconfirm.Command, requestHash string, proposal changecontroldomain.Proposal) error {
	if proposal.WorkspaceID != candidate.WorkspaceID || proposal.WorkspaceID != command.WorkspaceID || proposal.Type != changecontroldomain.ProposalTypeKnowledgeChange || proposal.Revision.KnowledgeChange == nil {
		return candidateConfirmConsistency(errors.New("relation proposal does not bind candidate workspace or type"))
	}
	if candidate.ProposalID != nil && *candidate.ProposalID != proposal.ID {
		return candidateConfirmConsistency(errors.New("candidate projection and relation proposal differ"))
	}
	effectiveRelationType := candidate.SuggestedRelationType
	if command.RelationType != nil {
		effectiveRelationType = *command.RelationType
	}
	expectedChange, err := buildKnowledgeChange(candidate, effectiveRelationType)
	if err != nil {
		return err
	}
	expectedHash, err := changecontroldomain.ComputeKnowledgeChangeHash(expectedChange, command.Risk, command.RollbackPlan)
	if err != nil {
		return candidateConfirmInvalid(err)
	}
	expectedRequestHash, err := changecontroldomain.ComputeKnowledgeChangeRequestHash(candidate.WorkspaceID, expectedChange, command.Risk, command.RollbackPlan)
	if err != nil {
		return candidateConfirmInvalid(err)
	}
	if proposal.IdempotencyKey != "semantic-link-confirm/v1:"+requestHash || proposal.RequestHash != expectedRequestHash || proposal.Revision.ChangeHash != expectedHash || proposal.Revision.Risk != command.Risk || proposal.Revision.RollbackPlan != command.RollbackPlan {
		return candidateConfirmConsistency(errors.New("relation proposal command binding differs"))
	}
	return nil
}

func sameOptionalRelationType(left, right *knowledge.RelationType) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func isNilCandidateConfirmDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func candidateConfirmInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "RELATION_PROPOSAL_CONFIRM_INVALID", false, cause)
}

func candidateConfirmVersionConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "RELATION_PROPOSAL_CONFIRM_CONFLICT", false, errors.New(message))
}

func candidateConfirmConsistency(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "RELATION_PROPOSAL_CONFIRM_CONSISTENCY", false, cause)
}

func candidateConfirmUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "RELATION_PROPOSAL_CONFIRM_UNAVAILABLE", true, cause)
}

func candidateConfirmClassify(err error, code string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		case "23503", "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, code, false, err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}
