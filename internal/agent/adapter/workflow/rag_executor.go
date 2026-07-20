package workflow

import (
	"context"
	"errors"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const ragFinalizeTimeout = 5 * time.Second

// RAGWorkflowExecutorDependencies 是 RAG Workflow 节点的全部真实依赖。
type RAGWorkflowExecutorDependencies struct {
	Model       agentapplication.ChatModel
	Catalog     *agentapplication.RuntimeCatalog
	Repository  agentapplication.ModelRunRepository
	Context     conversationapplication.QuestionExecutionContextLoader
	Search      agentapplication.ScopedRetrievalPort
	Retrieval   agentapplication.RetrievalPort
	Eligibility agentapplication.EvidenceEligibilityPort
	Topics      agentapplication.RAGEvidenceTopicPort
	Finalizer   conversationapplication.AnswerFinalizer
	Progress    agentapplication.RAGProgressRecorder
	IDs         foundation.IDGenerator
	Clock       foundation.Clock
	Budget      agentapplication.RunBudget
}

// RAGWorkflowExecutor 把冻结 Conversation 事实编排为一个原子发布命令。
type RAGWorkflowExecutor struct {
	dependencies RAGWorkflowExecutorDependencies
}

// NewRAGWorkflowExecutor 创建 retrieval-first、无 Tool Loop 的 RAG Workflow Executor。
func NewRAGWorkflowExecutor(dependencies RAGWorkflowExecutorDependencies) (*RAGWorkflowExecutor, error) {
	if nilDependency(dependencies.Model) || dependencies.Catalog == nil || nilDependency(dependencies.Repository) ||
		nilDependency(dependencies.Context) || nilDependency(dependencies.Search) || nilDependency(dependencies.Retrieval) ||
		nilDependency(dependencies.Eligibility) || nilDependency(dependencies.Topics) || nilDependency(dependencies.Finalizer) ||
		nilDependency(dependencies.Progress) || nilDependency(dependencies.IDs) || nilDependency(dependencies.Clock) {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("rag workflow dependencies are incomplete"))
	}
	if dependencies.Budget == (agentapplication.RunBudget{}) {
		dependencies.Budget = agentapplication.DefaultRunBudget()
	}
	if _, err := agentapplication.NewStructuredRunner(dependencies.Model, dependencies.Catalog, dependencies.Budget); err != nil {
		return nil, err
	}
	return &RAGWorkflowExecutor{dependencies: dependencies}, nil
}

// Execute 首先恢复终态回执，否则加载冻结上下文、执行 RAG 并交给 T09 原子发布。
func (executor *RAGWorkflowExecutor) Execute(ctx context.Context, execution workflowapplication.ExecutionContext) (workflowapplication.ExecutionResult, error) {
	if executor == nil {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("rag workflow executor is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if execution.NodeKind != RAGWorkflowNodeKind || execution.InputSchemaVersion != RAGWorkflowInputSchemaVersion ||
		!validExecutionID(execution.WorkspaceID) || !validExecutionID(execution.RunID) ||
		!validExecutionID(execution.NodeRunID) || !validExecutionID(execution.NodeAttemptID) {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("rag workflow execution binding is invalid"))
	}
	input, err := DecodeRAGWorkflowInput(execution.Input)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	lookup := conversationapplication.AnswerPublicationLookup{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, NodeRunID: execution.NodeRunID,
		NodeAttemptID: execution.NodeAttemptID, ConversationID: input.ConversationID,
		QuestionID: input.QuestionID, AnswerID: input.AnswerID,
	}
	if receipt, found, lookupErr := executor.dependencies.Finalizer.Lookup(ctx, lookup); lookupErr != nil {
		return workflowapplication.ExecutionResult{}, lookupErr
	} else if found {
		if receipt.AnswerID != input.AnswerID {
			return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, errors.New("replayed rag receipt differs from workflow input"))
		}
		return encodeRAGExecutionResult(receipt)
	}

	executionContext, err := executor.dependencies.Context.LoadQuestionExecutionContext(ctx, conversationapplication.QuestionExecutionContextQuery{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		ConversationID: input.ConversationID, QuestionID: input.QuestionID, AnswerID: input.AnswerID,
		QuestionOrdinal: input.QuestionOrdinal, ContextHash: input.ContextHash,
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	planInput, answerInput, err := buildRAGModelInputs(executionContext)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}

	run, err := executor.createRAGModelRun(ctx, execution)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	proposal, executeErr, unknown := executor.executeRun(ctx, run, executionContext, planInput, answerInput)
	if executeErr != nil {
		if finalizeErr := executor.bestEffortFinalizeRAG(ctx, run, executeErr, unknown); finalizeErr != nil {
			return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorManualRecoveryRequired, ErrorCodeRunFinalizationUnknown, false, finalizeErr)
		}
		return workflowapplication.ExecutionResult{}, executeErr
	}
	receipt, _, err := executor.dependencies.Finalizer.Finalize(ctx, conversationapplication.FinalizeAnswerCommand{
		AnswerPublicationLookup: lookup, ModelRunID: run.ID,
		ExpectedAnswerVersion: executionContext.Answer.Version, ExpectedModelRunVersion: run.Version,
		Proposal: proposal,
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorManualRecoveryRequired, ErrorCodeRunFinalizationUnknown, false, err)
	}
	if receipt.AnswerID != input.AnswerID || receipt.ModelRunID != run.ID {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, errors.New("finalized rag receipt differs from workflow execution"))
	}
	return encodeRAGExecutionResult(receipt)
}

