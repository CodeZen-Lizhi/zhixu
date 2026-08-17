package application

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisPublicationLookupRequiresCanonicalLeaseFence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisPublicationLookup)
	}{
		{name: "missing owner", mutate: func(lookup *WorkspaceAnalysisPublicationLookup) { lookup.ExpectedLeaseOwner = "" }},
		{name: "padded owner", mutate: func(lookup *WorkspaceAnalysisPublicationLookup) { lookup.ExpectedLeaseOwner = " worker" }},
		{name: "oversized owner", mutate: func(lookup *WorkspaceAnalysisPublicationLookup) { lookup.ExpectedLeaseOwner = strings.Repeat("w", 257) }},
		{name: "invalid utf8 owner", mutate: func(lookup *WorkspaceAnalysisPublicationLookup) { lookup.ExpectedLeaseOwner = string([]byte{0xff}) }},
		{name: "zero fence", mutate: func(lookup *WorkspaceAnalysisPublicationLookup) { lookup.ExpectedLeaseFence = 0 }},
		{name: "database integer overflow", mutate: func(lookup *WorkspaceAnalysisPublicationLookup) { lookup.ExpectedLeaseFence = math.MaxInt32 + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookup := workspaceAnalysisFinalizerTestLookup()
			test.mutate(&lookup)
			assertWorkspaceAnalysisFinalizerInvalid(t, lookup.Validate())
		})
	}
}

func TestWorkspaceAnalysisFinalizerCommandsAcceptFrozenTerminationMatrix(t *testing.T) {
	lookup := workspaceAnalysisFinalizerTestLookup()
	operationID := workspaceAnalysisFinalizerTestID(20)
	artifactID := workspaceAnalysisFinalizerTestID(21)
	failureID := workspaceAnalysisFinalizerTestID(22)
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	toolArtifact := &WorkspaceAnalysisTerminationArtifact{
		Kind: WorkspaceAnalysisTerminationArtifactToolReceipt, ID: artifactID, Hash: hashA,
	}
	modelArtifact := &WorkspaceAnalysisTerminationArtifact{
		Kind: WorkspaceAnalysisTerminationArtifactModelResult, ID: artifactID, Hash: hashA,
	}
	tests := []struct {
		name    string
		reason  agentdomain.WorkspaceAnalysisRunTerminationReason
		command FinalizeWorkspaceAnalysisTerminationCommand
	}{
		{name: "evidence insufficient", reason: agentdomain.WorkspaceAnalysisRunEvidenceInsufficient,
			command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunEvidenceInsufficient, &operationID, toolArtifact)},
		{name: "citation invalid", reason: agentdomain.WorkspaceAnalysisRunCitationInvalid,
			command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunCitationInvalid, &operationID, toolArtifact)},
		{name: "faithfulness rejected", reason: agentdomain.WorkspaceAnalysisRunFaithfulnessRejected,
			command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunFaithfulnessRejected, &operationID, modelArtifact)},
		{name: "needs clarification", reason: agentdomain.WorkspaceAnalysisRunNeedsClarification,
			command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunNeedsClarification, &operationID, modelArtifact)},
		{name: "budget exhausted", reason: agentdomain.WorkspaceAnalysisRunBudgetExhausted,
			command: FinalizeWorkspaceAnalysisTerminationCommand{
				WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
				Reason: agentdomain.WorkspaceAnalysisRunBudgetExhausted, OperationID: &operationID,
				BudgetRequest: &WorkspaceAnalysisBudgetRequest{ToolCalls: 1},
			}},
		{name: "receipt missing", reason: agentdomain.WorkspaceAnalysisRunReceiptInvalid,
			command: FinalizeWorkspaceAnalysisTerminationCommand{
				WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
				Reason: agentdomain.WorkspaceAnalysisRunReceiptInvalid, OperationID: &operationID,
				ReceiptFailure: &WorkspaceAnalysisReceiptFailure{
					ID: failureID, Code: WorkspaceAnalysisReceiptMissing, ExpectedHash: hashA,
				},
			}},
		{name: "receipt hash mismatch", reason: agentdomain.WorkspaceAnalysisRunReceiptInvalid,
			command: FinalizeWorkspaceAnalysisTerminationCommand{
				WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
				Reason: agentdomain.WorkspaceAnalysisRunReceiptInvalid, OperationID: &operationID,
				ReceiptFailure: &WorkspaceAnalysisReceiptFailure{
					ID: failureID, Code: WorkspaceAnalysisReceiptHashMismatch, ExpectedHash: hashA, ActualHash: &hashB,
				},
			}},
		{name: "receipt contract invalid", reason: agentdomain.WorkspaceAnalysisRunReceiptInvalid,
			command: FinalizeWorkspaceAnalysisTerminationCommand{
				WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
				Reason: agentdomain.WorkspaceAnalysisRunReceiptInvalid, OperationID: &operationID,
				ReceiptFailure: &WorkspaceAnalysisReceiptFailure{
					ID: failureID, Code: WorkspaceAnalysisReceiptContractInvalid, ExpectedHash: hashA, ActualHash: &hashA,
				},
			}},
		{name: "receipt binding invalid", reason: agentdomain.WorkspaceAnalysisRunReceiptInvalid,
			command: FinalizeWorkspaceAnalysisTerminationCommand{
				WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
				Reason: agentdomain.WorkspaceAnalysisRunReceiptInvalid, OperationID: &operationID,
				ReceiptFailure: &WorkspaceAnalysisReceiptFailure{
					ID: failureID, Code: WorkspaceAnalysisReceiptBindingInvalid, ExpectedHash: hashA, ActualHash: &hashA,
				},
			}},
	}
	for _, reason := range []agentdomain.WorkspaceAnalysisRunTerminationReason{
		agentdomain.WorkspaceAnalysisRunModelRefused,
		agentdomain.WorkspaceAnalysisRunResultUnknown,
		agentdomain.WorkspaceAnalysisRunDeadlineExceeded,
		agentdomain.WorkspaceAnalysisRunModelFailed,
		agentdomain.WorkspaceAnalysisRunToolFailed,
	} {
		tests = append(tests, struct {
			name    string
			reason  agentdomain.WorkspaceAnalysisRunTerminationReason
			command FinalizeWorkspaceAnalysisTerminationCommand
		}{name: string(reason), reason: reason, command: workspaceAnalysisTerminationTestCommand(lookup, reason, &operationID, nil)})
	}
	tests = append(tests, struct {
		name    string
		reason  agentdomain.WorkspaceAnalysisRunTerminationReason
		command FinalizeWorkspaceAnalysisTerminationCommand
	}{
		name: "deadline before operation creation", reason: agentdomain.WorkspaceAnalysisRunDeadlineExceeded,
		command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunDeadlineExceeded, nil, nil),
	})
	tests = append(tests, struct {
		name    string
		reason  agentdomain.WorkspaceAnalysisRunTerminationReason
		command FinalizeWorkspaceAnalysisTerminationCommand
	}{
		name: "cancelled", reason: agentdomain.WorkspaceAnalysisRunCancellation,
		command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunCancellation, nil, nil),
	})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.command.Reason != test.reason {
				t.Fatalf("reason = %s, want %s", test.command.Reason, test.reason)
			}
			if err := test.command.Validate(); err != nil {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}

