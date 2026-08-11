// Package http exposes the authenticated HTTP contract for user-owned Memory.
package http

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	memoryapp "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	"github.com/gin-gonic/gin"
)

const (
	defaultListLimit       = 50
	maxWriteBodyBytes      = 128 * 1024
	maxTransitionBodyBytes = 4 * 1024
	maxCursorBytes         = 4096
	requestTimeout         = 2 * time.Second
	userManualSource       = "user:manual"

	errorCodeUnavailable   = "MEMORY_HTTP_UNAVAILABLE"
	errorCodeAuthRequired  = "MEMORY_AUTH_REQUIRED"
	errorCodeResultInvalid = "MEMORY_HTTP_RESULT_INCONSISTENT"
	errorCodeCursorInvalid = "MEMORY_CURSOR_INVALID"
)

// Service is the Memory application contract required by this HTTP boundary.
type Service interface {
	CreateCandidate(context.Context, memoryapp.CreateCandidateCommand) (memoryapp.CommandResult, error)
	Confirm(context.Context, memoryapp.TransitionCommand) (memoryapp.CommandResult, error)
	Edit(context.Context, memoryapp.EditCommand) (memoryapp.CommandResult, error)
	Pause(context.Context, memoryapp.TransitionCommand) (memoryapp.CommandResult, error)
	Resume(context.Context, memoryapp.TransitionCommand) (memoryapp.CommandResult, error)
	Delete(context.Context, memoryapp.TransitionCommand) (memoryapp.CommandResult, error)
	Get(context.Context, memoryapp.Scope, foundation.ID) (memorydomain.Memory, error)
	List(context.Context, memoryapp.ListQuery) (memoryapp.ListPage, error)
}

// Handler maps authenticated, owner-scoped requests to Memory application calls.
type Handler struct {
	service Service
	timeout time.Duration
}

// NewHandler creates a fail-closed Memory HTTP handler.
func NewHandler(service Service, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = requestTimeout
	}
	return &Handler{service: service, timeout: timeout}
}

// Available reports whether a concrete Memory service was composed.
func (handler *Handler) Available() bool {
	return handler != nil && !nilDependency(handler.service)
}

// Routes registers Memory candidate, lifecycle, and owner-scoped query routes.
func (handler *Handler) Routes(router gin.IRouter) {
	router.POST("/memories", httpapi.GinHandler(handler.createCandidate))
	router.GET("/memories", httpapi.GinHandler(handler.list))
	router.GET("/memories/:memory_id", httpapi.GinHandler(handler.get))
	router.POST("/memories/:memory_id/confirm", httpapi.GinHandler(handler.confirm))
	router.PUT("/memories/:memory_id", httpapi.GinHandler(handler.edit))
	router.POST("/memories/:memory_id/pause", httpapi.GinHandler(handler.pause))
	router.POST("/memories/:memory_id/resume", httpapi.GinHandler(handler.resume))
	router.DELETE("/memories/:memory_id", httpapi.GinHandler(handler.delete))
}

