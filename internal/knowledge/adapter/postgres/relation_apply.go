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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const candidateFingerprintSchemaV1 = "semantic-link-candidate/v1"

type relationProposalRequestHashVersion string

const (
	relationProposalRequestHashV1 relationProposalRequestHashVersion = "v1"
	relationProposalRequestHashV2 relationProposalRequestHashVersion = "v2"
)

// ApprovedRelationApplyRepository 在一个 PostgreSQL 事务内把已批准的
// knowledge_change Proposal 应用为唯一的 canonical Relation。
type ApprovedRelationApplyRepository struct {
	db     DB
	ids    foundation.IDGenerator
	clock  foundation.Clock
	events eventsapplication.Appender
}

var _ knowledgeapplication.ApprovedRelationApplyPort = (*ApprovedRelationApplyRepository)(nil)
var _ knowledgeapplication.ApprovedRelationApprovalPort = (*ApprovedRelationApplyRepository)(nil)

// NewApprovedRelationApplyRepository 创建 Approval 到 Knowledge 的正式写入 adapter；可选事件追加器与 Relation apply 共用事务。
func NewApprovedRelationApplyRepository(db DB, ids foundation.IDGenerator, clock foundation.Clock, appenders ...eventsapplication.Appender) (*ApprovedRelationApplyRepository, error) {
	if isNilRelationApplyDependency(db) || isNilRelationApplyDependency(ids) || isNilRelationApplyDependency(clock) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "RELATION_PROPOSAL_APPLY_DEPENDENCY_MISSING", false, errors.New("relation apply dependency is missing"))
	}
	if len(appenders) > 1 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "RELATION_PROPOSAL_APPLY_EVENT_APPENDER_INVALID", false, errors.New("only one relation apply event appender may be configured"))
	}
	var events eventsapplication.Appender
	if len(appenders) == 1 && !isNilRelationApplyDependency(appenders[0]) {
		events = appenders[0]
	}
	return &ApprovedRelationApplyRepository{db: db, ids: ids, clock: clock, events: events}, nil
}

