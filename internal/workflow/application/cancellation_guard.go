package application

import (
	"context"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// CompositeCancellationSafetyGuard 将多个业务副作用的取消检查收敛到同一 Runtime 事务。
type CompositeCancellationSafetyGuard struct {
	guards []CancellationSafetyGuard
}

var _ CancellationSafetyGuard = (*CompositeCancellationSafetyGuard)(nil)

// NewCompositeCancellationSafetyGuard 创建按注册顺序执行的取消安全组合。
func NewCompositeCancellationSafetyGuard(guards ...CancellationSafetyGuard) (*CompositeCancellationSafetyGuard, error) {
	if len(guards) == 0 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_CANCELLATION_GUARD_EMPTY", false, errors.New("at least one cancellation guard is required"))
	}
	cloned := make([]CancellationSafetyGuard, len(guards))
	for index, guard := range guards {
		if nilCancellationGuard(guard) {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_CANCELLATION_GUARD_INVALID", false, errors.New("cancellation guard is nil"))
		}
		cloned[index] = guard
	}
	return &CompositeCancellationSafetyGuard{guards: cloned}, nil
}

// SafeToCancelWorkflowNode 只有全部业务边界都确认安全时才允许 Runtime 终态取消。
func (guard *CompositeCancellationSafetyGuard) SafeToCancelWorkflowNode(ctx context.Context, transaction any, nodeRunID foundation.ID) (bool, error) {
	if guard == nil || len(guard.guards) == 0 {
		return false, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_CANCELLATION_GUARD_UNAVAILABLE", true, errors.New("composite cancellation guard is unavailable"))
	}
	for _, item := range guard.guards {
		safe, err := item.SafeToCancelWorkflowNode(ctx, transaction, nodeRunID)
		if err != nil || !safe {
			return safe, err
		}
	}
	return true, nil
}

func nilCancellationGuard(guard CancellationSafetyGuard) bool {
	if guard == nil {
		return true
	}
	value := reflect.ValueOf(guard)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
