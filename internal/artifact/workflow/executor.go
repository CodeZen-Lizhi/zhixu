package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const (
	generationRetrievalLimit int32 = maxGenerationCitations
	maxRetrievalQueryBytes         = 8 * 1024
	maxOpenedExcerptBytes          = 16 * 1024
	finalizationTimeout            = 5 * time.Second
)

// ExecutorDependencies 是 Artifact 章节生成节点的全部受控依赖。
type ExecutorDependencies struct {
	Model       agentapplication.ChatModel
	Scheduler   agentapplication.StructuredPhaseScheduler
	Catalog     *agentapplication.RuntimeCatalog
	Repository  agentapplication.ModelRunRepository
	Context     ContextLoader
	Retrieval   agentapplication.RetrievalPort
	Eligibility agentapplication.EvidenceEligibilityPort
	Finalizer   Finalizer
	IDs         foundation.IDGenerator
	Clock       foundation.Clock
	Budget      agentapplication.RunBudget
}

// Executor 把一个冻结 Artifact 章节绑定到唯一记录型 Model Run 和原子 Revision 终态。
type Executor struct {
	dependencies ExecutorDependencies
}

// NewExecutor 创建 retrieval-first、无 Tool Loop 且 fail-closed 的 Artifact 章节生成 Executor。
func NewExecutor(dependencies ExecutorDependencies) (*Executor, error) {
	if nilDependency(dependencies.Model) || dependencies.Catalog == nil || nilDependency(dependencies.Repository) ||
		nilDependency(dependencies.Context) || nilDependency(dependencies.Retrieval) || nilDependency(dependencies.Eligibility) ||
		nilDependency(dependencies.Finalizer) || nilDependency(dependencies.IDs) || nilDependency(dependencies.Clock) {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("artifact generation dependencies are incomplete"))
	}
	if dependencies.Budget == (agentapplication.RunBudget{}) {
		dependencies.Budget = agentapplication.DefaultRunBudget()
	}
	if _, err := agentapplication.NewStructuredRunnerWithScheduler(dependencies.Model, dependencies.Catalog, dependencies.Budget, dependencies.Scheduler); err != nil {
		return nil, err
	}
	return &Executor{dependencies: dependencies}, nil
}

// Execute 先恢复精确终态，再重载冻结上下文、验证正式证据、执行记录型模型并原子完成 Revision 与 Model Run。
func (executor *Executor) Execute(ctx context.Context, execution workflowapplication.ExecutionContext) (workflowapplication.ExecutionResult, error) {
	if executor == nil || !executor.available() {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("artifact generation executor is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateExecution(execution); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	input, err := DecodeInput(execution.Input)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	lookup := FinalizationLookup{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID, Input: input,
	}
	if receipt, found, lookupErr := executor.dependencies.Finalizer.Lookup(ctx, lookup); lookupErr != nil {
		return workflowapplication.ExecutionResult{}, lookupErr
	} else if found {
		if err := ValidateOutputReceiptBinding(receipt, input); err != nil {
			return workflowapplication.ExecutionResult{}, err
		}
		return encodeExecutionResult(receipt)
	}

	generationContext, target, err := executor.loadContext(ctx, execution, input)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	retrieval, evidence, err := executor.loadEligibleEvidence(ctx, generationContext, target)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	snapshot, primarySchema, err := executor.runtimeSnapshot(generationContext.ProfileRef, len(evidence) > 0)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	run, err := executor.createModelRun(ctx, execution, snapshot, retrieval)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}

	proposal, executeErr, unknown := executor.executeCreatedRun(ctx, run, generationContext.Current, target, evidence, primarySchema, snapshot)
	if executeErr != nil {
		if finalizeErr := executor.bestEffortFinalizeRun(ctx, run, executeErr, unknown); finalizeErr != nil {
			return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorManualRecoveryRequired, ErrorCodeFinalizationUnknown, false, finalizeErr)
		}
		return workflowapplication.ExecutionResult{}, executeErr
	}
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizationTimeout)
	receipt, _, finalizeErr := executor.dependencies.Finalizer.Finalize(finalizeContext, FinalizeSectionCommand{
		FinalizationLookup: lookup, ModelRunID: run.ID, ExpectedModelRunVersion: run.Version, Proposal: proposal,
	})
	cancel()
	if finalizeErr != nil {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorManualRecoveryRequired, ErrorCodeFinalizationUnknown, false, finalizeErr)
	}
	if receipt.ModelRunID != run.ID {
		return workflowapplication.ExecutionResult{}, outputError(errors.New("artifact finalization receipt differs from the model run"))
	}
	if err := ValidateOutputReceiptBinding(receipt, input); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return encodeExecutionResult(receipt)
}

