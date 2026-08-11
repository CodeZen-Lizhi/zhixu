// Package interviewhttp exposes the strict, authenticated HTTP contract for Interview and Learning Path.
package interviewhttp

import (
	"context"
	"encoding/base64"
	"encoding/hex"
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

	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"github.com/gin-gonic/gin"
)

const (
	// EvidenceSchemaVersion 是对外返回的脱敏 Interview Evidence 契约版本。
	EvidenceSchemaVersion = "interview-evidence/v1"
)

const (
	defaultRequestTimeout = 2 * time.Second
	defaultListLimit      = 20
	maxBodyBytes          = 128 * 1024
	maxCursorBytes        = 4096
	maxIdempotencyKeySize = 128

	errorCodeUnavailable    = "INTERVIEW_HTTP_UNAVAILABLE"
	errorCodeAuthRequired   = "INTERVIEW_AUTH_REQUIRED"
	errorCodeRequestInvalid = "INTERVIEW_REQUEST_INVALID"
	errorCodeJSONInvalid    = "INTERVIEW_JSON_INVALID"
	errorCodeCursorInvalid  = "INTERVIEW_CURSOR_INVALID"
	errorCodeResultInvalid  = "INTERVIEW_HTTP_RESULT_INCONSISTENT"
)

// Service is the Interview application contract used by the HTTP boundary.
type Service interface {
	Start(context.Context, interviewapp.StartCommand) (interviewapp.StartResult, error)
	Get(context.Context, foundation.ID, foundation.ID) (interviewapp.Snapshot, error)
	List(context.Context, interviewapp.SessionListQuery) (interviewapp.SessionListPage, error)
	SubmitTurn(context.Context, interviewapp.SubmitTurnCommand) (interviewapp.SubmitTurnResult, error)
	Complete(context.Context, interviewapp.CompleteCommand) (interviewapp.CompleteResult, error)
	UpdatePathStep(context.Context, interviewapp.UpdatePathStepCommand) (interviewapp.PathStepResult, error)
	UpdatePathStatus(context.Context, interviewapp.UpdatePathStatusCommand) (interviewapp.PathStatusResult, error)
	SuggestMemoryCandidate(context.Context, interviewapp.SuggestMemoryCandidateCommand) (interviewapp.MemoryCandidateResult, error)
}

// Handler maps authenticated Interview requests to the application service.
// Identity is required at this edge. The current Interview application contract
// is workspace-scoped and has no persistent owner field to forward.
type Handler struct {
	service Service
	timeout time.Duration
}

// NewHandler creates a fail-closed Interview HTTP handler.
func NewHandler(service Service, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	return &Handler{service: service, timeout: timeout}
}

// Available reports whether this handler has a concrete application dependency.
func (handler *Handler) Available() bool {
	return handler != nil && !nilDependency(handler.service)
}

// Routes registers routes beneath the caller's /api/v1 router.
func (handler *Handler) Routes(router gin.IRouter) {
	router.POST("/review/interviews", httpapi.GinHandler(handler.start))
	router.GET("/review/interviews", httpapi.GinHandler(handler.list))
	router.GET("/review/interviews/:session_id", httpapi.GinHandler(handler.get))
	router.POST("/review/interviews/:session_id/turns", httpapi.GinHandler(handler.submitTurn))
	router.POST("/review/interviews/:session_id/complete", httpapi.GinHandler(handler.complete))
	router.POST("/review/interviews/:session_id/learning-paths/:path_id/steps/:step_id/memory-candidate", httpapi.GinHandler(handler.suggestMemoryCandidate))
	router.PUT("/review/learning-paths/:path_id/status", httpapi.GinHandler(handler.updatePathStatus))
	router.PUT("/review/learning-paths/:path_id/steps/:step_id", httpapi.GinHandler(handler.updatePathStep))
}

func (handler *Handler) list(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !requirePrincipal(w, r) {
		return
	}
	input, err := parseSessionListRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(input.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	after, err := decodeSessionCursor(input.Cursor, workspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.List(ctx, interviewapp.SessionListQuery{WorkspaceID: workspaceID, Limit: input.Limit, After: after})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateSessionListPage(page, workspaceID, input.Limit); err != nil {
		writeError(w, err)
		return
	}
	next, err := encodeSessionCursor(workspaceID, page.Next)
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]sessionResponse, 0, len(page.Items))
	for _, session := range page.Items {
		items = append(items, toSessionResponse(session))
	}
	httpapi.WriteJSON(w, http.StatusOK, sessionListResponse{WorkspaceID: string(workspaceID), Items: items, NextCursor: next})
}

