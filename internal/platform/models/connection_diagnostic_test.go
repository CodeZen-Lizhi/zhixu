package models_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
)

func TestChatProviderDiagnosticPreservesSafeOpenAIFields(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Request-ID", "req_chat_401")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"error":{"code":"invalid_api_key","type":"authentication_error","message":"Invalid API key","param":null}}`)
	}))
	defer server.Close()
	model, err := models.NewOpenAICompatibleChatModel(chatOptions(server.URL, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
	assertChatError(t, err, foundation.ErrorNonRetryableFailure, models.ErrorCodeChatUnauthorized, false)
	assertDiagnostic(t, err, models.ConnectionDiagnostic{
		Stage: models.ConnectionStageProviderResponse, ProviderHTTPStatus: http.StatusUnauthorized,
		ProviderErrorCode: "invalid_api_key", ProviderErrorType: "authentication_error",
		ProviderMessage: "Invalid API key", ProviderRequestID: "req_chat_401",
	})
}

func TestEmbeddingProviderDiagnosticPreservesDashScopeStatuses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status    int
		kind      foundation.ErrorKind
		code      string
		retryable bool
	}{
		{http.StatusBadRequest, foundation.ErrorNonRetryableFailure, models.ErrorCodeEmbeddingRejected, false},
		{http.StatusTooManyRequests, foundation.ErrorRetryableFailure, models.ErrorCodeEmbeddingRateLimited, true},
		{http.StatusInternalServerError, foundation.ErrorRetryableFailure, models.ErrorCodeEmbeddingUnavailable, true},
	}
	for _, test := range tests {
		test := test
		t.Run(fmt.Sprintf("status_%d", test.status), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, `{"code":"InvalidParameter","message":"model is unavailable","request_id":"req_embed"}`)
			}))
			defer server.Close()
			embedder, err := models.NewOpenAICompatibleEmbedder(openAIOptions(server.URL, server.Client(), time.Second, 0))
			if err != nil {
				t.Fatal(err)
			}
			_, err = embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"test"}})
			assertEmbeddingError(t, err, test.kind, test.code, test.retryable)
			assertDiagnostic(t, err, models.ConnectionDiagnostic{
				Stage: models.ConnectionStageProviderResponse, ProviderHTTPStatus: test.status,
				ProviderErrorCode: "InvalidParameter", ProviderMessage: "model is unavailable", ProviderRequestID: "req_embed",
			})
		})
	}
}

func TestProviderDiagnosticRejectsUnsafeOrUnsupportedBodies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		contentType string
		body        func(endpoint string) string
	}{
		{name: "non json", contentType: "text/html", body: func(string) string { return "<html>gateway-canary</html>" }},
		{name: "unknown json", contentType: "application/json", body: func(string) string { return `{"detail":"raw-body-canary"}` }},
		{name: "invalid unicode", contentType: "application/json", body: func(string) string { return string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}) }},
		{name: "oversized", contentType: "application/json", body: func(string) string { return `{"error":{"message":"` + strings.Repeat("x", 9<<10) + `"}}` }},
		{name: "credential and endpoint", contentType: "application/json", body: func(endpoint string) string {
			return fmt.Sprintf(`{"error":{"code":"credential-canary","type":"Bearer","message":"chat-secret-canary %s","param":null}}`, endpoint)
		}},
		{name: "unicode controls", contentType: "application/json", body: func(string) string {
			return `{"error":{"code":"code\u0085","type":"type\u202e","message":"message\u0085\u202e","param":null}}`
		}},
		{name: "unpaired surrogate", contentType: "application/json", body: func(string) string {
			return `{"error":{"code":"code\ud800","type":"type\ud800","message":"message\ud800","param":null}}`
		}},
		{name: "case folded credential and endpoint", contentType: "application/json", body: func(endpoint string) string {
			return fmt.Sprintf(`{"error":{"message":"CHAT-SECRET-CANARY %s","param":null}}`, strings.ToUpper(endpoint))
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, test.body(server.URL))
			}))
			defer server.Close()
			model, err := models.NewOpenAICompatibleChatModel(chatOptions(server.URL, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
			var diagnostic *models.ConnectionDiagnostic
			if !errors.As(err, &diagnostic) || diagnostic.ProviderHTTPStatus != http.StatusBadRequest || diagnostic.ProviderMessage != "响应详情无法安全解析" {
				t.Fatalf("diagnostic=%#v error=%v", diagnostic, err)
			}
			formatted := fmt.Sprintf("%v %#v %#v", err, err, diagnostic)
			for _, forbidden := range []string{"gateway-canary", "raw-body-canary", "credential-canary", "chat-secret-canary", "CHAT-SECRET-CANARY", server.URL, strings.ToUpper(server.URL)} {
				if strings.Contains(formatted, forbidden) {
					t.Fatalf("diagnostic leaked %q: %s", forbidden, formatted)
				}
			}
		})
	}
}

