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
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

type workspaceAnalysisSynthesisRunner interface {
	Run(context.Context, agentapplication.WorkspaceAnalysisSynthesisRequest) (agentapplication.WorkspaceAnalysisSynthesisResult, error)
}

// WorkspaceAnalysisSynthesizeExecutorDependencies 是 synthesize_answer 的全部权威读取和模型边界。
type WorkspaceAnalysisSynthesizeExecutorDependencies struct {
	Context   conversationapplication.QuestionExecutionContextLoader
	Runs      agentapplication.WorkspaceAnalysisRunLoader
	Inputs    workspaceAnalysisRunInputReader
	Stages    workspaceAnalysisStageOutputReader
	Evidence  toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityReader
	Synthesis workspaceAnalysisSynthesisRunner
	Finalizer conversationapplication.WorkspaceAnalysisFinalizer
	Clock     foundation.Clock
}

// WorkspaceAnalysisSynthesizeExecutor 从已持久化 Git/Search/Read facts 构造一次 tool-free Candidate。
type WorkspaceAnalysisSynthesizeExecutor struct {
	dependencies WorkspaceAnalysisSynthesizeExecutorDependencies
	clock        foundation.Clock
}

// NewWorkspaceAnalysisSynthesizeExecutor 创建只接受 workspace-analysis@1 synthesize_answer 的 Executor。
func NewWorkspaceAnalysisSynthesizeExecutor(
	dependencies WorkspaceAnalysisSynthesizeExecutorDependencies,
) (*WorkspaceAnalysisSynthesizeExecutor, error) {
	if nilDependency(dependencies.Context) || nilDependency(dependencies.Runs) || nilDependency(dependencies.Inputs) ||
		nilDependency(dependencies.Stages) || nilDependency(dependencies.Evidence) || nilDependency(dependencies.Synthesis) ||
		nilDependency(dependencies.Finalizer) {
		return nil, workflowError(
			foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("workspace analysis synthesis dependencies are unavailable"),
		)
	}
	clock := dependencies.Clock
	if nilDependency(clock) {
		clock = foundation.SystemClock{}
	}
	return &WorkspaceAnalysisSynthesizeExecutor{dependencies: dependencies, clock: clock}, nil
}

