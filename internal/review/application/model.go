// Package application 编排 Review Deck、Card、Session、Answer 与 FSRS 调度。
package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

const (
	// MaxInvalidationBatchSize 限制一次知识生命周期命令失效并写入 receipt 的 Card 数量。
	MaxInvalidationBatchSize = 200
)

// CreateDeckCommand 创建一个 Review Deck。
type CreateDeckCommand struct {
	WorkspaceID    foundation.ID
	Name           string
	Scope          json.RawMessage
	DailyLimit     int
	IdempotencyKey string
}

// CreateCardCommand 创建一张待审批且绑定证据的 Review Card。
type CreateCardCommand struct {
	WorkspaceID    foundation.ID
	DeckID         foundation.ID
	ClaimID        foundation.ID
	Question       string
	AnswerPoints   []string
	Evidence       []domain.EvidenceBinding
	CardType       domain.CardType
	Difficulty     float64
	ModelVersion   string
	IdempotencyKey string
}

// EditCardCommand replaces a card's learning content and returns it to DRAFT.
// The caller must approve the new evidence binding before it becomes due again.
type EditCardCommand struct {
	WorkspaceID     foundation.ID
	CardID          foundation.ID
	ExpectedVersion int64
	ClaimID         foundation.ID
	Question        string
	AnswerPoints    []string
	Evidence        []domain.EvidenceBinding
	CardType        domain.CardType
	Difficulty      float64
	ModelVersion    string
	IdempotencyKey  string
}

// CardDecisionCommand 对卡片执行审批、驳回或失效决策。
type CardDecisionCommand struct {
	WorkspaceID     foundation.ID
	CardID          foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
	Reason          string
}

// InvalidateCardsCommand invalidates the next bounded batch matching a narrow
// knowledge lifecycle selector. At least one selector must be present.
type InvalidateCardsCommand struct {
	WorkspaceID     foundation.ID
	ClaimID         *foundation.ID
	SourceVersionID *foundation.ID
	SourceSpanID    *foundation.ID
	Reason          string
	IdempotencyKey  string
}

// StartSessionCommand 创建可恢复的复习会话。
type StartSessionCommand struct {
	WorkspaceID    foundation.ID
	DeckID         *foundation.ID
	SessionType    domain.SessionType
	Config         json.RawMessage
	IdempotencyKey string
}

// SubmitAnswerCommand 只接收用户答案与自评难度；可信评分始终由服务端生成。
type SubmitAnswerCommand struct {
	WorkspaceID    foundation.ID
	SessionID      foundation.ID
	CardID         foundation.ID
	QuestionRef    string
	UserAnswer     string
	Rating         domain.Rating
	IdempotencyKey string
}

// CompleteSessionCommand 结束一段会话。
type CompleteSessionCommand struct {
	WorkspaceID    foundation.ID
	SessionID      foundation.ID
	Cancelled      bool
	IdempotencyKey string
}

