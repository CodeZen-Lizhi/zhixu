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
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentretrieval "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/retrieval"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontrolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/workflow"
	collectionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/collection/adapter/postgres"
	collectionapplication "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	conversationpostgres "github.com/CodeZen-Lizhi/zhixu/internal/conversation/adapter/postgres"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
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
	agentStructuredMaxOutputTokens = 8192
)

type workerComponents struct {
	safeWriteback   *changecontrolworkflow.Node
	tools           toolRuntimeComponents
	agentCapability agentCapabilityStatus
	reindexWorker   *reindexriver.Worker
	dispatcher      *retrievalruntime.Runner
	runtimeClient   *riveradapter.Client
	definitions     *workflowapplication.DefinitionRegistry
	executors       *workflowapplication.ExecutorRegistry
	semanticScan    *graphworkflow.SemanticLinkScanExecutor
	healthScan      *healthworkflowadapter.HealthScanExecutor
	healthScanStart *healthapplication.ScanService
	healthSchedule  *healthapplication.ScheduleService
	healthAffected  *healthapplication.AffectedChangeDispatcher
	fatalInvariants <-chan error
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
	components, err := newWorkerComponents(database.DB(), cfg, logger, telemetry.Metrics())
	if err != nil {
		logger.Error("worker components are unavailable", "error_code", "WORKER_COMPONENTS_UNAVAILABLE")
		return err
	}

	readiness := workflowruntime.NewReadiness()
	readiness.SetDatabaseOK(true)
	readiness.SetRiverSchemaOK(true)
	readiness.SetDefinitionsOK(components.definitions != nil)
	readiness.SetExecutorsOK(components.executors != nil)
	readiness.SetDependenciesOK(components.safeWriteback != nil && components.reindexWorker != nil && components.dispatcher != nil && agentWorkflowReadiness(components))
	toolEnabled := cfg.ToolRuntimeMode == config.ToolModeEnabled
	toolContractsOK, toolExecutorsOK, toolDependenciesOK := toolWorkflowReadiness(components)
	readiness.SetToolRuntimeState(toolEnabled, toolContractsOK, toolExecutorsOK, toolDependenciesOK)
	readiness.SetWebFetchState(cfg.WebFetchMode == config.ToolModeEnabled, false)
	health, err := startWorkerHealthServer(cfg.WorkerHealthAddr, workflowhealth.NewHandler(readiness))
	if err != nil {
		logger.Error("worker health server could not be started", "error_code", "WORKER_HEALTH_START_FAILED")
		return err
	}

	controller, err := newLifecycleController(components.runtimeClient, components.dispatcher)
	if err != nil {
		_ = health.server.Close()
		return err
	}
	processContext, cancelProcess := context.WithCancel(context.Background())
	defer cancelProcess()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := controller.Start(processContext); err != nil {
		readiness.BeginShutdown()
		_ = health.server.Close()
		logger.Error("workflow runtime could not be started", "error_code", "WORKFLOW_RIVER_CLIENT_START_FAILED")
		return err
	}
	readiness.SetRiverStarted(true)
	readiness.SetReindexDispatcherStarted(components.dispatcher.Started())
	logger.Info("worker started", "version", cfg.Version, "safe_writeback_node", components.safeWriteback != nil,
		"semantic_link_scan", components.semanticScan != nil,
		"agent_available", components.agentCapability.available, "agent_capability_code", components.agentCapability.code,
		"tool_runtime_enabled", components.tools.runtimeEnabled, "tool_executor_count", len(components.tools.enabledRefs),
		"web_fetch_enabled", cfg.WebFetchMode == config.ToolModeEnabled, "reindex_dispatcher", components.dispatcher.Started())

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
			}
		}
	}

