package changecontrolhttp

import (
	"context"
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
	"github.com/go-chi/chi/v5"
)

const (
	testWorkspaceID foundation.ID = "10000000-0000-4000-8000-000000000001"
	testProposalID  foundation.ID = "20000000-0000-4000-8000-000000000001"
	testRevisionID  foundation.ID = "30000000-0000-4000-8000-000000000001"
	testApprovalID  foundation.ID = "40000000-0000-4000-8000-000000000001"
	testChangeHash                = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestCreateProposalContract(t *testing.T) {
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	service := &fakeService{proposal: domain.Proposal{
		ID: testProposalID, WorkspaceID: testWorkspaceID, TargetPath: "notes/a.md", Status: domain.StatusReady,
		CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{ID: testRevisionID, ProposalID: testProposalID, RevisionNo: 1, TargetPath: "notes/a.md", BaseHash: testChangeHash, Content: "new", EvidenceSummary: "e", Risk: "low", RollbackPlan: "r", ChangeHash: testChangeHash, CreatedAt: now},
	}}
	recorder := serve(t, service, http.MethodPost, "/api/v1/workspaces/"+string(testWorkspaceID)+"/proposals", `{"target_path":"notes/a.md","base_hash":"`+testChangeHash+`","content":"new","evidence_summary":"e","risk":"low","rollback_plan":"r"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response proposalResponse
	decode(t, recorder, &response)
	if response.ID != string(testProposalID) || service.create.WorkspaceID != testWorkspaceID || service.create.TargetPath != "notes/a.md" {
		t.Fatalf("response=%#v command=%#v", response, service.create)
	}
}

func TestApprovalAndPreflightContracts(t *testing.T) {
	service := &fakeService{
		approval:  domain.Approval{ID: testApprovalID, ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, Decision: domain.DecisionApproved},
		preflight: application.ApplyPreflightResult{ProposalID: testProposalID, RevisionID: testRevisionID, ChangeHash: testChangeHash, BaseHash: testChangeHash},
	}
	approval := serve(t, service, http.MethodPost, "/api/v1/proposals/"+string(testProposalID)+"/approvals", `{"revision_id":"`+string(testRevisionID)+`","change_hash":"`+testChangeHash+`","decision":"approved"}`)
	if approval.Code != http.StatusCreated || service.decision != domain.DecisionApproved {
		t.Fatalf("approval status=%d body=%s", approval.Code, approval.Body.String())
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
	proposal  domain.Proposal
	approval  domain.Approval
	preflight application.ApplyPreflightResult
	err       error
	create    application.CreateCommand
	decision  domain.Decision
}

func (f *fakeService) CreateProposal(_ context.Context, command application.CreateCommand) (domain.Proposal, error) {
	f.create = command
	return f.proposal, f.err
}
func (f *fakeService) GetProposal(context.Context, foundation.ID) (domain.Proposal, error) {
	return f.proposal, f.err
}
func (f *fakeService) DecideProposal(_ context.Context, _, _ foundation.ID, _ string, decision domain.Decision) (domain.Approval, error) {
	f.decision = decision
	return f.approval, f.err
}
func (f *fakeService) CheckApplyPreflight(context.Context, foundation.ID, foundation.ID, string) (application.ApplyPreflightResult, error) {
	return f.preflight, f.err
}

func serve(t *testing.T, service Service, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	router.Route("/api/v1", func(api chi.Router) { NewHandler(service).Routes(api) })
	request := httptest.NewRequest(method, path, strings.NewReader(body))
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
