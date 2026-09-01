package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// PrepareWorkspaceAnalysisToolOperationScoped 在调用方事务中创建或锁定 Tool logical operation。
func (repository *GORMRepository) PrepareWorkspaceAnalysisToolOperationScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	command application.PrepareWorkspaceAnalysisToolOperationCommand,
) (application.WorkspaceAnalysisToolOperationSnapshot, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelInvalid(
			errors.New("workspace analysis tool prepare context is nil"),
		)
	}
	if err := command.Validate(); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	database, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}

	run, found, err := loadGORMWorkspaceAnalysisToolRunForUpdate(
		ctx, database, command.Identity.WorkspaceID, command.Identity.WorkflowRunID, command.OperationKey.AnalysisRunID,
	)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if !found {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool prepare run was not found"),
		)
	}
	if err := validateGORMWorkspaceAnalysisToolLiveRun(run, command.Identity, command.OperationKey.AnalysisRunID); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if run.ToolCatalogHash != command.ExpectedToolCatalogHash {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool catalog binding differs"),
		)
	}
	if err := verifyGORMWorkspaceAnalysisToolBudgetTotals(ctx, database, run); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if err := proveGORMWorkspaceAnalysisToolHasNoRefusal(ctx, database, command.OperationKey); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}

	insert := database.Exec(`INSERT INTO agent.workspace_analysis_operation(
		id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
		ordinal,call_kind,request_hash,status,version,created_at,updated_at
	) SELECT ?,?,?,?,?,?,?,?,'TOOL',?,'PENDING',1,pending.at,pending.at
		FROM (SELECT clock_timestamp() AS at) AS pending
		ON CONFLICT (analysis_run_id,node_key,operation_kind,ordinal) DO NOTHING`,
		string(command.CandidateOperationID), string(command.Identity.WorkspaceID), string(command.OperationKey.AnalysisRunID),
		string(command.Identity.WorkflowRunID), string(command.Identity.NodeRunID), string(command.OperationKey.NodeKey),
		string(command.OperationKey.Kind), command.OperationKey.Ordinal, command.RequestHash,
	)
	if insert.Error != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, classifyGORM(ctx, insert.Error)
	}
	if insert.RowsAffected < 0 || insert.RowsAffected > 1 {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, consistency(
			errors.New("workspace analysis tool prepare affected an invalid number of operations"),
		)
	}
	created := insert.RowsAffected == 1

	operation, found, err := loadGORMWorkspaceAnalysisToolOperationForUpdateByKey(ctx, database, command.OperationKey)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if !found {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, consistency(
			errors.New("workspace analysis tool prepared operation was not found"),
		)
	}
	if created && operation.id != command.CandidateOperationID {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, consistency(
			errors.New("workspace analysis tool created operation identity differs"),
		)
	}
	reservation, foundReservation, err := loadGORMWorkspaceAnalysisToolReservationForUpdate(ctx, database, operation.id)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	databaseNow, err := loadGORMWorkspaceAnalysisToolDatabaseTime(ctx, database)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if err := validateGORMWorkspaceAnalysisToolLiveOperation(
		run, operation, command.Identity, command.OperationKey, command.RequestHash,
	); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}

	if operation.status == domain.WorkspaceAnalysisOperationPending {
		if foundReservation {
			return application.WorkspaceAnalysisToolOperationSnapshot{}, consistency(
				errors.New("pending workspace analysis tool operation has a reservation"),
			)
		}
		if err := validateGORMWorkspaceAnalysisToolPending(operation); err != nil {
			return application.WorkspaceAnalysisToolOperationSnapshot{}, err
		}
		if err := validateGORMWorkspaceAnalysisToolAdmission(ctx, database, run, command.OperationKey.Kind, databaseNow); err != nil {
			return application.WorkspaceAnalysisToolOperationSnapshot{}, err
		}
		return buildGORMWorkspaceAnalysisToolSnapshot(run, operation, nil, databaseNow)
	}
	if !foundReservation {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, consistency(
			errors.New("workspace analysis tool operation reservation is missing"),
		)
	}
	if _, err := validateGORMWorkspaceAnalysisToolStoredClosure(run, operation, reservation); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	return buildGORMWorkspaceAnalysisToolSnapshot(run, operation, &reservation, databaseNow)
}

// LockWorkspaceAnalysisToolOperationByCallScoped 按 Tool Call 锁定既有 Agent closure。
func (repository *GORMRepository) LockWorkspaceAnalysisToolOperationByCallScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	query application.WorkspaceAnalysisToolCallLockQuery,
) (application.WorkspaceAnalysisToolOperationSnapshot, bool, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, workspaceAnalysisModelInvalid(
			errors.New("workspace analysis tool call lock context is nil"),
		)
	}
	if err := query.Validate(); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, err
	}
	database, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, err
	}

	run, found, err := loadGORMWorkspaceAnalysisToolRunByWorkflowForUpdate(
		ctx, database, query.Identity.WorkspaceID, query.Identity.WorkflowRunID,
	)
	if err != nil || !found {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, err
	}
	if err := validateGORMWorkspaceAnalysisToolLiveRun(run, query.Identity, run.ID); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, err
	}
	operation, found, err := loadGORMWorkspaceAnalysisToolOperationForUpdateByCall(ctx, database, run.ID, query.ToolCallID)
	if err != nil || !found {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, err
	}
	reservation, found, err := loadGORMWorkspaceAnalysisToolReservationForUpdate(ctx, database, operation.id)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, err
	}
	if !found {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, consistency(
			errors.New("workspace analysis tool call operation reservation is missing"),
		)
	}
	databaseNow, err := loadGORMWorkspaceAnalysisToolDatabaseTime(ctx, database)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, err
	}
	if err := validateGORMWorkspaceAnalysisToolLiveOperation(
		run, operation, query.Identity, operation.domain(&reservation.ID).LogicalKey(), query.RequestHash,
	); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, err
	}
	if operation.toolCallID == nil || *operation.toolCallID != query.ToolCallID ||
		operation.latestAttemptID == nil || *operation.latestAttemptID != query.Identity.NodeAttemptID {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool call lock binding differs"),
		)
	}
	if _, err := validateGORMWorkspaceAnalysisToolStoredClosure(run, operation, reservation); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, err
	}
	if err := verifyGORMWorkspaceAnalysisToolBudgetTotals(ctx, database, run); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, err
	}
	snapshot, err := buildGORMWorkspaceAnalysisToolSnapshot(run, operation, &reservation, databaseNow)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, false, err
	}
	return snapshot, true, nil
}

