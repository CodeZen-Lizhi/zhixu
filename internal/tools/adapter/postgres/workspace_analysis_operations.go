package postgres

import (
	"context"
	"errors"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/jackc/pgx/v5"
)

var (
	workspaceAnalysisGitStatusRef = domain.ToolRef{Name: "ReadGitStatus", Version: 2}
	workspaceAnalysisCitationRef  = domain.ToolRef{Name: "ValidateCitation", Version: 3}
)

// workspaceAnalysisOperationLocks is deliberately a narrow projection. Keeping
// the lock order here identical to result_receipts.go prevents a completion and
// a replacement Attempt from observing different authorization facts.
type workspaceAnalysisOperationLocks struct {
	fence       receiptExecutionFence
	policy      persistedPolicy
	analysisRun receiptAnalysisRun
	operation   receiptOperation
	reservation receiptReservation
	call        *domain.ToolCall
	cancelledAt *time.Time
	deadlineAt  time.Time
	catalogHash string
}

// AuthorizeWorkspaceAnalysisToolCall atomically admits one server-selected,
// canonical Workspace Analysis Tool call. It is intentionally separate from
// StartCall: this path owns the cross-attempt operation and durable budget.
func (repository *Repository) AuthorizeWorkspaceAnalysisToolCall(
	ctx context.Context,
	command application.AuthorizeWorkspaceAnalysisToolCallCommand,
) (application.WorkspaceAnalysisToolAuthorizationResult, error) {
	call, definition, contract, err := validateWorkspaceAnalysisToolAuthorization(command)
	if err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := lockWorkspaceAnalysisOperation(ctx, tx, command.Identity, command.OperationKey, command.OperationID, call, true)
	if err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, err
	}
	if err := validateWorkspaceAnalysisAuthorizationFence(locked, command.Identity, command.OperationKey); err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, err
	}
	if err := validateStartPolicy(locked.policy.policy, application.StartCallCommand{
		Identity:         command.Identity,
		Call:             call,
		AllowedWorkflows: definition.AllowedWorkflows,
	}); err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, err
	}
	if locked.catalogHash != workspaceAnalysisCatalogHash() {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, stale(errors.New("workspace analysis tool catalog hash drifted"))
	}
	if locked.operation.status != "PENDING" {
		if err := validateExistingWorkspaceAnalysisToolBinding(locked); err != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, err
		}
	}

	switch locked.operation.status {
	case "PENDING":
		result, authorizeErr := authorizePendingWorkspaceAnalysisToolCall(ctx, tx, locked, command, call, definition, contract)
		if authorizeErr != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, authorizeErr
		}
		if err := repository.appendWorkspaceAnalysisToolRequested(ctx, tx, locked.operation, result.Call, false); err != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, err
		}
		if _, constraintErr := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); constraintErr != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(constraintErr)
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return repository.recoverWorkspaceAnalysisAuthorizationAfterCommitError(ctx, command, call, commitErr)
		}
		return result, nil
	case "STARTED":
		if locked.call == nil || locked.reservation.id == "" {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, consistency(errors.New("started workspace analysis operation is incomplete"))
		}
		if locked.operation.requestHash != call.RequestHash || !sameWorkspaceAnalysisToolRequestBinding(*locked.call, call) {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, idempotencyConflict(errors.New("workspace analysis operation request binding differs"))
		}
		if stringPointerValue(locked.operation.latestAttemptID) == string(command.Identity.NodeAttemptID) {
			if err := repository.appendWorkspaceAnalysisToolRequested(ctx, tx, locked.operation, *locked.call, true); err != nil {
				return application.WorkspaceAnalysisToolAuthorizationResult{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(err)
			}
			return workspaceAnalysisAuthorizationResult(*locked.call, locked.operation.id, locked.reservation.id, application.WorkspaceAnalysisToolAuthorizationReconcile), nil
		}
		result, reduceErr := reducePriorWorkspaceAnalysisToolCallToUnknown(ctx, tx, locked, command.Identity)
		if reduceErr != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, reduceErr
		}
		if err := repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, result.Call, "unknown", false); err != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, err
		}
		if _, constraintErr := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); constraintErr != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(constraintErr)
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return repository.recoverWorkspaceAnalysisAuthorizationAfterCommitError(ctx, command, call, commitErr)
		}
		return result, nil
	case "SUCCEEDED":
		if locked.call == nil || locked.reservation.id == "" || locked.reservation.status != "SETTLED" || locked.call.Status != domain.CallSucceeded {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, consistency(errors.New("successful workspace analysis operation closure is incomplete"))
		}
		if locked.operation.requestHash != call.RequestHash || !sameWorkspaceAnalysisToolRequestBinding(*locked.call, call) {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, idempotencyConflict(errors.New("workspace analysis result replay binding differs"))
		}
		if err := repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, *locked.call, "succeeded", true); err != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, err
		}
		if err := advanceTerminalWorkspaceAnalysisOperationAttempt(ctx, tx, locked, command.Identity.NodeAttemptID); err != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(err)
		}
		return workspaceAnalysisAuthorizationResult(*locked.call, locked.operation.id, locked.reservation.id, application.WorkspaceAnalysisToolAuthorizationReuseResult), nil
	case "FAILED":
		if locked.call == nil || locked.reservation.id == "" || locked.reservation.status != "SETTLED" {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, consistency(errors.New("failed workspace analysis operation closure is incomplete"))
		}
		var receiptFailure *domain.ResultReceiptFailure
		switch {
		case locked.call.Status == domain.CallFailed && stringPointerValue(locked.operation.errorCode) != "WORKSPACE_ANALYSIS_RECEIPT_INVALID":
		case locked.call.Status == domain.CallSucceeded && stringPointerValue(locked.operation.errorCode) == "WORKSPACE_ANALYSIS_RECEIPT_INVALID":
			failure, loadErr := loadResultReceiptFailure(
				ctx, tx, *locked.call, definition, locked.analysisRun.id, locked.operation.id,
			)
			if loadErr != nil {
				return application.WorkspaceAnalysisToolAuthorizationResult{}, loadErr
			}
			receiptFailure = &failure
		default:
			return application.WorkspaceAnalysisToolAuthorizationResult{}, consistency(errors.New("failed workspace analysis operation call closure differs"))
		}
		if locked.operation.requestHash != call.RequestHash || !sameWorkspaceAnalysisToolRequestBinding(*locked.call, call) {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, idempotencyConflict(errors.New("workspace analysis failure replay binding differs"))
		}
		if err := repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, *locked.call, "failed", true); err != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, err
		}
		if err := advanceTerminalWorkspaceAnalysisOperationAttempt(ctx, tx, locked, command.Identity.NodeAttemptID); err != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(err)
		}
		result := workspaceAnalysisAuthorizationResult(*locked.call, locked.operation.id, locked.reservation.id, application.WorkspaceAnalysisToolAuthorizationReplayFailure)
		result.ReceiptFailure = receiptFailure
		return result, nil
	case "UNKNOWN":
		if locked.call == nil || locked.reservation.id == "" || locked.reservation.status != "UNKNOWN_CHARGED" || locked.call.Status != domain.CallUnknown {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, consistency(errors.New("unknown workspace analysis operation closure is incomplete"))
		}
		if locked.operation.requestHash != call.RequestHash || !sameWorkspaceAnalysisToolRequestBinding(*locked.call, call) {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, idempotencyConflict(errors.New("workspace analysis unknown replay binding differs"))
		}
		if err := repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, *locked.call, "unknown", true); err != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, err
		}
		if err := advanceTerminalWorkspaceAnalysisOperationAttempt(ctx, tx, locked, command.Identity.NodeAttemptID); err != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(err)
		}
		return workspaceAnalysisAuthorizationResult(*locked.call, locked.operation.id, locked.reservation.id, application.WorkspaceAnalysisToolAuthorizationTerminateUnknown), nil
	default:
		return application.WorkspaceAnalysisToolAuthorizationResult{}, consistency(errors.New("workspace analysis operation status is unsupported"))
	}
}

