package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

// LoadWorkspaceAnalysisRunToolAuthorityScoped loads the minimal Analysis Run identity in the caller scope.
func (repository *GORMRepository) LoadWorkspaceAnalysisRunToolAuthorityScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	query application.WorkspaceAnalysisRunToolAuthorityQuery,
) (application.WorkspaceAnalysisRunToolAuthority, bool, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisRunToolAuthority{}, false, workspaceAnalysisModelInvalid(
			errors.New("workspace analysis run tool authority context is nil"),
		)
	}
	if err := query.Validate(); err != nil {
		return application.WorkspaceAnalysisRunToolAuthority{}, false, err
	}
	database, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return application.WorkspaceAnalysisRunToolAuthority{}, false, err
	}
	run, found, err := loadGORMWorkspaceAnalysisToolAuthorityRun(ctx, database, query.WorkspaceID, query.WorkflowRunID)
	if err != nil || !found {
		return application.WorkspaceAnalysisRunToolAuthority{}, found, err
	}
	authority := application.WorkspaceAnalysisRunToolAuthority{
		AnalysisRunID:     run.ID,
		WorkspaceID:       run.WorkspaceID,
		WorkflowRunID:     run.WorkflowRunID,
		DefinitionVersion: run.DefinitionVersion,
	}
	if err := authority.Validate(); err != nil {
		return application.WorkspaceAnalysisRunToolAuthority{}, false, consistency(err)
	}
	return authority, true, nil
}

// LoadWorkspaceAnalysisSuccessfulToolAuthorityScoped loads one verified Agent-owned receipt binding.
func (repository *GORMRepository) LoadWorkspaceAnalysisSuccessfulToolAuthorityScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	query application.WorkspaceAnalysisSuccessfulToolAuthorityQuery,
) (application.WorkspaceAnalysisSuccessfulToolAuthority, bool, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisSuccessfulToolAuthority{}, false, workspaceAnalysisModelInvalid(
			errors.New("workspace analysis successful tool authority context is nil"),
		)
	}
	if err := query.Validate(); err != nil {
		return application.WorkspaceAnalysisSuccessfulToolAuthority{}, false, err
	}
	database, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return application.WorkspaceAnalysisSuccessfulToolAuthority{}, false, err
	}

	run, found, err := loadGORMWorkspaceAnalysisToolAuthorityRun(ctx, database, query.WorkspaceID, query.WorkflowRunID)
	if err != nil {
		return application.WorkspaceAnalysisSuccessfulToolAuthority{}, false, err
	}
	if !found || run.ID != query.OperationKey.AnalysisRunID {
		return application.WorkspaceAnalysisSuccessfulToolAuthority{}, false, nil
	}

	row, err := gormRawRow(database, `SELECT `+gormWorkspaceAnalysisToolOperationColumns+`
		FROM agent.workspace_analysis_operation AS operation
		WHERE operation.workspace_id=? AND operation.workflow_run_id=? AND operation.analysis_run_id=?
		  AND operation.node_key=? AND operation.operation_kind=? AND operation.ordinal=?`,
		string(query.WorkspaceID), string(query.WorkflowRunID), string(query.OperationKey.AnalysisRunID),
		string(query.OperationKey.NodeKey), string(query.OperationKey.Kind), query.OperationKey.Ordinal,
	)
	if err != nil {
		return application.WorkspaceAnalysisSuccessfulToolAuthority{}, false, classifyGORM(ctx, err)
	}
	operationRecord, err := scanGORMWorkspaceAnalysisToolOperation(row)
	if gormNoRows(err) {
		return application.WorkspaceAnalysisSuccessfulToolAuthority{}, false, nil
	}
	if err != nil {
		return application.WorkspaceAnalysisSuccessfulToolAuthority{}, false, classifyGORM(ctx, err)
	}

	row, err = gormRawRow(database, `SELECT `+gormWorkspaceAnalysisToolReservationColumns+`
		FROM agent.workspace_analysis_budget_reservation AS reservation
		WHERE reservation.workspace_id=? AND reservation.analysis_run_id=? AND reservation.operation_id=?`,
		string(query.WorkspaceID), string(query.OperationKey.AnalysisRunID), string(operationRecord.id),
	)
	if err != nil {
		return application.WorkspaceAnalysisSuccessfulToolAuthority{}, false, classifyGORM(ctx, err)
	}
	reservation, err := scanGORMWorkspaceAnalysisToolReservation(row)
	if gormNoRows(err) {
		return application.WorkspaceAnalysisSuccessfulToolAuthority{}, false, consistency(
			errors.New("workspace analysis successful tool reservation is missing"),
		)
	}
	if err != nil {
		return application.WorkspaceAnalysisSuccessfulToolAuthority{}, false, classifyGORM(ctx, err)
	}

	operation := operationRecord.domain(&reservation.ID)
	if operationRecord.workspaceID != query.WorkspaceID || operationRecord.workflowRunID != query.WorkflowRunID ||
		operation.LogicalKey() != query.OperationKey || operation.Status != domain.WorkspaceAnalysisOperationSucceeded ||
		operation.Call == nil || operation.Call.Kind != domain.WorkspaceAnalysisOperationCallTool ||
		operation.Result == nil || operation.Result.Kind != domain.WorkspaceAnalysisOperationResultToolReceipt ||
		domain.ValidateWorkspaceAnalysisOperation(operation) != nil ||
		domain.ValidateWorkspaceAnalysisBudgetReservationBinding(run, reservation, operation) != nil {
		return application.WorkspaceAnalysisSuccessfulToolAuthority{}, false, consistency(
			errors.New("workspace analysis successful tool closure is invalid"),
		)
	}

	authority := application.WorkspaceAnalysisSuccessfulToolAuthority{
		AnalysisRunID: operation.AnalysisRunID,
		OperationID:   operation.ID,
		ReservationID: reservation.ID,
		ToolCallID:    operation.Call.ID,
		ResultID:      operation.Result.ID,
		ResultHash:    operation.Result.Hash,
	}
	if err := authority.Validate(); err != nil {
		return application.WorkspaceAnalysisSuccessfulToolAuthority{}, false, consistency(err)
	}
	return authority, true, nil
}

