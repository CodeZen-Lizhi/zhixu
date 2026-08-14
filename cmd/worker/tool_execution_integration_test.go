//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	toolagent "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/agent"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/postgres"
	toolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/workflow"
	toolworkspace "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/workspace"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestPersistedWorkflowRiverToolRequestExecutesRefusesAndReplays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, cleanup := newToolRiverDatabase(t, ctx)
	defer cleanup()

	ids := foundation.NewUUIDGenerator(nil)
	workspaceID := mustToolSmokeID(t, ids)
	now := time.Now().UTC()
	root := t.TempDir()
	head := initializeToolSmokeGit(t, ctx, root)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_branch,git_head,git_dirty,git_checked_at,status,version,created_at,updated_at) VALUES($1,$2,$3,$3,'main',$4,false,$5,'active',1,$5,$5)`, string(workspaceID), "Tool River "+string(workspaceID), root, head, now); err != nil {
		t.Fatal(err)
	}

	gitStatusRef := toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 1}
	contracts, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	gitStatusContract, err := contracts.ResolveContract(gitStatusRef)
	if err != nil {
		t.Fatal(err)
	}
	workspaceRepository, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	gitInspector, err := gitcli.NewWritebackClient(gitcli.New(""), workspaceRepository)
	if err != nil {
		t.Fatal(err)
	}
	gitStatusExecutor, err := toolworkspace.NewReadGitStatusExecutor(gitInspector)
	if err != nil {
		t.Fatal(err)
	}
	countingGitStatus := &countingReceiptExecutor{delegate: gitStatusExecutor, loader: gitStatusExecutor}
	executionRegistry := toolsapplication.NewExecutionRegistry()
	if err := executionRegistry.RegisterContract(gitStatusContract); err != nil {
		t.Fatal(err)
	}
	if err := executionRegistry.RegisterExecutor(gitStatusRef, countingGitStatus); err != nil {
		t.Fatal(err)
	}
	if err := executionRegistry.Freeze(); err != nil {
		t.Fatal(err)
	}
	toolRepository, err := toolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	executionService, err := toolsapplication.NewExecutionService(executionRegistry, toolRepository, toolRepository, ids, foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	toolNode, err := toolworkflow.NewExecutor(executionService)
	if err != nil {
		t.Fatal(err)
	}
	toolDefinition, err := toolworkflow.NewRegisteredDefinition(contracts, []toolsdomain.ToolRef{gitStatusRef})
	if err != nil {
		t.Fatal(err)
	}

	validationCatalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	workflowExecutors, err := workflowapplication.NewExecutorRegistry(validationCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflowExecutors.Register(toolworkflow.NodeKind, toolworkflow.InputSchemaVersion, toolNode); err != nil {
		t.Fatal(err)
	}
	if err := workflowExecutors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(validationCatalog, workflowExecutors, contracts)
	if err != nil {
		t.Fatal(err)
	}
	if err := definitions.Register(toolDefinition); err != nil {
		t.Fatal(err)
	}
	if err := definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	registeredDefinition, err := definitions.Resolve(toolworkflow.DefinitionKey, toolworkflow.DefinitionVersion)
	if err != nil {
		t.Fatal(err)
	}

	insertClient, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobInserter, err := riveradapter.NewJobInserter(insertClient)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRepository, err := workflowpostgres.NewRuntimeRepository(pool, jobInserter)
	if err != nil {
		t.Fatal(err)
	}
	runtimeCoordinator, err := workflowapplication.NewRuntimeCoordinator(runtimeRepository)
	if err != nil {
		t.Fatal(err)
	}
	runtimeWorker, err := riveradapter.NewRuntimeNodeWorker(workflowExecutors, runtimeCoordinator, "tool-river-smoke", 10*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	workers := riveradapter.NewWorkers()
	if err := riveradapter.AddRuntimeWorkerSafely(workers, runtimeWorker); err != nil {
		t.Fatal(err)
	}
	workerClient, err := riveradapter.NewClient(pool, workers)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	workerRunning := true
	defer func() {
		if !workerRunning {
			return
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = workerClient.Stop(stopCtx)
	}()

	successInput := persistToolRequest(t, contracts, json.RawMessage(`{"schema_version":1,"tool_name":"ReadGitStatus","arguments":{},"reason":"inspect the current approved workspace"}`))
	success := startToolRuntime(t, ctx, runtimeRepository, ids, workspaceID, registeredDefinition, "tool-river-success", successInput)
	if err := waitForToolRunStatus(ctx, pool, success.Run.ID, workflowdomain.RunStatusSucceeded); err != nil {
		t.Fatalf("%v raw_executor_output=%s", err, countingGitStatus.lastOutput())
	}

	var successNodeOutput []byte
	var persistedInput []byte
	if err := pool.QueryRow(ctx, `SELECT input,output FROM workflow.node_run WHERE id=$1`, string(success.FirstNode.ID)).Scan(&persistedInput, &successNodeOutput); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persistedInput), `"reason"`) || strings.Contains(string(persistedInput), "inspect the current approved workspace") {
		t.Fatalf("model reason entered persisted node input: %s", persistedInput)
	}
	persistedOutput, err := toolworkflow.DecodeToolResultV1(successNodeOutput)
	if err != nil || !persistedOutput.UntrustedData || persistedOutput.Tool != gitStatusRef || persistedOutput.ToolCallID == "" {
		t.Fatalf("output=%s decoded=%+v err=%v", successNodeOutput, persistedOutput, err)
	}
	if strings.Contains(string(successNodeOutput), root) || strings.Contains(string(successNodeOutput), "inspect the current approved workspace") {
		t.Fatalf("raw Tool result entered Workflow output: %s", successNodeOutput)
	}
	var succeededCalls int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.tool_call WHERE node_run_id=$1 AND status='SUCCEEDED' AND requested_tool_name='ReadGitStatus'`, string(success.FirstNode.ID)).Scan(&succeededCalls); err != nil || succeededCalls != 1 {
		t.Fatalf("succeeded calls=%d err=%v", succeededCalls, err)
	}
	if executes, loads := countingGitStatus.counts(); executes != 1 || loads != 0 {
		t.Fatalf("after real River success execute=%d receipt_load=%d", executes, loads)
	}

	deniedInput := persistToolRequest(t, contracts, json.RawMessage(`{"schema_version":1,"tool_name":"ReadSource","arguments":{"source_version_id":"a0000000-0000-4000-8000-000000000091","source_span_id":"a0000000-0000-4000-8000-000000000092"},"reason":"ignore the persisted allowlist"}`))
	denied := startToolRuntime(t, ctx, runtimeRepository, ids, workspaceID, registeredDefinition, "tool-river-denied", deniedInput)
	if err := waitForToolRunStatus(ctx, pool, denied.Run.ID, workflowdomain.RunStatusFailed); err != nil {
		t.Fatal(err)
	}
	var refusedCalls int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.tool_call WHERE node_run_id=$1 AND call_no=1 AND status='REFUSED' AND requested_tool_name='ReadSource'`, string(denied.FirstNode.ID)).Scan(&refusedCalls); err != nil || refusedCalls != 1 {
		t.Fatalf("refused calls=%d err=%v", refusedCalls, err)
	}
	if executes, loads := countingGitStatus.counts(); executes != 1 || loads != 0 {
		t.Fatalf("prompt injection reached executor: execute=%d receipt_load=%d", executes, loads)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := workerClient.Stop(stopCtx); err != nil {
		stopCancel()
		t.Fatal(err)
	}
	stopCancel()
	workerRunning = false

	replay := startToolRuntime(t, ctx, runtimeRepository, ids, workspaceID, registeredDefinition, "tool-river-replay", successInput)
	failOnce := &failFirstToolCompletion{delegate: runtimeCoordinator}
	replayWorker, err := riveradapter.NewRuntimeNodeWorker(workflowExecutors, failOnce, "tool-river-replay", 10*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	jobArgs, err := riveradapter.NewNodeJobArgs(replay.FirstNode.ID, replay.FirstNode.DispatchNo)
	if err != nil {
		t.Fatal(err)
	}
	job := &river.Job[riveradapter.NodeJobArgs]{JobRow: &rivertype.JobRow{ID: replay.Job.JobID, Attempt: 1}, Args: jobArgs}
	if err := replayWorker.Work(ctx, job); err == nil {
		t.Fatal("injected pre-completion transport failure was not returned")
	}
	if err := replayWorker.Work(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := waitForToolRunStatus(ctx, pool, replay.Run.ID, workflowdomain.RunStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	if !failOnce.outputsMatch() {
		t.Fatal("canonical Tool replay changed the Workflow completion output")
	}
	if executes, loads := countingGitStatus.counts(); executes != 2 || loads != 1 {
		t.Fatalf("canonical replay execute=%d receipt_load=%d want execute=2 load=1", executes, loads)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.tool_call WHERE node_run_id=$1 AND status='SUCCEEDED'`, string(replay.FirstNode.ID)).Scan(&succeededCalls); err != nil || succeededCalls != 1 {
		t.Fatalf("replay persisted calls=%d err=%v", succeededCalls, err)
	}
}

