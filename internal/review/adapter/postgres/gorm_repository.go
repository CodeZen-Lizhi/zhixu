package postgres

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	"gorm.io/gorm"
)

// VerifyCardEvidence validates that every binding is still reachable from a confirmed Claim.
func (repository *GORMRepository) VerifyCardEvidence(ctx context.Context, workspaceID, claimID foundation.ID, evidence []domain.EvidenceBinding) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if len(evidence) == 0 {
		return domain.InvalidError(domain.ErrorCodeEvidenceMissing, "card evidence is empty")
	}
	return gormReviewVerifyEvidence(ctx, repository.database, workspaceID, claimID, evidence, false)
}

// CreateDeck persists a Deck and its command receipt in one transaction.
func (repository *GORMRepository) CreateDeck(ctx context.Context, deck domain.Deck, idempotencyKey, requestHash string) (reviewapp.CommandResult[domain.Deck], error) {
	if err := domain.ValidateDeck(deck); err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, err
	}
	if err := validateCommandBinding(idempotencyKey, requestHash); err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, err
	}

	result := reviewapp.CommandResult[domain.Deck]{Value: deck}
	callbackSucceeded := false
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormReviewLockWorkspace(callbackCtx, tx, deck.WorkspaceID); err != nil {
			return err
		}
		if value, found, err := gormReviewLoadDeckCommand(callbackCtx, tx, deck.WorkspaceID, idempotencyKey, requestHash, "CREATE_DECK"); err != nil || found {
			result = reviewapp.CommandResult[domain.Deck]{Value: value, Replayed: found}
			if err == nil {
				callbackSucceeded = true
			}
			return err
		}
		if _, err := gormReviewExec(callbackCtx, tx, `INSERT INTO learning.review_deck(id,workspace_id,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at) VALUES(?::uuid,?::uuid,?,?::jsonb,'ACTIVE',?,?,1,?,?)`,
			string(deck.ID), string(deck.WorkspaceID), deck.Name, reviewJSONB(deck.Scope), deck.DailyLimit, deck.SchedulerVersion, deck.CreatedAt.UTC(), deck.CreatedAt.UTC()); err != nil {
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeDeckInvalid)
		}
		response, err := encodeJSON(deck)
		if err != nil {
			return err
		}
		if err := gormReviewInsertCommand(callbackCtx, tx, deck.WorkspaceID, idempotencyKey, requestHash, "CREATE_DECK", deck.ID, deck.Version, response, deck.CreatedAt); err != nil {
			return err
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if callbackSucceeded {
		if value, found, recoveryErr := gormReviewLoadDeckCommand(ctx, repository.database, deck.WorkspaceID, idempotencyKey, requestHash, "CREATE_DECK"); recoveryErr == nil && found {
			return reviewapp.CommandResult[domain.Deck]{Value: value, Replayed: true}, nil
		}
	}
	return reviewapp.CommandResult[domain.Deck]{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
}

// GetDeck reads one Workspace-scoped Deck.
func (repository *GORMRepository) GetDeck(ctx context.Context, workspaceID, deckID foundation.ID) (domain.Deck, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Deck{}, err
	}
	return gormReviewLoadDeck(ctx, repository.database, workspaceID, deckID, false)
}

