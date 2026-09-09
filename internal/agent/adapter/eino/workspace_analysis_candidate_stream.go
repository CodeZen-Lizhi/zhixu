package eino

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const workspaceAnalysisCandidateResponseSchemaName = "zhixu_workspace_analysis_candidate_v1"

type workspaceAnalysisCandidateResponseFormat struct {
	Type       string                                       `json:"type"`
	JSONSchema workspaceAnalysisCandidateResponseJSONSchema `json:"json_schema"`
}

type workspaceAnalysisCandidateResponseJSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

// WorkspaceAnalysisCandidateStreamRuntime 是工作区分析候选合成的独立 Eino
// 适配器。它只有一次 tool-free Stream 调用，不拥有 Model Call 记录、预算、
// Candidate 持久化或 Draft 生命周期。
type WorkspaceAnalysisCandidateStreamRuntime struct {
	model einomodel.ToolCallingChatModel
}

// NewWorkspaceAnalysisCandidateStreamRuntime 创建候选合成流适配器。
func NewWorkspaceAnalysisCandidateStreamRuntime(model einomodel.ToolCallingChatModel) (*WorkspaceAnalysisCandidateStreamRuntime, error) {
	if nilEinoChatModel(model) {
		return nil, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode, errors.New("eino workspace analysis candidate stream model is unavailable"))
	}
	return &WorkspaceAnalysisCandidateStreamRuntime{model: model}, nil
}

// Stream 完整消费并关闭唯一 Provider stream。每一个 Provider frame 会在交给
// 调用方前校验，任何违规内容都不会送入 sink。
func (runtime *WorkspaceAnalysisCandidateStreamRuntime) Stream(
	ctx context.Context,
	request agentapplication.ChatRequest,
	sink agentapplication.WorkspaceAnalysisCandidateStreamSink,
) (agentapplication.ChatResponse, error) {
	if runtime == nil || nilEinoChatModel(runtime.model) {
		return agentapplication.ChatResponse{}, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode, errors.New("eino workspace analysis candidate stream runtime is unavailable"))
	}
	if err := agentapplication.ValidateWorkspaceAnalysisCandidateStreamRequest(request, sink); err != nil {
		return agentapplication.ChatResponse{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	reader, err := runtime.model.Stream(
		ctx,
		projectWorkspaceAnalysisCandidateMessages(request.Messages),
		einomodel.WithToolChoice(schema.ToolChoiceForbidden),
		einomodel.WithMaxTokens(request.MaxOutputTokens),
		einoopenai.WithExtraFields(workspaceAnalysisCandidateExtraFieldsForVersion(request.OutputSchema, request.SchemaRef.Version)),
	)
	if err != nil {
		return agentapplication.ChatResponse{}, mapEinoRuntimeError(err)
	}
	if reader == nil {
		return agentapplication.ChatResponse{}, candidateStreamInvalidError("eino workspace analysis candidate stream is nil")
	}
	defer reader.Close()

	var content strings.Builder
	var usage agentdomain.TokenUsage
	usageSeen := false
	var sinkErr error
	for {
		chunk, receiveErr := reader.Recv()
		if errors.Is(receiveErr, io.EOF) {
			break
		}
		if receiveErr != nil {
			return agentapplication.ChatResponse{}, mapEinoRuntimeError(receiveErr)
		}
		if err := validateWorkspaceAnalysisCandidateStreamChunk(chunk, content.Len(), usageSeen); err != nil {
			return agentapplication.ChatResponse{}, err
		}
		if chunk.Content != "" {
			content.WriteString(chunk.Content)
			if sinkErr == nil {
				sinkErr = sink.Append(ctx, agentapplication.WorkspaceAnalysisCandidateStreamChunk{Content: chunk.Content})
			}
		}
		if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil {
			usage, _ = runtimeMessageUsage(chunk)
			usageSeen = true
		}
	}
	if sinkErr != nil {
		return agentapplication.ChatResponse{}, sinkErr
	}
	response := agentapplication.ChatResponse{Model: request.Model, Content: []byte(content.String()), Usage: usage}
	if !usageSeen {
		return agentapplication.ChatResponse{}, candidateStreamInvalidError("eino workspace analysis candidate stream omitted token usage")
	}
	if err := agentapplication.ValidateChatResponse(request, response); err != nil {
		return agentapplication.ChatResponse{}, candidateStreamInvalidError("eino workspace analysis candidate stream returned an invalid result")
	}
	return response, nil
}

func newWorkspaceAnalysisCandidateExtraFields(outputSchema []byte) map[string]any {
	return workspaceAnalysisCandidateExtraFieldsForVersion(outputSchema, "1")
}

func workspaceAnalysisCandidateExtraFieldsForVersion(outputSchema []byte, version string) map[string]any {
	name := workspaceAnalysisCandidateResponseSchemaName
	if version == "2" {
		name = "zhixu_workspace_analysis_candidate_v2"
	}
	return map[string]any{
		"response_format": workspaceAnalysisCandidateResponseFormat{
			Type: "json_schema",
			JSONSchema: workspaceAnalysisCandidateResponseJSONSchema{
				Name:   name,
				Strict: true,
				Schema: append(json.RawMessage(nil), outputSchema...),
			},
		},
	}
}

func projectWorkspaceAnalysisCandidateMessages(messages []agentapplication.ChatMessage) []*schema.Message {
	projected := make([]*schema.Message, len(messages))
	for index, message := range messages {
		switch message.Role {
		case agentapplication.MessageRoleSystem:
			projected[index] = schema.SystemMessage(message.Content)
		case agentapplication.MessageRoleUser:
			projected[index] = schema.UserMessage(message.Content)
		case agentapplication.MessageRoleAssistant:
			projected[index] = schema.AssistantMessage(message.Content, nil)
		}
	}
	return projected
}

func validateWorkspaceAnalysisCandidateStreamChunk(chunk *schema.Message, retainedBytes int, usageSeen bool) error {
	if chunk == nil || len(chunk.ToolCalls) != 0 || chunk.ToolCallID != "" || chunk.ToolName != "" ||
		chunk.ReasoningContent != "" || chunk.Name != "" || !supportedRuntimeMessageExtra(chunk) || len(chunk.MultiContent) != 0 ||
		len(chunk.UserInputMultiContent) != 0 || len(chunk.AssistantGenMultiContent) != 0 ||
		!utf8.ValidString(chunk.Content) || retainedBytes > agentapplication.MaxAgentResultBytes-len(chunk.Content) {
		return candidateStreamInvalidError("eino workspace analysis candidate stream emitted an invalid bounded chunk")
	}
	if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil {
		if usageSeen {
			return candidateStreamInvalidError("eino workspace analysis candidate stream repeated token usage")
		}
		if _, known := runtimeMessageUsage(chunk); !known {
			return candidateStreamInvalidError("eino workspace analysis candidate stream emitted invalid token usage")
		}
	}
	return nil
}

func candidateStreamInvalidError(message string) error {
	return einoRuntimeError(foundation.ErrorConsistencyViolation, agentapplication.ErrorCodeWorkspaceAnalysisCandidateStreamInvalid, errors.New(message))
}

var _ agentapplication.WorkspaceAnalysisCandidateStreamRuntime = (*WorkspaceAnalysisCandidateStreamRuntime)(nil)
