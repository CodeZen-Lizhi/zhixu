package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
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
}

// OpenAICompatibleChatModel 通过直接 HTTP 执行单次结构化 Chat 调用。
type OpenAICompatibleChatModel struct {
	http chatHTTPConfig
}

// NewOpenAICompatibleChatModel 创建不自动重试且禁止重定向的 Chat Adapter。
func NewOpenAICompatibleChatModel(options OpenAIChatOptions) (*OpenAICompatibleChatModel, error) {
	config, err := newChatHTTPConfig(chatHTTPOptions{
		client: options.Client, baseURL: options.BaseURL, apiKey: options.APIKey,
		model: options.Model, modelVersion: options.ModelVersion, adapterVersion: options.AdapterVersion, timeout: options.Timeout,
		maxRequestBytes: options.MaxRequestBytes, maxResponseBytes: options.MaxResponseBytes,
	})
	if err != nil {
		return nil, err
	}
	return &OpenAICompatibleChatModel{http: config}, nil
}

// Contract 返回不含 API Key 与 Endpoint 的冻结运行时契约。
func (model *OpenAICompatibleChatModel) Contract() ChatContract {
	return model.http.contractCopy()
}

// Chat 执行一次 OpenAI-Compatible `/v1/chat/completions` 请求，不在 Adapter 内重试。
func (model *OpenAICompatibleChatModel) Chat(ctx context.Context, request agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	if model == nil {
		return agentapplication.ChatResponse{}, chatError(foundation.ErrorDependencyUnavailable, ErrorCodeChatCapabilityUnavailable, false, errChatCapabilityOff)
	}
	payload := buildOpenAIChatRequest(model.http.contract.Model.ModelID, request)
	var response openAIChatResponse
	if err := model.http.chat(ctx, request, payload, &response); err != nil {
		return agentapplication.ChatResponse{}, err
	}
	result, err := validateOpenAIChatResponse(model.http.contract.Model, response)
	if err != nil {
		return agentapplication.ChatResponse{}, err
	}
	if err := agentapplication.ValidateChatResponse(request, result); err != nil {
		return agentapplication.ChatResponse{}, err
	}
	return result, nil
}

func buildOpenAIChatRequest(modelID string, request agentapplication.ChatRequest) openAIChatRequest {
	payload := openAIChatRequest{
		Model:     modelID,
		Messages:  make([]openAIChatRequestMessage, len(request.Messages)),
		MaxTokens: request.MaxOutputTokens,
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

// String 返回不含 Endpoint、Credential、Prompt 或原始响应的 Adapter 摘要。
func (model *OpenAICompatibleChatModel) String() string {
	if model == nil {
		return "openai-compatible chat adapter(unavailable)"
	}
	return safeChatAdapterString(model.http.contract.Model)
}

// GoString 避免 `%#v` 展开私有 HTTP 配置。
func (model *OpenAICompatibleChatModel) GoString() string {
	return model.String()
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
	Role        string            `json:"role"`
	Content     *string           `json:"content"`
	Refusal     *string           `json:"refusal"`
	ToolCalls   []json.RawMessage `json:"tool_calls"`
	Annotations []json.RawMessage `json:"annotations"`
}

type openAIChatUsage struct {
	PromptTokens            *int64          `json:"prompt_tokens"`
	CompletionTokens        *int64          `json:"completion_tokens"`
	TotalTokens             *int64          `json:"total_tokens"`
	PromptTokensDetails     json.RawMessage `json:"prompt_tokens_details"`
	CompletionTokensDetails json.RawMessage `json:"completion_tokens_details"`
}

var _ agentapplication.ChatModel = (*OpenAICompatibleChatModel)(nil)
