package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
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

var _ application.WorkspaceAnalysisModelOperationRepository = (*Repository)(nil)
var _ application.WorkspaceAnalysisRetrievalPlanCheckpointReader = (*Repository)(nil)

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

// AuthorizeWorkspaceAnalysisModelCall 原子创建或归约 Workspace Analysis 的唯一模型调用槽位。
func (repository *Repository) AuthorizeWorkspaceAnalysisModelCall(
	ctx context.Context,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
) (application.WorkspaceAnalysisModelAuthorizationResult, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelInvalid(errors.New("workspace analysis model context is nil"))
	}
	if err := command.Validate(); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	tx, err := beginWorkspaceAnalysisModelTx(ctx, repository)
	if err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := lockWorkspaceAnalysisModelOperation(ctx, tx, command, true)
	if err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	if err := validateWorkspaceAnalysisModelDefinition(locked, command.Identity); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	if err := validateWorkspaceAnalysisModelFence(locked, command.Identity); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	if err := validateWorkspaceAnalysisModelLockedBinding(ctx, tx, locked, command); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}

	switch locked.operation.status {
	case domain.WorkspaceAnalysisOperationPending:
		result, authorizeErr := authorizePendingWorkspaceAnalysisModelCall(ctx, tx, locked, command)
		if authorizeErr != nil {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, authorizeErr
		}
		if err := forceWorkspaceAnalysisModelConstraints(ctx, tx); err != nil {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, err
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return repository.recoverWorkspaceAnalysisModelAuthorization(ctx, command, commitErr)
		}
		return result, nil
	case domain.WorkspaceAnalysisOperationStarted:
		if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, consistency(errors.New("started workspace analysis model operation is incomplete"))
		}
		if locked.operation.latestAttemptID != nil && *locked.operation.latestAttemptID == command.Identity.NodeAttemptID {
			result := workspaceAnalysisModelAuthorizationResult(
				*locked.modelRun, *locked.modelCall, locked.operation.id, locked.reservation.ID,
				application.WorkspaceAnalysisModelAuthorizationReconcile,
			)
			if err := result.ValidateFor(command); err != nil {
				return application.WorkspaceAnalysisModelAuthorizationResult{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return application.WorkspaceAnalysisModelAuthorizationResult{}, classify(err)
			}
			return result, nil
		}
		result, reduceErr := reducePriorWorkspaceAnalysisModelCallToUnknown(ctx, tx, locked, command)
		if reduceErr != nil {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, reduceErr
		}
		if err := forceWorkspaceAnalysisModelConstraints(ctx, tx); err != nil {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, err
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return repository.recoverWorkspaceAnalysisModelAuthorization(ctx, command, commitErr)
		}
		return result, nil
	case domain.WorkspaceAnalysisOperationSucceeded,
		domain.WorkspaceAnalysisOperationFailed,
		domain.WorkspaceAnalysisOperationUnknown:
		result, terminalErr := workspaceAnalysisModelTerminalAuthorizationResult(ctx, tx, locked, command)
		if terminalErr != nil {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, terminalErr
		}
		if err := advanceWorkspaceAnalysisModelTerminalAttempt(ctx, tx, locked, command.Identity.NodeAttemptID); err != nil {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, err
		}
		if err := forceWorkspaceAnalysisModelConstraints(ctx, tx); err != nil {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return repository.recoverWorkspaceAnalysisModelAuthorization(ctx, command, err)
		}
		return result, nil
	default:
		return application.WorkspaceAnalysisModelAuthorizationResult{}, consistency(errors.New("workspace analysis model operation status is unsupported"))
	}
}

// FinalizeWorkspaceAnalysisModelCall 原子关闭拒绝、失败或结果未知的 Model Call/Run 与预算。
func (repository *Repository) FinalizeWorkspaceAnalysisModelCall(
	ctx context.Context,
	command application.FinalizeWorkspaceAnalysisModelCallCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelInvalid(errors.New("workspace analysis model context is nil"))
	}
	if err := command.Validate(); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	tx, err := beginWorkspaceAnalysisModelTx(ctx, repository)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := lockWorkspaceAnalysisModelOperation(ctx, tx, authorizationCommandForTerminal(command.Identity, command.OperationKey, command.OperationID, command.ReservationID, command.Run, command.Call), false)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := validateWorkspaceAnalysisModelDefinition(locked, command.Identity); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := validateWorkspaceAnalysisModelTerminalCommandBinding(ctx, tx, locked, command.OperationID, command.ReservationID, command.Run, command.Call); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if locked.operation.status == domain.WorkspaceAnalysisOperationFailed || locked.operation.status == domain.WorkspaceAnalysisOperationUnknown {
		result, replayErr := replayWorkspaceAnalysisModelCallTerminal(locked, command)
		if replayErr != nil {
			return application.WorkspaceAnalysisModelMutationResult{}, replayErr
		}
		if err := tx.Commit(ctx); err != nil {
			return application.WorkspaceAnalysisModelMutationResult{}, classify(err)
		}
		return result, nil
	}
	if locked.operation.status != domain.WorkspaceAnalysisOperationStarted || locked.reservation == nil ||
		locked.reservation.Status != domain.WorkspaceAnalysisBudgetReserved || locked.modelRun == nil || locked.modelCall == nil ||
		locked.modelRun.Status != domain.ModelRunRunning || locked.modelCall.Status != domain.ModelCallStarted {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model terminal is not startable"))
	}
	if err := validateWorkspaceAnalysisModelFailureTerminalFence(locked, command.Identity, command.Call.Status); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	mutation, err := finalizeWorkspaceAnalysisModelCallTx(ctx, tx, locked, command)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := forceWorkspaceAnalysisModelConstraints(ctx, tx); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return repository.recoverWorkspaceAnalysisModelCallFinalization(ctx, command, commitErr)
	}
	return mutation, nil
}

