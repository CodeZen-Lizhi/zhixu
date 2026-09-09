package synthesispostgres

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// OnWorkflowNodeTerminalScoped advances source processing in the same commit as
// Workflow terminal state. It never calls a model, publisher, or another UoW.
func (store *Store) OnWorkflowNodeTerminalScoped(ctx context.Context, scope foundation.TransactionScope, event workflowapp.WorkflowNodeTerminalEvent) error {
	if !slices.Contains(organizingworkflow.SynthesisExecutorNodeKinds(), event.NodeKind) {
		return nil
	}
	if !validID(event.WorkspaceID) || !validID(event.WorkflowRunID) || !validID(event.NodeRunID) || event.TerminalAt.IsZero() {
		return invalid("synthesis terminal event is invalid")
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return err
	}
	binding, err := store.dependencies.WorkflowBindings.LoadRuntimeBindingScoped(ctx, scope, workflowapp.ScopedRuntimeBindingQuery{
		WorkspaceID: event.WorkspaceID, WorkflowRunID: event.WorkflowRunID, NodeRunID: event.NodeRunID, NodeAttemptID: event.NodeAttemptID})
	if err != nil {
		return err
	}
	input, err := organizingworkflow.DecodeSynthesisStartInput(binding.RunInput)
	if err != nil || binding.DefinitionKey != organizingworkflow.SynthesisDefinitionKey || binding.DefinitionVersion != organizingworkflow.SynthesisDefinitionVersion || binding.NodeKey != event.NodeKind || binding.NodeType != event.NodeKind || (event.NodeAttemptID != "" && !binding.AttemptFound) {
		return invalid("synthesis terminal event does not match its Workflow")
	}
	processing, err := loadProcessing(tx, event.WorkspaceID, input.ProcessingID, true)
	if err != nil {
		return err
	}
	if idValue(processing.WorkflowRunID) != event.WorkflowRunID {
		return nil
	} // old execution retained after an explicit retry
	if processing.Status != string(organizingapp.SynthesisProcessingPending) && processing.Status != string(organizingapp.SynthesisProcessingRunning) {
		return nil
	}
	if event.Outcome == workflowapp.WorkflowTerminalOutcomeSucceeded {
		if event.NodeKind != organizingworkflow.SynthesisApplyNodeKind {
			return nil
		}
		row, err := loadExecution(tx, event.WorkspaceID, input.ProcessingID, event.WorkflowRunID, true)
		if err != nil {
			return err
		}
		if len(row.AppliedResult) == 0 {
			return invalid("synthesis terminal result has no application receipt")
		}
		var result organizingapp.SynthesisApplyResult
		if json.Unmarshal(row.AppliedResult, &result) != nil || result.ProcessingID != input.ProcessingID || result.Changed != (len(result.RevisionIDs) > 0) {
			return invalid("synthesis application receipt is invalid")
		}
		receipt, err := strictjson.DecodeObject[organizingworkflow.SynthesisNodeReceipt]([]byte(event.TerminalOutput), strictjson.DefaultLimits(), nil)
		if err != nil || receipt.SchemaVersion != organizingworkflow.SynthesisOutputSchemaVersion || receipt.ProcessingID != input.ProcessingID || receipt.WorkflowRunID != event.WorkflowRunID || receipt.Phase != event.NodeKind ||
			!slices.Equal(receipt.RevisionIDs, result.RevisionIDs) || (!row.ApplyRecovery && receipt.RequestHash != stringValue(row.InputHash)) {
			return invalid("synthesis Workflow output differs from the application receipt")
		}
		revisions, err := marshal(result.RevisionIDs)
		if err != nil {
			return err
		}
		status := organizingapp.SynthesisProcessingNoChange
		if result.Changed {
			status = organizingapp.SynthesisProcessingSucceeded
		}
		var models []modelStepModel
		if err := tx.Where("workspace_id=? AND workflow_run_id=? AND stage=? AND status=?", string(event.WorkspaceID), string(event.WorkflowRunID), string(organizingapp.SynthesisModelGenerate), string(organizingapp.SynthesisModelStepReady)).Find(&models).Error; err != nil {
			return classify(ctx, err)
		}
		var modelID *string
		if len(models) > 1 {
			return invalid("synthesis execution has multiple accepted generators")
		}
		if len(models) == 1 {
			modelID = models[0].ModelRunID
		}
		updated := tx.Model(&processingModel{}).Where("id=? AND workspace_id=? AND version=?", processing.ID, processing.WorkspaceID, processing.Version).
			Updates(map[string]any{"status": string(status), "revision_ids": revisions, "model_run_id": modelID, "error_code": nil, "retryable": false, "version": processing.Version + 1, "updated_at": canonical(event.TerminalAt), "completed_at": canonical(event.TerminalAt)})
		if updated.Error != nil {
			return classify(ctx, updated.Error)
		}
		if updated.RowsAffected != 1 {
			return conflict("synthesis terminal result CAS failed")
		}
		return nil
	}
	if event.Outcome != workflowapp.WorkflowTerminalOutcomeFailed && event.Outcome != workflowapp.WorkflowTerminalOutcomeCancelled {
		return invalid("synthesis terminal outcome is invalid")
	}
	code := event.FailureCode
	if code == "" {
		code = "SYNTHESIS_WORKFLOW_FAILED"
	}
	if event.Outcome == workflowapp.WorkflowTerminalOutcomeCancelled {
		code = "SYNTHESIS_WORKFLOW_CANCELLED"
	}
	var uncertain int64
	if err := tx.Model(&modelStepModel{}).Where("workspace_id=? AND workflow_run_id=? AND status IN ?", string(event.WorkspaceID), string(event.WorkflowRunID),
		[]string{string(organizingapp.SynthesisModelStepRunning), string(organizingapp.SynthesisModelStepRecoveryRequired)}).Count(&uncertain).Error; err != nil {
		return classify(ctx, err)
	}
	status := organizingapp.SynthesisProcessingFailed
	retryable := true // explicit user retry, never automatic Provider replay
	if event.FailureClass == workflowdomain.FailureClassManualRecovery || uncertain > 0 {
		status = organizingapp.SynthesisProcessingRecoveryRequired
		retryable = false
	}
	if code == organizingworkflow.ErrorCodeSynthesisInputStale || code == "SYNTHESIS_SOURCE_STALE" || code == "SYNTHESIS_SOURCE_INVALID" {
		retryable = false
	}
	updated := tx.Model(&processingModel{}).Where("id=? AND workspace_id=? AND version=?", processing.ID, processing.WorkspaceID, processing.Version).
		Updates(map[string]any{"status": string(status), "error_code": code, "retryable": retryable, "version": processing.Version + 1, "updated_at": canonical(event.TerminalAt), "completed_at": canonical(event.TerminalAt)})
	if updated.Error != nil {
		return classify(ctx, updated.Error)
	}
	if updated.RowsAffected != 1 {
		return conflict("synthesis terminal failure CAS failed")
	}
	return nil
}

var _ workflowapp.ScopedWorkflowTerminalHook = (*Store)(nil)
