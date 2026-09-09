package synthesispostgres

import (
	"bytes"
	"context"
	"errors"
	"reflect"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (store *Store) LookupReady(ctx context.Context, workspaceID, nodeRunID foundation.ID, stage organizingapp.SynthesisModelStage, requestHash string) (organizingapp.SynthesisModelStepRecord, bool, error) {
	if err := store.ready(ctx); err != nil {
		return organizingapp.SynthesisModelStepRecord{}, false, err
	}
	if !validID(workspaceID) || !validID(nodeRunID) || synthesisStageKind(stage) == "" || !validHash(requestHash) {
		return organizingapp.SynthesisModelStepRecord{}, false, invalid("synthesis model result lookup is invalid")
	}
	var row modelStepModel
	err := store.database.WithContext(ctx).Where("workspace_id=? AND node_run_id=? AND status=?", string(workspaceID), string(nodeRunID), string(organizingapp.SynthesisModelStepReady)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return organizingapp.SynthesisModelStepRecord{}, false, nil
	}
	if err != nil {
		return organizingapp.SynthesisModelStepRecord{}, false, classify(ctx, err)
	}
	if row.Stage != string(stage) || row.RequestHash != requestHash {
		return organizingapp.SynthesisModelStepRecord{}, false, conflict("synthesis accepted model result belongs to a different request")
	}
	result, err := row.projection()
	return result, true, err
}

func (store *Store) Prepare(ctx context.Context, command organizingapp.PrepareSynthesisModelStepCommand) (organizingapp.SynthesisModelStepRecord, bool, error) {
	record := command.Record
	if record.Validate() != nil || record.Status != organizingapp.SynthesisModelStepRunning || record.Version != 1 || record.ModelRunID != "" {
		return organizingapp.SynthesisModelStepRecord{}, false, invalid("synthesis model preparation is invalid")
	}
	var result organizingapp.SynthesisModelStepRecord
	var replayed bool
	err := store.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, _ *gorm.DB) error {
		tx, execution, err := store.bindLive(ctx, scope, record.ProcessingID, stepExecution(record))
		if err != nil {
			return err
		}
		if execution.ApplyRecovery || stringValue(execution.InputHash) != record.InputRequestHash {
			return invalid("synthesis model request does not match its frozen execution input")
		}
		var row modelStepModel
		err = tx.Where("workspace_id=? AND node_attempt_id=?", string(record.WorkspaceID), string(record.NodeAttemptID)).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row).Error
		if err == nil {
			existing, err := row.projection()
			if err != nil {
				return err
			}
			if !sameModelStepRequest(existing, record) {
				return conflict("synthesis attempt already binds another model request")
			}
			result, replayed = existing, true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return classify(ctx, err)
		}
		err = tx.Where("workspace_id=? AND workflow_run_id=? AND stage=?", string(record.WorkspaceID), string(record.WorkflowRunID), string(record.Stage)).Order("created_at,id").Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row).Error
		if err == nil {
			existing, err := row.projection()
			if err != nil {
				return err
			}
			if existing.Status == organizingapp.SynthesisModelStepReady && existing.NodeRunID == record.NodeRunID && existing.RequestHash == record.RequestHash && existing.InputRequestHash == record.InputRequestHash {
				result, replayed = existing, true
				return nil
			}
			if existing.Status == organizingapp.SynthesisModelStepFailed {
				// A lost Workflow failure acknowledgement can cause lease rescue
				// even with MaxRetries=0. Known model failure is still terminal in
				// this run; only a new user-authorized execution may call again.
				return foundation.NewError(foundation.ErrorNonRetryableFailure, existing.ErrorCode, false, errors.New("synthesis model stage already failed in this Workflow"))
			}
			return recovery("an earlier synthesis model attempt has no safe replay result")
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return classify(ctx, err)
		}
		if record.Stage == organizingapp.SynthesisModelValidate {
			var generated modelStepModel
			if err := tx.Where("workspace_id=? AND workflow_run_id=? AND stage=? AND status=?", string(record.WorkspaceID), string(record.WorkflowRunID), string(organizingapp.SynthesisModelGenerate), string(organizingapp.SynthesisModelStepReady)).Take(&generated).Error; err != nil {
				return classify(ctx, err)
			}
			if stringValue(generated.OutputHash) != record.GenerationOutputHash || generated.InputRequestHash != record.InputRequestHash {
				return invalid("synthesis semantic request is not bound to its generation")
			}
		}
		row = modelStep(record)
		if err := tx.Create(&row).Error; err != nil {
			return classify(ctx, err)
		}
		result = record
		return nil
	})
	return result, replayed, err
}

