// Command local-model-runtime runs the managed local Ollama supervisor.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/localmodelruntime"
	platformconfig "github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

const (
	listenAddress  = ":11434"
	childStopGrace = 15 * time.Second
	shutdownGrace  = 20 * time.Second
)

const runtimeModeEnv = "ZHIXU_LOCAL_MODEL_RUNTIME_MODE"

var errReconcilerShutdownTimeout = errors.New("local model runtime reconciler did not stop before shutdown deadline")

func main() {
	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	if err := run(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "local-model-runtime: LOCAL_MODEL_RUNTIME_FAILED")
		os.Exit(1)
	}
}

func run(ctx context.Context) (runErr error) {
	if ctx == nil {
		return errors.New("local model runtime context is nil")
	}
	mode, err := runtimeModeFromEnvironment(os.LookupEnv)
	if err != nil {
		return err
	}
	manager, err := localmodelruntime.NewManager(localmodelruntime.ExecChildRunner{}, childStopGrace)
	if err != nil {
		return err
	}
	runtimeCtx, cancelRuntime := context.WithCancel(ctx)
	defer cancelRuntime()
	var reconciler *localmodelruntime.Reconciler
	var database closeableDatabase
	var reconcilerDone chan struct{}
	defer func() {
		// Cancel and join the supervisor before closing its database. This also
		// covers handler/listener construction failures after managed startup.
		cancelRuntime()
		joined := true
		if reconcilerDone != nil {
			if waitErr := waitForReconciler(reconcilerDone, shutdownGrace); waitErr != nil {
				joined = false
				runErr = errors.Join(runErr, waitErr)
			}
		}
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancelShutdown()
		closeErr := manager.Close(shutdownCtx)
		runErr = errors.Join(runErr, closeErr)
		if joined && database != nil {
			database.Close()
		}
	}()
	if mode == localmodelruntime.RuntimeModeManaged {
		reconciler, database, err = managedReconciler(runtimeCtx, manager)
		if err != nil {
			return err
		}
		reconcilerDone = make(chan struct{})
		go func() {
			defer close(reconcilerDone)
			if reconcilerErr := reconciler.Run(runtimeCtx); reconcilerErr != nil && runtimeCtx.Err() == nil {
				// Keep the liveness endpoint available while the reconciler retries
				// transient database/model failures, but leave a stable diagnostic.
				_, _ = fmt.Fprintln(os.Stderr, "local-model-runtime: supervisor unavailable")
			}
		}()
	}
	handler, err := localmodelruntime.NewHandler(manager, localmodelruntime.ProxyOptions{})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return err
	}
	serveErr := serve(ctx, listener, handler)
	return serveErr
}

func waitForReconciler(done <-chan struct{}, timeout time.Duration) error {
	if done == nil {
		return nil
	}
	if timeout <= 0 {
		return errors.New("local model runtime reconciler shutdown timeout is invalid")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return errReconcilerShutdownTimeout
	}
}

func runtimeModeFromEnvironment(lookup func(string) (string, bool)) (localmodelruntime.RuntimeMode, error) {
	if lookup == nil {
		return "", errors.New("local model runtime environment is unavailable")
	}
	value, ok := lookup(runtimeModeEnv)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", runtimeModeEnv)
	}
	mode := localmodelruntime.RuntimeMode(strings.TrimSpace(value))
	switch mode {
	case localmodelruntime.RuntimeModeManaged, localmodelruntime.RuntimeModeExternalStatic:
		return mode, nil
	default:
		return "", fmt.Errorf("%s is invalid", runtimeModeEnv)
	}
}

type closeableDatabase interface{ Close() }

func managedReconciler(ctx context.Context, manager *localmodelruntime.Manager) (*localmodelruntime.Reconciler, closeableDatabase, error) {
	cfg, restore, err := loadRuntimeDatabaseConfig()
	if restore != nil {
		defer restore()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("load local model runtime database configuration: %w", err)
	}
	databaseURL, err := cfg.DatabaseConnectionString()
	if err != nil {
		return nil, nil, fmt.Errorf("build local model runtime database configuration: %w", err)
	}
	database, err := platformpostgres.Open(ctx, databaseURL, 2, 1)
	if err != nil {
		return nil, nil, fmt.Errorf("open local model runtime database: %w", err)
	}
	store, err := localmodelruntime.NewPostgresStore(database.DB())
	if err != nil {
		database.Close()
		return nil, nil, err
	}
	ownerID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		database.Close()
		return nil, nil, err
	}
	ollama, err := localmodelruntime.NewFixedOllamaClient()
	if err != nil {
		database.Close()
		return nil, nil, err
	}
	reconciler, err := localmodelruntime.NewReconciler(store, manager, ollama, localmodelruntime.ReconcilerOptions{OwnerID: ownerID})
	if err != nil {
		database.Close()
		return nil, nil, err
	}
	return reconciler, database, nil
}

func loadRuntimeDatabaseConfig() (platformconfig.Config, func(), error) {
	keys := []string{"ZHIXU_DATABASE_USER", "ZHIXU_DATABASE_PASSWORD"}
	previous := make(map[string]*string, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			copy := value
			previous[key] = &copy
		} else {
			previous[key] = nil
		}
	}
	restore := func() {
		for _, key := range keys {
			if value := previous[key]; value != nil {
				_ = os.Setenv(key, *value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}
	for _, key := range keys {
		fileKey := key + "_FILE"
		path, ok := os.LookupEnv(fileKey)
		if !ok || strings.TrimSpace(path) == "" {
			continue
		}
		if filepath.IsAbs(path) == false || filepath.Clean(path) != path {
			return platformconfig.Config{}, restore, fmt.Errorf("%s is invalid", fileKey)
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return platformconfig.Config{}, restore, fmt.Errorf("read %s", fileKey)
		}
		value := strings.TrimSuffix(string(contents), "\n")
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n") {
			return platformconfig.Config{}, restore, fmt.Errorf("%s contains invalid data", fileKey)
		}
		if err := os.Setenv(key, value); err != nil {
			return platformconfig.Config{}, restore, fmt.Errorf("set %s", key)
		}
	}
	cfg, err := platformconfig.LoadMigration("")
	return cfg, restore, err
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	if ctx == nil || listener == nil || handler == nil {
		return errors.New("local model runtime server dependencies are invalid")
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Minute,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()

	select {
	case serveErr := <-serveResult:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancelShutdown()
	shutdownErr := server.Shutdown(shutdownCtx)
	serveErr := <-serveResult
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(shutdownErr, serveErr)
}
