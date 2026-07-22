// Package retrievalhttp 暴露 Retrieval Search 与可打开 Evidence 的 HTTP 查询边界。
package retrievalhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/go-chi/chi/v5"
)

const (
	defaultSearchPageLimit  = int32(20)
	searchUnavailableCode   = "RETRIEVAL_SEARCH_SERVICE_UNAVAILABLE"
	evidenceUnavailableCode = "RETRIEVAL_EVIDENCE_REFERENCE_SERVICE_UNAVAILABLE"
)

// SearchService 是 Handler 依赖的最小检索查询接口。
type SearchService interface {
	// Search 返回当前 Active Index 的有界可解释 Evidence。
	Search(context.Context, domain.SearchRequest) (domain.SearchResult, error)
}

// EvidenceReferenceService 是 Handler 依赖的 Source Version/Span 只读接口。
type EvidenceReferenceService interface {
	// GetSourceVersion 返回指定 Workspace 的 Source Version 引用。
	GetSourceVersion(context.Context, foundation.ID, foundation.ID) (domain.SourceVersionReference, error)
	// GetSourceSpan 返回经不可变 Artifact 复核的 Source Span。
	GetSourceSpan(context.Context, foundation.ID, foundation.ID, foundation.ID) (application.SourceSpanView, error)
}

// Handler 将 Retrieval Application 映射到稳定 HTTP/OpenAPI 契约。
type Handler struct {
	search   SearchService
	evidence EvidenceReferenceService
	cursors  *CursorCodec
}

// NewHandler 创建 Retrieval HTTP Handler；缺失依赖保留路由并显式返回 503。
func NewHandler(search SearchService, evidence EvidenceReferenceService, cursors *CursorCodec) *Handler {
	return &Handler{search: search, evidence: evidence, cursors: cursors}
}

// Routes 在既有 `/api/v1` Router 下注册 Retrieval 查询路由。
func (handler *Handler) Routes(router chi.Router) {
	router.Post("/search", handler.handleSearch)
	router.Get("/workspaces/{workspace_id}/source-versions/{source_version_id}", handler.handleSourceVersion)
	router.Get("/workspaces/{workspace_id}/source-versions/{source_version_id}/spans/{source_span_id}", handler.handleSourceSpan)
}

type searchRequest struct {
	WorkspaceID   optionalString       `json:"workspace_id"`
	Query         optionalString       `json:"query"`
	RetrievalMode optionalString       `json:"retrieval_mode,omitempty"`
	Filters       optionalSearchFilter `json:"filters,omitempty"`
	Cursor        optionalString       `json:"cursor,omitempty"`
	Limit         optionalInt32        `json:"limit,omitempty"`
}

type searchFilterRequest struct {
	SourceIDs        optionalStringSlice `json:"source_ids,omitempty"`
	SourceVersionIDs optionalStringSlice `json:"source_version_ids,omitempty"`
	PathPrefixes     optionalStringSlice `json:"path_prefixes,omitempty"`
	CapturedAtFrom   optionalString      `json:"captured_at_from,omitempty"`
	CapturedAtBefore optionalString      `json:"captured_at_before,omitempty"`
}

type optionalString struct {
	Value   string
	present bool
	null    bool
	invalid bool
}

func (value *optionalString) UnmarshalJSON(data []byte) error {
	value.present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		value.null = true
		return nil
	}
	if err := json.Unmarshal(data, &value.Value); err != nil {
		value.invalid = true
	}
	return nil
}

type optionalInt32 struct {
	Value   int32
	present bool
	null    bool
	invalid bool
}

func (value *optionalInt32) UnmarshalJSON(data []byte) error {
	value.present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		value.null = true
		return nil
	}
	if err := json.Unmarshal(data, &value.Value); err != nil {
		value.invalid = true
	}
	return nil
}

type optionalStringSlice struct {
	Value   []string
	present bool
	null    bool
	invalid bool
}

func (value *optionalStringSlice) UnmarshalJSON(data []byte) error {
	value.present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		value.null = true
		return nil
	}
	if err := json.Unmarshal(data, &value.Value); err != nil {
		value.invalid = true
	}
	return nil
}

type optionalSearchFilter struct {
	Value   searchFilterRequest
	present bool
	null    bool
	invalid bool
}

func (value *optionalSearchFilter) UnmarshalJSON(data []byte) error {
	value.present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		value.null = true
		return nil
	}
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		value.invalid = true
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(&value.Value)
}