// FinalizeWorkspaceAnalysisToolCall closes a failed or unknown Tool call with
// the same run budget and operation transaction used by successful receipts.
func (repository *Repository) FinalizeWorkspaceAnalysisToolCall(
	ctx context.Context,
	command application.FinalizeWorkspaceAnalysisToolCallCommand,
) (application.ToolCallMutationResult, error) {
	call, err := validateWorkspaceAnalysisToolFinalization(command)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := lockReceiptCompletion(ctx, tx, call)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	if err := validateReceiptCompletionBindings(locked, call); err != nil {
		return application.ToolCallMutationResult{}, err
	}
	if locked.operation.status == "FAILED" || locked.operation.status == "UNKNOWN" {
		if !sameTerminalResult(locked.call, call) ||
			(locked.operation.status == "FAILED" && locked.reservation.status != "SETTLED") ||
			(locked.operation.status == "UNKNOWN" && locked.reservation.status != "UNKNOWN_CHARGED") {
			return application.ToolCallMutationResult{}, receiptConflict(errors.New("workspace analysis terminal replay differs"))
		}
		status, statusErr := workspaceAnalysisToolTerminalEventStatus(locked.call)
		if statusErr != nil {
			return application.ToolCallMutationResult{}, statusErr
		}
		if err := repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, locked.call, status, true); err != nil {
			return application.ToolCallMutationResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return application.ToolCallMutationResult{}, classify(err)
		}
		return application.ToolCallMutationResult{Call: locked.call, Replayed: true}, nil
	}
	if locked.operation.status != "STARTED" || locked.reservation.status != "RESERVED" || locked.call.Status != domain.CallStarted {
		return application.ToolCallMutationResult{}, receiptConflict(errors.New("workspace analysis terminal call is not startable"))
	}
	if !activeReceiptExecutionFence(locked, call, command.Identity) {
		return application.ToolCallMutationResult{}, stale(errors.New("workspace analysis terminal attempt fence is stale"))
	}
	mutation, err := finalizeCallTx(ctx, tx, command.ExpectedVersion, call)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	if mutation.Replayed || mutation.Call.CompletedAt == nil {
		return application.ToolCallMutationResult{}, consistency(errors.New("workspace analysis terminal call CAS is inconsistent"))
	}
	unknown := mutation.Call.Status == domain.CallUnknown
	if err := settleWorkspaceAnalysisToolReservation(ctx, tx, locked, *mutation.Call.CompletedAt, unknown); err != nil {
		return application.ToolCallMutationResult{}, err
	}
	if err := settleWorkspaceAnalysisToolRunBudget(ctx, tx, locked, *mutation.Call.CompletedAt, unknown); err != nil {
		return application.ToolCallMutationResult{}, err
	}
	if err := completeWorkspaceAnalysisToolOperation(ctx, tx, locked, mutation.Call, *mutation.Call.CompletedAt, unknown, command.Identity.NodeAttemptID); err != nil {
		return application.ToolCallMutationResult{}, err
	}
	status, statusErr := workspaceAnalysisToolTerminalEventStatus(mutation.Call)
	if statusErr != nil {
		return application.ToolCallMutationResult{}, statusErr
	}
	if err := repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, mutation.Call, status, false); err != nil {
		return application.ToolCallMutationResult{}, err
	}
	if _, constraintErr := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); constraintErr != nil {
		return application.ToolCallMutationResult{}, classify(constraintErr)
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return repository.recoverWorkspaceAnalysisToolFinalizationAfterCommitError(ctx, call, commitErr)
	}
	return mutation, nil
}

