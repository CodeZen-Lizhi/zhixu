package postgres

import (
	"strings"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
)

func TestWorkspaceAnalysisTimelineV2ProjectsGlobalSourceAndExactTool(t *testing.T) {
	t.Parallel()
	run := workspaceAnalysisV2TimelineRunFixture()
	row := workspaceAnalysisV2TimelineSourceFixture()
	item, err := workspaceAnalysisTimelineOperationItemV2(run, row, 8)
	if err != nil {
		t.Fatal(err)
	}
	if item.Sequence != 8 || item.Phase != conversationdomain.WorkspaceAnalysisPhaseReadEvidence ||
		item.Kind != conversationdomain.WorkspaceAnalysisTimelineItemTool || item.ToolRef.Name != "ReadSource" || item.ToolRef.Version != 4 ||
		item.Summary.Source.EvidenceRef != "E32" || item.Summary.Source.ContentHash != strings.Repeat("a", 64) {
		t.Fatalf("source projection lost its journal identity: %#v", item)
	}
	if _, _, _, err := workspaceAnalysisTimelineOperationItem(run, row); err == nil {
		t.Fatal("v1 projection accepted a v2 dynamic operation")
	}
}

func TestWorkspaceAnalysisTimelineV2RejectsMixedVersionAndCallNamespaces(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*workspaceAnalysisTimelineRunRow, *workspaceAnalysisTimelineOperationRow)
	}{
		{"wrong policy", func(run *workspaceAnalysisTimelineRunRow, _ *workspaceAnalysisTimelineOperationRow) {
			run.policyVersion = 1
		}},
		{"wrong definition", func(run *workspaceAnalysisTimelineRunRow, _ *workspaceAnalysisTimelineOperationRow) {
			run.definitionVersion = 1
		}},
		{"legacy node", func(_ *workspaceAnalysisTimelineRunRow, row *workspaceAnalysisTimelineOperationRow) {
			row.nodeKey = "read_evidence"
			row.ordinal = 1
		}},
		{"old tool version", func(_ *workspaceAnalysisTimelineRunRow, row *workspaceAnalysisTimelineOperationRow) {
			version := int64(3)
			row.toolVersion = &version
		}},
		{"unknown tool version", func(_ *workspaceAnalysisTimelineRunRow, row *workspaceAnalysisTimelineOperationRow) {
			version := int64(5)
			row.toolVersion = &version
		}},
		{"wrong call kind", func(_ *workspaceAnalysisTimelineRunRow, row *workspaceAnalysisTimelineOperationRow) {
			row.callKind = "MODEL"
		}},
		{"cross call namespace", func(_ *workspaceAnalysisTimelineRunRow, row *workspaceAnalysisTimelineOperationRow) {
			status := "SUCCEEDED"
			row.modelCallStatus = &status
		}},
		{"missing call", func(_ *workspaceAnalysisTimelineRunRow, row *workspaceAnalysisTimelineOperationRow) {
			row.toolCallStatus = nil
		}},
		{"call has not completed", func(_ *workspaceAnalysisTimelineRunRow, row *workspaceAnalysisTimelineOperationRow) {
			status := "STARTED"
			row.toolCallStatus = &status
		}},
		{"source missing receipt ref", func(_ *workspaceAnalysisTimelineRunRow, row *workspaceAnalysisTimelineOperationRow) {
			row.sourceEvidenceRef = nil
		}},
		{"source ref overflow", func(_ *workspaceAnalysisTimelineRunRow, row *workspaceAnalysisTimelineOperationRow) {
			ref := "E33"
			row.sourceEvidenceRef = &ref
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run, row := workspaceAnalysisV2TimelineRunFixture(), workspaceAnalysisV2TimelineSourceFixture()
			test.mutate(&run, &row)
			if _, err := workspaceAnalysisTimelineOperationItemV2(run, row, 8); err == nil {
				t.Fatal("corrupt v2 operation projection accepted")
			}
		})
	}
}

