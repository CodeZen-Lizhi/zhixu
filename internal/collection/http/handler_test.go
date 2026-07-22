package collectionhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/go-chi/chi/v5"
)

const (
	httpWorkspaceID  foundation.ID = "91000000-0000-4000-8000-000000000001"
	httpCollectionID foundation.ID = "91000000-0000-4000-8000-000000000002"
	httpOtherID      foundation.ID = "91000000-0000-4000-8000-000000000003"
	httpTopicID      foundation.ID = "91000000-0000-4000-8000-000000000004"
)

type fakeService struct {
	createResult                collectionapp.CommandResult
	updateResult                collectionapp.CommandResult
	archiveResult               collectionapp.CommandResult
	collection                  collectionapp.Collection
	collections                 []collectionapp.Collection
	page                        collectionapp.ResultPage
	createErr                   error
	updateErr                   error
	archiveErr                  error
	getErr                      error
	listErr                     error
	resultsErr                  error
	previewErr                  error
	createCommand               collectionapp.CreateCommand
	updateCommand               collectionapp.UpdateCommand
	archiveCommand              collectionapp.ArchiveCommand
	getWorkspace, getCollection foundation.ID
	listQuery                   collectionapp.ListQuery
	resultsQuery                collectionapp.ResultsQuery
	previewQuery                collectionapp.PreviewQuery
}

func (f *fakeService) Create(_ context.Context, command collectionapp.CreateCommand) (collectionapp.CommandResult, error) {
	f.createCommand = command
	if f.createErr != nil {
		return collectionapp.CommandResult{}, f.createErr
	}
	if f.createResult.Collection.ID == "" {
		f.createResult = commandResult(f.collection, "CREATE", 1, false)
	}
	return f.createResult, nil
}
func (f *fakeService) Update(_ context.Context, command collectionapp.UpdateCommand) (collectionapp.CommandResult, error) {
	f.updateCommand = command
	if f.updateErr != nil {
		return collectionapp.CommandResult{}, f.updateErr
	}
	return f.updateResult, nil
}
func (f *fakeService) Archive(_ context.Context, command collectionapp.ArchiveCommand) (collectionapp.CommandResult, error) {
	f.archiveCommand = command
	if f.archiveErr != nil {
		return collectionapp.CommandResult{}, f.archiveErr
	}
	return f.archiveResult, nil
}
func (f *fakeService) Get(_ context.Context, workspaceID, collectionID foundation.ID) (collectionapp.Collection, error) {
	f.getWorkspace, f.getCollection = workspaceID, collectionID
	if f.getErr != nil {
		return collectionapp.Collection{}, f.getErr
	}
	return f.collection, nil
}
func (f *fakeService) List(_ context.Context, query collectionapp.ListQuery) (collectionapp.CollectionListPage, error) {
	f.listQuery = query
	if f.listErr != nil {
		return collectionapp.CollectionListPage{}, f.listErr
	}
	return collectionapp.CollectionListPage{Items: f.collections}, nil
}
func (f *fakeService) Results(_ context.Context, query collectionapp.ResultsQuery) (collectionapp.ResultPage, error) {
	f.resultsQuery = query
	if f.resultsErr != nil {
		return collectionapp.ResultPage{}, f.resultsErr
	}
	return f.page, nil
}

func (f *fakeService) Preview(_ context.Context, query collectionapp.PreviewQuery) (collectionapp.ResultPage, error) {
	f.previewQuery = query
	if f.previewErr != nil {
		return collectionapp.ResultPage{}, f.previewErr
	}
	return f.page, nil
}

