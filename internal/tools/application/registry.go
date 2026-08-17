package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	errorCodeRegistryMissing            = "TOOL_REGISTRY_MISSING"
	errorCodeRegistryFrozen             = "TOOL_REGISTRY_FROZEN"
	errorCodeRegistryNotFrozen          = "TOOL_REGISTRY_NOT_FROZEN"
	errorCodeRegistryEmpty              = "TOOL_REGISTRY_EMPTY"
	errorCodeContractInvalid            = "TOOL_CONTRACT_INVALID"
	errorCodeContractDuplicate          = "TOOL_CONTRACT_DUPLICATE"
	errorCodeContractNotFound           = "TOOL_NOT_REGISTERED"
	errorCodeExecutorInvalid            = "TOOL_EXECUTOR_INVALID"
	errorCodeExecutorDuplicate          = "TOOL_EXECUTOR_DUPLICATE"
	errorCodeExecutorNotRegistered      = "TOOL_CAPABILITY_UNAVAILABLE"
	errorCodeExecutorRegistryIncomplete = "TOOL_EXECUTOR_REGISTRY_INCOMPLETE"
	errorCodeDefinitionReferenceDrift   = "TOOL_DEFINITION_REFERENCE_DRIFT"
)

// DocumentDecoder 对一个精确 Schema 版本执行严格 JSON 与业务校验。
type DocumentDecoder func([]byte) (json.RawMessage, error)

// Contract 把不可变 Tool Definition 与唯一输入输出 Decoder 绑定。
type Contract struct {
	Definition   domain.Definition
	DecodeInput  DocumentDecoder
	DecodeOutput DocumentDecoder
}

// ExecutorRequest 是 Registry 校验后交给 typed Tool Adapter 的请求。
type ExecutorRequest struct {
	Identity       domain.TrustedExecutionIdentity
	Tool           domain.ToolRef
	Arguments      json.RawMessage
	Reason         string
	IdempotencyKey string
}

// ExecutorResult 是 typed Tool Adapter 返回的待验证结果和稳定引用。
type ExecutorResult struct {
	Output         json.RawMessage
	ResultRef      string
	SideEffectType string
	SideEffectID   string
	PrivateBinding *ExecutorPrivateBinding `json:"-"`
}

// ExecutorPrivateBinding 是仅供显式 opt-in Tool 回执持久化的 server-only 文档。
type ExecutorPrivateBinding struct {
	Schema   domain.SchemaRef
	Document json.RawMessage `json:"-"`
}

// String 只投影 Schema 和长度，禁止调试日志输出私有文档。
func (binding ExecutorPrivateBinding) String() string {
	return fmt.Sprintf("ExecutorPrivateBinding{schema:%s@%d bytes:%d}", binding.Schema.ID, binding.Schema.Version, len(binding.Document))
}

// GoString 避免 %#v 绕过私有绑定的安全 String 投影。
func (binding ExecutorPrivateBinding) GoString() string { return binding.String() }

// Executor 执行一个已经通过 Registry、Policy 和 Schema 校验的 Tool。
type Executor interface {
	Execute(context.Context, ExecutorRequest) (ExecutorResult, error)
}

// ResultReceiptLoader 从权威领域 receipt 恢复已成功 Tool Call 的 canonical 输出，不重复副作用。
type ResultReceiptLoader interface {
	// LoadResultReceipt 只读取已存在的权威结果；不得重新执行写入、网络或其他副作用。
	LoadResultReceipt(context.Context, ExecutorRequest, domain.ToolCall) (ExecutorResult, error)
}

type registryKey struct {
	name    string
	version int64
}

// Registry 保存版本化 Tool contract，并在 Worker 模式下保存真实 Executor。
type Registry struct {
	mu               sync.RWMutex
	requireExecutors bool
	contracts        map[registryKey]Contract
	executors        map[registryKey]Executor
	frozen           bool
}

