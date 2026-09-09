package main

import (
	"errors"

	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func registerWorkerWorkspaceAnalysisV2Executors(registry *workflowapplication.ExecutorRegistry, executors agentworkflow.WorkspaceAnalysisV2Executors) error {
	byKey := map[string]workflowapplication.Executor{
		conversationworkflow.WorkspaceAnalysisNodeDecideNext:        executors.DecideNext,
		conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer:  executors.SynthesizeAnswer,
		conversationworkflow.WorkspaceAnalysisNodeValidateCitations: executors.ValidateCitations,
		conversationworkflow.WorkspaceAnalysisNodeReviewPublish:     executors.ReviewPublish,
	}
	available := 0
	for _, executor := range byKey {
		if !workerRuntimeNilDependency(executor) {
			available++
		}
	}
	if available == 0 {
		return nil
	}
	if available != len(byKey) || registry == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_WORKSPACE_ANALYSIS_EXECUTORS_UNAVAILABLE", false,
			errors.New("workspace analysis v2 executor registration is incomplete"))
	}
	for _, node := range conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2().Graph.Nodes {
		executor, found := byKey[node.Key]
		if !found {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKER_WORKSPACE_ANALYSIS_CONTRACT_INVALID", false,
				errors.New("workspace analysis v2 node is not supported"))
		}
		if err := registry.Register(node.Kind, node.InputSchemaVersion, executor); err != nil {
			return err
		}
	}
	return nil
}
