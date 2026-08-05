package http

import (
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
)

const (
	httpHistoryWorkspaceID = foundation.ID("50000000-0000-4000-8000-000000000001")
	httpHistoryDocumentID  = foundation.ID("50000000-0000-4000-8000-000000000002")
	httpHistoryRevisionID  = foundation.ID("50000000-0000-4000-8000-000000000003")
	httpHistoryProposalID  = foundation.ID("50000000-0000-4000-8000-000000000004")
	httpHistoryProposalRev = foundation.ID("50000000-0000-4000-8000-000000000005")
	httpHistoryHead        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	httpHistoryTarget      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	httpHistoryPreviewHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestHandlerProjectsHistoryUnionAndBindsQuery(t *testing.T) {
	committedAt := time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC)
	service := &historyHTTPServiceStub{
		listFn: func(_ context.Context, query application.HistoryQuery) (domain.Page, error) {
			if query.WorkspaceID != httpHistoryWorkspaceID || query.DocumentID != httpHistoryDocumentID || query.Limit != 2 || query.Cursor != "next-page" {
				t.Fatalf("query=%+v", query)
			}
			return domain.Page{
				WorkspaceID: httpHistoryWorkspaceID, DocumentID: httpHistoryDocumentID, DocumentVersion: 7,
				Path: "notes/history.md", Branch: "main", Head: httpHistoryHead, Dirty: true,
				Items: []domain.Entry{
					{Kind: domain.EntryCurrentChange, Commit: domain.WorktreeRef, ParentCommits: []string{httpHistoryHead}, Summary: "当前未提交改动"},
					{Kind: domain.EntryManaged, Commit: httpHistoryHead, ParentCommits: []string{httpHistoryTarget}, AuthorName: "User", AuthorEmail: "user@example.com", CommittedAt: committedAt, Summary: "restore", ArticleRevisionID: httpHistoryRevisionID, ArticleRevisionNo: 7, ProposalID: httpHistoryProposalID, ProposalRevisionID: httpHistoryProposalRev, ProposalType: "restore_document"},
					{Kind: domain.EntryExternal, Commit: httpHistoryTarget, ParentCommits: []string{}, AuthorName: "External", AuthorEmail: "external@example.com", CommittedAt: committedAt.Add(-time.Hour), Summary: "external"},
				},
				NextCursor: "cursor-2",
			}, nil
		},
	}
	response := historyHTTPRequest(t, newHistoryHTTPRouter(service), stdhttp.MethodGet, historyHTTPPath("/history")+"?limit=2&cursor=next-page", "", nil, nil)
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("headers=%v", response.Header())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 3 || payload["next_cursor"] != "cursor-2" {
		t.Fatalf("payload=%#v", payload)
	}
	current := historyHTTPObject(t, items[0])
	if current["kind"] != string(domain.EntryCurrentChange) || current["commit"] != nil || current["committed_at"] != nil {
		t.Fatalf("current=%#v", current)
	}
	managed := historyHTTPObject(t, items[1])
	if managed["kind"] != string(domain.EntryManaged) || managed["proposal_id"] != string(httpHistoryProposalID) || managed["proposal_type"] != "restore_document" || managed["workflow_run_id"] != nil {
		t.Fatalf("managed=%#v", managed)
	}
	external := historyHTTPObject(t, items[2])
	if external["kind"] != string(domain.EntryExternal) {
		t.Fatalf("external=%#v", external)
	}
	if _, leaked := external["proposal_id"]; leaked {
		t.Fatalf("external entry leaked managed fields: %#v", external)
	}
}

