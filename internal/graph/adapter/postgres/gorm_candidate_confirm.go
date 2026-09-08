package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/graph/candidateconfirm"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMCandidateConfirmRepository owns the Graph transaction that atomically
// binds a Candidate decision to a Change Control knowledge Proposal.
type GORMCandidateConfirmRepository struct {
	repository *GORMRepository
	proposals  candidateconfirm.ScopedKnowledgeProposalPort
	ids        foundation.IDGenerator
	clock      foundation.Clock
}

var _ candidateconfirm.Port = (*GORMCandidateConfirmRepository)(nil)

// NewGORMCandidateConfirmRepository derives its database boundary from one
// platform Pool. The Proposal collaborator only consumes the resulting scope.
func NewGORMCandidateConfirmRepository(
	pool *platformpostgres.Pool,
	proposals candidateconfirm.ScopedKnowledgeProposalPort,
	ids foundation.IDGenerator,
	clock foundation.Clock,
) (*GORMCandidateConfirmRepository, error) {
	if pool == nil || isNilCandidateConfirmDependency(proposals) || isNilCandidateConfirmDependency(ids) || isNilCandidateConfirmDependency(clock) {
		return nil, candidateConfirmDependencyMissing(errors.New("candidate confirm GORM dependency is missing"))
	}
	repository, err := NewGORMRepository(pool)
	if err != nil {
		return nil, candidateConfirmDependencyMissing(err)
	}
	return &GORMCandidateConfirmRepository{
		repository: repository,
		proposals:  proposals,
		ids:        ids,
		clock:      clock,
	}, nil
}

// Confirm creates or exactly replays the Proposal, Decision receipt and
// Candidate transition in one Graph-owned UnitOfWork.
func (repository *GORMCandidateConfirmRepository) Confirm(ctx context.Context, command candidateconfirm.Command) (candidateconfirm.Result, error) {
	if repository == nil || repository.repository == nil || isNilCandidateConfirmDependency(repository.proposals) || isNilCandidateConfirmDependency(repository.ids) || isNilCandidateConfirmDependency(repository.clock) {
		return candidateconfirm.Result{}, candidateConfirmUnavailable(errors.New("candidate confirm GORM repository is unavailable"))
	}
	if ctx == nil {
		return candidateconfirm.Result{}, candidateConfirmInvalid(errors.New("candidate confirm context is nil"))
	}
	if err := repository.repository.ready(ctx); err != nil {
		return candidateconfirm.Result{}, candidateConfirmUnavailable(err)
	}

	canonical, err := candidateconfirm.Canonicalize(command)
	allowCreate := true
	if err != nil {
		if command.RiskLevel != "" {
			return candidateconfirm.Result{}, err
		}
		canonical, err = candidateconfirm.CanonicalizeLegacy(command)
		if err != nil {
			return candidateconfirm.Result{}, err
		}
		allowCreate = false
	}
	requestHash := ""
	if allowCreate {
		requestHash, err = candidateconfirm.RequestHash(canonical)
		if err != nil {
			return candidateconfirm.Result{}, candidateConfirmInvalid(err)
		}
	}
	legacyRequestHash, err := candidateconfirm.LegacyRequestHash(canonical)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmInvalid(err)
	}

	var result candidateconfirm.Result
	err = repository.repository.within(
		ctx,
		foundation.TransactionOptions{},
		candidateConfirmTransactionClassifier,
		func(callbackCtx context.Context, scope foundation.TransactionScope, transaction *gorm.DB) error {
			var confirmErr error
			result, confirmErr = repository.confirmScoped(callbackCtx, scope, transaction, canonical, requestHash, legacyRequestHash, allowCreate)
			return confirmErr
		},
	)
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	return result, nil
}

