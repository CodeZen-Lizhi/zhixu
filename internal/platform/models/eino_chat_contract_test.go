package models_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
)

func TestEinoChatModelPreservesProjectRequestAndResponseContract(t *testing.T) {
	t.Parallel()
	var (
		lock   sync.Mutex
		bodies [][]byte
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/base/v1/chat/completions" || request.URL.RawQuery != "" {
			t.Errorf("request=%s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer chat-secret-canary" || request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Accept") != "application/json" {
			t.Errorf("headers=%v", request.Header)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			return
		}
		assertStrictChatRequest(t, body, `{"type":"object"}`)
		lock.Lock()
		bodies = append(bodies, append([]byte(nil), body...))
		lock.Unlock()
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(writer, "\n"+validChatResponse("chat-v1", `{"result":"ok"}`)+"\n")
	}))
	defer server.Close()

	options := chatOptions(server.URL+"/base", server.Client())
	eino, err := models.NewEinoOpenAIChatModel(options)
	if err != nil {
		t.Fatal(err)
	}
	response, err := eino.Chat(context.Background(), validChatRequest(eino.Contract().Model))
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Content) != `{"result":"ok"}` {
		t.Fatalf("response=%#v", response)
	}
	lock.Lock()
	defer lock.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("wire request count=%d", len(bodies))
	}
}

func TestEinoChatModelAcceptsKnownReasoningExtensionWithoutExposingIt(t *testing.T) {
	t.Parallel()
	const reasoningCanary = "private-reasoning-canary"
	tests := []struct {
		name  string
		field string
		value string
	}{
		{name: "reasoning string", field: "reasoning", value: `"` + reasoningCanary + `"`},
		{name: "reasoning null", field: "reasoning", value: "null"},
		{name: "reasoning content string", field: "reasoning_content", value: `"` + reasoningCanary + `"`},
		{name: "reasoning content null", field: "reasoning_content", value: "null"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(writer, `{"id":"call-1","object":"chat.completion","created":1,"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{\"result\":\"ok\"}",%q:%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, test.field, test.value)
			}))
			defer server.Close()

			options := chatOptions(server.URL, server.Client())
			eino, err := models.NewEinoOpenAIChatModel(options)
			if err != nil {
				t.Fatal(err)
			}
			response, err := eino.Chat(context.Background(), validChatRequest(eino.Contract().Model))
			if err != nil {
				t.Fatalf("%T: %v", eino, err)
			}
			if string(response.Content) != `{"result":"ok"}` || strings.Contains(fmt.Sprintf("%#v", response), reasoningCanary) {
				t.Fatalf("%T exposed reasoning extension: %#v", eino, response)
			}
		})
	}
}

func TestEinoChatModelRejectsInvalidReasoningExtensionTypes(t *testing.T) {
	t.Parallel()
	invalidValues := []struct {
		name  string
		value string
	}{
		{name: "object", value: `{}`},
		{name: "array", value: `[]`},
		{name: "number", value: `1`},
		{name: "boolean", value: `true`},
	}
	for _, field := range []string{"reasoning", "reasoning_content"} {
		field := field
		for _, invalid := range invalidValues {
			invalid := invalid
			t.Run(field+" "+invalid.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					writer.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(writer, `{"id":"call-1","object":"chat.completion","created":1,"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}",%q:%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, field, invalid.value)
				}))
				defer server.Close()

				options := chatOptions(server.URL, server.Client())
				eino, err := models.NewEinoOpenAIChatModel(options)
				if err != nil {
					t.Fatal(err)
				}
				_, err = eino.Chat(context.Background(), validChatRequest(eino.Contract().Model))
				assertChatError(t, err, foundation.ErrorConsistencyViolation, models.ErrorCodeChatResponseInvalid, false)
			})
		}
	}
}

