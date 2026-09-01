package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const (
	gormReviewWorkspaceLockSQL = `SELECT id::text FROM core.workspace WHERE id=?::uuid FOR UPDATE`
	gormReviewCommandInsertSQL = `INSERT INTO learning.review_command(
workspace_id,idempotency_key,request_hash,command_type,aggregate_id,aggregate_version,response,created_at
) VALUES (?::uuid,?,?,?,?::uuid,?,?::jsonb,?)`
	gormReviewCommandSelectSQL = `SELECT request_hash,command_type,aggregate_id::text,aggregate_version,response
FROM learning.review_command
WHERE workspace_id=?::uuid AND idempotency_key=?`
	gormReviewDeckSelectSQL = `SELECT id::text,workspace_id::text,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at
FROM learning.review_deck
WHERE workspace_id=?::uuid AND id=?::uuid`
	gormReviewCardSelectSQL = `SELECT id::text,workspace_id::text,deck_id::text,claim_id::text,question,answer_points,evidence,card_type,difficulty,status,fingerprint,model_version,invalidation_reason,invalidated_at,version,created_at,updated_at
FROM learning.review_card
WHERE workspace_id=?::uuid AND id=?::uuid`
	gormReviewSessionSelectSQL = `SELECT id::text,workspace_id::text,deck_id::text,session_type,status,config,idempotency_key,started_at,ended_at
FROM learning.review_session
WHERE workspace_id=?::uuid AND id=?::uuid`
	gormReviewScheduleSelectSQL = `SELECT card_id::text,workspace_id::text,due_at,interval_days,stability,difficulty,last_reviewed_at,scheduler_version,paused,version
FROM learning.review_schedule
WHERE workspace_id=?::uuid AND card_id=?::uuid`
)

