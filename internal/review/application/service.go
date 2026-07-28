package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

// Service 编排 Review 聚合、证据校验和 FSRS 调度。
type Service struct {
	repository Repository
	evidence   EvidenceVerifier
	scorer     Scorer
	// scorerVersion 冻结构造时已验证的评分器历史版本。
	scorerVersion string
	scheduler     domain.Scheduler
	ids           foundation.IDGenerator
	clock         foundation.Clock
	// questionRefs 签发并校验跨 API 实例稳定的答题快照引用。
	questionRefs *questionRefIssuer
}

// NewService 创建 Review 应用服务。
func NewService(repository Repository, evidence EvidenceVerifier, scorer Scorer, scheduler domain.Scheduler, questionRefKey string, ids foundation.IDGenerator, clock foundation.Clock) (*Service, error) {
	if repository == nil || evidence == nil || scorer == nil || scheduler == nil || ids == nil || clock == nil {
		return nil, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review service dependencies are unavailable")
	}
	scorerVersion := scorer.Version()
	if scorerVersion == "" || scorerVersion != strings.TrimSpace(scorerVersion) || len(scorerVersion) > domain.MaxScorerVersionBytes {
		return nil, domain.UnavailableError(domain.ErrorCodeScoringUnavailable, "review scorer version is missing")
	}
	if strings.TrimSpace(scheduler.Version()) == "" {
		return nil, domain.UnavailableError(domain.ErrorCodeSchedulerUnavailable, "review scheduler version is missing")
	}
	questionRefs, err := newQuestionRefIssuer(questionRefKey)
	if err != nil {
		return nil, err
	}
	return &Service{repository: repository, evidence: evidence, scorer: scorer, scorerVersion: scorerVersion, scheduler: scheduler, ids: ids, clock: clock, questionRefs: questionRefs}, nil
}

