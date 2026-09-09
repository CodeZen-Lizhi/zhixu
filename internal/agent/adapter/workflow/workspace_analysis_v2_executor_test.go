package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestWorkspaceAnalysisV2DecideReturnsOnlyDurableFinishAndReplaysPublication(t *testing.T) {
	f := newWorkspaceAnalysisV2ExecutorFixture(t, "E17")
	execution := f.execution(conversationworkflow.WorkspaceAnalysisNodeDecideNext)
	result, err := f.executor(t, execution.NodeKey).Execute(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	output, err := decodeWorkspaceAnalysisLoopOutputV2(result.Output)
	if err != nil || output.DecisionCount != 3 || output.ToolCount != 2 || f.base.finalizer.successCalls != 0 || f.base.synthesis.calls != 0 {
		t.Fatalf("finish was not isolated from publication: %v", err)
	}
	if len(f.loop.request.Tools) != 4 || len(f.loop.request.Messages) != 2 || strings.Contains(f.loop.request.Messages[1].Content, string(f.run.ID)) {
		t.Fatal("model loop received a drifted catalog or server identity")
	}
	f.base.finalizer.lookupFound = true
	f.base.finalizer.lookupOutput = f.publication(conversationdomain.AnswerPublicationRefused, nil)
	for _, key := range workspaceAnalysisV2TestNodeKeys() {
		before := f.journal.calls
		result, err := f.executor(t, key).Execute(context.Background(), f.execution(key))
		if err != nil || f.journal.calls != before || f.loop.calls != 1 || f.base.synthesis.calls != 0 || f.review.calls != 0 || len(result.Output) == 0 {
			t.Fatalf("publication replay entered runtime for %s: %v", key, err)
		}
	}
	foreign := execution
	foreign.DefinitionVersion = 1
	if _, err := f.executor(t, execution.NodeKey).Execute(context.Background(), foreign); err == nil {
		t.Fatal("v2 executor accepted a v1 lease")
	}
}

func TestWorkspaceAnalysisV2SynthesisPreservesNullableGitGlobalRefsAndSearchIndexes(t *testing.T) {
	f := newWorkspaceAnalysisV2ExecutorFixture(t, "E17", "E23")
	execution := f.execution(conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer)
	result, err := f.executor(t, execution.NodeKey).Execute(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	request := f.base.synthesis.request
	if !reflect.DeepEqual(request.AllowedEvidenceRefs, []string{"E17", "E23"}) || request.Identity.DefinitionVersion != 2 || request.PromptRef != WorkspaceAnalysisSynthesisPromptRefV2() {
		t.Fatal("synthesis lost the dynamic reference or version contract")
	}
	first, _ := toolsdomain.ReadSourceV4ReceiptBinding(f.base.authority.result.ReadSourceReceipts[0])
	second, _ := toolsdomain.ReadSourceV4ReceiptBinding(f.base.authority.result.ReadSourceReceipts[1])
	if request.Retrieval.IndexVersionID != first.Identity.IndexVersionID || first.Identity.IndexVersionID == second.Identity.IndexVersionID {
		t.Fatal("synthesis replaced the actual first Search index")
	}
	var input struct {
		GitStatus json.RawMessage                           `json:"git_status"`
		Searches  []workspaceAnalysisSynthesisSearchInput   `json:"searches"`
		Evidence  []workspaceAnalysisSynthesisEvidenceInput `json:"evidence"`
	}
	if json.Unmarshal(request.Input, &input) != nil || string(input.GitStatus) != "null" || len(input.Searches) != 2 || input.Searches[0].HitCount != 1 || input.Searches[0].DegradationCodes == nil || len(input.Evidence) != 2 || f.outputs.outputCalls != 0 {
		t.Fatal("optional Git or multiple Search summaries drifted")
	}
	for _, secret := range []string{string(f.run.ID), string(first.SearchReceiptID), string(first.Identity.IndexVersionID), first.Identity.ContentHash, "content_hash", "citation_id", "private_binding"} {
		if bytes.Contains(request.Input, []byte(secret)) {
			t.Fatal("synthesis received server-only evidence identity")
		}
	}
	if _, err := decodeWorkspaceAnalysisSynthesisOutputV2(result.Output); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(result.Output, []byte("answer_markdown")) || bytes.Contains(result.Output, []byte("excerpt")) {
		t.Fatal("synthesis node output exposed the candidate or source body")
	}
}

func TestWorkspaceAnalysisV2SynthesisWithoutEffectiveReadsUsesFinishProof(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprint(truncated), func(t *testing.T) {
			refs := []string{}
			if truncated {
				refs = []string{"E17"}
			}
			f := newWorkspaceAnalysisV2ExecutorFixture(t, refs...)
			if truncated {
				read := &f.base.authority.result.ReadSourceReceipts[0]
				var output map[string]any
				_ = json.Unmarshal(read.Output, &output)
				output["truncated"] = true
				read.Output = v2ExecutorJSON(t, output)
				read.OutputHash, read.OutputBytes = workspaceAnalysisV2Hash(read.Output), int64(len(read.Output))
				f.base.authority.result.Evidence[0].Truncated = true
				f.steps[1].receipt = *read
				f.setLoop(t, f.steps)
			}
			f.base.finalizer.terminationOutput = f.publication(conversationdomain.AnswerPublicationRefused, nil)
			execution := f.execution(conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer)
			_, err := f.executor(t, execution.NodeKey).Execute(context.Background(), execution)
			if err != nil {
				t.Fatal(err)
			}
			command := f.base.finalizer.terminationCommand
			finish, _, _ := workspaceAnalysisV2Finish(f.journal.snapshot)
			if command.Validate() != nil || f.base.synthesis.calls != 0 || f.base.finalizer.terminationCalls != 1 || command.Reason != agentdomain.WorkspaceAnalysisRunEvidenceInsufficient ||
				command.DefinitionVersion != 2 || command.OperationID == nil || *command.OperationID != finish.OperationID || command.Artifact == nil ||
				command.Artifact.Kind != conversationapplication.WorkspaceAnalysisTerminationArtifactDecisionReceipt || command.Artifact.ID != finish.ID || command.Artifact.Hash != finish.DocumentHash {
				t.Fatal("empty evidence invented a Search proof or called synthesis")
			}
		})
	}
}

