package capturehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/gin-gonic/gin"
)

var captureHTTPNow = time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)

func TestCreateJSONStrictlyDispatchesTextAndReplays(t *testing.T) {
	service := &captureHTTPService{createResult: captureapp.CreateResult{Capture: captureHTTPRecord(domain.KindText), Replayed: true}}
	router := captureHTTPRouter(service)
	request := httptest.NewRequest(http.MethodPost, captureHTTPWorkspacePath()+"/captures", strings.NewReader(`{"kind":"TEXT","display_name":"随手记","text":"Evidence first"}`))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Idempotency-Key", "capture-text-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.textCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, service.textCalls, response.Body.String())
	}
	if service.text.WorkspaceID != captureHTTPID(1) || service.text.Text != "Evidence first" || service.text.IdempotencyKey != "capture-text-1" {
		t.Fatalf("command=%#v", service.text)
	}
	var wire createResponse
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if !wire.Replayed || wire.Capture.Kind != "TEXT" || wire.Capture.DetailHref == "" {
		t.Fatalf("response=%#v", wire)
	}

	unknown := httptest.NewRequest(http.MethodPost, captureHTTPWorkspacePath()+"/captures", strings.NewReader(`{"kind":"TEXT","text":"x","unknown":true}`))
	unknown.Header.Set("Content-Type", "application/json")
	unknown.Header.Set("Idempotency-Key", "capture-text-2")
	unknownResponse := httptest.NewRecorder()
	router.ServeHTTP(unknownResponse, unknown)
	if unknownResponse.Code != http.StatusBadRequest || service.textCalls != 1 {
		t.Fatalf("unknown status=%d calls=%d body=%s", unknownResponse.Code, service.textCalls, unknownResponse.Body.String())
	}
}

func TestCreateUploadUsesServerDetectedMediaAndRejectsShape(t *testing.T) {
	service := &captureHTTPService{createResult: captureapp.CreateResult{Capture: captureHTTPRecord(domain.KindImage)}}
	router := captureHTTPRouter(service)
	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)
	_ = form.WriteField("kind", "IMAGE")
	_ = form.WriteField("display_name", "架构截图")
	file, err := form.CreateFormFile("file", "screen.bin")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, captureHTTPWorkspacePath()+"/capture-files", body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("Idempotency-Key", "capture-image-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || service.uploadCalls != 1 || service.upload.MediaType != "image/png" || service.upload.Kind != domain.KindImage {
		t.Fatalf("status=%d command=%#v body=%s", response.Code, service.upload, response.Body.String())
	}

	badBody := &bytes.Buffer{}
	badForm := multipart.NewWriter(badBody)
	_ = badForm.WriteField("kind", "IMAGE")
	_ = badForm.WriteField("extra", "no")
	badFile, _ := badForm.CreateFormFile("file", "screen.png")
	_, _ = badFile.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
	_ = badForm.Close()
	badRequest := httptest.NewRequest(http.MethodPost, captureHTTPWorkspacePath()+"/capture-files", badBody)
	badRequest.Header.Set("Content-Type", badForm.FormDataContentType())
	badRequest.Header.Set("Idempotency-Key", "capture-image-2")
	badResponse := httptest.NewRecorder()
	router.ServeHTTP(badResponse, badRequest)
	if badResponse.Code != http.StatusBadRequest || service.uploadCalls != 1 {
		t.Fatalf("bad status=%d calls=%d body=%s", badResponse.Code, service.uploadCalls, badResponse.Body.String())
	}
}

