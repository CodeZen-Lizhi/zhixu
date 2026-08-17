package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const workspaceAnalysisRetrieveReason = "search approved workspace evidence"

var (
	workspaceAnalysisSearchRef          = toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 2}
	workspaceAnalysisSearchInputSchema  = toolsdomain.SchemaRef{ID: "tool.search_knowledge.input", Version: 2}
	workspaceAnalysisSearchOutputSchema = toolsdomain.SchemaRef{ID: "tool.search_knowledge.output", Version: 2}
)

type workspaceAnalysisRunInputReader interface {
	GetRunInput(context.Context, foundation.ID, foundation.ID) (json.RawMessage, error)
}

type workspaceAnalysisStageOutputReader interface {
	GetSucceededNodeOutput(context.Context, foundation.ID, foundation.ID, string) (json.RawMessage, error)
}

type workspaceAnalysisActiveSearchIndexLoader interface {
	LoadActiveSearchIndex(context.Context, foundation.ID) (retrievalapplication.SearchIndex, error)
}

// WorkspaceAnalysisRetrievalPlanRunner 将当前节点身份绑定到唯一 RETRIEVAL_PLAN 调用。
type WorkspaceAnalysisRetrievalPlanRunner interface {
	FindWorkspaceAnalysisRetrievalPlanCheckpoint(
		context.Context,
		agentapplication.WorkspaceAnalysisRetrievalPlanCheckpointQuery,
	) (agentapplication.WorkspaceAnalysisRetrievalPlanCheckpoint, bool, error)
	Run(context.Context, agentapplication.WorkspaceAnalysisRetrievalPlanRequest) (agentapplication.WorkspaceAnalysisRetrievalPlanResult, error)
}

// WorkspaceAnalysisRetrieveExecutorDependencies 是 retrieve_evidence 的全部权威读取与调用边界。
type WorkspaceAnalysisRetrieveExecutorDependencies struct {
	Context   conversationapplication.QuestionExecutionContextLoader
	Runs      agentapplication.WorkspaceAnalysisRunLoader
	Inputs    workspaceAnalysisRunInputReader
	Stages    workspaceAnalysisStageOutputReader
	Retrieval workspaceAnalysisActiveSearchIndexLoader
	Planner   WorkspaceAnalysisRetrievalPlanRunner
	Tools     workspaceAnalysisToolExecutor
	Receipts  toolsapplication.SearchKnowledgeV2ReceiptReader
	Finalizer conversationapplication.WorkspaceAnalysisFinalizer
	Clock     foundation.Clock
}

// WorkspaceAnalysisRetrieveExecutor 执行一次检索计划和一次 SearchKnowledge@2。
type WorkspaceAnalysisRetrieveExecutor struct {
	dependencies WorkspaceAnalysisRetrieveExecutorDependencies
	clock        foundation.Clock
}

// NewWorkspaceAnalysisRetrieveExecutor 创建只接受 workspace-analysis@1 retrieve_evidence 的 Executor。
func NewWorkspaceAnalysisRetrieveExecutor(
	dependencies WorkspaceAnalysisRetrieveExecutorDependencies,
) (*WorkspaceAnalysisRetrieveExecutor, error) {
	if nilDependency(dependencies.Context) || nilDependency(dependencies.Runs) || nilDependency(dependencies.Inputs) ||
		nilDependency(dependencies.Stages) || nilDependency(dependencies.Retrieval) || nilDependency(dependencies.Planner) ||
		nilDependency(dependencies.Tools) || nilDependency(dependencies.Receipts) || nilDependency(dependencies.Finalizer) {
		return nil, workflowError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeCapabilityUnavailable,
			false,
			errors.New("workspace analysis retrieve dependencies are unavailable"),
		)
	}
	clock := dependencies.Clock
	if nilDependency(clock) {
		clock = foundation.SystemClock{}
	}
	return &WorkspaceAnalysisRetrieveExecutor{dependencies: dependencies, clock: clock}, nil
}

