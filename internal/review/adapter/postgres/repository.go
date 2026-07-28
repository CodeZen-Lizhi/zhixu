// Package postgres 持久化 Review 学习事实，并维护答题与 FSRS 的原子边界。
package postgres

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB 是 Review PostgreSQL Adapter 所需的最小参数化查询边界。
type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

// Repository 是 Review 聚合的 PostgreSQL 实现。
type Repository struct{ db DB }

var _ reviewapp.Repository = (*Repository)(nil)
var _ reviewapp.EvidenceVerifier = (*Repository)(nil)

// NewRepository 创建 Review PostgreSQL Adapter。
func NewRepository(db DB) (*Repository, error) {
	if nilValue(db) {
		return nil, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review database is unavailable")
	}
	return &Repository{db: db}, nil
}

// VerifyCardEvidence 校验 Claim 为 CONFIRMED 且每条证据仍存在并归属于该 Claim。
func (repository *Repository) VerifyCardEvidence(ctx context.Context, workspaceID, claimID foundation.ID, evidence []domain.EvidenceBinding) error {
	if repository == nil || nilValue(repository.db) {
		return domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review database is unavailable")
	}
	if len(evidence) == 0 {
		return domain.InvalidError(domain.ErrorCodeEvidenceMissing, "card evidence is empty")
	}
	return verifyEvidence(ctx, repository.db, workspaceID, claimID, evidence, false)
}

