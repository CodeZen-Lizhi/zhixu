package workflow

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	// HumanTaskReviewSchemaVersion freezes the public review projection.
	HumanTaskReviewSchemaVersion = 1
	// HumanTaskReviewTopicOutline identifies a topic outline review.
	HumanTaskReviewTopicOutline = "TOPIC_OUTLINE"
	// HumanTaskReviewMergeComparison identifies a merge comparison review.
	HumanTaskReviewMergeComparison = "MERGE_COMPARISON"
)

// HumanTaskNodeReader proves that a pending task owns the requested waiting node.
type HumanTaskNodeReader interface {
	GetPendingHumanTaskNode(context.Context, foundation.ID, foundation.ID, foundation.ID, foundation.ID) (workflowdomain.NodeRun, error)
}

// HumanTaskDefinitionReader resolves the immutable Definition owned by a Run.
type HumanTaskDefinitionReader interface {
	GetRunDefinition(context.Context, foundation.ID, foundation.ID) (workflowdomain.Definition, error)
}

// HumanTaskReviewDependencies are the owner reads used to build a review-only projection.
type HumanTaskReviewDependencies struct {
	Bindings    RunBindingStore
	Snapshots   SnapshotReader
	Stages      StageOutputReader
	Nodes       HumanTaskNodeReader
	Definitions HumanTaskDefinitionReader
}

// HumanTaskReviewProjector exposes only the bounded facts needed before a
// reviewer submits a Human Task decision.
type HumanTaskReviewProjector struct{ dependencies HumanTaskReviewDependencies }

// TopicOutlineReviewSupport is one immutable Evidence tuple supporting a section.
type TopicOutlineReviewSupport struct {
	Kind              organizingdomain.MaterialKind `json:"kind"`
	SourceVersionID   foundation.ID                 `json:"source_version_id,omitempty"`
	SourceSpanID      foundation.ID                 `json:"source_span_id,omitempty"`
	DocumentID        foundation.ID                 `json:"document_id,omitempty"`
	ArticleRevisionID foundation.ID                 `json:"article_revision_id,omitempty"`
	RevisionNo        int64                         `json:"revision_no,omitempty"`
	ContentHash       string                        `json:"content_hash"`
	ExcerptHash       string                        `json:"excerpt_hash,omitempty"`
}

// TopicOutlineReviewSection is one proposed section with Evidence or an explicit GAP.
type TopicOutlineReviewSection struct {
	Key      string                      `json:"key"`
	Title    string                      `json:"title"`
	Supports []TopicOutlineReviewSupport `json:"supports"`
	GapCode  *string                     `json:"gap_code"`
}

// TopicOutlineReview is the public review projection for TOPIC_ARTICLE.
type TopicOutlineReview struct {
	Kind               string                      `json:"kind"`
	SchemaVersion      int                         `json:"schema_version"`
	WorkspaceID        foundation.ID               `json:"workspace_id"`
	RunID              foundation.ID               `json:"run_id"`
	TaskID             foundation.ID               `json:"task_id"`
	NodeRunID          foundation.ID               `json:"node_run_id"`
	SnapshotID         foundation.ID               `json:"snapshot_id"`
	SnapshotHash       string                      `json:"snapshot_hash"`
	TemplateRevisionID foundation.ID               `json:"template_revision_id"`
	TemplateHash       string                      `json:"template_hash"`
	Outline            []TopicOutlineReviewSection `json:"outline"`
}

// MergeReviewCategory reports the complete count for one fixed merge category.
type MergeReviewCategory struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}

// MergeReviewEvidence is one source-addressable comparison preview entry.
type MergeReviewEvidence struct {
	Category          string                        `json:"category"`
	Kind              organizingdomain.MaterialKind `json:"kind"`
	SourceVersionID   foundation.ID                 `json:"source_version_id,omitempty"`
	SourceSpanID      foundation.ID                 `json:"source_span_id,omitempty"`
	DocumentID        foundation.ID                 `json:"document_id,omitempty"`
	ArticleRevisionID foundation.ID                 `json:"article_revision_id,omitempty"`
	RevisionNo        int64                         `json:"revision_no,omitempty"`
	ContentHash       string                        `json:"content_hash"`
	ExcerptHash       string                        `json:"excerpt_hash,omitempty"`
}

