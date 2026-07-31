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

func TestAPIWorkspaceRuntimeGateDoesNotCountLongLivedEventStream(t *testing.T) {
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
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))
	}()
	<-entered
	hooks := gate.Hooks()
	if err := hooks.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if quiesced, err := hooks.IsQuiesced(context.Background()); err != nil || !quiesced {
		t.Fatalf("event stream blocked quiescence: quiesced=%t err=%v", quiesced, err)
	}
	close(release)
	<-done
}
