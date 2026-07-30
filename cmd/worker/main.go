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

	agentknowledge "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/knowledge"
	agentmemory "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/memory"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentretrieval "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/retrieval"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	artifactpostgres "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/postgres"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactworkflow "github.com/CodeZen-Lizhi/zhixu/internal/artifact/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
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
	memorypostgres "github.com/CodeZen-Lizhi/zhixu/internal/memory/adapter/postgres"
	memoryapplication "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
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
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// agentStructuredMaxOutputTokens 是 Agent 各结构化阶段单次响应的生产上限。
	agentStructuredMaxOutputTokens       = 8192
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

type workerComponents struct {
	safeWriteback    *changecontrolworkflow.Node
	tools            toolRuntimeComponents
	agentCapability  agentCapabilityStatus
	artifact         artifactWorkflowComponents
	reindexWorker    *reindexriver.Worker
	dispatcher       *retrievalruntime.Runner
	runtimeClient    *riveradapter.Client
	definitions      *workflowapplication.DefinitionRegistry
	executors        *workflowapplication.ExecutorRegistry
	semanticScan     *graphworkflow.SemanticLinkScanExecutor
	healthScan       *healthworkflowadapter.HealthScanExecutor
	healthScanStart  *healthapplication.ScanService
	healthSchedule   *healthapplication.ScheduleService
	healthAffected   *healthapplication.AffectedChangeDispatcher
	timelineProject  *knowledgeapplication.TimelineProjectionDispatcher
	citationBackfill *artifactapplication.CitationBackfillDispatcher
	exportWorker     *exportriver.Worker
	exportService    *exportapplication.Service
	memoryExpiry     memoryExpiryService
	// interviewCompletion 是 reservation/hidden hold 维护依赖。
	interviewCompletion interviewCompletionMaintenanceService
	// learningPathMaintenance 是 Review Path reservation/hidden hold 维护依赖。
	learningPathMaintenance learningPathMaintenanceService
	fatalInvariants         <-chan error
}

