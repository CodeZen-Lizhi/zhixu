package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/gin-gonic/gin"
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
		requireExportProblem(t, response, http.StatusInternalServerError, domain.ErrorCodeResultInvalid)
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
	if response.Code != http.StatusOK || service.getCalls != 1 || service.getScope != domain.ScopeCollection {
		t.Fatalf("status=%d get calls=%d scope=%q body=%s", response.Code, service.getCalls, service.getScope, response.Body.String())
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
	if response.Code != http.StatusOK || service.downloadCalls != 1 || service.downloadScope != domain.ScopeCollection {
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

func TestAttachmentExportRoutesUseWorkspaceTaggedContract(t *testing.T) {
	job := validAttachmentHTTPJob()
	service := &exportHTTPFake{
		create: exportapp.CreateResult{Job: job},
		list:   exportapp.ListPage{Items: []domain.Job{job}, NextCursor: "next-attachment-page"},
		get:    job,
	}
	router := exportHTTPRouter(service)
	routePath := "/workspaces/" + string(exportHTTPWorkspaceID) + "/attachment-exports"
	body := `{"kind":"ATTACHMENTS_ZIP","schema_version":"attachment-export/v1","attachment_root_contract_version":"workspace-attachments/v1","content_policy":"RAW_USER_OWNED","expires_in_seconds":3600}`
	request := httptest.NewRequest(http.MethodPost, routePath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "attachment-http-create")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || service.createCalls != 1 || service.createRequest.Scope.Kind != domain.ScopeWorkspaceAttachments ||
		service.createRequest.Kind != domain.KindAttachmentsZIP || service.createRequest.Redaction != domain.RedactionRawUserOwned ||
		service.createRequest.Scope.CollectionID != nil || service.createRequest.Scope.AttachmentRootContractVersion != attachmentRootVersion {
		t.Fatalf("attachment create status=%d request=%#v body=%s", response.Code, service.createRequest, response.Body.String())
	}
	wantLocation := "/api/v1/workspaces/" + string(exportHTTPWorkspaceID) + "/attachment-exports/" + string(job.ID)
	if response.Header().Get("Location") != wantLocation || !strings.Contains(response.Body.String(), `"scope_kind":"WORKSPACE_ATTACHMENTS"`) ||
		!strings.Contains(response.Body.String(), `"content_policy":"RAW_USER_OWNED"`) || strings.Contains(response.Body.String(), "collection_id") {
		t.Fatalf("attachment create location=%q body=%s", response.Header().Get("Location"), response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, routePath+"?limit=10", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.listCalls != 1 || service.listQuery.ScopeKind != domain.ScopeWorkspaceAttachments ||
		service.listQuery.CollectionID != nil || service.listQuery.WorkspaceID != exportHTTPWorkspaceID || service.listQuery.Limit != 10 ||
		!strings.Contains(response.Body.String(), `"next_cursor":"next-attachment-page"`) {
		t.Fatalf("attachment list status=%d query=%#v body=%s", response.Code, service.listQuery, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, routePath+"/"+string(job.ID), nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.getScope != domain.ScopeWorkspaceAttachments || !strings.Contains(response.Body.String(), `"kind":"ATTACHMENTS_ZIP"`) {
		t.Fatalf("attachment detail status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAttachmentExportDownloadUsesStrictZIPHeaders(t *testing.T) {
	job := validAttachmentHTTPSucceededJob()
	payload := []byte("PK\x03\x04attachment archive")
	job.FileSize = int64(len(payload))
	service := &exportHTTPFake{downloadJob: job, downloadPayload: payload}
	router := exportHTTPRouter(service)
	path := "/workspaces/" + string(exportHTTPWorkspaceID) + "/attachment-exports/" + string(job.ID) + "/download"
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.downloadCalls != 1 || service.downloadScope != domain.ScopeWorkspaceAttachments || !bytes.Equal(response.Body.Bytes(), payload) {
		t.Fatalf("attachment download status=%d calls=%d body=%q", response.Code, service.downloadCalls, response.Body.Bytes())
	}
	for header, want := range map[string]string{
		"Content-Type":           "application/zip",
		"Content-Length":         strconv.Itoa(len(payload)),
		"Cache-Control":          "private, no-store",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := response.Header().Get(header); got != want {
			t.Fatalf("attachment download header %s=%q want=%q", header, got, want)
		}
	}
	if got := response.Header().Get("Content-Disposition"); got != `attachment; filename="workspace-attachments-`+string(job.ID)+`.zip"` {
		t.Fatalf("attachment Content-Disposition=%q", got)
	}
}

func TestAttachmentExportCreateRejectsUnknownAndCrossScopeFields(t *testing.T) {
	service := &exportHTTPFake{}
	router := exportHTTPRouter(service)
	for name, body := range map[string]string{
		"unknown":          `{"kind":"ATTACHMENTS_ZIP","schema_version":"attachment-export/v1","attachment_root_contract_version":"workspace-attachments/v1","content_policy":"RAW_USER_OWNED","unknown":true}`,
		"collection field": `{"kind":"ATTACHMENTS_ZIP","schema_version":"attachment-export/v1","attachment_root_contract_version":"workspace-attachments/v1","content_policy":"RAW_USER_OWNED","collection_id":"` + string(exportHTTPCollectionID) + `"}`,
		"fake kind":        `{"kind":"AUDIT_JSON","schema_version":"attachment-export/v1","attachment_root_contract_version":"workspace-attachments/v1","content_policy":"RAW_USER_OWNED"}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/workspaces/"+string(exportHTTPWorkspaceID)+"/attachment-exports", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "attachment-http-invalid")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || service.createCalls != 0 {
				t.Fatalf("invalid attachment create status=%d calls=%d body=%s", response.Code, service.createCalls, response.Body.String())
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
		{name: "idempotency conflict", err: foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeConflict, false, errors.New("conflict")), want: http.StatusConflict, code: domain.ErrorCodeConflict},
		{name: "result not ready", err: foundation.NewError(foundation.ErrorVersionConflict, "EXPORT_RESULT_NOT_READY", true, errors.New("not ready")), want: http.StatusConflict, code: "EXPORT_RESULT_NOT_READY"},
		{name: "result inconsistent", err: foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeResultInvalid, false, errors.New("hash mismatch")), want: http.StatusInternalServerError, code: domain.ErrorCodeResultInvalid},
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
	createRequest   domain.CreateRequest
	get             domain.Job
	getErr          error
	getCalls        int
	getScope        domain.ScopeKind
	list            exportapp.ListPage
	listErr         error
	listCalls       int
	listQuery       exportapp.ListQuery
	downloadJob     domain.Job
	downloadPayload []byte
	downloadErr     error
	downloadCalls   int
	downloadActor   exportapp.DownloadActor
	downloadScope   domain.ScopeKind
}

func (fake *exportHTTPFake) Create(_ context.Context, request domain.CreateRequest) (exportapp.CreateResult, error) {
	fake.createCalls++
	fake.createRequest = request
	return fake.create, fake.createErr
}

func (fake *exportHTTPFake) GetForScope(_ context.Context, _, _ foundation.ID, scopeKind domain.ScopeKind) (domain.Job, error) {
	fake.getCalls++
	fake.getScope = scopeKind
	return fake.get, fake.getErr
}

func (fake *exportHTTPFake) List(_ context.Context, query exportapp.ListQuery) (exportapp.ListPage, error) {
	fake.listCalls++
	fake.listQuery = query
	return fake.list, fake.listErr
}

func (fake *exportHTTPFake) DownloadAsForScope(_ context.Context, _, _ foundation.ID, scopeKind domain.ScopeKind, actor exportapp.DownloadActor) (domain.Job, io.ReadCloser, error) {
	fake.downloadCalls++
	fake.downloadScope = scopeKind
	fake.downloadActor = actor
	if fake.downloadErr != nil {
		return domain.Job{}, nil, fake.downloadErr
	}
	return fake.downloadJob, io.NopCloser(bytes.NewReader(fake.downloadPayload)), nil
}

func exportHTTPRouter(service Service) http.Handler {
	router := gin.New()
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
		Scope:  domain.Scope{Kind: domain.ScopeCollection, CollectionID: &collectionID, CollectionVersion: &collectionVersion, QueryHash: strings.Repeat("a", 64)},
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

func validAttachmentHTTPJob() domain.Job {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	return domain.Job{
		ID: exportHTTPJobID, WorkspaceID: exportHTTPWorkspaceID, Kind: domain.KindAttachmentsZIP, SchemaVersion: attachmentSchemaVersion,
		Scope:  domain.Scope{Kind: domain.ScopeWorkspaceAttachments, AttachmentRootContractVersion: attachmentRootVersion},
		Fields: []domain.Field{}, Redaction: domain.RedactionRawUserOwned, PermissionScope: "READ_LOCAL", RequestedBy: "local",
		IdempotencyKey: "attachment-export-http-test", RequestHash: strings.Repeat("b", 64), RequestTTLSeconds: 3600,
		Status: domain.StatusPending, Version: 1, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		CleanupStatus: domain.CleanupNotRequired,
	}
}

func validAttachmentHTTPSucceededJob() domain.Job {
	job := validAttachmentHTTPJob()
	startedAt := job.CreatedAt.Add(time.Minute)
	preparedAt := job.CreatedAt.Add(2 * time.Minute)
	completedAt := job.CreatedAt.Add(3 * time.Minute)
	entryCount := int64(2)
	totalBytes := int64(42)
	job.Status = domain.StatusSucceeded
	job.Version = 4
	job.StartedAt = &startedAt
	job.PreparedAt = &preparedAt
	job.CompletedAt = &completedAt
	job.UpdatedAt = completedAt
	job.ManifestHash = strings.Repeat("c", 64)
	job.EntryCount = &entryCount
	job.TotalUncompressedBytes = &totalBytes
	job.PreparedStagingPath = ".knowledge/exports/.staging/" + string(job.ID) + "-" + strings.Repeat("e", 32) + ".zip.stage"
	job.FilePath = ".knowledge/exports/" + string(job.ID) + ".zip"
	job.FileHash = strings.Repeat("d", 64)
	job.FileSize = 24
	return job
}
