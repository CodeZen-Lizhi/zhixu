package main

import (
	"context"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingagent "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/agent"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/manuscript"
	organizingpostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/postgres"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspaceruntimegrant "github.com/CodeZen-Lizhi/zhixu/internal/workspace/runtimegrant"
	"log/slog"
	"time"
)

type unavailableSourceReviewModel struct{}

func (unavailableSourceReviewModel) Review(context.Context, workflowapp.ExecutionContext, foundation.ID) (app.SynthesisManuscriptSourceReview, error) {
	return app.SynthesisManuscriptSourceReview{}, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_UNAVAILABLE")
}
func newSourceReviewWorkerExecutor(owners synthesisWorkerOwners, runs organizingworkflow.WorkflowRunReader, modelRuns *agentpostgres.GORMRepository, model agentapp.ChatModel, contract platformmodels.ChatContract, scheduler agentapp.StructuredPhaseScheduler) (*organizingworkflow.SourceReviewExecutor, error) {
	var reviewModel organizingworkflow.SourceReviewModel = unavailableSourceReviewModel{}
	if model != nil && owners.sourceReviews != nil {
		catalog, err := newSynthesisRuntimeCatalog(contract)
		if err != nil {
			return nil, err
		}
		reviewModel, err = organizingagent.NewSourceReviewModel(organizingagent.SourceReviewModelDependencies{Model: model, ModelRuns: modelRuns, Store: owners.sourceReviews, Catalog: catalog, Scheduler: scheduler, ProfileRef: agentworkflow.DefaultProfileRef(), IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}, Budget: agentApplicationBudget(contract)})
		if err != nil {
			return nil, err
		}
	}
	return &organizingworkflow.SourceReviewExecutor{Runs: runs, Store: owners.sourceReviews, Model: reviewModel}, nil
}
func newSourceReviewWorkerStore(runtime *organizingpostgres.SynthesisManuscriptRuntime, models *agentpostgres.GORMRepository) (*organizingpostgres.GORMSynthesisManuscriptSourceReviewStore, error) {
	if runtime == nil {
		return nil, nil
	}
	return organizingpostgres.NewGORMSynthesisManuscriptSourceReviewStore(runtime, models, manuscript.Mapper{})
}
func registerSourceReviewWorkerExecutor(registry *workflowapp.ExecutorRegistry, executor *organizingworkflow.SourceReviewExecutor) error {
	for _, kind := range []string{app.SynthesisSourceReviewPrepare, app.SynthesisSourceReviewModel, app.SynthesisSourceReviewApply, app.SynthesisSourceReviewRecover} {
		if err := registry.Register(kind, 1, executor); err != nil {
			return err
		}
	}
	return nil
}
func newSourceReviewDispatcher(pool *postgres.Pool, owners synthesisWorkerOwners, workspaces workspaceruntimegrant.GORMRepositoryPort, starter workflowapp.ScopedRuntimeStarter, definitions *workflowapp.DefinitionRegistry) (*organizingworkflow.SourceReviewDispatcher, error) {
	if owners.sourceReviews == nil {
		return nil, nil
	}
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	if err := owners.sourceReviews.ConfigureSourceReviewCommands(starter, definitions); err != nil {
		return nil, err
	}
	return &organizingworkflow.SourceReviewDispatcher{Workspaces: workspaces, Store: owners.sourceReviews, UnitOfWork: uow, Starter: starter, Definitions: definitions, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}}, nil
}
func dispatchSourceReviews(ctx context.Context, logger *slog.Logger, dispatcher *organizingworkflow.SourceReviewDispatcher, timeout time.Duration) {
	if dispatcher == nil {
		return
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	count, err := dispatcher.DispatchBatch(bounded, 10)
	if err != nil {
		logger.Warn("current manuscript source review dispatch incomplete", "started", count, "error_code", "SYNTHESIS_SOURCE_REVIEW_DISPATCH_UNAVAILABLE")
	} else if count > 0 {
		logger.Info("current manuscript source reviews scheduled", "started", count)
	}
}
