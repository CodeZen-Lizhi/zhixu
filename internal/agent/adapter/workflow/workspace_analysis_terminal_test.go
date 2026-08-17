package workflow

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestFinalizeWorkspaceAnalysisPreAuthorizationDeadlineErrorAcceptsOnlyFrozenMarkers(t *testing.T) {
	execution, root := workspaceAnalysisInspectExecutionFixture(t)
	analysisRunID := workspaceAnalysisInspectID(9)
	tests := []struct {
		name      string
		kind      foundation.ErrorKind
		code      string
		retryable bool
	}{
		{name: "database admission", kind: foundation.ErrorNonRetryableFailure, code: agentdomain.ErrorCodeWorkspaceAnalysisPreAuthorizationDeadline},
		{name: "node context", kind: foundation.ErrorNonRetryableFailure, code: string(agentdomain.WorkspaceAnalysisRunDeadlineExceeded)},
		{name: "model context", kind: foundation.ErrorRetryableFailure, code: agentapplication.ErrorCodeOperationDeadline, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finalizer := &workspaceAnalysisTerminalFinalizerFake{}
			cause := foundation.NewError(
				test.kind, test.code, test.retryable, context.DeadlineExceeded,
			)

			err := finalizeWorkspaceAnalysisPreAuthorizationDeadlineError(
				context.Background(), finalizer, execution, root, 1, analysisRunID, cause,
			)
			command := finalizer.terminationCommand
			if !errors.Is(err, cause) || finalizer.terminationCalls != 1 ||
				command.Reason != agentdomain.WorkspaceAnalysisRunDeadlineExceeded || command.OperationID != nil ||
				command.ExpectedAnswerVersion != 1 || command.WorkspaceAnalysisPublicationLookup !=
				workspaceAnalysisPublicationLookup(execution, root, analysisRunID) || command.Validate() != nil {
				t.Fatalf("err=%v calls=%d command=%#v", err, finalizer.terminationCalls, command)
			}
		})
	}
}

func TestFinalizeWorkspaceAnalysisPreAuthorizationDeadlineErrorRejectsGenericDeadlineAndConflicts(t *testing.T) {
	execution, root := workspaceAnalysisInspectExecutionFixture(t)
	tests := []struct {
		name  string
		cause error
	}{
		{name: "generic deadline", cause: context.DeadlineExceeded},
		{name: "ordinary version conflict", cause: foundation.NewError(
			foundation.ErrorVersionConflict, "TOOL_CONTEXT_STALE", false, context.DeadlineExceeded,
		)},
		{name: "model authorization invalid", cause: foundation.NewError(
			foundation.ErrorConsistencyViolation, agentapplication.ErrorCodeWorkspaceAnalysisModelAuthorizationInvalid,
			false, context.DeadlineExceeded,
		)},
		{name: "marker without deadline", cause: foundation.NewError(
			foundation.ErrorNonRetryableFailure, agentdomain.ErrorCodeWorkspaceAnalysisPreAuthorizationDeadline,
			false, errors.New("authorization rejected"),
		)},
		{name: "database marker with retryable kind", cause: foundation.NewError(
			foundation.ErrorRetryableFailure, agentdomain.ErrorCodeWorkspaceAnalysisPreAuthorizationDeadline,
			false, context.DeadlineExceeded,
		)},
		{name: "database marker marked retryable", cause: foundation.NewError(
			foundation.ErrorNonRetryableFailure, agentdomain.ErrorCodeWorkspaceAnalysisPreAuthorizationDeadline,
			true, context.DeadlineExceeded,
		)},
		{name: "node marker with retryable kind", cause: foundation.NewError(
			foundation.ErrorRetryableFailure, string(agentdomain.WorkspaceAnalysisRunDeadlineExceeded),
			false, context.DeadlineExceeded,
		)},
		{name: "node marker marked retryable", cause: foundation.NewError(
			foundation.ErrorNonRetryableFailure, string(agentdomain.WorkspaceAnalysisRunDeadlineExceeded),
			true, context.DeadlineExceeded,
		)},
		{name: "model marker with non-retryable kind", cause: foundation.NewError(
			foundation.ErrorNonRetryableFailure, agentapplication.ErrorCodeOperationDeadline,
			true, context.DeadlineExceeded,
		)},
		{name: "model marker marked non-retryable", cause: foundation.NewError(
			foundation.ErrorRetryableFailure, agentapplication.ErrorCodeOperationDeadline,
			false, context.DeadlineExceeded,
		)},
		{name: "persistence unknown wrapping model deadline", cause: foundation.NewError(
			foundation.ErrorManualRecoveryRequired,
			agentapplication.ErrorCodeModelCallPersistenceUnknown,
			false,
			foundation.NewError(
				foundation.ErrorRetryableFailure,
				agentapplication.ErrorCodeOperationDeadline,
				true,
				context.DeadlineExceeded,
			),
		)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finalizer := &workspaceAnalysisTerminalFinalizerFake{}
			err := finalizeWorkspaceAnalysisPreAuthorizationDeadlineError(
				context.Background(), finalizer, execution, root, 1, workspaceAnalysisInspectID(9), test.cause,
			)
			if !errors.Is(err, test.cause) || finalizer.terminationCalls != 0 {
				t.Fatalf("err=%v calls=%d", err, finalizer.terminationCalls)
			}
		})
	}
}

