package eino

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestWorkspaceAnalysisCandidateStreamRuntimeUsesOneToolFreeStreamAndPreservesChunks(t *testing.T) {
	model := &workspaceAnalysisCandidateStreamModel{stream: func(messages []*schema.Message, options ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
		if len(messages) != 2 || messages[0].Role != schema.System || messages[1].Role != schema.User ||
			messages[0].Content != "policy" || messages[1].Content != "evidence E1" {
			t.Fatalf("messages = %#v", messages)
		}
		common := einomodel.GetCommonOptions(nil, options...)
		if common.ToolChoice == nil || *common.ToolChoice != schema.ToolChoiceForbidden || common.MaxTokens == nil || *common.MaxTokens != 512 {
			t.Fatalf("options = %#v", common)
		}
		return schema.StreamReaderFromArray([]*schema.Message{
			{Role: schema.Assistant, Content: `{"result_type":"workspace_`},
			{Content: `analysis_candidate",`},
			{Content: `"payload":{"answer_markdown":"ok [E1]"}}`, ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 7, CompletionTokens: 9, TotalTokens: 16}}},
		}), nil
	}}
	runtime, err := NewWorkspaceAnalysisCandidateStreamRuntime(model)
	if err != nil {
		t.Fatal(err)
	}
	sink := &workspaceAnalysisCandidateCollectingSink{}
	result, err := runtime.Stream(context.Background(), workspaceAnalysisCandidateStreamRequest(), sink)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if model.CallCount() != 1 || result.Model != workspaceAnalysisCandidateStreamRequest().Model ||
		string(result.Content) != `{"result_type":"workspace_analysis_candidate","payload":{"answer_markdown":"ok [E1]"}}` ||
		result.Usage != (agentdomain.TokenUsage{InputTokens: 7, OutputTokens: 9, TotalTokens: 16}) ||
		strings.Join(sink.chunks, "") != string(result.Content) {
		t.Fatalf("result=%#v chunks=%#v calls=%d", result, sink.chunks, model.CallCount())
	}
}

