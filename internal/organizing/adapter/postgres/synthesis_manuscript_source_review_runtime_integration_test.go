//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingagent "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/agent"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/manuscript"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"gorm.io/gorm"
)

type sourceReviewRuntimeTestHook struct{ mode string }

func TestSynthesisManuscriptSourceReviewRuntime(t *testing.T) {
	for _, mode := range []string{"multi_obligation", "recovery_retry", "target_hash", "supported", "rejected", "missing_obligation", "unknown_provider", "file_drift", "source_drift", "root_revoked", "accepted_recovery", "known_failure", "terminal_recovery", "cancel_pending", "expired_pending", "recheck_roundtrip", "historical_failure", "interrupted_call"} {
		t.Run(mode, func(t *testing.T) {
			testSynthesisManuscriptRuntimeWithHook(t, "audit_duplicate", &sourceReviewRuntimeTestHook{mode})
		})
	}
}

type sourceReviewProvider struct {
	mu       sync.Mutex
	count    int
	mode     string
	requests []agentapp.ChatRequest
	entered  chan struct{}
	release  chan struct{}
}

func (p *sourceReviewProvider) Chat(_ context.Context, r agentapp.ChatRequest) (agentapp.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count++
	if p.mode == "interrupted_call" {
		close(p.entered)
		p.mu.Unlock()
		<-p.release
		p.mu.Lock()
	}
	p.requests = append(p.requests, r)
	var payload struct {
		Targets []struct {
			Label       string `json:"label"`
			FullContent string `json:"full_content"`
			Paragraphs  []struct {
				Label string `json:"label"`
				Text  string `json:"text"`
			} `json:"paragraphs"`
		} `json:"targets"`
		Sources     []struct{ Label, Text string } `json:"sources"`
		Obligations []struct {
			Label, Note, Source string
			Statement           string `json:"untrusted_historical_statement"`
		} `json:"obligations"`
	}
	found := false
	for _, m := range r.Messages {
		if strings.HasPrefix(m.Content, "UNTRUSTED TASK INPUT") {
			if err := json.Unmarshal([]byte(m.Content[strings.IndexByte(m.Content, '\n')+1:]), &payload); err != nil {
				return agentapp.ChatResponse{}, err
			}
			found = true
		}
	}
	if !found {
		return agentapp.ChatResponse{}, fmt.Errorf("provider did not receive structured current fulltext")
	}
	if len(payload.Targets) == 0 || len(payload.Sources) == 0 || !strings.Contains(payload.Targets[0].FullContent, "User transaction annotation: keep verbatim.") {
		return agentapp.ChatResponse{}, fmt.Errorf("provider input omitted full current context or original source")
	}
	if (p.mode == "known_failure" || p.mode == "historical_failure") && p.count == 1 {
		return agentapp.ChatResponse{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "MODEL_CHAT_RATE_LIMITED", true, fmt.Errorf("definite provider failure"))
	}
	if p.mode == "unknown_provider" {
		return agentapp.ChatResponse{}, context.DeadlineExceeded
	}
	checks := []app.SynthesisSourceReviewCheck{}
	for _, o := range payload.Obligations {
		c := app.SynthesisSourceReviewCheck{Obligation: o.Label, Source: o.Source, Targets: []string{}, Verdict: "SUPPORTED", ReasonCode: "CURRENT_TEXT_SUPPORTED"}
		for _, t := range payload.Targets {
			if t.Label != o.Note {
				continue
			}
			for _, part := range t.Paragraphs {
				if strings.Contains(strings.ReplaceAll(part.Text, "\\", ""), o.Statement) {
					c.Targets = append(c.Targets, part.Label)
					break
				}
			}
		}
		if p.mode == "multi_obligation" {
			c.Targets = []string{payload.Targets[0].Paragraphs[0].Label}
		}
		if p.mode == "rejected" {
			c.Verdict = "UNSUPPORTED"
			c.ReasonCode = "CURRENT_TEXT_CONTRADICTS"
			c.Targets = []string{}
		}
		checks = append(checks, c)
	}
	if p.mode == "missing_obligation" {
		checks = append(checks, app.SynthesisSourceReviewCheck{Obligation: "O999", Source: "S001", Targets: []string{}, Verdict: "UNCERTAIN", ReasonCode: "UNCERTAIN"})
	}
	raw, err := json.Marshal(app.SynthesisSourceReviewOutput{Checks: checks})
	return agentapp.ChatResponse{Model: r.Model, Content: raw, Usage: agentdomain.TokenUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}}, err
}
func (p *sourceReviewProvider) calls() int { p.mu.Lock(); defer p.mu.Unlock(); return p.count }

