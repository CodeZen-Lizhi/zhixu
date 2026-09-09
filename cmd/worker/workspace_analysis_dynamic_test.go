package main

import (
	"testing"
	"time"

	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestWorkspaceAnalysisDynamicRegistrationRejectsPartialExecutors(t *testing.T) {
	catalog, err := workflowapplication.NewValidationCatalog([]int{1, 2}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	executor := workerUnavailableExecutor{code: agentworkflow.ErrorCodeCapabilityUnavailable}
	partial := agentworkflow.WorkspaceAnalysisV2Executors{DecideNext: executor}
	if err := registerWorkerWorkspaceAnalysisV2Executors(registry, partial); err == nil {
		t.Fatal("partial dynamic executor set was accepted")
	}
	complete := agentworkflow.WorkspaceAnalysisV2Executors{
		DecideNext: executor, SynthesizeAnswer: executor, ValidateCitations: executor, ReviewPublish: executor,
	}
	if err := registerWorkerWorkspaceAnalysisV2Executors(registry, complete); err != nil {
		t.Fatalf("complete registration after rejected partial set: %v", err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	for _, node := range conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2().Graph.Nodes {
		if _, err := registry.Resolve(node.Kind, node.InputSchemaVersion); err != nil {
			t.Fatalf("dynamic node %s is unreachable: %v", node.Key, err)
		}
	}
}

func TestWorkspaceAnalysisDynamicReadinessRequiresWholeLoopBudget(t *testing.T) {
	contracts, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshotV2FromRegistry(contracts)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV2Deadlines(agentdomain.WorkspaceAnalysisV2Timeouts{
		PlanModelTimeout: cfg.ChatTimeout, SynthesisModelTimeout: cfg.ChatTimeout, ReviewModelTimeout: cfg.ChatTimeout,
		GitToolTimeout: snapshot.ReadGitStatusTimeout, SearchToolTimeout: snapshot.SearchKnowledgeTimeout,
		SourceReadToolTimeout: snapshot.ReadSourceTimeout, ValidateCitationToolTimeout: snapshot.ValidateCitationTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.WorkerJobTimeout = deadlines.MinimumRiverJobTimeout() - time.Nanosecond
	if err := validateWorkspaceAnalysisWorkerRuntime(cfg, cfg.ChatTimeout, contracts); workspaceAnalysisWorkerErrorCode(err) != "WORKER_WORKSPACE_ANALYSIS_RUNTIME_BUDGET_INVALID" {
		t.Fatalf("loop timeout below its whole budget was accepted: %v", err)
	}
	cfg.WorkerJobTimeout = deadlines.MinimumRiverJobTimeout()
	if err := validateWorkspaceAnalysisWorkerRuntime(cfg, cfg.ChatTimeout, contracts); err != nil {
		t.Fatalf("exact dynamic loop budget: %v", err)
	}
}
