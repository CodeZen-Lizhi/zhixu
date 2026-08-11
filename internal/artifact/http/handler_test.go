package artifacthttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/gin-gonic/gin"
)

const (
	artifactHTTPWorkspaceID  = "10000000-0000-4000-8000-000000000001"
	artifactHTTPID           = "10000000-0000-4000-8000-000000000002"
	artifactHTTPRevisionID   = "10000000-0000-4000-8000-000000000003"
	artifactHTTPExportID     = "10000000-0000-4000-8000-000000000004"
	artifactHTTPIndexID      = "10000000-0000-4000-8000-000000000005"
	artifactHTTPChunkID      = "10000000-0000-4000-8000-000000000006"
	artifactHTTPSourceID     = "10000000-0000-4000-8000-000000000007"
	artifactHTTPSpanID       = "10000000-0000-4000-8000-000000000008"
	artifactHTTPGenerationID = "10000000-0000-4000-8000-000000000009"
	artifactHTTPWorkflowID   = "10000000-0000-4000-8000-000000000010"
	artifactHTTPNodeID       = "10000000-0000-4000-8000-000000000011"
)

func TestRoutesBindWorkspaceAndArtifactCommands(t *testing.T) {
	service := newArtifactHTTPFake()
	router := artifactHTTPRouter(service)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{"list", http.MethodGet, "/artifacts?workspace_id=" + artifactHTTPWorkspaceID, "", http.StatusOK},
		{"get", http.MethodGet, "/artifacts/" + artifactHTTPID + "?workspace_id=" + artifactHTTPWorkspaceID, "", http.StatusOK},
		{"plan", http.MethodPost, "/artifacts", `{"workspace_id":"` + artifactHTTPWorkspaceID + `","type":"study-guide","title":"Go","scope_definition":"approved material"}`, http.StatusCreated},
		{"submit outline", http.MethodPost, "/artifacts/" + artifactHTTPID + "/outline", `{"workspace_id":"` + artifactHTTPWorkspaceID + `","expected_version":1,"outline":[{"key":"channels","title":"Channels"}]}`, http.StatusOK},
		{"approve outline", http.MethodPost, "/artifacts/" + artifactHTTPID + "/outline/approve", versionedArtifactJSON(), http.StatusOK},
		{"start revision", http.MethodPost, "/artifacts/" + artifactHTTPID + "/revisions", versionedArtifactJSON(), http.StatusOK},
		{"record section", http.MethodPost, "/artifacts/" + artifactHTTPID + "/sections", sectionArtifactJSON(), http.StatusOK},
		{"generate section", http.MethodPost, "/artifacts/" + artifactHTTPID + "/sections/generate", sectionGenerationJSON(), http.StatusAccepted},
		{"list section generations", http.MethodGet, "/artifacts/" + artifactHTTPID + "/section-generations?workspace_id=" + artifactHTTPWorkspaceID, "", http.StatusOK},
		{"approve draft", http.MethodPost, "/artifacts/" + artifactHTTPID + "/draft/approve", versionedArtifactJSON(), http.StatusOK},
		{"export markdown", http.MethodPost, "/artifacts/" + artifactHTTPID + "/exports/markdown", versionedArtifactJSON(), http.StatusOK},
		{"get export", http.MethodGet, "/artifacts/" + artifactHTTPID + "/exports/" + artifactHTTPExportID + "?workspace_id=" + artifactHTTPWorkspaceID, "", http.StatusOK},
		{"publish", http.MethodPost, "/artifacts/" + artifactHTTPID + "/publish-proposals", versionedArtifactJSON(), http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Idempotency-Key", "artifact-http-test")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
		})
	}
	if service.plan.WorkspaceID != artifactHTTPIDValue(artifactHTTPWorkspaceID) || service.outline.ArtifactID != artifactHTTPIDValue(artifactHTTPID) || service.outline.ExpectedVersion != 1 {
		t.Fatalf("plan or outline binding lost: %+v %+v", service.plan, service.outline)
	}
	if service.section.Creator != domain.CreatorHuman || service.section.Metadata != nil || len(service.section.Section.Citations) != 1 || service.section.Section.Citations[0].SourceSpanID != artifactHTTPIDValue(artifactHTTPSpanID) {
		t.Fatalf("section identities were not passed to the server verifier: %+v", service.section)
	}
	if service.generation.WorkspaceID != artifactHTTPIDValue(artifactHTTPWorkspaceID) || service.generation.ArtifactID != artifactHTTPIDValue(artifactHTTPID) ||
		service.generation.ExpectedVersion != 1 || service.generation.SectionKey != "channels" || service.generation.IdempotencyKey != "artifact-http-test" {
		t.Fatalf("section generation binding lost: %+v", service.generation)
	}
	if service.queries != 4 || service.commands != 8 {
		t.Fatalf("calls queries=%d commands=%d", service.queries, service.commands)
	}
	if service.generations != 1 {
		t.Fatalf("generation calls=%d", service.generations)
	}

	request := httptest.NewRequest(http.MethodGet, "/artifacts/"+artifactHTTPID+"/exports/"+artifactHTTPExportID+"?workspace_id="+artifactHTTPWorkspaceID, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if strings.Contains(response.Body.String(), "output_path") || strings.Contains(response.Body.String(), "managed") {
		t.Fatalf("managed export path leaked: %s", response.Body.String())
	}
}

