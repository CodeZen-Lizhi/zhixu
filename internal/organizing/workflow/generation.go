package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const (
	maxGenerationEvidence       = 64
	maxGenerationExcerptBytes   = 4096
	maxGenerationDocuments      = 64
	maxGenerationDocumentBytes  = 64 * 1024
	maxGenerationDocumentsBytes = 320 * 1024
	generationFinalizationLimit = 5 * time.Second
	documentCapacityGap         = "DOCUMENT_CAPACITY_LIMIT"
	documentCapacityDetail      = "部分来源文档超过本次整理容量，结果只基于已明确载入的有界正文。"
)

// GeneratorDependencies 是冻结材料生成器的模型、恢复和证据依赖。
type GeneratorDependencies struct {
	Model      agentapp.ChatModel
	Catalog    *agentapp.RuntimeCatalog
	ModelRuns  agentapp.ModelRunRepository
	Store      GenerationStore
	Evidence   *EvidenceRenderer
	Documents  organizingapp.FrozenDocumentContentReader
	ProfileRef agentdomain.ModelProfileRef
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
	Budget     agentapp.RunBudget
}

// Generator 将一个 Workflow Node Attempt 绑定到唯一可重放的结构化模型输出。
type Generator struct{ dependencies GeneratorDependencies }

type unavailableGenerator struct{}

// NewUnavailableGenerator 返回显式失败的生成端口，用于未配置 Chat capability 的 Worker 组合。
func NewUnavailableGenerator() ContentGenerator { return unavailableGenerator{} }

func (unavailableGenerator) GenerateOutline(context.Context, workflowapp.ExecutionContext, organizingdomain.Snapshot, organizingdomain.TemplateRevision) (outlineReceipt, error) {
	return outlineReceipt{}, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_GENERATION_CAPABILITY_UNAVAILABLE", false, "organizing generation requires a configured chat model")
}

func (unavailableGenerator) GenerateDocument(context.Context, workflowapp.ExecutionContext, organizingdomain.Snapshot, organizingdomain.TemplateRevision, []artifactdomain.OutlineSection) (GeneratedDocument, error) {
	return GeneratedDocument{}, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_GENERATION_CAPABILITY_UNAVAILABLE", false, "organizing generation requires a configured chat model")
}

// NewGenerator 创建不允许 Tool Loop、外部知识或未记录模型调用的整理生成器。
func NewGenerator(dependencies GeneratorDependencies) (*Generator, error) {
	if nilGenerationDependency(dependencies.Model) || dependencies.Catalog == nil || nilGenerationDependency(dependencies.ModelRuns) ||
		nilGenerationDependency(dependencies.Store) || dependencies.Evidence == nil || nilGenerationDependency(dependencies.Documents) || dependencies.ProfileRef.Validate() != nil ||
		nilGenerationDependency(dependencies.IDs) || nilGenerationDependency(dependencies.Clock) {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_GENERATION_CAPABILITY_UNAVAILABLE", false, "organizing generator dependencies are incomplete")
	}
	if dependencies.Budget == (agentapp.RunBudget{}) {
		dependencies.Budget = agentapp.DefaultRunBudget()
	}
	if _, err := agentapp.NewStructuredRunner(dependencies.Model, dependencies.Catalog, dependencies.Budget); err != nil {
		return nil, err
	}
	return &Generator{dependencies: dependencies}, nil
}

type generationEvidenceInput struct {
	Label   string `json:"label"`
	Excerpt string `json:"excerpt"`
}

type generationDocumentInput struct {
	Label     string `json:"label"`
	Title     string `json:"title"`
	Markdown  string `json:"markdown"`
	Truncated bool   `json:"truncated"`
}

type generationTaskInput struct {
	Intent                 string                              `json:"intent"`
	Kind                   organizingdomain.TemplateKind       `json:"kind"`
	Sections               []artifactdomain.OutlineSection     `json:"sections"`
	Presentation           organizingdomain.PresentationPolicy `json:"presentation"`
	AdditionalInstructions string                              `json:"additional_instructions"`
	EvidenceTruncated      bool                                `json:"evidence_truncated"`
	DocumentsTruncated     bool                                `json:"documents_truncated"`
	FormalEvidence         []generationEvidenceInput           `json:"formal_evidence"`
	DocumentMaterials      []generationDocumentInput           `json:"document_materials"`
}

type generationEvidence struct {
	label string
	value verifiedEvidence
}

type generationDocument struct {
	label     string
	value     organizingapp.FrozenDocumentContent
	content   string
	truncated bool
}

// GenerateOutline 基于冻结 Evidence 生成可人工确认的大纲；无 Evidence 时不调用模型，只返回 GAP。
func (generator *Generator) GenerateOutline(
	ctx context.Context,
	execution workflowapp.ExecutionContext,
	snapshot organizingdomain.Snapshot,
	revision organizingdomain.TemplateRevision,
) (outlineReceipt, error) {
	evidence, documents, evidenceTruncated, documentsTruncated, err := generator.loadMaterials(ctx, execution, snapshot, revision)
	if err != nil {
		return outlineReceipt{}, err
	}
	if len(evidence)+len(documents) == 0 {
		return deterministicGapOutline(snapshot, revision), nil
	}
	outline := outlineFromTemplate(revision)
	record, err := generator.generate(ctx, GenerationOutline, execution, snapshot, revision, outline, evidence, documents, evidenceTruncated, documentsTruncated)
	if err != nil {
		return outlineReceipt{}, err
	}
	envelope, err := decodeOutlineEnvelope(record.Output)
	if err != nil {
		return outlineReceipt{}, err
	}
	return bindGeneratedOutline(envelope, snapshot, revision, evidence, documents)
}

