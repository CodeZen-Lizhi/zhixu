package workflow

import (
	"context"
	"errors"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const workspaceAnalysisTerminalizationTimeout = 5 * time.Second

func finalizeWorkspaceAnalysisModelTerminalError(
	ctx context.Context,
	finalizer conversationapplication.WorkspaceAnalysisFinalizer,
	execution workflowapplication.ExecutionContext,
	root conversationworkflow.WorkspaceAnalysisInput,
	answerVersion int64,
	analysisRunID foundation.ID,
	cause error,
) error {
	evidence, found := agentapplication.WorkspaceAnalysisModelTerminalEvidenceFromError(cause)
	if !found {
		return finalizeWorkspaceAnalysisPreAuthorizationDeadlineError(
			ctx, finalizer, execution, root, answerVersion, analysisRunID, cause,
		)
	}
	return finalizeWorkspaceAnalysisModelTerminalEvidenceError(
		ctx, finalizer, execution, root, answerVersion, analysisRunID, evidence, cause,
	)
}

func finalizeWorkspaceAnalysisModelTerminalEvidenceError(
	ctx context.Context,
	finalizer conversationapplication.WorkspaceAnalysisFinalizer,
	execution workflowapplication.ExecutionContext,
	root conversationworkflow.WorkspaceAnalysisInput,
	answerVersion int64,
	analysisRunID foundation.ID,
	evidence agentapplication.WorkspaceAnalysisModelTerminalEvidence,
	cause error,
) error {
	var reason agentdomain.WorkspaceAnalysisRunTerminationReason
	switch {
	case evidence.CallStatus == agentdomain.ModelCallFailed && evidence.RunStatus == agentdomain.ModelRunFailed:
		reason = agentdomain.WorkspaceAnalysisRunModelFailed
	case evidence.CallStatus == agentdomain.ModelCallUnknown && evidence.RunStatus == agentdomain.ModelRunUnknown:
		reason = agentdomain.WorkspaceAnalysisRunResultUnknown
	case evidence.CallStatus == agentdomain.ModelCallSucceeded && evidence.RunStatus == agentdomain.ModelRunRefused:
		reason = agentdomain.WorkspaceAnalysisRunModelRefused
	default:
		return cause
	}
	return finalizeWorkspaceAnalysisOperationTerminalError(
		ctx, finalizer, execution, root, answerVersion, analysisRunID, evidence.OperationID, reason, cause,
	)
}

func finalizeWorkspaceAnalysisToolTerminalError(
	ctx context.Context,
	finalizer conversationapplication.WorkspaceAnalysisFinalizer,
	execution workflowapplication.ExecutionContext,
	root conversationworkflow.WorkspaceAnalysisInput,
	answerVersion int64,
	analysisRunID foundation.ID,
	cause error,
) error {
	receiptFailure, receiptFailureFound := toolsapplication.WorkspaceAnalysisReceiptFailureTerminalFromError(cause)
	if receiptFailureFound {
		return finalizeWorkspaceAnalysisReceiptFailureTerminalError(
			ctx, finalizer, execution, root, answerVersion, analysisRunID, receiptFailure, cause,
		)
	}
	evidence, found := toolsapplication.WorkspaceAnalysisToolTerminalFromError(cause)
	if !found {
		return finalizeWorkspaceAnalysisPreAuthorizationDeadlineError(
			ctx, finalizer, execution, root, answerVersion, analysisRunID, cause,
		)
	}
	return finalizeWorkspaceAnalysisToolTerminalEvidenceError(
		ctx, finalizer, execution, root, answerVersion, analysisRunID, evidence, cause,
	)
}

func finalizeWorkspaceAnalysisToolTerminalEvidenceError(
	ctx context.Context,
	finalizer conversationapplication.WorkspaceAnalysisFinalizer,
	execution workflowapplication.ExecutionContext,
	root conversationworkflow.WorkspaceAnalysisInput,
	answerVersion int64,
	analysisRunID foundation.ID,
	evidence toolsapplication.WorkspaceAnalysisToolTerminal,
	cause error,
) error {
	var reason agentdomain.WorkspaceAnalysisRunTerminationReason
	switch evidence.CallStatus {
	case toolsdomain.CallFailed:
		reason = agentdomain.WorkspaceAnalysisRunToolFailed
	case toolsdomain.CallUnknown:
		reason = agentdomain.WorkspaceAnalysisRunResultUnknown
	default:
		return cause
	}
	return finalizeWorkspaceAnalysisOperationTerminalError(
		ctx, finalizer, execution, root, answerVersion, analysisRunID, evidence.OperationID, reason, cause,
	)
}

func finalizeWorkspaceAnalysisPreAuthorizationDeadlineError(
	ctx context.Context,
	finalizer conversationapplication.WorkspaceAnalysisFinalizer,
	execution workflowapplication.ExecutionContext,
	root conversationworkflow.WorkspaceAnalysisInput,
	answerVersion int64,
	analysisRunID foundation.ID,
	cause error,
) error {
	if !workspaceAnalysisPreAuthorizationDeadline(cause) {
		return cause
	}
	command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: workspaceAnalysisPublicationLookup(execution, root, analysisRunID),
		ExpectedAnswerVersion:              answerVersion,
		Reason:                             agentdomain.WorkspaceAnalysisRunDeadlineExceeded,
	}
	return finalizeWorkspaceAnalysisTerminationError(ctx, finalizer, command, cause)
}

