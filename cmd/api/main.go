package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/app"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	approvaldispatchpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/approvaldispatchpostgres"
	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontrolhttp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/http"
	conversationpostgres "github.com/CodeZen-Lizhi/zhixu/internal/conversation/adapter/postgres"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationhttp "github.com/CodeZen-Lizhi/zhixu/internal/conversation/http"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventshttp "github.com/CodeZen-Lizhi/zhixu/internal/events/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphpostgres "github.com/CodeZen-Lizhi/zhixu/internal/graph/adapter/postgres"
	graphapplication "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphhttp "github.com/CodeZen-Lizhi/zhixu/internal/graph/http"
	ingestionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/adapter/postgres"
	ingestionworkspace "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/adapter/workspace"
	ingestionapplication "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	ingestionhttp "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	platformparser "github.com/CodeZen-Lizhi/zhixu/internal/platform/parser"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	retrievalworkspace "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/workspace"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievalhttp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/http"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/webassets"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	workflowhttp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/http"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	workspacehttp "github.com/CodeZen-Lizhi/zhixu/internal/workspace/http"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	configPath := flag.String("config", "", "optional YAML configuration path")
	flag.Parse()

	logger := observability.NewLogger("info", os.Stderr)
	cfg, loadErr := config.Load(*configPath)
	if loadErr != nil {
		logger.Error("configuration is invalid", "error_code", "INVALID_CONFIGURATION")
		os.Exit(1)
	}

	var database *postgres.Pool
	var databaseErr error
	databaseURL, databaseConfigErr := cfg.DatabaseConnectionString()
	if databaseConfigErr == nil && loadErr == nil {
		database, databaseErr = postgres.Open(context.Background(), databaseURL, cfg.DatabaseMaxConns, cfg.DatabaseMinConns)
		if databaseErr != nil {
			logger.Error("database pool is unavailable", "error_code", "DEPENDENCY_UNAVAILABLE")
		}
	}
	defer func() {
		if database != nil {
			database.Close()
		}
	}()

	static, staticErr := webassets.NewDir(cfg.WebAssetsDir)
	if staticErr != nil {
		logger.Warn("web assets are unavailable", "error_code", "WEB_ASSETS_UNAVAILABLE")
		static = nil
	}

	workspaceHandler := workspacehttp.NewHandler(nil)
	workflowHandler := workflowhttp.NewHandler(nil)
	changeControlHandler := changecontrolhttp.NewHandler(nil)
	ingestionHandler := ingestionhttp.NewHandler(nil)
	retrievalHandler := retrievalhttp.NewHandler(nil, nil, nil)
	graphHandler := graphhttp.NewHandler(nil, cfg.GraphQueryTimeout)
	conversationHandler := conversationhttp.NewHandler(nil, conversationhttp.NewCursorCodec())
	eventsHandler := eventshttp.NewHandler(nil)
	ragEnabled := cfg.ChatProvider != config.ChatProviderDisabled
	var ragInitErr error
	fileScanner := filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}}
	if database != nil {
		configuredGraphHandler, graphHandlerErr := newGraphHandler(database.DB(), cfg.GraphQueryTimeout)
		if graphHandlerErr != nil {
			logger.Error("graph query service is unavailable", "error_code", "GRAPH_DEPENDENCY_UNAVAILABLE")
		} else {
			graphHandler = configuredGraphHandler
		}
		changeControlRepository, changeControlRepositoryErr := changecontrolpostgres.NewRepository(database.DB())
		var workflowRuntime *workflowpostgres.RuntimeRepository
		if changeControlRepositoryErr != nil {
			logger.Error("change control repository is unavailable", "error_code", "CHANGE_CONTROL_DATABASE_UNAVAILABLE")
		} else {
			workflowService, runtime, workflowServiceErr := newWorkflowComponents(database.DB(), cfg, changeControlRepository)
			if workflowServiceErr != nil {
				logger.Error("workflow service is unavailable", "error_code", "WORKFLOW_SERVICE_UNAVAILABLE")
				if ragEnabled {
					ragInitErr = firstError(ragInitErr, workflowServiceErr)
				}
			} else {
				workflowRuntime = runtime
				workflowHandler = workflowhttp.NewHandler(workflowService)
			}
		}
		workspaceRepository, repositoryErr := workspacepostgres.NewRepository(database.DB())
		if repositoryErr != nil {
			logger.Error("workspace repository is unavailable", "error_code", "WORKSPACE_DATABASE_UNAVAILABLE")
			if ragEnabled {
				ragInitErr = firstError(ragInitErr, errors.New("RAG workspace dependency is unavailable"))
			}
		} else {
			workspaceService := workspaceapplication.NewService(workspaceapplication.Dependencies{
				Repository:     workspaceRepository,
				Files:          fileScanner,
				Git:            gitcli.New(""),
				GitInitializer: gitcli.New(""),
				IDs:            foundation.NewUUIDGenerator(nil),
				Clock:          foundation.SystemClock{},
			})
			workspaceHandler = workspacehttp.NewHandler(workspaceService)
			configuredRetrievalHandler, retrievalHandlerErr := newRetrievalHandler(database.DB(), cfg, workspaceRepository, fileScanner)
			if retrievalHandlerErr != nil {
				logger.Error("retrieval search service is unavailable", "error_code", "RETRIEVAL_SEARCH_SERVICE_UNAVAILABLE")
				if ragEnabled {
					ragInitErr = firstError(ragInitErr, retrievalHandlerErr)
				}
			} else {
				retrievalHandler = configuredRetrievalHandler
			}

			ingestionRepository, ingestionRepositoryErr := ingestionpostgres.NewRepository(database.DB())
			sourceReader, sourceReaderErr := ingestionworkspace.NewReader(workspaceRepository, fileScanner)
			parserRegistry := platformparser.NewRegistry(platformparser.Options{MaxBytes: filesystem.DefaultMaxBytes})
			if ingestionRepositoryErr != nil || sourceReaderErr != nil {
				logger.Error("ingestion service is unavailable", "error_code", "INGESTION_SERVICE_UNAVAILABLE")
			} else {
				ingestionService, ingestionServiceErr := ingestionapplication.NewService(ingestionapplication.Dependencies{
					Repository: ingestionRepository, Sources: sourceReader, Parsers: parserRegistry,
					IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
					ContentPolicy: ingestionapplication.ContentPolicy{MaxBytes: ingestionapplication.DefaultContentMaxBytes},
					ChunkOptions:  ingestiondomain.ChunkOptions{StrategyVersion: "structure-v1", SchemaVersion: platformparser.ParseSchemaVersion, SoftMaxBytes: ingestiondomain.DefaultChunkSoftMaxBytes},
				})
				if ingestionServiceErr != nil {
					logger.Error("ingestion service is unavailable", "error_code", "INGESTION_SERVICE_UNAVAILABLE")
				} else {
					ingestionHandler = ingestionhttp.NewHandler(ingestionService)
				}
			}

			targetReader, targetReaderErr := changecontrollocalfs.NewReader(workspaceRepository)
			approvalGitInspector, approvalGitInspectorErr := gitcli.NewWritebackClient(gitcli.New(""), workspaceRepository)
			if changeControlRepositoryErr != nil || targetReaderErr != nil || approvalGitInspectorErr != nil || workflowRuntime == nil {
				logger.Error("change control dependencies are unavailable", "error_code", "CHANGE_CONTROL_DEPENDENCY_UNAVAILABLE")
			} else {
				dispatchRepository, dispatchRepositoryErr := approvaldispatchpostgres.NewApprovalDispatchRepository(database.DB(), workflowRuntime, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
				if dispatchRepositoryErr != nil {
					logger.Error("approval dispatch repository is unavailable", "error_code", "APPROVAL_DISPATCH_DEPENDENCY_MISSING")
				} else {
					changeControlService, changeControlServiceErr := changecontrolapplication.NewServiceWithDispatch(changeControlRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, targetReader, approvalGitInspector, dispatchRepository)
					if changeControlServiceErr != nil {
						logger.Error("change control service is unavailable", "error_code", "CHANGE_CONTROL_SERVICE_UNAVAILABLE")
					} else {
						changeControlHandler = changecontrolhttp.NewHandler(changeControlService)
					}
				}
			}
		}
		configuredConversation, configuredEvents, conversationErr := newConversationHandlers(database.DB(), workflowRuntime, questionDispatchEnabled(ragEnabled, ragInitErr))
		if conversationErr != nil {
			logger.Error("conversation service is unavailable", "error_code", "CONVERSATION_SERVICE_UNAVAILABLE")
			if ragEnabled {
				ragInitErr = firstError(ragInitErr, conversationErr)
			}
		} else {
			conversationHandler = configuredConversation
			eventsHandler = configuredEvents
		}
	}

	deps := app.Dependencies{
		Version:           cfg.Version,
		Database:          database,
		DatabaseConfigErr: firstError(loadErr, databaseConfigErr),
		DatabaseInitErr:   databaseErr,
		PingTimeout:       cfg.DatabasePingTimeout,
		Static:            static,
		Workspace:         workspaceHandler,
		Workflow:          workflowHandler,
		ChangeControl:     changeControlHandler,
		Ingestion:         ingestionHandler,
		Retrieval:         retrievalHandler,
		Conversation:      conversationHandler,
		Events:            eventsHandler,
		Graph:             graphHandler,
		RAGEnabled:        ragEnabled,
		RAGInitErr:        ragInitErr,
		Logger:            logger,
	}
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           app.NewRouter(deps),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("api server starting", "addr", cfg.HTTPAddr, "version", cfg.Version)
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	stop, stopCancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopCancel()
	select {
	case err := <-serverErr:
		logger.Error("api server stopped unexpectedly", "error_code", "SERVER_FAILED", "error", err)
		if database != nil {
			database.Close()
		}
		os.Exit(1)
	case <-stop.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("api server shutdown failed", "error_code", "SHUTDOWN_FAILED", "error", err)
		}
	}
}