func validateWorkspaceAnalysisToolAuthorization(command application.AuthorizeWorkspaceAnalysisToolCallCommand) (domain.ToolCall, domain.Definition, agentdomain.WorkspaceAnalysisOperationContract, error) {
	if err := validateIdentityCallBinding(command.Identity, command.Call); err != nil {
		return domain.ToolCall{}, domain.Definition{}, agentdomain.WorkspaceAnalysisOperationContract{}, err
	}
	operationID, operationIDErr := foundation.ParseID(string(command.OperationID))
	reservationID, reservationIDErr := foundation.ParseID(string(command.ReservationID))
	if err := command.OperationKey.Validate(); err != nil || command.OperationKey.AnalysisRunID == command.Identity.WorkspaceID ||
		operationIDErr != nil || reservationIDErr != nil || operationID != command.OperationID || reservationID != command.ReservationID ||
		command.OperationID == command.ReservationID || command.OperationID == command.OperationKey.AnalysisRunID || command.ReservationID == command.OperationKey.AnalysisRunID {
		if err == nil {
			err = errors.New("workspace analysis authorization ids are invalid")
		}
		return domain.ToolCall{}, domain.Definition{}, agentdomain.WorkspaceAnalysisOperationContract{}, foundation.NewError(foundation.ErrorInvalidInput, agentdomain.ErrorCodeWorkspaceAnalysisOperationInvalid, false, err)
	}
	contract, err := agentdomain.WorkspaceAnalysisOperationContractForKey(command.OperationKey)
	if err != nil || contract.CallKind != agentdomain.WorkspaceAnalysisOperationCallTool || string(contract.NodeKey) != command.Identity.NodeKey {
		if err == nil {
			err = errors.New("workspace analysis tool operation is not assigned to the current node")
		}
		return domain.ToolCall{}, domain.Definition{}, agentdomain.WorkspaceAnalysisOperationContract{}, foundation.NewError(foundation.ErrorInvalidInput, agentdomain.ErrorCodeWorkspaceAnalysisOperationInvalid, false, err)
	}
	call := command.Call
	call.StartedAt, call.CompletedAt, call.DurationMillis = time.Unix(1, 0).UTC(), nil, 0
	if call.Status != domain.CallStarted || call.Version != 1 || call.Tool == nil || call.IdempotencyKey != "" ||
		call.InvocationPolicy != domain.InvocationTrustedWorkflowOnly || call.SideEffectLevel != domain.SideEffectNone ||
		call.RequestHash == "" || domain.ValidateToolCall(call) != nil {
		return domain.ToolCall{}, domain.Definition{}, agentdomain.WorkspaceAnalysisOperationContract{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("workspace analysis STARTED tool call is invalid"))
	}
	definition, err := exactWorkspaceAnalysisToolDefinition(command.Definition, contract, *call.Tool)
	expectedCallNo := 1
	if contract.Kind == agentdomain.WorkspaceAnalysisOperationSourceRead {
		expectedCallNo = contract.Ordinal
	}
	if err != nil || call.DefinitionHash != definition.DefinitionHash || call.InputSchema == nil || *call.InputSchema != definition.InputSchema ||
		call.OutputSchema == nil || *call.OutputSchema != definition.OutputSchema || call.Capability != definition.RequiredCapability ||
		call.CallNo != expectedCallNo {
		if err == nil {
			err = errors.New("workspace analysis call definition binding differs")
		}
		return domain.ToolCall{}, domain.Definition{}, agentdomain.WorkspaceAnalysisOperationContract{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, err)
	}
	if call.RequestedToolName != definition.Ref.Name {
		return domain.ToolCall{}, domain.Definition{}, agentdomain.WorkspaceAnalysisOperationContract{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("workspace analysis requested tool differs"))
	}
	return call, definition, contract, nil
}

func validateWorkspaceAnalysisToolFinalization(command application.FinalizeWorkspaceAnalysisToolCallCommand) (domain.ToolCall, error) {
	if err := validateIdentityCallBinding(command.Identity, command.Call); err != nil {
		return domain.ToolCall{}, err
	}
	unknown := command.Call.Status == domain.CallUnknown
	if command.Call.Status != domain.CallFailed && !unknown {
		return domain.ToolCall{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("workspace analysis finalizer only accepts failed or unknown calls"))
	}
	return validateTerminalCommand(command.ExpectedVersion, command.Call, unknown)
}

func exactWorkspaceAnalysisToolDefinition(input domain.Definition, contract agentdomain.WorkspaceAnalysisOperationContract, ref domain.ToolRef) (domain.Definition, error) {
	expected, ok := workspaceAnalysisToolForOperation(contract)
	if !ok || expected != ref {
		return domain.Definition{}, errors.New("workspace analysis operation tool is not exact")
	}
	canonical, err := workspaceAnalysisBuiltinDefinition(ref)
	if err != nil {
		return domain.Definition{}, err
	}
	candidate, err := domain.CanonicalizeDefinition(input)
	if err != nil || candidate.DefinitionHash != input.DefinitionHash || candidate.DefinitionHash != canonical.DefinitionHash {
		return domain.Definition{}, errors.New("workspace analysis tool definition drifted")
	}
	return canonical, nil
}

