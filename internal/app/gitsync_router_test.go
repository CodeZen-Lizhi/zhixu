package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gitsynchttp "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/http"
)

func TestGitSyncRoutesAreRegisteredFailClosedAndDisableCaching(t *testing.T) {
	router := NewRouter(Dependencies{Version: "git-sync-test", GitSync: gitsynchttp.NewHandler(nil, time.Second)})
	workspace := "/api/v1/workspaces/00000000-0000-4000-8000-000000000001"
	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: workspace + "/git-remote"},
		{method: http.MethodPut, path: workspace + "/git-remote"},
		{method: http.MethodDelete, path: workspace + "/git-remote"},
		{method: http.MethodPost, path: workspace + "/git-remote/tests"},
		{method: http.MethodGet, path: workspace + "/git-sync"},
		{method: http.MethodGet, path: workspace + "/git-sync/runs"},
		{method: http.MethodPost, path: workspace + "/git-sync/runs"},
		{method: http.MethodGet, path: workspace + "/git-sync/runs/00000000-0000-4000-8000-000000000002"},
		{method: http.MethodPost, path: workspace + "/git-sync/runs/00000000-0000-4000-8000-000000000002/retries"},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"error_code":"GIT_SYNC_HTTP_UNAVAILABLE"`) {
			t.Fatalf("%s %s response=%d %s", test.method, test.path, response.Code, response.Body.String())
		}
		if strings.Contains(test.path, "/git-remote") && !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("%s %s cache-control=%q", test.method, test.path, response.Header().Get("Cache-Control"))
		}
	}
}
