package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	artifacthttp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/http"
)

const artifactRouterWorkspaceID = "92000000-0000-4000-8000-000000000001"
const artifactRouterID = "92000000-0000-4000-8000-000000000002"
const artifactRouterExportID = "92000000-0000-4000-8000-000000000003"

func TestRouterRegistersArtifactRoutesAndFailsClosedWithoutServices(t *testing.T) {
	router := NewRouter(Dependencies{
		Version:  "artifact-test",
		Artifact: artifacthttp.NewHandler(nil, nil, time.Second),
	})

	for _, test := range []struct {
		name      string
		method    string
		path      string
		body      string
		errorCode string
	}{
		{name: "list", method: http.MethodGet, path: "/api/v1/artifacts?workspace_id=" + artifactRouterWorkspaceID},
		{name: "plan", method: http.MethodPost, path: "/api/v1/artifacts", body: `{"workspace_id":"` + artifactRouterWorkspaceID + `","type":"guide","title":"Artifact","scope_definition":"scope"}`},
		{name: "detail", method: http.MethodGet, path: "/api/v1/artifacts/" + artifactRouterID + "?workspace_id=" + artifactRouterWorkspaceID},
		{name: "submit outline", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactRouterID + "/outline", body: `{"workspace_id":"` + artifactRouterWorkspaceID + `","expected_version":1,"outline":[{"key":"section-1","title":"Section"}]}`},
		{name: "approve outline", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactRouterID + "/outline/approve", body: `{"workspace_id":"` + artifactRouterWorkspaceID + `","expected_version":1}`},
		{name: "start revision", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactRouterID + "/revisions", body: `{"workspace_id":"` + artifactRouterWorkspaceID + `","expected_version":1}`},
		{name: "record section", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactRouterID + "/sections", body: `{"workspace_id":"` + artifactRouterWorkspaceID + `","expected_version":1,"section":{"key":"section-1","title":"Section","content":"","citations":[],"coverage":{"section_key":"section-1","status":"GAP","gaps":[]}}}`},
		{name: "generate section", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactRouterID + "/sections/generate", body: `{"workspace_id":"` + artifactRouterWorkspaceID + `","expected_version":1,"section_key":"section-1"}`, errorCode: "ARTIFACT_GENERATION_CAPABILITY_UNAVAILABLE"},
		{name: "approve draft", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactRouterID + "/draft/approve", body: `{"workspace_id":"` + artifactRouterWorkspaceID + `","expected_version":1}`},
		{name: "export markdown", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactRouterID + "/exports/markdown", body: `{"workspace_id":"` + artifactRouterWorkspaceID + `","expected_version":1}`},
		{name: "read export", method: http.MethodGet, path: "/api/v1/artifacts/" + artifactRouterID + "/exports/" + artifactRouterExportID + "?workspace_id=" + artifactRouterWorkspaceID},
		{name: "create publish proposal", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactRouterID + "/publish-proposals", body: `{"workspace_id":"` + artifactRouterWorkspaceID + `","expected_version":1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			errorCode := test.errorCode
			if errorCode == "" {
				errorCode = "ARTIFACT_HTTP_UNAVAILABLE"
			}
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.method == http.MethodPost {
				request.Header.Set("Idempotency-Key", "artifact-router-test-key")
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), errorCode) {
				t.Fatalf("%s status=%d body=%s", test.name, response.Code, response.Body.String())
			}
		})
	}
}

func TestRouterAuthProtectsArtifactRoutes(t *testing.T) {
	router := NewRouter(Dependencies{
		Version:      "artifact-auth-test",
		Auth:         readyAuthHandler(t),
		AuthRequired: true,
		Artifact:     artifacthttp.NewHandler(nil, nil, time.Second),
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts?workspace_id="+artifactRouterWorkspaceID, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "AUTH_UNAUTHORIZED") {
		t.Fatalf("unauthenticated artifact route status=%d body=%s", response.Code, response.Body.String())
	}
}