// ListDecks returns a bounded, stable Workspace-scoped Deck page.
func (repository *GORMRepository) ListDecks(ctx context.Context, workspaceID foundation.ID, limit int) (reviewapp.DeckPage, error) {
	if err := repository.ready(ctx); err != nil {
		return reviewapp.DeckPage{}, err
	}
	rows, err := gormReviewRawRows(ctx, repository.database, `SELECT id::text,workspace_id::text,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at FROM learning.review_deck WHERE workspace_id=?::uuid ORDER BY updated_at DESC,id DESC LIMIT ?`, string(workspaceID), limit)
	if err != nil {
		return reviewapp.DeckPage{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	page := reviewapp.DeckPage{Items: make([]domain.Deck, 0)}
	for rows.Next() {
		deck, scanErr := scanDeck(rows)
		if scanErr != nil {
			return reviewapp.DeckPage{}, gormReviewClassify(ctx, scanErr, domain.ErrorCodeDependencyUnavailable)
		}
		page.Items = append(page.Items, deck)
	}
	if err := rows.Err(); err != nil {
		return reviewapp.DeckPage{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if err := rows.Close(); err != nil {
		return reviewapp.DeckPage{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return page, nil
}

// ListCards returns the bounded management projection for one Deck.
func (repository *GORMRepository) ListCards(ctx context.Context, workspaceID, deckID foundation.ID, limit int) (reviewapp.CardPage, error) {
	if err := repository.ready(ctx); err != nil {
		return reviewapp.CardPage{}, err
	}
	rows, err := gormReviewRawRows(ctx, repository.database, `SELECT id::text,workspace_id::text,deck_id::text,claim_id::text,question,answer_points,evidence,card_type,difficulty,status,fingerprint,model_version,invalidation_reason,invalidated_at,version,created_at,updated_at FROM learning.review_card WHERE workspace_id=?::uuid AND deck_id=?::uuid ORDER BY updated_at DESC,id DESC LIMIT ?`, string(workspaceID), string(deckID), limit)
	if err != nil {
		return reviewapp.CardPage{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	page := reviewapp.CardPage{Items: make([]domain.Card, 0)}
	for rows.Next() {
		card, scanErr := scanCard(rows)
		if scanErr != nil {
			return reviewapp.CardPage{}, gormReviewClassify(ctx, scanErr, domain.ErrorCodeDependencyUnavailable)
		}
		page.Items = append(page.Items, card)
	}
	if err := rows.Err(); err != nil {
		return reviewapp.CardPage{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if err := rows.Close(); err != nil {
		return reviewapp.CardPage{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return page, nil
}

// CreateCard persists a DRAFT Card, evidence locks, and its receipt atomically.
func (repository *GORMRepository) CreateCard(ctx context.Context, card domain.Card, idempotencyKey, requestHash string) (reviewapp.CommandResult[domain.Card], error) {
	if err := domain.ValidateCard(card); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if !validHash(card.Fingerprint) {
		return reviewapp.CommandResult[domain.Card]{}, invalid("card fingerprint is invalid")
	}
	if err := validateCommandBinding(idempotencyKey, requestHash); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}

	result := reviewapp.CommandResult[domain.Card]{Value: card}
	callbackSucceeded := false
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormReviewLockWorkspace(callbackCtx, tx, card.WorkspaceID); err != nil {
			return err
		}
		if value, found, err := gormReviewLoadCardCommand(callbackCtx, tx, card.WorkspaceID, idempotencyKey, requestHash, "CREATE_CARD"); err != nil || found {
			result = reviewapp.CommandResult[domain.Card]{Value: value, Replayed: found}
			if err == nil {
				callbackSucceeded = true
			}
			return err
		}
		if err := gormReviewLockActiveDeck(callbackCtx, tx, card.WorkspaceID, card.DeckID); err != nil {
			return err
		}
		if card.ClaimID == nil {
			return domain.ConflictError(domain.ErrorCodeEvidenceStale, "card claim is missing")
		}
		if err := gormReviewVerifyEvidence(callbackCtx, tx, card.WorkspaceID, *card.ClaimID, card.Evidence, true); err != nil {
			return err
		}
		answerPoints, err := encodeJSON(card.AnswerPoints)
		if err != nil {
			return err
		}
		evidence, err := encodeJSON(card.Evidence)
		if err != nil {
			return err
		}
		claimID := any(nil)
		if card.ClaimID != nil {
			claimID = string(*card.ClaimID)
		}
		if _, err := gormReviewExec(callbackCtx, tx, `INSERT INTO learning.review_card(id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,status,fingerprint,model_version,version,created_at,updated_at) VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?,?::jsonb,?::jsonb,?,?,'DRAFT',?,?,1,?,?)`,
			string(card.ID), string(card.WorkspaceID), string(card.DeckID), claimID, card.Question, reviewJSONB(answerPoints), reviewJSONB(evidence), string(card.CardType), card.Difficulty, card.Fingerprint, card.ModelVersion, card.CreatedAt.UTC(), card.CreatedAt.UTC()); err != nil {
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeCardInvalid)
		}
		response, err := encodeJSON(card)
		if err != nil {
			return err
		}
		if err := gormReviewInsertCommand(callbackCtx, tx, card.WorkspaceID, idempotencyKey, requestHash, "CREATE_CARD", card.ID, card.Version, response, card.CreatedAt); err != nil {
			return err
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if callbackSucceeded {
		if value, found, recoveryErr := gormReviewLoadCardCommand(ctx, repository.database, card.WorkspaceID, idempotencyKey, requestHash, "CREATE_CARD"); recoveryErr == nil && found {
			return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: true}, nil
		}
	}
	return reviewapp.CommandResult[domain.Card]{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
}

// FindCardCreateReplay loads the exact create receipt before evidence is revalidated.
func (repository *GORMRepository) FindCardCreateReplay(ctx context.Context, workspaceID foundation.ID, idempotencyKey, requestHash string) (reviewapp.CommandResult[domain.Card], bool, error) {
	if err := repository.ready(ctx); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, err
	}
	if err := validateCommandBinding(idempotencyKey, requestHash); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, err
	}
	value, found, err := gormReviewLoadCardCommand(ctx, repository.database, workspaceID, idempotencyKey, requestHash, "CREATE_CARD")
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: found}, found, nil
}

// GetCard reads one Workspace-scoped Card.
func (repository *GORMRepository) GetCard(ctx context.Context, workspaceID, cardID foundation.ID) (domain.Card, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Card{}, err
	}
	return gormReviewLoadCard(ctx, repository.database, workspaceID, cardID, false)
}

// FindCardEditReplay loads the exact edit receipt before mutable state is read.
func (repository *GORMRepository) FindCardEditReplay(ctx context.Context, workspaceID foundation.ID, idempotencyKey, requestHash string) (reviewapp.CommandResult[domain.Card], bool, error) {
	if err := repository.ready(ctx); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, err
	}
	if err := validateCommandBinding(idempotencyKey, requestHash); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, err
	}
	value, found, err := gormReviewLoadCardCommand(ctx, repository.database, workspaceID, idempotencyKey, requestHash, "EDIT_CARD")
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: found}, found, nil
}

// EditCard replaces a Card with a CAS-protected DRAFT under fresh evidence locks.
func (repository *GORMRepository) EditCard(ctx context.Context, record reviewapp.EditCardRecord) (reviewapp.CommandResult[domain.Card], error) {
	if err := domain.ValidateCard(record.Card); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if record.Card.Status != domain.CardStatusDraft || record.ExpectedVersion < 1 || record.Card.Version != record.ExpectedVersion+1 || !validHash(record.Card.Fingerprint) {
		return reviewapp.CommandResult[domain.Card]{}, invalid("card edit record is invalid")
	}
	if err := validateCommandBinding(record.IdempotencyKey, record.RequestHash); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}

	result := reviewapp.CommandResult[domain.Card]{Value: record.Card}
	callbackSucceeded := false
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormReviewLockWorkspace(callbackCtx, tx, record.Card.WorkspaceID); err != nil {
			return err
		}
		if value, found, err := gormReviewLoadCardCommand(callbackCtx, tx, record.Card.WorkspaceID, record.IdempotencyKey, record.RequestHash, "EDIT_CARD"); err != nil || found {
			result = reviewapp.CommandResult[domain.Card]{Value: value, Replayed: found}
			if err == nil {
				callbackSucceeded = true
			}
			return err
		}
		current, err := gormReviewLoadCard(callbackCtx, tx, record.Card.WorkspaceID, record.Card.ID, true)
		if err != nil {
			return err
		}
		if current.Version != record.ExpectedVersion {
			return conflict(domain.ErrorCodeCardStateConflict, "card version did not match")
		}
		if err := gormReviewLockActiveDeck(callbackCtx, tx, record.Card.WorkspaceID, record.Card.DeckID); err != nil {
			return err
		}
		if record.Card.ClaimID == nil {
			return domain.ConflictError(domain.ErrorCodeEvidenceStale, "card claim is missing")
		}
		if err := gormReviewVerifyEvidence(callbackCtx, tx, record.Card.WorkspaceID, *record.Card.ClaimID, record.Card.Evidence, true); err != nil {
			return err
		}
		answerPoints, err := encodeJSON(record.Card.AnswerPoints)
		if err != nil {
			return err
		}
		evidence, err := encodeJSON(record.Card.Evidence)
		if err != nil {
			return err
		}
		updatedAt := record.At.UTC()
		if updatedAt.IsZero() {
			updatedAt = record.Card.UpdatedAt.UTC()
		}
		updated, err := gormReviewExec(callbackCtx, tx, `UPDATE learning.review_card
			SET claim_id=?::uuid,question=?,answer_points=?::jsonb,evidence=?::jsonb,card_type=?,difficulty=?,status='DRAFT',fingerprint=?,model_version=?,invalidation_reason=NULL,invalidated_at=NULL,version=?,updated_at=?
			WHERE workspace_id=?::uuid AND id=?::uuid AND version=?`,
			string(*record.Card.ClaimID), record.Card.Question, reviewJSONB(answerPoints), reviewJSONB(evidence), string(record.Card.CardType), record.Card.Difficulty, record.Card.Fingerprint, record.Card.ModelVersion, record.Card.Version, updatedAt,
			string(record.Card.WorkspaceID), string(record.Card.ID), record.ExpectedVersion)
		if err != nil {
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeCardStateConflict)
		}
		if updated != 1 {
			return conflict(domain.ErrorCodeCardStateConflict, "card CAS did not match")
		}
		response, err := encodeJSON(record.Card)
		if err != nil {
			return err
		}
		if err := gormReviewInsertCommand(callbackCtx, tx, record.Card.WorkspaceID, record.IdempotencyKey, record.RequestHash, "EDIT_CARD", record.Card.ID, record.Card.Version, response, updatedAt); err != nil {
			return err
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if callbackSucceeded {
		if value, found, recoveryErr := gormReviewLoadCardCommand(ctx, repository.database, record.Card.WorkspaceID, record.IdempotencyKey, record.RequestHash, "EDIT_CARD"); recoveryErr == nil && found {
			return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: true}, nil
		}
	}
	return reviewapp.CommandResult[domain.Card]{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
}

// FindCardDecisionReplay loads the exact decision receipt before current Card state.
func (repository *GORMRepository) FindCardDecisionReplay(ctx context.Context, workspaceID foundation.ID, idempotencyKey, requestHash string) (reviewapp.CommandResult[domain.Card], bool, error) {
	if err := repository.ready(ctx); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, err
	}
	if err := validateCommandBinding(idempotencyKey, requestHash); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, err
	}
	value, found, err := gormReviewLoadCardCommand(ctx, repository.database, workspaceID, idempotencyKey, requestHash, "DECIDE_CARD")
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: found}, found, nil
}