func TestHandlerRejectsStrictHTTPBoundariesBeforeService(t *testing.T) {
	previewBody := `{"target_commit":"` + httpHistoryTarget + `","expected_document_version":7}`
	proposalBody := `{"target_commit":"` + httpHistoryTarget + `","expected_head":"` + httpHistoryHead + `","expected_document_version":7,"preview_hash":"` + httpHistoryPreviewHash + `"}`
	tests := []struct {
		name         string
		method       string
		path         string
		body         string
		contentTypes []string
		keys         []string
		wantStatus   int
		wantCode     string
	}{
		{name: "unknown list query", method: stdhttp.MethodGet, path: historyHTTPPath("/history") + "?unexpected=1", wantStatus: stdhttp.StatusBadRequest, wantCode: application.ErrorCodeInvalid},
		{name: "duplicate list query", method: stdhttp.MethodGet, path: historyHTTPPath("/history") + "?limit=1&limit=2", wantStatus: stdhttp.StatusBadRequest, wantCode: application.ErrorCodeInvalid},
		{name: "duplicate compare query", method: stdhttp.MethodGet, path: historyHTTPPath("/history/compare") + "?left=" + httpHistoryTarget + "&left=" + httpHistoryHead + "&right=" + httpHistoryHead, wantStatus: stdhttp.StatusBadRequest, wantCode: application.ErrorCodeInvalid},
		{name: "preview query", method: stdhttp.MethodPost, path: historyHTTPPath("/restore-previews") + "?unused=1", body: previewBody, contentTypes: []string{"application/json"}, wantStatus: stdhttp.StatusBadRequest, wantCode: application.ErrorCodeInvalid},
		{name: "proposal query", method: stdhttp.MethodPost, path: historyHTTPPath("/restore-proposals") + "?unused=1", body: proposalBody, contentTypes: []string{"application/json"}, keys: []string{"restore-1"}, wantStatus: stdhttp.StatusBadRequest, wantCode: application.ErrorCodeInvalid},
		{name: "missing media type", method: stdhttp.MethodPost, path: historyHTTPPath("/restore-previews"), body: previewBody, wantStatus: stdhttp.StatusUnsupportedMediaType, wantCode: errorUnsupportedType},
		{name: "wrong media type", method: stdhttp.MethodPost, path: historyHTTPPath("/restore-previews"), body: previewBody, contentTypes: []string{"text/plain"}, wantStatus: stdhttp.StatusUnsupportedMediaType, wantCode: errorUnsupportedType},
		{name: "duplicate media type", method: stdhttp.MethodPost, path: historyHTTPPath("/restore-previews"), body: previewBody, contentTypes: []string{"application/json", "application/json"}, wantStatus: stdhttp.StatusUnsupportedMediaType, wantCode: errorUnsupportedType},
		{name: "malformed JSON", method: stdhttp.MethodPost, path: historyHTTPPath("/restore-previews"), body: `{`, contentTypes: []string{"application/json"}, wantStatus: stdhttp.StatusBadRequest, wantCode: errorInvalidJSON},
		{name: "unknown JSON field", method: stdhttp.MethodPost, path: historyHTTPPath("/restore-previews"), body: strings.TrimSuffix(previewBody, "}") + `,"unknown":true}`, contentTypes: []string{"application/json"}, wantStatus: stdhttp.StatusBadRequest, wantCode: errorInvalidJSON},
		{name: "duplicate JSON field", method: stdhttp.MethodPost, path: historyHTTPPath("/restore-previews"), body: `{"target_commit":"` + httpHistoryTarget + `","target_commit":"` + httpHistoryTarget + `","expected_document_version":7}`, contentTypes: []string{"application/json"}, wantStatus: stdhttp.StatusBadRequest, wantCode: errorInvalidJSON},
		{name: "trailing JSON value", method: stdhttp.MethodPost, path: historyHTTPPath("/restore-previews"), body: previewBody + `{}`, contentTypes: []string{"application/json"}, wantStatus: stdhttp.StatusBadRequest, wantCode: errorInvalidJSON},
		{name: "missing idempotency key", method: stdhttp.MethodPost, path: historyHTTPPath("/restore-proposals"), body: proposalBody, contentTypes: []string{"application/json"}, wantStatus: stdhttp.StatusBadRequest, wantCode: application.ErrorCodeInvalid},
		{name: "duplicate idempotency key", method: stdhttp.MethodPost, path: historyHTTPPath("/restore-proposals"), body: proposalBody, contentTypes: []string{"application/json"}, keys: []string{"restore-1", "restore-2"}, wantStatus: stdhttp.StatusBadRequest, wantCode: application.ErrorCodeInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &historyHTTPServiceStub{}
			response := historyHTTPRequest(t, newHistoryHTTPRouter(service), test.method, test.path, test.body, test.contentTypes, test.keys)
			problem := historyHTTPProblem(t, response)
			if response.Code != test.wantStatus || problem.ErrorCode != test.wantCode {
				t.Fatalf("status=%d code=%s want status=%d code=%s body=%s", response.Code, problem.ErrorCode, test.wantStatus, test.wantCode, response.Body.String())
			}
			if service.totalCalls() != 0 {
				t.Fatalf("malformed request reached service: %+v", service)
			}
		})
	}
}

