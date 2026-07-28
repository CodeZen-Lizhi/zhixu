// Package learningpathhttp exposes strict workspace-scoped HTTP endpoints for Review-derived Learning Paths.
package learningpathhttp

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	pathapp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	pathdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/domain"
	"github.com/go-chi/chi/v5"
)

const (
	defaultTimeout = 2 * time.Second
	maxBodyBytes   = 128 * 1024
)

// Service is the narrow application contract consumed by the HTTP boundary.
type Service interface {
	CreateForReview(context.Context, pathapp.CreateReviewCommand) (pathapp.Result, error)
	GetForReviewAnswer(context.Context, foundation.ID, foundation.ID) (pathapp.Result, error)
	UpdateStatus(context.Context, pathapp.UpdatePathStatusCommand) (pathapp.Result, error)
	UpdateStep(context.Context, pathapp.UpdateStepCommand) (pathapp.StepResult, error)
}

// Handler maps authenticated requests to shared Learning Path commands.
type Handler struct {
	service Service
	timeout time.Duration
}

// NewHandler constructs a fail-closed Learning Path HTTP handler.
func NewHandler(service Service, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Handler{service: service, timeout: timeout}
}

// Available reports whether a concrete Learning Path service was composed.
func (handler *Handler) Available() bool {
	return handler != nil && !nilDependency(handler.service)
}

// Routes registers endpoints under the caller's /api/v1 router.
func (handler *Handler) Routes(router chi.Router) {
	router.Post("/review/answers/{answer_id}/learning-path", handler.create)
	router.Get("/review/answers/{answer_id}/learning-path", handler.get)
	router.Put("/review/answers/{answer_id}/learning-path/status", handler.updateStatus)
	router.Put("/review/answers/{answer_id}/learning-path/steps/{step_id}", handler.updateStep)
}

type workspaceRequest struct {
	WorkspaceID string `json:"workspace_id"`
}
type statusRequest struct {
	WorkspaceID     string `json:"workspace_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Status          string `json:"status"`
}
type stepRequest struct {
	WorkspaceID     string `json:"workspace_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Status          string `json:"status"`
}

type artifactResponse struct {
	Kind            string `json:"kind"`
	ArtifactID      string `json:"artifact_id"`
	RevisionID      string `json:"revision_id"`
	ArtifactVersion int64  `json:"artifact_version"`
}

