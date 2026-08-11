package graphhttp

import (
	"context"
	"errors"
	"io"
	"math"
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
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/gin-gonic/gin"
)

const (
	defaultCandidatePageLimit = 20
	maxCandidateRequestBytes  = 64 * 1024
	maxCandidateQueryBytes    = 16 * 1024
	maxCandidateCursorBytes   = 2048
	maxIdempotencyKeyBytes    = 128

	errorCodeSemanticLinkRequestInvalid        = "SEMANTIC_LINK_REQUEST_INVALID"
	errorCodeSemanticLinkNotFound              = "SEMANTIC_LINK_NOT_FOUND"
	errorCodeSemanticLinkVersionConflict       = "SEMANTIC_LINK_VERSION_CONFLICT"
	errorCodeSemanticLinkCursorInvalid         = "SEMANTIC_LINK_CURSOR_INVALID"
	errorCodeSemanticLinkCursorStale           = "SEMANTIC_LINK_CURSOR_STALE"
	errorCodeSemanticLinkDependencyUnavailable = "SEMANTIC_LINK_DEPENDENCY_UNAVAILABLE"
	errorCodeSemanticLinkResultInvalid         = "SEMANTIC_LINK_RESULT_INVALID"
)

// CandidateHandler 将候选 Application seam 映射为独立的严格 HTTP 契约。
// 它不扩展正式 Graph 七个只读端点，也不参与正式 Graph readiness。
type CandidateHandler struct {
	service graphapp.CandidateService
	scans   graphapp.SemanticLinkScanHTTPService
	timeout time.Duration
}

// NewCandidateHandler 创建候选 HTTP Handler；缺失 Service 时仅候选路由 fail closed。
func NewCandidateHandler(service graphapp.CandidateService, timeout time.Duration, scans ...graphapp.SemanticLinkScanHTTPService) *CandidateHandler {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	handler := &CandidateHandler{service: service, timeout: timeout}
	if len(scans) == 1 && !nilCandidateHTTPDependency(scans[0]) {
		handler.scans = scans[0]
	}
	return handler
}

// Routes 在 `/api/v1` 下注册候选查询、详情、决策和持久 Topic scan 路由。
func (handler *CandidateHandler) Routes(router gin.IRouter) {
	router.GET("/graph/candidates", httpapi.GinHandler(handler.list))
	router.GET("/graph/candidates/:candidate_id", httpapi.GinHandler(handler.detail))
	router.POST("/graph/candidates/:candidate_id/decisions", httpapi.GinHandler(handler.decide))
	router.POST("/graph/candidate-scans", httpapi.GinHandler(handler.startScan))
	router.GET("/graph/candidate-scans/:scan_id", httpapi.GinHandler(handler.getScan))
}

func nilCandidateHTTPDependency(value any) bool {
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

// Available 报告候选 Handler 是否持有可调用的真实 Application Service。
func (handler *CandidateHandler) Available() bool {
	return handler.reviewAvailable() && handler.scanAvailable()
}

func (handler *CandidateHandler) reviewAvailable() bool {
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

func (handler *CandidateHandler) scanAvailable() bool {
	return handler != nil && !nilCandidateHTTPDependency(handler.scans)
}

func (handler *CandidateHandler) list(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	request, err := parseCandidateListRequest(r)
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.List(ctx, request)
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	if err := graphdomain.ValidateSemanticLinkCandidatePage(request.Query, page); err != nil || !validCandidateCursor(page.Meta.NextCursor, true) {
		writeSemanticLinkError(w, semanticLinkResultInvalid(errors.New("candidate page response is inconsistent")))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toCandidatePageResponse(page))
}

func (handler *CandidateHandler) detail(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	query, err := parseCandidateQueryParameters(r, "workspace_id")
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	workspaceID, err := parseCandidateID(singleCandidateQueryValue(query, "workspace_id"))
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	candidateID, err := parseCandidateID(r.PathValue("candidate_id"))
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	candidate, err := handler.service.Get(ctx, workspaceID, candidateID)
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	if err := graphdomain.ValidateSemanticLinkCandidate(candidate); err != nil || candidate.WorkspaceID != workspaceID || candidate.ID != candidateID {
		writeSemanticLinkError(w, semanticLinkResultInvalid(errors.New("candidate detail response is inconsistent")))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toCandidateResponse(candidate))
}

func (handler *CandidateHandler) decide(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	candidateID, err := parseCandidateID(r.PathValue("candidate_id"))
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	idempotencyKey, err := parseCandidateIdempotencyKey(r.Header.Values("Idempotency-Key"))
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	wire, err := decodeCandidateDecisionRequest(r)
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	command, err := wire.toCommand(candidateID, idempotencyKey)
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	receipt, err := handler.service.Decide(ctx, command)
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	if err := graphapp.ValidateCandidateDecisionReceipt(command, receipt); err != nil {
		writeSemanticLinkError(w, semanticLinkResultInvalid(err))
		return
	}
	status := http.StatusCreated
	if receipt.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, toCandidateDecisionReceiptResponse(receipt))
}

func (handler *CandidateHandler) available(w http.ResponseWriter) bool {
	if !handler.reviewAvailable() {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, errorCodeSemanticLinkDependencyUnavailable, "Semantic Link 服务暂不可用", true, nil)
		return false
	}
	return true
}

