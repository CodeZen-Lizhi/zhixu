package models_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
)

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