// DecideCard atomically applies approval, rejection, or invalidation and receipt.
func (repository *GORMRepository) DecideCard(ctx context.Context, record reviewapp.CardDecisionRecord) (reviewapp.CommandResult[domain.Card], error) {
	if record.ExpectedVersion < 1 {
		return reviewapp.CommandResult[domain.Card]{}, invalid("card decision record is invalid")
	}
	if err := validateCommandBinding(record.IdempotencyKey, record.RequestHash); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}

	var result reviewapp.CommandResult[domain.Card]
	callbackSucceeded := false
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormReviewLockWorkspace(callbackCtx, tx, record.WorkspaceID); err != nil {
			return err
		}
		if value, found, err := gormReviewLoadCardCommand(callbackCtx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "DECIDE_CARD"); err != nil || found {
			result = reviewapp.CommandResult[domain.Card]{Value: value, Replayed: found}
			if err == nil {
				callbackSucceeded = true
			}
			return err
		}
		current, err := gormReviewLoadCard(callbackCtx, tx, record.WorkspaceID, record.CardID, true)
		if err != nil {
			return err
		}
		if current.Version != record.ExpectedVersion {
			return conflict(domain.ErrorCodeCardStateConflict, "card version did not match")
		}
		switch record.TargetStatus {
		case domain.CardStatusApproved, domain.CardStatusRejected:
			if current.Status != domain.CardStatusDraft {
				return conflict(domain.ErrorCodeCardStateConflict, "only draft cards can be approved or rejected")
			}
		case domain.CardStatusInvalidated:
			if current.Status != domain.CardStatusApproved {
				return conflict(domain.ErrorCodeCardStateConflict, "only approved cards can be invalidated")
			}
		default:
			return invalid("card target status is invalid")
		}
		if record.TargetStatus == domain.CardStatusApproved && record.InitialSchedule == nil {
			return invalid("approved card requires initial schedule")
		}
		if record.TargetStatus == domain.CardStatusInvalidated {
			if err := domain.ValidateInvalidationReason(record.Reason); err != nil {
				return err
			}
		}

		now := record.At.UTC()
		newVersion := current.Version + 1
		var invalidationReason any
		var invalidatedAt any
		if record.TargetStatus == domain.CardStatusInvalidated {
			invalidationReason, invalidatedAt = record.Reason, now
			current.InvalidationReason = record.Reason
			current.InvalidatedAt = &now
		} else {
			current.InvalidationReason, current.InvalidatedAt = "", nil
		}
		if _, err := gormReviewExec(callbackCtx, tx, `UPDATE learning.review_card SET status=?,version=?,updated_at=?,invalidation_reason=?,invalidated_at=? WHERE workspace_id=?::uuid AND id=?::uuid AND version=?`,
			string(record.TargetStatus), newVersion, now, invalidationReason, invalidatedAt, string(record.WorkspaceID), string(record.CardID), record.ExpectedVersion); err != nil {
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeCardStateConflict)
		}
		current.Status, current.Version, current.UpdatedAt = record.TargetStatus, newVersion, now
		if record.TargetStatus == domain.CardStatusApproved {
			if current.ClaimID == nil {
				return domain.ConflictError(domain.ErrorCodeEvidenceStale, "card claim is missing")
			}
			if err := gormReviewVerifyEvidence(callbackCtx, tx, current.WorkspaceID, *current.ClaimID, current.Evidence, true); err != nil {
				return err
			}
			schedule := *record.InitialSchedule
			schedule.Difficulty = current.Difficulty
			if err := domain.ValidateSchedule(schedule); err != nil {
				return err
			}
			if _, err := gormReviewExec(callbackCtx, tx, `INSERT INTO learning.review_schedule(card_id,workspace_id,due_at,interval_days,stability,difficulty,last_reviewed_at,scheduler_version,paused,version) VALUES(?::uuid,?::uuid,?,?,?,?,NULL,?,false,1) ON CONFLICT(card_id) DO UPDATE SET due_at=EXCLUDED.due_at,interval_days=EXCLUDED.interval_days,stability=EXCLUDED.stability,difficulty=EXCLUDED.difficulty,last_reviewed_at=NULL,scheduler_version=EXCLUDED.scheduler_version,paused=false,version=learning.review_schedule.version+1`,
				string(schedule.CardID), string(schedule.WorkspaceID), schedule.DueAt.UTC(), schedule.IntervalDays, schedule.Stability, schedule.Difficulty, schedule.SchedulerVersion); err != nil {
				return gormReviewClassify(callbackCtx, err, domain.ErrorCodeScheduleConflict)
			}
		}
		if err := domain.ValidateCard(current); err != nil {
			return err
		}
		response, err := encodeJSON(current)
		if err != nil {
			return err
		}
		if err := gormReviewInsertCommand(callbackCtx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "DECIDE_CARD", record.CardID, current.Version, response, now); err != nil {
			return err
		}
		result = reviewapp.CommandResult[domain.Card]{Value: current}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if callbackSucceeded {
		if value, found, recoveryErr := gormReviewLoadCardCommand(ctx, repository.database, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "DECIDE_CARD"); recoveryErr == nil && found {
			return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: true}, nil
		}
	}
	return reviewapp.CommandResult[domain.Card]{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
}

