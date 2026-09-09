package main

import (
	"context"
	"errors"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
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

type apiWorkspaceAnalysisRunRepository interface {
	agentapplication.ScopedWorkspaceAnalysisRunPersistence
	agentapplication.ScopedWorkspaceAnalysisReadiness
}

// newAPIWorkspaceAnalysisRunStarter keeps the fixed workflow contract and
// caller-owned transaction. Managed starts acquire the current model each time.
func newAPIWorkspaceAnalysisRunStarter(
	database *platformpostgres.Pool,
	cfg config.Config,
	models *modelsettingsruntime.Models,
	runtimes ...modelsettingsruntime.RuntimeAcquirer[*modelsettingsruntime.Models],
) (agentapplication.ScopedWorkspaceAnalysisRunStarter, error) {
	if database == nil {
		return nil, workspaceAnalysisAPIUnavailable(errors.New("workspace analysis API database is unavailable"))
	}
	repository, err := agentpostgres.NewGORMRepository(database)
	if err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	return newAPIWorkspaceAnalysisRunStarterWithRepository(repository, cfg, models, runtimes...)
}

func newAPIWorkspaceAnalysisRunStarterWithRepository(
	repository apiWorkspaceAnalysisRunRepository,
	cfg config.Config,
	models *modelsettingsruntime.Models,
	runtimes ...modelsettingsruntime.RuntimeAcquirer[*modelsettingsruntime.Models],
) (agentapplication.ScopedWorkspaceAnalysisRunStarter, error) {
	if apiCompositionNilDependency(repository) || !cfg.WorkspaceAnalysisAPIEnabled || len(runtimes) > 1 {
		return nil, workspaceAnalysisAPIUnavailable(errors.New("workspace analysis API dependencies or feature flag are unavailable"))
	}
	var runtime modelsettingsruntime.RuntimeAcquirer[*modelsettingsruntime.Models]
	if len(runtimes) == 1 && !apiCompositionNilDependency(runtimes[0]) {
		runtime = runtimes[0]
	}
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged && runtime == nil ||
		cfg.ModelSettingsMode == config.ModelSettingsModeStatic && runtime != nil ||
		cfg.ModelSettingsMode != config.ModelSettingsModeManaged && cfg.ModelSettingsMode != config.ModelSettingsModeStatic {
		return nil, workspaceAnalysisAPIUnavailable(errors.New("workspace analysis runtime source does not match the model settings mode"))
	}
	tools, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshotV2()
	if err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2()
	factory := &apiWorkspaceAnalysisRunFactory{
		repository: repository, ids: foundation.NewUUIDGenerator(nil),
		config: agentapplication.WorkspaceAnalysisRunStartConfig{
			DefinitionHash: definition.GraphHash, ToolCatalogHash: tools.Hash,
			ConfigRevision: cfg.WorkspaceAnalysisConfigRevision,
			Timeouts: agentapplication.WorkspaceAnalysisV1Timeouts{
				GitToolTimeout:    tools.ReadGitStatusTimeout,
				SearchToolTimeout: tools.SearchKnowledgeTimeout, SourceReadToolTimeout: tools.ReadSourceTimeout,
				ValidateCitationToolTimeout: tools.ValidateCitationTimeout,
			},
			SynthesisProfileMaxOutputTokens: int(agentdomain.WorkspaceAnalysisV2SynthesisMaxOutputTokens),
			RuntimeLimits: agentapplication.WorkspaceAnalysisRuntimeLimits{
				RiverJobTimeout: cfg.WorkerJobTimeout, LeaseDuration: cfg.WorkflowLeaseDuration,
				HeartbeatInterval: cfg.WorkflowHeartbeatInterval,
			},
		},
		contract: agentapplication.WorkspaceAnalysisCapabilityContract{
			DefinitionKey: definition.Key, DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash,
			ToolCatalogHash: tools.Hash, PolicyVersion: agentdomain.WorkspaceAnalysisPolicyVersionV2, ConfigRevision: cfg.WorkspaceAnalysisConfigRevision,
		},
	}
	if err := factory.contract.Validate(); err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	replay, err := agentapplication.NewScopedWorkspaceAnalysisRunReplayService(repository)
	if err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	if runtime == nil {
		return &apiWorkspaceAnalysisStaticRunStarter{models: models, factory: factory, replay: replay}, nil
	}
	return &apiWorkspaceAnalysisManagedRunStarter{runtime: runtime, factory: factory, replay: replay}, nil
}

// The factory retains only non-secret, model-independent limits and the shared
// repository. A start receives a private config copy with the leased timeout.
type apiWorkspaceAnalysisRunFactory struct {
	repository apiWorkspaceAnalysisRunRepository
	ids        foundation.IDGenerator
	config     agentapplication.WorkspaceAnalysisRunStartConfig
	contract   agentapplication.WorkspaceAnalysisCapabilityContract
}

func (factory *apiWorkspaceAnalysisRunFactory) forModels(models *modelsettingsruntime.Models) (agentapplication.ScopedWorkspaceAnalysisRunStarter, error) {
	if factory == nil || apiCompositionNilDependency(factory.repository) || apiCompositionNilDependency(factory.ids) || models == nil {
		return nil, workspaceAnalysisAPIUnavailable(errors.New("workspace analysis model-bound start dependencies are unavailable"))
	}
	chatContract, ok := models.Chat().Contract()
	if !ok || chatContract.Timeout <= 0 || apiCompositionNilDependency(models.Chat().Model()) {
		return nil, workspaceAnalysisAPIUnavailable(errors.New("workspace analysis chat contract is unavailable"))
	}
	config := factory.config
	config.Timeouts.PlanModelTimeout = chatContract.Timeout
	config.Timeouts.SynthesisModelTimeout = chatContract.Timeout
	config.Timeouts.ReviewModelTimeout = chatContract.Timeout
	runService, err := agentapplication.NewScopedWorkspaceAnalysisRunServiceV2(factory.repository, factory.ids, config)
	if err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	starter, err := agentapplication.NewScopedWorkspaceAnalysisCapabilityCheckedRunStarter(factory.repository, runService, factory.contract)
	if err != nil {
		return nil, workspaceAnalysisAPIUnavailable(err)
	}
	return starter, nil
}

// Static models remain owned by the API process. Their availability gates only
// new starts, so disabling Chat does not remove the historical replay route.
type apiWorkspaceAnalysisStaticRunStarter struct {
	models  *modelsettingsruntime.Models
	factory *apiWorkspaceAnalysisRunFactory
	replay  agentapplication.ScopedWorkspaceAnalysisRunStarter
}

func (starter *apiWorkspaceAnalysisStaticRunStarter) StartWorkspaceAnalysisRunScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	command agentapplication.WorkspaceAnalysisRunStartCommand,
) (agentdomain.WorkspaceAnalysisRun, error) {
	if starter == nil || starter.factory == nil || apiCompositionNilDependency(starter.replay) || apiCompositionNilDependency(scope) || ctx == nil {
		return agentdomain.WorkspaceAnalysisRun{}, workspaceAnalysisAPIUnavailable(errors.New("workspace analysis static run starter is unavailable"))
	}
	if err := ctx.Err(); err != nil {
		return agentdomain.WorkspaceAnalysisRun{}, workspaceAnalysisAPIUnavailable(err)
	}
	if command.Replayed {
		return starter.replay.StartWorkspaceAnalysisRunScoped(ctx, scope, command)
	}
	delegate, err := starter.factory.forModels(starter.models)
	if err != nil {
		return agentdomain.WorkspaceAnalysisRun{}, err
	}
	return delegate.StartWorkspaceAnalysisRunScoped(ctx, scope, command)
}

