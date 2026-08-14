package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentknowledge "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/knowledge"
	agentmemory "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/memory"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentretrieval "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/retrieval"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	artifactauthoring "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/authoring"
	artifactpostgres "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/postgres"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactworkflow "github.com/CodeZen-Lizhi/zhixu/internal/artifact/workflow"
	authoringchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringpostgres "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	capturehttpfetch "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/httpfetch"
	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	captureapplication "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	captureprofile "github.com/CodeZen-Lizhi/zhixu/internal/capture/profile"
	captureworkflow "github.com/CodeZen-Lizhi/zhixu/internal/capture/workflow"
	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontrolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/workflow"
	collectionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/collection/adapter/postgres"
	collectionapplication "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	conversationpostgres "github.com/CodeZen-Lizhi/zhixu/internal/conversation/adapter/postgres"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	exportcollection "github.com/CodeZen-Lizhi/zhixu/internal/export/adapter/collection"
	exportlocalfs "github.com/CodeZen-Lizhi/zhixu/internal/export/adapter/localfs"
	exportpostgres "github.com/CodeZen-Lizhi/zhixu/internal/export/adapter/postgres"
	exportriver "github.com/CodeZen-Lizhi/zhixu/internal/export/adapter/river"
	exportapplication "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	gitsyncpostgres "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/adapter/postgres"
	gitsyncsecurity "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/adapter/security"
	gitsourcecapture "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/adapter/sourcecapture"
	gitsyncapplication "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	gitsyncdomain "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	graphpostgres "github.com/CodeZen-Lizhi/zhixu/internal/graph/adapter/postgres"
	graphworkflow "github.com/CodeZen-Lizhi/zhixu/internal/graph/adapter/workflow"
	graphapplication "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	healthcollection "github.com/CodeZen-Lizhi/zhixu/internal/health/adapter/collection"
	healthpostgres "github.com/CodeZen-Lizhi/zhixu/internal/health/adapter/postgres"
	healthworkflowadapter "github.com/CodeZen-Lizhi/zhixu/internal/health/adapter/workflow"
	healthapplication "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	healthdetector "github.com/CodeZen-Lizhi/zhixu/internal/health/detector"
	healthworkflow "github.com/CodeZen-Lizhi/zhixu/internal/health/workflow"
	ingestionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/adapter/postgres"
	ingestionworkspace "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/adapter/workspace"
	ingestionapplication "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	localmodelruntime "github.com/CodeZen-Lizhi/zhixu/internal/localmodelruntime"
	memorypostgres "github.com/CodeZen-Lizhi/zhixu/internal/memory/adapter/postgres"
	memoryapplication "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	organizingpostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/postgres"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitoperation"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	platformparser "github.com/CodeZen-Lizhi/zhixu/internal/platform/parser"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	reindexriver "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/river"
	retrievalworkspace "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/workspace"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	retrievalruntime "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/runtime"
	interviewpostgres "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/adapter/postgres"
	interviewapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	learningpathpostgres "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/adapter/postgres"
	learningpathapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/changecontrol"
	toolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/postgres"
	toolretrieval "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/retrieval"
	toolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/workflow"
	toolworkspace "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/workspace"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	workflowhealth "github.com/CodeZen-Lizhi/zhixu/internal/workflow/httphealth"
	workflowruntime "github.com/CodeZen-Lizhi/zhixu/internal/workflow/runtime"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	workspaceruntimegrant "github.com/CodeZen-Lizhi/zhixu/internal/workspace/runtimegrant"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// agentStructuredMaxOutputTokens 是 Agent 各结构化阶段单次响应的生产上限。
	agentStructuredMaxOutputTokens = 8192
	// ragWorkerJobTimeoutHeadroom reserves bounded non-model work around the
	// ten-call RAG Attempt, including retrieval, persistence, and finalization.
	ragWorkerJobTimeoutHeadroom          = 10 * time.Minute
	timelineProjectionDispatchErrorCode  = "KNOWLEDGE_TIMELINE_PROJECTION_FAILED"
	timelineProjectionStartupPhase       = "startup"
	timelineProjectionPeriodicPhase      = "periodic"
	citationBackfillStartupPhase         = "startup"
	citationBackfillPeriodicPhase        = "periodic"
	exportMaintenanceStartupPhase        = "startup"
	exportMaintenancePeriodicPhase       = "periodic"
	exportOrphanWorkspaceBatch           = 25
	memoryExpiryStartupPhase             = "startup"
	memoryExpiryPeriodicPhase            = "periodic"
	interviewCompletionStartupPhase      = "startup"
	interviewCompletionPeriodicPhase     = "periodic"
	learningPathMaintenanceStartupPhase  = "startup"
	learningPathMaintenancePeriodicPhase = "periodic"
	draftStreamCleanupStartupPhase       = "startup"
	draftStreamCleanupPeriodicPhase      = "periodic"
	draftStreamCleanupBatchSize          = 100
	captureDispatchStartupPhase          = "startup"
	captureDispatchPeriodicPhase         = "periodic"
	captureDispatchInterval              = time.Second
	captureDispatchBatchSize             = 25
	captureDispatchLeaseDuration         = 30 * time.Second
	captureDispatchRetryBase             = time.Second
	captureDispatchMaxAttempts           = 10
	organizingDispatchStartupPhase       = "startup"
	organizingDispatchPeriodicPhase      = "periodic"
	organizingDispatchBatchSize          = 25
	organizingDispatchLeaseDuration      = 30 * time.Second
	organizingDispatchRetryBase          = time.Second
	organizingDispatchMaxAttempts        = 10
	gitSyncDispatchStartupPhase          = "startup"
	gitSyncDispatchPeriodicPhase         = "periodic"
	gitSyncDispatchInterval              = time.Second
	gitSyncScheduleBatchSize             = 25
	gitSyncDispatchBatchSize             = 10
	gitSyncScheduleTimeout               = 5 * time.Second
	gitSyncDispatchTimeout               = 30 * time.Second
)

type exportMaintenanceService interface {
	Recover(context.Context, foundation.ID, int) (int, error)
	Sweep(context.Context, int) (exportapplication.SweepResult, error)
	SweepOrphansAll(context.Context, foundation.ID, int, int, time.Duration) (exportapplication.OrphanSweepResult, error)
}

type memoryExpiryService interface {
	ExpireDue(context.Context, int) (int, error)
}

// interviewCompletionMaintenanceService 提供有界 Completion reservation 过期维护。
type interviewCompletionMaintenanceService interface {
	// AbandonStaleCompletions 放弃超时 reservation，并将其 ACTIVE hold 转为 ORPHANED。
	AbandonStaleCompletions(context.Context, int) (interviewapplication.CompletionMaintenanceResult, error)
}

// learningPathMaintenanceService 提供有界 Review Path reservation 过期维护。
type learningPathMaintenanceService interface {
	// MaintainExpiredReservations 按 Application-owned 策略放弃超时 reservation。
	MaintainExpiredReservations(context.Context) (int, error)
}

// draftStreamCleanupService 提供有界的短期 Answer 草稿清理。
type draftStreamCleanupService interface {
	CleanupExpiredDraftStreams(context.Context, int) (int64, error)
}

type captureOutboxDispatchService interface {
	DispatchBatch(context.Context, int) (captureapplication.DispatchBatchResult, error)
}

type organizingOutboxDispatchService interface {
	DispatchBatch(context.Context, int) (organizingworkflow.DispatchBatchResult, error)
}

type gitSyncOutboxWorker interface {
	RunOnce(context.Context) (bool, error)
}

type gitSyncAutoScheduler interface {
	ScheduleBatch(context.Context, int) (gitsyncapplication.AutoSyncBatchResult, error)
}

type workerComponents struct {
	safeWriteback      *changecontrolworkflow.Node
	tools              toolRuntimeComponents
	agentCapability    agentCapabilityStatus
	artifact           artifactWorkflowComponents
	captureExecutor    *captureworkflow.Executor
	captureOutbox      captureOutboxDispatchService
	captureProfile     agentCapabilityStatus
	organizingExecutor *organizingworkflow.Executor
	organizingOutbox   organizingOutboxDispatchService
	gitSyncWorker      *gitsyncapplication.Worker
	gitSyncScheduler   *gitsyncapplication.AutoSyncScheduler
	gitSyncCapability  agentCapabilityStatus
	reindexWorker      *reindexriver.Worker
	dispatcher         *retrievalruntime.Runner
	runtimeClient      *riveradapter.Client
	definitions        *workflowapplication.DefinitionRegistry
	executors          *workflowapplication.ExecutorRegistry
	runtimeGeneration  workerRuntimeGenerationBuilder
	sourceProcessing   sourceProcessingComponents
	reindexRuntime     *workerReindexProcessorAcquirer
	semanticScan       *graphworkflow.SemanticLinkScanExecutor
	healthScan         *healthworkflowadapter.HealthScanExecutor
	healthScanStart    *healthapplication.ScanService
	healthSchedule     *healthapplication.ScheduleService
	healthAffected     *healthapplication.AffectedChangeDispatcher
	timelineProject    *knowledgeapplication.TimelineProjectionDispatcher
	citationBackfill   *artifactapplication.CitationBackfillDispatcher
	exportWorker       *exportriver.Worker
	exportService      *exportapplication.Service
	memoryExpiry       memoryExpiryService
	// interviewCompletion 是 reservation/hidden hold 维护依赖。
	interviewCompletion interviewCompletionMaintenanceService
	// learningPathMaintenance 是 Review Path reservation/hidden hold 维护依赖。
	learningPathMaintenance learningPathMaintenanceService
	// draftStreams 是跨 API/Worker 共享的短期 Answer 草稿投影清理依赖。
	draftStreams    draftStreamCleanupService
	fatalInvariants <-chan error
}

type toolRuntimeComponents struct {
	contracts      *toolsapplication.Registry
	executions     *toolsapplication.Registry
	execution      *toolsapplication.ExecutionService
	repository     *toolpostgres.Repository
	writebackAudit *toolchangecontrol.WritebackAuditRecorder
	// workflow/definition 只服务迁移前 agent-rag@1 的持久回放。
	// 新模型 Tool Calling 由 Eino AgentRuntime 通过 RAGAgentToolBridge 执行，
	// 不会创建本包的 Workflow Node。
	workflow       *toolworkflow.Executor
	definition     *workflowdomain.RegisteredDefinition
	enabledRefs    []toolsdomain.ToolRef
	runtimeEnabled bool
}

type agentCapabilityStatus struct {
	available bool
	code      string
}

type workerHealthServer struct {
	server  *http.Server
	errors  <-chan error
	address string
}

func main() {
	configPath := flag.String("config", "", "optional YAML configuration path")
	flag.Parse()
	logger := observability.NewLogger("info", os.Stderr)
	if err := run(*configPath, logger); err != nil {
		logger.Error("worker stopped with failure", "error_code", "WORKER_PROCESS_FAILED")
		os.Exit(1)
	}
}

