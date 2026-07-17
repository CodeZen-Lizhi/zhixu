package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontrolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

type workerComponents struct {
	safeWriteback *changecontrolworkflow.Node
	runtimeClient *riveradapter.Client
}

const (
	workflowLeaseDuration     = 30 * time.Second
	workflowHeartbeatInterval = 5 * time.Second
)

func main() {
	configPath := flag.String("config", "", "optional YAML configuration path")
	flag.Parse()
	logger := observability.NewLogger("info", os.Stderr)
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("configuration is invalid", "error_code", "INVALID_CONFIGURATION")
		os.Exit(1)
	}
	databaseURL, err := cfg.DatabaseConnectionString()
	if err != nil {
		logger.Error("database is not configured", "error_code", "DEPENDENCY_UNAVAILABLE")
		os.Exit(1)
	}

	database, err := postgres.Open(context.Background(), databaseURL, cfg.DatabaseMaxConns, cfg.DatabaseMinConns)
	if err != nil {
		logger.Error("database pool could not be opened", "error_code", "DEPENDENCY_UNAVAILABLE")
		os.Exit(1)
	}
	defer database.Close()

	if err := ping(database, cfg.DatabasePingTimeout); err != nil {
		logger.Error("worker startup database check failed", "error_code", "DEPENDENCY_UNAVAILABLE")
		os.Exit(1)
	}
	components, err := newWorkerComponents(database.DB())
	if err != nil {
		logger.Error("worker components are unavailable", "error_code", "WORKER_COMPONENTS_UNAVAILABLE")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := components.runtimeClient.Start(ctx); err != nil {
		logger.Error("workflow runtime could not be started", "error_code", "WORKFLOW_RIVER_CLIENT_START_FAILED")
		os.Exit(1)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer stopCancel()
		if err := components.runtimeClient.Stop(stopCtx); err != nil {
			logger.Error("workflow runtime could not be stopped", "error_code", "WORKFLOW_RIVER_CLIENT_STOP_FAILED")
		}
	}()
	logger.Info("worker started", "version", cfg.Version, "safe_writeback_node", components.safeWriteback != nil, "workflow_dispatcher", "configured")

	ticker := time.NewTicker(cfg.HealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("worker stopping")
			return
		case <-ticker.C:
			if err := ping(database, cfg.DatabasePingTimeout); err != nil {
				logger.Error("worker database health check failed", "error_code", "DEPENDENCY_UNAVAILABLE")
			} else {
				logger.Debug("worker database health check passed")
			}
		}
	}
}

func newWorkerComponents(db *pgxpool.Pool) (workerComponents, error) {
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
	insertClient, err := riveradapter.NewClient(db, nil)
	if err != nil {
		return workerComponents{}, err
	}
	inserter, err := riveradapter.NewJobInserter(insertClient)
	if err != nil {
		return workerComponents{}, err
	}
	runtimeRepository, err := workflowpostgres.NewRuntimeRepository(db, inserter)
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
	runtimeWorker, err := riveradapter.NewRuntimeNodeWorker(executors, runtimeCoordinator, fmt.Sprintf("worker:%s", workerID), workflowLeaseDuration, workflowHeartbeatInterval)
	if err != nil {
		return workerComponents{}, err
	}
	workers := riveradapter.NewWorkers()
	if err := riveradapter.AddRuntimeWorkerSafely(workers, runtimeWorker); err != nil {
		return workerComponents{}, err
	}
	runtimeClient, err := riveradapter.NewClient(db, workers)
	if err != nil {
		return workerComponents{}, err
	}
	return workerComponents{safeWriteback: node, runtimeClient: runtimeClient}, nil
}

func ping(database *postgres.Pool, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := database.Ping(ctx); err != nil {
		return err
	}
	return nil
}
