//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/postgres"
	toolretrieval "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/retrieval"
	toolworkspace "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/workspace"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
)

func TestWorkspaceAnalysisDynamicToolsSearchReadAliasReplayIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	testWorkspaceAnalysisDynamicToolsSearchReadAliasReplayIntegration(t, platform, ctx)
}

func testWorkspaceAnalysisDynamicToolsSearchReadAliasReplayIntegration(t *testing.T, platform *platformpostgres.Pool, ctx context.Context) {
	t.Helper()
	h := newDynamicToolsIntegration(t, ctx, platform)
	firstSearch := h.execute(t, ctx, 1, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionKnowledgeSearch, Query: ptrDynamicTool("approved evidence")}, "SearchKnowledge", `{"query":"approved evidence","mode":"keyword","limit":5}`)
	h.execute(t, ctx, 2, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionSourceRead, EvidenceRef: ptrDynamicTool("E1")}, "ReadSource", `{"evidence_ref":"E1"}`)
	secondSearch := h.execute(t, ctx, 3, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionKnowledgeSearch, Query: ptrDynamicTool("approved evidence")}, "SearchKnowledge", `{"query":"approved evidence","mode":"keyword","limit":5}`)
	secondRead := h.execute(t, ctx, 4, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionSourceRead, EvidenceRef: ptrDynamicTool("E7")}, "ReadSource", `{"evidence_ref":"E7"}`)
	h.execute(t, ctx, 5, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionCitationValidation, EvidenceRefs: []string{"E1", "E7"}}, "ValidateCitation", `{"candidate_id":null,"candidate_hash":null,"evidence_refs":["E1","E7"]}`)
	citationOutput, err := h.tools.LoadWorkspaceAnalysisDynamicToolOutput(ctx, h.outputQuery(5, domain.WorkspaceAnalysisOperationCitationValidation))
	if err != nil {
		t.Fatal(err)
	}
	var citationDocument struct {
		Results []struct {
			EvidenceRef string `json:"evidence_ref"`
			Valid       bool   `json:"valid"`
			ReasonCode  string `json:"reason_code"`
		} `json:"results"`
	}
	if err := json.Unmarshal(citationOutput, &citationDocument); err != nil || len(citationDocument.Results) != 2 {
		t.Fatalf("citation did not validate both searches: %v", err)
	}
	// Only the first Source is backed by a confirmed Claim. Merely reading
	// another immutable Source does not grant it formal evidence eligibility.
	for i, want := range []struct {
		ref    string
		valid  bool
		reason string
	}{{"E1", true, "OK"}, {"E7", false, "EVIDENCE_INELIGIBLE"}} {
		result := citationDocument.Results[i]
		if result.EvidenceRef != want.ref || result.Valid != want.valid || result.ReasonCode != want.reason {
			t.Fatalf("citation %s result valid=%t reason=%s did not match formal knowledge eligibility", want.ref, result.Valid, result.ReasonCode)
		}
	}

	for _, test := range []struct {
		ordinal     int
		first, last string
	}{{1, "E1", "E5"}, {3, "E6", "E10"}} {
		output, err := h.tools.LoadWorkspaceAnalysisDynamicToolOutput(ctx, h.outputQuery(test.ordinal, domain.WorkspaceAnalysisOperationKnowledgeSearch))
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			Items []struct {
				EvidenceRef string `json:"evidence_ref"`
			} `json:"items"`
		}
		if json.Unmarshal(output, &document) != nil || len(document.Items) != 5 || document.Items[0].EvidenceRef != test.first || document.Items[4].EvidenceRef != test.last {
			t.Fatalf("search %d global aliases changed: %s", test.ordinal, output)
		}
	}
	if firstSearch.ResultReceiptID == secondSearch.ResultReceiptID {
		t.Fatal("two searches reused a receipt")
	}
	authority, err := h.tools.LoadReadSourceV4Authority(ctx, toolsapplication.ReadSourceV4AuthorityQuery{WorkspaceID: h.command.Identity.WorkspaceID, WorkflowRunID: h.command.Identity.WorkflowRunID, EvidenceRef: "E7"})
	if err != nil || authority.SearchReceipt.ID != secondSearch.ResultReceiptID || authority.SearchEvidenceRef != "E2" {
		t.Fatalf("global reference lost its exact second Search: %v", err)
	}
	replayed, err := h.service.ExecuteWorkspaceAnalysisTool(ctx, h.toolCommand(4, domain.WorkspaceAnalysisOperationSourceRead, "ReadSource", `{"evidence_ref":"E7"}`))
	if err != nil || !replayed.Replayed || replayed.Call.ID != secondRead.Call.ID || replayed.ResultReceiptID != secondRead.ResultReceiptID || h.counts["ReadSource"].Load() != 2 {
		t.Fatalf("completed tool repeated work: %v", err)
	}
	h.assertCounts(t, ctx, platform, 5, 5, 2, 10, 0)
	query := toolsapplication.ReadSourceV4AuthorityQuery{WorkspaceID: workspaceAnalysisModelIntegrationID(999), WorkflowRunID: h.command.Identity.WorkflowRunID, EvidenceRef: "E7"}
	if _, err := h.tools.LoadReadSourceV4Authority(ctx, query); err == nil {
		t.Fatal("cross-workspace evidence was readable")
	}

	// BEFORE INSERT must reject a forged tuple, independently of the Go loader.
	_, err = platform.DB().Exec(ctx, `INSERT INTO agent.workspace_analysis_evidence(workspace_id,analysis_run_id,reference_no,search_operation_id,search_receipt_id,search_receipt_hash,local_evidence_ref,citation_id,index_version_id,chunk_id,source_version_id,source_span_id,content_hash,created_at)
	SELECT workspace_id,analysis_run_id,11,search_operation_id,search_receipt_id,search_receipt_hash,local_evidence_ref,citation_id,index_version_id,chunk_id,source_version_id,source_span_id,repeat('f',64),clock_timestamp() FROM agent.workspace_analysis_evidence WHERE analysis_run_id=$1 AND reference_no=1`, string(h.command.OperationKey.AnalysisRunID))
	if platformpostgres.SQLState(err) != "23514" || !strings.Contains(err.Error(), "dynamic evidence alias") {
		t.Fatalf("database accepted a forged alias or failed for an unrelated reason: %v", err)
	}

	oldIdentity := h.command.Identity
	advanceWorkspaceAnalysisModelAttemptIntegration(t, ctx, platform.DB())
	h.command.Identity.NodeAttemptID = workspaceAnalysisModelIntegrationID(90)
	h.command.Identity.LeaseFence = 2
	replacement, err := h.service.ExecuteWorkspaceAnalysisTool(ctx, h.toolCommand(4, domain.WorkspaceAnalysisOperationSourceRead, "ReadSource", `{"evidence_ref":"E7"}`))
	if err != nil || !replacement.Replayed || replacement.Call.ID != secondRead.Call.ID || replacement.ResultReceiptID != secondRead.ResultReceiptID || h.counts["ReadSource"].Load() != 2 {
		t.Fatalf("replacement Worker repeated a completed Read: %v", err)
	}
	stale := h.toolCommand(4, domain.WorkspaceAnalysisOperationSourceRead, "ReadSource", `{"evidence_ref":"E7"}`)
	stale.Tool.Identity = dynamicToolsIdentity(oldIdentity)
	if _, err := h.service.ExecuteWorkspaceAnalysisTool(ctx, stale); err == nil {
		t.Fatal("old fence retained tool authority")
	}
	persistWorkspaceAnalysisModelCancellation(t, ctx, platform, h.command.Identity.WorkflowRunID, "dynamic-tools-stop")
	if _, err := h.service.ExecuteWorkspaceAnalysisTool(ctx, h.toolCommand(4, domain.WorkspaceAnalysisOperationSourceRead, "ReadSource", `{"evidence_ref":"E7"}`)); err == nil {
		t.Fatal("cancelled analysis retained tool authority")
	}
	if h.counts["SearchKnowledge"].Load() != 2 || h.counts["ReadSource"].Load() != 2 || h.counts["ValidateCitation"].Load() != 1 {
		t.Fatal("replay, stale fence or cancellation invoked an executor")
	}
}