func TestWorkspaceAnalysisV2SynthesisRejectsDetachedPredecessorAndEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*workspaceAnalysisV2ExecutorFixture, *workflowapplication.ExecutionContext){
		"predecessor": func(_ *workspaceAnalysisV2ExecutorFixture, e *workflowapplication.ExecutionContext) {
			e.Input = append(e.Input, ' ')
		},
		"receipt hash": func(f *workspaceAnalysisV2ExecutorFixture, _ *workflowapplication.ExecutionContext) {
			f.base.authority.result.ReadSourceReceipts[0].OutputHash = strings.Repeat("f", 64)
		},
		"global alias": func(f *workspaceAnalysisV2ExecutorFixture, _ *workflowapplication.ExecutionContext) {
			f.base.authority.result.EvidenceRefs[0] = "E1"
		},
		"Search identity": func(f *workspaceAnalysisV2ExecutorFixture, _ *workflowapplication.ExecutionContext) {
			f.base.authority.result.SearchReceipts[0].ID = v2ExecutorID(9000)
		},
		"decision association": func(f *workspaceAnalysisV2ExecutorFixture, _ *workflowapplication.ExecutionContext) {
			foreign := v2ExecutorID(9000)
			f.journal.snapshot.Entries[1].DecisionID = &foreign
		},
		"cross run": func(f *workspaceAnalysisV2ExecutorFixture, _ *workflowapplication.ExecutionContext) {
			f.journal.snapshot.Entries[0].Decision.AnalysisRunID = v2ExecutorID(9000)
		},
		"model workspace": func(f *workspaceAnalysisV2ExecutorFixture, _ *workflowapplication.ExecutionContext) {
			f.journal.snapshot.Entries[0].ModelRun.WorkspaceID = v2ExecutorID(9000)
		},
		"model request": func(f *workspaceAnalysisV2ExecutorFixture, _ *workflowapplication.ExecutionContext) {
			f.journal.snapshot.Entries[0].ModelCall.RequestHash = strings.Repeat("f", 64)
		},
		"model attempt": func(f *workspaceAnalysisV2ExecutorFixture, _ *workflowapplication.ExecutionContext) {
			f.journal.snapshot.Entries[0].Decision.NodeAttemptID = v2ExecutorID(9000)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newWorkspaceAnalysisV2ExecutorFixture(t, "E17")
			execution := f.execution(conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer)
			mutate(f, &execution)
			if _, err := f.executor(t, execution.NodeKey).Execute(context.Background(), execution); err == nil || f.base.synthesis.calls != 0 || f.base.finalizer.successCalls != 0 {
				t.Fatalf("detached evidence crossed synthesis: %v", err)
			}
		})
	}
}