// ReserveWorkspaceAnalysisToolOperationScoped 在 Tool Call 已存在后预留 Agent 预算。
func (repository *GORMRepository) ReserveWorkspaceAnalysisToolOperationScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	command application.ReserveWorkspaceAnalysisToolOperationCommand,
) (application.WorkspaceAnalysisToolOperationSnapshot, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelInvalid(
			errors.New("workspace analysis tool reserve context is nil"),
		)
	}
	if err := command.Validate(); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	database, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}

	run, found, err := loadGORMWorkspaceAnalysisToolRunForUpdate(
		ctx, database, command.Identity.WorkspaceID, command.Identity.WorkflowRunID, command.OperationKey.AnalysisRunID,
	)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if !found {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool reserve run was not found"),
		)
	}
	if err := validateGORMWorkspaceAnalysisToolLiveRun(run, command.Identity, command.OperationKey.AnalysisRunID); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	operation, found, err := loadGORMWorkspaceAnalysisToolOperationForUpdateByKey(ctx, database, command.OperationKey)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if !found {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool reserve operation was not found"),
		)
	}
	_, foundReservation, err := loadGORMWorkspaceAnalysisToolReservationForUpdate(ctx, database, operation.id)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if err := validateGORMWorkspaceAnalysisToolLiveOperation(
		run, operation, command.Identity, command.OperationKey, command.RequestHash,
	); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if operation.id != command.OperationID || operation.status != domain.WorkspaceAnalysisOperationPending ||
		run.Version != command.ExpectedRunVersion || operation.version != command.ExpectedOperationVersion || foundReservation {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool reserve version or lifecycle changed"),
		)
	}
	if err := validateGORMWorkspaceAnalysisToolPending(operation); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if err := verifyGORMWorkspaceAnalysisToolBudgetTotals(ctx, database, run); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if err := proveGORMWorkspaceAnalysisToolHasNoRefusal(ctx, database, command.OperationKey); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	databaseNow, err := loadGORMWorkspaceAnalysisToolDatabaseTime(ctx, database)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if err := validateGORMWorkspaceAnalysisToolAdmission(ctx, database, run, command.OperationKey.Kind, databaseNow); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}

	reservedSourceReads := gormWorkspaceAnalysisToolReservedSourceReads(command.OperationKey.Kind)
	mutation := database.Exec(`UPDATE agent.workspace_analysis_run SET
		status='running',reserved_tool_calls=reserved_tool_calls+1,
		reserved_source_reads=reserved_source_reads+?,version=version+1,updated_at=GREATEST(updated_at,?)
		WHERE id=? AND workspace_id=? AND workflow_run_id=? AND version=? AND status IN ('queued','running')
		  AND reserved_tool_calls+settled_tool_calls+1<=max_tool_calls
		  AND reserved_source_reads+settled_source_reads+?<=max_source_reads`,
		reservedSourceReads, databaseNow, string(run.ID), string(run.WorkspaceID), string(run.WorkflowRunID),
		command.ExpectedRunVersion, reservedSourceReads,
	)
	if err := gormWorkspaceAnalysisToolCAS(ctx, mutation, "workspace analysis tool run budget CAS failed"); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}

	mutation = database.Exec(`UPDATE agent.workspace_analysis_operation SET
		status='STARTED',first_node_attempt_id=?,latest_node_attempt_id=?,tool_call_id=?,
		version=version+1,updated_at=GREATEST(updated_at,?),started_at=?
		WHERE id=? AND workspace_id=? AND analysis_run_id=? AND workflow_run_id=? AND node_run_id=?
		  AND node_key=? AND operation_kind=? AND ordinal=? AND call_kind='TOOL' AND request_hash=?
		  AND status='PENDING' AND version=?`,
		string(command.Identity.NodeAttemptID), string(command.Identity.NodeAttemptID), string(command.ToolCallID),
		databaseNow, databaseNow, string(operation.id), string(operation.workspaceID), string(operation.analysisRunID),
		string(operation.workflowRunID), string(operation.nodeRunID), string(operation.nodeKey), string(operation.kind),
		operation.ordinal, operation.requestHash, command.ExpectedOperationVersion,
	)
	if err := gormWorkspaceAnalysisToolCAS(ctx, mutation, "workspace analysis tool operation authorization CAS failed"); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}

	mutation = database.Exec(`INSERT INTO agent.workspace_analysis_budget_reservation(
		id,workspace_id,analysis_run_id,operation_id,call_kind,tool_call_id,status,
		reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,
		reserved_cost_microunits,created_at
	) VALUES(?,?,?,?, 'TOOL',?,'RESERVED',0,1,?,0,0,NULL,?)`,
		string(command.CandidateReservationID), string(command.Identity.WorkspaceID), string(command.OperationKey.AnalysisRunID),
		string(command.OperationID), string(command.ToolCallID), reservedSourceReads, databaseNow,
	)
	if mutation.Error != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, classifyGORM(ctx, mutation.Error)
	}
	if mutation.RowsAffected != 1 {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool reservation was not inserted"),
		)
	}
	snapshot, err := loadGORMWorkspaceAnalysisToolSnapshotAfterMutation(
		ctx, database, command.Identity.WorkspaceID, command.Identity.WorkflowRunID, command.OperationKey,
	)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if err := validateGORMWorkspaceAnalysisToolReservedSnapshot(snapshot, command); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	return snapshot, nil
}

