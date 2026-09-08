package main

import (
	"errors"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	conversationpostgres "github.com/CodeZen-Lizhi/zhixu/internal/conversation/adapter/postgres"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const apiWorkspaceAnalysisRuntimeActorRef = "api:workspace-analysis-runtime"

type apiWorkspaceAnalysisRuntimeHooks struct {
	terminal workflowapplication.ScopedWorkflowTerminalHook
	control  workflowapplication.ScopedWorkflowControlHook
}

func newAPIWorkspaceAnalysisRuntimeHooks(
	database *platformpostgres.Pool,
	events eventsapplication.ScopedAppender,
) (*apiWorkspaceAnalysisRuntimeHooks, error) {
	if database == nil || events == nil {
		return nil, workspaceAnalysisAPIUnavailable(errors.New("workspace analysis API runtime hook dependencies are unavailable"))
	}
	auditRepository, err := auditpostgres.NewGORMStore(database)
	if err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	auditRecorder, err := auditapplication.NewRecorder(auditRepository)
	if err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	terminal, err := conversationpostgres.NewGORMWorkspaceAnalysisCancellationTerminalHookWithAudit(
		events,
		foundation.NewUUIDGenerator(nil),
		auditRecorder,
		apiWorkspaceAnalysisRuntimeActorRef,
	)
	if err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	control, err := conversationpostgres.NewGORMWorkspaceAnalysisCancellationAuditHook(auditRecorder)
	if err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	return &apiWorkspaceAnalysisRuntimeHooks{terminal: terminal, control: control}, nil
}

func composeAPIWorkflowRuntimeHooks(
	artifactTerminal workflowapplication.ScopedWorkflowTerminalHook,
	workspaceAnalysis *apiWorkspaceAnalysisRuntimeHooks,
) (workflowapplication.ScopedWorkflowTerminalHook, workflowapplication.ScopedWorkflowControlHook, error) {
	terminalHooks := []workflowapplication.ScopedWorkflowTerminalHook{artifactTerminal}
	var control workflowapplication.ScopedWorkflowControlHook
	if workspaceAnalysis != nil {
		terminalHooks = append(terminalHooks, workspaceAnalysis.terminal)
		control = workspaceAnalysis.control
	}
	terminal, err := workflowapplication.NewCompositeScopedWorkflowTerminalHook(terminalHooks...)
	if err != nil {
		return nil, nil, err
	}
	return terminal, control, nil
}

// newAPIWorkspaceAnalysisRunStarter 组合本地冻结合同与事务内 Worker 广告检查。
// 该函数不探测 Worker；是否可接单由每次 Question 派发持有的数据库事务决定。
func newAPIWorkspaceAnalysisRunStarter(
	database *platformpostgres.Pool,
	cfg config.Config,
	models *modelsettingsruntime.Models,
) (agentapplication.ScopedWorkspaceAnalysisRunStarter, error) {
	if database == nil || models == nil || !cfg.WorkspaceAnalysisAPIEnabled {
		return nil, workspaceAnalysisAPIUnavailable(errors.New("workspace analysis API dependencies or feature flag are unavailable"))
	}
	chatContract, ok := models.Chat().Contract()
	if !ok || chatContract.Timeout <= 0 {
		return nil, workspaceAnalysisAPIUnavailable(errors.New("workspace analysis chat contract is unavailable"))
	}
	tools, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshot()
	if err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	repository, err := agentpostgres.NewGORMRepository(database)
	if err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	runService, err := agentapplication.NewScopedWorkspaceAnalysisRunService(
		repository,
		foundation.NewUUIDGenerator(nil),
		agentapplication.WorkspaceAnalysisRunStartConfig{
			DefinitionHash: definition.GraphHash, ToolCatalogHash: tools.Hash,
			ConfigRevision: cfg.WorkspaceAnalysisConfigRevision,
			Timeouts: agentapplication.WorkspaceAnalysisV1Timeouts{
				PlanModelTimeout: chatContract.Timeout, SynthesisModelTimeout: chatContract.Timeout,
				ReviewModelTimeout: chatContract.Timeout, GitToolTimeout: tools.ReadGitStatusTimeout,
				SearchToolTimeout: tools.SearchKnowledgeTimeout, SourceReadToolTimeout: tools.ReadSourceTimeout,
				ValidateCitationToolTimeout: tools.ValidateCitationTimeout,
			},
			SynthesisProfileMaxOutputTokens: int(agentapplication.WorkspaceAnalysisV1SynthesisMaxOutputTokens),
			RuntimeLimits: agentapplication.WorkspaceAnalysisRuntimeLimits{
				RiverJobTimeout: cfg.WorkerJobTimeout, LeaseDuration: cfg.WorkflowLeaseDuration,
				HeartbeatInterval: cfg.WorkflowHeartbeatInterval,
			},
		},
	)
	if err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	starter, err := agentapplication.NewScopedWorkspaceAnalysisCapabilityCheckedRunStarter(
		repository,
		runService,
		agentapplication.WorkspaceAnalysisCapabilityContract{
			DefinitionKey: definition.Key, DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash,
			ToolCatalogHash: tools.Hash, PolicyVersion: 1, ConfigRevision: cfg.WorkspaceAnalysisConfigRevision,
		},
	)
	if err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	return starter, nil
}

func workspaceAnalysisAPIUnavailable(cause error) error {
	return foundation.NewError(
		foundation.ErrorDependencyUnavailable,
		conversationdomain.WorkspaceAnalysisCapabilityUnavailableCode,
		false,
		cause,
	)
}
