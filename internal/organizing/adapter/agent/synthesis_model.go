package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const synthesisFinalizationTimeout = 5 * time.Second

// SynthesisModel runs generation and review in independent recorded attempts.
// PostgreSQL/Workflow own durable progress; Eino owns only the bounded in-process
// StructuredRunner phase schedule.
type SynthesisModel struct{ dependencies SynthesisModelDependencies }

func NewSynthesisModel(dependencies SynthesisModelDependencies) (*SynthesisModel, error) {
	if synthesisNil(dependencies.Model) || synthesisNil(dependencies.ModelRuns) || synthesisNil(dependencies.Store) ||
		synthesisNil(dependencies.IDs) || synthesisNil(dependencies.Clock) || dependencies.Catalog == nil || dependencies.ProfileRef.Validate() != nil {
		return nil, synthesisUnavailable()
	}
	if dependencies.Budget == (agentapp.RunBudget{}) {
		dependencies.Budget = agentapp.DefaultRunBudget()
	}
	if _, err := agentapp.NewStructuredRunnerWithScheduler(dependencies.Model, dependencies.Catalog, dependencies.Budget, dependencies.Scheduler); err != nil {
		return nil, err
	}
	for _, stage := range []organizingapp.SynthesisModelStage{organizingapp.SynthesisModelGenerate, organizingapp.SynthesisModelValidate} {
		if _, err := dependencies.Catalog.Snapshot(synthesisPrompt(stage), synthesisSchema(stage), synthesisSchema(stage), dependencies.ProfileRef); err != nil {
			return nil, err
		}
	}
	return &SynthesisModel{dependencies: dependencies}, nil
}

func NewUnavailableSynthesisModel() *SynthesisModel { return &SynthesisModel{} }

func (model *SynthesisModel) GenerateSynthesisForExecution(ctx context.Context, execution workflowapp.ExecutionContext, input organizingapp.SynthesisGenerationInput) (organizingapp.SynthesisGenerationResult, error) {
	return model.GenerateSynthesis(WithSynthesisExecution(ctx, execution), input)
}

func (model *SynthesisModel) ValidateSynthesisSemanticsForExecution(ctx context.Context, execution workflowapp.ExecutionContext, input organizingapp.SynthesisGenerationInput, generated organizingapp.SynthesisGenerationResult) (organizingapp.SynthesisSemanticReceipt, error) {
	return model.ValidateSynthesisSemanticsWithReceipt(WithSynthesisExecution(ctx, execution), input, generated)
}

func (model *SynthesisModel) GenerateSynthesis(ctx context.Context, input organizingapp.SynthesisGenerationInput) (organizingapp.SynthesisGenerationResult, error) {
	execution, err := model.execution(ctx, input, organizingapp.SynthesisModelGenerate)
	if err != nil {
		return organizingapp.SynthesisGenerationResult{}, err
	}
	payload, err := encodeSynthesisInput(synthesisInput(input))
	if err != nil {
		return organizingapp.SynthesisGenerationResult{}, err
	}
	requestHash, err := synthesisModelRequestHash(organizingapp.SynthesisModelGenerate, execution.NodeRunID, input, nil, payload)
	if err != nil {
		return organizingapp.SynthesisGenerationResult{}, err
	}
	record, err := model.invoke(ctx, execution, input, organizingapp.SynthesisModelGenerate, requestHash, "", payload, func(raw []byte, modelRunID foundation.ID) (*organizingapp.SynthesisGenerationResult, *organizingapp.SynthesisSemanticReceipt, error) {
		result, err := bindSynthesisOutput(raw, input, modelRunID, model.dependencies.IDs.New)
		if err == nil {
			// A generated delta must fit the independent review request too. This
			// structural capacity check does not substitute for model review.
			plan, planErr := synthesisSemanticPlan(input, result)
			if planErr != nil {
				err = planErr
			} else {
				_, err = encodeSynthesisInput(plan)
			}
		}
		return &result, nil, err
	})
	if err != nil {
		return organizingapp.SynthesisGenerationResult{}, err
	}
	if record.Generation == nil || validateSynthesisBoundOutput(record.Output, input, *record.Generation) != nil {
		return organizingapp.SynthesisGenerationResult{}, synthesisReplayUnsafe()
	}
	return *record.Generation, nil
}

