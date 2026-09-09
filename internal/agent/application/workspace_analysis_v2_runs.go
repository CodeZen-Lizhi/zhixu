package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WorkspaceAnalysisBudgetV2 freezes the dynamic budget and leaves price absent
// until a trusted, persisted model-price contract is configured by the product.
func WorkspaceAnalysisBudgetV2(synthesisProfileMaxOutputTokens int) (WorkspaceAnalysisBudgetPolicy, error) {
	if synthesisProfileMaxOutputTokens <= 0 || synthesisProfileMaxOutputTokens > MaxOutputTokens {
		return WorkspaceAnalysisBudgetPolicy{}, workspaceAnalysisPolicyError(errors.New("workspace analysis synthesis output limit is invalid"))
	}
	synthesis := min(int64(synthesisProfileMaxOutputTokens), domain.WorkspaceAnalysisV2SynthesisMaxOutputTokens)
	return WorkspaceAnalysisBudgetPolicy{
		PolicyVersion: domain.WorkspaceAnalysisPolicyVersionV2, MaxNodes: domain.WorkspaceAnalysisV2MaxNodes,
		MaxModelCalls: domain.WorkspaceAnalysisV2MaxModelCalls, MaxToolCalls: domain.WorkspaceAnalysisV2MaxToolCalls,
		MaxSourceReads: domain.WorkspaceAnalysisV2MaxSourceReads, MaxToolConcurrency: domain.WorkspaceAnalysisV2MaxToolConcurrency,
		MaxInputTokensPerModelCall: domain.WorkspaceAnalysisV2MaxInputTokensPerModelCall,
		MaxRunInputTokens:          domain.WorkspaceAnalysisV2MaxRunInputTokens,
		PlanMaxOutputTokens:        domain.WorkspaceAnalysisV2DecisionMaxOutputTokens, SynthesisMaxOutputTokens: synthesis,
		ReviewMaxOutputTokens: domain.WorkspaceAnalysisV2ReviewMaxOutputTokens,
		MaxRunOutputTokens:    domain.WorkspaceAnalysisV2MaxDecisions*domain.WorkspaceAnalysisV2DecisionMaxOutputTokens + synthesis + domain.WorkspaceAnalysisV2ReviewMaxOutputTokens,
	}, nil
}

// ScopedWorkspaceAnalysisRunServiceV2 starts new v2 runs and reads prior-version
// replay by its persisted binding, without applying the new config to history.
type ScopedWorkspaceAnalysisRunServiceV2 struct {
	repository ScopedWorkspaceAnalysisRunPersistence
	ids        foundation.IDGenerator
	config     WorkspaceAnalysisRunStartConfig
	budget     WorkspaceAnalysisBudgetPolicy
	deadlines  domain.WorkspaceAnalysisV2Deadlines
}

func NewScopedWorkspaceAnalysisRunServiceV2(repository ScopedWorkspaceAnalysisRunPersistence, ids foundation.IDGenerator, config WorkspaceAnalysisRunStartConfig) (*ScopedWorkspaceAnalysisRunServiceV2, error) {
	if isNilPort(repository) || isNilPort(ids) || !canonicalWorkspaceAnalysisSHA256(config.DefinitionHash) ||
		!canonicalWorkspaceAnalysisSHA256(config.ToolCatalogHash) || config.ConfigRevision < 0 {
		return nil, workspaceAnalysisRunStartError(foundation.ErrorInvalidInput, ErrorCodeWorkspaceAnalysisRunStartInvalid, false, errors.New("workspace analysis v2 run dependencies are invalid"))
	}
	budget, err := WorkspaceAnalysisBudgetV2(config.SynthesisProfileMaxOutputTokens)
	if err != nil {
		return nil, err
	}
	deadlines, err := domain.DeriveWorkspaceAnalysisV2Deadlines(config.Timeouts)
	if err != nil {
		return nil, err
	}
	if err := deadlines.ValidateRuntimeReadiness(config.RuntimeLimits); err != nil {
		return nil, err
	}
	return &ScopedWorkspaceAnalysisRunServiceV2{repository: repository, ids: ids, config: config, budget: budget, deadlines: deadlines}, nil
}