// CreateDeck 创建牌组并持久化幂等 receipt。
func (service *Service) CreateDeck(ctx context.Context, command CreateDeckCommand) (CommandResult[domain.Deck], error) {
	if err := service.available(); err != nil {
		return CommandResult[domain.Deck]{}, err
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return CommandResult[domain.Deck]{}, err
	}
	scope := command.Scope
	if len(scope) == 0 {
		scope = json.RawMessage(`{}`)
	}
	canonicalScope, err := domain.CanonicalJSON(scope)
	if err != nil {
		return CommandResult[domain.Deck]{}, err
	}
	if command.DailyLimit == 0 {
		command.DailyLimit = 20
	}
	id, err := service.ids.New()
	if err != nil {
		return CommandResult[domain.Deck]{}, err
	}
	now := service.clock.Now().UTC()
	deck := domain.Deck{ID: id, WorkspaceID: command.WorkspaceID, Name: command.Name, Scope: canonicalScope, Status: domain.DeckStatusActive, DailyLimit: command.DailyLimit, SchedulerVersion: service.scheduler.Version(), Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := domain.ValidateDeck(deck); err != nil {
		return CommandResult[domain.Deck]{}, err
	}
	hash, err := requestHash("CREATE_DECK", struct {
		WorkspaceID      foundation.ID   `json:"workspace_id"`
		Name             string          `json:"name"`
		Scope            json.RawMessage `json:"scope"`
		DailyLimit       int             `json:"daily_limit"`
		SchedulerVersion string          `json:"scheduler_version"`
	}{deck.WorkspaceID, deck.Name, deck.Scope, deck.DailyLimit, deck.SchedulerVersion})
	if err != nil {
		return CommandResult[domain.Deck]{}, err
	}
	return service.repository.CreateDeck(ctx, deck, command.IdempotencyKey, hash)
}

// GetDeck 返回 Workspace-scoped 牌组。
func (service *Service) GetDeck(ctx context.Context, workspaceID, deckID foundation.ID) (domain.Deck, error) {
	if err := service.available(); err != nil {
		return domain.Deck{}, err
	}
	return service.repository.GetDeck(ctx, workspaceID, deckID)
}

// ListDecks 返回有界牌组列表。
func (service *Service) ListDecks(ctx context.Context, workspaceID foundation.ID, limit int) (DeckPage, error) {
	if err := service.available(); err != nil {
		return DeckPage{}, err
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return DeckPage{}, domain.InvalidError(domain.ErrorCodeDeckInvalid, "deck list limit is invalid")
	}
	return service.repository.ListDecks(ctx, workspaceID, limit)
}

// ListCards 返回单一 Workspace Deck 下的有界完整卡片列表，供用户管理与审批。
func (service *Service) ListCards(ctx context.Context, workspaceID, deckID foundation.ID, limit int) (CardPage, error) {
	if err := service.available(); err != nil {
		return CardPage{}, err
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return CardPage{}, domain.InvalidError(domain.ErrorCodeCardInvalid, "card list limit is invalid")
	}
	if _, err := service.repository.GetDeck(ctx, workspaceID, deckID); err != nil {
		return CardPage{}, err
	}
	return service.repository.ListCards(ctx, workspaceID, deckID, limit)
}

// CreateCard 创建待审批卡片；卡片不会在审批前进入调度。
func (service *Service) CreateCard(ctx context.Context, command CreateCardCommand) (CommandResult[domain.Card], error) {
	if err := service.available(); err != nil {
		return CommandResult[domain.Card]{}, err
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return CommandResult[domain.Card]{}, err
	}
	id, err := service.ids.New()
	if err != nil {
		return CommandResult[domain.Card]{}, err
	}
	now := service.clock.Now().UTC()
	claimID := command.ClaimID
	card := domain.Card{ID: id, WorkspaceID: command.WorkspaceID, DeckID: command.DeckID, ClaimID: &claimID, Question: command.Question, AnswerPoints: append([]string(nil), command.AnswerPoints...), Evidence: append([]domain.EvidenceBinding(nil), command.Evidence...), CardType: command.CardType, Difficulty: command.Difficulty, Status: domain.CardStatusDraft, ModelVersion: strings.TrimSpace(command.ModelVersion), Version: 1, CreatedAt: now, UpdatedAt: now}
	if card.ModelVersion == "" {
		card.ModelVersion = "manual"
	}
	fingerprint, err := domain.ComputeCardFingerprint(card)
	if err != nil {
		return CommandResult[domain.Card]{}, err
	}
	card.Fingerprint = fingerprint
	hash, err := requestHash("CREATE_CARD", struct {
		WorkspaceID  foundation.ID `json:"workspace_id"`
		DeckID       foundation.ID `json:"deck_id"`
		Fingerprint  string        `json:"fingerprint"`
		ModelVersion string        `json:"model_version"`
	}{card.WorkspaceID, card.DeckID, card.Fingerprint, card.ModelVersion})
	if err != nil {
		return CommandResult[domain.Card]{}, err
	}
	if replay, found, err := service.repository.FindCardCreateReplay(ctx, command.WorkspaceID, command.IdempotencyKey, hash); err != nil || found {
		return replay, err
	}
	if err := service.evidence.VerifyCardEvidence(ctx, card.WorkspaceID, claimID, card.Evidence); err != nil {
		return service.replayCardCreateOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash, err)
	}
	return service.repository.CreateCard(ctx, card, command.IdempotencyKey, hash)
}

