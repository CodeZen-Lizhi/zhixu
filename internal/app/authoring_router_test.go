package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	authoringhttp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestAuthoringRoutesAreRegisteredAndFailClosed(t *testing.T) {
	router := NewRouter(Dependencies{Version: "authoring-test", Authoring: authoringhttp.NewHandler(nil, time.Second)})
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/00000000-0000-4000-8000-000000000001/authoring/overview", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"error_code":"AUTHORING_HTTP_UNAVAILABLE"`) {
		t.Fatalf("authoring route response=%d %s", response.Code, response.Body.String())
	}
}

func TestSystemStatusReportsAuthoringAvailability(t *testing.T) {
	for _, test := range []struct {
		name    string
		service authoringhttp.Service
		want    string
	}{
		{name: "unavailable", want: `"authoring":{"status":"unavailable"}`},
		{name: "ready", service: authoringRouterService{}, want: `"authoring":{"status":"ready"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := NewRouter(Dependencies{
				Version: "authoring-test", Authoring: authoringhttp.NewHandler(test.service, time.Second),
			})
			request := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("system status response=%d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAuthoringUnavailableDoesNotBlockCoreReadiness(t *testing.T) {
	router := NewRouter(Dependencies{
		Version: "authoring-test", Database: fakePinger{}, Graph: readyGraphHandler(),
		Authoring: authoringhttp.NewHandler(nil, time.Second),
	})
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("readyz response=%d %s", response.Code, response.Body.String())
	}
}

type authoringRouterService struct{}

func (authoringRouterService) CreateWorkingDraft(context.Context, authoringapp.CreateCommand) (authoringapp.CreateResult, error) {
	return authoringapp.CreateResult{}, nil
}

func (authoringRouterService) GetWorkingDraft(context.Context, foundation.ID, foundation.ID) (domain.WorkingDraft, error) {
	return domain.WorkingDraft{}, nil
}

func (authoringRouterService) UpdateWorkingDraft(context.Context, authoringapp.UpdateCommand) (authoringapp.UpdateResult, error) {
	return authoringapp.UpdateResult{}, nil
}

func (authoringRouterService) ListWorkingDrafts(context.Context, authoringapp.ListQuery) (authoringapp.Page, error) {
	return authoringapp.Page{}, nil
}

func (authoringRouterService) ListDocumentDrafts(context.Context, authoringapp.DocumentListQuery) (authoringapp.DocumentPage, error) {
	return authoringapp.DocumentPage{}, nil
}

func (authoringRouterService) FreezeWorkingDraft(context.Context, authoringapp.FreezeCommand) (authoringapp.FreezeResult, error) {
	return authoringapp.FreezeResult{}, nil
}

func (authoringRouterService) PublishArticleRevision(context.Context, authoringapp.PublishCommand) (authoringapp.PublishResult, error) {
	return authoringapp.PublishResult{}, nil
}

func (authoringRouterService) GetDocumentDetail(context.Context, foundation.ID, foundation.ID) (authoringapp.DocumentDetail, error) {
	return authoringapp.DocumentDetail{}, nil
}

func (authoringRouterService) GetOverview(context.Context, foundation.ID) (authoringapp.Overview, error) {
	return authoringapp.Overview{}, nil
}
