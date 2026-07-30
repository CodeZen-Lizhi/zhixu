package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// ExecutionContext 是从持久 Workflow Claim 构造并交给注册 Executor 的可信上下文。
type ExecutionContext struct {
	WorkspaceID       foundation.ID
	DefinitionID      foundation.ID
	DefinitionVersion int64
	DefinitionHash    string
	RunID             foundation.ID
	NodeKey           string
	NodeRunID         foundation.ID
	NodeAttemptID     foundation.ID
	NodeKind          string
	// ModelSettingsRevision 来自已持久化 Attempt；nil 明确表示 static/unmanaged 执行。
	ModelSettingsRevision *int64
	// NodeVersion 是 Claim 提交后的持久乐观锁版本，可与 Attempt/LeaseOwner 共同构造执行 Fence。
	NodeVersion        int64
	InputSchemaVersion int
	AttemptNo          int
	DispatchNo         int
	RetryNo            int
	LeaseOwner         string
	Input              json.RawMessage
}

// ExecutionResult is the project-owned successful result returned by an executor.
type ExecutionResult struct {
	Output    json.RawMessage
	HumanWait *HumanWaitResult
}

// HumanWaitResult is a trusted executor outcome that releases the lease and
// creates one durable Human Task instead of completing the node.
type HumanWaitResult struct {
	TaskID              foundation.ID
	ExpectedInputSchema json.RawMessage
	TargetVersion       int64
	ExpiresIn           time.Duration
}

// Executor executes one registered workflow node without exposing transport types.
type Executor interface {
	Execute(context.Context, ExecutionContext) (ExecutionResult, error)
}

// ValidationCatalog is the immutable allowlist of workflow schema versions and permissions.
type ValidationCatalog struct {
	schemaVersions map[int]struct{}
	permissions    map[domain.Permission]struct{}
}

// NewValidationCatalog constructs an immutable registry validation allowlist.
func NewValidationCatalog(schemaVersions []int, permissions []domain.Permission) (ValidationCatalog, error) {
	catalog := ValidationCatalog{
		schemaVersions: make(map[int]struct{}, len(schemaVersions)),
		permissions:    make(map[domain.Permission]struct{}, len(permissions)),
	}
	for _, version := range schemaVersions {
		if version < 1 {
			return ValidationCatalog{}, registryError(foundation.ErrorInvalidInput, "WORKFLOW_SCHEMA_VERSION_INVALID", errors.New("schema version must be positive"))
		}
		if _, exists := catalog.schemaVersions[version]; exists {
			return ValidationCatalog{}, registryError(foundation.ErrorVersionConflict, "WORKFLOW_SCHEMA_VERSION_DUPLICATE", errors.New("schema version is duplicated"))
		}
		catalog.schemaVersions[version] = struct{}{}
	}
	if len(catalog.schemaVersions) == 0 {
		return ValidationCatalog{}, registryError(foundation.ErrorInvalidInput, "WORKFLOW_SCHEMA_CATALOG_EMPTY", errors.New("schema catalog is empty"))
	}
	for _, permission := range permissions {
		parsed, err := capability.Parse(string(permission))
		if err != nil {
			return ValidationCatalog{}, registryError(foundation.ErrorInvalidInput, "WORKFLOW_PERMISSION_INVALID", errors.New("permission is not canonical"))
		}
		permission = parsed
		if _, exists := catalog.permissions[permission]; exists {
			return ValidationCatalog{}, registryError(foundation.ErrorVersionConflict, "WORKFLOW_PERMISSION_DUPLICATE", errors.New("permission is duplicated"))
		}
		catalog.permissions[permission] = struct{}{}
	}
	return catalog, nil
}

func (c ValidationCatalog) knowsSchema(version int) bool {
	_, ok := c.schemaVersions[version]
	return ok
}

func (c ValidationCatalog) knowsPermission(permission domain.Permission) bool {
	_, ok := c.permissions[permission]
	return ok
}

type executorKey struct {
	kind               string
	inputSchemaVersion int
}

// ExecutorRegistry stores executors by node kind and input schema version.
type ExecutorRegistry struct {
	mu        sync.RWMutex
	catalog   ValidationCatalog
	executors map[executorKey]Executor
	contracts map[executorKey]struct{}
	frozen    bool
}

// NewExecutorRegistry constructs an unfrozen executor registry.
func NewExecutorRegistry(catalog ValidationCatalog) (*ExecutorRegistry, error) {
	if len(catalog.schemaVersions) == 0 {
		return nil, registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_VALIDATION_CATALOG_MISSING", errors.New("validation catalog is not initialized"))
	}
	return &ExecutorRegistry{catalog: catalog, executors: make(map[executorKey]Executor), contracts: make(map[executorKey]struct{})}, nil
}