// SettleWorkspaceAnalysisToolOperationScoped 在调用方事务中结算 Agent-owned Tool closure。
func (repository *GORMRepository) SettleWorkspaceAnalysisToolOperationScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	command application.SettleWorkspaceAnalysisToolOperationCommand,
) (application.WorkspaceAnalysisToolOperationSnapshot, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelInvalid(
			errors.New("workspace analysis tool settlement context is nil"),
		)
	}
	if err := command.Validate(); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	database, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}

	run, found, err := loadGORMWorkspaceAnalysisToolRunForUpdate(
		ctx, database, command.Identity.WorkspaceID, command.Identity.WorkflowRunID, command.OperationKey.AnalysisRunID,
	)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if !found {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool settlement run was not found"),
		)
	}
	if err := validateGORMWorkspaceAnalysisToolLiveRun(run, command.Identity, command.OperationKey.AnalysisRunID); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	operation, found, err := loadGORMWorkspaceAnalysisToolOperationForUpdateByKey(ctx, database, command.OperationKey)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if !found {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool settlement operation was not found"),
		)
	}
	reservation, found, err := loadGORMWorkspaceAnalysisToolReservationForUpdate(ctx, database, operation.id)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if !found {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, consistency(
			errors.New("workspace analysis tool settlement reservation is missing"),
		)
	}
	if err := validateGORMWorkspaceAnalysisToolLiveOperation(
		run, operation, command.Identity, command.OperationKey, command.RequestHash,
	); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if operation.id != command.OperationID || reservation.ID != command.ReservationID ||
		operation.toolCallID == nil || *operation.toolCallID != command.ToolCallID ||
		reservation.ToolCallID == nil || *reservation.ToolCallID != command.ToolCallID ||
		run.Version != command.ExpectedRunVersion || operation.version != command.ExpectedOperationVersion ||
		operation.status != domain.WorkspaceAnalysisOperationStarted ||
		reservation.Status != domain.WorkspaceAnalysisBudgetReserved || run.Status != domain.WorkspaceAnalysisRunRunning {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool settlement binding or version changed"),
		)
	}
	if command.Kind != application.WorkspaceAnalysisToolSettlementUnknown &&
		(operation.latestAttemptID == nil || *operation.latestAttemptID != command.Identity.NodeAttemptID) {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool settlement attempt differs"),
		)
	}
	if _, err := validateGORMWorkspaceAnalysisToolStoredClosure(run, operation, reservation); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if err := verifyGORMWorkspaceAnalysisToolBudgetTotals(ctx, database, run); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	databaseNow, err := loadGORMWorkspaceAnalysisToolDatabaseTime(ctx, database)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if operation.startedAt == nil || command.CompletedAt.Before(*operation.startedAt) ||
		command.CompletedAt.Before(reservation.CreatedAt) || command.CompletedAt.After(databaseNow) {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool settlement time is invalid"),
		)
	}

	reservationStatus := domain.WorkspaceAnalysisBudgetSettled
	if command.Kind == application.WorkspaceAnalysisToolSettlementUnknown {
		reservationStatus = domain.WorkspaceAnalysisBudgetUnknownCharged
	}
	mutation := database.Exec(`UPDATE agent.workspace_analysis_budget_reservation SET
		status=?,settled_model_calls=reserved_model_calls,settled_tool_calls=reserved_tool_calls,
		settled_source_reads=reserved_source_reads,settled_input_tokens=reserved_input_tokens,
		settled_output_tokens=reserved_output_tokens,settled_cost_microunits=reserved_cost_microunits,settled_at=?
		WHERE id=? AND workspace_id=? AND analysis_run_id=? AND operation_id=?
		  AND call_kind='TOOL' AND tool_call_id=? AND status='RESERVED'`,
		string(reservationStatus), command.CompletedAt, string(reservation.ID), string(reservation.WorkspaceID),
		string(reservation.AnalysisRunID), string(reservation.OperationID), string(command.ToolCallID),
	)
	if err := gormWorkspaceAnalysisToolCAS(ctx, mutation, "workspace analysis tool reservation settlement CAS failed"); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}

	mutation = database.Exec(`UPDATE agent.workspace_analysis_run SET
		reserved_model_calls=reserved_model_calls-?,reserved_tool_calls=reserved_tool_calls-?,
		reserved_source_reads=reserved_source_reads-?,reserved_input_tokens=reserved_input_tokens-?,
		reserved_output_tokens=reserved_output_tokens-?,settled_model_calls=settled_model_calls+?,
		settled_tool_calls=settled_tool_calls+?,settled_source_reads=settled_source_reads+?,
		settled_input_tokens=settled_input_tokens+?,settled_output_tokens=settled_output_tokens+?,
		version=version+1,updated_at=GREATEST(updated_at,?)
		WHERE id=? AND workspace_id=? AND workflow_run_id=? AND status='running' AND version=?
		  AND reserved_model_calls>=? AND reserved_tool_calls>=? AND reserved_source_reads>=?
		  AND reserved_input_tokens>=? AND reserved_output_tokens>=?`,
		reservation.Reserved.ModelCalls, reservation.Reserved.ToolCalls, reservation.Reserved.SourceReads,
		reservation.Reserved.InputTokens, reservation.Reserved.OutputTokens,
		reservation.Reserved.ModelCalls, reservation.Reserved.ToolCalls, reservation.Reserved.SourceReads,
		reservation.Reserved.InputTokens, reservation.Reserved.OutputTokens, command.CompletedAt,
		string(run.ID), string(run.WorkspaceID), string(run.WorkflowRunID), command.ExpectedRunVersion,
		reservation.Reserved.ModelCalls, reservation.Reserved.ToolCalls, reservation.Reserved.SourceReads,
		reservation.Reserved.InputTokens, reservation.Reserved.OutputTokens,
	)
	if err := gormWorkspaceAnalysisToolCAS(ctx, mutation, "workspace analysis tool run settlement CAS failed"); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}

	operationStatus, resultKind, resultID, resultHash, errorCode := gormWorkspaceAnalysisToolSettlement(command)
	mutation = database.Exec(`UPDATE agent.workspace_analysis_operation SET
		status=?,latest_node_attempt_id=?,result_kind=?,result_id=?,result_hash=?,error_code=?,
		version=version+1,updated_at=GREATEST(updated_at,?),completed_at=GREATEST(updated_at,?)
		WHERE id=? AND workspace_id=? AND analysis_run_id=? AND workflow_run_id=? AND node_run_id=?
		  AND node_key=? AND operation_kind=? AND ordinal=? AND call_kind='TOOL' AND tool_call_id=?
		  AND request_hash=? AND status='STARTED' AND version=?`,
		string(operationStatus), string(command.Identity.NodeAttemptID), resultKind, resultID, resultHash, errorCode,
		command.CompletedAt, command.CompletedAt, string(operation.id), string(operation.workspaceID),
		string(operation.analysisRunID), string(operation.workflowRunID), string(operation.nodeRunID),
		string(operation.nodeKey), string(operation.kind), operation.ordinal, string(command.ToolCallID),
		operation.requestHash, command.ExpectedOperationVersion,
	)
	if err := gormWorkspaceAnalysisToolCAS(ctx, mutation, "workspace analysis tool operation settlement CAS failed"); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	snapshot, err := loadGORMWorkspaceAnalysisToolSnapshotAfterMutation(
		ctx, database, command.Identity.WorkspaceID, command.Identity.WorkflowRunID, command.OperationKey,
	)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if err := validateGORMWorkspaceAnalysisToolSettledSnapshot(snapshot, command); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	return snapshot, nil
}