// MergeComparisonReview is the public review projection for MERGE_DOCUMENTS.
type MergeComparisonReview struct {
	Kind              string                `json:"kind"`
	SchemaVersion     int                   `json:"schema_version"`
	WorkspaceID       foundation.ID         `json:"workspace_id"`
	RunID             foundation.ID         `json:"run_id"`
	TaskID            foundation.ID         `json:"task_id"`
	NodeRunID         foundation.ID         `json:"node_run_id"`
	SnapshotID        foundation.ID         `json:"snapshot_id"`
	SnapshotHash      string                `json:"snapshot_hash"`
	ArtifactID        foundation.ID         `json:"artifact_id"`
	RevisionHash      string                `json:"revision_hash"`
	DefaultTargetPath string                `json:"default_target_path"`
	DiffHash          string                `json:"diff_hash"`
	DiffPreview       string                `json:"diff_preview"`
	DiffTruncated     bool                  `json:"diff_truncated"`
	ConflictCount     int                   `json:"conflict_count"`
	EvidenceCount     int                   `json:"evidence_count"`
	DocumentCount     int                   `json:"document_count"`
	Categories        []MergeReviewCategory `json:"categories"`
	Comparison        []MergeReviewEvidence `json:"comparison"`
}

// NewHumanTaskReviewProjector constructs the strict Organizing review projection.
func NewHumanTaskReviewProjector(dependencies HumanTaskReviewDependencies) (*HumanTaskReviewProjector, error) {
	if dependencies.Bindings == nil || dependencies.Snapshots == nil || dependencies.Stages == nil || dependencies.Nodes == nil || dependencies.Definitions == nil {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_HUMAN_REVIEW_UNAVAILABLE", true, "organizing human review dependencies are incomplete")
	}
	return &HumanTaskReviewProjector{dependencies: dependencies}, nil
}

// ProjectHumanTaskReview returns found=false for non-Organizing tasks. Once a
// Run uses the Organizing start identity, every missing or drifting fact fails
// closed instead of returning a review-less approval task.
func (projector *HumanTaskReviewProjector) ProjectHumanTaskReview(
	ctx context.Context,
	run workflowdomain.Run,
	task workflowdomain.HumanTask,
) (json.RawMessage, bool, error) {
	if projector == nil || projector.dependencies.Bindings == nil || projector.dependencies.Snapshots == nil ||
		projector.dependencies.Stages == nil || projector.dependencies.Nodes == nil || projector.dependencies.Definitions == nil {
		return nil, false, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_HUMAN_REVIEW_UNAVAILABLE", true, "organizing human review projector is unavailable")
	}
	definition, err := projector.dependencies.Definitions.GetRunDefinition(ctx, run.WorkspaceID, run.ID)
	if err != nil {
		return nil, false, err
	}
	recognized := organizingDefinitionKey(definition.Key)
	if !recognized {
		return nil, false, nil
	}
	start, err := recognizeHumanReviewRun(run, definition)
	if err != nil {
		return nil, true, err
	}
	if ctx == nil || run.Status != workflowdomain.RunStatusWaitingForHuman || !validID(run.ID) || !validID(run.WorkspaceID) ||
		!validID(task.ID) || task.RunID != run.ID || !validID(task.NodeRunID) || task.Status != workflowdomain.HumanTaskPending {
		return nil, true, reviewInconsistent("ORGANIZING_HUMAN_REVIEW_TASK_INVALID", "organizing human review task binding is invalid")
	}
	binding, err := projector.dependencies.Bindings.GetRunBinding(ctx, run.WorkspaceID, start.SnapshotID)
	if err != nil {
		return nil, true, reviewReadError(err, "ORGANIZING_HUMAN_REVIEW_BINDING_INVALID", "organizing human review run binding is unavailable")
	}
	if binding.WorkspaceID != run.WorkspaceID || binding.SnapshotID != start.SnapshotID || binding.WorkflowRunID != run.ID ||
		binding.DefinitionKey != definition.Key || binding.DefinitionVersion != DefinitionVersion {
		return nil, true, reviewInconsistent("ORGANIZING_HUMAN_REVIEW_BINDING_INVALID", "organizing human review run binding drifted")
	}
	snapshot, err := projector.dependencies.Snapshots.GetSnapshot(ctx, run.WorkspaceID, start.SnapshotID)
	if err != nil {
		return nil, true, reviewReadError(err, "ORGANIZING_HUMAN_REVIEW_SNAPSHOT_INVALID", "organizing human review snapshot is unavailable")
	}
	if snapshot.ID != start.SnapshotID || snapshot.WorkspaceID != run.WorkspaceID || snapshot.Validate() != nil {
		return nil, true, reviewInconsistent("ORGANIZING_HUMAN_REVIEW_SNAPSHOT_INVALID", "organizing human review snapshot binding drifted")
	}

	node, err := projector.dependencies.Nodes.GetPendingHumanTaskNode(ctx, run.WorkspaceID, run.ID, task.ID, task.NodeRunID)
	if err != nil {
		return nil, true, reviewReadError(err, "ORGANIZING_HUMAN_REVIEW_TASK_INVALID", "organizing human review task node is unavailable")
	}
	if node.ID != task.NodeRunID || node.RunID != run.ID || node.Status != workflowdomain.NodeStatusWaitingForHuman ||
		node.InputSchemaVersion != InputSchemaVersion || node.OutputSchemaVersion != OutputSchemaVersion {
		return nil, true, reviewInconsistent("ORGANIZING_HUMAN_REVIEW_TASK_INVALID", "organizing human review node binding drifted")
	}

	switch binding.DefinitionKey {
	case TopicArticleDefinitionKey:
		return projector.projectTopicOutline(ctx, run, task, node, snapshot)
	case MergeDocumentsDefinitionKey:
		return projector.projectMergeComparison(ctx, run, task, node, snapshot)
	default:
		return nil, true, reviewInconsistent("ORGANIZING_HUMAN_REVIEW_DEFINITION_INVALID", "organizing workflow does not own a reviewable Human Task")
	}
}

