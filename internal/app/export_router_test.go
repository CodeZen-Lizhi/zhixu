package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	exporthttp "github.com/CodeZen-Lizhi/zhixu/internal/export/http"
)

func TestRouterRegistersExportRoutesAndFailsClosedWithoutService(t *testing.T) {
	const (
		workspaceID  = "93000000-0000-4000-8000-000000000001"
		exportID     = "93000000-0000-4000-8000-000000000002"
		collectionID = "93000000-0000-4000-8000-000000000003"
	)
	router := NewRouter(Dependencies{Version: "export-test", Export: exporthttp.NewHandler(nil)})

	for _, test := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/exports"},
		{name: "detail", method: http.MethodGet, path: "/api/v1/exports/" + exportID + "?workspace_id=" + workspaceID},
		{name: "list collection", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/exports?collection_id=" + collectionID},
		{name: "download", method: http.MethodGet, path: "/api/v1/exports/" + exportID + "/download?workspace_id=" + workspaceID},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "EXPORT_DEPENDENCY_UNAVAILABLE") {
				t.Fatalf("%s status=%d body=%s", test.name, response.Code, response.Body.String())
			}
		})
	}
}

func TestRouterAuthProtectsExportRoutes(t *testing.T) {
	router := NewRouter(Dependencies{
		Version:      "export-auth-test",
		Auth:         readyAuthHandler(t),
		AuthRequired: true,
		Export:       exporthttp.NewHandler(nil),
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/93000000-0000-4000-8000-000000000001/exports?collection_id=93000000-0000-4000-8000-000000000003", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "AUTH_UNAUTHORIZED") {
		t.Fatalf("unauthenticated export route status=%d body=%s", response.Code, response.Body.String())
	}
}
