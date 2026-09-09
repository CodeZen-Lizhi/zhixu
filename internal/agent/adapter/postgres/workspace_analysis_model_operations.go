package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const workspaceAnalysisModelResultColumns = `
	result.id::text,result.workspace_id::text,result.analysis_run_id::text,result.operation_id::text,
	result.node_attempt_id::text,result.model_run_id::text,result.model_call_id::text,
	result.operation_kind,result.schema_id,result.schema_version,
	result.subject_candidate_id::text,result.subject_candidate_hash,
	result.document,result.document_hash,result.document_bytes,result.created_at`

const workspaceAnalysisCandidateColumns = `
	candidate.id::text,candidate.workspace_id::text,candidate.analysis_run_id::text,candidate.answer_id::text,
	candidate.synthesis_operation_id::text,candidate.node_attempt_id::text,candidate.synthesis_model_run_id::text,
	candidate.schema_id,candidate.schema_version,candidate.document,candidate.document_hash,
	candidate.document_bytes,candidate.created_at`

type workspaceAnalysisModelWorkflowFence struct {
	definitionID      foundation.ID
	definitionKey     string
	definitionVersion int64
	definitionGraph   []byte
	workflowStatus    string
	cancelRequestedAt *time.Time
	databaseNow       time.Time
	nodeKey           string
	nodeStatus        string
	nodeAttempt       int64
	nodeOwner         *string
	nodeLease         *time.Time
	attemptStatus     string
	attemptNo         int64
	attemptOwner      *string
	attemptLease      *time.Time
}

type workspaceAnalysisModelOperationRecord struct {
	id, workspaceID, analysisRunID, workflowRunID, nodeRunID foundation.ID
	nodeKey                                                  domain.WorkspaceAnalysisOperationNodeKey
	kind                                                     domain.WorkspaceAnalysisOperationKind
	ordinal                                                  int
	callKind                                                 domain.WorkspaceAnalysisOperationCallKind
	requestHash                                              string
	status                                                   domain.WorkspaceAnalysisOperationStatus
	firstAttemptID, latestAttemptID, modelCallID             *foundation.ID
	resultKind                                               *domain.WorkspaceAnalysisOperationResultKind
	resultID                                                 *foundation.ID
	resultHash                                               *string
	errorCode                                                *string
	version                                                  int64
	createdAt, updatedAt                                     time.Time
	startedAt, completedAt                                   *time.Time
}

type workspaceAnalysisModelLocks struct {
	fence       workspaceAnalysisModelWorkflowFence
	analysisRun domain.WorkspaceAnalysisRun
	operation   workspaceAnalysisModelOperationRecord
	reservation *domain.WorkspaceAnalysisBudgetReservation
	modelRun    *domain.ModelRun
	modelCall   *domain.ModelCall
}

func scanWorkspaceAnalysisModelOperation(row rowScanner) (workspaceAnalysisModelOperationRecord, error) {
	var operation workspaceAnalysisModelOperationRecord
	var id, workspaceID, analysisRunID, workflowRunID, nodeRunID string
	var nodeKey, kind, callKind, status string
	var firstAttemptID, latestAttemptID, modelCallID, resultKind, resultID, resultHash, errorCode *string
	if err := row.Scan(
		&id, &workspaceID, &analysisRunID, &workflowRunID, &nodeRunID,
		&nodeKey, &kind, &operation.ordinal, &callKind, &operation.requestHash, &status,
		&firstAttemptID, &latestAttemptID, &modelCallID, &resultKind, &resultID, &resultHash, &errorCode,
		&operation.version, &operation.createdAt, &operation.updatedAt, &operation.startedAt, &operation.completedAt,
	); err != nil {
		return workspaceAnalysisModelOperationRecord{}, err
	}
	operation.id, operation.workspaceID, operation.analysisRunID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(analysisRunID)
	operation.workflowRunID, operation.nodeRunID = foundation.ID(workflowRunID), foundation.ID(nodeRunID)
	operation.nodeKey, operation.kind = domain.WorkspaceAnalysisOperationNodeKey(nodeKey), domain.WorkspaceAnalysisOperationKind(kind)
	operation.callKind, operation.status = domain.WorkspaceAnalysisOperationCallKind(callKind), domain.WorkspaceAnalysisOperationStatus(status)
	operation.firstAttemptID = workspaceAnalysisModelOptionalID(firstAttemptID)
	operation.latestAttemptID = workspaceAnalysisModelOptionalID(latestAttemptID)
	operation.modelCallID = workspaceAnalysisModelOptionalID(modelCallID)
	if resultKind != nil {
		value := domain.WorkspaceAnalysisOperationResultKind(*resultKind)
		operation.resultKind = &value
	}
	operation.resultID = workspaceAnalysisModelOptionalID(resultID)
	operation.resultHash, operation.errorCode = resultHash, errorCode
	return operation, nil
}

