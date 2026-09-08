package postgres

import (
	"context"
	"encoding/json"
	"errors"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"gorm.io/gorm"
)

const gormToolResultReceiptColumns = `
	id::text,tool_call_id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,
	tool_name,tool_version,output_schema_id,output_schema_version,definition_hash,persistence_policy,
	max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,
	server_binding_schema_id,server_binding_schema_version,server_binding_document,server_binding_hash,server_binding_bytes,created_at`

var (
	gormWorkspaceAnalysisSearchRef   = toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 2}
	gormWorkspaceAnalysisSourceRef   = toolsdomain.ToolRef{Name: "ReadSource", Version: 3}
	gormWorkspaceAnalysisCitationRef = toolsdomain.ToolRef{Name: "ValidateCitation", Version: 3}
)

// LoadSearchKnowledgeV2Receipt loads the sole successful Search operation in
// one UoW; Agent proves the operation closure and Tools proves the
// persisted Call/Receipt closure it owns.
func (repository *GORMWorkspaceAnalysisRepository) LoadSearchKnowledgeV2Receipt(
	ctx context.Context,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
) (toolsdomain.ResultReceipt, error) {
	if ctx == nil || !canonicalAuthorityID(workspaceID) || !canonicalAuthorityID(workflowRunID) || workspaceID == workflowRunID {
		return toolsdomain.ResultReceipt{}, authorityInputError(errors.New("search receipt authority query is invalid"))
	}
	var receipt toolsdomain.ResultReceipt
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		run, err := repository.gormLoadWorkspaceAnalysisRunAuthority(callbackCtx, scope, workspaceID, workflowRunID)
		if err != nil {
			return err
		}
		receipt, err = repository.gormLoadWorkspaceAnalysisSuccessfulReceipt(
			callbackCtx, database, scope, workspaceID, workflowRunID,
			gormWorkspaceAnalysisOperationKey(run.AnalysisRunID, agentdomain.WorkspaceAnalysisOperationNodeRetrieveEvidence, agentdomain.WorkspaceAnalysisOperationKnowledgeSearch, 1),
			gormWorkspaceAnalysisSearchRef,
		)
		return err
	})
	if err != nil {
		return toolsdomain.ResultReceipt{}, classifyGORMTools(ctx, err)
	}
	return receipt, nil
}

func (repository *GORMWorkspaceAnalysisRepository) LoadSearchKnowledgeV2PublicationAuthority(
	ctx context.Context,
	query toolsapplication.SearchKnowledgeV2PublicationAuthorityQuery,
) (toolsapplication.SearchKnowledgeV2PublicationAuthority, error) {
	if ctx == nil || !validAuthorityIdentitySet(query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID, query.ReceiptID) ||
		!canonicalAuthorityHash(query.ReceiptHash) {
		return toolsapplication.SearchKnowledgeV2PublicationAuthority{}, authorityInputError(errors.New("search publication authority query is invalid"))
	}
	var result toolsapplication.SearchKnowledgeV2PublicationAuthority
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		run, err := repository.gormLoadWorkspaceAnalysisRunAuthority(callbackCtx, scope, query.WorkspaceID, query.WorkflowRunID)
		if err != nil {
			return err
		}
		if run.AnalysisRunID != query.AnalysisRunID {
			return consistency(errors.New("search publication analysis run binding differs"))
		}
		authority, receipt, err := repository.gormLoadWorkspaceAnalysisSuccessfulReceiptAuthority(
			callbackCtx, database, scope, query.WorkspaceID, query.WorkflowRunID,
			gormWorkspaceAnalysisOperationKey(query.AnalysisRunID, agentdomain.WorkspaceAnalysisOperationNodeRetrieveEvidence, agentdomain.WorkspaceAnalysisOperationKnowledgeSearch, 1),
			gormWorkspaceAnalysisSearchRef,
		)
		if err != nil {
			return err
		}
		if receipt.ID != query.ReceiptID || receipt.OutputHash != query.ReceiptHash {
			return consistency(errors.New("search publication receipt binding differs"))
		}
		result = toolsapplication.SearchKnowledgeV2PublicationAuthority{OperationID: authority.OperationID, Receipt: receipt}
		return nil
	})
	if err != nil {
		return toolsapplication.SearchKnowledgeV2PublicationAuthority{}, classifyGORMTools(ctx, err)
	}
	return result, nil
}

