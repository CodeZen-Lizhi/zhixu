//go:build integration

package workspacepostgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapp "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	workspacehttp "github.com/CodeZen-Lizhi/zhixu/internal/workspace/http"
	"github.com/gin-gonic/gin"
)

func TestDiscoveryFailurePersistsRecoversAndFencesRoot(t *testing.T) {
	fixture := testdb.Require(t, testdb.Config{ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")), Availability: testdb.FailWhenUnavailable})
	ctx := t.Context()
	pool := fixture.Pool()
	repo, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	now := time.Now().UTC()
	workspace, err := repo.CreateWorkspace(ctx, domain.Workspace{ID: mustID(t, "10000000-0000-4000-8000-000000000991"), Name: "discovery", RootPath: root, Git: domain.GitBaseline{RepositoryPath: root, Branch: "main", Head: strings.Repeat("a", 40), CheckedAt: now}, Status: domain.WorkspaceStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(root, "unreadable.md")
	if err := os.WriteFile(bad, []byte("原始笔记"), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0600) })
	if f, e := os.Open(bad); e == nil {
		f.Close()
		t.Fatal("test requires a non-root user to prove real unreadability")
	}
	scanner := filesystem.Scanner{}
	service := workspaceapp.NewService(workspaceapp.Dependencies{Repository: repo, Files: scanner, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	page, err := service.DiscoverWorkspaceSources(ctx, workspace.ID, "", 100)
	if err != nil || page.Failed != 1 || len(page.Files) != 0 {
		t.Fatalf("unreadable scan: %+v %v", page, err)
	}
	// 扫描失败后重新打开仓库/服务，并查询真实 HTTP 边界。
	reopened, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	restarted := workspaceapp.NewService(workspaceapp.Dependencies{Repository: reopened, Files: scanner, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	router := gin.New()
	workspacehttp.NewHandler(restarted).Routes(router.Group("/api/v1"))
	read := func() domain.DiscoveryFailurePage {
		t.Helper()
		r := httptest.NewRecorder()
		router.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+string(workspace.ID)+"/discovery-failures", nil))
		if r.Code != 200 {
			t.Fatalf("HTTP: %d %s", r.Code, r.Body.String())
		}
		var p domain.DiscoveryFailurePage
		if err := json.Unmarshal(r.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := read()
	if len(first.Items) != 1 || first.Items[0].Path != "unreadable.md" || first.Items[0].Status != "FAILED" || first.Items[0].Code != "FILE_OBSERVATION_FAILED" {
		t.Fatalf("failed persisted row: %+v", first)
	}
	var versions int
	if err := pool.DB().QueryRow(ctx, "SELECT count(*) FROM core.source_version WHERE workspace_id=$1", workspace.ID).Scan(&versions); err != nil || versions != 0 {
		t.Fatalf("failed scan made source versions: %d %v", versions, err)
	}
	// 被排除的扩展名和未触及的路径绝不意味着恢复。
	if err := os.WriteFile(filepath.Join(root, "ignored.exe"), []byte("ignored"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.DiscoverWorkspaceSources(ctx, workspace.ID, "unreadable.md", 100); err != nil {
		t.Fatal(err)
	}
	if read().Items[0].Status != "FAILED" {
		t.Fatal("unscanned path recovered")
	}
	if err := os.Chmod(bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ScanWorkspace(ctx, workspace.ID); err != nil {
		t.Fatal(err)
	}
	recovered := read()
	if recovered.Items[0].Status != "RECOVERED" || recovered.Items[0].RecoveredAt == nil {
		t.Fatal("successful retry did not recover")
	}
	if _, err := restarted.ScanWorkspace(ctx, workspace.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.DB().QueryRow(ctx, "SELECT count(*) FROM core.source_version WHERE workspace_id=$1", workspace.ID).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("retry invented versions: %d %v", versions, err)
	}
	// 原本不可读的目录路径上出现文件，不能证明 WALK
	// 已恢复；子项注册成功也不能证明这一点。
	if err := repo.RecordDiscoveryObservations(ctx, workspace, []domain.DiscoveryObservation{{Path: "blocked", Stage: "WALK", Code: "DIRECTORY_READ_FAILED"}, {Path: "blocked/child.md", Stage: "REGISTER"}, {Path: "blocked", Stage: "REGISTER"}}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.Items[0].Path != "blocked" || got.Items[0].Status != "FAILED" {
		t.Fatalf("file registration recovered directory: %+v", got)
	}
	if err := repo.RecordDiscoveryObservations(ctx, workspace, []domain.DiscoveryObservation{{Path: "blocked", Stage: "WALK"}}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.Items[0].Status != "RECOVERED" {
		t.Fatalf("actual directory enumeration did not recover: %+v", got)
	}
	wrong := workspace
	wrong.BindingVersion++
	if err := repo.RecordDiscoveryObservations(ctx, wrong, []domain.DiscoveryObservation{{Path: "unreadable.md", Stage: "OBSERVE", Code: "FILE_OBSERVATION_FAILED"}}); err == nil {
		t.Fatal("stale root binding accepted")
	}
	wrong = workspace
	wrong.ID = mustID(t, "10000000-0000-4000-8000-000000000992")
	if _, err := repo.ListDiscoveryFailures(ctx, wrong.ID, "", 10); err == nil {
		t.Fatal("unknown workspace leaked failures")
	}
	// 撤销运行时授权后，真实数据库锁下的读写均被阻止。
	guarded, err := workspacepostgres.NewGORMRepository(pool, workspacepostgres.WithGORMRootGrantResolver(discoveryDeniedGrant{}, true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guarded.ListDiscoveryFailures(ctx, workspace.ID, "", 10); err == nil {
		t.Fatal("revoked grant read leaked")
	}
	if err := guarded.RecordDiscoveryObservations(ctx, workspace, []domain.DiscoveryObservation{{Path: "unreadable.md", Stage: "REGISTER"}}); err == nil {
		t.Fatal("revoked grant write accepted")
	}
	// 持久记录失败必须使批次失败，不能报告成功。
	if _, err := pool.DB().Exec(ctx, "DROP TABLE core.workspace_discovery_failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.DiscoverWorkspaceSources(ctx, workspace.ID, "", 100); err == nil {
		t.Fatal("recording outage silently succeeded")
	}
}

type discoveryDeniedGrant struct{}

func (discoveryDeniedGrant) Resolve(context.Context, foundation.ID) (*rootgrant.Capability, error) {
	return nil, errors.New("grant revoked")
}