func run(configPath string, logger *slog.Logger) error {
	if logger == nil {
		return errors.New("worker logger is nil")
	}
	cfg, err := config.LoadWorker(configPath)
	if err != nil {
		logger.Error("configuration is invalid", "error_code", "INVALID_CONFIGURATION")
		return err
	}
	telemetry, err := initializeWorkerTelemetry(context.Background(), cfg)
	if err != nil {
		logger.Error("telemetry initialization failed", "error_code", "TELEMETRY_EXPORTER_UNAVAILABLE")
		return err
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = telemetry.Shutdown(shutdownContext)
	}()
	telemetryStatus := telemetry.Status()
	if telemetryStatus.Degraded {
		logger.Warn("telemetry exporter is unavailable", "error_code", telemetryStatus.Code)
	}
	if err := observability.RecordProcessPresence(context.Background(), telemetry.Metrics()); err != nil {
		logger.Warn("worker process presence metric failed", "error_code", "PROCESS_PRESENCE_METRIC_FAILED")
	}
	if err := observability.RecordTelemetryRequired(
		context.Background(), telemetry.Metrics(), telemetry.Status().Mode == observability.TelemetryModeRequired,
	); err != nil {
		logger.Warn("worker telemetry mode metric failed", "error_code", "TELEMETRY_MODE_METRIC_FAILED")
	}
	modelTelemetry := platformmodels.NewModelTelemetry(telemetry.Tracer(), telemetry.Metrics())
	databaseURL, err := cfg.DatabaseConnectionString()
	if err != nil {
		logger.Error("database is not configured", "error_code", "DEPENDENCY_UNAVAILABLE")
		return err
	}

	database, err := postgres.Open(context.Background(), databaseURL, cfg.DatabaseMaxConns, cfg.DatabaseMinConns)
	if err != nil {
		logger.Error("database pool could not be opened", "error_code", "DEPENDENCY_UNAVAILABLE")
		return err
	}
	defer database.Close()

	if err := ping(database, cfg.DatabasePingTimeout); err != nil {
		logger.Error("worker startup database check failed", "error_code", "DEPENDENCY_UNAVAILABLE")
		return err
	}
	migrator, err := riveradapter.NewMigrator(database.DB())
	if err != nil {
		return err
	}
	validationContext, cancelValidation := context.WithTimeout(context.Background(), cfg.DatabasePingTimeout)
	validationErr := migrator.Validate(validationContext)
	cancelValidation()
	if validationErr != nil {
		logger.Error("worker River schema validation failed", "error_code", "WORKFLOW_RIVER_MIGRATION_INVALID")
		return validationErr
	}
	workspaceRuntime, err := workspaceruntimegrant.NewProcessComposition(
		context.Background(), database.DB(), os.LookupEnv, workspacedomain.RuntimeRoleWorker,
	)
	if err != nil {
		logger.Error("workspace root grant is unavailable", "error_code", "WORKSPACE_ROOT_GRANT_UNAVAILABLE")
		return err
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = workspaceRuntime.Close(shutdownContext)
	}()
	var managedModels modelsettingsruntime.BootstrapResult
	var modelEnqueueFences []riveradapter.EnqueueFence
	var configuredModels *modelsettingsruntime.Models
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		bootstrap, bootstrapErr := modelsettingsruntime.Bootstrap(context.Background(), database.DB(), cfg, modelTelemetry)
		managedModels = bootstrap
		if bootstrap.Repository != nil {
			modelEnqueueFences = append(modelEnqueueFences, bootstrap.Repository)
		}
		configuredModels = bootstrap.Loaded.Models
		if bootstrap.KeyError != nil {
			logger.Warn("model settings key is unavailable", "error_code", modelsettingsdomain.ErrorCodeSecretUnavailable)
		}
		if bootstrapErr != nil {
			logger.Warn("managed model revision is unavailable", "error_code", modelsettingsdomain.ErrorCodeUnavailable)
			if cfg.ModelSettingsRolloutID != "" || bootstrap.Service == nil || bootstrap.Loaded.Models == nil {
				return bootstrapErr
			}
		}
	} else {
		loaded, modelsErr := modelsettingsruntime.LoadSettings(context.Background(), cfg, nil, modelTelemetry)
		if modelsErr != nil {
			return modelsErr
		}
		configuredModels = loaded.Models
	}
	if configuredModels == nil {
		disabledModels, disabledErr := modelsettingsruntime.Build(cfg, modelsettingsdomain.ResolvedSettings{Settings: modelsettingsdomain.CanonicalDisabledSettings()}, modelTelemetry)
		if disabledErr != nil {
			return disabledErr
		}
		configuredModels = disabledModels
	}
	modelBinding, err := newWorkerModelRuntimeBinding(cfg.ModelSettingsMode, configuredModels, foundation.NewUUIDGenerator(nil))
	if err != nil {
		return err
	}
	var runtimeExecutorAcquirer *workerRuntimeExecutorAcquirer
	var sourceRefreshAcquirer *workerSourceRefreshAcquirer
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		runtimeExecutorAcquirer = &workerRuntimeExecutorAcquirer{}
		sourceRefreshAcquirer = &workerSourceRefreshAcquirer{}
		modelBinding.executorAcquirer = runtimeExecutorAcquirer
		modelBinding.sourceRefreshAcquirer = sourceRefreshAcquirer
	}
	cfg = modelsettingsruntime.WithoutModelCredentials(cfg)
	components, err := newWorkerComponentsWithModels(database.DB(), cfg, configuredModels, modelBinding, workspaceRuntime.Repository, logger, telemetry.Metrics(), telemetry.Tracer(), modelEnqueueFences...)
	if err != nil {
		logger.Error("worker components are unavailable", "error_code", "WORKER_COMPONENTS_UNAVAILABLE")
		return err
	}
	var modelController *modelsettingsruntime.HotRuntimeController[*workerRuntimeGeneration]
	var modelHost *modelsettingsruntime.RuntimeHost[*workerRuntimeGeneration]
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		if managedModels.Service == nil || modelBinding.revision == nil || modelBinding.instanceID == nil ||
			runtimeExecutorAcquirer == nil || sourceRefreshAcquirer == nil || components.runtimeGeneration == nil || components.reindexRuntime == nil || managedModels.Loaded.RolloutID != nil {
			return errors.New("managed worker hot runtime composition is unavailable")
		}
		generationLifecycle, lifecycleErr := modelsettingsruntime.NewGenerationLifecycle(
			managedModels.LocalModelStore, modelsettingsdomain.RuntimeRoleWorker, *modelBinding.instanceID,
		)
		if lifecycleErr != nil {
			return lifecycleErr
		}
		modelHost, err = modelsettingsruntime.NewRuntimeHost(modelsettingsruntime.RuntimeHostOptions[*workerRuntimeGeneration]{
			Initial: modelsettingsruntime.InitialRuntime[*workerRuntimeGeneration]{
				Binding: modelsettingsruntime.RuntimeBinding{
					Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleWorker,
					Revision: *modelBinding.revision, InstanceID: *modelBinding.instanceID,
				},
				Value:       &workerRuntimeGeneration{models: configuredModels, executors: components.executors, sources: components.sourceProcessing},
				Unavailable: managedModels.Loaded.InitialPhase == modelsettingsdomain.RuntimePhaseUnavailable,
			},
			Factory: &workerRuntimeGenerationFactory{
				base: cfg, revisions: managedModels.Service, buildGeneration: components.runtimeGeneration, telemetry: modelTelemetry,
			},
			EmbeddingCompatible: func(generation *workerRuntimeGeneration, version retrievaldomain.EmbeddingVersion) error {
				if generation == nil || generation.models == nil {
					return errors.New("worker runtime models are unavailable")
				}
				return generation.models.ValidateEmbeddingVersion(version)
			},
			LocalDemand: func(generation *workerRuntimeGeneration) (localmodelruntime.Requirement, error) {
				if generation == nil || generation.models == nil {
					return localmodelruntime.Requirement{}, errors.New("worker runtime models are unavailable")
				}
				return generation.models.LocalDemand(), nil
			},
			Lifecycle: generationLifecycle,
		})
		if err != nil {
			return err
		}
		defer modelHost.Close()
		if err := runtimeExecutorAcquirer.bind(modelHost); err != nil {
			return err
		}
		if err := components.reindexRuntime.bind(modelHost); err != nil {
			return err
		}
		if err := sourceRefreshAcquirer.bind(modelHost); err != nil {
			return err
		}
		modelController, err = modelsettingsruntime.NewHotRuntimeController(modelsettingsruntime.HotRuntimeControllerOptions[*workerRuntimeGeneration]{
			Host: modelHost, Activation: managedModels.Service, Revisions: managedModels.Service,
			Runtime: managedModels.Service, InitialPhase: managedModels.Loaded.InitialPhase,
			StaleAfter: modelBinding.runtimeFreshWithin,
		})
		if err != nil {
			return err
		}
	}

	readiness := workflowruntime.NewReadiness()
	readiness.SetDatabaseOK(true)
	readiness.SetRiverSchemaOK(true)
	readiness.SetDefinitionsOK(components.definitions != nil)
	readiness.SetExecutorsOK(components.executors != nil)
	readiness.SetDependenciesOK(components.safeWriteback != nil && components.reindexWorker != nil && components.dispatcher != nil && components.timelineProject != nil && components.citationBackfill != nil && components.exportWorker != nil && components.exportService != nil && components.memoryExpiry != nil && components.interviewCompletion != nil && components.learningPathMaintenance != nil && components.draftStreams != nil && captureWorkflowReadiness(components) && organizingWorkflowReadiness(components) && gitSyncWorkerReadiness(components) && agentWorkflowReadiness(components) && artifactWorkflowReadiness(components))
	toolEnabled := cfg.ToolRuntimeMode == config.ToolModeEnabled
	toolContractsOK, toolExecutorsOK, toolDependenciesOK := toolWorkflowReadiness(components)
	readiness.SetToolRuntimeState(toolEnabled, toolContractsOK, toolExecutorsOK, toolDependenciesOK)
	readiness.SetWebFetchState(cfg.WebFetchMode == config.ToolModeEnabled, false)
	health, err := startWorkerHealthServer(cfg.WorkerHealthAddr, workflowhealth.NewHandler(readiness))
	if err != nil {
		logger.Error("worker health server could not be started", "error_code", "WORKER_HEALTH_START_FAILED")
		return err
	}

	lifecycle, err := newLifecycleController(components.runtimeClient, components.dispatcher)
	if err != nil {
		_ = health.server.Close()
		return err
	}
	processContext, cancelProcess := context.WithCancel(context.Background())
	defer cancelProcess()
	modelDrain, err := newWorkerModelDrain(
		components.dispatcher,
		components.runtimeClient,
		processContext,
		func(ctx context.Context) (int64, error) {
			return riveradapter.RunningJobCount(ctx, database.DB(), cfg.WorkerQueue)
		},
		readiness.SetReindexDispatcherStarted,
	)
	if err != nil {
		_ = health.server.Close()
		return err
	}
	if err := workspaceRuntime.SetQuiescenceHooks(workspaceruntimegrant.QuiescenceHooks{
		Begin: modelDrain.Begin, IsQuiesced: modelDrain.IsQuiesced, Resume: modelDrain.Resume,
	}); err != nil {
		_ = health.server.Close()
		return err
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	modelRuntimeErr := make(chan error, 1)
	workspaceRuntimeErr := workspaceRuntime.Errors()
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		go func() {
			if runErr := modelController.Run(processContext); runErr != nil {
				modelRuntimeErr <- runErr
			}
		}()
		select {
		case <-modelController.Active():
		case runErr := <-modelRuntimeErr:
			_ = health.server.Close()
			return runErr
		case runErr := <-workspaceRuntimeErr:
			_ = health.server.Close()
			return runErr
		case received := <-signals:
			logger.Info("worker stopping before model activation", "signal", received.String())
			_ = health.server.Shutdown(context.Background())
			return nil
		case healthErr := <-health.errors:
			if healthErr == nil {
				healthErr = errors.New("worker health server stopped unexpectedly")
			}
			return healthErr
		}
	}
	resumeQueue := true
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		queueResumeContext, cancelQueueResume := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
		modelSnapshot, snapshotErr := managedModels.Service.Snapshot(queueResumeContext)
		cancelQueueResume()
		if snapshotErr != nil {
			_ = health.server.Close()
			return snapshotErr
		}
		resumeQueue = modelSnapshot.Rollout.Phase == modelsettingsdomain.RolloutPhaseIdle ||
			modelSnapshot.Rollout.Phase == modelsettingsdomain.RolloutPhaseFailed
	}
	if err := startWorkerRuntime(
		processContext,
		resumeQueue,
		cfg.DatabasePingTimeout,
		cfg.WorkerHardStopTimeout,
		lifecycle,
		components.runtimeClient,
		readiness,
		health.server,
	); err != nil {
		logger.Error("workflow runtime could not be started", "error_code", "WORKFLOW_RIVER_CLIENT_START_FAILED")
		return err
	}
	readiness.SetReindexDispatcherStarted(components.dispatcher.Started())
	gitSyncProcessContext, cancelGitSyncProcess := context.WithCancel(processContext)
	gitSyncStopped := startGitSyncDispatchLoop(
		gitSyncProcessContext, logger, components.gitSyncScheduler, components.gitSyncWorker,
		gitSyncDispatchInterval, gitSyncDispatchTimeout, modelDrain.ProducersEnabled,
	)
	defer cancelGitSyncProcess()
	captureContext, cancelCapture := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
	_, _ = dispatchCaptureOutbox(captureContext, logger, components.captureOutbox, captureDispatchStartupPhase)
	cancelCapture()
	organizingContext, cancelOrganizing := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
	_, _ = dispatchOrganizingOutbox(organizingContext, logger, components.organizingOutbox, organizingDispatchStartupPhase)
	cancelOrganizing()
	timelineContext, cancelTimeline := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
	_, _ = dispatchTimelineProjection(timelineContext, logger, components.timelineProject, timelineProjectionStartupPhase)
	cancelTimeline()
	citationBackfillContext, cancelCitationBackfill := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
	_, _ = dispatchCitationBackfill(citationBackfillContext, logger, components.citationBackfill, citationBackfillStartupPhase)
	cancelCitationBackfill()
	exportOrphanCursor := foundation.ID("")
	maintenanceContext, cancelMaintenance := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
	exportOrphanCursor = runExportMaintenance(maintenanceContext, logger, components.exportService, exportOrphanCursor, exportMaintenanceStartupPhase)
	cancelMaintenance()
	memoryExpiryContext, cancelMemoryExpiry := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
	runMemoryExpiryMaintenance(memoryExpiryContext, logger, components.memoryExpiry, memoryExpiryStartupPhase)
	cancelMemoryExpiry()
	interviewCompletionContext, cancelInterviewCompletion := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
	runInterviewCompletionMaintenance(interviewCompletionContext, logger, components.interviewCompletion, interviewCompletionStartupPhase)
	cancelInterviewCompletion()
	learningPathContext, cancelLearningPath := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
	runLearningPathMaintenance(learningPathContext, logger, components.learningPathMaintenance, learningPathMaintenanceStartupPhase)
	cancelLearningPath()
	draftStreamCleanupContext, cancelDraftStreamCleanup := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
	runDraftStreamCleanup(draftStreamCleanupContext, logger, components.draftStreams, draftStreamCleanupStartupPhase)
	cancelDraftStreamCleanup()
	logger.Info("worker started", "version", cfg.Version, "safe_writeback_node", components.safeWriteback != nil,
		"semantic_link_scan", components.semanticScan != nil,
		"agent_available", components.agentCapability.available, "agent_capability_code", components.agentCapability.code,
		"artifact_generation_available", components.artifact.capability.available, "artifact_generation_capability_code", components.artifact.capability.code,
		"capture_workflow", components.captureExecutor != nil, "capture_outbox", components.captureOutbox != nil,
		"capture_profile_available", components.captureProfile.available, "capture_profile_capability_code", components.captureProfile.code,
		"organizing_workflow", components.organizingExecutor != nil, "organizing_outbox", components.organizingOutbox != nil,
		"git_sync_available", components.gitSyncCapability.available,
		"git_sync_capability_code", components.gitSyncCapability.code,
		"tool_runtime_enabled", components.tools.runtimeEnabled, "tool_executor_count", len(components.tools.enabledRefs),
		"web_fetch_enabled", cfg.WebFetchMode == config.ToolModeEnabled, "reindex_dispatcher", components.dispatcher.Started(),
		"timeline_projector", components.timelineProject != nil, "export_worker", components.exportWorker != nil,
		"artifact_citation_backfill", components.citationBackfill != nil,
		"interview_completion_maintenance", components.interviewCompletion != nil,
		"learning_path_maintenance", components.learningPathMaintenance != nil,
		"draft_stream_cleanup", components.draftStreams != nil)

	ticker := time.NewTicker(cfg.HealthInterval)
	defer ticker.Stop()
	captureTicker := time.NewTicker(captureDispatchInterval)
	defer captureTicker.Stop()
	shutdownMode := shutdownGraceful
	var runErr error
	for {
		select {
		case received := <-signals:
			logger.Info("worker stopping", "signal", received.String())
			goto shutdown
		case err := <-health.errors:
			if err == nil {
				err = errors.New("worker health server stopped unexpectedly")
			}
			runErr = err
			logger.Error("worker health server failed", "error_code", "WORKER_HEALTH_FAILED")
			goto shutdown
		case <-components.runtimeClient.Stopped():
			readiness.SetRiverStarted(false)
			runErr = errors.New("workflow River runtime stopped unexpectedly")
			goto shutdown
		case fatalErr := <-components.dispatcher.Errors():
			if fatalErr == nil {
				fatalErr = errors.New("reindex dispatcher stopped without a fatal error")
			}
			readiness.SetReindexDispatcherStarted(false)
			shutdownMode = shutdownEmergency
			runErr = fatalErr
			logger.Error("reindex dispatcher fatal invariant", "error_code", "REINDEX_DISPATCHER_FATAL_INVARIANT")
			goto shutdown
		case fatalErr := <-components.fatalInvariants:
			if fatalErr == nil {
				fatalErr = errors.New("worker runtime reported a fatal invariant violation")
			}
			shutdownMode = shutdownEmergency
			runErr = fatalErr
			logger.Error("worker runtime fatal invariant", "error_code", "WORKER_FATAL_INVARIANT")
			goto shutdown
		case runtimeErr := <-modelRuntimeErr:
			shutdownMode = shutdownEmergency
			runErr = runtimeErr
			logger.Error("model runtime ownership was lost", "error_code", modelsettingsdomain.ErrorCodeRuntimeConflict)
			goto shutdown
		case runtimeErr := <-workspaceRuntimeErr:
			shutdownMode = shutdownEmergency
			runErr = runtimeErr
			logger.Error("workspace root grant ownership was lost", "error_code", "WORKSPACE_GRANT_STALE")
			goto shutdown
		case <-captureTicker.C:
			if !modelDrain.ProducersEnabled() {
				continue
			}
			dispatchContext, cancelDispatch := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
			_, _ = dispatchCaptureOutbox(dispatchContext, logger, components.captureOutbox, captureDispatchPeriodicPhase)
			cancelDispatch()
			organizingContext, cancelOrganizing := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
			_, _ = dispatchOrganizingOutbox(organizingContext, logger, components.organizingOutbox, organizingDispatchPeriodicPhase)
			cancelOrganizing()
		case <-ticker.C:
			if err := ping(database, cfg.DatabasePingTimeout); err != nil {
				readiness.SetDatabaseOK(false)
				logger.Error("worker database health check failed", "error_code", "DEPENDENCY_UNAVAILABLE")
			} else {
				readiness.SetDatabaseOK(true)
				logger.Debug("worker database health check passed")
				metricContext, cancelMetric := context.WithTimeout(context.Background(), cfg.DatabasePingTimeout)
				metricErr := recordQueueDepthMetric(metricContext, telemetry.Metrics(), database.DB(), cfg.WorkerQueue)
				cancelMetric()
				if metricErr != nil {
					logger.Warn("worker queue depth metric failed", "error_code", "WORKER_QUEUE_METRIC_FAILED")
				}
				if !modelDrain.ProducersEnabled() {
					continue
				}
				if components.healthSchedule != nil {
					dispatchContext, cancelDispatch := context.WithTimeout(context.Background(), cfg.DatabasePingTimeout)
					_, dispatchErr := components.healthSchedule.DispatchDue(dispatchContext, 10)
					cancelDispatch()
					if dispatchErr != nil {
						logger.Warn("health schedule dispatch failed", "error_code", "HEALTH_SCHEDULE_DISPATCH_FAILED")
					}
				}
				if components.healthAffected != nil {
					dispatchContext, cancelDispatch := context.WithTimeout(context.Background(), cfg.DatabasePingTimeout)
					_, dispatchErr := components.healthAffected.DispatchBatch(dispatchContext, 10)
					cancelDispatch()
					if dispatchErr != nil {
						logger.Warn("health affected-change dispatch failed", "error_code", "HEALTH_AFFECTED_CHANGE_DISPATCH_FAILED")
					}
				}
				if components.timelineProject != nil {
					dispatchContext, cancelDispatch := context.WithTimeout(context.Background(), cfg.DatabasePingTimeout)
					_, _ = dispatchTimelineProjection(dispatchContext, logger, components.timelineProject, timelineProjectionPeriodicPhase)
					cancelDispatch()
				}
				if components.citationBackfill != nil {
					backfillContext, cancelBackfill := context.WithTimeout(context.Background(), cfg.DatabasePingTimeout)
					_, _ = dispatchCitationBackfill(backfillContext, logger, components.citationBackfill, citationBackfillPeriodicPhase)
					cancelBackfill()
				}
				if components.exportService != nil {
					maintenanceContext, cancelMaintenance := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
					exportOrphanCursor = runExportMaintenance(maintenanceContext, logger, components.exportService, exportOrphanCursor, exportMaintenancePeriodicPhase)
					cancelMaintenance()
				}
				if components.memoryExpiry != nil {
					memoryExpiryContext, cancelMemoryExpiry := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
					runMemoryExpiryMaintenance(memoryExpiryContext, logger, components.memoryExpiry, memoryExpiryPeriodicPhase)
					cancelMemoryExpiry()
				}
				if components.interviewCompletion != nil {
					interviewCompletionContext, cancelInterviewCompletion := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
					runInterviewCompletionMaintenance(interviewCompletionContext, logger, components.interviewCompletion, interviewCompletionPeriodicPhase)
					cancelInterviewCompletion()
				}
				if components.learningPathMaintenance != nil {
					learningPathContext, cancelLearningPath := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
					runLearningPathMaintenance(learningPathContext, logger, components.learningPathMaintenance, learningPathMaintenancePeriodicPhase)
					cancelLearningPath()
				}
				draftStreamCleanupContext, cancelDraftStreamCleanup := context.WithTimeout(processContext, cfg.DatabasePingTimeout)
				runDraftStreamCleanup(draftStreamCleanupContext, logger, components.draftStreams, draftStreamCleanupPeriodicPhase)
				cancelDraftStreamCleanup()
			}
		}
	}

