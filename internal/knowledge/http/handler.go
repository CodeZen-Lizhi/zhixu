// Package knowledgehttp 提供 Knowledge Timeline 与 Impact Analysis 的只读/草稿 HTTP 契约。
package knowledgehttp

import (
	"context"
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

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	knowledgeapp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/go-chi/chi/v5"
)

const (
	maxBodyBytes                    = 4 * 1024
	maxIdempotencyKeyBytes          = 128
	maxCursorBytes                  = 4096
	errorCodeHTTPUnavailable        = "KNOWLEDGE_HTTP_UNAVAILABLE"
	errorCodeHTTPResultInconsistent = "KNOWLEDGE_HTTP_RESULT_INCONSISTENT"
)

// TimelineService 是 HTTP Timeline 查询所需的最小 Application 契约。
type TimelineService interface {
	List(context.Context, knowledgeapp.TimelineListRequest) (knowledgeapp.TimelinePage, error)
	Get(context.Context, foundation.ID, foundation.ID) (domain.KnowledgeEvent, error)
}

// ImpactService 是 HTTP Impact Analysis 所需的最小 Application 契约。
type ImpactService interface {
	Analyze(context.Context, knowledgeapp.ImpactAnalysisRequest) (knowledgeapp.ImpactAnalysisResult, error)
	GetReport(context.Context, foundation.ID, foundation.ID) (domain.ImpactReport, error)
}

// Handler 将 Timeline 与 Impact Application 映射为严格 JSON 契约。
type Handler struct {
	timeline TimelineService
	impact   ImpactService
	timeout  time.Duration
}

// NewHandler 构造 Knowledge HTTP Handler；缺失依赖的对应路由会 fail closed。
func NewHandler(timeline TimelineService, impact ImpactService, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Handler{timeline: timeline, impact: impact, timeout: timeout}
}

// Routes 在 /api/v1 下注册 Timeline 与 Impact 路由，不注册 Knowledge Event 写接口。
func (handler *Handler) Routes(router chi.Router) {
	router.Get("/workspaces/{workspace_id}/timeline", handler.listTimeline)
	router.Get("/workspaces/{workspace_id}/timeline/{event_id}", handler.getTimelineEvent)
	router.Post("/workspaces/{workspace_id}/timeline/{event_id}/impact-analysis", handler.analyzeImpact)
	router.Get("/workspaces/{workspace_id}/impact-reports/{report_id}", handler.getImpactReport)
}

// Available 报告 Timeline 与 Impact 两组公开查询是否都已组装。
func (handler *Handler) Available() bool {
	return handler != nil && !nilDependency(handler.timeline) && !nilDependency(handler.impact)
}