func (repository *GORMCandidateConfirmRepository) confirmScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	transaction *gorm.DB,
	command candidateconfirm.Command,
	requestHash string,
	legacyRequestHash string,
	allowCreate bool,
) (candidateconfirm.Result, error) {
	receipt, found, err := gormCandidateConfirmReceipt(ctx, transaction, command.WorkspaceID, command.IdempotencyKey)
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	if found {
		return repository.replayScoped(ctx, scope, transaction, command, requestHash, legacyRequestHash, receipt)
	}
	if !allowCreate {
		return candidateconfirm.Result{}, candidateConfirmInvalid(errors.New("new candidate confirmation requires HIGH risk level"))
	}

	base, err := loadCandidate(ctx, transaction, command.WorkspaceID, command.CandidateID, "id", true)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmGraphOperationError(ctx, err, "RELATION_PROPOSAL_CONFIRM_CANDIDATE_QUERY_FAILED")
	}
	if base.candidate.Version != command.ExpectedVersion {
		winner, winnerFound, receiptErr := gormCandidateConfirmReceipt(ctx, transaction, command.WorkspaceID, command.IdempotencyKey)
		if receiptErr != nil {
			return candidateconfirm.Result{}, receiptErr
		}
		if winnerFound {
			return repository.replayScoped(ctx, scope, transaction, command, requestHash, legacyRequestHash, winner)
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
	parsedRiskLevel, err := changecontroldomain.ValidateProposalRiskLevelForType(changecontroldomain.ProposalTypeKnowledgeChange, command.RiskLevel)
	if err != nil || parsedRiskLevel != candidateconfirm.ProposalRiskLevel {
		return candidateconfirm.Result{}, candidateConfirmInvalid(errors.New("confirmation risk level must be HIGH"))
	}
	changeHash, err := changecontroldomain.ComputeKnowledgeChangeHash(change, command.Risk, command.RollbackPlan)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmInvalid(err)
	}
	proposalRequestHash, err := changecontroldomain.ComputeKnowledgeChangeRequestHashWithRiskLevel(command.WorkspaceID, change, command.RiskLevel, command.Risk, command.RollbackPlan)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmInvalid(err)
	}

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
		RiskLevel: command.RiskLevel, IdempotencyKey: "semantic-link-confirm/v2:" + requestHash,
		RequestHash: proposalRequestHash, Status: changecontroldomain.StatusReady, Version: 1,
		CurrentRevisionID: revisionID, CreatedAt: now, UpdatedAt: now,
		Revision: changecontroldomain.Revision{
			ID: revisionID, ProposalID: proposalID, RevisionNo: 1, Risk: command.Risk,
			RollbackPlan: command.RollbackPlan, ChangeHash: changeHash, KnowledgeChange: &change, CreatedAt: now,
		},
	}
	if err := changecontroldomain.ValidateProposalRevisionForType(changecontroldomain.ProposalTypeKnowledgeChange, proposal.Revision); err != nil {
		return candidateconfirm.Result{}, candidateConfirmInvalid(err)
	}

	proposal, err = repository.proposals.CreateKnowledgeChangeProposalScoped(ctx, scope, proposal)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmCollaboratorError(ctx, err, "RELATION_PROPOSAL_CONFIRM_PROPOSAL_CREATE_FAILED")
	}
	if err := validateCandidateConfirmProposalBinding(base.candidate, command, requestHash, proposal, false); err != nil {
		return candidateconfirm.Result{}, err
	}
	proposalPointer := proposal.ID
	receipt = candidateDecisionRow{
		id: decisionID, workspaceID: command.WorkspaceID, candidateID: command.CandidateID,
		candidateVersion: command.ExpectedVersion, proposalID: &proposalPointer,
		idempotencyKey: command.IdempotencyKey, requestHash: requestHash,
		action: command.Action, relationType: command.RelationType, createdAt: now,
	}
	inserted, err := insertDecision(ctx, transaction, receipt)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmGraphOperationError(ctx, err, "RELATION_PROPOSAL_CONFIRM_DECISION_CREATE_FAILED")
	}
	if !inserted {
		winner, winnerFound, lookupErr := gormCandidateConfirmReceipt(ctx, transaction, command.WorkspaceID, command.IdempotencyKey)
		if lookupErr != nil {
			return candidateconfirm.Result{}, lookupErr
		}
		if !winnerFound {
			return candidateconfirm.Result{}, candidateConfirmConsistency(errors.New("candidate confirmation receipt disappeared"))
		}
		return repository.replayScoped(ctx, scope, transaction, command, requestHash, legacyRequestHash, winner)
	}

	changed, err := gormGraphExec(ctx, transaction, `
		UPDATE graph.semantic_link_candidate
		SET status='PROPOSAL_CREATED',current_proposal_id=(@p3),deferred_until=NULL,version=version+1,updated_at=(@p4)
		WHERE workspace_id=(@p1) AND id=(@p2) AND version=(@p5) AND current_proposal_id IS NULL`,
		sql.Named("p1", string(command.WorkspaceID)), sql.Named("p2", string(command.CandidateID)), sql.Named("p3", string(proposal.ID)), sql.Named("p4", now), sql.Named("p5", command.ExpectedVersion))
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmGORMClassify(ctx, err, "RELATION_PROPOSAL_CONFIRM_CANDIDATE_UPDATE_FAILED")
	}
	if changed != 1 {
		return candidateconfirm.Result{}, candidateConfirmVersionConflict("candidate confirmation CAS was lost")
	}

	resultCandidate, err := candidateAtDecision(base.candidate, receipt)
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	return candidateconfirm.Result{Candidate: resultCandidate, Proposal: proposal, DecisionID: decisionID}, nil
}

