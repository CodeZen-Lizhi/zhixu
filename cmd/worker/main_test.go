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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	captureapplication "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	captureprofile "github.com/CodeZen-Lizhi/zhixu/internal/capture/profile"
	captureworkflow "github.com/CodeZen-Lizhi/zhixu/internal/capture/workflow"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/workflow"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowhealth "github.com/CodeZen-Lizhi/zhixu/internal/workflow/httphealth"
	workflowruntime "github.com/CodeZen-Lizhi/zhixu/internal/workflow/runtime"
)

func TestInitializeWorkerTelemetryHonorsConfiguredMode(t *testing.T) {
	for _, test := range []struct {
		name          string
		mode          config.TelemetryMode
		wantExporting bool
	}{
		{name: "disabled", mode: config.TelemetryModeDisabled},
		{name: "optional exporter configured", mode: config.TelemetryModeOptional, wantExporting: true},
		{name: "required exporter configured", mode: config.TelemetryModeRequired, wantExporting: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.TelemetryMode = test.mode
			if test.mode != config.TelemetryModeDisabled {
				collector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					writer.Header().Set("Content-Type", "application/x-protobuf")
					writer.WriteHeader(http.StatusOK)
				}))
				defer collector.Close()
				cfg.TelemetryEndpoint = collector.URL
			}
			telemetry, err := initializeWorkerTelemetry(context.Background(), cfg)
			if err != nil {
				t.Fatalf("initializeWorkerTelemetry: %v", err)
			}
			defer func() {
				if shutdownErr := telemetry.Shutdown(context.Background()); shutdownErr != nil {
					t.Fatalf("telemetry shutdown: %v", shutdownErr)
				}
			}()
			if telemetry.Tracer() == nil || telemetry.Status().Degraded || telemetry.Status().Exporting != test.wantExporting {
				t.Fatalf("telemetry=%#v tracer=%#v", telemetry.Status(), telemetry.Tracer())
			}
		})
	}
}

func TestWorkerRunRecordsProcessPresenceMetric(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (selector.Sel.Name != "RecordProcessPresence" && selector.Sel.Name != "RecordTelemetryRequired") {
			return true
		}
		packageName, ok := selector.X.(*ast.Ident)
		if ok && packageName.Name == "observability" {
			found[selector.Sel.Name] = true
		}
		return true
	})
	if !found["RecordProcessPresence"] || !found["RecordTelemetryRequired"] {
		t.Fatalf("Worker composition telemetry signals=%#v", found)
	}
}

func TestWorkerShutdownDeadlinesReserveTelemetryFlushWithinHardStop(t *testing.T) {
	startedAt := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name             string
		softStop         time.Duration
		hardStop         time.Duration
		telemetryTimeout time.Duration
		wantRuntime      time.Duration
		wantHard         time.Duration
	}{
		{name: "configured flush budget", softStop: 30 * time.Second, hardStop: 60 * time.Second, telemetryTimeout: 10 * time.Second, wantRuntime: 50 * time.Second, wantHard: 60 * time.Second},
		{name: "flush budget capped by soft stop reserve", softStop: 50 * time.Second, hardStop: 60 * time.Second, telemetryTimeout: 30 * time.Second, wantRuntime: 50 * time.Second, wantHard: 60 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtimeDeadline, hardDeadline := workerShutdownDeadlines(startedAt, test.softStop, test.hardStop, test.telemetryTimeout)
			if got := runtimeDeadline.Sub(startedAt); got != test.wantRuntime {
				t.Fatalf("runtime deadline=%s, want=%s", got, test.wantRuntime)
			}
			if got := hardDeadline.Sub(startedAt); got != test.wantHard {
				t.Fatalf("hard deadline=%s, want=%s", got, test.wantHard)
			}
		})
	}
}

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