func (projector *HumanTaskReviewProjector) projectTopicOutline(
	ctx context.Context,
	run workflowdomain.Run,
	task workflowdomain.HumanTask,
	node workflowdomain.NodeRun,
	snapshot organizingdomain.Snapshot,
) (json.RawMessage, bool, error) {
	if node.NodeKey != TopicOutlineApprovalNodeKind || node.NodeType != TopicOutlineApprovalNodeKind ||
		!validReviewSchema(task.ExpectedInputSchema, false) {
		return nil, true, reviewInconsistent("ORGANIZING_HUMAN_REVIEW_TASK_INVALID", "topic outline review task contract drifted")
	}
	stage, err := projector.dependencies.Stages.GetSucceededNodeOutput(ctx, run.WorkspaceID, run.ID, TopicOutlineNodeKind)
	if err != nil {
		return nil, true, reviewReadError(err, "ORGANIZING_HUMAN_REVIEW_RECEIPT_INVALID", "topic outline review receipt is unavailable")
	}
	receipt, err := decodeOutlineReceipt(stage)
	if err != nil {
		return nil, true, reviewInconsistent("ORGANIZING_HUMAN_REVIEW_RECEIPT_INVALID", "topic outline review receipt is invalid")
	}
	nodeReceipt, err := decodeOutlineReceipt(node.Input)
	if err != nil || !sameOutlineReceipt(receipt, nodeReceipt) || !outlineReceiptMatchesSnapshot(receipt, snapshot) {
		return nil, true, reviewInconsistent("ORGANIZING_HUMAN_REVIEW_RECEIPT_INVALID", "topic outline review receipt binding drifted")
	}
	sections := make([]TopicOutlineReviewSection, len(receipt.Outline))
	for index, item := range receipt.Outline {
		sections[index] = TopicOutlineReviewSection{Key: item.Key, Title: item.Title, Supports: make([]TopicOutlineReviewSupport, len(item.Supports))}
		for supportIndex, support := range item.Supports {
			sections[index].Supports[supportIndex] = TopicOutlineReviewSupport(support)
		}
		if item.GapCode != "" {
			gap := item.GapCode
			sections[index].GapCode = &gap
		}
	}
	review := TopicOutlineReview{
		Kind: HumanTaskReviewTopicOutline, SchemaVersion: HumanTaskReviewSchemaVersion,
		WorkspaceID: run.WorkspaceID, RunID: run.ID, TaskID: task.ID, NodeRunID: task.NodeRunID,
		SnapshotID: receipt.SnapshotID, SnapshotHash: receipt.SnapshotHash,
		TemplateRevisionID: receipt.TemplateRevisionID, TemplateHash: receipt.TemplateHash, Outline: sections,
	}
	return marshalHumanTaskReview(review)
}