func TestWorkspaceAnalysisCandidateStreamRuntimeSendsStrictJSONSchemaResponseFormat(t *testing.T) {
	requestBody := make(chan []byte, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request=%s %s", request.Method, request.URL.String())
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		requestBody <- body

		writer.Header().Set("Content-Type", "text/event-stream")
		frames := []string{
			`data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"chat-v1","choices":[{"index":0,"delta":{"role":"assistant","content":"{\"answer\":\""},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"chat-v1","choices":[{"index":0,"delta":{"content":"ok\"}"},"finish_reason":"stop"}]}` + "\n\n",
			`data: {"id":"stream-1","object":"chat.completion.chunk","created":1,"model":"chat-v1","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}` + "\n\n",
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

	model, err := platformmodels.NewEinoRuntimeChatModel(platformmodels.OpenAIChatOptions{
		Client: server.Client(), BaseURL: server.URL, APIKey: "candidate-stream-secret", Model: "chat-alias", ModelVersion: "chat-v1",
		AdapterVersion: "v1", Timeout: time.Second, MaxRequestBytes: 1 << 20, MaxResponseBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewWorkspaceAnalysisCandidateStreamRuntime(model)
	if err != nil {
		t.Fatal(err)
	}
	request := workspaceAnalysisCandidateStreamRequest()
	request.OutputSchema = []byte(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)
	result, err := runtime.Stream(context.Background(), request, &workspaceAnalysisCandidateCollectingSink{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if string(result.Content) != `{"answer":"ok"}` {
		t.Fatalf("content=%q", result.Content)
	}

	var payload struct {
		MaxTokens      int             `json:"max_tokens"`
		Stream         bool            `json:"stream"`
		ToolChoice     string          `json:"tool_choice"`
		Tools          json.RawMessage `json:"tools"`
		ResponseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string          `json:"name"`
				Strict bool            `json:"strict"`
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	if len(requestBody) != 1 {
		t.Fatalf("provider requests=%d", len(requestBody))
	}
	body := <-requestBody
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode request: %v body=%s", err, body)
	}
	if payload.MaxTokens != request.MaxOutputTokens || !payload.Stream || payload.ToolChoice != "none" ||
		(len(payload.Tools) != 0 && string(payload.Tools) != "null" && string(payload.Tools) != "[]") ||
		payload.ResponseFormat.Type != "json_schema" || payload.ResponseFormat.JSONSchema.Name != workspaceAnalysisCandidateResponseSchemaName ||
		len(payload.ResponseFormat.JSONSchema.Name) > 64 || strings.Trim(payload.ResponseFormat.JSONSchema.Name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" ||
		!payload.ResponseFormat.JSONSchema.Strict || !equalWorkspaceAnalysisCandidateJSON(payload.ResponseFormat.JSONSchema.Schema, request.OutputSchema) {
		t.Fatalf("payload=%s", body)
	}
}

func TestWorkspaceAnalysisCandidateResponseFormatUsesCallPrivateSchemaCopies(t *testing.T) {
	original := []byte(`{"type":"object"}`)
	first := newWorkspaceAnalysisCandidateExtraFields(original)
	second := newWorkspaceAnalysisCandidateExtraFields(original)
	original[0] = 'x'

	firstFormat, firstOK := first["response_format"].(workspaceAnalysisCandidateResponseFormat)
	secondFormat, secondOK := second["response_format"].(workspaceAnalysisCandidateResponseFormat)
	if !firstOK || !secondOK || string(firstFormat.JSONSchema.Schema) != `{"type":"object"}` || string(secondFormat.JSONSchema.Schema) != `{"type":"object"}` {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	firstFormat.JSONSchema.Schema[0] = 'x'
	if string(secondFormat.JSONSchema.Schema) != `{"type":"object"}` {
		t.Fatalf("second schema changed with first: %q", secondFormat.JSONSchema.Schema)
	}
	first["response_format"] = nil
	if second["response_format"] == nil {
		t.Fatal("response format maps share mutable state")
	}
}

func TestWorkspaceAnalysisCandidateStreamRuntimeRejectsInvalidProviderFrames(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	tests := []struct {
		name   string
		frames []*schema.Message
	}{
		{name: "nil", frames: []*schema.Message{nil}},
		{name: "tool call", frames: []*schema.Message{{ToolCalls: []schema.ToolCall{{ID: "untrusted", Function: schema.FunctionCall{Name: "ReadSource"}}}}}},
		{name: "unknown extra", frames: []*schema.Message{{Extra: map[string]any{"untrusted": "metadata"}}}},
		{name: "invalid utf8", frames: []*schema.Message{{Content: invalidUTF8}}},
		{name: "overflow", frames: []*schema.Message{{Content: strings.Repeat("x", agentapplication.MaxAgentResultBytes+1)}}},
		{name: "invalid usage", frames: []*schema.Message{{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: -1, CompletionTokens: 2, TotalTokens: 1}}}}},
		{name: "repeated usage", frames: []*schema.Message{
			{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}}},
			{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}}},
		}},
		{name: "missing usage", frames: []*schema.Message{{Content: "{}"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := &workspaceAnalysisCandidateStreamModel{stream: func(_ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray(test.frames), nil
			}}
			runtime, err := NewWorkspaceAnalysisCandidateStreamRuntime(model)
			if err != nil {
				t.Fatal(err)
			}
			sink := &workspaceAnalysisCandidateCollectingSink{}
			_, err = runtime.Stream(context.Background(), workspaceAnalysisCandidateStreamRequest(), sink)
			if errorCode(err) != agentapplication.ErrorCodeWorkspaceAnalysisCandidateStreamInvalid || errorKind(err) != foundation.ErrorConsistencyViolation {
				t.Fatalf("error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
			}
			if model.CallCount() != 1 {
				t.Fatalf("stream calls=%d", model.CallCount())
			}
		})
	}
}

func TestWorkspaceAnalysisCandidateStreamRuntimeClosesReaderAfterInvalidFrame(t *testing.T) {
	closed := make(chan bool, 1)
	model := &workspaceAnalysisCandidateStreamModel{stream: func(_ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
		reader, writer := schema.Pipe[*schema.Message](0)
		go func() {
			defer writer.Close()
			writer.Send(&schema.Message{ToolCalls: []schema.ToolCall{{ID: "untrusted"}}}, nil)
			closed <- writer.Send(&schema.Message{Content: "must not block"}, nil)
		}()
		return reader, nil
	}}
	runtime, err := NewWorkspaceAnalysisCandidateStreamRuntime(model)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Stream(context.Background(), workspaceAnalysisCandidateStreamRequest(), &workspaceAnalysisCandidateCollectingSink{})
	if errorCode(err) != agentapplication.ErrorCodeWorkspaceAnalysisCandidateStreamInvalid {
		t.Fatalf("error=%v", err)
	}
	select {
	case wasClosed := <-closed:
		if !wasClosed {
			t.Fatal("provider writer remained open after invalid frame")
		}
	case <-time.After(time.Second):
		t.Fatal("provider writer did not observe reader close")
	}
}

func TestWorkspaceAnalysisCandidateStreamRuntimeDrainsAfterSinkFailure(t *testing.T) {
	sinkFailure := errors.New("candidate projector failed")
	drained := make(chan struct{})
	model := &workspaceAnalysisCandidateStreamModel{stream: func(_ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
		reader, writer := schema.Pipe[*schema.Message](0)
		go func() {
			defer close(drained)
			defer writer.Close()
			writer.Send(&schema.Message{Content: `{"payload":`}, nil)
			writer.Send(&schema.Message{Content: `"complete"}`}, nil)
			writer.Send(&schema.Message{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}}}, nil)
		}()
		return reader, nil
	}}
	runtime, err := NewWorkspaceAnalysisCandidateStreamRuntime(model)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Stream(context.Background(), workspaceAnalysisCandidateStreamRequest(), workspaceAnalysisCandidateFailingSink{err: sinkFailure})
	if !errors.Is(err, sinkFailure) {
		t.Fatalf("error=%v", err)
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("provider stream was not drained after sink failure")
	}
}

func TestWorkspaceAnalysisCandidateStreamRuntimeRejectsNilReader(t *testing.T) {
	model := &workspaceAnalysisCandidateStreamModel{stream: func(_ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
		return nil, nil
	}}
	runtime, err := NewWorkspaceAnalysisCandidateStreamRuntime(model)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Stream(context.Background(), workspaceAnalysisCandidateStreamRequest(), &workspaceAnalysisCandidateCollectingSink{})
	if errorCode(err) != agentapplication.ErrorCodeWorkspaceAnalysisCandidateStreamInvalid || errorKind(err) != foundation.ErrorConsistencyViolation {
		t.Fatalf("error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
	}
}

func TestWorkspaceAnalysisCandidateStreamRuntimeRejectsNonAnswerAndTypedNilSink(t *testing.T) {
	model := &workspaceAnalysisCandidateStreamModel{}
	runtime, err := NewWorkspaceAnalysisCandidateStreamRuntime(model)
	if err != nil {
		t.Fatal(err)
	}
	request := workspaceAnalysisCandidateStreamRequest()
	request.Phase = agentdomain.ModelCallPlan
	if _, err = runtime.Stream(context.Background(), request, &workspaceAnalysisCandidateCollectingSink{}); errorCode(err) != agentapplication.ErrorCodeWorkspaceAnalysisCandidateStreamInvalid {
		t.Fatalf("phase error=%v", err)
	}
	request = workspaceAnalysisCandidateStreamRequest()
	request.SchemaRef.ID = "agent.other"
	if _, err = runtime.Stream(context.Background(), request, &workspaceAnalysisCandidateCollectingSink{}); errorCode(err) != agentapplication.ErrorCodeWorkspaceAnalysisCandidateStreamInvalid {
		t.Fatalf("schema error=%v", err)
	}
	var sink *workspaceAnalysisCandidateCollectingSink
	if _, err = runtime.Stream(context.Background(), workspaceAnalysisCandidateStreamRequest(), sink); errorCode(err) != agentapplication.ErrorCodeWorkspaceAnalysisCandidateStreamInvalid {
		t.Fatalf("nil sink error=%v", err)
	}
	if model.CallCount() != 0 {
		t.Fatalf("stream calls=%d", model.CallCount())
	}
}

func workspaceAnalysisCandidateStreamRequest() agentapplication.ChatRequest {
	return agentapplication.ChatRequest{
		Phase:           agentdomain.ModelCallAnswer,
		ProfileRef:      agentdomain.ModelProfileRef{ID: "workspace-analysis", Version: "v1"},
		PromptRef:       agentdomain.PromptRef{ID: "workspace-analysis-synthesis", Version: "v1"},
		SchemaRef:       agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: "1"},
		Model:           agentdomain.ModelRef{AdapterName: "eino-openai", AdapterVersion: "v1", ModelID: "chat", ModelVersion: "chat-v1"},
		Messages:        []agentapplication.ChatMessage{{Role: agentapplication.MessageRoleSystem, Content: "policy"}, {Role: agentapplication.MessageRoleUser, Content: "evidence E1"}},
		OutputSchema:    []byte(`{"type":"object"}`),
		MaxOutputTokens: 512,
	}
}

func equalWorkspaceAnalysisCandidateJSON(left, right []byte) bool {
	var leftDocument any
	var rightDocument any
	if json.Unmarshal(left, &leftDocument) != nil || json.Unmarshal(right, &rightDocument) != nil {
		return false
	}
	return reflect.DeepEqual(leftDocument, rightDocument)
}

type workspaceAnalysisCandidateStreamModel struct {
	mu     sync.Mutex
	calls  int
	stream func([]*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error)
}

func (model *workspaceAnalysisCandidateStreamModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return nil, errors.New("unexpected generate")
}

func (model *workspaceAnalysisCandidateStreamModel) Stream(_ context.Context, messages []*schema.Message, options ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	model.mu.Lock()
	model.calls++
	stream := model.stream
	model.mu.Unlock()
	if stream == nil {
		return nil, errors.New("unexpected stream")
	}
	return stream(messages, options...)
}

func (model *workspaceAnalysisCandidateStreamModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return model, nil
}

func (model *workspaceAnalysisCandidateStreamModel) CallCount() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.calls
}

type workspaceAnalysisCandidateCollectingSink struct {
	chunks []string
}

func (sink *workspaceAnalysisCandidateCollectingSink) Append(_ context.Context, chunk agentapplication.WorkspaceAnalysisCandidateStreamChunk) error {
	sink.chunks = append(sink.chunks, chunk.Content)
	return nil
}

type workspaceAnalysisCandidateFailingSink struct{ err error }

func (sink workspaceAnalysisCandidateFailingSink) Append(context.Context, agentapplication.WorkspaceAnalysisCandidateStreamChunk) error {
	return sink.err
}

var _ einomodel.ToolCallingChatModel = (*workspaceAnalysisCandidateStreamModel)(nil)
var _ agentapplication.WorkspaceAnalysisCandidateStreamSink = (*workspaceAnalysisCandidateCollectingSink)(nil)
