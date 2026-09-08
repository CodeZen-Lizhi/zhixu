package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

// GORMWorkspaceAnalysisRepository 在同一平台事务中持久化 Workspace Analysis 模型操作。
type GORMWorkspaceAnalysisRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
	fence      workflowapplication.ScopedWorkspaceAnalysisExecutionFence
}

var _ application.WorkspaceAnalysisModelOperationRepository = (*GORMWorkspaceAnalysisRepository)(nil)
var _ application.WorkspaceAnalysisRetrievalPlanCheckpointReader = (*GORMWorkspaceAnalysisRepository)(nil)
var _ application.WorkspaceAnalysisCandidateAuthorityReader = (*GORMWorkspaceAnalysisRepository)(nil)

// NewGORMWorkspaceAnalysisRepository 从同一共享 Pool 派生 GORM root 与 UoW，并强制
// 接收非 nil/typed-nil 的 Workflow scoped fence；不提供默认或 fallback。
func NewGORMWorkspaceAnalysisRepository(
	pool *platformpostgres.Pool,
	fence workflowapplication.ScopedWorkspaceAnalysisExecutionFence,
) (*GORMWorkspaceAnalysisRepository, error) {
	if pool == nil {
		return nil, workspaceAnalysisModelUnavailable(errors.New("workspace analysis model pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, workspaceAnalysisModelUnavailable(errors.Join(
			errors.New("workspace analysis model GORM database is unavailable"), err,
		))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, workspaceAnalysisModelUnavailable(errors.Join(
			errors.New("workspace analysis model GORM unit of work is unavailable"), err,
		))
	}
	if !validAgentGORMDatabase(database) || nilAgentDependency(unitOfWork) || nilAgentDependency(fence) {
		return nil, workspaceAnalysisModelUnavailable(errors.New("workspace analysis model GORM dependencies are unavailable"))
	}
	return &GORMWorkspaceAnalysisRepository{database: database, unitOfWork: unitOfWork, fence: fence}, nil
}

// within 在一个 UoW 内执行 callback，向 callback 提供 scope 绑定的 GORM 事务。
func (repository *GORMWorkspaceAnalysisRepository) within(
	ctx context.Context,
	work func(context.Context, *gorm.DB, foundation.TransactionScope) error,
) error {
	if repository == nil || !validAgentGORMDatabase(repository.database) || nilAgentDependency(repository.unitOfWork) ||
		nilAgentDependency(repository.fence) {
		return workspaceAnalysisModelUnavailable(errors.New("workspace analysis model repository is unavailable"))
	}
	if ctx == nil {
		return workspaceAnalysisModelInvalid(errors.New("workspace analysis model context is nil"))
	}
	if work == nil {
		return workspaceAnalysisModelInvalid(errors.New("workspace analysis model transaction callback is nil"))
	}
	err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return workspaceAnalysisModelUnavailable(errors.Join(
				errors.New("workspace analysis model scoped transaction is unavailable"), err,
			))
		}
		return work(callbackCtx, transaction.WithContext(callbackCtx), scope)
	})
	return classifyGORM(ctx, err)
}

// translateGORMWorkspaceAnalysisModelFenceError 把 Workflow fence 错误翻译为 Agent
// 错误合同：found=false/快照漂移由调用方转为 authorization conflict；invalid 输入、
// scope/context 取消与未知依赖按 Agent 既有 code/retryability 归类并保留 cause，
// 不透传 Workflow 错误码。
func translateGORMWorkspaceAnalysisModelFenceError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		cause := unwrapGORMWorkspaceAnalysisModelFenceCause(err)
		if contextCause := agentContextCause(ctx, cause); contextCause != nil {
			preservedCause := errors.Join(contextCause, cause)
			if errors.Is(contextCause, context.Canceled) {
				return foundation.NewError(foundation.ErrorNonRetryableFailure, "AGENT_DATABASE_CANCELLED", false, preservedCause)
			}
			return foundation.NewError(foundation.ErrorRetryableFailure, "AGENT_DATABASE_TIMEOUT", true, preservedCause)
		}
		if platformpostgres.SQLState(cause) != "" || errors.Is(cause, sql.ErrTxDone) {
			return classifyGORM(ctx, cause)
		}
		switch classified.Kind {
		case foundation.ErrorInvalidInput:
			return workspaceAnalysisModelInvalid(cause)
		case foundation.ErrorVersionConflict, foundation.ErrorConsistencyViolation:
			return workspaceAnalysisModelConflict(cause)
		default:
			return workspaceAnalysisModelUnavailable(cause)
		}
	}
	return classifyGORM(ctx, err)
}

// unwrapGORMWorkspaceAnalysisModelFenceCause removes every cross-owner
// Foundation envelope while retaining all joined sentinel and SQL error leaves.
func unwrapGORMWorkspaceAnalysisModelFenceCause(err error) error {
	cause := stripGORMWorkspaceAnalysisModelFenceEnvelopes(err, make(map[*foundation.Error]struct{}), 0)
	if cause == nil {
		return errors.New("workflow execution fence failed without a usable cause")
	}
	return cause
}

func stripGORMWorkspaceAnalysisModelFenceEnvelopes(
	err error,
	seen map[*foundation.Error]struct{},
	depth int,
) error {
	if err == nil {
		return nil
	}
	if depth > 64 {
		return errors.New("workflow execution fence cause chain is too deep")
	}
	if classified, ok := err.(*foundation.Error); ok {
		if classified == nil || classified.Cause == nil {
			return nil
		}
		if _, exists := seen[classified]; exists {
			return errors.New("workflow execution fence cause chain is cyclic")
		}
		seen[classified] = struct{}{}
		cause := stripGORMWorkspaceAnalysisModelFenceEnvelopes(classified.Cause, seen, depth+1)
		delete(seen, classified)
		return cause
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		stripped := make([]error, 0, len(causes))
		for _, cause := range causes {
			if sanitized := stripGORMWorkspaceAnalysisModelFenceEnvelopes(cause, seen, depth+1); sanitized != nil {
				stripped = append(stripped, sanitized)
			}
		}
		return errors.Join(stripped...)
	}
	if cause := errors.Unwrap(err); cause != nil {
		return stripGORMWorkspaceAnalysisModelFenceEnvelopes(cause, seen, depth+1)
	}
	return err
}

// validateGORMWorkspaceAnalysisModelFenceSnapshotBinding rejects a fence
// implementation that reports found=true for facts outside the requested scope.
func validateGORMWorkspaceAnalysisModelFenceSnapshotBinding(
	snapshot workflowapplication.WorkspaceAnalysisExecutionFenceSnapshot,
	request workflowapplication.WorkspaceAnalysisExecutionFenceRequest,
) error {
	if snapshot.WorkspaceID != request.WorkspaceID || snapshot.WorkflowRunID != request.WorkflowRunID ||
		snapshot.NodeRunID != request.NodeRunID || snapshot.NodeAttemptID != request.NodeAttemptID {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model execution fence scope drifted"))
	}
	return nil
}