// NewContractRegistry 创建 API 使用的 contract-only Registry。
func NewContractRegistry() *Registry {
	return newRegistry(false)
}

// NewExecutionRegistry 创建 Worker 使用、冻结时要求真实 Executor 的 Registry。
func NewExecutionRegistry() *Registry {
	return newRegistry(true)
}

func newRegistry(requireExecutors bool) *Registry {
	return &Registry{
		requireExecutors: requireExecutors,
		contracts:        make(map[registryKey]Contract),
		executors:        make(map[registryKey]Executor),
	}
}

// RegisterContract 注册一个精确 Tool 版本与输入输出 Decoder。
func (registry *Registry) RegisterContract(contract Contract) error {
	if registry == nil {
		return registryError(foundation.ErrorDependencyUnavailable, errorCodeRegistryMissing, errors.New("tool registry is nil"))
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.frozen {
		return registryError(foundation.ErrorVersionConflict, errorCodeRegistryFrozen, errors.New("tool registry is frozen"))
	}
	canonical, err := canonicalContract(contract)
	if err != nil {
		return err
	}
	key := keyOf(canonical.Definition.Ref)
	if _, exists := registry.contracts[key]; exists {
		return registryError(foundation.ErrorVersionConflict, errorCodeContractDuplicate, errors.New("tool name and version are already registered"))
	}
	registry.contracts[key] = canonical
	return nil
}

// RegisterExecutor 为已注册的精确 Tool contract 注入真实 Adapter。
func (registry *Registry) RegisterExecutor(ref domain.ToolRef, executor Executor) error {
	if registry == nil {
		return registryError(foundation.ErrorDependencyUnavailable, errorCodeRegistryMissing, errors.New("tool registry is nil"))
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.frozen {
		return registryError(foundation.ErrorVersionConflict, errorCodeRegistryFrozen, errors.New("tool registry is frozen"))
	}
	if ref.Validate() != nil || isNilInterface(executor) {
		return registryError(foundation.ErrorInvalidInput, errorCodeExecutorInvalid, errors.New("tool executor or reference is invalid"))
	}
	key := keyOf(ref)
	if _, exists := registry.contracts[key]; !exists {
		return registryError(foundation.ErrorNotFound, errorCodeContractNotFound, errors.New("tool contract is not registered"))
	}
	if _, exists := registry.executors[key]; exists {
		return registryError(foundation.ErrorVersionConflict, errorCodeExecutorDuplicate, errors.New("tool executor is already registered"))
	}
	registry.executors[key] = executor
	return nil
}

// Freeze 校验 Registry 完整性并切换为只读。
func (registry *Registry) Freeze() error {
	if registry == nil {
		return registryError(foundation.ErrorDependencyUnavailable, errorCodeRegistryMissing, errors.New("tool registry is nil"))
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.frozen {
		return nil
	}
	if len(registry.contracts) == 0 {
		return registryError(foundation.ErrorDependencyUnavailable, errorCodeRegistryEmpty, errors.New("tool registry has no contracts"))
	}
	if registry.requireExecutors {
		for key := range registry.contracts {
			if _, exists := registry.executors[key]; !exists {
				return registryError(foundation.ErrorDependencyUnavailable, errorCodeExecutorRegistryIncomplete, errors.New("enabled tool contract has no executor"))
			}
		}
	}
	registry.frozen = true
	return nil
}

// ResolveContract 从冻结 Registry 解析一个精确版本的深拷贝 contract。
func (registry *Registry) ResolveContract(ref domain.ToolRef) (Contract, error) {
	if registry == nil {
		return Contract{}, registryError(foundation.ErrorDependencyUnavailable, errorCodeRegistryMissing, errors.New("tool registry is nil"))
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if !registry.frozen {
		return Contract{}, registryError(foundation.ErrorDependencyUnavailable, errorCodeRegistryNotFrozen, errors.New("tool registry is not frozen"))
	}
	contract, exists := registry.contracts[keyOf(ref)]
	if !exists {
		return Contract{}, registryError(foundation.ErrorNotFound, errorCodeContractNotFound, errors.New("tool contract is not registered"))
	}
	if contract.Definition.Ref != ref {
		return Contract{}, registryError(foundation.ErrorConsistencyViolation, errorCodeDefinitionReferenceDrift, errors.New("resolved tool reference differs from requested version"))
	}
	return cloneContract(contract), nil
}

// ResolveToolDefinition 为 Workflow Freeze 返回已冻结 Tool Definition 的深拷贝。
func (registry *Registry) ResolveToolDefinition(ref domain.ToolRef) (domain.Definition, error) {
	contract, err := registry.ResolveContract(ref)
	if err != nil {
		return domain.Definition{}, err
	}
	return contract.Definition, nil
}

// ResolveExecutor 从冻结 Worker Registry 解析一个精确版本的真实 Executor。
func (registry *Registry) ResolveExecutor(ref domain.ToolRef) (Executor, error) {
	if registry == nil {
		return nil, registryError(foundation.ErrorDependencyUnavailable, errorCodeRegistryMissing, errors.New("tool registry is nil"))
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if !registry.frozen {
		return nil, registryError(foundation.ErrorDependencyUnavailable, errorCodeRegistryNotFrozen, errors.New("tool registry is not frozen"))
	}
	executor, exists := registry.executors[keyOf(ref)]
	if !exists {
		return nil, registryError(foundation.ErrorNonRetryableFailure, errorCodeExecutorNotRegistered, errors.New("tool executor is not registered"))
	}
	return executor, nil
}

// ListContracts 返回冻结 Registry 中按 name/version 稳定排序的独立 contract 副本。
func (registry *Registry) ListContracts() ([]Contract, error) {
	if registry == nil {
		return nil, registryError(foundation.ErrorDependencyUnavailable, errorCodeRegistryMissing, errors.New("tool registry is nil"))
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if !registry.frozen {
		return nil, registryError(foundation.ErrorDependencyUnavailable, errorCodeRegistryNotFrozen, errors.New("tool registry is not frozen"))
	}
	result := make([]Contract, 0, len(registry.contracts))
	for _, contract := range registry.contracts {
		result = append(result, cloneContract(contract))
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Definition.Ref.Name == result[right].Definition.Ref.Name {
			return result[left].Definition.Ref.Version < result[right].Definition.Ref.Version
		}
		return result[left].Definition.Ref.Name < result[right].Definition.Ref.Name
	})
	return result, nil
}

func canonicalContract(contract Contract) (Contract, error) {
	if contract.DecodeInput == nil || contract.DecodeOutput == nil {
		return Contract{}, registryError(foundation.ErrorInvalidInput, errorCodeContractInvalid, errors.New("tool contract decoder is missing"))
	}
	definition, err := domain.CanonicalizeDefinition(contract.Definition)
	if err != nil {
		return Contract{}, err
	}
	return Contract{Definition: definition, DecodeInput: contract.DecodeInput, DecodeOutput: contract.DecodeOutput}, nil
}

func cloneContract(contract Contract) Contract {
	cloned := contract
	cloned.Definition.InputSchemaDocument = append(json.RawMessage(nil), contract.Definition.InputSchemaDocument...)
	cloned.Definition.OutputSchemaDocument = append(json.RawMessage(nil), contract.Definition.OutputSchemaDocument...)
	cloned.Definition.SensitiveFields = append([]string(nil), contract.Definition.SensitiveFields...)
	cloned.Definition.AllowedWorkflows = append([]domain.WorkflowBinding(nil), contract.Definition.AllowedWorkflows...)
	return cloned
}

func keyOf(ref domain.ToolRef) registryKey {
	return registryKey{name: ref.Name, version: ref.Version}
}

func isNilInterface(value any) bool {
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

func registryError(kind foundation.ErrorKind, code string, cause error) error {
	return foundation.NewError(kind, code, false, cause)
}
