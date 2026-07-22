//go:build integration

package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	approvaldispatchpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/approvaldispatchpostgres"
	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	changecontrolhttp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/http"
	changecontrolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/changecontrol"
	toolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/postgres"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestApprovalDispatchRealRiverSafeWritebackSmoke(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Safe Writeback v1 smoke requires local POSIX filesystem semantics")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, cleanup := newApprovalRiverSmokeDatabase(t, ctx)
	defer cleanup()

	root := t.TempDir()
	targetPath := "docs/approval-river-smoke.md"
	baseContent := []byte("# Approval River Smoke\n\nbase\n")
	approvedContent := "# Approval River Smoke\n\napproved\n\ncredential=" + reindexCredentialCanary + "\ndsn=" + reindexDSNCanary + "\n"
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(targetPath)), baseContent, 0o644); err != nil {
		t.Fatal(err)
	}
	runSmokeGit(t, ctx, root, "init", "--initial-branch=main")
	runSmokeGit(t, ctx, root, "config", "user.name", "ZHIXU River Smoke")
	runSmokeGit(t, ctx, root, "config", "user.email", "river-smoke@example.invalid")
	runSmokeGit(t, ctx, root, "add", "--", targetPath)
	runSmokeGit(t, ctx, root, "commit", "-m", "base")
	baseHead := strings.TrimSpace(runSmokeGit(t, ctx, root, "rev-parse", "HEAD"))

	ids := foundation.NewUUIDGenerator(nil)
	workspaceID := mustRiverSmokeID(t, ids)
	workspaceRepository, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Second)
	if _, err := workspaceRepository.CreateWorkspace(ctx, workspacedomain.Workspace{
		ID: workspaceID, Name: "Approval River Smoke", RootPath: root,
		Git:    workspacedomain.GitBaseline{RepositoryPath: root, Branch: "main", Head: baseHead, CheckedAt: now},
		Status: workspacedomain.WorkspaceStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	changeRepository, err := changecontrolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	targetReader, err := changecontrollocalfs.NewReader(workspaceRepository)
	if err != nil {
		t.Fatal(err)
	}
	gitRepository, err := gitcli.NewWritebackClient(gitcli.New(""), workspaceRepository)
	if err != nil {
		t.Fatal(err)
	}
	insertClient, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(insertClient)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRepository, err := workflowpostgres.NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	dispatchRepository, err := approvaldispatchpostgres.NewApprovalDispatchRepository(pool, runtimeRepository, ids, foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	changeService, err := application.NewServiceWithDispatch(changeRepository, ids, foundation.SystemClock{}, targetReader, gitRepository, dispatchRepository)
	if err != nil {
		t.Fatal(err)
	}
	created, err := changeService.CreateProposal(ctx, application.CreateCommand{
		WorkspaceID: workspaceID, TargetPath: targetPath, IdempotencyKey: "approval-river-smoke",
		BaseHash: domain.ComputeWritebackResultHash(baseContent), Content: approvedContent,
		EvidenceSummary: "real River smoke", RiskLevel: domain.ProposalRiskLevelLow, Risk: "low", RollbackPlan: "revert commit",
	})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	changecontrolhttp.NewHandler(changeService).Routes(router)
	decision := approveRiverSmokeHTTP(t, ctx, router, created.Proposal, http.StatusCreated)
	workflowRunID := foundation.ID(decision.WorkflowRunID)
	var workflowNodeID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM workflow.node_run WHERE run_id=$1`, string(workflowRunID)).Scan(&workflowNodeID); err != nil {
		t.Fatal(err)
	}

	validator, err := changecontrollocalfs.NewDefaultMarkdownValidator()
	if err != nil {
		t.Fatal(err)
	}
	workspaceStore, err := changecontrollocalfs.NewWriter(workspaceRepository, validator)
	if err != nil {
		t.Fatal(err)
	}
	contractRegistry, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	toolRepository, err := toolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	auditService, err := toolsapplication.NewTrustedWriteAuditService(contractRegistry, toolRepository, ids, foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	auditRecorder, err := toolchangecontrol.NewWritebackAuditRecorder(auditService)
	if err != nil {
		t.Fatal(err)
	}
	writebackService, err := application.NewWritebackService(application.WritebackServiceDependencies{Repository: changeRepository, Workspace: workspaceStore, Git: gitRepository, Audit: auditRecorder, IDs: ids, Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	node, err := changecontrolworkflow.NewNode(writebackService)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := changecontrolworkflow.NewBootstrapExecutor(changecontrolworkflow.BootstrapExecutorDependencies{Lookup: changeRepository, ChangeControl: changeService, Beginner: writebackService, Node: node, IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := workflowapplication.NewValidationCatalog([]int{1}, []workflowdomain.Permission{workflowdomain.PermissionWriteKnowledge, workflowdomain.PermissionGitWrite})
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := executors.Register(changecontrolworkflow.SafeWritebackNodeKind, changecontrolworkflow.SafeWritebackBootstrapInputSchemaVersion, bootstrap); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapplication.NewRuntimeCoordinator(runtimeRepository)
	if err != nil {
		t.Fatal(err)
	}
	newWorkerClient := func(owner string) *riveradapter.Client {
		runtimeWorker, workerErr := riveradapter.NewRuntimeNodeWorker(executors, coordinator, owner, 10*time.Second, time.Second)
		if workerErr != nil {
			t.Fatal(workerErr)
		}
		workers := riveradapter.NewWorkers()
		if workerErr := riveradapter.AddRuntimeWorkerSafely(workers, runtimeWorker); workerErr != nil {
			t.Fatal(workerErr)
		}
		client, workerErr := riveradapter.NewClient(pool, workers)
		if workerErr != nil {
			t.Fatal(workerErr)
		}
		return client
	}
	workerClients := []*riveradapter.Client{newWorkerClient("river-smoke-a"), newWorkerClient("river-smoke-b")}
	for _, client := range workerClients {
		if err := client.Start(ctx); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		for _, client := range workerClients {
			if err := client.Stop(stopCtx); err != nil {
				t.Errorf("stop worker: %v", err)
			}
		}
	}()

	var runStatus, proposalStatus, executionStatus string
	var executionID string
	var gitCommit *string
	deadline := time.NewTicker(100 * time.Millisecond)
	defer deadline.Stop()
	for {
		err := pool.QueryRow(ctx, `SELECT r.status,p.status,e.id::text,e.status,e.git_commit
			FROM workflow.run r
			JOIN change_control.proposal p ON p.workflow_run_id=r.id
			JOIN change_control.writeback_execution e ON e.workflow_run_id=r.id
			WHERE r.id=$1`, string(workflowRunID)).Scan(&runStatus, &proposalStatus, &executionID, &executionStatus, &gitCommit)
		if err == nil && runStatus == string(workflowdomain.RunStatusSucceeded) {
			break
		}
		if err != nil && err != pgx.ErrNoRows {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("workflow did not complete: run=%s proposal=%s execution=%s err=%v", runStatus, proposalStatus, executionStatus, ctx.Err())
		case <-deadline.C:
		}
	}
	if proposalStatus != string(domain.StatusVerifying) || executionStatus != string(domain.WritebackStatusVerifying) || executionID == "" || gitCommit == nil || !domain.ValidGitHead(*gitCommit) {
		t.Fatalf("run=%s proposal=%s execution=%s execution_id=%s git=%v", runStatus, proposalStatus, executionStatus, executionID, gitCommit)
	}
	if content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(targetPath))); err != nil || string(content) != approvedContent {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if count := strings.TrimSpace(runSmokeGit(t, ctx, root, "rev-list", "--count", "HEAD")); count != "2" {
		t.Fatalf("commit count=%s", count)
	}
	replayed := approveRiverSmokeHTTP(t, ctx, router, created.Proposal, http.StatusOK)
	if replayed.WorkflowRunID != decision.WorkflowRunID || replayed.WorkflowStatusURL != decision.WorkflowStatusURL || replayed.DispatchStatus != string(application.DispatchStatusReplayed) {
		t.Fatalf("first=%#v replayed=%#v", decision, replayed)
	}
	checks := []struct {
		query string
		args  []any
	}{
		{query: `SELECT count(*) FROM change_control.writeback_execution WHERE workflow_run_id=$1`, args: []any{string(workflowRunID)}},
		{query: `SELECT count(*) FROM change_control.proposal_commit WHERE writeback_execution_id=$1`, args: []any{executionID}},
		{query: `SELECT count(*) FROM workflow.outbox_event WHERE run_id=$1 AND event_type='retrieval.revision.reindex_requested'`, args: []any{string(workflowRunID)}},
		{query: `SELECT count(*) FROM workflow.river_job WHERE kind=$1 AND args->>'node_run_id'=$2`, args: []any{riveradapter.NodeJobKind, workflowNodeID}},
		{query: `SELECT count(*) FROM workflow.tool_call WHERE workflow_run_id=$1 AND node_run_id=$2 AND status='SUCCEEDED' AND side_effect_type='writeback_execution' AND side_effect_id=$3`, args: []any{string(workflowRunID), workflowNodeID, executionID}},
	}
	for index, check := range checks {
		var count int
		want := 1
		if index == len(checks)-1 {
			want = 2
		}
		if err := pool.QueryRow(ctx, check.query, check.args...).Scan(&count); err != nil || count != want {
			t.Fatalf("count=%d want=%d err=%v query=%s", count, want, err, check.query)
		}
	}
	assertApprovalRuntimePayloadsClean(t, ctx, pool, workflowRunID, foundation.ID(workflowNodeID), root, targetPath, approvedContent)
	stopContext, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	for _, client := range workerClients {
		if err := client.Stop(stopContext); err != nil {
			stopCancel()
			t.Fatal(err)
		}
	}
	stopCancel()
	workerClients = nil
	runReindexRiverFaultSmoke(t, ctx, pool, workspaceRepository, gitRepository, insertClient,
		workspaceID, foundation.ID(executionID), root, targetPath, approvedContent)
}

type riverSmokeApprovalResponse struct {
	WorkflowRunID     string `json:"workflow_run_id"`
	WorkflowStatusURL string `json:"workflow_status_url"`
	DispatchStatus    string `json:"dispatch_status"`
}

func approveRiverSmokeHTTP(t *testing.T, ctx context.Context, handler http.Handler, proposal domain.Proposal, wantStatus int) riverSmokeApprovalResponse {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"revision_id": proposal.Revision.ID,
		"change_hash": proposal.Revision.ChangeHash,
		"decision":    domain.DecisionApproved,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/proposals/"+string(proposal.ID)+"/approvals", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("approval status=%d want=%d body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
	var response riverSmokeApprovalResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.WorkflowRunID == "" || response.WorkflowStatusURL != "/api/v1/workflows/"+response.WorkflowRunID || response.DispatchStatus == "" {
		t.Fatalf("approval response=%#v", response)
	}
	return response
}

func assertApprovalRuntimePayloadsClean(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID, nodeID foundation.ID, workspaceRoot, targetPath, approvedContent string) {
	t.Helper()
	queries := []string{
		`SELECT concat_ws(' ',input::text,COALESCE(output::text,''),COALESCE(error_summary,'')) FROM workflow.node_run WHERE id=$1`,
		`SELECT COALESCE(string_agg(concat_ws(' ',delivery_id,COALESCE(error_summary,'')), ' '),'') FROM workflow.node_attempt WHERE node_run_id=$1`,
		`SELECT COALESCE(string_agg(payload::text,' '),'') FROM workflow.outbox_event WHERE run_id=$1 AND event_type='workflow.run.started'`,
		`SELECT COALESCE(string_agg(args::text,' '),'') FROM workflow.river_job WHERE kind=$1 AND args->>'node_run_id'=$2`,
	}
	for index, query := range queries {
		var payload string
		args := []any{string(nodeID)}
		if index == 2 {
			args = []any{string(runID)}
		}
		if index == 3 {
			args = []any{riveradapter.NodeJobKind, string(nodeID)}
		}
		if err := pool.QueryRow(ctx, query, args...).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(payload)
		for _, forbidden := range []string{"credential", "lock_token", "git_args", strings.ToLower(workspaceRoot), strings.ToLower(targetPath), strings.ToLower(approvedContent)} {
			if forbidden != "" && strings.Contains(lower, forbidden) {
				t.Fatalf("runtime payload %d leaked %q: %s", index, forbidden, payload)
			}
		}
	}
	var credentialColumns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema IN ('workflow','change_control') AND column_name ILIKE '%credential%'`).Scan(&credentialColumns); err != nil {
		t.Fatal(err)
	}
	if credentialColumns != 0 {
		t.Fatalf("persistent credential columns=%d", credentialColumns)
	}
}

func mustRiverSmokeID(t *testing.T, ids foundation.IDGenerator) foundation.ID {
	t.Helper()
	id, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newApprovalRiverSmokeDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for real River smoke")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_approval_river_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier)
		admin.Close()
		t.Fatal(err)
	}
	runner, err := platformmigration.NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	}
}
