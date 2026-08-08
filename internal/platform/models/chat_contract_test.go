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
	"sync/atomic"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
)

func TestOpenAICompatibleChatModelContractSuccess(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
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
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			MaxTokens      int `json:"max_tokens"`
			ResponseFormat struct {
				Type       string `json:"type"`
				JSONSchema struct {
					Name   string          `json:"name"`
					Strict bool            `json:"strict"`
					Schema json.RawMessage `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if payload.Model != "chat-alias" || payload.MaxTokens != 256 || len(payload.Messages) != 2 ||
			payload.Messages[0].Role != "system" || payload.Messages[0].Content != "system-policy-canary" ||
			payload.ResponseFormat.Type != "json_schema" || !payload.ResponseFormat.JSONSchema.Strict ||
			!strings.HasPrefix(payload.ResponseFormat.JSONSchema.Name, "zhixu_") ||
			string(payload.ResponseFormat.JSONSchema.Schema) != `{"type":"object"}` {
			t.Errorf("payload=%s", body)
		}
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(writer, validChatResponse("chat-v1", `{"result":"ok"}`))
	}))
	defer server.Close()

	model, err := models.NewOpenAICompatibleChatModel(chatOptions(server.URL+"/base", server.Client()))
	if err != nil {
		t.Fatalf("%v cause=%v", err, errors.Unwrap(err))
	}
	request := validChatRequest(model.Contract().Model)
	response, err := model.Chat(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || response.Model != model.Contract().Model || string(response.Content) != `{"result":"ok"}` ||
		response.Usage != (agentdomain.TokenUsage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}) {
		t.Fatalf("calls=%d response=%#v", calls.Load(), response)
	}
	contract := model.Contract()
	if contract.Provider != "openai-compatible" || contract.Model.AdapterName != "openai-compatible-http" ||
		contract.Model.AdapterVersion != "v7" || contract.Model.ModelID != "chat-alias" || contract.Model.ModelVersion != "chat-v1" || contract.Timeout != time.Second ||
		contract.MaxRequestBytes != 1<<20 || contract.MaxResponseBytes != 1<<20 {
		t.Fatalf("contract=%#v", contract)
	}
}

func TestOpenAICompatibleChatModelSupportsOllamaCompatibleVersionedBaseURL(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/ollama/v1/chat/completions" || request.Header.Get("Authorization") != "" {
			t.Errorf("path=%s authorization=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, validChatResponse("chat-v1", `{}`))
	}))
	defer server.Close()
	options := chatOptions(server.URL+"/ollama/v1", server.Client())
	options.APIKey = ""
	model, err := models.NewOpenAICompatibleChatModel(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Chat(context.Background(), validChatRequest(model.Contract().Model)); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAICompatibleChatModelClassifiesProviderStatusesWithoutRetry(t *testing.T) {
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
			model, err := models.NewOpenAICompatibleChatModel(chatOptions(server.URL, server.Client()))
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

func TestOpenAICompatibleChatModelRejectsRedirectWithoutFollowing(t *testing.T) {
	t.Parallel()
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/credential-sink", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	model, err := models.NewOpenAICompatibleChatModel(chatOptions(server.URL, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
	assertChatError(t, err, foundation.ErrorNonRetryableFailure, models.ErrorCodeChatRedirectRejected, false)
	if targetCalls.Load() != 0 {
		t.Fatal("chat adapter followed a provider redirect")
	}
}

func TestOpenAICompatibleChatModelTimeoutAndCancellation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	}))
	defer server.Close()

	timeoutOptions := chatOptions(server.URL, server.Client())
	timeoutOptions.Timeout = 20 * time.Millisecond
	timeoutModel, err := models.NewOpenAICompatibleChatModel(timeoutOptions)
	if err != nil {
		t.Fatal(err)
	}
	_, err = timeoutModel.Chat(context.Background(), validChatRequest(timeoutModel.Contract().Model))
	assertChatError(t, err, foundation.ErrorRetryableFailure, models.ErrorCodeChatTimeout, true)

	cancelModel, err := models.NewOpenAICompatibleChatModel(chatOptions(server.URL, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = cancelModel.Chat(ctx, validChatRequest(cancelModel.Contract().Model))
	assertChatError(t, err, foundation.ErrorNonRetryableFailure, models.ErrorCodeChatCancelled, false)
}

func TestOpenAICompatibleChatModelRejectsMalformedOrInconsistentResponses(t *testing.T) {
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
		{name: "trailing", contentType: "application/json", body: validChatResponse("chat-v1", `{}`) + `{}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "duplicate", contentType: "application/json", body: `{"model":"chat-v1","model":"chat-v1","choices":[],"usage":null}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "unknown field", contentType: "application/json", body: `{"model":"chat-v1","choices":[],"usage":null,"secret_unknown":true}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "unknown message field", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}","secret_unknown":true},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "invalid reasoning type", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}","reasoning":{}},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "wrong model", contentType: "application/json", body: validChatResponse("other-model", `{}`), code: models.ErrorCodeChatResponseModelMismatch},
		{name: "bad usage", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":11}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "empty usage", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "multiple choices", contentType: "application/json", body: `{"model":"chat-v1","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`, code: models.ErrorCodeChatResponseInvalid},
		{name: "truncated finish", contentType: "application/json", body: `{"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{}"},"finish_reason":"length"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`, code: models.ErrorCodeChatResponseInvalid},
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
			model, err := models.NewOpenAICompatibleChatModel(options)
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
			assertChatError(t, err, foundation.ErrorConsistencyViolation, test.code, false)
			if strings.Contains(test.body, "response-secret-canary") && strings.Contains(fmt.Sprintf("%v %#v", err, err), "response-secret-canary") {
				t.Fatal("invalid provider response leaked through classified error")
			}
		})
	}
}

func TestOpenAICompatibleChatModelRejectsProviderNativeToolCalls(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"call-1","object":"chat.completion","created":1,"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"{\"schema_version\":1,\"tool_name\":\"SearchKnowledge\",\"arguments\":{},\"reason\":\"need evidence\"}","refusal":null,"tool_calls":[{"id":"provider-call","type":"function","function":{"name":"SearchKnowledge","arguments":"{}"}}]},"finish_reason":"stop","logprobs":null}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"prompt_tokens_details":null,"completion_tokens_details":null},"system_fingerprint":null,"service_tier":null}`)
	}))
	defer server.Close()
	model, err := models.NewOpenAICompatibleChatModel(chatOptions(server.URL, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
	assertChatError(t, err, foundation.ErrorConsistencyViolation, models.ErrorCodeChatResponseInvalid, false)
}

func TestOpenAICompatibleChatModelClassifiesUnknownTransportFailureAsNonRetryableAndRedactsCause(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport-secret-canary https://private-endpoint.example prompt-secret-canary")
	})}
	options := chatOptions("https://private-endpoint.example", client)
	model, err := models.NewOpenAICompatibleChatModel(options)
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

