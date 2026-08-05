package authoringhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/go-chi/chi/v5"
)

const (
	testWorkspaceID        foundation.ID = "11111111-1111-4111-8111-111111111111"
	testDraftID            foundation.ID = "22222222-2222-4222-8222-222222222222"
	testDocumentID         foundation.ID = "33333333-3333-4333-8333-333333333333"
	testRevisionID         foundation.ID = "44444444-4444-4444-8444-444444444444"
	testReservationID      foundation.ID = "55555555-5555-4555-8555-555555555555"
	testPublicationID      foundation.ID = "66666666-6666-4666-8666-666666666666"
	testProposalID         foundation.ID = "77777777-7777-4777-8777-777777777777"
	testProposalRevisionID foundation.ID = "88888888-8888-4888-8888-888888888888"
	testPublishedDocument  foundation.ID = "99999999-9999-4999-8999-999999999999"
	testPublishedRevision  foundation.ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

var testNow = time.Date(2026, 8, 3, 1, 2, 3, 456000000, time.UTC)

type fakeService struct {
	createFn       func(context.Context, authoringapp.CreateCommand) (authoringapp.CreateResult, error)
	getFn          func(context.Context, foundation.ID, foundation.ID) (domain.WorkingDraft, error)
	updateFn       func(context.Context, authoringapp.UpdateCommand) (authoringapp.UpdateResult, error)
	listFn         func(context.Context, authoringapp.ListQuery) (authoringapp.Page, error)
	documentListFn func(context.Context, authoringapp.DocumentListQuery) (authoringapp.DocumentPage, error)
	freezeFn       func(context.Context, authoringapp.FreezeCommand) (authoringapp.FreezeResult, error)
	publishFn      func(context.Context, authoringapp.PublishCommand) (authoringapp.PublishResult, error)
	detailFn       func(context.Context, foundation.ID, foundation.ID) (authoringapp.DocumentDetail, error)
	overviewFn     func(context.Context, foundation.ID) (authoringapp.Overview, error)
}

func (service *fakeService) CreateWorkingDraft(ctx context.Context, command authoringapp.CreateCommand) (authoringapp.CreateResult, error) {
	if service.createFn == nil {
		return authoringapp.CreateResult{}, errors.New("unexpected CreateWorkingDraft call")
	}
	return service.createFn(ctx, command)
}

func (service *fakeService) GetWorkingDraft(ctx context.Context, workspaceID, draftID foundation.ID) (domain.WorkingDraft, error) {
	if service.getFn == nil {
		return domain.WorkingDraft{}, errors.New("unexpected GetWorkingDraft call")
	}
	return service.getFn(ctx, workspaceID, draftID)
}

func (service *fakeService) UpdateWorkingDraft(ctx context.Context, command authoringapp.UpdateCommand) (authoringapp.UpdateResult, error) {
	if service.updateFn == nil {
		return authoringapp.UpdateResult{}, errors.New("unexpected UpdateWorkingDraft call")
	}
	return service.updateFn(ctx, command)
}

func (service *fakeService) ListWorkingDrafts(ctx context.Context, query authoringapp.ListQuery) (authoringapp.Page, error) {
	if service.listFn == nil {
		return authoringapp.Page{}, errors.New("unexpected ListWorkingDrafts call")
	}
	return service.listFn(ctx, query)
}

func (service *fakeService) ListDocumentDrafts(ctx context.Context, query authoringapp.DocumentListQuery) (authoringapp.DocumentPage, error) {
	if service.documentListFn == nil {
		return authoringapp.DocumentPage{}, errors.New("unexpected ListDocumentDrafts call")
	}
	return service.documentListFn(ctx, query)
}

func (service *fakeService) FreezeWorkingDraft(ctx context.Context, command authoringapp.FreezeCommand) (authoringapp.FreezeResult, error) {
	if service.freezeFn == nil {
		return authoringapp.FreezeResult{}, errors.New("unexpected FreezeWorkingDraft call")
	}
	return service.freezeFn(ctx, command)
}

func (service *fakeService) PublishArticleRevision(ctx context.Context, command authoringapp.PublishCommand) (authoringapp.PublishResult, error) {
	if service.publishFn == nil {
		return authoringapp.PublishResult{}, errors.New("unexpected PublishArticleRevision call")
	}
	return service.publishFn(ctx, command)
}

func (service *fakeService) GetDocumentDetail(ctx context.Context, workspaceID, documentID foundation.ID) (authoringapp.DocumentDetail, error) {
	if service.detailFn == nil {
		return authoringapp.DocumentDetail{}, errors.New("unexpected GetDocumentDetail call")
	}
	return service.detailFn(ctx, workspaceID, documentID)
}

func (service *fakeService) GetOverview(ctx context.Context, workspaceID foundation.ID) (authoringapp.Overview, error) {
	if service.overviewFn == nil {
		return authoringapp.Overview{}, errors.New("unexpected GetOverview call")
	}
	return service.overviewFn(ctx, workspaceID)
}

func TestRoutesMatchAuthoringFrontendWireContract(t *testing.T) {
	blank := blankDraft()
	draft := editableDraft("# Java AI\n")
	frozen := frozenDraft(draft.Body)
	document := draftDocument()
	revision := draftRevision(draft.Body)
	publication := pendingPublication()
	published := publishedDocument()

	service := &fakeService{
		createFn: func(_ context.Context, command authoringapp.CreateCommand) (authoringapp.CreateResult, error) {
			if command.WorkspaceID != testWorkspaceID || command.IdempotencyKey != "create-key" {
				t.Fatalf("CreateWorkingDraft command = %#v", command)
			}
			return authoringapp.CreateResult{Draft: blank}, nil
		},
		getFn: func(_ context.Context, workspaceID, draftID foundation.ID) (domain.WorkingDraft, error) {
			if workspaceID != testWorkspaceID || draftID != testDraftID {
				t.Fatalf("GetWorkingDraft identity = %s/%s", workspaceID, draftID)
			}
			return draft, nil
		},
		updateFn: func(_ context.Context, command authoringapp.UpdateCommand) (authoringapp.UpdateResult, error) {
			if command.WorkspaceID != testWorkspaceID || command.DraftID != testDraftID || command.ExpectedVersion != 1 ||
				command.Title != "Java AI" || command.TargetPath != "java/ai.md" || command.Body != draft.Body || command.IdempotencyKey != "update-key" {
				t.Fatalf("UpdateWorkingDraft command = %#v", command)
			}
			return authoringapp.UpdateResult{Draft: draft}, nil
		},
		freezeFn: func(_ context.Context, command authoringapp.FreezeCommand) (authoringapp.FreezeResult, error) {
			if command.WorkspaceID != testWorkspaceID || command.DraftID != testDraftID || command.ExpectedVersion != draft.Version || command.IdempotencyKey != "freeze-key" {
				t.Fatalf("FreezeWorkingDraft command = %#v", command)
			}
			return authoringapp.FreezeResult{Draft: frozen, Document: document, Revision: revision}, nil
		},
		publishFn: func(_ context.Context, command authoringapp.PublishCommand) (authoringapp.PublishResult, error) {
			if command.WorkspaceID != testWorkspaceID || command.DocumentID != testDocumentID || command.RevisionID != testRevisionID || command.IdempotencyKey != "publish-key" {
				t.Fatalf("PublishArticleRevision command = %#v", command)
			}
			return authoringapp.PublishResult{Publication: publication}, nil
		},
		detailFn: func(_ context.Context, workspaceID, documentID foundation.ID) (authoringapp.DocumentDetail, error) {
			if workspaceID != testWorkspaceID || documentID != testDocumentID {
				t.Fatalf("GetDocumentDetail identity = %s/%s", workspaceID, documentID)
			}
			return authoringapp.DocumentDetail{Document: document, CurrentRevision: &revision, Publication: &publication}, nil
		},
		overviewFn: func(_ context.Context, workspaceID foundation.ID) (authoringapp.Overview, error) {
			if workspaceID != testWorkspaceID {
				t.Fatalf("GetOverview workspace = %s", workspaceID)
			}
			return authoringapp.Overview{
				WorkspaceID: workspaceID,
				Organizing:  authoringapp.OrganizingAvailability{Reason: "ORGANIZING_NOT_AVAILABLE"},
				RecentDrafts: []authoringapp.WorkingDraftSummary{{
					ID: draft.ID, WorkspaceID: draft.WorkspaceID, DocumentID: draft.DocumentID, Title: draft.Title,
					TargetPath: draft.TargetPath, Status: draft.Status, Version: draft.Version, UpdatedAt: draft.UpdatedAt,
				}}, PendingPublications: []domain.PublicationBinding{publication},
				CompletedDocuments: []domain.Document{published},
			}, nil
		},
	}
	router := newRouter(service, time.Second)

	create := serveJSON(t, router, http.MethodPost, "/workspaces/"+string(testWorkspaceID)+"/authoring/working-drafts", `{}`, "create-key")
	assertStatus(t, create, http.StatusCreated)
	createPayload := decodeObject(t, create)
	if createPayload["replayed"] != false {
		t.Fatalf("create replayed = %#v", createPayload["replayed"])
	}
	createdDraft := objectField(t, createPayload, "working_draft")
	if value, exists := createdDraft["document_id"]; !exists || value != nil {
		t.Fatalf("create document_id = %#v, exists=%v", value, exists)
	}

	get := serveJSON(t, router, http.MethodGet, "/workspaces/"+string(testWorkspaceID)+"/authoring/working-drafts/"+string(testDraftID), "", "")
	assertStatus(t, get, http.StatusOK)
	if payload := decodeObject(t, get); payload["body"] != draft.Body || payload["workspace_id"] != string(testWorkspaceID) {
		t.Fatalf("get payload = %#v", payload)
	}

	updateBody := `{"expected_version":1,"title":"Java AI","target_path":"java/ai.md","body":"# Java AI\n"}`
	update := serveJSON(t, router, http.MethodPut, "/workspaces/"+string(testWorkspaceID)+"/authoring/working-drafts/"+string(testDraftID), updateBody, "update-key")
	assertStatus(t, update, http.StatusOK)
	if objectField(t, decodeObject(t, update), "working_draft")["version"] != float64(draft.Version) {
		t.Fatalf("update response = %s", update.Body.String())
	}

	freeze := serveJSON(t, router, http.MethodPost, "/workspaces/"+string(testWorkspaceID)+"/authoring/working-drafts/"+string(testDraftID)+"/freeze", `{"expected_version":2}`, "freeze-key")
	assertStatus(t, freeze, http.StatusCreated)
	freezePayload := decodeObject(t, freeze)
	if objectField(t, freezePayload, "document")["current_published_revision_id"] != nil ||
		objectField(t, freezePayload, "article_revision")["git_commit"] != nil {
		t.Fatalf("freeze nullable wire fields = %s", freeze.Body.String())
	}

	publish := serveJSON(t, router, http.MethodPost, "/workspaces/"+string(testWorkspaceID)+"/documents/"+string(testDocumentID)+"/revisions/"+string(testRevisionID)+"/publish-proposals", `{}`, "publish-key")
	assertStatus(t, publish, http.StatusCreated)
	publishedBinding := objectField(t, decodeObject(t, publish), "publication")
	if publishedBinding["proposal_href"] != "/proposals/"+string(testProposalID) || publishedBinding["git_commit"] != nil || publishedBinding["error_code"] != nil {
		t.Fatalf("publish payload = %s", publish.Body.String())
	}

	detail := serveJSON(t, router, http.MethodGet, "/workspaces/"+string(testWorkspaceID)+"/documents/"+string(testDocumentID), "", "")
	assertStatus(t, detail, http.StatusOK)
	detailPayload := decodeObject(t, detail)
	if objectField(t, detailPayload, "current_revision")["id"] != string(testRevisionID) || objectField(t, detailPayload, "publication")["id"] != string(testPublicationID) {
		t.Fatalf("detail payload = %s", detail.Body.String())
	}

	overview := serveJSON(t, router, http.MethodGet, "/workspaces/"+string(testWorkspaceID)+"/authoring/overview", "", "")
	assertStatus(t, overview, http.StatusOK)
	overviewPayload := decodeObject(t, overview)
	recent := arrayField(t, overviewPayload, "recent_drafts")
	recentDraft, ok := recent[0].(map[string]any)
	if !ok {
		t.Fatalf("recent draft = %#v", recent[0])
	}
	if _, leaked := recentDraft["body"]; leaked {
		t.Fatalf("overview recent draft leaked body: %#v", recentDraft)
	}
	if organizing := objectField(t, overviewPayload, "organizing"); organizing["available"] != false || organizing["reason"] != "ORGANIZING_NOT_AVAILABLE" || organizing["href"] != nil {
		t.Fatalf("overview organizing = %#v", organizing)
	}
	if completed := arrayField(t, overviewPayload, "completed_documents"); len(completed) != 1 {
		t.Fatalf("completed_documents = %#v", completed)
	}

	for _, response := range []*httptest.ResponseRecorder{create, get, update, freeze, publish, detail, overview} {
		if response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("response headers = %#v", response.Header())
		}
	}
}

