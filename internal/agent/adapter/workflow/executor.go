package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const finalizeTimeout = 5 * time.Second

type evidenceOpener interface {
	Open(context.Context, agentdomain.Citation) (agentapplication.OpenedEvidence, error)
	OpenBatch(context.Context, []agentdomain.Citation) ([]agentapplication.OpenedEvidence, error)
}

// ExecutorDependencies 是 Agent Workflow Executor 的显式依赖。
type ExecutorDependencies struct {
	Model      agentapplication.ChatModel
	Catalog    *agentapplication.RuntimeCatalog
	Repository agentapplication.ModelRunRepository
	Knowledge  agentapplication.RelationKnowledgePort
	Evidence   evidenceOpener
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
	Budget     agentapplication.RunBudget
}

// Executor 把一个 Workflow Node Attempt 绑定到唯一 Model Run。
type Executor struct {
	model      agentapplication.ChatModel
	catalog    *agentapplication.RuntimeCatalog
	repository agentapplication.ModelRunRepository
	knowledge  agentapplication.RelationKnowledgePort
	evidence   evidenceOpener
	ids        foundation.IDGenerator
	clock      foundation.Clock
	budget     agentapplication.RunBudget
}

// NewExecutor 创建严格输入、记录调用且 fail-closed 的 Agent Executor。
func NewExecutor(dependencies ExecutorDependencies) (*Executor, error) {
	if nilDependency(dependencies.Model) || dependencies.Catalog == nil || nilDependency(dependencies.Repository) || nilDependency(dependencies.Knowledge) || nilDependency(dependencies.Evidence) ||
		nilDependency(dependencies.IDs) || nilDependency(dependencies.Clock) {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("agent workflow dependencies are incomplete"))
	}
	if dependencies.Budget == (agentapplication.RunBudget{}) {
		dependencies.Budget = agentapplication.DefaultRunBudget()
	}
	// 复用 Runner 构造器作为预算契约校验，不执行 Provider。
	if _, err := agentapplication.NewStructuredRunner(dependencies.Model, dependencies.Catalog, dependencies.Budget); err != nil {
		return nil, err
	}
	return &Executor{
		model: dependencies.Model, catalog: dependencies.Catalog, repository: dependencies.Repository, knowledge: dependencies.Knowledge, evidence: dependencies.Evidence,
		ids: dependencies.IDs, clock: dependencies.Clock, budget: dependencies.Budget,
	}, nil
}

// Execute 创建 Model Run、执行记录型三阶段调用、校验 Run 引用并持久化唯一终态。
func (executor *Executor) Execute(ctx context.Context, execution workflowapplication.ExecutionContext) (workflowapplication.ExecutionResult, error) {
	if executor == nil || nilDependency(executor.model) || executor.catalog == nil || nilDependency(executor.repository) || nilDependency(executor.knowledge) || nilDependency(executor.evidence) {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("agent workflow executor is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if execution.NodeKind != RelationAssessmentNodeKind || execution.InputSchemaVersion != RelationAssessmentInputSchemaVersion ||
		!validExecutionID(execution.WorkspaceID) || !validExecutionID(execution.RunID) ||
		!validExecutionID(execution.NodeRunID) || !validExecutionID(execution.NodeAttemptID) {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("workflow execution binding is invalid"))
	}
	input, err := DecodeRelationAssessmentWorkflowInput(execution.Input)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	snapshot, err := executor.catalog.Snapshot(input.PromptRef, input.SchemaRef, input.ReducedSchemaRef, input.ProfileRef)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	runID, err := executor.ids.New()
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	now := executor.clock.Now()
	run := agentdomain.ModelRun{
		ID: runID, WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
		Model: snapshot.Profile.Model, Profile: snapshot.Profile.Ref, Prompt: snapshot.Prompt.Ref,
		Schema: snapshot.Schema.Ref, ReducedSchema: snapshot.ReducedSchema.Ref, Retrieval: input.Retrieval,
		Status: agentdomain.ModelRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	created, replayed, err := executor.repository.CreateModelRun(ctx, run)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if replayed || created.Status != agentdomain.ModelRunRunning || created.Version != 1 {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorManualRecoveryRequired, ErrorCodeRunReplayUnsafe, false, errors.New("model run cannot be safely executed again"))
	}
	result, resultErr, unknown := executor.executeCreatedRun(ctx, created, input)
	if resultErr != nil {
		if errorCode(resultErr) == ErrorCodeRunFinalizationUnknown {
			return workflowapplication.ExecutionResult{}, resultErr
		}
		if finalizeErr := executor.bestEffortFinalize(ctx, created, resultErr, unknown); finalizeErr != nil {
			return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorManualRecoveryRequired, ErrorCodeRunFinalizationUnknown, false, finalizeErr)
		}
		return workflowapplication.ExecutionResult{}, resultErr
	}
	return result, nil
}

