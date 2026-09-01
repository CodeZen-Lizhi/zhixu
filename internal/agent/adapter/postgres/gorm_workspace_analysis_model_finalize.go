package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

// 本文件是 Workspace Analysis 模型操作 GORM 实现的事务内变更、回放与恢复
// helper。全部 SQL 与 legacy 逐语义对齐，仅把 pgx `$n` 绑定换成 GORM `?`，
// 事务边界由 Foundation UnitOfWork 提供。

func gormValidateWorkspaceAnalysisModelLockedBinding(
	ctx context.Context,
	transaction *gorm.DB,
	locked workspaceAnalysisModelLocks,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
) error {
	operation := locked.operation
	if operation.workspaceID != command.Identity.WorkspaceID || operation.analysisRunID != command.OperationKey.AnalysisRunID ||
		operation.workflowRunID != command.Identity.WorkflowRunID || operation.nodeRunID != command.Identity.NodeRunID ||
		operation.nodeKey != command.OperationKey.NodeKey || operation.kind != command.OperationKey.Kind ||
		operation.ordinal != command.OperationKey.Ordinal || operation.callKind != domain.WorkspaceAnalysisOperationCallModel ||
		operation.requestHash != command.Call.RequestHash || locked.analysisRun.ID != operation.analysisRunID ||
		locked.analysisRun.WorkspaceID != operation.workspaceID || locked.analysisRun.WorkflowRunID != operation.workflowRunID {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model operation scope or request drifted"))
	}
	if err := gormVerifyWorkspaceAnalysisModelBudgetTotals(ctx, transaction, locked.analysisRun); err != nil {
		return err
	}
	if operation.status == domain.WorkspaceAnalysisOperationPending {
		if operation.id != command.OperationID || locked.reservation != nil || locked.modelRun != nil || locked.modelCall != nil ||
			operation.modelCallID != nil {
			return workspaceAnalysisModelConflict(errors.New("pending workspace analysis model operation has runtime facts"))
		}
		return domain.ValidateWorkspaceAnalysisOperation(operation.domain(nil))
	}
	if locked.reservation == nil || locked.modelRun == nil || locked.modelCall == nil || operation.modelCallID == nil ||
		*operation.modelCallID != locked.modelCall.ID {
		return consistency(errors.New("workspace analysis model operation closure is incomplete"))
	}
	persistedOperation := operation.domain(&locked.reservation.ID)
	if err := domain.ValidateWorkspaceAnalysisOperation(persistedOperation); err != nil {
		return consistency(err)
	}
	if err := domain.ValidateWorkspaceAnalysisBudgetReservation(*locked.reservation); err != nil {
		return consistency(err)
	}
	if err := domain.ValidateWorkspaceAnalysisBudgetReservationBinding(locked.analysisRun, *locked.reservation, persistedOperation); err != nil {
		return consistency(err)
	}
	if err := domain.ValidateWorkspaceAnalysisOperationCallBinding(persistedOperation, domain.WorkspaceAnalysisCanonicalCallBinding{
		Kind: domain.WorkspaceAnalysisOperationCallModel, CallID: locked.modelCall.ID, RequestHash: locked.modelCall.RequestHash,
	}); err != nil {
		return consistency(err)
	}
	if locked.modelRun.WorkspaceID != operation.workspaceID || locked.modelRun.WorkflowRunID != operation.workflowRunID ||
		locked.modelRun.NodeRunID != operation.nodeRunID || operation.firstAttemptID == nil ||
		locked.modelRun.NodeAttemptID != *operation.firstAttemptID || locked.modelCall.ModelRunID != locked.modelRun.ID {
		return consistency(errors.New("workspace analysis model run/call scope drifted"))
	}
	return gormValidateWorkspaceAnalysisModelStoredClosure(ctx, transaction, locked)
}

func gormValidateWorkspaceAnalysisModelStoredClosure(ctx context.Context, transaction *gorm.DB, locked workspaceAnalysisModelLocks) error {
	run, call, operation, reservation := locked.modelRun, locked.modelCall, locked.operation, locked.reservation
	if run == nil || call == nil || reservation == nil {
		return consistency(errors.New("workspace analysis model closure is incomplete"))
	}
	if err := validateWorkspaceAnalysisModelReservationSettlement(*reservation, *call); err != nil {
		return err
	}
	switch operation.status {
	case domain.WorkspaceAnalysisOperationStarted:
		if run.Status != domain.ModelRunRunning || call.Status != domain.ModelCallStarted ||
			reservation.Status != domain.WorkspaceAnalysisBudgetReserved || workspaceAnalysisModelOperationHasResult(operation) ||
			operation.errorCode != nil {
			return consistency(errors.New("started workspace analysis model closure drifted"))
		}
	case domain.WorkspaceAnalysisOperationSucceeded:
		if run.Status != domain.ModelRunSucceeded || call.Status != domain.ModelCallSucceeded ||
			reservation.Status != domain.WorkspaceAnalysisBudgetSettled || operation.resultKind == nil || operation.resultID == nil || operation.resultHash == nil {
			return consistency(errors.New("successful workspace analysis model closure drifted"))
		}
		if err := gormVerifyWorkspaceAnalysisModelOperationResult(ctx, transaction, locked); err != nil {
			return err
		}
	case domain.WorkspaceAnalysisOperationFailed:
		failed := run.Status == domain.ModelRunFailed && call.Status == domain.ModelCallFailed &&
			operation.errorCode != nil && *operation.errorCode == call.ErrorCode && run.FinalErrorCode == call.ErrorCode
		refused := run.Status == domain.ModelRunRefused && call.Status == domain.ModelCallSucceeded &&
			run.FinalResultType == domain.ResultTypeRefusal && operation.errorCode != nil &&
			*operation.errorCode == string(domain.WorkspaceAnalysisRunModelRefused) && run.FinalErrorCode == *operation.errorCode
		if reservation.Status != domain.WorkspaceAnalysisBudgetSettled || workspaceAnalysisModelOperationHasResult(operation) ||
			(!failed && !refused) {
			return consistency(errors.New("failed workspace analysis model closure drifted"))
		}
	case domain.WorkspaceAnalysisOperationUnknown:
		if run.Status != domain.ModelRunUnknown || call.Status != domain.ModelCallUnknown ||
			reservation.Status != domain.WorkspaceAnalysisBudgetUnknownCharged || operation.errorCode == nil ||
			*operation.errorCode != call.ErrorCode || run.FinalErrorCode != call.ErrorCode ||
			workspaceAnalysisModelOperationHasResult(operation) {
			return consistency(errors.New("unknown workspace analysis model closure drifted"))
		}
	default:
		return consistency(errors.New("workspace analysis model closure status is unsupported"))
	}
	return nil
}