// fenceSnapshotFromScoped 把 Workflow fence 快照映射为模型操作 fence 投影，使全部
// 既有纯校验器（定义绑定、租约活性、执行 fence）原样复用。
func fenceSnapshotFromScoped(
	snapshot workflowapplication.WorkspaceAnalysisExecutionFenceSnapshot,
	databaseNow time.Time,
) workspaceAnalysisModelWorkflowFence {
	fence := workspaceAnalysisModelWorkflowFence{
		definitionID:      snapshot.DefinitionID,
		definitionKey:     snapshot.DefinitionKey,
		definitionVersion: snapshot.DefinitionVersion,
		definitionGraph:   []byte(snapshot.DefinitionGraph),
		workflowStatus:    string(snapshot.WorkflowStatus),
		databaseNow:       databaseNow,
		nodeKey:           snapshot.NodeKey,
		nodeStatus:        string(snapshot.NodeStatus),
		nodeAttempt:       int64(snapshot.NodeAttempt),
		attemptStatus:     string(snapshot.AttemptStatus),
		attemptNo:         int64(snapshot.AttemptNo),
	}
	if snapshot.CancelRequested {
		cancelledAt := databaseNow
		fence.cancelRequestedAt = &cancelledAt
	}
	if snapshot.NodeLeaseOwnerSet {
		owner := snapshot.NodeLeaseOwner
		fence.nodeOwner = &owner
	}
	if snapshot.NodeLeaseUntilSet {
		lease := snapshot.NodeLeaseUntil
		fence.nodeLease = &lease
	}
	if snapshot.AttemptLeaseOwnerSet {
		owner := snapshot.AttemptLeaseOwner
		fence.attemptOwner = &owner
	}
	if snapshot.AttemptLeaseUntilSet {
		lease := snapshot.AttemptLeaseUntil
		fence.attemptLease = &lease
	}
	return fence
}

// gormLockWorkspaceAnalysisModelOperation 按固定锁序在 caller scope 内锁定：
// Workflow fence（Run->Node Run->Node Attempt）-> Analysis Run -> Operation
// -> Reservation -> Model Run -> Model Call -> clock_timestamp()。
func gormLockWorkspaceAnalysisModelOperation(
	ctx context.Context,
	transaction *gorm.DB,
	scope foundation.TransactionScope,
	fence workflowapplication.ScopedWorkspaceAnalysisExecutionFence,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
	createOperation bool,
) (workspaceAnalysisModelLocks, error) {
	var locked workspaceAnalysisModelLocks
	if nilAgentDependency(fence) {
		return locked, workspaceAnalysisModelUnavailable(errors.New("workspace analysis model execution fence is unavailable"))
	}
	request := workflowapplication.WorkspaceAnalysisExecutionFenceRequest{
		WorkspaceID:   command.Identity.WorkspaceID,
		WorkflowRunID: command.Identity.WorkflowRunID,
		NodeRunID:     command.Identity.NodeRunID,
		NodeAttemptID: command.Identity.NodeAttemptID,
	}
	snapshot, found, err := fence.LockWorkspaceAnalysisExecutionScoped(ctx, scope, request)
	if err != nil {
		return locked, translateGORMWorkspaceAnalysisModelFenceError(ctx, err)
	}
	if !found {
		return locked, workspaceAnalysisModelConflict(errors.New("workspace analysis model authority binding was not found"))
	}
	if err := validateGORMWorkspaceAnalysisModelFenceSnapshotBinding(snapshot, request); err != nil {
		return locked, err
	}
	analysisRunRow, err := gormRawRow(transaction.WithContext(ctx), `SELECT `+workspaceAnalysisRunColumns+`
		FROM agent.workspace_analysis_run
		WHERE id=?::uuid AND workspace_id=?::uuid AND workflow_run_id=?::uuid FOR UPDATE`,
		string(command.OperationKey.AnalysisRunID), string(command.Identity.WorkspaceID), string(command.Identity.WorkflowRunID),
	)
	if err != nil {
		return locked, classifyGORM(ctx, err)
	}
	analysisRun, err := scanWorkspaceAnalysisRun(analysisRunRow)
	if err != nil {
		if gormWorkspaceAnalysisModelNoRows(err) {
			return locked, workspaceAnalysisModelConflict(errors.New("workspace analysis model authority binding was not found"))
		}
		return locked, classifyGORM(ctx, err)
	}
	locked.analysisRun = analysisRun

	if createOperation {
		insertResult := transaction.WithContext(ctx).Exec(`INSERT INTO agent.workspace_analysis_operation(
			id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
			ordinal,call_kind,request_hash,status,version,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?,'MODEL',?,'PENDING',1,clock_timestamp(),clock_timestamp())
		ON CONFLICT (analysis_run_id,node_key,operation_kind,ordinal) DO NOTHING`,
			string(command.OperationID), string(command.Identity.WorkspaceID), string(command.OperationKey.AnalysisRunID),
			string(command.Identity.WorkflowRunID), string(command.Identity.NodeRunID), string(command.OperationKey.NodeKey),
			string(command.OperationKey.Kind), command.OperationKey.Ordinal, command.Call.RequestHash,
		)
		if insertResult.Error != nil {
			return locked, classifyGORM(ctx, insertResult.Error)
		}
	}
	operationRow, err := gormRawRow(transaction.WithContext(ctx), `SELECT
		id::text,workspace_id::text,analysis_run_id::text,workflow_run_id::text,node_run_id::text,
		node_key,operation_kind,ordinal,call_kind,request_hash,status,
		first_node_attempt_id::text,latest_node_attempt_id::text,model_call_id::text,
		result_kind,result_id::text,result_hash,error_code,version,created_at,updated_at,started_at,completed_at
		FROM agent.workspace_analysis_operation
		WHERE analysis_run_id=?::uuid AND node_key=? AND operation_kind=? AND ordinal=?
		FOR UPDATE`, string(command.OperationKey.AnalysisRunID), string(command.OperationKey.NodeKey),
		string(command.OperationKey.Kind), command.OperationKey.Ordinal,
	)
	if err != nil {
		return locked, classifyGORM(ctx, err)
	}
	operation, err := scanWorkspaceAnalysisModelOperation(operationRow)
	if err != nil {
		if gormWorkspaceAnalysisModelNoRows(err) {
			return locked, workspaceAnalysisModelConflict(errors.New("workspace analysis model authority binding was not found"))
		}
		return locked, classifyGORM(ctx, err)
	}
	locked.operation = operation

	reservation, foundReservation, err := gormLoadWorkspaceAnalysisModelReservationForUpdate(ctx, transaction, operation.id)
	if err != nil {
		return locked, err
	}
	if foundReservation {
		locked.reservation = &reservation
	}
	if operation.modelCallID != nil {
		var modelRunID string
		modelRunRow, err := gormRawRow(transaction.WithContext(ctx), `SELECT model_run_id::text FROM agent.model_call WHERE id=?::uuid`, string(*operation.modelCallID))
		if err != nil {
			return locked, classifyGORM(ctx, err)
		}
		if err := modelRunRow.Scan(&modelRunID); err != nil {
			if gormWorkspaceAnalysisModelNoRows(err) {
				return locked, workspaceAnalysisModelConflict(errors.New("workspace analysis model authority binding was not found"))
			}
			return locked, classifyGORM(ctx, err)
		}
		runRow, err := gormRawRow(transaction.WithContext(ctx), gormModelRunSelect+` WHERE workspace_id=?::uuid AND id=?::uuid FOR UPDATE`,
			string(command.Identity.WorkspaceID), modelRunID,
		)
		if err != nil {
			return locked, classifyGORM(ctx, err)
		}
		run, err := scanModelRun(runRow)
		if err != nil {
			if gormWorkspaceAnalysisModelNoRows(err) {
				return locked, workspaceAnalysisModelConflict(errors.New("workspace analysis model authority binding was not found"))
			}
			return locked, classifyGORM(ctx, err)
		}
		locked.modelRun = &run
		callRow, err := gormRawRow(transaction.WithContext(ctx), gormModelCallSelect+` WHERE call.id=?::uuid AND call.model_run_id=?::uuid FOR UPDATE`,
			string(*operation.modelCallID), modelRunID,
		)
		if err != nil {
			return locked, classifyGORM(ctx, err)
		}
		call, err := scanModelCall(callRow)
		if err != nil {
			if gormWorkspaceAnalysisModelNoRows(err) {
				return locked, workspaceAnalysisModelConflict(errors.New("workspace analysis model authority binding was not found"))
			}
			return locked, classifyGORM(ctx, err)
		}
		locked.modelCall = &call
	}
	databaseNow, err := gormWorkspaceAnalysisModelDatabaseTime(ctx, transaction)
	if err != nil {
		return locked, err
	}
	locked.fence = fenceSnapshotFromScoped(snapshot, databaseNow)
	return locked, nil
}

