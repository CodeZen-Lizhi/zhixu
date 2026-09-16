package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// GoalSelectionModelStore 管理持久化选择的生命周期。适配器通过该模块重建提供方输入，绝不从工作流消息接收输入。
type GoalSelectionModelStore interface {
	app.SynthesisGoalSelectionStore
}

type GoalSelectionModelDependencies struct {
	Model      agentapp.ChatModel
	ModelRuns  agentapp.ModelRunRepository
	Store      GoalSelectionModelStore
	Catalog    *agentapp.RuntimeCatalog
	Scheduler  agentapp.StructuredPhaseScheduler
	ProfileRef agentdomain.ModelProfileRef
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
	Budget     agentapp.RunBudget
}

// GoalSelectionModel 先持久化精确的提供方请求，才允许对一个不可变目录切片执行有界的结构化选择调用。
type GoalSelectionModel struct {
	dependencies GoalSelectionModelDependencies
}

func NewGoalSelectionModel(dependencies GoalSelectionModelDependencies) (*GoalSelectionModel, error) {
	if synthesisNil(dependencies.Model) || synthesisNil(dependencies.ModelRuns) || synthesisNil(dependencies.Store) ||
		synthesisNil(dependencies.IDs) || synthesisNil(dependencies.Clock) || dependencies.Catalog == nil || dependencies.ProfileRef.Validate() != nil {
		return nil, goalSelectionModelUnavailable()
	}
	if dependencies.Budget == (agentapp.RunBudget{}) {
		dependencies.Budget = agentapp.DefaultRunBudget()
	}
	if _, err := agentapp.NewStructuredRunnerWithScheduler(dependencies.Model, dependencies.Catalog, dependencies.Budget, dependencies.Scheduler); err != nil {
		return nil, err
	}
	ref := agentdomain.SchemaRef{ID: agentdomain.GoalSelectionSchemaID, Version: "v1"}
	if _, err := dependencies.Catalog.Snapshot(agentdomain.PromptRef{ID: ref.ID, Version: ref.Version}, ref, ref, dependencies.ProfileRef); err != nil {
		return nil, err
	}
	return &GoalSelectionModel{dependencies: dependencies}, nil
}

