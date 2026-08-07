package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	captureapplication "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeCapabilityUnavailable 表示当前运行时没有可调用的文档画像模型能力。
	ErrorCodeCapabilityUnavailable = "CAPTURE_PROFILE_CAPABILITY_UNAVAILABLE"
	profileInputSchemaVersion      = "capture-profile-input/v1"
	profilePersistenceTimeout      = 5 * time.Second
	maxHeadingPartBytes            = 512
	maxHeadingParts                = 64
)

// GeneratorDependencies 是画像生成器唯一允许使用的 Agent 与持久化端口。
type GeneratorDependencies struct {
	Repository      captureapplication.ProfileGenerationRepository
	ModelRuns       agentapplication.ModelRunRepository
	Model           agentapplication.ChatModel
	Scheduler       agentapplication.StructuredPhaseScheduler
	Catalog         *agentapplication.RuntimeCatalog
	ModelProfileRef agentdomain.ModelProfileRef
	Budget          agentapplication.RunBudget
	IDs             foundation.IDGenerator
	Clock           foundation.Clock
}

// Generator 将冻结 Chunk 转为证据标签，执行 StructuredRunner，并原子闭合画像与 Model Run。
type Generator struct {
	repository      captureapplication.ProfileGenerationRepository
	modelRuns       agentapplication.ModelRunRepository
	model           agentapplication.ChatModel
	scheduler       agentapplication.StructuredPhaseScheduler
	catalog         *agentapplication.RuntimeCatalog
	modelProfileRef agentdomain.ModelProfileRef
	budget          agentapplication.RunBudget
	ids             foundation.IDGenerator
	clock           foundation.Clock
	unavailable     bool
}

// NewGenerator 创建一个使用真实 Agent StructuredRunner 的画像生成器。
func NewGenerator(dependencies GeneratorDependencies) (*Generator, error) {
	if nilPort(dependencies.Repository) || nilPort(dependencies.ModelRuns) || nilPort(dependencies.Model) ||
		dependencies.Catalog == nil || dependencies.ModelProfileRef.Validate() != nil ||
		dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, profileError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("profile generator dependencies are incomplete"))
	}
	if dependencies.Budget == (agentapplication.RunBudget{}) {
		dependencies.Budget = agentapplication.DefaultRunBudget()
	}
	if _, err := agentapplication.NewStructuredRunnerWithScheduler(dependencies.Model, dependencies.Catalog, dependencies.Budget, dependencies.Scheduler); err != nil {
		return nil, err
	}
	return &Generator{
		repository: dependencies.Repository, modelRuns: dependencies.ModelRuns, model: dependencies.Model, scheduler: dependencies.Scheduler,
		catalog: dependencies.Catalog, modelProfileRef: dependencies.ModelProfileRef,
		budget: dependencies.Budget, ids: dependencies.IDs, clock: dependencies.Clock,
	}, nil
}

// NewUnavailableGenerator 创建一个只持久化 CAPABILITY_UNAVAILABLE、绝不调用 Provider 的画像生成器。
func NewUnavailableGenerator(repository captureapplication.ProfileGenerationRepository, ids foundation.IDGenerator, clock foundation.Clock) (*Generator, error) {
	if nilPort(repository) || ids == nil || clock == nil {
		return nil, profileError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("unavailable profile generator dependencies are incomplete"))
	}
	return &Generator{repository: repository, ids: ids, clock: clock, unavailable: true}, nil
}