// newGraphHandler 以 canonical Knowledge facts 组装只读 Graph 查询链路。
func newGraphHandler(pool *pgxpool.Pool, timeout time.Duration) (*graphhttp.Handler, error) {
	if pool == nil {
		return nil, errors.New("graph database is unavailable")
	}
	repository, err := graphpostgres.NewRepository(pool)
	if err != nil {
		return nil, err
	}
	cursors, err := graphapplication.NewRandomCursorCodec()
	if err != nil {
		return nil, err
	}
	service, err := graphapplication.NewService(repository, cursors)
	if err != nil {
		return nil, err
	}
	return graphhttp.NewHandler(service, timeout), nil
}

func newRetrievalHandler(
	pool *pgxpool.Pool,
	cfg config.Config,
	workspaceRepository workspacedomain.SourceMaterialRepository,
	files workspacedomain.FileScanner,
) (*retrievalhttp.Handler, error) {
	if pool == nil || workspaceRepository == nil || files == nil {
		return nil, errors.New("retrieval search dependencies are unavailable")
	}
	searchRepository, err := retrievalpostgres.NewSearchRepository(pool)
	if err != nil {
		return nil, err
	}
	artifactReader, err := retrievalworkspace.NewReader(workspaceRepository, files)
	if err != nil {
		return nil, err
	}
	evidenceService, err := retrievalapplication.NewEvidenceReferenceService(searchRepository, artifactReader)
	if err != nil {
		return nil, err
	}
	embedder, err := platformmodels.NewConfiguredEmbedder(cfg)
	if err != nil {
		return nil, err
	}
	searchService, err := retrievalapplication.NewSearchService(searchRepository, embedder, nil)
	if err != nil {
		return nil, err
	}
	cursors, err := retrievalhttp.NewRandomCursorCodec()
	if err != nil {
		return nil, err
	}
	return retrievalhttp.NewHandler(searchService, evidenceService, cursors), nil
}