// GenerateDocument 基于冻结 Evidence 与已确认大纲生成完整 Artifact 章节。
func (generator *Generator) GenerateDocument(
	ctx context.Context,
	execution workflowapp.ExecutionContext,
	snapshot organizingdomain.Snapshot,
	revision organizingdomain.TemplateRevision,
	outline []artifactdomain.OutlineSection,
) (GeneratedDocument, error) {
	evidence, documents, evidenceTruncated, documentsTruncated, err := generator.loadMaterials(ctx, execution, snapshot, revision)
	if err != nil {
		return GeneratedDocument{}, err
	}
	if len(evidence)+len(documents) == 0 {
		return deterministicGapDocument(outline), nil
	}
	record, err := generator.generate(ctx, GenerationDocument, execution, snapshot, revision, outline, evidence, documents, evidenceTruncated, documentsTruncated)
	if err != nil {
		return GeneratedDocument{}, err
	}
	envelope, err := decodeDocumentEnvelope(record.Output)
	if err != nil {
		return GeneratedDocument{}, err
	}
	metadata, err := generator.readyMetadata(ctx, record, GenerationDocument)
	if err != nil {
		return GeneratedDocument{}, err
	}
	return bindGeneratedDocument(envelope, revision, outline, evidence, documents, evidenceTruncated, documentsTruncated, metadata)
}

func (generator *Generator) loadMaterials(
	ctx context.Context,
	execution workflowapp.ExecutionContext,
	snapshot organizingdomain.Snapshot,
	revision organizingdomain.TemplateRevision,
) ([]generationEvidence, []generationDocument, bool, bool, error) {
	if generator == nil || ctx == nil || !validGenerationExecution(execution) || snapshot.Validate() != nil ||
		snapshot.WorkspaceID != execution.WorkspaceID || snapshot.TemplateRevisionID != revision.ID ||
		snapshot.TemplateHash != revision.DeclarationHash {
		return nil, nil, false, false, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_CONTEXT_INVALID", false, "organizing generation context is invalid")
	}
	opened, capacityLimited, err := generator.dependencies.Evidence.openFrozenEvidence(ctx, snapshot)
	if err != nil {
		return nil, nil, false, false, err
	}
	if len(opened) > maxGenerationEvidence {
		opened = opened[:maxGenerationEvidence]
		capacityLimited = true
	}
	evidence := make([]generationEvidence, len(opened))
	for index, item := range opened {
		evidence[index] = generationEvidence{label: fmt.Sprintf("E%03d", index+1), value: item}
	}
	references := make([]organizingdomain.MaterialRef, 0)
	documentsLimited := false
	for _, material := range snapshot.Materials {
		if material.Kind != organizingdomain.MaterialDocumentRevision {
			continue
		}
		if len(references) == maxGenerationDocuments {
			documentsLimited = true
			continue
		}
		references = append(references, material)
	}
	if len(references) == 0 {
		return evidence, []generationDocument{}, capacityLimited, documentsLimited, nil
	}
	openedDocuments, err := generator.dependencies.Documents.OpenFrozenDocumentContents(ctx, snapshot.WorkspaceID, references)
	if err != nil {
		return nil, nil, false, false, err
	}
	if len(openedDocuments) != len(references) {
		return nil, nil, false, false, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_DOCUMENT_RESULT_INVALID", false, "organizing document reader returned an incomplete batch")
	}
	documents := make([]generationDocument, 0, len(openedDocuments))
	remaining := maxGenerationDocumentsBytes
	for index, document := range openedDocuments {
		if document.DocumentID != references[index].DocumentID || document.ArticleRevisionID != references[index].ArticleRevisionID ||
			document.RevisionNo != references[index].Version || document.ContentHash != references[index].ContentHash {
			return nil, nil, false, false, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_DOCUMENT_RESULT_INVALID", false, "organizing document reader crossed the frozen material binding")
		}
		if remaining <= 0 {
			documentsLimited = true
			continue
		}
		limit := maxGenerationDocumentBytes
		if remaining < limit {
			limit = remaining
		}
		content := boundedUTF8(document.Content, limit)
		truncated := content != document.Content
		documentsLimited = documentsLimited || truncated
		remaining -= len(content)
		documents = append(documents, generationDocument{label: fmt.Sprintf("D%03d", index+1), value: document, content: content, truncated: truncated})
	}
	return evidence, documents, capacityLimited, documentsLimited, nil
}