func workspaceAnalysisPreAuthorizationDeadline(err error) bool {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var classified *foundation.Error
	return errors.As(err, &classified) && workspaceAnalysisPreAuthorizationDeadlineMarker(classified) &&
		errors.Is(classified, context.DeadlineExceeded)
}

func workspaceAnalysisPreAuthorizationDeadlineMarker(err *foundation.Error) bool {
	if err == nil {
		return false
	}
	switch err.Code {
	case agentdomain.ErrorCodeWorkspaceAnalysisPreAuthorizationDeadline,
		string(agentdomain.WorkspaceAnalysisRunDeadlineExceeded):
		return err.Kind == foundation.ErrorNonRetryableFailure && !err.Retryable
	case agentapplication.ErrorCodeOperationDeadline:
		return err.Kind == foundation.ErrorRetryableFailure && err.Retryable
	default:
		return false
	}
}

func finalizeWorkspaceAnalysisOperationTerminalError(
	ctx context.Context,
	finalizer conversationapplication.WorkspaceAnalysisFinalizer,
	execution workflowapplication.ExecutionContext,
	root conversationworkflow.WorkspaceAnalysisInput,
	answerVersion int64,
	analysisRunID foundation.ID,
	operationID foundation.ID,
	reason agentdomain.WorkspaceAnalysisRunTerminationReason,
	cause error,
) error {
	lookup := workspaceAnalysisPublicationLookup(execution, root, analysisRunID)
	command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: lookup,
		ExpectedAnswerVersion:              answerVersion,
		Reason:                             reason,
		OperationID:                        &operationID,
	}
	return finalizeWorkspaceAnalysisTerminationError(ctx, finalizer, command, cause)
}

func finalizeWorkspaceAnalysisReceiptFailureTerminalError(
	ctx context.Context,
	finalizer conversationapplication.WorkspaceAnalysisFinalizer,
	execution workflowapplication.ExecutionContext,
	root conversationworkflow.WorkspaceAnalysisInput,
	answerVersion int64,
	analysisRunID foundation.ID,
	evidence toolsapplication.WorkspaceAnalysisReceiptFailureTerminal,
	cause error,
) error {
	failureCode, found := workspaceAnalysisReceiptFailureCode(evidence.Code)
	if !found {
		return cause
	}
	operationID := evidence.OperationID
	command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: workspaceAnalysisPublicationLookup(execution, root, analysisRunID),
		ExpectedAnswerVersion:              answerVersion,
		Reason:                             agentdomain.WorkspaceAnalysisRunReceiptInvalid,
		OperationID:                        &operationID,
		ReceiptFailure: &conversationapplication.WorkspaceAnalysisReceiptFailure{
			ID: evidence.FailureID, Code: failureCode,
			ExpectedHash: evidence.ExpectedHash, ActualHash: evidence.ActualHash,
		},
	}
	return finalizeWorkspaceAnalysisTerminationError(ctx, finalizer, command, cause)
}

func workspaceAnalysisReceiptFailureCode(
	code toolsdomain.ResultReceiptFailureCode,
) (conversationapplication.WorkspaceAnalysisReceiptFailureCode, bool) {
	switch code {
	case toolsdomain.ResultReceiptFailureMissing:
		return conversationapplication.WorkspaceAnalysisReceiptMissing, true
	case toolsdomain.ResultReceiptFailureHashMismatch:
		return conversationapplication.WorkspaceAnalysisReceiptHashMismatch, true
	case toolsdomain.ResultReceiptFailureContractInvalid:
		return conversationapplication.WorkspaceAnalysisReceiptContractInvalid, true
	case toolsdomain.ResultReceiptFailureBindingInvalid:
		return conversationapplication.WorkspaceAnalysisReceiptBindingInvalid, true
	default:
		return "", false
	}
}

