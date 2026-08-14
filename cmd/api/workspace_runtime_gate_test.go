package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIWorkspaceRuntimeGateWaitsForInflightAndBlocksNewRequests(t *testing.T) {
	gate := newAPIWorkspaceRuntimeGate()
	entered := make(chan struct{})
	release := make(chan struct{})
	handler := gate.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	}))
	completed := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil))
		completed <- response
	}()
	<-entered
	hooks := gate.Hooks()
	if err := hooks.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if quiesced, err := hooks.IsQuiesced(context.Background()); err != nil || quiesced {
		t.Fatalf("in-flight quiesced=%t err=%v", quiesced, err)
	}
	blocked := httptest.NewRecorder()
	handler.ServeHTTP(blocked, httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", nil))
	if blocked.Code != http.StatusServiceUnavailable || blocked.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(blocked.Body.String(), "WORKSPACE_SWITCH_QUIESCING") {
		t.Fatalf("blocked response=%d %s", blocked.Code, blocked.Body.String())
	}
	close(release)
	if response := <-completed; response.Code != http.StatusNoContent {
		t.Fatalf("in-flight response=%d", response.Code)
	}
	if quiesced, err := hooks.IsQuiesced(context.Background()); err != nil || !quiesced {
		t.Fatalf("drained quiesced=%t err=%v", quiesced, err)
	}
	if err := hooks.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	resumed := httptest.NewRecorder()
	gate.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(resumed, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil))
	if resumed.Code != http.StatusNoContent {
		t.Fatalf("resumed response=%d", resumed.Code)
	}
}

func TestAPIWorkspaceRuntimeGateDoesNotCountLongLivedStreams(t *testing.T) {
	for _, path := range []string{
		"/api/v1/events",
		"/api/v1/answers/92000000-0000-4000-8000-000000000002/stream?workspace_id=92000000-0000-4000-8000-000000000001",
	} {
		t.Run(path, func(t *testing.T) {
			gate := newAPIWorkspaceRuntimeGate()
			entered := make(chan struct{})
			release := make(chan struct{})
			handler := gate.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				close(entered)
				<-release
				writer.WriteHeader(http.StatusNoContent)
			}))
			done := make(chan struct{})
			go func() {
				defer close(done)
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
			}()
			<-entered
			hooks := gate.Hooks()
			if err := hooks.Begin(context.Background()); err != nil {
				t.Fatal(err)
			}
			if quiesced, err := hooks.IsQuiesced(context.Background()); err != nil || !quiesced {
				t.Fatalf("long-lived stream blocked quiescence: quiesced=%t err=%v", quiesced, err)
			}
			close(release)
			<-done
		})
	}
}

func TestAPIWorkspaceRuntimeGateStillCountsLookalikeDraftPaths(t *testing.T) {
	for _, path := range []string{
		"/api/v1/answers/stream",
		"/api/v1/answers/id/stream/extra",
		"/api/v1/answers/id/other/stream",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if !tracksWorkspaceRuntimeWork(request) {
			t.Fatalf("lookalike path was excluded from drain: %s", path)
		}
	}
}