func TestWorkspaceAnalysisV2FinalCitationRequiresCandidateBoundReceipt(t *testing.T) {
	for _, loopReceipt := range []bool{false, true} {
		t.Run(fmt.Sprint(loopReceipt), func(t *testing.T) {
			f := newWorkspaceAnalysisV2ExecutorFixture(t, "E17", "E23")
			candidate := f.persistCandidate(t)
			receipt := f.citationReceipt(t, candidate, loopReceipt)
			f.prepareValidationTool(t, receipt)
			execution := f.execution(conversationworkflow.WorkspaceAnalysisNodeValidateCitations)
			result, err := f.executor(t, execution.NodeKey).Execute(context.Background(), execution)
			if loopReceipt {
				if err == nil {
					t.Fatal("loop Citation receipt replaced final candidate validation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var args struct {
				CandidateID   foundation.ID `json:"candidate_id"`
				CandidateHash string        `json:"candidate_hash"`
				Refs          []string      `json:"evidence_refs"`
			}
			if len(f.tools.commands) != 1 || json.Unmarshal(f.tools.commands[0].Tool.Request.Arguments, &args) != nil || args.CandidateID != candidate.ID || args.CandidateHash != candidate.DocumentHash || !reflect.DeepEqual(args.Refs, []string{"E17", "E23"}) {
				t.Fatal("final Citation request was not bound to the whole candidate")
			}
			if _, err := decodeWorkspaceAnalysisValidationOutputV2(result.Output); err != nil {
				t.Fatal(err)
			}
			if f.review.calls != 0 || f.base.finalizer.successCalls != 0 {
				t.Fatal("Citation node published before independent review")
			}
		})
	}
}

func TestWorkspaceAnalysisV2ReviewPreservesOptionalLatestGitAndIndependentGate(t *testing.T) {
	for _, withGit := range []bool{false, true} {
		t.Run(fmt.Sprint(withGit), func(t *testing.T) {
			f := newWorkspaceAnalysisV2ExecutorFixture(t, "E17", "E23")
			var lastGit toolsdomain.ResultReceipt
			if withGit {
				for range 2 {
					lastGit = f.receipt(t, toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 3}, map[string]any{"branch": "main", "head": strings.Repeat("a", 40), "object_format": "sha1", "clean": true, "staged_count": 0, "unstaged_count": 0, "untracked_count": 0, "conflict_count": 0}, nil)
					f.steps = append(f.steps, workspaceAnalysisV2TestStep{decision: agentdomain.WorkspaceAnalysisDecision{Action: agentdomain.WorkspaceAnalysisDecisionGitStatus}, receipt: lastGit})
				}
				f.setLoop(t, f.steps)
			}
			candidate := f.persistCandidate(t)
			receipt := f.citationReceipt(t, candidate, false)
			f.persistValidation(t, candidate, receipt)
			execution := f.execution(conversationworkflow.WorkspaceAnalysisNodeReviewPublish)
			f.review.result = f.reviewResult(t, candidate, true)
			f.base.finalizer.successOutput = f.publication(conversationdomain.AnswerPublicationCompleted, &candidate.SynthesisModelRunID)
			_, err := f.executor(t, execution.NodeKey).Execute(context.Background(), execution)
			if err != nil {
				t.Fatal(err)
			}
			command := f.base.finalizer.successCommand
			if command.Validate() != nil || command.DefinitionVersion != 2 || f.review.calls != 1 || command.CandidateID != candidate.ID || command.ValidationReceiptID != receipt.ID ||
				!reflect.DeepEqual(f.review.request.Evidence, []agentapplication.WorkspaceAnalysisReviewEvidence{{EvidenceRef: "E17", Excerpt: "source E17"}, {EvidenceRef: "E23", Excerpt: "source E23"}}) {
				t.Fatal("review/publish lost its independent candidate/evidence binding")
			}
			if command.GitReceiptID != lastGit.ID || command.GitReceiptHash != lastGit.OutputHash {
				t.Fatal("optional latest Git proof drifted")
			}
		})
	}
}

func TestWorkspaceAnalysisV2ReviewRejectionsKeepTheirExactProof(t *testing.T) {
	for _, invalidCitation := range []bool{false, true} {
		t.Run(fmt.Sprint(invalidCitation), func(t *testing.T) {
			f := newWorkspaceAnalysisV2ExecutorFixture(t, "E17")
			candidate := f.persistCandidate(t)
			receipt := f.citationReceipt(t, candidate, false)
			if invalidCitation {
				receipt.Output = v2ExecutorJSON(t, map[string]any{"results": []any{map[string]any{"evidence_ref": "E17", "valid": false, "reason_code": "EVIDENCE_INELIGIBLE"}}})
				receipt.OutputHash, receipt.OutputBytes = workspaceAnalysisV2Hash(receipt.Output), int64(len(receipt.Output))
			}
			f.persistValidation(t, candidate, receipt)
			f.review.result = f.reviewResult(t, candidate, false)
			f.base.finalizer.terminationOutput = f.publication(conversationdomain.AnswerPublicationRefused, &candidate.SynthesisModelRunID)
			execution := f.execution(conversationworkflow.WorkspaceAnalysisNodeReviewPublish)
			if _, err := f.executor(t, execution.NodeKey).Execute(context.Background(), execution); err != nil {
				t.Fatal(err)
			}
			command := f.base.finalizer.terminationCommand
			if command.Validate() != nil || f.base.finalizer.successCalls != 0 || f.base.finalizer.terminationCalls != 1 || command.Artifact == nil || command.OperationID == nil {
				t.Fatal("rejected candidate did not close through an exact proof")
			}
			if invalidCitation {
				if f.review.calls != 0 || command.Reason != agentdomain.WorkspaceAnalysisRunCitationInvalid || command.Artifact.ID != receipt.ID || command.Artifact.Hash != receipt.OutputHash || *command.OperationID != f.validation.authority.OperationID {
					t.Fatal("invalid Citation invoked Review or lost its receipt")
				}
			} else if f.review.calls != 1 || command.Reason != agentdomain.WorkspaceAnalysisRunFaithfulnessRejected || command.Artifact.ID != f.review.result.ModelResult.ID || command.Artifact.Hash != f.review.result.ModelResult.DocumentHash || *command.OperationID != f.review.result.ModelResult.OperationID {
				t.Fatal("failed Review lost its candidate-bound model result")
			}
		})
	}
}

func TestWorkspaceAnalysisV2UnprovenModelOutcomeCannotAdvanceOrPublish(t *testing.T) {
	f := newWorkspaceAnalysisV2ExecutorFixture(t, "E17")
	f.base.synthesis.err = foundation.NewError(foundation.ErrorManualRecoveryRequired, agentapplication.ErrorCodeModelCallPersistenceUnknown, false, errors.New("commit result is not proven"))
	execution := f.execution(conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer)
	result, err := f.executor(t, execution.NodeKey).Execute(context.Background(), execution)
	if err == nil || len(result.Output) != 0 || f.base.synthesis.calls != 1 || len(f.tools.commands) != 0 || f.review.calls != 0 || f.base.finalizer.successCalls != 0 || f.base.finalizer.terminationCalls != 0 {
		t.Fatal("unknown synthesis outcome was promoted to a candidate or terminal proof")
	}
}

func TestWorkspaceAnalysisV2FullReferenceNamespaceStillUsesDurableAdmission(t *testing.T) {
	f := newWorkspaceAnalysisV2ExecutorFixture(t, "E17")
	f.outputs.limit = 0
	denial := &agentapplication.WorkspaceAnalysisAdmissionDenial{OperationID: v2ExecutorID(9500), Reason: agentdomain.WorkspaceAnalysisRunBudgetExhausted, Requested: agentdomain.WorkspaceAnalysisBudgetAmount{ToolCalls: 1}}
	f.tools.execute = func(toolsapplication.ExecuteWorkspaceAnalysisToolCommand) (toolsapplication.ToolExecutionResult, error) {
		return toolsapplication.ToolExecutionResult{}, denial
	}
	execution := f.execution(conversationworkflow.WorkspaceAnalysisNodeDecideNext)
	state := f.state(execution)
	entry := f.journal.snapshot.Entries[0]
	accepted := agentapplication.WorkspaceAnalysisDecisionMutationResult{Run: *entry.ModelRun, Call: *entry.ModelCall, Operation: entry.Operation, Decision: entry.Decision}
	invoker := workspaceAnalysisV2LoopToolInvoker{executor: f.executor(t, execution.NodeKey).(*workspaceAnalysisV2NodeExecutor), state: state}
	_, err := invoker.InvokeWorkspaceAnalysisDecisionTool(context.Background(), accepted)
	actual, found := agentapplication.WorkspaceAnalysisAdmissionDenialFromError(err)
	if !found || actual.OperationID != denial.OperationID || len(f.tools.commands) != 1 || f.outputs.outputCalls != 0 {
		t.Fatal("reference limit was not denied by the persistent authority")
	}
	var args struct {
		Limit int    `json:"limit"`
		Mode  string `json:"mode"`
	}
	if json.Unmarshal(f.tools.commands[0].Tool.Request.Arguments, &args) != nil || args.Limit != 1 || args.Mode != string(f.base.question.Question.Request.Scope.RetrievalMode) {
		t.Fatal("zero-capacity request did not carry a legal server-frozen tool request")
	}
	f.base.finalizer.terminationOutput = f.publication(conversationdomain.WorkspaceAnalysisPublicationFailed, nil)
	_ = invoker.executor.finalizeError(context.Background(), state, err)
	command := f.base.finalizer.terminationCommand
	if command.OperationID == nil || *command.OperationID != denial.OperationID || command.BudgetRequest == nil || command.BudgetRequest.ToolCalls != 1 || command.Validate() != nil {
		t.Fatal("budget termination lost the actual pending proof")
	}
}

type workspaceAnalysisV2TestStep struct {
	decision agentdomain.WorkspaceAnalysisDecision
	receipt  toolsdomain.ResultReceipt
}

type workspaceAnalysisV2ExecutorFixture struct {
	base       *workspaceAnalysisSynthesizeFixture
	run        agentdomain.WorkspaceAnalysisRun
	journal    *workspaceAnalysisV2JournalFake
	loop       *workspaceAnalysisV2LoopFake
	outputs    *workspaceAnalysisV2OutputsFake
	tools      *workspaceAnalysisV2ToolsFake
	validation *workspaceAnalysisV2ValidationFake
	candidates *workspaceAnalysisReviewCandidateFake
	review     *workspaceAnalysisReviewRunnerFake
	catalog    *agentapplication.RuntimeCatalog
	steps      []workspaceAnalysisV2TestStep
	nextID     int
}

func newWorkspaceAnalysisV2ExecutorFixture(t *testing.T, refs ...string) *workspaceAnalysisV2ExecutorFixture {
	t.Helper()
	base := newWorkspaceAnalysisSynthesizeFixture(t, 1)
	base.clock = time.Now().UTC().Truncate(time.Millisecond)
	run := base.run
	run.DefinitionVersion, run.PolicyVersion, run.DefinitionHash = 2, 2, conversationworkflow.WorkspaceAnalysisGraphHashV2
	run.CreatedAt, run.UpdatedAt = base.clock, base.clock
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV2Deadlines(run.Timeouts)
	if err != nil {
		t.Fatal(err)
	}
	run.DeadlineAt = run.CreatedAt.Add(deadlines.RunDeadline())
	run.Limits = agentdomain.WorkspaceAnalysisBudgetLimits{Nodes: 4, ToolConcurrency: 1, Amount: agentdomain.WorkspaceAnalysisBudgetAmount{ModelCalls: 14, ToolCalls: 13, SourceReads: 8, InputTokens: agentdomain.WorkspaceAnalysisV2MaxRunInputTokens, OutputTokens: agentdomain.WorkspaceAnalysisV2MaxRunOutputTokens}}
	if err := agentdomain.ValidateWorkspaceAnalysisRun(run); err != nil {
		t.Fatal(err)
	}
	base.runs.run = run
	base.stages.outputs = map[string]json.RawMessage{}
	base.authority.result = toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{WorkspaceID: run.WorkspaceID, WorkflowRunID: run.WorkflowRunID, AnalysisRunID: run.ID, EvidenceRefs: []string{}, Evidence: []toolsdomain.ReadSourceV3ReceiptEvidence{}, SearchReceipts: []toolsdomain.ResultReceipt{}, ReadSourceReceipts: []toolsdomain.ResultReceipt{}}
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: agentdomain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "dynamic-fixture", ModelVersion: "v1"}, Timeout: time.Minute, MaxOutputTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	f := &workspaceAnalysisV2ExecutorFixture{base: base, run: run, catalog: catalog, journal: &workspaceAnalysisV2JournalFake{}, loop: &workspaceAnalysisV2LoopFake{}, outputs: &workspaceAnalysisV2OutputsFake{limit: 5}, tools: &workspaceAnalysisV2ToolsFake{}, validation: &workspaceAnalysisV2ValidationFake{}, candidates: &workspaceAnalysisReviewCandidateFake{}, review: &workspaceAnalysisReviewRunnerFake{}, nextID: 1000}
	for _, ref := range refs {
		identity := map[string]any{"evidence_ref": "E1", "citation_id": "cite-" + strings.Repeat("a", 64), "index_version_id": f.id(), "chunk_id": f.id(), "source_version_id": f.id(), "source_span_id": f.id(), "content_hash": strings.Repeat("b", 64)}
		search := f.receipt(t, toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 3}, map[string]any{"effective_mode": "keyword", "degradations": []any{}, "items": []any{map[string]any{"evidence_ref": "E1", "rank": 1, "snippet": "safe snippet"}}}, map[string]any{"items": []any{identity}, "selected_refs": []string{"E1"}})
		private := map[string]any{}
		for k, v := range identity {
			private[k] = v
		}
		private["evidence_ref"], private["search_evidence_ref"], private["search_receipt_id"], private["search_receipt_hash"] = ref, "E1", search.ID, search.OutputHash
		read := f.receipt(t, toolsdomain.ToolRef{Name: "ReadSource", Version: 4}, map[string]any{"evidence_ref": ref, "content_hash": identity["content_hash"], "excerpt": "source " + ref, "truncated": false}, private)
		item, err := toolsdomain.ReadSourceV4ReceiptEvidenceForSearch(read, search)
		if err != nil {
			t.Fatal(err)
		}
		query := "query " + ref
		refCopy := ref
		f.steps = append(f.steps, workspaceAnalysisV2TestStep{agentdomain.WorkspaceAnalysisDecision{Action: agentdomain.WorkspaceAnalysisDecisionKnowledgeSearch, Query: &query}, search}, workspaceAnalysisV2TestStep{agentdomain.WorkspaceAnalysisDecision{Action: agentdomain.WorkspaceAnalysisDecisionSourceRead, EvidenceRef: &refCopy}, read})
		base.authority.result.EvidenceRefs = append(base.authority.result.EvidenceRefs, ref)
		base.authority.result.Evidence = append(base.authority.result.Evidence, item)
		base.authority.result.SearchReceipts = append(base.authority.result.SearchReceipts, search)
		base.authority.result.ReadSourceReceipts = append(base.authority.result.ReadSourceReceipts, read)
	}
	f.setLoop(t, f.steps)
	base.synthesis.build = func(request agentapplication.WorkspaceAnalysisSynthesisRequest) agentapplication.WorkspaceAnalysisSynthesisResult {
		return f.synthesisResult(t, request)
	}
	return f
}

