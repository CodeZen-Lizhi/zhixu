package changecontrolhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/gin-gonic/gin"
)

const (
	testWorkspaceID     foundation.ID = "10000000-0000-4000-8000-000000000001"
	testProposalID      foundation.ID = "20000000-0000-4000-8000-000000000001"
	testRevisionID      foundation.ID = "30000000-0000-4000-8000-000000000001"
	testApprovalID      foundation.ID = "40000000-0000-4000-8000-000000000001"
	testWorkflowRunID   foundation.ID = "50000000-0000-4000-8000-000000000001"
	testNodeRunID       foundation.ID = "60000000-0000-4000-8000-000000000001"
	testChangeHash                    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testApprovedGitHead               = "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
)

func TestCreateProposalContract(t *testing.T) {
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	service := &fakeService{proposal: domain.Proposal{
		ID: testProposalID, WorkspaceID: testWorkspaceID, TargetPath: "notes/a.md", RiskLevel: domain.ProposalRiskLevelLow, Status: domain.StatusReady,
		CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{ID: testRevisionID, ProposalID: testProposalID, RevisionNo: 1, TargetPath: "notes/a.md", BaseHash: testChangeHash, Content: "new", EvidenceSummary: "e", Risk: "reviewer narrative", RollbackPlan: "r", ChangeHash: testChangeHash, CreatedAt: now},
	}}
	recorder := serve(t, service, http.MethodPost, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals", `{"target_path":"notes/a.md","base_hash":"`+testChangeHash+`","content":"new","evidence_summary":"e","risk_level":"LOW","risk":"reviewer narrative","rollback_plan":"r"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := append([]byte(nil), recorder.Body.Bytes()...)
	var response proposalResponse
	decode(t, recorder, &response)
	if response.ProposalType != string(domain.ProposalTypeFilePatch) || response.ID != string(testProposalID) || response.RiskLevel != string(domain.ProposalRiskLevelLow) || service.create.WorkspaceID != testWorkspaceID || service.create.TargetPath != "notes/a.md" || service.create.RiskLevel != domain.ProposalRiskLevelLow || service.create.Risk != "reviewer narrative" {
		t.Fatalf("response=%#v command=%#v", response, service.create)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	approval, exists := raw["approval"]
	if !exists || approval != nil {
		t.Fatalf("created proposal approval must be explicit null: %#v", raw)
	}
}

func TestCreateProposalReplayReturnsOK(t *testing.T) {
	service := &fakeService{proposal: domain.Proposal{ID: testProposalID, RiskLevel: domain.ProposalRiskLevelLow}, replayed: true}
	recorder := serve(t, service, http.MethodPost, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals", `{"target_path":"notes/a.md","base_hash":"`+testChangeHash+`","content":"new","evidence_summary":"e","risk_level":"LOW","risk":"reviewer narrative","rollback_plan":"r"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateProposalRequiresExactRiskLevel(t *testing.T) {
	tests := []struct {
		name      string
		riskField string
	}{
		{name: "missing", riskField: ""},
		{name: "lowercase", riskField: `,"risk_level":"low"`},
		{name: "surrounding whitespace", riskField: `,"risk_level":" LOW "`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeService{}
			body := `{"target_path":"notes/a.md","base_hash":"` + testChangeHash + `","content":"new","evidence_summary":"e","risk":"free text","rollback_plan":"r"` + test.riskField + `}`
			recorder := serve(t, service, http.MethodPost, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals", body)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "PROPOSAL_RISK_LEVEL_INVALID") || service.createCalls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", recorder.Code, service.createCalls, recorder.Body.String())
			}
		})
	}
}

func TestCreateProposalRequiresIdempotencyKey(t *testing.T) {
	router := gin.New()
	NewHandler(&fakeService{}).Routes(router.Group("/api/v1"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals", strings.NewReader(`{"target_path":"a.md"}`))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "IDEMPOTENCY_KEY_REQUIRED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateDownstreamUpdateProposalContract(t *testing.T) {
	now := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	proposal := downstreamUpdateProposalFixture(t, now, knowledge.ImpactObjectArtifact)
	update := *proposal.Revision.DownstreamUpdate
	service := &fakeService{proposal: proposal}
	recorder := serve(t, service, http.MethodPost,
		"/api/v1/workspaces/"+string(testWorkspaceID)+"/impact-reports/"+string(update.ReportID)+"/proposals",
		`{"target_type":"ARTIFACT","target_id":"`+string(update.TargetID)+`","action":"REGENERATE_ARTIFACT"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.downstreamCreateCalls != 1 || service.downstreamCreate.WorkspaceID != testWorkspaceID || service.downstreamCreate.ReportID != update.ReportID || service.downstreamCreate.TargetType != update.TargetType || service.downstreamCreate.TargetID != update.TargetID || service.downstreamCreate.Action != update.Action || service.downstreamCreate.IdempotencyKey != "test-create" {
		t.Fatalf("command=%#v calls=%d", service.downstreamCreate, service.downstreamCreateCalls)
	}
	var response map[string]any
	decode(t, recorder, &response)
	assertJSONFields(t, "create response", response, "proposal_type", "id", "workspace_id", "status", "risk_level", "version", "revision_capability", "revision", "approval", "created_at", "updated_at", "replayed")
	if response["proposal_type"] != string(domain.ProposalTypeDownstreamUpdate) || response["risk_level"] != string(domain.ProposalRiskLevelHigh) || response["replayed"] != false {
		t.Fatalf("proposal=%#v", response)
	}
	if _, exists := response["target_path"]; exists {
		t.Fatalf("downstream proposal leaked file target: %#v", response)
	}
	if approval, exists := response["approval"]; !exists || approval != nil {
		t.Fatalf("ready downstream proposal approval must be explicit null: %#v", response)
	}
	revision, ok := response["revision"].(map[string]any)
	if !ok {
		t.Fatalf("revision=%#v", response["revision"])
	}
	assertJSONFields(t, "revision", revision, "id", "revision_no", "update", "risk", "rollback_plan", "change_hash", "created_at")
	payload, ok := revision["update"].(map[string]any)
	if !ok || payload["workspace_id"] != string(testWorkspaceID) || payload["target_type"] != "ARTIFACT" || payload["target_id"] != string(update.TargetID) || payload["action"] != "REGENERATE_ARTIFACT" || payload["schema_version"] != domain.DownstreamUpdateSchemaVersion {
		t.Fatalf("update=%#v", revision["update"])
	}
	assertJSONFields(t, "artifact update", payload, "workspace_id", "source_report", "source_event", "target_type", "target_id", "base_version", "action", "artifact_binding", "reason", "schema_version")
	sourceReport := payload["source_report"].(map[string]any)
	assertJSONFields(t, "source report", sourceReport, "id", "analysis_version", "fingerprint")
	if sourceReport["id"] != string(update.ReportID) || sourceReport["analysis_version"] != string(knowledge.ImpactAnalysisVersionV2) || sourceReport["fingerprint"] != testChangeHash {
		t.Fatalf("source_report=%#v", sourceReport)
	}
	sourceEvent := payload["source_event"].(map[string]any)
	assertJSONFields(t, "source event", sourceEvent, "id", "event_version")
	if sourceEvent["id"] != string(update.SourceEventID) || sourceEvent["event_version"] != float64(update.SourceEventVersion) {
		t.Fatalf("source_event=%#v", sourceEvent)
	}
	artifact, ok := payload["artifact_binding"].(map[string]any)
	if !ok || artifact["artifact_id"] != string(update.TargetID) || artifact["content_hash"] != testChangeHash {
		t.Fatalf("artifact_binding=%#v", payload["artifact_binding"])
	}
	assertJSONFields(t, "artifact binding", artifact, "artifact_id", "artifact_version", "revision_id", "revision_no", "content_hash")
	if _, exists := payload["review_card_binding"]; exists {
		t.Fatalf("artifact payload contains review binding: %#v", payload)
	}
}

func TestCreateDownstreamUpdateProposalReplayReturnsOK(t *testing.T) {
	proposal := downstreamUpdateProposalFixture(t, time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC), knowledge.ImpactObjectArtifact)
	update := *proposal.Revision.DownstreamUpdate
	service := &fakeService{proposal: proposal, replayed: true}
	recorder := serve(t, service, http.MethodPost,
		"/api/v1/workspaces/"+string(testWorkspaceID)+"/impact-reports/"+string(update.ReportID)+"/proposals",
		`{"target_type":"ARTIFACT","target_id":"`+string(update.TargetID)+`","action":"REGENERATE_ARTIFACT"}`)
	if recorder.Code != http.StatusOK || service.downstreamCreateCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, service.downstreamCreateCalls, recorder.Body.String())
	}
	var response map[string]any
	decode(t, recorder, &response)
	if response["replayed"] != true {
		t.Fatalf("response=%#v", response)
	}
}

func TestCreateDownstreamUpdateProposalRejectsInvalidRequestsBeforeService(t *testing.T) {
	proposal := downstreamUpdateProposalFixture(t, time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC), knowledge.ImpactObjectArtifact)
	update := *proposal.Revision.DownstreamUpdate
	path := "/api/v1/workspaces/" + string(testWorkspaceID) + "/impact-reports/" + string(update.ReportID) + "/proposals"
	validBody := `{"target_type":"ARTIFACT","target_id":"` + string(update.TargetID) + `","action":"REGENERATE_ARTIFACT"}`
	tests := []struct {
		name        string
		path        string
		body        string
		contentType string
		keys        []string
		status      int
		code        string
	}{
		{name: "unsupported media type", path: path, body: validBody, contentType: "text/plain", keys: []string{"key"}, status: http.StatusUnsupportedMediaType, code: "UNSUPPORTED_MEDIA_TYPE"},
		{name: "empty body", path: path, body: "", contentType: "application/json", keys: []string{"key"}, status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "unknown field", path: path, body: validBody[:len(validBody)-1] + `,"extra":true}`, contentType: "application/json", keys: []string{"key"}, status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "duplicate field", path: path, body: `{"target_type":"ARTIFACT","target_type":"ARTIFACT","target_id":"` + string(update.TargetID) + `","action":"REGENERATE_ARTIFACT"}`, contentType: "application/json", keys: []string{"key"}, status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "trailing value", path: path, body: validBody + ` {}`, contentType: "application/json", keys: []string{"key"}, status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "unsupported target", path: path, body: `{"target_type":"CLAIM","target_id":"` + string(update.TargetID) + `","action":"REGENERATE_ARTIFACT"}`, contentType: "application/json", keys: []string{"key"}, status: http.StatusBadRequest, code: "DOWNSTREAM_UPDATE_PROPOSAL_INVALID"},
		{name: "mismatched action", path: path, body: `{"target_type":"ARTIFACT","target_id":"` + string(update.TargetID) + `","action":"REVALIDATE_REVIEW_CARD"}`, contentType: "application/json", keys: []string{"key"}, status: http.StatusBadRequest, code: "DOWNSTREAM_UPDATE_PROPOSAL_INVALID"},
		{name: "noncanonical target ID", path: path, body: `{"target_type":"ARTIFACT","target_id":"A0000000-0000-4000-8000-000000000003","action":"REGENERATE_ARTIFACT"}`, contentType: "application/json", keys: []string{"key"}, status: http.StatusBadRequest, code: "DOWNSTREAM_UPDATE_PROPOSAL_INVALID"},
		{name: "missing idempotency key", path: path, body: validBody, contentType: "application/json", status: http.StatusBadRequest, code: "IDEMPOTENCY_KEY_REQUIRED"},
		{name: "duplicate idempotency key", path: path, body: validBody, contentType: "application/json", keys: []string{"key-a", "key-b"}, status: http.StatusBadRequest, code: "IDEMPOTENCY_KEY_REQUIRED"},
		{name: "padded idempotency key", path: path, body: validBody, contentType: "application/json", keys: []string{" key"}, status: http.StatusBadRequest, code: "DOWNSTREAM_UPDATE_PROPOSAL_INVALID"},
		{name: "oversized idempotency key", path: path, body: validBody, contentType: "application/json", keys: []string{strings.Repeat("k", 129)}, status: http.StatusBadRequest, code: "DOWNSTREAM_UPDATE_PROPOSAL_INVALID"},
		{name: "control idempotency key", path: path, body: validBody, contentType: "application/json", keys: []string{"key\tvalue"}, status: http.StatusBadRequest, code: "DOWNSTREAM_UPDATE_PROPOSAL_INVALID"},
		{name: "unexpected query", path: path + "?limit=1", body: validBody, contentType: "application/json", keys: []string{"key"}, status: http.StatusBadRequest, code: "DOWNSTREAM_UPDATE_PROPOSAL_INVALID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeService{proposal: proposal}
			recorder := serveRequest(t, service, http.MethodPost, test.path, test.body, test.contentType, test.keys)
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.code) || service.downstreamCreateCalls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", recorder.Code, service.downstreamCreateCalls, recorder.Body.String())
			}
		})
	}
}

func TestCreateDownstreamUpdateProposalMapsImpactFailures(t *testing.T) {
	proposal := downstreamUpdateProposalFixture(t, time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC), knowledge.ImpactObjectArtifact)
	update := *proposal.Revision.DownstreamUpdate
	path := "/api/v1/workspaces/" + string(testWorkspaceID) + "/impact-reports/" + string(update.ReportID) + "/proposals"
	body := `{"target_type":"ARTIFACT","target_id":"` + string(update.TargetID) + `","action":"REGENERATE_ARTIFACT"}`
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "not found", err: foundation.NewError(foundation.ErrorNotFound, "KNOWLEDGE_IMPACT_NOT_FOUND", false, errors.New("not found")), status: http.StatusNotFound, code: "KNOWLEDGE_IMPACT_NOT_FOUND"},
		{name: "unsupported target", err: foundation.NewError(foundation.ErrorInvalidInput, "DOWNSTREAM_UPDATE_TARGET_INVALID", false, errors.New("unsupported")), status: http.StatusBadRequest, code: "DOWNSTREAM_UPDATE_TARGET_INVALID"},
		{name: "stale binding", err: foundation.NewError(foundation.ErrorVersionConflict, "KNOWLEDGE_IMPACT_CONFLICT", false, errors.New("stale")), status: http.StatusConflict, code: "KNOWLEDGE_IMPACT_CONFLICT"},
		{name: "idempotency conflict", err: foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("different binding")), status: http.StatusConflict, code: "IDEMPOTENCY_KEY_REUSED"},
		{name: "owner unavailable", err: foundation.NewError(foundation.ErrorDependencyUnavailable, "KNOWLEDGE_IMPACT_UNAVAILABLE", false, errors.New("unavailable")), status: http.StatusServiceUnavailable, code: "KNOWLEDGE_IMPACT_UNAVAILABLE"},
		{name: "deadline", err: context.DeadlineExceeded, status: http.StatusServiceUnavailable, code: "KNOWLEDGE_IMPACT_UNAVAILABLE"},
		{name: "wrapped cancellation", err: foundation.NewError(foundation.ErrorNonRetryableFailure, "DOWNSTREAM_UPDATE_SOURCE_QUERY_FAILED", false, context.Canceled), status: http.StatusServiceUnavailable, code: "KNOWLEDGE_IMPACT_UNAVAILABLE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serve(t, &fakeService{proposal: proposal, err: test.err}, http.MethodPost, path, body)
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.code) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCreateDownstreamUpdateProposalEnforcesConfiguredTimeout(t *testing.T) {
	service := &fakeService{downstreamWaitForContext: true}
	router := gin.New()
	NewHandlerWithTimeout(service, 10*time.Millisecond).Routes(router.Group("/api/v1"))
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/workspaces/"+string(testWorkspaceID)+"/impact-reports/"+string(testRevisionID)+"/proposals",
		strings.NewReader(`{"target_type":"ARTIFACT","target_id":"`+string(testProposalID)+`","action":"REGENERATE_ARTIFACT"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "timeout-command")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), knowledge.ErrorCodeImpactUnavailable) || !errors.Is(service.downstreamContextErr, context.DeadlineExceeded) || service.downstreamCreateCalls != 1 {
		t.Fatalf("status=%d context_err=%v calls=%d body=%s", recorder.Code, service.downstreamContextErr, service.downstreamCreateCalls, recorder.Body.String())
	}
}

func TestApprovalAndPreflightContracts(t *testing.T) {
	approvedGitHead := testApprovedGitHead
	service := &fakeService{
		decisionResult: application.ApprovalDecisionResult{
			Approval: domain.Approval{ID: testApprovalID, ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, Decision: domain.DecisionApproved, ApprovedGitHead: &approvedGitHead},
			Workflow: &application.ApprovalWorkflowDispatch{RunID: testWorkflowRunID, NodeID: testNodeRunID, JobID: 42, Status: application.DispatchStatusQueued},
		},
		preflight: application.ApplyPreflightResult{ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, TargetMode: domain.TargetModeReplace, BaseHash: testChangeHash},
	}
	approval := serve(t, service, http.MethodPost, "/api/v1/proposals/"+string(testProposalID)+"/approvals", `{"revision_id":"`+string(testRevisionID)+`","change_hash":"`+testChangeHash+`","decision":"approved"}`)
	if approval.Code != http.StatusCreated || service.decision != domain.DecisionApproved {
		t.Fatalf("approval status=%d body=%s", approval.Code, approval.Body.String())
	}
	var responseApproval approvalDecisionResponse
	decode(t, approval, &responseApproval)
	if responseApproval.ApprovedGitHead == nil || *responseApproval.ApprovedGitHead != strings.ToLower(testApprovedGitHead) || responseApproval.WorkflowRunID != string(testWorkflowRunID) || responseApproval.WorkflowStatusURL != "/api/v1/workflows/"+string(testWorkflowRunID) || responseApproval.DispatchStatus != string(application.DispatchStatusQueued) {
		t.Fatalf("approval response=%#v", responseApproval)
	}
	preflight := serve(t, service, http.MethodPost, "/api/v1/proposals/"+string(testProposalID)+"/apply-preflight", `{"revision_id":"`+string(testRevisionID)+`","approved_change_hash":"`+testChangeHash+`"}`)
	if preflight.Code != http.StatusOK {
		t.Fatalf("preflight status=%d body=%s", preflight.Code, preflight.Body.String())
	}
	var response map[string]any
	decode(t, preflight, &response)
	if response["preflight_passed"] != true || response["write_performed"] != false || response["mode"] != "preflight_only" || response["target_mode"] != string(domain.TargetModeReplace) || response["base_hash"] != testChangeHash {
		t.Fatalf("preflight response=%#v", response)
	}
	if _, exists := response["eligible"]; exists {
		t.Fatalf("deprecated authorization field exists: %#v", response)
	}
}

func TestApprovalAndPreflightExposeRetryableWorkflowCancellationFailure(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "approval", path: "/api/v1/proposals/" + string(testProposalID) + "/approvals", body: `{"revision_id":"` + string(testRevisionID) + `","change_hash":"` + testChangeHash + `","decision":"approved"}`},
		{name: "preflight", path: "/api/v1/proposals/" + string(testProposalID) + "/apply-preflight", body: `{"revision_id":"` + string(testRevisionID) + `","approved_change_hash":"` + testChangeHash + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeService{err: foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_REVISION_WORKFLOW_CANCEL_UNAVAILABLE", true, errors.New("secret workflow dependency"))}
			recorder := serve(t, service, http.MethodPost, test.path, test.body)
			if recorder.Code != http.StatusServiceUnavailable ||
				!strings.Contains(recorder.Body.String(), `"error_code":"PROPOSAL_REVISION_WORKFLOW_CANCEL_UNAVAILABLE"`) ||
				!strings.Contains(recorder.Body.String(), `"retryable":true`) || strings.Contains(recorder.Body.String(), "secret") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestProposalCurrentContentContract(t *testing.T) {
	service := &fakeService{currentContent: application.ProposalCurrentContent{
		ProposalID: testProposalID, WorkspaceID: testWorkspaceID, TargetPath: "notes/a.md", Content: "current",
		TargetMode: domain.TargetModeReplace, CurrentHash: testChangeHash, BaseHash: testChangeHash, BaseHashMatch: true,
	}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/proposals/"+string(testProposalID)+"/current-content", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("Cache-Control=%q", recorder.Header().Get("Cache-Control"))
	}
	var response proposalCurrentContentResponse
	decode(t, recorder, &response)
	if response.ProposalID != string(testProposalID) || response.WorkspaceID != string(testWorkspaceID) || response.TargetMode != string(domain.TargetModeReplace) || response.Content != "current" || !response.BaseHashMatch {
		t.Fatalf("response=%#v", response)
	}
}

func TestProposalListBindsFiltersToCursor(t *testing.T) {
	now := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	service := &fakeService{listItems: []domain.ProposalListItem{{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusReady, RiskLevel: domain.ProposalRiskLevelLow, RevisionID: testRevisionID, Version: 1, RevisionCapability: listCapability(domain.ProposalTypeFilePatch, domain.StatusReady), CreatedAt: now, UpdatedAt: now}}, listHasMore: true}
	first := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals?status=ready_for_review&proposal_type=file_patch&limit=1", "")
	if first.Code != http.StatusOK || service.listQuery.Status != domain.StatusReady || service.listQuery.Type != domain.ProposalTypeFilePatch {
		t.Fatalf("status=%d query=%+v body=%s", first.Code, service.listQuery, first.Body.String())
	}
	var page proposalPageResponse
	decode(t, first, &page)
	if len(page.Items) != 1 || page.Items[0].Version != 1 || !page.Items[0].RevisionCapability.Editable || page.Items[0].RevisionCapability.Reason != string(domain.ProposalRevisionAvailable) {
		t.Fatalf("page=%#v", page)
	}
	legacyCursor := cursorWithoutProposalKind(t, page.NextCursor)
	legacy := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals?status=ready_for_review&proposal_type=file_patch&limit=1&cursor="+legacyCursor, "")
	if legacy.Code != http.StatusBadRequest || service.listCalls != 1 {
		t.Fatalf("legacy status=%d calls=%d body=%s", legacy.Code, service.listCalls, legacy.Body.String())
	}
	second := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals?status=rejected&proposal_type=file_patch&limit=1&cursor="+page.NextCursor, "")
	if second.Code != http.StatusBadRequest || service.listCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", second.Code, service.listCalls, second.Body.String())
	}
}

func TestProposalListRejectsInvalidRevisionCapabilityProjection(t *testing.T) {
	tests := []struct {
		name       string
		version    int64
		capability domain.ProposalRevisionCapability
	}{
		{name: "missing version", capability: listCapability(domain.ProposalTypeFilePatch, domain.StatusReady)},
		{name: "missing capability", version: 1},
		{name: "inconsistent capability", version: 1, capability: domain.ProposalRevisionCapability{Editable: true, Reason: domain.ProposalRevisionStatusNotEditable}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeService{listItems: []domain.ProposalListItem{{
				ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch,
				Status: domain.StatusReady, RiskLevel: domain.ProposalRiskLevelLow, RevisionID: testRevisionID,
				Version: test.version, RevisionCapability: test.capability,
			}}}
			recorder := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals", "")
			if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "PROPOSAL_REVISION_CAPABILITY_INVALID") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestProposalListRejectsLowercaseRiskFilter(t *testing.T) {
	service := &fakeService{listItems: []domain.ProposalListItem{{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusReady, RiskLevel: domain.ProposalRiskLevelHigh, RevisionID: testRevisionID, Version: 1, RevisionCapability: listCapability(domain.ProposalTypeFilePatch, domain.StatusReady)}}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals?risk=high", "")
	if recorder.Code != http.StatusBadRequest || service.listCalls != 0 {
		t.Fatalf("status=%d risk=%q body=%s", recorder.Code, service.listQuery.RiskLevel, recorder.Body.String())
	}
}

func TestProposalListRejectsPaddedRiskFilter(t *testing.T) {
	service := &fakeService{listItems: []domain.ProposalListItem{{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusReady, RiskLevel: domain.ProposalRiskLevelHigh, RevisionID: testRevisionID}}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals?risk=%20HIGH%20", "")
	if recorder.Code != http.StatusBadRequest || service.listCalls != 0 {
		t.Fatalf("status=%d risk=%q body=%s", recorder.Code, service.listQuery.RiskLevel, recorder.Body.String())
	}
}

func TestProposalListAcceptsUppercaseRiskFilter(t *testing.T) {
	service := &fakeService{listItems: []domain.ProposalListItem{{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusReady, RiskLevel: domain.ProposalRiskLevelHigh, RevisionID: testRevisionID, Version: 1, RevisionCapability: listCapability(domain.ProposalTypeFilePatch, domain.StatusReady)}}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals?risk=HIGH", "")
	if recorder.Code != http.StatusOK || service.listQuery.RiskLevel != domain.ProposalRiskLevelHigh {
		t.Fatalf("status=%d risk=%q body=%s", recorder.Code, service.listQuery.RiskLevel, recorder.Body.String())
	}
}

func TestProposalListAcceptsPublishArtifactFilter(t *testing.T) {
	now := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)
	service := &fakeService{listItems: []domain.ProposalListItem{{
		ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypePublishArtifact,
		Status: domain.StatusReady, Target: "知识关系", RiskLevel: domain.ProposalRiskLevelHigh, Risk: "publish approved artifact",
		RevisionID: testRevisionID, ChangeHash: testChangeHash, Version: 1, RevisionCapability: listCapability(domain.ProposalTypePublishArtifact, domain.StatusReady), CreatedAt: now, UpdatedAt: now,
	}}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals?proposal_type=publish_artifact", "")
	if recorder.Code != http.StatusOK || service.listQuery.Type != domain.ProposalTypePublishArtifact {
		t.Fatalf("status=%d type=%q body=%s", recorder.Code, service.listQuery.Type, recorder.Body.String())
	}
	var response proposalPageResponse
	decode(t, recorder, &response)
	if len(response.Items) != 1 || response.Items[0].ProposalType != string(domain.ProposalTypePublishArtifact) || response.Items[0].Target != "Artifact 发布" {
		t.Fatalf("response=%#v", response)
	}
}

func TestProposalListAcceptsDownstreamUpdateFilter(t *testing.T) {
	now := time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC)
	targetID := foundation.ID("70000000-0000-4000-8000-000000000003")
	service := &fakeService{listItems: []domain.ProposalListItem{{
		ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeDownstreamUpdate,
		Status: domain.StatusReady, Target: "ARTIFACT:" + string(targetID), RiskLevel: domain.ProposalRiskLevelHigh,
		Risk: "owner-backed downstream update", RevisionID: testRevisionID, ChangeHash: testChangeHash, Version: 1, RevisionCapability: listCapability(domain.ProposalTypeDownstreamUpdate, domain.StatusReady), CreatedAt: now, UpdatedAt: now,
	}}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals?proposal_type=downstream_update", "")
	if recorder.Code != http.StatusOK || service.listQuery.Type != domain.ProposalTypeDownstreamUpdate {
		t.Fatalf("status=%d type=%q body=%s", recorder.Code, service.listQuery.Type, recorder.Body.String())
	}
	var response proposalPageResponse
	decode(t, recorder, &response)
	if len(response.Items) != 1 || response.Items[0].ProposalType != string(domain.ProposalTypeDownstreamUpdate) || response.Items[0].Target != "ARTIFACT:"+string(targetID) {
		t.Fatalf("response=%#v", response)
	}
}

func TestProposalListIncludesLegalDurableApprovalBindings(t *testing.T) {
	now := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	approvedGitHead := testApprovedGitHead
	service := &fakeService{listItems: []domain.ProposalListItem{
		{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusReady, RiskLevel: domain.ProposalRiskLevelLow, RevisionID: testRevisionID, ChangeHash: testChangeHash, Version: 1, RevisionCapability: listCapability(domain.ProposalTypeFilePatch, domain.StatusReady), CreatedAt: now, UpdatedAt: now},
		{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusRejected, RiskLevel: domain.ProposalRiskLevelLow, RevisionID: testRevisionID, ChangeHash: testChangeHash, Version: 1, RevisionCapability: listCapability(domain.ProposalTypeFilePatch, domain.StatusRejected), Approval: &domain.Approval{ID: testApprovalID, ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, Decision: domain.DecisionRejected, DecidedAt: now}, CreatedAt: now, UpdatedAt: now},
		{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusApproved, RiskLevel: domain.ProposalRiskLevelHigh, RevisionID: testRevisionID, ChangeHash: testChangeHash, Version: 1, RevisionCapability: listCapability(domain.ProposalTypeFilePatch, domain.StatusApproved), Approval: &domain.Approval{ID: testApprovalID, ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, Decision: domain.DecisionApproved, ApprovedGitHead: &approvedGitHead, DecidedAt: now}, WorkflowRunID: proposalWorkflowRunID(testWorkflowRunID), CreatedAt: now, UpdatedAt: now},
		{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeKnowledgeChange, Status: domain.StatusApproved, RiskLevel: domain.ProposalRiskLevelHigh, RevisionID: testRevisionID, ChangeHash: testChangeHash, Version: 1, RevisionCapability: listCapability(domain.ProposalTypeKnowledgeChange, domain.StatusApproved), Approval: &domain.Approval{ID: testApprovalID, ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, Decision: domain.DecisionApproved, DecidedAt: now}, CreatedAt: now, UpdatedAt: now},
	}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Items []map[string]any `json:"items"`
	}
	decode(t, recorder, &response)
	if len(response.Items) != 4 {
		t.Fatalf("items=%#v", response.Items)
	}
	if _, exists := response.Items[0]["approval"]; exists {
		t.Fatalf("unapproved item contains approval: %#v", response.Items[0])
	}
	rejected := response.Items[1]["approval"].(map[string]any)
	for _, field := range []string{"approved_git_head", "workflow_run_id", "workflow_status_url", "dispatch_status"} {
		if _, exists := rejected[field]; exists {
			t.Fatalf("rejected approval contains %s: %#v", field, rejected)
		}
	}
	approved := response.Items[2]["approval"].(map[string]any)
	if approved["workflow_run_id"] != string(testWorkflowRunID) || approved["workflow_status_url"] != "/api/v1/workflows/"+string(testWorkflowRunID) || approved["approved_git_head"] != strings.ToLower(testApprovedGitHead) {
		t.Fatalf("approved file patch binding=%#v", approved)
	}
	if _, exists := approved["dispatch_status"]; exists {
		t.Fatalf("durable approval snapshot contains dispatch_status: %#v", approved)
	}
	knowledge := response.Items[3]["approval"].(map[string]any)
	for _, field := range []string{"approved_git_head", "workflow_run_id", "workflow_status_url", "dispatch_status"} {
		if _, exists := knowledge[field]; exists {
			t.Fatalf("knowledge approval contains %s: %#v", field, knowledge)
		}
	}
}

func proposalWorkflowRunID(value foundation.ID) *foundation.ID {
	return &value
}

func cursorWithoutProposalKind(t *testing.T, cursor string) string {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload, "version")
	delete(payload, "kind")
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func TestApprovalResponseOmitsWorkflowFieldsForRejected(t *testing.T) {
	approvedGitHead := strings.ToLower(testApprovedGitHead)
	result := application.ApprovalDecisionResult{Approval: domain.Approval{ID: testApprovalID, ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, Decision: domain.DecisionRejected, ApprovedGitHead: &approvedGitHead}}
	recorder := serve(t, &fakeService{decisionResult: result}, http.MethodPost, "/api/v1/proposals/"+string(testProposalID)+"/approvals", `{"revision_id":"`+string(testRevisionID)+`","change_hash":"`+testChangeHash+`","decision":"rejected"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	decode(t, recorder, &response)
	for _, field := range []string{"approved_git_head", "workflow_run_id", "workflow_status_url", "dispatch_status"} {
		if _, exists := response[field]; exists {
			t.Fatalf("%s must be omitted: %#v", field, response)
		}
	}
}

func TestApprovalExactReplayReturnsOKAndSameWorkflow(t *testing.T) {
	approvedGitHead := strings.ToLower(testApprovedGitHead)
	result := application.ApprovalDecisionResult{
		Approval: domain.Approval{ID: testApprovalID, ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, Decision: domain.DecisionApproved, ApprovedGitHead: &approvedGitHead},
		Workflow: &application.ApprovalWorkflowDispatch{RunID: testWorkflowRunID, NodeID: testNodeRunID, JobID: 42, Status: application.DispatchStatusReplayed},
		Replayed: true,
	}
	recorder := serve(t, &fakeService{decisionResult: result}, http.MethodPost, "/api/v1/proposals/"+string(testProposalID)+"/approvals", `{"revision_id":"`+string(testRevisionID)+`","change_hash":"`+testChangeHash+`","decision":"approved"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response approvalDecisionResponse
	decode(t, recorder, &response)
	if response.WorkflowRunID != string(testWorkflowRunID) || response.DispatchStatus != string(application.DispatchStatusReplayed) {
		t.Fatalf("response=%#v", response)
	}
}

func TestProposalResponseIncludesDurableApprovalWorkflowWithoutDispatchStatus(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	approvedGitHead := strings.Repeat("a", 64)
	workflowRunID := testWorkflowRunID
	service := &fakeService{proposal: domain.Proposal{
		ID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, RiskLevel: domain.ProposalRiskLevelHigh, TargetPath: "notes/a.md", Status: domain.StatusApproved, WorkflowRunID: &workflowRunID,
		CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{ID: testRevisionID, ProposalID: testProposalID, RevisionNo: 1, TargetPath: "notes/a.md", BaseHash: testChangeHash, Content: "new", EvidenceSummary: "e", Risk: "low", RollbackPlan: "r", ChangeHash: testChangeHash, CreatedAt: now},
		Approval: &domain.Approval{ID: testApprovalID, ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, Decision: domain.DecisionApproved, ApprovedGitHead: &approvedGitHead, DecidedAt: now},
	}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/proposals/"+string(testProposalID), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := append([]byte(nil), recorder.Body.Bytes()...)
	var response proposalResponse
	decode(t, recorder, &response)
	if response.Approval == nil || response.Approval.ApprovedGitHead == nil || *response.Approval.ApprovedGitHead != approvedGitHead || response.Approval.WorkflowRunID != string(testWorkflowRunID) || response.Approval.WorkflowStatusURL != "/api/v1/workflows/"+string(testWorkflowRunID) {
		t.Fatalf("response=%#v", response)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["proposal_type"] != string(domain.ProposalTypeFilePatch) {
		t.Fatalf("file patch discriminator missing: %#v", raw)
	}
	approval := raw["approval"].(map[string]any)
	if _, exists := approval["dispatch_status"]; exists {
		t.Fatalf("durable approval snapshot contains dispatch_status: %#v", approval)
	}
}

func TestFilePatchProposalDetailIncludesExplicitNullApproval(t *testing.T) {
	now := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	service := &fakeService{proposal: domain.Proposal{
		ID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch,
		RiskLevel: domain.ProposalRiskLevelLow, TargetPath: "notes/a.md", Status: domain.StatusReady,
		CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{
			ID: testRevisionID, ProposalID: testProposalID, RevisionNo: 1, TargetPath: "notes/a.md",
			BaseHash: testChangeHash, Content: "new", EvidenceSummary: "evidence", Risk: "low",
			RollbackPlan: "revert", ChangeHash: testChangeHash, CreatedAt: now,
		},
	}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/proposals/"+string(testProposalID), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	decode(t, recorder, &response)
	approval, exists := response["approval"]
	if !exists || approval != nil {
		t.Fatalf("ready file patch approval must be explicit null: %#v", response)
	}
}

func TestKnowledgeChangeProposalUsesDiscriminatedTypedResponse(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	sourceID := foundation.ID("70000000-0000-4000-8000-000000000001")
	targetID := foundation.ID("70000000-0000-4000-8000-000000000002")
	evidenceID := foundation.ID("70000000-0000-4000-8000-000000000003")
	change := domain.KnowledgeChange{
		TargetRefs: []domain.KnowledgeTargetRef{{
			Type: domain.KnowledgeTargetRefRelationCandidate, ID: testProposalID, Fingerprint: testChangeHash,
		}},
		BaseVersions: []domain.KnowledgeBaseVersion{
			{NodeType: knowledge.NodeTypeClaim, NodeID: sourceID, Version: 3},
			{NodeType: knowledge.NodeTypeClaim, NodeID: targetID, Version: 5},
		},
		ChangeSet: domain.KnowledgeChangeSet{
			Operation:    domain.KnowledgeChangeOperationCreateRelation,
			Source:       knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: sourceID},
			Target:       knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: targetID},
			RelationType: knowledge.RelationComplements,
		},
		EvidenceRefs:  []domain.KnowledgeEvidenceRef{{CandidateEvidenceID: evidenceID, SemanticHash: testChangeHash}},
		SchemaVersion: domain.KnowledgeChangeSchemaVersion,
	}
	changeHash, err := domain.ComputeKnowledgeChangeHash(change, "review relation", "retire through a compensating proposal")
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeService{proposal: domain.Proposal{
		ID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeKnowledgeChange, RiskLevel: domain.ProposalRiskLevelHigh,
		Status: domain.StatusReady, CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{
			ID: testRevisionID, ProposalID: testProposalID, RevisionNo: 1,
			Risk: "review relation", RollbackPlan: "retire through a compensating proposal",
			ChangeHash: changeHash, KnowledgeChange: &change, CreatedAt: now,
		},
	}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/proposals/"+string(testProposalID), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	decode(t, recorder, &response)
	if response["proposal_type"] != string(domain.ProposalTypeKnowledgeChange) {
		t.Fatalf("proposal=%#v", response)
	}
	if _, exists := response["target_path"]; exists {
		t.Fatalf("knowledge proposal leaked file target: %#v", response)
	}
	approval, exists := response["approval"]
	if !exists || approval != nil {
		t.Fatalf("ready knowledge change approval must be explicit null: %#v", response)
	}
	revision, ok := response["revision"].(map[string]any)
	if !ok || revision["schema_version"] != domain.KnowledgeChangeSchemaVersion {
		t.Fatalf("revision=%#v", response["revision"])
	}
	changeSet := revision["change_set"].(map[string]any)
	if changeSet["relation_type"] != string(knowledge.RelationComplements) ||
		changeSet["source"].(map[string]any)["version"] != float64(3) ||
		changeSet["target"].(map[string]any)["version"] != float64(5) {
		t.Fatalf("change_set=%#v", changeSet)
	}
}

func TestPublishArtifactProposalUsesDiscriminatedTypedResponse(t *testing.T) {
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	service := &fakeService{proposal: publishArtifactProposalFixture(t, now)}
	recorder := serve(t, service, http.MethodGet, "/api/v1/proposals/"+string(testProposalID), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	decode(t, recorder, &response)
	if response["proposal_type"] != string(domain.ProposalTypePublishArtifact) {
		t.Fatalf("proposal=%#v", response)
	}
	if _, exists := response["target_path"]; exists {
		t.Fatalf("publish artifact proposal leaked file target: %#v", response)
	}
	approval, exists := response["approval"]
	if !exists || approval != nil {
		t.Fatalf("ready publish artifact approval must be explicit null: %#v", response)
	}
	revision, ok := response["revision"].(map[string]any)
	if !ok || revision["revision_no"] != float64(1) {
		t.Fatalf("revision=%#v", response["revision"])
	}
	publication, ok := revision["publication"].(map[string]any)
	if !ok || publication["workspace_id"] != string(testWorkspaceID) || publication["artifact_id"] != "70000000-0000-4000-8000-000000000001" || publication["revision_id"] != "70000000-0000-4000-8000-000000000002" || publication["revision_no"] != float64(3) || publication["artifact_version"] != float64(5) || publication["content_hash"] != testChangeHash || publication["schema_version"] != domain.PublishArtifactSchemaVersion {
		t.Fatalf("publication=%#v", publication)
	}
	coverage, ok := publication["source_coverage"].([]any)
	if !ok || len(coverage) != 2 || coverage[0].(map[string]any)["section_key"] != "intro" || coverage[1].(map[string]any)["section_key"] != "summary" {
		t.Fatalf("coverage=%#v", publication["source_coverage"])
	}
	gaps := coverage[1].(map[string]any)["gaps"].([]any)
	if len(gaps) != 2 || gaps[0].(map[string]any)["code"] != "MISSING_PRIMARY" || gaps[1].(map[string]any)["code"] != "MISSING_SECONDARY" {
		t.Fatalf("gaps=%#v", gaps)
	}
}

func TestPublishArtifactProposalReadsOmitFileWritebackBindings(t *testing.T) {
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	proposal := publishArtifactProposalFixture(t, now)
	proposal.Status = domain.StatusApproved
	approvedGitHead := testApprovedGitHead
	proposal.Approval = &domain.Approval{
		ID: testApprovalID, ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
		ChangeHash: proposal.Revision.ChangeHash, Decision: domain.DecisionApproved,
		ApprovedGitHead: &approvedGitHead, DecidedAt: now.Add(time.Minute),
	}
	proposal.WorkflowRunID = proposalWorkflowRunID(testWorkflowRunID)

	assertNoWritebackFields := func(label string, approvalValue any) {
		t.Helper()
		approval, ok := approvalValue.(map[string]any)
		if !ok || approval["decision"] != string(domain.DecisionApproved) {
			t.Fatalf("%s approval=%#v", label, approvalValue)
		}
		for _, field := range []string{"approved_git_head", "workflow_run_id", "workflow_status_url"} {
			if _, exists := approval[field]; exists {
				t.Fatalf("%s approval leaked %s: %#v", label, field, approval)
			}
		}
	}

	detailRecorder := serve(t, &fakeService{proposal: proposal}, http.MethodGet, "/api/v1/proposals/"+string(testProposalID), "")
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailRecorder.Code, detailRecorder.Body.String())
	}
	var detail map[string]any
	decode(t, detailRecorder, &detail)
	assertNoWritebackFields("detail", detail["approval"])

	listItem := domain.ProposalListItem{
		ProposalID: proposal.ID, WorkspaceID: proposal.WorkspaceID, Type: proposal.Type, Status: proposal.Status,
		RiskLevel: proposal.RiskLevel, Risk: proposal.Revision.Risk, RevisionID: proposal.Revision.ID,
		ChangeHash: proposal.Revision.ChangeHash, Approval: proposal.Approval, WorkflowRunID: proposal.WorkflowRunID,
		Version: proposal.Version, RevisionCapability: listCapability(proposal.Type, proposal.Status), CreatedAt: proposal.CreatedAt, UpdatedAt: proposal.UpdatedAt,
	}
	listRecorder := serve(t, &fakeService{listItems: []domain.ProposalListItem{listItem}}, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals", "")
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var page map[string]any
	decode(t, listRecorder, &page)
	items, ok := page["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("list items=%#v", page["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("list item=%#v", items[0])
	}
	assertNoWritebackFields("list", item["approval"])
}

func TestPublishArtifactProposalRejectsInconsistentFrozenPayload(t *testing.T) {
	proposal := publishArtifactProposalFixture(t, time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC))
	proposal.Revision.PublishArtifact.ContentHash = "invalid"
	recorder := serve(t, &fakeService{proposal: proposal}, http.MethodGet, "/api/v1/proposals/"+string(testProposalID), "")
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "PROPOSAL_REVISION_INVALID") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestDownstreamUpdateProposalUsesTypedUpdateResponse(t *testing.T) {
	now := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	proposal := downstreamUpdateProposalFixture(t, now, knowledge.ImpactObjectReviewCard)
	proposal.Status = domain.StatusApproved
	approvedGitHead := testApprovedGitHead
	proposal.Approval = &domain.Approval{
		ID: testApprovalID, ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
		ChangeHash: proposal.Revision.ChangeHash, Decision: domain.DecisionApproved,
		ApprovedGitHead: &approvedGitHead, DecidedAt: now.Add(time.Minute),
	}
	proposal.WorkflowRunID = proposalWorkflowRunID(testWorkflowRunID)
	recorder := serve(t, &fakeService{proposal: proposal}, http.MethodGet, "/api/v1/proposals/"+string(testProposalID), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	decode(t, recorder, &response)
	if response["proposal_type"] != string(domain.ProposalTypeDownstreamUpdate) {
		t.Fatalf("proposal=%#v", response)
	}
	if _, exists := response["replayed"]; exists {
		t.Fatalf("proposal detail must not contain create-only replay state: %#v", response)
	}
	if _, exists := response["target_path"]; exists {
		t.Fatalf("downstream proposal leaked file target: %#v", response)
	}
	approval, ok := response["approval"].(map[string]any)
	if !ok || approval["decision"] != string(domain.DecisionApproved) {
		t.Fatalf("approval=%#v", response["approval"])
	}
	for _, field := range []string{"approved_git_head", "workflow_run_id", "workflow_status_url", "dispatch_status"} {
		if _, exists := approval[field]; exists {
			t.Fatalf("downstream approval leaked %s: %#v", field, approval)
		}
	}
	revision, ok := response["revision"].(map[string]any)
	if !ok {
		t.Fatalf("revision=%#v", response["revision"])
	}
	update, ok := revision["update"].(map[string]any)
	if !ok || update["target_type"] != "REVIEW_CARD" || update["action"] != "REVALIDATE_REVIEW_CARD" {
		t.Fatalf("update=%#v", revision["update"])
	}
	if _, exists := update["artifact_binding"]; exists {
		t.Fatalf("review card payload contains artifact binding: %#v", update)
	}
	binding, ok := update["review_card_binding"].(map[string]any)
	if !ok || binding["status"] != "INVALIDATED" || binding["fingerprint"] != testChangeHash || binding["evidence_binding_fingerprint"] != strings.Repeat("b", 64) {
		t.Fatalf("review_card_binding=%#v", update["review_card_binding"])
	}
}

func TestDownstreamUpdateProposalRejectsInconsistentFrozenPayload(t *testing.T) {
	proposal := downstreamUpdateProposalFixture(t, time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC), knowledge.ImpactObjectArtifact)
	proposal.Revision.DownstreamUpdate.ReportFingerprint = "invalid"
	recorder := serve(t, &fakeService{proposal: proposal}, http.MethodGet, "/api/v1/proposals/"+string(testProposalID), "")
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "PROPOSAL_REVISION_INVALID") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func publishArtifactProposalFixture(t *testing.T, now time.Time) domain.Proposal {
	t.Helper()
	publication := domain.PublishArtifact{
		WorkspaceID: testWorkspaceID, ArtifactID: "70000000-0000-4000-8000-000000000001", RevisionID: "70000000-0000-4000-8000-000000000002",
		RevisionNo: 3, ArtifactVersion: 5, ContentHash: testChangeHash, SchemaVersion: domain.PublishArtifactSchemaVersion,
		SourceCoverage: []domain.ArtifactSourceCoverage{
			{SectionKey: "summary", Status: domain.ArtifactCoverageStatusGap, Gaps: []domain.ArtifactCoverageGap{{Code: "MISSING_SECONDARY", Description: "Secondary source is unavailable"}, {Code: "MISSING_PRIMARY", Description: "Primary source is unavailable"}}},
			{SectionKey: "intro", Status: domain.ArtifactCoverageCovered, Gaps: []domain.ArtifactCoverageGap{}},
		},
	}
	changeHash, err := domain.ComputePublishArtifactHash(publication, "publish approved artifact", "keep artifact isolated")
	if err != nil {
		t.Fatal(err)
	}
	return domain.Proposal{
		ID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypePublishArtifact, RiskLevel: domain.ProposalRiskLevelHigh,
		Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{
			ID: testRevisionID, ProposalID: testProposalID, RevisionNo: 1, Risk: "publish approved artifact",
			RollbackPlan: "keep artifact isolated", ChangeHash: changeHash, PublishArtifact: &publication, CreatedAt: now,
		},
	}
}

func listCapability(proposalType domain.ProposalType, status domain.ProposalStatus) domain.ProposalRevisionCapability {
	return domain.EvaluateProposalRevisionCapability(domain.ProposalRevisionCapabilityFacts{
		ProposalType: proposalType, ProposalStatus: status, TargetMode: domain.TargetModeReplace,
		CurrentRevisionID: testRevisionID, RevisionID: testRevisionID,
	})
}

func downstreamUpdateProposalFixture(t *testing.T, now time.Time, targetType knowledge.ImpactObjectType) domain.Proposal {
	t.Helper()
	const (
		reportID       foundation.ID = "70000000-0000-4000-8000-000000000001"
		sourceEventID  foundation.ID = "70000000-0000-4000-8000-000000000002"
		targetID       foundation.ID = "70000000-0000-4000-8000-000000000003"
		bindingObject  foundation.ID = "70000000-0000-4000-8000-000000000004"
		bindingRelated foundation.ID = "70000000-0000-4000-8000-000000000005"
	)
	update := domain.DownstreamUpdate{
		WorkspaceID: testWorkspaceID, ReportID: reportID, AnalysisVersion: knowledge.ImpactAnalysisVersionV2,
		ReportFingerprint: testChangeHash, SourceEventID: sourceEventID, SourceEventVersion: 7,
		TargetType: targetType, TargetID: targetID, BaseVersion: 5, Reason: "owner-backed dependency changed",
		SchemaVersion: domain.DownstreamUpdateSchemaVersion,
	}
	switch targetType {
	case knowledge.ImpactObjectArtifact:
		update.Action = knowledge.ImpactActionRegenerateArtifact
		update.OwnerBinding = knowledge.EventOwnerBinding{Artifact: &knowledge.ArtifactImpactBinding{
			ArtifactID: targetID, ArtifactVersion: 5, RevisionID: bindingObject, RevisionNo: 3, ContentHash: testChangeHash,
		}}
	case knowledge.ImpactObjectReviewCard:
		update.Action = knowledge.ImpactActionRevalidateReviewCard
		update.OwnerBinding = knowledge.EventOwnerBinding{ReviewCard: &knowledge.ReviewCardImpactBinding{
			CardID: targetID, CardVersion: 5, Status: "INVALIDATED", Fingerprint: testChangeHash,
			ClaimID: bindingRelated, EvidenceBindingFingerprint: strings.Repeat("b", 64),
		}}
	default:
		t.Fatalf("unsupported fixture target type %q", targetType)
	}
	changeHash, err := domain.ComputeDownstreamUpdateHash(update, "owner-backed downstream update", "no target write has executed")
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := domain.ComputeDownstreamUpdateRequestHash(testWorkspaceID, update, domain.ProposalRiskLevelHigh, "owner-backed downstream update", "no target write has executed")
	if err != nil {
		t.Fatal(err)
	}
	return domain.Proposal{
		ID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeDownstreamUpdate,
		RiskLevel: domain.ProposalRiskLevelHigh, IdempotencyKey: "test-create", RequestHash: requestHash,
		Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{
			ID: testRevisionID, ProposalID: testProposalID, RevisionNo: 1, Risk: "owner-backed downstream update",
			RollbackPlan: "no target write has executed", ChangeHash: changeHash, DownstreamUpdate: &update, CreatedAt: now,
		},
	}
}

func TestProposalResponseRejectsInvalidPersistedRiskLevel(t *testing.T) {
	service := &fakeService{proposal: domain.Proposal{ID: testProposalID, WorkspaceID: testWorkspaceID, RiskLevel: "high"}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/proposals/"+string(testProposalID), "")
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "PROPOSAL_RISK_LEVEL_INVALID") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestProposalResponseRejectsNonHighKnowledgeRiskLevel(t *testing.T) {
	service := &fakeService{proposal: domain.Proposal{
		ID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeKnowledgeChange,
		RiskLevel: domain.ProposalRiskLevelMedium,
	}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/proposals/"+string(testProposalID), "")
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "PROPOSAL_RISK_LEVEL_INVALID") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPreflightConflictReturnsSafeDetails(t *testing.T) {
	conflict := &application.HashConflict{Expected: testChangeHash, Current: strings.Repeat("a", 64)}
	service := &fakeService{err: foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, conflict)}
	recorder := serve(t, service, http.MethodPost, "/api/v1/proposals/"+string(testProposalID)+"/apply-preflight", `{"revision_id":"`+string(testRevisionID)+`","approved_change_hash":"`+testChangeHash+`"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var problem struct {
		ErrorCode string         `json:"error_code"`
		Details   map[string]any `json:"details"`
	}
	decode(t, recorder, &problem)
	if problem.ErrorCode != "TARGET_BASE_HASH_CONFLICT" || problem.Details["conflict_type"] != "target_base_hash_changed" {
		t.Fatalf("problem=%#v", problem)
	}
}

func TestHandlerRejectsInvalidJSONAndInternalErrorsAreRedacted(t *testing.T) {
	invalid := serve(t, &fakeService{}, http.MethodPost, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals", `{"target_path":`)
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "INVALID_JSON") {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	internal := serve(t, &fakeService{err: errors.New("secret sql")}, http.MethodGet, "/api/v1/proposals/"+string(testProposalID), "")
	if internal.Code != http.StatusInternalServerError || strings.Contains(internal.Body.String(), "secret sql") {
		t.Fatalf("internal status=%d body=%s", internal.Code, internal.Body.String())
	}
}

func TestProposalRevisionMergePreviewContract(t *testing.T) {
	service, request := revisionHTTPFixture(t)
	recorder := serve(t, service, http.MethodPost, "/api/v1/proposals/"+string(testProposalID)+"/revision-merge-previews", request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status=%d cache=%q body=%s", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
	}
	if service.previewCalls != 1 || service.previewInput.WorkspaceID != testWorkspaceID || service.previewInput.ProposalID != testProposalID || service.previewInput.ExpectedCurrentHash != "" {
		t.Fatalf("input=%#v calls=%d", service.previewInput, service.previewCalls)
	}
	var response map[string]any
	decode(t, recorder, &response)
	assertJSONFields(t, "preview", response,
		"schema_version", "merge_algorithm", "merge_algorithm_version", "merge_fingerprint", "proposal_id", "workspace_id",
		"proposal_version", "source_revision_id", "source_revision_no", "source_change_hash", "target_path", "target_mode",
		"base", "current", "proposed", "candidate", "conflict_count", "conflicts")
	if response["schema_version"] != revisionPreviewSchemaVersion || response["proposal_version"] != float64(7) || response["target_mode"] != string(domain.TargetModeReplace) || response["conflict_count"] != float64(1) {
		t.Fatalf("response=%#v", response)
	}
	base := response["base"].(map[string]any)
	current := response["current"].(map[string]any)
	proposed := response["proposed"].(map[string]any)
	if base["content"] != "base\n" || current["content"] != "current\n" || proposed["content"] != "proposed\n" || current["byte_size"] != float64(len("current\n")) {
		t.Fatalf("snapshots=%#v %#v %#v", base, current, proposed)
	}
	conflicts := response["conflicts"].([]any)
	conflict := conflicts[0].(map[string]any)
	assertJSONFields(t, "conflict", conflict, "id", "ordinal", "base", "current", "proposed")
	if conflict["ordinal"] != float64(1) || conflict["current"] != "current\n" {
		t.Fatalf("conflict=%#v", conflict)
	}
}

func TestRevisionCapabilityUsesWorkflowTerminalStatus(t *testing.T) {
	tests := []struct {
		status   string
		editable bool
		reason   string
	}{
		{status: "failed", editable: true, reason: "AVAILABLE"},
		{status: "running", reason: "PROPOSAL_REVISION_WORKFLOW_ACTIVE"},
		{status: "succeeded", reason: "PROPOSAL_REVISION_SIDE_EFFECT_STARTED"},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			service, _ := revisionHTTPFixture(t)
			workflowID := testWorkflowRunID
			service.proposal.Status = domain.StatusNeedsRevision
			service.proposal.WorkflowRunID = &workflowID
			service.proposal.WorkflowRunStatus = test.status
			capability := revisionCapability(service.proposal, true)
			if capability.Editable != test.editable || capability.Reason != test.reason {
				t.Fatalf("capability=%#v", capability)
			}
		})
	}
}

func TestRevisionCapabilityFailsClosedWhenMergeEngineIsUnavailable(t *testing.T) {
	service, _ := revisionHTTPFixture(t)
	capability := revisionCapability(service.proposal, false)
	if capability.Editable || capability.Reason != string(domain.ProposalRevisionEngineUnavailable) {
		t.Fatalf("capability=%#v", capability)
	}
}

func TestAppendProposalRevisionContractAndReplay(t *testing.T) {
	service, _ := revisionHTTPFixture(t)
	newRevisionID := foundation.ID("30000000-0000-4000-8000-000000000002")
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	currentHash := domain.RawContentHash("current\n")
	newRevision := domain.Revision{
		ID: newRevisionID, ProposalID: testProposalID, RevisionNo: 2, TargetPath: "notes/a.md", TargetMode: domain.TargetModeReplace,
		BaseHash: currentHash, Content: "resolved\n", EvidenceSummary: "evidence", Risk: "risk",
		RollbackPlan: "rollback", ChangeHash: domain.ComputeChangeHash("notes/a.md", currentHash, "resolved\n"), CreatedAt: now,
	}
	service.appendResult = application.AppendRevisionResult{Proposal: domain.Proposal{
		ID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, RiskLevel: domain.ProposalRiskLevelLow,
		TargetPath: "notes/a.md", Status: domain.StatusReady, Version: 8, CurrentRevisionID: newRevisionID,
		Revision: newRevision, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}, Revision: newRevision}
	conflictID := strings.Repeat("c", 64)
	body := marshalJSON(t, appendRevisionRequest{
		ExpectedProposalVersion: 7, SourceRevisionID: string(testRevisionID), SourceChangeHash: testChangeHash,
		ExpectedCurrentHash: currentHash, MergeFingerprint: strings.Repeat("f", 64),
		MergeAlgorithm: domain.ProposalRevisionMergeAlgorithm, MergeAlgorithmVersion: domain.ProposalRevisionMergeAlgorithmVersion,
		Content: "resolved\n", EvidenceSummary: "evidence", Risk: "risk", RollbackPlan: "rollback",
		ResolvedConflictIDs: &[]string{conflictID},
	})
	path := "/api/v1/proposals/" + string(testProposalID) + "/revisions"
	recorder := serveRequest(t, service, http.MethodPost, path, body, "application/json", []string{"revision-command"})
	if recorder.Code != http.StatusCreated || recorder.Header().Get("Cache-Control") != "private, no-store" || service.appendCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, service.appendCalls, recorder.Body.String())
	}
	if service.appendCommand.IdempotencyKey != "revision-command" || service.appendCommand.ExpectedProposalVersion != 7 || service.appendCommand.SourceRevisionID != testRevisionID || len(service.appendCommand.ResolvedConflictIDs) != 1 || service.appendCommand.ResolvedConflictIDs[0] != conflictID {
		t.Fatalf("command=%#v", service.appendCommand)
	}
	var response map[string]any
	decode(t, recorder, &response)
	if response["replayed"] != false || response["version"] != float64(8) || response["approval"] != nil {
		t.Fatalf("response=%#v", response)
	}
	capability := response["revision_capability"].(map[string]any)
	if capability["editable"] != true || capability["reason"] != "AVAILABLE" {
		t.Fatalf("capability=%#v", capability)
	}

	service.appendResult.Replayed = true
	replay := serveRequest(t, service, http.MethodPost, path, body, "application/json", []string{"revision-command"})
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	decode(t, replay, &response)
	if response["replayed"] != true {
		t.Fatalf("replay=%#v", response)
	}

	service.appendResult.Proposal.Revision.EvidenceSummary = "different evidence"
	service.appendResult.Revision.EvidenceSummary = "different evidence"
	invalidBinding := serveRequest(t, service, http.MethodPost, path, body, "application/json", []string{"revision-command"})
	if invalidBinding.Code != http.StatusInternalServerError || !strings.Contains(invalidBinding.Body.String(), "PROPOSAL_REVISION_RESULT_INVALID") {
		t.Fatalf("invalid binding status=%d body=%s", invalidBinding.Code, invalidBinding.Body.String())
	}
	service.appendResult.Proposal.Revision.EvidenceSummary = "evidence"
	service.appendResult.Revision.EvidenceSummary = "evidence"

	service.appendResult.Proposal.Version = 9
	invalidReceipt := serveRequest(t, service, http.MethodPost, path, body, "application/json", []string{"revision-command"})
	if invalidReceipt.Code != http.StatusInternalServerError || !strings.Contains(invalidReceipt.Body.String(), "PROPOSAL_REVISION_RESULT_INVALID") {
		t.Fatalf("invalid receipt status=%d body=%s", invalidReceipt.Code, invalidReceipt.Body.String())
	}
}

func TestProposalRevisionCommandsRejectInvalidWireBeforeApplication(t *testing.T) {
	service, previewBody := revisionHTTPFixture(t)
	appendBody := marshalJSON(t, appendRevisionRequest{
		ExpectedProposalVersion: 7, SourceRevisionID: string(testRevisionID), SourceChangeHash: testChangeHash,
		ExpectedCurrentHash: domain.RawContentHash("current\n"), MergeFingerprint: strings.Repeat("f", 64),
		MergeAlgorithm: domain.ProposalRevisionMergeAlgorithm, MergeAlgorithmVersion: domain.ProposalRevisionMergeAlgorithmVersion,
		Content: "resolved\n", EvidenceSummary: "evidence", Risk: "risk", RollbackPlan: "rollback", ResolvedConflictIDs: &[]string{},
	})
	previewPath := "/api/v1/proposals/" + string(testProposalID) + "/revision-merge-previews"
	appendPath := "/api/v1/proposals/" + string(testProposalID) + "/revisions"
	tests := []struct {
		name, path, body, contentType string
		keys                          []string
		status                        int
		code                          string
	}{
		{name: "preview empty", path: previewPath, contentType: "application/json", status: 400, code: "PROPOSAL_MERGE_CONTENT_INVALID"},
		{name: "preview unknown", path: previewPath, body: strings.TrimSuffix(previewBody, "}") + `,"extra":true}`, contentType: "application/json", status: 400, code: "PROPOSAL_MERGE_CONTENT_INVALID"},
		{name: "preview duplicate", path: previewPath, body: strings.TrimSuffix(previewBody, "}") + `,"source_change_hash":"` + testChangeHash + `"}`, contentType: "application/json", status: 400, code: "PROPOSAL_MERGE_CONTENT_INVALID"},
		{name: "preview oversized", path: previewPath, body: strings.Repeat(" ", revisionPreviewRequestMaxBytes+1), contentType: "application/json", status: 413, code: "PROPOSAL_REVISION_INPUT_TOO_LARGE"},
		{name: "append content type", path: appendPath, body: appendBody, contentType: "text/plain", keys: []string{"key"}, status: 415, code: "UNSUPPORTED_MEDIA_TYPE"},
		{name: "append missing key", path: appendPath, body: appendBody, contentType: "application/json", status: 400, code: "IDEMPOTENCY_KEY_REQUIRED"},
		{name: "append duplicate key", path: appendPath, body: appendBody, contentType: "application/json", keys: []string{"a", "b"}, status: 400, code: "IDEMPOTENCY_KEY_REQUIRED"},
		{name: "append missing conflicts", path: appendPath, body: strings.Replace(appendBody, `,"resolved_conflict_ids":[]`, "", 1), contentType: "application/json", keys: []string{"key"}, status: 400, code: "PROPOSAL_MERGE_CONTENT_INVALID"},
		{name: "append oversized metadata", path: appendPath, body: strings.Replace(appendBody, `"evidence_summary":"evidence"`, `"evidence_summary":"`+strings.Repeat("e", revisionMetadataMaxBytes+1)+`"`, 1), contentType: "application/json", keys: []string{"key"}, status: 413, code: "PROPOSAL_REVISION_INPUT_TOO_LARGE"},
		{name: "append query", path: appendPath + "?unsafe=1", body: appendBody, contentType: "application/json", keys: []string{"key"}, status: 400, code: "PROPOSAL_MERGE_CONTENT_INVALID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforePreview, beforeAppend := service.previewCalls, service.appendCalls
			recorder := serveRequest(t, service, http.MethodPost, test.path, test.body, test.contentType, test.keys)
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.code) || service.previewCalls != beforePreview || service.appendCalls != beforeAppend {
				t.Fatalf("status=%d preview=%d append=%d body=%s", recorder.Code, service.previewCalls, service.appendCalls, recorder.Body.String())
			}
		})
	}
}

func TestProposalRevisionProblemsUseStableStatusesAndSafeDetails(t *testing.T) {
	path := "/api/v1/proposals/" + string(testProposalID) + "/revision-merge-previews"
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "input", err: foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_INPUT_TOO_LARGE", false, errors.New("secret content")), want: http.StatusRequestEntityTooLarge},
		{name: "result", err: foundation.NewError(foundation.ErrorNonRetryableFailure, "PROPOSAL_MERGE_RESULT_TOO_LARGE", false, errors.New("secret output")), want: http.StatusUnprocessableEntity},
		{name: "conflicts", err: foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_CONFLICTS_UNRESOLVED", false, &application.UnresolvedRevisionConflicts{ConflictIDs: []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}}), want: http.StatusUnprocessableEntity},
		{name: "invalid conflict details", err: foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_CONFLICTS_UNRESOLVED", false, &application.UnresolvedRevisionConflicts{ConflictIDs: []string{"unsafe fragment"}}), want: http.StatusUnprocessableEntity},
		{name: "engine", err: foundation.NewError(foundation.ErrorNonRetryableFailure, "PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID", false, errors.New("secret stderr")), want: http.StatusServiceUnavailable},
		{name: "workflow cancellation", err: foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_REVISION_WORKFLOW_CANCEL_UNAVAILABLE", true, errors.New("secret workflow error")), want: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, body := revisionHTTPFixture(t)
			service.revisionErr = test.err
			recorder := serve(t, service, http.MethodPost, path, body)
			if recorder.Code != test.want || strings.Contains(recorder.Body.String(), "secret") || recorder.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("status=%d cache=%q body=%s", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
			}
			if test.name == "conflicts" && (!strings.Contains(recorder.Body.String(), `"conflict_ids":["`+strings.Repeat("a", 64)+`","`+strings.Repeat("b", 64)+`"]`) || strings.Contains(recorder.Body.String(), "proposal revision has unresolved conflicts")) {
				t.Fatalf("unsafe or incomplete conflict details: %s", recorder.Body.String())
			}
			if test.name == "invalid conflict details" && strings.Contains(recorder.Body.String(), "conflict_ids") {
				t.Fatalf("invalid conflict details leaked: %s", recorder.Body.String())
			}
			if test.name == "workflow cancellation" && !strings.Contains(recorder.Body.String(), `"retryable":true`) {
				t.Fatalf("workflow cancellation dependency was not retryable: %s", recorder.Body.String())
			}
		})
	}
}

func TestProposalRevisionPreviewRejectsInvalidApplicationText(t *testing.T) {
	service, body := revisionHTTPFixture(t)
	service.previewResult.CurrentContent = string([]byte{0xff})
	service.previewResult.Preview.CurrentHash = domain.RawContentHash(service.previewResult.CurrentContent)
	recorder := serve(t, service, http.MethodPost, "/api/v1/proposals/"+string(testProposalID)+"/revision-merge-previews", body)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestProposalRevisionHistoryAndDetailContracts(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	revisionTwoID := foundation.ID("30000000-0000-4000-8000-000000000002")
	revisionThreeID := foundation.ID("30000000-0000-4000-8000-000000000003")
	approvedGitHead := strings.Repeat("a", 40)
	workflowID := testWorkflowRunID
	approval := &domain.Approval{
		ID: testApprovalID, ProposalID: testProposalID, RevisionID: revisionTwoID, ChangeHash: testChangeHash,
		Decision: domain.DecisionApproved, ApprovedGitHead: &approvedGitHead, DecidedAt: now,
	}
	service := &fakeService{
		revisionItems: []domain.ProposalRevisionHistoryItem{
			{ProposalID: testProposalID, RevisionID: revisionThreeID, RevisionNo: 3, TargetPath: "notes/a.md", TargetMode: domain.TargetModeReplace, BaseHash: testChangeHash, ChangeHash: strings.Repeat("b", 64), BaseAvailable: true, Current: true, CreatedAt: now.Add(time.Hour)},
			{ProposalID: testProposalID, RevisionID: revisionTwoID, RevisionNo: 2, TargetPath: "notes/a.md", TargetMode: domain.TargetModeReplace, BaseHash: testChangeHash, ChangeHash: testChangeHash, BaseAvailable: true, Approval: approval, WorkflowRunID: &workflowID, CreatedAt: now},
		},
		revisionHasMore: true,
	}
	list := serve(t, service, http.MethodGet, "/api/v1/proposals/"+string(testProposalID)+"/revisions?limit=2", "")
	if list.Code != http.StatusOK || list.Header().Get("Cache-Control") != "private, no-store" || service.historyQuery.Limit != 2 {
		t.Fatalf("status=%d query=%#v body=%s", list.Code, service.historyQuery, list.Body.String())
	}
	var page map[string]any
	decode(t, list, &page)
	if page["next_before_revision_no"] != float64(2) || len(page["items"].([]any)) != 2 {
		t.Fatalf("page=%#v", page)
	}
	historyApproval := page["items"].([]any)[1].(map[string]any)["approval"].(map[string]any)
	historyWorkflow := page["items"].([]any)[1].(map[string]any)["workflow"].(map[string]any)
	if historyApproval["revision_id"] != string(revisionTwoID) || historyWorkflow["status_url"] != "/api/v1/workflows/"+string(workflowID) {
		t.Fatalf("approval=%#v workflow=%#v", historyApproval, historyWorkflow)
	}

	baseContent := "base\n"
	baseHash := domain.RawContentHash(baseContent)
	detailChangeHash := domain.ComputeChangeHash("notes/a.md", baseHash, "proposed\n")
	detailApproval := *approval
	detailApproval.ChangeHash = detailChangeHash
	service.revisionDetail = domain.ProposalRevisionHistoryDetail{
		WorkspaceID: testWorkspaceID, ProposalID: testProposalID, CurrentRevisionID: revisionThreeID,
		Revision: domain.Revision{
			ID: revisionTwoID, ProposalID: testProposalID, RevisionNo: 2, TargetPath: "notes/a.md", TargetMode: domain.TargetModeReplace,
			BaseHash: baseHash, Content: "proposed\n", EvidenceSummary: "evidence", Risk: "risk", RollbackPlan: "rollback",
			ChangeHash: detailChangeHash, CreatedAt: now,
			BaseSnapshot: &domain.RevisionBaseSnapshot{ProposalID: testProposalID, RevisionID: revisionTwoID, BaseHash: baseHash, Content: baseContent, ByteSize: len(baseContent), SchemaVersion: domain.ProposalRevisionBaseSnapshotSchemaVersion, CreatedAt: now},
			Lineage:      &domain.RevisionLineage{ProposalID: testProposalID, RevisionID: revisionTwoID, SourceRevisionID: testRevisionID, SourceChangeHash: testChangeHash, Kind: domain.RevisionLineageThreeWayMerge, MergeAlgorithm: domain.ProposalRevisionMergeAlgorithm, MergeVersion: domain.ProposalRevisionMergeAlgorithmVersion, MergeFingerprint: strings.Repeat("f", 64), CreatedAt: now},
		},
		Approval: &detailApproval, WorkflowRunID: &workflowID,
	}
	detail := serve(t, service, http.MethodGet, "/api/v1/proposals/"+string(testProposalID)+"/revisions/"+string(revisionTwoID), "")
	if detail.Code != http.StatusOK || detail.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status=%d body=%s", detail.Code, detail.Body.String())
	}
	var response map[string]any
	decode(t, detail, &response)
	assertJSONFields(t, "revision detail", response, "workspace_id", "proposal_id", "current_revision_id", "current", "revision", "base_available", "base_snapshot", "lineage", "approval", "workflow")
	if response["current"] != false || response["base_available"] != true || response["base_snapshot"].(map[string]any)["content"] != baseContent || response["lineage"].(map[string]any)["source_revision_id"] != string(testRevisionID) {
		t.Fatalf("detail=%#v", response)
	}
	service.revisionDetail.Revision.Lineage = nil
	missingLineage := serve(t, service, http.MethodGet, "/api/v1/proposals/"+string(testProposalID)+"/revisions/"+string(revisionTwoID), "")
	if missingLineage.Code != http.StatusInternalServerError || !strings.Contains(missingLineage.Body.String(), "PROPOSAL_REVISION_HISTORY_BINDING_INVALID") {
		t.Fatalf("missing lineage status=%d body=%s", missingLineage.Code, missingLineage.Body.String())
	}
	service.revisionDetail.Revision.Lineage = &domain.RevisionLineage{ProposalID: testProposalID, RevisionID: revisionTwoID, SourceRevisionID: testRevisionID, SourceChangeHash: testChangeHash, Kind: domain.RevisionLineageThreeWayMerge, MergeAlgorithm: domain.ProposalRevisionMergeAlgorithm, MergeVersion: domain.ProposalRevisionMergeAlgorithmVersion, MergeFingerprint: strings.Repeat("f", 64), CreatedAt: now}

	for _, suffix := range []string{"?before_revision_no=1", "?limit=", "?before_revision_no="} {
		invalid := serve(t, service, http.MethodGet, "/api/v1/proposals/"+string(testProposalID)+"/revisions"+suffix, "")
		if invalid.Code != http.StatusBadRequest || service.historyCalls != 1 || !strings.Contains(invalid.Body.String(), "PROPOSAL_REVISION_HISTORY_QUERY_INVALID") {
			t.Fatalf("suffix=%q status=%d calls=%d body=%s", suffix, invalid.Code, service.historyCalls, invalid.Body.String())
		}
	}
	invalidID := serve(t, service, http.MethodGet, "/api/v1/proposals/not-a-uuid/revisions", "")
	if invalidID.Code != http.StatusBadRequest || service.historyCalls != 1 || !strings.Contains(invalidID.Body.String(), "PROPOSAL_REVISION_HISTORY_QUERY_INVALID") {
		t.Fatalf("invalid id status=%d calls=%d body=%s", invalidID.Code, service.historyCalls, invalidID.Body.String())
	}

	service.revisionItems = append(service.revisionItems, domain.ProposalRevisionHistoryItem{
		ProposalID: testProposalID, RevisionID: testRevisionID, RevisionNo: 1, TargetPath: "notes/a.md",
		TargetMode: domain.TargetModeReplace, BaseHash: testChangeHash, ChangeHash: strings.Repeat("d", 64), CreatedAt: now.Add(-time.Hour),
	})
	overLimit := serve(t, service, http.MethodGet, "/api/v1/proposals/"+string(testProposalID)+"/revisions?limit=2", "")
	if overLimit.Code != http.StatusInternalServerError || !strings.Contains(overLimit.Body.String(), "PROPOSAL_REVISION_HISTORY_BINDING_INVALID") {
		t.Fatalf("status=%d body=%s", overLimit.Code, overLimit.Body.String())
	}
}

func revisionHTTPFixture(t *testing.T) (*fakeService, string) {
	t.Helper()
	base, current, proposed := "base\n", "current\n", "proposed\n"
	conflictID := strings.Repeat("c", 64)
	proposal := domain.Proposal{
		ID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, RiskLevel: domain.ProposalRiskLevelLow,
		TargetPath: "notes/a.md", Status: domain.StatusReady, Version: 7, CurrentRevisionID: testRevisionID,
		Revision: domain.Revision{ID: testRevisionID, ProposalID: testProposalID, RevisionNo: 1, TargetPath: "notes/a.md", TargetMode: domain.TargetModeReplace, BaseHash: domain.RawContentHash(base), Content: proposed, EvidenceSummary: "evidence", Risk: "risk", RollbackPlan: "rollback", ChangeHash: testChangeHash},
	}
	service := &fakeService{proposal: proposal, previewResult: application.RevisionPreviewResult{
		WorkspaceID: testWorkspaceID, SourceRevisionNo: 1, SourceChangeHash: testChangeHash, TargetPath: "notes/a.md", TargetMode: domain.TargetModeReplace,
		BaseContent: base, CurrentContent: current, ProposedContent: proposed,
		Preview: domain.RevisionMergePreview{
			ProposalID: testProposalID, SourceRevisionID: testRevisionID, ProposalVersion: 7,
			BaseHash: domain.RawContentHash(base), CurrentHash: domain.RawContentHash(current), ProposedHash: domain.RawContentHash(proposed),
			Candidate: current, Conflicts: []domain.RevisionConflict{{ID: conflictID, Ordinal: 1, Base: base, Current: current, Proposed: proposed}},
			Algorithm: domain.ProposalRevisionMergeAlgorithm, AlgorithmVersion: domain.ProposalRevisionMergeAlgorithmVersion, Fingerprint: strings.Repeat("f", 64),
		},
	}}
	request := marshalJSON(t, revisionMergePreviewRequest{SourceRevisionID: string(testRevisionID), SourceChangeHash: testChangeHash, ExpectedProposalVersion: 7})
	return service, request
}

func marshalJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type fakeService struct {
	proposal                 domain.Proposal
	decisionResult           application.ApprovalDecisionResult
	preflight                application.ApplyPreflightResult
	err                      error
	create                   application.CreateCommand
	downstreamCreate         application.CreateDownstreamUpdateCommand
	decision                 domain.Decision
	replayed                 bool
	currentContent           application.ProposalCurrentContent
	listItems                []domain.ProposalListItem
	listHasMore              bool
	listQuery                domain.ProposalListQuery
	listCalls                int
	createCalls              int
	downstreamCreateCalls    int
	downstreamWaitForContext bool
	downstreamContextErr     error
	previewResult            application.RevisionPreviewResult
	appendResult             application.AppendRevisionResult
	revisionItems            []domain.ProposalRevisionHistoryItem
	revisionHasMore          bool
	revisionDetail           domain.ProposalRevisionHistoryDetail
	revisionErr              error
	historyErr               error
	previewInput             domain.RevisionMergeInput
	appendCommand            application.AppendRevisionCommand
	historyQuery             domain.ProposalRevisionHistoryQuery
	previewCalls             int
	appendCalls              int
	historyCalls             int
	detailCalls              int
	mergeUnavailable         bool
}

func (f *fakeService) RevisionMergeAvailable() bool { return !f.mergeUnavailable }

func (f *fakeService) ListProposals(_ context.Context, query domain.ProposalListQuery) ([]domain.ProposalListItem, bool, error) {
	f.listCalls++
	f.listQuery = query
	return append([]domain.ProposalListItem(nil), f.listItems...), f.listHasMore, f.err
}

func (f *fakeService) GetProposalCurrentContent(context.Context, foundation.ID) (application.ProposalCurrentContent, error) {
	return f.currentContent, f.err
}

func (f *fakeService) CreateProposal(_ context.Context, command application.CreateCommand) (application.CreateResult, error) {
	f.createCalls++
	f.create = command
	return application.CreateResult{Proposal: f.proposal, Replayed: f.replayed}, f.err
}
func (f *fakeService) CreateDownstreamUpdateProposal(ctx context.Context, command application.CreateDownstreamUpdateCommand) (application.CreateResult, error) {
	f.downstreamCreateCalls++
	f.downstreamCreate = command
	if f.downstreamWaitForContext {
		<-ctx.Done()
		f.downstreamContextErr = ctx.Err()
		return application.CreateResult{}, ctx.Err()
	}
	return application.CreateResult{Proposal: f.proposal, Replayed: f.replayed}, f.err
}
func (f *fakeService) GetProposal(context.Context, foundation.ID) (domain.Proposal, error) {
	return f.proposal, f.err
}
func (f *fakeService) DecideProposalWithDispatch(_ context.Context, _, _ foundation.ID, _ string, decision domain.Decision) (application.ApprovalDecisionResult, error) {
	f.decision = decision
	return f.decisionResult, f.err
}
func (f *fakeService) CheckApplyPreflight(context.Context, foundation.ID, foundation.ID, string) (application.ApplyPreflightResult, error) {
	return f.preflight, f.err
}

func (f *fakeService) PreviewProposalRevision(_ context.Context, input domain.RevisionMergeInput) (application.RevisionPreviewResult, error) {
	f.previewCalls++
	f.previewInput = input
	return f.previewResult, f.revisionErr
}

func (f *fakeService) AppendProposalRevision(_ context.Context, command application.AppendRevisionCommand) (application.AppendRevisionResult, error) {
	f.appendCalls++
	f.appendCommand = command
	return f.appendResult, f.revisionErr
}

func (f *fakeService) ListProposalRevisions(_ context.Context, query domain.ProposalRevisionHistoryQuery) ([]domain.ProposalRevisionHistoryItem, bool, error) {
	f.historyCalls++
	f.historyQuery = query
	return append([]domain.ProposalRevisionHistoryItem(nil), f.revisionItems...), f.revisionHasMore, f.historyErr
}

func (f *fakeService) GetProposalRevision(context.Context, foundation.ID, foundation.ID) (domain.ProposalRevisionHistoryDetail, error) {
	f.detailCalls++
	return f.revisionDetail, f.historyErr
}

func serve(t *testing.T, service Service, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return serveRequest(t, service, method, path, body, "application/json", []string{"test-create"})
}

func serveRequest(t *testing.T, service Service, method, path, body, contentType string, idempotencyKeys []string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	NewHandler(service).Routes(router.Group("/api/v1"))
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	for _, key := range idempotencyKeys {
		request.Header.Add("Idempotency-Key", key)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func decode(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(recorder.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func assertJSONFields(t *testing.T, label string, value map[string]any, fields ...string) {
	t.Helper()
	if len(value) != len(fields) {
		t.Fatalf("%s fields=%#v want=%#v", label, value, fields)
	}
	for _, field := range fields {
		if _, exists := value[field]; !exists {
			t.Fatalf("%s missing field %q: %#v", label, field, value)
		}
	}
}

var _ Service = (*fakeService)(nil)
