// Package reviewhttp exposes the strict public HTTP contract for Review.
package reviewhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	"github.com/gin-gonic/gin"
)

const (
	defaultListLimit = 50
	defaultDueLimit  = 20
	maxLimit         = 200
	maxBodyBytes     = 128 * 1024
	maxKeyBytes      = 128

	errorCodeUnavailable        = "REVIEW_HTTP_UNAVAILABLE"
	errorCodeScoringUnavailable = "REVIEW_SCORING_UNAVAILABLE"
	errorCodeResultInvalid      = "REVIEW_HTTP_RESULT_INCONSISTENT"
)

// Service is the Review application contract required by the HTTP boundary.
type Service interface {
	CreateDeck(context.Context, reviewapp.CreateDeckCommand) (reviewapp.CommandResult[domain.Deck], error)
	GetDeck(context.Context, foundation.ID, foundation.ID) (domain.Deck, error)
	ListDecks(context.Context, foundation.ID, int) (reviewapp.DeckPage, error)
	ListCards(context.Context, foundation.ID, foundation.ID, int) (reviewapp.CardPage, error)
	CreateCard(context.Context, reviewapp.CreateCardCommand) (reviewapp.CommandResult[domain.Card], error)
	EditCard(context.Context, reviewapp.EditCardCommand) (reviewapp.CommandResult[domain.Card], error)
	ApproveCard(context.Context, reviewapp.CardDecisionCommand) (reviewapp.CommandResult[domain.Card], error)
	RejectCard(context.Context, reviewapp.CardDecisionCommand) (reviewapp.CommandResult[domain.Card], error)
	InvalidateCard(context.Context, reviewapp.CardDecisionCommand) (reviewapp.CommandResult[domain.Card], error)
	InvalidateCards(context.Context, reviewapp.InvalidateCardsCommand) (reviewapp.InvalidationResult, error)
	ListDue(context.Context, foundation.ID, foundation.ID, *foundation.ID, int) ([]reviewapp.DueCard, error)
	StartSession(context.Context, reviewapp.StartSessionCommand) (reviewapp.CommandResult[domain.Session], error)
	CompleteSession(context.Context, reviewapp.CompleteSessionCommand) (reviewapp.CommandResult[domain.Session], error)
	SubmitAnswer(context.Context, reviewapp.SubmitAnswerCommand) (reviewapp.AnswerResult, error)
	ChangeDeckSchedule(context.Context, reviewapp.DeckScheduleCommand, reviewapp.DeckScheduleAction) (reviewapp.CommandResult[domain.Deck], error)
}

// Handler maps Review application methods to strict JSON HTTP endpoints.
type Handler struct {
	service Service
	timeout time.Duration
}

// NewHandler creates a Review HTTP handler. A missing service fails closed.
func NewHandler(service Service, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Handler{service: service, timeout: timeout}
}

// Routes registers Review endpoints under the caller's /api/v1 router.
func (handler *Handler) Routes(router gin.IRouter) {
	review := router.Group("")
	review.Use(noStore)
	review.GET("/review/decks", httpapi.GinHandler(handler.listDecks))
	review.POST("/review/decks", httpapi.GinHandler(handler.createDeck))
	review.GET("/review/decks/:deck_id", httpapi.GinHandler(handler.getDeck))
	review.GET("/review/decks/:deck_id/cards", httpapi.GinHandler(handler.listCards))
	review.POST("/review/decks/:deck_id/cards", httpapi.GinHandler(handler.createCard))
	review.PUT("/review/cards/:card_id", httpapi.GinHandler(handler.editCard))
	review.POST("/review/decks/:deck_id/schedule/pause", httpapi.GinHandler(handler.pauseDeck))
	review.POST("/review/decks/:deck_id/schedule/resume", httpapi.GinHandler(handler.resumeDeck))
	review.POST("/review/decks/:deck_id/schedule/reset", httpapi.GinHandler(handler.resetDeck))
	review.GET("/review/due", httpapi.GinHandler(handler.listDue))
	review.POST("/review/cards/:card_id/approve", httpapi.GinHandler(handler.approveCard))
	review.POST("/review/cards/:card_id/reject", httpapi.GinHandler(handler.rejectCard))
	review.POST("/review/cards/:card_id/invalidate", httpapi.GinHandler(handler.invalidateCard))
	review.POST("/review/invalidation", httpapi.GinHandler(handler.invalidateCards))
	review.POST("/review/sessions", httpapi.GinHandler(handler.startSession))
	review.POST("/review/sessions/:session_id/complete", httpapi.GinHandler(handler.completeSession))
	review.POST("/review/sessions/:session_id/answers", httpapi.GinHandler(handler.submitAnswer))
}