func (projector *HumanTaskReviewProjector) projectMergeComparison(
	ctx context.Context,
	run workflowdomain.Run,
	task workflowdomain.HumanTask,
	node workflowdomain.NodeRun,
	snapshot organizingdomain.Snapshot,
) (json.RawMessage, bool, error) {
	if node.NodeKey != MergeConfirmationNodeKind || node.NodeType != MergeConfirmationNodeKind ||
		!validReviewSchema(task.ExpectedInputSchema, true) {
		return nil, true, reviewInconsistent("ORGANIZING_HUMAN_REVIEW_TASK_INVALID", "merge comparison review task contract drifted")
	}
	stage, err := projector.dependencies.Stages.GetSucceededNodeOutput(ctx, run.WorkspaceID, run.ID, MergeCompareNodeKind)
	if err != nil {
		return nil, true, reviewReadError(err, "ORGANIZING_HUMAN_REVIEW_RECEIPT_INVALID", "merge comparison review receipt is unavailable")
	}
	receipt, err := decodeMergeReceipt(stage)
	if err != nil {
		return nil, true, reviewInconsistent("ORGANIZING_HUMAN_REVIEW_RECEIPT_INVALID", "merge comparison review receipt is invalid")
	}
	nodeReceipt, err := decodeMergeReceipt(node.Input)
	if err != nil || !sameMergeReceipt(receipt, nodeReceipt) || !mergeReceiptMatchesSnapshot(receipt, snapshot) {
		return nil, true, reviewInconsistent("ORGANIZING_HUMAN_REVIEW_RECEIPT_INVALID", "merge comparison review receipt binding drifted")
	}
	categories := make([]MergeReviewCategory, len(receipt.Categories))
	for index, category := range receipt.Categories {
		categories[index] = MergeReviewCategory(category)
	}
	comparison := make([]MergeReviewEvidence, len(receipt.Comparison))
	for index, item := range receipt.Comparison {
		comparison[index] = MergeReviewEvidence(item)
	}
	review := MergeComparisonReview{
		Kind: HumanTaskReviewMergeComparison, SchemaVersion: HumanTaskReviewSchemaVersion,
		WorkspaceID: run.WorkspaceID, RunID: run.ID, TaskID: task.ID, NodeRunID: task.NodeRunID,
		SnapshotID: receipt.SnapshotID, SnapshotHash: receipt.SnapshotHash, ArtifactID: receipt.ArtifactID,
		RevisionHash: receipt.RevisionHash, DefaultTargetPath: receipt.DefaultTargetPath, DiffHash: receipt.DiffHash,
		DiffPreview: receipt.DiffPreview, DiffTruncated: receipt.DiffTruncated, ConflictCount: receipt.ConflictCount,
		EvidenceCount: receipt.EvidenceCount, DocumentCount: receipt.DocumentCount, Categories: categories, Comparison: comparison,
	}
	return marshalHumanTaskReview(review)
}

func recognizeHumanReviewRun(run workflowdomain.Run, definition workflowdomain.Definition) (StartInput, error) {
	if definition.ID != run.DefinitionID || definition.WorkspaceID != run.WorkspaceID || definition.Version != DefinitionVersion {
		return StartInput{}, reviewInconsistent("ORGANIZING_HUMAN_REVIEW_RUN_INVALID", "organizing human review Definition binding is invalid")
	}
	start, err := decodeStartInput(run.Input)
	if err != nil || run.IdempotencyKey != organizingapp.StartIdempotencyKey(start.SnapshotID) {
		return StartInput{}, reviewInconsistent("ORGANIZING_HUMAN_REVIEW_RUN_INVALID", "organizing human review run input is invalid")
	}
	return start, nil
}

func organizingDefinitionKey(key string) bool {
	return key == TopicArticleDefinitionKey || key == MergeDocumentsDefinitionKey ||
		key == KnowledgeReportDefinitionKey || key == InterviewReviewDefinitionKey
}

func outlineReceiptMatchesSnapshot(receipt outlineReceipt, snapshot organizingdomain.Snapshot) bool {
	if receipt.SnapshotID != snapshot.ID || receipt.SnapshotHash != snapshot.Hash ||
		receipt.TemplateRevisionID != snapshot.TemplateRevisionID || receipt.TemplateHash != snapshot.TemplateHash {
		return false
	}
	evidence := snapshotEvidence(snapshot)
	documents := snapshotDocuments(snapshot)
	for _, section := range receipt.Outline {
		for _, support := range section.Supports {
			if support.Kind == organizingdomain.MaterialDocumentRevision {
				identity := string(support.DocumentID) + "\x00" + string(support.ArticleRevisionID)
				frozen, found := documents[identity]
				if !found || frozen.Version != support.RevisionNo || frozen.ContentHash != support.ContentHash {
					return false
				}
			} else {
				identity := string(support.SourceVersionID) + "\x00" + string(support.SourceSpanID)
				frozen, found := evidence[identity]
				if !found || frozen.ContentHash != support.ContentHash || frozen.ExcerptHash != support.ExcerptHash {
					return false
				}
			}
		}
	}
	return true
}

