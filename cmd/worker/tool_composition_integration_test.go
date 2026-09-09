//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	artifactworkflow "github.com/CodeZen-Lizhi/zhixu/internal/artifact/workflow"
	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	changecontrolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/workflow"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memorypostgres "github.com/CodeZen-Lizhi/zhixu/internal/memory/adapter/postgres"
	memoryapplication "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	toolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/workflow"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
)

func TestWorkerToolCompositionSeparatesContractsExecutorsAndTrustedAudit(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	pool := newMigratedWorkerTestPool(t, databaseURL)
	workspaceRepository, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	gitInspector, err := gitcli.NewWritebackClient(gitcli.New(""), workspaceRepository)
	if err != nil {
		t.Fatal(err)
	}

	disabled, err := newToolRuntimeComponents(pool, config.Defaults(), workspaceRepository, gitInspector)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.contracts == nil || disabled.repository == nil || disabled.writebackAudit == nil || disabled.runtimeEnabled || disabled.executions != nil || disabled.execution != nil || disabled.workflow != nil || disabled.definition != nil || len(disabled.enabledRefs) != 0 {
		t.Fatalf("disabled components=%+v", disabled)
	}

	cfg := config.Defaults()
	cfg.ToolRuntimeMode = config.ToolModeEnabled
	cfg.WorkspaceAnalysisWorkerEnabled = true
	enabled, err := newToolRuntimeComponents(pool, cfg, workspaceRepository, gitInspector,
		toolRuntimeCompositionInput{workspaceAnalysisAudit: newWorkerTestAuditRecorder(t, pool)})
	if err != nil {
		t.Fatal(err)
	}
	if enabled.contracts == nil || enabled.executions == nil || enabled.execution == nil || enabled.repository == nil || enabled.writebackAudit == nil || enabled.workflow == nil || enabled.definition == nil || !enabled.runtimeEnabled || len(enabled.enabledRefs) != 15 {
		t.Fatalf("enabled components=%+v", enabled)
	}
	for _, ref := range enabled.enabledRefs {
		executor, err := enabled.executions.ResolveExecutor(ref)
		if err != nil {
			t.Fatalf("resolve executor %s: %v", ref.Name, err)
		}
		contract, err := enabled.contracts.ResolveContract(ref)
		if err != nil {
			t.Fatalf("resolve contract %s: %v", ref.Name, err)
		}
		if contract.Definition.ResultPersistencePolicy != toolsdomain.ResultPersistenceCanonical {
			if _, ok := executor.(toolsapplication.ResultReceiptLoader); ok {
				continue
			}
			t.Fatalf("executor %s lacks canonical replay loader", ref.Name)
		}
	}
	for _, ref := range []toolsdomain.ToolRef{
		{Name: "ReadGitStatus", Version: 2},
		{Name: "SearchKnowledge", Version: 2},
		{Name: "ReadSource", Version: 3},
		{Name: "ValidateCitation", Version: 3},
		{Name: "ReadGitStatus", Version: 3},
		{Name: "SearchKnowledge", Version: 3},
		{Name: "ReadSource", Version: 4},
		{Name: "ValidateCitation", Version: 4},
	} {
		if _, err := enabled.executions.ResolveExecutor(ref); err != nil {
			t.Fatalf("workspace analysis executor %s@%d is unreachable: %v", ref.Name, ref.Version, err)
		}
	}
	for _, unavailable := range []toolsdomain.ToolRef{
		{Name: "ReadDocument", Version: 1}, {Name: "FetchWebPage", Version: 1},
		{Name: "RebuildIndex", Version: 1}, {Name: "RunRegressionEvaluation", Version: 1},
		{Name: "ApplyApprovedPatch", Version: 1}, {Name: "CreateGitCommit", Version: 1},
	} {
		if _, err := enabled.contracts.ResolveContract(unavailable); err != nil {
			t.Fatalf("contract %s missing: %v", unavailable.Name, err)
		}
		if _, err := enabled.executions.ResolveExecutor(unavailable); err == nil {
			t.Fatalf("unavailable/trusted-only executor exposed: %s", unavailable.Name)
		}
	}

	worker, err := newWorkerComponents(pool, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), observability.NewMemoryMetrics())
	if err != nil {
		t.Fatal(err)
	}
	contractsOK, executorsOK, dependenciesOK := toolWorkflowReadiness(worker)
	if !contractsOK || !executorsOK || !dependenciesOK {
		t.Fatalf("worker tool readiness contracts=%t executors=%t dependencies=%t", contractsOK, executorsOK, dependenciesOK)
	}
	if _, err := worker.executors.Resolve(toolworkflow.NodeKind, toolworkflow.InputSchemaVersion); err != nil {
		t.Fatalf("worker Tool node executor is unreachable: %v", err)
	}
	if _, err := worker.executors.Resolve(agentworkflow.RelationAssessmentNodeKind, agentworkflow.RelationAssessmentInputSchemaVersion); err == nil {
		t.Fatal("disabled Chat unexpectedly registered the Relation executor")
	}
	if _, err := worker.executors.Resolve(agentworkflow.RAGWorkflowNodeKind, agentworkflow.RAGWorkflowInputSchemaVersion); err == nil {
		t.Fatal("disabled Chat unexpectedly registered the RAG executor")
	}
	definition, err := worker.definitions.Resolve(toolworkflow.DefinitionKey, toolworkflow.DefinitionVersion)
	if err != nil || len(definition.Graph.Nodes) != 1 || len(definition.Graph.Nodes[0].AllowedTools) != len(toolworkflow.ModelReadToolRefsV1()) {
		t.Fatalf("worker Tool definition=%+v err=%v", definition, err)
	}
	for _, executionOnly := range []string{"SearchKnowledge", "CalculateDiff"} {
		if _, err := enabled.executions.ResolveExecutor(toolsdomain.ToolRef{Name: executionOnly, Version: 1}); err != nil {
			t.Fatalf("execution-only Tool %s lost its real Executor: %v", executionOnly, err)
		}
		for _, ref := range definition.Graph.Nodes[0].AllowedTools {
			if ref.Name == executionOnly {
				t.Fatalf("execution-only Tool entered production persisted Definition: %s", executionOnly)
			}
		}
	}
}