// FinalizeWorkspaceAnalysisModelResult 原子保存 Planner/Review canonical 结果并关闭全部模型预算事实。
func (repository *Repository) FinalizeWorkspaceAnalysisModelResult(
	ctx context.Context,
	command application.FinalizeWorkspaceAnalysisModelResultCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelInvalid(errors.New("workspace analysis model context is nil"))
	}
	if err := command.Validate(); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	tx, err := beginWorkspaceAnalysisModelTx(ctx, repository)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := lockWorkspaceAnalysisModelOperation(ctx, tx, authorizationCommandForResult(command), false)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := validateWorkspaceAnalysisModelDefinition(locked, command.Identity); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := validateWorkspaceAnalysisModelTerminalCommandBinding(ctx, tx, locked, command.OperationID, command.ReservationID, command.Run, command.Call); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if locked.operation.status == domain.WorkspaceAnalysisOperationSucceeded {
		result, replayErr := replayWorkspaceAnalysisModelResultTerminal(ctx, tx, locked, command)
		if replayErr != nil {
			return application.WorkspaceAnalysisModelMutationResult{}, replayErr
		}
		if err := tx.Commit(ctx); err != nil {
			return application.WorkspaceAnalysisModelMutationResult{}, classify(err)
		}
		return result, nil
	}
	if locked.operation.status != domain.WorkspaceAnalysisOperationStarted || locked.reservation == nil ||
		locked.reservation.Status != domain.WorkspaceAnalysisBudgetReserved || locked.modelRun == nil || locked.modelCall == nil ||
		locked.modelRun.Status != domain.ModelRunRunning || locked.modelCall.Status != domain.ModelCallStarted {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model result is not startable"))
	}
	if err := validateWorkspaceAnalysisModelFence(locked, command.Identity); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelSuccessfulFinalizationFenceError(
			locked, command.Identity, err,
		)
	}
	mutation, err := finalizeWorkspaceAnalysisModelResultTx(ctx, tx, locked, command)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := forceWorkspaceAnalysisModelConstraints(ctx, tx); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return repository.recoverWorkspaceAnalysisModelResultFinalization(ctx, command, commitErr)
	}
	return mutation, nil
}

// FinalizeWorkspaceAnalysisModelCandidate 原子保存 Synthesis canonical Candidate 并关闭全部模型预算事实。
func (repository *Repository) FinalizeWorkspaceAnalysisModelCandidate(
	ctx context.Context,
	command application.FinalizeWorkspaceAnalysisModelCandidateCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelInvalid(errors.New("workspace analysis model context is nil"))
	}
	if err := command.Validate(); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	tx, err := beginWorkspaceAnalysisModelTx(ctx, repository)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := lockWorkspaceAnalysisModelOperation(ctx, tx, authorizationCommandForCandidate(command), false)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := validateWorkspaceAnalysisModelDefinition(locked, command.Identity); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := validateWorkspaceAnalysisModelTerminalCommandBinding(ctx, tx, locked, command.OperationID, command.ReservationID, command.Run, command.Call); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if locked.operation.status == domain.WorkspaceAnalysisOperationSucceeded {
		result, replayErr := replayWorkspaceAnalysisModelCandidateTerminal(ctx, tx, locked, command)
		if replayErr != nil {
			return application.WorkspaceAnalysisModelMutationResult{}, replayErr
		}
		if err := tx.Commit(ctx); err != nil {
			return application.WorkspaceAnalysisModelMutationResult{}, classify(err)
		}
		return result, nil
	}
	if locked.operation.status != domain.WorkspaceAnalysisOperationStarted || locked.reservation == nil ||
		locked.reservation.Status != domain.WorkspaceAnalysisBudgetReserved || locked.modelRun == nil || locked.modelCall == nil ||
		locked.modelRun.Status != domain.ModelRunRunning || locked.modelCall.Status != domain.ModelCallStarted {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model candidate is not startable"))
	}
	if err := validateWorkspaceAnalysisModelFence(locked, command.Identity); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelSuccessfulFinalizationFenceError(
			locked, command.Identity, err,
		)
	}
	mutation, err := finalizeWorkspaceAnalysisModelCandidateTx(ctx, tx, locked, command)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := forceWorkspaceAnalysisModelConstraints(ctx, tx); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return repository.recoverWorkspaceAnalysisModelCandidateFinalization(ctx, command, commitErr)
	}
	return mutation, nil
}

