package postgres

import (
	"context"
	"errors"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"gorm.io/gorm"
)

func (repository *GORMWorkspaceAnalysisRepository) AuthorizeWorkspaceAnalysisToolCall(ctx context.Context, command application.AuthorizeWorkspaceAnalysisToolCallCommand) (application.WorkspaceAnalysisToolAuthorizationResult, error) {
	call, definition, contract, err := validateWorkspaceAnalysisToolAuthorization(command)
	if err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, err
	}
	arguments, err := workspaceAnalysisAuthorizedArguments(command)
	if err != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, err
	}
	var result application.WorkspaceAnalysisToolAuthorizationResult
	var denial *agentapplication.WorkspaceAnalysisAdmissionDenial
	callbackCompleted := false
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		fence, identity, err := repository.lockExecution(callbackCtx, scope, command.Identity)
		if err != nil {
			return err
		}
		policy, err := gormWorkspaceAnalysisPolicyFromFence(fence, command.Identity)
		if err != nil {
			return err
		}
		if err := validateStartPolicy(policy, application.StartCallCommand{
			Identity: command.Identity, Call: call, AllowedWorkflows: definition.AllowedWorkflows,
		}); err != nil {
			return err
		}
		snapshot, err := repository.participant.PrepareWorkspaceAnalysisToolOperationScoped(callbackCtx, scope, agentapplication.PrepareWorkspaceAnalysisToolOperationCommand{Identity: identity, OperationKey: command.OperationKey, CandidateOperationID: command.OperationID, RequestHash: call.RequestHash, ExpectedToolCatalogHash: workspaceAnalysisCatalogHashForVersion(command.Identity.DefinitionVersion), Arguments: arguments})
		if err != nil {
			return err
		}
		if snapshot.Operation.RequestHash != call.RequestHash {
			return idempotencyConflict(errors.New("Workspace Analysis operation request differs"))
		}
		switch snapshot.Operation.Status {
		case agentdomain.WorkspaceAnalysisOperationPending:
			if snapshot.Run.DefinitionVersion == 2 {
				denial, err = repository.dynamicToolAdmissionDenial(callbackCtx, scope, snapshot, definition)
				if err != nil {
					return err
				}
				if denial != nil {
					if err := gormToolsExec(database, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
						return classifyGORMTools(callbackCtx, err)
					}
					callbackCompleted = true
					return nil
				}
			}
			if err := gormEnsureNoActiveWorkspaceAnalysisToolCall(callbackCtx, database, call.WorkflowRunID); err != nil {
				return err
			}
			created, err := gormInsertWorkspaceAnalysisStartedCall(callbackCtx, database, call)
			if err != nil {
				return err
			}
			reserved, err := repository.participant.ReserveWorkspaceAnalysisToolOperationScoped(callbackCtx, scope, agentapplication.ReserveWorkspaceAnalysisToolOperationCommand{Identity: identity, OperationKey: command.OperationKey, OperationID: snapshot.Operation.ID, CandidateReservationID: command.ReservationID, ToolCallID: created.ID, RequestHash: created.RequestHash, ExpectedRunVersion: snapshot.Run.Version, ExpectedOperationVersion: snapshot.Operation.Version})
			if err != nil {
				return err
			}
			if err := repository.appendRequested(callbackCtx, scope, reserved.Operation, created, false); err != nil {
				return err
			}
			if err := gormToolsExec(database, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
				return classifyGORMTools(callbackCtx, err)
			}
			result = application.WorkspaceAnalysisToolAuthorizationResult{Call: created, OperationID: reserved.Operation.ID, ReservationID: *reserved.Operation.BudgetReservationID, Disposition: application.WorkspaceAnalysisToolAuthorizationCreated}
			callbackCompleted = true
			return nil
		case agentdomain.WorkspaceAnalysisOperationStarted:
			if snapshot.Reservation == nil || snapshot.Operation.Call == nil {
				return consistency(errors.New("Workspace Analysis started operation is incomplete"))
			}
			locked, err := gormLockWorkspaceAnalysisToolCall(callbackCtx, database, call.WorkspaceID, snapshot.Operation.Call.ID)
			if err != nil {
				return err
			}
			if !sameWorkspaceAnalysisToolRequestBinding(locked, call) {
				return idempotencyConflict(errors.New("Workspace Analysis started Call differs"))
			}
			if snapshot.Operation.LatestNodeAttemptID != nil && *snapshot.Operation.LatestNodeAttemptID == command.Identity.NodeAttemptID {
				if err := repository.appendRequested(callbackCtx, scope, snapshot.Operation, locked, true); err != nil {
					return err
				}
				result = application.WorkspaceAnalysisToolAuthorizationResult{Call: locked, OperationID: snapshot.Operation.ID, ReservationID: snapshot.Reservation.ID, Disposition: application.WorkspaceAnalysisToolAuthorizationReconcile}
				callbackCompleted = true
				return nil
			}
			if locked.Status != domain.CallStarted {
				return consistency(errors.New("Workspace Analysis operation and Tool Call diverged"))
			}
			unknown := locked
			unknown.Status, unknown.ErrorCode, unknown.Retryable = domain.CallUnknown, string(agentdomain.WorkspaceAnalysisRunResultUnknown), false
			closed, err := gormFinalizeWorkspaceAnalysisCall(callbackCtx, database, unknown, locked.Version)
			if err != nil {
				return err
			}
			settled, err := repository.participant.SettleWorkspaceAnalysisToolOperationScoped(callbackCtx, scope, gormSettlementCommand(identity, snapshot, closed, agentapplication.WorkspaceAnalysisToolSettlementUnknown, nil, string(agentdomain.WorkspaceAnalysisRunResultUnknown)))
			if err != nil {
				return err
			}
			if err := repository.appendCompleted(callbackCtx, scope, settled.Operation, closed, "unknown", false); err != nil {
				return err
			}
			if err := gormToolsExec(database, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
				return classifyGORMTools(callbackCtx, err)
			}
			result = application.WorkspaceAnalysisToolAuthorizationResult{Call: closed, OperationID: settled.Operation.ID, ReservationID: snapshot.Reservation.ID, Disposition: application.WorkspaceAnalysisToolAuthorizationTerminateUnknown}
			callbackCompleted = true
			return nil
		case agentdomain.WorkspaceAnalysisOperationSucceeded, agentdomain.WorkspaceAnalysisOperationFailed, agentdomain.WorkspaceAnalysisOperationUnknown:
			if snapshot.Reservation == nil || snapshot.Operation.Call == nil {
				return consistency(errors.New("Workspace Analysis terminal operation is incomplete"))
			}
			locked, err := gormLockWorkspaceAnalysisToolCall(callbackCtx, database, call.WorkspaceID, snapshot.Operation.Call.ID)
			if err != nil {
				return err
			}
			if !sameWorkspaceAnalysisToolRequestBinding(locked, call) {
				return idempotencyConflict(errors.New("Workspace Analysis terminal Call differs"))
			}
			switch snapshot.Operation.Status {
			case agentdomain.WorkspaceAnalysisOperationSucceeded:
				if locked.Status != domain.CallSucceeded {
					return consistency(errors.New("Workspace Analysis successful closure has a non-successful Call"))
				}
			case agentdomain.WorkspaceAnalysisOperationFailed:
				ordinaryFailure := locked.Status == domain.CallFailed && snapshot.Operation.ErrorCode != string(agentdomain.WorkspaceAnalysisRunReceiptInvalid)
				receiptFailure := locked.Status == domain.CallSucceeded && snapshot.Operation.ErrorCode == string(agentdomain.WorkspaceAnalysisRunReceiptInvalid)
				if !ordinaryFailure && !receiptFailure {
					return consistency(errors.New("Workspace Analysis failed closure differs"))
				}
			case agentdomain.WorkspaceAnalysisOperationUnknown:
				if locked.Status != domain.CallUnknown {
					return consistency(errors.New("Workspace Analysis unknown closure has a non-unknown Call"))
				}
			}
			if snapshot.Operation.LatestNodeAttemptID == nil || *snapshot.Operation.LatestNodeAttemptID != command.Identity.NodeAttemptID {
				advanced, advanceErr := repository.participant.AdvanceWorkspaceAnalysisToolOperationAttemptScoped(callbackCtx, scope, agentapplication.AdvanceWorkspaceAnalysisToolOperationAttemptCommand{Identity: identity, OperationKey: command.OperationKey, OperationID: snapshot.Operation.ID, ToolCallID: locked.ID, ExpectedOperationVersion: snapshot.Operation.Version})
				if advanceErr != nil {
					return advanceErr
				}
				snapshot.Operation = advanced
			}
			status, statusErr := gormWorkspaceAnalysisTerminalEventStatus(locked)
			if statusErr != nil {
				return statusErr
			}
			if err := repository.appendCompleted(callbackCtx, scope, snapshot.Operation, locked, status, true); err != nil {
				return err
			}
			disposition := application.WorkspaceAnalysisToolAuthorizationReplayFailure
			if snapshot.Operation.Status == agentdomain.WorkspaceAnalysisOperationSucceeded {
				disposition = application.WorkspaceAnalysisToolAuthorizationReuseResult
			}
			if snapshot.Operation.Status == agentdomain.WorkspaceAnalysisOperationUnknown {
				disposition = application.WorkspaceAnalysisToolAuthorizationTerminateUnknown
			}
			result = application.WorkspaceAnalysisToolAuthorizationResult{Call: locked, OperationID: snapshot.Operation.ID, ReservationID: snapshot.Reservation.ID, Disposition: disposition}
			if snapshot.Operation.Status == agentdomain.WorkspaceAnalysisOperationFailed &&
				locked.Status == domain.CallSucceeded &&
				snapshot.Operation.ErrorCode == string(agentdomain.WorkspaceAnalysisRunReceiptInvalid) {
				failure, loadErr := gormLoadResultReceiptFailure(callbackCtx, database, locked, definition, snapshot.Operation.AnalysisRunID, snapshot.Operation.ID)
				if loadErr != nil {
					return loadErr
				}
				result.ReceiptFailure = &failure
			}
			callbackCompleted = true
			return nil
		default:
			return consistency(errors.New("Workspace Analysis operation status is unsupported"))
		}
	})
	if err != nil {
		if callbackCompleted && gormToolsCommitFailure(err) {
			if denial != nil {
				return application.WorkspaceAnalysisToolAuthorizationResult{}, repository.recoverDynamicToolAdmissionDenial(ctx, command, call, definition, arguments, denial, err)
			}
			return repository.gormRecoverWorkspaceAnalysisAuthorizationAfterCommitError(ctx, command, call, result, err)
		}
		return application.WorkspaceAnalysisToolAuthorizationResult{}, classifyGORMTools(ctx, err)
	}
	_ = contract
	if denial != nil {
		return application.WorkspaceAnalysisToolAuthorizationResult{}, denial
	}
	return result, nil
}

