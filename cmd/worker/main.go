package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
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
	logger.Info("worker started", "version", cfg.Version)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
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

func ping(database *postgres.Pool, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := database.Ping(ctx); err != nil {
		return err
	}
	return nil
}
