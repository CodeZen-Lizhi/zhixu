package application

import (
	"context"
	"errors"
	"reflect"
	"sync"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	errorCodeFakeExhausted       = "AGENT_FAKE_RESPONSES_EXHAUSTED"
	errorCodeFakeRequestMismatch = "AGENT_FAKE_REQUEST_MISMATCH"
)

// DeterministicChatStep 描述测试 Fake 的一次精确调用行为。
type DeterministicChatStep struct {
	ExpectedRequest *ChatRequest
	Response        ChatResponse
	Err             error
	WaitForCancel   bool
}

// DeterministicChatModel 是并发安全且仅供测试显式注入的确定性 ChatModel。
type DeterministicChatModel struct {
	mu      sync.Mutex
	steps   []DeterministicChatStep
	calls   []ChatRequest
	started chan struct{}
	start   sync.Once
}

// NewDeterministicChatModel 用防御性副本创建按顺序消费响应的测试 Fake。
func NewDeterministicChatModel(steps ...DeterministicChatStep) *DeterministicChatModel {
	cloned := make([]DeterministicChatStep, len(steps))
	for i, step := range steps {
		cloned[i] = cloneDeterministicStep(step)
	}
	return &DeterministicChatModel{steps: cloned, started: make(chan struct{})}
}

// Chat 严格匹配预期请求，并精确消费一个配置步骤。
func (m *DeterministicChatModel) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if m == nil {
		return ChatResponse{}, applicationError(foundation.ErrorDependencyUnavailable, errorCodeChatModelMissing, false, errors.New("deterministic chat model is nil"))
	}
	m.mu.Lock()
	callNo := len(m.calls)
	m.calls = append(m.calls, cloneChatRequest(request))
	var step DeterministicChatStep
	if callNo < len(m.steps) {
		step = cloneDeterministicStep(m.steps[callNo])
	}
	m.mu.Unlock()
	m.start.Do(func() { close(m.started) })

	if callNo >= len(m.steps) {
		return ChatResponse{}, applicationError(foundation.ErrorNonRetryableFailure, errorCodeFakeExhausted, false, errors.New("deterministic fake has no remaining response"))
	}
	if step.ExpectedRequest != nil && !reflect.DeepEqual(*step.ExpectedRequest, request) {
		return ChatResponse{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeFakeRequestMismatch, false, errors.New("deterministic fake request differs from expectation"))
	}
	select {
	case <-ctx.Done():
		return ChatResponse{}, ctx.Err()
	default:
	}
	if step.WaitForCancel {
		<-ctx.Done()
		return ChatResponse{}, ctx.Err()
	}
	if step.Err != nil {
		return ChatResponse{}, step.Err
	}
	return cloneChatResponse(step.Response), nil
}

// Started 在 Fake 收到第一次调用后关闭。
func (m *DeterministicChatModel) Started() <-chan struct{} {
	if m == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return m.started
}

// CallCount 返回已记录的调用数。
func (m *DeterministicChatModel) CallCount() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// Calls 返回全部调用请求的防御性副本。
func (m *DeterministicChatModel) Calls() []ChatRequest {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	calls := make([]ChatRequest, len(m.calls))
	for i, request := range m.calls {
		calls[i] = cloneChatRequest(request)
	}
	return calls
}

func cloneDeterministicStep(step DeterministicChatStep) DeterministicChatStep {
	cloned := step
	if step.ExpectedRequest != nil {
		request := cloneChatRequest(*step.ExpectedRequest)
		cloned.ExpectedRequest = &request
	}
	cloned.Response = cloneChatResponse(step.Response)
	return cloned
}
