//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/postgres"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
)

func TestWorkspaceAnalysisDynamicCitationRejectionFinalizationIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	// E2 has a real immutable Source and Read receipt but no confirmed Claim.
	gates := seedWorkspaceAnalysisDynamicCandidateGates(t, platform, ctx, []string{"E2"}, false)
	review := reviewWorkspaceAnalysisModelAuthorizationCommand(t, gates.fixture, gates.candidate)
	if _, err := gates.tools.model.AuthorizeWorkspaceAnalysisModelCall(ctx, review); err == nil {
		t.Fatal("failed final citation gate allowed an independent review")
	}
	operation := lastDynamicOperation(t, ctx, gates.tools)
	lookup := dynamicFinalizerLookup(gates.tools.command)
	lookup.NodeRunID, lookup.NodeAttemptID = workspaceAnalysisModelIntegrationID(180), workspaceAnalysisModelIntegrationID(181)
	command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
		Reason: domain.WorkspaceAnalysisRunCitationInvalid, OperationID: &operation.ID,
		Artifact: &conversationapplication.WorkspaceAnalysisTerminationArtifact{
			Kind: conversationapplication.WorkspaceAnalysisTerminationArtifactToolReceipt,
			ID:   gates.validation.ResultReceiptID, Hash: gates.validation.Call.ResponseHash,
		},
	}
	finalizer, _ := dynamicFinalizerDependencies(t, platform)
	wrong := command
	wrong.Artifact = &conversationapplication.WorkspaceAnalysisTerminationArtifact{
		Kind: conversationapplication.WorkspaceAnalysisTerminationArtifactToolReceipt,
		ID:   gates.loopValidation.ResultReceiptID, Hash: gates.loopValidation.Call.ResponseHash,
	}
	if _, _, err := finalizer.FinalizeTermination(ctx, wrong); err == nil {
		t.Fatal("loop citation rejection replaced the independent final gate proof")
	}
	assertDynamicRefusalFinalization(t, ctx, platform, command, 9, 5, 4)
}

func TestWorkspaceAnalysisDynamicReviewRejectionFinalizationIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	gates := seedWorkspaceAnalysisDynamicCandidateGates(t, platform, ctx, []string{"E1"}, true)
	success := finalizeWorkspaceAnalysisDynamicReview(t, platform, ctx, gates, false)
	finalizer, _ := dynamicFinalizerDependencies(t, platform)
	if _, _, err := finalizer.FinalizeSuccess(ctx, success); err == nil {
		t.Fatal("a failed independent review published a successful answer")
	}
	operation := lastDynamicOperation(t, ctx, gates.tools)
	command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: success.WorkspaceAnalysisPublicationLookup, ExpectedAnswerVersion: 1,
		Reason: domain.WorkspaceAnalysisRunFaithfulnessRejected, OperationID: &operation.ID,
		Artifact: &conversationapplication.WorkspaceAnalysisTerminationArtifact{
			Kind: conversationapplication.WorkspaceAnalysisTerminationArtifactModelResult,
			ID:   success.ReviewModelResultID, Hash: success.ReviewModelResultHash,
		},
	}
	assertDynamicRefusalFinalization(t, ctx, platform, command, 10, 6, 4)
}