// Generate 为当前 Source Version 生成或精确重放一个不可变画像 Revision。
func (generator *Generator) Generate(ctx context.Context, request captureapplication.ProfileGenerationRequest) (captureapplication.ProfileGenerationResult, error) {
	if generator == nil || nilPort(generator.repository) || generator.ids == nil || generator.clock == nil {
		return captureapplication.ProfileGenerationResult{}, profileError(foundation.ErrorDependencyUnavailable,
			ErrorCodeCapabilityUnavailable, false, errors.New("profile generator is unavailable"))
	}
	lookup, contract, err := validateGenerationRequest(request)
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, err
	}
	ready, found, err := generator.repository.LookupReady(ctx, lookup, contract)
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, err
	}
	if found {
		return generationResult(ready)
	}

	attemptID, err := generator.ids.New()
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, err
	}
	prepared, err := generator.repository.PrepareProfile(ctx, captureapplication.PrepareProfileCommand{
		ProfileLookup: lookup, Contract: contract, RequestedProfileID: request.ProfileID,
		ProfileAttemptID: attemptID, WorkflowRunID: request.WorkflowRunID, NodeRunID: request.NodeRunID,
		NodeAttemptID: request.NodeAttemptID, StartedAt: generator.now(),
	})
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, err
	}
	if prepared.ReadyRevisionID != "" {
		return generationResult(captureapplication.ReadyProfile{ProfileID: prepared.ProfileID, RevisionID: prepared.ReadyRevisionID})
	}
	if err := validatePrepared(prepared); err != nil {
		return captureapplication.ProfileGenerationResult{}, err
	}

	if generator.unavailable || request.ModelSettingsRevision != nil && *request.ModelSettingsRevision == 0 {
		capabilityErr := profileError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("document profile model capability is disabled"))
		return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID, prepared, agentdomain.ModelRun{},
			capturedomain.ProfileStatusCapabilityUnavailable, capabilityErr)
	}

	runtimeSnapshot, err := generator.catalog.Snapshot(PromptRef(), SchemaRef(), ReducedSchemaRef(), generator.modelProfileRef)
	if err != nil {
		capabilityErr := profileError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, err)
		return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID, prepared, agentdomain.ModelRun{},
			capturedomain.ProfileStatusCapabilityUnavailable, capabilityErr)
	}
	source, err := generator.repository.LoadSource(ctx, lookup, request.ParseProjectionID, request.IndexVersionID)
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID, prepared, agentdomain.ModelRun{},
			capturedomain.ProfileStatusFailed, err)
	}
	modelInput, evidenceByLabel, err := encodeModelInput(source)
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID, prepared, agentdomain.ModelRun{},
			capturedomain.ProfileStatusFailed, err)
	}

	run, err := generator.prepareModelRun(ctx, request, prepared, source, runtimeSnapshot)
	if err != nil {
		if prepared.ModelRunID == "" {
			return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID,
				prepared, agentdomain.ModelRun{}, capturedomain.ProfileStatusFailed, err)
		}
		return captureapplication.ProfileGenerationResult{}, err
	}
	bound, err := generator.repository.BindProfileModelRun(ctx, captureapplication.BindProfileModelRunCommand{
		WorkspaceID: request.Capture.WorkspaceID, ProfileID: prepared.ProfileID,
		ProfileAttemptID: prepared.ProfileAttemptID, ExpectedProfileAttemptVersion: prepared.ProfileAttemptVersion,
		ModelRunID: run.ID,
	})
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, profileError(foundation.ErrorManualRecoveryRequired,
			ErrorCodeFinalizationUnknown, false, err)
	}
	if bound.ProfileID != prepared.ProfileID || bound.ProfileAttemptID != prepared.ProfileAttemptID ||
		bound.ModelRunID != run.ID || bound.ProfileAttemptVersion < prepared.ProfileAttemptVersion ||
		(prepared.ModelRunID == "" && bound.ProfileAttemptVersion == prepared.ProfileAttemptVersion) || bound.ReadyRevisionID != "" {
		return captureapplication.ProfileGenerationResult{}, profileError(foundation.ErrorConsistencyViolation,
			ErrorCodeContextInvalid, false, errors.New("bound profile attempt differs from the prepared attempt"))
	}

	recorded, err := agentapplication.NewRecordingChatModel(agentapplication.RecordingChatModelDependencies{
		Model: generator.model, Repository: generator.modelRuns, WorkspaceID: run.WorkspaceID,
		ModelRunID: run.ID, IDs: generator.ids, Clock: generator.clock,
	})
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID, bound, run,
			capturedomain.ProfileStatusFailed, err)
	}
	runner, err := agentapplication.NewStructuredRunnerWithScheduler(recorded, generator.catalog, generator.budget, generator.scheduler)
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID, bound, run,
			capturedomain.ProfileStatusFailed, err)
	}
	structured, err := runner.Run(ctx, agentapplication.StructuredRunRequest{
		ProfileRef: generator.modelProfileRef, PromptRef: PromptRef(), SchemaRef: SchemaRef(),
		ReducedSchemaRef: ReducedSchemaRef(), Input: modelInput,
	})
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID, bound, run,
			capturedomain.ProfileStatusFailed, err)
	}
	if structured.Runtime.Profile != runtimeSnapshot.Profile.Ref || structured.Runtime.Model != runtimeSnapshot.Profile.Model ||
		structured.Runtime.Prompt != PromptRef() ||
		(structured.Runtime.Schema != SchemaRef() && structured.Runtime.Schema != ReducedSchemaRef()) {
		runtimeErr := profileError(foundation.ErrorConsistencyViolation, ErrorCodeContextInvalid, false,
			errors.New("profile structured runtime differs from the frozen catalog"))
		return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID, bound, run,
			capturedomain.ProfileStatusFailed, runtimeErr)
	}
	output, err := DecodeOutput(structured.Output)
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID, bound, run,
			capturedomain.ProfileStatusFailed, err)
	}
	content, evidence, err := profileFromOutput(output, evidenceByLabel, request.Capture.WorkspaceID,
		request.Capture.LatestSourceVersionID, generator.now())
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID, bound, run,
			capturedomain.ProfileStatusFailed, err)
	}
	revisionID, err := generator.ids.New()
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID, bound, run,
			capturedomain.ProfileStatusFailed, err)
	}
	completedAt := generator.now()
	if completedAt.Before(bound.StartedAt) {
		completedAt = bound.StartedAt
	}
	digest, err := capturedomain.ComputeProfileDigest(content)
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID, bound, run,
			capturedomain.ProfileStatusFailed, err)
	}
	revision := capturedomain.ProfileRevision{
		ID: revisionID, ProfileID: bound.ProfileID, WorkspaceID: request.Capture.WorkspaceID,
		SourceVersionID: request.Capture.LatestSourceVersionID, ParseProjectionID: source.ParseProjectionID,
		IndexVersionID: source.IndexVersionID, ModelRunID: run.ID,
		ModelSettingsRevision: positiveRevision(request.ModelSettingsRevision), PromptVersion: PromptRef().Version,
		SchemaVersion: capturedomain.ProfileSchemaVersion, Content: content, ContentDigest: digest, CreatedAt: completedAt,
	}
	if err := revision.Validate(); err != nil {
		return captureapplication.ProfileGenerationResult{}, generator.failPrepared(ctx, request.Capture.WorkspaceID, bound, run,
			capturedomain.ProfileStatusFailed, err)
	}
	for index := range evidence {
		evidence[index].RevisionID = revisionID
		evidence[index].CreatedAt = completedAt
	}
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), profilePersistenceTimeout)
	ready, err = generator.repository.CompleteProfile(finalizeCtx, captureapplication.CompleteProfileCommand{
		WorkspaceID: request.Capture.WorkspaceID, ProfileAttemptID: bound.ProfileAttemptID,
		ExpectedProfileAttemptVersion: bound.ProfileAttemptVersion, ExpectedModelRunVersion: run.Version,
		Revision: revision, Evidence: evidence, CompletedAt: completedAt,
	})
	cancel()
	if err != nil {
		return captureapplication.ProfileGenerationResult{}, profileError(foundation.ErrorManualRecoveryRequired,
			ErrorCodeFinalizationUnknown, false, err)
	}
	return generationResult(ready)
}

