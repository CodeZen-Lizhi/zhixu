package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

const gormInsertWorkspaceAnalysisRunSQL = `
	INSERT INTO agent.workspace_analysis_run(
		id,workspace_id,conversation_id,question_id,answer_id,workflow_run_id,
		definition_key,definition_version,definition_hash,tool_catalog_hash,policy_version,config_revision,
		deadline_at,plan_model_timeout_ms,synthesis_model_timeout_ms,review_model_timeout_ms,
		git_tool_timeout_ms,search_tool_timeout_ms,source_read_tool_timeout_ms,validate_citation_tool_timeout_ms,durable_completion_margin_ms,
		max_nodes,max_model_calls,max_tool_calls,max_source_reads,max_tool_concurrency,max_input_tokens,max_output_tokens,max_cost_microunits,
		status,version,created_at,updated_at
	) VALUES(
		?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?
	) RETURNING ` + workspaceAnalysisRunColumns

// InsertWorkspaceAnalysisRunScoped inserts a queued run in the caller-owned scope.
func (repository *GORMRepository) InsertWorkspaceAnalysisRunScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	run domain.WorkspaceAnalysisRun,
) (domain.WorkspaceAnalysisRun, error) {
	if err := validateGORMWorkspaceAnalysisRunCall(
		repository,
		ctx,
		application.ErrorCodeWorkspaceAnalysisRunStartInvalid,
		application.ErrorCodeWorkspaceAnalysisRunStartUnavailable,
	); err != nil {
		return domain.WorkspaceAnalysisRun{}, err
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
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			application.ErrorCodeWorkspaceAnalysisRunStartUnavailable,
			true,
			errors.Join(errors.New("workspace analysis scoped transaction is unavailable"), err),
		)
	}
	row, err := gormRawRow(transaction.WithContext(ctx), gormInsertWorkspaceAnalysisRunSQL,
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
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, classifyGORM(ctx, err)
	}
	persisted, err := scanWorkspaceAnalysisRun(row)
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, classifyGORM(ctx, err)
	}
	return persisted, nil
}

// FindWorkspaceAnalysisRunScoped finds the immutable Question binding in the caller scope.
func (repository *GORMRepository) FindWorkspaceAnalysisRunScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	workspaceID foundation.ID,
	questionID foundation.ID,
) (domain.WorkspaceAnalysisRun, bool, error) {
	if err := validateGORMWorkspaceAnalysisRunCall(
		repository,
		ctx,
		application.ErrorCodeWorkspaceAnalysisRunStartInvalid,
		application.ErrorCodeWorkspaceAnalysisRunStartUnavailable,
	); err != nil {
		return domain.WorkspaceAnalysisRun{}, false, err
	}
	if !validID(workspaceID) || !validID(questionID) {
		return domain.WorkspaceAnalysisRun{}, false, foundation.NewError(
			foundation.ErrorInvalidInput,
			application.ErrorCodeWorkspaceAnalysisRunStartInvalid,
			false,
			errors.New("workspace analysis query identity is invalid"),
		)
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, false, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			application.ErrorCodeWorkspaceAnalysisRunStartUnavailable,
			true,
			errors.Join(errors.New("workspace analysis scoped transaction is unavailable"), err),
		)
	}
	row, err := gormRawRow(transaction.WithContext(ctx), `SELECT `+workspaceAnalysisRunColumns+`
		FROM agent.workspace_analysis_run WHERE workspace_id=? AND question_id=?`, string(workspaceID), string(questionID))
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
	return run, true, nil
}

// LoadWorkspaceAnalysisRunForExecution restores one immutable execution binding.
func (repository *GORMRepository) LoadWorkspaceAnalysisRunForExecution(
	ctx context.Context,
	query application.WorkspaceAnalysisRunExecutionQuery,
) (domain.WorkspaceAnalysisRun, error) {
	if err := validateGORMWorkspaceAnalysisRunCall(
		repository,
		ctx,
		application.ErrorCodeWorkspaceAnalysisRunExecutionInvalid,
		application.ErrorCodeWorkspaceAnalysisRunExecutionUnavailable,
	); err != nil {
		return domain.WorkspaceAnalysisRun{}, err
	}
	if err := query.Validate(); err != nil {
		return domain.WorkspaceAnalysisRun{}, err
	}
	row, err := gormRawRow(repository.database.WithContext(ctx), `SELECT `+workspaceAnalysisRunColumns+`
		FROM agent.workspace_analysis_run WHERE workspace_id=? AND workflow_run_id=?`,
		string(query.WorkspaceID), string(query.WorkflowRunID))
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, classifyGORM(ctx, err)
	}
	run, err := scanWorkspaceAnalysisRun(row)
	if gormNoRows(err) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunExecutionConflict(errors.New("workspace analysis execution run is missing"))
	}
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, classifyGORM(ctx, err)
	}
	if run.WorkspaceID != query.WorkspaceID || run.WorkflowRunID != query.WorkflowRunID ||
		run.ConversationID != query.ConversationID || run.QuestionID != query.QuestionID || run.AnswerID != query.AnswerID {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunExecutionConflict(errors.New("workspace analysis execution run binding differs"))
	}
	return run, nil
}

func validateGORMWorkspaceAnalysisRunCall(
	repository *GORMRepository,
	ctx context.Context,
	invalidCode string,
	unavailableCode string,
) error {
	if repository == nil || !validAgentGORMDatabase(repository.database) || nilAgentDependency(repository.unitOfWork) {
		return foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			unavailableCode,
			true,
			errors.New("workspace analysis GORM repository is unavailable"),
		)
	}
	if ctx == nil {
		return foundation.NewError(
			foundation.ErrorInvalidInput,
			invalidCode,
			false,
			errors.New("workspace analysis context is nil"),
		)
	}
	return nil
}

var _ application.ScopedWorkspaceAnalysisRunPersistence = (*GORMRepository)(nil)
var _ application.WorkspaceAnalysisRunLoader = (*GORMRepository)(nil)
