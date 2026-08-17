package postgres

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

const workspaceAnalysisRunColumns = `
	id::text,workspace_id::text,conversation_id::text,question_id::text,answer_id::text,workflow_run_id::text,
	definition_key,definition_version,definition_hash,tool_catalog_hash,policy_version,config_revision,
	deadline_at,plan_model_timeout_ms,synthesis_model_timeout_ms,review_model_timeout_ms,
	git_tool_timeout_ms,search_tool_timeout_ms,source_read_tool_timeout_ms,validate_citation_tool_timeout_ms,
	max_nodes,max_model_calls,max_tool_calls,max_source_reads,max_tool_concurrency,max_input_tokens,max_output_tokens,max_cost_microunits,
	reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,reserved_cost_microunits,
	settled_model_calls,settled_tool_calls,settled_source_reads,settled_input_tokens,settled_output_tokens,settled_cost_microunits,
	status,termination_reason,validation_receipt_id::text,review_model_run_id::text,version,created_at,updated_at,completed_at`

// InsertWorkspaceAnalysisRunTx 在调用方事务中插入全新 queued Analysis Run。
func (repository *Repository) InsertWorkspaceAnalysisRunTx(
	ctx context.Context,
	transaction any,
	run domain.WorkspaceAnalysisRun,
) (domain.WorkspaceAnalysisRun, error) {
	tx, ok := workspaceAnalysisTransaction(transaction)
	if repository == nil || !ok {
		return domain.WorkspaceAnalysisRun{}, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			application.ErrorCodeWorkspaceAnalysisRunStartUnavailable,
			true,
			errors.New("workspace analysis transaction is unavailable"),
		)
	}
	if err := domain.ValidateWorkspaceAnalysisRun(run); err != nil || run.Status != domain.WorkspaceAnalysisRunQueued || run.Version != 1 {
		if err == nil {
			err = errors.New("workspace analysis run must start queued at version one")
		}
		return domain.WorkspaceAnalysisRun{}, foundation.NewError(
			foundation.ErrorInvalidInput,
			application.ErrorCodeWorkspaceAnalysisRunStartInvalid,
			false,
			err,
		)
	}
	row := tx.QueryRow(ctx, `INSERT INTO agent.workspace_analysis_run(
		id,workspace_id,conversation_id,question_id,answer_id,workflow_run_id,
		definition_key,definition_version,definition_hash,tool_catalog_hash,policy_version,config_revision,
		deadline_at,plan_model_timeout_ms,synthesis_model_timeout_ms,review_model_timeout_ms,
		git_tool_timeout_ms,search_tool_timeout_ms,source_read_tool_timeout_ms,validate_citation_tool_timeout_ms,durable_completion_margin_ms,
		max_nodes,max_model_calls,max_tool_calls,max_source_reads,max_tool_concurrency,max_input_tokens,max_output_tokens,max_cost_microunits,
		status,version,created_at,updated_at
	) VALUES(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,
		$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33
	) RETURNING `+workspaceAnalysisRunColumns,
		string(run.ID), string(run.WorkspaceID), string(run.ConversationID), string(run.QuestionID), string(run.AnswerID), string(run.WorkflowRunID),
		run.DefinitionKey, run.DefinitionVersion, run.DefinitionHash, run.ToolCatalogHash, run.PolicyVersion, run.ConfigRevision,
		run.DeadlineAt.UTC(), durationMillis(run.Timeouts.PlanModelTimeout), durationMillis(run.Timeouts.SynthesisModelTimeout),
		durationMillis(run.Timeouts.ReviewModelTimeout), durationMillis(run.Timeouts.GitToolTimeout),
		durationMillis(run.Timeouts.SearchToolTimeout), durationMillis(run.Timeouts.SourceReadToolTimeout),
		durationMillis(run.Timeouts.ValidateCitationToolTimeout), durationMillis(domain.WorkspaceAnalysisV1DurableCompletionMargin),
		run.Limits.Nodes, run.Limits.Amount.ModelCalls, run.Limits.Amount.ToolCalls, run.Limits.Amount.SourceReads,
		run.Limits.ToolConcurrency, run.Limits.Amount.InputTokens, run.Limits.Amount.OutputTokens,
		nullableInt64(run.Limits.Amount.CostMicrounits), string(run.Status), run.Version, run.CreatedAt.UTC(), run.UpdatedAt.UTC(),
	)
	persisted, err := scanWorkspaceAnalysisRun(row)
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, classify(err)
	}
	return persisted, nil
}

