package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

type SynthesisExecutorDependencies struct {
	Runs       WorkflowRunReader
	Store      SynthesisProcessingStore
	Candidates SynthesisCandidateOwner
	Sources    organizingapp.SynthesisSourceReader
	Model      SynthesisExecutionModel
	Clock      foundation.Clock
}

type SynthesisExecutor struct{ dependencies SynthesisExecutorDependencies }

func NewSynthesisExecutor(dependencies SynthesisExecutorDependencies) (*SynthesisExecutor, error) {
	if nilScopedDependency(dependencies.Runs) || nilScopedDependency(dependencies.Store) ||
		nilScopedDependency(dependencies.Candidates) || nilScopedDependency(dependencies.Sources) ||
		nilScopedDependency(dependencies.Model) || nilScopedDependency(dependencies.Clock) {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisExecutionUnavailable, false, "synthesis executor dependencies are incomplete")
	}
	return &SynthesisExecutor{dependencies: dependencies}, nil
}

// Execute resolves every step from durable owner facts. A later transport
// attempt returns the same accepted result and never creates another model call.
func (executor *SynthesisExecutor) Execute(ctx context.Context, execution workflowapp.ExecutionContext) (workflowapp.ExecutionResult, error) {
	if executor == nil || ctx == nil {
		return workflowapp.ExecutionResult{}, synthesisInvalid("synthesis execution context is missing")
	}
	loaded, err := executor.loadExecution(ctx, execution)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if loaded.ApplyRecovery {
		if execution.NodeKind == SynthesisApplyNodeKind {
			if err := executor.dependencies.Store.PrepareSynthesisApplication(ctx, execution, loaded.Processing.ID); err != nil {
				return workflowapp.ExecutionResult{}, err
			}
			result, found, err := executor.dependencies.Candidates.RecoverAppliedGeneration(ctx, execution.WorkspaceID, loaded.Processing.ID)
			if err != nil {
				return workflowapp.ExecutionResult{}, err
			}
			if !found {
				return workflowapp.ExecutionResult{}, synthesisInvalid("synthesis publication recovery has no committed application receipt")
			}
			return executor.finishApplication(ctx, execution, loaded.Processing.ID, "", result)
		}
		return synthesisReceipt(execution, loaded.Processing.ID, "", nil)
	}
	switch execution.NodeKind {
	case SynthesisPrepareNodeKind:
		return executor.prepareInput(ctx, execution, loaded)
	case SynthesisGenerateNodeKind:
		if loaded.Generation != nil && loaded.Generation.Status == organizingapp.SynthesisModelStepReady {
			return synthesisReceipt(execution, loaded.Processing.ID, loaded.Generation.InputRequestHash, nil)
		}
		input, err := executor.openInput(ctx, loaded, execution.NodeRunID, execution.NodeAttemptID)
		if err != nil {
			return workflowapp.ExecutionResult{}, err
		}
		result, err := executor.dependencies.Model.GenerateSynthesisForExecution(ctx, execution, input)
		if err != nil {
			return workflowapp.ExecutionResult{}, err
		}
		if err := result.Validate(input); err != nil {
			return workflowapp.ExecutionResult{}, err
		}
		return synthesisReceipt(execution, loaded.Processing.ID, input.RequestHash, nil)
	case SynthesisValidateNodeKind:
		if loaded.Semantic != nil && loaded.Semantic.Status == organizingapp.SynthesisModelStepReady {
			if loaded.Semantic.Semantic == nil || !loaded.Semantic.Semantic.Accepted {
				return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorNonRetryableFailure, organizingapp.ErrorCodeSynthesisSemanticRejected, false, "synthesis semantic review rejected the generated delta")
			}
			return synthesisReceipt(execution, loaded.Processing.ID, loaded.Semantic.InputRequestHash, nil)
		}
		input, result, err := executor.openGeneration(ctx, loaded)
		if err != nil {
			return workflowapp.ExecutionResult{}, err
		}
		receipt, err := executor.dependencies.Model.ValidateSynthesisSemanticsForExecution(ctx, execution, input, result)
		if err != nil {
			return workflowapp.ExecutionResult{}, err
		}
		if !receipt.Accepted || receipt.GenerationModelRunID != result.ModelRunID || receipt.GenerationOutputHash != result.OutputHash ||
			!validID(receipt.ModelRunID) || receipt.ModelRunID == result.ModelRunID || !validHash(receipt.OutputHash) {
			return workflowapp.ExecutionResult{}, synthesisInvalid("synthesis semantic receipt does not prove the generated delta")
		}
		return synthesisReceipt(execution, loaded.Processing.ID, input.RequestHash, nil)
	case SynthesisApplyNodeKind:
		if loaded.Input == nil {
			return workflowapp.ExecutionResult{}, synthesisInvalid("synthesis application has no frozen input")
		}
		if loaded.Applied != nil {
			return synthesisReceipt(execution, loaded.Processing.ID, loaded.Input.RequestHash, loaded.Applied.RevisionIDs)
		}
		if loaded.Semantic == nil || loaded.Semantic.Status != organizingapp.SynthesisModelStepReady || loaded.Semantic.Semantic == nil || !loaded.Semantic.Semantic.Accepted {
			return workflowapp.ExecutionResult{}, synthesisInvalid("synthesis application has no accepted semantic review")
		}
		if err := executor.dependencies.Store.PrepareSynthesisApplication(ctx, execution, loaded.Processing.ID); err != nil {
			return workflowapp.ExecutionResult{}, err
		}
		// A candidate may have committed before publication or our own receipt
		// failed. Recover that immutable application before reopening sources;
		// later source changes cannot invalidate an already committed result.
		result, found, err := executor.dependencies.Candidates.RecoverAppliedGeneration(ctx, execution.WorkspaceID, loaded.Processing.ID)
		if err != nil {
			return workflowapp.ExecutionResult{}, err
		}
		if found {
			return executor.finishApplication(ctx, execution, loaded.Processing.ID, loaded.Input.RequestHash, result)
		}
		input, generation, err := executor.openGeneration(ctx, loaded)
		if err != nil {
			return workflowapp.ExecutionResult{}, err
		}
		result, err = executor.dependencies.Candidates.ApplyGeneration(ctx, input, generation)
		if err != nil {
			return workflowapp.ExecutionResult{}, err
		}
		return executor.finishApplication(ctx, execution, loaded.Processing.ID, input.RequestHash, result)
	default:
		return workflowapp.ExecutionResult{}, synthesisInvalid("synthesis node is not registered")
	}
}