func (service *ScopedWorkspaceAnalysisRunServiceV2) StartWorkspaceAnalysisRunScoped(ctx context.Context, scope foundation.TransactionScope, command WorkspaceAnalysisRunStartCommand) (domain.WorkspaceAnalysisRun, error) {
	if service == nil || isNilPort(service.repository) || isNilPort(service.ids) || isNilPort(scope) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(foundation.ErrorDependencyUnavailable, ErrorCodeWorkspaceAnalysisRunStartUnavailable, true, errors.New("workspace analysis v2 run service is unavailable"))
	}
	if ctx == nil || !validWorkspaceAnalysisRunStartCommand(command) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(foundation.ErrorInvalidInput, ErrorCodeWorkspaceAnalysisRunStartInvalid, false, errors.New("workspace analysis v2 run start binding is invalid"))
	}
	if command.Replayed {
		existing, found, err := service.repository.FindWorkspaceAnalysisRunScoped(ctx, scope, command.WorkspaceID, command.QuestionID)
		if err != nil {
			return domain.WorkspaceAnalysisRun{}, err
		}
		if !found || domain.ValidateWorkspaceAnalysisRun(existing) != nil || !sameWorkspaceAnalysisRunDispatchBinding(existing, command) {
			return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(foundation.ErrorConsistencyViolation, ErrorCodeWorkspaceAnalysisRunStartConflict, false, errors.New("replayed workspace analysis run is missing or drifted"))
		}
		return existing, nil
	}
	id, err := service.ids.New()
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(foundation.ErrorDependencyUnavailable, ErrorCodeWorkspaceAnalysisRunStartUnavailable, true, err)
	}
	run := domain.WorkspaceAnalysisRun{
		ID: id, WorkspaceID: command.WorkspaceID, ConversationID: command.ConversationID, QuestionID: command.QuestionID,
		AnswerID: command.AnswerID, WorkflowRunID: command.WorkflowRunID, DefinitionKey: "workspace-analysis", DefinitionVersion: 2,
		DefinitionHash: service.config.DefinitionHash, ToolCatalogHash: service.config.ToolCatalogHash, PolicyVersion: domain.WorkspaceAnalysisPolicyVersionV2,
		ConfigRevision: service.config.ConfigRevision, DeadlineAt: command.CreatedAt.Add(service.deadlines.RunDeadline()), Timeouts: service.config.Timeouts,
		Limits: domain.WorkspaceAnalysisBudgetLimits{Nodes: service.budget.MaxNodes, ToolConcurrency: service.budget.MaxToolConcurrency, Amount: domain.WorkspaceAnalysisBudgetAmount{
			ModelCalls: service.budget.MaxModelCalls, ToolCalls: service.budget.MaxToolCalls, SourceReads: service.budget.MaxSourceReads,
			InputTokens: service.budget.MaxRunInputTokens, OutputTokens: service.budget.MaxRunOutputTokens,
		}}, Status: domain.WorkspaceAnalysisRunQueued, Version: 1, CreatedAt: command.CreatedAt.UTC(), UpdatedAt: command.CreatedAt.UTC(),
	}
	if err := domain.ValidateWorkspaceAnalysisRun(run); err != nil {
		return domain.WorkspaceAnalysisRun{}, err
	}
	persisted, err := service.repository.InsertWorkspaceAnalysisRunScoped(ctx, scope, run)
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, err
	}
	if domain.ValidateWorkspaceAnalysisRun(persisted) != nil || !sameWorkspaceAnalysisQueuedRun(persisted, run) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(foundation.ErrorConsistencyViolation, ErrorCodeWorkspaceAnalysisRunStartConflict, false, errors.New("persisted workspace analysis v2 run differs from the queued candidate"))
	}
	return persisted, nil
}

var _ ScopedWorkspaceAnalysisRunStarter = (*ScopedWorkspaceAnalysisRunServiceV2)(nil)