func (executor *Executor) available() bool {
	dependencies := executor.dependencies
	return !nilDependency(dependencies.Model) && dependencies.Catalog != nil && !nilDependency(dependencies.Repository) &&
		!nilDependency(dependencies.Context) && !nilDependency(dependencies.Retrieval) && !nilDependency(dependencies.Eligibility) &&
		!nilDependency(dependencies.Finalizer) && !nilDependency(dependencies.IDs) && !nilDependency(dependencies.Clock)
}

func (executor *Executor) loadContext(
	ctx context.Context,
	execution workflowapplication.ExecutionContext,
	input Input,
) (GenerationContext, artifactdomain.OutlineSection, error) {
	loaded, err := executor.dependencies.Context.LoadGenerationContext(ctx, GenerationContextQuery{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, NodeRunID: execution.NodeRunID, Input: input,
	})
	if err != nil {
		return GenerationContext{}, artifactdomain.OutlineSection{}, err
	}
	current := loaded.Current
	source := loaded.SourceRevision
	if artifactapplication.ValidateState(current) != nil || artifactdomain.ValidateRevision(source) != nil {
		return GenerationContext{}, artifactdomain.OutlineSection{}, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeContextInvalid, false, errors.New("artifact generation context contains invalid domain state"))
	}
	if current.Artifact.WorkspaceID != execution.WorkspaceID || current.Artifact.ID != input.ArtifactID ||
		current.Artifact.Version < input.ArtifactVersion || current.Revision.RevisionNo < input.RevisionNo ||
		source.ArtifactID != input.ArtifactID || source.ID != input.RevisionID || source.RevisionNo != input.RevisionNo ||
		loaded.ProfileRef.Validate() != nil {
		return GenerationContext{}, artifactdomain.OutlineSection{}, workflowError(foundation.ErrorVersionConflict, ErrorCodeContextInvalid, false, errors.New("artifact generation context differs from frozen workflow input"))
	}
	target, err := artifactapplication.ValidateSectionGenerationRebase(source, current, input.SectionKey)
	if err != nil {
		return GenerationContext{}, artifactdomain.OutlineSection{}, workflowError(foundation.ErrorVersionConflict, ErrorCodeContextInvalid, false, err)
	}
	return GenerationContext{
		Current: artifactapplication.State{
			Artifact: artifactdomain.CloneArtifact(current.Artifact), Revision: artifactdomain.CloneRevision(current.Revision),
		},
		SourceRevision: artifactdomain.CloneRevision(source), ProfileRef: loaded.ProfileRef,
	}, target, nil
}

type eligibleEvidence struct {
	Label    string
	Citation agentdomain.Citation
	Excerpt  string
}

