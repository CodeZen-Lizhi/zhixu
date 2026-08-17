package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestWorkspaceAnalysisValidateCitationsExecutorPersistsExactReceiptProjection(t *testing.T) {
	fixture := newWorkspaceAnalysisValidateCitationsFixture(t, []workspaceAnalysisValidationResultFixture{
		{EvidenceRef: "E1", Valid: true, ReasonCode: conversationworkflow.WorkspaceAnalysisCitationValidationReasonOK},
	})
	result, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fixture.stages.calls != 1 || fixture.authority.authorityCalls != 1 || fixture.authority.receiptCalls != 1 ||
		fixture.tools.calls != 1 {
		t.Fatalf("calls stages=%d authority=%d receipts=%d tools=%d", fixture.stages.calls,
			fixture.authority.authorityCalls, fixture.authority.receiptCalls, fixture.tools.calls)
	}
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV1Deadlines(fixture.run.Timeouts)
	if err != nil {
		t.Fatal(err)
	}
	if !fixture.tools.hasDeadline || !fixture.tools.deadline.Equal(fixture.clock.Add(deadlines.ValidateCitationsDeadline())) {
		t.Fatalf("tool deadline=%s present=%t", fixture.tools.deadline, fixture.tools.hasDeadline)
	}
	command := fixture.tools.command
	if command.OperationKey != (agentdomain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: fixture.run.ID,
		NodeKey:       agentdomain.WorkspaceAnalysisOperationNodeValidateCitations,
		Kind:          agentdomain.WorkspaceAnalysisOperationCitationValidation,
		Ordinal:       1,
	}) || command.Tool.CallNo != 1 || command.Tool.Invocation != toolsdomain.InvocationSourceTrustedWorkflow ||
		command.Tool.Identity.NodeAttemptID != fixture.execution.NodeAttemptID ||
		command.Tool.Identity.LeaseFence != int64(fixture.execution.AttemptNo) ||
		command.Tool.Request.ToolName != workspaceAnalysisValidateCitationRef.Name ||
		command.Tool.Request.Reason != workspaceAnalysisValidateCitationsReason {
		t.Fatalf("command=%+v", command)
	}
	var arguments struct {
		CandidateHash string        `json:"candidate_hash"`
		CandidateID   foundation.ID `json:"candidate_id"`
		EvidenceRefs  []string      `json:"evidence_refs"`
	}
	if err := json.Unmarshal(command.Tool.Request.Arguments, &arguments); err != nil ||
		arguments.CandidateID != fixture.predecessor.CandidateID ||
		arguments.CandidateHash != fixture.predecessor.CandidateHash ||
		!reflect.DeepEqual(arguments.EvidenceRefs, fixture.authority.authority.EvidenceRefs) {
		t.Fatalf("arguments=%+v err=%v", arguments, err)
	}

	decoded, err := conversationworkflow.DecodeWorkspaceAnalysisValidationOutput(result.Output)
	if err != nil {
		t.Fatalf("DecodeWorkspaceAnalysisValidationOutput: %v", err)
	}
	want := conversationworkflow.WorkspaceAnalysisValidationOutput{
		SchemaVersion: conversationworkflow.WorkspaceAnalysisOutputSchemaVersion,
		CandidateID:   fixture.predecessor.CandidateID, CandidateHash: fixture.predecessor.CandidateHash,
		ValidationToolCallID: fixture.toolResult.Call.ID, ValidationToolReceiptID: fixture.receipt.ID,
		ValidationToolReceiptHash: fixture.receipt.OutputHash,
		Results: []conversationworkflow.WorkspaceAnalysisCitationValidationResult{
			{EvidenceRef: "E1", Valid: true, ReasonCode: conversationworkflow.WorkspaceAnalysisCitationValidationReasonOK},
		},
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("output=%#v want=%#v", decoded, want)
	}
	for _, forbidden := range []string{
		"excerpt", "content_hash", "citation_id", "index_version_id", "chunk_id",
		"source_version_id", "source_span_id", "private_binding", "SOURCE_BODY_CANARY",
	} {
		if strings.Contains(string(result.Output), forbidden) {
			t.Fatalf("node output leaked %q: %s", forbidden, result.Output)
		}
	}
}

