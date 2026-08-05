package organizinghttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/go-chi/chi/v5"
)

const testHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeService struct {
	createDraft     func(context.Context, organizingapp.CreateDraftCommand) (organizingapp.DraftResult, error)
	getDraft        func(context.Context, foundation.ID, foundation.ID) (domain.Draft, error)
	updateDraft     func(context.Context, organizingapp.UpdateDraftCommand) (organizingapp.DraftResult, error)
	addMaterial     func(context.Context, organizingapp.AddMaterialCommand) (organizingapp.DraftResult, error)
	removeMaterial  func(context.Context, organizingapp.RemoveMaterialCommand) (organizingapp.DraftResult, error)
	setSelection    func(context.Context, organizingapp.SetMaterialSelectionCommand) (organizingapp.DraftResult, error)
	suggest         func(context.Context, organizingapp.SuggestCommand) (organizingapp.DraftResult, error)
	searchMaterials func(context.Context, organizingapp.MaterialSearchQuery) (organizingapp.MaterialSearchPage, error)
	confirmDraft    func(context.Context, organizingapp.ConfirmCommand) (organizingapp.ConfirmResult, error)
	getSnapshot     func(context.Context, foundation.ID, foundation.ID) (domain.Snapshot, error)
	createTemplate  func(context.Context, organizingapp.CreateTemplateCommand) (organizingapp.TemplateResult, error)
	cloneTemplate   func(context.Context, organizingapp.CloneTemplateCommand) (organizingapp.TemplateResult, error)
	reviseTemplate  func(context.Context, organizingapp.ReviseTemplateCommand) (organizingapp.TemplateResult, error)
	getTemplate     func(context.Context, foundation.ID, foundation.ID) (organizingapp.TemplateDetail, error)
	listTemplates   func(context.Context, organizingapp.TemplateListQuery) (organizingapp.TemplatePage, error)
}