func (generator *Generator) generate(
	ctx context.Context,
	kind GenerationKind,
	execution workflowapp.ExecutionContext,
	snapshot organizingdomain.Snapshot,
	revision organizingdomain.TemplateRevision,
	outline []artifactdomain.OutlineSection,
	evidence []generationEvidence,
	documents []generationDocument,
	evidenceTruncated bool,
	documentsTruncated bool,
) (GenerationRecord, error) {
	requestHash, err := generationRequestHash(kind, execution, snapshot, revision, outline)
	if err != nil {
		return GenerationRecord{}, err
	}
	if ready, found, lookupErr := generator.dependencies.Store.LookupReady(ctx, execution.WorkspaceID, execution.NodeRunID, kind, requestHash); lookupErr != nil {
		return GenerationRecord{}, lookupErr
	} else if found {
		if err := generator.validateReady(ctx, ready, execution, snapshot, kind, requestHash); err != nil {
			return GenerationRecord{}, err
		}
		return ready, nil
	}

	input, err := encodeGenerationInput(snapshot, revision, outline, evidence, documents, evidenceTruncated, documentsTruncated)
	if err != nil {
		return GenerationRecord{}, err
	}
	runtime, primarySchema, reducedSchema, err := generator.runtimeSnapshot(kind)
	if err != nil {
		return GenerationRecord{}, err
	}
	prepared, err := generator.prepare(ctx, kind, execution, snapshot, requestHash)
	if err != nil {
		return GenerationRecord{}, err
	}
	if prepared.Status == GenerationReady {
		if err := generator.validateReady(ctx, prepared, execution, snapshot, kind, requestHash); err != nil {
			return GenerationRecord{}, err
		}
		return prepared, nil
	}
	run, prepared, err := generator.ensureModelRun(ctx, prepared, execution, runtime, primarySchema, reducedSchema, evidence)
	if err != nil {
		return GenerationRecord{}, err
	}
	recorded, err := agentapp.NewRecordingChatModel(agentapp.RecordingChatModelDependencies{
		Model: generator.dependencies.Model, Repository: generator.dependencies.ModelRuns,
		WorkspaceID: execution.WorkspaceID, ModelRunID: run.ID,
		IDs: generator.dependencies.IDs, Clock: generator.dependencies.Clock,
	})
	if err != nil {
		return GenerationRecord{}, generator.failRun(ctx, prepared, run, err)
	}
	runner, err := agentapp.NewStructuredRunner(recorded, generator.dependencies.Catalog, generator.dependencies.Budget)
	if err != nil {
		return GenerationRecord{}, generator.failRun(ctx, prepared, run, err)
	}
	result, runErr := runner.Run(ctx, agentapp.StructuredRunRequest{
		ProfileRef: generator.dependencies.ProfileRef, PromptRef: runtime.Prompt.Ref,
		SchemaRef: primarySchema, ReducedSchemaRef: reducedSchema, Input: input,
	})
	if runErr != nil {
		return GenerationRecord{}, generator.failRun(ctx, prepared, run, runErr)
	}
	if err := validateGeneratedBinding(kind, result.Output, revision, outline, evidence, documents); err != nil {
		return GenerationRecord{}, generator.failRun(ctx, prepared, run, err)
	}
	completedAt := generator.dependencies.Clock.Now().UTC()
	if completedAt.Before(run.CreatedAt) {
		completedAt = run.CreatedAt
	}
	resultType, err := generationResultType(kind)
	if err != nil {
		return GenerationRecord{}, generator.failRun(ctx, prepared, run, err)
	}
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), generationFinalizationLimit)
	completed, _, completeErr := generator.dependencies.Store.Complete(finalizeContext, CompleteGenerationCommand{
		WorkspaceID: execution.WorkspaceID, GenerationID: prepared.ID, ExpectedVersion: prepared.Version,
		ModelRunID: run.ID, ExpectedModelRunVersion: run.Version, ResultType: resultType,
		Output: append(json.RawMessage(nil), result.Output...), OutputHash: hashBytes(result.Output), CompletedAt: completedAt,
	})
	cancel()
	if completeErr != nil {
		return GenerationRecord{}, workflowError(foundation.ErrorManualRecoveryRequired, "ORGANIZING_GENERATION_FINALIZATION_UNKNOWN", false, "organizing model output finalization could not be confirmed")
	}
	if err := generator.validateReady(ctx, completed, execution, snapshot, kind, requestHash); err != nil {
		return GenerationRecord{}, err
	}
	return completed, nil
}

func (generator *Generator) runtimeSnapshot(kind GenerationKind) (agentapp.RuntimeSnapshot, agentdomain.SchemaRef, agentdomain.SchemaRef, error) {
	var prompt agentdomain.PromptRef
	var primary, reduced agentdomain.SchemaRef
	switch kind {
	case GenerationOutline:
		prompt, primary, reduced = outlinePromptRef(), outlineSchemaRef(), outlineReducedSchemaRef()
	case GenerationDocument:
		prompt, primary, reduced = documentPromptRef(), documentSchemaRef(), documentReducedSchemaRef()
	default:
		return agentapp.RuntimeSnapshot{}, agentdomain.SchemaRef{}, agentdomain.SchemaRef{}, generationOutputError("organizing generation kind is invalid")
	}
	runtime, err := generator.dependencies.Catalog.Snapshot(prompt, primary, reduced, generator.dependencies.ProfileRef)
	return runtime, primary, reduced, err
}

