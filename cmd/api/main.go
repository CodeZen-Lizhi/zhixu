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

	"github.com/CodeZen-Lizhi/zhixu/internal/app"
	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontrolhttp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/adapter/postgres"
	ingestionworkspace "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/adapter/workspace"
	ingestionapplication "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	ingestionhttp "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	platformparser "github.com/CodeZen-Lizhi/zhixu/internal/platform/parser"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/webassets"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowhttp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/http"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacehttp "github.com/CodeZen-Lizhi/zhixu/internal/workspace/http"
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
	fileScanner := filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}}
	if database != nil {
		workspaceRepository, repositoryErr := workspacepostgres.NewRepository(database.DB())
		if repositoryErr != nil {
			logger.Error("workspace repository is unavailable", "error_code", "WORKSPACE_DATABASE_UNAVAILABLE")
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

			changeControlRepository, changeControlRepositoryErr := changecontrolpostgres.NewRepository(database.DB())
			targetReader, targetReaderErr := changecontrollocalfs.NewReader(workspaceRepository)
			approvalGitInspector, approvalGitInspectorErr := gitcli.NewWritebackClient(gitcli.New(""), workspaceRepository)
			if changeControlRepositoryErr != nil || targetReaderErr != nil || approvalGitInspectorErr != nil {
				logger.Error("change control dependencies are unavailable", "error_code", "CHANGE_CONTROL_DEPENDENCY_UNAVAILABLE")
			} else {
				changeControlService, changeControlServiceErr := changecontrolapplication.NewService(changeControlRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, targetReader, approvalGitInspector)
				if changeControlServiceErr != nil {
					logger.Error("change control service is unavailable", "error_code", "CHANGE_CONTROL_SERVICE_UNAVAILABLE")
				} else {
					changeControlHandler = changecontrolhttp.NewHandler(changeControlService)
				}
			}
		}
		workflowRepository, workflowRepositoryErr := workflowpostgres.NewRepository(database.DB())
		if workflowRepositoryErr != nil {
			logger.Error("workflow repository is unavailable", "error_code", "WORKFLOW_DATABASE_UNAVAILABLE")
		} else {
			workflowService, workflowServiceErr := workflowapplication.NewService(workflowRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
			if workflowServiceErr != nil {
				logger.Error("workflow service is unavailable", "error_code", "WORKFLOW_SERVICE_UNAVAILABLE")
			} else {
				workflowHandler = workflowhttp.NewHandler(workflowService)
			}
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

func firstError(values ...error) error {
	for _, err := range values {
		if err != nil {
			return err
		}
	}
	return nil
}