type candidateDecisionRequest struct {
	WorkspaceID     optional[string] `json:"workspace_id"`
	Action          optional[string] `json:"action"`
	ExpectedVersion optional[int64]  `json:"expected_version"`
	Reason          optional[string] `json:"reason"`
	RelationType    optional[string] `json:"relation_type"`
	DeferredUntil   optional[string] `json:"deferred_until"`
}

func (request candidateDecisionRequest) toCommand(candidateID foundation.ID, idempotencyKey string) (graphapp.SemanticLinkCandidateDecisionCommand, error) {
	if !request.WorkspaceID.Present || request.WorkspaceID.Null || !request.Action.Present || request.Action.Null ||
		!request.ExpectedVersion.Present || request.ExpectedVersion.Null || request.ExpectedVersion.Value < 1 ||
		invalidOptional(request.Reason) || invalidOptional(request.RelationType) {
		return graphapp.SemanticLinkCandidateDecisionCommand{}, semanticLinkRequestInvalid(errors.New("candidate decision required fields are invalid"))
	}
	workspaceID, err := parseCandidateID(request.WorkspaceID.Value)
	if err != nil {
		return graphapp.SemanticLinkCandidateDecisionCommand{}, err
	}
	action, err := parseCandidateDecisionAction(request.Action.Value)
	if err != nil {
		return graphapp.SemanticLinkCandidateDecisionCommand{}, err
	}
	decision := graphdomain.SemanticLinkCandidateDecision{Action: action}
	if request.Reason.Present {
		normalized, normalizeErr := knowledge.NormalizeReason(request.Reason.Value, true)
		if normalizeErr != nil {
			return graphapp.SemanticLinkCandidateDecisionCommand{}, semanticLinkRequestInvalid(normalizeErr)
		}
		decision.Reason = normalized
	}
	if request.RelationType.Present {
		relationType, relationErr := parseCandidateRelationType(request.RelationType.Value)
		if relationErr != nil {
			return graphapp.SemanticLinkCandidateDecisionCommand{}, relationErr
		}
		decision.RelationType = &relationType
	}
	if request.DeferredUntil.Present && !request.DeferredUntil.Null {
		if strings.TrimSpace(request.DeferredUntil.Value) != request.DeferredUntil.Value {
			return graphapp.SemanticLinkCandidateDecisionCommand{}, semanticLinkRequestInvalid(errors.New("candidate deferred time is not canonical"))
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, request.DeferredUntil.Value)
		if parseErr != nil {
			return graphapp.SemanticLinkCandidateDecisionCommand{}, semanticLinkRequestInvalid(parseErr)
		}
		utc := parsed.UTC()
		decision.ResumeAfter = &utc
	}
	if err := validateCandidateDecisionPayload(request, decision); err != nil {
		return graphapp.SemanticLinkCandidateDecisionCommand{}, err
	}
	command := graphapp.SemanticLinkCandidateDecisionCommand{
		WorkspaceID: workspaceID, CandidateID: candidateID, ExpectedVersion: request.ExpectedVersion.Value,
		IdempotencyKey: idempotencyKey, Decision: decision,
	}
	canonical, err := graphapp.CanonicalizeSemanticLinkCandidateDecisionCommand(command)
	if err != nil {
		return graphapp.SemanticLinkCandidateDecisionCommand{}, semanticLinkRequestInvalid(err)
	}
	return canonical, nil
}