func (repository *GORMWorkspaceAnalysisRepository) FinalizeWorkspaceAnalysisToolCall(ctx context.Context, command application.FinalizeWorkspaceAnalysisToolCallCommand) (application.ToolCallMutationResult, error) {
	call, err := validateWorkspaceAnalysisToolFinalization(command)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	var result application.ToolCallMutationResult
	var closureQuery agentapplication.WorkspaceAnalysisToolClosureQuery
	callbackCompleted := false
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		_, identity, err := repository.lockExecution(callbackCtx, scope, command.Identity)
		if err != nil {
			return err
		}
		snapshot, found, err := repository.participant.LockWorkspaceAnalysisToolOperationByCallScoped(callbackCtx, scope, agentapplication.WorkspaceAnalysisToolCallLockQuery{Identity: identity, ToolCallID: call.ID, RequestHash: call.RequestHash})
		if err != nil {
			return err
		}
		if !found || snapshot.Reservation == nil {
			return receiptNotFound(errors.New("Workspace Analysis Tool operation is absent"))
		}
		locked, err := gormLockWorkspaceAnalysisToolCall(callbackCtx, database, call.WorkspaceID, call.ID)
		if err != nil {
			return err
		}
		if !sameStartBinding(locked, call, true) {
			return receiptConflict(errors.New("Workspace Analysis finalization binding differs"))
		}
		closureQuery = gormWorkspaceAnalysisToolClosureQuery(snapshot, locked)
		if snapshot.Operation.Status == agentdomain.WorkspaceAnalysisOperationFailed || snapshot.Operation.Status == agentdomain.WorkspaceAnalysisOperationUnknown {
			if !sameTerminalResult(locked, call) {
				return receiptConflict(errors.New("Workspace Analysis terminal replay differs"))
			}
			status, _ := gormWorkspaceAnalysisTerminalEventStatus(locked)
			if err := repository.appendCompleted(callbackCtx, scope, snapshot.Operation, locked, status, true); err != nil {
				return err
			}
			result = application.ToolCallMutationResult{Call: locked, Replayed: true}
			callbackCompleted = true
			return nil
		}
		if snapshot.Operation.Status != agentdomain.WorkspaceAnalysisOperationStarted || snapshot.Reservation.Status != agentdomain.WorkspaceAnalysisBudgetReserved || locked.Status != domain.CallStarted {
			return receiptConflict(errors.New("Workspace Analysis finalization is not startable"))
		}
		completed, err := gormFinalizeWorkspaceAnalysisCall(callbackCtx, database, call, command.ExpectedVersion)
		if err != nil {
			return err
		}
		kind, code := agentapplication.WorkspaceAnalysisToolSettlementFailed, completed.ErrorCode
		if completed.Status == domain.CallUnknown {
			kind, code = agentapplication.WorkspaceAnalysisToolSettlementUnknown, string(agentdomain.WorkspaceAnalysisRunResultUnknown)
		}
		settled, err := repository.participant.SettleWorkspaceAnalysisToolOperationScoped(callbackCtx, scope, gormSettlementCommand(identity, snapshot, completed, kind, nil, code))
		if err != nil {
			return err
		}
		status, err := gormWorkspaceAnalysisTerminalEventStatus(completed)
		if err != nil {
			return err
		}
		if err := repository.appendCompleted(callbackCtx, scope, settled.Operation, completed, status, false); err != nil {
			return err
		}
		if err := gormToolsExec(database, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return classifyGORMTools(callbackCtx, err)
		}
		result = application.ToolCallMutationResult{Call: completed}
		callbackCompleted = true
		return nil
	})
	if err != nil {
		if callbackCompleted && gormToolsCommitFailure(err) {
			return repository.gormRecoverWorkspaceAnalysisToolFinalizationAfterCommitError(ctx, call, closureQuery, err)
		}
		return application.ToolCallMutationResult{}, classifyGORMTools(ctx, err)
	}
	return result, nil
}

