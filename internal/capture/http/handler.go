// Package capturehttp exposes Quick Capture commands and read models over HTTP.
package capturehttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/go-chi/chi/v5"
)

const (
	defaultListLimit     = 30
	maxListLimit         = 100
	maxJSONBodyBytes     = 2 * 1024 * 1024
	maxMultipartOverhead = 1024 * 1024
	cursorVersion        = 1
	cursorKind           = "capture-list"
)

// Service is the minimum Capture application contract exposed by HTTP.
type Service interface {
	CreateText(context.Context, captureapp.TextCommand) (captureapp.CreateResult, error)
	CreateURL(context.Context, captureapp.URLCommand) (captureapp.CreateResult, error)
	CreateUpload(context.Context, captureapp.UploadCommand) (captureapp.CreateResult, error)
	Retry(context.Context, captureapp.RetryCommand) (captureapp.RetryResult, error)
	Get(context.Context, foundation.ID, foundation.ID) (domain.Capture, error)
	List(context.Context, captureapp.ListQuery) (captureapp.Page, error)
}

// Handler maps strict Workspace-bound requests to the Capture application service.
type Handler struct {
	service        Service
	profiles       captureapp.ProfileReader
	profileRetrier captureapp.ProfileRetrier
	timeout        time.Duration
}

// NewHandler creates a fail-closed Capture HTTP handler.
func NewHandler(service Service, timeout time.Duration) *Handler {
	return NewHandlerWithProfiles(service, nil, nil, timeout)
}

// NewHandlerWithProfiles creates a Capture handler with optional document knowledge profile reads and retries.
func NewHandlerWithProfiles(service Service, profiles captureapp.ProfileReader, retrier captureapp.ProfileRetrier, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Handler{service: service, profiles: profiles, profileRetrier: retrier, timeout: timeout}
}

// Routes registers Capture commands and read models under /api/v1.
func (handler *Handler) Routes(router chi.Router) {
	router.Post("/workspaces/{workspaceID}/captures", handler.createJSON)
	router.Post("/workspaces/{workspaceID}/capture-files", handler.createUpload)
	router.Get("/workspaces/{workspaceID}/captures", handler.list)
	router.Get("/workspaces/{workspaceID}/captures/{captureID}", handler.get)
	router.Post("/workspaces/{workspaceID}/captures/{captureID}/retry", handler.retry)
	router.Get("/workspaces/{workspaceID}/source-versions/{sourceVersionID}/knowledge-profile", handler.getProfile)
	router.Post("/workspaces/{workspaceID}/source-versions/{sourceVersionID}/knowledge-profile/retry", handler.retryProfile)
}

// Available reports whether the handler owns a real application service.
func (handler *Handler) Available() bool {
	return handler != nil && !nilDependency(handler.service)
}

type createRequest struct {
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name,omitempty"`
	Text        string `json:"text,omitempty"`
	URL         string `json:"url,omitempty"`
}

type retryRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

