// Package workspacehttp exposes the Workspace application contract over HTTP.
package workspacehttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/gin-gonic/gin"
)

// Service 定义 Workspace HTTP 所需的最小应用层契约。
type Service interface {
	CreateWorkspace(context.Context, application.CreateWorkspaceRequest) (application.WorkspaceResult, error)
	OpenWorkspace(context.Context, string) (application.WorkspaceResult, error)
	GetWorkspace(context.Context, foundation.ID) (application.WorkspaceResult, error)
	ScanWorkspace(context.Context, foundation.ID) ([]domain.ScannedFile, error)
}

type listService interface {
	ListSourceVersions(context.Context, domain.SourceVersionListQuery) ([]domain.SourceVersionListItem, bool, error)
}

type activeService interface {
	GetActiveWorkspace(context.Context) (domain.Workspace, error)
}

// Handler 负责 Workspace 请求解析、响应编码和错误映射。
type Handler struct{ service Service }

// NewHandler 创建 Workspace HTTP Handler。
func NewHandler(service Service) *Handler { return &Handler{service: service} }

// Routes 注册 Workspace 资源路由。
func (h *Handler) Routes(router gin.IRouter) {
	router.POST("/workspaces", httpapi.GinHandler(h.create))
	router.GET("/workspaces/active", httpapi.GinHandler(h.active))
	router.GET("/workspaces/:workspace_id", httpapi.GinHandler(h.detail))
	router.POST("/workspaces/:workspace_id/scan", httpapi.GinHandler(h.scan))
	router.GET("/workspaces/:workspace_id/source-versions", httpapi.GinHandler(h.listSourceVersions))
}

type sourceVersionPageResponse struct {
	Items      []sourceVersionListResponse `json:"items"`
	NextCursor string                      `json:"next_cursor,omitempty"`
}

const (
	sourceVersionCursorVersion = 1
	sourceVersionCursorKind    = "source_version_list"
)

type sourceVersionListCursor struct {
	Version     int    `json:"version"`
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id"`
	At          string `json:"at"`
	ID          string `json:"id"`
	Filter      string `json:"filter"`
}

type sourceVersionListResponse struct {
	ID              string `json:"id"`
	SourceID        string `json:"source_id"`
	WorkspaceID     string `json:"workspace_id"`
	Path            string `json:"path"`
	MimeType        string `json:"mime_type"`
	ByteSize        int64  `json:"byte_size"`
	CapturedAt      string `json:"captured_at"`
	ContentHash     string `json:"content_hash"`
	SecurityStatus  string `json:"security_status"`
	IngestionStatus string `json:"ingestion_status,omitempty"`
	WorkflowStatus  string `json:"workflow_status,omitempty"`
	IndexStatus     string `json:"index_status,omitempty"`
}

