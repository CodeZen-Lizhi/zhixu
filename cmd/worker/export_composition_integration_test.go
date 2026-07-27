//go:build integration

package main

import (
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
)

func TestWorkerExportCompositionRegistersWorkerAndMaintenanceService(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a PostgreSQL admin database")
	}
	pool := newMigratedWorkerTestPool(t, databaseURL)
	components, err := newWorkerComponents(
		pool,
		config.Defaults(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		observability.NewMemoryMetrics(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if components.exportWorker == nil || components.exportService == nil {
		t.Fatalf("export worker=%#v service=%#v", components.exportWorker, components.exportService)
	}
}
