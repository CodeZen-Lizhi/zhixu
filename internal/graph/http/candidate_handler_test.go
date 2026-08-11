package graphhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/gin-gonic/gin"
)

const (
	candidateHTTPWorkspaceID     foundation.ID = "92000000-0000-4000-8000-000000000001"
	candidateHTTPID              foundation.ID = "92000000-0000-4000-8000-000000000002"
	candidateHTTPSourceID        foundation.ID = "92000000-0000-4000-8000-000000000003"
	candidateHTTPTargetID        foundation.ID = "92000000-0000-4000-8000-000000000004"
	candidateHTTPSourceVersionID foundation.ID = "92000000-0000-4000-8000-000000000005"
	candidateHTTPSourceSpanID    foundation.ID = "92000000-0000-4000-8000-000000000006"
	candidateHTTPEvidenceID      foundation.ID = "92000000-0000-4000-8000-000000000007"
	candidateHTTPRuleID          foundation.ID = "92000000-0000-4000-8000-000000000008"
	candidateHTTPProposalID      foundation.ID = "92000000-0000-4000-8000-000000000009"
	candidateHTTPDecisionID      foundation.ID = "92000000-0000-4000-8000-00000000000a"
	candidateHTTPReopenedFromID  foundation.ID = "92000000-0000-4000-8000-00000000000b"
	candidateHTTPOtherWorkspace  foundation.ID = "92000000-0000-4000-8000-00000000000c"
	candidateHTTPScanID          foundation.ID = "92000000-0000-4000-8000-00000000000d"
	candidateHTTPWorkflowRunID   foundation.ID = "92000000-0000-4000-8000-00000000000e"
	candidateHTTPTopicID         foundation.ID = "92000000-0000-4000-8000-00000000000f"
)

func TestCandidateScanStartAndGetStrictTopicContract(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	scan := candidateHTTPScanFixture(now)
	statusURL := "/api/v1/graph/candidate-scans/" + string(candidateHTTPScanID) + "?workspace_id=" + string(candidateHTTPWorkspaceID)
	service := &fakeCandidateScanService{
		start: graphapp.SemanticLinkScanStartResult{
			Scan: scan, StatusURL: statusURL,
		},
		scan: scan,
	}
	body := `{"workspace_id":"` + string(candidateHTTPWorkspaceID) + `","scope":{"kind":"TOPIC","topic_id":"` + string(candidateHTTPTopicID) + `"}}`
	response := serveCandidateScanRequest(t, service, http.MethodPost, "/api/v1/graph/candidate-scans", body, "application/json", "scan-topic-1")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.startRequest.WorkspaceID != candidateHTTPWorkspaceID || service.startRequest.TopicID != candidateHTTPTopicID || service.startRequest.IdempotencyKey != "scan-topic-1" || service.startCalls != 1 {
		t.Fatalf("request=%+v calls=%d", service.startRequest, service.startCalls)
	}
	var accepted candidateScanAcceptanceResponse
	decodeCandidateResponse(t, response, &accepted)
	if accepted.ScanID != string(candidateHTTPScanID) || accepted.WorkflowRunID != string(candidateHTTPWorkflowRunID) || accepted.StatusURL != statusURL {
		t.Fatalf("accepted=%+v", accepted)
	}

	response = serveCandidateScanRequest(t, service, http.MethodGet,
		"/api/v1/graph/candidate-scans/"+string(candidateHTTPScanID)+"?workspace_id="+string(candidateHTTPWorkspaceID), "", "", "")
	if response.Code != http.StatusOK || service.getWorkspaceID != candidateHTTPWorkspaceID || service.getScanID != candidateHTTPScanID {
		t.Fatalf("status=%d workspace=%s scan=%s body=%s", response.Code, service.getWorkspaceID, service.getScanID, response.Body.String())
	}
	var payload candidateScanResponse
	decodeCandidateResponse(t, response, &payload)
	if payload.Scope.Kind != "TOPIC" || payload.Scope.TopicID != string(candidateHTTPTopicID) || payload.StatusURL != statusURL || payload.ProcessedCount != 2 || payload.CandidateCount != 1 || payload.IgnoredCount != 1 || payload.LastError != nil {
		t.Fatalf("payload=%+v", payload)
	}

	failed := scan
	failed.Status = graphdomain.SemanticLinkScanStatusFailed
	failed.LastError = &graphdomain.SemanticLinkScanError{Stage: "discover", Code: "SEMANTIC_PROVIDER_TIMEOUT", Retryable: true}
	completedAt := now.Add(2 * time.Second)
	failed.CompletedAt = &completedAt
	failed.UpdatedAt = completedAt
	service.scan = failed
	response = serveCandidateScanRequest(t, service, http.MethodGet,
		"/api/v1/graph/candidate-scans/"+string(candidateHTTPScanID)+"?workspace_id="+string(candidateHTTPWorkspaceID), "", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("failed scan status=%d body=%s", response.Code, response.Body.String())
	}
	decodeCandidateResponse(t, response, &payload)
	if payload.LastError == nil || payload.LastError.Stage != "discover" || payload.LastError.Code != "SEMANTIC_PROVIDER_TIMEOUT" || !payload.LastError.Retryable {
		t.Fatalf("failed payload=%+v", payload)
	}
}