func TestListRoutesBindKeysetCursorsAndFormalDocumentLifecycle(t *testing.T) {
	draft := editableDraft("# Java AI\n")
	document := draftDocument()
	service := &fakeService{
		listFn: func(_ context.Context, query authoringapp.ListQuery) (authoringapp.Page, error) {
			if query.WorkspaceID != testWorkspaceID || query.Status != domain.WorkingDraftEditing || query.Limit != 1 || query.After != nil {
				t.Fatalf("working list query=%#v", query)
			}
			return authoringapp.Page{Items: []domain.WorkingDraft{draft}, Next: &authoringapp.Cursor{UpdatedAt: draft.UpdatedAt, ID: draft.ID}}, nil
		},
		documentListFn: func(_ context.Context, query authoringapp.DocumentListQuery) (authoringapp.DocumentPage, error) {
			if query.WorkspaceID != testWorkspaceID || query.Limit != 1 || query.After != nil {
				t.Fatalf("document list query=%#v", query)
			}
			return authoringapp.DocumentPage{Items: []domain.Document{document}, Next: &authoringapp.DocumentCursor{UpdatedAt: document.UpdatedAt, ID: document.ID}}, nil
		},
	}
	router := newRouter(service, time.Second)
	working := serveJSON(t, router, http.MethodGet, validCreatePath()+"?status=EDITING&limit=1", "", "")
	assertStatus(t, working, http.StatusOK)
	var workingPage workingDraftPageResponse
	if err := json.Unmarshal(working.Body.Bytes(), &workingPage); err != nil || len(workingPage.Items) != 1 || workingPage.NextCursor == "" {
		t.Fatalf("working page=%s err=%v", working.Body.String(), err)
	}
	documents := serveJSON(t, router, http.MethodGet, "/workspaces/"+string(testWorkspaceID)+"/authoring/documents?limit=1", "", "")
	assertStatus(t, documents, http.StatusOK)
	var documentPage documentPageResponse
	if err := json.Unmarshal(documents.Body.Bytes(), &documentPage); err != nil || len(documentPage.Items) != 1 || documentPage.NextCursor == "" || documentPage.Items[0].LifecycleStatus != string(domain.DocumentDraft) {
		t.Fatalf("document page=%s err=%v", documents.Body.String(), err)
	}

	other := serveJSON(t, router, http.MethodGet, "/workspaces/"+string(testPublishedDocument)+"/authoring/documents?limit=1&cursor="+documentPage.NextCursor, "", "")
	assertProblem(t, other, http.StatusBadRequest, errorCodeRequestInvalid)
	unsupportedStatus := serveJSON(t, router, http.MethodGet, "/workspaces/"+string(testWorkspaceID)+"/authoring/documents?status=DRAFT", "", "")
	assertProblem(t, unsupportedStatus, http.StatusBadRequest, errorCodeRequestInvalid)
}