func TestWorkspaceAnalysisFinalizerCommandsRejectCallerInventedOrAmbiguousProofs(t *testing.T) {
	lookup := workspaceAnalysisFinalizerTestLookup()
	operationID := workspaceAnalysisFinalizerTestID(20)
	artifactID := workspaceAnalysisFinalizerTestID(21)
	failureID := workspaceAnalysisFinalizerTestID(22)
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	validArtifact := &WorkspaceAnalysisTerminationArtifact{
		Kind: WorkspaceAnalysisTerminationArtifactToolReceipt, ID: artifactID, Hash: hashA,
	}
	tests := []struct {
		name    string
		command FinalizeWorkspaceAnalysisTerminationCommand
	}{
		{name: "completed is not a termination", command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunCompleted, &operationID, nil)},
		{name: "citation missing operation", command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunCitationInvalid, nil, validArtifact)},
		{name: "citation wrong artifact kind", command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunCitationInvalid, &operationID,
			&WorkspaceAnalysisTerminationArtifact{Kind: WorkspaceAnalysisTerminationArtifactModelResult, ID: artifactID, Hash: hashA})},
		{name: "unknown with artifact", command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunResultUnknown, &operationID, validArtifact)},
		{name: "deadline without operation with artifact", command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunDeadlineExceeded, nil, validArtifact)},
		{name: "cancel with operation", command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunCancellation, &operationID, nil)},
		{name: "runtime failure with operation", command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunRuntimeFailed, &operationID, nil)},
		{name: "runtime failure with artifact", command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunRuntimeFailed, nil, validArtifact)},
		{name: "runtime failure is runtime-hook only", command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunRuntimeFailed, nil, nil)},
		{name: "zero budget request", command: FinalizeWorkspaceAnalysisTerminationCommand{
			WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
			Reason: agentdomain.WorkspaceAnalysisRunBudgetExhausted, OperationID: &operationID,
			BudgetRequest: &WorkspaceAnalysisBudgetRequest{},
		}},
		{name: "caller supplied budget cost", command: FinalizeWorkspaceAnalysisTerminationCommand{
			WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
			Reason: agentdomain.WorkspaceAnalysisRunBudgetExhausted, OperationID: &operationID,
			BudgetRequest: &WorkspaceAnalysisBudgetRequest{ToolCalls: 1, RequestedCostMicrounits: func() *int64 { value := int64(1); return &value }()},
		}},
		{name: "missing receipt has actual hash", command: FinalizeWorkspaceAnalysisTerminationCommand{
			WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
			Reason: agentdomain.WorkspaceAnalysisRunReceiptInvalid, OperationID: &operationID,
			ReceiptFailure: &WorkspaceAnalysisReceiptFailure{ID: failureID, Code: WorkspaceAnalysisReceiptMissing, ExpectedHash: hashA, ActualHash: &hashB},
		}},
		{name: "hash mismatch is equal", command: FinalizeWorkspaceAnalysisTerminationCommand{
			WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
			Reason: agentdomain.WorkspaceAnalysisRunReceiptInvalid, OperationID: &operationID,
			ReceiptFailure: &WorkspaceAnalysisReceiptFailure{ID: failureID, Code: WorkspaceAnalysisReceiptHashMismatch, ExpectedHash: hashA, ActualHash: &hashA},
		}},
		{name: "proof identity reused", command: workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunResultUnknown, &lookup.AnswerID, nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertWorkspaceAnalysisFinalizerInvalid(t, test.command.Validate())
		})
	}
}

