package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/go-chi/chi/v5"
)

const (
	exportHTTPWorkspaceID  foundation.ID = "10000000-0000-4000-8000-000000000001"
	exportHTTPCollectionID foundation.ID = "20000000-0000-4000-8000-000000000001"
	exportHTTPJobID        foundation.ID = "30000000-0000-4000-8000-000000000001"
)

func TestCreateReturnsAcceptedOrReplayOK(t *testing.T) {
	for _, test := range []struct {
		name     string
		replayed bool
		want     int
	}{
		{name: "created", want: http.StatusAccepted},
		{name: "replayed", replayed: true, want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &exportHTTPFake{create: exportapp.CreateResult{Job: validExportHTTPJob(), Replayed: test.replayed}}
			request := httptest.NewRequest(http.MethodPost, "/exports", strings.NewReader(exportHTTPCreateBody()))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "export-create-1")
			response := httptest.NewRecorder()
			exportHTTPRouter(service).ServeHTTP(response, request)
			if response.Code != test.want || service.createCalls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, service.createCalls, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Location") == "" {
				t.Fatalf("missing create response headers: %#v", response.Header())
			}
		})
	}
}

func TestListRequiresOneValidCollectionIDAndBindsItems(t *testing.T) {
	invalidQueries := []string{
		"", "?collection_id=" + string(exportHTTPCollectionID) + "&collection_id=" + string(exportHTTPCollectionID), "?collection_id=not-a-uuid",
	}
	for _, query := range invalidQueries {
		t.Run("invalid_"+query, func(t *testing.T) {
			service := &exportHTTPFake{}
			response := httptest.NewRecorder()
			exportHTTPRouter(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/workspaces/"+string(exportHTTPWorkspaceID)+"/exports"+query, nil))
			requireExportProblem(t, response, http.StatusBadRequest, domain.ErrorCodeInvalid)
			if service.listCalls != 0 {
				t.Fatalf("list calls=%d", service.listCalls)
			}
		})
	}

	t.Run("passes collection query", func(t *testing.T) {
		service := &exportHTTPFake{list: exportapp.ListPage{Items: []domain.Job{validExportHTTPJob()}}}
		response := httptest.NewRecorder()
		exportHTTPRouter(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/workspaces/"+string(exportHTTPWorkspaceID)+"/exports?collection_id="+string(exportHTTPCollectionID), nil))
		if response.Code != http.StatusOK || service.listQuery.CollectionID == nil || *service.listQuery.CollectionID != exportHTTPCollectionID {
			t.Fatalf("status=%d query=%+v body=%s", response.Code, service.listQuery, response.Body.String())
		}
	})

	t.Run("rejects foreign collection item", func(t *testing.T) {
		foreign := validExportHTTPJob()
		other := foundation.ID("20000000-0000-4000-8000-000000000002")
		foreign.Scope.CollectionID = &other
		service := &exportHTTPFake{list: exportapp.ListPage{Items: []domain.Job{foreign}}}
		response := httptest.NewRecorder()
		exportHTTPRouter(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/workspaces/"+string(exportHTTPWorkspaceID)+"/exports?collection_id="+string(exportHTTPCollectionID), nil))
		requireExportProblem(t, response, http.StatusConflict, domain.ErrorCodeResultInvalid)
	})
}

func TestListCursorUsesUTF8ByteLimit(t *testing.T) {
	query := "?collection_id=" + string(exportHTTPCollectionID) + "&cursor=" + strings.Repeat("界", maxCursorBytes/len("界")+1)
	service := &exportHTTPFake{}
	response := httptest.NewRecorder()
	exportHTTPRouter(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/workspaces/"+string(exportHTTPWorkspaceID)+"/exports"+query, nil))
	requireExportProblem(t, response, http.StatusBadRequest, domain.ErrorCodeInvalid)
	if service.listCalls != 0 {
		t.Fatalf("list calls=%d", service.listCalls)
	}
}

func TestGetProjectsVersionAndPreparedSnapshot(t *testing.T) {
	job := validExportHTTPSucceededJob()
	service := &exportHTTPFake{get: job}
	response := httptest.NewRecorder()
	exportHTTPRouter(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/exports/"+string(exportHTTPJobID)+"?workspace_id="+string(exportHTTPWorkspaceID), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Version           int64   `json:"version"`
		ReadModelRevision *string `json:"read_model_revision"`
		ExactCount        *int64  `json:"exact_count"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Version != job.Version || payload.ReadModelRevision == nil || *payload.ReadModelRevision != job.ReadModelRevision || payload.ExactCount == nil || *payload.ExactCount != *job.ExactCount {
		t.Fatalf("projection=%+v job=%+v", payload, job)
	}
}

func TestDownloadPassesAuditedActorAndSecurityHeaders(t *testing.T) {
	job := validExportHTTPSucceededJob()
	payload := []byte("export bytes")
	job.FileSize = int64(len(payload))
	service := &exportHTTPFake{downloadJob: job, downloadPayload: payload}
	response := httptest.NewRecorder()
	exportHTTPRouter(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/exports/"+string(exportHTTPJobID)+"/download?workspace_id="+string(exportHTTPWorkspaceID), nil))
	if response.Code != http.StatusOK || service.downloadCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, service.downloadCalls, response.Body.String())
	}
	if service.downloadActor != (exportapp.DownloadActor{Type: auditdomain.ActorAnonymous, Ref: "local"}) {
		t.Fatalf("download actor=%+v", service.downloadActor)
	}
	for header, want := range map[string]string{
		"Cache-Control":          "private, no-store",
		"X-Content-Type-Options": "nosniff",
		"Content-Length":         "12",
		"Content-Type":           "text/markdown; charset=utf-8",
	} {
		if got := response.Header().Get(header); got != want {
			t.Fatalf("header %s=%q want=%q", header, got, want)
		}
	}
	if !strings.Contains(response.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("Content-Disposition=%q", response.Header().Get("Content-Disposition"))
	}
}

func TestDownloadActorMapsKnownPrincipalsAndFailsClosed(t *testing.T) {
	principalID := foundation.ID("40000000-0000-4000-8000-000000000001")
	for _, test := range []struct {
		name      string
		principal authdomain.Principal
		want      exportapp.DownloadActor
		wantError bool
	}{
		{name: "session", principal: authdomain.Principal{Kind: authdomain.PrincipalSession, ID: principalID}, want: exportapp.DownloadActor{Type: auditdomain.ActorUser, Ref: string(principalID)}},
		{name: "api token", principal: authdomain.Principal{Kind: authdomain.PrincipalAPIToken, ID: principalID}, want: exportapp.DownloadActor{Type: auditdomain.ActorAPIToken, Ref: string(principalID)}},
		{name: "unknown", principal: authdomain.Principal{Kind: "UNKNOWN", ID: principalID}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := downloadActorForPrincipal(test.principal)
			if (err != nil) != test.wantError || !test.wantError && got != test.want {
				t.Fatalf("actor=%+v err=%v", got, err)
			}
		})
	}
}

func TestWriteErrorMapsExportProblems(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
		code string
	}{
		{name: "invalid", err: foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, errors.New("invalid")), want: http.StatusBadRequest, code: domain.ErrorCodeInvalid},
		{name: "unsupported media", err: foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("media")), want: http.StatusUnsupportedMediaType, code: "UNSUPPORTED_MEDIA_TYPE"},
		{name: "not found", err: foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeNotFound, false, errors.New("missing")), want: http.StatusNotFound, code: domain.ErrorCodeNotFound},
		{name: "conflict", err: foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeConflict, false, errors.New("conflict")), want: http.StatusConflict, code: domain.ErrorCodeConflict},
		{name: "expired", err: foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeExpired, false, errors.New("expired")), want: http.StatusGone, code: domain.ErrorCodeExpired},
		{name: "permission", err: foundation.NewError(foundation.ErrorPermissionDenied, domain.ErrorCodePermissionDenied, false, errors.New("denied")), want: http.StatusForbidden, code: domain.ErrorCodePermissionDenied},
		{name: "unavailable", err: foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, errors.New("unavailable")), want: http.StatusServiceUnavailable, code: domain.ErrorCodeUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeError(response, test.err)
			requireExportProblem(t, response, test.want, test.code)
		})
	}
}

type exportHTTPFake struct {
	create          exportapp.CreateResult
	createErr       error
	createCalls     int
	get             domain.Job
	getErr          error
	list            exportapp.ListPage
	listErr         error
	listCalls       int
	listQuery       exportapp.ListQuery
	downloadJob     domain.Job
	downloadPayload []byte
	downloadErr     error
	downloadCalls   int
	downloadActor   exportapp.DownloadActor
}

func (fake *exportHTTPFake) Create(_ context.Context, _ domain.CreateRequest) (exportapp.CreateResult, error) {
	fake.createCalls++
	return fake.create, fake.createErr
}

func (fake *exportHTTPFake) Get(_ context.Context, _, _ foundation.ID) (domain.Job, error) {
	return fake.get, fake.getErr
}

func (fake *exportHTTPFake) List(_ context.Context, query exportapp.ListQuery) (exportapp.ListPage, error) {
	fake.listCalls++
	fake.listQuery = query
	return fake.list, fake.listErr
}

func (fake *exportHTTPFake) DownloadAs(_ context.Context, _, _ foundation.ID, actor exportapp.DownloadActor) (domain.Job, []byte, error) {
	fake.downloadCalls++
	fake.downloadActor = actor
	return fake.downloadJob, fake.downloadPayload, fake.downloadErr
}

func exportHTTPRouter(service Service) http.Handler {
	router := chi.NewRouter()
	NewHandler(service).Routes(router)
	return router
}

func requireExportProblem(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	var problem httpapi.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || response.Code != wantStatus || problem.ErrorCode != wantCode {
		t.Fatalf("status=%d problem=%+v err=%v body=%s", response.Code, problem, err, response.Body.String())
	}
}

func exportHTTPCreateBody() string {
	return `{"workspace_id":"` + string(exportHTTPWorkspaceID) + `","kind":"MARKDOWN","collection_id":"` + string(exportHTTPCollectionID) + `","collection_version":1,"query_hash":"` + strings.Repeat("a", 64) + `"}`
}

func validExportHTTPJob() domain.Job {
	collectionID := exportHTTPCollectionID
	collectionVersion := int64(1)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	return domain.Job{
		ID: exportHTTPJobID, WorkspaceID: exportHTTPWorkspaceID, Kind: domain.KindMarkdown, SchemaVersion: "export/v1",
		Scope:  domain.Scope{CollectionID: &collectionID, CollectionVersion: &collectionVersion, QueryHash: strings.Repeat("a", 64)},
		Fields: []domain.Field{domain.FieldID}, Redaction: domain.RedactionMasked, PermissionScope: "READ_LOCAL", RequestedBy: "local",
		IdempotencyKey: "export-http-test", RequestHash: strings.Repeat("b", 64), RequestTTLSeconds: 3600, Status: domain.StatusPending, Version: 1,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, CleanupStatus: domain.CleanupNotRequired,
	}
}

func validExportHTTPSucceededJob() domain.Job {
	job := validExportHTTPJob()
	startedAt := job.CreatedAt.Add(time.Minute)
	preparedAt := job.CreatedAt.Add(2 * time.Minute)
	completedAt := job.CreatedAt.Add(3 * time.Minute)
	exactCount := int64(2)
	job.Status = domain.StatusSucceeded
	job.Version = 3
	job.StartedAt = &startedAt
	job.PreparedAt = &preparedAt
	job.CompletedAt = &completedAt
	job.UpdatedAt = completedAt
	job.ReadModelRevision = strings.Repeat("c", 64)
	job.ExactCount = &exactCount
	job.PreparedStagingPath = ".knowledge/exports/staging/export-http-test"
	job.FilePath = ".knowledge/exports/export-http-test.md"
	job.FileHash = strings.Repeat("d", 64)
	job.FileSize = 12
	return job
}