func TestCandidateScanRejectsUnsupportedScopeAndInvalidWire(t *testing.T) {
	workspace := string(candidateHTTPWorkspaceID)
	topic := string(candidateHTTPTopicID)
	tests := []struct {
		name        string
		body        string
		contentType string
		key         string
		status      int
		code        string
	}{
		{name: "missing key", body: `{"workspace_id":"` + workspace + `","scope":{"kind":"TOPIC","topic_id":"` + topic + `"}}`, contentType: "application/json", status: http.StatusBadRequest, code: errorCodeSemanticLinkRequestInvalid},
		{name: "node scope", body: `{"workspace_id":"` + workspace + `","scope":{"kind":"NODE","node_type":"CLAIM","node_id":"` + topic + `"}}`, contentType: "application/json", key: "scan-1", status: http.StatusBadRequest, code: errorCodeSemanticLinkRequestInvalid},
		{name: "unknown field", body: `{"workspace_id":"` + workspace + `","scope":{"kind":"TOPIC","topic_id":"` + topic + `"},"unknown":true}`, contentType: "application/json", key: "scan-1", status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "duplicate field", body: `{"workspace_id":"` + workspace + `","workspace_id":"` + workspace + `","scope":{"kind":"TOPIC","topic_id":"` + topic + `"}}`, contentType: "application/json", key: "scan-1", status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "media type", body: `{}`, contentType: "text/plain", key: "scan-1", status: http.StatusUnsupportedMediaType, code: "UNSUPPORTED_MEDIA_TYPE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeCandidateScanService{}
			response := serveCandidateScanRequest(t, service, http.MethodPost, "/api/v1/graph/candidate-scans", test.body, test.contentType, test.key)
			requireCandidateProblem(t, response, test.status, test.code, false)
			if service.startCalls != 0 {
				t.Fatal("invalid scan request reached service")
			}
		})
	}
}

func TestCandidateScanRejectsInconsistentServiceProjection(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	scan := candidateHTTPScanFixture(now)
	scan.WorkspaceID = candidateHTTPOtherWorkspace
	service := &fakeCandidateScanService{scan: scan}
	response := serveCandidateScanRequest(t, service, http.MethodGet,
		"/api/v1/graph/candidate-scans/"+string(candidateHTTPScanID)+"?workspace_id="+string(candidateHTTPWorkspaceID), "", "", "")
	requireCandidateProblem(t, response, http.StatusInternalServerError, errorCodeSemanticLinkResultInvalid, false)
}

func TestCandidateScanResponseBindsSmartCollectionScope(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	scan := candidateHTTPScanFixture(now)
	scan.Scope = graphdomain.SemanticLinkScanScope{
		Type: graphdomain.SemanticLinkScanScopeSmartCollection, Ref: string(candidateHTTPTopicID), Version: 7,
		SchemaVersion: graphapp.SemanticLinkSmartCollectionScanScopeSchemaVersion,
		QueryHash:     strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64),
	}
	response := toCandidateScanResponse(scan)
	if response.Scope.Kind != "SMART_COLLECTION" || response.Scope.CollectionID != string(candidateHTTPTopicID) || response.Scope.CollectionVersion != 7 || response.Scope.QueryHash != strings.Repeat("a", 64) || response.Scope.ReadModelRevision != strings.Repeat("b", 64) || response.Scope.TopicID != "" {
		t.Fatalf("smart response scope=%#v", response.Scope)
	}
}