func gormVerifyWorkspaceAnalysisModelOperationResult(ctx context.Context, transaction *gorm.DB, locked workspaceAnalysisModelLocks) error {
	operation := locked.operation
	if operation.resultKind == nil || operation.resultID == nil || operation.resultHash == nil ||
		locked.reservation == nil || locked.modelRun == nil || locked.modelCall == nil {
		return consistency(errors.New("workspace analysis model operation result is incomplete"))
	}
	persistedOperation := operation.domain(&locked.reservation.ID)
	switch *operation.resultKind {
	case domain.WorkspaceAnalysisOperationResultModelCall:
		result, err := gormLoadWorkspaceAnalysisModelResultByIdentity(ctx, transaction, operation.workspaceID, operation.analysisRunID,
			operation.id, locked.modelRun.ID, locked.modelCall.ID, *operation.resultID)
		if err != nil {
			return err
		}
		if result.DocumentHash != *operation.resultHash {
			return consistency(errors.New("workspace analysis model result hash drifted"))
		}
		var candidate *domain.WorkspaceAnalysisCandidate
		if result.SubjectCandidateID != nil {
			persistedCandidate, loadErr := gormLoadWorkspaceAnalysisCandidateSubject(ctx, transaction, operation.workspaceID,
				operation.analysisRunID, *result.SubjectCandidateID)
			if loadErr != nil {
				return loadErr
			}
			candidate = &persistedCandidate
		}
		if err := domain.ValidateWorkspaceAnalysisModelResultBinding(result, locked.analysisRun, persistedOperation,
			*locked.modelRun, *locked.modelCall, candidate); err != nil {
			return consistency(err)
		}
	case domain.WorkspaceAnalysisOperationResultCandidate:
		candidate, err := gormLoadWorkspaceAnalysisCandidateByIdentity(ctx, transaction, operation.workspaceID, operation.analysisRunID,
			operation.id, locked.modelRun.ID, *operation.resultID)
		if err != nil {
			return err
		}
		if candidate.DocumentHash != *operation.resultHash || candidate.DocumentHash != locked.modelCall.ResponseHash ||
			candidate.DocumentBytes != locked.modelCall.ResponseBytes {
			return consistency(errors.New("workspace analysis candidate result hash drifted"))
		}
		if err := domain.ValidateWorkspaceAnalysisCandidateBinding(candidate, locked.analysisRun, persistedOperation, *locked.modelRun); err != nil {
			return consistency(err)
		}
	default:
		return consistency(errors.New("workspace analysis model operation has an invalid result kind"))
	}
	return nil
}

func gormVerifyWorkspaceAnalysisModelBudgetTotals(ctx context.Context, transaction *gorm.DB, run domain.WorkspaceAnalysisRun) error {
	var reservedModelCalls, reservedToolCalls, reservedSourceReads int64
	var reservedInputTokens, reservedOutputTokens int64
	var settledModelCalls, settledToolCalls, settledSourceReads int64
	var settledInputTokens, settledOutputTokens int64
	row, err := gormRawRow(transaction.WithContext(ctx), `SELECT
		COALESCE(sum(reserved_model_calls) FILTER (WHERE status='RESERVED'),0)::bigint,
		COALESCE(sum(reserved_tool_calls) FILTER (WHERE status='RESERVED'),0)::bigint,
		COALESCE(sum(reserved_source_reads) FILTER (WHERE status='RESERVED'),0)::bigint,
		COALESCE(sum(reserved_input_tokens) FILTER (WHERE status='RESERVED'),0)::bigint,
		COALESCE(sum(reserved_output_tokens) FILTER (WHERE status='RESERVED'),0)::bigint,
		COALESCE(sum(settled_model_calls) FILTER (WHERE status<>'RESERVED'),0)::bigint,
		COALESCE(sum(settled_tool_calls) FILTER (WHERE status<>'RESERVED'),0)::bigint,
		COALESCE(sum(settled_source_reads) FILTER (WHERE status<>'RESERVED'),0)::bigint,
		COALESCE(sum(settled_input_tokens) FILTER (WHERE status<>'RESERVED'),0)::bigint,
		COALESCE(sum(settled_output_tokens) FILTER (WHERE status<>'RESERVED'),0)::bigint
		FROM agent.workspace_analysis_budget_reservation WHERE analysis_run_id=?::uuid`, string(run.ID))
	if err != nil {
		return classifyGORM(ctx, err)
	}
	if err := row.Scan(
		&reservedModelCalls, &reservedToolCalls, &reservedSourceReads, &reservedInputTokens, &reservedOutputTokens,
		&settledModelCalls, &settledToolCalls, &settledSourceReads, &settledInputTokens, &settledOutputTokens,
	); err != nil {
		return classifyGORM(ctx, err)
	}
	if int64(run.Reserved.ModelCalls) != reservedModelCalls || int64(run.Reserved.ToolCalls) != reservedToolCalls ||
		int64(run.Reserved.SourceReads) != reservedSourceReads || run.Reserved.InputTokens != reservedInputTokens ||
		run.Reserved.OutputTokens != reservedOutputTokens || int64(run.Settled.ModelCalls) != settledModelCalls ||
		int64(run.Settled.ToolCalls) != settledToolCalls || int64(run.Settled.SourceReads) != settledSourceReads ||
		run.Settled.InputTokens != settledInputTokens || run.Settled.OutputTokens != settledOutputTokens {
		return consistency(errors.New("workspace analysis model budget totals drifted"))
	}
	return nil
}

