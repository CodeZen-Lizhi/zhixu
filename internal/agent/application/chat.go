package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MaxChatMessageBytes 是单条模型消息允许的最大 UTF-8 字节数。
	MaxChatMessageBytes = 512 * 1024
	// MaxChatMessages 是一次模型调用允许的最大消息数。
	MaxChatMessages = 32
	// MaxOutputTokens 是 Application 允许 Profile 声明的最大输出 Token 数。
	MaxOutputTokens = domain.MaxModelCallOutputTokens
)

const (
	errorCodeChatModelMissing       = "AGENT_CHAT_MODEL_MISSING"
	errorCodeChatRequestInvalid     = "AGENT_CHAT_REQUEST_INVALID"
	errorCodeChatResponseInvalid    = "AGENT_CHAT_RESPONSE_INVALID"
	errorCodeChatResponseMismatched = "AGENT_CHAT_RESPONSE_MODEL_MISMATCH"
	// ErrorCodeOperationCancelled 是调用方主动取消时返回的稳定错误码。
	ErrorCodeOperationCancelled = "AGENT_OPERATION_CANCELLED"
	// ErrorCodeOperationDeadline 是模型调用超过有界 deadline 时返回的稳定错误码。
	ErrorCodeOperationDeadline = "AGENT_OPERATION_DEADLINE_EXCEEDED"
)

// MessageRole 是项目自有的模型消息角色，不暴露 Provider SDK 类型。
type MessageRole string

const (
	// MessageRoleSystem 表示受信任的系统策略。
	MessageRoleSystem MessageRole = "system"
	// MessageRoleUser 表示用户或任务输入。
	MessageRoleUser MessageRole = "user"
	// MessageRoleAssistant 表示显式保留的模型历史消息。
	MessageRoleAssistant MessageRole = "assistant"
)

// ChatMessage 是发送给模型的有界 UTF-8 消息。
type ChatMessage struct {
	Role    MessageRole
	Content string
}

// ChatRequest 是一次供应商无关且版本已冻结的模型调用请求。
type ChatRequest struct {
	Phase           domain.ModelCallPhase
	ProfileRef      domain.ModelProfileRef
	PromptRef       domain.PromptRef
	SchemaRef       domain.SchemaRef
	Model           domain.ModelRef
	Messages        []ChatMessage
	OutputSchema    []byte
	MaxOutputTokens int
}

// ChatResponse 是模型返回的原始结构化输出、实际模型身份和 Token 使用量。
type ChatResponse struct {
	Model   domain.ModelRef
	Content []byte
	Usage   domain.TokenUsage
}

// ChatModel 执行一次模型调用；实现不得在内部重试或切换模型。
type ChatModel interface {
	// Chat 返回单次 Provider 响应或稳定分类错误。
	Chat(context.Context, ChatRequest) (ChatResponse, error)
}

// ValidateChatRequest 校验一次模型调用的版本引用、消息、Schema 和输出上限。
func ValidateChatRequest(request ChatRequest) error {
	if err := request.ProfileRef.Validate(); err != nil {
		return applicationError(foundation.ErrorInvalidInput, errorCodeChatRequestInvalid, false, err)
	}
	if err := request.PromptRef.Validate(); err != nil {
		return applicationError(foundation.ErrorInvalidInput, errorCodeChatRequestInvalid, false, err)
	}
	if err := request.SchemaRef.Validate(); err != nil {
		return applicationError(foundation.ErrorInvalidInput, errorCodeChatRequestInvalid, false, err)
	}
	if err := request.Model.Validate(); err != nil {
		return applicationError(foundation.ErrorInvalidInput, errorCodeChatRequestInvalid, false, err)
	}
	if !validChatPhase(request.Phase) || len(request.Messages) == 0 || len(request.Messages) > MaxChatMessages ||
		request.MaxOutputTokens <= 0 || request.MaxOutputTokens > MaxOutputTokens {
		return applicationError(foundation.ErrorInvalidInput, errorCodeChatRequestInvalid, false, errors.New("chat request phase, messages, schema, or output token limit is invalid"))
	}
	if err := validateSchemaDocument(request.OutputSchema); err != nil {
		return applicationError(foundation.ErrorInvalidInput, errorCodeChatRequestInvalid, false, err)
	}
	for index, message := range request.Messages {
		if (message.Role != MessageRoleSystem && message.Role != MessageRoleUser && message.Role != MessageRoleAssistant) || strings.TrimSpace(message.Content) == "" ||
			len(message.Content) > MaxChatMessageBytes || !utf8.ValidString(message.Content) {
			return applicationError(foundation.ErrorInvalidInput, errorCodeChatRequestInvalid, false, errors.New("chat request contains an invalid message"))
		}
		if (index == 0 && message.Role != MessageRoleSystem) || (index > 0 && message.Role == MessageRoleSystem) {
			return applicationError(foundation.ErrorInvalidInput, errorCodeChatRequestInvalid, false, errors.New("chat request must contain exactly one leading system policy"))
		}
	}
	return nil
}

// ValidateChatResponse 校验响应模型回显、原始内容和精确 Token usage。
func ValidateChatResponse(request ChatRequest, response ChatResponse) error {
	if err := response.Model.Validate(); err != nil {
		return applicationError(foundation.ErrorConsistencyViolation, errorCodeChatResponseInvalid, false, err)
	}
	if response.Model != request.Model {
		return applicationError(foundation.ErrorConsistencyViolation, errorCodeChatResponseMismatched, false, errors.New("chat response model identity differs from the frozen request"))
	}
	if len(response.Content) == 0 {
		return applicationError(foundation.ErrorConsistencyViolation, errorCodeChatResponseInvalid, false, errors.New("chat response content is empty"))
	}
	if err := response.Usage.Validate(); err != nil || response.Usage.TotalTokens == 0 {
		if err == nil {
			err = errors.New("chat response token usage is empty")
		}
		return applicationError(foundation.ErrorConsistencyViolation, errorCodeChatResponseInvalid, false, err)
	}
	return nil
}

func cloneChatRequest(request ChatRequest) ChatRequest {
	cloned := request
	cloned.Messages = append([]ChatMessage(nil), request.Messages...)
	cloned.OutputSchema = append([]byte(nil), request.OutputSchema...)
	return cloned
}

func cloneChatResponse(response ChatResponse) ChatResponse {
	cloned := response
	cloned.Content = append([]byte(nil), response.Content...)
	return cloned
}

func isNilChatModel(model ChatModel) bool {
	if model == nil {
		return true
	}
	value := reflect.ValueOf(model)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func operationContextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return applicationError(foundation.ErrorNonRetryableFailure, ErrorCodeOperationCancelled, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return applicationError(foundation.ErrorRetryableFailure, ErrorCodeOperationDeadline, true, err)
	}
	return err
}

func validChatPhase(phase domain.ModelCallPhase) bool {
	switch phase {
	case domain.ModelCallPlan, domain.ModelCallAgent, domain.ModelCallAnswer, domain.ModelCallInitial,
		domain.ModelCallRepair, domain.ModelCallReduced, domain.ModelCallReview:
		return true
	default:
		return false
	}
}

func minPositiveDuration(left, right time.Duration) time.Duration {
	if left <= 0 {
		return right
	}
	if right <= 0 || left < right {
		return left
	}
	return right
}