func (handler *Handler) listTimeline(w http.ResponseWriter, r *http.Request) {
	if handler == nil || nilDependency(handler.timeline) {
		writeUnavailable(w)
		return
	}
	workspaceID, err := parseID(chi.URLParam(r, "workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	request, err := parseTimelineListRequest(r, workspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	page, err := handler.timeline.List(ctx, request)
	if err != nil {
		writeError(w, err)
		return
	}
	if page.WorkspaceID != workspaceID || len(page.Items) > requestLimit(request.Limit) || page.HasMore != (page.NextCursor != "") {
		writeError(w, resultInconsistent("timeline page crossed request binding"))
		return
	}
	items := make([]knowledgeEventResponse, 0, len(page.Items))
	for _, event := range page.Items {
		if event.WorkspaceID != workspaceID || event.Validate() != nil {
			writeError(w, resultInconsistent("timeline event projection is invalid"))
			return
		}
		items = append(items, toKnowledgeEventResponse(event))
	}
	response := timelineListResponse{WorkspaceID: string(workspaceID), Items: items}
	if page.NextCursor != "" {
		response.NextCursor = &page.NextCursor
	}
	httpapi.WriteJSON(w, http.StatusOK, response)
}

func (handler *Handler) getTimelineEvent(w http.ResponseWriter, r *http.Request) {
	if handler == nil || nilDependency(handler.timeline) {
		writeUnavailable(w)
		return
	}
	workspaceID, eventID, err := parsePathIDs(r, "event_id")
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, timelineInvalid("query parameters are not allowed"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	event, err := handler.timeline.Get(ctx, workspaceID, eventID)
	if err != nil {
		writeError(w, err)
		return
	}
	if event.WorkspaceID != workspaceID || event.ID != eventID || event.Validate() != nil {
		writeError(w, resultInconsistent("timeline event crossed request binding"))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toKnowledgeEventResponse(event))
}

func (handler *Handler) analyzeImpact(w http.ResponseWriter, r *http.Request) {
	if handler == nil || nilDependency(handler.impact) {
		writeUnavailable(w)
		return
	}
	workspaceID, eventID, err := parsePathIDs(r, "event_id")
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, impactInvalid("query parameters are not allowed"))
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := decodeEmptyObject(r); err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.impact.Analyze(ctx, knowledgeapp.ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: eventID, IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	if result.Report.WorkspaceID != workspaceID || result.Report.SourceEventID != eventID || domain.ValidateImpactReport(result.Report) != nil {
		writeError(w, resultInconsistent("impact report crossed request binding"))
		return
	}
	for _, draft := range result.ProposalDrafts {
		if draft.WorkspaceID != workspaceID || draft.SourceEventID != eventID || domain.ValidateProposalDraft(draft) != nil {
			writeError(w, resultInconsistent("impact proposal draft crossed request binding"))
			return
		}
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, impactAnalysisResponse{Report: toImpactReportResponse(result.Report), ProposalDrafts: result.ProposalDrafts, Replayed: result.Replayed})
}

func (handler *Handler) getImpactReport(w http.ResponseWriter, r *http.Request) {
	if handler == nil || nilDependency(handler.impact) {
		writeUnavailable(w)
		return
	}
	workspaceID, reportID, err := parsePathIDs(r, "report_id")
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, impactInvalid("query parameters are not allowed"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	report, err := handler.impact.GetReport(ctx, workspaceID, reportID)
	if err != nil {
		writeError(w, err)
		return
	}
	if report.WorkspaceID != workspaceID || report.ID != reportID || domain.ValidateImpactReport(report) != nil {
		writeError(w, resultInconsistent("impact report crossed request binding"))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toImpactReportResponse(report))
}

func parseTimelineListRequest(r *http.Request, workspaceID foundation.ID) (knowledgeapp.TimelineListRequest, error) {
	values, err := parseQuery(r, "event_type", "aggregate_type", "aggregate_id", "source_event_ref", "occurred_after", "occurred_before", "limit", "cursor")
	if err != nil {
		return knowledgeapp.TimelineListRequest{}, err
	}
	request := knowledgeapp.TimelineListRequest{WorkspaceID: workspaceID}
	if raw := single(values, "limit"); raw != "" {
		request.Limit, err = strconv.Atoi(raw)
		if err != nil || request.Limit < 1 || request.Limit > domain.MaxTimelineLimit {
			return knowledgeapp.TimelineListRequest{}, timelineInvalid("timeline limit is invalid")
		}
	}
	request.Cursor = single(values, "cursor")
	if request.Cursor != "" && (len(request.Cursor) > maxCursorBytes || !utf8.ValidString(request.Cursor) || strings.TrimSpace(request.Cursor) != request.Cursor) {
		return knowledgeapp.TimelineListRequest{}, timelineInvalid("timeline cursor is invalid")
	}
	for _, raw := range values["event_type"] {
		request.Filter.EventTypes = append(request.Filter.EventTypes, domain.EventType(raw))
	}
	request.Filter.AggregateType = domain.TimelineAggregateType(single(values, "aggregate_type"))
	if raw := single(values, "aggregate_id"); raw != "" {
		id, parseErr := parseID(raw)
		if parseErr != nil {
			return knowledgeapp.TimelineListRequest{}, parseErr
		}
		request.Filter.AggregateID = &id
	}
	request.Filter.SourceEventRef = single(values, "source_event_ref")
	if request.Filter.OccurredAfter, err = parseOptionalTime(single(values, "occurred_after")); err != nil {
		return knowledgeapp.TimelineListRequest{}, err
	}
	if request.Filter.OccurredBefore, err = parseOptionalTime(single(values, "occurred_before")); err != nil {
		return knowledgeapp.TimelineListRequest{}, err
	}
	return request, nil
}

func parseQuery(r *http.Request, allowed ...string) (url.Values, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, timelineInvalid("timeline query is invalid")
	}
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := set[key]; !ok || len(entries) == 0 {
			return nil, timelineInvalid("timeline query parameter is not allowed")
		}
		if key != "event_type" && len(entries) != 1 {
			return nil, timelineInvalid("timeline query parameter must occur once")
		}
		for _, entry := range entries {
			if entry == "" || strings.TrimSpace(entry) != entry {
				return nil, timelineInvalid("timeline query parameter is invalid")
			}
		}
	}
	return values, nil
}

func parsePathIDs(r *http.Request, resourceParam string) (foundation.ID, foundation.ID, error) {
	workspaceID, err := parseID(chi.URLParam(r, "workspace_id"))
	if err != nil {
		return "", "", err
	}
	resourceID, err := parseID(chi.URLParam(r, resourceParam))
	return workspaceID, resourceID, err
}

func parseID(raw string) (foundation.ID, error) {
	parsed, err := foundation.ParseID(raw)
	if err != nil || raw == "" || string(parsed) != raw {
		return "", timelineInvalid("knowledge resource id is invalid")
	}
	return parsed, nil
}

func parseOptionalTime(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, timelineInvalid("timeline time filter is invalid")
	}
	value = value.UTC()
	return &value, nil
}

func parseIdempotencyKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || !utf8.ValidString(values[0]) {
		return "", impactInvalid("exactly one Idempotency-Key is required")
	}
	value := values[0]
	if value == "" || len(value) > maxIdempotencyKeyBytes || strings.TrimSpace(value) != value {
		return "", impactInvalid("Idempotency-Key is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", impactInvalid("Idempotency-Key is invalid")
		}
	}
	return value, nil
}