// FindWorkspaceAnalysisRunTx 按不可变 Question 绑定读取 Analysis Run。
func (repository *Repository) FindWorkspaceAnalysisRunTx(
	ctx context.Context,
	transaction any,
	workspaceID foundation.ID,
	questionID foundation.ID,
) (domain.WorkspaceAnalysisRun, bool, error) {
	tx, ok := workspaceAnalysisTransaction(transaction)
	if repository == nil || !ok {
		return domain.WorkspaceAnalysisRun{}, false, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			application.ErrorCodeWorkspaceAnalysisRunStartUnavailable,
			true,
			errors.New("workspace analysis transaction is unavailable"),
		)
	}
	if !validID(workspaceID) || !validID(questionID) {
		return domain.WorkspaceAnalysisRun{}, false, foundation.NewError(
			foundation.ErrorInvalidInput,
			application.ErrorCodeWorkspaceAnalysisRunStartInvalid,
			false,
			errors.New("workspace analysis query identity is invalid"),
		)
	}
	run, err := scanWorkspaceAnalysisRun(tx.QueryRow(ctx, `SELECT `+workspaceAnalysisRunColumns+`
		FROM agent.workspace_analysis_run WHERE workspace_id=$1 AND question_id=$2`, string(workspaceID), string(questionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkspaceAnalysisRun{}, false, nil
	}
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, false, classify(err)
	}
	return run, true, nil
}

// LoadWorkspaceAnalysisRunForExecution 按 Workflow Run 读取并复核不可变 Question/Answer 绑定。
func (repository *Repository) LoadWorkspaceAnalysisRunForExecution(
	ctx context.Context,
	query application.WorkspaceAnalysisRunExecutionQuery,
) (domain.WorkspaceAnalysisRun, error) {
	if repository == nil || repository.db == nil {
		return domain.WorkspaceAnalysisRun{}, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			application.ErrorCodeWorkspaceAnalysisRunExecutionUnavailable,
			true,
			errors.New("workspace analysis repository is unavailable"),
		)
	}
	if ctx == nil {
		return domain.WorkspaceAnalysisRun{}, foundation.NewError(
			foundation.ErrorInvalidInput,
			application.ErrorCodeWorkspaceAnalysisRunExecutionInvalid,
			false,
			errors.New("workspace analysis execution context is nil"),
		)
	}
	if err := query.Validate(); err != nil {
		return domain.WorkspaceAnalysisRun{}, err
	}
	run, err := scanWorkspaceAnalysisRun(repository.db.QueryRow(ctx, `SELECT `+workspaceAnalysisRunColumns+`
		FROM agent.workspace_analysis_run WHERE workspace_id=$1 AND workflow_run_id=$2`,
		string(query.WorkspaceID), string(query.WorkflowRunID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunExecutionConflict(errors.New("workspace analysis execution run is missing"))
	}
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, classify(err)
	}
	if run.WorkspaceID != query.WorkspaceID || run.WorkflowRunID != query.WorkflowRunID ||
		run.ConversationID != query.ConversationID || run.QuestionID != query.QuestionID || run.AnswerID != query.AnswerID {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunExecutionConflict(errors.New("workspace analysis execution run binding differs"))
	}
	return run, nil
}

func workspaceAnalysisRunExecutionConflict(cause error) error {
	return foundation.NewError(
		foundation.ErrorConsistencyViolation,
		application.ErrorCodeWorkspaceAnalysisRunExecutionConflict,
		false,
		cause,
	)
}