type searchResponse struct {
	WorkspaceID               string                      `json:"workspace_id"`
	IndexVersionID            string                      `json:"index_version_id"`
	EmbeddingVersionID        *string                     `json:"embedding_version_id"`
	RequestedMode             string                      `json:"requested_mode"`
	EffectiveMode             string                      `json:"effective_mode"`
	IndexDegradedCapabilities []string                    `json:"index_degraded_capabilities"`
	Degradations              []searchDegradationResponse `json:"degradations"`
	Items                     []evidenceResponse          `json:"items"`
	NextCursor                string                      `json:"next_cursor,omitempty"`
}

type searchDegradationResponse struct {
	Capability string `json:"capability"`
	ErrorCode  string `json:"error_code"`
	Retryable  bool   `json:"retryable"`
}

type evidenceResponse struct {
	ChunkID             string                       `json:"chunk_id"`
	ParseProjectionID   string                       `json:"parse_projection_id"`
	Sequence            int32                        `json:"sequence"`
	ContentHash         string                       `json:"content_hash"`
	HeadingPath         []string                     `json:"heading_path"`
	Span                evidenceSpanResponse         `json:"span"`
	Snippet             string                       `json:"snippet"`
	Provenances         []evidenceProvenanceResponse `json:"provenances"`
	ProvenanceTruncated bool                         `json:"provenance_truncated"`
	Scores              evidenceScoresResponse       `json:"scores"`
}

type evidenceSpanResponse struct {
	SpanID    string `json:"span_id"`
	StartLine int32  `json:"start_line"`
	EndLine   int32  `json:"end_line"`
	StartByte int64  `json:"start_byte"`
	EndByte   int64  `json:"end_byte"`
}

type evidenceProvenanceResponse struct {
	SourceID          string `json:"source_id"`
	SourceVersionID   string `json:"source_version_id"`
	RelativePath      string `json:"relative_path"`
	CapturedAt        string `json:"captured_at"`
	SourceVersionHref string `json:"source_version_href"`
	SourceSpanHref    string `json:"source_span_href"`
}

type stageScoreResponse struct {
	Rank  int32   `json:"rank"`
	Score float64 `json:"score"`
}

type lexicalScoreResponse struct {
	Rank         int32   `json:"rank"`
	Score        float64 `json:"score"`
	FTSScore     float64 `json:"fts_score"`
	TrigramScore float64 `json:"trigram_score"`
}

type vectorDistanceResponse struct {
	Rank     int32   `json:"rank"`
	Distance float64 `json:"distance"`
}

type rerankScoreResponse struct {
	Rank         int32   `json:"rank"`
	Score        float64 `json:"score"`
	ModelVersion string  `json:"model_version"`
}

type evidenceScoresResponse struct {
	Lexical *lexicalScoreResponse   `json:"lexical"`
	Vector  *vectorDistanceResponse `json:"vector"`
	Fusion  stageScoreResponse      `json:"fusion"`
	Rerank  *rerankScoreResponse    `json:"rerank"`
}

type sourceVersionResponse struct {
	WorkspaceID     string `json:"workspace_id"`
	SourceID        string `json:"source_id"`
	SourceVersionID string `json:"source_version_id"`
	SourceType      string `json:"source_type"`
	LogicalName     string `json:"logical_name"`
	RelativePath    string `json:"relative_path"`
	ContentHash     string `json:"content_hash"`
	ByteSize        int64  `json:"byte_size"`
	MediaType       string `json:"media_type"`
	SecurityStatus  string `json:"security_status"`
	IngestionStatus string `json:"ingestion_status,omitempty"`
	WorkflowStatus  string `json:"workflow_status,omitempty"`
	IndexStatus     string `json:"index_status,omitempty"`
	CapturedAt      string `json:"captured_at"`
}

type sourceSpanResponse struct {
	SourceVersion     sourceVersionResponse `json:"source_version"`
	ParseProjectionID string                `json:"parse_projection_id"`
	SpanID            string                `json:"span_id"`
	SpanType          string                `json:"span_type"`
	StartLine         int32                 `json:"start_line"`
	EndLine           int32                 `json:"end_line"`
	StartByte         int64                 `json:"start_byte"`
	EndByte           int64                 `json:"end_byte"`
	Selector          json.RawMessage       `json:"selector"`
	ExcerptHash       string                `json:"excerpt_hash"`
	ParserVersion     string                `json:"parser_version"`
	SchemaVersion     string                `json:"schema_version"`
	Excerpt           string                `json:"excerpt"`
	ExcerptTruncated  bool                  `json:"excerpt_truncated"`
}

