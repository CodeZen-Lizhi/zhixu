package fake

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ChatBehavior configures one deterministic Fake ChatModel behavior.
type ChatBehavior struct {
	Response      string
	ResponseRole  schema.RoleType
	Err           error
	WaitForCancel bool
}

// ChatModel is a concurrency-safe deterministic Eino BaseChatModel fake.
type ChatModel struct {
	behavior ChatBehavior
	started  chan struct{}
	start    sync.Once

	mu        sync.Mutex
	callCount int
	lastInput []*schema.Message
}

var _ model.BaseChatModel = (*ChatModel)(nil)

// NewChatModel creates a deterministic model for graph and adapter tests.
func NewChatModel(behavior ChatBehavior) *ChatModel {
	return &ChatModel{
		behavior: behavior,
		started:  make(chan struct{}),
	}
}

// Generate returns the configured response, failure, or waits for cancellation.
func (m *ChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.recordCall(input)
	m.start.Do(func() { close(m.started) })

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if m.behavior.WaitForCancel {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if m.behavior.Err != nil {
		return nil, m.behavior.Err
	}

	role := m.behavior.ResponseRole
	if role == "" {
		role = schema.Assistant
	}
	return &schema.Message{Role: role, Content: m.behavior.Response}, nil
}

// Stream satisfies Eino BaseChatModel and emits the same deterministic result.
func (m *ChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

// Started closes when Generate or Stream reaches the fake model.
func (m *ChatModel) Started() <-chan struct{} {
	return m.started
}

// CallCount returns the number of model invocations.
func (m *ChatModel) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// LastInput returns a defensive copy of the last model input.
func (m *ChatModel) LastInput() []*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneMessages(m.lastInput)
}

func (m *ChatModel) recordCall(input []*schema.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	m.lastInput = cloneMessages(input)
}

func cloneMessages(messages []*schema.Message) []*schema.Message {
	cloned := make([]*schema.Message, len(messages))
	for index, message := range messages {
		if message == nil {
			continue
		}
		copyOfMessage := *message
		cloned[index] = &copyOfMessage
	}
	return cloned
}
