package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func TestWorkspaceAnalysisReviewPublishExecutorPublishesExactSucceededFacts(t *testing.T) {
	fixture := newWorkspaceAnalysisReviewPublishFixture(t, true, true)

	result, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	decoded, err := conversationworkflow.DecodeWorkspaceAnalysisPublicationOutput(result.Output)
	if err != nil || !reflect.DeepEqual(decoded, fixture.finalizer.successOutput) {
		t.Fatalf("output=%#v err=%v", decoded, err)
	}
	if fixture.review.calls != 1 || fixture.finalizer.lookupCalls != 1 || fixture.finalizer.successCalls != 1 ||
		fixture.finalizer.terminationCalls != 0 {
		t.Fatalf("calls review=%d lookup=%d success=%d termination=%d", fixture.review.calls,
			fixture.finalizer.lookupCalls, fixture.finalizer.successCalls, fixture.finalizer.terminationCalls)
	}
	want := conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand{
		WorkspaceAnalysisPublicationLookup: workspaceAnalysisPublicationLookup(
			fixture.execution, fixture.root, fixture.run.ID,
		),
		ExpectedAnswerVersion: fixture.question.Answer.Version,
		CandidateID:           fixture.candidate.ID,
		CandidateHash:         fixture.candidate.DocumentHash,
		GitReceiptID:          fixture.inspect.ToolReceiptID,
		GitReceiptHash:        fixture.inspect.ToolReceiptHash,
		ValidationReceiptID:   fixture.validation.authority.Receipt.ID,
		ValidationReceiptHash: fixture.validation.authority.Receipt.OutputHash,
		ReviewModelResultID:   fixture.review.result.ModelResult.ID,
		ReviewModelResultHash: fixture.review.result.ModelResult.DocumentHash,
	}
	if !reflect.DeepEqual(fixture.finalizer.successCommand, want) {
		t.Fatalf("success command=%#v want=%#v", fixture.finalizer.successCommand, want)
	}
	if fixture.review.request.Candidate.ID != fixture.candidate.ID ||
		!reflect.DeepEqual(fixture.review.request.Evidence, []agentapplication.WorkspaceAnalysisReviewEvidence{
			{EvidenceRef: "E1", Excerpt: "OPENED_EVIDENCE_1"},
		}) || fixture.review.request.Identity.NodeAttemptID != fixture.execution.NodeAttemptID {
		t.Fatalf("review request=%+v", fixture.review.request)
	}
}

func TestWorkspaceAnalysisReviewPublishExecutorRejectsInvalidCitationWithoutReview(t *testing.T) {
	fixture := newWorkspaceAnalysisReviewPublishFixture(t, false, true)

	result, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	decoded, err := conversationworkflow.DecodeWorkspaceAnalysisPublicationOutput(result.Output)
	if err != nil || !reflect.DeepEqual(decoded, fixture.finalizer.terminationOutput) {
		t.Fatalf("output=%#v err=%v", decoded, err)
	}
	if fixture.review.calls != 0 || fixture.finalizer.successCalls != 0 || fixture.finalizer.terminationCalls != 1 {
		t.Fatalf("calls review=%d success=%d termination=%d", fixture.review.calls,
			fixture.finalizer.successCalls, fixture.finalizer.terminationCalls)
	}
	command := fixture.finalizer.terminationCommand
	if command.Reason != agentdomain.WorkspaceAnalysisRunCitationInvalid || command.OperationID == nil ||
		*command.OperationID != fixture.validation.authority.OperationID || command.Artifact == nil ||
		command.Artifact.Kind != conversationapplication.WorkspaceAnalysisTerminationArtifactToolReceipt ||
		command.Artifact.ID != fixture.validation.authority.Receipt.ID ||
		command.Artifact.Hash != fixture.validation.authority.Receipt.OutputHash {
		t.Fatalf("termination command=%#v", command)
	}
}

