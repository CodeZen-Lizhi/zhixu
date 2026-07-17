package application

import (
	"testing"
	"time"

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
