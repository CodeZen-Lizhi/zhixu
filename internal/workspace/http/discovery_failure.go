package workspacehttp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

type discoveryFailureService interface {
	ListDiscoveryFailures(context.Context, foundation.ID, string, int) (domain.DiscoveryFailurePage, error)
}

func (h *Handler) listDiscoveryFailures(w http.ResponseWriter, r *http.Request) {
	id, err := foundation.ParseID(r.PathValue("workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	query, err := parseDiscoveryFailureQuery(r)
	if err != nil {
		writeProblem(w, 400, "DISCOVERY_FAILURE_QUERY_INVALID", "扫描记录查询参数无效", false, nil)
		return
	}
	service, ok := h.service.(discoveryFailureService)
	if !ok {
		writeProblem(w, 503, "WORKSPACE_DISCOVERY_FAILURE_STORE_UNAVAILABLE", "扫描记录暂不可用", true, nil)
		return
	}
	page, err := service.ListDiscoveryFailures(r.Context(), id, query.after, query.limit)
	if err != nil {
		writeError(w, err)
		return
	}
	if page.WorkspaceID != id {
		writeProblem(w, 500, "DISCOVERY_FAILURE_RESPONSE_INVALID", "扫描记录响应无效", false, nil)
		return
	}
	writeJSON(w, 200, page)
}

type discoveryQuery struct {
	after string
	limit int
}

func parseDiscoveryFailureQuery(r *http.Request) (discoveryQuery, error) {
	out := discoveryQuery{limit: 50}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return out, err
	}
	for key, values := range query {
		if (key != "cursor" && key != "limit") || len(values) != 1 || values[0] == "" {
			return out, errors.New("invalid query")
		}
	}
	out.after = query.Get("cursor")
	if out.after != "" && !domain.ValidDiscoveryPath(out.after) {
		return out, errors.New("invalid cursor")
	}
	if value := query.Get("limit"); value != "" {
		n, e := strconv.Atoi(value)
		if e != nil || n < 1 || n > 100 {
			return out, errors.New("invalid limit")
		}
		out.limit = n
	}
	return out, nil
}
