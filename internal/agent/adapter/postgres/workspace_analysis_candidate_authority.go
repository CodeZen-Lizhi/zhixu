package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/jackc/pgx/v5"
)

// LoadWorkspaceAnalysisCandidateAuthority 由数据库重新证明候选与成功 Synthesis 调用的完整闭包。
func (repository *Repository) LoadWorkspaceAnalysisCandidateAuthority(
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
	if repository == nil || repository.db == nil {
		return domain.WorkspaceAnalysisCandidate{}, workspaceAnalysisModelUnavailable(
			errors.New("workspace analysis candidate authority repository is unavailable"),
		)
	}

	candidate, err := scanWorkspaceAnalysisCandidate(repository.db.QueryRow(ctx, `SELECT `+workspaceAnalysisCandidateColumns+`
		FROM agent.workspace_analysis_candidate AS candidate
		JOIN agent.workspace_analysis_run AS analysis
		  ON analysis.id=candidate.analysis_run_id AND analysis.workspace_id=candidate.workspace_id
		 AND analysis.answer_id=candidate.answer_id AND analysis.workflow_run_id=$2
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
		WHERE candidate.workspace_id=$1 AND candidate.analysis_run_id=$3
		  AND candidate.id=$4 AND candidate.document_hash=$5`,
		string(query.WorkspaceID), string(query.WorkflowRunID), string(query.AnalysisRunID),
		string(query.CandidateID), query.CandidateHash,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkspaceAnalysisCandidate{}, notFound()
	}
	if err != nil {
		return domain.WorkspaceAnalysisCandidate{}, classify(err)
	}
	if err := domain.ValidateWorkspaceAnalysisCandidate(candidate); err != nil ||
		candidate.ID != query.CandidateID || candidate.DocumentHash != query.CandidateHash {
		return domain.WorkspaceAnalysisCandidate{}, consistency(
			errors.New("workspace analysis candidate authority binding drifted"),
		)
	}
	return candidate, nil
}

var _ application.WorkspaceAnalysisCandidateAuthorityReader = (*Repository)(nil)
