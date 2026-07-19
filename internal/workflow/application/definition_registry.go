package application

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

type definitionKey struct {
	key     string
	version int64
}

// ToolContractCatalog 按精确 name/version 解析已冻结的 Tool 契约。
//
// AllowedTools 只表达服务端 Workflow Node 的调用资格；模型请求与
// TRUSTED_WORKFLOW_ONLY 的入口隔离由 Tool ExecutionService 另行强制。
type ToolContractCatalog interface {
	ResolveToolDefinition(toolsdomain.ToolRef) (toolsdomain.Definition, error)
}

// DefinitionRegistry stores immutable server-owned workflow definitions.
type DefinitionRegistry struct {
	mu          sync.RWMutex
	catalog     ValidationCatalog
	executors   *ExecutorRegistry
	tools       ToolContractCatalog
	definitions map[definitionKey]domain.RegisteredDefinition
	frozen      bool
}

// NewDefinitionRegistry constructs an unfrozen definition registry.
func NewDefinitionRegistry(catalog ValidationCatalog, executors *ExecutorRegistry, toolCatalog ...ToolContractCatalog) (*DefinitionRegistry, error) {
	if len(catalog.schemaVersions) == 0 {
		return nil, registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_VALIDATION_CATALOG_MISSING", errors.New("validation catalog is not initialized"))
	}
	if executors == nil {
		return nil, registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_EXECUTOR_REGISTRY_MISSING", errors.New("executor registry is nil"))
	}
	if len(toolCatalog) > 1 || (len(toolCatalog) == 1 && isNilToolContractCatalog(toolCatalog[0])) {
		return nil, registryError(foundation.ErrorInvalidInput, "WORKFLOW_TOOL_CONTRACT_CATALOG_INVALID", errors.New("tool contract catalog is invalid"))
	}
	var tools ToolContractCatalog
	if len(toolCatalog) == 1 {
		tools = toolCatalog[0]
	}
	return &DefinitionRegistry{catalog: catalog, executors: executors, tools: tools, definitions: make(map[definitionKey]domain.RegisteredDefinition)}, nil
}

// Register validates and stores one canonical workflow definition.
func (r *DefinitionRegistry) Register(definition domain.RegisteredDefinition) error {
	if r == nil {
		return registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_DEFINITION_REGISTRY_MISSING", errors.New("definition registry is nil"))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return registryError(foundation.ErrorVersionConflict, "WORKFLOW_DEFINITION_REGISTRY_FROZEN", errors.New("definition registry is frozen"))
	}
	canonical, err := canonicalizeDefinition(definition)
	if err != nil {
		return err
	}
	key := definitionKey{key: canonical.Key, version: canonical.Version}
	if _, exists := r.definitions[key]; exists {
		return registryError(foundation.ErrorVersionConflict, "WORKFLOW_DEFINITION_DUPLICATE", errors.New("definition key and version are already registered"))
	}
	r.definitions[key] = canonical
	return nil
}

// Freeze validates external schema, permission and executor references and makes the registry read-only.
func (r *DefinitionRegistry) Freeze() error {
	if r == nil {
		return registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_DEFINITION_REGISTRY_MISSING", errors.New("definition registry is nil"))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return nil
	}
	if len(r.definitions) == 0 {
		return registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_DEFINITION_REGISTRY_EMPTY", errors.New("definition registry is empty"))
	}
	keys := make([]definitionKey, 0, len(r.definitions))
	for key := range r.definitions {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].key == keys[j].key {
			return keys[i].version < keys[j].version
		}
		return keys[i].key < keys[j].key
	})
	for _, key := range keys {
		definition := r.definitions[key]
		if !r.catalog.knowsSchema(definition.InputSchemaVersion) {
			return registryError(foundation.ErrorInvalidInput, "WORKFLOW_DEFINITION_SCHEMA_UNKNOWN", errors.New("definition input schema version is unknown"))
		}
		for _, node := range definition.Graph.Nodes {
			if !r.catalog.knowsSchema(node.InputSchemaVersion) || !r.catalog.knowsSchema(node.OutputSchemaVersion) {
				return registryError(foundation.ErrorInvalidInput, "WORKFLOW_DEFINITION_SCHEMA_UNKNOWN", errors.New("node schema version is unknown"))
			}
			for _, permission := range node.RequiredPermissions {
				if !r.catalog.knowsPermission(permission) {
					return registryError(foundation.ErrorPermissionDenied, "WORKFLOW_DEFINITION_PERMISSION_UNKNOWN", errors.New("node permission is not allowed"))
				}
			}
			if !r.executors.SupportsContract(node.Kind, node.InputSchemaVersion) {
				return registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_DEFINITION_EXECUTOR_MISSING", errors.New("node executor contract is not registered"))
			}
			if err := r.validateAllowedTools(definition, node); err != nil {
				return err
			}
		}
	}
	r.frozen = true
	return nil
}