// noStore 防止题面、答案、评分和 Evidence 被共享或浏览器缓存持久化。
func noStore(context *gin.Context) {
	context.Header("Cache-Control", "no-store")
	context.Next()
}

// Available reports whether this handler has a real application dependency.
func (handler *Handler) Available() bool { return handler != nil && !nilDependency(handler.service) }

func (handler *Handler) listDecks(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	query, err := parseQuery(r, "workspace_id", "limit")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(single(query, "workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := parseLimit(singleDefault(query, "limit", strconv.Itoa(defaultListLimit)))
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.ListDecks(ctx, workspaceID, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(page.Items) > limit {
		writeError(w, resultInvalid("deck list exceeded requested limit"))
		return
	}
	items := make([]deckResponse, 0, len(page.Items))
	for _, deck := range page.Items {
		if err := validateDeck(deck, workspaceID, ""); err != nil {
			writeError(w, err)
			return
		}
		items = append(items, toDeckResponse(deck))
	}
	httpapi.WriteJSON(w, http.StatusOK, deckListResponse{WorkspaceID: string(workspaceID), Items: items})
}

func (handler *Handler) createDeck(w http.ResponseWriter, r *http.Request) {
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[createDeckRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.CreateDeck(ctx, reviewapp.CreateDeckCommand{WorkspaceID: workspaceID, Name: wire.Name, Scope: wire.Scope, DailyLimit: wire.DailyLimit, IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateDeck(result.Value, workspaceID, ""); err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, toDeckResponse(result.Value))
}

func (handler *Handler) getDeck(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	workspaceID, deckID, err := parsePathWorkspaceResource(r, "deck_id")
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	deck, err := handler.service.GetDeck(ctx, workspaceID, deckID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateDeck(deck, workspaceID, deckID); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toDeckResponse(deck))
}

func (handler *Handler) listCards(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	query, err := parseQuery(r, "workspace_id", "limit")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(single(query, "workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	deckID, err := parseID(r.PathValue("deck_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := parseLimit(singleDefault(query, "limit", strconv.Itoa(defaultListLimit)))
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.ListCards(ctx, workspaceID, deckID, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(page.Items) > limit {
		writeError(w, resultInvalid("card list exceeded requested limit"))
		return
	}
	items := make([]cardResponse, 0, len(page.Items))
	for _, card := range page.Items {
		if err := validateCard(card, workspaceID, deckID, ""); err != nil {
			writeError(w, err)
			return
		}
		items = append(items, toCardResponse(card))
	}
	httpapi.WriteJSON(w, http.StatusOK, cardListResponse{WorkspaceID: string(workspaceID), DeckID: string(deckID), Items: items})
}

func (handler *Handler) createCard(w http.ResponseWriter, r *http.Request) {
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	deckID, err := parseID(r.PathValue("deck_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[createCardRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, claimID, err := parseTwoIDs(wire.WorkspaceID, wire.ClaimID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.CreateCard(ctx, reviewapp.CreateCardCommand{WorkspaceID: workspaceID, DeckID: deckID, ClaimID: claimID, Question: wire.Question, AnswerPoints: wire.AnswerPoints, Evidence: wire.Evidence, CardType: domain.CardType(wire.CardType), Difficulty: wire.Difficulty, ModelVersion: wire.ModelVersion, IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateCard(result.Value, workspaceID, deckID, ""); err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, toCardResponse(result.Value))
}

func (handler *Handler) editCard(w http.ResponseWriter, r *http.Request) {
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	cardID, err := parseID(r.PathValue("card_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[editCardRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, claimID, err := parseTwoIDs(wire.WorkspaceID, wire.ClaimID)
	if err != nil {
		writeError(w, err)
		return
	}
	if wire.ExpectedVersion < 1 {
		writeError(w, requestInvalid("expected_version is invalid"))
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.EditCard(ctx, reviewapp.EditCardCommand{
		WorkspaceID: workspaceID, CardID: cardID, ExpectedVersion: wire.ExpectedVersion, ClaimID: claimID,
		Question: wire.Question, AnswerPoints: wire.AnswerPoints, Evidence: wire.Evidence, CardType: domain.CardType(wire.CardType),
		Difficulty: wire.Difficulty, ModelVersion: wire.ModelVersion, IdempotencyKey: key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateCard(result.Value, workspaceID, "", cardID); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toCardResponse(result.Value))
}

func (handler *Handler) approveCard(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	handler.decideCard(w, r, handler.service.ApproveCard)
}
func (handler *Handler) rejectCard(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	handler.decideCard(w, r, handler.service.RejectCard)
}
func (handler *Handler) invalidateCard(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	handler.decideCard(w, r, handler.service.InvalidateCard)
}

func (handler *Handler) invalidateCards(w http.ResponseWriter, r *http.Request) {
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[invalidationRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	claimID, err := parseOptionalID(wire.ClaimID)
	if err != nil {
		writeError(w, err)
		return
	}
	sourceVersionID, err := parseOptionalID(wire.SourceVersionID)
	if err != nil {
		writeError(w, err)
		return
	}
	sourceSpanID, err := parseOptionalID(wire.SourceSpanID)
	if err != nil {
		writeError(w, err)
		return
	}
	if claimID == nil && sourceVersionID == nil && sourceSpanID == nil {
		writeError(w, requestInvalid("invalidation target is required"))
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.InvalidateCards(ctx, reviewapp.InvalidateCardsCommand{
		WorkspaceID: workspaceID, ClaimID: claimID, SourceVersionID: sourceVersionID, SourceSpanID: sourceSpanID,
		Reason: wire.Reason, IdempotencyKey: key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if result.InvalidatedCount < 0 || result.InvalidatedCount > reviewapp.MaxInvalidationBatchSize ||
		(result.HasMore && result.InvalidatedCount != reviewapp.MaxInvalidationBatchSize) {
		writeError(w, resultInvalid("invalidation result exceeded the bounded batch contract"))
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, invalidationResponse{
		WorkspaceID: string(workspaceID), InvalidatedCount: result.InvalidatedCount,
		HasMore: result.HasMore, Replayed: result.Replayed,
	})
}

func (handler *Handler) decideCard(w http.ResponseWriter, r *http.Request, decide func(context.Context, reviewapp.CardDecisionCommand) (reviewapp.CommandResult[domain.Card], error)) {
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	cardID, err := parseID(r.PathValue("card_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[cardDecisionRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if wire.ExpectedVersion < 1 {
		writeError(w, requestInvalid("expected_version is invalid"))
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := decide(ctx, reviewapp.CardDecisionCommand{WorkspaceID: workspaceID, CardID: cardID, ExpectedVersion: wire.ExpectedVersion, IdempotencyKey: key, Reason: wire.Reason})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateCard(result.Value, workspaceID, "", cardID); err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if !result.Replayed {
		status = http.StatusCreated
	}
	httpapi.WriteJSON(w, status, toCardResponse(result.Value))
}

func (handler *Handler) listDue(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	query, err := parseQuery(r, "workspace_id", "session_id", "deck_id", "limit")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(single(query, "workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	sessionID, err := parseID(single(query, "session_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := parseLimit(singleDefault(query, "limit", strconv.Itoa(defaultDueLimit)))
	if err != nil {
		writeError(w, err)
		return
	}
	var deckID *foundation.ID
	if raw := single(query, "deck_id"); raw != "" {
		id, parseErr := parseID(raw)
		if parseErr != nil {
			writeError(w, parseErr)
			return
		}
		deckID = &id
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	items, err := handler.service.ListDue(ctx, workspaceID, sessionID, deckID, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(items) > limit {
		writeError(w, resultInvalid("due list exceeded requested limit"))
		return
	}
	response := dueListResponse{WorkspaceID: string(workspaceID), Items: make([]dueCardResponse, 0, len(items))}
	for _, item := range items {
		if err := validateCard(item.Card, workspaceID, "", ""); err != nil || (deckID != nil && item.Card.DeckID != *deckID) || item.Schedule.WorkspaceID != workspaceID || item.Schedule.CardID != item.Card.ID || domain.ValidateSchedule(item.Schedule) != nil || !validQuestionRef(item.QuestionRef) {
			writeError(w, resultInvalid("due card crossed workspace binding"))
			return
		}
		response.Items = append(response.Items, dueCardResponse{Card: toDueCardResponse(item.Card), Schedule: toScheduleResponse(item.Schedule), QuestionRef: item.QuestionRef})
	}
	httpapi.WriteJSON(w, http.StatusOK, response)
}

func (handler *Handler) startSession(w http.ResponseWriter, r *http.Request) {
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[startSessionRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	deckID, err := parseID(wire.DeckID)
	if err != nil {
		writeError(w, err)
		return
	}
	if wire.SessionType != string(domain.SessionTypeReview) {
		writeError(w, requestInvalid("session_type is invalid"))
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.StartSession(ctx, reviewapp.StartSessionCommand{WorkspaceID: workspaceID, DeckID: &deckID, SessionType: domain.SessionTypeReview, Config: wire.Config, IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateSession(result.Value, workspaceID, ""); err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, toSessionResponse(result.Value))
}

func (handler *Handler) completeSession(w http.ResponseWriter, r *http.Request) {
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	sessionID, err := parseID(r.PathValue("session_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[completeSessionRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.CompleteSession(ctx, reviewapp.CompleteSessionCommand{WorkspaceID: workspaceID, SessionID: sessionID, Cancelled: wire.Cancelled, IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateSession(result.Value, workspaceID, sessionID); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toSessionResponse(result.Value))
}

func (handler *Handler) pauseDeck(w http.ResponseWriter, r *http.Request) {
	handler.changeDeckSchedule(w, r, reviewapp.DeckSchedulePause)
}
func (handler *Handler) resumeDeck(w http.ResponseWriter, r *http.Request) {
	handler.changeDeckSchedule(w, r, reviewapp.DeckScheduleResume)
}
func (handler *Handler) resetDeck(w http.ResponseWriter, r *http.Request) {
	handler.changeDeckSchedule(w, r, reviewapp.DeckScheduleReset)
}

func (handler *Handler) changeDeckSchedule(w http.ResponseWriter, r *http.Request, action reviewapp.DeckScheduleAction) {
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	deckID, err := parseID(r.PathValue("deck_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[deckScheduleRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if wire.ExpectedVersion < 1 {
		writeError(w, requestInvalid("expected_version is invalid"))
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.ChangeDeckSchedule(ctx, reviewapp.DeckScheduleCommand{WorkspaceID: workspaceID, DeckID: deckID, ExpectedVersion: wire.ExpectedVersion, IdempotencyKey: key}, action)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateDeck(result.Value, workspaceID, deckID); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toDeckResponse(result.Value))
}

// submitAnswer accepts only user answer and self-rating, then returns the
// server-created Score and the atomically advanced Schedule.
func (handler *Handler) submitAnswer(w http.ResponseWriter, r *http.Request) {
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	sessionID, err := parseID(r.PathValue("session_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[submitAnswerRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateSubmitAnswerRequest(wire); err != nil {
		writeError(w, err)
		return
	}
	workspaceID, cardID, err := parseTwoIDs(wire.WorkspaceID, wire.CardID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.SubmitAnswer(ctx, reviewapp.SubmitAnswerCommand{
		WorkspaceID: workspaceID, SessionID: sessionID, CardID: cardID, QuestionRef: wire.QuestionRef,
		UserAnswer: wire.UserAnswer, Rating: domain.Rating(wire.Rating), IdempotencyKey: key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateAnswerResult(result, workspaceID, sessionID, cardID); err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, answerResultResponse{Answer: toAnswerResponse(result.Answer), Schedule: toScheduleResponse(result.Schedule), Replayed: result.Replayed})
}

type createDeckRequest struct {
	WorkspaceID string          `json:"workspace_id"`
	Name        string          `json:"name"`
	Scope       json.RawMessage `json:"scope"`
	DailyLimit  int             `json:"daily_limit"`
}
type createCardRequest struct {
	WorkspaceID  string                   `json:"workspace_id"`
	ClaimID      string                   `json:"claim_id"`
	Question     string                   `json:"question"`
	AnswerPoints []string                 `json:"answer_points"`
	Evidence     []domain.EvidenceBinding `json:"evidence"`
	CardType     string                   `json:"card_type"`
	Difficulty   float64                  `json:"difficulty"`
	ModelVersion string                   `json:"model_version"`
}
type editCardRequest struct {
	WorkspaceID     string                   `json:"workspace_id"`
	ExpectedVersion int64                    `json:"expected_version"`
	ClaimID         string                   `json:"claim_id"`
	Question        string                   `json:"question"`
	AnswerPoints    []string                 `json:"answer_points"`
	Evidence        []domain.EvidenceBinding `json:"evidence"`
	CardType        string                   `json:"card_type"`
	Difficulty      float64                  `json:"difficulty"`
	ModelVersion    string                   `json:"model_version"`
}
type cardDecisionRequest struct {
	WorkspaceID     string `json:"workspace_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
}
type invalidationRequest struct {
	WorkspaceID     string  `json:"workspace_id"`
	ClaimID         *string `json:"claim_id"`
	SourceVersionID *string `json:"source_version_id"`
	SourceSpanID    *string `json:"source_span_id"`
	Reason          string  `json:"reason"`
}
type startSessionRequest struct {
	WorkspaceID string          `json:"workspace_id"`
	DeckID      string          `json:"deck_id"`
	SessionType string          `json:"session_type"`
	Config      json.RawMessage `json:"config"`
}
type completeSessionRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Cancelled   bool   `json:"cancelled"`
}
type deckScheduleRequest struct {
	WorkspaceID     string `json:"workspace_id"`
	ExpectedVersion int64  `json:"expected_version"`
}
type submitAnswerRequest struct {
	WorkspaceID string `json:"workspace_id"`
	CardID      string `json:"card_id"`
	QuestionRef string `json:"question_ref"`
	UserAnswer  string `json:"user_answer"`
	Rating      int    `json:"rating"`
}

// validateSubmitAnswerRequest keeps the unavailable scorer boundary strict.
func validateSubmitAnswerRequest(value submitAnswerRequest) error {
	if _, _, err := parseTwoIDs(value.WorkspaceID, value.CardID); err != nil {
		return err
	}
	if !validQuestionRef(value.QuestionRef) {
		return requestInvalid("question_ref is invalid")
	}
	if len(value.UserAnswer) > domain.MaxUserAnswerBytes || !utf8.ValidString(value.UserAnswer) || strings.ContainsRune(value.UserAnswer, '\x00') {
		return requestInvalid("user_answer is invalid")
	}
	if value.Rating < int(domain.RatingAgain) || value.Rating > int(domain.RatingEasy) {
		return requestInvalid("rating is invalid")
	}
	return nil
}

// validQuestionRef 校验 question_ref 的公开传输边界。
func validQuestionRef(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= domain.MaxQuestionRefBytes && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

type deckListResponse struct {
	WorkspaceID string         `json:"workspace_id"`
	Items       []deckResponse `json:"items"`
}
type cardListResponse struct {
	WorkspaceID string         `json:"workspace_id"`
	DeckID      string         `json:"deck_id"`
	Items       []cardResponse `json:"items"`
}
type dueListResponse struct {
	WorkspaceID string            `json:"workspace_id"`
	Items       []dueCardResponse `json:"items"`
}
type invalidationResponse struct {
	WorkspaceID      string `json:"workspace_id"`
	InvalidatedCount int    `json:"invalidated_count"`
	HasMore          bool   `json:"has_more"`
	Replayed         bool   `json:"replayed"`
}
type dueCardResponse struct {
	Card     dueCardCardResponse `json:"card"`
	Schedule scheduleResponse    `json:"schedule"`
	// QuestionRef 是服务端签发并绑定 Review Session 的当前题面快照引用。
	QuestionRef string `json:"question_ref"`
}

// dueCardCardResponse deliberately excludes answer points and evidence. The
// review queue is rendered before a user answers, so revealing either here
// would disclose the expected answer or source body before submission.
type dueCardCardResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	DeckID      string  `json:"deck_id"`
	Question    string  `json:"question"`
	CardType    string  `json:"card_type"`
	Difficulty  float64 `json:"difficulty"`
	Status      string  `json:"status"`
	Version     int64   `json:"version"`
}
type deckResponse struct {
	ID               string          `json:"id"`
	WorkspaceID      string          `json:"workspace_id"`
	Name             string          `json:"name"`
	Scope            json.RawMessage `json:"scope"`
	Status           string          `json:"status"`
	DailyLimit       int             `json:"daily_limit"`
	SchedulerVersion string          `json:"scheduler_version"`
	Version          int64           `json:"version"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
}
type cardResponse struct {
	ID                 string                   `json:"id"`
	WorkspaceID        string                   `json:"workspace_id"`
	DeckID             string                   `json:"deck_id"`
	ClaimID            *string                  `json:"claim_id,omitempty"`
	Question           string                   `json:"question"`
	AnswerPoints       []string                 `json:"answer_points"`
	Evidence           []domain.EvidenceBinding `json:"evidence"`
	CardType           string                   `json:"card_type"`
	Difficulty         float64                  `json:"difficulty"`
	Status             string                   `json:"status"`
	Fingerprint        string                   `json:"fingerprint"`
	ModelVersion       string                   `json:"model_version"`
	InvalidationReason string                   `json:"invalidation_reason,omitempty"`
	InvalidatedAt      *string                  `json:"invalidated_at,omitempty"`
	Version            int64                    `json:"version"`
	CreatedAt          string                   `json:"created_at"`
	UpdatedAt          string                   `json:"updated_at"`
}
type scheduleResponse struct {
	CardID           string  `json:"card_id"`
	WorkspaceID      string  `json:"workspace_id"`
	DueAt            string  `json:"due_at"`
	IntervalDays     float64 `json:"interval_days"`
	Stability        float64 `json:"stability"`
	Difficulty       float64 `json:"difficulty"`
	LastReviewedAt   *string `json:"last_reviewed_at,omitempty"`
	SchedulerVersion string  `json:"scheduler_version"`
	Paused           bool    `json:"paused"`
	Version          int64   `json:"version"`
}
type sessionResponse struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	DeckID      *string         `json:"deck_id,omitempty"`
	SessionType string          `json:"session_type"`
	Status      string          `json:"status"`
	Config      json.RawMessage `json:"config"`
	StartedAt   string          `json:"started_at"`
	EndedAt     *string         `json:"ended_at,omitempty"`
}
type answerResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	SessionID   string  `json:"session_id"`
	CardID      *string `json:"card_id,omitempty"`
	QuestionRef string  `json:"question_ref"`
	UserAnswer  string  `json:"user_answer"`
	Rating      int     `json:"rating"`
	// ScorerVersion 是生成本次评分的服务端实现版本。
	ScorerVersion string         `json:"scorer_version"`
	Score         scoreResponse  `json:"score"`
	Feedback      map[string]any `json:"feedback"`
	CreatedAt     string         `json:"created_at"`
}
type scoreResponse struct {
	SchemaVersion string                  `json:"schema_version"`
	Correctness   domain.ScoreDimension   `json:"correctness"`
	Coverage      domain.ScoreDimension   `json:"coverage"`
	Boundaries    domain.ScoreDimension   `json:"boundaries"`
	Clarity       domain.ScoreDimension   `json:"clarity"`
	Confidence    domain.ScoreDimension   `json:"confidence"`
	Errors        []string                `json:"errors,omitempty"`
	Omissions     []string                `json:"omissions,omitempty"`
	Evidence      []scoreEvidenceResponse `json:"evidence,omitempty"`
}

// scoreEvidenceResponse is only returned after a scored answer. Its hrefs
// are server-owned so clients never construct an evidence URL from IDs.
type scoreEvidenceResponse struct {
	SchemaVersion     string `json:"schema_version"`
	ClaimID           string `json:"claim_id"`
	SourceVersionID   string `json:"source_version_id"`
	SourceSpanID      string `json:"source_span_id"`
	EvidenceHash      string `json:"evidence_hash"`
	SourceVersionHref string `json:"source_version_href"`
	SourceSpanHref    string `json:"source_span_href"`
}
type answerResultResponse struct {
	Answer   answerResponse   `json:"answer"`
	Schedule scheduleResponse `json:"schedule"`
	Replayed bool             `json:"replayed"`
}

func decodeJSON[T any](r *http.Request) (T, error) {
	var zero T
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("request requires application/json"))
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxBodyBytes {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, errors.New("request JSON is empty or oversized"))
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxBodyBytes
	value, err := strictjson.DecodeObject[T](body, limits, nil)
	if err != nil {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return value, nil
}

func parseIdempotencyKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || !utf8.ValidString(values[0]) {
		return "", requestInvalid("exactly one Idempotency-Key is required")
	}
	key := values[0]
	if key == "" || key != strings.TrimSpace(key) || len(key) > maxKeyBytes {
		return "", requestInvalid("Idempotency-Key is invalid")
	}
	for _, character := range key {
		if unicode.IsControl(character) {
			return "", requestInvalid("Idempotency-Key is invalid")
		}
	}
	return key, nil
}

func parseQuery(r *http.Request, allowed ...string) (url.Values, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, requestInvalid("query parameters are invalid")
	}
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := set[key]; !ok || len(entries) != 1 || entries[0] == "" || strings.TrimSpace(entries[0]) != entries[0] {
			return nil, requestInvalid("query parameter is invalid or not allowed")
		}
	}
	return values, nil
}
func rejectQuery(r *http.Request) error {
	if r.URL.RawQuery != "" {
		return requestInvalid("query parameters are not allowed")
	}
	return nil
}
func single(values url.Values, key string) string {
	if values[key] == nil {
		return ""
	}
	return values.Get(key)
}
func singleDefault(values url.Values, key, fallback string) string {
	if value := single(values, key); value != "" {
		return value
	}
	return fallback
}
func parseLimit(raw string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maxLimit {
		return 0, requestInvalid("limit is invalid")
	}
	return value, nil
}
func parseID(raw string) (foundation.ID, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return "", requestInvalid("UUID is invalid")
	}
	id, err := foundation.ParseID(raw)
	if err != nil || string(id) != raw {
		return "", requestInvalid("UUID is invalid")
	}
	return id, nil
}
func parseTwoIDs(first, second string) (foundation.ID, foundation.ID, error) {
	one, err := parseID(first)
	if err != nil {
		return "", "", err
	}
	two, err := parseID(second)
	if err != nil {
		return "", "", err
	}
	return one, two, nil
}
func parseOptionalID(raw *string) (*foundation.ID, error) {
	if raw == nil {
		return nil, nil
	}
	id, err := parseID(*raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
func parsePathWorkspaceResource(r *http.Request, resource string) (foundation.ID, foundation.ID, error) {
	query, err := parseQuery(r, "workspace_id")
	if err != nil {
		return "", "", err
	}
	workspaceID, err := parseID(single(query, "workspace_id"))
	if err != nil {
		return "", "", err
	}
	resourceID, err := parseID(r.PathValue(resource))
	if err != nil {
		return "", "", err
	}
	return workspaceID, resourceID, nil
}

func validateDeck(value domain.Deck, workspaceID, deckID foundation.ID) error {
	if value.WorkspaceID != workspaceID || (deckID != "" && value.ID != deckID) || domain.ValidateDeck(value) != nil {
		return resultInvalid("deck response crossed request binding")
	}
	return nil
}
func validateCard(value domain.Card, workspaceID, deckID, cardID foundation.ID) error {
	if value.WorkspaceID != workspaceID || (deckID != "" && value.DeckID != deckID) || (cardID != "" && value.ID != cardID) || domain.ValidateCard(value) != nil {
		return resultInvalid("card response crossed request binding")
	}
	return nil
}
func validateSession(value domain.Session, workspaceID, sessionID foundation.ID) error {
	if value.WorkspaceID != workspaceID || (sessionID != "" && value.ID != sessionID) || !domain.IsDeckBoundReviewSession(value) || domain.ValidateSession(value) != nil {
		return resultInvalid("session response crossed request binding")
	}
	return nil
}
func validateAnswerResult(value reviewapp.AnswerResult, workspaceID, sessionID, cardID foundation.ID) error {
	if value.Answer.WorkspaceID != workspaceID || value.Answer.SessionID != sessionID || value.Answer.CardID == nil || *value.Answer.CardID != cardID ||
		value.Schedule.WorkspaceID != workspaceID || value.Schedule.CardID != cardID ||
		domain.ValidateAnswer(value.Answer) != nil || domain.ValidateSchedule(value.Schedule) != nil {
		return resultInvalid("answer response crossed request binding")
	}
	return nil
}

func toDeckResponse(value domain.Deck) deckResponse {
	return deckResponse{ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), Name: value.Name, Scope: append(json.RawMessage(nil), value.Scope...), Status: string(value.Status), DailyLimit: value.DailyLimit, SchedulerVersion: value.SchedulerVersion, Version: value.Version, CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt)}
}
func toCardResponse(value domain.Card) cardResponse {
	return cardResponse{ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), DeckID: string(value.DeckID), ClaimID: idString(value.ClaimID), Question: value.Question, AnswerPoints: append([]string(nil), value.AnswerPoints...), Evidence: append([]domain.EvidenceBinding(nil), value.Evidence...), CardType: string(value.CardType), Difficulty: value.Difficulty, Status: string(value.Status), Fingerprint: value.Fingerprint, ModelVersion: value.ModelVersion, InvalidationReason: value.InvalidationReason, InvalidatedAt: optionalTime(value.InvalidatedAt), Version: value.Version, CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt)}
}
func toDueCardResponse(value domain.Card) dueCardCardResponse {
	return dueCardCardResponse{
		ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), DeckID: string(value.DeckID),
		Question: value.Question, CardType: string(value.CardType), Difficulty: value.Difficulty,
		Status: string(value.Status), Version: value.Version,
	}
}
func toScheduleResponse(value domain.Schedule) scheduleResponse {
	return scheduleResponse{CardID: string(value.CardID), WorkspaceID: string(value.WorkspaceID), DueAt: formatTime(value.DueAt), IntervalDays: value.IntervalDays, Stability: value.Stability, Difficulty: value.Difficulty, LastReviewedAt: optionalTime(value.LastReviewedAt), SchedulerVersion: value.SchedulerVersion, Paused: value.Paused, Version: value.Version}
}
func toSessionResponse(value domain.Session) sessionResponse {
	return sessionResponse{ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), DeckID: idString(value.DeckID), SessionType: string(value.SessionType), Status: string(value.Status), Config: append(json.RawMessage(nil), value.Config...), StartedAt: formatTime(value.StartedAt), EndedAt: optionalTime(value.EndedAt)}
}
func toAnswerResponse(value domain.Answer) answerResponse {
	return answerResponse{ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), SessionID: string(value.SessionID), CardID: idString(value.CardID), QuestionRef: value.QuestionRef, UserAnswer: value.UserAnswer, Rating: int(value.Rating), ScorerVersion: value.ScorerVersion, Score: toScoreResponse(value.WorkspaceID, value.Score), Feedback: value.Feedback, CreatedAt: formatTime(value.CreatedAt)}
}
func toScoreResponse(workspaceID foundation.ID, value domain.Score) scoreResponse {
	evidence := make([]scoreEvidenceResponse, 0, len(value.Evidence))
	for _, item := range value.Evidence {
		sourceVersionHref := "/api/v1/workspaces/" + url.PathEscape(string(workspaceID)) + "/source-versions/" + url.PathEscape(string(item.SourceVersionID))
		evidence = append(evidence, scoreEvidenceResponse{
			SchemaVersion: item.SchemaVersion, ClaimID: string(item.ClaimID), SourceVersionID: string(item.SourceVersionID),
			SourceSpanID: string(item.SourceSpanID), EvidenceHash: item.EvidenceHash,
			SourceVersionHref: sourceVersionHref, SourceSpanHref: sourceVersionHref + "/spans/" + url.PathEscape(string(item.SourceSpanID)),
		})
	}
	return scoreResponse{
		SchemaVersion: value.SchemaVersion, Correctness: value.Correctness, Coverage: value.Coverage,
		Boundaries: value.Boundaries, Clarity: value.Clarity, Confidence: value.Confidence,
		Errors: append([]string(nil), value.Errors...), Omissions: append([]string(nil), value.Omissions...), Evidence: evidence,
	}
}
func idString(value *foundation.ID) *string {
	if value == nil {
		return nil
	}
	copy := string(*value)
	return &copy
}
func optionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	copy := formatTime(*value)
	return &copy
}
func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func nilDependency(value any) bool {
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
func requestInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeJSONInvalid, false, errors.New(message))
}
func resultInvalid(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeResultInvalid, false, errors.New(message))
}
func (handler *Handler) available(w http.ResponseWriter) bool {
	if !handler.Available() {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, errorCodeUnavailable, "Review 服务暂不可用", true, nil)
		return false
	}
	return true
}
func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, domain.ErrorCodeDependencyUnavailable, "Review 请求超时", true, nil)
		return
	}
	if errors.Is(err, context.Canceled) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, domain.ErrorCodeDependencyUnavailable, "Review 请求已取消", false, nil)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Review 请求失败", false, nil)
		return
	}
	status := httpapi.StatusForErrorKind(classified.Kind)
	message := "Review 请求未完成"
	switch classified.Code {
	case "UNSUPPORTED_MEDIA_TYPE":
		status, message = http.StatusUnsupportedMediaType, "请求必须使用 application/json"
	case "INVALID_JSON", domain.ErrorCodeJSONInvalid, domain.ErrorCodeDeckInvalid, domain.ErrorCodeCardInvalid, domain.ErrorCodeEvidenceInvalid, domain.ErrorCodeEvidenceMissing, domain.ErrorCodeEvidenceDuplicate, domain.ErrorCodeInvalidationInvalid, domain.ErrorCodeScheduleInvalid, domain.ErrorCodeSessionInvalid, domain.ErrorCodeAnswerInvalid:
		status, message = http.StatusBadRequest, "Review 请求参数无效"
	case errorCodeResultInvalid:
		status, message = http.StatusInternalServerError, "Review 响应校验失败"
	case domain.ErrorCodeWorkspaceNotFound, domain.ErrorCodeDeckNotFound, domain.ErrorCodeCardNotFound, domain.ErrorCodeSessionNotFound:
		status, message = http.StatusNotFound, "请求的 Review 资源不存在"
	case domain.ErrorCodeDeckArchived, domain.ErrorCodeCardInactive, domain.ErrorCodeCardStateConflict, domain.ErrorCodeScheduleConflict, domain.ErrorCodeSessionClosed, domain.ErrorCodeSessionTypeConflict, domain.ErrorCodeAnswerIdempotencyConflict:
		status, message = http.StatusConflict, "Review 版本或状态冲突"
	case domain.ErrorCodeDependencyUnavailable, domain.ErrorCodeSchedulerUnavailable, domain.ErrorCodeScoringUnavailable:
		status, message = http.StatusServiceUnavailable, "Review 依赖暂不可用"
	}
	httpapi.WriteProblem(w, status, classified.Code, message, classified.Retryable, nil)
}

var _ Service = (*reviewapp.Service)(nil)
