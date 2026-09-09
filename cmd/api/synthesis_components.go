package main

import (
	"errors"
	"reflect"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	authoringchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringpostgres "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/postgres"
	authoringapplication "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	organizingpostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/postgres"
	synthesispostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/synthesispostgres"
	organizingapplication "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizinghttp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/http"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalworkspace "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/workspace"
	interviewpostgres "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/adapter/postgres"
	interviewapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/http"
	interviewworkflow "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/workflow"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// apiSynthesisRuntimeComponents contains only same-pool persistence ports. It is
// built before the Workflow runtime so terminal hooks never depend on a partially
// initialized runtime or on the availability of a model provider.
type apiSynthesisRuntimeComponents struct {
	pool             *platformpostgres.Pool
	modelRuns        *agentpostgres.GORMRepository
	workflowFence    *workflowpostgres.GORMWorkspaceAnalysisExecutionFence
	workflowBindings *workflowpostgres.GORMRuntimeBindingReader
	store            *synthesispostgres.Store
	interviews       *interviewpostgres.GORMNotePreparationRepository
}

func newAPISynthesisRuntimeComponents(pool *platformpostgres.Pool) (*apiSynthesisRuntimeComponents, error) {
	if pool == nil {
		return nil, synthesisAPICompositionUnavailable("synthesis database is unavailable")
	}
	modelRuns, err := agentpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(pool)
	if err != nil {
		return nil, err
	}
	bindings, err := workflowpostgres.NewGORMRuntimeBindingReader(pool)
	if err != nil {
		return nil, err
	}
	store, err := synthesispostgres.NewStore(pool, synthesispostgres.Dependencies{
		ModelRuns: modelRuns, WorkflowFence: fence, WorkflowBindings: bindings,
	})
	if err != nil {
		return nil, err
	}
	interviewFence, err := workflowpostgres.NewGORMToolExecutionPolicySnapshot(pool)
	if err != nil {
		return nil, err
	}
	interviews, err := interviewpostgres.NewGORMNotePreparationRepository(pool, interviewpostgres.NotePreparationDependencies{
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
		ModelRuns: modelRuns, Fence: interviewFence, Bindings: bindings,
	})
	if err != nil {
		return nil, err
	}
	return &apiSynthesisRuntimeComponents{
		pool: pool, modelRuns: modelRuns, workflowFence: fence, workflowBindings: bindings, store: store, interviews: interviews,
	}, nil
}

func (components *apiSynthesisRuntimeComponents) terminalHooks() ([]workflowapplication.ScopedWorkflowTerminalHook, error) {
	if !components.available() {
		return nil, synthesisAPICompositionUnavailable("synthesis terminal persistence is unavailable")
	}
	return []workflowapplication.ScopedWorkflowTerminalHook{components.store, components.interviews}, nil
}

func (components *apiSynthesisRuntimeComponents) available() bool {
	return components != nil && components.pool != nil && components.modelRuns != nil &&
		components.workflowFence != nil && components.workflowBindings != nil && components.store != nil && components.interviews != nil
}

type apiSynthesisDependencies struct {
	Workspaces   workspacedomain.SourceMaterialRepository
	Files        workspacedomain.FileScanner
	Publications *authoringapplication.Service
	Retirements  changecontroldomain.GeneratedPublicationRetirementRepository
	Runtime      *workflowpostgres.GORMRuntimeRepository
	Timeout      time.Duration
}

type apiSynthesisComponents struct {
	store            *organizingpostgres.GORMSynthesisStore
	sources          *organizingowner.GORMSynthesisSourceReader
	service          *organizingapplication.SynthesisService
	processing       *organizingworkflow.SynthesisProcessingService
	definitions      *workflowapplication.DefinitionRegistry
	handler          *organizinghttp.SynthesisHandler
	interviews       *interviewapplication.NotePreparationService
	interviewHandler *interviewhttp.NotePreparationHandler
}

// newAPISynthesisComponents completes the composition only after the existing
// Workflow runtime and Authoring service are available. In particular, publication
// receipt recovery remains usable while chat generation is disabled or unhealthy.
func newAPISynthesisComponents(runtime *apiSynthesisRuntimeComponents, dependencies apiSynthesisDependencies) (*apiSynthesisComponents, error) {
	if !runtime.available() || dependencies.Runtime == nil || dependencies.Publications == nil ||
		apiCompositionNilDependency(dependencies.Workspaces) || apiCompositionNilDependency(dependencies.Files) ||
		apiCompositionNilDependency(dependencies.Retirements) {
		return nil, synthesisAPICompositionUnavailable("synthesis API dependencies are incomplete")
	}
	pool := runtime.pool
	artifacts, err := retrievalworkspace.NewReader(dependencies.Workspaces, dependencies.Files)
	if err != nil {
		return nil, err
	}
	sources, err := organizingowner.NewGORMSynthesisSourceReader(pool, artifacts)
	if err != nil {
		return nil, err
	}
	authoring, err := authoringpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	retirer, err := authoringchangecontrol.NewGeneratedPublicationRetirer(dependencies.Retirements)
	if err != nil {
		return nil, err
	}
	store, err := organizingpostgres.NewGORMSynthesisStore(pool, organizingpostgres.SynthesisStoreDependencies{
		Authoring: authoring, Retirer: retirer, Sources: sources, Validated: runtime.store,
	})
	if err != nil {
		return nil, err
	}
	ids, clock := foundation.NewUUIDGenerator(nil), foundation.SystemClock{}
	service, err := organizingapplication.NewSynthesisService(organizingapplication.SynthesisDependencies{
		Store: store, Sources: sources, Publications: dependencies.Publications, IDs: ids, Clock: clock,
	})
	if err != nil {
		return nil, err
	}
	definitions, err := newAPISynthesisDefinitions()
	if err != nil {
		return nil, err
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	processing, err := organizingworkflow.NewSynthesisProcessingService(organizingworkflow.SynthesisProcessingServiceDependencies{
		UnitOfWork: unitOfWork, Queries: runtime.store, Retries: runtime.store, Applied: store,
		Starter: dependencies.Runtime, Definitions: definitions, IDs: ids, Clock: clock,
	})
	if err != nil {
		return nil, err
	}
	handler := organizinghttp.NewSynthesisHandler(service, processing, dependencies.Timeout)
	if !handler.Available() {
		return nil, synthesisAPICompositionUnavailable("synthesis HTTP handler is unavailable")
	}
	interviews, err := interviewapplication.NewNotePreparationService(interviewapplication.NotePreparationDependencies{
		Store: runtime.interviews, Snapshots: service, IDs: ids, Clock: clock,
	})
	if err != nil {
		return nil, err
	}
	interviewHandler := interviewhttp.NewNotePreparationHandler(interviews)
	if !interviewHandler.Available() {
		return nil, synthesisAPICompositionUnavailable("synthesis interview HTTP handler is unavailable")
	}
	// Bind only after all other construction succeeded, and before exposing either
	// handler. The same Runtime owns the scoped start and the terminal callback.
	if err := runtime.interviews.BindWorkflowStarter(dependencies.Runtime); err != nil {
		return nil, err
	}
	return &apiSynthesisComponents{
		store: store, sources: sources, service: service, processing: processing, definitions: definitions, handler: handler,
		interviews: interviews, interviewHandler: interviewHandler,
	}, nil
}

// Contract registration is shared by the HTTP command registry and the main API
// Workflow registry. Register before Freeze; APIs never install model executors.
func registerAPISynthesisWorkflowContracts(executors *workflowapplication.ExecutorRegistry) error {
	if executors == nil {
		return synthesisAPICompositionUnavailable("synthesis executor contracts are unavailable")
	}
	for _, kind := range organizingworkflow.SynthesisExecutorNodeKinds() {
		if err := executors.RegisterContract(kind, organizingworkflow.SynthesisInputSchemaVersion); err != nil {
			return err
		}
	}
	return executors.RegisterContract(interviewapplication.NotePreparationNodeKind, interviewapplication.NotePreparationSchemaVersion)
}

func registerAPISynthesisWorkflowDefinitions(definitions *workflowapplication.DefinitionRegistry) error {
	if definitions == nil {
		return synthesisAPICompositionUnavailable("synthesis workflow definitions are unavailable")
	}
	for _, definition := range organizingworkflow.SynthesisRegisteredDefinitions() {
		if err := definitions.Register(definition); err != nil {
			return err
		}
	}
	return interviewworkflow.RegisterNotePreparationDefinition(definitions)
}

// The bounded command registry holds the same immutable definitions as the main
// registry; it creates no scheduler, runtime, transaction or second queue.
func newAPISynthesisDefinitions() (*workflowapplication.DefinitionRegistry, error) {
	catalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		return nil, err
	}
	executors, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		return nil, err
	}
	if err := registerAPISynthesisWorkflowContracts(executors); err != nil {
		return nil, err
	}
	if err := executors.Freeze(); err != nil {
		return nil, err
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors)
	if err != nil {
		return nil, err
	}
	if err := registerAPISynthesisWorkflowDefinitions(definitions); err != nil {
		return nil, err
	}
	if err := definitions.Freeze(); err != nil {
		return nil, err
	}
	return definitions, nil
}

