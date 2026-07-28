package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	reviewhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/http"
)

func TestRouterRegistersReviewRoutesAndFailsClosedWithoutService(t *testing.T) {
	router := NewRouter(Dependencies{Version: "review-test", Review: reviewhttp.NewHandler(nil, time.Second)})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/review/decks?workspace_id=92000000-0000-4000-8000-000000000001", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "REVIEW_HTTP_UNAVAILABLE") {
		t.Fatalf("review route status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRouterAuthProtectsReviewRoutes(t *testing.T) {
	router := NewRouter(Dependencies{
		Version:      "review-auth-test",
		Auth:         readyAuthHandler(t),
		AuthRequired: true,
		Review:       reviewhttp.NewHandler(nil, time.Second),
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/review/decks?workspace_id=92000000-0000-4000-8000-000000000001", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "AUTH_UNAUTHORIZED") {
		t.Fatalf("unauthenticated review route status=%d body=%s", response.Code, response.Body.String())
	}
}
