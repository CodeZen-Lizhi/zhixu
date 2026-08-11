package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
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
	APIStyle         ChatAPIStyle
}

// OpenAICompatibleChatModel 通过直接 HTTP 执行单次结构化 Chat 调用。
type OpenAICompatibleChatModel struct {
	http chatHTTPConfig
}

// ChatConnectionProber executes a fixed plain probe for the configured Chat API style without returning provider output.
type ChatConnectionProber interface {
	ProbeConnection(context.Context) error
}

// NewOpenAICompatibleChatModel 创建不自动重试且禁止重定向的 Chat Adapter。
func NewOpenAICompatibleChatModel(options OpenAIChatOptions) (*OpenAICompatibleChatModel, error) {
	config, err := newChatHTTPConfig(chatHTTPOptions{
		client: options.Client, baseURL: options.BaseURL, apiKey: options.APIKey,
		model: options.Model, modelVersion: options.ModelVersion, adapterVersion: options.AdapterVersion, timeout: options.Timeout,
		maxRequestBytes: options.MaxRequestBytes, maxResponseBytes: options.MaxResponseBytes,
		apiStyle: options.APIStyle,
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

// Chat 执行一次显式配置的 OpenAI-Compatible Chat 请求，不在 Adapter 内重试。
func (model *OpenAICompatibleChatModel) Chat(ctx context.Context, request agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	if model == nil {
		return agentapplication.ChatResponse{}, chatError(foundation.ErrorDependencyUnavailable, ErrorCodeChatCapabilityUnavailable, false, errChatCapabilityOff)
	}
	if model.http.apiStyle == ChatAPIStyleResponses {
		return model.chatResponses(ctx, request)
	}
	payload := openAIChatRequest{
		Model:     model.http.contract.Model.ModelID,
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
	var response openAIChatResponse
	if err := model.http.chat(ctx, request, payload, &response); err != nil {
		return agentapplication.ChatResponse{}, err
	}
	result, err := validateOpenAIChatResponse(model.http.contract.Model, response)
	if err != nil {
		return agentapplication.ChatResponse{}, err
	}
	if err := agentapplication.ValidateChatResponse(request, result); err != nil {
		return agentapplication.ChatResponse{}, withResponseValidationDiagnostic(err)
	}
	return result, nil
}

// ProbeConnection verifies that the configured endpoint, credential, and model can complete one minimal request.
func (model *OpenAICompatibleChatModel) ProbeConnection(ctx context.Context) error {
	if model == nil {
		return chatError(foundation.ErrorDependencyUnavailable, ErrorCodeChatCapabilityUnavailable, false, errChatCapabilityOff)
	}
	if model.http.apiStyle == ChatAPIStyleResponses {
		return model.probeResponses(ctx)
	}
	payload := openAIChatProbeRequest{
		Model: model.http.contract.Model.ModelID,
		Messages: []openAIChatRequestMessage{
			{Role: string(agentapplication.MessageRoleUser), Content: "test"},
		},
	}
	var response openAIChatProbeResponse
	if err := model.http.probe(ctx, payload, &response); err != nil {
		return err
	}
	if len(response.Choices) != 1 || response.Choices[0].Index != 0 || response.Choices[0].Message.Role != "assistant" {
		return chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationInvalidResponse))
	}
	if response.Choices[0].Message.Content == nil || strings.TrimSpace(*response.Choices[0].Message.Content) == "" {
		return chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationEmptyContent))
	}
	return nil
}

func (model *OpenAICompatibleChatModel) chatResponses(ctx context.Context, request agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	payload := openAIResponsesRequest{
		Model:           model.http.contract.Model.ModelID,
		Input:           make([]openAIResponsesInput, len(request.Messages)),
		MaxOutputTokens: request.MaxOutputTokens,
		Store:           false,
		Text: openAIResponsesText{Format: openAIResponsesFormat{
			Type: "json_schema", Name: schemaRequestName(request.SchemaRef), Strict: true,
			Schema: append(json.RawMessage(nil), request.OutputSchema...),
		}},
	}
	for index, message := range request.Messages {
		payload.Input[index] = openAIResponsesInput{Role: string(message.Role), Content: []openAIResponsesInputContent{{Type: "input_text", Text: message.Content}}}
	}
	var response openAIResponsesResponse
	if err := model.http.chatWithResponseMode(ctx, request, payload, &response, false); err != nil {
		return agentapplication.ChatResponse{}, err
	}
	result, err := validateOpenAIResponsesResponse(model.http.contract.Model, response)
	if err != nil {
		return agentapplication.ChatResponse{}, err
	}
	if err := agentapplication.ValidateChatResponse(request, result); err != nil {
		return agentapplication.ChatResponse{}, withResponseValidationDiagnostic(err)
	}
	return result, nil
}

func (model *OpenAICompatibleChatModel) probeResponses(ctx context.Context) error {
	var response openAIResponsesProbeResponse
	if err := model.http.probe(ctx, openAIResponsesProbeRequest{Model: model.http.contract.Model.ModelID, Input: "test", Store: false}, &response); err != nil {
		return err
	}
	if response.Status != "completed" {
		return chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationInvalidResponse))
	}
	if responsesOutputText(response.Output, false) == "" {
		return chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationEmptyContent))
	}
	return nil
}