func TestEinoOpenAIChatModelNormalizesSupportedBaseURLs(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/base/v1/chat/completions" {
			t.Errorf("path=%q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, validChatResponse("chat-v1", `{}`))
	}))
	defer server.Close()
	for _, baseURL := range []string{
		server.URL + "/base",
		server.URL + "/base/v1",
		server.URL + "/base/v1/chat/completions",
	} {
		baseURL := baseURL
		t.Run(strings.TrimPrefix(baseURL, server.URL), func(t *testing.T) {
			model, err := models.NewEinoOpenAIChatModel(chatOptions(baseURL, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.Chat(context.Background(), validChatRequest(model.Contract().Model)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEinoChatModelPreservesLegacyCompatibleModelIDs(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var payload chatWireRequest
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Error(err)
			return
		}
		if payload.Model != "text-davinci-003" {
			t.Errorf("wire model=%q", payload.Model)
		}
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, validChatResponse("chat-v1", `{}`))
	}))
	defer server.Close()

	options := chatOptions(server.URL, server.Client())
	options.Model = "text-davinci-003"
	eino, err := models.NewEinoOpenAIChatModel(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eino.Chat(context.Background(), validChatRequest(eino.Contract().Model)); err != nil {
		t.Fatalf("%T: %v", eino, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
}

func TestEinoOpenAIChatModelClassifiesProviderStatusesWithoutRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status    int
		kind      foundation.ErrorKind
		code      string
		retryable bool
	}{
		{http.StatusTooManyRequests, foundation.ErrorRetryableFailure, models.ErrorCodeChatRateLimited, true},
		{http.StatusBadGateway, foundation.ErrorRetryableFailure, models.ErrorCodeChatProviderUnavailable, true},
		{http.StatusServiceUnavailable, foundation.ErrorRetryableFailure, models.ErrorCodeChatProviderUnavailable, true},
		{http.StatusGatewayTimeout, foundation.ErrorRetryableFailure, models.ErrorCodeChatProviderUnavailable, true},
		{http.StatusUnauthorized, foundation.ErrorNonRetryableFailure, models.ErrorCodeChatUnauthorized, false},
		{http.StatusForbidden, foundation.ErrorNonRetryableFailure, models.ErrorCodeChatUnauthorized, false},
		{http.StatusBadRequest, foundation.ErrorNonRetryableFailure, models.ErrorCodeChatRejected, false},
		{http.StatusInternalServerError, foundation.ErrorNonRetryableFailure, models.ErrorCodeChatRejected, false},
	}
	for _, test := range tests {
		test := test
		t.Run(fmt.Sprintf("status-%d", test.status), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, `{"secret":"provider-error-body-canary"}`)
			}))
			defer server.Close()
			model, err := models.NewEinoOpenAIChatModel(chatOptions(server.URL, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
			assertChatError(t, err, test.kind, test.code, test.retryable)
			if calls.Load() != 1 {
				t.Fatalf("adapter retried status %d: calls=%d", test.status, calls.Load())
			}
			if strings.Contains(fmt.Sprintf("%v %#v", err, err), "provider-error-body-canary") {
				t.Fatal("provider error body leaked through classified error")
			}
		})
	}
}

func TestEinoOpenAIChatModelRejectsRedirectWithoutFollowing(t *testing.T) {
	t.Parallel()
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/credential-sink", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	model, err := models.NewEinoOpenAIChatModel(chatOptions(server.URL, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
	assertChatError(t, err, foundation.ErrorNonRetryableFailure, models.ErrorCodeChatRedirectRejected, false)
	if targetCalls.Load() != 0 {
		t.Fatal("Eino chat adapter followed a Provider redirect")
	}
}

