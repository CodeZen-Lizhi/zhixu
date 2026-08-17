package workflow

import (
	"bytes"
	"context"
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

type workspaceAnalysisReviewRunner interface {
	Run(context.Context, agentapplication.WorkspaceAnalysisReviewRequest) (agentapplication.WorkspaceAnalysisReviewResult, error)
}

// WorkspaceAnalysisReviewPublishExecutorDependencies 是 review_publish 的全部权威读取、Review 与发布边界。
type WorkspaceAnalysisReviewPublishExecutorDependencies struct {
	Context    conversationapplication.QuestionExecutionContextLoader
	Runs       agentapplication.WorkspaceAnalysisRunLoader
	Inputs     workspaceAnalysisRunInputReader
	Stages     workspaceAnalysisStageOutputReader
	Candidates agentapplication.WorkspaceAnalysisCandidateAuthorityReader
	Evidence   toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityReader
	Validation toolsapplication.ValidateCitationV3PublicationAuthorityReader
	Review     workspaceAnalysisReviewRunner
	Finalizer  conversationapplication.WorkspaceAnalysisFinalizer
	Clock      foundation.Clock
}

// WorkspaceAnalysisReviewPublishExecutor 在完整 Citation 闭包上执行一次 Review 并原子发布 Answer。
type WorkspaceAnalysisReviewPublishExecutor struct {
	dependencies WorkspaceAnalysisReviewPublishExecutorDependencies
	clock        foundation.Clock
}

// NewWorkspaceAnalysisReviewPublishExecutor 创建只接受 workspace-analysis@1 review_publish 的 Executor。
func NewWorkspaceAnalysisReviewPublishExecutor(
	dependencies WorkspaceAnalysisReviewPublishExecutorDependencies,
) (*WorkspaceAnalysisReviewPublishExecutor, error) {
	if nilDependency(dependencies.Context) || nilDependency(dependencies.Runs) || nilDependency(dependencies.Inputs) ||
		nilDependency(dependencies.Stages) || nilDependency(dependencies.Candidates) || nilDependency(dependencies.Evidence) ||
		nilDependency(dependencies.Validation) || nilDependency(dependencies.Review) || nilDependency(dependencies.Finalizer) {
		return nil, workflowError(
			foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("workspace analysis review publication dependencies are unavailable"),
		)
	}
	clock := dependencies.Clock
	if nilDependency(clock) {
		clock = foundation.SystemClock{}
	}
	return &WorkspaceAnalysisReviewPublishExecutor{dependencies: dependencies, clock: clock}, nil
}

// Execute 先恢复既有发布，再从同 Run 不可变事实重建 Review 输入和最终发布证明。
func (executor *WorkspaceAnalysisReviewPublishExecutor) Execute(
	ctx context.Context,
	execution workflowapplication.ExecutionContext,
) (workflowapplication.ExecutionResult, error) {
	if executor == nil || nilDependency(executor.dependencies.Context) || nilDependency(executor.dependencies.Runs) ||
		nilDependency(executor.dependencies.Inputs) || nilDependency(executor.dependencies.Stages) ||
		nilDependency(executor.dependencies.Candidates) || nilDependency(executor.dependencies.Evidence) ||
		nilDependency(executor.dependencies.Validation) || nilDependency(executor.dependencies.Review) ||
		nilDependency(executor.dependencies.Finalizer) {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("workspace analysis review publication executor is unavailable"),
		)
	}
	if ctx == nil {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false,
			errors.New("workspace analysis review publication context is nil"),
		)
	}
	if err := validateWorkspaceAnalysisReviewPublishExecution(execution); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	nodeStartedAt, err := workspaceAnalysisExecutionStartedAt(executor.clock)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}

	predecessor, err := conversationworkflow.DecodeWorkspaceAnalysisValidationOutput(execution.Input)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	authoritativeValidationRaw, err := executor.dependencies.Stages.GetSucceededNodeOutput(
		ctx, execution.WorkspaceID, execution.RunID, conversationworkflow.WorkspaceAnalysisNodeValidateCitations,
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if !bytes.Equal(execution.Input, authoritativeValidationRaw) {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(
			errors.New("workspace analysis validation predecessor differs from its succeeded output"),
		)
	}
	authoritativeValidation, err := conversationworkflow.DecodeWorkspaceAnalysisValidationOutput(authoritativeValidationRaw)
	if err != nil || !reflect.DeepEqual(predecessor, authoritativeValidation) {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(
			workspaceAnalysisReviewCause(err, "workspace analysis validation predecessor is invalid"),
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
	lookup := workspaceAnalysisPublicationLookup(execution, root, analysisRun.ID)
	if published, found, lookupErr := executor.dependencies.Finalizer.LookupPublication(ctx, lookup); lookupErr != nil {
		return workflowapplication.ExecutionResult{}, lookupErr
	} else if found {
		return workspaceAnalysisPublicationExecutionResult(root.AnswerID, published)
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
		ctx, nodeStartedAt, analysisRun.DeadlineAt, deadlines.ReviewPublishDeadline(),
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, finalizeWorkspaceAnalysisPreAuthorizationDeadlineError(
			ctx, executor.dependencies.Finalizer, execution, root,
			questionContext.Answer.Version, analysisRun.ID, err,
		)
	}
	defer cancel()

	inspect, synthesis, retrieve, read, err := executor.loadWorkspaceAnalysisReviewPredecessors(nodeContext, execution)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if predecessor.CandidateID != synthesis.CandidateID || predecessor.CandidateHash != synthesis.CandidateHash {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(
			errors.New("workspace analysis validation and synthesis candidate bindings differ"),
		)
	}
	candidate, err := executor.dependencies.Candidates.LoadWorkspaceAnalysisCandidateAuthority(
		nodeContext,
		agentapplication.WorkspaceAnalysisCandidateAuthorityQuery{
			WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, AnalysisRunID: analysisRun.ID,
			CandidateID: synthesis.CandidateID, CandidateHash: synthesis.CandidateHash,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if err := validateWorkspaceAnalysisReviewCandidate(candidate, synthesis, analysisRun, root); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	evidenceAuthority, err := executor.dependencies.Evidence.LoadWorkspaceAnalysisSynthesisEvidence(
		nodeContext,
		toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityQuery{
			WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, AnalysisRunID: analysisRun.ID,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	retrieval, reviewEvidence, candidateRefs, err := validateWorkspaceAnalysisReviewEvidenceAuthority(
		execution, analysisRun, candidate, retrieve, read, evidenceAuthority,
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	validationAuthority, err := executor.dependencies.Validation.LoadValidateCitationV3PublicationAuthority(
		nodeContext,
		toolsapplication.ValidateCitationV3PublicationAuthorityQuery{
			WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, AnalysisRunID: analysisRun.ID,
			CandidateID: candidate.ID, CandidateHash: candidate.DocumentHash,
			ReceiptID: predecessor.ValidationToolReceiptID, ReceiptHash: predecessor.ValidationToolReceiptHash,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	results, err := toolsdomain.ValidateCitationV3ReceiptResults(
		validationAuthority.Receipt, candidate.ID, candidate.DocumentHash, candidateRefs,
	)
	if err != nil || !sameWorkspaceAnalysisValidationResults(predecessor.Results, results) {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(
			workspaceAnalysisReviewCause(err, "workspace analysis validation receipt differs from its node output"),
		)
	}
	if predecessor.ValidationToolCallID != validationAuthority.Receipt.ToolCallID ||
		predecessor.ValidationToolReceiptID != validationAuthority.Receipt.ID ||
		predecessor.ValidationToolReceiptHash != validationAuthority.Receipt.OutputHash {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(
			errors.New("workspace analysis validation publication authority drifted"),
		)
	}

	if workspaceAnalysisValidationRejected(results) {
		operationID := validationAuthority.OperationID
		published, finalizeErr := finalizeWorkspaceAnalysisTerminationPublication(
			nodeContext,
			executor.dependencies.Finalizer,
			conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
				WorkspaceAnalysisPublicationLookup: lookup,
				ExpectedAnswerVersion:              questionContext.Answer.Version,
				Reason:                             agentdomain.WorkspaceAnalysisRunCitationInvalid,
				OperationID:                        &operationID,
				Artifact: &conversationapplication.WorkspaceAnalysisTerminationArtifact{
					Kind: conversationapplication.WorkspaceAnalysisTerminationArtifactToolReceipt,
					ID:   validationAuthority.Receipt.ID, Hash: validationAuthority.Receipt.OutputHash,
				},
			},
		)
		if finalizeErr != nil {
			return workflowapplication.ExecutionResult{}, finalizeErr
		}
		return workspaceAnalysisPublicationExecutionResult(root.AnswerID, published)
	}

	review, err := executor.dependencies.Review.Run(
		nodeContext,
		agentapplication.WorkspaceAnalysisReviewRequest{
			Identity: workspaceAnalysisModelIdentity(execution), AnalysisRunID: analysisRun.ID,
			Candidate: candidate, Evidence: reviewEvidence,
			ModelSettingsRevision: cloneWorkspaceAnalysisInt64(execution.ModelSettingsRevision),
			Retrieval:             retrieval, ProfileRef: DefaultProfileRef(), PromptRef: FaithfulnessReviewPromptRef(),
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, finalizeWorkspaceAnalysisModelTerminalError(
			nodeContext, executor.dependencies.Finalizer, execution, root,
			questionContext.Answer.Version, analysisRun.ID, err,
		)
	}
	if err := validateWorkspaceAnalysisReviewResult(execution, analysisRun, candidate, review); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if !review.Passed {
		operationID := review.ModelResult.OperationID
		published, finalizeErr := finalizeWorkspaceAnalysisTerminationPublication(
			nodeContext,
			executor.dependencies.Finalizer,
			conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
				WorkspaceAnalysisPublicationLookup: lookup,
				ExpectedAnswerVersion:              questionContext.Answer.Version,
				Reason:                             agentdomain.WorkspaceAnalysisRunFaithfulnessRejected,
				OperationID:                        &operationID,
				Artifact: &conversationapplication.WorkspaceAnalysisTerminationArtifact{
					Kind: conversationapplication.WorkspaceAnalysisTerminationArtifactModelResult,
					ID:   review.ModelResult.ID, Hash: review.ModelResult.DocumentHash,
				},
			},
		)
		if finalizeErr != nil {
			return workflowapplication.ExecutionResult{}, finalizeErr
		}
		return workspaceAnalysisPublicationExecutionResult(root.AnswerID, published)
	}

	published, err := finalizeWorkspaceAnalysisSuccessPublication(
		nodeContext,
		executor.dependencies.Finalizer,
		conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand{
			WorkspaceAnalysisPublicationLookup: lookup,
			ExpectedAnswerVersion:              questionContext.Answer.Version,
			CandidateID:                        candidate.ID, CandidateHash: candidate.DocumentHash,
			GitReceiptID: inspect.ToolReceiptID, GitReceiptHash: inspect.ToolReceiptHash,
			ValidationReceiptID:   validationAuthority.Receipt.ID,
			ValidationReceiptHash: validationAuthority.Receipt.OutputHash,
			ReviewModelResultID:   review.ModelResult.ID, ReviewModelResultHash: review.ModelResult.DocumentHash,
		},
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workspaceAnalysisPublicationExecutionResult(root.AnswerID, published)
}

func (executor *WorkspaceAnalysisReviewPublishExecutor) loadWorkspaceAnalysisReviewPredecessors(
	ctx context.Context,
	execution workflowapplication.ExecutionContext,
) (
	conversationworkflow.WorkspaceAnalysisInspectOutput,
	conversationworkflow.WorkspaceAnalysisSynthesisOutput,
	conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput,
	conversationworkflow.WorkspaceAnalysisReadEvidenceOutput,
	error,
) {
	inspectRaw, err := executor.dependencies.Stages.GetSucceededNodeOutput(
		ctx, execution.WorkspaceID, execution.RunID, conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace,
	)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisInspectOutput{}, conversationworkflow.WorkspaceAnalysisSynthesisOutput{},
			conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput{}, conversationworkflow.WorkspaceAnalysisReadEvidenceOutput{}, err
	}
	inspect, err := conversationworkflow.DecodeWorkspaceAnalysisInspectOutput(inspectRaw)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisInspectOutput{}, conversationworkflow.WorkspaceAnalysisSynthesisOutput{},
			conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput{}, conversationworkflow.WorkspaceAnalysisReadEvidenceOutput{}, err
	}
	retrieveRaw, err := executor.dependencies.Stages.GetSucceededNodeOutput(
		ctx, execution.WorkspaceID, execution.RunID, conversationworkflow.WorkspaceAnalysisNodeRetrieveEvidence,
	)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisInspectOutput{}, conversationworkflow.WorkspaceAnalysisSynthesisOutput{},
			conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput{}, conversationworkflow.WorkspaceAnalysisReadEvidenceOutput{}, err
	}
	retrieve, err := conversationworkflow.DecodeWorkspaceAnalysisRetrieveEvidenceOutput(retrieveRaw)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisInspectOutput{}, conversationworkflow.WorkspaceAnalysisSynthesisOutput{},
			conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput{}, conversationworkflow.WorkspaceAnalysisReadEvidenceOutput{}, err
	}
	readRaw, err := executor.dependencies.Stages.GetSucceededNodeOutput(
		ctx, execution.WorkspaceID, execution.RunID, conversationworkflow.WorkspaceAnalysisNodeReadEvidence,
	)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisInspectOutput{}, conversationworkflow.WorkspaceAnalysisSynthesisOutput{},
			conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput{}, conversationworkflow.WorkspaceAnalysisReadEvidenceOutput{}, err
	}
	read, err := conversationworkflow.DecodeWorkspaceAnalysisReadEvidenceOutput(readRaw)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisInspectOutput{}, conversationworkflow.WorkspaceAnalysisSynthesisOutput{},
			conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput{}, conversationworkflow.WorkspaceAnalysisReadEvidenceOutput{}, err
	}
	synthesisRaw, err := executor.dependencies.Stages.GetSucceededNodeOutput(
		ctx, execution.WorkspaceID, execution.RunID, conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer,
	)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisInspectOutput{}, conversationworkflow.WorkspaceAnalysisSynthesisOutput{},
			conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput{}, conversationworkflow.WorkspaceAnalysisReadEvidenceOutput{}, err
	}
	synthesis, err := conversationworkflow.DecodeWorkspaceAnalysisSynthesisOutput(synthesisRaw)
	if err != nil {
		return conversationworkflow.WorkspaceAnalysisInspectOutput{}, conversationworkflow.WorkspaceAnalysisSynthesisOutput{},
			conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput{}, conversationworkflow.WorkspaceAnalysisReadEvidenceOutput{}, err
	}
	if inspect.ToolCallID != retrieve.GitToolCallID || inspect.ToolReceiptID != retrieve.GitToolReceiptID ||
		inspect.ToolReceiptHash != retrieve.GitToolReceiptHash {
		return conversationworkflow.WorkspaceAnalysisInspectOutput{}, conversationworkflow.WorkspaceAnalysisSynthesisOutput{},
			conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput{}, conversationworkflow.WorkspaceAnalysisReadEvidenceOutput{},
			workspaceAnalysisReceiptError(errors.New("workspace analysis Git predecessor chain drifted"))
	}
	return inspect, synthesis, retrieve, read, nil
}

func validateWorkspaceAnalysisReviewPublishExecution(execution workflowapplication.ExecutionContext) error {
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	node, found := workspaceAnalysisNode(definition, conversationworkflow.WorkspaceAnalysisNodeReviewPublish)
	identity := workspaceAnalysisModelIdentity(execution)
	if !found || execution.DefinitionVersion != definition.Version || execution.DefinitionHash != definition.GraphHash ||
		execution.NodeKey != node.Key || execution.NodeKind != node.Kind || execution.InputSchemaVersion != node.InputSchemaVersion ||
		!validExecutionID(execution.WorkspaceID) || !validExecutionID(execution.DefinitionID) || !validExecutionID(execution.RunID) ||
		!validExecutionID(execution.NodeRunID) || !validExecutionID(execution.NodeAttemptID) || execution.NodeVersion < 1 ||
		execution.AttemptNo < 1 || execution.DispatchNo < 1 || execution.RetryNo < 0 || strings.TrimSpace(execution.LeaseOwner) == "" ||
		(execution.ModelSettingsRevision != nil && *execution.ModelSettingsRevision < 0) || identity.Validate() != nil {
		return workflowError(
			foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false,
			errors.New("workspace analysis review publication execution binding is invalid"),
		)
	}
	return nil
}

func workspaceAnalysisPublicationLookup(
	execution workflowapplication.ExecutionContext,
	root conversationworkflow.WorkspaceAnalysisInput,
	analysisRunID foundation.ID,
) conversationapplication.WorkspaceAnalysisPublicationLookup {
	return conversationapplication.WorkspaceAnalysisPublicationLookup{
		AnswerPublicationLookup: conversationapplication.AnswerPublicationLookup{
			WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
			NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
			ConversationID: root.ConversationID, QuestionID: root.QuestionID, AnswerID: root.AnswerID,
		},
		AnalysisRunID:      analysisRunID,
		ExpectedLeaseOwner: execution.LeaseOwner,
		ExpectedLeaseFence: int64(execution.AttemptNo),
	}
}

func validateWorkspaceAnalysisReviewCandidate(
	candidate agentdomain.WorkspaceAnalysisCandidate,
	synthesis conversationworkflow.WorkspaceAnalysisSynthesisOutput,
	run agentdomain.WorkspaceAnalysisRun,
	root conversationworkflow.WorkspaceAnalysisInput,
) error {
	if agentdomain.ValidateWorkspaceAnalysisCandidate(candidate) != nil || candidate.ID != synthesis.CandidateID ||
		candidate.DocumentHash != synthesis.CandidateHash || candidate.SynthesisModelRunID != synthesis.SynthesisModelRunID ||
		candidate.WorkspaceID != run.WorkspaceID || candidate.AnalysisRunID != run.ID || candidate.AnswerID != root.AnswerID {
		return workspaceAnalysisReceiptError(errors.New("workspace analysis candidate authority binding is invalid"))
	}
	return nil
}

func validateWorkspaceAnalysisReviewEvidenceAuthority(
	execution workflowapplication.ExecutionContext,
	run agentdomain.WorkspaceAnalysisRun,
	candidate agentdomain.WorkspaceAnalysisCandidate,
	retrieve conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput,
	read conversationworkflow.WorkspaceAnalysisReadEvidenceOutput,
	authority toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority,
) (agentdomain.RetrievalRef, []agentapplication.WorkspaceAnalysisReviewEvidence, []string, error) {
	if authority.WorkspaceID != execution.WorkspaceID || authority.WorkflowRunID != execution.RunID ||
		authority.AnalysisRunID != run.ID || authority.SearchReceipt.ID != retrieve.SearchToolReceiptID ||
		authority.SearchReceipt.ToolCallID != retrieve.SearchToolCallID ||
		authority.SearchReceipt.OutputHash != retrieve.SearchToolReceiptHash ||
		len(authority.EvidenceRefs) != len(authority.ReadSourceReceipts) ||
		len(authority.EvidenceRefs) != len(authority.Evidence) || len(authority.EvidenceRefs) != len(read.Reads) {
		return agentdomain.RetrievalRef{}, nil, nil, workspaceAnalysisReceiptError(
			errors.New("workspace analysis review evidence authority binding is invalid"),
		)
	}
	for index, item := range read.Reads {
		if item.EvidenceRef != authority.EvidenceRefs[index] ||
			item.ToolCallID != authority.ReadSourceReceipts[index].ToolCallID ||
			item.ToolReceiptID != authority.ReadSourceReceipts[index].ID ||
			item.ToolReceiptHash != authority.ReadSourceReceipts[index].OutputHash ||
			authority.Evidence[index].EvidenceRef != item.EvidenceRef {
			return agentdomain.RetrievalRef{}, nil, nil, workspaceAnalysisReceiptError(
				errors.New("workspace analysis review source receipt binding is invalid"),
			)
		}
	}
	limits := agentdomain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(agentdomain.MaxWorkspaceAnalysisCandidateBytes)
	decoded, err := agentdomain.DecodeWorkspaceAnalysisCandidate(candidate.Document, limits)
	if err != nil || decoded.ModelRunRef != candidate.SynthesisModelRunID {
		return agentdomain.RetrievalRef{}, nil, nil, workspaceAnalysisReceiptError(
			workspaceAnalysisReviewCause(err, "workspace analysis review candidate document is invalid"),
		)
	}
	candidateRefs := append([]string(nil), decoded.Payload.CitationRefs...)
	evidenceByRef := make(map[string]toolsdomain.ReadSourceV3ReceiptEvidence, len(authority.Evidence))
	for _, evidence := range authority.Evidence {
		if _, duplicate := evidenceByRef[evidence.EvidenceRef]; duplicate {
			return agentdomain.RetrievalRef{}, nil, nil, workspaceAnalysisReceiptError(
				errors.New("workspace analysis review evidence reference is duplicated"),
			)
		}
		evidenceByRef[evidence.EvidenceRef] = evidence
	}
	reviewEvidence := make([]agentapplication.WorkspaceAnalysisReviewEvidence, len(candidateRefs))
	for index, reference := range candidateRefs {
		evidence, found := evidenceByRef[reference]
		if !found {
			return agentdomain.RetrievalRef{}, nil, nil, workspaceAnalysisReceiptError(
				errors.New("workspace analysis candidate reference has no opened evidence"),
			)
		}
		reviewEvidence[index] = agentapplication.WorkspaceAnalysisReviewEvidence{
			EvidenceRef: reference, Excerpt: evidence.Excerpt,
		}
	}
	indexVersionID, found, err := toolsdomain.SearchKnowledgeV2ReceiptIndexVersionID(authority.SearchReceipt)
	if err != nil || !found {
		return agentdomain.RetrievalRef{}, nil, nil, workspaceAnalysisReceiptError(
			workspaceAnalysisReviewCause(err, "workspace analysis review search index binding is missing"),
		)
	}
	retrieval := agentdomain.RetrievalRef{IndexVersionID: indexVersionID}
	if err := retrieval.Validate(); err != nil {
		return agentdomain.RetrievalRef{}, nil, nil, workspaceAnalysisReceiptError(err)
	}
	return retrieval, reviewEvidence, candidateRefs, nil
}

func sameWorkspaceAnalysisValidationResults(
	want []conversationworkflow.WorkspaceAnalysisCitationValidationResult,
	got []toolsdomain.ValidateCitationV3ReceiptResult,
) bool {
	if len(want) != len(got) {
		return false
	}
	for index := range want {
		if want[index].EvidenceRef != got[index].EvidenceRef || want[index].Valid != got[index].Valid ||
			want[index].ReasonCode != got[index].ReasonCode {
			return false
		}
	}
	return true
}

func workspaceAnalysisValidationRejected(results []toolsdomain.ValidateCitationV3ReceiptResult) bool {
	for _, result := range results {
		if !result.Valid {
			return true
		}
	}
	return false
}

func validateWorkspaceAnalysisReviewResult(
	execution workflowapplication.ExecutionContext,
	run agentdomain.WorkspaceAnalysisRun,
	candidate agentdomain.WorkspaceAnalysisCandidate,
	result agentapplication.WorkspaceAnalysisReviewResult,
) error {
	if result.Passed != result.Review.Payload.Passed || agentdomain.ValidateWorkspaceAnalysisModelResult(result.ModelResult) != nil ||
		result.ModelResult.WorkspaceID != execution.WorkspaceID || result.ModelResult.AnalysisRunID != run.ID ||
		result.ModelResult.SubjectCandidateID == nil || *result.ModelResult.SubjectCandidateID != candidate.ID ||
		result.ModelResult.SubjectCandidateHash != candidate.DocumentHash || result.ModelResult.ModelRunID != result.Run.ID ||
		result.ModelResult.ModelCallID != result.Call.ID || result.ModelResult.DocumentHash != result.Call.ResponseHash ||
		result.Run.WorkflowRunID != execution.RunID || result.Run.NodeRunID != execution.NodeRunID ||
		(!result.Replayed && result.Run.NodeAttemptID != execution.NodeAttemptID) ||
		result.Run.Status != agentdomain.ModelRunSucceeded || result.Call.Status != agentdomain.ModelCallSucceeded {
		return workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false,
			errors.New("workspace analysis review result binding is invalid"),
		)
	}
	return nil
}

func workspaceAnalysisPublicationExecutionResult(
	answerID foundation.ID,
	publication conversationworkflow.WorkspaceAnalysisPublicationOutput,
) (workflowapplication.ExecutionResult, error) {
	if publication.AnswerID != answerID {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false,
			errors.New("workspace analysis publication answer binding is invalid"),
		)
	}
	output, err := conversationworkflow.EncodeWorkspaceAnalysisPublicationOutput(publication)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workflowapplication.ExecutionResult{Output: output}, nil
}

func workspaceAnalysisReviewCause(err error, message string) error {
	if err != nil {
		return errors.Join(errors.New(message), err)
	}
	return errors.New(message)
}

var _ workflowapplication.Executor = (*WorkspaceAnalysisReviewPublishExecutor)(nil)