func TestWorkspaceAnalysisTerminalWrappersDoNotFinalizeOrdinaryConflict(t *testing.T) {
	execution, root := workspaceAnalysisInspectExecutionFixture(t)
	cause := foundation.NewError(
		foundation.ErrorVersionConflict, "TOOL_CONTEXT_STALE", false, context.DeadlineExceeded,
	)
	tests := []struct {
		name string
		run  func(*workspaceAnalysisTerminalFinalizerFake) error
	}{
		{name: "model", run: func(finalizer *workspaceAnalysisTerminalFinalizerFake) error {
			return finalizeWorkspaceAnalysisModelTerminalError(
				context.Background(), finalizer, execution, root, 1, workspaceAnalysisInspectID(9), cause,
			)
		}},
		{name: "tool", run: func(finalizer *workspaceAnalysisTerminalFinalizerFake) error {
			return finalizeWorkspaceAnalysisToolTerminalError(
				context.Background(), finalizer, execution, root, 1, workspaceAnalysisInspectID(9), cause,
			)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finalizer := &workspaceAnalysisTerminalFinalizerFake{}
			err := test.run(finalizer)
			if !errors.Is(err, cause) || finalizer.terminationCalls != 0 {
				t.Fatalf("err=%v calls=%d", err, finalizer.terminationCalls)
			}
		})
	}
}