// EditCard replaces a card's content after strict evidence validation and
// deliberately returns it to DRAFT, so any changed evidence needs reapproval.
func (service *Service) EditCard(ctx context.Context, command EditCardCommand) (CommandResult[domain.Card], error) {
	if err := service.available(); err != nil {
		return CommandResult[domain.Card]{}, err
	}
	if command.ExpectedVersion < 1 {
		return CommandResult[domain.Card]{}, domain.InvalidError(domain.ErrorCodeCardStateConflict, "card expected version is invalid")
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return CommandResult[domain.Card]{}, err
	}
	hash, err := requestHash("EDIT_CARD", struct {
		WorkspaceID     foundation.ID            `json:"workspace_id"`
		CardID          foundation.ID            `json:"card_id"`
		ExpectedVersion int64                    `json:"expected_version"`
		ClaimID         foundation.ID            `json:"claim_id"`
		Question        string                   `json:"question"`
		AnswerPoints    []string                 `json:"answer_points"`
		Evidence        []domain.EvidenceBinding `json:"evidence"`
		CardType        domain.CardType          `json:"card_type"`
		Difficulty      float64                  `json:"difficulty"`
		ModelVersion    string                   `json:"model_version"`
	}{command.WorkspaceID, command.CardID, command.ExpectedVersion, command.ClaimID, command.Question, command.AnswerPoints, command.Evidence, command.CardType, command.Difficulty, strings.TrimSpace(command.ModelVersion)})
	if err != nil {
		return CommandResult[domain.Card]{}, err
	}
	if replay, found, err := service.repository.FindCardEditReplay(ctx, command.WorkspaceID, command.IdempotencyKey, hash); err != nil || found {
		return replay, err
	}
	current, err := service.repository.GetCard(ctx, command.WorkspaceID, command.CardID)
	if err != nil {
		return CommandResult[domain.Card]{}, err
	}
	if current.Version != command.ExpectedVersion {
		return service.replayCardEditOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash,
			domain.ConflictError(domain.ErrorCodeCardStateConflict, "card version did not match"))
	}
	modelVersion := strings.TrimSpace(command.ModelVersion)
	if modelVersion == "" {
		modelVersion = "manual"
	}
	claimID := command.ClaimID
	now := service.clock.Now().UTC()
	updated := domain.Card{
		ID: current.ID, WorkspaceID: current.WorkspaceID, DeckID: current.DeckID, ClaimID: &claimID,
		Question: command.Question, AnswerPoints: append([]string(nil), command.AnswerPoints...), Evidence: append([]domain.EvidenceBinding(nil), command.Evidence...),
		CardType: command.CardType, Difficulty: command.Difficulty, Status: domain.CardStatusDraft, ModelVersion: modelVersion,
		Version: current.Version + 1, CreatedAt: current.CreatedAt, UpdatedAt: now,
	}
	fingerprint, err := domain.ComputeCardFingerprint(updated)
	if err != nil {
		return CommandResult[domain.Card]{}, err
	}
	updated.Fingerprint = fingerprint
	if err := service.evidence.VerifyCardEvidence(ctx, updated.WorkspaceID, claimID, updated.Evidence); err != nil {
		return service.replayCardEditOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash, err)
	}
	return service.repository.EditCard(ctx, EditCardRecord{Card: updated, ExpectedVersion: command.ExpectedVersion, IdempotencyKey: command.IdempotencyKey, RequestHash: hash, At: now})
}

// ApproveCard 在证据仍可达且 Claim 仍为正式知识时激活卡片和初始调度。
func (service *Service) ApproveCard(ctx context.Context, command CardDecisionCommand) (CommandResult[domain.Card], error) {
	if err := service.available(); err != nil {
		return CommandResult[domain.Card]{}, err
	}
	hash, err := cardDecisionRequestHash(command, domain.CardStatusApproved)
	if err != nil {
		return CommandResult[domain.Card]{}, err
	}
	if replay, found, err := service.repository.FindCardDecisionReplay(ctx, command.WorkspaceID, command.IdempotencyKey, hash); err != nil || found {
		return replay, err
	}
	card, err := service.repository.GetCard(ctx, command.WorkspaceID, command.CardID)
	if err != nil {
		return CommandResult[domain.Card]{}, err
	}
	if card.Status != domain.CardStatusDraft || card.Version != command.ExpectedVersion || card.ClaimID == nil {
		return service.replayCardDecisionOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash,
			domain.ConflictError(domain.ErrorCodeCardStateConflict, "card is not an approvable draft"))
	}
	if err := service.evidence.VerifyCardEvidence(ctx, command.WorkspaceID, *card.ClaimID, card.Evidence); err != nil {
		return service.replayCardDecisionOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash, err)
	}
	now := service.clock.Now().UTC()
	schedule := domain.Schedule{CardID: card.ID, WorkspaceID: card.WorkspaceID, DueAt: now, IntervalDays: 0, Stability: 0, Difficulty: card.Difficulty, SchedulerVersion: service.scheduler.Version(), Version: 1}
	if err := domain.ValidateSchedule(schedule); err != nil {
		return CommandResult[domain.Card]{}, err
	}
	return service.decideCardWithHash(ctx, command, domain.CardStatusApproved, &schedule, now, hash)
}