func TestOpenAICompatibleChatModelRejectsInvalidOrOversizedRequestBeforeNetwork(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	options := chatOptions(server.URL, server.Client())
	options.MaxRequestBytes = 128
	model, err := models.NewOpenAICompatibleChatModel(options)
	if err != nil {
		t.Fatal(err)
	}
	request := validChatRequest(model.Contract().Model)
	request.Messages[0].Content = "prompt-secret-canary" + strings.Repeat("x", 256)
	_, err = model.Chat(context.Background(), request)
	assertChatError(t, err, foundation.ErrorInvalidInput, models.ErrorCodeChatRequestInvalid, false)
	if strings.Contains(fmt.Sprintf("%v %#v", err, err), "prompt-secret-canary") {
		t.Fatal("oversized request error leaked prompt content")
	}

	request = validChatRequest(agentdomain.ModelRef{AdapterName: "different", AdapterVersion: "v7", ModelID: "chat-v1", ModelVersion: "chat-v1"})
	_, err = model.Chat(context.Background(), request)
	assertChatError(t, err, foundation.ErrorConsistencyViolation, models.ErrorCodeChatRequestInvalid, false)

	request = validChatRequest(model.Contract().Model)
	request.OutputSchema = []byte(`{"type":"object","type":"array"}`)
	_, err = model.Chat(context.Background(), request)
	assertChatError(t, err, foundation.ErrorInvalidInput, "AGENT_CHAT_REQUEST_INVALID", false)
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached provider: calls=%d", calls.Load())
	}
}