func TestWorkspaceAnalysisTimelineV2DistinguishesLoopAndFinalCitationGates(t *testing.T) {
	t.Parallel()
	run := workspaceAnalysisV2TimelineRunFixture()
	for _, node := range []string{"decide_next", "validate_citations"} {
		status, name, reasons := "SUCCEEDED", "ValidateCitation", `["OK"]`
		version, duration, valid, invalid := int64(4), int64(10), int64(1), int64(0)
		ordinal := 3
		if node == "validate_citations" {
			ordinal = 1
		}
		row := workspaceAnalysisTimelineOperationRow{nodeKey: node, operationKind: "CITATION_VALIDATION", ordinal: ordinal,
			callKind: "TOOL", status: status, toolName: &name, toolVersion: &version, toolCallStatus: &status, durationMS: &duration,
			citationValid: &valid, citationInvalid: &invalid, citationReasons: &reasons}
		item, err := workspaceAnalysisTimelineOperationItemV2(run, row, 6)
		if err != nil || item.Phase != conversationdomain.WorkspaceAnalysisPhaseValidateCitations || item.ToolRef.Version != 4 || item.Summary.Citation.ValidCount != 1 {
			t.Fatalf("%s citation projection rejected: %v", node, err)
		}
	}
}

func TestWorkspaceAnalysisTimelineV2PendingAndUnknownPreserveLifecycle(t *testing.T) {
	t.Parallel()
	run := workspaceAnalysisV2TimelineRunFixture()
	pending := workspaceAnalysisTimelineOperationRow{nodeKey: "decide_next", operationKind: "DECISION", ordinal: 13, callKind: "MODEL", status: "PENDING"}
	item, err := workspaceAnalysisTimelineOperationItemV2(run, pending, 25)
	if err != nil || item.Status != conversationdomain.WorkspaceAnalysisTimelineItemPending || item.Summary != nil || item.ToolRef != nil || item.DurationMS != nil {
		t.Fatalf("budget denied decision lost its uncalled state: %v", err)
	}
	unknown := workspaceAnalysisV2TimelineSourceFixture()
	status, reason := "UNKNOWN", string(conversationdomain.WorkspaceAnalysisResultUnknown)
	unknown.status, unknown.toolCallStatus, unknown.errorCode = status, &status, &reason
	item, err = workspaceAnalysisTimelineOperationItemV2(run, unknown, 8)
	if err != nil || item.Status != conversationdomain.WorkspaceAnalysisTimelineItemUnknown || item.Summary != nil || item.ErrorCode == nil || *item.ErrorCode != reason {
		t.Fatalf("unknown operation exposed receipt contents or lost its cause: %v", err)
	}
	wrongStatus := "SUCCEEDED"
	unknown.toolCallStatus = &wrongStatus
	if _, err := workspaceAnalysisTimelineOperationItemV2(run, unknown, 8); err == nil {
		t.Fatal("unknown operation accepted a different call lifecycle")
	}
}

func TestWorkspaceAnalysisTimelineV2KeepsReceiptFailureAndModelRefusal(t *testing.T) {
	t.Parallel()
	run := workspaceAnalysisV2TimelineRunFixture()
	row := workspaceAnalysisV2TimelineSourceFixture()
	reason := string(conversationdomain.WorkspaceAnalysisReceiptInvalid)
	row.status, row.errorCode = "FAILED", &reason
	run.status, run.terminationReason = "failed", &reason
	if item, err := workspaceAnalysisTimelineOperationItemV2(run, row, 8); err != nil || item.Status != conversationdomain.WorkspaceAnalysisTimelineItemFailed || item.Summary != nil {
		t.Fatalf("successful tool response with failed receipt was misclassified: %v", err)
	}
	reason = string(conversationdomain.WorkspaceAnalysisModelRefused)
	run.status, run.terminationReason = "refused", &reason
	status, duration := "SUCCEEDED", int64(10)
	row = workspaceAnalysisTimelineOperationRow{nodeKey: "decide_next", operationKind: "DECISION", ordinal: 1, callKind: "MODEL", status: "FAILED",
		modelCallStatus: &status, durationMS: &duration, errorCode: &reason}
	if item, err := workspaceAnalysisTimelineOperationItemV2(run, row, 1); err != nil || item.Status != conversationdomain.WorkspaceAnalysisTimelineItemRefused || item.Summary != nil {
		t.Fatalf("model refusal was treated as a corrupt call: %v", err)
	}
}