type profileInput struct {
	SchemaVersion string                 `json:"schema_version"`
	Evidence      []profileInputEvidence `json:"evidence"`
}

type profileInputEvidence struct {
	Label       string   `json:"label"`
	Sequence    int      `json:"sequence"`
	HeadingPath []string `json:"heading_path"`
	Content     string   `json:"content"`
}

type labeledEvidence struct {
	ChunkID      foundation.ID
	SourceSpanID foundation.ID
}

func encodeModelInput(snapshot captureapplication.ProfileSourceSnapshot) ([]byte, map[string]labeledEvidence, error) {
	if !validID(snapshot.WorkspaceID) || !validID(snapshot.SourceVersionID) || !validID(snapshot.ParseProjectionID) ||
		!validID(snapshot.IndexVersionID) || len(snapshot.Chunks) == 0 ||
		len(snapshot.Chunks) > captureapplication.MaxProfileSourceChunks {
		return nil, nil, contextError(errors.New("profile source snapshot identity or chunk count is invalid"))
	}
	input := profileInput{SchemaVersion: profileInputSchemaVersion, Evidence: make([]profileInputEvidence, len(snapshot.Chunks))}
	byLabel := make(map[string]labeledEvidence, len(snapshot.Chunks))
	seenChunks := make(map[foundation.ID]struct{}, len(snapshot.Chunks))
	previousSequence := -1
	for index, chunk := range snapshot.Chunks {
		if !validID(chunk.ChunkID) || !validID(chunk.SourceSpanID) || chunk.Sequence < 0 ||
			chunk.Sequence <= previousSequence || len(chunk.Content) == 0 ||
			len(chunk.Content) > captureapplication.MaxProfileChunkBytes || !utf8.ValidString(chunk.Content) ||
			len(chunk.HeadingPath) > maxHeadingParts {
			return nil, nil, contextError(errors.New("profile source chunk is invalid or unordered"))
		}
		if _, duplicate := seenChunks[chunk.ChunkID]; duplicate {
			return nil, nil, contextError(errors.New("profile source chunks contain duplicate identities"))
		}
		seenChunks[chunk.ChunkID] = struct{}{}
		headingPath := append([]string(nil), chunk.HeadingPath...)
		for _, heading := range headingPath {
			if !canonicalText(heading, maxHeadingPartBytes) {
				return nil, nil, contextError(errors.New("profile source heading is invalid"))
			}
		}
		label := fmt.Sprintf("E%04d", index+1)
		input.Evidence[index] = profileInputEvidence{
			Label: label, Sequence: chunk.Sequence, HeadingPath: headingPath, Content: chunk.Content,
		}
		byLabel[label] = labeledEvidence{ChunkID: chunk.ChunkID, SourceSpanID: chunk.SourceSpanID}
		previousSequence = chunk.Sequence
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, nil, contextError(err)
	}
	if len(encoded) == 0 || len(encoded) > agentapplication.MaxStructuredInputBytes || !utf8.Valid(encoded) {
		return nil, nil, contextError(errors.New("profile model input exceeds the structured boundary"))
	}
	return encoded, byLabel, nil
}