func (executor *Executor) executeCreatedRun(ctx context.Context, run agentdomain.ModelRun, input RelationAssessmentWorkflowInput) (workflowapplication.ExecutionResult, error, bool) {
	recordedModel, err := agentapplication.NewRecordingChatModel(agentapplication.RecordingChatModelDependencies{
		Model: executor.model, Repository: executor.repository, WorkspaceID: run.WorkspaceID, ModelRunID: run.ID,
		IDs: executor.ids, Clock: executor.clock,
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err, false
	}
	runner, err := agentapplication.NewStructuredRunner(recordedModel, executor.catalog, executor.budget)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err, false
	}
	analyzer, err := agentapplication.NewRelationAnalyzer(runner, executor.knowledge)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err, false
	}
	opened, err := executor.openRelationEvidence(ctx, input)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err, false
	}
	candidate, err := executor.relationClaimApplicationInput(run.WorkspaceID, input.Retrieval, input.Candidate, opened)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err, false
	}
	var existing *agentapplication.RelationClaimInput
	if input.Existing != nil {
		converted, convertErr := executor.relationClaimApplicationInput(run.WorkspaceID, input.Retrieval, *input.Existing, opened)
		if convertErr != nil {
			return workflowapplication.ExecutionResult{}, convertErr, false
		}
		existing = &converted
	}
	analysis, err := analyzer.Analyze(ctx, agentapplication.RelationAnalyzeRequest{
		WorkspaceID: run.WorkspaceID, ModelRunRef: run.ID, Retrieval: input.Retrieval,
		Runtime: agentapplication.StructuredRunRequest{
			ProfileRef: input.ProfileRef, PromptRef: input.PromptRef, SchemaRef: input.SchemaRef,
			ReducedSchemaRef: input.ReducedSchemaRef,
		},
		Candidate: candidate, Existing: existing,
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err, errorCode(err) == agentapplication.ErrorCodeModelCallPersistenceUnknown
	}
	structured := analysis.StructuredRun
	output := RelationAssessmentWorkflowOutput{
		SchemaVersion: RelationAssessmentOutputSchemaVersion, ModelRunRef: run.ID, ResultType: agentdomain.ResultTypeRelationAssessment,
		Phase: structured.Phase, CallCount: structured.CallCount, Usage: structured.Usage,
		SchemaRef: structured.Runtime.Schema, BusinessJSON: append(json.RawMessage(nil), structured.Output...),
		Action: relationActionOutput(analysis.Action),
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorNonRetryableFailure, ErrorCodeOutputInvalid, false, errors.New("agent workflow output could not be encoded")), false
	}
	if _, err := executor.finalize(ctx, run, agentdomain.ModelRunSucceeded, agentdomain.ResultTypeRelationAssessment, ""); err != nil {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorManualRecoveryRequired, ErrorCodeRunFinalizationUnknown, false, err), true
	}
	return workflowapplication.ExecutionResult{Output: encoded}, nil, false
}

func (executor *Executor) openRelationEvidence(ctx context.Context, input RelationAssessmentWorkflowInput) (map[string]agentapplication.OpenedEvidence, error) {
	citations := make([]agentdomain.Citation, 0, len(input.Candidate.Evidence))
	for _, evidence := range input.Candidate.Evidence {
		citations = append(citations, evidence.Citation)
	}
	if input.Existing != nil {
		for _, evidence := range input.Existing.Evidence {
			citations = append(citations, evidence.Citation)
		}
	}
	opened, err := executor.evidence.OpenBatch(ctx, citations)
	if err != nil {
		return nil, err
	}
	if len(opened) != len(citations) {
		return nil, workflowError(foundation.ErrorConsistencyViolation, errorCodeEvidenceOpenInvalid, false, errors.New("opened relation evidence batch is incomplete"))
	}
	byID := make(map[string]agentapplication.OpenedEvidence, len(opened))
	for index, value := range opened {
		if value.Citation != citations[index] || value.Excerpt == "" {
			return nil, workflowError(foundation.ErrorConsistencyViolation, errorCodeEvidenceOpenInvalid, false, errors.New("opened relation evidence differs from citation"))
		}
		if _, duplicate := byID[value.Citation.ID]; duplicate {
			return nil, workflowError(foundation.ErrorConsistencyViolation, errorCodeEvidenceOpenInvalid, false, errors.New("opened relation evidence contains duplicate citation ids"))
		}
		byID[value.Citation.ID] = value
	}
	return byID, nil
}