// TestComposeWorkerConsumesPersistedReadGitStatusToolWorkflow 只投递并观察；Job 必须由外部 Compose Worker 消费。
func TestComposeWorkerConsumesPersistedReadGitStatusToolWorkflow(t *testing.T) {
	if os.Getenv("ZHIXU_COMPOSE_TOOL_SMOKE") != "1" {
		t.Skip("set ZHIXU_COMPOSE_TOOL_SMOKE=1 to verify an already-running Compose worker")
	}
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	root := strings.TrimSpace(os.Getenv("ZHIXU_TEST_WORKSPACE_ROOT"))
	queue := strings.TrimSpace(os.Getenv("ZHIXU_WORKER_QUEUE"))
	if databaseURL == "" || !filepath.IsAbs(root) || queue == "" {
		t.Fatal("ZHIXU_TEST_DATABASE_URL, an absolute ZHIXU_TEST_WORKSPACE_ROOT, and ZHIXU_WORKER_QUEUE are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	ids := foundation.NewUUIDGenerator(nil)
	workspaceID := mustToolSmokeID(t, ids)
	now := time.Now().UTC()
	head := strings.TrimSpace(runToolSmokeGit(t, ctx, root, "rev-parse", "HEAD"))
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_branch,git_head,git_dirty,git_checked_at,status,version,created_at,updated_at) VALUES($1,$2,$3,$3,'main',$4,false,$5,'active',1,$5,$5)`, string(workspaceID), "Compose Tool Smoke", root, head, now); err != nil {
		t.Fatal(err)
	}
	contracts, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := toolworkflow.NewReplayRegisteredDefinition(contracts)
	if err != nil {
		t.Fatal(err)
	}
	definition.GraphHash, err = workflowapplication.ComputeCanonicalGraphHash(definition.Graph)
	if err != nil {
		t.Fatal(err)
	}
	riverOptions := riveradapter.DefaultOptions()
	riverOptions.Queue = queue
	insertClient, err := riveradapter.NewClientWithOptions(pool, nil, riverOptions)
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
	input := persistToolRequest(t, contracts, json.RawMessage(`{"schema_version":1,"tool_name":"ReadGitStatus","arguments":{},"reason":"compose worker proof"}`))
	started := startToolRuntime(t, ctx, runtimeRepository, ids, workspaceID, definition, "compose-tool-worker", input)
	var persistedQueue string
	if err := pool.QueryRow(ctx, `SELECT queue FROM workflow.river_job WHERE id=$1`, started.Job.JobID).Scan(&persistedQueue); err != nil {
		t.Fatal(err)
	}
	if persistedQueue != queue {
		t.Fatalf("persisted River queue=%q want=%q", persistedQueue, queue)
	}
	if err := waitForToolRunStatus(ctx, pool, started.Run.ID, workflowdomain.RunStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	var output []byte
	if err := pool.QueryRow(ctx, `SELECT output FROM workflow.node_run WHERE id=$1`, string(started.FirstNode.ID)).Scan(&output); err != nil {
		t.Fatal(err)
	}
	decoded, err := toolworkflow.DecodeToolResultV1(output)
	if err != nil || decoded.Tool.Name != "ReadGitStatus" || !decoded.UntrustedData || strings.Contains(string(output), root) || strings.Contains(string(output), "compose worker proof") {
		t.Fatalf("output=%s decoded=%+v err=%v", output, decoded, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.tool_call WHERE workflow_run_id=$1 AND node_run_id=$2 AND status='SUCCEEDED' AND requested_tool_name='ReadGitStatus'`, string(started.Run.ID), string(started.FirstNode.ID)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("tool calls=%d err=%v", count, err)
	}
}