func TestWorkspaceAnalysisReviewPublishExecutorPublishesFaithfulnessRejection(t *testing.T) {
	fixture := newWorkspaceAnalysisReviewPublishFixture(t, true, false)

	result, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	decoded, err := conversationworkflow.DecodeWorkspaceAnalysisPublicationOutput(result.Output)
	if err != nil || !reflect.DeepEqual(decoded, fixture.finalizer.terminationOutput) {
		t.Fatalf("output=%#v err=%v", decoded, err)
	}
	command := fixture.finalizer.terminationCommand
	if fixture.review.calls != 1 || fixture.finalizer.successCalls != 0 || fixture.finalizer.terminationCalls != 1 ||
		command.Reason != agentdomain.WorkspaceAnalysisRunFaithfulnessRejected || command.OperationID == nil ||
		*command.OperationID != fixture.review.result.ModelResult.OperationID || command.Artifact == nil ||
		command.Artifact.Kind != conversationapplication.WorkspaceAnalysisTerminationArtifactModelResult ||
		command.Artifact.ID != fixture.review.result.ModelResult.ID ||
		command.Artifact.Hash != fixture.review.result.ModelResult.DocumentHash {
		t.Fatalf("review=%d success=%d termination=%d command=%#v", fixture.review.calls,
			fixture.finalizer.successCalls, fixture.finalizer.terminationCalls, command)
	}
}

func TestWorkspaceAnalysisReviewPublishExecutorReplaysPublicationBeforeMutableReads(t *testing.T) {
	fixture := newWorkspaceAnalysisReviewPublishFixture(t, true, true)
	fixture.finalizer.lookupFound = true

	result, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	decoded, err := conversationworkflow.DecodeWorkspaceAnalysisPublicationOutput(result.Output)
	if err != nil || !reflect.DeepEqual(decoded, fixture.finalizer.lookupOutput) {
		t.Fatalf("output=%#v err=%v", decoded, err)
	}
	if fixture.context.calls != 0 || fixture.candidates.calls != 0 || fixture.evidence.calls != 0 ||
		fixture.validation.calls != 0 || fixture.review.calls != 0 || fixture.finalizer.successCalls != 0 ||
		fixture.finalizer.terminationCalls != 0 {
		t.Fatalf("mutable calls context=%d candidate=%d evidence=%d validation=%d review=%d success=%d termination=%d",
			fixture.context.calls, fixture.candidates.calls, fixture.evidence.calls, fixture.validation.calls,
			fixture.review.calls, fixture.finalizer.successCalls, fixture.finalizer.terminationCalls)
	}
}

type workspaceAnalysisReviewPublishFixture struct {
	execution  workflowapplication.ExecutionContext
	root       conversationworkflow.WorkspaceAnalysisInput
	question   conversationapplication.QuestionExecutionContext
	run        agentdomain.WorkspaceAnalysisRun
	inspect    conversationworkflow.WorkspaceAnalysisInspectOutput
	candidate  agentdomain.WorkspaceAnalysisCandidate
	inputs     *workspaceAnalysisRetrieveInputFake
	context    *workspaceAnalysisRetrieveContextFake
	runs       *workspaceAnalysisRetrieveRunFake
	stages     *workspaceAnalysisSynthesisStageFake
	candidates *workspaceAnalysisReviewCandidateFake
	evidence   *workspaceAnalysisSynthesisAuthorityFake
	validation *workspaceAnalysisReviewValidationFake
	review     *workspaceAnalysisReviewRunnerFake
	finalizer  *workspaceAnalysisFinalizerFake
	clock      time.Time
}