func TestCandidateListStrictContractAndSafeResponse(t *testing.T) {
	candidate := candidateHTTPFixture(t)
	service := &fakeCandidateService{page: graphdomain.SemanticLinkCandidatePage{
		WorkspaceID: candidateHTTPWorkspaceID,
		Items:       []graphdomain.SemanticLinkCandidate{candidate},
		Meta: graphdomain.PageMeta{
			Fingerprint: strings.Repeat("c", 64), NextCursor: "opaque.v2", Complete: false,
		},
	}}
	path := "/api/v1/graph/candidates?workspace_id=" + string(candidateHTTPWorkspaceID) +
		"&node_type=CLAIM&node_id=" + string(candidateHTTPSourceID) +
		"&status=ACTIVE&status=DEFERRED&relation_type=BELONGS_TO&reopened_reason=CONTENT_CHANGED" +
		"&min_confidence=0.5&cursor=opaque.v1&limit=20"
	response := serveCandidateRequest(t, service, time.Second, http.MethodGet, path, "", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	request := service.listRequest
	if request.Query.WorkspaceID != candidateHTTPWorkspaceID || request.Query.NodeRef == nil || request.Query.NodeRef.Type != knowledge.NodeTypeClaim ||
		request.Query.NodeRef.ID != candidateHTTPSourceID || request.Query.Limit != 20 || request.Cursor != "opaque.v1" ||
		len(request.Query.Statuses) != 2 || len(request.Query.RelationTypes) != 1 || len(request.Query.ReopenedReasons) != 1 ||
		request.Query.MinConfidence == nil || *request.Query.MinConfidence != 0.5 {
		t.Fatalf("request=%#v", request)
	}
	var payload map[string]any
	decodeCandidateResponse(t, response, &payload)
	if payload["workspace_id"] != string(candidateHTTPWorkspaceID) || payload["next_cursor"] != "opaque.v2" {
		t.Fatalf("payload=%#v", payload)
	}
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items=%#v", payload["items"])
	}
	item := items[0].(map[string]any)
	source := item["source"].(map[string]any)
	target := item["target"].(map[string]any)
	if source["excerpt"] != "Claim excerpt" || target["excerpt"] != "Topic excerpt" {
		t.Fatalf("source=%#v target=%#v", source, target)
	}
	generation := item["generation"].(map[string]any)
	if generation["rule_id"] != string(candidateHTTPRuleID) || generation["rule_version"] != "rule-v1" ||
		generation["index_version_id"] != nil || generation["rerank_version_id"] != nil {
		t.Fatalf("generation=%#v", generation)
	}
	for _, forbidden := range []string{"model_adapter", "model_name", "rerank_model_version"} {
		if _, exists := generation[forbidden]; exists {
			t.Fatalf("generation must not invent %s: %#v", forbidden, generation)
		}
	}
	evidence := item["evidence"].([]any)[0].(map[string]any)
	wantSourceHref := "/api/v1/workspaces/" + string(candidateHTTPWorkspaceID) + "/source-versions/" + string(candidateHTTPSourceVersionID)
	if evidence["source_version_href"] != wantSourceHref || evidence["source_span_href"] != wantSourceHref+"/spans/"+string(candidateHTTPSourceSpanID) {
		t.Fatalf("evidence=%#v", evidence)
	}
	if !service.listDeadline.After(time.Now()) {
		t.Fatalf("candidate list request did not receive a deadline")
	}
}