func TestProviderDiagnosticTokenMatchesPublicContract(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":{"code":"-invalid","type":".authentication","message":"safe message","param":null}}`)
	}))
	defer server.Close()
	model, err := models.NewOpenAICompatibleChatModel(chatOptions(server.URL, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
	var diagnostic *models.ConnectionDiagnostic
	if !errors.As(err, &diagnostic) {
		t.Fatalf("missing connection diagnostic: %v", err)
	}
	if diagnostic.ProviderErrorCode != "" || diagnostic.ProviderErrorType != "" || diagnostic.ProviderMessage != "safe message" {
		t.Fatalf("diagnostic=%#v", diagnostic)
	}
}

func TestChatTransportDiagnosticClassifiesDNSAndTLSEOFWithoutURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		cause     error
		wantStage models.ConnectionStage
		wantText  string
	}{
		{name: "dns", cause: &net.DNSError{Err: "no such host", Name: "private-endpoint.example"}, wantStage: models.ConnectionStageDNS, wantText: "DNS lookup failed"},
		{name: "connect", cause: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, wantStage: models.ConnectionStageConnect, wantText: "connection refused"},
		{name: "tls eof", cause: io.EOF, wantStage: models.ConnectionStageTLS, wantText: "EOF"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, test.cause })}
			model, err := models.NewOpenAICompatibleChatModel(chatOptions("https://private-endpoint.example", client))
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
			assertDiagnostic(t, err, models.ConnectionDiagnostic{Stage: test.wantStage, TransportError: test.wantText})
			if strings.Contains(fmt.Sprintf("%v %#v", err, err), "private-endpoint.example") {
				t.Fatal("transport diagnostic leaked endpoint")
			}
		})
	}
}

func TestChatResponseReadDiagnosticDoesNotExposeRawReadError(t *testing.T) {
	t.Parallel()
	for _, readErr := range []error{errors.New("read-secret-canary"), io.ErrUnexpectedEOF} {
		readErr := readErr
		t.Run(readErr.Error(), func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       errorReadCloser{err: readErr},
				}, nil
			})}
			model, err := models.NewOpenAICompatibleChatModel(chatOptions("https://private-endpoint.example", client))
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
			assertDiagnostic(t, err, models.ConnectionDiagnostic{Stage: models.ConnectionStageResponseRead, TransportError: "response read failed"})
			if strings.Contains(fmt.Sprintf("%v %#v", err, err), "read-secret-canary") {
				t.Fatal("response read diagnostic leaked raw error")
			}
		})
	}
}

func TestOpenAIEmbeddingVersionedBaseURLDoesNotDuplicateV1(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"/compatible-mode":               "/compatible-mode/v1/embeddings",
		"/compatible-mode/v1":            "/compatible-mode/v1/embeddings",
		"/compatible-mode/v1/embeddings": "/compatible-mode/v1/embeddings",
	}
	for basePath, wantPath := range tests {
		basePath, wantPath := basePath, wantPath
		t.Run(basePath, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != wantPath {
					t.Errorf("path=%q want=%q", request.URL.Path, wantPath)
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, openAIResponse(testEmbeddingModel, [][]float32{{1, 0, 0}}))
			}))
			defer server.Close()
			embedder, err := models.NewOpenAICompatibleEmbedder(openAIOptions(server.URL+basePath, server.Client(), time.Second, 0))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"test"}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertDiagnostic(t *testing.T, err error, want models.ConnectionDiagnostic) {
	t.Helper()
	var diagnostic *models.ConnectionDiagnostic
	if !errors.As(err, &diagnostic) {
		t.Fatalf("missing connection diagnostic: %T %v", err, err)
	}
	if diagnostic.Stage != want.Stage || diagnostic.ProviderHTTPStatus != want.ProviderHTTPStatus ||
		diagnostic.ProviderErrorCode != want.ProviderErrorCode || diagnostic.ProviderErrorType != want.ProviderErrorType ||
		diagnostic.ProviderMessage != want.ProviderMessage || diagnostic.ProviderRequestID != want.ProviderRequestID ||
		diagnostic.TransportError != want.TransportError || diagnostic.ValidationReason != want.ValidationReason {
		t.Fatalf("diagnostic=%#v want=%#v", diagnostic, want)
	}
}

type errorReadCloser struct{ err error }

func (reader errorReadCloser) Read([]byte) (int, error) { return 0, reader.err }
func (errorReadCloser) Close() error                    { return nil }