func gormWorkspaceAnalysisToolClosureQuery(
	snapshot agentapplication.WorkspaceAnalysisToolOperationSnapshot,
	call domain.ToolCall,
) agentapplication.WorkspaceAnalysisToolClosureQuery {
	query := agentapplication.WorkspaceAnalysisToolClosureQuery{
		WorkspaceID: call.WorkspaceID, WorkflowRunID: call.WorkflowRunID,
		AnalysisRunID: snapshot.Operation.AnalysisRunID, OperationKey: snapshot.Operation.LogicalKey(),
		OperationID: snapshot.Operation.ID, ToolCallID: call.ID, RequestHash: call.RequestHash,
	}
	if snapshot.Reservation != nil {
		query.ReservationID = snapshot.Reservation.ID
	}
	return query
}

func (repository *GORMWorkspaceAnalysisRepository) gormVerifyWorkspaceAnalysisToolClosure(
	ctx context.Context,
	database *gorm.DB,
	scope foundation.TransactionScope,
	query agentapplication.WorkspaceAnalysisToolClosureQuery,
	requested domain.ToolCall,
) (agentapplication.WorkspaceAnalysisToolClosure, domain.ToolCall, error) {
	closure, found, err := repository.participant.VerifyWorkspaceAnalysisToolClosureScoped(ctx, scope, query)
	if err != nil {
		return agentapplication.WorkspaceAnalysisToolClosure{}, domain.ToolCall{}, classifyGORMTools(ctx, err)
	}
	if !found {
		return agentapplication.WorkspaceAnalysisToolClosure{}, domain.ToolCall{}, consistency(errors.New("Workspace Analysis durable closure was not found"))
	}
	call, err := gormLoadCallByID(ctx, database, requested.WorkspaceID, query.ToolCallID)
	if gormToolsNoRows(err) {
		return agentapplication.WorkspaceAnalysisToolClosure{}, domain.ToolCall{}, receiptNotFound(err)
	}
	if err != nil {
		return agentapplication.WorkspaceAnalysisToolClosure{}, domain.ToolCall{}, classifyGORMTools(ctx, err)
	}
	if !sameWorkspaceAnalysisToolRequestBinding(call, requested) || call.ID != query.ToolCallID ||
		closure.Operation.ID != query.OperationID || closure.Reservation.ID != query.ReservationID {
		return agentapplication.WorkspaceAnalysisToolClosure{}, domain.ToolCall{}, consistency(errors.New("Workspace Analysis durable closure binding differs"))
	}
	return closure, call, nil
}

