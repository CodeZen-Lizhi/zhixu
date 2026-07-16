package webassets

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandlerServesStaticFileAndSPAFallback(t *testing.T) {
	handler := NewFS(fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<html>app</html>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log('app')")},
	})

	staticRequest := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	staticResponse := httptest.NewRecorder()
	handler.ServeHTTP(staticResponse, staticRequest)
	if staticResponse.Code != http.StatusOK || !strings.Contains(staticResponse.Body.String(), "console.log") {
		t.Fatalf("static response: %d %s", staticResponse.Code, staticResponse.Body.String())
	}

	spaRequest := httptest.NewRequest(http.MethodGet, "/dashboard/search", nil)
	spaResponse := httptest.NewRecorder()
	handler.ServeHTTP(spaResponse, spaRequest)
	if spaResponse.Code != http.StatusOK || !strings.Contains(spaResponse.Body.String(), "<html>app</html>") {
		t.Fatalf("SPA response: %d %s", spaResponse.Code, spaResponse.Body.String())
	}
}

func TestHandlerDoesNotServeTraversalPath(t *testing.T) {
	handler := NewFS(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("app")}})
	request := httptest.NewRequest(http.MethodGet, "/../secret", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}
