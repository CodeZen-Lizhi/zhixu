//go:build integration

package workflowpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRuntimeRepositoryStartReplayConflictRollbackAndLegacyGuard(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	outer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outer.Rollback(ctx) }()
	workspaceID := foundation.ID("a0000000-0000-4000-8000-000000000001")
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	if _, err := outer.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'M4A Runtime',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), "/tmp/m4a-runtime", now); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(outer, inserter)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeStartFixture(workspaceID, now)
	first, err := repository.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.Job.Duplicate || first.Job.JobID < 1 {
		t.Fatalf("first job=%#v", first.Job)
	}
	replay := request
	replay.Definition.ID = foundation.ID("b0000000-0000-4000-8000-000000000011")
	replay.Run.ID = foundation.ID("b0000000-0000-4000-8000-000000000012")
	replay.Run.DefinitionID = replay.Definition.ID
	replay.FirstNode.ID = foundation.ID("b0000000-0000-4000-8000-000000000013")
	replay.FirstNode.RunID = replay.Run.ID
	replay.Event.ID = foundation.ID("b0000000-0000-4000-8000-000000000014")
	replay.Event.RunID = &replay.Run.ID
	second, err := repository.Start(ctx, replay)
	if err != nil {
		t.Fatal(err)
	}
	if second.Run.ID != first.Run.ID || second.FirstNode.ID != first.FirstNode.ID || second.Job.JobID != first.Job.JobID || !second.Job.Duplicate || !second.Replayed {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	conflict := replay
	conflict.Run.RequestHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	conflict.RequestHash = conflict.Run.RequestHash
	if _, err := repository.Start(ctx, conflict); !hasCode(err, "WORKFLOW_START_IDEMPOTENCY_CONFLICT") {
		t.Fatalf("idempotency conflict=%v", err)
	}

	failingRepository, err := NewRuntimeRepository(outer, failingJobInserter{})
	if err != nil {
		t.Fatal(err)
	}
	failed := runtimeStartFixture(workspaceID, now.Add(time.Minute))
	remapRuntimeStartIDs(&failed, "c")
	failed.Definition.Key = "m4a-failed"
	failed.Run.IdempotencyKey = "m4a-failed"
	failed.Event.IdempotencyKey = "workflow-start:m4a-failed"
	failed.Event.EventKey = "workflow.run.started:m4a-failed"
	failed.FirstNode.IdempotencyKey = "node-start:m4a-failed"
	if _, err := failingRepository.Start(ctx, failed); err == nil {
		t.Fatal("job insertion failure was ignored")
	}
	var failedRuns int
	if err := outer.QueryRow(ctx, `SELECT count(*) FROM workflow.run WHERE id=$1`, string(failed.Run.ID)).Scan(&failedRuns); err != nil || failedRuns != 0 {
		t.Fatalf("failed start persisted run count=%d err=%v", failedRuns, err)
	}

	legacy := runtimeStartFixture(workspaceID, now.Add(2*time.Minute))
	remapRuntimeStartIDs(&legacy, "d")
	legacy.Definition.Key = "m4a-legacy"
	if _, err := outer.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,$3,1,$4,$5)`, string(legacy.Definition.ID), string(workspaceID), legacy.Definition.Key, legacy.Definition.Graph, now); err != nil {
		t.Fatal(err)
	}
	if _, err := outer.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'pending','{}',1,$4,$4)`, string(legacy.Run.ID), string(workspaceID), string(legacy.Definition.ID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Start(ctx, legacy); !hasCode(err, "WORKFLOW_LEGACY_RUNTIME_UNSUPPORTED") {
		t.Fatalf("legacy active error=%v", err)
	}
}

