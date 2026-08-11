// Package http 提供 Git Remote 与 SyncRun 的严格、无密钥 HTTP wire。
package http

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	stdhttp "net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
)

const (
	defaultTimeout       = 35 * time.Second
	defaultRunListLimit  = 30
	maxRequestBodyBytes  = 32 * 1024
	errorInvalidJSON     = "INVALID_JSON"
	errorUnsupportedType = "UNSUPPORTED_MEDIA_TYPE"
	errorUnavailable     = "GIT_SYNC_HTTP_UNAVAILABLE"
)

// Service 是 Handler 使用的完整 Git Sync 应用边界。
type Service interface {
	GetConfig(context.Context, foundation.ID) (domain.RemoteConfig, error)
	SaveConfig(context.Context, application.SaveConfigCommand) (application.ConfigReceipt, error)
	RemoveConfig(context.Context, application.RemoveConfigCommand) (application.ConfigReceipt, error)
	TestConfig(context.Context, application.TestConfigCommand) (application.TestConfigResult, error)
	GetStatus(context.Context, foundation.ID) (application.StatusSnapshot, error)
	CreateRun(context.Context, application.CreateRunCommand) (application.RunReceipt, error)
	RetryRun(context.Context, application.RetryRunCommand) (application.RunReceipt, error)
	GetRun(context.Context, foundation.ID, foundation.ID) (domain.SyncRun, error)
	ListRuns(context.Context, application.ListRunsCommand) (application.PublicRunPage, error)
}

// Handler 将严格请求映射到 Git Sync 应用服务。
type Handler struct {
	service Service
	timeout time.Duration
}

// NewHandler 创建 fail-closed Git Sync Handler。
func NewHandler(service Service, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Handler{service: service, timeout: timeout}
}

// Available 报告是否注入真实应用服务。
func (handler *Handler) Available() bool { return handler != nil && handler.service != nil }

// Routes 在 `/api/v1` Router 下注册 Workspace-scoped Git Sync 端点。
func (handler *Handler) Routes(router gin.IRouter) {
	router.GET("/workspaces/:workspace_id/git-remote", httpapi.GinHandler(handler.getConfig))
	router.PUT("/workspaces/:workspace_id/git-remote", httpapi.GinHandler(handler.saveConfig))
	router.DELETE("/workspaces/:workspace_id/git-remote", httpapi.GinHandler(handler.removeConfig))
	router.POST("/workspaces/:workspace_id/git-remote/tests", httpapi.GinHandler(handler.testConfig))
	router.GET("/workspaces/:workspace_id/git-sync", httpapi.GinHandler(handler.getStatus))
	router.GET("/workspaces/:workspace_id/git-sync/runs", httpapi.GinHandler(handler.listRuns))
	router.POST("/workspaces/:workspace_id/git-sync/runs", httpapi.GinHandler(handler.createRun))
	router.GET("/workspaces/:workspace_id/git-sync/runs/:run_id", httpapi.GinHandler(handler.getRun))
	router.POST("/workspaces/:workspace_id/git-sync/runs/:run_id/retries", httpapi.GinHandler(handler.retryRun))
}

type tokenActionRequest struct {
	Action string  `json:"action"`
	Value  *string `json:"value,omitempty"`
}

type saveConfigRequest struct {
	ExpectedRevision int64              `json:"expected_revision"`
	RemoteURL        string             `json:"remote_url"`
	Branch           string             `json:"branch"`
	AutoSync         bool               `json:"auto_sync"`
	Token            tokenActionRequest `json:"token"`
}

type removeConfigRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

type testConfigRequest struct {
	ExpectedRevision int64              `json:"expected_revision"`
	RemoteURL        string             `json:"remote_url"`
	Branch           string             `json:"branch"`
	Token            tokenActionRequest `json:"token"`
}

type retryRunRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