// Every API runtime construction path, including the non-chat fallback, installs
// the same synthesis terminal owners while retaining existing control/safety hooks.
func (factory *apiRuntimeRepositoryFactory) withSynthesisRuntimeHooks(hooks workflowpostgres.GORMRuntimeRepositoryHooks) (workflowpostgres.GORMRuntimeRepositoryHooks, error) {
	if factory == nil {
		return workflowpostgres.GORMRuntimeRepositoryHooks{}, synthesisAPICompositionUnavailable("synthesis runtime factory is unavailable")
	}
	terminals, err := factory.synthesis.terminalHooks()
	if err != nil {
		return workflowpostgres.GORMRuntimeRepositoryHooks{}, err
	}
	if hooks.Terminal != nil {
		terminals = append([]workflowapplication.ScopedWorkflowTerminalHook{hooks.Terminal}, terminals...)
	}
	terminal, err := workflowapplication.NewCompositeScopedWorkflowTerminalHook(terminals...)
	if err != nil {
		return workflowpostgres.GORMRuntimeRepositoryHooks{}, err
	}
	hooks.Terminal = terminal
	return hooks, nil
}

func apiCompositionNilDependency(value any) bool {
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

func synthesisAPICompositionUnavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "SYNTHESIS_COMPOSITION_UNAVAILABLE", false, errors.New(message))
}
