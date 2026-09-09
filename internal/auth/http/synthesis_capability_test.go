package http

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
)

// Synthesis can prepare work and proposals, but none of its endpoints grants
// file-write or Git-commit capability. Approval retains its existing boundary.
func TestSynthesisReadAndPreparationCapabilities(t *testing.T) {
	const base = "/api/v1/workspaces/00000000-0000-4000-8000-000000000001/synthesis"
	const note = "/notes/00000000-0000-4000-8000-000000000002"
	const revision = "/revisions/00000000-0000-4000-8000-000000000003"
	const processing = "/processing/00000000-0000-4000-8000-000000000004"
	const preparation = "/interviews/00000000-0000-4000-8000-000000000005"
	for _, route := range []struct {
		method string
		path   string
		want   capability.Capability
	}{
		{http.MethodGet, "/notes", capability.ReadLocal},
		{http.MethodGet, note, capability.ReadLocal},
		{http.MethodGet, note + "/revisions", capability.ReadLocal},
		{http.MethodGet, note + revision, capability.ReadLocal},
		{http.MethodGet, note + revision + "/sources/00000000-0000-4000-8000-000000000006", capability.ReadLocal},
		{http.MethodGet, "/processing", capability.ReadLocal},
		{http.MethodGet, processing, capability.ReadLocal},
		{http.MethodPost, processing + "/retry", capability.WriteProposal},
		{http.MethodPost, note + "/interviews", capability.WriteProposal},
		{http.MethodGet, note + "/interviews", capability.ReadLocal},
		{http.MethodGet, note + preparation, capability.ReadLocal},
		{http.MethodPost, note + preparation + "/retry", capability.WriteProposal},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, base+route.path, nil)
			if got := RequiredCapabilities(request); !slices.Equal(got, []capability.Capability{route.want}) {
				t.Fatalf("capabilities=%v, want only %v", got, route.want)
			}
		})
	}
}
