package models_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestEinoOpenAIEmbeddingCallStateIsConcurrentRequestScoped(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var payload struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || len(payload.Input) != 2 {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		identifier, err := strconv.Atoi(strings.TrimPrefix(payload.Input[0], "request-"))
		if err != nil {
			http.Error(w, "invalid identifier", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"model":"embed-v1","data":[{"index":1,"embedding":[0,%d,1]},{"index":0,"embedding":[%d,1,0]}]}`, identifier+1, identifier+1)
	}))
	defer server.Close()
	embedder, err := models.NewEinoOpenAICompatibleEmbedder(openAIOptions(server.URL, server.Client(), time.Second, 0))
	if err != nil {
		t.Fatal(err)
	}

	const calls = 48
	failures := make(chan error, calls)
	var wait sync.WaitGroup
	for identifier := range calls {
		identifier := identifier
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, callErr := embedder.Embed(context.Background(), application.EmbedRequest{
				Inputs: []string{fmt.Sprintf("request-%d", identifier), "second"},
			})
			if callErr != nil {
				failures <- callErr
				return
			}
			want := float64(identifier+1) / math.Sqrt(float64((identifier+1)*(identifier+1)+1))
			if len(result.Embeddings) != 2 || math.Abs(float64(result.Embeddings[0][0])-want) > 1e-6 || result.Embeddings[1][0] != 0 {
				failures <- fmt.Errorf("request %d received another call state: %#v", identifier, result)
			}
		}()
	}
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Fatal(failure)
	}
}

func TestEinoOllamaEmbedderRejectsSDKImplicitAuthentication(t *testing.T) {
	t.Setenv("OLLAMA_AUTH", "true")
	options := ollamaOptions("http://127.0.0.1:11434", nil, time.Second, 0)
	embedder, err := models.NewEinoOllamaEmbedder(options)
	if embedder != nil {
		t.Fatalf("embedder=%T", embedder)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != models.ErrorCodeEmbeddingConfigInvalid {
		t.Fatalf("error=%v", err)
	}
}

func TestEinoOllamaEmbedderRejectsOllamaCloudImplicitAuthentication(t *testing.T) {
	t.Setenv("OLLAMA_AUTH", "false")
	options := ollamaOptions("https://ollama.com", nil, time.Second, 0)
	embedder, err := models.NewEinoOllamaEmbedder(options)
	if embedder != nil {
		t.Fatalf("embedder=%T", embedder)
	}
	assertEmbeddingError(t, err, foundation.ErrorInvalidInput, models.ErrorCodeEmbeddingConfigInvalid, false)
}

func TestEinoEmbeddingPreservesSuppliedHTTPClientTimeoutClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		newServer   func(http.Handler) (*httptest.Server, *http.Client)
		newEmbedder func(*httptest.Server, *http.Client) (application.Embedder, error)
	}{
		{
			name:      "openai-compatible",
			newServer: newTLSEmbeddingServer,
			newEmbedder: func(server *httptest.Server, client *http.Client) (application.Embedder, error) {
				return models.NewEinoOpenAICompatibleEmbedder(openAIOptions(server.URL, client, time.Second, 0))
			},
		},
		{
			name:      "ollama",
			newServer: newPlainEmbeddingServer,
			newEmbedder: func(server *httptest.Server, client *http.Client) (application.Embedder, error) {
				return models.NewEinoOllamaEmbedder(ollamaOptions(server.URL, client, time.Second, 0))
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server, client := test.newServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				time.Sleep(100 * time.Millisecond)
			}))
			defer server.Close()
			client.Timeout = 10 * time.Millisecond
			embedder, err := test.newEmbedder(server, client)
			if err != nil {
				t.Fatal(err)
			}
			if client.Timeout != 10*time.Millisecond {
				t.Fatalf("constructor mutated supplied client timeout: %s", client.Timeout)
			}
			_, err = embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"first"}})
			assertEmbeddingError(t, err, foundation.ErrorRetryableFailure, models.ErrorCodeEmbeddingTimeout, true)
		})
	}
}

func TestEinoEmbeddingSDKDecodeFailureAfterWireValidationIsConsistencyViolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		newServer   func(http.Handler) (*httptest.Server, *http.Client)
		newEmbedder func(*httptest.Server, *http.Client) (application.Embedder, error)
		body        string
	}{
		{
			name:      "openai-compatible",
			newServer: newTLSEmbeddingServer,
			newEmbedder: func(server *httptest.Server, client *http.Client) (application.Embedder, error) {
				return models.NewEinoOpenAICompatibleEmbedder(openAIOptions(server.URL, client, time.Second, 0))
			},
			body: `{"model":"embed-v1","data":[{"index":0,"embedding":[1,0,0]}],"usage":"invalid"}`,
		},
		{
			name:      "ollama",
			newServer: newPlainEmbeddingServer,
			newEmbedder: func(server *httptest.Server, client *http.Client) (application.Embedder, error) {
				return models.NewEinoOllamaEmbedder(ollamaOptions(server.URL, client, time.Second, 0))
			},
			body: `{"model":"embed-v1","embeddings":[[1,0,0]],"prompt_eval_count":"invalid"}`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server, client := test.newServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			embedder, err := test.newEmbedder(server, client)
			if err != nil {
				t.Fatal(err)
			}
			_, err = embedder.Embed(context.Background(), application.EmbedRequest{Inputs: []string{"first"}})
			assertEmbeddingError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbedResultInvalid, false)
		})
	}
}