// ApproveAndApplyRelation 在一个 PostgreSQL 事务内创建 Approval、推进 Proposal，
// 并应用正式 Relation。普通 apply 失败会回滚整笔事务；基线漂移只提交
// Approval 与 needs_revision，供调用方创建新 Revision。相同审批的响应丢失会
// 通过已有 Approval 和 Knowledge receipt 精确重放。
func (repository *ApprovedRelationApplyRepository) ApproveAndApplyRelation(ctx context.Context, approval changecontroldomain.Approval) (changecontroldomain.Approval, knowledgeapplication.ApprovedRelationApplyResult, error) {
	if repository == nil || isNilRelationApplyDependency(repository.db) || isNilRelationApplyDependency(repository.ids) || isNilRelationApplyDependency(repository.clock) {
		return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyUnavailable(errors.New("relation approval repository is unavailable"))
	}
	if err := validateRelationApprovalInput(approval); err != nil {
		return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyInvalid(err)
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPROVAL_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	lockTarget, err := lockRelationApplyCandidateBeforeProposal(ctx, tx, approval.ProposalID, approval.RevisionID)
	if err != nil {
		return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	binding, err := loadRelationApprovalBinding(ctx, tx, approval)
	if err != nil {
		return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if binding.workspaceID != lockTarget.workspaceID {
		return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(errors.New("relation approval workspace binding changed"))
	}
	persistedApproval, err := repository.persistRelationApproval(ctx, tx, approval, binding)
	if err != nil {
		return persistedApproval, knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	command := knowledgeapplication.ApprovedRelationApplyCommand{
		WorkspaceID: binding.workspaceID,
		ProposalID:  persistedApproval.ProposalID,
		RevisionID:  persistedApproval.RevisionID,
		ApprovalID:  persistedApproval.ID,
	}
	requestHash, err := knowledgeapplication.RelationApplyRequestHash(command)
	if err != nil {
		return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyInvalid(err)
	}
	result, err := repository.applyApprovedRelationTx(
		ctx, tx, command,
		knowledgeapplication.RelationApplyIdempotencyKey(command.ApprovalID), requestHash,
	)
	var stale *relationApplyNeedsRevision
	if errors.As(err, &stale) {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(commitErr, "RELATION_PROPOSAL_APPROVAL_STALE_COMMIT_FAILED")
		}
		return persistedApproval, knowledgeapplication.ApprovedRelationApplyResult{}, foundation.NewError(foundation.ErrorVersionConflict, "RELATION_PROPOSAL_BASE_STALE", false, stale)
	}
	if err != nil {
		return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return changecontroldomain.Approval{}, knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPROVAL_COMMIT_FAILED")
	}
	return persistedApproval, result, nil
}

type relationApprovalBinding struct {
	workspaceID     foundation.ID
	status          changecontroldomain.ProposalStatus
	proposalVersion int64
	revisionHash    string
	existing        *changecontroldomain.Approval
}

// relationApplyLockTarget 是在锁 Proposal 前确定的 Candidate 锁目标。
// Proposal Revision 为不可变事实，先锁 Candidate 可让 Candidate Confirm、旧 Apply
// 与原子 Approval UoW 使用同一锁序，避免 Proposal↔Candidate 反向等待。
type relationApplyLockTarget struct {
	workspaceID foundation.ID
	candidateID foundation.ID
}

func lockRelationApplyCandidateBeforeProposal(ctx context.Context, tx pgx.Tx, proposalID, revisionID foundation.ID) (relationApplyLockTarget, error) {
	var target relationApplyLockTarget
	var workspaceID string
	var targetRefsRaw []byte
	if err := tx.QueryRow(ctx, `
		SELECT p.workspace_id::text,r.target_refs
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.id=$2
		WHERE p.id=$1`, string(proposalID), string(revisionID)).Scan(&workspaceID, &targetRefsRaw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return relationApplyLockTarget{}, relationApplyNotFound(err)
		}
		return relationApplyLockTarget{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_LOCK_TARGET_QUERY_FAILED")
	}
	target.workspaceID = foundation.ID(workspaceID)
	var refs []changecontroldomain.KnowledgeTargetRef
	if json.Unmarshal(targetRefsRaw, &refs) != nil || len(refs) != 1 || refs[0].Type != changecontroldomain.KnowledgeTargetRefRelationCandidate {
		return relationApplyLockTarget{}, relationApplyConsistency(errors.New("knowledge relation proposal candidate target is invalid"))
	}
	parsed, err := foundation.ParseID(string(refs[0].ID))
	if err != nil || parsed != refs[0].ID {
		return relationApplyLockTarget{}, relationApplyConsistency(errors.New("knowledge relation proposal candidate identity is invalid"))
	}
	target.candidateID = refs[0].ID
	var lockedID string
	if err := tx.QueryRow(ctx, `
		SELECT id::text
		FROM graph.semantic_link_candidate
		WHERE workspace_id=$1 AND id=$2
		FOR UPDATE`, string(target.workspaceID), string(target.candidateID)).Scan(&lockedID); errors.Is(err, pgx.ErrNoRows) {
		// 缺失 Candidate 在后续基线校验中转为 needs_revision；此处没有可锁的行。
		return target, nil
	} else if err != nil {
		return relationApplyLockTarget{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_CANDIDATE_LOCK_FAILED")
	}
	return target, nil
}

func validateRelationApprovalInput(approval changecontroldomain.Approval) error {
	for name, value := range map[string]foundation.ID{
		"proposal_id": approval.ProposalID,
		"revision_id": approval.RevisionID,
		"approval_id": approval.ID,
	} {
		parsed, err := foundation.ParseID(string(value))
		if err != nil || parsed != value {
			return errors.New(name + " is not a canonical UUID")
		}
	}
	if !changecontroldomain.ValidHash(approval.ChangeHash) {
		return errors.New("approval change hash is invalid")
	}
	if approval.Decision != changecontroldomain.DecisionApproved {
		return errors.New("knowledge relation approval must be approved")
	}
	if approval.ApprovedGitHead != nil {
		return errors.New("knowledge relation approval cannot bind a git head")
	}
	if approval.DecidedAt.IsZero() {
		return errors.New("approval decision time is required")
	}
	return nil
}

func loadRelationApprovalBinding(ctx context.Context, tx pgx.Tx, approval changecontroldomain.Approval) (relationApprovalBinding, error) {
	var binding relationApprovalBinding
	var workspaceID, status, revisionHash string
	err := tx.QueryRow(ctx, `
		SELECT p.workspace_id::text,p.status,p.version,r.change_hash
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.id=$2
		WHERE p.id=$1
		FOR UPDATE OF p,r`, string(approval.ProposalID), string(approval.RevisionID)).Scan(
		&workspaceID, &status, &binding.proposalVersion, &revisionHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return relationApprovalBinding{}, relationApplyNotFound(err)
	}
	if err != nil {
		return relationApprovalBinding{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPROVAL_BINDING_QUERY_FAILED")
	}
	binding.workspaceID = foundation.ID(workspaceID)
	binding.status = changecontroldomain.ProposalStatus(status)
	binding.revisionHash = strings.ToLower(revisionHash)
	var existingID, existingProposalID, existingRevisionID, existingHash, existingDecision string
	var approvedGitHead *string
	var decidedAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT id::text,proposal_id::text,revision_id::text,change_hash,decision,approved_git_head,decided_at
		FROM change_control.approval
		WHERE revision_id=$1
		FOR UPDATE`, string(approval.RevisionID)).Scan(
		&existingID, &existingProposalID, &existingRevisionID, &existingHash, &existingDecision, &approvedGitHead, &decidedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return binding, nil
	}
	if err != nil {
		return relationApprovalBinding{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPROVAL_QUERY_FAILED")
	}
	persisted := changecontroldomain.Approval{
		ID: foundation.ID(existingID), ProposalID: foundation.ID(existingProposalID), RevisionID: foundation.ID(existingRevisionID),
		ChangeHash: strings.ToLower(existingHash), Decision: changecontroldomain.Decision(existingDecision),
		ApprovedGitHead: approvedGitHead, DecidedAt: decidedAt,
	}
	binding.existing = &persisted
	return binding, nil
}

func (repository *ApprovedRelationApplyRepository) persistRelationApproval(ctx context.Context, tx pgx.Tx, requested changecontroldomain.Approval, binding relationApprovalBinding) (changecontroldomain.Approval, error) {
	if binding.existing != nil {
		existing := *binding.existing
		if existing.ProposalID != requested.ProposalID || existing.RevisionID != requested.RevisionID ||
			!strings.EqualFold(existing.ChangeHash, requested.ChangeHash) || existing.Decision != requested.Decision ||
			(existing.ApprovedGitHead != nil) {
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
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.approval(id,proposal_id,revision_id,change_hash,decision,approved_git_head,decided_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, string(requested.ID), string(requested.ProposalID), string(requested.RevisionID),
		strings.ToLower(requested.ChangeHash), string(requested.Decision), requested.ApprovedGitHead, requested.DecidedAt.UTC()); err != nil {
		return changecontroldomain.Approval{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPROVAL_CREATE_FAILED")
	}
	newVersion, err := transitionRelationApplyProposal(ctx, tx, requested.ProposalID, binding.proposalVersion,
		changecontroldomain.StatusReady, changecontroldomain.StatusApproved, requested.DecidedAt)
	if err != nil {
		return changecontroldomain.Approval{}, err
	}
	if !isNilRelationApplyDependency(repository.events) {
		request := changecontroleventcontract.ProposalStatusRequest(
			binding.workspaceID, requested.ProposalID, requested.ID,
			changecontroleventcontract.ProposalApprovedEventType, string(changecontroldomain.StatusApproved), newVersion, requested.DecidedAt,
		)
		if _, _, err := repository.events.AppendTx(ctx, tx, request); err != nil {
			return changecontroldomain.Approval{}, err
		}
	}
	requested.ChangeHash = strings.ToLower(requested.ChangeHash)
	requested.DecidedAt = requested.DecidedAt.UTC()
	return requested, nil
}

// ApplyApprovedRelation 重新校验 Approval、typed Revision、Candidate、端点版本和
// Provenance，再幂等创建一条 CONFIRMED Relation。基线漂移会持久化
// Proposal needs_revision；任一正式写入失败则整体回滚。
func (repository *ApprovedRelationApplyRepository) ApplyApprovedRelation(ctx context.Context, command knowledgeapplication.ApprovedRelationApplyCommand) (knowledgeapplication.ApprovedRelationApplyResult, error) {
	if repository == nil || isNilRelationApplyDependency(repository.db) {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyUnavailable(errors.New("relation apply repository is unavailable"))
	}
	if err := knowledgeapplication.ValidateApprovedRelationApplyCommand(command); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	requestHash, err := knowledgeapplication.RelationApplyRequestHash(command)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyInvalid(err)
	}
	idempotencyKey := knowledgeapplication.RelationApplyIdempotencyKey(command.ApprovalID)
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	lockTarget, err := lockRelationApplyCandidateBeforeProposal(ctx, tx, command.ProposalID, command.RevisionID)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if lockTarget.workspaceID != command.WorkspaceID {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(errors.New("relation apply workspace binding changed"))
	}
	result, err := repository.applyApprovedRelationTx(ctx, tx, command, idempotencyKey, requestHash)
	var stale *relationApplyNeedsRevision
	if errors.As(err, &stale) {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(commitErr, "RELATION_PROPOSAL_APPLY_STALE_COMMIT_FAILED")
		}
		return knowledgeapplication.ApprovedRelationApplyResult{}, foundation.NewError(foundation.ErrorVersionConflict, "RELATION_PROPOSAL_BASE_STALE", false, stale)
	}
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_COMMIT_FAILED")
	}
	return result, nil
}

func (repository *ApprovedRelationApplyRepository) applyApprovedRelationTx(
	ctx context.Context,
	tx pgx.Tx,
	command knowledgeapplication.ApprovedRelationApplyCommand,
	idempotencyKey string,
	requestHash string,
) (knowledgeapplication.ApprovedRelationApplyResult, error) {
	proposal, err := loadApprovedRelationProposal(ctx, tx, command)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if err := lockRelationApplyReceipt(ctx, tx, command.WorkspaceID, idempotencyKey); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	receipt, err := loadReceipt(ctx, tx, command.WorkspaceID, idempotencyKey)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_RECEIPT_QUERY_FAILED")
	}
	if receipt != nil {
		return repository.replayApprovedRelationApply(ctx, tx, command, requestHash, proposal, receipt)
	}
	if proposal.Status != changecontroldomain.StatusApproved {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyApprovalRequired(errors.New("knowledge relation proposal is not approved"))
	}

	candidate, err := loadRelationApplyCandidate(ctx, tx, command.WorkspaceID, proposal.change.TargetRefs[0].ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return repository.markNeedsRevision(ctx, tx, proposal, "relation candidate is missing")
		}
		var baseline *relationApplyBaselineError
		if errors.As(err, &baseline) {
			return repository.markNeedsRevision(ctx, tx, proposal, baseline.Error())
		}
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_CANDIDATE_QUERY_FAILED")
	}
	if err := validateRelationApplyBinding(candidate, proposal); err != nil {
		return repository.markNeedsRevision(ctx, tx, proposal, err.Error())
	}

	applicability, err := loadAndValidateRelationApplyEndpoints(ctx, tx, command.WorkspaceID, proposal.change.BaseVersions)
	if err != nil {
		var baseline *relationApplyBaselineError
		if errors.As(err, &baseline) {
			return repository.markNeedsRevision(ctx, tx, proposal, baseline.Error())
		}
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_ENDPOINT_QUERY_FAILED")
	}
	if err := validateRelationApplyProvenance(ctx, tx, candidate); err != nil {
		var baseline *relationApplyBaselineError
		if errors.As(err, &baseline) {
			return repository.markNeedsRevision(ctx, tx, proposal, baseline.Error())
		}
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_PROVENANCE_QUERY_FAILED")
	}

	relationFingerprint := domain.ComputeRelationFingerprint(
		command.WorkspaceID,
		proposal.change.ChangeSet.RelationType,
		proposal.change.ChangeSet.Source,
		proposal.change.ChangeSet.Target,
	)
	if relationFingerprint == "" {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(errors.New("relation fingerprint cannot be computed"))
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || chr(31) || $2, 0))`, string(command.WorkspaceID), relationFingerprint); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_RELATION_LOCK_FAILED")
	}
	existingResult, relationErr := loadRelationByFingerprint(ctx, tx, command.WorkspaceID, relationFingerprint)
	if relationErr != nil && !errors.Is(relationErr, pgx.ErrNoRows) {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(relationErr, "RELATION_PROPOSAL_APPLY_RELATION_QUERY_FAILED")
	}
	var existing *domain.RelationResult
	if relationErr == nil {
		if err := validateReusableSuggestedRelation(existingResult, command.WorkspaceID, proposal.change.ChangeSet, relationFingerprint); err != nil {
			return repository.markNeedsRevision(ctx, tx, proposal, "canonical relation already exists in a non-reusable state")
		}
		existing = &existingResult
	}

	atValues := []time.Time{repository.clock.Now().UTC(), proposal.UpdatedAt, proposal.Approval.DecidedAt, candidate.UpdatedAt}
	if existing != nil {
		atValues = append(atValues, existing.Relation.UpdatedAt)
	}
	at := latestRelationApplyTime(atValues...)
	if at.IsZero() {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyUnavailable(errors.New("relation apply clock returned zero time"))
	}
	applyingVersion, err := transitionRelationApplyProposal(ctx, tx, proposal.ID, proposal.Version, changecontroldomain.StatusApproved, changecontroldomain.StatusApplying, at)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}

	confirmation := domain.Confirmation{Method: domain.ConfirmationUserApproval, Reference: string(command.ApprovalID)}
	relationID := foundation.ID("")
	if existing != nil {
		relationID = existing.Relation.ID
	} else {
		relationID, err = repository.ids.New()
		if err != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, err
		}
	}
	evidence, err := repository.buildConfirmedRelationEvidence(candidate, relationID, confirmation, applicability, at)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	allEvidence := evidence
	if existing != nil {
		allEvidence = append(append([]domain.RelationEvidence(nil), existing.Evidence...), evidence...)
	} else {
		confidence := candidate.Confidence
		relation := domain.Relation{
			ID: relationID, WorkspaceID: command.WorkspaceID,
			Source: proposal.change.ChangeSet.Source, Target: proposal.change.ChangeSet.Target,
			Type: proposal.change.ChangeSet.RelationType, Status: domain.RelationStatusSuggested,
			ConfidenceScore: &confidence, Fingerprint: relationFingerprint,
			EvidenceFingerprint: domain.ComputeRelationEvidenceFingerprint(allEvidence),
			Version:             1, CreatedAt: at, UpdatedAt: at,
		}
		if err := domain.ValidateRelationAggregate(relation, allEvidence); err != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(err)
		}
		inserted, insertErr := insertRelation(ctx, tx, relation)
		if errors.Is(insertErr, pgx.ErrNoRows) {
			// A regular SuggestRelation does not take the apply advisory lock. It can
			// win the fingerprint unique constraint after the initial lookup above.
			// Re-read that winner and follow the same Suggested-reuse path instead of
			// incorrectly terminating an otherwise valid approval as stale.
			winner, loadErr := loadRelationByFingerprint(ctx, tx, command.WorkspaceID, relationFingerprint)
			if loadErr != nil {
				return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(loadErr, "RELATION_PROPOSAL_APPLY_RELATION_QUERY_FAILED")
			}
			if reuseErr := validateReusableSuggestedRelation(winner, command.WorkspaceID, proposal.change.ChangeSet, relationFingerprint); reuseErr != nil {
				return repository.markNeedsRevisionAtVersion(ctx, tx, proposal, applyingVersion, changecontroldomain.StatusApplying, "canonical relation was created concurrently in a non-reusable state")
			}
			existing = &winner
			relationID = winner.Relation.ID
			at = latestRelationApplyTime(at, winner.Relation.UpdatedAt)
			evidence, err = repository.buildConfirmedRelationEvidence(candidate, relationID, confirmation, applicability, at)
			if err != nil {
				return knowledgeapplication.ApprovedRelationApplyResult{}, err
			}
			allEvidence = append(append([]domain.RelationEvidence(nil), winner.Evidence...), evidence...)
		} else if insertErr != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(insertErr, "RELATION_PROPOSAL_APPLY_RELATION_CREATE_FAILED")
		}
		if existing == nil {
			relationID = inserted.ID
		}
	}
	for _, item := range evidence {
		if err := insertRelationEvidence(ctx, tx, item); err != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_EVIDENCE_CREATE_FAILED")
		}
	}
	confirmed, err := scanRelation(tx.QueryRow(ctx, `
		UPDATE core.relation
		SET status=$1,confirmation_method=$2,confirmation_ref=$3,evidence_fingerprint=$4,version=version+1,updated_at=$5
		WHERE workspace_id=$6 AND id=$7 AND version=$8 AND status=$9
		RETURNING id::text,workspace_id::text,source_node_type,source_node_id::text,target_node_type,target_node_id::text,
			relation_type,status,confirmation_method,confirmation_ref,confidence_score,valid_from,valid_to,
			fingerprint,evidence_fingerprint,version,created_at,updated_at`,
		string(domain.RelationStatusConfirmed), string(confirmation.Method), confirmation.Reference,
		domain.ComputeRelationEvidenceFingerprint(allEvidence), at,
		string(command.WorkspaceID), string(relationID), relationVersionBeforeConfirm(existing), string(domain.RelationStatusSuggested),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(errors.New("relation confirmation CAS was lost"))
	}
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_RELATION_CONFIRM_FAILED")
	}
	if err := domain.ValidateRelationAggregate(confirmed, allEvidence); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(err)
	}
	if err := insertReceipt(ctx, tx, command.WorkspaceID, idempotencyKey, requestHash, commandConfirmRelation, aggregateRelation, confirmed.ID, confirmed.Version, at); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_RECEIPT_CREATE_FAILED")
	}
	appliedVersion, err := transitionRelationApplyProposal(ctx, tx, proposal.ID, applyingVersion, changecontroldomain.StatusApplying, changecontroldomain.StatusApplied, at)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if !isNilRelationApplyDependency(repository.events) {
		request := changecontroleventcontract.ProposalStatusRequest(
			command.WorkspaceID, proposal.ID, command.ApprovalID,
			changecontroleventcontract.ProposalAppliedEventType, string(changecontroldomain.StatusApplied), appliedVersion, at,
		)
		if _, _, err := repository.events.AppendTx(ctx, tx, request); err != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, err
		}
	}
	return knowledgeapplication.ApprovedRelationApplyResult{
		Relation: confirmed, Evidence: allEvidence,
		ProposalStatus: changecontroldomain.StatusApplied, ProposalVersion: appliedVersion,
	}, nil
}