func workspaceAnalysisToolForOperation(contract agentdomain.WorkspaceAnalysisOperationContract) (domain.ToolRef, bool) {
	switch contract.Kind {
	case agentdomain.WorkspaceAnalysisOperationGitStatus:
		return workspaceAnalysisGitStatusRef, contract.Ordinal == 1
	case agentdomain.WorkspaceAnalysisOperationKnowledgeSearch:
		return workspaceAnalysisSearchRef, contract.Ordinal == 1
	case agentdomain.WorkspaceAnalysisOperationSourceRead:
		return workspaceAnalysisSourceRef, contract.Ordinal >= 1 && contract.Ordinal <= 3
	case agentdomain.WorkspaceAnalysisOperationCitationValidation:
		return workspaceAnalysisCitationRef, contract.Ordinal == 1
	default:
		return domain.ToolRef{}, false
	}
}

func sameWorkspaceAnalysisToolRequestBinding(existing, requested domain.ToolCall) bool {
	return existing.WorkspaceID == requested.WorkspaceID && existing.WorkflowRunID == requested.WorkflowRunID &&
		existing.NodeRunID == requested.NodeRunID && existing.CallNo == requested.CallNo &&
		existing.RequestedToolName == requested.RequestedToolName && sameToolRef(existing.Tool, requested.Tool) &&
		existing.DefinitionHash == requested.DefinitionHash && sameSchemaRef(existing.InputSchema, requested.InputSchema) &&
		sameSchemaRef(existing.OutputSchema, requested.OutputSchema) && existing.Capability == requested.Capability &&
		existing.SideEffectLevel == requested.SideEffectLevel && existing.InvocationPolicy == requested.InvocationPolicy &&
		existing.IdempotencyKey == requested.IdempotencyKey && existing.RequestHash == requested.RequestHash &&
		existing.RequestBytes == requested.RequestBytes && jsonEqual(existing.RequestSummary, requested.RequestSummary)
}

func validateExistingWorkspaceAnalysisToolBinding(locked workspaceAnalysisOperationLocks) error {
	if locked.call == nil || locked.reservation.id == "" {
		return consistency(errors.New("workspace analysis tool operation closure is incomplete"))
	}
	return validateReceiptCompletionBindings(receiptCompletionLocks{
		analysisRun: locked.analysisRun,
		operation:   locked.operation,
		reservation: locked.reservation,
		call:        *locked.call,
	}, *locked.call)
}

func workspaceAnalysisCatalogHash() string {
	snapshot, err := catalog.WorkspaceAnalysisToolCatalogSnapshot()
	if err != nil {
		return ""
	}
	return snapshot.Hash
}

