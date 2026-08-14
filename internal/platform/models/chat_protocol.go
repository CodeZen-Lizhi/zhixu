package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

// OpenAIChatOptions 是 OpenAI-Compatible Chat Adapter 的 Composition Root 输入。
type OpenAIChatOptions struct {
	Client           *http.Client
	BaseURL          string
	APIKey           string
	Model            string
	ModelVersion     string
	AdapterVersion   string
	Timeout          time.Duration
	MaxRequestBytes  int64
	MaxResponseBytes int64
	APIStyle         ChatAPIStyle
	Provider         string
}

func buildOpenAIChatRequest(modelID string, request agentapplication.ChatRequest) openAIChatRequest {
	payload := openAIChatRequest{
		Model:       modelID,
		Messages:    make([]openAIChatRequestMessage, len(request.Messages)),
		MaxTokens:   request.MaxOutputTokens,
		Temperature: 0,
		ResponseFormat: openAIChatResponseFormat{
			Type: "json_schema",
			JSONSchema: openAIChatJSONSchema{
				Name:   schemaRequestName(request.SchemaRef),
				Strict: true,
				Schema: append(json.RawMessage(nil), request.OutputSchema...),
			},
		},
	}
	for index, message := range request.Messages {
		payload.Messages[index] = openAIChatRequestMessage{Role: string(message.Role), Content: message.Content}
	}
	return payload
}

func schemaRequestName(ref agentdomain.SchemaRef) string {
	hash := sha256.Sum256([]byte(ref.ID + "\x00" + ref.Version))
	return "zhixu_" + hex.EncodeToString(hash[:8])
}

func validateOpenAIChatResponse(model agentdomain.ModelRef, response openAIChatResponse) (agentapplication.ChatResponse, error) {
	if response.Model != model.ModelVersion {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseModelMismatch)
	}
	if len(response.Choices) != 1 || response.Choices[0].Index != 0 || response.Choices[0].Message.Role != "assistant" ||
		response.Choices[0].Message.Content == nil || *response.Choices[0].Message.Content == "" ||
		response.Choices[0].FinishReason == nil || *response.Choices[0].FinishReason != "stop" ||
		(response.Choices[0].Message.Refusal != nil && *response.Choices[0].Message.Refusal != "") ||
		len(response.Choices[0].Message.ToolCalls) != 0 || response.Usage == nil ||
		response.Usage.PromptTokens == nil || response.Usage.CompletionTokens == nil || response.Usage.TotalTokens == nil {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid)
	}
	usage := agentdomain.TokenUsage{
		InputTokens:  *response.Usage.PromptTokens,
		OutputTokens: *response.Usage.CompletionTokens,
		TotalTokens:  *response.Usage.TotalTokens,
	}
	if err := usage.Validate(); err != nil || usage.TotalTokens == 0 {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid)
	}
	return agentapplication.ChatResponse{Model: model, Content: []byte(*response.Choices[0].Message.Content), Usage: usage}, nil
}

type openAIChatRequest struct {
	Model          string                     `json:"model"`
	Messages       []openAIChatRequestMessage `json:"messages"`
	MaxTokens      int                        `json:"max_tokens"`
	Temperature    float64                    `json:"temperature"`
	ResponseFormat openAIChatResponseFormat   `json:"response_format"`
}

type openAIChatRequestMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponseFormat struct {
	Type       string               `json:"type"`
	JSONSchema openAIChatJSONSchema `json:"json_schema"`
}

type openAIChatJSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type openAIChatResponse struct {
	ID                string             `json:"id"`
	Object            string             `json:"object"`
	Created           int64              `json:"created"`
	Model             string             `json:"model"`
	Choices           []openAIChatChoice `json:"choices"`
	Usage             *openAIChatUsage   `json:"usage"`
	SystemFingerprint *string            `json:"system_fingerprint"`
	ServiceTier       *string            `json:"service_tier"`
}

type openAIChatChoice struct {
	Index        int               `json:"index"`
	Message      openAIChatMessage `json:"message"`
	FinishReason *string           `json:"finish_reason"`
	Logprobs     json.RawMessage   `json:"logprobs"`
}

type openAIChatMessage struct {
	Role    string  `json:"role"`
	Content *string `json:"content"`
	Refusal *string `json:"refusal"`
	// Reasoning 接收已知 Provider 推理扩展；项目响应不会暴露或持久化该内容。
	Reasoning *string `json:"reasoning"`
	// ReasoningContent 接收 reasoning_content 别名，并保持与 Reasoning 相同的丢弃语义。
	ReasoningContent *string           `json:"reasoning_content"`
	ToolCalls        []json.RawMessage `json:"tool_calls"`
	Annotations      []json.RawMessage `json:"annotations"`
}

type openAIChatUsage struct {
	PromptTokens            *int64          `json:"prompt_tokens"`
	CompletionTokens        *int64          `json:"completion_tokens"`
	TotalTokens             *int64          `json:"total_tokens"`
	PromptTokensDetails     json.RawMessage `json:"prompt_tokens_details"`
	CompletionTokensDetails json.RawMessage `json:"completion_tokens_details"`
}
