package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const workspaceAnalysisValidateCitationsReason = "validate candidate citations"

var (
	workspaceAnalysisValidateCitationRef          = toolsdomain.ToolRef{Name: "ValidateCitation", Version: 3}
	workspaceAnalysisValidateCitationInputSchema  = toolsdomain.SchemaRef{ID: "tool.validate_citation.input", Version: 2}
	workspaceAnalysisValidateCitationOutputSchema = toolsdomain.SchemaRef{ID: "tool.validate_citation.output", Version: 2}
)

// WorkspaceAnalysisValidateCitationsExecutorDependencies 是 validate_citations 的全部权威读取与 Tool 边界。
type WorkspaceAnalysisValidateCitationsExecutorDependencies struct {
	Context   conversationapplication.QuestionExecutionContextLoader
	Runs      agentapplication.WorkspaceAnalysisRunLoader
	Inputs    workspaceAnalysisRunInputReader
	Stages    workspaceAnalysisStageOutputReader
	Authority toolsapplication.ValidateCitationV3AuthorityReader
	Receipts  toolsapplication.ValidateCitationV3ReceiptReader
	Tools     workspaceAnalysisToolExecutor
	Finalizer conversationapplication.WorkspaceAnalysisFinalizer
	Clock     foundation.Clock
}

// WorkspaceAnalysisValidateCitationsExecutor 只校验 Candidate 已冻结的 1-3 个短引用。
type WorkspaceAnalysisValidateCitationsExecutor struct {
	dependencies WorkspaceAnalysisValidateCitationsExecutorDependencies
	clock        foundation.Clock
}

// NewWorkspaceAnalysisValidateCitationsExecutor 创建 Workspace Analysis Citation 校验节点。
func NewWorkspaceAnalysisValidateCitationsExecutor(
	dependencies WorkspaceAnalysisValidateCitationsExecutorDependencies,
) (*WorkspaceAnalysisValidateCitationsExecutor, error) {
	if nilDependency(dependencies.Context) || nilDependency(dependencies.Runs) || nilDependency(dependencies.Inputs) ||
		nilDependency(dependencies.Stages) || nilDependency(dependencies.Authority) || nilDependency(dependencies.Receipts) ||
		nilDependency(dependencies.Tools) || nilDependency(dependencies.Finalizer) {
		return nil, workflowError(
			foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("workspace analysis citation validation dependencies are unavailable"),
		)
	}
	clock := dependencies.Clock
	if nilDependency(clock) {
		clock = foundation.SystemClock{}
	}
	return &WorkspaceAnalysisValidateCitationsExecutor{dependencies: dependencies, clock: clock}, nil
}

