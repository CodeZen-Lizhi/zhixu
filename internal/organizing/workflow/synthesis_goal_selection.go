package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	GoalSelectionDefinitionKey     = "organizing.goal-point-selection"
	GoalSelectionDefinitionVersion = int64(1)
	GoalSelectionNodeKind          = "organizing.goal-point-selection.run"
	GoalSelectionInputSchema       = 1
)

func GoalSelectionDefinitions() []workflowdomain.RegisteredDefinition {
	node := workflowdomain.NodeDefinition{Key: GoalSelectionNodeKind, Kind: GoalSelectionNodeKind, InputSchemaVersion: 1, OutputSchemaVersion: 1, RequiredPermissions: []workflowdomain.Permission{workflowdomain.PermissionReadLocal, workflowdomain.PermissionWriteProposal}, RetryPolicy: workflowdomain.RetryPolicy{}}
	return []workflowdomain.RegisteredDefinition{{Key: GoalSelectionDefinitionKey, Version: 1, InputSchemaVersion: 1, Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{node}}}}
}
func GoalSelectionStartKey(id foundation.ID, version int64) string {
	return "goal-selection-start:" + string(id) + ":" + strconv.FormatInt(version, 10)
}

type GoalSelectionDispatcher struct {
	Workspaces  workspacedomain.ActiveWorkspaceRepository
	Store       app.GoalSelectionDispatchStore
	UnitOfWork  foundation.UnitOfWork
	Starter     workflowapp.ScopedRuntimeStarter
	Definitions *workflowapp.DefinitionRegistry
	IDs         foundation.IDGenerator
	Clock       foundation.Clock
}

func (d *GoalSelectionDispatcher) DispatchBatch(ctx context.Context, limit int) (int, error) {
	if d == nil || ctx == nil || limit < 1 || limit > 100 || nilScopedDependency(d.Workspaces) || nilScopedDependency(d.Store) || nilScopedDependency(d.UnitOfWork) || nilScopedDependency(d.Starter) || d.Definitions == nil || nilScopedDependency(d.IDs) || nilScopedDependency(d.Clock) {
		return 0, goalSelectionUnavailable()
	}
	workspace, err := d.Workspaces.GetActiveWorkspace(ctx)
	if err != nil {
		return 0, err
	}
	definition, err := d.Definitions.Resolve(GoalSelectionDefinitionKey, GoalSelectionDefinitionVersion)
	if err != nil {
		return 0, err
	}
	if _, err := d.Store.ReconcileGoalSelectionWorkflows(ctx, workspace.ID, limit); err != nil {
		return 0, err
	}
	batches, err := d.Store.ListUnpreparedGoalSelectionBatches(ctx, workspace.ID, 4)
	if err != nil {
		return 0, err
	}
	var failures error
	for _, batch := range batches {
		if err := ctx.Err(); err != nil {
			return 0, errors.Join(failures, err)
		}
		if batch.WorkspaceID != workspace.ID {
			return 0, goalSelectionInvalid()
		}
		if _, err := d.Store.PrepareGoalSelections(ctx, workspace.ID, batch.RequestID, batch.BatchNo); err != nil {
			recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			deferErr := d.Store.DeferGoalSelectionPreparation(recoveryCtx, batch, goalSelectionFailureCode(err))
			cancel()
			failures = errors.Join(failures, err, deferErr)
		}
	}
	started := 0
	for started < limit {
		found := false
		err := d.UnitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			selection, ok, err := d.Store.ClaimPendingGoalSelectionScoped(ctx, scope, workspace.ID)
			if err != nil || !ok {
				return err
			}
			found = true
			if selection.WorkspaceID != workspace.ID || selection.Status != app.GoalSelectionPending || selection.ScheduledWorkflowID != "" {
				return goalSelectionInvalid()
			}
			input, err := json.Marshal(app.GoalSelectionStartInput{SelectionID: selection.ID, ExpectedVersion: selection.Version})
			if err != nil {
				return err
			}
			start, err := workflowapp.BuildRuntimeStartRequest(d.IDs, d.Clock, workspace.ID, GoalSelectionStartKey(selection.ID, selection.Version), input, definition)
			if err != nil {
				return err
			}
			result, err := d.Starter.StartScoped(ctx, scope, start)
			if err != nil {
				return err
			}
			if result.Run.WorkspaceID != workspace.ID || !validID(result.Run.ID) || result.Run.IdempotencyKey != start.Run.IdempotencyKey || result.Job.JobID < 1 {
				return goalSelectionInvalid()
			}
			return d.Store.BindGoalSelectionWorkflowScoped(ctx, scope, workspace.ID, selection.ID, result.Run.ID)
		})
		if err != nil {
			return started, errors.Join(failures, err)
		}
		if !found {
			break
		}
		started++
	}
	return started, failures
}