func (model *SynthesisModel) ValidateSynthesisSemantics(ctx context.Context, input organizingapp.SynthesisGenerationInput, generated organizingapp.SynthesisGenerationResult) error {
	_, err := model.ValidateSynthesisSemanticsWithReceipt(ctx, input, generated)
	return err
}

func (model *SynthesisModel) ValidateSynthesisSemanticsWithReceipt(ctx context.Context, input organizingapp.SynthesisGenerationInput, generated organizingapp.SynthesisGenerationResult) (organizingapp.SynthesisSemanticReceipt, error) {
	execution, err := model.execution(ctx, input, organizingapp.SynthesisModelValidate)
	if err != nil {
		return organizingapp.SynthesisSemanticReceipt{}, err
	}
	if err := model.verifyGeneratedResult(ctx, input, generated); err != nil {
		return organizingapp.SynthesisSemanticReceipt{}, err
	}
	plan, err := synthesisSemanticPlan(input, generated)
	if err != nil {
		return organizingapp.SynthesisSemanticReceipt{}, err
	}
	payload, err := encodeSynthesisInput(plan)
	if err != nil {
		return organizingapp.SynthesisSemanticReceipt{}, err
	}
	requestHash, err := synthesisModelRequestHash(organizingapp.SynthesisModelValidate, execution.NodeRunID, input, &generated, payload)
	if err != nil {
		return organizingapp.SynthesisSemanticReceipt{}, err
	}
	record, err := model.invoke(ctx, execution, input, organizingapp.SynthesisModelValidate, requestHash, generated.OutputHash, payload, func(raw []byte, modelRunID foundation.ID) (*organizingapp.SynthesisGenerationResult, *organizingapp.SynthesisSemanticReceipt, error) {
		receipt, err := bindSynthesisSemanticOutput(raw, plan, input, generated, modelRunID)
		return nil, &receipt, err
	})
	if err != nil {
		return organizingapp.SynthesisSemanticReceipt{}, err
	}
	bound, err := bindSynthesisSemanticOutput(record.Output, plan, input, generated, record.ModelRunID)
	if err != nil || record.Semantic == nil || bound != *record.Semantic || bound.Validate() != nil {
		return organizingapp.SynthesisSemanticReceipt{}, synthesisReplayUnsafe()
	}
	if !bound.Accepted {
		return bound, synthesisError(foundation.ErrorNonRetryableFailure, organizingapp.ErrorCodeSynthesisSemanticRejected, false, "synthesis semantic review rejected unsupported content")
	}
	return bound, nil
}

func (model *SynthesisModel) execution(ctx context.Context, input organizingapp.SynthesisGenerationInput, stage organizingapp.SynthesisModelStage) (workflowapp.ExecutionContext, error) {
	if model == nil || synthesisNil(model.dependencies.Model) || synthesisNil(model.dependencies.Store) {
		return workflowapp.ExecutionContext{}, synthesisUnavailable()
	}
	if ctx == nil {
		return workflowapp.ExecutionContext{}, synthesisContextInvalid()
	}
	if err := synthesisContextError(ctx, nil); err != nil {
		return workflowapp.ExecutionContext{}, err
	}
	if err := input.Validate(); err != nil {
		return workflowapp.ExecutionContext{}, err
	}
	execution, ok := ctx.Value(synthesisExecutionKey{}).(workflowapp.ExecutionContext)
	if !ok || execution.WorkspaceID != input.SourceEvent.Source.WorkspaceID || execution.RunID != input.WorkflowRunID ||
		!synthesisValidID(execution.NodeRunID) || !synthesisValidID(execution.NodeAttemptID) || execution.NodeRunID == execution.NodeAttemptID ||
		execution.ModelSettingsRevision != nil && *execution.ModelSettingsRevision < 0 {
		return execution, synthesisContextInvalid()
	}
	if stage == organizingapp.SynthesisModelGenerate {
		if execution.NodeRunID != input.NodeRunID || execution.NodeAttemptID != input.NodeAttemptID {
			return execution, synthesisContextInvalid()
		}
	} else if execution.NodeRunID == input.NodeRunID || execution.NodeAttemptID == input.NodeAttemptID {
		return execution, synthesisContextInvalid()
	}
	return execution, nil
}