func TestRuntimeRepositoryConcurrentDuplicateCreatesOneRunNodeOutboxAndJob(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("e0000000-0000-4000-8000-000000000001")
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'M4A Concurrent',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), "/tmp/m4a-concurrent", now); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	firstRequest := runtimeStartFixture(workspaceID, now)
	secondRequest := firstRequest
	remapRuntimeStartIDs(&secondRequest, "f")
	type outcome struct {
		result application.RuntimeStartResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for _, request := range []application.RuntimeStartRequest{firstRequest, secondRequest} {
		request := request
		go func() {
			result, err := repository.Start(ctx, request)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	left, right := <-outcomes, <-outcomes
	if left.err != nil || right.err != nil {
		t.Fatalf("left=%#v right=%#v", left, right)
	}
	if left.result.Run.ID != right.result.Run.ID || left.result.FirstNode.ID != right.result.FirstNode.ID || left.result.Job.JobID != right.result.Job.JobID || left.result.Job.Duplicate == right.result.Job.Duplicate || left.result.Replayed == right.result.Replayed || left.result.Replayed != left.result.Job.Duplicate || right.result.Replayed != right.result.Job.Duplicate {
		t.Fatalf("left=%#v right=%#v", left.result, right.result)
	}
	var runs, nodes, events, jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.run WHERE workspace_id=$1 AND idempotency_key='m4a-runtime'`, string(workspaceID)).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.node_run WHERE run_id=$1 AND idempotency_key='node-start:m4a-runtime'`, string(left.result.Run.ID)).Scan(&nodes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.outbox_event WHERE workspace_id=$1 AND event_key='workflow.run.started:m4a-runtime'`, string(workspaceID)).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job WHERE kind=$1 AND args->>'node_run_id'=$2`, riveradapter.NodeJobKind, string(left.result.FirstNode.ID)).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || nodes != 1 || events != 1 || jobs != 1 {
		t.Fatalf("runs=%d nodes=%d events=%d jobs=%d", runs, nodes, events, jobs)
	}
}

func TestRuntimeRepositoryRecoversCommitResponseLoss(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("90000000-0000-4000-8000-000000000001")
	now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'M4A Commit Loss',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), "/tmp/m4a-commit-loss", now); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(commitResponseLossDB{pool: pool}, inserter)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeStartFixture(workspaceID, now)
	result, err := repository.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.ID == "" || result.FirstNode.ID == "" || result.Job.JobID < 1 || result.Replayed {
		t.Fatalf("recovered result=%#v", result)
	}
	replay := request
	remapRuntimeStartIDs(&replay, "7")
	replayed, err := repository.Start(ctx, replay)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || !replayed.Job.Duplicate || replayed.Run.ID != result.Run.ID || replayed.FirstNode.ID != result.FirstNode.ID || replayed.Job.JobID != result.Job.JobID {
		t.Fatalf("initial=%#v replayed=%#v", result, replayed)
	}
}