func validateWorkspaceAnalysisModelDefinition(
	locked workspaceAnalysisModelLocks,
	identity application.WorkspaceAnalysisModelExecutionIdentity,
) error {
	graph, err := workflowapplication.DecodeCanonicalGraph(locked.fence.definitionGraph)
	if err != nil {
		return consistency(err)
	}
	graphHash, err := workflowapplication.ComputeCanonicalGraphHash(graph)
	if err != nil {
		return consistency(err)
	}
	if locked.fence.definitionID != identity.DefinitionID || locked.fence.definitionKey != locked.analysisRun.DefinitionKey ||
		locked.fence.definitionVersion != identity.DefinitionVersion || locked.fence.definitionVersion != locked.analysisRun.DefinitionVersion ||
		graphHash != identity.DefinitionHash || graphHash != locked.analysisRun.DefinitionHash {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model definition binding drifted"))
	}
	return nil
}

func validateWorkspaceAnalysisModelFence(
	locked workspaceAnalysisModelLocks,
	identity application.WorkspaceAnalysisModelExecutionIdentity,
) error {
	return validateWorkspaceAnalysisModelExecutionFence(locked, identity, false)
}

// workspaceAnalysisModelSuccessfulFinalizationFenceError distinguishes a
// cancellation that won the success-publication race from every other stale
// execution fence. A stale owner or expired lease must not authorize the old
// worker to close the Call.
func workspaceAnalysisModelSuccessfulFinalizationFenceError(
	locked workspaceAnalysisModelLocks,
	identity application.WorkspaceAnalysisModelExecutionIdentity,
	fenceErr error,
) error {
	if locked.fence.cancelRequestedAt == nil ||
		validateWorkspaceAnalysisModelExecutionFence(locked, identity, true) != nil {
		return fenceErr
	}
	return workspaceAnalysisModelCancellationConflict(fenceErr)
}

// validateWorkspaceAnalysisModelFailureTerminalFence keeps cancellation from
// authorizing new work or successful output, while allowing the current owner
// to close an already-authorized call as FAILED or UNKNOWN.
func validateWorkspaceAnalysisModelFailureTerminalFence(
	locked workspaceAnalysisModelLocks,
	identity application.WorkspaceAnalysisModelExecutionIdentity,
	status domain.ModelCallStatus,
) error {
	if locked.fence.cancelRequestedAt == nil {
		return validateWorkspaceAnalysisModelFence(locked, identity)
	}
	if status != domain.ModelCallFailed && status != domain.ModelCallUnknown {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis cancelled model call must settle as failed or unknown"))
	}
	return validateWorkspaceAnalysisModelExecutionFence(locked, identity, true)
}

func validateWorkspaceAnalysisModelExecutionFence(
	locked workspaceAnalysisModelLocks,
	identity application.WorkspaceAnalysisModelExecutionIdentity,
	allowCancellation bool,
) error {
	if locked.fence.workflowStatus != "running" || (!allowCancellation && locked.fence.cancelRequestedAt != nil) ||
		!locked.analysisRun.Status.Active() || locked.fence.nodeKey != string(identity.NodeKey) ||
		locked.fence.nodeStatus != "running" || locked.fence.attemptStatus != "running" ||
		locked.fence.nodeAttempt != identity.LeaseFence || locked.fence.attemptNo != identity.LeaseFence ||
		!workspaceAnalysisModelActiveLease(locked.fence, identity.LeaseOwner) {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model execution fence is stale"))
	}
	return nil
}

func workspaceAnalysisModelActiveLease(fence workspaceAnalysisModelWorkflowFence, expectedOwner string) bool {
	if fence.nodeOwner == nil || fence.attemptOwner == nil || fence.nodeLease == nil || fence.attemptLease == nil ||
		*fence.nodeOwner == "" || *fence.nodeOwner != strings.TrimSpace(*fence.nodeOwner) ||
		*fence.nodeOwner != *fence.attemptOwner || *fence.nodeOwner != expectedOwner ||
		!fence.nodeLease.Equal(*fence.attemptLease) {
		return false
	}
	return fence.attemptLease.After(fence.databaseNow)
}

