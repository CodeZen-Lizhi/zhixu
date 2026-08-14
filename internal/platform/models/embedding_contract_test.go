package models_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	testEmbeddingModel = "embed-v1"
	testEmbeddingKey   = "embedding-secret-key"
)

type embeddingProviderContract struct {
	name        string
	newServer   func(http.Handler) (*httptest.Server, *http.Client)
	newEmbedder func(t *testing.T, server *httptest.Server, client *http.Client, timeout time.Duration, maxResponseBytes int64) application.Embedder
	response    func(model string, embeddings [][]float32) string
}

func TestEinoEmbeddingProvidersShareProjectContract(t *testing.T) {
	t.Parallel()
	for _, provider := range embeddingProviderContracts() {
		provider := provider
		t.Run(provider.name, func(t *testing.T) {
			t.Parallel()
			t.Run("status classification", func(t *testing.T) {
				tests := []struct {
					status    int
					kind      foundation.ErrorKind
					code      string
					retryable bool
				}{
					{http.StatusTooManyRequests, foundation.ErrorRetryableFailure, models.ErrorCodeEmbeddingRateLimited, true},
					{http.StatusRequestTimeout, foundation.ErrorRetryableFailure, models.ErrorCodeEmbeddingTimeout, true},
					{http.StatusBadGateway, foundation.ErrorRetryableFailure, models.ErrorCodeEmbeddingUnavailable, true},
					{http.StatusUnauthorized, foundation.ErrorNonRetryableFailure, models.ErrorCodeEmbeddingUnauthorized, false},
					{http.StatusBadRequest, foundation.ErrorNonRetryableFailure, models.ErrorCodeEmbeddingRejected, false},
				}
				for _, test := range tests {
					test := test
					t.Run(fmt.Sprintf("status_%d", test.status), func(t *testing.T) {
						server, client := provider.newServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
							w.WriteHeader(test.status)
							_, _ = w.Write([]byte(`{"error":"must-not-leak"}`))
						}))
						defer server.Close()
						embedder := provider.newEmbedder(t, server, client, time.Second, 0)
						_, err := embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"first"}})
						assertEmbeddingError(t, err, test.kind, test.code, test.retryable)
						assertErrorExcludes(t, err, "must-not-leak", testEmbeddingKey, server.URL)
					})
				}
			})

			t.Run("contract and request limits", func(t *testing.T) {
				var calls atomic.Int32
				server, client := provider.newServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					calls.Add(1)
				}))
				defer server.Close()
				embedder := provider.newEmbedder(t, server, client, time.Second, 0)
				contract := embedder.Contract()
				if contract.Model != testEmbeddingModel || contract.Dimensions != 3 || contract.Normalization != domain.NormalizationL2 ||
					contract.DistanceMetric != domain.DistanceCosine || contract.MaxBatchSize != 4 || contract.MaxInputBytes != 1024 {
					t.Fatalf("contract=%#v", contract)
				}
				if err := domain.ValidateEmbeddingContract(contract); err != nil {
					t.Fatal(err)
				}
				for _, request := range []application.EmbedRequest{
					{},
					{Inputs: []string{strings.Repeat("x", 1025)}},
					{Inputs: []string{"1", "2", "3", "4", "5"}},
				} {
					_, err := embedder.Embed(context.Background(), request)
					assertEmbeddingError(t, err, foundation.ErrorInvalidInput, domain.ErrorCodeEmbedRequestInvalid, false)
				}
				if calls.Load() != 0 {
					t.Fatalf("invalid requests reached provider: calls=%d", calls.Load())
				}
			})

			t.Run("invalid provider responses fail closed", func(t *testing.T) {
				tests := []struct {
					name        string
					contentType string
					body        string
				}{
					{"invalid json", "application/json", `{not-json`},
					{"extra json", "application/json", provider.response(testEmbeddingModel, [][]float32{{1, 0, 0}}) + `{}`},
					{"wrong content type", "text/plain", provider.response(testEmbeddingModel, [][]float32{{1, 0, 0}})},
					{"wrong model", "application/json", provider.response("other-model", [][]float32{{1, 0, 0}})},
					{"wrong dimensions", "application/json", provider.response(testEmbeddingModel, [][]float32{{1, 0}})},
					{"zero norm", "application/json", provider.response(testEmbeddingModel, [][]float32{{0, 0, 0}})},
					{"non finite", "application/json", strings.Replace(provider.response(testEmbeddingModel, [][]float32{{1, 0, 0}}), "[1,0,0]", "[1e999,0,0]", 1)},
				}
				for _, test := range tests {
					test := test
					t.Run(test.name, func(t *testing.T) {
						server, client := provider.newServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
							w.Header().Set("Content-Type", test.contentType)
							_, _ = w.Write([]byte(test.body))
						}))
						defer server.Close()
						embedder := provider.newEmbedder(t, server, client, time.Second, 0)
						_, err := embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"first"}})
						assertEmbeddingError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbedResultInvalid, false)
					})
				}
			})

			t.Run("response byte limit", func(t *testing.T) {
				server, client := provider.newServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(strings.Repeat("x", 257)))
				}))
				defer server.Close()
				embedder := provider.newEmbedder(t, server, client, time.Second, 256)
				_, err := embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"first"}})
				assertEmbeddingError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbedResultInvalid, false)
			})

			t.Run("timeout and cancellation", func(t *testing.T) {
				server, client := provider.newServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
					select {
					case <-request.Context().Done():
					case <-time.After(250 * time.Millisecond):
					}
				}))
				defer func() {
					server.CloseClientConnections()
					server.Close()
				}()
				embedder := provider.newEmbedder(t, server, client, 20*time.Millisecond, 0)
				_, err := embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"first"}})
				assertEmbeddingError(t, err, foundation.ErrorRetryableFailure, models.ErrorCodeEmbeddingTimeout, true)
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("timeout error chain = %v", err)
				}

				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				_, err = embedder.Embed(ctx, application.EmbedRequest{Inputs: []string{"first"}})
				assertEmbeddingError(t, err, foundation.ErrorNonRetryableFailure, models.ErrorCodeEmbeddingCancelled, false)
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error chain = %v", err)
				}
			})

			t.Run("network failure is retryable", func(t *testing.T) {
				server, client := provider.newServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				embedder := provider.newEmbedder(t, server, client, time.Second, 0)
				server.Close()
				_, err := embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"first"}})
				assertEmbeddingError(t, err, foundation.ErrorRetryableFailure, models.ErrorCodeEmbeddingRequestFailed, true)
				assertErrorExcludes(t, err, server.URL, testEmbeddingKey, "first")
			})

			t.Run("redirect is not followed", func(t *testing.T) {
				var redirected atomic.Bool
				target, _ := provider.newServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					redirected.Store(true)
				}))
				defer target.Close()
				server, client := provider.newServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					http.Redirect(w, request, target.URL, http.StatusTemporaryRedirect)
				}))
				defer server.Close()
				embedder := provider.newEmbedder(t, server, client, time.Second, 0)
				_, err := embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"first"}})
				assertEmbeddingError(t, err, foundation.ErrorNonRetryableFailure, models.ErrorCodeEmbeddingRejected, false)
				if redirected.Load() {
					t.Fatal("embedding adapter followed a redirect")
				}
			})
		})
	}
}

