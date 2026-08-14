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
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	ragFinalizeTimeout             = 5 * time.Second
	ragMaxAgentIterations          = 4
	ragMaxAgentToolCalls           = 2
	ragRuntimeMaxInputTokens int64 = 64 * 1024
	// RAGModelCallLimit covers PLAN, the maximum Agent rounds, ANSWER, the
	// INITIAL/REPAIR/REDUCED structured phases, and REVIEW. Keep this as the
	// single source used by both the Attempt ledger and its time budget.
	RAGModelCallLimit = 1 + ragMaxAgentIterations + 5
	// MaxRAGAttemptTimeout bounds the complete multi-call RAG Attempt while
	// each Provider call remains bounded by its frozen profile timeout.
	MaxRAGAttemptTimeout = time.Hour
)

// RAGAttemptTimeout derives the complete Attempt budget from the frozen
// single-call timeout without widening any individual Provider call.
func RAGAttemptTimeout(modelCallTimeout time.Duration) time.Duration {
	if modelCallTimeout <= 0 {
		return 0
	}
	if modelCallTimeout >= MaxRAGAttemptTimeout/time.Duration(RAGModelCallLimit) {
		return MaxRAGAttemptTimeout
	}
	return modelCallTimeout * time.Duration(RAGModelCallLimit)
}

// RAGWorkflowExecutorDependencies 是 RAG Workflow 节点的全部真实依赖。
type RAGWorkflowExecutorDependencies struct {
	Model        agentapplication.ChatModel
	Scheduler    agentapplication.StructuredPhaseScheduler
	RAGScheduler agentapplication.RAGExecutionScheduler
	Catalog      *agentapplication.RuntimeCatalog
	Repository   agentapplication.ModelRunRepository
	Snapshots    agentapplication.RAGMemorySnapshotRepository
	Memory       agentapplication.EffectiveMemoryLoader
	MemoryOwner  agentapplication.MemoryOwnerRef
	Context      conversationapplication.QuestionExecutionContextLoader
	Search       agentapplication.ScopedRetrievalPort
	Retrieval    agentapplication.RetrievalPort
	Eligibility  agentapplication.EvidenceEligibilityPort
	Topics       agentapplication.RAGEvidenceTopicPort
	Finalizer    conversationapplication.AnswerFinalizer
	Progress     agentapplication.RAGProgressRecorder
	IDs          foundation.IDGenerator
	Clock        foundation.Clock
	Budget       agentapplication.RunBudget
	Metrics      observability.Metrics

	AgentRuntime  agentapplication.AgentRuntime
	AnswerStream  agentapplication.AnswerStreamRuntime
	ToolContracts *toolsapplication.Registry
	ToolExecution RAGAgentExecutionService
	DraftStreams  agentapplication.DraftStreamStore
}

// RAGWorkflowExecutor 把冻结 Conversation 事实编排为一个原子发布命令。
type RAGWorkflowExecutor struct {
	dependencies RAGWorkflowExecutorDependencies
}