type approvedRelationProposal struct {
	ID, WorkspaceID foundation.ID
	Type            changecontroldomain.ProposalType
	RiskLevel       changecontroldomain.ProposalRiskLevel
	Status          changecontroldomain.ProposalStatus
	Version         int64
	RequestHash     string
	UpdatedAt       time.Time
	Revision        changecontroldomain.Revision
	Approval        changecontroldomain.Approval
	change          changecontroldomain.KnowledgeChange
}

func loadApprovedRelationProposal(ctx context.Context, tx pgx.Tx, command knowledgeapplication.ApprovedRelationApplyCommand) (approvedRelationProposal, error) {
	var proposal approvedRelationProposal
	var proposalID, workspaceID, proposalType, riskLevel, status, revisionID, revisionProposalID string
	var approvalID, approvalProposalID, approvalRevisionID, approvalDecision string
	var targetRefsRaw, baseVersionsRaw, changeSetRaw, evidenceRefsRaw []byte
	var schemaVersion string
	var approvedGitHead *string
	err := tx.QueryRow(ctx, `
			SELECT p.id::text,p.workspace_id::text,p.proposal_type,p.risk_level,p.status,p.version,p.request_hash,p.updated_at,
			r.id::text,r.proposal_id::text,r.revision_no,r.risk,r.rollback_plan,r.change_hash,
			r.target_refs,r.base_versions,r.change_set,r.evidence_refs,r.schema_version,r.created_at,
			a.id::text,a.proposal_id::text,a.revision_id::text,a.change_hash,a.decision,a.approved_git_head,a.decided_at
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.id=$2
		JOIN change_control.approval a ON a.proposal_id=p.id AND a.revision_id=r.id AND a.id=$3
		WHERE p.id=$1 AND p.workspace_id=$4
		FOR UPDATE OF p,r,a`,
		string(command.ProposalID), string(command.RevisionID), string(command.ApprovalID), string(command.WorkspaceID),
	).Scan(
		&proposalID, &workspaceID, &proposalType, &riskLevel, &status, &proposal.Version, &proposal.RequestHash, &proposal.UpdatedAt,
		&revisionID, &revisionProposalID, &proposal.Revision.RevisionNo, &proposal.Revision.Risk, &proposal.Revision.RollbackPlan, &proposal.Revision.ChangeHash,
		&targetRefsRaw, &baseVersionsRaw, &changeSetRaw, &evidenceRefsRaw, &schemaVersion, &proposal.Revision.CreatedAt,
		&approvalID, &approvalProposalID, &approvalRevisionID, &proposal.Approval.ChangeHash, &approvalDecision, &approvedGitHead, &proposal.Approval.DecidedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return approvedRelationProposal{}, relationApplyNotFound(err)
	}
	if err != nil {
		return approvedRelationProposal{}, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_BINDING_QUERY_FAILED")
	}
	proposal.ID, proposal.WorkspaceID = foundation.ID(proposalID), foundation.ID(workspaceID)
	proposal.Type = changecontroldomain.ProposalType(proposalType)
	proposal.RiskLevel, err = changecontroldomain.ValidateProposalRiskLevelForType(proposal.Type, changecontroldomain.ProposalRiskLevel(riskLevel))
	if err != nil {
		return approvedRelationProposal{}, relationApplyConsistency(err)
	}
	proposal.Status = changecontroldomain.ProposalStatus(status)
	proposal.Revision.ID, proposal.Revision.ProposalID = foundation.ID(revisionID), foundation.ID(revisionProposalID)
	proposal.Approval.ID = foundation.ID(approvalID)
	proposal.Approval.ProposalID, proposal.Approval.RevisionID = foundation.ID(approvalProposalID), foundation.ID(approvalRevisionID)
	proposal.Approval.Decision = changecontroldomain.Decision(approvalDecision)
	proposal.Approval.ApprovedGitHead = approvedGitHead
	proposal.change.SchemaVersion = schemaVersion
	if json.Unmarshal(targetRefsRaw, &proposal.change.TargetRefs) != nil ||
		json.Unmarshal(baseVersionsRaw, &proposal.change.BaseVersions) != nil ||
		json.Unmarshal(changeSetRaw, &proposal.change.ChangeSet) != nil ||
		json.Unmarshal(evidenceRefsRaw, &proposal.change.EvidenceRefs) != nil {
		return approvedRelationProposal{}, relationApplyConsistency(errors.New("knowledge relation revision payload cannot be decoded"))
	}
	canonicalChange, err := changecontroldomain.ValidateKnowledgeChange(proposal.change)
	if err != nil || len(canonicalChange.TargetRefs) != 1 {
		return approvedRelationProposal{}, relationApplyConsistency(errors.New("knowledge relation revision payload is invalid"))
	}
	proposal.change = canonicalChange
	proposal.Revision.KnowledgeChange = &proposal.change
	if _, err := validateApprovedRelationProposalBinding(proposal, command); err != nil {
		return approvedRelationProposal{}, relationApplyConsistency(err)
	}
	return proposal, nil
}

