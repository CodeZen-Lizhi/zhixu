package postgres

import (
	"context"
	"encoding/json"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	"gorm.io/gorm"
)

const (
	gormReviewInvalidationStatementPrefix = `
WITH input AS MATERIALIZED (
    SELECT ?::uuid AS workspace_id,
           ?::uuid AS claim_id,
           ?::uuid AS source_version_id,
           ?::uuid AS source_span_id,
           ?::integer AS candidate_limit
), targets AS MATERIALIZED (`
	gormReviewInvalidationByClaimTargets = `
    SELECT card.id
    FROM input
    JOIN learning.review_card AS card
      ON card.workspace_id=input.workspace_id
     AND card.claim_id=input.claim_id
    WHERE input.claim_id IS NOT NULL
      AND card.status='APPROVED'
    ORDER BY card.id
    LIMIT (SELECT candidate_limit FROM input)
    FOR UPDATE OF card`
	gormReviewInvalidationBySourceVersionTargets = `
    SELECT card.id
    FROM input
    JOIN learning.review_card_evidence_selector AS selector
      ON selector.workspace_id=input.workspace_id
     AND selector.selector_kind='SOURCE_VERSION'
     AND selector.selector_id=input.source_version_id
    JOIN learning.review_card AS card
      ON card.workspace_id=selector.workspace_id
     AND card.id=selector.card_id
    WHERE input.source_version_id IS NOT NULL
      AND card.status='APPROVED'
    ORDER BY selector.card_id
    LIMIT (SELECT candidate_limit FROM input)
    FOR UPDATE OF card`
	gormReviewInvalidationByClaimAndSourceVersionTargets = `
    SELECT card.id
    FROM input
    JOIN learning.review_card_evidence_selector AS selector
      ON selector.workspace_id=input.workspace_id
     AND selector.selector_kind='SOURCE_VERSION'
     AND selector.selector_id=input.source_version_id
     AND selector.claim_id=input.claim_id
    JOIN learning.review_card AS card
      ON card.workspace_id=selector.workspace_id
     AND card.id=selector.card_id
     AND card.claim_id=selector.claim_id
    WHERE input.claim_id IS NOT NULL
      AND input.source_version_id IS NOT NULL
      AND card.status='APPROVED'
    ORDER BY selector.card_id
    LIMIT (SELECT candidate_limit FROM input)
    FOR UPDATE OF card`
	gormReviewInvalidationBySourceSpanTargets = `
    SELECT card.id
    FROM input
    JOIN learning.review_card_evidence_selector AS span_selector
      ON span_selector.workspace_id=input.workspace_id
     AND span_selector.selector_kind='SOURCE_SPAN'
     AND span_selector.selector_id=input.source_span_id
    JOIN learning.review_card AS card
      ON card.workspace_id=span_selector.workspace_id
     AND card.id=span_selector.card_id
    WHERE input.source_span_id IS NOT NULL
      AND card.status='APPROVED'
      AND (input.source_version_id IS NULL OR EXISTS (
          SELECT 1
          FROM learning.review_card_evidence_selector AS version_selector
          WHERE version_selector.workspace_id=span_selector.workspace_id
            AND version_selector.card_id=span_selector.card_id
            AND version_selector.selector_kind='SOURCE_VERSION'
            AND version_selector.selector_id=input.source_version_id
      ))
    ORDER BY span_selector.card_id
    LIMIT (SELECT candidate_limit FROM input)
    FOR UPDATE OF card`
	gormReviewInvalidationByClaimAndSourceSpanTargets = `
    SELECT card.id
    FROM input
    JOIN learning.review_card_evidence_selector AS span_selector
      ON span_selector.workspace_id=input.workspace_id
     AND span_selector.selector_kind='SOURCE_SPAN'
     AND span_selector.selector_id=input.source_span_id
     AND span_selector.claim_id=input.claim_id
    JOIN learning.review_card AS card
      ON card.workspace_id=span_selector.workspace_id
     AND card.id=span_selector.card_id
     AND card.claim_id=span_selector.claim_id
    WHERE input.claim_id IS NOT NULL
      AND input.source_span_id IS NOT NULL
      AND card.status='APPROVED'
      AND (input.source_version_id IS NULL OR EXISTS (
          SELECT 1
          FROM learning.review_card_evidence_selector AS version_selector
          WHERE version_selector.workspace_id=span_selector.workspace_id
            AND version_selector.card_id=span_selector.card_id
            AND version_selector.claim_id=span_selector.claim_id
            AND version_selector.selector_kind='SOURCE_VERSION'
            AND version_selector.selector_id=input.source_version_id
      ))
    ORDER BY span_selector.card_id
    LIMIT (SELECT candidate_limit FROM input)
    FOR UPDATE OF card`
	gormReviewInvalidationStatementSuffix = `
), updated AS (
    UPDATE learning.review_card AS card
    SET status='INVALIDATED',
        invalidation_reason=?,
        invalidated_at=?,
        version=card.version+1,
        updated_at=?
    FROM (
        SELECT id
        FROM targets
        ORDER BY id
        LIMIT ?::integer
    ) AS batch
    WHERE card.workspace_id=?::uuid
      AND card.id=batch.id
      AND card.status='APPROVED'
    RETURNING card.id
)
SELECT count(*)::integer,
       (SELECT count(*) > ?::integer FROM targets)
FROM updated`
)

