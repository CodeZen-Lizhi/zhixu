package application

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestDefinitionRegistryCanonicalHashIsStableAndResolveReturnsDeepCopy(t *testing.T) {
	catalog := testValidationCatalog(t)
	executors := testFrozenExecutors(t, catalog, "deterministic.test")
	registry, err := NewDefinitionRegistry(catalog, executors)
	if err != nil {
		t.Fatal(err)
	}
	left := definitionFixture("flow-left", []domain.NodeDefinition{
		{Key: "finish", Kind: "deterministic.test", Dependencies: []string{"enrich", "load"}, InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy(), RequiredPermissions: []domain.Permission{domain.PermissionReadExternal, domain.PermissionReadLocal}},
		{Key: "load", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
		{Key: "enrich", Kind: "deterministic.test", Dependencies: []string{"load"}, InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
	})
	right := definitionFixture("flow-right", []domain.NodeDefinition{
		{Key: "enrich", Kind: "deterministic.test", Dependencies: []string{"load"}, InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
		{Key: "finish", Kind: "deterministic.test", Dependencies: []string{"load", "enrich"}, InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy(), RequiredPermissions: []domain.Permission{domain.PermissionReadLocal, domain.PermissionReadExternal}},
		{Key: "load", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
	})
	if err := registry.Register(left); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(right); err != nil {
		t.Fatal(err)
	}
	different := definitionFixture("flow-different", []domain.NodeDefinition{{Key: "load", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()}})
	if err := registry.Register(different); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	leftResolved, err := registry.Resolve("flow-left", 1)
	if err != nil {
		t.Fatal(err)
	}
	rightResolved, err := registry.Resolve("flow-right", 1)
	if err != nil {
		t.Fatal(err)
	}
	if leftResolved.GraphHash == "" || leftResolved.GraphHash != rightResolved.GraphHash {
		t.Fatalf("graph hashes differ: %q != %q", leftResolved.GraphHash, rightResolved.GraphHash)
	}
	differentResolved, err := registry.Resolve("flow-different", 1)
	if err != nil {
		t.Fatal(err)
	}
	if differentResolved.GraphHash == leftResolved.GraphHash {
		t.Fatalf("different graph reused hash %q", leftResolved.GraphHash)
	}
	leftResolved.Graph.Nodes[0].Key = "mutated"
	leftResolved.Graph.Nodes[1].Dependencies[0] = "mutated"
	leftResolved.Graph.Nodes[1].RequiredPermissions[0] = "MUTATED"
	again, err := registry.Resolve("flow-left", 1)
	if err != nil {
		t.Fatal(err)
	}
	if again.Graph.Nodes[0].Key != "enrich" || again.Graph.Nodes[1].Dependencies[0] != "enrich" || again.Graph.Nodes[1].RequiredPermissions[0] != domain.PermissionReadExternal {
		t.Fatalf("registry state was mutated through resolve: %#v", again.Graph.Nodes)
	}
	assertWorkflowErrorCode(t, registry.Register(definitionFixture("late", left.Graph.Nodes)), "WORKFLOW_DEFINITION_REGISTRY_FROZEN")
}

func TestDefinitionRegistryRejectsDuplicateIdentityNodeAndCycle(t *testing.T) {
	catalog := testValidationCatalog(t)
	executors := testFrozenExecutors(t, catalog, "deterministic.test")

	registry, _ := NewDefinitionRegistry(catalog, executors)
	valid := definitionFixture("flow", []domain.NodeDefinition{{Key: "one", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()}})
	if err := registry.Register(valid); err != nil {
		t.Fatal(err)
	}
	assertWorkflowErrorCode(t, registry.Register(valid), "WORKFLOW_DEFINITION_DUPLICATE")

	registry, _ = NewDefinitionRegistry(catalog, executors)
	duplicateNode := definitionFixture("duplicate-node", []domain.NodeDefinition{
		{Key: "same", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
		{Key: "same", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
	})
	assertWorkflowErrorCode(t, registry.Register(duplicateNode), "WORKFLOW_DEFINITION_NODE_DUPLICATE")

	registry, _ = NewDefinitionRegistry(catalog, executors)
	cycle := definitionFixture("cycle", []domain.NodeDefinition{
		{Key: "one", Kind: "deterministic.test", Dependencies: []string{"two"}, InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
		{Key: "two", Kind: "deterministic.test", Dependencies: []string{"one"}, InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
	})
	assertWorkflowErrorCode(t, registry.Register(cycle), "WORKFLOW_DEFINITION_CYCLE")

	registry, _ = NewDefinitionRegistry(catalog, executors)
	missingDependency := definitionFixture("missing-dependency", []domain.NodeDefinition{{Key: "one", Kind: "deterministic.test", Dependencies: []string{"missing"}, InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()}})
	assertWorkflowErrorCode(t, registry.Register(missingDependency), "WORKFLOW_DEFINITION_DEPENDENCY_UNKNOWN")
}

func TestDefinitionRegistryFreezeRejectsUnknownSchemaPermissionAndExecutor(t *testing.T) {
	catalog := testValidationCatalog(t)
	executors := testFrozenExecutors(t, catalog, "deterministic.test")
	tests := []struct {
		name       string
		definition domain.RegisteredDefinition
		code       string
	}{
		{name: "definition input schema", definition: func() domain.RegisteredDefinition {
			d := definitionFixture("unknown-definition-schema", oneNode("deterministic.test"))
			d.InputSchemaVersion = 2
			return d
		}(), code: "WORKFLOW_DEFINITION_SCHEMA_UNKNOWN"},
		{name: "node output schema", definition: func() domain.RegisteredDefinition {
			d := definitionFixture("unknown-node-schema", oneNode("deterministic.test"))
			d.Graph.Nodes[0].OutputSchemaVersion = 2
			return d
		}(), code: "WORKFLOW_DEFINITION_SCHEMA_UNKNOWN"},
		{name: "permission", definition: func() domain.RegisteredDefinition {
			d := definitionFixture("unknown-permission", oneNode("deterministic.test"))
			d.Graph.Nodes[0].RequiredPermissions = []domain.Permission{"UNREGISTERED"}
			return d
		}(), code: "WORKFLOW_DEFINITION_PERMISSION_UNKNOWN"},
		{name: "executor", definition: definitionFixture("missing-executor", oneNode("deterministic.missing")), code: "WORKFLOW_DEFINITION_EXECUTOR_MISSING"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			registry, err := NewDefinitionRegistry(catalog, executors)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Register(tc.definition); err != nil {
				t.Fatal(err)
			}
			assertWorkflowErrorCode(t, registry.Freeze(), tc.code)
		})
	}
}

func TestDefinitionRegistryAcceptsDeclaredContractWithoutLocalExecutor(t *testing.T) {
	catalog := testValidationCatalog(t)
	executors, err := NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := executors.RegisterContract("agent.relation-assessment", 1); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	registry, err := NewDefinitionRegistry(catalog, executors)
	if err != nil {
		t.Fatal(err)
	}
	definition := definitionFixture("agent-relation-assessment", []domain.NodeDefinition{{
		Key: "relation-assessment", Kind: "agent.relation-assessment", InputSchemaVersion: 1, OutputSchemaVersion: 1,
	}})
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
}

func TestDefinitionRegistryAcceptsSplitMaintenancePermissionsAndRejectsLegacyGraph(t *testing.T) {
	catalog, err := NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors := testFrozenExecutors(t, catalog, "deterministic.test")

	registry, err := NewDefinitionRegistry(catalog, executors)
	if err != nil {
		t.Fatal(err)
	}
	definition := definitionFixture("maintenance", []domain.NodeDefinition{
		{Key: "evaluate", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RequiredPermissions: []domain.Permission{domain.PermissionEvaluationRun}},
		{Key: "reindex", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RequiredPermissions: []domain.Permission{domain.PermissionIndexMaintenance}},
	})
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}

	legacy, err := NewDefinitionRegistry(catalog, executors)
	if err != nil {
		t.Fatal(err)
	}
	definition = definitionFixture("legacy-maintenance", oneNode("deterministic.test"))
	definition.Graph.Nodes[0].RequiredPermissions = []domain.Permission{"ADMIN_MAINTENANCE"}
	if err := legacy.Register(definition); err != nil {
		t.Fatal(err)
	}
	assertWorkflowErrorCode(t, legacy.Freeze(), "WORKFLOW_DEFINITION_PERMISSION_UNKNOWN")
}

func TestDefinitionRegistryCanonicalizesAllowedToolsAndReturnsDeepCopies(t *testing.T) {
	catalog := testValidationCatalog(t)
	executors := testFrozenExecutors(t, catalog, "deterministic.test")
	search := toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 2}
	read := toolsdomain.ToolRef{Name: "ReadSource", Version: 1}
	tools := fakeToolContractCatalog{contracts: map[toolsdomain.ToolRef]toolsdomain.Definition{
		search: {Ref: search, RequiredCapability: capability.ReadLocal, InvocationPolicy: toolsdomain.InvocationModelRequestable, AllowedWorkflows: []toolsdomain.WorkflowBinding{{Key: "agent-rag", Version: 1}}},
		read:   {Ref: read, RequiredCapability: capability.ReadLocal, InvocationPolicy: toolsdomain.InvocationModelRequestable, AllowedWorkflows: []toolsdomain.WorkflowBinding{{Key: "agent-rag", Version: 1}}},
	}}
	registry, err := NewDefinitionRegistry(catalog, executors, tools)
	if err != nil {
		t.Fatal(err)
	}
	definition := definitionFixture("agent-rag", []domain.NodeDefinition{{
		Key: "tool-node", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1,
		RetryPolicy: testRetryPolicy(), RequiredPermissions: []domain.Permission{domain.PermissionReadLocal},
		AllowedTools: []toolsdomain.ToolRef{search, read, search},
	}})
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	definition.Graph.Nodes[0].AllowedTools[0].Name = "MutatedCallerValue"
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.Resolve("agent-rag", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []toolsdomain.ToolRef{read, search}
	if len(resolved.Graph.Nodes[0].AllowedTools) != len(want) || resolved.Graph.Nodes[0].AllowedTools[0] != want[0] || resolved.Graph.Nodes[0].AllowedTools[1] != want[1] {
		t.Fatalf("allowed tools=%+v, want %+v", resolved.Graph.Nodes[0].AllowedTools, want)
	}
	resolved.Graph.Nodes[0].AllowedTools[0].Name = "MutatedResolvedValue"
	again, err := registry.Resolve("agent-rag", 1)
	if err != nil || again.Graph.Nodes[0].AllowedTools[0] != read {
		t.Fatalf("registry allowed tools mutated: %+v, err=%v", again.Graph.Nodes[0].AllowedTools, err)
	}
}

func TestDefinitionRegistryRejectsAmbiguousToolVersionsAtRegistration(t *testing.T) {
	catalog := testValidationCatalog(t)
	executors := testFrozenExecutors(t, catalog, "deterministic.test")
	registry, err := NewDefinitionRegistry(catalog, executors)
	if err != nil {
		t.Fatal(err)
	}
	definition := definitionFixture("agent-rag", []domain.NodeDefinition{{
		Key: "tool-node", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1,
		AllowedTools: []toolsdomain.ToolRef{{Name: "SearchKnowledge", Version: 1}, {Name: "SearchKnowledge", Version: 2}},
	}})
	assertWorkflowErrorCode(t, registry.Register(definition), "WORKFLOW_DEFINITION_TOOL_VERSION_AMBIGUOUS")
}

func TestDefinitionRegistryFreezeRejectsInvalidToolPolicy(t *testing.T) {
	ref := toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 1}
	base := toolsdomain.Definition{
		Ref: ref, RequiredCapability: capability.ReadLocal, InvocationPolicy: toolsdomain.InvocationModelRequestable,
		AllowedWorkflows: []toolsdomain.WorkflowBinding{{Key: "agent-rag", Version: 1}},
	}
	tests := []struct {
		name        string
		toolCatalog []ToolContractCatalog
		permissions []domain.Permission
		code        string
	}{
		{name: "catalog missing", code: "WORKFLOW_DEFINITION_TOOL_CATALOG_MISSING", permissions: []domain.Permission{domain.PermissionReadLocal}},
		{name: "unknown tool", toolCatalog: []ToolContractCatalog{fakeToolContractCatalog{err: errors.New("not found")}}, code: "WORKFLOW_DEFINITION_TOOL_UNKNOWN", permissions: []domain.Permission{domain.PermissionReadLocal}},
		{name: "capability mismatch", toolCatalog: []ToolContractCatalog{fakeToolContractCatalog{contracts: map[toolsdomain.ToolRef]toolsdomain.Definition{ref: base}}}, code: "WORKFLOW_DEFINITION_TOOL_PERMISSION_MISMATCH", permissions: []domain.Permission{domain.PermissionReadExternal}},
		{name: "workflow mismatch", toolCatalog: []ToolContractCatalog{fakeToolContractCatalog{contracts: map[toolsdomain.ToolRef]toolsdomain.Definition{ref: func() toolsdomain.Definition {
			value := base
			value.AllowedWorkflows = []toolsdomain.WorkflowBinding{{Key: "other", Version: 1}}
			return value
		}()}}}, code: "WORKFLOW_DEFINITION_TOOL_WORKFLOW_MISMATCH", permissions: []domain.Permission{domain.PermissionReadLocal}},
		{name: "contract drift", toolCatalog: []ToolContractCatalog{fakeToolContractCatalog{contracts: map[toolsdomain.ToolRef]toolsdomain.Definition{ref: func() toolsdomain.Definition { value := base; value.Ref.Version = 2; return value }()}}}, code: "WORKFLOW_DEFINITION_TOOL_CONTRACT_INVALID", permissions: []domain.Permission{domain.PermissionReadLocal}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := testValidationCatalog(t)
			executors := testFrozenExecutors(t, catalog, "deterministic.test")
			registry, err := NewDefinitionRegistry(catalog, executors, test.toolCatalog...)
			if err != nil {
				t.Fatal(err)
			}
			definition := definitionFixture("agent-rag", oneNode("deterministic.test"))
			definition.Graph.Nodes[0].RequiredPermissions = test.permissions
			definition.Graph.Nodes[0].AllowedTools = []toolsdomain.ToolRef{ref}
			if err := registry.Register(definition); err != nil {
				t.Fatal(err)
			}
			assertWorkflowErrorCode(t, registry.Freeze(), test.code)
		})
	}
}

func TestDefinitionRegistryAllowsTrustedToolOnlyForServerOwnedNodePolicy(t *testing.T) {
	catalog, err := NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors := testFrozenExecutors(t, catalog, "deterministic.test")
	ref := toolsdomain.ToolRef{Name: "ApplyApprovedPatch", Version: 1}
	tools := fakeToolContractCatalog{contracts: map[toolsdomain.ToolRef]toolsdomain.Definition{ref: {
		Ref: ref, RequiredCapability: capability.WriteKnowledge, InvocationPolicy: toolsdomain.InvocationTrustedWorkflowOnly,
		AllowedWorkflows: []toolsdomain.WorkflowBinding{{Key: "trusted-writeback", Version: 1}},
	}}}
	registry, err := NewDefinitionRegistry(catalog, executors, tools)
	if err != nil {
		t.Fatal(err)
	}
	definition := definitionFixture("trusted-writeback", oneNode("deterministic.test"))
	definition.Graph.Nodes[0].RequiredPermissions = []domain.Permission{domain.PermissionWriteKnowledge}
	definition.Graph.Nodes[0].AllowedTools = []toolsdomain.ToolRef{ref}
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
}

func TestDefinitionRegistryLegacyGraphOmitsAllowedToolsAndKeepsHash(t *testing.T) {
	definition := definitionFixture("legacy", oneNode("deterministic.test"))
	canonical, err := canonicalizeDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(mustJSON(t, canonical.Graph))
	if strings.Contains(encoded, "allowed_tools") {
		t.Fatalf("legacy graph contains allowed_tools: %s", encoded)
	}
	const legacyHash = "ec49f74d39814089a375bc57e0822ccd9b4a595937238b94c0797cb42a857e87"
	if canonical.GraphHash != legacyHash {
		t.Fatalf("legacy graph hash=%q, want %q", canonical.GraphHash, legacyHash)
	}
}

type fakeToolContractCatalog struct {
	contracts map[toolsdomain.ToolRef]toolsdomain.Definition
	err       error
}

func (catalog fakeToolContractCatalog) ResolveToolDefinition(ref toolsdomain.ToolRef) (toolsdomain.Definition, error) {
	if catalog.err != nil {
		return toolsdomain.Definition{}, catalog.err
	}
	contract, ok := catalog.contracts[ref]
	if !ok {
		return toolsdomain.Definition{}, errors.New("tool contract not found")
	}
	contract.AllowedWorkflows = append([]toolsdomain.WorkflowBinding(nil), contract.AllowedWorkflows...)
	return contract, nil
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func testValidationCatalog(t *testing.T) ValidationCatalog {
	t.Helper()
	catalog, err := NewValidationCatalog([]int{1}, []domain.Permission{domain.PermissionReadLocal, domain.PermissionReadExternal})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func testFrozenExecutors(t *testing.T, catalog ValidationCatalog, kinds ...string) *ExecutorRegistry {
	t.Helper()
	registry, err := NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range kinds {
		if err := registry.Register(kind, 1, registryTestExecutor{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	return registry
}

func definitionFixture(key string, nodes []domain.NodeDefinition) domain.RegisteredDefinition {
	return domain.RegisteredDefinition{Key: key, Version: 1, InputSchemaVersion: 1, Graph: domain.CanonicalGraph{Nodes: nodes}}
}

func oneNode(kind string) []domain.NodeDefinition {
	return []domain.NodeDefinition{{Key: "one", Kind: kind, InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()}}
}

func testRetryPolicy() domain.RetryPolicy {
	return domain.RetryPolicy{MaxRetries: 2, BaseDelay: time.Second, MaxDelay: time.Minute}
}