func (operation workspaceAnalysisModelOperationRecord) domain(reservationID *foundation.ID) domain.WorkspaceAnalysisOperation {
	result := domain.WorkspaceAnalysisOperation{
		ID: operation.id, AnalysisRunID: operation.analysisRunID, NodeKey: operation.nodeKey,
		Kind: operation.kind, Ordinal: operation.ordinal, RequestHash: operation.requestHash,
		Status: operation.status, FirstNodeAttemptID: operation.firstAttemptID,
		LatestNodeAttemptID: operation.latestAttemptID, BudgetReservationID: reservationID,
		Version: operation.version, CreatedAt: operation.createdAt, UpdatedAt: operation.updatedAt,
		StartedAt: operation.startedAt, CompletedAt: operation.completedAt,
	}
	if operation.modelCallID != nil {
		result.Call = &domain.WorkspaceAnalysisOperationCallRef{Kind: domain.WorkspaceAnalysisOperationCallModel, ID: *operation.modelCallID}
	}
	if operation.resultKind != nil && operation.resultID != nil && operation.resultHash != nil {
		result.Result = &domain.WorkspaceAnalysisOperationResultRef{Kind: *operation.resultKind, ID: *operation.resultID, Hash: *operation.resultHash}
	}
	if operation.errorCode != nil {
		result.ErrorCode = *operation.errorCode
	}
	return result
}

func workspaceAnalysisModelOperationHasResult(operation workspaceAnalysisModelOperationRecord) bool {
	return operation.resultKind != nil || operation.resultID != nil || operation.resultHash != nil
}

func validateWorkspaceAnalysisModelReservationSettlement(
	reservation domain.WorkspaceAnalysisBudgetReservation,
	call domain.ModelCall,
) error {
	if reservation.ModelCallID == nil || *reservation.ModelCallID != call.ID ||
		!reservation.CreatedAt.Equal(call.StartedAt) {
		return consistency(errors.New("workspace analysis model reservation call binding drifted"))
	}
	switch reservation.Status {
	case domain.WorkspaceAnalysisBudgetReserved:
		if call.Status != domain.ModelCallStarted {
			return consistency(errors.New("workspace analysis model reserved call is terminal"))
		}
	case domain.WorkspaceAnalysisBudgetSettled:
		if call.Status == domain.ModelCallStarted || reservation.SettledAt == nil || call.CompletedAt == nil ||
			reservation.SettledAt.Before(*call.CompletedAt) ||
			reservation.Settled.InputTokens != call.Usage.InputTokens ||
			reservation.Settled.OutputTokens != call.Usage.OutputTokens {
			return consistency(errors.New("workspace analysis model actual settlement drifted"))
		}
	case domain.WorkspaceAnalysisBudgetUnknownCharged:
		if call.Status != domain.ModelCallUnknown || reservation.SettledAt == nil || call.CompletedAt == nil ||
			reservation.SettledAt.Before(*call.CompletedAt) {
			return consistency(errors.New("workspace analysis model unknown settlement drifted"))
		}
	default:
		return consistency(errors.New("workspace analysis model reservation status is unsupported"))
	}
	return nil
}

func validateWorkspaceAnalysisModelAdmission(
	locked workspaceAnalysisModelLocks,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
) error {
	if locked.analysisRun.PolicyVersion == domain.WorkspaceAnalysisPolicyVersionV2 {
		return validateWorkspaceAnalysisV2ModelAdmission(locked, command)
	}
	run := locked.analysisRun
	timeout, ok := workspaceAnalysisModelTimeout(run, command.OperationKey.Kind)
	if !ok || timeout <= 0 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model timeout is invalid"))
	}
	reservedOutputTokens, ok := workspaceAnalysisModelReservedOutputTokens(run, command.OperationKey.Kind)
	if !ok || int64(command.Call.MaxOutputTokens) != reservedOutputTokens {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model call does not match the frozen output budget"))
	}
	usedModelCalls := run.Reserved.ModelCalls + run.Settled.ModelCalls
	usedInputTokens := run.Reserved.InputTokens + run.Settled.InputTokens
	usedOutputTokens := run.Reserved.OutputTokens + run.Settled.OutputTokens
	if usedModelCalls >= run.Limits.Amount.ModelCalls ||
		usedInputTokens > run.Limits.Amount.InputTokens-domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall ||
		int64(command.Call.MaxOutputTokens) > run.Limits.Amount.OutputTokens-usedOutputTokens {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model budget is exhausted"))
	}
	if locked.fence.databaseNow.Add(timeout + domain.WorkspaceAnalysisV1DurableCompletionMargin).After(run.DeadlineAt) {
		return domain.NewWorkspaceAnalysisPreAuthorizationDeadlineError()
	}
	return nil
}