func (f *workspaceAnalysisV2ExecutorFixture) id() foundation.ID {
	f.nextID++
	return v2ExecutorID(f.nextID)
}
func v2ExecutorID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("99000000-0000-4000-8000-%012d", value))
}
func v2ExecutorJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func workspaceAnalysisV2TestNodeKeys() []string {
	return []string{conversationworkflow.WorkspaceAnalysisNodeDecideNext, conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer, conversationworkflow.WorkspaceAnalysisNodeValidateCitations, conversationworkflow.WorkspaceAnalysisNodeReviewPublish}
}

func (f *workspaceAnalysisV2ExecutorFixture) execution(key string) workflowapplication.ExecutionContext {
	e := f.base.execution
	e.DefinitionVersion, e.DefinitionHash, e.NodeKey, e.NodeKind = 2, conversationworkflow.WorkspaceAnalysisGraphHashV2, key, conversationworkflow.WorkspaceAnalysisNodeKindV2(key)
	for index, value := range workspaceAnalysisV2TestNodeKeys() {
		if value == key {
			e.NodeRunID, e.NodeAttemptID = v2ExecutorID(10+index*2), v2ExecutorID(11+index*2)
		}
	}
	switch key {
	case conversationworkflow.WorkspaceAnalysisNodeDecideNext:
		e.Input = append(json.RawMessage(nil), f.base.inputs.raw...)
	case conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer:
		e.Input = append(json.RawMessage(nil), f.base.stages.outputs[conversationworkflow.WorkspaceAnalysisNodeDecideNext]...)
	case conversationworkflow.WorkspaceAnalysisNodeValidateCitations:
		e.Input = append(json.RawMessage(nil), f.base.stages.outputs[conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer]...)
	case conversationworkflow.WorkspaceAnalysisNodeReviewPublish:
		e.Input = append(json.RawMessage(nil), f.base.stages.outputs[conversationworkflow.WorkspaceAnalysisNodeValidateCitations]...)
	}
	return e
}

