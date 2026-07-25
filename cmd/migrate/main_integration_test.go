//go:build integration

package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRunWithConfiguredDatabase exercises the same migration entry point used
// by the container. It is opt-in so ordinary integration runs can keep their
// existing disposable-database ownership.
func TestRunWithConfiguredDatabase(t *testing.T) {
	if os.Getenv("ZHIXU_MIGRATION_INTEGRATION") != "1" {
		t.Skip("set ZHIXU_MIGRATION_INTEGRATION=1 to exercise the configured migration target")
	}
	// The runtime image exposes the API on all interfaces. Migration must not
	// inherit API-only authentication validation from that process setting.
	t.Setenv("ZHIXU_HTTP_ADDR", "0.0.0.0:8080")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, ""); err != nil {
		t.Fatalf("configured migration failed: %v", err)
	}
}
