package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRouterUsesExplicitGinBoundaryConfiguration(t *testing.T) {
	router := NewRouter(quietRouterDependencies())
	if router.RedirectTrailingSlash || router.RedirectFixedPath || router.RemoveExtraSlash {
		t.Fatal("Gin path normalization or redirects must remain disabled")
	}
	if !router.HandleMethodNotAllowed || !router.UseRawPath || router.UseEscapedPath || router.UnescapePathValues {
		t.Fatal("Gin method or raw-path configuration drifted")
	}
	if router.ContextWithFallback || router.ForwardedByClientIP || len(router.RemoteIPHeaders) != 0 || router.TrustedPlatform != "" {
		t.Fatal("Gin context or proxy trust configuration drifted")
	}
}

func TestRouterDoesNotNormalizeOrRedirectRequestPaths(t *testing.T) {
	router := NewRouter(quietRouterDependencies())
	for _, test := range []struct {
		target string
		code   string
	}{
		{target: "/api/v1/system/status/", code: "NOT_FOUND"},
		{target: "/api/v1//system/status", code: "NOT_FOUND"},
		{target: "/API/v1/system/status", code: "WEB_ASSETS_UNAVAILABLE"},
	} {
		t.Run(test.target, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.target, nil))
			if response.Code != http.StatusNotFound || response.Header().Get("Location") != "" ||
				!strings.Contains(response.Body.String(), `"error_code":"`+test.code+`"`) {
				t.Fatalf("status=%d Location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
			}
		})
	}
}

func TestRouterMethodErrorsUseProblemWithoutAllowHeader(t *testing.T) {
	router := NewRouter(quietRouterDependencies())
	tests := []struct {
		name   string
		method string
		target string
		status int
		code   string
	}{
		{name: "known path wrong method", method: http.MethodPost, target: "/api/v1/system/status", status: http.StatusMethodNotAllowed, code: "METHOD_NOT_ALLOWED"},
		{name: "unsupported method", method: "BREW", target: "/api/v1/unknown", status: http.StatusMethodNotAllowed, code: "METHOD_NOT_ALLOWED"},
		{name: "supported method unknown path", method: http.MethodOptions, target: "/api/v1/unknown", status: http.StatusNotFound, code: "NOT_FOUND"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(test.method, test.target, nil))
			if response.Code != test.status || response.Header().Get("Allow") != "" ||
				!strings.Contains(response.Body.String(), `"error_code":"`+test.code+`"`) {
				t.Fatalf("status=%d Allow=%q body=%s", response.Code, response.Header().Get("Allow"), response.Body.String())
			}
		})
	}
}

func TestRouterDoesNotTrustForwardedClientIP(t *testing.T) {
	router := NewRouter(quietRouterDependencies())
	router.GET("/client-ip-test", func(context *gin.Context) {
		context.String(http.StatusOK, context.ClientIP())
	})
	request := httptest.NewRequest(http.MethodGet, "/client-ip-test", nil)
	request.RemoteAddr = "203.0.113.10:4321"
	request.Header.Set("X-Forwarded-For", "198.51.100.20")
	request.Header.Set("X-Real-IP", "198.51.100.21")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "203.0.113.10" {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestRouterStaticFallbackNeverOwnsAPIPaths(t *testing.T) {
	calls := 0
	deps := quietRouterDependencies()
	deps.Static = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		writer.WriteHeader(http.StatusAccepted)
	})
	router := NewRouter(deps)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/documents/getting-started", nil))
	if response.Code != http.StatusAccepted || calls != 1 {
		t.Fatalf("static status=%d calls=%d", response.Code, calls)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil))
	if response.Code != http.StatusNotFound || calls != 1 || !strings.Contains(response.Body.String(), `"error_code":"NOT_FOUND"`) {
		t.Fatalf("api status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
}

func quietRouterDependencies() Dependencies {
	return Dependencies{
		Version: "gin-boundary-test",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}
