//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestWorkspaceAnalysisDynamicCandidateAndIndependentValidationIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	seedWorkspaceAnalysisDynamicPublicationFacts(t, platform, ctx)
}

func TestWorkspaceAnalysisDynamicPublicationWithoutGitIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	testWorkspaceAnalysisDynamicPublicationWithoutGit(t, platform, ctx)
}

func testWorkspaceAnalysisDynamicPublicationWithoutGit(t *testing.T, platform *platformpostgres.Pool, ctx context.Context) {
	t.Helper()
	h, command, loopValidation := seedWorkspaceAnalysisDynamicPublicationFacts(t, platform, ctx)
	finalizer, timelines := dynamicFinalizerDependencies(t, platform)
	wrong := command
	wrong.ValidationReceiptID, wrong.ValidationReceiptHash = loopValidation.ResultReceiptID, loopValidation.Call.ResponseHash
	if _, _, err := finalizer.FinalizeSuccess(ctx, wrong); err == nil {
		t.Fatal("loop citation receipt replaced the independent candidate-bound publication gate")
	}
	wrong = command
	wrong.CandidateHash = strings.Repeat("f", 64)
	if _, _, err := finalizer.FinalizeSuccess(ctx, wrong); err == nil {
		t.Fatal("publication accepted a different candidate hash")
	}
	output, replayed, err := finalizer.FinalizeSuccess(ctx, command)
	if err != nil || replayed || output.SchemaVersion != 2 || output.PublicationStatus != conversationdomain.AnswerPublicationCompleted ||
		output.ResultType != conversationdomain.AnswerResultWorkspaceAnalysis {
		t.Fatalf("dynamic success publication: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	again, replayed, err := finalizer.FinalizeSuccess(ctx, command)
	if err != nil || !replayed || again.ProofID != output.ProofID || again.ResultHash != output.ResultHash || again.SchemaVersion != 2 {
		t.Fatalf("dynamic success did not replay its exact proof: %v", err)
	}
	var resultDocument []byte
	var proofs, running int
	var gitID, gitHash *string
	if err := platform.DB().QueryRow(ctx, `SELECT answer.result,
		(SELECT count(*) FROM agent.workspace_analysis_publication_proof WHERE analysis_run_id=analysis.id),
		(SELECT count(*) FROM agent.model_run WHERE workflow_run_id=analysis.workflow_run_id AND status='RUNNING'),
		proof.git_receipt_id::text,proof.git_receipt_hash
		FROM agent.workspace_analysis_run analysis JOIN agent.answer answer ON answer.id=analysis.answer_id
		JOIN agent.workspace_analysis_publication_proof proof ON proof.analysis_run_id=analysis.id WHERE analysis.id=$1`,
		string(command.AnalysisRunID)).Scan(&resultDocument, &proofs, &running, &gitID, &gitHash); err != nil {
		t.Fatal(err)
	}
	var result conversationdomain.WorkspaceAnalysisAnswerResultV2
	if err := json.Unmarshal(resultDocument, &result); err != nil || result.Validate() != nil || result.SchemaVersion != "v2" ||
		result.Payload.GitStatus != nil || len(result.Payload.Citations) != 1 || result.Payload.Budget.ModelCalls != 6 || result.Payload.Budget.ToolCalls != 4 ||
		proofs != 1 || running != 0 || gitID != nil || gitHash != nil {
		t.Fatalf("published v2 result/proof differs from exact source and budget facts: %v", err)
	}
	assertDynamicTerminalTimeline(t, ctx, timelines, command.WorkspaceAnalysisPublicationLookup,
		conversationdomain.WorkspaceAnalysisTimelineRunSucceeded, domain.WorkspaceAnalysisRunCompleted, 10, 6, 4)
	h.assertCounts(t, ctx, platform, 6, 4, 1, 5, 0)
	if h.counts["ReadGitStatus"].Load() != 0 || h.counts["SearchKnowledge"].Load() != 1 || h.counts["ReadSource"].Load() != 1 || h.counts["ValidateCitation"].Load() != 2 {
		t.Fatal("publication replay executed tools or invented a Git observation")
	}
}

// This fixture exercises the production persistence ports and real evidence
// validators. Model output is deterministic; it is not a Provider-quality or
// River delivery test.
func seedWorkspaceAnalysisDynamicPublicationFacts(t *testing.T, platform *platformpostgres.Pool, ctx context.Context) (*dynamicToolsIntegration, conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand, toolsapplication.ToolExecutionResult) {
	t.Helper()
	gates := seedWorkspaceAnalysisDynamicCandidateGates(t, platform, ctx, []string{"E1"}, true)
	return gates.tools, finalizeWorkspaceAnalysisDynamicReview(t, platform, ctx, gates, true), gates.loopValidation
}

type workspaceAnalysisDynamicCandidateGates struct {
	tools          *dynamicToolsIntegration
	fixture        workspaceAnalysisModelIntegrationFixture
	candidate      domain.WorkspaceAnalysisCandidate
	loopValidation toolsapplication.ToolExecutionResult
	validation     toolsapplication.ToolExecutionResult
	sourceReads    int
}

func seedWorkspaceAnalysisDynamicCandidateGates(t *testing.T, platform *platformpostgres.Pool, ctx context.Context, refs []string, wantValid bool) workspaceAnalysisDynamicCandidateGates {
	t.Helper()
	h := newDynamicToolsIntegration(t, ctx, platform)
	for index, ref := range refs {
		h.execute(t, ctx, 2*index+1, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionKnowledgeSearch, Query: ptrDynamicTool("approved evidence")}, "SearchKnowledge", `{"query":"approved evidence","mode":"keyword","limit":5}`)
		arguments, err := json.Marshal(map[string]string{"evidence_ref": ref})
		if err != nil {
			t.Fatal(err)
		}
		h.execute(t, ctx, 2*index+2, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionSourceRead, EvidenceRef: ptrDynamicTool(ref)}, "ReadSource", string(arguments))
	}
	loopArguments, err := json.Marshal(struct {
		CandidateID   *string  `json:"candidate_id"`
		CandidateHash *string  `json:"candidate_hash"`
		EvidenceRefs  []string `json:"evidence_refs"`
	}{EvidenceRefs: refs})
	if err != nil {
		t.Fatal(err)
	}
	loopValidation := h.execute(t, ctx, 2*len(refs)+1, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionCitationValidation, EvidenceRefs: refs}, "ValidateCitation", string(loopArguments))
	h.decide(t, ctx, 2*len(refs)+2, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionFinish})
	evidence, err := h.tools.LoadWorkspaceAnalysisSynthesisEvidence(ctx, toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityQuery{
		WorkspaceID: h.command.Identity.WorkspaceID, WorkflowRunID: h.command.Identity.WorkflowRunID, AnalysisRunID: h.command.OperationKey.AnalysisRunID,
	})
	if err != nil || len(evidence.ReadSourceReceipts) != len(refs) || len(evidence.Evidence) != len(refs) {
		t.Fatalf("dynamic synthesis evidence: %v", err)
	}
	binding, err := toolsdomain.ReadSourceV4ReceiptBinding(evidence.ReadSourceReceipts[0])
	if err != nil {
		t.Fatal(err)
	}
	seedWorkspaceAnalysisDynamicPublicationNodes(t, platform, ctx, h.command)
	fixture := workspaceAnalysisModelIntegrationFixture{command: h.command}
	fixture.command.Run.Retrieval = domain.RetrievalRef{IndexVersionID: binding.Identity.IndexVersionID}
	synthesis := synthesisWorkspaceAnalysisModelAuthorizationCommand(fixture)
	synthesis.Run.Schema.Version, synthesis.Run.ReducedSchema.Version, synthesis.Call.Schema.Version = "2", "2", "2"
	synthesis.Call.MaxOutputTokens = int(domain.WorkspaceAnalysisV2SynthesisMaxOutputTokens)
	authorized, err := h.model.AuthorizeWorkspaceAnalysisModelCall(ctx, synthesis)
	if err != nil || authorized.Disposition != application.WorkspaceAnalysisModelAuthorizationCreated {
		t.Fatalf("v2 synthesis authorization: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	candidateCommand := successfulWorkspaceAnalysisModelCandidateCommand(t, fixture, authorized, synthesis)
	candidateResult, err := domain.DecodeWorkspaceAnalysisCandidate(candidateCommand.Candidate.Document, domain.DefaultDecodeLimits())
	if err != nil {
		t.Fatal(err)
	}
	candidateResult.SchemaVersion = "2"
	candidateResult.Payload.CitationRefs = append([]string(nil), refs...)
	candidateResult.Payload.AnswerMarkdown = strings.TrimSpace(evidence.Evidence[0].Excerpt) + " [" + strings.Join(refs, "] [") + "]."
	document, err := json.Marshal(candidateResult)
	if err != nil {
		t.Fatal(err)
	}
	candidateCommand.Candidate.SchemaVersion = 2
	candidateCommand.Candidate.Document, candidateCommand.Candidate.DocumentHash, candidateCommand.Candidate.DocumentBytes = document, workspaceAnalysisModelIntegrationHash(document), int64(len(document))
	candidateCommand.Call.ResponseHash, candidateCommand.Call.ResponseBytes = candidateCommand.Candidate.DocumentHash, candidateCommand.Candidate.DocumentBytes
	mutation, err := h.model.FinalizeWorkspaceAnalysisModelCandidate(ctx, candidateCommand)
	if err != nil || mutation.Candidate == nil {
		t.Fatalf("v2 candidate persistence: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	candidate := *mutation.Candidate
	var valid bool
	if err := platform.DB().QueryRow(ctx, `SELECT agent.workspace_analysis_validation_all_valid($1)`, string(candidate.AnalysisRunID)).Scan(&valid); err != nil || valid {
		t.Fatalf("loop citation validation became the final candidate gate: %v", err)
	}
	validationIdentity := h.command.Identity
	validationIdentity.NodeKey, validationIdentity.NodeRunID, validationIdentity.NodeAttemptID = domain.WorkspaceAnalysisOperationNodeValidateCitations,
		workspaceAnalysisModelIntegrationID(170), workspaceAnalysisModelIntegrationID(171)
	arguments, err := json.Marshal(struct {
		CandidateID   string   `json:"candidate_id"`
		CandidateHash string   `json:"candidate_hash"`
		EvidenceRefs  []string `json:"evidence_refs"`
	}{string(candidate.ID), candidate.DocumentHash, refs})
	if err != nil {
		t.Fatal(err)
	}
	validation, err := h.service.ExecuteWorkspaceAnalysisTool(ctx, toolsapplication.ExecuteWorkspaceAnalysisToolCommand{
		OperationKey: domain.WorkspaceAnalysisOperationKey{AnalysisRunID: candidate.AnalysisRunID, NodeKey: validationIdentity.NodeKey, Kind: domain.WorkspaceAnalysisOperationCitationValidation, Ordinal: 1},
		Tool: toolsapplication.ExecuteToolCommand{Identity: dynamicToolsIdentity(validationIdentity), Invocation: toolsdomain.InvocationSourceTrustedWorkflow, CallNo: 1,
			Request: toolsdomain.ToolRequestV1{SchemaVersion: 1, ToolName: "ValidateCitation", Arguments: arguments, Reason: "validate final candidate"}},
	})
	if err != nil || validation.Call.Status != toolsdomain.CallSucceeded || validation.ResultReceiptID == loopValidation.ResultReceiptID {
		t.Fatalf("independent candidate citation execution: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	var bound bool
	if err := platform.DB().QueryRow(ctx, `SELECT agent.workspace_analysis_validation_all_valid($1),agent.workspace_analysis_v2_final_validation_bound($1)`,
		string(candidate.AnalysisRunID)).Scan(&valid, &bound); err != nil || valid != wantValid || !bound {
		t.Fatalf("exact final citation gate validity=%t bound=%t: %v", valid, bound, err)
	}
	h.assertCounts(t, ctx, platform, 2*len(refs)+3, 2*len(refs)+2, len(refs), 5*len(refs), 0)
	return workspaceAnalysisDynamicCandidateGates{tools: h, fixture: fixture, candidate: candidate,
		loopValidation: loopValidation, validation: validation, sourceReads: len(refs)}
}

func finalizeWorkspaceAnalysisDynamicReview(t *testing.T, platform *platformpostgres.Pool, ctx context.Context, gates workspaceAnalysisDynamicCandidateGates, passed bool) conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand {
	t.Helper()
	review := reviewWorkspaceAnalysisModelAuthorizationCommand(t, gates.fixture, gates.candidate)
	reviewAuthorization, err := gates.tools.model.AuthorizeWorkspaceAnalysisModelCall(ctx, review)
	if err != nil || reviewAuthorization.Disposition != application.WorkspaceAnalysisModelAuthorizationCreated {
		t.Fatalf("v2 independent review authorization: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	reviewCommand := successfulWorkspaceAnalysisModelReviewCommand(t, reviewAuthorization, review, gates.candidate, passed)
	if _, err := gates.tools.model.FinalizeWorkspaceAnalysisModelResult(ctx, reviewCommand); err != nil {
		t.Fatalf("v2 independent review persistence: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	gates.tools.assertCounts(t, ctx, platform, 2*gates.sourceReads+4, 2*gates.sourceReads+2, gates.sourceReads, 5*gates.sourceReads, 0)
	return conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand{
		WorkspaceAnalysisPublicationLookup: dynamicFinalizerLookup(review), ExpectedAnswerVersion: 1,
		CandidateID: gates.candidate.ID, CandidateHash: gates.candidate.DocumentHash,
		ValidationReceiptID: gates.validation.ResultReceiptID, ValidationReceiptHash: gates.validation.Call.ResponseHash,
		ReviewModelResultID: reviewCommand.Result.ID, ReviewModelResultHash: reviewCommand.Result.DocumentHash,
	}
}

func seedWorkspaceAnalysisDynamicPublicationNodes(t *testing.T, platform *platformpostgres.Pool, ctx context.Context, command application.AuthorizeWorkspaceAnalysisModelCallCommand, selectedKeys ...string) {
	t.Helper()
	tx, err := platform.DB().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, node := range []struct {
		base int
		key  string
	}{{160, "synthesize_answer"}, {170, "validate_citations"}, {180, "review_publish"}} {
		if len(selectedKeys) != 0 {
			selected := false
			for _, key := range selectedKeys {
				selected = selected || node.key == key
			}
			if !selected {
				continue
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO workflow.node_run(
			id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,idempotency_key,
			input_schema_version,output_schema_version,dispatch_no,version,created_at,updated_at)
			VALUES($1,$2,$3,$4,'running',1,'{}',$5,now()+interval '30 minutes',$6,1,2,1,1,now(),now())`,
			string(workspaceAnalysisModelIntegrationID(node.base)), string(command.Identity.WorkflowRunID), node.key,
			"agent.workspace-analysis.v2."+node.key, command.Identity.LeaseOwner, "dynamic-publication:"+node.key)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at,heartbeat_at)
			VALUES($1,$2,1,1,0,$3,$4,now()+interval '30 minutes','running',now(),now())`,
			string(workspaceAnalysisModelIntegrationID(node.base+1)), string(workspaceAnalysisModelIntegrationID(node.base)),
			"dynamic-publication:"+node.key, command.Identity.LeaseOwner)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