func (executor *Executor) loadEligibleEvidence(
	ctx context.Context,
	generationContext GenerationContext,
	target artifactdomain.OutlineSection,
) (agentdomain.RetrievalRef, []eligibleEvidence, error) {
	request := agentapplication.RetrievalRequest{
		WorkspaceID: generationContext.Current.Artifact.WorkspaceID,
		Query:       buildRetrievalQuery(generationContext.Current.Artifact, target),
		Limit:       generationRetrievalLimit,
	}
	if err := agentapplication.ValidateRetrievalRequest(request); err != nil {
		return agentdomain.RetrievalRef{}, nil, err
	}
	batch, err := executor.dependencies.Retrieval.Retrieve(ctx, request)
	if err != nil {
		return agentdomain.RetrievalRef{}, nil, err
	}
	if batch.WorkspaceID != request.WorkspaceID || !validID(batch.IndexVersionID) || len(batch.Items) > int(generationRetrievalLimit) {
		return agentdomain.RetrievalRef{}, nil, evidenceError("artifact retrieval result has invalid scope or size")
	}
	if batch.EmbeddingVersionID != nil && (!validID(*batch.EmbeddingVersionID) || *batch.EmbeddingVersionID == batch.IndexVersionID) {
		return agentdomain.RetrievalRef{}, nil, evidenceError("artifact retrieval embedding binding is invalid")
	}
	retrieval := agentdomain.RetrievalRef{IndexVersionID: batch.IndexVersionID}
	if batch.EmbeddingVersionID != nil {
		embedding := *batch.EmbeddingVersionID
		retrieval.EmbeddingVersionID = &embedding
	}
	if err := retrieval.Validate(); err != nil {
		return agentdomain.RetrievalRef{}, nil, evidenceError("artifact retrieval version is invalid")
	}

	citations := make([]agentdomain.Citation, 0, len(batch.Items))
	seenCitationIDs := make(map[string]struct{}, len(batch.Items))
	seenProvenance := make(map[knowledgedomain.ProvenanceRef]struct{}, len(batch.Items))
	for _, item := range batch.Items {
		citation := item.Citation
		if citation.Validate() != nil || citation.WorkspaceID != batch.WorkspaceID || citation.IndexVersionID != batch.IndexVersionID || item.CapturedAt.IsZero() {
			return agentdomain.RetrievalRef{}, nil, evidenceError("artifact retrieval candidate binding is invalid")
		}
		if _, duplicate := seenCitationIDs[citation.ID]; duplicate {
			return agentdomain.RetrievalRef{}, nil, evidenceError("artifact retrieval contains duplicate citation labels")
		}
		seenCitationIDs[citation.ID] = struct{}{}
		provenance := provenanceFromCitation(citation)
		if _, duplicate := seenProvenance[provenance]; duplicate {
			continue
		}
		seenProvenance[provenance] = struct{}{}
		citations = append(citations, citation)
	}
	if len(citations) == 0 {
		return retrieval, []eligibleEvidence{}, nil
	}
	opened, err := executor.dependencies.Retrieval.OpenBatch(ctx, citations)
	if err != nil {
		return agentdomain.RetrievalRef{}, nil, err
	}
	if len(opened) != len(citations) {
		return agentdomain.RetrievalRef{}, nil, evidenceError("artifact opened evidence batch is incomplete")
	}
	provenance := make([]knowledgedomain.ProvenanceRef, len(citations))
	for index, value := range opened {
		if value.Citation != citations[index] || !canonicalText(value.Excerpt, maxOpenedExcerptBytes) {
			return agentdomain.RetrievalRef{}, nil, evidenceError("artifact opened evidence differs from retrieval citation")
		}
		provenance[index] = provenanceFromCitation(citations[index])
	}
	eligibility, err := executor.dependencies.Eligibility.CheckEvidenceEligibility(ctx, knowledgedomain.EvidenceEligibilityQuery{
		WorkspaceID: batch.WorkspaceID, Provenance: provenance,
	})
	if err != nil {
		return agentdomain.RetrievalRef{}, nil, err
	}
	if len(eligibility) != len(provenance) {
		return agentdomain.RetrievalRef{}, nil, evidenceError("artifact eligibility result is incomplete")
	}
	byProvenance := make(map[knowledgedomain.ProvenanceRef]knowledgedomain.ProvenanceEligibility, len(eligibility))
	for _, result := range eligibility {
		if result.Provenance.WorkspaceID != batch.WorkspaceID || knowledgedomain.ValidateProvenanceEligibility(result) != nil {
			return agentdomain.RetrievalRef{}, nil, evidenceError("artifact eligibility result is invalid")
		}
		if _, requested := seenProvenance[result.Provenance]; !requested {
			return agentdomain.RetrievalRef{}, nil, evidenceError("artifact eligibility returned unexpected provenance")
		}
		if _, duplicate := byProvenance[result.Provenance]; duplicate {
			return agentdomain.RetrievalRef{}, nil, evidenceError("artifact eligibility returned duplicate provenance")
		}
		byProvenance[result.Provenance] = result
	}
	result := make([]eligibleEvidence, 0, len(citations))
	for index, citation := range citations {
		projection, exists := byProvenance[provenance[index]]
		if !exists {
			return agentdomain.RetrievalRef{}, nil, evidenceError("artifact eligibility omitted requested provenance")
		}
		// Artifact CitationVerifier 当前只接受无争议的正式 Evidence；争议和不合格结果均 fail closed 为不可选证据。
		if projection.Eligibility != knowledgedomain.EvidenceEligible {
			continue
		}
		result = append(result, eligibleEvidence{
			Label: fmt.Sprintf("citation-%03d", len(result)+1), Citation: citation, Excerpt: opened[index].Excerpt,
		})
	}
	return retrieval, result, nil
}