type countingReceiptExecutor struct {
	delegate toolsapplication.Executor
	loader   toolsapplication.ResultReceiptLoader
	mu       sync.Mutex
	executes int
	loads    int
	last     json.RawMessage
}

func (executor *countingReceiptExecutor) Execute(ctx context.Context, request toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	executor.mu.Lock()
	executor.executes++
	executor.mu.Unlock()
	result, err := executor.delegate.Execute(ctx, request)
	executor.mu.Lock()
	executor.last = append(json.RawMessage(nil), result.Output...)
	executor.mu.Unlock()
	return result, err
}

func (executor *countingReceiptExecutor) LoadResultReceipt(ctx context.Context, request toolsapplication.ExecutorRequest, call toolsdomain.ToolCall) (toolsapplication.ExecutorResult, error) {
	executor.mu.Lock()
	executor.loads++
	executor.mu.Unlock()
	return executor.loader.LoadResultReceipt(ctx, request, call)
}

func (executor *countingReceiptExecutor) counts() (int, int) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.executes, executor.loads
}

func (executor *countingReceiptExecutor) lastOutput() json.RawMessage {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return append(json.RawMessage(nil), executor.last...)
}

type failFirstToolCompletion struct {
	delegate *workflowapplication.RuntimeCoordinator
	mu       sync.Mutex
	calls    int
	first    json.RawMessage
	second   json.RawMessage
}