shutdown:
	readiness.BeginShutdown()
	readiness.SetReindexDispatcherStarted(false)
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), cfg.WorkerHardStopTimeout)
	defer cancelShutdown()
	if err := controller.Shutdown(shutdownContext, shutdownMode); err != nil && runErr == nil {
		runErr = err
	}
	readiness.SetRiverStarted(false)
	if err := health.server.Shutdown(shutdownContext); err != nil && runErr == nil {
		runErr = err
	}
	if errors.Is(shutdownContext.Err(), context.DeadlineExceeded) {
		if err := recordShutdownMetric(telemetry.Metrics(), controller.Mode(), "failure"); err != nil {
			logger.Warn("worker shutdown metric failed", "error_code", "WORKER_METRIC_RECORD_FAILED")
		}
		logger.Error("worker hard shutdown deadline exceeded", "error_code", "WORKER_HARD_SHUTDOWN_TIMEOUT")
		return context.DeadlineExceeded
	}
	result := "success"
	if runErr != nil {
		result = "failure"
	}
	if err := recordShutdownMetric(telemetry.Metrics(), controller.Mode(), result); err != nil {
		logger.Warn("worker shutdown metric failed", "error_code", "WORKER_METRIC_RECORD_FAILED")
	}
	if err := telemetry.Shutdown(shutdownContext); err != nil && runErr == nil {
		runErr = err
	}
	return runErr
}

