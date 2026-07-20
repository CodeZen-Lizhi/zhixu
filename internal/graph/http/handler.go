// Package graphhttp exposes the Graph read projection over HTTP.
package graphhttp

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

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/go-chi/chi/v5"
)

const maxGraphRequestBytes = 64 * 1024

// Service 是 Graph Handler 所需的最小 Application 查询边界。
type Service interface {
	GlobalPage(context.Context, graphapp.GlobalPageRequest) (graphdomain.GlobalPage, error)
	SearchNodes(context.Context, graphdomain.NodeSearchRequest) (graphdomain.NodeSearchResult, error)
	NeighborhoodPage(context.Context, graphapp.NeighborhoodPageRequest) (graphdomain.Neighborhood, error)
	FindPath(context.Context, graphdomain.PathRequest) (graphdomain.PathResult, error)
	NodeDetail(context.Context, foundation.ID, knowledge.NodeRef) (graphdomain.GraphNode, error)
	RelationDetail(context.Context, foundation.ID, foundation.ID) (graphdomain.RelationDetail, error)
	RelationEvidencePage(context.Context, graphapp.RelationEvidencePageRequest) (graphdomain.RelationEvidencePage, error)
}

// Handler 将 Graph Application 映射为严格 JSON/OpenAPI 查询契约。
type Handler struct {
	service Service
	timeout time.Duration
}

// NewHandler 创建 Graph HTTP Handler；缺失 Service 时保留路由并 fail closed。
func NewHandler(service Service, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Handler{service: service, timeout: timeout}
}

// Routes 在 `/api/v1` 下注册 Graph 只读路由。
func (handler *Handler) Routes(router chi.Router) {
	router.Post("/graph/global", handler.handleGlobal)
	router.Post("/graph/neighborhood", handler.handleNeighborhood)
	router.Post("/graph/path", handler.handlePath)
	router.Get("/graph/nodes", handler.handleNodeSearch)
	router.Get("/graph/nodes/{node_type}/{node_id}", handler.handleNodeDetail)
	router.Get("/graph/relations/{relation_id}", handler.handleRelationDetail)
	router.Get("/graph/relations/{relation_id}/evidence", handler.handleRelationEvidence)
}