func workspaceAnalysisModelReservedOutputTokens(
	run domain.WorkspaceAnalysisRun,
	kind domain.WorkspaceAnalysisOperationKind,
) (int64, bool) {
	if run.PolicyVersion == domain.WorkspaceAnalysisPolicyVersionV2 {
		switch kind {
		case domain.WorkspaceAnalysisOperationDecision:
			return domain.WorkspaceAnalysisV2DecisionMaxOutputTokens, true
		case domain.WorkspaceAnalysisOperationAnswerSynthesis:
			return run.Limits.Amount.OutputTokens - domain.WorkspaceAnalysisV2MaxDecisions*domain.WorkspaceAnalysisV2DecisionMaxOutputTokens - domain.WorkspaceAnalysisV2ReviewMaxOutputTokens, true
		case domain.WorkspaceAnalysisOperationFaithfulnessReview:
			return domain.WorkspaceAnalysisV2ReviewMaxOutputTokens, true
		default:
			return 0, false
		}
	}
	switch kind {
	case domain.WorkspaceAnalysisOperationRetrievalPlan:
		return domain.WorkspaceAnalysisV1PlanMaxOutputTokens, true
	case domain.WorkspaceAnalysisOperationAnswerSynthesis:
		return run.Limits.Amount.OutputTokens -
			domain.WorkspaceAnalysisV1PlanMaxOutputTokens - domain.WorkspaceAnalysisV1ReviewMaxOutputTokens, true
	case domain.WorkspaceAnalysisOperationFaithfulnessReview:
		return domain.WorkspaceAnalysisV1ReviewMaxOutputTokens, true
	default:
		return 0, false
	}
}

func workspaceAnalysisModelTimeout(run domain.WorkspaceAnalysisRun, kind domain.WorkspaceAnalysisOperationKind) (time.Duration, bool) {
	switch kind {
	case domain.WorkspaceAnalysisOperationDecision:
		return run.Timeouts.PlanModelTimeout, run.PolicyVersion == domain.WorkspaceAnalysisPolicyVersionV2
	case domain.WorkspaceAnalysisOperationRetrievalPlan:
		return run.Timeouts.PlanModelTimeout, true
	case domain.WorkspaceAnalysisOperationAnswerSynthesis:
		return run.Timeouts.SynthesisModelTimeout, true
	case domain.WorkspaceAnalysisOperationFaithfulnessReview:
		return run.Timeouts.ReviewModelTimeout, true
	default:
		return 0, false
	}
}

func workspaceAnalysisModelAuthorizationResult(
	run domain.ModelRun,
	call domain.ModelCall,
	operationID foundation.ID,
	reservationID foundation.ID,
	disposition application.WorkspaceAnalysisModelAuthorizationDisposition,
) application.WorkspaceAnalysisModelAuthorizationResult {
	return application.WorkspaceAnalysisModelAuthorizationResult{
		Run: run, Call: call, OperationID: operationID, ReservationID: reservationID, Disposition: disposition,
	}
}

func sameWorkspaceAnalysisModelAuthorizationRequest(
	expectedRun domain.ModelRun,
	expectedCall domain.ModelCall,
	actualRun domain.ModelRun,
	actualCall domain.ModelCall,
) bool {
	return expectedCall.ModelRunID == expectedRun.ID && actualCall.ModelRunID == actualRun.ID &&
		expectedRun.WorkspaceID == actualRun.WorkspaceID && expectedRun.WorkflowRunID == actualRun.WorkflowRunID &&
		expectedRun.NodeRunID == actualRun.NodeRunID && sameOptionalInt64(expectedRun.ModelSettingsRevision, actualRun.ModelSettingsRevision) &&
		expectedRun.Model == actualRun.Model && expectedRun.Profile == actualRun.Profile && expectedRun.Prompt == actualRun.Prompt &&
		expectedRun.Schema == actualRun.Schema && expectedRun.ReducedSchema == actualRun.ReducedSchema &&
		sameRetrieval(expectedRun.Retrieval, actualRun.Retrieval) && expectedRun.MemoryContext == actualRun.MemoryContext &&
		expectedCall.CallNo == actualCall.CallNo && expectedCall.Phase == actualCall.Phase &&
		expectedCall.Model == actualCall.Model && expectedCall.Profile == actualCall.Profile && expectedCall.Prompt == actualCall.Prompt &&
		expectedCall.Schema == actualCall.Schema && expectedCall.MaxOutputTokens == actualCall.MaxOutputTokens &&
		expectedCall.RequestHash == actualCall.RequestHash && expectedCall.RequestBytes == actualCall.RequestBytes
}

