//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	authpostgres "github.com/CodeZen-Lizhi/zhixu/internal/auth/adapter/postgres"
	authapp "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	synthesispostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/synthesispostgres"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizinghttp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/http"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/gin-gonic/gin"
)

// 显式启用的浏览器通过真实 HTTP/PG 所属模块操作测试自有 Git 根目录，并观察候选已提交、提案尚未创建的中断窗口。
func TestSynthesisCandidateRemergeBrowser(t *testing.T) {
	directory := os.Getenv("ZHIXU_REMERGE_BROWSER_DIR")
	if directory == "" {
		t.Skip("set ZHIXU_REMERGE_BROWSER_DIR for the live browser checkpoint")
	}
	if !filepath.IsAbs(directory) {
		t.Fatal("browser artifact directory must be absolute")
	}
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	f, r1 := newCandidateRemergeFixture(t)
	runtime, _ := candidateRemergeOwner(t, f)
	ctx := t.Context()
	path, err := authoringdomain.DefaultGeneratedTargetPath(r1.Note.ID)
	check(err)
	current := "# Latest workspace manuscript\n\nKeep this later workspace paragraph.\n"
	check(os.WriteFile(filepath.Join(f.root, path), []byte(current), 0600))
	synthesisGit(t, ctx, f.root, "add", "--", path)
	synthesisGit(t, ctx, f.root, "commit", "-m", "isolated latest manuscript before remerge")
	expected := current + "\n" + r1.CurrentRevision.Manuscript.FullContent + "\nBrowser remerge annotation retained.\n"
	var originalModels int64
	check(f.db.Table("agent.model_run").Count(&originalModels).Error)
	runtime.dependencies.Service.Publications = &remergeBrowserBeforeProposal{publisher: runtime.dependencies.Service.Publications}

	repository, err := authpostgres.NewGORMRepository(f.db)
	check(err)
	const bootstrap = "remerge-browser-isolated-bootstrap"
	auth, err := authapp.NewService(repository, foundation.UUIDGenerator{}, foundation.SystemClock{}, authapp.Options{BootstrapToken: bootstrap})
	check(err)
	session, err := auth.ExchangeBootstrap(ctx, bootstrap)
	check(err)
	principal, err := auth.AuthenticateSession(ctx, session.Token, session.CSRFToken, false)
	check(err)
	credential, err := auth.CreateAPIToken(ctx, principal, "isolated candidate remerge", []capability.Capability{capability.ReadLocal, capability.WriteProposal}, time.Hour)
	check(err)
	middleware, err := authhttp.NewHandler(auth, authhttp.Options{AllowedOrigins: []string{"http://127.0.0.1:8080"}})
	check(err)
	router := gin.New()
	group := router.Group("/api/v1")
	group.Use(middleware.Middleware)
	modelRepository, err := agentpostgres.NewGORMRepository(f.platform)
	check(err)
	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(f.platform)
	check(err)
	bindings, err := workflowpostgres.NewGORMRuntimeBindingReader(f.platform)
	check(err)
	processingStore, err := synthesispostgres.NewStore(f.platform, synthesispostgres.Dependencies{ModelRuns: modelRepository, WorkflowFence: fence, WorkflowBindings: bindings})
	check(err)
	workflowRuntime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(f.platform, riveradapter.DefaultOptions(), riveradapter.NewStaticScopedEnqueueFence(), workflowpostgres.GORMRuntimeRepositoryHooks{Terminal: processingStore})
	check(err)
	validation, err := workflowapp.NewValidationCatalog([]int{1}, capability.All())
	check(err)
	executors, err := workflowapp.NewExecutorRegistry(validation)
	check(err)
	definitions, err := workflowapp.NewDefinitionRegistry(validation, executors)
	check(err)
	// 此浏览器桥接仅开放笔记和重合并路由；处理状态读取使用真实存储，但本测试不提供其重试定义。
	processing, err := organizingworkflow.NewSynthesisProcessingService(organizingworkflow.SynthesisProcessingServiceDependencies{UnitOfWork: f.uow, Queries: processingStore, Retries: processingStore, Applied: f.store, Starter: workflowRuntime, Definitions: definitions, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}})
	check(err)
	handler := organizinghttp.NewSynthesisHandler(f.service, processing, 10*time.Second).WithCandidateRemerge(runtime)
	if !handler.Available() {
		t.Fatal("browser synthesis HTTP dependencies are incomplete")
	}
	handler.Routes(group)

	var mu sync.Mutex
	phase := "review"
	advance := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/qa/state" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"phase": phase, "workspace_id": f.workspace, "note_id": r1.Note.ID})
			return
		}
		if r.URL.Path == "/api/qa/advance" && r.Method == http.MethodPost {
			select {
			case advance <- struct{}{}:
				w.WriteHeader(http.StatusAccepted)
			default:
				w.WriteHeader(http.StatusConflict)
			}
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/workspaces/"+string(f.workspace)+"/synthesis/notes/"+string(r1.Note.ID)) {
			http.NotFound(w, r)
			return
		}
		// 回环桥接仅提供自身短期有效的测试凭据。
		forward := r.Clone(r.Context())
		forward.Header.Set("Authorization", "Bearer "+credential.Plain)
		router.ServeHTTP(w, forward)
	}))
	defer server.Close()
	manifest, err := os.OpenFile(filepath.Join(directory, "server.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	check(err)
	check(json.NewEncoder(manifest).Encode(map[string]any{"url": server.URL, "workspace_id": f.workspace, "note_id": r1.Note.ID, "original_revision_id": r1.CurrentRevision.ID, "expected_content": expected}))
	check(manifest.Close())
	t.Logf("remerge browser fixture ready: %s", filepath.Join(directory, "server.json"))
	wait := func() {
		t.Helper()
		select {
		case <-advance:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(12 * time.Minute):
			t.Fatal("remerge browser checkpoint timed out")
		}
	}
	wait()
	r2, err := f.service.GetNote(ctx, f.workspace, r1.Note.ID)
	check(err)
	check(os.WriteFile(filepath.Join(directory, "observed-content.md"), []byte(r2.CurrentRevision.Manuscript.FullContent), 0600))
	if r2.CurrentRevision.ID == r1.CurrentRevision.ID || r2.CurrentRevision.Remerge == nil || r2.CurrentRevision.Manuscript.FullContent != expected || r2.Publication == nil {
		t.Fatalf("browser candidate mismatch: new_revision=%t remerge=%t exact_content=%t proposal=%t bytes=%d expected=%d", r2.CurrentRevision.ID != r1.CurrentRevision.ID, r2.CurrentRevision.Remerge != nil, r2.CurrentRevision.Manuscript.FullContent == expected, r2.Publication != nil, len(r2.CurrentRevision.Manuscript.FullContent), len(expected))
	}
	if r2.PublishedRevision.ID != r1.PublishedRevision.ID {
		t.Fatal("browser candidate changed the published pointer")
	}
	disk, err := os.ReadFile(filepath.Join(f.root, path))
	check(err)
	if string(disk) != current {
		t.Fatal("browser remerge wrote the current file")
	}
	old, err := f.service.GetSynthesisRevision(ctx, f.workspace, r1.Note.ID, r1.CurrentRevision.ID)
	check(err)
	original, err := json.Marshal(r1.CurrentRevision)
	check(err)
	retained, err := json.Marshal(old)
	check(err)
	if !bytes.Equal(original, retained) {
		t.Fatal("browser remerge changed original candidate history")
	}
	var models, applications int64
	check(f.db.Table("agent.model_run").Count(&models).Error)
	check(f.db.Table("organizing.synthesis_candidate_remerge_event").Where("kind=?", "APPLY").Count(&applications).Error)
	if models != originalModels || applications != 1 {
		t.Fatalf("browser recovery duplicated model/candidate work: models=%d applications=%d", models, applications)
	}
	mu.Lock()
	phase = "candidate"
	mu.Unlock()
	wait()
	prepared := f.preparePublication(t, r2)
	written, err := f.node.Execute(ctx, prepared.input, prepared.identity)
	check(err)
	f.assertPublished(t, r2, path, written)
	mu.Lock()
	phase = "published"
	mu.Unlock()
	wait()
	t.Log("browser Target/Begin/conflict/refresh/recovery/candidate/approval/Git/read passed")
}

type remergeBrowserBeforeProposal struct {
	publisher app.SynthesisPublicationPublisher
	once      sync.Once
}

func (p *remergeBrowserBeforeProposal) PublishArticleRevision(ctx context.Context, command authoringapp.PublishCommand) (authoringapp.PublishResult, error) {
	interrupted := false
	p.once.Do(func() { interrupted = true })
	if interrupted {
		return authoringapp.PublishResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "REMERGE_BROWSER_PROPOSAL_INTERRUPTED", true, context.DeadlineExceeded)
	}
	return p.publisher.PublishArticleRevision(ctx, command)
}
