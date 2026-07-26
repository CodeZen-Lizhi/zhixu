package http

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
)

func TestArtifactCapabilityMatrixUsesReadAndProposalScopes(t *testing.T) {
	const (
		workspaceID = "92000000-0000-4000-8000-000000000001"
		artifactID  = "92000000-0000-4000-8000-000000000002"
		exportID    = "92000000-0000-4000-8000-000000000003"
	)

	tests := []struct {
		name   string
		method string
		path   string
		want   []capability.Capability
	}{
		{name: "list", method: http.MethodGet, path: "/api/v1/artifacts?workspace_id=" + workspaceID, want: []capability.Capability{capability.ReadLocal}},
		{name: "detail", method: http.MethodGet, path: "/api/v1/artifacts/" + artifactID + "?workspace_id=" + workspaceID, want: []capability.Capability{capability.ReadLocal}},
		{name: "read export", method: http.MethodGet, path: "/api/v1/artifacts/" + artifactID + "/exports/" + exportID + "?workspace_id=" + workspaceID, want: []capability.Capability{capability.ReadLocal}},
		{name: "plan", method: http.MethodPost, path: "/api/v1/artifacts", want: []capability.Capability{capability.WriteProposal}},
		{name: "submit outline", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactID + "/outline", want: []capability.Capability{capability.WriteProposal}},
		{name: "approve outline", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactID + "/outline/approve", want: []capability.Capability{capability.WriteProposal}},
		{name: "start revision", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactID + "/revisions", want: []capability.Capability{capability.WriteProposal}},
		{name: "record section", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactID + "/sections", want: []capability.Capability{capability.WriteProposal}},
		{name: "generate section", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactID + "/sections/generate", want: []capability.Capability{capability.WriteProposal}},
		{name: "approve draft", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactID + "/draft/approve", want: []capability.Capability{capability.WriteProposal}},
		{name: "export markdown", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactID + "/exports/markdown", want: []capability.Capability{capability.WriteProposal}},
		{name: "create publish proposal", method: http.MethodPost, path: "/api/v1/artifacts/" + artifactID + "/publish-proposals", want: []capability.Capability{capability.WriteProposal}},
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
