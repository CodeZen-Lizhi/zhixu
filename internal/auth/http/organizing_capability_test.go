package http

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
)

func TestOrganizingCapabilitiesAreExplicitAndLeastPrivilege(t *testing.T) {
	const (
		workspaceID = "00000000-0000-4000-8000-000000000001"
		draftID     = "00000000-0000-4000-8000-000000000002"
		materialID  = "00000000-0000-4000-8000-000000000003"
		snapshotID  = "00000000-0000-4000-8000-000000000004"
		templateID  = "00000000-0000-4000-8000-000000000005"
	)
	tests := []struct {
		name   string
		method string
		path   string
		want   capability.Capability
	}{
		{name: "create draft", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/organizing/drafts", want: capability.WriteProposal},
		{name: "draft detail", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/organizing/drafts/" + draftID, want: capability.ReadLocal},
		{name: "update draft", method: http.MethodPut, path: "/api/v1/workspaces/" + workspaceID + "/organizing/drafts/" + draftID, want: capability.WriteProposal},
		{name: "suggest", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/organizing/drafts/" + draftID + "/suggestions", want: capability.WriteProposal},
		{name: "add material", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/organizing/drafts/" + draftID + "/materials", want: capability.WriteProposal},
		{name: "set material selection", method: http.MethodPatch, path: "/api/v1/workspaces/" + workspaceID + "/organizing/drafts/" + draftID + "/materials/" + materialID, want: capability.WriteProposal},
		{name: "remove material", method: http.MethodDelete, path: "/api/v1/workspaces/" + workspaceID + "/organizing/drafts/" + draftID + "/materials/" + materialID, want: capability.WriteProposal},
		{name: "confirm", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/organizing/drafts/" + draftID + "/confirm", want: capability.WriteProposal},
		{name: "material search", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/organizing/materials/search", want: capability.ReadLocal},
		{name: "snapshot detail", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/organizing/snapshots/" + snapshotID, want: capability.ReadLocal},
		{name: "template list", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/organizing/templates", want: capability.ReadLocal},
		{name: "create template", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/organizing/templates", want: capability.WriteProposal},
		{name: "template detail", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/organizing/templates/" + templateID, want: capability.ReadLocal},
		{name: "clone template", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/organizing/templates/" + templateID + "/clone", want: capability.WriteProposal},
		{name: "revise template", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/organizing/templates/" + templateID + "/revisions", want: capability.WriteProposal},
		{name: "run detail", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/organizing/runs/" + snapshotID, want: capability.ReadLocal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			if got := RequiredCapabilities(request); !slices.Equal(got, []capability.Capability{test.want}) {
				t.Fatalf("capabilities=%v want=%v", got, []capability.Capability{test.want})
			}
		})
	}
}