func TestCreateRejectsMalformedBoundaryBeforeService(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		body        string
		contentType []string
		keys        []string
		status      int
		code        string
	}{
		{name: "missing key", path: validCreatePath(), body: `{}`, contentType: []string{"application/json"}, status: http.StatusBadRequest, code: authoringapp.ErrorCodeIdempotencyKeyInvalid},
		{name: "duplicate key", path: validCreatePath(), body: `{}`, contentType: []string{"application/json"}, keys: []string{"one", "two"}, status: http.StatusBadRequest, code: authoringapp.ErrorCodeIdempotencyKeyInvalid},
		{name: "unknown field", path: validCreatePath(), body: `{"extra":true}`, contentType: []string{"application/json"}, keys: []string{"key"}, status: http.StatusBadRequest, code: errorCodeInvalidJSON},
		{name: "duplicate field", path: validCreatePath(), body: `{"extra":1,"extra":2}`, contentType: []string{"application/json"}, keys: []string{"key"}, status: http.StatusBadRequest, code: errorCodeInvalidJSON},
		{name: "trailing value", path: validCreatePath(), body: `{} {}`, contentType: []string{"application/json"}, keys: []string{"key"}, status: http.StatusBadRequest, code: errorCodeInvalidJSON},
		{name: "null", path: validCreatePath(), body: `null`, contentType: []string{"application/json"}, keys: []string{"key"}, status: http.StatusBadRequest, code: errorCodeInvalidJSON},
		{name: "missing media type", path: validCreatePath(), body: `{}`, keys: []string{"key"}, status: http.StatusUnsupportedMediaType, code: errorCodeUnsupportedMedia},
		{name: "duplicate media type", path: validCreatePath(), body: `{}`, contentType: []string{"application/json", "application/json"}, keys: []string{"key"}, status: http.StatusUnsupportedMediaType, code: errorCodeUnsupportedMedia},
		{name: "query rejected", path: validCreatePath() + "?unused=1", body: `{}`, contentType: []string{"application/json"}, keys: []string{"key"}, status: http.StatusBadRequest, code: errorCodeRequestInvalid},
		{name: "invalid workspace", path: "/workspaces/not-a-uuid/authoring/working-drafts", body: `{}`, contentType: []string{"application/json"}, keys: []string{"key"}, status: http.StatusBadRequest, code: "INVALID_ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			service := &fakeService{createFn: func(context.Context, authoringapp.CreateCommand) (authoringapp.CreateResult, error) {
				called = true
				return authoringapp.CreateResult{Draft: blankDraft()}, nil
			}}
			router := newRouter(service, time.Second)
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			for _, value := range test.contentType {
				request.Header.Add("Content-Type", value)
			}
			for _, value := range test.keys {
				request.Header.Add("Idempotency-Key", value)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assertProblem(t, response, test.status, test.code)
			if called {
				t.Fatal("malformed request reached Authoring service")
			}
		})
	}
}