shutdown:
	readiness.BeginShutdown()
	readiness.SetReindexDispatcherStarted(false)
	shutdownStartedAt := time.Now()
	runtimeShutdownDeadline, hardShutdownDeadline := workerShutdownDeadlines(
		shutdownStartedAt, cfg.WorkerSoftStopTimeout, cfg.WorkerHardStopTimeout, cfg.ShutdownTimeout,
	)
	shutdownContext, cancelShutdown := context.WithDeadline(context.Background(), runtimeShutdownDeadline)
	defer cancelShutdown()
	cancelGitSyncProcess()
	select {
	case <-gitSyncStopped:
	case <-shutdownContext.Done():
	}
	if err := lifecycle.Shutdown(shutdownContext, shutdownMode); err != nil && runErr == nil {
		runErr = err
	}
	readiness.SetRiverStarted(false)
	if err := health.server.Shutdown(shutdownContext); err != nil && runErr == nil {
		runErr = err
	}
	runtimeShutdownExpired := errors.Is(shutdownContext.Err(), context.DeadlineExceeded)
	result := "success"
	if runErr != nil || runtimeShutdownExpired {
		result = "failure"
	}
	if err := recordShutdownMetric(telemetry.Metrics(), lifecycle.Mode(), result); err != nil {
		logger.Warn("worker shutdown metric failed", "error_code", "WORKER_METRIC_RECORD_FAILED")
	}
	telemetryShutdownContext, cancelTelemetryShutdown := context.WithDeadline(context.Background(), hardShutdownDeadline)
	telemetryShutdownErr := telemetry.Shutdown(telemetryShutdownContext)
	hardShutdownExpired := errors.Is(telemetryShutdownContext.Err(), context.DeadlineExceeded)
	cancelTelemetryShutdown()
	if telemetryShutdownErr != nil && runErr == nil {
		runErr = telemetryShutdownErr
	}
	if runtimeShutdownExpired || hardShutdownExpired {
		logger.Error("worker hard shutdown deadline exceeded", "error_code", "WORKER_HARD_SHUTDOWN_TIMEOUT")
		return context.DeadlineExceeded
	}
	return runErr
}

func workerShutdownDeadlines(startedAt time.Time, softStopTimeout, hardStopTimeout, telemetryTimeout time.Duration) (time.Time, time.Time) {
	hardDeadline := startedAt.Add(hardStopTimeout)
	flushBudget := min(telemetryTimeout, hardStopTimeout-softStopTimeout)
	return hardDeadline.Add(-flushBudget), hardDeadline
}

// initializeWorkerTelemetry 以独立 service.name 组合 Worker 的 OTLP Provider。
func initializeWorkerTelemetry(ctx context.Context, cfg config.Config) (*observability.Telemetry, error) {
	return observability.InitializeTelemetry(ctx, observability.TelemetryOptions{
		Mode:           observability.TelemetryMode(cfg.TelemetryMode),
		Endpoint:       cfg.TelemetryEndpoint,
		ServiceName:    cfg.AppName + "-worker",
		ServiceVersion: cfg.Version,
		Environment:    cfg.Environment,
	})
}

func dispatchCaptureOutbox(ctx context.Context, logger *slog.Logger, dispatcher captureOutboxDispatchService, phase string) (captureapplication.DispatchBatchResult, error) {
	if dispatcher == nil {
		return captureapplication.DispatchBatchResult{}, errors.New("capture outbox dispatcher is unavailable")
	}
	batch, err := dispatcher.DispatchBatch(ctx, captureDispatchBatchSize)
	if err != nil {
		errorCode, retryable := captureDispatchFailure(err)
		logger.Warn("capture outbox dispatch failed",
			"error_code", errorCode, "retryable", retryable, "phase", phase,
			"claimed_count", batch.Claimed, "started_count", batch.Started,
			"retried_count", batch.Retried, "poisoned_count", batch.Poisoned)
		return batch, err
	}
	if phase == captureDispatchStartupPhase || batch.Claimed > 0 {
		logger.Info("capture outbox dispatch completed",
			"phase", phase, "claimed_count", batch.Claimed, "started_count", batch.Started,
			"retried_count", batch.Retried, "poisoned_count", batch.Poisoned)
	}
	return batch, nil
}

func captureDispatchFailure(err error) (string, bool) {
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Code != "" {
		return classified.Code, classified.Retryable
	}
	return "CAPTURE_OUTBOX_DISPATCH_FAILED", false
}

func dispatchOrganizingOutbox(ctx context.Context, logger *slog.Logger, dispatcher organizingOutboxDispatchService, phase string) (organizingworkflow.DispatchBatchResult, error) {
	if dispatcher == nil {
		return organizingworkflow.DispatchBatchResult{}, errors.New("organizing outbox dispatcher is unavailable")
	}
	batch, err := dispatcher.DispatchBatch(ctx, organizingDispatchBatchSize)
	if err != nil {
		errorCode, retryable := organizingDispatchFailure(err)
		logger.Warn("organizing outbox dispatch failed",
			"error_code", errorCode, "retryable", retryable, "phase", phase,
			"claimed_count", batch.Claimed, "started_count", batch.Started,
			"replayed_count", batch.Replayed, "retried_count", batch.Retried, "poisoned_count", batch.Poisoned)
		return batch, err
	}
	if phase == organizingDispatchStartupPhase || batch.Claimed > 0 {
		logger.Info("organizing outbox dispatch completed",
			"phase", phase, "claimed_count", batch.Claimed, "started_count", batch.Started,
			"replayed_count", batch.Replayed, "retried_count", batch.Retried, "poisoned_count", batch.Poisoned)
	}
	return batch, nil
}

func organizingDispatchFailure(err error) (string, bool) {
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Code != "" {
		return classified.Code, classified.Retryable
	}
	return "ORGANIZING_OUTBOX_DISPATCH_FAILED", false
}

func startGitSyncDispatchLoop(
	ctx context.Context,
	logger *slog.Logger,
	scheduler gitSyncAutoScheduler,
	worker gitSyncOutboxWorker,
	interval time.Duration,
	timeout time.Duration,
	enabled func() bool,
) <-chan struct{} {
	stopped := make(chan struct{})
	if ctx == nil || logger == nil || scheduler == nil || worker == nil || interval <= 0 || timeout <= 0 {
		close(stopped)
		return stopped
	}
	if enabled == nil {
		enabled = func() bool { return true }
	}
	go func() {
		defer close(stopped)
		dispatch := func(phase string) {
			if !enabled() {
				return
			}
			dispatchContext, cancelDispatch := context.WithTimeout(ctx, timeout)
			defer cancelDispatch()
			_, _, _ = dispatchGitSync(dispatchContext, logger, scheduler, worker, phase)
		}
		if ctx.Err() != nil {
			return
		}
		dispatch(gitSyncDispatchStartupPhase)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				dispatch(gitSyncDispatchPeriodicPhase)
			}
		}
	}()
	return stopped
}

// dispatchGitSync first turns eligible approved writebacks into standard Runs,
// then drains already durable outbox work even when scheduling reports a fault.
func dispatchGitSync(ctx context.Context, logger *slog.Logger, scheduler gitSyncAutoScheduler, worker gitSyncOutboxWorker, phase string) (gitsyncapplication.AutoSyncBatchResult, int, error) {
	if scheduler == nil && worker == nil {
		return gitsyncapplication.AutoSyncBatchResult{}, 0, nil
	}
	if scheduler == nil || worker == nil {
		return gitsyncapplication.AutoSyncBatchResult{}, 0, errors.New("Git sync scheduler and worker must be configured together")
	}
	scheduleContext, cancelSchedule := context.WithTimeout(ctx, gitSyncScheduleTimeout)
	batch, scheduleErr := scheduler.ScheduleBatch(scheduleContext, gitSyncScheduleBatchSize)
	cancelSchedule()
	if scheduleErr != nil {
		errorCode, retryable := gitSyncDispatchFailure(scheduleErr)
		logger.Warn("Git automatic sync scheduling failed",
			"error_code", errorCode, "retryable", retryable, "phase", phase,
			"scanned_count", batch.Scanned, "scheduled_count", batch.Scheduled,
			"replayed_count", batch.Replayed, "deferred_count", batch.Deferred)
	} else if phase == gitSyncDispatchStartupPhase || batch.Scanned > 0 {
		logger.Info("Git automatic sync scheduling completed",
			"phase", phase, "scanned_count", batch.Scanned, "scheduled_count", batch.Scheduled,
			"replayed_count", batch.Replayed, "deferred_count", batch.Deferred)
	}
	processed, dispatchErr := dispatchGitSyncOutbox(ctx, logger, worker, phase)
	return batch, processed, errors.Join(scheduleErr, dispatchErr)
}

// dispatchGitSyncOutbox drains a bounded number of durable Git sync events.
// The worker itself persists retry and terminal state, so a transient dispatch
// error is observed and retried on the next periodic tick.
func dispatchGitSyncOutbox(ctx context.Context, logger *slog.Logger, worker gitSyncOutboxWorker, phase string) (int, error) {
	if worker == nil {
		return 0, nil
	}
	processed := 0
	for processed < gitSyncDispatchBatchSize {
		worked, err := worker.RunOnce(ctx)
		if err != nil {
			errorCode, retryable := gitSyncDispatchFailure(err)
			logger.Warn("Git sync outbox dispatch failed",
				"error_code", errorCode, "retryable", retryable, "phase", phase, "processed_count", processed)
			return processed, err
		}
		if !worked {
			break
		}
		processed++
	}
	if phase == gitSyncDispatchStartupPhase || processed > 0 {
		logger.Info("Git sync outbox dispatch completed", "phase", phase, "processed_count", processed)
	}
	return processed, nil
}

func gitSyncDispatchFailure(err error) (string, bool) {
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Code != "" {
		return classified.Code, classified.Retryable
	}
	return "GIT_SYNC_OUTBOX_DISPATCH_FAILED", false
}

func dispatchTimelineProjection(ctx context.Context, logger *slog.Logger, dispatcher *knowledgeapplication.TimelineProjectionDispatcher, phase string) (knowledgeapplication.TimelineProjectionBatchResult, error) {
	batch, err := dispatcher.DispatchBatch(ctx, knowledgeapplication.MaxTimelineProjectionBatch)
	if err != nil {
		errorCode, retryable := timelineProjectionFailure(err)
		logger.Warn("timeline projection dispatch failed",
			"error_code", errorCode, "retryable", retryable, "phase", phase,
			"processed_count", batch.Processed, "projected_count", batch.Projected,
			"replayed_count", batch.Replayed, "poisoned_count", batch.Poisoned)
		return batch, err
	}
	if phase == timelineProjectionStartupPhase || batch.Processed > 0 {
		logger.Info("timeline projection dispatch completed",
			"phase", phase, "processed_count", batch.Processed, "projected_count", batch.Projected,
			"replayed_count", batch.Replayed, "poisoned_count", batch.Poisoned)
	}
	return batch, nil
}

func timelineProjectionFailure(err error) (string, bool) {
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Code != "" {
		return classified.Code, classified.Retryable
	}
	return timelineProjectionDispatchErrorCode, false
}

func citationBackfillFailure(err error) (string, bool) {
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Code != "" {
		return classified.Code, classified.Retryable
	}
	return artifactapplication.ErrorCodeCitationBackfillFailed, false
}

func dispatchCitationBackfill(ctx context.Context, logger *slog.Logger, dispatcher *artifactapplication.CitationBackfillDispatcher, phase string) (artifactapplication.CitationBackfillBatchResult, error) {
	batch, err := dispatcher.DispatchBatch(ctx, artifactapplication.MaxCitationBackfillWorkspaceBatch, artifactapplication.MaxCitationBackfillRevisionBatch)
	if err != nil {
		errorCode, retryable := citationBackfillFailure(err)
		logger.Warn("artifact citation selector backfill failed",
			"error_code", errorCode, "retryable", retryable, "phase", phase,
			"workspace_count", batch.Workspaces, "processed_revision_count", batch.ProcessedRevisions,
			"processed_selector_count", batch.ProcessedSelectors, "validated_revision_count", batch.ValidatedRevisions,
			"completed_workspace_count", batch.Completed)
		return batch, err
	}
	if phase == citationBackfillStartupPhase || batch.Workspaces > 0 {
		logger.Info("artifact citation selector backfill completed",
			"phase", phase, "workspace_count", batch.Workspaces,
			"processed_revision_count", batch.ProcessedRevisions, "processed_selector_count", batch.ProcessedSelectors,
			"validated_revision_count", batch.ValidatedRevisions, "completed_workspace_count", batch.Completed)
	}
	return batch, nil
}

func runExportMaintenance(ctx context.Context, logger *slog.Logger, service exportMaintenanceService, orphanCursor foundation.ID, phase string) foundation.ID {
	if service == nil {
		return orphanCursor
	}
	recovered, recoverErr := service.Recover(ctx, "", exportapplication.MaxListLimit)
	if recoverErr != nil {
		logger.Warn("export recovery dispatch failed", "error_code", "EXPORT_RECOVERY_DISPATCH_FAILED", "phase", phase, "recovered_count", recovered)
	} else if phase == exportMaintenanceStartupPhase || recovered > 0 {
		logger.Info("export recovery dispatched", "phase", phase, "recovered_count", recovered)
	}
	swept, sweepErr := service.Sweep(ctx, exportapplication.MaxListLimit)
	if sweepErr != nil {
		logger.Warn("export expiry cleanup failed", "error_code", "EXPORT_CLEANUP_FAILED", "phase", phase,
			"expired_count", swept.Expired, "cleaned_count", swept.Cleaned, "failed_count", swept.Failed)
	} else if phase == exportMaintenanceStartupPhase || swept.Expired > 0 || swept.Cleaned > 0 {
		logger.Info("export expiry cleanup completed", "phase", phase,
			"expired_count", swept.Expired, "cleaned_count", swept.Cleaned, "failed_count", swept.Failed)
	}
	orphans, orphanErr := service.SweepOrphansAll(
		ctx,
		orphanCursor,
		exportOrphanWorkspaceBatch,
		exportapplication.MaxListLimit,
		exportapplication.DefaultOrphanGrace,
	)
	if orphanErr != nil {
		logger.Warn("export orphan cleanup failed", "error_code", "EXPORT_ORPHAN_CLEANUP_FAILED", "phase", phase,
			"workspace_count", orphans.ScannedWorkspaces, "deleted_count", orphans.Deleted)
		return orphanCursor
	}
	if phase == exportMaintenanceStartupPhase || orphans.Deleted > 0 {
		logger.Info("export orphan cleanup completed", "phase", phase,
			"workspace_count", orphans.ScannedWorkspaces, "deleted_count", orphans.Deleted)
	}
	return orphans.NextWorkspaceID
}

func runMemoryExpiryMaintenance(ctx context.Context, logger *slog.Logger, service memoryExpiryService, phase string) {
	if service == nil {
		return
	}
	expired, err := service.ExpireDue(ctx, memorydomain.MaxListLimit)
	if err != nil {
		logger.Warn("memory expiry maintenance failed", "error_code", "MEMORY_EXPIRY_MAINTENANCE_FAILED", "phase", phase, "expired_count", expired)
		return
	}
	if phase == memoryExpiryStartupPhase || expired > 0 {
		logger.Info("memory expiry maintenance completed", "phase", phase, "expired_count", expired)
	}
}

// runInterviewCompletionMaintenance 执行一次有界 reservation/hold 维护并记录稳定计数。
func runInterviewCompletionMaintenance(ctx context.Context, logger *slog.Logger, service interviewCompletionMaintenanceService, phase string) {
	if service == nil {
		return
	}
	result, err := service.AbandonStaleCompletions(ctx, interviewapplication.MaxCompletionMaintenanceBatch)
	if err != nil {
		logger.Warn("interview completion maintenance failed", "error_code", "INTERVIEW_COMPLETION_MAINTENANCE_FAILED", "phase", phase,
			"abandoned_reservation_count", result.AbandonedReservations, "orphaned_hold_count", result.OrphanedHolds)
		return
	}
	if phase == interviewCompletionStartupPhase || result.AbandonedReservations > 0 || result.OrphanedHolds > 0 {
		logger.Info("interview completion maintenance completed", "phase", phase,
			"abandoned_reservation_count", result.AbandonedReservations, "orphaned_hold_count", result.OrphanedHolds)
	}
}

// runLearningPathMaintenance 执行一次有界 Review Path reservation/hold 维护。
func runLearningPathMaintenance(ctx context.Context, logger *slog.Logger, service learningPathMaintenanceService, phase string) {
	if service == nil {
		return
	}
	abandoned, err := service.MaintainExpiredReservations(ctx)
	if err != nil {
		logger.Warn("learning path maintenance failed", "error_code", "LEARNING_PATH_MAINTENANCE_FAILED", "phase", phase,
			"abandoned_reservation_count", abandoned)
		return
	}
	if phase == learningPathMaintenanceStartupPhase || abandoned > 0 {
		logger.Info("learning path maintenance completed", "phase", phase, "abandoned_reservation_count", abandoned)
	}
}

