package http

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
)

func TestGitSyncCapabilitiesMatchReadConfigurationAndGitSideEffects(t *testing.T) {
	workspace := "/api/v1/workspaces/00000000-0000-4000-8000-000000000001"
	run := "/00000000-0000-4000-8000-000000000002"
	read := []capability.Capability{capability.ReadLocal}
	gitWrite := []capability.Capability{capability.GitWrite}
	for _, test := range []struct {
		method string
		path   string
		want   []capability.Capability
	}{
		{method: http.MethodGet, path: workspace + "/git-remote", want: read},
		{method: http.MethodPut, path: workspace + "/git-remote", want: gitWrite},
		{method: http.MethodDelete, path: workspace + "/git-remote", want: gitWrite},
		{method: http.MethodPost, path: workspace + "/git-remote/tests", want: gitWrite},
		{method: http.MethodGet, path: workspace + "/git-sync", want: read},
		{method: http.MethodGet, path: workspace + "/git-sync/runs", want: read},
		{method: http.MethodGet, path: workspace + "/git-sync/runs" + run, want: read},
		{method: http.MethodPost, path: workspace + "/git-sync/runs", want: gitWrite},
		{method: http.MethodPost, path: workspace + "/git-sync/runs" + run + "/retries", want: gitWrite},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		if got := RequiredCapabilities(request); !slices.Equal(got, test.want) {
			t.Fatalf("%s %s capabilities=%v want=%v", test.method, test.path, got, test.want)
		}
	}
}