type sourceReviewTestStore struct {
	*GORMSynthesisManuscriptSourceReviewStore
	mode, path    string
	once          sync.Once
	recoveryMu    sync.Mutex
	firstRecovery foundation.ID
	probeErr      error
}

func (s *sourceReviewTestStore) CompleteSourceReview(ctx context.Context, e workflowapp.ExecutionContext, id foundation.ID, output []byte) (app.SynthesisManuscriptSourceReview, error) {
	r, err := s.GORMSynthesisManuscriptSourceReviewStore.CompleteSourceReview(ctx, e, id, output)
	if err == nil && s.mode == "accepted_recovery" {
		return app.SynthesisManuscriptSourceReview{}, fmt.Errorf("injected response loss after accepted output commit")
	}
	return r, err
}
func (s *sourceReviewTestStore) ApplySourceReview(ctx context.Context, e workflowapp.ExecutionContext, id foundation.ID) (app.SynthesisManuscriptSourceReview, error) {
	if (s.mode == "terminal_recovery" || s.mode == "recovery_retry") && e.NodeKind == app.SynthesisSourceReviewApply {
		return app.SynthesisManuscriptSourceReview{}, app.SourceReviewError("SOURCE_REVIEW_TEST_APPLY_INTERRUPTED")
	}
	if s.mode == "recovery_retry" && e.NodeKind == app.SynthesisSourceReviewRecover {
		s.recoveryMu.Lock()
		if s.firstRecovery == "" {
			s.firstRecovery = e.RunID
		}
		failed := s.firstRecovery == e.RunID
		s.recoveryMu.Unlock()
		if failed {
			return app.SynthesisManuscriptSourceReview{}, app.SourceReviewError("SOURCE_REVIEW_TEST_RECOVERY_INTERRUPTED")
		}
	}
	if s.mode == "target_hash" {
		s.once.Do(func() {
			for _, bad := range []string{"hash", "target_identity", "identity"} {
				err := s.runtime.dependencies.Candidates.database.Transaction(func(tx *gorm.DB) error {
					query := `INSERT INTO organizing.synthesis_manuscript_source_evidence(id,workspace_id,review_id,note_id,base_revision_id,target_hash,full_content_hash,obligation,paragraph,start_byte,end_byte,paragraph_hash,source_id,source_version_id,content_artifact_id,parse_projection_id,source_span_id,content_hash,excerpt_hash,title,created_at)
SELECT gen_random_uuid(),r.workspace_id,r.id,CASE WHEN ?='target_identity' THEN gen_random_uuid() ELSE (t->>'note_id')::uuid END,(t->>'base_revision_id')::uuid,CASE WHEN ?='hash' THEN repeat('a',64) ELSE organizing.source_review_frozen_target_hash(r.snapshot,(t->>'note_id')::uuid) END,t->>'full_content_hash',o->>'label',p->>'label',(p->>'start_byte')::int,(p->>'end_byte')::int,p->>'hash',CASE WHEN ?='identity' THEN gen_random_uuid() ELSE (o->'reference'->'source'->>'source_id')::uuid END,(o->'reference'->'source'->>'source_version_id')::uuid,(o->'reference'->'source'->>'content_artifact_id')::uuid,(o->'reference'->'source'->>'parse_projection_id')::uuid,(o->'reference'->>'source_span_id')::uuid,o->'reference'->'source'->>'content_hash',o->'reference'->>'excerpt_hash',o->'reference'->>'title',clock_timestamp()
FROM organizing.synthesis_manuscript_source_review r CROSS JOIN LATERAL jsonb_array_elements(convert_from(r.snapshot,'UTF8')::jsonb->'targets') t CROSS JOIN LATERAL jsonb_array_elements(convert_from(r.snapshot,'UTF8')::jsonb->'obligations') o CROSS JOIN LATERAL jsonb_array_elements(t->'paragraphs') p
WHERE r.id=? AND p->>'label'=(convert_from(r.output,'UTF8')::jsonb->'checks'->0->'targets'->>0) LIMIT 1`
					return tx.Exec(query, bad, bad, bad, string(id)).Error
				})
				if err == nil || !strings.Contains(err.Error(), "SQLSTATE 23514") || (bad == "hash" && !strings.Contains(err.Error(), "source evidence lacks exact")) {
					s.probeErr = fmt.Errorf("invalid %s accepted or wrong guard: %v", bad, err)
				}
			}
		})
	}
	if s.probeErr != nil {
		return app.SynthesisManuscriptSourceReview{}, s.probeErr
	}
	if s.mode == "file_drift" {
		s.once.Do(func() {
			file, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				panic(err)
			}
			_, _ = file.WriteString("\nNew manual annotation after model review.\n")
			_ = file.Close()
		})
	}
	if s.mode == "source_drift" {
		row, err := s.GetSourceReview(ctx, e.WorkspaceID, id)
		if err != nil {
			return row, err
		}
		source := row.Snapshot.Obligations[0].Source.Source.SourceID
		if err = s.runtime.dependencies.Candidates.database.WithContext(ctx).Exec("UPDATE core.source SET removed_at=clock_timestamp() WHERE workspace_id=? AND id=?", string(e.WorkspaceID), string(source)).Error; err != nil {
			return row, err
		}
	}
	if s.mode == "root_revoked" {
		if err := s.runtime.dependencies.Candidates.database.WithContext(ctx).Exec("UPDATE core.workspace SET status='inactive',version=version+1,updated_at=clock_timestamp() WHERE id=?", string(e.WorkspaceID)).Error; err != nil {
			return app.SynthesisManuscriptSourceReview{}, err
		}
	}
	return s.GORMSynthesisManuscriptSourceReviewStore.ApplySourceReview(ctx, e, id)
}
func (h *sourceReviewRuntimeTestHook) run(t *testing.T, f *synthesisGitFixture, manuscripts *SynthesisManuscriptRuntime, models *agentpostgres.GORMRepository, runs *workflowpostgres.GORMRepository, runtime *workflowpostgres.GORMRuntimeRepository, origin app.SynthesisProcessing, before app.SynthesisNoteDetail, originalCalls func() int, oldClient interface{ Stop(context.Context) error }) {
	t.Helper()
	ctx := t.Context()
	stop, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := oldClient.Stop(stop); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	store, err := NewGORMSynthesisManuscriptSourceReviewStore(manuscripts, models, manuscript.Mapper{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err = workflowpostgres.NewGORMRuntimeRepositoryWithHooks(f.platform, riveradapter.DefaultOptions(), riveradapter.NewStaticScopedEnqueueFence(), workflowpostgres.GORMRuntimeRepositoryHooks{Terminal: store})
	if err != nil {
		t.Fatal(err)
	}
	var target string
	if err = f.db.Raw("SELECT canonical_path FROM core.document WHERE workspace_id=? AND id=?", string(f.workspace), string(before.Note.DocumentID)).Scan(&target).Error; err != nil {
		t.Fatal(err)
	}
	originalFile, err := os.ReadFile(filepath.Join(f.root, target))
	if err != nil {
		t.Fatal(err)
	}
	if h.mode == "recheck_roundtrip" {
		initialFile := append([]byte{}, originalFile...)
		defer func() {
			if err := os.WriteFile(filepath.Join(f.root, target), initialFile, 0600); err != nil {
				t.Error(err)
			}
		}()
		synthesisGit(t, ctx, f.root, "add", "--", target)
		synthesisGit(t, ctx, f.root, "commit", "-m", "source review isolated fixture baseline")
		prepared := f.preparePublication(t, before)
		written, err := f.node.Execute(ctx, prepared.input, prepared.identity)
		if err != nil {
			t.Fatal(err)
		}
		f.assertPublished(t, before, target, written)
		before, err = f.service.GetNote(ctx, f.workspace, before.Note.ID)
		if err != nil {
			t.Fatal(err)
		}
		originalFile, err = os.ReadFile(filepath.Join(f.root, target))
		if err != nil {
			t.Fatal(err)
		}
	}
	wrapped := &sourceReviewTestStore{GORMSynthesisManuscriptSourceReviewStore: store, mode: h.mode, path: filepath.Join(f.root, target)}
	catalog := agentapp.NewRuntimeCatalog()
	if err = organizingagent.RegisterSourceReviewRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "source-review-test", Version: "v1"}, Model: agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "source-review", ModelVersion: "v1"}, Timeout: time.Second, MaxOutputTokens: 2048}
	if err = catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err = catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	scheduler, err := agenteino.NewStructuredPhaseScheduler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	provider := &sourceReviewProvider{mode: h.mode, entered: make(chan struct{}), release: make(chan struct{})}
	model, err := organizingagent.NewSourceReviewModel(organizingagent.SourceReviewModelDependencies{Model: provider, ModelRuns: models, Store: wrapped, Catalog: catalog, Scheduler: scheduler, ProfileRef: profile.Ref, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	validation, err := workflowapp.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapp.NewExecutorRegistry(validation)
	if err != nil {
		t.Fatal(err)
	}
	executor := &organizingworkflow.SourceReviewExecutor{Runs: runs, Store: wrapped, Model: model}
	for _, kind := range []string{app.SynthesisSourceReviewPrepare, app.SynthesisSourceReviewModel, app.SynthesisSourceReviewApply, app.SynthesisSourceReviewRecover} {
		if err = executors.Register(kind, 1, executor); err != nil {
			t.Fatal(err)
		}
	}
	if err = executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := workflowapp.NewDefinitionRegistry(validation, executors)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range organizingworkflow.SourceReviewDefinitions() {
		if err = definitions.Register(d); err != nil {
			t.Fatal(err)
		}
	}
	if err = definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapp.NewRuntimeCoordinator(runtime)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := riveradapter.NewRuntimeNodeWorker(executors, coordinator, "source-review-integration", 10*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	workers := riveradapter.NewWorkers()
	if err = riveradapter.AddRuntimeWorkerSafely(workers, worker); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(f.platform.DB(), workers)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Stop(stop)
	})
	workspaces, err := workspacepostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &organizingworkflow.SourceReviewDispatcher{Workspaces: workspaces, Store: store, UnitOfWork: f.uow, Starter: runtime, Definitions: definitions, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}}
	if err = store.ConfigureSourceReviewCommands(runtime, definitions); err != nil {
		t.Fatal(err)
	}
	commands, err := app.NewSynthesisManuscriptSourceReviewCommandService(store, app.NewSynthesisManuscriptCaller([]capability.Capability{capability.ReadLocal, capability.WriteProposal}))
	if err != nil {
		t.Fatal(err)
	}
	if h.mode == "cancel_pending" || h.mode == "expired_pending" {
		stop, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = client.Stop(stop)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	originalClock := manuscripts.dependencies.Storage.Clock
	if h.mode == "expired_pending" {
		manuscripts.dependencies.Storage.Clock = sourceReviewOldClock{}
	}
	n, err := dispatcher.DispatchBatch(ctx, 1)
	if err != nil || n != 1 {
		t.Fatalf("dispatch=%d err=%v", n, err)
	}
	manuscripts.dependencies.Storage.Clock = originalClock
	if h.mode == "cancel_pending" || h.mode == "expired_pending" {
		rows, err := store.ListSourceReviews(ctx, f.workspace, origin.ID)
		if err != nil || len(rows) != 1 {
			t.Fatalf("pending row: %v", err)
		}
		if h.mode == "cancel_pending" {
			run, err := runs.GetRun(ctx, rows[0].WorkflowRunID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = coordinator.Cancel(ctx, workflowapp.RunControlCommand{WorkflowRunID: run.ID, ExpectedVersion: run.Version, IdempotencyKey: "source-review-test-cancel", CallerCapabilities: capability.All()}); err != nil {
				t.Fatal(err)
			}
		} else {
			if n, err := store.ReconcileSourceReviews(ctx, 10); err != nil || n > 1 {
				t.Fatalf("expired reconcile=%d %v", n, err)
			}
		}
		row, err := store.GetSourceReview(ctx, f.workspace, rows[0].ID)
		if err != nil || row.Status != "FAILED" || provider.calls() != 0 {
			t.Fatalf("pending reduction=%s calls=%d err=%v", row.Status, provider.calls(), err)
		}
		t.Logf("%s actual owner reduction, zero Provider calls", h.mode)
		return
	}
	if h.mode == "interrupted_call" {
		select {
		case <-provider.entered:
		case <-time.After(15 * time.Second):
			t.Fatal("provider not entered")
		}
		// 真实 Workflow 取消请求使本次尝试失去资格；协调流程记录未知状态，不伪造提供方结果。
		rows, err := store.ListSourceReviews(ctx, f.workspace, origin.ID)
		if err != nil || len(rows) != 1 {
			t.Fatal(err)
		}
		run, err := runs.GetRun(ctx, rows[0].WorkflowRunID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = coordinator.Cancel(ctx, workflowapp.RunControlCommand{WorkflowRunID: run.ID, ExpectedVersion: run.Version, IdempotencyKey: "source-review-running-cancel", CallerCapabilities: capability.All()})
		if err != nil {
			close(provider.release)
			t.Fatal(err)
		}
		if _, err = store.ReconcileSourceReviews(ctx, 10); err != nil {
			close(provider.release)
			t.Fatal(err)
		}
		row, err := store.GetSourceReview(ctx, f.workspace, rows[0].ID)
		if err != nil {
			close(provider.release)
			t.Fatal(err)
		}
		record, err := models.GetModelRun(ctx, f.workspace, row.ModelRunID)
		close(provider.release)
		if err != nil || row.Status != "RECOVERY_REQUIRED" || record.Run.Status != agentdomain.ModelRunUnknown || len(record.Calls) != 1 || record.Calls[0].Status != agentdomain.ModelCallUnknown {
			t.Fatalf("interrupted call was not reduced unknown: row=%s run=%s calls=%+v err=%v", row.Status, record.Run.Status, record.Calls, err)
		}
		waitSourceReviewWorkflow(t, ctx, runs, row.WorkflowRunID)
		if provider.calls() != 1 {
			t.Fatal("interrupted call rerun")
		}
		t.Log("actual running cancellation closes original STARTED call and RUNNING model as UNKNOWN; late output cannot overwrite; zero new Provider calls")
		return
	}
	var current app.SynthesisManuscriptSourceReview
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		rows, e := store.ListSourceReviews(ctx, f.workspace, origin.ID)
		if e != nil {
			t.Fatal(e)
		}
		if len(rows) == 1 {
			current = rows[0]
			if (h.mode == "terminal_recovery" || h.mode == "recovery_retry") && current.Status == "REVIEWED" {
				run, e := runs.GetRun(ctx, current.WorkflowRunID)
				if e == nil && run.Status == "failed" {
					break
				}
			}
			if current.Status == "SUCCEEDED" || current.Status == "REJECTED" || current.Status == "STALE" || current.Status == "FAILED" || current.Status == "RECOVERY_REQUIRED" {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	expected := "SUCCEEDED"
	if h.mode == "known_failure" || h.mode == "historical_failure" {
		expected = "FAILED"
	}
	if h.mode == "terminal_recovery" || h.mode == "recovery_retry" {
		expected = "REVIEWED"
	}
	if h.mode == "rejected" {
		expected = "REJECTED"
	}
	if h.mode == "unknown_provider" {
		expected = "RECOVERY_REQUIRED"
	}
	if h.mode == "missing_obligation" {
		expected = "FAILED"
	}
	if h.mode == "file_drift" || h.mode == "source_drift" || h.mode == "root_revoked" {
		expected = "STALE"
	}
	if current.Status != expected || provider.calls() != 1 || originalCalls() != 4 {
		var debugRows []map[string]any
		_ = f.db.Raw("SELECT node_key,status,error_code,attempt FROM workflow.node_run WHERE run_id=?", string(current.WorkflowRunID)).Scan(&debugRows).Error
		t.Logf("workflow nodes=%+v", debugRows)
		t.Fatalf("review=%s code=%s wanted=%s calls=%d original=%d", current.Status, current.ErrorCode, expected, provider.calls(), originalCalls())
	}
	if h.mode == "root_revoked" {
		if view, viewErr := store.SourceReviewView(ctx, f.workspace, current.ID); viewErr == nil || view.Completed || len(view.Targets) != 0 {
			t.Fatalf("revoked root exposed current text: completed=%v targets=%d err=%v", view.Completed, len(view.Targets), viewErr)
		}
		if err = f.db.WithContext(ctx).Exec("UPDATE core.workspace SET status='active',version=version+1,updated_at=clock_timestamp() WHERE id=?", string(f.workspace)).Error; err != nil {
			t.Fatal(err)
		}
	}
	after, err := f.service.GetNote(ctx, f.workspace, before.Note.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Note.Version != before.Note.Version || after.CurrentRevision.ID != before.CurrentRevision.ID || after.CurrentRevision.ArticleRevisionID != before.CurrentRevision.ArticleRevisionID || after.PublishedRevision.ID != before.PublishedRevision.ID {
		t.Fatal("source review altered body version or publication")
	}
	if h.mode != "file_drift" {
		disk, e := os.ReadFile(filepath.Join(f.root, target))
		if e != nil || string(disk) != string(originalFile) {
			t.Fatal("source review wrote current file")
		}
	}
	var evidence int64
	if err = f.db.Table("organizing.synthesis_manuscript_source_evidence").Where("review_id=?", string(current.ID)).Count(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	if (evidence > 0) != (expected == "SUCCEEDED") {
		t.Fatalf("unexpected evidence count=%d", evidence)
	}
	if _, err = store.GetSourceReview(ctx, f.otherWorkspace, current.ID); err == nil {
		t.Fatal("cross-workspace review exposed")
	}
	if _, err = store.PrepareSourceReview(ctx, workflowapp.ExecutionContext{WorkspaceID: f.workspace, RunID: current.WorkflowRunID, NodeKind: app.SynthesisSourceReviewPrepare}, current.ID); err == nil {
		t.Fatal("forged execution authorized")
	}
	view, err := store.SourceReviewView(ctx, f.workspace, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Completed != (expected == "SUCCEEDED") {
		t.Fatalf("view completion mismatch: %+v", view)
	}
	if h.mode == "multi_obligation" {
		if evidence != 1 || view.ObligationCount != 2 || len(view.Targets[0].Evidence) != 1 || len(view.Targets[0].Evidence[0].Obligations) != 2 {
			t.Fatalf("multi obligation projection: evidence=%d view=%+v", evidence, view)
		}
		var manifest int64
		if err := f.db.Table("organizing.synthesis_manuscript_source_review_result").Where("review_id=?", current.ID).Count(&manifest).Error; err != nil || manifest != 2 {
			t.Fatalf("manifest=%d err=%v", manifest, err)
		}
		reader, err := NewSynthesisSourceReviewRead(store, f.sources)
		if err != nil {
			t.Fatal(err)
		}
		opened, source, err := reader.OpenSourceReviewEvidence(ctx, f.workspace, current.ID, view.Targets[0].Evidence[0].ID)
		if err != nil || len(opened.Obligations) != 2 || source.Reference != opened.Source {
			t.Fatalf("multi open=%+v err=%v", opened, err)
		}
		read, err := reader.GetSourceReviewView(ctx, f.workspace, current.ID)
		if err != nil || len(read.Targets[0].Evidence) != 1 || len(read.Targets[0].Evidence[0].Obligations) != 2 {
			t.Fatalf("multi refresh=%+v err=%v", read, err)
		}
		t.Log("multi obligation: 1 physical evidence + 2 manifest links; public read/Open/refresh preserve both obligations")
	}
	for range 3 {
		n, e := dispatcher.DispatchBatch(ctx, 1)
		if e != nil || n != 0 {
			t.Fatalf("redispatch=%d err=%v", n, e)
		}
	}
	if provider.calls() != 1 {
		t.Fatal("duplicate provider call")
	}
	if h.mode == "missing_obligation" || h.mode == "known_failure" || h.mode == "historical_failure" {
		record, err := models.GetModelRun(ctx, f.workspace, current.ModelRunID)
		if err != nil || record.Run.Status != agentdomain.ModelRunFailed {
			t.Fatalf("model ledger not failed: %+v %v", record.Run, err)
		}
	}
	command := app.SynthesisManuscriptSourceReviewCommand{WorkspaceID: f.workspace, ReviewID: current.ID, ExpectedVersion: current.Version, IdempotencyKey: "source-review-explicit-test"}
	if h.mode == "rejected" || h.mode == "unknown_provider" {
		if _, err := commands.Recheck(ctx, command); err == nil {
			t.Fatal("nonretryable unchanged/unknown recheck allowed")
		}
		if _, err := commands.Recover(ctx, command); err == nil {
			t.Fatal("unaccepted proof recover allowed")
		}
		if provider.calls() != 1 {
			t.Fatal("rejected command called provider")
		}
	}
	if h.mode == "known_failure" || (h.mode == "terminal_recovery" || h.mode == "recovery_retry") {
		waitSourceReviewWorkflow(t, ctx, runs, current.WorkflowRunID)
		var winner app.SynthesisSourceReviewView
		if h.mode == "known_failure" {
			type answer struct {
				view app.SynthesisSourceReviewView
				err  error
			}
			answers := make(chan answer, 2)
			for range 2 {
				go func() { v, e := commands.Recheck(ctx, command); answers <- answer{v, e} }()
			}
			a, b := <-answers, <-answers
			if a.err != nil || b.err != nil || a.view.ID != b.view.ID || a.view.ID == current.ID {
				t.Fatalf("concurrent successor: %+v %+v", a, b)
			}
			winner = a.view
		} else {
			winner, err = commands.Recover(ctx, command)
			if err != nil {
				for cause := err; cause != nil; cause = errors.Unwrap(cause) {
					t.Logf("recover error: %v", cause)
				}
				t.Fatal(err)
			}
			if winner.RecoveryWorkflowRunID == "" || winner.RecoveryWorkflowRunID == current.WorkflowRunID {
				t.Fatal("recovery did not use independent workflow")
			}
		}
		if h.mode == "recovery_retry" {
			first := winner.RecoveryWorkflowRunID
			waitSourceReviewWorkflow(t, ctx, runs, first)
			failed, err := commands.Read(ctx, f.workspace, current.ID)
			if err != nil || failed.Completed || !failed.CanRecover || failed.RecoveryStatus != "failed" {
				t.Fatalf("failed recovery not retryable: %+v %v", failed, err)
			}
			replay, err := commands.Recover(ctx, command)
			if err != nil || replay.RecoveryWorkflowRunID != first {
				t.Fatalf("old key winner changed: %+v %v", replay, err)
			}
			command.IdempotencyKey += "-second"
			winner, err = commands.Recover(ctx, command)
			if err != nil || winner.RecoveryWorkflowRunID == first {
				t.Fatalf("new independent recovery: %+v %v", winner, err)
			}
			var count int64
			if err := f.db.Table("organizing.synthesis_manuscript_source_review_recovery").Where("review_id=?", current.ID).Count(&count).Error; err != nil || count != 2 {
				t.Fatalf("recovery history=%d %v", count, err)
			}
			t.Log("failed recovery retained; old key replays original run; new key binds a second independent run")
		}
		until := time.Now().Add(30 * time.Second)
		for time.Now().Before(until) {
			winner, err = commands.Read(ctx, f.workspace, winner.ID)
			if err != nil {
				t.Fatal(err)
			}
			if winner.Completed {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		want := 2
		if h.mode == "terminal_recovery" || h.mode == "recovery_retry" {
			want = 1
		}
		if !winner.Completed || provider.calls() != want {
			t.Fatalf("explicit result completed=%v calls=%d wanted=%d failure=%s recovery=%s", winner.Completed, provider.calls(), want, winner.Failure, winner.RecoveryStatus)
		}
		replay, e := commands.Recheck(ctx, command)
		if h.mode == "terminal_recovery" || h.mode == "recovery_retry" {
			replay, e = commands.Recover(ctx, command)
		}
		if e != nil || replay.ID != winner.ID {
			t.Fatalf("command replay: %+v %v", replay, e)
		}
		original, e := store.GetSourceReview(ctx, f.workspace, current.ID)
		if e != nil || original.ModelRunID != current.ModelRunID || original.WorkflowRunID != current.WorkflowRunID {
			t.Fatal("original proof identity changed")
		}
		t.Logf("explicit %s completed with provider calls=%d", h.mode, provider.calls())
	}
	if expected == "SUCCEEDED" {
		file, e := os.OpenFile(filepath.Join(f.root, target), os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = file.WriteString("\nManual edit invalidates this support snapshot.\n")
		_ = file.Close()
		view, e = store.SourceReviewView(ctx, f.workspace, current.ID)
		if e != nil || view.Completed || view.Status != "SUCCEEDED" {
			t.Fatalf("stale successful snapshot=%+v err=%v", view, e)
		}
		if e = os.WriteFile(filepath.Join(f.root, target), originalFile, 0600); e != nil {
			t.Fatal(e)
		}
	} else if h.mode == "file_drift" {
		if err = os.WriteFile(filepath.Join(f.root, target), originalFile, 0600); err != nil {
			t.Fatal(err)
		}
		waitSourceReviewWorkflow(t, ctx, runs, current.WorkflowRunID)
		command.IdempotencyKey = "source-review-stale-recover"
		recovered, err := commands.Recover(ctx, command)
		if err != nil {
			t.Fatal(err)
		}
		until := time.Now().Add(20 * time.Second)
		for time.Now().Before(until) {
			recovered, err = commands.Read(ctx, f.workspace, current.ID)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Completed {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !recovered.Completed || recovered.Status != "STALE" || recovered.RecoveryCompletedAt == nil || recovered.ReceiptHash == "" || provider.calls() != 1 {
			t.Fatalf("immutable stale recovery: %+v calls=%d", recovered, provider.calls())
		}
		original, err := store.GetSourceReview(ctx, f.workspace, current.ID)
		if err != nil || original.Version != current.Version || original.Status != current.Status {
			t.Fatal("terminal history modified during recovery")
		}
		if err = os.WriteFile(filepath.Join(f.root, target), append(append([]byte{}, originalFile...), []byte("\nNew drift after recovered receipt\n")...), 0600); err != nil {
			t.Fatal(err)
		}
		replay, err := commands.Recover(ctx, command)
		if err != nil || replay.Completed || replay.Status != "STALE" || replay.RecoveryCompletedAt == nil || !replay.RecoveryCompletedAt.Equal(*recovered.RecoveryCompletedAt) || provider.calls() != 1 {
			t.Fatalf("stale exact receipt replay: %+v %v", replay, err)
		}
		if err = os.WriteFile(filepath.Join(f.root, target), originalFile, 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("terminal STALE history preserved; accepted proof recovered on independent run; receipt replay after drift uses zero Provider calls")
	}
	if h.mode == "recheck_roundtrip" {
		previous := current
		for index, body := range [][]byte{append(append([]byte{}, originalFile...), []byte("\nCurrent fulltext change B.\n")...), originalFile} {
			waitSourceReviewWorkflow(t, ctx, runs, previous.WorkflowRunID)
			if err = os.WriteFile(filepath.Join(f.root, target), body, 0600); err != nil {
				t.Fatal(err)
			}
			v, err := store.SourceReviewView(ctx, f.workspace, previous.ID)
			if err != nil || v.Completed || !v.CanRecheck {
				t.Fatalf("changed fulltext recheck unavailable: %+v %v", v, err)
			}
			c := app.SynthesisManuscriptSourceReviewCommand{WorkspaceID: f.workspace, ReviewID: previous.ID, ExpectedVersion: previous.Version, IdempotencyKey: fmt.Sprintf("source-review-roundtrip-%d", index)}
			winner, err := commands.Recheck(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			until := time.Now().Add(30 * time.Second)
			for time.Now().Before(until) {
				previous, err = store.GetSourceReview(ctx, f.workspace, winner.ID)
				if err != nil {
					t.Fatal(err)
				}
				if previous.Status == "SUCCEEDED" {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if previous.Status != "SUCCEEDED" {
				t.Fatalf("roundtrip successor not successful: %+v", previous)
			}
			if index == 1 {
				var owned, manifest int64
				if err = f.db.Table("organizing.synthesis_manuscript_source_evidence").Where("review_id=?", string(previous.ID)).Count(&owned).Error; err != nil {
					t.Fatal(err)
				}
				if err = f.db.Table("organizing.synthesis_manuscript_source_review_result").Where("review_id=? AND proof_review_id=?", string(previous.ID), string(current.ID)).Count(&manifest).Error; err != nil {
					t.Fatal(err)
				}
				if owned != 0 || manifest != 1 {
					t.Fatalf("A-B-A failed proof reuse: owned=%d reused=%d", owned, manifest)
				}
			}
		}
		if provider.calls() != 3 {
			t.Fatalf("roundtrip calls=%d", provider.calls())
		}
		t.Log("actual published fulltext A-B-A, independent successors and original evidence reused")
	}
	t.Logf("current-text source review mode=%s status=%s independent_calls=%d original_calls=%d evidence=%d; body/version/publication unchanged", h.mode, current.Status, provider.calls(), originalCalls(), evidence)
}

// 测试数据使用持久化的旧创建时间，不伪造租约或校验条件。
type sourceReviewOldClock struct{}

func (sourceReviewOldClock) Now() time.Time { return time.Now().Add(-31 * time.Minute) }
func waitSourceReviewWorkflow(t *testing.T, ctx context.Context, runs *workflowpostgres.GORMRepository, id foundation.ID) {
	t.Helper()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		run, err := runs.GetRun(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == "failed" || run.Status == "succeeded" || run.Status == "cancelled" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("source review workflow not terminal")
}

func (s *sourceReviewTestStore) FailSourceReview(ctx context.Context, w, id, run foundation.ID, cause error) error {
	if s.mode == "historical_failure" {
		return nil
	} // 模拟同步失败终结器丢失，由真实终态钩子负责恢复。
	return s.GORMSynthesisManuscriptSourceReviewStore.FailSourceReview(ctx, w, id, run, cause)
}