func TestRunDraftStreamCleanupUsesBoundedBatchAndReportsResult(t *testing.T) {
	service := &draftStreamCleanupServiceFake{deleted: 3}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	runDraftStreamCleanup(context.Background(), logger, service, draftStreamCleanupStartupPhase)

	if service.calls != 1 || service.limit != draftStreamCleanupBatchSize {
		t.Fatalf("calls=%d limit=%d", service.calls, service.limit)
	}
	logged := output.String()
	if !strings.Contains(logged, `"msg":"answer draft stream cleanup completed"`) ||
		!strings.Contains(logged, `"phase":"startup"`) ||
		!strings.Contains(logged, `"deleted_count":3`) ||
		!strings.Contains(logged, `"batch_size":100`) {
		t.Fatalf("cleanup log=%s", logged)
	}
}

func TestRunDraftStreamCleanupReportsFailureWithoutStopping(t *testing.T) {
	service := &draftStreamCleanupServiceFake{err: errors.New("database unavailable")}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	runDraftStreamCleanup(context.Background(), logger, service, draftStreamCleanupPeriodicPhase)

	if service.calls != 1 || service.limit != draftStreamCleanupBatchSize {
		t.Fatalf("calls=%d limit=%d", service.calls, service.limit)
	}
	logged := output.String()
	if !strings.Contains(logged, `"msg":"answer draft stream cleanup failed"`) ||
		!strings.Contains(logged, `"error_code":"ANSWER_DRAFT_STREAM_CLEANUP_FAILED"`) ||
		!strings.Contains(logged, `"phase":"periodic"`) {
		t.Fatalf("cleanup log=%s", logged)
	}
}

type draftStreamCleanupServiceFake struct {
	calls   int
	limit   int
	deleted int64
	err     error
}

