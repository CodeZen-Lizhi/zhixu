package http

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
)

func TestAuthoringCapabilitiesAreExplicitAndLeastPrivilege(t *testing.T) {
	workspaceID := "00000000-0000-4000-8000-000000000001"
	draftID := "00000000-0000-4000-8000-000000000002"
	documentID := "00000000-0000-4000-8000-000000000003"
	revisionID := "00000000-0000-4000-8000-000000000004"
	tests := []struct {
		name   string
		method string
		path   string
		want   []capability.Capability
	}{
		{name: "overview", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/authoring/overview", want: []capability.Capability{capability.ReadLocal}},
		{name: "working draft list", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/authoring/working-drafts", want: []capability.Capability{capability.ReadLocal}},
		{name: "document draft list", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/authoring/documents", want: []capability.Capability{capability.ReadLocal}},
		{name: "draft detail", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/authoring/working-drafts/" + draftID, want: []capability.Capability{capability.ReadLocal}},
		{name: "document detail", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/documents/" + documentID, want: []capability.Capability{capability.ReadLocal}},
		{name: "create draft", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/authoring/working-drafts", want: []capability.Capability{capability.WriteProposal}},
		{name: "autosave", method: http.MethodPut, path: "/api/v1/workspaces/" + workspaceID + "/authoring/working-drafts/" + draftID, want: []capability.Capability{capability.WriteProposal}},
		{name: "freeze", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/authoring/working-drafts/" + draftID + "/freeze", want: []capability.Capability{capability.WriteProposal}},
		{name: "publish proposal", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/documents/" + documentID + "/revisions/" + revisionID + "/publish-proposals", want: []capability.Capability{capability.WriteProposal}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			if got := RequiredCapabilities(request); !slices.Equal(got, test.want) {
				t.Fatalf("capabilities=%v want=%v", got, test.want)
			}
		})
	}
}