// CountWorkspaceAnalysisSourceReadOperationsScoped counts all persisted SOURCE_READ logical slots.
func (repository *GORMRepository) CountWorkspaceAnalysisSourceReadOperationsScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	query application.WorkspaceAnalysisSourceReadCountQuery,
) (int, error) {
	if ctx == nil {
		return 0, workspaceAnalysisModelInvalid(errors.New("workspace analysis source read count context is nil"))
	}
	if err := query.Validate(); err != nil {
		return 0, err
	}
	database, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return 0, err
	}
	run, found, err := loadGORMWorkspaceAnalysisToolAuthorityRun(ctx, database, query.WorkspaceID, query.WorkflowRunID)
	if err != nil {
		return 0, err
	}
	if !found || run.ID != query.AnalysisRunID {
		return 0, notFound()
	}

	row, err := gormRawRow(database, `SELECT count(*)
		FROM agent.workspace_analysis_operation
		WHERE workspace_id=? AND workflow_run_id=? AND analysis_run_id=? AND operation_kind='SOURCE_READ'`,
		string(query.WorkspaceID), string(query.WorkflowRunID), string(query.AnalysisRunID),
	)
	if err != nil {
		return 0, classifyGORM(ctx, err)
	}
	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, classifyGORM(ctx, err)
	}
	maxReads := domain.WorkspaceAnalysisV1MaxSourceReads
	if run.DefinitionVersion == 2 {
		maxReads = domain.WorkspaceAnalysisV2MaxSourceReads
	}
	if count < 0 || count > int64(maxReads) {
		return 0, consistency(errors.New("workspace analysis source read operation count is invalid"))
	}
	return int(count), nil
}

