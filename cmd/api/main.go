package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/app"
	artifactauthoring "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/authoring"
	artifactchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/changecontrol"
	artifactlearningpath "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/learningpath"
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
	authoringchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringpostgres "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/postgres"
	authoringapplication "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringhttp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	captureapplication "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturehttp "github.com/CodeZen-Lizhi/zhixu/internal/capture/http"
	approvaldispatchpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/approvaldispatchpostgres"
	changecontrolgitmerge "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/gitmerge"
	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolworkflowcancel "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/workflowcancel"
	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontrolhttp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/http"
	collectionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/collection/adapter/postgres"
	collectionapplication "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	collectionhttp "github.com/CodeZen-Lizhi/zhixu/internal/collection/http"
	conversationpostgres "github.com/CodeZen-Lizhi/zhixu/internal/conversation/adapter/postgres"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationhttp "github.com/CodeZen-Lizhi/zhixu/internal/conversation/http"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	documenthistorychangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/adapter/changecontrol"
	documenthistorypostgres "github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/adapter/postgres"
	documenthistoryapplication "github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/application"
	documenthistoryhttp "github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/http"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventshttp "github.com/CodeZen-Lizhi/zhixu/internal/events/http"
	exportauth "github.com/CodeZen-Lizhi/zhixu/internal/export/adapter/auth"
	exportcollection "github.com/CodeZen-Lizhi/zhixu/internal/export/adapter/collection"
	exportlocalfs "github.com/CodeZen-Lizhi/zhixu/internal/export/adapter/localfs"
	exportpostgres "github.com/CodeZen-Lizhi/zhixu/internal/export/adapter/postgres"
	exportriver "github.com/CodeZen-Lizhi/zhixu/internal/export/adapter/river"
	exportapplication "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	exporthttp "github.com/CodeZen-Lizhi/zhixu/internal/export/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	gitsyncpostgres "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/adapter/postgres"
	gitsyncsecurity "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/adapter/security"
	gitsyncapplication "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	gitsynchttp "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/http"
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
	memorypostgres "github.com/CodeZen-Lizhi/zhixu/internal/memory/adapter/postgres"
	memoryapplication "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	memoryhttp "github.com/CodeZen-Lizhi/zhixu/internal/memory/http"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	modelsettingshttp "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/http"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	organizingpostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/postgres"
	organizingapplication "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizinghttp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/http"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	platformparser "github.com/CodeZen-Lizhi/zhixu/internal/platform/parser"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	platformscheduler "github.com/CodeZen-Lizhi/zhixu/internal/platform/scheduler"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	retrievalworkspace "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/workspace"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievalhttp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/http"
	reviewpostgres "github.com/CodeZen-Lizhi/zhixu/internal/review/adapter/postgres"
	reviewapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	reviewhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/http"
	interviewartifact "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/adapter/artifact"
	interviewmemory "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/adapter/memory"
	interviewpostgres "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/adapter/postgres"
	interviewapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/http"
	learningpathartifact "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/adapter/artifact"
	learningpathpostgres "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/adapter/postgres"
	learningpathapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	learningpathhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/http"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/webassets"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	workflowhttp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/http"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	workspacehttp "github.com/CodeZen-Lizhi/zhixu/internal/workspace/http"
	workspaceruntimegrant "github.com/CodeZen-Lizhi/zhixu/internal/workspace/runtimegrant"
	"github.com/gin-gonic/gin"
)

const (
	apiReadTimeout            = 30 * time.Second
	apiReadHeaderTimeout      = 5 * time.Second
	apiIdleTimeout            = 60 * time.Second
	proposalMergeProbeTimeout = 6 * time.Second
)

func main() {
	os.Exit(runAPI())
}