func TestListCursorBindsWorkspaceAndFilters(t *testing.T) {
	record := captureHTTPRecord(domain.KindURL)
	service := &captureHTTPService{page: captureapp.Page{Items: []domain.Capture{record}, Next: &captureapp.Cursor{CapturedAt: record.CapturedAt, ID: record.ID}}}
	router := captureHTTPRouter(service)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, captureHTTPWorkspacePath()+"/captures?kind=URL&limit=1", nil))
	if response.Code != http.StatusOK || service.listCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, service.listCalls, response.Body.String())
	}
	var page listResponse
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor == "" || page.Items[0].ProfileHref != "" {
		t.Fatalf("page=%#v", page)
	}

	otherPath := "/api/v1/workspaces/" + string(captureHTTPID(9)) + "/captures?kind=URL&limit=1&cursor=" + page.NextCursor
	other := httptest.NewRecorder()
	router.ServeHTTP(other, httptest.NewRequest(http.MethodGet, otherPath, nil))
	if other.Code != http.StatusBadRequest || service.listCalls != 1 {
		t.Fatalf("other status=%d calls=%d body=%s", other.Code, service.listCalls, other.Body.String())
	}
}

func TestHandlerFailsClosedWithoutServiceAndRejectsDuplicateKey(t *testing.T) {
	router := captureHTTPRouter(nil)
	request := httptest.NewRequest(http.MethodGet, captureHTTPWorkspacePath()+"/captures", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	service := &captureHTTPService{}
	duplicate := httptest.NewRequest(http.MethodPost, captureHTTPWorkspacePath()+"/captures", strings.NewReader(`{"kind":"URL","url":"https://example.com"}`))
	duplicate.Header.Set("Content-Type", "application/json")
	duplicate.Header.Add("Idempotency-Key", "one")
	duplicate.Header.Add("Idempotency-Key", "two")
	duplicateResponse := httptest.NewRecorder()
	captureHTTPRouter(service).ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusBadRequest || service.urlCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", duplicateResponse.Code, service.urlCalls, duplicateResponse.Body.String())
	}
}