func (store *Store) BindModelRun(ctx context.Context, command organizingapp.BindSynthesisModelRunCommand) (organizingapp.SynthesisModelStepRecord, error) {
	if !validID(command.WorkspaceID) || !validID(command.StepID) || !validID(command.ModelRunID) || command.ExpectedVersion < 1 {
		return organizingapp.SynthesisModelStepRecord{}, invalid("synthesis model run binding is invalid")
	}
	var result organizingapp.SynthesisModelStepRecord
	err := store.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		step, err := store.lockModelStep(ctx, scope, tx, command.WorkspaceID, command.StepID)
		if err != nil {
			return err
		}
		replayed := step.ModelRunID == command.ModelRunID && step.Status == organizingapp.SynthesisModelStepRunning
		if !replayed && (step.Status != organizingapp.SynthesisModelStepRunning || step.ModelRunID != "" || step.Version != command.ExpectedVersion) {
			return conflict("synthesis model step is not bindable")
		}
		run, err := store.dependencies.ModelRuns.GetModelRunScoped(ctx, scope, command.WorkspaceID, command.ModelRunID, true)
		if err != nil {
			return err
		}
		if !sameModelRunBinding(run, step) || run.Status != agentdomain.ModelRunRunning || run.Version != 1 || !validModelRuntime(run, step.Stage) {
			return invalid("synthesis model run differs from its prepared step")
		}
		if replayed {
			result = step
			return nil
		}
		updated := tx.Model(&modelStepModel{}).Where("workspace_id=? AND id=? AND version=? AND model_run_id IS NULL", string(step.WorkspaceID), string(step.ID), step.Version).
			Updates(map[string]any{"model_run_id": string(run.ID), "version": step.Version + 1, "updated_at": gorm.Expr("GREATEST(updated_at,?)", canonical(run.UpdatedAt))})
		if updated.Error != nil {
			return classify(ctx, updated.Error)
		}
		if updated.RowsAffected != 1 {
			return conflict("synthesis model run binding CAS failed")
		}
		step.ModelRunID, step.Version = run.ID, step.Version+1
		if run.UpdatedAt.After(step.UpdatedAt) {
			step.UpdatedAt = canonical(run.UpdatedAt)
		}
		result = step
		return result.Validate()
	})
	return result, err
}