// Execute 只从权威 root、前序回执、Question 和 Analysis Run 恢复检索输入。
func (executor *WorkspaceAnalysisRetrieveExecutor) Execute(
	ctx context.Context,
	execution workflowapplication.ExecutionContext,
) (workflowapplication.ExecutionResult, error) {
	if executor == nil || nilDependency(executor.dependencies.Context) || nilDependency(executor.dependencies.Runs) ||
		nilDependency(executor.dependencies.Inputs) || nilDependency(executor.dependencies.Stages) ||
		nilDependency(executor.dependencies.Retrieval) || nilDependency(executor.dependencies.Planner) ||
		nilDependency(executor.dependencies.Tools) || nilDependency(executor.dependencies.Receipts) ||
		nilDependency(executor.dependencies.Finalizer) {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeCapabilityUnavailable,
			false,
			errors.New("workspace analysis retrieve executor is unavailable"),
		)
	}
	if ctx == nil {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("workspace analysis retrieve context is nil"),
		)
	}
	if err := validateWorkspaceAnalysisRetrieveExecution(execution); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	nodeStartedAt, err := workspaceAnalysisExecutionStartedAt(executor.clock)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}

	predecessor, err := conversationworkflow.DecodeWorkspaceAnalysisInspectOutput(execution.Input)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	authoritativeRaw, err := executor.dependencies.Stages.GetSucceededNodeOutput(
		ctx, execution.WorkspaceID, execution.RunID, conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace,
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	authoritative, err := conversationworkflow.DecodeWorkspaceAnalysisInspectOutput(authoritativeRaw)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if !bytes.Equal(execution.Input, authoritativeRaw) || predecessor != authoritative {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorConsistencyViolation,
			ErrorCodeInputInvalid,
			false,
			errors.New("workspace analysis inspect predecessor differs from its succeeded output"),
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
		ctx, nodeStartedAt, analysisRun.DeadlineAt, deadlines.RetrieveEvidenceDeadline(),
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, finalizeWorkspaceAnalysisPreAuthorizationDeadlineError(
			ctx, executor.dependencies.Finalizer, execution, root,
			questionContext.Answer.Version, analysisRun.ID, err,
		)
	}
	defer cancel()

	checkpointQuery := agentapplication.WorkspaceAnalysisRetrievalPlanCheckpointQuery{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		AnalysisRunID: analysisRun.ID, NodeRunID: execution.NodeRunID,
	}
	checkpoint, checkpointFound, err := executor.dependencies.Planner.FindWorkspaceAnalysisRetrievalPlanCheckpoint(
		nodeContext,
		checkpointQuery,
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	var retrievalRef agentdomain.RetrievalRef
	if checkpointFound {
		retrievalRef = checkpoint.Retrieval
	} else {
		activeIndex, loadErr := executor.dependencies.Retrieval.LoadActiveSearchIndex(nodeContext, execution.WorkspaceID)
		if loadErr != nil {
			return workflowapplication.ExecutionResult{}, loadErr
		}
		retrievalRef, err = workspaceAnalysisRetrievalRef(execution.WorkspaceID, activeIndex)
		if err != nil {
			return workflowapplication.ExecutionResult{}, err
		}
	}
	plannerInput, err := buildWorkspaceAnalysisPlannerInput(questionContext, predecessor.GitStatus)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	plannerRequest := agentapplication.WorkspaceAnalysisRetrievalPlanRequest{
		Identity:              workspaceAnalysisModelIdentity(execution),
		AnalysisRunID:         analysisRun.ID,
		ModelSettingsRevision: cloneWorkspaceAnalysisInt64(execution.ModelSettingsRevision),
		Retrieval:             retrievalRef,
		ProfileRef:            DefaultProfileRef(),
		PromptRef:             WorkspaceAnalysisPlanPromptRef(),
		Input:                 plannerInput,
	}
	plannerResult, err := executor.dependencies.Planner.Run(nodeContext, plannerRequest)
	if err != nil {
		return workflowapplication.ExecutionResult{}, finalizeWorkspaceAnalysisModelTerminalError(
			nodeContext, executor.dependencies.Finalizer, execution, root,
			questionContext.Answer.Version, analysisRun.ID, err,
		)
	}
	if err := validateWorkspaceAnalysisPlannerResult(execution, analysisRun, predecessor, plannerRequest, plannerResult); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if checkpointFound {
		if err := validateWorkspaceAnalysisRetrievalPlanCheckpointResult(checkpoint, plannerResult); err != nil {
			return workflowapplication.ExecutionResult{}, err
		}
	}
	if plannerResult.Plan.Payload.RequiresClarification {
		operationID := plannerResult.ModelResult.OperationID
		_, finalizeErr := finalizeWorkspaceAnalysisTerminationPublication(
			nodeContext,
			executor.dependencies.Finalizer,
			conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
				WorkspaceAnalysisPublicationLookup: workspaceAnalysisPublicationLookup(execution, root, analysisRun.ID),
				ExpectedAnswerVersion:              questionContext.Answer.Version,
				Reason:                             agentdomain.WorkspaceAnalysisRunNeedsClarification,
				OperationID:                        &operationID,
				Artifact: &conversationapplication.WorkspaceAnalysisTerminationArtifact{
					Kind: conversationapplication.WorkspaceAnalysisTerminationArtifactModelResult,
					ID:   plannerResult.ModelResult.ID, Hash: plannerResult.ModelResult.DocumentHash,
				},
			},
		)
		if finalizeErr != nil {
			return workflowapplication.ExecutionResult{}, finalizeErr
		}
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorNonRetryableFailure,
			conversationdomain.WorkspaceAnalysisClarificationRequired,
			false,
			errors.New("workspace analysis requires clarification"),
		)
	}

	searchArguments, err := workspaceAnalysisSearchArguments(questionContext.Question, plannerResult.Plan)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	toolResult, err := executor.dependencies.Tools.ExecuteWorkspaceAnalysisTool(
		nodeContext,
		toolsapplication.ExecuteWorkspaceAnalysisToolCommand{
			OperationKey: agentdomain.WorkspaceAnalysisOperationKey{
				AnalysisRunID: analysisRun.ID,
				NodeKey:       agentdomain.WorkspaceAnalysisOperationNodeRetrieveEvidence,
				Kind:          agentdomain.WorkspaceAnalysisOperationKnowledgeSearch,
				Ordinal:       1,
			},
			Tool: toolsapplication.ExecuteToolCommand{
				Identity: workspaceAnalysisToolIdentity(execution), Invocation: toolsdomain.InvocationSourceTrustedWorkflow,
				CallNo: 1,
				Request: toolsdomain.ToolRequestV1{
					SchemaVersion: toolsdomain.ToolRequestSchemaVersionV1,
					ToolName:      workspaceAnalysisSearchRef.Name,
					Arguments:     searchArguments,
					Reason:        workspaceAnalysisRetrieveReason,
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
	searchReceipt, err := executor.dependencies.Receipts.LoadSearchKnowledgeV2Receipt(
		nodeContext,
		execution.WorkspaceID,
		execution.RunID,
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workspaceAnalysisRetrieveExecutionResult(
		execution,
		questionContext.Question.Request.Scope.RetrievalMode,
		predecessor,
		plannerResult,
		plannerRequest.Retrieval,
		searchArguments,
		toolResult,
		searchReceipt,
	)
}

func validateWorkspaceAnalysisRetrieveExecution(execution workflowapplication.ExecutionContext) error {
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	node, found := workspaceAnalysisNode(definition, conversationworkflow.WorkspaceAnalysisNodeRetrieveEvidence)
	modelIdentity := workspaceAnalysisModelIdentity(execution)
	toolIdentity := workspaceAnalysisToolIdentity(execution)
	if !found || execution.DefinitionVersion != definition.Version || execution.DefinitionHash != definition.GraphHash ||
		execution.NodeKey != node.Key || execution.NodeKind != node.Kind || execution.InputSchemaVersion != node.InputSchemaVersion ||
		!validExecutionID(execution.WorkspaceID) || !validExecutionID(execution.DefinitionID) || !validExecutionID(execution.RunID) ||
		!validExecutionID(execution.NodeRunID) || !validExecutionID(execution.NodeAttemptID) || execution.NodeVersion < 1 ||
		execution.AttemptNo < 1 || execution.DispatchNo < 1 || execution.RetryNo < 0 || strings.TrimSpace(execution.LeaseOwner) == "" ||
		(execution.ModelSettingsRevision != nil && *execution.ModelSettingsRevision < 0) ||
		modelIdentity.Validate() != nil || toolIdentity.Validate() != nil {
		return workflowError(
			foundation.ErrorInvalidInput,
			ErrorCodeInputInvalid,
			false,
			errors.New("workspace analysis retrieve execution binding is invalid"),
		)
	}
	return nil
}

func workspaceAnalysisModelIdentity(execution workflowapplication.ExecutionContext) agentapplication.WorkspaceAnalysisModelExecutionIdentity {
	return agentapplication.WorkspaceAnalysisModelExecutionIdentity{
		WorkspaceID: execution.WorkspaceID, DefinitionID: execution.DefinitionID,
		DefinitionVersion: execution.DefinitionVersion, DefinitionHash: execution.DefinitionHash,
		WorkflowRunID: execution.RunID, NodeKey: agentdomain.WorkspaceAnalysisOperationNodeKey(execution.NodeKey),
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
		LeaseOwner: execution.LeaseOwner, LeaseFence: int64(execution.AttemptNo),
	}
}

func validateWorkspaceAnalysisQuestionContext(
	value conversationapplication.QuestionExecutionContext,
	execution workflowapplication.ExecutionContext,
	root conversationworkflow.WorkspaceAnalysisInput,
) error {
	question := value.Question
	answer := value.Answer
	contextHash, throughOrdinal, _, contextErr := conversationdomain.ComputeContextHash(value.History)
	if conversationdomain.ValidateQuestion(question) != nil || conversationdomain.ValidateAnswer(answer) != nil || contextErr != nil ||
		question.ID != root.QuestionID || question.Request.WorkspaceID != execution.WorkspaceID ||
		question.Request.ConversationID != root.ConversationID || question.Request.Mode != conversationdomain.QuestionModeWorkspaceAnalysis ||
		question.Ordinal != root.QuestionOrdinal || question.ContextHash != root.ContextHash || contextHash != root.ContextHash ||
		throughOrdinal != question.ContextThroughOrdinal || answer.ID != root.AnswerID ||
		answer.WorkspaceID != execution.WorkspaceID || answer.ConversationID != root.ConversationID ||
		answer.QuestionID != root.QuestionID || answer.WorkflowRunID != execution.RunID ||
		answer.PublicationStatus != conversationdomain.AnswerPublicationPending {
		return workflowError(
			foundation.ErrorConsistencyViolation,
			ErrorCodeInputInvalid,
			false,
			errors.New("workspace analysis question context differs from the root input"),
		)
	}
	return nil
}

func validateWorkspaceAnalysisRetrieveRun(
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
			foundation.ErrorConsistencyViolation,
			ErrorCodeInputInvalid,
			false,
			errors.New("workspace analysis run differs from the active retrieve execution"),
		)
	}
	return nil
}

func workspaceAnalysisRetrievalRef(
	workspaceID foundation.ID,
	value retrievalapplication.SearchIndex,
) (agentdomain.RetrievalRef, error) {
	if err := retrievaldomain.ValidateIndexVersion(value.Index); err != nil || !validExecutionID(value.Index.ID) ||
		!validExecutionID(value.Index.WorkspaceID) || value.Index.WorkspaceID != workspaceID ||
		value.Index.Status != retrievaldomain.IndexStatusActive {
		return agentdomain.RetrievalRef{}, workflowError(
			foundation.ErrorConsistencyViolation,
			ErrorCodeInputInvalid,
			false,
			errors.New("workspace analysis active search index is invalid"),
		)
	}
	if value.Fusion != nil {
		persisted, err := retrievaldomain.DecodeRRFConfig(value.Index.FusionConfig)
		if err != nil || retrievaldomain.ValidateRRFConfig(*value.Fusion) != nil || persisted != *value.Fusion {
			return agentdomain.RetrievalRef{}, workflowError(
				foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false,
				errors.New("workspace analysis active search fusion binding is invalid"),
			)
		}
	}
	ref := agentdomain.RetrievalRef{IndexVersionID: value.Index.ID}
	if value.Index.EmbeddingVersionID == nil {
		if value.EmbeddingVersion != nil {
			return agentdomain.RetrievalRef{}, workflowError(
				foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false,
				errors.New("workspace analysis fts index returned an embedding version"),
			)
		}
	} else {
		if !validExecutionID(*value.Index.EmbeddingVersionID) || value.Fusion == nil || value.EmbeddingVersion == nil ||
			value.EmbeddingVersion.ID != *value.Index.EmbeddingVersionID ||
			retrievaldomain.ValidateEmbeddingVersion(*value.EmbeddingVersion) != nil {
			return agentdomain.RetrievalRef{}, workflowError(
				foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false,
				errors.New("workspace analysis active embedding binding is invalid"),
			)
		}
		embeddingID := *value.Index.EmbeddingVersionID
		ref.EmbeddingVersionID = &embeddingID
	}
	if err := ref.Validate(); err != nil {
		return agentdomain.RetrievalRef{}, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false, err)
	}
	return ref, nil
}

type workspaceAnalysisPlannerScope struct {
	RetrievalMode        string `json:"retrieval_mode"`
	AllowOriginalSources bool   `json:"allow_original_sources"`
	AllowWeb             bool   `json:"allow_web"`
}

type workspaceAnalysisPlannerInput struct {
	SchemaVersion int                                                    `json:"schema_version"`
	UntrustedData bool                                                   `json:"untrusted_data"`
	Question      string                                                 `json:"question"`
	History       []ragModelHistoryTurn                                  `json:"history"`
	Scope         workspaceAnalysisPlannerScope                          `json:"scope"`
	AnswerDepth   string                                                 `json:"answer_depth"`
	OutputFormat  string                                                 `json:"output_format"`
	GitStatus     conversationworkflow.WorkspaceAnalysisGitStatusSummary `json:"git_status"`
}

func buildWorkspaceAnalysisPlannerInput(
	value conversationapplication.QuestionExecutionContext,
	git conversationworkflow.WorkspaceAnalysisGitStatusSummary,
) ([]byte, error) {
	history := make([]ragModelHistoryTurn, len(value.History))
	for index, turn := range value.History {
		history[index] = ragModelHistoryTurn{
			Ordinal: turn.Ordinal, QuestionText: turn.QuestionText,
			AssistantText: turn.AssistantText, ResultType: turn.ResultType,
		}
	}
	request := value.Question.Request
	payload := workspaceAnalysisPlannerInput{
		SchemaVersion: 1, UntrustedData: true, Question: request.QuestionText, History: history,
		Scope: workspaceAnalysisPlannerScope{
			RetrievalMode:        string(request.Scope.RetrievalMode),
			AllowOriginalSources: request.Scope.AllowOriginalSources, AllowWeb: request.Scope.AllowWeb,
		},
		AnswerDepth: string(request.AnswerDepth), OutputFormat: string(request.OutputFormat), GitStatus: git,
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) == 0 || len(encoded) > agentapplication.MaxStructuredInputBytes || bytes.IndexByte(encoded, 0) >= 0 {
		return nil, workflowError(
			foundation.ErrorNonRetryableFailure,
			ErrorCodeInputInvalid,
			false,
			errors.New("workspace analysis planner input cannot be encoded within its budget"),
		)
	}
	return encoded, nil
}

func validateWorkspaceAnalysisPlannerResult(
	execution workflowapplication.ExecutionContext,
	analysisRun agentdomain.WorkspaceAnalysisRun,
	predecessor conversationworkflow.WorkspaceAnalysisInspectOutput,
	request agentapplication.WorkspaceAnalysisRetrievalPlanRequest,
	result agentapplication.WorkspaceAnalysisRetrievalPlanResult,
) error {
	canonicalPlan, encodeErr := json.Marshal(result.Plan)
	digest := sha256.Sum256(canonicalPlan)
	planHash := hex.EncodeToString(digest[:])
	run := result.Run
	call := result.Call
	modelResult := result.ModelResult
	validAttempt := result.Replayed || run.NodeAttemptID == execution.NodeAttemptID
	if result.Plan.Validate() != nil || agentdomain.ValidateModelRun(run) != nil || agentdomain.ValidateModelCall(call) != nil ||
		agentdomain.ValidateWorkspaceAnalysisModelResult(modelResult) != nil || encodeErr != nil || !validAttempt ||
		result.Plan.ModelRunRef != run.ID || run.WorkspaceID != execution.WorkspaceID || run.WorkflowRunID != execution.RunID ||
		run.NodeRunID != execution.NodeRunID || !sameWorkspaceAnalysisOptionalInt64(run.ModelSettingsRevision, request.ModelSettingsRevision) ||
		run.Profile != request.ProfileRef || run.Prompt != request.PromptRef ||
		run.Schema != (agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisPlanSchemaID, Version: agentdomain.OutputSchemaVersionV1}) ||
		run.ReducedSchema != run.Schema || !sameWorkspaceAnalysisRetrievalRef(run.Retrieval, request.Retrieval) ||
		run.Status != agentdomain.ModelRunSucceeded || run.FinalResultType != agentdomain.ResultTypeWorkspaceAnalysisPlan || run.FinalErrorCode != "" ||
		call.ModelRunID != run.ID || call.CallNo != 1 || call.Phase != agentdomain.ModelCallPlan || call.Model != run.Model ||
		call.Profile != run.Profile || call.Prompt != run.Prompt || call.Schema != run.Schema ||
		call.MaxOutputTokens != int(agentapplication.WorkspaceAnalysisV1PlanMaxOutputTokens) || call.Status != agentdomain.ModelCallSucceeded ||
		call.ResponseHash != planHash || call.ResponseBytes != int64(len(canonicalPlan)) || result.Usage != call.Usage ||
		modelResult.WorkspaceID != execution.WorkspaceID || modelResult.AnalysisRunID != analysisRun.ID ||
		modelResult.NodeAttemptID != run.NodeAttemptID || modelResult.ModelRunID != run.ID || modelResult.ModelCallID != call.ID ||
		modelResult.OperationKind != agentdomain.WorkspaceAnalysisOperationRetrievalPlan || modelResult.Schema != run.Schema ||
		modelResult.DocumentHash != planHash || modelResult.DocumentBytes != int64(len(canonicalPlan)) ||
		!bytes.Equal(modelResult.Document, canonicalPlan) || modelResult.CreatedAt.Before(*run.CompletedAt) ||
		modelResult.CreatedAt.Before(*call.CompletedAt) || plannerResultReusesPredecessorID(predecessor, result) {
		return workflowError(
			foundation.ErrorConsistencyViolation,
			ErrorCodeOutputInvalid,
			false,
			errors.New("workspace analysis retrieval plan authority binding is invalid"),
		)
	}
	return nil
}

