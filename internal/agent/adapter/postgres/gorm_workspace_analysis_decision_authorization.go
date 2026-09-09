package postgres

import (
	"bytes"
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

func validateWorkspaceAnalysisV2ModelAdmission(locked workspaceAnalysisModelLocks, command application.AuthorizeWorkspaceAnalysisModelCallCommand) error {
	run := locked.analysisRun
	timeout, ok := workspaceAnalysisModelTimeout(run, command.OperationKey.Kind)
	output, outputOK := workspaceAnalysisModelReservedOutputTokens(run, command.OperationKey.Kind)
	if run.DefinitionVersion != 2 || !ok || timeout <= 0 || !outputOK || int64(command.Call.MaxOutputTokens) != output {
		return workspaceAnalysisModelConflict(errors.New("dynamic model admission differs from the frozen policy"))
	}
	requested := domain.WorkspaceAnalysisBudgetAmount{ModelCalls: 1, InputTokens: domain.WorkspaceAnalysisV2MaxInputTokensPerModelCall, OutputTokens: output}
	deny := func(reason domain.WorkspaceAnalysisRunTerminationReason) error {
		return &application.WorkspaceAnalysisAdmissionDenial{OperationID: locked.operation.id, Reason: reason, Requested: requested}
	}
	// Decisions cannot consume the model/token allowance reserved for synthesis
	// and independent review, even when preceding calls used fewer actual tokens.
	remainingCalls, remainingInput, remainingOutput := 0, int64(0), int64(0)
	if command.OperationKey.Kind == domain.WorkspaceAnalysisOperationDecision {
		if command.OperationKey.Ordinal > domain.WorkspaceAnalysisV2MaxDecisions {
			return deny(domain.WorkspaceAnalysisRunBudgetExhausted)
		}
		remainingCalls, remainingInput = 2, 2*domain.WorkspaceAnalysisV2MaxInputTokensPerModelCall
		remainingOutput = run.Limits.Amount.OutputTokens - domain.WorkspaceAnalysisV2MaxDecisions*domain.WorkspaceAnalysisV2DecisionMaxOutputTokens
	}
	if run.Reserved.ModelCalls+run.Settled.ModelCalls+1+remainingCalls > run.Limits.Amount.ModelCalls ||
		run.Reserved.InputTokens+run.Settled.InputTokens+requested.InputTokens+remainingInput > run.Limits.Amount.InputTokens ||
		run.Reserved.OutputTokens+run.Settled.OutputTokens+requested.OutputTokens+remainingOutput > run.Limits.Amount.OutputTokens {
		return deny(domain.WorkspaceAnalysisRunBudgetExhausted)
	}
	if locked.fence.databaseNow.Add(timeout + domain.WorkspaceAnalysisV2DurableCompletionMargin).After(run.DeadlineAt) {
		return deny(domain.WorkspaceAnalysisRunDeadlineExceeded)
	}
	return nil
}

// A new logical decision reuses the current attempt's ModelRun. A replacement
// may close a fully persisted prefix, but can never resend an uncertain Call.
func gormAuthorizePendingWorkspaceAnalysisDecision(ctx context.Context, transaction *gorm.DB, locked workspaceAnalysisModelLocks, command application.AuthorizeWorkspaceAnalysisModelCallCommand) (application.WorkspaceAnalysisModelAuthorizationResult, error) {
	if err := validateWorkspaceAnalysisV2ModelAdmission(locked, command); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	if command.Run.NodeAttemptID != command.Identity.NodeAttemptID || command.Run.Status != domain.ModelRunRunning || command.Run.Version != 1 {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("new decision requires the current running model attempt"))
	}
	// This function validates and closes only prior attempts; the active attempt
	// remains RUNNING until finish or a terminal failure settles its last Call.
	closed := transaction.WithContext(ctx).Exec(`SELECT agent.close_workspace_analysis_v2_prior_decision_runs(?::uuid,?::uuid,?)`,
		string(locked.analysisRun.ID), string(command.Identity.NodeAttemptID), locked.fence.databaseNow)
	if closed.Error != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, classifyGORM(ctx, closed.Error)
	}
	row, err := gormRawRow(transaction.WithContext(ctx), gormModelRunSelect+` WHERE workspace_id=?::uuid AND node_attempt_id=?::uuid FOR UPDATE`,
		string(command.Identity.WorkspaceID), string(command.Identity.NodeAttemptID))
	if err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, classifyGORM(ctx, err)
	}
	run, err := scanModelRun(row)
	if err != nil && !gormWorkspaceAnalysisModelNoRows(err) {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, classifyGORM(ctx, err)
	}
	if gormWorkspaceAnalysisModelNoRows(err) {
		if command.Call.CallNo != 1 {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("first decision call must start at one"))
		}
		run = command.Run
		run.CreatedAt, run.UpdatedAt, run.CompletedAt = locked.fence.databaseNow, locked.fence.databaseNow, nil
		if err := domain.ValidateModelRun(run); err != nil {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, err
		}
		insert := transaction.WithContext(ctx).Exec(gormInsertModelRunSQL, modelRunArgs(run)...)
		if insert.Error != nil {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, classifyGORM(ctx, insert.Error)
		}
		if insert.RowsAffected != 1 {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("decision model run already exists"))
		}
	} else {
		if run.ID != command.Run.ID || run.Status != domain.ModelRunRunning || !sameModelRunBinding(run, command.Run) {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("retained decision model run binding drifted"))
		}
		var calls, lastCall, unfinished int
		stats, readErr := gormRawRow(transaction.WithContext(ctx), `SELECT count(*),COALESCE(max(call_no),0),
			count(*) FILTER (WHERE status<>'SUCCEEDED') FROM agent.model_call WHERE model_run_id=?::uuid`, string(run.ID))
		if readErr != nil {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, classifyGORM(ctx, readErr)
		}
		if readErr = stats.Scan(&calls, &lastCall, &unfinished); readErr != nil {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, classifyGORM(ctx, readErr)
		}
		if calls == 0 || calls != lastCall || unfinished != 0 || command.Call.CallNo != lastCall+1 {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("decision call is not the next completed prefix"))
		}
	}
	call := command.Call
	call.StartedAt, call.CompletedAt = locked.fence.databaseNow, nil
	if err := domain.ValidateModelCall(call); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	return gormStartWorkspaceAnalysisModelCall(ctx, transaction, locked, command, run, call)
}

