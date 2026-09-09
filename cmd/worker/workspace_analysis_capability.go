package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const workspaceAnalysisCapabilityHeartbeatInterval = 10 * time.Second

type workerWorkspaceAnalysisCapability struct {
	service       *agentapplication.WorkspaceAnalysisCapabilityService
	advertisement agentapplication.WorkspaceAnalysisWorkerAdvertisement
	runtime       workerRuntimeAttemptAcquirer
	definition    workflowdomain.RegisteredDefinition
	limits        agentdomain.WorkspaceAnalysisRuntimeLimits
	mu            sync.Mutex
	attempted     bool
	released      bool
}

// Each immutable generation carries its own optional composition result and
// frozen tool registries. It contains no model settings or credentials.
type workerWorkspaceAnalysisGeneration struct {
	capability agentCapabilityStatus
	contracts  *toolsapplication.Registry
	executors  *toolsapplication.Registry
}

func newWorkerWorkspaceAnalysisGeneration(status agentCapabilityStatus, tools toolRuntimeComponents) workerWorkspaceAnalysisGeneration {
	if tools.execution == nil || !tools.workspaceAnalysisCapability.available || tools.workspaceAnalysisCapability.code != "" {
		status = agentCapabilityStatus{code: agentapplication.ErrorCodeWorkspaceAnalysisCapabilityUnavailable}
	}
	return workerWorkspaceAnalysisGeneration{capability: status, contracts: tools.contracts, executors: tools.executions}
}

