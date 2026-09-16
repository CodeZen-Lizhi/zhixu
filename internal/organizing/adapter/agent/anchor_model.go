package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// AnchorModelStore 将推荐与已批准范围及笔记内容分开保存；完成操作可以创建提案，但不会接受或发布提案。
type AnchorModelStore interface {
	app.AnchorRecommendationExecutionStore
	app.AnchorRecommendations
	GetAnchorRecommendation(context.Context, foundation.ID, foundation.ID) (app.AnchorRecommendationRequest, error)
}

type AnchorModelDependencies struct {
	Model      agentapp.ChatModel
	ModelRuns  agentapp.ModelRunRepository
	Store      AnchorModelStore
	Catalog    *agentapp.RuntimeCatalog
	Scheduler  agentapp.StructuredPhaseScheduler
	ProfileRef agentdomain.ModelProfileRef
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
	Budget     agentapp.RunBudget
}

type AnchorModel struct{ dependencies AnchorModelDependencies }

func NewAnchorModel(dependencies AnchorModelDependencies) (*AnchorModel, error) {
	if synthesisNil(dependencies.Model) || synthesisNil(dependencies.ModelRuns) || synthesisNil(dependencies.Store) || synthesisNil(dependencies.IDs) || synthesisNil(dependencies.Clock) || dependencies.Catalog == nil {
		return nil, anchorModelUnavailable()
	}
	if dependencies.Budget == (agentapp.RunBudget{}) {
		dependencies.Budget = agentapp.DefaultRunBudget()
	}
	if _, err := agentapp.NewStructuredRunnerWithScheduler(dependencies.Model, dependencies.Catalog, dependencies.Budget, dependencies.Scheduler); err != nil {
		return nil, err
	}
	ref := agentdomain.SchemaRef{ID: agentdomain.AnchorRecommendationSchemaID, Version: "v1"}
	if _, err := dependencies.Catalog.Snapshot(agentdomain.PromptRef{ID: ref.ID, Version: ref.Version}, ref, ref, dependencies.ProfileRef); err != nil {
		return nil, err
	}
	return &AnchorModel{dependencies: dependencies}, nil
}