func (fake *draftStreamCleanupServiceFake) CleanupExpiredDraftStreams(_ context.Context, limit int) (int64, error) {
	fake.calls++
	fake.limit = limit
	return fake.deleted, fake.err
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

func TestDispatchGitSyncOutboxDrainsBoundedWork(t *testing.T) {
	results := make([]gitSyncWorkerResult, gitSyncDispatchBatchSize+1)
	for index := range results {
		results[index].worked = true
	}
	worker := &gitSyncWorkerFake{results: results}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	processed, err := dispatchGitSyncOutbox(context.Background(), logger, worker, gitSyncDispatchStartupPhase)
	if err != nil || processed != gitSyncDispatchBatchSize || worker.calls != gitSyncDispatchBatchSize {
		t.Fatalf("processed=%d calls=%d err=%v", processed, worker.calls, err)
	}
	if !strings.Contains(output.String(), `"msg":"Git sync outbox dispatch completed"`) ||
		!strings.Contains(output.String(), `"processed_count":10`) {
		t.Fatalf("dispatch log=%s", output.String())
	}
}

func TestDispatchGitSyncOutboxDoesNothingWhenCapabilityIsDisabled(t *testing.T) {
	processed, err := dispatchGitSyncOutbox(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, gitSyncDispatchPeriodicPhase)
	if err != nil || processed != 0 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	worker, scheduler, err := newGitSyncWorker(nil, config.Defaults(), nil, nil, sourceProcessingComponents{}, nil, "")
	if err != nil || worker != nil || scheduler != nil {
		t.Fatalf("worker=%v scheduler=%v err=%v", worker, scheduler, err)
	}
}

type gitSyncWorkerResult struct {
	worked bool
	err    error
}

type gitSyncWorkerFake struct {
	results []gitSyncWorkerResult
	calls   int
}

func (fake *gitSyncWorkerFake) RunOnce(context.Context) (bool, error) {
	if fake.calls >= len(fake.results) {
		return false, nil
	}
	result := fake.results[fake.calls]
	fake.calls++
	return result.worked, result.err
}

func TestNewWorkerComponentsRequiresDatabase(t *testing.T) {
	components, err := newWorkerComponents(nil, config.Defaults(), nil, nil)
	if err == nil || components.safeWriteback != nil {
		t.Fatalf("components=%#v err=%v", components, err)
	}
}

func TestWorkerModelRuntimeBindingSharesOneManagedInstance(t *testing.T) {
	staticModels, err := modelRuntimeForComposition(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	staticBinding, err := newWorkerModelRuntimeBinding(config.ModelSettingsModeStatic, staticModels, nil)
	if err != nil || staticBinding.revision != nil || staticBinding.instanceID != nil || len(staticBinding.runtimeWorkerOptions()) != 0 {
		t.Fatalf("static binding=%+v err=%v", staticBinding, err)
	}

	managedConfig := config.Defaults()
	managedConfig.ModelSettingsMode = config.ModelSettingsModeManaged
	managedConfig.ModelSettingsKeyFile = "/run/secrets/model-settings.key"
	managedModels, err := modelsettingsruntime.Build(managedConfig, modelsettingsdomain.ResolvedSettings{Revision: 0, Settings: modelsettingsdomain.CanonicalDisabledSettings()})
	if err != nil {
		t.Fatal(err)
	}
	ids := &workerIDGeneratorFake{id: foundation.ID("10000000-0000-4000-8000-000000000001")}
	binding, err := newWorkerModelRuntimeBinding(config.ModelSettingsModeManaged, managedModels, ids)
	if err != nil {
		t.Fatal(err)
	}
	options := binding.runtimeWorkerOptions()
	if ids.calls != 1 || binding.revision == nil || *binding.revision != 0 || binding.instanceID == nil || *binding.instanceID != ids.id ||
		len(options) != 1 || options[0].ModelSettingsRevision != binding.revision || options[0].ModelRuntimeInstanceID != binding.instanceID {
		t.Fatalf("binding=%+v options=%+v calls=%d", binding, options, ids.calls)
	}
}

type workerIDGeneratorFake struct {
	id    foundation.ID
	calls int
}

func (generator *workerIDGeneratorFake) New() (foundation.ID, error) {
	generator.calls++
	return generator.id, nil
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

func TestEinoRAGCompositionFailsClosedWithoutToolRuntime(t *testing.T) {
	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	cfg.ChatBaseURL = "http://127.0.0.1:11434/v1"
	cfg.ChatModel = "composition-test"
	cfg.ChatModelVersion = "composition-test-v1"
	_, err := newAgentWorkflowComponentsWithTools(nil, cfg, nil, nil, toolRuntimeComponents{})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKER_RAG_EINO_TOOL_RUNTIME_UNAVAILABLE" {
		t.Fatalf("error=%v", err)
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

func TestWorkspaceAnalysisExecutorRegistrationRejectsPartialComposition(t *testing.T) {
	if err := registerWorkerWorkspaceAnalysisExecutors(nil, agentWorkflowComponents{}); err != nil {
		t.Fatalf("empty workspace analysis executor set should remain disabled: %v", err)
	}
	for _, partial := range []agentWorkflowComponents{
		{workspaceInspect: &agentworkflow.WorkspaceAnalysisInspectExecutor{}},
		{workspaceRetrieve: &agentworkflow.WorkspaceAnalysisRetrieveExecutor{}},
		{workspaceRead: &agentworkflow.WorkspaceAnalysisReadEvidenceExecutor{}},
		{workspaceSynthesize: &agentworkflow.WorkspaceAnalysisSynthesizeExecutor{}},
		{workspaceValidate: &agentworkflow.WorkspaceAnalysisValidateCitationsExecutor{}},
		{workspaceReview: &agentworkflow.WorkspaceAnalysisReviewPublishExecutor{}},
		{
			workspaceInspect:  &agentworkflow.WorkspaceAnalysisInspectExecutor{},
			workspaceRetrieve: &agentworkflow.WorkspaceAnalysisRetrieveExecutor{},
		},
	} {
		err := registerWorkerWorkspaceAnalysisExecutors(nil, partial)
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Code != "WORKER_WORKSPACE_ANALYSIS_EXECUTORS_UNAVAILABLE" {
			t.Fatalf("partial=%+v error=%v", partial, err)
		}
	}
	complete := agentWorkflowComponents{
		workspaceInspect:    &agentworkflow.WorkspaceAnalysisInspectExecutor{},
		workspaceRetrieve:   &agentworkflow.WorkspaceAnalysisRetrieveExecutor{},
		workspaceRead:       &agentworkflow.WorkspaceAnalysisReadEvidenceExecutor{},
		workspaceSynthesize: &agentworkflow.WorkspaceAnalysisSynthesizeExecutor{},
		workspaceValidate:   &agentworkflow.WorkspaceAnalysisValidateCitationsExecutor{},
		workspaceReview:     &agentworkflow.WorkspaceAnalysisReviewPublishExecutor{},
	}
	err := registerWorkerWorkspaceAnalysisExecutors(nil, complete)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKER_WORKSPACE_ANALYSIS_EXECUTOR_REGISTRY_UNAVAILABLE" {
		t.Fatalf("complete nil-registry error=%v", err)
	}
}

func TestWorkspaceAnalysisUnavailableSkipsAllWorkflowExecutorRegistrations(t *testing.T) {
	catalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("fixed-rag", 1, workerUnavailableExecutor{code: agentworkflow.ErrorCodeCapabilityUnavailable}); err != nil {
		t.Fatal(err)
	}
	components := agentWorkflowComponents{
		workspaceAnalysisCapability: agentCapabilityStatus{code: agentapplication.ErrorCodeWorkspaceAnalysisCapabilityUnavailable},
	}
	if err := registerWorkerWorkspaceAnalysisExecutors(registry, components); err != nil {
		t.Fatalf("unavailable optional composition must not block fixed executor registry: %v", err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	for _, node := range conversationworkflow.RegisteredWorkspaceAnalysisDefinition().Graph.Nodes {
		if registry.SupportsContract(node.Kind, node.InputSchemaVersion) {
			t.Fatalf("workspace analysis executor remained registered: %s@%d", node.Kind, node.InputSchemaVersion)
		}
	}
}

func TestWorkerToolSubsetContainsOnlyApprovedRealReadExecutors(t *testing.T) {
	want := []toolsdomain.ToolRef{
		{Name: "SearchKnowledge", Version: 1}, {Name: "ReadSource", Version: 1},
		{Name: "ValidateCitation", Version: 1}, {Name: "CalculateDiff", Version: 1},
		{Name: "ReadGitStatus", Version: 1},
		{Name: "ReadSource", Version: 2}, {Name: "ValidateCitation", Version: 2},
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
	if len(production) != 3 || len(enabledReadToolRefs()) != 7 {
		t.Fatalf("production Tool refs=%d execution refs=%d", len(production), len(enabledReadToolRefs()))
	}
}

func TestAgentApplicationBudgetAccountsForAllThreeStructuredCalls(t *testing.T) {
	cfg := config.Defaults()
	contract := platformmodels.ChatContract{
		Timeout:          cfg.ChatTimeout,
		MaxRequestBytes:  cfg.ChatMaxRequestBytes,
		MaxResponseBytes: cfg.ChatMaxResponseBytes,
	}
	budget := agentApplicationBudget(contract)
	if budget.MaxRequestBytes != cfg.ChatMaxRequestBytes*agentapplication.StructuredCallLimit ||
		budget.MaxResponseBytes != cfg.ChatMaxResponseBytes*agentapplication.StructuredCallLimit ||
		budget.Timeout != cfg.ChatTimeout*time.Duration(agentapplication.StructuredCallLimit) {
		t.Fatalf("budget=%+v", budget)
	}
}

func TestRAGAttemptTimeoutReservesFullModelCallSequence(t *testing.T) {
	cfg := config.Defaults()
	if got, want := agentworkflow.RAGAttemptTimeout(cfg.ChatTimeout), cfg.ChatTimeout*10; got != want {
		t.Fatalf("rag attempt timeout=%s want=%s", got, want)
	}
	if got := agentworkflow.RAGAttemptTimeout(2 * time.Hour); got != agentworkflow.MaxRAGAttemptTimeout {
		t.Fatalf("capped rag attempt timeout=%s want=%s", got, agentworkflow.MaxRAGAttemptTimeout)
	}
}

func TestValidateRAGWorkerJobTimeoutRequiresAttemptHeadroom(t *testing.T) {
	modelCallTimeout := 30 * time.Second
	attemptTimeout := agentworkflow.RAGAttemptTimeout(modelCallTimeout)
	minimumJobTimeout := attemptTimeout + ragWorkerJobTimeoutHeadroom
	if err := validateRAGWorkerJobTimeout(minimumJobTimeout-time.Nanosecond, modelCallTimeout); err == nil {
		t.Fatal("expected insufficient non-model headroom to be rejected")
	}
	if err := validateRAGWorkerJobTimeout(minimumJobTimeout, modelCallTimeout); err != nil {
		t.Fatalf("validate timeout with headroom: %v", err)
	}
}

func TestWorkspaceAnalysisWorkerRuntimeReadinessUsesFrozenToolTimeouts(t *testing.T) {
	contracts, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	if err := validateWorkspaceAnalysisWorkerRuntime(cfg, cfg.ChatTimeout, contracts); err != nil {
		t.Fatalf("default runtime readiness: %v", err)
	}

	invalidJob := cfg
	invalidJob.WorkerJobTimeout = time.Second
	if err := validateWorkspaceAnalysisWorkerRuntime(invalidJob, cfg.ChatTimeout, contracts); workspaceAnalysisWorkerErrorCode(err) != "WORKER_WORKSPACE_ANALYSIS_RUNTIME_BUDGET_INVALID" {
		t.Fatalf("job timeout error=%v", err)
	}
	invalidLease := cfg
	invalidLease.WorkflowLeaseDuration = agentdomain.WorkspaceAnalysisV1DurableCompletionMargin
	invalidLease.WorkflowHeartbeatInterval = time.Second
	if err := validateWorkspaceAnalysisWorkerRuntime(invalidLease, cfg.ChatTimeout, contracts); workspaceAnalysisWorkerErrorCode(err) != "WORKER_WORKSPACE_ANALYSIS_RUNTIME_BUDGET_INVALID" {
		t.Fatalf("lease error=%v", err)
	}
	if err := validateWorkspaceAnalysisWorkerRuntime(cfg, cfg.ChatTimeout, nil); workspaceAnalysisWorkerErrorCode(err) != "WORKER_WORKSPACE_ANALYSIS_RUNTIME_CONTRACT_UNAVAILABLE" {
		t.Fatalf("contracts error=%v", err)
	}
}

func workspaceAnalysisWorkerErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func TestDispatchCaptureOutboxUsesBoundedBatchAndRedactedLogs(t *testing.T) {
	dispatcher := &captureOutboxDispatcherFake{result: captureapplication.DispatchBatchResult{
		Claimed: 3, Started: 2, Retried: 1,
	}}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	result, err := dispatchCaptureOutbox(context.Background(), logger, dispatcher, captureDispatchStartupPhase)
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.calls != 1 || dispatcher.limit != captureDispatchBatchSize || result != dispatcher.result {
		t.Fatalf("calls=%d limit=%d result=%+v", dispatcher.calls, dispatcher.limit, result)
	}
	logged := output.String()
	if !strings.Contains(logged, `"msg":"capture outbox dispatch completed"`) ||
		!strings.Contains(logged, `"phase":"startup"`) || !strings.Contains(logged, `"started_count":2`) {
		t.Fatalf("dispatch log=%s", logged)
	}

	output.Reset()
	dispatcher.err = errors.New("postgres password=must-not-leak")
	_, err = dispatchCaptureOutbox(context.Background(), logger, dispatcher, captureDispatchPeriodicPhase)
	if err == nil {
		t.Fatal("capture dispatch failure was hidden")
	}
	logged = output.String()
	if !strings.Contains(logged, `"error_code":"CAPTURE_OUTBOX_DISPATCH_FAILED"`) || strings.Contains(logged, "must-not-leak") {
		t.Fatalf("failure log=%s", logged)
	}
}

func TestCaptureDispatchUsesDedicatedBoundedCadence(t *testing.T) {
	if captureDispatchInterval != time.Second {
		t.Fatalf("capture dispatch interval=%s", captureDispatchInterval)
	}
	if captureDispatchBatchSize < 1 || captureDispatchBatchSize > 100 {
		t.Fatalf("capture dispatch batch=%d", captureDispatchBatchSize)
	}
}

func TestCaptureWorkflowReadinessRequiresFrozenExecutorDefinitionAndOutbox(t *testing.T) {
	catalog, err := workflowapplication.NewValidationCatalog([]int{1, 2}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	executor := new(captureworkflow.Executor)
	if err := executors.Register(captureapplication.ProcessingNodeKind, captureapplication.ProcessingInputSchemaVersion, executor); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := captureworkflow.RegisteredDefinition()
	if err != nil {
		t.Fatal(err)
	}
	if err := definitions.Register(definition); err != nil {
		t.Fatal(err)
	}
	if err := definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	components := workerComponents{
		captureExecutor: executor, captureOutbox: &captureOutboxDispatcherFake{},
		captureProfile: agentCapabilityStatus{code: captureprofile.ErrorCodeCapabilityUnavailable},
		executors:      executors, definitions: definitions,
	}
	if !captureWorkflowReadiness(components) {
		t.Fatal("complete Capture composition reported unavailable")
	}
	components.captureOutbox = nil
	if captureWorkflowReadiness(components) {
		t.Fatal("Capture composition without outbox reported ready")
	}
	components.captureOutbox = &captureOutboxDispatcherFake{}
	components.captureProfile = agentCapabilityStatus{}
	if captureWorkflowReadiness(components) {
		t.Fatal("Capture composition hid an invalid Profile capability state")
	}
}

func TestNewCaptureProfileGeneratorRequiresPersistenceWhenCapabilityIsDisabled(t *testing.T) {
	generator, capabilityStatus, err := newCaptureProfileGenerator(
		nil, nil, platformmodels.ChatContract{}, nil,
		nil,
		foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err == nil || generator != nil || capabilityStatus.available || capabilityStatus.code != captureprofile.ErrorCodeCapabilityUnavailable {
		t.Fatalf("generator=%T capability=%+v", generator, capabilityStatus)
	}
}

type captureOutboxDispatcherFake struct {
	calls  int
	limit  int
	result captureapplication.DispatchBatchResult
	err    error
}

func (dispatcher *captureOutboxDispatcherFake) DispatchBatch(_ context.Context, limit int) (captureapplication.DispatchBatchResult, error) {
	dispatcher.calls++
	dispatcher.limit = limit
	return dispatcher.result, dispatcher.err
}

func TestWorkerCompositionConsumesOneFrozenModelRuntime(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	factoryCalls := 0
	consumers := map[string]bool{
		"newToolRuntimeComponents":                      false,
		"newAgentWorkflowComponentsWithToolsAndMetrics": false,
		"newArtifactWorkflowComponents":                 false,
		"newSourceProcessingComponents":                 false,
		"newReindexComponents":                          false,
	}
	containsModels := func(node ast.Node) bool {
		found := false
		ast.Inspect(node, func(child ast.Node) bool {
			identifier, ok := child.(*ast.Ident)
			if ok && identifier.Name == "models" {
				found = true
				return false
			}
			return !found
		})
		return found
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok && (selector.Sel.Name == "NewConfiguredEmbedder" || selector.Sel.Name == "NewConfiguredChatModel") {
				factoryCalls++
			}
			if function.Name.Name == "newWorkerComponentsWithModels" {
				if identifier, ok := call.Fun.(*ast.Ident); ok {
					if _, tracked := consumers[identifier.Name]; tracked && containsModels(call) {
						consumers[identifier.Name] = true
					}
				}
			}
			return true
		})
	}
	if factoryCalls != 0 {
		t.Fatalf("worker composition directly called configured model factories %d times", factoryCalls)
	}
	for consumer, injected := range consumers {
		if !injected {
			t.Fatalf("%s did not receive the shared frozen model runtime", consumer)
		}
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