func (repository *GORMWorkspaceAnalysisRepository) recoverWorkspaceAnalysisAdmissionDenial(ctx context.Context, command application.AuthorizeWorkspaceAnalysisModelCallCommand, denial *application.WorkspaceAnalysisAdmissionDenial, commitErr error) error {
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		locked, err := gormLockWorkspaceAnalysisModelOperation(callbackCtx, transaction, scope, repository.fence, command, false)
		if err != nil {
			return err
		}
		if err := validateWorkspaceAnalysisModelDefinition(locked, command.Identity); err != nil {
			return err
		}
		if err := gormValidateWorkspaceAnalysisModelLockedBinding(callbackCtx, transaction, locked, command); err != nil {
			return err
		}
		if locked.operation.status != domain.WorkspaceAnalysisOperationPending || locked.operation.id != denial.OperationID ||
			locked.modelCall != nil || locked.modelRun != nil || locked.reservation != nil {
			return errors.New("admission denial has no exact pending operation")
		}
		actual, ok := application.WorkspaceAnalysisAdmissionDenialFromError(validateWorkspaceAnalysisV2ModelAdmission(locked, command))
		if !ok || actual.Reason != denial.Reason || actual.Requested != denial.Requested {
			return errors.New("admission denial cannot be recovered from durable budgets")
		}
		return nil
	})
	if err != nil {
		return workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}
	return denial
}

const workspaceAnalysisDecisionColumns = `decision.id::text,decision.workspace_id::text,decision.analysis_run_id::text,
	decision.operation_id::text,decision.node_attempt_id::text,decision.model_run_id::text,decision.model_call_id::text,
	decision.ordinal,decision.document,decision.document_hash,decision.document_bytes,decision.created_at`

func scanWorkspaceAnalysisDecision(row rowScanner) (domain.WorkspaceAnalysisDecisionReceipt, error) {
	var receipt domain.WorkspaceAnalysisDecisionReceipt
	var id, workspace, analysis, operation, attempt, run, call string
	var document []byte
	if err := row.Scan(&id, &workspace, &analysis, &operation, &attempt, &run, &call, &receipt.Ordinal,
		&document, &receipt.DocumentHash, &receipt.DocumentBytes, &receipt.CreatedAt); err != nil {
		return receipt, err
	}
	receipt.ID, receipt.WorkspaceID, receipt.AnalysisRunID = foundation.ID(id), foundation.ID(workspace), foundation.ID(analysis)
	receipt.OperationID, receipt.NodeAttemptID, receipt.ModelRunID, receipt.ModelCallID = foundation.ID(operation), foundation.ID(attempt), foundation.ID(run), foundation.ID(call)
	decision, err := domain.DecodeWorkspaceAnalysisDecision(document)
	if err != nil {
		return receipt, consistency(err)
	}
	receipt.Decision = decision
	canonical, err := decision.Canonical()
	if err != nil || !bytes.Equal(canonical, document) {
		return receipt, consistency(errors.New("decision receipt is not canonical"))
	}
	if err := receipt.Validate(); err != nil {
		return receipt, consistency(err)
	}
	return receipt, nil
}

