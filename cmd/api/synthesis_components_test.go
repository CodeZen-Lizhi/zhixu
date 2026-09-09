package main

import (
	"context"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	interviewapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
)

// These are constructor tests: the lazy shared pool cannot contact a database.
// Transactional model/publication behavior is covered by the owning adapters.
func TestAPISynthesisCompositionKeepsHistoryAvailableWithoutChat(t *testing.T) {
	persistence, dependencies := apiSynthesisCompositionFixture(t)
	components, err := newAPISynthesisComponents(persistence, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if components.service == nil || components.processing == nil || components.interviews == nil ||
		!components.handler.Available() || !components.interviewHandler.Available() {
		t.Fatal("model-independent synthesis services were not composed")
	}
	if _, err := components.definitions.Resolve(interviewapplication.NotePreparationDefinitionKey, interviewapplication.NotePreparationDefinitionVersion); err != nil {
		t.Fatalf("note interview contract was not frozen: %v", err)
	}
	if err := persistence.interviews.BindWorkflowStarter(dependencies.Runtime); err == nil {
		t.Fatal("the preparation store allowed a second runtime binding")
	}
}

func TestAPISynthesisCompositionRejectsMissingAndTypedNilDependencies(t *testing.T) {
	if components, err := newAPISynthesisRuntimeComponents(nil); err == nil || components != nil {
		t.Fatal("missing persistence pool was accepted")
	}
	var missing *apiSynthesisRuntimeComponents
	if hooks, err := missing.terminalHooks(); err == nil || hooks != nil {
		t.Fatal("missing terminal persistence was accepted")
	}
	persistence, dependencies := apiSynthesisCompositionFixture(t)
	for _, test := range []struct {
		name   string
		remove func(*apiSynthesisDependencies)
	}{
		{"runtime", func(value *apiSynthesisDependencies) { value.Runtime = nil }},
		{"authoring", func(value *apiSynthesisDependencies) { value.Publications = nil }},
		{"typed nil workspaces", func(value *apiSynthesisDependencies) { value.Workspaces = (*workspacepostgres.GORMRepository)(nil) }},
		{"typed nil scanner", func(value *apiSynthesisDependencies) { value.Files = (*fakeFileScanner)(nil) }},
		{"typed nil retirement", func(value *apiSynthesisDependencies) {
			value.Retirements = (*changecontrolpostgres.GORMRepository)(nil)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			incomplete := dependencies
			test.remove(&incomplete)
			if components, err := newAPISynthesisComponents(persistence, incomplete); err == nil || components != nil {
				t.Fatal("incomplete synthesis composition was accepted")
			}
		})
	}
}

func TestAPISynthesisRuntimeFactoryIncludesBothTerminalOwnersInFallback(t *testing.T) {
	factory, err := newAPIRuntimeRepositoryFactory(apiConstructorPool(t), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := factory.withSynthesisRuntimeHooks(workflowpostgres.GORMRuntimeRepositoryHooks{})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{organizingworkflow.SynthesisGenerateNodeKind, interviewapplication.NotePreparationNodeKind} {
		// An owned event without a live transaction must fail. A missing hook
		// would incorrectly accept it without touching the durable owner.
		if err := hooks.Terminal.OnWorkflowNodeTerminalScoped(t.Context(), nil, workflowapplication.WorkflowNodeTerminalEvent{NodeKind: kind}); err == nil {
			t.Fatalf("runtime omitted terminal owner for %s", kind)
		}
	}
	service, runtime, err := newWorkflowComponentsWithFactory(factory, workflowpostgres.GORMRuntimeRepositoryHooks{})
	if err != nil || service == nil || runtime == nil {
		t.Fatalf("non-chat Workflow fallback was not composed: %v", err)
	}
	terminalCalled, controlCalled := false, false
	hooks, err = factory.withSynthesisRuntimeHooks(workflowpostgres.GORMRuntimeRepositoryHooks{
		Terminal: apiTerminalHookFunc(func(context.Context, foundation.TransactionScope, workflowapplication.WorkflowNodeTerminalEvent) error {
			terminalCalled = true
			return nil
		}),
		Control: apiControlHookFunc(func(context.Context, foundation.TransactionScope, workflowapplication.WorkflowControlEvent) error {
			controlCalled = true
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := hooks.Terminal.OnWorkflowNodeTerminalScoped(t.Context(), nil, workflowapplication.WorkflowNodeTerminalEvent{}); err != nil {
		t.Fatal(err)
	}
	if err := hooks.Control.OnWorkflowControlScoped(t.Context(), nil, workflowapplication.WorkflowControlEvent{}); err != nil {
		t.Fatal(err)
	}
	if !terminalCalled || !controlCalled {
		t.Fatal("synthesis composition replaced an existing Workflow hook")
	}
	var missing apiTerminalHookFunc
	if _, err := factory.withSynthesisRuntimeHooks(workflowpostgres.GORMRuntimeRepositoryHooks{Terminal: missing}); err == nil {
		t.Fatal("runtime accepted a typed-nil terminal hook")
	}
}

func TestAPISynthesisWorkflowRegistrationDoesNotDependOnChat(t *testing.T) {
	for _, chatEnabled := range []bool{false, true} {
		catalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
		if err != nil {
			t.Fatal(err)
		}
		executors, err := workflowapplication.NewExecutorRegistry(catalog)
		if err != nil {
			t.Fatal(err)
		}
		if err := registerAPIWorkflowExecutors(chatEnabled, executors); err != nil {
			t.Fatal(err)
		}
		if err := executors.Freeze(); err != nil {
			t.Fatal(err)
		}
		toolContracts, err := newToolContractRegistry()
		if err != nil {
			t.Fatal(err)
		}
		definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors, toolContracts)
		if err != nil {
			t.Fatal(err)
		}
		if err := registerAPIWorkflowDefinitions(chatEnabled, definitions); err != nil {
			t.Fatal(err)
		}
		if err := definitions.Freeze(); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{organizingworkflow.SynthesisDefinitionKey, interviewapplication.NotePreparationDefinitionKey} {
			definition, err := definitions.Resolve(key, 1)
			if err != nil {
				t.Fatalf("chat=%t definition=%s: %v", chatEnabled, key, err)
			}
			for _, node := range definition.Graph.Nodes {
				if !executors.SupportsContract(node.Kind, node.InputSchemaVersion) {
					t.Fatalf("missing API contract for %s", node.Kind)
				}
				if _, err := executors.Resolve(node.Kind, node.InputSchemaVersion); err == nil {
					t.Fatalf("API installed a model executor for %s", node.Kind)
				}
			}
		}
	}
}

func TestAPISynthesisPublicationDependencyRejectsTypedNilService(t *testing.T) {
	service, err := newAuthoringService(apiConstructorPool(t), (*changecontrolapplication.Service)(nil), &authoringTargetReaderFake{})
	if err == nil || service != nil {
		t.Fatal("typed-nil proposal service created an apparently available publisher")
	}
}

func apiSynthesisCompositionFixture(t *testing.T) (*apiSynthesisRuntimeComponents, apiSynthesisDependencies) {
	t.Helper()
	pool := apiConstructorPool(t)
	factory, err := newAPIRuntimeRepositoryFactory(pool, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	persistence := factory.synthesis
	hooks, err := persistence.terminalHooks()
	if err != nil || len(hooks) != 2 || hooks[0] == nil || hooks[1] == nil {
		t.Fatalf("terminal hooks must be available before runtime construction: %v", err)
	}
	runtime, err := factory.newRepository(workflowpostgres.GORMRuntimeRepositoryHooks{})
	if err != nil {
		t.Fatal(err)
	}
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	publications, err := newAuthoringService(pool, authoringProposalServiceFake{}, &authoringTargetReaderFake{})
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewGORMStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	retirements, err := changecontrolpostgres.NewGORMRepository(pool, events)
	if err != nil {
		t.Fatal(err)
	}
	return persistence, apiSynthesisDependencies{
		Workspaces: workspaces, Files: fakeFileScanner{}, Publications: publications,
		Retirements: retirements, Runtime: runtime, Timeout: time.Second,
	}
}