func profileFromOutput(output Output, byLabel map[string]labeledEvidence, workspaceID, sourceVersionID foundation.ID, at time.Time) (capturedomain.ProfileContent, []capturedomain.ProfileEvidence, error) {
	mapLabels := func(labels []string) ([]foundation.ID, error) {
		spans := make([]foundation.ID, 0, len(labels))
		seen := make(map[foundation.ID]struct{}, len(labels))
		for _, label := range labels {
			binding, found := byLabel[label]
			if !found || !validID(binding.ChunkID) || !validID(binding.SourceSpanID) {
				return nil, outputError(errors.New("profile output references an unknown evidence label"))
			}
			if _, duplicate := seen[binding.SourceSpanID]; !duplicate {
				seen[binding.SourceSpanID] = struct{}{}
				spans = append(spans, binding.SourceSpanID)
			}
		}
		sort.Slice(spans, func(left, right int) bool { return spans[left] < spans[right] })
		return spans, nil
	}
	convertCandidates := func(values []CandidateOutput) ([]capturedomain.ProfileCandidate, error) {
		result := make([]capturedomain.ProfileCandidate, len(values))
		for index, value := range values {
			spans, err := mapLabels(value.EvidenceLabels)
			if err != nil {
				return nil, err
			}
			result[index] = capturedomain.ProfileCandidate{
				Label: value.Label, Aliases: append([]string(nil), value.Aliases...), SourceSpanIDs: spans,
			}
		}
		return result, nil
	}
	convertPoints := func(values []PointOutput) ([]capturedomain.ProfilePoint, error) {
		result := make([]capturedomain.ProfilePoint, len(values))
		for index, value := range values {
			spans, err := mapLabels(value.EvidenceLabels)
			if err != nil {
				return nil, err
			}
			result[index] = capturedomain.ProfilePoint{Text: value.Text, SourceSpanIDs: spans}
		}
		return result, nil
	}
	topics, err := convertCandidates(output.Topics)
	if err != nil {
		return capturedomain.ProfileContent{}, nil, err
	}
	terms, err := convertCandidates(output.Terms)
	if err != nil {
		return capturedomain.ProfileContent{}, nil, err
	}
	points, err := convertPoints(output.KnowledgePoints)
	if err != nil {
		return capturedomain.ProfileContent{}, nil, err
	}
	examples, err := convertPoints(output.Examples)
	if err != nil {
		return capturedomain.ProfileContent{}, nil, err
	}
	content, err := capturedomain.NormalizeProfileContent(capturedomain.ProfileContent{
		Summary: output.Summary, Topics: topics, Terms: terms, KnowledgePoints: points, Examples: examples,
	})
	if err != nil {
		return capturedomain.ProfileContent{}, nil, err
	}
	spanSet := make(map[foundation.ID]struct{})
	collectCandidates := func(values []capturedomain.ProfileCandidate) {
		for _, value := range values {
			for _, spanID := range value.SourceSpanIDs {
				spanSet[spanID] = struct{}{}
			}
		}
	}
	collectPoints := func(values []capturedomain.ProfilePoint) {
		for _, value := range values {
			for _, spanID := range value.SourceSpanIDs {
				spanSet[spanID] = struct{}{}
			}
		}
	}
	collectCandidates(content.Topics)
	collectCandidates(content.Terms)
	collectPoints(content.KnowledgePoints)
	collectPoints(content.Examples)
	spanIDs := make([]foundation.ID, 0, len(spanSet))
	for spanID := range spanSet {
		spanIDs = append(spanIDs, spanID)
	}
	sort.Slice(spanIDs, func(left, right int) bool { return spanIDs[left] < spanIDs[right] })
	evidence := make([]capturedomain.ProfileEvidence, len(spanIDs))
	for index, spanID := range spanIDs {
		evidence[index] = capturedomain.ProfileEvidence{
			WorkspaceID: workspaceID, SourceVersionID: sourceVersionID, SourceSpanID: spanID, CreatedAt: at,
		}
	}
	return content, evidence, nil
}