func gormAuthorizePendingWorkspaceAnalysisModelCall(
	ctx context.Context,
	transaction *gorm.DB,
	locked workspaceAnalysisModelLocks,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
) (application.WorkspaceAnalysisModelAuthorizationResult, error) {
	if err := validateWorkspaceAnalysisModelAdmission(locked, command); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	at := locked.fence.databaseNow
	run := command.Run
	run.CreatedAt, run.UpdatedAt, run.CompletedAt = at, at, nil
	call := command.Call
	call.StartedAt, call.CompletedAt = at, nil
	if err := domain.ValidateModelRun(run); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	if err := domain.ValidateModelCall(call); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	execResult := transaction.WithContext(ctx).Exec(gormInsertModelRunSQL, modelRunArgs(run)...)
	if execResult.Error != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, classifyGORM(ctx, execResult.Error)
	}
	if execResult.RowsAffected != 1 {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model run already exists"))
	}
	execResult = transaction.WithContext(ctx).Exec(`INSERT INTO agent.model_call(
		id,model_run_id,call_no,phase,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
		request_hash,response_hash,request_bytes,response_bytes,input_tokens,output_tokens,latency_ms,
		status,error_code,version,started_at,completed_at
	) VALUES(
		?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,
		?,NULL,?,0,0,0,0,'STARTED',NULL,1,?,NULL
	)`, string(call.ID), string(call.ModelRunID), call.CallNo, string(call.Phase),
		call.Model.AdapterName, call.Model.AdapterVersion, call.Model.ModelID, call.Model.ModelVersion,
		call.Profile.ID, call.Profile.Version, call.Prompt.ID, call.Prompt.Version,
		call.Schema.ID, call.Schema.Version, call.MaxOutputTokens,
		call.RequestHash, call.RequestBytes, at,
	)
	if execResult.Error != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, classifyGORM(ctx, execResult.Error)
	}
	if execResult.RowsAffected != 1 {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model call was not inserted"))
	}
	execResult = transaction.WithContext(ctx).Exec(`UPDATE agent.workspace_analysis_run SET
		status='running',reserved_model_calls=reserved_model_calls+1,
		reserved_input_tokens=reserved_input_tokens+?,reserved_output_tokens=reserved_output_tokens+?,
		version=version+1,updated_at=GREATEST(updated_at,?)
		WHERE id=?::uuid AND workspace_id=?::uuid AND workflow_run_id=?::uuid AND version=?
		  AND status IN ('queued','running')
		  AND reserved_model_calls+settled_model_calls+1<=max_model_calls
		  AND reserved_input_tokens+settled_input_tokens+?<=max_input_tokens
		  AND reserved_output_tokens+settled_output_tokens+?<=max_output_tokens`,
		domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall, call.MaxOutputTokens, at,
		string(locked.analysisRun.ID), string(locked.analysisRun.WorkspaceID), string(locked.analysisRun.WorkflowRunID),
		locked.analysisRun.Version,
		domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall, call.MaxOutputTokens,
	)
	if execResult.Error != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, classifyGORM(ctx, execResult.Error)
	}
	if execResult.RowsAffected != 1 {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model run budget CAS failed"))
	}
	execResult = transaction.WithContext(ctx).Exec(`UPDATE agent.workspace_analysis_operation SET
		status='STARTED',first_node_attempt_id=?,latest_node_attempt_id=?,model_call_id=?,
		version=version+1,updated_at=GREATEST(updated_at,?),started_at=?
		WHERE id=?::uuid AND workspace_id=?::uuid AND analysis_run_id=?::uuid AND workflow_run_id=?::uuid AND node_run_id=?::uuid
		  AND call_kind='MODEL' AND request_hash=? AND status='PENDING' AND version=?`,
		string(command.Identity.NodeAttemptID), string(command.Identity.NodeAttemptID), string(call.ID), at, at,
		string(locked.operation.id), string(locked.operation.workspaceID), string(locked.operation.analysisRunID),
		string(locked.operation.workflowRunID), string(locked.operation.nodeRunID), locked.operation.requestHash, locked.operation.version,
	)
	if execResult.Error != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, classifyGORM(ctx, execResult.Error)
	}
	if execResult.RowsAffected != 1 {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model operation authorization CAS failed"))
	}
	execResult = transaction.WithContext(ctx).Exec(`INSERT INTO agent.workspace_analysis_budget_reservation(
		id,workspace_id,analysis_run_id,operation_id,call_kind,model_call_id,status,
		reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,
		reserved_cost_microunits,created_at
	) VALUES(?,?,?,?,'MODEL',?,'RESERVED',1,0,0,?,?,NULL,?)`,
		string(command.ReservationID), string(command.Identity.WorkspaceID), string(command.OperationKey.AnalysisRunID),
		string(locked.operation.id), string(call.ID), domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall,
		call.MaxOutputTokens, at,
	)
	if execResult.Error != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, classifyGORM(ctx, execResult.Error)
	}
	if execResult.RowsAffected != 1 {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model reservation was not inserted"))
	}
	result := workspaceAnalysisModelAuthorizationResult(run, call, locked.operation.id, command.ReservationID,
		application.WorkspaceAnalysisModelAuthorizationCreated)
	if err := result.ValidateFor(command); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	return result, nil
}

// gormReducePriorWorkspaceAnalysisModelCallToUnknown 把前任 attempt 的未完成调用
// 全额结算为 UNKNOWN，再让当前 attempt 的授权继续走 terminal replay 路径。
func gormReducePriorWorkspaceAnalysisModelCallToUnknown(
	ctx context.Context,
	transaction *gorm.DB,
	locked workspaceAnalysisModelLocks,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
) (application.WorkspaceAnalysisModelAuthorizationResult, error) {
	if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
		locked.modelRun.Status != domain.ModelRunRunning || locked.modelCall.Status != domain.ModelCallStarted ||
		locked.reservation.Status != domain.WorkspaceAnalysisBudgetReserved || locked.operation.firstAttemptID == nil ||
		locked.operation.latestAttemptID == nil || *locked.operation.latestAttemptID == command.Identity.NodeAttemptID ||
		!sameWorkspaceAnalysisModelAuthorizationRequest(command.Run, command.Call, *locked.modelRun, *locked.modelCall) {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis replacement model request drifted"))
	}
	at := locked.fence.databaseNow
	errorCode := string(domain.WorkspaceAnalysisRunResultUnknown)
	call := *locked.modelCall
	call.Status, call.ResponseHash, call.ResponseBytes = domain.ModelCallUnknown, "", 0
	call.Usage, call.LatencyMillis, call.ErrorCode = domain.TokenUsage{}, 0, errorCode
	call.Version, call.CompletedAt = call.Version+1, &at
	run := *locked.modelRun
	run.Status, run.FinalResultType, run.FinalErrorCode = domain.ModelRunUnknown, "", errorCode
	run.Version, run.UpdatedAt, run.CompletedAt = run.Version+1, at, &at
	if err := gormUpdateWorkspaceAnalysisModelCall(ctx, transaction, locked.modelCall.Version, call); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	if err := gormUpdateWorkspaceAnalysisModelRun(ctx, transaction, locked.modelRun.Version, run); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	if err := gormSettleWorkspaceAnalysisModelReservation(ctx, transaction, *locked.reservation, call, at, true); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	if err := gormSettleWorkspaceAnalysisModelRunBudget(ctx, transaction, locked.analysisRun, *locked.reservation, call, at, true); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	if err := gormCompleteWorkspaceAnalysisModelOperation(ctx, transaction, locked.operation, command.Identity.NodeAttemptID,
		domain.WorkspaceAnalysisOperationUnknown, nil, errorCode, at); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	result := workspaceAnalysisModelAuthorizationResult(run, call, locked.operation.id, locked.reservation.ID,
		application.WorkspaceAnalysisModelAuthorizationTerminateUnknown)
	if err := result.ValidateFor(command); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	return result, nil
}