type apiWorkspaceAnalysisManagedRunStarter struct {
	runtime modelsettingsruntime.RuntimeAcquirer[*modelsettingsruntime.Models]
	factory *apiWorkspaceAnalysisRunFactory
	replay  agentapplication.ScopedWorkspaceAnalysisRunStarter
}

func (starter *apiWorkspaceAnalysisManagedRunStarter) StartWorkspaceAnalysisRunScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	command agentapplication.WorkspaceAnalysisRunStartCommand,
) (agentdomain.WorkspaceAnalysisRun, error) {
	if starter == nil || apiCompositionNilDependency(starter.runtime) || starter.factory == nil ||
		apiCompositionNilDependency(starter.replay) || apiCompositionNilDependency(scope) || ctx == nil {
		return agentdomain.WorkspaceAnalysisRun{}, workspaceAnalysisAPIUnavailable(errors.New("workspace analysis live run starter is unavailable"))
	}
	if err := ctx.Err(); err != nil {
		return agentdomain.WorkspaceAnalysisRun{}, workspaceAnalysisAPIUnavailable(err)
	}
	if command.Replayed {
		// Historical dispatches keep their persisted version and budget even if
		// the current runtime is disabled, switching or no longer available.
		return starter.replay.StartWorkspaceAnalysisRunScoped(ctx, scope, command)
	}
	lease, err := starter.runtime.Acquire(ctx, modelsettingsruntime.CurrentRuntime())
	if !apiCompositionNilDependency(lease) {
		defer lease.Release()
	}
	if err != nil {
		return agentdomain.WorkspaceAnalysisRun{}, workspaceAnalysisAPIUnavailable(err)
	}
	if apiCompositionNilDependency(lease) {
		return agentdomain.WorkspaceAnalysisRun{}, workspaceAnalysisAPIUnavailable(errors.New("workspace analysis current model lease is unavailable"))
	}
	if err := ctx.Err(); err != nil {
		return agentdomain.WorkspaceAnalysisRun{}, workspaceAnalysisAPIUnavailable(err)
	}
	models, binding := lease.Value(), lease.Binding()
	instanceID, instanceErr := foundation.ParseID(string(binding.InstanceID))
	if models == nil || binding.Mode != modelsettingsruntime.RuntimeModeManaged || binding.Role != modelsettingsruntime.RuntimeRoleAPI ||
		binding.Revision < 0 || models.Revision() != binding.Revision || instanceErr != nil || instanceID != binding.InstanceID {
		return agentdomain.WorkspaceAnalysisRun{}, workspaceAnalysisAPIUnavailable(errors.New("workspace analysis current model binding is inconsistent"))
	}
	delegate, err := starter.factory.forModels(models)
	if err != nil {
		return agentdomain.WorkspaceAnalysisRun{}, err
	}
	return delegate.StartWorkspaceAnalysisRunScoped(ctx, scope, command)
}

var _ agentapplication.ScopedWorkspaceAnalysisRunStarter = (*apiWorkspaceAnalysisStaticRunStarter)(nil)
var _ agentapplication.ScopedWorkspaceAnalysisRunStarter = (*apiWorkspaceAnalysisManagedRunStarter)(nil)

func workspaceAnalysisAPIUnavailable(cause error) error {
	return foundation.NewError(
		foundation.ErrorDependencyUnavailable,
		conversationdomain.WorkspaceAnalysisCapabilityUnavailableCode,
		false,
		cause,
	)
}