func validateWorkspaceAnalysisRetrievalPlanCheckpointResult(
	checkpoint agentapplication.WorkspaceAnalysisRetrievalPlanCheckpoint,
	result agentapplication.WorkspaceAnalysisRetrievalPlanResult,
) error {
	valid := result.ModelResult.OperationID == checkpoint.OperationID && result.Run.ID == checkpoint.ModelRunID &&
		result.Call.ID == checkpoint.ModelCallID && result.Call.RequestHash == checkpoint.RequestHash &&
		sameWorkspaceAnalysisRetrievalRef(result.Run.Retrieval, checkpoint.Retrieval)
	if checkpoint.ModelResultID != "" {
		valid = valid && result.Replayed && result.ModelResult.ID == checkpoint.ModelResultID &&
			result.ModelResult.DocumentHash == checkpoint.ResultHash
	}
	if !valid {
		return workflowError(
			foundation.ErrorConsistencyViolation,
			ErrorCodeOutputInvalid,
			false,
			errors.New("workspace analysis retrieval plan checkpoint differs from the replayed result"),
		)
	}
	return nil
}

func plannerResultReusesPredecessorID(
	predecessor conversationworkflow.WorkspaceAnalysisInspectOutput,
	result agentapplication.WorkspaceAnalysisRetrievalPlanResult,
) bool {
	for _, id := range []foundation.ID{result.Run.ID, result.Call.ID, result.ModelResult.ID} {
		if id == predecessor.ToolCallID || id == predecessor.ToolReceiptID {
			return true
		}
	}
	return false
}