func newWorkspaceAnalysisReviewPublishFixture(t *testing.T, citationsValid, reviewPassed bool) *workspaceAnalysisReviewPublishFixture {
	t.Helper()
	synthesis := newWorkspaceAnalysisSynthesizeFixture(t, 3)
	synthesisExecution, err := synthesis.executor(t).Execute(context.Background(), synthesis.execution)
	if err != nil {
		t.Fatalf("synthesis fixture Execute: %v", err)
	}
	synthesisOutput, err := conversationworkflow.DecodeWorkspaceAnalysisSynthesisOutput(synthesisExecution.Output)
	if err != nil {
		t.Fatal(err)
	}
	candidate := synthesis.synthesis.result.Candidate

	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	node, found := workspaceAnalysisNode(definition, conversationworkflow.WorkspaceAnalysisNodeReviewPublish)
	if !found {
		t.Fatal("review_publish node missing")
	}
	execution := synthesis.execution
	execution.NodeKey, execution.NodeKind = node.Key, node.Kind
	execution.NodeRunID, execution.NodeAttemptID = workspaceAnalysisReviewPublishTestID(1), workspaceAnalysisReviewPublishTestID(2)
	execution.NodeVersion, execution.AttemptNo, execution.DispatchNo, execution.RetryNo = 2, 1, 1, 0
	execution.LeaseOwner = "worker-review-publish"

	validationExecution := execution
	validationExecution.NodeRunID, validationExecution.NodeAttemptID = workspaceAnalysisValidationTestID(1), workspaceAnalysisValidationTestID(2)
	reason := conversationworkflow.WorkspaceAnalysisCitationValidationReasonOK
	if !citationsValid {
		reason = conversationworkflow.WorkspaceAnalysisCitationValidationReasonUnresolvable
	}
	validationFixtures := []workspaceAnalysisValidationResultFixture{{EvidenceRef: "E1", Valid: citationsValid, ReasonCode: reason}}
	validationSource := toolsapplication.ValidateCitationV3Authority{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, AnalysisRunID: synthesis.run.ID,
		CandidateID: candidate.ID, CandidateHash: candidate.DocumentHash, EvidenceRefs: []string{"E1"},
		SearchReceipt:      synthesis.authority.result.SearchReceipt,
		ReadSourceReceipts: []toolsdomain.ResultReceipt{synthesis.authority.result.ReadSourceReceipts[0]},
	}
	receipt := workspaceAnalysisValidationReceiptFixture(t, validationExecution, synthesisOutput, validationSource, validationFixtures)
	results, err := toolsdomain.ValidateCitationV3ReceiptResults(receipt, candidate.ID, candidate.DocumentHash, []string{"E1"})
	if err != nil {
		t.Fatal(err)
	}
	validationOutput := conversationworkflow.WorkspaceAnalysisValidationOutput{
		SchemaVersion: conversationworkflow.WorkspaceAnalysisOutputSchemaVersion,
		CandidateID:   candidate.ID, CandidateHash: candidate.DocumentHash,
		ValidationToolCallID: receipt.ToolCallID, ValidationToolReceiptID: receipt.ID,
		ValidationToolReceiptHash: receipt.OutputHash,
		Results: []conversationworkflow.WorkspaceAnalysisCitationValidationResult{{
			EvidenceRef: results[0].EvidenceRef, Valid: results[0].Valid, ReasonCode: results[0].ReasonCode,
		}},
	}
	validationRaw, err := conversationworkflow.EncodeWorkspaceAnalysisValidationOutput(validationOutput)
	if err != nil {
		t.Fatal(err)
	}
	execution.Input = validationRaw

	stageOutputs := make(map[string]json.RawMessage, len(synthesis.stages.outputs)+2)
	for key, raw := range synthesis.stages.outputs {
		stageOutputs[key] = append(json.RawMessage(nil), raw...)
	}
	stageOutputs[conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer] = append(json.RawMessage(nil), synthesisExecution.Output...)
	stageOutputs[conversationworkflow.WorkspaceAnalysisNodeValidateCitations] = append(json.RawMessage(nil), validationRaw...)

	validationAuthority := toolsapplication.ValidateCitationV3PublicationAuthority{
		OperationID: workspaceAnalysisReviewPublishTestID(10), Receipt: receipt,
	}
	reviewResult := workspaceAnalysisReviewResultFixture(t, execution, synthesis.run, candidate, reviewPassed, synthesis.clock)
	successModelRunID := candidate.SynthesisModelRunID
	finalizer := &workspaceAnalysisFinalizerFake{
		lookupOutput:      workspaceAnalysisPublicationFixture(candidate.AnswerID, &successModelRunID, conversationdomain.AnswerPublicationCompleted, 20),
		successOutput:     workspaceAnalysisPublicationFixture(candidate.AnswerID, &successModelRunID, conversationdomain.AnswerPublicationCompleted, 21),
		terminationOutput: workspaceAnalysisPublicationFixture(candidate.AnswerID, nil, conversationdomain.AnswerPublicationRefused, 22),
	}
	synthesis.context.calls = 0
	synthesis.authority.calls = 0
	return &workspaceAnalysisReviewPublishFixture{
		execution: execution, root: synthesis.root, question: synthesis.question, run: synthesis.run,
		inspect: synthesis.inspect, candidate: candidate,
		inputs: synthesis.inputs, context: synthesis.context, runs: synthesis.runs,
		stages:     &workspaceAnalysisSynthesisStageFake{outputs: stageOutputs},
		candidates: &workspaceAnalysisReviewCandidateFake{candidate: candidate}, evidence: synthesis.authority,
		validation: &workspaceAnalysisReviewValidationFake{authority: validationAuthority},
		review:     &workspaceAnalysisReviewRunnerFake{result: reviewResult}, finalizer: finalizer, clock: synthesis.clock,
	}
}