func (service *fakeService) CreateDraft(ctx context.Context, command organizingapp.CreateDraftCommand) (organizingapp.DraftResult, error) {
	if service.createDraft == nil {
		return organizingapp.DraftResult{}, errors.New("unexpected CreateDraft call")
	}
	return service.createDraft(ctx, command)
}
func (service *fakeService) GetDraft(ctx context.Context, workspaceID, draftID foundation.ID) (domain.Draft, error) {
	if service.getDraft == nil {
		return domain.Draft{}, errors.New("unexpected GetDraft call")
	}
	return service.getDraft(ctx, workspaceID, draftID)
}
func (service *fakeService) UpdateDraft(ctx context.Context, command organizingapp.UpdateDraftCommand) (organizingapp.DraftResult, error) {
	if service.updateDraft == nil {
		return organizingapp.DraftResult{}, errors.New("unexpected UpdateDraft call")
	}
	return service.updateDraft(ctx, command)
}
func (service *fakeService) AddMaterial(ctx context.Context, command organizingapp.AddMaterialCommand) (organizingapp.DraftResult, error) {
	if service.addMaterial == nil {
		return organizingapp.DraftResult{}, errors.New("unexpected AddMaterial call")
	}
	return service.addMaterial(ctx, command)
}
func (service *fakeService) RemoveMaterial(ctx context.Context, command organizingapp.RemoveMaterialCommand) (organizingapp.DraftResult, error) {
	if service.removeMaterial == nil {
		return organizingapp.DraftResult{}, errors.New("unexpected RemoveMaterial call")
	}
	return service.removeMaterial(ctx, command)
}
func (service *fakeService) SetMaterialSelection(ctx context.Context, command organizingapp.SetMaterialSelectionCommand) (organizingapp.DraftResult, error) {
	if service.setSelection == nil {
		return organizingapp.DraftResult{}, errors.New("unexpected SetMaterialSelection call")
	}
	return service.setSelection(ctx, command)
}
func (service *fakeService) Suggest(ctx context.Context, command organizingapp.SuggestCommand) (organizingapp.DraftResult, error) {
	if service.suggest == nil {
		return organizingapp.DraftResult{}, errors.New("unexpected Suggest call")
	}
	return service.suggest(ctx, command)
}
func (service *fakeService) SearchMaterials(ctx context.Context, query organizingapp.MaterialSearchQuery) (organizingapp.MaterialSearchPage, error) {
	if service.searchMaterials == nil {
		return organizingapp.MaterialSearchPage{}, errors.New("unexpected SearchMaterials call")
	}
	return service.searchMaterials(ctx, query)
}
func (service *fakeService) ConfirmDraft(ctx context.Context, command organizingapp.ConfirmCommand) (organizingapp.ConfirmResult, error) {
	if service.confirmDraft == nil {
		return organizingapp.ConfirmResult{}, errors.New("unexpected ConfirmDraft call")
	}
	return service.confirmDraft(ctx, command)
}
func (service *fakeService) GetSnapshot(ctx context.Context, workspaceID, snapshotID foundation.ID) (domain.Snapshot, error) {
	if service.getSnapshot == nil {
		return domain.Snapshot{}, errors.New("unexpected GetSnapshot call")
	}
	return service.getSnapshot(ctx, workspaceID, snapshotID)
}
func (service *fakeService) CreateTemplate(ctx context.Context, command organizingapp.CreateTemplateCommand) (organizingapp.TemplateResult, error) {
	if service.createTemplate == nil {
		return organizingapp.TemplateResult{}, errors.New("unexpected CreateTemplate call")
	}
	return service.createTemplate(ctx, command)
}
func (service *fakeService) CloneTemplate(ctx context.Context, command organizingapp.CloneTemplateCommand) (organizingapp.TemplateResult, error) {
	if service.cloneTemplate == nil {
		return organizingapp.TemplateResult{}, errors.New("unexpected CloneTemplate call")
	}
	return service.cloneTemplate(ctx, command)
}
func (service *fakeService) ReviseTemplate(ctx context.Context, command organizingapp.ReviseTemplateCommand) (organizingapp.TemplateResult, error) {
	if service.reviseTemplate == nil {
		return organizingapp.TemplateResult{}, errors.New("unexpected ReviseTemplate call")
	}
	return service.reviseTemplate(ctx, command)
}
func (service *fakeService) GetTemplate(ctx context.Context, workspaceID, templateID foundation.ID) (organizingapp.TemplateDetail, error) {
	if service.getTemplate == nil {
		return organizingapp.TemplateDetail{}, errors.New("unexpected GetTemplate call")
	}
	return service.getTemplate(ctx, workspaceID, templateID)
}
func (service *fakeService) ListTemplates(ctx context.Context, query organizingapp.TemplateListQuery) (organizingapp.TemplatePage, error) {
	if service.listTemplates == nil {
		return organizingapp.TemplatePage{}, errors.New("unexpected ListTemplates call")
	}
	return service.listTemplates(ctx, query)
}

type fakeRunReader struct {
	get func(context.Context, foundation.ID, foundation.ID) (organizingapp.RunProjection, error)
}

func (reader fakeRunReader) GetRunProjection(ctx context.Context, workspaceID, snapshotID foundation.ID) (organizingapp.RunProjection, error) {
	return reader.get(ctx, workspaceID, snapshotID)
}

type fakeWorkflowReader struct {
	get func(context.Context, foundation.ID) (workflowdomain.Run, error)
}

func (reader fakeWorkflowReader) Get(ctx context.Context, runID foundation.ID) (workflowdomain.Run, error) {
	return reader.get(ctx, runID)
}

