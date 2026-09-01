package postgres

import (
	"context"
	"time"

	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	"gorm.io/gorm"
)

// ChangeDeckSchedule 原子地改变牌组状态及其全部卡片调度。
func (repository *GORMRepository) ChangeDeckSchedule(ctx context.Context, record reviewapp.DeckScheduleRecord) (reviewapp.CommandResult[domain.Deck], error) {
	if record.ExpectedVersion < 1 {
		return reviewapp.CommandResult[domain.Deck]{}, invalid("deck schedule record is invalid")
	}
	if err := validateCommandBinding(record.IdempotencyKey, record.RequestHash); err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, err
	}

	var result reviewapp.CommandResult[domain.Deck]
	callbackSucceeded := false
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormReviewLockWorkspace(callbackCtx, tx, record.WorkspaceID); err != nil {
			return err
		}
		deck, found, err := gormReviewLoadDeckCommand(
			callbackCtx,
			tx,
			record.WorkspaceID,
			record.IdempotencyKey,
			record.RequestHash,
			"CHANGE_DECK_SCHEDULE",
		)
		if err != nil {
			return err
		}
		if found {
			result = reviewapp.CommandResult[domain.Deck]{Value: deck, Replayed: true}
			callbackSucceeded = true
			return nil
		}

		deck, err = gormReviewLoadDeck(callbackCtx, tx, record.WorkspaceID, record.DeckID, true)
		if err != nil {
			return err
		}
		if deck.Version != record.ExpectedVersion || deck.Status == domain.DeckStatusArchived {
			return conflict(domain.ErrorCodeDeckArchived, "deck version or status did not match")
		}

		status := deck.Status
		switch record.Action {
		case reviewapp.DeckSchedulePause:
			status = domain.DeckStatusPaused
		case reviewapp.DeckScheduleResume, reviewapp.DeckScheduleReset:
			status = domain.DeckStatusActive
		default:
			return invalid("deck schedule action is invalid")
		}

		at := record.At.UTC()
		if at.IsZero() {
			at = time.Now().UTC()
		}
		newVersion := deck.Version + 1
		rowsAffected, err := gormReviewExec(
			callbackCtx,
			tx,
			`UPDATE learning.review_deck
SET status=?,version=?,updated_at=?
WHERE workspace_id=?::uuid AND id=?::uuid AND version=?`,
			string(status),
			newVersion,
			at,
			string(record.WorkspaceID),
			string(record.DeckID),
			record.ExpectedVersion,
		)
		if err != nil {
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeDeckArchived)
		}
		if rowsAffected != 1 {
			return conflict(domain.ErrorCodeDeckArchived, "deck schedule CAS did not match")
		}

		switch record.Action {
		case reviewapp.DeckSchedulePause:
			_, err = gormReviewExec(
				callbackCtx,
				tx,
				`UPDATE learning.review_schedule
SET paused=true,version=version+1
WHERE workspace_id=?::uuid
  AND card_id IN (
      SELECT id
      FROM learning.review_card
      WHERE workspace_id=?::uuid AND deck_id=?::uuid
  )`,
				string(record.WorkspaceID),
				string(record.WorkspaceID),
				string(record.DeckID),
			)
		case reviewapp.DeckScheduleResume:
			_, err = gormReviewExec(
				callbackCtx,
				tx,
				`UPDATE learning.review_schedule
SET paused=false,version=version+1
WHERE workspace_id=?::uuid
  AND card_id IN (
      SELECT id
      FROM learning.review_card
      WHERE workspace_id=?::uuid
        AND deck_id=?::uuid
        AND status='APPROVED'
  )`,
				string(record.WorkspaceID),
				string(record.WorkspaceID),
				string(record.DeckID),
			)
		case reviewapp.DeckScheduleReset:
			_, err = gormReviewExec(
				callbackCtx,
				tx,
				`UPDATE learning.review_schedule AS schedule
SET due_at=?,
    interval_days=0,
    stability=0,
    difficulty=card.difficulty,
    last_reviewed_at=NULL,
    paused=false,
    version=schedule.version+1
FROM learning.review_card AS card
WHERE schedule.workspace_id=?::uuid
  AND schedule.card_id=card.id
  AND card.workspace_id=?::uuid
  AND card.deck_id=?::uuid
  AND card.status='APPROVED'`,
				at,
				string(record.WorkspaceID),
				string(record.WorkspaceID),
				string(record.DeckID),
			)
		}
		if err != nil {
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeScheduleConflict)
		}

		deck.Status = status
		deck.Version = newVersion
		deck.UpdatedAt = at
		response, err := encodeJSON(deck)
		if err != nil {
			return err
		}
		if err := gormReviewInsertCommand(
			callbackCtx,
			tx,
			record.WorkspaceID,
			record.IdempotencyKey,
			record.RequestHash,
			"CHANGE_DECK_SCHEDULE",
			record.DeckID,
			deck.Version,
			response,
			at,
		); err != nil {
			return err
		}
		result = reviewapp.CommandResult[domain.Deck]{Value: deck}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if callbackSucceeded {
		deck, found, recoveryErr := gormReviewLoadDeckCommand(
			ctx,
			repository.database,
			record.WorkspaceID,
			record.IdempotencyKey,
			record.RequestHash,
			"CHANGE_DECK_SCHEDULE",
		)
		if recoveryErr == nil && found {
			return reviewapp.CommandResult[domain.Deck]{Value: deck, Replayed: true}, nil
		}
	}
	return reviewapp.CommandResult[domain.Deck]{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
}
