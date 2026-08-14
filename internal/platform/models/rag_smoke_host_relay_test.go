package models

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRAGSmokeChatRelayForwardsAuthorizationAndSSE(t *testing.T) {
	var received struct {
		authorization string
		path          string
		host          string
		forwarded     string
		xForwardedFor string
		via           string
		xRealIP       string
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received.authorization = request.Header.Get("Authorization")
		received.path = request.URL.Path
		received.host = request.Host
		received.forwarded = request.Header.Get("Forwarded")
		received.xForwardedFor = request.Header.Get("X-Forwarded-For")
		received.via = request.Header.Get("Via")
		received.xRealIP = request.Header.Get("X-Real-IP")
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(writer, "data: {\"delta\":\"one\"}\n\n")
		writer.(http.Flusher).Flush()
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()
	relay := newTestRAGSmokeChatRelay(t, server)
	request := httptest.NewRequest(http.MethodPost, ragSmokeRelayChatPath, strings.NewReader(`{"stream":true}`))
	request.Header.Set("Authorization", "Bearer smoke-secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Forwarded", "for=198.51.100.1")
	request.Header.Set("X-Forwarded-For", "198.51.100.1")
	request.Header.Set("Via", "untrusted")
	request.Header.Set("X-Real-IP", "198.51.100.1")
	response := httptest.NewRecorder()
	relay.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" ||
		response.Body.String() != "data: {\"delta\":\"one\"}\n\ndata: [DONE]\n\n" {
		t.Fatalf("status=%d type=%q body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if received.authorization != "Bearer smoke-secret" || received.path != ragSmokeRelayChatPath || received.host != relayEndpointHost(t, server) ||
		received.forwarded != "" || received.xForwardedFor != "" || received.via != "" || received.xRealIP != "" {
		t.Fatalf("received=%#v", received)
	}
	health := httptest.NewRecorder()
	relay.ServeHTTP(health, httptest.NewRequest(http.MethodGet, ragSmokeRelayHealthPath, nil))
	if health.Code != http.StatusOK || health.Body.String() != `{"forwarded_count":1}` {
		t.Fatalf("health status=%d body=%q", health.Code, health.Body.String())
	}
}

func TestRAGSmokeChatRelayRejectsInvalidRequestBeforeUpstream(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	relay := newTestRAGSmokeChatRelay(t, server)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, ragSmokeRelayChatPath, nil),
		httptest.NewRequest(http.MethodPost, ragSmokeRelayChatPath+"?unexpected=1", nil),
		httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil),
		httptest.NewRequest(http.MethodPost, ragSmokeRelayChatPath, nil),
		httptest.NewRequest(http.MethodPost, ragSmokeRelayChatPath, strings.NewReader("{}")),
	} {
		response := httptest.NewRecorder()
		relay.ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed && response.Code != http.StatusNotFound && response.Code != http.StatusUnauthorized && response.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("request=%s %s status=%d", request.Method, request.URL, response.Code)
		}
	}
	if calls != 0 {
		t.Fatalf("unexpected upstream calls=%d", calls)
	}
}

func TestRAGSmokeChatRelayRejectsOversizedBody(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	relay := newTestRAGSmokeChatRelay(t, server)
	request := httptest.NewRequest(http.MethodPost, ragSmokeRelayChatPath, bytes.NewReader(make([]byte, ragSmokeRelayMaxRequestBytes+1)))
	request.Header.Set("Authorization", "Bearer smoke-secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	relay.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestRAGSmokeChatRelaySanitizesProviderFailureAndRedirect(t *testing.T) {
	secret := "provider-error-must-not-leak"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/redirect" {
			http.Redirect(writer, request, "/target", http.StatusFound)
			return
		}
		http.Error(writer, secret, http.StatusInternalServerError)
	}))
	defer server.Close()
	relay := newTestRAGSmokeChatRelay(t, server)
	request := httptest.NewRequest(http.MethodPost, ragSmokeRelayChatPath, strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer smoke-secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	relay.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), server.URL) {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}

	relay.endpoint = server.URL + "/redirect"
	response = httptest.NewRecorder()
	relay.ServeHTTP(response, request.Clone(request.Context()))
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("redirect status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestNewRAGSmokeChatRelayRequiresHTTPSAndProductionTransport(t *testing.T) {
	if _, err := NewRAGSmokeChatRelay("http://127.0.0.1:11434"); err == nil {
		t.Fatal("loopback HTTP upstream was accepted")
	}
	relay, err := NewRAGSmokeChatRelay("https://chat.example.test/gateway/v1/")
	if err != nil {
		t.Fatal(err)
	}
	if relay.endpoint != "https://chat.example.test/gateway/v1/chat/completions" {
		t.Fatalf("endpoint=%q", relay.endpoint)
	}
	transport, ok := relay.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("transport=%#v", relay.client.Transport)
	}
	if err := relay.client.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Fatalf("redirect error=%v", err)
	}
}

func newTestRAGSmokeChatRelay(t *testing.T, server *httptest.Server) *RAGSmokeChatRelay {
	t.Helper()
	client := server.Client()
	endpoint, err := newRAGSmokeChatRelay(server.URL, client)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func relayEndpointHost(t *testing.T, server *httptest.Server) string {
	t.Helper()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint.Host
}

func TestRAGSmokeRelayTestTransportHasTLSMinimum(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	client := &http.Client{Transport: transport}
	if _, err := newRAGSmokeChatRelay(server.URL, client); err != nil {
		t.Fatal(err)
	}
}