type toolRuntimeComponents struct {
	contracts      *toolsapplication.Registry
	executions     *toolsapplication.Registry
	execution      *toolsapplication.ExecutionService
	repository     *toolpostgres.Repository
	writebackAudit *toolchangecontrol.WritebackAuditRecorder
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
	telemetry, err := observability.InitializeTelemetry(context.Background(), observability.TelemetryOptions{
		Mode: observability.TelemetryMode(cfg.TelemetryMode), Endpoint: cfg.TelemetryEndpoint,
	})
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
	var managedModels modelsettingsruntime.BootstrapResult
	var modelEnqueueFences []riveradapter.EnqueueFence
	var configuredModels *modelsettingsruntime.Models
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		bootstrap, bootstrapErr := modelsettingsruntime.Bootstrap(context.Background(), database.DB(), cfg)
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
		loaded, modelsErr := modelsettingsruntime.LoadSettings(context.Background(), cfg, nil)
		if modelsErr != nil {
			return modelsErr
		}
		configuredModels = loaded.Models
	}
	if configuredModels == nil {
		fallback, fallbackErr := modelsettingsruntime.Build(cfg, modelsettingsdomain.ResolvedSettings{Settings: modelsettingsdomain.CanonicalDisabledSettings()})
		if fallbackErr != nil {
			return fallbackErr
		}
		configuredModels = fallback
	}
	modelBinding, err := newWorkerModelRuntimeBinding(cfg.ModelSettingsMode, configuredModels, foundation.NewUUIDGenerator(nil))
	if err != nil {
		return err
	}
	cfg = modelsettingsruntime.WithoutModelCredentials(cfg)
	components, err := newWorkerComponentsWithModels(database.DB(), cfg, configuredModels, modelBinding, logger, telemetry.Metrics(), modelEnqueueFences...)
	if err != nil {
		logger.Error("worker components are unavailable", "error_code", "WORKER_COMPONENTS_UNAVAILABLE")
		return err
	}

	readiness := workflowruntime.NewReadiness()
	readiness.SetDatabaseOK(true)
	readiness.SetRiverSchemaOK(true)
	readiness.SetDefinitionsOK(components.definitions != nil)
	readiness.SetExecutorsOK(components.executors != nil)
	readiness.SetDependenciesOK(components.safeWriteback != nil && components.reindexWorker != nil && components.dispatcher != nil && components.timelineProject != nil && components.citationBackfill != nil && components.exportWorker != nil && components.exportService != nil && components.memoryExpiry != nil && components.interviewCompletion != nil && components.learningPathMaintenance != nil && agentWorkflowReadiness(components) && artifactWorkflowReadiness(components))
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
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	modelRuntimeErr := make(chan error, 1)
	var modelController *modelsettingsruntime.Controller
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		if managedModels.Service == nil || modelBinding.instanceID == nil {
			_ = health.server.Close()
			return errors.New("managed model runtime registration is unavailable")
		}
		modelController, err = modelsettingsruntime.NewController(modelsettingsruntime.ControllerOptions{
			Service: managedModels.Service, Role: modelsettingsdomain.RuntimeRoleWorker, InstanceID: *modelBinding.instanceID, Loaded: managedModels.Loaded,
			Drain: modelDrain.Hooks(),
		})
		if err != nil {
			_ = health.server.Close()
			return err
		}
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
	if err := lifecycle.Start(processContext); err != nil {
		readiness.BeginShutdown()
		_ = health.server.Close()
		logger.Error("workflow runtime could not be started", "error_code", "WORKFLOW_RIVER_CLIENT_START_FAILED")
		return err
	}
	readiness.SetRiverStarted(true)
	readiness.SetReindexDispatcherStarted(components.dispatcher.Started())
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
	logger.Info("worker started", "version", cfg.Version, "safe_writeback_node", components.safeWriteback != nil,
		"semantic_link_scan", components.semanticScan != nil,
		"agent_available", components.agentCapability.available, "agent_capability_code", components.agentCapability.code,
		"artifact_generation_available", components.artifact.capability.available, "artifact_generation_capability_code", components.artifact.capability.code,
		"tool_runtime_enabled", components.tools.runtimeEnabled, "tool_executor_count", len(components.tools.enabledRefs),
		"web_fetch_enabled", cfg.WebFetchMode == config.ToolModeEnabled, "reindex_dispatcher", components.dispatcher.Started(),
		"timeline_projector", components.timelineProject != nil, "export_worker", components.exportWorker != nil,
		"artifact_citation_backfill", components.citationBackfill != nil,
		"interview_completion_maintenance", components.interviewCompletion != nil,
		"learning_path_maintenance", components.learningPathMaintenance != nil)

	ticker := time.NewTicker(cfg.HealthInterval)
	defer ticker.Stop()
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
			}
		}
	}

shutdown:
	readiness.BeginShutdown()
	readiness.SetReindexDispatcherStarted(false)
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), cfg.WorkerHardStopTimeout)
	defer cancelShutdown()
	if err := lifecycle.Shutdown(shutdownContext, shutdownMode); err != nil && runErr == nil {
		runErr = err
	}
	readiness.SetRiverStarted(false)
	if err := health.server.Shutdown(shutdownContext); err != nil && runErr == nil {
		runErr = err
	}
	if errors.Is(shutdownContext.Err(), context.DeadlineExceeded) {
		if err := recordShutdownMetric(telemetry.Metrics(), lifecycle.Mode(), "failure"); err != nil {
			logger.Warn("worker shutdown metric failed", "error_code", "WORKER_METRIC_RECORD_FAILED")
		}
		logger.Error("worker hard shutdown deadline exceeded", "error_code", "WORKER_HARD_SHUTDOWN_TIMEOUT")
		return context.DeadlineExceeded
	}
	result := "success"
	if runErr != nil {
		result = "failure"
	}
	if err := recordShutdownMetric(telemetry.Metrics(), lifecycle.Mode(), result); err != nil {
		logger.Warn("worker shutdown metric failed", "error_code", "WORKER_METRIC_RECORD_FAILED")
	}
	if err := telemetry.Shutdown(shutdownContext); err != nil && runErr == nil {
		runErr = err
	}
	return runErr
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