// gormWorkspaceAnalysisModelTerminalAuthorizationResult 对已终结槽位做 exact replay。
func gormWorkspaceAnalysisModelTerminalAuthorizationResult(
	ctx context.Context,
	transaction *gorm.DB,
	locked workspaceAnalysisModelLocks,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
) (application.WorkspaceAnalysisModelAuthorizationResult, error) {
	if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
		!sameWorkspaceAnalysisModelAuthorizationRequest(command.Run, command.Call, *locked.modelRun, *locked.modelCall) {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model replay request drifted"))
	}
	if err := gormValidateWorkspaceAnalysisModelStoredClosure(ctx, transaction, locked); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	disposition := application.WorkspaceAnalysisModelAuthorizationReuseResult
	switch locked.operation.status {
	case domain.WorkspaceAnalysisOperationFailed:
		disposition = application.WorkspaceAnalysisModelAuthorizationReplayFailure
	case domain.WorkspaceAnalysisOperationUnknown:
		disposition = application.WorkspaceAnalysisModelAuthorizationTerminateUnknown
	}
	result := workspaceAnalysisModelAuthorizationResult(*locked.modelRun, *locked.modelCall, locked.operation.id,
		locked.reservation.ID, disposition)
	if err := result.ValidateFor(command); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	return result, nil
}

// gormAdvanceWorkspaceAnalysisModelTerminalAttempt 在 terminal replay 时推进
// Operation 的 latest attempt 标记。
func gormAdvanceWorkspaceAnalysisModelTerminalAttempt(
	ctx context.Context,
	transaction *gorm.DB,
	locked workspaceAnalysisModelLocks,
	attemptID foundation.ID,
) error {
	if locked.operation.latestAttemptID != nil && *locked.operation.latestAttemptID == attemptID {
		return nil
	}
	execResult := transaction.WithContext(ctx).Exec(`UPDATE agent.workspace_analysis_operation SET
		latest_node_attempt_id=?,version=version+1,updated_at=GREATEST(updated_at,?)
		WHERE id=?::uuid AND workspace_id=?::uuid AND analysis_run_id=?::uuid AND call_kind='MODEL'
		  AND status IN ('SUCCEEDED','FAILED','UNKNOWN') AND version=?`,
		string(attemptID), locked.fence.databaseNow, string(locked.operation.id), string(locked.operation.workspaceID),
		string(locked.operation.analysisRunID), locked.operation.version,
	)
	if execResult.Error != nil {
		return classifyGORM(ctx, execResult.Error)
	}
	if execResult.RowsAffected != 1 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model terminal replay attempt CAS failed"))
	}
	return nil
}

// gormValidateWorkspaceAnalysisModelTerminalCommandBinding 校验终结命令与持久化闭包
// 完全一致。
func gormValidateWorkspaceAnalysisModelTerminalCommandBinding(
	ctx context.Context,
	transaction *gorm.DB,
	locked workspaceAnalysisModelLocks,
	operationID foundation.ID,
	reservationID foundation.ID,
	run domain.ModelRun,
	call domain.ModelCall,
) error {
	command := authorizationCommandForTerminal(application.WorkspaceAnalysisModelExecutionIdentity{
		WorkspaceID: locked.analysisRun.WorkspaceID, DefinitionID: locked.fence.definitionID,
		DefinitionVersion: locked.analysisRun.DefinitionVersion, DefinitionHash: locked.analysisRun.DefinitionHash,
		WorkflowRunID: locked.analysisRun.WorkflowRunID, NodeKey: locked.operation.nodeKey,
		NodeRunID: locked.operation.nodeRunID, NodeAttemptID: run.NodeAttemptID,
		LeaseOwner: workspaceAnalysisModelOwner(locked.fence), LeaseFence: locked.fence.attemptNo,
	}, domain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: locked.operation.analysisRunID, NodeKey: locked.operation.nodeKey,
		Kind: locked.operation.kind, Ordinal: locked.operation.ordinal,
	}, operationID, reservationID, run, call)
	if err := gormValidateWorkspaceAnalysisModelLockedBinding(ctx, transaction, locked, command); err != nil {
		return err
	}
	if locked.operation.id != operationID || locked.reservation == nil || locked.reservation.ID != reservationID ||
		locked.modelRun == nil || locked.modelCall == nil || locked.operation.firstAttemptID == nil ||
		locked.operation.latestAttemptID == nil || *locked.operation.firstAttemptID != run.NodeAttemptID ||
		*locked.operation.latestAttemptID != run.NodeAttemptID ||
		!sameModelRunBinding(*locked.modelRun, run) || !sameModelCallBinding(*locked.modelCall, call) {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model terminal binding drifted"))
	}
	return nil
}

