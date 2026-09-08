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
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDraftStreamRepositoryFencesLeaseAndKeepsOrderedChunks(t *testing.T) {
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
	repository, err := NewGORMDraftStreamRepository(shared)
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
	if _, err := pool.Exec(ctx, `UPDATE agent.answer_draft_session
		SET expires_at=clock_timestamp()-interval '1 millisecond' WHERE id=$1`, string(session.ID)); err != nil {
		t.Fatal(err)
	}
	_, expiredReplayErr := repository.BeginDraftStream(ctx, agentapplication.BeginDraftStreamCommand{DraftStreamBinding: binding, TTL: time.Minute})
	var expiredReplay *foundation.Error
	if !errors.As(expiredReplayErr, &expiredReplay) || expiredReplay.Kind != foundation.ErrorVersionConflict || expiredReplay.Code != ErrorCodeDraftStreamConflict {
		t.Fatalf("expired same-attempt begin error = %#v", expiredReplayErr)
	}
	var sameAttemptSessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.answer_draft_session WHERE node_attempt_id=$1`, string(binding.NodeAttemptID)).Scan(&sameAttemptSessions); err != nil {
		t.Fatal(err)
	}
	if sameAttemptSessions != 1 {
		t.Fatalf("same attempt draft sessions = %d, want 1", sameAttemptSessions)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.answer_draft_session
		SET expires_at=clock_timestamp()+interval '1 minute' WHERE id=$1`, string(session.ID)); err != nil {
		t.Fatal(err)
	}
	err = withinConversationTransaction(ctx, repository.uow, foundation.TransactionOptions{}, func(ctx context.Context, claimTx *gorm.DB, _ foundation.TransactionScope) error {
		if _, err := gormLockDraftRuntimeClaim(ctx, claimTx, binding); err != nil {
			return err
		}
		reclaimCtx, cancelReclaim := context.WithTimeout(ctx, 75*time.Millisecond)
		_, reclaimErr := pool.Exec(reclaimCtx, `UPDATE workflow.node_run
			SET status='retry_wait',lease_owner=NULL,lease_until=NULL,version=version+1,updated_at=clock_timestamp()
			WHERE id=$1`, string(binding.NodeRunID))
		cancelReclaim()
		if !errors.Is(reclaimCtx.Err(), context.DeadlineExceeded) || reclaimErr == nil {
			t.Fatalf("runtime claim lock did not fence reclaim: ctx=%v err=%v", reclaimCtx.Err(), reclaimErr)
		}
		return nil
	})
	if err != nil {
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
	err = withinConversationTransaction(ctx, repository.uow, foundation.TransactionOptions{}, func(ctx context.Context, claimTx *gorm.DB, _ foundation.TransactionScope) error {
		leaseUntil, err := gormLockDraftRuntimeClaim(ctx, claimTx, binding)
		if err != nil {
			return err
		}
		if err := claimTx.WithContext(ctx).Exec(`SELECT pg_sleep(0.15)`).Error; err != nil {
			return err
		}
		_, expiredLeaseErr := gormValidateDraftClaimLease(ctx, claimTx, leaseUntil)
		var expiredLease *foundation.Error
		if !errors.As(expiredLeaseErr, &expiredLease) || expiredLease.Code != ErrorCodeDraftStreamConflict {
			t.Fatalf("expired long-transaction claim error = %v", expiredLeaseErr)
		}
		return nil
	})
	if err != nil {
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
	terminalBinding := agentapplication.DraftStreamBinding{
		WorkspaceID: workspaceID, AnswerID: mismatchFixture.answerID(2), WorkflowRunID: mismatchFixture.runID(2), NodeRunID: mismatchFixture.nodeID(2),
		NodeAttemptID: mismatchFixture.attemptID(2), AttemptNo: 1, LeaseOwner: "conversation-worker",
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run
		SET attempt=1,version=version+1,updated_at=clock_timestamp() WHERE id=$1`, string(terminalBinding.NodeRunID)); err != nil {
		t.Fatal(err)
	}
	terminalSession, err := repository.BeginDraftStream(ctx, agentapplication.BeginDraftStreamCommand{
		DraftStreamBinding: terminalBinding,
		TTL:                agentapplication.WorkspaceAnalysisV1MaxRunDuration,
	})
	if err != nil {
		t.Fatal(err)
	}
	if retention := terminalSession.ExpiresAt.Sub(terminalSession.CreatedAt); retention != agentapplication.WorkspaceAnalysisV1MaxRunDuration {
		t.Fatalf("Workspace Analysis draft retention = %s, want %s", retention, agentapplication.WorkspaceAnalysisV1MaxRunDuration)
	}
	if _, err := repository.AbortDraftStream(ctx, agentapplication.DraftStreamTransitionCommand{SessionID: terminalSession.ID, Binding: terminalBinding}); err != nil {
		t.Fatal(err)
	}
	_, terminalReplayErr := repository.BeginDraftStream(ctx, agentapplication.BeginDraftStreamCommand{
		DraftStreamBinding: terminalBinding,
		TTL:                agentapplication.WorkspaceAnalysisV1MaxRunDuration,
	})
	var terminalReplay *foundation.Error
	if !errors.As(terminalReplayErr, &terminalReplay) || terminalReplay.Kind != foundation.ErrorVersionConflict || terminalReplay.Code != ErrorCodeDraftStreamConflict {
		t.Fatalf("terminal same-attempt begin error = %#v", terminalReplayErr)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.answer_draft_session WHERE node_attempt_id=$1`, string(terminalBinding.NodeAttemptID)).Scan(&sameAttemptSessions); err != nil {
		t.Fatal(err)
	}
	if sameAttemptSessions != 1 {
		t.Fatalf("terminal same-attempt draft sessions = %d, want 1", sameAttemptSessions)
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
	lockedReader, err := NewGORMDraftStreamRepository(shared)
	if err != nil {
		t.Fatal(err)
	}
	lockedReader.uow = &draftReadLockDB{UnitOfWork: lockedReader.uow, Interface: logger.Discard, selected: selected, release: release}
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
	foundation.UnitOfWork
	logger.Interface
	selected     chan struct{}
	release      <-chan struct{}
	selectedOnce sync.Once
}

func (db *draftReadLockDB) Within(ctx context.Context, options foundation.TransactionOptions, work foundation.TransactionFunc) error {
	return db.UnitOfWork.Within(ctx, options, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		original := tx.Config
		config := *original
		config.Logger = db
		tx.Config = &config
		defer func() { tx.Config = original }()
		return work(ctx, scope)
	})
}

func (db *draftReadLockDB) Trace(ctx context.Context, _ time.Time, query func() (string, int64), err error) {
	sql, _ := query()
	if err != nil || !strings.Contains(sql, "FROM workflow.node_attempt AS attempt") {
		return
	}
	// The lease query follows the session Scan, so its FOR SHARE lock is already held.
	db.selectedOnce.Do(func() { close(db.selected) })
	select {
	case <-db.release:
	case <-ctx.Done():
	}
}