func TestWorkspaceAnalysisValidateCitationsExecutorKeepsInvalidResultAsDurableNodeSuccess(t *testing.T) {
	fixture := newWorkspaceAnalysisValidateCitationsFixture(t, []workspaceAnalysisValidationResultFixture{
		{EvidenceRef: "E1", Valid: false, ReasonCode: conversationworkflow.WorkspaceAnalysisCitationValidationReasonUnresolvable},
	})
	result, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	decoded, err := conversationworkflow.DecodeWorkspaceAnalysisValidationOutput(result.Output)
	if err != nil || len(decoded.Results) != 1 || decoded.Results[0].Valid ||
		decoded.Results[0].ReasonCode != conversationworkflow.WorkspaceAnalysisCitationValidationReasonUnresolvable {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	if fixture.tools.calls != 1 || fixture.authority.receiptCalls != 1 {
		t.Fatalf("tools=%d receipts=%d", fixture.tools.calls, fixture.authority.receiptCalls)
	}
}

func TestWorkspaceAnalysisValidateCitationsExecutorReplacementReusesStableReceipt(t *testing.T) {
	fixture := newWorkspaceAnalysisValidateCitationsFixture(t, []workspaceAnalysisValidationResultFixture{
		{EvidenceRef: "E1", Valid: true, ReasonCode: conversationworkflow.WorkspaceAnalysisCitationValidationReasonOK},
	})
	executor := fixture.executor(t)
	first, err := executor.Execute(context.Background(), fixture.execution)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	replacement := fixture.execution
	replacement.NodeAttemptID = workspaceAnalysisValidationTestID(90)
	replacement.AttemptNo = 2
	replacement.DispatchNo = 2
	replacement.NodeVersion++
	replacement.LeaseOwner = "worker-validation-replacement"
	fixture.toolResult.Replayed = true
	fixture.tools.result = fixture.toolResult

	second, err := executor.Execute(context.Background(), replacement)
	if err != nil {
		t.Fatalf("replacement Execute: %v", err)
	}
	if !bytes.Equal(first.Output, second.Output) || fixture.tools.calls != 2 || fixture.authority.receiptCalls != 2 ||
		fixture.tools.command.Tool.Identity.NodeAttemptID != replacement.NodeAttemptID ||
		fixture.tools.command.Tool.Identity.LeaseFence != int64(replacement.AttemptNo) {
		t.Fatalf("first=%s second=%s tools=%d receipts=%d command=%+v", first.Output, second.Output,
			fixture.tools.calls, fixture.authority.receiptCalls, fixture.tools.command)
	}
}

func TestWorkspaceAnalysisValidateCitationsExecutorRejectsAuthorityAndReceiptDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*workspaceAnalysisValidateCitationsFixture)
		code   string
		tools  int
	}{
		{
			name: "candidate hash",
			mutate: func(fixture *workspaceAnalysisValidateCitationsFixture) {
				fixture.authority.authority.CandidateHash = strings.Repeat("f", 64)
			},
			code: string(conversationdomain.WorkspaceAnalysisReceiptInvalid), tools: 0,
		},
		{
			name: "source receipt cross workspace",
			mutate: func(fixture *workspaceAnalysisValidateCitationsFixture) {
				fixture.authority.authority.ReadSourceReceipts[0].WorkspaceID = workspaceAnalysisValidationTestID(91)
			},
			code: string(conversationdomain.WorkspaceAnalysisReceiptInvalid), tools: 0,
		},
		{
			name: "result receipt id",
			mutate: func(fixture *workspaceAnalysisValidateCitationsFixture) {
				fixture.authority.receipt.ID = workspaceAnalysisValidationTestID(92)
			},
			code: ErrorCodeOutputInvalid, tools: 1,
		},
		{
			name: "result output",
			mutate: func(fixture *workspaceAnalysisValidateCitationsFixture) {
				fixture.authority.receipt.Output = json.RawMessage(`{"results":[{"evidence_ref":"E1","valid":false,"reason_code":"BINDING_MISMATCH"}]}`)
			},
			code: ErrorCodeOutputInvalid, tools: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorkspaceAnalysisValidateCitationsFixture(t, []workspaceAnalysisValidationResultFixture{
				{EvidenceRef: "E1", Valid: true, ReasonCode: conversationworkflow.WorkspaceAnalysisCitationValidationReasonOK},
			})
			test.mutate(fixture)
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			if codeOf(err) != test.code || fixture.tools.calls != test.tools {
				t.Fatalf("err=%v code=%q tools=%d", err, codeOf(err), fixture.tools.calls)
			}
		})
	}
}

