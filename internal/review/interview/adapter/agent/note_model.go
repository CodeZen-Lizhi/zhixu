// Package agent adapts frozen note interviews to the recorded Agent runtime.
package agent

import (
	"context"
	"errors"
	"reflect"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

type NoteInterviewModelDependencies struct {
	Model      agentapp.ChatModel
	Scheduler  agentapp.StructuredPhaseScheduler
	Catalog    *agentapp.RuntimeCatalog
	ModelRuns  agentapp.ModelRunRepository
	Store      interviewapp.NoteGenerationStore
	ProfileRef agentdomain.ModelProfileRef
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
	Budget     agentapp.RunBudget
}
type NoteInterviewModel struct {
	dependencies NoteInterviewModelDependencies
}

func NewNoteInterviewModel(dependencies NoteInterviewModelDependencies) (*NoteInterviewModel, error) {
	for _, port := range []any{dependencies.Model, dependencies.Scheduler, dependencies.Catalog, dependencies.ModelRuns, dependencies.Store, dependencies.IDs, dependencies.Clock} {
		if nilPort(port) {
			return nil, noteUnavailable()
		}
	}
	if dependencies.Budget == (agentapp.RunBudget{}) {
		dependencies.Budget = agentapp.DefaultRunBudget()
	}
	if _, err := agentapp.NewStructuredRunnerWithScheduler(dependencies.Model, dependencies.Catalog, dependencies.Budget, dependencies.Scheduler); err != nil {
		return nil, err
	}
	if _, err := dependencies.Catalog.Snapshot(interviewapp.NoteModelPromptRef(), interviewapp.NoteModelSchemaRef(), interviewapp.NoteModelSchemaRef(), dependencies.ProfileRef); err != nil {
		return nil, err
	}
	return &NoteInterviewModel{dependencies: dependencies}, nil
}

func NewUnavailableNoteInterviewModel() *NoteInterviewModel { return &NoteInterviewModel{} }

func (model *NoteInterviewModel) GenerateNoteInterview(ctx context.Context, execution workflowapp.ExecutionContext, preparation interviewapp.NotePreparation) (interviewapp.NotePreparation, error) {
	if model == nil || nilPort(model.dependencies.Model) {
		return preparation, noteUnavailable()
	}
	if ctx == nil || preparation.Validate() != nil || preparation.Status != interviewapp.NotePreparationGenerating || preparation.WorkspaceID != execution.WorkspaceID || preparation.WorkflowRunID != execution.RunID ||
		preparation.NodeRunID != execution.NodeRunID || preparation.NodeAttemptID != execution.NodeAttemptID || !reflect.DeepEqual(preparation.ModelSettingsRevision, execution.ModelSettingsRevision) {
		return preparation, domain.InvalidError(interviewapp.ErrorCodeNotePreparationInvalid, "note interview model execution is invalid")
	}
	payload, err := interviewapp.EncodeNotePlanInput(preparation)
	if err != nil {
		return preparation, err
	}
	runtime, err := model.dependencies.Catalog.Snapshot(interviewapp.NoteModelPromptRef(), interviewapp.NoteModelSchemaRef(), interviewapp.NoteModelSchemaRef(), model.dependencies.ProfileRef)
	if err != nil {
		return preparation, err
	}
	id, err := model.dependencies.IDs.New()
	if err != nil {
		return preparation, err
	}
	now := model.dependencies.Clock.Now().UTC().Truncate(time.Microsecond)
	requested := agentdomain.ModelRun{ID: id, WorkspaceID: preparation.WorkspaceID, WorkflowRunID: preparation.WorkflowRunID, NodeRunID: preparation.NodeRunID, NodeAttemptID: preparation.NodeAttemptID,
		ModelSettingsRevision: preparation.ModelSettingsRevision, Model: runtime.Profile.Model, Profile: runtime.Profile.Ref, Prompt: runtime.Prompt.Ref, Schema: runtime.Schema.Ref, ReducedSchema: runtime.ReducedSchema.Ref,
		Status: agentdomain.ModelRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now}
	run, replayed, err := model.dependencies.ModelRuns.CreateModelRun(ctx, requested)
	if err != nil {
		return preparation, err
	}
	if interviewapp.ValidateNoteModelBinding(preparation, run) != nil || run.Status != agentdomain.ModelRunRunning || run.Version != 1 ||
		run.Model != requested.Model || run.Profile != requested.Profile {
		return preparation, noteRecovery(nil)
	}
	if replayed {
		record, readErr := model.dependencies.ModelRuns.GetModelRun(ctx, preparation.WorkspaceID, run.ID)
		if readErr != nil {
			return preparation, noteRecovery(readErr)
		}
		if record.Run.Status != agentdomain.ModelRunRunning || record.Run.Version != 1 || len(record.Calls) != 0 {
			return preparation, noteRecovery(nil)
		}
	}
	preparation, err = model.dependencies.Store.BindNoteModelRun(ctx, preparation, run)
	if err != nil {
		return preparation, err
	}
	recorded, err := agentapp.NewRecordingChatModel(agentapp.RecordingChatModelDependencies{Model: model.dependencies.Model, Repository: model.dependencies.ModelRuns,
		WorkspaceID: preparation.WorkspaceID, ModelRunID: run.ID, IDs: model.dependencies.IDs, Clock: model.dependencies.Clock})
	if err != nil {
		return preparation, err
	}
	runner, err := agentapp.NewStructuredRunnerWithScheduler(recorded, model.dependencies.Catalog, model.dependencies.Budget, model.dependencies.Scheduler)
	if err != nil {
		return preparation, err
	}
	result, err := runner.Run(ctx, agentapp.StructuredRunRequest{ProfileRef: run.Profile, PromptRef: run.Prompt, SchemaRef: run.Schema, ReducedSchemaRef: run.ReducedSchema, Input: payload})
	if err != nil {
		return preparation, err
	}
	plan, err := interviewapp.DecodeNotePlan(result.Output, preparation)
	if err != nil {
		return preparation, err
	}
	now = model.dependencies.Clock.Now().UTC().Truncate(time.Microsecond)
	start, err := interviewapp.BuildNoteInterviewStart(preparation, plan, now)
	if err != nil {
		return preparation, err
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	completed, err := model.dependencies.Store.CompleteNoteGeneration(finalCtx, interviewapp.NoteGenerationCompletion{
		Preparation: preparation, Execution: execution, ModelRun: run, Output: result.Output, Plan: plan, Start: start, At: now})
	if err != nil {
		// Commit response loss is resolved only by the exact stored session and
		// accepted response bytes; the Provider is never invoked again here.
		recovered, lookupErr := model.dependencies.Store.GetNotePreparation(finalCtx, preparation.WorkspaceID, preparation.ID)
		if lookupErr != nil || recovered.Status != interviewapp.NotePreparationReady || recovered.SessionID == nil || *recovered.SessionID != start.Session.ID ||
			recovered.ModelRunID != run.ID || string(recovered.ModelOutput) != string(result.Output) {
			return preparation, noteRecovery(errors.Join(err, lookupErr))
		}
		completed = recovered
	}
	if completed.Validate() != nil || completed.Status != interviewapp.NotePreparationReady || completed.SessionID == nil || *completed.SessionID != start.Session.ID || completed.ModelRunID != run.ID || string(completed.ModelOutput) != string(result.Output) {
		return preparation, noteRecovery(nil)
	}
	return completed, nil
}

func nilPort(port any) bool {
	if port == nil {
		return true
	}
	v := reflect.ValueOf(port)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}
func noteUnavailable() error {
	return domain.UnavailableError(interviewapp.ErrorCodeNotePlanUnavailable, "note interview requires a configured model runtime")
}
func noteRecovery(cause error) error {
	if cause == nil {
		cause = errors.New("note interview model result requires explicit recovery")
	}
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, interviewapp.ErrorCodeNoteRecoveryRequired, false, cause)
}

var _ interviewapp.NoteInterviewGenerator = (*NoteInterviewModel)(nil)