func (handler *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	if handler == nil || handler.search == nil || handler.cursors == nil {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, searchUnavailableCode, "Retrieval Search 服务暂不可用", false, nil)
		return
	}
	var wire searchRequest
	if err := httpapi.DecodeJSON(r, &wire); err != nil {
		httpapi.WriteProblem(w, http.StatusBadRequest, "INVALID_JSON", "请求 JSON 无效", false, nil)
		return
	}
	request, pageLimit, cursor, err := decodeSearchRequest(wire)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := handler.cursors.ValidateRequestCursor(request, pageLimit, cursor); err != nil {
		writeError(w, err)
		return
	}
	result, err := handler.search.Search(r.Context(), request)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := handler.cursors.Paginate(request, result, pageLimit, cursor)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toSearchResponse(page))
}

func (handler *Handler) handleSourceVersion(w http.ResponseWriter, r *http.Request) {
	if handler == nil || handler.evidence == nil {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, evidenceUnavailableCode, "Evidence Reference 服务暂不可用", false, nil)
		return
	}
	workspaceID, sourceVersionID, err := parseEvidenceRouteIDs(chi.URLParam(r, "workspace_id"), chi.URLParam(r, "source_version_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	reference, err := handler.evidence.GetSourceVersion(r.Context(), workspaceID, sourceVersionID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toSourceVersionResponse(reference))
}

func (handler *Handler) handleSourceSpan(w http.ResponseWriter, r *http.Request) {
	if handler == nil || handler.evidence == nil {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, evidenceUnavailableCode, "Evidence Reference 服务暂不可用", false, nil)
		return
	}
	workspaceID, sourceVersionID, err := parseEvidenceRouteIDs(chi.URLParam(r, "workspace_id"), chi.URLParam(r, "source_version_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	spanID, err := parseRouteID(chi.URLParam(r, "source_span_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	view, err := handler.evidence.GetSourceSpan(r.Context(), workspaceID, sourceVersionID, spanID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toSourceSpanResponse(view))
}

func decodeSearchRequest(wire searchRequest) (domain.SearchRequest, int32, string, error) {
	if !wire.WorkspaceID.present || wire.WorkspaceID.null || wire.WorkspaceID.invalid {
		return domain.SearchRequest{}, 0, "", searchRequestInvalid("search workspace identity is invalid")
	}
	if !wire.Query.present || wire.Query.null || wire.Query.invalid {
		return domain.SearchRequest{}, 0, "", searchRequestInvalid("search query is invalid")
	}
	workspaceID, err := parseSearchID(wire.WorkspaceID.Value)
	if err != nil {
		return domain.SearchRequest{}, 0, "", err
	}
	mode := domain.SearchModeHybrid
	if wire.RetrievalMode.present {
		modeText := strings.TrimSpace(wire.RetrievalMode.Value)
		if wire.RetrievalMode.null || wire.RetrievalMode.invalid || modeText == "" || wire.RetrievalMode.Value != modeText {
			return domain.SearchRequest{}, 0, "", searchRequestInvalid("search retrieval mode is invalid")
		}
		mode = domain.SearchMode(modeText)
	}
	cursor := ""
	if wire.Cursor.present {
		if wire.Cursor.null || wire.Cursor.invalid || wire.Cursor.Value == "" {
			return domain.SearchRequest{}, 0, "", foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeSearchCursorInvalid, false, errors.New("search cursor is invalid"))
		}
		cursor = wire.Cursor.Value
	}
	pageLimit := defaultSearchPageLimit
	if wire.Limit.present {
		if wire.Limit.null || wire.Limit.invalid {
			return domain.SearchRequest{}, 0, "", searchRequestInvalid("search page limit is invalid")
		}
		pageLimit = wire.Limit.Value
	}
	filter := domain.SearchFilter{}
	if wire.Filters.present {
		if wire.Filters.null || wire.Filters.invalid {
			return domain.SearchRequest{}, 0, "", searchRequestInvalid("search filters are invalid")
		}
		filterWire := wire.Filters.Value
		if filterWire.SourceIDs.null || filterWire.SourceIDs.invalid ||
			filterWire.SourceVersionIDs.null || filterWire.SourceVersionIDs.invalid ||
			filterWire.PathPrefixes.null || filterWire.PathPrefixes.invalid ||
			filterWire.CapturedAtFrom.null || filterWire.CapturedAtFrom.invalid ||
			filterWire.CapturedAtBefore.null || filterWire.CapturedAtBefore.invalid {
			return domain.SearchRequest{}, 0, "", searchRequestInvalid("search filters are invalid")
		}
		filter.SourceIDs, err = parseIDList(filterWire.SourceIDs.Value)
		if err != nil {
			return domain.SearchRequest{}, 0, "", err
		}
		filter.SourceVersionIDs, err = parseIDList(filterWire.SourceVersionIDs.Value)
		if err != nil {
			return domain.SearchRequest{}, 0, "", err
		}
		filter.PathPrefixes = append([]string(nil), filterWire.PathPrefixes.Value...)
		filter.CapturedAtFrom, err = parseSearchTime(filterWire.CapturedAtFrom)
		if err != nil {
			return domain.SearchRequest{}, 0, "", err
		}
		filter.CapturedAtBefore, err = parseSearchTime(filterWire.CapturedAtBefore)
		if err != nil {
			return domain.SearchRequest{}, 0, "", err
		}
	}
	if pageLimit <= 0 || pageLimit > domain.MaxSearchLimit {
		return domain.SearchRequest{}, 0, "", searchRequestInvalid("search page limit is invalid")
	}
	request, err := domain.CanonicalizeSearchRequest(domain.SearchRequest{
		WorkspaceID: workspaceID, Query: wire.Query.Value, Mode: mode, Filter: filter, Limit: domain.MaxSearchLimit,
	})
	if err != nil {
		return domain.SearchRequest{}, 0, "", err
	}
	return request, pageLimit, cursor, nil
}

func searchRequestInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeSearchRequestInvalid, false, errors.New(message))
}

