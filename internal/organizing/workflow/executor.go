package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	humanTaskExpiry     = 7 * 24 * time.Hour
	receiptSchemaV1     = 1
	maxDiffPreviewBytes = 32 * 1024
)

var (
	outlineApprovalSchema   = json.RawMessage(`{"type":"object","required":["approved"],"properties":{"approved":{"type":"boolean"}},"additionalProperties":false}`)
	mergeConfirmationSchema = json.RawMessage(`{"type":"object","required":["approved"],"properties":{"approved":{"type":"boolean"},"target_path":{"type":"string"}},"additionalProperties":false}`)
)

// WorkflowRunReader reloads the server-owned Run and its original input.
type WorkflowRunReader interface {
	GetRun(context.Context, foundation.ID) (workflowdomain.Run, error)
}

// SnapshotReader reloads immutable Organizing input facts.
type SnapshotReader interface {
	GetSnapshot(context.Context, foundation.ID, foundation.ID) (organizingdomain.Snapshot, error)
}

// TemplateReader reloads the immutable selected Template Revision.
type TemplateReader interface {
	GetFrozenTemplateRevision(context.Context, foundation.ID, foundation.ID, foundation.ID) (organizingapp.TemplateDetail, error)
}

// RunBindingStore proves the immutable Run/Snapshot binding.
type RunBindingStore interface {
	GetRunBinding(context.Context, foundation.ID, foundation.ID) (organizingdomain.RunBinding, error)
}

// StageOutputReader reads a succeeded node output through the Workflow owner.
type StageOutputReader interface {
	GetSucceededNodeOutput(context.Context, foundation.ID, foundation.ID, string) (json.RawMessage, error)
}

// ArtifactResultOwner is the Artifact owner surface used by the executor.
type ArtifactResultOwner interface {
	Create(context.Context, ArtifactCreateRequest) (ArtifactReceipt, error)
	GetExact(context.Context, foundation.ID, ArtifactReceipt) (artifactapp.State, error)
}

// ProposalResultOwner is the Change Control owner surface used after human confirmation.
type ProposalResultOwner interface {
	CreateMergeProposal(context.Context, foundation.ID, foundation.ID, string, artifactapp.State) (ProposalReceipt, error)
}

// ExecutorDependencies are the owner-level ports required by all fixed nodes.
type ExecutorDependencies struct {
	Runs      WorkflowRunReader
	Snapshots SnapshotReader
	Templates TemplateReader
	Bindings  RunBindingStore
	Stages    StageOutputReader
	Artifacts ArtifactResultOwner
	Proposals ProposalResultOwner
	Generator ContentGenerator
	IDs       foundation.IDGenerator
}

// Executor implements all fixed Organizing node contracts.
type Executor struct{ dependencies ExecutorDependencies }

// NewExecutor constructs the fixed Organizing Workflow executor.
func NewExecutor(dependencies ExecutorDependencies) (*Executor, error) {
	if dependencies.Runs == nil || dependencies.Snapshots == nil || dependencies.Templates == nil || dependencies.Bindings == nil ||
		dependencies.Stages == nil || dependencies.Artifacts == nil || dependencies.Proposals == nil || dependencies.Generator == nil ||
		dependencies.IDs == nil {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_EXECUTOR_UNAVAILABLE", true, "organizing executor dependencies are incomplete")
	}
	return &Executor{dependencies: dependencies}, nil
}

type runContext struct {
	run      workflowdomain.Run
	binding  organizingdomain.RunBinding
	snapshot organizingdomain.Snapshot
	template organizingdomain.TemplateRevision
	compiled organizingdomain.CompiledTemplate
}

type outlineItem struct {
	Key      string           `json:"key"`
	Title    string           `json:"title"`
	Supports []outlineSupport `json:"supports"`
	GapCode  string           `json:"gap_code,omitempty"`
}