func lockWorkspaceAnalysisOperation(ctx context.Context, tx pgx.Tx, identity domain.TrustedExecutionIdentity, key agentdomain.WorkspaceAnalysisOperationKey, candidateOperationID foundation.ID, call domain.ToolCall, loadPolicy bool) (workspaceAnalysisOperationLocks, error) {
	var locked workspaceAnalysisOperationLocks
	var definitionID string
	if err := tx.QueryRow(ctx, `SELECT definition_id::text,status,cancel_requested_at,clock_timestamp() FROM workflow.run WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, string(identity.WorkflowRunID), string(identity.WorkspaceID)).Scan(&definitionID, &locked.fence.workflowStatus, &locked.cancelledAt, &locked.fence.databaseNow); err != nil {
		return locked, classifyReceiptLockError(err)
	}
	locked.fence.workflowDefinitionID = foundation.ID(definitionID)
	if err := tx.QueryRow(ctx, `SELECT node_key,status,attempt,lease_owner,lease_until FROM workflow.node_run WHERE id=$1 AND run_id=$2 FOR UPDATE`, string(identity.NodeRunID), string(identity.WorkflowRunID)).Scan(&locked.fence.nodeKey, &locked.fence.nodeStatus, &locked.fence.nodeAttempt, &locked.fence.nodeOwner, &locked.fence.nodeLease); err != nil {
		return locked, classifyReceiptLockError(err)
	}
	if err := tx.QueryRow(ctx, `SELECT status,attempt_no,lease_owner,lease_until FROM workflow.node_attempt WHERE id=$1 AND node_run_id=$2 FOR UPDATE`, string(identity.NodeAttemptID), string(identity.NodeRunID)).Scan(&locked.fence.attemptStatus, &locked.fence.attemptNo, &locked.fence.attemptOwner, &locked.fence.attemptLease); err != nil {
		return locked, classifyReceiptLockError(err)
	}
	if loadPolicy {
		policy, err := loadPersistedPolicy(ctx, tx, identity)
		if err != nil {
			return locked, err
		}
		locked.policy = policy
	}
	var analysisID string
	if err := tx.QueryRow(ctx, `SELECT id::text,status,definition_version,definition_hash,version,deadline_at,tool_catalog_hash FROM agent.workspace_analysis_run WHERE workspace_id=$1 AND workflow_run_id=$2 FOR UPDATE`, string(identity.WorkspaceID), string(identity.WorkflowRunID)).Scan(&analysisID, &locked.analysisRun.status, &locked.analysisRun.definitionVersion, &locked.analysisRun.definitionHash, &locked.analysisRun.version, &locked.deadlineAt, &locked.catalogHash); err != nil {
		return locked, classifyReceiptLockError(err)
	}
	locked.analysisRun.id = foundation.ID(analysisID)
	if locked.analysisRun.id != key.AnalysisRunID {
		return locked, stale(errors.New("workspace analysis operation run differs"))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_operation(
		id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,ordinal,call_kind,request_hash,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'TOOL',$9,'PENDING',1,clock_timestamp(),clock_timestamp()) ON CONFLICT (analysis_run_id,node_key,operation_kind,ordinal) DO NOTHING`,
		string(candidateOperationID), string(identity.WorkspaceID), string(key.AnalysisRunID), string(identity.WorkflowRunID), string(identity.NodeRunID), string(key.NodeKey), string(key.Kind), key.Ordinal, call.RequestHash,
	); err != nil {
		return locked, classify(err)
	}
	var operationID, workspaceID, analysisRunID, workflowRunID, nodeRunID string
	if err := tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,analysis_run_id::text,workflow_run_id::text,node_run_id::text,node_key,call_kind,request_hash,status,first_node_attempt_id::text,latest_node_attempt_id::text,tool_call_id::text,result_kind,result_id::text,result_hash,error_code,version FROM agent.workspace_analysis_operation WHERE analysis_run_id=$1 AND node_key=$2 AND operation_kind=$3 AND ordinal=$4 FOR UPDATE`, string(key.AnalysisRunID), string(key.NodeKey), string(key.Kind), key.Ordinal).Scan(&operationID, &workspaceID, &analysisRunID, &workflowRunID, &nodeRunID, &locked.operation.nodeKey, &locked.operation.callKind, &locked.operation.requestHash, &locked.operation.status, &locked.operation.firstAttemptID, &locked.operation.latestAttemptID, &locked.operation.toolCallID, &locked.operation.resultKind, &locked.operation.resultID, &locked.operation.resultHash, &locked.operation.errorCode, &locked.operation.version); err != nil {
		return locked, classifyReceiptLockError(err)
	}
	locked.operation.id, locked.operation.workspaceID, locked.operation.analysisRunID = foundation.ID(operationID), foundation.ID(workspaceID), foundation.ID(analysisRunID)
	locked.operation.workflowRunID, locked.operation.nodeRunID = foundation.ID(workflowRunID), foundation.ID(nodeRunID)
	var reservationID, reservationWorkspaceID, reservationAnalysisID, reservationOperationID string
	err := tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,analysis_run_id::text,operation_id::text,call_kind,tool_call_id::text,status,reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens FROM agent.workspace_analysis_budget_reservation WHERE operation_id=$1 FOR UPDATE`, operationID).Scan(&reservationID, &reservationWorkspaceID, &reservationAnalysisID, &reservationOperationID, &locked.reservation.callKind, &locked.reservation.toolCallID, &locked.reservation.status, &locked.reservation.reservedModelCalls, &locked.reservation.reservedToolCalls, &locked.reservation.reservedSourceReads, &locked.reservation.reservedInputTokens, &locked.reservation.reservedOutputTokens)
	if err != nil && !noRows(err) {
		return locked, classify(err)
	}
	if err == nil {
		locked.reservation.id, locked.reservation.workspaceID, locked.reservation.analysisRunID, locked.reservation.operationID = foundation.ID(reservationID), foundation.ID(reservationWorkspaceID), foundation.ID(reservationAnalysisID), foundation.ID(reservationOperationID)
	}
	if locked.operation.toolCallID != nil {
		lockedCall, err := scanToolCall(tx.QueryRow(ctx, `SELECT `+toolCallColumns+` FROM workflow.tool_call WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, *locked.operation.toolCallID, string(identity.WorkspaceID)))
		if err != nil {
			return locked, classifyReceiptLockError(err)
		}
		locked.call = &lockedCall
	}
	locked.fence.databaseNow = locked.fence.databaseNow.UTC()
	locked.deadlineAt = locked.deadlineAt.UTC()
	return locked, nil
}

func validateWorkspaceAnalysisAuthorizationFence(locked workspaceAnalysisOperationLocks, identity domain.TrustedExecutionIdentity, key agentdomain.WorkspaceAnalysisOperationKey) error {
	if locked.cancelledAt != nil || locked.fence.workflowStatus != "running" || locked.analysisRun.status != "queued" && locked.analysisRun.status != "running" ||
		locked.fence.workflowDefinitionID != identity.DefinitionID || locked.analysisRun.definitionVersion != identity.DefinitionVersion || locked.analysisRun.definitionHash != identity.DefinitionHash ||
		locked.fence.nodeKey != identity.NodeKey || locked.operation.nodeKey != identity.NodeKey || locked.operation.workspaceID != identity.WorkspaceID ||
		locked.operation.analysisRunID != key.AnalysisRunID || locked.operation.workflowRunID != identity.WorkflowRunID || locked.operation.nodeRunID != identity.NodeRunID ||
		locked.fence.nodeStatus != "running" || locked.fence.attemptStatus != "running" || locked.fence.nodeAttempt != identity.LeaseFence || locked.fence.attemptNo != identity.LeaseFence ||
		!canonicalActiveLease(locked.fence.nodeOwner, locked.fence.nodeLease, locked.fence.attemptOwner, locked.fence.attemptLease, identity.LeaseOwner, locked.fence.databaseNow) {
		return stale(errors.New("workspace analysis authorization fence is stale"))
	}
	return nil
}

func authorizePendingWorkspaceAnalysisToolCall(ctx context.Context, tx pgx.Tx, locked workspaceAnalysisOperationLocks, command application.AuthorizeWorkspaceAnalysisToolCallCommand, call domain.ToolCall, definition domain.Definition, contract agentdomain.WorkspaceAnalysisOperationContract) (application.WorkspaceAnalysisToolAuthorizationResult, error) {
	if locked.operation.requestHash != call.RequestHash || locked.call != nil || locked.reservation.id != "" {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, idempotencyConflict(errors.New("pending workspace analysis operation binding differs"))
	}
	if err := validateWorkspaceAnalysisToolAdmission(ctx, tx, locked, contract, call); err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, err
	}
	created, err := scanToolCall(tx.QueryRow(ctx, `INSERT INTO workflow.tool_call(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,idempotency_key,request_hash,request_bytes,request_summary,status,retryable,version,started_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,NULL,$17,$18,$19,'STARTED',false,1,clock_timestamp()) RETURNING `+toolCallColumns,
		string(call.ID), string(call.WorkspaceID), string(call.WorkflowRunID), string(call.NodeRunID), string(call.NodeAttemptID), call.CallNo, call.RequestedToolName, call.Tool.Version, definition.DefinitionHash, call.InputSchema.ID, call.InputSchema.Version, call.OutputSchema.ID, call.OutputSchema.Version, string(call.Capability), string(call.SideEffectLevel), string(call.InvocationPolicy), call.RequestHash, call.RequestBytes, call.RequestSummary,
	))
	if err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(err)
	}
	reservedSourceReads := 0
	if contract.Kind == agentdomain.WorkspaceAnalysisOperationSourceRead {
		reservedSourceReads = 1
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET status='running',reserved_tool_calls=reserved_tool_calls+1,reserved_source_reads=reserved_source_reads+$1,version=version+1,updated_at=GREATEST(updated_at,clock_timestamp()) WHERE id=$2 AND version=$3 AND status IN ('queued','running')`, reservedSourceReads, string(locked.analysisRun.id), locked.analysisRun.version)
	if err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, receiptConflict(errors.New("workspace analysis run authorization CAS failed"))
	}
	tag, err = tx.Exec(ctx, `UPDATE agent.workspace_analysis_operation SET
		status='STARTED',first_node_attempt_id=$1,latest_node_attempt_id=$1,tool_call_id=$2,
		version=version+1,updated_at=GREATEST(updated_at,authorized.at),started_at=authorized.at
		FROM (SELECT clock_timestamp() AS at) AS authorized
		WHERE id=$3 AND status='PENDING' AND version=$4`, string(command.Identity.NodeAttemptID), string(created.ID), string(locked.operation.id), locked.operation.version)
	if err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, receiptConflict(errors.New("workspace analysis operation authorization CAS failed"))
	}
	tag, err = tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_budget_reservation(
		id,workspace_id,analysis_run_id,operation_id,call_kind,tool_call_id,status,reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,reserved_cost_microunits,created_at
	) VALUES($1,$2,$3,$4,'TOOL',$5,'RESERVED',0,1,$6,0,0,NULL,clock_timestamp())`, string(command.ReservationID), string(call.WorkspaceID), string(locked.analysisRun.id), string(locked.operation.id), string(created.ID), reservedSourceReads)
	if err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, receiptConflict(errors.New("workspace analysis reservation insert did not persist"))
	}
	return workspaceAnalysisAuthorizationResult(created, locked.operation.id, command.ReservationID, application.WorkspaceAnalysisToolAuthorizationCreated), nil
}

func validateWorkspaceAnalysisToolAdmission(ctx context.Context, tx pgx.Tx, locked workspaceAnalysisOperationLocks, contract agentdomain.WorkspaceAnalysisOperationContract, call domain.ToolCall) error {
	var deadlineExceeded, toolBudgetAvailable, sourceReadBudgetAvailable, concurrencyAvailable bool
	sourceReads := 0
	if contract.Kind == agentdomain.WorkspaceAnalysisOperationSourceRead {
		sourceReads = 1
	}
	err := tx.QueryRow(ctx, `SELECT
		clock_timestamp()+(CASE $2
			WHEN 'GIT_STATUS' THEN git_tool_timeout_ms
			WHEN 'KNOWLEDGE_SEARCH' THEN search_tool_timeout_ms
			WHEN 'SOURCE_READ' THEN source_read_tool_timeout_ms
			WHEN 'CITATION_VALIDATION' THEN validate_citation_tool_timeout_ms
			END+durable_completion_margin_ms)*interval '1 millisecond'>deadline_at,
			reserved_tool_calls+settled_tool_calls+1<=max_tool_calls,
			reserved_source_reads+settled_source_reads+$3<=max_source_reads,
			NOT EXISTS(SELECT 1 FROM workflow.tool_call WHERE workflow_run_id=$4 AND status='STARTED')
			FROM agent.workspace_analysis_run WHERE id=$1`,
		string(locked.analysisRun.id), string(contract.Kind), sourceReads, string(call.WorkflowRunID),
	).Scan(
		&deadlineExceeded, &toolBudgetAvailable, &sourceReadBudgetAvailable, &concurrencyAvailable,
	)
	if err != nil {
		return classify(err)
	}
	return workspaceAnalysisToolAdmissionError(
		deadlineExceeded, toolBudgetAvailable, sourceReadBudgetAvailable, concurrencyAvailable,
	)
}

func workspaceAnalysisToolAdmissionError(deadlineExceeded, toolBudgetAvailable, sourceReadBudgetAvailable, concurrencyAvailable bool) error {
	if !toolBudgetAvailable || !sourceReadBudgetAvailable {
		return stale(errors.New("workspace analysis tool budget is exhausted"))
	}
	if !concurrencyAvailable {
		return stale(errors.New("workspace analysis tool concurrency is exhausted"))
	}
	if deadlineExceeded {
		return agentdomain.NewWorkspaceAnalysisPreAuthorizationDeadlineError()
	}
	return nil
}

func reducePriorWorkspaceAnalysisToolCallToUnknown(ctx context.Context, tx pgx.Tx, locked workspaceAnalysisOperationLocks, identity domain.TrustedExecutionIdentity) (application.WorkspaceAnalysisToolAuthorizationResult, error) {
	if locked.call == nil || locked.reservation.status != "RESERVED" || locked.call.Status != domain.CallStarted || !activeWorkspaceAnalysisReplacementFence(locked, identity) {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, stale(errors.New("workspace analysis replacement attempt is stale"))
	}
	unknown := *locked.call
	unknown.Status, unknown.ErrorCode, unknown.Retryable, unknown.Version = domain.CallUnknown, "WORKSPACE_ANALYSIS_RESULT_UNKNOWN", false, unknown.Version+1
	mutation, err := finalizeCallTx(ctx, tx, locked.call.Version, unknown)
	if err != nil || mutation.Call.CompletedAt == nil {
		if err == nil {
			err = errors.New("workspace analysis old call did not complete")
		}
		return application.WorkspaceAnalysisToolAuthorizationResult{}, err
	}
	receiptLocks := receiptCompletionLocks{analysisRun: locked.analysisRun, operation: locked.operation, reservation: locked.reservation, call: mutation.Call}
	if err := settleWorkspaceAnalysisToolReservation(ctx, tx, receiptLocks, *mutation.Call.CompletedAt, true); err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, err
	}
	if err := settleWorkspaceAnalysisToolRunBudget(ctx, tx, receiptLocks, *mutation.Call.CompletedAt, true); err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, err
	}
	if err := completeWorkspaceAnalysisToolOperation(ctx, tx, receiptLocks, mutation.Call, *mutation.Call.CompletedAt, true, identity.NodeAttemptID); err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, err
	}
	return workspaceAnalysisAuthorizationResult(mutation.Call, locked.operation.id, locked.reservation.id, application.WorkspaceAnalysisToolAuthorizationTerminateUnknown), nil
}

func activeWorkspaceAnalysisReplacementFence(locked workspaceAnalysisOperationLocks, identity domain.TrustedExecutionIdentity) bool {
	return locked.fence.workflowStatus == "running" && locked.analysisRun.status == "running" && locked.cancelledAt == nil &&
		locked.fence.workflowDefinitionID == identity.DefinitionID && locked.analysisRun.definitionVersion == identity.DefinitionVersion && locked.analysisRun.definitionHash == identity.DefinitionHash &&
		locked.fence.nodeKey == identity.NodeKey && locked.fence.nodeStatus == "running" && locked.fence.attemptStatus == "running" &&
		locked.fence.nodeAttempt == identity.LeaseFence && locked.fence.attemptNo == identity.LeaseFence &&
		canonicalActiveLease(locked.fence.nodeOwner, locked.fence.nodeLease, locked.fence.attemptOwner, locked.fence.attemptLease, identity.LeaseOwner, locked.fence.databaseNow)
}

func settleWorkspaceAnalysisToolReservation(ctx context.Context, tx pgx.Tx, locked receiptCompletionLocks, completedAt time.Time, unknown bool) error {
	status := "SETTLED"
	if unknown {
		status = "UNKNOWN_CHARGED"
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_budget_reservation SET status=$1,settled_model_calls=0,settled_tool_calls=1,settled_source_reads=reserved_source_reads,settled_input_tokens=0,settled_output_tokens=0,settled_cost_microunits=NULL,settled_at=$2 WHERE id=$3 AND workspace_id=$4 AND analysis_run_id=$5 AND operation_id=$6 AND call_kind='TOOL' AND tool_call_id=$7 AND status='RESERVED'`, status, completedAt, string(locked.reservation.id), string(locked.reservation.workspaceID), string(locked.reservation.analysisRunID), string(locked.reservation.operationID), string(locked.call.ID))
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return receiptConflict(errors.New("workspace analysis tool reservation CAS failed"))
	}
	return nil
}