func (executor *SynthesisExecutor) finishApplication(ctx context.Context, execution workflowapp.ExecutionContext, processingID foundation.ID, requestHash string, result organizingapp.SynthesisApplyResult) (workflowapp.ExecutionResult, error) {
	if result.ProcessingID != processingID || result.Changed != (len(result.RevisionIDs) > 0) || len(result.RevisionIDs) > organizingapp.MaxSynthesisGeneratedNotes {
		return workflowapp.ExecutionResult{}, synthesisInvalid("synthesis candidate owner returned an invalid application receipt")
	}
	if err := executor.dependencies.Store.RecordSynthesisApplication(ctx, execution, result); err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	return synthesisReceipt(execution, processingID, requestHash, result.RevisionIDs)
}

func (executor *SynthesisExecutor) loadExecution(ctx context.Context, execution workflowapp.ExecutionContext) (SynthesisExecution, error) {
	if !validID(execution.WorkspaceID) || !validID(execution.RunID) || !validID(execution.DefinitionID) ||
		!validID(execution.NodeRunID) || !validID(execution.NodeAttemptID) || !validHash(execution.DefinitionHash) ||
		execution.DefinitionVersion != SynthesisDefinitionVersion || execution.InputSchemaVersion != SynthesisInputSchemaVersion ||
		execution.NodeKey != execution.NodeKind || !synthesisNodeKind(execution.NodeKind) {
		return SynthesisExecution{}, synthesisInvalid("synthesis Workflow execution binding is invalid")
	}
	run, err := executor.dependencies.Runs.GetRun(ctx, execution.RunID)
	if err != nil {
		return SynthesisExecution{}, err
	}
	input, decodeErr := DecodeSynthesisStartInput(run.Input)
	if run.ID != execution.RunID || run.WorkspaceID != execution.WorkspaceID || run.DefinitionID != execution.DefinitionID ||
		decodeErr != nil || !validID(input.ProcessingID) ||
		input.ExecutionNo < 1 || input.ExecutionNo > SynthesisMaxAttempts || run.IdempotencyKey != SynthesisStartIdempotencyKey(input.ProcessingID, input.ExecutionNo) {
		return SynthesisExecution{}, synthesisInvalid("synthesis Workflow start input is invalid")
	}
	loaded, err := executor.dependencies.Store.LoadSynthesisExecution(ctx, execution.WorkspaceID, input.ProcessingID, execution.RunID)
	if err != nil {
		return SynthesisExecution{}, err
	}
	if loaded.WorkflowRunID != execution.RunID || loaded.Processing.ID != input.ProcessingID || loaded.Processing.SourceEvent.Source.WorkspaceID != execution.WorkspaceID ||
		loaded.ExecutionNo != input.ExecutionNo || loaded.ApplyRecovery != input.ApplyRecovery || loaded.CreatedAt.IsZero() {
		return SynthesisExecution{}, synthesisInvalid("synthesis execution ledger changed its binding")
	}
	if executor.dependencies.Clock.Now().After(loaded.CreatedAt.Add(SynthesisMaxExecutionAge)) {
		return SynthesisExecution{}, workflowError(foundation.ErrorNonRetryableFailure, ErrorCodeSynthesisExecutionBudget, false, "synthesis execution exceeded its persisted time budget")
	}
	return loaded, nil
}