func (generator *Generator) prepare(
	ctx context.Context,
	kind GenerationKind,
	execution workflowapp.ExecutionContext,
	snapshot organizingdomain.Snapshot,
	requestHash string,
) (GenerationRecord, error) {
	id, err := generator.dependencies.IDs.New()
	if err != nil {
		return GenerationRecord{}, err
	}
	now := generator.dependencies.Clock.Now().UTC()
	record := GenerationRecord{
		ID: id, WorkspaceID: execution.WorkspaceID, SnapshotID: snapshot.ID,
		WorkflowRunID: execution.RunID, NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
		Kind: kind, RequestHash: requestHash, ModelSettingsRevision: cloneRevision(execution.ModelSettingsRevision),
		Status: GenerationRunning, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	prepared, _, err := generator.dependencies.Store.Prepare(ctx, PrepareGenerationCommand{Record: record})
	if err != nil {
		return GenerationRecord{}, err
	}
	if !sameGenerationRequest(prepared, record) || prepared.Validate() != nil {
		return GenerationRecord{}, workflowError(foundation.ErrorManualRecoveryRequired, "ORGANIZING_GENERATION_REPLAY_UNSAFE", false, "organizing generation recovery binding is inconsistent")
	}
	switch prepared.Status {
	case GenerationRunning, GenerationReady:
		return prepared, nil
	case GenerationRecoveryRequired:
		return GenerationRecord{}, workflowError(foundation.ErrorManualRecoveryRequired, prepared.ErrorCode, false, "organizing generation requires explicit recovery")
	case GenerationFailed:
		return GenerationRecord{}, workflowError(foundation.ErrorVersionConflict, prepared.ErrorCode, prepared.Retryable, "organizing generation attempt is already terminal")
	default:
		return GenerationRecord{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_INVALID", false, "organizing generation status is invalid")
	}
}

func (generator *Generator) ensureModelRun(
	ctx context.Context,
	prepared GenerationRecord,
	execution workflowapp.ExecutionContext,
	runtime agentapp.RuntimeSnapshot,
	primary, reduced agentdomain.SchemaRef,
	evidence []generationEvidence,
) (agentdomain.ModelRun, GenerationRecord, error) {
	runID, err := generator.dependencies.IDs.New()
	if err != nil {
		return agentdomain.ModelRun{}, prepared, err
	}
	now := generator.dependencies.Clock.Now().UTC()
	retrieval := agentdomain.RetrievalRef{}
	if len(evidence) > 0 {
		retrieval.IndexVersionID = evidence[0].value.input.IndexVersionID
	}
	run := agentdomain.ModelRun{
		ID: runID, WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
		ModelSettingsRevision: cloneRevision(execution.ModelSettingsRevision), Model: runtime.Profile.Model,
		Profile: runtime.Profile.Ref, Prompt: runtime.Prompt.Ref, Schema: primary, ReducedSchema: reduced,
		Retrieval: retrieval,
		Status:    agentdomain.ModelRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	created, replayed, err := generator.dependencies.ModelRuns.CreateModelRun(ctx, run)
	if err != nil {
		return agentdomain.ModelRun{}, prepared, err
	}
	if !sameGenerationModelRun(created, run) || created.Status != agentdomain.ModelRunRunning || created.Version != 1 || agentdomain.ValidateModelRun(created) != nil {
		return agentdomain.ModelRun{}, prepared, workflowError(foundation.ErrorManualRecoveryRequired, "ORGANIZING_GENERATION_REPLAY_UNSAFE", false, "organizing model run binding is inconsistent")
	}
	if replayed {
		record, lookupErr := generator.dependencies.ModelRuns.GetModelRun(ctx, execution.WorkspaceID, created.ID)
		if lookupErr != nil || record.Run.Status != agentdomain.ModelRunRunning || record.Run.Version != 1 || len(record.Calls) != 0 || !sameGenerationModelRun(record.Run, run) {
			return agentdomain.ModelRun{}, prepared, workflowError(foundation.ErrorManualRecoveryRequired, "ORGANIZING_GENERATION_REPLAY_UNSAFE", false, "organizing model run contains non-replayable call facts")
		}
		created = record.Run
	}
	if prepared.ModelRunID != "" && prepared.ModelRunID != created.ID {
		return agentdomain.ModelRun{}, prepared, workflowError(foundation.ErrorManualRecoveryRequired, "ORGANIZING_GENERATION_REPLAY_UNSAFE", false, "organizing generation is bound to another model run")
	}
	if prepared.ModelRunID == "" {
		prepared, err = generator.dependencies.Store.BindModelRun(ctx, BindGenerationModelRunCommand{
			WorkspaceID: execution.WorkspaceID, GenerationID: prepared.ID,
			ExpectedVersion: prepared.Version, ModelRunID: created.ID,
		})
		if err != nil {
			return agentdomain.ModelRun{}, prepared, err
		}
	}
	if prepared.Status != GenerationRunning || prepared.ModelRunID != created.ID {
		return agentdomain.ModelRun{}, prepared, workflowError(foundation.ErrorManualRecoveryRequired, "ORGANIZING_GENERATION_REPLAY_UNSAFE", false, "organizing generation model run binding is incomplete")
	}
	return created, prepared, nil
}

func (generator *Generator) failRun(ctx context.Context, generation GenerationRecord, run agentdomain.ModelRun, cause error) error {
	status, code, retryable := generationFailure(cause)
	completedAt := generator.dependencies.Clock.Now().UTC()
	if completedAt.Before(run.CreatedAt) {
		completedAt = run.CreatedAt
	}
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), generationFinalizationLimit)
	err := generator.dependencies.Store.Fail(finalizeContext, FailGenerationCommand{
		WorkspaceID: generation.WorkspaceID, GenerationID: generation.ID, ExpectedVersion: generation.Version,
		ModelRunID: run.ID, ExpectedModelRunVersion: run.Version, ModelRunStatus: status,
		ErrorCode: code, Retryable: retryable, CompletedAt: completedAt,
	})
	cancel()
	if err != nil {
		return workflowError(foundation.ErrorManualRecoveryRequired, "ORGANIZING_GENERATION_FINALIZATION_UNKNOWN", false, "organizing generation failure could not be finalized")
	}
	return cause
}

func (generator *Generator) validateReady(
	ctx context.Context,
	record GenerationRecord,
	execution workflowapp.ExecutionContext,
	snapshot organizingdomain.Snapshot,
	kind GenerationKind,
	requestHash string,
) error {
	if record.Validate() != nil || record.Status != GenerationReady || record.WorkspaceID != execution.WorkspaceID ||
		record.SnapshotID != snapshot.ID || record.WorkflowRunID != execution.RunID || record.NodeRunID != execution.NodeRunID ||
		record.Kind != kind || record.RequestHash != requestHash {
		return workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_RESULT_INVALID", false, "recovered organizing generation differs from the workflow input")
	}
	_, err := generator.readyMetadata(ctx, record, kind)
	return err
}

func (generator *Generator) readyMetadata(ctx context.Context, record GenerationRecord, kind GenerationKind) (artifactdomain.GenerationMetadata, error) {
	modelRecord, err := generator.dependencies.ModelRuns.GetModelRun(ctx, record.WorkspaceID, record.ModelRunID)
	if err != nil {
		return artifactdomain.GenerationMetadata{}, err
	}
	resultType, _ := generationResultType(kind)
	if modelRecord.Run.Status != agentdomain.ModelRunSucceeded || modelRecord.Run.FinalResultType != resultType || len(modelRecord.Calls) == 0 {
		return artifactdomain.GenerationMetadata{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_RESULT_INVALID", false, "organizing generation model run is not a successful terminal result")
	}
	var succeeded *agentdomain.ModelCall
	for index := range modelRecord.Calls {
		call := modelRecord.Calls[index]
		if call.Status == agentdomain.ModelCallStarted || call.Status == agentdomain.ModelCallUnknown {
			return artifactdomain.GenerationMetadata{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_RESULT_INVALID", false, "organizing generation model call history is incomplete")
		}
		if call.Status == agentdomain.ModelCallSucceeded {
			copy := call
			succeeded = &copy
		}
	}
	if succeeded == nil {
		return artifactdomain.GenerationMetadata{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_RESULT_INVALID", false, "organizing generation has no successful model call")
	}
	return artifactdomain.GenerationMetadata{
		PromptVersion:             modelRecord.Run.Prompt.ID + "@" + modelRecord.Run.Prompt.Version,
		ModelVersion:              modelRecord.Run.Model.AdapterName + "/" + modelRecord.Run.Model.ModelID + "@" + modelRecord.Run.Model.ModelVersion,
		WorkflowDefinitionVersion: strconv.FormatInt(DefinitionVersion, 10),
		SchemaVersion:             succeeded.Schema.ID + "@" + succeeded.Schema.Version,
	}, nil
}

func encodeGenerationInput(
	snapshot organizingdomain.Snapshot,
	revision organizingdomain.TemplateRevision,
	outline []artifactdomain.OutlineSection,
	evidence []generationEvidence,
	documents []generationDocument,
	evidenceTruncated bool,
	documentsTruncated bool,
) ([]byte, error) {
	items := make([]generationEvidenceInput, len(evidence))
	for index, item := range evidence {
		items[index] = generationEvidenceInput{Label: item.label, Excerpt: boundedUTF8(item.value.citation.Excerpt, maxGenerationExcerptBytes)}
	}
	documentItems := make([]generationDocumentInput, len(documents))
	for index, item := range documents {
		documentItems[index] = generationDocumentInput{Label: item.label, Title: boundedUTF8(item.value.Title, maxGeneratedTitleBytes), Markdown: item.content, Truncated: item.truncated}
	}
	encoded, err := json.Marshal(generationTaskInput{
		Intent: snapshot.Intent, Kind: revision.Declaration.Kind, Sections: append([]artifactdomain.OutlineSection(nil), outline...),
		Presentation: revision.Declaration.Presentation, AdditionalInstructions: revision.Declaration.AdditionalInstructions,
		EvidenceTruncated: evidenceTruncated, DocumentsTruncated: documentsTruncated,
		FormalEvidence: items, DocumentMaterials: documentItems,
	})
	if err != nil {
		return nil, err
	}
	if len(encoded) == 0 || len(encoded) > agentapp.MaxStructuredInputBytes {
		return nil, workflowError(foundation.ErrorNonRetryableFailure, "ORGANIZING_GENERATION_INPUT_TOO_LARGE", false, "organizing generation input exceeds the bounded model context")
	}
	return encoded, nil
}

func validateGeneratedBinding(
	kind GenerationKind,
	raw json.RawMessage,
	revision organizingdomain.TemplateRevision,
	outline []artifactdomain.OutlineSection,
	evidence []generationEvidence,
	documents []generationDocument,
) error {
	switch kind {
	case GenerationOutline:
		envelope, err := decodeOutlineEnvelope(raw)
		if err != nil {
			return err
		}
		_, err = bindGeneratedOutline(envelope, organizingdomain.Snapshot{ID: "00000000-0000-4000-8000-000000000001", Hash: hashBytes([]byte("binding"))}, revision, evidence, documents)
		return err
	case GenerationDocument:
		envelope, err := decodeDocumentEnvelope(raw)
		if err != nil {
			return err
		}
		_, err = bindGeneratedDocument(envelope, revision, outline, evidence, documents, false, false, artifactdomain.GenerationMetadata{
			PromptVersion: "binding/v1", ModelVersion: "binding/v1", WorkflowDefinitionVersion: "1", SchemaVersion: "binding/v1",
		})
		return err
	default:
		return generationOutputError("organizing generation kind is invalid")
	}
}

func bindGeneratedOutline(
	envelope generatedOutlineEnvelope,
	snapshot organizingdomain.Snapshot,
	revision organizingdomain.TemplateRevision,
	evidence []generationEvidence,
	documents []generationDocument,
) (outlineReceipt, error) {
	if len(envelope.Payload.Sections) != len(revision.Declaration.Sections) {
		return outlineReceipt{}, generationOutputError("organizing outline does not contain every requested section")
	}
	byLabel := generationEvidenceByLabel(evidence)
	documentsByLabel := generationDocumentsByLabel(documents)
	items := make([]outlineItem, len(envelope.Payload.Sections))
	for index, generated := range envelope.Payload.Sections {
		requested := revision.Declaration.Sections[index]
		if generated.Key != requested.Key {
			return outlineReceipt{}, generationOutputError("organizing outline section order differs from the template")
		}
		item := outlineItem{Key: generated.Key, Title: generated.Title, GapCode: generated.GapCode, Supports: []outlineSupport{}}
		for _, label := range generated.EvidenceLabels {
			value, found := byLabel[label]
			if !found {
				return outlineReceipt{}, generationOutputError("organizing outline cites an unknown evidence label")
			}
			item.Supports = append(item.Supports, outlineSupport{
				Kind:            organizingdomain.MaterialSourceVersion,
				SourceVersionID: value.citation.SourceVersionID, SourceSpanID: value.citation.SourceSpanID,
				ContentHash: value.citation.VerifiedContentHash, ExcerptHash: hashText(value.citation.Excerpt),
			})
		}
		for _, label := range generated.DocumentLabels {
			value, found := documentsByLabel[label]
			if !found {
				return outlineReceipt{}, generationOutputError("organizing outline cites an unknown document label")
			}
			item.Supports = append(item.Supports, outlineSupport{
				Kind: organizingdomain.MaterialDocumentRevision, DocumentID: value.value.DocumentID,
				ArticleRevisionID: value.value.ArticleRevisionID, RevisionNo: value.value.RevisionNo,
				ContentHash: value.value.ContentHash,
			})
		}
		items[index] = item
	}
	return outlineReceipt{
		SchemaVersion: receiptSchemaV1, SnapshotID: snapshot.ID, SnapshotHash: snapshot.Hash,
		TemplateRevisionID: revision.ID, TemplateHash: revision.DeclarationHash, Outline: items,
	}, nil
}

func bindGeneratedDocument(
	envelope generatedDocumentEnvelope,
	revision organizingdomain.TemplateRevision,
	outline []artifactdomain.OutlineSection,
	evidence []generationEvidence,
	documents []generationDocument,
	evidenceTruncated bool,
	documentsTruncated bool,
	metadata artifactdomain.GenerationMetadata,
) (GeneratedDocument, error) {
	if len(outline) == 0 || len(envelope.Payload.Sections) != len(outline) {
		return GeneratedDocument{}, generationOutputError("organizing document does not contain every requested section")
	}
	byLabel := generationEvidenceByLabel(evidence)
	documentsByLabel := generationDocumentsByLabel(documents)
	sections := make([]artifactapp.SectionInput, len(outline))
	for index, generated := range envelope.Payload.Sections {
		requested := outline[index]
		if generated.Key != requested.Key {
			return GeneratedDocument{}, generationOutputError("organizing document section order differs from the approved outline")
		}
		citations := make([]artifactapp.CitationInput, len(generated.EvidenceLabels))
		for labelIndex, label := range generated.EvidenceLabels {
			value, found := byLabel[label]
			if !found {
				return GeneratedDocument{}, generationOutputError("organizing document cites an unknown evidence label")
			}
			citations[labelIndex] = value.input
		}
		documentSources := make([]artifactapp.DocumentSourceInput, len(generated.DocumentLabels))
		for labelIndex, label := range generated.DocumentLabels {
			value, found := documentsByLabel[label]
			if !found {
				return GeneratedDocument{}, generationOutputError("organizing document cites an unknown document label")
			}
			documentSources[labelIndex] = artifactapp.DocumentSourceInput{
				DocumentID: value.value.DocumentID, ArticleRevisionID: value.value.ArticleRevisionID,
				RevisionNo: value.value.RevisionNo, ContentHash: value.value.ContentHash,
			}
		}
		gaps := make([]artifactdomain.Gap, len(generated.Gaps))
		for gapIndex, gap := range generated.Gaps {
			gaps[gapIndex] = artifactdomain.Gap{Code: gap.Code, Description: gap.Description}
		}
		status := generated.CoverageStatus
		if evidenceTruncated && status != artifactdomain.CoverageGap {
			status = artifactdomain.CoveragePartial
			gaps = appendGap(gaps, artifactdomain.Gap{Code: evidenceCapacityGap, Description: evidenceCapacityDetail})
		}
		if documentsTruncated && status != artifactdomain.CoverageGap {
			status = artifactdomain.CoveragePartial
			gaps = appendGap(gaps, artifactdomain.Gap{Code: documentCapacityGap, Description: documentCapacityDetail})
		}
		sections[index] = artifactapp.SectionInput{
			Key: requested.Key, Title: requested.Title, Content: generated.Content, Citations: citations, DocumentSources: documentSources,
			Coverage: artifactdomain.Coverage{SectionKey: requested.Key, Status: status, Gaps: gaps},
		}
	}
	if revision.Declaration.Kind != organizingdomain.TemplateMergeDocuments && len(envelope.Payload.Comparisons) != 0 {
		return GeneratedDocument{}, generationOutputError("non-merge organizing output contains merge comparisons")
	}
	comparison, err := bindGeneratedComparison(envelope.Payload.Comparisons, evidence, documents)
	if err != nil {
		return GeneratedDocument{}, err
	}
	return GeneratedDocument{Sections: sections, Comparison: comparison, Metadata: metadata}, nil
}

func bindGeneratedComparison(values []generatedComparison, evidence []generationEvidence, documents []generationDocument) (mergeComparison, error) {
	categoryByLabel := make(map[string]string, len(evidence)+len(documents))
	for _, item := range evidence {
		categoryByLabel[item.label] = mergeCategoryUnique
	}
	for _, item := range documents {
		categoryByLabel[item.label] = mergeCategoryUnique
	}
	for _, comparison := range values {
		labels := append(append([]string{}, comparison.EvidenceLabels...), comparison.DocumentLabels...)
		for _, label := range labels {
			current, found := categoryByLabel[label]
			if !found {
				return mergeComparison{}, generationOutputError("organizing comparison cites an unknown evidence label")
			}
			if mergeCategoryPriority(comparison.Category) > mergeCategoryPriority(current) {
				categoryByLabel[label] = comparison.Category
			}
		}
	}
	result := mergeComparison{
		EvidenceCount: len(evidence),
		DocumentCount: len(documents),
		Categories:    []mergeCategoryCount{{Category: mergeCategoryDuplicate}, {Category: mergeCategoryComplementary}, {Category: mergeCategoryConflict}, {Category: mergeCategoryUnique}},
		Preview:       []mergeComparisonEntry{},
		byCategory:    map[string][]verifiedEvidence{mergeCategoryDuplicate: {}, mergeCategoryComplementary: {}, mergeCategoryConflict: {}, mergeCategoryUnique: {}},
	}
	for _, item := range evidence {
		category := categoryByLabel[item.label]
		result.byCategory[category] = append(result.byCategory[category], item.value)
		for index := range result.Categories {
			if result.Categories[index].Category == category {
				result.Categories[index].Count++
			}
		}
		if len(result.Preview) < mergePreviewLimit {
			result.Preview = append(result.Preview, mergeComparisonEntry{
				Category: category, Kind: organizingdomain.MaterialSourceVersion, SourceVersionID: item.value.citation.SourceVersionID,
				SourceSpanID: item.value.citation.SourceSpanID, ContentHash: item.value.citation.VerifiedContentHash,
				ExcerptHash: hashText(item.value.citation.Excerpt),
			})
		}
	}
	for _, item := range documents {
		category := categoryByLabel[item.label]
		for index := range result.Categories {
			if result.Categories[index].Category == category {
				result.Categories[index].Count++
			}
		}
		if len(result.Preview) < mergePreviewLimit {
			result.Preview = append(result.Preview, mergeComparisonEntry{
				Category: category, Kind: organizingdomain.MaterialDocumentRevision,
				DocumentID: item.value.DocumentID, ArticleRevisionID: item.value.ArticleRevisionID,
				RevisionNo: item.value.RevisionNo, ContentHash: item.value.ContentHash,
			})
		}
	}
	return result, nil
}

func deterministicGapOutline(snapshot organizingdomain.Snapshot, revision organizingdomain.TemplateRevision) outlineReceipt {
	items := make([]outlineItem, len(revision.Declaration.Sections))
	for index, section := range revision.Declaration.Sections {
		items[index] = outlineItem{Key: section.Key, Title: section.Title, Supports: []outlineSupport{}, GapCode: evidenceUnavailableGap}
	}
	return outlineReceipt{
		SchemaVersion: receiptSchemaV1, SnapshotID: snapshot.ID, SnapshotHash: snapshot.Hash,
		TemplateRevisionID: revision.ID, TemplateHash: revision.DeclarationHash, Outline: items,
	}
}

func deterministicGapDocument(outline []artifactdomain.OutlineSection) GeneratedDocument {
	sections := make([]artifactapp.SectionInput, len(outline))
	for index, item := range outline {
		sections[index] = artifactapp.SectionInput{
			Key: item.Key, Title: item.Title, Citations: []artifactapp.CitationInput{}, DocumentSources: []artifactapp.DocumentSourceInput{},
			Coverage: artifactdomain.Coverage{SectionKey: item.Key, Status: artifactdomain.CoverageGap,
				Gaps: []artifactdomain.Gap{{Code: evidenceUnavailableGap, Description: evidenceUnavailableDetail}}},
		}
	}
	return GeneratedDocument{
		Sections:   sections,
		Comparison: mergeComparison{Categories: []mergeCategoryCount{{Category: mergeCategoryDuplicate}, {Category: mergeCategoryComplementary}, {Category: mergeCategoryConflict}, {Category: mergeCategoryUnique}}, Preview: []mergeComparisonEntry{}, byCategory: map[string][]verifiedEvidence{}},
		Metadata: artifactdomain.GenerationMetadata{
			PromptVersion: "organizing.no-evidence@v1", ModelVersion: "deterministic/gap@v1",
			WorkflowDefinitionVersion: strconv.FormatInt(DefinitionVersion, 10), SchemaVersion: "organizing-document-gap@v1",
		},
	}
}

func generationEvidenceByLabel(values []generationEvidence) map[string]verifiedEvidence {
	result := make(map[string]verifiedEvidence, len(values))
	for _, value := range values {
		result[value.label] = value.value
	}
	return result
}

func generationDocumentsByLabel(values []generationDocument) map[string]generationDocument {
	result := make(map[string]generationDocument, len(values))
	for _, value := range values {
		result[value.label] = value
	}
	return result
}

func appendGap(values []artifactdomain.Gap, gap artifactdomain.Gap) []artifactdomain.Gap {
	for _, value := range values {
		if value.Code == gap.Code {
			return values
		}
	}
	return append(values, gap)
}

func validGenerationExecution(execution workflowapp.ExecutionContext) bool {
	return validID(execution.WorkspaceID) && validID(execution.RunID) && validID(execution.NodeRunID) &&
		validID(execution.NodeAttemptID) && execution.NodeKey == execution.NodeKind &&
		(execution.ModelSettingsRevision == nil || *execution.ModelSettingsRevision >= 0)
}

func sameGenerationRequest(left, right GenerationRecord) bool {
	return left.WorkspaceID == right.WorkspaceID && left.SnapshotID == right.SnapshotID &&
		left.WorkflowRunID == right.WorkflowRunID && left.NodeRunID == right.NodeRunID &&
		left.NodeAttemptID == right.NodeAttemptID && left.Kind == right.Kind && left.RequestHash == right.RequestHash &&
		sameRevision(left.ModelSettingsRevision, right.ModelSettingsRevision)
}

func sameGenerationModelRun(left, right agentdomain.ModelRun) bool {
	return left.WorkspaceID == right.WorkspaceID && left.WorkflowRunID == right.WorkflowRunID &&
		left.NodeRunID == right.NodeRunID && left.NodeAttemptID == right.NodeAttemptID &&
		sameRevision(left.ModelSettingsRevision, right.ModelSettingsRevision) && left.Model == right.Model &&
		left.Profile == right.Profile && left.Prompt == right.Prompt && left.Schema == right.Schema &&
		left.ReducedSchema == right.ReducedSchema && left.Retrieval.IndexVersionID == right.Retrieval.IndexVersionID
}

func sameRevision(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func cloneRevision(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func generationFailure(err error) (agentdomain.ModelRunStatus, string, bool) {
	status := agentdomain.ModelRunFailed
	code := "ORGANIZING_GENERATION_FAILED"
	retryable := false
	var classified *foundation.Error
	if errors.As(err, &classified) {
		if validGenerationErrorCode(classified.Code) {
			code = classified.Code
		}
		retryable = classified.Retryable
		if classified.Kind == foundation.ErrorManualRecoveryRequired || classified.Code == agentapp.ErrorCodeModelCallPersistenceUnknown ||
			classified.Code == agentapp.ErrorCodeModelCallReplayUnsafe {
			status = agentdomain.ModelRunUnknown
			retryable = false
		}
	}
	return status, code, retryable
}

func nilGenerationDependency(value any) bool {
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