func TestHandlerSearchMaterialsReturnsOnlyOwnerIdentity(t *testing.T) {
	workspaceID, claimID := testID(100), testID(101)
	service := &fakeService{searchMaterials: func(_ context.Context, query organizingapp.MaterialSearchQuery) (organizingapp.MaterialSearchPage, error) {
		if query.WorkspaceID != workspaceID || query.Query != "幂等恢复" || query.Kind != domain.MaterialClaim || query.Limit != 12 {
			t.Fatalf("material search query = %#v", query)
		}
		return organizingapp.MaterialSearchPage{WorkspaceID: workspaceID, Query: query.Query, Kind: query.Kind, Items: []organizingapp.MaterialSearchHit{{
			Selector: organizingapp.MaterialSelector{Kind: domain.MaterialClaim, ClaimID: claimID}, Title: "幂等恢复依赖稳定证据", Availability: domain.MaterialAvailable,
		}}}, nil
	}}
	router := testRouter(NewHandler(service, nil, nil, time.Second))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/"+string(workspaceID)+"/organizing/materials/search?q=%E5%B9%82%E7%AD%89%E6%81%A2%E5%A4%8D&kind=CLAIM&limit=12", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("material search response = %d %s", response.Code, response.Body.String())
	}
	for _, required := range []string{`"workspace_id":"` + string(workspaceID) + `"`, `"kind":"CLAIM"`, `"claim_id":"` + string(claimID) + `"`, `"title":"幂等恢复依赖稳定证据"`} {
		if !strings.Contains(response.Body.String(), required) {
			t.Fatalf("material search response missing %s: %s", required, response.Body.String())
		}
	}
	for _, forbidden := range []string{`"evidence"`, `"content_hash"`, `"version"`} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("material search leaked frozen field %s: %s", forbidden, response.Body.String())
		}
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/"+string(workspaceID)+"/organizing/materials/search?q=a%09b&kind=CLAIM", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("control-character material search response = %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerAddMaterialAcceptsOnlyIdentityFieldsAndUsesReplayStatus(t *testing.T) {
	workspaceID := testID(1)
	draftID := testID(2)
	sourceVersionID := testID(3)
	draft := testEditingDraft(t, workspaceID, draftID, true)
	draft.Version = 2
	draft.UpdatedAt = testTime().Add(time.Second)
	if err := draft.Validate(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := &fakeService{addMaterial: func(_ context.Context, command organizingapp.AddMaterialCommand) (organizingapp.DraftResult, error) {
		calls++
		if command.WorkspaceID != workspaceID || command.DraftID != draftID || command.ExpectedVersion != 1 ||
			command.Selector.Kind != domain.MaterialSourceVersion || command.Selector.SourceVersionID != sourceVersionID ||
			command.IdempotencyKey != "add-source" {
			t.Fatalf("AddMaterial command = %#v", command)
		}
		return organizingapp.DraftResult{Draft: draft, Replayed: calls == 2}, nil
	}}
	router := testRouter(NewHandler(service, nil, nil, time.Second))

	forged := newJSONRequest(http.MethodPost, "/api/v1/workspaces/"+string(workspaceID)+"/organizing/drafts/"+string(draftID)+"/materials",
		`{"expected_version":1,"kind":"SOURCE_VERSION","source_version_id":"`+string(sourceVersionID)+`","availability":"AVAILABLE"}`, "add-source")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, forged)
	if response.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("forged material response = %d %s calls=%d", response.Code, response.Body.String(), calls)
	}

	validBody := `{"expected_version":1,"kind":"SOURCE_VERSION","source_version_id":"` + string(sourceVersionID) + `"}`
	for attempt, expectedStatus := range []int{http.StatusCreated, http.StatusOK} {
		response = httptest.NewRecorder()
		router.ServeHTTP(response, newJSONRequest(http.MethodPost,
			"/api/v1/workspaces/"+string(workspaceID)+"/organizing/drafts/"+string(draftID)+"/materials", validBody, "add-source"))
		if response.Code != expectedStatus {
			t.Fatalf("attempt %d response = %d %s", attempt, response.Code, response.Body.String())
		}
	}
	if calls != 2 {
		t.Fatalf("AddMaterial calls = %d", calls)
	}
}