func validateApprovedRelationProposalBinding(proposal approvedRelationProposal, command knowledgeapplication.ApprovedRelationApplyCommand) (relationProposalRequestHashVersion, error) {
	if proposal.Type != changecontroldomain.ProposalTypeKnowledgeChange ||
		proposal.ID != command.ProposalID || proposal.WorkspaceID != command.WorkspaceID ||
		proposal.Revision.ID != command.RevisionID || proposal.Revision.ProposalID != proposal.ID ||
		proposal.Approval.ID != command.ApprovalID || proposal.Approval.ProposalID != proposal.ID || proposal.Approval.RevisionID != proposal.Revision.ID ||
		proposal.Approval.Decision != changecontroldomain.DecisionApproved || proposal.Approval.ApprovedGitHead != nil {
		return "", errors.New("knowledge relation approval binding is inconsistent")
	}
	riskLevel, err := changecontroldomain.ValidateProposalRiskLevelForType(changecontroldomain.ProposalTypeKnowledgeChange, proposal.RiskLevel)
	if err != nil {
		return "", errors.New("knowledge relation proposal risk level is invalid")
	}
	if err := changecontroldomain.ValidateProposalRevisionForType(changecontroldomain.ProposalTypeKnowledgeChange, proposal.Revision); err != nil {
		return "", err
	}
	requestHashV2, err := changecontroldomain.ComputeKnowledgeChangeRequestHashWithRiskLevel(
		proposal.WorkspaceID,
		proposal.change,
		riskLevel,
		proposal.Revision.Risk,
		proposal.Revision.RollbackPlan,
	)
	if err != nil {
		return "", err
	}
	requestHashV1, err := changecontroldomain.ComputeKnowledgeChangeRequestHash(
		proposal.WorkspaceID,
		proposal.change,
		proposal.Revision.Risk,
		proposal.Revision.RollbackPlan,
	)
	if err != nil {
		return "", err
	}
	var version relationProposalRequestHashVersion
	switch proposal.RequestHash {
	case requestHashV2:
		version = relationProposalRequestHashV2
	case requestHashV1:
		version = relationProposalRequestHashV1
	default:
		return "", errors.New("knowledge relation proposal request hash binding is inconsistent")
	}
	if proposal.Approval.ChangeHash != proposal.Revision.ChangeHash {
		return "", errors.New("knowledge relation approval change hash binding is inconsistent")
	}
	return version, nil
}