func (generator *Generator) prepareModelRun(ctx context.Context, request captureapplication.ProfileGenerationRequest, prepared captureapplication.PreparedProfile, source captureapplication.ProfileSourceSnapshot, snapshot agentapplication.RuntimeSnapshot) (agentdomain.ModelRun, error) {
	if prepared.ModelRunID != "" {
		return generator.safeExistingRun(ctx, request, prepared, source, snapshot, prepared.ModelRunID)
	}
	runID, err := generator.ids.New()
	if err != nil {
		return agentdomain.ModelRun{}, err
	}
	run := expectedModelRun(request, prepared, source, snapshot, runID)
	if err := agentdomain.ValidateModelRun(run); err != nil {
		return agentdomain.ModelRun{}, contextError(err)
	}
	created, replayed, err := generator.modelRuns.CreateModelRun(ctx, run)
	if err != nil {
		return agentdomain.ModelRun{}, err
	}
	if replayed {
		return generator.safeExistingRun(ctx, request, prepared, source, snapshot, created.ID)
	}
	if !sameExpectedModelRun(created, run) || created.ID != run.ID || created.Status != agentdomain.ModelRunRunning ||
		created.Version != 1 || agentdomain.ValidateModelRun(created) != nil {
		return agentdomain.ModelRun{}, profileError(foundation.ErrorManualRecoveryRequired, ErrorCodeRunReplayUnsafe,
			false, errors.New("created profile model run differs from its frozen request"))
	}
	return created, nil
}

