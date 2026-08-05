package http

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
)

func TestDocumentHistoryCapabilitiesAreExplicitAndLeastPrivilege(t *testing.T) {
	workspaceID := "00000000-0000-4000-8000-000000000001"
	documentID := "00000000-0000-4000-8000-000000000002"
	base := "/api/v1/workspaces/" + workspaceID + "/documents/" + documentID
	tests := []struct {
		name   string
		method string
		path   string
		want   []capability.Capability
	}{
		{name: "history", method: http.MethodGet, path: base + "/history", want: []capability.Capability{capability.ReadLocal}},
		{name: "compare", method: http.MethodGet, path: base + "/history/compare?left=WORKTREE&right=HEAD", want: []capability.Capability{capability.ReadLocal}},
		{name: "restore preview", method: http.MethodPost, path: base + "/restore-previews", want: []capability.Capability{capability.ReadLocal}},
		{name: "restore proposal", method: http.MethodPost, path: base + "/restore-proposals", want: []capability.Capability{capability.WriteProposal}},
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