// NewRAGWorkflowExecutor 创建 retrieval-first、Eino Agent/Tool/Stream 接入的 RAG Workflow Executor。
func NewRAGWorkflowExecutor(dependencies RAGWorkflowExecutorDependencies) (*RAGWorkflowExecutor, error) {
	if nilDependency(dependencies.Model) || dependencies.Catalog == nil || nilDependency(dependencies.Repository) ||
		nilDependency(dependencies.Scheduler) || nilDependency(dependencies.RAGScheduler) ||
		nilDependency(dependencies.Snapshots) || nilDependency(dependencies.Memory) ||
		!validExecutionID(dependencies.MemoryOwner.ID) || dependencies.MemoryOwner.Kind == "" ||
		nilDependency(dependencies.Context) || nilDependency(dependencies.Search) || nilDependency(dependencies.Retrieval) ||
		nilDependency(dependencies.Eligibility) || nilDependency(dependencies.Topics) || nilDependency(dependencies.Finalizer) ||
		nilDependency(dependencies.Progress) || nilDependency(dependencies.IDs) || nilDependency(dependencies.Clock) {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("rag workflow dependencies are incomplete"))
	}
	if dependencies.Budget == (agentapplication.RunBudget{}) {
		dependencies.Budget = agentapplication.DefaultRunBudget()
	}
	if _, err := agentapplication.NewStructuredRunnerWithScheduler(dependencies.Model, dependencies.Catalog, dependencies.Budget, dependencies.Scheduler); err != nil {
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
	definition, definitionErr := resolveRAGExecutionDefinition(execution.DefinitionVersion)
	if definitionErr != nil || execution.NodeKind != RAGWorkflowNodeKind || execution.NodeKey != RAGWorkflowNodeKey ||
		execution.InputSchemaVersion != RAGWorkflowInputSchemaVersion || !validExecutionID(execution.WorkspaceID) ||
		!validExecutionID(execution.DefinitionID) || !validExecutionID(execution.RunID) ||
		!validExecutionID(execution.NodeRunID) || !validExecutionID(execution.NodeAttemptID) ||
		execution.DefinitionHash != definition.GraphHash || execution.NodeVersion < 1 || execution.AttemptNo < 1 ||
		execution.DispatchNo < 1 || execution.RetryNo < 0 || execution.LeaseOwner == "" {
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
	snapshotID, err := executor.dependencies.IDs.New()
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	claimantID, err := executor.dependencies.IDs.New()
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	snapshotStartedAt := executor.dependencies.Clock.Now().UTC().Truncate(time.Microsecond)
	snapshot, claimed, err := executor.dependencies.Snapshots.BeginRAGMemorySnapshot(ctx, agentdomain.RAGMemorySnapshot{
		ID: snapshotID, WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID, ClaimantID: claimantID,
		OwnerKind: executor.dependencies.MemoryOwner.Kind, OwnerID: executor.dependencies.MemoryOwner.ID,
		TaskScopeID: input.ConversationID, Status: agentdomain.RAGMemorySnapshotPreparing,
		CreatedAt: snapshotStartedAt, UpdatedAt: snapshotStartedAt,
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if !claimed {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorManualRecoveryRequired, agentapplication.ErrorCodeMemorySnapshotConflict, false, errors.New("rag memory snapshot already exists for this node attempt"))
	}
	memoryQuery := agentapplication.EffectiveMemoryQuery{
		WorkspaceID: execution.WorkspaceID, Owner: executor.dependencies.MemoryOwner,
		TaskScopeID: input.ConversationID, Limit: agentdomain.MaxRAGMemoryContextItems,
	}
	memoryItems, err := executor.dependencies.Memory.Load(ctx, memoryQuery)
	if err != nil {
		return workflowapplication.ExecutionResult{}, executor.failRAGMemorySnapshot(ctx, snapshot, err)
	}
	memoryContext, err := agentapplication.BuildMemoryContextSnapshot(memoryQuery, memoryItems)
	if err != nil {
		return workflowapplication.ExecutionResult{}, executor.failRAGMemorySnapshot(ctx, snapshot, err)
	}
	planInput, answerInput, err := buildRAGModelInputs(executionContext, memoryContext.Context)
	if err != nil {
		return workflowapplication.ExecutionResult{}, executor.failRAGMemorySnapshot(ctx, snapshot, err)
	}
	run, err := executor.prepareRAGModelRun(execution, agentdomain.RAGMemoryContextRef{
		SnapshotID: snapshot.ID, SchemaVersion: memoryContext.SchemaVersion, Digest: memoryContext.Digest,
		ItemCount: memoryContext.ItemCount, ByteCount: memoryContext.ByteCount,
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, executor.failRAGMemorySnapshot(ctx, snapshot, err)
	}
	run, err = executor.dependencies.Snapshots.FinalizeRAGMemorySnapshotAndCreateModelRun(ctx, agentapplication.FinalizeRAGMemorySnapshotCommand{
		SnapshotID: snapshot.ID, ClaimantID: snapshot.ClaimantID, Context: run.MemoryContext, Run: run,
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorManualRecoveryRequired, ErrorCodeRunFinalizationUnknown, false, err)
	}
	proposal, draft, executeErr, unknown := executor.executeRun(ctx, execution, run, executionContext, planInput, answerInput)
	if executeErr != nil {
		if finalizeErr := executor.bestEffortFinalizeRAG(ctx, run, executeErr, unknown); finalizeErr != nil {
			return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorManualRecoveryRequired, ErrorCodeRunFinalizationUnknown, false, finalizeErr)
		}
		return workflowapplication.ExecutionResult{}, executeErr
	}
	receipt, replayed, err := executor.dependencies.Finalizer.Finalize(ctx, conversationapplication.FinalizeAnswerCommand{
		AnswerPublicationLookup: lookup, ModelRunID: run.ID,
		ExpectedAnswerVersion: executionContext.Answer.Version, ExpectedModelRunVersion: run.Version,
		Proposal: proposal, Draft: draft,
	})
	if err != nil {
		receipt, replayed, err = executor.recoverRAGFinalization(ctx, execution, lookup, run, input.AnswerID, draft, err)
		if err != nil {
			return workflowapplication.ExecutionResult{}, err
		}
	}
	if receipt.AnswerID != input.AnswerID || receipt.ModelRunID != run.ID {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, errors.New("finalized rag receipt differs from workflow execution"))
	}
	if execution.DefinitionVersion == conversationworkflow.DefinitionVersionV2 && !replayed {
		recordRAGOutcomeMetric(ctx, executor.dependencies.Metrics, string(receipt.PublicationStatus))
	}
	return encodeRAGExecutionResult(receipt)
}

func (executor *RAGWorkflowExecutor) recoverRAGFinalization(
	ctx context.Context,
	execution workflowapplication.ExecutionContext,
	lookup conversationapplication.AnswerPublicationLookup,
	run agentdomain.ModelRun,
	answerID foundation.ID,
	draft *conversationapplication.AnswerDraftTerminalBinding,
	cause error,
) (conversationworkflow.OutputReceipt, bool, error) {
	recoveryContext, cancelRecovery := context.WithTimeout(context.WithoutCancel(ctxOrBackground(ctx)), ragFinalizeTimeout)
	receipt, found, lookupErr := executor.dependencies.Finalizer.Lookup(recoveryContext, lookup)
	cancelRecovery()
	if lookupErr != nil {
		return conversationworkflow.OutputReceipt{}, false, workflowError(
			foundation.ErrorManualRecoveryRequired,
			ErrorCodeRunFinalizationUnknown,
			false,
			errors.Join(cause, lookupErr),
		)
	}
	if found {
		return receipt, true, nil
	}

	cleanupErr := executor.cleanupFailedRAGFinalization(ctx, execution, run, answerID, draft, cause)
	return conversationworkflow.OutputReceipt{}, false, workflowError(
		foundation.ErrorManualRecoveryRequired,
		ErrorCodeRunFinalizationUnknown,
		false,
		errors.Join(cause, cleanupErr),
	)
}

func (executor *RAGWorkflowExecutor) cleanupFailedRAGFinalization(
	ctx context.Context,
	execution workflowapplication.ExecutionContext,
	run agentdomain.ModelRun,
	answerID foundation.ID,
	draft *conversationapplication.AnswerDraftTerminalBinding,
	cause error,
) error {
	var abortErr error
	if draft != nil {
		if nilDependency(executor.dependencies.DraftStreams) {
			abortErr = workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("rag draft stream store is unavailable during finalization recovery"))
		} else {
			abortContext, cancelAbort := context.WithTimeout(context.WithoutCancel(ctxOrBackground(ctx)), ragFinalizeTimeout)
			_, abortErr = executor.dependencies.DraftStreams.AbortDraftStream(abortContext, agentapplication.DraftStreamTransitionCommand{
				SessionID: draft.SessionID,
				Binding: agentapplication.DraftStreamBinding{
					WorkspaceID: execution.WorkspaceID, AnswerID: answerID, WorkflowRunID: execution.RunID,
					NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
					AttemptNo: draft.AttemptNo, LeaseOwner: draft.LeaseOwner,
				},
			})
			cancelAbort()
		}
	}
	runErr := executor.bestEffortFinalizeRAG(ctxOrBackground(ctx), run, cause, false)
	return errors.Join(abortErr, runErr)
}

