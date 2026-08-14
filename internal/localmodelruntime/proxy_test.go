package localmodelruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxyAllowlistAndHealthAreIndependentOfChild(t *testing.T) {
	manager := newTestManager(t, &fakeChildRunner{}, time.Second)
	transport := &recordingTransport{}
	handler := newTestHandler(t, manager, transport, 1024)

	health := performRequest(handler, http.MethodGet, "/healthz", "", nil)
	if health.Code != http.StatusNoContent {
		t.Fatalf("health status = %d body=%s", health.Code, health.Body.String())
	}

	tests := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/v1/chat/completions", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/pull", http.StatusNotFound},
		{http.MethodPost, "/api/embed?debug=true", http.StatusNotFound},
		{http.MethodPost, "/api%2Fembed", http.StatusNotFound},
		{http.MethodPost, "/debug/pprof", http.StatusNotFound},
	}
	for _, testCase := range tests {
		response := performRequest(handler, testCase.method, testCase.path, "application/json", strings.NewReader(`{}`))
		if response.Code != testCase.want {
			t.Errorf("%s %s status=%d want=%d body=%s", testCase.method, testCase.path, response.Code, testCase.want, response.Body.String())
		}
	}
	if transport.calls() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.calls())
	}

	for _, path := range []string{"/v1/chat/completions", "/api/embed"} {
		response := performRequest(handler, http.MethodPost, path, "text/plain", strings.NewReader(`{"model":"fixture"}`))
		if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" || !strings.Contains(response.Body.String(), "LOCAL_MODEL_RUNTIME_UNAVAILABLE") {
			t.Errorf("%s unavailable response: status=%d headers=%v body=%s", path, response.Code, response.Header(), response.Body.String())
		}
	}
}