func (repository *GORMWorkspaceAnalysisRepository) LoadWorkspaceAnalysisSynthesisEvidence(
	ctx context.Context,
	query toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityQuery,
) (toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority, error) {
	if ctx == nil || !validAuthorityIdentitySet(query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID) {
		return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{}, authorityInputError(errors.New("synthesis evidence authority query is invalid"))
	}
	var result toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		run, err := repository.gormLoadWorkspaceAnalysisRunAuthority(callbackCtx, scope, query.WorkspaceID, query.WorkflowRunID)
		if err != nil {
			return err
		}
		if run.AnalysisRunID != query.AnalysisRunID {
			return consistency(errors.New("synthesis evidence analysis run binding differs"))
		}
		search, err := repository.gormLoadWorkspaceAnalysisSuccessfulReceipt(
			callbackCtx, database, scope, query.WorkspaceID, query.WorkflowRunID,
			gormWorkspaceAnalysisOperationKey(query.AnalysisRunID, agentdomain.WorkspaceAnalysisOperationNodeRetrieveEvidence, agentdomain.WorkspaceAnalysisOperationKnowledgeSearch, 1),
			gormWorkspaceAnalysisSearchRef,
		)
		if err != nil {
			return err
		}
		references, err := toolsdomain.SearchKnowledgeV2ReceiptSelectedRefs(search)
		if err != nil || len(references) < 1 || len(references) > agentdomain.WorkspaceAnalysisV1MaxSourceReads {
			return consistency(errors.New("synthesis evidence search selection is invalid"))
		}
		reads := make([]toolsdomain.ResultReceipt, len(references))
		evidence := make([]toolsdomain.ReadSourceV3ReceiptEvidence, len(references))
		for index, reference := range references {
			reads[index], err = repository.gormLoadWorkspaceAnalysisSuccessfulReceipt(
				callbackCtx, database, scope, query.WorkspaceID, query.WorkflowRunID,
				gormWorkspaceAnalysisOperationKey(query.AnalysisRunID, agentdomain.WorkspaceAnalysisOperationNodeReadEvidence, agentdomain.WorkspaceAnalysisOperationSourceRead, index+1),
				gormWorkspaceAnalysisSourceRef,
			)
			if err != nil {
				return err
			}
			evidence[index], err = toolsdomain.ReadSourceV3ReceiptEvidenceForSearch(reads[index], search)
			if err != nil || evidence[index].EvidenceRef != reference {
				return consistency(errors.New("synthesis evidence source receipt closure is invalid"))
			}
		}
		count, err := repository.authority.CountWorkspaceAnalysisSourceReadOperationsScoped(callbackCtx, scope,
			agentapplication.WorkspaceAnalysisSourceReadCountQuery{WorkspaceID: query.WorkspaceID, WorkflowRunID: query.WorkflowRunID, AnalysisRunID: query.AnalysisRunID})
		if err != nil {
			return classifyGORMTools(callbackCtx, err)
		}
		if count != len(reads) {
			return consistency(errors.New("synthesis evidence source operation count differs"))
		}
		result = toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{
			WorkspaceID: query.WorkspaceID, WorkflowRunID: query.WorkflowRunID, AnalysisRunID: query.AnalysisRunID,
			EvidenceRefs: append([]string(nil), references...), SearchReceipt: search, ReadSourceReceipts: reads, Evidence: evidence,
		}
		return nil
	})
	if err != nil {
		return toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{}, classifyGORMTools(ctx, err)
	}
	return result, nil
}