// Recommend 只执行已领取的工作流尝试。所属模块必须先打开精确冻结的摘录；构造输入时会在调用提供方前再次核验哈希和身份。
func (model *AnchorModel) Recommend(ctx context.Context, execution workflowapp.ExecutionContext, request app.AnchorRecommendationRequest, revision domain.SynthesisRevision, anchor *domain.Anchor, sources []app.SynthesisSourceView) (app.AnchorRecommendationRequest, error) {
	if model == nil || synthesisNil(model.dependencies.Model) {
		return request, anchorModelUnavailable()
	}
	if ctx == nil || execution.WorkspaceID != request.WorkspaceID || !synthesisValidID(execution.RunID) || !synthesisValidID(execution.NodeRunID) || !synthesisValidID(execution.NodeAttemptID) || execution.NodeRunID == execution.NodeAttemptID {
		return request, app.AnchorInvalid()
	}
	if err := ctx.Err(); err != nil {
		return request, err
	}
	payload, err := app.BuildAnchorRecommendationInput(request, revision, anchor, sources)
	if err != nil {
		return request, err
	}
	current, err := model.dependencies.Store.GetAnchorRecommendation(ctx, request.WorkspaceID, request.ID)
	if err != nil {
		return request, err
	}
	if current.Status != domain.AnchorRecommendationPending || current.Version != request.Version {
		return current, app.AnchorConflict()
	}
	if current.NoteID != request.NoteID || current.BasisRevisionID != request.BasisRevisionID || current.AnchorID != request.AnchorID || current.Kind != request.Kind || current.ExpectedScopeVersion != request.ExpectedScopeVersion || !reflect.DeepEqual(current.Evidence, request.Evidence) || !reflect.DeepEqual(current.Source, request.Source) {
		return current, app.AnchorConflict()
	}
	schema := agentdomain.SchemaRef{ID: agentdomain.AnchorRecommendationSchemaID, Version: "v1"}
	runRequest := agentapp.StructuredRunRequest{ProfileRef: model.dependencies.ProfileRef, PromptRef: agentdomain.PromptRef{ID: schema.ID, Version: schema.Version}, SchemaRef: schema, ReducedSchemaRef: schema, Input: payload}
	initial, err := agentapp.InitialStructuredRequest(model.dependencies.Catalog, runRequest)
	if err != nil {
		return request, err
	}
	rawRequest, err := json.Marshal(initial)
	if err != nil {
		return request, err
	}
	claimed, err := model.dependencies.Store.ClaimAnchorRecommendation(ctx, app.ClaimAnchorRecommendationCommand{WorkspaceID: request.WorkspaceID, RequestID: request.ID, ExpectedVersion: request.Version, WorkflowRunID: execution.RunID, NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID, ModelInputHash: synthesisHash(rawRequest)})
	if err != nil {
		return request, err
	}
	id, err := model.dependencies.IDs.New()
	if err != nil {
		return claimed, model.fail(ctx, claimed, nil, err)
	}
	now := model.now()
	run := agentdomain.ModelRun{ID: id, WorkspaceID: request.WorkspaceID, WorkflowRunID: execution.RunID, NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID, ModelSettingsRevision: cloneSynthesisRevisionNumber(execution.ModelSettingsRevision), Model: initial.Model, Profile: initial.ProfileRef, Prompt: initial.PromptRef, Schema: initial.SchemaRef, ReducedSchema: schema, Status: agentdomain.ModelRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now}
	stored, replayed, err := model.dependencies.ModelRuns.CreateModelRun(ctx, run)
	if err != nil {
		return claimed, model.recovery(ctx, claimed, err)
	}
	// 即使共享节点身份，第二个执行者也不得再次调用提供方。恢复由持久化工作流所属模块负责。
	if replayed {
		return claimed, app.AnchorConflict()
	}
	if !sameSynthesisModelRun(stored, run) || stored.Status != agentdomain.ModelRunRunning {
		return claimed, model.recovery(ctx, claimed, app.AnchorConflict())
	}
	run = stored
	recorded, err := agentapp.NewRecordingChatModel(agentapp.RecordingChatModelDependencies{Model: model.dependencies.Model, Repository: model.dependencies.ModelRuns, WorkspaceID: request.WorkspaceID, ModelRunID: run.ID, IDs: model.dependencies.IDs, Clock: model.dependencies.Clock})
	if err != nil {
		return claimed, model.fail(ctx, claimed, &run, err)
	}
	runner, err := agentapp.NewStructuredRunnerWithScheduler(recorded, model.dependencies.Catalog, model.dependencies.Budget, model.dependencies.Scheduler)
	if err != nil {
		return claimed, model.fail(ctx, claimed, &run, err)
	}
	result, err := runner.Run(ctx, runRequest)
	if err != nil {
		return claimed, model.fail(ctx, claimed, &run, err)
	}
	if err := ctx.Err(); err != nil {
		return claimed, model.recovery(ctx, claimed, err)
	}
	bound, err := app.BindAnchorModelOutput(result.Output, claimed)
	if err != nil {
		return claimed, model.fail(ctx, claimed, &run, err)
	}
	// 已有锚点的身份和标题保持稳定；范围推荐不构成重命名或替换锚点的授权。
	if anchor != nil && bound.Recommendation != nil && bound.Recommendation.Title != anchor.Title {
		return claimed, model.fail(ctx, claimed, &run, app.AnchorInvalid())
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), synthesisFinalizationTimeout)
	defer cancel()
	completedAt := model.now()
	if completedAt.Before(run.CreatedAt) {
		completedAt = run.CreatedAt
	}
	run.Status, run.FinalResultType, run.UpdatedAt, run.CompletedAt = agentdomain.ModelRunSucceeded, agentdomain.ResultTypeAnchorRecommendation, completedAt, &completedAt
	run.Version++
	if _, _, err := model.dependencies.ModelRuns.FinalizeModelRun(finalCtx, agentapp.FinalizeModelRunCommand{ExpectedVersion: run.Version - 1, Run: run}); err != nil {
		return claimed, model.recovery(finalCtx, claimed, err)
	}
	outputHash := synthesisHash(result.Output)
	if bound.NoRecommendation || request.Kind == app.AnchorInitialScopeRecommendation {
		completed, err := model.dependencies.Store.CompleteAnchorRecommendation(finalCtx, app.CompleteAnchorRecommendationCommand{WorkspaceID: request.WorkspaceID, RequestID: request.ID, ExpectedVersion: claimed.Version, ModelRunID: run.ID, ModelOutputHash: outputHash, ModelOutput: result.Output})
		if err == nil {
			return completed, nil
		}
		return model.resolveCompletion(finalCtx, claimed, run.ID, outputHash, err)
	}
	recommendation := bound.Recommendation
	_, err = model.dependencies.Store.RecordAnchorRecommendation(finalCtx, app.RecordAnchorRecommendation{RequestID: request.ID, WorkspaceID: request.WorkspaceID, AnchorID: request.AnchorID, IdempotencyKey: "anchor-model:" + string(request.ID), ExpectedScopeVersion: request.ExpectedScopeVersion, Kind: recommendation.Kind, Suggested: recommendation.Scope, Reason: recommendation.Reason, Evidence: recommendation.Evidence, ModelRunID: run.ID, ModelOutputHash: outputHash, ModelOutput: result.Output})
	return model.resolveCompletion(finalCtx, claimed, run.ID, outputHash, err)
}