type synthesisOutputBinder func([]byte, foundation.ID) (*organizingapp.SynthesisGenerationResult, *organizingapp.SynthesisSemanticReceipt, error)

func (model *SynthesisModel) invoke(ctx context.Context, execution workflowapp.ExecutionContext, input organizingapp.SynthesisGenerationInput, stage organizingapp.SynthesisModelStage, requestHash, generationHash string, payload []byte, bind synthesisOutputBinder) (organizingapp.SynthesisModelStepRecord, error) {
	if ready, found, err := model.dependencies.Store.LookupReady(ctx, execution.WorkspaceID, execution.NodeRunID, stage, requestHash); err != nil {
		return organizingapp.SynthesisModelStepRecord{}, synthesisContextError(ctx, err)
	} else if found {
		return ready, model.verifyReady(ctx, ready, input, stage, execution.NodeRunID, requestHash, generationHash)
	}
	runtime, err := model.dependencies.Catalog.Snapshot(synthesisPrompt(stage), synthesisSchema(stage), synthesisSchema(stage), model.dependencies.ProfileRef)
	if err != nil {
		return organizingapp.SynthesisModelStepRecord{}, err
	}
	id, err := model.dependencies.IDs.New()
	if err != nil {
		return organizingapp.SynthesisModelStepRecord{}, err
	}
	now := model.now()
	requested := organizingapp.SynthesisModelStepRecord{
		ID: id, WorkspaceID: execution.WorkspaceID, ProcessingID: input.ProcessingID, WorkflowRunID: execution.RunID,
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID, Stage: stage,
		RequestHash: requestHash, InputRequestHash: input.RequestHash, GenerationOutputHash: generationHash,
		ModelSettingsRevision: cloneSynthesisRevisionNumber(execution.ModelSettingsRevision),
		Status:                organizingapp.SynthesisModelStepRunning, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := requested.Validate(); err != nil {
		return requested, err
	}
	prepared, _, err := model.dependencies.Store.Prepare(ctx, organizingapp.PrepareSynthesisModelStepCommand{Record: requested})
	if err != nil {
		return prepared, synthesisContextError(ctx, err)
	}
	if prepared.Validate() != nil {
		return prepared, synthesisReplayUnsafe()
	}
	if prepared.Status == organizingapp.SynthesisModelStepReady {
		return prepared, model.verifyReady(ctx, prepared, input, stage, execution.NodeRunID, requestHash, generationHash)
	}
	if !sameSynthesisStepRequest(prepared, requested) {
		return prepared, synthesisReplayUnsafe()
	}
	if prepared.Status == organizingapp.SynthesisModelStepRecoveryRequired {
		return prepared, synthesisReplayUnsafe()
	}
	if prepared.Status == organizingapp.SynthesisModelStepFailed {
		return prepared, synthesisError(foundation.ErrorVersionConflict, prepared.ErrorCode, prepared.Retryable, "synthesis model attempt is already terminal")
	}
	run, prepared, err := model.ensureRun(ctx, prepared, runtime)
	if err != nil {
		return prepared, err
	}
	recorded, err := agentapp.NewRecordingChatModel(agentapp.RecordingChatModelDependencies{
		Model: model.dependencies.Model, Repository: model.dependencies.ModelRuns, WorkspaceID: prepared.WorkspaceID,
		ModelRunID: run.ID, IDs: model.dependencies.IDs, Clock: model.dependencies.Clock,
	})
	if err != nil {
		return prepared, model.fail(ctx, prepared, run, err)
	}
	runner, err := agentapp.NewStructuredRunnerWithScheduler(recorded, model.dependencies.Catalog, model.dependencies.Budget, model.dependencies.Scheduler)
	if err != nil {
		return prepared, model.fail(ctx, prepared, run, err)
	}
	result, err := runner.Run(ctx, agentapp.StructuredRunRequest{ProfileRef: run.Profile, PromptRef: run.Prompt, SchemaRef: run.Schema, ReducedSchemaRef: run.ReducedSchema, Input: payload})
	if err != nil {
		return prepared, model.fail(ctx, prepared, run, synthesisContextError(ctx, err))
	}
	generation, semantic, err := bind(result.Output, run.ID)
	if err != nil {
		return prepared, model.fail(ctx, prepared, run, err)
	}
	completedAt := model.now()
	if completedAt.Before(run.CreatedAt) {
		completedAt = run.CreatedAt
	}
	command := organizingapp.CompleteSynthesisModelStepCommand{
		WorkspaceID: prepared.WorkspaceID, StepID: prepared.ID, ExpectedVersion: prepared.Version,
		ModelRunID: run.ID, ExpectedModelRunVersion: run.Version, ResultType: synthesisResultType(stage),
		Output: append(json.RawMessage(nil), result.Output...), OutputHash: synthesisHash(result.Output), Generation: generation, Semantic: semantic, CompletedAt: completedAt,
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), synthesisFinalizationTimeout)
	defer cancel()
	completed, _, completeErr := model.dependencies.Store.Complete(finalCtx, command)
	if completeErr != nil {
		// A lost commit response is not a rollback. Only a complete, exact durable
		// receipt can resolve it; no path repeats the Provider call here.
		recovered, found, lookupErr := model.dependencies.Store.LookupReady(finalCtx, prepared.WorkspaceID, prepared.NodeRunID, stage, requestHash)
		if lookupErr != nil || !found {
			return prepared, synthesisFinalizationUnknown(errors.Join(completeErr, lookupErr))
		}
		completed = recovered
	}
	if completed.OutputHash != command.OutputHash || !reflect.DeepEqual(completed.Generation, generation) || !reflect.DeepEqual(completed.Semantic, semantic) {
		return completed, synthesisFinalizationUnknown(completeErr)
	}
	if err := model.verifyReady(finalCtx, completed, input, stage, execution.NodeRunID, requestHash, generationHash); err != nil {
		return completed, synthesisFinalizationUnknown(err)
	}
	return completed, nil
}

