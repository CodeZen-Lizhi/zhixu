package workflow

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

type SynthesisGoalPreparation interface {
	Prepare(context.Context, foundation.ID, foundation.ID) (app.SynthesisGoalPreparedInput, error)
}

func (e *SynthesisExecutor) prepareGoalInput(ctx context.Context, execution workflowapp.ExecutionContext, loaded SynthesisExecution) (workflowapp.ExecutionResult, error) {
	if nilScopedDependency(e.dependencies.Goals) {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisExecutionUnavailable, true, "goal preparation is unavailable")
	}
	prepared, err := e.dependencies.Goals.Prepare(ctx, execution.WorkspaceID, loaded.GoalRequestID)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if prepared.Progress.Request.ID != loaded.GoalRequestID || prepared.Progress.Request.WorkspaceID != execution.WorkspaceID {
		return workflowapp.ExecutionResult{}, synthesisInvalid("goal preparation changed the requested identity")
	}
	binding, err := app.BuildSynthesisGoalBinding(prepared)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	frozen := SynthesisFrozenInput{Goal: binding, ProcessingID: loaded.Processing.ID, WorkflowRunID: execution.RunID, SourceEvent: loaded.Processing.SourceEvent, Notes: []SynthesisFrozenNote{}, Sources: make([]domain.SynthesisSourceRef, len(prepared.Sources))}
	for i, source := range prepared.Sources {
		frozen.Sources[i] = source.Excerpt.Reference
	}
	frozen.freezeSourceIdentityPromptVersions()
	frozen.RequestHash, err = frozen.ComputeHash()
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if err := frozen.Validate(); err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	stored, err := e.dependencies.Store.FreezeSynthesisInput(ctx, execution, frozen)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if stored.Validate() != nil || stored.Goal == nil || stored.Goal.RequestID != loaded.GoalRequestID || stored.ProcessingID != frozen.ProcessingID || stored.WorkflowRunID != frozen.WorkflowRunID {
		return workflowapp.ExecutionResult{}, synthesisInvalid("goal freeze returned a different binding")
	}
	return synthesisReceipt(execution, loaded.Processing.ID, stored.RequestHash, nil)
}
