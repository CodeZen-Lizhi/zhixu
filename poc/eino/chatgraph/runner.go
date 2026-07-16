package chatgraph

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/poc/eino/contract"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	graphName     = "zhixu_eino_chat_poc"
	modelNodeName = "chat_model"
	operationRun  = "eino_chat_graph.run"
)

// Runner adapts an Eino chat graph to the project-owned ChatRunner contract.
type Runner struct {
	runnable compose.Runnable[[]*schema.Message, *schema.Message]
}

var _ contract.ChatRunner = (*Runner)(nil)

// New compiles the smallest useful Eino graph: START -> ChatModel -> END.
func New(ctx context.Context, chatModel model.BaseChatModel) (*Runner, error) {
	if chatModel == nil {
		return nil, &contract.Error{
			Kind:      contract.ErrorInvalidInput,
			Operation: "eino_chat_graph.new",
			Cause:     errors.New("chat model is required"),
		}
	}

	graph := compose.NewGraph[[]*schema.Message, *schema.Message]()
	if err := graph.AddChatModelNode(modelNodeName, chatModel); err != nil {
		return nil, newConstructionError("add chat model node", err)
	}
	if err := graph.AddEdge(compose.START, modelNodeName); err != nil {
		return nil, newConstructionError("connect graph start", err)
	}
	if err := graph.AddEdge(modelNodeName, compose.END); err != nil {
		return nil, newConstructionError("connect graph end", err)
	}

	runnable, err := graph.Compile(ctx, compose.WithGraphName(graphName))
	if err != nil {
		return nil, newConstructionError("compile graph", err)
	}

	return &Runner{runnable: runnable}, nil
}

// Run validates and converts the project contract at the adapter boundary.
func (r *Runner) Run(ctx context.Context, request contract.ChatRequest) (contract.ChatResponse, error) {
	if r == nil || r.runnable == nil {
		return contract.ChatResponse{}, &contract.Error{
			Kind:      contract.ErrorNonRetryableFailure,
			Operation: operationRun,
			Cause:     errors.New("runner is not initialized"),
		}
	}

	messages, err := toEinoMessages(request.Messages)
	if err != nil {
		return contract.ChatResponse{}, err
	}

	output, err := r.runnable.Invoke(ctx, messages)
	if err != nil {
		return contract.ChatResponse{}, classifyRunError(err)
	}
	if output == nil {
		return contract.ChatResponse{}, &contract.Error{
			Kind:      contract.ErrorNonRetryableFailure,
			Operation: operationRun,
			Cause:     errors.New("model returned a nil message"),
		}
	}
	if output.Role != schema.Assistant {
		return contract.ChatResponse{}, &contract.Error{
			Kind:      contract.ErrorNonRetryableFailure,
			Operation: operationRun,
			Cause:     fmt.Errorf("model returned unsupported response role %q", output.Role),
		}
	}

	return contract.ChatResponse{
		Message: contract.Message{
			Role:    contract.RoleAssistant,
			Content: output.Content,
		},
	}, nil
}

func toEinoMessages(messages []contract.Message) ([]*schema.Message, error) {
	if len(messages) == 0 {
		return nil, invalidInput("at least one message is required")
	}

	converted := make([]*schema.Message, 0, len(messages))
	for index, message := range messages {
		if strings.TrimSpace(message.Content) == "" {
			return nil, invalidInput(fmt.Sprintf("message %d content is required", index))
		}

		switch message.Role {
		case contract.RoleSystem:
			converted = append(converted, schema.SystemMessage(message.Content))
		case contract.RoleUser:
			converted = append(converted, schema.UserMessage(message.Content))
		case contract.RoleAssistant:
			converted = append(converted, schema.AssistantMessage(message.Content, nil))
		default:
			return nil, invalidInput(fmt.Sprintf("message %d has unsupported role %q", index, message.Role))
		}
	}

	return converted, nil
}

func invalidInput(message string) error {
	return &contract.Error{
		Kind:      contract.ErrorInvalidInput,
		Operation: operationRun,
		Cause:     errors.New(message),
	}
}

func newConstructionError(action string, err error) error {
	return &contract.Error{
		Kind:      contract.ErrorNonRetryableFailure,
		Operation: "eino_chat_graph.new",
		Cause:     fmt.Errorf("%s: %w", action, err),
	}
}

func classifyRunError(err error) error {
	var classified *contract.Error
	if errors.As(err, &classified) {
		return classified
	}

	switch {
	case errors.Is(err, context.Canceled):
		return &contract.Error{
			Kind:      contract.ErrorNonRetryableFailure,
			Operation: operationRun,
			Cause:     err,
		}
	case errors.Is(err, context.DeadlineExceeded):
		return &contract.Error{
			Kind:      contract.ErrorRetryableFailure,
			Operation: operationRun,
			Retryable: true,
			Cause:     err,
		}
	default:
		return &contract.Error{
			Kind:      contract.ErrorNonRetryableFailure,
			Operation: operationRun,
			Cause:     err,
		}
	}
}