// gormFinalizeWorkspaceAnalysisModelCallTx 终结失败/拒绝/UNKNOWN 的 Call 与 Run，
// 全额结算预算并关闭 Operation。
func gormFinalizeWorkspaceAnalysisModelCallTx(
	ctx context.Context,
	transaction *gorm.DB,
	locked workspaceAnalysisModelLocks,
	command application.FinalizeWorkspaceAnalysisModelCallCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
		locked.modelCall.Version != command.ExpectedCallVersion || locked.modelRun.Version != command.ExpectedRunVersion {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model terminal version changed"))
	}
	if err := gormUpdateWorkspaceAnalysisModelCall(ctx, transaction, command.ExpectedCallVersion, command.Call); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := gormUpdateWorkspaceAnalysisModelRun(ctx, transaction, command.ExpectedRunVersion, command.Run); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	completedAt := *command.Run.CompletedAt
	unknown := command.Call.Status == domain.ModelCallUnknown
	if err := gormSettleWorkspaceAnalysisModelReservation(ctx, transaction, *locked.reservation, command.Call, completedAt, unknown); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := gormSettleWorkspaceAnalysisModelRunBudget(ctx, transaction, locked.analysisRun, *locked.reservation, command.Call, completedAt, unknown); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	operationStatus := domain.WorkspaceAnalysisOperationFailed
	if unknown {
		operationStatus = domain.WorkspaceAnalysisOperationUnknown
	}
	if err := gormCompleteWorkspaceAnalysisModelOperation(ctx, transaction, locked.operation, command.Identity.NodeAttemptID,
		operationStatus, nil, command.Run.FinalErrorCode, completedAt); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	return application.WorkspaceAnalysisModelMutationResult{
		Run: command.Run, Call: command.Call, OperationID: locked.operation.id,
		ReservationID: locked.reservation.ID,
	}, nil
}

// gormFinalizeWorkspaceAnalysisModelResultTx 保存 canonical 结果并关闭全部模型预算事实。
func gormFinalizeWorkspaceAnalysisModelResultTx(
	ctx context.Context,
	transaction *gorm.DB,
	locked workspaceAnalysisModelLocks,
	command application.FinalizeWorkspaceAnalysisModelResultCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
		locked.modelCall.Version != command.ExpectedCallVersion || locked.modelRun.Version != command.ExpectedRunVersion {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model result version changed"))
	}
	if err := gormUpdateWorkspaceAnalysisModelCall(ctx, transaction, command.ExpectedCallVersion, command.Call); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := gormUpdateWorkspaceAnalysisModelRun(ctx, transaction, command.ExpectedRunVersion, command.Run); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := gormInsertWorkspaceAnalysisModelResult(ctx, transaction, command.Result); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	completedAt := command.Result.CreatedAt
	if err := gormSettleWorkspaceAnalysisModelReservation(ctx, transaction, *locked.reservation, command.Call, completedAt, false); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := gormSettleWorkspaceAnalysisModelRunBudget(ctx, transaction, locked.analysisRun, *locked.reservation, command.Call, completedAt, false); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	resultKind := domain.WorkspaceAnalysisOperationResultModelCall
	resultID, resultHash := command.Result.ID, command.Result.DocumentHash
	if err := gormCompleteWorkspaceAnalysisModelOperation(ctx, transaction, locked.operation, command.Identity.NodeAttemptID,
		domain.WorkspaceAnalysisOperationSucceeded,
		&domain.WorkspaceAnalysisOperationResultRef{Kind: resultKind, ID: resultID, Hash: resultHash}, "", completedAt); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	resultCopy := command.Result
	return application.WorkspaceAnalysisModelMutationResult{
		Run: command.Run, Call: command.Call, Result: &resultCopy, OperationID: locked.operation.id,
		ReservationID: locked.reservation.ID,
	}, nil
}

// gormFinalizeWorkspaceAnalysisModelCandidateTx 保存 Synthesis canonical Candidate
// 并关闭全部模型预算事实。
func gormFinalizeWorkspaceAnalysisModelCandidateTx(
	ctx context.Context,
	transaction *gorm.DB,
	locked workspaceAnalysisModelLocks,
	command application.FinalizeWorkspaceAnalysisModelCandidateCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
		locked.modelCall.Version != command.ExpectedCallVersion || locked.modelRun.Version != command.ExpectedRunVersion {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model candidate version changed"))
	}
	if err := gormUpdateWorkspaceAnalysisModelCall(ctx, transaction, command.ExpectedCallVersion, command.Call); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := gormUpdateWorkspaceAnalysisModelRun(ctx, transaction, command.ExpectedRunVersion, command.Run); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := gormInsertWorkspaceAnalysisCandidate(ctx, transaction, command.Candidate); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	completedAt := command.Candidate.CreatedAt
	if err := gormSettleWorkspaceAnalysisModelReservation(ctx, transaction, *locked.reservation, command.Call, completedAt, false); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := gormSettleWorkspaceAnalysisModelRunBudget(ctx, transaction, locked.analysisRun, *locked.reservation, command.Call, completedAt, false); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	resultRef := domain.WorkspaceAnalysisOperationResultRef{
		Kind: domain.WorkspaceAnalysisOperationResultCandidate,
		ID:   command.Candidate.ID,
		Hash: command.Candidate.DocumentHash,
	}
	if err := gormCompleteWorkspaceAnalysisModelOperation(ctx, transaction, locked.operation, command.Identity.NodeAttemptID,
		domain.WorkspaceAnalysisOperationSucceeded, &resultRef, "", completedAt); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	candidateCopy := command.Candidate
	return application.WorkspaceAnalysisModelMutationResult{
		Run: command.Run, Call: command.Call, Candidate: &candidateCopy, OperationID: locked.operation.id,
		ReservationID: locked.reservation.ID,
	}, nil
}

// gormUpdateWorkspaceAnalysisModelCall 以 CAS 更新 STARTED 状态的 Model Call。
func gormUpdateWorkspaceAnalysisModelCall(ctx context.Context, transaction *gorm.DB, expectedVersion int64, call domain.ModelCall) error {
	execResult := transaction.WithContext(ctx).Exec(`UPDATE agent.model_call SET
		response_hash=?,response_bytes=?,input_tokens=?,output_tokens=?,latency_ms=?,
		status=?,error_code=?,version=?,completed_at=?
		WHERE id=?::uuid AND model_run_id=?::uuid AND status='STARTED' AND version=?`,
		optionalText(call.ResponseHash), call.ResponseBytes, call.Usage.InputTokens, call.Usage.OutputTokens,
		call.LatencyMillis, string(call.Status), optionalText(call.ErrorCode), call.Version, optionalTime(call.CompletedAt),
		string(call.ID), string(call.ModelRunID), expectedVersion,
	)
	if execResult.Error != nil {
		return classifyGORM(ctx, execResult.Error)
	}
	if execResult.RowsAffected != 1 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model call CAS failed"))
	}
	return nil
}