func (repository *GORMWorkspaceAnalysisRepository) LoadValidateCitationV3Authority(
	ctx context.Context,
	query toolsapplication.ValidateCitationV3AuthorityQuery,
) (toolsapplication.ValidateCitationV3Authority, error) {
	if ctx == nil || !validAuthorityIdentitySet(query.WorkspaceID, query.WorkflowRunID, query.CandidateID) {
		return toolsapplication.ValidateCitationV3Authority{}, authorityInputError(errors.New("citation authority query is invalid"))
	}
	var result toolsapplication.ValidateCitationV3Authority
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		candidate, err := repository.gormLoadWorkspaceAnalysisCandidateAuthority(callbackCtx, scope, query.WorkspaceID, query.WorkflowRunID, query.CandidateID)
		if err != nil {
			return err
		}
		search, reads, err := repository.gormLoadWorkspaceAnalysisEvidenceForReferences(
			callbackCtx, database, scope, query.WorkspaceID, query.WorkflowRunID, candidate.AnalysisRunID, candidate.CitationRefs,
		)
		if err != nil {
			return err
		}
		result = toolsapplication.ValidateCitationV3Authority{
			WorkspaceID: query.WorkspaceID, WorkflowRunID: query.WorkflowRunID, AnalysisRunID: candidate.AnalysisRunID,
			CandidateID: candidate.CandidateID, CandidateHash: candidate.CandidateHash,
			EvidenceRefs: append([]string(nil), candidate.CitationRefs...), SearchReceipt: search, ReadSourceReceipts: reads,
		}
		return nil
	})
	if err != nil {
		return toolsapplication.ValidateCitationV3Authority{}, classifyGORMTools(ctx, err)
	}
	return result, nil
}

func (repository *GORMWorkspaceAnalysisRepository) LoadValidateCitationV3Receipt(
	ctx context.Context,
	query toolsapplication.ValidateCitationV3ReceiptQuery,
) (toolsdomain.ResultReceipt, error) {
	if ctx == nil || !validAuthorityIdentitySet(query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID, query.CandidateID) ||
		!canonicalAuthorityHash(query.CandidateHash) {
		return toolsdomain.ResultReceipt{}, authorityInputError(errors.New("citation receipt authority query is invalid"))
	}
	_, receipt, err := repository.gormLoadValidateCitationV3ReceiptClosure(ctx, query)
	return receipt, err
}

func (repository *GORMWorkspaceAnalysisRepository) LoadValidateCitationV3PublicationAuthority(
	ctx context.Context,
	query toolsapplication.ValidateCitationV3PublicationAuthorityQuery,
) (toolsapplication.ValidateCitationV3PublicationAuthority, error) {
	if ctx == nil || !validAuthorityIdentitySet(query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID, query.CandidateID, query.ReceiptID) ||
		!canonicalAuthorityHash(query.CandidateHash) || !canonicalAuthorityHash(query.ReceiptHash) {
		return toolsapplication.ValidateCitationV3PublicationAuthority{}, authorityInputError(errors.New("validation publication authority query is invalid"))
	}
	operationID, receipt, err := repository.gormLoadValidateCitationV3ReceiptClosure(ctx, toolsapplication.ValidateCitationV3ReceiptQuery{
		WorkspaceID: query.WorkspaceID, WorkflowRunID: query.WorkflowRunID, AnalysisRunID: query.AnalysisRunID,
		CandidateID: query.CandidateID, CandidateHash: query.CandidateHash,
	})
	if err != nil {
		return toolsapplication.ValidateCitationV3PublicationAuthority{}, err
	}
	if receipt.ID != query.ReceiptID || receipt.OutputHash != query.ReceiptHash {
		return toolsapplication.ValidateCitationV3PublicationAuthority{}, consistency(errors.New("validation publication receipt binding differs"))
	}
	return toolsapplication.ValidateCitationV3PublicationAuthority{OperationID: operationID, Receipt: receipt}, nil
}

