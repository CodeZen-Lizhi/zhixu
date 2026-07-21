package graphhttp

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/go-chi/chi/v5"
)

type candidateScanStartRequest struct {
	WorkspaceID optional[string]                    `json:"workspace_id"`
	Scope       optional[candidateScanScopeRequest] `json:"scope"`
}

type candidateScanScopeRequest struct {
	Kind     optional[string] `json:"kind"`
	TopicID  optional[string] `json:"topic_id"`
	NodeType optional[string] `json:"node_type"`
	NodeID   optional[string] `json:"node_id"`
}

func (handler *CandidateHandler) startScan(w http.ResponseWriter, r *http.Request) {
	if handler == nil || nilCandidateHTTPDependency(handler.scans) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, errorCodeSemanticLinkDependencyUnavailable, "Semantic Link 扫描服务暂不可用", true, nil)
		return
	}
	idempotencyKey, err := parseCandidateIdempotencyKey(r.Header.Values("Idempotency-Key"))
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	wire, err := decodeCandidateScanStartRequest(r)
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	request, err := wire.toTopicRequest(idempotencyKey)
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.scans.StartTopicScan(ctx, request)
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	if err := validateCandidateScanStartResult(request, result); err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, toCandidateScanAcceptanceResponse(result))
}

func (handler *CandidateHandler) getScan(w http.ResponseWriter, r *http.Request) {
	if handler == nil || nilCandidateHTTPDependency(handler.scans) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, errorCodeSemanticLinkDependencyUnavailable, "Semantic Link 扫描服务暂不可用", true, nil)
		return
	}
	values, err := parseCandidateQueryParameters(r, "workspace_id")
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	workspaceID, err := parseCandidateID(singleCandidateQueryValue(values, "workspace_id"))
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	scanID, err := parseCandidateID(chi.URLParam(r, "scan_id"))
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	scan, err := handler.scans.Get(ctx, workspaceID, scanID)
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	if err := graphdomain.ValidateSemanticLinkScan(scan); err != nil || scan.ID != scanID || scan.WorkspaceID != workspaceID || scan.Scope.Type != graphdomain.SemanticLinkScanScopeTopic {
		writeSemanticLinkError(w, semanticLinkResultInvalid(errors.New("candidate scan response is inconsistent")))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toCandidateScanResponse(scan))
}

func validateCandidateScanStartResult(request graphapp.SemanticLinkTopicScanRequest, result graphapp.SemanticLinkScanStartResult) error {
	scan := result.Scan
	if err := graphdomain.ValidateSemanticLinkScan(scan); err != nil || scan.WorkspaceID != request.WorkspaceID || scan.Scope.Type != graphdomain.SemanticLinkScanScopeTopic || scan.Scope.Ref != string(request.TopicID) || result.StatusURL != graphapp.SemanticLinkScanStatusURL(scan.WorkspaceID, scan.ID) {
		return semanticLinkResultInvalid(errors.New("candidate scan start response is inconsistent"))
	}
	return nil
}

func decodeCandidateScanStartRequest(r *http.Request) (candidateScanStartRequest, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return candidateScanStartRequest{}, foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("candidate scan requires application/json"))
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCandidateRequestBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxCandidateRequestBytes {
		return candidateScanStartRequest{}, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, errors.New("candidate scan body is empty or oversized"))
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxCandidateRequestBytes
	decoded, err := strictjson.DecodeObject[candidateScanStartRequest](body, limits, nil)
	if err != nil {
		return candidateScanStartRequest{}, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return decoded, nil
}

func (request candidateScanStartRequest) toTopicRequest(idempotencyKey string) (graphapp.SemanticLinkTopicScanRequest, error) {
	if !request.WorkspaceID.Present || request.WorkspaceID.Null || !request.Scope.Present || request.Scope.Null {
		return graphapp.SemanticLinkTopicScanRequest{}, semanticLinkRequestInvalid(errors.New("candidate scan required fields are missing"))
	}
	workspaceID, err := parseCandidateID(request.WorkspaceID.Value)
	if err != nil {
		return graphapp.SemanticLinkTopicScanRequest{}, err
	}
	scope := request.Scope.Value
	if !scope.Kind.Present || scope.Kind.Null || scope.Kind.Value != "TOPIC" || !scope.TopicID.Present || scope.TopicID.Null || scope.NodeType.Present || scope.NodeID.Present {
		return graphapp.SemanticLinkTopicScanRequest{}, semanticLinkRequestInvalid(errors.New("only TOPIC scan scope is supported"))
	}
	topicID, err := parseCandidateID(scope.TopicID.Value)
	if err != nil {
		return graphapp.SemanticLinkTopicScanRequest{}, err
	}
	return graphapp.SemanticLinkTopicScanRequest{WorkspaceID: workspaceID, TopicID: topicID, IdempotencyKey: idempotencyKey}, nil
}
