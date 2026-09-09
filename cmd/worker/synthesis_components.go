package main

import (
	"context"
	"errors"
	"log/slog"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	authoringchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringpostgres "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/postgres"
	authoringapplication "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingagent "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/agent"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	organizingpostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/postgres"
	synthesispostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/synthesispostgres"
	organizingapplication "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalworkspace "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/workspace"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspaceruntimegrant "github.com/CodeZen-Lizhi/zhixu/internal/workspace/runtimegrant"
)

// These owners are shared by all model runtime generations. Only the model and
// executor are rebuilt when a managed model revision changes.
type synthesisWorkerOwners struct {
	sources *organizingowner.GORMSynthesisSourceReader
	store   *organizingpostgres.GORMSynthesisStore
	notes   *organizingapplication.SynthesisService
}

// Build before RuntimeRepository so its terminal hook shares that repository's
// transaction. The read/fence adapters do not depend on a RuntimeRepository.
func newSynthesisRuntimeStore(pool *postgres.Pool, modelRuns *agentpostgres.GORMRepository) (*synthesispostgres.Store, error) {
	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(pool)
	if err != nil {
		return nil, err
	}
	bindings, err := workflowpostgres.NewGORMRuntimeBindingReader(pool)
	if err != nil {
		return nil, err
	}
	return synthesispostgres.NewStore(pool, synthesispostgres.Dependencies{ModelRuns: modelRuns, WorkflowFence: fence, WorkflowBindings: bindings})
}

func newSynthesisWorkerOwners(
	pool *postgres.Pool,
	workspaces workspaceruntimegrant.GORMRepositoryPort,
	authoring *authoringpostgres.GORMRepository,
	retirement changecontroldomain.GeneratedPublicationRetirementRepository,
	proposals *changecontrolapplication.Service,
	targets authoringchangecontrol.TargetReader,
	runtimeStore *synthesispostgres.Store,
) (synthesisWorkerOwners, error) {
	if pool == nil || workspaces == nil || authoring == nil || runtimeStore == nil || proposals == nil {
		return synthesisWorkerOwners{}, foundation.NewError(foundation.ErrorDependencyUnavailable, organizingworkflow.ErrorCodeSynthesisExecutionUnavailable, false, errors.New("synthesis worker owners are unavailable"))
	}
	artifacts, err := retrievalworkspace.NewReader(workspaces, filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}})
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	sources, err := organizingowner.NewGORMSynthesisSourceReader(pool, artifacts)
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	retirer, err := authoringchangecontrol.NewGeneratedPublicationRetirer(retirement)
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	store, err := organizingpostgres.NewGORMSynthesisStore(pool, organizingpostgres.SynthesisStoreDependencies{
		Authoring: authoring, Retirer: retirer, Sources: sources, Validated: runtimeStore,
	})
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	creator, err := authoringchangecontrol.NewProposalCreator(proposals, targets)
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	publications, err := authoringapplication.NewService(authoringapplication.Dependencies{
		Repository: authoring, Proposals: creator, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	notes, err := organizingapplication.NewSynthesisService(organizingapplication.SynthesisDependencies{
		Store: store, Sources: sources, Publications: publications, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	return synthesisWorkerOwners{sources: sources, store: store, notes: notes}, nil
}

// Called at startup and again for every managed runtime rebuild. A missing
// model still has a registered executor so imported sources get visible failure
// facts and can be explicitly retried after configuration is repaired.
func newSynthesisWorkerExecutor(
	owners synthesisWorkerOwners,
	store *synthesispostgres.Store,
	runs organizingworkflow.WorkflowRunReader,
	modelRuns *agentpostgres.GORMRepository,
	model agentapplication.ChatModel,
	contract platformmodels.ChatContract,
	scheduler agentapplication.StructuredPhaseScheduler,
) (*organizingworkflow.SynthesisExecutor, error) {
	var synthesisModel organizingworkflow.SynthesisExecutionModel = organizingagent.NewUnavailableSynthesisModel()
	if model != nil {
		catalog, err := newSynthesisRuntimeCatalog(contract)
		if err != nil {
			return nil, err
		}
		synthesisModel, err = organizingagent.NewSynthesisModel(organizingagent.SynthesisModelDependencies{
			Model: model, Scheduler: scheduler, Catalog: catalog, ModelRuns: modelRuns, Store: store,
			ProfileRef: agentworkflow.DefaultProfileRef(), IDs: foundation.NewUUIDGenerator(nil),
			Clock: foundation.SystemClock{}, Budget: agentApplicationBudget(contract),
		})
		if err != nil {
			return nil, err
		}
	}
	return organizingworkflow.NewSynthesisExecutor(organizingworkflow.SynthesisExecutorDependencies{
		Runs: runs, Store: store, Candidates: owners.notes, Sources: owners.sources, Model: synthesisModel, Clock: foundation.SystemClock{},
	})
}

func newSynthesisRuntimeCatalog(contract platformmodels.ChatContract) (*agentapplication.RuntimeCatalog, error) {
	if contract.Model.Validate() != nil || contract.Timeout <= 0 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, organizingapplication.ErrorCodeSynthesisCapabilityUnavailable, false, errors.New("synthesis chat contract is invalid"))
	}
	catalog := agentapplication.NewRuntimeCatalog()
	if err := organizingagent.RegisterSynthesisRuntimeCatalog(catalog); err != nil {
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

func registerSynthesisWorkerExecutors(registry *workflowapplication.ExecutorRegistry, executor *organizingworkflow.SynthesisExecutor) error {
	for _, kind := range organizingworkflow.SynthesisExecutorNodeKinds() {
		if err := registry.Register(kind, organizingworkflow.SynthesisInputSchemaVersion, executor); err != nil {
			return err
		}
	}
	return nil
}

func registerSynthesisWorkerDefinitions(registry *workflowapplication.DefinitionRegistry) error {
	for _, definition := range organizingworkflow.SynthesisRegisteredDefinitions() {
		if err := registry.Register(definition); err != nil {
			return err
		}
	}
	return nil
}

// Construct after Definitions.Freeze; its StartScoped uses the same Pool as the
// source-ready outbox and records the River job in the consuming transaction.
func newSynthesisSourceDispatcher(pool *postgres.Pool, owners synthesisWorkerOwners, store *synthesispostgres.Store, starter workflowapplication.ScopedRuntimeStarter, definitions *workflowapplication.DefinitionRegistry) (*organizingworkflow.SynthesisDispatcher, error) {
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	outbox, err := workflowpostgres.NewGORMSourceReadyOutbox(pool)
	if err != nil {
		return nil, err
	}
	return organizingworkflow.NewSynthesisDispatcher(organizingworkflow.SynthesisDispatcherDependencies{
		UnitOfWork: uow, Outbox: outbox, Sources: owners.sources, Processing: store, Starter: starter,
		Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
}

func dispatchSynthesisSources(ctx context.Context, logger *slog.Logger, dispatcher *organizingworkflow.SynthesisDispatcher, phase string) (organizingworkflow.SynthesisDispatchBatchResult, error) {
	result, err := dispatcher.DispatchBatch(ctx, 20)
	if err != nil {
		code := organizingworkflow.ErrorCodeSynthesisExecutionUnavailable
		var classified *foundation.Error
		if errors.As(err, &classified) {
			code = classified.Code
		}
		logger.Warn("synthesis source dispatch failed", "phase", phase, "error_code", code)
		return result, err
	}
	if result.Claimed > 0 {
		logger.Info("synthesis sources dispatched", "phase", phase, "started", result.Started, "skipped", result.Skipped, "replayed", result.Replayed)
	}
	return result, nil
}