func (repository *GORMWorkspaceAnalysisRepository) gormRecoverWorkspaceAnalysisAuthorizationAfterCommitError(
	ctx context.Context,
	command application.AuthorizeWorkspaceAnalysisToolCallCommand,
	requested domain.ToolCall,
	committed application.WorkspaceAnalysisToolAuthorizationResult,
	commitErr error,
) (application.WorkspaceAnalysisToolAuthorizationResult, error) {
	var result application.WorkspaceAnalysisToolAuthorizationResult
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		query := agentapplication.WorkspaceAnalysisToolClosureQuery{
			WorkspaceID: requested.WorkspaceID, WorkflowRunID: requested.WorkflowRunID,
			AnalysisRunID: command.OperationKey.AnalysisRunID, OperationKey: command.OperationKey,
			OperationID: committed.OperationID, ReservationID: committed.ReservationID,
			ToolCallID: committed.Call.ID, RequestHash: requested.RequestHash,
		}
		closure, call, err := repository.gormVerifyWorkspaceAnalysisToolClosure(callbackCtx, database, scope, query, requested)
		if err != nil {
			return err
		}
		result = application.WorkspaceAnalysisToolAuthorizationResult{
			Call: call, OperationID: closure.Operation.ID, ReservationID: closure.Reservation.ID,
		}
		switch closure.Operation.Status {
		case agentdomain.WorkspaceAnalysisOperationStarted:
			if call.Status != domain.CallStarted {
				return consistency(errors.New("Workspace Analysis recovered started closure differs"))
			}
			result.Disposition = application.WorkspaceAnalysisToolAuthorizationReconcile
			return repository.appendRequested(callbackCtx, scope, closure.Operation, call, true)
		case agentdomain.WorkspaceAnalysisOperationSucceeded:
			if call.Status != domain.CallSucceeded {
				return consistency(errors.New("Workspace Analysis recovered success closure differs"))
			}
			result.Disposition = application.WorkspaceAnalysisToolAuthorizationReuseResult
			return repository.appendCompleted(callbackCtx, scope, closure.Operation, call, "succeeded", true)
		case agentdomain.WorkspaceAnalysisOperationFailed:
			ordinaryFailure := call.Status == domain.CallFailed && closure.Operation.ErrorCode != string(agentdomain.WorkspaceAnalysisRunReceiptInvalid)
			receiptFailure := call.Status == domain.CallSucceeded && closure.Operation.ErrorCode == string(agentdomain.WorkspaceAnalysisRunReceiptInvalid)
			if !ordinaryFailure && !receiptFailure {
				return consistency(errors.New("Workspace Analysis recovered failure closure differs"))
			}
			if receiptFailure {
				failure, loadErr := gormLoadResultReceiptFailure(callbackCtx, database, call, command.Definition, closure.Operation.AnalysisRunID, closure.Operation.ID)
				if loadErr != nil {
					return loadErr
				}
				result.ReceiptFailure = &failure
			}
			result.Disposition = application.WorkspaceAnalysisToolAuthorizationReplayFailure
			return repository.appendCompleted(callbackCtx, scope, closure.Operation, call, "failed", true)
		case agentdomain.WorkspaceAnalysisOperationUnknown:
			if call.Status != domain.CallUnknown {
				return consistency(errors.New("Workspace Analysis recovered unknown closure differs"))
			}
			result.Disposition = application.WorkspaceAnalysisToolAuthorizationTerminateUnknown
			return repository.appendCompleted(callbackCtx, scope, closure.Operation, call, "unknown", true)
		default:
			return consistency(errors.New("Workspace Analysis recovered closure status is unsupported"))
		}
	})
	if err == nil {
		return result, nil
	}
	return application.WorkspaceAnalysisToolAuthorizationResult{}, classifyGORMTools(ctx, errors.Join(commitErr, err))
}