func (executor *SynthesisExecutor) prepareInput(ctx context.Context, execution workflowapp.ExecutionContext, loaded SynthesisExecution) (workflowapp.ExecutionResult, error) {
	if loaded.Input != nil {
		return synthesisReceipt(execution, loaded.Processing.ID, loaded.Input.RequestHash, nil)
	}
	candidates, err := executor.dependencies.Candidates.ListCandidates(ctx, execution.WorkspaceID)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if len(candidates) > organizingapp.MaxSynthesisCandidateNotes {
		return workflowapp.ExecutionResult{}, synthesisInvalid("synthesis candidate owner exceeded the input bound")
	}
	sources, err := executor.dependencies.Sources.ReadSynthesisSource(ctx, loaded.Processing.SourceEvent.Source)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if len(sources) == 0 {
		return workflowapp.ExecutionResult{}, synthesisInvalid("synthesis source owner returned no incoming excerpts")
	}
	snapshot := SynthesisFrozenInput{ProcessingID: loaded.Processing.ID, WorkflowRunID: execution.RunID,
		SourceEvent: loaded.Processing.SourceEvent, Notes: make([]SynthesisFrozenNote, 0, len(candidates)), Sources: []organizingdomain.SynthesisSourceRef{}}
	seen := make(map[string]bool)
	total := 0
	appendSource := func(source organizingapp.SynthesisSourceExcerpt) error {
		if err := source.Validate(execution.WorkspaceID); err != nil {
			return err
		}
		key, _ := source.Reference.IdentityKey()
		if seen[key] {
			return nil
		}
		if len(snapshot.Sources) >= organizingdomain.MaxSynthesisSources || total+len(source.Text) > organizingapp.MaxSynthesisSourceInputBytes {
			return workflowError(foundation.ErrorNonRetryableFailure, organizingapp.ErrorCodeSynthesisModelInputTooLarge, false, "synthesis exact source excerpts exceed the frozen input budget")
		}
		seen[key], total = true, total+len(source.Text)
		snapshot.Sources = append(snapshot.Sources, source.Reference)
		return nil
	}
	for _, source := range sources {
		if source.Reference.Source != snapshot.SourceEvent.Source {
			return workflowapp.ExecutionResult{}, synthesisInvalid("incoming source reader changed the source tuple")
		}
		if err := appendSource(source); err != nil {
			return workflowapp.ExecutionResult{}, err
		}
	}
	// Read each historical artifact once, even when many candidate items cite it.
	grouped := make(map[organizingdomain.SynthesisSourceVersion]map[string]organizingdomain.SynthesisSourceRef)
	var order []organizingdomain.SynthesisSourceVersion
	for _, candidate := range candidates {
		if candidate.Note.Validate() != nil || candidate.Revision.Validate() != nil || candidate.Note.WorkspaceID != execution.WorkspaceID ||
			candidate.Revision.NoteID != candidate.Note.ID || candidate.Note.CurrentRevisionID != candidate.Revision.ID {
			return workflowapp.ExecutionResult{}, synthesisInvalid("candidate owner returned a mismatched revision")
		}
		snapshot.Notes = append(snapshot.Notes, SynthesisFrozenNote{Note: candidate.Note, RevisionID: candidate.Revision.ID, RevisionHash: candidate.Revision.Hash})
		for _, item := range candidate.Revision.Items {
			for _, ref := range item.SourceReferences() {
				key, _ := ref.IdentityKey()
				if seen[key] {
					continue
				}
				if grouped[ref.Source] == nil {
					grouped[ref.Source] = make(map[string]organizingdomain.SynthesisSourceRef)
					order = append(order, ref.Source)
				}
				grouped[ref.Source][key] = ref
			}
		}
	}
	for _, version := range order {
		opened, err := executor.dependencies.Sources.ReadSynthesisSource(ctx, version)
		if err != nil {
			var classified *foundation.Error
			if errors.As(err, &classified) && (classified.Code == "SYNTHESIS_SOURCE_STALE" || classified.Kind == foundation.ErrorNotFound) {
				continue
			}
			return workflowapp.ExecutionResult{}, err
		}
		for _, source := range opened {
			key, err := source.Reference.IdentityKey()
			if err != nil {
				return workflowapp.ExecutionResult{}, err
			}
			ref, wanted := grouped[version][key]
			if !wanted {
				continue
			}
			source.Reference = ref
			if err := appendSource(source); err != nil {
				return workflowapp.ExecutionResult{}, err
			}
		}
	}
	snapshot.RequestHash, err = snapshot.ComputeHash()
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	frozen, err := executor.dependencies.Store.FreezeSynthesisInput(ctx, execution, snapshot)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if err := frozen.Validate(); err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	return synthesisReceipt(execution, loaded.Processing.ID, frozen.RequestHash, nil)
}