type GoalSelectionModel interface {
	Select(context.Context, workflowapp.ExecutionContext, app.SynthesisGoalSelection) (app.SynthesisGoalSelection, error)
}
type unavailableGoalSelectionModel struct{}

func NewUnavailableGoalSelectionModel() GoalSelectionModel { return unavailableGoalSelectionModel{} }
func (unavailableGoalSelectionModel) Select(_ context.Context, _ workflowapp.ExecutionContext, selection app.SynthesisGoalSelection) (app.SynthesisGoalSelection, error) {
	return selection, goalSelectionUnavailable()
}

type GoalSelectionExecutor struct {
	Runs  WorkflowRunReader
	Store app.GoalSelectionDispatchStore
	Model GoalSelectionModel
}

func NewGoalSelectionExecutor(runs WorkflowRunReader, store app.GoalSelectionDispatchStore, model GoalSelectionModel) (*GoalSelectionExecutor, error) {
	if nilScopedDependency(runs) || nilScopedDependency(store) || nilScopedDependency(model) {
		return nil, goalSelectionUnavailable()
	}
	return &GoalSelectionExecutor{Runs: runs, Store: store, Model: model}, nil
}
func (e *GoalSelectionExecutor) Execute(ctx context.Context, execution workflowapp.ExecutionContext) (workflowapp.ExecutionResult, error) {
	if e == nil || ctx == nil || execution.NodeKind != GoalSelectionNodeKind || execution.NodeKey != GoalSelectionNodeKind || execution.DefinitionVersion != GoalSelectionDefinitionVersion || execution.InputSchemaVersion != 1 || !validID(execution.WorkspaceID) || !validID(execution.RunID) || !validID(execution.NodeRunID) || !validID(execution.NodeAttemptID) {
		return workflowapp.ExecutionResult{}, goalSelectionInvalid()
	}
	run, err := e.Runs.GetRun(ctx, execution.RunID)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	input, err := app.DecodeGoalSelectionStartInput(run.Input)
	if err != nil || run.WorkspaceID != execution.WorkspaceID || run.ID != execution.RunID || run.IdempotencyKey != GoalSelectionStartKey(input.SelectionID, input.ExpectedVersion) {
		return workflowapp.ExecutionResult{}, goalSelectionInvalid()
	}
	selection, err := e.Store.GetGoalSelection(ctx, execution.WorkspaceID, input.SelectionID)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if selection.ScheduledWorkflowID != execution.RunID {
		return workflowapp.ExecutionResult{}, goalSelectionInvalid()
	}
	if selection.Status == app.GoalSelectionSucceeded {
		return goalSelectionReceipt(selection), nil
	}
	if selection.Status != app.GoalSelectionPending || selection.Version != input.ExpectedVersion {
		return workflowapp.ExecutionResult{}, goalSelectionInvalid()
	}
	result, err := e.Model.Select(ctx, execution, selection)
	if err != nil {
		retryable := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		var classified *foundation.Error
		if errors.As(err, &classified) {
			retryable = retryable || classified.Retryable
		}
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, saveErr := e.Store.FailScheduledGoalSelection(recoveryCtx, selection.WorkspaceID, selection.ID, execution.RunID, goalSelectionFailureCode(err), retryable)
		return workflowapp.ExecutionResult{}, errors.Join(err, saveErr)
	}
	if result.ID != selection.ID || result.WorkspaceID != selection.WorkspaceID || result.ScheduledWorkflowID != execution.RunID || result.Status != app.GoalSelectionSucceeded {
		return workflowapp.ExecutionResult{}, goalSelectionInvalid()
	}
	return goalSelectionReceipt(result), nil
}
func goalSelectionReceipt(selection app.SynthesisGoalSelection) workflowapp.ExecutionResult {
	raw, _ := json.Marshal(struct {
		SelectionID foundation.ID           `json:"selection_id"`
		Status      app.GoalSelectionStatus `json:"status"`
	}{selection.ID, selection.Status})
	return workflowapp.ExecutionResult{Output: raw}
}
func goalSelectionFailureCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return "SYNTHESIS_GOAL_SELECTION_FAILED"
}
func goalSelectionUnavailable() error {
	return workflowError(foundation.ErrorDependencyUnavailable, "SYNTHESIS_GOAL_SELECTION_UNAVAILABLE", true, "goal selection runtime is unavailable")
}
func goalSelectionInvalid() error {
	return workflowError(foundation.ErrorConsistencyViolation, "SYNTHESIS_GOAL_SELECTION_EXECUTION_INVALID", false, "goal selection execution binding is invalid")
}