func scanWorkspaceAnalysisRun(row rowScanner) (domain.WorkspaceAnalysisRun, error) {
	var run domain.WorkspaceAnalysisRun
	var id, workspaceID, conversationID, questionID, answerID, workflowRunID string
	var status string
	var terminationReason, validationReceiptID, reviewModelRunID *string
	var planTimeoutMS, synthesisTimeoutMS, reviewTimeoutMS int64
	var gitTimeoutMS, searchTimeoutMS, sourceTimeoutMS, validateTimeoutMS int64
	if err := row.Scan(
		&id, &workspaceID, &conversationID, &questionID, &answerID, &workflowRunID,
		&run.DefinitionKey, &run.DefinitionVersion, &run.DefinitionHash, &run.ToolCatalogHash, &run.PolicyVersion, &run.ConfigRevision,
		&run.DeadlineAt, &planTimeoutMS, &synthesisTimeoutMS, &reviewTimeoutMS,
		&gitTimeoutMS, &searchTimeoutMS, &sourceTimeoutMS, &validateTimeoutMS,
		&run.Limits.Nodes, &run.Limits.Amount.ModelCalls, &run.Limits.Amount.ToolCalls, &run.Limits.Amount.SourceReads,
		&run.Limits.ToolConcurrency, &run.Limits.Amount.InputTokens, &run.Limits.Amount.OutputTokens, &run.Limits.Amount.CostMicrounits,
		&run.Reserved.ModelCalls, &run.Reserved.ToolCalls, &run.Reserved.SourceReads, &run.Reserved.InputTokens, &run.Reserved.OutputTokens, &run.Reserved.CostMicrounits,
		&run.Settled.ModelCalls, &run.Settled.ToolCalls, &run.Settled.SourceReads, &run.Settled.InputTokens, &run.Settled.OutputTokens, &run.Settled.CostMicrounits,
		&status, &terminationReason, &validationReceiptID, &reviewModelRunID, &run.Version, &run.CreatedAt, &run.UpdatedAt, &run.CompletedAt,
	); err != nil {
		return domain.WorkspaceAnalysisRun{}, err
	}
	run.ID, run.WorkspaceID, run.ConversationID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(conversationID)
	run.QuestionID, run.AnswerID, run.WorkflowRunID = foundation.ID(questionID), foundation.ID(answerID), foundation.ID(workflowRunID)
	run.Timeouts = domain.WorkspaceAnalysisV1Timeouts{
		PlanModelTimeout: durationFromMillis(planTimeoutMS), SynthesisModelTimeout: durationFromMillis(synthesisTimeoutMS),
		ReviewModelTimeout: durationFromMillis(reviewTimeoutMS), GitToolTimeout: durationFromMillis(gitTimeoutMS),
		SearchToolTimeout: durationFromMillis(searchTimeoutMS), SourceReadToolTimeout: durationFromMillis(sourceTimeoutMS),
		ValidateCitationToolTimeout: durationFromMillis(validateTimeoutMS),
	}
	run.Status = domain.WorkspaceAnalysisRunStatus(status)
	if terminationReason != nil {
		run.TerminationReason = domain.WorkspaceAnalysisRunTerminationReason(*terminationReason)
	}
	if validationReceiptID != nil {
		value := foundation.ID(*validationReceiptID)
		run.ValidationReceiptID = &value
	}
	if reviewModelRunID != nil {
		value := foundation.ID(*reviewModelRunID)
		run.ReviewModelRunID = &value
	}
	if err := domain.ValidateWorkspaceAnalysisRun(run); err != nil {
		return domain.WorkspaceAnalysisRun{}, consistency(err)
	}
	return run, nil
}

func workspaceAnalysisTransaction(transaction any) (pgx.Tx, bool) {
	tx, ok := transaction.(pgx.Tx)
	if !ok || tx == nil {
		return nil, false
	}
	value := reflect.ValueOf(tx)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil, false
	}
	return tx, true
}

func durationMillis(value time.Duration) int64 {
	return value.Milliseconds()
}

func durationFromMillis(value int64) time.Duration {
	return time.Duration(value) * time.Millisecond
}

var _ application.WorkspaceAnalysisRunPersistence = (*Repository)(nil)
var _ application.WorkspaceAnalysisRunLoader = (*Repository)(nil)