type workspaceAnalysisValidateCitationsFixture struct {
	execution   workflowapplication.ExecutionContext
	predecessor conversationworkflow.WorkspaceAnalysisSynthesisOutput
	run         agentdomain.WorkspaceAnalysisRun
	inputs      *workspaceAnalysisRetrieveInputFake
	context     *workspaceAnalysisRetrieveContextFake
	runs        *workspaceAnalysisRetrieveRunFake
	stages      *workspaceAnalysisSynthesisStageFake
	authority   *workspaceAnalysisValidationAuthorityFake
	tools       *workspaceAnalysisValidationToolFake
	finalizer   *workspaceAnalysisFinalizerFake
	toolResult  toolsapplication.ToolExecutionResult
	receipt     toolsdomain.ResultReceipt
	clock       time.Time
}

type workspaceAnalysisValidationResultFixture struct {
	EvidenceRef string
	Valid       bool
	ReasonCode  string
}

func newWorkspaceAnalysisValidateCitationsFixture(
	t *testing.T,
	validationResults []workspaceAnalysisValidationResultFixture,
) *workspaceAnalysisValidateCitationsFixture {
	t.Helper()
	synthesisFixture := newWorkspaceAnalysisSynthesizeFixture(t, 3)
	synthesisExecutionResult, err := synthesisFixture.executor(t).Execute(context.Background(), synthesisFixture.execution)
	if err != nil {
		t.Fatalf("synthesis fixture Execute: %v", err)
	}
	predecessor, err := conversationworkflow.DecodeWorkspaceAnalysisSynthesisOutput(synthesisExecutionResult.Output)
	if err != nil {
		t.Fatal(err)
	}
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	node, found := workspaceAnalysisNode(definition, conversationworkflow.WorkspaceAnalysisNodeValidateCitations)
	if !found {
		t.Fatal("validate_citations node missing")
	}
	execution := synthesisFixture.execution
	execution.NodeKey = node.Key
	execution.NodeKind = node.Kind
	execution.NodeRunID = workspaceAnalysisValidationTestID(1)
	execution.NodeAttemptID = workspaceAnalysisValidationTestID(2)
	execution.NodeVersion = 2
	execution.AttemptNo = 1
	execution.DispatchNo = 1
	execution.RetryNo = 0
	execution.LeaseOwner = "worker-validation"
	execution.Input = append(json.RawMessage(nil), synthesisExecutionResult.Output...)

	evidenceRefs := make([]string, len(validationResults))
	readReceipts := make([]toolsdomain.ResultReceipt, len(validationResults))
	for index, result := range validationResults {
		evidenceRefs[index] = result.EvidenceRef
		readReceipts[index] = synthesisFixture.authority.result.ReadSourceReceipts[validationReferenceOrdinal(t, result.EvidenceRef)-1]
	}
	authority := toolsapplication.ValidateCitationV3Authority{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		AnalysisRunID: synthesisFixture.run.ID, CandidateID: predecessor.CandidateID,
		CandidateHash: predecessor.CandidateHash, EvidenceRefs: evidenceRefs,
		SearchReceipt: synthesisFixture.authority.result.SearchReceipt, ReadSourceReceipts: readReceipts,
	}
	if err := validateWorkspaceAnalysisCitationAuthority(execution, synthesisFixture.run, predecessor, authority); err != nil {
		t.Fatalf("validation authority fixture: %v", err)
	}
	receipt := workspaceAnalysisValidationReceiptFixture(t, execution, predecessor, authority, validationResults)
	if _, err := toolsdomain.ValidateCitationV3ReceiptResults(receipt, predecessor.CandidateID, predecessor.CandidateHash, evidenceRefs); err != nil {
		t.Fatalf("validation receipt fixture: %v", err)
	}
	toolResult := workspaceAnalysisValidationToolResult(t, execution, predecessor, authority.EvidenceRefs, receipt)
	authorityFake := &workspaceAnalysisValidationAuthorityFake{authority: authority, receipt: receipt}
	return &workspaceAnalysisValidateCitationsFixture{
		execution: execution, predecessor: predecessor, run: synthesisFixture.run,
		inputs: synthesisFixture.inputs, context: synthesisFixture.context, runs: synthesisFixture.runs,
		stages: &workspaceAnalysisSynthesisStageFake{outputs: map[string]json.RawMessage{
			conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer: append(json.RawMessage(nil), synthesisExecutionResult.Output...),
		}},
		authority: authorityFake, tools: &workspaceAnalysisValidationToolFake{result: toolResult},
		finalizer:  synthesisFixture.finalizer,
		toolResult: toolResult, receipt: receipt, clock: synthesisFixture.clock,
	}
}

