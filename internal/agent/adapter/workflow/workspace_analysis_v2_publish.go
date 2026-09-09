package workflow

import (
	"context"
	"encoding/json"
	"errors"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func (executor *workspaceAnalysisV2NodeExecutor) validateCitations(ctx context.Context, state workspaceAnalysisV2Execution) (workflowapplication.ExecutionResult, error) {
	candidate, body, err := executor.loadCandidate(ctx, state, true)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	evidence, err := executor.loadEvidence(ctx, state)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if !workspaceAnalysisV2ReferencesSubset(body.Payload.CitationRefs, evidence.refs) {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 candidate cites unopened or truncated evidence"))
	}
	key := agentdomain.WorkspaceAnalysisOperationKey{AnalysisRunID: state.run.ID, NodeKey: agentdomain.WorkspaceAnalysisOperationNodeValidateCitations,
		Kind: agentdomain.WorkspaceAnalysisOperationCitationValidation, Ordinal: 1}
	result, err := executor.executeTool(ctx, state, key, toolsdomain.ToolRef{Name: "ValidateCitation", Version: 4}, map[string]any{
		"candidate_id": candidate.ID, "candidate_hash": candidate.DocumentHash, "evidence_refs": body.Payload.CitationRefs,
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	output := workspaceAnalysisValidationOutputV2{SchemaVersion: 2, CandidateID: candidate.ID, CandidateHash: candidate.DocumentHash,
		OperationID: result.OperationID, ToolCallID: result.Call.ID, ReceiptID: result.ResultReceiptID, ReceiptHash: result.Call.ResponseHash}
	if _, _, err := executor.loadValidation(ctx, state, candidate, body.Payload.CitationRefs, output); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if _, err := decodeWorkspaceAnalysisValidationOutputV2(raw); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workflowapplication.ExecutionResult{Output: raw}, nil
}

func (executor *workspaceAnalysisV2NodeExecutor) loadValidation(ctx context.Context, state workspaceAnalysisV2Execution, candidate agentdomain.WorkspaceAnalysisCandidate, refs []string, output workspaceAnalysisValidationOutputV2) (toolsapplication.ValidateCitationV4PublicationAuthority, []toolsdomain.ValidateCitationV3ReceiptResult, error) {
	if output.CandidateID != candidate.ID || output.CandidateHash != candidate.DocumentHash {
		return toolsapplication.ValidateCitationV4PublicationAuthority{}, nil, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 validation subject drifted"))
	}
	authority, err := executor.dependencies.Validation.LoadValidateCitationV4PublicationAuthority(ctx, toolsapplication.ValidateCitationV4PublicationAuthorityQuery{
		WorkspaceID: state.execution.WorkspaceID, WorkflowRunID: state.execution.RunID, AnalysisRunID: state.run.ID, CandidateID: candidate.ID, CandidateHash: candidate.DocumentHash,
		ReceiptID: output.ReceiptID, ReceiptHash: output.ReceiptHash,
	})
	if err != nil {
		return authority, nil, err
	}
	receipt := authority.Receipt
	if authority.OperationID != output.OperationID || receipt.ID != output.ReceiptID || receipt.ToolCallID != output.ToolCallID || receipt.OutputHash != output.ReceiptHash ||
		receipt.WorkspaceID != state.execution.WorkspaceID || receipt.WorkflowRunID != state.execution.RunID {
		return authority, nil, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 citation authority drifted"))
	}
	results, err := toolsdomain.ValidateCitationV4ReceiptResults(receipt, candidate.ID, candidate.DocumentHash, refs)
	return authority, results, err
}

func (executor *workspaceAnalysisV2NodeExecutor) reviewPublish(ctx context.Context, state workspaceAnalysisV2Execution) (workflowapplication.ExecutionResult, error) {
	candidate, body, err := executor.loadCandidate(ctx, state, false)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	raw, err := executor.loadStage(ctx, state, conversationworkflow.WorkspaceAnalysisNodeValidateCitations, true)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	validationOutput, err := decodeWorkspaceAnalysisValidationOutputV2(raw)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	validation, results, err := executor.loadValidation(ctx, state, candidate, body.Payload.CitationRefs, validationOutput)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if workspaceAnalysisValidationRejected(results) {
		return executor.publishV2Rejection(ctx, state, agentdomain.WorkspaceAnalysisRunCitationInvalid, validation.OperationID,
			conversationapplication.WorkspaceAnalysisTerminationArtifact{Kind: conversationapplication.WorkspaceAnalysisTerminationArtifactToolReceipt, ID: validation.Receipt.ID, Hash: validation.Receipt.OutputHash})
	}
	evidence, err := executor.loadEvidence(ctx, state)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if !workspaceAnalysisV2ReferencesSubset(body.Payload.CitationRefs, evidence.refs) {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 review evidence is incomplete"))
	}
	byRef := make(map[string]string, len(evidence.items))
	for _, item := range evidence.items {
		byRef[item.EvidenceRef] = item.Excerpt
	}
	reviewEvidence := make([]agentapplication.WorkspaceAnalysisReviewEvidence, len(body.Payload.CitationRefs))
	for index, ref := range body.Payload.CitationRefs {
		reviewEvidence[index] = agentapplication.WorkspaceAnalysisReviewEvidence{EvidenceRef: ref, Excerpt: byRef[ref]}
	}
	review, err := executor.dependencies.Review.Run(ctx, agentapplication.WorkspaceAnalysisReviewRequest{
		Identity: workspaceAnalysisModelIdentity(state.execution), AnalysisRunID: state.run.ID, Candidate: candidate, Evidence: reviewEvidence,
		ModelSettingsRevision: cloneWorkspaceAnalysisInt64(state.execution.ModelSettingsRevision), Retrieval: evidence.retrieval, ProfileRef: DefaultProfileRef(), PromptRef: FaithfulnessReviewPromptRef(),
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if err := validateWorkspaceAnalysisReviewResult(state.execution, state.run, candidate, review); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if !review.Passed {
		return executor.publishV2Rejection(ctx, state, agentdomain.WorkspaceAnalysisRunFaithfulnessRejected, review.ModelResult.OperationID,
			conversationapplication.WorkspaceAnalysisTerminationArtifact{Kind: conversationapplication.WorkspaceAnalysisTerminationArtifactModelResult, ID: review.ModelResult.ID, Hash: review.ModelResult.DocumentHash})
	}
	command := conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand{WorkspaceAnalysisPublicationLookup: state.lookup(), ExpectedAnswerVersion: state.question.Answer.Version,
		CandidateID: candidate.ID, CandidateHash: candidate.DocumentHash, ValidationReceiptID: validation.Receipt.ID, ValidationReceiptHash: validation.Receipt.OutputHash,
		ReviewModelResultID: review.ModelResult.ID, ReviewModelResultHash: review.ModelResult.DocumentHash}
	for _, entry := range state.journal.Entries {
		op := entry.Operation
		if op.NodeKey == agentdomain.WorkspaceAnalysisOperationNodeDecideNext && op.Kind == agentdomain.WorkspaceAnalysisOperationGitStatus &&
			op.Status == agentdomain.WorkspaceAnalysisOperationSucceeded && op.Result != nil {
			command.GitReceiptID, command.GitReceiptHash = op.Result.ID, op.Result.Hash
		}
	}
	publication, err := finalizeWorkspaceAnalysisSuccessPublication(ctx, executor.dependencies.Finalizer, command)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workspaceAnalysisV2PublicationResult(state.root.AnswerID, publication)
}

func (executor *workspaceAnalysisV2NodeExecutor) publishV2Rejection(ctx context.Context, state workspaceAnalysisV2Execution, reason agentdomain.WorkspaceAnalysisRunTerminationReason, operationID foundation.ID, artifact conversationapplication.WorkspaceAnalysisTerminationArtifact) (workflowapplication.ExecutionResult, error) {
	publication, err := finalizeWorkspaceAnalysisTerminationPublication(ctx, executor.dependencies.Finalizer, conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: state.lookup(), ExpectedAnswerVersion: state.question.Answer.Version, Reason: reason, OperationID: &operationID, Artifact: &artifact})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workspaceAnalysisV2PublicationResult(state.root.AnswerID, publication)
}

func workspaceAnalysisV2PublicationResult(answerID foundation.ID, publication conversationworkflow.WorkspaceAnalysisPublicationOutput) (workflowapplication.ExecutionResult, error) {
	if publication.SchemaVersion != 2 {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 publication schema drifted"))
	}
	return workspaceAnalysisPublicationExecutionResult(answerID, publication)
}