func (executor *Executor) runtimeSnapshot(profile agentdomain.ModelProfileRef, hasEvidence bool) (agentapplication.RuntimeSnapshot, agentdomain.SchemaRef, error) {
	primary := SchemaRef()
	if !hasEvidence {
		// 没有可选正式证据时从第一次调用起就使用只允许 GAP 的 Schema，而不是信任模型自行降级。
		primary = ReducedSchemaRef()
	}
	snapshot, err := executor.dependencies.Catalog.Snapshot(PromptRef(), primary, ReducedSchemaRef(), profile)
	return snapshot, primary, err
}

func (executor *Executor) createModelRun(
	ctx context.Context,
	execution workflowapplication.ExecutionContext,
	snapshot agentapplication.RuntimeSnapshot,
	retrieval agentdomain.RetrievalRef,
) (agentdomain.ModelRun, error) {
	runID, err := executor.dependencies.IDs.New()
	if err != nil {
		return agentdomain.ModelRun{}, err
	}
	now := executor.dependencies.Clock.Now().UTC()
	run := agentdomain.ModelRun{
		ID: runID, WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
		ModelSettingsRevision: cloneModelSettingsRevision(execution.ModelSettingsRevision),
		Model:                 snapshot.Profile.Model, Profile: snapshot.Profile.Ref, Prompt: snapshot.Prompt.Ref,
		Schema: snapshot.Schema.Ref, ReducedSchema: snapshot.ReducedSchema.Ref, Retrieval: retrieval,
		Status: agentdomain.ModelRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := agentdomain.ValidateModelRun(run); err != nil {
		return agentdomain.ModelRun{}, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeContextInvalid, false, err)
	}
	created, replayed, err := executor.dependencies.Repository.CreateModelRun(ctx, run)
	if err != nil {
		return agentdomain.ModelRun{}, err
	}
	if !replayed {
		if created.ID != run.ID || !sameModelRunExecutionBinding(created, run) || created.Status != agentdomain.ModelRunRunning ||
			created.Version != 1 || !created.UpdatedAt.Equal(created.CreatedAt) || agentdomain.ValidateModelRun(created) != nil {
			return agentdomain.ModelRun{}, workflowError(foundation.ErrorManualRecoveryRequired, ErrorCodeRunReplayUnsafe, false, errors.New("artifact model run cannot be safely executed"))
		}
		return created, nil
	}
	record, lookupErr := executor.dependencies.Repository.GetModelRun(ctx, execution.WorkspaceID, created.ID)
	if lookupErr != nil || len(record.Calls) != 0 || record.Run.ID != created.ID ||
		!sameModelRunExecutionBinding(created, run) || created.Status != agentdomain.ModelRunRunning ||
		created.Version != 1 || !created.UpdatedAt.Equal(created.CreatedAt) || agentdomain.ValidateModelRun(created) != nil ||
		!sameModelRunExecutionBinding(record.Run, run) || record.Run.Status != agentdomain.ModelRunRunning ||
		record.Run.Version != 1 || !record.Run.UpdatedAt.Equal(record.Run.CreatedAt) || agentdomain.ValidateModelRun(record.Run) != nil {
		if lookupErr == nil {
			lookupErr = errors.New("artifact model run replay contains provider-call or terminal facts")
		}
		return agentdomain.ModelRun{}, workflowError(foundation.ErrorManualRecoveryRequired, ErrorCodeRunReplayUnsafe, false,
			errors.Join(errors.New("artifact model run cannot be safely executed again"), lookupErr))
	}
	return record.Run, nil
}

func sameModelRunExecutionBinding(left, right agentdomain.ModelRun) bool {
	return left.WorkspaceID == right.WorkspaceID && left.WorkflowRunID == right.WorkflowRunID &&
		left.NodeRunID == right.NodeRunID && left.NodeAttemptID == right.NodeAttemptID &&
		sameModelSettingsRevision(left.ModelSettingsRevision, right.ModelSettingsRevision) &&
		left.Model == right.Model && left.Profile == right.Profile && left.Prompt == right.Prompt &&
		left.Schema == right.Schema && left.ReducedSchema == right.ReducedSchema &&
		sameRetrievalRef(left.Retrieval, right.Retrieval)
}