// runAPI 组装并运行 API，返回退出码以保证资源清理 defer 在进程退出前执行。
func runAPI() (exitCode int) {
	gin.SetMode(gin.ReleaseMode)
	configPath := flag.String("config", "", "optional YAML configuration path")
	flag.Parse()

	logger := observability.NewLogger("info", os.Stderr)
	cfg, loadErr := config.Load(*configPath)
	if loadErr != nil {
		logger.Error("configuration is invalid", "error_code", "INVALID_CONFIGURATION")
		return 1
	}
	var shutdownContext context.Context
	var cancelShutdown context.CancelFunc
	beginShutdown := func() context.Context {
		if shutdownContext == nil {
			shutdownContext, cancelShutdown = context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		}
		return shutdownContext
	}
	defer func() {
		if cancelShutdown != nil {
			cancelShutdown()
		}
	}()
	telemetry, telemetryErr := initializeAPITelemetry(context.Background(), cfg)
	if telemetryErr != nil {
		logger.Error("telemetry initialization failed", "error_code", "TELEMETRY_EXPORTER_UNAVAILABLE")
		return 1
	}
	defer func() {
		if err := telemetry.Shutdown(beginShutdown()); err != nil {
			logger.Error("telemetry shutdown failed", "error_code", "SHUTDOWN_FAILED", "error", err)
			exitCode = 1
		}
	}()
	if telemetryStatus := telemetry.Status(); telemetryStatus.Degraded {
		logger.Warn("telemetry exporter is unavailable", "error_code", telemetryStatus.Code)
	}
	if err := observability.RecordProcessPresence(context.Background(), telemetry.Metrics()); err != nil {
		logger.Warn("API process presence metric failed", "error_code", "PROCESS_PRESENCE_METRIC_FAILED")
	}
	if err := observability.RecordTelemetryRequired(
		context.Background(), telemetry.Metrics(), telemetry.Status().Mode == observability.TelemetryModeRequired,
	); err != nil {
		logger.Warn("API telemetry mode metric failed", "error_code", "TELEMETRY_MODE_METRIC_FAILED")
	}
	modelTelemetry := platformmodels.NewModelTelemetry(telemetry.Tracer(), telemetry.Metrics())

	var database *postgres.Pool
	var databaseErr error
	modelBackgroundStopped := true
	consumersStopped := true
	databaseURL, databaseConfigErr := cfg.DatabaseConnectionString()
	if databaseConfigErr == nil && loadErr == nil {
		database, databaseErr = postgres.Open(context.Background(), databaseURL, cfg.DatabaseMaxConns, cfg.DatabaseMinConns)
		if databaseErr != nil {
			logger.Error("database pool is unavailable", "error_code", "DEPENDENCY_UNAVAILABLE")
		}
	}
	defer func() {
		// A shutdown timeout leaves resources owned by the unfinished task until process exit.
		if database != nil && modelBackgroundStopped && consumersStopped {
			database.Close()
		}
	}()

	producerGate := newAPIProducerGate()
	workspaceGate := newAPIWorkspaceRuntimeGate()
	var modelRuntimeController *modelsettingsruntime.HotRuntimeController[*modelsettingsruntime.Models]
	var activationCoordinator *modelsettingsruntime.ActivationCoordinator
	var modelRuntimeHost *modelsettingsruntime.RuntimeHost[*modelsettingsruntime.Models]
	var modelSettingsManager modelsettingsapplication.SettingsManager
	var modelActivationStarter modelsettingshttp.ActivationStarter
	var modelEnqueueFences []riveradapter.ScopedEnqueueFence
	var configuredModels *modelsettingsruntime.Models
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		if database == nil {
			logger.Error("managed model settings database is unavailable", "error_code", modelsettingsdomain.ErrorCodeUnavailable)
			if cfg.ModelSettingsRolloutID != "" {
				return 1
			}
		} else {
			bootstrap, bootstrapErr := modelsettingsruntime.BootstrapGORM(context.Background(), database, cfg, modelTelemetry)
			modelSettingsManager = bootstrap.Manager
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
					return 1
				}
			}
			if bootstrap.Service != nil && bootstrap.Loaded.Models != nil {
				instanceID, instanceErr := foundation.NewUUIDGenerator(nil).New()
				if instanceErr != nil {
					logger.Error("model runtime identity is unavailable", "error_code", modelsettingsdomain.ErrorCodeUnavailable)
					return 1
				}
				if bootstrap.LocalModelStore == nil {
					logger.Error("model generation lifecycle store is unavailable", "error_code", modelsettingsdomain.ErrorCodeUnavailable)
					return 1
				}
				generationLifecycle, lifecycleErr := modelsettingsruntime.NewGenerationLifecycle(
					bootstrap.LocalModelStore, modelsettingsdomain.RuntimeRoleAPI, instanceID,
				)
				if lifecycleErr != nil {
					logger.Error("model generation lifecycle is unavailable", "error_code", modelsettingsdomain.ErrorCodeUnavailable)
					return 1
				}
				modelRuntimeHost, bootstrapErr = modelsettingsruntime.NewManagedModelsHost(modelsettingsruntime.ManagedModelsHostOptions{
					Base: cfg, Revisions: bootstrap.Service, Role: modelsettingsdomain.RuntimeRoleAPI,
					InstanceID: instanceID, Loaded: bootstrap.Loaded,
					Lifecycle: generationLifecycle, Telemetry: modelTelemetry,
				})
				if bootstrapErr != nil {
					logger.Error("model runtime host is unavailable", "error_code", modelsettingsdomain.ErrorCodeUnavailable)
					return 1
				}
				defer func() {
					if modelBackgroundStopped && consumersStopped {
						modelRuntimeHost.Close()
					}
				}()
				modelRuntimeController, bootstrapErr = modelsettingsruntime.NewHotRuntimeController(
					modelsettingsruntime.HotRuntimeControllerOptions[*modelsettingsruntime.Models]{
						Host: modelRuntimeHost, Activation: bootstrap.Service, Revisions: bootstrap.Service,
						Runtime: bootstrap.Service, InitialPhase: bootstrap.Loaded.InitialPhase,
					},
				)
				if bootstrapErr != nil {
					logger.Error("model runtime controller is unavailable", "error_code", modelsettingsdomain.ErrorCodeUnavailable)
					return 1
				}
				activationCoordinator, bootstrapErr = modelsettingsruntime.NewActivationCoordinator(
					modelsettingsruntime.ActivationCoordinatorOptions{Control: bootstrap.Service, Revisions: bootstrap.Service},
				)
				if bootstrapErr != nil {
					logger.Error("model activation coordinator is unavailable", "error_code", modelsettingsdomain.ErrorCodeUnavailable)
					return 1
				}
				modelActivationStarter = bootstrap.Service
			}
		}
	} else {
		loaded, modelsErr := modelsettingsruntime.LoadSettings(context.Background(), cfg, nil, modelTelemetry)
		if modelsErr != nil {
			logger.Error("static model runtime is unavailable", "error_code", modelsettingsdomain.ErrorCodeUnavailable)
			return 1
		}
		configuredModels = loaded.Models
	}
	if configuredModels == nil {
		disabledModels, disabledErr := modelsettingsruntime.Build(cfg, modelsettingsdomain.ResolvedSettings{Settings: modelsettingsdomain.CanonicalDisabledSettings()}, modelTelemetry)
		if disabledErr != nil {
			logger.Error("disabled model runtime is unavailable", "error_code", modelsettingsdomain.ErrorCodeUnavailable)
			return 1
		}
		configuredModels = disabledModels
	}
	if modelRuntimeHost == nil {
		defer func() {
			if consumersStopped {
				_ = configuredModels.Close()
			}
		}()
	}
	cfg = modelsettingsruntime.WithoutModelCredentials(cfg)
	fixedSearchEmbedder := configuredModels.Embedding().Embedder()
	var searchEmbeddingAcquirers []retrievalapplication.CompatibleEmbeddingAcquirer
	if modelRuntimeHost != nil {
		fixedSearchEmbedder = nil
		acquirer, acquirerErr := newAPIEmbeddingRuntimeAcquirer(modelRuntimeHost)
		if acquirerErr != nil {
			logger.Error("model embedding runtime is unavailable", "error_code", modelsettingsdomain.ErrorCodeUnavailable)
			return 1
		}
		searchEmbeddingAcquirers = append(searchEmbeddingAcquirers, acquirer)
	}

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
			gormDB, repositoryErr := database.GORM()
			var authRepository *authpostgres.GORMRepository
			if repositoryErr == nil {
				authRepository, repositoryErr = authpostgres.NewGORMRepository(gormDB)
			}
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
	var modelSettingsHandler *modelsettingshttp.Handler
	if modelSettingsManager != nil {
		configuredHandler, handlerErr := modelsettingshttp.NewHandler(
			modelSettingsManager,
			modelsettingshttp.Options{RequireSession: authRequired, ActivationStarter: modelActivationStarter},
		)
		if handlerErr != nil {
			logger.Error("model settings HTTP service is unavailable", "error_code", modelsettingsdomain.ErrorCodeUnavailable)
		} else {
			modelSettingsHandler = configuredHandler
		}
	}

	workspaceHandler := workspacehttp.NewHandler(nil)
	collectionHandler := collectionhttp.NewHandler(nil, cfg.GraphQueryTimeout)
	healthHandler := healthhttp.NewHandler(nil, nil, nil, nil, nil)
	workflowHandler := workflowhttp.NewHandler(nil)
	changeControlHandler := changecontrolhttp.NewHandlerWithTimeout(nil, cfg.GraphQueryTimeout)
	ingestionHandler := ingestionhttp.NewHandler(nil)
	retrievalHandler := retrievalhttp.NewHandler(nil, nil, nil)
	graphHandler := graphhttp.NewHandler(nil, cfg.GraphQueryTimeout)
	candidateHandler := graphhttp.NewCandidateHandler(nil, cfg.GraphQueryTimeout)
	conversationHandler := conversationhttp.NewHandler(nil, conversationhttp.NewCursorCodec())
	draftStreamHandler := conversationhttp.NewDraftStreamHandler(nil)
	eventsHandler := eventshttp.NewHandler(nil)
	exportHandler := exporthttp.NewHandler(nil)
	reviewHandler := reviewhttp.NewHandler(nil, cfg.GraphQueryTimeout)
	memoryHandler := memoryhttp.NewHandler(nil, cfg.GraphQueryTimeout)
	interviewHandler := interviewhttp.NewHandler(nil, cfg.GraphQueryTimeout)
	learningPathHandler := learningpathhttp.NewHandler(nil, cfg.GraphQueryTimeout)
	knowledgeHandler := knowledgehttp.NewHandler(nil, nil, cfg.GraphQueryTimeout)
	artifactHandler := artifacthttp.NewHandler(nil, nil, cfg.GraphQueryTimeout)
	authoringHandler := authoringhttp.NewHandler(nil, cfg.GraphQueryTimeout)
	captureHandler := capturehttp.NewHandler(nil, 0)
	organizingHandler := organizinghttp.NewHandler(nil, nil, nil, cfg.GraphQueryTimeout)
	synthesisHandler := organizinghttp.NewSynthesisHandler(nil, nil, cfg.GraphQueryTimeout)
	synthesisInterviewHandler := interviewhttp.NewNotePreparationHandler(nil)
	documentHistoryHandler := documenthistoryhttp.NewHandler(nil, cfg.GraphQueryTimeout)
	gitSyncHandler := gitsynchttp.NewHandler(nil, cfg.GraphQueryTimeout)
	var ragInitErr error
	if database == nil {
		ragInitErr = errors.New("RAG database dependency is unavailable")
	}
	var changeControlService *changecontrolapplication.Service
	var artifactGeneration *artifactpostgres.GORMSectionGenerationRepository
	artifactIDs := foundation.NewUUIDGenerator(nil)
	artifactClock := foundation.SystemClock{}
	fileScanner := filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}}
	var workspaceRuntime *workspaceruntimegrant.GORMProcessComposition
	if database != nil {
		workspaceRuntime, databaseErr = workspaceruntimegrant.NewGORMProcessComposition(
			context.Background(), database, os.LookupEnv, workspacedomain.RuntimeRoleAPI,
		)
		if databaseErr != nil {
			logger.Error("workspace root grant is unavailable", "error_code", "WORKSPACE_ROOT_GRANT_UNAVAILABLE")
			return 1
		}
		defer func() {
			if !consumersStopped || !modelBackgroundStopped {
				return
			}
			if err := workspaceRuntime.Close(beginShutdown()); err != nil {
				consumersStopped = false
				exitCode = 1
				logger.Error("workspace runtime shutdown failed", "error_code", "SHUTDOWN_FAILED", "error", err)
			}
		}()
		if err := workspaceRuntime.SetQuiescenceHooks(workspaceGate.Hooks()); err != nil {
			logger.Error("workspace runtime quiescence is unavailable", "error_code", "WORKSPACE_QUIESCENCE_UNAVAILABLE")
			return 1
		}
	}
	if database != nil {
		configuredKnowledgeHandler, knowledgeHandlerErr := newKnowledgeHandler(database, cfg.GraphQueryTimeout)
		if knowledgeHandlerErr != nil {
			logger.Error("knowledge timeline and impact services are unavailable", "error_code", "KNOWLEDGE_TIMELINE_IMPACT_UNAVAILABLE")
		} else {
			knowledgeHandler = configuredKnowledgeHandler
		}
		configuredReviewHandler, reviewHandlerErr := newReviewHandler(database, cfg.GraphQueryTimeout, cfg.ReviewQuestionRefKey)
		if reviewHandlerErr != nil {
			logger.Error("review service is unavailable", "error_code", "REVIEW_DEPENDENCY_UNAVAILABLE")
		} else {
			reviewHandler = configuredReviewHandler
		}
		configuredMemoryHandler, memoryHandlerErr := newMemoryHandler(database, cfg.GraphQueryTimeout)
		if memoryHandlerErr != nil {
			logger.Error("memory service is unavailable", "error_code", "MEMORY_DEPENDENCY_UNAVAILABLE")
		} else {
			memoryHandler = configuredMemoryHandler
		}
		if err := configureCollectionHealth(database, cfg, &collectionHandler, &healthHandler, nil); err != nil {
			logger.Error("collection and health services are unavailable", "error_code", "COLLECTION_HEALTH_DEPENDENCY_UNAVAILABLE")
		}
		configuredGraphHandler, graphHandlerErr := newGraphHandler(database, cfg.GraphQueryTimeout)
		if graphHandlerErr != nil {
			logger.Error("graph query service is unavailable", "error_code", "GRAPH_DEPENDENCY_UNAVAILABLE")
		} else {
			graphHandler = configuredGraphHandler
		}
		workspaceRepository := workspaceRuntime.Repository
		var repositoryErr error
		healthEvents, healthEventsErr := eventspostgres.NewGORMStore(database)
		changeControlRepository, changeControlRepositoryErr := changecontrolpostgres.NewGORMRepository(database, healthEvents)
		var workflowService *workflowapplication.Service
		var workflowControlService *workflowapplication.Service
		var workflowRuntime *workflowpostgres.GORMRuntimeRepository
		var synthesisRuntime *apiSynthesisRuntimeComponents
		var workspaceAnalysisRuntimeHooks *apiWorkspaceAnalysisRuntimeHooks
		if changeControlRepositoryErr != nil {
			logger.Error("change control repository is unavailable", "error_code", "CHANGE_CONTROL_DATABASE_UNAVAILABLE")
		} else if healthEventsErr != nil {
			logger.Error("health event store is unavailable", "error_code", "HEALTH_EVENT_STORE_UNAVAILABLE")
		} else {
			if cfg.WorkspaceAnalysisAPIEnabled {
				configuredHooks, hooksErr := newAPIWorkspaceAnalysisRuntimeHooks(database, healthEvents)
				if hooksErr != nil {
					logger.Warn("workspace analysis API runtime hooks are unavailable", "error_code", "WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE")
				} else {
					workspaceAnalysisRuntimeHooks = configuredHooks
				}
			}
			healthCancellationGuard, healthCancellationGuardErr := healthpostgres.NewGORMScanCancellationGuard(database, healthEvents)
			graphCancellationGuard, graphCancellationGuardErr := graphpostgres.NewGORMRepository(database)
			cancellationGuard, cancellationGuardErr := workflowapplication.NewCompositeScopedCancellationSafetyGuard(
				changeControlRepository,
				graphCancellationGuard,
				healthCancellationGuard,
			)
			cancellationGuardErr = firstError(healthCancellationGuardErr, graphCancellationGuardErr, cancellationGuardErr)
			if cancellationGuardErr != nil {
				logger.Error("workflow cancellation guard is unavailable", "error_code", "WORKFLOW_CANCELLATION_GUARD_UNAVAILABLE")
			}
			var runtime *workflowpostgres.GORMRuntimeRepository
			var workflowServiceErr error
			if cancellationGuardErr != nil {
				workflowServiceErr = cancellationGuardErr
			} else if repositoryErr != nil {
				workflowServiceErr = repositoryErr
			} else {
				factory, factoryErr := newAPIRuntimeRepositoryFactory(database, cfg, modelEnqueueFences...)
				if factoryErr != nil {
					workflowServiceErr = factoryErr
				} else {
					synthesisRuntime = factory.synthesis
					workflowService, runtime, artifactGeneration, workflowServiceErr = newAPIArtifactWorkflowComponentsWithFactory(
						factory, workspaceRepository, fileScanner, cancellationGuard, artifactIDs, artifactClock,
						workspaceAnalysisRuntimeHooks,
					)
				}
			}
			if workflowServiceErr != nil {
				logger.Error("workflow service is unavailable", "error_code", "WORKFLOW_SERVICE_UNAVAILABLE")
				ragInitErr = firstError(ragInitErr, workflowServiceErr)
			} else {
				workflowRuntime = runtime
				workflowControlService = workflowService
				humanReviewProjector, humanReviewErr := newOrganizingHumanTaskReviewProjector(database, workflowRuntime)
				if humanReviewErr != nil {
					logger.Error("workflow human review projector is unavailable", "error_code", "ORGANIZING_HUMAN_REVIEW_UNAVAILABLE")
					workflowService = nil
				} else {
					workflowHandler = workflowhttp.NewHandler(workflowService, humanReviewProjector)
				}
				if err := configureCollectionHealth(database, cfg, &collectionHandler, &healthHandler, workflowRuntime); err != nil {
					logger.Error("collection and health workflow services are unavailable", "error_code", "COLLECTION_HEALTH_WORKFLOW_DEPENDENCY_UNAVAILABLE")
				}
			}
		}
		if repositoryErr != nil {
			logger.Error("workspace repository is unavailable", "error_code", "WORKSPACE_DATABASE_UNAVAILABLE")
			ragInitErr = firstError(ragInitErr, errors.New("RAG workspace dependency is unavailable"))
		} else {
			workspaceService := workspaceapplication.NewService(workspaceapplication.Dependencies{
				Repository:     workspaceRepository,
				Files:          fileScanner,
				ManagedFiles:   fileScanner,
				Git:            gitcli.New(""),
				GitInitializer: gitcli.New(""),
				IDs:            foundation.NewUUIDGenerator(nil),
				Clock:          foundation.SystemClock{},
			})
			workspaceHandler = workspacehttp.NewHandler(workspaceService)
			configuredGitSyncHandler, gitSyncHandlerErr := newGitSyncHandler(
				database, workspaceRepository, cfg.GitSyncKeyFile, cfg.GraphQueryTimeout,
			)
			if gitSyncHandlerErr != nil {
				logger.Warn("Git sync capability is unavailable", "error_code", "GIT_SYNC_CAPABILITY_UNAVAILABLE")
			} else {
				gitSyncHandler = configuredGitSyncHandler
			}
			configuredCaptureHandler, captureHandlerErr := newCaptureHandler(database, workspaceService, workspaceRepository, 0)
			if captureHandlerErr != nil {
				logger.Error("capture service is unavailable", "error_code", "CAPTURE_SERVICE_UNAVAILABLE")
			} else {
				captureHandler = configuredCaptureHandler
			}
			configuredInterviewHandler, interviewHandlerErr := newInterviewHandler(database, workspaceRepository, fileScanner, cfg.GraphQueryTimeout)
			if interviewHandlerErr != nil {
				logger.Error("interview service is unavailable", "error_code", "INTERVIEW_DEPENDENCY_UNAVAILABLE")
			} else {
				interviewHandler = configuredInterviewHandler
			}
			configuredLearningPathHandler, learningPathHandlerErr := newLearningPathHandler(database, workspaceRepository, fileScanner, cfg.GraphQueryTimeout)
			if learningPathHandlerErr != nil {
				logger.Error("learning path service is unavailable", "error_code", "LEARNING_PATH_DEPENDENCY_UNAVAILABLE")
			} else {
				learningPathHandler = configuredLearningPathHandler
			}
			configuredExportHandler, exportHandlerErr := newExportHandler(database, cfg, workspaceRepository, modelEnqueueFences...)
			if exportHandlerErr != nil {
				logger.Error("export service is unavailable", "error_code", "EXPORT_DEPENDENCY_UNAVAILABLE")
			} else {
				exportHandler = configuredExportHandler
			}
			configuredRetrievalHandler, retrievalHandlerErr := newRetrievalHandler(
				database, fixedSearchEmbedder, workspaceRepository, fileScanner, searchEmbeddingAcquirers...,
			)
			if retrievalHandlerErr != nil {
				logger.Error("retrieval search service is unavailable", "error_code", "RETRIEVAL_SEARCH_SERVICE_UNAVAILABLE")
				ragInitErr = firstError(ragInitErr, retrievalHandlerErr)
			} else {
				retrievalHandler = configuredRetrievalHandler
			}

			sourceReadyOutbox, ingestionRepositoryErr := workflowpostgres.NewGORMSourceReadyOutbox(database)
			var ingestionRepository *ingestionpostgres.GORMRepository
			if ingestionRepositoryErr == nil {
				ingestionRepository, ingestionRepositoryErr = ingestionpostgres.NewGORMRepositoryWithSourceReady(database, sourceReadyOutbox)
			}
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
				dispatchRepository, dispatchRepositoryErr := approvaldispatchpostgres.NewGORMApprovalDispatchRepository(database, workflowRuntime, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, healthEvents)
				if dispatchRepositoryErr != nil {
					logger.Error("approval dispatch repository is unavailable", "error_code", "APPROVAL_DISPATCH_DEPENDENCY_MISSING")
				} else {
					knowledgeRelationApplier, knowledgeRelationApplierErr := knowledgepostgres.NewGORMApprovedRelationApplyRepository(database, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, healthEvents)
					if knowledgeRelationApplierErr != nil {
						logger.Error("knowledge relation apply service is unavailable", "error_code", "KNOWLEDGE_RELATION_APPLIER_UNAVAILABLE")
					}
					configuredChangeControlService, changeControlServiceErr := changecontrolapplication.NewServiceWithDispatch(changeControlRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, targetReader, approvalGitInspector, dispatchRepository, knowledgeRelationApplier)
					if changeControlServiceErr != nil {
						logger.Error("change control service is unavailable", "error_code", "CHANGE_CONTROL_SERVICE_UNAVAILABLE")
					} else {
						workflowCanceller, workflowCancellerErr := changecontrolworkflowcancel.New(workflowControlService)
						if workflowCancellerErr != nil {
							logger.Error("proposal revision workflow cancellation is unavailable", "error_code", "PROPOSAL_REVISION_WORKFLOW_CANCEL_UNAVAILABLE")
						} else {
							configuredChangeControlService.SetRevisionWorkflowCanceller(workflowCanceller)
						}
						mergeEngine, mergeEngineErr := changecontrolgitmerge.New(gitcli.New(""))
						if mergeEngineErr == nil {
							probeContext, cancelProbe := context.WithTimeout(context.Background(), proposalMergeProbeTimeout)
							mergeEngineErr = mergeEngine.Probe(probeContext)
							cancelProbe()
						}
						if mergeEngineErr != nil {
							logger.Error("proposal revision merge engine is unavailable", "error_code", "PROPOSAL_MERGE_ENGINE_UNAVAILABLE")
						} else {
							configuredChangeControlService.SetRevisionMergeEngine(mergeEngine)
						}
						changeControlService = configuredChangeControlService
						changeControlHandler = changecontrolhttp.NewHandlerWithTimeout(changeControlService, cfg.GraphQueryTimeout)
					}
				}
			}
			if changeControlService != nil {
				configuredDocumentHistoryHandler, documentHistoryErr := newDocumentHistoryHandler(
					database, workspaceRepository, changeControlService, changeControlRepository, cfg.GraphQueryTimeout,
				)
				if documentHistoryErr != nil {
					logger.Error("document history service is unavailable", "error_code", "DOCUMENT_HISTORY_DEPENDENCY_UNAVAILABLE")
				} else {
					documentHistoryHandler = configuredDocumentHistoryHandler
				}
			}
			configuredAuthoringService, authoringHandlerErr := newAuthoringService(
				database, changeControlService, targetReader,
			)
			if authoringHandlerErr != nil {
				logger.Error("authoring service is unavailable", "error_code", "AUTHORING_DEPENDENCY_UNAVAILABLE")
			} else {
				configuredSynthesis, synthesisErr := newAPISynthesisComponents(synthesisRuntime, apiSynthesisDependencies{
					Workspaces: workspaceRepository, Files: fileScanner, Publications: configuredAuthoringService,
					Retirements: changeControlRepository, Runtime: workflowRuntime, Timeout: cfg.GraphQueryTimeout,
				})
				if synthesisErr != nil {
					logger.Error("synthesis note services are unavailable", "error_code", "SYNTHESIS_COMPOSITION_UNAVAILABLE")
				} else {
					synthesisHandler = configuredSynthesis.handler
					synthesisInterviewHandler = configuredSynthesis.interviewHandler
				}
				configuredOrganizingHandler, organizingHandlerErr := newOrganizingHandler(
					context.Background(), database, fixedSearchEmbedder, workspaceRepository,
					fileScanner, configuredAuthoringService, workflowService, cfg.GraphQueryTimeout,
					searchEmbeddingAcquirers...,
				)
				if organizingHandlerErr != nil {
					logger.Error("organizing service is unavailable", "error_code", "ORGANIZING_DEPENDENCY_UNAVAILABLE")
				} else {
					organizingHandler = configuredOrganizingHandler
					availableAuthoringService, availabilityErr := configuredAuthoringService.WithOrganizingAvailability(authoringapplication.OrganizingAvailability{
						Available: true, Href: "/authoring/organize",
					})
					if availabilityErr != nil {
						logger.Error("organizing overview capability is unavailable", "error_code", "ORGANIZING_OVERVIEW_UNAVAILABLE")
					} else {
						configuredAuthoringService = availableAuthoringService
					}
				}
				authoringHandler = authoringhttp.NewHandler(configuredAuthoringService, cfg.GraphQueryTimeout)
			}
			generation := []artifactapplication.SectionGenerationStarter(nil)
			if artifactGeneration != nil {
				generation = artifactGenerationDependencies(artifactGeneration)
			}
			configuredArtifactHandler, artifactHandlerErr := newArtifactHandlerWithDependencies(
				database, workspaceRepository, fileScanner, changeControlService, cfg.GraphQueryTimeout,
				artifactIDs, artifactClock, generation...,
			)
			if artifactHandlerErr != nil {
				logger.Error("artifact service is unavailable", "error_code", "ARTIFACT_DEPENDENCY_UNAVAILABLE")
			} else {
				artifactHandler = configuredArtifactHandler
			}
		}
		configuredCandidateHandler, candidateHandlerErr := newCandidateHandler(database, workflowRuntime, cfg.GraphQueryTimeout)
		if candidateHandlerErr != nil {
			logger.Error("semantic link candidate service is unavailable", "error_code", "SEMANTIC_LINK_DEPENDENCY_UNAVAILABLE")
		} else {
			candidateHandler = configuredCandidateHandler
		}
		dispatchReady := questionDispatchEnabled(true, ragInitErr)
		var workspaceAnalysisStarters []agentapplication.ScopedWorkspaceAnalysisRunStarter
		if cfg.WorkspaceAnalysisAPIEnabled && dispatchReady && workspaceAnalysisRuntimeHooks != nil {
			starter, starterErr := newAPIWorkspaceAnalysisRunStarter(database, cfg, configuredModels, modelRuntimeHost)
			if starterErr != nil {
				logger.Warn("workspace analysis API capability is unavailable", "error_code", "WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE")
			} else {
				workspaceAnalysisStarters = append(workspaceAnalysisStarters, starter)
			}
		}
		configuredConversation, configuredEvents, conversationErr := newConversationHandlers(
			database, workflowRuntime, dispatchReady, workspaceAnalysisStarters...,
		)
		if conversationErr != nil {
			logger.Error("conversation service is unavailable", "error_code", "CONVERSATION_SERVICE_UNAVAILABLE")
			ragInitErr = firstError(ragInitErr, conversationErr)
		} else {
			conversationHandler = configuredConversation
			eventsHandler = configuredEvents
		}
		configuredDraftStream, draftStreamErr := newDraftStreamHandler(database)
		if draftStreamErr != nil {
			logger.Error("answer draft stream is unavailable", "error_code", conversationhttp.ErrorCodeDraftStreamUnavailable)
		} else {
			draftStreamHandler = configuredDraftStream
		}
	}

	deps := app.Dependencies{
		Version:            cfg.Version,
		Database:           database,
		DatabaseConfigErr:  firstError(loadErr, databaseConfigErr),
		DatabaseInitErr:    databaseErr,
		PingTimeout:        cfg.DatabasePingTimeout,
		Static:             static,
		Workspace:          workspaceHandler,
		Collection:         collectionHandler,
		Health:             healthHandler,
		Workflow:           workflowHandler,
		ChangeControl:      changeControlHandler,
		Ingestion:          ingestionHandler,
		Retrieval:          retrievalHandler,
		Conversation:       conversationHandler,
		DraftStream:        draftStreamHandler,
		Events:             eventsHandler,
		Export:             exportHandler,
		Review:             reviewHandler,
		LearningPath:       learningPathHandler,
		Memory:             memoryHandler,
		Interview:          interviewHandler,
		Knowledge:          knowledgeHandler,
		Artifact:           artifactHandler,
		Authoring:          authoringHandler,
		Capture:            captureHandler,
		Organizing:         organizingHandler,
		Synthesis:          synthesisHandler,
		SynthesisInterview: synthesisInterviewHandler,
		DocumentHistory:    documentHistoryHandler,
		GitSync:            gitSyncHandler,
		ModelSettings:      modelSettingsHandler,
		Auth:               authHandler,
		AuthRequired:       authRequired,
		AuthInitErr:        authInitErr,
		AuthCheck:          authCheck,
		Graph:              graphHandler,
		Candidate:          candidateHandler,
		RAGEnabled:         true,
		RAGInitErr:         ragInitErr,
		Logger:             logger,
		Tracer:             telemetry.Tracer(),
		MetricsHandler:     telemetry.MetricsHandler(),
	}
	server := newAPIServer(cfg.HTTPAddr, workspaceGate.Wrap(producerGate.Wrap(app.NewRouter(deps))))
	stop, stopCancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopCancel()
	modelRuntimeContext, cancelModelRuntime := context.WithCancel(context.Background())
	var modelRuntimeErr <-chan error
	var activationCoordinatorErr <-chan error
	var modelRuntimeStopped <-chan struct{}
	var activationCoordinatorStopped <-chan struct{}
	var workspaceRuntimeErr <-chan error
	defer func() {
		// HTTP Close cancels connections but does not join unfinished handlers.
		// Keep their runtime, Workspace anchor and Pool alive until process exit.
		if !consumersStopped {
			exitCode = 1
			return
		}
		cancelModelRuntime()
		if err := waitAPIModelRuntime(beginShutdown(), modelRuntimeStopped, activationCoordinatorStopped); err != nil {
			logger.Error("model runtime shutdown failed", "error_code", "SHUTDOWN_FAILED", "error", err)
			exitCode = 1
		} else {
			modelBackgroundStopped = true
		}
		select {
		case err := <-modelRuntimeErr:
			logger.Error("model runtime stopped during shutdown", "error_code", modelsettingsdomain.ErrorCodeRuntimeConflict, "error", err)
			exitCode = 1
		default:
		}
		select {
		case err := <-activationCoordinatorErr:
			logger.Error("model activation coordinator stopped during shutdown", "error_code", modelsettingsdomain.ErrorCodeActivationConflict, "error", err)
			exitCode = 1
		default:
		}
	}()
	if workspaceRuntime != nil {
		workspaceRuntimeErr = workspaceRuntime.Errors()
	}
	if modelRuntimeController != nil {
		modelBackgroundStopped = false
		modelRuntimeErr, modelRuntimeStopped = startAPIModelRuntime(modelRuntimeContext, modelRuntimeController)
		select {
		case <-modelRuntimeController.Active():
		case err := <-modelRuntimeErr:
			logger.Error("model runtime registration failed", "error_code", modelsettingsdomain.ErrorCodeRuntimeConflict, "error", err)
			return 1
		case <-stop.Done():
			return 0
		}
		if activationCoordinator == nil {
			logger.Error("model activation coordinator is unavailable", "error_code", modelsettingsdomain.ErrorCodeUnavailable)
			return 1
		}
		activationCoordinatorErr, activationCoordinatorStopped = startAPIActivationCoordinator(modelRuntimeContext, activationCoordinator)
	}

	serverErr := make(chan error, 1)
	consumersStopped = false
	go func() {
		logger.Info("api server starting", "addr", cfg.HTTPAddr, "version", cfg.Version)
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		logger.Error("api server stopped unexpectedly", "error_code", "SERVER_FAILED", "error", err)
		exitCode = 1
	case err := <-modelRuntimeErr:
		logger.Error("model runtime ownership was lost", "error_code", modelsettingsdomain.ErrorCodeRuntimeConflict, "error", err)
		exitCode = 1
	case err := <-activationCoordinatorErr:
		logger.Error("model activation coordinator stopped", "error_code", modelsettingsdomain.ErrorCodeActivationConflict, "error", err)
		exitCode = 1
	case err := <-workspaceRuntimeErr:
		logger.Error("workspace root grant ownership was lost", "error_code", "WORKSPACE_GRANT_STALE", "error", err)
		exitCode = 1
	case <-stop.Done():
	}
	if err := server.Shutdown(beginShutdown()); err != nil {
		_ = server.Close()
		logger.Error("api server shutdown failed", "error_code", "SHUTDOWN_FAILED", "error", err)
		if exitCode == 0 {
			exitCode = 1
		}
	} else {
		consumersStopped = true
	}
	return exitCode
}