// gormWorkspaceAnalysisModelNoRows 判断 GORM raw 查询的 no-row 结果。
func gormWorkspaceAnalysisModelNoRows(err error) bool {
	return gormNoRows(err)
}

// gormWorkspaceAnalysisModelDatabaseTime 在当前事务内读取数据库时钟。
func gormWorkspaceAnalysisModelDatabaseTime(ctx context.Context, transaction *gorm.DB) (time.Time, error) {
	row, err := gormRawRow(transaction.WithContext(ctx), `SELECT clock_timestamp()`)
	if err != nil {
		return time.Time{}, classifyGORM(ctx, err)
	}
	var now time.Time
	if err := row.Scan(&now); err != nil {
		return time.Time{}, classifyGORM(ctx, err)
	}
	return now.UTC(), nil
}

// gormLoadWorkspaceAnalysisModelReservationForUpdate 在 caller scope 内按 Operation
// 锁定读取预算 Reservation，无行时返回 found=false。
func gormLoadWorkspaceAnalysisModelReservationForUpdate(
	ctx context.Context,
	transaction *gorm.DB,
	operationID foundation.ID,
) (domain.WorkspaceAnalysisBudgetReservation, bool, error) {
	var reservation domain.WorkspaceAnalysisBudgetReservation
	var id, workspaceID, analysisRunID, persistedOperationID, callKind, status string
	var modelCallID, toolCallID *string
	row, err := gormRawRow(transaction.WithContext(ctx), `SELECT
		id::text,workspace_id::text,analysis_run_id::text,operation_id::text,call_kind,
		model_call_id::text,tool_call_id::text,status,
		reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,reserved_cost_microunits,
		settled_model_calls,settled_tool_calls,settled_source_reads,settled_input_tokens,settled_output_tokens,settled_cost_microunits,
		created_at,settled_at
		FROM agent.workspace_analysis_budget_reservation WHERE operation_id=?::uuid FOR UPDATE`, string(operationID))
	if err != nil {
		return domain.WorkspaceAnalysisBudgetReservation{}, false, classifyGORM(ctx, err)
	}
	err = row.Scan(
		&id, &workspaceID, &analysisRunID, &persistedOperationID, &callKind,
		&modelCallID, &toolCallID, &status,
		&reservation.Reserved.ModelCalls, &reservation.Reserved.ToolCalls, &reservation.Reserved.SourceReads,
		&reservation.Reserved.InputTokens, &reservation.Reserved.OutputTokens, &reservation.Reserved.CostMicrounits,
		&reservation.Settled.ModelCalls, &reservation.Settled.ToolCalls, &reservation.Settled.SourceReads,
		&reservation.Settled.InputTokens, &reservation.Settled.OutputTokens, &reservation.Settled.CostMicrounits,
		&reservation.CreatedAt, &reservation.SettledAt,
	)
	if gormWorkspaceAnalysisModelNoRows(err) {
		return domain.WorkspaceAnalysisBudgetReservation{}, false, nil
	}
	if err != nil {
		return domain.WorkspaceAnalysisBudgetReservation{}, false, classifyGORM(ctx, err)
	}
	reservation.ID, reservation.WorkspaceID, reservation.AnalysisRunID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(analysisRunID)
	reservation.OperationID = foundation.ID(persistedOperationID)
	reservation.CallKind = domain.WorkspaceAnalysisOperationCallKind(callKind)
	reservation.ModelCallID = workspaceAnalysisModelOptionalID(modelCallID)
	reservation.ToolCallID = workspaceAnalysisModelOptionalID(toolCallID)
	reservation.Status = domain.WorkspaceAnalysisBudgetReservationStatus(status)
	return reservation, true, nil
}

