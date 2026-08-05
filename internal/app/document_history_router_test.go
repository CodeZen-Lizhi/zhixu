package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	documenthistoryhttp "github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/http"
)

func TestDocumentHistoryRoutesAreRegisteredAndFailClosed(t *testing.T) {
	router := NewRouter(Dependencies{
		Version:         "document-history-test",
		DocumentHistory: documenthistoryhttp.NewHandler(nil, time.Second),
	})
	base := "/api/v1/workspaces/00000000-0000-4000-8000-000000000001/documents/00000000-0000-4000-8000-000000000002"
	for _, test := range []struct {
		name, method, path string
	}{
		{name: "history", method: http.MethodGet, path: base + "/history"},
		{name: "compare", method: http.MethodGet, path: base + "/history/compare"},
		{name: "restore preview", method: http.MethodPost, path: base + "/restore-previews"},
		{name: "restore proposal", method: http.MethodPost, path: base + "/restore-proposals"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"error_code":"DOCUMENT_HISTORY_HTTP_UNAVAILABLE"`) {
				t.Fatalf("document history route response=%d %s", response.Code, response.Body.String())
			}
		})
	}
}