// RejectCard 驳回待审批卡片且不创建调度。
func (service *Service) RejectCard(ctx context.Context, command CardDecisionCommand) (CommandResult[domain.Card], error) {
	if err := service.available(); err != nil {
		return CommandResult[domain.Card]{}, err
	}
	return service.decideCard(ctx, command, domain.CardStatusRejected, nil, service.clock.Now().UTC())
}

// InvalidateCard 使已审批卡片停止参与复习。
func (service *Service) InvalidateCard(ctx context.Context, command CardDecisionCommand) (CommandResult[domain.Card], error) {
	if err := service.available(); err != nil {
		return CommandResult[domain.Card]{}, err
	}
	if err := domain.ValidateInvalidationReason(strings.TrimSpace(command.Reason)); err != nil {
		return CommandResult[domain.Card]{}, err
	}
	return service.decideCard(ctx, command, domain.CardStatusInvalidated, nil, service.clock.Now().UTC())
}

// InvalidateCards invalidates the next bounded batch made unsafe by a Claim,
// source version, source span, or severe conflict. HasMore tells the caller to
// continue with a new Idempotency-Key until the selector is drained.
func (service *Service) InvalidateCards(ctx context.Context, command InvalidateCardsCommand) (InvalidationResult, error) {
	if err := service.available(); err != nil {
		return InvalidationResult{}, err
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return InvalidationResult{}, err
	}
	if err := domain.ValidateInvalidationReason(strings.TrimSpace(command.Reason)); err != nil {
		return InvalidationResult{}, err
	}
	if err := validateInvalidationTarget(command); err != nil {
		return InvalidationResult{}, err
	}
	hash, err := requestHash("INVALIDATE_CARDS", struct {
		WorkspaceID     foundation.ID  `json:"workspace_id"`
		ClaimID         *foundation.ID `json:"claim_id,omitempty"`
		SourceVersionID *foundation.ID `json:"source_version_id,omitempty"`
		SourceSpanID    *foundation.ID `json:"source_span_id,omitempty"`
		Reason          string         `json:"reason"`
	}{command.WorkspaceID, command.ClaimID, command.SourceVersionID, command.SourceSpanID, strings.TrimSpace(command.Reason)})
	if err != nil {
		return InvalidationResult{}, err
	}
	return service.repository.InvalidateCards(ctx, InvalidateCardsRecord{
		WorkspaceID: command.WorkspaceID, ClaimID: command.ClaimID, SourceVersionID: command.SourceVersionID,
		SourceSpanID: command.SourceSpanID, Reason: strings.TrimSpace(command.Reason), IdempotencyKey: command.IdempotencyKey,
		RequestHash: hash, At: service.clock.Now().UTC(), BatchSize: MaxInvalidationBatchSize,
	})
}

func (service *Service) decideCard(ctx context.Context, command CardDecisionCommand, target domain.CardStatus, initial *domain.Schedule, at time.Time) (CommandResult[domain.Card], error) {
	hash, err := cardDecisionRequestHash(command, target)
	if err != nil {
		return CommandResult[domain.Card]{}, err
	}
	return service.decideCardWithHash(ctx, command, target, initial, at, hash)
}

func (service *Service) decideCardWithHash(ctx context.Context, command CardDecisionCommand, target domain.CardStatus, initial *domain.Schedule, at time.Time, hash string) (CommandResult[domain.Card], error) {
	return service.repository.DecideCard(ctx, CardDecisionRecord{WorkspaceID: command.WorkspaceID, CardID: command.CardID, ExpectedVersion: command.ExpectedVersion, TargetStatus: target, InitialSchedule: initial, IdempotencyKey: command.IdempotencyKey, RequestHash: hash, At: at, Reason: strings.TrimSpace(command.Reason)})
}