func (f *workspaceAnalysisV2ExecutorFixture) state(e workflowapplication.ExecutionContext) workspaceAnalysisV2Execution {
	return workspaceAnalysisV2Execution{execution: e, root: f.base.root, question: f.base.question, run: f.run, journal: f.journal.snapshot}
}
func (f *workspaceAnalysisV2ExecutorFixture) executor(t *testing.T, key string) workflowapplication.Executor {
	t.Helper()
	executors, err := NewWorkspaceAnalysisV2Executors(WorkspaceAnalysisV2ExecutorDependencies{Context: f.base.context, Runs: f.base.runs, Inputs: f.base.inputs, Stages: f.base.stages, Journal: f.journal, Catalog: f.catalog, Decisions: f.loop, Runtime: f.loop, Tools: f.tools, ToolOutputs: f.outputs, Evidence: f.base.authority, Candidates: f.candidates, Validation: f.validation, Synthesis: f.base.synthesis, Review: f.review, Finalizer: f.base.finalizer, Clock: foundation.FixedClock{Value: f.base.clock}})
	if err != nil {
		t.Fatal(err)
	}
	switch key {
	case conversationworkflow.WorkspaceAnalysisNodeDecideNext:
		return executors.DecideNext
	case conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer:
		return executors.SynthesizeAnswer
	case conversationworkflow.WorkspaceAnalysisNodeValidateCitations:
		return executors.ValidateCitations
	default:
		return executors.ReviewPublish
	}
}

func (f *workspaceAnalysisV2ExecutorFixture) receipt(t *testing.T, ref toolsdomain.ToolRef, output, private any) toolsdomain.ResultReceipt {
	t.Helper()
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(ref)
	if !found {
		t.Fatal("receipt contract missing")
	}
	e := f.execution(conversationworkflow.WorkspaceAnalysisNodeDecideNext)
	raw := v2ExecutorJSON(t, output)
	r := toolsdomain.ResultReceipt{ID: f.id(), ToolCallID: f.id(), WorkspaceID: f.run.WorkspaceID, WorkflowRunID: f.run.WorkflowRunID, NodeRunID: e.NodeRunID, NodeAttemptID: e.NodeAttemptID, Tool: ref, OutputSchema: contract.OutputSchema, DefinitionHash: strings.Repeat("c", 64), PersistencePolicy: toolsdomain.ResultPersistenceCanonical, MaxOutputBytes: contract.MaxOutputBytes, MaxPrivateBindingBytes: contract.MaxPrivateBindingBytes, Output: raw, OutputHash: workspaceAnalysisV2Hash(raw), OutputBytes: int64(len(raw)), CreatedAt: f.base.clock.Add(time.Second)}
	if private != nil {
		b := v2ExecutorJSON(t, private)
		r.PrivateBinding = &toolsdomain.ResultReceiptPrivateBinding{Schema: contract.PrivateBindingSchema, Document: b, Hash: workspaceAnalysisV2Hash(b), Bytes: int64(len(b))}
	}
	return r
}