// Resolve returns a deep copy of a frozen registered definition.
func (r *DefinitionRegistry) Resolve(key string, version int64) (domain.RegisteredDefinition, error) {
	if r == nil {
		return domain.RegisteredDefinition{}, registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_DEFINITION_REGISTRY_MISSING", errors.New("definition registry is nil"))
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.frozen {
		return domain.RegisteredDefinition{}, registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_DEFINITION_REGISTRY_NOT_FROZEN", errors.New("definition registry is not frozen"))
	}
	definition, exists := r.definitions[definitionKey{key: strings.TrimSpace(key), version: version}]
	if !exists {
		return domain.RegisteredDefinition{}, registryError(foundation.ErrorNotFound, "WORKFLOW_DEFINITION_NOT_REGISTERED", errors.New("definition is not registered"))
	}
	return cloneDefinition(definition), nil
}

func canonicalizeDefinition(definition domain.RegisteredDefinition) (domain.RegisteredDefinition, error) {
	definition.Key = strings.TrimSpace(definition.Key)
	definition.GraphHash = ""
	if definition.Key == "" || definition.Version < 1 || definition.InputSchemaVersion < 1 || len(definition.Graph.Nodes) == 0 {
		return domain.RegisteredDefinition{}, registryError(foundation.ErrorInvalidInput, "WORKFLOW_DEFINITION_INVALID", errors.New("definition identity, input schema or graph is invalid"))
	}
	nodes := make([]domain.NodeDefinition, len(definition.Graph.Nodes))
	seenNodes := make(map[string]struct{}, len(nodes))
	for i, node := range definition.Graph.Nodes {
		node.Key = strings.TrimSpace(node.Key)
		node.Kind = strings.TrimSpace(node.Kind)
		if node.Key == "" || node.Kind == "" || node.InputSchemaVersion < 1 || node.OutputSchemaVersion < 1 || !validRetryPolicy(node.RetryPolicy) {
			return domain.RegisteredDefinition{}, registryError(foundation.ErrorInvalidInput, "WORKFLOW_DEFINITION_NODE_INVALID", errors.New("node identity, schema or retry policy is invalid"))
		}
		if _, exists := seenNodes[node.Key]; exists {
			return domain.RegisteredDefinition{}, registryError(foundation.ErrorVersionConflict, "WORKFLOW_DEFINITION_NODE_DUPLICATE", errors.New("node key is duplicated"))
		}
		seenNodes[node.Key] = struct{}{}

		dependencies, err := canonicalStrings(node.Dependencies, "WORKFLOW_DEFINITION_DEPENDENCY_INVALID")
		if err != nil {
			return domain.RegisteredDefinition{}, err
		}
		permissions, err := canonicalPermissions(node.RequiredPermissions)
		if err != nil {
			return domain.RegisteredDefinition{}, err
		}
		allowedTools, err := canonicalToolRefs(node.AllowedTools)
		if err != nil {
			return domain.RegisteredDefinition{}, err
		}
		node.Dependencies = dependencies
		node.RequiredPermissions = permissions
		node.AllowedTools = allowedTools
		nodes[i] = node
	}
	for _, node := range nodes {
		for _, dependency := range node.Dependencies {
			if _, exists := seenNodes[dependency]; !exists {
				return domain.RegisteredDefinition{}, registryError(foundation.ErrorInvalidInput, "WORKFLOW_DEFINITION_DEPENDENCY_UNKNOWN", errors.New("node dependency is not registered"))
			}
		}
	}
	if hasDefinitionCycle(nodes) {
		return domain.RegisteredDefinition{}, registryError(foundation.ErrorInvalidInput, "WORKFLOW_DEFINITION_CYCLE", errors.New("workflow graph contains a cycle"))
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Key < nodes[j].Key })
	definition.Graph = domain.CanonicalGraph{Nodes: nodes}
	graphHash, err := ComputeCanonicalGraphHash(definition.Graph)
	if err != nil {
		return domain.RegisteredDefinition{}, err
	}
	definition.GraphHash = graphHash
	return cloneDefinition(definition), nil
}