// runDraftStreamCleanup 执行一次有界 TTL 清理。清理失败只记录日志，下一次
// 周期继续重试，不能影响 River 消费和 Worker 主循环。
func runDraftStreamCleanup(ctx context.Context, logger *slog.Logger, service draftStreamCleanupService, phase string) {
	if service == nil {
		return
	}
	deleted, err := service.CleanupExpiredDraftStreams(ctx, draftStreamCleanupBatchSize)
	if err != nil {
		logger.Warn("answer draft stream cleanup failed", "error_code", "ANSWER_DRAFT_STREAM_CLEANUP_FAILED", "phase", phase,
			"batch_size", draftStreamCleanupBatchSize)
		return
	}
	if phase == draftStreamCleanupStartupPhase || deleted > 0 {
		logger.Info("answer draft stream cleanup completed", "phase", phase,
			"deleted_count", deleted, "batch_size", draftStreamCleanupBatchSize)
	}
}

func newWorkerComponents(db *pgxpool.Pool, cfg config.Config, logger *slog.Logger, metrics observability.Metrics, enqueueFences ...riveradapter.EnqueueFence) (workerComponents, error) {
	models, err := modelRuntimeForComposition(cfg)
	if err != nil {
		return workerComponents{}, err
	}
	return newWorkerComponentsWithModels(db, cfg, models, workerModelRuntimeBinding{}, nil, logger, metrics, observability.NewNoopTracer(), enqueueFences...)
}

type workerModelRuntimeBinding struct {
	revision              *int64
	instanceID            *foundation.ID
	runtimeFreshWithin    time.Duration
	executorAcquirer      riveradapter.RuntimeExecutorAcquirer
	sourceRefreshAcquirer *workerSourceRefreshAcquirer
}

func newWorkerModelRuntimeBinding(mode config.ModelSettingsMode, models *modelsettingsruntime.Models, ids foundation.IDGenerator) (workerModelRuntimeBinding, error) {
	if mode == config.ModelSettingsModeStatic {
		return workerModelRuntimeBinding{}, nil
	}
	if mode != config.ModelSettingsModeManaged || models == nil || models.Revision() < 0 || ids == nil {
		return workerModelRuntimeBinding{}, errors.New("managed worker model runtime binding is invalid")
	}
	instanceID, err := ids.New()
	if err != nil {
		return workerModelRuntimeBinding{}, err
	}
	parsed, err := foundation.ParseID(string(instanceID))
	if err != nil || parsed != instanceID {
		return workerModelRuntimeBinding{}, errors.New("managed worker model runtime instance is invalid")
	}
	revision := models.Revision()
	return workerModelRuntimeBinding{
		revision: &revision, instanceID: &instanceID, runtimeFreshWithin: modelsettingsapplication.DefaultRuntimeFreshWithin,
	}, nil
}

func (binding workerModelRuntimeBinding) runtimeWorkerOptions() []riveradapter.RuntimeWorkerOptions {
	if binding.revision == nil && binding.instanceID == nil && binding.executorAcquirer == nil {
		return nil
	}
	return []riveradapter.RuntimeWorkerOptions{{
		ModelSettingsRevision: binding.revision, ModelRuntimeInstanceID: binding.instanceID, RuntimeExecutorAcquirer: binding.executorAcquirer,
	}}
}

func modelRuntimeForComposition(cfg config.Config, supplied ...*modelsettingsruntime.Models) (*modelsettingsruntime.Models, error) {
	if len(supplied) > 1 {
		return nil, errors.New("composition accepts at most one frozen model runtime")
	}
	if len(supplied) == 1 {
		if supplied[0] == nil {
			return nil, errors.New("frozen model runtime is nil")
		}
		return supplied[0], nil
	}
	if cfg.ModelSettingsMode != config.ModelSettingsModeStatic {
		return nil, errors.New("managed composition requires a prepared model runtime")
	}
	loaded, err := modelsettingsruntime.LoadSettings(context.Background(), cfg, nil)
	if err != nil {
		return nil, err
	}
	return loaded.Models, nil
}