func gormReviewLockWorkspace(ctx context.Context, database *gorm.DB, workspaceID foundation.ID) error {
	row, err := gormReviewRawRow(ctx, database, gormReviewWorkspaceLockSQL, string(workspaceID))
	if err != nil {
		return gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	var id string
	if err := row.Scan(&id); err != nil {
		if gormReviewNoRows(err) {
			return notFound(domain.ErrorCodeWorkspaceNotFound, "review workspace was not found")
		}
		return gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return nil
}

func gormReviewInsertCommand(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	key, requestHash, commandType string,
	aggregateID foundation.ID,
	version int64,
	response []byte,
	at time.Time,
) error {
	_, err := gormReviewExec(ctx, database, gormReviewCommandInsertSQL,
		string(workspaceID), key, requestHash, commandType, string(aggregateID), version,
		reviewJSONB(response), at.UTC(),
	)
	return gormReviewClassify(ctx, err, domain.ErrorCodeAnswerIdempotencyConflict)
}

func gormReviewLoadCommand(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key string) (commandReceipt, bool, error) {
	row, err := gormReviewRawRow(ctx, database, gormReviewCommandSelectSQL, string(workspaceID), key)
	if err != nil {
		return commandReceipt{}, false, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	var receipt commandReceipt
	var aggregateID string
	var response reviewJSONB
	if err := row.Scan(&receipt.RequestHash, &receipt.CommandType, &aggregateID, &receipt.AggregateVersion, &response); err != nil {
		if gormReviewNoRows(err) {
			return commandReceipt{}, false, nil
		}
		return commandReceipt{}, false, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	receipt.AggregateID = foundation.ID(aggregateID)
	receipt.Response = append(json.RawMessage(nil), response...)
	return receipt, true, nil
}

func gormReviewLoadDeckCommand(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key, hash, commandType string) (domain.Deck, bool, error) {
	receipt, found, err := gormReviewLoadCommand(ctx, database, workspaceID, key)
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

func gormReviewLoadCardCommand(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key, hash, commandType string) (domain.Card, bool, error) {
	receipt, found, err := gormReviewLoadCommand(ctx, database, workspaceID, key)
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

func gormReviewLoadDeck(ctx context.Context, database *gorm.DB, workspaceID, deckID foundation.ID, forUpdate bool) (domain.Deck, error) {
	query := gormReviewDeckSelectSQL
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormReviewRawRow(ctx, database, query, string(workspaceID), string(deckID))
	if err != nil {
		return domain.Deck{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	deck, err := scanDeck(row)
	if gormReviewNoRows(err) {
		return domain.Deck{}, notFound(domain.ErrorCodeDeckNotFound, "review deck was not found")
	}
	if err != nil {
		return domain.Deck{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return deck, nil
}

func gormReviewLoadCard(ctx context.Context, database *gorm.DB, workspaceID, cardID foundation.ID, forUpdate bool) (domain.Card, error) {
	query := gormReviewCardSelectSQL
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormReviewRawRow(ctx, database, query, string(workspaceID), string(cardID))
	if err != nil {
		return domain.Card{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	card, err := scanCard(row)
	if gormReviewNoRows(err) {
		return domain.Card{}, notFound(domain.ErrorCodeCardNotFound, "review card was not found")
	}
	if err != nil {
		return domain.Card{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return card, nil
}

func gormReviewLoadSession(ctx context.Context, database *gorm.DB, workspaceID, sessionID foundation.ID, forUpdate bool) (domain.Session, error) {
	query := gormReviewSessionSelectSQL
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormReviewRawRow(ctx, database, query, string(workspaceID), string(sessionID))
	if err != nil {
		return domain.Session{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	var session domain.Session
	var deckID *string
	var config reviewJSONB
	var status, sessionType string
	if err := row.Scan(&session.ID, &session.WorkspaceID, &deckID, &sessionType, &status, &config, &session.IdempotencyKey, &session.StartedAt, &session.EndedAt); err != nil {
		if gormReviewNoRows(err) {
			return domain.Session{}, notFound(domain.ErrorCodeSessionNotFound, "review session was not found")
		}
		return domain.Session{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if deckID != nil {
		parsed := foundation.ID(*deckID)
		session.DeckID = &parsed
	}
	session.Config = append(json.RawMessage(nil), config...)
	session.SessionType, session.Status = domain.SessionType(sessionType), domain.SessionStatus(status)
	if err := domain.ValidateSession(session); err != nil {
		return domain.Session{}, persistenceInvalid("persisted review session is invalid", err)
	}
	return session, nil
}

func gormReviewLoadSchedule(ctx context.Context, database *gorm.DB, workspaceID, cardID foundation.ID, forUpdate bool) (domain.Schedule, error) {
	query := gormReviewScheduleSelectSQL
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormReviewRawRow(ctx, database, query, string(workspaceID), string(cardID))
	if err != nil {
		return domain.Schedule{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	var schedule domain.Schedule
	if err := row.Scan(&schedule.CardID, &schedule.WorkspaceID, &schedule.DueAt, &schedule.IntervalDays, &schedule.Stability, &schedule.Difficulty, &schedule.LastReviewedAt, &schedule.SchedulerVersion, &schedule.Paused, &schedule.Version); err != nil {
		if gormReviewNoRows(err) {
			return domain.Schedule{}, notFound(domain.ErrorCodeScheduleConflict, "review schedule was not found")
		}
		return domain.Schedule{}, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if err := domain.ValidateSchedule(schedule); err != nil {
		return domain.Schedule{}, persistenceInvalid("persisted review schedule is invalid", err)
	}
	return schedule, nil
}

func gormReviewVerifyEvidence(ctx context.Context, database *gorm.DB, workspaceID, claimID foundation.ID, evidence []domain.EvidenceBinding, lock bool) error {
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
WHERE claim.workspace_id=?::uuid
  AND claim.id=?::uuid
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
	row, err := gormReviewRawRow(ctx, database, claimQuery, string(workspaceID), string(claimID))
	if err != nil {
		return gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	var confirmedID string
	if err := row.Scan(&confirmedID); err != nil {
		if gormReviewNoRows(err) {
			return domain.ConflictError(domain.ErrorCodeEvidenceStale, "claim is no longer confirmed")
		}
		return gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	sourceIDs := make([]string, 0, len(evidence))
	spanIDs := make([]string, 0, len(evidence))
	hashes := make([]string, 0, len(evidence))
	for _, item := range evidence {
		sourceIDs = append(sourceIDs, string(item.SourceVersionID))
		spanIDs = append(spanIDs, string(item.SourceSpanID))
		hashes = append(hashes, item.EvidenceHash)
	}
	query := `WITH parameters AS (
  SELECT ?::uuid AS workspace_id,?::uuid AS claim_id
), requested AS (
  SELECT source_version_id,source_span_id,evidence_hash
  FROM unnest(?::uuid[],?::uuid[],?::text[]) AS value(source_version_id,source_span_id,evidence_hash)
)
SELECT cs.id::text
FROM parameters p
JOIN requested r ON true
JOIN core.claim_source cs
  ON cs.workspace_id=p.workspace_id AND cs.claim_id=p.claim_id
 AND cs.support_type='SUPPORTS'
 AND cs.source_version_id=r.source_version_id
 AND cs.source_span_id=r.source_span_id
 AND cs.evidence_hash=r.evidence_hash
JOIN core.source_version sv
  ON sv.workspace_id=p.workspace_id
 AND sv.id=r.source_version_id
 AND sv.security_status <> 'quarantined'
WHERE COALESCE((
  SELECT attempt.security_status
  FROM ingestion.attempt attempt
  WHERE attempt.workspace_id=p.workspace_id
    AND attempt.source_version_id=r.source_version_id
  ORDER BY attempt.started_at DESC,attempt.id DESC
  LIMIT 1
), 'passed') <> 'quarantined'`
	if lock {
		query += ` FOR SHARE OF cs`
	}
	rows, err := gormReviewRawRows(ctx, database, query,
		string(workspaceID), string(claimID), pq.Array(sourceIDs), pq.Array(spanIDs), pq.Array(hashes),
	)
	if err != nil {
		return gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if count != len(evidence) {
		return domain.ConflictError(domain.ErrorCodeEvidenceStale, "one or more evidence bindings are no longer reachable")
	}
	return nil
}

const gormRankedDueCardsSQL = `
WITH parameters AS (
    SELECT ?::uuid AS workspace_id,?::timestamptz AS current_time,?::uuid AS deck_id
), answered_today AS (
    SELECT
        COALESCE(answer_session.deck_id,answered_card.deck_id) AS deck_id,
        count(*) AS answer_count
    FROM parameters p
    JOIN learning.review_answer AS answer ON answer.workspace_id=p.workspace_id
    JOIN learning.review_card AS answered_card
      ON answered_card.workspace_id=answer.workspace_id
     AND answered_card.id=answer.card_id
    JOIN learning.review_session AS answer_session
      ON answer_session.workspace_id=answer.workspace_id
     AND answer_session.id=answer.session_id
    WHERE answer.created_at >= date_trunc('day',p.current_time,'UTC')
      AND answer.created_at < date_trunc('day',p.current_time,'UTC') + interval '1 day'
    GROUP BY COALESCE(answer_session.deck_id,answered_card.deck_id)
), due AS (
    SELECT
        c.id::text AS card_id,c.workspace_id::text AS card_workspace_id,c.deck_id::text AS deck_id,c.claim_id::text AS claim_id,c.question AS question,c.answer_points AS answer_points,c.evidence AS evidence,c.card_type AS card_type,c.difficulty AS card_difficulty,c.status AS card_status,c.fingerprint AS fingerprint,c.model_version AS model_version,c.invalidation_reason AS invalidation_reason,c.invalidated_at AS invalidated_at,c.version AS card_version,c.created_at AS card_created_at,c.updated_at AS card_updated_at,
        s.card_id::text AS schedule_card_id,s.workspace_id::text AS schedule_workspace_id,s.due_at AS due_at,s.interval_days AS interval_days,s.stability AS stability,s.difficulty AS schedule_difficulty,s.last_reviewed_at AS last_reviewed_at,s.scheduler_version AS scheduler_version,s.paused AS paused,s.version AS schedule_version,
        d.daily_limit,
        COALESCE(answered_today.answer_count,0) AS answered_today,
        row_number() OVER (PARTITION BY c.deck_id ORDER BY s.due_at ASC,c.id ASC) AS deck_position
    FROM parameters p
    JOIN learning.review_card c ON c.workspace_id=p.workspace_id
    JOIN learning.review_schedule s ON s.workspace_id=c.workspace_id AND s.card_id=c.id
    JOIN learning.review_deck d ON d.workspace_id=c.workspace_id AND d.id=c.deck_id
    JOIN core.claim claim ON claim.workspace_id=c.workspace_id AND claim.id=c.claim_id AND claim.status='CONFIRMED'
    LEFT JOIN answered_today ON answered_today.deck_id=c.deck_id
    WHERE c.status='APPROVED'
      AND d.status='ACTIVE'
      AND s.paused=false
      AND s.due_at <= p.current_time
      AND (p.deck_id IS NULL OR c.deck_id=p.deck_id)
      AND NOT EXISTS (
          SELECT 1
          FROM core.conflict_member AS member
          JOIN core.conflict AS conflict
            ON conflict.workspace_id=member.workspace_id
           AND conflict.id=member.conflict_id
          WHERE member.workspace_id=c.workspace_id
            AND member.claim_id=c.claim_id
            AND conflict.severity IN ('CRITICAL','HIGH')
            AND conflict.status NOT IN ('RESOLVED','ACCEPTED_DIVERGENCE')
      )
      AND CASE
          WHEN jsonb_typeof(c.evidence)='array' THEN
              jsonb_array_length(c.evidence) BETWEEN 1 AND 128
              AND jsonb_array_length(c.evidence)=(
                  SELECT count(DISTINCT evidence.item->>'evidence_hash')
                  FROM jsonb_array_elements(c.evidence) AS evidence(item)
              )
              AND NOT EXISTS (
                  SELECT 1
                  FROM jsonb_array_elements(c.evidence) AS evidence(item)
                  WHERE jsonb_typeof(evidence.item) IS DISTINCT FROM 'object'
                     OR jsonb_typeof(evidence.item->'schema_version') IS DISTINCT FROM 'string'
                     OR evidence.item->>'schema_version' IS DISTINCT FROM 'review-evidence/v1'
                     OR jsonb_typeof(evidence.item->'claim_id') IS DISTINCT FROM 'string'
                     OR evidence.item->>'claim_id' IS DISTINCT FROM c.claim_id::text
                     OR learning.try_review_evidence_uuid(evidence.item->>'claim_id') IS DISTINCT FROM c.claim_id
                     OR jsonb_typeof(evidence.item->'source_version_id') IS DISTINCT FROM 'string'
                     OR learning.try_review_evidence_uuid(evidence.item->>'source_version_id') IS NULL
                     OR jsonb_typeof(evidence.item->'source_span_id') IS DISTINCT FROM 'string'
                     OR learning.try_review_evidence_uuid(evidence.item->>'source_span_id') IS NULL
                     OR jsonb_typeof(evidence.item->'evidence_hash') IS DISTINCT FROM 'string'
                     OR NOT COALESCE(evidence.item->>'evidence_hash' ~ '^[0-9a-f]{64}$', false)
                     OR NOT EXISTS (
                         SELECT 1
                         FROM core.claim_source claim_source
                         WHERE claim_source.workspace_id=c.workspace_id
                           AND claim_source.claim_id=c.claim_id
                           AND claim_source.support_type='SUPPORTS'
                           AND claim_source.source_version_id=learning.try_review_evidence_uuid(evidence.item->>'source_version_id')
                           AND claim_source.source_span_id=learning.try_review_evidence_uuid(evidence.item->>'source_span_id')
                           AND claim_source.evidence_hash=evidence.item->>'evidence_hash'
                     )
                     OR EXISTS (
                         SELECT 1
                         FROM core.source_version source_version
                         WHERE source_version.workspace_id=c.workspace_id
                           AND source_version.id=learning.try_review_evidence_uuid(evidence.item->>'source_version_id')
                           AND source_version.security_status='quarantined'
                     )
                     OR COALESCE((
                         SELECT attempt.security_status
                         FROM ingestion.attempt attempt
                         WHERE attempt.workspace_id=c.workspace_id
                           AND attempt.source_version_id=learning.try_review_evidence_uuid(evidence.item->>'source_version_id')
                         ORDER BY attempt.started_at DESC,attempt.id DESC
                         LIMIT 1
                     ), 'passed')='quarantined'
              )
          ELSE false
      END
)`

func gormReviewCheckAnswerEligibility(ctx context.Context, database *gorm.DB, workspaceID, deckID, cardID foundation.ID, now time.Time) error {
	row, err := gormReviewRawRow(ctx, database, gormRankedDueCardsSQL+`
SELECT EXISTS (
    SELECT 1 FROM due
    WHERE card_id=?::text AND deck_position <= GREATEST(daily_limit-answered_today,0)
)`, string(workspaceID), now.UTC(), string(deckID), string(cardID))
	if err != nil {
		return gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	var eligible bool
	if err := row.Scan(&eligible); err != nil {
		return gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if !eligible {
		return conflict(domain.ErrorCodeScheduleConflict, "card is not due within the deck daily limit")
	}
	return nil
}

func gormReviewScanDeck(row interface{ Scan(...any) error }) (domain.Deck, error) {
	return scanDeck(row)
}

func gormReviewScanCard(row interface{ Scan(...any) error }) (domain.Card, error) {
	return scanCard(row)
}

func gormReviewScanCardAndSchedule(row interface{ Scan(...any) error }) (domain.Card, domain.Schedule, error) {
	return scanCardAndSchedule(row)
}