func TestCandidateListTopicScopeReturnsClaimPair(t *testing.T) {
	candidate := candidateHTTPFixture(t)
	candidate.Target = graphdomain.SemanticLinkCandidateEndpoint{
		Ref: knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: candidateHTTPTargetID}, Version: 2,
		Summary: "Related Claim", Excerpt: "Related Claim excerpt",
	}
	candidate.SuggestedRelationType = knowledge.RelationComplements
	candidate.Fingerprint = ""
	fingerprint, err := graphdomain.ComputeSemanticLinkCandidateFingerprint(candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Fingerprint = fingerprint
	service := &fakeCandidateService{page: graphdomain.SemanticLinkCandidatePage{
		WorkspaceID: candidateHTTPWorkspaceID,
		Items:       []graphdomain.SemanticLinkCandidate{candidate},
		Meta:        graphdomain.PageMeta{Fingerprint: strings.Repeat("d", 64), Complete: true},
	}}
	path := "/api/v1/graph/candidates?workspace_id=" + string(candidateHTTPWorkspaceID) +
		"&node_type=TOPIC&node_id=" + string(candidateHTTPTopicID) + "&status=ACTIVE&limit=20"
	response := serveCandidateRequest(t, service, time.Second, http.MethodGet, path, "", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	request := service.listRequest.Query
	if request.NodeRef == nil || request.NodeRef.Type != knowledge.NodeTypeTopic || request.NodeRef.ID != candidateHTTPTopicID {
		t.Fatalf("topic request=%+v", request)
	}
}

func TestCandidateListUsesDefaultLimitAndEmptyArray(t *testing.T) {
	service := &fakeCandidateService{page: graphdomain.SemanticLinkCandidatePage{
		WorkspaceID: candidateHTTPWorkspaceID,
		Items:       []graphdomain.SemanticLinkCandidate{},
		Meta:        graphdomain.PageMeta{Fingerprint: strings.Repeat("d", 64), Complete: true},
	}}
	response := serveCandidateRequest(t, service, time.Second, http.MethodGet,
		"/api/v1/graph/candidates?workspace_id="+string(candidateHTTPWorkspaceID), "", "", "")
	if response.Code != http.StatusOK || service.listRequest.Query.Limit != defaultCandidatePageLimit {
		t.Fatalf("status=%d request=%#v body=%s", response.Code, service.listRequest, response.Body.String())
	}
	var payload map[string]json.RawMessage
	decodeCandidateResponse(t, response, &payload)
	if string(payload["items"]) != "[]" {
		t.Fatalf("items must be an empty array: %s", payload["items"])
	}
	if _, exists := payload["next_cursor"]; exists {
		t.Fatalf("complete empty page must omit next_cursor: %s", response.Body.String())
	}
}

func TestCandidateDetailBindsWorkspaceAndIdentity(t *testing.T) {
	candidate := candidateHTTPFixture(t)
	service := &fakeCandidateService{candidate: candidate}
	path := "/api/v1/graph/candidates/" + string(candidateHTTPID) + "?workspace_id=" + string(candidateHTTPWorkspaceID)
	response := serveCandidateRequest(t, service, time.Second, http.MethodGet, path, "", "", "")
	if response.Code != http.StatusOK || service.getWorkspaceID != candidateHTTPWorkspaceID || service.getCandidateID != candidateHTTPID {
		t.Fatalf("status=%d workspace=%s candidate=%s body=%s", response.Code, service.getWorkspaceID, service.getCandidateID, response.Body.String())
	}
	var payload candidateResponse
	decodeCandidateResponse(t, response, &payload)
	if payload.ID != string(candidateHTTPID) || payload.WorkspaceID != string(candidateHTTPWorkspaceID) {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestCandidateDecisionCreatesAndReplaysPersistedReceipt(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	service := &fakeCandidateService{receipt: graphapp.CandidateDecisionReceipt{
		ID: candidateHTTPDecisionID, CandidateID: candidateHTTPID, WorkspaceID: candidateHTTPWorkspaceID,
		Action: graphdomain.SemanticLinkCandidateDecisionIgnore, Status: graphdomain.SemanticLinkCandidateStatusIgnored,
		Version: 4, CreatedAt: now, UpdatedAt: now,
	}}
	body := `{"workspace_id":"` + string(candidateHTTPWorkspaceID) + `","action":"IGNORE","expected_version":3,"reason":"  repeated   meaning  ","deferred_until":null}`
	path := "/api/v1/graph/candidates/" + string(candidateHTTPID) + "/decisions"
	response := serveCandidateRequest(t, service, time.Second, http.MethodPost, path, body, "application/json", "decision-1")
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	firstBody := response.Body.String()
	if service.command.WorkspaceID != candidateHTTPWorkspaceID || service.command.CandidateID != candidateHTTPID ||
		service.command.ExpectedVersion != 3 || service.command.IdempotencyKey != "decision-1" || service.command.Decision.Reason != "repeated meaning" {
		t.Fatalf("command=%#v", service.command)
	}
	var receipt candidateDecisionReceiptResponse
	decodeCandidateResponse(t, response, &receipt)
	if receipt.ID != string(candidateHTTPDecisionID) || receipt.CandidateID != string(candidateHTTPID) || receipt.Status != "IGNORED" || receipt.ProposalID != nil {
		t.Fatalf("receipt=%#v", receipt)
	}

	service.receipt.Replayed = true
	replayed := serveCandidateRequest(t, service, time.Second, http.MethodPost, path, body, "application/json", "decision-1")
	if replayed.Code != http.StatusOK || replayed.Body.String() != firstBody {
		t.Fatalf("replay status=%d body=%s first=%s", replayed.Code, replayed.Body.String(), firstBody)
	}
}

func TestCandidateTypedConfirmReturnsProposalReceipt(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	proposalID := candidateHTTPProposalID
	service := &fakeCandidateService{receipt: graphapp.CandidateDecisionReceipt{
		ID: candidateHTTPDecisionID, CandidateID: candidateHTTPID, WorkspaceID: candidateHTTPWorkspaceID,
		Action: graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType,
		Status: graphdomain.SemanticLinkCandidateStatusProposalCreated, Version: 4, ProposalID: &proposalID,
		CreatedAt: now, UpdatedAt: now,
	}}
	body := `{"workspace_id":"` + string(candidateHTTPWorkspaceID) + `","action":"CONFIRM_WITH_RELATION_TYPE","expected_version":3,"relation_type":"BELONGS_TO","deferred_until":null}`
	response := serveCandidateRequest(t, service, time.Second, http.MethodPost,
		"/api/v1/graph/candidates/"+string(candidateHTTPID)+"/decisions", body, "application/json", "decision-confirm-1")
	if response.Code != http.StatusCreated || service.command.Decision.RelationType == nil || *service.command.Decision.RelationType != knowledge.RelationBelongsTo {
		t.Fatalf("status=%d command=%#v body=%s", response.Code, service.command, response.Body.String())
	}
	var receipt candidateDecisionReceiptResponse
	decodeCandidateResponse(t, response, &receipt)
	if receipt.ProposalID == nil || *receipt.ProposalID != string(candidateHTTPProposalID) {
		t.Fatalf("receipt=%#v", receipt)
	}
}

func TestCandidateDecisionNullableDeferredUntilSemantics(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		action graphdomain.SemanticLinkCandidateDecisionAction
		status graphdomain.SemanticLinkCandidateStatus
		body   string
	}{
		{
			name: "defer uses null for manual resume", action: graphdomain.SemanticLinkCandidateDecisionDefer,
			status: graphdomain.SemanticLinkCandidateStatusDeferred,
			body:   `{"workspace_id":"` + string(candidateHTTPWorkspaceID) + `","action":"DEFER","expected_version":3,"deferred_until":null}`,
		},
		{
			name: "resume may explicitly clear deferred time", action: graphdomain.SemanticLinkCandidateDecisionResume,
			status: graphdomain.SemanticLinkCandidateStatusActive,
			body:   `{"workspace_id":"` + string(candidateHTTPWorkspaceID) + `","action":"RESUME","expected_version":3,"deferred_until":null}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeCandidateService{receipt: graphapp.CandidateDecisionReceipt{
				ID: candidateHTTPDecisionID, CandidateID: candidateHTTPID, WorkspaceID: candidateHTTPWorkspaceID,
				Action: test.action, Status: test.status, Version: 4, CreatedAt: now, UpdatedAt: now,
			}}
			response := serveCandidateRequest(t, service, time.Second, http.MethodPost,
				"/api/v1/graph/candidates/"+string(candidateHTTPID)+"/decisions", test.body, "application/json", "decision-null-1")
			if response.Code != http.StatusCreated || service.command.Decision.ResumeAfter != nil {
				t.Fatalf("status=%d command=%#v body=%s", response.Code, service.command, response.Body.String())
			}
		})
	}
}

func TestCandidateListRejectsInvalidQueriesBeforeService(t *testing.T) {
	base := "/api/v1/graph/candidates?workspace_id=" + string(candidateHTTPWorkspaceID)
	tests := []string{
		"/api/v1/graph/candidates",
		base + "&unknown=1",
		base + "&workspace_id=" + string(candidateHTTPWorkspaceID),
		base + "&node_type=CLAIM",
		base + "&node_id=" + string(candidateHTTPSourceID),
		base + "&node_type=DOCUMENT&node_id=" + string(candidateHTTPSourceID),
		base + "&status=ACTIVE&status=ACTIVE",
		base + "&status=UNKNOWN",
		base + "&relation_type=UNKNOWN",
		base + "&reopened_reason=MODEL_CHANGED",
		base + "&min_confidence=NaN",
		base + "&min_confidence=1.01",
		base + "&limit=01",
		base + "&limit=101",
		base + "&cursor=",
		base + "&cursor=" + strings.Repeat("x", maxCandidateQueryBytes),
		base + "%3Binvalid=1",
		"/api/v1/graph/candidates?workspace_id=92000000-0000-4000-8000-00000000000A",
	}
	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			service := &fakeCandidateService{}
			response := serveCandidateRequest(t, service, time.Second, http.MethodGet, path, "", "", "")
			requireCandidateProblem(t, response, http.StatusBadRequest, errorCodeSemanticLinkRequestInvalid, false)
			if service.listCalls != 0 {
				t.Fatalf("service called for invalid path %q", path)
			}
		})
	}
}

func TestCandidateDecisionRejectsStrictJSONAndIllegalPayloads(t *testing.T) {
	path := "/api/v1/graph/candidates/" + string(candidateHTTPID) + "/decisions"
	workspace := string(candidateHTTPWorkspaceID)
	tests := []struct {
		name        string
		body        string
		contentType string
		key         string
		status      int
		code        string
	}{
		{name: "media type", body: `{}`, contentType: "text/plain", key: "decision-1", status: http.StatusUnsupportedMediaType, code: "UNSUPPORTED_MEDIA_TYPE"},
		{name: "unknown field", body: `{"workspace_id":"` + workspace + `","action":"RESUME","expected_version":3,"unknown":true}`, contentType: "application/json", key: "decision-1", status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "duplicate field", body: `{"workspace_id":"` + workspace + `","action":"RESUME","action":"RESUME","expected_version":3}`, contentType: "application/json", key: "decision-1", status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "trailing object", body: `{"workspace_id":"` + workspace + `","action":"RESUME","expected_version":3}{}`, contentType: "application/json", key: "decision-1", status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "null required", body: `{"workspace_id":null,"action":"RESUME","expected_version":3}`, contentType: "application/json", key: "decision-1", status: http.StatusBadRequest, code: errorCodeSemanticLinkRequestInvalid},
		{name: "null reason", body: `{"workspace_id":"` + workspace + `","action":"IGNORE","expected_version":3,"reason":null}`, contentType: "application/json", key: "decision-1", status: http.StatusBadRequest, code: errorCodeSemanticLinkRequestInvalid},
		{name: "null relation type", body: `{"workspace_id":"` + workspace + `","action":"CONFIRM_WITH_RELATION_TYPE","expected_version":3,"relation_type":null}`, contentType: "application/json", key: "decision-1", status: http.StatusBadRequest, code: errorCodeSemanticLinkRequestInvalid},
		{name: "missing key", body: `{"workspace_id":"` + workspace + `","action":"RESUME","expected_version":3}`, contentType: "application/json", status: http.StatusBadRequest, code: errorCodeSemanticLinkRequestInvalid},
		{name: "spaced key", body: `{"workspace_id":"` + workspace + `","action":"RESUME","expected_version":3}`, contentType: "application/json", key: " decision-1 ", status: http.StatusBadRequest, code: errorCodeSemanticLinkRequestInvalid},
		{name: "confirm extra", body: `{"workspace_id":"` + workspace + `","action":"CONFIRM","expected_version":3,"reason":"not allowed"}`, contentType: "application/json", key: "decision-1", status: http.StatusBadRequest, code: errorCodeSemanticLinkRequestInvalid},
		{name: "typed confirm missing type", body: `{"workspace_id":"` + workspace + `","action":"CONFIRM_WITH_RELATION_TYPE","expected_version":3}`, contentType: "application/json", key: "decision-1", status: http.StatusBadRequest, code: errorCodeSemanticLinkRequestInvalid},
		{name: "ignore missing reason", body: `{"workspace_id":"` + workspace + `","action":"IGNORE","expected_version":3}`, contentType: "application/json", key: "decision-1", status: http.StatusBadRequest, code: errorCodeSemanticLinkRequestInvalid},
		{name: "bad timestamp", body: `{"workspace_id":"` + workspace + `","action":"DEFER","expected_version":3,"deferred_until":"tomorrow"}`, contentType: "application/json", key: "decision-1", status: http.StatusBadRequest, code: errorCodeSemanticLinkRequestInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeCandidateService{}
			response := serveCandidateRequest(t, service, time.Second, http.MethodPost, path, test.body, test.contentType, test.key)
			requireCandidateProblem(t, response, test.status, test.code, false)
			if service.decideCalls != 0 {
				t.Fatalf("service called for invalid request")
			}
		})
	}

	service := &fakeCandidateService{}
	router := candidateHTTPTestRouter(service, time.Second)
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"workspace_id":"`+workspace+`","action":"RESUME","expected_version":3}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header["Idempotency-Key"] = []string{"decision-1", "decision-2"}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	requireCandidateProblem(t, response, http.StatusBadRequest, errorCodeSemanticLinkRequestInvalid, false)
	if service.decideCalls != 0 {
		t.Fatal("service called for duplicate Idempotency-Key headers")
	}
}

func TestCandidateErrorMappingAndResponseIsolation(t *testing.T) {
	path := "/api/v1/graph/candidates/" + string(candidateHTTPID) + "?workspace_id=" + string(candidateHTTPWorkspaceID)
	tests := []struct {
		name      string
		err       error
		status    int
		code      string
		retryable bool
	}{
		{name: "not found", err: foundation.NewError(foundation.ErrorNotFound, graphdomain.ErrorCodeSemanticLinkCandidateNotFound, false, errors.New("secret id")), status: http.StatusNotFound, code: errorCodeSemanticLinkNotFound},
		{name: "version conflict", err: foundation.NewError(foundation.ErrorVersionConflict, graphdomain.ErrorCodeSemanticLinkCandidateTransitionInvalid, false, errors.New("secret version")), status: http.StatusConflict, code: errorCodeSemanticLinkVersionConflict},
		{name: "cursor invalid", err: foundation.NewError(foundation.ErrorInvalidInput, graphdomain.ErrorCodeCursorInvalid, false, errors.New("secret cursor")), status: http.StatusBadRequest, code: errorCodeSemanticLinkCursorInvalid},
		{name: "cursor stale", err: foundation.NewError(foundation.ErrorVersionConflict, graphdomain.ErrorCodeCursorStale, false, errors.New("secret cursor")), status: http.StatusConflict, code: errorCodeSemanticLinkCursorStale},
		{name: "proposal binding conflict", err: foundation.NewError(foundation.ErrorConsistencyViolation, "RELATION_PROPOSAL_CONFIRM_CONSISTENCY", false, errors.New("secret proposal binding")), status: http.StatusConflict, code: errorCodeSemanticLinkVersionConflict},
		{name: "projection corruption", err: foundation.NewError(foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeProjectionInconsistent, false, errors.New("secret row")), status: http.StatusInternalServerError, code: errorCodeSemanticLinkResultInvalid},
		{name: "dependency", err: foundation.NewError(foundation.ErrorDependencyUnavailable, "DATABASE_UNAVAILABLE", true, errors.New("postgres://secret")), status: http.StatusServiceUnavailable, code: errorCodeSemanticLinkDependencyUnavailable, retryable: true},
		{name: "internal", err: errors.New("postgres://secret"), status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeCandidateService{err: test.err}
			response := serveCandidateRequest(t, service, time.Second, http.MethodGet, path, "", "", "")
			requireCandidateProblem(t, response, test.status, test.code, test.retryable)
			if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "postgres://") {
				t.Fatalf("problem leaked internal cause: %s", response.Body.String())
			}
		})
	}

	crossWorkspace := candidateHTTPFixture(t)
	crossWorkspace.WorkspaceID = candidateHTTPOtherWorkspace
	response := serveCandidateRequest(t, &fakeCandidateService{candidate: crossWorkspace}, time.Second, http.MethodGet, path, "", "", "")
	requireCandidateProblem(t, response, http.StatusInternalServerError, errorCodeSemanticLinkResultInvalid, false)

	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	invalidReceipt := graphapp.CandidateDecisionReceipt{
		ID: candidateHTTPDecisionID, CandidateID: candidateHTTPID, WorkspaceID: candidateHTTPOtherWorkspace,
		Action: graphdomain.SemanticLinkCandidateDecisionResume, Status: graphdomain.SemanticLinkCandidateStatusActive,
		Version: 4, CreatedAt: now, UpdatedAt: now,
	}
	decisionPath := "/api/v1/graph/candidates/" + string(candidateHTTPID) + "/decisions"
	decisionBody := `{"workspace_id":"` + string(candidateHTTPWorkspaceID) + `","action":"RESUME","expected_version":3}`
	decisionResponse := serveCandidateRequest(t, &fakeCandidateService{receipt: invalidReceipt}, time.Second, http.MethodPost, decisionPath, decisionBody, "application/json", "decision-1")
	requireCandidateProblem(t, decisionResponse, http.StatusInternalServerError, errorCodeSemanticLinkResultInvalid, false)
}

func TestCandidateUnavailableIsIndependentAndFailClosed(t *testing.T) {
	path := "/api/v1/graph/candidates?workspace_id=" + string(candidateHTTPWorkspaceID)
	response := serveCandidateRequest(t, nil, time.Second, http.MethodGet, path, "", "", "")
	requireCandidateProblem(t, response, http.StatusServiceUnavailable, errorCodeSemanticLinkDependencyUnavailable, true)

	var typedNil *fakeCandidateService
	handler := NewCandidateHandler(typedNil, time.Second)
	if handler.Available() {
		t.Fatal("typed nil candidate service must be unavailable")
	}
}

func candidateHTTPFixture(t *testing.T) graphdomain.SemanticLinkCandidate {
	t.Helper()
	now := time.Date(2026, 7, 21, 7, 0, 0, 0, time.UTC)
	reopenedFrom := candidateHTTPReopenedFromID
	ruleID := candidateHTTPRuleID
	candidate := graphdomain.SemanticLinkCandidate{
		ID: candidateHTTPID, WorkspaceID: candidateHTTPWorkspaceID,
		Source: graphdomain.SemanticLinkCandidateEndpoint{
			Ref: knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: candidateHTTPSourceID}, Version: 3, Summary: "Claim summary", Excerpt: "Claim excerpt",
		},
		Target: graphdomain.SemanticLinkCandidateEndpoint{
			Ref: knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: candidateHTTPTargetID}, Version: 2, Summary: "Topic summary", Excerpt: "Topic excerpt",
		},
		SuggestedRelationType: knowledge.RelationBelongsTo,
		Status:                graphdomain.SemanticLinkCandidateStatusActive,
		Reason:                "The claim belongs to the topic", Confidence: 0.91,
		DiscoveryMethods: []graphdomain.SemanticLinkDiscoveryMethod{
			graphdomain.SemanticLinkDiscoveryMethodCommonTopic, graphdomain.SemanticLinkDiscoveryMethodTermMatch,
		},
		Evidence: []graphdomain.SemanticLinkCandidateEvidence{{
			ID:           candidateHTTPEvidenceID,
			Provenance:   knowledge.ProvenanceRef{WorkspaceID: candidateHTTPWorkspaceID, SourceVersionID: candidateHTTPSourceVersionID, SourceSpanID: candidateHTTPSourceSpanID},
			SemanticHash: strings.Repeat("b", 64), Reason: "The source span supports the link", Excerpt: "shared bounded terminology",
		}},
		Generation:              graphdomain.SemanticLinkCandidateGeneration{RuleID: &ruleID, RuleVersion: "rule-v1"},
		ReopenedFromCandidateID: &reopenedFrom,
		ReopenedReason:          graphdomain.SemanticLinkCandidateReopenedReasonContentChanged,
		Version:                 3, CreatedAt: now, UpdatedAt: now.Add(time.Minute),
	}
	fingerprint, err := graphdomain.ComputeSemanticLinkCandidateFingerprint(candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Fingerprint = fingerprint
	if err := graphdomain.ValidateSemanticLinkCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	return candidate
}

type fakeCandidateService struct {
	page      graphdomain.SemanticLinkCandidatePage
	candidate graphdomain.SemanticLinkCandidate
	receipt   graphapp.CandidateDecisionReceipt
	err       error

	listRequest  graphapp.CandidateListRequest
	listDeadline time.Time
	listCalls    int

	getWorkspaceID foundation.ID
	getCandidateID foundation.ID
	getCalls       int

	command     graphapp.SemanticLinkCandidateDecisionCommand
	decideCalls int
}

type fakeCandidateScanService struct {
	start graphapp.SemanticLinkScanStartResult
	scan  graphdomain.SemanticLinkScan
	err   error

	startRequest   graphapp.SemanticLinkTopicScanRequest
	startCalls     int
	getWorkspaceID foundation.ID
	getScanID      foundation.ID
	getCalls       int
}

func (service *fakeCandidateScanService) StartTopicScan(_ context.Context, request graphapp.SemanticLinkTopicScanRequest) (graphapp.SemanticLinkScanStartResult, error) {
	service.startCalls++
	service.startRequest = request
	return service.start, service.err
}

func (service *fakeCandidateScanService) Get(_ context.Context, workspaceID, scanID foundation.ID) (graphdomain.SemanticLinkScan, error) {
	service.getCalls++
	service.getWorkspaceID, service.getScanID = workspaceID, scanID
	return service.scan, service.err
}

func (service *fakeCandidateService) List(ctx context.Context, request graphapp.CandidateListRequest) (graphdomain.SemanticLinkCandidatePage, error) {
	service.listCalls++
	service.listRequest = request
	service.listDeadline, _ = ctx.Deadline()
	return service.page, service.err
}

func (service *fakeCandidateService) Get(_ context.Context, workspaceID, candidateID foundation.ID) (graphdomain.SemanticLinkCandidate, error) {
	service.getCalls++
	service.getWorkspaceID, service.getCandidateID = workspaceID, candidateID
	return service.candidate, service.err
}

func (service *fakeCandidateService) Decide(_ context.Context, command graphapp.SemanticLinkCandidateDecisionCommand) (graphapp.CandidateDecisionReceipt, error) {
	service.decideCalls++
	service.command = command
	return service.receipt, service.err
}

func serveCandidateRequest(t *testing.T, service graphapp.CandidateService, timeout time.Duration, method, path, body, contentType, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	router := candidateHTTPTestRouter(service, timeout)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func candidateHTTPTestRouter(service graphapp.CandidateService, timeout time.Duration) http.Handler {
	router := gin.New()
	NewCandidateHandler(service, timeout).Routes(router.Group("/api/v1"))
	return router
}

func serveCandidateScanRequest(t *testing.T, scans graphapp.SemanticLinkScanHTTPService, method, path, body, contentType, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	NewCandidateHandler(&fakeCandidateService{}, time.Second, scans).Routes(router.Group("/api/v1"))
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func candidateHTTPScanFixture(now time.Time) graphdomain.SemanticLinkScan {
	return graphdomain.SemanticLinkScan{
		ID: candidateHTTPScanID, WorkspaceID: candidateHTTPWorkspaceID,
		Scope:       graphdomain.SemanticLinkScanScope{Type: graphdomain.SemanticLinkScanScopeTopic, Ref: string(candidateHTTPTopicID), Version: 3, SchemaVersion: graphapp.SemanticLinkTopicScanScopeSchemaVersion},
		Fingerprint: strings.Repeat("a", 64), IdempotencyKey: "scan-topic-1", RequestHash: strings.Repeat("b", 64),
		WorkflowRunID: candidateHTTPWorkflowRunID, Status: graphdomain.SemanticLinkScanStatusRunning,
		TotalNodes: 5, ProcessedNodes: 2, CandidateCount: 1, SuppressedCount: 1,
		Checkpoint: graphdomain.SemanticLinkScanCheckpoint{ProcessedPage: 1}, Version: 2,
		CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
}

func decodeCandidateResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func requireCandidateProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string, retryable bool) httpapi.Problem {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	var problem httpapi.Problem
	decodeCandidateResponse(t, response, &problem)
	if problem.ErrorCode != code || problem.Retryable != retryable {
		t.Fatalf("problem=%#v want code=%s retryable=%v", problem, code, retryable)
	}
	return problem
}

var _ graphapp.CandidateService = (*fakeCandidateService)(nil)
var _ graphapp.SemanticLinkScanHTTPService = (*fakeCandidateScanService)(nil)
