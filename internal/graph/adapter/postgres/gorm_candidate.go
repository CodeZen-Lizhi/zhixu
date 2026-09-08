package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	"gorm.io/gorm"
)

// UpsertSemanticLinkCandidate persists one candidate evaluation in a
// GORM-owned transaction. The existing private SQL/scanner helpers remain the
// single source of truth for candidate validation and projection.
func (repository *GORMRepository) UpsertSemanticLinkCandidate(ctx context.Context, candidate graphdomain.SemanticLinkCandidate) (SemanticLinkCandidateUpsertResult, error) {
	if err := repository.ready(ctx); err != nil {
		return SemanticLinkCandidateUpsertResult{}, err
	}
	if err := graphdomain.ValidateSemanticLinkCandidate(candidate); err != nil {
		return SemanticLinkCandidateUpsertResult{}, err
	}
	if candidate.Status != graphdomain.SemanticLinkCandidateStatusActive && candidate.Status != graphdomain.SemanticLinkCandidateStatusDeferred {
		return SemanticLinkCandidateUpsertResult{}, candidateInvalid("candidate evaluation must start active or deferred")
	}

	var result SemanticLinkCandidateUpsertResult
	err := repository.within(ctx, foundation.TransactionOptions{}, gormCandidateClassify, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		existing, err := loadCandidate(callbackCtx, database, candidate.WorkspaceID, candidate.Fingerprint, "fingerprint", true)
		if err == nil {
			result = SemanticLinkCandidateUpsertResult{
				Candidate:  existing.candidate,
				Suppressed: isSuppressed(existing.candidate),
			}
			return nil
		}
		if !gormGraphNoRows(err) {
			return err
		}
		if err := validateCandidateEndpoints(callbackCtx, database, candidate); err != nil {
			return err
		}

		candidate.ReopenedFromCandidateID, candidate.ReopenedReason, err = findReopenSource(callbackCtx, database, candidate)
		if err != nil {
			return err
		}
		if candidate.ReopenedFromCandidateID == nil {
			candidate.ReopenedReason = ""
		}
		if err := graphdomain.ValidateSemanticLinkCandidate(candidate); err != nil {
			return err
		}

		inserted, insertedOK, err := gormInsertCandidate(callbackCtx, database, candidate)
		if err != nil {
			return err
		}
		if !insertedOK {
			winner, err := loadCandidate(callbackCtx, database, candidate.WorkspaceID, candidate.Fingerprint, "fingerprint", true)
			if err != nil {
				return err
			}
			result = SemanticLinkCandidateUpsertResult{
				Candidate:  winner.candidate,
				Suppressed: isSuppressed(winner.candidate),
			}
			return nil
		}
		if err := insertGORMCandidateEvidence(callbackCtx, database, candidate); err != nil {
			return err
		}
		if err := supersedePriorCandidates(callbackCtx, database, candidate); err != nil {
			return err
		}
		inserted.candidate.Evidence = append([]graphdomain.SemanticLinkCandidateEvidence(nil), candidate.Evidence...)
		result = SemanticLinkCandidateUpsertResult{
			Candidate: inserted.candidate,
			Created:   true,
			Reopened:  candidate.ReopenedFromCandidateID != nil,
		}
		return nil
	})
	if err != nil {
		return SemanticLinkCandidateUpsertResult{}, err
	}
	return result, nil
}

// ListSemanticLinkCandidates returns a stable bounded candidate page from one
// repeatable-read/read-only snapshot and hydrates all evidence in one query.
func (repository *GORMRepository) ListSemanticLinkCandidates(ctx context.Context, request graphdomain.SemanticLinkCandidateQuery) (graphdomain.SemanticLinkCandidatePage, error) {
	if err := repository.ready(ctx); err != nil {
		return graphdomain.SemanticLinkCandidatePage{}, err
	}
	if err := graphdomain.ValidateSemanticLinkCandidateQuery(request); err != nil {
		return graphdomain.SemanticLinkCandidatePage{}, err
	}

	var page graphdomain.SemanticLinkCandidatePage
	err := repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, database *gorm.DB) error {
		bases, truncated, err := queryCandidateWindow(callbackCtx, database, request, request.Limit)
		if err != nil {
			return gormCandidateClassify(callbackCtx, err, gormGraphTransactionStageCallback)
		}
		items, err := hydrateCandidateBatchCandidates(callbackCtx, database, bases)
		if err != nil {
			return gormCandidateClassify(callbackCtx, err, gormGraphTransactionStageCallback)
		}
		page = graphdomain.SemanticLinkCandidatePage{
			WorkspaceID: request.WorkspaceID,
			Items:       items,
			Meta:        graphdomain.PageMeta{Fingerprint: candidatePageFingerprint(items), Complete: !truncated},
		}
		if truncated {
			page.Meta.Truncated = true
			page.Meta.Reason = semanticLinkCandidateWindowReason
		}
		return graphdomain.ValidateSemanticLinkCandidatePage(request, page)
	})
	if err != nil {
		return graphdomain.SemanticLinkCandidatePage{}, err
	}
	return page, nil
}

