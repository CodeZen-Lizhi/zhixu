//go:build integration

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	changecontrolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	toolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/workflow"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkerToolCompositionSeparatesContractsExecutorsAndTrustedAudit(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated PostgreSQL database")
	}
	pool := newMigratedWorkerTestPool(t, databaseURL)
	workspaceRepository, err := workspacepostgres.NewRepository(pool)
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
	enabled, err := newToolRuntimeComponents(pool, cfg, workspaceRepository, gitInspector)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.contracts == nil || enabled.executions == nil || enabled.execution == nil || enabled.repository == nil || enabled.writebackAudit == nil || enabled.workflow == nil || enabled.definition == nil || !enabled.runtimeEnabled || len(enabled.enabledRefs) != 5 {
		t.Fatalf("enabled components=%+v", enabled)
	}
	for _, ref := range enabled.enabledRefs {
		executor, err := enabled.executions.ResolveExecutor(ref)
		if err != nil {
			t.Fatalf("resolve executor %s: %v", ref.Name, err)
		}
		if _, ok := executor.(toolsapplication.ResultReceiptLoader); !ok {
			t.Fatalf("executor %s lacks canonical replay loader", ref.Name)
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

func newMigratedWorkerTestPool(t *testing.T, baseURL string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_worker_rag_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	databaseURL := parsed.String()
	migrationPool, err := platformpostgres.OpenMigration(ctx, databaseURL, 4, 0)
	if err == nil {
		var runner *platformmigration.Runner
		runner, err = platformmigration.NewRunner(migrationPool.DB(), projectmigrations.FS)
		if err == nil {
			err = runner.Up(ctx)
		}
		migrationPool.Close()
	}
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	return pool
}

func TestWorkerChatCompositionRegistersRelationAndRAGTogether(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated PostgreSQL database")
	}
	pool := newMigratedWorkerTestPool(t, databaseURL)
	var requiredTables int
	var tableNames string
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*),COALESCE(string_agg(table_schema||'.'||table_name,',' ORDER BY table_schema,table_name),'')
		FROM information_schema.tables
		WHERE (table_schema,table_name) IN (
			('agent','answer'),('agent','model_run'),('ops','server_event'),('workflow','run')
		)`).Scan(&requiredTables, &tableNames); err != nil || requiredTables != 4 {
		t.Fatalf("required RAG PostgreSQL table count=%d tables=%q err=%v", requiredTables, tableNames, err)
	}
	workspaceRepository, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	cfg.ChatBaseURL = "http://127.0.0.1:11434/v1"
	cfg.ChatModel = "composition-test"
	cfg.ChatModelVersion = "composition-test-v1"

	agent, err := newAgentWorkflowComponents(pool, cfg, workspaceRepository)
	if err != nil {
		t.Fatal(err)
	}
	if agent.relation == nil || agent.rag == nil || !agent.capability.available || agent.capability.code != "" {
		t.Fatalf("agent components=%+v", agent)
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
	if _, err := worker.executors.Resolve(changecontrolworkflow.SafeWritebackNodeKind, changecontrolworkflow.SafeWritebackBootstrapInputSchemaVersion); err != nil {
		t.Fatalf("Safe Writeback executor regressed: %v", err)
	}
	if _, err := worker.definitions.Resolve(agentworkflow.RelationAssessmentDefinitionKey, agentworkflow.RelationAssessmentDefinitionVersion); err != nil {
		t.Fatalf("relation definition is unreachable: %v", err)
	}
	if _, err := worker.definitions.Resolve(agentworkflow.RAGWorkflowDefinitionKey, agentworkflow.RAGWorkflowDefinitionVersion); err != nil {
		t.Fatalf("RAG definition is unreachable: %v", err)
	}
	if _, err := worker.executors.Resolve(toolworkflow.NodeKind, toolworkflow.InputSchemaVersion); err == nil {
		t.Fatal("disabled Tool runtime unexpectedly registered the Tool executor")
	}
}