func (store *Store) Complete(ctx context.Context, command organizingapp.CompleteSynthesisModelStepCommand) (organizingapp.SynthesisModelStepRecord, bool, error) {
	if !validID(command.WorkspaceID) || !validID(command.StepID) || !validID(command.ModelRunID) || command.ExpectedVersion < 1 || command.ExpectedModelRunVersion < 1 || command.CompletedAt.IsZero() {
		return organizingapp.SynthesisModelStepRecord{}, false, invalid("synthesis model completion is invalid")
	}
	var result organizingapp.SynthesisModelStepRecord
	var replayed bool
	err := store.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		// An exact terminal receipt can be recovered even after lease completion.
		row, err := loadStep(tx, command.WorkspaceID, command.StepID, false)
		if err != nil {
			return err
		}
		step, err := row.projection()
		if err != nil {
			return err
		}
		if step.Status == organizingapp.SynthesisModelStepReady {
			if !sameStepCompletion(step, command) {
				return conflict("synthesis model completion changed its committed result")
			}
			result, replayed = step, true
			return nil
		}
		step, err = store.lockModelStep(ctx, scope, tx, command.WorkspaceID, command.StepID)
		if err != nil {
			return err
		}
		if step.Status == organizingapp.SynthesisModelStepReady {
			if !sameStepCompletion(step, command) {
				return conflict("synthesis model completion changed its committed result")
			}
			result, replayed = step, true
			return nil
		}
		if step.Status != organizingapp.SynthesisModelStepRunning || step.Version != command.ExpectedVersion || step.ModelRunID != command.ModelRunID || synthesisResultType(step.Stage) != command.ResultType {
			return conflict("synthesis model step is not completable")
		}
		next := step
		next.Status = organizingapp.SynthesisModelStepReady
		next.Output = append([]byte(nil), command.Output...)
		next.OutputHash = command.OutputHash
		next.Generation, next.Semantic = command.Generation, command.Semantic
		next.Version++
		next.UpdatedAt = canonical(command.CompletedAt)
		next.CompletedAt = timePointer(command.CompletedAt)
		if err := next.Validate(); err != nil {
			return err
		}
		model, err := store.dependencies.ModelRuns.GetModelRunRecordScoped(ctx, scope, step.WorkspaceID, step.ModelRunID, true)
		if err != nil {
			return err
		}
		if model.Run.Status != agentdomain.ModelRunRunning || model.Run.Version != command.ExpectedModelRunVersion {
			return invalid("synthesis model run is not running at the expected version")
		}
		if err := validateModelProof(model, step, command.OutputHash, int64(len(command.Output))); err != nil {
			return err
		}
		terminal := model.Run
		terminal.Status = agentdomain.ModelRunSucceeded
		terminal.FinalResultType = command.ResultType
		terminal.FinalErrorCode = ""
		terminal.Version++
		terminal.UpdatedAt = canonical(command.CompletedAt)
		terminal.CompletedAt = timePointer(command.CompletedAt)
		if _, _, err := store.dependencies.ModelRuns.FinalizeModelRunScoped(ctx, scope, agentapp.FinalizeModelRunCommand{ExpectedVersion: command.ExpectedModelRunVersion, Run: terminal}); err != nil {
			return err
		}
		var generation, semantic jsonValue
		if command.Generation != nil {
			generation, err = marshal(command.Generation)
			if err != nil {
				return err
			}
		}
		if command.Semantic != nil {
			semantic, err = marshal(command.Semantic)
			if err != nil {
				return err
			}
		}
		updated := tx.Model(&modelStepModel{}).Where("workspace_id=? AND id=? AND version=? AND status=?", string(step.WorkspaceID), string(step.ID), step.Version, string(organizingapp.SynthesisModelStepRunning)).
			Updates(map[string]any{"status": string(next.Status), "output_document": []byte(command.Output), "output_hash": command.OutputHash, "generation_result": generation, "semantic_receipt": semantic,
				"version": next.Version, "updated_at": next.UpdatedAt, "completed_at": next.CompletedAt})
		if updated.Error != nil {
			return classify(ctx, updated.Error)
		}
		if updated.RowsAffected != 1 {
			return conflict("synthesis model completion CAS failed")
		}
		result = next
		return nil
	})
	return result, replayed, err
}

