package http

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
)

func TestCaptureCapabilityMatrixUsesReadAndProposalScopes(t *testing.T) {
	const (
		workspaceID = "93000000-0000-4000-8000-000000000001"
		captureID   = "93000000-0000-4000-8000-000000000002"
		sourceID    = "93000000-0000-4000-8000-000000000003"
	)

	tests := []struct {
		name   string
		method string
		path   string
		want   []capability.Capability
	}{
		{name: "list", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/captures", want: []capability.Capability{capability.ReadLocal}},
		{name: "detail", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/captures/" + captureID, want: []capability.Capability{capability.ReadLocal}},
		{name: "create text or URL", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/captures", want: []capability.Capability{capability.WriteProposal}},
		{name: "upload file", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/capture-files", want: []capability.Capability{capability.WriteProposal}},
		{name: "retry", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/captures/" + captureID + "/retry", want: []capability.Capability{capability.WriteProposal}},
		{name: "profile detail", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/source-versions/" + sourceID + "/knowledge-profile", want: []capability.Capability{capability.ReadLocal}},
		{name: "profile retry", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/source-versions/" + sourceID + "/knowledge-profile/retry", want: []capability.Capability{capability.WriteProposal}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			if got := RequiredCapabilities(request); !slices.Equal(got, test.want) {
				t.Fatalf("capabilities=%q want=%q", got, test.want)
			}
		})
	}
}