func cardDecisionRequestHash(command CardDecisionCommand, target domain.CardStatus) (string, error) {
	if command.ExpectedVersion < 1 {
		return "", domain.InvalidError(domain.ErrorCodeCardStateConflict, "card expected version is invalid")
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return "", err
	}
	return requestHash("DECIDE_CARD", struct {
		WorkspaceID     foundation.ID     `json:"workspace_id"`
		CardID          foundation.ID     `json:"card_id"`
		ExpectedVersion int64             `json:"expected_version"`
		Target          domain.CardStatus `json:"target_status"`
		Reason          string            `json:"reason"`
	}{command.WorkspaceID, command.CardID, command.ExpectedVersion, target, strings.TrimSpace(command.Reason)})
}

func validateInvalidationTarget(command InvalidateCardsCommand) error {
	if command.ClaimID == nil && command.SourceVersionID == nil && command.SourceSpanID == nil {
		return domain.InvalidError(domain.ErrorCodeInvalidationInvalid, "an invalidation target is required")
	}
	for _, value := range []*foundation.ID{command.ClaimID, command.SourceVersionID, command.SourceSpanID} {
		if value == nil {
			continue
		}
		parsed, err := foundation.ParseID(string(*value))
		if err != nil || parsed != *value {
			return domain.InvalidError(domain.ErrorCodeInvalidationInvalid, "invalidation target is invalid")
		}
	}
	return nil
}

// ListDue 返回绑定到活动 Review Session 的到期卡片。
func (service *Service) ListDue(ctx context.Context, workspaceID, sessionID foundation.ID, deckID *foundation.ID, limit int) ([]DueCard, error) {
	if err := service.available(); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 200 {
		return nil, domain.InvalidError(domain.ErrorCodeScheduleInvalid, "due card limit is invalid")
	}
	session, err := service.repository.GetSession(ctx, workspaceID, sessionID)
	if err != nil {
		return nil, err
	}
	if err := validateReviewSessionBinding(session); err != nil {
		return nil, err
	}
	if session.Status != domain.SessionStatusActive {
		return nil, domain.ConflictError(domain.ErrorCodeSessionClosed, "session is not active")
	}
	if deckID != nil && *deckID != *session.DeckID {
		return nil, domain.ConflictError(domain.ErrorCodeSessionTypeConflict, "due deck does not match the review session")
	}
	items, err := service.repository.ListDue(ctx, workspaceID, session.DeckID, service.clock.Now().UTC(), limit)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].QuestionRef, err = service.questionRefs.issue(session.ID, items[index].Card, items[index].Schedule)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

// StartSession 创建绑定 Deck 的复习会话；重复请求返回同一会话。
func (service *Service) StartSession(ctx context.Context, command StartSessionCommand) (CommandResult[domain.Session], error) {
	if err := service.available(); err != nil {
		return CommandResult[domain.Session]{}, err
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return CommandResult[domain.Session]{}, err
	}
	if command.SessionType != domain.SessionTypeReview || command.DeckID == nil {
		return CommandResult[domain.Session]{}, domain.InvalidError(domain.ErrorCodeSessionInvalid, "review session requires REVIEW type and a deck")
	}
	config := command.Config
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	canonicalConfig, err := domain.CanonicalJSON(config)
	if err != nil {
		return CommandResult[domain.Session]{}, err
	}
	id, err := service.ids.New()
	if err != nil {
		return CommandResult[domain.Session]{}, err
	}
	session := domain.Session{ID: id, WorkspaceID: command.WorkspaceID, DeckID: command.DeckID, SessionType: command.SessionType, Status: domain.SessionStatusActive, Config: canonicalConfig, IdempotencyKey: command.IdempotencyKey, StartedAt: service.clock.Now().UTC()}
	if err := domain.ValidateSession(session); err != nil {
		return CommandResult[domain.Session]{}, err
	}
	hash, err := requestHash("START_SESSION", struct {
		WorkspaceID foundation.ID      `json:"workspace_id"`
		DeckID      *foundation.ID     `json:"deck_id,omitempty"`
		SessionType domain.SessionType `json:"session_type"`
		Config      json.RawMessage    `json:"config"`
	}{session.WorkspaceID, session.DeckID, session.SessionType, session.Config})
	if err != nil {
		return CommandResult[domain.Session]{}, err
	}
	return service.repository.StartSession(ctx, session, hash)
}