func (repository *GORMCandidateConfirmRepository) replayScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	transaction *gorm.DB,
	command candidateconfirm.Command,
	requestHash string,
	legacyRequestHash string,
	receipt candidateDecisionRow,
) (candidateconfirm.Result, error) {
	legacy := receipt.requestHash == legacyRequestHash
	if (receipt.requestHash != requestHash && !legacy) || receipt.candidateID != command.CandidateID || receipt.workspaceID != command.WorkspaceID || receipt.proposalID == nil || receipt.action != command.Action || !sameOptionalRelationType(receipt.relationType, command.RelationType) {
		return candidateconfirm.Result{}, candidateConfirmVersionConflict("candidate confirmation idempotency binding differs")
	}
	current, err := loadCandidate(ctx, transaction, command.WorkspaceID, command.CandidateID, "id", true)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmGraphOperationError(ctx, err, "RELATION_PROPOSAL_CONFIRM_CANDIDATE_QUERY_FAILED")
	}
	resultCandidate, err := candidateAtDecision(current.candidate, receipt)
	if err != nil {
		return candidateconfirm.Result{}, err
	}
	proposal, err := repository.proposals.GetInitialKnowledgeChangeProposalScoped(ctx, scope, command.WorkspaceID, *receipt.proposalID)
	if err != nil {
		return candidateconfirm.Result{}, candidateConfirmCollaboratorError(ctx, err, "RELATION_PROPOSAL_CONFIRM_PROPOSAL_QUERY_FAILED")
	}
	if err := validateCandidateConfirmProposalBinding(current.candidate, command, receipt.requestHash, proposal, legacy); err != nil {
		return candidateconfirm.Result{}, err
	}
	resultCandidate.ProposalID = receipt.proposalID
	resultCandidate.Status = graphdomain.SemanticLinkCandidateStatusProposalCreated
	if err := graphdomain.ValidateSemanticLinkCandidate(resultCandidate); err != nil {
		return candidateconfirm.Result{}, candidateConfirmConsistency(err)
	}
	return candidateconfirm.Result{Candidate: resultCandidate, Proposal: proposal, DecisionID: receipt.id, Replayed: true}, nil
}

func gormCandidateConfirmReceipt(ctx context.Context, transaction *gorm.DB, workspaceID foundation.ID, key string) (candidateDecisionRow, bool, error) {
	receipt, found, err := loadDecisionByIdempotency(ctx, transaction, workspaceID, key)
	if err != nil {
		return candidateDecisionRow{}, false, candidateConfirmGraphOperationError(ctx, err, "RELATION_PROPOSAL_CONFIRM_RECEIPT_QUERY_FAILED")
	}
	if !found {
		return candidateDecisionRow{}, false, nil
	}
	if !validID(receipt.id) || !validID(receipt.candidateID) || !validID(receipt.workspaceID) ||
		receipt.workspaceID != workspaceID || receipt.idempotencyKey != key || receipt.candidateVersion < 1 ||
		!changecontroldomain.ValidHash(receipt.requestHash) || receipt.requestHash != strings.ToLower(receipt.requestHash) || receipt.createdAt.IsZero() {
		return candidateDecisionRow{}, false, candidateConfirmConsistency(errors.New("candidate confirmation receipt is invalid"))
	}
	if receipt.proposalID != nil && !validID(*receipt.proposalID) {
		return candidateDecisionRow{}, false, candidateConfirmConsistency(errors.New("candidate confirmation proposal receipt binding is invalid"))
	}
	return receipt, true, nil
}

