package main

import (
	"context"
	"errors"
	"reflect"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
)

const workspaceAnalysisCapabilityHeartbeatInterval = 10 * time.Second

type workerWorkspaceAnalysisCapability struct {
	service       *agentapplication.WorkspaceAnalysisCapabilityService
	advertisement agentapplication.WorkspaceAnalysisWorkerAdvertisement
}

func newWorkerWorkspaceAnalysisCapability(
	database *platformpostgres.Pool,
	cfg config.Config,
	components workerComponents,
	models *modelsettingsruntime.Models,
) (*workerWorkspaceAnalysisCapability, error) {
	if !cfg.WorkspaceAnalysisWorkerEnabled {
		return nil, nil
	}
	if !components.workspaceAnalysisCapability.available || components.workspaceAnalysisCapability.code != "" {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis optional worker composition is unavailable"))
	}
	if database == nil || models == nil || components.tools.contracts == nil || components.tools.execution == nil ||
		components.executors == nil || components.definitions == nil || components.workerID == "" {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis worker capability dependencies are unavailable"))
	}
	chatContract, ok := models.Chat().Contract()
	if !ok || chatContract.Timeout <= 0 {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis worker chat contract is unavailable"))
	}
	if err := validateWorkspaceAnalysisWorkerRuntime(cfg, chatContract.Timeout, components.tools.contracts); err != nil {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(err)
	}
	expectedDefinition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	persistedDefinition, err := components.definitions.Resolve(expectedDefinition.Key, expectedDefinition.Version)
	if err != nil || !reflect.DeepEqual(persistedDefinition, expectedDefinition) {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis workflow definition is unavailable or drifted"))
	}
	for _, node := range expectedDefinition.Graph.Nodes {
		if _, err := components.executors.Resolve(node.Kind, node.InputSchemaVersion); err != nil {
			return nil, workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis executor set is incomplete"))
		}
	}
	tools, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshotFromRegistry(components.tools.contracts)
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
			DefinitionKey: expectedDefinition.Key, DefinitionVersion: expectedDefinition.Version,
			DefinitionHash: expectedDefinition.GraphHash, ToolCatalogHash: tools.Hash,
			PolicyVersion: 1, ConfigRevision: cfg.WorkspaceAnalysisConfigRevision,
		},
		LeaseDuration: agentapplication.DefaultWorkspaceAnalysisWorkerCapabilityLease,
	}
	if err := advertisement.Validate(); err != nil {
		return nil, workerWorkspaceAnalysisCapabilityUnavailable(err)
	}
	return &workerWorkspaceAnalysisCapability{service: service, advertisement: advertisement}, nil
}

func (capability *workerWorkspaceAnalysisCapability) Advertise(ctx context.Context) error {
	if capability == nil || capability.service == nil {
		return workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis worker capability is unavailable"))
	}
	_, err := capability.service.Advertise(ctx, capability.advertisement)
	return err
}

func (capability *workerWorkspaceAnalysisCapability) Renew(ctx context.Context) error {
	if capability == nil || capability.service == nil {
		return workerWorkspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis worker capability is unavailable"))
	}
	if _, err := capability.service.Heartbeat(ctx, capability.advertisement); err == nil {
		return nil
	}
	// Advertise may safely revive an expired advertisement with the same immutable contract.
	_, err := capability.service.Advertise(ctx, capability.advertisement)
	return err
}

func (capability *workerWorkspaceAnalysisCapability) Release(ctx context.Context) error {
	if capability == nil || capability.service == nil {
		return nil
	}
	_, err := capability.service.Release(ctx, capability.advertisement)
	return err
}

func workerWorkspaceAnalysisCapabilityUnavailable(cause error) error {
	return foundation.NewError(
		foundation.ErrorDependencyUnavailable,
		agentapplication.ErrorCodeWorkspaceAnalysisCapabilityUnavailable,
		false,
		cause,
	)
}