// Select 执行一次已领取的选择；已有成功结果直接返回，不再调用模型。RUNNING 和需要恢复的状态交由持久化工作流所属模块处理。
func (model *GoalSelectionModel) Select(ctx context.Context, execution workflowapp.ExecutionContext, selection app.SynthesisGoalSelection) (app.SynthesisGoalSelection, error) {
	if model == nil || synthesisNil(model.dependencies.Model) || synthesisNil(model.dependencies.Store) {
		return selection, goalSelectionModelUnavailable()
	}
	if ctx == nil || execution.WorkspaceID != selection.WorkspaceID || !synthesisValidID(execution.RunID) || !synthesisValidID(execution.NodeRunID) || !synthesisValidID(execution.NodeAttemptID) || execution.NodeRunID == execution.NodeAttemptID {
		return selection, goalSelectionModelContextInvalid()
	}
	if err := ctx.Err(); err != nil {
		return selection, err
	}
	current, err := model.dependencies.Store.GetGoalSelection(ctx, selection.WorkspaceID, selection.ID)
	if err != nil {
		return selection, err
	}
	if !sameGoalSelectionIdentity(current, selection) {
		return current, goalSelectionModelConflict()
	}
	if current.Status == app.GoalSelectionSucceeded {
		return current, nil
	}
	if current.Status != selection.Status || current.Version != selection.Version {
		return current, goalSelectionModelConflict()
	}
	if current.Status != app.GoalSelectionPending {
		return current, goalSelectionModelConflict()
	}
	input, err := model.dependencies.Store.ReadGoalSelectionInput(ctx, current.WorkspaceID, current.ID)
	if err != nil {
		return current, err
	}
	payload, err := validateGoalSelectionInput(current, input)
	if err != nil {
		return current, err
	}
	ref := agentdomain.SchemaRef{ID: agentdomain.GoalSelectionSchemaID, Version: "v1"}
	runRequest := agentapp.StructuredRunRequest{
		ProfileRef: model.dependencies.ProfileRef,
		PromptRef:  agentdomain.PromptRef{ID: ref.ID, Version: ref.Version},
		SchemaRef:  ref, ReducedSchemaRef: ref, Input: payload,
	}
	initial, err := agentapp.InitialStructuredRequest(model.dependencies.Catalog, runRequest)
	if err != nil {
		return current, err
	}
	rawRequest, err := json.Marshal(initial)
	if err != nil {
		return current, err
	}
	claimed, err := model.dependencies.Store.ClaimGoalSelection(ctx, app.ClaimGoalSelectionCommand{
		WorkspaceID: current.WorkspaceID, SelectionID: current.ID, ExpectedVersion: current.Version,
		WorkflowRunID: execution.RunID, NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
		ModelInputHash: synthesisHash(rawRequest),
	})
	if err != nil {
		return current, model.recovery(ctx, current, err)
	}
	if !validClaimedGoalSelection(current, claimed, execution, synthesisHash(rawRequest)) {
		return claimed, model.recovery(ctx, claimed, goalSelectionModelConflict())
	}
	id, err := model.dependencies.IDs.New()
	if err != nil {
		return claimed, model.fail(ctx, claimed, nil, err)
	}
	now := model.now()
	run := agentdomain.ModelRun{
		ID: id, WorkspaceID: claimed.WorkspaceID, WorkflowRunID: execution.RunID, NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
		ModelSettingsRevision: cloneSynthesisRevisionNumber(execution.ModelSettingsRevision), Model: initial.Model,
		Profile: initial.ProfileRef, Prompt: initial.PromptRef, Schema: initial.SchemaRef, ReducedSchema: ref,
		Status: agentdomain.ModelRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	stored, replayed, err := model.dependencies.ModelRuns.CreateModelRun(ctx, run)
	if err != nil {
		return claimed, model.recovery(ctx, claimed, err)
	}
	if replayed || !sameSynthesisModelRun(stored, run) || stored.Status != agentdomain.ModelRunRunning || stored.Version != 1 {
		return claimed, model.recovery(ctx, claimed, goalSelectionModelConflict())
	}
	run = stored
	recorded, err := agentapp.NewRecordingChatModel(agentapp.RecordingChatModelDependencies{
		Model: model.dependencies.Model, Repository: model.dependencies.ModelRuns, WorkspaceID: claimed.WorkspaceID,
		ModelRunID: run.ID, IDs: model.dependencies.IDs, Clock: model.dependencies.Clock,
	})
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
	if _, err := app.BindGoalSelectionOutput(result.Output, input); err != nil {
		return claimed, model.fail(ctx, claimed, &run, err)
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), synthesisFinalizationTimeout)
	defer cancel()
	completedAt := model.now()
	if completedAt.Before(run.CreatedAt) {
		completedAt = run.CreatedAt
	}
	run.Status, run.FinalResultType, run.UpdatedAt, run.CompletedAt = agentdomain.ModelRunSucceeded, agentdomain.ResultTypeGoalSelection, completedAt, &completedAt
	run.Version++
	if _, _, err := model.dependencies.ModelRuns.FinalizeModelRun(finalCtx, agentapp.FinalizeModelRunCommand{ExpectedVersion: run.Version - 1, Run: run}); err != nil {
		return claimed, model.recovery(finalCtx, claimed, err)
	}
	completed, err := model.dependencies.Store.CompleteGoalSelection(finalCtx, app.CompleteGoalSelectionCommand{
		WorkspaceID: claimed.WorkspaceID, SelectionID: claimed.ID, ExpectedVersion: claimed.Version,
		ModelRunID: run.ID, ModelOutput: append([]byte(nil), result.Output...),
	})
	if err == nil {
		return completed, nil
	}
	return model.resolveCompletion(finalCtx, claimed, run.ID, result.Output, err)
}

func validateGoalSelectionInput(selection app.SynthesisGoalSelection, input app.SynthesisGoalSelectionInput) ([]byte, error) {
	if input.RequestID != selection.RequestID || input.CatalogBatchID != selection.CatalogBatchID || input.Source.WorkspaceID != selection.WorkspaceID ||
		input.SourceOrdinal != selection.SourceOrdinal || input.PointOffset != selection.PointOffset || len(input.Points) != selection.PointCount {
		return nil, goalSelectionModelConflict()
	}
	payload, err := app.BuildGoalSelectionPayload(input)
	if err != nil || synthesisHash(payload) != selection.PayloadHash {
		return nil, goalSelectionModelConflict()
	}
	locators := make(map[app.KnowledgePointLocator]struct{}, len(input.Points))
	for _, point := range input.Points {
		if point.Locator.ProfileRevisionID != input.ProfileRevisionID || point.Locator.Index < 0 {
			return nil, goalSelectionModelConflict()
		}
		if _, exists := locators[point.Locator]; exists {
			return nil, goalSelectionModelConflict()
		}
		locators[point.Locator] = struct{}{}
		spans := make(map[foundation.ID]struct{}, len(point.SourceSpanIDs))
		for _, spanID := range point.SourceSpanIDs {
			if !synthesisValidID(spanID) {
				return nil, goalSelectionModelConflict()
			}
			if _, exists := spans[spanID]; exists {
				return nil, goalSelectionModelConflict()
			}
			spans[spanID] = struct{}{}
		}
	}
	return payload, nil
}