func authorizationCommandForTerminal(
	identity application.WorkspaceAnalysisModelExecutionIdentity,
	key domain.WorkspaceAnalysisOperationKey,
	operationID foundation.ID,
	reservationID foundation.ID,
	run domain.ModelRun,
	call domain.ModelCall,
) application.AuthorizeWorkspaceAnalysisModelCallCommand {
	return application.AuthorizeWorkspaceAnalysisModelCallCommand{
		Identity: identity, OperationKey: key, OperationID: operationID, ReservationID: reservationID,
		Run: run, Call: call,
	}
}

func authorizationCommandForResult(command application.FinalizeWorkspaceAnalysisModelResultCommand) application.AuthorizeWorkspaceAnalysisModelCallCommand {
	return authorizationCommandForTerminal(command.Identity, command.OperationKey, command.OperationID,
		command.ReservationID, command.Run, command.Call)
}

func authorizationCommandForCandidate(command application.FinalizeWorkspaceAnalysisModelCandidateCommand) application.AuthorizeWorkspaceAnalysisModelCallCommand {
	return authorizationCommandForTerminal(command.Identity, command.OperationKey, command.OperationID,
		command.ReservationID, command.Run, command.Call)
}

func workspaceAnalysisModelOwner(fence workspaceAnalysisModelWorkflowFence) string {
	if fence.attemptOwner == nil {
		return ""
	}
	return *fence.attemptOwner
}

func replayWorkspaceAnalysisModelCallTerminal(
	locked workspaceAnalysisModelLocks,
	command application.FinalizeWorkspaceAnalysisModelCallCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
		!sameModelRunResult(*locked.modelRun, command.Run) || !sameModelCallResult(*locked.modelCall, command.Call) {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model terminal replay differs"))
	}
	wantStatus := domain.WorkspaceAnalysisOperationFailed
	wantReservation := domain.WorkspaceAnalysisBudgetSettled
	if command.Call.Status == domain.ModelCallUnknown {
		wantStatus, wantReservation = domain.WorkspaceAnalysisOperationUnknown, domain.WorkspaceAnalysisBudgetUnknownCharged
	}
	if locked.operation.status != wantStatus || locked.reservation.Status != wantReservation {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model terminal closure differs"))
	}
	return application.WorkspaceAnalysisModelMutationResult{
		Run: *locked.modelRun, Call: *locked.modelCall, OperationID: locked.operation.id,
		ReservationID: locked.reservation.ID, Replayed: true,
	}, nil
}

func scanWorkspaceAnalysisModelResult(row rowScanner) (domain.WorkspaceAnalysisModelResult, error) {
	var result domain.WorkspaceAnalysisModelResult
	var id, workspaceID, analysisRunID, operationID, attemptID, modelRunID, modelCallID string
	var operationKind string
	var subjectCandidateID, subjectCandidateHash *string
	var document []byte
	if err := row.Scan(
		&id, &workspaceID, &analysisRunID, &operationID, &attemptID, &modelRunID, &modelCallID,
		&operationKind, &result.Schema.ID, &result.Schema.Version,
		&subjectCandidateID, &subjectCandidateHash, &document, &result.DocumentHash, &result.DocumentBytes, &result.CreatedAt,
	); err != nil {
		return domain.WorkspaceAnalysisModelResult{}, err
	}
	result.ID, result.WorkspaceID, result.AnalysisRunID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(analysisRunID)
	result.OperationID, result.NodeAttemptID = foundation.ID(operationID), foundation.ID(attemptID)
	result.ModelRunID, result.ModelCallID = foundation.ID(modelRunID), foundation.ID(modelCallID)
	result.OperationKind = domain.WorkspaceAnalysisOperationKind(operationKind)
	result.SubjectCandidateID = workspaceAnalysisModelOptionalID(subjectCandidateID)
	if subjectCandidateHash != nil {
		result.SubjectCandidateHash = *subjectCandidateHash
	}
	result.Document = append(json.RawMessage(nil), document...)
	return result, nil
}

