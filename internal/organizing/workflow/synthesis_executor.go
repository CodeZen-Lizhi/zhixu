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
	Manuscripts SynthesisManuscriptRuntime
	Runs        WorkflowRunReader
	Store       SynthesisProcessingStore
	Candidates  SynthesisCandidateOwner
	Sources     organizingapp.SynthesisSourceReader
	Anchors     organizingapp.SynthesisAnchorAdmissionReader
	Goals       SynthesisGoalPreparation
	BodyRefresh organizingapp.SynthesisBodyRefreshPreparer
	Model       SynthesisExecutionModel
	Clock       foundation.Clock
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
	case SynthesisMergeReviewNodeKind:
		if execution.DefinitionVersion != SynthesisManuscriptDefinitionVersion || nilScopedDependency(executor.dependencies.Manuscripts) {
			return workflowapp.ExecutionResult{}, synthesisInvalid("manuscript runtime is unavailable")
		}
		input, generation, err := executor.openGeneration(ctx, loaded)
		if err != nil {
			return workflowapp.ExecutionResult{}, err
		}
		wait, err := executor.dependencies.Manuscripts.PrepareManuscripts(ctx, execution, input, generation)
		if err != nil {
			return workflowapp.ExecutionResult{}, err
		}
		if wait != nil {
			return workflowapp.ExecutionResult{HumanWait: wait}, nil
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
		if execution.DefinitionVersion == SynthesisManuscriptDefinitionVersion {
			if nilScopedDependency(executor.dependencies.Manuscripts) {
				return workflowapp.ExecutionResult{}, synthesisInvalid("manuscript runtime is unavailable")
			}
			result, err = executor.dependencies.Manuscripts.ApplyManuscripts(ctx, execution, input, generation)
		} else {
			result, err = executor.dependencies.Candidates.ApplyGeneration(ctx, input, generation)
		}
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
		(execution.DefinitionVersion != SynthesisDefinitionVersion && execution.DefinitionVersion != SynthesisManuscriptDefinitionVersion) || execution.InputSchemaVersion != SynthesisInputSchemaVersion ||
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
		loaded.BodyRefreshRequestID != input.BodyRefreshRequestID || loaded.GoalRequestID != input.GoalRequestID || loaded.Input != nil && ((loaded.Input.Goal == nil) != (input.GoalRequestID == "") || loaded.Input.Goal != nil && loaded.Input.Goal.RequestID != input.GoalRequestID) || loaded.ExecutionNo != input.ExecutionNo || loaded.ApplyRecovery != input.ApplyRecovery || loaded.CreatedAt.IsZero() {
		return SynthesisExecution{}, synthesisInvalid("synthesis execution ledger changed its binding")
	}
	if executor.dependencies.Clock.Now().After(loaded.CreatedAt.Add(SynthesisMaxExecutionAge + loaded.HumanWaitDuration)) {
		return SynthesisExecution{}, workflowError(foundation.ErrorNonRetryableFailure, ErrorCodeSynthesisExecutionBudget, false, "synthesis execution exceeded its persisted time budget")
	}
	return loaded, nil
}

