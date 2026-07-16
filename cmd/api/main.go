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
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/webassets"
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

	deps := app.Dependencies{
		Version:           cfg.Version,
		Database:          database,
		DatabaseConfigErr: firstError(loadErr, databaseConfigErr),
		DatabaseInitErr:   databaseErr,
		PingTimeout:       cfg.DatabasePingTimeout,
		Static:            static,
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
