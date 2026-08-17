package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const (
	workspaceAnalysisReadEvidenceReason    = "read selected workspace evidence"
	workspaceAnalysisMaxReadSourceExcerpt  = 4 * 1024
	workspaceAnalysisMaxReadSourceDocument = int(toolsdomain.ReadSourceV3ReceiptMaxOutputBytes)
)

var (
	workspaceAnalysisReadSourceRef          = toolsdomain.ToolRef{Name: "ReadSource", Version: 3}
	workspaceAnalysisReadSourceInputSchema  = toolsdomain.SchemaRef{ID: "tool.read_source.input", Version: 2}
	workspaceAnalysisReadSourceOutputSchema = toolsdomain.SchemaRef{ID: "tool.read_source.output", Version: 2}
)

// WorkspaceAnalysisReadEvidenceExecutorDependencies 是 read_evidence 的全部权威读取与 Tool 执行边界。
type WorkspaceAnalysisReadEvidenceExecutorDependencies struct {
	Context   conversationapplication.QuestionExecutionContextLoader
	Runs      agentapplication.WorkspaceAnalysisRunLoader
	Inputs    workspaceAnalysisRunInputReader
	Stages    workspaceAnalysisStageOutputReader
	Receipts  toolsapplication.SearchKnowledgeV2PublicationAuthorityReader
	Tools     workspaceAnalysisToolExecutor
	Finalizer conversationapplication.WorkspaceAnalysisFinalizer
	Clock     foundation.Clock
}

// WorkspaceAnalysisReadEvidenceExecutor 按 Search receipt 冻结的顺序读取一至三个 Source。
type WorkspaceAnalysisReadEvidenceExecutor struct {
	dependencies WorkspaceAnalysisReadEvidenceExecutorDependencies
	clock        foundation.Clock
}

// NewWorkspaceAnalysisReadEvidenceExecutor 创建只接受 workspace-analysis@1 read_evidence 节点的 Executor。
func NewWorkspaceAnalysisReadEvidenceExecutor(
	dependencies WorkspaceAnalysisReadEvidenceExecutorDependencies,
) (*WorkspaceAnalysisReadEvidenceExecutor, error) {
	if nilDependency(dependencies.Context) || nilDependency(dependencies.Runs) || nilDependency(dependencies.Inputs) ||
		nilDependency(dependencies.Stages) || nilDependency(dependencies.Receipts) || nilDependency(dependencies.Tools) ||
		nilDependency(dependencies.Finalizer) {
		return nil, workflowError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeCapabilityUnavailable,
			false,
			errors.New("workspace analysis read evidence dependencies are unavailable"),
		)
	}
	clock := dependencies.Clock
	if nilDependency(clock) {
		clock = foundation.SystemClock{}
	}
	return &WorkspaceAnalysisReadEvidenceExecutor{dependencies: dependencies, clock: clock}, nil
}