func TestWorkspaceAnalysisDynamicModelFailureFinalizationIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	seed := seedWorkspaceAnalysisDecisionIntegration(t, ctx, platform.DB(), 0)
	model := openGORMWorkspaceAnalysisModelRepositoryIntegration(t, platform).(*GORMWorkspaceAnalysisRepository)
	authorized, err := model.AuthorizeWorkspaceAnalysisModelCall(ctx, seed.command)
	if err != nil {
		t.Fatal(err)
	}
	completion := application.FinalizeWorkspaceAnalysisDecisionCommand{
		Identity: seed.command.Identity, OperationKey: seed.command.OperationKey,
		OperationID: authorized.OperationID, ExpectedCallVersion: authorized.Call.Version,
		Status: domain.ModelCallFailed, ErrorCode: "MODEL_PROVIDER_FAILED",
		Usage: domain.TokenUsage{InputTokens: 12, OutputTokens: 0, TotalTokens: 12}, LatencyMillis: 1,
	}
	if _, err := model.FinalizeWorkspaceAnalysisDecision(ctx, completion); err != nil {
		t.Fatalf("dynamic failed decision settlement: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	lookup := dynamicFinalizerLookup(seed.command)
	command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
		Reason: domain.WorkspaceAnalysisRunModelFailed, OperationID: &authorized.OperationID,
	}
	finalizer, timelines := dynamicFinalizerDependencies(t, platform)
	output, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || replayed || output.SchemaVersion != 2 || output.PublicationStatus != conversationdomain.WorkspaceAnalysisPublicationFailed {
		t.Fatalf("dynamic model failure publication: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	again, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || !replayed || again.ProofID != output.ProofID || again.ResultHash != output.ResultHash {
		t.Fatalf("dynamic model failure publication replay: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	assertDynamicFinalizationHasNoLiveModel(t, ctx, platform, lookup.AnalysisRunID)
	assertDynamicTerminalTimeline(t, ctx, timelines, lookup, conversationdomain.WorkspaceAnalysisTimelineRunFailed,
		domain.WorkspaceAnalysisRunModelFailed, 1, 1, 0)
}

func TestWorkspaceAnalysisDynamicPublicationDeduplicatesSourceAliasesIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	// Two searches bind E1 and E6 to the same immutable source. Both readings
	// remain separate private authority facts and project to one public Citation.
	gates := seedWorkspaceAnalysisDynamicCandidateGates(t, platform, ctx, []string{"E1", "E6"}, true)
	command := finalizeWorkspaceAnalysisDynamicReview(t, platform, ctx, gates, true)
	finalizer, timelines := dynamicFinalizerDependencies(t, platform)
	output, replayed, err := finalizer.FinalizeSuccess(ctx, command)
	if err != nil || replayed || output.SchemaVersion != 2 || output.PublicationStatus != conversationdomain.AnswerPublicationCompleted {
		t.Fatalf("dynamic alias publication: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	again, replayed, err := finalizer.FinalizeSuccess(ctx, command)
	if err != nil || !replayed || again.ProofID != output.ProofID || again.ResultHash != output.ResultHash {
		t.Fatalf("dynamic alias publication replay: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	var document []byte
	if err := platform.DB().QueryRow(ctx, `SELECT result FROM agent.answer WHERE id=$1`, string(command.AnswerID)).Scan(&document); err != nil {
		t.Fatal(err)
	}
	var result conversationdomain.WorkspaceAnalysisAnswerResultV2
	if err := json.Unmarshal(document, &result); err != nil || result.Validate() != nil || len(result.Payload.Citations) != 1 ||
		result.Payload.GitStatus != nil || result.Payload.Budget.ModelCalls != 8 || result.Payload.Budget.ToolCalls != 6 {
		t.Fatalf("dynamic public aliases were not deduplicated: %v", err)
	}
	assertDynamicTerminalTimeline(t, ctx, timelines, command.WorkspaceAnalysisPublicationLookup, conversationdomain.WorkspaceAnalysisTimelineRunSucceeded,
		domain.WorkspaceAnalysisRunCompleted, 14, 8, 6)
	gates.tools.assertCounts(t, ctx, platform, 8, 6, 2, 10, 0)
	if gates.tools.counts["SearchKnowledge"].Load() != 2 || gates.tools.counts["ReadSource"].Load() != 2 || gates.tools.counts["ValidateCitation"].Load() != 2 {
		t.Fatal("publication replay repeated source or citation execution")
	}
}

func TestWorkspaceAnalysisDynamicToolFailureFinalizationIntegration(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name, reason, status := "failed", domain.WorkspaceAnalysisRunToolFailed, toolsdomain.CallFailed
		cause := errors.New("injected Git execution failure")
		if unknown {
			name, reason, status = "unknown", domain.WorkspaceAnalysisRunResultUnknown, toolsdomain.CallUnknown
			cause = foundation.NewError(foundation.ErrorManualRecoveryRequired, "TOOL_OUTCOME_UNKNOWN", false, cause)
		}
		t.Run(name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			h := newDynamicToolsIntegration(t, ctx, platform)
			service := dynamicFailureToolService(t, platform, h, cause)
			h.decide(t, ctx, 1, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionGitStatus})
			toolCommand := h.toolCommand(1, domain.WorkspaceAnalysisOperationGitStatus, "ReadGitStatus", `{}`)
			_, err := service.ExecuteWorkspaceAnalysisTool(ctx, toolCommand)
			terminal, ok := toolsapplication.WorkspaceAnalysisToolTerminalFromError(err)
			if !ok || terminal.CallStatus != status {
				t.Fatalf("dynamic tool terminal evidence: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
			}
			_, err = service.ExecuteWorkspaceAnalysisTool(ctx, toolCommand)
			repeated, ok := toolsapplication.WorkspaceAnalysisToolTerminalFromError(err)
			if !ok || repeated.OperationID != terminal.OperationID || repeated.CallStatus != status || h.counts["ReadGitStatus"].Load() != 1 {
				t.Fatalf("dynamic tool failure replay: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
			}
			lookup := dynamicFinalizerLookup(h.command)
			command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
				WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1, Reason: reason, OperationID: &terminal.OperationID,
			}
			finalizer, timelines := dynamicFinalizerDependencies(t, platform)
			output, replayed, err := finalizer.FinalizeTermination(ctx, command)
			if err != nil || replayed || output.SchemaVersion != 2 || output.PublicationStatus != conversationdomain.WorkspaceAnalysisPublicationFailed {
				t.Fatalf("dynamic tool failure publication: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
			}
			again, replayed, err := finalizer.FinalizeTermination(ctx, command)
			if err != nil || !replayed || again.ProofID != output.ProofID || again.ResultHash != output.ResultHash {
				t.Fatalf("dynamic tool failure publication replay: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
			}
			assertDynamicFinalizationHasNoLiveModel(t, ctx, platform, lookup.AnalysisRunID)
			assertDynamicTerminalTimeline(t, ctx, timelines, lookup, conversationdomain.WorkspaceAnalysisTimelineRunFailed, reason, 2, 1, 1)
			h.assertCounts(t, ctx, platform, 1, 1, 0, 0, 0)
		})
	}
}

type dynamicErrorToolExecutor struct{ cause error }

func (executor dynamicErrorToolExecutor) Execute(context.Context, toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	return toolsapplication.ExecutorResult{}, executor.cause
}

func dynamicFailureToolService(t *testing.T, platform *platformpostgres.Pool, h *dynamicToolsIntegration, cause error) *toolsapplication.ExecutionService {
	t.Helper()
	contracts, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	ref := toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 3}
	contract, err := contracts.ResolveContract(ref)
	if err != nil {
		t.Fatal(err)
	}
	registry := toolsapplication.NewExecutionRegistry()
	if err := registry.RegisterContract(contract); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterExecutor(ref, dynamicToolsCountedExecutor{inner: dynamicErrorToolExecutor{cause: cause}, count: h.counts["ReadGitStatus"]}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	policy, err := workflowpostgres.NewGORMToolExecutionPolicySnapshot(platform)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := workflowpostgres.NewGORMToolCallRecoveryFence(platform)
	if err != nil {
		t.Fatal(err)
	}
	base, err := toolpostgres.NewGORMRepository(platform, policy, recovery)
	if err != nil {
		t.Fatal(err)
	}
	repository := &dynamicToolsCombinedRepository{base, h.tools}
	service, err := toolsapplication.NewExecutionService(registry, repository, repository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func lastDynamicOperation(t *testing.T, ctx context.Context, h *dynamicToolsIntegration) domain.WorkspaceAnalysisOperation {
	t.Helper()
	journal, err := h.model.LoadWorkspaceAnalysisJournal(ctx, application.WorkspaceAnalysisJournalQuery{
		WorkspaceID: h.command.Identity.WorkspaceID, WorkflowRunID: h.command.Identity.WorkflowRunID, AnalysisRunID: h.command.OperationKey.AnalysisRunID,
	})
	if err != nil || len(journal.Entries) == 0 {
		t.Fatalf("load dynamic terminal journal: %v", err)
	}
	return journal.Entries[len(journal.Entries)-1].Operation
}

func assertDynamicRefusalFinalization(t *testing.T, ctx context.Context, platform *platformpostgres.Pool, command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand, items int, models, tools int64) {
	t.Helper()
	finalizer, timelines := dynamicFinalizerDependencies(t, platform)
	output, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || replayed || output.SchemaVersion != 2 || output.PublicationStatus != conversationdomain.AnswerPublicationRefused ||
		output.ResultType != conversationdomain.AnswerResultWorkspaceAnalysisRefusal {
		t.Fatalf("dynamic gate refusal: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	again, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || !replayed || again.ProofID != output.ProofID || again.ResultHash != output.ResultHash {
		t.Fatalf("dynamic gate refusal replay: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	assertDynamicFinalizationHasNoLiveModel(t, ctx, platform, command.AnalysisRunID)
	assertDynamicTerminalTimeline(t, ctx, timelines, command.WorkspaceAnalysisPublicationLookup, conversationdomain.WorkspaceAnalysisTimelineRunRefused,
		command.Reason, items, models, tools)
}