func TestSectionGenerationListSupportsRefreshWithoutGenerationCapability(t *testing.T) {
	service := newArtifactHTTPFake()
	router := gin.New()
	NewHandler(service, service, time.Second).Routes(router)
	request := httptest.NewRequest(http.MethodGet, "/artifacts/"+artifactHTTPID+"/section-generations?workspace_id="+artifactHTTPWorkspaceID, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		WorkspaceID string `json:"workspace_id"`
		ArtifactID  string `json:"artifact_id"`
		Items       []struct {
			GenerationID string `json:"generation_id"`
			SectionKey   string `json:"section_key"`
			Status       string `json:"status"`
			StatusURL    string `json:"status_url"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.WorkspaceID != artifactHTTPWorkspaceID ||
		body.ArtifactID != artifactHTTPID || len(body.Items) != 1 || body.Items[0].GenerationID != artifactHTTPGenerationID ||
		body.Items[0].SectionKey != "channels" || body.Items[0].Status != string(artifactapp.SectionGenerationPending) ||
		body.Items[0].StatusURL != "/api/v1/workflows/"+artifactHTTPWorkflowID {
		t.Fatalf("body=%+v err=%v raw=%s", body, err, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"replayed"`) {
		t.Fatalf("GET item leaked command-only replayed field: %s", response.Body.String())
	}
	for _, forbidden := range []string{"idempotency_key", "request_hash", "failure_class", "error_summary"} {
		if strings.Contains(response.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("GET item leaked %q: %s", forbidden, response.Body.String())
		}
	}
	if service.generations != 0 || service.generationQueries != 1 {
		t.Fatalf("starter calls=%d query calls=%d", service.generations, service.generationQueries)
	}

	service.generationList.Items = []artifactapp.SectionGeneration{}
	request = httptest.NewRequest(http.MethodGet, "/artifacts/"+artifactHTTPID+"/section-generations?workspace_id="+artifactHTTPWorkspaceID, nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatalf("empty status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSectionGenerationListQueryIsStrictAndNotFoundIsOpaque(t *testing.T) {
	tests := []string{
		"", "?workspace_id=", "?workspace_id=" + artifactHTTPWorkspaceID + "&extra=1",
		"?workspace_id=" + artifactHTTPWorkspaceID + "&workspace_id=" + artifactHTTPWorkspaceID,
		"?workspace_id=%20" + artifactHTTPWorkspaceID, "?workspace_id=%zz",
	}
	for _, suffix := range tests {
		t.Run(suffix, func(t *testing.T) {
			service := newArtifactHTTPFake()
			router := artifactHTTPRouter(service)
			request := httptest.NewRequest(http.MethodGet, "/artifacts/"+artifactHTTPID+"/section-generations"+suffix, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"error_code":"ARTIFACT_REQUEST_INVALID"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if service.generationQueries != 0 {
				t.Fatalf("invalid query reached service: %d", service.generationQueries)
			}
		})
	}

	service := newArtifactHTTPFake()
	service.err = foundation.NewError(foundation.ErrorNotFound, artifactapp.ErrorCodeNotFound, false, nil)
	router := artifactHTTPRouter(service)
	request := httptest.NewRequest(http.MethodGet, "/artifacts/"+artifactHTTPID+"/section-generations?workspace_id="+artifactHTTPWorkspaceID, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"error_code":"ARTIFACT_NOT_FOUND"`) ||
		strings.Contains(response.Body.String(), artifactHTTPWorkspaceID) || strings.Contains(response.Body.String(), artifactHTTPID) {
		t.Fatalf("opaque not found status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestStrictMutationValidationRejectsBeforeService(t *testing.T) {
	service := newArtifactHTTPFake()
	router := artifactHTTPRouter(service)
	tests := []struct {
		name string
		path string
		body string
		key  string
		code string
	}{
		{"missing key", "/artifacts", `{"workspace_id":"` + artifactHTTPWorkspaceID + `","type":"study-guide","title":"Go","scope_definition":"approved"}`, "", artifactapp.ErrorCodeRequestInvalid},
		{"unknown plan field", "/artifacts", `{"workspace_id":"` + artifactHTTPWorkspaceID + `","type":"study-guide","title":"Go","scope_definition":"approved","extra":true}`, "artifact-1", "INVALID_JSON"},
		{"duplicate plan field", "/artifacts", `{"workspace_id":"` + artifactHTTPWorkspaceID + `","workspace_id":"` + artifactHTTPWorkspaceID + `","type":"study-guide","title":"Go","scope_definition":"approved"}`, "artifact-1", "INVALID_JSON"},
		{"missing cas", "/artifacts/" + artifactHTTPID + "/outline/approve", `{"workspace_id":"` + artifactHTTPWorkspaceID + `"}`, "artifact-1", artifactapp.ErrorCodeRequestInvalid},
		{"start revision unknown field", "/artifacts/" + artifactHTTPID + "/revisions", strings.TrimSuffix(versionedArtifactJSON(), "}") + `,"future":true}`, "artifact-1", "INVALID_JSON"},
		{"forged citation", "/artifacts/" + artifactHTTPID + "/sections", strings.TrimSuffix(sectionArtifactJSON(), "}") + `,"verified":true}`, "artifact-1", "INVALID_JSON"},
		{"implicit citations", "/artifacts/" + artifactHTTPID + "/sections", `{"workspace_id":"` + artifactHTTPWorkspaceID + `","expected_version":1,"section":{"key":"channels","title":"Channels","content":"","coverage":{"section_key":"channels","status":"GAP","gaps":[{"code":"MISSING","description":"missing"}]}}}`, "artifact-1", artifactapp.ErrorCodeRequestInvalid},
		{"manual creator", "/artifacts/" + artifactHTTPID + "/sections", strings.TrimSuffix(sectionArtifactJSON(), "}") + `,"creator":"HUMAN"}`, "artifact-1", "INVALID_JSON"},
		{"manual generation metadata", "/artifacts/" + artifactHTTPID + "/sections", strings.TrimSuffix(sectionArtifactJSON(), "}") + `,"metadata":{"prompt_version":"p1","model_version":"m1","workflow_definition_version":"w1","schema_version":"s1"}}`, "artifact-1", "INVALID_JSON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			if test.key != "" {
				request.Header.Set("Idempotency-Key", test.key)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var problem struct {
				ErrorCode string `json:"error_code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.ErrorCode != test.code {
				t.Fatalf("problem=%+v err=%v want=%s", problem, err, test.code)
			}
		})
	}
	if service.commands != 0 {
		t.Fatalf("invalid mutation reached command service: %d", service.commands)
	}
}

