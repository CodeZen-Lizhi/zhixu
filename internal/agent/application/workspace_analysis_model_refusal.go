package application

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// workspaceAnalysisModelRefusalSignal accepts only the explicit provider
// refusal contract. Ordinary provider failures and output validation errors
// must remain FAILED or UNKNOWN.
func workspaceAnalysisModelRefusalSignal(cause error) bool {
	var classified *foundation.Error
	return errors.As(cause, &classified) && classified != nil &&
		classified.Kind == foundation.ErrorNonRetryableFailure &&
		!classified.Retryable &&
		classified.Code == string(domain.WorkspaceAnalysisRunModelRefused)
}

// validateWorkspaceAnalysisModelProviderOutcome validates successful responses
// and requires an explicit refusal to carry the same frozen model, non-empty
// bounded content, and exact token usage as any other provider response.
func validateWorkspaceAnalysisModelProviderOutcome(
	request ChatRequest,
	response ChatResponse,
	cause error,
) error {
	if cause != nil && !workspaceAnalysisModelRefusalSignal(cause) {
		return cause
	}
	if err := ValidateChatResponse(request, response); err != nil {
		return err
	}
	if cause != nil && int64(len(response.Content)) > MaxModelCallResponseBytes {
		return applicationError(
			foundation.ErrorConsistencyViolation,
			errorCodeChatResponseInvalid,
			false,
			errors.New("workspace analysis refusal response exceeds the bounded model call limit"),
		)
	}
	return cause
}

// workspaceAnalysisModelRefusalTerminal creates the sole durable refusal
// shape. The provider body remains private; only its hash, size, and usage are
// persisted on the successful Model Call.
func workspaceAnalysisModelRefusalTerminal(
	authorized WorkspaceAnalysisModelAuthorizationResult,
	response ChatResponse,
	completedAt time.Time,
) (domain.ModelCall, domain.ModelRun) {
	call := authorized.Call
	call.Status = domain.ModelCallSucceeded
	call.ResponseHash = workspaceAnalysisSHA256(response.Content)
	call.ResponseBytes = int64(len(response.Content))
	call.Usage = response.Usage
	call.ErrorCode = ""
	call.LatencyMillis = completedAt.Sub(call.StartedAt).Milliseconds()
	call.Version++
	call.CompletedAt = &completedAt

	run := authorized.Run
	run.Status = domain.ModelRunRefused
	run.FinalResultType = domain.ResultTypeRefusal
	run.FinalErrorCode = string(domain.WorkspaceAnalysisRunModelRefused)
	run.Version++
	run.UpdatedAt = completedAt
	run.CompletedAt = &completedAt
	return call, run
}

func workspaceAnalysisReplayedModelFailure(call domain.ModelCall, run domain.ModelRun) error {
	code := call.ErrorCode
	if call.Status == domain.ModelCallSucceeded && run.Status == domain.ModelRunRefused &&
		run.FinalResultType == domain.ResultTypeRefusal &&
		run.FinalErrorCode == string(domain.WorkspaceAnalysisRunModelRefused) {
		code = run.FinalErrorCode
	}
	return applicationError(
		foundation.ErrorNonRetryableFailure,
		stableTerminalErrorCode(code, nil, ErrorCodeModelCallFailed),
		false,
		errors.New("workspace analysis replays a terminal model failure"),
	)
}