func TestWorkspaceAnalysisDynamicToolsEvidenceCapacityDenialIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	testWorkspaceAnalysisDynamicToolsEvidenceCapacityDenialIntegration(t, platform, ctx)
}

func testWorkspaceAnalysisDynamicToolsEvidenceCapacityDenialIntegration(t *testing.T, platform *platformpostgres.Pool, ctx context.Context) {
	t.Helper()
	h := newDynamicToolsIntegration(t, ctx, platform)
	for ordinal := 1; ordinal <= 7; ordinal++ {
		query := h.outputQuery(ordinal, domain.WorkspaceAnalysisOperationKnowledgeSearch)
		limit, err := h.tools.WorkspaceAnalysisDynamicSearchLimit(ctx, query)
		if err != nil || limit != min(5, 32-(ordinal-1)*5) {
			t.Fatalf("search %d capacity=%d err=%v", ordinal, limit, err)
		}
		h.execute(t, ctx, ordinal, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionKnowledgeSearch, Query: ptrDynamicTool("approved evidence")}, "SearchKnowledge", fmt.Sprintf(`{"query":"approved evidence","mode":"keyword","limit":%d}`, limit))
	}
	h.decide(t, ctx, 8, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionKnowledgeSearch, Query: ptrDynamicTool("approved evidence")})
	limit, err := h.tools.WorkspaceAnalysisDynamicSearchLimit(ctx, h.outputQuery(8, domain.WorkspaceAnalysisOperationKnowledgeSearch))
	if err != nil || limit != 0 {
		t.Fatalf("exhausted capacity=%d err=%v", limit, err)
	}
	command := h.toolCommand(8, domain.WorkspaceAnalysisOperationKnowledgeSearch, "SearchKnowledge", `{"query":"approved evidence","mode":"keyword","limit":1}`)
	_, err = h.service.ExecuteWorkspaceAnalysisTool(ctx, command)
	denial, ok := application.WorkspaceAnalysisAdmissionDenialFromError(err)
	if !ok || denial.Reason != domain.WorkspaceAnalysisRunBudgetExhausted || denial.Requested != (domain.WorkspaceAnalysisBudgetAmount{ToolCalls: 1}) {
		t.Fatalf("capacity did not produce durable pre-call denial: %v", err)
	}
	_, err = h.service.ExecuteWorkspaceAnalysisTool(ctx, command)
	replayed, ok := application.WorkspaceAnalysisAdmissionDenialFromError(err)
	if !ok || replayed.OperationID != denial.OperationID || replayed.Reason != denial.Reason {
		t.Fatalf("denial replay created a second pending operation: %v", err)
	}
	if h.counts["SearchKnowledge"].Load() != 7 {
		t.Fatal("exhausted capacity reached the Search executor")
	}
	h.assertCounts(t, ctx, platform, 8, 7, 0, 32, 1)
	// Replaying an earlier search computes its original capacity, not the current remainder.
	limit, err = h.tools.WorkspaceAnalysisDynamicSearchLimit(ctx, h.outputQuery(2, domain.WorkspaceAnalysisOperationKnowledgeSearch))
	if err != nil || limit != 5 {
		t.Fatalf("historical search capacity changed: %d %v", limit, err)
	}
}