func TestCollectionRoutesLifecycleAndStrictBoundaries(t *testing.T) {
	service := &fakeService{collection: collectionFixture(collectionapp.CollectionStatusActive)}
	service.createResult = commandResult(service.collection, "CREATE", 1, false)
	service.updateResult = commandResult(collectionFixture(collectionapp.CollectionStatusActive), "UPDATE", 4, false)
	archived := collectionFixture(collectionapp.CollectionStatusArchived)
	service.archiveResult = commandResult(archived, "ARCHIVE", 5, false)
	service.collections = []collectionapp.Collection{service.collection}
	service.page = resultFixture()
	router := newRouter(service)

	createBody := definitionJSON()
	response := request(router, http.MethodPost, "/api/v1/collections", createBody, "application/json", "create-1")
	if response.Code != http.StatusCreated || service.createCommand.IdempotencyKey != "create-1" || service.createCommand.WorkspaceID != httpWorkspaceID {
		t.Fatalf("create status=%d command=%+v body=%s", response.Code, service.createCommand, response.Body.String())
	}

	updateBody := strings.TrimSuffix(strings.Replace(definitionJSON(), `"name":"Inbox"`, `"name":"Updated"`, 1), "}") + `,"expected_version":3}`
	response = request(router, http.MethodPut, "/api/v1/collections/"+string(httpCollectionID), updateBody, "application/json", "update-1")
	if response.Code != http.StatusOK || service.updateCommand.ExpectedVersion != 3 || service.updateCommand.CollectionID != httpCollectionID {
		t.Fatalf("update status=%d command=%+v body=%s", response.Code, service.updateCommand, response.Body.String())
	}
	response = request(router, http.MethodPost, "/api/v1/collections/"+string(httpCollectionID)+"/archive", `{"workspace_id":"`+string(httpWorkspaceID)+`","expected_version":4}`, "application/json", "archive-1")
	if response.Code != http.StatusOK || service.archiveCommand.ExpectedVersion != 4 {
		t.Fatalf("archive status=%d command=%+v body=%s", response.Code, service.archiveCommand, response.Body.String())
	}

	response = request(router, http.MethodGet, "/api/v1/collections?workspace_id="+string(httpWorkspaceID), "", "", "")
	if response.Code != http.StatusOK || service.listQuery.Limit != defaultCollectionListLimit {
		t.Fatalf("list status=%d query=%+v body=%s", response.Code, service.listQuery, response.Body.String())
	}
	response = request(router, http.MethodGet, "/api/v1/collections/"+string(httpCollectionID)+"?workspace_id="+string(httpWorkspaceID), "", "", "")
	if response.Code != http.StatusOK || service.getWorkspace != httpWorkspaceID || service.getCollection != httpCollectionID {
		t.Fatalf("get status=%d scope=%s/%s body=%s", response.Code, service.getWorkspace, service.getCollection, response.Body.String())
	}
	response = request(router, http.MethodGet, "/api/v1/collections/"+string(httpCollectionID)+"/results?workspace_id="+string(httpWorkspaceID)+"&limit=2&cursor=opaque", "", "", "")
	if response.Code != http.StatusOK || service.resultsQuery.Limit != 2 || service.resultsQuery.Cursor != "opaque" {
		t.Fatalf("results status=%d query=%+v body=%s", response.Code, service.resultsQuery, response.Body.String())
	}
}

func TestCollectionValidateAndPreviewUseCanonicalBoundary(t *testing.T) {
	service := &fakeService{page: resultFixture()}
	router := newRouter(service)
	body := validationJSON()
	response := request(router, http.MethodPost, "/api/v1/collections/validate", body, "application/json", "")
	if response.Code != http.StatusOK {
		t.Fatalf("validate status=%d body=%s", response.Code, response.Body.String())
	}
	var validation collectionValidationResponse
	if err := json.Unmarshal(response.Body.Bytes(), &validation); err != nil || !validation.Valid || validation.QueryHash == "" {
		t.Fatalf("validation=%+v err=%v", validation, err)
	}
	response = request(router, http.MethodPost, "/api/v1/collections/preview", body+`{"limit":1}`, "application/json", "")
	// The body above has two JSON values and must fail strict JSON before capability handling.
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid preview status=%d body=%s", response.Code, response.Body.String())
	}
	response = request(router, http.MethodPost, "/api/v1/collections/preview", previewJSON(), "application/json", "")
	if response.Code != http.StatusOK || service.previewQuery.WorkspaceID != httpWorkspaceID || service.previewQuery.Limit != defaultCollectionResultLimit {
		t.Fatalf("preview status=%d query=%+v body=%s", response.Code, service.previewQuery, response.Body.String())
	}
	var preview collectionPreviewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil || preview.QueryHash == "" || preview.ExactCount != service.page.ExactCount {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	for _, suffix := range []string{`,"limit":0}`, `,"cursor":" "}`} {
		response = request(router, http.MethodPost, "/api/v1/collections/preview", strings.TrimSuffix(previewJSON(), "}")+suffix, "application/json", "")
		problem := requireProblem(t, response)
		if response.Code != http.StatusBadRequest || problem.ErrorCode != collectionapp.ErrorCodeRequestInvalid {
			t.Fatalf("invalid preview status=%d problem=%+v body=%s", response.Code, problem, response.Body.String())
		}
	}
}

