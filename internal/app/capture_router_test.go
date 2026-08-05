package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	captureapplication "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	capturehttp "github.com/CodeZen-Lizhi/zhixu/internal/capture/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCaptureRoutesAreRegisteredFailClosed(t *testing.T) {
	router := NewRouter(Dependencies{
		Version: "capture-test",
		Capture: capturehttp.NewHandler(nil, time.Second),
	})

	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/00000000-0000-4000-8000-000000000001/captures", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), `"error_code":"CAPTURE_HTTP_UNAVAILABLE"`) {
		t.Fatalf("capture route response = %d %s", response.Code, response.Body.String())
	}
	profileRequest := httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/00000000-0000-4000-8000-000000000001/source-versions/00000000-0000-4000-8000-000000000002/knowledge-profile", nil)
	profileResponse := httptest.NewRecorder()
	router.ServeHTTP(profileResponse, profileRequest)
	if profileResponse.Code != http.StatusServiceUnavailable ||
		!strings.Contains(profileResponse.Body.String(), `"error_code":"CAPTURE_PROFILE_HTTP_UNAVAILABLE"`) {
		t.Fatalf("profile route response = %d %s", profileResponse.Code, profileResponse.Body.String())
	}
}

func TestSystemStatusReportsCaptureAvailability(t *testing.T) {
	for _, test := range []struct {
		name    string
		service capturehttp.Service
		want    string
	}{
		{name: "unavailable", want: `"capture":{"status":"unavailable"}`},
		{name: "ready", service: captureRouterService{}, want: `"capture":{"status":"ready"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := NewRouter(Dependencies{
				Version: "capture-test",
				Capture: capturehttp.NewHandler(test.service, time.Second),
			})

			request := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("system status response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCaptureCapabilityUnavailableDoesNotBlockCoreReadiness(t *testing.T) {
	router := NewRouter(Dependencies{
		Version:  "capture-test",
		Database: fakePinger{},
		Graph:    readyGraphHandler(),
		Capture:  capturehttp.NewHandler(nil, time.Second),
	})

	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("readyz response = %d %s", response.Code, response.Body.String())
	}
}

type captureRouterService struct{}

func (captureRouterService) CreateText(context.Context, captureapplication.TextCommand) (captureapplication.CreateResult, error) {
	return captureapplication.CreateResult{}, nil
}

func (captureRouterService) CreateURL(context.Context, captureapplication.URLCommand) (captureapplication.CreateResult, error) {
	return captureapplication.CreateResult{}, nil
}

func (captureRouterService) CreateUpload(context.Context, captureapplication.UploadCommand) (captureapplication.CreateResult, error) {
	return captureapplication.CreateResult{}, nil
}

func (captureRouterService) Retry(context.Context, captureapplication.RetryCommand) (captureapplication.RetryResult, error) {
	return captureapplication.RetryResult{}, nil
}

func (captureRouterService) Get(context.Context, foundation.ID, foundation.ID) (domain.Capture, error) {
	return domain.Capture{}, nil
}

func (captureRouterService) List(context.Context, captureapplication.ListQuery) (captureapplication.Page, error) {
	return captureapplication.Page{}, nil
}