func TestHandlerBindsRestorePreviewAndProposalCommands(t *testing.T) {
	preview := domain.RestorePreview{
		WorkspaceID: httpHistoryWorkspaceID, DocumentID: httpHistoryDocumentID, Path: "notes/history.md",
		TargetCommit: httpHistoryTarget, ExpectedHead: httpHistoryHead, ExpectedDocumentVersion: 7,
		CurrentContentHash: strings.Repeat("d", 64), TargetContentHash: strings.Repeat("e", 64),
		CurrentContent: "current\n", TargetContent: "target\n", Patch: "-current\n+target\n",
		DiffHash: strings.Repeat("f", 64), PreviewHash: httpHistoryPreviewHash,
	}
	service := &historyHTTPServiceStub{
		previewFn: func(_ context.Context, command application.PreviewCommand) (domain.RestorePreview, error) {
			if command.WorkspaceID != httpHistoryWorkspaceID || command.DocumentID != httpHistoryDocumentID || command.TargetCommit != httpHistoryTarget || command.ExpectedDocumentVersion != 7 {
				t.Fatalf("preview command=%+v", command)
			}
			return preview, nil
		},
		createFn: func(_ context.Context, command application.RestoreCommand) (application.RestoreProposalReceipt, error) {
			if command.WorkspaceID != httpHistoryWorkspaceID || command.DocumentID != httpHistoryDocumentID || command.TargetCommit != httpHistoryTarget || command.ExpectedHead != httpHistoryHead || command.ExpectedDocumentVersion != 7 || command.PreviewHash != httpHistoryPreviewHash || command.IdempotencyKey != "restore-1" {
				t.Fatalf("restore command=%+v", command)
			}
			return application.RestoreProposalReceipt{
				WorkspaceID: httpHistoryWorkspaceID, DocumentID: httpHistoryDocumentID,
				ProposalID: httpHistoryProposalID, ProposalRevisionID: httpHistoryProposalRev,
				TargetCommit: httpHistoryTarget, ExpectedHead: httpHistoryHead, ExpectedDocumentVersion: 7,
				PreviewHash: httpHistoryPreviewHash, ProposalType: "restore_document", Status: "ready_for_review",
				ChangeHash: strings.Repeat("1", 64),
			}, nil
		},
	}
	router := newHistoryHTTPRouter(service)
	previewResponse := historyHTTPRequest(t, router, stdhttp.MethodPost, historyHTTPPath("/restore-previews"),
		`{"target_commit":"`+httpHistoryTarget+`","expected_document_version":7}`, []string{"application/json; charset=utf-8"}, nil)
	if previewResponse.Code != stdhttp.StatusOK || !strings.Contains(previewResponse.Body.String(), `"preview_hash":"`+httpHistoryPreviewHash+`"`) {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	proposalResponse := historyHTTPRequest(t, router, stdhttp.MethodPost, historyHTTPPath("/restore-proposals"),
		`{"target_commit":"`+httpHistoryTarget+`","expected_head":"`+httpHistoryHead+`","expected_document_version":7,"preview_hash":"`+httpHistoryPreviewHash+`"}`,
		[]string{"application/json"}, []string{"restore-1"})
	if proposalResponse.Code != stdhttp.StatusCreated || !strings.Contains(proposalResponse.Body.String(), `"proposal_type":"restore_document"`) || !strings.Contains(proposalResponse.Body.String(), `"replayed":false`) {
		t.Fatalf("proposal status=%d body=%s", proposalResponse.Code, proposalResponse.Body.String())
	}
	if service.previewCalls != 1 || service.createCalls != 1 {
		t.Fatalf("preview calls=%d create calls=%d", service.previewCalls, service.createCalls)
	}
}

func TestHandlerBindsCompareVersions(t *testing.T) {
	service := &historyHTTPServiceStub{
		compareFn: func(_ context.Context, query application.CompareQuery) (domain.Diff, error) {
			if query.WorkspaceID != httpHistoryWorkspaceID || query.DocumentID != httpHistoryDocumentID || query.Left != httpHistoryTarget || query.Right != domain.WorktreeRef {
				t.Fatalf("compare query=%+v", query)
			}
			return domain.Diff{
				WorkspaceID: httpHistoryWorkspaceID, DocumentID: httpHistoryDocumentID, Path: "notes/history.md",
				Head: httpHistoryHead, Left: httpHistoryTarget, Right: domain.WorktreeRef,
				LeftContent: "old\n", RightContent: "current\n", Patch: "-old\n+current\n",
				DiffHash: strings.Repeat("f", 64),
			}, nil
		},
	}
	response := historyHTTPRequest(t, newHistoryHTTPRouter(service), stdhttp.MethodGet,
		historyHTTPPath("/history/compare")+"?left="+httpHistoryTarget+"&right="+domain.WorktreeRef, "", nil, nil)
	if response.Code != stdhttp.StatusOK || !strings.Contains(response.Body.String(), `"right":"WORKTREE"`) || !strings.Contains(response.Body.String(), `"left_content":"old\n"`) {
		t.Fatalf("compare status=%d body=%s", response.Code, response.Body.String())
	}
	if service.compareCalls != 1 {
		t.Fatalf("compare calls=%d", service.compareCalls)
	}
}

func TestHandlerFailsClosedWhenUnavailableAndMapsOutputLimit(t *testing.T) {
	unavailable := historyHTTPRequest(t, newHistoryHTTPRouter(nil), stdhttp.MethodGet, historyHTTPPath("/history"), "", nil, nil)
	problem := historyHTTPProblem(t, unavailable)
	if unavailable.Code != stdhttp.StatusServiceUnavailable || problem.ErrorCode != errorUnavailable || !problem.Retryable {
		t.Fatalf("unavailable status=%d problem=%+v", unavailable.Code, problem)
	}

	service := &historyHTTPServiceStub{
		listFn: func(context.Context, application.HistoryQuery) (domain.Page, error) {
			return domain.Page{}, foundation.NewError(
				foundation.ErrorInvalidInput,
				application.ErrorCodeOutputTooLarge,
				false,
				errors.New("bounded Git output exceeded"),
			)
		},
	}
	tooLarge := historyHTTPRequest(t, newHistoryHTTPRouter(service), stdhttp.MethodGet, historyHTTPPath("/history"), "", nil, nil)
	problem = historyHTTPProblem(t, tooLarge)
	if tooLarge.Code != stdhttp.StatusRequestEntityTooLarge || problem.ErrorCode != application.ErrorCodeOutputTooLarge || problem.Retryable {
		t.Fatalf("too large status=%d problem=%+v", tooLarge.Code, problem)
	}
}

type historyHTTPServiceStub struct {
	listFn       func(context.Context, application.HistoryQuery) (domain.Page, error)
	compareFn    func(context.Context, application.CompareQuery) (domain.Diff, error)
	previewFn    func(context.Context, application.PreviewCommand) (domain.RestorePreview, error)
	createFn     func(context.Context, application.RestoreCommand) (application.RestoreProposalReceipt, error)
	listCalls    int
	compareCalls int
	previewCalls int
	createCalls  int
}

func (service *historyHTTPServiceStub) ListHistory(ctx context.Context, query application.HistoryQuery) (domain.Page, error) {
	service.listCalls++
	if service.listFn == nil {
		return domain.Page{}, errors.New("unexpected ListHistory call")
	}
	return service.listFn(ctx, query)
}

func (service *historyHTTPServiceStub) CompareVersions(ctx context.Context, query application.CompareQuery) (domain.Diff, error) {
	service.compareCalls++
	if service.compareFn == nil {
		return domain.Diff{}, errors.New("unexpected CompareVersions call")
	}
	return service.compareFn(ctx, query)
}

func (service *historyHTTPServiceStub) PreviewRestore(ctx context.Context, command application.PreviewCommand) (domain.RestorePreview, error) {
	service.previewCalls++
	if service.previewFn == nil {
		return domain.RestorePreview{}, errors.New("unexpected PreviewRestore call")
	}
	return service.previewFn(ctx, command)
}

func (service *historyHTTPServiceStub) CreateRestoreProposal(ctx context.Context, command application.RestoreCommand) (application.RestoreProposalReceipt, error) {
	service.createCalls++
	if service.createFn == nil {
		return application.RestoreProposalReceipt{}, errors.New("unexpected CreateRestoreProposal call")
	}
	return service.createFn(ctx, command)
}

func (service *historyHTTPServiceStub) totalCalls() int {
	return service.listCalls + service.compareCalls + service.previewCalls + service.createCalls
}

func newHistoryHTTPRouter(service Service) chi.Router {
	router := chi.NewRouter()
	router.Route("/api/v1", NewHandler(service, time.Second).Routes)
	return router
}

func historyHTTPPath(suffix string) string {
	return "/api/v1/workspaces/" + string(httpHistoryWorkspaceID) + "/documents/" + string(httpHistoryDocumentID) + suffix
}

func historyHTTPRequest(t *testing.T, router stdhttp.Handler, method, path, body string, contentTypes, keys []string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for _, value := range contentTypes {
		request.Header.Add("Content-Type", value)
	}
	for _, value := range keys {
		request.Header.Add("Idempotency-Key", value)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func historyHTTPProblem(t *testing.T, response *httptest.ResponseRecorder) httpapi.Problem {
	t.Helper()
	var problem httpapi.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("problem body=%s err=%v", response.Body.String(), err)
	}
	return problem
}

func historyHTTPObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value=%#v", value)
	}
	return object
}