func TestFinalizeWorkspaceAnalysisSuccessCommandOnlyCarriesDurableFactReferences(t *testing.T) {
	lookup := workspaceAnalysisFinalizerTestLookup()
	command := FinalizeWorkspaceAnalysisSuccessCommand{
		WorkspaceAnalysisPublicationLookup: lookup,
		ExpectedAnswerVersion:              1,
		CandidateID:                        workspaceAnalysisFinalizerTestID(20),
		CandidateHash:                      strings.Repeat("a", 64),
		GitReceiptID:                       workspaceAnalysisFinalizerTestID(21),
		GitReceiptHash:                     strings.Repeat("b", 64),
		ValidationReceiptID:                workspaceAnalysisFinalizerTestID(22),
		ValidationReceiptHash:              strings.Repeat("c", 64),
		ReviewModelResultID:                workspaceAnalysisFinalizerTestID(23),
		ReviewModelResultHash:              strings.Repeat("d", 64),
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	command.CandidateID = lookup.AnswerID
	assertWorkspaceAnalysisFinalizerInvalid(t, command.Validate())
}

func workspaceAnalysisFinalizerTestLookup() WorkspaceAnalysisPublicationLookup {
	return WorkspaceAnalysisPublicationLookup{
		AnswerPublicationLookup: AnswerPublicationLookup{
			WorkspaceID: workspaceAnalysisFinalizerTestID(1), WorkflowRunID: workspaceAnalysisFinalizerTestID(2),
			NodeRunID: workspaceAnalysisFinalizerTestID(3), NodeAttemptID: workspaceAnalysisFinalizerTestID(4),
			ConversationID: workspaceAnalysisFinalizerTestID(5), QuestionID: workspaceAnalysisFinalizerTestID(6),
			AnswerID: workspaceAnalysisFinalizerTestID(7),
		},
		AnalysisRunID:      workspaceAnalysisFinalizerTestID(8),
		ExpectedLeaseOwner: "workspace-analysis-worker",
		ExpectedLeaseFence: 3,
	}
}

func workspaceAnalysisTerminationTestCommand(
	lookup WorkspaceAnalysisPublicationLookup,
	reason agentdomain.WorkspaceAnalysisRunTerminationReason,
	operationID *foundation.ID,
	artifact *WorkspaceAnalysisTerminationArtifact,
) FinalizeWorkspaceAnalysisTerminationCommand {
	return FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: lookup,
		ExpectedAnswerVersion:              1,
		Reason:                             reason,
		OperationID:                        operationID,
		Artifact:                           artifact,
	}
}

func workspaceAnalysisFinalizerTestID(suffix int) foundation.ID {
	return foundation.ID(fmt.Sprintf("97000000-0000-4000-8000-%012d", suffix))
}

func assertWorkspaceAnalysisFinalizerInvalid(t *testing.T, err error) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != ErrorCodeWorkspaceAnalysisFinalizerInvalid {
		t.Fatalf("error = %#v, want %s", err, ErrorCodeWorkspaceAnalysisFinalizerInvalid)
	}
}