func decodeEmptyObject(r *http.Request) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("request requires application/json"))
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxBodyBytes {
		return foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, errors.New("request JSON is empty or oversized"))
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxBodyBytes
	decoded, err := strictjson.DecodeObject[map[string]json.RawMessage](body, limits, nil)
	if err != nil || len(decoded) != 0 {
		return foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, errors.New("impact analysis body must be an empty object"))
	}
	return nil
}

func requestLimit(limit int) int {
	if limit == 0 {
		return knowledgeapp.DefaultTimelineLimit
	}
	return limit
}

func single(values url.Values, key string) string {
	if entries := values[key]; len(entries) == 1 {
		return entries[0]
	}
	return ""
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

func timelineInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeTimelineInvalid, false, errors.New(message))
}

func impactInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeImpactInvalid, false, errors.New(message))
}

func resultInconsistent(message string) error {
	return foundation.NewError(foundation.ErrorNonRetryableFailure, errorCodeHTTPResultInconsistent, false, errors.New(message))
}

func writeUnavailable(w http.ResponseWriter) {
	httpapi.WriteProblem(w, http.StatusServiceUnavailable, errorCodeHTTPUnavailable, "Knowledge Timeline/Impact 服务暂不可用", true, nil)
}

func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, domain.ErrorCodeTimelineUnavailable, "Knowledge 请求超时", true, nil)
		return
	}
	if errors.Is(err, context.Canceled) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, domain.ErrorCodeTimelineUnavailable, "Knowledge 请求已取消", false, nil)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Knowledge 请求失败", false, nil)
		return
	}
	status := httpapi.StatusForErrorKind(classified.Kind)
	message := "Knowledge 请求未完成"
	switch classified.Code {
	case "UNSUPPORTED_MEDIA_TYPE":
		status, message = http.StatusUnsupportedMediaType, "请求必须使用 application/json"
	case "INVALID_JSON":
		status, message = http.StatusBadRequest, "请求 JSON 无效"
	case domain.ErrorCodeTimelineNotFound, domain.ErrorCodeImpactNotFound:
		status, message = http.StatusNotFound, "请求的 Timeline/Impact 资源不存在"
	case domain.ErrorCodeTimelineUnavailable, domain.ErrorCodeImpactUnavailable, errorCodeHTTPUnavailable:
		status, message = http.StatusServiceUnavailable, "Knowledge Timeline/Impact 依赖暂不可用"
	case domain.ErrorCodeTimelineInconsistent, errorCodeHTTPResultInconsistent:
		status, message = http.StatusInternalServerError, "Knowledge Timeline/Impact 响应校验失败"
	case domain.ErrorCodeImpactConflict:
		status, message = http.StatusConflict, "Impact 报告或 Proposal 版本冲突"
	case domain.ErrorCodeTimelineInvalid, domain.ErrorCodeTimelineCursorInvalid, domain.ErrorCodeImpactInvalid:
		status, message = http.StatusBadRequest, "Knowledge Timeline/Impact 请求参数无效"
	}
	httpapi.WriteProblem(w, status, classified.Code, message, classified.Retryable, nil)
}