func (fixture *workspaceAnalysisValidateCitationsFixture) executor(t *testing.T) *WorkspaceAnalysisValidateCitationsExecutor {
	t.Helper()
	executor, err := NewWorkspaceAnalysisValidateCitationsExecutor(WorkspaceAnalysisValidateCitationsExecutorDependencies{
		Context: fixture.context, Runs: fixture.runs, Inputs: fixture.inputs, Stages: fixture.stages,
		Authority: fixture.authority, Receipts: fixture.authority, Tools: fixture.tools,
		Finalizer: fixture.finalizer, Clock: foundation.FixedClock{Value: fixture.clock},
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func workspaceAnalysisValidationReceiptFixture(
	t *testing.T,
	execution workflowapplication.ExecutionContext,
	predecessor conversationworkflow.WorkspaceAnalysisSynthesisOutput,
	authority toolsapplication.ValidateCitationV3Authority,
	results []workspaceAnalysisValidationResultFixture,
) toolsdomain.ResultReceipt {
	t.Helper()
	outputResults := make([]map[string]any, len(results))
	privateResults := make([]map[string]any, len(results))
	for index, result := range results {
		outputResults[index] = map[string]any{
			"evidence_ref": result.EvidenceRef, "reason_code": result.ReasonCode, "valid": result.Valid,
		}
		var private struct {
			ChunkID           foundation.ID `json:"chunk_id"`
			CitationID        string        `json:"citation_id"`
			ContentHash       string        `json:"content_hash"`
			EvidenceRef       string        `json:"evidence_ref"`
			IndexVersionID    foundation.ID `json:"index_version_id"`
			SearchReceiptHash string        `json:"search_receipt_hash"`
			SearchReceiptID   foundation.ID `json:"search_receipt_id"`
			SourceSpanID      foundation.ID `json:"source_span_id"`
			SourceVersionID   foundation.ID `json:"source_version_id"`
		}
		if authority.ReadSourceReceipts[index].PrivateBinding == nil ||
			json.Unmarshal(authority.ReadSourceReceipts[index].PrivateBinding.Document, &private) != nil {
			t.Fatal("read receipt fixture private binding is invalid")
		}
		privateResults[index] = map[string]any{
			"evidence_ref": private.EvidenceRef, "citation_id": private.CitationID,
			"index_version_id": private.IndexVersionID, "chunk_id": private.ChunkID,
			"source_version_id": private.SourceVersionID, "source_span_id": private.SourceSpanID,
			"content_hash": private.ContentHash,
		}
	}
	output, err := json.Marshal(map[string]any{"results": outputResults})
	if err != nil {
		t.Fatal(err)
	}
	private, err := json.Marshal(map[string]any{
		"candidate_id": predecessor.CandidateID, "candidate_hash": predecessor.CandidateHash, "results": privateResults,
	})
	if err != nil {
		t.Fatal(err)
	}
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(workspaceAnalysisValidateCitationRef)
	if !found {
		t.Fatal("validate citation receipt contract missing")
	}
	outputDigest := sha256.Sum256(output)
	privateDigest := sha256.Sum256(private)
	completedAt := workspaceAnalysisRetrieveNow().Add(9 * time.Second)
	return toolsdomain.ResultReceipt{
		ID: workspaceAnalysisValidationTestID(11), ToolCallID: workspaceAnalysisValidationTestID(10),
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
		Tool: workspaceAnalysisValidateCitationRef, OutputSchema: contract.OutputSchema,
		DefinitionHash: strings.Repeat("8", 64), PersistencePolicy: toolsdomain.ResultPersistenceCanonical,
		MaxOutputBytes: contract.MaxOutputBytes, MaxPrivateBindingBytes: contract.MaxPrivateBindingBytes,
		Output: output, OutputHash: hex.EncodeToString(outputDigest[:]), OutputBytes: int64(len(output)),
		PrivateBinding: &toolsdomain.ResultReceiptPrivateBinding{
			Schema: contract.PrivateBindingSchema, Document: private,
			Hash: hex.EncodeToString(privateDigest[:]), Bytes: int64(len(private)),
		},
		CreatedAt: completedAt,
	}
}

func workspaceAnalysisValidationToolResult(
	t *testing.T,
	execution workflowapplication.ExecutionContext,
	predecessor conversationworkflow.WorkspaceAnalysisSynthesisOutput,
	evidenceRefs []string,
	receipt toolsdomain.ResultReceipt,
) toolsapplication.ToolExecutionResult {
	t.Helper()
	arguments, err := json.Marshal(struct {
		CandidateID   foundation.ID `json:"candidate_id"`
		CandidateHash string        `json:"candidate_hash"`
		EvidenceRefs  []string      `json:"evidence_refs"`
	}{CandidateID: predecessor.CandidateID, CandidateHash: predecessor.CandidateHash, EvidenceRefs: evidenceRefs})
	if err != nil {
		t.Fatal(err)
	}
	requestDocument, err := workspaceAnalysisValidateCitationRequestDocument(arguments)
	if err != nil {
		t.Fatal(err)
	}
	requestDigest := sha256.Sum256(requestDocument)
	completedAt := receipt.CreatedAt
	tool := workspaceAnalysisValidateCitationRef
	inputSchema := workspaceAnalysisValidateCitationInputSchema
	outputSchema := workspaceAnalysisValidateCitationOutputSchema
	return toolsapplication.ToolExecutionResult{
		Call: toolsdomain.ToolCall{
			ID: receipt.ToolCallID, WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
			NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
			CallNo: 1, RequestedToolName: tool.Name, Tool: &tool, DefinitionHash: receipt.DefinitionHash,
			InputSchema: &inputSchema, OutputSchema: &outputSchema, Capability: capability.ReadLocal,
			SideEffectLevel: toolsdomain.SideEffectNone, InvocationPolicy: toolsdomain.InvocationTrustedWorkflowOnly,
			RequestHash: hex.EncodeToString(requestDigest[:]), RequestBytes: int64(len(requestDocument)),
			RequestSummary: json.RawMessage(`{}`), ResponseHash: receipt.OutputHash, ResponseBytes: receipt.OutputBytes,
			ResponseSummary: json.RawMessage(`{}`), Status: toolsdomain.CallSucceeded, Version: 2,
			StartedAt: completedAt.Add(-time.Second), CompletedAt: &completedAt, DurationMillis: 1000,
		},
		ResultReceiptID: receipt.ID, Output: append(json.RawMessage(nil), receipt.Output...),
		UntrustedData: true,
	}
}

func validationReferenceOrdinal(t *testing.T, reference string) int {
	t.Helper()
	if len(reference) != 2 || reference[0] != 'E' || reference[1] < '1' || reference[1] > '3' {
		t.Fatalf("invalid validation reference %q", reference)
	}
	return int(reference[1] - '0')
}

type workspaceAnalysisValidationAuthorityFake struct {
	authority      toolsapplication.ValidateCitationV3Authority
	receipt        toolsdomain.ResultReceipt
	authorityQuery toolsapplication.ValidateCitationV3AuthorityQuery
	receiptQuery   toolsapplication.ValidateCitationV3ReceiptQuery
	authorityErr   error
	receiptErr     error
	authorityCalls int
	receiptCalls   int
}

func (fake *workspaceAnalysisValidationAuthorityFake) LoadValidateCitationV3Authority(
	_ context.Context,
	query toolsapplication.ValidateCitationV3AuthorityQuery,
) (toolsapplication.ValidateCitationV3Authority, error) {
	fake.authorityCalls++
	fake.authorityQuery = query
	if fake.authorityErr != nil {
		return toolsapplication.ValidateCitationV3Authority{}, fake.authorityErr
	}
	return fake.authority, nil
}

func (fake *workspaceAnalysisValidationAuthorityFake) LoadValidateCitationV3Receipt(
	_ context.Context,
	query toolsapplication.ValidateCitationV3ReceiptQuery,
) (toolsdomain.ResultReceipt, error) {
	fake.receiptCalls++
	fake.receiptQuery = query
	if fake.receiptErr != nil {
		return toolsdomain.ResultReceipt{}, fake.receiptErr
	}
	return fake.receipt, nil
}

type workspaceAnalysisValidationToolFake struct {
	result      toolsapplication.ToolExecutionResult
	command     toolsapplication.ExecuteWorkspaceAnalysisToolCommand
	err         error
	deadline    time.Time
	hasDeadline bool
	calls       int
}

func (fake *workspaceAnalysisValidationToolFake) ExecuteWorkspaceAnalysisTool(
	ctx context.Context,
	command toolsapplication.ExecuteWorkspaceAnalysisToolCommand,
) (toolsapplication.ToolExecutionResult, error) {
	fake.calls++
	fake.command = command
	fake.deadline, fake.hasDeadline = ctx.Deadline()
	if fake.err != nil {
		return toolsapplication.ToolExecutionResult{}, fake.err
	}
	return fake.result, nil
}

func workspaceAnalysisValidationTestID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("98000000-0000-4000-8000-%012d", value))
}

var (
	_ toolsapplication.ValidateCitationV3AuthorityReader = (*workspaceAnalysisValidationAuthorityFake)(nil)
	_ toolsapplication.ValidateCitationV3ReceiptReader   = (*workspaceAnalysisValidationAuthorityFake)(nil)
	_ workspaceAnalysisToolExecutor                      = (*workspaceAnalysisValidationToolFake)(nil)
)
