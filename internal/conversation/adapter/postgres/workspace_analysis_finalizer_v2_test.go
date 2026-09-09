package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestWorkspaceAnalysisV2PublicationPreservesAliasesAndOptionalGit(t *testing.T) {
	workspaceID, fence, facts := workspaceAnalysisFinalizerV2Fixture(t)
	publication, err := buildWorkspaceAnalysisSuccessPublication(workspaceID, fence, facts)
	if err != nil {
		t.Fatalf("publication: %#v", err)
	}
	var result conversationdomain.WorkspaceAnalysisAnswerResultV2
	if json.Unmarshal(publication.Document, &result) != nil || result.Validate() != nil {
		t.Fatalf("invalid v2 document: %s", publication.Document)
	}
	if result.Payload.GitStatus != nil || len(result.Payload.Citations) != 1 || publication.CitationCount != 1 ||
		result.Payload.AnswerMarkdown != "Grounded answer [E1] and [E6]." ||
		result.Payload.ProposalSuggestion == nil || len(result.Payload.ProposalSuggestion.CitationIDs) != 1 {
		t.Fatalf("alias/null-Git projection: %#v", result.Payload)
	}
	lookup := workspaceAnalysisFinalizerAdapterTestLookup()
	lookup.DefinitionVersion = 2
	answer := workspaceAnalysisFinalizerAdapterPendingAnswer(lookup)
	now := facts.Candidate.CreatedAt
	answer.Version, answer.UpdatedAt, answer.PublishedAt = 2, now, &now
	answer.PublicationStatus, answer.ResultType, answer.Result, answer.ResultHash = publication.Status, publication.ResultType, publication.Document, publication.ResultHash
	answer.ModelRunID = publication.ModelRunID
	count, err := workspaceAnalysisTerminalCitationCount(answer)
	if err != nil || count != 1 {
		t.Fatalf("replay citations=%d err=%v", count, err)
	}
	reason := agentdomain.WorkspaceAnalysisRunCompleted
	state := workspaceAnalysisProofState{Answer: answer, RunStatus: agentdomain.WorkspaceAnalysisRunSucceeded, RunReason: &reason,
		Success: &workspaceAnalysisSuccessProofRecord{ID: workspaceAnalysisFinalizerAdapterTestID(40), NodeRunID: lookup.NodeRunID,
			PublishedDocument: publication.Document, PublishedResultHash: publication.ResultHash}}
	output, found, err := workspaceAnalysisOutputFromState(lookup, state)
	if err != nil || !found || output.SchemaVersion != 2 {
		t.Fatalf("v2 replay=%#v found=%t err=%v", output, found, err)
	}
	lookup.DefinitionVersion = 1
	if _, _, err := workspaceAnalysisOutputFromState(lookup, state); err == nil {
		t.Fatal("v1 replay accepted v2 success")
	}
	fence.DefinitionVersion = 1
	if _, err := buildWorkspaceAnalysisSuccessPublication(workspaceID, fence, facts); err == nil {
		t.Fatal("v1 builder accepted v2 candidate")
	}
}

func TestWorkspaceAnalysisV2PublicationRejectsLoopAndHashDrift(t *testing.T) {
	for _, test := range []string{"loop", "candidate hash", "invalid citation", "candidate version", "alias identity"} {
		t.Run(test, func(t *testing.T) {
			workspaceID, fence, facts := workspaceAnalysisFinalizerV2Fixture(t)
			var binding map[string]any
			if err := json.Unmarshal(facts.ValidationReceipt.PrivateBinding.Document, &binding); err != nil {
				t.Fatal(err)
			}
			switch test {
			case "loop":
				binding["candidate_id"], binding["candidate_hash"] = nil, nil
			case "candidate hash":
				facts.Candidate.DocumentHash = strings.Repeat("e", 64)
			case "invalid citation":
				facts.ValidationReceipt.Output = workspaceAnalysisFinalizerAdapterTestJSON(t, map[string]any{"results": []any{
					map[string]any{"evidence_ref": "E1", "valid": false, "reason_code": "CITATION_UNRESOLVABLE"},
					map[string]any{"evidence_ref": "E6", "valid": true, "reason_code": "OK"},
				}})
				facts.ValidationReceipt.OutputHash = workspaceAnalysisFinalizerAdapterTestHash(facts.ValidationReceipt.Output)
				facts.ValidationReceipt.OutputBytes = int64(len(facts.ValidationReceipt.Output))
			case "candidate version":
				facts.Candidate.SchemaVersion = 1
			case "alias identity":
				binding["results"].([]any)[1].(map[string]any)["chunk_id"] = string(workspaceAnalysisFinalizerAdapterTestID(45))
			}
			document := workspaceAnalysisFinalizerAdapterTestJSON(t, binding)
			facts.ValidationReceipt.PrivateBinding.Document = document
			facts.ValidationReceipt.PrivateBinding.Hash = workspaceAnalysisFinalizerAdapterTestHash(document)
			facts.ValidationReceipt.PrivateBinding.Bytes = int64(len(document))
			if test == "invalid citation" {
				if _, err := toolsdomain.ValidateCitationV4ReceiptResults(facts.ValidationReceipt, facts.Candidate.ID, facts.Candidate.DocumentHash, []string{"E1", "E6"}); err != nil {
					t.Fatalf("invalid citation must remain a well-formed receipt: %v", err)
				}
			}
			if _, err := buildWorkspaceAnalysisSuccessPublication(workspaceID, fence, facts); err == nil {
				t.Fatalf("%s accepted", test)
			}
		})
	}
}