func (handler *Handler) start(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !requirePrincipal(w, r) {
		return
	}
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := decodeJSON[startRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(input.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	config, err := domain.CanonicalConfig(input.Config)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.Start(ctx, interviewapp.StartCommand{
		WorkspaceID: workspaceID, Config: config, IdempotencyKey: key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateStartResult(result, workspaceID, config); err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, startResponse{
		Session: toSessionResponse(result.Session), Questions: toQuestionResponses(result.Questions), Replayed: result.Replayed,
	})
}

func (handler *Handler) get(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !requirePrincipal(w, r) {
		return
	}
	workspaceID, sessionID, err := parseSessionLookup(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	snapshot, err := handler.service.Get(ctx, workspaceID, sessionID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateSnapshot(snapshot, workspaceID, sessionID); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toSnapshotResponse(snapshot, workspaceID))
}

func (handler *Handler) submitTurn(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !requirePrincipal(w, r) {
		return
	}
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
	input, err := decodeJSON[submitTurnRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(input.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	questionID, err := parseID(input.QuestionID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !validUserAnswer(input.UserAnswer) {
		writeError(w, requestInvalid("user_answer is invalid"))
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.SubmitTurn(ctx, interviewapp.SubmitTurnCommand{
		WorkspaceID: workspaceID, SessionID: sessionID, QuestionID: questionID, UserAnswer: input.UserAnswer, IdempotencyKey: key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateSubmitTurnResult(result, workspaceID, sessionID, questionID, input.UserAnswer, key); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, submitTurnResponse{
		Turn: toTurnResponse(result.Turn, workspaceID), FollowUp: toQuestionResponsePointer(result.FollowUp),
		NextQuestion: toQuestionResponsePointer(result.NextQuestion), Replayed: result.Replayed,
	})
}

func (handler *Handler) complete(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !requirePrincipal(w, r) {
		return
	}
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
	input, err := decodeJSON[completeRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(input.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.Complete(ctx, interviewapp.CompleteCommand{
		WorkspaceID: workspaceID, SessionID: sessionID, ManualEnd: input.ManualEnd, IdempotencyKey: key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateCompleteResult(result, workspaceID, sessionID); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, completeResponse{
		Session: toSessionResponse(result.Session), Report: toReportResponse(result.Report, workspaceID), Path: toPathResponse(result.Path),
		Steps: toPathStepResponses(result.Steps), Replayed: result.Replayed,
	})
}

func (handler *Handler) updatePathStatus(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !requirePrincipal(w, r) {
		return
	}
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	pathID, err := parseID(r.PathValue("path_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := decodeJSON[pathStatusRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(input.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if input.ExpectedVersion < 1 || !validPathStatus(input.Status) {
		writeError(w, requestInvalid("learning path status request is invalid"))
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.UpdatePathStatus(ctx, interviewapp.UpdatePathStatusCommand{
		WorkspaceID: workspaceID, PathID: pathID, ExpectedVersion: input.ExpectedVersion, Status: input.Status, IdempotencyKey: key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validatePathStatusResult(result, workspaceID, pathID, input.ExpectedVersion, input.Status); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, pathStatusResponse{Path: toPathResponse(result.Path), Replayed: result.Replayed})
}

func (handler *Handler) updatePathStep(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !requirePrincipal(w, r) {
		return
	}
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	pathID, err := parseID(r.PathValue("path_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	stepID, err := parseID(r.PathValue("step_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := decodeJSON[pathStepRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(input.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if input.ExpectedVersion < 1 || !validStepStatus(input.Status) {
		writeError(w, requestInvalid("learning path step request is invalid"))
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.UpdatePathStep(ctx, interviewapp.UpdatePathStepCommand{
		WorkspaceID: workspaceID, PathID: pathID, StepID: stepID, ExpectedVersion: input.ExpectedVersion, Status: input.Status, IdempotencyKey: key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validatePathStepResult(result, workspaceID, pathID, stepID, input.ExpectedVersion, input.Status); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, pathStepCommandResponse{Path: toPathResponse(result.Path), Step: toPathStepResponse(result.Step), Replayed: result.Replayed})
}

func (handler *Handler) suggestMemoryCandidate(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !requirePrincipal(w, r) {
		return
	}
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
	pathID, err := parseID(r.PathValue("path_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	stepID, err := parseID(r.PathValue("step_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := decodeJSON[memoryCandidateRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(input.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.SuggestMemoryCandidate(ctx, interviewapp.SuggestMemoryCandidateCommand{
		WorkspaceID: workspaceID, SessionID: sessionID, PathID: pathID, StepID: stepID, IdempotencyKey: key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if !isCanonicalID(result.MemoryID) {
		writeError(w, resultInvalid("interview memory candidate result is invalid"))
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, memoryCandidateResponse{MemoryID: string(result.MemoryID), Replayed: result.Replayed})
}

type startRequest struct {
	WorkspaceID string        `json:"workspace_id"`
	Config      domain.Config `json:"config"`
}

type sessionListRequest struct {
	WorkspaceID string
	Limit       int
	Cursor      string
}

type submitTurnRequest struct {
	WorkspaceID string `json:"workspace_id"`
	QuestionID  string `json:"question_id"`
	UserAnswer  string `json:"user_answer"`
}

type completeRequest struct {
	WorkspaceID string `json:"workspace_id"`
	ManualEnd   bool   `json:"manual_end"`
}

type pathStatusRequest struct {
	WorkspaceID     string            `json:"workspace_id"`
	ExpectedVersion int64             `json:"expected_version"`
	Status          domain.PathStatus `json:"status"`
}

type pathStepRequest struct {
	WorkspaceID     string            `json:"workspace_id"`
	ExpectedVersion int64             `json:"expected_version"`
	Status          domain.StepStatus `json:"status"`
}

type memoryCandidateRequest struct {
	WorkspaceID string `json:"workspace_id"`
}

type startResponse struct {
	Session   sessionResponse    `json:"session"`
	Questions []questionResponse `json:"questions"`
	Replayed  bool               `json:"replayed"`
}

type sessionListResponse struct {
	WorkspaceID string            `json:"workspace_id"`
	Items       []sessionResponse `json:"items"`
	NextCursor  string            `json:"next_cursor,omitempty"`
}

type sessionCursorDocument struct {
	Version     int    `json:"version"`
	WorkspaceID string `json:"workspace_id"`
	StartedAt   string `json:"started_at"`
	ID          string `json:"id"`
}

type snapshotResponse struct {
	Session   sessionResponse    `json:"session"`
	Questions []questionResponse `json:"questions"`
	Turns     []turnResponse     `json:"turns"`
	Report    *reportResponse    `json:"report,omitempty"`
	Path      *pathResponse      `json:"path,omitempty"`
	Steps     []pathStepResponse `json:"steps"`
}

type submitTurnResponse struct {
	Turn         turnResponse      `json:"turn"`
	FollowUp     *questionResponse `json:"follow_up,omitempty"`
	NextQuestion *questionResponse `json:"next_question,omitempty"`
	Replayed     bool              `json:"replayed"`
}

type completeResponse struct {
	Session  sessionResponse    `json:"session"`
	Report   reportResponse     `json:"report"`
	Path     pathResponse       `json:"path"`
	Steps    []pathStepResponse `json:"steps"`
	Replayed bool               `json:"replayed"`
}

type pathStatusResponse struct {
	Path     pathResponse `json:"path"`
	Replayed bool         `json:"replayed"`
}

type pathStepCommandResponse struct {
	Path     pathResponse     `json:"path"`
	Step     pathStepResponse `json:"step"`
	Replayed bool             `json:"replayed"`
}

type memoryCandidateResponse struct {
	MemoryID string `json:"memory_id"`
	Replayed bool   `json:"replayed"`
}

type configResponse struct {
	SchemaVersion   string        `json:"schema_version"`
	Role            string        `json:"role"`
	Scope           scopeResponse `json:"scope"`
	Difficulty      string        `json:"difficulty"`
	DurationMinutes int           `json:"duration_minutes"`
	QuestionCount   int           `json:"question_count"`
	MaxFollowUps    int           `json:"max_follow_ups"`
}

type scopeResponse struct {
	ClaimIDs []string `json:"claim_ids,omitempty"`
	TopicIDs []string `json:"topic_ids,omitempty"`
}

type sessionResponse struct {
	ID            string         `json:"id"`
	WorkspaceID   string         `json:"workspace_id"`
	Config        configResponse `json:"config"`
	Status        string         `json:"status"`
	Version       int64          `json:"version"`
	FollowUpCount int            `json:"follow_up_count"`
	StartedAt     string         `json:"started_at"`
	EndedAt       *string        `json:"ended_at,omitempty"`
}

// questionResponse deliberately excludes AnswerPoints and Evidence. Both are
// scoring references and must not reach the participant before an answer.
type questionResponse struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspace_id"`
	SessionID        string  `json:"session_id"`
	QuestionNo       int     `json:"question_no"`
	FollowUpNo       int     `json:"follow_up_no"`
	ParentQuestionID *string `json:"parent_question_id,omitempty"`
	ClaimID          string  `json:"claim_id"`
	TopicID          *string `json:"topic_id,omitempty"`
	Prompt           string  `json:"prompt"`
	Status           string  `json:"status"`
	CreatedAt        string  `json:"created_at"`
	AnsweredAt       *string `json:"answered_at,omitempty"`
}

type evidenceResponse struct {
	SchemaVersion     string `json:"schema_version"`
	ClaimID           string `json:"claim_id"`
	SourceVersionID   string `json:"source_version_id"`
	SourceSpanID      string `json:"source_span_id"`
	EvidenceHash      string `json:"evidence_hash"`
	SupportType       string `json:"support_type"`
	SourceVersionHref string `json:"source_version_href"`
	SourceSpanHref    string `json:"source_span_href"`
}

type scoreDimensionResponse struct {
	Value     float64 `json:"value"`
	Rationale string  `json:"rationale"`
}

type scoreResponse struct {
	SchemaVersion string                 `json:"schema_version"`
	Correctness   scoreDimensionResponse `json:"correctness"`
	Coverage      scoreDimensionResponse `json:"coverage"`
	Boundaries    scoreDimensionResponse `json:"boundaries"`
	Clarity       scoreDimensionResponse `json:"clarity"`
	Errors        []string               `json:"errors,omitempty"`
	Omissions     []string               `json:"omissions,omitempty"`
	Evidence      []evidenceResponse     `json:"evidence"`
}

type turnDecisionResponse struct {
	FollowUpCreated    bool    `json:"follow_up_created"`
	FollowUpQuestionID *string `json:"follow_up_question_id,omitempty"`
	NextQuestionID     *string `json:"next_question_id,omitempty"`
}

// turnResponse deliberately excludes UserAnswer, IdempotencyKey, and
// RequestHash. A participant's raw answer is not a default session projection.
type turnResponse struct {
	ID            string               `json:"id"`
	WorkspaceID   string               `json:"workspace_id"`
	SessionID     string               `json:"session_id"`
	QuestionID    string               `json:"question_id"`
	Score         scoreResponse        `json:"score"`
	Decision      turnDecisionResponse `json:"decision"`
	ScorerVersion string               `json:"scorer_version"`
	CreatedAt     string               `json:"created_at"`
}

type findingResponse struct {
	ClaimID  string             `json:"claim_id"`
	TopicID  *string            `json:"topic_id,omitempty"`
	Detail   string             `json:"detail"`
	Evidence []evidenceResponse `json:"evidence"`
}

type reportSummaryResponse struct {
	QuestionsTotal int     `json:"questions_total"`
	AnsweredTotal  int     `json:"answered_total"`
	SkippedTotal   int     `json:"skipped_total"`
	Correctness    float64 `json:"correctness"`
	Coverage       float64 `json:"coverage"`
	Boundaries     float64 `json:"boundaries"`
	Clarity        float64 `json:"clarity"`
}

type artifactBindingResponse struct {
	Kind            string `json:"kind"`
	ArtifactID      string `json:"artifact_id"`
	RevisionID      string `json:"revision_id"`
	ArtifactVersion int64  `json:"artifact_version"`
}

type reportResponse struct {
	ID            string                  `json:"id"`
	WorkspaceID   string                  `json:"workspace_id"`
	SessionID     string                  `json:"session_id"`
	SchemaVersion string                  `json:"schema_version"`
	Summary       reportSummaryResponse   `json:"summary"`
	Strengths     []findingResponse       `json:"strengths"`
	Gaps          []findingResponse       `json:"gaps"`
	Expression    []findingResponse       `json:"expression"`
	Evidence      []evidenceResponse      `json:"evidence"`
	Artifact      artifactBindingResponse `json:"artifact"`
	CreatedAt     string                  `json:"created_at"`
}

type pathResponse struct {
	ID          string                  `json:"id"`
	WorkspaceID string                  `json:"workspace_id"`
	SessionID   string                  `json:"session_id"`
	ReportID    string                  `json:"report_id"`
	Artifact    artifactBindingResponse `json:"artifact"`
	Status      string                  `json:"status"`
	Version     int64                   `json:"version"`
	CreatedAt   string                  `json:"created_at"`
	UpdatedAt   string                  `json:"updated_at"`
}

type pathStepResponse struct {
	ID              string  `json:"id"`
	WorkspaceID     string  `json:"workspace_id"`
	PathID          string  `json:"path_id"`
	StepNo          int     `json:"step_no"`
	ClaimID         string  `json:"claim_id"`
	TopicID         *string `json:"topic_id,omitempty"`
	SourceVersionID string  `json:"source_version_id"`
	SourceSpanID    string  `json:"source_span_id"`
	EvidenceHash    string  `json:"evidence_hash"`
	Title           string  `json:"title"`
	Rationale       string  `json:"rationale"`
	Status          string  `json:"status"`
	Version         int64   `json:"version"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

func toSnapshotResponse(value interviewapp.Snapshot, workspaceID foundation.ID) snapshotResponse {
	response := snapshotResponse{
		Session: toSessionResponse(value.Session), Questions: toQuestionResponses(value.Questions),
		Turns: toTurnResponses(value.Turns, workspaceID), Steps: toPathStepResponses(value.Steps),
	}
	if value.Report != nil {
		report := toReportResponse(*value.Report, workspaceID)
		response.Report = &report
	}
	if value.Path != nil {
		path := toPathResponse(*value.Path)
		response.Path = &path
	}
	return response
}

func toConfigResponse(value domain.Config) configResponse {
	return configResponse{
		SchemaVersion: value.SchemaVersion, Role: value.Role, Scope: scopeResponse{ClaimIDs: idStrings(value.Scope.ClaimIDs), TopicIDs: idStrings(value.Scope.TopicIDs)},
		Difficulty: string(value.Difficulty), DurationMinutes: value.DurationMinutes, QuestionCount: value.QuestionCount, MaxFollowUps: value.MaxFollowUps,
	}
}

func toSessionResponse(value domain.Session) sessionResponse {
	return sessionResponse{
		ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), Config: toConfigResponse(value.Config), Status: string(value.Status),
		Version: value.Version, FollowUpCount: value.FollowUpCount, StartedAt: formatTime(value.StartedAt), EndedAt: optionalTime(value.EndedAt),
	}
}

func toQuestionResponses(values []domain.Question) []questionResponse {
	response := make([]questionResponse, 0, len(values))
	for _, value := range values {
		response = append(response, toQuestionResponse(value))
	}
	return response
}

func toQuestionResponsePointer(value *domain.Question) *questionResponse {
	if value == nil {
		return nil
	}
	response := toQuestionResponse(*value)
	return &response
}

func toQuestionResponse(value domain.Question) questionResponse {
	return questionResponse{
		ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), SessionID: string(value.SessionID), QuestionNo: value.QuestionNo,
		FollowUpNo: value.FollowUpNo, ParentQuestionID: optionalID(value.ParentQuestionID), ClaimID: string(value.ClaimID), TopicID: optionalID(value.TopicID),
		Prompt: value.Prompt, Status: string(value.Status), CreatedAt: formatTime(value.CreatedAt), AnsweredAt: optionalTime(value.AnsweredAt),
	}
}

func toEvidenceResponses(values []domain.EvidenceRef, workspaceID foundation.ID) []evidenceResponse {
	response := make([]evidenceResponse, 0, len(values))
	for _, value := range values {
		sourceVersionHref := "/api/v1/workspaces/" + url.PathEscape(string(workspaceID)) + "/source-versions/" + url.PathEscape(string(value.SourceVersionID))
		response = append(response, evidenceResponse{
			SchemaVersion: EvidenceSchemaVersion, ClaimID: string(value.ClaimID), SourceVersionID: string(value.SourceVersionID), SourceSpanID: string(value.SourceSpanID),
			EvidenceHash: value.EvidenceHash, SupportType: value.SupportType, SourceVersionHref: sourceVersionHref,
			SourceSpanHref: sourceVersionHref + "/spans/" + url.PathEscape(string(value.SourceSpanID)),
		})
	}
	return response
}

func toTurnResponses(values []domain.Turn, workspaceID foundation.ID) []turnResponse {
	response := make([]turnResponse, 0, len(values))
	for _, value := range values {
		response = append(response, toTurnResponse(value, workspaceID))
	}
	return response
}

func toTurnResponse(value domain.Turn, workspaceID foundation.ID) turnResponse {
	return turnResponse{
		ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), SessionID: string(value.SessionID), QuestionID: string(value.QuestionID),
		Score: toScoreResponse(value.Score, workspaceID), Decision: turnDecisionResponse{
			FollowUpCreated: value.Decision.FollowUpCreated, FollowUpQuestionID: optionalID(value.Decision.FollowUpQuestionID), NextQuestionID: optionalID(value.Decision.NextQuestionID),
		},
		ScorerVersion: value.ScorerVersion, CreatedAt: formatTime(value.CreatedAt),
	}
}

func toScoreResponse(value domain.Score, workspaceID foundation.ID) scoreResponse {
	return scoreResponse{
		SchemaVersion: value.SchemaVersion, Correctness: toScoreDimensionResponse(value.Correctness), Coverage: toScoreDimensionResponse(value.Coverage),
		Boundaries: toScoreDimensionResponse(value.Boundaries), Clarity: toScoreDimensionResponse(value.Clarity), Errors: append([]string(nil), value.Errors...),
		Omissions: append([]string(nil), value.Omissions...), Evidence: toEvidenceResponses(value.Evidence, workspaceID),
	}
}

func toScoreDimensionResponse(value domain.ScoreDimension) scoreDimensionResponse {
	return scoreDimensionResponse{Value: value.Value, Rationale: value.Rationale}
}

func toReportResponse(value domain.Report, workspaceID foundation.ID) reportResponse {
	return reportResponse{
		ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), SessionID: string(value.SessionID), SchemaVersion: value.SchemaVersion,
		Summary: reportSummaryResponse{
			QuestionsTotal: value.Summary.QuestionsTotal, AnsweredTotal: value.Summary.AnsweredTotal, SkippedTotal: value.Summary.SkippedTotal,
			Correctness: value.Summary.Correctness, Coverage: value.Summary.Coverage, Boundaries: value.Summary.Boundaries, Clarity: value.Summary.Clarity,
		},
		Strengths: toFindingResponses(value.Strengths, workspaceID), Gaps: toFindingResponses(value.Gaps, workspaceID),
		Expression: toFindingResponses(value.Expression, workspaceID), Evidence: toEvidenceResponses(value.Evidence, workspaceID),
		Artifact: toArtifactBindingResponse(value.Artifact), CreatedAt: formatTime(value.CreatedAt),
	}
}

func toFindingResponses(values []domain.Finding, workspaceID foundation.ID) []findingResponse {
	response := make([]findingResponse, 0, len(values))
	for _, value := range values {
		response = append(response, findingResponse{ClaimID: string(value.ClaimID), TopicID: optionalID(value.TopicID), Detail: value.Detail, Evidence: toEvidenceResponses(value.Evidence, workspaceID)})
	}
	return response
}

func toArtifactBindingResponse(value domain.ArtifactBinding) artifactBindingResponse {
	return artifactBindingResponse{Kind: value.Kind, ArtifactID: string(value.ArtifactID), RevisionID: string(value.RevisionID), ArtifactVersion: value.ArtifactVersion}
}

func toPathResponse(value domain.LearningPath) pathResponse {
	return pathResponse{
		ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), SessionID: string(value.SessionID), ReportID: string(value.ReportID),
		Artifact: toArtifactBindingResponse(value.Artifact), Status: string(value.Status), Version: value.Version, CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt),
	}
}

func toPathStepResponses(values []domain.PathStep) []pathStepResponse {
	response := make([]pathStepResponse, 0, len(values))
	for _, value := range values {
		response = append(response, toPathStepResponse(value))
	}
	return response
}

func toPathStepResponse(value domain.PathStep) pathStepResponse {
	return pathStepResponse{
		ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), PathID: string(value.PathID), StepNo: value.StepNo,
		ClaimID: string(value.ClaimID), TopicID: optionalID(value.TopicID), SourceVersionID: string(value.SourceVersionID), SourceSpanID: string(value.SourceSpanID),
		EvidenceHash: value.EvidenceHash, Title: value.Title, Rationale: value.Rationale, Status: string(value.Status), Version: value.Version,
		CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt),
	}
}

func validateSessionListPage(page interviewapp.SessionListPage, workspaceID foundation.ID, limit int) error {
	if len(page.Items) > limit {
		return resultInvalid("interview session list exceeded its requested limit")
	}
	for index, session := range page.Items {
		if err := validateSession(session, workspaceID, ""); err != nil {
			return resultInvalid("interview session list escaped its requested workspace")
		}
		if index > 0 {
			previous := page.Items[index-1]
			if session.StartedAt.After(previous.StartedAt) || session.StartedAt.Equal(previous.StartedAt) && session.ID >= previous.ID {
				return resultInvalid("interview session list order is unstable")
			}
		}
	}
	if page.Next != nil {
		if len(page.Items) == 0 {
			return resultInvalid("interview session list cursor has no boundary item")
		}
		last := page.Items[len(page.Items)-1]
		if !page.Next.StartedAt.Equal(last.StartedAt) || page.Next.ID != last.ID {
			return resultInvalid("interview session list cursor does not match its boundary item")
		}
	}
	return nil
}

func validateStartResult(value interviewapp.StartResult, workspaceID foundation.ID, config domain.Config) error {
	if err := validateSession(value.Session, workspaceID, ""); err != nil || value.Session.Status != domain.SessionStatusActive || !reflect.DeepEqual(value.Session.Config, config) || len(value.Questions) != config.QuestionCount {
		return resultInvalid("interview start response crossed its requested binding")
	}
	seen := make(map[foundation.ID]struct{}, len(value.Questions))
	seenNumbers := make(map[int]struct{}, len(value.Questions))
	for _, question := range value.Questions {
		if err := validateQuestion(question, workspaceID, value.Session.ID); err != nil || question.FollowUpNo != 0 || question.ParentQuestionID != nil || question.Status != domain.QuestionStatusPending || question.QuestionNo > config.QuestionCount {
			return resultInvalid("interview start response contains an invalid initial question")
		}
		if _, exists := seen[question.ID]; exists {
			return resultInvalid("interview start response has duplicate questions")
		}
		seen[question.ID] = struct{}{}
		if _, exists := seenNumbers[question.QuestionNo]; exists {
			return resultInvalid("interview start response has duplicate question numbers")
		}
		seenNumbers[question.QuestionNo] = struct{}{}
	}
	return nil
}

func validateSnapshot(value interviewapp.Snapshot, workspaceID, sessionID foundation.ID) error {
	if err := validateSession(value.Session, workspaceID, sessionID); err != nil {
		return resultInvalid("interview snapshot session crossed its requested binding")
	}
	questions := make(map[foundation.ID]domain.Question, len(value.Questions))
	questionPositions := make(map[struct{ question, followUp int }]struct{}, len(value.Questions))
	for _, question := range value.Questions {
		if err := validateQuestion(question, workspaceID, sessionID); err != nil {
			return resultInvalid("interview snapshot question crossed its requested binding")
		}
		if _, exists := questions[question.ID]; exists {
			return resultInvalid("interview snapshot has duplicate question IDs")
		}
		questions[question.ID] = question
		position := struct{ question, followUp int }{question.QuestionNo, question.FollowUpNo}
		if _, exists := questionPositions[position]; exists {
			return resultInvalid("interview snapshot has duplicate question positions")
		}
		questionPositions[position] = struct{}{}
	}
	turnIDs := make(map[foundation.ID]struct{}, len(value.Turns))
	answeredQuestions := make(map[foundation.ID]struct{}, len(value.Turns))
	for _, turn := range value.Turns {
		question, found := questions[turn.QuestionID]
		if !found || question.Status != domain.QuestionStatusAnswered || question.AnsweredAt == nil || turn.WorkspaceID != workspaceID || turn.SessionID != sessionID || domain.ValidateTurn(turn, question) != nil {
			return resultInvalid("interview snapshot turn crossed its requested binding")
		}
		if _, exists := turnIDs[turn.ID]; exists {
			return resultInvalid("interview snapshot has duplicate turn IDs")
		}
		turnIDs[turn.ID] = struct{}{}
		if _, exists := answeredQuestions[turn.QuestionID]; exists {
			return resultInvalid("interview snapshot has duplicate turns for one question")
		}
		answeredQuestions[turn.QuestionID] = struct{}{}
	}
	for questionID, question := range questions {
		_, hasTurn := answeredQuestions[questionID]
		if (question.Status == domain.QuestionStatusAnswered) != hasTurn {
			return resultInvalid("interview snapshot question and turn states are inconsistent")
		}
	}
	hasCompletion := value.Report != nil && value.Path != nil
	if value.Report == nil || value.Path == nil {
		if value.Report != nil || value.Path != nil || len(value.Steps) != 0 || value.Session.Status == domain.SessionStatusCompleted {
			return resultInvalid("interview snapshot completion facts are incomplete")
		}
		return nil
	}
	if !hasCompletion || value.Session.Status != domain.SessionStatusCompleted {
		return resultInvalid("interview snapshot completion facts do not match session status")
	}
	if err := validateReport(*value.Report, workspaceID, sessionID); err != nil {
		return resultInvalid("interview snapshot report crossed its requested binding")
	}
	if err := validatePath(*value.Path, workspaceID, sessionID, value.Report.ID); err != nil {
		return resultInvalid("interview snapshot learning path crossed its requested binding")
	}
	if err := validatePathSteps(value.Steps, workspaceID, value.Path.ID); err != nil {
		return resultInvalid("interview snapshot learning path steps crossed their requested binding")
	}
	return nil
}

func validateSubmitTurnResult(value interviewapp.SubmitTurnResult, workspaceID, sessionID, questionID foundation.ID, userAnswer, idempotencyKey string) error {
	turn := value.Turn
	if !isCanonicalID(turn.ID) || turn.WorkspaceID != workspaceID || turn.SessionID != sessionID || turn.QuestionID != questionID ||
		turn.UserAnswer != userAnswer || turn.IdempotencyKey != idempotencyKey || !validHash(turn.RequestHash) || turn.CreatedAt.IsZero() ||
		!validRequiredText(turn.ScorerVersion, 128) || domain.ValidateScore(turn.Score, turn.Score.Evidence) != nil {
		return resultInvalid("interview turn response crossed its requested binding")
	}
	for _, evidence := range turn.Score.Evidence {
		if domain.ValidateEvidenceRef(evidence) != nil {
			return resultInvalid("interview turn score contains invalid evidence")
		}
	}
	if value.FollowUp == nil {
		if turn.Decision.FollowUpCreated || turn.Decision.FollowUpQuestionID != nil {
			return resultInvalid("interview turn response has an invalid follow-up decision")
		}
	} else {
		if err := validateQuestion(*value.FollowUp, workspaceID, sessionID); err != nil || value.FollowUp.Status != domain.QuestionStatusPending || value.FollowUp.FollowUpNo < 1 || !turn.Decision.FollowUpCreated || turn.Decision.FollowUpQuestionID == nil ||
			*turn.Decision.FollowUpQuestionID != value.FollowUp.ID || value.FollowUp.ParentQuestionID == nil || *value.FollowUp.ParentQuestionID != questionID {
			return resultInvalid("interview turn response follow-up crossed its requested binding")
		}
	}
	if value.NextQuestion == nil {
		if turn.Decision.NextQuestionID != nil {
			return resultInvalid("interview turn response has an invalid next-question decision")
		}
	} else if err := validateQuestion(*value.NextQuestion, workspaceID, sessionID); err != nil || value.NextQuestion.Status != domain.QuestionStatusPending || turn.Decision.NextQuestionID == nil || *turn.Decision.NextQuestionID != value.NextQuestion.ID {
		return resultInvalid("interview turn response next question crossed its requested binding")
	}
	return nil
}

func validateCompleteResult(value interviewapp.CompleteResult, workspaceID, sessionID foundation.ID) error {
	if err := validateSession(value.Session, workspaceID, sessionID); err != nil || value.Session.Status != domain.SessionStatusCompleted {
		return resultInvalid("interview completion session crossed its requested binding")
	}
	if err := validateReport(value.Report, workspaceID, sessionID); err != nil {
		return resultInvalid("interview completion report crossed its requested binding")
	}
	if err := validatePath(value.Path, workspaceID, sessionID, value.Report.ID); err != nil {
		return resultInvalid("interview completion learning path crossed its requested binding")
	}
	if err := validatePathSteps(value.Steps, workspaceID, value.Path.ID); err != nil {
		return resultInvalid("interview completion learning path steps crossed their requested binding")
	}
	return nil
}

func validatePathStatusResult(value interviewapp.PathStatusResult, workspaceID, pathID foundation.ID, expectedVersion int64, status domain.PathStatus) error {
	if err := validatePath(value.Path, workspaceID, "", ""); err != nil || value.Path.ID != pathID || value.Path.Version != expectedVersion+1 || value.Path.Status != status {
		return resultInvalid("learning path status response crossed its requested binding")
	}
	return nil
}

func validatePathStepResult(value interviewapp.PathStepResult, workspaceID, pathID, stepID foundation.ID, expectedVersion int64, status domain.StepStatus) error {
	if err := validatePath(value.Path, workspaceID, "", ""); err != nil || value.Path.ID != pathID || value.Path.Version != expectedVersion+1 ||
		validatePathStep(value.Step, workspaceID, pathID) != nil || value.Step.ID != stepID || value.Step.Status != status {
		return resultInvalid("learning path step response crossed its requested binding")
	}
	return nil
}

func validateSession(value domain.Session, workspaceID, sessionID foundation.ID) error {
	if value.WorkspaceID != workspaceID || (sessionID != "" && value.ID != sessionID) || domain.ValidateSession(value) != nil {
		return resultInvalid("interview session response is invalid")
	}
	return nil
}

func validateQuestion(value domain.Question, workspaceID, sessionID foundation.ID) error {
	if value.WorkspaceID != workspaceID || value.SessionID != sessionID || domain.ValidateQuestion(value) != nil {
		return resultInvalid("interview question response is invalid")
	}
	return nil
}

func validateReport(value domain.Report, workspaceID, sessionID foundation.ID) error {
	if value.WorkspaceID != workspaceID || value.SessionID != sessionID || domain.ValidateReport(value) != nil {
		return resultInvalid("interview report response is invalid")
	}
	return nil
}

func validatePath(value domain.LearningPath, workspaceID, sessionID, reportID foundation.ID) error {
	if value.WorkspaceID != workspaceID || (sessionID != "" && value.SessionID != sessionID) || (reportID != "" && value.ReportID != reportID) || domain.ValidateLearningPath(value) != nil {
		return resultInvalid("learning path response is invalid")
	}
	return nil
}

func validatePathSteps(values []domain.PathStep, workspaceID, pathID foundation.ID) error {
	seen := make(map[foundation.ID]struct{}, len(values))
	seenNumbers := make(map[int]struct{}, len(values))
	for _, value := range values {
		if err := validatePathStep(value, workspaceID, pathID); err != nil {
			return err
		}
		if _, exists := seen[value.ID]; exists {
			return resultInvalid("learning path response has duplicate steps")
		}
		seen[value.ID] = struct{}{}
		if _, exists := seenNumbers[value.StepNo]; exists {
			return resultInvalid("learning path response has duplicate step numbers")
		}
		seenNumbers[value.StepNo] = struct{}{}
	}
	return nil
}

func validatePathStep(value domain.PathStep, workspaceID, pathID foundation.ID) error {
	if value.WorkspaceID != workspaceID || value.PathID != pathID || domain.ValidatePathStep(value) != nil {
		return resultInvalid("learning path step response is invalid")
	}
	return nil
}

func requirePrincipal(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := authhttp.PrincipalFromContext(r.Context()); ok {
		return true
	}
	writeError(w, foundation.NewError(foundation.ErrorPermissionDenied, errorCodeAuthRequired, false, errors.New("authenticated principal is required")))
	return false
}

func (handler *Handler) available(w http.ResponseWriter) bool {
	if handler.Available() {
		return true
	}
	httpapi.WriteProblem(w, http.StatusServiceUnavailable, errorCodeUnavailable, "Interview service is unavailable", true, nil)
	return false
}

func parseSessionLookup(r *http.Request) (foundation.ID, foundation.ID, error) {
	values, err := parseQuery(r, map[string]queryRule{"workspace_id": querySingleRequired})
	if err != nil {
		return "", "", err
	}
	workspaceID, err := parseID(values.Get("workspace_id"))
	if err != nil {
		return "", "", err
	}
	sessionID, err := parseID(r.PathValue("session_id"))
	if err != nil {
		return "", "", err
	}
	return workspaceID, sessionID, nil
}

func parseSessionListRequest(r *http.Request) (sessionListRequest, error) {
	values, err := parseQuery(r, map[string]queryRule{
		"workspace_id": querySingleRequired,
		"limit":        querySingleOptional,
		"cursor":       querySingleOptional,
	})
	if err != nil {
		return sessionListRequest{}, err
	}
	limit := defaultListLimit
	if raw := values.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > interviewapp.MaxSessionListLimit {
			return sessionListRequest{}, requestInvalid("interview session list limit is invalid")
		}
		limit = parsed
	}
	if len(values.Get("cursor")) > maxCursorBytes {
		return sessionListRequest{}, cursorInvalid()
	}
	return sessionListRequest{WorkspaceID: values.Get("workspace_id"), Limit: limit, Cursor: values.Get("cursor")}, nil
}

type queryRule uint8

const (
	querySingleRequired queryRule = iota + 1
	querySingleOptional
)

func parseQuery(r *http.Request, allowed map[string]queryRule) (url.Values, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, requestInvalid("query parameters are invalid")
	}
	for key, entries := range values {
		if _, ok := allowed[key]; !ok || len(entries) != 1 || !validQueryValue(entries[0]) {
			return nil, requestInvalid("query parameter is invalid or not allowed")
		}
	}
	for key, rule := range allowed {
		if rule == querySingleRequired && len(values[key]) != 1 {
			return nil, requestInvalid("required query parameter is missing")
		}
	}
	return values, nil
}

func encodeSessionCursor(workspaceID foundation.ID, cursor *interviewapp.SessionCursor) (string, error) {
	if cursor == nil {
		return "", nil
	}
	if cursor.StartedAt.IsZero() || !isCanonicalID(cursor.ID) {
		return "", cursorInvalid()
	}
	payload, err := json.Marshal(sessionCursorDocument{
		Version: 1, WorkspaceID: string(workspaceID), StartedAt: formatTime(cursor.StartedAt), ID: string(cursor.ID),
	})
	if err != nil || len(payload) == 0 || len(payload) > maxCursorBytes {
		return "", cursorInvalid()
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeSessionCursor(raw string, workspaceID foundation.ID) (*interviewapp.SessionCursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) == 0 || len(decoded) > maxCursorBytes {
		return nil, cursorInvalid()
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxCursorBytes
	limits.MaxDepth = 3
	cursor, err := strictjson.DecodeObject[sessionCursorDocument](decoded, limits, nil)
	if err != nil {
		return nil, cursorInvalid()
	}
	startedAt, timeErr := time.Parse(time.RFC3339Nano, cursor.StartedAt)
	id, idErr := parseID(cursor.ID)
	if timeErr != nil || idErr != nil || cursor.Version != 1 || cursor.WorkspaceID != string(workspaceID) ||
		startedAt.UTC().Format(time.RFC3339Nano) != cursor.StartedAt {
		return nil, cursorInvalid()
	}
	return &interviewapp.SessionCursor{StartedAt: startedAt.UTC(), ID: id}, nil
}

func validQueryValue(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value)
}

func rejectQuery(r *http.Request) error {
	if r.URL.RawQuery != "" {
		return requestInvalid("query parameters are not allowed")
	}
	return nil
}

func decodeJSON[T any](r *http.Request) (T, error) {
	var zero T
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("request requires application/json"))
	}
	if r.Body == nil {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, errorCodeJSONInvalid, false, errors.New("request JSON is missing"))
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxBodyBytes {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, errorCodeJSONInvalid, false, errors.New("request JSON is empty or oversized"))
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxBodyBytes
	limits.MaxStringBytes = maxBodyBytes
	value, err := strictjson.DecodeObject[T](body, limits, nil)
	if err != nil {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, errorCodeJSONInvalid, false, err)
	}
	return value, nil
}

func parseIdempotencyKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || !utf8.ValidString(values[0]) {
		return "", requestInvalid("exactly one Idempotency-Key is required")
	}
	key := values[0]
	if key == "" || key != strings.TrimSpace(key) || len(key) > maxIdempotencyKeySize {
		return "", requestInvalid("Idempotency-Key is invalid")
	}
	for _, character := range key {
		if unicode.IsControl(character) {
			return "", requestInvalid("Idempotency-Key is invalid")
		}
	}
	if err := domain.ValidateIdempotencyKey(key); err != nil {
		return "", requestInvalid("Idempotency-Key is invalid")
	}
	return key, nil
}

func parseID(raw string) (foundation.ID, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return "", requestInvalid("UUID is invalid")
	}
	value, err := foundation.ParseID(raw)
	if err != nil || string(value) != raw {
		return "", requestInvalid("UUID is invalid")
	}
	return value, nil
}

func isCanonicalID(value foundation.ID) bool {
	parsed, err := parseID(string(value))
	return err == nil && parsed == value
}

func validUserAnswer(value string) bool {
	return len(value) <= 64*1024 && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validRequiredText(value string, maxBytes int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validPathStatus(value domain.PathStatus) bool {
	return value == domain.PathStatusActive || value == domain.PathStatusPaused || value == domain.PathStatusCompleted
}

func validStepStatus(value domain.StepStatus) bool {
	return value == domain.StepStatusPending || value == domain.StepStatusInProgress || value == domain.StepStatusCompleted || value == domain.StepStatusSkipped
}

func requestInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeRequestInvalid, false, errors.New(message))
}

func cursorInvalid() error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeCursorInvalid, false, errors.New("interview session cursor is invalid"))
}

func resultInvalid(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeResultInvalid, false, errors.New(message))
}

func writeError(w http.ResponseWriter, err error) {
	noStore(w)
	if errors.Is(err, context.DeadlineExceeded) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, domain.ErrorCodeDependencyUnavailable, "Interview request timed out", true, nil)
		return
	}
	if errors.Is(err, context.Canceled) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, domain.ErrorCodeDependencyUnavailable, "Interview request was canceled", false, nil)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Interview request failed", false, nil)
		return
	}
	status, message := httpapi.StatusForErrorKind(classified.Kind), "Interview request was not completed"
	switch classified.Code {
	case "UNSUPPORTED_MEDIA_TYPE":
		status, message = http.StatusUnsupportedMediaType, "Request must use application/json"
	case errorCodeJSONInvalid, errorCodeRequestInvalid, errorCodeCursorInvalid, domain.ErrorCodeConfigInvalid, domain.ErrorCodeTurnInvalid, domain.ErrorCodePathInvalid:
		status, message = http.StatusBadRequest, "Interview request is invalid"
	case errorCodeAuthRequired:
		status, message = http.StatusForbidden, "Authenticated principal is required"
	case domain.ErrorCodeSessionNotFound, domain.ErrorCodeQuestionNotFound:
		status, message = http.StatusNotFound, "Interview resource was not found"
	case domain.ErrorCodeSessionClosed, domain.ErrorCodeSessionExpired, domain.ErrorCodeQuestionOrderConflict, domain.ErrorCodeIdempotencyConflict:
		status, message = http.StatusConflict, "Interview state conflicts with the request"
	case domain.ErrorCodeScorerUnavailable, domain.ErrorCodeDependencyUnavailable:
		status, message = http.StatusServiceUnavailable, "Interview dependency is unavailable"
	case errorCodeResultInvalid, domain.ErrorCodePersistenceInvalid:
		status, message = http.StatusInternalServerError, "Interview response could not be validated"
	}
	httpapi.WriteProblem(w, status, classified.Code, message, classified.Retryable, nil)
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func optionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}

func optionalID(value *foundation.ID) *string {
	if value == nil {
		return nil
	}
	copy := string(*value)
	return &copy
}

func idStrings(values []foundation.ID) []string {
	if len(values) == 0 {
		return nil
	}
	response := make([]string, len(values))
	for index, value := range values {
		response[index] = string(value)
	}
	return response
}

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

var _ Service = (*interviewapp.Service)(nil)