// SubmitAnswer obtains a trusted server-side Score, then atomically writes the
// Answer and the next FSRS state. No caller-controlled score enters this path.
func (service *Service) SubmitAnswer(ctx context.Context, command SubmitAnswerCommand) (AnswerResult, error) {
	if err := service.available(); err != nil {
		return AnswerResult{}, err
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return AnswerResult{}, err
	}
	cardID := command.CardID
	answer := domain.Answer{
		WorkspaceID: command.WorkspaceID, SessionID: command.SessionID, CardID: &cardID,
		QuestionRef: strings.TrimSpace(command.QuestionRef), IdempotencyKey: command.IdempotencyKey,
		UserAnswer: command.UserAnswer, Rating: command.Rating,
	}
	if err := domain.ValidateAnswerInput(answer); err != nil {
		return AnswerResult{}, err
	}
	hash, err := requestHash("SUBMIT_ANSWER", struct {
		WorkspaceID foundation.ID `json:"workspace_id"`
		SessionID   foundation.ID `json:"session_id"`
		CardID      foundation.ID `json:"card_id"`
		QuestionRef string        `json:"question_ref"`
		UserAnswer  string        `json:"user_answer"`
		Rating      domain.Rating `json:"rating"`
	}{answer.WorkspaceID, answer.SessionID, cardID, answer.QuestionRef, answer.UserAnswer, answer.Rating})
	if err != nil {
		return AnswerResult{}, err
	}
	if replay, found, err := service.repository.FindAnswerReplay(ctx, command.WorkspaceID, command.IdempotencyKey, hash); err != nil || found {
		return replay, err
	}
	session, err := service.repository.GetSession(ctx, command.WorkspaceID, command.SessionID)
	if err != nil {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash, err)
	}
	if err := validateReviewSessionBinding(session); err != nil {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash, err)
	}
	if session.Status != domain.SessionStatusActive {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash,
			domain.ConflictError(domain.ErrorCodeSessionClosed, "session is not active"))
	}
	card, err := service.repository.GetCard(ctx, command.WorkspaceID, command.CardID)
	if err != nil {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash, err)
	}
	if !domain.IsActiveCard(card) {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash,
			domain.ConflictError(domain.ErrorCodeCardInactive, "card is not active"))
	}
	if *session.DeckID != card.DeckID {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash,
			domain.ConflictError(domain.ErrorCodeCardInactive, "card does not belong to the session deck"))
	}
	schedule, err := service.repository.GetSchedule(ctx, command.WorkspaceID, command.CardID)
	if err != nil {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash, err)
	}
	if schedule.Paused {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash,
			domain.ConflictError(domain.ErrorCodeScheduleConflict, "card schedule is paused"))
	}
	if err := service.questionRefs.verify(answer.QuestionRef, session.ID, card, schedule); err != nil {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash, err)
	}
	if card.ClaimID == nil {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash,
			domain.ConflictError(domain.ErrorCodeEvidenceStale, "card claim is missing"))
	}
	if err := service.evidence.VerifyCardEvidence(ctx, command.WorkspaceID, *card.ClaimID, card.Evidence); err != nil {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash, err)
	}
	now := service.clock.Now().UTC()
	if err := service.repository.CheckAnswerEligibility(ctx, command.WorkspaceID, card.DeckID, command.CardID, now); err != nil {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash, err)
	}
	score, err := service.scorer.Score(ctx, ScoreInput{Card: card, UserAnswer: answer.UserAnswer, Rating: answer.Rating})
	if err != nil {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash,
			domain.UnavailableError(domain.ErrorCodeScoringUnavailable, "trusted review scorer is unavailable"))
	}
	if err := validateScoreEvidence(card, score); err != nil {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash, err)
	}
	answer.Score = score
	answer.ScorerVersion = service.scorerVersion
	answer.Feedback = scoreFeedback(score)
	id, err := service.ids.New()
	if err != nil {
		return AnswerResult{}, err
	}
	answer.ID, answer.CreatedAt = id, now
	if err := domain.ValidateAnswer(answer); err != nil {
		return AnswerResult{}, err
	}
	decision, err := service.scheduler.Next(domain.ScheduleInput{Now: now, Rating: answer.Rating, Previous: &schedule})
	if err != nil {
		return service.replayAnswerOr(ctx, command.WorkspaceID, command.IdempotencyKey, hash,
			domain.UnavailableError(domain.ErrorCodeSchedulerUnavailable, "scheduler could not produce a decision"))
	}
	return service.repository.SubmitAnswer(ctx, SubmitAnswerRecord{Answer: answer, ExpectedCardVersion: card.Version, ExpectedCardFingerprint: card.Fingerprint, ExpectedScheduleVersion: schedule.Version, ScheduleDecision: decision, RequestHash: hash})
}