func parseSearchID(value string) (foundation.ID, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := foundation.ParseID(trimmed)
	if err != nil || value != trimmed || string(parsed) != value {
		return "", foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeSearchRequestInvalid, false, errors.New("search workspace identity is invalid"))
	}
	return parsed, nil
}

func parseIDList(values []string) ([]foundation.ID, error) {
	ids := make([]foundation.ID, len(values))
	for index, value := range values {
		parsed, err := parseSearchID(value)
		if err != nil {
			return nil, err
		}
		ids[index] = parsed
	}
	return ids, nil
}

func parseEvidenceRouteIDs(workspace, version string) (foundation.ID, foundation.ID, error) {
	workspaceID, err := parseRouteID(workspace)
	if err != nil {
		return "", "", err
	}
	versionID, err := parseRouteID(version)
	if err != nil {
		return "", "", err
	}
	return workspaceID, versionID, nil
}

func parseRouteID(value string) (foundation.ID, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := foundation.ParseID(trimmed)
	if err != nil || value != trimmed || string(parsed) != value {
		return "", foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("request identity is invalid"))
	}
	return parsed, nil
}

func parseSearchTime(value optionalString) (*time.Time, error) {
	if !value.present {
		return nil, nil
	}
	if strings.TrimSpace(value.Value) != value.Value {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeSearchRequestInvalid, false, errors.New("search filter time is invalid"))
	}
	parsed, err := time.Parse(time.RFC3339, value.Value)
	if err != nil {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeSearchRequestInvalid, false, errors.New("search filter time is invalid"))
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func toSearchResponse(page SearchPage) searchResponse {
	result := page.Result
	response := searchResponse{
		WorkspaceID: string(result.WorkspaceID), IndexVersionID: string(result.IndexVersionID),
		RequestedMode: string(result.RequestedMode), EffectiveMode: string(result.EffectiveMode),
		IndexDegradedCapabilities: make([]string, len(result.IndexDegradedCapabilities)),
		Degradations:              make([]searchDegradationResponse, len(result.Degradations)),
		Items:                     make([]evidenceResponse, len(result.Items)), NextCursor: page.NextCursor,
	}
	if result.EmbeddingVersionID != nil {
		value := string(*result.EmbeddingVersionID)
		response.EmbeddingVersionID = &value
	}
	for index, capability := range result.IndexDegradedCapabilities {
		response.IndexDegradedCapabilities[index] = string(capability)
	}
	for index, degradation := range result.Degradations {
		response.Degradations[index] = searchDegradationResponse{
			Capability: string(degradation.Capability), ErrorCode: degradation.Code, Retryable: degradation.Retryable,
		}
	}
	for index, item := range result.Items {
		response.Items[index] = toEvidenceResponse(result.WorkspaceID, item)
	}
	return response
}