func loadRelationApplyCandidate(ctx context.Context, tx pgx.Tx, workspaceID, candidateID foundation.ID) (graphdomain.SemanticLinkCandidate, error) {
	var candidate graphdomain.SemanticLinkCandidate
	var id, persistedWorkspace, sourceType, sourceID, targetType, targetID, relationType, status, fingerprintSchema string
	var reopenedReason, reopenedFrom, proposalID *string
	var confidence *float64
	var discoveryRaw, evidenceHashesRaw, generationRaw []byte
	var evidenceHashes []string
	err := tx.QueryRow(ctx, `
		SELECT id::text,workspace_id::text,source_node_type,source_node_id::text,source_node_version,
			target_node_type,target_node_id::text,target_node_version,relation_type,status,reopened_reason,
			reopened_from_candidate_id::text,current_proposal_id::text,confidence_score,reason,
			source_summary,source_excerpt,target_summary,target_excerpt,discovery_methods,evidence_semantic_hashes,
			generation,deferred_until,fingerprint_schema_version,fingerprint,version,created_at,updated_at
		FROM graph.semantic_link_candidate
		WHERE workspace_id=$1 AND id=$2
		FOR UPDATE`, string(workspaceID), string(candidateID)).Scan(
		&id, &persistedWorkspace, &sourceType, &sourceID, &candidate.Source.Version,
		&targetType, &targetID, &candidate.Target.Version, &relationType, &status, &reopenedReason,
		&reopenedFrom, &proposalID, &confidence, &candidate.Reason,
		&candidate.Source.Summary, &candidate.Source.Excerpt, &candidate.Target.Summary, &candidate.Target.Excerpt,
		&discoveryRaw, &evidenceHashesRaw, &generationRaw, &candidate.ResumeAfter, &fingerprintSchema,
		&candidate.Fingerprint, &candidate.Version, &candidate.CreatedAt, &candidate.UpdatedAt,
	)
	if err != nil {
		return graphdomain.SemanticLinkCandidate{}, err
	}
	candidate.ID, candidate.WorkspaceID = foundation.ID(id), foundation.ID(persistedWorkspace)
	candidate.Source.Ref = domain.NodeRef{Type: domain.NodeType(sourceType), ID: foundation.ID(sourceID)}
	candidate.Target.Ref = domain.NodeRef{Type: domain.NodeType(targetType), ID: foundation.ID(targetID)}
	candidate.SuggestedRelationType = domain.RelationType(relationType)
	candidate.Status = graphdomain.SemanticLinkCandidateStatus(status)
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
	if fingerprintSchema != candidateFingerprintSchemaV1 || json.Unmarshal(discoveryRaw, &candidate.DiscoveryMethods) != nil ||
		json.Unmarshal(generationRaw, &candidate.Generation) != nil || json.Unmarshal(evidenceHashesRaw, &evidenceHashes) != nil {
		return graphdomain.SemanticLinkCandidate{}, baselineChanged("candidate stored payload is invalid")
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text,source_version_id::text,source_span_id::text,semantic_hash,summary,excerpt
		FROM graph.semantic_link_candidate_evidence
		WHERE workspace_id=$1 AND candidate_id=$2
		ORDER BY semantic_hash,id`, string(workspaceID), string(candidateID))
	if err != nil {
		return graphdomain.SemanticLinkCandidate{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var evidence graphdomain.SemanticLinkCandidateEvidence
		var evidenceID, sourceVersionID, sourceSpanID string
		if err := rows.Scan(&evidenceID, &sourceVersionID, &sourceSpanID, &evidence.SemanticHash, &evidence.Reason, &evidence.Excerpt); err != nil {
			return graphdomain.SemanticLinkCandidate{}, err
		}
		evidence.ID = foundation.ID(evidenceID)
		evidence.Provenance = domain.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: foundation.ID(sourceVersionID), SourceSpanID: foundation.ID(sourceSpanID)}
		candidate.Evidence = append(candidate.Evidence, evidence)
	}
	if err := rows.Err(); err != nil {
		return graphdomain.SemanticLinkCandidate{}, err
	}
	actualHashes := make([]string, len(candidate.Evidence))
	for index := range candidate.Evidence {
		actualHashes[index] = candidate.Evidence[index].SemanticHash
	}
	sort.Strings(actualHashes)
	sort.Strings(evidenceHashes)
	if !reflect.DeepEqual(actualHashes, evidenceHashes) {
		return graphdomain.SemanticLinkCandidate{}, baselineChanged("candidate evidence hash projection is inconsistent")
	}
	if err := graphdomain.ValidateSemanticLinkCandidate(candidate); err != nil {
		return graphdomain.SemanticLinkCandidate{}, baselineChanged("candidate fingerprint or snapshot is invalid")
	}
	return candidate, nil
}

func validateRelationApplyBinding(candidate graphdomain.SemanticLinkCandidate, proposal approvedRelationProposal) error {
	if candidate.WorkspaceID != proposal.WorkspaceID || candidate.Status != graphdomain.SemanticLinkCandidateStatusProposalCreated ||
		candidate.ProposalID == nil || *candidate.ProposalID != proposal.ID ||
		proposal.change.TargetRefs[0].ID != candidate.ID || proposal.change.TargetRefs[0].Fingerprint != candidate.Fingerprint {
		return errors.New("candidate and proposal binding changed")
	}
	expected := changecontroldomain.KnowledgeChange{
		TargetRefs: []changecontroldomain.KnowledgeTargetRef{{
			Type: changecontroldomain.KnowledgeTargetRefRelationCandidate, ID: candidate.ID, Fingerprint: candidate.Fingerprint,
		}},
		BaseVersions: []changecontroldomain.KnowledgeBaseVersion{
			{NodeType: candidate.Source.Ref.Type, NodeID: candidate.Source.Ref.ID, Version: candidate.Source.Version},
			{NodeType: candidate.Target.Ref.Type, NodeID: candidate.Target.Ref.ID, Version: candidate.Target.Version},
		},
		ChangeSet:     proposal.change.ChangeSet,
		SchemaVersion: changecontroldomain.KnowledgeChangeSchemaVersion,
	}
	for _, evidence := range candidate.Evidence {
		expected.EvidenceRefs = append(expected.EvidenceRefs, changecontroldomain.KnowledgeEvidenceRef{CandidateEvidenceID: evidence.ID, SemanticHash: evidence.SemanticHash})
	}
	canonical, err := changecontroldomain.ValidateKnowledgeChange(expected)
	if err != nil || !reflect.DeepEqual(canonical, proposal.change) {
		return errors.New("candidate snapshot differs from approved typed revision")
	}
	return nil
}

type relationApplyEndpoint struct {
	Ref           domain.NodeRef
	Version       int64
	Lifecycle     string
	Applicability *domain.Applicability
}

func loadAndValidateRelationApplyEndpoints(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, baseVersions []changecontroldomain.KnowledgeBaseVersion) (domain.Applicability, error) {
	claims := make([]foundation.ID, 0, 2)
	topics := make([]foundation.ID, 0, 2)
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
	if len(claims) > 0 {
		rows, err := tx.Query(ctx, `
			SELECT id::text,version,status,applicability,applicability_schema_version,applicability_hash
			FROM core.claim
			WHERE workspace_id=$1 AND id=ANY($2::uuid[])
			ORDER BY id FOR SHARE`, string(workspaceID), idsAsStrings(claims))
		if err != nil {
			return domain.Applicability{}, err
		}
		for rows.Next() {
			var id, lifecycle, schemaVersion, hash string
			var version int64
			var raw []byte
			if err := rows.Scan(&id, &version, &lifecycle, &raw, &schemaVersion, &hash); err != nil {
				rows.Close()
				return domain.Applicability{}, err
			}
			applicability, err := storedApplicability(raw, schemaVersion, hash)
			if err != nil {
				rows.Close()
				return domain.Applicability{}, baselineChanged("relation endpoint applicability snapshot is invalid")
			}
			ref := domain.NodeRef{Type: domain.NodeTypeClaim, ID: foundation.ID(id)}
			snapshots[relationApplyNodeKey(ref)] = relationApplyEndpoint{Ref: ref, Version: version, Lifecycle: lifecycle, Applicability: &applicability}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return domain.Applicability{}, err
		}
		rows.Close()
	}
	if len(topics) > 0 {
		rows, err := tx.Query(ctx, `
			SELECT id::text,version,status
			FROM core.topic
			WHERE workspace_id=$1 AND id=ANY($2::uuid[])
			ORDER BY id FOR SHARE`, string(workspaceID), idsAsStrings(topics))
		if err != nil {
			return domain.Applicability{}, err
		}
		for rows.Next() {
			var id, lifecycle string
			var version int64
			if err := rows.Scan(&id, &version, &lifecycle); err != nil {
				rows.Close()
				return domain.Applicability{}, err
			}
			ref := domain.NodeRef{Type: domain.NodeTypeTopic, ID: foundation.ID(id)}
			snapshots[relationApplyNodeKey(ref)] = relationApplyEndpoint{Ref: ref, Version: version, Lifecycle: lifecycle}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return domain.Applicability{}, err
		}
		rows.Close()
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
	// Topic 没有独立 Applicability；空 object 是 Knowledge 定义的无附加条件，
	// 不是由 Candidate 文本推测出的条件。
	return domain.ParseApplicability(json.RawMessage(`{}`))
}

func validateRelationApplyProvenance(ctx context.Context, tx pgx.Tx, candidate graphdomain.SemanticLinkCandidate) error {
	var count int
	var reachable bool
	err := tx.QueryRow(ctx, `
		SELECT count(*)::int,COALESCE(bool_and(core.knowledge_validate_provenance_binding(
			e.workspace_id,e.source_version_id,e.source_span_id
		)),false)
		FROM graph.semantic_link_candidate_evidence e
		WHERE e.workspace_id=$1 AND e.candidate_id=$2`, string(candidate.WorkspaceID), string(candidate.ID)).Scan(&count, &reachable)
	if err != nil {
		return err
	}
	if count != len(candidate.Evidence) || !reachable {
		return baselineChanged("candidate evidence provenance is no longer reachable")
	}
	return nil
}

func (repository *ApprovedRelationApplyRepository) buildConfirmedRelationEvidence(
	candidate graphdomain.SemanticLinkCandidate,
	relationID foundation.ID,
	confirmation domain.Confirmation,
	applicability domain.Applicability,
	at time.Time,
) ([]domain.RelationEvidence, error) {
	result := make([]domain.RelationEvidence, len(candidate.Evidence))
	var modelRunRef *string
	if candidate.Generation.ModelRunID != nil {
		value := string(*candidate.Generation.ModelRunID)
		modelRunRef = &value
	}
	for index, candidateEvidence := range candidate.Evidence {
		id, err := repository.ids.New()
		if err != nil {
			return nil, err
		}
		item := domain.RelationEvidence{
			ID: id, WorkspaceID: candidate.WorkspaceID, RelationID: relationID,
			Provenance: candidateEvidence.Provenance, Reason: candidateEvidence.Reason,
			Applicability: applicability, ModelRunRef: modelRunRef,
			Confirmation: &confirmation, CreatedAt: at,
		}
		item.EvidenceHash = domain.ComputeRelationEvidenceHash(item)
		if err := domain.ValidateRelationEvidence(item); err != nil {
			return nil, relationApplyConsistency(err)
		}
		result[index] = item
	}
	return result, nil
}

func (repository *ApprovedRelationApplyRepository) replayApprovedRelationApply(ctx context.Context, tx pgx.Tx, command knowledgeapplication.ApprovedRelationApplyCommand, requestHash string, proposal approvedRelationProposal, receipt *commandReceipt) (knowledgeapplication.ApprovedRelationApplyResult, error) {
	if receipt.RequestHash != requestHash || receipt.CommandType != commandConfirmRelation || receipt.AggregateType != aggregateRelation {
		return knowledgeapplication.ApprovedRelationApplyResult{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeIdempotencyConflict, false, errors.New("relation apply idempotency binding differs"))
	}
	result, err := loadRelationResult(ctx, tx, command.WorkspaceID, receipt.AggregateID)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(err)
	}
	if err := validateRelationApplyReplayFact(command, proposal, receipt, result.Relation); err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, relationApplyConsistency(err)
	}
	if !isNilRelationApplyDependency(repository.events) {
		request := changecontroleventcontract.ProposalStatusRequest(
			command.WorkspaceID, proposal.ID, command.ApprovalID,
			changecontroleventcontract.ProposalAppliedEventType, string(changecontroldomain.StatusApplied), proposal.Version, proposal.UpdatedAt,
		)
		if _, _, err := repository.events.AppendTx(ctx, tx, request); err != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, err
		}
	}
	return knowledgeapplication.ApprovedRelationApplyResult{
		Relation: result.Relation, Evidence: result.Evidence,
		ProposalStatus: proposal.Status, ProposalVersion: proposal.Version,
		Replayed: true,
	}, nil
}

func validateRelationApplyReplayFact(command knowledgeapplication.ApprovedRelationApplyCommand, proposal approvedRelationProposal, receipt *commandReceipt, relation domain.Relation) error {
	if proposal.Status != changecontroldomain.StatusApplied {
		return errors.New("relation apply receipt and proposal state are inconsistent")
	}
	if receipt == nil || relation.ID != receipt.AggregateID || relation.Version != receipt.AggregateVersion || relation.Confirmation == nil ||
		relation.Confirmation.Method != domain.ConfirmationUserApproval || relation.Confirmation.Reference != string(command.ApprovalID) ||
		relation.WorkspaceID != proposal.WorkspaceID || relation.Source != proposal.change.ChangeSet.Source ||
		relation.Target != proposal.change.ChangeSet.Target || relation.Type != proposal.change.ChangeSet.RelationType {
		return errors.New("relation apply receipt points to a different relation fact")
	}
	return nil
}

func lockRelationApplyReceipt(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, idempotencyKey string) error {
	var persistedWorkspace string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM core.workspace WHERE id=$1 FOR KEY SHARE`, string(workspaceID)).Scan(&persistedWorkspace); errors.Is(err, pgx.ErrNoRows) {
		return relationApplyNotFound(err)
	} else if err != nil {
		return relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_WORKSPACE_QUERY_FAILED")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || chr(31) || $2, 0))`, string(workspaceID), idempotencyKey); err != nil {
		return relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_RECEIPT_LOCK_FAILED")
	}
	return nil
}