type configResponse struct {
	WorkspaceID     string  `json:"workspace_id"`
	Configured      bool    `json:"configured"`
	RemoteURL       *string `json:"remote_url"`
	Branch          *string `json:"branch"`
	AutoSync        bool    `json:"auto_sync"`
	TokenConfigured bool    `json:"token_configured"`
	Revision        int64   `json:"revision"`
	CreatedAt       *string `json:"created_at"`
	UpdatedAt       *string `json:"updated_at"`
	Replayed        bool    `json:"replayed,omitempty"`
}

type fileChangeResponse struct {
	Path    string  `json:"path"`
	OldPath *string `json:"old_path"`
	Kind    string  `json:"kind"`
}

type runResponse struct {
	ID                string               `json:"id"`
	WorkspaceID       string               `json:"workspace_id"`
	ConfigRevision    int64                `json:"config_revision"`
	RemoteURL         string               `json:"remote_url"`
	Branch            string               `json:"branch"`
	Trigger           string               `json:"trigger"`
	RetryOfRunID      *string              `json:"retry_of_run_id"`
	Status            string               `json:"status"`
	Direction         string               `json:"direction"`
	FailureClass      string               `json:"failure_class"`
	ErrorCode         string               `json:"error_code"`
	Retryable         bool                 `json:"retryable"`
	ExpectedHeadOID   *string              `json:"expected_head_oid"`
	ExpectedRemoteOID *string              `json:"expected_remote_oid"`
	VerifiedHeadOID   *string              `json:"verified_head_oid"`
	VerifiedRemoteOID *string              `json:"verified_remote_oid"`
	ChangedFiles      []fileChangeResponse `json:"changed_files"`
	IndexStatus       string               `json:"index_status"`
	IndexErrorCode    string               `json:"index_error_code"`
	IndexRetryable    bool                 `json:"index_retryable"`
	IndexVersionID    *string              `json:"index_version_id"`
	AttemptCount      int                  `json:"attempt_count"`
	Version           int64                `json:"version"`
	CreatedAt         string               `json:"created_at"`
	UpdatedAt         string               `json:"updated_at"`
	CompletedAt       *string              `json:"completed_at"`
	Replayed          bool                 `json:"replayed,omitempty"`
}

type statusResponse struct {
	Config     configResponse `json:"config"`
	CurrentRun *runResponse   `json:"current_run"`
}

