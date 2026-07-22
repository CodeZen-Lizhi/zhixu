// Package collectionhttp exposes the strict public HTTP contract for Smart Collections.
package collectionhttp

import (
	"context"
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

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/go-chi/chi/v5"
)

const (
	defaultCollectionListLimit       = 50
	defaultCollectionResultLimit     = 25
	maxCollectionHTTPPageLimit       = 100
	maxCollectionHTTPBodyBytes       = 64 * 1024
	maxCollectionHTTPCursorBytes     = 32 * 1024
	maxCollectionHTTPKeyBytes        = 128
	collectionHTTPUnavailable        = "COLLECTION_HTTP_UNAVAILABLE"
	collectionHTTPResultInconsistent = "COLLECTION_RESULT_INCONSISTENT"
)

// Service 是 Collection HTTP 所需的最小 Application 契约。
type Service interface {
	Create(context.Context, collectionapp.CreateCommand) (collectionapp.CommandResult, error)
	Update(context.Context, collectionapp.UpdateCommand) (collectionapp.CommandResult, error)
	Archive(context.Context, collectionapp.ArchiveCommand) (collectionapp.CommandResult, error)
	Get(context.Context, foundation.ID, foundation.ID) (collectionapp.Collection, error)
	List(context.Context, collectionapp.ListQuery) (collectionapp.CollectionListPage, error)
	Results(context.Context, collectionapp.ResultsQuery) (collectionapp.ResultPage, error)
	Preview(context.Context, collectionapp.PreviewQuery) (collectionapp.ResultPage, error)
}

// Handler 将 Collection Application 映射为严格 JSON、Workspace 隔离的 HTTP 契约。
type Handler struct {
	service Service
	timeout time.Duration
}

// NewHandler 创建 Collection HTTP Handler；缺少 Service 时保存路由但 fail closed。
func NewHandler(service Service, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Handler{service: service, timeout: timeout}
}

// Routes 在 /api/v1 下注册 Collection 生命周期、校验和结果路由。
func (handler *Handler) Routes(router chi.Router) {
	router.Get("/collections", handler.list)
	router.Post("/collections", handler.create)
	router.Post("/collections/validate", handler.validate)
	router.Post("/collections/preview", handler.preview)
	router.Get("/collections/{collection_id}", handler.get)
	router.Put("/collections/{collection_id}", handler.update)
	router.Post("/collections/{collection_id}/archive", handler.archive)
	router.Get("/collections/{collection_id}/results", handler.results)
}