func TestProxyForwardsOnlyBoundedInferenceRequest(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetInferenceReady(true); err != nil {
		t.Fatal(err)
	}
	defer runner.child(0).exit(nil)
	transport := &recordingTransport{responseBody: "fixture-stream"}
	handler := newTestHandler(t, manager, transport, 64)

	requestBody := `{"model":"fixture"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Authorization", "Bearer must-not-cross")
	request.Header.Set("X-Forwarded-For", "203.0.113.5")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != "fixture-stream" {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	forwarded := transport.lastRequest()
	if forwarded.URL.String() != childOllamaOrigin+"/v1/chat/completions" || forwarded.Host != "127.0.0.1:11435" {
		t.Fatalf("forwarded target = %s host=%s", forwarded.URL, forwarded.Host)
	}
	if forwarded.Header.Get("Authorization") != "" || forwarded.Header.Get("X-Forwarded-For") != "" || forwarded.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("forwarded headers = %v", forwarded.Header)
	}
	if transport.lastBody() != requestBody {
		t.Fatalf("forwarded body = %q", transport.lastBody())
	}
}

func TestProxyRejectsInvalidOrOversizedBodies(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetInferenceReady(true); err != nil {
		t.Fatal(err)
	}
	defer runner.child(0).exit(nil)
	transport := &recordingTransport{}
	handler := newTestHandler(t, manager, transport, 8)

	tests := []struct {
		name        string
		contentType string
		body        string
		want        int
	}{
		{"content type", "text/plain", `{}`, http.StatusUnsupportedMediaType},
		{"empty", "application/json", "", http.StatusBadRequest},
		{"too large", "application/json", `123456789`, http.StatusRequestEntityTooLarge},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			response := performRequest(handler, http.MethodPost, "/api/embed", testCase.contentType, strings.NewReader(testCase.body))
			if response.Code != testCase.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, testCase.want, response.Body.String())
			}
		})
	}
	if transport.calls() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.calls())
	}
}

func TestProxyMapsUpstreamFailureWithoutLeakingCause(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetInferenceReady(true); err != nil {
		t.Fatal(err)
	}
	defer runner.child(0).exit(nil)
	const canary = "upstream-secret-canary"
	handler := newTestHandler(t, manager, &recordingTransport{err: errors.New(canary)}, 1024)

	response := performRequest(handler, http.MethodPost, "/api/embed", "application/json", strings.NewReader(`{}`))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "LOCAL_MODEL_RUNTIME_UNAVAILABLE") || strings.Contains(response.Body.String(), canary) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProxyBoundsOversizedUpstreamError(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetInferenceReady(true); err != nil {
		t.Fatal(err)
	}
	defer runner.child(0).exit(nil)
	transport := &recordingTransport{responseStatus: http.StatusInternalServerError, responseBody: strings.Repeat("x", int(maxProxyErrorBytes)+1)}
	handler := newTestHandler(t, manager, transport, 1024)

	response := performRequest(handler, http.MethodPost, "/api/embed", "application/json", strings.NewReader(`{}`))
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "LOCAL_MODEL_RUNTIME_RESPONSE_INVALID") || response.Body.Len() > int(maxProxyErrorBytes) {
		t.Fatalf("status=%d size=%d body=%s", response.Code, response.Body.Len(), response.Body.String())
	}
}

func TestProxyRequestIsCancelledWhenChildStops(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetInferenceReady(true); err != nil {
		t.Fatal(err)
	}
	transport := &cancellationTransport{entered: make(chan struct{})}
	handler := newTestHandler(t, manager, transport, 1024)
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- performRequest(handler, http.MethodPost, "/api/embed", "application/json", strings.NewReader(`{}`))
	}()
	<-transport.entered
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-response:
		if result.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("proxy request did not stop with child generation")
	}
}

func TestProxyPropagatesCallerCancellation(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetInferenceReady(true); err != nil {
		t.Fatal(err)
	}
	defer runner.child(0).exit(nil)
	transport := &cancellationTransport{entered: make(chan struct{})}
	handler := newTestHandler(t, manager, transport, 1024)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/embed", strings.NewReader(`{}`)).WithContext(requestContext)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	<-transport.entered
	cancelRequest()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("proxy request ignored caller cancellation")
	}
}

func TestProxyBodyReadIsCancelledWhenChildStops(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetInferenceReady(true); err != nil {
		t.Fatal(err)
	}
	reader := &blockingReader{entered: make(chan struct{})}
	handler := newTestHandler(t, manager, &recordingTransport{}, 1024)
	request := httptest.NewRequest(http.MethodPost, "/api/embed", reader)
	request.ContentLength = -1
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	<-reader.entered
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader.release()
	select {
	case <-done:
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("body read did not observe child stop")
	}
}

func newTestHandler(t *testing.T, manager *Manager, transport http.RoundTripper, maxBytes int64) http.Handler {
	t.Helper()
	handler, err := NewHandler(manager, ProxyOptions{MaxRequestBytes: maxBytes, RequestTimeout: time.Second, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func performRequest(handler http.Handler, method, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, body)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type recordingTransport struct {
	mu             sync.Mutex
	requests       []*http.Request
	bodies         []string
	responseBody   string
	responseStatus int
	err            error
}

type cancellationTransport struct {
	entered chan struct{}
	once    sync.Once
}

type blockingReader struct {
	entered chan struct{}
	wake    chan struct{}
	once    sync.Once
}

func (reader *blockingReader) Read([]byte) (int, error) {
	reader.once.Do(func() {
		reader.wake = make(chan struct{})
		close(reader.entered)
	})
	<-reader.wake
	return 0, io.EOF
}

func (reader *blockingReader) release() {
	close(reader.wake)
}

func (transport *cancellationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.once.Do(func() { close(transport.entered) })
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func (transport *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(request.Body)
	clone := request.Clone(context.Background())
	clone.Header = request.Header.Clone()
	transport.mu.Lock()
	transport.requests = append(transport.requests, clone)
	transport.bodies = append(transport.bodies, string(body))
	transport.mu.Unlock()
	if transport.err != nil {
		return nil, transport.err
	}
	status := transport.responseStatus
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(transport.responseBody)),
		Request:    request,
	}, nil
}

func (transport *recordingTransport) calls() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return len(transport.requests)
}

func (transport *recordingTransport) lastRequest() *http.Request {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.requests[len(transport.requests)-1]
}

func (transport *recordingTransport) lastBody() string {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.bodies[len(transport.bodies)-1]
}