func TestWorkspaceAnalysisV2BudgetProofMatchesAdmissionPolicy(t *testing.T) {
	_, base, _ := workspaceAnalysisFinalizerV2Fixture(t)
	base.MaxModelCalls, base.MaxToolCalls, base.MaxSourceReads = 14, 13, 8
	base.MaxInputTokens, base.MaxOutputTokens = 917504, 11264
	base.SettledModelCalls, base.SettledToolCalls, base.SettledSourceReads = 0, 0, 0
	base.SettledInputTokens, base.SettledOutputTokens = 0, 0
	model := conversationapplication.WorkspaceAnalysisBudgetRequest{ModelCalls: 1, InputTokens: 65536, OutputTokens: 512}
	cases := []struct {
		name    string
		kind    agentdomain.WorkspaceAnalysisOperationKind
		ordinal int
		request conversationapplication.WorkspaceAnalysisBudgetRequest
		change  func(*workspaceAnalysisFinalizationFence)
		want    bool
	}{
		{"thirteenth decision", agentdomain.WorkspaceAnalysisOperationDecision, 13, model, func(*workspaceAnalysisFinalizationFence) {}, true},
		{"fitting decision", agentdomain.WorkspaceAnalysisOperationDecision, 1, model, func(*workspaceAnalysisFinalizationFence) {}, false},
		{"reserved publication models", agentdomain.WorkspaceAnalysisOperationDecision, 12, model, func(f *workspaceAnalysisFinalizationFence) { f.SettledModelCalls = 12 }, true},
		{"evidence namespace", agentdomain.WorkspaceAnalysisOperationKnowledgeSearch, 11, conversationapplication.WorkspaceAnalysisBudgetRequest{ToolCalls: 1}, func(f *workspaceAnalysisFinalizationFence) { f.EvidenceCount = 32 }, true},
		{"final citation allowance", agentdomain.WorkspaceAnalysisOperationKnowledgeSearch, 12, conversationapplication.WorkspaceAnalysisBudgetRequest{ToolCalls: 1}, func(f *workspaceAnalysisFinalizationFence) { f.SettledToolCalls = 12 }, true},
		{"source read allowance", agentdomain.WorkspaceAnalysisOperationSourceRead, 10, conversationapplication.WorkspaceAnalysisBudgetRequest{ToolCalls: 1, SourceReads: 1}, func(f *workspaceAnalysisFinalizationFence) { f.SettledSourceReads = 8 }, true},
		{"fitting search", agentdomain.WorkspaceAnalysisOperationKnowledgeSearch, 2, conversationapplication.WorkspaceAnalysisBudgetRequest{ToolCalls: 1}, func(f *workspaceAnalysisFinalizationFence) { f.EvidenceCount = 31 }, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fence := base
			test.change(&fence)
			kind := agentdomain.WorkspaceAnalysisOperationCallTool
			if test.kind == agentdomain.WorkspaceAnalysisOperationDecision {
				kind = agentdomain.WorkspaceAnalysisOperationCallModel
			}
			operation := workspaceAnalysisOperationRecord{Kind: test.kind, Ordinal: test.ordinal, NodeKey: "decide_next", CallKind: kind, Status: agentdomain.WorkspaceAnalysisOperationPending}
			if !workspaceAnalysisV2BudgetRequestMatchesOperation(test.request, operation.Kind, fence) {
				t.Fatal("exact request rejected")
			}
			if got := workspaceAnalysisV2BudgetExhausted(test.request, operation, fence); got != test.want {
				t.Fatalf("exhausted=%t want=%t", got, test.want)
			}
		})
	}
	forged := model
	forged.OutputTokens = 256
	if workspaceAnalysisV2BudgetRequestMatchesOperation(forged, agentdomain.WorkspaceAnalysisOperationDecision, base) {
		t.Fatal("legacy planner request accepted for decision")
	}
}