func (store *Store) Fail(ctx context.Context, command organizingapp.FailSynthesisModelStepCommand) error {
	if !validID(command.WorkspaceID) || !validID(command.StepID) || !validID(command.ModelRunID) || command.ExpectedVersion < 1 || command.ExpectedModelRunVersion < 1 || command.CompletedAt.IsZero() ||
		(command.ModelRunStatus != agentdomain.ModelRunFailed && command.ModelRunStatus != agentdomain.ModelRunUnknown) {
		return invalid("synthesis model failure is invalid")
	}
	return store.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		row, err := loadStep(tx, command.WorkspaceID, command.StepID, false)
		if err != nil {
			return err
		}
		step, err := row.projection()
		if err != nil {
			return err
		}
		nextStatus := organizingapp.SynthesisModelStepFailed
		if command.ModelRunStatus == agentdomain.ModelRunUnknown {
			nextStatus = organizingapp.SynthesisModelStepRecoveryRequired
		}
		if step.Status == nextStatus && step.ModelRunID == command.ModelRunID && step.ErrorCode == command.ErrorCode && step.Retryable == command.Retryable {
			return nil
		}
		step, err = store.lockModelStep(ctx, scope, tx, command.WorkspaceID, command.StepID)
		if err != nil {
			return err
		}
		if step.Status != organizingapp.SynthesisModelStepRunning || step.Version != command.ExpectedVersion || step.ModelRunID != command.ModelRunID {
			return conflict("synthesis model failure cannot replace this step")
		}
		next := step
		next.Status = nextStatus
		next.ErrorCode = command.ErrorCode
		next.Retryable = command.Retryable
		next.Version++
		next.UpdatedAt = canonical(command.CompletedAt)
		next.CompletedAt = timePointer(command.CompletedAt)
		if err := next.Validate(); err != nil {
			return err
		}
		model, err := store.dependencies.ModelRuns.GetModelRunRecordScoped(ctx, scope, step.WorkspaceID, step.ModelRunID, true)
		if err != nil {
			return err
		}
		if !sameModelRunBinding(model.Run, step) || !validModelRuntime(model.Run, step.Stage) || model.Run.Status != agentdomain.ModelRunRunning || model.Run.Version != command.ExpectedModelRunVersion {
			return invalid("synthesis model failure binding is invalid")
		}
		terminal := model.Run
		terminal.Status = command.ModelRunStatus
		terminal.FinalResultType = ""
		terminal.FinalErrorCode = command.ErrorCode
		terminal.Version++
		terminal.UpdatedAt = next.UpdatedAt
		terminal.CompletedAt = next.CompletedAt
		if _, _, err := store.dependencies.ModelRuns.FinalizeModelRunScoped(ctx, scope, agentapp.FinalizeModelRunCommand{ExpectedVersion: command.ExpectedModelRunVersion, Run: terminal}); err != nil {
			return err
		}
		updated := tx.Model(&modelStepModel{}).Where("workspace_id=? AND id=? AND version=? AND status=?", string(step.WorkspaceID), string(step.ID), step.Version, string(organizingapp.SynthesisModelStepRunning)).
			Updates(map[string]any{"status": string(nextStatus), "error_code": command.ErrorCode, "retryable": command.Retryable, "version": next.Version, "updated_at": next.UpdatedAt, "completed_at": next.CompletedAt})
		if updated.Error != nil {
			return classify(ctx, updated.Error)
		}
		if updated.RowsAffected != 1 {
			return conflict("synthesis model failure CAS failed")
		}
		return nil
	})
}

func (store *Store) lockModelStep(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, workspaceID, stepID foundation.ID) (organizingapp.SynthesisModelStepRecord, error) {
	// Read identity without a row lock, then use the global Workflow-first order.
	row, err := loadStep(tx, workspaceID, stepID, false)
	if err != nil {
		return organizingapp.SynthesisModelStepRecord{}, err
	}
	step, err := row.projection()
	if err != nil {
		return organizingapp.SynthesisModelStepRecord{}, err
	}
	if _, _, err := store.bindLive(ctx, scope, step.ProcessingID, stepExecution(step)); err != nil {
		return organizingapp.SynthesisModelStepRecord{}, err
	}
	row, err = loadStep(tx, workspaceID, stepID, true)
	if err != nil {
		return organizingapp.SynthesisModelStepRecord{}, err
	}
	return row.projection()
}

func loadStep(tx *gorm.DB, workspaceID, stepID foundation.ID, lock bool) (modelStepModel, error) {
	query := tx.Where("workspace_id=? AND id=?", string(workspaceID), string(stepID))
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row modelStepModel
	if err := query.Take(&row).Error; err != nil {
		return row, classify(tx.Statement.Context, err)
	}
	return row, nil
}