// initializeAPITelemetry 按 API 配置构造 telemetry，并保留 disabled、optional、required 的统一语义。
func initializeAPITelemetry(ctx context.Context, cfg config.Config) (*observability.Telemetry, error) {
	return observability.InitializeTelemetry(ctx, observability.TelemetryOptions{
		Mode:           observability.TelemetryMode(cfg.TelemetryMode),
		Endpoint:       cfg.TelemetryEndpoint,
		ServiceName:    cfg.AppName + "-api",
		ServiceVersion: cfg.Version,
		Environment:    cfg.Environment,
	})
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

// newAuthoringHandler 组装持久草稿、Change Control Proposal 与受控目标读取边界。
func newAuthoringHandler(
	pool *postgres.Pool,
	proposals authoringchangecontrol.ProposalService,
	targets authoringchangecontrol.TargetReader,
	timeout time.Duration,
) (*authoringhttp.Handler, error) {
	service, err := newAuthoringService(pool, proposals, targets)
	if err != nil {
		return nil, err
	}
	return authoringhttp.NewHandler(service, timeout), nil
}

// newAuthoringService 组装可供 Authoring HTTP 与 Organizing owner bridge 复用的应用服务。
func newAuthoringService(
	pool *postgres.Pool,
	proposals authoringchangecontrol.ProposalService,
	targets authoringchangecontrol.TargetReader,
) (*authoringapplication.Service, error) {
	if pool == nil || apiCompositionNilDependency(proposals) || apiCompositionNilDependency(targets) {
		return nil, errors.New("authoring database, proposal service and target reader are required")
	}
	repository, err := authoringpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	proposalCreator, err := authoringchangecontrol.NewProposalCreator(proposals, targets)
	if err != nil {
		return nil, err
	}
	service, err := authoringapplication.NewService(authoringapplication.Dependencies{
		Repository: repository, Proposals: proposalCreator,
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return nil, err
	}
	return service, nil
}

// newDocumentHistoryHandler 组装文档投影、只读 Git 历史与强类型恢复 Proposal 边界。
func newDocumentHistoryHandler(
	pool *postgres.Pool,
	workspaces gitcli.WorkspaceRepository,
	proposals *changecontrolapplication.Service,
	proposalLookup *changecontrolpostgres.GORMRepository,
	timeout time.Duration,
) (*documenthistoryhttp.Handler, error) {
	if pool == nil || workspaces == nil || proposals == nil || proposalLookup == nil {
		return nil, errors.New("document history database, workspace and proposal services are required")
	}
	gormDB, err := pool.GORM()
	if err != nil {
		return nil, err
	}
	documents, err := documenthistorypostgres.NewGORMRepository(gormDB)
	if err != nil {
		return nil, err
	}
	history, err := gitcli.NewHistoryClient(gitcli.New(""), workspaces)
	if err != nil {
		return nil, err
	}
	gateway, err := documenthistorychangecontrol.NewGateway(proposals, proposalLookup)
	if err != nil {
		return nil, err
	}
	cursors, err := documenthistoryapplication.NewRandomCursorCodec()
	if err != nil {
		return nil, err
	}
	service, err := documenthistoryapplication.NewService(documenthistoryapplication.Dependencies{
		Documents: documents,
		Git:       history,
		Proposals: gateway,
		Lookup:    gateway,
		Cursors:   cursors,
	})
	if err != nil {
		return nil, err
	}
	return documenthistoryhttp.NewHandler(service, timeout), nil
}

// newGitSyncHandler composes the production-only Git Remote configuration and
// run service. The caller retains a fail-closed Handler when this returns an error.
func newGitSyncHandler(
	pool *postgres.Pool,
	workspaces gitcli.WorkspaceRepository,
	keyFile string,
	timeout time.Duration,
) (*gitsynchttp.Handler, error) {
	if pool == nil || workspaces == nil || keyFile == "" {
		return nil, errors.New("Git sync database, workspace repository and key file are required")
	}
	sealer, err := gitsyncsecurity.NewCredentialSealerFromFile(keyFile)
	if err != nil {
		return nil, err
	}
	gormDB, err := pool.GORM()
	if err != nil {
		return nil, err
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	repository, err := gitsyncpostgres.NewGORMRepository(gormDB, unitOfWork, sealer)
	if err != nil {
		return nil, err
	}
	policy := gitsyncsecurity.NewURLPolicy(net.DefaultResolver)
	remote, err := gitcli.NewRemoteClient(gitcli.New(""), workspaces, policy)
	if err != nil {
		return nil, err
	}
	cursors, err := gitsyncapplication.NewRandomCursorCodec()
	if err != nil {
		return nil, err
	}
	service, err := gitsyncapplication.NewService(
		repository, repository, policy, remote, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, cursors,
	)
	if err != nil {
		return nil, err
	}
	return gitsynchttp.NewHandler(service, timeout), nil
}

// newOrganizingHandler 通过公开 owner 读取服务组装 Draft、Template、Snapshot 与 Run 投影。
func newOrganizingHandler(
	ctx context.Context,
	pool *postgres.Pool,
	embedder retrievalapplication.Embedder,
	workspaceRepository workspacedomain.SourceMaterialRepository,
	files workspacedomain.FileScanner,
	authoring organizingowner.AuthoringReader,
	workflows *workflowapplication.Service,
	timeout time.Duration,
	embeddingAcquirers ...retrievalapplication.CompatibleEmbeddingAcquirer,
) (*organizinghttp.Handler, error) {
	if ctx == nil || pool == nil || workspaceRepository == nil || files == nil || authoring == nil || workflows == nil {
		return nil, errors.New("organizing database, owner services and workflow reader are required")
	}
	searchRepository, err := retrievalpostgres.NewGORMSearchRepository(pool)
	if err != nil {
		return nil, err
	}
	artifactReader, err := retrievalworkspace.NewReader(workspaceRepository, files)
	if err != nil {
		return nil, err
	}
	evidence, err := retrievalapplication.NewEvidenceReferenceService(searchRepository, artifactReader)
	if err != nil {
		return nil, err
	}
	search, err := newAPISearchService(searchRepository, embedder, nil, embeddingAcquirers...)
	if err != nil {
		return nil, err
	}
	modelRuns, err := agentpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	profiles, err := capturepostgres.NewGORMProfileRepository(pool, modelRuns)
	if err != nil {
		return nil, err
	}
	knowledgeRepository, err := knowledgepostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	claims, err := knowledgeapplication.NewClaimQueryService(knowledgeRepository)
	if err != nil {
		return nil, err
	}
	claimSearch, err := graphpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	collectionRepository, err := collectionpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	collections, err := collectionapplication.NewService(collectionapplication.Dependencies{
		Repository: collectionRepository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return nil, err
	}
	frozenFence, err := organizingpostgres.NewGORMFrozenMaterialFence(searchRepository, knowledgeRepository)
	if err != nil {
		return nil, err
	}
	owners, err := organizingowner.New(organizingowner.Dependencies{
		Search: search, Evidence: evidence, Profiles: profiles, Knowledge: claims, Collections: collections,
		Authoring: authoring, Claims: claimSearch, FrozenFence: frozenFence,
	})
	if err != nil {
		return nil, err
	}
	repository, err := organizingpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	bootstrapTimeout := timeout
	if bootstrapTimeout <= 0 {
		bootstrapTimeout = 5 * time.Second
	}
	bootstrapCtx, cancel := context.WithTimeout(ctx, bootstrapTimeout)
	defer cancel()
	clock := foundation.SystemClock{}
	if err := repository.EnsureBuiltIns(bootstrapCtx, clock.Now()); err != nil {
		return nil, err
	}
	service, err := organizingapplication.NewService(organizingapplication.Dependencies{
		Drafts: repository, Templates: repository, Starts: repository, Materials: owners, Suggestions: owners, Searches: owners,
		IDs: foundation.NewUUIDGenerator(nil), Clock: clock,
	})
	if err != nil {
		return nil, err
	}
	return organizinghttp.NewHandler(service, service, workflows, timeout), nil
}

// newOrganizingHumanTaskReviewProjector binds Workflow Definition, pending
// task/node and immutable Organizing Snapshot reads before HTTP exposes a
// reviewable Human Task.
func newOrganizingHumanTaskReviewProjector(
	pool *postgres.Pool,
	runtime *workflowpostgres.GORMRuntimeRepository,
) (*organizingworkflow.HumanTaskReviewProjector, error) {
	if pool == nil || runtime == nil {
		return nil, errors.New("organizing human review database and Workflow runtime are required")
	}
	repository, err := organizingpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	return organizingworkflow.NewHumanTaskReviewProjector(organizingworkflow.HumanTaskReviewDependencies{
		Bindings: repository, Snapshots: repository, Stages: runtime, Nodes: runtime, Definitions: runtime,
	})
}

// newCaptureHandler 组装 Workspace-owned 原始内容写入与 PostgreSQL Capture 事实边界。
func newCaptureHandler(pool *postgres.Pool, content captureapplication.ManagedContentWriter, sources workspaceapplication.ScopedSourceWriter, timeout time.Duration) (*capturehttp.Handler, error) {
	if pool == nil || content == nil || sources == nil {
		return nil, errors.New("capture database and managed content writer are required")
	}
	repository, err := capturepostgres.NewGORMRepository(pool, sources)
	if err != nil {
		return nil, err
	}
	service, err := captureapplication.NewService(captureapplication.Dependencies{
		Repository: repository,
		Content:    content,
		IDs:        foundation.NewUUIDGenerator(nil),
		Clock:      foundation.SystemClock{},
	})
	if err != nil {
		return nil, err
	}
	modelRuns, err := agentpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	profiles, err := capturepostgres.NewGORMProfileRepository(pool, modelRuns)
	if err != nil {
		return nil, err
	}
	profileRetries, err := captureapplication.NewProfileRetryService(captureapplication.ProfileRetryDependencies{
		Scheduler: profiles, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return nil, err
	}
	return capturehttp.NewHandlerWithProfiles(service, profiles, profileRetries, timeout), nil
}

// newKnowledgeHandler 组装不可变 Timeline 投影查询与只读 Impact 报告服务。
func newKnowledgeHandler(pool *postgres.Pool, timeout time.Duration) (*knowledgehttp.Handler, error) {
	if pool == nil {
		return nil, errors.New("knowledge timeline database is unavailable")
	}
	repository, err := knowledgepostgres.NewGORMRepository(pool)
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
	auditRepository, err := auditpostgres.NewGORMStore(pool)
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
	impact, err := knowledgeapplication.NewScopedImpactServiceWithAudit(repository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, impactAudit)
	if err != nil {
		return nil, err
	}
	return knowledgehttp.NewHandler(timeline, impact, timeout), nil
}

// newReviewHandler 组装 Review 的 PostgreSQL 事实、证据校验与冻结 FSRS 调度器。
func newReviewHandler(pool *postgres.Pool, timeout time.Duration, questionRefKey string) (*reviewhttp.Handler, error) {
	if pool == nil {
		return nil, errors.New("review database is unavailable")
	}
	repository, err := reviewpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	fsrs, err := platformscheduler.NewFSRSAdapter()
	if err != nil {
		return nil, err
	}
	scorer := reviewapplication.NewDeterministicScorer()
	service, err := reviewapplication.NewService(repository, repository, scorer, fsrs, questionRefKey, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		return nil, err
	}
	return reviewhttp.NewHandler(service, timeout), nil
}

// newMemoryHandler 组装 Memory 的 PostgreSQL 生命周期事实与认证 HTTP 边界。
func newMemoryHandler(pool *postgres.Pool, timeout time.Duration) (*memoryhttp.Handler, error) {
	service, err := newMemoryService(pool)
	if err != nil {
		return nil, err
	}
	return memoryhttp.NewHandler(service, timeout), nil
}

// newMemoryService 统一组装 Memory 生命周期与 effective-context 读取依赖。
func newMemoryService(pool *postgres.Pool) (*memoryapplication.Service, error) {
	if pool == nil {
		return nil, errors.New("memory database is unavailable")
	}
	gormDB, err := pool.GORM()
	if err != nil {
		return nil, err
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	repository, err := memorypostgres.NewGORMRepository(gormDB, unitOfWork)
	if err != nil {
		return nil, err
	}
	return memoryapplication.NewService(memoryapplication.Dependencies{
		Repository: repository,
		IDs:        foundation.NewUUIDGenerator(nil),
		Clock:      foundation.SystemClock{},
	})
}

// newInterviewHandler 组装 Interview 专属 PostgreSQL 事实、确定性评分和 Artifact DRAFT bridge。
func newInterviewHandler(
	pool *postgres.Pool,
	workspaces workspaceruntimegrant.GORMRepositoryPort,
	files workspacedomain.FileScanner,
	timeout time.Duration,
) (*interviewhttp.Handler, error) {
	service, err := newInterviewService(pool, workspaces, files)
	if err != nil {
		return nil, err
	}
	return interviewhttp.NewHandler(service, timeout), nil
}

// newInterviewService 统一组装 Interview、Memory 上下文和带服务端 Citation 校验的 Artifact 命令。
func newInterviewService(
	pool *postgres.Pool,
	workspaces workspaceruntimegrant.GORMRepositoryPort,
	files workspacedomain.FileScanner,
) (*interviewapplication.Service, error) {
	if pool == nil || workspaces == nil || files == nil {
		return nil, errors.New("interview dependencies are unavailable")
	}
	repository, err := interviewpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	artifactRepository, err := artifactpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	verifier, err := newArtifactCitationVerifier(pool, workspaces, files)
	if err != nil {
		return nil, err
	}
	authoringRepository, err := authoringpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	documentVerifier, err := artifactauthoring.NewVerifier(authoringRepository)
	if err != nil {
		return nil, err
	}
	artifactCommands, err := artifactapplication.NewCommandService(artifactapplication.Dependencies{
		Repository: artifactRepository,
		Evidence:   verifier,
		Documents:  documentVerifier,
		IDs:        foundation.NewUUIDGenerator(nil),
		Clock:      foundation.SystemClock{},
	})
	if err != nil {
		return nil, err
	}
	bridge, err := interviewartifact.NewBridge(artifactCommands)
	if err != nil {
		return nil, err
	}
	memoryService, err := newMemoryService(pool)
	if err != nil {
		return nil, err
	}
	contextLoader, err := interviewmemory.NewLoader(memoryService, memorydomain.SingleUserOwner())
	if err != nil {
		return nil, err
	}
	candidateWriter, err := interviewmemory.NewWriter(memoryService)
	if err != nil {
		return nil, err
	}
	return interviewapplication.NewService(interviewapplication.Dependencies{
		Store:           repository,
		QuestionSource:  repository,
		ContextLoader:   contextLoader,
		CandidateWriter: candidateWriter,
		Scorer:          interviewapplication.DeterministicScorer{},
		ArtifactBridge:  bridge,
		IDGenerator:     foundation.NewUUIDGenerator(nil),
		Clock:           foundation.SystemClock{},
	})
}

// newLearningPathHandler 组装 Review Answer 派生的共享 Path、Artifact hidden hold 与 HTTP 边界。
func newLearningPathHandler(
	pool *postgres.Pool,
	workspaces workspaceruntimegrant.GORMRepositoryPort,
	files workspacedomain.FileScanner,
	timeout time.Duration,
) (*learningpathhttp.Handler, error) {
	if pool == nil || workspaces == nil || files == nil {
		return nil, errors.New("learning path dependencies are unavailable")
	}
	repository, err := learningpathpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	artifactRepository, err := artifactpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	verifier, err := newArtifactCitationVerifier(pool, workspaces, files)
	if err != nil {
		return nil, err
	}
	artifactCommands, err := artifactapplication.NewCommandService(artifactapplication.Dependencies{
		Repository: artifactRepository,
		Evidence:   verifier,
		IDs:        foundation.NewUUIDGenerator(nil),
		Clock:      foundation.SystemClock{},
	})
	if err != nil {
		return nil, err
	}
	creator, err := artifactlearningpath.NewCreator(artifactCommands)
	if err != nil {
		return nil, err
	}
	bridge, err := learningpathartifact.NewBridge(creator)
	if err != nil {
		return nil, err
	}
	service, err := learningpathapplication.NewService(learningpathapplication.Dependencies{
		Store: repository, ArtifactBridge: bridge,
		Clock: foundation.SystemClock{},
	})
	if err != nil {
		return nil, err
	}
	return learningpathhttp.NewHandler(service, timeout), nil
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

// newExportHandler 组装 Collection durable snapshot、受限本地文件和 insert-only River 投递边界。
func newExportHandler(pool *postgres.Pool, cfg config.Config, workspaces workspaceruntimegrant.GORMRepositoryPort, enqueueFences ...riveradapter.ScopedEnqueueFence) (*exporthttp.Handler, error) {
	if pool == nil || workspaces == nil {
		return nil, errors.New("export dependencies are unavailable")
	}
	collectionRepository, err := collectionpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	collectionService, err := collectionapplication.NewService(collectionapplication.Dependencies{
		Repository: collectionRepository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return nil, err
	}
	snapshots, err := exportcollection.NewSnapshotReader(collectionService)
	if err != nil {
		return nil, err
	}
	files, err := exportlocalfs.NewStore(workspaces)
	if err != nil {
		return nil, err
	}
	eventStore, err := eventspostgres.NewGORMStore(pool)
	if err != nil {
		return nil, err
	}
	auditStore, err := auditpostgres.NewGORMStore(pool)
	if err != nil {
		return nil, err
	}
	repository, err := exportpostgres.NewGORMRepository(
		pool,
		exportpostgres.WithGORMEventAppender(eventStore),
		exportpostgres.WithGORMAuditAppender(auditStore),
	)
	if err != nil {
		return nil, err
	}
	riverOptions := riveradapter.Options{
		Queue: cfg.WorkerQueue, MaxWorkers: cfg.WorkerMaxWorkers,
		JobTimeout: cfg.WorkerJobTimeout, RescueStuckJobsAfter: cfg.WorkerRescueStuckJobsAfter,
		SoftStopTimeout: cfg.WorkerSoftStopTimeout,
	}
	fence, err := apiEnqueueFence(cfg, enqueueFences)
	if err != nil {
		return nil, err
	}
	client, err := riveradapter.NewClientWithOptions(pool.DB(), nil, riverOptions)
	if err != nil {
		return nil, err
	}
	dispatcher, err := exportriver.NewGORMTransactionalDispatcher(pool, client, fence)
	if err != nil {
		return nil, err
	}
	service, err := exportapplication.NewService(exportapplication.Dependencies{
		Repository: repository, Dispatcher: dispatcher, Snapshots: snapshots, Workspaces: workspaces, Files: files,
		Attachments: files, Authorizer: exportauth.Authorizer{}, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return nil, err
	}
	return exporthttp.NewHandler(service), nil
}

// newGraphHandler 以 canonical Knowledge facts 组装只读 Graph 查询链路。
func newGraphHandler(pool *postgres.Pool, timeout time.Duration) (*graphhttp.Handler, error) {
	if pool == nil {
		return nil, errors.New("graph database is unavailable")
	}
	repository, err := graphpostgres.NewGORMRepository(pool)
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
func newCandidateHandler(pool *postgres.Pool, runtime *workflowpostgres.GORMRuntimeRepository, timeout time.Duration) (*graphhttp.CandidateHandler, error) {
	if pool == nil {
		return nil, errors.New("semantic link candidate database is unavailable")
	}
	repository, err := graphpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	proposals, err := changecontrolpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	confirmer, err := graphpostgres.NewGORMCandidateConfirmRepository(
		pool,
		proposals,
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
	collectionRepository, err := collectionpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	scanRepository, err := graphpostgres.NewGORMSemanticLinkScanRepository(pool, runtime, collectionRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		return nil, err
	}
	scanService, err := graphapplication.NewSemanticLinkScanService(scanRepository, scanRepository)
	if err != nil {
		return nil, err
	}
	planner, err := graphpostgres.NewGORMSemanticLinkTopicScanPlanner(pool)
	if err != nil {
		return nil, err
	}
	collectionService, err := collectionapplication.NewService(collectionapplication.Dependencies{
		Repository: collectionRepository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return nil, err
	}
	smartPlanner, err := graphpostgres.NewGORMSmartCollectionScanPlanner(collectionService)
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
	pool *postgres.Pool,
	embedder retrievalapplication.Embedder,
	workspaceRepository workspacedomain.SourceMaterialRepository,
	files workspacedomain.FileScanner,
	embeddingAcquirers ...retrievalapplication.CompatibleEmbeddingAcquirer,
) (*retrievalhttp.Handler, error) {
	if pool == nil || workspaceRepository == nil || files == nil {
		return nil, errors.New("retrieval search dependencies are unavailable")
	}
	searchRepository, err := retrievalpostgres.NewGORMSearchRepository(pool)
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
	searchService, err := newAPISearchService(searchRepository, embedder, nil, embeddingAcquirers...)
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
	pool *postgres.Pool,
	workspaces workspaceruntimegrant.GORMRepositoryPort,
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
	pool *postgres.Pool,
	workspaces workspaceruntimegrant.GORMRepositoryPort,
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
	repository, err := artifactpostgres.NewGORMRepository(pool)
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
	pool *postgres.Pool,
	workspaces workspaceruntimegrant.GORMRepositoryPort,
	files workspacedomain.FileScanner,
) (*artifactapplication.ServerCitationVerifier, error) {
	if pool == nil || workspaces == nil || files == nil {
		return nil, errors.New("artifact citation dependencies are unavailable")
	}
	searchRepository, err := retrievalpostgres.NewGORMSearchRepository(pool)
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
	knowledgeRepository, err := knowledgepostgres.NewGORMRepository(pool)
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
	pool *postgres.Pool,
) (*agentpostgres.GORMRepository, *artifactpostgres.GORMSectionGenerationTerminalHook, error) {
	if pool == nil {
		return nil, nil, errors.New("artifact generation database is unavailable")
	}
	agentRepository, err := agentpostgres.NewGORMRepository(pool)
	if err != nil {
		return nil, nil, err
	}
	terminal, err := artifactpostgres.NewGORMSectionGenerationTerminalHook(pool, agentRepository, agentworkflow.DefaultProfileRef())
	if err != nil {
		return nil, nil, err
	}
	return agentRepository, terminal, nil
}

// newArtifactSectionGeneration 在权威 Runtime 构造后组装 PostgreSQL Generation Repository。
func newArtifactSectionGeneration(
	pool *postgres.Pool,
	workspaces workspaceruntimegrant.GORMRepositoryPort,
	files workspacedomain.FileScanner,
	runtime *workflowpostgres.GORMRuntimeRepository,
	agentRepository *agentpostgres.GORMRepository,
	ids foundation.IDGenerator,
	clock foundation.Clock,
) (*artifactpostgres.GORMSectionGenerationRepository, error) {
	if pool == nil || workspaces == nil || files == nil || runtime == nil || agentRepository == nil || ids == nil || clock == nil {
		return nil, errors.New("artifact generation dependencies are unavailable")
	}
	verifier, err := newArtifactCitationVerifier(pool, workspaces, files)
	if err != nil {
		return nil, err
	}
	binding, err := workflowpostgres.NewGORMRuntimeBindingReader(pool)
	if err != nil {
		return nil, err
	}
	generation, err := artifactpostgres.NewGORMSectionGenerationRepository(
		pool,
		runtime,
		binding,
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

// artifactGenerationDependencies 只要持久化 Workflow 边界可用就注入章节生成能力。
// 模型 capability 在 Worker 获取实际 generation 时再 fail closed。
func artifactGenerationDependencies(
	generation artifactapplication.SectionGenerationStarter,
) []artifactapplication.SectionGenerationStarter {
	if generation == nil {
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
	pool *postgres.Pool,
	runtime *workflowpostgres.GORMRuntimeRepository,
	ragEnabled bool,
	workspaceAnalysis ...agentapplication.ScopedWorkspaceAnalysisRunStarter,
) (*conversationhttp.Handler, *eventshttp.Handler, error) {
	if pool == nil {
		return nil, nil, errors.New("conversation database is unavailable")
	}
	if len(workspaceAnalysis) > 1 || (!ragEnabled && len(workspaceAnalysis) != 0) {
		return nil, nil, errors.New("conversation workspace analysis capability is ambiguous or cannot run without dispatch")
	}
	eventStore, err := eventspostgres.NewGORMStore(pool)
	if err != nil {
		return nil, nil, err
	}
	repository, err := conversationpostgres.NewGORMRepository(pool, eventStore)
	if err != nil {
		return nil, nil, err
	}
	var dispatcher conversationapplication.QuestionDispatcher
	if ragEnabled {
		if len(workspaceAnalysis) == 1 {
			auditRepository, auditErr := auditpostgres.NewGORMStore(pool)
			if auditErr != nil {
				return nil, nil, auditErr
			}
			auditRecorder, auditErr := auditapplication.NewRecorder(auditRepository)
			if auditErr != nil {
				return nil, nil, auditErr
			}
			dispatcher, err = conversationpostgres.NewGORMQuestionDispatcherWithWorkspaceAnalysisV2AndAudit(
				pool, runtime, eventStore, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, workspaceAnalysis[0], auditRecorder,
			)
		} else {
			dispatcher, err = conversationpostgres.NewGORMQuestionDispatcher(
				pool, runtime, eventStore, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
			)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	service, err := conversationapplication.NewService(conversationapplication.Dependencies{
		Repository: repository, QuestionDispatcher: dispatcher, FeedbackRepository: repository,
		WorkspaceAnalysisTimelineReader: repository,
		IDs:                             foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		return nil, nil, err
	}
	return conversationhttp.NewHandler(service, conversationhttp.NewCursorCodec()), eventshttp.NewHandler(eventStore), nil
}

// newDraftStreamHandler 独立于持久 Server Event 组装短期 Answer 草稿流。
func newDraftStreamHandler(pool *postgres.Pool) (*conversationhttp.DraftStreamHandler, error) {
	if pool == nil {
		return nil, errors.New("answer draft stream database is unavailable")
	}
	repository, err := conversationpostgres.NewGORMDraftStreamRepository(pool)
	if err != nil {
		return nil, err
	}
	return conversationhttp.NewDraftStreamHandler(repository), nil
}

func newWorkflowService(pool *postgres.Pool) (*workflowapplication.Service, error) {
	service, _, err := newWorkflowComponents(pool, config.Defaults())
	return service, err
}

type apiRuntimeRepositoryFactory struct {
	pool      *postgres.Pool
	options   riveradapter.Options
	fence     riveradapter.ScopedEnqueueFence
	synthesis *apiSynthesisRuntimeComponents
}

// apiEnqueueFence 显式保持静态配置无 rollout 锁、受管配置必须有真实事务围栏的边界。
func apiEnqueueFence(cfg config.Config, fences []riveradapter.ScopedEnqueueFence) (riveradapter.ScopedEnqueueFence, error) {
	if len(fences) > 1 {
		return nil, errors.New("workflow enqueue fence is ambiguous")
	}
	if len(fences) == 1 {
		if fences[0] == nil {
			return nil, errors.New("workflow enqueue fence is unavailable")
		}
		return fences[0], nil
	}
	if cfg.ModelSettingsMode == config.ModelSettingsModeStatic {
		return riveradapter.NewStaticScopedEnqueueFence(), nil
	}
	return nil, errors.New("managed workflow enqueue fence is unavailable")
}

// newAPIRuntimeRepositoryFactory 使用同一 pool 与 Worker 配置创建可复用的 Runtime Repository factory。
func newAPIRuntimeRepositoryFactory(pool *postgres.Pool, cfg config.Config, enqueueFences ...riveradapter.ScopedEnqueueFence) (*apiRuntimeRepositoryFactory, error) {
	if pool == nil {
		return nil, errors.New("workflow database is unavailable")
	}
	riverOptions := riveradapter.Options{
		Queue: cfg.WorkerQueue, MaxWorkers: cfg.WorkerMaxWorkers,
		JobTimeout: cfg.WorkerJobTimeout, RescueStuckJobsAfter: cfg.WorkerRescueStuckJobsAfter,
		SoftStopTimeout: cfg.WorkerSoftStopTimeout,
	}
	fence, err := apiEnqueueFence(cfg, enqueueFences)
	if err != nil {
		return nil, err
	}
	synthesis, err := newAPISynthesisRuntimeComponents(pool)
	if err != nil {
		return nil, err
	}
	return &apiRuntimeRepositoryFactory{pool: pool, options: riverOptions, fence: fence, synthesis: synthesis}, nil
}

func (factory *apiRuntimeRepositoryFactory) newRepository(hooks workflowpostgres.GORMRuntimeRepositoryHooks) (*workflowpostgres.GORMRuntimeRepository, error) {
	if factory == nil || factory.pool == nil || factory.fence == nil {
		return nil, errors.New("workflow runtime repository factory is unavailable")
	}
	hooks, err := factory.withSynthesisRuntimeHooks(hooks)
	if err != nil {
		return nil, err
	}
	return workflowpostgres.NewGORMRuntimeRepositoryWithHooks(factory.pool, factory.options, factory.fence, hooks)
}

// newAPIArtifactWorkflowComponents 按 Agent、terminal hook、权威 Runtime、Generation 的顺序消除构造环。
func newAPIArtifactWorkflowComponents(
	pool *postgres.Pool,
	cfg config.Config,
	workspaces workspaceruntimegrant.GORMRepositoryPort,
	files workspacedomain.FileScanner,
	cancellation workflowapplication.ScopedCancellationSafetyGuard,
	ids foundation.IDGenerator,
	clock foundation.Clock,
	workspaceAnalysis *apiWorkspaceAnalysisRuntimeHooks,
	enqueueFences ...riveradapter.ScopedEnqueueFence,
) (*workflowapplication.Service, *workflowpostgres.GORMRuntimeRepository, *artifactpostgres.GORMSectionGenerationRepository, error) {
	factory, err := newAPIRuntimeRepositoryFactory(pool, cfg, enqueueFences...)
	if err != nil {
		return nil, nil, nil, err
	}
	return newAPIArtifactWorkflowComponentsWithFactory(factory, workspaces, files, cancellation, ids, clock, workspaceAnalysis)
}

// The production entry point retains this factory's same-pool synthesis owners
// for HTTP composition after the one authoritative Workflow runtime is built.
func newAPIArtifactWorkflowComponentsWithFactory(
	factory *apiRuntimeRepositoryFactory,
	workspaces workspaceruntimegrant.GORMRepositoryPort,
	files workspacedomain.FileScanner,
	cancellation workflowapplication.ScopedCancellationSafetyGuard,
	ids foundation.IDGenerator,
	clock foundation.Clock,
	workspaceAnalysis *apiWorkspaceAnalysisRuntimeHooks,
) (*workflowapplication.Service, *workflowpostgres.GORMRuntimeRepository, *artifactpostgres.GORMSectionGenerationRepository, error) {
	if factory == nil || factory.pool == nil {
		return nil, nil, nil, errors.New("workflow runtime repository factory is unavailable")
	}
	pool := factory.pool
	agentRepository, terminal, err := newArtifactGenerationAgent(pool)
	if err != nil {
		return nil, nil, nil, err
	}
	terminalHooks, controlHook, err := composeAPIWorkflowRuntimeHooks(terminal, workspaceAnalysis)
	if err != nil {
		return nil, nil, nil, err
	}
	runtime, err := factory.newRepository(workflowpostgres.GORMRuntimeRepositoryHooks{
		CancellationSafety: cancellation,
		Terminal:           terminalHooks,
		Control:            controlHook,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	generation, err := newArtifactSectionGeneration(pool, workspaces, files, runtime, agentRepository, ids, clock)
	if err != nil {
		return nil, nil, nil, err
	}
	service, err := newWorkflowServiceWithRuntime(factory, runtime, true)
	if err != nil {
		return nil, nil, nil, err
	}
	return service, runtime, generation, nil
}

func newWorkflowComponents(pool *postgres.Pool, cfg config.Config, guards ...workflowapplication.ScopedCancellationSafetyGuard) (*workflowapplication.Service, *workflowpostgres.GORMRuntimeRepository, error) {
	if len(guards) > 1 {
		return nil, nil, errors.New("workflow accepts at most one cancellation guard")
	}
	factory, err := newAPIRuntimeRepositoryFactory(pool, cfg)
	if err != nil {
		return nil, nil, err
	}
	hooks := workflowpostgres.GORMRuntimeRepositoryHooks{}
	if len(guards) == 1 {
		hooks.CancellationSafety = guards[0]
	}
	return newWorkflowComponentsWithFactory(factory, hooks)
}

func newWorkflowComponentsWithFactory(
	factory *apiRuntimeRepositoryFactory,
	hooks workflowpostgres.GORMRuntimeRepositoryHooks,
) (*workflowapplication.Service, *workflowpostgres.GORMRuntimeRepository, error) {
	if factory == nil || factory.pool == nil {
		return nil, nil, errors.New("workflow runtime repository factory is unavailable")
	}
	runtimeRepository, err := factory.newRepository(hooks)
	if err != nil {
		return nil, nil, err
	}
	service, err := newWorkflowServiceWithRuntime(factory, runtimeRepository, false)
	if err != nil {
		return nil, nil, err
	}
	return service, runtimeRepository, nil
}

func newWorkflowServiceWithRuntime(
	factory *apiRuntimeRepositoryFactory,
	runtimeRepository *workflowpostgres.GORMRuntimeRepository,
	chatEnabled bool,
) (*workflowapplication.Service, error) {
	if factory == nil || factory.pool == nil || runtimeRepository == nil {
		return nil, errors.New("workflow runtime dependencies are unavailable")
	}
	repository, err := workflowpostgres.NewGORMRepository(factory.pool)
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
	if err := registerAPIWorkflowExecutors(chatEnabled, executors); err != nil {
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
	if err := registerAPIWorkflowDefinitions(chatEnabled, definitions); err != nil {
		return nil, err
	}
	if err := definitions.Freeze(); err != nil {
		return nil, err
	}
	service, err := workflowapplication.NewRuntimeService(repository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, workflowapplication.RuntimeDependencies{Definitions: definitions, Starter: runtimeRepository, State: runtimeRepository, Human: runtimeRepository})
	if err != nil {
		return nil, err
	}
	return service, nil
}

func newToolContractRegistry() (*toolsapplication.Registry, error) {
	return toolcatalog.NewFrozenContractRegistry()
}

func registerAPIWorkflowExecutors(chatEnabled bool, executors *workflowapplication.ExecutorRegistry) error {
	if err := executors.Register(workflowapplication.CanonicalJSONHashNodeKind, workflowapplication.CanonicalJSONHashInputSchemaVersion, workflowapplication.NewCanonicalJSONHashExecutor()); err != nil {
		return err
	}
	if err := executors.RegisterContract(healthapplication.HealthScanNodeKind, healthapplication.HealthScanInputSchemaVersion); err != nil {
		return err
	}
	if chatEnabled {
		if err := executors.RegisterContract(agentworkflow.RelationAssessmentNodeKind, agentworkflow.RelationAssessmentInputSchemaVersion); err != nil {
			return err
		}
		if err := executors.RegisterContract(conversationworkflow.NodeKind, conversationworkflow.InputSchemaVersion); err != nil {
			return err
		}
	}
	return registerAPISynthesisWorkflowContracts(executors)
}

func registerAPIWorkflowDefinitions(chatEnabled bool, definitions *workflowapplication.DefinitionRegistry) error {
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
	if chatEnabled {
		if err := definitions.Register(agentworkflow.RegisteredDefinition()); err != nil {
			return err
		}
		if err := definitions.Register(conversationworkflow.RegisteredDefinitionV2()); err != nil {
			return err
		}
	}
	return registerAPISynthesisWorkflowDefinitions(definitions)
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
func configureCollectionHealth(database *postgres.Pool, cfg config.Config, collectionHandler **collectionhttp.Handler, healthHandler **healthhttp.Handler, runtime *workflowpostgres.GORMRuntimeRepository) error {
	if database == nil || collectionHandler == nil || healthHandler == nil {
		return errors.New("collection and health composition dependencies are unavailable")
	}
	repository, err := collectionpostgres.NewGORMRepository(database)
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

	issueRepository, err := healthpostgres.NewGORMIssueRepository(database, membership, foundation.NewUUIDGenerator(nil), repository)
	if err != nil {
		return err
	}
	readRepository, err := healthpostgres.NewGORMReadRepository(database)
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

	factReader, err := healthpostgres.NewGORMFactReader(database, membership)
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
		healthEvents, err := eventspostgres.NewGORMStore(database)
		if err != nil {
			return err
		}
		scanRepository, err := healthpostgres.NewGORMScanRepository(database, runtime, healthEvents, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, repository)
		if err != nil {
			return err
		}
		scanService, err = healthapplication.NewSmartCollectionScanService(scanRepository, scanRepository, registry, membership)
		if err != nil {
			return err
		}
		scheduleRepository, err := healthpostgres.NewGORMScheduleRepository(database, foundation.NewUUIDGenerator(nil))
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