func newWorkerComponentsWithModels(db *pgxpool.Pool, cfg config.Config, models *modelsettingsruntime.Models, modelBinding workerModelRuntimeBinding, workspaceRepository *workspacepostgres.Repository, logger *slog.Logger, metrics observability.Metrics, tracer observability.Tracer, enqueueFences ...riveradapter.EnqueueFence) (workerComponents, error) {
	if db == nil {
		return workerComponents{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_DATABASE_UNAVAILABLE", true, errors.New("database pool is nil"))
	}
	if models == nil {
		return workerComponents{}, foundation.NewError(foundation.ErrorDependencyUnavailable, modelsettingsdomain.ErrorCodeUnavailable, false, errors.New("model runtime is nil"))
	}
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		if modelBinding.revision == nil || modelBinding.instanceID == nil || !modelsettingsapplication.ValidRuntimeFreshWithin(modelBinding.runtimeFreshWithin) || modelBinding.executorAcquirer == nil || modelBinding.sourceRefreshAcquirer == nil || *modelBinding.revision != models.Revision() {
			return workerComponents{}, foundation.NewError(foundation.ErrorConsistencyViolation, modelsettingsdomain.ErrorCodeRuntimeConflict, false, errors.New("managed worker model runtime binding does not match the frozen runtime"))
		}
	} else if modelBinding.revision != nil || modelBinding.instanceID != nil || modelBinding.runtimeFreshWithin != 0 || modelBinding.executorAcquirer != nil || modelBinding.sourceRefreshAcquirer != nil {
		return workerComponents{}, foundation.NewError(foundation.ErrorConsistencyViolation, modelsettingsdomain.ErrorCodeRuntimeConflict, false, errors.New("static worker must not bind a managed model runtime"))
	}
	draftStreams, err := conversationpostgres.NewDraftStreamRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	if workspaceRepository == nil {
		var err error
		workspaceRepository, err = workspacepostgres.NewRepository(db)
		if err != nil {
			return workerComponents{}, err
		}
	}
	gitOperationLocker, err := gitoperation.NewPostgresLocker(db)
	if err != nil {
		return workerComponents{}, err
	}
	writebackRepository, err := changecontrolpostgres.NewRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	validator, err := changecontrollocalfs.NewDefaultMarkdownValidator()
	if err != nil {
		return workerComponents{}, err
	}
	workspaceStore, err := changecontrollocalfs.NewWriter(workspaceRepository, validator)
	if err != nil {
		return workerComponents{}, err
	}
	gitRepository, err := gitcli.NewWritebackClient(gitcli.New(""), workspaceRepository)
	if err != nil {
		return workerComponents{}, err
	}
	targetReader, err := changecontrollocalfs.NewReader(workspaceRepository)
	if err != nil {
		return workerComponents{}, err
	}
	toolComponents, err := newToolRuntimeComponents(db, cfg, workspaceRepository, gitRepository, models)
	if err != nil {
		return workerComponents{}, err
	}
	authoringRepository, err := authoringpostgres.NewRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	publicationFinalizer, err := authoringchangecontrol.NewPublicationFinalizer(authoringRepository, foundation.SystemClock{})
	if err != nil {
		return workerComponents{}, err
	}
	service, err := changecontrolapplication.NewWritebackService(changecontrolapplication.WritebackServiceDependencies{
		Repository: writebackRepository, Workspace: workspaceStore, Git: gitRepository, GitOperations: gitOperationLocker, Audit: toolComponents.writebackAudit,
		Publication: publicationFinalizer, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return workerComponents{}, err
	}
	node, err := changecontrolworkflow.NewNode(service)
	if err != nil {
		return workerComponents{}, err
	}
	changeControlService, err := changecontrolapplication.NewService(writebackRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, targetReader, gitRepository)
	if err != nil {
		return workerComponents{}, err
	}
	bootstrap, err := changecontrolworkflow.NewBootstrapExecutor(changecontrolworkflow.BootstrapExecutorDependencies{
		Lookup: writebackRepository, ChangeControl: changeControlService, Beginner: service,
		Node: node, IDs: foundation.NewUUIDGenerator(nil),
	})
	if err != nil {
		return workerComponents{}, err
	}
	catalog, err := workflowapplication.NewValidationCatalog([]int{1, 2}, capability.All())
	if err != nil {
		return workerComponents{}, err
	}
	executors, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		return workerComponents{}, err
	}
	riverOptions := riveradapter.Options{
		Queue: cfg.WorkerQueue, MaxWorkers: cfg.WorkerMaxWorkers,
		JobTimeout: cfg.WorkerJobTimeout, RescueStuckJobsAfter: cfg.WorkerRescueStuckJobsAfter,
		SoftStopTimeout: cfg.WorkerSoftStopTimeout, Logger: logger,
	}
	if len(enqueueFences) > 0 {
		riverOptions.EnqueueFence = enqueueFences[0]
	}
	insertClient, err := riveradapter.NewClientWithOptions(db, nil, riverOptions)
	if err != nil {
		return workerComponents{}, err
	}
	inserter, err := riveradapter.NewJobInserter(insertClient)
	if err != nil {
		return workerComponents{}, err
	}
	workerID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		return workerComponents{}, err
	}
	if err := executors.Register(changecontrolworkflow.SafeWritebackNodeKind, changecontrolworkflow.SafeWritebackBootstrapInputSchemaVersion, bootstrap); err != nil {
		return workerComponents{}, err
	}
	graphRepository, err := graphpostgres.NewRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	scanState, err := graphpostgres.NewSemanticLinkScanStateRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	scanService, err := graphapplication.NewSemanticLinkScanStateService(scanState)
	if err != nil {
		return workerComponents{}, err
	}
	pageSource, err := graphpostgres.NewSemanticLinkTopicScanPageRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	collectionRepository, err := collectionpostgres.NewRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	collectionService, err := collectionapplication.NewService(collectionapplication.Dependencies{
		Repository: collectionRepository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return workerComponents{}, err
	}
	smartPageSource, err := graphpostgres.NewSmartCollectionScanPageRepository(collectionService, db)
	if err != nil {
		return workerComponents{}, err
	}
	pageRouter, err := graphapplication.NewSemanticLinkScanPageSourceRouter(pageSource, smartPageSource)
	if err != nil {
		return workerComponents{}, err
	}
	candidateWriter, err := graphpostgres.NewSemanticLinkDiscoveryCandidateWriter(db, graphRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		return workerComponents{}, err
	}
	pageExecutor, err := graphapplication.NewSemanticLinkTopicScanExecutor(pageRouter, graphapplication.NewSemanticLinkDiscoveryService(nil, nil), candidateWriter)
	if err != nil {
		return workerComponents{}, err
	}
	semanticScan, err := graphworkflow.NewSemanticLinkScanExecutor(scanState, scanService, pageExecutor, foundation.SystemClock{})
	if err != nil {
		return workerComponents{}, err
	}
	if err := executors.Register(graphapplication.SemanticLinkScanNodeKind, graphapplication.SemanticLinkScanInputSchemaVersion, semanticScan); err != nil {
		return workerComponents{}, err
	}
	if err := executors.Register(graphapplication.SemanticLinkScanNodeKind, graphapplication.SemanticLinkSmartCollectionScanInputSchemaVersion, semanticScan); err != nil {
		return workerComponents{}, err
	}
	healthMembership, err := healthcollection.NewMembership(collectionService)
	if err != nil {
		return workerComponents{}, err
	}
	healthFactReader, err := healthpostgres.NewFactReader(db, healthMembership)
	if err != nil {
		return workerComponents{}, err
	}
	healthRegistry, err := healthdetector.NewDefaultRegistry(healthFactReader, healthdetector.DefaultConfig())
	if err != nil {
		return workerComponents{}, err
	}
	healthEvents, err := eventspostgres.NewStore(db)
	if err != nil {
		return workerComponents{}, err
	}
	healthScanState, err := healthpostgres.NewScanStateRepository(db, healthEvents)
	if err != nil {
		return workerComponents{}, err
	}
	healthScanService, err := healthapplication.NewScanStateService(healthScanState)
	if err != nil {
		return workerComponents{}, err
	}
	healthIssueRepository, err := healthpostgres.NewSmartCollectionIssueRepository(db, healthMembership, foundation.NewUUIDGenerator(nil))
	if err != nil {
		return workerComponents{}, err
	}
	healthScan, err := healthworkflowadapter.NewHealthScanExecutor(healthRegistry, healthScanService, healthIssueRepository, foundation.SystemClock{})
	if err != nil {
		return workerComponents{}, err
	}
	if err := executors.Register(healthapplication.HealthScanNodeKind, healthapplication.HealthScanInputSchemaVersion, healthScan); err != nil {
		return workerComponents{}, err
	}
	healthCancellationGuard, err := healthpostgres.NewScanCancellationGuard(healthEvents)
	if err != nil {
		return workerComponents{}, err
	}
	cancellationGuard, err := workflowapplication.NewCompositeCancellationSafetyGuard(
		writebackRepository,
		graphpostgres.NewSemanticLinkScanCancellationGuard(),
		healthCancellationGuard,
	)
	if err != nil {
		return workerComponents{}, err
	}
	artifactAgentRepository, artifactTerminal, err := newArtifactGenerationAgent(db)
	if err != nil {
		return workerComponents{}, err
	}
	terminalHooks, err := workflowapplication.NewCompositeWorkflowTerminalHook(
		artifactTerminal,
		organizingworkflow.NewTerminalHook(),
	)
	if err != nil {
		return workerComponents{}, err
	}
	runtimeRepository, err := workflowpostgres.NewRuntimeRepositoryWithHooks(db, inserter, workflowpostgres.RuntimeRepositoryHooks{
		CancellationSafety:      cancellationGuard,
		Terminal:                terminalHooks,
		ModelRuntimeFreshWithin: modelBinding.runtimeFreshWithin,
	})
	if err != nil {
		return workerComponents{}, err
	}
	workflowRepository, err := workflowpostgres.NewRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	sourceProcessing, err := newSourceProcessingComponents(
		db, cfg, workspaceRepository, gitRepository, modelBinding.revision, models,
	)
	if err != nil {
		return workerComponents{}, err
	}
	gitSyncSourceProcessing := sourceProcessing
	if modelBinding.sourceRefreshAcquirer != nil {
		dynamicRefresher, dynamicErr := retrievalapplication.NewDynamicSourceRefresher(modelBinding.sourceRefreshAcquirer)
		if dynamicErr != nil {
			return workerComponents{}, dynamicErr
		}
		gitSyncSourceProcessing.refresher = dynamicRefresher
	}
	gitSyncWorker, gitSyncScheduler, err := newGitSyncWorker(
		db, cfg, workspaceRepository, gitRepository, gitSyncSourceProcessing, gitOperationLocker, workerID,
	)
	if err != nil {
		return workerComponents{}, err
	}
	gitSyncCapability := agentCapabilityStatus{code: gitsyncdomain.ErrorCodeUnavailable}
	if gitSyncWorker != nil && gitSyncScheduler != nil {
		gitSyncCapability = agentCapabilityStatus{available: true}
	}
	captureRepository, err := capturepostgres.NewRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	captureFetcher, err := capturehttpfetch.New(capturehttpfetch.Options{})
	if err != nil {
		return workerComponents{}, err
	}
	memoryRepository, err := memorypostgres.NewRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	memoryService, err := memoryapplication.NewService(memoryapplication.Dependencies{
		Repository: memoryRepository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return workerComponents{}, err
	}
	agentComponents, err := newAgentWorkflowComponentsWithToolsAndMetrics(db, cfg, workspaceRepository, memoryService, toolComponents, metrics, models)
	if err != nil {
		return workerComponents{}, err
	}
	captureProfileGenerator, captureProfileCapability, err := newCaptureProfileGenerator(
		db, agentComponents.model, agentComponents.contract, artifactAgentRepository,
		agentComponents.captureScheduler,
		foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		return workerComponents{}, err
	}
	captureExecutor, err := newCaptureWorkflowExecutor(
		captureRepository, captureRepository, sourceProcessing.workspace, captureFetcher,
		sourceProcessing.refresher, captureProfileGenerator, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		return workerComponents{}, err
	}
	if err := executors.Register(captureapplication.ProcessingNodeKind, captureapplication.ProcessingInputSchemaVersion, captureExecutor); err != nil {
		return workerComponents{}, err
	}
	if agentComponents.relation != nil {
		if err := executors.Register(agentworkflow.RelationAssessmentNodeKind, agentworkflow.RelationAssessmentInputSchemaVersion, agentComponents.relation); err != nil {
			return workerComponents{}, err
		}
		if agentComponents.rag == nil {
			return workerComponents{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_RAG_EXECUTOR_UNAVAILABLE", false, errors.New("chat is enabled but the RAG executor is unavailable"))
		}
		if err := executors.Register(agentworkflow.RAGWorkflowNodeKind, agentworkflow.RAGWorkflowInputSchemaVersion, agentComponents.rag); err != nil {
			return workerComponents{}, err
		}
	}
	artifactIDs := foundation.NewUUIDGenerator(nil)
	artifactClock := foundation.SystemClock{}
	artifactComponents, err := newArtifactWorkflowComponents(
		db, workspaceRepository, runtimeRepository, artifactAgentRepository, artifactTerminal,
		agentComponents.model, agentComponents.contract, agentComponents.artifactScheduler,
		artifactIDs, artifactClock, models.Embedding().Embedder(),
	)
	if err != nil {
		return workerComponents{}, err
	}
	if artifactComponents.executor != nil {
		if err := executors.Register(artifactworkflow.NodeKind, artifactworkflow.InputSchemaVersion, artifactComponents.executor); err != nil {
			return workerComponents{}, err
		}
	}
	artifactRepository, err := artifactpostgres.NewRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	documentSourceVerifier, err := artifactauthoring.NewVerifier(authoringRepository)
	if err != nil {
		return workerComponents{}, err
	}
	documentContentReader, err := organizingowner.NewDocumentContentReader(authoringRepository)
	if err != nil {
		return workerComponents{}, err
	}
	artifactCommands, err := artifactapplication.NewCommandService(artifactapplication.Dependencies{
		Repository: artifactRepository, Evidence: artifactComponents.citationVerifier,
		Documents: documentSourceVerifier,
		IDs:       artifactIDs, Clock: artifactClock,
	})
	if err != nil {
		return workerComponents{}, err
	}
	artifactQueries, err := artifactapplication.NewQueryService(artifactRepository)
	if err != nil {
		return workerComponents{}, err
	}
	organizingRepository, err := organizingpostgres.NewRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	builtInContext, cancelBuiltIns := context.WithTimeout(context.Background(), cfg.DatabasePingTimeout)
	builtInErr := organizingRepository.EnsureBuiltIns(builtInContext, foundation.SystemClock{}.Now())
	cancelBuiltIns()
	if builtInErr != nil {
		return workerComponents{}, builtInErr
	}
	organizingRenderer, err := organizingworkflow.NewEvidenceRenderer(artifactComponents.citationVerifier)
	if err != nil {
		return workerComponents{}, err
	}
	var organizingGenerator organizingworkflow.ContentGenerator = organizingworkflow.NewUnavailableGenerator()
	if agentComponents.model != nil {
		organizingGenerationRepository, repositoryErr := organizingpostgres.NewGenerationRepository(db, artifactAgentRepository)
		if repositoryErr != nil {
			return workerComponents{}, repositoryErr
		}
		organizingCatalog, catalogErr := newOrganizingRuntimeCatalog(agentComponents.contract)
		if catalogErr != nil {
			return workerComponents{}, catalogErr
		}
		organizingGenerator, err = organizingworkflow.NewGenerator(organizingworkflow.GeneratorDependencies{
			Model: agentComponents.model, Scheduler: agentComponents.organizingScheduler, Catalog: organizingCatalog, ModelRuns: artifactAgentRepository,
			Store: organizingGenerationRepository, Evidence: organizingRenderer, Documents: documentContentReader,
			ProfileRef: agentworkflow.DefaultProfileRef(), IDs: foundation.NewUUIDGenerator(nil),
			Clock: foundation.SystemClock{}, Budget: agentApplicationBudget(agentComponents.contract),
		})
		if err != nil {
			return workerComponents{}, err
		}
	}
	organizingArtifacts, err := organizingworkflow.NewArtifactOwner(artifactCommands, artifactQueries)
	if err != nil {
		return workerComponents{}, err
	}
	organizingProposals, err := organizingworkflow.NewProposalOwner(changeControlService)
	if err != nil {
		return workerComponents{}, err
	}
	organizingExecutor, err := organizingworkflow.NewExecutor(organizingworkflow.ExecutorDependencies{
		Runs: workflowRepository, Snapshots: organizingRepository, Templates: organizingRepository,
		Bindings: organizingRepository, Stages: runtimeRepository, Artifacts: organizingArtifacts,
		Proposals: organizingProposals, Generator: organizingGenerator,
		IDs: foundation.NewUUIDGenerator(nil),
	})
	if err != nil {
		return workerComponents{}, err
	}
	var runtimeGeneration workerRuntimeGenerationBuilder
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		runtimeGeneration = newWorkerRuntimeGenerationBuilder(
			db, cfg, catalog, workspaceRepository, gitRepository,
			bootstrap, semanticScan, healthScan,
			artifactAgentRepository, artifactTerminal, runtimeRepository, workflowRepository,
			captureRepository, captureFetcher, memoryService,
			organizingRepository, organizingArtifacts, organizingProposals, organizingRenderer, documentContentReader,
			metrics,
		)
	}
	for _, kind := range organizingworkflow.ExecutorNodeKinds() {
		if err := executors.Register(kind, organizingworkflow.InputSchemaVersion, organizingExecutor); err != nil {
			return workerComponents{}, err
		}
	}
	if toolComponents.runtimeEnabled {
		if toolComponents.workflow == nil || toolComponents.definition == nil {
			return workerComponents{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_TOOL_EXECUTORS_UNAVAILABLE", false, errors.New("tool workflow executor or definition is unavailable"))
		}
		if err := executors.Register(toolworkflow.NodeKind, toolworkflow.InputSchemaVersion, toolComponents.workflow); err != nil {
			return workerComponents{}, err
		}
	}
	if err := executors.Freeze(); err != nil {
		return workerComponents{}, err
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors, toolComponents.contracts)
	if err != nil {
		return workerComponents{}, err
	}
	if err := definitions.Register(changecontrolworkflow.RegisteredDefinition()); err != nil {
		return workerComponents{}, err
	}
	semanticScanDefinition, err := graphapplication.RegisteredSemanticLinkScanDefinition()
	if err != nil {
		return workerComponents{}, err
	}
	if err := definitions.Register(semanticScanDefinition); err != nil {
		return workerComponents{}, err
	}
	semanticSmartScanDefinition, err := graphapplication.RegisteredSemanticLinkSmartCollectionScanDefinition()
	if err != nil {
		return workerComponents{}, err
	}
	if err := definitions.Register(semanticSmartScanDefinition); err != nil {
		return workerComponents{}, err
	}
	healthScanDefinition, err := healthworkflow.RegisteredDefinition()
	if err != nil {
		return workerComponents{}, err
	}
	if err := definitions.Register(healthScanDefinition); err != nil {
		return workerComponents{}, err
	}
	captureDefinition, err := captureworkflow.RegisteredDefinition()
	if err != nil {
		return workerComponents{}, err
	}
	if err := definitions.Register(captureDefinition); err != nil {
		return workerComponents{}, err
	}
	if agentComponents.relation != nil {
		if err := definitions.Register(agentworkflow.RegisteredDefinition()); err != nil {
			return workerComponents{}, err
		}
		// v1 remains registered for replaying runs persisted before the
		// tool-enabled RAG definition was introduced. New starts use v2.
		if err := definitions.Register(agentworkflow.RegisteredRAGDefinitionV1()); err != nil {
			return workerComponents{}, err
		}
		if err := definitions.Register(agentworkflow.RegisteredRAGDefinitionV2()); err != nil {
			return workerComponents{}, err
		}
	}
	if artifactComponents.executor != nil {
		if err := definitions.Register(artifactworkflow.RegisteredDefinition()); err != nil {
			return workerComponents{}, err
		}
	}
	if toolComponents.runtimeEnabled {
		if err := definitions.Register(*toolComponents.definition); err != nil {
			return workerComponents{}, err
		}
	}
	for _, definition := range organizingworkflow.RegisteredDefinitions() {
		if err := definitions.Register(definition); err != nil {
			return workerComponents{}, err
		}
	}
	if err := definitions.Freeze(); err != nil {
		return workerComponents{}, err
	}
	workflowService, err := workflowapplication.NewRuntimeService(
		workflowRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
		workflowapplication.RuntimeDependencies{
			Definitions: definitions, Starter: runtimeRepository, State: runtimeRepository, Human: runtimeRepository,
		},
	)
	if err != nil {
		return workerComponents{}, err
	}
	captureOutbox, err := captureapplication.NewOutboxDispatcher(captureapplication.DispatcherDependencies{
		Outbox: captureRepository, Workflows: captureworkflow.Starter{Runtime: workflowService},
		Owner: fmt.Sprintf("capture-worker:%s", workerID), LeaseDuration: captureDispatchLeaseDuration,
		RetryBase: captureDispatchRetryBase, MaxAttempts: captureDispatchMaxAttempts,
	})
	if err != nil {
		return workerComponents{}, err
	}
	organizingOutbox, err := organizingworkflow.NewDispatcher(organizingworkflow.DispatcherDependencies{
		Repository: organizingRepository, Workflows: workflowService,
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
		Owner: fmt.Sprintf("organizing-worker:%s", workerID), LeaseDuration: organizingDispatchLeaseDuration,
		RetryBase: organizingDispatchRetryBase, MaxAttempts: organizingDispatchMaxAttempts,
	})
	if err != nil {
		return workerComponents{}, err
	}
	healthScanStartRepository, err := healthpostgres.NewScanRepository(db, runtimeRepository, healthEvents, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, healthcollection.DurableBindingVerifier{})
	if err != nil {
		return workerComponents{}, err
	}
	healthScanStartService, err := healthapplication.NewSmartCollectionScanService(healthScanStartRepository, healthScanStartRepository, healthRegistry, healthMembership)
	if err != nil {
		return workerComponents{}, err
	}
	healthScheduleRepository, err := healthpostgres.NewScheduleRepository(db, foundation.NewUUIDGenerator(nil))
	if err != nil {
		return workerComponents{}, err
	}
	healthSchedule, err := healthapplication.NewScheduleDispatcher(healthScheduleRepository, healthScanStartService, healthRegistry)
	if err != nil {
		return workerComponents{}, err
	}
	healthAffectedPlanner, err := healthapplication.NewAffectedChangePlanner(healthRegistry)
	if err != nil {
		return workerComponents{}, err
	}
	healthAffectedRepository, err := healthpostgres.NewAffectedChangeDispatchRepository(healthScanStartRepository, healthAffectedPlanner)
	if err != nil {
		return workerComponents{}, err
	}
	healthAffected, err := healthapplication.NewAffectedChangeDispatcher(healthAffectedRepository)
	if err != nil {
		return workerComponents{}, err
	}
	timelineRepository, err := knowledgepostgres.NewRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	timelineProject, err := knowledgeapplication.NewTimelineProjectionDispatcher(timelineRepository)
	if err != nil {
		return workerComponents{}, err
	}
	citationBackfill, err := artifactapplication.NewCitationBackfillDispatcher(artifactRepository)
	if err != nil {
		return workerComponents{}, err
	}
	runtimeCoordinator, err := workflowapplication.NewRuntimeCoordinator(runtimeRepository)
	if err != nil {
		return workerComponents{}, err
	}
	exportSnapshots, err := exportcollection.NewSnapshotReader(collectionService)
	if err != nil {
		return workerComponents{}, err
	}
	exportFiles, err := exportlocalfs.NewStore(workspaceRepository)
	if err != nil {
		return workerComponents{}, err
	}
	exportRepository, err := exportpostgres.NewRepository(db, exportpostgres.WithEventAppender(healthEvents))
	if err != nil {
		return workerComponents{}, err
	}
	exportDispatcher, err := exportriver.NewTransactionalDispatcher(db, insertClient)
	if err != nil {
		return workerComponents{}, err
	}
	exportService, err := exportapplication.NewService(exportapplication.Dependencies{
		Repository: exportRepository, Dispatcher: exportDispatcher, Snapshots: exportSnapshots,
		Workspaces: workspaceRepository, Files: exportFiles, Attachments: exportFiles,
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return workerComponents{}, err
	}
	exportWorker, err := exportriver.NewWorker(exportService, fmt.Sprintf("worker:%s", workerID))
	if err != nil {
		return workerComponents{}, err
	}
	interviewCompletion, err := interviewpostgres.NewRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	learningPathRepository, err := learningpathpostgres.NewRepository(db)
	if err != nil {
		return workerComponents{}, err
	}
	learningPathMaintenance, err := learningpathapplication.NewReservationMaintainer(learningPathRepository, foundation.SystemClock{})
	if err != nil {
		return workerComponents{}, err
	}
	fatalInvariants := make(chan error, 1)
	runtimeWorker, err := riveradapter.NewRuntimeNodeWorkerWithObservability(executors, runtimeCoordinator, fmt.Sprintf("worker:%s", workerID), cfg.WorkflowLeaseDuration, cfg.WorkflowHeartbeatInterval, riveradapter.RuntimeWorkerObservability{
		Metrics: metrics, Tracer: tracer, Queue: cfg.WorkerQueue, Logger: logger, FatalInvariants: fatalInvariants,
	}, modelBinding.runtimeWorkerOptions()...)
	if err != nil {
		return workerComponents{}, err
	}
	reindex, err := newReindexComponents(
		db, cfg, sourceProcessing, insertClient, workerID, logger, metrics, fatalInvariants, models,
	)
	if err != nil {
		return workerComponents{}, err
	}
	workers := riveradapter.NewWorkers()
	if err := riveradapter.AddRuntimeWorkerSafely(workers, runtimeWorker); err != nil {
		return workerComponents{}, err
	}
	if err := reindexriver.AddWorkerSafely(workers, reindex.worker); err != nil {
		return workerComponents{}, err
	}
	if err := riveradapter.AddWorkerSafely(workers, exportWorker); err != nil {
		return workerComponents{}, err
	}
	runtimeClient, err := riveradapter.NewClientWithOptions(db, workers, riverOptions)
	if err != nil {
		return workerComponents{}, err
	}
	return workerComponents{
		safeWriteback: node, tools: toolComponents, agentCapability: agentComponents.capability, artifact: artifactComponents,
		captureExecutor: captureExecutor, captureOutbox: captureOutbox, captureProfile: captureProfileCapability,
		organizingExecutor: organizingExecutor, organizingOutbox: organizingOutbox,
		gitSyncWorker: gitSyncWorker, gitSyncScheduler: gitSyncScheduler, gitSyncCapability: gitSyncCapability,
		reindexWorker: reindex.worker, reindexRuntime: reindex.runtime, dispatcher: reindex.dispatcher,
		runtimeClient: runtimeClient, definitions: definitions, executors: executors, runtimeGeneration: runtimeGeneration, sourceProcessing: sourceProcessing, semanticScan: semanticScan, healthScan: healthScan, healthScanStart: healthScanStartService, healthSchedule: healthSchedule, healthAffected: healthAffected, timelineProject: timelineProject, citationBackfill: citationBackfill,
		exportWorker: exportWorker, exportService: exportService, memoryExpiry: memoryService,
		interviewCompletion: interviewCompletion, learningPathMaintenance: learningPathMaintenance, draftStreams: draftStreams,
		fatalInvariants: fatalInvariants,
	}, nil
}

func newWorkerRuntimeGenerationBuilder(
	db *pgxpool.Pool,
	cfg config.Config,
	catalog workflowapplication.ValidationCatalog,
	workspaceRepository *workspacepostgres.Repository,
	gitRepository *gitcli.WritebackClient,
	safeWriteback workflowapplication.Executor,
	semanticScan workflowapplication.Executor,
	healthScan workflowapplication.Executor,
	artifactAgentRepository *agentpostgres.Repository,
	artifactTerminal *artifactpostgres.SectionGenerationTerminalHook,
	runtimeRepository *workflowpostgres.RuntimeRepository,
	workflowRepository *workflowpostgres.Repository,
	captureRepository *capturepostgres.Repository,
	captureFetcher captureapplication.URLFetcher,
	memoryService *memoryapplication.Service,
	organizingRepository *organizingpostgres.Repository,
	organizingArtifacts *organizingworkflow.ArtifactOwner,
	organizingProposals *organizingworkflow.ProposalOwner,
	organizingRenderer *organizingworkflow.EvidenceRenderer,
	documentContentReader *organizingowner.DocumentContentReader,
	metrics observability.Metrics,
) workerRuntimeGenerationBuilder {
	return func(ctx context.Context, models *modelsettingsruntime.Models) (*workerRuntimeGeneration, error) {
		if ctx == nil || models == nil || models.Revision() <= 0 {
			return nil, workerRuntimeRevisionError(errors.New("managed worker runtime executor build input is invalid"))
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		executors, err := workflowapplication.NewExecutorRegistry(catalog)
		if err != nil {
			return nil, err
		}
		if err := executors.Register(changecontrolworkflow.SafeWritebackNodeKind, changecontrolworkflow.SafeWritebackBootstrapInputSchemaVersion, safeWriteback); err != nil {
			return nil, err
		}
		if err := executors.Register(graphapplication.SemanticLinkScanNodeKind, graphapplication.SemanticLinkScanInputSchemaVersion, semanticScan); err != nil {
			return nil, err
		}
		if err := executors.Register(graphapplication.SemanticLinkScanNodeKind, graphapplication.SemanticLinkSmartCollectionScanInputSchemaVersion, semanticScan); err != nil {
			return nil, err
		}
		if err := executors.Register(healthapplication.HealthScanNodeKind, healthapplication.HealthScanInputSchemaVersion, healthScan); err != nil {
			return nil, err
		}

		toolComponents, err := newToolRuntimeComponents(db, cfg, workspaceRepository, gitRepository, models)
		if err != nil {
			return nil, err
		}
		revision := models.Revision()
		sourceProcessing, err := newSourceProcessingComponents(db, cfg, workspaceRepository, gitRepository, &revision, models)
		if err != nil {
			return nil, err
		}
		agentComponents, err := newAgentWorkflowComponentsWithToolsAndMetrics(db, cfg, workspaceRepository, memoryService, toolComponents, metrics, models)
		if err != nil {
			return nil, err
		}
		captureProfileGenerator, _, err := newCaptureProfileGenerator(
			db, agentComponents.model, agentComponents.contract, artifactAgentRepository, agentComponents.captureScheduler,
			foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
		)
		if err != nil {
			return nil, err
		}
		captureExecutor, err := newCaptureWorkflowExecutor(
			captureRepository, captureRepository, sourceProcessing.workspace, captureFetcher,
			sourceProcessing.refresher, captureProfileGenerator, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
		)
		if err != nil {
			return nil, err
		}
		if err := executors.Register(captureapplication.ProcessingNodeKind, captureapplication.ProcessingInputSchemaVersion, captureExecutor); err != nil {
			return nil, err
		}
		if err := registerWorkerAgentExecutors(executors, agentComponents); err != nil {
			return nil, err
		}

		artifactComponents, err := newArtifactWorkflowComponents(
			db, workspaceRepository, runtimeRepository, artifactAgentRepository, artifactTerminal,
			agentComponents.model, agentComponents.contract, agentComponents.artifactScheduler,
			foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, models.Embedding().Embedder(),
		)
		if err != nil {
			return nil, err
		}
		if err := registerWorkerArtifactExecutor(executors, artifactComponents); err != nil {
			return nil, err
		}

		var organizingGenerator organizingworkflow.ContentGenerator = organizingworkflow.NewUnavailableGenerator()
		if agentComponents.model != nil {
			organizingGenerationRepository, err := organizingpostgres.NewGenerationRepository(db, artifactAgentRepository)
			if err != nil {
				return nil, err
			}
			organizingCatalog, err := newOrganizingRuntimeCatalog(agentComponents.contract)
			if err != nil {
				return nil, err
			}
			organizingGenerator, err = organizingworkflow.NewGenerator(organizingworkflow.GeneratorDependencies{
				Model: agentComponents.model, Scheduler: agentComponents.organizingScheduler, Catalog: organizingCatalog, ModelRuns: artifactAgentRepository,
				Store: organizingGenerationRepository, Evidence: organizingRenderer, Documents: documentContentReader,
				ProfileRef: agentworkflow.DefaultProfileRef(), IDs: foundation.NewUUIDGenerator(nil),
				Clock: foundation.SystemClock{}, Budget: agentApplicationBudget(agentComponents.contract),
			})
			if err != nil {
				return nil, err
			}
		}
		organizingExecutor, err := organizingworkflow.NewExecutor(organizingworkflow.ExecutorDependencies{
			Runs: workflowRepository, Snapshots: organizingRepository, Templates: organizingRepository,
			Bindings: organizingRepository, Stages: runtimeRepository, Artifacts: organizingArtifacts,
			Proposals: organizingProposals, Generator: organizingGenerator, IDs: foundation.NewUUIDGenerator(nil),
		})
		if err != nil {
			return nil, err
		}
		for _, kind := range organizingworkflow.ExecutorNodeKinds() {
			if err := executors.Register(kind, organizingworkflow.InputSchemaVersion, organizingExecutor); err != nil {
				return nil, err
			}
		}
		if toolComponents.runtimeEnabled {
			if toolComponents.workflow == nil || toolComponents.definition == nil {
				return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_TOOL_EXECUTORS_UNAVAILABLE", false, errors.New("tool workflow executor or definition is unavailable"))
			}
			if err := executors.Register(toolworkflow.NodeKind, toolworkflow.InputSchemaVersion, toolComponents.workflow); err != nil {
				return nil, err
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := executors.Freeze(); err != nil {
			return nil, err
		}
		if sourceProcessing.workspace == nil || sourceProcessing.ingestion == nil || sourceProcessing.retrieval == nil ||
			sourceProcessing.store == nil || sourceProcessing.vectors == nil || sourceProcessing.regression == nil || sourceProcessing.refresher == nil {
			return nil, workerRuntimeNotReadyError(errors.New("managed worker source-processing generation is incomplete"))
		}
		return &workerRuntimeGeneration{executors: executors, sources: sourceProcessing}, nil
	}
}

func registerWorkerAgentExecutors(registry *workflowapplication.ExecutorRegistry, components agentWorkflowComponents) error {
	var relation workflowapplication.Executor = workerUnavailableExecutor{code: agentworkflow.ErrorCodeCapabilityUnavailable}
	var rag workflowapplication.Executor = workerUnavailableExecutor{code: agentworkflow.ErrorCodeCapabilityUnavailable}
	if components.capability.available {
		if components.capability.code != "" || components.relation == nil || components.rag == nil {
			return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_AGENT_EXECUTORS_UNAVAILABLE", false, errors.New("enabled chat worker executors are incomplete"))
		}
		relation = components.relation
		rag = components.rag
	} else if components.capability.code != agentworkflow.ErrorCodeCapabilityUnavailable || components.relation != nil || components.rag != nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKER_AGENT_CAPABILITY_INVALID", false, errors.New("disabled chat worker executor state is inconsistent"))
	}
	if err := registry.Register(agentworkflow.RelationAssessmentNodeKind, agentworkflow.RelationAssessmentInputSchemaVersion, relation); err != nil {
		return err
	}
	return registry.Register(agentworkflow.RAGWorkflowNodeKind, agentworkflow.RAGWorkflowInputSchemaVersion, rag)
}

func registerWorkerArtifactExecutor(registry *workflowapplication.ExecutorRegistry, components artifactWorkflowComponents) error {
	var executor workflowapplication.Executor = workerUnavailableExecutor{code: artifactworkflow.ErrorCodeCapabilityUnavailable}
	if components.capability.available {
		if components.capability.code != "" || components.executor == nil || components.catalog == nil {
			return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_ARTIFACT_EXECUTOR_UNAVAILABLE", false, errors.New("enabled artifact worker executor is incomplete"))
		}
		executor = components.executor
	} else if components.capability.code != artifactworkflow.ErrorCodeCapabilityUnavailable || components.executor != nil || components.catalog != nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKER_ARTIFACT_CAPABILITY_INVALID", false, errors.New("disabled artifact worker executor state is inconsistent"))
	}
	return registry.Register(artifactworkflow.NodeKind, artifactworkflow.InputSchemaVersion, executor)
}

func newToolRuntimeComponents(db *pgxpool.Pool, cfg config.Config, workspaceRepository *workspacepostgres.Repository, gitInspector *gitcli.WritebackClient, modelRuntimes ...*modelsettingsruntime.Models) (toolRuntimeComponents, error) {
	models, err := modelRuntimeForComposition(cfg, modelRuntimes...)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	if err := validateToolCompositionMode(cfg); err != nil {
		return toolRuntimeComponents{}, err
	}
	contracts, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	repository, err := toolpostgres.NewRepository(db)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	auditService, err := toolsapplication.NewTrustedWriteAuditService(contracts, repository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	writebackAudit, err := toolchangecontrol.NewWritebackAuditRecorder(auditService)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	components := toolRuntimeComponents{contracts: contracts, repository: repository, writebackAudit: writebackAudit}
	if cfg.ToolRuntimeMode == config.ToolModeDisabled {
		return components, nil
	}
	if workspaceRepository == nil || gitInspector == nil {
		return toolRuntimeComponents{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_TOOL_DEPENDENCIES_UNAVAILABLE", false, errors.New("tool workspace dependencies are unavailable"))
	}

	searchRepository, err := retrievalpostgres.NewSearchRepository(db)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	artifactReader, err := retrievalworkspace.NewReader(workspaceRepository, filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}})
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	evidenceReference, err := retrievalapplication.NewEvidenceReferenceService(searchRepository, artifactReader)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	searchService, err := retrievalapplication.NewSearchService(searchRepository, models.Embedding().Embedder(), nil)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	knowledgeRepository, err := knowledgepostgres.NewRepository(db)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	eligibility, err := knowledgeapplication.NewEvidenceEligibilityService(knowledgeRepository)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	searchExecutor, err := toolretrieval.NewSearchKnowledgeExecutor(searchService)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	readSourceExecutor, err := toolretrieval.NewReadSourceExecutor(evidenceReference)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	citationExecutor, err := toolretrieval.NewValidateCitationExecutor(evidenceReference, eligibility)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	gitStatusExecutor, err := toolworkspace.NewReadGitStatusExecutor(gitInspector)
	if err != nil {
		return toolRuntimeComponents{}, err
	}

	executionRegistry := toolsapplication.NewExecutionRegistry()
	refs := enabledReadToolRefs()
	enabled := []struct {
		ref      toolsdomain.ToolRef
		executor toolsapplication.Executor
	}{
		{refs[0], searchExecutor},
		{refs[1], readSourceExecutor},
		{refs[2], citationExecutor},
		{refs[3], toolchangecontrol.NewCalculateDiffExecutor()},
		{refs[4], gitStatusExecutor},
		{refs[5], readSourceExecutor},
		{refs[6], citationExecutor},
	}
	for _, item := range enabled {
		contract, err := contracts.ResolveContract(item.ref)
		if err != nil {
			return toolRuntimeComponents{}, err
		}
		if err := executionRegistry.RegisterContract(contract); err != nil {
			return toolRuntimeComponents{}, err
		}
		if err := executionRegistry.RegisterExecutor(item.ref, item.executor); err != nil {
			return toolRuntimeComponents{}, err
		}
		components.enabledRefs = append(components.enabledRefs, item.ref)
	}
	if err := executionRegistry.Freeze(); err != nil {
		return toolRuntimeComponents{}, err
	}
	executionService, err := toolsapplication.NewExecutionService(executionRegistry, repository, repository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	components.executions = executionRegistry
	components.execution = executionService
	workflowExecutor, err := toolworkflow.NewExecutor(executionService)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	workflowDefinition, err := toolworkflow.NewReplayRegisteredDefinition(contracts)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	components.workflow = workflowExecutor
	components.definition = &workflowDefinition
	components.runtimeEnabled = true
	return components, nil
}

func validateToolCompositionMode(cfg config.Config) error {
	if cfg.WebFetchMode == config.ToolModeEnabled {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_WEB_FETCH_POLICY_UNAVAILABLE", false, errors.New("persisted web fetch policy and executor wrapper are unavailable"))
	}
	return nil
}

func enabledReadToolRefs() []toolsdomain.ToolRef {
	return []toolsdomain.ToolRef{
		{Name: "SearchKnowledge", Version: 1},
		{Name: "ReadSource", Version: 1},
		{Name: "ValidateCitation", Version: 1},
		{Name: "CalculateDiff", Version: 1},
		{Name: "ReadGitStatus", Version: 1},
		{Name: "ReadSource", Version: 2},
		{Name: "ValidateCitation", Version: 2},
	}
}

func toolWorkflowReadiness(components workerComponents) (bool, bool, bool) {
	if !components.tools.runtimeEnabled {
		return components.tools.contracts != nil, true, true
	}
	contractsOK := components.tools.contracts != nil
	executorsOK := components.tools.executions != nil && components.tools.execution != nil && components.tools.workflow != nil && components.executors != nil
	if executorsOK {
		_, err := components.executors.Resolve(toolworkflow.NodeKind, toolworkflow.InputSchemaVersion)
		executorsOK = err == nil
	}
	dependenciesOK := components.tools.repository != nil && components.tools.definition != nil && components.definitions != nil
	if dependenciesOK {
		definition, err := components.definitions.Resolve(toolworkflow.DefinitionKey, toolworkflow.DefinitionVersion)
		dependenciesOK = err == nil && len(definition.Graph.Nodes) == 1 && definition.Graph.Nodes[0].Kind == toolworkflow.NodeKind
	}
	return contractsOK, executorsOK, dependenciesOK
}

func captureWorkflowReadiness(components workerComponents) bool {
	if components.captureExecutor == nil || components.captureOutbox == nil || components.executors == nil || components.definitions == nil {
		return false
	}
	if components.captureProfile.available {
		if components.captureProfile.code != "" {
			return false
		}
	} else if components.captureProfile.code != captureprofile.ErrorCodeCapabilityUnavailable {
		return false
	}
	executor, err := components.executors.Resolve(captureapplication.ProcessingNodeKind, captureapplication.ProcessingInputSchemaVersion)
	if err != nil || executor != components.captureExecutor {
		return false
	}
	definition, err := components.definitions.Resolve(captureapplication.ProcessingDefinitionKey, captureapplication.ProcessingDefinitionVersion)
	return err == nil && len(definition.Graph.Nodes) == 1 && definition.Graph.Nodes[0].Kind == captureapplication.ProcessingNodeKind
}

func organizingWorkflowReadiness(components workerComponents) bool {
	if components.organizingExecutor == nil || components.organizingOutbox == nil || components.executors == nil || components.definitions == nil {
		return false
	}
	for _, kind := range organizingworkflow.ExecutorNodeKinds() {
		executor, err := components.executors.Resolve(kind, organizingworkflow.InputSchemaVersion)
		if err != nil || executor != components.organizingExecutor {
			return false
		}
	}
	for _, registered := range organizingworkflow.RegisteredDefinitions() {
		definition, err := components.definitions.Resolve(registered.Key, registered.Version)
		if err != nil || len(definition.Graph.Nodes) != len(registered.Graph.Nodes) {
			return false
		}
	}
	return true
}

func gitSyncWorkerReadiness(components workerComponents) bool {
	if !components.gitSyncCapability.available {
		return components.gitSyncWorker == nil && components.gitSyncScheduler == nil && components.gitSyncCapability.code == gitsyncdomain.ErrorCodeUnavailable
	}
	return components.gitSyncWorker != nil && components.gitSyncScheduler != nil && components.gitSyncCapability.code == ""
}

func newCaptureWorkflowExecutor(
	captures captureapplication.Repository,
	runtime captureapplication.ProcessingRepository,
	content captureapplication.ManagedContentWriter,
	fetcher captureapplication.URLFetcher,
	refresher captureworkflow.SourceRefresher,
	profiles captureworkflow.ProfileGenerator,
	ids foundation.IDGenerator,
	clock foundation.Clock,
) (*captureworkflow.Executor, error) {
	return captureworkflow.NewExecutor(captureworkflow.ExecutorDependencies{
		Captures: captures, Runtime: runtime, Content: content, Fetcher: fetcher,
		Refresher: refresher, Profiles: profiles, IDs: ids, Clock: clock,
	})
}

func newCaptureProfileRuntimeCatalog(contract platformmodels.ChatContract) (*agentapplication.RuntimeCatalog, error) {
	if contract.Model.Validate() != nil || contract.Timeout <= 0 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, captureprofile.ErrorCodeCapabilityUnavailable, false, errors.New("capture profile chat contract is invalid"))
	}
	catalog := agentapplication.NewRuntimeCatalog()
	if err := captureprofile.RegisterRuntimeCatalog(catalog); err != nil {
		return nil, err
	}
	if err := catalog.RegisterProfile(agentapplication.ModelProfile{
		Ref: agentworkflow.DefaultProfileRef(), Model: contract.Model,
		Timeout: contract.Timeout, MaxOutputTokens: agentStructuredMaxOutputTokens,
	}); err != nil {
		return nil, err
	}
	if err := catalog.Freeze(); err != nil {
		return nil, err
	}
	return catalog, nil
}

// agentWorkflowReadiness 保证 Chat capability 要么显式关闭，要么 Relation 与 RAG 都在冻结 Registry 可达。
func agentWorkflowReadiness(components workerComponents) bool {
	if !components.agentCapability.available {
		return components.agentCapability.code == agentworkflow.ErrorCodeCapabilityUnavailable
	}
	if components.agentCapability.code != "" || components.executors == nil || components.definitions == nil {
		return false
	}
	if _, err := components.executors.Resolve(agentworkflow.RelationAssessmentNodeKind, agentworkflow.RelationAssessmentInputSchemaVersion); err != nil {
		return false
	}
	if _, err := components.executors.Resolve(agentworkflow.RAGWorkflowNodeKind, agentworkflow.RAGWorkflowInputSchemaVersion); err != nil {
		return false
	}
	if _, err := components.definitions.Resolve(agentworkflow.RelationAssessmentDefinitionKey, agentworkflow.RelationAssessmentDefinitionVersion); err != nil {
		return false
	}
	for _, definition := range []workflowdomain.RegisteredDefinition{
		agentworkflow.RegisteredRAGDefinitionV1(),
		agentworkflow.RegisteredRAGDefinitionV2(),
	} {
		if _, err := components.definitions.Resolve(definition.Key, definition.Version); err != nil {
			return false
		}
	}
	return true
}

// artifactWorkflowReadiness 保证 Generation 终态收敛始终存在，且 Chat 启用时 Executor 与 Definition 成对可达。
func artifactWorkflowReadiness(components workerComponents) bool {
	artifact := components.artifact
	if artifact.generation == nil || artifact.terminal == nil || artifact.citationVerifier == nil {
		return false
	}
	if !artifact.capability.available {
		return artifact.capability.code == artifactworkflow.ErrorCodeCapabilityUnavailable && artifact.executor == nil && artifact.catalog == nil
	}
	if artifact.capability.code != "" || artifact.executor == nil || artifact.catalog == nil || components.executors == nil || components.definitions == nil {
		return false
	}
	if _, err := components.executors.Resolve(artifactworkflow.NodeKind, artifactworkflow.InputSchemaVersion); err != nil {
		return false
	}
	_, err := components.definitions.Resolve(artifactworkflow.DefinitionKey, artifactworkflow.DefinitionVersion)
	return err == nil
}

type agentWorkflowComponents struct {
	relation            *agentworkflow.Executor
	rag                 *agentworkflow.RAGWorkflowExecutor
	model               agentapplication.ChatModel
	contract            platformmodels.ChatContract
	relationScheduler   agentapplication.StructuredPhaseScheduler
	ragScheduler        agentapplication.StructuredPhaseScheduler
	ragRuntimeScheduler agentapplication.RAGExecutionScheduler
	artifactScheduler   agentapplication.StructuredPhaseScheduler
	captureScheduler    agentapplication.StructuredPhaseScheduler
	organizingScheduler agentapplication.StructuredPhaseScheduler
	capability          agentCapabilityStatus
}

// newStructuredPhaseScheduler 在 Composition Root 编译一次 Eino 短 Graph。
func newStructuredPhaseScheduler() (agentapplication.StructuredPhaseScheduler, error) {
	return agenteino.NewStructuredPhaseScheduler(context.Background())
}

// newRAGExecutionScheduler 编译完整 Eino RAG Graph。
func newRAGExecutionScheduler(metrics ...observability.Metrics) (agentapplication.RAGExecutionScheduler, error) {
	var selected observability.Metrics
	if len(metrics) > 0 {
		selected = metrics[len(metrics)-1]
	}
	return agenteino.NewRAGExecutionSchedulerWithMetrics(context.Background(), selected)
}

// newAgentWorkflowComponents preserves the pre-v2 constructor used by focused
// composition tests. Production composition must pass the frozen Tool runtime
// and Metrics through newAgentWorkflowComponentsWithToolsAndMetrics.
func newAgentWorkflowComponents(db *pgxpool.Pool, cfg config.Config, workspaceRepository *workspacepostgres.Repository, memoryService *memoryapplication.Service, modelRuntimes ...*modelsettingsruntime.Models) (agentWorkflowComponents, error) {
	return newAgentWorkflowComponentsWithDependencies(db, cfg, workspaceRepository, memoryService, toolRuntimeComponents{}, false, nil, modelRuntimes...)
}

// newAgentWorkflowComponentsWithTools composes the formal v2 RAG runtime with
// the same frozen model runtime and the sole project Tool execution boundary.
func newAgentWorkflowComponentsWithTools(db *pgxpool.Pool, cfg config.Config, workspaceRepository *workspacepostgres.Repository, memoryService *memoryapplication.Service, tools toolRuntimeComponents, modelRuntimes ...*modelsettingsruntime.Models) (agentWorkflowComponents, error) {
	return newAgentWorkflowComponentsWithDependencies(db, cfg, workspaceRepository, memoryService, tools, true, nil, modelRuntimes...)
}

func newAgentWorkflowComponentsWithToolsAndMetrics(db *pgxpool.Pool, cfg config.Config, workspaceRepository *workspacepostgres.Repository, memoryService *memoryapplication.Service, tools toolRuntimeComponents, metrics observability.Metrics, modelRuntimes ...*modelsettingsruntime.Models) (agentWorkflowComponents, error) {
	return newAgentWorkflowComponentsWithDependencies(db, cfg, workspaceRepository, memoryService, tools, true, metrics, modelRuntimes...)
}

func newAgentWorkflowComponentsWithDependencies(db *pgxpool.Pool, cfg config.Config, workspaceRepository *workspacepostgres.Repository, memoryService *memoryapplication.Service, tools toolRuntimeComponents, requireV2Runtime bool, metrics observability.Metrics, modelRuntimes ...*modelsettingsruntime.Models) (agentWorkflowComponents, error) {
	models, err := modelRuntimeForComposition(cfg, modelRuntimes...)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	chat := models.Chat()
	if chat.State() != platformmodels.CapabilityConfigured {
		return agentWorkflowComponents{capability: agentCapabilityStatus{code: agentworkflow.ErrorCodeCapabilityUnavailable}}, nil
	}
	model := chat.Model()
	contract, ok := chat.Contract()
	if !ok {
		return agentWorkflowComponents{}, foundation.NewError(foundation.ErrorConsistencyViolation, "AGENT_CHAT_CONTRACT_UNAVAILABLE", false, errors.New("configured chat model does not expose its frozen contract"))
	}
	var agentRuntime agentapplication.AgentRuntime
	var answerStream agentapplication.AnswerStreamRuntime
	if requireV2Runtime {
		if err := validateRAGWorkerJobTimeout(cfg.WorkerJobTimeout, contract.Timeout); err != nil {
			return agentWorkflowComponents{}, err
		}
		runtimeChat := models.RuntimeChat()
		if runtimeChat.State() != platformmodels.CapabilityConfigured || runtimeChat.Model() == nil {
			return agentWorkflowComponents{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_RAG_EINO_RUNTIME_UNAVAILABLE", false, errors.New("configured Eino Agent/Stream runtime is unavailable"))
		}
		if !tools.runtimeEnabled || tools.contracts == nil || tools.execution == nil {
			return agentWorkflowComponents{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_RAG_EINO_TOOL_RUNTIME_UNAVAILABLE", false, errors.New("configured Eino RAG Tool runtime is unavailable"))
		}
		agentRuntime, err = agenteino.NewAgentRuntimeWithMetrics(runtimeChat.Model(), metrics)
		if err != nil {
			return agentWorkflowComponents{}, err
		}
		answerStream, err = agenteino.NewAnswerStreamRuntimeWithMetrics(runtimeChat.Model(), metrics)
		if err != nil {
			return agentWorkflowComponents{}, err
		}
	}
	catalog, err := agentworkflow.NewRuntimeCatalog(agentworkflow.CatalogOptions{
		Model: contract.Model, Timeout: contract.Timeout, MaxOutputTokens: agentStructuredMaxOutputTokens,
	})
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	relationScheduler, err := newStructuredPhaseScheduler()
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	ragScheduler, err := newStructuredPhaseScheduler()
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	ragRuntimeScheduler, err := newRAGExecutionScheduler(metrics)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	artifactScheduler, err := newStructuredPhaseScheduler()
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	captureScheduler, err := newStructuredPhaseScheduler()
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	organizingScheduler, err := newStructuredPhaseScheduler()
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	repository, err := agentpostgres.NewRepository(db)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	knowledgeRepository, err := knowledgepostgres.NewRepository(db)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	eligibility, err := knowledgeapplication.NewEvidenceEligibilityService(knowledgeRepository)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	formalClaims, err := knowledgeapplication.NewFormalClaimReader(knowledgeRepository)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	knowledgePort, err := agentknowledge.NewAdapter(eligibility, formalClaims)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	searchRepository, err := retrievalpostgres.NewSearchRepository(db)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	artifactReader, err := retrievalworkspace.NewReader(workspaceRepository, filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}})
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	evidenceReference, err := retrievalapplication.NewEvidenceReferenceService(searchRepository, artifactReader)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	evidenceOpener, err := agentworkflow.NewReferenceOpener(evidenceReference)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	relation, err := agentworkflow.NewExecutor(agentworkflow.ExecutorDependencies{
		Model: model, Scheduler: relationScheduler, Catalog: catalog, Repository: repository, Knowledge: knowledgePort, Evidence: evidenceOpener,
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}, Budget: agentApplicationBudget(contract),
	})
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	eventStore, err := eventspostgres.NewStore(db)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	conversationRepository, err := conversationpostgres.NewRepository(db, eventStore)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	finalizer, err := conversationpostgres.NewAnswerFinalizer(db, repository, eventStore, foundation.SystemClock{})
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	draftStreams, err := conversationpostgres.NewDraftStreamRepository(db)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	progress, err := agentpostgres.NewRAGProgressStore(db, eventStore)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	searchService, err := retrievalapplication.NewSearchService(searchRepository, models.Embedding().Embedder(), nil)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	retrievalAdapter, err := agentretrieval.NewAdapter(searchService, evidenceReference)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	topicService, err := knowledgeapplication.NewEvidenceTopicService(knowledgeRepository)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	topicAdapter, err := agentknowledge.NewTopicAdapter(topicService)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	memoryOwner := memorydomain.SingleUserOwner()
	memoryLoader, err := agentmemory.NewLoader(memoryService, memoryOwner)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	rag, err := agentworkflow.NewRAGWorkflowExecutor(agentworkflow.RAGWorkflowExecutorDependencies{
		Model: model, Scheduler: ragScheduler, RAGScheduler: ragRuntimeScheduler, Catalog: catalog, Repository: repository, Snapshots: repository,
		Memory: memoryLoader, MemoryOwner: agentapplication.MemoryOwnerRef{Kind: string(memoryOwner.Kind), ID: memoryOwner.ID},
		Context: conversationRepository,
		Search:  retrievalAdapter, Retrieval: retrievalAdapter, Eligibility: knowledgePort, Topics: topicAdapter,
		Finalizer: finalizer, Progress: progress, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
		Budget: agentApplicationBudget(contract), Metrics: metrics,
		AgentRuntime: agentRuntime, AnswerStream: answerStream, ToolContracts: tools.contracts, ToolExecution: tools.execution,
		DraftStreams: draftStreams,
	})
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	return agentWorkflowComponents{
		relation: relation, rag: rag, model: model, contract: contract,
		relationScheduler: relationScheduler, ragScheduler: ragScheduler, ragRuntimeScheduler: ragRuntimeScheduler,
		artifactScheduler: artifactScheduler, captureScheduler: captureScheduler, organizingScheduler: organizingScheduler,
		capability: agentCapabilityStatus{available: true},
	}, nil
}

type artifactWorkflowComponents struct {
	generation       *artifactpostgres.SectionGenerationRepository
	terminal         *artifactpostgres.SectionGenerationTerminalHook
	citationVerifier *artifactapplication.ServerCitationVerifier
	executor         *artifactworkflow.Executor
	catalog          *agentapplication.RuntimeCatalog
	capability       agentCapabilityStatus
}

func newCaptureProfileGenerator(
	db *pgxpool.Pool,
	model agentapplication.ChatModel,
	contract platformmodels.ChatContract,
	modelRuns *agentpostgres.Repository,
	scheduler agentapplication.StructuredPhaseScheduler,
	ids foundation.IDGenerator,
	clock foundation.Clock,
) (captureworkflow.ProfileGenerator, agentCapabilityStatus, error) {
	unavailable := agentCapabilityStatus{code: captureprofile.ErrorCodeCapabilityUnavailable}
	if db == nil || modelRuns == nil || ids == nil || clock == nil {
		return nil, unavailable, foundation.NewError(foundation.ErrorDependencyUnavailable, captureprofile.ErrorCodeCapabilityUnavailable, false, errors.New("configured capture profile dependencies are unavailable"))
	}
	profileRepository, err := capturepostgres.NewProfileRepository(db, modelRuns)
	if err != nil {
		return nil, unavailable, err
	}
	if model == nil {
		generator, generatorErr := captureprofile.NewUnavailableGenerator(profileRepository, ids, clock)
		if generatorErr != nil {
			return nil, unavailable, generatorErr
		}
		return generator, unavailable, nil
	}
	if contract.Model.Validate() != nil || contract.Timeout <= 0 {
		return nil, unavailable, foundation.NewError(foundation.ErrorDependencyUnavailable, captureprofile.ErrorCodeCapabilityUnavailable, false, errors.New("configured capture profile model contract is unavailable"))
	}
	catalog, err := newCaptureProfileRuntimeCatalog(contract)
	if err != nil {
		return nil, unavailable, err
	}
	profileRef := agentworkflow.DefaultProfileRef()
	generator, err := captureprofile.NewGenerator(captureprofile.GeneratorDependencies{
		Repository: profileRepository, ModelRuns: modelRuns, Model: model, Scheduler: scheduler, Catalog: catalog,
		ModelProfileRef: profileRef, Budget: agentApplicationBudget(contract), IDs: ids, Clock: clock,
	})
	if err != nil {
		return nil, unavailable, err
	}
	return generator, agentCapabilityStatus{available: true}, nil
}

// newArtifactGenerationAgent 先组装 Agent 事务端口与独立 terminal hook，解除 Runtime 构造依赖环。
func newArtifactGenerationAgent(
	db *pgxpool.Pool,
) (*agentpostgres.Repository, *artifactpostgres.SectionGenerationTerminalHook, error) {
	if db == nil {
		return nil, nil, errors.New("artifact generation database is unavailable")
	}
	agentRepository, err := agentpostgres.NewRepository(db)
	if err != nil {
		return nil, nil, err
	}
	terminal, err := artifactpostgres.NewSectionGenerationTerminalHook(agentRepository, agentworkflow.DefaultProfileRef())
	if err != nil {
		return nil, nil, err
	}
	return agentRepository, terminal, nil
}

// newArtifactWorkflowComponents 组装始终存在的 Generation 终态事实，并仅在 Chat 启用时创建真实 Executor。
func newArtifactWorkflowComponents(
	db *pgxpool.Pool,
	workspaceRepository *workspacepostgres.Repository,
	runtime artifactpostgres.RuntimeStarterTx,
	agentRepository *agentpostgres.Repository,
	terminal *artifactpostgres.SectionGenerationTerminalHook,
	model agentapplication.ChatModel,
	contract platformmodels.ChatContract,
	scheduler agentapplication.StructuredPhaseScheduler,
	ids foundation.IDGenerator,
	clock foundation.Clock,
	embedders ...retrievalapplication.Embedder,
) (artifactWorkflowComponents, error) {
	if db == nil || workspaceRepository == nil || runtime == nil || agentRepository == nil || terminal == nil || ids == nil || clock == nil {
		return artifactWorkflowComponents{}, foundation.NewError(foundation.ErrorDependencyUnavailable, artifactworkflow.ErrorCodeCapabilityUnavailable, false, errors.New("artifact generation persistence dependencies are incomplete"))
	}
	knowledgeRepository, err := knowledgepostgres.NewRepository(db)
	if err != nil {
		return artifactWorkflowComponents{}, err
	}
	eligibility, err := knowledgeapplication.NewEvidenceEligibilityService(knowledgeRepository)
	if err != nil {
		return artifactWorkflowComponents{}, err
	}
	searchRepository, err := retrievalpostgres.NewSearchRepository(db)
	if err != nil {
		return artifactWorkflowComponents{}, err
	}
	artifactReader, err := retrievalworkspace.NewReader(workspaceRepository, filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}})
	if err != nil {
		return artifactWorkflowComponents{}, err
	}
	evidenceReference, err := retrievalapplication.NewEvidenceReferenceService(searchRepository, artifactReader)
	if err != nil {
		return artifactWorkflowComponents{}, err
	}
	citationVerifier, err := artifactapplication.NewServerCitationVerifier(evidenceReference, eligibility)
	if err != nil {
		return artifactWorkflowComponents{}, err
	}
	generation, err := artifactpostgres.NewSectionGenerationRepository(
		db, runtime, agentRepository, citationVerifier, ids, clock, agentworkflow.DefaultProfileRef(),
	)
	if err != nil {
		return artifactWorkflowComponents{}, err
	}
	components := artifactWorkflowComponents{
		generation: generation, terminal: terminal, citationVerifier: citationVerifier,
		capability: agentCapabilityStatus{code: artifactworkflow.ErrorCodeCapabilityUnavailable},
	}
	if model == nil {
		return components, nil
	}
	if model == nil || contract.Model.Validate() != nil || contract.Timeout <= 0 {
		return artifactWorkflowComponents{}, foundation.NewError(foundation.ErrorDependencyUnavailable, artifactworkflow.ErrorCodeCapabilityUnavailable, false, errors.New("configured artifact chat model contract is unavailable"))
	}
	catalog, err := newArtifactRuntimeCatalog(contract)
	if err != nil {
		return artifactWorkflowComponents{}, err
	}
	if len(embedders) > 1 {
		return artifactWorkflowComponents{}, errors.New("artifact composition accepts at most one shared embedder")
	}
	var embedder retrievalapplication.Embedder
	if len(embedders) == 1 {
		embedder = embedders[0]
	}
	searchService, err := retrievalapplication.NewSearchService(searchRepository, embedder, nil)
	if err != nil {
		return artifactWorkflowComponents{}, err
	}
	retrievalAdapter, err := agentretrieval.NewAdapter(searchService, evidenceReference)
	if err != nil {
		return artifactWorkflowComponents{}, err
	}
	executor, err := artifactworkflow.NewExecutor(artifactworkflow.ExecutorDependencies{
		Model: model, Scheduler: scheduler, Catalog: catalog, Repository: agentRepository, Context: generation,
		Retrieval: retrievalAdapter, Eligibility: eligibility, Finalizer: generation,
		IDs: ids, Clock: clock, Budget: agentApplicationBudget(contract),
	})
	if err != nil {
		return artifactWorkflowComponents{}, err
	}
	components.executor = executor
	components.catalog = catalog
	components.capability = agentCapabilityStatus{available: true}
	return components, nil
}