func (f *workspaceAnalysisV2ExecutorFixture) setLoop(t *testing.T, steps []workspaceAnalysisV2TestStep) {
	t.Helper()
	f.journal.snapshot = agentapplication.WorkspaceAnalysisJournalSnapshot{Run: f.run}
	e := f.execution(conversationworkflow.WorkspaceAnalysisNodeDecideNext)
	model := agentdomain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "dynamic-fixture", ModelVersion: "v1"}
	schema := agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisDecisionSchemaID, Version: agentdomain.WorkspaceAnalysisDecisionSchemaVersion}
	completed := f.base.clock.Add(time.Second)
	run := agentdomain.ModelRun{ID: v2ExecutorID(25), WorkspaceID: e.WorkspaceID, WorkflowRunID: e.RunID, NodeRunID: e.NodeRunID, NodeAttemptID: e.NodeAttemptID, Model: model, Profile: DefaultProfileRef(), Prompt: WorkspaceAnalysisDecisionPromptRef(), Schema: schema, ReducedSchema: schema, Status: agentdomain.ModelRunSucceeded, FinalResultType: agentdomain.ResultTypeWorkspaceAnalysisDecision, Version: 2, CreatedAt: f.base.clock, UpdatedAt: completed, CompletedAt: &completed}
	for index := 0; index <= len(steps); index++ {
		ordinal := index + 1
		decision := agentdomain.WorkspaceAnalysisDecision{Action: agentdomain.WorkspaceAnalysisDecisionFinish}
		if index < len(steps) {
			decision = steps[index].decision
		}
		raw, err := decision.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		call := agentdomain.ModelCall{ID: v2ExecutorID(100 + ordinal*10), ModelRunID: run.ID, CallNo: ordinal, Phase: agentdomain.ModelCallAgent, Model: model, Profile: run.Profile, Prompt: run.Prompt, Schema: schema, MaxOutputTokens: 512, Status: agentdomain.ModelCallSucceeded, RequestHash: workspaceAnalysisV2Hash([]byte(`{}`)), RequestBytes: 2, ResponseHash: workspaceAnalysisV2Hash(raw), ResponseBytes: int64(len(raw)), Usage: agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, Version: 2, StartedAt: f.base.clock, CompletedAt: &completed}
		opID := v2ExecutorID(101 + ordinal*10)
		d := agentdomain.WorkspaceAnalysisDecisionReceipt{ID: v2ExecutorID(102 + ordinal*10), WorkspaceID: f.run.WorkspaceID, AnalysisRunID: f.run.ID, OperationID: opID, NodeAttemptID: e.NodeAttemptID, ModelRunID: run.ID, ModelCallID: call.ID, Ordinal: ordinal, Decision: decision, DocumentHash: call.ResponseHash, DocumentBytes: call.ResponseBytes, CreatedAt: completed}
		op := f.operation(t, opID, e.NodeKey, agentdomain.WorkspaceAnalysisOperationDecision, ordinal, e.NodeAttemptID, call.ID, d.ID, d.DocumentHash, agentdomain.WorkspaceAnalysisOperationResultDecision)
		f.appendJournal(t, agentapplication.WorkspaceAnalysisJournalEntry{Operation: op, ModelRun: &run, ModelCall: &call, Decision: &d})
		if index < len(steps) {
			r := steps[index].receipt
			op := f.operation(t, v2ExecutorID(103+ordinal*10), e.NodeKey, workspaceAnalysisV2DecisionToolKind(decision.Action), ordinal, r.NodeAttemptID, r.ToolCallID, r.ID, r.OutputHash, agentdomain.WorkspaceAnalysisOperationResultToolReceipt)
			f.appendJournal(t, agentapplication.WorkspaceAnalysisJournalEntry{Operation: op, DecisionID: &d.ID})
		} else {
			f.loop.result = agentapplication.WorkspaceAnalysisLoopResult{Finish: agentapplication.WorkspaceAnalysisDecisionMutationResult{Run: run, Call: call, Operation: op, Decision: &d}, Decisions: ordinal, ToolCalls: len(steps)}
			f.base.stages.outputs[e.NodeKey] = v2ExecutorJSON(t, workspaceAnalysisLoopOutputV2{SchemaVersion: 2, FinishDecisionID: d.ID, FinishDecisionHash: d.DocumentHash, DecisionCount: ordinal, ToolCount: len(steps)})
		}
	}
}

func (f *workspaceAnalysisV2ExecutorFixture) operation(t *testing.T, id foundation.ID, node string, kind agentdomain.WorkspaceAnalysisOperationKind, ordinal int, attempt, call, result foundation.ID, hash string, resultKind agentdomain.WorkspaceAnalysisOperationResultKind) agentdomain.WorkspaceAnalysisOperation {
	t.Helper()
	contract, err := agentdomain.WorkspaceAnalysisOperationContractForKey(agentdomain.WorkspaceAnalysisOperationKey{AnalysisRunID: f.run.ID, NodeKey: agentdomain.WorkspaceAnalysisOperationNodeKey(node), Kind: kind, Ordinal: ordinal})
	if err != nil {
		t.Fatal(err)
	}
	reservation := f.id()
	started, completed := f.base.clock, f.base.clock.Add(time.Second)
	op := agentdomain.WorkspaceAnalysisOperation{ID: id, AnalysisRunID: f.run.ID, NodeKey: agentdomain.WorkspaceAnalysisOperationNodeKey(node), Kind: kind, Ordinal: ordinal, RequestHash: workspaceAnalysisV2Hash([]byte(`{}`)), Status: agentdomain.WorkspaceAnalysisOperationSucceeded, FirstNodeAttemptID: &attempt, LatestNodeAttemptID: &attempt, BudgetReservationID: &reservation, Call: &agentdomain.WorkspaceAnalysisOperationCallRef{Kind: contract.CallKind, ID: call}, Result: &agentdomain.WorkspaceAnalysisOperationResultRef{Kind: resultKind, ID: result, Hash: hash}, Version: 3, CreatedAt: started, UpdatedAt: completed, StartedAt: &started, CompletedAt: &completed}
	if err := agentdomain.ValidateWorkspaceAnalysisOperation(op); err != nil {
		t.Fatal(err)
	}
	return op
}

func (f *workspaceAnalysisV2ExecutorFixture) appendJournal(t *testing.T, entry agentapplication.WorkspaceAnalysisJournalEntry) {
	t.Helper()
	entry.Sequence = int64(len(f.journal.snapshot.Entries) + 1)
	f.journal.snapshot.Entries = append(f.journal.snapshot.Entries, entry)
	if err := validateWorkspaceAnalysisV2Journal(f.state(f.execution(conversationworkflow.WorkspaceAnalysisNodeDecideNext)), f.journal.snapshot); err != nil {
		t.Fatal(err)
	}
}

func (f *workspaceAnalysisV2ExecutorFixture) synthesisResult(t *testing.T, request agentapplication.WorkspaceAnalysisSynthesisRequest) agentapplication.WorkspaceAnalysisSynthesisResult {
	t.Helper()
	result := workspaceAnalysisSynthesisResultFixture(t, f.execution(conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer), request, f.base.clock)
	result.CandidateResult.SchemaVersion = "2"
	result.CandidateResult.Payload.CitationRefs = append([]string(nil), request.AllowedEvidenceRefs...)
	result.CandidateResult.Payload.AnswerMarkdown = "Grounded [" + strings.Join(request.AllowedEvidenceRefs, "] [") + "]."
	raw := v2ExecutorJSON(t, result.CandidateResult)
	result.Candidate.SchemaVersion = 2
	result.Candidate.Document = raw
	result.Candidate.DocumentHash = workspaceAnalysisV2Hash(raw)
	result.Candidate.DocumentBytes = int64(len(raw))
	result.Run.Schema.Version = "2"
	result.Run.ReducedSchema = result.Run.Schema
	result.Call.Schema = result.Run.Schema
	result.Call.ResponseHash = result.Candidate.DocumentHash
	result.Call.ResponseBytes = result.Candidate.DocumentBytes
	return result
}