func (repository *GORMWorkspaceAnalysisRepository) gormLoadValidateCitationV3ReceiptClosure(
	ctx context.Context,
	query toolsapplication.ValidateCitationV3ReceiptQuery,
) (foundation.ID, toolsdomain.ResultReceipt, error) {
	var operationID foundation.ID
	var result toolsdomain.ResultReceipt
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		candidate, err := repository.gormLoadWorkspaceAnalysisCandidateAuthority(callbackCtx, scope, query.WorkspaceID, query.WorkflowRunID, query.CandidateID)
		if err != nil {
			return err
		}
		if candidate.AnalysisRunID != query.AnalysisRunID || candidate.CandidateHash != query.CandidateHash {
			return consistency(errors.New("citation receipt candidate binding differs"))
		}
		search, reads, err := repository.gormLoadWorkspaceAnalysisEvidenceForReferences(
			callbackCtx, database, scope, query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID, candidate.CitationRefs,
		)
		if err != nil {
			return err
		}
		if len(reads) != len(candidate.CitationRefs) {
			return consistency(errors.New("citation evidence receipt count differs"))
		}
		for index, reference := range candidate.CitationRefs {
			evidence, evidenceErr := toolsdomain.ReadSourceV3ReceiptEvidenceForSearch(reads[index], search)
			if evidenceErr != nil || evidence.EvidenceRef != reference {
				if evidenceErr == nil {
					evidenceErr = errors.New("citation evidence receipt reference differs")
				}
				return consistency(evidenceErr)
			}
		}
		authority, receipt, err := repository.gormLoadWorkspaceAnalysisSuccessfulReceiptAuthority(
			callbackCtx, database, scope, query.WorkspaceID, query.WorkflowRunID,
			gormWorkspaceAnalysisOperationKey(query.AnalysisRunID, agentdomain.WorkspaceAnalysisOperationNodeValidateCitations, agentdomain.WorkspaceAnalysisOperationCitationValidation, 1),
			gormWorkspaceAnalysisCitationRef,
		)
		if err != nil {
			return err
		}
		if _, err := toolsdomain.ValidateCitationV3ReceiptResults(receipt, query.CandidateID, query.CandidateHash, candidate.CitationRefs); err != nil {
			return consistency(err)
		}
		operationID, result = authority.OperationID, receipt
		return nil
	})
	if err != nil {
		return "", toolsdomain.ResultReceipt{}, classifyGORMTools(ctx, err)
	}
	return operationID, result, nil
}

func (repository *GORMWorkspaceAnalysisRepository) gormLoadWorkspaceAnalysisEvidenceForReferences(
	ctx context.Context,
	database *gorm.DB,
	scope foundation.TransactionScope,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
	analysisRunID foundation.ID,
	references []string,
) (toolsdomain.ResultReceipt, []toolsdomain.ResultReceipt, error) {
	search, err := repository.gormLoadWorkspaceAnalysisSuccessfulReceipt(
		ctx, database, scope, workspaceID, workflowRunID,
		gormWorkspaceAnalysisOperationKey(analysisRunID, agentdomain.WorkspaceAnalysisOperationNodeRetrieveEvidence, agentdomain.WorkspaceAnalysisOperationKnowledgeSearch, 1),
		gormWorkspaceAnalysisSearchRef,
	)
	if err != nil {
		return toolsdomain.ResultReceipt{}, nil, err
	}
	reads := make([]toolsdomain.ResultReceipt, len(references))
	for index, reference := range references {
		if len(reference) != 2 || reference[0] != 'E' || reference[1] < '1' || reference[1] > '3' {
			return toolsdomain.ResultReceipt{}, nil, consistency(errors.New("citation evidence reference is invalid"))
		}
		ordinal := int(reference[1] - '0')
		reads[index], err = repository.gormLoadWorkspaceAnalysisSuccessfulReceipt(
			ctx, database, scope, workspaceID, workflowRunID,
			gormWorkspaceAnalysisOperationKey(analysisRunID, agentdomain.WorkspaceAnalysisOperationNodeReadEvidence, agentdomain.WorkspaceAnalysisOperationSourceRead, ordinal),
			gormWorkspaceAnalysisSourceRef,
		)
		if err != nil {
			return toolsdomain.ResultReceipt{}, nil, err
		}
	}
	return search, reads, nil
}