func TestUpdateBodyLimitAcceptsTenMiBAndFailsClosedAboveIt(t *testing.T) {
	t.Run("exact decoded body limit", func(t *testing.T) {
		body := strings.Repeat("a", domain.MaxBodyBytes)
		service := &fakeService{updateFn: func(_ context.Context, command authoringapp.UpdateCommand) (authoringapp.UpdateResult, error) {
			if len(command.Body) != domain.MaxBodyBytes {
				t.Fatalf("body bytes = %d", len(command.Body))
			}
			draft := editableDraft(command.Body)
			return authoringapp.UpdateResult{Draft: draft}, nil
		}}
		wire, err := json.Marshal(updateRequest{ExpectedVersion: 1, Title: "Java AI", TargetPath: "java/ai.md", Body: body})
		if err != nil {
			t.Fatal(err)
		}
		response := serveJSON(t, newRouter(service, time.Second), http.MethodPut,
			"/workspaces/"+string(testWorkspaceID)+"/authoring/working-drafts/"+string(testDraftID), string(wire), "key")
		assertStatus(t, response, http.StatusOK)
	})

	t.Run("decoded body exceeds limit", func(t *testing.T) {
		called := false
		service := &fakeService{updateFn: func(context.Context, authoringapp.UpdateCommand) (authoringapp.UpdateResult, error) {
			called = true
			return authoringapp.UpdateResult{}, nil
		}}
		wire, err := json.Marshal(updateRequest{ExpectedVersion: 1, Title: "Java AI", TargetPath: "java/ai.md", Body: strings.Repeat("a", domain.MaxBodyBytes+1)})
		if err != nil {
			t.Fatal(err)
		}
		response := serveJSON(t, newRouter(service, time.Second), http.MethodPut,
			"/workspaces/"+string(testWorkspaceID)+"/authoring/working-drafts/"+string(testDraftID), string(wire), "key")
		assertProblem(t, response, http.StatusBadRequest, errorCodeInvalidJSON)
		if called {
			t.Fatal("oversized decoded body reached service")
		}
	})

	t.Run("raw document hard cap", func(t *testing.T) {
		called := false
		service := &fakeService{updateFn: func(context.Context, authoringapp.UpdateCommand) (authoringapp.UpdateResult, error) {
			called = true
			return authoringapp.UpdateResult{}, nil
		}}
		router := newRouter(service, time.Second)
		request := httptest.NewRequest(http.MethodPut,
			"/workspaces/"+string(testWorkspaceID)+"/authoring/working-drafts/"+string(testDraftID), strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "key")
		request.ContentLength = int64(maxUpdateRequestBodyBytes) + 1
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertProblem(t, response, http.StatusBadRequest, errorCodeInvalidJSON)
		if called {
			t.Fatal("raw oversized document reached service")
		}
	})
}