func (f *workspaceAnalysisV2ExecutorFixture) persistCandidate(t *testing.T) agentdomain.WorkspaceAnalysisCandidate {
	t.Helper()
	e := f.execution(conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer)
	evidence, err := validateWorkspaceAnalysisEvidenceV2(f.state(e), f.base.authority.result)
	if err != nil {
		t.Fatal(err)
	}
	request := agentapplication.WorkspaceAnalysisSynthesisRequest{Identity: workspaceAnalysisModelIdentity(e), AnalysisRunID: f.run.ID, AnswerID: f.base.root.AnswerID, AttemptNo: e.AttemptNo, ProfileRef: DefaultProfileRef(), PromptRef: WorkspaceAnalysisSynthesisPromptRefV2(), Retrieval: evidence.retrieval, AllowedEvidenceRefs: evidence.refs}
	r := f.synthesisResult(t, request)
	f.candidates.candidate = r.Candidate
	op := f.operation(t, r.Candidate.SynthesisOperationID, e.NodeKey, agentdomain.WorkspaceAnalysisOperationAnswerSynthesis, 1, e.NodeAttemptID, r.Call.ID, r.Candidate.ID, r.Candidate.DocumentHash, agentdomain.WorkspaceAnalysisOperationResultCandidate)
	op.RequestHash = r.Call.RequestHash
	f.appendJournal(t, agentapplication.WorkspaceAnalysisJournalEntry{Operation: op, ModelRun: &r.Run, ModelCall: &r.Call})
	f.base.stages.outputs[e.NodeKey] = v2ExecutorJSON(t, workspaceAnalysisSynthesisOutputV2{SchemaVersion: 2, CandidateID: r.Candidate.ID, CandidateHash: r.Candidate.DocumentHash, SynthesisModelRunID: r.Run.ID, SynthesisModelCallID: r.Call.ID, DraftSessionID: r.Draft.ID, DraftGeneration: r.Draft.Generation})
	return r.Candidate
}

func (f *workspaceAnalysisV2ExecutorFixture) citationReceipt(t *testing.T, candidate agentdomain.WorkspaceAnalysisCandidate, loop bool) toolsdomain.ResultReceipt {
	t.Helper()
	items, results := []any{}, []any{}
	for _, read := range f.base.authority.result.ReadSourceReceipts {
		var item map[string]any
		_ = json.Unmarshal(read.PrivateBinding.Document, &item)
		item["read_receipt_id"], item["read_receipt_hash"] = read.ID, read.OutputHash
		items = append(items, item)
		results = append(results, map[string]any{"evidence_ref": item["evidence_ref"], "valid": true, "reason_code": "OK"})
	}
	private := map[string]any{"candidate_id": candidate.ID, "candidate_hash": candidate.DocumentHash, "results": items}
	if loop {
		private["candidate_id"], private["candidate_hash"] = nil, nil
	}
	receipt := f.receipt(t, toolsdomain.ToolRef{Name: "ValidateCitation", Version: 4}, map[string]any{"results": results}, private)
	e := f.execution(conversationworkflow.WorkspaceAnalysisNodeValidateCitations)
	receipt.NodeRunID, receipt.NodeAttemptID = e.NodeRunID, e.NodeAttemptID
	return receipt
}

func (f *workspaceAnalysisV2ExecutorFixture) reviewResult(t *testing.T, candidate agentdomain.WorkspaceAnalysisCandidate, passed bool) agentapplication.WorkspaceAnalysisReviewResult {
	t.Helper()
	result := workspaceAnalysisReviewResultFixture(t, f.execution(conversationworkflow.WorkspaceAnalysisNodeReviewPublish), f.run, candidate, passed, f.base.clock)
	result.Review.Payload.Items[0].CitationIDs = append([]string{}, f.base.authority.result.EvidenceRefs...)
	result.Review.Payload.Items[0].Reason = "checked against opened global references"
	result.ModelResult.Document = v2ExecutorJSON(t, result.Review)
	result.ModelResult.DocumentHash, result.ModelResult.DocumentBytes = workspaceAnalysisV2Hash(result.ModelResult.Document), int64(len(result.ModelResult.Document))
	result.Call.ResponseHash = result.ModelResult.DocumentHash
	return result
}

func (f *workspaceAnalysisV2ExecutorFixture) prepareValidationTool(t *testing.T, receipt toolsdomain.ResultReceipt) {
	t.Helper()
	f.tools.execute = func(command toolsapplication.ExecuteWorkspaceAnalysisToolCommand) (toolsapplication.ToolExecutionResult, error) {
		e := f.execution(conversationworkflow.WorkspaceAnalysisNodeValidateCitations)
		input := toolsdomain.SchemaRef{ID: "tool.validate_citation.input", Version: 3}
		ref := receipt.Tool
		started, completed := f.base.clock, f.base.clock.Add(time.Second)
		call := toolsdomain.ToolCall{ID: receipt.ToolCallID, WorkspaceID: e.WorkspaceID, WorkflowRunID: e.RunID, NodeRunID: e.NodeRunID, NodeAttemptID: e.NodeAttemptID, CallNo: 1, RequestedToolName: ref.Name, Tool: &ref, DefinitionHash: receipt.DefinitionHash, InputSchema: &input, OutputSchema: &receipt.OutputSchema, SideEffectLevel: toolsdomain.SideEffectNone, InvocationPolicy: toolsdomain.InvocationTrustedWorkflowOnly, RequestHash: workspaceAnalysisV2Hash([]byte(`{}`)), RequestBytes: 2, RequestSummary: json.RawMessage(`{}`), ResponseHash: receipt.OutputHash, ResponseBytes: receipt.OutputBytes, ResponseSummary: json.RawMessage(`{}`), Status: toolsdomain.CallSucceeded, Version: 2, StartedAt: started, CompletedAt: &completed, DurationMillis: 1000}
		opID := f.id()
		op := f.operation(t, opID, e.NodeKey, agentdomain.WorkspaceAnalysisOperationCitationValidation, 1, e.NodeAttemptID, call.ID, receipt.ID, receipt.OutputHash, agentdomain.WorkspaceAnalysisOperationResultToolReceipt)
		f.appendJournal(t, agentapplication.WorkspaceAnalysisJournalEntry{Operation: op})
		f.validation.authority = toolsapplication.ValidateCitationV4PublicationAuthority{OperationID: opID, Receipt: receipt}
		return toolsapplication.ToolExecutionResult{Call: call, ResultReceiptID: receipt.ID, Output: receipt.Output, UntrustedData: true}, nil
	}
}