// CreateDeck 持久化牌组和命令 receipt。
func (repository *Repository) CreateDeck(ctx context.Context, deck domain.Deck, idempotencyKey, requestHash string) (reviewapp.CommandResult[domain.Deck], error) {
	if err := domain.ValidateDeck(deck); err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, err
	}
	if err := validateCommandBinding(idempotencyKey, requestHash); err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rollback(tx)
	if err := lockWorkspace(ctx, tx, deck.WorkspaceID); err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, err
	}
	if value, found, err := loadDeckCommand(ctx, tx, deck.WorkspaceID, idempotencyKey, requestHash, "CREATE_DECK"); err != nil || found {
		return reviewapp.CommandResult[domain.Deck]{Value: value, Replayed: found}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO learning.review_deck(id,workspace_id,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at) VALUES($1,$2,$3,$4,'ACTIVE',$5,$6,1,$7,$7)`, string(deck.ID), string(deck.WorkspaceID), deck.Name, deck.Scope, deck.DailyLimit, deck.SchedulerVersion, deck.CreatedAt.UTC()); err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, classify(err, domain.ErrorCodeDeckInvalid)
	}
	response, err := encodeJSON(deck)
	if err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, err
	}
	if err := insertCommand(ctx, tx, deck.WorkspaceID, idempotencyKey, requestHash, "CREATE_DECK", deck.ID, deck.Version, response, deck.CreatedAt); err != nil {
		return reviewapp.CommandResult[domain.Deck]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if value, found, recoveryErr := loadDeckCommand(ctx, repository.db, deck.WorkspaceID, idempotencyKey, requestHash, "CREATE_DECK"); recoveryErr == nil && found {
			return reviewapp.CommandResult[domain.Deck]{Value: value, Replayed: true}, nil
		}
		return reviewapp.CommandResult[domain.Deck]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.CommandResult[domain.Deck]{Value: deck}, nil
}

// GetDeck 按 Workspace 与 ID 读取牌组。
func (repository *Repository) GetDeck(ctx context.Context, workspaceID, deckID foundation.ID) (domain.Deck, error) {
	if repository == nil || nilValue(repository.db) {
		return domain.Deck{}, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review database is unavailable")
	}
	return loadDeck(ctx, repository.db, workspaceID, deckID, false)
}

// ListDecks 返回 Workspace 内的有界牌组列表。
func (repository *Repository) ListDecks(ctx context.Context, workspaceID foundation.ID, limit int) (reviewapp.DeckPage, error) {
	if repository == nil || nilValue(repository.db) {
		return reviewapp.DeckPage{}, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review database is unavailable")
	}
	rows, err := repository.db.Query(ctx, `SELECT id::text,workspace_id::text,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at FROM learning.review_deck WHERE workspace_id=$1 ORDER BY updated_at DESC,id DESC LIMIT $2`, string(workspaceID), limit)
	if err != nil {
		return reviewapp.DeckPage{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	page := reviewapp.DeckPage{Items: make([]domain.Deck, 0)}
	for rows.Next() {
		deck, err := scanDeck(rows)
		if err != nil {
			return reviewapp.DeckPage{}, classify(err, domain.ErrorCodeDependencyUnavailable)
		}
		page.Items = append(page.Items, deck)
	}
	return page, classify(rows.Err(), domain.ErrorCodeDependencyUnavailable)
}

// ListCards 返回 Workspace-scoped Deck 下的完整卡片管理投影。
func (repository *Repository) ListCards(ctx context.Context, workspaceID, deckID foundation.ID, limit int) (reviewapp.CardPage, error) {
	if repository == nil || nilValue(repository.db) {
		return reviewapp.CardPage{}, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review database is unavailable")
	}
	rows, err := repository.db.Query(ctx, `SELECT id::text,workspace_id::text,deck_id::text,claim_id::text,question,answer_points,evidence,card_type,difficulty,status,fingerprint,model_version,invalidation_reason,invalidated_at,version,created_at,updated_at FROM learning.review_card WHERE workspace_id=$1 AND deck_id=$2 ORDER BY updated_at DESC,id DESC LIMIT $3`, string(workspaceID), string(deckID), limit)
	if err != nil {
		return reviewapp.CardPage{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	page := reviewapp.CardPage{Items: make([]domain.Card, 0)}
	for rows.Next() {
		card, scanErr := scanCard(rows)
		if scanErr != nil {
			return reviewapp.CardPage{}, classify(scanErr, domain.ErrorCodeDependencyUnavailable)
		}
		page.Items = append(page.Items, card)
	}
	return page, classify(rows.Err(), domain.ErrorCodeDependencyUnavailable)
}

// CreateCard 持久化待审批卡片和命令 receipt。
func (repository *Repository) CreateCard(ctx context.Context, card domain.Card, idempotencyKey, requestHash string) (reviewapp.CommandResult[domain.Card], error) {
	if err := domain.ValidateCard(card); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if !validHash(card.Fingerprint) {
		return reviewapp.CommandResult[domain.Card]{}, invalid("card fingerprint is invalid")
	}
	if err := validateCommandBinding(idempotencyKey, requestHash); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rollback(tx)
	if err := lockWorkspace(ctx, tx, card.WorkspaceID); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if value, found, err := loadCardCommand(ctx, tx, card.WorkspaceID, idempotencyKey, requestHash, "CREATE_CARD"); err != nil || found {
		return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: found}, err
	}
	var deckID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM learning.review_deck WHERE workspace_id=$1 AND id=$2 AND status<>'ARCHIVED' FOR UPDATE`, string(card.WorkspaceID), string(card.DeckID)).Scan(&deckID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reviewapp.CommandResult[domain.Card]{}, notFound(domain.ErrorCodeDeckNotFound, "review deck was not found or archived")
		}
		return reviewapp.CommandResult[domain.Card]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	if card.ClaimID == nil {
		return reviewapp.CommandResult[domain.Card]{}, domain.ConflictError(domain.ErrorCodeEvidenceStale, "card claim is missing")
	}
	if err := verifyEvidence(ctx, tx, card.WorkspaceID, *card.ClaimID, card.Evidence, true); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	answerPoints, err := encodeJSON(card.AnswerPoints)
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	evidence, err := encodeJSON(card.Evidence)
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	claimID := any(nil)
	if card.ClaimID != nil {
		claimID = string(*card.ClaimID)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO learning.review_card(id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,status,fingerprint,model_version,version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'DRAFT',$10,$11,1,$12,$12)`, string(card.ID), string(card.WorkspaceID), string(card.DeckID), claimID, card.Question, answerPoints, evidence, string(card.CardType), card.Difficulty, card.Fingerprint, card.ModelVersion, card.CreatedAt.UTC()); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, classify(err, domain.ErrorCodeCardInvalid)
	}
	response, err := encodeJSON(card)
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if err := insertCommand(ctx, tx, card.WorkspaceID, idempotencyKey, requestHash, "CREATE_CARD", card.ID, card.Version, response, card.CreatedAt); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if value, found, recoveryErr := loadCardCommand(ctx, repository.db, card.WorkspaceID, idempotencyKey, requestHash, "CREATE_CARD"); recoveryErr == nil && found {
			return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: true}, nil
		}
		return reviewapp.CommandResult[domain.Card]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.CommandResult[domain.Card]{Value: card}, nil
}

// FindCardCreateReplay returns an exact create receipt before evidence is
// revalidated, preserving idempotent response-loss recovery.
func (repository *Repository) FindCardCreateReplay(ctx context.Context, workspaceID foundation.ID, idempotencyKey, requestHash string) (reviewapp.CommandResult[domain.Card], bool, error) {
	if repository == nil || nilValue(repository.db) {
		return reviewapp.CommandResult[domain.Card]{}, false, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review database is unavailable")
	}
	if err := validateCommandBinding(idempotencyKey, requestHash); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, err
	}
	value, found, err := loadCardCommand(ctx, repository.db, workspaceID, idempotencyKey, requestHash, "CREATE_CARD")
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: found}, found, nil
}

// GetCard 按 Workspace 与 ID 读取卡片。
func (repository *Repository) GetCard(ctx context.Context, workspaceID, cardID foundation.ID) (domain.Card, error) {
	if repository == nil || nilValue(repository.db) {
		return domain.Card{}, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review database is unavailable")
	}
	return loadCard(ctx, repository.db, workspaceID, cardID, false)
}

// FindCardEditReplay returns an exact edit receipt before mutable state is read.
func (repository *Repository) FindCardEditReplay(ctx context.Context, workspaceID foundation.ID, idempotencyKey, requestHash string) (reviewapp.CommandResult[domain.Card], bool, error) {
	if repository == nil || nilValue(repository.db) {
		return reviewapp.CommandResult[domain.Card]{}, false, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review database is unavailable")
	}
	if err := validateCommandBinding(idempotencyKey, requestHash); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, err
	}
	value, found, err := loadCardCommand(ctx, repository.db, workspaceID, idempotencyKey, requestHash, "EDIT_CARD")
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: found}, found, nil
}

// EditCard replaces the content of a card with a CAS-protected DRAFT and
// revalidates its Claim/Evidence under the same transaction that writes it.
func (repository *Repository) EditCard(ctx context.Context, record reviewapp.EditCardRecord) (reviewapp.CommandResult[domain.Card], error) {
	if err := domain.ValidateCard(record.Card); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if record.Card.Status != domain.CardStatusDraft || record.ExpectedVersion < 1 || record.Card.Version != record.ExpectedVersion+1 || !validHash(record.Card.Fingerprint) {
		return reviewapp.CommandResult[domain.Card]{}, invalid("card edit record is invalid")
	}
	if err := validateCommandBinding(record.IdempotencyKey, record.RequestHash); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rollback(tx)
	if err := lockWorkspace(ctx, tx, record.Card.WorkspaceID); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if value, found, err := loadCardCommand(ctx, tx, record.Card.WorkspaceID, record.IdempotencyKey, record.RequestHash, "EDIT_CARD"); err != nil || found {
		return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: found}, err
	}
	current, err := loadCard(ctx, tx, record.Card.WorkspaceID, record.Card.ID, true)
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if current.Version != record.ExpectedVersion {
		return reviewapp.CommandResult[domain.Card]{}, conflict(domain.ErrorCodeCardStateConflict, "card version did not match")
	}
	var deckID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM learning.review_deck WHERE workspace_id=$1 AND id=$2 AND status<>'ARCHIVED' FOR UPDATE`, string(record.Card.WorkspaceID), string(record.Card.DeckID)).Scan(&deckID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reviewapp.CommandResult[domain.Card]{}, notFound(domain.ErrorCodeDeckNotFound, "review deck was not found or archived")
		}
		return reviewapp.CommandResult[domain.Card]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	if record.Card.ClaimID == nil {
		return reviewapp.CommandResult[domain.Card]{}, domain.ConflictError(domain.ErrorCodeEvidenceStale, "card claim is missing")
	}
	if err := verifyEvidence(ctx, tx, record.Card.WorkspaceID, *record.Card.ClaimID, record.Card.Evidence, true); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	answerPoints, err := encodeJSON(record.Card.AnswerPoints)
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	evidence, err := encodeJSON(record.Card.Evidence)
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	updatedAt := record.At.UTC()
	if updatedAt.IsZero() {
		updatedAt = record.Card.UpdatedAt.UTC()
	}
	commandTag, err := tx.Exec(ctx, `UPDATE learning.review_card
SET claim_id=$4,question=$5,answer_points=$6,evidence=$7,card_type=$8,difficulty=$9,status='DRAFT',fingerprint=$10,model_version=$11,invalidation_reason=NULL,invalidated_at=NULL,version=$12,updated_at=$13
WHERE workspace_id=$1 AND id=$2 AND version=$3`, string(record.Card.WorkspaceID), string(record.Card.ID), record.ExpectedVersion, string(*record.Card.ClaimID), record.Card.Question, answerPoints, evidence, string(record.Card.CardType), record.Card.Difficulty, record.Card.Fingerprint, record.Card.ModelVersion, record.Card.Version, updatedAt)
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, classify(err, domain.ErrorCodeCardStateConflict)
	}
	if commandTag.RowsAffected() != 1 {
		return reviewapp.CommandResult[domain.Card]{}, conflict(domain.ErrorCodeCardStateConflict, "card CAS did not match")
	}
	response, err := encodeJSON(record.Card)
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if err := insertCommand(ctx, tx, record.Card.WorkspaceID, record.IdempotencyKey, record.RequestHash, "EDIT_CARD", record.Card.ID, record.Card.Version, response, updatedAt); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if value, found, recoveryErr := loadCardCommand(ctx, repository.db, record.Card.WorkspaceID, record.IdempotencyKey, record.RequestHash, "EDIT_CARD"); recoveryErr == nil && found {
			return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: true}, nil
		}
		return reviewapp.CommandResult[domain.Card]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.CommandResult[domain.Card]{Value: record.Card}, nil
}