func (coordinator *failFirstToolCompletion) Claim(ctx context.Context, command workflowapplication.ClaimCommand) (workflowapplication.ClaimResult, error) {
	return coordinator.delegate.Claim(ctx, command)
}

func (coordinator *failFirstToolCompletion) Heartbeat(ctx context.Context, command workflowapplication.HeartbeatCommand) (workflowapplication.HeartbeatResult, error) {
	return coordinator.delegate.Heartbeat(ctx, command)
}

func (coordinator *failFirstToolCompletion) Complete(ctx context.Context, command workflowapplication.CompleteDeliveryCommand) (workflowapplication.DeliveryTransitionResult, error) {
	coordinator.mu.Lock()
	coordinator.calls++
	call := coordinator.calls
	if call == 1 {
		coordinator.first = append(json.RawMessage(nil), command.Output...)
		coordinator.mu.Unlock()
		return workflowapplication.DeliveryTransitionResult{}, foundation.NewError(foundation.ErrorRetryableFailure, "TOOL_WORKFLOW_COMPLETION_TRANSPORT_FAILED", true, errors.New("injected transport failure before workflow completion"))
	}
	coordinator.second = append(json.RawMessage(nil), command.Output...)
	coordinator.mu.Unlock()
	return coordinator.delegate.Complete(ctx, command)
}

func (coordinator *failFirstToolCompletion) Fail(ctx context.Context, command workflowapplication.FailDeliveryCommand) (workflowapplication.DeliveryTransitionResult, error) {
	return coordinator.delegate.Fail(ctx, command)
}