func TestEinoOpenAIChatModelRejectsMalformedOrInconsistentResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		contentType string
		body        string
		maxBytes    int64
		code        string
	}{
		{name: "wrong content type", contentType: "text/plain", body: validChatResponse("chat-v1", `{}`), code: models.ErrorCodeChatResponseInvalid},
		{name: "malformed", contentType: "application/json", body: `{`, code: models.ErrorCodeChatResponseInvalid},
		{name: "invalid utf8", contentType: "application/json", body: string(append([]byte(`{"model":"chat-v1","secret":"response-secret-canary-`), 0xff, '"', '}')), code: models.ErrorCodeChatResponseInvalid},
		{name: "trailing json", contentType: "application/json", body: validChatResponse("chat-v1", `{}`) + `{}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "duplicate", contentType: "application/json", body: `{"model":"chat-v1","model":"chat-v1","choices":[],"usage":null}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "unknown field", contentType: "application/json", body: `{"model":"chat-v1","choices":[],"usage":null,"secret_unknown":true}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "unknown message field", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}","secret_unknown":true},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "invalid reasoning type", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}","reasoning":{}},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "missing usage", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}]}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "empty usage", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "empty choices", contentType: "application/json", body: `{"model":"chat-v1","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "multiple choices", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"},{"index":1,"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "wrong model", contentType: "application/json", body: validChatResponse("other-model", `{}`), code: models.ErrorCodeChatResponseModelMismatch},
		{name: "bad usage total", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":11}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "finish length", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}"},"finish_reason":"length"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "refusal", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}","refusal":"no"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "tool call", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}","tool_calls":[{"id":"call-1","type":"function","function":{"name":"SearchKnowledge","arguments":"{}"}}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "oversized", contentType: "application/json", body: validChatResponse("chat-v1", strings.Repeat("x", 1000)), maxBytes: 128, code: models.ErrorCodeChatResponseInvalid},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			options := chatOptions(server.URL, server.Client())
			if test.maxBytes != 0 {
				options.MaxResponseBytes = test.maxBytes
			}
			model, err := models.NewEinoOpenAIChatModel(options)
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
			assertChatError(t, err, foundation.ErrorConsistencyViolation, test.code, false)
		})
	}
}

func TestEinoOpenAIChatModelTimeoutCancellationAndRequestLimit(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		select {
		case <-request.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	}))
	defer server.Close()
	timeoutOptions := chatOptions(server.URL, server.Client())
	timeoutOptions.Timeout = 20 * time.Millisecond
	timeoutModel, err := models.NewEinoOpenAIChatModel(timeoutOptions)
	if err != nil {
		t.Fatal(err)
	}
	_, err = timeoutModel.Chat(context.Background(), validChatRequest(timeoutModel.Contract().Model))
	assertChatError(t, err, foundation.ErrorRetryableFailure, models.ErrorCodeChatTimeout, true)

	cancelModel, err := models.NewEinoOpenAIChatModel(chatOptions(server.URL, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = cancelModel.Chat(ctx, validChatRequest(cancelModel.Contract().Model))
	assertChatError(t, err, foundation.ErrorNonRetryableFailure, models.ErrorCodeChatCancelled, false)

	requestOptions := chatOptions(server.URL, server.Client())
	requestOptions.MaxRequestBytes = 128
	requestModel, err := models.NewEinoOpenAIChatModel(requestOptions)
	if err != nil {
		t.Fatal(err)
	}
	request := validChatRequest(requestModel.Contract().Model)
	request.Messages[0].Content = strings.Repeat("x", 256)
	before := calls.Load()
	_, err = requestModel.Chat(context.Background(), request)
	assertChatError(t, err, foundation.ErrorInvalidInput, models.ErrorCodeChatRequestInvalid, false)
	if calls.Load() != before {
		t.Fatal("oversized Eino request reached Provider")
	}
}

func TestEinoOpenAIChatModelKeepsDynamicSchemasRequestScoped(t *testing.T) {
	t.Parallel()
	var providerCalls atomic.Int32
	serverErrors := make(chan error, 32)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		body, err := io.ReadAll(request.Body)
		if err != nil {
			serverErrors <- err
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload chatWireRequest
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			serverErrors <- err
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		marker := payload.Messages[1].Content
		wantSchema := dynamicSchema(marker)
		if string(payload.ResponseFormat.JSONSchema.Schema) != string(wantSchema) {
			serverErrors <- fmt.Errorf("marker=%s schema=%s", marker, payload.ResponseFormat.JSONSchema.Schema)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, validChatResponse("chat-v1", fmt.Sprintf(`{"marker":%q}`, marker)))
	}))
	defer server.Close()
	model, err := models.NewEinoOpenAIChatModel(chatOptions(server.URL, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	callErrors := make(chan error, 32)
	phases := []agentdomain.ModelCallPhase{agentdomain.ModelCallInitial, agentdomain.ModelCallRepair, agentdomain.ModelCallReduced}
	for index := range 32 {
		marker := fmt.Sprintf("schema-%02d", index)
		phase := phases[index%len(phases)]
		wait.Add(1)
		go func() {
			defer wait.Done()
			request := validChatRequest(model.Contract().Model)
			request.Phase = phase
			request.SchemaRef = agentdomain.SchemaRef{ID: marker, Version: "v1"}
			request.Messages[1].Content = marker
			request.OutputSchema = dynamicSchema(marker)
			response, err := model.Chat(context.Background(), request)
			if err != nil {
				callErrors <- err
				return
			}
			if string(response.Content) != fmt.Sprintf(`{"marker":%q}`, marker) {
				callErrors <- fmt.Errorf("marker=%s response=%s", marker, response.Content)
			}
		}()
	}
	wait.Wait()
	close(callErrors)
	close(serverErrors)
	for err := range callErrors {
		t.Error(err)
	}
	for err := range serverErrors {
		t.Error(err)
	}
	if providerCalls.Load() != 32 {
		t.Fatalf("provider calls=%d", providerCalls.Load())
	}
}

func TestEinoOpenAIChatModelBoundsAndRedactsProviderErrorBody(t *testing.T) {
	t.Parallel()
	body := &countingReadCloser{reader: strings.NewReader(strings.Repeat("provider-error-secret-canary", 1024))}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})}
	model, err := models.NewEinoOpenAIChatModel(chatOptions("https://private-endpoint.example", client))
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
	assertChatError(t, err, foundation.ErrorNonRetryableFailure, models.ErrorCodeChatRejected, false)
	if body.bytes.Load() > 4096 || !body.closed.Load() {
		t.Fatalf("error body bytes=%d closed=%t", body.bytes.Load(), body.closed.Load())
	}
	formatted := fmt.Sprintf("%v %#v", err, err)
	for _, private := range []string{"provider-error-secret-canary", "private-endpoint.example", "chat-secret-canary"} {
		if strings.Contains(formatted, private) {
			t.Fatalf("classified error leaked %q", private)
		}
	}
}

