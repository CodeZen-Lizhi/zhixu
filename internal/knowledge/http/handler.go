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
	"github.com/gin-gonic/gin"
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
func (handler *Handler) Routes(router gin.IRouter) {
	router.GET("/workspaces/:workspace_id/timeline", httpapi.GinHandler(handler.listTimeline))
	router.GET("/workspaces/:workspace_id/timeline/:event_id", httpapi.GinHandler(handler.getTimelineEvent))
	router.POST("/workspaces/:workspace_id/timeline/:event_id/impact-analysis", httpapi.GinHandler(handler.analyzeImpact))
	router.GET("/workspaces/:workspace_id/impact-reports/:report_id", httpapi.GinHandler(handler.getImpactReport))
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
	workspaceID, err := parseID(r.PathValue("workspace_id"))
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
	items := make([]any, 0, len(page.Items))
	for _, event := range page.Items {
		if event.WorkspaceID != workspaceID || event.Validate() != nil {
			writeError(w, resultInconsistent("timeline event projection is invalid"))
			return
		}
		response, responseErr := toKnowledgeEventResponse(event)
		if responseErr != nil {
			writeError(w, resultInconsistent("timeline event wire is invalid"))
			return
		}
		items = append(items, response)
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
	response, responseErr := toKnowledgeEventResponse(event)
	if responseErr != nil {
		writeError(w, resultInconsistent("timeline event wire is invalid"))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, response)
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
	if result.Report.WorkspaceID != workspaceID || result.Report.SourceEventID != eventID || validateImpactReportWire(result.Report) != nil {
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
	reportResponse, responseErr := toImpactReportResponse(result.Report)
	if responseErr != nil {
		writeError(w, resultInconsistent("impact report wire is invalid"))
		return
	}
	httpapi.WriteJSON(w, status, impactAnalysisResponse{Report: reportResponse, ProposalDrafts: result.ProposalDrafts, Replayed: result.Replayed})
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
	if report.WorkspaceID != workspaceID || report.ID != reportID || validateImpactReportWire(report) != nil {
		writeError(w, resultInconsistent("impact report crossed request binding"))
		return
	}
	response, responseErr := toImpactReportResponse(report)
	if responseErr != nil {
		writeError(w, resultInconsistent("impact report wire is invalid"))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, response)
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
	workspaceID, err := parseID(r.PathValue("workspace_id"))
	if err != nil {
		return "", "", err
	}
	resourceID, err := parseID(r.PathValue(resourceParam))
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
	WorkspaceID string  `json:"workspace_id"`
	Items       []any   `json:"items"`
	NextCursor  *string `json:"next_cursor,omitempty"`
}

// knowledgeEventResponse 是 knowledge-event/v1 的精确 wire 字段集合。
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
	Correlation    eventCorrelationResponse     `json:"correlation"`
	OccurredAt     string                       `json:"occurred_at"`
	CreatedAt      string                       `json:"created_at"`
}

// knowledgeEventV2Response 只为 knowledge-event/v2 追加受控的操作者和 owner snapshot。
type knowledgeEventV2Response struct {
	knowledgeEventResponse
	Operator     eventOperatorResponse      `json:"operator"`
	OwnerBinding *eventOwnerBindingResponse `json:"owner_binding"`
}

type eventOperatorResponse struct {
	Type domain.EventOperatorType `json:"type"`
	ID   *string                  `json:"id,omitempty"`
}

type eventCorrelationResponse struct {
	ProposalID    *string `json:"proposal_id,omitempty"`
	ApprovalID    *string `json:"approval_id,omitempty"`
	WorkflowRunID *string `json:"workflow_run_id,omitempty"`
	AuditEventID  *string `json:"audit_event_id,omitempty"`
	GitCommitRef  string  `json:"git_commit_ref,omitempty"`
}

type artifactImpactBindingResponse struct {
	ArtifactID      string `json:"artifact_id"`
	ArtifactVersion int64  `json:"artifact_version"`
	RevisionID      string `json:"revision_id"`
	RevisionNo      int64  `json:"revision_no"`
	ContentHash     string `json:"content_hash"`
}

type reviewCardImpactBindingResponse struct {
	CardID                     string `json:"card_id"`
	CardVersion                int64  `json:"card_version"`
	Status                     string `json:"status"`
	Fingerprint                string `json:"fingerprint"`
	ClaimID                    string `json:"claim_id"`
	EvidenceBindingFingerprint string `json:"evidence_binding_fingerprint"`
}

// eventOwnerBindingResponse 是 Timeline v2 owner binding 的严格判别联合。
type eventOwnerBindingResponse struct {
	Artifact   *artifactImpactBindingResponse   `json:"artifact,omitempty"`
	ReviewCard *reviewCardImpactBindingResponse `json:"review_card,omitempty"`
}

type impactAnalysisResponse struct {
	Report         any                    `json:"report"`
	ProposalDrafts []domain.ProposalDraft `json:"proposal_drafts"`
	Replayed       bool                   `json:"replayed"`
}

// impactReportResponse 是 impact-report/v1 的精确 wire 字段集合。
type impactReportResponse struct {
	ID             string                    `json:"id"`
	WorkspaceID    string                    `json:"workspace_id"`
	SourceEventID  string                    `json:"source_event_id"`
	SourceEventRef string                    `json:"source_event_ref"`
	SourceVersion  int64                     `json:"source_event_version"`
	Status         domain.ImpactReportStatus `json:"status"`
	Objects        []impactObjectResponse    `json:"objects"`
	Summary        map[string]int            `json:"summary"`
	Fingerprint    string                    `json:"fingerprint"`
	ErrorCode      *string                   `json:"error_code,omitempty"`
	StaleReason    *string                   `json:"stale_reason,omitempty"`
	SchemaVersion  string                    `json:"schema_version"`
	GeneratedAt    string                    `json:"generated_at"`
	CreatedAt      string                    `json:"created_at"`
	Version        int64                     `json:"version"`
}

// impactReportV2Response 只为 impact-report/v2 追加分析策略和 supersession 关系。
type impactReportV2Response struct {
	impactReportResponse
	AnalysisVersion      domain.ImpactAnalysisVersion `json:"analysis_version"`
	SupersedesReportID   *string                      `json:"supersedes_report_id"`
	SupersededByReportID *string                      `json:"superseded_by_report_id"`
}

type impactObjectResponse struct {
	Type              domain.ImpactObjectType          `json:"type"`
	ID                string                           `json:"id"`
	WorkspaceID       string                           `json:"workspace_id"`
	Version           int64                            `json:"version"`
	Action            domain.ImpactAction              `json:"action"`
	Reason            string                           `json:"reason"`
	RequiresProposal  bool                             `json:"requires_proposal"`
	ArtifactBinding   *artifactImpactBindingResponse   `json:"artifact_binding,omitempty"`
	ReviewCardBinding *reviewCardImpactBindingResponse `json:"review_card_binding,omitempty"`
}

func toKnowledgeEventResponse(event domain.KnowledgeEvent) (any, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	response := knowledgeEventResponse{
		ID: string(event.ID), WorkspaceID: string(event.WorkspaceID), EventType: event.EventType, AggregateType: event.AggregateType,
		SourceEventRef: event.SourceEventRef, SourceRef: event.SourceRef, EventVersion: event.EventVersion, SchemaVersion: event.SchemaVersion,
		Summary: event.Summary, Payload: append(json.RawMessage(nil), event.Payload...), Correlation: toEventCorrelationResponse(event.Correlation),
		OccurredAt: event.OccurredAt.UTC().Format(time.RFC3339Nano), CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if event.AggregateID != nil {
		value := string(*event.AggregateID)
		response.AggregateID = &value
	}
	switch event.SchemaVersion {
	case domain.KnowledgeEventSchemaVersion:
		return response, nil
	case domain.KnowledgeEventSchemaVersionV2:
		if event.Operator == nil {
			return nil, errors.New("knowledge event v2 operator is missing")
		}
		binding, err := toEventOwnerBindingResponse(event.OwnerBinding)
		if err != nil {
			return nil, err
		}
		return knowledgeEventV2Response{
			knowledgeEventResponse: response,
			Operator:               toEventOperatorResponse(*event.Operator),
			OwnerBinding:           binding,
		}, nil
	default:
		return nil, errors.New("knowledge event schema is unsupported")
	}
}

func toImpactReportResponse(report domain.ImpactReport) (any, error) {
	if err := validateImpactReportWire(report); err != nil {
		return nil, err
	}
	objects, err := toImpactObjectResponses(report.Objects)
	if err != nil {
		return nil, err
	}
	response := impactReportResponse{
		ID: string(report.ID), WorkspaceID: string(report.WorkspaceID), SourceEventID: string(report.SourceEventID), SourceEventRef: report.SourceEventRef,
		SourceVersion: report.SourceVersion, Status: report.Status, Objects: objects, Summary: copyImpactSummary(report.Summary),
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
	if report.EffectiveAnalysisVersion() == domain.ImpactAnalysisVersionV1 {
		return response, nil
	}
	return impactReportV2Response{
		impactReportResponse: response,
		AnalysisVersion:      report.EffectiveAnalysisVersion(),
		SupersedesReportID:   responseID(report.SupersedesReportID),
		SupersededByReportID: responseID(report.SupersededByReportID),
	}, nil
}

func toEventOperatorResponse(operator domain.EventOperator) eventOperatorResponse {
	return eventOperatorResponse{Type: operator.Type, ID: responseID(operator.ID)}
}

func toEventCorrelationResponse(correlation domain.EventCorrelation) eventCorrelationResponse {
	return eventCorrelationResponse{
		ProposalID: responseID(correlation.ProposalID), ApprovalID: responseID(correlation.ApprovalID),
		WorkflowRunID: responseID(correlation.WorkflowRunID), AuditEventID: responseID(correlation.AuditEventID),
		GitCommitRef: correlation.GitCommitRef,
	}
}

func toEventOwnerBindingResponse(binding *domain.EventOwnerBinding) (*eventOwnerBindingResponse, error) {
	if binding == nil {
		return nil, nil
	}
	if (binding.Artifact == nil && binding.ReviewCard == nil) || (binding.Artifact != nil && binding.ReviewCard != nil) {
		return nil, errors.New("timeline owner binding union is invalid")
	}
	response := &eventOwnerBindingResponse{}
	if binding.Artifact != nil {
		response.Artifact = toArtifactImpactBindingResponse(*binding.Artifact)
	}
	if binding.ReviewCard != nil {
		response.ReviewCard = toReviewCardImpactBindingResponse(*binding.ReviewCard)
	}
	return response, nil
}

func toImpactObjectResponses(objects []domain.ImpactObject) ([]impactObjectResponse, error) {
	responses := make([]impactObjectResponse, 0, len(objects))
	for _, object := range objects {
		if err := domain.ValidateImpactObject(object); err != nil {
			return nil, err
		}
		response := impactObjectResponse{
			Type: object.Type, ID: string(object.ID), WorkspaceID: string(object.WorkspaceID), Version: object.Version,
			Action: object.Action, Reason: object.Reason, RequiresProposal: object.RequiresProposal,
		}
		if object.ArtifactBinding != nil {
			response.ArtifactBinding = toArtifactImpactBindingResponse(*object.ArtifactBinding)
		}
		if object.ReviewCardBinding != nil {
			response.ReviewCardBinding = toReviewCardImpactBindingResponse(*object.ReviewCardBinding)
		}
		responses = append(responses, response)
	}
	return responses, nil
}

func toArtifactImpactBindingResponse(binding domain.ArtifactImpactBinding) *artifactImpactBindingResponse {
	return &artifactImpactBindingResponse{
		ArtifactID: string(binding.ArtifactID), ArtifactVersion: binding.ArtifactVersion, RevisionID: string(binding.RevisionID),
		RevisionNo: binding.RevisionNo, ContentHash: binding.ContentHash,
	}
}

func toReviewCardImpactBindingResponse(binding domain.ReviewCardImpactBinding) *reviewCardImpactBindingResponse {
	return &reviewCardImpactBindingResponse{
		CardID: string(binding.CardID), CardVersion: binding.CardVersion, Status: binding.Status, Fingerprint: binding.Fingerprint,
		ClaimID: string(binding.ClaimID), EvidenceBindingFingerprint: binding.EvidenceBindingFingerprint,
	}
}

func responseID(value *foundation.ID) *string {
	if value == nil {
		return nil
	}
	result := string(*value)
	return &result
}

func copyImpactSummary(summary map[string]int) map[string]int {
	result := make(map[string]int, len(summary))
	for key, value := range summary {
		result[key] = value
	}
	return result
}

func validateImpactReportWire(report domain.ImpactReport) error {
	if err := domain.ValidateImpactReport(report); err != nil {
		return err
	}
	fingerprint, err := domain.ComputeImpactFingerprintForVersion(
		report.EffectiveAnalysisVersion(), report.SourceEventID, report.SourceVersion, report.Objects,
	)
	if err != nil {
		return err
	}
	if fingerprint != report.Fingerprint {
		return errors.New("impact report fingerprint is inconsistent")
	}
	return nil
}

var (
	_ TimelineService = (*knowledgeapp.TimelineService)(nil)
	_ ImpactService   = (*knowledgeapp.ImpactService)(nil)
)
