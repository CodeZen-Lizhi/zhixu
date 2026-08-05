package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestHumanTaskReviewProjectorReturnsBoundTopicOutline(t *testing.T) {
	fixture, run, task, receipt := topicReviewFixture(t)
	projector := mustHumanReviewProjector(t, fixture)
	raw, required, err := projector.ProjectHumanTaskReview(context.Background(), run, task)
	if err != nil || !required {
		t.Fatalf("required=%v error=%v", required, err)
	}
	var review TopicOutlineReview
	if err := json.Unmarshal(raw, &review); err != nil {
		t.Fatal(err)
	}
	if review.Kind != HumanTaskReviewTopicOutline || review.SchemaVersion != HumanTaskReviewSchemaVersion ||
		review.WorkspaceID != run.WorkspaceID || review.RunID != run.ID || review.TaskID != task.ID || review.NodeRunID != task.NodeRunID ||
		review.SnapshotID != receipt.SnapshotID || review.SnapshotHash != receipt.SnapshotHash ||
		review.TemplateRevisionID != receipt.TemplateRevisionID || review.TemplateHash != receipt.TemplateHash ||
		len(review.Outline) != len(receipt.Outline) {
		t.Fatalf("review=%+v", review)
	}
	for index, section := range review.Outline {
		if len(section.Supports) == 0 && section.GapCode == nil {
			t.Fatalf("section %d has neither support nor GAP: %+v", index, section)
		}
		if len(section.Supports) > 0 && section.GapCode != nil {
			t.Fatalf("section %d has support and GAP: %+v", index, section)
		}
	}
}

func TestHumanTaskReviewProjectorAllowsEvidenceToSupportMultipleOutlineSections(t *testing.T) {
	t.Parallel()
	fixture, run, task, receipt := topicReviewFixture(t)
	if len(receipt.Outline) < 2 || len(receipt.Outline[0].Supports) == 0 {
		t.Fatal("topic review fixture does not contain reusable evidence")
	}
	receipt.Outline[1].Supports = append([]outlineSupport(nil), receipt.Outline[0].Supports...)
	receipt.Outline[1].GapCode = ""
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	fixture.stage = encoded
	fixture.node.Input = append(json.RawMessage(nil), encoded...)

	raw, required, err := mustHumanReviewProjector(t, fixture).ProjectHumanTaskReview(context.Background(), run, task)
	if err != nil || !required || len(raw) == 0 {
		t.Fatalf("required=%v raw=%s error=%v", required, raw, err)
	}
}

func TestHumanTaskReviewProjectorReturnsBoundMergeComparison(t *testing.T) {
	fixture, run, task, receipt := mergeReviewFixture(t)
	projector := mustHumanReviewProjector(t, fixture)
	raw, required, err := projector.ProjectHumanTaskReview(context.Background(), run, task)
	if err != nil || !required {
		t.Fatalf("required=%v error=%v", required, err)
	}
	var review MergeComparisonReview
	if err := json.Unmarshal(raw, &review); err != nil {
		t.Fatal(err)
	}
	if review.Kind != HumanTaskReviewMergeComparison || review.WorkspaceID != run.WorkspaceID || review.RunID != run.ID ||
		review.TaskID != task.ID || review.NodeRunID != task.NodeRunID || review.ArtifactID != receipt.ArtifactID ||
		review.RevisionHash != receipt.RevisionHash || review.DefaultTargetPath != receipt.DefaultTargetPath ||
		review.DiffHash != receipt.DiffHash || review.DiffPreview != receipt.DiffPreview ||
		review.DiffTruncated || review.EvidenceCount != 1 || review.DocumentCount != 1 ||
		len(review.Categories) != 4 || review.Categories[3].Category != mergeCategoryUnique || review.Categories[3].Count != 2 ||
		len(review.Comparison) != 2 || review.Comparison[0].SourceSpanID != receipt.Comparison[0].SourceSpanID ||
		review.Comparison[1].DocumentID != receipt.Comparison[1].DocumentID || review.Comparison[1].ArticleRevisionID != receipt.Comparison[1].ArticleRevisionID {
		t.Fatalf("review=%+v", review)
	}
}