func TestWorkspaceAnalysisV2TerminalNodeVersionAndNoAttempt(t *testing.T) {
	for _, definition := range []workflowdomain.RegisteredDefinition{conversationworkflow.RegisteredWorkspaceAnalysisDefinition(), conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2()} {
		for _, node := range definition.Graph.Nodes {
			key, owned := workspaceAnalysisCancellationNodeKey(node.Kind)
			if !owned || key != node.Key || workspaceAnalysisCancellationNodeVersion(node.Kind) != definition.Version {
				t.Fatalf("node not routed exactly: %s", node.Kind)
			}
		}
	}
	event := workspaceAnalysisCancellationEventFixture()
	event.NodeKind = conversationworkflow.WorkspaceAnalysisNodeKindV2("decide_next")
	event.Outcome, event.FailureClass = workflowapplication.WorkflowTerminalOutcomeFailed, workflowdomain.FailureClassNonRetryable
	event.FailureCode, event.FailureSummary = "RUNTIME_INPUT_INVALID", "Runtime input is invalid"
	if err := validateWorkspaceAnalysisRuntimeFailureEvent(event); err != nil {
		t.Fatalf("v2 no-attempt failure shape: %v", err)
	}
	lookup := workspaceAnalysisFinalizerAdapterTestLookup()
	lookup.DefinitionVersion = 2
	command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{WorkspaceAnalysisPublicationLookup: lookup, Reason: agentdomain.WorkspaceAnalysisRunCitationInvalid}
	op := workspaceAnalysisOperationRecord{NodeRunID: lookup.NodeRunID, NodeKey: "decide_next", Kind: agentdomain.WorkspaceAnalysisOperationCitationValidation, Ordinal: 1}
	if workspaceAnalysisTerminationOperationBinding(command, workspaceAnalysisFinalizationFence{DefinitionVersion: 2, NodeKey: "review_publish"}, op) {
		t.Fatal("loop citation accepted as final rejection authority")
	}
}