// AuthorizeWorkspaceAnalysisModelCall 原子创建或归约 Workspace Analysis 的唯一模型调用槽位。
func (repository *GORMWorkspaceAnalysisRepository) AuthorizeWorkspaceAnalysisModelCall(
	ctx context.Context,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
) (application.WorkspaceAnalysisModelAuthorizationResult, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelInvalid(errors.New("workspace analysis model context is nil"))
	}
	if err := command.Validate(); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	var result application.WorkspaceAnalysisModelAuthorizationResult
	callbackSucceeded := false
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		locked, err := gormLockWorkspaceAnalysisModelOperation(callbackCtx, transaction, scope, repository.fence, command, true)
		if err != nil {
			return err
		}
		if err := validateWorkspaceAnalysisModelDefinition(locked, command.Identity); err != nil {
			return err
		}
		if err := validateWorkspaceAnalysisModelFence(locked, command.Identity); err != nil {
			return err
		}
		if err := gormValidateWorkspaceAnalysisModelLockedBinding(callbackCtx, transaction, locked, command); err != nil {
			return err
		}

		switch locked.operation.status {
		case domain.WorkspaceAnalysisOperationPending:
			authorizeResult, authorizeErr := gormAuthorizePendingWorkspaceAnalysisModelCall(callbackCtx, transaction, locked, command)
			if authorizeErr != nil {
				return authorizeErr
			}
			result = authorizeResult
		case domain.WorkspaceAnalysisOperationStarted:
			if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil {
				return consistency(errors.New("started workspace analysis model operation is incomplete"))
			}
			if locked.operation.latestAttemptID != nil && *locked.operation.latestAttemptID == command.Identity.NodeAttemptID {
				result = workspaceAnalysisModelAuthorizationResult(
					*locked.modelRun, *locked.modelCall, locked.operation.id, locked.reservation.ID,
					application.WorkspaceAnalysisModelAuthorizationReconcile,
				)
				if err := result.ValidateFor(command); err != nil {
					return err
				}
				callbackSucceeded = true
				return nil
			}
			reduceResult, reduceErr := gormReducePriorWorkspaceAnalysisModelCallToUnknown(callbackCtx, transaction, locked, command)
			if reduceErr != nil {
				return reduceErr
			}
			result = reduceResult
		case domain.WorkspaceAnalysisOperationSucceeded,
			domain.WorkspaceAnalysisOperationFailed,
			domain.WorkspaceAnalysisOperationUnknown:
			terminalResult, terminalErr := gormWorkspaceAnalysisModelTerminalAuthorizationResult(callbackCtx, transaction, locked, command)
			if terminalErr != nil {
				return terminalErr
			}
			if err := gormAdvanceWorkspaceAnalysisModelTerminalAttempt(callbackCtx, transaction, locked, command.Identity.NodeAttemptID); err != nil {
				return err
			}
			result = terminalResult
		default:
			return consistency(errors.New("workspace analysis model operation status is unsupported"))
		}
		if err := gormForceWorkspaceAnalysisModelConstraints(callbackCtx, transaction); err != nil {
			return err
		}
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		if callbackSucceeded {
			return repository.recoverGORMWorkspaceAnalysisModelAuthorization(ctx, command, err)
		}
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	return result, nil
}