func (f *workspaceAnalysisV2ExecutorFixture) persistValidation(t *testing.T, candidate agentdomain.WorkspaceAnalysisCandidate, receipt toolsdomain.ResultReceipt) {
	t.Helper()
	e := f.execution(conversationworkflow.WorkspaceAnalysisNodeValidateCitations)
	opID := f.id()
	op := f.operation(t, opID, e.NodeKey, agentdomain.WorkspaceAnalysisOperationCitationValidation, 1, e.NodeAttemptID, receipt.ToolCallID, receipt.ID, receipt.OutputHash, agentdomain.WorkspaceAnalysisOperationResultToolReceipt)
	f.appendJournal(t, agentapplication.WorkspaceAnalysisJournalEntry{Operation: op})
	f.validation.authority = toolsapplication.ValidateCitationV4PublicationAuthority{OperationID: opID, Receipt: receipt}
	f.base.stages.outputs[e.NodeKey] = v2ExecutorJSON(t, workspaceAnalysisValidationOutputV2{SchemaVersion: 2, CandidateID: candidate.ID, CandidateHash: candidate.DocumentHash, OperationID: opID, ToolCallID: receipt.ToolCallID, ReceiptID: receipt.ID, ReceiptHash: receipt.OutputHash})
}

func (f *workspaceAnalysisV2ExecutorFixture) publication(status conversationdomain.AnswerPublicationStatus, model *foundation.ID) conversationworkflow.WorkspaceAnalysisPublicationOutput {
	output := workspaceAnalysisPublicationFixture(f.base.root.AnswerID, model, status, 50)
	output.SchemaVersion = 2
	if status == conversationdomain.WorkspaceAnalysisPublicationFailed {
		output.ResultType = conversationdomain.AnswerResultWorkspaceAnalysisTermination
	}
	return output
}

type workspaceAnalysisV2JournalFake struct {
	snapshot agentapplication.WorkspaceAnalysisJournalSnapshot
	calls    int
}

func (fake *workspaceAnalysisV2JournalFake) LoadWorkspaceAnalysisJournal(context.Context, agentapplication.WorkspaceAnalysisJournalQuery) (agentapplication.WorkspaceAnalysisJournalSnapshot, error) {
	fake.calls++
	return fake.snapshot, nil
}

type workspaceAnalysisV2LoopFake struct {
	request agentapplication.WorkspaceAnalysisLoopRequest
	result  agentapplication.WorkspaceAnalysisLoopResult
	calls   int
}

func (fake *workspaceAnalysisV2LoopFake) BindWorkspaceAnalysisLoop(agentapplication.WorkspaceAnalysisDecisionRunRequest) (agentapplication.WorkspaceAnalysisLoopModelCaller, error) {
	return fake, nil
}
func (*workspaceAnalysisV2LoopFake) CallWorkspaceAnalysisDecision(context.Context, agentapplication.WorkspaceAnalysisLoopModelCall) (agentapplication.WorkspaceAnalysisDecisionMutationResult, error) {
	return agentapplication.WorkspaceAnalysisDecisionMutationResult{}, errors.New("unexpected model call")
}
func (fake *workspaceAnalysisV2LoopFake) RunWorkspaceAnalysisLoop(_ context.Context, request agentapplication.WorkspaceAnalysisLoopRequest) (agentapplication.WorkspaceAnalysisLoopResult, error) {
	fake.request = request
	fake.calls++
	return fake.result, nil
}

type workspaceAnalysisV2OutputsFake struct {
	limit       int
	outputCalls int
}

func (fake *workspaceAnalysisV2OutputsFake) WorkspaceAnalysisDynamicSearchLimit(context.Context, toolsapplication.WorkspaceAnalysisDynamicToolQuery) (int, error) {
	return fake.limit, nil
}
func (fake *workspaceAnalysisV2OutputsFake) LoadWorkspaceAnalysisDynamicToolOutput(context.Context, toolsapplication.WorkspaceAnalysisDynamicToolQuery) (json.RawMessage, error) {
	fake.outputCalls++
	return json.RawMessage(`{"clean":true,"staged_count":0,"unstaged_count":0,"untracked_count":0,"conflict_count":0}`), nil
}

type workspaceAnalysisV2ToolsFake struct {
	commands []toolsapplication.ExecuteWorkspaceAnalysisToolCommand
	execute  func(toolsapplication.ExecuteWorkspaceAnalysisToolCommand) (toolsapplication.ToolExecutionResult, error)
}

func (fake *workspaceAnalysisV2ToolsFake) ExecuteWorkspaceAnalysisTool(_ context.Context, command toolsapplication.ExecuteWorkspaceAnalysisToolCommand) (toolsapplication.ToolExecutionResult, error) {
	fake.commands = append(fake.commands, command)
	if fake.execute == nil {
		return toolsapplication.ToolExecutionResult{}, errors.New("unexpected tool call")
	}
	return fake.execute(command)
}

type workspaceAnalysisV2ValidationFake struct {
	authority toolsapplication.ValidateCitationV4PublicationAuthority
}

func (fake *workspaceAnalysisV2ValidationFake) LoadValidateCitationV4PublicationAuthority(context.Context, toolsapplication.ValidateCitationV4PublicationAuthorityQuery) (toolsapplication.ValidateCitationV4PublicationAuthority, error) {
	return fake.authority, nil
}