func (repository *GORMWorkspaceAnalysisRepository) gormLoadWorkspaceAnalysisRunAuthority(
	ctx context.Context,
	scope foundation.TransactionScope,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
) (agentapplication.WorkspaceAnalysisRunToolAuthority, error) {
	run, found, err := repository.authority.LoadWorkspaceAnalysisRunToolAuthorityScoped(
		ctx, scope, agentapplication.WorkspaceAnalysisRunToolAuthorityQuery{WorkspaceID: workspaceID, WorkflowRunID: workflowRunID},
	)
	if err != nil {
		return agentapplication.WorkspaceAnalysisRunToolAuthority{}, classifyGORMTools(ctx, err)
	}
	if !found {
		return agentapplication.WorkspaceAnalysisRunToolAuthority{}, receiptNotFound(errors.New("workspace analysis run authority was not found"))
	}
	if err := run.Validate(); err != nil || run.WorkspaceID != workspaceID || run.WorkflowRunID != workflowRunID {
		if err == nil {
			err = errors.New("workspace analysis run authority binding differs")
		}
		return agentapplication.WorkspaceAnalysisRunToolAuthority{}, consistency(err)
	}
	return run, nil
}

func (repository *GORMWorkspaceAnalysisRepository) gormLoadWorkspaceAnalysisCandidateAuthority(
	ctx context.Context,
	scope foundation.TransactionScope,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
	candidateID foundation.ID,
) (agentapplication.WorkspaceAnalysisToolCandidateAuthority, error) {
	candidate, found, err := repository.authority.LoadWorkspaceAnalysisToolCandidateAuthorityScoped(
		ctx, scope, agentapplication.WorkspaceAnalysisToolCandidateAuthorityQuery{WorkspaceID: workspaceID, WorkflowRunID: workflowRunID, CandidateID: candidateID},
	)
	if err != nil {
		return agentapplication.WorkspaceAnalysisToolCandidateAuthority{}, classifyGORMTools(ctx, err)
	}
	if !found {
		return agentapplication.WorkspaceAnalysisToolCandidateAuthority{}, receiptNotFound(errors.New("workspace analysis candidate authority was not found"))
	}
	if err := candidate.Validate(); err != nil || candidate.CandidateID != candidateID {
		if err == nil {
			err = errors.New("workspace analysis candidate authority binding differs")
		}
		return agentapplication.WorkspaceAnalysisToolCandidateAuthority{}, consistency(err)
	}
	return candidate, nil
}

func (repository *GORMWorkspaceAnalysisRepository) gormLoadWorkspaceAnalysisSuccessfulReceipt(
	ctx context.Context,
	database *gorm.DB,
	scope foundation.TransactionScope,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
	key agentdomain.WorkspaceAnalysisOperationKey,
	tool toolsdomain.ToolRef,
) (toolsdomain.ResultReceipt, error) {
	_, receipt, err := repository.gormLoadWorkspaceAnalysisSuccessfulReceiptAuthority(ctx, database, scope, workspaceID, workflowRunID, key, tool)
	return receipt, err
}