func newWorkerComponents(db *pgxpool.Pool, cfg config.Config, logger *slog.Logger, metrics observability.Metrics) (workerComponents, error) {
	if db == nil {
		return workerComponents{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKER_DATABASE_UNAVAILABLE", true, errors.New("database pool is nil"))
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
	toolComponents, err := newToolRuntimeComponents(db, cfg, workspaceRepository, gitRepository)
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
	agentComponents, err := newAgentWorkflowComponents(db, cfg, workspaceRepository)
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
	if toolComponents.runtimeEnabled {
		if err := definitions.Register(*toolComponents.definition); err != nil {
			return workerComponents{}, err
		}
	}
	if err := definitions.Freeze(); err != nil {
		return workerComponents{}, err
	}
	riverOptions := riveradapter.Options{
		Queue: cfg.WorkerQueue, MaxWorkers: cfg.WorkerMaxWorkers,
		JobTimeout: cfg.WorkerJobTimeout, RescueStuckJobsAfter: cfg.WorkerRescueStuckJobsAfter,
		SoftStopTimeout: cfg.WorkerSoftStopTimeout, Logger: logger,
	}
	insertClient, err := riveradapter.NewClientWithOptions(db, nil, riverOptions)
	if err != nil {
		return workerComponents{}, err
	}
	inserter, err := riveradapter.NewJobInserter(insertClient)
	if err != nil {
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
	runtimeRepository, err := workflowpostgres.NewRuntimeRepository(db, inserter, cancellationGuard)
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
	runtimeCoordinator, err := workflowapplication.NewRuntimeCoordinator(runtimeRepository)
	if err != nil {
		return workerComponents{}, err
	}
	workerID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		return workerComponents{}, err
	}
	fatalInvariants := make(chan error, 1)
	runtimeWorker, err := riveradapter.NewRuntimeNodeWorkerWithObservability(executors, runtimeCoordinator, fmt.Sprintf("worker:%s", workerID), cfg.WorkflowLeaseDuration, cfg.WorkflowHeartbeatInterval, riveradapter.RuntimeWorkerObservability{
		Metrics: metrics, Queue: cfg.WorkerQueue, Logger: logger, FatalInvariants: fatalInvariants,
	})
	if err != nil {
		return workerComponents{}, err
	}
	reindex, err := newReindexComponents(db, cfg, workspaceRepository, gitRepository, insertClient, workerID, logger, metrics, fatalInvariants)
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
	runtimeClient, err := riveradapter.NewClientWithOptions(db, workers, riverOptions)
	if err != nil {
		return workerComponents{}, err
	}
	return workerComponents{
		safeWriteback: node, tools: toolComponents, agentCapability: agentComponents.capability,
		reindexWorker: reindex.worker, dispatcher: reindex.dispatcher,
		runtimeClient: runtimeClient, definitions: definitions, executors: executors, semanticScan: semanticScan, healthScan: healthScan, healthScanStart: healthScanStartService, healthSchedule: healthSchedule, healthAffected: healthAffected, fatalInvariants: fatalInvariants,
	}, nil
}

func newToolRuntimeComponents(db *pgxpool.Pool, cfg config.Config, workspaceRepository *workspacepostgres.Repository, gitInspector *gitcli.WritebackClient) (toolRuntimeComponents, error) {
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
	embedder, err := platformmodels.NewConfiguredEmbedder(cfg)
	if err != nil {
		return toolRuntimeComponents{}, err
	}
	searchService, err := retrievalapplication.NewSearchService(searchRepository, embedder, nil)
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

type agentWorkflowComponents struct {
	relation   *agentworkflow.Executor
	rag        *agentworkflow.RAGWorkflowExecutor
	capability agentCapabilityStatus
}

func newAgentWorkflowComponents(db *pgxpool.Pool, cfg config.Config, workspaceRepository *workspacepostgres.Repository) (agentWorkflowComponents, error) {
	if cfg.ChatProvider == config.ChatProviderDisabled {
		return agentWorkflowComponents{capability: agentCapabilityStatus{code: agentworkflow.ErrorCodeCapabilityUnavailable}}, nil
	}
	model, err := platformmodels.NewConfiguredChatModel(cfg)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	contractProvider, ok := model.(interface {
		Contract() platformmodels.ChatContract
	})
	if !ok {
		return agentWorkflowComponents{}, foundation.NewError(foundation.ErrorConsistencyViolation, "AGENT_CHAT_CONTRACT_UNAVAILABLE", false, errors.New("configured chat model does not expose its frozen contract"))
	}
	contract := contractProvider.Contract()
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
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}, Budget: agentApplicationBudget(cfg),
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
	embedder, err := platformmodels.NewConfiguredEmbedder(cfg)
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	searchService, err := retrievalapplication.NewSearchService(searchRepository, embedder, nil)
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
	rag, err := agentworkflow.NewRAGWorkflowExecutor(agentworkflow.RAGWorkflowExecutorDependencies{
		Model: model, Catalog: catalog, Repository: repository, Context: conversationRepository,
		Search: retrievalAdapter, Retrieval: retrievalAdapter, Eligibility: knowledgePort, Topics: topicAdapter,
		Finalizer: finalizer, Progress: progress, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
		Budget: agentApplicationBudget(cfg),
	})
	if err != nil {
		return agentWorkflowComponents{}, err
	}
	return agentWorkflowComponents{relation: relation, rag: rag, capability: agentCapabilityStatus{available: true}}, nil
}

func agentApplicationBudget(cfg config.Config) agentapplication.RunBudget {
	budget := agentapplication.DefaultRunBudget()
	budget.MaxRequestBytes = min(cfg.ChatMaxRequestBytes*agentapplication.StructuredCallLimit, agentapplication.MaxRunRequestBytes)
	budget.MaxResponseBytes = min(cfg.ChatMaxResponseBytes*agentapplication.StructuredCallLimit, agentapplication.MaxRunResponseBytes)
	budget.Timeout = min(cfg.ChatTimeout*time.Duration(agentapplication.StructuredCallLimit), agentapplication.MaxRunTimeout)
	return budget
}

type reindexComponents struct {
	worker     *reindexriver.Worker
	dispatcher *retrievalruntime.Runner
}

func newReindexComponents(db *pgxpool.Pool, cfg config.Config, workspaceRepository *workspacepostgres.Repository, committedGit *gitcli.WritebackClient, insertClient *riveradapter.Client, workerID foundation.ID, logger *slog.Logger, metrics observability.Metrics, fatalInvariants chan<- error) (reindexComponents, error) {
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
	embedder, err := platformmodels.NewConfiguredEmbedder(cfg)
	if err != nil {
		return reindexComponents{}, err
	}
	if embedder != nil {
		contract := embedder.Contract()
		registrationContext, cancelRegistration := context.WithTimeout(context.Background(), cfg.DatabasePingTimeout)
		registered, registrationErr := retrievalService.RegisterEmbedding(registrationContext, retrievalapplication.RegisterEmbeddingRequest{
			Provider: contract.Provider, AdapterName: contract.AdapterName, AdapterVersion: contract.AdapterVersion,
			Model: contract.Model, Dimensions: contract.Dimensions, Normalization: contract.Normalization,
			DistanceMetric: contract.DistanceMetric, ConfigHash: contract.ConfigHash,
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