type pathResponse struct {
	ID                  string           `json:"id"`
	WorkspaceID         string           `json:"workspace_id"`
	OriginType          string           `json:"origin_type"`
	ReviewAnswerID      string           `json:"review_answer_id"`
	Artifact            artifactResponse `json:"artifact"`
	SourcePolicyVersion string           `json:"source_policy_version"`
	Status              string           `json:"status"`
	Version             int64            `json:"version"`
	CreatedAt           time.Time        `json:"created_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
}

type stepResponse struct {
	ID              string    `json:"id"`
	WorkspaceID     string    `json:"workspace_id"`
	PathID          string    `json:"path_id"`
	StepNo          int       `json:"step_no"`
	ClaimID         string    `json:"claim_id"`
	TopicID         string    `json:"topic_id,omitempty"`
	SourceVersionID string    `json:"source_version_id"`
	SourceSpanID    string    `json:"source_span_id"`
	EvidenceHash    string    `json:"evidence_hash"`
	Title           string    `json:"title"`
	Rationale       string    `json:"rationale"`
	Status          string    `json:"status"`
	Version         int64     `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type resultResponse struct {
	Path     pathResponse   `json:"path"`
	Steps    []stepResponse `json:"steps"`
	Replayed bool           `json:"replayed"`
}

type statusResponse struct {
	Path     pathResponse `json:"path"`
	Replayed bool         `json:"replayed"`
}
type stepResultResponse struct {
	Path     pathResponse `json:"path"`
	Step     stepResponse `json:"step"`
	Replayed bool         `json:"replayed"`
}

func (handler *Handler) create(w http.ResponseWriter, request *http.Request) {
	if !handler.prerequisites(w, request, true) {
		return
	}
	key, ok := idempotencyKey(w, request)
	if !ok {
		return
	}
	input, ok := decode[workspaceRequest](w, request)
	if !ok {
		return
	}
	workspaceID, ok := id(w, input.WorkspaceID)
	if !ok {
		return
	}
	answerID, ok := id(w, chi.URLParam(request, "answer_id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.CreateForReview(ctx, pathapp.CreateReviewCommand{WorkspaceID: workspaceID, ReviewAnswerID: answerID, IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, toResultResponse(result))
}

func (handler *Handler) get(w http.ResponseWriter, request *http.Request) {
	if !handler.prerequisites(w, request, false) {
		return
	}
	if err := rejectBody(request); err != nil {
		writeError(w, err)
		return
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["workspace_id"]) != 1 {
		writeError(w, invalid("query parameters are invalid"))
		return
	}
	workspaceID, ok := id(w, query["workspace_id"][0])
	if !ok {
		return
	}
	answerID, ok := id(w, chi.URLParam(request, "answer_id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.GetForReviewAnswer(ctx, workspaceID, answerID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toResultResponse(result))
}

func (handler *Handler) updateStatus(w http.ResponseWriter, request *http.Request) {
	if !handler.prerequisites(w, request, true) {
		return
	}
	key, ok := idempotencyKey(w, request)
	if !ok {
		return
	}
	input, ok := decode[statusRequest](w, request)
	if !ok {
		return
	}
	workspaceID, ok := id(w, input.WorkspaceID)
	if !ok {
		return
	}
	answerID, ok := id(w, chi.URLParam(request, "answer_id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	path, err := handler.service.GetForReviewAnswer(ctx, workspaceID, answerID)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := handler.service.UpdateStatus(ctx, pathapp.UpdatePathStatusCommand{WorkspaceID: workspaceID, PathID: path.Path.ID, ExpectedVersion: input.ExpectedVersion, Status: pathdomain.Status(input.Status), IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	if result.Path.ReviewAnswerID == nil || *result.Path.ReviewAnswerID != answerID {
		writeError(w, invalid("path does not belong to review answer"))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, statusResponse{Path: toPathResponse(result.Path), Replayed: result.Replayed})
}

func (handler *Handler) updateStep(w http.ResponseWriter, request *http.Request) {
	if !handler.prerequisites(w, request, true) {
		return
	}
	key, ok := idempotencyKey(w, request)
	if !ok {
		return
	}
	input, ok := decode[stepRequest](w, request)
	if !ok {
		return
	}
	workspaceID, ok := id(w, input.WorkspaceID)
	if !ok {
		return
	}
	answerID, ok := id(w, chi.URLParam(request, "answer_id"))
	if !ok {
		return
	}
	stepID, ok := id(w, chi.URLParam(request, "step_id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	path, err := handler.service.GetForReviewAnswer(ctx, workspaceID, answerID)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := handler.service.UpdateStep(ctx, pathapp.UpdateStepCommand{WorkspaceID: workspaceID, PathID: path.Path.ID, StepID: stepID, ExpectedVersion: input.ExpectedVersion, Status: pathdomain.StepStatus(input.Status), IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	if result.Path.ReviewAnswerID == nil || *result.Path.ReviewAnswerID != answerID {
		writeError(w, invalid("path does not belong to review answer"))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, stepResultResponse{Path: toPathResponse(result.Path), Step: toStepResponse(result.Step), Replayed: result.Replayed})
}

func toResultResponse(result pathapp.Result) resultResponse {
	steps := make([]stepResponse, 0, len(result.Steps))
	for _, step := range result.Steps {
		steps = append(steps, toStepResponse(step))
	}
	return resultResponse{Path: toPathResponse(result.Path), Steps: steps, Replayed: result.Replayed}
}

func toPathResponse(path pathdomain.Path) pathResponse {
	answerID := ""
	if path.ReviewAnswerID != nil {
		answerID = string(*path.ReviewAnswerID)
	}
	return pathResponse{ID: string(path.ID), WorkspaceID: string(path.WorkspaceID), OriginType: string(path.OriginType), ReviewAnswerID: answerID, Artifact: artifactResponse{Kind: pathapp.ArtifactKind, ArtifactID: string(path.Artifact.ArtifactID), RevisionID: string(path.Artifact.ArtifactRevisionID), ArtifactVersion: path.Artifact.ArtifactVersion}, SourcePolicyVersion: path.SourcePolicyVersion, Status: string(path.Status), Version: path.Version, CreatedAt: path.CreatedAt, UpdatedAt: path.UpdatedAt}
}

func toStepResponse(step pathdomain.Step) stepResponse {
	topicID := ""
	if step.TopicID != nil {
		topicID = string(*step.TopicID)
	}
	return stepResponse{ID: string(step.ID), WorkspaceID: string(step.WorkspaceID), PathID: string(step.PathID), StepNo: step.StepNo, ClaimID: string(step.ClaimID), TopicID: topicID, SourceVersionID: string(step.SourceVersionID), SourceSpanID: string(step.SourceSpanID), EvidenceHash: step.EvidenceHash, Title: step.Title, Rationale: step.Rationale, Status: string(step.Status), Version: step.Version, CreatedAt: step.CreatedAt, UpdatedAt: step.UpdatedAt}
}

func (handler *Handler) prerequisites(w http.ResponseWriter, request *http.Request, write bool) bool {
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := authhttp.PrincipalFromContext(request.Context()); !ok {
		writeError(w, foundation.NewError(foundation.ErrorPermissionDenied, "LEARNING_PATH_AUTH_REQUIRED", false, errors.New("authenticated principal is required")))
		return false
	}
	if !handler.Available() {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, "LEARNING_PATH_HTTP_UNAVAILABLE", "Learning Path service is unavailable", true, nil)
		return false
	}
	if write && request.URL.RawQuery != "" {
		writeError(w, invalid("query parameters are not allowed"))
		return false
	}
	return true
}
func idempotencyKey(w http.ResponseWriter, request *http.Request) (string, bool) {
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 || !utf8.ValidString(values[0]) {
		writeError(w, invalid("Idempotency-Key is invalid"))
		return "", false
	}
	key := values[0]
	if strings.TrimSpace(key) != key || key == "" || len(key) > 128 {
		writeError(w, invalid("Idempotency-Key is invalid"))
		return "", false
	}
	for _, character := range key {
		if unicode.IsControl(character) {
			writeError(w, invalid("Idempotency-Key is invalid"))
			return "", false
		}
	}
	return key, true
}
func id(w http.ResponseWriter, value string) (foundation.ID, bool) {
	parsed, err := foundation.ParseID(value)
	if err != nil {
		writeError(w, invalid("identifier is invalid"))
		return "", false
	}
	return parsed, true
}
func decode[T any](w http.ResponseWriter, request *http.Request) (T, bool) {
	var zero T
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("request requires application/json")))
		return zero, false
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxBodyBytes {
		writeError(w, invalid("request JSON is invalid"))
		return zero, false
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxBodyBytes
	value, err := strictjson.DecodeObject[T](body, limits, nil)
	if err != nil {
		writeError(w, invalid("request JSON is invalid"))
		return zero, false
	}
	return value, true
}
func rejectBody(request *http.Request) error {
	if request.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 1))
	if err != nil {
		return invalid("GET request body is invalid")
	}
	if len(body) == 0 {
		return nil
	}
	return invalid("GET request must not include a body")
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
func invalid(message string) error {
	return pathdomain.InvalidError(pathdomain.ErrorCodePathInvalid, message)
}
func writeError(w http.ResponseWriter, err error) {
	var typed *foundation.Error
	if errors.As(err, &typed) {
		status := httpapi.StatusForErrorKind(typed.Kind)
		if typed.Code == "UNSUPPORTED_MEDIA_TYPE" {
			status = http.StatusUnsupportedMediaType
		}
		httpapi.WriteProblem(w, status, typed.Code, typed.Error(), typed.Retryable, nil)
		return
	}
	httpapi.WriteProblem(w, http.StatusInternalServerError, "LEARNING_PATH_HTTP_FAILED", "Learning Path request failed", false, nil)
}
