package postgres

import (
	"context"
	"time"

	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

// ChangeDeckSchedule 原子地改变牌组状态及其全部卡片调度。
func (repository *Repository) ChangeDeckSchedule(ctx context.Context, record reviewapp.DeckScheduleRecord) (reviewapp.CommandResult[domain.Deck], error) {
	if record.ExpectedVersion < 1 {
		return reviewapp.CommandResult[domain.Deck]{}, invalid("deck schedule record is invalid")
	}
	if err := validateCommandBinding(record.IdempotencyKey, record.RequestHash); err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rollback(tx)
	if err := lockWorkspace(ctx, tx, record.WorkspaceID); err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, err
	}
	if value, found, err := loadDeckCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "CHANGE_DECK_SCHEDULE"); err != nil || found {
		return reviewapp.CommandResult[domain.Deck]{Value: value, Replayed: found}, err
	}
	deck, err := loadDeck(ctx, tx, record.WorkspaceID, record.DeckID, true)
	if err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, err
	}
	if deck.Version != record.ExpectedVersion || deck.Status == domain.DeckStatusArchived {
		return reviewapp.CommandResult[domain.Deck]{}, conflict(domain.ErrorCodeDeckArchived, "deck version or status did not match")
	}
	status := deck.Status
	switch record.Action {
	case reviewapp.DeckSchedulePause:
		status = domain.DeckStatusPaused
	case reviewapp.DeckScheduleResume, reviewapp.DeckScheduleReset:
		status = domain.DeckStatusActive
	default:
		return reviewapp.CommandResult[domain.Deck]{}, invalid("deck schedule action is invalid")
	}
	at := record.At.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	newVersion := deck.Version + 1
	commandTag, err := tx.Exec(ctx, `UPDATE learning.review_deck SET status=$3,version=$4,updated_at=$5 WHERE workspace_id=$1 AND id=$2 AND version=$6`, string(record.WorkspaceID), string(record.DeckID), string(status), newVersion, at, record.ExpectedVersion)
	if err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, classify(err, domain.ErrorCodeDeckArchived)
	}
	if commandTag.RowsAffected() != 1 {
		return reviewapp.CommandResult[domain.Deck]{}, conflict(domain.ErrorCodeDeckArchived, "deck schedule CAS did not match")
	}
	switch record.Action {
	case reviewapp.DeckSchedulePause:
		_, err = tx.Exec(ctx, `UPDATE learning.review_schedule SET paused=true,version=version+1 WHERE workspace_id=$1 AND card_id IN (SELECT id FROM learning.review_card WHERE workspace_id=$1 AND deck_id=$2)`, string(record.WorkspaceID), string(record.DeckID))
	case reviewapp.DeckScheduleResume:
		_, err = tx.Exec(ctx, `UPDATE learning.review_schedule SET paused=false,version=version+1 WHERE workspace_id=$1 AND card_id IN (SELECT id FROM learning.review_card WHERE workspace_id=$1 AND deck_id=$2 AND status='APPROVED')`, string(record.WorkspaceID), string(record.DeckID))
	case reviewapp.DeckScheduleReset:
		_, err = tx.Exec(ctx, `UPDATE learning.review_schedule s SET due_at=$3,interval_days=0,stability=0,difficulty=c.difficulty,last_reviewed_at=NULL,paused=false,version=s.version+1 FROM learning.review_card c WHERE s.workspace_id=$1 AND s.card_id=c.id AND c.workspace_id=$1 AND c.deck_id=$2 AND c.status='APPROVED'`, string(record.WorkspaceID), string(record.DeckID), at)
	}
	if err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, classify(err, domain.ErrorCodeScheduleConflict)
	}
	deck.Status, deck.Version, deck.UpdatedAt = status, newVersion, at
	response, err := encodeJSON(deck)
	if err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, err
	}
	if err := insertCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "CHANGE_DECK_SCHEDULE", record.DeckID, deck.Version, response, at); err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if value, found, recoveryErr := loadDeckCommand(ctx, repository.db, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "CHANGE_DECK_SCHEDULE"); recoveryErr == nil && found {
			return reviewapp.CommandResult[domain.Deck]{Value: value, Replayed: true}, nil
		}
		return reviewapp.CommandResult[domain.Deck]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.CommandResult[domain.Deck]{Value: deck}, nil
}
