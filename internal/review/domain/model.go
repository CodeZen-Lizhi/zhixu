// Package domain 定义复习牌组、卡片、答题与调度的稳定领域模型。
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// SchedulerVersionFSRSV1 是默认的可替换 FSRS 调度器版本。
	SchedulerVersionFSRSV1 = "fsrs/v1"
	// EvidenceSchemaVersionV1 是 Review Card 证据绑定的版本。
	EvidenceSchemaVersionV1 = "review-evidence/v1"
	// ScoreSchemaVersionV1 是答题评分结构的版本。
	ScoreSchemaVersionV1 = "review-score/v1"
	// MaxDeckNameBytes 限制牌组名称大小。
	MaxDeckNameBytes = 256
	// MaxQuestionBytes 限制问题大小。
	MaxQuestionBytes = 8192
	// MaxAnswerPointBytes 限制单个答案要点大小。
	MaxAnswerPointBytes = 4096
	// MaxAnswerPoints 限制一张卡片的答案要点数量。
	MaxAnswerPoints = 128
	// MaxEvidenceItems 限制一张卡片的证据数量。
	MaxEvidenceItems = 128
	// MaxQuestionRefBytes 限制答题关联的稳定题目引用大小。
	MaxQuestionRefBytes = 512
	// MaxScorerVersionBytes 限制评分器历史版本标识大小。
	MaxScorerVersionBytes = 128
	// MaxUserAnswerBytes 限制单次用户文本答案大小。
	MaxUserAnswerBytes = 64 * 1024
	// MaxScoreRationaleBytes 限制单个评分维度解释大小。
	MaxScoreRationaleBytes = 4 * 1024
	// MaxScoreFeedbackItemBytes 限制单个错误点或遗漏点大小。
	MaxScoreFeedbackItemBytes = 4 * 1024
	// MaxAnswerFeedbackBytes 限制附加评分反馈 JSON 大小。
	MaxAnswerFeedbackBytes = 32 * 1024
	// MaxInvalidationReasonBytes 限制卡片失效原因大小。
	MaxInvalidationReasonBytes = 1024
	// MaxIdempotencyKeyBytes 限制命令幂等键大小。
	MaxIdempotencyKeyBytes = 128
)

// DeckStatus 是 Review Deck 的生命周期状态。
type DeckStatus string

const (
	// DeckStatusActive 表示牌组参与复习。
	DeckStatusActive DeckStatus = "ACTIVE"
	// DeckStatusPaused 表示牌组暂停调度。
	DeckStatusPaused DeckStatus = "PAUSED"
	// DeckStatusArchived 表示牌组归档且不可继续修改。
	DeckStatusArchived DeckStatus = "ARCHIVED"
)

// CardStatus 是 Review Card 的审批和失效状态。
type CardStatus string

const (
	// CardStatusDraft 表示待审批卡片。
	CardStatusDraft CardStatus = "DRAFT"
	// CardStatusApproved 表示已审批且可复习卡片。
	CardStatusApproved CardStatus = "APPROVED"
	// CardStatusInvalidated 表示绑定知识已失效的卡片。
	CardStatusInvalidated CardStatus = "INVALIDATED"
	// CardStatusRejected 表示用户驳回的卡片。
	CardStatusRejected CardStatus = "REJECTED"
)

// CardType 是受控的题型集合。
type CardType string

const (
	// CardTypeShortAnswer 表示简答题。
	CardTypeShortAnswer CardType = "SHORT_ANSWER"
	// CardTypeCloze 表示填空题。
	CardTypeCloze CardType = "CLOZE"
	// CardTypeComparison 表示概念对比题。
	CardTypeComparison CardType = "COMPARISON"
	// CardTypeScenario 表示场景分析题。
	CardTypeScenario CardType = "SCENARIO"
	// CardTypeCodeReading 表示代码阅读题。
	CardTypeCodeReading CardType = "CODE_READING"
	// CardTypeDesign 表示代码设计题。
	CardTypeDesign CardType = "DESIGN"
)

// SessionType 表示复习或面试会话。
type SessionType string

const (
	// SessionTypeReview 表示普通复习会话。
	SessionTypeReview SessionType = "REVIEW"
	// SessionTypeInterview 表示面试模拟会话。
	SessionTypeInterview SessionType = "INTERVIEW"
)