func settleWorkspaceAnalysisToolRunBudget(ctx context.Context, tx pgx.Tx, locked receiptCompletionLocks, completedAt time.Time, unknown bool) error {
	_ = unknown // Both known failures and unknown outcomes consume the reserved upper bound for a Tool call.
	r := locked.reservation
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET reserved_tool_calls=reserved_tool_calls-$1,reserved_source_reads=reserved_source_reads-$2,settled_tool_calls=settled_tool_calls+1,settled_source_reads=settled_source_reads+$2,version=version+1,updated_at=GREATEST(updated_at,$3) WHERE id=$4 AND status='running' AND version=$5 AND reserved_tool_calls>=$1 AND reserved_source_reads>=$2`, r.reservedToolCalls, r.reservedSourceReads, completedAt, string(locked.analysisRun.id), locked.analysisRun.version)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return receiptConflict(errors.New("workspace analysis tool run budget CAS failed"))
	}
	return nil
}

func completeWorkspaceAnalysisToolOperation(ctx context.Context, tx pgx.Tx, locked receiptCompletionLocks, call domain.ToolCall, completedAt time.Time, unknown bool, latestAttemptID foundation.ID) error {
	status := "FAILED"
	if unknown {
		status = "UNKNOWN"
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_operation SET status=$1,latest_node_attempt_id=$2,error_code=$3,version=version+1,updated_at=GREATEST(updated_at,$4),completed_at=GREATEST(updated_at,$4) WHERE id=$5 AND workspace_id=$6 AND analysis_run_id=$7 AND workflow_run_id=$8 AND node_run_id=$9 AND call_kind='TOOL' AND tool_call_id=$10 AND request_hash=$11 AND status='STARTED' AND version=$12`, status, string(latestAttemptID), call.ErrorCode, completedAt, string(locked.operation.id), string(locked.operation.workspaceID), string(locked.operation.analysisRunID), string(locked.operation.workflowRunID), string(locked.operation.nodeRunID), string(call.ID), locked.operation.requestHash, locked.operation.version)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return receiptConflict(errors.New("workspace analysis tool operation CAS failed"))
	}
	return nil
}