// Available 报告生命周期/结果 Handler 是否持有真实 Application Service。
func (handler *Handler) Available() bool {
	return handler != nil && !nilDependency(handler.service)
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

func (handler *Handler) list(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	query, err := parseQuery(r, "workspace_id", "status", "limit", "cursor")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(singleQuery(query, "workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := parseLimit(singleQueryDefault(query, "limit", strconv.Itoa(defaultCollectionListLimit)))
	if err != nil {
		writeError(w, err)
		return
	}
	statuses, err := parseStatuses(query["status"])
	if err != nil {
		writeError(w, err)
		return
	}
	cursor := singleQuery(query, "cursor")
	if cursor != "" && !validCursor(cursor, false) {
		writeError(w, foundation.NewError(foundation.ErrorInvalidInput, collectionapp.ErrorCodeCursorInvalid, false, errors.New("collection list cursor is invalid")))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.List(ctx, collectionapp.ListQuery{WorkspaceID: workspaceID, Statuses: statuses, Limit: limit, Cursor: cursor})
	if err != nil {
		writeError(w, err)
		return
	}
	if len(page.Items) > limit {
		writeError(w, resultInvalid("collection list exceeded requested limit"))
		return
	}
	for _, collection := range page.Items {
		if err := validateCollectionProjection(collection, workspaceID, ""); err != nil {
			writeError(w, err)
			return
		}
	}
	items := make([]collectionResponse, 0, len(page.Items))
	for _, collection := range page.Items {
		items = append(items, toCollectionResponse(collection))
	}
	response := collectionListResponse{WorkspaceID: string(workspaceID), Items: items}
	if page.NextCursor != "" {
		response.NextCursor = &page.NextCursor
	}
	httpapi.WriteJSON(w, http.StatusOK, response)
}

func (handler *Handler) create(w http.ResponseWriter, r *http.Request) {
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeDefinitionRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateDefinition(wire.Query, wire.ViewType, wire.ViewConfig); err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.Create(ctx, collectionapp.CreateCommand{
		WorkspaceID: workspaceID, Name: wire.Name, Description: wire.Description,
		Query: wire.Query, ViewType: domain.ViewType(wire.ViewType), ViewConfig: wire.ViewConfig,
		IdempotencyKey: key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateCommandProjection(result, workspaceID, "", "CREATE"); err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, toCollectionResponse(result.Collection))
}

func (handler *Handler) validate(w http.ResponseWriter, r *http.Request) {
	wire, err := decodeValidationRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	canonicalQuery, err := domain.CanonicalizeQuery(wire.Query)
	if err != nil {
		writeError(w, err)
		return
	}
	canonicalView, err := domain.CanonicalizeViewConfig(domain.ViewType(wire.ViewType), wire.ViewConfig)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, collectionValidationResponse{
		WorkspaceID: string(workspaceID), Valid: true, Query: canonicalQuery.Definition,
		QueryHash: canonicalQuery.Hash, ViewType: canonicalViewType(wire.ViewType), ViewConfig: canonicalView,
	})
}

func (handler *Handler) preview(w http.ResponseWriter, r *http.Request) {
	wire, err := decodePreviewRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validatePreviewDefinition(wire); err != nil {
		writeError(w, err)
		return
	}
	if wire.Limit != nil {
		if _, err := parseLimit(strconv.Itoa(*wire.Limit)); err != nil {
			writeError(w, err)
			return
		}
	}
	if wire.Cursor != nil && !validCursor(*wire.Cursor, false) {
		writeError(w, requestInvalid("collection preview cursor is invalid"))
		return
	}
	if !handler.available(w) {
		return
	}
	limit := defaultCollectionResultLimit
	if wire.Limit != nil {
		limit = *wire.Limit
	}
	cursor := ""
	if wire.Cursor != nil {
		cursor = *wire.Cursor
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.Preview(ctx, collectionapp.PreviewQuery{WorkspaceID: workspaceID, Query: wire.Query, Limit: limit, Cursor: cursor})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateResultProjection(page, limit); err != nil {
		writeError(w, err)
		return
	}
	canonical, err := domain.CanonicalizeQuery(wire.Query)
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]collectionItemResponse, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, toCollectionItemResponse(item))
	}
	httpapi.WriteJSON(w, http.StatusOK, collectionPreviewResponse{
		WorkspaceID: string(workspaceID), QueryHash: canonical.Hash, Items: items,
		ExactCount: page.ExactCount, NextCursor: optionalCursor(page.NextCursor), RevisionHash: page.RevisionHash,
	})
}

func (handler *Handler) get(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	query, err := parseQuery(r, "workspace_id")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(singleQuery(query, "workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	collectionID, err := parseID(chi.URLParam(r, "collection_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	collection, err := handler.service.Get(ctx, workspaceID, collectionID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateCollectionProjection(collection, workspaceID, collectionID); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toCollectionResponse(collection))
}

func (handler *Handler) update(w http.ResponseWriter, r *http.Request) {
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeVersionedDefinitionRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	collectionID, err := parseID(chi.URLParam(r, "collection_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if wire.ExpectedVersion < 1 {
		writeError(w, requestInvalid("expected version is invalid"))
		return
	}
	if err := validateDefinition(wire.Query, wire.ViewType, wire.ViewConfig); err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.Update(ctx, collectionapp.UpdateCommand{
		WorkspaceID: workspaceID, CollectionID: collectionID, ExpectedVersion: wire.ExpectedVersion,
		Name: wire.Name, Description: wire.Description, Query: wire.Query,
		ViewType: domain.ViewType(wire.ViewType), ViewConfig: wire.ViewConfig, IdempotencyKey: key,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateCommandProjection(result, workspaceID, collectionID, "UPDATE"); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toCollectionResponse(result.Collection))
}

func (handler *Handler) archive(w http.ResponseWriter, r *http.Request) {
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeArchiveRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	collectionID, err := parseID(chi.URLParam(r, "collection_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if wire.ExpectedVersion < 1 {
		writeError(w, requestInvalid("expected version is invalid"))
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.Archive(ctx, collectionapp.ArchiveCommand{WorkspaceID: workspaceID, CollectionID: collectionID, ExpectedVersion: wire.ExpectedVersion, IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateCommandProjection(result, workspaceID, collectionID, "ARCHIVE"); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toCollectionResponse(result.Collection))
}

func (handler *Handler) results(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	query, err := parseQuery(r, "workspace_id", "limit", "cursor")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(singleQuery(query, "workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	collectionID, err := parseID(chi.URLParam(r, "collection_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := parseLimit(singleQueryDefault(query, "limit", strconv.Itoa(defaultCollectionResultLimit)))
	if err != nil {
		writeError(w, err)
		return
	}
	cursor := singleQuery(query, "cursor")
	if !validCursor(cursor, true) {
		writeError(w, requestInvalid("collection result cursor is invalid"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: workspaceID, CollectionID: collectionID, Limit: limit, Cursor: cursor})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateResultProjection(page, limit); err != nil {
		writeError(w, err)
		return
	}
	items := make([]collectionItemResponse, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, toCollectionItemResponse(item))
	}
	response := collectionResultResponse{WorkspaceID: string(workspaceID), CollectionID: string(collectionID), QueryHash: page.QueryHash, Items: items, ExactCount: page.ExactCount, RevisionHash: page.RevisionHash}
	if page.NextCursor != "" {
		response.NextCursor = &page.NextCursor
	}
	httpapi.WriteJSON(w, http.StatusOK, response)
}

type definitionRequest struct {
	WorkspaceID string            `json:"workspace_id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Query       domain.Query      `json:"query"`
	ViewType    string            `json:"view_type"`
	ViewConfig  domain.ViewConfig `json:"view_config"`
}

type validationRequest struct {
	WorkspaceID string            `json:"workspace_id"`
	Query       domain.Query      `json:"query"`
	ViewType    string            `json:"view_type"`
	ViewConfig  domain.ViewConfig `json:"view_config"`
}

type versionedDefinitionRequest struct {
	definitionRequest
	ExpectedVersion int64 `json:"expected_version"`
}

type archiveRequest struct {
	WorkspaceID     string `json:"workspace_id"`
	ExpectedVersion int64  `json:"expected_version"`
}

type previewRequest struct {
	validationRequest
	Limit  *int    `json:"limit,omitempty"`
	Cursor *string `json:"cursor,omitempty"`
}

type collectionListResponse struct {
	WorkspaceID string               `json:"workspace_id"`
	Items       []collectionResponse `json:"items"`
	NextCursor  *string              `json:"next_cursor,omitempty"`
}

type collectionResponse struct {
	ID                  string            `json:"id"`
	WorkspaceID         string            `json:"workspace_id"`
	Name                string            `json:"name"`
	Description         string            `json:"description"`
	QuerySchemaVersion  string            `json:"query_schema_version"`
	QueryVersion        int64             `json:"query_version"`
	Query               domain.Query      `json:"query"`
	QueryHash           string            `json:"query_hash"`
	ViewType            string            `json:"view_type"`
	ViewConfig          domain.ViewConfig `json:"view_config"`
	Status              string            `json:"status"`
	CachedResultVersion *string           `json:"cached_result_version,omitempty"`
	LastExecutedAt      *string           `json:"last_executed_at,omitempty"`
	Version             int64             `json:"version"`
	CreatedAt           string            `json:"created_at"`
	UpdatedAt           string            `json:"updated_at"`
}

type collectionValidationResponse struct {
	WorkspaceID string            `json:"workspace_id"`
	Valid       bool              `json:"valid"`
	Query       domain.Query      `json:"query"`
	QueryHash   string            `json:"query_hash"`
	ViewType    string            `json:"view_type"`
	ViewConfig  domain.ViewConfig `json:"view_config"`
}

type collectionResultResponse struct {
	WorkspaceID  string                   `json:"workspace_id"`
	CollectionID string                   `json:"collection_id"`
	QueryHash    string                   `json:"query_hash"`
	Items        []collectionItemResponse `json:"items"`
	ExactCount   int64                    `json:"exact_count"`
	NextCursor   *string                  `json:"next_cursor,omitempty"`
	RevisionHash string                   `json:"revision_hash"`
}

type collectionPreviewResponse struct {
	WorkspaceID  string                   `json:"workspace_id"`
	QueryHash    string                   `json:"query_hash"`
	Items        []collectionItemResponse `json:"items"`
	ExactCount   int64                    `json:"exact_count"`
	NextCursor   *string                  `json:"next_cursor,omitempty"`
	RevisionHash string                   `json:"revision_hash"`
}

type collectionItemResponse struct {
	ObjectType                 string                                  `json:"object_type"`
	ID                         string                                  `json:"id"`
	TopicID                    *string                                 `json:"topic_id,omitempty"`
	Title                      string                                  `json:"title"`
	Summary                    string                                  `json:"summary"`
	Status                     string                                  `json:"status"`
	Confidence                 *float64                                `json:"confidence,omitempty"`
	Applicability              json.RawMessage                         `json:"applicability,omitempty"`
	ApplicabilitySchemaVersion string                                  `json:"applicability_schema_version,omitempty"`
	ApplicabilityHash          string                                  `json:"applicability_hash,omitempty"`
	Aliases                    []string                                `json:"aliases,omitempty"`
	SourceSummaries            []collectionapp.CollectionSourceSummary `json:"source_summaries,omitempty"`
	RelationTypes              []string                                `json:"relation_types,omitempty"`
	HealthSummary              *collectionapp.CollectionHealthSummary  `json:"health_summary,omitempty"`
	RelationType               *string                                 `json:"relation_type,omitempty"`
	HealthType                 *string                                 `json:"health_issue_type,omitempty"`
	SourceType                 *string                                 `json:"source_type,omitempty"`
	FilePath                   *string                                 `json:"file_path,omitempty"`
	CreatedAt                  string                                  `json:"created_at"`
	UpdatedAt                  string                                  `json:"updated_at"`
}

func decodeDefinitionRequest(r *http.Request) (definitionRequest, error) {
	return decodeJSON[definitionRequest](r)
}

func decodeVersionedDefinitionRequest(r *http.Request) (versionedDefinitionRequest, error) {
	return decodeJSON[versionedDefinitionRequest](r)
}

func decodeValidationRequest(r *http.Request) (validationRequest, error) {
	return decodeJSON[validationRequest](r)
}

func decodeArchiveRequest(r *http.Request) (archiveRequest, error) {
	return decodeJSON[archiveRequest](r)
}

func decodePreviewRequest(r *http.Request) (previewRequest, error) {
	return decodeJSON[previewRequest](r)
}

func decodeJSON[T any](r *http.Request) (T, error) {
	var zero T
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("request requires application/json"))
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCollectionHTTPBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxCollectionHTTPBodyBytes {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, errors.New("request JSON is empty or oversized"))
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxCollectionHTTPBodyBytes
	decoded, err := strictjson.DecodeObject[T](body, limits, nil)
	if err != nil {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return decoded, nil
}

func parseIdempotencyKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || !utf8.ValidString(values[0]) {
		return "", requestInvalid("exactly one Idempotency-Key is required")
	}
	value := values[0]
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxCollectionHTTPKeyBytes {
		return "", requestInvalid("Idempotency-Key is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", requestInvalid("Idempotency-Key is invalid")
		}
	}
	return value, nil
}

func parseQuery(r *http.Request, allowed ...string) (url.Values, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, requestInvalid("query parameters are invalid")
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := allowedSet[key]; !ok || len(entries) == 0 {
			return nil, requestInvalid("query parameter is not allowed")
		}
		switch key {
		case "status":
			for _, entry := range entries {
				if strings.TrimSpace(entry) == "" {
					return nil, requestInvalid("query parameter is empty")
				}
			}
		default:
			if len(entries) != 1 || strings.TrimSpace(entries[0]) == "" {
				return nil, requestInvalid("query parameter must occur exactly once")
			}
		}
	}
	return values, nil
}

func parseStatuses(values []string) ([]collectionapp.CollectionStatus, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make([]collectionapp.CollectionStatus, 0, len(values))
	seen := make(map[collectionapp.CollectionStatus]struct{}, len(values))
	for _, raw := range values {
		status := collectionapp.CollectionStatus(raw)
		if status != collectionapp.CollectionStatusActive && status != collectionapp.CollectionStatusArchived {
			return nil, requestInvalid("collection status is invalid")
		}
		if _, ok := seen[status]; ok {
			return nil, requestInvalid("collection status is duplicated")
		}
		seen[status] = struct{}{}
		result = append(result, status)
	}
	return result, nil
}

func parseLimit(raw string) (int, error) {
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maxCollectionHTTPPageLimit {
		return 0, requestInvalid("collection limit is invalid")
	}
	return limit, nil
}

func parseID(raw string) (foundation.ID, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return "", requestInvalid("collection UUID is invalid")
	}
	parsed, err := foundation.ParseID(raw)
	if err != nil || string(parsed) != raw {
		return "", requestInvalid("collection UUID is invalid")
	}
	return parsed, nil
}

func singleQuery(values url.Values, key string) string {
	entries := values[key]
	if len(entries) == 1 {
		return entries[0]
	}
	return ""
}

func singleQueryDefault(values url.Values, key, fallback string) string {
	if value := singleQuery(values, key); value != "" {
		return value
	}
	return fallback
}

func validatePreviewDefinition(wire previewRequest) error {
	return validateDefinition(wire.Query, wire.ViewType, wire.ViewConfig)
}

func validCursor(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	if len(value) > maxCollectionHTTPCursorBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

func validateDefinition(query domain.Query, viewType string, viewConfig domain.ViewConfig) error {
	if _, err := domain.CanonicalizeQuery(query); err != nil {
		return err
	}
	_, err := domain.CanonicalizeViewConfig(domain.ViewType(viewType), viewConfig)
	return err
}

func canonicalViewType(raw string) string {
	return string(domain.ViewType(strings.ToUpper(strings.TrimSpace(raw))))
}

func validateCommandProjection(result collectionapp.CommandResult, workspaceID, collectionID foundation.ID, commandType string) error {
	if result.Collection.WorkspaceID != workspaceID || (collectionID != "" && result.Collection.ID != collectionID) || result.CommandType != commandType || result.CommandVersion < 1 || result.Collection.Version != result.CommandVersion || !validID(result.Collection.ID) {
		return resultInvalid("collection command response crossed its request binding")
	}
	return validateCollectionProjection(result.Collection, workspaceID, result.Collection.ID)
}

func validateCollectionProjection(collection collectionapp.Collection, workspaceID, collectionID foundation.ID) error {
	if collection.WorkspaceID != workspaceID || (collectionID != "" && collection.ID != collectionID) || !validID(collection.ID) || collection.Name == "" || collection.Version < 1 || collection.QueryVersion < 1 || collection.QuerySchemaVersion != domain.QuerySchemaVersionV1 || collection.CreatedAt.IsZero() || collection.UpdatedAt.IsZero() {
		return resultInvalid("collection response crossed workspace or version binding")
	}
	if collection.Status != collectionapp.CollectionStatusActive && collection.Status != collectionapp.CollectionStatusArchived {
		return resultInvalid("collection response status is invalid")
	}
	canonicalQuery, err := domain.CanonicalizeQuery(collection.Query)
	if err != nil || canonicalQuery.Hash != collection.QueryHash || !reflect.DeepEqual(canonicalQuery.Definition, collection.Query) {
		return resultInvalid("collection response query is invalid")
	}
	canonicalView, err := domain.CanonicalizeViewConfig(collection.ViewType, collection.ViewConfig)
	if err != nil || !reflect.DeepEqual(canonicalView, collection.ViewConfig) || canonicalViewType(string(collection.ViewType)) != string(collection.ViewType) {
		return resultInvalid("collection response view is invalid")
	}
	return nil
}

func validateResultProjection(page collectionapp.ResultPage, limit int) error {
	if page.ExactCount < int64(len(page.Items)) || len(page.Items) > limit || !validCursor(page.NextCursor, true) || len(page.RevisionHash) != 64 {
		return resultInvalid("collection result page metadata is invalid")
	}
	if _, err := hex.DecodeString(page.RevisionHash); err != nil {
		return resultInvalid("collection result revision is invalid")
	}
	for _, item := range page.Items {
		if !validID(item.ID) || (item.ObjectType != "TOPIC" && item.ObjectType != "CLAIM") || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() || item.Confidence != nil && (*item.Confidence < 0 || *item.Confidence > 1) {
			return resultInvalid("collection result item is invalid")
		}
		if item.TopicID != nil && !validID(*item.TopicID) {
			return resultInvalid("collection result topic reference is invalid")
		}
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func toCollectionResponse(value collectionapp.Collection) collectionResponse {
	return collectionResponse{
		ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), Name: value.Name, Description: value.Description,
		QuerySchemaVersion: value.QuerySchemaVersion, QueryVersion: value.QueryVersion, Query: value.Query, QueryHash: value.QueryHash,
		ViewType: string(value.ViewType), ViewConfig: value.ViewConfig, Status: string(value.Status), CachedResultVersion: cloneString(value.CachedResultVersion),
		LastExecutedAt: formatOptionalTime(value.LastExecutedAt), Version: value.Version, CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt),
	}
}

func toCollectionItemResponse(value collectionapp.CollectionItem) collectionItemResponse {
	return collectionItemResponse{ObjectType: value.ObjectType, ID: string(value.ID), TopicID: cloneID(value.TopicID), Title: value.Title, Summary: value.Summary, Status: value.Status, Confidence: value.Confidence,
		Applicability: append(json.RawMessage(nil), value.Applicability...), ApplicabilitySchemaVersion: value.ApplicabilitySchemaVersion, ApplicabilityHash: value.ApplicabilityHash,
		Aliases: append([]string(nil), value.Aliases...), SourceSummaries: append([]collectionapp.CollectionSourceSummary(nil), value.SourceSummaries...), RelationTypes: append([]string(nil), value.RelationTypes...), HealthSummary: cloneHealthSummary(value.HealthSummary),
		RelationType: cloneString(value.RelationType), HealthType: cloneString(value.HealthType), SourceType: cloneString(value.SourceType), FilePath: cloneString(value.FilePath), CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt)}
}

func cloneHealthSummary(value *collectionapp.CollectionHealthSummary) *collectionapp.CollectionHealthSummary {
	if value == nil {
		return nil
	}
	clone := *value
	clone.IssueTypes = append([]string(nil), value.IssueTypes...)
	return &clone
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func optionalCursor(value string) *string {
	if value == "" {
		return nil
	}
	result := value
	return &result
}

func cloneID(value *foundation.ID) *string {
	if value == nil {
		return nil
	}
	result := string(*value)
	return &result
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	result := formatTime(*value)
	return &result
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func requestInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, collectionapp.ErrorCodeRequestInvalid, false, errors.New(message))
}

func resultInvalid(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, collectionHTTPResultInconsistent, false, errors.New(message))
}

func (handler *Handler) available(w http.ResponseWriter) bool {
	if !handler.Available() {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, collectionHTTPUnavailable, "Collection 服务暂不可用", true, nil)
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, collectionapp.ErrorCodeDependencyUnavailable, "Collection 请求超时", true, nil)
		return
	}
	if errors.Is(err, context.Canceled) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, collectionapp.ErrorCodeDependencyUnavailable, "Collection 请求已取消", false, nil)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Collection 请求失败", false, nil)
		return
	}
	status := httpapi.StatusForErrorKind(classified.Kind)
	code := classified.Code
	message := "Collection 请求未完成"
	switch classified.Code {
	case "UNSUPPORTED_MEDIA_TYPE":
		status, message = http.StatusUnsupportedMediaType, "请求必须使用 application/json"
	case "INVALID_JSON":
		status, message = http.StatusBadRequest, "请求 JSON 无效"
	case collectionHTTPResultInconsistent:
		status, message = http.StatusInternalServerError, "Collection 响应校验失败"
	case collectionapp.ErrorCodeNotFound:
		status, message = http.StatusNotFound, "请求的 Collection 不存在"
	case collectionapp.ErrorCodeDependencyUnavailable:
		status, message = http.StatusServiceUnavailable, "Collection 依赖暂不可用"
	case collectionapp.ErrorCodeVersionConflict, collectionapp.ErrorCodeIdempotencyConflict, collectionapp.ErrorCodeArchivedImmutable, "COLLECTION_NAME_CONFLICT", "COLLECTION_CURSOR_STALE":
		status, message = http.StatusConflict, "Collection 版本或游标冲突"
	case collectionapp.ErrorCodeRequestInvalid, domain.ErrorCodeQueryInvalid, domain.ErrorCodeFieldUnknown, domain.ErrorCodeFieldUnavailable, domain.ErrorCodeSortInvalid, domain.ErrorCodeViewConfigInvalid:
		status, message = http.StatusBadRequest, "Collection 请求参数无效"
	}
	httpapi.WriteProblem(w, status, code, message, classified.Retryable, nil)
}

var _ Service = (*collectionapp.Service)(nil)