// LoadWorkspaceAnalysisModelResult 只读取 exact Workspace/Run/Operation/Model Run/Call 闭环中的不可变结果。
func (repository *GORMWorkspaceAnalysisRepository) LoadWorkspaceAnalysisModelResult(
	ctx context.Context,
	query application.WorkspaceAnalysisModelResultQuery,
) (domain.WorkspaceAnalysisModelResult, error) {
	if ctx == nil {
		return domain.WorkspaceAnalysisModelResult{}, workspaceAnalysisModelInvalid(errors.New("workspace analysis model context is nil"))
	}
	if err := query.Validate(); err != nil {
		return domain.WorkspaceAnalysisModelResult{}, err
	}
	if repository == nil || !validAgentGORMDatabase(repository.database) || nilAgentDependency(repository.unitOfWork) ||
		nilAgentDependency(repository.fence) {
		return domain.WorkspaceAnalysisModelResult{}, workspaceAnalysisModelUnavailable(errors.New("workspace analysis model repository is unavailable"))
	}
	row, err := gormRawRow(repository.database.WithContext(ctx), `SELECT `+workspaceAnalysisModelResultColumns+`
		FROM agent.workspace_analysis_model_result AS result
		JOIN agent.workspace_analysis_run AS analysis
		  ON analysis.id=result.analysis_run_id AND analysis.workspace_id=result.workspace_id
		JOIN agent.workspace_analysis_operation AS operation
		  ON operation.id=result.operation_id AND operation.analysis_run_id=result.analysis_run_id
		 AND operation.workspace_id=result.workspace_id AND operation.operation_kind=result.operation_kind
		 AND operation.status='SUCCEEDED' AND operation.call_kind='MODEL'
		 AND operation.model_call_id=result.model_call_id AND operation.first_node_attempt_id=result.node_attempt_id
		 AND operation.result_kind='MODEL_RESULT_RECEIPT'
		 AND operation.result_id=result.id AND operation.result_hash=result.document_hash
		JOIN agent.workspace_analysis_budget_reservation AS reservation
		  ON reservation.operation_id=operation.id AND reservation.analysis_run_id=analysis.id
		 AND reservation.model_call_id=result.model_call_id AND reservation.status='SETTLED'
		 AND reservation.settled_model_calls=1 AND reservation.settled_tool_calls=0
		 AND reservation.settled_source_reads=0
		JOIN agent.model_run AS model_run
		  ON model_run.id=result.model_run_id AND model_run.workspace_id=result.workspace_id
		 AND model_run.workflow_run_id=analysis.workflow_run_id AND model_run.node_run_id=operation.node_run_id
		 AND model_run.node_attempt_id=result.node_attempt_id AND model_run.status='SUCCEEDED'
		 AND model_run.output_schema_id=result.schema_id AND model_run.output_schema_version=result.schema_version
		 AND model_run.final_result_type=CASE result.operation_kind
		       WHEN 'RETRIEVAL_PLAN' THEN 'workspace_analysis_plan'
		       WHEN 'FAITHFULNESS_REVIEW' THEN 'faithfulness_review'
		     END
		JOIN agent.model_call AS model_call
		  ON model_call.id=result.model_call_id AND model_call.model_run_id=result.model_run_id
		 AND model_call.status='SUCCEEDED' AND model_call.response_hash=result.document_hash
		 AND model_call.response_bytes=result.document_bytes
		 AND model_call.output_schema_id=result.schema_id AND model_call.output_schema_version=result.schema_version
		 AND model_call.request_hash=operation.request_hash
		 AND reservation.settled_input_tokens=model_call.input_tokens
		 AND reservation.settled_output_tokens=model_call.output_tokens
		WHERE result.workspace_id=?::uuid AND result.analysis_run_id=?::uuid AND result.operation_id=?::uuid
		  AND result.model_run_id=?::uuid AND result.model_call_id=?::uuid`,
		string(query.WorkspaceID), string(query.AnalysisRunID), string(query.OperationID),
		string(query.ModelRunID), string(query.ModelCallID),
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

// FindWorkspaceAnalysisRetrievalPlanCheckpoint 在读取当前 Active Index 前恢复首次计划调用冻结的版本身份。
func (repository *GORMWorkspaceAnalysisRepository) FindWorkspaceAnalysisRetrievalPlanCheckpoint(
	ctx context.Context,
	query application.WorkspaceAnalysisRetrievalPlanCheckpointQuery,
) (application.WorkspaceAnalysisRetrievalPlanCheckpoint, bool, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisRetrievalPlanCheckpoint{}, false,
			workspaceAnalysisModelInvalid(errors.New("workspace analysis retrieval plan checkpoint context is nil"))
	}
	if err := query.Validate(); err != nil {
		return application.WorkspaceAnalysisRetrievalPlanCheckpoint{}, false, err
	}
	if repository == nil || !validAgentGORMDatabase(repository.database) || nilAgentDependency(repository.unitOfWork) ||
		nilAgentDependency(repository.fence) {
		return application.WorkspaceAnalysisRetrievalPlanCheckpoint{}, false,
			workspaceAnalysisModelUnavailable(errors.New("workspace analysis model repository is unavailable"))
	}

	var operationID, status, requestHash string
	var modelCallID, modelRunID, indexVersionID, embeddingVersionID, rerankModelVersion *string
	var modelResultID, resultHash *string
	row, err := gormRawRow(repository.database.WithContext(ctx), `SELECT
		operation.id::text,operation.status,operation.request_hash,
		model_call.id::text,model_run.id::text,
		model_run.retrieval_index_version_id::text,model_run.embedding_version_id::text,model_run.rerank_model_version,
		model_result.id::text,operation.result_hash
		FROM agent.workspace_analysis_operation AS operation
		LEFT JOIN agent.model_call AS model_call
		  ON model_call.id=operation.model_call_id AND model_call.request_hash=operation.request_hash
		LEFT JOIN agent.model_run AS model_run
		  ON model_run.id=model_call.model_run_id
		 AND model_run.workspace_id=operation.workspace_id
		 AND model_run.workflow_run_id=operation.workflow_run_id
		 AND model_run.node_run_id=operation.node_run_id
		 AND model_run.node_attempt_id=operation.first_node_attempt_id
		LEFT JOIN agent.workspace_analysis_model_result AS model_result
		  ON model_result.id=operation.result_id
		 AND model_result.workspace_id=operation.workspace_id
		 AND model_result.analysis_run_id=operation.analysis_run_id
		 AND model_result.operation_id=operation.id
		 AND model_result.model_run_id=model_run.id
		 AND model_result.model_call_id=model_call.id
		 AND model_result.operation_kind='RETRIEVAL_PLAN'
		 AND model_result.document_hash=operation.result_hash
		WHERE operation.workspace_id=?::uuid AND operation.workflow_run_id=?::uuid
		  AND operation.analysis_run_id=?::uuid AND operation.node_run_id=?::uuid
		  AND operation.node_key='retrieve_evidence'
		  AND operation.operation_kind='RETRIEVAL_PLAN'
		  AND operation.ordinal=1 AND operation.call_kind='MODEL'`,
		string(query.WorkspaceID), string(query.WorkflowRunID), string(query.AnalysisRunID), string(query.NodeRunID),
	)
	if err != nil {
		return application.WorkspaceAnalysisRetrievalPlanCheckpoint{}, false, classifyGORM(ctx, err)
	}
	err = row.Scan(
		&operationID, &status, &requestHash, &modelCallID, &modelRunID,
		&indexVersionID, &embeddingVersionID, &rerankModelVersion, &modelResultID, &resultHash,
	)
	if gormWorkspaceAnalysisModelNoRows(err) {
		return application.WorkspaceAnalysisRetrievalPlanCheckpoint{}, false, nil
	}
	if err != nil {
		return application.WorkspaceAnalysisRetrievalPlanCheckpoint{}, false, classifyGORM(ctx, err)
	}
	if modelCallID == nil || modelRunID == nil || indexVersionID == nil {
		return application.WorkspaceAnalysisRetrievalPlanCheckpoint{}, false,
			consistency(errors.New("workspace analysis retrieval plan checkpoint runtime binding is incomplete"))
	}
	checkpoint := application.WorkspaceAnalysisRetrievalPlanCheckpoint{
		OperationID: foundation.ID(operationID), ModelRunID: foundation.ID(*modelRunID),
		ModelCallID: foundation.ID(*modelCallID), Retrieval: domain.RetrievalRef{IndexVersionID: foundation.ID(*indexVersionID)},
		RequestHash: requestHash, Status: domain.WorkspaceAnalysisOperationStatus(status),
	}
	if embeddingVersionID != nil {
		value := foundation.ID(*embeddingVersionID)
		checkpoint.Retrieval.EmbeddingVersionID = &value
	}
	if rerankModelVersion != nil {
		checkpoint.Retrieval.RerankModelVersion = *rerankModelVersion
	}
	if modelResultID != nil {
		checkpoint.ModelResultID = foundation.ID(*modelResultID)
	}
	if resultHash != nil {
		checkpoint.ResultHash = *resultHash
	}
	if err := checkpoint.ValidateFor(query); err != nil {
		return application.WorkspaceAnalysisRetrievalPlanCheckpoint{}, false, consistency(err)
	}
	return checkpoint, true, nil
}

// LoadWorkspaceAnalysisCandidate 只读取 exact Workspace/Run/Operation/Model Run/Call 闭环中的不可变候选。
func (repository *GORMWorkspaceAnalysisRepository) LoadWorkspaceAnalysisCandidate(
	ctx context.Context,
	query application.WorkspaceAnalysisCandidateQuery,
) (domain.WorkspaceAnalysisCandidate, error) {
	if ctx == nil {
		return domain.WorkspaceAnalysisCandidate{}, workspaceAnalysisModelInvalid(errors.New("workspace analysis candidate context is nil"))
	}
	if err := query.Validate(); err != nil {
		return domain.WorkspaceAnalysisCandidate{}, err
	}
	if repository == nil || !validAgentGORMDatabase(repository.database) || nilAgentDependency(repository.unitOfWork) ||
		nilAgentDependency(repository.fence) {
		return domain.WorkspaceAnalysisCandidate{}, workspaceAnalysisModelUnavailable(errors.New("workspace analysis candidate repository is unavailable"))
	}
	row, err := gormRawRow(repository.database.WithContext(ctx), `SELECT `+workspaceAnalysisCandidateColumns+`
		FROM agent.workspace_analysis_candidate AS candidate
		JOIN agent.workspace_analysis_run AS analysis
		  ON analysis.id=candidate.analysis_run_id AND analysis.workspace_id=candidate.workspace_id
		 AND analysis.answer_id=candidate.answer_id
		JOIN agent.answer AS answer
		  ON answer.id=candidate.answer_id AND answer.workspace_id=candidate.workspace_id
		 AND answer.question_id=analysis.question_id AND answer.workflow_run_id=analysis.workflow_run_id
		JOIN agent.workspace_analysis_operation AS operation
		  ON operation.id=candidate.synthesis_operation_id AND operation.analysis_run_id=candidate.analysis_run_id
		 AND operation.workspace_id=candidate.workspace_id
		 AND operation.operation_kind='ANSWER_SYNTHESIS' AND operation.status='SUCCEEDED'
		 AND operation.call_kind='MODEL' AND operation.model_call_id=?::uuid
		 AND operation.first_node_attempt_id=candidate.node_attempt_id
		 AND operation.result_kind='SYNTHESIS_CANDIDATE'
		 AND operation.result_id=candidate.id AND operation.result_hash=candidate.document_hash
		JOIN agent.workspace_analysis_budget_reservation AS reservation
		  ON reservation.operation_id=operation.id AND reservation.analysis_run_id=analysis.id
		 AND reservation.model_call_id=operation.model_call_id AND reservation.status='SETTLED'
		 AND reservation.settled_model_calls=1 AND reservation.settled_tool_calls=0
		 AND reservation.settled_source_reads=0
		JOIN agent.model_run AS model_run
		  ON model_run.id=candidate.synthesis_model_run_id AND model_run.workspace_id=candidate.workspace_id
		 AND model_run.workflow_run_id=analysis.workflow_run_id AND model_run.node_run_id=operation.node_run_id
		 AND model_run.node_attempt_id=candidate.node_attempt_id AND model_run.status='SUCCEEDED'
		 AND model_run.final_result_type='workspace_analysis_answer'
		 AND model_run.output_schema_id=candidate.schema_id
		 AND model_run.output_schema_version=candidate.schema_version::text
		JOIN agent.model_call AS model_call
		  ON model_call.id=operation.model_call_id AND model_call.model_run_id=model_run.id
		 AND model_call.status='SUCCEEDED' AND model_call.response_hash=candidate.document_hash
		 AND model_call.response_bytes=candidate.document_bytes
		 AND model_call.output_schema_id=candidate.schema_id
		 AND model_call.output_schema_version=candidate.schema_version::text
		 AND model_call.request_hash=operation.request_hash
		 AND reservation.settled_input_tokens=model_call.input_tokens
		 AND reservation.settled_output_tokens=model_call.output_tokens
		WHERE candidate.workspace_id=?::uuid AND candidate.analysis_run_id=?::uuid
		  AND candidate.synthesis_operation_id=?::uuid AND candidate.synthesis_model_run_id=?::uuid`,
		string(query.ModelCallID),
		string(query.WorkspaceID), string(query.AnalysisRunID), string(query.OperationID),
		string(query.ModelRunID),
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

// LoadWorkspaceAnalysisCandidateAuthority 由数据库重新证明候选与成功 Synthesis 调用的完整闭包。
func (repository *GORMWorkspaceAnalysisRepository) LoadWorkspaceAnalysisCandidateAuthority(
	ctx context.Context,
	query application.WorkspaceAnalysisCandidateAuthorityQuery,
) (domain.WorkspaceAnalysisCandidate, error) {
	if ctx == nil {
		return domain.WorkspaceAnalysisCandidate{}, workspaceAnalysisModelInvalid(
			errors.New("workspace analysis candidate authority context is nil"),
		)
	}
	if err := query.Validate(); err != nil {
		return domain.WorkspaceAnalysisCandidate{}, err
	}
	if repository == nil || !validAgentGORMDatabase(repository.database) || nilAgentDependency(repository.unitOfWork) ||
		nilAgentDependency(repository.fence) {
		return domain.WorkspaceAnalysisCandidate{}, workspaceAnalysisModelUnavailable(
			errors.New("workspace analysis candidate authority repository is unavailable"),
		)
	}
	row, err := gormRawRow(repository.database.WithContext(ctx), `SELECT `+workspaceAnalysisCandidateColumns+`
		FROM agent.workspace_analysis_candidate AS candidate
		JOIN agent.workspace_analysis_run AS analysis
		  ON analysis.id=candidate.analysis_run_id AND analysis.workspace_id=candidate.workspace_id
		 AND analysis.answer_id=candidate.answer_id AND analysis.workflow_run_id=?::uuid
		JOIN agent.workspace_analysis_operation AS operation
		  ON operation.id=candidate.synthesis_operation_id AND operation.workspace_id=candidate.workspace_id
		 AND operation.analysis_run_id=candidate.analysis_run_id AND operation.workflow_run_id=analysis.workflow_run_id
		 AND operation.operation_kind='ANSWER_SYNTHESIS' AND operation.status='SUCCEEDED'
		 AND operation.call_kind='MODEL' AND operation.first_node_attempt_id=candidate.node_attempt_id
		 AND operation.result_kind='SYNTHESIS_CANDIDATE' AND operation.result_id=candidate.id
		 AND operation.result_hash=candidate.document_hash
		JOIN agent.workspace_analysis_budget_reservation AS reservation
		  ON reservation.operation_id=operation.id AND reservation.analysis_run_id=analysis.id
		 AND reservation.model_call_id=operation.model_call_id AND reservation.status='SETTLED'
		 AND reservation.settled_model_calls=1 AND reservation.settled_tool_calls=0
		 AND reservation.settled_source_reads=0
		JOIN agent.model_run AS model_run
		  ON model_run.id=candidate.synthesis_model_run_id AND model_run.workspace_id=candidate.workspace_id
		 AND model_run.workflow_run_id=analysis.workflow_run_id AND model_run.node_run_id=operation.node_run_id
		 AND model_run.node_attempt_id=candidate.node_attempt_id AND model_run.status='SUCCEEDED'
		 AND model_run.final_result_type='workspace_analysis_answer'
		 AND model_run.output_schema_id=candidate.schema_id
		 AND model_run.output_schema_version=candidate.schema_version::text
		JOIN agent.model_call AS model_call
		  ON model_call.id=operation.model_call_id AND model_call.model_run_id=model_run.id
		 AND model_call.status='SUCCEEDED' AND model_call.response_hash=candidate.document_hash
		 AND model_call.response_bytes=candidate.document_bytes
		 AND model_call.output_schema_id=candidate.schema_id
		 AND model_call.output_schema_version=candidate.schema_version::text
		 AND model_call.request_hash=operation.request_hash
		 AND reservation.settled_input_tokens=model_call.input_tokens
		 AND reservation.settled_output_tokens=model_call.output_tokens
		WHERE candidate.workspace_id=?::uuid AND candidate.analysis_run_id=?::uuid
		  AND candidate.id=?::uuid AND candidate.document_hash=?`,
		string(query.WorkflowRunID), string(query.WorkspaceID), string(query.AnalysisRunID),
		string(query.CandidateID), query.CandidateHash,
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
	if err := domain.ValidateWorkspaceAnalysisCandidate(candidate); err != nil ||
		candidate.ID != query.CandidateID || candidate.DocumentHash != query.CandidateHash {
		return domain.WorkspaceAnalysisCandidate{}, consistency(
			errors.New("workspace analysis candidate authority binding drifted"),
		)
	}
	return candidate, nil
}

// recoverGORMWorkspaceAnalysisModelAuthorization 在授权 commit 结果未知时于新事务
// 内按 durable binding 恢复；恢复失败不猜测成功。
func (repository *GORMWorkspaceAnalysisRepository) recoverGORMWorkspaceAnalysisModelAuthorization(
	ctx context.Context,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
	commitErr error,
) (application.WorkspaceAnalysisModelAuthorizationResult, error) {
	var result application.WorkspaceAnalysisModelAuthorizationResult
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		locked, err := gormLockWorkspaceAnalysisModelOperation(callbackCtx, transaction, scope, repository.fence, command, false)
		if err == nil {
			err = validateWorkspaceAnalysisModelDefinition(locked, command.Identity)
		}
		if err == nil {
			err = gormValidateWorkspaceAnalysisModelLockedBinding(callbackCtx, transaction, locked, command)
		}
		if err != nil {
			return err
		}
		switch locked.operation.status {
		case domain.WorkspaceAnalysisOperationStarted:
			if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
				locked.operation.latestAttemptID == nil || *locked.operation.latestAttemptID != command.Identity.NodeAttemptID {
				return errors.New("workspace analysis model authorization commit is not provable")
			}
			result = workspaceAnalysisModelAuthorizationResult(*locked.modelRun, *locked.modelCall,
				locked.operation.id, locked.reservation.ID, application.WorkspaceAnalysisModelAuthorizationReconcile)
		case domain.WorkspaceAnalysisOperationSucceeded,
			domain.WorkspaceAnalysisOperationFailed,
			domain.WorkspaceAnalysisOperationUnknown:
			terminalResult, terminalErr := gormWorkspaceAnalysisModelTerminalAuthorizationResult(callbackCtx, transaction, locked, command)
			if terminalErr != nil {
				return terminalErr
			}
			result = terminalResult
		default:
			return errors.New("workspace analysis model authorization commit has no durable closure")
		}
		return result.ValidateFor(command)
	})
	if err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}
	return result, nil
}

// recoverGORMWorkspaceAnalysisModelCallFinalization 在 Call 终结 commit 未知时恢复。
func (repository *GORMWorkspaceAnalysisRepository) recoverGORMWorkspaceAnalysisModelCallFinalization(
	ctx context.Context,
	command application.FinalizeWorkspaceAnalysisModelCallCommand,
	commitErr error,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	var result application.WorkspaceAnalysisModelMutationResult
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		locked, err := gormLockWorkspaceAnalysisModelOperation(callbackCtx, transaction, scope, repository.fence,
			authorizationCommandForTerminal(command.Identity, command.OperationKey, command.OperationID, command.ReservationID, command.Run, command.Call), false)
		if err == nil {
			err = validateWorkspaceAnalysisModelDefinition(locked, command.Identity)
		}
		if err == nil {
			err = gormValidateWorkspaceAnalysisModelTerminalCommandBinding(callbackCtx, transaction, locked,
				command.OperationID, command.ReservationID, command.Run, command.Call)
		}
		if err != nil {
			return err
		}
		replayResult, replayErr := replayWorkspaceAnalysisModelCallTerminal(locked, command)
		if replayErr != nil {
			return replayErr
		}
		result = replayResult
		return nil
	})
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}
	return result, nil
}