func newWorkerWorkspaceAnalysisCapability(
	database *platformpostgres.Pool,
	cfg config.Config,
	components workerComponents,
	models *modelsettingsruntime.Models,
	runtimes ...workerRuntimeAttemptAcquirer,
) (*workerWorkspaceAnalysisCapability, error) {
	if !cfg.WorkspaceAnalysisWorkerEnabled {
		return nil, nil
	}
	if len(runtimes) > 1 {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis runtime source is ambiguous"))
	}
	var runtime workerRuntimeAttemptAcquirer
	if len(runtimes) == 1 && !workerRuntimeNilDependency(runtimes[0]) {
		runtime = runtimes[0]
	}
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged && runtime == nil ||
		cfg.ModelSettingsMode == config.ModelSettingsModeStatic && runtime != nil ||
		cfg.ModelSettingsMode != config.ModelSettingsModeManaged && cfg.ModelSettingsMode != config.ModelSettingsModeStatic {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis runtime source does not match the model settings mode"))
	}
	if database == nil || components.tools.contracts == nil || components.workerID == "" {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis worker capability dependencies are unavailable"))
	}
	definition, err := frozenWorkerWorkspaceAnalysisDefinition(components.tools.contracts)
	if err != nil {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(err)
	}
	tools, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshotV2FromRegistry(components.tools.contracts)
	if err != nil {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(err)
	}
	repository, err := agentpostgres.NewGORMRepository(database)
	if err != nil {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(err)
	}
	service, err := agentapplication.NewWorkspaceAnalysisCapabilityService(repository)
	if err != nil {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(err)
	}
	advertisement := agentapplication.WorkspaceAnalysisWorkerAdvertisement{
		WorkerInstanceID: components.workerID,
		Contract: agentapplication.WorkspaceAnalysisCapabilityContract{
			DefinitionKey: definition.Key, DefinitionVersion: definition.Version,
			DefinitionHash: definition.GraphHash, ToolCatalogHash: tools.Hash,
			PolicyVersion: agentdomain.WorkspaceAnalysisPolicyVersionV2, ConfigRevision: cfg.WorkspaceAnalysisConfigRevision,
		},
		LeaseDuration: agentapplication.DefaultWorkspaceAnalysisWorkerCapabilityLease,
	}
	if err := advertisement.Validate(); err != nil {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(err)
	}
	result := &workerWorkspaceAnalysisCapability{
		service: service, advertisement: advertisement, runtime: runtime, definition: definition,
		limits: agentdomain.WorkspaceAnalysisRuntimeLimits{
			RiverJobTimeout: cfg.WorkerJobTimeout, LeaseDuration: cfg.WorkflowLeaseDuration,
			HeartbeatInterval: cfg.WorkflowHeartbeatInterval,
		},
	}
	if runtime == nil {
		// Static callers retain the original constructor checks. Managed callers
		// must not depend on the startup registry: chat may be enabled later.
		persisted, err := components.definitions.Resolve(definition.Key, definition.Version)
		if err != nil || !reflect.DeepEqual(persisted, definition) {
			return nil, workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis workflow definition is unavailable or drifted"))
		}
		if err := result.validateGeneration(&workerRuntimeGeneration{
			models: models, executors: components.executors,
			workspaceAnalysis: newWorkerWorkspaceAnalysisGeneration(components.workspaceAnalysisCapability, components.tools),
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (capability *workerWorkspaceAnalysisCapability) Advertise(ctx context.Context) error {
	return capability.refresh(ctx, false)
}

func (capability *workerWorkspaceAnalysisCapability) Renew(ctx context.Context) error {
	return capability.refresh(ctx, true)
}

func (capability *workerWorkspaceAnalysisCapability) refresh(ctx context.Context, renew bool) error {
	if capability == nil || capability.service == nil || ctx == nil {
		return workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis worker capability is unavailable"))
	}
	capability.mu.Lock()
	defer capability.mu.Unlock()
	if capability.released {
		return workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis worker capability is released"))
	}
	if err := ctx.Err(); err != nil {
		return workerWorkspaceAnalysisCapabilityUnavailable(errors.Join(err, context.Cause(ctx)))
	}
	if capability.runtime != nil {
		lease, err := capability.runtime.Acquire(ctx, modelsettingsruntime.CurrentRuntime())
		if !workerRuntimeNilDependency(lease) {
			defer lease.Release()
		}
		if err != nil {
			return workerWorkspaceAnalysisCapabilityUnavailable(err)
		}
		if workerRuntimeNilDependency(lease) {
			return workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis current runtime lease is unavailable"))
		}
		generation, binding := lease.Value(), lease.Binding()
		if generation == nil || generation.models == nil || binding.Mode != modelsettingsruntime.RuntimeModeManaged ||
			binding.Role != modelsettingsruntime.RuntimeRoleWorker || generation.models.Revision() != binding.Revision {
			return workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis current runtime binding is inconsistent"))
		}
		if err := capability.validateGeneration(generation); err != nil {
			return err
		}
	}
	// A failed/disabled acquisition never reaches a write. Its old advertisement
	// expires under the existing database lease; permanent Release is reserved
	// for shutdown so this worker can advertise again after a later activation.
	capability.attempted = true
	if renew {
		if _, err := capability.service.Heartbeat(ctx, capability.advertisement); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return err
		}
	}
	_, err := capability.service.Advertise(ctx, capability.advertisement)
	return err
}

func (capability *workerWorkspaceAnalysisCapability) Release(ctx context.Context) error {
	if capability == nil || capability.service == nil {
		return nil
	}
	if ctx == nil {
		return workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis capability release context is unavailable"))
	}
	capability.mu.Lock()
	defer capability.mu.Unlock()
	if capability.released {
		return nil
	}
	if !capability.attempted {
		capability.released = true
		return nil
	}
	_, err := capability.service.Release(ctx, capability.advertisement)
	if err == nil {
		capability.released = true
	}
	return err
}

func (capability *workerWorkspaceAnalysisCapability) validateGeneration(generation *workerRuntimeGeneration) error {
	if generation == nil || generation.models == nil || generation.executors == nil ||
		!generation.workspaceAnalysis.capability.available || generation.workspaceAnalysis.capability.code != "" ||
		generation.workspaceAnalysis.contracts == nil || generation.workspaceAnalysis.executors == nil {
		return workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis current generation composition is unavailable"))
	}
	chatContract, ok := generation.models.Chat().Contract()
	if !ok || workerRuntimeNilDependency(generation.models.Chat().Model()) {
		return workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis current chat contract is unavailable"))
	}
	limits := config.Config{
		WorkerJobTimeout: capability.limits.RiverJobTimeout, WorkflowLeaseDuration: capability.limits.LeaseDuration,
		WorkflowHeartbeatInterval: capability.limits.HeartbeatInterval,
	}
	if err := validateWorkspaceAnalysisWorkerRuntime(limits, chatContract.Timeout, generation.workspaceAnalysis.contracts); err != nil {
		return workerWorkspaceAnalysisCapabilityUnavailable(err)
	}
	for _, registry := range []*toolsapplication.Registry{generation.workspaceAnalysis.contracts, generation.workspaceAnalysis.executors} {
		snapshot, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshotV2FromRegistry(registry)
		if err != nil || snapshot.Hash != capability.advertisement.Contract.ToolCatalogHash {
			return workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis current tool contract is unavailable or drifted"))
		}
	}
	for _, node := range capability.definition.Graph.Nodes {
		executor, err := generation.executors.Resolve(node.Kind, node.InputSchemaVersion)
		if err != nil || workerRuntimeNilDependency(executor) {
			return workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis current executor set is incomplete"))
		}
		switch executor.(type) {
		case workerUnavailableExecutor, *workerUnavailableExecutor:
			return workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis current executor is unavailable"))
		}
		for _, tool := range node.AllowedTools {
			if _, err := generation.workspaceAnalysis.executors.ResolveExecutor(tool); err != nil {
				return workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis current tool executor is unavailable"))
			}
		}
	}
	return nil
}

// Freeze the public capability contract without model-bound executors. This
// registry only validates the fixed definition and does not start any work.
func frozenWorkerWorkspaceAnalysisDefinition(contracts *toolsapplication.Registry) (workflowdomain.RegisteredDefinition, error) {
	expected := conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2()
	catalog, err := workflowapplication.NewValidationCatalog([]int{1, 2}, capability.All())
	if err != nil {
		return workflowdomain.RegisteredDefinition{}, err
	}
	executors, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		return workflowdomain.RegisteredDefinition{}, err
	}
	for _, node := range expected.Graph.Nodes {
		if err := executors.RegisterContract(node.Kind, node.InputSchemaVersion); err != nil {
			return workflowdomain.RegisteredDefinition{}, err
		}
	}
	if err := executors.Freeze(); err != nil {
		return workflowdomain.RegisteredDefinition{}, err
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors, contracts)
	if err != nil {
		return workflowdomain.RegisteredDefinition{}, err
	}
	if err := definitions.Register(expected); err != nil {
		return workflowdomain.RegisteredDefinition{}, err
	}
	if err := definitions.Freeze(); err != nil {
		return workflowdomain.RegisteredDefinition{}, err
	}
	definition, err := definitions.Resolve(expected.Key, expected.Version)
	if err != nil || !reflect.DeepEqual(definition, expected) {
		return workflowdomain.RegisteredDefinition{}, workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis fixed definition is drifted"))
	}
	return definition, nil
}

func workerWorkspaceAnalysisCapabilityUnavailable(cause error) error {
	return foundation.NewError(
		foundation.ErrorDependencyUnavailable,
		agentapplication.ErrorCodeWorkspaceAnalysisCapabilityUnavailable,
		false,
		cause,
	)
}