func transitionRelationApplyProposal(ctx context.Context, tx pgx.Tx, proposalID foundation.ID, expectedVersion int64, from, to changecontroldomain.ProposalStatus, at time.Time) (int64, error) {
	if err := changecontroldomain.ValidateProposalTransition(from, to); err != nil {
		return 0, relationApplyConsistency(err)
	}
	var version int64
	err := tx.QueryRow(ctx, `
		UPDATE change_control.proposal
		SET status=$1,updated_at=$2,version=version+1
		WHERE id=$3 AND status=$4 AND version=$5
		RETURNING version`, string(to), at.UTC(), string(proposalID), string(from), expectedVersion).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, foundation.NewError(foundation.ErrorVersionConflict, "RELATION_PROPOSAL_APPLY_STATE_CONFLICT", false, err)
	}
	if err != nil {
		return 0, relationApplyClassify(err, "RELATION_PROPOSAL_APPLY_STATE_UPDATE_FAILED")
	}
	return version, nil
}

func (repository *ApprovedRelationApplyRepository) markNeedsRevision(ctx context.Context, tx pgx.Tx, proposal approvedRelationProposal, reason string) (knowledgeapplication.ApprovedRelationApplyResult, error) {
	return repository.markNeedsRevisionAtVersion(ctx, tx, proposal, proposal.Version, proposal.Status, reason)
}