func (handler *Handler) createCandidate(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	owner, ok := authenticatedOwner(w, r)
	if !ok {
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
	input, err := decodeJSON[candidateRequest](r, maxWriteBodyBytes, "task_scope_id", "expires_at")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(input.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	taskScopeID, err := parseOptionalID(input.TaskScopeID)
	if err != nil {
		writeError(w, err)
		return
	}
	content, err := memorydomain.CanonicalContent(input.Content)
	if err != nil {
		writeError(w, err)
		return
	}
	source := memorydomain.Source{Type: memorydomain.SourceUser, Ref: userManualSource}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.CreateCandidate(ctx, memoryapp.CreateCandidateCommand{
		WorkspaceID:    workspaceID,
		Owner:          owner,
		Type:           input.Type,
		Content:        content,
		Source:         source,
		TaskScopeID:    taskScopeID,
		ExpiresAt:      cloneTime(input.ExpiresAt),
		IdempotencyKey: key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateCandidateResult(result, workspaceID, owner, input.Type, content, source, taskScopeID, input.ExpiresAt); err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	w.Header().Set("Location", "/api/v1/memories/"+string(result.Memory.ID)+"?workspace_id="+url.QueryEscape(string(workspaceID)))
	httpapi.WriteJSON(w, status, commandResponse{Memory: toMemoryResponse(result.Memory), Replayed: result.Replayed})
}

func (handler *Handler) get(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	owner, ok := authenticatedOwner(w, r)
	if !ok {
		return
	}
	workspaceID, memoryID, err := parseResourceLookup(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	memory, err := handler.service.Get(ctx, memoryapp.Scope{WorkspaceID: workspaceID, Owner: owner}, memoryID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateMemory(memory, workspaceID, owner, memoryID); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toMemoryResponse(memory))
}

func (handler *Handler) list(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	owner, ok := authenticatedOwner(w, r)
	if !ok {
		return
	}
	input, err := parseListRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(input.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	after, err := decodeCursor(input.Cursor, workspaceID, input.Types, input.Statuses)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.List(ctx, memoryapp.ListQuery{
		Scope:    memoryapp.Scope{WorkspaceID: workspaceID, Owner: owner},
		Types:    append([]memorydomain.Type(nil), input.Types...),
		Statuses: append([]memorydomain.Status(nil), input.Statuses...),
		Limit:    input.Limit,
		After:    after,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if len(page.Items) > input.Limit {
		writeError(w, resultInvalid("memory list exceeded its requested limit"))
		return
	}
	items := make([]memoryResponse, 0, len(page.Items))
	for index, memory := range page.Items {
		if err := validateMemory(memory, workspaceID, owner, ""); err != nil {
			writeError(w, err)
			return
		}
		if len(input.Types) > 0 && !slices.Contains(input.Types, memory.Type) ||
			len(input.Statuses) > 0 && !slices.Contains(input.Statuses, memory.Status) {
			writeError(w, resultInvalid("memory list escaped its requested filters"))
			return
		}
		if index > 0 && (memory.UpdatedAt.After(page.Items[index-1].UpdatedAt) ||
			memory.UpdatedAt.Equal(page.Items[index-1].UpdatedAt) && memory.ID >= page.Items[index-1].ID) {
			writeError(w, resultInvalid("memory list order is unstable"))
			return
		}
		items = append(items, toMemoryResponse(memory))
	}
	if page.Next != nil {
		if len(page.Items) == 0 || page.Next.UpdatedAt != page.Items[len(page.Items)-1].UpdatedAt || page.Next.ID != page.Items[len(page.Items)-1].ID {
			writeError(w, resultInvalid("memory list next cursor does not match its last item"))
			return
		}
	}
	next, err := encodeCursor(workspaceID, input.Types, input.Statuses, page.Next)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, listResponse{WorkspaceID: string(workspaceID), Items: items, NextCursor: next})
}

func (handler *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	handler.transition(w, r, memorydomain.StatusActive, func(ctx context.Context, command memoryapp.TransitionCommand) (memoryapp.CommandResult, error) {
		return handler.service.Confirm(ctx, command)
	})
}

func (handler *Handler) pause(w http.ResponseWriter, r *http.Request) {
	handler.transition(w, r, memorydomain.StatusPaused, func(ctx context.Context, command memoryapp.TransitionCommand) (memoryapp.CommandResult, error) {
		return handler.service.Pause(ctx, command)
	})
}

func (handler *Handler) resume(w http.ResponseWriter, r *http.Request) {
	handler.transition(w, r, memorydomain.StatusActive, func(ctx context.Context, command memoryapp.TransitionCommand) (memoryapp.CommandResult, error) {
		return handler.service.Resume(ctx, command)
	})
}

func (handler *Handler) delete(w http.ResponseWriter, r *http.Request) {
	handler.transition(w, r, memorydomain.StatusDeleted, func(ctx context.Context, command memoryapp.TransitionCommand) (memoryapp.CommandResult, error) {
		return handler.service.Delete(ctx, command)
	})
}

func (handler *Handler) transition(
	w http.ResponseWriter,
	r *http.Request,
	expectedStatus memorydomain.Status,
	execute func(context.Context, memoryapp.TransitionCommand) (memoryapp.CommandResult, error),
) {
	noStore(w)
	owner, ok := authenticatedOwner(w, r)
	if !ok {
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
	memoryID, err := parseID(r.PathValue("memory_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := decodeJSON[transitionRequest](r, maxTransitionBodyBytes)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(input.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if input.ExpectedVersion < 1 {
		writeError(w, requestInvalid("expected_version is invalid"))
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := execute(ctx, memoryapp.TransitionCommand{
		Scope:           memoryapp.Scope{WorkspaceID: workspaceID, Owner: owner},
		MemoryID:        memoryID,
		ExpectedVersion: input.ExpectedVersion,
		IdempotencyKey:  key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateCommandResult(result, workspaceID, owner, memoryID, input.ExpectedVersion+1, expectedStatus); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, commandResponse{Memory: toMemoryResponse(result.Memory), Replayed: result.Replayed})
}

func (handler *Handler) edit(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	owner, ok := authenticatedOwner(w, r)
	if !ok {
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
	memoryID, err := parseID(r.PathValue("memory_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := decodeJSON[editRequest](r, maxWriteBodyBytes, "task_scope_id", "expires_at")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(input.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	taskScopeID, err := parseOptionalID(input.TaskScopeID)
	if err != nil {
		writeError(w, err)
		return
	}
	if input.ExpectedVersion < 1 {
		writeError(w, requestInvalid("expected_version is invalid"))
		return
	}
	content, err := memorydomain.CanonicalContent(input.Content)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.Edit(ctx, memoryapp.EditCommand{
		Scope:           memoryapp.Scope{WorkspaceID: workspaceID, Owner: owner},
		MemoryID:        memoryID,
		ExpectedVersion: input.ExpectedVersion,
		Content:         content,
		TaskScopeID:     taskScopeID,
		ExpiresAt:       cloneTime(input.ExpiresAt),
		IdempotencyKey:  key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateEditResult(result, workspaceID, owner, memoryID, input.ExpectedVersion+1, content, taskScopeID, input.ExpiresAt); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, commandResponse{Memory: toMemoryResponse(result.Memory), Replayed: result.Replayed})
}

type candidateRequest struct {
	WorkspaceID string            `json:"workspace_id"`
	Type        memorydomain.Type `json:"type"`
	Content     json.RawMessage   `json:"content"`
	TaskScopeID *string           `json:"task_scope_id,omitempty"`
	ExpiresAt   *time.Time        `json:"expires_at,omitempty"`
}

type editRequest struct {
	WorkspaceID     string          `json:"workspace_id"`
	ExpectedVersion int64           `json:"expected_version"`
	Content         json.RawMessage `json:"content"`
	TaskScopeID     *string         `json:"task_scope_id,omitempty"`
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`
}

type transitionRequest struct {
	WorkspaceID     string `json:"workspace_id"`
	ExpectedVersion int64  `json:"expected_version"`
}

type listRequest struct {
	WorkspaceID string
	Types       []memorydomain.Type
	Statuses    []memorydomain.Status
	Limit       int
	Cursor      string
}

type commandResponse struct {
	Memory   memoryResponse `json:"memory"`
	Replayed bool           `json:"replayed"`
}

type listResponse struct {
	WorkspaceID string           `json:"workspace_id"`
	Items       []memoryResponse `json:"items"`
	NextCursor  string           `json:"next_cursor,omitempty"`
}

// memoryResponse deliberately omits owner and confirmed_by because ownership
// is a server-side authorization fact, not user-facing Memory content.
type memoryResponse struct {
	ID          string              `json:"id"`
	WorkspaceID string              `json:"workspace_id"`
	Type        memorydomain.Type   `json:"type"`
	Content     json.RawMessage     `json:"content"`
	Source      memorydomain.Source `json:"source"`
	TaskScopeID *foundation.ID      `json:"task_scope_id,omitempty"`
	Status      memorydomain.Status `json:"status"`
	ExpiresAt   *time.Time          `json:"expires_at,omitempty"`
	ConfirmedAt *time.Time          `json:"confirmed_at,omitempty"`
	Version     int64               `json:"version"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

func toMemoryResponse(memory memorydomain.Memory) memoryResponse {
	return memoryResponse{
		ID: string(memory.ID), WorkspaceID: string(memory.WorkspaceID), Type: memory.Type,
		Content: cloneJSON(memory.Content), Source: memory.Source, TaskScopeID: cloneID(memory.TaskScopeID),
		Status: memory.Status, ExpiresAt: cloneTime(memory.ExpiresAt), ConfirmedAt: cloneTime(memory.ConfirmedAt),
		Version: memory.Version, CreatedAt: memory.CreatedAt, UpdatedAt: memory.UpdatedAt,
	}
}

func authenticatedOwner(w http.ResponseWriter, r *http.Request) (memorydomain.Principal, bool) {
	principal, ok := authhttp.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, foundation.NewError(foundation.ErrorPermissionDenied, errorCodeAuthRequired, false, errors.New("authenticated principal is required")))
		return memorydomain.Principal{}, false
	}
	owner, err := memorydomain.OwnerForAuthenticatedPrincipal(principal)
	if err != nil {
		writeError(w, foundation.NewError(foundation.ErrorPermissionDenied, errorCodeAuthRequired, false, errors.New("authenticated principal is invalid")))
		return memorydomain.Principal{}, false
	}
	return owner, true
}

func (handler *Handler) available(w http.ResponseWriter) bool {
	if !handler.Available() {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, errorCodeUnavailable, "Memory service is unavailable", true, nil)
		return false
	}
	return true
}

func parseResourceLookup(r *http.Request) (foundation.ID, foundation.ID, error) {
	values, err := parseQuery(r, map[string]queryRule{"workspace_id": singleRequired})
	if err != nil {
		return "", "", err
	}
	workspaceID, err := parseID(values.Get("workspace_id"))
	if err != nil {
		return "", "", err
	}
	memoryID, err := parseID(r.PathValue("memory_id"))
	if err != nil {
		return "", "", err
	}
	return workspaceID, memoryID, nil
}

func parseListRequest(r *http.Request) (listRequest, error) {
	values, err := parseQuery(r, map[string]queryRule{
		"workspace_id": singleRequired,
		"type":         repeatedOptional,
		"status":       repeatedOptional,
		"limit":        singleOptional,
		"cursor":       singleOptional,
	})
	if err != nil {
		return listRequest{}, err
	}
	limit := defaultListLimit
	if raw := values.Get("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > memorydomain.MaxListLimit {
			return listRequest{}, requestInvalid("list limit is invalid")
		}
		limit = parsed
	}
	types, err := canonicalTypes(values["type"])
	if err != nil {
		return listRequest{}, err
	}
	statuses, err := canonicalStatuses(values["status"])
	if err != nil {
		return listRequest{}, err
	}
	if len(values.Get("cursor")) > maxCursorBytes {
		return listRequest{}, cursorInvalid()
	}
	return listRequest{WorkspaceID: values.Get("workspace_id"), Types: types, Statuses: statuses, Limit: limit, Cursor: values.Get("cursor")}, nil
}

type queryRule uint8

const (
	singleRequired queryRule = iota + 1
	singleOptional
	repeatedOptional
)

func parseQuery(r *http.Request, allowed map[string]queryRule) (url.Values, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, requestInvalid("query parameters are invalid")
	}
	for key, entries := range values {
		rule, ok := allowed[key]
		if !ok || len(entries) == 0 || (rule != repeatedOptional && len(entries) != 1) {
			return nil, requestInvalid("query parameter is invalid or not allowed")
		}
		for _, value := range entries {
			if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) {
				return nil, requestInvalid("query parameter is invalid or not allowed")
			}
		}
	}
	for key, rule := range allowed {
		if rule == singleRequired && len(values[key]) != 1 {
			return nil, requestInvalid("required query parameter is missing")
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

func canonicalTypes(raw []string) ([]memorydomain.Type, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	values := make([]memorydomain.Type, len(raw))
	for index, value := range raw {
		values[index] = memorydomain.Type(value)
	}
	slices.Sort(values)
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			return nil, requestInvalid("memory type filters must be unique")
		}
	}
	return values, nil
}

func canonicalStatuses(raw []string) ([]memorydomain.Status, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	values := make([]memorydomain.Status, len(raw))
	for index, value := range raw {
		values[index] = memorydomain.Status(value)
	}
	slices.Sort(values)
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			return nil, requestInvalid("memory status filters must be unique")
		}
	}
	return values, nil
}

type memoryCursor struct {
	Version     int                   `json:"version"`
	WorkspaceID string                `json:"workspace_id"`
	Types       []memorydomain.Type   `json:"types"`
	Statuses    []memorydomain.Status `json:"statuses"`
	UpdatedAt   string                `json:"updated_at"`
	ID          string                `json:"id"`
}

func encodeCursor(workspaceID foundation.ID, types []memorydomain.Type, statuses []memorydomain.Status, value *memoryapp.Cursor) (string, error) {
	if value == nil {
		return "", nil
	}
	if value.UpdatedAt.IsZero() {
		return "", cursorInvalid()
	}
	id, err := parseID(string(value.ID))
	if err != nil || id != value.ID {
		return "", cursorInvalid()
	}
	payload, err := json.Marshal(memoryCursor{
		Version: 1, WorkspaceID: string(workspaceID), Types: append([]memorydomain.Type(nil), types...),
		Statuses: append([]memorydomain.Status(nil), statuses...), UpdatedAt: formatTime(value.UpdatedAt), ID: string(value.ID),
	})
	if err != nil || len(payload) == 0 || len(payload) > maxCursorBytes {
		return "", cursorInvalid()
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeCursor(raw string, workspaceID foundation.ID, types []memorydomain.Type, statuses []memorydomain.Status) (*memoryapp.Cursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) == 0 || len(decoded) > maxCursorBytes {
		return nil, cursorInvalid()
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxCursorBytes
	limits.MaxDepth = 4
	limits.MaxArrayItems = memorydomain.MaxListLimit
	cursor, err := strictjson.DecodeObject[memoryCursor](decoded, limits, nil)
	if err != nil {
		return nil, cursorInvalid()
	}
	at, err := time.Parse(time.RFC3339Nano, cursor.UpdatedAt)
	id, idErr := parseID(cursor.ID)
	if err != nil || idErr != nil || cursor.Version != 1 || cursor.WorkspaceID != string(workspaceID) ||
		at.UTC().Format(time.RFC3339Nano) != cursor.UpdatedAt || !slices.Equal(cursor.Types, types) || !slices.Equal(cursor.Statuses, statuses) {
		return nil, cursorInvalid()
	}
	return &memoryapp.Cursor{UpdatedAt: at.UTC(), ID: id}, nil
}

func decodeJSON[T any](r *http.Request, maxBytes int, rejectNullFields ...string) (T, error) {
	var zero T
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("request requires application/json"))
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBytes)+1))
	if err != nil || len(body) == 0 || len(body) > maxBytes {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, errors.New("request JSON is empty or oversized"))
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxBytes
	if len(rejectNullFields) > 0 {
		fields, inspectErr := strictjson.DecodeObject[map[string]json.RawMessage](body, limits, nil)
		if inspectErr != nil {
			return zero, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, inspectErr)
		}
		for _, field := range rejectNullFields {
			if raw, found := fields[field]; found && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return zero, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, errors.New("optional request fields cannot be null"))
			}
		}
	}
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
	if key == "" || key != strings.TrimSpace(key) || len(key) > memorydomain.MaxIdempotencyKeyBytes {
		return "", requestInvalid("Idempotency-Key is invalid")
	}
	for _, character := range key {
		if unicode.IsControl(character) {
			return "", requestInvalid("Idempotency-Key is invalid")
		}
	}
	if err := memorydomain.ValidateIdempotencyKey(key); err != nil {
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

func validateCommandResult(result memoryapp.CommandResult, workspaceID foundation.ID, owner memorydomain.Principal, memoryID foundation.ID, version int64, expectedStatus memorydomain.Status) error {
	if err := validateMemory(result.Memory, workspaceID, owner, memoryID); err != nil || result.Memory.Version != version ||
		expectedStatus != "" && result.Memory.Status != expectedStatus {
		return resultInvalid("memory command response crossed its requested binding")
	}
	return nil
}

func validateCandidateResult(
	result memoryapp.CommandResult,
	workspaceID foundation.ID,
	owner memorydomain.Principal,
	typ memorydomain.Type,
	content json.RawMessage,
	source memorydomain.Source,
	taskScopeID *foundation.ID,
	expiresAt *time.Time,
) error {
	if err := validateCommandResult(result, workspaceID, owner, "", 1, memorydomain.StatusCandidate); err != nil ||
		result.Memory.Type != typ || !bytes.Equal(result.Memory.Content, content) || result.Memory.Source != source || !sameID(result.Memory.TaskScopeID, taskScopeID) ||
		!sameMemoryTime(result.Memory.ExpiresAt, expiresAt) {
		return resultInvalid("memory candidate response crossed its requested payload")
	}
	return nil
}

func validateEditResult(
	result memoryapp.CommandResult,
	workspaceID foundation.ID,
	owner memorydomain.Principal,
	memoryID foundation.ID,
	version int64,
	content json.RawMessage,
	taskScopeID *foundation.ID,
	expiresAt *time.Time,
) error {
	if err := validateCommandResult(result, workspaceID, owner, memoryID, version, ""); err != nil ||
		!bytes.Equal(result.Memory.Content, content) || !sameID(result.Memory.TaskScopeID, taskScopeID) ||
		!sameMemoryTime(result.Memory.ExpiresAt, expiresAt) {
		return resultInvalid("memory edit response crossed its requested payload")
	}
	return nil
}

func validateMemory(memory memorydomain.Memory, workspaceID foundation.ID, owner memorydomain.Principal, memoryID foundation.ID) error {
	if memorydomain.ValidateMemory(memory) != nil || memory.WorkspaceID != workspaceID || !samePrincipal(memory.Owner, owner) ||
		memoryID != "" && memory.ID != memoryID {
		return resultInvalid("memory response crossed its requested binding")
	}
	return nil
}

func samePrincipal(left, right memorydomain.Principal) bool {
	return left.Kind == right.Kind && left.ID == right.ID
}

func sameID(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameMemoryTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.UTC().Truncate(time.Microsecond).Equal(right.UTC().Truncate(time.Microsecond))
}

func requestInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, memorydomain.ErrorCodeInvalid, false, errors.New(message))
}

func resultInvalid(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeResultInvalid, false, errors.New(message))
}

func cursorInvalid() error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeCursorInvalid, false, errors.New("memory cursor is invalid"))
}

func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, memorydomain.ErrorCodeUnavailable, "Memory request timed out", true, nil)
		return
	}
	if errors.Is(err, context.Canceled) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, memorydomain.ErrorCodeUnavailable, "Memory request was canceled", false, nil)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Memory request failed", false, nil)
		return
	}
	status, message := httpapi.StatusForErrorKind(classified.Kind), "Memory request was not completed"
	switch classified.Code {
	case "UNSUPPORTED_MEDIA_TYPE":
		status, message = http.StatusUnsupportedMediaType, "Request must use application/json"
	case "INVALID_JSON", memorydomain.ErrorCodeInvalid, errorCodeCursorInvalid:
		status, message = http.StatusBadRequest, "Memory request is invalid"
	case errorCodeAuthRequired, memorydomain.ErrorCodeOwnerDenied:
		status, message = http.StatusForbidden, "Authenticated Memory owner is required"
	case memorydomain.ErrorCodeNotFound:
		status, message = http.StatusNotFound, "Memory resource was not found"
	case memorydomain.ErrorCodeStateConflict, memorydomain.ErrorCodeVersionConflict, memorydomain.ErrorCodeIdempotencyConflict, memorydomain.ErrorCodeExpired:
		status, message = http.StatusConflict, "Memory lifecycle or version conflicts with the request"
	case memorydomain.ErrorCodeUnavailable:
		status, message = http.StatusServiceUnavailable, "Memory dependency is unavailable"
	case memorydomain.ErrorCodePersistenceInvalid, memorydomain.ErrorCodeResultInconsistent, errorCodeResultInvalid:
		status, message = http.StatusInternalServerError, "Memory response could not be validated"
	}
	httpapi.WriteProblem(w, status, classified.Code, message, classified.Retryable, nil)
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func cloneID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
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

var _ Service = (*memoryapp.Service)(nil)