func mergeReceiptMatchesSnapshot(receipt mergeStageReceipt, snapshot organizingdomain.Snapshot) bool {
	if receipt.SnapshotID != snapshot.ID || receipt.SnapshotHash != snapshot.Hash {
		return false
	}
	evidence := snapshotEvidence(snapshot)
	documents := snapshotDocuments(snapshot)
	for _, item := range receipt.Comparison {
		if item.Kind == organizingdomain.MaterialDocumentRevision {
			identity := string(item.DocumentID) + "\x00" + string(item.ArticleRevisionID)
			frozen, found := documents[identity]
			if !found || frozen.Version != item.RevisionNo || frozen.ContentHash != item.ContentHash {
				return false
			}
		} else {
			identity := string(item.SourceVersionID) + "\x00" + string(item.SourceSpanID)
			frozen, found := evidence[identity]
			if !found || frozen.ContentHash != item.ContentHash || frozen.ExcerptHash != item.ExcerptHash {
				return false
			}
		}
	}
	return true
}

func snapshotEvidence(snapshot organizingdomain.Snapshot) map[string]organizingdomain.EvidenceRef {
	result := make(map[string]organizingdomain.EvidenceRef)
	for _, material := range snapshot.Materials {
		for _, evidence := range material.Evidence {
			identity := string(evidence.SourceVersionID) + "\x00" + string(evidence.SourceSpanID)
			if _, found := result[identity]; !found {
				result[identity] = evidence
			}
		}
	}
	return result
}

func snapshotDocuments(snapshot organizingdomain.Snapshot) map[string]organizingdomain.MaterialRef {
	result := make(map[string]organizingdomain.MaterialRef)
	for _, material := range snapshot.Materials {
		if material.Kind == organizingdomain.MaterialDocumentRevision {
			result[string(material.DocumentID)+"\x00"+string(material.ArticleRevisionID)] = material
		}
	}
	return result
}

func sameMergeReceipt(left, right mergeStageReceipt) bool {
	if left.SchemaVersion != right.SchemaVersion || left.SnapshotID != right.SnapshotID || left.SnapshotHash != right.SnapshotHash ||
		left.ArtifactID != right.ArtifactID || left.RevisionHash != right.RevisionHash || left.DiffHash != right.DiffHash ||
		left.DefaultTargetPath != right.DefaultTargetPath ||
		left.DiffPreview != right.DiffPreview || left.DiffTruncated != right.DiffTruncated ||
		left.ConflictCount != right.ConflictCount || left.EvidenceCount != right.EvidenceCount || left.DocumentCount != right.DocumentCount ||
		len(left.Categories) != len(right.Categories) || len(left.Comparison) != len(right.Comparison) {
		return false
	}
	for index := range left.Categories {
		if left.Categories[index] != right.Categories[index] {
			return false
		}
	}
	for index := range left.Comparison {
		if left.Comparison[index] != right.Comparison[index] {
			return false
		}
	}
	return true
}

type humanReviewSchema struct {
	Type                 string                      `json:"type"`
	Required             []string                    `json:"required"`
	Properties           humanReviewSchemaProperties `json:"properties"`
	AdditionalProperties *bool                       `json:"additionalProperties"`
}

type humanReviewSchemaProperties struct {
	Approved   humanReviewSchemaField  `json:"approved"`
	TargetPath *humanReviewSchemaField `json:"target_path,omitempty"`
}

type humanReviewSchemaField struct {
	Type string `json:"type"`
}

func validReviewSchema(raw json.RawMessage, merge bool) bool {
	schema, err := decodeObject(raw, func(value humanReviewSchema) error {
		if value.Type != "object" || len(value.Required) != 1 || value.Required[0] != "approved" ||
			value.Properties.Approved.Type != "boolean" || value.AdditionalProperties == nil || *value.AdditionalProperties {
			return errors.New("human review schema is invalid")
		}
		if merge {
			if value.Properties.TargetPath == nil || value.Properties.TargetPath.Type != "string" {
				return errors.New("merge review schema is invalid")
			}
		} else if value.Properties.TargetPath != nil {
			return errors.New("topic review schema is invalid")
		}
		return nil
	})
	return err == nil && schema.Type == "object"
}

func marshalHumanTaskReview(value any) (json.RawMessage, bool, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, true, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_HUMAN_REVIEW_ENCODING_FAILED", false, "organizing human review could not be encoded")
	}
	return encoded, true, nil
}

func reviewReadError(err error, code, message string) error {
	var classified *foundation.Error
	if errors.As(err, &classified) && (classified.Kind == foundation.ErrorDependencyUnavailable || classified.Kind == foundation.ErrorRetryableFailure) {
		return err
	}
	return reviewInconsistent(code, message)
}

func reviewInconsistent(code, message string) error {
	return workflowError(foundation.ErrorConsistencyViolation, code, false, message)
}