func (model *AnchorModel) resolveCompletion(ctx context.Context, request app.AnchorRecommendationRequest, runID foundation.ID, outputHash string, cause error) (app.AnchorRecommendationRequest, error) {
	completed, err := model.dependencies.Store.GetAnchorRecommendation(ctx, request.WorkspaceID, request.ID)
	if err == nil && (completed.Status == domain.AnchorRecommendationSucceeded || completed.Status == domain.AnchorRecommendationNoRecommendation) && completed.ModelRunID == runID && synthesisHash(completed.ModelOutput) == outputHash {
		return completed, nil
	}
	return request, model.recovery(ctx, request, errors.Join(cause, err))
}

func (model *AnchorModel) fail(ctx context.Context, request app.AnchorRecommendationRequest, run *agentdomain.ModelRun, cause error) error {
	if ctx.Err() != nil || errors.Is(cause, context.Canceled) {
		return model.recovery(ctx, request, cause)
	}
	code, retryable := "ANCHOR_MODEL_FAILED", false
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		code, retryable = classified.Code, classified.Retryable
		if classified.Kind == foundation.ErrorManualRecoveryRequired {
			return model.recovery(ctx, request, cause)
		}
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), synthesisFinalizationTimeout)
	defer cancel()
	if run != nil {
		now := model.now()
		if now.Before(run.CreatedAt) {
			now = run.CreatedAt
		}
		run.Status, run.FinalErrorCode, run.UpdatedAt, run.CompletedAt = agentdomain.ModelRunFailed, code, now, &now
		run.Version++
		if _, _, err := model.dependencies.ModelRuns.FinalizeModelRun(finalCtx, agentapp.FinalizeModelRunCommand{ExpectedVersion: run.Version - 1, Run: *run}); err != nil {
			return model.recovery(finalCtx, request, errors.Join(cause, err))
		}
	}
	if _, err := model.dependencies.Store.FailAnchorRecommendation(finalCtx, app.FailAnchorRecommendationCommand{WorkspaceID: request.WorkspaceID, RequestID: request.ID, ExpectedVersion: request.Version, Status: domain.AnchorRecommendationFailed, ErrorCode: code, Retryable: retryable}); err != nil {
		return anchorModelRecovery(errors.Join(cause, err))
	}
	return cause
}

func (model *AnchorModel) recovery(ctx context.Context, request app.AnchorRecommendationRequest, cause error) error {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), synthesisFinalizationTimeout)
	defer cancel()
	_, err := model.dependencies.Store.FailAnchorRecommendation(finalCtx, app.FailAnchorRecommendationCommand{WorkspaceID: request.WorkspaceID, RequestID: request.ID, ExpectedVersion: request.Version, Status: domain.AnchorRecommendationRecoveryRequired, ErrorCode: "ANCHOR_MODEL_RECOVERY_REQUIRED"})
	return anchorModelRecovery(errors.Join(cause, err))
}
func (model *AnchorModel) now() time.Time {
	return model.dependencies.Clock.Now().UTC().Truncate(time.Microsecond)
}
func anchorModelUnavailable() error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "ANCHOR_MODEL_UNAVAILABLE", false, errors.New("anchor recommendation model is unavailable"))
}
func anchorModelRecovery(cause error) error {
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, "ANCHOR_MODEL_RECOVERY_REQUIRED", false, cause)
}
