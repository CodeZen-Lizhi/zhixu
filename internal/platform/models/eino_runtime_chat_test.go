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
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestEinoRuntimeChatModelGeneratePreservesToolsAndFrozenModel(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request=%s %s", request.Method, request.URL.String())
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var payload struct {
			Model       string   `json:"model"`
			Temperature *float32 `json:"temperature"`
			Stream      bool     `json:"stream"`
			ToolChoice  any      `json:"tool_choice"`
			Tools       []struct {
				Type     string `json:"type"`
				Function struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if payload.Model != "chat-v1" || payload.Temperature == nil || *payload.Temperature != 0 || payload.Stream || payload.ToolChoice != "auto" || len(payload.Tools) != 1 ||
			payload.Tools[0].Type != "function" || payload.Tools[0].Function.Name != "ReadSource" ||
			payload.Tools[0].Function.Description != "Read a frozen source" {
			t.Errorf("payload=%s", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"call-1","object":"chat.completion","created":1,"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_read_1","type":"function","function":{"name":"ReadSource","arguments":"{\"source_id\":\"source-1\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`)
	}))
	defer server.Close()

	runtimeModel, err := models.NewEinoRuntimeChatModel(chatOptions(server.URL, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	toolModel, err := runtimeModel.WithTools([]*schema.ToolInfo{{Name: "ReadSource", Desc: "Read a frozen source"}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := toolModel.Generate(context.Background(), runtimeChatMessages())
	if err != nil {
		t.Fatal(err)
	}
	if message.Role != schema.Assistant || message.ResponseMeta == nil || message.ResponseMeta.FinishReason != "tool_calls" || len(message.ToolCalls) != 1 {
		t.Fatalf("message=%#v", message)
	}
	call := message.ToolCalls[0]
	if call.ID != "call_read_1" || call.Type != "function" || call.Function.Name != "ReadSource" || call.Function.Arguments != `{"source_id":"source-1"}` {
		t.Fatalf("tool call=%#v", call)
	}
}

func TestEinoRuntimeChatModelStreamsMultipleSSEFramesWithUsageAndForbiddenTools(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var payload struct {
			Model         string   `json:"model"`
			Temperature   *float32 `json:"temperature"`
			Stream        bool     `json:"stream"`
			ToolChoice    any      `json:"tool_choice"`
			Tools         []any    `json:"tools"`
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if payload.Model != "chat-v1" || payload.Temperature == nil || *payload.Temperature != 0 || !payload.Stream || payload.ToolChoice != "none" || len(payload.Tools) != 0 || !payload.StreamOptions.IncludeUsage {
			t.Errorf("payload=%s", body)
		}

		writer.Header().Set("Content-Type", "text/event-stream")
		frames := []string{
			`data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"chat-v1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"chat-v1","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}` + "\n\n",
			`data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"chat-v1","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, frame := range frames {
			_, _ = io.WriteString(writer, frame)
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	runtimeModel, err := models.NewEinoRuntimeChatModel(chatOptions(server.URL, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := runtimeModel.Stream(context.Background(), runtimeChatMessages(), einomodel.WithToolChoice(schema.ToolChoiceForbidden))
	if err != nil {
		t.Fatal(err)
	}

	var content strings.Builder
	var usage *schema.TokenUsage
	for {
		chunk, receiveErr := stream.Recv()
		if errors.Is(receiveErr, io.EOF) {
			break
		}
		if receiveErr != nil {
			stream.Close()
			t.Fatal(receiveErr)
		}
		content.WriteString(chunk.Content)
		if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil {
			usage = chunk.ResponseMeta.Usage
		}
	}
	stream.Close()
	if content.String() != "Hello" || usage == nil || usage.PromptTokens != 4 || usage.CompletionTokens != 2 || usage.TotalTokens != 6 {
		t.Fatalf("content=%q usage=%#v", content.String(), usage)
	}
}

func TestEinoRuntimeChatModelStreamsFragmentedToolCallWithRequestTemperature(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var payload struct {
			Model       string   `json:"model"`
			Temperature *float32 `json:"temperature"`
			Stream      bool     `json:"stream"`
			ToolChoice  any      `json:"tool_choice"`
			Tools       []struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if payload.Model != "chat-v1" || payload.Temperature == nil || *payload.Temperature != 0.35 || !payload.Stream ||
			payload.ToolChoice != "auto" || len(payload.Tools) != 1 || payload.Tools[0].Type != "function" || payload.Tools[0].Function.Name != "ReadSource" {
			t.Errorf("payload=%s", body)
		}

		writer.Header().Set("Content-Type", "text/event-stream")
		frames := []string{
			`data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"chat-v1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_read_1","type":"function","function":{"name":"ReadSource","arguments":"{\"source_id\":\""}}]},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"chat-v1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"source-1\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n",
			`data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"chat-v1","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, frame := range frames {
			_, _ = io.WriteString(writer, frame)
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	runtimeModel, err := models.NewEinoRuntimeChatModel(chatOptions(server.URL, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	toolModel, err := runtimeModel.WithTools([]*schema.ToolInfo{{Name: "ReadSource", Desc: "Read a frozen source"}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := toolModel.Stream(context.Background(), runtimeChatMessages(), einomodel.WithTemperature(0.35))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	chunks := make([]*schema.Message, 0, 3)
	for {
		chunk, receiveErr := stream.Recv()
		if errors.Is(receiveErr, io.EOF) {
			break
		}
		if receiveErr != nil {
			t.Fatal(receiveErr)
		}
		chunks = append(chunks, chunk)
	}
	combined, err := schema.ConcatMessages(chunks)
	if err != nil {
		t.Fatal(err)
	}
	if combined.ResponseMeta == nil || combined.ResponseMeta.FinishReason != "tool_calls" || combined.ResponseMeta.Usage == nil ||
		combined.ResponseMeta.Usage.PromptTokens != 4 || combined.ResponseMeta.Usage.CompletionTokens != 2 || combined.ResponseMeta.Usage.TotalTokens != 6 ||
		len(combined.ToolCalls) != 1 {
		t.Fatalf("combined=%#v", combined)
	}
	call := combined.ToolCalls[0]
	if call.ID != "call_read_1" || call.Type != "function" || call.Function.Name != "ReadSource" || call.Function.Arguments != `{"source_id":"source-1"}` {
		t.Fatalf("tool call=%#v", call)
	}
}

func TestEinoRuntimeChatModelRejectsInvalidGenerateResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		code string
	}{
		{
			name: "model mismatch",
			body: `{"id":"call-1","object":"chat.completion","created":1,"model":"other-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			code: models.ErrorCodeChatResponseModelMismatch,
		},
		{
			name: "tool arguments are not an object",
			body: `{"id":"call-1","object":"chat.completion","created":1,"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"ReadSource","arguments":"[]"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			code: models.ErrorCodeChatResponseInvalid,
		},
		{
			name: "duplicate tool call ids",
			body: `{"id":"call-1","object":"chat.completion","created":1,"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"duplicate","type":"function","function":{"name":"ReadSource","arguments":"{}"}},{"id":"duplicate","type":"function","function":{"name":"ReadSource","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			code: models.ErrorCodeChatResponseInvalid,
		},
		{
			name: "zero usage",
			body: `{"id":"call-1","object":"chat.completion","created":1,"model":"chat-v1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
			code: models.ErrorCodeChatResponseInvalid,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			model, err := models.NewEinoRuntimeChatModel(chatOptions(server.URL, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Generate(context.Background(), runtimeChatMessages())
			assertChatError(t, err, foundation.ErrorConsistencyViolation, test.code, false)
		})
	}
}

func TestEinoRuntimeChatModelRejectsInvalidStreamingResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		contentType string
		body        string
		maxBytes    int64
		code        string
	}{
		{
			name:        "model mismatch",
			contentType: "text/event-stream",
			body:        `data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"other-model","choices":[{"index":0,"delta":{"role":"assistant","content":"secret-response-canary"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n",
			code:        models.ErrorCodeChatResponseModelMismatch,
		},
		{
			name:        "wrong content type",
			contentType: "application/json",
			body:        `{"secret":"response-secret-canary"}`,
			code:        models.ErrorCodeChatResponseInvalid,
		},
		{
			name:        "oversized event stream",
			contentType: "text/event-stream",
			body:        "data: " + strings.Repeat("response-secret-canary", 64) + "\n\n",
			maxBytes:    128,
			code:        models.ErrorCodeChatResponseInvalid,
		},
		{
			name:        "duplicate usage",
			contentType: "text/event-stream",
			body: `data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"chat-v1","choices":[{"index":0,"delta":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]}` + "\n\n" +
				`data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"chat-v1","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}` + "\n\n" +
				`data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"chat-v1","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}` + "\n\ndata: [DONE]\n\n",
			code: models.ErrorCodeChatResponseInvalid,
		},
		{
			name:        "incomplete fragmented tool arguments",
			contentType: "text/event-stream",
			body:        `data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"chat-v1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_read_1","type":"function","function":{"name":"ReadSource","arguments":"{"}}]},"finish_reason":null}]}` + "\n\ndata: [DONE]\n\n",
			code:        models.ErrorCodeChatResponseInvalid,
		},
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
			model, err := models.NewEinoRuntimeChatModel(options)
			if err != nil {
				t.Fatal(err)
			}
			err = receiveRuntimeStreamError(model)
			assertChatError(t, err, foundation.ErrorConsistencyViolation, test.code, false)
			formatted := fmt.Sprintf("%v %#v", err, err)
			for _, private := range []string{"response-secret-canary", "secret-response-canary"} {
				if strings.Contains(formatted, private) {
					t.Fatalf("stream error leaked %q", private)
				}
			}
		})
	}
}

func TestEinoRuntimeChatModelBoundsAndRedactsProviderError(t *testing.T) {
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
	model, err := models.NewEinoRuntimeChatModel(chatOptions("https://private-endpoint.example", client))
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Generate(context.Background(), runtimeChatMessages())
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

func runtimeChatMessages() []*schema.Message {
	return []*schema.Message{
		schema.SystemMessage("system-policy-canary"),
		schema.UserMessage("untrusted-source-canary"),
	}
}

func receiveRuntimeStreamError(model *models.EinoRuntimeChatModel) error {
	stream, err := model.Stream(context.Background(), runtimeChatMessages())
	if err != nil {
		return err
	}
	for {
		_, receiveErr := stream.Recv()
		if receiveErr != nil {
			stream.Close()
			if errors.Is(receiveErr, io.EOF) {
				return nil
			}
			return receiveErr
		}
	}
}

func TestEinoChatReasoningEffortUsesFrozenWireSettings(t *testing.T) {
	for _, test := range []struct {
		name, model, effort string
		reasoning           bool
	}{
		{name: "legacy default", model: "chat-v1"},
		{name: "provider default reasoning", model: "deepseek-v4.1-flash"},
		{name: "explicit high", model: "chat-v1", effort: "high", reasoning: true},
		{name: "GPT6 default", model: "gpt-6-astra", reasoning: true},
		{name: "GPT6 max", model: "gpt-6-astra", effort: "max", reasoning: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				var effort string
				_ = json.Unmarshal(body["reasoning_effort"], &effort)
				if effort != test.effort || (test.effort == "" && body["reasoning_effort"] != nil) {
					t.Error("wire effort differs from frozen configuration")
				}
				budgetKey := "max_tokens"
				if test.reasoning {
					budgetKey = "max_completion_tokens"
					for _, forbidden := range []string{"temperature", "top_p", "logprobs", "top_logprobs", "max_tokens"} {
						if body[forbidden] != nil {
							t.Errorf("reasoning request retained %s", forbidden)
						}
					}
				} else if string(body["temperature"]) != "0" || body["max_completion_tokens"] != nil {
					t.Error("legacy defaults changed")
				}
				wantBudget := "256"
				if calls == 3 {
					wantBudget = "2048" // 探测额度独立于名称和思考强度。
				}
				if string(body[budgetKey]) != wantBudget {
					t.Error("request did not retain the caller token budget")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, validChatResponse(test.model, `{"result":"ok"}`))
			}))
			defer server.Close()
			options := chatOptions(server.URL, server.Client())
			options.Model, options.ModelVersion, options.ReasoningEffort = test.model, test.model, test.effort
			structured, err := models.NewEinoOpenAIChatModel(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := structured.Chat(context.Background(), validChatRequest(structured.Contract().Model)); err != nil {
				t.Fatal(err)
			}
			runtime, err := models.NewEinoRuntimeChatModel(options)
			if err != nil {
				t.Fatal(err)
			}
			// 调用方选项不能覆盖不可变运行代次的思考强度。
			if _, err := runtime.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "test"}},
				einomodel.WithMaxTokens(256), einoopenai.WithReasoningEffort(einoopenai.ReasoningEffortLevelLow)); err != nil {
				t.Fatal(err)
			}
			if err := structured.ProbeConnection(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls != 3 {
				t.Fatalf("provider calls=%d, want exactly 3", calls)
			}
		})
	}
}