// AdvanceWorkspaceAnalysisToolOperationAttemptScoped 只推进 terminal replay 的 latest Attempt。
func (repository *GORMRepository) AdvanceWorkspaceAnalysisToolOperationAttemptScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	command application.AdvanceWorkspaceAnalysisToolOperationAttemptCommand,
) (domain.WorkspaceAnalysisOperation, error) {
	if ctx == nil {
		return domain.WorkspaceAnalysisOperation{}, workspaceAnalysisModelInvalid(
			errors.New("workspace analysis tool attempt advance context is nil"),
		)
	}
	if err := command.Validate(); err != nil {
		return domain.WorkspaceAnalysisOperation{}, err
	}
	database, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return domain.WorkspaceAnalysisOperation{}, err
	}

	run, found, err := loadGORMWorkspaceAnalysisToolRunForUpdate(
		ctx, database, command.Identity.WorkspaceID, command.Identity.WorkflowRunID, command.OperationKey.AnalysisRunID,
	)
	if err != nil {
		return domain.WorkspaceAnalysisOperation{}, err
	}
	if !found {
		return domain.WorkspaceAnalysisOperation{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool attempt advance run was not found"),
		)
	}
	if err := validateGORMWorkspaceAnalysisToolLiveRun(run, command.Identity, command.OperationKey.AnalysisRunID); err != nil {
		return domain.WorkspaceAnalysisOperation{}, err
	}
	operation, found, err := loadGORMWorkspaceAnalysisToolOperationForUpdateByKey(ctx, database, command.OperationKey)
	if err != nil {
		return domain.WorkspaceAnalysisOperation{}, err
	}
	if !found {
		return domain.WorkspaceAnalysisOperation{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool attempt advance operation was not found"),
		)
	}
	reservation, found, err := loadGORMWorkspaceAnalysisToolReservationForUpdate(ctx, database, operation.id)
	if err != nil {
		return domain.WorkspaceAnalysisOperation{}, err
	}
	if !found {
		return domain.WorkspaceAnalysisOperation{}, consistency(
			errors.New("workspace analysis tool attempt advance reservation is missing"),
		)
	}
	if err := validateGORMWorkspaceAnalysisToolLiveOperation(
		run, operation, command.Identity, command.OperationKey, operation.requestHash,
	); err != nil {
		return domain.WorkspaceAnalysisOperation{}, err
	}
	if operation.id != command.OperationID || operation.toolCallID == nil || *operation.toolCallID != command.ToolCallID ||
		!gormWorkspaceAnalysisToolTerminal(operation.status) {
		return domain.WorkspaceAnalysisOperation{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool attempt advance binding differs"),
		)
	}
	persisted, err := validateGORMWorkspaceAnalysisToolStoredClosure(run, operation, reservation)
	if err != nil {
		return domain.WorkspaceAnalysisOperation{}, err
	}
	if err := verifyGORMWorkspaceAnalysisToolBudgetTotals(ctx, database, run); err != nil {
		return domain.WorkspaceAnalysisOperation{}, err
	}
	if operation.latestAttemptID != nil && *operation.latestAttemptID == command.Identity.NodeAttemptID {
		return persisted, nil
	}
	if operation.version != command.ExpectedOperationVersion {
		return domain.WorkspaceAnalysisOperation{}, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool attempt advance version changed"),
		)
	}

	mutation := database.Exec(`UPDATE agent.workspace_analysis_operation SET
		latest_node_attempt_id=?,version=version+1,updated_at=GREATEST(updated_at,replay.at)
		FROM (SELECT clock_timestamp() AS at) AS replay
		WHERE id=? AND workspace_id=? AND analysis_run_id=? AND workflow_run_id=? AND node_run_id=?
		  AND node_key=? AND operation_kind=? AND ordinal=? AND call_kind='TOOL' AND tool_call_id=?
		  AND status IN ('SUCCEEDED','FAILED','UNKNOWN') AND version=?`,
		string(command.Identity.NodeAttemptID), string(operation.id), string(operation.workspaceID),
		string(operation.analysisRunID), string(operation.workflowRunID), string(operation.nodeRunID),
		string(operation.nodeKey), string(operation.kind), operation.ordinal, string(command.ToolCallID),
		command.ExpectedOperationVersion,
	)
	if err := gormWorkspaceAnalysisToolCAS(ctx, mutation, "workspace analysis tool terminal attempt CAS failed"); err != nil {
		return domain.WorkspaceAnalysisOperation{}, err
	}

	updated, found, err := loadGORMWorkspaceAnalysisToolOperationForUpdateByKey(ctx, database, command.OperationKey)
	if err != nil {
		return domain.WorkspaceAnalysisOperation{}, err
	}
	if !found {
		return domain.WorkspaceAnalysisOperation{}, consistency(
			errors.New("workspace analysis tool advanced operation was not found"),
		)
	}
	persisted, err = validateGORMWorkspaceAnalysisToolStoredClosure(run, updated, reservation)
	if err != nil {
		return domain.WorkspaceAnalysisOperation{}, err
	}
	if updated.latestAttemptID == nil || *updated.latestAttemptID != command.Identity.NodeAttemptID ||
		updated.version != command.ExpectedOperationVersion+1 {
		return domain.WorkspaceAnalysisOperation{}, consistency(
			errors.New("workspace analysis tool advanced attempt was not persisted"),
		)
	}
	return persisted, nil
}