func embeddingProviderContracts() []embeddingProviderContract {
	return []embeddingProviderContract{
		{
			name:      "openai-compatible",
			newServer: newTLSEmbeddingServer,
			newEmbedder: func(t *testing.T, server *httptest.Server, client *http.Client, timeout time.Duration, maxResponseBytes int64) application.Embedder {
				t.Helper()
				embedder, err := models.NewEinoOpenAICompatibleEmbedder(openAIOptions(server.URL, client, timeout, maxResponseBytes))
				if err != nil {
					t.Fatal(err)
				}
				return embedder
			},
			response: openAIResponse,
		},
		{
			name:      "ollama",
			newServer: newPlainEmbeddingServer,
			newEmbedder: func(t *testing.T, server *httptest.Server, client *http.Client, timeout time.Duration, maxResponseBytes int64) application.Embedder {
				t.Helper()
				embedder, err := models.NewEinoOllamaEmbedder(ollamaOptions(server.URL, client, timeout, maxResponseBytes))
				if err != nil {
					t.Fatal(err)
				}
				return embedder
			},
			response: ollamaResponse,
		},
	}
}

func TestOpenAICompatibleEmbedderBatchIndexAndFloatContract(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/gateway/v1/embeddings" {
			t.Errorf("request=%s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+testEmbeddingKey || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers=%v", request.Header)
		}
		var payload struct {
			Input          []string `json:"input"`
			Model          string   `json:"model"`
			EncodingFormat string   `json:"encoding_format"`
			Dimensions     int32    `json:"dimensions"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if fmt.Sprint(payload.Input) != "[first second]" || payload.Model != testEmbeddingModel || payload.EncodingFormat != "float" || payload.Dimensions != 3 {
			t.Errorf("payload=%#v", payload)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"object":"list","unknown":true,"model":"embed-v1","data":[{"index":1,"embedding":[0,0,2]},{"index":0,"embedding":[3,4,0]}]}`))
	}))
	defer server.Close()
	embedder, err := models.NewEinoOpenAICompatibleEmbedder(openAIOptions(server.URL+"/gateway/", server.Client(), time.Second, 0))
	if err != nil {
		t.Fatal(err)
	}
	result, err := embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"first", "second"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != testEmbeddingModel || len(result.Embeddings) != 2 || math.Abs(float64(result.Embeddings[0][0])-0.6) > 1e-6 || result.Embeddings[1][2] != 1 {
		t.Fatalf("result=%#v", result)
	}
}

