//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationpostgres "github.com/CodeZen-Lizhi/zhixu/internal/conversation/adapter/postgres"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func TestWorkspaceAnalysisDynamicFinishWithoutEvidencePublishesRefusalIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	testWorkspaceAnalysisDynamicFinishWithoutEvidencePublishesRefusal(t, platform, ctx)
}

func testWorkspaceAnalysisDynamicFinishWithoutEvidencePublishesRefusal(t *testing.T, platform *platformpostgres.Pool, ctx context.Context) {
	t.Helper()
	h := newDynamicToolsIntegration(t, ctx, platform)
	h.decide(t, ctx, 1, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionFinish})
	journal, err := h.model.LoadWorkspaceAnalysisJournal(ctx, application.WorkspaceAnalysisJournalQuery{
		WorkspaceID: h.command.Identity.WorkspaceID, WorkflowRunID: h.command.Identity.WorkflowRunID, AnalysisRunID: h.command.OperationKey.AnalysisRunID,
	})
	if err != nil || len(journal.Entries) != 1 || journal.Entries[0].Decision == nil {
		t.Fatalf("finish receipt: %v", err)
	}
	entry := journal.Entries[0]
	seedWorkspaceAnalysisDynamicPublicationNodes(t, platform, ctx, h.command, "synthesize_answer")
	lookup := dynamicFinalizerLookup(h.command)
	lookup.NodeRunID, lookup.NodeAttemptID = workspaceAnalysisModelIntegrationID(160), workspaceAnalysisModelIntegrationID(161)
	command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1, Reason: domain.WorkspaceAnalysisRunEvidenceInsufficient,
		OperationID: &entry.Operation.ID,
		Artifact: &conversationapplication.WorkspaceAnalysisTerminationArtifact{
			Kind: conversationapplication.WorkspaceAnalysisTerminationArtifactDecisionReceipt, ID: entry.Decision.ID, Hash: entry.Decision.DocumentHash,
		},
	}
	finalizer, timelines := dynamicFinalizerDependencies(t, platform)
	output, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || replayed || output.SchemaVersion != 2 || output.PublicationStatus != conversationdomain.AnswerPublicationRefused ||
		output.ResultType != conversationdomain.AnswerResultWorkspaceAnalysisRefusal {
		t.Fatalf("finish without evidence did not publish a v2 refusal: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	again, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || !replayed || again.ProofID != output.ProofID || again.ResultHash != output.ResultHash || again.SchemaVersion != 2 {
		t.Fatalf("no-evidence refusal did not replay its proof: %v", err)
	}
	assertDynamicTerminalTimeline(t, ctx, timelines, lookup, conversationdomain.WorkspaceAnalysisTimelineRunRefused,
		domain.WorkspaceAnalysisRunEvidenceInsufficient, 1, 1, 0)
	assertDynamicFinalizationHasNoLiveModel(t, ctx, platform, lookup.AnalysisRunID)
}

func TestWorkspaceAnalysisDynamicDecisionBudgetFinalizationClosesModelIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	testWorkspaceAnalysisDynamicDecisionBudgetFinalizationClosesModel(t, platform, ctx)
}