func sameWorkspaceAnalysisModelResult(left, right domain.WorkspaceAnalysisModelResult) bool {
	if left.ID != right.ID || left.WorkspaceID != right.WorkspaceID || left.AnalysisRunID != right.AnalysisRunID ||
		left.OperationID != right.OperationID || left.NodeAttemptID != right.NodeAttemptID ||
		left.ModelRunID != right.ModelRunID || left.ModelCallID != right.ModelCallID ||
		left.OperationKind != right.OperationKind || left.Schema != right.Schema ||
		left.SubjectCandidateHash != right.SubjectCandidateHash || left.DocumentHash != right.DocumentHash ||
		left.DocumentBytes != right.DocumentBytes || !left.CreatedAt.Equal(right.CreatedAt) || !bytes.Equal(left.Document, right.Document) {
		return false
	}
	if left.SubjectCandidateID == nil || right.SubjectCandidateID == nil {
		return left.SubjectCandidateID == nil && right.SubjectCandidateID == nil
	}
	return *left.SubjectCandidateID == *right.SubjectCandidateID
}

func scanWorkspaceAnalysisCandidate(row rowScanner) (domain.WorkspaceAnalysisCandidate, error) {
	var candidate domain.WorkspaceAnalysisCandidate
	var id, workspaceID, analysisRunID, answerID, operationID, attemptID, modelRunID string
	var document []byte
	if err := row.Scan(
		&id, &workspaceID, &analysisRunID, &answerID, &operationID, &attemptID, &modelRunID,
		&candidate.SchemaID, &candidate.SchemaVersion, &document, &candidate.DocumentHash,
		&candidate.DocumentBytes, &candidate.CreatedAt,
	); err != nil {
		return domain.WorkspaceAnalysisCandidate{}, err
	}
	candidate.ID, candidate.WorkspaceID = foundation.ID(id), foundation.ID(workspaceID)
	candidate.AnalysisRunID, candidate.AnswerID = foundation.ID(analysisRunID), foundation.ID(answerID)
	candidate.SynthesisOperationID, candidate.NodeAttemptID = foundation.ID(operationID), foundation.ID(attemptID)
	candidate.SynthesisModelRunID = foundation.ID(modelRunID)
	candidate.Document = append(json.RawMessage(nil), document...)
	return candidate, nil
}

func sameWorkspaceAnalysisCandidate(left, right domain.WorkspaceAnalysisCandidate) bool {
	return left.ID == right.ID && left.WorkspaceID == right.WorkspaceID && left.AnalysisRunID == right.AnalysisRunID &&
		left.AnswerID == right.AnswerID && left.SynthesisOperationID == right.SynthesisOperationID &&
		left.NodeAttemptID == right.NodeAttemptID && left.SynthesisModelRunID == right.SynthesisModelRunID &&
		left.SchemaID == right.SchemaID && left.SchemaVersion == right.SchemaVersion &&
		left.DocumentHash == right.DocumentHash && left.DocumentBytes == right.DocumentBytes &&
		left.CreatedAt.Equal(right.CreatedAt) && bytes.Equal(left.Document, right.Document)
}

func workspaceAnalysisModelRecoveryUnknown(commitErr, recoveryErr error) error {
	if recoveryErr == nil {
		return workspaceAnalysisModelCommitUnknown(commitErr)
	}
	return workspaceAnalysisModelCommitUnknown(errors.Join(commitErr, recoveryErr))
}

func workspaceAnalysisModelOptionalID(value *string) *foundation.ID {
	if value == nil {
		return nil
	}
	parsed := foundation.ID(*value)
	return &parsed
}

func workspaceAnalysisModelInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, application.ErrorCodeWorkspaceAnalysisModelCommandInvalid, false, cause)
}

func workspaceAnalysisModelConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, application.ErrorCodeWorkspaceAnalysisModelAuthorizationInvalid, false, cause)
}

func workspaceAnalysisModelCancellationConflict(cause error) error {
	return foundation.NewError(
		foundation.ErrorVersionConflict,
		application.ErrorCodeWorkspaceAnalysisModelCancellationConflict,
		false,
		cause,
	)
}

func workspaceAnalysisModelUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, application.ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable, true, cause)
}

func workspaceAnalysisModelCommitUnknown(cause error) error {
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, ErrorCodeModelCallResultUnknown, false, cause)
}
