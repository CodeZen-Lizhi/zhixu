package postgres

import (
	"context"
	"encoding/json"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

const (
	invalidationStatementPrefix = `
WITH input AS MATERIALIZED (
    SELECT $1::uuid AS workspace_id,
           $2::uuid AS claim_id,
           $3::uuid AS source_version_id,
           $4::uuid AS source_span_id,
           $5::integer AS candidate_limit
), targets AS MATERIALIZED (`
	invalidationByClaimTargets = `
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
	invalidationBySourceVersionTargets = `
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
	invalidationByClaimAndSourceVersionTargets = `
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
	invalidationBySourceSpanTargets = `
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
	invalidationByClaimAndSourceSpanTargets = `
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
	invalidationStatementSuffix = `
), updated AS (
    UPDATE learning.review_card AS card
    SET status='INVALIDATED',
        invalidation_reason=$7,
        invalidated_at=$8,
        version=card.version+1,
        updated_at=$8
    FROM (
        SELECT id
        FROM targets
        ORDER BY id
        LIMIT $6
    ) AS batch
    WHERE card.workspace_id=$1
      AND card.id=batch.id
      AND card.status='APPROVED'
    RETURNING card.id
)
SELECT count(*)::integer,
       (SELECT count(*) > $6 FROM targets)
FROM updated`
)

// InvalidateCards persists one bounded knowledge-lifecycle invalidation batch.
// Card status is the sole lifecycle fact; the database trigger removes its
// schedule in the same transaction and the receipt makes delivery idempotent.
func (repository *Repository) InvalidateCards(ctx context.Context, record reviewapp.InvalidateCardsRecord) (reviewapp.InvalidationResult, error) {
	if err := validateInvalidationRecord(record); err != nil {
		return reviewapp.InvalidationResult{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return reviewapp.InvalidationResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rollback(tx)
	if err := lockWorkspace(ctx, tx, record.WorkspaceID); err != nil {
		return reviewapp.InvalidationResult{}, err
	}
	if result, found, err := loadInvalidationCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); err != nil || found {
		return result, err
	}
	at := record.At.UTC()
	result := reviewapp.InvalidationResult{}
	query := invalidationStatementPrefix + invalidationTargetQuery(record) + invalidationStatementSuffix
	err = tx.QueryRow(ctx, query, string(record.WorkspaceID), optionalID(record.ClaimID), optionalID(record.SourceVersionID), optionalID(record.SourceSpanID), record.BatchSize+1, record.BatchSize, record.Reason, at).Scan(&result.InvalidatedCount, &result.HasMore)
	if err != nil {
		return reviewapp.InvalidationResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	if !validInvalidationSummary(result.InvalidatedCount, result.HasMore, record.BatchSize) {
		return reviewapp.InvalidationResult{}, persistenceInvalid("review invalidation result is invalid", nil)
	}
	response, err := encodeJSON(struct {
		InvalidatedCount int  `json:"invalidated_count"`
		HasMore          bool `json:"has_more"`
	}{InvalidatedCount: result.InvalidatedCount, HasMore: result.HasMore})
	if err != nil {
		return reviewapp.InvalidationResult{}, err
	}
	if err := insertCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "INVALIDATE_CARDS", record.WorkspaceID, 1, response, at); err != nil {
		return reviewapp.InvalidationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if replay, found, recoveryErr := loadInvalidationCommand(ctx, repository.db, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); recoveryErr == nil && found {
			return replay, nil
		}
		return reviewapp.InvalidationResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return result, nil
}

// invalidationTargetQuery selects a static plan whose leading index keys match
// every supplied selector.
// No request value is interpolated into SQL; every selector remains bound.
func invalidationTargetQuery(record reviewapp.InvalidateCardsRecord) string {
	if record.SourceSpanID != nil {
		if record.ClaimID != nil {
			return invalidationByClaimAndSourceSpanTargets
		}
		return invalidationBySourceSpanTargets
	}
	if record.SourceVersionID != nil {
		if record.ClaimID != nil {
			return invalidationByClaimAndSourceVersionTargets
		}
		return invalidationBySourceVersionTargets
	}
	return invalidationByClaimTargets
}

func validInvalidationSummary(count int, hasMore bool, batchSize int) bool {
	return count >= 0 && count <= batchSize && (!hasMore || count == batchSize)
}

func validateInvalidationRecord(record reviewapp.InvalidateCardsRecord) error {
	if err := validateCommandBinding(record.IdempotencyKey, record.RequestHash); err != nil {
		return err
	}
	if err := domain.ValidateInvalidationReason(record.Reason); err != nil {
		return err
	}
	if record.ClaimID == nil && record.SourceVersionID == nil && record.SourceSpanID == nil {
		return domain.InvalidError(domain.ErrorCodeInvalidationInvalid, "an invalidation target is required")
	}
	for _, id := range []*foundation.ID{record.ClaimID, record.SourceVersionID, record.SourceSpanID} {
		if id == nil {
			continue
		}
		parsed, err := foundation.ParseID(string(*id))
		if err != nil || parsed != *id {
			return domain.InvalidError(domain.ErrorCodeInvalidationInvalid, "invalidation target is invalid")
		}
	}
	if record.At.IsZero() {
		return domain.InvalidError(domain.ErrorCodeInvalidationInvalid, "invalidation time is invalid")
	}
	if record.BatchSize < 1 || record.BatchSize > reviewapp.MaxInvalidationBatchSize {
		return domain.InvalidError(domain.ErrorCodeInvalidationInvalid, "invalidation batch size is invalid")
	}
	return nil
}

func optionalID(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func loadInvalidationCommand(ctx context.Context, db rowQuerier, workspaceID foundation.ID, key, requestHash string) (reviewapp.InvalidationResult, bool, error) {
	receipt, found, err := loadCommand(ctx, db, workspaceID, key)
	if err != nil || !found {
		return reviewapp.InvalidationResult{}, false, err
	}
	if receipt.RequestHash != requestHash || receipt.CommandType != "INVALIDATE_CARDS" || receipt.AggregateID != workspaceID {
		return reviewapp.InvalidationResult{}, false, conflict(domain.ErrorCodeAnswerIdempotencyConflict, "idempotency key is bound to another request")
	}
	var response struct {
		InvalidatedCount int  `json:"invalidated_count"`
		HasMore          bool `json:"has_more"`
	}
	if err := json.Unmarshal(receipt.Response, &response); err != nil {
		return reviewapp.InvalidationResult{}, false, persistenceInvalid("review invalidation receipt cannot be decoded", err)
	}
	if !validInvalidationSummary(response.InvalidatedCount, response.HasMore, reviewapp.MaxInvalidationBatchSize) {
		return reviewapp.InvalidationResult{}, false, persistenceInvalid("review invalidation receipt binding is invalid", nil)
	}
	return reviewapp.InvalidationResult{InvalidatedCount: response.InvalidatedCount, HasMore: response.HasMore, Replayed: true}, true, nil
}