func TestSectionGenerationReturnsAuthoritativeBindingForExactReplay(t *testing.T) {
	service := newArtifactHTTPFake()
	service.generationReplayed = true
	router := artifactHTTPRouter(service)
	request := httptest.NewRequest(http.MethodPost, "/artifacts/"+artifactHTTPID+"/sections/generate", strings.NewReader(sectionGenerationJSON()))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "artifact-generation-replay")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("replay status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		GenerationID          string `json:"generation_id"`
		WorkspaceID           string `json:"workspace_id"`
		ArtifactID            string `json:"artifact_id"`
		SourceRevisionID      string `json:"source_revision_id"`
		SourceRevisionNo      int64  `json:"source_revision_no"`
		SourceArtifactVersion int64  `json:"source_artifact_version"`
		SectionKey            string `json:"section_key"`
		WorkflowRunID         string `json:"workflow_run_id"`
		NodeRunID             string `json:"node_run_id"`
		Status                string `json:"status"`
		Version               int64  `json:"version"`
		CreatedAt             string `json:"created_at"`
		UpdatedAt             string `json:"updated_at"`
		Replayed              bool   `json:"replayed"`
		StatusURL             string `json:"status_url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.GenerationID != artifactHTTPGenerationID || body.WorkspaceID != artifactHTTPWorkspaceID || body.ArtifactID != artifactHTTPID ||
		body.SourceRevisionID != artifactHTTPRevisionID || body.SourceRevisionNo != 1 || body.SourceArtifactVersion != 1 ||
		body.SectionKey != "channels" || body.WorkflowRunID != artifactHTTPWorkflowID || body.NodeRunID != artifactHTTPNodeID ||
		body.Status != string(artifactapp.SectionGenerationPending) || body.Version != 1 || body.CreatedAt != "2026-07-26T08:00:00Z" ||
		body.UpdatedAt != "2026-07-26T08:00:00Z" || !body.Replayed || body.StatusURL != "/api/v1/workflows/"+artifactHTTPWorkflowID {
		t.Fatalf("unexpected generation response: %+v", body)
	}
	for _, forbidden := range []string{"idempotency_key", "request_hash", "model_run_id", "content_hash"} {
		if strings.Contains(response.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("internal generation field %q leaked: %s", forbidden, response.Body.String())
		}
	}
}

func TestSectionGenerationRejectsCallerControlledGenerationFacts(t *testing.T) {
	for _, field := range []string{
		`"content":"forged"`, `"citations":[]`, `"model":"forged"`, `"prompt_version":"forged"`,
		`"schema_version":"forged"`, `"creator":"AGENT"`, `"metadata":{}`,
	} {
		t.Run(field, func(t *testing.T) {
			service := newArtifactHTTPFake()
			router := artifactHTTPRouter(service)
			body := strings.TrimSuffix(sectionGenerationJSON(), "}") + "," + field + "}"
			request := httptest.NewRequest(http.MethodPost, "/artifacts/"+artifactHTTPID+"/sections/generate", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "artifact-generation-invalid")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"error_code":"INVALID_JSON"`) {
				t.Fatalf("field=%s status=%d body=%s", field, response.Code, response.Body.String())
			}
			if service.generations != 0 {
				t.Fatalf("invalid generation reached service: %d", service.generations)
			}
		})
	}
}