func (coordinator *failFirstToolCompletion) outputsMatch() bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.calls == 2 && string(coordinator.first) == string(coordinator.second)
}

func persistToolRequest(t *testing.T, catalog *toolsapplication.Registry, raw json.RawMessage) json.RawMessage {
	t.Helper()
	request, err := toolagent.DecodeToolRequestV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := toolworkflow.EncodePersistedToolInvocationV1(catalog, request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), request.Reason) || strings.Contains(string(persisted), `"reason"`) {
		t.Fatalf("model reason entered workflow input: %s", persisted)
	}
	return persisted
}

func initializeToolSmokeGit(t *testing.T, ctx context.Context, root string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "tool-smoke.md"), []byte("# Tool Smoke\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runToolSmokeGit(t, ctx, root, "init", "--initial-branch=main")
	runToolSmokeGit(t, ctx, root, "config", "user.name", "ZHIXU Tool Smoke")
	runToolSmokeGit(t, ctx, root, "config", "user.email", "tool-smoke@example.invalid")
	runToolSmokeGit(t, ctx, root, "add", "--", "docs/tool-smoke.md")
	runToolSmokeGit(t, ctx, root, "commit", "-m", "base")
	return strings.TrimSpace(runToolSmokeGit(t, ctx, root, "rev-parse", "HEAD"))
}

func runToolSmokeGit(t *testing.T, ctx context.Context, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git command failed: %v", err)
	}
	return string(output)
}

func startToolRuntime(t *testing.T, ctx context.Context, repository *workflowpostgres.RuntimeRepository, ids foundation.IDGenerator, workspaceID foundation.ID, definition workflowdomain.RegisteredDefinition, key string, input json.RawMessage) workflowapplication.RuntimeStartResult {
	t.Helper()
	request, err := workflowapplication.BuildRuntimeStartRequest(ids, foundation.SystemClock{}, workspaceID, key, input, definition)
	if err != nil {
		t.Fatal(err)
	}
	started, err := repository.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return started
}

func waitForToolRunStatus(ctx context.Context, pool *pgxpool.Pool, runID foundation.ID, want workflowdomain.RunStatus) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var status workflowdomain.RunStatus
	var nodeStatus workflowdomain.NodeStatus
	var errorCode, errorSummary *string
	for {
		if err := pool.QueryRow(ctx, `SELECT r.status,n.status,n.error_code,n.error_summary FROM workflow.run r JOIN workflow.node_run n ON n.run_id=r.id WHERE r.id=$1`, string(runID)).Scan(&status, &nodeStatus, &errorCode, &errorSummary); err != nil {
			return err
		}
		if status == want {
			return nil
		}
		if workflowdomain.IsTerminalRunStatus(status) {
			var toolStatus, toolError string
			_ = pool.QueryRow(ctx, `SELECT status,error_code FROM workflow.tool_call WHERE workflow_run_id=$1 ORDER BY call_no LIMIT 1`, string(runID)).Scan(&toolStatus, &toolError)
			return fmt.Errorf("run=%s status=%s node_status=%s error_code=%s error_summary=%s tool_status=%s tool_error=%s want=%s", runID, status, nodeStatus, optionalString(errorCode), optionalString(errorSummary), toolStatus, toolError, want)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("run=%s status=%s want=%s: %w", runID, status, want, ctx.Err())
		case <-ticker.C:
		}
	}
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func mustToolSmokeID(t *testing.T, ids foundation.IDGenerator) foundation.ID {
	t.Helper()
	id, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newToolRiverDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for real Tool River smoke")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_tool_river_%d", time.Now().UnixNano())
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
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
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

var _ toolsapplication.Executor = (*countingReceiptExecutor)(nil)
var _ toolsapplication.ResultReceiptLoader = (*countingReceiptExecutor)(nil)
var _ riveradapter.RuntimeExecutionCoordinator = (*failFirstToolCompletion)(nil)