// RegisterContract 登记 API 与 Worker 共享的 Node kind/schema 契约，不构造可执行实现。
func (r *ExecutorRegistry) RegisterContract(kind string, inputSchemaVersion int) error {
	if r == nil {
		return registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_EXECUTOR_REGISTRY_MISSING", errors.New("executor registry is nil"))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key, err := r.validateRegistration(kind, inputSchemaVersion)
	if err != nil {
		return err
	}
	if _, exists := r.contracts[key]; exists {
		return registryError(foundation.ErrorVersionConflict, "WORKFLOW_EXECUTOR_CONTRACT_DUPLICATE", errors.New("executor contract is already registered"))
	}
	r.contracts[key] = struct{}{}
	return nil
}

// Register adds an executor under its stable node kind and input schema version.
func (r *ExecutorRegistry) Register(kind string, inputSchemaVersion int, executor Executor) error {
	if r == nil {
		return registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_EXECUTOR_REGISTRY_MISSING", errors.New("executor registry is nil"))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key, err := r.validateRegistration(kind, inputSchemaVersion)
	if err != nil {
		return err
	}
	if isNilExecutor(executor) {
		return registryError(foundation.ErrorInvalidInput, "WORKFLOW_EXECUTOR_INVALID", errors.New("executor kind or dependency is missing"))
	}
	if _, exists := r.executors[key]; exists {
		return registryError(foundation.ErrorVersionConflict, "WORKFLOW_EXECUTOR_DUPLICATE", errors.New("executor key is already registered"))
	}
	r.executors[key] = executor
	r.contracts[key] = struct{}{}
	return nil
}

func (r *ExecutorRegistry) validateRegistration(kind string, inputSchemaVersion int) (executorKey, error) {
	if r.frozen {
		return executorKey{}, registryError(foundation.ErrorVersionConflict, "WORKFLOW_EXECUTOR_REGISTRY_FROZEN", errors.New("executor registry is frozen"))
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return executorKey{}, registryError(foundation.ErrorInvalidInput, "WORKFLOW_EXECUTOR_INVALID", errors.New("executor kind is missing"))
	}
	if !r.catalog.knowsSchema(inputSchemaVersion) {
		return executorKey{}, registryError(foundation.ErrorInvalidInput, "WORKFLOW_EXECUTOR_SCHEMA_UNKNOWN", errors.New("executor input schema version is unknown"))
	}
	return executorKey{kind: kind, inputSchemaVersion: inputSchemaVersion}, nil
}

// Freeze makes the executor registry read-only.
func (r *ExecutorRegistry) Freeze() error {
	if r == nil {
		return registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_EXECUTOR_REGISTRY_MISSING", errors.New("executor registry is nil"))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return nil
	}
	if len(r.contracts) == 0 {
		return registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_EXECUTOR_REGISTRY_EMPTY", errors.New("executor registry is empty"))
	}
	r.frozen = true
	return nil
}

// SupportsContract 判断冻结 Registry 是否声明了指定 Node kind/schema 契约。
func (r *ExecutorRegistry) SupportsContract(kind string, inputSchemaVersion int) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.frozen {
		return false
	}
	_, exists := r.contracts[executorKey{kind: strings.TrimSpace(kind), inputSchemaVersion: inputSchemaVersion}]
	return exists
}

// Resolve returns the frozen executor registered for a node kind and input schema version.
func (r *ExecutorRegistry) Resolve(kind string, inputSchemaVersion int) (Executor, error) {
	if r == nil {
		return nil, registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_EXECUTOR_REGISTRY_MISSING", errors.New("executor registry is nil"))
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.frozen {
		return nil, registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_EXECUTOR_REGISTRY_NOT_FROZEN", errors.New("executor registry is not frozen"))
	}
	executor, exists := r.executors[executorKey{kind: strings.TrimSpace(kind), inputSchemaVersion: inputSchemaVersion}]
	if !exists {
		return nil, registryError(foundation.ErrorNonRetryableFailure, "WORKFLOW_EXECUTOR_NOT_REGISTERED", errors.New("executor is not registered"))
	}
	return executor, nil
}

func isNilExecutor(executor Executor) bool {
	if executor == nil {
		return true
	}
	value := reflect.ValueOf(executor)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func registryError(kind foundation.ErrorKind, code string, cause error) error {
	return foundation.NewError(kind, code, false, cause)
}