type timelineListResponse struct {
	WorkspaceID string                   `json:"workspace_id"`
	Items       []knowledgeEventResponse `json:"items"`
	NextCursor  *string                  `json:"next_cursor,omitempty"`
}

type knowledgeEventResponse struct {
	ID             string                       `json:"id"`
	WorkspaceID    string                       `json:"workspace_id"`
	EventType      domain.EventType             `json:"event_type"`
	AggregateType  domain.TimelineAggregateType `json:"aggregate_type"`
	AggregateID    *string                      `json:"aggregate_id,omitempty"`
	SourceEventRef string                       `json:"source_event_ref"`
	SourceRef      string                       `json:"source_ref"`
	EventVersion   int                          `json:"event_version"`
	SchemaVersion  string                       `json:"schema_version"`
	Summary        string                       `json:"summary"`
	Payload        json.RawMessage              `json:"payload"`
	Correlation    domain.EventCorrelation      `json:"correlation"`
	OccurredAt     string                       `json:"occurred_at"`
	CreatedAt      string                       `json:"created_at"`
}

type impactAnalysisResponse struct {
	Report         impactReportResponse   `json:"report"`
	ProposalDrafts []domain.ProposalDraft `json:"proposal_drafts"`
	Replayed       bool                   `json:"replayed"`
}

type impactReportResponse struct {
	ID             string                    `json:"id"`
	WorkspaceID    string                    `json:"workspace_id"`
	SourceEventID  string                    `json:"source_event_id"`
	SourceEventRef string                    `json:"source_event_ref"`
	SourceVersion  int64                     `json:"source_event_version"`
	Status         domain.ImpactReportStatus `json:"status"`
	Objects        []domain.ImpactObject     `json:"objects"`
	Summary        map[string]int            `json:"summary"`
	Fingerprint    string                    `json:"fingerprint"`
	ErrorCode      *string                   `json:"error_code,omitempty"`
	StaleReason    *string                   `json:"stale_reason,omitempty"`
	SchemaVersion  string                    `json:"schema_version"`
	GeneratedAt    string                    `json:"generated_at"`
	CreatedAt      string                    `json:"created_at"`
	Version        int64                     `json:"version"`
}

func toKnowledgeEventResponse(event domain.KnowledgeEvent) knowledgeEventResponse {
	response := knowledgeEventResponse{
		ID: string(event.ID), WorkspaceID: string(event.WorkspaceID), EventType: event.EventType, AggregateType: event.AggregateType,
		SourceEventRef: event.SourceEventRef, SourceRef: event.SourceRef, EventVersion: event.EventVersion, SchemaVersion: event.SchemaVersion,
		Summary: event.Summary, Payload: append(json.RawMessage(nil), event.Payload...), Correlation: event.Correlation,
		OccurredAt: event.OccurredAt.UTC().Format(time.RFC3339Nano), CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if event.AggregateID != nil {
		value := string(*event.AggregateID)
		response.AggregateID = &value
	}
	return response
}

func toImpactReportResponse(report domain.ImpactReport) impactReportResponse {
	response := impactReportResponse{
		ID: string(report.ID), WorkspaceID: string(report.WorkspaceID), SourceEventID: string(report.SourceEventID), SourceEventRef: report.SourceEventRef,
		SourceVersion: report.SourceVersion, Status: report.Status, Objects: append([]domain.ImpactObject(nil), report.Objects...), Summary: report.Summary,
		Fingerprint: report.Fingerprint, SchemaVersion: report.SchemaVersion(), GeneratedAt: report.GeneratedAt.UTC().Format(time.RFC3339Nano),
		CreatedAt: report.CreatedAt.UTC().Format(time.RFC3339Nano), Version: report.Version,
	}
	if report.ErrorCode != "" {
		value := report.ErrorCode
		response.ErrorCode = &value
	}
	if report.StaleReason != "" {
		value := report.StaleReason
		response.StaleReason = &value
	}
	return response
}

var (
	_ TimelineService = (*knowledgeapp.TimelineService)(nil)
	_ ImpactService   = (*knowledgeapp.ImpactService)(nil)
)