func TestCommandReplaysReturnHTTP200(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		service := &fakeService{createFn: func(context.Context, authoringapp.CreateCommand) (authoringapp.CreateResult, error) {
			return authoringapp.CreateResult{Draft: blankDraft(), Replayed: true}, nil
		}}
		response := serveJSON(t, newRouter(service, time.Second), http.MethodPost, validCreatePath(), `{}`, "key")
		assertReplayResponse(t, response)
	})

	t.Run("freeze", func(t *testing.T) {
		draft := editableDraft("# Java AI\n")
		service := &fakeService{freezeFn: func(context.Context, authoringapp.FreezeCommand) (authoringapp.FreezeResult, error) {
			return authoringapp.FreezeResult{Draft: frozenDraft(draft.Body), Document: draftDocument(), Revision: draftRevision(draft.Body), Replayed: true}, nil
		}}
		response := serveJSON(t, newRouter(service, time.Second), http.MethodPost,
			"/workspaces/"+string(testWorkspaceID)+"/authoring/working-drafts/"+string(testDraftID)+"/freeze",
			`{"expected_version":2}`, "key")
		assertReplayResponse(t, response)
	})

	t.Run("publish", func(t *testing.T) {
		service := &fakeService{publishFn: func(context.Context, authoringapp.PublishCommand) (authoringapp.PublishResult, error) {
			return authoringapp.PublishResult{Publication: pendingPublication(), Replayed: true}, nil
		}}
		response := serveJSON(t, newRouter(service, time.Second), http.MethodPost,
			"/workspaces/"+string(testWorkspaceID)+"/documents/"+string(testDocumentID)+"/revisions/"+string(testRevisionID)+"/publish-proposals",
			`{}`, "key")
		assertReplayResponse(t, response)
	})
}

