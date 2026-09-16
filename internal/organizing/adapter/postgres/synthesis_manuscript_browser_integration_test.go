//go:build integration

package postgres

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	synthesispostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/synthesispostgres"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// 显式启用检查点，让真实浏览器驱动既有 PG/River 测试环境。回环桥接在服务端附加测试凭据，不暴露令牌；发布仍使用真实审批、Git 测试环境及其隔离根目录。
func driveManuscriptBrowser(t *testing.T, f *synthesisGitFixture, manuscripts *SynthesisManuscriptRuntime, runtime *workflowpostgres.GORMRuntimeRepository, definitions *workflowapp.DefinitionRegistry, store *synthesispostgres.Store, human *workflowapp.RuntimeHumanCoordinator, processing app.SynthesisProcessing, before app.SynthesisNoteDetail, calls func() int, expectedCalls int) {
	t.Helper()
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	directory := os.Getenv("ZHIXU_MANUSCRIPT_BROWSER_DIR")
	if !filepath.IsAbs(directory) {
		t.Fatal("browser artifact directory must be absolute")
	}
	ps, err := organizingworkflow.NewSynthesisProcessingService(organizingworkflow.SynthesisProcessingServiceDependencies{UnitOfWork: f.uow, Queries: store, Retries: store, Applied: f.store, Starter: runtime, Definitions: definitions, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}})
	check(err)
	call := manuscriptAuthenticatedHTTP(t, f, manuscripts, human, processing.ID, before.Note.ID, ps)
	base := "/api/v1/workspaces/" + string(f.workspace) + "/synthesis/processing/" + string(processing.ID) + "/manuscript-review"
	path, err := authoringdomain.DefaultGeneratedTargetPath(before.Note.ID)
	check(err)
	original, err := os.ReadFile(filepath.Join(f.root, path))
	check(err)
	var mu sync.Mutex
	phase := "review"
	advance := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/qa/state" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"phase": phase, "workspace_id": f.workspace, "processing_id": processing.ID, "note_id": before.Note.ID})
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
		if !strings.HasPrefix(r.URL.Path, "/api/v1/workspaces/"+string(f.workspace)+"/synthesis/") {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024+1))
		if err != nil || len(body) > 8*1024*1024 {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		suffix := r.URL.RequestURI()
		if strings.HasPrefix(suffix, base) {
			suffix = strings.TrimPrefix(suffix, base)
		}
		response := call(r.Method, suffix, body, true)
		for key, values := range response.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	defer server.Close()
	manifest, err := os.OpenFile(filepath.Join(directory, "server.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	check(err)
	check(json.NewEncoder(manifest).Encode(map[string]any{"url": server.URL, "workspace_id": f.workspace, "processing_id": processing.ID, "note_id": before.Note.ID}))
	check(manifest.Close())
	t.Logf("live browser fixture ready: %s", filepath.Join(directory, "server.json"))
	wait := func() {
		t.Helper()
		select {
		case <-advance:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(12 * time.Minute):
			t.Fatal("browser checkpoint timed out")
		}
	}
	wait()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		processing, err = store.GetSynthesisProcessing(ctx, f.workspace, processing.ID)
		check(err)
		if processing.Status != app.SynthesisProcessingRunning && processing.Status != app.SynthesisProcessingPending {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if processing.Status != app.SynthesisProcessingSucceeded || len(processing.RevisionIDs) != 1 || calls() != expectedCalls {
		t.Fatalf("browser did not finish original processing: %+v calls=%d", processing, calls())
	}
	after, err := f.service.GetNote(ctx, f.workspace, before.Note.ID)
	check(err)
	if after.CurrentRevision.Manuscript == nil || after.CurrentRevision.ParentRevisionID != before.CurrentRevision.ID || after.PublishedRevision.ID != before.PublishedRevision.ID {
		t.Fatal("browser candidate lost parent/publication identity")
	}
	if !strings.Contains(after.CurrentRevision.Manuscript.FullContent, "Browser manual annotation retained.") {
		t.Fatal("browser full text was not retained")
	}
	disk, err := os.ReadFile(filepath.Join(f.root, path))
	check(err)
	if string(disk) != string(original) {
		t.Fatal("browser review published the file")
	}
	f.count(t, "organizing.synthesis_manuscript_review_decision", "workspace_id", string(f.workspace), 3)
	f.count(t, "organizing.synthesis_manuscript_receipt", "workspace_id", string(f.workspace), 2)
	mu.Lock()
	phase = "candidate"
	mu.Unlock()
	wait()
	// 仅在此 t.TempDir Git 仓库提交人工基线，保留生产环境要求工作区干净的门禁；获批写回自行创建提交。
	synthesisGit(t, ctx, f.root, "add", "--", path)
	synthesisGit(t, ctx, f.root, "commit", "-m", "isolated manual manuscript baseline")
	prepared := f.preparePublication(t, after)
	written, err := f.node.Execute(ctx, prepared.input, prepared.identity)
	check(err)
	f.assertPublished(t, after, path, written)
	if replay, err := f.node.Execute(ctx, prepared.input, prepared.identity); err != nil || replay != written {
		t.Fatalf("publication replay changed: %+v %v", replay, err)
	}
	if calls() != expectedCalls {
		t.Fatal("publication called the model again")
	}
	mu.Lock()
	phase = "published"
	mu.Unlock()
	wait()
	t.Log("live browser two-stage review, actual candidate, approval/Git and refreshed reading passed")
}
