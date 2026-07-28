package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	"github.com/jackc/pgx/v5"
)

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type evidenceDB interface {
	rowQuerier
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

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

func lockWorkspace(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM core.workspace WHERE id=$1 FOR UPDATE`, string(workspaceID)).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(domain.ErrorCodeWorkspaceNotFound, "review workspace was not found")
		}
		return classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return nil
}

func insertCommand(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, key, requestHash, commandType string, aggregateID foundation.ID, version int64, response []byte, at time.Time) error {
	if _, err := tx.Exec(ctx, `INSERT INTO learning.review_command(workspace_id,idempotency_key,request_hash,command_type,aggregate_id,aggregate_version,response,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, string(workspaceID), key, requestHash, commandType, string(aggregateID), version, response, at.UTC()); err != nil {
		return classify(err, domain.ErrorCodeAnswerIdempotencyConflict)
	}
	return nil
}

func loadCommand(ctx context.Context, db rowQuerier, workspaceID foundation.ID, key string) (commandReceipt, bool, error) {
	var receipt commandReceipt
	var aggregateID string
	err := db.QueryRow(ctx, `SELECT request_hash,command_type,aggregate_id::text,aggregate_version,response FROM learning.review_command WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), key).Scan(&receipt.RequestHash, &receipt.CommandType, &aggregateID, &receipt.AggregateVersion, &receipt.Response)
	if errors.Is(err, pgx.ErrNoRows) {
		return commandReceipt{}, false, nil
	}
	if err != nil {
		return commandReceipt{}, false, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	receipt.AggregateID = foundation.ID(aggregateID)
	return receipt, true, nil
}

func verifyEvidence(ctx context.Context, db evidenceDB, workspaceID, claimID foundation.ID, evidence []domain.EvidenceBinding, lock bool) error {
	if len(evidence) == 0 {
		return domain.InvalidError(domain.ErrorCodeEvidenceMissing, "card evidence is empty")
	}
	seen := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		if err := domain.ValidateEvidence(claimID, item); err != nil {
			return err
		}
		if _, duplicate := seen[item.EvidenceHash]; duplicate {
			return domain.InvalidError(domain.ErrorCodeEvidenceDuplicate, "card evidence must be unique")
		}
		seen[item.EvidenceHash] = struct{}{}
	}
	claimQuery := `SELECT claim.id::text
	FROM core.claim AS claim
	WHERE claim.workspace_id=$1
	  AND claim.id=$2
	  AND claim.status='CONFIRMED'
	  AND NOT EXISTS (
	      SELECT 1
	      FROM core.conflict_member AS member
	      JOIN core.conflict AS conflict
	        ON conflict.workspace_id=member.workspace_id
	       AND conflict.id=member.conflict_id
	      WHERE member.workspace_id=claim.workspace_id
	        AND member.claim_id=claim.id
	        AND conflict.severity IN ('CRITICAL','HIGH')
	        AND conflict.status NOT IN ('RESOLVED','ACCEPTED_DIVERGENCE')
	  )`
	if lock {
		claimQuery += ` FOR SHARE`
	}
	var confirmedID string
	if err := db.QueryRow(ctx, claimQuery, string(workspaceID), string(claimID)).Scan(&confirmedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ConflictError(domain.ErrorCodeEvidenceStale, "claim is no longer confirmed")
		}
		return classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	sourceIDs := make([]string, 0, len(evidence))
	spanIDs := make([]string, 0, len(evidence))
	hashes := make([]string, 0, len(evidence))
	for _, item := range evidence {
		sourceIDs = append(sourceIDs, string(item.SourceVersionID))
		spanIDs = append(spanIDs, string(item.SourceSpanID))
		hashes = append(hashes, item.EvidenceHash)
	}
	query := `WITH requested AS (
  SELECT source_version_id,source_span_id,evidence_hash
  FROM unnest($3::uuid[],$4::uuid[],$5::text[]) AS value(source_version_id,source_span_id,evidence_hash)
)
SELECT cs.id::text
FROM requested r
	JOIN core.claim_source cs
	  ON cs.workspace_id=$1 AND cs.claim_id=$2
	 AND cs.support_type='SUPPORTS'
	 AND cs.source_version_id=r.source_version_id
	 AND cs.source_span_id=r.source_span_id
	 AND cs.evidence_hash=r.evidence_hash
	JOIN core.source_version sv
	  ON sv.workspace_id=$1
	 AND sv.id=r.source_version_id
	 AND sv.security_status <> 'quarantined'
	WHERE COALESCE((
	  SELECT attempt.security_status
	  FROM ingestion.attempt attempt
	  WHERE attempt.workspace_id=$1
	    AND attempt.source_version_id=r.source_version_id
	  ORDER BY attempt.started_at DESC,attempt.id DESC
	  LIMIT 1
	), 'passed') <> 'quarantined'`
	if lock {
		query += ` FOR SHARE OF cs`
	}
	rows, err := db.Query(ctx, query, string(workspaceID), string(claimID), sourceIDs, spanIDs, hashes)
	if err != nil {
		return classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return classify(err, domain.ErrorCodeDependencyUnavailable)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	if count != len(evidence) {
		return domain.ConflictError(domain.ErrorCodeEvidenceStale, "one or more evidence bindings are no longer reachable")
	}
	return nil
}

func loadDeckCommand(ctx context.Context, db rowQuerier, workspaceID foundation.ID, key, hash, commandType string) (domain.Deck, bool, error) {
	receipt, found, err := loadCommand(ctx, db, workspaceID, key)
	if err != nil || !found {
		return domain.Deck{}, false, err
	}
	if receipt.RequestHash != hash || receipt.CommandType != commandType {
		return domain.Deck{}, false, conflict(domain.ErrorCodeAnswerIdempotencyConflict, "idempotency key is bound to another request")
	}
	var value domain.Deck
	if err := json.Unmarshal(receipt.Response, &value); err != nil {
		return domain.Deck{}, false, persistenceInvalid("review deck receipt cannot be decoded", err)
	}
	if err := domain.ValidateDeck(value); err != nil || value.WorkspaceID != workspaceID || value.ID != receipt.AggregateID || value.Version != receipt.AggregateVersion {
		return domain.Deck{}, false, persistenceInvalid("review deck receipt binding is invalid", err)
	}
	return value, true, nil
}

func loadCardCommand(ctx context.Context, db rowQuerier, workspaceID foundation.ID, key, hash, commandType string) (domain.Card, bool, error) {
	receipt, found, err := loadCommand(ctx, db, workspaceID, key)
	if err != nil || !found {
		return domain.Card{}, false, err
	}
	if receipt.RequestHash != hash || receipt.CommandType != commandType {
		return domain.Card{}, false, conflict(domain.ErrorCodeAnswerIdempotencyConflict, "idempotency key is bound to another request")
	}
	var value domain.Card
	if err := json.Unmarshal(receipt.Response, &value); err != nil {
		return domain.Card{}, false, persistenceInvalid("review card receipt cannot be decoded", err)
	}
	if err := domain.ValidateCard(value); err != nil || value.WorkspaceID != workspaceID || value.ID != receipt.AggregateID || value.Version != receipt.AggregateVersion {
		return domain.Card{}, false, persistenceInvalid("review card receipt binding is invalid", err)
	}
	return value, true, nil
}

func loadDeck(ctx context.Context, db rowQuerier, workspaceID, deckID foundation.ID, forUpdate bool) (domain.Deck, error) {
	query := `SELECT id::text,workspace_id::text,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at FROM learning.review_deck WHERE workspace_id=$1 AND id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var deck domain.Deck
	var scope []byte
	var status string
	err := db.QueryRow(ctx, query, string(workspaceID), string(deckID)).Scan(&deck.ID, &deck.WorkspaceID, &deck.Name, &scope, &status, &deck.DailyLimit, &deck.SchedulerVersion, &deck.Version, &deck.CreatedAt, &deck.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Deck{}, notFound(domain.ErrorCodeDeckNotFound, "review deck was not found")
	}
	if err != nil {
		return domain.Deck{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	deck.Scope, deck.Status = append([]byte(nil), scope...), domain.DeckStatus(status)
	if err := domain.ValidateDeck(deck); err != nil {
		return domain.Deck{}, persistenceInvalid("persisted review deck is invalid", err)
	}
	return deck, nil
}

func loadCard(ctx context.Context, db rowQuerier, workspaceID, cardID foundation.ID, forUpdate bool) (domain.Card, error) {
	query := `SELECT id::text,workspace_id::text,deck_id::text,claim_id::text,question,answer_points,evidence,card_type,difficulty,status,fingerprint,model_version,invalidation_reason,invalidated_at,version,created_at,updated_at FROM learning.review_card WHERE workspace_id=$1 AND id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	card, err := scanCard(db.QueryRow(ctx, query, string(workspaceID), string(cardID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Card{}, notFound(domain.ErrorCodeCardNotFound, "review card was not found")
	}
	if err != nil {
		return domain.Card{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return card, nil
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

func loadSession(ctx context.Context, db rowQuerier, workspaceID, sessionID foundation.ID, forUpdate bool) (domain.Session, error) {
	query := `SELECT id::text,workspace_id::text,deck_id::text,session_type,status,config,idempotency_key,started_at,ended_at FROM learning.review_session WHERE workspace_id=$1 AND id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var session domain.Session
	var deckID *string
	var config []byte
	var status, sessionType string
	err := db.QueryRow(ctx, query, string(workspaceID), string(sessionID)).Scan(&session.ID, &session.WorkspaceID, &deckID, &sessionType, &status, &config, &session.IdempotencyKey, &session.StartedAt, &session.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, notFound(domain.ErrorCodeSessionNotFound, "review session was not found")
	}
	if err != nil {
		return domain.Session{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	if deckID != nil {
		parsed := foundation.ID(*deckID)
		session.DeckID = &parsed
	}
	session.Config = append([]byte(nil), config...)
	session.SessionType, session.Status = domain.SessionType(sessionType), domain.SessionStatus(status)
	if err := domain.ValidateSession(session); err != nil {
		return domain.Session{}, persistenceInvalid("persisted review session is invalid", err)
	}
	return session, nil
}

func loadSchedule(ctx context.Context, db rowQuerier, workspaceID, cardID foundation.ID, forUpdate bool) (domain.Schedule, error) {
	query := `SELECT card_id::text,workspace_id::text,due_at,interval_days,stability,difficulty,last_reviewed_at,scheduler_version,paused,version FROM learning.review_schedule WHERE workspace_id=$1 AND card_id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var schedule domain.Schedule
	err := db.QueryRow(ctx, query, string(workspaceID), string(cardID)).Scan(&schedule.CardID, &schedule.WorkspaceID, &schedule.DueAt, &schedule.IntervalDays, &schedule.Stability, &schedule.Difficulty, &schedule.LastReviewedAt, &schedule.SchedulerVersion, &schedule.Paused, &schedule.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Schedule{}, notFound(domain.ErrorCodeScheduleConflict, "review schedule was not found")
	}
	if err != nil {
		return domain.Schedule{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	if err := domain.ValidateSchedule(schedule); err != nil {
		return domain.Schedule{}, persistenceInvalid("persisted review schedule is invalid", err)
	}
	return schedule, nil
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