// recoverGORMWorkspaceAnalysisModelResultFinalization 在结果终结 commit 未知时恢复。
func (repository *GORMWorkspaceAnalysisRepository) recoverGORMWorkspaceAnalysisModelResultFinalization(
	ctx context.Context,
	command application.FinalizeWorkspaceAnalysisModelResultCommand,
	commitErr error,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	var result application.WorkspaceAnalysisModelMutationResult
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		locked, err := gormLockWorkspaceAnalysisModelOperation(callbackCtx, transaction, scope, repository.fence,
			authorizationCommandForResult(command), false)
		if err == nil {
			err = validateWorkspaceAnalysisModelDefinition(locked, command.Identity)
		}
		if err == nil {
			err = gormValidateWorkspaceAnalysisModelTerminalCommandBinding(callbackCtx, transaction, locked,
				command.OperationID, command.ReservationID, command.Run, command.Call)
		}
		if err != nil {
			return err
		}
		replayResult, replayErr := gormReplayWorkspaceAnalysisModelResultTerminal(callbackCtx, transaction, locked, command)
		if replayErr != nil {
			return replayErr
		}
		result = replayResult
		return nil
	})
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}
	return result, nil
}

// recoverGORMWorkspaceAnalysisModelCandidateFinalization 在候选终结 commit 未知时恢复。
func (repository *GORMWorkspaceAnalysisRepository) recoverGORMWorkspaceAnalysisModelCandidateFinalization(
	ctx context.Context,
	command application.FinalizeWorkspaceAnalysisModelCandidateCommand,
	commitErr error,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	var result application.WorkspaceAnalysisModelMutationResult
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		locked, err := gormLockWorkspaceAnalysisModelOperation(callbackCtx, transaction, scope, repository.fence,
			authorizationCommandForCandidate(command), false)
		if err == nil {
			err = validateWorkspaceAnalysisModelDefinition(locked, command.Identity)
		}
		if err == nil {
			err = gormValidateWorkspaceAnalysisModelTerminalCommandBinding(callbackCtx, transaction, locked,
				command.OperationID, command.ReservationID, command.Run, command.Call)
		}
		if err != nil {
			return err
		}
		replayResult, replayErr := gormReplayWorkspaceAnalysisModelCandidateTerminal(callbackCtx, transaction, locked, command)
		if replayErr != nil {
			return replayErr
		}
		result = replayResult
		return nil
	})
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}
	return result, nil
}