func TestProblemMappingTimeoutAndResponseValidation(t *testing.T) {
	t.Run("not found remains indistinguishable", func(t *testing.T) {
		service := &fakeService{getFn: func(context.Context, foundation.ID, foundation.ID) (domain.WorkingDraft, error) {
			return domain.WorkingDraft{}, foundation.NewError(foundation.ErrorNotFound, authoringapp.ErrorCodeNotFound, false, errors.New("cross-workspace or absent"))
		}}
		response := serveJSON(t, newRouter(service, time.Second), http.MethodGet,
			"/workspaces/"+string(testWorkspaceID)+"/authoring/working-drafts/"+string(testDraftID), "", "")
		assertProblem(t, response, http.StatusNotFound, authoringapp.ErrorCodeNotFound)
	})

	t.Run("version conflict", func(t *testing.T) {
		service := &fakeService{updateFn: func(context.Context, authoringapp.UpdateCommand) (authoringapp.UpdateResult, error) {
			return authoringapp.UpdateResult{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict, false, errors.New("stale"))
		}}
		response := serveJSON(t, newRouter(service, time.Second), http.MethodPut,
			"/workspaces/"+string(testWorkspaceID)+"/authoring/working-drafts/"+string(testDraftID),
			`{"expected_version":1,"title":"Java AI","target_path":"java/ai.md","body":"draft"}`, "key")
		assertProblem(t, response, http.StatusConflict, domain.ErrorCodeVersionConflict)
	})

	t.Run("handler deadline", func(t *testing.T) {
		service := &fakeService{getFn: func(ctx context.Context, _, _ foundation.ID) (domain.WorkingDraft, error) {
			<-ctx.Done()
			return domain.WorkingDraft{}, ctx.Err()
		}}
		response := serveJSON(t, newRouter(service, time.Millisecond), http.MethodGet,
			"/workspaces/"+string(testWorkspaceID)+"/authoring/working-drafts/"+string(testDraftID), "", "")
		problem := assertProblem(t, response, http.StatusServiceUnavailable, errorCodeRequestTimeout)
		if !problem.Retryable {
			t.Fatal("deadline problem must be retryable")
		}
	})

	t.Run("cross-workspace result fails closed", func(t *testing.T) {
		service := &fakeService{getFn: func(context.Context, foundation.ID, foundation.ID) (domain.WorkingDraft, error) {
			draft := editableDraft("content")
			draft.WorkspaceID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
			return draft, nil
		}}
		response := serveJSON(t, newRouter(service, time.Second), http.MethodGet,
			"/workspaces/"+string(testWorkspaceID)+"/authoring/working-drafts/"+string(testDraftID), "", "")
		assertProblem(t, response, http.StatusInternalServerError, errorCodeResultInvalid)
	})

	t.Run("duplicate overview identities fail closed", func(t *testing.T) {
		draft := editableDraft("content")
		summary := authoringapp.WorkingDraftSummary{
			ID: draft.ID, WorkspaceID: draft.WorkspaceID, DocumentID: draft.DocumentID, Title: draft.Title,
			TargetPath: draft.TargetPath, Status: draft.Status, Version: draft.Version, UpdatedAt: draft.UpdatedAt,
		}
		service := &fakeService{overviewFn: func(context.Context, foundation.ID) (authoringapp.Overview, error) {
			return authoringapp.Overview{
				WorkspaceID: testWorkspaceID, Organizing: authoringapp.OrganizingAvailability{Reason: "ORGANIZING_NOT_AVAILABLE"},
				RecentDrafts: []authoringapp.WorkingDraftSummary{summary, summary},
			}, nil
		}}
		response := serveJSON(t, newRouter(service, time.Second), http.MethodGet,
			"/workspaces/"+string(testWorkspaceID)+"/authoring/overview", "", "")
		assertProblem(t, response, http.StatusInternalServerError, errorCodeResultInvalid)
	})

	t.Run("closed publication in pending overview fails closed", func(t *testing.T) {
		publication := pendingPublication()
		publication.Status = domain.PublicationClosed
		publication.ErrorCode = "AUTHORING_PUBLICATION_PROPOSAL_REJECTED"
		service := &fakeService{overviewFn: func(context.Context, foundation.ID) (authoringapp.Overview, error) {
			return authoringapp.Overview{
				WorkspaceID:         testWorkspaceID,
				Organizing:          authoringapp.OrganizingAvailability{Reason: "ORGANIZING_NOT_AVAILABLE"},
				PendingPublications: []domain.PublicationBinding{publication},
			}, nil
		}}
		response := serveJSON(t, newRouter(service, time.Second), http.MethodGet,
			"/workspaces/"+string(testWorkspaceID)+"/authoring/overview", "", "")
		assertProblem(t, response, http.StatusInternalServerError, errorCodeResultInvalid)
	})
}