// gormUpdateWorkspaceAnalysisModelRun 以 CAS 更新 RUNNING 状态的 Model Run。
func gormUpdateWorkspaceAnalysisModelRun(ctx context.Context, transaction *gorm.DB, expectedVersion int64, run domain.ModelRun) error {
	execResult := transaction.WithContext(ctx).Exec(`UPDATE agent.model_run SET
		retrieval_index_version_id=?,embedding_version_id=?,rerank_model_version=?,
		status=?,final_result_type=?,error_code=?,version=?,updated_at=?,completed_at=?
		WHERE id=?::uuid AND workspace_id=?::uuid AND workflow_run_id=?::uuid AND node_run_id=?::uuid AND node_attempt_id=?::uuid
		  AND status='RUNNING' AND version=?`,
		optionalFoundationID(run.Retrieval.IndexVersionID), optionalID(run.Retrieval.EmbeddingVersionID), optionalText(run.Retrieval.RerankModelVersion),
		string(run.Status), optionalText(run.FinalResultType), optionalText(run.FinalErrorCode), run.Version,
		run.UpdatedAt.UTC(), optionalTime(run.CompletedAt), string(run.ID), string(run.WorkspaceID),
		string(run.WorkflowRunID), string(run.NodeRunID), string(run.NodeAttemptID), expectedVersion,
	)
	if execResult.Error != nil {
		return classifyGORM(ctx, execResult.Error)
	}
	if execResult.RowsAffected != 1 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model run CAS failed"))
	}
	return nil
}

// gormInsertWorkspaceAnalysisModelResult 写入不可变模型结果。
func gormInsertWorkspaceAnalysisModelResult(ctx context.Context, transaction *gorm.DB, result domain.WorkspaceAnalysisModelResult) error {
	var candidateID, candidateHash any
	if result.SubjectCandidateID != nil {
		candidateID, candidateHash = string(*result.SubjectCandidateID), result.SubjectCandidateHash
	}
	execResult := transaction.WithContext(ctx).Exec(`INSERT INTO agent.workspace_analysis_model_result(
		id,workspace_id,analysis_run_id,operation_id,node_attempt_id,model_run_id,model_call_id,
		operation_kind,schema_id,schema_version,subject_candidate_id,subject_candidate_hash,
		document,document_hash,document_bytes,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(result.ID), string(result.WorkspaceID), string(result.AnalysisRunID), string(result.OperationID),
		string(result.NodeAttemptID), string(result.ModelRunID), string(result.ModelCallID), string(result.OperationKind),
		result.Schema.ID, result.Schema.Version, candidateID, candidateHash, []byte(result.Document),
		result.DocumentHash, result.DocumentBytes, result.CreatedAt.UTC(),
	)
	if execResult.Error != nil {
		return classifyGORM(ctx, execResult.Error)
	}
	return nil
}

// gormInsertWorkspaceAnalysisCandidate 写入不可变 Synthesis 候选。
func gormInsertWorkspaceAnalysisCandidate(ctx context.Context, transaction *gorm.DB, candidate domain.WorkspaceAnalysisCandidate) error {
	execResult := transaction.WithContext(ctx).Exec(`INSERT INTO agent.workspace_analysis_candidate(
		id,workspace_id,analysis_run_id,answer_id,synthesis_operation_id,node_attempt_id,synthesis_model_run_id,
		schema_id,schema_version,document,document_hash,document_bytes,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(candidate.ID), string(candidate.WorkspaceID), string(candidate.AnalysisRunID), string(candidate.AnswerID),
		string(candidate.SynthesisOperationID), string(candidate.NodeAttemptID), string(candidate.SynthesisModelRunID),
		candidate.SchemaID, candidate.SchemaVersion, []byte(candidate.Document), candidate.DocumentHash,
		candidate.DocumentBytes, candidate.CreatedAt.UTC(),
	)
	if execResult.Error != nil {
		return classifyGORM(ctx, execResult.Error)
	}
	return nil
}

// gormSettleWorkspaceAnalysisModelReservation 结算预算 Reservation；UNKNOWN 时按
// 预留全额结算。
func gormSettleWorkspaceAnalysisModelReservation(
	ctx context.Context,
	transaction *gorm.DB,
	reservation domain.WorkspaceAnalysisBudgetReservation,
	call domain.ModelCall,
	completedAt time.Time,
	unknown bool,
) error {
	settledInput, settledOutput := call.Usage.InputTokens, call.Usage.OutputTokens
	status := domain.WorkspaceAnalysisBudgetSettled
	if unknown {
		status = domain.WorkspaceAnalysisBudgetUnknownCharged
		settledInput, settledOutput = reservation.Reserved.InputTokens, reservation.Reserved.OutputTokens
	}
	if settledInput < 0 || settledOutput < 0 || settledInput > reservation.Reserved.InputTokens || settledOutput > reservation.Reserved.OutputTokens {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model actual usage exceeds its reservation"))
	}
	execResult := transaction.WithContext(ctx).Exec(`UPDATE agent.workspace_analysis_budget_reservation SET
		status=?,settled_model_calls=1,settled_tool_calls=0,settled_source_reads=0,
		settled_input_tokens=?,settled_output_tokens=?,settled_cost_microunits=NULL,settled_at=?
		WHERE id=?::uuid AND workspace_id=?::uuid AND analysis_run_id=?::uuid AND operation_id=?::uuid
		  AND call_kind='MODEL' AND model_call_id=?::uuid AND status='RESERVED'`,
		string(status), settledInput, settledOutput, completedAt.UTC(), string(reservation.ID),
		string(reservation.WorkspaceID), string(reservation.AnalysisRunID), string(reservation.OperationID), string(call.ID),
	)
	if execResult.Error != nil {
		return classifyGORM(ctx, execResult.Error)
	}
	if execResult.RowsAffected != 1 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model reservation CAS failed"))
	}
	return nil
}

