package application

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

func validateWorkspaceAnalysisV2ModelAuthorization(command AuthorizeWorkspaceAnalysisModelCallCommand) error {
	if err := command.Identity.Validate(); err != nil {
		return err
	}
	contract, err := domain.WorkspaceAnalysisOperationContractForKey(command.OperationKey)
	if err != nil {
		return err
	}
	run, call, identity := command.Run, command.Call, command.Identity
	if identity.DefinitionVersion != 2 || contract.CallKind != domain.WorkspaceAnalysisOperationCallModel ||
		command.OperationKey.NodeKey != identity.NodeKey || !workspaceAnalysisModelCandidateIDs(command) ||
		domain.ValidateModelRun(run) != nil || domain.ValidateModelCall(call) != nil ||
		call.Status != domain.ModelCallStarted || call.Version != 1 || run.WorkspaceID != identity.WorkspaceID ||
		run.WorkflowRunID != identity.WorkflowRunID || run.NodeRunID != identity.NodeRunID ||
		call.ModelRunID != run.ID || call.Model != run.Model || call.Profile != run.Profile || call.Prompt != run.Prompt || call.Schema != run.Schema ||
		len(command.RequestDocument) == 0 || int64(len(command.RequestDocument)) != call.RequestBytes || workspaceAnalysisSHA256(command.RequestDocument) != call.RequestHash {
		return workspaceAnalysisModelError(errors.New("workspace analysis v2 model authorization binding is invalid"))
	}
	valid := false
	switch command.OperationKey.Kind {
	case domain.WorkspaceAnalysisOperationDecision:
		valid = identity.NodeKey == domain.WorkspaceAnalysisOperationNodeDecideNext && call.Phase == domain.ModelCallAgent &&
			call.MaxOutputTokens == int(domain.WorkspaceAnalysisV2DecisionMaxOutputTokens) && call.CallNo <= domain.WorkspaceAnalysisV2MaxDecisions+1 &&
			run.Schema == (domain.SchemaRef{ID: domain.WorkspaceAnalysisDecisionSchemaID, Version: domain.WorkspaceAnalysisDecisionSchemaVersion}) && run.ReducedSchema == run.Schema
		// A retained run may already be terminal when replaying its exact calls.
		// Only the repository's CREATED result grants a new external call.
	case domain.WorkspaceAnalysisOperationAnswerSynthesis:
		valid = identity.NodeKey == domain.WorkspaceAnalysisOperationNodeSynthesizeAnswer && run.Status == domain.ModelRunRunning && run.Version == 1 &&
			run.NodeAttemptID == identity.NodeAttemptID && call.CallNo == 1 && call.Phase == domain.ModelCallAnswer && call.MaxOutputTokens > 0 &&
			call.MaxOutputTokens <= int(domain.WorkspaceAnalysisV2SynthesisMaxOutputTokens) && run.Schema == (domain.SchemaRef{ID: domain.WorkspaceAnalysisCandidateSchemaID, Version: "2"}) && run.ReducedSchema == run.Schema
	case domain.WorkspaceAnalysisOperationFaithfulnessReview:
		valid = identity.NodeKey == domain.WorkspaceAnalysisOperationNodeReviewPublish && run.Status == domain.ModelRunRunning && run.Version == 1 &&
			run.NodeAttemptID == identity.NodeAttemptID && call.CallNo == 1 && workspaceAnalysisModelSlotBinding(command.OperationKey.Kind, run, call)
	}
	if !valid {
		return workspaceAnalysisModelError(errors.New("workspace analysis v2 model slot is invalid"))
	}
	return nil
}

func validateWorkspaceAnalysisV2DecisionAuthorizationResult(result WorkspaceAnalysisModelAuthorizationResult, command AuthorizeWorkspaceAnalysisModelCallCommand) error {
	if err := command.Validate(); err != nil {
		return err
	}
	if domain.ValidateModelRun(result.Run) != nil || domain.ValidateModelCall(result.Call) != nil ||
		!canonicalApplicationID(result.OperationID) || !canonicalApplicationID(result.ReservationID) ||
		!sameWorkspaceAnalysisModelRequest(command.Run, command.Call, result.Run, result.Call) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis decision authorization returned a different request"))
	}
	valid := false
	switch result.Disposition {
	case WorkspaceAnalysisModelAuthorizationCreated:
		valid = result.Run.Status == domain.ModelRunRunning && result.Run.NodeAttemptID == command.Identity.NodeAttemptID &&
			result.Call.Status == domain.ModelCallStarted && result.Call.Version == 1 && result.Call.ID == command.Call.ID &&
			result.Run.ID == command.Run.ID && result.OperationID == command.OperationID && result.ReservationID == command.ReservationID
	case WorkspaceAnalysisModelAuthorizationReconcile:
		valid = result.Run.Status == domain.ModelRunRunning && result.Call.Status == domain.ModelCallStarted && result.Run.NodeAttemptID == command.Identity.NodeAttemptID
	case WorkspaceAnalysisModelAuthorizationReuseResult:
		valid = result.Call.Status == domain.ModelCallSucceeded && (result.Run.Status == domain.ModelRunRunning || result.Run.Status == domain.ModelRunSucceeded || result.Run.Status == domain.ModelRunFailed || result.Run.Status == domain.ModelRunUnknown)
	case WorkspaceAnalysisModelAuthorizationReplayFailure:
		valid = result.Call.Status == domain.ModelCallFailed && result.Run.Status == domain.ModelRunFailed
	case WorkspaceAnalysisModelAuthorizationTerminateUnknown:
		valid = result.Call.Status == domain.ModelCallUnknown && result.Run.Status == domain.ModelRunUnknown
	}
	if !valid {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis decision authorization disposition drifted"))
	}
	return nil
}