func (model *SynthesisModel) ensureRun(ctx context.Context, prepared organizingapp.SynthesisModelStepRecord, runtime agentapp.RuntimeSnapshot) (agentdomain.ModelRun, organizingapp.SynthesisModelStepRecord, error) {
	id, err := model.dependencies.IDs.New()
	if err != nil {
		return agentdomain.ModelRun{}, prepared, err
	}
	now := model.now()
	requested := agentdomain.ModelRun{
		ID: id, WorkspaceID: prepared.WorkspaceID, WorkflowRunID: prepared.WorkflowRunID, NodeRunID: prepared.NodeRunID,
		NodeAttemptID: prepared.NodeAttemptID, ModelSettingsRevision: cloneSynthesisRevisionNumber(prepared.ModelSettingsRevision),
		Model: runtime.Profile.Model, Profile: runtime.Profile.Ref, Prompt: runtime.Prompt.Ref, Schema: runtime.Schema.Ref, ReducedSchema: runtime.ReducedSchema.Ref,
		Status: agentdomain.ModelRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	created, replayed, err := model.dependencies.ModelRuns.CreateModelRun(ctx, requested)
	if err != nil {
		return created, prepared, synthesisContextError(ctx, err)
	}
	if agentdomain.ValidateModelRun(created) != nil || !sameSynthesisModelRun(created, requested) || created.Status != agentdomain.ModelRunRunning || created.Version != 1 ||
		prepared.ModelRunID != "" && prepared.ModelRunID != created.ID {
		return created, prepared, synthesisReplayUnsafe()
	}
	if replayed {
		stored, err := model.dependencies.ModelRuns.GetModelRun(ctx, prepared.WorkspaceID, created.ID)
		if err != nil {
			return created, prepared, synthesisContextError(ctx, err)
		}
		if !sameSynthesisModelRun(stored.Run, requested) || stored.Run.Status != agentdomain.ModelRunRunning || stored.Run.Version != 1 || len(stored.Calls) != 0 {
			return created, prepared, synthesisReplayUnsafe()
		}
	}
	if prepared.ModelRunID == "" {
		bound, err := model.dependencies.Store.BindModelRun(ctx, organizingapp.BindSynthesisModelRunCommand{WorkspaceID: prepared.WorkspaceID, StepID: prepared.ID, ExpectedVersion: prepared.Version, ModelRunID: created.ID})
		if err != nil {
			return created, prepared, synthesisContextError(ctx, err)
		}
		if bound.Validate() != nil || !sameSynthesisStepRequest(bound, prepared) || bound.ID != prepared.ID || bound.ModelRunID != created.ID || bound.Status != organizingapp.SynthesisModelStepRunning {
			return created, bound, synthesisReplayUnsafe()
		}
		prepared = bound
	}
	return created, prepared, nil
}

func (model *SynthesisModel) verifyReady(ctx context.Context, record organizingapp.SynthesisModelStepRecord, input organizingapp.SynthesisGenerationInput, stage organizingapp.SynthesisModelStage, nodeID foundation.ID, requestHash, generationHash string) error {
	if record.Validate() != nil || record.Status != organizingapp.SynthesisModelStepReady || record.WorkspaceID != input.SourceEvent.Source.WorkspaceID ||
		record.ProcessingID != input.ProcessingID || record.WorkflowRunID != input.WorkflowRunID || record.NodeRunID != nodeID || record.Stage != stage ||
		record.RequestHash != requestHash || record.InputRequestHash != input.RequestHash || record.GenerationOutputHash != generationHash {
		return synthesisReplayUnsafe()
	}
	stored, err := model.dependencies.ModelRuns.GetModelRun(ctx, record.WorkspaceID, record.ModelRunID)
	if err != nil {
		return synthesisContextError(ctx, err)
	}
	run := stored.Run
	if agentdomain.ValidateModelRun(run) != nil || run.ID != record.ModelRunID || run.WorkspaceID != record.WorkspaceID || run.WorkflowRunID != record.WorkflowRunID ||
		run.NodeRunID != record.NodeRunID || run.NodeAttemptID != record.NodeAttemptID || !sameSynthesisRevisionNumber(run.ModelSettingsRevision, record.ModelSettingsRevision) ||
		run.Status != agentdomain.ModelRunSucceeded || run.FinalResultType != synthesisResultType(stage) || run.Prompt != synthesisPrompt(stage) ||
		run.Schema != synthesisSchema(stage) || run.ReducedSchema != synthesisSchema(stage) || run.Retrieval.IsBound() || run.MemoryContext.IsBound() ||
		len(stored.Calls) < 1 || len(stored.Calls) > agentapp.StructuredCallLimit {
		return synthesisReplayUnsafe()
	}
	phases := []agentdomain.ModelCallPhase{agentdomain.ModelCallInitial, agentdomain.ModelCallRepair, agentdomain.ModelCallReduced}
	for index, call := range stored.Calls {
		if agentdomain.ValidateModelCall(call) != nil || call.ModelRunID != run.ID || call.CallNo != index+1 || call.Phase != phases[index] ||
			call.Status != agentdomain.ModelCallSucceeded || call.Model != run.Model || call.Profile != run.Profile || call.Prompt != run.Prompt || call.Schema != run.Schema {
			return synthesisReplayUnsafe()
		}
	}
	last := stored.Calls[len(stored.Calls)-1]
	if last.ResponseHash != record.OutputHash || last.ResponseBytes != int64(len(record.Output)) {
		return synthesisReplayUnsafe()
	}
	return nil
}

func (model *SynthesisModel) verifyGeneratedResult(ctx context.Context, input organizingapp.SynthesisGenerationInput, generated organizingapp.SynthesisGenerationResult) error {
	if err := generated.Validate(input); err != nil {
		return err
	}
	payload, err := encodeSynthesisInput(synthesisInput(input))
	if err != nil {
		return err
	}
	requestHash, err := synthesisModelRequestHash(organizingapp.SynthesisModelGenerate, input.NodeRunID, input, nil, payload)
	if err != nil {
		return err
	}
	record, found, err := model.dependencies.Store.LookupReady(ctx, input.SourceEvent.Source.WorkspaceID, input.NodeRunID, organizingapp.SynthesisModelGenerate, requestHash)
	if err != nil {
		return synthesisContextError(ctx, err)
	}
	if !found || record.Generation == nil || !reflect.DeepEqual(*record.Generation, generated) || validateSynthesisBoundOutput(record.Output, input, generated) != nil {
		return synthesisReplayUnsafe()
	}
	return model.verifyReady(ctx, record, input, organizingapp.SynthesisModelGenerate, input.NodeRunID, requestHash, "")
}

func (model *SynthesisModel) fail(ctx context.Context, record organizingapp.SynthesisModelStepRecord, run agentdomain.ModelRun, cause error) error {
	status, code, retryable := agentdomain.ModelRunFailed, "SYNTHESIS_MODEL_FAILED", false
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		if (domain.SynthesisFailure{Code: classified.Code}).Validate() == nil {
			code = classified.Code
		}
		retryable = classified.Retryable
		if classified.Kind == foundation.ErrorManualRecoveryRequired || classified.Code == agentapp.ErrorCodeModelCallPersistenceUnknown || classified.Code == agentapp.ErrorCodeModelCallReplayUnsafe {
			status, retryable = agentdomain.ModelRunUnknown, false
		}
	}
	completedAt := model.now()
	if completedAt.Before(run.CreatedAt) {
		completedAt = run.CreatedAt
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), synthesisFinalizationTimeout)
	defer cancel()
	if err := model.dependencies.Store.Fail(finalCtx, organizingapp.FailSynthesisModelStepCommand{
		WorkspaceID: record.WorkspaceID, StepID: record.ID, ExpectedVersion: record.Version, ModelRunID: run.ID, ExpectedModelRunVersion: run.Version,
		ModelRunStatus: status, ErrorCode: code, Retryable: retryable, CompletedAt: completedAt,
	}); err != nil {
		return synthesisFinalizationUnknown(errors.Join(cause, err))
	}
	return cause
}