// SessionStatus 是会话生命周期状态。
type SessionStatus string

const (
	// SessionStatusActive 表示会话进行中。
	SessionStatusActive SessionStatus = "ACTIVE"
	// SessionStatusCompleted 表示会话正常结束。
	SessionStatusCompleted SessionStatus = "COMPLETED"
	// SessionStatusCancelled 表示会话被取消。
	SessionStatusCancelled SessionStatus = "CANCELLED"
)

// Rating 是 FSRS 使用的四级自评难度。
type Rating int

const (
	// RatingAgain 表示未能回忆。
	RatingAgain Rating = 1
	// RatingHard 表示困难但完成回忆。
	RatingHard Rating = 2
	// RatingGood 表示正常回忆。
	RatingGood Rating = 3
	// RatingEasy 表示轻松回忆。
	RatingEasy Rating = 4
)

// EvidenceBinding 将卡片绑定到一个可回查的正式来源证据。
type EvidenceBinding struct {
	SchemaVersion   string        `json:"schema_version"`
	ClaimID         foundation.ID `json:"claim_id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
	EvidenceHash    string        `json:"evidence_hash"`
}

// ScoreDimension 保存一个可解释评分维度，取值范围为 0..1。
type ScoreDimension struct {
	Value     float64 `json:"value"`
	Rationale string  `json:"rationale"`
}

// Score 是证据支持的多维答题评分。
type Score struct {
	SchemaVersion string            `json:"schema_version"`
	Correctness   ScoreDimension    `json:"correctness"`
	Coverage      ScoreDimension    `json:"coverage"`
	Boundaries    ScoreDimension    `json:"boundaries"`
	Clarity       ScoreDimension    `json:"clarity"`
	Confidence    ScoreDimension    `json:"confidence"`
	Errors        []string          `json:"errors,omitempty"`
	Omissions     []string          `json:"omissions,omitempty"`
	Evidence      []EvidenceBinding `json:"evidence,omitempty"`
}

// Deck 是 Review Deck 聚合根。
type Deck struct {
	ID               foundation.ID   `json:"id"`
	WorkspaceID      foundation.ID   `json:"workspace_id"`
	Name             string          `json:"name"`
	Scope            json.RawMessage `json:"scope"`
	Status           DeckStatus      `json:"status"`
	DailyLimit       int             `json:"daily_limit"`
	SchedulerVersion string          `json:"scheduler_version"`
	Version          int64           `json:"version"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// Card 是绑定正式 Claim 与来源证据的 Review Card。
type Card struct {
	ID           foundation.ID     `json:"id"`
	WorkspaceID  foundation.ID     `json:"workspace_id"`
	DeckID       foundation.ID     `json:"deck_id"`
	ClaimID      *foundation.ID    `json:"claim_id,omitempty"`
	Question     string            `json:"question"`
	AnswerPoints []string          `json:"answer_points"`
	Evidence     []EvidenceBinding `json:"evidence"`
	CardType     CardType          `json:"card_type"`
	Difficulty   float64           `json:"difficulty"`
	Status       CardStatus        `json:"status"`
	Fingerprint  string            `json:"fingerprint"`
	ModelVersion string            `json:"model_version"`
	// InvalidationReason 说明卡片为何不再可复习；仅 INVALIDATED 状态可设置。
	InvalidationReason string `json:"invalidation_reason,omitempty"`
	// InvalidatedAt 记录卡片进入 INVALIDATED 的可信时间。
	InvalidatedAt *time.Time `json:"invalidated_at,omitempty"`
	Version       int64      `json:"version"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Schedule 保存单卡片的 FSRS 当前状态。
type Schedule struct {
	CardID           foundation.ID `json:"card_id"`
	WorkspaceID      foundation.ID `json:"workspace_id"`
	DueAt            time.Time     `json:"due_at"`
	IntervalDays     float64       `json:"interval_days"`
	Stability        float64       `json:"stability"`
	Difficulty       float64       `json:"difficulty"`
	LastReviewedAt   *time.Time    `json:"last_reviewed_at,omitempty"`
	SchedulerVersion string        `json:"scheduler_version"`
	Paused           bool          `json:"paused"`
	Version          int64         `json:"version"`
}

// Session 表示一段可恢复的复习或面试会话。
type Session struct {
	ID             foundation.ID   `json:"id"`
	WorkspaceID    foundation.ID   `json:"workspace_id"`
	DeckID         *foundation.ID  `json:"deck_id,omitempty"`
	SessionType    SessionType     `json:"session_type"`
	Status         SessionStatus   `json:"status"`
	Config         json.RawMessage `json:"config"`
	IdempotencyKey string          `json:"-"`
	StartedAt      time.Time       `json:"started_at"`
	EndedAt        *time.Time      `json:"ended_at,omitempty"`
}

// Answer 是一次答题结果，评分与调度在同一领域命令中提交。
type Answer struct {
	ID             foundation.ID  `json:"id"`
	WorkspaceID    foundation.ID  `json:"workspace_id"`
	SessionID      foundation.ID  `json:"session_id"`
	CardID         *foundation.ID `json:"card_id,omitempty"`
	QuestionRef    string         `json:"question_ref"`
	IdempotencyKey string         `json:"-"`
	UserAnswer     string         `json:"user_answer"`
	Rating         Rating         `json:"rating"`
	// ScorerVersion 冻结生成本次评分的服务端评分器版本。
	ScorerVersion string         `json:"scorer_version"`
	Score         Score          `json:"score"`
	Feedback      map[string]any `json:"feedback"`
	CreatedAt     time.Time      `json:"created_at"`
}

// ValidateDeck 校验牌组边界与生命周期。
func ValidateDeck(deck Deck) error {
	if !validID(deck.ID) || !validID(deck.WorkspaceID) || !validStatus(deck.Status, DeckStatusActive, DeckStatusPaused, DeckStatusArchived) {
		return invalid(ErrorCodeDeckInvalid, "deck identity or status is invalid")
	}
	if strings.TrimSpace(deck.Name) != deck.Name || deck.Name == "" || len(deck.Name) > MaxDeckNameBytes || !utf8.ValidString(deck.Name) {
		return invalid(ErrorCodeDeckInvalid, "deck name is invalid")
	}
	if len(deck.Scope) == 0 {
		deck.Scope = json.RawMessage(`{}`)
	}
	if !isJSONObject(deck.Scope) {
		return invalid(ErrorCodeDeckInvalid, "deck scope must be a JSON object")
	}
	if deck.DailyLimit < 1 || deck.DailyLimit > 1000 || strings.TrimSpace(deck.SchedulerVersion) == "" || deck.Version < 1 {
		return invalid(ErrorCodeDeckInvalid, "deck configuration is invalid")
	}
	if !validTimeOrder(deck.CreatedAt, deck.UpdatedAt) {
		return invalid(ErrorCodeDeckInvalid, "deck timestamps are invalid")
	}
	return nil
}

// ValidateCard 校验证据绑定、答案要点、难度和状态。
func ValidateCard(card Card) error {
	if !validID(card.ID) || !validID(card.WorkspaceID) || !validID(card.DeckID) {
		return invalid(ErrorCodeCardInvalid, "card identity is invalid")
	}
	if strings.TrimSpace(card.Question) != card.Question || card.Question == "" || len(card.Question) > MaxQuestionBytes || !utf8.ValidString(card.Question) {
		return invalid(ErrorCodeCardInvalid, "card question is invalid")
	}
	if len(card.AnswerPoints) == 0 || len(card.AnswerPoints) > MaxAnswerPoints {
		return invalid(ErrorCodeCardInvalid, "card answer points are invalid")
	}
	seenPoints := make(map[string]struct{}, len(card.AnswerPoints))
	for _, point := range card.AnswerPoints {
		if strings.TrimSpace(point) != point || point == "" || len(point) > MaxAnswerPointBytes || !utf8.ValidString(point) {
			return invalid(ErrorCodeCardInvalid, "card answer point is invalid")
		}
		if _, exists := seenPoints[point]; exists {
			return invalid(ErrorCodeCardInvalid, "card answer points must be unique")
		}
		seenPoints[point] = struct{}{}
	}
	if len(card.Evidence) == 0 || len(card.Evidence) > MaxEvidenceItems {
		return invalid(ErrorCodeEvidenceMissing, "card must bind at least one evidence item")
	}
	if card.ClaimID == nil || !validID(*card.ClaimID) {
		return invalid(ErrorCodeEvidenceMissing, "card must bind a formal claim")
	}
	seenEvidence := make(map[string]struct{}, len(card.Evidence))
	for _, item := range card.Evidence {
		if err := ValidateEvidence(*card.ClaimID, item); err != nil {
			return err
		}
		if _, exists := seenEvidence[item.EvidenceHash]; exists {
			return invalid(ErrorCodeEvidenceDuplicate, "card evidence hashes must be unique")
		}
		seenEvidence[item.EvidenceHash] = struct{}{}
	}
	if !validCardType(card.CardType) || card.Difficulty < 0 || card.Difficulty > 1 || math.IsNaN(card.Difficulty) || math.IsInf(card.Difficulty, 0) {
		return invalid(ErrorCodeCardInvalid, "card type or difficulty is invalid")
	}
	if !validStatus(card.Status, CardStatusDraft, CardStatusApproved, CardStatusInvalidated, CardStatusRejected) || card.Version < 1 {
		return invalid(ErrorCodeCardInvalid, "card status or version is invalid")
	}
	if card.Status == CardStatusInvalidated {
		if err := ValidateInvalidationReason(card.InvalidationReason); err != nil || card.InvalidatedAt == nil || card.InvalidatedAt.IsZero() {
			return invalid(ErrorCodeInvalidationInvalid, "invalidated card must retain a reason and timestamp")
		}
	} else if card.InvalidationReason != "" || card.InvalidatedAt != nil {
		return invalid(ErrorCodeInvalidationInvalid, "only invalidated cards may retain invalidation metadata")
	}
	if strings.TrimSpace(card.Fingerprint) != "" && !validHash(card.Fingerprint) {
		return invalid(ErrorCodeCardInvalid, "card fingerprint is invalid")
	}
	if strings.TrimSpace(card.ModelVersion) == "" {
		return invalid(ErrorCodeCardInvalid, "card model version is required")
	}
	if !validTimeOrder(card.CreatedAt, card.UpdatedAt) {
		return invalid(ErrorCodeCardInvalid, "card timestamps are invalid")
	}
	return nil
}

// ValidateInvalidationReason 校验可观察的知识或证据失效原因。
func ValidateInvalidationReason(reason string) error {
	if !validRequiredText(reason, MaxInvalidationReasonBytes) {
		return invalid(ErrorCodeInvalidationInvalid, "invalidation reason is invalid")
	}
	return nil
}

// ValidateEvidence 校验证据是否绑定同一 Claim、来源版本和来源片段。
func ValidateEvidence(claimID foundation.ID, evidence EvidenceBinding) error {
	if evidence.SchemaVersion != EvidenceSchemaVersionV1 || !validID(claimID) || evidence.ClaimID != claimID || !validID(evidence.SourceVersionID) || !validID(evidence.SourceSpanID) || !validHash(evidence.EvidenceHash) {
		return invalid(ErrorCodeEvidenceInvalid, "evidence binding is invalid")
	}
	return nil
}

// ComputeCardFingerprint 根据卡片业务内容计算稳定去重指纹。
func ComputeCardFingerprint(card Card) (string, error) {
	if err := ValidateCard(card); err != nil {
		return "", err
	}
	type fingerprintEvidence struct {
		ClaimID         foundation.ID `json:"claim_id"`
		SourceVersionID foundation.ID `json:"source_version_id"`
		SourceSpanID    foundation.ID `json:"source_span_id"`
		EvidenceHash    string        `json:"evidence_hash"`
	}
	evidence := make([]fingerprintEvidence, 0, len(card.Evidence))
	for _, item := range card.Evidence {
		evidence = append(evidence, fingerprintEvidence{item.ClaimID, item.SourceVersionID, item.SourceSpanID, item.EvidenceHash})
	}
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].EvidenceHash < evidence[j].EvidenceHash })
	payload := struct {
		Schema       string                `json:"schema"`
		DeckID       foundation.ID         `json:"deck_id"`
		ClaimID      foundation.ID         `json:"claim_id"`
		Question     string                `json:"question"`
		AnswerPoints []string              `json:"answer_points"`
		Evidence     []fingerprintEvidence `json:"evidence"`
		CardType     CardType              `json:"card_type"`
		Difficulty   float64               `json:"difficulty"`
	}{"review-card-fingerprint/v1", card.DeckID, *card.ClaimID, card.Question, card.AnswerPoints, evidence, card.CardType, card.Difficulty}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", invalid(ErrorCodeCardInvalid, "card fingerprint payload cannot be encoded")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ValidateSchedule 校验 FSRS 状态的数值边界。
func ValidateSchedule(schedule Schedule) error {
	if !validID(schedule.CardID) || !validID(schedule.WorkspaceID) || schedule.DueAt.IsZero() || schedule.Version < 1 || strings.TrimSpace(schedule.SchedulerVersion) == "" {
		return invalid(ErrorCodeScheduleInvalid, "schedule identity or due time is invalid")
	}
	if invalidNumber(schedule.IntervalDays) || schedule.IntervalDays < 0 || invalidNumber(schedule.Stability) || schedule.Stability < 0 || invalidNumber(schedule.Difficulty) || schedule.Difficulty < 0 || schedule.Difficulty > 1 {
		return invalid(ErrorCodeScheduleInvalid, "schedule numeric state is invalid")
	}
	if schedule.LastReviewedAt != nil && schedule.LastReviewedAt.After(schedule.DueAt) && schedule.IntervalDays == 0 {
		return invalid(ErrorCodeScheduleInvalid, "initial schedule cannot be reviewed before due time")
	}
	return nil
}

// ValidateSession 校验会话边界。
func ValidateSession(session Session) error {
	if !validID(session.ID) || !validID(session.WorkspaceID) || !validStatus(session.SessionType, SessionTypeReview, SessionTypeInterview) || !validStatus(session.Status, SessionStatusActive, SessionStatusCompleted, SessionStatusCancelled) {
		return invalid(ErrorCodeSessionInvalid, "session identity or status is invalid")
	}
	if len(session.Config) == 0 || !isJSONObject(session.Config) || !validIdempotencyKey(session.IdempotencyKey) || session.StartedAt.IsZero() {
		return invalid(ErrorCodeSessionInvalid, "session config or start time is invalid")
	}
	if session.EndedAt != nil && session.EndedAt.Before(session.StartedAt) {
		return invalid(ErrorCodeSessionInvalid, "session end time is invalid")
	}
	if session.SessionType == SessionTypeReview && session.DeckID == nil {
		return invalid(ErrorCodeSessionInvalid, "review session must bind a deck")
	}
	if session.DeckID != nil && !validID(*session.DeckID) {
		return invalid(ErrorCodeSessionInvalid, "session deck is invalid")
	}
	return nil
}

// IsDeckBoundReviewSession 报告会话是否属于公开 Review 操作边界。
func IsDeckBoundReviewSession(session Session) bool {
	return session.SessionType == SessionTypeReview && session.DeckID != nil
}

// ValidateScore 校验多维评分与评分证据。
func ValidateScore(score Score) error {
	if score.SchemaVersion != ScoreSchemaVersionV1 {
		return invalid(ErrorCodeScoreInvalid, "score schema version is unsupported")
	}
	for _, dimension := range []ScoreDimension{score.Correctness, score.Coverage, score.Boundaries, score.Clarity, score.Confidence} {
		if invalidNumber(dimension.Value) || dimension.Value < 0 || dimension.Value > 1 || !validRequiredText(dimension.Rationale, MaxScoreRationaleBytes) {
			return invalid(ErrorCodeScoreInvalid, "score dimension is invalid")
		}
	}
	if len(score.Errors) > MaxAnswerPoints || len(score.Omissions) > MaxAnswerPoints {
		return invalid(ErrorCodeScoreInvalid, "score feedback is too large")
	}
	if len(score.Evidence) > MaxEvidenceItems {
		return invalid(ErrorCodeScoreInvalid, "score evidence is too large")
	}
	for _, item := range append(append([]string(nil), score.Errors...), score.Omissions...) {
		if !validRequiredText(item, MaxScoreFeedbackItemBytes) {
			return invalid(ErrorCodeScoreInvalid, "score feedback item is invalid")
		}
	}
	for _, evidence := range score.Evidence {
		if err := ValidateEvidence(evidence.ClaimID, evidence); err != nil {
			return err
		}
	}
	return nil
}

// ValidateAnswerInput 校验用户可提交的答案输入，不信任评分或反馈字段。
func ValidateAnswerInput(answer Answer) error {
	if !validID(answer.WorkspaceID) || !validID(answer.SessionID) || !validIDPtr(answer.CardID) ||
		!validRequiredText(answer.QuestionRef, MaxQuestionRefBytes) || !validIdempotencyKey(answer.IdempotencyKey) ||
		len(answer.UserAnswer) > MaxUserAnswerBytes || !utf8.ValidString(answer.UserAnswer) || strings.ContainsRune(answer.UserAnswer, '\x00') {
		return invalid(ErrorCodeAnswerInvalid, "answer identity or text is invalid")
	}
	if answer.Rating < RatingAgain || answer.Rating > RatingEasy {
		return invalid(ErrorCodeAnswerInvalid, "answer rating is invalid")
	}
	return nil
}

// ValidateAnswerSubmission 校验服务端评分后的完整 Answer 写入。
func ValidateAnswerSubmission(answer Answer) error {
	if err := ValidateAnswerInput(answer); err != nil {
		return err
	}
	if !validRequiredText(answer.ScorerVersion, MaxScorerVersionBytes) {
		return invalid(ErrorCodeAnswerInvalid, "answer scorer version is invalid")
	}
	feedback, err := json.Marshal(answer.Feedback)
	if answer.Feedback == nil || err != nil || len(feedback) > MaxAnswerFeedbackBytes {
		return invalid(ErrorCodeAnswerInvalid, "answer feedback is invalid")
	}
	return ValidateScore(answer.Score)
}

// ValidateAnswer 校验已分配身份和时间的持久答题事实。
func ValidateAnswer(answer Answer) error {
	if !validID(answer.ID) || answer.CreatedAt.IsZero() {
		return invalid(ErrorCodeAnswerInvalid, "answer identity or creation time is invalid")
	}
	return ValidateAnswerSubmission(answer)
}

// IsActiveCard 报告卡片是否可以进入复习队列。
func IsActiveCard(card Card) bool { return card.Status == CardStatusApproved }

// IsDue 报告卡片在给定时间是否到期且未暂停。
func IsDue(schedule Schedule, now time.Time) bool {
	return !schedule.Paused && !schedule.DueAt.After(now.UTC())
}

// ValidateIdempotencyKey 校验 Review 命令幂等键。
func ValidateIdempotencyKey(value string) error {
	if !validIdempotencyKey(value) {
		return invalid(ErrorCodeAnswerInvalid, "review idempotency key is invalid")
	}
	return nil
}

func validID(value foundation.ID) bool {
	if value == "" {
		return false
	}
	_, err := foundation.ParseID(string(value))
	return err == nil
}

func validIDPtr(value *foundation.ID) bool { return value == nil || validID(*value) }

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func validStatus[T ~string](value T, allowed ...T) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func validCardType(value CardType) bool {
	return validStatus(value, CardTypeShortAnswer, CardTypeCloze, CardTypeComparison, CardTypeScenario, CardTypeCodeReading, CardTypeDesign)
}

func validTimeOrder(created, updated time.Time) bool {
	return !created.IsZero() && !updated.IsZero() && !updated.Before(created)
}

func invalidNumber(value float64) bool { return math.IsNaN(value) || math.IsInf(value, 0) }

func validIdempotencyKey(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > MaxIdempotencyKeyBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r <= 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validRequiredText(value string, maxBytes int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func isJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return len(raw) > 0 && json.Unmarshal(raw, &object) == nil && object != nil
}

// CanonicalJSON returns stable JSON for scope/config values without exposing map order.
func CanonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, invalid(ErrorCodeJSONInvalid, "json value is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, invalid(ErrorCodeJSONInvalid, "json contains trailing values")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, invalid(ErrorCodeJSONInvalid, "json value cannot be encoded")
	}
	return encoded, nil
}