func TestAvailableFailsClosedForNilAndTypedNilServices(t *testing.T) {
	if NewHandler(nil, 0).Available() {
		t.Fatal("nil service must be unavailable")
	}
	var typedNil *fakeService
	handler := NewHandler(typedNil, 0)
	if handler.Available() {
		t.Fatal("typed nil service must be unavailable")
	}
	response := serveJSON(t, routerForHandler(handler), http.MethodPost, validCreatePath(), `{}`, "key")
	assertProblem(t, response, http.StatusServiceUnavailable, errorCodeUnavailable)
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
}

func blankDraft() domain.WorkingDraft {
	return domain.WorkingDraft{
		ID: testDraftID, WorkspaceID: testWorkspaceID, Status: domain.WorkingDraftEditing,
		Version: 1, CreatedAt: testNow, UpdatedAt: testNow,
	}
}

func editableDraft(body string) domain.WorkingDraft {
	return domain.WorkingDraft{
		ID: testDraftID, WorkspaceID: testWorkspaceID, DocumentID: testDocumentID,
		Title: "Java AI", TargetPath: "java/ai.md", Body: body, Status: domain.WorkingDraftEditing,
		Version: 2, CreatedAt: testNow, UpdatedAt: testNow.Add(time.Second),
	}
}

func frozenDraft(body string) domain.WorkingDraft {
	draft := editableDraft(body)
	draft.Version++
	draft.UpdatedAt = testNow.Add(2 * time.Second)
	return draft
}