// Execute 重读全部前序事实；只有完整闭包通过后才允许 Synthesis Runner 授权模型调用。
func (executor *WorkspaceAnalysisSynthesizeExecutor) Execute(
	ctx context.Context,
	execution workflowapplication.ExecutionContext,
) (workflowapplication.ExecutionResult, error) {
	if executor == nil || nilDependency(executor.dependencies.Context) || nilDependency(executor.dependencies.Runs) ||
		nilDependency(executor.dependencies.Inputs) || nilDependency(executor.dependencies.Stages) ||
		nilDependency(executor.dependencies.Evidence) || nilDependency(executor.dependencies.Synthesis) ||
		nilDependency(executor.dependencies.Finalizer) {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("workspace analysis synthesis executor is unavailable"),
		)
	}
	if ctx == nil {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false,
			errors.New("workspace analysis synthesis context is nil"),
		)
	}
	if err := validateWorkspaceAnalysisSynthesisExecution(execution); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	nodeStartedAt, err := workspaceAnalysisExecutionStartedAt(executor.clock)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}

	readOutput, err := conversationworkflow.DecodeWorkspaceAnalysisReadEvidenceOutput(execution.Input)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	authoritativeReadRaw, err := executor.dependencies.Stages.GetSucceededNodeOutput(
		ctx, execution.WorkspaceID, execution.RunID, conversationworkflow.WorkspaceAnalysisNodeReadEvidence,
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if !bytes.Equal(execution.Input, authoritativeReadRaw) {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(
			errors.New("workspace analysis read predecessor differs from its succeeded output"),
		)
	}
	authoritativeRead, err := conversationworkflow.DecodeWorkspaceAnalysisReadEvidenceOutput(authoritativeReadRaw)
	if err != nil || !reflect.DeepEqual(readOutput, authoritativeRead) {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(
			workspaceAnalysisSynthesisCause(err, "workspace analysis read predecessor is invalid"),
		)
	}
	retrieveRaw, err := executor.dependencies.Stages.GetSucceededNodeOutput(
		ctx, execution.WorkspaceID, execution.RunID, conversationworkflow.WorkspaceAnalysisNodeRetrieveEvidence,
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	retrieveOutput, err := conversationworkflow.DecodeWorkspaceAnalysisRetrieveEvidenceOutput(retrieveRaw)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	inspectRaw, err := executor.dependencies.Stages.GetSucceededNodeOutput(
		ctx, execution.WorkspaceID, execution.RunID, conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace,
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	inspectOutput, err := conversationworkflow.DecodeWorkspaceAnalysisInspectOutput(inspectRaw)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if err := validateWorkspaceAnalysisSynthesisPredecessors(inspectOutput, retrieveOutput, readOutput); err != nil {
		return workflowapplication.ExecutionResult{}, err
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
		ctx, nodeStartedAt, analysisRun.DeadlineAt, deadlines.SynthesizeAnswerDeadline(),
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, finalizeWorkspaceAnalysisPreAuthorizationDeadlineError(
			ctx, executor.dependencies.Finalizer, execution, root,
			questionContext.Answer.Version, analysisRun.ID, err,
		)
	}
	defer cancel()

	authority, err := executor.dependencies.Evidence.LoadWorkspaceAnalysisSynthesisEvidence(
		nodeContext,
		toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityQuery{
			WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, AnalysisRunID: analysisRun.ID,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	retrieval, evidence, err := validateWorkspaceAnalysisSynthesisAuthority(
		execution, analysisRun, retrieveOutput, readOutput, authority,
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	modelInput, err := buildWorkspaceAnalysisSynthesisInput(questionContext, inspectOutput, retrieveOutput, evidence)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	request := agentapplication.WorkspaceAnalysisSynthesisRequest{
		Identity: workspaceAnalysisModelIdentity(execution), AnalysisRunID: analysisRun.ID, AnswerID: root.AnswerID,
		AttemptNo: execution.AttemptNo, ModelSettingsRevision: cloneWorkspaceAnalysisInt64(execution.ModelSettingsRevision),
		Retrieval: retrieval, ProfileRef: DefaultProfileRef(), PromptRef: WorkspaceAnalysisSynthesisPromptRef(),
		Input: modelInput, AllowedEvidenceRefs: append([]string{}, authority.EvidenceRefs...),
	}
	result, err := executor.dependencies.Synthesis.Run(nodeContext, request)
	if err != nil {
		return workflowapplication.ExecutionResult{}, finalizeWorkspaceAnalysisModelTerminalError(
			nodeContext, executor.dependencies.Finalizer, execution, root,
			questionContext.Answer.Version, analysisRun.ID, err,
		)
	}
	if err := validateWorkspaceAnalysisSynthesisResult(execution, analysisRun, request, result, retrieveOutput, readOutput); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	output, err := conversationworkflow.EncodeWorkspaceAnalysisSynthesisOutput(
		conversationworkflow.WorkspaceAnalysisSynthesisOutput{
			SchemaVersion: conversationworkflow.WorkspaceAnalysisOutputSchemaVersion,
			CandidateID:   result.Candidate.ID, CandidateHash: result.Candidate.DocumentHash,
			SynthesisModelRunID: result.Run.ID, SynthesisModelCallID: result.Call.ID,
			DraftSessionID: result.Draft.ID, DraftGeneration: result.Draft.Generation,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workflowapplication.ExecutionResult{Output: output}, nil
}

func validateWorkspaceAnalysisSynthesisExecution(execution workflowapplication.ExecutionContext) error {
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	node, found := workspaceAnalysisNode(definition, conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer)
	identity := workspaceAnalysisModelIdentity(execution)
	if !found || execution.DefinitionVersion != definition.Version || execution.DefinitionHash != definition.GraphHash ||
		execution.NodeKey != node.Key || execution.NodeKind != node.Kind || execution.InputSchemaVersion != node.InputSchemaVersion ||
		!validExecutionID(execution.WorkspaceID) || !validExecutionID(execution.DefinitionID) || !validExecutionID(execution.RunID) ||
		!validExecutionID(execution.NodeRunID) || !validExecutionID(execution.NodeAttemptID) || execution.NodeVersion < 1 ||
		execution.AttemptNo < 1 || execution.DispatchNo < 1 || execution.RetryNo < 0 || strings.TrimSpace(execution.LeaseOwner) == "" ||
		(execution.ModelSettingsRevision != nil && *execution.ModelSettingsRevision < 0) || identity.Validate() != nil {
		return workflowError(
			foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false,
			errors.New("workspace analysis synthesis execution binding is invalid"),
		)
	}
	return nil
}

func validateWorkspaceAnalysisSynthesisRun(
	run agentdomain.WorkspaceAnalysisRun,
	execution workflowapplication.ExecutionContext,
	root conversationworkflow.WorkspaceAnalysisInput,
) error {
	if err := agentdomain.ValidateWorkspaceAnalysisRun(run); err != nil {
		return workflowError(foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false, err)
	}
	if run.WorkspaceID != execution.WorkspaceID || run.WorkflowRunID != execution.RunID ||
		run.ConversationID != root.ConversationID || run.QuestionID != root.QuestionID || run.AnswerID != root.AnswerID ||
		run.DefinitionKey != conversationworkflow.WorkspaceAnalysisDefinitionKey ||
		run.DefinitionVersion != conversationworkflow.WorkspaceAnalysisDefinitionVersion || run.DefinitionHash != execution.DefinitionHash ||
		(run.Status != agentdomain.WorkspaceAnalysisRunQueued && run.Status != agentdomain.WorkspaceAnalysisRunRunning) {
		return workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false,
			errors.New("workspace analysis run differs from the active synthesis execution"),
		)
	}
	return nil
}

func validateWorkspaceAnalysisSynthesisPredecessors(
	inspect conversationworkflow.WorkspaceAnalysisInspectOutput,
	retrieve conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput,
	read conversationworkflow.WorkspaceAnalysisReadEvidenceOutput,
) error {
	if retrieve.GitToolCallID != inspect.ToolCallID || retrieve.GitToolReceiptID != inspect.ToolReceiptID ||
		retrieve.GitToolReceiptHash != inspect.ToolReceiptHash || retrieve.SearchSummary.HitCount < 1 ||
		len(read.Reads) != min(retrieve.SearchSummary.HitCount, agentdomain.WorkspaceAnalysisV1MaxSourceReads) {
		return workspaceAnalysisReceiptError(errors.New("workspace analysis synthesis predecessor chain is invalid"))
	}
	seen := map[foundation.ID]struct{}{
		inspect.ToolCallID: {}, inspect.ToolReceiptID: {}, retrieve.PlannerModelRunID: {},
		retrieve.PlannerModelCallID: {}, retrieve.PlannerModelResultID: {},
		retrieve.SearchToolCallID: {}, retrieve.SearchToolReceiptID: {},
	}
	if len(seen) != 7 {
		return workspaceAnalysisReceiptError(errors.New("workspace analysis synthesis predecessor identity is reused"))
	}
	for index, item := range read.Reads {
		if item.EvidenceRef != retrieve.SearchSummary.EvidenceRefs[index] ||
			workspaceAnalysisReadEvidenceIDReused(seen, item.ToolCallID, item.ToolReceiptID) {
			return workspaceAnalysisReceiptError(errors.New("workspace analysis synthesis read predecessor is invalid"))
		}
	}
	return nil
}

func validateWorkspaceAnalysisSynthesisAuthority(
	execution workflowapplication.ExecutionContext,
	run agentdomain.WorkspaceAnalysisRun,
	retrieve conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput,
	read conversationworkflow.WorkspaceAnalysisReadEvidenceOutput,
	authority toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority,
) (agentdomain.RetrievalRef, []toolsdomain.ReadSourceV3ReceiptEvidence, error) {
	if authority.WorkspaceID != execution.WorkspaceID || authority.WorkflowRunID != execution.RunID ||
		authority.AnalysisRunID != run.ID || len(authority.EvidenceRefs) != len(read.Reads) ||
		len(authority.ReadSourceReceipts) != len(read.Reads) || len(authority.Evidence) != len(read.Reads) {
		return agentdomain.RetrievalRef{}, nil, workspaceAnalysisReceiptError(
			errors.New("workspace analysis synthesis authority binding is invalid"),
		)
	}
	searchSummary, err := validateWorkspaceAnalysisSearchReceipt(retrieve, authority.SearchReceipt, execution)
	if err != nil || !sameWorkspaceAnalysisSearchSummary(searchSummary, retrieve.SearchSummary) {
		return agentdomain.RetrievalRef{}, nil, workspaceAnalysisReceiptError(
			workspaceAnalysisSynthesisCause(err, "workspace analysis synthesis search authority differs"),
		)
	}
	selected, err := toolsdomain.SearchKnowledgeV2ReceiptSelectedRefs(authority.SearchReceipt)
	if err != nil || !reflect.DeepEqual(selected, authority.EvidenceRefs) || len(selected) != len(read.Reads) {
		return agentdomain.RetrievalRef{}, nil, workspaceAnalysisReceiptError(
			workspaceAnalysisSynthesisCause(err, "workspace analysis synthesis selected references differ"),
		)
	}
	indexVersionID, found, err := toolsdomain.SearchKnowledgeV2ReceiptIndexVersionID(authority.SearchReceipt)
	if err != nil || !found {
		return agentdomain.RetrievalRef{}, nil, workspaceAnalysisReceiptError(
			workspaceAnalysisSynthesisCause(err, "workspace analysis synthesis search index is unavailable"),
		)
	}
	retrieval := agentdomain.RetrievalRef{IndexVersionID: indexVersionID}
	if retrieval.Validate() != nil {
		return agentdomain.RetrievalRef{}, nil, workspaceAnalysisReceiptError(
			errors.New("workspace analysis synthesis retrieval snapshot is invalid"),
		)
	}
	evidence := make([]toolsdomain.ReadSourceV3ReceiptEvidence, len(read.Reads))
	for index, expected := range read.Reads {
		receipt := authority.ReadSourceReceipts[index]
		contract, contractFound := toolsdomain.WorkspaceAnalysisResultReceiptContract(workspaceAnalysisReadSourceRef)
		digest := sha256.Sum256(receipt.Output)
		if !contractFound || receipt.ID != expected.ToolReceiptID || receipt.ToolCallID != expected.ToolCallID ||
			receipt.WorkspaceID != execution.WorkspaceID || receipt.WorkflowRunID != execution.RunID ||
			receipt.Tool != workspaceAnalysisReadSourceRef || receipt.OutputSchema != contract.OutputSchema ||
			receipt.PersistencePolicy != toolsdomain.ResultPersistenceCanonical || receipt.MaxOutputBytes != contract.MaxOutputBytes ||
			receipt.MaxPrivateBindingBytes != contract.MaxPrivateBindingBytes || receipt.OutputBytes != int64(len(receipt.Output)) ||
			receipt.OutputHash != expected.ToolReceiptHash || receipt.OutputHash != hex.EncodeToString(digest[:]) {
			return agentdomain.RetrievalRef{}, nil, workspaceAnalysisReceiptError(
				errors.New("workspace analysis synthesis source receipt differs from the read predecessor"),
			)
		}
		evidence[index], err = toolsdomain.ReadSourceV3ReceiptEvidenceForSearch(receipt, authority.SearchReceipt)
		if err != nil || evidence[index].EvidenceRef != expected.EvidenceRef ||
			evidence[index] != authority.Evidence[index] || authority.EvidenceRefs[index] != expected.EvidenceRef {
			return agentdomain.RetrievalRef{}, nil, workspaceAnalysisReceiptError(
				workspaceAnalysisSynthesisCause(err, "workspace analysis synthesis source authority differs"),
			)
		}
	}
	return retrieval, evidence, nil
}

type workspaceAnalysisSynthesisSearchInput struct {
	EffectiveMode    string   `json:"effective_mode"`
	HitCount         int      `json:"hit_count"`
	DegradationCodes []string `json:"degradation_codes"`
}

type workspaceAnalysisSynthesisEvidenceInput struct {
	EvidenceRef string `json:"evidence_ref"`
	Excerpt     string `json:"excerpt"`
	Truncated   bool   `json:"truncated"`
}

type workspaceAnalysisSynthesisInput struct {
	SchemaVersion int                                                    `json:"schema_version"`
	UntrustedData bool                                                   `json:"untrusted_data"`
	Question      string                                                 `json:"question"`
	History       []ragModelHistoryTurn                                  `json:"history"`
	AnswerDepth   string                                                 `json:"answer_depth"`
	OutputFormat  string                                                 `json:"output_format"`
	GitStatus     conversationworkflow.WorkspaceAnalysisGitStatusSummary `json:"git_status"`
	Search        workspaceAnalysisSynthesisSearchInput                  `json:"search"`
	Evidence      []workspaceAnalysisSynthesisEvidenceInput              `json:"evidence"`
}

func buildWorkspaceAnalysisSynthesisInput(
	value conversationapplication.QuestionExecutionContext,
	inspect conversationworkflow.WorkspaceAnalysisInspectOutput,
	retrieve conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput,
	evidence []toolsdomain.ReadSourceV3ReceiptEvidence,
) ([]byte, error) {
	history := make([]ragModelHistoryTurn, len(value.History))
	for index, turn := range value.History {
		history[index] = ragModelHistoryTurn{
			Ordinal: turn.Ordinal, QuestionText: turn.QuestionText,
			AssistantText: turn.AssistantText, ResultType: turn.ResultType,
		}
	}
	modelEvidence := make([]workspaceAnalysisSynthesisEvidenceInput, len(evidence))
	for index, item := range evidence {
		modelEvidence[index] = workspaceAnalysisSynthesisEvidenceInput{
			EvidenceRef: item.EvidenceRef, Excerpt: item.Excerpt, Truncated: item.Truncated,
		}
	}
	input := workspaceAnalysisSynthesisInput{
		SchemaVersion: 1, UntrustedData: true, Question: value.Question.Request.QuestionText, History: history,
		AnswerDepth: string(value.Question.Request.AnswerDepth), OutputFormat: string(value.Question.Request.OutputFormat),
		GitStatus: inspect.GitStatus,
		Search: workspaceAnalysisSynthesisSearchInput{
			EffectiveMode: retrieve.SearchSummary.EffectiveMode, HitCount: retrieve.SearchSummary.HitCount,
			DegradationCodes: append([]string{}, retrieve.SearchSummary.DegradationCodes...),
		},
		Evidence: modelEvidence,
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) == 0 || len(encoded) > agentapplication.MaxStructuredInputBytes || bytes.IndexByte(encoded, 0) >= 0 {
		return nil, workflowError(
			foundation.ErrorNonRetryableFailure, ErrorCodeInputInvalid, false,
			workspaceAnalysisSynthesisCause(err, "workspace analysis synthesis input cannot be encoded within its budget"),
		)
	}
	return encoded, nil
}

func validateWorkspaceAnalysisSynthesisResult(
	execution workflowapplication.ExecutionContext,
	analysisRun agentdomain.WorkspaceAnalysisRun,
	request agentapplication.WorkspaceAnalysisSynthesisRequest,
	result agentapplication.WorkspaceAnalysisSynthesisResult,
	retrieve conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput,
	read conversationworkflow.WorkspaceAnalysisReadEvidenceOutput,
) error {
	candidate := result.Candidate
	run := result.Run
	call := result.Call
	canonicalCandidate, encodeErr := json.Marshal(result.CandidateResult)
	digest := sha256.Sum256(canonicalCandidate)
	validAttempt := result.Replayed || run.NodeAttemptID == execution.NodeAttemptID
	if agentdomain.ValidateWorkspaceAnalysisCandidate(candidate) != nil || result.CandidateResult.Validate() != nil ||
		agentdomain.ValidateModelRun(run) != nil || agentdomain.ValidateModelCall(call) != nil || encodeErr != nil || !validAttempt ||
		candidate.WorkspaceID != execution.WorkspaceID || candidate.AnalysisRunID != analysisRun.ID ||
		candidate.AnswerID != request.AnswerID || candidate.NodeAttemptID != run.NodeAttemptID ||
		candidate.SynthesisModelRunID != run.ID || candidate.DocumentHash != hex.EncodeToString(digest[:]) ||
		candidate.DocumentBytes != int64(len(canonicalCandidate)) || !bytes.Equal(candidate.Document, canonicalCandidate) ||
		result.CandidateResult.ModelRunRef != run.ID ||
		run.WorkspaceID != execution.WorkspaceID || run.WorkflowRunID != execution.RunID || run.NodeRunID != execution.NodeRunID ||
		!sameWorkspaceAnalysisOptionalInt64(run.ModelSettingsRevision, request.ModelSettingsRevision) ||
		run.Profile != request.ProfileRef || run.Prompt != request.PromptRef ||
		run.Schema != (agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: "1"}) ||
		run.ReducedSchema != run.Schema || !sameWorkspaceAnalysisRetrievalRef(run.Retrieval, request.Retrieval) ||
		run.Status != agentdomain.ModelRunSucceeded || run.FinalResultType != agentdomain.ResultTypeWorkspaceAnalysisAnswer ||
		run.FinalErrorCode != "" || call.ModelRunID != run.ID || call.CallNo != 1 ||
		call.Phase != agentdomain.ModelCallAnswer || call.Model != run.Model || call.Profile != run.Profile ||
		call.Prompt != run.Prompt || call.Schema != run.Schema || call.MaxOutputTokens < 1 ||
		call.MaxOutputTokens > int(agentapplication.WorkspaceAnalysisV1SynthesisMaxOutputTokens) ||
		call.Status != agentdomain.ModelCallSucceeded || call.ResponseHash != candidate.DocumentHash ||
		call.ResponseBytes != candidate.DocumentBytes || call.Usage != result.Usage ||
		!validExecutionID(result.Draft.ID) || result.Draft.Binding.WorkspaceID != execution.WorkspaceID ||
		result.Draft.Binding.AnswerID != request.AnswerID || result.Draft.Binding.WorkflowRunID != execution.RunID ||
		result.Draft.Binding.NodeRunID != run.NodeRunID || result.Draft.Binding.NodeAttemptID != run.NodeAttemptID ||
		result.Draft.Binding.AttemptNo < 1 || strings.TrimSpace(result.Draft.Binding.LeaseOwner) == "" ||
		result.Draft.Generation < 1 ||
		(result.Draft.Status != agentapplication.DraftStreamCompleted && result.Draft.Status != agentapplication.DraftStreamDegraded) ||
		!workspaceAnalysisCandidateRefsAllowedByRead(result.CandidateResult.Payload.CitationRefs, read.Reads) ||
		workspaceAnalysisSynthesisResultReusesPredecessorID(result, retrieve, read) {
		return workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false,
			errors.New("workspace analysis synthesis result authority binding is invalid"),
		)
	}
	return nil
}

func workspaceAnalysisCandidateRefsAllowedByRead(
	references []string,
	reads []conversationworkflow.WorkspaceAnalysisReadEvidenceReceipt,
) bool {
	if len(references) < 1 {
		return false
	}
	allowed := make(map[string]struct{}, len(reads))
	for _, read := range reads {
		allowed[read.EvidenceRef] = struct{}{}
	}
	for _, reference := range references {
		if _, exists := allowed[reference]; !exists {
			return false
		}
	}
	return true
}

func workspaceAnalysisSynthesisResultReusesPredecessorID(
	result agentapplication.WorkspaceAnalysisSynthesisResult,
	retrieve conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput,
	read conversationworkflow.WorkspaceAnalysisReadEvidenceOutput,
) bool {
	seen := map[foundation.ID]struct{}{
		retrieve.GitToolCallID: {}, retrieve.GitToolReceiptID: {}, retrieve.PlannerModelRunID: {},
		retrieve.PlannerModelCallID: {}, retrieve.PlannerModelResultID: {},
		retrieve.SearchToolCallID: {}, retrieve.SearchToolReceiptID: {},
	}
	for _, item := range read.Reads {
		seen[item.ToolCallID] = struct{}{}
		seen[item.ToolReceiptID] = struct{}{}
	}
	for _, id := range []foundation.ID{
		result.Candidate.ID, result.Candidate.SynthesisOperationID, result.Run.ID, result.Call.ID, result.Draft.ID,
	} {
		if _, duplicate := seen[id]; duplicate {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}

func workspaceAnalysisSynthesisCause(err error, message string) error {
	if err != nil {
		return err
	}
	return errors.New(message)
}

var _ workflowapplication.Executor = (*WorkspaceAnalysisSynthesizeExecutor)(nil)