// LoadWorkspaceAnalysisToolCandidateAuthorityScoped loads a strict Candidate closure and returns no document body.
func (repository *GORMRepository) LoadWorkspaceAnalysisToolCandidateAuthorityScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	query application.WorkspaceAnalysisToolCandidateAuthorityQuery,
) (application.WorkspaceAnalysisToolCandidateAuthority, bool, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisToolCandidateAuthority{}, false, workspaceAnalysisModelInvalid(
			errors.New("workspace analysis tool candidate authority context is nil"),
		)
	}
	if err := query.Validate(); err != nil {
		return application.WorkspaceAnalysisToolCandidateAuthority{}, false, err
	}
	database, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return application.WorkspaceAnalysisToolCandidateAuthority{}, false, err
	}

	row, err := gormRawRow(database, `SELECT `+workspaceAnalysisCandidateColumns+`
		FROM agent.workspace_analysis_candidate AS candidate
		JOIN agent.workspace_analysis_run AS analysis
		  ON analysis.id=candidate.analysis_run_id AND analysis.workspace_id=candidate.workspace_id
		 AND analysis.answer_id=candidate.answer_id AND analysis.workflow_run_id=?
		WHERE candidate.id=? AND candidate.workspace_id=?`,
		string(query.WorkflowRunID), string(query.CandidateID), string(query.WorkspaceID),
	)
	if err != nil {
		return application.WorkspaceAnalysisToolCandidateAuthority{}, false, classifyGORM(ctx, err)
	}
	candidate, err := scanWorkspaceAnalysisCandidate(row)
	if gormNoRows(err) {
		return application.WorkspaceAnalysisToolCandidateAuthority{}, false, nil
	}
	if err != nil {
		return application.WorkspaceAnalysisToolCandidateAuthority{}, false, classifyGORM(ctx, err)
	}
	if err := domain.ValidateWorkspaceAnalysisCandidate(candidate); err != nil {
		return application.WorkspaceAnalysisToolCandidateAuthority{}, false, consistency(err)
	}

	row, err = gormRawRow(database, `SELECT 1
		FROM agent.workspace_analysis_candidate AS candidate
		JOIN agent.workspace_analysis_run AS analysis
		  ON analysis.id=candidate.analysis_run_id AND analysis.workspace_id=candidate.workspace_id
		 AND analysis.answer_id=candidate.answer_id AND analysis.workflow_run_id=?
		JOIN agent.workspace_analysis_operation AS operation
		  ON operation.id=candidate.synthesis_operation_id AND operation.workspace_id=candidate.workspace_id
		 AND operation.analysis_run_id=candidate.analysis_run_id AND operation.workflow_run_id=analysis.workflow_run_id
		 AND operation.operation_kind='ANSWER_SYNTHESIS' AND operation.status='SUCCEEDED'
		 AND operation.call_kind='MODEL' AND operation.first_node_attempt_id=candidate.node_attempt_id
		 AND operation.result_kind='SYNTHESIS_CANDIDATE' AND operation.result_id=candidate.id
		 AND operation.result_hash=candidate.document_hash
		JOIN agent.workspace_analysis_budget_reservation AS reservation
		  ON reservation.operation_id=operation.id AND reservation.workspace_id=candidate.workspace_id
		 AND reservation.analysis_run_id=analysis.id
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
		WHERE candidate.id=? AND candidate.workspace_id=? AND candidate.analysis_run_id=?`,
		string(query.WorkflowRunID), string(query.CandidateID), string(query.WorkspaceID), string(candidate.AnalysisRunID),
	)
	if err != nil {
		return application.WorkspaceAnalysisToolCandidateAuthority{}, false, classifyGORM(ctx, err)
	}
	var closureProof int
	if err := row.Scan(&closureProof); err != nil {
		if gormNoRows(err) {
			return application.WorkspaceAnalysisToolCandidateAuthority{}, false, consistency(
				errors.New("workspace analysis tool candidate closure is incomplete"),
			)
		}
		return application.WorkspaceAnalysisToolCandidateAuthority{}, false, classifyGORM(ctx, err)
	}
	if closureProof != 1 {
		return application.WorkspaceAnalysisToolCandidateAuthority{}, false, consistency(
			errors.New("workspace analysis tool candidate closure proof is invalid"),
		)
	}

	limits := domain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(domain.MaxWorkspaceAnalysisCandidateBytes)
	limits.MaxStringBytes = int(domain.MaxWorkspaceAnalysisCandidateBytes)
	decoded, err := domain.DecodeWorkspaceAnalysisCandidate(candidate.Document, limits)
	if err != nil {
		return application.WorkspaceAnalysisToolCandidateAuthority{}, false, consistency(err)
	}
	authority := application.WorkspaceAnalysisToolCandidateAuthority{
		CandidateID:   candidate.ID,
		AnalysisRunID: candidate.AnalysisRunID,
		CandidateHash: candidate.DocumentHash,
		CitationRefs:  append([]string(nil), decoded.Payload.CitationRefs...),
		SchemaVersion: candidate.SchemaVersion,
	}
	if err := authority.Validate(); err != nil {
		return application.WorkspaceAnalysisToolCandidateAuthority{}, false, consistency(err)
	}
	return authority, true, nil
}

func loadGORMWorkspaceAnalysisToolAuthorityRun(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
) (domain.WorkspaceAnalysisRun, bool, error) {
	row, err := gormRawRow(database, `SELECT `+workspaceAnalysisRunColumns+`
		FROM agent.workspace_analysis_run WHERE workspace_id=? AND workflow_run_id=?`,
		string(workspaceID), string(workflowRunID),
	)
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

var _ application.ScopedWorkspaceAnalysisToolAuthorityReader = (*GORMRepository)(nil)