func synthesisStageKind(stage organizingapp.SynthesisModelStage) string {
	switch stage {
	case organizingapp.SynthesisModelGenerate:
		return organizingworkflow.SynthesisGenerateNodeKind
	case organizingapp.SynthesisModelValidate:
		return organizingworkflow.SynthesisValidateNodeKind
	}
	return ""
}
func synthesisResultType(stage organizingapp.SynthesisModelStage) string {
	switch stage {
	case organizingapp.SynthesisModelGenerate:
		return "synthesis_delta"
	case organizingapp.SynthesisModelValidate:
		return "synthesis_semantic_review"
	}
	return ""
}
func stepExecution(step organizingapp.SynthesisModelStepRecord) workflowapp.ExecutionContext {
	return workflowapp.ExecutionContext{WorkspaceID: step.WorkspaceID, RunID: step.WorkflowRunID, NodeRunID: step.NodeRunID, NodeAttemptID: step.NodeAttemptID, NodeKind: synthesisStageKind(step.Stage)}
}
func sameModelStepRequest(a, b organizingapp.SynthesisModelStepRecord) bool {
	return a.WorkspaceID == b.WorkspaceID && a.ProcessingID == b.ProcessingID && a.WorkflowRunID == b.WorkflowRunID && a.NodeRunID == b.NodeRunID && a.NodeAttemptID == b.NodeAttemptID && a.Stage == b.Stage && a.RequestHash == b.RequestHash && a.InputRequestHash == b.InputRequestHash && a.GenerationOutputHash == b.GenerationOutputHash && reflect.DeepEqual(a.ModelSettingsRevision, b.ModelSettingsRevision)
}
func sameStepCompletion(step organizingapp.SynthesisModelStepRecord, command organizingapp.CompleteSynthesisModelStepCommand) bool {
	return step.ModelRunID == command.ModelRunID && step.OutputHash == command.OutputHash && bytes.Equal(step.Output, command.Output) && reflect.DeepEqual(step.Generation, command.Generation) && reflect.DeepEqual(step.Semantic, command.Semantic) && synthesisResultType(step.Stage) == command.ResultType
}
func sameModelRunBinding(run agentdomain.ModelRun, step organizingapp.SynthesisModelStepRecord) bool {
	return run.WorkspaceID == step.WorkspaceID && run.WorkflowRunID == step.WorkflowRunID && run.NodeRunID == step.NodeRunID && run.NodeAttemptID == step.NodeAttemptID && reflect.DeepEqual(run.ModelSettingsRevision, step.ModelSettingsRevision)
}
func validModelRuntime(run agentdomain.ModelRun, stage organizingapp.SynthesisModelStage) bool {
	prompt, schema := "", ""
	switch stage {
	case organizingapp.SynthesisModelGenerate:
		prompt, schema = organizingapp.SynthesisDeltaPromptID, "agent.synthesis-delta"
	case organizingapp.SynthesisModelValidate:
		prompt, schema = organizingapp.SynthesisSemanticPromptID, "agent.synthesis-semantic-review"
	default:
		return false
	}
	return run.Prompt == (agentdomain.PromptRef{ID: prompt, Version: organizingapp.SynthesisRuntimeVersion}) && run.Schema == (agentdomain.SchemaRef{ID: schema, Version: organizingapp.SynthesisRuntimeVersion}) && run.ReducedSchema == run.Schema && !run.Retrieval.IsBound() && !run.MemoryContext.IsBound()
}

func validateModelProof(model agentapp.ModelRunRecord, step organizingapp.SynthesisModelStepRecord, outputHash string, outputBytes int64) error {
	if agentdomain.ValidateModelRun(model.Run) != nil || model.Run.ID != step.ModelRunID || !sameModelRunBinding(model.Run, step) || !validModelRuntime(model.Run, step.Stage) || len(model.Calls) < 1 || len(model.Calls) > agentapp.StructuredCallLimit {
		return invalid("synthesis model result has no complete recorded invocation")
	}
	phases := []agentdomain.ModelCallPhase{agentdomain.ModelCallInitial, agentdomain.ModelCallRepair, agentdomain.ModelCallReduced}
	for index, call := range model.Calls {
		if agentdomain.ValidateModelCall(call) != nil || call.ModelRunID != model.Run.ID || call.CallNo != index+1 || call.Phase != phases[index] || call.Status != agentdomain.ModelCallSucceeded || call.Model != model.Run.Model || call.Profile != model.Run.Profile || call.Prompt != model.Run.Prompt || call.Schema != model.Run.Schema {
			return invalid("synthesis model call history differs from the frozen runtime")
		}
	}
	last := model.Calls[len(model.Calls)-1]
	if last.ResponseHash != outputHash || last.ResponseBytes != outputBytes {
		return invalid("synthesis accepted output is not a recorded successful response")
	}
	return nil
}

