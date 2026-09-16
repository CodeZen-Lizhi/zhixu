package workspacehttp

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
)

type discoveryHTTPService struct {
	fakeWorkspaceService
	calls int
	id    foundation.ID
}

func (s *discoveryHTTPService) ListDiscoveryFailures(_ context.Context, id foundation.ID, _ string, _ int) (domain.DiscoveryFailurePage, error) {
	s.calls++
	s.id = id
	return domain.DiscoveryFailurePage{WorkspaceID: id, Items: []domain.DiscoveryFailure{}}, nil
}
func TestDiscoveryFailureHTTPStrictQuery(t *testing.T) {
	service := &discoveryHTTPService{}
	router := gin.New()
	NewHandler(service).Routes(router.Group("/api/v1"))
	path := "/api/v1/workspaces/" + string(handlerTestWorkspaceID) + "/discovery-failures"
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=1&limit=2", "?extra=1", "?cursor=../secret", "?cursor=/etc/passwd", "?cursor=.knowledge/a", "?cursor=%zz", "?limit="} {
		r := httptest.NewRecorder()
		router.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path+query, nil))
		if r.Code != 400 {
			t.Fatalf("query %s: %d", query, r.Code)
		}
	}
	if service.calls != 0 {
		t.Fatal("invalid query reached service")
	}
	r := httptest.NewRecorder()
	router.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path+"?limit=10", nil))
	if r.Code != 200 || service.calls != 1 || service.id != handlerTestWorkspaceID {
		t.Fatalf("valid bound query: %d", r.Code)
	}
}