func TestHumanTaskReviewProjectorRejectsBindingAndReceiptDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*humanReviewFixture, *workflowdomain.Run, *workflowdomain.HumanTask)
		code   string
	}{
		{name: "workspace binding", code: "ORGANIZING_HUMAN_REVIEW_BINDING_INVALID", mutate: func(f *humanReviewFixture, _ *workflowdomain.Run, _ *workflowdomain.HumanTask) {
			f.binding.WorkspaceID = semanticID(901)
		}},
		{name: "definition key binding", code: "ORGANIZING_HUMAN_REVIEW_BINDING_INVALID", mutate: func(f *humanReviewFixture, _ *workflowdomain.Run, _ *workflowdomain.HumanTask) {
			f.binding.DefinitionKey = MergeDocumentsDefinitionKey
		}},
		{name: "task node binding", code: "ORGANIZING_HUMAN_REVIEW_TASK_INVALID", mutate: func(f *humanReviewFixture, _ *workflowdomain.Run, _ *workflowdomain.HumanTask) {
			f.node.NodeKey = MergeConfirmationNodeKind
		}},
		{name: "node input receipt", code: "ORGANIZING_HUMAN_REVIEW_RECEIPT_INVALID", mutate: func(f *humanReviewFixture, _ *workflowdomain.Run, _ *workflowdomain.HumanTask) {
			var receipt outlineReceipt
			if err := json.Unmarshal(f.node.Input, &receipt); err != nil {
				panic(err)
			}
			receipt.SnapshotHash = hashText("drift")
			f.node.Input, _ = json.Marshal(receipt)
		}},
		{name: "unknown receipt field", code: "ORGANIZING_HUMAN_REVIEW_RECEIPT_INVALID", mutate: func(f *humanReviewFixture, _ *workflowdomain.Run, _ *workflowdomain.HumanTask) {
			var object map[string]any
			if err := json.Unmarshal(f.stage, &object); err != nil {
				panic(err)
			}
			object["unexpected"] = true
			f.stage, _ = json.Marshal(object)
		}},
		{name: "snapshot evidence hash", code: "ORGANIZING_HUMAN_REVIEW_RECEIPT_INVALID", mutate: func(f *humanReviewFixture, _ *workflowdomain.Run, _ *workflowdomain.HumanTask) {
			var receipt outlineReceipt
			if err := json.Unmarshal(f.stage, &receipt); err != nil {
				panic(err)
			}
			for index := range receipt.Outline {
				if len(receipt.Outline[index].Supports) > 0 {
					receipt.Outline[index].Supports[0].ExcerptHash = hashText("drift")
					break
				}
			}
			f.stage, _ = json.Marshal(receipt)
			f.node.Input = append(json.RawMessage(nil), f.stage...)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, run, task, _ := topicReviewFixture(t)
			test.mutate(fixture, &run, &task)
			_, required, err := mustHumanReviewProjector(t, fixture).ProjectHumanTaskReview(context.Background(), run, task)
			if !required || reviewErrorCode(err) != test.code {
				t.Fatalf("required=%v code=%q error=%v", required, reviewErrorCode(err), err)
			}
		})
	}
}

func TestHumanTaskReviewProjectorDoesNotInventReviewForOtherWorkflows(t *testing.T) {
	fixture, run, task, _ := topicReviewFixture(t)
	// Another owner may use the same snapshot-shaped input and key. Definition
	// ownership, not the generic "snapshot:" prefix, decides projection.
	fixture.definition.Key = "other.snapshot-review"
	raw, required, err := mustHumanReviewProjector(t, fixture).ProjectHumanTaskReview(context.Background(), run, task)
	if err != nil || required || raw != nil || fixture.bindingCalls != 0 || fixture.nodeCalls != 0 || fixture.stageCalls != 0 {
		t.Fatalf("raw=%s required=%v error=%v fixture=%+v", raw, required, err, fixture)
	}
}

func TestHumanTaskReviewProjectorFailsClosedWhenOwnerReadUnavailable(t *testing.T) {
	fixture, run, task, _ := topicReviewFixture(t)
	fixture.stageErr = foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_STAGE_OUTPUT_UNAVAILABLE", true, errors.New("offline"))
	_, required, err := mustHumanReviewProjector(t, fixture).ProjectHumanTaskReview(context.Background(), run, task)
	if !required || reviewErrorCode(err) != "WORKFLOW_STAGE_OUTPUT_UNAVAILABLE" {
		t.Fatalf("required=%v code=%q error=%v", required, reviewErrorCode(err), err)
	}
}

type humanReviewFixture struct {
	binding                                                             organizingdomain.RunBinding
	snapshot                                                            organizingdomain.Snapshot
	node                                                                workflowdomain.NodeRun
	definition                                                          workflowdomain.Definition
	stage                                                               json.RawMessage
	bindingErr, snapshotErr, nodeErr, definitionErr, stageErr           error
	bindingCalls, snapshotCalls, nodeCalls, definitionCalls, stageCalls int
}

func (fixture *humanReviewFixture) GetRunBinding(_ context.Context, workspaceID, snapshotID foundation.ID) (organizingdomain.RunBinding, error) {
	fixture.bindingCalls++
	if workspaceID != fixture.binding.WorkspaceID || snapshotID != fixture.binding.SnapshotID {
		return organizingdomain.RunBinding{}, errors.New("unexpected run binding query")
	}
	return fixture.binding, fixture.bindingErr
}