func TestWorkspaceAnalysisPersistedTerminalEvidenceKeepsOperationProofAheadOfDeadlineMarker(t *testing.T) {
	execution, root := workspaceAnalysisInspectExecutionFixture(t)
	analysisRunID := workspaceAnalysisInspectID(9)
	operationID := workspaceAnalysisInspectID(10)
	cause := foundation.NewError(
		foundation.ErrorNonRetryableFailure,
		agentdomain.ErrorCodeWorkspaceAnalysisPreAuthorizationDeadline,
		false,
		context.DeadlineExceeded,
	)
	tests := []struct {
		name string
		run  func(*workspaceAnalysisTerminalFinalizerFake) error
		want agentdomain.WorkspaceAnalysisRunTerminationReason
	}{
		{
			name: "model evidence",
			want: agentdomain.WorkspaceAnalysisRunModelFailed,
			run: func(finalizer *workspaceAnalysisTerminalFinalizerFake) error {
				return finalizeWorkspaceAnalysisModelTerminalEvidenceError(
					context.Background(), finalizer, execution, root, 1, analysisRunID,
					agentapplication.WorkspaceAnalysisModelTerminalEvidence{
						OperationID: operationID, CallStatus: agentdomain.ModelCallFailed, RunStatus: agentdomain.ModelRunFailed,
					},
					cause,
				)
			},
		},
		{
			name: "tool evidence",
			want: agentdomain.WorkspaceAnalysisRunToolFailed,
			run: func(finalizer *workspaceAnalysisTerminalFinalizerFake) error {
				return finalizeWorkspaceAnalysisToolTerminalEvidenceError(
					context.Background(), finalizer, execution, root, 1, analysisRunID,
					toolsapplication.WorkspaceAnalysisToolTerminal{
						OperationID: operationID, CallStatus: toolsdomain.CallFailed,
					},
					cause,
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finalizer := &workspaceAnalysisTerminalFinalizerFake{}
			err := test.run(finalizer)
			command := finalizer.terminationCommand
			if !errors.Is(err, cause) || finalizer.terminationCalls != 1 || command.Reason != test.want ||
				command.OperationID == nil || *command.OperationID != operationID || command.Validate() != nil {
				t.Fatalf("err=%v calls=%d command=%#v", err, finalizer.terminationCalls, command)
			}
		})
	}
}

func TestWorkspaceAnalysisPersistedModelTerminalEvidenceKeepsCancellationCheckpoint(t *testing.T) {
	execution, root := workspaceAnalysisInspectExecutionFixture(t)
	analysisRunID := workspaceAnalysisInspectID(9)
	finalizer := &workspaceAnalysisTerminalFinalizerFake{}
	cause := foundation.NewError(
		foundation.ErrorNonRetryableFailure,
		agentapplication.ErrorCodeOperationCancelled,
		false,
		context.Canceled,
	)

	err := finalizeWorkspaceAnalysisModelTerminalEvidenceError(
		context.Background(), finalizer, execution, root, 1, analysisRunID,
		agentapplication.WorkspaceAnalysisModelTerminalEvidence{
			OperationID: workspaceAnalysisInspectID(10),
			CallStatus:  agentdomain.ModelCallFailed,
			RunStatus:   agentdomain.ModelRunFailed,
		},
		cause,
	)
	command := finalizer.terminationCommand
	if !errors.Is(err, cause) || !errors.Is(err, context.Canceled) || finalizer.terminationCalls != 1 ||
		command.Reason != agentdomain.WorkspaceAnalysisRunCancellation || command.OperationID != nil ||
		command.ExpectedAnswerVersion != 1 || command.WorkspaceAnalysisPublicationLookup !=
		workspaceAnalysisPublicationLookup(execution, root, analysisRunID) || command.Validate() != nil {
		t.Fatalf("err=%v calls=%d command=%#v", err, finalizer.terminationCalls, command)
	}
}

func TestWorkspaceAnalysisExecutorsFinalizeExpiredNodeContextWithoutOperation(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T) (error, *workspaceAnalysisFinalizerFake, int)
	}{
		{name: "inspect", run: func(t *testing.T) (error, *workspaceAnalysisFinalizerFake, int) {
			execution, input := workspaceAnalysisInspectExecutionFixture(t)
			run := workspaceAnalysisInspectRunFixture(t, execution, input)
			tool := &workspaceAnalysisInspectToolFake{result: workspaceAnalysisInspectToolResult(execution)}
			finalizer := &workspaceAnalysisFinalizerFake{}
			executor, err := NewWorkspaceAnalysisInspectExecutor(WorkspaceAnalysisInspectExecutorDependencies{
				Context: &workspaceAnalysisInspectContextFake{result: workspaceAnalysisInspectQuestionContext(t, execution, input)},
				Runs:    &workspaceAnalysisInspectRunFake{run: run}, Tools: tool, Finalizer: finalizer,
				Clock: foundation.FixedClock{Value: run.DeadlineAt},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = executor.Execute(context.Background(), execution)
			return err, finalizer, tool.calls
		}},
		{name: "retrieve", run: func(t *testing.T) (error, *workspaceAnalysisFinalizerFake, int) {
			fixture := newWorkspaceAnalysisRetrieveFixture(t, false)
			fixture.clock = fixture.run.DeadlineAt
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			return err, fixture.finalizer, fixture.planner.calls + fixture.tools.calls
		}},
		{name: "read", run: func(t *testing.T) (error, *workspaceAnalysisFinalizerFake, int) {
			fixture := newWorkspaceAnalysisReadEvidenceFixture(t, 1)
			fixture.clock = fixture.run.DeadlineAt
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			return err, fixture.finalizer, fixture.receipts.calls + fixture.tools.calls
		}},
		{name: "synthesis", run: func(t *testing.T) (error, *workspaceAnalysisFinalizerFake, int) {
			fixture := newWorkspaceAnalysisSynthesizeFixture(t, 1)
			fixture.clock = fixture.run.DeadlineAt
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			return err, fixture.finalizer, fixture.authority.calls + fixture.synthesis.calls
		}},
		{name: "citation", run: func(t *testing.T) (error, *workspaceAnalysisFinalizerFake, int) {
			fixture := newWorkspaceAnalysisValidateCitationsFixture(t, []workspaceAnalysisValidationResultFixture{{
				EvidenceRef: "E1", Valid: true, ReasonCode: conversationworkflow.WorkspaceAnalysisCitationValidationReasonOK,
			}})
			fixture.clock = fixture.run.DeadlineAt
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			return err, fixture.finalizer, fixture.authority.authorityCalls + fixture.tools.calls
		}},
		{name: "review", run: func(t *testing.T) (error, *workspaceAnalysisFinalizerFake, int) {
			fixture := newWorkspaceAnalysisReviewPublishFixture(t, true, true)
			fixture.clock = fixture.run.DeadlineAt
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			return err, fixture.finalizer, fixture.candidates.calls + fixture.evidence.calls + fixture.validation.calls + fixture.review.calls
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err, finalizer, externalCalls := test.run(t)
			command := finalizer.terminationCommand
			if codeOf(err) != string(agentdomain.WorkspaceAnalysisRunDeadlineExceeded) ||
				!errors.Is(err, context.DeadlineExceeded) || externalCalls != 0 || finalizer.terminationCalls != 1 ||
				command.Reason != agentdomain.WorkspaceAnalysisRunDeadlineExceeded || command.OperationID != nil || command.Validate() != nil {
				t.Fatalf("err=%v external=%d calls=%d command=%#v", err, externalCalls, finalizer.terminationCalls, command)
			}
		})
	}
}

func TestWorkspaceAnalysisOperationAdmissionDeadlineMarkersFinalizeWithoutOperation(t *testing.T) {
	tests := []struct {
		name               string
		modelMarker        bool
		wantAdmissionCalls int
		run                func(*testing.T, error) (error, *workspaceAnalysisFinalizerFake, int, int)
	}{
		{name: "inspect", wantAdmissionCalls: 1, run: func(t *testing.T, marker error) (error, *workspaceAnalysisFinalizerFake, int, int) {
			execution, input := workspaceAnalysisInspectExecutionFixture(t)
			run := workspaceAnalysisInspectRunFixture(t, execution, input)
			tool := &workspaceAnalysisInspectToolFake{err: marker}
			finalizer := &workspaceAnalysisFinalizerFake{}
			executor, err := NewWorkspaceAnalysisInspectExecutor(WorkspaceAnalysisInspectExecutorDependencies{
				Context: &workspaceAnalysisInspectContextFake{result: workspaceAnalysisInspectQuestionContext(t, execution, input)},
				Runs:    &workspaceAnalysisInspectRunFake{run: run}, Tools: tool, Finalizer: finalizer,
				Clock: foundation.FixedClock{Value: workspaceAnalysisInspectNow()},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = executor.Execute(context.Background(), execution)
			return err, finalizer, tool.calls, 0
		}},
		{name: "plan", modelMarker: true, wantAdmissionCalls: 1, run: func(t *testing.T, marker error) (error, *workspaceAnalysisFinalizerFake, int, int) {
			fixture := newWorkspaceAnalysisRetrieveFixture(t, false)
			fixture.planner.err = marker
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			return err, fixture.finalizer, fixture.planner.calls, fixture.tools.calls
		}},
		{name: "search", wantAdmissionCalls: 1, run: func(t *testing.T, marker error) (error, *workspaceAnalysisFinalizerFake, int, int) {
			fixture := newWorkspaceAnalysisRetrieveFixture(t, false)
			fixture.tools.err = marker
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			return err, fixture.finalizer, fixture.tools.calls, fixture.receipts.calls
		}},
		{name: "source ordinal 1", wantAdmissionCalls: 1, run: func(t *testing.T, marker error) (error, *workspaceAnalysisFinalizerFake, int, int) {
			fixture := newWorkspaceAnalysisReadEvidenceFixture(t, 3)
			fixture.tools.failAt, fixture.tools.err = 1, marker
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			return err, fixture.finalizer, fixture.tools.calls, 0
		}},
		{name: "source ordinal 2", wantAdmissionCalls: 2, run: func(t *testing.T, marker error) (error, *workspaceAnalysisFinalizerFake, int, int) {
			fixture := newWorkspaceAnalysisReadEvidenceFixture(t, 3)
			fixture.tools.failAt, fixture.tools.err = 2, marker
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			return err, fixture.finalizer, fixture.tools.calls, 0
		}},
		{name: "source ordinal 3", wantAdmissionCalls: 3, run: func(t *testing.T, marker error) (error, *workspaceAnalysisFinalizerFake, int, int) {
			fixture := newWorkspaceAnalysisReadEvidenceFixture(t, 3)
			fixture.tools.failAt, fixture.tools.err = 3, marker
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			return err, fixture.finalizer, fixture.tools.calls, 0
		}},
		{name: "synthesis", modelMarker: true, wantAdmissionCalls: 1, run: func(t *testing.T, marker error) (error, *workspaceAnalysisFinalizerFake, int, int) {
			fixture := newWorkspaceAnalysisSynthesizeFixture(t, 1)
			fixture.synthesis.err = marker
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			return err, fixture.finalizer, fixture.synthesis.calls, 0
		}},
		{name: "citation", wantAdmissionCalls: 1, run: func(t *testing.T, marker error) (error, *workspaceAnalysisFinalizerFake, int, int) {
			fixture := newWorkspaceAnalysisValidateCitationsFixture(t, []workspaceAnalysisValidationResultFixture{{
				EvidenceRef: "E1", Valid: true, ReasonCode: conversationworkflow.WorkspaceAnalysisCitationValidationReasonOK,
			}})
			fixture.tools.err = marker
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			return err, fixture.finalizer, fixture.tools.calls, fixture.authority.receiptCalls
		}},
		{name: "review", modelMarker: true, wantAdmissionCalls: 1, run: func(t *testing.T, marker error) (error, *workspaceAnalysisFinalizerFake, int, int) {
			fixture := newWorkspaceAnalysisReviewPublishFixture(t, true, true)
			fixture.review.err = marker
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			return err, fixture.finalizer, fixture.review.calls, fixture.finalizer.successCalls
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			marker := agentdomain.NewWorkspaceAnalysisPreAuthorizationDeadlineError()
			if test.modelMarker {
				marker = foundation.NewError(
					foundation.ErrorRetryableFailure,
					agentapplication.ErrorCodeOperationDeadline,
					true,
					context.DeadlineExceeded,
				)
			}
			err, finalizer, admissionCalls, downstreamCalls := test.run(t, marker)
			command := finalizer.terminationCommand
			if !errors.Is(err, marker) || admissionCalls != test.wantAdmissionCalls || downstreamCalls != 0 || finalizer.terminationCalls != 1 ||
				command.Reason != agentdomain.WorkspaceAnalysisRunDeadlineExceeded || command.OperationID != nil || command.Validate() != nil {
				t.Fatalf("err=%v admission=%d/%d downstream=%d finalizer=%d command=%#v",
					err, admissionCalls, test.wantAdmissionCalls, downstreamCalls, finalizer.terminationCalls, command)
			}
		})
	}
}

func TestFinalizeWorkspaceAnalysisReceiptFailureTerminalErrorMapsExactProof(t *testing.T) {
	execution, root := workspaceAnalysisInspectExecutionFixture(t)
	analysisRunID := workspaceAnalysisInspectID(9)
	operationID := workspaceAnalysisInspectID(10)
	failureID := workspaceAnalysisInspectID(11)
	expectedHash := strings.Repeat("a", 64)
	differentHash := strings.Repeat("b", 64)

	tests := []struct {
		name       string
		code       toolsdomain.ResultReceiptFailureCode
		actualHash *string
		wantCode   conversationapplication.WorkspaceAnalysisReceiptFailureCode
	}{
		{name: "missing", code: toolsdomain.ResultReceiptFailureMissing, wantCode: conversationapplication.WorkspaceAnalysisReceiptMissing},
		{name: "hash mismatch", code: toolsdomain.ResultReceiptFailureHashMismatch, actualHash: &differentHash, wantCode: conversationapplication.WorkspaceAnalysisReceiptHashMismatch},
		{name: "contract invalid", code: toolsdomain.ResultReceiptFailureContractInvalid, actualHash: &expectedHash, wantCode: conversationapplication.WorkspaceAnalysisReceiptContractInvalid},
		{name: "binding invalid", code: toolsdomain.ResultReceiptFailureBindingInvalid, actualHash: &expectedHash, wantCode: conversationapplication.WorkspaceAnalysisReceiptBindingInvalid},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finalizer := &workspaceAnalysisTerminalFinalizerFake{}
			cause := errors.New("receipt formation failed")
			evidence := toolsapplication.WorkspaceAnalysisReceiptFailureTerminal{
				OperationID: operationID, FailureID: failureID, Code: test.code,
				ExpectedHash: expectedHash, ActualHash: test.actualHash,
			}

			err := finalizeWorkspaceAnalysisReceiptFailureTerminalError(
				context.Background(), finalizer, execution, root, 1, analysisRunID, evidence, cause,
			)
			if !errors.Is(err, cause) || finalizer.terminationCalls != 1 {
				t.Fatalf("err=%v termination calls=%d", err, finalizer.terminationCalls)
			}
			wantLookup := workspaceAnalysisPublicationLookup(execution, root, analysisRunID)
			command := finalizer.terminationCommand
			if command.WorkspaceAnalysisPublicationLookup != wantLookup || command.ExpectedAnswerVersion != 1 ||
				command.OperationID == nil || *command.OperationID != operationID || command.ReceiptFailure == nil ||
				command.ReceiptFailure.ID != failureID || command.ReceiptFailure.Code != test.wantCode ||
				command.ReceiptFailure.ExpectedHash != expectedHash || !reflect.DeepEqual(command.ReceiptFailure.ActualHash, test.actualHash) ||
				command.Validate() != nil {
				t.Fatalf("termination command=%#v", command)
			}
		})
	}
}

func TestFinalizeWorkspaceAnalysisReceiptFailureTerminalErrorRejectsUnknownCode(t *testing.T) {
	execution, root := workspaceAnalysisInspectExecutionFixture(t)
	finalizer := &workspaceAnalysisTerminalFinalizerFake{}
	cause := errors.New("receipt formation failed")

	err := finalizeWorkspaceAnalysisReceiptFailureTerminalError(
		context.Background(), finalizer, execution, root, 1, workspaceAnalysisInspectID(9),
		toolsapplication.WorkspaceAnalysisReceiptFailureTerminal{Code: "OTHER"}, cause,
	)
	if !errors.Is(err, cause) || finalizer.terminationCalls != 0 {
		t.Fatalf("err=%v termination calls=%d", err, finalizer.terminationCalls)
	}
}

func TestFinalizeWorkspaceAnalysisReceiptFailureTerminalErrorUsesDetachedContext(t *testing.T) {
	execution, root := workspaceAnalysisInspectExecutionFixture(t)
	finalizer := &workspaceAnalysisTerminalFinalizerFake{}
	cause := errors.New("receipt formation failed")
	expectedHash := strings.Repeat("a", 64)
	callerContext, cancel := context.WithCancel(context.Background())
	cancel()

	err := finalizeWorkspaceAnalysisReceiptFailureTerminalError(
		callerContext,
		finalizer,
		execution,
		root,
		1,
		workspaceAnalysisInspectID(9),
		toolsapplication.WorkspaceAnalysisReceiptFailureTerminal{
			OperationID: workspaceAnalysisInspectID(10), FailureID: workspaceAnalysisInspectID(11),
			Code: toolsdomain.ResultReceiptFailureContractInvalid, ExpectedHash: expectedHash, ActualHash: &expectedHash,
		},
		cause,
	)
	if !errors.Is(err, cause) || finalizer.terminationCalls != 1 || finalizer.terminationContextErr != nil {
		t.Fatalf("err=%v termination calls=%d finalizer context err=%v", err, finalizer.terminationCalls, finalizer.terminationContextErr)
	}
}

func TestFinalizeWorkspaceAnalysisSuccessPublicationFallsBackToCancellation(t *testing.T) {
	execution, root := workspaceAnalysisInspectExecutionFixture(t)
	finalizer := &workspaceAnalysisTerminalFinalizerFake{
		successErr: foundation.NewError(
			foundation.ErrorVersionConflict,
			"CONVERSATION_WORKSPACE_ANALYSIS_FINALIZE_CONFLICT",
			false,
			errors.New("cancellation won the publication race"),
		),
	}
	hash := strings.Repeat("a", 64)
	command := conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand{
		WorkspaceAnalysisPublicationLookup: workspaceAnalysisPublicationLookup(execution, root, workspaceAnalysisInspectID(9)),
		ExpectedAnswerVersion:              1,
		CandidateID:                        workspaceAnalysisInspectID(10), CandidateHash: hash,
		GitReceiptID: workspaceAnalysisInspectID(11), GitReceiptHash: hash,
		ValidationReceiptID: workspaceAnalysisInspectID(12), ValidationReceiptHash: hash,
		ReviewModelResultID: workspaceAnalysisInspectID(13), ReviewModelResultHash: hash,
	}
	callerContext, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := finalizeWorkspaceAnalysisSuccessPublication(callerContext, finalizer, command)
	if err != nil || finalizer.successCalls != 1 || finalizer.terminationCalls != 1 ||
		finalizer.successContextErr != nil || finalizer.terminationContextErr != nil ||
		finalizer.terminationCommand.Reason != "WORKSPACE_ANALYSIS_CANCELLED" ||
		finalizer.terminationCommand.WorkspaceAnalysisPublicationLookup != command.WorkspaceAnalysisPublicationLookup ||
		finalizer.terminationCommand.ExpectedAnswerVersion != command.ExpectedAnswerVersion {
		t.Fatalf("err=%v success calls=%d termination calls=%d success ctx=%v termination ctx=%v command=%#v",
			err, finalizer.successCalls, finalizer.terminationCalls, finalizer.successContextErr,
			finalizer.terminationContextErr, finalizer.terminationCommand)
	}
}

type workspaceAnalysisTerminalFinalizerFake struct {
	terminationCommand    conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand
	terminationContextErr error
	terminationCalls      int
	successContextErr     error
	successErr            error
	successCalls          int
}

func (*workspaceAnalysisTerminalFinalizerFake) LookupPublication(
	context.Context,
	conversationapplication.WorkspaceAnalysisPublicationLookup,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, nil
}

func (fake *workspaceAnalysisTerminalFinalizerFake) FinalizeSuccess(
	ctx context.Context,
	_ conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	fake.successCalls++
	fake.successContextErr = ctx.Err()
	return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, fake.successErr
}

func (fake *workspaceAnalysisTerminalFinalizerFake) FinalizeTermination(
	ctx context.Context,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	fake.terminationCalls++
	fake.terminationCommand = command
	fake.terminationContextErr = ctx.Err()
	return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, nil
}

var _ conversationapplication.WorkspaceAnalysisFinalizer = (*workspaceAnalysisTerminalFinalizerFake)(nil)