// newArtifactRuntimeCatalog 使用真实 Chat contract 冻结 Artifact 独立 Prompt、Schema 与共享 Profile 引用。
func newArtifactRuntimeCatalog(contract platformmodels.ChatContract) (*agentapplication.RuntimeCatalog, error) {
	if contract.Model.Validate() != nil || contract.Timeout <= 0 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, artifactworkflow.ErrorCodeCapabilityUnavailable, false, errors.New("artifact chat contract is invalid"))
	}
	catalog := agentapplication.NewRuntimeCatalog()
	if err := artifactworkflow.RegisterRuntimeCatalog(catalog); err != nil {
		return nil, err
	}
	if err := catalog.RegisterProfile(agentapplication.ModelProfile{
		Ref:             agentworkflow.DefaultProfileRef(),
		Model:           contract.Model,
		Timeout:         contract.Timeout,
		MaxOutputTokens: agentStructuredMaxOutputTokens,
	}); err != nil {
		return nil, err
	}
	if err := catalog.Freeze(); err != nil {
		return nil, err
	}
	return catalog, nil
}

// newOrganizingRuntimeCatalog 使用共享 Chat contract 冻结 Organizing 独立 Prompt、Schema 与 Profile。
func newOrganizingRuntimeCatalog(contract platformmodels.ChatContract) (*agentapplication.RuntimeCatalog, error) {
	if contract.Model.Validate() != nil || contract.Timeout <= 0 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "ORGANIZING_GENERATION_CAPABILITY_UNAVAILABLE", false, errors.New("organizing chat contract is invalid"))
	}
	catalog := agentapplication.NewRuntimeCatalog()
	if err := organizingworkflow.RegisterGenerationRuntimeCatalog(catalog); err != nil {
		return nil, err
	}
	if err := catalog.RegisterProfile(agentapplication.ModelProfile{
		Ref: agentworkflow.DefaultProfileRef(), Model: contract.Model,
		Timeout: contract.Timeout, MaxOutputTokens: agentStructuredMaxOutputTokens,
	}); err != nil {
		return nil, err
	}
	if err := catalog.Freeze(); err != nil {
		return nil, err
	}
	return catalog, nil
}