func newMigratedWorkerTestPool(t *testing.T, baseURL string) *platformpostgres.Pool {
	t.Helper()
	return testdb.Require(t, testdb.Config{ExternalAdminURL: baseURL, MaxConns: 8}).Pool()
}

func newWorkerTestAuditRecorder(t *testing.T, pool *platformpostgres.Pool) *auditapplication.Recorder {
	t.Helper()
	store, err := auditpostgres.NewGORMStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := auditapplication.NewRecorder(store)
	if err != nil {
		t.Fatal(err)
	}
	return recorder
}

func newWorkerTestMemoryRepository(t *testing.T, pool *platformpostgres.Pool) *memorypostgres.GORMRepository {
	t.Helper()
	database, err := pool.GORM()
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	repository, err := memorypostgres.NewGORMRepository(database, unitOfWork)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func TestWorkerChatCompositionUsesEinoSchedulersAndRegistersRelationAndRAGTogether(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	pool := newMigratedWorkerTestPool(t, databaseURL)
	var requiredTables int
	var tableNames string
	if err := pool.DB().QueryRow(context.Background(), `
		SELECT count(*),COALESCE(string_agg(table_schema||'.'||table_name,',' ORDER BY table_schema,table_name),'')
		FROM information_schema.tables
		WHERE (table_schema,table_name) IN (
			('agent','answer'),('agent','model_run'),('ops','server_event'),('workflow','run')
		)`).Scan(&requiredTables, &tableNames); err != nil || requiredTables != 4 {
		t.Fatalf("required RAG PostgreSQL table count=%d tables=%q err=%v", requiredTables, tableNames, err)
	}
	workspaceRepository, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	memoryRepository := newWorkerTestMemoryRepository(t, pool)
	memoryService, err := memoryapplication.NewService(memoryapplication.Dependencies{
		Repository: memoryRepository,
		IDs:        foundation.NewUUIDGenerator(nil),
		Clock:      foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	disabledWorker, err := newWorkerComponents(pool, config.Defaults(), slog.New(slog.NewTextHandler(io.Discard, nil)), observability.NewMemoryMetrics())
	if err != nil {
		t.Fatal(err)
	}
	if disabledWorker.artifact.generation == nil || disabledWorker.artifact.terminal == nil || disabledWorker.artifact.executor != nil || disabledWorker.artifact.catalog != nil || disabledWorker.artifact.capability.available || disabledWorker.artifact.capability.code != artifactworkflow.ErrorCodeCapabilityUnavailable {
		t.Fatalf("disabled artifact components=%+v", disabledWorker.artifact)
	}
	if !artifactWorkflowReadiness(disabledWorker) {
		t.Fatal("disabled Artifact composition is not ready")
	}
	disabledCatalogLeak := disabledWorker
	disabledCatalogLeak.artifact.catalog = agentapplication.NewRuntimeCatalog()
	if artifactWorkflowReadiness(disabledCatalogLeak) {
		t.Fatal("disabled Artifact readiness accepted a registered runtime catalog")
	}
	if _, err := disabledWorker.executors.Resolve(artifactworkflow.NodeKind, artifactworkflow.InputSchemaVersion); err == nil {
		t.Fatal("disabled Chat unexpectedly registered the Artifact executor")
	}
	if _, err := disabledWorker.definitions.Resolve(artifactworkflow.DefinitionKey, artifactworkflow.DefinitionVersion); err == nil {
		t.Fatal("disabled Chat unexpectedly registered the Artifact definition")
	}
	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	cfg.ChatBaseURL = "http://127.0.0.1:11434/v1"
	cfg.ChatModel = "composition-test"
	cfg.ChatModelVersion = "composition-test-v1"
	cfg.ToolRuntimeMode = config.ToolModeEnabled
	cfg.WorkspaceAnalysisWorkerEnabled = true
	gitInspector, err := gitcli.NewWritebackClient(gitcli.New(""), workspaceRepository)
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := newToolRuntimeComponents(pool, cfg, workspaceRepository, gitInspector,
		toolRuntimeCompositionInput{workspaceAnalysisAudit: newWorkerTestAuditRecorder(t, pool)})
	if err != nil {
		t.Fatal(err)
	}

	agent, err := newAgentWorkflowComponentsWithTools(pool, cfg, workspaceRepository, memoryService, enabled)
	if err != nil {
		t.Fatal(err)
	}
	if agent.relation == nil || agent.rag == nil || !agent.capability.available || agent.capability.code != "" {
		t.Fatalf("agent components=%+v", agent)
	}
	if agent.workspaceInspect != nil || agent.workspaceRetrieve != nil || agent.workspaceRead != nil ||
		agent.workspaceSynthesize != nil || agent.workspaceValidate != nil || agent.workspaceReview != nil {
		t.Fatalf("test-only RAG constructor returned a partial Workspace Analysis composition: %+v", agent)
	}
	if agent.relationScheduler == nil || agent.ragScheduler == nil || agent.ragRuntimeScheduler == nil || agent.artifactScheduler == nil ||
		agent.captureScheduler == nil || agent.organizingScheduler == nil {
		t.Fatalf("Eino schedulers were not wired through Worker composition: %+v", agent)
	}

	worker, err := newWorkerComponents(pool, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), observability.NewMemoryMetrics())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.executors.Resolve(agentworkflow.RelationAssessmentNodeKind, agentworkflow.RelationAssessmentInputSchemaVersion); err != nil {
		t.Fatalf("relation executor is unreachable: %v", err)
	}
	if _, err := worker.executors.Resolve(agentworkflow.RAGWorkflowNodeKind, agentworkflow.RAGWorkflowInputSchemaVersion); err != nil {
		t.Fatalf("RAG executor is unreachable: %v", err)
	}
	for _, key := range []string{
		conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace,
		conversationworkflow.WorkspaceAnalysisNodeRetrieveEvidence,
		conversationworkflow.WorkspaceAnalysisNodeReadEvidence,
		conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer,
		conversationworkflow.WorkspaceAnalysisNodeValidateCitations,
		conversationworkflow.WorkspaceAnalysisNodeReviewPublish,
	} {
		node, found := workerWorkspaceAnalysisNode(key)
		if !found {
			t.Fatalf("workspace analysis node %s is missing", key)
		}
		if _, err := worker.executors.Resolve(node.Kind, node.InputSchemaVersion); err != nil {
			t.Fatalf("workspace analysis executor %s is unreachable: %v", key, err)
		}
	}
	if worker.artifact.generation == nil || worker.artifact.terminal == nil || worker.artifact.executor == nil || worker.artifact.catalog == nil || !worker.artifact.capability.available || worker.artifact.capability.code != "" {
		t.Fatalf("enabled artifact components=%+v", worker.artifact)
	}
	if !artifactWorkflowReadiness(worker) {
		t.Fatal("enabled Artifact composition is not ready")
	}
	enabledWithoutCatalog := worker
	enabledWithoutCatalog.artifact.catalog = nil
	if artifactWorkflowReadiness(enabledWithoutCatalog) {
		t.Fatal("enabled Artifact readiness accepted a missing runtime catalog")
	}
	if _, err := worker.executors.Resolve(artifactworkflow.NodeKind, artifactworkflow.InputSchemaVersion); err != nil {
		t.Fatalf("Artifact executor is unreachable: %v", err)
	}
	if _, err := worker.executors.Resolve(changecontrolworkflow.SafeWritebackNodeKind, changecontrolworkflow.SafeWritebackBootstrapInputSchemaVersion); err != nil {
		t.Fatalf("Safe Writeback executor regressed: %v", err)
	}
	if _, err := worker.definitions.Resolve(agentworkflow.RelationAssessmentDefinitionKey, agentworkflow.RelationAssessmentDefinitionVersion); err != nil {
		t.Fatalf("relation definition is unreachable: %v", err)
	}
	if _, err := worker.definitions.Resolve(agentworkflow.RAGWorkflowDefinitionKey, agentworkflow.RAGWorkflowDefinitionVersion); err != nil {
		t.Fatalf("RAG definition is unreachable: %v", err)
	}
	if _, err := worker.definitions.Resolve(agentworkflow.RAGWorkflowDefinitionKey, agentworkflow.RegisteredRAGDefinitionV1().Version); err != nil {
		t.Fatalf("historical RAG v1 definition is unreachable: %v", err)
	}
	workspaceAnalysisDefinition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	registeredWorkspaceAnalysis, err := worker.definitions.Resolve(workspaceAnalysisDefinition.Key, workspaceAnalysisDefinition.Version)
	if err != nil || registeredWorkspaceAnalysis.GraphHash != workspaceAnalysisDefinition.GraphHash {
		t.Fatalf("workspace analysis definition is unreachable or drifted: definition=%+v err=%v", registeredWorkspaceAnalysis, err)
	}
	if _, err := worker.definitions.Resolve(artifactworkflow.DefinitionKey, artifactworkflow.DefinitionVersion); err != nil {
		t.Fatalf("Artifact definition is unreachable: %v", err)
	}
	if _, err := worker.executors.Resolve(toolworkflow.NodeKind, toolworkflow.InputSchemaVersion); err != nil {
		t.Fatalf("enabled Tool runtime executor is unreachable: %v", err)
	}
}