// Execute 仅从权威 retrieve 输出和同 Run Search receipt 派生 ReadSource@3 调用。
func (executor *WorkspaceAnalysisReadEvidenceExecutor) Execute(
	ctx context.Context,
	execution workflowapplication.ExecutionContext,
) (workflowapplication.ExecutionResult, error) {
	if executor == nil || nilDependency(executor.dependencies.Context) || nilDependency(executor.dependencies.Runs) ||
		nilDependency(executor.dependencies.Inputs) || nilDependency(executor.dependencies.Stages) ||
		nilDependency(executor.dependencies.Receipts) || nilDependency(executor.dependencies.Tools) ||
		nilDependency(executor.dependencies.Finalizer) {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeCapabilityUnavailable,
			false,
			errors.New("workspace analysis read evidence executor is unavailable"),
		)
	}
	if ctx == nil {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("workspace analysis read evidence context is nil"),
		)
	}
	if err := validateWorkspaceAnalysisReadEvidenceExecution(execution); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	nodeStartedAt, err := workspaceAnalysisExecutionStartedAt(executor.clock)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}

	predecessor, err := conversationworkflow.DecodeWorkspaceAnalysisRetrieveEvidenceOutput(execution.Input)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	authoritativeRaw, err := executor.dependencies.Stages.GetSucceededNodeOutput(
		ctx, execution.WorkspaceID, execution.RunID, conversationworkflow.WorkspaceAnalysisNodeRetrieveEvidence,
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	_, err = conversationworkflow.DecodeWorkspaceAnalysisRetrieveEvidenceOutput(authoritativeRaw)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if !bytes.Equal(execution.Input, authoritativeRaw) {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(
			errors.New("workspace analysis retrieve predecessor differs from its succeeded output"),
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
	if err := validateWorkspaceAnalysisRetrieveRun(analysisRun, execution, root); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV1Deadlines(analysisRun.Timeouts)
	if err != nil {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false, err,
		)
	}
	nodeContext, cancel, err := workspaceAnalysisNodeContext(
		ctx, nodeStartedAt, analysisRun.DeadlineAt, deadlines.ReadEvidenceDeadline(),
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, finalizeWorkspaceAnalysisPreAuthorizationDeadlineError(
			ctx, executor.dependencies.Finalizer, execution, root,
			questionContext.Answer.Version, analysisRun.ID, err,
		)
	}
	defer cancel()

	searchAuthority, err := executor.dependencies.Receipts.LoadSearchKnowledgeV2PublicationAuthority(
		nodeContext,
		toolsapplication.SearchKnowledgeV2PublicationAuthorityQuery{
			WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, AnalysisRunID: analysisRun.ID,
			ReceiptID: predecessor.SearchToolReceiptID, ReceiptHash: predecessor.SearchToolReceiptHash,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	searchReceipt := searchAuthority.Receipt
	searchSummary, err := validateWorkspaceAnalysisSearchReceipt(predecessor, searchReceipt, execution)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if searchSummary.HitCount == 0 {
		operationID := searchAuthority.OperationID
		_, finalizeErr := finalizeWorkspaceAnalysisTerminationPublication(
			nodeContext,
			executor.dependencies.Finalizer,
			conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
				WorkspaceAnalysisPublicationLookup: workspaceAnalysisPublicationLookup(execution, root, analysisRun.ID),
				ExpectedAnswerVersion:              questionContext.Answer.Version,
				Reason:                             agentdomain.WorkspaceAnalysisRunEvidenceInsufficient,
				OperationID:                        &operationID,
				Artifact: &conversationapplication.WorkspaceAnalysisTerminationArtifact{
					Kind: conversationapplication.WorkspaceAnalysisTerminationArtifactToolReceipt,
					ID:   searchReceipt.ID, Hash: searchReceipt.OutputHash,
				},
			},
		)
		if finalizeErr != nil {
			return workflowapplication.ExecutionResult{}, finalizeErr
		}
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorNonRetryableFailure,
			string(conversationdomain.WorkspaceAnalysisEvidenceInsufficient),
			false,
			errors.New("workspace analysis search returned no eligible evidence"),
		)
	}

	readCount := min(searchSummary.HitCount, agentdomain.WorkspaceAnalysisV1MaxSourceReads)
	reads := make([]conversationworkflow.WorkspaceAnalysisReadEvidenceReceipt, 0, readCount)
	seenResultIDs := map[foundation.ID]struct{}{
		predecessor.GitToolCallID: {}, predecessor.GitToolReceiptID: {},
		predecessor.PlannerModelRunID: {}, predecessor.PlannerModelCallID: {}, predecessor.PlannerModelResultID: {},
		predecessor.SearchToolCallID: {}, predecessor.SearchToolReceiptID: {},
	}
	for ordinal := 1; ordinal <= readCount; ordinal++ {
		evidenceRef := workspaceAnalysisReadEvidenceRef(ordinal)
		arguments, err := json.Marshal(struct {
			EvidenceRef string `json:"evidence_ref"`
		}{EvidenceRef: evidenceRef})
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
					NodeKey:       agentdomain.WorkspaceAnalysisOperationNodeReadEvidence,
					Kind:          agentdomain.WorkspaceAnalysisOperationSourceRead,
					Ordinal:       ordinal,
				},
				Tool: toolsapplication.ExecuteToolCommand{
					Identity: workspaceAnalysisToolIdentity(execution), Invocation: toolsdomain.InvocationSourceTrustedWorkflow,
					CallNo: ordinal,
					Request: toolsdomain.ToolRequestV1{
						SchemaVersion: toolsdomain.ToolRequestSchemaVersionV1,
						ToolName:      workspaceAnalysisReadSourceRef.Name,
						Arguments:     arguments,
						Reason:        workspaceAnalysisReadEvidenceReason,
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
		read, err := workspaceAnalysisReadEvidenceReceipt(execution, searchReceipt, ordinal, evidenceRef, arguments, toolResult)
		if err != nil {
			return workflowapplication.ExecutionResult{}, err
		}
		if workspaceAnalysisReadEvidenceIDReused(seenResultIDs, read.ToolCallID, read.ToolReceiptID) {
			return workflowapplication.ExecutionResult{}, workflowError(
				foundation.ErrorConsistencyViolation,
				ErrorCodeOutputInvalid,
				false,
				errors.New("workspace analysis source result reuses a predecessor or read identity"),
			)
		}
		reads = append(reads, read)
	}

	output, err := conversationworkflow.EncodeWorkspaceAnalysisReadEvidenceOutput(
		conversationworkflow.WorkspaceAnalysisReadEvidenceOutput{
			SchemaVersion: conversationworkflow.WorkspaceAnalysisOutputSchemaVersion,
			Reads:         reads,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workflowapplication.ExecutionResult{Output: output}, nil
}

func workspaceAnalysisReadEvidenceIDReused(
	seen map[foundation.ID]struct{},
	values ...foundation.ID,
) bool {
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func validateWorkspaceAnalysisReadEvidenceExecution(execution workflowapplication.ExecutionContext) error {
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	node, found := workspaceAnalysisNode(definition, conversationworkflow.WorkspaceAnalysisNodeReadEvidence)
	identity := workspaceAnalysisToolIdentity(execution)
	if !found || execution.DefinitionVersion != definition.Version || execution.DefinitionHash != definition.GraphHash ||
		execution.NodeKey != node.Key || execution.NodeKind != node.Kind || execution.InputSchemaVersion != node.InputSchemaVersion ||
		!validExecutionID(execution.WorkspaceID) || !validExecutionID(execution.DefinitionID) || !validExecutionID(execution.RunID) ||
		!validExecutionID(execution.NodeRunID) || !validExecutionID(execution.NodeAttemptID) || execution.NodeVersion < 1 ||
		execution.AttemptNo < 1 || execution.DispatchNo < 1 || execution.RetryNo < 0 || strings.TrimSpace(execution.LeaseOwner) == "" ||
		(execution.ModelSettingsRevision != nil && *execution.ModelSettingsRevision < 0) || identity.Validate() != nil {
		return workflowError(
			foundation.ErrorInvalidInput,
			ErrorCodeInputInvalid,
			false,
			errors.New("workspace analysis read evidence execution binding is invalid"),
		)
	}
	return nil
}

func validateWorkspaceAnalysisSearchReceipt(
	predecessor conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput,
	receipt toolsdomain.ResultReceipt,
	execution workflowapplication.ExecutionContext,
) (conversationworkflow.WorkspaceAnalysisRetrieveSearchSummary, error) {
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(workspaceAnalysisSearchRef)
	outputHash := sha256.Sum256(receipt.Output)
	if !found || !validExecutionID(receipt.ID) || !validExecutionID(receipt.ToolCallID) ||
		!validExecutionID(receipt.NodeRunID) || !validExecutionID(receipt.NodeAttemptID) ||
		receipt.ID != predecessor.SearchToolReceiptID || receipt.ToolCallID != predecessor.SearchToolCallID ||
		receipt.WorkspaceID != execution.WorkspaceID || receipt.WorkflowRunID != execution.RunID ||
		receipt.Tool != workspaceAnalysisSearchRef || receipt.OutputSchema != contract.OutputSchema ||
		receipt.PersistencePolicy != toolsdomain.ResultPersistenceCanonical || receipt.MaxOutputBytes != contract.MaxOutputBytes ||
		receipt.MaxPrivateBindingBytes != contract.MaxPrivateBindingBytes || receipt.OutputBytes != int64(len(receipt.Output)) ||
		receipt.OutputHash != predecessor.SearchToolReceiptHash || receipt.OutputHash != hex.EncodeToString(outputHash[:]) {
		return conversationworkflow.WorkspaceAnalysisRetrieveSearchSummary{}, workspaceAnalysisReceiptError(
			errors.New("workspace analysis search receipt differs from the retrieve predecessor"),
		)
	}
	if _, _, err := toolsdomain.SearchKnowledgeV2ReceiptIndexVersionID(receipt); err != nil {
		return conversationworkflow.WorkspaceAnalysisRetrieveSearchSummary{}, workspaceAnalysisReceiptError(err)
	}
	summary, err := conversationworkflow.DecodeWorkspaceAnalysisRetrieveSearchSummary(receipt.Output)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisRetrieveSearchSummary{}, workspaceAnalysisReceiptError(err)
	}
	if !sameWorkspaceAnalysisSearchSummary(summary, predecessor.SearchSummary) {
		return conversationworkflow.WorkspaceAnalysisRetrieveSearchSummary{}, workspaceAnalysisReceiptError(
			errors.New("workspace analysis search receipt summary differs from the retrieve predecessor"),
		)
	}
	return summary, nil
}

func sameWorkspaceAnalysisSearchSummary(
	left, right conversationworkflow.WorkspaceAnalysisRetrieveSearchSummary,
) bool {
	if left.EffectiveMode != right.EffectiveMode || left.HitCount != right.HitCount ||
		len(left.EvidenceRefs) != len(right.EvidenceRefs) || len(left.DegradationCodes) != len(right.DegradationCodes) {
		return false
	}
	for index := range left.EvidenceRefs {
		if left.EvidenceRefs[index] != right.EvidenceRefs[index] {
			return false
		}
	}
	for index := range left.DegradationCodes {
		if left.DegradationCodes[index] != right.DegradationCodes[index] {
			return false
		}
	}
	return true
}

func workspaceAnalysisReadEvidenceReceipt(
	execution workflowapplication.ExecutionContext,
	searchReceipt toolsdomain.ResultReceipt,
	ordinal int,
	evidenceRef string,
	arguments json.RawMessage,
	result toolsapplication.ToolExecutionResult,
) (conversationworkflow.WorkspaceAnalysisReadEvidenceReceipt, error) {
	call := result.Call
	outputHash := sha256.Sum256(result.Output)
	requestDocument, requestErr := workspaceAnalysisReadEvidenceRequestDocument(arguments)
	requestHash := sha256.Sum256(requestDocument)
	validAttempt := result.Replayed || call.NodeAttemptID == execution.NodeAttemptID
	if err := toolsdomain.ValidateToolCall(call); err != nil || requestErr != nil || !validAttempt ||
		call.Status != toolsdomain.CallSucceeded || call.WorkspaceID != execution.WorkspaceID ||
		call.WorkflowRunID != execution.RunID || call.NodeRunID != execution.NodeRunID || call.CallNo != ordinal ||
		call.RequestedToolName != workspaceAnalysisReadSourceRef.Name || call.Tool == nil || *call.Tool != workspaceAnalysisReadSourceRef ||
		call.InputSchema == nil || *call.InputSchema != workspaceAnalysisReadSourceInputSchema ||
		call.OutputSchema == nil || *call.OutputSchema != workspaceAnalysisReadSourceOutputSchema ||
		call.Capability != capability.ReadLocal || call.SideEffectLevel != toolsdomain.SideEffectNone ||
		call.InvocationPolicy != toolsdomain.InvocationTrustedWorkflowOnly || call.IdempotencyKey != "" ||
		call.RequestHash != hex.EncodeToString(requestHash[:]) || call.RequestBytes != int64(len(requestDocument)) ||
		!validExecutionID(result.ResultReceiptID) || result.ResultReceiptID == call.ID ||
		result.ResultReceiptID == searchReceipt.ID || call.ID == searchReceipt.ToolCallID || !result.UntrustedData ||
		call.ResponseBytes != int64(len(result.Output)) || call.ResponseHash != hex.EncodeToString(outputHash[:]) {
		if err == nil {
			err = errors.New("workspace analysis source result binding is invalid")
		}
		return conversationworkflow.WorkspaceAnalysisReadEvidenceReceipt{}, workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, err,
		)
	}
	decoded, err := decodeWorkspaceAnalysisReadSourceOutput(result.Output)
	if err != nil || decoded.EvidenceRef != evidenceRef {
		if err == nil {
			err = errors.New("workspace analysis source output references different evidence")
		}
		return conversationworkflow.WorkspaceAnalysisReadEvidenceReceipt{}, workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, err,
		)
	}
	return conversationworkflow.WorkspaceAnalysisReadEvidenceReceipt{
		EvidenceRef: evidenceRef, ToolCallID: call.ID,
		ToolReceiptID: result.ResultReceiptID, ToolReceiptHash: call.ResponseHash,
	}, nil
}

func workspaceAnalysisReadEvidenceRequestDocument(arguments json.RawMessage) ([]byte, error) {
	request := struct {
		SchemaVersion int                   `json:"schema_version"`
		ToolName      string                `json:"tool_name"`
		ToolVersion   int64                 `json:"tool_version"`
		InputSchema   toolsdomain.SchemaRef `json:"input_schema"`
		Arguments     json.RawMessage       `json:"arguments"`
		Reason        string                `json:"reason"`
	}{
		SchemaVersion: toolsdomain.ToolRequestSchemaVersionV1,
		ToolName:      workspaceAnalysisReadSourceRef.Name,
		ToolVersion:   workspaceAnalysisReadSourceRef.Version,
		InputSchema:   workspaceAnalysisReadSourceInputSchema,
		Arguments:     append(json.RawMessage(nil), arguments...),
		Reason:        workspaceAnalysisReadEvidenceReason,
	}
	return json.Marshal(request)
}

type persistedWorkspaceAnalysisReadSourceOutput struct {
	EvidenceRef *string `json:"evidence_ref"`
	ContentHash *string `json:"content_hash"`
	Truncated   *bool   `json:"truncated"`
	Excerpt     *string `json:"excerpt"`
}

type workspaceAnalysisReadSourceOutput struct {
	EvidenceRef string
}

func decodeWorkspaceAnalysisReadSourceOutput(raw json.RawMessage) (workspaceAnalysisReadSourceOutput, error) {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = workspaceAnalysisMaxReadSourceDocument
	limits.MaxDepth = 2
	limits.MaxStringBytes = workspaceAnalysisMaxReadSourceExcerpt
	limits.MaxArrayItems = 0
	limits.MaxObjectFields = 4
	persisted, err := foundationstrictjson.DecodeObject[persistedWorkspaceAnalysisReadSourceOutput](raw, limits, nil)
	if err != nil || persisted.EvidenceRef == nil || persisted.ContentHash == nil || persisted.Truncated == nil || persisted.Excerpt == nil ||
		!validWorkspaceAnalysisReadEvidenceRef(*persisted.EvidenceRef) || !validWorkspaceAnalysisLowerHash(*persisted.ContentHash) ||
		len(*persisted.Excerpt) < 1 || len(*persisted.Excerpt) > workspaceAnalysisMaxReadSourceExcerpt ||
		!utf8.ValidString(*persisted.Excerpt) || strings.ContainsRune(*persisted.Excerpt, '\x00') || strings.TrimSpace(*persisted.Excerpt) == "" {
		if err == nil {
			err = errors.New("workspace analysis source output is invalid")
		}
		return workspaceAnalysisReadSourceOutput{}, err
	}
	return workspaceAnalysisReadSourceOutput{EvidenceRef: *persisted.EvidenceRef}, nil
}

func validWorkspaceAnalysisReadEvidenceRef(value string) bool {
	return len(value) == 2 && value[0] == 'E' && value[1] >= '1' && value[1] <= byte('0'+agentdomain.WorkspaceAnalysisV1MaxSourceReads)
}

func workspaceAnalysisReadEvidenceRef(ordinal int) string {
	return "E" + strconv.Itoa(ordinal)
}

func validWorkspaceAnalysisLowerHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for index := range value {
		if (value[index] < '0' || value[index] > '9') && (value[index] < 'a' || value[index] > 'f') {
			return false
		}
	}
	return true
}

func workspaceAnalysisReceiptError(cause error) error {
	return workflowError(
		foundation.ErrorConsistencyViolation,
		string(conversationdomain.WorkspaceAnalysisReceiptInvalid),
		false,
		cause,
	)
}

var _ workflowapplication.Executor = (*WorkspaceAnalysisReadEvidenceExecutor)(nil)