// outlineSupport is copied from the frozen Snapshot. It proves an outline
// entry is grounded in concrete material before a reviewer approves it.
type outlineSupport struct {
	Kind              organizingdomain.MaterialKind `json:"kind"`
	SourceVersionID   foundation.ID                 `json:"source_version_id,omitempty"`
	SourceSpanID      foundation.ID                 `json:"source_span_id,omitempty"`
	DocumentID        foundation.ID                 `json:"document_id,omitempty"`
	ArticleRevisionID foundation.ID                 `json:"article_revision_id,omitempty"`
	RevisionNo        int64                         `json:"revision_no,omitempty"`
	ContentHash       string                        `json:"content_hash"`
	ExcerptHash       string                        `json:"excerpt_hash,omitempty"`
}

type outlineReceipt struct {
	SchemaVersion      int           `json:"schema_version"`
	SnapshotID         foundation.ID `json:"snapshot_id"`
	SnapshotHash       string        `json:"snapshot_hash"`
	TemplateRevisionID foundation.ID `json:"template_revision_id"`
	TemplateHash       string        `json:"template_hash"`
	Outline            []outlineItem `json:"outline"`
}

type mergeStageReceipt struct {
	SchemaVersion     int                    `json:"schema_version"`
	SnapshotID        foundation.ID          `json:"snapshot_id"`
	SnapshotHash      string                 `json:"snapshot_hash"`
	ArtifactID        foundation.ID          `json:"artifact_id"`
	RevisionHash      string                 `json:"revision_hash"`
	DefaultTargetPath string                 `json:"default_target_path"`
	DiffHash          string                 `json:"diff_hash"`
	DiffPreview       string                 `json:"diff_preview"`
	DiffTruncated     bool                   `json:"diff_truncated"`
	ConflictCount     int                    `json:"conflict_count"`
	EvidenceCount     int                    `json:"evidence_count"`
	DocumentCount     int                    `json:"document_count"`
	Categories        []mergeCategoryCount   `json:"categories"`
	Comparison        []mergeComparisonEntry `json:"comparison"`
}

type approvalDecision struct {
	Approved bool `json:"approved"`
}

type mergeDecision struct {
	Approved   bool   `json:"approved"`
	TargetPath string `json:"target_path"`
}

type finalReceipt struct {
	SchemaVersion int                         `json:"schema_version"`
	ResultID      foundation.ID               `json:"result_id"`
	RunBindingID  foundation.ID               `json:"run_binding_id"`
	SnapshotID    foundation.ID               `json:"snapshot_id"`
	Kind          organizingdomain.ResultKind `json:"kind"`
	ResultRef     foundation.ID               `json:"result_ref"`
	ResultHash    string                      `json:"result_hash"`
}

// Execute dispatches exactly one registered Organizing node.
func (executor *Executor) Execute(ctx context.Context, execution workflowapp.ExecutionContext) (workflowapp.ExecutionResult, error) {
	if executor == nil || ctx == nil {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_EXECUTOR_UNAVAILABLE", true, "organizing executor is unavailable")
	}
	expectedDefinition, ok := definitionForNode(execution.NodeKind)
	if !ok {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorInvalidInput, "ORGANIZING_NODE_UNREGISTERED", false, "organizing node kind is not registered")
	}
	loaded, err := executor.loadAuthoritativeContext(ctx, execution, expectedDefinition)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	switch execution.NodeKind {
	case TopicOutlineNodeKind:
		return executor.executeTopicOutline(ctx, execution, loaded)
	case TopicOutlineApprovalNodeKind:
		return executor.executeTopicOutlineApproval(execution, loaded)
	case TopicArtifactNodeKind:
		return executor.executeTopicArtifact(ctx, execution, loaded)
	case MergeCompareNodeKind:
		return executor.executeMergeCompare(ctx, execution, loaded)
	case MergeConfirmationNodeKind:
		return executor.executeMergeConfirmation(execution, loaded)
	case MergeProposalNodeKind:
		return executor.executeMergeProposal(ctx, execution, loaded)
	case KnowledgeReportNodeKind, InterviewReviewNodeKind:
		return executor.executeDirectArtifact(ctx, execution, loaded)
	default:
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorInvalidInput, "ORGANIZING_NODE_UNREGISTERED", false, "organizing node kind is not registered")
	}
}