// FinalizeWorkspaceAnalysisModelCall 原子关闭拒绝、失败或结果未知的 Model Call/Run 与预算。
func (repository *GORMWorkspaceAnalysisRepository) FinalizeWorkspaceAnalysisModelCall(
	ctx context.Context,
	command application.FinalizeWorkspaceAnalysisModelCallCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelInvalid(errors.New("workspace analysis model context is nil"))
	}
	if err := command.Validate(); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	var result application.WorkspaceAnalysisModelMutationResult
	callbackSucceeded := false
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		locked, err := gormLockWorkspaceAnalysisModelOperation(callbackCtx, transaction, scope, repository.fence,
			authorizationCommandForTerminal(command.Identity, command.OperationKey, command.OperationID, command.ReservationID, command.Run, command.Call), false)
		if err != nil {
			return err
		}
		if err := validateWorkspaceAnalysisModelDefinition(locked, command.Identity); err != nil {
			return err
		}
		if err := gormValidateWorkspaceAnalysisModelTerminalCommandBinding(callbackCtx, transaction, locked, command.OperationID, command.ReservationID, command.Run, command.Call); err != nil {
			return err
		}
		if locked.operation.status == domain.WorkspaceAnalysisOperationFailed || locked.operation.status == domain.WorkspaceAnalysisOperationUnknown {
			replayResult, replayErr := replayWorkspaceAnalysisModelCallTerminal(locked, command)
			if replayErr != nil {
				return replayErr
			}
			result = replayResult
			callbackSucceeded = true
			return nil
		}
		if locked.operation.status != domain.WorkspaceAnalysisOperationStarted || locked.reservation == nil ||
			locked.reservation.Status != domain.WorkspaceAnalysisBudgetReserved || locked.modelRun == nil || locked.modelCall == nil ||
			locked.modelRun.Status != domain.ModelRunRunning || locked.modelCall.Status != domain.ModelCallStarted {
			return workspaceAnalysisModelConflict(errors.New("workspace analysis model terminal is not startable"))
		}
		if err := validateWorkspaceAnalysisModelFailureTerminalFence(locked, command.Identity, command.Call.Status); err != nil {
			return err
		}
		mutation, err := gormFinalizeWorkspaceAnalysisModelCallTx(callbackCtx, transaction, locked, command)
		if err != nil {
			return err
		}
		if err := gormForceWorkspaceAnalysisModelConstraints(callbackCtx, transaction); err != nil {
			return err
		}
		result = mutation
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		if callbackSucceeded {
			return repository.recoverGORMWorkspaceAnalysisModelCallFinalization(ctx, command, err)
		}
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	return result, nil
}