func advanceTerminalWorkspaceAnalysisOperationAttempt(ctx context.Context, tx pgx.Tx, locked workspaceAnalysisOperationLocks, attemptID foundation.ID) error {
	if stringPointerValue(locked.operation.latestAttemptID) == string(attemptID) {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_operation SET latest_node_attempt_id=$1,version=version+1,updated_at=GREATEST(updated_at,clock_timestamp()) WHERE id=$2 AND status IN ('SUCCEEDED','FAILED','UNKNOWN') AND version=$3`, string(attemptID), string(locked.operation.id), locked.operation.version)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return receiptConflict(errors.New("workspace analysis terminal replay attempt CAS failed"))
	}
	return nil
}

func workspaceAnalysisAuthorizationResult(call domain.ToolCall, operationID, reservationID foundation.ID, disposition application.WorkspaceAnalysisToolAuthorizationDisposition) application.WorkspaceAnalysisToolAuthorizationResult {
	return application.WorkspaceAnalysisToolAuthorizationResult{Call: call, OperationID: operationID, ReservationID: reservationID, Disposition: disposition}
}

func (repository *Repository) recoverWorkspaceAnalysisAuthorizationAfterCommitError(ctx context.Context, command application.AuthorizeWorkspaceAnalysisToolCallCommand, requested domain.ToolCall, commitErr error) (application.WorkspaceAnalysisToolAuthorizationResult, error) {
	tx, err := repository.begin(ctx)
	if err == nil {
		defer func() { _ = tx.Rollback(ctx) }()
		locked, lockErr := lockWorkspaceAnalysisOperation(ctx, tx, command.Identity, command.OperationKey, command.OperationID, requested, false)
		if lockErr == nil && locked.call != nil && locked.reservation.id != "" && locked.operation.requestHash == requested.RequestHash {
			disposition := application.WorkspaceAnalysisToolAuthorizationReconcile
			switch locked.operation.status {
			case "SUCCEEDED":
				disposition = application.WorkspaceAnalysisToolAuthorizationReuseResult
			case "FAILED":
				disposition = application.WorkspaceAnalysisToolAuthorizationReplayFailure
			case "UNKNOWN":
				disposition = application.WorkspaceAnalysisToolAuthorizationTerminateUnknown
			}
			result := workspaceAnalysisAuthorizationResult(*locked.call, locked.operation.id, locked.reservation.id, disposition)
			if locked.operation.status == "FAILED" && locked.call.Status == domain.CallSucceeded &&
				stringPointerValue(locked.operation.errorCode) == "WORKSPACE_ANALYSIS_RECEIPT_INVALID" {
				failure, failureErr := loadResultReceiptFailure(
					ctx, tx, *locked.call, command.Definition, locked.analysisRun.id, locked.operation.id,
				)
				if failureErr != nil {
					return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(commitErr)
				}
				result.ReceiptFailure = &failure
			}
			if !sameWorkspaceAnalysisToolRequestBinding(*locked.call, requested) {
				return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(commitErr)
			}
			if eventErr := repository.verifyRecoveredWorkspaceAnalysisToolEvent(ctx, tx, locked); eventErr != nil {
				return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(commitErr)
			}
			if tx.Commit(ctx) == nil {
				return result, nil
			}
		}
	}
	return application.WorkspaceAnalysisToolAuthorizationResult{}, classify(commitErr)
}

func (repository *Repository) recoverWorkspaceAnalysisToolFinalizationAfterCommitError(ctx context.Context, requested domain.ToolCall, commitErr error) (application.ToolCallMutationResult, error) {
	tx, err := repository.begin(ctx)
	if err == nil {
		defer func() { _ = tx.Rollback(ctx) }()
		locked, lockErr := lockReceiptCompletion(ctx, tx, requested)
		if lockErr == nil && sameTerminalResult(locked.call, requested) &&
			((requested.Status == domain.CallFailed && locked.operation.status == "FAILED" && locked.reservation.status == "SETTLED") ||
				(requested.Status == domain.CallUnknown && locked.operation.status == "UNKNOWN" && locked.reservation.status == "UNKNOWN_CHARGED")) {
			status, statusErr := workspaceAnalysisToolTerminalEventStatus(locked.call)
			if statusErr == nil {
				statusErr = repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, locked.call, status, true)
			}
			if statusErr == nil && tx.Commit(ctx) == nil {
				return application.ToolCallMutationResult{Call: locked.call, Replayed: true}, nil
			}
		}
	}
	return application.ToolCallMutationResult{}, classify(commitErr)
}

func (repository *Repository) verifyRecoveredWorkspaceAnalysisToolEvent(ctx context.Context, tx pgx.Tx, locked workspaceAnalysisOperationLocks) error {
	if locked.call == nil {
		return consistency(errors.New("workspace analysis recovered event call is missing"))
	}
	switch locked.operation.status {
	case "STARTED":
		return repository.appendWorkspaceAnalysisToolRequested(ctx, tx, locked.operation, *locked.call, true)
	case "SUCCEEDED", "FAILED", "UNKNOWN":
		status := "failed"
		if locked.operation.status == "SUCCEEDED" {
			status = "succeeded"
		} else if locked.operation.status == "UNKNOWN" {
			status = "unknown"
		}
		return repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, *locked.call, status, true)
	default:
		return consistency(errors.New("workspace analysis recovered event operation status is unsupported"))
	}
}

var _ application.WorkspaceAnalysisToolOperationRepository = (*Repository)(nil)