// gormSettleWorkspaceAnalysisModelRunBudget 按 Reservation 归约 Analysis Run 的
// 预留/实际预算。
func gormSettleWorkspaceAnalysisModelRunBudget(
	ctx context.Context,
	transaction *gorm.DB,
	run domain.WorkspaceAnalysisRun,
	reservation domain.WorkspaceAnalysisBudgetReservation,
	call domain.ModelCall,
	completedAt time.Time,
	unknown bool,
) error {
	settledInput, settledOutput := call.Usage.InputTokens, call.Usage.OutputTokens
	if unknown {
		settledInput, settledOutput = reservation.Reserved.InputTokens, reservation.Reserved.OutputTokens
	}
	execResult := transaction.WithContext(ctx).Exec(`UPDATE agent.workspace_analysis_run SET
		reserved_model_calls=reserved_model_calls-?,
		reserved_input_tokens=reserved_input_tokens-?,reserved_output_tokens=reserved_output_tokens-?,
		settled_model_calls=settled_model_calls+1,
		settled_input_tokens=settled_input_tokens+?,settled_output_tokens=settled_output_tokens+?,
		version=version+1,updated_at=GREATEST(updated_at,?)
		WHERE id=?::uuid AND workspace_id=?::uuid AND workflow_run_id=?::uuid AND status='running' AND version=?
		  AND reserved_model_calls>=? AND reserved_input_tokens>=? AND reserved_output_tokens>=?`,
		reservation.Reserved.ModelCalls, reservation.Reserved.InputTokens, reservation.Reserved.OutputTokens,
		settledInput, settledOutput, completedAt.UTC(), string(run.ID), string(run.WorkspaceID),
		string(run.WorkflowRunID), run.Version,
		reservation.Reserved.ModelCalls, reservation.Reserved.InputTokens, reservation.Reserved.OutputTokens,
	)
	if execResult.Error != nil {
		return classifyGORM(ctx, execResult.Error)
	}
	if execResult.RowsAffected != 1 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model run settlement CAS failed"))
	}
	return nil
}

// gormCompleteWorkspaceAnalysisModelOperation 以 CAS 关闭 Operation。
func gormCompleteWorkspaceAnalysisModelOperation(
	ctx context.Context,
	transaction *gorm.DB,
	operation workspaceAnalysisModelOperationRecord,
	latestAttemptID foundation.ID,
	status domain.WorkspaceAnalysisOperationStatus,
	result *domain.WorkspaceAnalysisOperationResultRef,
	errorCode string,
	completedAt time.Time,
) error {
	var resultKind, resultID, resultHash, persistedError any
	if result != nil {
		resultKind, resultID, resultHash = string(result.Kind), string(result.ID), result.Hash
	}
	if errorCode != "" {
		persistedError = errorCode
	}
	execResult := transaction.WithContext(ctx).Exec(`UPDATE agent.workspace_analysis_operation SET
		status=?,latest_node_attempt_id=?,result_kind=?,result_id=?,result_hash=?,error_code=?,
		version=version+1,updated_at=GREATEST(updated_at,?),completed_at=GREATEST(updated_at,?)
		WHERE id=?::uuid AND workspace_id=?::uuid AND analysis_run_id=?::uuid AND workflow_run_id=?::uuid AND node_run_id=?::uuid
		  AND call_kind='MODEL' AND request_hash=? AND status='STARTED' AND version=?`,
		string(status), string(latestAttemptID), resultKind, resultID, resultHash, persistedError, completedAt.UTC(), completedAt.UTC(),
		string(operation.id), string(operation.workspaceID), string(operation.analysisRunID),
		string(operation.workflowRunID), string(operation.nodeRunID), operation.requestHash, operation.version,
	)
	if execResult.Error != nil {
		return classifyGORM(ctx, execResult.Error)
	}
	if execResult.RowsAffected != 1 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model operation CAS failed"))
	}
	return nil
}

// gormForceWorkspaceAnalysisModelConstraints 提交前把所有 deferred constraint 立即求值。
func gormForceWorkspaceAnalysisModelConstraints(ctx context.Context, transaction *gorm.DB) error {
	if execResult := transaction.WithContext(ctx).Exec(`SET CONSTRAINTS ALL IMMEDIATE`); execResult.Error != nil {
		return classifyGORM(ctx, execResult.Error)
	}
	return nil
}

// gormReplayWorkspaceAnalysisModelResultTerminal 回放已成功的结果终结。
func gormReplayWorkspaceAnalysisModelResultTerminal(
	ctx context.Context,
	transaction *gorm.DB,
	locked workspaceAnalysisModelLocks,
	command application.FinalizeWorkspaceAnalysisModelResultCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if locked.operation.status != domain.WorkspaceAnalysisOperationSucceeded ||
		locked.operation.resultKind == nil || *locked.operation.resultKind != domain.WorkspaceAnalysisOperationResultModelCall ||
		locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
		locked.reservation.Status != domain.WorkspaceAnalysisBudgetSettled ||
		!sameModelRunResult(*locked.modelRun, command.Run) || !sameModelCallResult(*locked.modelCall, command.Call) {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model result terminal replay differs"))
	}
	result, err := gormLoadWorkspaceAnalysisModelResultByIdentity(ctx, transaction, command.Result.WorkspaceID,
		command.Result.AnalysisRunID, command.Result.OperationID, command.Result.ModelRunID,
		command.Result.ModelCallID, command.Result.ID)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if !sameWorkspaceAnalysisModelResult(result, command.Result) || locked.operation.resultID == nil ||
		*locked.operation.resultID != result.ID || locked.operation.resultHash == nil || *locked.operation.resultHash != result.DocumentHash {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model result replay identity differs"))
	}
	resultCopy := result
	return application.WorkspaceAnalysisModelMutationResult{
		Run: *locked.modelRun, Call: *locked.modelCall, Result: &resultCopy,
		OperationID: locked.operation.id, ReservationID: locked.reservation.ID, Replayed: true,
	}, nil
}