func sameModelSettingsRevision(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func cloneModelSettingsRevision(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func sameRetrievalRef(left, right agentdomain.RetrievalRef) bool {
	if left.IndexVersionID != right.IndexVersionID || left.RerankModelVersion != right.RerankModelVersion ||
		(left.EmbeddingVersionID == nil) != (right.EmbeddingVersionID == nil) {
		return false
	}
	return left.EmbeddingVersionID == nil || *left.EmbeddingVersionID == *right.EmbeddingVersionID
}

func (executor *Executor) executeCreatedRun(
	ctx context.Context,
	run agentdomain.ModelRun,
	state artifactapplication.State,
	target artifactdomain.OutlineSection,
	evidence []eligibleEvidence,
	primarySchema agentdomain.SchemaRef,
	snapshot agentapplication.RuntimeSnapshot,
) (SectionProposal, error, bool) {
	modelInput, err := encodeModelInput(state, target, evidence)
	if err != nil {
		return SectionProposal{}, err, false
	}
	recorded, err := agentapplication.NewRecordingChatModel(agentapplication.RecordingChatModelDependencies{
		Model: executor.dependencies.Model, Repository: executor.dependencies.Repository,
		WorkspaceID: run.WorkspaceID, ModelRunID: run.ID, IDs: executor.dependencies.IDs, Clock: executor.dependencies.Clock,
	})
	if err != nil {
		return SectionProposal{}, err, false
	}
	runner, err := agentapplication.NewStructuredRunnerWithScheduler(recorded, executor.dependencies.Catalog, executor.dependencies.Budget, executor.dependencies.Scheduler)
	if err != nil {
		return SectionProposal{}, err, false
	}
	structured, err := runner.Run(ctx, agentapplication.StructuredRunRequest{
		ProfileRef: snapshot.Profile.Ref, PromptRef: PromptRef(), SchemaRef: primarySchema,
		ReducedSchemaRef: ReducedSchemaRef(), Input: modelInput,
	})
	if err != nil {
		return SectionProposal{}, err, stableErrorCode(err) == agentapplication.ErrorCodeModelCallPersistenceUnknown
	}
	if structured.Runtime.Profile != snapshot.Profile.Ref || structured.Runtime.Model != snapshot.Profile.Model {
		return SectionProposal{}, outputError(errors.New("artifact structured runtime profile or model drifted")), false
	}
	output, err := DecodeSectionGenerationOutput(structured.Output)
	if err != nil {
		return SectionProposal{}, err, false
	}
	proposal, err := proposalFromOutput(target, evidence, output, structured.Runtime)
	if err != nil {
		return SectionProposal{}, err, false
	}
	return proposal, nil, false
}

type modelArtifactInput struct {
	Type            string `json:"type"`
	Title           string `json:"title"`
	ScopeDefinition string `json:"scope_definition"`
}

type modelSectionInput struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

type modelEvidenceInput struct {
	Label   string `json:"label"`
	Excerpt string `json:"excerpt"`
}

type sectionModelInput struct {
	Artifact modelArtifactInput   `json:"artifact"`
	Section  modelSectionInput    `json:"section"`
	Evidence []modelEvidenceInput `json:"evidence"`
}

func encodeModelInput(state artifactapplication.State, target artifactdomain.OutlineSection, evidence []eligibleEvidence) ([]byte, error) {
	modelEvidence := make([]modelEvidenceInput, len(evidence))
	for index, item := range evidence {
		modelEvidence[index] = modelEvidenceInput{Label: item.Label, Excerpt: item.Excerpt}
	}
	encoded, err := json.Marshal(sectionModelInput{
		Artifact: modelArtifactInput{
			Type: state.Artifact.Type, Title: state.Artifact.Title, ScopeDefinition: state.Artifact.ScopeDefinition,
		},
		Section: modelSectionInput{Key: target.Key, Title: target.Title}, Evidence: modelEvidence,
	})
	if err != nil {
		return nil, workflowError(foundation.ErrorNonRetryableFailure, ErrorCodeContextInvalid, false, err)
	}
	if len(encoded) == 0 || len(encoded) > agentapplication.MaxStructuredInputBytes || !utf8.Valid(encoded) {
		return nil, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeContextInvalid, false, errors.New("artifact model input exceeds the structured boundary"))
	}
	return encoded, nil
}

func proposalFromOutput(
	target artifactdomain.OutlineSection,
	evidence []eligibleEvidence,
	output SectionGenerationEnvelope,
	runtime agentapplication.FrozenRuntimeRefs,
) (SectionProposal, error) {
	byLabel := make(map[string]agentdomain.Citation, len(evidence))
	for _, item := range evidence {
		byLabel[item.Label] = item.Citation
	}
	citations := make([]agentdomain.Citation, len(output.Payload.CitationLabels))
	for index, label := range output.Payload.CitationLabels {
		citation, exists := byLabel[label]
		if !exists {
			return SectionProposal{}, outputError(errors.New("artifact model selected an unknown citation label"))
		}
		citations[index] = citation
	}
	gaps := make([]artifactdomain.Gap, len(output.Payload.Gaps))
	for index, gap := range output.Payload.Gaps {
		gaps[index] = artifactdomain.Gap{Code: gap.Code, Description: gap.Description}
	}
	if runtime.Profile.Validate() != nil || runtime.Prompt != PromptRef() ||
		(runtime.Schema != SchemaRef() && runtime.Schema != ReducedSchemaRef()) || runtime.Model.Validate() != nil {
		return SectionProposal{}, outputError(errors.New("artifact structured runtime references drifted"))
	}
	return SectionProposal{
		SectionKey: target.Key, Title: target.Title, Content: output.Payload.Content, Citations: citations,
		Coverage: artifactdomain.Coverage{SectionKey: target.Key, Status: output.Payload.CoverageStatus, Gaps: gaps},
		Metadata: artifactdomain.GenerationMetadata{
			PromptVersion: runtime.Prompt.Version, ModelVersion: runtime.Model.ModelVersion,
			WorkflowDefinitionVersion: strconv.FormatInt(DefinitionVersion, 10), SchemaVersion: runtime.Schema.Version,
		},
	}, nil
}

func (executor *Executor) bestEffortFinalizeRun(ctx context.Context, run agentdomain.ModelRun, cause error, unknown bool) error {
	status := agentdomain.ModelRunFailed
	code := stableErrorCode(cause)
	if code == "" {
		code = "ARTIFACT_WORKFLOW_EXECUTION_FAILED"
	}
	if unknown {
		status = agentdomain.ModelRunUnknown
		code = agentapplication.ErrorCodeModelCallPersistenceUnknown
	}
	now := executor.dependencies.Clock.Now().UTC()
	if now.Before(run.CreatedAt) {
		now = run.CreatedAt
	}
	terminal := run
	terminal.Status = status
	terminal.FinalErrorCode = code
	terminal.Version++
	terminal.UpdatedAt = now
	terminal.CompletedAt = &now
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizationTimeout)
	defer cancel()
	_, _, err := executor.dependencies.Repository.FinalizeModelRun(finalizeContext, agentapplication.FinalizeModelRunCommand{
		ExpectedVersion: run.Version, Run: terminal,
	})
	return err
}

