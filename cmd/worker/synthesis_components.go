package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	authoringchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringpostgres "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/postgres"
	authoringapplication "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/gitmerge"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingagent "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/agent"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/manuscript"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	organizingpostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/postgres"
	synthesispostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/synthesispostgres"
	organizingapplication "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
	retrievalworkspace "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/workspace"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspaceruntimegrant "github.com/CodeZen-Lizhi/zhixu/internal/workspace/runtimegrant"
)

// 进程拥有此解析器，并在所有 Worker 停止后关闭它。
type synthesisWorkspaceRuntime struct {
	workspaceruntimegrant.GORMRepositoryPort
	resolver *rootgrant.RootGrantResolver
}

// These owners are shared by all model runtime generations. Only the model and
// executor are rebuilt when a managed model revision changes.
type synthesisWorkerOwners struct {
	manuscripts    *organizingpostgres.SynthesisManuscriptRuntime
	sourceReviews  *organizingpostgres.GORMSynthesisManuscriptSourceReviewStore
	sources        *organizingowner.GORMSynthesisSourceReader
	store          *organizingpostgres.GORMSynthesisStore
	notes          *organizingapplication.SynthesisService
	anchors        *organizingpostgres.GORMAnchorStore
	directory      *organizingowner.KnowledgeDirectoryReader
	goalCatalog    *organizingowner.SynthesisGoalCatalogReader
	goalSelections *organizingpostgres.GORMGoalSelectionStore
	goalPreparer   *organizingapplication.SynthesisGoalPreparer
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
	profiles, err := capturepostgres.NewGORMProfileRepository(pool, modelRuns)
	if err != nil {
		return nil, err
	}
	snapshots, err := organizingowner.NewSynthesisGoalCatalogSnapshotReader(profiles)
	if err != nil {
		return nil, err
	}
	goalResults, err := organizingpostgres.NewGORMGoalSelectionResultReader(pool, snapshots)
	if err != nil {
		return nil, err
	}
	goalProof, err := organizingapplication.NewSynthesisGoalBindingProof(goalResults)
	if err != nil {
		return nil, err
	}
	return synthesispostgres.NewStore(pool, synthesispostgres.Dependencies{Goals: goalProof, ModelRuns: modelRuns, WorkflowFence: fence, WorkflowBindings: bindings})
}