func TestEinoOpenAIChatModelRedactsUnknownTransportFailure(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport-secret-canary https://private-endpoint.example prompt-secret-canary")
	})}
	model, err := models.NewEinoOpenAIChatModel(chatOptions("https://private-endpoint.example", client))
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
	assertChatError(t, err, foundation.ErrorNonRetryableFailure, models.ErrorCodeChatRequestFailed, false)
	formatted := fmt.Sprintf("%v %#v", err, err)
	for _, private := range []string{"transport-secret-canary", "private-endpoint.example", "prompt-secret-canary"} {
		if strings.Contains(formatted, private) {
			t.Fatalf("transport error leaked %q", private)
		}
	}
}

type chatWireRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	MaxTokens      int      `json:"max_tokens"`
	Temperature    *float64 `json:"temperature"`
	ResponseFormat struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name   string          `json:"name"`
			Strict bool            `json:"strict"`
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	} `json:"response_format"`
}

func assertStrictChatRequest(t *testing.T, body []byte, wantSchema string) {
	t.Helper()
	var payload chatWireRequest
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		t.Errorf("decode request: %v", err)
		return
	}
	if payload.Model != "chat-alias" || payload.MaxTokens != 256 || payload.Temperature == nil || *payload.Temperature != 0 || len(payload.Messages) != 2 ||
		payload.Messages[0].Role != "system" || payload.Messages[0].Content != "system-policy-canary" ||
		payload.ResponseFormat.Type != "json_schema" || !payload.ResponseFormat.JSONSchema.Strict ||
		!strings.HasPrefix(payload.ResponseFormat.JSONSchema.Name, "zhixu_") || string(payload.ResponseFormat.JSONSchema.Schema) != wantSchema {
		t.Errorf("payload=%s", body)
	}
}

func dynamicSchema(marker string) []byte {
	return []byte(fmt.Sprintf(`{"type":"object","properties":{"marker":{"type":"string","const":%q}},"required":["marker"],"additionalProperties":false}`, marker))
}

type countingReadCloser struct {
	reader *strings.Reader
	bytes  atomic.Int64
	closed atomic.Bool
}

func (body *countingReadCloser) Read(buffer []byte) (int, error) {
	count, err := body.reader.Read(buffer)
	body.bytes.Add(int64(count))
	return count, err
}

func (body *countingReadCloser) Close() error {
	body.closed.Store(true)
	return nil
}