// Execute 在 Tool 调用前后都从持久事实重建候选与 Citation receipt 闭包。
func (executor *WorkspaceAnalysisValidateCitationsExecutor) Execute(
	ctx context.Context,
	execution workflowapplication.ExecutionContext,
) (workflowapplication.ExecutionResult, error) {
	if executor == nil || nilDependency(executor.dependencies.Context) || nilDependency(executor.dependencies.Runs) ||
		nilDependency(executor.dependencies.Inputs) || nilDependency(executor.dependencies.Stages) ||
		nilDependency(executor.dependencies.Authority) || nilDependency(executor.dependencies.Receipts) ||
		nilDependency(executor.dependencies.Tools) || nilDependency(executor.dependencies.Finalizer) {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("workspace analysis citation validation executor is unavailable"),
		)
	}
	if ctx == nil {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false,
			errors.New("workspace analysis citation validation context is nil"),
		)
	}
	if err := validateWorkspaceAnalysisCitationExecution(execution); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	nodeStartedAt, err := workspaceAnalysisExecutionStartedAt(executor.clock)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}

	predecessor, err := conversationworkflow.DecodeWorkspaceAnalysisSynthesisOutput(execution.Input)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	authoritativeRaw, err := executor.dependencies.Stages.GetSucceededNodeOutput(
		ctx, execution.WorkspaceID, execution.RunID, conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer,
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if !bytes.Equal(execution.Input, authoritativeRaw) {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(
			errors.New("workspace analysis synthesis predecessor differs from its succeeded output"),
		)
	}
	authoritativePredecessor, err := conversationworkflow.DecodeWorkspaceAnalysisSynthesisOutput(authoritativeRaw)
	if err != nil || !reflect.DeepEqual(predecessor, authoritativePredecessor) {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(
			workspaceAnalysisSynthesisCause(err, "workspace analysis synthesis predecessor is invalid"),
		)
	}

	rootRaw, err := executor.dependencies.Inputs.GetRunInput(ctx, execution.WorkspaceID, execution.RunID)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	root, err := conversationworkflow.DecodeWorkspaceAnalysisInput(rootRaw)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	questionContext, err := executor.dependencies.Context.LoadQuestionExecutionContext(
		ctx,
		conversationapplication.QuestionExecutionContextQuery{
			WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
			ConversationID: root.ConversationID, QuestionID: root.QuestionID, AnswerID: root.AnswerID,
			QuestionOrdinal: root.QuestionOrdinal, ContextHash: root.ContextHash,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if err := validateWorkspaceAnalysisQuestionContext(questionContext, execution, root); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	analysisRun, err := executor.dependencies.Runs.LoadWorkspaceAnalysisRunForExecution(
		ctx,
		agentapplication.WorkspaceAnalysisRunExecutionQuery{
			WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
			ConversationID: root.ConversationID, QuestionID: root.QuestionID, AnswerID: root.AnswerID,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if err := validateWorkspaceAnalysisSynthesisRun(analysisRun, execution, root); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV1Deadlines(analysisRun.Timeouts)
	if err != nil {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false, err,
		)
	}
	nodeContext, cancel, err := workspaceAnalysisNodeContext(
		ctx, nodeStartedAt, analysisRun.DeadlineAt, deadlines.ValidateCitationsDeadline(),
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, finalizeWorkspaceAnalysisPreAuthorizationDeadlineError(
			ctx, executor.dependencies.Finalizer, execution, root,
			questionContext.Answer.Version, analysisRun.ID, err,
		)
	}
	defer cancel()

	authority, err := executor.dependencies.Authority.LoadValidateCitationV3Authority(
		nodeContext,
		toolsapplication.ValidateCitationV3AuthorityQuery{
			WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, CandidateID: predecessor.CandidateID,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if err := validateWorkspaceAnalysisCitationAuthority(execution, analysisRun, predecessor, authority); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	arguments, err := json.Marshal(struct {
		CandidateID   foundation.ID `json:"candidate_id"`
		CandidateHash string        `json:"candidate_hash"`
		EvidenceRefs  []string      `json:"evidence_refs"`
	}{
		CandidateID: predecessor.CandidateID, CandidateHash: predecessor.CandidateHash,
		EvidenceRefs: append([]string{}, authority.EvidenceRefs...),
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorNonRetryableFailure, ErrorCodeInputInvalid, false, err,
		)
	}
	toolResult, err := executor.dependencies.Tools.ExecuteWorkspaceAnalysisTool(
		nodeContext,
		toolsapplication.ExecuteWorkspaceAnalysisToolCommand{
			OperationKey: agentdomain.WorkspaceAnalysisOperationKey{
				AnalysisRunID: analysisRun.ID,
				NodeKey:       agentdomain.WorkspaceAnalysisOperationNodeValidateCitations,
				Kind:          agentdomain.WorkspaceAnalysisOperationCitationValidation,
				Ordinal:       1,
			},
			Tool: toolsapplication.ExecuteToolCommand{
				Identity: workspaceAnalysisToolIdentity(execution), Invocation: toolsdomain.InvocationSourceTrustedWorkflow,
				CallNo: 1,
				Request: toolsdomain.ToolRequestV1{
					SchemaVersion: toolsdomain.ToolRequestSchemaVersionV1,
					ToolName:      workspaceAnalysisValidateCitationRef.Name,
					Arguments:     arguments,
					Reason:        workspaceAnalysisValidateCitationsReason,
				},
			},
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, finalizeWorkspaceAnalysisToolTerminalError(
			nodeContext, executor.dependencies.Finalizer, execution, root,
			questionContext.Answer.Version, analysisRun.ID, err,
		)
	}
	persistedReceipt, err := executor.dependencies.Receipts.LoadValidateCitationV3Receipt(
		nodeContext,
		toolsapplication.ValidateCitationV3ReceiptQuery{
			WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, AnalysisRunID: analysisRun.ID,
			CandidateID: predecessor.CandidateID, CandidateHash: predecessor.CandidateHash,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	results, err := validateWorkspaceAnalysisCitationToolResult(
		execution, predecessor, authority.EvidenceRefs, arguments, toolResult, persistedReceipt,
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	output, err := conversationworkflow.EncodeWorkspaceAnalysisValidationOutput(
		conversationworkflow.WorkspaceAnalysisValidationOutput{
			SchemaVersion: conversationworkflow.WorkspaceAnalysisOutputSchemaVersion,
			CandidateID:   predecessor.CandidateID, CandidateHash: predecessor.CandidateHash,
			ValidationToolCallID: toolResult.Call.ID, ValidationToolReceiptID: persistedReceipt.ID,
			ValidationToolReceiptHash: persistedReceipt.OutputHash, Results: results,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workflowapplication.ExecutionResult{Output: output}, nil
}

func validateWorkspaceAnalysisCitationExecution(execution workflowapplication.ExecutionContext) error {
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	node, found := workspaceAnalysisNode(definition, conversationworkflow.WorkspaceAnalysisNodeValidateCitations)
	identity := workspaceAnalysisToolIdentity(execution)
	if !found || execution.DefinitionVersion != definition.Version || execution.DefinitionHash != definition.GraphHash ||
		execution.NodeKey != node.Key || execution.NodeKind != node.Kind || execution.InputSchemaVersion != node.InputSchemaVersion ||
		!validExecutionID(execution.WorkspaceID) || !validExecutionID(execution.DefinitionID) || !validExecutionID(execution.RunID) ||
		!validExecutionID(execution.NodeRunID) || !validExecutionID(execution.NodeAttemptID) || execution.NodeVersion < 1 ||
		execution.AttemptNo < 1 || execution.DispatchNo < 1 || execution.RetryNo < 0 || strings.TrimSpace(execution.LeaseOwner) == "" ||
		(execution.ModelSettingsRevision != nil && *execution.ModelSettingsRevision < 0) || identity.Validate() != nil {
		return workflowError(
			foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false,
			errors.New("workspace analysis citation validation execution binding is invalid"),
		)
	}
	return nil
}

func validateWorkspaceAnalysisCitationAuthority(
	execution workflowapplication.ExecutionContext,
	run agentdomain.WorkspaceAnalysisRun,
	predecessor conversationworkflow.WorkspaceAnalysisSynthesisOutput,
	authority toolsapplication.ValidateCitationV3Authority,
) error {
	if authority.WorkspaceID != execution.WorkspaceID || authority.WorkflowRunID != execution.RunID ||
		authority.AnalysisRunID != run.ID || authority.CandidateID != predecessor.CandidateID ||
		authority.CandidateHash != predecessor.CandidateHash || len(authority.EvidenceRefs) < 1 ||
		len(authority.EvidenceRefs) > agentdomain.WorkspaceAnalysisV1MaxSourceReads ||
		len(authority.ReadSourceReceipts) != len(authority.EvidenceRefs) ||
		authority.SearchReceipt.WorkspaceID != execution.WorkspaceID ||
		authority.SearchReceipt.WorkflowRunID != execution.RunID {
		return workspaceAnalysisReceiptError(errors.New("workspace analysis citation authority binding is invalid"))
	}
	selected, err := toolsdomain.SearchKnowledgeV2ReceiptSelectedRefs(authority.SearchReceipt)
	if err != nil {
		return workspaceAnalysisReceiptError(err)
	}
	selectedSet := make(map[string]struct{}, len(selected))
	for _, reference := range selected {
		selectedSet[reference] = struct{}{}
	}
	seenRefs := make(map[string]struct{}, len(authority.EvidenceRefs))
	seenIDs := map[foundation.ID]struct{}{
		predecessor.CandidateID: {}, predecessor.SynthesisModelRunID: {}, predecessor.SynthesisModelCallID: {},
		predecessor.DraftSessionID: {}, authority.SearchReceipt.ID: {}, authority.SearchReceipt.ToolCallID: {},
	}
	if len(seenIDs) != 6 {
		return workspaceAnalysisReceiptError(errors.New("workspace analysis citation authority reuses a predecessor identity"))
	}
	for index, reference := range authority.EvidenceRefs {
		if _, selectedReference := selectedSet[reference]; !selectedReference {
			return workspaceAnalysisReceiptError(errors.New("workspace analysis candidate references unselected evidence"))
		}
		if _, duplicate := seenRefs[reference]; duplicate {
			return workspaceAnalysisReceiptError(errors.New("workspace analysis candidate reference is duplicated"))
		}
		seenRefs[reference] = struct{}{}
		receipt := authority.ReadSourceReceipts[index]
		if receipt.WorkspaceID != execution.WorkspaceID || receipt.WorkflowRunID != execution.RunID ||
			receipt.ID == receipt.ToolCallID {
			return workspaceAnalysisReceiptError(errors.New("workspace analysis citation source receipt binding is invalid"))
		}
		for _, id := range []foundation.ID{receipt.ID, receipt.ToolCallID} {
			if _, duplicate := seenIDs[id]; duplicate {
				return workspaceAnalysisReceiptError(errors.New("workspace analysis citation authority identity is reused"))
			}
			seenIDs[id] = struct{}{}
		}
		evidence, evidenceErr := toolsdomain.ReadSourceV3ReceiptEvidenceForSearch(receipt, authority.SearchReceipt)
		if evidenceErr != nil || evidence.EvidenceRef != reference {
			return workspaceAnalysisReceiptError(
				workspaceAnalysisSynthesisCause(evidenceErr, "workspace analysis citation source receipt closure is invalid"),
			)
		}
	}
	return nil
}

func validateWorkspaceAnalysisCitationToolResult(
	execution workflowapplication.ExecutionContext,
	predecessor conversationworkflow.WorkspaceAnalysisSynthesisOutput,
	evidenceRefs []string,
	arguments json.RawMessage,
	result toolsapplication.ToolExecutionResult,
	receipt toolsdomain.ResultReceipt,
) ([]conversationworkflow.WorkspaceAnalysisCitationValidationResult, error) {
	call := result.Call
	requestDocument, requestErr := workspaceAnalysisValidateCitationRequestDocument(arguments)
	requestHash := sha256.Sum256(requestDocument)
	responseHash := sha256.Sum256(result.Output)
	validAttempt := result.Replayed || call.NodeAttemptID == execution.NodeAttemptID
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(workspaceAnalysisValidateCitationRef)
	if err := toolsdomain.ValidateToolCall(call); err != nil || requestErr != nil || !found || !validAttempt ||
		call.Status != toolsdomain.CallSucceeded || call.WorkspaceID != execution.WorkspaceID ||
		call.WorkflowRunID != execution.RunID || call.NodeRunID != execution.NodeRunID || call.CallNo != 1 ||
		call.RequestedToolName != workspaceAnalysisValidateCitationRef.Name || call.Tool == nil ||
		*call.Tool != workspaceAnalysisValidateCitationRef || call.InputSchema == nil ||
		*call.InputSchema != workspaceAnalysisValidateCitationInputSchema || call.OutputSchema == nil ||
		*call.OutputSchema != workspaceAnalysisValidateCitationOutputSchema || call.Capability != capability.ReadLocal ||
		call.SideEffectLevel != toolsdomain.SideEffectNone || call.InvocationPolicy != toolsdomain.InvocationTrustedWorkflowOnly ||
		call.IdempotencyKey != "" || call.RequestHash != hex.EncodeToString(requestHash[:]) ||
		call.RequestBytes != int64(len(requestDocument)) || !result.UntrustedData ||
		call.ResponseBytes != int64(len(result.Output)) || call.ResponseHash != hex.EncodeToString(responseHash[:]) ||
		!validExecutionID(result.ResultReceiptID) || result.ResultReceiptID == call.ID ||
		receipt.ID != result.ResultReceiptID || receipt.ToolCallID != call.ID || receipt.WorkspaceID != execution.WorkspaceID ||
		receipt.WorkflowRunID != execution.RunID || receipt.NodeRunID != call.NodeRunID ||
		receipt.NodeAttemptID != call.NodeAttemptID || receipt.Tool != workspaceAnalysisValidateCitationRef ||
		receipt.OutputSchema != contract.OutputSchema || receipt.DefinitionHash != call.DefinitionHash ||
		receipt.PersistencePolicy != toolsdomain.ResultPersistenceCanonical || receipt.MaxOutputBytes != contract.MaxOutputBytes ||
		receipt.MaxPrivateBindingBytes != contract.MaxPrivateBindingBytes || !bytes.Equal(receipt.Output, result.Output) ||
		receipt.OutputHash != call.ResponseHash || receipt.OutputBytes != call.ResponseBytes {
		if err == nil {
			err = errors.New("workspace analysis citation validation result binding is invalid")
		}
		return nil, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, err)
	}
	for _, id := range []foundation.ID{
		call.ID, receipt.ID, predecessor.CandidateID, predecessor.SynthesisModelRunID,
		predecessor.SynthesisModelCallID, predecessor.DraftSessionID,
	} {
		if !validExecutionID(id) {
			return nil, workflowError(
				foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false,
				errors.New("workspace analysis citation validation result identity is invalid"),
			)
		}
	}
	if workspaceAnalysisValidationResultReusesPredecessorID(call.ID, receipt.ID, predecessor) {
		return nil, workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false,
			errors.New("workspace analysis citation validation result reuses a predecessor identity"),
		)
	}
	domainResults, err := toolsdomain.ValidateCitationV3ReceiptResults(
		receipt, predecessor.CandidateID, predecessor.CandidateHash, evidenceRefs,
	)
	if err != nil {
		return nil, workspaceAnalysisReceiptError(err)
	}
	results := make([]conversationworkflow.WorkspaceAnalysisCitationValidationResult, len(domainResults))
	for index, domainResult := range domainResults {
		results[index] = conversationworkflow.WorkspaceAnalysisCitationValidationResult{
			EvidenceRef: domainResult.EvidenceRef, Valid: domainResult.Valid, ReasonCode: domainResult.ReasonCode,
		}
	}
	return results, nil
}

func workspaceAnalysisValidationResultReusesPredecessorID(
	callID foundation.ID,
	receiptID foundation.ID,
	predecessor conversationworkflow.WorkspaceAnalysisSynthesisOutput,
) bool {
	seen := make(map[foundation.ID]struct{}, 6)
	for _, id := range []foundation.ID{
		predecessor.CandidateID, predecessor.SynthesisModelRunID, predecessor.SynthesisModelCallID,
		predecessor.DraftSessionID, callID, receiptID,
	} {
		if _, duplicate := seen[id]; duplicate {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}

func workspaceAnalysisValidateCitationRequestDocument(arguments json.RawMessage) ([]byte, error) {
	request := struct {
		SchemaVersion int                   `json:"schema_version"`
		ToolName      string                `json:"tool_name"`
		ToolVersion   int64                 `json:"tool_version"`
		InputSchema   toolsdomain.SchemaRef `json:"input_schema"`
		Arguments     json.RawMessage       `json:"arguments"`
		Reason        string                `json:"reason"`
	}{
		SchemaVersion: toolsdomain.ToolRequestSchemaVersionV1,
		ToolName:      workspaceAnalysisValidateCitationRef.Name,
		ToolVersion:   workspaceAnalysisValidateCitationRef.Version,
		InputSchema:   workspaceAnalysisValidateCitationInputSchema,
		Arguments:     append(json.RawMessage(nil), arguments...),
		Reason:        workspaceAnalysisValidateCitationsReason,
	}
	return json.Marshal(request)
}

var _ workflowapplication.Executor = (*WorkspaceAnalysisValidateCitationsExecutor)(nil)
