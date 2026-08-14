//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDraftStreamRepositoryFencesLeaseAndKeepsOrderedChunks(t *testing.T) {
	conversationRepository, pool, ctx := newConversationTestRepository(t)
	repository, err := NewDraftStreamRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := conversationTurnID(61001)
	otherWorkspaceID := conversationTurnID(61004)
	seedConversationWorkspaces(t, ctx, pool, workspaceID, otherWorkspaceID)
	now := time.Now().UTC().Add(-time.Minute)
	conversation := conversationCreateRecord(t, workspaceID, conversationTurnID(61002), "draft stream", "draft-stream-conversation", now)
	if _, err := conversationRepository.CreateConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	fixture := seedConversationRuntimeBase(t, ctx, pool, workspaceID, conversation.Conversation.ID, 61100, now)
	seedPendingTurn(t, ctx, pool, fixture, 1, "stream this answer", now)
	mismatchConversation := conversationCreateRecord(t, workspaceID, conversationTurnID(61003), "draft binding", "draft-stream-binding-conversation", now)
	if _, err := conversationRepository.CreateConversation(ctx, mismatchConversation); err != nil {
		t.Fatal(err)
	}
	mismatchFixture := seedConversationRuntimeBase(t, ctx, pool, workspaceID, mismatchConversation.Conversation.ID, 61200, now)
	seedPendingTurn(t, ctx, pool, mismatchFixture, 2, "keep draft bindings coherent", now)
	assertDraftSessionBindingConstraints(t, ctx, pool, fixture, mismatchFixture, now)
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run
		SET attempt=1,version=version+1,updated_at=clock_timestamp() WHERE id=$1`, string(fixture.nodeID(1))); err != nil {
		t.Fatal(err)
	}
	binding := agentapplication.DraftStreamBinding{
		WorkspaceID: workspaceID, AnswerID: fixture.answerID(1), WorkflowRunID: fixture.runID(1), NodeRunID: fixture.nodeID(1),
		NodeAttemptID: fixture.attemptID(1), AttemptNo: 1, LeaseOwner: "conversation-worker",
	}
	session, err := repository.BeginDraftStream(ctx, agentapplication.BeginDraftStreamCommand{DraftStreamBinding: binding, TTL: time.Minute})
	if err != nil || session.Status != agentapplication.DraftStreamActive || session.Generation != 1 || session.NextSeq != 1 {
		t.Fatalf("begin = %#v, %v", session, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.answer_draft_session
		SET status='PUBLISHED',completed_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, string(session.ID)); err == nil {
		t.Fatal("database lifecycle allowed ACTIVE draft to publish directly")
	}
	replayed, err := repository.BeginDraftStream(ctx, agentapplication.BeginDraftStreamCommand{DraftStreamBinding: binding, TTL: time.Minute})
	if err != nil || replayed.ID != session.ID || replayed.Generation != session.Generation {
		t.Fatalf("same attempt begin replay = %#v, %v", replayed, err)
	}
	claimTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockDraftRuntimeClaim(ctx, claimTx, binding); err != nil {
		_ = claimTx.Rollback(ctx)
		t.Fatal(err)
	}
	reclaimCtx, cancelReclaim := context.WithTimeout(ctx, 75*time.Millisecond)
	_, reclaimErr := pool.Exec(reclaimCtx, `UPDATE workflow.node_run
		SET status='retry_wait',lease_owner=NULL,lease_until=NULL,version=version+1,updated_at=clock_timestamp()
		WHERE id=$1`, string(binding.NodeRunID))
	cancelReclaim()
	if !errors.Is(reclaimCtx.Err(), context.DeadlineExceeded) || reclaimErr == nil {
		_ = claimTx.Rollback(ctx)
		t.Fatalf("runtime claim lock did not fence reclaim: ctx=%v err=%v", reclaimCtx.Err(), reclaimErr)
	}
	if err := claimTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	var shortLease time.Time
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp()+interval '75 milliseconds'`).Scan(&shortLease); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run SET lease_until=$2,updated_at=clock_timestamp() WHERE id=$1`, string(binding.NodeRunID), shortLease); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=$2,heartbeat_at=clock_timestamp() WHERE id=$1`, string(binding.NodeAttemptID), shortLease); err != nil {
		t.Fatal(err)
	}
	claimTx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	leaseUntil, err := lockDraftRuntimeClaim(ctx, claimTx, binding)
	if err != nil {
		_ = claimTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := claimTx.Exec(ctx, `SELECT pg_sleep(0.15)`); err != nil {
		_ = claimTx.Rollback(ctx)
		t.Fatal(err)
	}
	_, expiredLeaseErr := validateDraftClaimLease(ctx, claimTx, leaseUntil)
	var expiredLease *foundation.Error
	if !errors.As(expiredLeaseErr, &expiredLease) || expiredLease.Code != ErrorCodeDraftStreamConflict {
		_ = claimTx.Rollback(ctx)
		t.Fatalf("expired long-transaction claim error = %v", expiredLeaseErr)
	}
	if err := claimTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run SET lease_until=clock_timestamp()+interval '5 minutes',updated_at=clock_timestamp() WHERE id=$1`, string(binding.NodeRunID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=clock_timestamp()+interval '5 minutes',heartbeat_at=clock_timestamp() WHERE id=$1`, string(binding.NodeAttemptID)); err != nil {
		t.Fatal(err)
	}
	first, err := repository.AppendDraftStream(ctx, agentapplication.DraftStreamAppendCommand{SessionID: session.ID, Binding: binding, Content: "first "})
	if err != nil || first.Sequence != 1 {
		t.Fatalf("first append = %#v, %v", first, err)
	}
	second, err := repository.AppendDraftStream(ctx, agentapplication.DraftStreamAppendCommand{SessionID: session.ID, Binding: binding, Content: "second"})
	if err != nil || second.Sequence != 2 || second.Generation != session.Generation {
		t.Fatalf("second append = %#v, %v", second, err)
	}
	page, err := repository.ReadDraftStream(ctx, agentapplication.DraftStreamReadQuery{WorkspaceID: workspaceID, AnswerID: binding.AnswerID, Limit: 10})
	if err != nil || page.Session == nil || page.Session.ID != session.ID || len(page.Chunks) != 2 || page.Chunks[0].Sequence != 1 || page.Chunks[1].Sequence != 2 {
		t.Fatalf("draft replay page = %#v, %v", page, err)
	}
	for _, test := range []struct {
		name  string
		query agentapplication.DraftStreamReadQuery
	}{
		{
			name:  "answer through another workspace",
			query: agentapplication.DraftStreamReadQuery{WorkspaceID: otherWorkspaceID, AnswerID: binding.AnswerID, Limit: 10},
		},
		{
			name:  "another answer in the workspace",
			query: agentapplication.DraftStreamReadQuery{WorkspaceID: workspaceID, AnswerID: mismatchFixture.answerID(2), Limit: 10},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			unbound, readErr := repository.ReadDraftStream(ctx, test.query)
			if readErr != nil || unbound.Session != nil || unbound.Invalidated || len(unbound.Chunks) != 0 {
				t.Fatalf("unbound draft replay = %#v, %v", unbound, readErr)
			}
		})
	}
	afterFirst := agentapplication.DraftStreamReadCursor{Generation: session.Generation, Sequence: 1}
	page, err = repository.ReadDraftStream(ctx, agentapplication.DraftStreamReadQuery{WorkspaceID: workspaceID, AnswerID: binding.AnswerID, After: &afterFirst, Limit: 10})
	if err != nil || len(page.Chunks) != 1 || page.Chunks[0].Sequence != 2 {
		t.Fatalf("draft replay after cursor = %#v, %v", page, err)
	}
	completed, err := repository.CompleteDraftStream(ctx, agentapplication.DraftStreamTransitionCommand{SessionID: session.ID, Binding: binding})
	if err != nil || completed.Status != agentapplication.DraftStreamCompleted {
		t.Fatalf("complete = %#v, %v", completed, err)
	}
	selected := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	lockedReader, err := NewDraftStreamRepository(&draftReadLockDB{DB: pool, selected: selected, release: release})
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() {
		page, readErr := lockedReader.ReadDraftStream(ctx, agentapplication.DraftStreamReadQuery{WorkspaceID: workspaceID, AnswerID: binding.AnswerID, Limit: 10})
		if readErr == nil && (page.Session == nil || page.Session.Status != agentapplication.DraftStreamCompleted || len(page.Chunks) != 2) {
			readErr = errors.New("locked draft read returned an incomplete page")
		}
		readDone <- readErr
	}()
	select {
	case <-selected:
	case <-time.After(time.Second):
		t.Fatal("draft read did not lock its selected session")
	}
	updateCtx, cancelUpdate := context.WithTimeout(ctx, 75*time.Millisecond)
	_, updateErr := pool.Exec(updateCtx, `UPDATE agent.answer_draft_session SET updated_at=clock_timestamp() WHERE id=$1`, string(session.ID))
	cancelUpdate()
	if !errors.Is(updateCtx.Err(), context.DeadlineExceeded) || updateErr == nil {
		t.Fatalf("draft read did not linearize against session update: ctx=%v err=%v", updateCtx.Err(), updateErr)
	}
	releaseOnce.Do(func() { close(release) })
	if readErr := <-readDone; readErr != nil {
		t.Fatal(readErr)
	}

	if _, err := pool.Exec(ctx, `UPDATE agent.answer_draft_session SET expires_at=CURRENT_TIMESTAMP+interval '50 milliseconds' WHERE id=$1`, string(session.ID)); err != nil {
		t.Fatal(err)
	}
	if deleted, err := repository.CleanupExpiredDraftStreams(ctx, 10); err != nil || deleted != 0 {
		t.Fatalf("cleanup retained active lease = %d, %v", deleted, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=CURRENT_TIMESTAMP-interval '1 second' WHERE id=$1`, string(binding.NodeAttemptID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run SET lease_until=CURRENT_TIMESTAMP-interval '1 second' WHERE id=$1`, string(binding.NodeRunID)); err != nil {
		t.Fatal(err)
	}
	page, err = repository.ReadDraftStream(ctx, agentapplication.DraftStreamReadQuery{WorkspaceID: workspaceID, AnswerID: binding.AnswerID, Limit: 10})
	if err != nil || page.Session != nil || !page.Invalidated || len(page.Chunks) != 0 {
		t.Fatalf("stale lease draft read = %#v, %v", page, err)
	}
	_, appendErr := repository.AppendDraftStream(ctx, agentapplication.DraftStreamAppendCommand{SessionID: session.ID, Binding: binding, Content: "stale"})
	var classified *foundation.Error
	if !errors.As(appendErr, &classified) || classified.Kind != foundation.ErrorVersionConflict || classified.Code != ErrorCodeDraftStreamConflict {
		t.Fatalf("stale append error = %#v", appendErr)
	}
	time.Sleep(100 * time.Millisecond)
	if deleted, err := repository.CleanupExpiredDraftStreams(ctx, 10); err != nil || deleted != 1 {
		t.Fatalf("cleanup abandoned session = %d, %v", deleted, err)
	}
}

