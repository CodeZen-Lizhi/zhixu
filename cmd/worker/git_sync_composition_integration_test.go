//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	gitsyncapplication "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitoperation"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
)

func TestGitSyncWorkerProductionCompositionAndEmptyDispatch(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	pool := newMigratedWorkerTestPool(t, baseURL)
	workspaceRepository, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	committedGit, err := gitcli.NewWritebackClient(gitcli.New(""), workspaceRepository)
	if err != nil {
		t.Fatal(err)
	}
	locker, err := gitoperation.NewPostgresLocker(pool.DB())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.GitSyncKeyFile = writeGitSyncTestKey(t)
	models, err := modelRuntimeForComposition(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := newSourceProcessingComponents(pool, cfg, workspaceRepository, committedGit, nil, models)
	if err != nil {
		t.Fatal(err)
	}
	workerID := foundation.ID("10000000-0000-4000-8000-000000000002")
	worker, scheduler, err := newGitSyncWorker(pool, cfg, workspaceRepository, committedGit, sources, locker, workerID)
	if err != nil {
		t.Fatal(err)
	}
	if worker == nil || scheduler == nil {
		t.Fatalf("configured Git sync composition is incomplete: worker=%v scheduler=%v", worker, scheduler)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	batch, processed, err := dispatchGitSync(
		ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), scheduler, worker, gitSyncDispatchStartupPhase,
	)
	if err != nil || batch != (gitsyncapplication.AutoSyncBatchResult{}) || processed != 0 {
		t.Fatalf("batch=%+v processed=%d err=%v", batch, processed, err)
	}
}

func writeGitSyncTestKey(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git-sync.key")
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32))
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