func TestCollectionCreateReplayReturnsOriginalResourceWithoutDuplicateCreationStatus(t *testing.T) {
	collection := collectionFixture(collectionapp.CollectionStatusActive)
	service := &fakeService{collection: collection, createResult: commandResult(collection, "CREATE", 1, true)}
	response := request(newRouter(service), http.MethodPost, "/api/v1/collections", definitionJSON(), "application/json", "create-replay")
	if response.Code != http.StatusOK || !service.createResult.Replayed || service.createCommand.IdempotencyKey != "create-replay" {
		t.Fatalf("status=%d result=%+v command=%+v body=%s", response.Code, service.createResult, service.createCommand, response.Body.String())
	}
}

func TestCollectionRejectsReplayWhoseResourceVersionDoesNotMatchReceipt(t *testing.T) {
	collection := collectionFixture(collectionapp.CollectionStatusActive)
	collection.Version = 5
	service := &fakeService{createResult: collectionapp.CommandResult{Collection: collection, CommandVersion: 1, CommandType: "CREATE", Replayed: true}}
	response := request(newRouter(service), http.MethodPost, "/api/v1/collections", definitionJSON(), "application/json", "stale-replay")
	problem := requireProblem(t, response)
	if response.Code != http.StatusInternalServerError || problem.ErrorCode != collectionHTTPResultInconsistent {
		t.Fatalf("status=%d problem=%+v body=%s", response.Code, problem, response.Body.String())
	}
}

