package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

type workspaceAnalysisJournalReader interface {
	LoadWorkspaceAnalysisJournal(context.Context, agentapplication.WorkspaceAnalysisJournalQuery) (agentapplication.WorkspaceAnalysisJournalSnapshot, error)
}

type workspaceAnalysisDecisionBinder interface {
	BindWorkspaceAnalysisLoop(agentapplication.WorkspaceAnalysisDecisionRunRequest) (agentapplication.WorkspaceAnalysisLoopModelCaller, error)
}

// WorkspaceAnalysisV2ExecutorDependencies binds the four v2 stages to the same
// durable authorities. No executor stores an Eino checkpoint or provider state.
type WorkspaceAnalysisV2ExecutorDependencies struct {
	Context     conversationapplication.QuestionExecutionContextLoader
	Runs        agentapplication.WorkspaceAnalysisRunLoader
	Inputs      workspaceAnalysisRunInputReader
	Stages      workspaceAnalysisStageOutputReader
	Journal     workspaceAnalysisJournalReader
	Catalog     *agentapplication.RuntimeCatalog
	Decisions   workspaceAnalysisDecisionBinder
	Runtime     agentapplication.WorkspaceAnalysisLoopRuntime
	Tools       workspaceAnalysisToolExecutor
	ToolOutputs toolsapplication.WorkspaceAnalysisDynamicToolAuthorityReader
	Evidence    toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityReader
	Candidates  agentapplication.WorkspaceAnalysisCandidateAuthorityReader
	Validation  toolsapplication.ValidateCitationV4PublicationAuthorityReader
	Synthesis   workspaceAnalysisSynthesisRunner
	Review      workspaceAnalysisReviewRunner
	Finalizer   conversationapplication.WorkspaceAnalysisFinalizer
	Clock       foundation.Clock
}

// WorkspaceAnalysisV2Executors has one exact registered executor for each node.
type WorkspaceAnalysisV2Executors struct {
	DecideNext        workflowapplication.Executor
	SynthesizeAnswer  workflowapplication.Executor
	ValidateCitations workflowapplication.Executor
	ReviewPublish     workflowapplication.Executor
}

func NewWorkspaceAnalysisV2Executors(dependencies WorkspaceAnalysisV2ExecutorDependencies) (WorkspaceAnalysisV2Executors, error) {
	for _, dependency := range []any{dependencies.Context, dependencies.Runs, dependencies.Inputs, dependencies.Stages,
		dependencies.Journal, dependencies.Catalog, dependencies.Decisions, dependencies.Runtime, dependencies.Tools,
		dependencies.ToolOutputs, dependencies.Evidence, dependencies.Candidates, dependencies.Validation,
		dependencies.Synthesis, dependencies.Review, dependencies.Finalizer} {
		if nilDependency(dependency) {
			return WorkspaceAnalysisV2Executors{}, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
				errors.New("workspace analysis v2 executor dependencies are unavailable"))
		}
	}
	if nilDependency(dependencies.Clock) {
		dependencies.Clock = foundation.SystemClock{}
	}
	build := func(key string) workflowapplication.Executor {
		return &workspaceAnalysisV2NodeExecutor{dependencies: dependencies, nodeKey: key}
	}
	return WorkspaceAnalysisV2Executors{
		DecideNext:        build(conversationworkflow.WorkspaceAnalysisNodeDecideNext),
		SynthesizeAnswer:  build(conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer),
		ValidateCitations: build(conversationworkflow.WorkspaceAnalysisNodeValidateCitations),
		ReviewPublish:     build(conversationworkflow.WorkspaceAnalysisNodeReviewPublish),
	}, nil
}

type workspaceAnalysisV2NodeExecutor struct {
	dependencies WorkspaceAnalysisV2ExecutorDependencies
	nodeKey      string
}

type workspaceAnalysisV2Execution struct {
	execution   workflowapplication.ExecutionContext
	root        conversationworkflow.WorkspaceAnalysisInput
	question    conversationapplication.QuestionExecutionContext
	run         agentdomain.WorkspaceAnalysisRun
	journal     agentapplication.WorkspaceAnalysisJournalSnapshot
	publication *conversationworkflow.WorkspaceAnalysisPublicationOutput
}