func testWorkspaceAnalysisDynamicDecisionBudgetFinalizationClosesModel(t *testing.T, platform *platformpostgres.Pool, ctx context.Context) {
	t.Helper()
	modelCommand, denial := testWorkspaceAnalysisDynamicDecisionBudgetDenial(t, platform, ctx)
	lookup := dynamicFinalizerLookup(modelCommand)
	command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1, Reason: denial.Reason, OperationID: &denial.OperationID,
		BudgetRequest: &conversationapplication.WorkspaceAnalysisBudgetRequest{
			ModelCalls: denial.Requested.ModelCalls, ToolCalls: denial.Requested.ToolCalls, SourceReads: denial.Requested.SourceReads,
			InputTokens: denial.Requested.InputTokens, OutputTokens: denial.Requested.OutputTokens,
		},
	}
	finalizer, timelines := dynamicFinalizerDependencies(t, platform)
	output, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || replayed || output.SchemaVersion != 2 || output.PublicationStatus != conversationdomain.WorkspaceAnalysisPublicationFailed ||
		output.ResultType != conversationdomain.AnswerResultWorkspaceAnalysisTermination {
		t.Fatalf("budget denial did not publish a v2 termination: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	assertDynamicFinalizationHasNoLiveModel(t, ctx, platform, lookup.AnalysisRunID)
	again, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || !replayed || again.ProofID != output.ProofID || again.ResultHash != output.ResultHash || again.SchemaVersion != 2 {
		t.Fatalf("budget termination did not replay its proof: %v", err)
	}
	assertDynamicTerminalTimeline(t, ctx, timelines, lookup, conversationdomain.WorkspaceAnalysisTimelineRunFailed,
		domain.WorkspaceAnalysisRunBudgetExhausted, 25, 12, 12)
	var models, calls, reservations, pending int
	if err := platform.DB().QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.model_run WHERE workflow_run_id=analysis.workflow_run_id),
		(SELECT count(*) FROM agent.model_call call_row JOIN agent.model_run model ON model.id=call_row.model_run_id WHERE model.workflow_run_id=analysis.workflow_run_id),
		(SELECT count(*) FROM agent.workspace_analysis_budget_reservation WHERE analysis_run_id=analysis.id),
		(SELECT count(*) FROM agent.workspace_analysis_operation WHERE analysis_run_id=analysis.id AND status='PENDING')
		FROM agent.workspace_analysis_run analysis WHERE id=$1`, string(lookup.AnalysisRunID)).Scan(&models, &calls, &reservations, &pending); err != nil {
		t.Fatal(err)
	}
	if models != 1 || calls != 12 || reservations != 24 || pending != 1 {
		t.Fatalf("finalization created extra calls or consumed the refusal slot: models=%d calls=%d reservations=%d pending=%d", models, calls, reservations, pending)
	}
}

func TestWorkspaceAnalysisDynamicTimelineReadsJournalOrderIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	testWorkspaceAnalysisDynamicTimelineReadsJournalOrder(t, platform, ctx)
}

func TestWorkspaceAnalysisDynamicDeadlineFinalizationIntegration(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "before_operation"
		if pending {
			name = "durable_pending"
		}
		t.Run(name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			var modelCommand application.AuthorizeWorkspaceAnalysisModelCallCommand
			var operationID *foundation.ID
			items := 0
			if pending {
				var denial application.WorkspaceAnalysisAdmissionDenial
				modelCommand, denial = testWorkspaceAnalysisDynamicDecisionDeadlineDenial(t, platform, ctx)
				operationID, items = &denial.OperationID, 1
			} else {
				modelCommand = seedWorkspaceAnalysisDecisionIntegration(t, ctx, platform.DB(), 36*time.Minute).command
			}
			lookup := dynamicFinalizerLookup(modelCommand)
			command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
				WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
				Reason: domain.WorkspaceAnalysisRunDeadlineExceeded, OperationID: operationID,
			}
			finalizer, timelines := dynamicFinalizerDependencies(t, platform)
			output, replayed, err := finalizer.FinalizeTermination(ctx, command)
			if err != nil || replayed || output.SchemaVersion != 2 || output.PublicationStatus != conversationdomain.WorkspaceAnalysisPublicationFailed {
				t.Fatalf("dynamic deadline publication: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
			}
			again, replayed, err := finalizer.FinalizeTermination(ctx, command)
			if err != nil || !replayed || again.ProofID != output.ProofID || again.ResultHash != output.ResultHash {
				t.Fatalf("dynamic deadline publication replay: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
			}
			assertDynamicFinalizationHasNoLiveModel(t, ctx, platform, lookup.AnalysisRunID)
			assertDynamicTerminalTimeline(t, ctx, timelines, lookup, conversationdomain.WorkspaceAnalysisTimelineRunFailed,
				domain.WorkspaceAnalysisRunDeadlineExceeded, items, 0, 0)
		})
	}
}

func TestWorkspaceAnalysisDynamicUnknownFinalizationIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	modelCommand := testWorkspaceAnalysisDynamicDecisionReplacementChargesUnknown(t, platform, ctx)
	lookup := dynamicFinalizerLookup(modelCommand)
	command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
		Reason: domain.WorkspaceAnalysisRunResultUnknown, OperationID: &modelCommand.OperationID,
	}
	finalizer, timelines := dynamicFinalizerDependencies(t, platform)
	output, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || replayed || output.SchemaVersion != 2 || output.PublicationStatus != conversationdomain.WorkspaceAnalysisPublicationFailed {
		t.Fatalf("dynamic unknown publication: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	again, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || !replayed || again.ProofID != output.ProofID || again.ResultHash != output.ResultHash {
		t.Fatalf("dynamic unknown publication replay: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	assertDynamicFinalizationHasNoLiveModel(t, ctx, platform, lookup.AnalysisRunID)
	assertDynamicTerminalTimeline(t, ctx, timelines, lookup, conversationdomain.WorkspaceAnalysisTimelineRunFailed,
		domain.WorkspaceAnalysisRunResultUnknown, 1, 1, 0)
	var input, outputTokens int64
	if err := platform.DB().QueryRow(ctx, `SELECT settled_input_tokens,settled_output_tokens FROM agent.workspace_analysis_run WHERE id=$1`,
		string(lookup.AnalysisRunID)).Scan(&input, &outputTokens); err != nil || input != 65536 || outputTokens != 512 {
		t.Fatalf("unknown publication changed the full reserved charge: %v", err)
	}
}

func testWorkspaceAnalysisDynamicTimelineReadsJournalOrder(t *testing.T, platform *platformpostgres.Pool, ctx context.Context) {
	t.Helper()
	h := newDynamicToolsIntegration(t, ctx, platform)
	h.execute(t, ctx, 1, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionKnowledgeSearch, Query: ptrDynamicTool("approved evidence")}, "SearchKnowledge", `{"query":"approved evidence","mode":"keyword","limit":5}`)
	h.execute(t, ctx, 2, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionSourceRead, EvidenceRef: ptrDynamicTool("E1")}, "ReadSource", `{"evidence_ref":"E1"}`)
	h.execute(t, ctx, 3, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionKnowledgeSearch, Query: ptrDynamicTool("approved evidence")}, "SearchKnowledge", `{"query":"approved evidence","mode":"keyword","limit":5}`)
	h.execute(t, ctx, 4, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionSourceRead, EvidenceRef: ptrDynamicTool("E7")}, "ReadSource", `{"evidence_ref":"E7"}`)
	_, timelines := dynamicFinalizerDependencies(t, platform)
	query := conversationapplication.WorkspaceAnalysisTimelineQuery{WorkspaceID: h.command.Identity.WorkspaceID, AnswerID: workspaceAnalysisModelIntegrationID(13)}
	first, err := timelines.GetWorkspaceAnalysisTimeline(ctx, query)
	if err != nil || first.SchemaVersion != "v2" || len(first.Items) != 8 || first.Budget.ModelCalls.Used != 4 || first.Budget.ToolCalls.Used != 4 {
		t.Fatalf("public dynamic timeline: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	for index, phase := range []conversationdomain.WorkspaceAnalysisPhase{
		conversationdomain.WorkspaceAnalysisPhaseRetrieveEvidence, conversationdomain.WorkspaceAnalysisPhaseReadEvidence,
		conversationdomain.WorkspaceAnalysisPhaseRetrieveEvidence, conversationdomain.WorkspaceAnalysisPhaseReadEvidence,
	} {
		decision, tool := first.Items[2*index], first.Items[2*index+1]
		if decision.Sequence != 2*index+1 || decision.Phase != conversationdomain.WorkspaceAnalysisPhaseDecideNext ||
			tool.Sequence != 2*index+2 || tool.Phase != phase || tool.Status != conversationdomain.WorkspaceAnalysisTimelineItemSucceeded {
			t.Fatal("public timeline sorted by phase instead of journal order")
		}
	}
	if first.Items[3].Summary == nil || first.Items[3].Summary.Source == nil || first.Items[3].Summary.Source.EvidenceRef != "E1" ||
		first.Items[7].Summary == nil || first.Items[7].Summary.Source == nil || first.Items[7].Summary.Source.EvidenceRef != "E7" {
		t.Fatal("public timeline lost source aliases across searches")
	}
	second, err := timelines.GetWorkspaceAnalysisTimeline(ctx, query)
	if err != nil || len(second.Items) != len(first.Items) || second.LatestServerEventSequence != first.LatestServerEventSequence {
		t.Fatalf("public timeline refresh changed durable sequence: %v", err)
	}
	query.WorkspaceID = workspaceAnalysisModelIntegrationID(999)
	if _, err := timelines.GetWorkspaceAnalysisTimeline(ctx, query); err == nil {
		t.Fatal("public timeline crossed its workspace")
	}
	if h.counts["SearchKnowledge"].Load() != 2 || h.counts["ReadSource"].Load() != 2 {
		t.Fatal("timeline refresh invoked a tool")
	}
}

func dynamicFinalizerLookup(command application.AuthorizeWorkspaceAnalysisModelCallCommand) conversationapplication.WorkspaceAnalysisPublicationLookup {
	return conversationapplication.WorkspaceAnalysisPublicationLookup{
		AnswerPublicationLookup: conversationapplication.AnswerPublicationLookup{
			WorkspaceID: command.Identity.WorkspaceID, WorkflowRunID: command.Identity.WorkflowRunID,
			NodeRunID: command.Identity.NodeRunID, NodeAttemptID: command.Identity.NodeAttemptID,
			ConversationID: workspaceAnalysisModelIntegrationID(2), QuestionID: workspaceAnalysisModelIntegrationID(10), AnswerID: workspaceAnalysisModelIntegrationID(13),
		},
		AnalysisRunID: command.OperationKey.AnalysisRunID, DefinitionVersion: command.Identity.DefinitionVersion,
		ExpectedLeaseOwner: command.Identity.LeaseOwner, ExpectedLeaseFence: command.Identity.LeaseFence,
	}
}

func dynamicFinalizerDependencies(t *testing.T, platform *platformpostgres.Pool) (*conversationpostgres.GORMWorkspaceAnalysisFinalizer, *conversationpostgres.GORMRepository) {
	t.Helper()
	events, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := conversationpostgres.NewGORMWorkspaceAnalysisFinalizer(platform, events, foundation.NewUUIDGenerator(nil))
	if err != nil {
		t.Fatal(err)
	}
	timelines, err := conversationpostgres.NewGORMRepository(platform, events)
	if err != nil {
		t.Fatal(err)
	}
	return finalizer, timelines
}

func assertDynamicFinalizationHasNoLiveModel(t *testing.T, ctx context.Context, platform *platformpostgres.Pool, analysisRunID foundation.ID) {
	t.Helper()
	var running, reserved, proofs int
	if err := platform.DB().QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.model_run model WHERE model.workflow_run_id=analysis.workflow_run_id AND model.status='RUNNING'),
		analysis.reserved_model_calls+analysis.reserved_tool_calls+analysis.reserved_source_reads,
		(SELECT count(*) FROM agent.workspace_analysis_termination_proof WHERE analysis_run_id=analysis.id)
		FROM agent.workspace_analysis_run analysis WHERE id=$1`, string(analysisRunID)).Scan(&running, &reserved, &proofs); err != nil {
		t.Fatal(err)
	}
	if running != 0 || reserved != 0 || proofs != 1 {
		t.Fatalf("terminal publication left active execution or duplicate proof: models=%d reserved=%d proofs=%d", running, reserved, proofs)
	}
}

func assertDynamicTerminalTimeline(t *testing.T, ctx context.Context, timelines *conversationpostgres.GORMRepository, lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
	status conversationdomain.WorkspaceAnalysisTimelineRunStatus, reason domain.WorkspaceAnalysisRunTerminationReason, items int, models, tools int64,
) {
	t.Helper()
	timeline, err := timelines.GetWorkspaceAnalysisTimeline(ctx, conversationapplication.WorkspaceAnalysisTimelineQuery{WorkspaceID: lookup.WorkspaceID, AnswerID: lookup.AnswerID})
	if err != nil || timeline.SchemaVersion != "v2" || timeline.RunStatus != status || timeline.TerminationReason == nil || *timeline.TerminationReason != string(reason) ||
		len(timeline.Items) != items || timeline.Budget.ModelCalls.Used != models || timeline.Budget.ToolCalls.Used != tools {
		t.Fatalf("terminal public timeline differs from its proof and budget: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
}
