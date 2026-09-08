package postgres

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

type commandReceipt struct {
	RequestHash      string
	CommandType      string
	AggregateID      foundation.ID
	AggregateVersion int64
	Response         json.RawMessage
}

func encodeJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, persistenceInvalid("review persistence payload cannot be encoded", fmt.Errorf("encode review persistence payload: %w", err))
	}
	return encoded, nil
}

func validateCommandBinding(key, requestHash string) error {
	if err := domain.ValidateIdempotencyKey(key); err != nil {
		return err
	}
	if !validHash(requestHash) {
		return invalid("review request hash is invalid")
	}
	return nil
}

func scanCard(row interface{ Scan(...any) error }) (domain.Card, error) {
	var card domain.Card
	var claimID, invalidationReason *string
	var invalidatedAt *time.Time
	var answerPoints, evidence []byte
	var cardType, status string
	if err := row.Scan(&card.ID, &card.WorkspaceID, &card.DeckID, &claimID, &card.Question, &answerPoints, &evidence, &cardType, &card.Difficulty, &status, &card.Fingerprint, &card.ModelVersion, &invalidationReason, &invalidatedAt, &card.Version, &card.CreatedAt, &card.UpdatedAt); err != nil {
		return domain.Card{}, err
	}
	if claimID != nil {
		parsed := foundation.ID(*claimID)
		card.ClaimID = &parsed
	}
	if invalidationReason != nil {
		card.InvalidationReason = *invalidationReason
	}
	card.InvalidatedAt = invalidatedAt
	if err := json.Unmarshal(answerPoints, &card.AnswerPoints); err != nil {
		return domain.Card{}, persistenceInvalid("persisted review card answer points cannot be decoded", err)
	}
	if err := json.Unmarshal(evidence, &card.Evidence); err != nil {
		return domain.Card{}, persistenceInvalid("persisted review card evidence cannot be decoded", err)
	}
	card.CardType, card.Status = domain.CardType(cardType), domain.CardStatus(status)
	if err := domain.ValidateCard(card); err != nil {
		return domain.Card{}, persistenceInvalid("persisted review card is invalid", err)
	}
	return card, nil
}

func scanDeck(row interface{ Scan(...any) error }) (domain.Deck, error) {
	var deck domain.Deck
	var scope []byte
	var status string
	if err := row.Scan(&deck.ID, &deck.WorkspaceID, &deck.Name, &scope, &status, &deck.DailyLimit, &deck.SchedulerVersion, &deck.Version, &deck.CreatedAt, &deck.UpdatedAt); err != nil {
		return domain.Deck{}, err
	}
	deck.Scope, deck.Status = append([]byte(nil), scope...), domain.DeckStatus(status)
	if err := domain.ValidateDeck(deck); err != nil {
		return domain.Deck{}, persistenceInvalid("persisted review deck is invalid", err)
	}
	return deck, nil
}

func scanCardAndSchedule(row interface{ Scan(...any) error }) (domain.Card, domain.Schedule, error) {
	var card domain.Card
	var schedule domain.Schedule
	var claimID, invalidationReason *string
	var invalidatedAt *time.Time
	var answerPoints, evidence []byte
	var cardType, cardStatus string
	if err := row.Scan(&card.ID, &card.WorkspaceID, &card.DeckID, &claimID, &card.Question, &answerPoints, &evidence, &cardType, &card.Difficulty, &cardStatus, &card.Fingerprint, &card.ModelVersion, &invalidationReason, &invalidatedAt, &card.Version, &card.CreatedAt, &card.UpdatedAt, &schedule.CardID, &schedule.WorkspaceID, &schedule.DueAt, &schedule.IntervalDays, &schedule.Stability, &schedule.Difficulty, &schedule.LastReviewedAt, &schedule.SchedulerVersion, &schedule.Paused, &schedule.Version); err != nil {
		return domain.Card{}, domain.Schedule{}, err
	}
	if claimID != nil {
		parsed := foundation.ID(*claimID)
		card.ClaimID = &parsed
	}
	if invalidationReason != nil {
		card.InvalidationReason = *invalidationReason
	}
	card.InvalidatedAt = invalidatedAt
	if err := json.Unmarshal(answerPoints, &card.AnswerPoints); err != nil {
		return domain.Card{}, domain.Schedule{}, persistenceInvalid("persisted review card answer points cannot be decoded", err)
	}
	if err := json.Unmarshal(evidence, &card.Evidence); err != nil {
		return domain.Card{}, domain.Schedule{}, persistenceInvalid("persisted review card evidence cannot be decoded", err)
	}
	card.CardType, card.Status = domain.CardType(cardType), domain.CardStatus(cardStatus)
	if err := domain.ValidateCard(card); err != nil {
		return domain.Card{}, domain.Schedule{}, persistenceInvalid("persisted review card is invalid", err)
	}
	if err := domain.ValidateSchedule(schedule); err != nil {
		return domain.Card{}, domain.Schedule{}, persistenceInvalid("persisted review schedule is invalid", err)
	}
	return card, schedule, nil
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