func (repository *GORMWorkspaceAnalysisRepository) gormRecoverWorkspaceAnalysisToolFinalizationAfterCommitError(
	ctx context.Context,
	requested domain.ToolCall,
	query agentapplication.WorkspaceAnalysisToolClosureQuery,
	commitErr error,
) (application.ToolCallMutationResult, error) {
	var result application.ToolCallMutationResult
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		closure, call, err := repository.gormVerifyWorkspaceAnalysisToolClosure(callbackCtx, database, scope, query, requested)
		if err != nil {
			return err
		}
		if !sameTerminalResult(call, requested) ||
			(requested.Status == domain.CallFailed && closure.Operation.Status != agentdomain.WorkspaceAnalysisOperationFailed) ||
			(requested.Status == domain.CallUnknown && closure.Operation.Status != agentdomain.WorkspaceAnalysisOperationUnknown) {
			return consistency(errors.New("Workspace Analysis recovered terminal closure differs"))
		}
		status, err := gormWorkspaceAnalysisTerminalEventStatus(call)
		if err != nil {
			return err
		}
		if err := repository.appendCompleted(callbackCtx, scope, closure.Operation, call, status, true); err != nil {
			return err
		}
		result = application.ToolCallMutationResult{Call: call, Replayed: true}
		return nil
	})
	if err == nil {
		return result, nil
	}
	return application.ToolCallMutationResult{}, classifyGORMTools(ctx, errors.Join(commitErr, err))
}