// The request key includes authoritative bindings and the exact labelled
// Provider input digest. Attempt IDs and live model settings are excluded so a
// READY node can replay its original, audited result after transport recovery.
func synthesisModelRequestHash(stage organizingapp.SynthesisModelStage, nodeID foundation.ID, input organizingapp.SynthesisGenerationInput, generated *organizingapp.SynthesisGenerationResult, payload []byte) (string, error) {
	type candidateBinding struct {
		Note         domain.SynthesisNote `json:"note"`
		RevisionID   foundation.ID        `json:"revision_id"`
		RevisionHash string               `json:"revision_hash"`
	}
	candidates := make([]candidateBinding, len(input.Notes))
	for index, note := range input.Notes {
		candidates[index] = candidateBinding{Note: note.Note, RevisionID: note.Revision.ID, RevisionHash: note.Revision.Hash}
	}
	sources := make([]domain.SynthesisSourceRef, len(input.Sources))
	for index, source := range input.Sources {
		sources[index] = source.Reference
	}
	value := struct {
		Stage             organizingapp.SynthesisModelStage        `json:"stage"`
		Version           string                                   `json:"version"`
		ProcessingID      foundation.ID                            `json:"processing_id"`
		SourceEvent       domain.SynthesisSourceReady              `json:"source_event"`
		RunID             foundation.ID                            `json:"run_id"`
		NodeID            foundation.ID                            `json:"node_id"`
		InputRequestHash  string                                   `json:"input_request_hash"`
		Candidates        []candidateBinding                       `json:"candidates"`
		Sources           []domain.SynthesisSourceRef              `json:"sources"`
		ProviderInputHash string                                   `json:"provider_input_hash"`
		Generated         *organizingapp.SynthesisGenerationResult `json:"generated"`
	}{stage, organizingapp.SynthesisRuntimeVersion, input.ProcessingID, input.SourceEvent, input.WorkflowRunID, nodeID, input.RequestHash, candidates, sources, synthesisHash(payload), generated}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", synthesisContextInvalid()
	}
	return synthesisHash(encoded), nil
}