func TestWorkspaceAnalysisV2CancellationAuditUsesPersistedVersion(t *testing.T) {
	analysis := workspaceAnalysisAuditTestRun()
	run := workspaceAnalysisCancellationAuditRun{ID: analysis.ID, WorkspaceID: analysis.WorkspaceID, ConversationID: analysis.ConversationID,
		QuestionID: analysis.QuestionID, AnswerID: analysis.AnswerID, WorkflowRunID: analysis.WorkflowRunID, DefinitionVersion: 2}
	control := workflowapplication.WorkflowControlEvent{WorkspaceID: analysis.WorkspaceID, WorkflowRunID: analysis.WorkflowRunID,
		Action: workflowapplication.ControlActionCancel, IdempotencyKey: "cancel-v2", ExpectedVersion: 1,
		PersistedControl: workflowapplication.ControlPersistenceResult{WorkflowRunID: analysis.WorkflowRunID, Version: 2, CancelRequested: true},
		OccurredAt:       time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
	capture := &workspaceAnalysisAuditCapture{}
	if err := appendScopedWorkspaceAnalysisCancelRequestedAudit(context.Background(), nil, capture, run, control); err != nil {
		t.Fatal(err)
	}
	if len(capture.events) != 1 || !strings.Contains(string(capture.events[0].Metadata), "\"definition_version\":2") {
		t.Fatal("v2 cancellation audit version was not preserved")
	}
}

func workspaceAnalysisFinalizerV2Fixture(t *testing.T) (foundation.ID, workspaceAnalysisFinalizationFence, workspaceAnalysisSuccessFacts) {
	t.Helper()
	workspaceID := workspaceAnalysisFinalizerAdapterTestLookup().WorkspaceID
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	candidate := agentdomain.WorkspaceAnalysisCandidate{ID: workspaceAnalysisFinalizerAdapterTestID(4), WorkspaceID: workspaceID, SchemaVersion: 2, SynthesisModelRunID: workspaceAnalysisFinalizerAdapterTestID(2), CreatedAt: now}
	candidate.Document = workspaceAnalysisFinalizerAdapterTestJSON(t, agentdomain.WorkspaceAnalysisCandidateResult{
		ResultType: agentdomain.ResultTypeWorkspaceAnalysisCandidate, SchemaID: agentdomain.WorkspaceAnalysisCandidateSchemaID, SchemaVersion: "2", ModelRunRef: candidate.SynthesisModelRunID,
		Payload: agentdomain.WorkspaceAnalysisCandidatePayload{AnswerMarkdown: "Grounded answer [E1] and [E6].", CitationRefs: []string{"E1", "E6"},
			ProposalSuggestion: &agentdomain.WorkspaceAnalysisCandidateProposal{Summary: "Review the cited implementation.", CitationRefs: []string{"E1", "E6"}}},
	})
	candidate.DocumentHash = workspaceAnalysisFinalizerAdapterTestHash(candidate.Document)
	candidate.DocumentBytes = int64(len(candidate.Document))
	receipt := workspaceAnalysisFinalizerAdapterValidationReceipt(t, workspaceID, candidate.ID, candidate.DocumentHash, now)
	contract, _ := toolsdomain.WorkspaceAnalysisResultReceiptContract(toolsdomain.ToolRef{Name: "ValidateCitation", Version: 4})
	receipt.Tool, receipt.OutputSchema = contract.Tool, contract.OutputSchema
	receipt.MaxOutputBytes, receipt.MaxPrivateBindingBytes = contract.MaxOutputBytes, contract.MaxPrivateBindingBytes
	outputs := []any{}
	bindings := []any{}
	for index, ref := range []string{"E1", "E6"} {
		outputs = append(outputs, map[string]any{"evidence_ref": ref, "valid": true, "reason_code": "OK"})
		bindings = append(bindings, map[string]any{
			"evidence_ref": ref, "citation_id": workspaceAnalysisFinalizerAdapterCitationID(),
			"index_version_id": workspaceAnalysisFinalizerAdapterTestID(20), "chunk_id": workspaceAnalysisFinalizerAdapterTestID(21),
			"source_version_id": workspaceAnalysisFinalizerAdapterTestID(22), "source_span_id": workspaceAnalysisFinalizerAdapterTestID(23), "content_hash": strings.Repeat("b", 64),
			"search_receipt_id": workspaceAnalysisFinalizerAdapterTestID(30 + index), "search_receipt_hash": strings.Repeat("a", 64), "search_evidence_ref": "E1",
			"read_receipt_id": workspaceAnalysisFinalizerAdapterTestID(32 + index), "read_receipt_hash": strings.Repeat("c", 64),
		})
	}
	receipt.Output = workspaceAnalysisFinalizerAdapterTestJSON(t, map[string]any{"results": outputs})
	receipt.OutputHash = workspaceAnalysisFinalizerAdapterTestHash(receipt.Output)
	receipt.OutputBytes = int64(len(receipt.Output))
	private := workspaceAnalysisFinalizerAdapterTestJSON(t, map[string]any{"candidate_id": candidate.ID, "candidate_hash": candidate.DocumentHash, "results": bindings})
	receipt.PrivateBinding = &toolsdomain.ResultReceiptPrivateBinding{Schema: contract.PrivateBindingSchema, Document: private, Hash: workspaceAnalysisFinalizerAdapterTestHash(private), Bytes: int64(len(private))}
	if _, err := toolsdomain.ValidateCitationV4ReceiptResults(receipt, candidate.ID, candidate.DocumentHash, []string{"E1", "E6"}); err != nil {
		t.Fatalf("v4 fixture: %#v", err)
	}
	return workspaceID, workspaceAnalysisFinalizationFence{DefinitionVersion: 2, PolicyVersion: 2, SettledModelCalls: 7, SettledToolCalls: 5, SettledSourceReads: 2, SettledInputTokens: 1400, SettledOutputTokens: 700},
		workspaceAnalysisSuccessFacts{Candidate: candidate, ValidationReceipt: receipt}
}