func TestWorkspaceAnalysisTimelineFromV2RowsDoesNotReorderOrInventBudget(t *testing.T) {
	t.Parallel()
	run := workspaceAnalysisV2TimelineRunFixture()
	run.modelCalls, run.toolCalls, run.sourceReads = 2, 1, 0
	run.inputTokens, run.outputTokens = 600, 80
	items := []workspaceAnalysisTimelineSortableItem{
		{phaseOrder: 7, item: conversationdomain.WorkspaceAnalysisTimelineItem{Sequence: 1, Phase: conversationdomain.WorkspaceAnalysisPhaseDecideNext}},
		{phaseOrder: 2, item: conversationdomain.WorkspaceAnalysisTimelineItem{Sequence: 2, Phase: conversationdomain.WorkspaceAnalysisPhaseRetrieveEvidence}},
		{phaseOrder: 7, item: conversationdomain.WorkspaceAnalysisTimelineItem{Sequence: 3, Phase: conversationdomain.WorkspaceAnalysisPhaseDecideNext}},
	}
	timeline := workspaceAnalysisTimelineFromRows(run, items)
	if timeline.SchemaVersion != conversationdomain.WorkspaceAnalysisTimelineSchemaVersionV2 ||
		timeline.Items[0].Phase != conversationdomain.WorkspaceAnalysisPhaseDecideNext || timeline.Items[1].Phase != conversationdomain.WorkspaceAnalysisPhaseRetrieveEvidence ||
		timeline.Items[2].Phase != conversationdomain.WorkspaceAnalysisPhaseDecideNext || timeline.Budget.SourceReads.Used != 0 ||
		timeline.Budget.ModelCalls.Used != 2 || timeline.Budget.ToolCalls.Used != 1 || timeline.Budget.InputTokens.Used != 600 {
		t.Fatal("v2 projection regrouped operations or generated ledger usage from items")
	}
}

func workspaceAnalysisV2TimelineRunFixture() workspaceAnalysisTimelineRunRow {
	return workspaceAnalysisTimelineRunRow{
		analysisRunID: workspaceAnalysisFinalizerAdapterTestID(401), workspaceID: workspaceAnalysisFinalizerAdapterTestID(402),
		answerID: workspaceAnalysisFinalizerAdapterTestID(403), workflowRunID: workspaceAnalysisFinalizerAdapterTestID(404),
		definitionVersion: 2, policyVersion: 2, status: "running",
		maxModelCalls: agentdomain.WorkspaceAnalysisV2MaxModelCalls, maxToolCalls: agentdomain.WorkspaceAnalysisV2MaxToolCalls,
		maxSourceReads: agentdomain.WorkspaceAnalysisV2MaxSourceReads, maxInputTokens: agentdomain.WorkspaceAnalysisV2MaxRunInputTokens,
		maxOutputTokens: agentdomain.WorkspaceAnalysisV2MaxRunOutputTokens,
	}
}

func workspaceAnalysisV2TimelineSourceFixture() workspaceAnalysisTimelineOperationRow {
	status, name, ref, hash := "SUCCEEDED", "ReadSource", "E32", strings.Repeat("a", 64)
	version, duration, truncated := int64(4), int64(10), false
	return workspaceAnalysisTimelineOperationRow{nodeKey: "decide_next", operationKind: "SOURCE_READ", ordinal: 4,
		callKind: "TOOL", status: status, durationMS: &duration, toolCallStatus: &status, toolName: &name, toolVersion: &version,
		sourceEvidenceRef: &ref, sourceContentHash: &hash, sourceTruncated: &truncated}
}