func (executor *SynthesisExecutor) openInput(ctx context.Context, loaded SynthesisExecution, nodeID, attemptID foundation.ID) (organizingapp.SynthesisGenerationInput, error) {
	if loaded.Input == nil || loaded.Input.Validate() != nil {
		return organizingapp.SynthesisGenerationInput{}, synthesisInvalid("synthesis frozen input is missing")
	}
	frozen := *loaded.Input
	input := organizingapp.SynthesisGenerationInput{ProcessingID: frozen.ProcessingID, SourceEvent: frozen.SourceEvent,
		WorkflowRunID: frozen.WorkflowRunID, NodeRunID: nodeID, NodeAttemptID: attemptID, RequestHash: frozen.RequestHash,
		Notes: make([]organizingapp.SynthesisGenerationNote, 0, len(frozen.Notes)), Sources: make([]organizingapp.SynthesisSourceExcerpt, 0, len(frozen.Sources))}
	for _, note := range frozen.Notes {
		revision, err := executor.dependencies.Candidates.GetSynthesisRevision(ctx, frozen.SourceEvent.Source.WorkspaceID, note.Note.ID, note.RevisionID)
		if err != nil {
			return organizingapp.SynthesisGenerationInput{}, err
		}
		if revision.Validate() != nil || revision.ID != note.RevisionID || revision.Hash != note.RevisionHash || revision.NoteID != note.Note.ID {
			return organizingapp.SynthesisGenerationInput{}, synthesisInvalid("synthesis frozen note revision changed")
		}
		input.Notes = append(input.Notes, organizingapp.SynthesisGenerationNote{Note: note.Note, Revision: revision})
	}
	opened := make(map[organizingdomain.SynthesisSourceVersion]map[string]organizingapp.SynthesisSourceExcerpt)
	for _, ref := range frozen.Sources {
		if opened[ref.Source] == nil {
			sources, err := executor.dependencies.Sources.ReadSynthesisSource(ctx, ref.Source)
			if err != nil {
				return organizingapp.SynthesisGenerationInput{}, err
			}
			values := make(map[string]organizingapp.SynthesisSourceExcerpt, len(sources))
			for _, source := range sources {
				if err := source.Validate(frozen.SourceEvent.Source.WorkspaceID); err != nil {
					return organizingapp.SynthesisGenerationInput{}, err
				}
				key, _ := source.Reference.IdentityKey()
				if _, duplicate := values[key]; duplicate {
					return organizingapp.SynthesisGenerationInput{}, synthesisInvalid("source reader duplicated an exact excerpt")
				}
				values[key] = source
			}
			opened[ref.Source] = values
		}
		key, _ := ref.IdentityKey()
		source, found := opened[ref.Source][key]
		if !found {
			return organizingapp.SynthesisGenerationInput{}, workflowError(foundation.ErrorVersionConflict, ErrorCodeSynthesisInputStale, false, "synthesis frozen source excerpt is no longer available")
		}
		source.Reference = ref
		input.Sources = append(input.Sources, source)
	}
	if err := input.Validate(); err != nil {
		return organizingapp.SynthesisGenerationInput{}, err
	}
	return input, nil
}

