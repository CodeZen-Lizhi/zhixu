package organizinghttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/gin-gonic/gin"
)

type sourceReviewHTTP struct {
	reader  app.SynthesisSourceReviewReader
	timeout time.Duration
}

// RegisterSynthesisSourceReviewRoutes 必须注册到已认证的 v1 路由器。
func RegisterSynthesisSourceReviewRoutes(router gin.IRouter, reader app.SynthesisSourceReviewReader, timeout time.Duration) {
	h := sourceReviewHTTP{reader: reader, timeout: timeout}
	router.GET("/workspaces/:workspace_id/synthesis/processing/:processing_id/source-reviews", httpapi.GinHandler(h.list))
	router.GET("/workspaces/:workspace_id/synthesis/notes/:note_id/revisions/:revision_id/source-reviews", httpapi.GinHandler(h.list))
	router.GET("/workspaces/:workspace_id/synthesis/source-reviews/:review_id", httpapi.GinHandler(h.get))
	router.GET("/workspaces/:workspace_id/synthesis/source-reviews/:review_id/evidence/:evidence_id", httpapi.GinHandler(h.open))
}
func (h sourceReviewHTTP) ready(w http.ResponseWriter, r *http.Request) bool {
	principal, ok := authhttp.PrincipalFromContext(r.Context())
	allowed := false
	if ok {
		for _, c := range principal.Scopes {
			if c == capability.ReadLocal {
				allowed = true
			}
		}
	}
	if !allowed {
		writeError(w, foundation.NewError(foundation.ErrorPermissionDenied, "SYNTHESIS_SOURCE_REVIEW_FORBIDDEN", false, errors.New("authenticated read capability required")))
		return false
	}
	if nilDependency(h.reader) {
		writeProblem(w, 503, "SYNTHESIS_SOURCE_REVIEW_UNAVAILABLE", "当前正文补充来源暂不可用", true)
		return false
	}
	return true
}
func sourceReviewIDs(w http.ResponseWriter, r *http.Request, names ...string) ([]foundation.ID, bool) {
	ids := make([]foundation.ID, len(names))
	for i, name := range names {
		id, err := parseID(r.PathValue(name))
		if err != nil {
			writeError(w, err)
			return nil, false
		}
		ids[i] = id
	}
	return ids, true
}
func (h sourceReviewHTTP) list(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	ids, ok := sourceReviewIDs(w, r, "workspace_id")
	if !ok {
		return
	}
	q := app.SynthesisSourceReviewQuery{WorkspaceID: ids[0], Limit: 20}
	names := []string{"note_id", "revision_id"}
	if r.PathValue("processing_id") != "" {
		names = []string{"processing_id"}
	}
	ids, ok = sourceReviewIDs(w, r, names...)
	if !ok {
		return
	}
	if len(ids) == 1 {
		q.ProcessingID = ids[0]
	} else {
		q.NoteID, q.RevisionID = ids[0], ids[1]
	}
	values, err := parseSourceReviewQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if raw := values["limit"]; raw != "" {
		q.Limit, err = strconv.Atoi(raw)
		if err != nil || q.Limit < 1 || q.Limit > 100 {
			writeError(w, requestInvalid("invalid review limit"))
			return
		}
	}
	if raw := values["after_id"]; raw != "" {
		q.AfterID, err = parseID(raw)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	page, err := h.reader.ListSourceReviewViews(ctx, q)
	if err != nil {
		writeError(w, err)
		return
	}
	if page.WorkspaceID != q.WorkspaceID || page.ProcessingID != q.ProcessingID || page.NoteID != q.NoteID || page.RevisionID != q.RevisionID || page.Items == nil || len(page.Items) > q.Limit {
		writeError(w, synthesisInvalidResult())
		return
	}
	previous := q.AfterID
	for i, v := range page.Items {
		v = sourceReviewWire(v)
		page.Items[i] = v
		if !validSourceReviewView(v, q.WorkspaceID) || v.ID <= previous || (q.ProcessingID != "" && v.OriginProcessingID != q.ProcessingID) {
			writeError(w, synthesisInvalidResult())
			return
		}
		for _, t := range v.Targets {
			if q.NoteID != "" && (t.NoteID != q.NoteID || t.BaseRevisionID != q.RevisionID) {
				writeError(w, synthesisInvalidResult())
				return
			}
		}
		previous = v.ID
		if !sourceReviewWriteAllowed(r) {
			page.Items[i].CanRecheck, page.Items[i].CanRecover = false, false
		}
	}
	if page.NextAfterID != "" && (len(page.Items) != q.Limit || page.NextAfterID != previous) {
		writeError(w, synthesisInvalidResult())
		return
	}
	writeJSON(w, 200, page)
}
func parseSourceReviewQuery(r *http.Request) (map[string]string, error) {
	values := map[string]string{}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, requestInvalid("invalid source review query")
	}
	for k, v := range query {
		if (k != "limit" && k != "after_id") || len(v) != 1 || v[0] == "" {
			return nil, requestInvalid("invalid source review query")
		}
		values[k] = v[0]
	}
	return values, nil
}
func (h sourceReviewHTTP) get(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	ids, ok := sourceReviewIDs(w, r, "workspace_id", "review_id")
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, requestInvalid("unexpected query"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	v, err := h.reader.GetSourceReviewView(ctx, ids[0], ids[1])
	if err != nil {
		writeError(w, err)
		return
	}
	v = sourceReviewWire(v)
	if v.ID != ids[1] || !validSourceReviewView(v, ids[0]) {
		writeError(w, synthesisInvalidResult())
		return
	}
	if !sourceReviewWriteAllowed(r) {
		v.CanRecheck, v.CanRecover = false, false
	}
	writeJSON(w, 200, v)
}

func sourceReviewWriteAllowed(r *http.Request) bool {
	principal, ok := authhttp.PrincipalFromContext(r.Context())
	return ok && app.NewSynthesisManuscriptCaller(principal.Scopes).Authorize() == nil
}

func sourceReviewWire(v app.SynthesisSourceReviewView) app.SynthesisSourceReviewView {
	v.CreatedAt = v.CreatedAt.UTC()
	if v.CompletedAt != nil {
		at := v.CompletedAt.UTC()
		v.CompletedAt = &at
	}
	if v.RecoveryCompletedAt != nil {
		at := v.RecoveryCompletedAt.UTC()
		v.RecoveryCompletedAt = &at
	}
	return v
}
func (h sourceReviewHTTP) open(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	ids, ok := sourceReviewIDs(w, r, "workspace_id", "review_id", "evidence_id")
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, requestInvalid("unexpected query"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	e, v, err := h.reader.OpenSourceReviewEvidence(ctx, ids[0], ids[1], ids[2])
	if err != nil {
		writeError(w, err)
		return
	}
	if e.ID != ids[2] || !validSourceReviewObligations(e.Obligations) || e.Source.Validate() != nil || e.Source.Source.WorkspaceID != ids[0] || e.Source != v.Reference || v.Validate(ids[0]) != nil {
		writeError(w, synthesisInvalidResult())
		return
	}
	var text *string
	if v.Availability == domain.MaterialAvailable {
		text = &v.Text
	}
	writeJSON(w, 200, struct {
		WorkspaceID  foundation.ID                         `json:"workspace_id"`
		ReviewID     foundation.ID                         `json:"review_id"`
		Evidence     app.SynthesisSourceReviewEvidenceView `json:"evidence"`
		Availability domain.MaterialAvailability           `json:"availability"`
		Text         *string                               `json:"text"`
		SnapshotText string                                `json:"snapshot_text,omitempty"`
	}{ids[0], ids[1], e, v.Availability, text, v.SnapshotText})
}
func validSourceReviewView(v app.SynthesisSourceReviewView, w foundation.ID) bool {
	if v.WorkspaceID != w || !validResponseID(v.ID) || !validResponseID(v.OriginProcessingID) || !validResponseID(v.OriginWorkflowRunID) || v.Version < 1 || v.AttemptNo < 1 || v.CreatedAt.IsZero() || v.Targets == nil || v.ObligationCount < 0 || v.SupportedCount < 0 || v.SupportedCount > v.ObligationCount {
		return false
	}
	if v.WorkflowRunID != "" && !validResponseID(v.WorkflowRunID) {
		return false
	}
	if v.SupersedesID != "" && (!validResponseID(v.SupersedesID) || v.SupersedesID == v.ID) || v.AttemptNo > 10 || v.CanRecheck && v.CanRecover || (v.CanRecheck || v.CanRecover) && !v.Latest {
		return false
	}
	if (v.RecoveryWorkflowRunID == "") != (v.RecoveryStatus == "") || v.RecoveryWorkflowRunID != "" && !validResponseID(v.RecoveryWorkflowRunID) {
		return false
	}
	switch v.RecoveryStatus {
	case "", "pending", "running", "waiting_for_human", "retry_wait", "paused", "succeeded", "failed", "cancelled":
	default:
		return false
	}
	if v.RecoveryCompletedAt != nil && (v.RecoveryCompletedAt.IsZero() || v.RecoveryWorkflowRunID == "" || !synthesisValidHash(v.ReceiptHash)) {
		return false
	}
	if v.Completed && ((v.Status != "SUCCEEDED" || v.CompletedAt == nil) && v.RecoveryCompletedAt == nil || v.EffectiveStatus != "CURRENT" || !synthesisValidHash(v.ReceiptHash) || v.ObligationCount == 0 || v.SupportedCount != v.ObligationCount) {
		return false
	}
	for _, t := range v.Targets {
		if !validResponseID(t.NoteID) || !validResponseID(t.BaseRevisionID) || (t.TargetKind != "REVISION" && t.TargetKind != "LOCAL_FILE") || sourceReviewHash(t.FullContent) != t.FullContentHash || !utf8.ValidString(t.FullContent) || t.Paragraphs == nil || t.Evidence == nil {
			return false
		}
		labels := map[string]bool{}
		for _, p := range t.Paragraphs {
			if p.StartByte < 0 || p.EndByte <= p.StartByte || p.EndByte > len(t.FullContent) || p.Ordinal < 1 || labels[p.Label] || sourceReviewHash(t.FullContent[p.StartByte:p.EndByte]) != p.Hash || !utf8.ValidString(t.FullContent[p.StartByte:p.EndByte]) {
				return false
			}
			labels[p.Label] = true
		}
		evidenceIDs := map[foundation.ID]bool{}
		for _, e := range t.Evidence {
			if !validResponseID(e.ID) || evidenceIDs[e.ID] || !validSourceReviewObligations(e.Obligations) || !labels[e.Paragraph] || e.Source.Validate() != nil || e.Source.Source.WorkspaceID != w {
				return false
			}
			evidenceIDs[e.ID] = true
		}
	}
	return true
}

func validSourceReviewObligations(labels []string) bool {
	if len(labels) < 1 || len(labels) > 65536 {
		return false
	}
	seen := make(map[string]bool, len(labels))
	for _, label := range labels {
		if len(label) < 1 || len(label) > 32 || seen[label] {
			return false
		}
		for _, char := range label {
			if !(char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '/') {
				return false
			}
		}
		seen[label] = true
	}
	return true
}

func sourceReviewHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func (h *SynthesisHandler) WithSourceReviews(reader app.SynthesisSourceReviewReader) *SynthesisHandler {
	out := *h
	out.sourceReviews = reader
	return &out
}