func validateExecution(execution workflowapplication.ExecutionContext) error {
	if execution.DefinitionVersion != DefinitionVersion || execution.DefinitionHash != definitionGraphHash ||
		execution.NodeKey != NodeKey || execution.NodeKind != NodeKind || execution.InputSchemaVersion != InputSchemaVersion ||
		!validHash(execution.DefinitionHash) || execution.NodeVersion < 1 || execution.AttemptNo < 1 || execution.DispatchNo < 1 ||
		(execution.ModelSettingsRevision != nil && *execution.ModelSettingsRevision < 0) ||
		strings.TrimSpace(execution.LeaseOwner) == "" ||
		!validDistinctIDs(execution.WorkspaceID, execution.DefinitionID, execution.RunID, execution.NodeRunID, execution.NodeAttemptID) {
		return inputError(errors.New("artifact generation execution binding is invalid"))
	}
	return nil
}

func buildRetrievalQuery(artifact artifactdomain.Artifact, target artifactdomain.OutlineSection) string {
	query := strings.Join([]string{artifact.Title, target.Title, artifact.ScopeDefinition}, "\n")
	if len(query) <= maxRetrievalQueryBytes {
		return query
	}
	query = query[:maxRetrievalQueryBytes]
	for !utf8.ValidString(query) {
		query = query[:len(query)-1]
	}
	return strings.TrimSpace(query)
}

func provenanceFromCitation(citation agentdomain.Citation) knowledgedomain.ProvenanceRef {
	return knowledgedomain.ProvenanceRef{
		WorkspaceID: citation.WorkspaceID, SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
	}
}

func encodeExecutionResult(receipt OutputReceipt) (workflowapplication.ExecutionResult, error) {
	encoded, err := EncodeOutputReceipt(receipt)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workflowapplication.ExecutionResult{Output: encoded}, nil
}

func evidenceError(message string) error {
	return workflowError(foundation.ErrorConsistencyViolation, ErrorCodeEvidenceInvalid, false, errors.New(message))
}

func stableErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func nilDependency(value any) bool {
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

var _ workflowapplication.Executor = (*Executor)(nil)