// CompleteSession 将活动会话结束为 completed 或 cancelled，并持久化幂等 receipt。
func (service *Service) CompleteSession(ctx context.Context, command CompleteSessionCommand) (CommandResult[domain.Session], error) {
	if err := service.available(); err != nil {
		return CommandResult[domain.Session]{}, err
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return CommandResult[domain.Session]{}, err
	}
	session, err := service.repository.GetSession(ctx, command.WorkspaceID, command.SessionID)
	if err != nil {
		return CommandResult[domain.Session]{}, err
	}
	if err := validateReviewSessionBinding(session); err != nil {
		return CommandResult[domain.Session]{}, err
	}
	status := domain.SessionStatusCompleted
	if command.Cancelled {
		status = domain.SessionStatusCancelled
	}
	hash, err := requestHash("COMPLETE_SESSION", struct {
		WorkspaceID foundation.ID `json:"workspace_id"`
		SessionID   foundation.ID `json:"session_id"`
		Cancelled   bool          `json:"cancelled"`
	}{command.WorkspaceID, command.SessionID, command.Cancelled})
	if err != nil {
		return CommandResult[domain.Session]{}, err
	}
	return service.repository.CompleteSession(ctx, CompleteSessionRecord{WorkspaceID: command.WorkspaceID, SessionID: command.SessionID, Status: status, IdempotencyKey: command.IdempotencyKey, RequestHash: hash, At: service.clock.Now().UTC()})
}

func validateReviewSessionBinding(session domain.Session) error {
	if !domain.IsDeckBoundReviewSession(session) {
		return domain.ConflictError(domain.ErrorCodeSessionTypeConflict, "review operation requires a deck-bound REVIEW session")
	}
	return nil
}

// ChangeDeckSchedule 暂停、恢复或重置整个牌组的调度。
func (service *Service) ChangeDeckSchedule(ctx context.Context, command DeckScheduleCommand, action DeckScheduleAction) (CommandResult[domain.Deck], error) {
	if err := service.available(); err != nil {
		return CommandResult[domain.Deck]{}, err
	}
	if action != DeckSchedulePause && action != DeckScheduleResume && action != DeckScheduleReset {
		return CommandResult[domain.Deck]{}, domain.InvalidError(domain.ErrorCodeScheduleInvalid, "deck schedule action is invalid")
	}
	if command.ExpectedVersion < 1 {
		return CommandResult[domain.Deck]{}, domain.InvalidError(domain.ErrorCodeDeckInvalid, "deck expected version is invalid")
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return CommandResult[domain.Deck]{}, err
	}
	hash, err := requestHash("CHANGE_DECK_SCHEDULE", struct {
		WorkspaceID     foundation.ID      `json:"workspace_id"`
		DeckID          foundation.ID      `json:"deck_id"`
		ExpectedVersion int64              `json:"expected_version"`
		Action          DeckScheduleAction `json:"action"`
	}{command.WorkspaceID, command.DeckID, command.ExpectedVersion, action})
	if err != nil {
		return CommandResult[domain.Deck]{}, err
	}
	return service.repository.ChangeDeckSchedule(ctx, DeckScheduleRecord{WorkspaceID: command.WorkspaceID, DeckID: command.DeckID, ExpectedVersion: command.ExpectedVersion, Action: action, IdempotencyKey: command.IdempotencyKey, RequestHash: hash, At: service.clock.Now().UTC()})
}