// FinalizeWorkspaceAnalysisModelResult 原子保存 Planner/Review canonical 结果并关闭全部模型预算事实。
func (repository *GORMWorkspaceAnalysisRepository) FinalizeWorkspaceAnalysisModelResult(
	ctx context.Context,
	command application.FinalizeWorkspaceAnalysisModelResultCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelInvalid(errors.New("workspace analysis model context is nil"))
	}
	if err := command.Validate(); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	var result application.WorkspaceAnalysisModelMutationResult
	callbackSucceeded := false
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		locked, err := gormLockWorkspaceAnalysisModelOperation(callbackCtx, transaction, scope, repository.fence,
			authorizationCommandForResult(command), false)
		if err != nil {
			return err
		}
		if err := validateWorkspaceAnalysisModelDefinition(locked, command.Identity); err != nil {
			return err
		}
		if err := gormValidateWorkspaceAnalysisModelTerminalCommandBinding(callbackCtx, transaction, locked, command.OperationID, command.ReservationID, command.Run, command.Call); err != nil {
			return err
		}
		if locked.operation.status == domain.WorkspaceAnalysisOperationSucceeded {
			replayResult, replayErr := gormReplayWorkspaceAnalysisModelResultTerminal(callbackCtx, transaction, locked, command)
			if replayErr != nil {
				return replayErr
			}
			result = replayResult
			callbackSucceeded = true
			return nil
		}
		if locked.operation.status != domain.WorkspaceAnalysisOperationStarted || locked.reservation == nil ||
			locked.reservation.Status != domain.WorkspaceAnalysisBudgetReserved || locked.modelRun == nil || locked.modelCall == nil ||
			locked.modelRun.Status != domain.ModelRunRunning || locked.modelCall.Status != domain.ModelCallStarted {
			return workspaceAnalysisModelConflict(errors.New("workspace analysis model result is not startable"))
		}
		if err := validateWorkspaceAnalysisModelFence(locked, command.Identity); err != nil {
			return workspaceAnalysisModelSuccessfulFinalizationFenceError(locked, command.Identity, err)
		}
		mutation, err := gormFinalizeWorkspaceAnalysisModelResultTx(callbackCtx, transaction, locked, command)
		if err != nil {
			return err
		}
		if err := gormForceWorkspaceAnalysisModelConstraints(callbackCtx, transaction); err != nil {
			return err
		}
		result = mutation
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		if callbackSucceeded {
			return repository.recoverGORMWorkspaceAnalysisModelResultFinalization(ctx, command, err)
		}
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	return result, nil
}

// FinalizeWorkspaceAnalysisModelCandidate 原子保存 Synthesis canonical Candidate 并关闭全部模型预算事实。
func (repository *GORMWorkspaceAnalysisRepository) FinalizeWorkspaceAnalysisModelCandidate(
	ctx context.Context,
	command application.FinalizeWorkspaceAnalysisModelCandidateCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelInvalid(errors.New("workspace analysis model context is nil"))
	}
	if err := command.Validate(); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	var result application.WorkspaceAnalysisModelMutationResult
	callbackSucceeded := false
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		locked, err := gormLockWorkspaceAnalysisModelOperation(callbackCtx, transaction, scope, repository.fence,
			authorizationCommandForCandidate(command), false)
		if err != nil {
			return err
		}
		if err := validateWorkspaceAnalysisModelDefinition(locked, command.Identity); err != nil {
			return err
		}
		if err := gormValidateWorkspaceAnalysisModelTerminalCommandBinding(callbackCtx, transaction, locked, command.OperationID, command.ReservationID, command.Run, command.Call); err != nil {
			return err
		}
		if locked.operation.status == domain.WorkspaceAnalysisOperationSucceeded {
			replayResult, replayErr := gormReplayWorkspaceAnalysisModelCandidateTerminal(callbackCtx, transaction, locked, command)
			if replayErr != nil {
				return replayErr
			}
			result = replayResult
			callbackSucceeded = true
			return nil
		}
		if locked.operation.status != domain.WorkspaceAnalysisOperationStarted || locked.reservation == nil ||
			locked.reservation.Status != domain.WorkspaceAnalysisBudgetReserved || locked.modelRun == nil || locked.modelCall == nil ||
			locked.modelRun.Status != domain.ModelRunRunning || locked.modelCall.Status != domain.ModelCallStarted {
			return workspaceAnalysisModelConflict(errors.New("workspace analysis model candidate is not startable"))
		}
		if err := validateWorkspaceAnalysisModelFence(locked, command.Identity); err != nil {
			return workspaceAnalysisModelSuccessfulFinalizationFenceError(locked, command.Identity, err)
		}
		mutation, err := gormFinalizeWorkspaceAnalysisModelCandidateTx(callbackCtx, transaction, locked, command)
		if err != nil {
			return err
		}
		if err := gormForceWorkspaceAnalysisModelConstraints(callbackCtx, transaction); err != nil {
			return err
		}
		result = mutation
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		if callbackSucceeded {
			return repository.recoverGORMWorkspaceAnalysisModelCandidateFinalization(ctx, command, err)
		}
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	return result, nil
}