// VerifyWorkspaceAnalysisToolClosureScoped 验证 durable Agent closure，不重新执行 live admission。
func (repository *GORMRepository) VerifyWorkspaceAnalysisToolClosureScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	query application.WorkspaceAnalysisToolClosureQuery,
) (application.WorkspaceAnalysisToolClosure, bool, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisToolClosure{}, false, workspaceAnalysisModelInvalid(
			errors.New("workspace analysis tool closure context is nil"),
		)
	}
	if err := query.Validate(); err != nil {
		return application.WorkspaceAnalysisToolClosure{}, false, err
	}
	database, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return application.WorkspaceAnalysisToolClosure{}, false, err
	}

	run, found, err := loadGORMWorkspaceAnalysisToolRunForShare(
		ctx, database, query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID,
	)
	if err != nil || !found {
		return application.WorkspaceAnalysisToolClosure{}, false, err
	}
	operation, found, err := loadGORMWorkspaceAnalysisToolOperationForShareByKey(ctx, database, query.OperationKey)
	if err != nil || !found {
		return application.WorkspaceAnalysisToolClosure{}, false, err
	}
	if operation.id != query.OperationID || operation.workspaceID != query.WorkspaceID ||
		operation.analysisRunID != query.AnalysisRunID || operation.workflowRunID != query.WorkflowRunID ||
		operation.requestHash != query.RequestHash || operation.toolCallID == nil || *operation.toolCallID != query.ToolCallID {
		return application.WorkspaceAnalysisToolClosure{}, false, consistency(
			errors.New("workspace analysis tool durable operation binding differs"),
		)
	}
	if operation.status == domain.WorkspaceAnalysisOperationPending {
		return application.WorkspaceAnalysisToolClosure{}, false, consistency(
			errors.New("workspace analysis tool durable operation is pending"),
		)
	}
	reservation, found, err := loadGORMWorkspaceAnalysisToolReservationForShare(ctx, database, operation.id)
	if err != nil {
		return application.WorkspaceAnalysisToolClosure{}, false, err
	}
	if !found {
		return application.WorkspaceAnalysisToolClosure{}, false, consistency(
			errors.New("workspace analysis tool durable reservation is missing"),
		)
	}
	if reservation.ID != query.ReservationID || reservation.ToolCallID == nil || *reservation.ToolCallID != query.ToolCallID {
		return application.WorkspaceAnalysisToolClosure{}, false, consistency(
			errors.New("workspace analysis tool durable reservation binding differs"),
		)
	}
	persisted, err := validateGORMWorkspaceAnalysisToolStoredClosure(run, operation, reservation)
	if err != nil {
		return application.WorkspaceAnalysisToolClosure{}, false, err
	}
	if err := verifyGORMWorkspaceAnalysisToolBudgetTotals(ctx, database, run); err != nil {
		return application.WorkspaceAnalysisToolClosure{}, false, err
	}
	closure := application.WorkspaceAnalysisToolClosure{Run: run, Operation: persisted, Reservation: reservation}
	if err := closure.Validate(); err != nil {
		return application.WorkspaceAnalysisToolClosure{}, false, consistency(err)
	}
	return closure, true, nil
}

func gormWorkspaceAnalysisToolTransaction(
	repository *GORMRepository,
	ctx context.Context,
	scope foundation.TransactionScope,
) (*gorm.DB, error) {
	if repository == nil || !validAgentGORMDatabase(repository.database) || nilAgentDependency(repository.unitOfWork) {
		return nil, workspaceAnalysisModelUnavailable(errors.New("workspace analysis tool repository is unavailable"))
	}
	if err := ctx.Err(); err != nil {
		return nil, classifyGORM(ctx, err)
	}
	database, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return nil, workspaceAnalysisModelUnavailable(errors.Join(
			errors.New("workspace analysis tool scope is unavailable"),
			err,
		))
	}
	if !validAgentGORMDatabase(database) {
		return nil, workspaceAnalysisModelUnavailable(errors.New("workspace analysis tool scope is unavailable"))
	}
	return database.WithContext(ctx), nil
}

func loadGORMWorkspaceAnalysisToolRunForUpdate(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
	analysisRunID foundation.ID,
) (domain.WorkspaceAnalysisRun, bool, error) {
	return loadGORMWorkspaceAnalysisToolRun(ctx, database, `SELECT `+workspaceAnalysisRunColumns+`
		FROM agent.workspace_analysis_run
		WHERE id=? AND workspace_id=? AND workflow_run_id=? FOR UPDATE`,
		string(analysisRunID), string(workspaceID), string(workflowRunID),
	)
}

func loadGORMWorkspaceAnalysisToolRunByWorkflowForUpdate(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
) (domain.WorkspaceAnalysisRun, bool, error) {
	return loadGORMWorkspaceAnalysisToolRun(ctx, database, `SELECT `+workspaceAnalysisRunColumns+`
		FROM agent.workspace_analysis_run
		WHERE workspace_id=? AND workflow_run_id=? FOR UPDATE`,
		string(workspaceID), string(workflowRunID),
	)
}

func loadGORMWorkspaceAnalysisToolRunForShare(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
	analysisRunID foundation.ID,
) (domain.WorkspaceAnalysisRun, bool, error) {
	return loadGORMWorkspaceAnalysisToolRun(ctx, database, `SELECT `+workspaceAnalysisRunColumns+`
		FROM agent.workspace_analysis_run
		WHERE id=? AND workspace_id=? AND workflow_run_id=? FOR SHARE`,
		string(analysisRunID), string(workspaceID), string(workflowRunID),
	)
}

func loadGORMWorkspaceAnalysisToolRun(
	ctx context.Context,
	database *gorm.DB,
	query string,
	arguments ...any,
) (domain.WorkspaceAnalysisRun, bool, error) {
	row, err := gormRawRow(database, query, arguments...)
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, false, classifyGORM(ctx, err)
	}
	run, err := scanWorkspaceAnalysisRun(row)
	if gormNoRows(err) {
		return domain.WorkspaceAnalysisRun{}, false, nil
	}
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, false, classifyGORM(ctx, err)
	}
	run = canonicalGORMWorkspaceAnalysisToolRun(run)
	if err := domain.ValidateWorkspaceAnalysisRun(run); err != nil {
		return domain.WorkspaceAnalysisRun{}, false, consistency(err)
	}
	return run, true, nil
}

func loadGORMWorkspaceAnalysisToolOperationForUpdateByKey(
	ctx context.Context,
	database *gorm.DB,
	key domain.WorkspaceAnalysisOperationKey,
) (gormWorkspaceAnalysisToolOperationRecord, bool, error) {
	return loadGORMWorkspaceAnalysisToolOperation(ctx, database, `SELECT `+gormWorkspaceAnalysisToolOperationColumns+`
		FROM agent.workspace_analysis_operation AS operation
		WHERE operation.analysis_run_id=? AND operation.node_key=? AND operation.operation_kind=? AND operation.ordinal=?
		FOR UPDATE`, string(key.AnalysisRunID), string(key.NodeKey), string(key.Kind), key.Ordinal)
}

func loadGORMWorkspaceAnalysisToolOperationForUpdateByCall(
	ctx context.Context,
	database *gorm.DB,
	analysisRunID foundation.ID,
	toolCallID foundation.ID,
) (gormWorkspaceAnalysisToolOperationRecord, bool, error) {
	return loadGORMWorkspaceAnalysisToolOperation(ctx, database, `SELECT `+gormWorkspaceAnalysisToolOperationColumns+`
		FROM agent.workspace_analysis_operation AS operation
		WHERE operation.analysis_run_id=? AND operation.tool_call_id=? FOR UPDATE`,
		string(analysisRunID), string(toolCallID))
}