// Available 报告 Handler 是否持有可调用的真实 Graph Application Service。
func (handler *Handler) Available() bool {
	if handler == nil || handler.service == nil {
		return false
	}
	value := reflect.ValueOf(handler.service)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func (handler *Handler) handleGlobal(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	wire, ok := decodeGraphJSON[globalRequest](w, r)
	if !ok {
		return
	}
	request, cursor, err := wire.toDomain()
	if err != nil {
		writeGraphError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.GlobalPage(ctx, graphapp.GlobalPageRequest{Request: request, Cursor: cursor})
	if err != nil {
		writeGraphError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toGlobalResponse(result))
}

func (handler *Handler) handleNeighborhood(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	wire, ok := decodeGraphJSON[neighborhoodRequest](w, r)
	if !ok {
		return
	}
	request, cursor, err := wire.toDomain()
	if err != nil {
		writeGraphError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.NeighborhoodPage(ctx, graphapp.NeighborhoodPageRequest{Request: request, Cursor: cursor})
	if err != nil {
		writeGraphError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toNeighborhoodResponse(result))
}

func (handler *Handler) handlePath(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	wire, ok := decodeGraphJSON[pathRequest](w, r)
	if !ok {
		return
	}
	request, err := wire.toDomain()
	if err != nil {
		writeGraphError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.FindPath(ctx, request)
	if err != nil {
		writeGraphError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toPathResponse(result))
}

func (handler *Handler) handleNodeSearch(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	query, ok := parseQueryParameters(w, r, "workspace_id", "query", "limit")
	if !ok {
		return
	}
	request, err := parseNodeSearchRequest(query)
	if err != nil {
		writeGraphError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.SearchNodes(ctx, request)
	if err != nil {
		writeGraphError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toNodeSearchResponse(result))
}

func (handler *Handler) handleNodeDetail(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	query, ok := parseQueryParameters(w, r, "workspace_id")
	if !ok {
		return
	}
	workspaceID, err := parseID(query.Get("workspace_id"))
	if err != nil {
		writeGraphError(w, err)
		return
	}
	ref, err := parseNodeRef(nodeRefRequest{Type: chi.URLParam(r, "node_type"), ID: chi.URLParam(r, "node_id")})
	if err != nil {
		writeGraphError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.NodeDetail(ctx, workspaceID, ref)
	if err != nil {
		writeGraphError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toNodeResponse(result))
}

func (handler *Handler) handleRelationDetail(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	query, ok := parseQueryParameters(w, r, "workspace_id")
	if !ok {
		return
	}
	workspaceID, relationID, err := parseTwoIDs(query.Get("workspace_id"), chi.URLParam(r, "relation_id"))
	if err != nil {
		writeGraphError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.RelationDetail(ctx, workspaceID, relationID)
	if err != nil {
		writeGraphError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toRelationDetailResponse(result))
}

func (handler *Handler) handleRelationEvidence(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	query, ok := parseQueryParameters(w, r, "workspace_id", "limit", "cursor")
	if !ok {
		return
	}
	workspaceID, relationID, err := parseTwoIDs(query.Get("workspace_id"), chi.URLParam(r, "relation_id"))
	if err != nil {
		writeGraphError(w, err)
		return
	}
	limit, err := parseIntegerDefault(query, "limit", graphdomain.DefaultEvidenceLimit)
	if err != nil {
		writeGraphError(w, err)
		return
	}
	cursor, err := parseOptionalQueryString(query, "cursor")
	if err != nil {
		writeGraphError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.RelationEvidencePage(ctx, graphapp.RelationEvidencePageRequest{WorkspaceID: workspaceID, RelationID: relationID, Limit: limit, Cursor: cursor})
	if err != nil {
		writeGraphError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toRelationEvidenceResponse(result))
}

func (handler *Handler) available(w http.ResponseWriter) bool {
	if !handler.Available() {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, graphdomain.ErrorCodeDependencyUnavailable, "Graph 服务暂不可用", false, nil)
		return false
	}
	return true
}

func decodeGraphJSON[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var zero T
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		httpapi.WriteProblem(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "请求必须使用 application/json", false, nil)
		return zero, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxGraphRequestBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxGraphRequestBytes {
		httpapi.WriteProblem(w, http.StatusBadRequest, "INVALID_JSON", "请求 JSON 无效", false, nil)
		return zero, false
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxGraphRequestBytes
	decoded, err := strictjson.DecodeObject[T](body, limits, nil)
	if err != nil {
		httpapi.WriteProblem(w, http.StatusBadRequest, "INVALID_JSON", "请求 JSON 无效", false, nil)
		return zero, false
	}
	return decoded, true
}

func parseQueryParameters(w http.ResponseWriter, r *http.Request, allowed ...string) (url.Values, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		httpapi.WriteProblem(w, http.StatusBadRequest, graphdomain.ErrorCodeRequestInvalid, "Graph 请求参数无效", false, nil)
		return nil, false
	}
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := set[key]; !ok || len(entries) != 1 {
			httpapi.WriteProblem(w, http.StatusBadRequest, graphdomain.ErrorCodeRequestInvalid, "Graph 请求参数无效", false, nil)
			return nil, false
		}
	}
	return values, true
}

func writeGraphError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, graphdomain.ErrorCodeQueryTimeout, "Graph 查询超时", true, nil)
		return
	}
	if errors.Is(err, context.Canceled) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, graphdomain.ErrorCodeQueryCanceled, "Graph 查询已取消", false, nil)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Graph 查询失败", false, nil)
		return
	}
	status := httpapi.StatusForErrorKind(classified.Kind)
	switch classified.Code {
	case graphdomain.ErrorCodeQueryCanceled:
		status = http.StatusServiceUnavailable
	case graphdomain.ErrorCodeQueryBudgetExceeded:
		status = http.StatusUnprocessableEntity
	}
	httpapi.WriteProblem(w, status, classified.Code, "Graph 查询未完成："+classified.Code, classified.Retryable, nil)
}

var _ Service = (*graphapp.Service)(nil)