func sameSynthesisStepRequest(left, right organizingapp.SynthesisModelStepRecord) bool {
	return left.WorkspaceID == right.WorkspaceID && left.ProcessingID == right.ProcessingID && left.WorkflowRunID == right.WorkflowRunID &&
		left.NodeRunID == right.NodeRunID && left.NodeAttemptID == right.NodeAttemptID && left.Stage == right.Stage && left.RequestHash == right.RequestHash &&
		left.InputRequestHash == right.InputRequestHash && left.GenerationOutputHash == right.GenerationOutputHash && sameSynthesisRevisionNumber(left.ModelSettingsRevision, right.ModelSettingsRevision)
}

func sameSynthesisModelRun(left, right agentdomain.ModelRun) bool {
	return left.WorkspaceID == right.WorkspaceID && left.WorkflowRunID == right.WorkflowRunID && left.NodeRunID == right.NodeRunID && left.NodeAttemptID == right.NodeAttemptID &&
		sameSynthesisRevisionNumber(left.ModelSettingsRevision, right.ModelSettingsRevision) && left.Model == right.Model && left.Profile == right.Profile && left.Prompt == right.Prompt &&
		left.Schema == right.Schema && left.ReducedSchema == right.ReducedSchema && left.Retrieval == right.Retrieval && left.MemoryContext == right.MemoryContext
}

