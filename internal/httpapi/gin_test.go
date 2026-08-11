package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type ginContextKey struct{}

func TestGinHandlerPreservesStandardRequestContracts(t *testing.T) {
	engine := gin.New()
	engine.GET("/workspaces/:workspace_id/sources/:source_id", GinHandler(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.PathValue("workspace_id"); got != "workspace%2Fone" {
			t.Fatalf("workspace_id=%q", got)
		}
		if got := request.PathValue("source_id"); got != "source-2" {
			t.Fatalf("source_id=%q", got)
		}
		if got := request.Pattern; got != "/workspaces/{workspace_id}/sources/{source_id}" {
			t.Fatalf("request pattern=%q", got)
		}
		if got := request.Context().Value(ginContextKey{}); got != "request-context" {
			t.Fatalf("request context value=%v", got)
		}
		writer.Header().Set("X-Adapter", "gin")
		writer.WriteHeader(http.StatusAccepted)
	}))
	engine.UseRawPath = true
	engine.UnescapePathValues = false

	request := httptest.NewRequest(http.MethodGet, "/workspaces/workspace%2Fone/sources/source-2", nil)
	request = request.WithContext(context.WithValue(request.Context(), ginContextKey{}, "request-context"))
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || response.Header().Get("X-Adapter") != "gin" {
		t.Fatalf("status=%d header=%q", response.Code, response.Header().Get("X-Adapter"))
	}
}

func TestGinHandlerPreservesFlusher(t *testing.T) {
	engine := gin.New()
	engine.GET("/events", GinHandler(func(writer http.ResponseWriter, _ *http.Request) {
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Fatal("adapted writer does not implement http.Flusher")
		}
		writer.WriteHeader(http.StatusOK)
		flusher.Flush()
	}))

	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/events", nil))

	if !response.Flushed {
		t.Fatal("adapted writer did not flush the underlying response")
	}
}

func TestCanonicalRoutePattern(t *testing.T) {
	if got := CanonicalRoutePattern("/api/v1/workspaces/:workspace_id/files/*path"); got != "/api/v1/workspaces/{workspace_id}/files/{path}" {
		t.Fatalf("canonical pattern=%q", got)
	}
}
