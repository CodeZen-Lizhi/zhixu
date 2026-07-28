//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryRAGMemorySnapshotBeginHasExactlyOneConcurrentClaimant(t *testing.T) {
	pool, ctx := newAgentRepositoryIntegrationPool(t)
	seedAgentRuntime(t, ctx, pool)
	seedRAGMemoryNodeKind(t, ctx, pool)
	firstConnection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer firstConnection.Release()
	secondConnection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer secondConnection.Release()
	firstRepository, err := NewRepository(firstConnection)
	if err != nil {
		t.Fatal(err)
	}
	secondRepository, err := NewRepository(secondConnection)
	if err != nil {
		t.Fatal(err)
	}
	if firstConnection.Conn().PgConn().PID() == secondConnection.Conn().PgConn().PID() {
		t.Fatal("concurrent claim test requires independent PostgreSQL connections")
	}
	conversationID := seedRAGMemoryConversation(t, ctx, pool, testAgentID(60))
	at := time.Now().UTC().Truncate(time.Microsecond)
	first := testRAGMemorySnapshot(testAgentID(61), testAgentID(62), conversationID, at)
	second := testRAGMemorySnapshot(testAgentID(63), testAgentID(64), conversationID, at)

	type result struct {
		snapshot domain.RAGMemorySnapshot
		claimed  bool
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var group sync.WaitGroup
	for index, candidate := range []domain.RAGMemorySnapshot{first, second} {
		repository := firstRepository
		if index == 1 {
			repository = secondRepository
		}
		group.Add(1)
		go func(repository *Repository, candidate domain.RAGMemorySnapshot) {
			defer group.Done()
			<-start
			snapshot, claimed, beginErr := repository.BeginRAGMemorySnapshot(ctx, candidate)
			results <- result{snapshot: snapshot, claimed: claimed, err: beginErr}
		}(repository, candidate)
	}
	close(start)
	group.Wait()
	close(results)

	claims := 0
	var persisted domain.RAGMemorySnapshot
	for result := range results {
		if result.err != nil {
			t.Fatalf("BeginRAGMemorySnapshot: %v", result.err)
		}
		if result.claimed {
			claims++
		}
		if persisted.ID == "" {
			persisted = result.snapshot
			continue
		}
		if result.snapshot != persisted {
			t.Fatalf("concurrent begin returned different reservations: first=%+v second=%+v", persisted, result.snapshot)
		}
	}
	if claims != 1 {
		t.Fatalf("claimants=%d, want exactly one", claims)
	}
	if persisted.Status != domain.RAGMemorySnapshotPreparing || persisted.ID == "" ||
		(persisted.ID != first.ID && persisted.ID != second.ID) {
		t.Fatalf("persisted reservation=%+v", persisted)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.rag_memory_snapshot WHERE workspace_id=$1 AND node_attempt_id=$2`,
		string(first.WorkspaceID), string(first.NodeAttemptID)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("attempt snapshot rows=%d, want 1", count)
	}
}

func TestRepositoryRAGMemorySnapshotFinalizeAtomicallyBindsReadySnapshotAndModelRun(t *testing.T) {
	pool, ctx := newAgentRepositoryIntegrationPool(t)
	seedAgentRuntime(t, ctx, pool)
	seedRAGMemoryNodeKind(t, ctx, pool)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := seedRAGMemoryConversation(t, ctx, pool, testAgentID(70))
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	snapshot := testRAGMemorySnapshot(testAgentID(71), testAgentID(72), conversationID, at)
	persisted, claimed, err := repository.BeginRAGMemorySnapshot(ctx, snapshot)
	if err != nil || !claimed || persisted.ID != snapshot.ID {
		t.Fatalf("BeginRAGMemorySnapshot snapshot=%+v claimed=%t err=%v", persisted, claimed, err)
	}
	contextRef := domain.RAGMemoryContextRef{
		SnapshotID: snapshot.ID, SchemaVersion: domain.RAGMemoryContextSchemaVersion,
		Digest: hash64('a'), ItemCount: 2, ByteCount: 128,
	}
	run := testModelRun(testAgentID(73), testAgentID(5), at.Add(time.Second))
	run.NodeRunID = snapshot.NodeRunID
	run.NodeAttemptID = snapshot.NodeAttemptID
	run.MemoryContext = contextRef
	command := application.FinalizeRAGMemorySnapshotCommand{
		SnapshotID: snapshot.ID, ClaimantID: snapshot.ClaimantID, Context: contextRef, Run: run,
	}

	wrongClaimant := command
	wrongClaimant.ClaimantID = testAgentID(74)
	if _, err := repository.FinalizeRAGMemorySnapshotAndCreateModelRun(ctx, wrongClaimant); err == nil {
		t.Fatal("finalize with another claimant was accepted")
	}
	assertRAGMemorySnapshotState(t, ctx, repository, snapshot.WorkspaceID, snapshot.ID, domain.RAGMemorySnapshotPreparing, "", "")
	assertRAGMemoryModelRunCount(t, ctx, pool, snapshot.NodeAttemptID, 0)

	created, err := repository.FinalizeRAGMemorySnapshotAndCreateModelRun(ctx, command)
	if err != nil || created.ID != run.ID || created.MemoryContext != contextRef {
		t.Fatalf("FinalizeRAGMemorySnapshotAndCreateModelRun run=%+v err=%v", created, err)
	}
	ready := assertRAGMemorySnapshotState(t, ctx, repository, snapshot.WorkspaceID, snapshot.ID, domain.RAGMemorySnapshotReady, run.ID, "")
	if ready.Context != contextRef {
		t.Fatalf("ready context=%+v, want %+v", ready.Context, contextRef)
	}
	record, err := repository.GetModelRun(ctx, run.WorkspaceID, run.ID)
	if err != nil || record.Run.MemoryContext != contextRef || record.Run.Status != domain.ModelRunRunning {
		t.Fatalf("GetModelRun record=%+v err=%v", record, err)
	}
	assertRAGMemoryModelRunCount(t, ctx, pool, snapshot.NodeAttemptID, 1)

	if _, err := repository.FinalizeRAGMemorySnapshotAndCreateModelRun(ctx, command); err == nil {
		t.Fatal("duplicate finalize was accepted")
	}
	assertRAGMemorySnapshotState(t, ctx, repository, snapshot.WorkspaceID, snapshot.ID, domain.RAGMemorySnapshotReady, run.ID, "")
	assertRAGMemoryModelRunCount(t, ctx, pool, snapshot.NodeAttemptID, 1)
}

func TestRepositoryRAGMemorySnapshotFailureIsTerminalForAttempt(t *testing.T) {
	pool, ctx := newAgentRepositoryIntegrationPool(t)
	seedAgentRuntime(t, ctx, pool)
	seedRAGMemoryNodeKind(t, ctx, pool)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := seedRAGMemoryConversation(t, ctx, pool, testAgentID(80))
	at := time.Now().UTC().Truncate(time.Microsecond)
	snapshot := testRAGMemorySnapshot(testAgentID(81), testAgentID(82), conversationID, at)
	if _, claimed, err := repository.BeginRAGMemorySnapshot(ctx, snapshot); err != nil || !claimed {
		t.Fatalf("BeginRAGMemorySnapshot claimed=%t err=%v", claimed, err)
	}
	failure := application.FailRAGMemorySnapshotCommand{
		SnapshotID: snapshot.ID, WorkspaceID: snapshot.WorkspaceID, ClaimantID: snapshot.ClaimantID,
		ErrorCode: application.ErrorCodeMemoryContextUnavailable,
	}
	failed, err := repository.FailRAGMemorySnapshot(ctx, failure)
	if err != nil || failed.Status != domain.RAGMemorySnapshotFailed || failed.ErrorCode != failure.ErrorCode {
		t.Fatalf("FailRAGMemorySnapshot snapshot=%+v err=%v", failed, err)
	}
	if replay, err := repository.FailRAGMemorySnapshot(ctx, failure); err != nil || replay != failed {
		t.Fatalf("FailRAGMemorySnapshot replay=%+v err=%v", replay, err)
	}
	assertRAGMemorySnapshotState(t, ctx, repository, snapshot.WorkspaceID, snapshot.ID, domain.RAGMemorySnapshotFailed, "", failure.ErrorCode)

	retry := testRAGMemorySnapshot(testAgentID(83), testAgentID(84), conversationID, at)
	existing, claimed, err := repository.BeginRAGMemorySnapshot(ctx, retry)
	if err != nil || claimed || existing.ID != snapshot.ID || existing.Status != domain.RAGMemorySnapshotFailed ||
		existing.ErrorCode != failure.ErrorCode || existing.Context.IsBound() || existing.ModelRunID != "" {
		t.Fatalf("retry reservation=%+v claimed=%t err=%v", existing, claimed, err)
	}
	assertRAGMemoryModelRunCount(t, ctx, pool, snapshot.NodeAttemptID, 0)
}

func testRAGMemorySnapshot(id, claimantID, conversationID foundation.ID, at time.Time) domain.RAGMemorySnapshot {
	return domain.RAGMemorySnapshot{
		ID: id, WorkspaceID: testAgentID(1), WorkflowRunID: testAgentID(4), NodeRunID: testAgentID(55),
		NodeAttemptID: testAgentID(56), ClaimantID: claimantID, OwnerKind: "USER", OwnerID: testAgentID(2),
		TaskScopeID: conversationID, Status: domain.RAGMemorySnapshotPreparing, CreatedAt: at, UpdatedAt: at,
	}
}

func seedRAGMemoryConversation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, conversationID foundation.ID) foundation.ID {
	t.Helper()
	at := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO agent.conversation(
		id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,idempotency_key,request_hash
	) VALUES($1,$2,'open','RAG Memory',1,$3,$3,$3,$4,repeat('b',64))`,
		string(conversationID), string(testAgentID(1)), at, "rag-memory-"+string(conversationID)); err != nil {
		t.Fatal(err)
	}
	return conversationID
}

