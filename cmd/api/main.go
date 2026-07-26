package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/app"
	artifactchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/changecontrol"
	artifactlocalfs "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/localfs"
	artifactpostgres "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/postgres"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifacthttp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/http"
	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	authpostgres "github.com/CodeZen-Lizhi/zhixu/internal/auth/adapter/postgres"
	authapplication "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	approvaldispatchpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/approvaldispatchpostgres"
	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontrolhttp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/http"
	collectionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/collection/adapter/postgres"
	collectionapplication "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	collectionhttp "github.com/CodeZen-Lizhi/zhixu/internal/collection/http"
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
	healthcollection "github.com/CodeZen-Lizhi/zhixu/internal/health/adapter/collection"
	healthpostgres "github.com/CodeZen-Lizhi/zhixu/internal/health/adapter/postgres"
	healthapplication "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	healthdetector "github.com/CodeZen-Lizhi/zhixu/internal/health/detector"
	healthhttp "github.com/CodeZen-Lizhi/zhixu/internal/health/http"
	healthworkflow "github.com/CodeZen-Lizhi/zhixu/internal/health/workflow"
	ingestionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/adapter/postgres"
	ingestionworkspace "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/adapter/workspace"
	ingestionapplication "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	ingestionhttp "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/http"
	knowledgeaudit "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/audit"
	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledgehttp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/http"
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