func (executor *SynthesisExecutor) openGeneration(ctx context.Context, loaded SynthesisExecution) (organizingapp.SynthesisGenerationInput, organizingapp.SynthesisGenerationResult, error) {
	step := loaded.Generation
	if step == nil || step.Status != organizingapp.SynthesisModelStepReady || step.Generation == nil || loaded.Input == nil ||
		step.WorkflowRunID != loaded.WorkflowRunID || step.ProcessingID != loaded.Processing.ID || step.InputRequestHash != loaded.Input.RequestHash {
		return organizingapp.SynthesisGenerationInput{}, organizingapp.SynthesisGenerationResult{}, synthesisInvalid("synthesis accepted generation is missing or mismatched")
	}
	input, err := executor.openInput(ctx, loaded, step.NodeRunID, step.NodeAttemptID)
	if err != nil {
		return organizingapp.SynthesisGenerationInput{}, organizingapp.SynthesisGenerationResult{}, err
	}
	if err := step.Generation.Validate(input); err != nil {
		return organizingapp.SynthesisGenerationInput{}, organizingapp.SynthesisGenerationResult{}, err
	}
	return input, *step.Generation, nil
}

func synthesisReceipt(execution workflowapp.ExecutionContext, processingID foundation.ID, requestHash string, revisions []foundation.ID) (workflowapp.ExecutionResult, error) {
	output, err := json.Marshal(SynthesisNodeReceipt{SchemaVersion: SynthesisOutputSchemaVersion, ProcessingID: processingID,
		WorkflowRunID: execution.RunID, Phase: execution.NodeKind, RequestHash: requestHash, RevisionIDs: revisions})
	return workflowapp.ExecutionResult{Output: output}, err
}

func synthesisNodeKind(kind string) bool {
	for _, registered := range SynthesisExecutorNodeKinds() {
		if registered == kind {
			return true
		}
	}
	return false
}

// DecodeSynthesisStartInput is shared by the executor and its persistence fence.
func DecodeSynthesisStartInput(raw []byte) (SynthesisStartInput, error) {
	type wire struct {
		ProcessingID  *foundation.ID `json:"processing_id"`
		ExecutionNo   *int           `json:"execution_no"`
		ApplyRecovery *bool          `json:"apply_recovery"`
	}
	value, err := strictjson.DecodeObject[wire](raw, strictjson.Limits{MaxDocumentBytes: 1024, MaxDepth: 2, MaxStringBytes: 128, MaxArrayItems: 1, MaxObjectFields: 3}, nil)
	if err != nil || value.ProcessingID == nil || !validID(*value.ProcessingID) || value.ExecutionNo == nil || *value.ExecutionNo < 1 ||
		*value.ExecutionNo > SynthesisMaxAttempts || value.ApplyRecovery == nil {
		return SynthesisStartInput{}, synthesisInvalid("synthesis Workflow input is not a complete strict document")
	}
	return SynthesisStartInput{ProcessingID: *value.ProcessingID, ExecutionNo: *value.ExecutionNo, ApplyRecovery: *value.ApplyRecovery}, nil
}

// EqualSynthesisFrozenGeneration compares only persistable input, never source
// text. The reopened excerpts are independently checked by Input.Validate.
func EqualSynthesisFrozenGeneration(frozen SynthesisFrozenInput, input organizingapp.SynthesisGenerationInput) bool {
	if frozen.ProcessingID != input.ProcessingID || frozen.WorkflowRunID != input.WorkflowRunID || frozen.SourceEvent != input.SourceEvent || frozen.RequestHash != input.RequestHash ||
		len(frozen.Notes) != len(input.Notes) || len(frozen.Sources) != len(input.Sources) {
		return false
	}
	for index, note := range frozen.Notes {
		if !reflect.DeepEqual(note.Note, input.Notes[index].Note) || note.RevisionID != input.Notes[index].Revision.ID || note.RevisionHash != input.Notes[index].Revision.Hash {
			return false
		}
	}
	for index, ref := range frozen.Sources {
		if ref != input.Sources[index].Reference {
			return false
		}
	}
	return true
}