func TestHandlerUpdateDraftBindsExactTemplateRevision(t *testing.T) {
	workspaceID := testID(4)
	draftID := testID(5)
	templateRevisionID := testID(6)
	current := testEditingDraft(t, workspaceID, draftID, false)
	updated, err := domain.UpdateDraft(current, current.Version, "updated intent", templateRevisionID, testTime().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := &fakeService{updateDraft: func(_ context.Context, command organizingapp.UpdateDraftCommand) (organizingapp.DraftResult, error) {
		calls++
		if command.WorkspaceID != workspaceID || command.DraftID != draftID || command.ExpectedVersion != 1 ||
			command.Intent != "updated intent" || command.TemplateRevisionID != templateRevisionID ||
			command.IdempotencyKey != "update-draft" {
			t.Fatalf("UpdateDraft command = %#v", command)
		}
		return organizingapp.DraftResult{Draft: updated}, nil
	}}
	router := testRouter(NewHandler(service, nil, nil, time.Second))
	path := "/api/v1/workspaces/" + string(workspaceID) + "/organizing/drafts/" + string(draftID)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, newJSONRequest(http.MethodPut, path,
		`{"expected_version":1,"intent":"updated intent"}`, "update-draft"))
	if response.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("missing Template Revision response = %d %s calls=%d", response.Code, response.Body.String(), calls)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, newJSONRequest(http.MethodPut, path,
		`{"expected_version":1,"intent":"updated intent","template_revision_id":"`+string(templateRevisionID)+`"}`, "update-draft"))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"template_revision_id":"`+string(templateRevisionID)+`"`) {
		t.Fatalf("updated Draft response = %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerSetsMaterialSelectionExplicitly(t *testing.T) {
	workspaceID := testID(7)
	draftID := testID(8)
	materialID := testID(9)
	draft := testEditingDraft(t, workspaceID, draftID, true)
	draft.Materials[0].ID = materialID
	draft.Materials[0].Selected = true
	draft.Version = 2
	draft.UpdatedAt = testTime().Add(time.Second)
	if err := draft.Validate(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := &fakeService{setSelection: func(_ context.Context, command organizingapp.SetMaterialSelectionCommand) (organizingapp.DraftResult, error) {
		calls++
		if command.WorkspaceID != workspaceID || command.DraftID != draftID || command.MaterialID != materialID ||
			command.ExpectedVersion != 1 || !command.Selected || command.IdempotencyKey != "select-material" {
			t.Fatalf("SetMaterialSelection command=%#v", command)
		}
		return organizingapp.DraftResult{Draft: draft}, nil
	}}
	router := testRouter(NewHandler(service, nil, nil, time.Second))
	path := "/api/v1/workspaces/" + string(workspaceID) + "/organizing/drafts/" + string(draftID) + "/materials/" + string(materialID)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, newJSONRequest(http.MethodPatch, path, `{"expected_version":1}`, "select-material"))
	if response.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("missing selected response=%d %s calls=%d", response.Code, response.Body.String(), calls)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, newJSONRequest(http.MethodPatch, path, `{"expected_version":1,"selected":true}`, "select-material"))
	if response.Code != http.StatusOK || calls != 1 || !strings.Contains(response.Body.String(), `"selected":true`) {
		t.Fatalf("selection response=%d %s calls=%d", response.Code, response.Body.String(), calls)
	}
}

func TestHandlerConfirmReturnsPendingSnapshotWithoutInventingRun(t *testing.T) {
	workspaceID := testID(10)
	draftID := testID(11)
	templateID := testID(12)
	templateRevisionID := testID(13)
	snapshotID := testID(14)
	outboxID := testID(15)
	draft := testEditingDraft(t, workspaceID, draftID, true)
	draft.TemplateRevisionID = templateRevisionID
	if err := draft.Validate(); err != nil {
		t.Fatal(err)
	}
	confirmed, snapshot, err := domain.Confirm(draft, draft.Version, snapshotID, templateID, templateRevisionID, testHash,
		[]domain.MaterialRef{draft.Materials[0].Ref}, testTime().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	confirmCalls := 0
	service := &fakeService{confirmDraft: func(_ context.Context, command organizingapp.ConfirmCommand) (organizingapp.ConfirmResult, error) {
		confirmCalls++
		if command.TemplateRevisionID != templateRevisionID || command.ExpectedVersion != 1 {
			t.Fatalf("Confirm command = %#v", command)
		}
		return organizingapp.ConfirmResult{Draft: confirmed, Snapshot: snapshot, OutboxID: outboxID, Replayed: confirmCalls == 2}, nil
	}}
	router := testRouter(NewHandler(service, nil, nil, time.Second))
	body := `{"expected_version":1,"template_revision_id":"` + string(templateRevisionID) + `"}`
	response := httptest.NewRecorder()
	router.ServeHTTP(response, newJSONRequest(http.MethodPost,
		"/api/v1/workspaces/"+string(workspaceID)+"/organizing/drafts/"+string(draftID)+"/confirm", body, "confirm-draft"))
	if response.Code != http.StatusAccepted {
		t.Fatalf("confirm response = %d %s", response.Code, response.Body.String())
	}
	text := response.Body.String()
	for _, required := range []string{`"dispatch_status":"PENDING"`, `"snapshot"`, `"content_hash":"` + testHash + `"`, `"excerpt_hash":"` + testHash + `"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("confirm response missing %s: %s", required, text)
		}
	}
	if strings.Contains(text, "workflow_run_id") || strings.Contains(text, string(outboxID)) {
		t.Fatalf("confirm response leaked a non-existent Run or internal outbox: %s", text)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, newJSONRequest(http.MethodPost,
		"/api/v1/workspaces/"+string(workspaceID)+"/organizing/drafts/"+string(draftID)+"/confirm", body, "confirm-draft"))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"replayed":true`) {
		t.Fatalf("replayed confirm response = %d %s", response.Code, response.Body.String())
	}

	confirmed.TemplateRevisionID = testID(16)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, newJSONRequest(http.MethodPost,
		"/api/v1/workspaces/"+string(workspaceID)+"/organizing/drafts/"+string(draftID)+"/confirm", body, "confirm-draft"))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), errorCodeResultInvalid) {
		t.Fatalf("cross-bound confirm response = %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerRunProjectionFailsClosedAndRepresentsPendingExplicitly(t *testing.T) {
	workspaceID := testID(20)
	snapshotID := testID(21)
	service := &fakeService{}
	route := "/api/v1/workspaces/" + string(workspaceID) + "/organizing/runs/" + string(snapshotID)

	response := httptest.NewRecorder()
	testRouter(NewHandler(service, nil, nil, time.Second)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), errorCodeRunUnavailable) {
		t.Fatalf("missing run projection response = %d %s", response.Code, response.Body.String())
	}

	runs := fakeRunReader{get: func(context.Context, foundation.ID, foundation.ID) (organizingapp.RunProjection, error) {
		return organizingapp.RunProjection{}, foundation.NewError(foundation.ErrorNotFound, organizingapp.ErrorCodeNotFound, false, errors.New("missing outbox"))
	}}
	response = httptest.NewRecorder()
	testRouter(NewHandler(service, runs, nil, time.Second)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), `"dispatch_status":"PENDING"`) {
		t.Fatalf("missing outbox was inferred as pending = %d %s", response.Code, response.Body.String())
	}

	runs = fakeRunReader{get: func(_ context.Context, gotWorkspaceID, gotSnapshotID foundation.ID) (organizingapp.RunProjection, error) {
		if gotWorkspaceID != workspaceID || gotSnapshotID != snapshotID {
			t.Fatalf("run query = %s/%s", gotWorkspaceID, gotSnapshotID)
		}
		return organizingapp.RunProjection{WorkspaceID: workspaceID, SnapshotID: snapshotID,
			Status: organizingapp.StartPending, UpdatedAt: testTime()}, nil
	}}
	response = httptest.NewRecorder()
	testRouter(NewHandler(service, runs, nil, time.Second)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("pending run response = %d %s", response.Code, response.Body.String())
	}
	for _, required := range []string{`"dispatch_status":"PENDING"`, `"workflow_status":null`, `"retryable":true`, `"attempt_count":0`, `"binding":null`, `"result":null`} {
		if !strings.Contains(response.Body.String(), required) {
			t.Fatalf("pending run response missing %s: %s", required, response.Body.String())
		}
	}
	runs = fakeRunReader{get: func(context.Context, foundation.ID, foundation.ID) (organizingapp.RunProjection, error) {
		return organizingapp.RunProjection{WorkspaceID: workspaceID, SnapshotID: snapshotID,
			Status: organizingapp.StartPoisoned, AttemptCount: 3, LastErrorCode: "WORKFLOW_START_FAILED", UpdatedAt: testTime()}, nil
	}}
	response = httptest.NewRecorder()
	testRouter(NewHandler(service, runs, nil, time.Second)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"dispatch_status":"POISONED"`) ||
		!strings.Contains(response.Body.String(), `"workflow_status":null`) || !strings.Contains(response.Body.String(), `"retryable":false`) {
		t.Fatalf("poisoned run response = %d %s", response.Code, response.Body.String())
	}
	binding := domain.RunBinding{
		ID: testID(22), WorkspaceID: workspaceID, SnapshotID: snapshotID, WorkflowRunID: testID(23),
		DefinitionKey: "organizing.topic-article", DefinitionVersion: 1, CreatedAt: testTime(),
	}
	runs = fakeRunReader{get: func(context.Context, foundation.ID, foundation.ID) (organizingapp.RunProjection, error) {
		return organizingapp.RunProjection{WorkspaceID: workspaceID, SnapshotID: snapshotID,
			Status: organizingapp.StartStarted, AttemptCount: 2, LastErrorCode: "TEMPORARY_FAILURE",
			Binding: &binding, UpdatedAt: testTime()}, nil
	}}
	response = httptest.NewRecorder()
	testRouter(NewHandler(service, runs, nil, time.Second)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), errorCodeWorkflowUnavailable) {
		t.Fatalf("missing Workflow projection response = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	testRouter(NewHandler(service, runs, fakeWorkflowReader{get: func(_ context.Context, runID foundation.ID) (workflowdomain.Run, error) {
		if runID != binding.WorkflowRunID {
			t.Fatalf("Workflow query = %s", runID)
		}
		return workflowdomain.Run{ID: binding.WorkflowRunID, WorkspaceID: workspaceID, Status: workflowdomain.RunStatusRunning}, nil
	}}, time.Second)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("started run response = %d %s", response.Code, response.Body.String())
	}
	for _, required := range []string{`"dispatch_status":"STARTED"`, `"retryable":false`, `"attempt_count":2`,
		`"last_error_code":"TEMPORARY_FAILURE"`, `"workflow_status":"running"`, `"workflow_run_id":"` + string(binding.WorkflowRunID) + `"`} {
		if !strings.Contains(response.Body.String(), required) {
			t.Fatalf("started run response missing %s: %s", required, response.Body.String())
		}
	}
	result := domain.RunResult{
		ID: testID(25), WorkspaceID: workspaceID, RunBindingID: binding.ID, SnapshotID: snapshotID,
		WorkflowRunID: binding.WorkflowRunID, NodeRunID: testID(26), Kind: domain.ResultArtifact,
		ResultRef: testID(27), ResultHash: testHash, CreatedAt: testTime(),
	}
	tornReadCalls := 0
	tornReads := fakeRunReader{get: func(context.Context, foundation.ID, foundation.ID) (organizingapp.RunProjection, error) {
		tornReadCalls++
		view := organizingapp.RunProjection{WorkspaceID: workspaceID, SnapshotID: snapshotID,
			Status: organizingapp.StartStarted, AttemptCount: 2, Binding: &binding, UpdatedAt: testTime()}
		if tornReadCalls == 2 {
			view.Result = &result
		}
		return view, nil
	}}
	response = httptest.NewRecorder()
	testRouter(NewHandler(service, tornReads, fakeWorkflowReader{get: func(context.Context, foundation.ID) (workflowdomain.Run, error) {
		return workflowdomain.Run{ID: binding.WorkflowRunID, WorkspaceID: workspaceID, Status: workflowdomain.RunStatusSucceeded}, nil
	}}, time.Second)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
	if response.Code != http.StatusOK || tornReadCalls != 2 || !strings.Contains(response.Body.String(), `"workflow_status":"succeeded"`) ||
		!strings.Contains(response.Body.String(), `"result":{"id":"`+string(result.ID)+`"`) {
		t.Fatalf("refreshed succeeded Workflow response = %d calls=%d %s", response.Code, tornReadCalls, response.Body.String())
	}

	mismatchedBinding := binding
	mismatchedBinding.WorkflowRunID = testID(28)
	tornReadCalls = 0
	mismatchedReads := fakeRunReader{get: func(context.Context, foundation.ID, foundation.ID) (organizingapp.RunProjection, error) {
		tornReadCalls++
		currentBinding := &binding
		currentResult := (*domain.RunResult)(nil)
		if tornReadCalls == 2 {
			currentBinding = &mismatchedBinding
			mismatchedResult := result
			mismatchedResult.WorkflowRunID = mismatchedBinding.WorkflowRunID
			currentResult = &mismatchedResult
		}
		return organizingapp.RunProjection{WorkspaceID: workspaceID, SnapshotID: snapshotID,
			Status: organizingapp.StartStarted, AttemptCount: 2, Binding: currentBinding, Result: currentResult, UpdatedAt: testTime()}, nil
	}}
	response = httptest.NewRecorder()
	testRouter(NewHandler(service, mismatchedReads, fakeWorkflowReader{get: func(context.Context, foundation.ID) (workflowdomain.Run, error) {
		return workflowdomain.Run{ID: binding.WorkflowRunID, WorkspaceID: workspaceID, Status: workflowdomain.RunStatusSucceeded}, nil
	}}, time.Second)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
	if response.Code != http.StatusInternalServerError || tornReadCalls != 2 || !strings.Contains(response.Body.String(), errorCodeResultInvalid) {
		t.Fatalf("mismatched refreshed binding response = %d calls=%d %s", response.Code, tornReadCalls, response.Body.String())
	}

	response = httptest.NewRecorder()
	testRouter(NewHandler(service, runs, fakeWorkflowReader{get: func(context.Context, foundation.ID) (workflowdomain.Run, error) {
		return workflowdomain.Run{ID: binding.WorkflowRunID, WorkspaceID: testID(24), Status: workflowdomain.RunStatusSucceeded}, nil
	}}, time.Second)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), errorCodeResultInvalid) {
		t.Fatalf("cross-workspace Workflow response = %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerStrictJSONAndQueryValidationPrecedeApplication(t *testing.T) {
	workspaceID := testID(30)
	calls := 0
	service := &fakeService{createDraft: func(context.Context, organizingapp.CreateDraftCommand) (organizingapp.DraftResult, error) {
		calls++
		return organizingapp.DraftResult{}, errors.New("must not be called")
	}}
	router := testRouter(NewHandler(service, nil, nil, time.Second))
	path := "/api/v1/workspaces/" + string(workspaceID) + "/organizing/drafts"
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "unknown", path: path, body: `{"intent":"topic","status":"CONFIRMED"}`},
		{name: "duplicate", path: path, body: `{"intent":"topic","intent":"other"}`},
		{name: "trailing", path: path, body: `{"intent":"topic"}{}`},
		{name: "query", path: path + "?unexpected=1", body: `{"intent":"topic"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, newJSONRequest(http.MethodPost, test.path, test.body, "create-draft"))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
	missingContentType := newJSONRequest(http.MethodPost, path, `{"intent":"topic"}`, "create-draft")
	missingContentType.Header.Del("Content-Type")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, missingContentType)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing Content-Type response = %d %s", response.Code, response.Body.String())
	}
	duplicateContentType := newJSONRequest(http.MethodPost, path, `{"intent":"topic"}`, "create-draft")
	duplicateContentType.Header.Add("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, duplicateContentType)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("duplicate Content-Type response = %d %s", response.Code, response.Body.String())
	}
	duplicateIdempotency := newJSONRequest(http.MethodPost, path, `{"intent":"topic"}`, "create-draft")
	duplicateIdempotency.Header.Add("Idempotency-Key", "other")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, duplicateIdempotency)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate Idempotency-Key response = %d %s", response.Code, response.Body.String())
	}
	if calls != 0 {
		t.Fatalf("CreateDraft calls = %d", calls)
	}
}

func TestHandlerTemplateListMapsBuiltInsAndRejectsCrossWorkspaceResult(t *testing.T) {
	workspaceID := testID(40)
	templates, revisions, err := domain.BuiltInTemplates(testTime())
	if err != nil {
		t.Fatal(err)
	}
	detail := organizingapp.TemplateDetail{Template: templates[0], Revision: revisions[0]}
	service := &fakeService{listTemplates: func(_ context.Context, query organizingapp.TemplateListQuery) (organizingapp.TemplatePage, error) {
		if query.WorkspaceID != workspaceID || query.Kind != domain.TemplateTopicArticle || query.Limit != 10 {
			t.Fatalf("template query = %#v", query)
		}
		return organizingapp.TemplatePage{Items: []organizingapp.TemplateDetail{detail}}, nil
	}}
	response := httptest.NewRecorder()
	testRouter(NewHandler(service, nil, nil, time.Second)).ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/"+string(workspaceID)+"/organizing/templates?kind=TOPIC_ARTICLE&limit=10", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("template response = %d %s", response.Code, response.Body.String())
	}
	for _, required := range []string{`"workspace_id":null`, `"built_in":true`, `"key":"topic-article"`, `"items":[`} {
		if !strings.Contains(response.Body.String(), required) {
			t.Fatalf("template response missing %s: %s", required, response.Body.String())
		}
	}
	otherWorkspaceID := testID(41)
	custom, customRevision, err := domain.NewCustomTemplate(testID(42), testID(43), otherWorkspaceID, revisions[0].Declaration, testTime())
	if err != nil {
		t.Fatal(err)
	}
	crossWorkspaceService := &fakeService{listTemplates: func(context.Context, organizingapp.TemplateListQuery) (organizingapp.TemplatePage, error) {
		return organizingapp.TemplatePage{Items: []organizingapp.TemplateDetail{{Template: custom, Revision: customRevision}}}, nil
	}}
	response = httptest.NewRecorder()
	testRouter(NewHandler(crossWorkspaceService, nil, nil, time.Second)).ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/"+string(workspaceID)+"/organizing/templates", nil))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), errorCodeResultInvalid) {
		t.Fatalf("cross-workspace Template response = %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerMapsStableOrganizingErrors(t *testing.T) {
	workspaceID := testID(50)
	draftID := testID(51)
	tests := []struct {
		name   string
		kind   foundation.ErrorKind
		code   string
		status int
	}{
		{name: "invalid", kind: foundation.ErrorInvalidInput, code: domain.ErrorCodeDraftInvalid, status: http.StatusBadRequest},
		{name: "not found", kind: foundation.ErrorNotFound, code: organizingapp.ErrorCodeNotFound, status: http.StatusNotFound},
		{name: "conflict", kind: foundation.ErrorVersionConflict, code: domain.ErrorCodeVersionConflict, status: http.StatusConflict},
		{name: "dependency", kind: foundation.ErrorDependencyUnavailable, code: organizingapp.ErrorCodeDependencyUnavailable, status: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeService{getDraft: func(context.Context, foundation.ID, foundation.ID) (domain.Draft, error) {
				return domain.Draft{}, foundation.NewError(test.kind, test.code, test.status == http.StatusServiceUnavailable, errors.New("private detail"))
			}}
			response := httptest.NewRecorder()
			testRouter(NewHandler(service, nil, nil, time.Second)).ServeHTTP(response, httptest.NewRequest(http.MethodGet,
				"/api/v1/workspaces/"+string(workspaceID)+"/organizing/drafts/"+string(draftID), nil))
			if response.Code != test.status || strings.Contains(response.Body.String(), "private detail") {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			var problem map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem["error_code"] != test.code {
				t.Fatalf("problem = %#v err=%v", problem, err)
			}
		})
	}
}

func testRouter(handler *Handler) http.Handler {
	router := chi.NewRouter()
	router.Route("/api/v1", handler.Routes)
	return router
}

func newJSONRequest(method, target, body, key string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	return request
}

func testEditingDraft(t *testing.T, workspaceID, draftID foundation.ID, withMaterial bool) domain.Draft {
	t.Helper()
	draft, err := domain.NewDraft(draftID, workspaceID, "organize this topic", testTime())
	if err != nil {
		t.Fatal(err)
	}
	if !withMaterial {
		return draft
	}
	reference := domain.MaterialRef{Kind: domain.MaterialSourceVersion, SourceVersionID: testID(90), ContentHash: testHash,
		Evidence: []domain.EvidenceRef{{IndexVersionID: testID(91), ChunkID: testID(92), SourceVersionID: testID(90),
			SourceSpanID: testID(93), ContentHash: testHash, ExcerptHash: testHash}}}
	material := domain.DraftMaterial{ID: testID(94), DraftID: draftID, Ref: reference, Title: "Source material",
		Reasons: []domain.SuggestionReasonCode{domain.ReasonUserAdded}, Origin: domain.MaterialOriginUser,
		Availability: domain.MaterialAvailable, Score: 1, Selected: true, Position: 0, CreatedAt: testTime()}
	draft.Materials = []domain.DraftMaterial{material}
	if err := draft.Validate(); err != nil {
		t.Fatal(err)
	}
	return draft
}

func testID(value int) foundation.ID {
	return foundation.ID(fmtID(value))
}

func fmtID(value int) string {
	return "00000000-0000-4000-8000-" + leftPad(value, 12)
}

func leftPad(value, width int) string {
	raw := strconv.Itoa(value)
	return strings.Repeat("0", width-len(raw)) + raw
}

func testTime() time.Time {
	return time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC)
}
