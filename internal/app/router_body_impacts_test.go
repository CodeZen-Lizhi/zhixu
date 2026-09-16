package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	organizinghttp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/http"
)

func TestBodyImpactRouteRequiresReadCapability(t *testing.T) {
	const id = "93000000-0000-4000-8000-000000000001"
	router := NewRouter(Dependencies{AuthRequired: true, Auth: readyScopedAuthHandler(t, map[string]authdomain.Principal{
		"read":     {Kind: authdomain.PrincipalAPIToken, ID: id, Scopes: []capability.Capability{capability.ReadLocal}},
		"proposal": {Kind: authdomain.PrincipalAPIToken, ID: id, Scopes: []capability.Capability{capability.WriteProposal}},
	}), Synthesis: organizinghttp.NewSynthesisHandler(nil, nil, time.Second)})
	path := "/api/v1/workspaces/" + id + "/synthesis/notes/" + id + "/revisions/" + id + "/body-impacts"
	for _, tc := range []struct {
		token  string
		status int
		code   string
	}{{"", 401, "AUTH_UNAUTHORIZED"}, {"proposal", 403, "AUTH_CAPABILITY_DENIED"}, {"read", 503, "SYNTHESIS_HTTP_UNAVAILABLE"}} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if tc.token != "" {
			request.Header.Set("Authorization", "Bearer "+tc.token)
		}
		result := httptest.NewRecorder()
		router.ServeHTTP(result, request)
		if result.Code != tc.status || !strings.Contains(result.Body.String(), tc.code) {
			t.Fatalf("token=%s status=%d body=%s", tc.token, result.Code, result.Body.String())
		}
	}
}
