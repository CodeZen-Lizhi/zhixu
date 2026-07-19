package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"testing"
	"time"

	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowhealth "github.com/CodeZen-Lizhi/zhixu/internal/workflow/httphealth"
	workflowruntime "github.com/CodeZen-Lizhi/zhixu/internal/workflow/runtime"
)

func TestNewWorkerComponentsRequiresDatabase(t *testing.T) {
	components, err := newWorkerComponents(nil, config.Defaults(), nil, nil)
	if err == nil || components.safeWriteback != nil {
		t.Fatalf("components=%#v err=%v", components, err)
	}
}

func TestDisabledChatLeavesWorkerAgentCapabilityExplicitlyUnavailable(t *testing.T) {
	components, err := newAgentWorkflowComponents(nil, config.Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if components.executor != nil || components.capability.available || components.capability.code != agentworkflow.ErrorCodeCapabilityUnavailable {
		t.Fatalf("components=%+v", components)
	}
}

func TestAgentApplicationBudgetAccountsForAllThreeStructuredCalls(t *testing.T) {
	cfg := config.Defaults()
	budget := agentApplicationBudget(cfg)
	if budget.MaxRequestBytes != cfg.ChatMaxRequestBytes*agentapplication.StructuredCallLimit ||
		budget.MaxResponseBytes != cfg.ChatMaxResponseBytes*agentapplication.StructuredCallLimit ||
		budget.Timeout != cfg.ChatTimeout*time.Duration(agentapplication.StructuredCallLimit) {
		t.Fatalf("budget=%+v", budget)
	}
}

func TestWorkerCompositionUsesSharedConfiguredEmbedderFactory(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var reindexFactoryCalls int
	var foundReindexComposition bool
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if function.Name.Name == "newConfiguredEmbedder" {
			t.Fatal("worker must not own a private configured embedder factory")
		}
		if function.Name.Name != "newReindexComponents" {
			continue
		}
		foundReindexComposition = true
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "NewConfiguredEmbedder" {
				return true
			}
			packageName, ok := selector.X.(*ast.Ident)
			if ok && packageName.Name == "platformmodels" {
				reindexFactoryCalls++
			}
			return true
		})
	}
	if !foundReindexComposition {
		t.Fatal("worker reindex composition root was not found")
	}
	if reindexFactoryCalls != 1 {
		t.Fatalf("reindex shared configured embedder factory calls=%d want=1", reindexFactoryCalls)
	}
}

func TestConfiguredRRFUsesVersionedTypedLimits(t *testing.T) {
	cfg := config.Defaults()
	raw, err := configuredRRF(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rrf, err := retrievaldomain.ParseRRFConfig(raw)
	if err != nil || rrf.K != cfg.RetrievalRRFK || rrf.FusedCandidateLimit != cfg.RetrievalRRFFusedCandidateLimit {
		t.Fatalf("rrf=%#v raw=%s err=%v", rrf, raw, err)
	}
}

func TestRecordShutdownMetricMapsLifecycleModesToBoundedLabels(t *testing.T) {
	metrics := observability.NewMemoryMetrics()
	if err := recordShutdownMetric(metrics, shutdownGraceful, "success"); err != nil {
		t.Fatal(err)
	}
	if err := recordShutdownMetric(metrics, shutdownEmergency, "failure"); err != nil {
		t.Fatal(err)
	}
	snapshot := metrics.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("measurements=%d", len(snapshot))
	}
	if got := snapshot[0].Labels.Map()["shutdown_kind"]; got != "graceful" {
		t.Fatalf("graceful label=%q", got)
	}
	if got := snapshot[1].Labels.Map()["shutdown_kind"]; got != "forced" {
		t.Fatalf("forced label=%q", got)
	}
}

func TestStartWorkerHealthServerServesReadinessAndCloses(t *testing.T) {
	readiness := workflowruntime.NewReadiness()
	readiness.SetDatabaseOK(true)
	readiness.SetRiverSchemaOK(true)
	readiness.SetRiverStarted(true)
	readiness.SetDefinitionsOK(true)
	readiness.SetExecutorsOK(true)
	readiness.SetDependenciesOK(true)
	readiness.SetReindexDispatcherStarted(true)

	health, err := startWorkerHealthServer("127.0.0.1:0", workflowhealth.NewHandler(readiness))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get("http://" + health.address + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("readyz status=%d", response.StatusCode)
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := health.server.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-health.errors:
		if err != nil {
			t.Fatalf("serve err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("health server did not stop")
	}
	if _, err := http.Get("http://" + health.address + "/livez"); err == nil {
		t.Fatal("health server still accepts requests after shutdown")
	}
}
