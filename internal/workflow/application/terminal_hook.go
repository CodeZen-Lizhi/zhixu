package application

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// WorkflowTerminalOutcome 表示 Workflow Node 已持久化的稳定终态结果。
type WorkflowTerminalOutcome string

const (
	// WorkflowTerminalOutcomeSucceeded 表示 Node 成功终结。
	WorkflowTerminalOutcomeSucceeded WorkflowTerminalOutcome = "SUCCEEDED"
	// WorkflowTerminalOutcomeFailed 表示 Node 失败终结。
	WorkflowTerminalOutcomeFailed WorkflowTerminalOutcome = "FAILED"
	// WorkflowTerminalOutcomeCancelled 表示 Node 取消终结。
	WorkflowTerminalOutcomeCancelled WorkflowTerminalOutcome = "CANCELLED"
)

// WorkflowNodeTerminalEvent 是 Runtime 在提交 Node 终态前发送给领域适配器的事务内事件。
type WorkflowNodeTerminalEvent struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	NodeKind      string
	// NodeAttemptID 在 delivery/checkpoint 终态中绑定当前 Attempt；尚未尝试的直接取消可为空。
	NodeAttemptID  foundation.ID
	Outcome        WorkflowTerminalOutcome
	FailureClass   domain.FailureClass
	FailureCode    string
	FailureSummary string
	// TerminalOutput 是成功 Node 已持久化的原始 JSON；失败或取消时为空。
	// 使用 string 保持事件可比较，便于精确验证 Hook 契约。
	TerminalOutput string
	TerminalAt     time.Time
}

// WorkflowTerminalHook 在 Runtime 当前事务内同步领域终态；transaction 是 opaque handle。
// 实现方只处理自己拥有的 Node，其余 Node 必须 no-op，且不得提交事务或执行事务外副作用。
type WorkflowTerminalHook interface {
	// OnWorkflowNodeTerminal 使用调用方持有的事务同步一个 Node 终态。
	OnWorkflowNodeTerminal(context.Context, any, WorkflowNodeTerminalEvent) error
}

// CompositeWorkflowTerminalHook 将多个领域终态 Hook 按注册顺序收敛到同一 Runtime 事务。
type CompositeWorkflowTerminalHook struct {
	hooks []WorkflowTerminalHook
}

var _ WorkflowTerminalHook = (*CompositeWorkflowTerminalHook)(nil)

// NewCompositeWorkflowTerminalHook 创建按注册顺序执行的终态 Hook 组合。
func NewCompositeWorkflowTerminalHook(hooks ...WorkflowTerminalHook) (*CompositeWorkflowTerminalHook, error) {
	if len(hooks) == 0 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_TERMINAL_HOOK_EMPTY", false, errors.New("at least one workflow terminal hook is required"))
	}
	cloned := make([]WorkflowTerminalHook, len(hooks))
	for index, hook := range hooks {
		if nilWorkflowTerminalHook(hook) {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_TERMINAL_HOOK_INVALID", false, errors.New("workflow terminal hook is nil"))
		}
		cloned[index] = hook
	}
	return &CompositeWorkflowTerminalHook{hooks: cloned}, nil
}

// OnWorkflowNodeTerminal 顺序调用全部领域 Hook，并在首个错误处停止。
func (hook *CompositeWorkflowTerminalHook) OnWorkflowNodeTerminal(ctx context.Context, transaction any, event WorkflowNodeTerminalEvent) error {
	if hook == nil || len(hook.hooks) == 0 {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_TERMINAL_HOOK_UNAVAILABLE", true, errors.New("composite workflow terminal hook is unavailable"))
	}
	for _, item := range hook.hooks {
		if err := item.OnWorkflowNodeTerminal(ctx, transaction, event); err != nil {
			return err
		}
	}
	return nil
}

func nilWorkflowTerminalHook(hook WorkflowTerminalHook) bool {
	if hook == nil {
		return true
	}
	value := reflect.ValueOf(hook)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