func validateCandidateDecisionPayload(wire candidateDecisionRequest, decision graphdomain.SemanticLinkCandidateDecision) error {
	hasReason, hasRelationType := wire.Reason.Present, wire.RelationType.Present
	// null means the optional resume time is explicitly cleared; it carries no
	// decision value and remains compatible with clients that serialize optionals.
	hasDeferredUntil := wire.DeferredUntil.Present && !wire.DeferredUntil.Null
	switch decision.Action {
	case graphdomain.SemanticLinkCandidateDecisionConfirm:
		if hasReason || hasRelationType || hasDeferredUntil {
			return semanticLinkRequestInvalid(errors.New("confirm does not accept optional decision fields"))
		}
	case graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType:
		if !hasRelationType || hasReason || hasDeferredUntil {
			return semanticLinkRequestInvalid(errors.New("typed confirm accepts only relation_type"))
		}
	case graphdomain.SemanticLinkCandidateDecisionIgnore, graphdomain.SemanticLinkCandidateDecisionFalsePositive:
		if !hasReason || hasRelationType || hasDeferredUntil {
			return semanticLinkRequestInvalid(errors.New("ignore decisions require only a reason"))
		}
	case graphdomain.SemanticLinkCandidateDecisionDefer:
		if hasRelationType {
			return semanticLinkRequestInvalid(errors.New("defer does not accept relation_type"))
		}
	case graphdomain.SemanticLinkCandidateDecisionResume:
		if hasReason || hasRelationType || hasDeferredUntil {
			return semanticLinkRequestInvalid(errors.New("resume does not accept optional decision fields"))
		}
	default:
		return semanticLinkRequestInvalid(errors.New("candidate decision action is unsupported"))
	}
	return nil
}

func decodeCandidateDecisionRequest(r *http.Request) (candidateDecisionRequest, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return candidateDecisionRequest{}, foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("candidate decision requires application/json"))
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCandidateRequestBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxCandidateRequestBytes {
		return candidateDecisionRequest{}, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, errors.New("candidate decision body is empty or oversized"))
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxCandidateRequestBytes
	decoded, err := strictjson.DecodeObject[candidateDecisionRequest](body, limits, nil)
	if err != nil {
		return candidateDecisionRequest{}, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return decoded, nil
}

func parseCandidateListRequest(r *http.Request) (graphapp.CandidateListRequest, error) {
	values, err := parseCandidateQueryParameters(r,
		"workspace_id", "node_type", "node_id", "status", "relation_type", "reopened_reason", "min_confidence", "cursor", "limit")
	if err != nil {
		return graphapp.CandidateListRequest{}, err
	}
	workspaceID, err := parseCandidateID(singleCandidateQueryValue(values, "workspace_id"))
	if err != nil {
		return graphapp.CandidateListRequest{}, err
	}
	request := graphdomain.SemanticLinkCandidateQuery{WorkspaceID: workspaceID, Limit: defaultCandidatePageLimit}
	nodeTypeValues, hasNodeType := values["node_type"]
	nodeIDValues, hasNodeID := values["node_id"]
	if hasNodeType != hasNodeID {
		return graphapp.CandidateListRequest{}, semanticLinkRequestInvalid(errors.New("candidate node filter is incomplete"))
	}
	if hasNodeType {
		nodeType, parseErr := parseCandidateNodeType(nodeTypeValues[0])
		if parseErr != nil {
			return graphapp.CandidateListRequest{}, parseErr
		}
		nodeID, parseErr := parseCandidateID(nodeIDValues[0])
		if parseErr != nil {
			return graphapp.CandidateListRequest{}, parseErr
		}
		request.NodeRef = &knowledge.NodeRef{Type: nodeType, ID: nodeID}
	}
	for _, raw := range values["status"] {
		status, parseErr := parseCandidateStatus(raw)
		if parseErr != nil {
			return graphapp.CandidateListRequest{}, parseErr
		}
		request.Statuses = append(request.Statuses, status)
	}
	for _, raw := range values["relation_type"] {
		relationType, parseErr := parseCandidateRelationType(raw)
		if parseErr != nil {
			return graphapp.CandidateListRequest{}, parseErr
		}
		request.RelationTypes = append(request.RelationTypes, relationType)
	}
	for _, raw := range values["reopened_reason"] {
		reason, parseErr := parseCandidateReopenedReason(raw)
		if parseErr != nil {
			return graphapp.CandidateListRequest{}, parseErr
		}
		request.ReopenedReasons = append(request.ReopenedReasons, reason)
	}
	if raw, present := values["min_confidence"]; present {
		confidence, parseErr := strconv.ParseFloat(raw[0], 64)
		if parseErr != nil || math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
			return graphapp.CandidateListRequest{}, semanticLinkRequestInvalid(errors.New("candidate confidence filter is invalid"))
		}
		request.MinConfidence = &confidence
	}
	if raw, present := values["limit"]; present {
		limit, parseErr := strconv.ParseInt(raw[0], 10, 32)
		if parseErr != nil || strconv.FormatInt(limit, 10) != raw[0] {
			return graphapp.CandidateListRequest{}, semanticLinkRequestInvalid(errors.New("candidate limit is invalid"))
		}
		request.Limit = int(limit)
	}
	cursor := ""
	if raw, present := values["cursor"]; present {
		cursor = raw[0]
		if !validCandidateCursor(cursor, false) {
			return graphapp.CandidateListRequest{}, foundation.NewError(foundation.ErrorInvalidInput, graphdomain.ErrorCodeCursorInvalid, false, errors.New("candidate cursor is invalid"))
		}
	}
	if err := graphdomain.ValidateSemanticLinkCandidateQuery(request); err != nil {
		return graphapp.CandidateListRequest{}, semanticLinkRequestInvalid(err)
	}
	return graphapp.CandidateListRequest{Query: request, Cursor: cursor}, nil
}

