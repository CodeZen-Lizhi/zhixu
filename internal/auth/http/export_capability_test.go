package http

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
)

func TestExportCapabilityMatrixRequiresReadLocal(t *testing.T) {
	const (
		workspaceID  = "93000000-0000-4000-8000-000000000001"
		exportID     = "93000000-0000-4000-8000-000000000002"
		collectionID = "93000000-0000-4000-8000-000000000003"
	)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/exports"},
		{name: "detail", method: http.MethodGet, path: "/api/v1/exports/" + exportID + "?workspace_id=" + workspaceID},
		{name: "list collection", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/exports?collection_id=" + collectionID},
		{name: "download", method: http.MethodGet, path: "/api/v1/exports/" + exportID + "/download?workspace_id=" + workspaceID},
	}
	want := []capability.Capability{capability.ReadLocal}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			if got := RequiredCapabilities(request); !slices.Equal(got, want) {
				t.Fatalf("capabilities=%q want=%q", got, want)
			}
		})
	}
}