// questionDispatchEnabled 只在显式启用且全部 API RAG 依赖组装成功时开放异步 Question 命令。
func questionDispatchEnabled(ragEnabled bool, ragInitErr error) bool {
	return ragEnabled && ragInitErr == nil
}

// newConversationHandlers 使用同一个持久 Event Store 组装 Conversation 写事件与 SSE 重放边界。
func newConversationHandlers(
	pool *pgxpool.Pool,
	runtime *workflowpostgres.RuntimeRepository,
	ragEnabled bool,
) (*conversationhttp.Handler, *eventshttp.Handler, error) {
	if pool == nil {
		return nil, nil, errors.New("conversation database is unavailable")
	}
	eventStore, err := eventspostgres.NewStore(pool)
	if err != nil {
		return nil, nil, err
	}
	repository, err := conversationpostgres.NewRepository(pool, eventStore)
	if err != nil {
		return nil, nil, err
	}
	var dispatcher conversationapplication.QuestionDispatcher
	if ragEnabled {
		dispatcher, err = conversationpostgres.NewQuestionDispatcher(
			pool, runtime, eventStore, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, conversationworkflow.RegisteredDefinition(),
		)
		if err != nil {
			return nil, nil, err
		}
	}
	service, err := conversationapplication.NewService(conversationapplication.Dependencies{
		Repository: repository, QuestionDispatcher: dispatcher, FeedbackRepository: repository,
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return nil, nil, err
	}
	return conversationhttp.NewHandler(service, conversationhttp.NewCursorCodec()), eventshttp.NewHandler(eventStore), nil
}

func newWorkflowService(pool *pgxpool.Pool) (*workflowapplication.Service, error) {
	service, _, err := newWorkflowComponents(pool, config.Defaults())
	return service, err
}

func newWorkflowComponents(pool *pgxpool.Pool, cfg config.Config, guards ...workflowapplication.CancellationSafetyGuard) (*workflowapplication.Service, *workflowpostgres.RuntimeRepository, error) {
	legacyRepository, err := workflowpostgres.NewRepository(pool)
	if err != nil {
		return nil, nil, err
	}
	client, err := riveradapter.NewClientWithOptions(pool, nil, riveradapter.Options{
		Queue: cfg.WorkerQueue, MaxWorkers: cfg.WorkerMaxWorkers,
		JobTimeout: cfg.WorkerJobTimeout, RescueStuckJobsAfter: cfg.WorkerRescueStuckJobsAfter,
		SoftStopTimeout: cfg.WorkerSoftStopTimeout,
	})
	if err != nil {
		return nil, nil, err
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		return nil, nil, err
	}
	runtimeRepository, err := workflowpostgres.NewRuntimeRepository(pool, inserter, guards...)
	if err != nil {
		return nil, nil, err
	}
	catalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		return nil, nil, err
	}
	executors, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		return nil, nil, err
	}
	if err := registerAPIWorkflowExecutors(cfg, executors); err != nil {
		return nil, nil, err
	}
	if err := executors.Freeze(); err != nil {
		return nil, nil, err
	}
	toolContracts, err := newToolContractRegistry()
	if err != nil {
		return nil, nil, err
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors, toolContracts)
	if err != nil {
		return nil, nil, err
	}
	if err := registerAPIWorkflowDefinitions(cfg, definitions); err != nil {
		return nil, nil, err
	}
	if err := definitions.Freeze(); err != nil {
		return nil, nil, err
	}
	service, err := workflowapplication.NewRuntimeService(legacyRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, workflowapplication.RuntimeDependencies{Definitions: definitions, Starter: runtimeRepository, State: runtimeRepository, Human: runtimeRepository})
	if err != nil {
		return nil, nil, err
	}
	return service, runtimeRepository, nil
}