func (generator *Generator) safeExistingRun(ctx context.Context, request captureapplication.ProfileGenerationRequest, prepared captureapplication.PreparedProfile, source captureapplication.ProfileSourceSnapshot, snapshot agentapplication.RuntimeSnapshot, runID foundation.ID) (agentdomain.ModelRun, error) {
	record, err := generator.modelRuns.GetModelRun(ctx, request.Capture.WorkspaceID, runID)
	if err != nil {
		return agentdomain.ModelRun{}, err
	}
	expected := expectedModelRun(request, prepared, source, snapshot, runID)
	if len(record.Calls) != 0 || !sameExpectedModelRun(record.Run, expected) || record.Run.ID != runID ||
		record.Run.Status != agentdomain.ModelRunRunning || record.Run.Version != 1 || agentdomain.ValidateModelRun(record.Run) != nil {
		return agentdomain.ModelRun{}, profileError(foundation.ErrorManualRecoveryRequired, ErrorCodeRunReplayUnsafe,
			false, errors.New("profile model run contains calls or terminal facts and cannot call the provider again"))
	}
	return record.Run, nil
}

func expectedModelRun(request captureapplication.ProfileGenerationRequest, prepared captureapplication.PreparedProfile, source captureapplication.ProfileSourceSnapshot, snapshot agentapplication.RuntimeSnapshot, runID foundation.ID) agentdomain.ModelRun {
	return agentdomain.ModelRun{
		ID: runID, WorkspaceID: request.Capture.WorkspaceID, WorkflowRunID: request.WorkflowRunID,
		NodeRunID: request.NodeRunID, NodeAttemptID: request.NodeAttemptID,
		ModelSettingsRevision: positiveRevision(request.ModelSettingsRevision), Model: snapshot.Profile.Model,
		Profile: snapshot.Profile.Ref, Prompt: snapshot.Prompt.Ref, Schema: snapshot.Schema.Ref,
		ReducedSchema: snapshot.ReducedSchema.Ref,
		Retrieval: agentdomain.RetrievalRef{
			IndexVersionID: source.IndexVersionID, EmbeddingVersionID: cloneID(source.EmbeddingVersionID),
		},
		Status: agentdomain.ModelRunRunning, Version: 1, CreatedAt: prepared.StartedAt, UpdatedAt: prepared.StartedAt,
	}
}

func sameExpectedModelRun(actual, expected agentdomain.ModelRun) bool {
	return actual.WorkspaceID == expected.WorkspaceID && actual.WorkflowRunID == expected.WorkflowRunID &&
		actual.NodeRunID == expected.NodeRunID && actual.NodeAttemptID == expected.NodeAttemptID &&
		sameRevision(actual.ModelSettingsRevision, expected.ModelSettingsRevision) && actual.Model == expected.Model &&
		actual.Profile == expected.Profile && actual.Prompt == expected.Prompt && actual.Schema == expected.Schema &&
		actual.ReducedSchema == expected.ReducedSchema && actual.Retrieval.IndexVersionID == expected.Retrieval.IndexVersionID &&
		sameID(actual.Retrieval.EmbeddingVersionID, expected.Retrieval.EmbeddingVersionID) &&
		actual.Retrieval.RerankModelVersion == expected.Retrieval.RerankModelVersion &&
		actual.CreatedAt.Equal(expected.CreatedAt) && actual.UpdatedAt.Equal(expected.UpdatedAt)
}

func (generator *Generator) failPrepared(ctx context.Context, workspaceID foundation.ID, prepared captureapplication.PreparedProfile, run agentdomain.ModelRun, status capturedomain.ProfileStatus, cause error) error {
	code, retryable := stableFailure(cause)
	completedAt := generator.now()
	if completedAt.Before(prepared.StartedAt) {
		completedAt = prepared.StartedAt
	}
	command := captureapplication.FailProfileCommand{
		WorkspaceID: workspaceID, ProfileID: prepared.ProfileID, ProfileAttemptID: prepared.ProfileAttemptID,
		ExpectedProfileAttemptVersion: prepared.ProfileAttemptVersion, ProfileStatus: status,
		ErrorCode: code, Retryable: retryable, CompletedAt: completedAt,
	}
	if run.ID != "" {
		command.WorkspaceID = run.WorkspaceID
		command.ModelRunID = run.ID
		command.ExpectedModelRunVersion = run.Version
		command.ModelRunStatus = agentdomain.ModelRunFailed
		if code == agentapplication.ErrorCodeModelCallPersistenceUnknown || errors.Is(cause, context.Canceled) {
			command.ModelRunStatus = agentdomain.ModelRunUnknown
		}
	}
	if !validID(command.WorkspaceID) {
		return profileError(foundation.ErrorConsistencyViolation, ErrorCodeContextInvalid, false,
			errors.New("profile failure lost its workspace binding"))
	}
	failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), profilePersistenceTimeout)
	failErr := generator.repository.FailProfile(failCtx, command)
	cancel()
	if failErr != nil {
		return profileError(foundation.ErrorManualRecoveryRequired, ErrorCodeFinalizationUnknown, false, errors.Join(cause, failErr))
	}
	return cause
}