// ComputeCanonicalGraphHash 对已经 canonicalize 的 Workflow Graph 计算稳定 SHA-256。
func ComputeCanonicalGraphHash(graph domain.CanonicalGraph) (string, error) {
	encoded, err := json.Marshal(graph)
	if err != nil {
		return "", registryError(foundation.ErrorNonRetryableFailure, "WORKFLOW_DEFINITION_CANONICAL_ENCODING_FAILED", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// DecodeCanonicalGraph 严格解析持久 Workflow Graph，拒绝未知字段和尾随 JSON。
func DecodeCanonicalGraph(document []byte) (domain.CanonicalGraph, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var graph domain.CanonicalGraph
	if err := decoder.Decode(&graph); err != nil || len(graph.Nodes) == 0 {
		return domain.CanonicalGraph{}, registryError(foundation.ErrorConsistencyViolation, "WORKFLOW_DEFINITION_GRAPH_INVALID", errors.New("persisted workflow graph is invalid"))
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return domain.CanonicalGraph{}, registryError(foundation.ErrorConsistencyViolation, "WORKFLOW_DEFINITION_GRAPH_INVALID", errors.New("persisted workflow graph contains trailing data"))
	}
	return graph, nil
}

func validRetryPolicy(policy domain.RetryPolicy) bool {
	if policy.MaxRetries < 0 || policy.BaseDelay < 0 || policy.MaxDelay < 0 {
		return false
	}
	if policy.MaxRetries == 0 {
		return policy.BaseDelay == 0 && policy.MaxDelay == 0
	}
	return policy.BaseDelay > 0 && policy.MaxDelay >= policy.BaseDelay
}

func canonicalStrings(values []string, code string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, registryError(foundation.ErrorInvalidInput, code, errors.New("value is empty"))
		}
		if _, exists := seen[value]; exists {
			return nil, registryError(foundation.ErrorVersionConflict, code, errors.New("value is duplicated"))
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func canonicalPermissions(values []domain.Permission) ([]domain.Permission, error) {
	if len(values) == 0 {
		return nil, nil
	}
	stringsToSort := make([]string, len(values))
	for i, value := range values {
		stringsToSort[i] = string(value)
	}
	canonical, err := canonicalStrings(stringsToSort, "WORKFLOW_DEFINITION_PERMISSION_INVALID")
	if err != nil {
		return nil, err
	}
	result := make([]domain.Permission, len(canonical))
	for i, value := range canonical {
		result[i] = domain.Permission(value)
	}
	return result, nil
}

func canonicalToolRefs(values []toolsdomain.ToolRef) ([]toolsdomain.ToolRef, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := append([]toolsdomain.ToolRef(nil), values...)
	for _, ref := range result {
		if err := ref.Validate(); err != nil {
			return nil, registryError(foundation.ErrorInvalidInput, "WORKFLOW_DEFINITION_TOOL_INVALID", errors.New("allowed tool reference is invalid"))
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Name == result[right].Name {
			return result[left].Version < result[right].Version
		}
		return result[left].Name < result[right].Name
	})
	deduplicated := result[:0]
	for _, ref := range result {
		if len(deduplicated) > 0 && deduplicated[len(deduplicated)-1].Name == ref.Name {
			if deduplicated[len(deduplicated)-1] == ref {
				continue
			}
			return nil, registryError(foundation.ErrorVersionConflict, "WORKFLOW_DEFINITION_TOOL_VERSION_AMBIGUOUS", errors.New("allowed tool name resolves to multiple versions"))
		}
		deduplicated = append(deduplicated, ref)
	}
	return deduplicated, nil
}

func (r *DefinitionRegistry) validateAllowedTools(definition domain.RegisteredDefinition, node domain.NodeDefinition) error {
	if len(node.AllowedTools) == 0 {
		return nil
	}
	if isNilToolContractCatalog(r.tools) {
		return registryError(foundation.ErrorDependencyUnavailable, "WORKFLOW_DEFINITION_TOOL_CATALOG_MISSING", errors.New("workflow definition allows tools without a contract catalog"))
	}
	for _, ref := range node.AllowedTools {
		contract, err := r.tools.ResolveToolDefinition(ref)
		if err != nil {
			return registryError(foundation.ErrorNotFound, "WORKFLOW_DEFINITION_TOOL_UNKNOWN", errors.New("allowed tool contract is not registered"))
		}
		if contract.Ref != ref || (contract.RequiredCapability != "" && !capability.IsKnown(contract.RequiredCapability)) ||
			(contract.InvocationPolicy != toolsdomain.InvocationModelRequestable && contract.InvocationPolicy != toolsdomain.InvocationTrustedWorkflowOnly) {
			return registryError(foundation.ErrorConsistencyViolation, "WORKFLOW_DEFINITION_TOOL_CONTRACT_INVALID", errors.New("tool catalog returned a drifting contract"))
		}
		if contract.RequiredCapability != "" && !containsPermission(node.RequiredPermissions, contract.RequiredCapability) {
			return registryError(foundation.ErrorPermissionDenied, "WORKFLOW_DEFINITION_TOOL_PERMISSION_MISMATCH", errors.New("node does not declare the tool required capability"))
		}
		if !containsWorkflowBinding(contract.AllowedWorkflows, definition.Key, definition.Version) {
			return registryError(foundation.ErrorPermissionDenied, "WORKFLOW_DEFINITION_TOOL_WORKFLOW_MISMATCH", errors.New("tool contract does not allow this workflow definition"))
		}
	}
	return nil
}

func containsPermission(permissions []domain.Permission, required capability.Capability) bool {
	for _, permission := range permissions {
		if permission == required {
			return true
		}
	}
	return false
}

func containsWorkflowBinding(bindings []toolsdomain.WorkflowBinding, key string, version int64) bool {
	for _, binding := range bindings {
		if binding.Key == key && binding.Version == version {
			return true
		}
	}
	return false
}

func isNilToolContractCatalog(catalog ToolContractCatalog) bool {
	if catalog == nil {
		return true
	}
	value := reflect.ValueOf(catalog)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func hasDefinitionCycle(nodes []domain.NodeDefinition) bool {
	dependencies := make(map[string][]string, len(nodes))
	for _, node := range nodes {
		dependencies[node.Key] = node.Dependencies
	}
	const (
		unvisited = iota
		visiting
		visited
	)
	states := make(map[string]int, len(nodes))
	var visit func(string) bool
	visit = func(node string) bool {
		switch states[node] {
		case visiting:
			return true
		case visited:
			return false
		}
		states[node] = visiting
		for _, dependency := range dependencies[node] {
			if visit(dependency) {
				return true
			}
		}
		states[node] = visited
		return false
	}
	for node := range dependencies {
		if states[node] == unvisited && visit(node) {
			return true
		}
	}
	return false
}

func cloneDefinition(definition domain.RegisteredDefinition) domain.RegisteredDefinition {
	cloned := definition
	cloned.Graph.Nodes = make([]domain.NodeDefinition, len(definition.Graph.Nodes))
	for i, node := range definition.Graph.Nodes {
		cloned.Graph.Nodes[i] = node
		cloned.Graph.Nodes[i].Dependencies = append([]string(nil), node.Dependencies...)
		cloned.Graph.Nodes[i].RequiredPermissions = append([]domain.Permission(nil), node.RequiredPermissions...)
		cloned.Graph.Nodes[i].AllowedTools = append([]toolsdomain.ToolRef(nil), node.AllowedTools...)
	}
	return cloned
}