func newSynthesisWorkerOwners(
	pool *postgres.Pool,
	workspaces workspaceruntimegrant.GORMRepositoryPort,
	authoring *authoringpostgres.GORMRepository,
	retirement changecontroldomain.GeneratedPublicationRetirementRepository,
	proposals *changecontrolapplication.Service,
	targets authoringchangecontrol.TargetReader,
	runtimeStore *synthesispostgres.Store,
	modelRuns *agentpostgres.GORMRepository,
) (synthesisWorkerOwners, error) {
	if pool == nil || workspaces == nil || authoring == nil || runtimeStore == nil || proposals == nil || modelRuns == nil {
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
	verifier, err := organizingpostgres.NewAnchorRecommendationModelVerifier(modelRuns)
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	anchorStore, err := organizingpostgres.NewGORMAnchorStore(pool, sources, verifier)
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	store, err := organizingpostgres.NewGORMSynthesisStore(pool, organizingpostgres.SynthesisStoreDependencies{
		Authoring: authoring, Retirer: retirer, Sources: sources, Validated: runtimeStore, Anchors: anchorStore,
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
	profiles, err := capturepostgres.NewGORMProfileRepository(pool, modelRuns)
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	directory, err := organizingowner.NewKnowledgeDirectoryReader(profiles)
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	goalCatalog, err := organizingowner.NewSynthesisGoalCatalogReader(profiles, sources)
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	goalSelections, err := organizingpostgres.NewGORMGoalSelectionStore(pool, store, goalCatalog, modelRuns)
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	goalMaterials, err := organizingowner.NewSynthesisGoalMaterialReader(goalSelections, sources)
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	goalPreparer, err := organizingapplication.NewSynthesisGoalPreparer(goalMaterials, sources)
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	var manuscriptRuntime *organizingpostgres.SynthesisManuscriptRuntime
	if workspaceRuntime, ok := workspaces.(synthesisWorkspaceRuntime); ok {
		files, err := localfs.NewReader(workspaces)
		if err != nil {
			return synthesisWorkerOwners{}, err
		}
		roots, err := organizingowner.NewSynthesisManuscriptRoot(workspaceRuntime.resolver, files)
		if err != nil {
			return synthesisWorkerOwners{}, err
		}
		merger, err := gitmerge.New(gitcli.New("git"))
		if err != nil {
			return synthesisWorkerOwners{}, err
		}
		manuscriptRuntime, err = organizingpostgres.NewSynthesisManuscriptRuntime(organizingpostgres.SynthesisManuscriptRuntimeDependencies{
			Pool: pool, Candidates: store, Models: runtimeStore, Executions: runtimeStore, Roots: roots,
			Service: organizingapplication.SynthesisDependencies{Store: store, Sources: sources, Publications: publications, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}},
			Storage: organizingpostgres.SynthesisManuscriptStoreDependencies{Files: roots, Mapper: manuscript.Mapper{}, Merge: merger, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}},
		})
		if err != nil {
			return synthesisWorkerOwners{}, err
		}
	}
	sourceReviews, err := newSourceReviewWorkerStore(manuscriptRuntime, modelRuns)
	if err != nil {
		return synthesisWorkerOwners{}, err
	}
	return synthesisWorkerOwners{sourceReviews: sourceReviews, manuscripts: manuscriptRuntime, sources: sources, store: store, notes: notes, anchors: anchorStore, directory: directory, goalCatalog: goalCatalog, goalSelections: goalSelections, goalPreparer: goalPreparer}, nil
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
		Manuscripts: owners.manuscripts, Runs: runs, Store: store, Candidates: owners.notes, Sources: owners.sources, Anchors: owners.anchors, Goals: owners.goalPreparer, BodyRefresh: owners.store, Model: synthesisModel, Clock: foundation.SystemClock{},
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
	if err := organizingagent.RegisterAnchorRecommendationRuntimeCatalog(catalog); err != nil {
		return nil, err
	}
	if err := organizingagent.RegisterGoalSelectionRuntimeCatalog(catalog); err != nil {
		return nil, err
	}
	if err := organizingagent.RegisterSourceReviewRuntimeCatalog(catalog); err != nil {
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

func newAnchorRecommendationWorkerExecutor(
	owners synthesisWorkerOwners,
	runs organizingworkflow.WorkflowRunReader,
	modelRuns *agentpostgres.GORMRepository,
	model agentapplication.ChatModel,
	contract platformmodels.ChatContract,
	scheduler agentapplication.StructuredPhaseScheduler,
) (*organizingworkflow.AnchorRecommendationExecutor, error) {
	var recommendationModel organizingworkflow.AnchorRecommendationModel = organizingworkflow.NewUnavailableAnchorRecommendationModel(owners.anchors)
	if model != nil {
		catalog, err := newSynthesisRuntimeCatalog(contract)
		if err != nil {
			return nil, err
		}
		recommendationModel, err = organizingagent.NewAnchorModel(organizingagent.AnchorModelDependencies{
			Model: model, Scheduler: scheduler, Catalog: catalog, ModelRuns: modelRuns, Store: owners.anchors,
			ProfileRef: agentworkflow.DefaultProfileRef(), IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}, Budget: agentApplicationBudget(contract),
		})
		if err != nil {
			return nil, err
		}
	}
	return organizingworkflow.NewAnchorRecommendationExecutor(organizingworkflow.AnchorRecommendationExecutorDependencies{
		Runs: runs, Requests: owners.anchors, Notes: owners.notes, Sources: owners.sources, Anchors: owners.anchors, Model: recommendationModel,
	})
}

func registerSynthesisWorkerExecutors(registry *workflowapplication.ExecutorRegistry, executor *organizingworkflow.SynthesisExecutor) error {
	for _, kind := range organizingworkflow.SynthesisExecutorNodeKinds() {
		if err := registry.Register(kind, organizingworkflow.SynthesisInputSchemaVersion, executor); err != nil {
			return err
		}
	}
	return nil
}

func registerAnchorRecommendationWorkerExecutor(registry *workflowapplication.ExecutorRegistry, executor *organizingworkflow.AnchorRecommendationExecutor) error {
	return registry.Register(organizingworkflow.AnchorRecommendationNodeKind, organizingworkflow.AnchorRecommendationInputSchema, executor)
}

func registerSynthesisWorkerDefinitions(registry *workflowapplication.DefinitionRegistry) error {
	for _, definition := range organizingworkflow.SynthesisRegisteredDefinitions() {
		if err := registry.Register(definition); err != nil {
			return err
		}
	}
	for _, definition := range append(append(organizingworkflow.AnchorRecommendationDefinitions(), organizingworkflow.GoalSelectionDefinitions()...), organizingworkflow.SourceReviewDefinitions()...) {
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
	profiled, err := organizingowner.NewProfiledSynthesisOutbox(outbox)
	if err != nil {
		return nil, err
	}
	return organizingworkflow.NewSynthesisDispatcher(organizingworkflow.SynthesisDispatcherDependencies{
		Manuscripts: owners.manuscripts != nil, UnitOfWork: uow, Outbox: profiled, Sources: owners.sources, Processing: store, Starter: starter,
		Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
}

func newAnchorFusionDispatcher(pool *postgres.Pool, owners synthesisWorkerOwners, store *synthesispostgres.Store, starter workflowapplication.ScopedRuntimeStarter, definitions *workflowapplication.DefinitionRegistry) (*organizingworkflow.AnchorFusionDispatcher, error) {
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	return &organizingworkflow.AnchorFusionDispatcher{Manuscripts: owners.manuscripts != nil, UnitOfWork: uow, Requests: owners.anchors, Processing: store, Sources: owners.sources, Starter: starter, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}}, nil
}

func newAnchorRecommendationDispatcher(pool *postgres.Pool, owners synthesisWorkerOwners, starter workflowapplication.ScopedRuntimeStarter, definitions *workflowapplication.DefinitionRegistry) (*organizingworkflow.AnchorRecommendationDispatcher, error) {
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	return &organizingworkflow.AnchorRecommendationDispatcher{UnitOfWork: uow, Requests: owners.anchors, Starter: starter, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}}, nil
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

func dispatchAnchorFusions(ctx context.Context, logger *slog.Logger, dispatcher *organizingworkflow.AnchorFusionDispatcher, phase string) (organizingworkflow.SynthesisDispatchBatchResult, error) {
	result, err := dispatcher.DispatchBatch(ctx, 20)
	if err != nil {
		logger.Warn("anchor fusion dispatch failed", "phase", phase)
		return result, err
	}
	if result.Claimed > 0 {
		logger.Info("anchor fusions dispatched", "phase", phase, "started", result.Started)
	}
	return result, nil
}

func dispatchAnchorRecommendations(ctx context.Context, logger *slog.Logger, dispatcher *organizingworkflow.AnchorRecommendationDispatcher, phase string) (int, error) {
	if dispatcher == nil {
		return 0, foundation.NewError(foundation.ErrorDependencyUnavailable, "ANCHOR_RECOMMENDATION_DISPATCH_UNAVAILABLE", false, errors.New("anchor recommendation dispatcher is unavailable"))
	}
	started, err := dispatcher.DispatchBatch(ctx, 20)
	if err != nil {
		logger.Warn("anchor recommendation dispatch failed", "phase", phase)
		return started, err
	}
	if started > 0 {
		logger.Info("anchor recommendations dispatched", "phase", phase, "started", started)
	}
	return started, nil
}

func newAnchorDiscoveryDispatcher(owners synthesisWorkerOwners) *organizingworkflow.AnchorDiscoveryDispatcher {
	return &organizingworkflow.AnchorDiscoveryDispatcher{Store: owners.anchors, Directory: owners.directory, Sources: owners.sources, Requests: owners.anchors}
}
func dispatchAnchorDiscoveries(ctx context.Context, logger *slog.Logger, dispatcher *organizingworkflow.AnchorDiscoveryDispatcher, phase string) {
	if dispatcher == nil {
		return
	}
	completed, err := dispatcher.DispatchBatch(ctx, 20)
	if err != nil {
		logger.Warn("anchor source discovery incomplete", "phase", phase, "completed", completed, "error_code", "ANCHOR_DISCOVERY_UNAVAILABLE")
		return
	}
	if completed > 0 {
		logger.Info("anchor source discovery completed", "phase", phase, "completed", completed)
	}
}

func newGoalSelectionWorkerExecutor(owners synthesisWorkerOwners, runs organizingworkflow.WorkflowRunReader, modelRuns *agentpostgres.GORMRepository, model agentapplication.ChatModel, contract platformmodels.ChatContract, scheduler agentapplication.StructuredPhaseScheduler) (*organizingworkflow.GoalSelectionExecutor, error) {
	var selectionModel organizingworkflow.GoalSelectionModel = organizingworkflow.NewUnavailableGoalSelectionModel()
	if model != nil {
		catalog, err := newSynthesisRuntimeCatalog(contract)
		if err != nil {
			return nil, err
		}
		selectionModel, err = organizingagent.NewGoalSelectionModel(organizingagent.GoalSelectionModelDependencies{Model: model, ModelRuns: modelRuns, Store: owners.goalSelections, Catalog: catalog, Scheduler: scheduler, ProfileRef: agentworkflow.DefaultProfileRef(), IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}, Budget: agentApplicationBudget(contract)})
		if err != nil {
			return nil, err
		}
	}
	return organizingworkflow.NewGoalSelectionExecutor(runs, owners.goalSelections, selectionModel)
}
func registerGoalSelectionWorkerExecutor(registry *workflowapplication.ExecutorRegistry, executor *organizingworkflow.GoalSelectionExecutor) error {
	return registry.Register(organizingworkflow.GoalSelectionNodeKind, organizingworkflow.GoalSelectionInputSchema, executor)
}
func newGoalSelectionDispatcher(pool *postgres.Pool, owners synthesisWorkerOwners, workspaces workspaceruntimegrant.GORMRepositoryPort, starter workflowapplication.ScopedRuntimeStarter, definitions *workflowapplication.DefinitionRegistry) (*organizingworkflow.GoalSelectionDispatcher, error) {
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	return &organizingworkflow.GoalSelectionDispatcher{Workspaces: workspaces, Store: owners.goalSelections, UnitOfWork: uow, Starter: starter, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}}, nil
}

func newGoalGenerationDispatcher(pool *postgres.Pool, store *synthesispostgres.Store, workspaces workspaceruntimegrant.GORMRepositoryPort, starter workflowapplication.ScopedRuntimeStarter, definitions *workflowapplication.DefinitionRegistry) (*organizingworkflow.GoalGenerationDispatcher, error) {
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	return &organizingworkflow.GoalGenerationDispatcher{Workspaces: workspaces, Store: store, UnitOfWork: uow, Starter: starter, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}}, nil
}

func dispatchGoalGenerations(ctx context.Context, logger *slog.Logger, dispatcher *organizingworkflow.GoalGenerationDispatcher, timeout time.Duration) {
	dispatchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := dispatcher.DispatchBatch(dispatchCtx, 8)
	if err != nil {
		logger.Warn("goal generation dispatch failed", "error_code", organizingworkflow.ErrorCodeSynthesisExecutionUnavailable)
		return
	}
	if result.Started > 0 {
		logger.Info("goal generation workflows started", "started", result.Started)
	}
}

func newBodyRefreshGenerationDispatcher(pool *postgres.Pool, store *synthesispostgres.Store, workspaces workspaceruntimegrant.GORMRepositoryPort, starter workflowapplication.ScopedRuntimeStarter, definitions *workflowapplication.DefinitionRegistry) (*organizingworkflow.BodyRefreshGenerationDispatcher, error) {
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	_, manuscripts := workspaces.(synthesisWorkspaceRuntime)
	return &organizingworkflow.BodyRefreshGenerationDispatcher{Manuscripts: manuscripts, Workspaces: workspaces, Store: store, UnitOfWork: uow, Starter: starter, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}}, nil
}

func dispatchBodyRefreshGenerations(ctx context.Context, logger *slog.Logger, dispatcher *organizingworkflow.BodyRefreshGenerationDispatcher, timeout time.Duration) {
	dispatchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := dispatcher.DispatchBatch(dispatchCtx, 8)
	if err != nil {
		logger.Warn("body refresh generation dispatch failed", "error_code", organizingworkflow.ErrorCodeSynthesisExecutionUnavailable)
		return
	}
	if result.Started > 0 {
		logger.Info("body refresh generation workflows started", "started", result.Started)
	}
}