func TestWorkspaceAnalysisDynamicToolsSourceReadBudgetDenialIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	testWorkspaceAnalysisDynamicToolsSourceReadBudgetDenialIntegration(t, platform, ctx)
}

func testWorkspaceAnalysisDynamicToolsSourceReadBudgetDenialIntegration(t *testing.T, platform *platformpostgres.Pool, ctx context.Context) {
	t.Helper()
	h := newDynamicToolsIntegration(t, ctx, platform)
	for ordinal := 1; ordinal <= 2; ordinal++ {
		h.execute(t, ctx, ordinal, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionKnowledgeSearch, Query: ptrDynamicTool("approved evidence")}, "SearchKnowledge", `{"query":"approved evidence","mode":"keyword","limit":5}`)
	}
	refs := make([]string, 8)
	for index := range refs {
		refs[index] = fmt.Sprintf("E%d", index+1)
		h.execute(t, ctx, index+3, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionSourceRead, EvidenceRef: &refs[index]}, "ReadSource", fmt.Sprintf(`{"evidence_ref":"%s"}`, refs[index]))
	}
	h.decide(t, ctx, 11, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionSourceRead, EvidenceRef: ptrDynamicTool("E9")})
	command := h.toolCommand(11, domain.WorkspaceAnalysisOperationSourceRead, "ReadSource", `{"evidence_ref":"E9"}`)
	_, err := h.service.ExecuteWorkspaceAnalysisTool(ctx, command)
	denial, ok := application.WorkspaceAnalysisAdmissionDenialFromError(err)
	if !ok || denial.Reason != domain.WorkspaceAnalysisRunBudgetExhausted || denial.Requested != (domain.WorkspaceAnalysisBudgetAmount{ToolCalls: 1, SourceReads: 1}) {
		t.Fatalf("source read budget did not produce durable pre-call denial: %v", err)
	}
	_, err = h.service.ExecuteWorkspaceAnalysisTool(ctx, command)
	replayed, ok := application.WorkspaceAnalysisAdmissionDenialFromError(err)
	if !ok || replayed.OperationID != denial.OperationID || replayed.Reason != denial.Reason || h.counts["ReadSource"].Load() != 8 {
		t.Fatalf("source read denial replay repeated an operation or read: %v", err)
	}
	h.assertCounts(t, ctx, platform, 11, 10, 8, 10, 1)
	evidence, err := h.tools.LoadWorkspaceAnalysisSynthesisEvidence(ctx, toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityQuery{
		WorkspaceID: h.command.Identity.WorkspaceID, WorkflowRunID: h.command.Identity.WorkflowRunID, AnalysisRunID: h.command.OperationKey.AnalysisRunID,
	})
	if err != nil || !slices.Equal(evidence.EvidenceRefs, refs) || len(evidence.SearchReceipts) != 2 || len(evidence.ReadSourceReceipts) != 8 || len(evidence.Evidence) != 8 {
		t.Fatalf("pending ninth read invalidated completed synthesis evidence: %v", err)
	}
}