func draftDocument() domain.Document {
	return domain.Document{
		ID: testDocumentID, WorkspaceID: testWorkspaceID, CanonicalPath: "java/ai.md", Title: "Java AI",
		Lifecycle: domain.DocumentDraft, Version: 1, CreatedAt: testNow.Add(time.Second), UpdatedAt: testNow.Add(time.Second),
	}
}

func draftRevision(content string) domain.ArticleRevision {
	return domain.ArticleRevision{
		ID: testRevisionID, WorkspaceID: testWorkspaceID, DocumentID: testDocumentID, RevisionNo: 1,
		Content: content, ContentHash: domain.ComputeContentHash(content), Status: domain.RevisionDraft,
		OptimizationMode: "NONE", CreatedByType: "USER", CreatedAt: testNow.Add(time.Second),
	}
}

func pendingPublication() domain.PublicationBinding {
	token, err := domain.ComputeAbsenceToken(testWorkspaceID, "java/ai.md")
	if err != nil {
		panic(err)
	}
	return domain.PublicationBinding{
		ID: testPublicationID, ReservationID: testReservationID, WorkspaceID: testWorkspaceID,
		DocumentID: testDocumentID, ArticleRevisionID: testRevisionID, ProposalID: testProposalID,
		ProposalRevisionID: testProposalRevisionID, TargetPath: "java/ai.md",
		ContentHash: domain.ComputeContentHash("# Java AI\n"), TargetMode: domain.ProposalTargetCreateOnly,
		AbsenceToken: token, Status: domain.PublicationPending, Version: 1,
		CreatedAt: testNow.Add(2 * time.Second), UpdatedAt: testNow.Add(2 * time.Second),
	}
}

func publishedDocument() domain.Document {
	return domain.Document{
		ID: testPublishedDocument, WorkspaceID: testWorkspaceID, CanonicalPath: "java/published.md", Title: "Published",
		Lifecycle: domain.DocumentPublished, CurrentPublishedRevisionID: testPublishedRevision,
		Version: 2, CreatedAt: testNow, UpdatedAt: testNow.Add(3 * time.Second),
	}
}

func newRouter(service Service, timeout time.Duration) http.Handler {
	return routerForHandler(NewHandler(service, timeout))
}

func routerForHandler(handler *Handler) http.Handler {
	router := chi.NewRouter()
	handler.Routes(router)
	return router
}

func validCreatePath() string {
	return "/workspaces/" + string(testWorkspaceID) + "/authoring/working-drafts"
}

func serveJSON(t *testing.T, handler http.Handler, method, path, body, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if method == http.MethodPost || method == http.MethodPut {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, want, response.Body.String())
	}
}

func assertProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) httpapi.Problem {
	t.Helper()
	assertStatus(t, response, status)
	var problem httpapi.Problem
	decodeResponse(t, response, &problem)
	if problem.ErrorCode != code || problem.Message == "" {
		t.Fatalf("problem = %#v, want code %q", problem, code)
	}
	return problem
}

func assertReplayResponse(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	assertStatus(t, response, http.StatusOK)
	if replayed, ok := decodeObject(t, response)["replayed"].(bool); !ok || !replayed {
		t.Fatalf("replay response = %s", response.Body.String())
	}
}

func decodeObject(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	decodeResponse(t, response, &payload)
	return payload
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
	}
}

func objectField(t *testing.T, payload map[string]any, field string) map[string]any {
	t.Helper()
	value, ok := payload[field].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v", field, payload[field])
	}
	return value
}

func arrayField(t *testing.T, payload map[string]any, field string) []any {
	t.Helper()
	value, ok := payload[field].([]any)
	if !ok {
		t.Fatalf("%s = %#v", field, payload[field])
	}
	return value
}

var _ Service = (*fakeService)(nil)