func recordRAGOutcomeMetric(ctx context.Context, metrics observability.Metrics, outcome string) {
	defer func() { _ = recover() }()
	if metrics == nil {
		return
	}
	labels, err := observability.NewLabels(map[string]string{"outcome": outcome})
	if err != nil {
		return
	}
	measurement, err := observability.NewMeasurement(observability.MetricRAGOutcomeTotal, observability.MetricKindCounter, 1, labels)
	if err != nil {
		return
	}
	_ = metrics.Record(ctx, measurement)
}

func (executor *RAGWorkflowExecutor) prepareRAGModelRun(execution workflowapplication.ExecutionContext, memoryContext agentdomain.RAGMemoryContextRef) (agentdomain.ModelRun, error) {
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
	now := executor.dependencies.Clock.Now().UTC().Truncate(time.Microsecond)
	run := agentdomain.ModelRun{
		ID: runID, WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
		ModelSettingsRevision: cloneOptionalInt64(execution.ModelSettingsRevision),
		Model:                 snapshot.Profile.Model, Profile: snapshot.Profile.Ref, Prompt: snapshot.Prompt.Ref,
		Schema: snapshot.Schema.Ref, ReducedSchema: snapshot.ReducedSchema.Ref,
		MemoryContext: memoryContext,
		Status:        agentdomain.ModelRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := agentdomain.ValidateModelRun(run); err != nil {
		return agentdomain.ModelRun{}, err
	}
	return run, nil
}

func (executor *RAGWorkflowExecutor) failRAGMemorySnapshot(ctx context.Context, snapshot agentdomain.RAGMemorySnapshot, cause error) error {
	code := errorCode(cause)
	if code == "" {
		code = agentapplication.ErrorCodeMemoryContextUnavailable
	}
	failureContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), ragFinalizeTimeout)
	defer cancel()
	_, err := executor.dependencies.Snapshots.FailRAGMemorySnapshot(failureContext, agentapplication.FailRAGMemorySnapshotCommand{
		SnapshotID: snapshot.ID, WorkspaceID: snapshot.WorkspaceID, ClaimantID: snapshot.ClaimantID, ErrorCode: code,
	})
	if err != nil {
		return workflowError(foundation.ErrorManualRecoveryRequired, ErrorCodeRunFinalizationUnknown, false, errors.Join(cause, err))
	}
	return cause
}