func TestSectionGenerationCapabilityUnavailableIsStableAndNonRetryable(t *testing.T) {
	tests := []struct {
		name             string
		starterAvailable bool
		starterError     error
		wantCalls        int
	}{
		{name: "chat disabled", wantCalls: 0},
		{name: "generation dependency disabled", starterAvailable: true, starterError: foundation.NewError(foundation.ErrorDependencyUnavailable, artifactapp.ErrorCodeDependencyUnavailable, true, nil), wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newArtifactHTTPFake()
			service.generationErr = test.starterError
			router := gin.New()
			if test.starterAvailable {
				NewHandler(service, service, time.Second, service).Routes(router)
			} else {
				NewHandler(service, service, time.Second).Routes(router)
			}
			request := httptest.NewRequest(http.MethodPost, "/artifacts/"+artifactHTTPID+"/sections/generate", strings.NewReader(sectionGenerationJSON()))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "artifact-generation-disabled")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable ||
				!strings.Contains(response.Body.String(), `"error_code":"ARTIFACT_GENERATION_CAPABILITY_UNAVAILABLE"`) ||
				!strings.Contains(response.Body.String(), `"retryable":false`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if service.generations != test.wantCalls {
				t.Fatalf("generation calls=%d want=%d", service.generations, test.wantCalls)
			}
		})
	}
}