func (executor *SynthesisExecutor) prepareInput(ctx context.Context, execution workflowapp.ExecutionContext, loaded SynthesisExecution) (workflowapp.ExecutionResult, error) {
	if loaded.Input != nil {
		return synthesisReceipt(execution, loaded.Processing.ID, loaded.Input.RequestHash, nil)
	}
	if loaded.GoalRequestID != "" {
		return executor.prepareGoalInput(ctx, execution, loaded)
	}
	if loaded.BodyRefreshRequestID != "" {
		return executor.prepareBodyRefreshInput(ctx, execution, loaded)
	}
	candidates, err := executor.dependencies.Candidates.ListCandidates(ctx, execution.WorkspaceID)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if len(candidates) > organizingapp.MaxSynthesisCandidateNotes {
		return workflowapp.ExecutionResult{}, synthesisInvalid("synthesis candidate owner exceeded the input bound")
	}
	if trigger := loaded.Processing.SourceEvent.Fusion; trigger != nil {
		filtered := candidates[:0]
		for _, candidate := range candidates {
			if candidate.Note.ID == trigger.NoteID {
				filtered = append(filtered, candidate)
			}
		}
		if len(filtered) != 1 {
			return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorVersionConflict, ErrorCodeSynthesisInputStale, false, "fusion anchor target is no longer a candidate")
		}
		candidates = filtered
	}
	sources, err := executor.dependencies.Sources.ReadSynthesisSource(ctx, loaded.Processing.SourceEvent.Source)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if len(sources) == 0 {
		return workflowapp.ExecutionResult{}, synthesisInvalid("synthesis source owner returned no incoming excerpts")
	}
	if trigger := loaded.Processing.SourceEvent.Fusion; trigger != nil {
		wanted := map[string]bool{}
		for _, ref := range trigger.AllowedSources {
			key, _ := ref.IdentityKey()
			wanted[key] = true
		}
		filtered := sources[:0]
		for _, source := range sources {
			key, _ := source.Reference.IdentityKey()
			if wanted[key] {
				filtered = append(filtered, source)
				delete(wanted, key)
			}
		}
		if len(filtered) == 0 || len(wanted) != 0 {
			return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorVersionConflict, ErrorCodeSynthesisInputStale, false, "fusion accepted source spans are unavailable")
		}
		sources = filtered
		// 审批是快照，不是永久写入授权。构造模型输入前，
		// 须重新检查锚点当前范围和每个已接受片段。
		if executor.dependencies.Anchors == nil || nilScopedDependency(executor.dependencies.Anchors) {
			return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisInputStale, false, "fusion anchor admission reader is unavailable")
		}
		admission, admissionErr := executor.dependencies.Anchors.ReadSynthesisAnchorAdmission(ctx, execution.WorkspaceID, trigger.NoteID, loaded.Processing.SourceEvent.Source)
		if admissionErr != nil {
			return workflowapp.ExecutionResult{}, admissionErr
		}
		if admission.AnchorID != trigger.AnchorID || admission.ScopeVersion != trigger.ScopeVersion {
			return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorVersionConflict, ErrorCodeSynthesisInputStale, false, "fusion anchor scope is no longer current")
		}
		admitted := make(map[string]bool, len(admission.AllowedSources))
		for _, ref := range admission.AllowedSources {
			key, keyErr := ref.IdentityKey()
			if keyErr != nil {
				return workflowapp.ExecutionResult{}, keyErr
			}
			admitted[key] = true
		}
		for _, ref := range trigger.AllowedSources {
			key, keyErr := ref.IdentityKey()
			if keyErr != nil || !admitted[key] {
				return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorVersionConflict, ErrorCodeSynthesisInputStale, false, "fusion accepted source spans are no longer current")
			}
		}
	}
	incomingRefs := make(map[string]bool, len(sources))
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
		key, keyErr := source.Reference.IdentityKey()
		if keyErr != nil {
			return workflowapp.ExecutionResult{}, keyErr
		}
		incomingRefs[key] = true
	}
	// Read each historical artifact once, even when many candidate items cite it.
	grouped := make(map[organizingdomain.SynthesisSourceVersion]map[string]organizingdomain.SynthesisSourceRef)
	var order []organizingdomain.SynthesisSourceVersion
	for _, candidate := range candidates {
		if candidate.Note.Validate() != nil || candidate.Revision.Validate() != nil || candidate.Note.WorkspaceID != execution.WorkspaceID ||
			candidate.Revision.NoteID != candidate.Note.ID || candidate.Note.CurrentRevisionID != candidate.Revision.ID {
			return workflowapp.ExecutionResult{}, synthesisInvalid("candidate owner returned a mismatched revision")
		}
		var anchor *organizingapp.SynthesisAnchorBinding
		if reader, ok := executor.dependencies.Anchors.(organizingapp.SynthesisAnchorAdmissionReader); ok && !nilScopedDependency(reader) {
			admission, admissionErr := reader.ReadSynthesisAnchorAdmission(ctx, execution.WorkspaceID, candidate.Note.ID, snapshot.SourceEvent.Source)
			if admissionErr != nil {
				return workflowapp.ExecutionResult{}, admissionErr
			}
			if admission.AnchorID != "" {
				allowed := make([]organizingdomain.SynthesisSourceRef, 0, len(admission.AllowedSources))
				for _, ref := range admission.AllowedSources {
					key, keyErr := ref.IdentityKey()
					if keyErr != nil {
						return workflowapp.ExecutionResult{}, keyErr
					}
					if incomingRefs[key] {
						allowed = append(allowed, ref)
					}
				}
				if len(allowed) == 0 {
					continue
				}
				binding := organizingapp.SynthesisAnchorBinding{AnchorID: admission.AnchorID, ScopeVersion: admission.ScopeVersion, Scope: admission.Scope, AllowedSources: allowed}
				if err := binding.Validate(execution.WorkspaceID); err != nil {
					return workflowapp.ExecutionResult{}, err
				}
				// 只有传入来源版本至少有一个被明确接受的精确片段时，
				// 锚点笔记才具备准入资格。
				anchor = &binding
			}
		}
		snapshot.Notes = append(snapshot.Notes, SynthesisFrozenNote{PublicationID: candidate.PublicationID, Note: candidate.Note, RevisionID: candidate.Revision.ID, RevisionHash: candidate.Revision.Hash, Anchor: anchor})
		references := []organizingdomain.SynthesisSourceRef{}
		for _, item := range candidate.Revision.Items {
			references = append(references, item.SourceReferences()...)
		}
		for _, ref := range references {
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
	openedByVersion := map[organizingdomain.SynthesisSourceVersion][]organizingapp.SynthesisSourceExcerpt{snapshot.SourceEvent.Source: sources}
	for _, version := range order {
		opened, err := executor.dependencies.Sources.ReadSynthesisSource(ctx, version)
		if err != nil {
			var classified *foundation.Error
			if errors.As(err, &classified) && (classified.Code == "SYNTHESIS_SOURCE_STALE" || classified.Kind == foundation.ErrorNotFound) {
				continue
			}
			return workflowapp.ExecutionResult{}, err
		}
		openedByVersion[version] = opened
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
	// 补充证据用于丰富已有断言，不能在有界生成请求中
	// 挤掉必需的传入证据或正文证据。
	for frozenIndex := range snapshot.Notes {
		frozenNote := snapshot.Notes[frozenIndex]
		candidateIndex := -1
		for index, candidate := range candidates {
			if candidate.Note.ID == frozenNote.Note.ID {
				candidateIndex = index
				break
			}
		}
		if candidateIndex < 0 {
			return workflowapp.ExecutionResult{}, synthesisInvalid("frozen synthesis candidate was not found")
		}
		candidate := candidates[candidateIndex]
		for _, supplement := range candidate.Supplements {
			if supplement.Validate() != nil || supplement.NoteID != candidate.Note.ID || supplement.WorkspaceID != execution.WorkspaceID || !supplement.MatchesItem(candidate.Revision.Items) {
				return workflowapp.ExecutionResult{}, synthesisInvalid("candidate owner returned an invalid supplement")
			}
			key, _ := supplement.Reference.IdentityKey()
			if seen[key] {
				snapshot.Notes[frozenIndex].Supplements = append(snapshot.Notes[frozenIndex].Supplements, supplement)
				continue
			}
			if len(snapshot.Sources) >= organizingdomain.MaxSynthesisSources {
				continue
			}
			version := supplement.Reference.Source
			opened, cached := openedByVersion[version]
			if !cached {
				opened, err = executor.dependencies.Sources.ReadSynthesisSource(ctx, version)
				if err != nil {
					var classified *foundation.Error
					if !errors.As(err, &classified) || classified.Code != "SYNTHESIS_SOURCE_STALE" && classified.Kind != foundation.ErrorNotFound {
						return workflowapp.ExecutionResult{}, err
					}
					opened = nil
				}
				openedByVersion[version] = opened
			}
			for _, source := range opened {
				sourceKey, sourceErr := source.Reference.IdentityKey()
				if sourceErr != nil {
					return workflowapp.ExecutionResult{}, sourceErr
				}
				if sourceKey != key {
					continue
				}
				if total+len(source.Text) > organizingapp.MaxSynthesisSourceInputBytes {
					break
				}
				source.Reference = supplement.Reference
				if err := appendSource(source); err != nil {
					return workflowapp.ExecutionResult{}, err
				}
				snapshot.Notes[frozenIndex].Supplements = append(snapshot.Notes[frozenIndex].Supplements, supplement)
				break
			}
		}
	}
	snapshot.freezeSourceIdentityPromptVersions()
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
	input := organizingapp.SynthesisGenerationInput{GenerationPromptVersion: frozen.GenerationPromptVersion, SemanticPromptVersion: frozen.SemanticPromptVersion, BodyRefresh: frozen.BodyRefresh, Goal: frozen.Goal, ProcessingID: frozen.ProcessingID, SourceEvent: frozen.SourceEvent,
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
		input.Notes = append(input.Notes, organizingapp.SynthesisGenerationNote{PublicationID: note.PublicationID, Note: note.Note, Revision: revision, Supplements: note.Supplements, Anchor: note.Anchor})
	}
	if frozen.BodyRefresh != nil {
		for _, binding := range organizingapp.SynthesisBodyRefreshPublications(frozen.BodyRefresh) {
			revision, err := executor.dependencies.Candidates.GetSynthesisRevision(ctx, binding.WorkspaceID, binding.NoteID, binding.RevisionID)
			if err != nil {
				return organizingapp.SynthesisGenerationInput{}, err
			}
			if revision.Hash != binding.ProjectionHash {
				return organizingapp.SynthesisGenerationInput{}, synthesisInvalid("refresh historical revision changed")
			}
			input.BodyRefreshRevisions = append(input.BodyRefreshRevisions, revision)
		}
	}
	if frozen.Goal != nil || frozen.BodyRefresh != nil {
		for _, ref := range frozen.Sources {
			view, err := executor.dependencies.Sources.OpenSynthesisSource(ctx, ref)
			if err != nil {
				return organizingapp.SynthesisGenerationInput{}, err
			}
			if view.Reference != ref || view.Validate(ref.Source.WorkspaceID) != nil || view.Availability != organizingdomain.MaterialAvailable {
				return organizingapp.SynthesisGenerationInput{}, workflowError(foundation.ErrorVersionConflict, ErrorCodeSynthesisInputStale, false, "selected original evidence is no longer available")
			}
			input.Sources = append(input.Sources, organizingapp.SynthesisSourceExcerpt{Reference: ref, Text: view.Text})
		}
		return input, input.Validate()
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
		ProcessingID         *foundation.ID  `json:"processing_id"`
		ExecutionNo          *int            `json:"execution_no"`
		ApplyRecovery        *bool           `json:"apply_recovery"`
		GoalRequestID        json.RawMessage `json:"goal_request_id"`
		BodyRefreshRequestID json.RawMessage `json:"body_refresh_request_id"`
	}
	value, err := strictjson.DecodeObject[wire](raw, strictjson.Limits{MaxDocumentBytes: 1024, MaxDepth: 2, MaxStringBytes: 128, MaxArrayItems: 1, MaxObjectFields: 5}, nil)
	if err != nil || value.ProcessingID == nil || !validID(*value.ProcessingID) || value.ExecutionNo == nil || *value.ExecutionNo < 1 ||
		*value.ExecutionNo > SynthesisMaxAttempts || value.ApplyRecovery == nil {
		return SynthesisStartInput{}, synthesisInvalid("synthesis Workflow input is not a complete strict document")
	}
	result := SynthesisStartInput{ProcessingID: *value.ProcessingID, ExecutionNo: *value.ExecutionNo, ApplyRecovery: *value.ApplyRecovery}
	if len(value.GoalRequestID) != 0 {
		if json.Unmarshal(value.GoalRequestID, &result.GoalRequestID) != nil || !validID(result.GoalRequestID) {
			return SynthesisStartInput{}, synthesisInvalid("synthesis goal request identity is invalid")
		}
	}
	if len(value.BodyRefreshRequestID) != 0 {
		if json.Unmarshal(value.BodyRefreshRequestID, &result.BodyRefreshRequestID) != nil || !validID(result.BodyRefreshRequestID) || result.GoalRequestID != "" {
			return SynthesisStartInput{}, synthesisInvalid("synthesis body refresh request identity is invalid")
		}
	}
	return result, nil
}

// EqualSynthesisFrozenGeneration compares only persistable input, never source
// text. The reopened excerpts are independently checked by Input.Validate.
func EqualSynthesisFrozenGeneration(frozen SynthesisFrozenInput, input organizingapp.SynthesisGenerationInput) bool {
	if frozen.GenerationPromptVersion != input.GenerationPromptVersion || frozen.SemanticPromptVersion != input.SemanticPromptVersion || !reflect.DeepEqual(frozen.BodyRefresh, input.BodyRefresh) || !reflect.DeepEqual(frozen.Goal, input.Goal) || frozen.ProcessingID != input.ProcessingID || frozen.WorkflowRunID != input.WorkflowRunID || !reflect.DeepEqual(frozen.SourceEvent, input.SourceEvent) || frozen.RequestHash != input.RequestHash ||
		len(frozen.Notes) != len(input.Notes) || len(frozen.Sources) != len(input.Sources) {
		return false
	}
	for index, note := range frozen.Notes {
		if note.PublicationID != input.Notes[index].PublicationID {
			return false
		}
		if !reflect.DeepEqual(note.Note, input.Notes[index].Note) || note.RevisionID != input.Notes[index].Revision.ID || note.RevisionHash != input.Notes[index].Revision.Hash || !reflect.DeepEqual(note.Supplements, input.Notes[index].Supplements) || !reflect.DeepEqual(note.Anchor, input.Notes[index].Anchor) {
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