func (repository *GORMWorkspaceAnalysisRepository) gormLoadWorkspaceAnalysisSuccessfulReceiptAuthority(
	ctx context.Context,
	database *gorm.DB,
	scope foundation.TransactionScope,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
	key agentdomain.WorkspaceAnalysisOperationKey,
	tool toolsdomain.ToolRef,
) (agentapplication.WorkspaceAnalysisSuccessfulToolAuthority, toolsdomain.ResultReceipt, error) {
	authority, found, err := repository.authority.LoadWorkspaceAnalysisSuccessfulToolAuthorityScoped(
		ctx, scope, agentapplication.WorkspaceAnalysisSuccessfulToolAuthorityQuery{WorkspaceID: workspaceID, WorkflowRunID: workflowRunID, OperationKey: key},
	)
	if err != nil {
		return agentapplication.WorkspaceAnalysisSuccessfulToolAuthority{}, toolsdomain.ResultReceipt{}, classifyGORMTools(ctx, err)
	}
	if !found {
		return agentapplication.WorkspaceAnalysisSuccessfulToolAuthority{}, toolsdomain.ResultReceipt{}, receiptNotFound(errors.New("workspace analysis tool authority was not found"))
	}
	if err := authority.Validate(); err != nil || authority.AnalysisRunID != key.AnalysisRunID {
		if err == nil {
			err = errors.New("workspace analysis successful tool authority binding differs")
		}
		return agentapplication.WorkspaceAnalysisSuccessfulToolAuthority{}, toolsdomain.ResultReceipt{}, consistency(err)
	}
	call, err := scanGORMToolCall(gormToolsRawRow(database,
		`SELECT `+gormToolCallColumns+` FROM workflow.tool_call WHERE workspace_id=? AND id=?`, string(workspaceID), string(authority.ToolCallID)))
	if gormToolsNoRows(err) {
		return agentapplication.WorkspaceAnalysisSuccessfulToolAuthority{}, toolsdomain.ResultReceipt{}, receiptNotFound(err)
	}
	if err != nil {
		return agentapplication.WorkspaceAnalysisSuccessfulToolAuthority{}, toolsdomain.ResultReceipt{}, classifyGORMTools(ctx, err)
	}
	if call.WorkflowRunID != workflowRunID || call.Status != toolsdomain.CallSucceeded || call.Tool == nil || *call.Tool != tool {
		return agentapplication.WorkspaceAnalysisSuccessfulToolAuthority{}, toolsdomain.ResultReceipt{}, consistency(errors.New("workspace analysis authority call binding is invalid"))
	}
	definition, err := workspaceAnalysisBuiltinDefinition(tool)
	if err != nil {
		return agentapplication.WorkspaceAnalysisSuccessfulToolAuthority{}, toolsdomain.ResultReceipt{}, err
	}
	receipt, err := gormLoadWorkspaceAnalysisResultReceipt(ctx, database, authority.ResultID, workspaceID, authority.ToolCallID)
	if gormToolsNoRows(err) {
		return agentapplication.WorkspaceAnalysisSuccessfulToolAuthority{}, toolsdomain.ResultReceipt{}, receiptNotFound(err)
	}
	if err != nil {
		return agentapplication.WorkspaceAnalysisSuccessfulToolAuthority{}, toolsdomain.ResultReceipt{}, err
	}
	if receipt.OutputHash != authority.ResultHash || receipt.ID != authority.ResultID || receipt.ToolCallID != authority.ToolCallID {
		return agentapplication.WorkspaceAnalysisSuccessfulToolAuthority{}, toolsdomain.ResultReceipt{}, consistency(errors.New("workspace analysis authority receipt binding differs"))
	}
	if err := toolsdomain.ValidateResultReceipt(receipt, call, definition); err != nil {
		return agentapplication.WorkspaceAnalysisSuccessfulToolAuthority{}, toolsdomain.ResultReceipt{}, consistency(err)
	}
	return authority, receipt, nil
}

func gormLoadWorkspaceAnalysisResultReceipt(
	ctx context.Context,
	database *gorm.DB,
	receiptID foundation.ID,
	workspaceID foundation.ID,
	toolCallID foundation.ID,
) (toolsdomain.ResultReceipt, error) {
	row, err := gormToolsRawRow(database, `SELECT `+gormToolResultReceiptColumns+`
		FROM workflow.tool_result_receipt
		WHERE id=? AND workspace_id=? AND tool_call_id=?`, string(receiptID), string(workspaceID), string(toolCallID))
	if err != nil {
		return toolsdomain.ResultReceipt{}, classifyGORMTools(ctx, err)
	}
	receipt, scanErr := scanGORMWorkspaceAnalysisResultReceipt(row)
	if scanErr != nil {
		return toolsdomain.ResultReceipt{}, classifyGORMTools(ctx, scanErr)
	}
	return receipt, nil
}