const (
	apiReadTimeout       = 30 * time.Second
	apiReadHeaderTimeout = 5 * time.Second
	apiIdleTimeout       = 60 * time.Second
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
	authRequired := cfg.AuthMode == config.AuthModeRequired
	var authHandler *authhttp.Handler
	var authInitErr error
	var authCheck func(context.Context) error
	if authRequired {
		if database == nil {
			authInitErr = errors.New("authentication database is unavailable")
		} else {
			authRepository, repositoryErr := authpostgres.NewRepository(database.DB())
			if repositoryErr != nil {
				authInitErr = repositoryErr
			} else {
				authCheck = authRepository.Check
				authService, serviceErr := authapplication.NewService(authRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, authapplication.Options{
					BootstrapToken: cfg.AuthBootstrapToken,
					SessionTTL:     cfg.AuthSessionTTL,
					APITokenTTL:    cfg.AuthAPITokenTTL,
				})
				if serviceErr != nil {
					authInitErr = serviceErr
				} else {
					authHandler, authInitErr = authhttp.NewHandler(authService, authhttp.Options{
						SecureCookie: cfg.AuthSecureCookie, AllowedOrigins: cfg.AuthAllowedOrigins,
					})
				}
			}
		}
	}

	workspaceHandler := workspacehttp.NewHandler(nil)
	collectionHandler := collectionhttp.NewHandler(nil, cfg.GraphQueryTimeout)
	healthHandler := healthhttp.NewHandler(nil, nil, nil, nil, nil)
	workflowHandler := workflowhttp.NewHandler(nil)
	changeControlHandler := changecontrolhttp.NewHandler(nil)
	ingestionHandler := ingestionhttp.NewHandler(nil)
	retrievalHandler := retrievalhttp.NewHandler(nil, nil, nil)
	graphHandler := graphhttp.NewHandler(nil, cfg.GraphQueryTimeout)
	candidateHandler := graphhttp.NewCandidateHandler(nil, cfg.GraphQueryTimeout)
	conversationHandler := conversationhttp.NewHandler(nil, conversationhttp.NewCursorCodec())
	eventsHandler := eventshttp.NewHandler(nil)
	knowledgeHandler := knowledgehttp.NewHandler(nil, nil, cfg.GraphQueryTimeout)
	artifactHandler := artifacthttp.NewHandler(nil, nil, cfg.GraphQueryTimeout)
	ragEnabled := cfg.ChatProvider != config.ChatProviderDisabled
	var ragInitErr error
	var changeControlService *changecontrolapplication.Service
	var artifactGeneration *artifactpostgres.SectionGenerationRepository
	artifactIDs := foundation.NewUUIDGenerator(nil)
	artifactClock := foundation.SystemClock{}
	fileScanner := filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}}
	if database != nil {
		configuredKnowledgeHandler, knowledgeHandlerErr := newKnowledgeHandler(database.DB(), cfg.GraphQueryTimeout)
		if knowledgeHandlerErr != nil {
			logger.Error("knowledge timeline and impact services are unavailable", "error_code", "KNOWLEDGE_TIMELINE_IMPACT_UNAVAILABLE")
		} else {
			knowledgeHandler = configuredKnowledgeHandler
		}
		if err := configureCollectionHealth(database, cfg, &collectionHandler, &healthHandler, nil); err != nil {
			logger.Error("collection and health services are unavailable", "error_code", "COLLECTION_HEALTH_DEPENDENCY_UNAVAILABLE")
		}
		configuredGraphHandler, graphHandlerErr := newGraphHandler(database.DB(), cfg.GraphQueryTimeout)
		if graphHandlerErr != nil {
			logger.Error("graph query service is unavailable", "error_code", "GRAPH_DEPENDENCY_UNAVAILABLE")
		} else {
			graphHandler = configuredGraphHandler
		}
		workspaceRepository, repositoryErr := workspacepostgres.NewRepository(database.DB())
		healthEvents, healthEventsErr := eventspostgres.NewStore(database.DB())
		changeControlRepository, changeControlRepositoryErr := changecontrolpostgres.NewRepository(database.DB(), healthEvents)
		var workflowRuntime *workflowpostgres.RuntimeRepository
		if changeControlRepositoryErr != nil {
			logger.Error("change control repository is unavailable", "error_code", "CHANGE_CONTROL_DATABASE_UNAVAILABLE")
		} else if healthEventsErr != nil {
			logger.Error("health event store is unavailable", "error_code", "HEALTH_EVENT_STORE_UNAVAILABLE")
		} else {
			healthCancellationGuard, healthCancellationGuardErr := healthpostgres.NewScanCancellationGuard(healthEvents)
			cancellationGuard, cancellationGuardErr := workflowapplication.NewCompositeCancellationSafetyGuard(
				changeControlRepository,
				graphpostgres.NewSemanticLinkScanCancellationGuard(),
				healthCancellationGuard,
			)
			if healthCancellationGuardErr != nil {
				cancellationGuardErr = healthCancellationGuardErr
			}
			if cancellationGuardErr != nil {
				logger.Error("workflow cancellation guard is unavailable", "error_code", "WORKFLOW_CANCELLATION_GUARD_UNAVAILABLE")
			}
			var workflowService *workflowapplication.Service
			var runtime *workflowpostgres.RuntimeRepository
			var workflowServiceErr error
			if cancellationGuardErr != nil {
				workflowServiceErr = cancellationGuardErr
			} else if repositoryErr != nil {
				workflowServiceErr = repositoryErr
			} else {
				workflowService, runtime, artifactGeneration, workflowServiceErr = newAPIArtifactWorkflowComponents(
					database.DB(), cfg, workspaceRepository, fileScanner, cancellationGuard, artifactIDs, artifactClock,
				)
			}
			if workflowServiceErr != nil {
				logger.Error("workflow service is unavailable", "error_code", "WORKFLOW_SERVICE_UNAVAILABLE")
				if ragEnabled {
					ragInitErr = firstError(ragInitErr, workflowServiceErr)
				}
			} else {
				workflowRuntime = runtime
				workflowHandler = workflowhttp.NewHandler(workflowService)
				if err := configureCollectionHealth(database, cfg, &collectionHandler, &healthHandler, workflowRuntime); err != nil {
					logger.Error("collection and health workflow services are unavailable", "error_code", "COLLECTION_HEALTH_WORKFLOW_DEPENDENCY_UNAVAILABLE")
				}
			}
		}
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
				dispatchRepository, dispatchRepositoryErr := approvaldispatchpostgres.NewApprovalDispatchRepository(database.DB(), workflowRuntime, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, healthEvents)
				if dispatchRepositoryErr != nil {
					logger.Error("approval dispatch repository is unavailable", "error_code", "APPROVAL_DISPATCH_DEPENDENCY_MISSING")
				} else {
					knowledgeRelationApplier, knowledgeRelationApplierErr := knowledgepostgres.NewApprovedRelationApplyRepository(database.DB(), foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, healthEvents)
					if knowledgeRelationApplierErr != nil {
						logger.Error("knowledge relation apply service is unavailable", "error_code", "KNOWLEDGE_RELATION_APPLIER_UNAVAILABLE")
					}
					configuredChangeControlService, changeControlServiceErr := changecontrolapplication.NewServiceWithDispatch(changeControlRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, targetReader, approvalGitInspector, dispatchRepository, knowledgeRelationApplier)
					if changeControlServiceErr != nil {
						logger.Error("change control service is unavailable", "error_code", "CHANGE_CONTROL_SERVICE_UNAVAILABLE")
					} else {
						changeControlService = configuredChangeControlService
						changeControlHandler = changecontrolhttp.NewHandler(changeControlService)
					}
				}
			}
			generation := []artifactapplication.SectionGenerationStarter(nil)
			if artifactGeneration != nil {
				generation = artifactGenerationDependencies(ragEnabled, artifactGeneration)
			}
			configuredArtifactHandler, artifactHandlerErr := newArtifactHandlerWithDependencies(
				database.DB(), workspaceRepository, fileScanner, changeControlService, cfg.GraphQueryTimeout,
				artifactIDs, artifactClock, generation...,
			)
			if artifactHandlerErr != nil {
				logger.Error("artifact service is unavailable", "error_code", "ARTIFACT_DEPENDENCY_UNAVAILABLE")
			} else {
				artifactHandler = configuredArtifactHandler
			}
		}
		configuredCandidateHandler, candidateHandlerErr := newCandidateHandler(database.DB(), workflowRuntime, cfg.GraphQueryTimeout)
		if candidateHandlerErr != nil {
			logger.Error("semantic link candidate service is unavailable", "error_code", "SEMANTIC_LINK_DEPENDENCY_UNAVAILABLE")
		} else {
			candidateHandler = configuredCandidateHandler
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
		Collection:        collectionHandler,
		Health:            healthHandler,
		Workflow:          workflowHandler,
		ChangeControl:     changeControlHandler,
		Ingestion:         ingestionHandler,
		Retrieval:         retrievalHandler,
		Conversation:      conversationHandler,
		Events:            eventsHandler,
		Knowledge:         knowledgeHandler,
		Artifact:          artifactHandler,
		Auth:              authHandler,
		AuthRequired:      authRequired,
		AuthInitErr:       authInitErr,
		AuthCheck:         authCheck,
		Graph:             graphHandler,
		Candidate:         candidateHandler,
		RAGEnabled:        ragEnabled,
		RAGInitErr:        ragInitErr,
		Logger:            logger,
	}
	server := newAPIServer(cfg.HTTPAddr, app.NewRouter(deps))

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

func newAPIServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadTimeout:       apiReadTimeout,
		ReadHeaderTimeout: apiReadHeaderTimeout,
		IdleTimeout:       apiIdleTimeout,
	}
}

// newKnowledgeHandler 组装不可变 Timeline 投影查询与只读 Impact 报告服务。
func newKnowledgeHandler(pool *pgxpool.Pool, timeout time.Duration) (*knowledgehttp.Handler, error) {
	if pool == nil {
		return nil, errors.New("knowledge timeline database is unavailable")
	}
	repository, err := knowledgepostgres.NewRepository(pool)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	cursor, err := knowledgeapplication.NewTimelineCursorCodec(key)
	if err != nil {
		return nil, err
	}
	timeline, err := knowledgeapplication.NewTimelineService(repository, cursor)
	if err != nil {
		return nil, err
	}
	auditRepository, err := auditpostgres.NewRepository(pool)
	if err != nil {
		return nil, err
	}
	auditRecorder, err := auditapplication.NewRecorder(auditRepository)
	if err != nil {
		return nil, err
	}
	impactAudit, err := knowledgeaudit.NewImpactRecorder(auditRecorder, impactAuditActor)
	if err != nil {
		return nil, err
	}
	impact, err := knowledgeapplication.NewImpactServiceWithAudit(repository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, impactAudit)
	if err != nil {
		return nil, err
	}
	return knowledgehttp.NewHandler(timeline, impact, timeout), nil
}

func impactAuditActor(ctx context.Context) (auditdomain.ActorType, string) {
	principal, found := authhttp.PrincipalFromContext(ctx)
	return impactAuditActorForPrincipal(principal, found)
}