func toEvidenceResponse(workspaceID foundation.ID, item domain.EvidenceV1) evidenceResponse {
	response := evidenceResponse{
		ChunkID: string(item.ChunkID), ParseProjectionID: string(item.ParseProjectionID), Sequence: item.Sequence,
		ContentHash: item.ContentHash, HeadingPath: append([]string{}, item.HeadingPath...),
		Span:    evidenceSpanResponse{SpanID: string(item.Span.ID), StartLine: item.Span.StartLine, EndLine: item.Span.EndLine, StartByte: item.Span.StartByte, EndByte: item.Span.EndByte},
		Snippet: item.Snippet, Provenances: make([]evidenceProvenanceResponse, len(item.Provenances)),
		ProvenanceTruncated: item.ProvenanceTruncated,
		Scores:              evidenceScoresResponse{Fusion: stageScoreResponse{Rank: item.Fusion.Rank, Score: item.Fusion.Score}},
	}
	for index, provenance := range item.Provenances {
		versionHref := sourceVersionHref(workspaceID, provenance.SourceVersionID)
		response.Provenances[index] = evidenceProvenanceResponse{
			SourceID: string(provenance.SourceID), SourceVersionID: string(provenance.SourceVersionID),
			RelativePath: provenance.RelativePath, CapturedAt: provenance.CapturedAt.UTC().Format(time.RFC3339Nano),
			SourceVersionHref: versionHref, SourceSpanHref: versionHref + "/spans/" + url.PathEscape(string(item.Span.ID)),
		}
	}
	if item.Lexical != nil && item.LexicalFTSScore != nil && item.LexicalTrigramScore != nil {
		response.Scores.Lexical = &lexicalScoreResponse{Rank: item.Lexical.Rank, Score: item.Lexical.Score, FTSScore: *item.LexicalFTSScore, TrigramScore: *item.LexicalTrigramScore}
	}
	if item.Vector != nil {
		response.Scores.Vector = &vectorDistanceResponse{Rank: item.Vector.Rank, Distance: item.Vector.Score}
	}
	if item.Rerank != nil {
		response.Scores.Rerank = &rerankScoreResponse{Rank: item.Rerank.Rank, Score: item.Rerank.Score, ModelVersion: item.RerankModelVersion}
	}
	return response
}

func sourceVersionHref(workspaceID, sourceVersionID foundation.ID) string {
	return "/api/v1/workspaces/" + url.PathEscape(string(workspaceID)) + "/source-versions/" + url.PathEscape(string(sourceVersionID))
}

func toSourceVersionResponse(reference domain.SourceVersionReference) sourceVersionResponse {
	return sourceVersionResponse{
		WorkspaceID: string(reference.WorkspaceID), SourceID: string(reference.SourceID), SourceVersionID: string(reference.SourceVersionID),
		SourceType: reference.SourceType, LogicalName: reference.LogicalName, RelativePath: reference.RelativePath,
		ContentHash: reference.ContentHash, ByteSize: reference.ByteSize, MediaType: reference.MediaType,
		SecurityStatus: reference.SecurityStatus, IngestionStatus: reference.IngestionStatus,
		WorkflowStatus: reference.WorkflowStatus, IndexStatus: reference.IndexStatus,
		CapturedAt: reference.CapturedAt.UTC().Format(time.RFC3339Nano),
	}
}

func toSourceSpanResponse(view application.SourceSpanView) sourceSpanResponse {
	reference := view.Reference
	return sourceSpanResponse{
		SourceVersion: toSourceVersionResponse(reference.SourceVersion), ParseProjectionID: string(reference.ParseProjectionID),
		SpanID: string(reference.Span.ID), SpanType: reference.SpanType, StartLine: reference.Span.StartLine,
		EndLine: reference.Span.EndLine, StartByte: reference.Span.StartByte, EndByte: reference.Span.EndByte,
		Selector: append(json.RawMessage(nil), reference.Selector...), ExcerptHash: reference.ExcerptHash,
		ParserVersion: reference.ParserVersion, SchemaVersion: reference.SchemaVersion,
		Excerpt: view.Excerpt, ExcerptTruncated: view.ExcerptTruncated,
	}
}

func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, "RETRIEVAL_SEARCH_CANCELLED", "请求已取消", false, nil)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "服务处理失败", false, nil)
		return
	}
	status := httpapi.StatusForErrorKind(classified.Kind)
	if classified.Kind == foundation.ErrorNonRetryableFailure {
		status = http.StatusServiceUnavailable
	}
	httpapi.WriteProblem(w, status, classified.Code, publicMessage(classified.Code), classified.Retryable, nil)
}

func publicMessage(code string) string {
	if strings.TrimSpace(code) == "" {
		return "请求处理失败"
	}
	return "请求未完成：" + code
}

var _ SearchService = (*application.SearchService)(nil)
var _ EvidenceReferenceService = (*application.EvidenceReferenceService)(nil)