func sameSynthesisRevisionNumber(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func synthesisResultType(stage organizingapp.SynthesisModelStage) string {
	if stage == organizingapp.SynthesisModelValidate {
		return agentdomain.ResultTypeSynthesisSemanticReview
	}
	return agentdomain.ResultTypeSynthesisDelta
}

func (model *SynthesisModel) now() time.Time {
	return model.dependencies.Clock.Now().UTC().Truncate(time.Microsecond)
}

func synthesisHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func synthesisValidID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && value == parsed
}

func synthesisNil(value any) bool {
	if value == nil {
		return true
	}
	switch reflected := reflect.ValueOf(value); reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func synthesisError(kind foundation.ErrorKind, code string, retryable bool, message string) error {
	return foundation.NewError(kind, code, retryable, errors.New(message))
}

func synthesisUnavailable() error {
	return synthesisError(foundation.ErrorDependencyUnavailable, organizingapp.ErrorCodeSynthesisCapabilityUnavailable, false, "synthesis requires a configured recorded chat runtime")
}

func synthesisContextInvalid() error {
	return synthesisError(foundation.ErrorConsistencyViolation, organizingapp.ErrorCodeSynthesisModelContextInvalid, false, "synthesis model execution binding is invalid")
}

func synthesisReplayUnsafe() error {
	return synthesisError(foundation.ErrorManualRecoveryRequired, organizingapp.ErrorCodeSynthesisModelReplayUnsafe, false, "synthesis model result requires explicit recovery")
}

func synthesisFinalizationUnknown(cause error) error {
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, organizingapp.ErrorCodeSynthesisModelFinalizationUnknown, false, cause)
}

func synthesisContextError(ctx context.Context, err error) error {
	var classified *foundation.Error
	known := errors.As(err, &classified)
	if ctx != nil && ctx.Err() != nil {
		cause := errors.Join(ctx.Err(), context.Cause(ctx), err)
		if known {
			return foundation.NewError(classified.Kind, classified.Code, classified.Retryable, cause)
		}
		kind, retryable := foundation.ErrorNonRetryableFailure, false
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind, retryable = foundation.ErrorRetryableFailure, true
		}
		return foundation.NewError(kind, organizingapp.ErrorCodeSynthesisModelContextInvalid, retryable, cause)
	}
	if err == nil || known {
		return err
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, "SYNTHESIS_MODEL_FAILED", false, err)
}

var _ organizingapp.SynthesisGenerator = (*SynthesisModel)(nil)
var _ organizingapp.SynthesisSemanticValidator = (*SynthesisModel)(nil)
var _ organizingapp.SynthesisSemanticReviewer = (*SynthesisModel)(nil)
