// Package http 提供认证路由、Cookie/CSRF 边界与 Capability Middleware。
package http

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

	"github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	authorigin "github.com/CodeZen-Lizhi/zhixu/internal/auth/origin"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/go-chi/chi/v5"
)

const (
	// SessionCookieName 是浏览器 Session 的固定 Cookie 名称。
	SessionCookieName = "zhixu_session"
	// CSRFHeaderName 是状态修改请求必须携带的 CSRF Header。
	CSRFHeaderName = "X-CSRF-Token"
)

type principalKey struct{}

// Service 是 HTTP 边界所需的认证应用能力。
type Service interface {
	ExchangeBootstrap(context.Context, string) (application.SessionCredential, error)
	AuthenticateSession(context.Context, string, string, bool) (domain.Principal, error)
	CurrentSession(context.Context, string, string, bool) (domain.SessionInfo, error)
	RotateSession(context.Context, domain.Principal, string, string, bool) (application.SessionCredential, error)
	AuthenticateAPIToken(context.Context, string) (domain.Principal, error)
	CreateAPIToken(context.Context, domain.Principal, string, []capability.Capability, time.Duration) (application.APITokenCredential, error)
	ListAPITokens(context.Context, domain.Principal, domain.APITokenListQuery) (domain.APITokenListPage, error)
	RevokeSession(context.Context, domain.Principal, foundation.ID) error
	RevokeAPIToken(context.Context, domain.Principal, foundation.ID) error
}

// Options 配置 Cookie 和允许的浏览器 Origin。
type Options struct {
	SecureCookie   bool
	AllowedOrigins []string
}

// Handler 组合开放认证路由、受保护路由和认证 Middleware。
type Handler struct {
	service        Service
	secureCookie   bool
	allowedOrigins map[string]struct{}
}

const (
	apiTokenCursorVersion = 1
	apiTokenCursorKind    = "auth_api_token_list"
)