func loadGORMWorkspaceAnalysisToolOperationForShareByKey(
	ctx context.Context,
	database *gorm.DB,
	key domain.WorkspaceAnalysisOperationKey,
) (gormWorkspaceAnalysisToolOperationRecord, bool, error) {
	return loadGORMWorkspaceAnalysisToolOperation(ctx, database, `SELECT `+gormWorkspaceAnalysisToolOperationColumns+`
		FROM agent.workspace_analysis_operation AS operation
		WHERE operation.analysis_run_id=? AND operation.node_key=? AND operation.operation_kind=? AND operation.ordinal=?
		FOR SHARE`, string(key.AnalysisRunID), string(key.NodeKey), string(key.Kind), key.Ordinal)
}

func loadGORMWorkspaceAnalysisToolOperation(
	ctx context.Context,
	database *gorm.DB,
	query string,
	arguments ...any,
) (gormWorkspaceAnalysisToolOperationRecord, bool, error) {
	row, err := gormRawRow(database, query, arguments...)
	if err != nil {
		return gormWorkspaceAnalysisToolOperationRecord{}, false, classifyGORM(ctx, err)
	}
	operation, err := scanGORMWorkspaceAnalysisToolOperation(row)
	if gormNoRows(err) {
		return gormWorkspaceAnalysisToolOperationRecord{}, false, nil
	}
	if err != nil {
		return gormWorkspaceAnalysisToolOperationRecord{}, false, classifyGORM(ctx, err)
	}
	return operation, true, nil
}

func loadGORMWorkspaceAnalysisToolReservationForUpdate(
	ctx context.Context,
	database *gorm.DB,
	operationID foundation.ID,
) (domain.WorkspaceAnalysisBudgetReservation, bool, error) {
	return loadGORMWorkspaceAnalysisToolReservation(ctx, database, `SELECT `+gormWorkspaceAnalysisToolReservationColumns+`
		FROM agent.workspace_analysis_budget_reservation AS reservation
		WHERE reservation.operation_id=? FOR UPDATE`, string(operationID))
}

func loadGORMWorkspaceAnalysisToolReservationForShare(
	ctx context.Context,
	database *gorm.DB,
	operationID foundation.ID,
) (domain.WorkspaceAnalysisBudgetReservation, bool, error) {
	return loadGORMWorkspaceAnalysisToolReservation(ctx, database, `SELECT `+gormWorkspaceAnalysisToolReservationColumns+`
		FROM agent.workspace_analysis_budget_reservation AS reservation
		WHERE reservation.operation_id=? FOR SHARE`, string(operationID))
}

func loadGORMWorkspaceAnalysisToolReservation(
	ctx context.Context,
	database *gorm.DB,
	query string,
	arguments ...any,
) (domain.WorkspaceAnalysisBudgetReservation, bool, error) {
	row, err := gormRawRow(database, query, arguments...)
	if err != nil {
		return domain.WorkspaceAnalysisBudgetReservation{}, false, classifyGORM(ctx, err)
	}
	reservation, err := scanGORMWorkspaceAnalysisToolReservation(row)
	if gormNoRows(err) {
		return domain.WorkspaceAnalysisBudgetReservation{}, false, nil
	}
	if err != nil {
		return domain.WorkspaceAnalysisBudgetReservation{}, false, classifyGORM(ctx, err)
	}
	return reservation, true, nil
}

func loadGORMWorkspaceAnalysisToolDatabaseTime(ctx context.Context, database *gorm.DB) (time.Time, error) {
	row, err := gormRawRow(database, `SELECT clock_timestamp()`)
	if err != nil {
		return time.Time{}, classifyGORM(ctx, err)
	}
	var databaseNow time.Time
	if err := row.Scan(&databaseNow); err != nil {
		return time.Time{}, classifyGORM(ctx, err)
	}
	return gormWorkspaceAnalysisToolTime(databaseNow), nil
}

func loadGORMWorkspaceAnalysisToolSnapshotAfterMutation(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
	key domain.WorkspaceAnalysisOperationKey,
) (application.WorkspaceAnalysisToolOperationSnapshot, error) {
	run, found, err := loadGORMWorkspaceAnalysisToolRunForUpdate(
		ctx, database, workspaceID, workflowRunID, key.AnalysisRunID,
	)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if !found {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, consistency(
			errors.New("workspace analysis tool mutated run was not found"),
		)
	}
	operation, found, err := loadGORMWorkspaceAnalysisToolOperationForUpdateByKey(ctx, database, key)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if !found {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, consistency(
			errors.New("workspace analysis tool mutated operation was not found"),
		)
	}
	reservation, found, err := loadGORMWorkspaceAnalysisToolReservationForUpdate(ctx, database, operation.id)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if !found {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, consistency(
			errors.New("workspace analysis tool mutated reservation was not found"),
		)
	}
	if _, err := validateGORMWorkspaceAnalysisToolStoredClosure(run, operation, reservation); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	if err := verifyGORMWorkspaceAnalysisToolBudgetTotals(ctx, database, run); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	databaseNow, err := loadGORMWorkspaceAnalysisToolDatabaseTime(ctx, database)
	if err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, err
	}
	return buildGORMWorkspaceAnalysisToolSnapshot(run, operation, &reservation, databaseNow)
}

func validateGORMWorkspaceAnalysisToolLiveRun(
	run domain.WorkspaceAnalysisRun,
	identity application.WorkspaceAnalysisToolExecutionIdentity,
	analysisRunID foundation.ID,
) error {
	if run.ID != analysisRunID || run.WorkspaceID != identity.WorkspaceID || run.WorkflowRunID != identity.WorkflowRunID ||
		run.DefinitionVersion != identity.DefinitionVersion || run.DefinitionHash != identity.DefinitionHash || !run.Status.Active() {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis tool run binding is stale"))
	}
	return nil
}

func validateGORMWorkspaceAnalysisToolLiveOperation(
	run domain.WorkspaceAnalysisRun,
	operation gormWorkspaceAnalysisToolOperationRecord,
	identity application.WorkspaceAnalysisToolExecutionIdentity,
	key domain.WorkspaceAnalysisOperationKey,
	requestHash string,
) error {
	if operation.workspaceID != identity.WorkspaceID || operation.analysisRunID != key.AnalysisRunID ||
		operation.workflowRunID != identity.WorkflowRunID || operation.nodeRunID != identity.NodeRunID ||
		operation.nodeKey != identity.NodeKey || operation.nodeKey != key.NodeKey || operation.kind != key.Kind ||
		operation.ordinal != key.Ordinal || operation.callKind != domain.WorkspaceAnalysisOperationCallTool ||
		operation.requestHash != requestHash || run.ID != operation.analysisRunID || run.WorkspaceID != operation.workspaceID ||
		run.WorkflowRunID != operation.workflowRunID {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis tool operation scope or request differs"))
	}
	return nil
}

