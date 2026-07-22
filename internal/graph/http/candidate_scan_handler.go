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
	Kind         optional[string] `json:"kind"`
	TopicID      optional[string] `json:"topic_id"`
	CollectionID optional[string] `json:"collection_id"`
	NodeType     optional[string] `json:"node_type"`
	NodeID       optional[string] `json:"node_id"`
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
	request, err := wire.toStartRequest(idempotencyKey)
	if err != nil {
		writeSemanticLinkError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	var result graphapp.SemanticLinkScanStartResult
	switch request.kind {
	case string(graphdomain.SemanticLinkScanScopeTopic):
		result, err = handler.scans.StartTopicScan(ctx, graphapp.SemanticLinkTopicScanRequest{WorkspaceID: request.workspaceID, TopicID: request.scopeID, IdempotencyKey: idempotencyKey})
	case string(graphdomain.SemanticLinkScanScopeSmartCollection):
		smart, ok := handler.scans.(graphapp.SemanticLinkSmartScanHTTPService)
		if !ok {
			writeSemanticLinkError(w, foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeSemanticLinkDiscoveryUnavailable, false, errors.New("smart collection scan service is unavailable")))
			return
		}
		result, err = smart.StartSmartCollectionScan(ctx, graphapp.SemanticLinkSmartCollectionScanRequest{WorkspaceID: request.workspaceID, CollectionID: request.scopeID, IdempotencyKey: idempotencyKey})
	default:
		err = semanticLinkRequestInvalid(errors.New("candidate scan scope is unsupported"))
	}
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
	if err := graphdomain.ValidateSemanticLinkScan(scan); err != nil || scan.ID != scanID || scan.WorkspaceID != workspaceID || (scan.Scope.Type != graphdomain.SemanticLinkScanScopeTopic && scan.Scope.Type != graphdomain.SemanticLinkScanScopeSmartCollection) {
		writeSemanticLinkError(w, semanticLinkResultInvalid(errors.New("candidate scan response is inconsistent")))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toCandidateScanResponse(scan))
}

type candidateScanStartCommand struct {
	workspaceID foundation.ID
	kind        string
	scopeID     foundation.ID
}

func validateCandidateScanStartResult(request candidateScanStartCommand, result graphapp.SemanticLinkScanStartResult) error {
	scan := result.Scan
	if err := graphdomain.ValidateSemanticLinkScan(scan); err != nil || scan.WorkspaceID != request.workspaceID || string(scan.Scope.Type) != request.kind || scan.Scope.Ref != string(request.scopeID) || result.StatusURL != graphapp.SemanticLinkScanStatusURL(scan.WorkspaceID, scan.ID) {
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

func (request candidateScanStartRequest) toStartRequest(idempotencyKey string) (candidateScanStartCommand, error) {
	if !request.WorkspaceID.Present || request.WorkspaceID.Null || !request.Scope.Present || request.Scope.Null {
		return candidateScanStartCommand{}, semanticLinkRequestInvalid(errors.New("candidate scan required fields are missing"))
	}
	workspaceID, err := parseCandidateID(request.WorkspaceID.Value)
	if err != nil {
		return candidateScanStartCommand{}, err
	}
	scope := request.Scope.Value
	if !scope.Kind.Present || scope.Kind.Null || scope.NodeType.Present || scope.NodeID.Present {
		return candidateScanStartCommand{}, semanticLinkRequestInvalid(errors.New("candidate scan scope is invalid"))
	}
	switch scope.Kind.Value {
	case string(graphdomain.SemanticLinkScanScopeTopic):
		if !scope.TopicID.Present || scope.TopicID.Null || scope.CollectionID.Present {
			return candidateScanStartCommand{}, semanticLinkRequestInvalid(errors.New("topic scan scope is invalid"))
		}
		topicID, parseErr := parseCandidateID(scope.TopicID.Value)
		if parseErr != nil {
			return candidateScanStartCommand{}, parseErr
		}
		return candidateScanStartCommand{workspaceID: workspaceID, kind: scope.Kind.Value, scopeID: topicID}, nil
	case string(graphdomain.SemanticLinkScanScopeSmartCollection):
		if !scope.CollectionID.Present || scope.CollectionID.Null || scope.TopicID.Present {
			return candidateScanStartCommand{}, semanticLinkRequestInvalid(errors.New("smart collection scan scope is invalid"))
		}
		collectionID, parseErr := parseCandidateID(scope.CollectionID.Value)
		if parseErr != nil {
			return candidateScanStartCommand{}, parseErr
		}
		return candidateScanStartCommand{workspaceID: workspaceID, kind: scope.Kind.Value, scopeID: collectionID}, nil
	default:
		return candidateScanStartCommand{}, semanticLinkRequestInvalid(errors.New("candidate scan scope is unsupported"))
	}
}