// ListDue returns the server-ranked, evidence-safe due Cards for a Deck filter.
func (repository *GORMRepository) ListDue(ctx context.Context, workspaceID foundation.ID, deckID *foundation.ID, now time.Time, limit int) ([]reviewapp.DueCard, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	arguments := []any{string(workspaceID), now.UTC(), optionalID(deckID), limit}
	rows, err := gormReviewRawRows(ctx, repository.database, gormRankedDueCardsSQL+`
		SELECT card_id,card_workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,card_difficulty,card_status,fingerprint,model_version,invalidation_reason,invalidated_at,card_version,card_created_at,card_updated_at,
		       schedule_card_id,schedule_workspace_id,due_at,interval_days,stability,schedule_difficulty,last_reviewed_at,scheduler_version,paused,schedule_version
		FROM due
		WHERE deck_position <= GREATEST(daily_limit-answered_today,0)
		ORDER BY due_at ASC,card_id ASC
		LIMIT ?`, arguments...)
	if err != nil {
		return nil, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	result := make([]reviewapp.DueCard, 0)
	for rows.Next() {
		card, schedule, scanErr := gormReviewScanCardAndSchedule(rows)
		if scanErr != nil {
			return nil, gormReviewClassify(ctx, scanErr, domain.ErrorCodeDependencyUnavailable)
		}
		result = append(result, reviewapp.DueCard{Card: card, Schedule: schedule})
	}
	if err := rows.Err(); err != nil {
		return nil, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if err := rows.Close(); err != nil {
		return nil, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return result, nil
}

func gormReviewLockActiveDeck(ctx context.Context, database *gorm.DB, workspaceID, deckID foundation.ID) error {
	row, err := gormReviewRawRow(ctx, database, `SELECT id::text FROM learning.review_deck WHERE workspace_id=?::uuid AND id=?::uuid AND status<>'ARCHIVED' FOR UPDATE`, string(workspaceID), string(deckID))
	if err != nil {
		return gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	var persistedID string
	if err := row.Scan(&persistedID); gormReviewNoRows(err) {
		return notFound(domain.ErrorCodeDeckNotFound, "review deck was not found or archived")
	} else if err != nil {
		return gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return nil
}