func (executor *Executor) loadAuthoritativeContext(ctx context.Context, execution workflowapp.ExecutionContext, expectedDefinition string) (runContext, error) {
	if !validID(execution.WorkspaceID) || !validID(execution.DefinitionID) || !validID(execution.RunID) ||
		!validID(execution.NodeRunID) || !validID(execution.NodeAttemptID) || execution.DefinitionVersion != DefinitionVersion ||
		execution.InputSchemaVersion != InputSchemaVersion || execution.NodeKey != execution.NodeKind || !validHash(execution.DefinitionHash) {
		return runContext{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_EXECUTION_CONTEXT_INVALID", false, "workflow execution context is not bound to the registered node")
	}
	run, err := executor.dependencies.Runs.GetRun(ctx, execution.RunID)
	if err != nil {
		return runContext{}, err
	}
	if run.ID != execution.RunID || run.WorkspaceID != execution.WorkspaceID || run.DefinitionID != execution.DefinitionID {
		return runContext{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_WORKFLOW_RUN_INVALID", false, "workflow run does not match the execution context")
	}
	start, err := decodeStartInput(run.Input)
	if err != nil {
		return runContext{}, err
	}
	if run.IdempotencyKey != organizingapp.StartIdempotencyKey(start.SnapshotID) {
		return runContext{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_WORKFLOW_RUN_INVALID", false, "workflow run uses a conflicting snapshot idempotency key")
	}
	binding, err := executor.dependencies.Bindings.GetRunBinding(ctx, execution.WorkspaceID, start.SnapshotID)
	if err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) && classified.Kind == foundation.ErrorNotFound {
			return runContext{}, workflowError(foundation.ErrorRetryableFailure, "ORGANIZING_RUN_BINDING_PENDING", true, "organizing run binding is not visible yet")
		}
		return runContext{}, err
	}
	if binding.WorkflowRunID != execution.RunID || binding.WorkspaceID != execution.WorkspaceID ||
		binding.SnapshotID != start.SnapshotID || binding.DefinitionKey != expectedDefinition || binding.DefinitionVersion != DefinitionVersion {
		return runContext{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_RUN_BINDING_INVALID", false, "organizing run binding conflicts with the workflow run")
	}
	snapshot, err := executor.dependencies.Snapshots.GetSnapshot(ctx, execution.WorkspaceID, start.SnapshotID)
	if err != nil {
		return runContext{}, err
	}
	if snapshot.ID != start.SnapshotID || snapshot.WorkspaceID != execution.WorkspaceID || snapshot.Validate() != nil {
		return runContext{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_SNAPSHOT_RELOAD_INVALID", false, "organizing snapshot reload is invalid")
	}
	detail, err := executor.dependencies.Templates.GetFrozenTemplateRevision(
		ctx, execution.WorkspaceID, snapshot.TemplateID, snapshot.TemplateRevisionID,
	)
	if err != nil {
		return runContext{}, err
	}
	revision := detail.Revision
	if detail.Template.ID != snapshot.TemplateID || revision.ID != snapshot.TemplateRevisionID ||
		revision.TemplateID != snapshot.TemplateID || revision.DeclarationHash != snapshot.TemplateHash {
		return runContext{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_TEMPLATE_RELOAD_INVALID", false, "organizing template revision differs from the snapshot")
	}
	compiled, err := organizingdomain.Compile(revision)
	if err != nil {
		return runContext{}, err
	}
	if compiled.DefinitionKey != expectedDefinition || compiled.DefinitionVersion != DefinitionVersion || compiled.DeclarationHash != snapshot.TemplateHash {
		return runContext{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_TEMPLATE_COMPILE_INVALID", false, "compiled template conflicts with the registered workflow")
	}
	return runContext{run: run, binding: binding, snapshot: snapshot, template: revision, compiled: compiled}, nil
}

func (executor *Executor) executeTopicOutline(ctx context.Context, execution workflowapp.ExecutionContext, loaded runContext) (workflowapp.ExecutionResult, error) {
	start, err := decodeStartInput(execution.Input)
	if err != nil || start.SnapshotID != loaded.snapshot.ID {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_NODE_INPUT_INVALID", false, "topic outline input does not match the workflow snapshot")
	}
	receipt, err := executor.dependencies.Generator.GenerateOutline(ctx, execution, loaded.snapshot, loaded.template)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	output, err := json.Marshal(receipt)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	return workflowapp.ExecutionResult{Output: output}, nil
}

func (executor *Executor) executeTopicOutlineApproval(execution workflowapp.ExecutionContext, loaded runContext) (workflowapp.ExecutionResult, error) {
	receipt, err := decodeOutlineReceipt(execution.Input)
	if err != nil || !validOutlineReceiptBinding(receipt, loaded) {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_OUTLINE_RECEIPT_INVALID", false, "topic outline receipt differs from the immutable template")
	}
	return executor.humanWait(outlineApprovalSchema)
}

func (executor *Executor) executeTopicArtifact(ctx context.Context, execution workflowapp.ExecutionContext, loaded runContext) (workflowapp.ExecutionResult, error) {
	decision, err := decodeApproval(execution.Input)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if !decision.Approved {
		return workflowapp.ExecutionResult{}, humanRejected()
	}
	stage, err := executor.dependencies.Stages.GetSucceededNodeOutput(ctx, loaded.snapshot.WorkspaceID, execution.RunID, TopicOutlineNodeKind)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	receipt, err := decodeOutlineReceipt(stage)
	if err != nil || !validOutlineReceiptBinding(receipt, loaded) {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_OUTLINE_RECEIPT_INVALID", false, "reviewed topic outline differs from the immutable template")
	}
	return executor.createAndBindArtifact(ctx, execution, loaded, outlineFromReceipt(receipt))
}

func (executor *Executor) executeMergeCompare(ctx context.Context, execution workflowapp.ExecutionContext, loaded runContext) (workflowapp.ExecutionResult, error) {
	start, err := decodeStartInput(execution.Input)
	if err != nil || start.SnapshotID != loaded.snapshot.ID {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_NODE_INPUT_INVALID", false, "merge comparison input does not match the workflow snapshot")
	}
	defaultTargetPath, err := defaultOutputPath(loaded.snapshot, loaded.template)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	generated, err := executor.dependencies.Generator.GenerateDocument(ctx, execution, loaded.snapshot, loaded.template, outlineFromTemplate(loaded.template))
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	artifactReceipt, err := executor.dependencies.Artifacts.Create(ctx, ArtifactCreateRequest{
		RunID: execution.RunID, Snapshot: loaded.snapshot, Template: loaded.template,
		DefaultTargetPath: defaultTargetPath, Outline: outlineFromTemplate(loaded.template),
		Sections: generated.Sections, Metadata: generated.Metadata,
	})
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	state, err := executor.dependencies.Artifacts.GetExact(ctx, loaded.snapshot.WorkspaceID, artifactReceipt)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	markdown, err := RenderArtifactMarkdown(state)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	diff := RenderCreateOnlyDiff(markdown)
	diffPreview, diffTruncated := boundedDiffPreview(diff)
	receipt := mergeStageReceipt{
		SchemaVersion: receiptSchemaV1, SnapshotID: loaded.snapshot.ID, SnapshotHash: loaded.snapshot.Hash,
		ArtifactID: artifactReceipt.ArtifactID, RevisionHash: artifactReceipt.RevisionHash,
		DefaultTargetPath: defaultTargetPath, DiffHash: hashText(diff), DiffPreview: diffPreview, DiffTruncated: diffTruncated,
		ConflictCount: mergeConflictCount(generated.Comparison), EvidenceCount: generated.Comparison.EvidenceCount, DocumentCount: generated.Comparison.DocumentCount,
		Categories: append([]mergeCategoryCount(nil), generated.Comparison.Categories...),
		Comparison: append([]mergeComparisonEntry(nil), generated.Comparison.Preview...),
	}
	output, err := json.Marshal(receipt)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	return workflowapp.ExecutionResult{Output: output}, nil
}

func (executor *Executor) executeMergeConfirmation(execution workflowapp.ExecutionContext, loaded runContext) (workflowapp.ExecutionResult, error) {
	receipt, err := decodeMergeReceipt(execution.Input)
	if err != nil || !mergeReceiptMatchesLoaded(receipt, loaded) {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_MERGE_RECEIPT_INVALID", false, "merge comparison receipt differs from the immutable snapshot")
	}
	return executor.humanWait(mergeConfirmationSchema)
}

func (executor *Executor) executeMergeProposal(ctx context.Context, execution workflowapp.ExecutionContext, loaded runContext) (workflowapp.ExecutionResult, error) {
	decision, err := decodeMergeDecision(execution.Input)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if !decision.Approved {
		return workflowapp.ExecutionResult{}, humanRejected()
	}
	stage, err := executor.dependencies.Stages.GetSucceededNodeOutput(ctx, loaded.snapshot.WorkspaceID, execution.RunID, MergeCompareNodeKind)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	receipt, err := decodeMergeReceipt(stage)
	if err != nil || !mergeReceiptMatchesLoaded(receipt, loaded) {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_MERGE_RECEIPT_INVALID", false, "reviewed merge receipt differs from the immutable snapshot")
	}
	state, err := executor.dependencies.Artifacts.GetExact(ctx, loaded.snapshot.WorkspaceID, ArtifactReceipt{ArtifactID: receipt.ArtifactID, RevisionHash: receipt.RevisionHash})
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	markdown, err := RenderArtifactMarkdown(state)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if hashText(RenderCreateOnlyDiff(markdown)) != receipt.DiffHash {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_MERGE_DIFF_DRIFT", false, "reviewed merge diff differs from the Artifact revision")
	}
	proposal, err := executor.dependencies.Proposals.CreateMergeProposal(ctx, loaded.snapshot.WorkspaceID, execution.RunID, decision.TargetPath, state)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	return executor.resultReceipt(execution, loaded, organizingdomain.ResultMergeProposal, proposal.ProposalID, proposal.ChangeHash)
}

func (executor *Executor) executeDirectArtifact(ctx context.Context, execution workflowapp.ExecutionContext, loaded runContext) (workflowapp.ExecutionResult, error) {
	start, err := decodeStartInput(execution.Input)
	if err != nil || start.SnapshotID != loaded.snapshot.ID {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_NODE_INPUT_INVALID", false, "artifact input does not match the workflow snapshot")
	}
	return executor.createAndBindArtifact(ctx, execution, loaded, outlineFromTemplate(loaded.template))
}

func (executor *Executor) createAndBindArtifact(ctx context.Context, execution workflowapp.ExecutionContext, loaded runContext, outline []artifactdomain.OutlineSection) (workflowapp.ExecutionResult, error) {
	defaultTargetPath, err := defaultOutputPath(loaded.snapshot, loaded.template)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	generated, err := executor.dependencies.Generator.GenerateDocument(ctx, execution, loaded.snapshot, loaded.template, outline)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	receipt, err := executor.dependencies.Artifacts.Create(ctx, ArtifactCreateRequest{
		RunID: execution.RunID, Snapshot: loaded.snapshot, Template: loaded.template,
		DefaultTargetPath: defaultTargetPath, Outline: outline, Sections: generated.Sections, Metadata: generated.Metadata,
	})
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	return executor.resultReceipt(execution, loaded, organizingdomain.ResultArtifact, receipt.ArtifactID, receipt.RevisionHash)
}

func (executor *Executor) resultReceipt(execution workflowapp.ExecutionContext, loaded runContext, kind organizingdomain.ResultKind, reference foundation.ID, resultHash string) (workflowapp.ExecutionResult, error) {
	if loaded.compiled.ResultKind != kind || !validID(reference) || !validHash(resultHash) {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_RESULT_OWNER_INVALID", false, "owner result conflicts with the compiled template")
	}
	resultID, err := executor.dependencies.IDs.New()
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	output, err := json.Marshal(finalReceipt{
		SchemaVersion: receiptSchemaV1, ResultID: resultID, RunBindingID: loaded.binding.ID,
		SnapshotID: loaded.snapshot.ID, Kind: kind, ResultRef: reference, ResultHash: resultHash,
	})
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	return workflowapp.ExecutionResult{Output: output}, nil
}

func (executor *Executor) humanWait(schema json.RawMessage) (workflowapp.ExecutionResult, error) {
	taskID, err := executor.dependencies.IDs.New()
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	return workflowapp.ExecutionResult{HumanWait: &workflowapp.HumanWaitResult{
		TaskID: taskID, ExpectedInputSchema: append(json.RawMessage(nil), schema...), TargetVersion: 1, ExpiresIn: humanTaskExpiry,
	}}, nil
}

func newOutlineReceipt(loaded runContext) outlineReceipt {
	outline := make([]outlineItem, len(loaded.template.Declaration.Sections))
	supports := frozenOutlineSupports(loaded.snapshot)
	for index, section := range loaded.template.Declaration.Sections {
		item := outlineItem{Key: section.Key, Title: section.Title, Supports: []outlineSupport{}}
		for supportIndex, support := range supports {
			if supportIndex%len(outline) == index && len(item.Supports) < maxCitationsPerSection {
				item.Supports = append(item.Supports, support)
			}
		}
		if len(item.Supports) == 0 {
			item.GapCode = evidenceUnavailableGap
		}
		outline[index] = item
	}
	return outlineReceipt{
		SchemaVersion: receiptSchemaV1, SnapshotID: loaded.snapshot.ID, SnapshotHash: loaded.snapshot.Hash,
		TemplateRevisionID: loaded.template.ID, TemplateHash: loaded.template.DeclarationHash, Outline: outline,
	}
}

func sameOutlineReceipt(left, right outlineReceipt) bool {
	if left.SchemaVersion != right.SchemaVersion || left.SnapshotID != right.SnapshotID || left.SnapshotHash != right.SnapshotHash ||
		left.TemplateRevisionID != right.TemplateRevisionID || left.TemplateHash != right.TemplateHash || len(left.Outline) != len(right.Outline) {
		return false
	}
	for index := range left.Outline {
		if left.Outline[index].Key != right.Outline[index].Key || left.Outline[index].Title != right.Outline[index].Title ||
			left.Outline[index].GapCode != right.Outline[index].GapCode || len(left.Outline[index].Supports) != len(right.Outline[index].Supports) {
			return false
		}
		for supportIndex := range left.Outline[index].Supports {
			if left.Outline[index].Supports[supportIndex] != right.Outline[index].Supports[supportIndex] {
				return false
			}
		}
	}
	return true
}

func validOutlineReceiptBinding(receipt outlineReceipt, loaded runContext) bool {
	if receipt.SchemaVersion != receiptSchemaV1 || receipt.SnapshotID != loaded.snapshot.ID ||
		receipt.SnapshotHash != loaded.snapshot.Hash || receipt.TemplateRevisionID != loaded.template.ID ||
		receipt.TemplateHash != loaded.template.DeclarationHash || len(receipt.Outline) != len(loaded.template.Declaration.Sections) {
		return false
	}
	allowed := make(map[outlineSupport]struct{})
	for _, support := range frozenOutlineSupports(loaded.snapshot) {
		allowed[support] = struct{}{}
	}
	for index, item := range receipt.Outline {
		if item.Key != loaded.template.Declaration.Sections[index].Key {
			return false
		}
		for _, support := range item.Supports {
			if _, found := allowed[support]; !found {
				return false
			}
		}
	}
	return true
}

func outlineFromTemplate(revision organizingdomain.TemplateRevision) []artifactdomain.OutlineSection {
	result := make([]artifactdomain.OutlineSection, len(revision.Declaration.Sections))
	for index, section := range revision.Declaration.Sections {
		result[index] = artifactdomain.OutlineSection{Key: section.Key, Title: section.Title}
	}
	return result
}

func outlineFromReceipt(receipt outlineReceipt) []artifactdomain.OutlineSection {
	result := make([]artifactdomain.OutlineSection, len(receipt.Outline))
	for index, section := range receipt.Outline {
		result[index] = artifactdomain.OutlineSection{Key: section.Key, Title: section.Title}
	}
	return result
}

func decodeStartInput(raw json.RawMessage) (StartInput, error) {
	return decodeObject(raw, func(value StartInput) error {
		if !validID(value.SnapshotID) {
			return workflowError(foundation.ErrorInvalidInput, "ORGANIZING_WORKFLOW_INPUT_INVALID", false, "organizing workflow snapshot identity is invalid")
		}
		return nil
	})
}

func decodeOutlineReceipt(raw json.RawMessage) (outlineReceipt, error) {
	return decodeObject(raw, func(value outlineReceipt) error {
		if value.SchemaVersion != receiptSchemaV1 || !validID(value.SnapshotID) || !validID(value.TemplateRevisionID) ||
			!validHash(value.SnapshotHash) || !validHash(value.TemplateHash) || len(value.Outline) == 0 || len(value.Outline) > 24 {
			return workflowError(foundation.ErrorInvalidInput, "ORGANIZING_OUTLINE_RECEIPT_INVALID", false, "organizing outline receipt is invalid")
		}
		for _, section := range value.Outline {
			if strings.TrimSpace(section.Key) == "" || strings.TrimSpace(section.Title) == "" ||
				(len(section.Supports) == 0 && section.GapCode != evidenceUnavailableGap) ||
				(len(section.Supports) > 0 && section.GapCode != "") || len(section.Supports) > maxCitationsPerSection {
				return workflowError(foundation.ErrorInvalidInput, "ORGANIZING_OUTLINE_RECEIPT_INVALID", false, "organizing outline section is invalid")
			}
			seen := make(map[string]struct{}, len(section.Supports))
			for _, support := range section.Supports {
				if !validOutlineSupport(support) {
					return workflowError(foundation.ErrorInvalidInput, "ORGANIZING_OUTLINE_RECEIPT_INVALID", false, "organizing outline support is invalid")
				}
				identity := outlineSupportIdentity(support)
				if _, duplicate := seen[identity]; duplicate {
					return workflowError(foundation.ErrorInvalidInput, "ORGANIZING_OUTLINE_RECEIPT_INVALID", false, "organizing outline support is duplicated")
				}
				seen[identity] = struct{}{}
			}
		}
		return nil
	})
}

func validOutlineSupport(support outlineSupport) bool {
	if !validHash(support.ContentHash) {
		return false
	}
	switch support.Kind {
	case organizingdomain.MaterialSourceVersion:
		return validID(support.SourceVersionID) && validID(support.SourceSpanID) && validHash(support.ExcerptHash) &&
			support.DocumentID == "" && support.ArticleRevisionID == "" && support.RevisionNo == 0
	case organizingdomain.MaterialDocumentRevision:
		return validID(support.DocumentID) && validID(support.ArticleRevisionID) && support.RevisionNo > 0 &&
			support.SourceVersionID == "" && support.SourceSpanID == "" && support.ExcerptHash == ""
	default:
		return false
	}
}

func outlineSupportIdentity(support outlineSupport) string {
	if support.Kind == organizingdomain.MaterialDocumentRevision {
		return string(support.Kind) + "\x00" + string(support.DocumentID) + "\x00" + string(support.ArticleRevisionID)
	}
	return string(support.Kind) + "\x00" + string(support.SourceVersionID) + "\x00" + string(support.SourceSpanID)
}

func decodeMergeReceipt(raw json.RawMessage) (mergeStageReceipt, error) {
	return decodeObject(raw, func(value mergeStageReceipt) error {
		if value.SchemaVersion != receiptSchemaV1 || !validID(value.SnapshotID) || !validID(value.ArtifactID) ||
			!validHash(value.SnapshotHash) || !validHash(value.RevisionHash) || !validHash(value.DiffHash) || value.ConflictCount < 0 ||
			value.EvidenceCount < 0 || value.DocumentCount < 0 || !validDefaultOutputPath(value.DefaultTargetPath) ||
			!validDiffPreview(value) || !validMergeComparisonReceipt(value) {
			return workflowError(foundation.ErrorInvalidInput, "ORGANIZING_MERGE_RECEIPT_INVALID", false, "organizing merge receipt is invalid")
		}
		return nil
	})
}

func mergeReceiptMatchesLoaded(receipt mergeStageReceipt, loaded runContext) bool {
	defaultTargetPath, err := defaultOutputPath(loaded.snapshot, loaded.template)
	return err == nil && receipt.SnapshotID == loaded.snapshot.ID && receipt.SnapshotHash == loaded.snapshot.Hash &&
		receipt.DefaultTargetPath == defaultTargetPath
}

// RenderCreateOnlyDiff renders a deterministic unified diff against /dev/null.
// The stable preview path is intentionally not the eventual user-selected path.
func RenderCreateOnlyDiff(markdown string) string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(markdown, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var diff strings.Builder
	diff.WriteString("--- /dev/null\n+++ b/organized-result.md\n")
	diff.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", len(lines)))
	for _, line := range lines {
		diff.WriteByte('+')
		diff.WriteString(line)
		diff.WriteByte('\n')
	}
	return diff.String()
}

func boundedDiffPreview(diff string) (string, bool) {
	if len(diff) <= maxDiffPreviewBytes {
		return diff, false
	}
	preview := diff[:maxDiffPreviewBytes]
	for !utf8.ValidString(preview) && len(preview) > 0 {
		preview = preview[:len(preview)-1]
	}
	return preview, true
}

func validDiffPreview(receipt mergeStageReceipt) bool {
	if receipt.DiffPreview == "" || len(receipt.DiffPreview) > maxDiffPreviewBytes || !utf8.ValidString(receipt.DiffPreview) ||
		(receipt.DiffTruncated && len(receipt.DiffPreview) < maxDiffPreviewBytes-(utf8.UTFMax-1)) {
		return false
	}
	if !receipt.DiffTruncated && hashText(receipt.DiffPreview) != receipt.DiffHash {
		return false
	}
	return strings.HasPrefix(receipt.DiffPreview, "--- /dev/null\n+++ b/organized-result.md\n@@ ")
}

func decodeApproval(raw json.RawMessage) (approvalDecision, error) {
	return decodeObject(raw, func(approvalDecision) error { return nil })
}

func decodeMergeDecision(raw json.RawMessage) (mergeDecision, error) {
	decision, err := decodeObject(raw, func(value mergeDecision) error {
		if !value.Approved {
			return nil
		}
		canonical, pathErr := changecontroldomain.ValidateTargetPath(value.TargetPath)
		if pathErr != nil || canonical != value.TargetPath || !validDefaultOutputPath(value.TargetPath) ||
			len(value.TargetPath) > 4096 || strings.ContainsAny(value.TargetPath, "\r\n") {
			return workflowError(foundation.ErrorInvalidInput, "ORGANIZING_MERGE_DECISION_INVALID", false, "merge target path is invalid")
		}
		return nil
	})
	if err != nil {
		return mergeDecision{}, err
	}
	if !decision.Approved {
		decision.TargetPath = ""
	}
	return decision, nil
}

func decodeObject[T any](raw json.RawMessage, validate func(T) error) (T, error) {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = 64 * 1024
	limits.MaxArrayItems = 64
	limits.MaxObjectFields = 32
	value, err := strictjson.DecodeObject(raw, limits, validate)
	if err != nil {
		var zero T
		var classified *foundation.Error
		if errors.As(err, &classified) {
			return zero, err
		}
		return zero, workflowError(foundation.ErrorInvalidInput, "ORGANIZING_WORKFLOW_INPUT_INVALID", false, "organizing workflow input does not match its schema")
	}
	return value, nil
}

func definitionForNode(kind string) (string, bool) {
	switch kind {
	case TopicOutlineNodeKind, TopicOutlineApprovalNodeKind, TopicArtifactNodeKind:
		return TopicArticleDefinitionKey, true
	case MergeCompareNodeKind, MergeConfirmationNodeKind, MergeProposalNodeKind:
		return MergeDocumentsDefinitionKey, true
	case KnowledgeReportNodeKind:
		return KnowledgeReportDefinitionKey, true
	case InterviewReviewNodeKind:
		return InterviewReviewDefinitionKey, true
	default:
		return "", false
	}
}

func humanRejected() error {
	return workflowError(foundation.ErrorNonRetryableFailure, "ORGANIZING_HUMAN_REJECTED", false, "organizing result was rejected by the reviewer")
}