func newWorkerComponents(db *pgxpool.Pool, cfg config.Config, logger *slog.Logger, metrics observability.Metrics, enqueueFences ...riveradapter.EnqueueFence) (workerComponents, error) {
	models, err := modelRuntimeForComposition(cfg)
	if err != nil {
		return workerComponents{}, err
	}
	return newWorkerComponentsWithModels(db, cfg, models, workerModelRuntimeBinding{}, logger, metrics, enqueueFences...)
}

type workerModelRuntimeBinding struct {
	revision   *int64
	instanceID *foundation.ID
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
	return workerModelRuntimeBinding{revision: &revision, instanceID: &instanceID}, nil
}

func (binding workerModelRuntimeBinding) runtimeWorkerOptions() []riveradapter.RuntimeWorkerOptions {
	if binding.revision == nil && binding.instanceID == nil {
		return nil
	}
	return []riveradapter.RuntimeWorkerOptions{{ModelSettingsRevision: binding.revision, ModelRuntimeInstanceID: binding.instanceID}}
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

func newWorkerComponentsWithModels(db *pgxpool.Pool, cfg config.Config, models *modelsettingsruntime.Models, modelBinding workerModelRuntimeBinding, logger *slog.Logger, metrics observability.Metrics, enqueueFences ...riveradapter.EnqueueFence) (workerComponents, error) {
	if db == nil {
		return workerComponents{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_DATABASE_UNAVAILABLE", true, errors.New("database pool is nil"))
	}
	if models == nil {
		return workerComponents{}, foundation.NewError(foundation.ErrorDependencyUnavailable, modelsettingsdomain.ErrorCodeUnavailable, false, errors.New("model runtime is nil"))
	}
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		if modelBinding.revision == nil || modelBinding.instanceID == nil || *modelBinding.revision != models.Revision() {
			return workerComponents{}, foundation.NewError(foundation.ErrorConsistencyViolation, modelsettingsdomain.ErrorCodeRuntimeConflict, false, errors.New("managed worker model runtime binding does not match the frozen runtime"))
		}
	} else if modelBinding.revision != nil || modelBinding.instanceID != nil {
		return workerComponents{}, foundation.NewError(foundation.ErrorConsistencyViolation, modelsettingsdomain.ErrorCodeRuntimeConflict, false, errors.New("static worker must not bind a managed model runtime"))
	}
	workspaceRepository, err := workspacepostgres.NewRepository(db)
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
	service, err := changecontrolapplication.NewWritebackService(changecontrolapplication.WritebackServiceDependencies{
		Repository: writebackRepository, Workspace: workspaceStore, Git: gitRepository, Audit: toolComponents.writebackAudit,
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
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
	runtimeRepository, err := workflowpostgres.NewRuntimeRepositoryWithHooks(db, inserter, workflowpostgres.RuntimeRepositoryHooks{
		CancellationSafety: cancellationGuard,
		Terminal:           artifactTerminal,
	})
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
	agentComponents, err := newAgentWorkflowComponents(db, cfg, workspaceRepository, memoryService, models)
	if err != nil {
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
		agentComponents.model, agentComponents.contract, artifactIDs, artifactClock, models.Embedding().Embedder(),
	)
	if err != nil {
		return workerComponents{}, err
	}
	if artifactComponents.executor != nil {
		if err := executors.Register(artifactworkflow.NodeKind, artifactworkflow.InputSchemaVersion, artifactComponents.executor); err != nil {
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
	if agentComponents.relation != nil {
		if err := definitions.Register(agentworkflow.RegisteredDefinition()); err != nil {
			return workerComponents{}, err
		}
		if err := definitions.Register(agentworkflow.RegisteredRAGDefinition()); err != nil {
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
	if err := definitions.Freeze(); err != nil {
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
	artifactRepository, err := artifactpostgres.NewRepository(db)
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
	workerID, err := foundation.NewUUIDGenerator(nil).New()
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
		Metrics: metrics, Queue: cfg.WorkerQueue, Logger: logger, FatalInvariants: fatalInvariants,
	}, modelBinding.runtimeWorkerOptions()...)
	if err != nil {
		return workerComponents{}, err
	}
	reindex, err := newReindexComponents(
		db, cfg, workspaceRepository, gitRepository, insertClient, workerID, logger, metrics, fatalInvariants,
		modelBinding.revision, models,
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
		reindexWorker: reindex.worker, dispatcher: reindex.dispatcher,
		runtimeClient: runtimeClient, definitions: definitions, executors: executors, semanticScan: semanticScan, healthScan: healthScan, healthScanStart: healthScanStartService, healthSchedule: healthSchedule, healthAffected: healthAffected, timelineProject: timelineProject, citationBackfill: citationBackfill,
		exportWorker: exportWorker, exportService: exportService, memoryExpiry: memoryService,
		interviewCompletion: interviewCompletion, learningPathMaintenance: learningPathMaintenance,
		fatalInvariants: fatalInvariants,
	}, nil
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
	workflowDefinition, err := toolworkflow.NewProductionRegisteredDefinition(contracts)
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
	_, err := components.definitions.Resolve(agentworkflow.RAGWorkflowDefinitionKey, agentworkflow.RAGWorkflowDefinitionVersion)
	return err == nil
}

// artifactWorkflowReadiness 保证 Generation 终态收敛始终存在，且 Chat 启用时 Executor 与 Definition 成对可达。
func artifactWorkflowReadiness(components workerComponents) bool {
	artifact := components.artifact
	if artifact.generation == nil || artifact.terminal == nil {
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
	relation   *agentworkflow.Executor
	rag        *agentworkflow.RAGWorkflowExecutor
	model      agentapplication.ChatModel
	contract   platformmodels.ChatContract
	capability agentCapabilityStatus
}

func newAgentWorkflowComponents(db *pgxpool.Pool, cfg config.Config, workspaceRepository *workspacepostgres.Repository, memoryService *memoryapplication.Service, modelRuntimes ...*modelsettingsruntime.Models) (agentWorkflowComponents, error) {
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
	catalog, err := agentworkflow.NewRuntimeCatalog(agentworkflow.CatalogOptions{
		Model: contract.Model, Timeout: contract.Timeout, MaxOutputTokens: agentStructuredMaxOutputTokens,
	})
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
		Model: model, Catalog: catalog, Repository: repository, Knowledge: knowledgePort, Evidence: evidenceOpener,
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
		Model: model, Catalog: catalog, Repository: repository, Snapshots: repository,
		Memory: memoryLoader, MemoryOwner: agentapplication.MemoryOwnerRef{Kind: string(memoryOwner.Kind), ID: memoryOwner.ID},
		Context: conversationRepository,
		Search:  retrievalAdapter, Retrieval: retrievalAdapter, Eligibility: knowledgePort, Topics: topicAdapter,
		Finalizer: finalizer, Progress: progress, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
		Budget: agentApplicationBudget(contract),
	})
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	return agentWorkflowComponents{
		relation: relation, rag: rag, model: model, contract: contract,
		capability: agentCapabilityStatus{available: true},
	}, nil
}

type artifactWorkflowComponents struct {
	generation *artifactpostgres.SectionGenerationRepository
	terminal   *artifactpostgres.SectionGenerationTerminalHook
	executor   *artifactworkflow.Executor
	catalog    *agentapplication.RuntimeCatalog
	capability agentCapabilityStatus
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
		generation: generation, terminal: terminal,
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
		Model: model, Catalog: catalog, Repository: agentRepository, Context: generation,
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

func agentApplicationBudget(contract platformmodels.ChatContract) agentapplication.RunBudget {
	budget := agentapplication.DefaultRunBudget()
	budget.MaxRequestBytes = min(contract.MaxRequestBytes*agentapplication.StructuredCallLimit, agentapplication.MaxRunRequestBytes)
	budget.MaxResponseBytes = min(contract.MaxResponseBytes*agentapplication.StructuredCallLimit, agentapplication.MaxRunResponseBytes)
	budget.Timeout = min(contract.Timeout*time.Duration(agentapplication.StructuredCallLimit), agentapplication.MaxRunTimeout)
	return budget
}

type reindexComponents struct {
	worker     *reindexriver.Worker
	dispatcher *retrievalruntime.Runner
}

func newReindexComponents(db *pgxpool.Pool, cfg config.Config, workspaceRepository *workspacepostgres.Repository, committedGit *gitcli.WritebackClient, insertClient *riveradapter.Client, workerID foundation.ID, logger *slog.Logger, metrics observability.Metrics, fatalInvariants chan<- error, modelSettingsRevision *int64, modelRuntimes ...*modelsettingsruntime.Models) (reindexComponents, error) {
	models, err := modelRuntimeForComposition(cfg, modelRuntimes...)
	if err != nil {
		return reindexComponents{}, err
	}
	ids := foundation.NewUUIDGenerator(nil)
	clock := foundation.SystemClock{}
	files := filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}}
	workspaceService := workspaceapplication.NewService(workspaceapplication.Dependencies{
		Repository: workspaceRepository, CommittedFiles: files, CommittedGit: committedGit, IDs: ids, Clock: clock,
	})
	ingestionRepository, err := ingestionpostgres.NewRepository(db)
	if err != nil {
		return reindexComponents{}, err
	}
	sourceReader, err := ingestionworkspace.NewReader(workspaceRepository, files)
	if err != nil {
		return reindexComponents{}, err
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
		return reindexComponents{}, err
	}
	retrievalRepository, err := retrievalpostgres.NewRepository(db)
	if err != nil {
		return reindexComponents{}, err
	}
	retrievalService, err := retrievalapplication.NewService(retrievalapplication.Dependencies{Store: retrievalRepository, IDs: ids, Clock: clock})
	if err != nil {
		return reindexComponents{}, err
	}
	regressionService, err := retrievalapplication.NewRegressionService(retrievalRepository)
	if err != nil {
		return reindexComponents{}, err
	}
	processorOptions := retrievalapplication.DefaultFTSOnlyProcessorOptions(cfg.ReindexDispatchErrorBackoff)
	vectorBuilder, err := retrievalapplication.NewVectorRecoveryBuilder(retrievalRepository, clock)
	if err != nil {
		return reindexComponents{}, err
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
			return reindexComponents{}, registrationErr
		}
		builder, builderErr := retrievalapplication.NewVectorBuilder(retrievalapplication.VectorBuilderDependencies{
			Store: retrievalRepository, Embedder: embedder, Clock: clock,
		})
		if builderErr != nil {
			return reindexComponents{}, builderErr
		}
		fusion, fusionErr := configuredRRF(cfg)
		if fusionErr != nil {
			return reindexComponents{}, fusionErr
		}
		embeddingVersionID := registered.EmbeddingVersion.ID
		processorOptions.EmbeddingVersionID = &embeddingVersionID
		processorOptions.FusionConfig = fusion
		vectorBuilder = builder
	}
	deliveryRepository, err := retrievalpostgres.NewDeliveryRepository(db, ids)
	if err != nil {
		return reindexComponents{}, err
	}
	deliveryRuntime, err := retrievalapplication.NewDeliveryRuntime(deliveryRepository)
	if err != nil {
		return reindexComponents{}, err
	}
	processor, err := retrievalapplication.NewProcessor(retrievalapplication.ProcessorDependencies{
		Contexts: deliveryRepository, Capture: workspaceService, Ingestion: ingestionService,
		Retrieval: retrievalService, Vectors: vectorBuilder, Regression: regressionService,
	}, processorOptions)
	if err != nil {
		return reindexComponents{}, err
	}
	completion, err := retrievalapplication.NewCompletionService(retrievalRepository, ids, cfg.ReindexDispatchErrorBackoff)
	if err != nil {
		return reindexComponents{}, err
	}
	worker, err := reindexriver.NewWorker(deliveryRuntime, processor, completion, reindexriver.WorkerOptions{
		Owner: fmt.Sprintf("reindex-worker:%s", workerID), LeaseDuration: cfg.ReindexLeaseDuration,
		HeartbeatInterval: cfg.ReindexHeartbeatInterval, Metrics: metrics, Logger: logger,
		FatalInvariants: fatalInvariants,
	})
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
	return reindexComponents{worker: worker, dispatcher: runner}, nil
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