func TestRuntimeRepositoryStartTxUsesCallerTransactionAndReportsReplay(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("91000000-0000-4000-8000-000000000001")
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'M4A StartTx',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), "/tmp/m4a-start-tx", now); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	request := runtimeStartFixture(workspaceID, now)
	first, err := repository.StartTx(ctx, tx, request)
	if err != nil {
		t.Fatal(err)
	}
	replay := request
	remapRuntimeStartIDs(&replay, "8")
	second, err := repository.StartTx(ctx, tx, replay)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.Job.Duplicate || !second.Replayed || !second.Job.Duplicate || second.Run.ID != first.Run.ID || second.FirstNode.ID != first.FirstNode.ID || second.Job.JobID != first.Job.JobID {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	var inTransaction int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM workflow.run WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), request.Run.IdempotencyKey).Scan(&inTransaction); err != nil || inTransaction != 1 {
		t.Fatalf("in-transaction runs=%d err=%v", inTransaction, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var runs, jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.run WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), request.Run.IdempotencyKey).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job WHERE kind=$1 AND args->>'node_run_id'=$2`, riveradapter.NodeJobKind, string(first.FirstNode.ID)).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || jobs != 0 {
		t.Fatalf("caller rollback left runs=%d jobs=%d", runs, jobs)
	}
}

type failingJobInserter struct{}

type commitResponseLossDB struct{ pool *pgxpool.Pool }

func (d commitResponseLossDB) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	return d.pool.QueryRow(ctx, sql, arguments...)
}

func (d commitResponseLossDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return commitResponseLossTx{Tx: tx}, nil
}

type commitResponseLossTx struct{ pgx.Tx }

func (tx commitResponseLossTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("injected commit response loss")
}

func (failingJobInserter) InsertTx(context.Context, any, riveradapter.NodeJobArgs, riveradapter.InsertOptions) (application.JobReceipt, error) {
	return application.JobReceipt{}, foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_JOB_INSERT_FAILED", true, errors.New("injected job failure"))
}

func runtimeStartFixture(workspaceID foundation.ID, now time.Time) application.RuntimeStartRequest {
	definitionID := foundation.ID("a0000000-0000-4000-8000-000000000011")
	runID := foundation.ID("a0000000-0000-4000-8000-000000000012")
	nodeID := foundation.ID("a0000000-0000-4000-8000-000000000013")
	eventID := foundation.ID("a0000000-0000-4000-8000-000000000014")
	graph := json.RawMessage(`{"nodes":[]}`)
	runIDCopy := runID
	return application.RuntimeStartRequest{
		Definition:                   domain.Definition{ID: definitionID, WorkspaceID: workspaceID, Key: "m4a-runtime", Version: 1, Graph: graph, CreatedAt: now},
		DefinitionGraphHash:          "1111111111111111111111111111111111111111111111111111111111111111",
		DefinitionInputSchemaVersion: 1,
		Run:                          domain.Run{ID: runID, WorkspaceID: workspaceID, DefinitionID: definitionID, Status: domain.StatusPending, Input: json.RawMessage(`{"value":1}`), IdempotencyKey: "m4a-runtime", RequestHash: "2222222222222222222222222222222222222222222222222222222222222222", Version: 1, CreatedAt: now, UpdatedAt: now},
		FirstNode:                    domain.NodeRun{ID: nodeID, RunID: runID, NodeKey: "hash", NodeType: application.CanonicalJSONHashNodeKind, Status: domain.StatusPending, Input: json.RawMessage(`{"value":1}`), IdempotencyKey: "node-start:m4a-runtime", InputSchemaVersion: 1, OutputSchemaVersion: 1, DispatchNo: 1, Version: 1, CreatedAt: now, UpdatedAt: now},
		Event:                        domain.OutboxEvent{ID: eventID, WorkspaceID: workspaceID, RunID: &runIDCopy, Type: "workflow.run.started", IdempotencyKey: "workflow-start:m4a-runtime", EventKey: "workflow.run.started:m4a-runtime", SchemaVersion: 1, EventVersion: 1, Payload: json.RawMessage(`{}`), OccurredAt: now},
		RequestHash:                  "2222222222222222222222222222222222222222222222222222222222222222",
	}
}

var _ riveradapter.JobInserter = failingJobInserter{}

func remapRuntimeStartIDs(request *application.RuntimeStartRequest, prefix string) {
	request.Definition.ID = foundation.ID(prefix + string(request.Definition.ID)[1:])
	request.Run.ID = foundation.ID(prefix + string(request.Run.ID)[1:])
	request.Run.DefinitionID = request.Definition.ID
	request.FirstNode.ID = foundation.ID(prefix + string(request.FirstNode.ID)[1:])
	request.FirstNode.RunID = request.Run.ID
	request.Event.ID = foundation.ID(prefix + string(request.Event.ID)[1:])
	runID := request.Run.ID
	request.Event.RunID = &runID
}

func newRuntimeTestDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Fatal("ZHIXU_TEST_DATABASE_URL is required for Runtime Start integration tests")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_runtime_%d", time.Now().UnixNano())
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
