package organizinghttp

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/gin-gonic/gin"
)

type AnchorHandler struct {
	service app.AnchorService
	timeout time.Duration
}

func NewAnchorHandler(service app.AnchorService, timeout time.Duration) *AnchorHandler {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &AnchorHandler{service: service, timeout: timeout}
}
func (h *AnchorHandler) Routes(router gin.IRouter) {
	h.recommendationRoutes(router)
	base := "/workspaces/:workspace_id/synthesis/anchors"
	router.GET(base, httpapi.GinHandler(h.list))
	router.POST(base, httpapi.GinHandler(h.create))
	router.GET(base+"/:anchor_id", httpapi.GinHandler(h.get))
	router.GET(base+"/:anchor_id/fusion-requests", httpapi.GinHandler(h.listFusion))
	router.GET(base+"/:anchor_id/scope-proposals", httpapi.GinHandler(h.listProposals(domain.AnchorScopeAdjustment)))
	router.GET(base+"/:anchor_id/associations", httpapi.GinHandler(h.listProposals(domain.AnchorSourceAssociation)))
	router.POST(base+"/:anchor_id/scope-proposals/decisions", httpapi.GinHandler(h.decide(domain.AnchorScopeAdjustment)))
	router.POST(base+"/:anchor_id/associations/decisions", httpapi.GinHandler(h.decide(domain.AnchorSourceAssociation)))
}
func (h *AnchorHandler) available(w http.ResponseWriter) bool {
	if h == nil || nilDependency(h.service) {
		writeProblem(w, http.StatusServiceUnavailable, "ANCHOR_UNAVAILABLE", "锚点服务暂不可用", true)
		return false
	}
	return true
}
func (h *AnchorHandler) create(w http.ResponseWriter, r *http.Request) {
	workspace, key, ok := commandRoute(w, r)
	if !ok {
		return
	}
	input, e := decodeJSON[app.CreateAnchorCommand](r, 32768, 2048, 64)
	if e != nil {
		writeError(w, e)
		return
	}
	input.WorkspaceID = workspace
	input.IdempotencyKey = key
	if e := input.Validate(); e != nil {
		writeError(w, e)
		return
	}
	if !h.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, e := h.service.CreateAnchor(ctx, input)
	if e != nil {
		writeError(w, e)
		return
	}
	if result.Anchor.Validate() != nil || result.Anchor.WorkspaceID != workspace || result.Anchor.NoteID != input.NoteID || result.Anchor.BasisRevisionID != input.BasisRevisionID {
		writeError(w, synthesisInvalidResult())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
func (h *AnchorHandler) get(w http.ResponseWriter, r *http.Request) {
	workspace, id, ok := queryRoute(w, r, "anchor_id")
	if !ok || !h.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, e := h.service.GetAnchor(ctx, workspace, id)
	if e != nil {
		writeError(w, e)
		return
	}
	if result.Validate() != nil || result.WorkspaceID != workspace || result.ID != id {
		writeError(w, synthesisInvalidResult())
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Anchor domain.Anchor `json:"anchor"`
	}{result})
}
func parseAnchorList(r *http.Request, allowNote bool) (app.AnchorListQuery, error) {
	var q app.AnchorListQuery
	id, e := parseID(r.PathValue("workspace_id"))
	if e != nil {
		return q, e
	}
	q.WorkspaceID = id
	q.Limit = 20
	values, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return q, requestInvalid("anchor query is invalid")
	}
	for k, v := range values {
		if len(v) != 1 || v[0] == "" || (k != "limit" && k != "after_id" && !(allowNote && k == "note_id")) {
			return q, requestInvalid("anchor query is invalid")
		}
	}
	if raw := values.Get("limit"); raw != "" {
		q.Limit, e = strconv.Atoi(raw)
		if e != nil || q.Limit < 1 || q.Limit > 100 || strconv.Itoa(q.Limit) != raw {
			return q, requestInvalid("anchor limit is invalid")
		}
	}
	if raw := values.Get("after_id"); raw != "" {
		q.AfterID, e = parseID(raw)
		if e != nil {
			return q, e
		}
	}
	if raw := values.Get("note_id"); raw != "" {
		q.NoteID, e = parseID(raw)
		if e != nil {
			return q, e
		}
	}
	return q, nil
}
func (h *AnchorHandler) list(w http.ResponseWriter, r *http.Request) {
	q, e := parseAnchorList(r, true)
	if e != nil {
		writeError(w, e)
		return
	}
	if !h.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, e := h.service.ListAnchors(ctx, q)
	if e != nil {
		writeError(w, e)
		return
	}
	if result.Items == nil || len(result.Items) > q.Limit {
		writeError(w, synthesisInvalidResult())
		return
	}
	last := q.AfterID
	for _, a := range result.Items {
		if a.Validate() != nil || a.WorkspaceID != q.WorkspaceID || q.NoteID != "" && a.NoteID != q.NoteID || a.ID <= last {
			writeError(w, synthesisInvalidResult())
			return
		}
		last = a.ID
	}
	if !validAnchorNext(result.NextAfterID, last, len(result.Items), q.Limit) {
		writeError(w, synthesisInvalidResult())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func validAnchorNext(next *foundation.ID, last foundation.ID, count, limit int) bool {
	return next == nil || count == limit && *next == last
}
func (h *AnchorHandler) listProposals(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, e := parseAnchorList(r, false)
		if e != nil {
			writeError(w, e)
			return
		}
		id, e := parseID(r.PathValue("anchor_id"))
		if e != nil {
			writeError(w, e)
			return
		}
		if !h.available(w) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
		defer cancel()
		result, e := h.service.ListAnchorProposals(ctx, app.AnchorProposalQuery{WorkspaceID: q.WorkspaceID, AnchorID: id, Kind: kind, AfterID: q.AfterID, Limit: q.Limit})
		if e != nil {
			writeError(w, e)
			return
		}
		if result.Items == nil || len(result.Items) > q.Limit {
			writeError(w, synthesisInvalidResult())
			return
		}
		last := q.AfterID
		for _, p := range result.Items {
			if p.Validate(q.WorkspaceID) != nil || p.AnchorID != id || p.Kind != kind || p.ID <= last {
				writeError(w, synthesisInvalidResult())
				return
			}
			last = p.ID
		}
		if !validAnchorNext(result.NextAfterID, last, len(result.Items), q.Limit) {
			writeError(w, synthesisInvalidResult())
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}
func (h *AnchorHandler) decide(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		workspace, id, key, ok := identifiedCommandRoute(w, r, "anchor_id")
		if !ok {
			return
		}
		input, e := decodeJSON[app.DecideAnchorCommand](r, 16384, 512, 32)
		if e != nil {
			writeError(w, e)
			return
		}
		input.WorkspaceID = workspace
		input.AnchorID = id
		input.IdempotencyKey = key
		input.Kind = kind
		if e := input.Validate(); e != nil {
			writeError(w, e)
			return
		}
		if !h.available(w) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
		defer cancel()
		result, e := h.service.DecideAnchor(ctx, input)
		if e != nil {
			writeError(w, e)
			return
		}
		if result.Anchor.Validate() != nil || result.Anchor.WorkspaceID != workspace || result.Anchor.ID != id || len(result.Items) != len(input.Items) {
			writeError(w, synthesisInvalidResult())
			return
		}
		for i, p := range result.Items {
			if p.Validate(workspace) != nil || p.AnchorID != id || p.ID != input.Items[i].ProposalID || p.Status != input.Decision || p.Kind != kind {
				writeError(w, synthesisInvalidResult())
				return
			}
		}
		writeJSON(w, http.StatusOK, result)
	}
}
