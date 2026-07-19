//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	toolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/workflow"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkerToolCompositionSeparatesContractsExecutorsAndTrustedAudit(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated PostgreSQL database")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
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