func gormEnsureNoActiveWorkspaceAnalysisToolCall(ctx context.Context, database *gorm.DB, workflowRunID foundation.ID) error {
	row, err := gormToolsRawRow(database, `SELECT EXISTS(SELECT 1 FROM workflow.tool_call WHERE workflow_run_id=? AND status='STARTED')`, string(workflowRunID))
	if err != nil {
		return classifyGORMTools(ctx, err)
	}
	var exists bool
	if err := row.Scan(&exists); err != nil {
		return classifyGORMTools(ctx, err)
	}
	if exists {
		return idempotencyConflict(errors.New("Workspace Analysis Tool concurrency is exhausted"))
	}
	return nil
}

func gormInsertWorkspaceAnalysisStartedCall(ctx context.Context, database *gorm.DB, call domain.ToolCall) (domain.ToolCall, error) {
	row, err := gormToolsRawRow(database, `INSERT INTO workflow.tool_call(id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,idempotency_key,request_hash,request_bytes,request_summary,status,retryable,version,started_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, 'STARTED',false,1,clock_timestamp()) RETURNING `+gormToolCallColumns, string(call.ID), string(call.WorkspaceID), string(call.WorkflowRunID), string(call.NodeRunID), string(call.NodeAttemptID), call.CallNo, call.RequestedToolName, call.Tool.Version, call.DefinitionHash, call.InputSchema.ID, call.InputSchema.Version, call.OutputSchema.ID, call.OutputSchema.Version, nullableGORMText(string(call.Capability)), string(call.SideEffectLevel), string(call.InvocationPolicy), nil, call.RequestHash, call.RequestBytes, gormToolsJSONB(call.RequestSummary))
	if err != nil {
		return domain.ToolCall{}, classifyGORMTools(ctx, err)
	}
	created, err := scanGORMToolCall(row)
	if err != nil {
		if gormToolsNoRows(err) {
			return domain.ToolCall{}, idempotencyConflict(err)
		}
		return domain.ToolCall{}, classifyGORMTools(ctx, err)
	}
	return created, nil
}

var _ application.WorkspaceAnalysisToolOperationRepository = (*GORMWorkspaceAnalysisRepository)(nil)