func TestSectionGenerationRejectsAStarterResponseOutsideTheRequestBinding(t *testing.T) {
	service := newArtifactHTTPFake()
	service.generationResult.WorkspaceID = artifactHTTPIDValue("10000000-0000-4000-8000-000000000012")
	// Preserve the fake's mismatched value instead of projecting command-owned fields.
	service.preserveGenerationBinding = true
	router := artifactHTTPRouter(service)
	request := httptest.NewRequest(http.MethodPost, "/artifacts/"+artifactHTTPID+"/sections/generate", strings.NewReader(sectionGenerationJSON()))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "artifact-generation-inconsistent")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"error_code":"ARTIFACT_HTTP_RESULT_INCONSISTENT"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandlerFailsClosedAndMapsApplicationConflict(t *testing.T) {
	missingRouter := gin.New()
	NewHandler(nil, nil, time.Second).Routes(missingRouter)
	request := httptest.NewRequest(http.MethodGet, "/artifacts?workspace_id="+artifactHTTPWorkspaceID, nil)
	response := httptest.NewRecorder()
	missingRouter.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing dependencies status=%d body=%s", response.Code, response.Body.String())
	}

	service := newArtifactHTTPFake()
	service.err = foundation.NewError(foundation.ErrorVersionConflict, artifactapp.ErrorCodeVersionConflict, false, nil)
	router := artifactHTTPRouter(service)
	request = httptest.NewRequest(http.MethodPost, "/artifacts/"+artifactHTTPID+"/outline/approve", strings.NewReader(versionedArtifactJSON()))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "artifact-conflict")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPlanExactReplayReturnsOK(t *testing.T) {
	service := newArtifactHTTPFake()
	service.replayed = true
	router := artifactHTTPRouter(service)
	request := httptest.NewRequest(http.MethodPost, "/artifacts", strings.NewReader(`{"workspace_id":"`+artifactHTTPWorkspaceID+`","type":"study-guide","title":"Go","scope_definition":"approved material"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "artifact-replay")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Replayed bool `json:"replayed"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !body.Replayed {
		t.Fatalf("replay response=%+v err=%v", body, err)
	}
}

func artifactHTTPRouter(service *artifactHTTPFake) http.Handler {
	router := gin.New()
	NewHandler(service, service, time.Second, service).Routes(router)
	return router
}

func versionedArtifactJSON() string {
	return `{"workspace_id":"` + artifactHTTPWorkspaceID + `","expected_version":1}`
}
func sectionArtifactJSON() string {
	return `{"workspace_id":"` + artifactHTTPWorkspaceID + `","expected_version":1,"section":{"key":"channels","title":"Channels","content":"","citations":[{"index_version_id":"` + artifactHTTPIndexID + `","chunk_id":"` + artifactHTTPChunkID + `","source_version_id":"` + artifactHTTPSourceID + `","source_span_id":"` + artifactHTTPSpanID + `"}],"coverage":{"section_key":"channels","status":"GAP","gaps":[{"code":"MISSING","description":"missing"}]}}}`
}
func sectionGenerationJSON() string {
	return `{"workspace_id":"` + artifactHTTPWorkspaceID + `","expected_version":1,"section_key":"channels"}`
}

type artifactHTTPFake struct {
	state                     artifactapp.State
	export                    artifactapp.ExportRecord
	err                       error
	plan                      artifactapp.PlanCommand
	outline                   artifactapp.SubmitOutlinePersistentCommand
	section                   artifactapp.RecordSectionPersistentCommand
	generation                artifactapp.StartSectionGenerationCommand
	commands                  int
	queries                   int
	generations               int
	replayed                  bool
	generationReplayed        bool
	generationErr             error
	generationResult          artifactapp.SectionGeneration
	generationList            artifactapp.SectionGenerationList
	generationQueries         int
	preserveGenerationBinding bool
}

func newArtifactHTTPFake() *artifactHTTPFake {
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	artifact, revision, err := domain.PlanArtifact(domain.PlanInput{ArtifactID: artifactHTTPIDValue(artifactHTTPID), InitialRevisionID: artifactHTTPIDValue(artifactHTTPRevisionID), WorkspaceID: artifactHTTPIDValue(artifactHTTPWorkspaceID), Type: "study-guide", Title: "Go", ScopeDefinition: "approved material", CreatedAt: now})
	if err != nil {
		panic(err)
	}
	state := artifactapp.State{Artifact: artifact, Revision: revision}
	fake := &artifactHTTPFake{
		state:  state,
		export: artifactapp.ExportRecord{ID: artifactHTTPIDValue(artifactHTTPExportID), WorkspaceID: artifact.WorkspaceID, ArtifactID: artifact.ID, RevisionID: revision.ID, ArtifactVersion: artifact.Version, RevisionNo: revision.RevisionNo, RevisionHash: revision.ContentHash, OutputPath: "managed", OutputHash: strings.Repeat("a", 64), OutputSize: 4, ExportedAt: now},
		generationResult: artifactapp.SectionGeneration{
			ID: artifactHTTPIDValue(artifactHTTPGenerationID), WorkspaceID: artifact.WorkspaceID, ArtifactID: artifact.ID,
			SourceRevisionID: revision.ID, SourceRevisionNo: revision.RevisionNo, SourceArtifactVersion: artifact.Version,
			SectionKey: "channels", IdempotencyKey: "artifact-generation", RequestHash: strings.Repeat("c", 64),
			WorkflowRunID: artifactHTTPIDValue(artifactHTTPWorkflowID), NodeRunID: artifactHTTPIDValue(artifactHTTPNodeID),
			Status: artifactapp.SectionGenerationPending, Version: 1, CreatedAt: now, UpdatedAt: now,
		},
	}
	fake.generationList = artifactapp.SectionGenerationList{
		WorkspaceID: artifact.WorkspaceID, ArtifactID: artifact.ID,
		Items: []artifactapp.SectionGeneration{fake.generationResult},
	}
	return fake
}
func (fake *artifactHTTPFake) command(commandType artifactapp.CommandType) (artifactapp.CommandResult, error) {
	fake.commands++
	if fake.err != nil {
		return artifactapp.CommandResult{}, fake.err
	}
	return artifactapp.CommandResult{State: fake.state, CommandVersion: fake.state.Artifact.Version, RequestHash: strings.Repeat("b", 64), CommandType: commandType, Replayed: fake.replayed}, nil
}
func (fake *artifactHTTPFake) Plan(_ context.Context, command artifactapp.PlanCommand) (artifactapp.CommandResult, error) {
	fake.plan = command
	return fake.command(artifactapp.CommandPlan)
}
func (fake *artifactHTTPFake) SubmitOutline(_ context.Context, command artifactapp.SubmitOutlinePersistentCommand) (artifactapp.CommandResult, error) {
	fake.outline = command
	return fake.command(artifactapp.CommandSubmitOutline)
}
func (fake *artifactHTTPFake) ApproveOutline(_ context.Context, _ artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
	return fake.command(artifactapp.CommandApproveOutline)
}
func (fake *artifactHTTPFake) StartRevision(_ context.Context, _ artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
	return fake.command(artifactapp.CommandStartRevision)
}
func (fake *artifactHTTPFake) RecordSection(_ context.Context, command artifactapp.RecordSectionPersistentCommand) (artifactapp.CommandResult, error) {
	fake.section = command
	return fake.command(artifactapp.CommandRecordSection)
}
func (fake *artifactHTTPFake) ApproveDraft(_ context.Context, _ artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
	return fake.command(artifactapp.CommandApproveDraft)
}
func (fake *artifactHTTPFake) ExportMarkdown(_ context.Context, _ artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
	return fake.command(artifactapp.CommandExportMarkdown)
}
func (fake *artifactHTTPFake) Publish(_ context.Context, _ artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
	return fake.command(artifactapp.CommandPublish)
}
func (fake *artifactHTTPFake) Get(_ context.Context, _, _ foundation.ID) (artifactapp.State, error) {
	fake.queries++
	return fake.state, fake.err
}
func (fake *artifactHTTPFake) List(_ context.Context, _ artifactapp.ListQuery) (artifactapp.ArtifactPage, error) {
	fake.queries++
	return artifactapp.ArtifactPage{Items: []artifactapp.State{fake.state}}, fake.err
}
func (fake *artifactHTTPFake) ListSectionGenerations(_ context.Context, _, _ foundation.ID) (artifactapp.SectionGenerationList, error) {
	fake.queries++
	fake.generationQueries++
	return fake.generationList, fake.err
}
func (fake *artifactHTTPFake) GetExport(_ context.Context, _, _, _ foundation.ID) (artifactapp.ExportRecord, error) {
	fake.queries++
	return fake.export, fake.err
}
func (fake *artifactHTTPFake) StartSectionGeneration(_ context.Context, command artifactapp.StartSectionGenerationCommand) (artifactapp.StartSectionGenerationResult, error) {
	fake.generations++
	fake.generation = command
	if fake.generationErr != nil {
		return artifactapp.StartSectionGenerationResult{}, fake.generationErr
	}
	generation := fake.generationResult
	if !fake.preserveGenerationBinding {
		generation.WorkspaceID = command.WorkspaceID
		generation.ArtifactID = command.ArtifactID
		generation.SourceArtifactVersion = command.ExpectedVersion
		generation.SectionKey = command.SectionKey
	}
	generation.IdempotencyKey = command.IdempotencyKey
	return artifactapp.StartSectionGenerationResult{Generation: generation, Replayed: fake.generationReplayed}, nil
}
func artifactHTTPIDValue(value string) foundation.ID { return foundation.ID(value) }