func TestOpenAICompatibleChatModelRejectsUnsafeConfigAndDoesNotLeakPrivateData(t *testing.T) {
	t.Parallel()
	base := chatOptions("https://models.example.test/private-endpoint-canary", nil)
	tests := []struct {
		name   string
		change func(*models.OpenAIChatOptions)
	}{
		{name: "remote http", change: func(options *models.OpenAIChatOptions) { options.BaseURL = "http://models.example.test" }},
		{name: "userinfo", change: func(options *models.OpenAIChatOptions) { options.BaseURL = "https://user:secret@models.example.test" }},
		{name: "query", change: func(options *models.OpenAIChatOptions) { options.BaseURL = "https://models.example.test?secret=1" }},
		{name: "fragment", change: func(options *models.OpenAIChatOptions) { options.BaseURL = "https://models.example.test#secret" }},
		{name: "missing model", change: func(options *models.OpenAIChatOptions) { options.Model = "" }},
		{name: "missing model version", change: func(options *models.OpenAIChatOptions) { options.ModelVersion = "" }},
		{name: "missing version", change: func(options *models.OpenAIChatOptions) { options.AdapterVersion = "" }},
		{name: "bad key", change: func(options *models.OpenAIChatOptions) { options.APIKey = "secret\nheader" }},
		{name: "zero timeout", change: func(options *models.OpenAIChatOptions) { options.Timeout = 0 }},
		{name: "request limit", change: func(options *models.OpenAIChatOptions) { options.MaxRequestBytes = 16<<20 + 1 }},
		{name: "response limit", change: func(options *models.OpenAIChatOptions) { options.MaxResponseBytes = 16<<20 + 1 }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := base
			test.change(&options)
			model, err := models.NewOpenAICompatibleChatModel(options)
			if model != nil {
				t.Fatal("unsafe config constructed a model")
			}
			assertChatError(t, err, foundation.ErrorInvalidInput, models.ErrorCodeChatConfigInvalid, false)
			formatted := fmt.Sprintf("%v %#v", err, err)
			for _, private := range []string{options.APIKey, options.BaseURL} {
				if private != "" && strings.Contains(formatted, private) {
					t.Fatalf("configuration error leaked %q", private)
				}
			}
		})
	}

	model, err := models.NewOpenAICompatibleChatModel(base)
	if err != nil {
		t.Fatal(err)
	}
	formatted := fmt.Sprintf("%v %#v %#v", model, model, model.Contract())
	for _, private := range []string{base.APIKey, base.BaseURL, "private-endpoint-canary"} {
		if strings.Contains(formatted, private) {
			t.Fatalf("adapter formatting leaked %q: %s", private, formatted)
		}
	}
	for _, expected := range []string{`model="chat-alias"`, `model_version="chat-v1"`, `adapter_version="v7"`} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("adapter formatting omitted %q: %s", expected, formatted)
		}
	}
}

func chatOptions(baseURL string, client *http.Client) models.OpenAIChatOptions {
	return models.OpenAIChatOptions{
		Client: client, BaseURL: baseURL, APIKey: "chat-secret-canary", Model: "chat-alias", ModelVersion: "chat-v1",
		AdapterVersion: "v7", Timeout: time.Second, MaxRequestBytes: 1 << 20, MaxResponseBytes: 1 << 20,
	}
}

func validChatRequest(model agentdomain.ModelRef) agentapplication.ChatRequest {
	return agentapplication.ChatRequest{
		Phase:      agentdomain.ModelCallInitial,
		ProfileRef: agentdomain.ModelProfileRef{ID: "default", Version: "v1"},
		PromptRef:  agentdomain.PromptRef{ID: "rag-answer", Version: "v1"},
		SchemaRef:  agentdomain.SchemaRef{ID: "rag-answer", Version: "v1"},
		Model:      model,
		Messages: []agentapplication.ChatMessage{
			{Role: agentapplication.MessageRoleSystem, Content: "system-policy-canary"},
			{Role: agentapplication.MessageRoleUser, Content: "untrusted-source-canary"},
		},
		OutputSchema:    []byte(`{"type":"object"}`),
		MaxOutputTokens: 256,
	}
}

func validChatResponse(model, content string) string {
	encodedContent, _ := json.Marshal(content)
	return fmt.Sprintf(`{"id":"call-1","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":%s,"refusal":null},"finish_reason":"stop","logprobs":null}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"prompt_tokens_details":null,"completion_tokens_details":null},"system_fingerprint":null,"service_tier":null}`, model, encodedContent)
}

func assertChatError(t *testing.T, err error, kind foundation.ErrorKind, code string, retryable bool) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error=%T %v", err, err)
	}
	if classified.Kind != kind || classified.Code != code || classified.Retryable != retryable {
		t.Fatalf("error=%#v want kind=%s code=%s retryable=%t", classified, kind, code, retryable)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