// SemanticLinkCandidateWindow returns the full bounded result window used by
// the application-owned cursor codec.
func (repository *GORMRepository) SemanticLinkCandidateWindow(ctx context.Context, request graphdomain.SemanticLinkCandidateQuery) (graphapp.SemanticLinkCandidateResultWindow, error) {
	if err := repository.ready(ctx); err != nil {
		return graphapp.SemanticLinkCandidateResultWindow{}, err
	}
	if err := graphdomain.ValidateSemanticLinkCandidateQuery(request); err != nil {
		return graphapp.SemanticLinkCandidateResultWindow{}, err
	}

	var window graphapp.SemanticLinkCandidateResultWindow
	err := repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, database *gorm.DB) error {
		bases, truncated, err := queryCandidateWindow(callbackCtx, database, request, graphapp.MaxSemanticLinkCandidateWindow)
		if err != nil {
			return gormCandidateClassify(callbackCtx, err, gormGraphTransactionStageCallback)
		}
		items, err := hydrateCandidateBatchCandidates(callbackCtx, database, bases)
		if err != nil {
			return gormCandidateClassify(callbackCtx, err, gormGraphTransactionStageCallback)
		}
		window = graphapp.SemanticLinkCandidateResultWindow{Items: items, Truncated: truncated}
		if truncated {
			window.Reason = semanticLinkCandidateWindowReason
		}
		return validateCandidateWindow(request, window)
	})
	if err != nil {
		return graphapp.SemanticLinkCandidateResultWindow{}, err
	}
	return window, nil
}

// GetSemanticLinkCandidate reads one candidate and its complete evidence set
// from one repeatable-read/read-only snapshot.
func (repository *GORMRepository) GetSemanticLinkCandidate(ctx context.Context, workspaceID, candidateID foundation.ID) (graphdomain.SemanticLinkCandidate, error) {
	if err := repository.ready(ctx); err != nil {
		return graphdomain.SemanticLinkCandidate{}, err
	}
	if !validID(workspaceID) || !validID(candidateID) {
		return graphdomain.SemanticLinkCandidate{}, candidateInvalid("candidate lookup is invalid")
	}

	var candidate graphdomain.SemanticLinkCandidate
	err := repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, database *gorm.DB) error {
		base, err := loadCandidate(callbackCtx, database, workspaceID, candidateID, "id", false)
		if err != nil {
			return gormCandidateClassify(callbackCtx, err, gormGraphTransactionStageCallback)
		}
		candidate = base.candidate
		return nil
	})
	if err != nil {
		return graphdomain.SemanticLinkCandidate{}, err
	}
	return candidate, nil
}