func TestOpenAICompatibleEmbedderRejectsMissingDuplicateAndOutOfRangeIndex(t *testing.T) {
	t.Parallel()
	responses := []string{
		`{"model":"embed-v1","data":[{"embedding":[1,0,0]}]}`,
		`{"model":"embed-v1","data":[{"index":0,"embedding":[1,0,0]},{"index":0,"embedding":[0,1,0]}]}`,
		`{"model":"embed-v1","data":[{"index":1,"embedding":[1,0,0]}]}`,
	}
	for _, body := range responses {
		body := body
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		embedder, err := models.NewEinoOpenAICompatibleEmbedder(openAIOptions(server.URL, server.Client(), time.Second, 0))
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		_, err = embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"first"}})
		server.Close()
		assertEmbeddingError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbedResultInvalid, false)
	}
}

func TestOllamaEmbedderBatchOrderAndTruncateContract(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/embed" {
			t.Errorf("request=%s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "" {
			t.Errorf("unexpected authorization header")
		}
		var payload struct {
			Input    []string `json:"input"`
			Model    string   `json:"model"`
			Truncate *bool    `json:"truncate"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if fmt.Sprint(payload.Input) != "[first second]" || payload.Model != testEmbeddingModel || payload.Truncate == nil || *payload.Truncate {
			t.Errorf("payload=%#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"embed-v1","embeddings":[[3,4,0],[0,0,2]],"prompt_eval_count":2}`))
	}))
	defer server.Close()
	embedder, err := models.NewEinoOllamaEmbedder(ollamaOptions(server.URL, server.Client(), time.Second, 0))
	if err != nil {
		t.Fatal(err)
	}
	result, err := embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"first", "second"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Embeddings) != 2 || math.Abs(float64(result.Embeddings[0][0])-0.6) > 1e-6 || result.Embeddings[1][2] != 1 {
		t.Fatalf("result=%#v", result)
	}
}