type dynamicToolsIntegration struct {
	command application.AuthorizeWorkspaceAnalysisModelCallCommand
	model   *GORMWorkspaceAnalysisRepository
	tools   *toolpostgres.GORMWorkspaceAnalysisRepository
	service *toolsapplication.ExecutionService
	counts  map[string]*atomic.Int64
}

type dynamicToolsCombinedRepository struct {
	*toolpostgres.GORMRepository
	*toolpostgres.GORMWorkspaceAnalysisRepository
}

func newDynamicToolsIntegration(t *testing.T, ctx context.Context, platform *platformpostgres.Pool) *dynamicToolsIntegration {
	t.Helper()
	seed := seedWorkspaceAnalysisDecisionIntegration(t, ctx, platform.DB(), 0)
	search, artifacts := seedDynamicToolsEvidence(t, ctx, platform, seed.command.Identity.WorkspaceID)
	model := openGORMWorkspaceAnalysisModelRepositoryIntegration(t, platform).(*GORMWorkspaceAnalysisRepository)
	agent, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(platform)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	auditStore, err := auditpostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := auditapplication.NewRecorder(auditStore)
	if err != nil {
		t.Fatal(err)
	}
	analysisTools, err := toolpostgres.NewGORMWorkspaceAnalysisRepository(platform, fence, agent, agent, agent, events, audit)
	if err != nil {
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
	baseTools, err := toolpostgres.NewGORMRepository(platform, policy, recovery)
	if err != nil {
		t.Fatal(err)
	}
	repository := &dynamicToolsCombinedRepository{baseTools, analysisTools}
	evidenceStore, err := retrievalpostgres.NewGORMSearchRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := retrievalapplication.NewEvidenceReferenceService(evidenceStore, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	knowledge, err := knowledgepostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	eligibility, err := knowledgeapplication.NewEvidenceEligibilityService(knowledge)
	if err != nil {
		t.Fatal(err)
	}
	searchExecutor, err := toolretrieval.NewSearchKnowledgeV3Executor(search, evidenceStore)
	if err != nil {
		t.Fatal(err)
	}
	readExecutor, err := toolretrieval.NewReadSourceV4Executor(analysisTools, evidence)
	if err != nil {
		t.Fatal(err)
	}
	citationExecutor, err := toolretrieval.NewValidateCitationV4Executor(analysisTools, evidence, eligibility)
	if err != nil {
		t.Fatal(err)
	}
	gitExecutor, err := toolworkspace.NewReadGitStatusV3Executor(dynamicToolsGitInspector{})
	if err != nil {
		t.Fatal(err)
	}
	contracts, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	registry := toolsapplication.NewExecutionRegistry()
	counts := map[string]*atomic.Int64{}
	for _, entry := range []struct {
		ref      toolsdomain.ToolRef
		executor toolsapplication.Executor
	}{
		{toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 3}, searchExecutor},
		{toolsdomain.ToolRef{Name: "ReadSource", Version: 4}, readExecutor},
		{toolsdomain.ToolRef{Name: "ValidateCitation", Version: 4}, citationExecutor},
		{toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 3}, gitExecutor},
	} {
		contract, err := contracts.ResolveContract(entry.ref)
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterContract(contract); err != nil {
			t.Fatal(err)
		}
		counter := &atomic.Int64{}
		counts[entry.ref.Name] = counter
		if err := registry.RegisterExecutor(entry.ref, dynamicToolsCountedExecutor{inner: entry.executor, count: counter}); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	service, err := toolsapplication.NewExecutionService(registry, repository, repository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	return &dynamicToolsIntegration{command: seed.command, model: model, tools: analysisTools, service: service, counts: counts}
}

func (h *dynamicToolsIntegration) decide(t *testing.T, ctx context.Context, ordinal int, decision domain.WorkspaceAnalysisDecision) {
	t.Helper()
	command := h.command
	command.OperationKey.Ordinal = ordinal
	command.OperationID = workspaceAnalysisModelIntegrationID(500 + ordinal*10)
	command.ReservationID = workspaceAnalysisModelIntegrationID(501 + ordinal*10)
	command.Call.ID = workspaceAnalysisModelIntegrationID(502 + ordinal*10)
	command.Call.CallNo = ordinal
	created, err := h.model.AuthorizeWorkspaceAnalysisModelCall(ctx, command)
	if err != nil || created.Disposition != application.WorkspaceAnalysisModelAuthorizationCreated {
		t.Fatalf("decision %d authorization: %v", ordinal, err)
	}
	completed, err := h.model.FinalizeWorkspaceAnalysisDecision(ctx, application.FinalizeWorkspaceAnalysisDecisionCommand{
		Identity: command.Identity, OperationKey: command.OperationKey, OperationID: created.OperationID, ExpectedCallVersion: created.Call.Version,
		ReceiptID: workspaceAnalysisModelIntegrationID(503 + ordinal*10), Status: domain.ModelCallSucceeded, Decision: &decision,
		Usage: domain.TokenUsage{InputTokens: 16, OutputTokens: 16, TotalTokens: 32}, LatencyMillis: 1,
	})
	if err != nil {
		t.Fatalf("decision %d completion: %v", ordinal, err)
	}
	h.command.Run = completed.Run
}

func (h *dynamicToolsIntegration) execute(t *testing.T, ctx context.Context, ordinal int, decision domain.WorkspaceAnalysisDecision, name, arguments string) toolsapplication.ToolExecutionResult {
	t.Helper()
	h.decide(t, ctx, ordinal, decision)
	var kind domain.WorkspaceAnalysisOperationKind
	switch name {
	case "SearchKnowledge":
		kind = domain.WorkspaceAnalysisOperationKnowledgeSearch
	case "ReadSource":
		kind = domain.WorkspaceAnalysisOperationSourceRead
	case "ValidateCitation":
		kind = domain.WorkspaceAnalysisOperationCitationValidation
	default:
		kind = domain.WorkspaceAnalysisOperationGitStatus
	}
	result, err := h.service.ExecuteWorkspaceAnalysisTool(ctx, h.toolCommand(ordinal, kind, name, arguments))
	if err != nil {
		t.Fatalf("%s ordinal %d: %v", name, ordinal, err)
	}
	return result
}

func (h *dynamicToolsIntegration) toolCommand(ordinal int, kind domain.WorkspaceAnalysisOperationKind, name, arguments string) toolsapplication.ExecuteWorkspaceAnalysisToolCommand {
	return toolsapplication.ExecuteWorkspaceAnalysisToolCommand{
		OperationKey: h.outputQuery(ordinal, kind).OperationKey,
		Tool: toolsapplication.ExecuteToolCommand{Identity: dynamicToolsIdentity(h.command.Identity), Invocation: toolsdomain.InvocationSourceTrustedWorkflow, CallNo: ordinal,
			Request: toolsdomain.ToolRequestV1{SchemaVersion: 1, ToolName: name, Arguments: json.RawMessage(arguments), Reason: "dynamic decision"}},
	}
}

func (h *dynamicToolsIntegration) outputQuery(ordinal int, kind domain.WorkspaceAnalysisOperationKind) toolsapplication.WorkspaceAnalysisDynamicToolQuery {
	return toolsapplication.WorkspaceAnalysisDynamicToolQuery{WorkspaceID: h.command.Identity.WorkspaceID, WorkflowRunID: h.command.Identity.WorkflowRunID, AnalysisRunID: h.command.OperationKey.AnalysisRunID,
		OperationKey: domain.WorkspaceAnalysisOperationKey{AnalysisRunID: h.command.OperationKey.AnalysisRunID, NodeKey: domain.WorkspaceAnalysisOperationNodeDecideNext, Kind: kind, Ordinal: ordinal}}
}

func (h *dynamicToolsIntegration) assertCounts(t *testing.T, ctx context.Context, platform *platformpostgres.Pool, models, tools, reads, refs, pending int) {
	t.Helper()
	var gotModels, gotTools, gotReads, gotRefs, gotPending, reserved int
	err := platform.DB().QueryRow(ctx, `SELECT settled_model_calls,settled_tool_calls,settled_source_reads,reserved_model_calls+reserved_tool_calls+reserved_source_reads,
	(SELECT count(*) FROM agent.workspace_analysis_evidence WHERE analysis_run_id=analysis.id),
	(SELECT count(*) FROM agent.workspace_analysis_operation WHERE analysis_run_id=analysis.id AND status='PENDING')
	FROM agent.workspace_analysis_run analysis WHERE id=$1`, string(h.command.OperationKey.AnalysisRunID)).Scan(&gotModels, &gotTools, &gotReads, &reserved, &gotRefs, &gotPending)
	if err != nil || gotModels != models || gotTools != tools || gotReads != reads || gotRefs != refs || gotPending != pending || reserved != 0 {
		t.Fatalf("durable counts models=%d tools=%d reads=%d refs=%d pending=%d reserved=%d: %v", gotModels, gotTools, gotReads, gotRefs, gotPending, reserved, err)
	}
}

func dynamicToolsIdentity(identity application.WorkspaceAnalysisModelExecutionIdentity) toolsdomain.TrustedExecutionIdentity {
	return toolsdomain.TrustedExecutionIdentity{WorkspaceID: identity.WorkspaceID, DefinitionID: identity.DefinitionID, DefinitionVersion: identity.DefinitionVersion, DefinitionHash: identity.DefinitionHash, WorkflowRunID: identity.WorkflowRunID, NodeKey: string(identity.NodeKey), NodeRunID: identity.NodeRunID, NodeAttemptID: identity.NodeAttemptID, LeaseOwner: identity.LeaseOwner, LeaseFence: identity.LeaseFence}
}

func ptrDynamicTool(value string) *string { return &value }

type dynamicToolsCountedExecutor struct {
	inner toolsapplication.Executor
	count *atomic.Int64
}

func (e dynamicToolsCountedExecutor) Execute(ctx context.Context, request toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	e.count.Add(1)
	return e.inner.Execute(ctx, request)
}

type dynamicToolsGitInspector struct{}

func (dynamicToolsGitInspector) Inspect(_ context.Context, workspace foundation.ID) (gitcli.StatusAggregate, error) {
	return gitcli.StatusAggregate{WorkspaceID: workspace, Branch: "main", Head: strings.Repeat("a", 40), ObjectFormat: gitcli.StatusObjectFormatSHA1, Clean: true}, nil
}

type dynamicToolsSearch struct{ result retrievaldomain.SearchResult }

func (s *dynamicToolsSearch) Search(_ context.Context, request retrievaldomain.SearchRequest) (retrievaldomain.SearchResult, error) {
	if request.WorkspaceID != s.result.WorkspaceID {
		return retrievaldomain.SearchResult{}, errors.New("search workspace mismatch")
	}
	result := s.result
	result.Items = append([]retrievaldomain.EvidenceV1(nil), result.Items[:min(int(request.Limit), len(result.Items))]...)
	return result, nil
}

type dynamicToolsArtifactReader map[foundation.ID]retrievalapplication.EvidenceArtifact

func (r dynamicToolsArtifactReader) ReadEvidenceArtifact(_ context.Context, workspace, source foundation.ID) (retrievalapplication.EvidenceArtifact, error) {
	value, ok := r[source]
	if !ok || value.WorkspaceID != workspace {
		return retrievalapplication.EvidenceArtifact{}, errors.New("artifact workspace mismatch")
	}
	value.Bytes = append([]byte(nil), value.Bytes...)
	return value, nil
}

func seedDynamicToolsEvidence(t *testing.T, ctx context.Context, platform *platformpostgres.Pool, workspace foundation.ID) (*dynamicToolsSearch, dynamicToolsArtifactReader) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	index := workspaceAnalysisModelIntegrationID(400)
	tx, err := platform.DB().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO retrieval.index_version(id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version)
	VALUES($1,$2,'default','v1',repeat('a',64),'{}','dynamic-tools',repeat('b',64),5,'dynamic-tools','building','["vector"]',1,$3,$3,repeat('c',64),5,'goldmark','goldmark-v1',repeat('a',64),'chunk-v1','schema-v1')`, string(index), string(workspace), now)
	if err != nil {
		t.Fatal(err)
	}
	result := retrievaldomain.SearchResult{WorkspaceID: workspace, IndexVersionID: index, RequestedMode: retrievaldomain.SearchModeKeyword, EffectiveMode: retrievaldomain.SearchModeKeyword}
	artifacts := dynamicToolsArtifactReader{}
	for i := 0; i < 5; i++ {
		id := func(offset int) foundation.ID { return workspaceAnalysisModelIntegrationID(410 + i*10 + offset) }
		content := []byte(fmt.Sprintf("dynamic source evidence %d\n", i))
		hash := workspaceAnalysisModelIntegrationHash(content)
		size := int64(len(content))
		relativePath := fmt.Sprintf("docs/evidence%d.md", i)
		statements := []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'file',$3,$4,$5)`, []any{string(id(0)), string(workspace), fmt.Sprintf("Evidence%d", i), relativePath, now}},
			{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,$4,'.knowledge/sources/' || $3,$5)`, []any{string(id(1)), string(workspace), hash, size, now}},
			{`INSERT INTO core.source_version(id,source_id,workspace_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at,content_artifact_id) VALUES($1,$2,$3,$4,$5,'text/markdown',$6,'passed',$7,$8)`, []any{string(id(2)), string(id(0)), string(workspace), hash, size, relativePath, now, string(id(1))}},
			{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at) VALUES($1,$2,$3,'goldmark','goldmark-v1',repeat('a',64),'schema-v1',$4,$5)`, []any{string(id(3)), string(workspace), string(id(1)), hash, now}},
			{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES($1,$2,$3,$4)`, []any{string(id(2)), string(id(3)), string(workspace), now}},
			{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,'{}',$6,'goldmark-v1','schema-v1',$7)`, []any{string(id(4)), string(workspace), string(id(1)), string(id(3)), size, hash, now}},
			{`INSERT INTO ingestion.canonical_chunk(id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at) VALUES($1,$2,$3,0,'[]',$4,$5,$6,$7,$7,'goldmark-v1','chunk-v1','schema-v1',false,'active',$8)`, []any{string(id(5)), string(workspace), string(id(3)), string(content), hash, string(id(4)), size, now}},
			{`INSERT INTO retrieval.index_manifest_chunk(index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at) VALUES($1,$2,$3,$4,0,'goldmark-v1','chunk-v1','schema-v1',$5)`, []any{string(index), string(id(5)), string(workspace), hash, now}},
			{`INSERT INTO retrieval.index_manifest_source(index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at) VALUES($1,$2,$3,$4,$5,'included',$6)`, []any{string(index), string(workspace), string(id(0)), string(id(2)), string(id(3)), now}},
		}
		for _, statement := range statements {
			if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			applicability, err := knowledgedomain.ParseApplicability(json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			statement, normalized, err := knowledgedomain.NormalizeStatement(string(content))
			if err != nil {
				t.Fatal(err)
			}
			fingerprint := knowledgedomain.ComputeClaimFingerprint(workspace, normalized, applicability)
			source := knowledgedomain.ClaimSource{ID: id(7), WorkspaceID: workspace, ClaimID: id(6),
				Provenance:  knowledgedomain.ProvenanceRef{WorkspaceID: workspace, SourceVersionID: id(2), SourceSpanID: id(4)},
				SupportType: knowledgedomain.ClaimSupportSupports, Reason: "source directly supports the claim", CreatedAt: now}
			source.EvidenceHash = knowledgedomain.ComputeClaimSourceEvidenceHash(source, applicability)
			if err := knowledgedomain.ValidateClaimSource(source, applicability); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO core.claim(id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_factors,fingerprint,version,created_at,updated_at)
				VALUES($1,$2,$3,$4,$5::jsonb,$6,$7,'SUGGESTED','{}',$8,1,$9,$9)`,
				string(source.ClaimID), string(workspace), statement, normalized, string(applicability.CanonicalJSON), applicability.SchemaVersion, applicability.Hash, fingerprint, now); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at)
				VALUES($1,$2,$3,$4,$5,'SUPPORTS',$6,$7,$8)`, string(source.ID), string(workspace), string(source.ClaimID), string(id(2)), string(id(4)), source.Reason, source.EvidenceHash, now); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `UPDATE core.claim SET status='CONFIRMED',version=version+1,updated_at=$1 WHERE id=$2`, now, string(source.ClaimID)); err != nil {
				t.Fatal(err)
			}
		}
		fts, trigram := 0.9-float64(i)/10, 0.1
		result.Items = append(result.Items, retrievaldomain.EvidenceV1{WorkspaceID: workspace, IndexVersionID: index, ChunkID: id(5), ParseProjectionID: id(3), ContentHash: hash, HeadingPath: []string{},
			Span: retrievaldomain.EvidenceSpan{ID: id(4), StartLine: 1, EndLine: 1, StartByte: 0, EndByte: size}, Snippet: string(content),
			Provenances:     []retrievaldomain.EvidenceProvenance{{SourceID: id(0), SourceVersionID: id(2), RelativePath: relativePath, CapturedAt: now}},
			LexicalFTSScore: &fts, LexicalTrigramScore: &trigram,
			Lexical: &retrievaldomain.CandidateStageScore{Rank: int32(i + 1), Score: fts}, Fusion: retrievaldomain.CandidateStageScore{Rank: int32(i + 1), Score: fts}})
		artifacts[id(2)] = retrievalapplication.EvidenceArtifact{WorkspaceID: workspace, SourceVersionID: id(2), ContentArtifactID: id(1), ContentHash: hash, ByteSize: size, Bytes: content}
	}
	if err := retrievaldomain.ValidateSearchResult(retrievaldomain.SearchRequest{WorkspaceID: workspace, Query: "approved evidence", Mode: retrievaldomain.SearchModeKeyword, Limit: 5}, result); err != nil {
		t.Fatalf("invalid Search fixture: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return &dynamicToolsSearch{result: result}, artifacts
}
