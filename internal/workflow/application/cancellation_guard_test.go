package application

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCompositeScopedCancellationSafetyGuardRequiresEveryBoundary(t *testing.T) {
	first := &cancellationGuardFake{safe: true}
	second := &cancellationGuardFake{safe: false}
	guard, err := NewCompositeScopedCancellationSafetyGuard(first, second)
	if err != nil {
		t.Fatal(err)
	}
	safe, err := guard.SafeToCancelWorkflowNodeScoped(context.Background(), &workflowHookScope{name: "runtime-tx"}, id(1))
	if err != nil || safe || first.calls != 1 || second.calls != 1 {
		t.Fatalf("safe=%v err=%v first=%d second=%d", safe, err, first.calls, second.calls)
	}

	cause := errors.New("guard failed")
	first.err = cause
	second.calls = 0
	if _, err := guard.SafeToCancelWorkflowNodeScoped(context.Background(), &workflowHookScope{name: "runtime-tx"}, id(1)); !errors.Is(err, cause) || second.calls != 0 {
		t.Fatalf("error=%v second_calls=%d", err, second.calls)
	}
}

type cancellationGuardFake struct {
	safe  bool
	err   error
	calls int
}

func (guard *cancellationGuardFake) SafeToCancelWorkflowNodeScoped(context.Context, foundation.TransactionScope, foundation.ID) (bool, error) {
	guard.calls++
	return guard.safe, guard.err
}