func (executor *RAGWorkflowExecutor) createRAGModelRun(ctx context.Context, execution workflowapplication.ExecutionContext) (agentdomain.ModelRun, error) {
	profile := DefaultProfileRef()
	answerSchema := agentdomain.SchemaRef{ID: agentdomain.RAGAnswerSchemaID, Version: agentdomain.OutputSchemaVersionV2}
	refusalSchema := agentdomain.SchemaRef{ID: agentdomain.RefusalSchemaID, Version: agentdomain.OutputSchemaVersionV1}
	snapshot, err := executor.dependencies.Catalog.Snapshot(RAGAnswerPromptRef(), answerSchema, refusalSchema, profile)
	if err != nil {
		return agentdomain.ModelRun{}, err
	}
	runID, err := executor.dependencies.IDs.New()
	if err != nil {
		return agentdomain.ModelRun{}, err
	}
	now := executor.dependencies.Clock.Now()
	run := agentdomain.ModelRun{
		ID: runID, WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
		Model: snapshot.Profile.Model, Profile: snapshot.Profile.Ref, Prompt: snapshot.Prompt.Ref,
		Schema: snapshot.Schema.Ref, ReducedSchema: snapshot.ReducedSchema.Ref,
		Status: agentdomain.ModelRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	created, replayed, err := executor.dependencies.Repository.CreateModelRun(ctx, run)
	if err != nil {
		return agentdomain.ModelRun{}, err
	}
	if replayed || created.Status != agentdomain.ModelRunRunning || created.Version != 1 {
		return agentdomain.ModelRun{}, workflowError(foundation.ErrorManualRecoveryRequired, ErrorCodeRunReplayUnsafe, false, errors.New("rag model run cannot be safely replayed"))
	}
	return created, nil
}

func (executor *RAGWorkflowExecutor) executeRun(
	ctx context.Context,
	run agentdomain.ModelRun,
	executionContext conversationapplication.QuestionExecutionContext,
	planInput, answerInput []byte,
) (agentapplication.RAGTerminalProposal, error, bool) {
	recorded, err := agentapplication.NewRecordingChatModel(agentapplication.RecordingChatModelDependencies{
		Model: executor.dependencies.Model, Repository: executor.dependencies.Repository,
		WorkspaceID: run.WorkspaceID, ModelRunID: run.ID, StartingCallNo: 1,
		IDs: executor.dependencies.IDs, Clock: executor.dependencies.Clock,
	})
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, err, false
	}
	planner, err := agentapplication.NewQueryPlanner(recorded, executor.dependencies.Catalog)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, err, false
	}
	runner, err := agentapplication.NewStructuredRunner(recorded, executor.dependencies.Catalog, executor.dependencies.Budget)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, err, false
	}
	reviewer, err := agentapplication.NewStructuredFaithfulnessReviewer(recorded, executor.dependencies.Catalog, executor.dependencies.Budget)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, err, false
	}
	citations, err := agentapplication.NewCitationValidator(executor.dependencies.Retrieval, executor.dependencies.Eligibility)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, err, false
	}
	publisher, err := agentapplication.NewAnswerPublisher(citations, reviewer)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, err, false
	}
	progress := boundRAGProgressRecorder{
		recorder: executor.dependencies.Progress,
		record: agentapplication.RAGProgressRecord{
			WorkspaceID: run.WorkspaceID, WorkflowRunID: run.WorkflowRunID,
			ConversationID: executionContext.Question.Request.ConversationID,
			QuestionID:     executionContext.Question.ID, AnswerID: executionContext.Answer.ID,
			ModelRunID: run.ID,
		},
		clock: executor.dependencies.Clock,
	}
	rag, err := agentapplication.NewRAGExecutor(planner, executor.dependencies.Search, executor.dependencies.Eligibility, executor.dependencies.Topics, runner, publisher, progress)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, err, false
	}
	request := executionContext.Question.Request
	proposal, err := rag.Execute(ctx, agentapplication.RAGExecutionRequest{
		WorkspaceID: run.WorkspaceID, ModelRunRef: run.ID, PlanInput: planInput, AnswerInput: answerInput,
		SearchMode: request.Scope.RetrievalMode, Filter: request.Scope.Filter,
		AllowOriginalSources: request.Scope.AllowOriginalSources, AllowWeb: request.Scope.AllowWeb,
		PlanProfileRef: DefaultProfileRef(), PlanPromptRef: QueryPlanPromptRef(),
		PlanSchemaRef:    agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		AnswerProfileRef: DefaultProfileRef(), AnswerPromptRef: RAGAnswerPromptRef(),
		AnswerSchemaRef:        agentdomain.SchemaRef{ID: agentdomain.RAGAnswerSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		AnswerReducedSchemaRef: agentdomain.SchemaRef{ID: agentdomain.RefusalSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		ReviewProfileRef:       DefaultProfileRef(), ReviewPromptRef: FaithfulnessReviewPromptRef(),
		ReviewSchemaRef: agentdomain.SchemaRef{ID: agentdomain.FaithfulnessReviewSchemaID, Version: agentdomain.OutputSchemaVersionV1},
	})
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, err, errorCode(err) == agentapplication.ErrorCodeModelCallPersistenceUnknown
	}
	return proposal, nil, false
}