func (fixture *workspaceAnalysisReviewPublishFixture) executor(t *testing.T) *WorkspaceAnalysisReviewPublishExecutor {
	t.Helper()
	executor, err := NewWorkspaceAnalysisReviewPublishExecutor(WorkspaceAnalysisReviewPublishExecutorDependencies{
		Context: fixture.context, Runs: fixture.runs, Inputs: fixture.inputs, Stages: fixture.stages,
		Candidates: fixture.candidates, Evidence: fixture.evidence, Validation: fixture.validation,
		Review: fixture.review, Finalizer: fixture.finalizer, Clock: foundation.FixedClock{Value: fixture.clock},
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func workspaceAnalysisReviewResultFixture(
	t *testing.T,
	execution workflowapplication.ExecutionContext,
	run agentdomain.WorkspaceAnalysisRun,
	candidate agentdomain.WorkspaceAnalysisCandidate,
	passed bool,
	now time.Time,
) agentapplication.WorkspaceAnalysisReviewResult {
	t.Helper()
	modelRunID := workspaceAnalysisReviewPublishTestID(30)
	verdict := agentdomain.FaithfulnessSupported
	if !passed {
		verdict = agentdomain.FaithfulnessUnsupported
	}
	review := agentdomain.FaithfulnessReviewResult{
		ResultType: agentdomain.ResultTypeFaithfulnessReview, SchemaID: agentdomain.FaithfulnessReviewSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: modelRunID,
		Payload: agentdomain.FaithfulnessReviewPayload{
			Passed: passed,
			Items: []agentdomain.FaithfulnessReviewItem{{
				AssertionID: "@answer/conclusion", Verdict: verdict,
				CitationIDs: []string{"E1"}, Reason: "checked against E1",
			}},
			Summary: "bounded faithfulness decision",
		},
	}
	document, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(document)
	hash := hex.EncodeToString(digest[:])
	completedAt := now.Add(20 * time.Second)
	modelCallID, operationID := workspaceAnalysisReviewPublishTestID(31), workspaceAnalysisReviewPublishTestID(32)
	candidateID := candidate.ID
	modelResult := agentdomain.WorkspaceAnalysisModelResult{
		ID: workspaceAnalysisReviewPublishTestID(33), WorkspaceID: execution.WorkspaceID,
		AnalysisRunID: run.ID, OperationID: operationID, NodeAttemptID: execution.NodeAttemptID,
		ModelRunID: modelRunID, ModelCallID: modelCallID,
		OperationKind:      agentdomain.WorkspaceAnalysisOperationFaithfulnessReview,
		Schema:             agentdomain.SchemaRef{ID: agentdomain.FaithfulnessReviewSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		SubjectCandidateID: &candidateID, SubjectCandidateHash: candidate.DocumentHash,
		Document: document, DocumentHash: hash, DocumentBytes: int64(len(document)), CreatedAt: completedAt,
	}
	if err := agentdomain.ValidateWorkspaceAnalysisModelResult(modelResult); err != nil {
		t.Fatalf("review model result: %v", err)
	}
	return agentapplication.WorkspaceAnalysisReviewResult{
		Review: review, ModelResult: modelResult,
		Run: agentdomain.ModelRun{
			ID: modelRunID, WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
			NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
			Status: agentdomain.ModelRunSucceeded,
		},
		Call:   agentdomain.ModelCall{ID: modelCallID, Status: agentdomain.ModelCallSucceeded, ResponseHash: hash},
		Passed: passed,
	}
}

func workspaceAnalysisPublicationFixture(
	answerID foundation.ID,
	modelRunID *foundation.ID,
	status conversationdomain.AnswerPublicationStatus,
	ordinal int,
) conversationworkflow.WorkspaceAnalysisPublicationOutput {
	resultType := conversationdomain.AnswerResultWorkspaceAnalysis
	if status == conversationdomain.AnswerPublicationRefused {
		resultType = conversationdomain.AnswerResultWorkspaceAnalysisRefusal
	}
	return conversationworkflow.WorkspaceAnalysisPublicationOutput{
		SchemaVersion: conversationworkflow.WorkspaceAnalysisOutputSchemaVersion,
		AnswerID:      answerID, PublicationStatus: status, ResultType: resultType,
		ModelRunID: modelRunID, ResultHash: strings.Repeat("e", 64),
		ProofID: workspaceAnalysisReviewPublishTestID(ordinal),
	}
}

type workspaceAnalysisReviewCandidateFake struct {
	candidate agentdomain.WorkspaceAnalysisCandidate
	query     agentapplication.WorkspaceAnalysisCandidateAuthorityQuery
	calls     int
}

func (fake *workspaceAnalysisReviewCandidateFake) LoadWorkspaceAnalysisCandidateAuthority(
	_ context.Context,
	query agentapplication.WorkspaceAnalysisCandidateAuthorityQuery,
) (agentdomain.WorkspaceAnalysisCandidate, error) {
	fake.calls++
	fake.query = query
	return fake.candidate, nil
}

type workspaceAnalysisReviewValidationFake struct {
	authority toolsapplication.ValidateCitationV3PublicationAuthority
	query     toolsapplication.ValidateCitationV3PublicationAuthorityQuery
	calls     int
}

func (fake *workspaceAnalysisReviewValidationFake) LoadValidateCitationV3PublicationAuthority(
	_ context.Context,
	query toolsapplication.ValidateCitationV3PublicationAuthorityQuery,
) (toolsapplication.ValidateCitationV3PublicationAuthority, error) {
	fake.calls++
	fake.query = query
	return fake.authority, nil
}

type workspaceAnalysisReviewRunnerFake struct {
	result  agentapplication.WorkspaceAnalysisReviewResult
	request agentapplication.WorkspaceAnalysisReviewRequest
	err     error
	calls   int
}

func (fake *workspaceAnalysisReviewRunnerFake) Run(
	_ context.Context,
	request agentapplication.WorkspaceAnalysisReviewRequest,
) (agentapplication.WorkspaceAnalysisReviewResult, error) {
	fake.calls++
	fake.request = request
	if fake.err != nil {
		return agentapplication.WorkspaceAnalysisReviewResult{}, fake.err
	}
	return fake.result, nil
}

type workspaceAnalysisFinalizerFake struct {
	lookupOutput       conversationworkflow.WorkspaceAnalysisPublicationOutput
	successOutput      conversationworkflow.WorkspaceAnalysisPublicationOutput
	terminationOutput  conversationworkflow.WorkspaceAnalysisPublicationOutput
	lookup             conversationapplication.WorkspaceAnalysisPublicationLookup
	successCommand     conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand
	terminationCommand conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand
	lookupFound        bool
	lookupCalls        int
	successCalls       int
	terminationCalls   int
}

func (fake *workspaceAnalysisFinalizerFake) LookupPublication(
	_ context.Context,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	fake.lookupCalls++
	fake.lookup = lookup
	return fake.lookupOutput, fake.lookupFound, nil
}

func (fake *workspaceAnalysisFinalizerFake) FinalizeSuccess(
	_ context.Context,
	command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	fake.successCalls++
	fake.successCommand = command
	return fake.successOutput, false, nil
}

func (fake *workspaceAnalysisFinalizerFake) FinalizeTermination(
	_ context.Context,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	fake.terminationCalls++
	fake.terminationCommand = command
	return fake.terminationOutput, false, nil
}

func workspaceAnalysisReviewPublishTestID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("95000000-0000-4000-8000-%012d", value))
}

var (
	_ agentapplication.WorkspaceAnalysisCandidateAuthorityReader    = (*workspaceAnalysisReviewCandidateFake)(nil)
	_ toolsapplication.ValidateCitationV3PublicationAuthorityReader = (*workspaceAnalysisReviewValidationFake)(nil)
	_ workspaceAnalysisReviewRunner                                 = (*workspaceAnalysisReviewRunnerFake)(nil)
	_ conversationapplication.WorkspaceAnalysisFinalizer            = (*workspaceAnalysisFinalizerFake)(nil)
)