func candidateConfirmTransactionClassifier(ctx context.Context, err error, stage gormGraphTransactionStage) error {
	code := "RELATION_PROPOSAL_CONFIRM_TRANSACTION_FAILED"
	if stage == gormGraphTransactionStageCommit {
		code = "RELATION_PROPOSAL_CONFIRM_COMMIT_FAILED"
	}
	return candidateConfirmGORMClassify(ctx, err, code)
}

func candidateConfirmGraphOperationError(ctx context.Context, err error, operationCode string) error {
	if err == nil {
		return nil
	}
	if cause := graphGORMContextCause(ctx, err); cause != nil || errors.Is(err, sql.ErrTxDone) {
		return candidateConfirmGORMClassify(ctx, err, operationCode)
	}
	if platformpostgres.SQLState(err) != "" {
		return candidateConfirmGORMClassify(ctx, err, operationCode)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return candidateConfirmGORMClassify(ctx, err, operationCode)
	}
	switch classified.Kind {
	case foundation.ErrorNotFound:
		return err
	case foundation.ErrorVersionConflict:
		return foundation.NewError(foundation.ErrorVersionConflict, "RELATION_PROPOSAL_CONFIRM_CONFLICT", false, err)
	case foundation.ErrorInvalidInput, foundation.ErrorPermissionDenied, foundation.ErrorConsistencyViolation, foundation.ErrorManualRecoveryRequired:
		return candidateConfirmConsistency(err)
	case foundation.ErrorRetryableFailure:
		return foundation.NewError(foundation.ErrorRetryableFailure, operationCode, true, err)
	case foundation.ErrorNonRetryableFailure:
		return foundation.NewError(foundation.ErrorNonRetryableFailure, operationCode, false, err)
	case foundation.ErrorDependencyUnavailable:
		return foundation.NewError(foundation.ErrorDependencyUnavailable, operationCode, classified.Retryable, err)
	default:
		return foundation.NewError(foundation.ErrorDependencyUnavailable, operationCode, true, err)
	}
}

func candidateConfirmCollaboratorError(ctx context.Context, err error, operationCode string) error {
	if err == nil {
		return nil
	}
	if cause := graphGORMContextCause(ctx, err); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, operationCode, false, cause)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, operationCode, true, cause)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return candidateConfirmGORMClassify(ctx, err, operationCode)
	}
	switch classified.Kind {
	case foundation.ErrorInvalidInput:
		return candidateConfirmInvalid(err)
	case foundation.ErrorVersionConflict:
		return foundation.NewError(foundation.ErrorVersionConflict, "RELATION_PROPOSAL_CONFIRM_CONFLICT", false, err)
	case foundation.ErrorNotFound, foundation.ErrorPermissionDenied, foundation.ErrorConsistencyViolation, foundation.ErrorManualRecoveryRequired:
		return candidateConfirmConsistency(err)
	case foundation.ErrorRetryableFailure:
		return foundation.NewError(foundation.ErrorRetryableFailure, operationCode, true, err)
	case foundation.ErrorNonRetryableFailure:
		return foundation.NewError(foundation.ErrorNonRetryableFailure, operationCode, false, err)
	case foundation.ErrorDependencyUnavailable:
		return candidateConfirmUnavailable(err)
	default:
		return candidateConfirmUnavailable(err)
	}
}

func candidateConfirmGORMClassify(ctx context.Context, err error, code string) error {
	if err == nil {
		return nil
	}
	if cause := graphGORMContextCause(ctx, err); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, cause)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, cause)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
	}
	switch platformpostgres.SQLState(err) {
	case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01":
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
	case "23503", "23514", "55000":
		return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
	case "23505":
		return foundation.NewError(foundation.ErrorVersionConflict, code, false, err)
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}

func candidateConfirmDependencyMissing(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "RELATION_PROPOSAL_CONFIRM_DEPENDENCY_MISSING", false, cause)
}