type profileResponse struct {
	ID                string `json:"id"`
	WorkspaceID       string `json:"workspace_id"`
	CaptureID         string `json:"capture_id"`
	SourceVersionID   string `json:"source_version_id"`
	CurrentRevisionID string `json:"current_revision_id,omitempty"`
	Status            string `json:"status"`
	ErrorCode         string `json:"error_code,omitempty"`
	Retryable         bool   `json:"retryable"`
	Version           int64  `json:"version"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type profileRevisionResponse struct {
	ID                    string                `json:"id"`
	ProfileID             string                `json:"profile_id"`
	WorkspaceID           string                `json:"workspace_id"`
	SourceVersionID       string                `json:"source_version_id"`
	ParseProjectionID     string                `json:"parse_projection_id"`
	IndexVersionID        string                `json:"index_version_id"`
	ModelRunID            string                `json:"model_run_id"`
	ModelSettingsRevision *int64                `json:"model_settings_revision"`
	PromptVersion         string                `json:"prompt_version"`
	SchemaVersion         string                `json:"schema_version"`
	Content               domain.ProfileContent `json:"content"`
	ContentDigest         string                `json:"content_digest"`
	CreatedAt             string                `json:"created_at"`
}

type profileViewResponse struct {
	Profile  profileResponse          `json:"profile"`
	Revision *profileRevisionResponse `json:"revision"`
}

type profileCommandResponse struct {
	Profile  profileResponse          `json:"profile"`
	Revision *profileRevisionResponse `json:"revision"`
	Replayed bool                     `json:"replayed"`
}

type captureResponse struct {
	ID                    string `json:"id"`
	WorkspaceID           string `json:"workspace_id"`
	Kind                  string `json:"kind"`
	DisplayName           string `json:"display_name"`
	OriginalURL           string `json:"original_url,omitempty"`
	SourceID              string `json:"source_id"`
	LatestSourceVersionID string `json:"latest_source_version_id,omitempty"`
	Status                string `json:"status"`
	FetchStatus           string `json:"fetch_status"`
	IngestionStatus       string `json:"ingestion_status"`
	IndexStatus           string `json:"index_status"`
	ProfileStatus         string `json:"profile_status"`
	FailureStage          string `json:"failure_stage,omitempty"`
	ErrorCode             string `json:"error_code,omitempty"`
	Retryable             bool   `json:"retryable"`
	Version               int64  `json:"version"`
	CapturedAt            string `json:"captured_at"`
	UpdatedAt             string `json:"updated_at"`
	DetailHref            string `json:"detail_href"`
	ProfileHref           string `json:"profile_href,omitempty"`
}

type createResponse struct {
	Capture  captureResponse `json:"capture"`
	Replayed bool            `json:"replayed"`
}

type listResponse struct {
	WorkspaceID string            `json:"workspace_id"`
	Items       []captureResponse `json:"items"`
	NextCursor  string            `json:"next_cursor,omitempty"`
}

type listCursor struct {
	Version     int    `json:"version"`
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id"`
	Filter      string `json:"filter"`
	CapturedAt  string `json:"captured_at"`
	ID          string `json:"id"`
}