func agentApplicationBudget(contract platformmodels.ChatContract) agentapplication.RunBudget {
	budget := agentapplication.DefaultRunBudget()
	budget.MaxRequestBytes = min(contract.MaxRequestBytes*agentapplication.StructuredCallLimit, agentapplication.MaxRunRequestBytes)
	budget.MaxResponseBytes = min(contract.MaxResponseBytes*agentapplication.StructuredCallLimit, agentapplication.MaxRunResponseBytes)
	budget.Timeout = min(contract.Timeout*time.Duration(agentapplication.StructuredCallLimit), agentapplication.MaxRunTimeout)
	return budget
}

func validateRAGWorkerJobTimeout(jobTimeout, modelCallTimeout time.Duration) error {
	attemptTimeout := agentworkflow.RAGAttemptTimeout(modelCallTimeout)
	minimumJobTimeout := attemptTimeout + ragWorkerJobTimeoutHeadroom
	if attemptTimeout <= 0 || jobTimeout < minimumJobTimeout {
		return foundation.NewError(
			foundation.ErrorInvalidInput,
			"WORKER_RAG_EINO_TIMEOUT_BUDGET_INVALID",
			false,
			errors.New("worker job timeout does not cover the complete Eino RAG attempt and bounded non-model work"),
		)
	}
	return nil
}

