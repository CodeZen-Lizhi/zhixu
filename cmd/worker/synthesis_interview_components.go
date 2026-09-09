package main

import (
	"errors"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	interviewagent "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/adapter/agent"
	interviewpostgres "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/adapter/postgres"
	interviewapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewworkflow "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/workflow"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// workerSynthesisInterviewPersistence is shared by all model generations. It
// owns neither a model nor a Workflow runtime, and can therefore be installed as
// a terminal hook before the one authoritative RuntimeRepository is constructed.
type workerSynthesisInterviewPersistence struct {
	repository *interviewpostgres.GORMNotePreparationRepository
	modelRuns  *agentpostgres.GORMRepository
}

func newWorkerSynthesisInterviewPersistence(pool *platformpostgres.Pool, modelRuns *agentpostgres.GORMRepository) (*workerSynthesisInterviewPersistence, error) {
	if pool == nil || modelRuns == nil {
		return nil, synthesisInterviewCompositionUnavailable("note interview persistence dependencies are unavailable")
	}
	fence, err := workflowpostgres.NewGORMToolExecutionPolicySnapshot(pool)
	if err != nil {
		return nil, err
	}
	bindings, err := workflowpostgres.NewGORMRuntimeBindingReader(pool)
	if err != nil {
		return nil, err
	}
	repository, err := interviewpostgres.NewGORMNotePreparationRepository(pool, interviewpostgres.NotePreparationDependencies{
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
		ModelRuns: modelRuns, Fence: fence, Bindings: bindings,
	})
	if err != nil {
		return nil, err
	}
	return &workerSynthesisInterviewPersistence{repository: repository, modelRuns: modelRuns}, nil
}

func (persistence *workerSynthesisInterviewPersistence) terminalHook() (workflowapplication.ScopedWorkflowTerminalHook, error) {
	if !persistence.available() {
		return nil, synthesisInterviewCompositionUnavailable("note interview terminal persistence is unavailable")
	}
	return persistence.repository, nil
}

func (persistence *workerSynthesisInterviewPersistence) bindRuntime(runtime *workflowpostgres.GORMRuntimeRepository) error {
	if !persistence.available() || runtime == nil {
		return synthesisInterviewCompositionUnavailable("note interview workflow runtime is unavailable")
	}
	return persistence.repository.BindWorkflowStarter(runtime)
}

func (persistence *workerSynthesisInterviewPersistence) available() bool {
	return persistence != nil && persistence.repository != nil && persistence.modelRuns != nil
}

type workerSynthesisInterviewComponents struct {
	executor   *interviewworkflow.NotePreparationExecutor
	model      *interviewagent.NoteInterviewModel
	catalog    *agentapplication.RuntimeCatalog
	capability agentCapabilityStatus
}

// newWorkerSynthesisInterviewComponents is called both at initial composition
// and inside each managed model generation build. Only persistence is shared;
// each executor captures that generation's exact ChatModel/profile/scheduler.
func newWorkerSynthesisInterviewComponents(
	persistence *workerSynthesisInterviewPersistence,
	runs *workflowpostgres.GORMRepository,
	agent agentWorkflowComponents,
) (workerSynthesisInterviewComponents, error) {
	if !persistence.available() || runs == nil {
		return workerSynthesisInterviewComponents{}, synthesisInterviewCompositionUnavailable("note interview executor dependencies are unavailable")
	}
	components := workerSynthesisInterviewComponents{
		model:      interviewagent.NewUnavailableNoteInterviewModel(),
		capability: agentCapabilityStatus{code: interviewapplication.ErrorCodeNotePlanUnavailable},
	}
	if !workerRuntimeNilDependency(agent.model) {
		if workerRuntimeNilDependency(agent.organizingScheduler) {
			return workerSynthesisInterviewComponents{}, synthesisInterviewCompositionUnavailable("note interview structured scheduler is unavailable")
		}
		catalog, err := newSynthesisInterviewRuntimeCatalog(agent.contract)
		if err != nil {
			return workerSynthesisInterviewComponents{}, err
		}
		model, err := interviewagent.NewNoteInterviewModel(interviewagent.NoteInterviewModelDependencies{
			Model: agent.model, Scheduler: agent.organizingScheduler, Catalog: catalog,
			ModelRuns: persistence.modelRuns, Store: persistence.repository,
			ProfileRef: agentworkflow.DefaultProfileRef(), IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
			Budget: agentApplicationBudget(agent.contract),
		})
		if err != nil {
			return workerSynthesisInterviewComponents{}, err
		}
		components.model, components.catalog = model, catalog
		components.capability = agentCapabilityStatus{available: true}
	} else if agent.capability.available {
		return workerSynthesisInterviewComponents{}, synthesisInterviewCompositionUnavailable("configured note interview chat model is unavailable")
	}
	executor, err := interviewworkflow.NewNotePreparationExecutor(interviewworkflow.NotePreparationExecutorDependencies{
		Runs: runs, Store: persistence.repository, Model: components.model, Clock: foundation.SystemClock{},
	})
	if err != nil {
		return workerSynthesisInterviewComponents{}, err
	}
	components.executor = executor
	return components, nil
}

// A missing model still uses the real executor so the durable preparation can
// become CAPABILITY_UNAVAILABLE and an explicit retry can later recover it.
func registerWorkerSynthesisInterviewExecutor(registry *workflowapplication.ExecutorRegistry, components workerSynthesisInterviewComponents) error {
	if registry == nil || components.executor == nil || components.model == nil ||
		components.capability.available && (components.catalog == nil || components.capability.code != "") ||
		!components.capability.available && components.capability.code != interviewapplication.ErrorCodeNotePlanUnavailable {
		return synthesisInterviewCompositionUnavailable("note interview executor registration is incomplete")
	}
	return registry.Register(interviewapplication.NotePreparationNodeKind, interviewapplication.NotePreparationSchemaVersion, components.executor)
}

func registerWorkerSynthesisInterviewDefinition(registry *workflowapplication.DefinitionRegistry) error {
	if registry == nil {
		return synthesisInterviewCompositionUnavailable("note interview definition registry is unavailable")
	}
	return interviewworkflow.RegisterNotePreparationDefinition(registry)
}

func newSynthesisInterviewRuntimeCatalog(contract platformmodels.ChatContract) (*agentapplication.RuntimeCatalog, error) {
	if contract.Model.Validate() != nil || contract.Timeout <= 0 || contract.MaxRequestBytes <= 0 || contract.MaxResponseBytes <= 0 {
		return nil, synthesisInterviewCompositionUnavailable("note interview chat contract is invalid")
	}
	catalog := agentapplication.NewRuntimeCatalog()
	if err := interviewagent.RegisterNoteInterviewRuntimeCatalog(catalog); err != nil {
		return nil, err
	}
	if err := catalog.RegisterProfile(agentapplication.ModelProfile{
		Ref: agentworkflow.DefaultProfileRef(), Model: contract.Model,
		Timeout: contract.Timeout, MaxOutputTokens: agentStructuredMaxOutputTokens,
	}); err != nil {
		return nil, err
	}
	if err := catalog.Freeze(); err != nil {
		return nil, err
	}
	return catalog, nil
}

func synthesisInterviewCompositionUnavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "SYNTHESIS_INTERVIEW_COMPOSITION_UNAVAILABLE", false, errors.New(message))
}