func (fixture *humanReviewFixture) GetSnapshot(_ context.Context, workspaceID, snapshotID foundation.ID) (organizingdomain.Snapshot, error) {
	fixture.snapshotCalls++
	if workspaceID != fixture.snapshot.WorkspaceID || snapshotID != fixture.snapshot.ID {
		return organizingdomain.Snapshot{}, errors.New("unexpected snapshot query")
	}
	return fixture.snapshot, fixture.snapshotErr
}

func (fixture *humanReviewFixture) GetPendingHumanTaskNode(_ context.Context, workspaceID, runID, taskID, nodeRunID foundation.ID) (workflowdomain.NodeRun, error) {
	fixture.nodeCalls++
	if workspaceID != fixture.binding.WorkspaceID || runID != fixture.binding.WorkflowRunID || taskID == "" || nodeRunID != fixture.node.ID {
		return workflowdomain.NodeRun{}, errors.New("unexpected task node query")
	}
	return fixture.node, fixture.nodeErr
}

func (fixture *humanReviewFixture) GetRunDefinition(_ context.Context, workspaceID, runID foundation.ID) (workflowdomain.Definition, error) {
	fixture.definitionCalls++
	if workspaceID != fixture.definition.WorkspaceID || runID != fixture.binding.WorkflowRunID {
		return workflowdomain.Definition{}, errors.New("unexpected definition query")
	}
	return fixture.definition, fixture.definitionErr
}

func (fixture *humanReviewFixture) GetSucceededNodeOutput(_ context.Context, workspaceID, runID foundation.ID, _ string) (json.RawMessage, error) {
	fixture.stageCalls++
	if workspaceID != fixture.binding.WorkspaceID || runID != fixture.binding.WorkflowRunID {
		return nil, errors.New("unexpected stage output query")
	}
	return append(json.RawMessage(nil), fixture.stage...), fixture.stageErr
}

func topicReviewFixture(t *testing.T) (*humanReviewFixture, workflowdomain.Run, workflowdomain.HumanTask, outlineReceipt) {
	t.Helper()
	snapshot, _ := semanticSnapshot([]semanticEvidence{{source: 1, span: 11, excerpt: "Cache is shared."}})
	revision := semanticBuiltInRevision(t, organizingdomain.TemplateTopicArticle)
	snapshot.TemplateID, snapshot.TemplateRevisionID, snapshot.TemplateHash = revision.TemplateID, revision.ID, revision.DeclarationHash
	snapshot.Hash = validSnapshotHash(t, snapshot)
	receipt := newOutlineReceipt(runContext{snapshot: snapshot, template: revision})
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	runID, taskID, nodeID := semanticID(801), semanticID(802), semanticID(803)
	run := workflowdomain.Run{ID: runID, WorkspaceID: snapshot.WorkspaceID, DefinitionID: semanticID(804), Status: workflowdomain.RunStatusWaitingForHuman,
		Input: json.RawMessage(`{"snapshot_id":"` + string(snapshot.ID) + `"}`), IdempotencyKey: organizingapp.StartIdempotencyKey(snapshot.ID)}
	task := workflowdomain.HumanTask{ID: taskID, RunID: runID, NodeRunID: nodeID, Status: workflowdomain.HumanTaskPending,
		ExpectedInputSchema: append(json.RawMessage(nil), outlineApprovalSchema...), TargetVersion: 1}
	fixture := &humanReviewFixture{
		binding: organizingdomain.RunBinding{ID: semanticID(805), WorkspaceID: snapshot.WorkspaceID, SnapshotID: snapshot.ID,
			WorkflowRunID: runID, DefinitionKey: TopicArticleDefinitionKey, DefinitionVersion: DefinitionVersion, CreatedAt: snapshot.CreatedAt},
		snapshot: snapshot,
		definition: workflowdomain.Definition{ID: run.DefinitionID, WorkspaceID: run.WorkspaceID, Key: TopicArticleDefinitionKey,
			Version: DefinitionVersion, CreatedAt: snapshot.CreatedAt},
		node: workflowdomain.NodeRun{ID: nodeID, RunID: runID, NodeKey: TopicOutlineApprovalNodeKind, NodeType: TopicOutlineApprovalNodeKind,
			Status: workflowdomain.NodeStatusWaitingForHuman, Input: append(json.RawMessage(nil), encoded...), InputSchemaVersion: InputSchemaVersion,
			OutputSchemaVersion: OutputSchemaVersion},
		stage: encoded,
	}
	return fixture, run, task, receipt
}

