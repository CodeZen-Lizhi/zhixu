// Package workspacehttp exposes the Workspace application contract over HTTP.
package workspacehttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/go-chi/chi/v5"
)

// Service 定义 Workspace HTTP 所需的最小应用层契约。
type Service interface {
	CreateWorkspace(context.Context, application.CreateWorkspaceRequest) (application.WorkspaceResult, error)
	OpenWorkspace(context.Context, string) (application.WorkspaceResult, error)
	GetWorkspace(context.Context, foundation.ID) (application.WorkspaceResult, error)
	ScanWorkspace(context.Context, foundation.ID) ([]domain.ScannedFile, error)
}

// Handler 负责 Workspace 请求解析、响应编码和错误映射。
type Handler struct{ service Service }

// NewHandler 创建 Workspace HTTP Handler。
func NewHandler(service Service) *Handler { return &Handler{service: service} }

// Routes 注册 Workspace 资源路由。
func (h *Handler) Routes(router chi.Router) {
	router.Post("/workspaces", h.create)
	router.Get("/workspaces/{workspaceID}", h.detail)
	router.Post("/workspaces/{workspaceID}/scan", h.scan)
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

func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	id, err := foundation.ParseID(chi.URLParam(r, "workspaceID"))
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
	id, err := foundation.ParseID(chi.URLParam(r, "workspaceID"))
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