func seedRAGMemoryNodeKind(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at
	) VALUES($1,$2,'agent-rag-memory','agent.rag-answer','running','{}','worker',now()+interval '5 minutes',1,now(),now())`,
		string(testAgentID(55)), string(testAgentID(4))); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
	) VALUES($1,$2,1,1,0,'agent-rag-memory','worker',now()+interval '5 minutes','running',now())`,
		string(testAgentID(56)), string(testAgentID(55))); err != nil {
		t.Fatal(err)
	}
}

func assertRAGMemorySnapshotState(t *testing.T, ctx context.Context, repository *Repository, workspaceID, snapshotID foundation.ID, status domain.RAGMemorySnapshotStatus, modelRunID foundation.ID, errorCode string) domain.RAGMemorySnapshot {
	t.Helper()
	snapshot, err := loadRAGMemorySnapshotByID(ctx, repository.db, workspaceID, snapshotID, false)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != status || snapshot.ModelRunID != modelRunID || snapshot.ErrorCode != errorCode {
		t.Fatalf("snapshot=%+v, want status=%s model_run=%s error=%s", snapshot, status, modelRunID, errorCode)
	}
	return snapshot
}

func assertRAGMemoryModelRunCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attemptID foundation.ID, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.model_run WHERE node_attempt_id=$1`, string(attemptID)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("model runs=%d, want %d", count, want)
	}
}