func finalizeWorkspaceAnalysisTerminationError(
	ctx context.Context,
	finalizer conversationapplication.WorkspaceAnalysisFinalizer,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
	cause error,
) error {
	lookup := command.WorkspaceAnalysisPublicationLookup
	if errors.Is(cause, context.Canceled) {
		if err := finalizeWorkspaceAnalysisCancellation(ctx, finalizer, lookup, command.ExpectedAnswerVersion); err == nil {
			return cause
		} else if !workspaceAnalysisFinalizationConflict(err) {
			return errors.Join(err, cause)
		}
	}
	finalizeContext, cancel := workspaceAnalysisFinalizationContext(ctx)
	_, _, err := finalizer.FinalizeTermination(finalizeContext, command)
	cancel()
	if err == nil {
		return cause
	}
	if !errors.Is(cause, context.Canceled) && workspaceAnalysisFinalizationConflict(err) {
		if cancellationErr := finalizeWorkspaceAnalysisCancellation(ctx, finalizer, lookup, command.ExpectedAnswerVersion); cancellationErr == nil {
			return cause
		}
	}
	return errors.Join(err, cause)
}

func finalizeWorkspaceAnalysisCancellation(
	ctx context.Context,
	finalizer conversationapplication.WorkspaceAnalysisFinalizer,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
	answerVersion int64,
) error {
	finalizeContext, cancel := workspaceAnalysisFinalizationContext(ctx)
	defer cancel()
	_, _, err := finalizer.FinalizeTermination(
		finalizeContext,
		conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
			WorkspaceAnalysisPublicationLookup: lookup,
			ExpectedAnswerVersion:              answerVersion,
			Reason:                             agentdomain.WorkspaceAnalysisRunCancellation,
		},
	)
	return err
}

func finalizeWorkspaceAnalysisTerminationPublication(
	ctx context.Context,
	finalizer conversationapplication.WorkspaceAnalysisFinalizer,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, error) {
	finalizeContext, cancel := workspaceAnalysisFinalizationContext(ctx)
	published, _, err := finalizer.FinalizeTermination(finalizeContext, command)
	cancel()
	if err == nil {
		return published, nil
	}
	if !workspaceAnalysisFinalizationConflict(err) {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}

	cancellationContext, cancellationCancel := workspaceAnalysisFinalizationContext(ctx)
	published, _, cancellationErr := finalizer.FinalizeTermination(
		cancellationContext,
		conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
			WorkspaceAnalysisPublicationLookup: command.WorkspaceAnalysisPublicationLookup,
			ExpectedAnswerVersion:              command.ExpectedAnswerVersion,
			Reason:                             agentdomain.WorkspaceAnalysisRunCancellation,
		},
	)
	cancellationCancel()
	if cancellationErr == nil {
		return published, nil
	}
	return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, errors.Join(err, cancellationErr)
}

func finalizeWorkspaceAnalysisSuccessPublication(
	ctx context.Context,
	finalizer conversationapplication.WorkspaceAnalysisFinalizer,
	command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, error) {
	finalizeContext, cancel := workspaceAnalysisFinalizationContext(ctx)
	published, _, err := finalizer.FinalizeSuccess(finalizeContext, command)
	cancel()
	if err == nil {
		return published, nil
	}
	if !workspaceAnalysisFinalizationConflict(err) {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, err
	}

	cancellationContext, cancellationCancel := workspaceAnalysisFinalizationContext(ctx)
	published, _, cancellationErr := finalizer.FinalizeTermination(
		cancellationContext,
		conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
			WorkspaceAnalysisPublicationLookup: command.WorkspaceAnalysisPublicationLookup,
			ExpectedAnswerVersion:              command.ExpectedAnswerVersion,
			Reason:                             agentdomain.WorkspaceAnalysisRunCancellation,
		},
	)
	cancellationCancel()
	if cancellationErr == nil {
		return published, nil
	}
	return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, errors.Join(err, cancellationErr)
}

func workspaceAnalysisFinalizationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), workspaceAnalysisTerminalizationTimeout)
}

func workspaceAnalysisFinalizationConflict(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == foundation.ErrorVersionConflict
}