func TestEmbeddingAdapterURLAndSensitiveInformationBoundary(t *testing.T) {
	t.Parallel()
	invalidOpenAI := []string{
		"http://127.0.0.1:8080",
		"https://user:url-secret@example.test",
		"https://example.test?query=url-secret",
		"https://example.test/#url-secret",
		" https://example.test",
	}
	for _, endpoint := range invalidOpenAI {
		options := openAIOptions(endpoint, nil, time.Second, 0)
		_, err := models.NewEinoOpenAICompatibleEmbedder(options)
		assertEmbeddingError(t, err, foundation.ErrorInvalidInput, models.ErrorCodeEmbeddingConfigInvalid, false)
		assertErrorExcludes(t, err, "url-secret", endpoint, testEmbeddingKey)
	}
	invalidOllama := []string{
		"http://example.test",
		"http://localhost:11434?query=url-secret",
		"http://user:url-secret@localhost:11434",
		"file:///tmp/ollama",
	}
	for _, endpoint := range invalidOllama {
		options := ollamaOptions(endpoint, nil, time.Second, 0)
		_, err := models.NewEinoOllamaEmbedder(options)
		assertEmbeddingError(t, err, foundation.ErrorInvalidInput, models.ErrorCodeEmbeddingConfigInvalid, false)
		assertErrorExcludes(t, err, "url-secret", endpoint)
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"credential":"embedding-secret-key","input":"sensitive input"}`))
	}))
	defer server.Close()
	embedder, err := models.NewEinoOpenAICompatibleEmbedder(openAIOptions(server.URL, server.Client(), time.Second, 0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"sensitive input"}})
	assertEmbeddingError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbedResultInvalid, false)
	assertErrorExcludes(t, err, testEmbeddingKey, "sensitive input", server.URL)
	for _, formatted := range []string{fmt.Sprint(embedder), fmt.Sprintf("%#v", embedder)} {
		if strings.Contains(formatted, testEmbeddingKey) || strings.Contains(formatted, server.URL) || strings.Contains(formatted, "sensitive input") {
			t.Fatalf("adapter formatting leaked sensitive data: %q", formatted)
		}
	}
	contract := embedder.Contract()
	if contract.ConfigHash == "" || strings.Contains(fmt.Sprintf("%#v", contract), testEmbeddingKey) {
		t.Fatalf("contract contains credential: %#v", contract)
	}
}

func newTLSEmbeddingServer(handler http.Handler) (*httptest.Server, *http.Client) {
	server := httptest.NewTLSServer(handler)
	return server, server.Client()
}

func newPlainEmbeddingServer(handler http.Handler) (*httptest.Server, *http.Client) {
	server := httptest.NewServer(handler)
	return server, server.Client()
}

func openAIOptions(baseURL string, client *http.Client, timeout time.Duration, maxResponseBytes int64) models.OpenAIEmbeddingOptions {
	return models.OpenAIEmbeddingOptions{
		Client: client, BaseURL: baseURL, APIKey: testEmbeddingKey, Model: testEmbeddingModel,
		Dimensions: 3, Normalization: domain.NormalizationL2, DistanceMetric: domain.DistanceCosine,
		MaxBatchSize: 4, MaxInputBytes: 1024, MaxBatchInputBytes: 4096, Timeout: timeout, MaxResponseBytes: maxResponseBytes,
	}
}

func ollamaOptions(baseURL string, client *http.Client, timeout time.Duration, maxResponseBytes int64) models.OllamaEmbeddingOptions {
	return models.OllamaEmbeddingOptions{
		Client: client, BaseURL: baseURL, Model: testEmbeddingModel,
		Dimensions: 3, Normalization: domain.NormalizationL2, DistanceMetric: domain.DistanceCosine,
		MaxBatchSize: 4, MaxInputBytes: 1024, MaxBatchInputBytes: 4096, Timeout: timeout, MaxResponseBytes: maxResponseBytes,
	}
}

func openAIResponse(model string, embeddings [][]float32) string {
	data := make([]map[string]any, len(embeddings))
	for index, embedding := range embeddings {
		data[index] = map[string]any{"index": index, "embedding": embedding}
	}
	encoded, _ := json.Marshal(map[string]any{"model": model, "data": data})
	return string(encoded)
}

func ollamaResponse(model string, embeddings [][]float32) string {
	encoded, _ := json.Marshal(map[string]any{"model": model, "embeddings": embeddings})
	return string(encoded)
}

func assertEmbeddingError(t *testing.T, err error, kind foundation.ErrorKind, code string, retryable bool) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error kind=%q code=%q", kind, code)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error=%T %v", err, err)
	}
	if classified.Kind != kind || classified.Code != code || classified.Retryable != retryable {
		t.Fatalf("error=%#v want kind=%q code=%q retryable=%v", classified, kind, code, retryable)
	}
}

func assertErrorExcludes(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	formatted := fmt.Sprintf("%v %#v", err, err)
	for _, value := range forbidden {
		if value != "" && strings.Contains(formatted, value) {
			t.Fatalf("error leaked %q: %q", value, formatted)
		}
	}
}
