package application

import (
	"context"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ScopedRuntimeStarter starts or exactly replays Workflow facts in a
// caller-owned transaction scope.
type ScopedRuntimeStarter interface {
	StartScoped(context.Context, foundation.TransactionScope, RuntimeStartRequest) (RuntimeStartResult, error)
}

// ScopedCancellationSafetyGuard 在当前 Workflow 事务中检查副作用的持久取消检查点。
// 未开始副作用的 Node 返回 true；查询失败返回错误，由 Runtime 拒绝取消。
type ScopedCancellationSafetyGuard interface {
	SafeToCancelWorkflowNodeScoped(context.Context, foundation.TransactionScope, foundation.ID) (bool, error)
}

// ScopedWorkflowTerminalHook 在当前 Workflow 事务中同步 Node 终态。
// 实现方只处理自己拥有的 Node，其他 Node 必须 no-op；不得提交事务或执行事务外副作用。
type ScopedWorkflowTerminalHook interface {
	OnWorkflowNodeTerminalScoped(context.Context, foundation.TransactionScope, WorkflowNodeTerminalEvent) error
}

// ScopedWorkflowControlHook 在当前 Workflow 事务中同步首次持久化的控制命令。
// 实现方只处理自己拥有的 Run，其他 Run 必须 no-op；不得提交事务或执行事务外副作用。
type ScopedWorkflowControlHook interface {
	OnWorkflowControlScoped(context.Context, foundation.TransactionScope, WorkflowControlEvent) error
}

// CompositeScopedCancellationSafetyGuard preserves registration order and
// permits cancellation only when every scoped guard agrees.
type CompositeScopedCancellationSafetyGuard struct {
	guards []ScopedCancellationSafetyGuard
}

// NewCompositeScopedCancellationSafetyGuard constructs the scoped guard chain.
func NewCompositeScopedCancellationSafetyGuard(guards ...ScopedCancellationSafetyGuard) (*CompositeScopedCancellationSafetyGuard, error) {
	if len(guards) == 0 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_SCOPED_CANCELLATION_GUARD_EMPTY", false, errors.New("at least one scoped cancellation guard is required"))
	}
	cloned := make([]ScopedCancellationSafetyGuard, len(guards))
	for index, guard := range guards {
		if nilScopedWorkflowDependency(guard) {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_SCOPED_CANCELLATION_GUARD_INVALID", false, errors.New("scoped cancellation guard is nil"))
		}
		cloned[index] = guard
	}
	return &CompositeScopedCancellationSafetyGuard{guards: cloned}, nil
}

// SafeToCancelWorkflowNodeScoped stops at the first error or unsafe result.
func (guard *CompositeScopedCancellationSafetyGuard) SafeToCancelWorkflowNodeScoped(ctx context.Context, scope foundation.TransactionScope, nodeRunID foundation.ID) (bool, error) {
	if guard == nil || len(guard.guards) == 0 {
		return false, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_SCOPED_CANCELLATION_GUARD_UNAVAILABLE", true, errors.New("scoped cancellation guard is unavailable"))
	}
	for _, item := range guard.guards {
		safe, err := item.SafeToCancelWorkflowNodeScoped(ctx, scope, nodeRunID)
		if err != nil || !safe {
			return safe, err
		}
	}
	return true, nil
}

// CompositeScopedWorkflowTerminalHook preserves terminal hook order.
type CompositeScopedWorkflowTerminalHook struct {
	hooks []ScopedWorkflowTerminalHook
}

// NewCompositeScopedWorkflowTerminalHook constructs the scoped terminal chain.
func NewCompositeScopedWorkflowTerminalHook(hooks ...ScopedWorkflowTerminalHook) (*CompositeScopedWorkflowTerminalHook, error) {
	if len(hooks) == 0 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_SCOPED_TERMINAL_HOOK_EMPTY", false, errors.New("at least one scoped terminal hook is required"))
	}
	cloned := make([]ScopedWorkflowTerminalHook, len(hooks))
	for index, hook := range hooks {
		if nilScopedWorkflowDependency(hook) {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_SCOPED_TERMINAL_HOOK_INVALID", false, errors.New("scoped terminal hook is nil"))
		}
		cloned[index] = hook
	}
	return &CompositeScopedWorkflowTerminalHook{hooks: cloned}, nil
}

// OnWorkflowNodeTerminalScoped stops at the first hook error.
func (hook *CompositeScopedWorkflowTerminalHook) OnWorkflowNodeTerminalScoped(ctx context.Context, scope foundation.TransactionScope, event WorkflowNodeTerminalEvent) error {
	if hook == nil || len(hook.hooks) == 0 {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_SCOPED_TERMINAL_HOOK_UNAVAILABLE", true, errors.New("scoped terminal hook is unavailable"))
	}
	for _, item := range hook.hooks {
		if err := item.OnWorkflowNodeTerminalScoped(ctx, scope, event); err != nil {
			return err
		}
	}
	return nil
}

// CompositeScopedWorkflowControlHook preserves control hook order.
type CompositeScopedWorkflowControlHook struct {
	hooks []ScopedWorkflowControlHook
}

// NewCompositeScopedWorkflowControlHook constructs the scoped control chain.
func NewCompositeScopedWorkflowControlHook(hooks ...ScopedWorkflowControlHook) (*CompositeScopedWorkflowControlHook, error) {
	if len(hooks) == 0 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_SCOPED_CONTROL_HOOK_EMPTY", false, errors.New("at least one scoped control hook is required"))
	}
	cloned := make([]ScopedWorkflowControlHook, len(hooks))
	for index, hook := range hooks {
		if nilScopedWorkflowDependency(hook) {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_SCOPED_CONTROL_HOOK_INVALID", false, errors.New("scoped control hook is nil"))
		}
		cloned[index] = hook
	}
	return &CompositeScopedWorkflowControlHook{hooks: cloned}, nil
}

// OnWorkflowControlScoped stops at the first hook error.
func (hook *CompositeScopedWorkflowControlHook) OnWorkflowControlScoped(ctx context.Context, scope foundation.TransactionScope, event WorkflowControlEvent) error {
	if hook == nil || len(hook.hooks) == 0 {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_SCOPED_CONTROL_HOOK_UNAVAILABLE", true, errors.New("scoped control hook is unavailable"))
	}
	for _, item := range hook.hooks {
		if err := item.OnWorkflowControlScoped(ctx, scope, event); err != nil {
			return err
		}
	}
	return nil
}

func nilScopedWorkflowDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ ScopedCancellationSafetyGuard = (*CompositeScopedCancellationSafetyGuard)(nil)
var _ ScopedWorkflowTerminalHook = (*CompositeScopedWorkflowTerminalHook)(nil)
var _ ScopedWorkflowControlHook = (*CompositeScopedWorkflowControlHook)(nil)