type reindexComponents struct {
	worker     *reindexriver.Worker
	runtime    *workerReindexProcessorAcquirer
	dispatcher *retrievalruntime.Runner
}

type sourceProcessingComponents struct {
	workspace        *workspaceapplication.Service
	ingestion        *ingestionapplication.Service
	retrieval        *retrievalapplication.Service
	store            *retrievalpostgres.Repository
	vectors          retrievalapplication.ProcessorVectorPort
	regression       *retrievalapplication.RegressionService
	processorOptions retrievalapplication.ProcessorOptions
	refresher        retrievalapplication.SourceRefreshOperationRunner
}

func newSourceProcessingComponents(db *pgxpool.Pool, cfg config.Config, workspaceRepository *workspacepostgres.Repository, committedGit *gitcli.WritebackClient, modelSettingsRevision *int64, modelRuntimes ...*modelsettingsruntime.Models) (sourceProcessingComponents, error) {
	models, err := modelRuntimeForComposition(cfg, modelRuntimes...)
	if err != nil {
		return sourceProcessingComponents{}, err
	}
	ids := foundation.NewUUIDGenerator(nil)
	clock := foundation.SystemClock{}
	files := filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}}
	workspaceService := workspaceapplication.NewService(workspaceapplication.Dependencies{
		Repository: workspaceRepository, ManagedFiles: files, CommittedFiles: files, CommittedGit: committedGit,
		IDs: ids, Clock: clock,
	})
	ingestionRepository, err := ingestionpostgres.NewRepository(db)
	if err != nil {
		return sourceProcessingComponents{}, err
	}
	sourceReader, err := ingestionworkspace.NewReader(workspaceRepository, files)
	if err != nil {
		return sourceProcessingComponents{}, err
	}
	ingestionService, err := ingestionapplication.NewService(ingestionapplication.Dependencies{
		Repository: ingestionRepository, Sources: sourceReader,
		Parsers: platformparser.NewRegistry(platformparser.Options{MaxBytes: filesystem.DefaultMaxBytes}),
		IDs:     ids, Clock: clock,
		ContentPolicy: ingestionapplication.ContentPolicy{MaxBytes: ingestionapplication.DefaultContentMaxBytes},
		ChunkOptions: ingestiondomain.ChunkOptions{
			StrategyVersion: "structure-v1", SchemaVersion: platformparser.ParseSchemaVersion,
			SoftMaxBytes: ingestiondomain.DefaultChunkSoftMaxBytes,
		},
	})
	if err != nil {
		return sourceProcessingComponents{}, err
	}
	retrievalRepository, err := retrievalpostgres.NewRepository(db)
	if err != nil {
		return sourceProcessingComponents{}, err
	}
	retrievalService, err := retrievalapplication.NewService(retrievalapplication.Dependencies{Store: retrievalRepository, IDs: ids, Clock: clock})
	if err != nil {
		return sourceProcessingComponents{}, err
	}
	regressionService, err := retrievalapplication.NewRegressionService(retrievalRepository)
	if err != nil {
		return sourceProcessingComponents{}, err
	}
	processorOptions := retrievalapplication.DefaultFTSOnlyProcessorOptions(cfg.ReindexDispatchErrorBackoff)
	vectorBuilder, err := retrievalapplication.NewVectorRecoveryBuilder(retrievalRepository, clock)
	if err != nil {
		return sourceProcessingComponents{}, err
	}
	embedder := models.Embedding().Embedder()
	if embedder != nil {
		contract := embedder.Contract()
		registrationContext, cancelRegistration := context.WithTimeout(context.Background(), cfg.DatabasePingTimeout)
		registered, registrationErr := retrievalService.RegisterEmbedding(registrationContext, retrievalapplication.RegisterEmbeddingRequest{
			Provider: contract.Provider, AdapterName: contract.AdapterName, AdapterVersion: contract.AdapterVersion,
			Model: contract.Model, Dimensions: contract.Dimensions, Normalization: contract.Normalization,
			DistanceMetric: contract.DistanceMetric, ConfigHash: contract.ConfigHash,
			ModelSettingsRevision: modelSettingsRevision,
		})
		cancelRegistration()
		if registrationErr != nil {
			return sourceProcessingComponents{}, registrationErr
		}
		builder, builderErr := retrievalapplication.NewVectorBuilder(retrievalapplication.VectorBuilderDependencies{
			Store: retrievalRepository, Embedder: embedder, Clock: clock,
		})
		if builderErr != nil {
			return sourceProcessingComponents{}, builderErr
		}
		fusion, fusionErr := configuredRRF(cfg)
		if fusionErr != nil {
			return sourceProcessingComponents{}, fusionErr
		}
		embeddingVersionID := registered.EmbeddingVersion.ID
		processorOptions.EmbeddingVersionID = &embeddingVersionID
		processorOptions.FusionConfig = fusion
		vectorBuilder = builder
	}
	refresher, err := retrievalapplication.NewSourceRefresher(retrievalapplication.SourceRefresherDependencies{
		Ingestion: ingestionService, Retrieval: retrievalService, Locker: retrievalRepository, Vectors: vectorBuilder,
	}, retrievalapplication.SourceRefresherOptions{
		EmbeddingVersionID: processorOptions.EmbeddingVersionID,
		TokenizerID:        processorOptions.TokenizerID, TokenizerVersion: processorOptions.TokenizerVersion,
		TokenizerConfigHash: processorOptions.TokenizerConfigHash, FusionConfig: processorOptions.FusionConfig,
		PageSize: processorOptions.PageSize, MaxSources: processorOptions.MaxSources, MaxChunks: processorOptions.MaxChunks,
	})
	if err != nil {
		return sourceProcessingComponents{}, err
	}
	return sourceProcessingComponents{
		workspace: workspaceService, ingestion: ingestionService, retrieval: retrievalService,
		store: retrievalRepository, vectors: vectorBuilder, regression: regressionService,
		processorOptions: processorOptions, refresher: refresher,
	}, nil
}

// newGitSyncWorker composes the optional remote-sync capability. A missing key
// intentionally leaves it unavailable: no credential store or remote client is
// created, while unrelated Worker capabilities remain runnable.
func newGitSyncWorker(
	db *pgxpool.Pool,
	cfg config.Config,
	workspaceRepository *workspacepostgres.Repository,
	committedGit *gitcli.WritebackClient,
	sources sourceProcessingComponents,
	locker gitoperation.WorkspaceLocker,
	workerID foundation.ID,
) (*gitsyncapplication.Worker, *gitsyncapplication.AutoSyncScheduler, error) {
	if cfg.GitSyncKeyFile == "" {
		return nil, nil, nil
	}
	sealer, err := gitsyncsecurity.NewCredentialSealerFromFile(cfg.GitSyncKeyFile)
	if err != nil {
		return nil, nil, err
	}
	if db == nil || workspaceRepository == nil || committedGit == nil || sources.workspace == nil ||
		sources.refresher == nil || locker == nil || workerID == "" {
		return nil, nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_GIT_SYNC_DEPENDENCIES_UNAVAILABLE", false, errors.New("Git sync worker dependencies are unavailable"))
	}
	repository, err := gitsyncpostgres.NewRepository(db, sealer)
	if err != nil {
		return nil, nil, err
	}
	policy := gitsyncsecurity.NewURLPolicy(net.DefaultResolver)
	remote, err := gitcli.NewRemoteClient(gitcli.New(""), workspaceRepository, policy)
	if err != nil {
		return nil, nil, err
	}
	ids := foundation.NewUUIDGenerator(nil)
	clock := foundation.SystemClock{}
	cursors, err := gitsyncapplication.NewRandomCursorCodec()
	if err != nil {
		return nil, nil, err
	}
	service, err := gitsyncapplication.NewService(repository, repository, policy, remote, ids, clock, cursors)
	if err != nil {
		return nil, nil, err
	}
	scheduler, err := gitsyncapplication.NewAutoSyncScheduler(repository, service)
	if err != nil {
		return nil, nil, err
	}
	runs, err := gitsyncapplication.NewRunExecutor(
		repository, repository, remote, locker, ids, 0,
	)
	if err != nil {
		return nil, nil, err
	}
	capture, err := gitsourcecapture.New(gitsourcecapture.Dependencies{
		Trees: committedGit, Blobs: committedGit, Sources: sources.workspace,
		Repository: workspaceRepository, Refresher: sources.refresher, Clock: clock,
	})
	if err != nil {
		return nil, nil, err
	}
	followup, err := gitsyncapplication.NewFollowupExecutor(repository, capture)
	if err != nil {
		return nil, nil, err
	}
	worker, err := gitsyncapplication.NewWorker(repository, runs, followup, fmt.Sprintf("git-sync-worker:%s", workerID), 0)
	if err != nil {
		return nil, nil, err
	}
	return worker, scheduler, nil
}

func newReindexComponents(db *pgxpool.Pool, cfg config.Config, processing sourceProcessingComponents, insertClient *riveradapter.Client, workerID foundation.ID, logger *slog.Logger, metrics observability.Metrics, fatalInvariants chan<- error, modelRuntimes ...*modelsettingsruntime.Models) (reindexComponents, error) {
	models, err := modelRuntimeForComposition(cfg, modelRuntimes...)
	if err != nil {
		return reindexComponents{}, err
	}
	if processing.workspace == nil || processing.ingestion == nil || processing.retrieval == nil || processing.store == nil ||
		processing.vectors == nil || processing.regression == nil || processing.refresher == nil ||
		(processing.processorOptions.EmbeddingVersionID != nil) != (models.Embedding().Embedder() != nil) {
		return reindexComponents{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKER_SOURCE_PROCESSING_UNAVAILABLE", false, errors.New("shared source processing components do not match the frozen model runtime"))
	}
	ids := foundation.NewUUIDGenerator(nil)
	deliveryRepository, err := retrievalpostgres.NewDeliveryRepository(db, ids)
	if err != nil {
		return reindexComponents{}, err
	}
	deliveryRuntime, err := retrievalapplication.NewDeliveryRuntime(deliveryRepository)
	if err != nil {
		return reindexComponents{}, err
	}
	processor, err := retrievalapplication.NewProcessor(retrievalapplication.ProcessorDependencies{
		Contexts: deliveryRepository, Capture: processing.workspace, Ingestion: processing.ingestion,
		Retrieval: processing.retrieval, Vectors: processing.vectors, Regression: processing.regression,
	}, processing.processorOptions)
	if err != nil {
		return reindexComponents{}, err
	}
	completion, err := retrievalapplication.NewCompletionService(processing.store, ids, cfg.ReindexDispatchErrorBackoff)
	if err != nil {
		return reindexComponents{}, err
	}
	workerOptions := reindexriver.WorkerOptions{
		Owner: fmt.Sprintf("reindex-worker:%s", workerID), LeaseDuration: cfg.ReindexLeaseDuration,
		HeartbeatInterval: cfg.ReindexHeartbeatInterval, Metrics: metrics, Logger: logger,
		FatalInvariants: fatalInvariants,
	}
	var worker *reindexriver.Worker
	var runtimeAcquirer *workerReindexProcessorAcquirer
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		runtimeAcquirer, err = newWorkerReindexProcessorAcquirer(processing.store, deliveryRepository, cfg.ReindexDispatchErrorBackoff)
		if err != nil {
			return reindexComponents{}, err
		}
		worker, err = reindexriver.NewWorkerWithProcessorAcquirer(deliveryRuntime, runtimeAcquirer, completion, workerOptions)
	} else {
		worker, err = reindexriver.NewWorker(deliveryRuntime, processor, completion, workerOptions)
	}
	if err != nil {
		return reindexComponents{}, err
	}
	inserter, err := reindexriver.NewInserter(insertClient)
	if err != nil {
		return reindexComponents{}, err
	}
	dispatcher, err := retrievalpostgres.NewDispatcher(db, ids, inserter)
	if err != nil {
		return reindexComponents{}, err
	}
	runner := retrievalruntime.NewRunner(dispatcher, retrievalruntime.Options{
		PollInterval: cfg.ReindexDispatchPollInterval, BatchSize: cfg.ReindexDispatchBatchSize,
		ErrorBackoff: cfg.ReindexDispatchErrorBackoff,
	})
	return reindexComponents{worker: worker, runtime: runtimeAcquirer, dispatcher: runner}, nil
}

func configuredRRF(cfg config.Config) (json.RawMessage, error) {
	return retrievaldomain.CanonicalRRFConfig(cfg.RetrievalRRFConfig())
}

func startWorkerHealthServer(address string, handler http.Handler) (workerHealthServer, error) {
	if handler == nil {
		return workerHealthServer{}, errors.New("worker health handler is nil")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return workerHealthServer{}, err
	}
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	errorsChannel := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errorsChannel <- err
		close(errorsChannel)
	}()
	return workerHealthServer{server: server, errors: errorsChannel, address: listener.Addr().String()}, nil
}

func recordShutdownMetric(metrics observability.Metrics, mode shutdownMode, result string) error {
	if metrics == nil {
		return errors.New("worker metrics are nil")
	}
	shutdownKind := "graceful"
	if mode == shutdownEmergency {
		shutdownKind = "forced"
	}
	labels, err := observability.NewLabels(map[string]string{"shutdown_kind": shutdownKind, "result": result})
	if err != nil {
		return err
	}
	measurement, err := observability.NewMeasurement(observability.MetricShutdownTotal, observability.MetricKindCounter, 1, labels)
	if err != nil {
		return err
	}
	return metrics.Record(context.Background(), measurement)
}

func recordQueueDepthMetric(ctx context.Context, metrics observability.Metrics, database riveradapter.QueueDepthQuerier, queue string) error {
	if metrics == nil {
		return errors.New("worker metrics are nil")
	}
	depth, err := riveradapter.QueueDepth(ctx, database, queue)
	if err != nil {
		return err
	}
	labels, err := observability.NewLabels(map[string]string{"queue": queue})
	if err != nil {
		return err
	}
	measurement, err := observability.NewMeasurement(observability.MetricQueueDepth, observability.MetricKindGauge, float64(depth), labels)
	if err != nil {
		return err
	}
	return metrics.Record(ctx, measurement)
}

func ping(database *postgres.Pool, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := database.Ping(ctx); err != nil {
		return err
	}
	return nil
}