func newToolContractRegistry() (*toolsapplication.Registry, error) {
	return toolcatalog.NewFrozenContractRegistry()
}

func registerAPIWorkflowExecutors(cfg config.Config, executors *workflowapplication.ExecutorRegistry) error {
	if err := executors.Register(workflowapplication.CanonicalJSONHashNodeKind, workflowapplication.CanonicalJSONHashInputSchemaVersion, workflowapplication.NewCanonicalJSONHashExecutor()); err != nil {
		return err
	}
	if cfg.ChatProvider != config.ChatProviderDisabled {
		if err := executors.RegisterContract(agentworkflow.RelationAssessmentNodeKind, agentworkflow.RelationAssessmentInputSchemaVersion); err != nil {
			return err
		}
		if err := executors.RegisterContract(conversationworkflow.NodeKind, conversationworkflow.InputSchemaVersion); err != nil {
			return err
		}
	}
	return nil
}

func registerAPIWorkflowDefinitions(cfg config.Config, definitions *workflowapplication.DefinitionRegistry) error {
	if err := definitions.Register(workflowdomain.RegisteredDefinition{
		Key: "deterministic.hash", Version: 1, InputSchemaVersion: 1,
		Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
			Key: "hash", Kind: workflowapplication.CanonicalJSONHashNodeKind,
			InputSchemaVersion: 1, OutputSchemaVersion: 1,
			RetryPolicy: workflowdomain.RetryPolicy{},
		}}},
	}); err != nil {
		return err
	}
	if cfg.ChatProvider != config.ChatProviderDisabled {
		if err := definitions.Register(agentworkflow.RegisteredDefinition()); err != nil {
			return err
		}
		if err := definitions.Register(conversationworkflow.RegisteredDefinition()); err != nil {
			return err
		}
	}
	return nil
}

func firstError(values ...error) error {
	for _, err := range values {
		if err != nil {
			return err
		}
	}
	return nil
}
