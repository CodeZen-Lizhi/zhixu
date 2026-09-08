package postgres

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
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

func durationMillis(value time.Duration) int64 {
	return value.Milliseconds()
}

func durationFromMillis(value int64) time.Duration {
	return time.Duration(value) * time.Millisecond
}
