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
	"github.com/go-chi/chi/v5"
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
	router := chi.NewRouter()
	router.Route("/api/v1", func(api chi.Router) { NewHandler(&fakeService{}).Routes(api) })
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals", strings.NewReader(`{"target_path":"a.md"}`))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "IDEMPOTENCY_KEY_REQUIRED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestApprovalAndPreflightContracts(t *testing.T) {
	approvedGitHead := testApprovedGitHead
	service := &fakeService{
		decisionResult: application.ApprovalDecisionResult{
			Approval: domain.Approval{ID: testApprovalID, ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, Decision: domain.DecisionApproved, ApprovedGitHead: &approvedGitHead},
			Workflow: &application.ApprovalWorkflowDispatch{RunID: testWorkflowRunID, NodeID: testNodeRunID, JobID: 42, Status: application.DispatchStatusQueued},
		},
		preflight: application.ApplyPreflightResult{ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, BaseHash: testChangeHash},
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
	if response["preflight_passed"] != true || response["write_performed"] != false || response["mode"] != "preflight_only" {
		t.Fatalf("preflight response=%#v", response)
	}
	if _, exists := response["eligible"]; exists {
		t.Fatalf("deprecated authorization field exists: %#v", response)
	}
}

func TestProposalCurrentContentContract(t *testing.T) {
	service := &fakeService{currentContent: application.ProposalCurrentContent{
		ProposalID: testProposalID, WorkspaceID: testWorkspaceID, TargetPath: "notes/a.md", Content: "current",
		CurrentHash: testChangeHash, BaseHash: testChangeHash, BaseHashMatch: true,
	}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/proposals/"+string(testProposalID)+"/current-content", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", recorder.Header().Get("Cache-Control"))
	}
	var response proposalCurrentContentResponse
	decode(t, recorder, &response)
	if response.ProposalID != string(testProposalID) || response.WorkspaceID != string(testWorkspaceID) || response.Content != "current" || !response.BaseHashMatch {
		t.Fatalf("response=%#v", response)
	}
}

func TestProposalListBindsFiltersToCursor(t *testing.T) {
	now := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	service := &fakeService{listItems: []domain.ProposalListItem{{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusReady, RiskLevel: domain.ProposalRiskLevelLow, RevisionID: testRevisionID, CreatedAt: now, UpdatedAt: now}}, listHasMore: true}
	first := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals?status=ready_for_review&proposal_type=file_patch&limit=1", "")
	if first.Code != http.StatusOK || service.listQuery.Status != domain.StatusReady || service.listQuery.Type != domain.ProposalTypeFilePatch {
		t.Fatalf("status=%d query=%+v body=%s", first.Code, service.listQuery, first.Body.String())
	}
	var page proposalPageResponse
	decode(t, first, &page)
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

func TestProposalListRejectsLowercaseRiskFilter(t *testing.T) {
	service := &fakeService{listItems: []domain.ProposalListItem{{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusReady, RiskLevel: domain.ProposalRiskLevelHigh, RevisionID: testRevisionID}}}
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
	service := &fakeService{listItems: []domain.ProposalListItem{{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusReady, RiskLevel: domain.ProposalRiskLevelHigh, RevisionID: testRevisionID}}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals?risk=HIGH", "")
	if recorder.Code != http.StatusOK || service.listQuery.RiskLevel != domain.ProposalRiskLevelHigh {
		t.Fatalf("status=%d risk=%q body=%s", recorder.Code, service.listQuery.RiskLevel, recorder.Body.String())
	}
}

func TestProposalListIncludesLegalDurableApprovalBindings(t *testing.T) {
	now := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	approvedGitHead := testApprovedGitHead
	service := &fakeService{listItems: []domain.ProposalListItem{
		{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusReady, RiskLevel: domain.ProposalRiskLevelLow, RevisionID: testRevisionID, ChangeHash: testChangeHash, CreatedAt: now, UpdatedAt: now},
		{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusRejected, RiskLevel: domain.ProposalRiskLevelLow, RevisionID: testRevisionID, ChangeHash: testChangeHash, Approval: &domain.Approval{ID: testApprovalID, ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, Decision: domain.DecisionRejected, DecidedAt: now}, CreatedAt: now, UpdatedAt: now},
		{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusApproved, RiskLevel: domain.ProposalRiskLevelHigh, RevisionID: testRevisionID, ChangeHash: testChangeHash, Approval: &domain.Approval{ID: testApprovalID, ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, Decision: domain.DecisionApproved, ApprovedGitHead: &approvedGitHead, DecidedAt: now}, WorkflowRunID: proposalWorkflowRunID(testWorkflowRunID), CreatedAt: now, UpdatedAt: now},
		{ProposalID: testProposalID, WorkspaceID: testWorkspaceID, Type: domain.ProposalTypeKnowledgeChange, Status: domain.StatusApproved, RiskLevel: domain.ProposalRiskLevelHigh, RevisionID: testRevisionID, ChangeHash: testChangeHash, Approval: &domain.Approval{ID: testApprovalID, ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, Decision: domain.DecisionApproved, DecidedAt: now}, CreatedAt: now, UpdatedAt: now},
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

type fakeService struct {
	proposal       domain.Proposal
	decisionResult application.ApprovalDecisionResult
	preflight      application.ApplyPreflightResult
	err            error
	create         application.CreateCommand
	decision       domain.Decision
	replayed       bool
	currentContent application.ProposalCurrentContent
	listItems      []domain.ProposalListItem
	listHasMore    bool
	listQuery      domain.ProposalListQuery
	listCalls      int
	createCalls    int
}

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

func serve(t *testing.T, service Service, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	router.Route("/api/v1", func(api chi.Router) { NewHandler(service).Routes(api) })
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "test-create")
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

var _ Service = (*fakeService)(nil)