func workspaceAnalysisSearchArguments(
	question conversationdomain.Question,
	plan agentdomain.WorkspaceAnalysisPlanResult,
) (json.RawMessage, error) {
	if err := plan.Validate(); err != nil || plan.Payload.RequiresClarification || len(plan.Payload.Rewrites) == 0 {
		return nil, workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false,
			errors.New("workspace analysis retrieval plan has no searchable rewrite"),
		)
	}
	canonical, err := retrievaldomain.CanonicalizeSearchRequest(retrievaldomain.SearchRequest{
		WorkspaceID: question.Request.WorkspaceID,
		Query:       plan.Payload.Rewrites[0],
		Mode:        question.Request.Scope.RetrievalMode,
		Limit:       5,
	})
	if err != nil {
		return nil, err
	}
	arguments, err := json.Marshal(struct {
		Query string                     `json:"query"`
		Mode  retrievaldomain.SearchMode `json:"mode"`
		Limit int32                      `json:"limit"`
	}{Query: canonical.Query, Mode: canonical.Mode, Limit: canonical.Limit})
	if err != nil {
		return nil, workflowError(foundation.ErrorNonRetryableFailure, ErrorCodeInputInvalid, false, err)
	}
	return arguments, nil
}

func workspaceAnalysisRetrieveExecutionResult(
	execution workflowapplication.ExecutionContext,
	requestedMode retrievaldomain.SearchMode,
	predecessor conversationworkflow.WorkspaceAnalysisInspectOutput,
	planner agentapplication.WorkspaceAnalysisRetrievalPlanResult,
	plannerRetrieval agentdomain.RetrievalRef,
	arguments json.RawMessage,
	result toolsapplication.ToolExecutionResult,
	receipt toolsdomain.ResultReceipt,
) (workflowapplication.ExecutionResult, error) {
	call := result.Call
	outputHash := sha256.Sum256(result.Output)
	requestDocument, requestErr := workspaceAnalysisSearchRequestDocument(arguments)
	requestHash := sha256.Sum256(requestDocument)
	validAttempt := result.Replayed || call.NodeAttemptID == execution.NodeAttemptID
	if err := toolsdomain.ValidateToolCall(call); err != nil || requestErr != nil || !validAttempt ||
		call.Status != toolsdomain.CallSucceeded || call.WorkspaceID != execution.WorkspaceID ||
		call.WorkflowRunID != execution.RunID || call.NodeRunID != execution.NodeRunID || call.CallNo != 1 ||
		call.RequestedToolName != workspaceAnalysisSearchRef.Name || call.Tool == nil || *call.Tool != workspaceAnalysisSearchRef ||
		call.InputSchema == nil || *call.InputSchema != workspaceAnalysisSearchInputSchema ||
		call.OutputSchema == nil || *call.OutputSchema != workspaceAnalysisSearchOutputSchema ||
		call.Capability != capability.ReadLocal || call.SideEffectLevel != toolsdomain.SideEffectNone ||
		call.InvocationPolicy != toolsdomain.InvocationTrustedWorkflowOnly || call.IdempotencyKey != "" ||
		call.RequestHash != hex.EncodeToString(requestHash[:]) || call.RequestBytes != int64(len(requestDocument)) ||
		!validExecutionID(result.ResultReceiptID) || result.ResultReceiptID == call.ID || !result.UntrustedData ||
		call.ResponseBytes != int64(len(result.Output)) || call.ResponseHash != hex.EncodeToString(outputHash[:]) {
		if err == nil {
			err = errors.New("workspace analysis search result binding is invalid")
		}
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, err,
		)
	}
	if err := validateWorkspaceAnalysisRetrieveSearchReceipt(result, receipt, plannerRetrieval); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	summary, err := conversationworkflow.DecodeWorkspaceAnalysisRetrieveSearchSummary(result.Output)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if !workspaceAnalysisSearchModeMatches(requestedMode, summary.EffectiveMode) {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false,
			errors.New("workspace analysis search effective mode differs from the requested mode"),
		)
	}
	output, err := conversationworkflow.EncodeWorkspaceAnalysisRetrieveEvidenceOutput(
		conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput{
			SchemaVersion:          conversationworkflow.WorkspaceAnalysisOutputSchemaVersion,
			GitToolCallID:          predecessor.ToolCallID,
			GitToolReceiptID:       predecessor.ToolReceiptID,
			GitToolReceiptHash:     predecessor.ToolReceiptHash,
			PlannerModelRunID:      planner.Run.ID,
			PlannerModelCallID:     planner.Call.ID,
			PlannerModelResultID:   planner.ModelResult.ID,
			PlannerModelResultHash: planner.ModelResult.DocumentHash,
			SearchToolCallID:       call.ID,
			SearchToolReceiptID:    result.ResultReceiptID,
			SearchToolReceiptHash:  call.ResponseHash,
			SearchSummary:          summary,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workflowapplication.ExecutionResult{Output: output}, nil
}

func validateWorkspaceAnalysisRetrieveSearchReceipt(
	result toolsapplication.ToolExecutionResult,
	receipt toolsdomain.ResultReceipt,
	plannerRetrieval agentdomain.RetrievalRef,
) error {
	if plannerRetrieval.Validate() != nil || receipt.ID != result.ResultReceiptID || receipt.ToolCallID != result.Call.ID ||
		receipt.WorkspaceID != result.Call.WorkspaceID || receipt.WorkflowRunID != result.Call.WorkflowRunID ||
		receipt.NodeRunID != result.Call.NodeRunID || receipt.NodeAttemptID != result.Call.NodeAttemptID ||
		receipt.Tool != workspaceAnalysisSearchRef || receipt.OutputSchema != workspaceAnalysisSearchOutputSchema ||
		receipt.DefinitionHash != result.Call.DefinitionHash || !bytes.Equal(receipt.Output, result.Output) ||
		receipt.OutputHash != result.Call.ResponseHash || receipt.OutputBytes != result.Call.ResponseBytes {
		return workflowError(
			foundation.ErrorConsistencyViolation,
			ErrorCodeOutputInvalid,
			false,
			errors.New("workspace analysis persisted search receipt differs from tool execution result"),
		)
	}
	indexVersionID, found, err := toolsdomain.SearchKnowledgeV2ReceiptIndexVersionID(receipt)
	if err != nil {
		return workflowError(foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, err)
	}
	if found && indexVersionID != plannerRetrieval.IndexVersionID {
		return workflowError(
			foundation.ErrorConsistencyViolation,
			ErrorCodeOutputInvalid,
			false,
			errors.New("workspace analysis search receipt index differs from the frozen planner retrieval"),
		)
	}
	// SearchKnowledge@2 has no index field for zero hits. This is safe only because read_evidence
	// deterministically terminates evidence-insufficient before any answer synthesis can begin.
	return nil
}

func workspaceAnalysisSearchRequestDocument(arguments json.RawMessage) ([]byte, error) {
	request := struct {
		SchemaVersion int                   `json:"schema_version"`
		ToolName      string                `json:"tool_name"`
		ToolVersion   int64                 `json:"tool_version"`
		InputSchema   toolsdomain.SchemaRef `json:"input_schema"`
		Arguments     json.RawMessage       `json:"arguments"`
		Reason        string                `json:"reason"`
	}{
		SchemaVersion: toolsdomain.ToolRequestSchemaVersionV1,
		ToolName:      workspaceAnalysisSearchRef.Name, ToolVersion: workspaceAnalysisSearchRef.Version,
		InputSchema: workspaceAnalysisSearchInputSchema, Arguments: append(json.RawMessage(nil), arguments...),
		Reason: workspaceAnalysisRetrieveReason,
	}
	return json.Marshal(request)
}

func workspaceAnalysisSearchModeMatches(requested retrievaldomain.SearchMode, effective string) bool {
	switch requested {
	case retrievaldomain.SearchModeKeyword:
		return effective == string(retrievaldomain.SearchModeKeyword)
	case retrievaldomain.SearchModeSemantic:
		return effective == string(retrievaldomain.SearchModeSemantic)
	case retrievaldomain.SearchModeHybrid:
		return effective == string(retrievaldomain.SearchModeHybrid) || effective == string(retrievaldomain.SearchModeKeyword)
	default:
		return false
	}
}

func sameWorkspaceAnalysisOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameWorkspaceAnalysisRetrievalRef(left, right agentdomain.RetrievalRef) bool {
	if left.IndexVersionID != right.IndexVersionID || left.RerankModelVersion != right.RerankModelVersion {
		return false
	}
	if left.EmbeddingVersionID == nil || right.EmbeddingVersionID == nil {
		return left.EmbeddingVersionID == nil && right.EmbeddingVersionID == nil
	}
	return *left.EmbeddingVersionID == *right.EmbeddingVersionID
}

func cloneWorkspaceAnalysisInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

var _ workflowapplication.Executor = (*WorkspaceAnalysisRetrieveExecutor)(nil)
var _ WorkspaceAnalysisRetrievalPlanRunner = (*agentapplication.WorkspaceAnalysisRetrievalPlanRunner)(nil)