func (executor *workspaceAnalysisV2NodeExecutor) Execute(ctx context.Context, execution workflowapplication.ExecutionContext) (workflowapplication.ExecutionResult, error) {
	if executor == nil || ctx == nil || validateWorkspaceAnalysisV2Execution(execution, executor.nodeKey) != nil {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 execution binding is invalid"))
	}
	startedAt, err := workspaceAnalysisExecutionStartedAt(executor.dependencies.Clock)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	state, err := executor.loadExecution(ctx, execution)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if state.publication != nil {
		return workspaceAnalysisV2PublicationResult(state.root.AnswerID, *state.publication)
	}
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV2Deadlines(state.run.Timeouts)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	var timeout time.Duration
	switch executor.nodeKey {
	case conversationworkflow.WorkspaceAnalysisNodeDecideNext:
		timeout = deadlines.DecideNextDeadline()
	case conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer:
		timeout = deadlines.SynthesizeAnswerDeadline()
	case conversationworkflow.WorkspaceAnalysisNodeValidateCitations:
		timeout = deadlines.ValidateCitationsDeadline()
	case conversationworkflow.WorkspaceAnalysisNodeReviewPublish:
		timeout = deadlines.ReviewPublishDeadline()
	}
	nodeContext, cancel, err := workspaceAnalysisNodeContext(ctx, startedAt, state.run.DeadlineAt, timeout)
	if err != nil {
		return workflowapplication.ExecutionResult{}, executor.finalizeError(ctx, state, err)
	}
	defer cancel()
	var result workflowapplication.ExecutionResult
	switch executor.nodeKey {
	case conversationworkflow.WorkspaceAnalysisNodeDecideNext:
		result, err = executor.decideNext(nodeContext, state)
	case conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer:
		result, err = executor.synthesizeAnswer(nodeContext, state)
	case conversationworkflow.WorkspaceAnalysisNodeValidateCitations:
		result, err = executor.validateCitations(nodeContext, state)
	case conversationworkflow.WorkspaceAnalysisNodeReviewPublish:
		result, err = executor.reviewPublish(nodeContext, state)
	}
	if err != nil {
		return workflowapplication.ExecutionResult{}, executor.finalizeError(nodeContext, state, err)
	}
	return result, nil
}

func validateWorkspaceAnalysisV2Execution(execution workflowapplication.ExecutionContext, key string) error {
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2()
	node, found := workspaceAnalysisNode(definition, key)
	if !found || execution.NodeKey != key || execution.NodeKind != node.Kind || execution.DefinitionVersion != definition.Version ||
		execution.DefinitionHash != definition.GraphHash || execution.InputSchemaVersion != node.InputSchemaVersion ||
		execution.NodeVersion < 1 || execution.AttemptNo < 1 || execution.DispatchNo < 1 || execution.RetryNo < 0 ||
		strings.TrimSpace(execution.LeaseOwner) != execution.LeaseOwner ||
		(execution.ModelSettingsRevision != nil && *execution.ModelSettingsRevision < 0) || workspaceAnalysisModelIdentity(execution).Validate() != nil {
		return workspaceAnalysisReceiptError(errors.New("workspace analysis v2 node identity drifted"))
	}
	return nil
}

func (executor *workspaceAnalysisV2NodeExecutor) loadExecution(ctx context.Context, execution workflowapplication.ExecutionContext) (workspaceAnalysisV2Execution, error) {
	state := workspaceAnalysisV2Execution{execution: execution}
	rootRaw, err := executor.dependencies.Inputs.GetRunInput(ctx, execution.WorkspaceID, execution.RunID)
	if err != nil {
		return state, err
	}
	state.root, err = conversationworkflow.DecodeWorkspaceAnalysisInput(rootRaw)
	if err != nil {
		return state, err
	}
	if executor.nodeKey == conversationworkflow.WorkspaceAnalysisNodeDecideNext && !bytes.Equal(rootRaw, execution.Input) {
		return state, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 root input differs from the durable run"))
	}
	root := state.root
	state.run, err = executor.dependencies.Runs.LoadWorkspaceAnalysisRunForExecution(ctx, agentapplication.WorkspaceAnalysisRunExecutionQuery{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, ConversationID: root.ConversationID, QuestionID: root.QuestionID, AnswerID: root.AnswerID,
	})
	if err != nil {
		return state, err
	}
	run := state.run
	if agentdomain.ValidateWorkspaceAnalysisRun(run) != nil || run.WorkspaceID != execution.WorkspaceID || run.WorkflowRunID != execution.RunID ||
		run.ConversationID != root.ConversationID || run.QuestionID != root.QuestionID || run.AnswerID != root.AnswerID ||
		run.DefinitionKey != conversationworkflow.WorkspaceAnalysisDefinitionKey || run.DefinitionVersion != 2 ||
		run.PolicyVersion != agentdomain.WorkspaceAnalysisPolicyVersionV2 || run.DefinitionHash != execution.DefinitionHash {
		return state, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 run binding drifted"))
	}
	publication, found, err := executor.dependencies.Finalizer.LookupPublication(ctx, state.lookup())
	if err != nil {
		return state, err
	}
	if found {
		if publication.SchemaVersion != 2 || publication.AnswerID != root.AnswerID {
			return state, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 publication replay drifted"))
		}
		state.publication = &publication
		return state, nil
	}
	if run.Status != agentdomain.WorkspaceAnalysisRunQueued && run.Status != agentdomain.WorkspaceAnalysisRunRunning {
		return state, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 terminal run has no publication"))
	}
	state.question, err = executor.dependencies.Context.LoadQuestionExecutionContext(ctx, conversationapplication.QuestionExecutionContextQuery{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, ConversationID: root.ConversationID, QuestionID: root.QuestionID, AnswerID: root.AnswerID,
		QuestionOrdinal: root.QuestionOrdinal, ContextHash: root.ContextHash,
	})
	if err != nil {
		return state, err
	}
	if err := validateWorkspaceAnalysisQuestionContext(state.question, execution, root); err != nil {
		return state, err
	}
	state.journal, err = executor.dependencies.Journal.LoadWorkspaceAnalysisJournal(ctx, state.journalQuery())
	if err != nil {
		return state, err
	}
	if err := validateWorkspaceAnalysisV2Journal(state, state.journal); err != nil {
		return state, err
	}
	return state, nil
}

