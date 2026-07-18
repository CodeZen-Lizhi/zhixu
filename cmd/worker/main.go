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

	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontrolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/adapter/postgres"
	ingestionworkspace "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/adapter/workspace"
	ingestionapplication "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	platformparser "github.com/CodeZen-Lizhi/zhixu/internal/platform/parser"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	reindexriver "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/river"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	retrievalruntime "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/runtime"
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

type workerComponents struct {
	safeWriteback   *changecontrolworkflow.Node
	reindexWorker   *reindexriver.Worker
	dispatcher      *retrievalruntime.Runner
	runtimeClient   *riveradapter.Client
	definitions     *workflowapplication.DefinitionRegistry
	executors       *workflowapplication.ExecutorRegistry
	fatalInvariants <-chan error
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
	cfg, err := config.Load(configPath)
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
	readiness.SetDependenciesOK(components.safeWriteback != nil && components.reindexWorker != nil && components.dispatcher != nil)
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
	logger.Info("worker started", "version", cfg.Version, "safe_writeback_node", components.safeWriteback != nil, "reindex_dispatcher", components.dispatcher.Started())

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
	service, err := changecontrolapplication.NewWritebackService(changecontrolapplication.WritebackServiceDependencies{
		Repository: writebackRepository, Workspace: workspaceStore, Git: gitRepository,
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return workerComponents{}, err
	}
	node, err := changecontrolworkflow.NewNode(service)
	if err != nil {
		return workerComponents{}, err
	}
	targetReader, err := changecontrollocalfs.NewReader(workspaceRepository)
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
	catalog, err := workflowapplication.NewValidationCatalog([]int{1}, []workflowdomain.Permission{workflowdomain.PermissionWriteKnowledge, workflowdomain.PermissionGitWrite})
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
	if err := executors.Freeze(); err != nil {
		return workerComponents{}, err
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors)
	if err != nil {
		return workerComponents{}, err
	}
	if err := definitions.Register(changecontrolworkflow.RegisteredDefinition()); err != nil {
		return workerComponents{}, err
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
	runtimeRepository, err := workflowpostgres.NewRuntimeRepository(db, inserter, writebackRepository)
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
		safeWriteback: node, reindexWorker: reindex.worker, dispatcher: reindex.dispatcher,
		runtimeClient: runtimeClient, definitions: definitions, executors: executors, fatalInvariants: fatalInvariants,
	}, nil
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
	embedder, err := newConfiguredEmbedder(cfg)
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

func newConfiguredEmbedder(cfg config.Config) (retrievalapplication.Embedder, error) {
	switch cfg.EmbeddingProvider {
	case config.EmbeddingProviderDisabled:
		return nil, nil
	case config.EmbeddingProviderOpenAICompatible:
		return platformmodels.NewOpenAICompatibleEmbedder(platformmodels.OpenAIEmbeddingOptions{
			BaseURL: cfg.EmbeddingBaseURL, APIKey: cfg.EmbeddingAPIKey, Model: cfg.EmbeddingModel,
			Dimensions: cfg.EmbeddingDimensions, Normalization: cfg.EmbeddingNormalization,
			DistanceMetric: cfg.EmbeddingDistanceMetric, MaxBatchSize: cfg.EmbeddingMaxBatchSize,
			MaxInputBytes: cfg.EmbeddingMaxInputBytes, MaxBatchInputBytes: cfg.EmbeddingMaxBatchInputBytes,
			Timeout:          cfg.EmbeddingTimeout,
			MaxResponseBytes: cfg.EmbeddingMaxResponseBytes,
		})
	case config.EmbeddingProviderOllama:
		return platformmodels.NewOllamaEmbedder(platformmodels.OllamaEmbeddingOptions{
			BaseURL: cfg.EmbeddingBaseURL, Model: cfg.EmbeddingModel, Dimensions: cfg.EmbeddingDimensions,
			Normalization: cfg.EmbeddingNormalization, DistanceMetric: cfg.EmbeddingDistanceMetric,
			MaxBatchSize: cfg.EmbeddingMaxBatchSize, MaxInputBytes: cfg.EmbeddingMaxInputBytes,
			MaxBatchInputBytes: cfg.EmbeddingMaxBatchInputBytes,
			Timeout:            cfg.EmbeddingTimeout, MaxResponseBytes: cfg.EmbeddingMaxResponseBytes,
		})
	default:
		return nil, errors.New("embedding provider is unsupported")
	}
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