func impactAuditActorForPrincipal(principal authdomain.Principal, found bool) (auditdomain.ActorType, string) {
	if !found || authdomain.ValidatePrincipal(principal) != nil {
		return auditdomain.ActorAnonymous, ""
	}
	switch principal.Kind {
	case authdomain.PrincipalSession:
		return auditdomain.ActorUser, string(principal.ID)
	case authdomain.PrincipalAPIToken:
		return auditdomain.ActorAPIToken, string(principal.ID)
	default:
		return auditdomain.ActorAnonymous, ""
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

// newCandidateHandler 组装独立候选查询与 Confirm Proposal 写入链路。
// 候选不可用不会改变正式 Graph 查询的 readiness。
func newCandidateHandler(pool *pgxpool.Pool, runtime *workflowpostgres.RuntimeRepository, timeout time.Duration) (*graphhttp.CandidateHandler, error) {
	if pool == nil {
		return nil, errors.New("semantic link candidate database is unavailable")
	}
	repository, err := graphpostgres.NewRepository(pool)
	if err != nil {
		return nil, err
	}
	confirmer, err := graphpostgres.NewCandidateConfirmRepository(
		pool,
		foundation.NewUUIDGenerator(nil),
		foundation.SystemClock{},
	)
	if err != nil {
		return nil, err
	}
	cursors, err := graphapplication.NewRandomCursorCodec()
	if err != nil {
		return nil, err
	}
	service, err := graphapplication.NewSemanticLinkCandidateService(repository, confirmer, cursors)
	if err != nil {
		return nil, err
	}
	if runtime == nil {
		return graphhttp.NewCandidateHandler(service, timeout), nil
	}
	scanRepository, err := graphpostgres.NewSemanticLinkScanRepository(pool, runtime, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		return nil, err
	}
	scanService, err := graphapplication.NewSemanticLinkScanService(scanRepository, scanRepository)
	if err != nil {
		return nil, err
	}
	planner, err := graphpostgres.NewSemanticLinkTopicScanPlanner(pool)
	if err != nil {
		return nil, err
	}
	collectionRepository, err := collectionpostgres.NewRepository(pool)
	if err != nil {
		return nil, err
	}
	collectionService, err := collectionapplication.NewService(collectionapplication.Dependencies{
		Repository: collectionRepository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return nil, err
	}
	smartPlanner, err := graphpostgres.NewSmartCollectionScanPlanner(collectionService)
	if err != nil {
		return nil, err
	}
	plannerSet, err := graphapplication.NewSemanticLinkScanPlannerSet(planner, smartPlanner)
	if err != nil {
		return nil, err
	}
	scanCommands, err := graphapplication.NewSemanticLinkScanCommandService(plannerSet, scanService)
	if err != nil {
		return nil, err
	}
	return graphhttp.NewCandidateHandler(service, timeout, scanCommands), nil
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

// newArtifactHandler 从 PostgreSQL 事实组装 Artifact 人工、查询和可选生成边界。
func newArtifactHandler(
	pool *pgxpool.Pool,
	workspaces *workspacepostgres.Repository,
	files workspacedomain.FileScanner,
	proposals artifactchangecontrol.ProposalCreator,
	timeout time.Duration,
	generation ...artifactapplication.SectionGenerationStarter,
) (*artifacthttp.Handler, error) {
	return newArtifactHandlerWithDependencies(
		pool, workspaces, files, proposals, timeout,
		foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, generation...,
	)
}

func newArtifactHandlerWithDependencies(
	pool *pgxpool.Pool,
	workspaces *workspacepostgres.Repository,
	files workspacedomain.FileScanner,
	proposals artifactchangecontrol.ProposalCreator,
	timeout time.Duration,
	ids foundation.IDGenerator,
	clock foundation.Clock,
	generation ...artifactapplication.SectionGenerationStarter,
) (*artifacthttp.Handler, error) {
	if pool == nil || workspaces == nil || files == nil || ids == nil || clock == nil {
		return nil, errors.New("artifact dependencies are unavailable")
	}
	if len(generation) > 1 {
		return nil, errors.New("artifact generation dependency is ambiguous")
	}
	repository, err := artifactpostgres.NewRepository(pool)
	if err != nil {
		return nil, err
	}
	verifier, err := newArtifactCitationVerifier(pool, workspaces, files)
	if err != nil {
		return nil, err
	}
	exporter, err := artifactlocalfs.NewExporter(workspaces)
	if err != nil {
		return nil, err
	}
	dependencies := artifactapplication.Dependencies{
		Repository: repository,
		Evidence:   verifier,
		Exporter:   exporter,
		IDs:        ids,
		Clock:      clock,
	}
	if proposals != nil {
		publisher, publisherErr := artifactchangecontrol.NewPublicationCreator(proposals)
		if publisherErr != nil {
			return nil, publisherErr
		}
		dependencies.Publisher = publisher
	}
	commands, err := artifactapplication.NewCommandService(dependencies)
	if err != nil {
		return nil, err
	}
	queries, err := artifactapplication.NewQueryService(repository)
	if err != nil {
		return nil, err
	}
	return artifacthttp.NewHandler(commands, queries, timeout, generation...), nil
}

// newArtifactCitationVerifier 组装 Retrieval 打开与 Knowledge 正式资格的服务端 Citation 校验器。
func newArtifactCitationVerifier(
	pool *pgxpool.Pool,
	workspaces *workspacepostgres.Repository,
	files workspacedomain.FileScanner,
) (*artifactapplication.ServerCitationVerifier, error) {
	if pool == nil || workspaces == nil || files == nil {
		return nil, errors.New("artifact citation dependencies are unavailable")
	}
	searchRepository, err := retrievalpostgres.NewSearchRepository(pool)
	if err != nil {
		return nil, err
	}
	artifactReader, err := retrievalworkspace.NewReader(workspaces, files)
	if err != nil {
		return nil, err
	}
	evidenceReferences, err := retrievalapplication.NewEvidenceReferenceService(searchRepository, artifactReader)
	if err != nil {
		return nil, err
	}
	knowledgeRepository, err := knowledgepostgres.NewRepository(pool)
	if err != nil {
		return nil, err
	}
	eligibility, err := knowledgeapplication.NewEvidenceEligibilityService(knowledgeRepository)
	if err != nil {
		return nil, err
	}
	return artifactapplication.NewServerCitationVerifier(evidenceReferences, eligibility)
}

// newArtifactGenerationAgent 先组装 Agent 事务端口与独立 terminal hook，解除 Runtime 构造依赖环。
func newArtifactGenerationAgent(
	pool *pgxpool.Pool,
) (*agentpostgres.Repository, *artifactpostgres.SectionGenerationTerminalHook, error) {
	if pool == nil {
		return nil, nil, errors.New("artifact generation database is unavailable")
	}
	agentRepository, err := agentpostgres.NewRepository(pool)
	if err != nil {
		return nil, nil, err
	}
	terminal, err := artifactpostgres.NewSectionGenerationTerminalHook(agentRepository, agentworkflow.DefaultProfileRef())
	if err != nil {
		return nil, nil, err
	}
	return agentRepository, terminal, nil
}

// newArtifactSectionGeneration 在权威 Runtime 构造后组装 PostgreSQL Generation Repository。
func newArtifactSectionGeneration(
	pool *pgxpool.Pool,
	workspaces *workspacepostgres.Repository,
	files workspacedomain.FileScanner,
	runtime artifactpostgres.RuntimeStarterTx,
	agentRepository *agentpostgres.Repository,
	ids foundation.IDGenerator,
	clock foundation.Clock,
) (*artifactpostgres.SectionGenerationRepository, error) {
	if pool == nil || workspaces == nil || files == nil || runtime == nil || agentRepository == nil || ids == nil || clock == nil {
		return nil, errors.New("artifact generation dependencies are unavailable")
	}
	verifier, err := newArtifactCitationVerifier(pool, workspaces, files)
	if err != nil {
		return nil, err
	}
	generation, err := artifactpostgres.NewSectionGenerationRepository(
		pool,
		runtime,
		agentRepository,
		verifier,
		ids,
		clock,
		agentworkflow.DefaultProfileRef(),
	)
	if err != nil {
		return nil, err
	}
	return generation, nil
}

// artifactGenerationDependencies 只在 Chat capability 启用时向 Artifact HTTP 注入章节生成能力。
func artifactGenerationDependencies(
	chatEnabled bool,
	generation artifactapplication.SectionGenerationStarter,
) []artifactapplication.SectionGenerationStarter {
	if !chatEnabled || generation == nil {
		return nil
	}
	return []artifactapplication.SectionGenerationStarter{generation}
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

type apiRuntimeRepositoryFactory struct {
	pool     *pgxpool.Pool
	inserter riveradapter.JobInserter
}

// newAPIRuntimeRepositoryFactory 使用同一 pool 与 Worker 配置创建可复用的 Runtime Repository factory。
func newAPIRuntimeRepositoryFactory(pool *pgxpool.Pool, cfg config.Config) (*apiRuntimeRepositoryFactory, error) {
	if pool == nil {
		return nil, errors.New("workflow database is unavailable")
	}
	client, err := riveradapter.NewClientWithOptions(pool, nil, riveradapter.Options{
		Queue: cfg.WorkerQueue, MaxWorkers: cfg.WorkerMaxWorkers,
		JobTimeout: cfg.WorkerJobTimeout, RescueStuckJobsAfter: cfg.WorkerRescueStuckJobsAfter,
		SoftStopTimeout: cfg.WorkerSoftStopTimeout,
	})
	if err != nil {
		return nil, err
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		return nil, err
	}
	return &apiRuntimeRepositoryFactory{pool: pool, inserter: inserter}, nil
}

func (factory *apiRuntimeRepositoryFactory) newRepository(hooks workflowpostgres.RuntimeRepositoryHooks) (*workflowpostgres.RuntimeRepository, error) {
	if factory == nil || factory.pool == nil || factory.inserter == nil {
		return nil, errors.New("workflow runtime repository factory is unavailable")
	}
	return workflowpostgres.NewRuntimeRepositoryWithHooks(factory.pool, factory.inserter, hooks)
}

// newAPIArtifactWorkflowComponents 按 Agent、terminal hook、权威 Runtime、Generation 的顺序消除构造环。
func newAPIArtifactWorkflowComponents(
	pool *pgxpool.Pool,
	cfg config.Config,
	workspaces *workspacepostgres.Repository,
	files workspacedomain.FileScanner,
	cancellation workflowapplication.CancellationSafetyGuard,
	ids foundation.IDGenerator,
	clock foundation.Clock,
) (*workflowapplication.Service, *workflowpostgres.RuntimeRepository, *artifactpostgres.SectionGenerationRepository, error) {
	factory, err := newAPIRuntimeRepositoryFactory(pool, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	agentRepository, terminal, err := newArtifactGenerationAgent(pool)
	if err != nil {
		return nil, nil, nil, err
	}
	runtime, err := factory.newRepository(workflowpostgres.RuntimeRepositoryHooks{
		CancellationSafety: cancellation,
		Terminal:           terminal,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	generation, err := newArtifactSectionGeneration(pool, workspaces, files, runtime, agentRepository, ids, clock)
	if err != nil {
		return nil, nil, nil, err
	}
	service, err := newWorkflowServiceWithRuntime(factory, cfg, runtime)
	if err != nil {
		return nil, nil, nil, err
	}
	return service, runtime, generation, nil
}

func newWorkflowComponents(pool *pgxpool.Pool, cfg config.Config, guards ...workflowapplication.CancellationSafetyGuard) (*workflowapplication.Service, *workflowpostgres.RuntimeRepository, error) {
	if len(guards) > 1 {
		return nil, nil, errors.New("workflow accepts at most one cancellation guard")
	}
	factory, err := newAPIRuntimeRepositoryFactory(pool, cfg)
	if err != nil {
		return nil, nil, err
	}
	hooks := workflowpostgres.RuntimeRepositoryHooks{}
	if len(guards) == 1 {
		hooks.CancellationSafety = guards[0]
	}
	return newWorkflowComponentsWithFactory(factory, cfg, hooks)
}

func newWorkflowComponentsWithFactory(
	factory *apiRuntimeRepositoryFactory,
	cfg config.Config,
	hooks workflowpostgres.RuntimeRepositoryHooks,
) (*workflowapplication.Service, *workflowpostgres.RuntimeRepository, error) {
	if factory == nil || factory.pool == nil {
		return nil, nil, errors.New("workflow runtime repository factory is unavailable")
	}
	runtimeRepository, err := factory.newRepository(hooks)
	if err != nil {
		return nil, nil, err
	}
	service, err := newWorkflowServiceWithRuntime(factory, cfg, runtimeRepository)
	if err != nil {
		return nil, nil, err
	}
	return service, runtimeRepository, nil
}

func newWorkflowServiceWithRuntime(
	factory *apiRuntimeRepositoryFactory,
	cfg config.Config,
	runtimeRepository *workflowpostgres.RuntimeRepository,
) (*workflowapplication.Service, error) {
	if factory == nil || factory.pool == nil || runtimeRepository == nil {
		return nil, errors.New("workflow runtime dependencies are unavailable")
	}
	legacyRepository, err := workflowpostgres.NewRepository(factory.pool)
	if err != nil {
		return nil, err
	}
	catalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		return nil, err
	}
	executors, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		return nil, err
	}
	if err := registerAPIWorkflowExecutors(cfg, executors); err != nil {
		return nil, err
	}
	if err := executors.Freeze(); err != nil {
		return nil, err
	}
	toolContracts, err := newToolContractRegistry()
	if err != nil {
		return nil, err
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors, toolContracts)
	if err != nil {
		return nil, err
	}
	if err := registerAPIWorkflowDefinitions(cfg, definitions); err != nil {
		return nil, err
	}
	if err := definitions.Freeze(); err != nil {
		return nil, err
	}
	service, err := workflowapplication.NewRuntimeService(legacyRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, workflowapplication.RuntimeDependencies{Definitions: definitions, Starter: runtimeRepository, State: runtimeRepository, Human: runtimeRepository})
	if err != nil {
		return nil, err
	}
	return service, nil
}

func newToolContractRegistry() (*toolsapplication.Registry, error) {
	return toolcatalog.NewFrozenContractRegistry()
}

func registerAPIWorkflowExecutors(cfg config.Config, executors *workflowapplication.ExecutorRegistry) error {
	if err := executors.Register(workflowapplication.CanonicalJSONHashNodeKind, workflowapplication.CanonicalJSONHashInputSchemaVersion, workflowapplication.NewCanonicalJSONHashExecutor()); err != nil {
		return err
	}
	if err := executors.RegisterContract(healthapplication.HealthScanNodeKind, healthapplication.HealthScanInputSchemaVersion); err != nil {
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
	healthDefinition, err := healthworkflow.RegisteredDefinition()
	if err != nil {
		return err
	}
	if err := definitions.Register(healthDefinition); err != nil {
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

// configureCollectionHealth 组装 Collection/Health 的真实 API seam；构造失败时由调用方显式保持路由不可用。
func configureCollectionHealth(database *postgres.Pool, cfg config.Config, collectionHandler **collectionhttp.Handler, healthHandler **healthhttp.Handler, runtime *workflowpostgres.RuntimeRepository) error {
	if database == nil || collectionHandler == nil || healthHandler == nil {
		return errors.New("collection and health composition dependencies are unavailable")
	}
	repository, err := collectionpostgres.NewRepository(database.DB())
	if err != nil {
		return err
	}
	service, err := collectionapplication.NewService(collectionapplication.Dependencies{Repository: repository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		return err
	}
	membership, err := healthcollection.NewMembership(service)
	if err != nil {
		return err
	}
	*collectionHandler = collectionhttp.NewHandler(service, cfg.GraphQueryTimeout)

	issueRepository, err := healthpostgres.NewSmartCollectionIssueRepository(database.DB(), membership, foundation.NewUUIDGenerator(nil))
	if err != nil {
		return err
	}
	readRepository, err := healthpostgres.NewReadRepository(database.DB())
	if err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	cursor, err := healthapplication.NewIssueCursorCodec(key)
	if err != nil {
		return err
	}
	readService, err := healthapplication.NewIssueReadService(readRepository, cursor)
	if err != nil {
		return err
	}

	factReader, err := healthpostgres.NewFactReader(database.DB(), membership)
	if err != nil {
		return err
	}
	registry, err := healthdetector.NewDefaultRegistry(factReader, healthdetector.DefaultConfig())
	if err != nil {
		return err
	}
	var scanService *healthapplication.ScanService
	var scheduleService *healthapplication.ScheduleService
	if runtime != nil {
		healthEvents, err := eventspostgres.NewStore(database.DB())
		if err != nil {
			return err
		}
		scanRepository, err := healthpostgres.NewScanRepository(database.DB(), runtime, healthEvents, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, healthcollection.DurableBindingVerifier{})
		if err != nil {
			return err
		}
		scanService, err = healthapplication.NewSmartCollectionScanService(scanRepository, scanRepository, registry, membership)
		if err != nil {
			return err
		}
		scheduleRepository, err := healthpostgres.NewScheduleRepository(database.DB(), foundation.NewUUIDGenerator(nil))
		if err != nil {
			return err
		}
		scheduleService, err = healthapplication.NewScheduleDispatcher(scheduleRepository, scanService, registry)
		if err != nil {
			return err
		}
	}
	*healthHandler = healthhttp.NewHandler(readService, scanService, scheduleService, registry, issueRepository)
	return nil
}