func scanGORMWorkspaceAnalysisResultReceipt(row rowScanner) (toolsdomain.ResultReceipt, error) {
	var receipt toolsdomain.ResultReceipt
	var id, toolCallID, workspaceID, workflowRunID, nodeRunID, nodeAttemptID string
	var persistencePolicy string
	var bindingSchemaID, bindingHash *string
	var bindingSchemaVersion, bindingBytes *int64
	var outputDocument, bindingDocument gormToolsBytes
	if err := row.Scan(
		&id, &toolCallID, &workspaceID, &workflowRunID, &nodeRunID, &nodeAttemptID,
		&receipt.Tool.Name, &receipt.Tool.Version, &receipt.OutputSchema.ID, &receipt.OutputSchema.Version,
		&receipt.DefinitionHash, &persistencePolicy, &receipt.MaxOutputBytes, &receipt.MaxPrivateBindingBytes,
		&outputDocument, &receipt.OutputHash, &receipt.OutputBytes,
		&bindingSchemaID, &bindingSchemaVersion, &bindingDocument, &bindingHash, &bindingBytes, &receipt.CreatedAt,
	); err != nil {
		return toolsdomain.ResultReceipt{}, err
	}
	receipt.ID, receipt.ToolCallID, receipt.WorkspaceID = foundation.ID(id), foundation.ID(toolCallID), foundation.ID(workspaceID)
	receipt.WorkflowRunID, receipt.NodeRunID, receipt.NodeAttemptID = foundation.ID(workflowRunID), foundation.ID(nodeRunID), foundation.ID(nodeAttemptID)
	receipt.PersistencePolicy = toolsdomain.ResultPersistencePolicy(persistencePolicy)
	receipt.Output = append(json.RawMessage(nil), outputDocument...)
	receipt.CreatedAt = receipt.CreatedAt.UTC()
	hasBinding := bindingSchemaID != nil || bindingSchemaVersion != nil || len(bindingDocument) != 0 || bindingHash != nil || bindingBytes != nil
	if hasBinding {
		if bindingSchemaID == nil || bindingSchemaVersion == nil || len(bindingDocument) == 0 || bindingHash == nil || bindingBytes == nil {
			return toolsdomain.ResultReceipt{}, errors.New("result receipt private binding columns are incomplete")
		}
		receipt.PrivateBinding = &toolsdomain.ResultReceiptPrivateBinding{
			Schema: toolsdomain.SchemaRef{ID: *bindingSchemaID, Version: *bindingSchemaVersion}, Document: append(json.RawMessage(nil), bindingDocument...),
			Hash: *bindingHash, Bytes: *bindingBytes,
		}
	}
	return receipt, nil
}

func gormWorkspaceAnalysisOperationKey(
	analysisRunID foundation.ID,
	nodeKey agentdomain.WorkspaceAnalysisOperationNodeKey,
	kind agentdomain.WorkspaceAnalysisOperationKind,
	ordinal int,
) agentdomain.WorkspaceAnalysisOperationKey {
	return agentdomain.WorkspaceAnalysisOperationKey{AnalysisRunID: analysisRunID, NodeKey: nodeKey, Kind: kind, Ordinal: ordinal}
}

var _ toolsapplication.SearchKnowledgeV2ReceiptReader = (*GORMWorkspaceAnalysisRepository)(nil)
var _ toolsapplication.SearchKnowledgeV2PublicationAuthorityReader = (*GORMWorkspaceAnalysisRepository)(nil)
var _ toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityReader = (*GORMWorkspaceAnalysisRepository)(nil)
var _ toolsapplication.ValidateCitationV3AuthorityReader = (*GORMWorkspaceAnalysisRepository)(nil)
var _ toolsapplication.ValidateCitationV3ReceiptReader = (*GORMWorkspaceAnalysisRepository)(nil)
var _ toolsapplication.ValidateCitationV3PublicationAuthorityReader = (*GORMWorkspaceAnalysisRepository)(nil)
