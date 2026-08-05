package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	organizinghttp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/http"
)

func TestOrganizingRoutesAreRegisteredFailClosed(t *testing.T) {
	router := NewRouter(Dependencies{
		Version:    "organizing-test",
		Organizing: organizinghttp.NewHandler(nil, nil, nil, time.Second),
	})

	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/00000000-0000-4000-8000-000000000001/organizing/drafts/00000000-0000-4000-8000-000000000002", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), `"error_code":"ORGANIZING_HTTP_UNAVAILABLE"`) {
		t.Fatalf("organizing route response = %d %s", response.Code, response.Body.String())
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	statusResponse := httptest.NewRecorder()
	router.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK ||
		!strings.Contains(statusResponse.Body.String(), `"organizing":{"status":"unavailable"}`) {
		t.Fatalf("organizing status response = %d %s", statusResponse.Code, statusResponse.Body.String())
	}
}