func (repository *ApprovedRelationApplyRepository) markNeedsRevisionAtVersion(ctx context.Context, tx pgx.Tx, proposal approvedRelationProposal, version int64, status changecontroldomain.ProposalStatus, reason string) (knowledgeapplication.ApprovedRelationApplyResult, error) {
	at := latestRelationApplyTime(repository.clock.Now().UTC(), proposal.UpdatedAt, proposal.Approval.DecidedAt)
	newVersion, err := transitionRelationApplyProposal(ctx, tx, proposal.ID, version, status, changecontroldomain.StatusNeedsRevision, at)
	if err != nil {
		return knowledgeapplication.ApprovedRelationApplyResult{}, err
	}
	if !isNilRelationApplyDependency(repository.events) {
		request := changecontroleventcontract.ProposalStatusRequest(
			proposal.WorkspaceID, proposal.ID, proposal.Approval.ID,
			changecontroleventcontract.ProposalNeedsRevisionEventType, string(changecontroldomain.StatusNeedsRevision), newVersion, at,
		)
		if _, _, err := repository.events.AppendTx(ctx, tx, request); err != nil {
			return knowledgeapplication.ApprovedRelationApplyResult{}, err
		}
	}
	return knowledgeapplication.ApprovedRelationApplyResult{}, &relationApplyNeedsRevision{reason: reason}
}