func parseCandidateQueryParameters(r *http.Request, allowed ...string) (url.Values, error) {
	if len(r.URL.RawQuery) > maxCandidateQueryBytes {
		return nil, semanticLinkRequestInvalid(errors.New("candidate query exceeds byte limit"))
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, semanticLinkRequestInvalid(err)
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	repeated := map[string]bool{"status": true, "relation_type": true, "reopened_reason": true}
	for key, entries := range values {
		if _, ok := allowedSet[key]; !ok || len(entries) == 0 || !repeated[key] && len(entries) != 1 {
			return nil, semanticLinkRequestInvalid(errors.New("candidate query parameter is unknown or repeated"))
		}
		seen := make(map[string]struct{}, len(entries))
		for _, entry := range entries {
			if entry == "" {
				return nil, semanticLinkRequestInvalid(errors.New("candidate query parameter is empty"))
			}
			if _, duplicate := seen[entry]; duplicate {
				return nil, semanticLinkRequestInvalid(errors.New("candidate query parameter is duplicated"))
			}
			seen[entry] = struct{}{}
		}
	}
	return values, nil
}

func singleCandidateQueryValue(values url.Values, key string) string {
	entries := values[key]
	if len(entries) != 1 {
		return ""
	}
	return entries[0]
}

func parseCandidateID(raw string) (foundation.ID, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", semanticLinkRequestInvalid(errors.New("candidate identifier is empty or not canonical"))
	}
	id, err := foundation.ParseID(raw)
	if err != nil || string(id) != raw {
		return "", semanticLinkRequestInvalid(errors.New("candidate identifier is invalid"))
	}
	return id, nil
}

func parseCandidateIdempotencyKey(values []string) (string, error) {
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] || len(values[0]) > maxIdempotencyKeyBytes || !utf8.ValidString(values[0]) {
		return "", semanticLinkRequestInvalid(errors.New("candidate Idempotency-Key is invalid"))
	}
	for _, character := range values[0] {
		if unicode.IsControl(character) {
			return "", semanticLinkRequestInvalid(errors.New("candidate Idempotency-Key contains a control character"))
		}
	}
	return values[0], nil
}

