package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"syscall"

	atlasmigrations "github.com/CodeZen-Lizhi/zhixu/atlas"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func main() {
	configPath := flag.String("config", "", "optional YAML configuration path")
	flag.Parse()
	logger := observability.NewLogger("info", os.Stderr)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, *configPath); err != nil {
		logger.Error("database migration failed", "error_code", "DATABASE_MIGRATION_FAILED")
		os.Exit(1)
	}
	logger.Info("database migration completed")
}

func run(ctx context.Context, configPath string) error {
	if ctx == nil {
		return errors.New("migration context is nil")
	}
	cfg, err := config.LoadMigration(configPath)
	if err != nil {
		return err
	}
	if err := cfg.ValidateDatabase(); err != nil {
		return err
	}
	databaseURL, err := cfg.DatabaseConnectionString()
	if err != nil {
		return err
	}
	database, err := postgres.OpenMigration(ctx, databaseURL, cfg.DatabaseMaxConns, cfg.DatabaseMinConns)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.Ping(ctx); err != nil {
		return err
	}
	dir, err := platformmigration.LoadAtlasDir(atlasmigrations.MigrationDir())
	if err != nil {
		return err
	}
	runner, err := platformmigration.NewAtlasRunner(database.DB(), dir)
	if err != nil {
		return err
	}
	return runner.Up(ctx)
}