type relationApplyNeedsRevision struct{ reason string }

func (err *relationApplyNeedsRevision) Error() string {
	if err == nil {
		return "relation proposal needs revision"
	}
	return "relation proposal needs revision: " + err.reason
}

type relationApplyBaselineError struct{ reason string }

func (err *relationApplyBaselineError) Error() string {
	if err == nil {
		return "relation proposal baseline changed"
	}
	return err.reason
}

func baselineChanged(reason string) error { return &relationApplyBaselineError{reason: reason} }

func relationApplyNodeKey(ref domain.NodeRef) string {
	return string(ref.Type) + "\x00" + string(ref.ID)
}

func relationApplyLifecycleActive(nodeType domain.NodeType, lifecycle string) bool {
	if nodeType == domain.NodeTypeTopic {
		return lifecycle == string(domain.TopicStatusActive)
	}
	return lifecycle == string(domain.ClaimStatusSuggested) || lifecycle == string(domain.ClaimStatusConfirmed) || lifecycle == string(domain.ClaimStatusDisputed)
}

func latestRelationApplyTime(values ...time.Time) time.Time {
	var latest time.Time
	for _, value := range values {
		if value.After(latest) {
			latest = value.UTC()
		}
	}
	return latest
}

func relationVersionBeforeConfirm(existing *domain.RelationResult) int64 {
	if existing == nil {
		return 1
	}
	return existing.Relation.Version
}

func validateReusableSuggestedRelation(existing domain.RelationResult, workspaceID foundation.ID, changeSet changecontroldomain.KnowledgeChangeSet, fingerprint string) error {
	if existing.Relation.Status != domain.RelationStatusSuggested ||
		existing.Relation.WorkspaceID != workspaceID ||
		existing.Relation.Source != changeSet.Source ||
		existing.Relation.Target != changeSet.Target ||
		existing.Relation.Type != changeSet.RelationType ||
		existing.Relation.Fingerprint != fingerprint {
		return errors.New("canonical relation is not a reusable suggested relation")
	}
	return nil
}

func isNilRelationApplyDependency(value any) bool {
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

func relationApplyInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "RELATION_PROPOSAL_APPLY_INVALID", false, cause)
}

func relationApplyNotFound(cause error) error {
	return foundation.NewError(foundation.ErrorNotFound, "RELATION_PROPOSAL_APPLY_NOT_FOUND", false, cause)
}

func relationApplyApprovalRequired(cause error) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, "RELATION_PROPOSAL_APPROVAL_REQUIRED", false, cause)
}

func relationApplyConsistency(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "RELATION_PROPOSAL_APPLY_CONSISTENCY", false, cause)
}

func relationApplyUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "RELATION_PROPOSAL_APPLY_UNAVAILABLE", true, cause)
}

func relationApplyClassify(err error, code string) error {
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