func (executor *Executor) relationClaimApplicationInput(
	workspaceID foundation.ID,
	retrieval agentdomain.RetrievalRef,
	input RelationClaimInput,
	openedByID map[string]agentapplication.OpenedEvidence,
) (agentapplication.RelationClaimInput, error) {
	applicability, err := knowledgedomain.ParseApplicability(input.Applicability)
	if err != nil {
		return agentapplication.RelationClaimInput{}, workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, err)
	}
	evidence := make([]agentapplication.RelationEvidenceInput, len(input.Evidence))
	for index, item := range input.Evidence {
		if item.Citation.WorkspaceID != workspaceID || item.Citation.IndexVersionID != retrieval.IndexVersionID {
			return agentapplication.RelationClaimInput{}, workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("relation citation differs from workflow scope"))
		}
		opened, exists := openedByID[item.Citation.ID]
		if !exists {
			return agentapplication.RelationClaimInput{}, workflowError(foundation.ErrorConsistencyViolation, errorCodeEvidenceOpenInvalid, false, errors.New("opened relation evidence is missing"))
		}
		if opened.Citation != item.Citation || opened.Excerpt == "" {
			return agentapplication.RelationClaimInput{}, workflowError(foundation.ErrorConsistencyViolation, errorCodeEvidenceOpenInvalid, false, errors.New("opened relation evidence differs from citation"))
		}
		evidence[index] = agentapplication.RelationEvidenceInput{Citation: item.Citation, Excerpt: opened.Excerpt}
	}
	return agentapplication.RelationClaimInput{
		Node: knowledgedomain.NodeRef{Type: input.Node.Type, ID: input.Node.ID}, Statement: input.Statement,
		Applicability: applicability, Evidence: evidence,
	}, nil
}

func relationActionOutput(action knowledgedomain.AssessmentAction) RelationAction {
	result := RelationAction{Decision: action.Decision, RelationType: action.RelationType, OpenConflict: action.OpenConflict}
	if action.Source.ID != "" {
		result.Source = &RelationNodeInput{Type: action.Source.Type, ID: action.Source.ID}
	}
	if action.Target.ID != "" {
		result.Target = &RelationNodeInput{Type: action.Target.Type, ID: action.Target.ID}
	}
	return result
}

func (executor *Executor) bestEffortFinalize(ctx context.Context, run agentdomain.ModelRun, cause error, unknown bool) error {
	status := agentdomain.ModelRunFailed
	code := errorCode(cause)
	if code == "" {
		code = "AGENT_WORKFLOW_FAILED"
	}
	if unknown {
		status = agentdomain.ModelRunUnknown
		code = ErrorCodeRunFinalizationUnknown
		if errorCode(cause) == agentapplication.ErrorCodeModelCallPersistenceUnknown {
			code = agentapplication.ErrorCodeModelCallPersistenceUnknown
		}
	}
	_, err := executor.finalize(ctx, run, status, "", code)
	return err
}

func (executor *Executor) finalize(ctx context.Context, run agentdomain.ModelRun, status agentdomain.ModelRunStatus, resultType, code string) (agentdomain.ModelRun, error) {
	now := executor.clock.Now()
	if now.Before(run.CreatedAt) {
		now = run.CreatedAt
	}
	terminal := run
	terminal.Status = status
	terminal.FinalResultType = resultType
	terminal.FinalErrorCode = code
	terminal.Version = run.Version + 1
	terminal.UpdatedAt = now
	terminal.CompletedAt = &now
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizeTimeout)
	defer cancel()
	finalized, _, err := executor.repository.FinalizeModelRun(finalizeContext, agentapplication.FinalizeModelRunCommand{ExpectedVersion: run.Version, Run: terminal})
	return finalized, err
}

func validExecutionID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func errorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ workflowapplication.Executor = (*Executor)(nil)
