package postgres

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

func validateReviewSessionBinding(session domain.Session) error {
	if !domain.IsDeckBoundReviewSession(session) {
		return conflict(domain.ErrorCodeSessionTypeConflict, "review operation requires a deck-bound REVIEW session")
	}
	return nil
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