func validateOpenAIResponsesResponse(model agentdomain.ModelRef, response openAIResponsesResponse) (agentapplication.ChatResponse, error) {
	if response.Model != model.ModelVersion {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseModelMismatch, responseValidationDiagnosticWithReason(ConnectionValidationModelMismatch))
	}
	if response.Status != "completed" {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationInvalidResponse))
	}
	if !responsesAssistantMessagesCompleted(response.Output) {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationInvalidResponse))
	}
	content := responsesOutputText(response.Output, true)
	if content == "" {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationEmptyContent))
	}
	if response.Usage == nil {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationMissingUsage))
	}
	usage := agentdomain.TokenUsage{InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens, TotalTokens: response.Usage.TotalTokens}
	if err := usage.Validate(); err != nil || usage.TotalTokens == 0 {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationInvalidUsage))
	}
	return agentapplication.ChatResponse{Model: model, Content: []byte(content), Usage: usage}, nil
}

func responsesOutputText(output []openAIResponsesOutput, requireCompleted bool) string {
	var builder strings.Builder
	for _, item := range output {
		if item.Type != "message" || item.Role != "assistant" || (requireCompleted && item.Status != "completed") {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				builder.WriteString(content.Text)
			}
		}
	}
	return builder.String()
}

func responsesAssistantMessagesCompleted(output []openAIResponsesOutput) bool {
	for _, item := range output {
		if item.Type == "message" && item.Role == "assistant" && item.Status != "completed" {
			return false
		}
	}
	return true
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
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseModelMismatch, responseValidationDiagnosticWithReason(ConnectionValidationModelMismatch))
	}
	if len(response.Choices) != 1 || response.Choices[0].Index != 0 || response.Choices[0].Message.Role != "assistant" {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationInvalidResponse))
	}
	choice := response.Choices[0]
	if choice.FinishReason == nil || *choice.FinishReason != "stop" {
		reason := ConnectionValidationFinishReasonInvalid
		if choice.FinishReason != nil && *choice.FinishReason == "length" {
			reason = ConnectionValidationFinishReasonLength
		}
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(reason))
	}
	if choice.Message.Content == nil || *choice.Message.Content == "" {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationEmptyContent))
	}
	if choice.Message.Refusal != nil && *choice.Message.Refusal != "" {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationRefusal))
	}
	if len(choice.Message.ToolCalls) != 0 {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationToolCalls))
	}
	if response.Usage == nil || response.Usage.PromptTokens == nil || response.Usage.CompletionTokens == nil || response.Usage.TotalTokens == nil {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationMissingUsage))
	}
	usage := agentdomain.TokenUsage{
		InputTokens:  *response.Usage.PromptTokens,
		OutputTokens: *response.Usage.CompletionTokens,
		TotalTokens:  *response.Usage.TotalTokens,
	}
	if err := usage.Validate(); err != nil || usage.TotalTokens == 0 {
		return agentapplication.ChatResponse{}, chatResponseError(ErrorCodeChatResponseInvalid, responseValidationDiagnosticWithReason(ConnectionValidationInvalidUsage))
	}
	return agentapplication.ChatResponse{Model: model, Content: []byte(*choice.Message.Content), Usage: usage}, nil
}

type openAIChatRequest struct {
	Model          string                     `json:"model"`
	Messages       []openAIChatRequestMessage `json:"messages"`
	MaxTokens      int                        `json:"max_tokens"`
	ResponseFormat openAIChatResponseFormat   `json:"response_format"`
}

type openAIChatProbeRequest struct {
	Model    string                     `json:"model"`
	Messages []openAIChatRequestMessage `json:"messages"`
}

type openAIChatProbeResponse struct {
	Choices []openAIChatChoice `json:"choices"`
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

type openAIResponsesRequest struct {
	Model           string                 `json:"model"`
	Input           []openAIResponsesInput `json:"input"`
	MaxOutputTokens int                    `json:"max_output_tokens"`
	Store           bool                   `json:"store"`
	Text            openAIResponsesText    `json:"text"`
}

type openAIResponsesInput struct {
	Role    string                        `json:"role"`
	Content []openAIResponsesInputContent `json:"content"`
}

type openAIResponsesInputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type openAIResponsesText struct {
	Format openAIResponsesFormat `json:"format"`
}
type openAIResponsesFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}
type openAIResponsesProbeRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
	Store bool   `json:"store"`
}
type openAIResponsesProbeResponse struct {
	Status string                  `json:"status"`
	Output []openAIResponsesOutput `json:"output"`
}
type openAIResponsesResponse struct {
	ID     string                  `json:"id"`
	Object string                  `json:"object"`
	Status string                  `json:"status"`
	Model  string                  `json:"model"`
	Output []openAIResponsesOutput `json:"output"`
	Usage  *openAIResponsesUsage   `json:"usage"`
}
type openAIResponsesOutput struct {
	ID      string                         `json:"id"`
	Type    string                         `json:"type"`
	Role    string                         `json:"role"`
	Status  string                         `json:"status"`
	Content []openAIResponsesOutputContent `json:"content"`
	Summary []json.RawMessage              `json:"summary"`
}
type openAIResponsesOutputContent struct {
	Type        string            `json:"type"`
	Text        string            `json:"text"`
	Annotations []json.RawMessage `json:"annotations"`
	Logprobs    []json.RawMessage `json:"logprobs"`
}
type openAIResponsesUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

var _ agentapplication.ChatModel = (*OpenAICompatibleChatModel)(nil)
var _ ChatConnectionProber = (*OpenAICompatibleChatModel)(nil)