func (executor *RAGWorkflowExecutor) executeRun(
	ctx context.Context,
	execution workflowapplication.ExecutionContext,
	run agentdomain.ModelRun,
	executionContext conversationapplication.QuestionExecutionContext,
	planInput, answerInput []byte,
) (agentapplication.RAGTerminalProposal, *conversationapplication.AnswerDraftTerminalBinding, error, bool) {
	if execution.DefinitionVersion == conversationworkflow.DefinitionVersionV1 {
		proposal, err, unknown := executor.executeRunV1(ctx, run, executionContext, planInput, answerInput)
		return proposal, nil, err, unknown
	}
	if execution.DefinitionVersion == conversationworkflow.DefinitionVersionV2 {
		return executor.executeRunV2(ctx, execution, run, executionContext, planInput, answerInput)
	}
	return agentapplication.RAGTerminalProposal{}, nil, workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("rag workflow definition version is unsupported")), false
}

func (executor *RAGWorkflowExecutor) executeRunV1(
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
	runner, err := agentapplication.NewStructuredRunnerWithScheduler(recorded, executor.dependencies.Catalog, executor.dependencies.Budget, executor.dependencies.Scheduler)
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
	rag, err := agentapplication.NewRAGExecutorWithScheduler(
		planner, executor.dependencies.Search, executor.dependencies.Eligibility, executor.dependencies.Topics,
		runner, publisher, progress, executor.dependencies.RAGScheduler,
	)
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

func (executor *RAGWorkflowExecutor) executeRunV2(
	ctx context.Context,
	execution workflowapplication.ExecutionContext,
	run agentdomain.ModelRun,
	executionContext conversationapplication.QuestionExecutionContext,
	planInput, answerInput []byte,
) (agentapplication.RAGTerminalProposal, *conversationapplication.AnswerDraftTerminalBinding, error, bool) {
	if nilDependency(executor.dependencies.AgentRuntime) || nilDependency(executor.dependencies.AnswerStream) ||
		executor.dependencies.ToolContracts == nil || nilRAGAgentExecutionService(executor.dependencies.ToolExecution) || nilDependency(executor.dependencies.DraftStreams) {
		return agentapplication.RAGTerminalProposal{}, nil, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("rag v2 eino runtime dependencies are unavailable")), false
	}
	metadataSnapshot, err := executor.dependencies.Catalog.Snapshot(
		RAGAnswerMetadataPromptRef(),
		agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		DefaultProfileRef(),
	)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, err, false
	}
	attemptTimeout := RAGAttemptTimeout(metadataSnapshot.Profile.Timeout)
	now := executor.dependencies.Clock.Now()
	deadline := now.Add(attemptTimeout)
	attemptContext, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
	defer cancelAttempt()
	phaseReservation := agentapplication.RunBudgetReservation{
		ModelCalls: 1, ReservedInputTokens: ragRuntimeMaxInputTokens,
		ReservedOutputTokens: int64(metadataSnapshot.Profile.MaxOutputTokens),
	}
	reservations := make(map[agentapplication.RunBudgetPhase]agentapplication.RunBudgetReservation, 5)
	for _, phase := range []agentapplication.RunBudgetPhase{
		agentapplication.RunBudgetPhaseAnswer, agentapplication.RunBudgetPhaseInitial, agentapplication.RunBudgetPhaseRepair,
		agentapplication.RunBudgetPhaseReduced, agentapplication.RunBudgetPhaseReview,
	} {
		reservations[phase] = phaseReservation
	}
	ledger, err := agentapplication.NewRunBudgetLedger(agentapplication.RunBudgetLedgerConfig{
		NodeAttemptID: run.NodeAttemptID, ModelRunID: run.ID,
		MaxTotalModelCalls: RAGModelCallLimit, MaxAgentIterations: ragMaxAgentIterations,
		MaxToolCalls: ragMaxAgentToolCalls, MaxInputTokens: agentapplication.MaxRunTokens,
		MaxOutputTokens: agentapplication.MaxRunTokens, Deadline: deadline, Clock: executor.dependencies.Clock,
		DownstreamReservations: reservations,
	})
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, err, false
	}
	recorder, err := agentapplication.NewModelCallRecorder(agentapplication.ModelCallRecorderDependencies{
		Repository: executor.dependencies.Repository, WorkspaceID: run.WorkspaceID, ModelRunID: run.ID,
		IDs: executor.dependencies.IDs, Clock: executor.dependencies.Clock,
	})
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, err, false
	}
	recorded, err := agentapplication.NewRecordingChatModel(agentapplication.RecordingChatModelDependencies{
		Model: executor.dependencies.Model, WorkspaceID: run.WorkspaceID, ModelRunID: run.ID,
		Recorder: recorder, BudgetLedger: ledger, MaxInputTokens: ragRuntimeMaxInputTokens,
	})
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, err, false
	}
	planner, err := agentapplication.NewQueryPlanner(recorded, executor.dependencies.Catalog)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, err, false
	}
	metadata, err := agentapplication.NewStructuredRunnerWithScheduler(recorded, executor.dependencies.Catalog, executor.dependencies.Budget, executor.dependencies.Scheduler)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, err, false
	}
	reviewer, err := agentapplication.NewStructuredFaithfulnessReviewer(recorded, executor.dependencies.Catalog, executor.dependencies.Budget)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, err, false
	}
	bridge, err := NewRAGAgentToolBridge(execution, executor.dependencies.ToolContracts, executor.dependencies.ToolExecution)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, err, false
	}
	draft, err := beginRAGDraftStream(attemptContext, executor.dependencies.DraftStreams, agentapplication.DraftStreamBinding{
		WorkspaceID: run.WorkspaceID, AnswerID: executionContext.Answer.ID, WorkflowRunID: execution.RunID,
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
		AttemptNo: execution.AttemptNo, LeaseOwner: execution.LeaseOwner,
	})
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, err, false
	}
	generation, err := NewRAGAgentGeneration(RAGAgentGenerationDependencies{
		Agent: executor.dependencies.AgentRuntime, AnswerStream: executor.dependencies.AnswerStream, Metadata: metadata,
		Tools: bridge, Budget: ledger, Recorder: recorder, Sink: draft,
		Model: run.Model, MaxIterations: ragMaxAgentIterations, MaxInputTokens: ragRuntimeMaxInputTokens,
		MaxOutputTokens: metadataSnapshot.Profile.MaxOutputTokens,
	})
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, abortRAGDraft(attemptContext, draft, err), false
	}
	citations, err := agentapplication.NewCitationValidator(executor.dependencies.Retrieval, executor.dependencies.Eligibility)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, abortRAGDraft(attemptContext, draft, err), false
	}
	publisher, err := agentapplication.NewAnswerPublisher(citations, reviewer)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, abortRAGDraft(attemptContext, draft, err), false
	}
	progress := boundRAGProgressRecorder{
		recorder: executor.dependencies.Progress,
		record: agentapplication.RAGProgressRecord{
			WorkspaceID: run.WorkspaceID, WorkflowRunID: run.WorkflowRunID,
			ConversationID: executionContext.Question.Request.ConversationID,
			QuestionID:     executionContext.Question.ID, AnswerID: executionContext.Answer.ID, ModelRunID: run.ID,
		},
		clock: executor.dependencies.Clock,
	}
	rag, err := agentapplication.NewRAGExecutorWithGenerationScheduler(
		planner, executor.dependencies.Search, executor.dependencies.Eligibility, executor.dependencies.Topics,
		generation, publisher, progress, executor.dependencies.RAGScheduler,
	)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, abortRAGDraft(attemptContext, draft, err), false
	}
	request := executionContext.Question.Request
	proposal, err := rag.Execute(attemptContext, agentapplication.RAGExecutionRequest{
		WorkspaceID: run.WorkspaceID, ModelRunRef: run.ID, PlanInput: planInput, AnswerInput: answerInput,
		SearchMode: request.Scope.RetrievalMode, Filter: request.Scope.Filter,
		AllowOriginalSources: request.Scope.AllowOriginalSources, AllowWeb: request.Scope.AllowWeb,
		PlanProfileRef: DefaultProfileRef(), PlanPromptRef: QueryPlanProviderPromptRef(),
		PlanSchemaRef:    agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		AnswerProfileRef: DefaultProfileRef(), AnswerPromptRef: RAGAnswerMetadataPromptRef(),
		AnswerSchemaRef:        agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		AnswerReducedSchemaRef: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		ReviewProfileRef:       DefaultProfileRef(), ReviewPromptRef: FaithfulnessReviewPromptRef(),
		ReviewSchemaRef: agentdomain.SchemaRef{ID: agentdomain.FaithfulnessReviewSchemaID, Version: agentdomain.OutputSchemaVersionV1},
	})
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, abortRAGDraft(attemptContext, draft, err), errorCode(err) == agentapplication.ErrorCodeModelCallPersistenceUnknown
	}
	if proposal.Answer == nil {
		sealContext, cancelSeal := context.WithTimeout(context.WithoutCancel(attemptContext), ragFinalizeTimeout)
		session, sealErr := draft.SealForTerminal(sealContext)
		cancelSeal()
		if sealErr != nil {
			return agentapplication.RAGTerminalProposal{}, nil, abortRAGDraft(attemptContext, draft, sealErr), false
		}
		terminalDraft := &conversationapplication.AnswerDraftTerminalBinding{
			SessionID: session.ID, Generation: session.Generation,
			AttemptNo: session.Binding.AttemptNo, LeaseOwner: session.Binding.LeaseOwner,
		}
		return proposal, terminalDraft, nil, false
	}
	session, err := completeRAGDraft(attemptContext, draft)
	if err != nil {
		return agentapplication.RAGTerminalProposal{}, nil, err, false
	}
	publication := &conversationapplication.AnswerDraftTerminalBinding{
		SessionID: session.ID, Generation: session.Generation,
		AttemptNo: session.Binding.AttemptNo, LeaseOwner: session.Binding.LeaseOwner,
	}
	return proposal, publication, nil, false
}

func completeRAGDraft(ctx context.Context, draft *ragDraftStreamSink) (agentapplication.DraftStreamSession, error) {
	finishContext, cancel := context.WithTimeout(context.WithoutCancel(ctxOrBackground(ctx)), ragFinalizeTimeout)
	defer cancel()
	session, err := draft.Complete(finishContext)
	if err != nil {
		return agentapplication.DraftStreamSession{}, abortRAGDraft(ctx, draft, err)
	}
	return session, nil
}

func abortRAGDraft(ctx context.Context, draft *ragDraftStreamSink, cause error) error {
	if draft == nil {
		return cause
	}
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctxOrBackground(ctx)), ragFinalizeTimeout)
	defer cancel()
	return errors.Join(cause, draft.Abort(finalizeContext))
}

func resolveRAGExecutionDefinition(version int64) (definition workflowdomain.RegisteredDefinition, err error) {
	switch version {
	case conversationworkflow.DefinitionVersionV1:
		return conversationworkflow.RegisteredDefinitionV1(), nil
	case conversationworkflow.DefinitionVersionV2:
		return conversationworkflow.RegisteredDefinitionV2(), nil
	default:
		return workflowdomain.RegisteredDefinition{}, errors.New("unsupported rag workflow definition")
	}
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
