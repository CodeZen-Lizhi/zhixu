package application

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

type registryTestExecutor struct{}

func (registryTestExecutor) Execute(context.Context, ExecutionContext) (ExecutionResult, error) {
	return ExecutionResult{}, nil
}

type nilRegistryTestExecutor struct{}

func (*nilRegistryTestExecutor) Execute(context.Context, ExecutionContext) (ExecutionResult, error) {
	return ExecutionResult{}, nil
}

func TestExecutorRegistryRejectsDuplicateAndBecomesImmutableAfterFreeze(t *testing.T) {
	catalog, err := NewValidationCatalog([]int{1}, []domain.Permission{domain.PermissionReadLocal})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("deterministic.test", 1, registryTestExecutor{}); err != nil {
		t.Fatal(err)
	}
	assertWorkflowErrorCode(t, registry.Register("deterministic.test", 1, registryTestExecutor{}), "WORKFLOW_EXECUTOR_DUPLICATE")
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve("deterministic.test", 1); err != nil {
		t.Fatal(err)
	}
	assertWorkflowErrorCode(t, registry.Register("deterministic.other", 1, registryTestExecutor{}), "WORKFLOW_EXECUTOR_REGISTRY_FROZEN")
}

func TestExecutorRegistryRejectsInvalidRegistrationAndUnknownExecutor(t *testing.T) {
	catalog, err := NewValidationCatalog([]int{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkflowErrorCode(t, registry.Register("", 1, registryTestExecutor{}), "WORKFLOW_EXECUTOR_INVALID")
	assertWorkflowErrorCode(t, registry.Register("deterministic.test", 2, registryTestExecutor{}), "WORKFLOW_EXECUTOR_SCHEMA_UNKNOWN")
	var missing *nilRegistryTestExecutor
	assertWorkflowErrorCode(t, registry.Register("deterministic.test", 1, missing), "WORKFLOW_EXECUTOR_INVALID")
	if _, err := registry.Resolve("deterministic.test", 1); err == nil {
		t.Fatal("unfrozen registry resolved an executor")
	}
	if err := registry.Register("deterministic.test", 1, registryTestExecutor{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	_, err = registry.Resolve("deterministic.missing", 1)
	assertWorkflowErrorCode(t, err, "WORKFLOW_EXECUTOR_NOT_REGISTERED")
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorNonRetryableFailure || classified.Retryable {
		t.Fatalf("unknown executor classification = %#v", classified)
	}
}

func assertWorkflowErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}