func validateGORMWorkspaceAnalysisToolPending(operation gormWorkspaceAnalysisToolOperationRecord) error {
	if err := domain.ValidateWorkspaceAnalysisOperation(operation.domain(nil)); err != nil {
		return consistency(err)
	}
	return nil
}

func validateGORMWorkspaceAnalysisToolStoredClosure(
	run domain.WorkspaceAnalysisRun,
	operation gormWorkspaceAnalysisToolOperationRecord,
	reservation domain.WorkspaceAnalysisBudgetReservation,
) (domain.WorkspaceAnalysisOperation, error) {
	if operation.workspaceID != run.WorkspaceID || operation.analysisRunID != run.ID ||
		operation.workflowRunID != run.WorkflowRunID || operation.callKind != domain.WorkspaceAnalysisOperationCallTool ||
		reservation.WorkspaceID != run.WorkspaceID || reservation.AnalysisRunID != run.ID ||
		reservation.OperationID != operation.id || reservation.CallKind != domain.WorkspaceAnalysisOperationCallTool ||
		operation.toolCallID == nil || reservation.ToolCallID == nil || *operation.toolCallID != *reservation.ToolCallID {
		return domain.WorkspaceAnalysisOperation{}, consistency(
			errors.New("workspace analysis tool stored closure scope differs"),
		)
	}
	persisted := operation.domain(&reservation.ID)
	if err := domain.ValidateWorkspaceAnalysisOperation(persisted); err != nil {
		return domain.WorkspaceAnalysisOperation{}, consistency(err)
	}
	if err := domain.ValidateWorkspaceAnalysisBudgetReservationBinding(run, reservation, persisted); err != nil {
		return domain.WorkspaceAnalysisOperation{}, consistency(err)
	}
	if err := domain.ValidateWorkspaceAnalysisOperationCallBinding(persisted, domain.WorkspaceAnalysisCanonicalCallBinding{
		Kind: domain.WorkspaceAnalysisOperationCallTool, CallID: *operation.toolCallID, RequestHash: operation.requestHash,
	}); err != nil {
		return domain.WorkspaceAnalysisOperation{}, consistency(err)
	}
	return persisted, nil
}

func buildGORMWorkspaceAnalysisToolSnapshot(
	run domain.WorkspaceAnalysisRun,
	operation gormWorkspaceAnalysisToolOperationRecord,
	reservation *domain.WorkspaceAnalysisBudgetReservation,
	databaseNow time.Time,
) (application.WorkspaceAnalysisToolOperationSnapshot, error) {
	var reservationID *foundation.ID
	if reservation != nil {
		value := reservation.ID
		reservationID = &value
	}
	snapshot := application.WorkspaceAnalysisToolOperationSnapshot{
		Run: run, Operation: operation.domain(reservationID), Reservation: reservation, DatabaseNow: databaseNow,
	}
	if err := snapshot.Validate(); err != nil {
		return application.WorkspaceAnalysisToolOperationSnapshot{}, consistency(err)
	}
	return snapshot, nil
}

func validateGORMWorkspaceAnalysisToolReservedSnapshot(
	snapshot application.WorkspaceAnalysisToolOperationSnapshot,
	command application.ReserveWorkspaceAnalysisToolOperationCommand,
) error {
	operation := snapshot.Operation
	reservation := snapshot.Reservation
	if reservation == nil || snapshot.Run.Version != command.ExpectedRunVersion+1 ||
		snapshot.Run.Status != domain.WorkspaceAnalysisRunRunning || operation.ID != command.OperationID ||
		operation.Version != command.ExpectedOperationVersion+1 || operation.Status != domain.WorkspaceAnalysisOperationStarted ||
		operation.FirstNodeAttemptID == nil || *operation.FirstNodeAttemptID != command.Identity.NodeAttemptID ||
		operation.LatestNodeAttemptID == nil || *operation.LatestNodeAttemptID != command.Identity.NodeAttemptID ||
		operation.Call == nil || operation.Call.Kind != domain.WorkspaceAnalysisOperationCallTool ||
		operation.Call.ID != command.ToolCallID || operation.BudgetReservationID == nil ||
		*operation.BudgetReservationID != command.CandidateReservationID || reservation.ID != command.CandidateReservationID ||
		reservation.Status != domain.WorkspaceAnalysisBudgetReserved || reservation.ToolCallID == nil ||
		*reservation.ToolCallID != command.ToolCallID || operation.StartedAt == nil ||
		!operation.StartedAt.Equal(reservation.CreatedAt) {
		return consistency(errors.New("workspace analysis tool reserved snapshot differs"))
	}
	return nil
}

func validateGORMWorkspaceAnalysisToolSettledSnapshot(
	snapshot application.WorkspaceAnalysisToolOperationSnapshot,
	command application.SettleWorkspaceAnalysisToolOperationCommand,
) error {
	operation := snapshot.Operation
	reservation := snapshot.Reservation
	expectedOperationStatus, _, _, _, _ := gormWorkspaceAnalysisToolSettlement(command)
	expectedReservationStatus := domain.WorkspaceAnalysisBudgetSettled
	if command.Kind == application.WorkspaceAnalysisToolSettlementUnknown {
		expectedReservationStatus = domain.WorkspaceAnalysisBudgetUnknownCharged
	}
	if reservation == nil || snapshot.Run.Version != command.ExpectedRunVersion+1 ||
		operation.ID != command.OperationID || operation.Version != command.ExpectedOperationVersion+1 ||
		operation.Status != expectedOperationStatus || operation.LatestNodeAttemptID == nil ||
		*operation.LatestNodeAttemptID != command.Identity.NodeAttemptID || operation.Call == nil ||
		operation.Call.Kind != domain.WorkspaceAnalysisOperationCallTool || operation.Call.ID != command.ToolCallID ||
		operation.BudgetReservationID == nil || *operation.BudgetReservationID != command.ReservationID ||
		reservation.ID != command.ReservationID || reservation.Status != expectedReservationStatus ||
		reservation.ToolCallID == nil || *reservation.ToolCallID != command.ToolCallID ||
		reservation.SettledAt == nil || !reservation.SettledAt.Equal(command.CompletedAt) ||
		operation.CompletedAt == nil || operation.CompletedAt.Before(command.CompletedAt) ||
		operation.ErrorCode != command.ErrorCode || !sameGORMWorkspaceAnalysisToolResult(operation.Result, command.Result) {
		return consistency(errors.New("workspace analysis tool settled snapshot differs"))
	}
	return nil
}

