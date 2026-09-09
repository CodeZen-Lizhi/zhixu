package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authapplication "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationhttp "github.com/CodeZen-Lizhi/zhixu/internal/conversation/http"
	interviewhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/http"
)

func TestV2RoutesRetainAuthenticationCapabilitiesAndCSRF(t *testing.T) {
	const id = "93000000-0000-4000-8000-000000000001"
	const workspace = "93000000-0000-4000-8000-000000000002"
	router := NewRouter(Dependencies{
		AuthRequired: true,
		Auth: readyScopedAuthHandler(t, map[string]authdomain.Principal{
			"read":     {Kind: authdomain.PrincipalAPIToken, ID: id, Scopes: []capability.Capability{capability.ReadLocal}},
			"proposal": {Kind: authdomain.PrincipalAPIToken, ID: id, Scopes: []capability.Capability{capability.WriteProposal}},
		}),
		Conversation: conversationhttp.NewHandler(nil, nil),
		Interview:    interviewhttp.NewHandler(nil, time.Second),
	})
	unavailable := NewRouter(Dependencies{
		AuthRequired: true,
		Conversation: conversationhttp.NewHandler(nil, nil),
		Interview:    interviewhttp.NewHandler(nil, time.Second),
	})
	for _, route := range []struct {
		method string
		path   string
		token  string
	}{
		{http.MethodPost, "/conversations/" + id + "/questions", "read"},
		{http.MethodGet, "/conversations/" + id + "/turns?workspace_id=" + workspace, "read"},
		{http.MethodGet, "/answers/" + id + "?workspace_id=" + workspace, "read"},
		{http.MethodGet, "/answers/" + id + "/analysis-timeline?workspace_id=" + workspace, "read"},
		{http.MethodGet, "/review/interviews?workspace_id=" + workspace, "read"},
		{http.MethodPost, "/review/interviews", "proposal"},
		{http.MethodGet, "/review/interviews/" + id + "?workspace_id=" + workspace, "read"},
		{http.MethodPost, "/review/interviews/" + id + "/turns", "proposal"},
		{http.MethodPost, "/review/interviews/" + id + "/complete", "proposal"},
		{http.MethodPut, "/review/learning-paths/" + id + "/steps/93000000-0000-4000-8000-000000000003", "proposal"},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			request := func() *http.Request {
				return httptest.NewRequest(route.method, "/api/v2"+route.path, nil)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request())
			if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), authapplication.ErrorCodeUnauthorized) {
				t.Fatalf("missing authentication: status=%d body=%s", response.Code, response.Body.String())
			}

			wrongToken := "proposal"
			if route.token == wrongToken {
				wrongToken = "read"
			}
			denied := request()
			denied.Header.Set("Authorization", "Bearer "+wrongToken)
			response = httptest.NewRecorder()
			router.ServeHTTP(response, denied)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), authapplication.ErrorCodeForbidden) {
				t.Fatalf("insufficient capability: status=%d body=%s", response.Code, response.Body.String())
			}

			allowed := request()
			allowed.Header.Set("Authorization", "Bearer "+route.token)
			response = httptest.NewRecorder()
			router.ServeHTTP(response, allowed)
			if response.Code != http.StatusBadRequest && response.Code != http.StatusUnsupportedMediaType && response.Code != http.StatusServiceUnavailable {
				t.Fatalf("authorized request did not reach the domain boundary: status=%d body=%s", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), "CONVERSATION_") && !strings.Contains(response.Body.String(), "INTERVIEW_") {
				t.Fatalf("authorized request was rejected outside the domain boundary: %s", response.Body.String())
			}

			response = httptest.NewRecorder()
			unavailable.ServeHTTP(response, request())
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "AUTH_DEPENDENCY_UNAVAILABLE") {
				t.Fatalf("missing authentication dependency must fail closed: status=%d body=%s", response.Code, response.Body.String())
			}

			if route.method == http.MethodGet {
				return
			}
			for _, origin := range []string{"", "https://untrusted.example.test", "https://app.example.test"} {
				cookieRequest := request()
				cookieRequest.AddCookie(&http.Cookie{Name: authhttp.SessionCookieName, Value: "test-session"})
				if origin != "" {
					cookieRequest.Header.Set("Origin", origin)
				}
				response = httptest.NewRecorder()
				router.ServeHTTP(response, cookieRequest)
				if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), authapplication.ErrorCodeCSRF) {
					t.Fatalf("unsafe Cookie request without valid Origin/CSRF: status=%d body=%s", response.Code, response.Body.String())
				}
			}
		})
	}
}

func TestV2DoesNotAliasUnversionedOperations(t *testing.T) {
	router := NewRouter(Dependencies{
		AuthRequired: true, Auth: readyAuthHandler(t),
		Conversation: conversationhttp.NewHandler(nil, nil),
		Interview:    interviewhttp.NewHandler(nil, time.Second),
	})
	for _, path := range []string{"/api/v2/workspaces", "/api/v2/auth/sessions", "/api/v2/conversations"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("unregistered v2 route %s unexpectedly exists: status=%d", path, response.Code)
		}
	}
}