// FindCardDecisionReplay 在读取当前卡片状态前返回历史决策快照。
func (repository *Repository) FindCardDecisionReplay(ctx context.Context, workspaceID foundation.ID, idempotencyKey, requestHash string) (reviewapp.CommandResult[domain.Card], bool, error) {
	if repository == nil || nilValue(repository.db) {
		return reviewapp.CommandResult[domain.Card]{}, false, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review database is unavailable")
	}
	if err := validateCommandBinding(idempotencyKey, requestHash); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, err
	}
	value, found, err := loadCardCommand(ctx, repository.db, workspaceID, idempotencyKey, requestHash, "DECIDE_CARD")
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, false, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: found}, found, nil
}

// DecideCard 执行审批、驳回或失效并保存命令 receipt。
func (repository *Repository) DecideCard(ctx context.Context, record reviewapp.CardDecisionRecord) (reviewapp.CommandResult[domain.Card], error) {
	if record.ExpectedVersion < 1 {
		return reviewapp.CommandResult[domain.Card]{}, invalid("card decision record is invalid")
	}
	if err := validateCommandBinding(record.IdempotencyKey, record.RequestHash); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rollback(tx)
	if err := lockWorkspace(ctx, tx, record.WorkspaceID); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if value, found, err := loadCardCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "DECIDE_CARD"); err != nil || found {
		return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: found}, err
	}
	current, err := loadCard(ctx, tx, record.WorkspaceID, record.CardID, true)
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if current.Version != record.ExpectedVersion {
		return reviewapp.CommandResult[domain.Card]{}, conflict(domain.ErrorCodeCardStateConflict, "card version did not match")
	}
	switch record.TargetStatus {
	case domain.CardStatusApproved, domain.CardStatusRejected:
		if current.Status != domain.CardStatusDraft {
			return reviewapp.CommandResult[domain.Card]{}, conflict(domain.ErrorCodeCardStateConflict, "only draft cards can be approved or rejected")
		}
	case domain.CardStatusInvalidated:
		if current.Status != domain.CardStatusApproved {
			return reviewapp.CommandResult[domain.Card]{}, conflict(domain.ErrorCodeCardStateConflict, "only approved cards can be invalidated")
		}
	default:
		return reviewapp.CommandResult[domain.Card]{}, invalid("card target status is invalid")
	}
	if record.TargetStatus == domain.CardStatusApproved && record.InitialSchedule == nil {
		return reviewapp.CommandResult[domain.Card]{}, invalid("approved card requires initial schedule")
	}
	if record.TargetStatus == domain.CardStatusInvalidated {
		if err := domain.ValidateInvalidationReason(record.Reason); err != nil {
			return reviewapp.CommandResult[domain.Card]{}, err
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
	if _, err := tx.Exec(ctx, `UPDATE learning.review_card SET status=$4,version=$5,updated_at=$6,invalidation_reason=$7,invalidated_at=$8 WHERE workspace_id=$1 AND id=$2 AND version=$3`, string(record.WorkspaceID), string(record.CardID), record.ExpectedVersion, string(record.TargetStatus), newVersion, now, invalidationReason, invalidatedAt); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, classify(err, domain.ErrorCodeCardStateConflict)
	}
	current.Status, current.Version, current.UpdatedAt = record.TargetStatus, newVersion, now
	if record.TargetStatus == domain.CardStatusApproved {
		if current.ClaimID == nil {
			return reviewapp.CommandResult[domain.Card]{}, domain.ConflictError(domain.ErrorCodeEvidenceStale, "card claim is missing")
		}
		if err := verifyEvidence(ctx, tx, current.WorkspaceID, *current.ClaimID, current.Evidence, true); err != nil {
			return reviewapp.CommandResult[domain.Card]{}, err
		}
		schedule := *record.InitialSchedule
		schedule.Difficulty = current.Difficulty
		if err := domain.ValidateSchedule(schedule); err != nil {
			return reviewapp.CommandResult[domain.Card]{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO learning.review_schedule(card_id,workspace_id,due_at,interval_days,stability,difficulty,last_reviewed_at,scheduler_version,paused,version) VALUES($1,$2,$3,$4,$5,$6,NULL,$7,false,1) ON CONFLICT(card_id) DO UPDATE SET due_at=EXCLUDED.due_at,interval_days=EXCLUDED.interval_days,stability=EXCLUDED.stability,difficulty=EXCLUDED.difficulty,last_reviewed_at=NULL,scheduler_version=EXCLUDED.scheduler_version,paused=false,version=learning.review_schedule.version+1`, string(schedule.CardID), string(schedule.WorkspaceID), schedule.DueAt.UTC(), schedule.IntervalDays, schedule.Stability, schedule.Difficulty, schedule.SchedulerVersion); err != nil {
			return reviewapp.CommandResult[domain.Card]{}, classify(err, domain.ErrorCodeScheduleConflict)
		}
	}
	if err := domain.ValidateCard(current); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	response, err := encodeJSON(current)
	if err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if err := insertCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "DECIDE_CARD", record.CardID, current.Version, response, now); err != nil {
		return reviewapp.CommandResult[domain.Card]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if value, found, recoveryErr := loadCardCommand(ctx, repository.db, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "DECIDE_CARD"); recoveryErr == nil && found {
			return reviewapp.CommandResult[domain.Card]{Value: value, Replayed: true}, nil
		}
		return reviewapp.CommandResult[domain.Card]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.CommandResult[domain.Card]{Value: current}, nil
}

const rankedDueCardsSQL = `
WITH answered_today AS (
    SELECT
        COALESCE(answer_session.deck_id,answered_card.deck_id) AS deck_id,
        count(*) AS answer_count
    FROM learning.review_answer AS answer
    JOIN learning.review_card AS answered_card
      ON answered_card.workspace_id=answer.workspace_id
     AND answered_card.id=answer.card_id
    JOIN learning.review_session AS answer_session
      ON answer_session.workspace_id=answer.workspace_id
     AND answer_session.id=answer.session_id
    WHERE answer.workspace_id=$1
      AND answer.created_at >= date_trunc('day',$2::timestamptz,'UTC')
      AND answer.created_at < date_trunc('day',$2::timestamptz,'UTC') + interval '1 day'
    GROUP BY COALESCE(answer_session.deck_id,answered_card.deck_id)
), due AS (
    SELECT
        c.id::text AS card_id,c.workspace_id::text AS card_workspace_id,c.deck_id::text AS deck_id,c.claim_id::text AS claim_id,c.question AS question,c.answer_points AS answer_points,c.evidence AS evidence,c.card_type AS card_type,c.difficulty AS card_difficulty,c.status AS card_status,c.fingerprint AS fingerprint,c.model_version AS model_version,c.invalidation_reason AS invalidation_reason,c.invalidated_at AS invalidated_at,c.version AS card_version,c.created_at AS card_created_at,c.updated_at AS card_updated_at,
        s.card_id::text AS schedule_card_id,s.workspace_id::text AS schedule_workspace_id,s.due_at AS due_at,s.interval_days AS interval_days,s.stability AS stability,s.difficulty AS schedule_difficulty,s.last_reviewed_at AS last_reviewed_at,s.scheduler_version AS scheduler_version,s.paused AS paused,s.version AS schedule_version,
        d.daily_limit,
        COALESCE(answered_today.answer_count,0) AS answered_today,
        row_number() OVER (PARTITION BY c.deck_id ORDER BY s.due_at ASC,c.id ASC) AS deck_position
    FROM learning.review_card c
    JOIN learning.review_schedule s ON s.workspace_id=c.workspace_id AND s.card_id=c.id
    JOIN learning.review_deck d ON d.workspace_id=c.workspace_id AND d.id=c.deck_id
    JOIN core.claim claim ON claim.workspace_id=c.workspace_id AND claim.id=c.claim_id AND claim.status='CONFIRMED'
    LEFT JOIN answered_today ON answered_today.deck_id=c.deck_id
    WHERE c.workspace_id=$1
      AND c.status='APPROVED'
      AND d.status='ACTIVE'
      AND s.paused=false
      AND s.due_at <= $2
      AND ($3::uuid IS NULL OR c.deck_id=$3::uuid)
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

// CheckAnswerEligibility fail-closes unless the card belongs to the exact
// server-ranked due set, including the owning Deck's daily limit.
func (repository *Repository) CheckAnswerEligibility(ctx context.Context, workspaceID, deckID, cardID foundation.ID, now time.Time) error {
	if repository == nil || nilValue(repository.db) {
		return domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review database is unavailable")
	}
	if now.IsZero() {
		return domain.InvalidError(domain.ErrorCodeScheduleInvalid, "answer eligibility time is invalid")
	}
	return checkAnswerEligibility(ctx, repository.db, workspaceID, deckID, cardID, now)
}

func checkAnswerEligibility(ctx context.Context, db rowQuerier, workspaceID, deckID, cardID foundation.ID, now time.Time) error {
	var eligible bool
	err := db.QueryRow(ctx, rankedDueCardsSQL+`
	SELECT EXISTS (
	    SELECT 1 FROM due
	    WHERE card_id=$4::text AND deck_position <= GREATEST(daily_limit-answered_today,0)
	)`, string(workspaceID), now.UTC(), string(deckID), string(cardID)).Scan(&eligible)
	if err != nil {
		return classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	if !eligible {
		return conflict(domain.ErrorCodeScheduleConflict, "card is not due within the deck daily limit")
	}
	return nil
}

// ListDue returns due cards with a server-owned per-Deck daily limit. Evidence
// reachability is checked in the query so stale bindings never leak into due.
func (repository *Repository) ListDue(ctx context.Context, workspaceID foundation.ID, deckID *foundation.ID, now time.Time, limit int) ([]reviewapp.DueCard, error) {
	query := rankedDueCardsSQL + `
SELECT card_id,card_workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,card_difficulty,card_status,fingerprint,model_version,invalidation_reason,invalidated_at,card_version,card_created_at,card_updated_at,
       schedule_card_id,schedule_workspace_id,due_at,interval_days,stability,schedule_difficulty,last_reviewed_at,scheduler_version,paused,schedule_version
FROM due
WHERE deck_position <= GREATEST(daily_limit-answered_today,0)
ORDER BY due_at ASC,card_id ASC
LIMIT $4`
	rows, err := repository.db.Query(ctx, query, string(workspaceID), now.UTC(), optionalID(deckID), limit)
	if err != nil {
		return nil, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	result := make([]reviewapp.DueCard, 0)
	for rows.Next() {
		card, schedule, err := scanCardAndSchedule(rows)
		if err != nil {
			return nil, classify(err, domain.ErrorCodeDependencyUnavailable)
		}
		result = append(result, reviewapp.DueCard{Card: card, Schedule: schedule})
	}
	return result, classify(rows.Err(), domain.ErrorCodeDependencyUnavailable)
}

func (repository *Repository) begin(ctx context.Context) (pgx.Tx, error) {
	if repository == nil || nilValue(repository.db) {
		return nil, errors.New("review database is unavailable")
	}
	return repository.db.Begin(ctx)
}

func rollback(tx pgx.Tx) { _ = tx.Rollback(context.Background()) }

func nilValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