// LoadWorkspaceAnalysisModelResult 只读取 exact Workspace/Run/Operation/Model Run/Call 闭环中的不可变结果。
func (repository *Repository) LoadWorkspaceAnalysisModelResult(
	ctx context.Context,
	query application.WorkspaceAnalysisModelResultQuery,
) (domain.WorkspaceAnalysisModelResult, error) {
	if ctx == nil {
		return domain.WorkspaceAnalysisModelResult{}, workspaceAnalysisModelInvalid(errors.New("workspace analysis model context is nil"))
	}
	if err := query.Validate(); err != nil {
		return domain.WorkspaceAnalysisModelResult{}, err
	}
	if repository == nil || repository.db == nil {
		return domain.WorkspaceAnalysisModelResult{}, workspaceAnalysisModelUnavailable(errors.New("workspace analysis model repository is unavailable"))
	}
	result, err := scanWorkspaceAnalysisModelResult(repository.db.QueryRow(ctx, `SELECT `+workspaceAnalysisModelResultColumns+`
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
		WHERE result.workspace_id=$1 AND result.analysis_run_id=$2 AND result.operation_id=$3
		  AND result.model_run_id=$4 AND result.model_call_id=$5`,
		string(query.WorkspaceID), string(query.AnalysisRunID), string(query.OperationID),
		string(query.ModelRunID), string(query.ModelCallID),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkspaceAnalysisModelResult{}, notFound()
	}
	if err != nil {
		return domain.WorkspaceAnalysisModelResult{}, classify(err)
	}
	if err := domain.ValidateWorkspaceAnalysisModelResult(result); err != nil {
		return domain.WorkspaceAnalysisModelResult{}, consistency(err)
	}
	return result, nil
}

// FindWorkspaceAnalysisRetrievalPlanCheckpoint 在读取当前 Active Index 前恢复首次计划调用冻结的版本身份。
func (repository *Repository) FindWorkspaceAnalysisRetrievalPlanCheckpoint(
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
	if repository == nil || repository.db == nil {
		return application.WorkspaceAnalysisRetrievalPlanCheckpoint{}, false,
			workspaceAnalysisModelUnavailable(errors.New("workspace analysis model repository is unavailable"))
	}

	var operationID, status, requestHash string
	var modelCallID, modelRunID, indexVersionID, embeddingVersionID, rerankModelVersion *string
	var modelResultID, resultHash *string
	err := repository.db.QueryRow(ctx, `SELECT
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
		WHERE operation.workspace_id=$1 AND operation.workflow_run_id=$2
		  AND operation.analysis_run_id=$3 AND operation.node_run_id=$4
		  AND operation.node_key='retrieve_evidence'
		  AND operation.operation_kind='RETRIEVAL_PLAN'
		  AND operation.ordinal=1 AND operation.call_kind='MODEL'`,
		string(query.WorkspaceID), string(query.WorkflowRunID), string(query.AnalysisRunID), string(query.NodeRunID),
	).Scan(
		&operationID, &status, &requestHash, &modelCallID, &modelRunID,
		&indexVersionID, &embeddingVersionID, &rerankModelVersion, &modelResultID, &resultHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.WorkspaceAnalysisRetrievalPlanCheckpoint{}, false, nil
	}
	if err != nil {
		return application.WorkspaceAnalysisRetrievalPlanCheckpoint{}, false, classify(err)
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
func (repository *Repository) LoadWorkspaceAnalysisCandidate(
	ctx context.Context,
	query application.WorkspaceAnalysisCandidateQuery,
) (domain.WorkspaceAnalysisCandidate, error) {
	if ctx == nil {
		return domain.WorkspaceAnalysisCandidate{}, workspaceAnalysisModelInvalid(errors.New("workspace analysis model context is nil"))
	}
	if err := query.Validate(); err != nil {
		return domain.WorkspaceAnalysisCandidate{}, err
	}
	if repository == nil || repository.db == nil {
		return domain.WorkspaceAnalysisCandidate{}, workspaceAnalysisModelUnavailable(errors.New("workspace analysis model repository is unavailable"))
	}
	candidate, err := scanWorkspaceAnalysisCandidate(repository.db.QueryRow(ctx, `SELECT `+workspaceAnalysisCandidateColumns+`
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
		 AND operation.call_kind='MODEL' AND operation.model_call_id=$5
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
		WHERE candidate.workspace_id=$1 AND candidate.analysis_run_id=$2
		  AND candidate.synthesis_operation_id=$3 AND candidate.synthesis_model_run_id=$4`,
		string(query.WorkspaceID), string(query.AnalysisRunID), string(query.OperationID),
		string(query.ModelRunID), string(query.ModelCallID),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkspaceAnalysisCandidate{}, notFound()
	}
	if err != nil {
		return domain.WorkspaceAnalysisCandidate{}, classify(err)
	}
	if err := domain.ValidateWorkspaceAnalysisCandidate(candidate); err != nil {
		return domain.WorkspaceAnalysisCandidate{}, consistency(err)
	}
	return candidate, nil
}

func beginWorkspaceAnalysisModelTx(ctx context.Context, repository *Repository) (pgx.Tx, error) {
	if repository == nil || repository.db == nil {
		return nil, workspaceAnalysisModelUnavailable(errors.New("workspace analysis model repository is unavailable"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return nil, classify(err)
	}
	return tx, nil
}

func lockWorkspaceAnalysisModelOperation(
	ctx context.Context,
	tx pgx.Tx,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
	createOperation bool,
) (workspaceAnalysisModelLocks, error) {
	var locked workspaceAnalysisModelLocks
	var definitionID string
	if err := tx.QueryRow(ctx, `SELECT run.definition_id::text,definition.key,definition.version,definition.graph,
		run.status,run.cancel_requested_at
		FROM workflow.run AS run
		JOIN workflow.definition AS definition
		  ON definition.id=run.definition_id AND definition.workspace_id=run.workspace_id
		WHERE run.id=$1 AND run.workspace_id=$2
		FOR UPDATE OF run`, string(command.Identity.WorkflowRunID), string(command.Identity.WorkspaceID)).Scan(
		&definitionID, &locked.fence.definitionKey, &locked.fence.definitionVersion, &locked.fence.definitionGraph,
		&locked.fence.workflowStatus, &locked.fence.cancelRequestedAt,
	); err != nil {
		return locked, classifyWorkspaceAnalysisModelLock(err)
	}
	locked.fence.definitionID = foundation.ID(definitionID)
	if err := tx.QueryRow(ctx, `SELECT node_key,status,attempt,lease_owner,lease_until
		FROM workflow.node_run WHERE id=$1 AND run_id=$2 FOR UPDATE`,
		string(command.Identity.NodeRunID), string(command.Identity.WorkflowRunID),
	).Scan(
		&locked.fence.nodeKey, &locked.fence.nodeStatus, &locked.fence.nodeAttempt,
		&locked.fence.nodeOwner, &locked.fence.nodeLease,
	); err != nil {
		return locked, classifyWorkspaceAnalysisModelLock(err)
	}
	if err := tx.QueryRow(ctx, `SELECT status,attempt_no,lease_owner,lease_until
		FROM workflow.node_attempt WHERE id=$1 AND node_run_id=$2 FOR UPDATE`,
		string(command.Identity.NodeAttemptID), string(command.Identity.NodeRunID),
	).Scan(
		&locked.fence.attemptStatus, &locked.fence.attemptNo,
		&locked.fence.attemptOwner, &locked.fence.attemptLease,
	); err != nil {
		return locked, classifyWorkspaceAnalysisModelLock(err)
	}
	analysisRun, err := scanWorkspaceAnalysisRun(tx.QueryRow(ctx, `SELECT `+workspaceAnalysisRunColumns+`
		FROM agent.workspace_analysis_run
		WHERE id=$1 AND workspace_id=$2 AND workflow_run_id=$3 FOR UPDATE`,
		string(command.OperationKey.AnalysisRunID), string(command.Identity.WorkspaceID), string(command.Identity.WorkflowRunID),
	))
	if err != nil {
		return locked, classifyWorkspaceAnalysisModelLock(err)
	}
	locked.analysisRun = analysisRun

	if createOperation {
		if _, err := tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_operation(
			id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
			ordinal,call_kind,request_hash,status,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'MODEL',$9,'PENDING',1,clock_timestamp(),clock_timestamp())
		ON CONFLICT (analysis_run_id,node_key,operation_kind,ordinal) DO NOTHING`,
			string(command.OperationID), string(command.Identity.WorkspaceID), string(command.OperationKey.AnalysisRunID),
			string(command.Identity.WorkflowRunID), string(command.Identity.NodeRunID), string(command.OperationKey.NodeKey),
			string(command.OperationKey.Kind), command.OperationKey.Ordinal, command.Call.RequestHash,
		); err != nil {
			return locked, classify(err)
		}
	}
	operation, err := scanWorkspaceAnalysisModelOperation(tx.QueryRow(ctx, `SELECT
		id::text,workspace_id::text,analysis_run_id::text,workflow_run_id::text,node_run_id::text,
		node_key,operation_kind,ordinal,call_kind,request_hash,status,
		first_node_attempt_id::text,latest_node_attempt_id::text,model_call_id::text,
		result_kind,result_id::text,result_hash,error_code,version,created_at,updated_at,started_at,completed_at
		FROM agent.workspace_analysis_operation
		WHERE analysis_run_id=$1 AND node_key=$2 AND operation_kind=$3 AND ordinal=$4
		FOR UPDATE`, string(command.OperationKey.AnalysisRunID), string(command.OperationKey.NodeKey),
		string(command.OperationKey.Kind), command.OperationKey.Ordinal,
	))
	if err != nil {
		return locked, classifyWorkspaceAnalysisModelLock(err)
	}
	locked.operation = operation

	reservation, foundReservation, err := loadWorkspaceAnalysisModelReservationForUpdate(ctx, tx, operation.id)
	if err != nil {
		return locked, err
	}
	if foundReservation {
		locked.reservation = &reservation
	}
	if operation.modelCallID != nil {
		var modelRunID string
		if err := tx.QueryRow(ctx, `SELECT model_run_id::text FROM agent.model_call WHERE id=$1`, string(*operation.modelCallID)).Scan(&modelRunID); err != nil {
			return locked, classifyWorkspaceAnalysisModelLock(err)
		}
		run, err := scanModelRun(tx.QueryRow(ctx, modelRunSelect+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`,
			string(command.Identity.WorkspaceID), modelRunID,
		))
		if err != nil {
			return locked, classifyWorkspaceAnalysisModelLock(err)
		}
		locked.modelRun = &run
		call, err := scanModelCall(tx.QueryRow(ctx, modelCallSelect+` WHERE call.id=$1 AND call.model_run_id=$2 FOR UPDATE`,
			string(*operation.modelCallID), modelRunID,
		))
		if err != nil {
			return locked, classifyWorkspaceAnalysisModelLock(err)
		}
		locked.modelCall = &call
	}
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&locked.fence.databaseNow); err != nil {
		return locked, classify(err)
	}
	locked.fence.databaseNow = locked.fence.databaseNow.UTC()
	return locked, nil
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

func loadWorkspaceAnalysisModelReservationForUpdate(
	ctx context.Context,
	tx pgx.Tx,
	operationID foundation.ID,
) (domain.WorkspaceAnalysisBudgetReservation, bool, error) {
	var reservation domain.WorkspaceAnalysisBudgetReservation
	var id, workspaceID, analysisRunID, persistedOperationID, callKind, status string
	var modelCallID, toolCallID *string
	err := tx.QueryRow(ctx, `SELECT
		id::text,workspace_id::text,analysis_run_id::text,operation_id::text,call_kind,
		model_call_id::text,tool_call_id::text,status,
		reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,reserved_cost_microunits,
		settled_model_calls,settled_tool_calls,settled_source_reads,settled_input_tokens,settled_output_tokens,settled_cost_microunits,
		created_at,settled_at
		FROM agent.workspace_analysis_budget_reservation WHERE operation_id=$1 FOR UPDATE`, string(operationID)).Scan(
		&id, &workspaceID, &analysisRunID, &persistedOperationID, &callKind,
		&modelCallID, &toolCallID, &status,
		&reservation.Reserved.ModelCalls, &reservation.Reserved.ToolCalls, &reservation.Reserved.SourceReads,
		&reservation.Reserved.InputTokens, &reservation.Reserved.OutputTokens, &reservation.Reserved.CostMicrounits,
		&reservation.Settled.ModelCalls, &reservation.Settled.ToolCalls, &reservation.Settled.SourceReads,
		&reservation.Settled.InputTokens, &reservation.Settled.OutputTokens, &reservation.Settled.CostMicrounits,
		&reservation.CreatedAt, &reservation.SettledAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkspaceAnalysisBudgetReservation{}, false, nil
	}
	if err != nil {
		return domain.WorkspaceAnalysisBudgetReservation{}, false, classify(err)
	}
	reservation.ID, reservation.WorkspaceID, reservation.AnalysisRunID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(analysisRunID)
	reservation.OperationID = foundation.ID(persistedOperationID)
	reservation.CallKind = domain.WorkspaceAnalysisOperationCallKind(callKind)
	reservation.ModelCallID = workspaceAnalysisModelOptionalID(modelCallID)
	reservation.ToolCallID = workspaceAnalysisModelOptionalID(toolCallID)
	reservation.Status = domain.WorkspaceAnalysisBudgetReservationStatus(status)
	return reservation, true, nil
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

func validateWorkspaceAnalysisModelLockedBinding(
	ctx context.Context,
	tx pgx.Tx,
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
	if err := verifyWorkspaceAnalysisModelBudgetTotals(ctx, tx, locked.analysisRun); err != nil {
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
	if err := validateWorkspaceAnalysisModelStoredClosure(ctx, tx, locked); err != nil {
		return err
	}
	return nil
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

func validateWorkspaceAnalysisModelStoredClosure(ctx context.Context, tx pgx.Tx, locked workspaceAnalysisModelLocks) error {
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
		if err := verifyWorkspaceAnalysisModelOperationResult(ctx, tx, locked); err != nil {
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

func verifyWorkspaceAnalysisModelOperationResult(ctx context.Context, tx pgx.Tx, locked workspaceAnalysisModelLocks) error {
	operation := locked.operation
	if operation.resultKind == nil || operation.resultID == nil || operation.resultHash == nil ||
		locked.reservation == nil || locked.modelRun == nil || locked.modelCall == nil {
		return consistency(errors.New("workspace analysis model operation result is incomplete"))
	}
	persistedOperation := operation.domain(&locked.reservation.ID)
	switch *operation.resultKind {
	case domain.WorkspaceAnalysisOperationResultModelCall:
		result, err := loadWorkspaceAnalysisModelResultByIdentity(ctx, tx, operation.workspaceID, operation.analysisRunID,
			operation.id, locked.modelRun.ID, locked.modelCall.ID, *operation.resultID)
		if err != nil {
			return err
		}
		if result.DocumentHash != *operation.resultHash {
			return consistency(errors.New("workspace analysis model result hash drifted"))
		}
		var candidate *domain.WorkspaceAnalysisCandidate
		if result.SubjectCandidateID != nil {
			persistedCandidate, loadErr := loadWorkspaceAnalysisCandidateSubject(ctx, tx, operation.workspaceID,
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
		candidate, err := loadWorkspaceAnalysisCandidateByIdentity(ctx, tx, operation.workspaceID, operation.analysisRunID,
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

func verifyWorkspaceAnalysisModelBudgetTotals(ctx context.Context, tx pgx.Tx, run domain.WorkspaceAnalysisRun) error {
	var reservedModelCalls, reservedToolCalls, reservedSourceReads int64
	var reservedInputTokens, reservedOutputTokens int64
	var settledModelCalls, settledToolCalls, settledSourceReads int64
	var settledInputTokens, settledOutputTokens int64
	if err := tx.QueryRow(ctx, `SELECT
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
		FROM agent.workspace_analysis_budget_reservation WHERE analysis_run_id=$1`, string(run.ID)).Scan(
		&reservedModelCalls, &reservedToolCalls, &reservedSourceReads, &reservedInputTokens, &reservedOutputTokens,
		&settledModelCalls, &settledToolCalls, &settledSourceReads, &settledInputTokens, &settledOutputTokens,
	); err != nil {
		return classify(err)
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

func authorizePendingWorkspaceAnalysisModelCall(
	ctx context.Context,
	tx pgx.Tx,
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
	tag, err := tx.Exec(ctx, insertModelRunSQL, modelRunArgs(run)...)
	if err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model run already exists"))
	}
	tag, err = tx.Exec(ctx, `INSERT INTO agent.model_call(
		id,model_run_id,call_no,phase,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
		request_hash,response_hash,request_bytes,response_bytes,input_tokens,output_tokens,latency_ms,
		status,error_code,version,started_at,completed_at
	) VALUES(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
		$16,NULL,$17,0,0,0,0,'STARTED',NULL,1,$18,NULL
	)`, string(call.ID), string(call.ModelRunID), call.CallNo, string(call.Phase),
		call.Model.AdapterName, call.Model.AdapterVersion, call.Model.ModelID, call.Model.ModelVersion,
		call.Profile.ID, call.Profile.Version, call.Prompt.ID, call.Prompt.Version,
		call.Schema.ID, call.Schema.Version, call.MaxOutputTokens,
		call.RequestHash, call.RequestBytes, at,
	)
	if err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model call was not inserted"))
	}
	tag, err = tx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
		status='running',reserved_model_calls=reserved_model_calls+1,
		reserved_input_tokens=reserved_input_tokens+$1,reserved_output_tokens=reserved_output_tokens+$2,
		version=version+1,updated_at=GREATEST(updated_at,$3)
		WHERE id=$4 AND workspace_id=$5 AND workflow_run_id=$6 AND version=$7
		  AND status IN ('queued','running')
		  AND reserved_model_calls+settled_model_calls+1<=max_model_calls
		  AND reserved_input_tokens+settled_input_tokens+$1<=max_input_tokens
		  AND reserved_output_tokens+settled_output_tokens+$2<=max_output_tokens`,
		domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall, call.MaxOutputTokens, at,
		string(locked.analysisRun.ID), string(locked.analysisRun.WorkspaceID), string(locked.analysisRun.WorkflowRunID),
		locked.analysisRun.Version,
	)
	if err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model run budget CAS failed"))
	}
	tag, err = tx.Exec(ctx, `UPDATE agent.workspace_analysis_operation SET
		status='STARTED',first_node_attempt_id=$1,latest_node_attempt_id=$1,model_call_id=$2,
		version=version+1,updated_at=GREATEST(updated_at,$3),started_at=$3
		WHERE id=$4 AND workspace_id=$5 AND analysis_run_id=$6 AND workflow_run_id=$7 AND node_run_id=$8
		  AND call_kind='MODEL' AND request_hash=$9 AND status='PENDING' AND version=$10`,
		string(command.Identity.NodeAttemptID), string(call.ID), at,
		string(locked.operation.id), string(locked.operation.workspaceID), string(locked.operation.analysisRunID),
		string(locked.operation.workflowRunID), string(locked.operation.nodeRunID), locked.operation.requestHash, locked.operation.version,
	)
	if err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model operation authorization CAS failed"))
	}
	tag, err = tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_budget_reservation(
		id,workspace_id,analysis_run_id,operation_id,call_kind,model_call_id,status,
		reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,
		reserved_cost_microunits,created_at
	) VALUES($1,$2,$3,$4,'MODEL',$5,'RESERVED',1,0,0,$6,$7,NULL,$8)`,
		string(command.ReservationID), string(command.Identity.WorkspaceID), string(command.OperationKey.AnalysisRunID),
		string(locked.operation.id), string(call.ID), domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall,
		call.MaxOutputTokens, at,
	)
	if err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model reservation was not inserted"))
	}
	result := workspaceAnalysisModelAuthorizationResult(run, call, locked.operation.id, command.ReservationID,
		application.WorkspaceAnalysisModelAuthorizationCreated)
	if err := result.ValidateFor(command); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	return result, nil
}

func validateWorkspaceAnalysisModelAdmission(
	locked workspaceAnalysisModelLocks,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
) error {
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

func reducePriorWorkspaceAnalysisModelCallToUnknown(
	ctx context.Context,
	tx pgx.Tx,
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
	if err := updateWorkspaceAnalysisModelCall(ctx, tx, locked.modelCall.Version, call); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	if err := updateWorkspaceAnalysisModelRun(ctx, tx, locked.modelRun.Version, run); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	if err := settleWorkspaceAnalysisModelReservation(ctx, tx, *locked.reservation, call, at, true); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	if err := settleWorkspaceAnalysisModelRunBudget(ctx, tx, locked.analysisRun, *locked.reservation, call, at, true); err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, err
	}
	if err := completeWorkspaceAnalysisModelOperation(ctx, tx, locked.operation, command.Identity.NodeAttemptID,
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

func workspaceAnalysisModelTerminalAuthorizationResult(
	ctx context.Context,
	tx pgx.Tx,
	locked workspaceAnalysisModelLocks,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
) (application.WorkspaceAnalysisModelAuthorizationResult, error) {
	if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
		!sameWorkspaceAnalysisModelAuthorizationRequest(command.Run, command.Call, *locked.modelRun, *locked.modelCall) {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model replay request drifted"))
	}
	if err := validateWorkspaceAnalysisModelStoredClosure(ctx, tx, locked); err != nil {
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

func advanceWorkspaceAnalysisModelTerminalAttempt(
	ctx context.Context,
	tx pgx.Tx,
	locked workspaceAnalysisModelLocks,
	attemptID foundation.ID,
) error {
	if locked.operation.latestAttemptID != nil && *locked.operation.latestAttemptID == attemptID {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_operation SET
		latest_node_attempt_id=$1,version=version+1,updated_at=GREATEST(updated_at,$2)
		WHERE id=$3 AND workspace_id=$4 AND analysis_run_id=$5 AND call_kind='MODEL'
		  AND status IN ('SUCCEEDED','FAILED','UNKNOWN') AND version=$6`,
		string(attemptID), locked.fence.databaseNow, string(locked.operation.id), string(locked.operation.workspaceID),
		string(locked.operation.analysisRunID), locked.operation.version,
	)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model terminal replay attempt CAS failed"))
	}
	return nil
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

func validateWorkspaceAnalysisModelTerminalCommandBinding(
	ctx context.Context,
	tx pgx.Tx,
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
	if err := validateWorkspaceAnalysisModelLockedBinding(ctx, tx, locked, command); err != nil {
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

func replayWorkspaceAnalysisModelResultTerminal(
	ctx context.Context,
	tx pgx.Tx,
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
	result, err := loadWorkspaceAnalysisModelResultByIdentity(ctx, tx, command.Result.WorkspaceID,
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

func replayWorkspaceAnalysisModelCandidateTerminal(
	ctx context.Context,
	tx pgx.Tx,
	locked workspaceAnalysisModelLocks,
	command application.FinalizeWorkspaceAnalysisModelCandidateCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if locked.operation.status != domain.WorkspaceAnalysisOperationSucceeded ||
		locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
		locked.reservation.Status != domain.WorkspaceAnalysisBudgetSettled ||
		!sameModelRunResult(*locked.modelRun, command.Run) || !sameModelCallResult(*locked.modelCall, command.Call) {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model candidate terminal replay differs"))
	}
	candidate, err := loadWorkspaceAnalysisCandidateByIdentity(ctx, tx, command.Candidate.WorkspaceID,
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

func finalizeWorkspaceAnalysisModelCallTx(
	ctx context.Context,
	tx pgx.Tx,
	locked workspaceAnalysisModelLocks,
	command application.FinalizeWorkspaceAnalysisModelCallCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
		locked.modelCall.Version != command.ExpectedCallVersion || locked.modelRun.Version != command.ExpectedRunVersion {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model terminal version changed"))
	}
	if err := updateWorkspaceAnalysisModelCall(ctx, tx, command.ExpectedCallVersion, command.Call); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := updateWorkspaceAnalysisModelRun(ctx, tx, command.ExpectedRunVersion, command.Run); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	completedAt := *command.Run.CompletedAt
	unknown := command.Call.Status == domain.ModelCallUnknown
	if err := settleWorkspaceAnalysisModelReservation(ctx, tx, *locked.reservation, command.Call, completedAt, unknown); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := settleWorkspaceAnalysisModelRunBudget(ctx, tx, locked.analysisRun, *locked.reservation, command.Call, completedAt, unknown); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	operationStatus := domain.WorkspaceAnalysisOperationFailed
	if unknown {
		operationStatus = domain.WorkspaceAnalysisOperationUnknown
	}
	if err := completeWorkspaceAnalysisModelOperation(ctx, tx, locked.operation, command.Identity.NodeAttemptID,
		operationStatus, nil, command.Run.FinalErrorCode, completedAt); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	return application.WorkspaceAnalysisModelMutationResult{
		Run: command.Run, Call: command.Call, OperationID: locked.operation.id,
		ReservationID: locked.reservation.ID,
	}, nil
}

func finalizeWorkspaceAnalysisModelResultTx(
	ctx context.Context,
	tx pgx.Tx,
	locked workspaceAnalysisModelLocks,
	command application.FinalizeWorkspaceAnalysisModelResultCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
		locked.modelCall.Version != command.ExpectedCallVersion || locked.modelRun.Version != command.ExpectedRunVersion {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model result version changed"))
	}
	if err := updateWorkspaceAnalysisModelCall(ctx, tx, command.ExpectedCallVersion, command.Call); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := updateWorkspaceAnalysisModelRun(ctx, tx, command.ExpectedRunVersion, command.Run); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := insertWorkspaceAnalysisModelResult(ctx, tx, command.Result); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	completedAt := command.Result.CreatedAt
	if err := settleWorkspaceAnalysisModelReservation(ctx, tx, *locked.reservation, command.Call, completedAt, false); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := settleWorkspaceAnalysisModelRunBudget(ctx, tx, locked.analysisRun, *locked.reservation, command.Call, completedAt, false); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	resultKind := domain.WorkspaceAnalysisOperationResultModelCall
	resultID, resultHash := command.Result.ID, command.Result.DocumentHash
	if err := completeWorkspaceAnalysisModelOperation(ctx, tx, locked.operation, command.Identity.NodeAttemptID,
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

func finalizeWorkspaceAnalysisModelCandidateTx(
	ctx context.Context,
	tx pgx.Tx,
	locked workspaceAnalysisModelLocks,
	command application.FinalizeWorkspaceAnalysisModelCandidateCommand,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
		locked.modelCall.Version != command.ExpectedCallVersion || locked.modelRun.Version != command.ExpectedRunVersion {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelConflict(errors.New("workspace analysis model candidate version changed"))
	}
	if err := updateWorkspaceAnalysisModelCall(ctx, tx, command.ExpectedCallVersion, command.Call); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := updateWorkspaceAnalysisModelRun(ctx, tx, command.ExpectedRunVersion, command.Run); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := insertWorkspaceAnalysisCandidate(ctx, tx, command.Candidate); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	completedAt := command.Candidate.CreatedAt
	if err := settleWorkspaceAnalysisModelReservation(ctx, tx, *locked.reservation, command.Call, completedAt, false); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	if err := settleWorkspaceAnalysisModelRunBudget(ctx, tx, locked.analysisRun, *locked.reservation, command.Call, completedAt, false); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	result := domain.WorkspaceAnalysisOperationResultRef{
		Kind: domain.WorkspaceAnalysisOperationResultCandidate,
		ID:   command.Candidate.ID,
		Hash: command.Candidate.DocumentHash,
	}
	if err := completeWorkspaceAnalysisModelOperation(ctx, tx, locked.operation, command.Identity.NodeAttemptID,
		domain.WorkspaceAnalysisOperationSucceeded, &result, "", completedAt); err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, err
	}
	candidateCopy := command.Candidate
	return application.WorkspaceAnalysisModelMutationResult{
		Run: command.Run, Call: command.Call, Candidate: &candidateCopy, OperationID: locked.operation.id,
		ReservationID: locked.reservation.ID,
	}, nil
}

func updateWorkspaceAnalysisModelCall(ctx context.Context, tx pgx.Tx, expectedVersion int64, call domain.ModelCall) error {
	tag, err := tx.Exec(ctx, `UPDATE agent.model_call SET
		response_hash=$1,response_bytes=$2,input_tokens=$3,output_tokens=$4,latency_ms=$5,
		status=$6,error_code=$7,version=$8,completed_at=$9
		WHERE id=$10 AND model_run_id=$11 AND status='STARTED' AND version=$12`,
		optionalText(call.ResponseHash), call.ResponseBytes, call.Usage.InputTokens, call.Usage.OutputTokens,
		call.LatencyMillis, string(call.Status), optionalText(call.ErrorCode), call.Version, optionalTime(call.CompletedAt),
		string(call.ID), string(call.ModelRunID), expectedVersion,
	)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model call CAS failed"))
	}
	return nil
}

func updateWorkspaceAnalysisModelRun(ctx context.Context, tx pgx.Tx, expectedVersion int64, run domain.ModelRun) error {
	tag, err := tx.Exec(ctx, `UPDATE agent.model_run SET
		retrieval_index_version_id=$1,embedding_version_id=$2,rerank_model_version=$3,
		status=$4,final_result_type=$5,error_code=$6,version=$7,updated_at=$8,completed_at=$9
		WHERE id=$10 AND workspace_id=$11 AND workflow_run_id=$12 AND node_run_id=$13 AND node_attempt_id=$14
		  AND status='RUNNING' AND version=$15`,
		optionalFoundationID(run.Retrieval.IndexVersionID), optionalID(run.Retrieval.EmbeddingVersionID), optionalText(run.Retrieval.RerankModelVersion),
		string(run.Status), optionalText(run.FinalResultType), optionalText(run.FinalErrorCode), run.Version,
		run.UpdatedAt.UTC(), optionalTime(run.CompletedAt), string(run.ID), string(run.WorkspaceID),
		string(run.WorkflowRunID), string(run.NodeRunID), string(run.NodeAttemptID), expectedVersion,
	)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model run CAS failed"))
	}
	return nil
}

func insertWorkspaceAnalysisModelResult(ctx context.Context, tx pgx.Tx, result domain.WorkspaceAnalysisModelResult) error {
	var candidateID, candidateHash any
	if result.SubjectCandidateID != nil {
		candidateID, candidateHash = string(*result.SubjectCandidateID), result.SubjectCandidateHash
	}
	_, err := tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_model_result(
		id,workspace_id,analysis_run_id,operation_id,node_attempt_id,model_run_id,model_call_id,
		operation_kind,schema_id,schema_version,subject_candidate_id,subject_candidate_hash,
		document,document_hash,document_bytes,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		string(result.ID), string(result.WorkspaceID), string(result.AnalysisRunID), string(result.OperationID),
		string(result.NodeAttemptID), string(result.ModelRunID), string(result.ModelCallID), string(result.OperationKind),
		result.Schema.ID, result.Schema.Version, candidateID, candidateHash, []byte(result.Document),
		result.DocumentHash, result.DocumentBytes, result.CreatedAt.UTC(),
	)
	if err != nil {
		return classify(err)
	}
	return nil
}

func insertWorkspaceAnalysisCandidate(ctx context.Context, tx pgx.Tx, candidate domain.WorkspaceAnalysisCandidate) error {
	_, err := tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_candidate(
		id,workspace_id,analysis_run_id,answer_id,synthesis_operation_id,node_attempt_id,synthesis_model_run_id,
		schema_id,schema_version,document,document_hash,document_bytes,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		string(candidate.ID), string(candidate.WorkspaceID), string(candidate.AnalysisRunID), string(candidate.AnswerID),
		string(candidate.SynthesisOperationID), string(candidate.NodeAttemptID), string(candidate.SynthesisModelRunID),
		candidate.SchemaID, candidate.SchemaVersion, []byte(candidate.Document), candidate.DocumentHash,
		candidate.DocumentBytes, candidate.CreatedAt.UTC(),
	)
	if err != nil {
		return classify(err)
	}
	return nil
}

func settleWorkspaceAnalysisModelReservation(
	ctx context.Context,
	tx pgx.Tx,
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
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_budget_reservation SET
		status=$1,settled_model_calls=1,settled_tool_calls=0,settled_source_reads=0,
		settled_input_tokens=$2,settled_output_tokens=$3,settled_cost_microunits=NULL,settled_at=$4
		WHERE id=$5 AND workspace_id=$6 AND analysis_run_id=$7 AND operation_id=$8
		  AND call_kind='MODEL' AND model_call_id=$9 AND status='RESERVED'`,
		string(status), settledInput, settledOutput, completedAt.UTC(), string(reservation.ID),
		string(reservation.WorkspaceID), string(reservation.AnalysisRunID), string(reservation.OperationID), string(call.ID),
	)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model reservation CAS failed"))
	}
	return nil
}

func settleWorkspaceAnalysisModelRunBudget(
	ctx context.Context,
	tx pgx.Tx,
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
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
		reserved_model_calls=reserved_model_calls-$1,
		reserved_input_tokens=reserved_input_tokens-$2,reserved_output_tokens=reserved_output_tokens-$3,
		settled_model_calls=settled_model_calls+1,
		settled_input_tokens=settled_input_tokens+$4,settled_output_tokens=settled_output_tokens+$5,
		version=version+1,updated_at=GREATEST(updated_at,$6)
		WHERE id=$7 AND workspace_id=$8 AND workflow_run_id=$9 AND status='running' AND version=$10
		  AND reserved_model_calls>=$1 AND reserved_input_tokens>=$2 AND reserved_output_tokens>=$3`,
		reservation.Reserved.ModelCalls, reservation.Reserved.InputTokens, reservation.Reserved.OutputTokens,
		settledInput, settledOutput, completedAt.UTC(), string(run.ID), string(run.WorkspaceID),
		string(run.WorkflowRunID), run.Version,
	)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model run settlement CAS failed"))
	}
	return nil
}

func completeWorkspaceAnalysisModelOperation(
	ctx context.Context,
	tx pgx.Tx,
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
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_operation SET
		status=$1,latest_node_attempt_id=$2,result_kind=$3,result_id=$4,result_hash=$5,error_code=$6,
		version=version+1,updated_at=GREATEST(updated_at,$7),completed_at=GREATEST(updated_at,$7)
		WHERE id=$8 AND workspace_id=$9 AND analysis_run_id=$10 AND workflow_run_id=$11 AND node_run_id=$12
		  AND call_kind='MODEL' AND request_hash=$13 AND status='STARTED' AND version=$14`,
		string(status), string(latestAttemptID), resultKind, resultID, resultHash, persistedError, completedAt.UTC(),
		string(operation.id), string(operation.workspaceID), string(operation.analysisRunID),
		string(operation.workflowRunID), string(operation.nodeRunID), operation.requestHash, operation.version,
	)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model operation CAS failed"))
	}
	return nil
}

func forceWorkspaceAnalysisModelConstraints(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return classify(err)
	}
	return nil
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

func loadWorkspaceAnalysisModelResultByIdentity(
	ctx context.Context,
	queryer queryRower,
	workspaceID foundation.ID,
	analysisRunID foundation.ID,
	operationID foundation.ID,
	modelRunID foundation.ID,
	modelCallID foundation.ID,
	resultID foundation.ID,
) (domain.WorkspaceAnalysisModelResult, error) {
	result, err := scanWorkspaceAnalysisModelResult(queryer.QueryRow(ctx, `SELECT `+workspaceAnalysisModelResultColumns+`
		FROM agent.workspace_analysis_model_result AS result
		WHERE result.id=$1 AND result.workspace_id=$2 AND result.analysis_run_id=$3
		  AND result.operation_id=$4 AND result.model_run_id=$5 AND result.model_call_id=$6`,
		string(resultID), string(workspaceID), string(analysisRunID), string(operationID), string(modelRunID), string(modelCallID),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkspaceAnalysisModelResult{}, notFound()
	}
	if err != nil {
		return domain.WorkspaceAnalysisModelResult{}, classify(err)
	}
	if err := domain.ValidateWorkspaceAnalysisModelResult(result); err != nil {
		return domain.WorkspaceAnalysisModelResult{}, consistency(err)
	}
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

func loadWorkspaceAnalysisCandidateByIdentity(
	ctx context.Context,
	queryer queryRower,
	workspaceID foundation.ID,
	analysisRunID foundation.ID,
	operationID foundation.ID,
	modelRunID foundation.ID,
	candidateID foundation.ID,
) (domain.WorkspaceAnalysisCandidate, error) {
	candidate, err := scanWorkspaceAnalysisCandidate(queryer.QueryRow(ctx, `SELECT `+workspaceAnalysisCandidateColumns+`
		FROM agent.workspace_analysis_candidate AS candidate
		WHERE candidate.id=$1 AND candidate.workspace_id=$2 AND candidate.analysis_run_id=$3
		  AND candidate.synthesis_operation_id=$4 AND candidate.synthesis_model_run_id=$5`,
		string(candidateID), string(workspaceID), string(analysisRunID), string(operationID), string(modelRunID),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkspaceAnalysisCandidate{}, notFound()
	}
	if err != nil {
		return domain.WorkspaceAnalysisCandidate{}, classify(err)
	}
	if err := domain.ValidateWorkspaceAnalysisCandidate(candidate); err != nil {
		return domain.WorkspaceAnalysisCandidate{}, consistency(err)
	}
	return candidate, nil
}

func loadWorkspaceAnalysisCandidateSubject(
	ctx context.Context,
	queryer queryRower,
	workspaceID foundation.ID,
	analysisRunID foundation.ID,
	candidateID foundation.ID,
) (domain.WorkspaceAnalysisCandidate, error) {
	candidate, err := scanWorkspaceAnalysisCandidate(queryer.QueryRow(ctx, `SELECT `+workspaceAnalysisCandidateColumns+`
		FROM agent.workspace_analysis_candidate AS candidate
		WHERE candidate.id=$1 AND candidate.workspace_id=$2 AND candidate.analysis_run_id=$3`,
		string(candidateID), string(workspaceID), string(analysisRunID),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkspaceAnalysisCandidate{}, notFound()
	}
	if err != nil {
		return domain.WorkspaceAnalysisCandidate{}, classify(err)
	}
	if err := domain.ValidateWorkspaceAnalysisCandidate(candidate); err != nil {
		return domain.WorkspaceAnalysisCandidate{}, consistency(err)
	}
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

func (repository *Repository) recoverWorkspaceAnalysisModelAuthorization(
	ctx context.Context,
	command application.AuthorizeWorkspaceAnalysisModelCallCommand,
	commitErr error,
) (application.WorkspaceAnalysisModelAuthorizationResult, error) {
	tx, err := beginWorkspaceAnalysisModelTx(ctx, repository)
	if err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := lockWorkspaceAnalysisModelOperation(ctx, tx, command, false)
	if err == nil {
		err = validateWorkspaceAnalysisModelDefinition(locked, command.Identity)
	}
	if err == nil {
		err = validateWorkspaceAnalysisModelLockedBinding(ctx, tx, locked, command)
	}
	if err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}

	var result application.WorkspaceAnalysisModelAuthorizationResult
	switch locked.operation.status {
	case domain.WorkspaceAnalysisOperationStarted:
		if locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
			locked.operation.latestAttemptID == nil || *locked.operation.latestAttemptID != command.Identity.NodeAttemptID {
			return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelRecoveryUnknown(
				commitErr, errors.New("workspace analysis model authorization commit is not provable"),
			)
		}
		result = workspaceAnalysisModelAuthorizationResult(*locked.modelRun, *locked.modelCall,
			locked.operation.id, locked.reservation.ID, application.WorkspaceAnalysisModelAuthorizationReconcile)
	case domain.WorkspaceAnalysisOperationSucceeded,
		domain.WorkspaceAnalysisOperationFailed,
		domain.WorkspaceAnalysisOperationUnknown:
		result, err = workspaceAnalysisModelTerminalAuthorizationResult(ctx, tx, locked, command)
	default:
		err = errors.New("workspace analysis model authorization commit has no durable closure")
	}
	if err == nil {
		err = result.ValidateFor(command)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return application.WorkspaceAnalysisModelAuthorizationResult{}, workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}
	return result, nil
}

func (repository *Repository) recoverWorkspaceAnalysisModelCallFinalization(
	ctx context.Context,
	command application.FinalizeWorkspaceAnalysisModelCallCommand,
	commitErr error,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	tx, err := beginWorkspaceAnalysisModelTx(ctx, repository)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := lockWorkspaceAnalysisModelOperation(ctx, tx,
		authorizationCommandForTerminal(command.Identity, command.OperationKey, command.OperationID, command.ReservationID, command.Run, command.Call), false)
	if err == nil {
		err = validateWorkspaceAnalysisModelDefinition(locked, command.Identity)
	}
	if err == nil {
		err = validateWorkspaceAnalysisModelTerminalCommandBinding(ctx, tx, locked,
			command.OperationID, command.ReservationID, command.Run, command.Call)
	}
	var result application.WorkspaceAnalysisModelMutationResult
	if err == nil {
		result, err = replayWorkspaceAnalysisModelCallTerminal(locked, command)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}
	return result, nil
}

func (repository *Repository) recoverWorkspaceAnalysisModelResultFinalization(
	ctx context.Context,
	command application.FinalizeWorkspaceAnalysisModelResultCommand,
	commitErr error,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	tx, err := beginWorkspaceAnalysisModelTx(ctx, repository)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := lockWorkspaceAnalysisModelOperation(ctx, tx, authorizationCommandForResult(command), false)
	if err == nil {
		err = validateWorkspaceAnalysisModelDefinition(locked, command.Identity)
	}
	if err == nil {
		err = validateWorkspaceAnalysisModelTerminalCommandBinding(ctx, tx, locked,
			command.OperationID, command.ReservationID, command.Run, command.Call)
	}
	var result application.WorkspaceAnalysisModelMutationResult
	if err == nil {
		result, err = replayWorkspaceAnalysisModelResultTerminal(ctx, tx, locked, command)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}
	return result, nil
}

func (repository *Repository) recoverWorkspaceAnalysisModelCandidateFinalization(
	ctx context.Context,
	command application.FinalizeWorkspaceAnalysisModelCandidateCommand,
	commitErr error,
) (application.WorkspaceAnalysisModelMutationResult, error) {
	tx, err := beginWorkspaceAnalysisModelTx(ctx, repository)
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := lockWorkspaceAnalysisModelOperation(ctx, tx, authorizationCommandForCandidate(command), false)
	if err == nil {
		err = validateWorkspaceAnalysisModelDefinition(locked, command.Identity)
	}
	if err == nil {
		err = validateWorkspaceAnalysisModelTerminalCommandBinding(ctx, tx, locked,
			command.OperationID, command.ReservationID, command.Run, command.Call)
	}
	var result application.WorkspaceAnalysisModelMutationResult
	if err == nil {
		result, err = replayWorkspaceAnalysisModelCandidateTerminal(ctx, tx, locked, command)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return application.WorkspaceAnalysisModelMutationResult{}, workspaceAnalysisModelRecoveryUnknown(commitErr, err)
	}
	return result, nil
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

func classifyWorkspaceAnalysisModelLock(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis model authority binding was not found"))
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	return classify(err)
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