func TestCollectionStrictJSONContentTypeIdempotencyAndQuery(t *testing.T) {
	router := newRouter(&fakeService{collection: collectionFixture(collectionapp.CollectionStatusActive)})
	tests := []struct {
		name, method, path, body, contentType, key string
		status                                     int
		code                                       string
	}{
		{"unknown field", http.MethodPost, "/api/v1/collections", strings.TrimSuffix(definitionJSON(), "}") + `,"unknown":true}`, "application/json", "k", http.StatusBadRequest, "INVALID_JSON"},
		{"duplicate field", http.MethodPost, "/api/v1/collections", `{"workspace_id":"` + string(httpWorkspaceID) + `","workspace_id":"` + string(httpWorkspaceID) + `"}`, "application/json", "k", http.StatusBadRequest, "INVALID_JSON"},
		{"duplicate nested field", http.MethodPost, "/api/v1/collections", strings.Replace(definitionJSON(), `"kind":"group"`, `"kind":"group","kind":"group"`, 1), "application/json", "k", http.StatusBadRequest, "INVALID_JSON"},
		{"wrong content type", http.MethodPost, "/api/v1/collections", `{}`, "text/plain", "k", http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE"},
		{"missing idempotency", http.MethodPost, "/api/v1/collections", definitionJSON(), "application/json", "", http.StatusBadRequest, collectionapp.ErrorCodeRequestInvalid},
		{"duplicate idempotency", http.MethodPost, "/api/v1/collections", definitionJSON(), "application/json", "k\nk", http.StatusBadRequest, collectionapp.ErrorCodeRequestInvalid},
		{"invalid uuid", http.MethodPost, "/api/v1/collections", strings.Replace(definitionJSON(), string(httpWorkspaceID), "bad", 1), "application/json", "k", http.StatusBadRequest, collectionapp.ErrorCodeRequestInvalid},
		{"invalid enum", http.MethodPost, "/api/v1/collections", strings.Replace(definitionJSON(), `"view_type":"LIST"`, `"view_type":"KANBAN"`, 1), "application/json", "k", http.StatusBadRequest, domain.ErrorCodeViewConfigInvalid},
		{"invalid query enum", http.MethodPost, "/api/v1/collections", strings.Replace(definitionJSON(), `"value":"TOPIC"`, `"value":"NOPE"`, 1), "application/json", "k", http.StatusBadRequest, domain.ErrorCodeQueryInvalid},
		{"invalid expected version", http.MethodPut, "/api/v1/collections/" + string(httpCollectionID), strings.TrimSuffix(definitionJSON(), "}") + `,"expected_version":0}`, "application/json", "k", http.StatusBadRequest, collectionapp.ErrorCodeRequestInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			if strings.Contains(test.key, "\n") {
				parts := strings.Split(test.key, "\n")
				request.Header.Add("Idempotency-Key", parts[0])
				request.Header.Add("Idempotency-Key", parts[1])
			} else if test.key != "" {
				request.Header.Set("Idempotency-Key", test.key)
			}
			response := httptest.NewRecorder()
			newRouter(&fakeService{collection: collectionFixture(collectionapp.CollectionStatusActive)}).ServeHTTP(response, request)
			problem := requireProblem(t, response)
			if response.Code != test.status || problem.ErrorCode != test.code {
				t.Fatalf("status=%d code=%s want status=%d code=%s body=%s", response.Code, problem.ErrorCode, test.status, test.code, response.Body.String())
			}
		})
	}
	for _, path := range []string{
		"/api/v1/collections?workspace_id=" + string(httpWorkspaceID) + "&limit=0",
		"/api/v1/collections?workspace_id=" + string(httpWorkspaceID) + "&status=NOPE",
		"/api/v1/collections?workspace_id=" + string(httpWorkspaceID) + "&status=active",
		"/api/v1/collections?workspace_id=" + string(httpWorkspaceID) + "&workspace_id=" + string(httpWorkspaceID),
		"/api/v1/collections/" + string(httpCollectionID) + "/results?workspace_id=" + string(httpWorkspaceID) + "&cursor=",
	} {
		response := request(router, http.MethodGet, path, "", "", "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestCollectionErrorsIsolationCursorConflictAndUnavailable(t *testing.T) {
	notFound := foundation.NewError(foundation.ErrorNotFound, collectionapp.ErrorCodeNotFound, false, errors.New("hidden"))
	service := &fakeService{collection: collectionFixture(collectionapp.CollectionStatusActive), getErr: notFound}
	router := newRouter(service)
	response := request(router, http.MethodGet, "/api/v1/collections/"+string(httpOtherID)+"?workspace_id="+string(httpWorkspaceID), "", "", "")
	problem := requireProblem(t, response)
	if response.Code != http.StatusNotFound || problem.ErrorCode != collectionapp.ErrorCodeNotFound || strings.Contains(problem.Message, "hidden") {
		t.Fatalf("not found status=%d problem=%+v", response.Code, problem)
	}
	service.resultsErr = foundation.NewError(foundation.ErrorVersionConflict, "COLLECTION_CURSOR_STALE", false, errors.New("revision changed"))
	response = request(router, http.MethodGet, "/api/v1/collections/"+string(httpCollectionID)+"/results?workspace_id="+string(httpWorkspaceID), "", "", "")
	problem = requireProblem(t, response)
	if response.Code != http.StatusConflict || problem.ErrorCode != "COLLECTION_CURSOR_STALE" {
		t.Fatalf("cursor status=%d problem=%+v", response.Code, problem)
	}
	service.resultsErr = foundation.NewError(foundation.ErrorDependencyUnavailable, collectionapp.ErrorCodeDependencyUnavailable, true, errors.New("db down"))
	response = request(router, http.MethodGet, "/api/v1/collections/"+string(httpCollectionID)+"/results?workspace_id="+string(httpWorkspaceID), "", "", "")
	problem = requireProblem(t, response)
	if response.Code != http.StatusServiceUnavailable || problem.ErrorCode != collectionapp.ErrorCodeDependencyUnavailable || !problem.Retryable {
		t.Fatalf("unavailable status=%d problem=%+v", response.Code, problem)
	}
	response = request(newRouter(nil), http.MethodGet, "/api/v1/collections?workspace_id="+string(httpWorkspaceID), "", "", "")
	problem = requireProblem(t, response)
	if response.Code != http.StatusServiceUnavailable || problem.ErrorCode != collectionHTTPUnavailable {
		t.Fatalf("nil service status=%d problem=%+v", response.Code, problem)
	}
}

func TestCollectionRejectsCrossWorkspaceProjection(t *testing.T) {
	collection := collectionFixture(collectionapp.CollectionStatusActive)
	collection.WorkspaceID = httpOtherID
	service := &fakeService{collection: collection}
	response := request(newRouter(service), http.MethodGet, "/api/v1/collections/"+string(httpCollectionID)+"?workspace_id="+string(httpWorkspaceID), "", "", "")
	problem := requireProblem(t, response)
	if response.Code != http.StatusInternalServerError || problem.ErrorCode != collectionHTTPResultInconsistent {
		t.Fatalf("status=%d problem=%+v", response.Code, problem)
	}
}

func newRouter(service Service) chi.Router {
	router := chi.NewRouter()
	router.Route("/api/v1", NewHandler(service, time.Second).Routes)
	return router
}

func request(router http.Handler, method, path, body, contentType, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func definitionJSON() string {
	return `{"workspace_id":"` + string(httpWorkspaceID) + `","name":"Inbox","description":"desc","query":{"schema_version":"collection-query/v1","root":{"kind":"group","operator":"AND","clauses":[{"kind":"predicate","field":"object_type","operator":"EQ","value":"TOPIC"}]}},"view_type":"LIST","view_config":{"density":"COMFORTABLE"}}`
}

func validationJSON() string {
	return `{"workspace_id":"` + string(httpWorkspaceID) + `","query":{"schema_version":"collection-query/v1","root":{"kind":"group","operator":"AND","clauses":[{"kind":"predicate","field":"object_type","operator":"EQ","value":"TOPIC"}]}},"view_type":"LIST","view_config":{"density":"COMFORTABLE"}}`
}

func previewJSON() string { return validationJSON() }

func collectionFixture(status collectionapp.CollectionStatus) collectionapp.Collection {
	query := domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{Kind: domain.ClauseKindGroup, Operator: "AND", Clauses: []domain.Clause{{Kind: domain.ClauseKindPredicate, Field: "object_type", Operator: "EQ", Value: json.RawMessage(`"TOPIC"`)}}}}
	canonical, _ := domain.CanonicalizeQuery(query)
	return collectionapp.Collection{ID: httpCollectionID, WorkspaceID: httpWorkspaceID, Name: "Inbox", Description: "desc", QuerySchemaVersion: domain.QuerySchemaVersionV1, QueryVersion: 1, Query: canonical.Definition, QueryHash: canonical.Hash, ViewType: domain.ViewTypeList, ViewConfig: domain.ViewConfig{Density: "COMFORTABLE"}, Status: status, Version: 1, CreatedAt: time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC), UpdatedAt: time.Date(2026, 7, 22, 1, 2, 4, 0, time.UTC)}
}

func resultFixture() collectionapp.ResultPage {
	return collectionapp.ResultPage{Items: []collectionapp.CollectionItem{{ObjectType: "TOPIC", ID: httpTopicID, Title: "Topic", Summary: "Summary", Status: "ACTIVE", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}}, ExactCount: 1, RevisionHash: strings.Repeat("a", 64), NextCursor: "next"}
}

func commandResult(collection collectionapp.Collection, command string, version int64, replayed bool) collectionapp.CommandResult {
	collection.Version = version
	return collectionapp.CommandResult{Collection: collection, CommandVersion: version, CommandType: command, Replayed: replayed}
}

func requireProblem(t *testing.T, response *httptest.ResponseRecorder) httpapi.Problem {
	t.Helper()
	var problem httpapi.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("problem body=%s err=%v", response.Body.String(), err)
	}
	return problem
}