func mergeReviewFixture(t *testing.T) (*humanReviewFixture, workflowdomain.Run, workflowdomain.HumanTask, mergeStageReceipt) {
	t.Helper()
	snapshot, _ := semanticSnapshot([]semanticEvidence{{source: 1, span: 11, excerpt: "Cache is shared."}})
	revision := semanticBuiltInRevision(t, organizingdomain.TemplateMergeDocuments)
	snapshot.TemplateID, snapshot.TemplateRevisionID, snapshot.TemplateHash = revision.TemplateID, revision.ID, revision.DeclarationHash
	document := organizingdomain.MaterialRef{
		Kind: organizingdomain.MaterialDocumentRevision, DocumentID: semanticID(817), ArticleRevisionID: semanticID(818),
		Version: 2, ContentHash: hashText("historical-document"), Evidence: []organizingdomain.EvidenceRef{},
	}
	snapshot.Materials = append(snapshot.Materials, document)
	snapshot.Hash = validSnapshotHash(t, snapshot)
	evidence := snapshot.Materials[0].Evidence[0]
	diff := RenderCreateOnlyDiff("# diff\n")
	defaultTargetPath, err := defaultOutputPath(snapshot, revision)
	if err != nil {
		t.Fatal(err)
	}
	receipt := mergeStageReceipt{
		SchemaVersion: receiptSchemaV1, SnapshotID: snapshot.ID, SnapshotHash: snapshot.Hash,
		ArtifactID: semanticID(811), RevisionHash: hashText("revision"), DefaultTargetPath: defaultTargetPath,
		DiffHash: hashText(diff), DiffPreview: diff, EvidenceCount: 1, DocumentCount: 1,
		Categories: []mergeCategoryCount{{Category: mergeCategoryDuplicate}, {Category: mergeCategoryComplementary},
			{Category: mergeCategoryConflict}, {Category: mergeCategoryUnique, Count: 2}},
		Comparison: []mergeComparisonEntry{{Category: mergeCategoryUnique, Kind: organizingdomain.MaterialSourceVersion, SourceVersionID: evidence.SourceVersionID,
			SourceSpanID: evidence.SourceSpanID, ContentHash: evidence.ContentHash, ExcerptHash: evidence.ExcerptHash},
			{Category: mergeCategoryUnique, Kind: organizingdomain.MaterialDocumentRevision, DocumentID: document.DocumentID,
				ArticleRevisionID: document.ArticleRevisionID, RevisionNo: document.Version, ContentHash: document.ContentHash}},
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	runID, taskID, nodeID := semanticID(812), semanticID(813), semanticID(814)
	run := workflowdomain.Run{ID: runID, WorkspaceID: snapshot.WorkspaceID, DefinitionID: semanticID(815), Status: workflowdomain.RunStatusWaitingForHuman,
		Input: json.RawMessage(`{"snapshot_id":"` + string(snapshot.ID) + `"}`), IdempotencyKey: organizingapp.StartIdempotencyKey(snapshot.ID)}
	task := workflowdomain.HumanTask{ID: taskID, RunID: runID, NodeRunID: nodeID, Status: workflowdomain.HumanTaskPending,
		ExpectedInputSchema: append(json.RawMessage(nil), mergeConfirmationSchema...), TargetVersion: 1}
	fixture := &humanReviewFixture{
		binding: organizingdomain.RunBinding{ID: semanticID(816), WorkspaceID: snapshot.WorkspaceID, SnapshotID: snapshot.ID,
			WorkflowRunID: runID, DefinitionKey: MergeDocumentsDefinitionKey, DefinitionVersion: DefinitionVersion, CreatedAt: snapshot.CreatedAt},
		snapshot: snapshot,
		definition: workflowdomain.Definition{ID: run.DefinitionID, WorkspaceID: run.WorkspaceID, Key: MergeDocumentsDefinitionKey,
			Version: DefinitionVersion, CreatedAt: snapshot.CreatedAt},
		node: workflowdomain.NodeRun{ID: nodeID, RunID: runID, NodeKey: MergeConfirmationNodeKind, NodeType: MergeConfirmationNodeKind,
			Status: workflowdomain.NodeStatusWaitingForHuman, Input: append(json.RawMessage(nil), encoded...), InputSchemaVersion: InputSchemaVersion,
			OutputSchemaVersion: OutputSchemaVersion},
		stage: encoded,
	}
	return fixture, run, task, receipt
}

func validSnapshotHash(t *testing.T, snapshot organizingdomain.Snapshot) string {
	t.Helper()
	snapshot.Hash = ""
	hash, err := organizingdomain.ComputeSnapshotHash(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func mustHumanReviewProjector(t *testing.T, fixture *humanReviewFixture) *HumanTaskReviewProjector {
	t.Helper()
	projector, err := NewHumanTaskReviewProjector(HumanTaskReviewDependencies{
		Bindings: fixture, Snapshots: fixture, Stages: fixture, Nodes: fixture, Definitions: fixture,
	})
	if err != nil {
		t.Fatal(err)
	}
	return projector
}

func reviewErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
