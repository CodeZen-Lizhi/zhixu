package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/jackc/pgx/v5"
)

var (
	workspaceAnalysisSearchRef = toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 2}
	workspaceAnalysisSourceRef = toolsdomain.ToolRef{Name: "ReadSource", Version: 3}
)

// LoadSearchKnowledgeV2Receipt 读取一个 Workspace Analysis Run 的唯一成功 Search receipt。
func (repository *Repository) LoadSearchKnowledgeV2Receipt(
	ctx context.Context,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
) (toolsdomain.ResultReceipt, error) {
	if ctx == nil || !canonicalAuthorityID(workspaceID) || !canonicalAuthorityID(workflowRunID) || workspaceID == workflowRunID {
		return toolsdomain.ResultReceipt{}, authorityInputError(errors.New("search receipt authority query is invalid"))
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return toolsdomain.ResultReceipt{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	analysisRunID, err := loadWorkspaceAnalysisRunID(ctx, tx, workspaceID, workflowRunID)
	if err != nil {
		return toolsdomain.ResultReceipt{}, err
	}
	receipt, err := loadWorkspaceAnalysisOperationReceipt(
		ctx, tx, workspaceID, workflowRunID, analysisRunID, "KNOWLEDGE_SEARCH", 1, workspaceAnalysisSearchRef,
	)
	if err != nil {
		return toolsdomain.ResultReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return toolsdomain.ResultReceipt{}, classify(err)
	}
	return receipt, nil
}

// LoadSearchKnowledgeV2PublicationAuthority 读取发布终态所需的 exact Search operation/receipt 闭包。
func (repository *Repository) LoadSearchKnowledgeV2PublicationAuthority(
	ctx context.Context,
	query toolsapplication.SearchKnowledgeV2PublicationAuthorityQuery,
) (toolsapplication.SearchKnowledgeV2PublicationAuthority, error) {
	if ctx == nil || !validAuthorityIdentitySet(
		query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID, query.ReceiptID,
	) || !canonicalAuthorityHash(query.ReceiptHash) {
		return toolsapplication.SearchKnowledgeV2PublicationAuthority{}, authorityInputError(
			errors.New("search publication authority query is invalid"),
		)
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return toolsapplication.SearchKnowledgeV2PublicationAuthority{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	analysisRunID, err := loadWorkspaceAnalysisRunID(ctx, tx, query.WorkspaceID, query.WorkflowRunID)
	if err != nil {
		return toolsapplication.SearchKnowledgeV2PublicationAuthority{}, err
	}
	if analysisRunID != query.AnalysisRunID {
		return toolsapplication.SearchKnowledgeV2PublicationAuthority{}, consistency(
			errors.New("search publication analysis run binding differs"),
		)
	}
	operationID, receipt, err := loadWorkspaceAnalysisOperationReceiptAuthority(
		ctx, tx, query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID,
		"KNOWLEDGE_SEARCH", 1, workspaceAnalysisSearchRef,
	)
	if err != nil {
		return toolsapplication.SearchKnowledgeV2PublicationAuthority{}, err
	}
	if receipt.ID != query.ReceiptID || receipt.OutputHash != query.ReceiptHash {
		return toolsapplication.SearchKnowledgeV2PublicationAuthority{}, consistency(
			errors.New("search publication receipt binding differs"),
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return toolsapplication.SearchKnowledgeV2PublicationAuthority{}, classify(err)
	}
	return toolsapplication.SearchKnowledgeV2PublicationAuthority{OperationID: operationID, Receipt: receipt}, nil
}

// LoadWorkspaceAnalysisSynthesisEvidence 读取合成节点所需的唯一 Search/Read receipt 闭包。
func (repository *Repository) LoadWorkspaceAnalysisSynthesisEvidence(
	ctx context.Context,
	query toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityQuery,
) (toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority, error) {
	if ctx == nil || !canonicalAuthorityID(query.WorkspaceID) || !canonicalAuthorityID(query.WorkflowRunID) ||
		!canonicalAuthorityID(query.AnalysisRunID) || query.WorkspaceID == query.WorkflowRunID ||
		query.WorkspaceID == query.AnalysisRunID || query.WorkflowRunID == query.AnalysisRunID {
		return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{}, authorityInputError(
			errors.New("synthesis evidence authority query is invalid"),
		)
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	analysisRunID, err := loadWorkspaceAnalysisRunID(ctx, tx, query.WorkspaceID, query.WorkflowRunID)
	if err != nil {
		return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{}, err
	}
	if analysisRunID != query.AnalysisRunID {
		return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{}, consistency(
			errors.New("synthesis evidence analysis run binding differs"),
		)
	}
	searchReceipt, err := loadWorkspaceAnalysisOperationReceipt(
		ctx, tx, query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID,
		"KNOWLEDGE_SEARCH", 1, workspaceAnalysisSearchRef,
	)
	if err != nil {
		return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{}, err
	}
	references, err := toolsdomain.SearchKnowledgeV2ReceiptSelectedRefs(searchReceipt)
	if err != nil || len(references) < 1 || len(references) > 3 {
		return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{}, consistency(
			errors.New("synthesis evidence search selection is invalid"),
		)
	}

	readReceipts := make([]toolsdomain.ResultReceipt, len(references))
	evidence := make([]toolsdomain.ReadSourceV3ReceiptEvidence, len(references))
	for index, reference := range references {
		readReceipts[index], err = loadWorkspaceAnalysisOperationReceipt(
			ctx, tx, query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID,
			"SOURCE_READ", int64(index+1), workspaceAnalysisSourceRef,
		)
		if err != nil {
			return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{}, err
		}
		evidence[index], err = toolsdomain.ReadSourceV3ReceiptEvidenceForSearch(readReceipts[index], searchReceipt)
		if err != nil || evidence[index].EvidenceRef != reference {
			return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{}, consistency(
				errors.New("synthesis evidence source receipt closure is invalid"),
			)
		}
	}
	var sourceReadOperations int
	if err := tx.QueryRow(ctx, `SELECT count(*)
		FROM agent.workspace_analysis_operation
		WHERE workspace_id=$1 AND workflow_run_id=$2 AND analysis_run_id=$3
		  AND operation_kind='SOURCE_READ'`,
		string(query.WorkspaceID), string(query.WorkflowRunID), string(query.AnalysisRunID),
	).Scan(&sourceReadOperations); err != nil {
		return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{}, classifyScan(err)
	}
	if sourceReadOperations != len(readReceipts) {
		return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{}, consistency(
			errors.New("synthesis evidence source operation count differs"),
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{}, classify(err)
	}
	return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{
		WorkspaceID: query.WorkspaceID, WorkflowRunID: query.WorkflowRunID, AnalysisRunID: query.AnalysisRunID,
		EvidenceRefs: append([]string{}, references...), SearchReceipt: searchReceipt,
		ReadSourceReceipts: readReceipts, Evidence: evidence,
	}, nil
}

// LoadValidateCitationV3Authority 读取候选的安全引用投影及其同 Run Search/ReadSource receipts。
func (repository *Repository) LoadValidateCitationV3Authority(
	ctx context.Context,
	query toolsapplication.ValidateCitationV3AuthorityQuery,
) (toolsapplication.ValidateCitationV3Authority, error) {
	if ctx == nil || !canonicalAuthorityID(query.WorkspaceID) || !canonicalAuthorityID(query.WorkflowRunID) ||
		!canonicalAuthorityID(query.CandidateID) {
		return toolsapplication.ValidateCitationV3Authority{}, authorityInputError(errors.New("citation authority query is invalid"))
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return toolsapplication.ValidateCitationV3Authority{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	candidate, references, err := loadWorkspaceAnalysisCandidateAuthority(ctx, tx, query)
	if err != nil {
		return toolsapplication.ValidateCitationV3Authority{}, err
	}
	searchReceipt, err := loadWorkspaceAnalysisOperationReceipt(
		ctx, tx, query.WorkspaceID, query.WorkflowRunID, candidate.AnalysisRunID,
		"KNOWLEDGE_SEARCH", 1, workspaceAnalysisSearchRef,
	)
	if err != nil {
		return toolsapplication.ValidateCitationV3Authority{}, err
	}
	readReceipts := make([]toolsdomain.ResultReceipt, len(references))
	for index, reference := range references {
		ordinal := int64(reference[1] - '0')
		readReceipts[index], err = loadWorkspaceAnalysisOperationReceipt(
			ctx, tx, query.WorkspaceID, query.WorkflowRunID, candidate.AnalysisRunID,
			"SOURCE_READ", ordinal, workspaceAnalysisSourceRef,
		)
		if err != nil {
			return toolsapplication.ValidateCitationV3Authority{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return toolsapplication.ValidateCitationV3Authority{}, classify(err)
	}
	return toolsapplication.ValidateCitationV3Authority{
		WorkspaceID: query.WorkspaceID, WorkflowRunID: query.WorkflowRunID,
		AnalysisRunID: candidate.AnalysisRunID, CandidateID: candidate.ID, CandidateHash: candidate.DocumentHash,
		EvidenceRefs: append([]string(nil), references...), SearchReceipt: searchReceipt,
		ReadSourceReceipts: readReceipts,
	}, nil
}

// LoadValidateCitationV3Receipt 读取与候选精确绑定的唯一成功 ValidateCitation@3 receipt。
func (repository *Repository) LoadValidateCitationV3Receipt(
	ctx context.Context,
	query toolsapplication.ValidateCitationV3ReceiptQuery,
) (toolsdomain.ResultReceipt, error) {
	if ctx == nil || !validAuthorityIdentitySet(
		query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID, query.CandidateID,
	) || !canonicalAuthorityHash(query.CandidateHash) {
		return toolsdomain.ResultReceipt{}, authorityInputError(errors.New("citation receipt authority query is invalid"))
	}

	tx, err := repository.begin(ctx)
	if err != nil {
		return toolsdomain.ResultReceipt{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, receipt, err := loadValidateCitationV3ReceiptClosure(ctx, tx, query)
	if err != nil {
		return toolsdomain.ResultReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return toolsdomain.ResultReceipt{}, classify(err)
	}
	return receipt, nil
}

// LoadValidateCitationV3PublicationAuthority 读取发布器所需的 exact Validation operation/receipt 闭包。
func (repository *Repository) LoadValidateCitationV3PublicationAuthority(
	ctx context.Context,
	query toolsapplication.ValidateCitationV3PublicationAuthorityQuery,
) (toolsapplication.ValidateCitationV3PublicationAuthority, error) {
	if ctx == nil || !validAuthorityIdentitySet(
		query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID, query.CandidateID, query.ReceiptID,
	) || !canonicalAuthorityHash(query.CandidateHash) || !canonicalAuthorityHash(query.ReceiptHash) {
		return toolsapplication.ValidateCitationV3PublicationAuthority{}, authorityInputError(
			errors.New("validation publication authority query is invalid"),
		)
	}

	tx, err := repository.begin(ctx)
	if err != nil {
		return toolsapplication.ValidateCitationV3PublicationAuthority{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	operationID, receipt, err := loadValidateCitationV3ReceiptClosure(
		ctx,
		tx,
		toolsapplication.ValidateCitationV3ReceiptQuery{
			WorkspaceID: query.WorkspaceID, WorkflowRunID: query.WorkflowRunID,
			AnalysisRunID: query.AnalysisRunID, CandidateID: query.CandidateID, CandidateHash: query.CandidateHash,
		},
	)
	if err != nil {
		return toolsapplication.ValidateCitationV3PublicationAuthority{}, err
	}
	if receipt.ID != query.ReceiptID || receipt.OutputHash != query.ReceiptHash {
		return toolsapplication.ValidateCitationV3PublicationAuthority{}, consistency(
			errors.New("validation publication receipt binding differs"),
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return toolsapplication.ValidateCitationV3PublicationAuthority{}, classify(err)
	}
	return toolsapplication.ValidateCitationV3PublicationAuthority{
		OperationID: operationID,
		Receipt:     receipt,
	}, nil
}

func loadValidateCitationV3ReceiptClosure(
	ctx context.Context,
	tx pgx.Tx,
	query toolsapplication.ValidateCitationV3ReceiptQuery,
) (foundation.ID, toolsdomain.ResultReceipt, error) {
	analysisRunID, err := loadWorkspaceAnalysisRunID(ctx, tx, query.WorkspaceID, query.WorkflowRunID)
	if err != nil {
		return "", toolsdomain.ResultReceipt{}, err
	}
	if analysisRunID != query.AnalysisRunID {
		return "", toolsdomain.ResultReceipt{}, consistency(errors.New("citation receipt analysis run binding differs"))
	}
	candidate, references, err := loadWorkspaceAnalysisCandidateAuthority(ctx, tx, toolsapplication.ValidateCitationV3AuthorityQuery{
		WorkspaceID: query.WorkspaceID, WorkflowRunID: query.WorkflowRunID, CandidateID: query.CandidateID,
	})
	if err != nil {
		return "", toolsdomain.ResultReceipt{}, err
	}
	if candidate.AnalysisRunID != query.AnalysisRunID || candidate.DocumentHash != query.CandidateHash {
		return "", toolsdomain.ResultReceipt{}, consistency(errors.New("citation receipt candidate binding differs"))
	}
	operationID, receipt, err := loadWorkspaceAnalysisOperationReceiptAuthority(
		ctx, tx, query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID,
		"CITATION_VALIDATION", 1, workspaceAnalysisCitationRef,
	)
	if err != nil {
		return "", toolsdomain.ResultReceipt{}, err
	}
	if _, err := toolsdomain.ValidateCitationV3ReceiptResults(
		receipt, query.CandidateID, query.CandidateHash, references,
	); err != nil {
		return "", toolsdomain.ResultReceipt{}, consistency(err)
	}
	return operationID, receipt, nil
}

func loadWorkspaceAnalysisRunID(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
) (foundation.ID, error) {
	var analysisRunID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM agent.workspace_analysis_run
		WHERE workspace_id=$1 AND workflow_run_id=$2`, string(workspaceID), string(workflowRunID)).Scan(&analysisRunID); err != nil {
		if noRows(err) {
			return "", receiptNotFound(err)
		}
		return "", classifyScan(err)
	}
	parsed, err := foundation.ParseID(analysisRunID)
	if err != nil || parsed == workspaceID || parsed == workflowRunID {
		return "", consistency(errors.New("workspace analysis run authority is invalid"))
	}
	return parsed, nil
}

func loadWorkspaceAnalysisOperationReceipt(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
	analysisRunID foundation.ID,
	operationKind string,
	ordinal int64,
	tool toolsdomain.ToolRef,
) (toolsdomain.ResultReceipt, error) {
	_, receipt, err := loadWorkspaceAnalysisOperationReceiptAuthority(
		ctx, tx, workspaceID, workflowRunID, analysisRunID, operationKind, ordinal, tool,
	)
	return receipt, err
}

func loadWorkspaceAnalysisOperationReceiptAuthority(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
	analysisRunID foundation.ID,
	operationKind string,
	ordinal int64,
	tool toolsdomain.ToolRef,
) (foundation.ID, toolsdomain.ResultReceipt, error) {
	var operationID, callID, resultID, resultHash string
	if err := tx.QueryRow(ctx, `SELECT id::text,tool_call_id::text,result_id::text,result_hash
		FROM agent.workspace_analysis_operation
		WHERE workspace_id=$1 AND workflow_run_id=$2 AND analysis_run_id=$3
		  AND operation_kind=$4 AND ordinal=$5 AND call_kind='TOOL' AND status='SUCCEEDED'
		  AND result_kind='TOOL_RESULT_RECEIPT'`,
		string(workspaceID), string(workflowRunID), string(analysisRunID), operationKind, ordinal,
	).Scan(&operationID, &callID, &resultID, &resultHash); err != nil {
		if noRows(err) {
			return "", toolsdomain.ResultReceipt{}, receiptNotFound(err)
		}
		return "", toolsdomain.ResultReceipt{}, classifyScan(err)
	}
	parsedOperationID, err := foundation.ParseID(operationID)
	if err != nil {
		return "", toolsdomain.ResultReceipt{}, consistency(
			errors.New("workspace analysis operation identity is invalid"),
		)
	}
	parsedCallID, err := foundation.ParseID(callID)
	if err != nil {
		return "", toolsdomain.ResultReceipt{}, consistency(errors.New("workspace analysis operation call identity is invalid"))
	}
	parsedResultID, err := foundation.ParseID(resultID)
	if err != nil || !canonicalAuthorityHash(resultHash) {
		return "", toolsdomain.ResultReceipt{}, consistency(
			errors.New("workspace analysis operation result identity is invalid"),
		)
	}
	call, err := loadCallByID(ctx, tx, workspaceID, parsedCallID)
	if err != nil {
		if noRows(err) {
			return "", toolsdomain.ResultReceipt{}, receiptNotFound(err)
		}
		return "", toolsdomain.ResultReceipt{}, classifyScan(err)
	}
	if call.WorkflowRunID != workflowRunID || call.Status != toolsdomain.CallSucceeded || call.Tool == nil || *call.Tool != tool {
		return "", toolsdomain.ResultReceipt{}, consistency(errors.New("workspace analysis operation call binding is invalid"))
	}
	definition, err := workspaceAnalysisBuiltinDefinition(tool)
	if err != nil {
		return "", toolsdomain.ResultReceipt{}, err
	}
	receipt, err := loadClosedResultReceipt(ctx, tx, call, definition)
	if err != nil {
		return "", toolsdomain.ResultReceipt{}, err
	}
	if err := validateWorkspaceAnalysisOperationReceiptBinding(
		parsedOperationID, parsedCallID, parsedResultID, resultHash, receipt,
	); err != nil {
		return "", toolsdomain.ResultReceipt{}, err
	}
	return parsedOperationID, receipt, nil
}

func validateWorkspaceAnalysisOperationReceiptBinding(
	operationID foundation.ID,
	callID foundation.ID,
	resultID foundation.ID,
	resultHash string,
	receipt toolsdomain.ResultReceipt,
) error {
	if !validAuthorityIdentitySet(operationID, callID, resultID) || !canonicalAuthorityHash(resultHash) ||
		receipt.ID != resultID || receipt.OutputHash != resultHash || receipt.ToolCallID != callID {
		return consistency(errors.New("workspace analysis operation result binding is invalid"))
	}
	return nil
}

func loadWorkspaceAnalysisCandidateAuthority(
	ctx context.Context,
	tx pgx.Tx,
	query toolsapplication.ValidateCitationV3AuthorityQuery,
) (agentdomain.WorkspaceAnalysisCandidate, []string, error) {
	var candidate agentdomain.WorkspaceAnalysisCandidate
	var id, workspaceID, analysisRunID, answerID, operationID, attemptID, modelRunID string
	var document []byte
	if err := tx.QueryRow(ctx, `SELECT
		candidate.id::text,candidate.workspace_id::text,candidate.analysis_run_id::text,candidate.answer_id::text,
		candidate.synthesis_operation_id::text,candidate.node_attempt_id::text,candidate.synthesis_model_run_id::text,
		candidate.schema_id,candidate.schema_version,candidate.document,candidate.document_hash,candidate.document_bytes,candidate.created_at
		FROM agent.workspace_analysis_candidate AS candidate
		JOIN agent.workspace_analysis_run AS analysis
		  ON analysis.id=candidate.analysis_run_id AND analysis.workspace_id=candidate.workspace_id
		WHERE candidate.id=$1 AND candidate.workspace_id=$2 AND analysis.workflow_run_id=$3`,
		string(query.CandidateID), string(query.WorkspaceID), string(query.WorkflowRunID),
	).Scan(
		&id, &workspaceID, &analysisRunID, &answerID, &operationID, &attemptID, &modelRunID,
		&candidate.SchemaID, &candidate.SchemaVersion, &document, &candidate.DocumentHash,
		&candidate.DocumentBytes, &candidate.CreatedAt,
	); err != nil {
		if noRows(err) {
			return agentdomain.WorkspaceAnalysisCandidate{}, nil, receiptNotFound(err)
		}
		return agentdomain.WorkspaceAnalysisCandidate{}, nil, classifyScan(err)
	}
	candidate.ID, candidate.WorkspaceID, candidate.AnalysisRunID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(analysisRunID)
	candidate.AnswerID, candidate.SynthesisOperationID = foundation.ID(answerID), foundation.ID(operationID)
	candidate.NodeAttemptID, candidate.SynthesisModelRunID = foundation.ID(attemptID), foundation.ID(modelRunID)
	candidate.Document = append(json.RawMessage(nil), document...)
	candidate.CreatedAt = candidate.CreatedAt.UTC().Truncate(time.Microsecond)
	if candidate.ID != query.CandidateID || candidate.WorkspaceID != query.WorkspaceID ||
		agentdomain.ValidateWorkspaceAnalysisCandidate(candidate) != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, nil, consistency(errors.New("workspace analysis candidate authority is invalid"))
	}
	limits := agentdomain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(agentdomain.MaxWorkspaceAnalysisCandidateBytes)
	decoded, err := agentdomain.DecodeWorkspaceAnalysisCandidate(candidate.Document, limits)
	if err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, nil, consistency(errors.New("workspace analysis candidate document is invalid"))
	}
	return candidate, append([]string(nil), decoded.Payload.CitationRefs...), nil
}

func workspaceAnalysisBuiltinDefinition(ref toolsdomain.ToolRef) (toolsdomain.Definition, error) {
	contracts, err := catalog.Contracts()
	if err != nil {
		return toolsdomain.Definition{}, consistency(errors.New("workspace analysis catalog is invalid"))
	}
	for _, contract := range contracts {
		if contract.Definition.Ref == ref {
			if contract.Definition.ResultPersistencePolicy != toolsdomain.ResultPersistenceCanonical {
				return toolsdomain.Definition{}, consistency(errors.New("workspace analysis receipt definition is not canonical"))
			}
			return contract.Definition, nil
		}
	}
	return toolsdomain.Definition{}, consistency(errors.New("workspace analysis receipt definition is unavailable"))
}

func canonicalAuthorityID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func canonicalAuthorityHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validAuthorityIdentitySet(ids ...foundation.ID) bool {
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalAuthorityID(id) {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func authorityInputError(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, toolsdomain.ErrorCodeResultReceiptInvalid, false, cause)
}

var _ toolsapplication.SearchKnowledgeV2ReceiptReader = (*Repository)(nil)
var _ toolsapplication.SearchKnowledgeV2PublicationAuthorityReader = (*Repository)(nil)
var _ toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityReader = (*Repository)(nil)
var _ toolsapplication.ValidateCitationV3AuthorityReader = (*Repository)(nil)
var _ toolsapplication.ValidateCitationV3ReceiptReader = (*Repository)(nil)
var _ toolsapplication.ValidateCitationV3PublicationAuthorityReader = (*Repository)(nil)