type listRunsResponse struct {
	Items      []runResponse `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

func (handler *Handler) getConfig(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	workspaceID, ok := handler.prepare(writer, request)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	config, err := handler.service.GetConfig(ctx, workspaceID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, stdhttp.StatusOK, projectConfig(config, false))
}

func (handler *Handler) saveConfig(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	workspaceID, ok := handler.prepare(writer, request)
	if !ok {
		return
	}
	key, ok := idempotencyKey(writer, request)
	if !ok {
		return
	}
	var input saveConfigRequest
	if err := decodeRequest(request, &input); err != nil {
		writeError(writer, err)
		return
	}
	action, err := tokenAction(input.Token)
	clearTokenRequest(&input.Token)
	if err != nil {
		writeError(writer, err)
		return
	}
	defer action.Value.Destroy()
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	receipt, err := handler.service.SaveConfig(ctx, application.SaveConfigCommand{
		WorkspaceID: workspaceID, ExpectedRevision: input.ExpectedRevision, RemoteURL: input.RemoteURL,
		Branch: input.Branch, AutoSync: input.AutoSync, SecretAction: action,
		IdempotencyKey: key, Actor: actor(request),
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	status := stdhttp.StatusCreated
	if receipt.Replayed {
		status = stdhttp.StatusOK
	}
	writeJSON(writer, status, projectConfig(receipt.Config, receipt.Replayed))
}

func (handler *Handler) removeConfig(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	workspaceID, ok := handler.prepare(writer, request)
	if !ok {
		return
	}
	key, ok := idempotencyKey(writer, request)
	if !ok {
		return
	}
	var input removeConfigRequest
	if err := decodeRequest(request, &input); err != nil {
		writeError(writer, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	receipt, err := handler.service.RemoveConfig(ctx, application.RemoveConfigCommand{
		WorkspaceID: workspaceID, ExpectedRevision: input.ExpectedRevision,
		IdempotencyKey: key, Actor: actor(request),
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, stdhttp.StatusOK, projectConfig(receipt.Config, receipt.Replayed))
}

func (handler *Handler) testConfig(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	workspaceID, ok := handler.prepare(writer, request)
	if !ok {
		return
	}
	var input testConfigRequest
	if err := decodeRequest(request, &input); err != nil {
		writeError(writer, err)
		return
	}
	action, err := tokenAction(input.Token)
	clearTokenRequest(&input.Token)
	if err != nil {
		writeError(writer, err)
		return
	}
	defer action.Value.Destroy()
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.TestConfig(ctx, application.TestConfigCommand{
		WorkspaceID: workspaceID, ExpectedRevision: input.ExpectedRevision,
		RemoteURL: input.RemoteURL, Branch: input.Branch, SecretAction: action,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, stdhttp.StatusOK, map[string]any{
		"status": "ok", "remote_url": result.RemoteURL, "branch": result.Branch,
	})
}

func (handler *Handler) getStatus(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	workspaceID, ok := handler.prepare(writer, request)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	status, err := handler.service.GetStatus(ctx, workspaceID)
	if err != nil {
		writeError(writer, err)
		return
	}
	response := statusResponse{Config: projectConfig(status.Config, false)}
	if status.CurrentRun != nil {
		value := projectRun(*status.CurrentRun, false)
		response.CurrentRun = &value
	}
	writeJSON(writer, stdhttp.StatusOK, response)
}

func (handler *Handler) createRun(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	workspaceID, ok := handler.prepare(writer, request)
	if !ok {
		return
	}
	key, ok := idempotencyKey(writer, request)
	if !ok || !requireEmptyBody(writer, request) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	receipt, err := handler.service.CreateRun(ctx, application.CreateRunCommand{
		WorkspaceID: workspaceID, Trigger: domain.TriggerManual, IdempotencyKey: key,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	status := stdhttp.StatusAccepted
	if receipt.Replayed {
		status = stdhttp.StatusOK
	}
	writeJSON(writer, status, projectRun(receipt.Run, receipt.Replayed))
}

func (handler *Handler) retryRun(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	workspaceID, ok := handler.prepare(writer, request)
	if !ok {
		return
	}
	runID, err := foundation.ParseID(request.PathValue("run_id"))
	if err != nil {
		writeError(writer, invalid("Git sync run identity is invalid"))
		return
	}
	key, ok := idempotencyKey(writer, request)
	if !ok {
		return
	}
	var input retryRunRequest
	if err := decodeRequest(request, &input); err != nil {
		writeError(writer, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	receipt, err := handler.service.RetryRun(ctx, application.RetryRunCommand{
		WorkspaceID: workspaceID, RunID: runID, ExpectedVersion: input.ExpectedVersion, IdempotencyKey: key,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	status := stdhttp.StatusAccepted
	if receipt.Replayed {
		status = stdhttp.StatusOK
	}
	writeJSON(writer, status, projectRun(receipt.Run, receipt.Replayed))
}

func (handler *Handler) getRun(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	workspaceID, ok := handler.prepare(writer, request)
	if !ok {
		return
	}
	runID, err := foundation.ParseID(request.PathValue("run_id"))
	if err != nil {
		writeError(writer, invalid("Git sync run identity is invalid"))
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	run, err := handler.service.GetRun(ctx, workspaceID, runID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, stdhttp.StatusOK, projectRun(run, false))
}

func (handler *Handler) listRuns(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	workspaceID, ok := handler.prepare(writer, request, "limit", "cursor")
	if !ok {
		return
	}
	limit := defaultRunListLimit
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(writer, invalid("Git sync run limit is invalid"))
			return
		}
		limit = parsed
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.ListRuns(ctx, application.ListRunsCommand{
		WorkspaceID: workspaceID, Cursor: request.URL.Query().Get("cursor"), Limit: limit,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]runResponse, len(page.Items))
	for index, run := range page.Items {
		items[index] = projectRun(run, false)
	}
	writeJSON(writer, stdhttp.StatusOK, listRunsResponse{Items: items, NextCursor: page.NextCursor})
}

func (handler *Handler) prepare(writer stdhttp.ResponseWriter, request *stdhttp.Request, allowedQuery ...string) (foundation.ID, bool) {
	if handler == nil || handler.service == nil {
		writeError(writer, foundation.NewError(foundation.ErrorDependencyUnavailable, errorUnavailable, true, errors.New("Git sync service is unavailable")))
		return "", false
	}
	if request == nil || !onlyQueryParameters(request, allowedQuery...) {
		writeError(writer, invalid("Git sync query parameters are invalid"))
		return "", false
	}
	workspaceID, err := foundation.ParseID(request.PathValue("workspace_id"))
	if err != nil {
		writeError(writer, invalid("Workspace identity is invalid"))
		return "", false
	}
	return workspaceID, true
}

func tokenAction(request tokenActionRequest) (domain.SecretAction, error) {
	switch domain.SecretActionKind(request.Action) {
	case domain.SecretActionKeep:
		if request.Value != nil {
			return domain.SecretAction{}, invalid("keep must not include a token value")
		}
		return domain.KeepSecret(), nil
	case domain.SecretActionReplace:
		if request.Value == nil {
			return domain.SecretAction{}, invalid("replace requires a token value")
		}
		return domain.ReplaceSecret(*request.Value)
	case domain.SecretActionClear:
		if request.Value != nil {
			return domain.SecretAction{}, invalid("clear must not include a token value")
		}
		return domain.ClearSecret(), nil
	default:
		return domain.SecretAction{}, invalid("remote token action is invalid")
	}
}

func clearTokenRequest(request *tokenActionRequest) {
	if request == nil || request.Value == nil {
		return
	}
	*request.Value = ""
	request.Value = nil
}

func idempotencyKey(writer stdhttp.ResponseWriter, request *stdhttp.Request) (string, bool) {
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 || values[0] == "" {
		writeError(writer, invalid("Idempotency-Key must be provided exactly once"))
		return "", false
	}
	return values[0], true
}

func actor(request *stdhttp.Request) string {
	if principal, ok := authhttp.PrincipalFromContext(request.Context()); ok {
		return string(principal.Kind) + ":" + string(principal.ID)
	}
	return "local-development"
}

func decodeRequest(request *stdhttp.Request, target any) error {
	if request == nil || request.Body == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, errorInvalidJSON, false, errors.New("request body is required"))
	}
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return foundation.NewError(foundation.ErrorInvalidInput, errorUnsupportedType, false, errors.New("request must use exactly one application/json Content-Type"))
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		return foundation.NewError(foundation.ErrorInvalidInput, errorUnsupportedType, false, errors.New("request must use application/json"))
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxRequestBodyBytes {
		return foundation.NewError(foundation.ErrorInvalidInput, errorInvalidJSON, false, errors.New("request body is invalid"))
	}
	defer clear(body)
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxRequestBodyBytes
	limits.MaxStringBytes = 16 * 1024
	decoded, err := strictjson.DecodeObject[map[string]any](body, limits, nil)
	if err != nil || decoded == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, errorInvalidJSON, false, errors.New("request body is invalid"))
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	if err := httpapi.DecodeJSON(request, target); err != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, errorInvalidJSON, false, err)
	}
	return nil
}

func requireEmptyBody(writer stdhttp.ResponseWriter, request *stdhttp.Request) bool {
	if request == nil || request.Body == nil {
		return true
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 2))
	if err != nil || len(body) != 0 {
		clear(body)
		writeError(writer, invalid("request body must be empty"))
		return false
	}
	return true
}

func onlyQueryParameters(request *stdhttp.Request, allowed ...string) bool {
	if request == nil || request.URL == nil {
		return false
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return false
	}
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := set[key]; !ok || len(entries) != 1 {
			return false
		}
	}
	return true
}

func projectConfig(config domain.RemoteConfig, replayed bool) configResponse {
	response := configResponse{
		WorkspaceID: string(config.WorkspaceID), Configured: config.Configured, AutoSync: config.AutoSync,
		TokenConfigured: config.TokenConfigured, Revision: config.Revision, Replayed: replayed,
	}
	if config.Configured {
		response.RemoteURL, response.Branch = stringPointer(config.RemoteURL), stringPointer(config.Branch)
	}
	if !config.CreatedAt.IsZero() {
		response.CreatedAt = stringPointer(config.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
	if !config.UpdatedAt.IsZero() {
		response.UpdatedAt = stringPointer(config.UpdatedAt.UTC().Format(time.RFC3339Nano))
	}
	return response
}

func projectRun(run domain.SyncRun, replayed bool) runResponse {
	changes := make([]fileChangeResponse, len(run.ChangedFiles))
	for index, change := range run.ChangedFiles {
		changes[index] = fileChangeResponse{Path: change.Path, Kind: string(change.Kind)}
		if change.OldPath != "" {
			changes[index].OldPath = stringPointer(change.OldPath)
		}
	}
	response := runResponse{
		ID: string(run.ID), WorkspaceID: string(run.WorkspaceID), ConfigRevision: run.ConfigRevision,
		RemoteURL: run.RemoteURL, Branch: run.Branch, Trigger: string(run.Trigger), Status: string(run.Status),
		Direction: string(run.Direction), FailureClass: string(run.FailureClass), ErrorCode: run.ErrorCode,
		Retryable: run.Retryable, ChangedFiles: changes, IndexStatus: string(run.IndexStatus),
		IndexErrorCode: run.IndexErrorCode, IndexRetryable: run.IndexRetryable, AttemptCount: run.AttemptCount,
		Version: run.Version, CreatedAt: run.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: run.UpdatedAt.UTC().Format(time.RFC3339Nano), Replayed: replayed,
	}
	response.RetryOfRunID = optionalID(run.RetryOfRunID)
	response.IndexVersionID = optionalID(run.IndexVersionID)
	response.ExpectedHeadOID = optionalString(run.ExpectedHeadOID)
	response.ExpectedRemoteOID = optionalString(run.ExpectedRemoteOID)
	response.VerifiedHeadOID = optionalString(run.VerifiedHeadOID)
	response.VerifiedRemoteOID = optionalString(run.VerifiedRemoteOID)
	if run.CompletedAt != nil {
		response.CompletedAt = stringPointer(run.CompletedAt.UTC().Format(time.RFC3339Nano))
	}
	return response
}

func optionalID(value foundation.ID) *string {
	if value == "" {
		return nil
	}
	return stringPointer(string(value))
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return stringPointer(value)
}

func stringPointer(value string) *string { return &value }

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, errors.New(message))
}

func writeJSON(writer stdhttp.ResponseWriter, status int, value any) {
	writer.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(writer, status, value)
}

func writeError(writer stdhttp.ResponseWriter, err error) {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(writer, stdhttp.StatusInternalServerError, "INTERNAL_ERROR", "Git 同步请求失败", false, nil)
		return
	}
	status := httpapi.StatusForErrorKind(classified.Kind)
	if classified.Code == errorUnsupportedType {
		status = stdhttp.StatusUnsupportedMediaType
	}
	writer.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteProblem(writer, status, classified.Code, "Git 同步请求未完成", classified.Retryable, nil)
}