func (h *Handler) listSourceVersions(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(listService)
	if !ok {
		writeProblem(w, http.StatusServiceUnavailable, "SOURCE_VERSION_LIST_UNAVAILABLE", "Source Version 列表暂不可用", true, nil)
		return
	}
	workspaceID, err := foundation.ParseID(r.PathValue("workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	allowed := map[string]struct{}{"cursor": {}, "limit": {}, "security_status": {}, "ingestion_status": {}, "workflow_status": {}, "index_status": {}, "mime_type": {}}
	for key, values := range r.URL.Query() {
		if _, ok := allowed[key]; !ok {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_LIST_FILTER_INVALID", false, fmt.Errorf("unknown query parameter %q", key)))
			return
		}
		if len(values) != 1 {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_LIST_FILTER_INVALID", false, fmt.Errorf("query parameter %q must appear once", key)))
			return
		}
	}
	filters := struct {
		SecurityStatus  string `json:"security_status,omitempty"`
		IngestionStatus string `json:"ingestion_status,omitempty"`
		WorkflowStatus  string `json:"workflow_status,omitempty"`
		IndexStatus     string `json:"index_status,omitempty"`
		MimeType        string `json:"mime_type,omitempty"`
	}{
		SecurityStatus: strings.TrimSpace(r.URL.Query().Get("security_status")), IngestionStatus: strings.TrimSpace(r.URL.Query().Get("ingestion_status")),
		WorkflowStatus: strings.TrimSpace(r.URL.Query().Get("workflow_status")), IndexStatus: strings.TrimSpace(r.URL.Query().Get("index_status")), MimeType: strings.TrimSpace(r.URL.Query().Get("mime_type")),
	}
	for _, value := range []string{filters.SecurityStatus, filters.IngestionStatus, filters.WorkflowStatus, filters.IndexStatus, filters.MimeType} {
		if len(value) > 128 || strings.ContainsAny(value, "\r\n\t") {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_LIST_FILTER_INVALID", false, errors.New("filter value is invalid")))
			return
		}
	}
	if !validSourceSecurityStatus(filters.SecurityStatus) || !validSourceIngestionStatus(filters.IngestionStatus) || !validSourceWorkflowStatus(filters.WorkflowStatus) || !validSourceIndexStatus(filters.IndexStatus) {
		writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_LIST_FILTER_INVALID", false, errors.New("source version list status filter is invalid")))
		return
	}
	filterJSON, _ := json.Marshal(filters)
	limit := 30
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, scanErr := strconv.Atoi(raw)
		if scanErr != nil {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_LIST_LIMIT_INVALID", false, scanErr))
			return
		}
		limit = parsed
	}
	if limit < 1 || limit > 100 {
		writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_LIST_LIMIT_INVALID", false, errors.New("limit must be between 1 and 100")))
		return
	}
	var cursorTime *time.Time
	var cursorID foundation.ID
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		if len(raw) > 2048 {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_CURSOR_INVALID", false, errors.New("cursor is too long")))
			return
		}
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(raw)
		if decodeErr != nil {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_CURSOR_INVALID", false, decodeErr))
			return
		}
		var cursor sourceVersionListCursor
		decoder := json.NewDecoder(strings.NewReader(string(decoded)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cursor) != nil || decoder.Decode(&struct{}{}) != io.EOF || cursor.Version != sourceVersionCursorVersion || cursor.Kind != sourceVersionCursorKind || cursor.WorkspaceID != string(workspaceID) || cursor.Filter != string(filterJSON) {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_CURSOR_INVALID", false, errors.New("cursor payload is invalid")))
			return
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, cursor.At)
		if parseErr != nil {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_CURSOR_INVALID", false, parseErr))
			return
		}
		id, idErr := foundation.ParseID(cursor.ID)
		if idErr != nil {
			writeError(w, idErr)
			return
		}
		cursorTime, cursorID = &parsed, id
	}
	items, hasMore, err := service.ListSourceVersions(r.Context(), domain.SourceVersionListQuery{WorkspaceID: workspaceID, SecurityStatus: filters.SecurityStatus, IngestionStatus: filters.IngestionStatus, WorkflowStatus: filters.WorkflowStatus, IndexStatus: filters.IndexStatus, MimeType: filters.MimeType, CursorTime: cursorTime, CursorID: cursorID, Limit: limit})
	if err != nil {
		writeError(w, err)
		return
	}
	response := sourceVersionPageResponse{Items: make([]sourceVersionListResponse, len(items))}
	for index, item := range items {
		response.Items[index] = sourceVersionListResponse{ID: string(item.ID), SourceID: string(item.SourceID), WorkspaceID: string(item.WorkspaceID), Path: item.Path, MimeType: item.MimeType, ByteSize: item.ByteSize, CapturedAt: item.CapturedAt.UTC().Format(time.RFC3339Nano), ContentHash: item.ContentHash, SecurityStatus: item.SecurityStatus, IngestionStatus: item.IngestionStatus, WorkflowStatus: item.WorkflowStatus, IndexStatus: item.IndexStatus}
	}
	if hasMore && len(items) > 0 {
		tail := items[len(items)-1]
		payload, _ := json.Marshal(sourceVersionListCursor{Version: sourceVersionCursorVersion, Kind: sourceVersionCursorKind, WorkspaceID: string(workspaceID), At: tail.CapturedAt.UTC().Format(time.RFC3339Nano), ID: string(tail.ID), Filter: string(filterJSON)})
		response.NextCursor = base64.RawURLEncoding.EncodeToString(payload)
	}
	writeJSON(w, http.StatusOK, response)
}

func validSourceSecurityStatus(status string) bool {
	switch status {
	case "", "pending", "passed", "quarantined":
		return true
	default:
		return false
	}
}

func validSourceIngestionStatus(status string) bool {
	switch status {
	case "", "validating", "parsing", "parsed", "chunking", "chunked", "parse_failed", "cancelled":
		return true
	default:
		return false
	}
}

func validSourceWorkflowStatus(status string) bool {
	switch status {
	case "", "pending", "running", "waiting_for_human", "retry_wait", "paused", "succeeded", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func validSourceIndexStatus(status string) bool {
	switch status {
	case "", "included", "excluded":
		return true
	default:
		return false
	}
}

type createRequest struct {
	Name          string `json:"name"`
	RootPath      string `json:"root_path"`
	InitializeGit bool   `json:"initialize_git"`
}

type workspaceResponse struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	RootPath  string      `json:"root_path"`
	Status    string      `json:"status"`
	Version   int64       `json:"version"`
	Git       gitResponse `json:"git"`
	Warnings  []string    `json:"warnings,omitempty"`
	CreatedAt string      `json:"created_at"`
	UpdatedAt string      `json:"updated_at"`
}

type activeWorkspaceResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	RootPath     string `json:"root_path"`
	Status       string `json:"status"`
	Availability string `json:"availability"`
	Version      int64  `json:"version"`
}

type gitResponse struct {
	Present        bool   `json:"present"`
	RepositoryPath string `json:"repository_path"`
	Branch         string `json:"branch"`
	Head           string `json:"head"`
	Dirty          bool   `json:"dirty"`
	CheckedAt      string `json:"checked_at"`
}

type scanResponse struct {
	WorkspaceID string        `json:"workspace_id"`
	Files       []scannedFile `json:"files"`
	Count       int           `json:"count"`
}

type scannedFile struct {
	RelativePath           string `json:"relative_path"`
	ByteSize               int64  `json:"byte_size"`
	ContentHash            string `json:"content_hash"`
	MediaType              string `json:"media_type"`
	SourceID               string `json:"source_id"`
	SourceVersionID        string `json:"source_version_id"`
	ContentArtifactID      string `json:"content_artifact_id"`
	ContentArtifactCreated bool   `json:"content_artifact_created"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var request createRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "WORKSPACE_SERVICE_UNAVAILABLE", "Workspace 服务暂不可用", true, nil)
		return
	}
	result, err := h.service.CreateWorkspace(r.Context(), application.CreateWorkspaceRequest{
		Name: request.Name, RootPath: request.RootPath, InitializeGit: request.InitializeGit,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toWorkspaceResponse(result))
}

func (h *Handler) active(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		writeProblem(w, http.StatusServiceUnavailable, "WORKSPACE_SERVICE_UNAVAILABLE", "Workspace 服务暂不可用", true, nil)
		return
	}
	service, ok := h.service.(activeService)
	if !ok {
		writeProblem(w, http.StatusServiceUnavailable, "WORKSPACE_SERVICE_UNAVAILABLE", "Workspace 服务暂不可用", true, nil)
		return
	}
	workspace, err := service.GetActiveWorkspace(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toActiveWorkspaceResponse(workspace))
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	id, err := foundation.ParseID(r.PathValue("workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "WORKSPACE_SERVICE_UNAVAILABLE", "Workspace 服务暂不可用", true, nil)
		return
	}
	result, err := h.service.GetWorkspace(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toWorkspaceResponse(result))
}

func (h *Handler) scan(w http.ResponseWriter, r *http.Request) {
	id, err := foundation.ParseID(r.PathValue("workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "WORKSPACE_SERVICE_UNAVAILABLE", "Workspace 服务暂不可用", true, nil)
		return
	}
	files, err := h.service.ScanWorkspace(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]scannedFile, len(files))
	for index, file := range files {
		items[index] = scannedFile{
			RelativePath: file.RelativePath, ByteSize: file.ByteSize, ContentHash: file.ContentHash, MediaType: file.MediaType,
			SourceID: string(file.SourceID), SourceVersionID: string(file.SourceVersionID), ContentArtifactID: string(file.ContentArtifactID),
			ContentArtifactCreated: file.ContentArtifactCreated,
		}
	}
	writeJSON(w, http.StatusOK, scanResponse{WorkspaceID: string(id), Files: items, Count: len(items)})
}

func toWorkspaceResponse(result application.WorkspaceResult) workspaceResponse {
	workspace := result.Workspace
	return workspaceResponse{
		ID: string(workspace.ID), Name: workspace.Name, RootPath: workspace.RootPath,
		Status: string(workspace.Status), Version: workspace.Version, Warnings: result.Warnings,
		CreatedAt: workspace.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		UpdatedAt: workspace.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Git:       gitResponse{Present: result.Git.Present, RepositoryPath: result.Git.RepositoryPath, Branch: result.Git.Branch, Head: result.Git.Head, Dirty: result.Git.Dirty, CheckedAt: workspace.Git.CheckedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00")},
	}
}

func toActiveWorkspaceResponse(workspace domain.Workspace) activeWorkspaceResponse {
	return activeWorkspaceResponse{
		ID: string(workspace.ID), Name: workspace.Name, RootPath: workspace.RootPath,
		Status: string(workspace.Status), Availability: string(workspace.Availability), Version: workspace.Version,
	}
}

func decodeJSON(r *http.Request, target any) error {
	if err := httpapi.DecodeJSON(r, target); err != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeProblem(w http.ResponseWriter, status int, code, message string, retryable bool, details map[string]any) {
	httpapi.WriteProblem(w, status, code, message, retryable, details)
}

func writeError(w http.ResponseWriter, err error) {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		writeProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "服务处理失败", false, nil)
		return
	}
	status := http.StatusInternalServerError
	switch classified.Kind {
	case foundation.ErrorInvalidInput:
		status = http.StatusBadRequest
	case foundation.ErrorNotFound:
		status = http.StatusNotFound
	case foundation.ErrorVersionConflict:
		status = http.StatusConflict
	case foundation.ErrorDependencyUnavailable:
		status = http.StatusServiceUnavailable
	case foundation.ErrorRetryableFailure:
		status = http.StatusServiceUnavailable
	case foundation.ErrorConsistencyViolation:
		status = http.StatusConflict
	case foundation.ErrorManualRecoveryRequired:
		status = http.StatusConflict
	case foundation.ErrorPermissionDenied:
		status = http.StatusForbidden
	}
	writeProblem(w, status, classified.Code, publicMessage(classified.Code), classified.Retryable, nil)
}

func publicMessage(code string) string {
	if strings.TrimSpace(code) == "" {
		return "请求处理失败"
	}
	return "请求未完成：" + code
}