// DeckScheduleCommand 暂停、恢复或重置牌组调度。
type DeckScheduleCommand struct {
	WorkspaceID     foundation.ID
	DeckID          foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

// CommandResult 表示带持久幂等重放标记的聚合结果。
type CommandResult[T any] struct {
	Value    T
	Replayed bool
}

// AnswerResult 包含答题事实和同事务提交的调度状态。
type AnswerResult struct {
	Answer   domain.Answer
	Schedule domain.Schedule
	Replayed bool
}

// InvalidationResult 返回一次有界知识生命周期失效的摘要。
type InvalidationResult struct {
	InvalidatedCount int
	HasMore          bool
	Replayed         bool
}

// DueCard 是今日复习查询返回的卡片与调度快照。
type DueCard struct {
	Card     domain.Card
	Schedule domain.Schedule
	// QuestionRef 是服务端签发且绑定当前会话、卡片与调度快照的 opaque 引用。
	QuestionRef string
}

// DeckPage 是稳定排序的有界牌组结果。
type DeckPage struct {
	Items []domain.Deck
}

// CardPage 是单一 Deck 下稳定排序的有界卡片结果。
type CardPage struct {
	Items []domain.Card
}

// Repository 是 Review 聚合和答题原子事务的持久化端口。
type Repository interface {
	CreateDeck(context.Context, domain.Deck, string, string) (CommandResult[domain.Deck], error)
	GetDeck(context.Context, foundation.ID, foundation.ID) (domain.Deck, error)
	ListDecks(context.Context, foundation.ID, int) (DeckPage, error)
	ListCards(context.Context, foundation.ID, foundation.ID, int) (CardPage, error)
	CreateCard(context.Context, domain.Card, string, string) (CommandResult[domain.Card], error)
	FindCardCreateReplay(context.Context, foundation.ID, string, string) (CommandResult[domain.Card], bool, error)
	GetCard(context.Context, foundation.ID, foundation.ID) (domain.Card, error)
	FindCardEditReplay(context.Context, foundation.ID, string, string) (CommandResult[domain.Card], bool, error)
	EditCard(context.Context, EditCardRecord) (CommandResult[domain.Card], error)
	FindCardDecisionReplay(context.Context, foundation.ID, string, string) (CommandResult[domain.Card], bool, error)
	DecideCard(context.Context, CardDecisionRecord) (CommandResult[domain.Card], error)
	InvalidateCards(context.Context, InvalidateCardsRecord) (InvalidationResult, error)
	ListDue(context.Context, foundation.ID, *foundation.ID, time.Time, int) ([]DueCard, error)
	StartSession(context.Context, domain.Session, string) (CommandResult[domain.Session], error)
	GetSession(context.Context, foundation.ID, foundation.ID) (domain.Session, error)
	CompleteSession(context.Context, CompleteSessionRecord) (CommandResult[domain.Session], error)
	GetSchedule(context.Context, foundation.ID, foundation.ID) (domain.Schedule, error)
	CheckAnswerEligibility(context.Context, foundation.ID, foundation.ID, foundation.ID, time.Time) error
	FindAnswerReplay(context.Context, foundation.ID, string, string) (AnswerResult, bool, error)
	SubmitAnswer(context.Context, SubmitAnswerRecord) (AnswerResult, error)
	ChangeDeckSchedule(context.Context, DeckScheduleRecord) (CommandResult[domain.Deck], error)
}

// CompleteSessionRecord 是带持久幂等 receipt 的会话完成写入输入。
type CompleteSessionRecord struct {
	WorkspaceID    foundation.ID
	SessionID      foundation.ID
	Status         domain.SessionStatus
	IdempotencyKey string
	RequestHash    string
	At             time.Time
}

// EvidenceVerifier 校验 Claim 仍为正式知识且证据片段仍可达。
type EvidenceVerifier interface {
	VerifyCardEvidence(context.Context, foundation.ID, foundation.ID, []domain.EvidenceBinding) error
}

// ScoreInput is the controlled context passed to a trusted server-side scorer.
// It intentionally has no caller-supplied Score or Feedback field.
type ScoreInput struct {
	Card       domain.Card
	UserAnswer string
	Rating     domain.Rating
}

// Scorer evaluates a validated Card and user answer without writing learning facts.
type Scorer interface {
	// Version identifies the frozen scorer implementation that emitted the Score.
	Version() string
	// Score returns a fully evidence-bound Score or an error; callers fail closed.
	Score(context.Context, ScoreInput) (domain.Score, error)
}

// CardDecisionRecord 是持久层审批/驳回/失效的完整写入记录。
type CardDecisionRecord struct {
	WorkspaceID     foundation.ID
	CardID          foundation.ID
	ExpectedVersion int64
	TargetStatus    domain.CardStatus
	InitialSchedule *domain.Schedule
	Reason          string
	IdempotencyKey  string
	RequestHash     string
	At              time.Time
}

// EditCardRecord is the CAS-protected replacement of a card's review content.
type EditCardRecord struct {
	Card            domain.Card
	ExpectedVersion int64
	IdempotencyKey  string
	RequestHash     string
	At              time.Time
}

// InvalidateCardsRecord is the idempotent lifecycle bridge for Claim or evidence loss.
// Every supplied selector is combined with AND so broad invalidation is impossible.
type InvalidateCardsRecord struct {
	WorkspaceID     foundation.ID
	ClaimID         *foundation.ID
	SourceVersionID *foundation.ID
	SourceSpanID    *foundation.ID
	Reason          string
	IdempotencyKey  string
	RequestHash     string
	At              time.Time
	BatchSize       int
}

// SubmitAnswerRecord 是 Answer 与 Schedule 同事务提交的完整写入记录。
type SubmitAnswerRecord struct {
	Answer              domain.Answer
	ExpectedCardVersion int64
	// ExpectedCardFingerprint 复验评分所用卡片内容未在事务前发生漂移。
	ExpectedCardFingerprint string
	ExpectedScheduleVersion int64
	ScheduleDecision        domain.ScheduleDecision
	RequestHash             string
}

// DeckScheduleAction 是牌组调度变更动作。
type DeckScheduleAction string

const (
	// DeckSchedulePause 暂停牌组和全部卡片调度。
	DeckSchedulePause DeckScheduleAction = "PAUSE"
	// DeckScheduleResume 恢复牌组和全部卡片调度。
	DeckScheduleResume DeckScheduleAction = "RESUME"
	// DeckScheduleReset 清空复习历史状态并立即到期。
	DeckScheduleReset DeckScheduleAction = "RESET"
)

// DeckScheduleRecord 是调度批量变更的持久层输入。
type DeckScheduleRecord struct {
	WorkspaceID     foundation.ID
	DeckID          foundation.ID
	ExpectedVersion int64
	Action          DeckScheduleAction
	IdempotencyKey  string
	RequestHash     string
	At              time.Time
}