func (service *Service) available() error {
	if service == nil || service.repository == nil || service.evidence == nil || service.scorer == nil || service.scorerVersion == "" || service.scheduler == nil || service.ids == nil || service.clock == nil || service.questionRefs == nil {
		return domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review service is unavailable")
	}
	return nil
}

func (service *Service) replayCardCreateOr(ctx context.Context, workspaceID foundation.ID, idempotencyKey, requestHash string, fallback error) (CommandResult[domain.Card], error) {
	replay, found, err := service.repository.FindCardCreateReplay(ctx, workspaceID, idempotencyKey, requestHash)
	if err != nil || found {
		return replay, err
	}
	return CommandResult[domain.Card]{}, fallback
}

func (service *Service) replayCardEditOr(ctx context.Context, workspaceID foundation.ID, idempotencyKey, requestHash string, fallback error) (CommandResult[domain.Card], error) {
	replay, found, err := service.repository.FindCardEditReplay(ctx, workspaceID, idempotencyKey, requestHash)
	if err != nil || found {
		return replay, err
	}
	return CommandResult[domain.Card]{}, fallback
}

func (service *Service) replayCardDecisionOr(ctx context.Context, workspaceID foundation.ID, idempotencyKey, requestHash string, fallback error) (CommandResult[domain.Card], error) {
	replay, found, err := service.repository.FindCardDecisionReplay(ctx, workspaceID, idempotencyKey, requestHash)
	if err != nil || found {
		return replay, err
	}
	return CommandResult[domain.Card]{}, fallback
}

func (service *Service) replayAnswerOr(ctx context.Context, workspaceID foundation.ID, idempotencyKey, requestHash string, fallback error) (AnswerResult, error) {
	replay, found, err := service.repository.FindAnswerReplay(ctx, workspaceID, idempotencyKey, requestHash)
	if err != nil || found {
		return replay, err
	}
	return AnswerResult{}, fallback
}

func validateScoreEvidence(card domain.Card, score domain.Score) error {
	if err := domain.ValidateScore(score); err != nil {
		return err
	}
	if len(score.Evidence) == 0 {
		return domain.InvalidError(domain.ErrorCodeEvidenceMissing, "score must cite card evidence")
	}
	allowed := make(map[domain.EvidenceBinding]struct{}, len(card.Evidence))
	for _, item := range card.Evidence {
		allowed[item] = struct{}{}
	}
	seen := make(map[domain.EvidenceBinding]struct{}, len(score.Evidence))
	for _, item := range score.Evidence {
		if _, ok := allowed[item]; !ok {
			return domain.InvalidError(domain.ErrorCodeEvidenceInvalid, "score cites evidence outside the card binding")
		}
		if _, duplicate := seen[item]; duplicate {
			return domain.InvalidError(domain.ErrorCodeEvidenceDuplicate, "score evidence must be unique")
		}
		seen[item] = struct{}{}
	}
	return nil
}

func scoreFeedback(score domain.Score) map[string]any {
	return map[string]any{
		"schema_version": score.SchemaVersion,
		"errors":         append([]string(nil), score.Errors...),
		"omissions":      append([]string(nil), score.Omissions...),
		"evidence_count": len(score.Evidence),
	}
}

func requestHash(operation string, value any) (string, error) {
	encoded, err := json.Marshal(struct {
		Operation string `json:"operation"`
		Payload   any    `json:"payload"`
	}{operation, value})
	if err != nil {
		return "", fmt.Errorf("encode review request: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