func (executor *RAGWorkflowExecutor) bestEffortFinalizeRAG(ctx context.Context, run agentdomain.ModelRun, cause error, unknown bool) error {
	status := agentdomain.ModelRunFailed
	code := errorCode(cause)
	if code == "" {
		code = "AGENT_RAG_WORKFLOW_FAILED"
	}
	if unknown {
		status = agentdomain.ModelRunUnknown
		code = agentapplication.ErrorCodeModelCallPersistenceUnknown
	}
	now := executor.dependencies.Clock.Now()
	if now.Before(run.CreatedAt) {
		now = run.CreatedAt
	}
	terminal := run
	terminal.Status = status
	terminal.FinalErrorCode = code
	terminal.Version++
	terminal.UpdatedAt = now
	terminal.CompletedAt = &now
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), ragFinalizeTimeout)
	defer cancel()
	_, _, err := executor.dependencies.Repository.FinalizeModelRun(finalizeContext, agentapplication.FinalizeModelRunCommand{ExpectedVersion: run.Version, Run: terminal})
	return err
}

func encodeRAGExecutionResult(receipt conversationworkflow.OutputReceipt) (workflowapplication.ExecutionResult, error) {
	encoded, err := EncodeRAGWorkflowOutput(receipt)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workflowapplication.ExecutionResult{Output: encoded}, nil
}

var _ workflowapplication.Executor = (*RAGWorkflowExecutor)(nil)

type boundRAGProgressRecorder struct {
	recorder agentapplication.RAGProgressRecorder
	record   agentapplication.RAGProgressRecord
	clock    foundation.Clock
}

func (recorder boundRAGProgressRecorder) RecordRAGProgress(ctx context.Context, update agentapplication.RAGProgressUpdate) error {
	record := recorder.record
	record.OccurredAt = recorder.clock.Now().UTC()
	record.Update = update
	return recorder.recorder.RecordRAGProgress(ctx, record)
}