func (state workspaceAnalysisV2Execution) journalQuery() agentapplication.WorkspaceAnalysisJournalQuery {
	return agentapplication.WorkspaceAnalysisJournalQuery{WorkspaceID: state.execution.WorkspaceID, WorkflowRunID: state.execution.RunID, AnalysisRunID: state.run.ID}
}

func (state workspaceAnalysisV2Execution) lookup() conversationapplication.WorkspaceAnalysisPublicationLookup {
	return workspaceAnalysisPublicationLookup(state.execution, state.root, state.run.ID)
}

func validateWorkspaceAnalysisV2Journal(state workspaceAnalysisV2Execution, journal agentapplication.WorkspaceAnalysisJournalSnapshot) error {
	if agentdomain.ValidateWorkspaceAnalysisRun(journal.Run) != nil || journal.Run.ID != state.run.ID ||
		journal.Run.WorkspaceID != state.execution.WorkspaceID || journal.Run.WorkflowRunID != state.execution.RunID ||
		journal.Run.ConversationID != state.root.ConversationID || journal.Run.QuestionID != state.root.QuestionID || journal.Run.AnswerID != state.root.AnswerID ||
		journal.Run.DefinitionVersion != 2 || journal.Run.DefinitionHash != state.execution.DefinitionHash || journal.Run.ToolCatalogHash != state.run.ToolCatalogHash ||
		journal.Run.ConfigRevision != state.run.ConfigRevision || journal.Run.Timeouts != state.run.Timeouts || journal.Run.Limits.Amount.OutputTokens != state.run.Limits.Amount.OutputTokens {
		return workspaceAnalysisReceiptError(errors.New("workspace analysis v2 journal owner drifted"))
	}
	seen := map[foundation.ID]bool{}
	for index, entry := range journal.Entries {
		if entry.Sequence != int64(index+1) || entry.Operation.AnalysisRunID != state.run.ID ||
			agentdomain.ValidateWorkspaceAnalysisOperation(entry.Operation) != nil || seen[entry.Operation.ID] {
			return workspaceAnalysisReceiptError(errors.New("workspace analysis v2 journal order drifted"))
		}
		seen[entry.Operation.ID] = true
	}
	return nil
}

func (executor *workspaceAnalysisV2NodeExecutor) loadStage(ctx context.Context, state workspaceAnalysisV2Execution, key string, predecessor bool) (json.RawMessage, error) {
	raw, err := executor.dependencies.Stages.GetSucceededNodeOutput(ctx, state.execution.WorkspaceID, state.execution.RunID, key)
	if err != nil {
		return nil, err
	}
	if predecessor && !bytes.Equal(raw, state.execution.Input) {
		return nil, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 predecessor differs from durable output"))
	}
	return raw, nil
}

func (executor *workspaceAnalysisV2NodeExecutor) finalizeError(ctx context.Context, state workspaceAnalysisV2Execution, cause error) error {
	if denial, found := agentapplication.WorkspaceAnalysisAdmissionDenialFromError(cause); found {
		command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{WorkspaceAnalysisPublicationLookup: state.lookup(), ExpectedAnswerVersion: state.question.Answer.Version,
			Reason: denial.Reason, OperationID: &denial.OperationID}
		if denial.Reason == agentdomain.WorkspaceAnalysisRunBudgetExhausted {
			command.BudgetRequest = &conversationapplication.WorkspaceAnalysisBudgetRequest{ModelCalls: denial.Requested.ModelCalls,
				ToolCalls: denial.Requested.ToolCalls, SourceReads: denial.Requested.SourceReads, InputTokens: denial.Requested.InputTokens, OutputTokens: denial.Requested.OutputTokens}
		}
		return finalizeWorkspaceAnalysisTerminationError(ctx, executor.dependencies.Finalizer, command, cause)
	}
	if _, found := agentapplication.WorkspaceAnalysisModelTerminalEvidenceFromError(cause); found {
		return finalizeWorkspaceAnalysisModelTerminalError(ctx, executor.dependencies.Finalizer, state.execution, state.root, state.question.Answer.Version, state.run.ID, cause)
	}
	return finalizeWorkspaceAnalysisToolTerminalError(ctx, executor.dependencies.Finalizer, state.execution, state.root, state.question.Answer.Version, state.run.ID, cause)
}

var _ workflowapplication.Executor = (*workspaceAnalysisV2NodeExecutor)(nil)