type apiTokenPageResponse struct {
	Items      []domain.APITokenInfo `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

type apiTokenListCursor struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Limit   int    `json:"limit"`
	At      string `json:"at"`
	ID      string `json:"id"`
}

// NewHandler 创建认证 HTTP 边界。
func NewHandler(service Service, options Options) (*Handler, error) {
	if service == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, application.ErrorCodeUnavailable, true, errors.New("authentication service is nil"))
	}
	origins := make(map[string]struct{}, len(options.AllowedOrigins))
	for _, value := range options.AllowedOrigins {
		if !authorigin.IsCanonical(value) {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, application.ErrorCodeInvalid, false, errors.New("allowed origin is invalid"))
		}
		origins[value] = struct{}{}
	}
	if len(origins) == 0 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, application.ErrorCodeInvalid, false, errors.New("at least one allowed origin is required"))
	}
	return &Handler{service: service, secureCookie: options.SecureCookie, allowedOrigins: origins}, nil
}

// OpenRoutes 注册无需既有 Session 的 Bootstrap 交换端点。
func (handler *Handler) OpenRoutes(router chi.Router) {
	router.Post("/auth/sessions", handler.exchangeBootstrap)
}

// ProtectedRoutes 注册必须经过认证 Middleware 的凭据管理端点。
func (handler *Handler) ProtectedRoutes(router chi.Router) {
	router.Get("/auth/session", handler.currentSession)
	router.Post("/auth/session/rotate", handler.rotateSession)
	router.Delete("/auth/session", handler.revokeCurrentSession)
	router.Post("/auth/api-tokens", handler.createAPIToken)
	router.Get("/auth/api-tokens", handler.listAPITokens)
	router.Delete("/auth/api-tokens/{token_id}", handler.revokeAPIToken)
}

// Middleware 验证 Session 或 Bearer Token，并执行 Origin/CSRF 与 Capability 策略。
func (handler *Handler) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if handler == nil || handler.service == nil {
			writeAuthProblem(writer, foundation.NewError(foundation.ErrorDependencyUnavailable, application.ErrorCodeUnavailable, true, errors.New("authentication handler is unavailable")))
			return
		}
		principal, err := handler.authenticate(request)
		if err != nil {
			writeAuthProblem(writer, err)
			return
		}
		for _, required := range RequiredCapabilities(request) {
			if err := application.Authorize(principal, required); err != nil {
				writeAuthProblem(writer, err)
				return
			}
		}
		ctx := context.WithValue(request.Context(), principalKey{}, principal)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

// PrincipalFromContext 返回已认证身份的独立 Scope 副本。
func PrincipalFromContext(ctx context.Context) (domain.Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(domain.Principal)
	if !ok || domain.ValidatePrincipal(principal) != nil {
		return domain.Principal{}, false
	}
	principal.Scopes = append([]capability.Capability(nil), principal.Scopes...)
	return principal, true
}

// RequiredCapability 返回公共路由的最小 Capability。
//
// 该策略故意采用显式路由模板，而不是 contains/前缀启发式：新增或改名的
// 状态修改路由默认落到最高风险的 WRITE_KNOWLEDGE，避免悄悄获得较低 Scope。
func RequiredCapability(request *http.Request) capability.Capability {
	required := RequiredCapabilities(request)
	if len(required) == 0 {
		return ""
	}
	return required[0]
}

// RequiredCapabilities 返回公共路由必须同时具备的全部 Capability。
// 多 Capability 路由用于把“读取本地知识”和“创建持久候选/Issue”这两个
// 不可互相替代的权限绑定到同一个入口，避免只拥有索引维护权限即可触发副作用。
func RequiredCapabilities(request *http.Request) []capability.Capability {
	if request == nil {
		return nil
	}
	path := request.URL.Path
	if path == "/api/v1/system/status" || isAuthManagementPath(path) {
		return nil
	}
	if request.Method == http.MethodGet || request.Method == http.MethodHead || request.Method == http.MethodOptions {
		return []capability.Capability{capability.ReadLocal}
	}
	for _, route := range capabilityRoutes {
		if route.method == request.Method && routePatternMatches(route.pattern, path) {
			return append([]capability.Capability(nil), route.capabilities...)
		}
	}
	// Unknown mutation: fail closed. A route accidentally omitted from the
	// policy cannot be reached by a low-scope API Token.
	return []capability.Capability{capability.WriteKnowledge}
}

type capabilityRoute struct {
	method       string
	pattern      string
	capabilities []capability.Capability
}

func oneCapability(method, pattern string, value capability.Capability) capabilityRoute {
	return capabilityRoute{method: method, pattern: pattern, capabilities: []capability.Capability{value}}
}

func multipleCapabilities(method, pattern string, values ...capability.Capability) capabilityRoute {
	return capabilityRoute{method: method, pattern: pattern, capabilities: append([]capability.Capability(nil), values...)}
}

// capabilityRoutes 是 API 公共状态修改端点的唯一 Capability 映射表。
var capabilityRoutes = []capabilityRoute{
	oneCapability(http.MethodPost, "/api/v1/search", capability.ReadLocal),
	oneCapability(http.MethodPost, "/api/v1/graph/global", capability.ReadLocal),
	oneCapability(http.MethodPost, "/api/v1/graph/neighborhood", capability.ReadLocal),
	oneCapability(http.MethodPost, "/api/v1/graph/path", capability.ReadLocal),
	oneCapability(http.MethodPost, "/api/v1/collections/validate", capability.ReadLocal),
	oneCapability(http.MethodPost, "/api/v1/collections/preview", capability.ReadLocal),
	oneCapability(http.MethodPost, "/api/v1/conversations", capability.ReadLocal),
	oneCapability(http.MethodPost, "/api/v1/conversations/{conversation_id}/questions", capability.ReadLocal),
	oneCapability(http.MethodPost, "/api/v1/answers/{answer_id}/feedback", capability.ReadLocal),
	oneCapability(http.MethodPost, "/api/v1/exports", capability.ReadLocal),

	oneCapability(http.MethodPost, "/api/v1/workspaces", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/artifacts", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/artifacts/{artifact_id}/outline", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/artifacts/{artifact_id}/outline/approve", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/artifacts/{artifact_id}/revisions", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/artifacts/{artifact_id}/sections", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/artifacts/{artifact_id}/sections/generate", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/artifacts/{artifact_id}/draft/approve", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/artifacts/{artifact_id}/exports/markdown", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/artifacts/{artifact_id}/publish-proposals", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/workspaces/{workspaceID}/workflows", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/workflows/{runID}/human-tasks/{taskID}/decision", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/workflows/{runID}/pause", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/workflows/{runID}/resume", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/workflows/{runID}/cancel", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/workspaces/{workspaceID}/proposals", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/collections", capability.WriteProposal),
	oneCapability(http.MethodPut, "/api/v1/collections/{collection_id}", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/collections/{collection_id}/archive", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/health/issues/{issue_id}/decisions", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/health/issues/{issue_id}/repair-proposals", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/graph/candidates/{candidate_id}/decisions", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/workspaces/{workspaceID}/timeline/{eventID}/impact-analysis", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/decks", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/decks/{deck_id}/cards", capability.WriteProposal),
	oneCapability(http.MethodPut, "/api/v1/review/cards/{card_id}", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/decks/{deck_id}/schedule/pause", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/decks/{deck_id}/schedule/resume", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/decks/{deck_id}/schedule/reset", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/cards/{card_id}/approve", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/cards/{card_id}/reject", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/cards/{card_id}/invalidate", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/invalidation", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/sessions", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/sessions/{session_id}/complete", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/sessions/{session_id}/answers", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/answers/{answer_id}/learning-path", capability.WriteProposal),
	oneCapability(http.MethodPut, "/api/v1/review/answers/{answer_id}/learning-path/status", capability.WriteProposal),
	oneCapability(http.MethodPut, "/api/v1/review/answers/{answer_id}/learning-path/steps/{step_id}", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/memories", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/memories/{memory_id}/confirm", capability.WriteProposal),
	oneCapability(http.MethodPut, "/api/v1/memories/{memory_id}", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/memories/{memory_id}/pause", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/memories/{memory_id}/resume", capability.WriteProposal),
	oneCapability(http.MethodDelete, "/api/v1/memories/{memory_id}", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/interviews", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/interviews/{session_id}/turns", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/interviews/{session_id}/complete", capability.WriteProposal),
	oneCapability(http.MethodPost, "/api/v1/review/interviews/{session_id}/learning-paths/{path_id}/steps/{step_id}/memory-candidate", capability.WriteProposal),
	oneCapability(http.MethodPut, "/api/v1/review/learning-paths/{path_id}/status", capability.WriteProposal),
	oneCapability(http.MethodPut, "/api/v1/review/learning-paths/{path_id}/steps/{step_id}", capability.WriteProposal),

	oneCapability(http.MethodPost, "/api/v1/proposals/{proposalID}/approvals", capability.WriteKnowledge),
	oneCapability(http.MethodPost, "/api/v1/proposals/{proposalID}/apply-preflight", capability.WriteKnowledge),

	multipleCapabilities(http.MethodPost, "/api/v1/workspaces/{workspaceID}/scan", capability.ReadLocal, capability.IndexMaintenance),
	multipleCapabilities(http.MethodPost, "/api/v1/source-versions/{sourceVersionID}/ingestion-attempts", capability.ReadLocal, capability.IndexMaintenance),
	multipleCapabilities(http.MethodPost, "/api/v1/health/scans", capability.ReadLocal, capability.WriteProposal),
	multipleCapabilities(http.MethodPut, "/api/v1/health/schedules", capability.ReadLocal, capability.WriteProposal),
	multipleCapabilities(http.MethodPost, "/api/v1/graph/candidate-scans", capability.ReadLocal, capability.WriteProposal),
}

func isAuthManagementPath(path string) bool {
	for _, pattern := range []string{
		"/api/v1/auth/sessions",
		"/api/v1/auth/session",
		"/api/v1/auth/session/rotate",
		"/api/v1/auth/api-tokens",
		"/auth/sessions",
		"/auth/session",
		"/auth/session/rotate",
		"/auth/api-tokens",
	} {
		if path == pattern || routePatternMatches(pattern+"/{token_id}", path) {
			return true
		}
	}
	return false
}

func routePatternMatches(pattern, path string) bool {
	patternParts := splitPath(pattern)
	pathParts := splitPath(path)
	if len(patternParts) != len(pathParts) {
		return false
	}
	for index, patternPart := range patternParts {
		if strings.HasPrefix(patternPart, "{") && strings.HasSuffix(patternPart, "}") {
			if pathParts[index] == "" {
				return false
			}
			continue
		}
		if patternPart != pathParts[index] {
			return false
		}
	}
	return true
}

func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func (handler *Handler) exchangeBootstrap(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	if origin := request.Header.Get("Origin"); origin != "" {
		if origin != strings.TrimSpace(origin) {
			writeAuthProblem(writer, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeCSRF, false, errors.New("request origin is not allowed")))
			return
		}
		if _, allowed := handler.allowedOrigins[origin]; !allowed {
			writeAuthProblem(writer, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeCSRF, false, errors.New("request origin is not allowed")))
			return
		}
	}
	authorization := request.Header.Values("Authorization")
	if len(authorization) != 1 {
		writeAuthProblem(writer, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeUnauthorized, false, errors.New("exactly one bootstrap authorization header is required")))
		return
	}
	bootstrap, ok := bearerToken(authorization[0])
	if !ok {
		writeAuthProblem(writer, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeUnauthorized, false, errors.New("bootstrap bearer token is required")))
		return
	}
	if rejectUnexpectedBody(writer, request) {
		return
	}
	credential, err := handler.service.ExchangeBootstrap(request.Context(), bootstrap)
	if err != nil {
		writeAuthProblem(writer, err)
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: SessionCookieName, Value: credential.Token, Path: "/", HttpOnly: true, Secure: handler.secureCookie,
		SameSite: http.SameSiteStrictMode, Expires: credential.Session.ExpiresAt, MaxAge: maxAge(credential.Session.ExpiresAt, credential.Session.CreatedAt),
	})
	httpapi.WriteJSON(writer, http.StatusCreated, map[string]any{
		"session_id": credential.Session.ID, "csrf_token": credential.CSRFToken, "expires_at": credential.Session.ExpiresAt,
	})
}

func (handler *Handler) currentSession(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	principal, ok := PrincipalFromContext(request.Context())
	if !ok || principal.Kind != domain.PrincipalSession {
		writeAuthProblem(writer, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeForbidden, false, errors.New("browser session is required")))
		return
	}
	cookie, err := request.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		writeAuthProblem(writer, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeUnauthorized, false, errors.New("session cookie is missing")))
		return
	}
	info, err := handler.service.CurrentSession(request.Context(), cookie.Value, request.Header.Get(CSRFHeaderName), false)
	if err != nil {
		writeAuthProblem(writer, err)
		return
	}
	httpapi.WriteJSON(writer, http.StatusOK, info)
}

func (handler *Handler) rotateSession(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	principal, ok := PrincipalFromContext(request.Context())
	if !ok || principal.Kind != domain.PrincipalSession {
		writeAuthProblem(writer, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeForbidden, false, errors.New("browser session is required")))
		return
	}
	cookie, err := request.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		writeAuthProblem(writer, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeUnauthorized, false, errors.New("session cookie is missing")))
		return
	}
	if rejectUnexpectedBody(writer, request) {
		return
	}
	credential, err := handler.service.RotateSession(request.Context(), principal, cookie.Value, request.Header.Get(CSRFHeaderName), true)
	if err != nil {
		writeAuthProblem(writer, err)
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: SessionCookieName, Value: credential.Token, Path: "/", HttpOnly: true, Secure: handler.secureCookie,
		SameSite: http.SameSiteStrictMode, Expires: credential.Session.ExpiresAt, MaxAge: maxAge(credential.Session.ExpiresAt, credential.Session.CreatedAt),
	})
	httpapi.WriteJSON(writer, http.StatusOK, map[string]any{
		"session_id": credential.Session.ID, "csrf_token": credential.CSRFToken, "expires_at": credential.Session.ExpiresAt,
	})
}

func (handler *Handler) revokeCurrentSession(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	principal, ok := PrincipalFromContext(request.Context())
	if !ok || principal.Kind != domain.PrincipalSession {
		writeAuthProblem(writer, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeForbidden, false, errors.New("browser session is required")))
		return
	}
	if rejectUnexpectedBody(writer, request) {
		return
	}
	if err := handler.service.RevokeSession(request.Context(), principal, principal.ID); err != nil {
		writeAuthProblem(writer, err)
		return
	}
	http.SetCookie(writer, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", HttpOnly: true, Secure: handler.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writer.WriteHeader(http.StatusNoContent)
}

type createTokenRequest struct {
	Name             string   `json:"name"`
	Scopes           []string `json:"scopes"`
	ExpiresInSeconds int64    `json:"expires_in_seconds"`
}

func (handler *Handler) createAPIToken(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	principal, ok := PrincipalFromContext(request.Context())
	if !ok || principal.Kind != domain.PrincipalSession {
		writeAuthProblem(writer, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeForbidden, false, errors.New("browser session is required")))
		return
	}
	var input createTokenRequest
	if err := httpapi.DecodeJSON(request, &input); err != nil {
		httpapi.WriteProblem(writer, http.StatusBadRequest, application.ErrorCodeInvalid, "认证请求格式无效", false, nil)
		return
	}
	scopes := make([]capability.Capability, 0, len(input.Scopes))
	for _, raw := range input.Scopes {
		parsed, err := capability.Parse(raw)
		if err != nil || string(parsed) != raw {
			httpapi.WriteProblem(writer, http.StatusBadRequest, application.ErrorCodeInvalid, "Capability 无效", false, nil)
			return
		}
		scopes = append(scopes, parsed)
	}
	if input.ExpiresInSeconds < 0 || input.ExpiresInSeconds > int64((365*24*time.Hour)/time.Second) {
		httpapi.WriteProblem(writer, http.StatusBadRequest, application.ErrorCodeInvalid, "Token 有效期无效", false, nil)
		return
	}
	ttl := time.Duration(input.ExpiresInSeconds) * time.Second
	credential, err := handler.service.CreateAPIToken(request.Context(), principal, input.Name, scopes, ttl)
	if err != nil {
		writeAuthProblem(writer, err)
		return
	}
	httpapi.WriteJSON(writer, http.StatusCreated, map[string]any{
		"id": credential.Token.ID, "name": credential.Token.Name, "scopes": credential.Token.Scopes,
		"expires_at": credential.Token.ExpiresAt, "token": credential.Plain,
	})
}

func (handler *Handler) listAPITokens(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	principal, ok := PrincipalFromContext(request.Context())
	if !ok || principal.Kind != domain.PrincipalSession {
		writeAuthProblem(writer, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeForbidden, false, errors.New("browser session is required")))
		return
	}
	query, err := parseAPITokenListQuery(request)
	if err != nil {
		httpapi.WriteProblem(writer, http.StatusBadRequest, application.ErrorCodeInvalid, "API Token 列表参数无效", false, nil)
		return
	}
	page, err := handler.service.ListAPITokens(request.Context(), principal, query)
	if err != nil {
		writeAuthProblem(writer, err)
		return
	}
	response := apiTokenPageResponse{Items: page.Items}
	if page.HasMore && len(page.Items) > 0 {
		tail := page.Items[len(page.Items)-1]
		payload, marshalErr := json.Marshal(apiTokenListCursor{
			Version: apiTokenCursorVersion, Kind: apiTokenCursorKind, Limit: query.Limit,
			At: tail.CreatedAt.UTC().Format(time.RFC3339Nano), ID: string(tail.ID),
		})
		if marshalErr != nil {
			writeAuthProblem(writer, foundation.NewError(foundation.ErrorDependencyUnavailable, application.ErrorCodeUnavailable, true, marshalErr))
			return
		}
		response.NextCursor = base64.RawURLEncoding.EncodeToString(payload)
	}
	httpapi.WriteJSON(writer, http.StatusOK, response)
}

func parseAPITokenListQuery(request *http.Request) (domain.APITokenListQuery, error) {
	allowed := map[string]struct{}{"cursor": {}, "limit": {}}
	for key, values := range request.URL.Query() {
		if _, ok := allowed[key]; !ok || len(values) != 1 {
			return domain.APITokenListQuery{}, fmt.Errorf("query parameter %q is invalid", key)
		}
	}
	limit := domain.DefaultAPITokenListLimit
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return domain.APITokenListQuery{}, err
		}
		limit = parsed
	}
	query := domain.APITokenListQuery{Limit: limit}
	if raw := strings.TrimSpace(request.URL.Query().Get("cursor")); raw != "" {
		if len(raw) > 2048 {
			return domain.APITokenListQuery{}, errors.New("cursor is too long")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return domain.APITokenListQuery{}, err
		}
		var cursor apiTokenListCursor
		decoder := json.NewDecoder(strings.NewReader(string(decoded)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cursor) != nil || decoder.Decode(&struct{}{}) != io.EOF || cursor.Version != apiTokenCursorVersion || cursor.Kind != apiTokenCursorKind || cursor.Limit != limit {
			return domain.APITokenListQuery{}, errors.New("cursor payload is invalid")
		}
		at, err := time.Parse(time.RFC3339Nano, cursor.At)
		if err != nil {
			return domain.APITokenListQuery{}, err
		}
		id, err := foundation.ParseID(cursor.ID)
		if err != nil {
			return domain.APITokenListQuery{}, err
		}
		query.CursorTime, query.CursorID = &at, id
	}
	if err := domain.ValidateAPITokenListQuery(query); err != nil {
		return domain.APITokenListQuery{}, err
	}
	return query, nil
}

func (handler *Handler) revokeAPIToken(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	principal, ok := PrincipalFromContext(request.Context())
	if !ok || principal.Kind != domain.PrincipalSession {
		writeAuthProblem(writer, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeForbidden, false, errors.New("browser session is required")))
		return
	}
	if rejectUnexpectedBody(writer, request) {
		return
	}
	tokenID, err := foundation.ParseID(chi.URLParam(request, "token_id"))
	if err != nil {
		httpapi.WriteProblem(writer, http.StatusBadRequest, application.ErrorCodeInvalid, "Token ID 无效", false, nil)
		return
	}
	if err := handler.service.RevokeAPIToken(request.Context(), principal, tokenID); err != nil {
		writeAuthProblem(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) authenticate(request *http.Request) (domain.Principal, error) {
	authorization := request.Header.Values("Authorization")
	if len(authorization) > 1 {
		return domain.Principal{}, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeUnauthorized, false, errors.New("multiple authorization headers are not allowed"))
	}
	if len(authorization) == 1 {
		plain, ok := bearerToken(authorization[0])
		if !ok {
			return domain.Principal{}, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeUnauthorized, false, errors.New("authorization header is invalid"))
		}
		return handler.service.AuthenticateAPIToken(request.Context(), plain)
	}
	cookie, err := request.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return domain.Principal{}, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeUnauthorized, false, errors.New("authentication credential is missing"))
	}
	unsafe := request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodOptions
	if unsafe {
		origins := request.Header.Values("Origin")
		if len(origins) != 1 {
			return domain.Principal{}, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeCSRF, false, errors.New("exactly one origin header is required"))
		}
		origin := origins[0]
		if origin != strings.TrimSpace(origin) {
			return domain.Principal{}, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeCSRF, false, errors.New("request origin is not allowed"))
		}
		if _, allowed := handler.allowedOrigins[origin]; !allowed {
			return domain.Principal{}, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeCSRF, false, errors.New("request origin is not allowed"))
		}
	}
	csrfValues := request.Header.Values(CSRFHeaderName)
	if unsafe && len(csrfValues) != 1 {
		return domain.Principal{}, foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeCSRF, false, errors.New("exactly one csrf header is required"))
	}
	csrf := ""
	if len(csrfValues) == 1 {
		csrf = csrfValues[0]
	}
	return handler.service.AuthenticateSession(request.Context(), cookie.Value, csrf, unsafe)
}

func bearerToken(value string) (string, bool) {
	parts := strings.Fields(value)
	returnValue := ""
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		returnValue = parts[1]
	}
	return returnValue, returnValue != "" && !strings.ContainsAny(returnValue, "\r\n")
}

func writeAuthProblem(writer http.ResponseWriter, err error) {
	noStore(writer)
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(writer, http.StatusInternalServerError, application.ErrorCodeUnavailable, "认证服务不可用", false, nil)
		return
	}
	status := httpapi.StatusForErrorKind(classified.Kind)
	message := "认证请求失败"
	if classified.Code == application.ErrorCodeUnauthorized {
		status, message = http.StatusUnauthorized, "需要有效身份凭据"
		writer.Header().Set("WWW-Authenticate", `Bearer realm="zhixu"`)
	} else if classified.Code == application.ErrorCodeForbidden || classified.Code == application.ErrorCodeCSRF {
		status, message = http.StatusForbidden, "当前身份无权执行此操作"
	}
	httpapi.WriteProblem(writer, status, classified.Code, message, classified.Retryable, nil)
}

func noStore(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
}

// rejectUnexpectedBody 强制凭据生命周期端点不接受请求体。
//
// ContentLength 为负数表示未知长度，例如 chunked 请求。此处不探测读取，
// 以免客户端故意不发送正文时占用认证请求处理协程。
func rejectUnexpectedBody(writer http.ResponseWriter, request *http.Request) bool {
	if request.Body == nil || request.ContentLength == 0 {
		return false
	}
	httpapi.WriteProblem(writer, http.StatusBadRequest, application.ErrorCodeInvalid, "认证端点不接受请求体", false, nil)
	return true
}

func maxAge(expiresAt, createdAt time.Time) int {
	seconds := int(expiresAt.Sub(createdAt).Seconds())
	if seconds < 1 {
		return 1
	}
	maxInt := int64(^uint(0) >> 1)
	if int64(seconds) > maxInt {
		return int(maxInt)
	}
	return seconds
}