func assertDraftSessionBindingConstraints(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture, mismatchFixture conversationRuntimeFixture,
	at time.Time,
) {
	t.Helper()
	insert := `INSERT INTO agent.answer_draft_session(
		id,workspace_id,answer_id,workflow_run_id,node_run_id,node_attempt_id,attempt_no,
		lease_owner,generation,status,expires_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,'conversation-worker',1,'ACTIVE',$8)`

	_, err := pool.Exec(ctx, insert,
		string(conversationTurnID(61901)), string(fixture.workspaceID), string(mismatchFixture.answerID(2)),
		string(fixture.runID(1)), string(fixture.nodeID(1)), string(fixture.attemptID(1)), 1, at.Add(time.Hour),
	)
	assertForeignKeyViolation(t, err, "answer and workflow run mismatch")

	_, err = pool.Exec(ctx, insert,
		string(conversationTurnID(61902)), string(fixture.workspaceID), string(mismatchFixture.answerID(2)),
		string(mismatchFixture.runID(2)), string(mismatchFixture.nodeID(2)), string(mismatchFixture.attemptID(2)), 2, at.Add(time.Hour),
	)
	assertForeignKeyViolation(t, err, "node attempt number mismatch")
}

func assertForeignKeyViolation(t *testing.T, err error, scenario string) {
	t.Helper()
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != "23503" {
		t.Fatalf("%s: error = %v", scenario, err)
	}
}

type draftReadLockDB struct {
	DB
	selected chan struct{}
	release  <-chan struct{}
}

func (db *draftReadLockDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := db.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &draftReadLockTx{Tx: tx, selected: db.selected, release: db.release}, nil
}

type draftReadLockTx struct {
	pgx.Tx
	selected chan struct{}
	release  <-chan struct{}
}

func (tx *draftReadLockTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := tx.Tx.QueryRow(ctx, query, args...)
	if !strings.Contains(query, "ORDER BY session.generation DESC LIMIT 1 FOR SHARE") {
		return row
	}
	return &draftReadLockRow{Row: row, selected: tx.selected, release: tx.release}
}

type draftReadLockRow struct {
	pgx.Row
	selected chan struct{}
	release  <-chan struct{}
}

func (row *draftReadLockRow) Scan(dest ...any) error {
	if err := row.Row.Scan(dest...); err != nil {
		return err
	}
	close(row.selected)
	<-row.release
	return nil
}