// VerifyValidatedSynthesisGenerationScoped is called before the candidate owner
// takes Note locks. It fences the live apply attempt and both independent model
// results, so passing a fabricated boolean or a stale Worker cannot publish.
func (store *Store) VerifyValidatedSynthesisGenerationScoped(ctx context.Context, scope foundation.TransactionScope, input organizingapp.SynthesisGenerationInput, generation organizingapp.SynthesisGenerationResult) error {
	if input.Validate() != nil || generation.Validate(input) != nil {
		return invalid("synthesis application input is invalid")
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return err
	}
	row, err := loadExecution(tx, input.SourceEvent.Source.WorkspaceID, input.ProcessingID, input.WorkflowRunID, false)
	if err != nil {
		return err
	}
	if row.ApplyRecovery || row.ApplyNodeRunID == nil || row.ApplyNodeAttemptID == nil {
		return invalid("synthesis generation has no live application reservation")
	}
	execution := workflowapp.ExecutionContext{WorkspaceID: input.SourceEvent.Source.WorkspaceID, RunID: input.WorkflowRunID, NodeRunID: idValue(row.ApplyNodeRunID), NodeAttemptID: idValue(row.ApplyNodeAttemptID), NodeKind: organizingworkflow.SynthesisApplyNodeKind}
	_, row, err = store.bindLive(ctx, scope, input.ProcessingID, execution)
	if err != nil {
		return err
	}
	frozen, err := row.frozen()
	if err != nil {
		return err
	}
	if frozen == nil || !organizingworkflow.EqualSynthesisFrozenGeneration(*frozen, input) {
		return invalid("synthesis application changed the frozen input")
	}
	var rows []modelStepModel
	if err := tx.Where("workspace_id=? AND workflow_run_id=? AND status=?", string(execution.WorkspaceID), string(execution.RunID), string(organizingapp.SynthesisModelStepReady)).Order("stage").Clauses(clause.Locking{Strength: "UPDATE"}).Find(&rows).Error; err != nil {
		return classify(ctx, err)
	}
	if len(rows) != 2 {
		return invalid("synthesis application requires separate generation and semantic results")
	}
	var generated, reviewed organizingapp.SynthesisModelStepRecord
	for _, row := range rows {
		step, err := row.projection()
		if err != nil {
			return err
		}
		if step.Stage == organizingapp.SynthesisModelGenerate {
			generated = step
		} else {
			reviewed = step
		}
	}
	if generated.Generation == nil || !reflect.DeepEqual(*generated.Generation, generation) || generated.NodeRunID != input.NodeRunID || generated.NodeAttemptID != input.NodeAttemptID ||
		reviewed.Semantic == nil || !reviewed.Semantic.Accepted || reviewed.Semantic.GenerationModelRunID != generation.ModelRunID || reviewed.Semantic.GenerationOutputHash != generation.OutputHash ||
		generated.InputRequestHash != input.RequestHash || reviewed.InputRequestHash != input.RequestHash || generated.NodeAttemptID == reviewed.NodeAttemptID {
		return invalid("synthesis semantic review is not bound to this exact generation")
	}
	for _, step := range []organizingapp.SynthesisModelStepRecord{generated, reviewed} {
		model, err := store.dependencies.ModelRuns.GetModelRunRecordScoped(ctx, scope, step.WorkspaceID, step.ModelRunID, true)
		if err != nil {
			return err
		}
		if model.Run.Status != agentdomain.ModelRunSucceeded || model.Run.FinalResultType != synthesisResultType(step.Stage) {
			return invalid("synthesis model result is not successfully terminal")
		}
		if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output))); err != nil {
			return err
		}
	}
	return nil
}

var _ organizingapp.SynthesisModelStore = (*Store)(nil)
var _ organizingapp.SynthesisValidatedGenerationFence = (*Store)(nil)