// InvalidateCards 持久化一批有界的知识生命周期失效。
func (repository *GORMRepository) InvalidateCards(ctx context.Context, record reviewapp.InvalidateCardsRecord) (reviewapp.InvalidationResult, error) {
	if err := validateInvalidationRecord(record); err != nil {
		return reviewapp.InvalidationResult{}, err
	}

	var result reviewapp.InvalidationResult
	callbackSucceeded := false
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormReviewLockWorkspace(callbackCtx, tx, record.WorkspaceID); err != nil {
			return err
		}
		replay, found, err := gormReviewLoadInvalidationCommand(
			callbackCtx,
			tx,
			record.WorkspaceID,
			record.IdempotencyKey,
			record.RequestHash,
		)
		if err != nil {
			return err
		}
		if found {
			result = replay
			callbackSucceeded = true
			return nil
		}

		at := record.At.UTC()
		query := gormReviewInvalidationStatementPrefix +
			gormReviewInvalidationTargetQuery(record) +
			gormReviewInvalidationStatementSuffix
		row, err := gormReviewRawRow(
			callbackCtx,
			tx,
			query,
			string(record.WorkspaceID),
			optionalID(record.ClaimID),
			optionalID(record.SourceVersionID),
			optionalID(record.SourceSpanID),
			record.BatchSize+1,
			record.Reason,
			at,
			at,
			record.BatchSize,
			string(record.WorkspaceID),
			record.BatchSize,
		)
		if err != nil {
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeDependencyUnavailable)
		}
		if err := row.Scan(&result.InvalidatedCount, &result.HasMore); err != nil {
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeDependencyUnavailable)
		}
		if !validInvalidationSummary(result.InvalidatedCount, result.HasMore, record.BatchSize) {
			return persistenceInvalid("review invalidation result is invalid", nil)
		}

		response, err := encodeJSON(struct {
			InvalidatedCount int  `json:"invalidated_count"`
			HasMore          bool `json:"has_more"`
		}{InvalidatedCount: result.InvalidatedCount, HasMore: result.HasMore})
		if err != nil {
			return err
		}
		if err := gormReviewInsertCommand(
			callbackCtx,
			tx,
			record.WorkspaceID,
			record.IdempotencyKey,
			record.RequestHash,
			"INVALIDATE_CARDS",
			record.WorkspaceID,
			1,
			response,
			at,
		); err != nil {
			return err
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if callbackSucceeded {
		replay, found, recoveryErr := gormReviewLoadInvalidationCommand(
			ctx,
			repository.database,
			record.WorkspaceID,
			record.IdempotencyKey,
			record.RequestHash,
		)
		if recoveryErr == nil && found {
			return replay, nil
		}
	}
	return reviewapp.InvalidationResult{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
}

func gormReviewInvalidationTargetQuery(record reviewapp.InvalidateCardsRecord) string {
	if record.SourceSpanID != nil {
		if record.ClaimID != nil {
			return gormReviewInvalidationByClaimAndSourceSpanTargets
		}
		return gormReviewInvalidationBySourceSpanTargets
	}
	if record.SourceVersionID != nil {
		if record.ClaimID != nil {
			return gormReviewInvalidationByClaimAndSourceVersionTargets
		}
		return gormReviewInvalidationBySourceVersionTargets
	}
	return gormReviewInvalidationByClaimTargets
}

func gormReviewLoadInvalidationCommand(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	key string,
	requestHash string,
) (reviewapp.InvalidationResult, bool, error) {
	receipt, found, err := gormReviewLoadCommand(ctx, database, workspaceID, key)
	if err != nil || !found {
		return reviewapp.InvalidationResult{}, false, err
	}
	if receipt.RequestHash != requestHash ||
		receipt.CommandType != "INVALIDATE_CARDS" ||
		receipt.AggregateID != workspaceID {
		return reviewapp.InvalidationResult{}, false, conflict(
			domain.ErrorCodeAnswerIdempotencyConflict,
			"idempotency key is bound to another request",
		)
	}
	var response struct {
		InvalidatedCount int  `json:"invalidated_count"`
		HasMore          bool `json:"has_more"`
	}
	if err := json.Unmarshal(receipt.Response, &response); err != nil {
		return reviewapp.InvalidationResult{}, false, persistenceInvalid(
			"review invalidation receipt cannot be decoded",
			err,
		)
	}
	if !validInvalidationSummary(
		response.InvalidatedCount,
		response.HasMore,
		reviewapp.MaxInvalidationBatchSize,
	) {
		return reviewapp.InvalidationResult{}, false, persistenceInvalid(
			"review invalidation receipt binding is invalid",
			nil,
		)
	}
	return reviewapp.InvalidationResult{
		InvalidatedCount: response.InvalidatedCount,
		HasMore:          response.HasMore,
		Replayed:         true,
	}, true, nil
}