// DecideSemanticLinkCandidate writes an ordinary append-only decision receipt
// before the candidate CAS. Confirm is routed through the separate scoped
// Change Control collaborator and does not use this method directly.
func (repository *GORMRepository) DecideSemanticLinkCandidate(ctx context.Context, command graphapp.SemanticLinkCandidateDecisionCommand, proposalID *foundation.ID) (graphapp.SemanticLinkCandidateDecisionResult, error) {
	if err := repository.ready(ctx); err != nil {
		return graphapp.SemanticLinkCandidateDecisionResult{}, err
	}
	canonical, err := graphapp.CanonicalizeSemanticLinkCandidateDecisionCommand(command)
	if err != nil {
		return graphapp.SemanticLinkCandidateDecisionResult{}, err
	}
	if err := validateDecisionProposal(canonical, proposalID); err != nil {
		return graphapp.SemanticLinkCandidateDecisionResult{}, err
	}
	requestHash, err := graphapp.ComputeSemanticLinkCandidateDecisionRequestHash(canonical)
	if err != nil {
		return graphapp.SemanticLinkCandidateDecisionResult{}, err
	}

	var result graphapp.SemanticLinkCandidateDecisionResult
	err = repository.within(ctx, foundation.TransactionOptions{}, gormCandidateClassify, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		if receipt, found, err := loadDecisionByIdempotency(callbackCtx, database, canonical.WorkspaceID, canonical.IdempotencyKey); err != nil {
			return err
		} else if found {
			base, err := loadCandidate(callbackCtx, database, canonical.WorkspaceID, canonical.CandidateID, "id", false)
			if err != nil {
				return err
			}
			result, err = replayCandidateDecision(canonical, requestHash, proposalID, receipt, base.candidate)
			return err
		}

		base, err := loadCandidate(callbackCtx, database, canonical.WorkspaceID, canonical.CandidateID, "id", true)
		if err != nil {
			return err
		}
		if base.candidate.Version != canonical.ExpectedVersion {
			// Re-read only after holding the Candidate lock so a concurrent
			// receipt winner cannot be misclassified as a stale command.
			if receipt, found, receiptErr := loadDecisionByIdempotency(callbackCtx, database, canonical.WorkspaceID, canonical.IdempotencyKey); receiptErr != nil {
				return receiptErr
			} else if found {
				result, err = replayCandidateDecision(canonical, requestHash, proposalID, receipt, base.candidate)
				return err
			}
			return candidateVersionConflict("candidate decision expected version mismatch")
		}
		if err := graphdomain.ValidateSemanticLinkCandidateDecision(base.candidate, canonical.Decision); err != nil {
			return err
		}
		if proposalID != nil {
			if err := validateKnowledgeProposalBinding(callbackCtx, database, canonical.WorkspaceID, *proposalID); err != nil {
				return err
			}
		}

		now, err := candidateDatabaseNow(callbackCtx, database)
		if err != nil {
			return err
		}
		if now.Before(base.candidate.UpdatedAt) {
			now = base.candidate.UpdatedAt
		}
		decisionID, err := foundation.NewUUIDGenerator(nil).New()
		if err != nil {
			return err
		}
		receipt := candidateDecisionRow{
			id: decisionID, workspaceID: canonical.WorkspaceID, candidateID: canonical.CandidateID,
			candidateVersion: canonical.ExpectedVersion, proposalID: proposalID,
			idempotencyKey: canonical.IdempotencyKey, requestHash: requestHash,
			action: canonical.Decision.Action, relationType: canonical.Decision.RelationType,
			deferUntil: canonical.Decision.ResumeAfter, reason: candidateDecisionReason(canonical.Decision.Reason), createdAt: now,
		}
		inserted, err := insertDecision(callbackCtx, database, receipt)
		if err != nil {
			return err
		}
		if !inserted {
			winner, found, err := loadDecisionByIdempotency(callbackCtx, database, canonical.WorkspaceID, canonical.IdempotencyKey)
			if err != nil {
				return err
			}
			if !found {
				return candidateVersionConflict("candidate decision receipt was concurrently rebound")
			}
			result, err = replayCandidateDecision(canonical, requestHash, proposalID, winner, base.candidate)
			return err
		}

		nextStatus, err := graphdomain.NextSemanticLinkCandidateStatus(base.candidate.Status, canonical.Decision)
		if err != nil {
			return err
		}
		var nextProposal any
		if proposalID != nil {
			nextProposal = string(*proposalID)
		}
		var deferUntil any
		if canonical.Decision.ResumeAfter != nil {
			deferUntil = canonical.Decision.ResumeAfter.UTC()
		}
		rowsAffected, err := gormGraphExec(callbackCtx, database, `
			UPDATE graph.semantic_link_candidate
			SET status=(@p3),current_proposal_id=(@p4),deferred_until=(@p5),version=version+1,updated_at=(@p6)
			WHERE workspace_id=(@p1) AND id=(@p2) AND version=(@p7)`,
			sql.Named("p1", string(canonical.WorkspaceID)), sql.Named("p2", string(canonical.CandidateID)), sql.Named("p3", string(nextStatus)), sql.Named("p4", nextProposal), sql.Named("p5", deferUntil), sql.Named("p6", now), sql.Named("p7", canonical.ExpectedVersion))
		if err != nil {
			return err
		}
		if rowsAffected != 1 {
			return candidateVersionConflict("candidate decision CAS was lost")
		}

		resultCandidate, err := candidateAtDecision(base.candidate, receipt)
		if err != nil {
			return err
		}
		resultCandidate.Status = nextStatus
		resultCandidate.Version = canonical.ExpectedVersion + 1
		resultCandidate.UpdatedAt = now
		if nextStatus == graphdomain.SemanticLinkCandidateStatusProposalCreated {
			resultCandidate.ProposalID = proposalID
		} else {
			resultCandidate.ProposalID = nil
		}
		if nextStatus == graphdomain.SemanticLinkCandidateStatusDeferred {
			resultCandidate.ResumeAfter = canonical.Decision.ResumeAfter
		} else {
			resultCandidate.ResumeAfter = nil
		}
		result = graphapp.SemanticLinkCandidateDecisionResult{
			Candidate: resultCandidate, ProposalID: resultCandidate.ProposalID,
			DecisionID: receipt.id, DecidedAt: receipt.createdAt,
		}
		return graphapp.ValidateSemanticLinkCandidateDecisionResult(canonical, result)
	})
	if err != nil {
		return graphapp.SemanticLinkCandidateDecisionResult{}, err
	}
	return result, nil
}

var _ graphapp.SemanticLinkCandidateRepository = (*GORMRepository)(nil)

func gormCandidateClassify(ctx context.Context, err error, _ gormGraphTransactionStage) error {
	if err == nil {
		return nil
	}
	if cause := graphGORMContextCause(ctx, err); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, graphdomain.ErrorCodeQueryCanceled, false, cause)
		}
		return foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeQueryTimeout, true, cause)
	}
	return classifyGORM(ctx, err)
}