// gormReplayWorkspaceAnalysisModelCandidateTerminal 回放已成功的候选终结。
func gormReplayWorkspaceAnalysisModelCandidateTerminal(
	ctx context.Context,
	transaction *gorm.DB,
	locked workspaceAnalysisModelLocks,
	command application.FinalizeWorkspaceAnalysisModelCandidateCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if locked.operation.status != domain.WorkspaceAnalysisOperationSucceeded ||
		locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
		locked.reservation.Status != domain.WorkspaceAnalysisBudgetSettled ||
		!sameModelRunResult(*locked.modelRun, command.Run) || !sameModelCallResult(*locked.modelCall, command.Call) {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model candidate terminal replay differs"))
	}
	candidate, err := gormLoadWorkspaceAnalysisCandidateByIdentity(ctx, transaction, command.Candidate.WorkspaceID,
		command.Candidate.AnalysisRunID, command.Candidate.SynthesisOperationID,
		command.Candidate.SynthesisModelRunID, command.Candidate.ID)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if !sameWorkspaceAnalysisCandidate(candidate, command.Candidate) ||
		locked.operation.resultKind == nil || *locked.operation.resultKind != domain.WorkspaceAnalysisOperationResultCandidate ||
		locked.operation.resultID == nil || *locked.operation.resultID != candidate.ID ||
		locked.operation.resultHash == nil || *locked.operation.resultHash != candidate.DocumentHash {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model candidate replay identity differs"))
	}
	candidateCopy := candidate
	return application.WorkspaceAnalysisModelMutationResult{
		Run: *locked.modelRun, Call: *locked.modelCall, Candidate: &candidateCopy,
		OperationID: locked.operation.id, ReservationID: locked.reservation.ID, Replayed: true,
	}, nil
}

// gormLoadWorkspaceAnalysisModelResultByIdentity 按完整身份读取不可变模型结果。
func gormLoadWorkspaceAnalysisModelResultByIdentity(
	ctx context.Context,
	transaction *gorm.DB,
	workspaceID foundation.ID,
	analysisRunID foundation.ID,
	operationID foundation.ID,
	modelRunID foundation.ID,
	modelCallID foundation.ID,
	resultID foundation.ID,
) (domain.WorkspaceAnalysisModelResult, error) {
	row, err := gormRawRow(transaction.WithContext(ctx), `SELECT `+workspaceAnalysisModelResultColumns+`
		FROM agent.workspace_analysis_model_result AS result
		WHERE result.id=?::uuid AND result.workspace_id=?::uuid AND result.analysis_run_id=?::uuid
		  AND result.operation_id=?::uuid AND result.model_run_id=?::uuid AND result.model_call_id=?::uuid`,
		string(resultID), string(workspaceID), string(analysisRunID), string(operationID), string(modelRunID), string(modelCallID),
	)
	if err != nil {
		return domain.WorkspaceAnalysisModelResult{}, classifyGORM(ctx, err)
	}
	result, err := scanWorkspaceAnalysisModelResult(row)
	if gormWorkspaceAnalysisModelNoRows(err) {
		return domain.WorkspaceAnalysisModelResult{}, notFound()
	}
	if err != nil {
		return domain.WorkspaceAnalysisModelResult{}, classifyGORM(ctx, err)
	}
	if err := domain.ValidateWorkspaceAnalysisModelResult(result); err != nil {
		return domain.WorkspaceAnalysisModelResult{}, consistency(err)
	}
	return result, nil
}

// gormLoadWorkspaceAnalysisCandidateByIdentity 按完整身份读取不可变候选。
func gormLoadWorkspaceAnalysisCandidateByIdentity(
	ctx context.Context,
	transaction *gorm.DB,
	workspaceID foundation.ID,
	analysisRunID foundation.ID,
	operationID foundation.ID,
	modelRunID foundation.ID,
	candidateID foundation.ID,
) (domain.WorkspaceAnalysisCandidate, error) {
	row, err := gormRawRow(transaction.WithContext(ctx), `SELECT `+workspaceAnalysisCandidateColumns+`
		FROM agent.workspace_analysis_candidate AS candidate
		WHERE candidate.id=?::uuid AND candidate.workspace_id=?::uuid AND candidate.analysis_run_id=?::uuid
		  AND candidate.synthesis_operation_id=?::uuid AND candidate.synthesis_model_run_id=?::uuid`,
		string(candidateID), string(workspaceID), string(analysisRunID), string(operationID), string(modelRunID),
	)
	if err != nil {
		return domain.WorkspaceAnalysisCandidate{}, classifyGORM(ctx, err)
	}
	candidate, err := scanWorkspaceAnalysisCandidate(row)
	if gormWorkspaceAnalysisModelNoRows(err) {
		return domain.WorkspaceAnalysisCandidate{}, notFound()
	}
	if err != nil {
		return domain.WorkspaceAnalysisCandidate{}, classifyGORM(ctx, err)
	}
	if err := domain.ValidateWorkspaceAnalysisCandidate(candidate); err != nil {
		return domain.WorkspaceAnalysisCandidate{}, consistency(err)
	}
	return candidate, nil
}

// gormLoadWorkspaceAnalysisCandidateSubject 按 Workspace/Run 范围读取主体候选。
func gormLoadWorkspaceAnalysisCandidateSubject(
	ctx context.Context,
	transaction *gorm.DB,
	workspaceID foundation.ID,
	analysisRunID foundation.ID,
	candidateID foundation.ID,
) (domain.WorkspaceAnalysisCandidate, error) {
	row, err := gormRawRow(transaction.WithContext(ctx), `SELECT `+workspaceAnalysisCandidateColumns+`
		FROM agent.workspace_analysis_candidate AS candidate
		WHERE candidate.id=?::uuid AND candidate.workspace_id=?::uuid AND candidate.analysis_run_id=?::uuid`,
		string(candidateID), string(workspaceID), string(analysisRunID),
	)
	if err != nil {
		return domain.WorkspaceAnalysisCandidate{}, classifyGORM(ctx, err)
	}
	candidate, err := scanWorkspaceAnalysisCandidate(row)
	if gormWorkspaceAnalysisModelNoRows(err) {
		return domain.WorkspaceAnalysisCandidate{}, notFound()
	}
	if err != nil {
		return domain.WorkspaceAnalysisCandidate{}, classifyGORM(ctx, err)
	}
	if err := domain.ValidateWorkspaceAnalysisCandidate(candidate); err != nil {
		return domain.WorkspaceAnalysisCandidate{}, consistency(err)
	}
	return candidate, nil
}