func sameGORMWorkspaceAnalysisToolResult(
	left *domain.WorkspaceAnalysisOperationResultRef,
	right *domain.WorkspaceAnalysisOperationResultRef,
) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validateGORMWorkspaceAnalysisToolAdmission(
	ctx context.Context,
	database *gorm.DB,
	run domain.WorkspaceAnalysisRun,
	kind domain.WorkspaceAnalysisOperationKind,
	databaseNow time.Time,
) error {
	reservedSourceReads := gormWorkspaceAnalysisToolReservedSourceReads(kind)
	if run.Reserved.ToolCalls+run.Settled.ToolCalls+1 > run.Limits.Amount.ToolCalls ||
		run.Reserved.SourceReads+run.Settled.SourceReads+reservedSourceReads > run.Limits.Amount.SourceReads {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis tool budget is exhausted"))
	}
	row, err := gormRawRow(database, `SELECT NOT EXISTS(
		SELECT 1 FROM agent.workspace_analysis_operation
		WHERE analysis_run_id=? AND call_kind='TOOL' AND status='STARTED'
	)`, string(run.ID))
	if err != nil {
		return classifyGORM(ctx, err)
	}
	var concurrencyAvailable bool
	if err := row.Scan(&concurrencyAvailable); err != nil {
		return classifyGORM(ctx, err)
	}
	if !concurrencyAvailable {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis tool concurrency is exhausted"))
	}
	timeout, ok := gormWorkspaceAnalysisToolTimeout(run, kind)
	if !ok || timeout <= 0 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis tool timeout is invalid"))
	}
	if databaseNow.Add(timeout + domain.WorkspaceAnalysisV1DurableCompletionMargin).After(run.DeadlineAt) {
		return domain.NewWorkspaceAnalysisPreAuthorizationDeadlineError()
	}
	return nil
}

func verifyGORMWorkspaceAnalysisToolBudgetTotals(
	ctx context.Context,
	database *gorm.DB,
	run domain.WorkspaceAnalysisRun,
) error {
	row, err := gormRawRow(database, `SELECT
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
		FROM agent.workspace_analysis_budget_reservation WHERE analysis_run_id=?`, string(run.ID))
	if err != nil {
		return classifyGORM(ctx, err)
	}
	var reservedModelCalls, reservedToolCalls, reservedSourceReads int64
	var reservedInputTokens, reservedOutputTokens int64
	var settledModelCalls, settledToolCalls, settledSourceReads int64
	var settledInputTokens, settledOutputTokens int64
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
		return consistency(errors.New("workspace analysis tool budget totals drifted"))
	}
	return nil
}

func proveGORMWorkspaceAnalysisToolHasNoRefusal(
	ctx context.Context,
	database *gorm.DB,
	key domain.WorkspaceAnalysisOperationKey,
) error {
	row, err := gormRawRow(database, `SELECT EXISTS(
		SELECT 1 FROM agent.workspace_analysis_tool_refusal
		WHERE analysis_run_id=? AND node_key=? AND operation_kind=? AND ordinal=?
	)`, string(key.AnalysisRunID), string(key.NodeKey), string(key.Kind), key.Ordinal)
	if err != nil {
		return classifyGORM(ctx, err)
	}
	var exists bool
	if err := row.Scan(&exists); err != nil {
		return classifyGORM(ctx, err)
	}
	if exists {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis tool slot already has a refusal"))
	}
	return nil
}

func gormWorkspaceAnalysisToolCAS(ctx context.Context, mutation *gorm.DB, message string) error {
	if mutation == nil {
		return workspaceAnalysisModelUnavailable(errors.New("workspace analysis tool mutation is unavailable"))
	}
	if mutation.Error != nil {
		return classifyGORM(ctx, mutation.Error)
	}
	if mutation.RowsAffected != 1 {
		return workspaceAnalysisModelConflict(errors.New(message))
	}
	return nil
}

func gormWorkspaceAnalysisToolSettlement(
	command application.SettleWorkspaceAnalysisToolOperationCommand,
) (domain.WorkspaceAnalysisOperationStatus, any, any, any, any) {
	var resultKind, resultID, resultHash, errorCode any
	if command.Result != nil {
		resultKind, resultID, resultHash = string(command.Result.Kind), string(command.Result.ID), command.Result.Hash
	}
	if command.ErrorCode != "" {
		errorCode = command.ErrorCode
	}
	switch command.Kind {
	case application.WorkspaceAnalysisToolSettlementSucceeded:
		return domain.WorkspaceAnalysisOperationSucceeded, resultKind, resultID, resultHash, nil
	case application.WorkspaceAnalysisToolSettlementUnknown:
		return domain.WorkspaceAnalysisOperationUnknown, nil, nil, nil, errorCode
	default:
		return domain.WorkspaceAnalysisOperationFailed, nil, nil, nil, errorCode
	}
}

func gormWorkspaceAnalysisToolReservedSourceReads(kind domain.WorkspaceAnalysisOperationKind) int {
	if kind == domain.WorkspaceAnalysisOperationSourceRead {
		return 1
	}
	return 0
}

func gormWorkspaceAnalysisToolTimeout(
	run domain.WorkspaceAnalysisRun,
	kind domain.WorkspaceAnalysisOperationKind,
) (time.Duration, bool) {
	switch kind {
	case domain.WorkspaceAnalysisOperationGitStatus:
		return run.Timeouts.GitToolTimeout, true
	case domain.WorkspaceAnalysisOperationKnowledgeSearch:
		return run.Timeouts.SearchToolTimeout, true
	case domain.WorkspaceAnalysisOperationSourceRead:
		return run.Timeouts.SourceReadToolTimeout, true
	case domain.WorkspaceAnalysisOperationCitationValidation:
		return run.Timeouts.ValidateCitationToolTimeout, true
	default:
		return 0, false
	}
}

func gormWorkspaceAnalysisToolTerminal(status domain.WorkspaceAnalysisOperationStatus) bool {
	return status == domain.WorkspaceAnalysisOperationSucceeded || status == domain.WorkspaceAnalysisOperationFailed ||
		status == domain.WorkspaceAnalysisOperationUnknown
}

func canonicalGORMWorkspaceAnalysisToolRun(run domain.WorkspaceAnalysisRun) domain.WorkspaceAnalysisRun {
	run.CreatedAt = gormWorkspaceAnalysisToolTime(run.CreatedAt)
	run.UpdatedAt = gormWorkspaceAnalysisToolTime(run.UpdatedAt)
	run.DeadlineAt = gormWorkspaceAnalysisToolTime(run.DeadlineAt)
	run.CompletedAt = gormWorkspaceAnalysisOptionalTime(run.CompletedAt)
	return run
}

var _ application.ScopedWorkspaceAnalysisToolParticipant = (*GORMRepository)(nil)