func gormLoadWorkspaceAnalysisDecision(ctx context.Context, transaction *gorm.DB, operationID foundation.ID) (domain.WorkspaceAnalysisDecisionReceipt, error) {
	row, err := gormRawRow(transaction.WithContext(ctx), `SELECT `+workspaceAnalysisDecisionColumns+`
		FROM agent.workspace_analysis_decision decision WHERE decision.operation_id=?::uuid`, string(operationID))
	if err != nil {
		return domain.WorkspaceAnalysisDecisionReceipt{}, classifyGORM(ctx, err)
	}
	receipt, err := scanWorkspaceAnalysisDecision(row)
	if err != nil {
		return domain.WorkspaceAnalysisDecisionReceipt{}, classifyGORM(ctx, err)
	}
	return receipt, nil
}

func gormValidateWorkspaceAnalysisDecisionClosure(ctx context.Context, transaction *gorm.DB, locked workspaceAnalysisModelLocks) error {
	run, call, operation, reservation := locked.modelRun, locked.modelCall, locked.operation, locked.reservation
	if run == nil || call == nil || reservation == nil || locked.analysisRun.DefinitionVersion != 2 ||
		operation.nodeKey != domain.WorkspaceAnalysisOperationNodeDecideNext || operation.ordinal > domain.WorkspaceAnalysisV2MaxDecisions ||
		call.Phase != domain.ModelCallAgent || call.Schema != (domain.SchemaRef{ID: domain.WorkspaceAnalysisDecisionSchemaID, Version: domain.WorkspaceAnalysisDecisionSchemaVersion}) ||
		run.Schema != call.Schema || run.ReducedSchema != call.Schema {
		return consistency(errors.New("dynamic decision closure is incomplete"))
	}
	if err := validateWorkspaceAnalysisModelReservationSettlement(*reservation, *call); err != nil {
		return err
	}
	switch operation.status {
	case domain.WorkspaceAnalysisOperationStarted:
		if run.Status != domain.ModelRunRunning || call.Status != domain.ModelCallStarted || reservation.Status != domain.WorkspaceAnalysisBudgetReserved || workspaceAnalysisModelOperationHasResult(operation) || operation.errorCode != nil {
			return consistency(errors.New("started decision closure drifted"))
		}
	case domain.WorkspaceAnalysisOperationSucceeded:
		if call.Status != domain.ModelCallSucceeded || reservation.Status != domain.WorkspaceAnalysisBudgetSettled ||
			operation.resultKind == nil || *operation.resultKind != domain.WorkspaceAnalysisOperationResultDecision || operation.resultID == nil || operation.resultHash == nil || operation.errorCode != nil ||
			(run.Status != domain.ModelRunRunning && run.Status != domain.ModelRunSucceeded && run.Status != domain.ModelRunFailed && run.Status != domain.ModelRunUnknown) ||
			(run.Status == domain.ModelRunSucceeded && run.FinalResultType != domain.ResultTypeWorkspaceAnalysisDecision) {
			return consistency(errors.New("successful decision closure drifted"))
		}
		receipt, err := gormLoadWorkspaceAnalysisDecision(ctx, transaction, operation.id)
		if err != nil {
			return err
		}
		if receipt.ID != *operation.resultID || receipt.DocumentHash != *operation.resultHash || receipt.DocumentHash != call.ResponseHash || receipt.DocumentBytes != call.ResponseBytes ||
			receipt.WorkspaceID != operation.workspaceID || receipt.AnalysisRunID != operation.analysisRunID || receipt.OperationID != operation.id ||
			receipt.ModelRunID != run.ID || receipt.ModelCallID != call.ID || receipt.NodeAttemptID != run.NodeAttemptID || receipt.Ordinal != operation.ordinal ||
			call.CompletedAt == nil || !receipt.CreatedAt.Equal(*call.CompletedAt) ||
			(receipt.Decision.Action == domain.WorkspaceAnalysisDecisionFinish && run.Status != domain.ModelRunSucceeded) {
			return consistency(errors.New("decision receipt binding drifted"))
		}
	case domain.WorkspaceAnalysisOperationFailed:
		if call.Status != domain.ModelCallFailed || run.Status != domain.ModelRunFailed || reservation.Status != domain.WorkspaceAnalysisBudgetSettled ||
			workspaceAnalysisModelOperationHasResult(operation) || operation.errorCode == nil || *operation.errorCode != call.ErrorCode || run.FinalErrorCode != call.ErrorCode {
			return consistency(errors.New("failed decision closure drifted"))
		}
	case domain.WorkspaceAnalysisOperationUnknown:
		if call.Status != domain.ModelCallUnknown || run.Status != domain.ModelRunUnknown || reservation.Status != domain.WorkspaceAnalysisBudgetUnknownCharged ||
			workspaceAnalysisModelOperationHasResult(operation) || operation.errorCode == nil || *operation.errorCode != call.ErrorCode || run.FinalErrorCode != call.ErrorCode {
			return consistency(errors.New("unknown decision closure drifted"))
		}
	default:
		return consistency(errors.New("decision closure status is unsupported"))
	}
	return nil
}