func TestRetryStrictlySchedulesAndReplays(t *testing.T) {
	record := captureHTTPRecord(domain.KindURL)
	record.Status = domain.StatusReceived
	record.FetchStatus = domain.StagePending
	record.Version = 4
	service := &captureHTTPService{retryResult: captureapp.RetryResult{Capture: record}}
	router := captureHTTPRouter(service)

	request := httptest.NewRequest(http.MethodPost, captureHTTPWorkspacePath()+"/captures/"+string(record.ID)+"/retry", strings.NewReader(`{"expected_version":3}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "capture-retry-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || service.retryCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, service.retryCalls, response.Body.String())
	}
	if service.retry.WorkspaceID != captureHTTPID(1) || service.retry.CaptureID != record.ID ||
		service.retry.ExpectedVersion != 3 || service.retry.IdempotencyKey != "capture-retry-1" {
		t.Fatalf("command=%#v", service.retry)
	}

	service.retryResult.Replayed = true
	replayRequest := httptest.NewRequest(http.MethodPost, captureHTTPWorkspacePath()+"/captures/"+string(record.ID)+"/retry", strings.NewReader(`{"expected_version":3}`))
	replayRequest.Header.Set("Content-Type", "application/json")
	replayRequest.Header.Set("Idempotency-Key", "capture-retry-1")
	replay := httptest.NewRecorder()
	router.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}

	invalidRequest := httptest.NewRequest(http.MethodPost, captureHTTPWorkspacePath()+"/captures/"+string(record.ID)+"/retry", strings.NewReader(`{"expected_version":3,"extra":true}`))
	invalidRequest.Header.Set("Content-Type", "application/json")
	invalidRequest.Header.Set("Idempotency-Key", "capture-retry-2")
	invalidResponse := httptest.NewRecorder()
	router.ServeHTTP(invalidResponse, invalidRequest)
	if invalidResponse.Code != http.StatusBadRequest || service.retryCalls != 2 {
		t.Fatalf("invalid status=%d calls=%d body=%s", invalidResponse.Code, service.retryCalls, invalidResponse.Body.String())
	}
}

func TestKnowledgeProfileReadAndRetryUseBoundedStrictContract(t *testing.T) {
	ready := captureHTTPProfileView(t, domain.ProfileStatusReady, false)
	reader := &captureHTTPProfileReader{view: ready}
	router := captureHTTPProfileRouter(&captureHTTPService{}, reader, nil)
	path := captureHTTPWorkspacePath() + "/source-versions/" + string(ready.Profile.SourceVersionID) + "/knowledge-profile"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK || reader.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, reader.calls, response.Body.String())
	}
	var wire profileViewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Profile.ID != string(ready.Profile.ID) || wire.Revision == nil || wire.Revision.ContentDigest != ready.Revision.ContentDigest {
		t.Fatalf("response=%#v", wire)
	}

	pending := ready
	pending.Profile.Status = domain.ProfileStatusPending
	pending.Profile.Version++
	pending.Profile.UpdatedAt = pending.Profile.UpdatedAt.Add(time.Minute)
	retrier := &captureHTTPProfileRetrier{result: captureapp.ProfileRetryResult{View: pending}}
	retryRouter := captureHTTPProfileRouter(&captureHTTPService{}, reader, retrier)
	retry := httptest.NewRequest(http.MethodPost, path+"/retry", strings.NewReader(`{"expected_version":2}`))
	retry.Header.Set("Content-Type", "application/json")
	retry.Header.Set("Idempotency-Key", "profile-retry-1")
	retryResponse := httptest.NewRecorder()
	retryRouter.ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusAccepted || retrier.calls != 1 || retrier.command.ExpectedVersion != 2 ||
		retrier.command.WorkspaceID != captureHTTPID(1) || retrier.command.SourceVersionID != ready.Profile.SourceVersionID {
		t.Fatalf("status=%d command=%#v body=%s", retryResponse.Code, retrier.command, retryResponse.Body.String())
	}
	var retryWire profileCommandResponse
	if err := json.Unmarshal(retryResponse.Body.Bytes(), &retryWire); err != nil {
		t.Fatal(err)
	}
	if retryWire.Replayed || retryWire.Revision == nil || retryWire.Revision.ID != string(ready.Revision.ID) ||
		retryWire.Profile.CurrentRevisionID != string(ready.Revision.ID) {
		t.Fatalf("retry response=%#v", retryWire)
	}
	retrier.result.Replayed = true
	replay := httptest.NewRequest(http.MethodPost, path+"/retry", strings.NewReader(`{"expected_version":2}`))
	replay.Header.Set("Content-Type", "application/json")
	replay.Header.Set("Idempotency-Key", "profile-retry-1")
	replayResponse := httptest.NewRecorder()
	retryRouter.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusOK || retrier.calls != 2 {
		t.Fatalf("replay status=%d calls=%d body=%s", replayResponse.Code, retrier.calls, replayResponse.Body.String())
	}
	var replayWire profileCommandResponse
	if err := json.Unmarshal(replayResponse.Body.Bytes(), &replayWire); err != nil {
		t.Fatal(err)
	}
	if !replayWire.Replayed || replayWire.Revision == nil || replayWire.Revision.ID != retryWire.Revision.ID ||
		replayWire.Profile.CurrentRevisionID != retryWire.Profile.CurrentRevisionID {
		t.Fatalf("replay response=%#v", replayWire)
	}

	invalid := httptest.NewRequest(http.MethodPost, path+"/retry", strings.NewReader(`{"expected_version":2,"unknown":true}`))
	invalid.Header.Set("Content-Type", "application/json")
	invalid.Header.Set("Idempotency-Key", "profile-retry-2")
	invalidResponse := httptest.NewRecorder()
	retryRouter.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest || retrier.calls != 2 {
		t.Fatalf("invalid status=%d calls=%d body=%s", invalidResponse.Code, retrier.calls, invalidResponse.Body.String())
	}
}

func TestKnowledgeProfileFailsClosedWithoutReaderOrRetrier(t *testing.T) {
	path := captureHTTPWorkspacePath() + "/source-versions/" + string(captureHTTPID(4)) + "/knowledge-profile"
	getResponse := httptest.NewRecorder()
	captureHTTPRouter(&captureHTTPService{}).ServeHTTP(getResponse, httptest.NewRequest(http.MethodGet, path, nil))
	if getResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("get status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
	retry := httptest.NewRequest(http.MethodPost, path+"/retry", strings.NewReader(`{"expected_version":1}`))
	retry.Header.Set("Content-Type", "application/json")
	retry.Header.Set("Idempotency-Key", "profile-retry-unavailable")
	retryResponse := httptest.NewRecorder()
	captureHTTPRouter(&captureHTTPService{}).ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("retry status=%d body=%s", retryResponse.Code, retryResponse.Body.String())
	}
}

type captureHTTPService struct {
	createResult captureapp.CreateResult
	createErr    error
	retryResult  captureapp.RetryResult
	retryErr     error
	page         captureapp.Page
	text         captureapp.TextCommand
	url          captureapp.URLCommand
	upload       captureapp.UploadCommand
	textCalls    int
	urlCalls     int
	uploadCalls  int
	retry        captureapp.RetryCommand
	retryCalls   int
	listCalls    int
}

type captureHTTPProfileReader struct {
	view  captureapp.ProfileView
	err   error
	calls int
}

func (reader *captureHTTPProfileReader) GetProfile(_ context.Context, query captureapp.ProfileQuery) (captureapp.ProfileView, error) {
	reader.calls++
	if reader.view.Profile.WorkspaceID != query.WorkspaceID || reader.view.Profile.SourceVersionID != query.SourceVersionID {
		return captureapp.ProfileView{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_PROFILE_NOT_FOUND", false, errors.New("missing"))
	}
	return reader.view, reader.err
}

type captureHTTPProfileRetrier struct {
	result  captureapp.ProfileRetryResult
	err     error
	command captureapp.ProfileRetryCommand
	calls   int
}

func (retrier *captureHTTPProfileRetrier) RetryProfile(_ context.Context, command captureapp.ProfileRetryCommand) (captureapp.ProfileRetryResult, error) {
	retrier.calls++
	retrier.command = command
	return retrier.result, retrier.err
}

func (service *captureHTTPService) CreateText(_ context.Context, command captureapp.TextCommand) (captureapp.CreateResult, error) {
	service.textCalls++
	service.text = command
	return service.createResult, service.createErr
}

func (service *captureHTTPService) CreateURL(_ context.Context, command captureapp.URLCommand) (captureapp.CreateResult, error) {
	service.urlCalls++
	service.url = command
	return service.createResult, service.createErr
}

func (service *captureHTTPService) CreateUpload(_ context.Context, command captureapp.UploadCommand) (captureapp.CreateResult, error) {
	service.uploadCalls++
	service.upload = command
	return service.createResult, service.createErr
}

func (service *captureHTTPService) Retry(_ context.Context, command captureapp.RetryCommand) (captureapp.RetryResult, error) {
	service.retryCalls++
	service.retry = command
	return service.retryResult, service.retryErr
}

func (service *captureHTTPService) Get(_ context.Context, workspaceID, captureID foundation.ID) (domain.Capture, error) {
	if service.createResult.Capture.WorkspaceID != workspaceID || service.createResult.Capture.ID != captureID {
		return domain.Capture{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_NOT_FOUND", false, errors.New("missing"))
	}
	return service.createResult.Capture, nil
}

func (service *captureHTTPService) List(_ context.Context, query captureapp.ListQuery) (captureapp.Page, error) {
	service.listCalls++
	if query.WorkspaceID != captureHTTPID(1) {
		return captureapp.Page{}, errors.New("wrong workspace reached service")
	}
	return service.page, nil
}

func captureHTTPRouter(service Service) http.Handler {
	return captureHTTPProfileRouter(service, nil, nil)
}

func captureHTTPProfileRouter(service Service, profiles captureapp.ProfileReader, retrier captureapp.ProfileRetrier) http.Handler {
	router := gin.New()
	NewHandlerWithProfiles(service, profiles, retrier, time.Second).Routes(router.Group("/api/v1"))
	return router
}

func captureHTTPWorkspacePath() string {
	return "/api/v1/workspaces/" + string(captureHTTPID(1))
}

func captureHTTPRecord(kind domain.Kind) domain.Capture {
	record := domain.Capture{
		ID: captureHTTPID(2), WorkspaceID: captureHTTPID(1), Kind: kind, DisplayName: "Capture",
		OriginalLocation: "captures/input", SourceID: captureHTTPID(3), Status: domain.StatusReceived,
		FetchStatus: domain.StagePending, IngestionStatus: domain.StagePending, IndexStatus: domain.StagePending,
		ProfileStatus: domain.StagePending, Version: 1, CapturedAt: captureHTTPNow, UpdatedAt: captureHTTPNow,
	}
	if kind == domain.KindURL {
		record.OriginalURL = "https://example.com"
		return record
	}
	record.OriginalInputHash = strings.Repeat("a", 64)
	record.LatestSourceVersionID = captureHTTPID(4)
	record.Status = domain.StatusSourceSaved
	record.FetchStatus = domain.StageNotApplicable
	return record
}

func captureHTTPProfileView(t *testing.T, status domain.ProfileStatus, retryable bool) captureapp.ProfileView {
	t.Helper()
	profile := domain.Profile{
		ID: captureHTTPID(5), WorkspaceID: captureHTTPID(1), CaptureID: captureHTTPID(2), SourceVersionID: captureHTTPID(4),
		Status: status, Retryable: retryable, Version: 2, CreatedAt: captureHTTPNow, UpdatedAt: captureHTTPNow,
	}
	if status == domain.ProfileStatusFailed {
		profile.ErrorCode = "CAPTURE_PROFILE_GENERATION_FAILED"
	}
	if status != domain.ProfileStatusReady {
		return captureapp.ProfileView{Profile: profile}
	}
	profile.CurrentRevisionID = captureHTTPID(6)
	content := domain.ProfileContent{
		Summary:         "Capture profile",
		Topics:          []domain.ProfileCandidate{{Label: "Topic", SourceSpanIDs: []foundation.ID{captureHTTPID(7)}}},
		Terms:           []domain.ProfileCandidate{{Label: "Term", SourceSpanIDs: []foundation.ID{captureHTTPID(7)}}},
		KnowledgePoints: []domain.ProfilePoint{{Text: "Point", SourceSpanIDs: []foundation.ID{captureHTTPID(7)}}},
		Examples:        []domain.ProfilePoint{{Text: "Example", SourceSpanIDs: []foundation.ID{captureHTTPID(7)}}},
	}
	digest, err := domain.ComputeProfileDigest(content)
	if err != nil {
		t.Fatal(err)
	}
	settingsRevision := int64(1)
	revision := &domain.ProfileRevision{
		ID: profile.CurrentRevisionID, ProfileID: profile.ID, WorkspaceID: profile.WorkspaceID, SourceVersionID: profile.SourceVersionID,
		ParseProjectionID: captureHTTPID(8), IndexVersionID: captureHTTPID(9), ModelRunID: captureHTTPID(10),
		ModelSettingsRevision: &settingsRevision, PromptVersion: "v1", SchemaVersion: domain.ProfileSchemaVersion,
		Content: content, ContentDigest: digest, CreatedAt: captureHTTPNow,
	}
	return captureapp.ProfileView{Profile: profile, Revision: revision, Evidence: []domain.ProfileEvidence{{
		RevisionID: revision.ID, WorkspaceID: profile.WorkspaceID, SourceVersionID: profile.SourceVersionID,
		SourceSpanID: captureHTTPID(7), CreatedAt: captureHTTPNow,
	}}}
}

func captureHTTPID(value int) foundation.ID {
	id, err := foundation.ParseID(fmt.Sprintf("91000000-0000-4000-8000-%012d", value))
	if err != nil {
		panic(err)
	}
	return id
}
