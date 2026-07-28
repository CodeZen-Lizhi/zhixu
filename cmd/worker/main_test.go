package main

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/workflow"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowhealth "github.com/CodeZen-Lizhi/zhixu/internal/workflow/httphealth"
	workflowruntime "github.com/CodeZen-Lizhi/zhixu/internal/workflow/runtime"
)

func TestRunMemoryExpiryMaintenanceUsesBoundedBatchAndReportsResult(t *testing.T) {
	service := &memoryExpiryServiceFake{expired: 3}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	runMemoryExpiryMaintenance(context.Background(), logger, service, memoryExpiryStartupPhase)

	if service.calls != 1 || service.limit != memorydomain.MaxListLimit {
		t.Fatalf("calls=%d limit=%d", service.calls, service.limit)
	}
	logged := output.String()
	if !strings.Contains(logged, `"msg":"memory expiry maintenance completed"`) ||
		!strings.Contains(logged, `"phase":"startup"`) ||
		!strings.Contains(logged, `"expired_count":3`) {
		t.Fatalf("maintenance log=%s", logged)
	}
}

func TestRunMemoryExpiryMaintenanceReportsFailureWithoutRetrying(t *testing.T) {
	service := &memoryExpiryServiceFake{expired: 2, err: errors.New("database unavailable")}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	runMemoryExpiryMaintenance(context.Background(), logger, service, memoryExpiryPeriodicPhase)

	if service.calls != 1 || service.limit != memorydomain.MaxListLimit {
		t.Fatalf("calls=%d limit=%d", service.calls, service.limit)
	}
	logged := output.String()
	if !strings.Contains(logged, `"msg":"memory expiry maintenance failed"`) ||
		!strings.Contains(logged, `"error_code":"MEMORY_EXPIRY_MAINTENANCE_FAILED"`) ||
		!strings.Contains(logged, `"phase":"periodic"`) {
		t.Fatalf("maintenance log=%s", logged)
	}
}

type memoryExpiryServiceFake struct {
	calls   int
	limit   int
	expired int
	err     error
}

func (fake *memoryExpiryServiceFake) ExpireDue(_ context.Context, limit int) (int, error) {
	fake.calls++
	fake.limit = limit
	return fake.expired, fake.err
}

func TestNewWorkerComponentsRequiresDatabase(t *testing.T) {
	components, err := newWorkerComponents(nil, config.Defaults(), nil, nil)
	if err == nil || components.safeWriteback != nil {
		t.Fatalf("components=%#v err=%v", components, err)
	}
}

func TestDisabledChatLeavesWorkerAgentCapabilityExplicitlyUnavailable(t *testing.T) {
	components, err := newAgentWorkflowComponents(nil, config.Defaults(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if components.relation != nil || components.rag != nil || components.capability.available || components.capability.code != agentworkflow.ErrorCodeCapabilityUnavailable {
		t.Fatalf("components=%+v", components)
	}
}

func TestEnabledChatFailsClosedWithoutProductionDependencies(t *testing.T) {
	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	cfg.ChatBaseURL = "http://127.0.0.1:11434/v1"
	cfg.ChatModel = "composition-test"
	cfg.ChatModelVersion = "composition-test-v1"
	components, err := newAgentWorkflowComponents(nil, cfg, nil, nil)
	if err == nil || components.relation != nil || components.rag != nil || components.capability.available {
		t.Fatalf("components=%+v err=%v", components, err)
	}
}

func TestAgentWorkflowReadinessRejectsPartialChatComposition(t *testing.T) {
	disabled := workerComponents{agentCapability: agentCapabilityStatus{code: agentworkflow.ErrorCodeCapabilityUnavailable}}
	if !agentWorkflowReadiness(disabled) {
		t.Fatal("disabled Chat must not make legacy workers unready")
	}
	for _, partial := range []workerComponents{
		{agentCapability: agentCapabilityStatus{available: true}},
		{agentCapability: agentCapabilityStatus{code: "unexpected"}},
	} {
		if agentWorkflowReadiness(partial) {
			t.Fatalf("partial Agent composition reported ready: %+v", partial.agentCapability)
		}
	}
}

func TestWorkerToolSubsetContainsOnlyFiveRealReadExecutors(t *testing.T) {
	want := []toolsdomain.ToolRef{
		{Name: "SearchKnowledge", Version: 1}, {Name: "ReadSource", Version: 1},
		{Name: "ValidateCitation", Version: 1}, {Name: "CalculateDiff", Version: 1},
		{Name: "ReadGitStatus", Version: 1},
	}
	got := enabledReadToolRefs()
	if len(got) != len(want) {
		t.Fatalf("enabled refs=%+v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("enabled[%d]=%+v want=%+v", index, got[index], want[index])
		}
	}
	for _, unavailable := range []string{"ReadDocument", "FetchWebPage", "RebuildIndex", "RunRegressionEvaluation", "ApplyApprovedPatch", "CreateGitCommit"} {
		for _, ref := range got {
			if ref.Name == unavailable {
				t.Fatalf("unavailable or trusted-only tool entered execution subset: %s", unavailable)
			}
		}
	}
}

func TestWorkerWebFetchEnabledFailsBeforeComposition(t *testing.T) {
	cfg := config.Defaults()
	cfg.ToolRuntimeMode = config.ToolModeEnabled
	cfg.WebFetchMode = config.ToolModeEnabled
	err := validateToolCompositionMode(cfg)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKER_WEB_FETCH_POLICY_UNAVAILABLE" {
		t.Fatalf("error=%v", err)
	}
}

func TestToolWorkflowReadinessRequiresReachableNodeAndDefinition(t *testing.T) {
	disabled := workerComponents{tools: toolRuntimeComponents{contracts: &toolsapplication.Registry{}}}
	contractsOK, executorsOK, dependenciesOK := toolWorkflowReadiness(disabled)
	if !contractsOK || !executorsOK || !dependenciesOK {
		t.Fatalf("disabled readiness contracts=%t executors=%t dependencies=%t", contractsOK, executorsOK, dependenciesOK)
	}
	incomplete := workerComponents{tools: toolRuntimeComponents{runtimeEnabled: true, contracts: &toolsapplication.Registry{}}}
	contractsOK, executorsOK, dependenciesOK = toolWorkflowReadiness(incomplete)
	if !contractsOK || executorsOK || dependenciesOK {
		t.Fatalf("incomplete readiness contracts=%t executors=%t dependencies=%t", contractsOK, executorsOK, dependenciesOK)
	}
	production := toolworkflow.ModelReadToolRefsV1()
	if len(production) != 3 || len(enabledReadToolRefs()) != 5 {
		t.Fatalf("production Tool refs=%d execution refs=%d", len(production), len(enabledReadToolRefs()))
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