func validCandidateCursor(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	if len(value) > maxCandidateCursorBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

func parseCandidateNodeType(raw string) (knowledge.NodeType, error) {
	switch knowledge.NodeType(raw) {
	case knowledge.NodeTypeTopic, knowledge.NodeTypeClaim:
		return knowledge.NodeType(raw), nil
	default:
		return "", semanticLinkRequestInvalid(errors.New("candidate node type is unsupported"))
	}
}

func parseCandidateStatus(raw string) (graphdomain.SemanticLinkCandidateStatus, error) {
	switch graphdomain.SemanticLinkCandidateStatus(raw) {
	case graphdomain.SemanticLinkCandidateStatusActive, graphdomain.SemanticLinkCandidateStatusDeferred,
		graphdomain.SemanticLinkCandidateStatusIgnored, graphdomain.SemanticLinkCandidateStatusFalsePositive,
		graphdomain.SemanticLinkCandidateStatusProposalCreated, graphdomain.SemanticLinkCandidateStatusSuperseded:
		return graphdomain.SemanticLinkCandidateStatus(raw), nil
	default:
		return "", semanticLinkRequestInvalid(errors.New("candidate status is unsupported"))
	}
}

func parseCandidateRelationType(raw string) (knowledge.RelationType, error) {
	switch knowledge.RelationType(raw) {
	case knowledge.RelationCites, knowledge.RelationDerivedFrom, knowledge.RelationBelongsTo,
		knowledge.RelationSupports, knowledge.RelationComplements, knowledge.RelationDuplicates,
		knowledge.RelationConflictsWith, knowledge.RelationPrerequisiteOf, knowledge.RelationVersionOf,
		knowledge.RelationImpacts:
		return knowledge.RelationType(raw), nil
	default:
		return "", semanticLinkRequestInvalid(errors.New("candidate relation type is unsupported"))
	}
}

func parseCandidateReopenedReason(raw string) (graphdomain.SemanticLinkCandidateReopenedReason, error) {
	if raw != string(graphdomain.SemanticLinkCandidateReopenedReasonContentChanged) {
		return "", semanticLinkRequestInvalid(errors.New("candidate reopened reason is unsupported"))
	}
	return graphdomain.SemanticLinkCandidateReopenedReason(raw), nil
}

func parseCandidateDecisionAction(raw string) (graphdomain.SemanticLinkCandidateDecisionAction, error) {
	switch graphdomain.SemanticLinkCandidateDecisionAction(raw) {
	case graphdomain.SemanticLinkCandidateDecisionConfirm, graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType,
		graphdomain.SemanticLinkCandidateDecisionIgnore, graphdomain.SemanticLinkCandidateDecisionFalsePositive,
		graphdomain.SemanticLinkCandidateDecisionDefer, graphdomain.SemanticLinkCandidateDecisionResume:
		return graphdomain.SemanticLinkCandidateDecisionAction(raw), nil
	default:
		return "", semanticLinkRequestInvalid(errors.New("candidate decision action is unsupported"))
	}
}

func writeSemanticLinkError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, errorCodeSemanticLinkDependencyUnavailable, "Semantic Link 请求超时", true, nil)
		return
	}
	if errors.Is(err, context.Canceled) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, errorCodeSemanticLinkDependencyUnavailable, "Semantic Link 请求已取消", false, nil)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Semantic Link 请求失败", false, nil)
		return
	}
	status, code, message := semanticLinkPublicError(classified)
	httpapi.WriteProblem(w, status, code, message, classified.Retryable, nil)
}

func semanticLinkPublicError(err *foundation.Error) (int, string, string) {
	switch {
	case err.Code == "UNSUPPORTED_MEDIA_TYPE":
		return http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "请求必须使用 application/json"
	case err.Code == "INVALID_JSON":
		return http.StatusBadRequest, "INVALID_JSON", "请求 JSON 无效"
	case err.Code == graphdomain.ErrorCodeCursorInvalid:
		return http.StatusBadRequest, errorCodeSemanticLinkCursorInvalid, "Candidate cursor 无效"
	case err.Code == graphdomain.ErrorCodeCursorStale:
		return http.StatusConflict, errorCodeSemanticLinkCursorStale, "Candidate cursor 已失效"
	case err.Kind == foundation.ErrorInvalidInput:
		return http.StatusBadRequest, errorCodeSemanticLinkRequestInvalid, "Semantic Link 请求参数无效"
	case err.Kind == foundation.ErrorNotFound:
		return http.StatusNotFound, errorCodeSemanticLinkNotFound, "请求的 Semantic Link Candidate 不存在"
	case err.Kind == foundation.ErrorVersionConflict:
		return http.StatusConflict, errorCodeSemanticLinkVersionConflict, "Semantic Link Candidate 版本冲突"
	case err.Kind == foundation.ErrorPermissionDenied:
		return http.StatusForbidden, "SEMANTIC_LINK_FORBIDDEN", "无权访问 Semantic Link Candidate"
	case err.Kind == foundation.ErrorDependencyUnavailable || err.Kind == foundation.ErrorRetryableFailure:
		return http.StatusServiceUnavailable, errorCodeSemanticLinkDependencyUnavailable, "Semantic Link 依赖暂不可用"
	case err.Code == graphdomain.ErrorCodeSemanticLinkCandidateResultInvalid || err.Code == graphdomain.ErrorCodeProjectionInconsistent:
		return http.StatusInternalServerError, errorCodeSemanticLinkResultInvalid, "Semantic Link 响应校验失败"
	case err.Kind == foundation.ErrorConsistencyViolation:
		return http.StatusConflict, errorCodeSemanticLinkVersionConflict, "Semantic Link Candidate 绑定冲突"
	default:
		return http.StatusInternalServerError, "INTERNAL_ERROR", "Semantic Link 请求失败"
	}
}

func semanticLinkRequestInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, graphdomain.ErrorCodeSemanticLinkCandidateRequestInvalid, false, cause)
}

func semanticLinkResultInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeSemanticLinkCandidateResultInvalid, false, cause)
}