func validateGenerationRequest(request captureapplication.ProfileGenerationRequest) (captureapplication.ProfileLookup, captureapplication.ProfileContract, error) {
	if request.Capture.Validate() != nil || request.Attempt.Validate() != nil || !validID(request.ProfileID) ||
		!validID(request.ParseProjectionID) || !validID(request.IndexVersionID) || !validID(request.WorkflowRunID) ||
		!validID(request.NodeRunID) || !validID(request.NodeAttemptID) ||
		(request.ModelSettingsRevision != nil && *request.ModelSettingsRevision < 0) ||
		request.Attempt.WorkspaceID != request.Capture.WorkspaceID || request.Attempt.CaptureID != request.Capture.ID ||
		request.Attempt.WorkflowRunID != request.WorkflowRunID || request.Attempt.Status != capturedomain.AttemptStatusRunning ||
		request.Capture.LatestSourceVersionID == "" || request.Attempt.SourceVersionID != request.Capture.LatestSourceVersionID ||
		request.Attempt.IndexVersionID != request.IndexVersionID {
		return captureapplication.ProfileLookup{}, captureapplication.ProfileContract{}, contextError(errors.New("profile generation request binding is invalid"))
	}
	lookup := captureapplication.ProfileLookup{
		WorkspaceID: request.Capture.WorkspaceID, CaptureID: request.Capture.ID,
		SourceVersionID: request.Capture.LatestSourceVersionID,
	}
	contract := captureapplication.ProfileContract{
		ParseProjectionID: request.ParseProjectionID, IndexVersionID: request.IndexVersionID,
		ModelSettingsRevision: positiveRevision(request.ModelSettingsRevision), PromptVersion: PromptRef().Version,
		SchemaVersion: capturedomain.ProfileSchemaVersion,
	}
	return lookup, contract, nil
}

func validatePrepared(prepared captureapplication.PreparedProfile) error {
	if !validID(prepared.ProfileID) || !validID(prepared.ProfileAttemptID) || prepared.ProfileAttemptNumber < 1 ||
		prepared.ProfileAttemptVersion < 1 || prepared.StartedAt.IsZero() ||
		(prepared.ModelRunID != "" && !validID(prepared.ModelRunID)) {
		return contextError(errors.New("prepared profile attempt is invalid"))
	}
	return nil
}

func generationResult(ready captureapplication.ReadyProfile) (captureapplication.ProfileGenerationResult, error) {
	if !validID(ready.ProfileID) || !validID(ready.RevisionID) {
		return captureapplication.ProfileGenerationResult{}, contextError(errors.New("ready profile binding is invalid"))
	}
	return captureapplication.ProfileGenerationResult{ProfileID: ready.ProfileID, RevisionID: ready.RevisionID}, nil
}

func stableFailure(err error) (string, bool) {
	var classified *foundation.Error
	if errors.As(err, &classified) && strings.TrimSpace(classified.Code) != "" {
		return classified.Code, classified.Retryable
	}
	if errors.Is(err, context.Canceled) {
		return agentapplication.ErrorCodeOperationCancelled, false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return agentapplication.ErrorCodeOperationDeadline, true
	}
	return "CAPTURE_PROFILE_GENERATION_FAILED", false
}

func positiveRevision(value *int64) *int64 {
	if value == nil || *value <= 0 {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func sameRevision(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func sameID(left, right *foundation.ID) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func contextError(cause error) error {
	return profileError(foundation.ErrorConsistencyViolation, ErrorCodeContextInvalid, false, cause)
}

func nilPort(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func (generator *Generator) now() time.Time {
	return generator.clock.Now().UTC()
}

var _ captureapplication.ProfileGenerator = (*Generator)(nil)