func sameGoalSelectionIdentity(left, right app.SynthesisGoalSelection) bool {
	return left.ID == right.ID && left.WorkspaceID == right.WorkspaceID && left.RequestID == right.RequestID && left.CatalogBatchID == right.CatalogBatchID &&
		left.SourceOrdinal == right.SourceOrdinal && left.PointOffset == right.PointOffset && left.PointCount == right.PointCount && left.PayloadHash == right.PayloadHash &&
		left.ScheduledWorkflowID == right.ScheduledWorkflowID
}

func validClaimedGoalSelection(before, claimed app.SynthesisGoalSelection, execution workflowapp.ExecutionContext, inputHash string) bool {
	return claimed.ID == before.ID && claimed.WorkspaceID == before.WorkspaceID && claimed.RequestID == before.RequestID && claimed.CatalogBatchID == before.CatalogBatchID &&
		claimed.SourceOrdinal == before.SourceOrdinal && claimed.PointOffset == before.PointOffset && claimed.PointCount == before.PointCount && claimed.PayloadHash == before.PayloadHash &&
		claimed.Status == app.GoalSelectionRunning && claimed.Version == before.Version+1 && claimed.ScheduledWorkflowID == before.ScheduledWorkflowID && claimed.WorkflowRunID == execution.RunID && claimed.NodeRunID == execution.NodeRunID &&
		claimed.NodeAttemptID == execution.NodeAttemptID && claimed.ModelInputHash == inputHash
}

func (model *GoalSelectionModel) resolveCompletion(ctx context.Context, selection app.SynthesisGoalSelection, runID foundation.ID, output []byte, cause error) (app.SynthesisGoalSelection, error) {
	completed, err := model.dependencies.Store.GetGoalSelection(ctx, selection.WorkspaceID, selection.ID)
	if err == nil && completed.Status == app.GoalSelectionSucceeded && completed.ModelRunID == runID && bytes.Equal(completed.ModelOutput, output) {
		return completed, nil
	}
	return selection, model.recovery(ctx, selection, errors.Join(cause, err))
}

func (model *GoalSelectionModel) fail(ctx context.Context, selection app.SynthesisGoalSelection, run *agentdomain.ModelRun, cause error) error {
	if ctx.Err() != nil || errors.Is(cause, context.Canceled) {
		return model.recovery(ctx, selection, cause)
	}
	code, retryable := "SYNTHESIS_GOAL_SELECTION_FAILED", false
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		code, retryable = classified.Code, classified.Retryable
		if classified.Kind == foundation.ErrorManualRecoveryRequired {
			return model.recovery(ctx, selection, cause)
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
			return model.recovery(finalCtx, selection, errors.Join(cause, err))
		}
	}
	if _, err := model.dependencies.Store.FailGoalSelection(finalCtx, app.FailGoalSelectionCommand{
		WorkspaceID: selection.WorkspaceID, SelectionID: selection.ID, ExpectedVersion: selection.Version,
		Status: app.GoalSelectionFailed, ErrorCode: code, Retryable: retryable,
	}); err != nil {
		return goalSelectionModelRecovery(errors.Join(cause, err))
	}
	return cause
}

func (model *GoalSelectionModel) recovery(ctx context.Context, selection app.SynthesisGoalSelection, cause error) error {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), synthesisFinalizationTimeout)
	defer cancel()
	_, err := model.dependencies.Store.FailGoalSelection(finalCtx, app.FailGoalSelectionCommand{
		WorkspaceID: selection.WorkspaceID, SelectionID: selection.ID, ExpectedVersion: selection.Version,
		Status: app.GoalSelectionRecoveryRequired, ErrorCode: "SYNTHESIS_GOAL_SELECTION_RECOVERY_REQUIRED",
	})
	return goalSelectionModelRecovery(errors.Join(cause, err))
}

func (model *GoalSelectionModel) now() time.Time {
	return model.dependencies.Clock.Now().UTC().Truncate(time.Microsecond)
}

func goalSelectionModelUnavailable() error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "SYNTHESIS_GOAL_SELECTION_UNAVAILABLE", false, errors.New("goal selection model is unavailable"))
}

func goalSelectionModelContextInvalid() error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "SYNTHESIS_GOAL_SELECTION_CONTEXT_INVALID", false, errors.New("goal selection execution binding is invalid"))
}

func goalSelectionModelConflict() error {
	return foundation.NewError(foundation.ErrorVersionConflict, "SYNTHESIS_GOAL_SELECTION_CONFLICT", false, errors.New("goal selection state or immutable input changed"))
}

func goalSelectionModelRecovery(cause error) error {
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, "SYNTHESIS_GOAL_SELECTION_RECOVERY_REQUIRED", false, cause)
}