func (handler *Handler) createJSON(writer http.ResponseWriter, request *http.Request) {
	workspaceID, err := parseID(chi.URLParam(request, "workspaceID"))
	if err != nil {
		writeError(writer, err)
		return
	}
	key, err := parseIdempotencyKey(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	wire, err := decodeJSON(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	var result captureapp.CreateResult
	switch wire.Kind {
	case "TEXT":
		if wire.URL != "" {
			writeError(writer, invalid("CAPTURE_REQUEST_INVALID", "TEXT capture cannot contain url"))
			return
		}
		result, err = handler.service.CreateText(ctx, captureapp.TextCommand{
			WorkspaceID: workspaceID, DisplayName: wire.DisplayName, Text: wire.Text, IdempotencyKey: key,
		})
	case "URL":
		if wire.Text != "" {
			writeError(writer, invalid("CAPTURE_REQUEST_INVALID", "URL capture cannot contain text"))
			return
		}
		result, err = handler.service.CreateURL(ctx, captureapp.URLCommand{
			WorkspaceID: workspaceID, DisplayName: wire.DisplayName, URL: wire.URL, IdempotencyKey: key,
		})
	default:
		writeError(writer, invalid("CAPTURE_KIND_INVALID", "JSON capture kind must be TEXT or URL"))
		return
	}
	if err != nil {
		writeError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, createResponse{Capture: toCaptureResponse(result.Capture), Replayed: result.Replayed})
}

func (handler *Handler) createUpload(writer http.ResponseWriter, request *http.Request) {
	workspaceID, err := parseID(chi.URLParam(request, "workspaceID"))
	if err != nil {
		writeError(writer, err)
		return
	}
	key, err := parseIdempotencyKey(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		writeError(writer, invalid("UNSUPPORTED_MEDIA_TYPE", "request requires multipart/form-data"))
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, workspacedomain.MaxCommittedSourceBytes+maxMultipartOverhead)
	if err := request.ParseMultipartForm(maxMultipartOverhead); err != nil {
		writeError(writer, invalid("CAPTURE_UPLOAD_INVALID", "multipart upload is invalid or oversized"))
		return
	}
	if request.MultipartForm != nil {
		defer func() { _ = request.MultipartForm.RemoveAll() }()
	}
	if err := validateMultipartShape(request); err != nil {
		writeError(writer, err)
		return
	}
	kind := domain.Kind(singleMultipartValue(request, "kind"))
	if kind != domain.KindFile && kind != domain.KindImage {
		writeError(writer, invalid("CAPTURE_KIND_INVALID", "upload kind must be FILE or IMAGE"))
		return
	}
	fileHeaders := request.MultipartForm.File["file"]
	file, err := fileHeaders[0].Open()
	if err != nil {
		writeError(writer, unavailable("CAPTURE_UPLOAD_READ_FAILED", "uploaded content could not be opened"))
		return
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, workspacedomain.MaxCommittedSourceBytes+1))
	if err != nil {
		writeError(writer, unavailable("CAPTURE_UPLOAD_READ_FAILED", "uploaded content could not be read"))
		return
	}
	if len(content) == 0 || int64(len(content)) > workspacedomain.MaxCommittedSourceBytes {
		writeError(writer, invalid("CAPTURE_CONTENT_SIZE_INVALID", "uploaded content is empty or oversized"))
		return
	}
	sniffed, err := sniffMediaType(kind, fileHeaders[0].Filename, content)
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.CreateUpload(ctx, captureapp.UploadCommand{
		WorkspaceID: workspaceID, Kind: kind, DisplayName: singleMultipartValue(request, "display_name"),
		FileName: fileHeaders[0].Filename, MediaType: sniffed, Content: content, IdempotencyKey: key,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, createResponse{Capture: toCaptureResponse(result.Capture), Replayed: result.Replayed})
}

func (handler *Handler) get(writer http.ResponseWriter, request *http.Request) {
	workspaceID, err := parseID(chi.URLParam(request, "workspaceID"))
	if err != nil {
		writeError(writer, err)
		return
	}
	captureID, err := parseID(chi.URLParam(request, "captureID"))
	if err != nil {
		writeError(writer, err)
		return
	}
	if len(request.URL.Query()) != 0 {
		writeError(writer, invalid("CAPTURE_QUERY_INVALID", "capture detail does not accept query parameters"))
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	capture, err := handler.service.Get(ctx, workspaceID, captureID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, toCaptureResponse(capture))
}

func (handler *Handler) retry(writer http.ResponseWriter, request *http.Request) {
	workspaceID, err := parseID(chi.URLParam(request, "workspaceID"))
	if err != nil {
		writeError(writer, err)
		return
	}
	captureID, err := parseID(chi.URLParam(request, "captureID"))
	if err != nil {
		writeError(writer, err)
		return
	}
	key, err := parseIdempotencyKey(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	wire, err := decodeRetryJSON(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	if wire.ExpectedVersion < 1 {
		writeError(writer, invalid("CAPTURE_RETRY_INVALID", "expected_version must be positive"))
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.Retry(ctx, captureapp.RetryCommand{
		WorkspaceID: workspaceID, CaptureID: captureID,
		ExpectedVersion: wire.ExpectedVersion, IdempotencyKey: key,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, createResponse{Capture: toCaptureResponse(result.Capture), Replayed: result.Replayed})
}

func (handler *Handler) getProfile(writer http.ResponseWriter, request *http.Request) {
	workspaceID, sourceVersionID, err := profileRouteIDs(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	if len(request.URL.Query()) != 0 {
		writeError(writer, invalid("CAPTURE_PROFILE_QUERY_INVALID", "knowledge profile detail does not accept query parameters"))
		return
	}
	if !handler.profilesAvailable(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	view, err := handler.profiles.GetProfile(ctx, captureapp.ProfileQuery{
		WorkspaceID: workspaceID, SourceVersionID: sourceVersionID,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toProfileViewResponse(view, workspaceID, sourceVersionID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) retryProfile(writer http.ResponseWriter, request *http.Request) {
	workspaceID, sourceVersionID, err := profileRouteIDs(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	key, err := parseIdempotencyKey(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	wire, err := decodeRetryJSON(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	if wire.ExpectedVersion < 1 {
		writeError(writer, invalid("CAPTURE_PROFILE_RETRY_INVALID", "expected_version must be positive"))
		return
	}
	if !handler.profileRetrierAvailable(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.profileRetrier.RetryProfile(ctx, captureapp.ProfileRetryCommand{
		WorkspaceID: workspaceID, SourceVersionID: sourceVersionID,
		ExpectedVersion: wire.ExpectedVersion, IdempotencyKey: key,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toProfileViewResponse(result.View, workspaceID, sourceVersionID)
	if err != nil {
		writeError(writer, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, profileCommandResponse{
		Profile: response.Profile, Revision: response.Revision, Replayed: result.Replayed,
	})
}

func profileRouteIDs(request *http.Request) (foundation.ID, foundation.ID, error) {
	workspaceID, err := parseID(chi.URLParam(request, "workspaceID"))
	if err != nil {
		return "", "", err
	}
	sourceVersionID, err := parseID(chi.URLParam(request, "sourceVersionID"))
	if err != nil {
		return "", "", err
	}
	return workspaceID, sourceVersionID, nil
}

func (handler *Handler) list(writer http.ResponseWriter, request *http.Request) {
	workspaceID, err := parseID(chi.URLParam(request, "workspaceID"))
	if err != nil {
		writeError(writer, err)
		return
	}
	values, err := parseListQuery(request.URL.RawQuery)
	if err != nil {
		writeError(writer, err)
		return
	}
	limit := defaultListLimit
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxListLimit {
			writeError(writer, invalid("CAPTURE_LIST_INVALID", "capture list limit is invalid"))
			return
		}
	}
	kind := domain.Kind(values.Get("kind"))
	if kind != "" && !kind.Valid() {
		writeError(writer, invalid("CAPTURE_LIST_INVALID", "capture kind filter is invalid"))
		return
	}
	status := domain.Status(values.Get("status"))
	if status != "" && !status.Valid() {
		writeError(writer, invalid("CAPTURE_LIST_INVALID", "capture status filter is invalid"))
		return
	}
	filter, _ := json.Marshal(struct {
		Kind   domain.Kind   `json:"kind,omitempty"`
		Status domain.Status `json:"status,omitempty"`
		Limit  int           `json:"limit"`
	}{Kind: kind, Status: status, Limit: limit})
	position, err := decodeCursor(values.Get("cursor"), workspaceID, string(filter))
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.List(ctx, captureapp.ListQuery{
		WorkspaceID: workspaceID, Kind: kind, Status: status, After: position, Limit: limit,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	response := listResponse{WorkspaceID: string(workspaceID), Items: make([]captureResponse, 0, len(page.Items))}
	for _, capture := range page.Items {
		response.Items = append(response.Items, toCaptureResponse(capture))
	}
	if page.Next != nil {
		response.NextCursor, err = encodeCursor(*page.Next, workspaceID, string(filter))
		if err != nil {
			writeError(writer, err)
			return
		}
	}
	writeJSON(writer, http.StatusOK, response)
}

func decodeJSON(request *http.Request) (createRequest, error) {
	var zero createRequest
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return zero, invalid("UNSUPPORTED_MEDIA_TYPE", "request requires application/json")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxJSONBodyBytes+1))
	if err != nil || len(body) == 0 || int64(len(body)) > maxJSONBodyBytes {
		return zero, invalid("INVALID_JSON", "request JSON is empty or oversized")
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxJSONBodyBytes
	value, err := strictjson.DecodeObject[createRequest](body, limits, nil)
	if err != nil {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return value, nil
}

func decodeRetryJSON(request *http.Request) (retryRequest, error) {
	var zero retryRequest
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return zero, invalid("UNSUPPORTED_MEDIA_TYPE", "request requires application/json")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 1025))
	if err != nil || len(body) == 0 || len(body) > 1024 {
		return zero, invalid("INVALID_JSON", "request JSON is empty or oversized")
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = 1024
	value, err := strictjson.DecodeObject[retryRequest](body, limits, nil)
	if err != nil {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return value, nil
}

func validateMultipartShape(request *http.Request) error {
	if request.MultipartForm == nil {
		return invalid("CAPTURE_UPLOAD_INVALID", "multipart upload is missing")
	}
	for key, values := range request.MultipartForm.Value {
		if key != "kind" && key != "display_name" || len(values) != 1 {
			return invalid("CAPTURE_UPLOAD_INVALID", "multipart field is invalid")
		}
	}
	for key, files := range request.MultipartForm.File {
		if key != "file" || len(files) != 1 {
			return invalid("CAPTURE_UPLOAD_INVALID", "multipart file field is invalid")
		}
	}
	if len(request.MultipartForm.Value["kind"]) != 1 || len(request.MultipartForm.File["file"]) != 1 {
		return invalid("CAPTURE_UPLOAD_INVALID", "kind and one file are required")
	}
	return nil
}

func singleMultipartValue(request *http.Request, key string) string {
	values := request.MultipartForm.Value[key]
	if len(values) == 1 {
		return values[0]
	}
	return ""
}

func sniffMediaType(kind domain.Kind, name string, content []byte) (string, error) {
	detected := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(content), ";")[0]))
	if kind == domain.KindFile && detected == "text/plain" && strings.EqualFold(filepath.Ext(name), ".md") {
		detected = "text/markdown"
	}
	allowedFiles := map[string]struct{}{"text/plain": {}, "text/markdown": {}, "text/html": {}, "application/pdf": {}}
	allowedImages := map[string]struct{}{"image/png": {}, "image/jpeg": {}, "image/webp": {}, "image/gif": {}}
	allowed := allowedFiles
	if kind == domain.KindImage {
		allowed = allowedImages
	}
	if _, ok := allowed[detected]; !ok {
		return "", invalid("CAPTURE_MEDIA_TYPE_UNSUPPORTED", "server-detected media type is unsupported")
	}
	return detected, nil
}

func parseListQuery(raw string) (url.Values, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, invalid("CAPTURE_LIST_INVALID", "capture list query is invalid")
	}
	allowed := map[string]struct{}{"kind": {}, "status": {}, "limit": {}, "cursor": {}}
	for key, entries := range values {
		if _, ok := allowed[key]; !ok || len(entries) != 1 || entries[0] == "" || entries[0] != strings.TrimSpace(entries[0]) {
			return nil, invalid("CAPTURE_LIST_INVALID", "capture list query parameter is invalid")
		}
	}
	return values, nil
}

func encodeCursor(cursor captureapp.Cursor, workspaceID foundation.ID, filter string) (string, error) {
	document := listCursor{Version: cursorVersion, Kind: cursorKind, WorkspaceID: string(workspaceID), Filter: filter,
		CapturedAt: cursor.CapturedAt.UTC().Format(time.RFC3339Nano), ID: string(cursor.ID)}
	payload, err := json.Marshal(document)
	if err != nil {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "CAPTURE_CURSOR_INVALID", false, err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeCursor(raw string, workspaceID foundation.ID, filter string) (*captureapp.Cursor, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 4096 || raw != strings.TrimSpace(raw) {
		return nil, invalid("CAPTURE_CURSOR_INVALID", "capture list cursor is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, invalid("CAPTURE_CURSOR_INVALID", "capture list cursor is invalid")
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = 4096
	document, err := strictjson.DecodeObject[listCursor](payload, limits, nil)
	if err != nil || document.Version != cursorVersion || document.Kind != cursorKind ||
		document.WorkspaceID != string(workspaceID) || document.Filter != filter {
		return nil, invalid("CAPTURE_CURSOR_INVALID", "capture list cursor binding is invalid")
	}
	at, err := time.Parse(time.RFC3339Nano, document.CapturedAt)
	if err != nil {
		return nil, invalid("CAPTURE_CURSOR_INVALID", "capture list cursor time is invalid")
	}
	id, err := parseID(document.ID)
	if err != nil {
		return nil, invalid("CAPTURE_CURSOR_INVALID", "capture list cursor id is invalid")
	}
	return &captureapp.Cursor{CapturedAt: at, ID: id}, nil
}

func parseIdempotencyKey(request *http.Request) (string, error) {
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 || !utf8.ValidString(values[0]) || values[0] == "" || values[0] != strings.TrimSpace(values[0]) || len(values[0]) > 128 {
		return "", invalid("CAPTURE_IDEMPOTENCY_KEY_INVALID", "exactly one valid Idempotency-Key is required")
	}
	for _, character := range values[0] {
		if unicode.IsControl(character) {
			return "", invalid("CAPTURE_IDEMPOTENCY_KEY_INVALID", "Idempotency-Key is invalid")
		}
	}
	return values[0], nil
}

func parseID(raw string) (foundation.ID, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return "", invalid("CAPTURE_ID_INVALID", "capture UUID is invalid")
	}
	id, err := foundation.ParseID(raw)
	if err != nil || string(id) != raw {
		return "", invalid("CAPTURE_ID_INVALID", "capture UUID is invalid")
	}
	return id, nil
}

func toCaptureResponse(capture domain.Capture) captureResponse {
	base := "/api/v1/workspaces/" + string(capture.WorkspaceID) + "/captures/" + string(capture.ID)
	response := captureResponse{
		ID: string(capture.ID), WorkspaceID: string(capture.WorkspaceID), Kind: string(capture.Kind), DisplayName: capture.DisplayName,
		OriginalURL: capture.OriginalURL, SourceID: string(capture.SourceID),
		LatestSourceVersionID: string(capture.LatestSourceVersionID), Status: string(capture.Status), FetchStatus: string(capture.FetchStatus),
		IngestionStatus: string(capture.IngestionStatus), IndexStatus: string(capture.IndexStatus), ProfileStatus: string(capture.ProfileStatus),
		FailureStage: capture.FailureStage, ErrorCode: capture.ErrorCode, Retryable: capture.Retryable, Version: capture.Version,
		CapturedAt: capture.CapturedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: capture.UpdatedAt.UTC().Format(time.RFC3339Nano), DetailHref: base,
	}
	if capture.LatestSourceVersionID != "" {
		response.ProfileHref = "/api/v1/workspaces/" + string(capture.WorkspaceID) + "/source-versions/" + string(capture.LatestSourceVersionID) + "/knowledge-profile"
	}
	return response
}

func toProfileViewResponse(view captureapp.ProfileView, workspaceID, sourceVersionID foundation.ID) (profileViewResponse, error) {
	if err := view.Profile.Validate(); err != nil || view.Profile.WorkspaceID != workspaceID || view.Profile.SourceVersionID != sourceVersionID {
		return profileViewResponse{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CAPTURE_PROFILE_RESULT_INVALID", false, errors.New("profile reader returned an invalid profile binding"))
	}
	response := profileViewResponse{Profile: profileResponse{
		ID: string(view.Profile.ID), WorkspaceID: string(view.Profile.WorkspaceID), CaptureID: string(view.Profile.CaptureID),
		SourceVersionID: string(view.Profile.SourceVersionID), CurrentRevisionID: string(view.Profile.CurrentRevisionID),
		Status: string(view.Profile.Status), ErrorCode: view.Profile.ErrorCode, Retryable: view.Profile.Retryable,
		Version: view.Profile.Version, CreatedAt: view.Profile.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: view.Profile.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}}
	if view.Revision == nil {
		if view.Profile.CurrentRevisionID != "" || len(view.Evidence) != 0 {
			return profileViewResponse{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CAPTURE_PROFILE_RESULT_INVALID", false, errors.New("profile current revision is missing"))
		}
		return response, nil
	}
	if err := view.Revision.Validate(); err != nil || view.Revision.ID != view.Profile.CurrentRevisionID ||
		view.Revision.ProfileID != view.Profile.ID || view.Revision.WorkspaceID != workspaceID || view.Revision.SourceVersionID != sourceVersionID {
		return profileViewResponse{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CAPTURE_PROFILE_RESULT_INVALID", false, errors.New("profile reader returned an invalid revision binding"))
	}
	if len(view.Evidence) == 0 {
		return profileViewResponse{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CAPTURE_PROFILE_RESULT_INVALID", false, errors.New("profile revision evidence is missing"))
	}
	seen := make(map[foundation.ID]struct{}, len(view.Evidence))
	for _, evidence := range view.Evidence {
		if evidence.RevisionID != view.Revision.ID || evidence.WorkspaceID != workspaceID || evidence.SourceVersionID != sourceVersionID || evidence.SourceSpanID == "" || evidence.CreatedAt.IsZero() {
			return profileViewResponse{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CAPTURE_PROFILE_RESULT_INVALID", false, errors.New("profile evidence binding is invalid"))
		}
		if _, exists := seen[evidence.SourceSpanID]; exists {
			return profileViewResponse{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CAPTURE_PROFILE_RESULT_INVALID", false, errors.New("profile evidence is duplicated"))
		}
		seen[evidence.SourceSpanID] = struct{}{}
	}
	response.Revision = &profileRevisionResponse{
		ID: string(view.Revision.ID), ProfileID: string(view.Revision.ProfileID), WorkspaceID: string(view.Revision.WorkspaceID),
		SourceVersionID: string(view.Revision.SourceVersionID), ParseProjectionID: string(view.Revision.ParseProjectionID),
		IndexVersionID: string(view.Revision.IndexVersionID), ModelRunID: string(view.Revision.ModelRunID),
		ModelSettingsRevision: view.Revision.ModelSettingsRevision, PromptVersion: view.Revision.PromptVersion,
		SchemaVersion: view.Revision.SchemaVersion, Content: view.Revision.Content, ContentDigest: view.Revision.ContentDigest,
		CreatedAt: view.Revision.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	return response, nil
}

func (handler *Handler) available(writer http.ResponseWriter) bool {
	if handler == nil || nilDependency(handler.service) {
		httpapi.WriteProblem(writer, http.StatusServiceUnavailable, "CAPTURE_HTTP_UNAVAILABLE", "快速记录服务暂不可用", true, nil)
		return false
	}
	return true
}

func (handler *Handler) profilesAvailable(writer http.ResponseWriter) bool {
	if handler == nil || nilDependency(handler.profiles) {
		httpapi.WriteProblem(writer, http.StatusServiceUnavailable, "CAPTURE_PROFILE_HTTP_UNAVAILABLE", "知识画像服务暂不可用", true, nil)
		return false
	}
	return true
}

func (handler *Handler) profileRetrierAvailable(writer http.ResponseWriter) bool {
	if handler == nil || nilDependency(handler.profileRetrier) {
		httpapi.WriteProblem(writer, http.StatusServiceUnavailable, "CAPTURE_PROFILE_HTTP_UNAVAILABLE", "知识画像服务暂不可用", true, nil)
		return false
	}
	return true
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

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(writer, status, value)
}

func writeError(writer http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		httpapi.WriteProblem(writer, http.StatusServiceUnavailable, "CAPTURE_REQUEST_CANCELLED", "快速记录请求已取消", false, nil)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(writer, http.StatusInternalServerError, "INTERNAL_ERROR", "快速记录请求未完成", false, nil)
		return
	}
	status := http.StatusInternalServerError
	switch classified.Kind {
	case foundation.ErrorInvalidInput:
		status = http.StatusBadRequest
	case foundation.ErrorNotFound:
		status = http.StatusNotFound
	case foundation.ErrorPermissionDenied:
		status = http.StatusForbidden
	case foundation.ErrorVersionConflict, foundation.ErrorConsistencyViolation, foundation.ErrorManualRecoveryRequired:
		status = http.StatusConflict
	case foundation.ErrorDependencyUnavailable, foundation.ErrorRetryableFailure:
		status = http.StatusServiceUnavailable
	case foundation.ErrorNonRetryableFailure:
		status = http.StatusUnprocessableEntity
	}
	httpapi.WriteProblem(writer, status, classified.Code, "快速记录请求未完成："+classified.Code, classified.Retryable, nil)
}

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func unavailable(code, message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, errors.New(message))
}

var _ Service = (*captureapp.Service)(nil)
