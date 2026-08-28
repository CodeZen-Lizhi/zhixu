//go:build integration && testcontainers

package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStoreReadsScopedRetainedEventsInMonotonicBoundedPages(t *testing.T) {
	stores := requireEventIntegrationStores(t)
	store, pool, ctx := stores.legacy, stores.platform.DB(), context.Background()
	workspaceA := foundation.ID("7b000000-0000-4000-8000-000000000001")
	workspaceB := foundation.ID("7b000000-0000-4000-8000-000000000002")
	seedEventWorkspaces(t, ctx, pool, workspaceA, workspaceB)

	now := time.Now().UTC().Truncate(time.Microsecond)
	expiredSeq := insertServerEvent(t, ctx, pool, workspaceA, "workflow.run.expired", "7b000000-0000-4000-8000-000000000011", now.Add(-25*time.Hour), `{}`)
	otherSeq := insertServerEvent(t, ctx, pool, workspaceB, "workflow.run.started", "7b000000-0000-4000-8000-000000000012", now.Add(-time.Hour), `{}`)
	firstSeq := insertServerEvent(t, ctx, pool, workspaceA, "workflow.run.started", "7b000000-0000-4000-8000-000000000013", now.Add(-time.Hour), `{"status":"running","candidate_count":0}`)
	secondSeq := insertServerEvent(t, ctx, pool, workspaceA, "workflow.run.progress", "7b000000-0000-4000-8000-000000000014", now.Add(-30*time.Minute), `{"status":"running","candidate_count":2}`)
	if !(expiredSeq < otherSeq && otherSeq < firstSeq && firstSeq < secondSeq) {
		t.Fatalf("fixture sequences are not globally monotonic: %d %d %d %d", expiredSeq, otherSeq, firstSeq, secondSeq)
	}

	watermark, err := store.CurrentWatermark(ctx, workspaceA)
	if err != nil || watermark != secondSeq {
		t.Fatalf("current watermark=%d want=%d err=%v", watermark, secondSeq, err)
	}
	earliest, err := store.EarliestRetained(ctx, workspaceA)
	if err != nil || earliest == nil || *earliest != firstSeq {
		t.Fatalf("earliest retained=%v want=%d err=%v", earliest, firstSeq, err)
	}

	firstPage, err := store.ListAfter(ctx, workspaceA, 0, 1)
	if err != nil || len(firstPage) != 1 || firstPage[0].Seq != firstSeq {
		t.Fatalf("first page=%#v err=%v", firstPage, err)
	}
	secondPage, err := store.ListAfter(ctx, workspaceA, firstPage[0].Seq, domain.MaxReplayPageSize)
	if err != nil || len(secondPage) != 1 || secondPage[0].Seq != secondSeq {
		t.Fatalf("second page=%#v err=%v", secondPage, err)
	}
	if secondPage[0].WorkspaceID != workspaceA || secondPage[0].PayloadSummary.CandidateCount == nil || *secondPage[0].PayloadSummary.CandidateCount != 2 {
		t.Fatalf("second page lost workspace or summary binding: %#v", secondPage[0])
	}
	otherPage, err := store.ListAfter(ctx, workspaceB, 0, domain.MaxReplayPageSize)
	if err != nil || len(otherPage) != 1 || otherPage[0].Seq != otherSeq || otherPage[0].WorkspaceID != workspaceB {
		t.Fatalf("workspace B page=%#v err=%v", otherPage, err)
	}

	if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("short replay queries retained %d database connections", acquired)
	}
}

func TestStoreRejectsUnsafeSummaryWithoutReturningPartialReplay(t *testing.T) {
	stores := requireEventIntegrationStores(t)
	store, pool, ctx := stores.legacy, stores.platform.DB(), context.Background()
	workspaceID := foundation.ID("7c000000-0000-4000-8000-000000000001")
	seedEventWorkspaces(t, ctx, pool, workspaceID)
	now := time.Now().UTC().Truncate(time.Microsecond)
	insertServerEvent(t, ctx, pool, workspaceID, "workflow.run.started", "7c000000-0000-4000-8000-000000000011", now.Add(-time.Minute), `{}`)
	insertServerEvent(t, ctx, pool, workspaceID, "answer.completed", "7c000000-0000-4000-8000-000000000012", now, `{"answer_text":"private answer canary"}`)

	events, err := store.ListAfter(ctx, workspaceID, 0, domain.MaxReplayPageSize)
	if len(events) != 0 || eventErrorCode(err) != domain.ErrorCodeEventCorrupt {
		t.Fatalf("unsafe replay events=%d code=%q err=%v", len(events), eventErrorCode(err), err)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("failed replay retained %d database connections", acquired)
	}
}

func TestServerEventSourceReferenceIsUniquePerWorkspace(t *testing.T) {
	stores := requireEventIntegrationStores(t)
	pool, ctx := stores.platform.DB(), context.Background()
	workspaceID := foundation.ID("7d000000-0000-4000-8000-000000000001")
	seedEventWorkspaces(t, ctx, pool, workspaceID)
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceID := "7d000000-0000-4000-8000-000000000011"
	insertServerEvent(t, ctx, pool, workspaceID, "workflow.run.started", sourceID, now, `{}`)

	_, err := pool.Exec(ctx, insertEventSQL, string(workspaceID), "workflow.run.started", "workflow.run:"+sourceID, "workflow.outbox_event:"+sourceID, now, `{}`)
	var postgresError *pgconn.PgError
	if err == nil || !errors.As(err, &postgresError) || postgresError.Code != "23505" {
		t.Fatalf("duplicate source reference error=%v", err)
	}
}

func TestStoreAppendTxCreatesExactReplayRejectsConflictAndLeavesCommitToCaller(t *testing.T) {
	stores := requireEventIntegrationStores(t)
	store, pool, ctx := stores.legacy, stores.platform.DB(), context.Background()
	workspaceID := foundation.ID("7d100000-0000-4000-8000-000000000001")
	conversationID := foundation.ID("7d100000-0000-4000-8000-000000000002")
	seedEventWorkspaces(t, ctx, pool, workspaceID)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	insertEventConversation(t, ctx, tx, workspaceID, conversationID, "append-committed")
	request := domain.AppendRequest{
		WorkspaceID: workspaceID, ConversationID: &conversationID,
		Type: "conversation.created", ResourceRef: "conversation:" + string(conversationID), ResourceVersion: 1,
		PayloadSummary: domain.PayloadSummary{Status: "open"}, SchemaVersion: 1,
		SourceEventRef: "conversation.created:" + string(conversationID) + ":v1",
		OccurredAt:     time.Now().UTC().Truncate(time.Second).Add(123456789),
	}
	created, replayed, err := store.AppendTx(ctx, tx, request)
	if err != nil || replayed || created.Seq <= 0 || created.ConversationID == nil || *created.ConversationID != conversationID || created.OccurredAt.Nanosecond() != 123456000 {
		t.Fatalf("created event=%#v replayed=%t err=%v", created, replayed, err)
	}
	replayedEvent, replayed, err := store.AppendTx(ctx, tx, request)
	if err != nil || !replayed || replayedEvent.Seq != created.Seq {
		t.Fatalf("replayed event=%#v replayed=%t err=%v", replayedEvent, replayed, err)
	}
	conflict := request
	conflict.ResourceVersion = 2
	if _, _, err := store.AppendTx(ctx, tx, conflict); eventErrorCode(err) != domain.ErrorCodeAppendConflict {
		t.Fatalf("append conflict code=%q err=%v", eventErrorCode(err), err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListAfter(ctx, workspaceID, 0, domain.MaxReplayPageSize)
	if err != nil || len(page) != 1 || page[0].Seq != created.Seq {
		t.Fatalf("committed page=%#v err=%v", page, err)
	}

	rolledBackConversationID := foundation.ID("7d100000-0000-4000-8000-000000000003")
	rollbackTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertEventConversation(t, ctx, rollbackTx, workspaceID, rolledBackConversationID, "append-rolled-back")
	rollbackRequest := request
	rollbackRequest.ConversationID = &rolledBackConversationID
	rollbackRequest.ResourceRef = "conversation:" + string(rolledBackConversationID)
	rollbackRequest.SourceEventRef = "conversation.created:" + string(rolledBackConversationID) + ":v1"
	if _, replayed, err := store.AppendTx(ctx, rollbackTx, rollbackRequest); err != nil || replayed {
		_ = rollbackTx.Rollback(ctx)
		t.Fatalf("append before caller rollback replayed=%t err=%v", replayed, err)
	}
	if err := rollbackTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListAfter(ctx, workspaceID, created.Seq, domain.MaxReplayPageSize)
	if err != nil || len(page) != 0 {
		t.Fatalf("caller rollback left events=%#v err=%v", page, err)
	}
	if _, _, err := store.AppendTx(ctx, nil, request); eventErrorCode(err) != domain.ErrorCodeAppendTransactionUnavailable {
		t.Fatalf("nil transaction code=%q err=%v", eventErrorCode(err), err)
	}
}

func TestStoreAppendTxSerializesConcurrentSourceReferenceClaims(t *testing.T) {
	stores := requireEventIntegrationStores(t)
	store, pool, ctx := stores.legacy, stores.platform.DB(), context.Background()
	workspaceID := foundation.ID("7d200000-0000-4000-8000-000000000001")
	conversationID := foundation.ID("7d200000-0000-4000-8000-000000000002")
	seedEventWorkspaces(t, ctx, pool, workspaceID)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertEventConversation(t, ctx, tx, workspaceID, conversationID, "append-concurrent")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	request := domain.AppendRequest{
		WorkspaceID: workspaceID, ConversationID: &conversationID,
		Type: "conversation.created", ResourceRef: "conversation:" + string(conversationID), ResourceVersion: 1,
		PayloadSummary: domain.PayloadSummary{Status: "open"}, SchemaVersion: 1,
		SourceEventRef: "conversation.created:" + string(conversationID) + ":concurrent-v1", OccurredAt: now,
	}

	exact := runConcurrentAppends(t, ctx, pool, store, request, request)
	var createdCount, replayedCount int
	var exactSequence int64
	for _, result := range exact {
		if result.err != nil {
			t.Fatalf("concurrent exact append error=%v", result.err)
		}
		if exactSequence != 0 && result.event.Seq != exactSequence {
			t.Fatalf("concurrent exact sequences differ: %d and %d", exactSequence, result.event.Seq)
		}
		exactSequence = result.event.Seq
		if result.replayed {
			replayedCount++
		} else {
			createdCount++
		}
	}
	if createdCount != 1 || replayedCount != 1 {
		t.Fatalf("concurrent exact created=%d replayed=%d", createdCount, replayedCount)
	}

	first := request
	first.Type = "conversation.updated"
	first.SourceEventRef = "conversation.updated:" + string(conversationID) + ":concurrent-v2"
	first.OccurredAt = now.Add(time.Second)
	second := first
	second.ResourceVersion = 2
	conflicting := runConcurrentAppends(t, ctx, pool, store, first, second)
	var successCount, conflictCount int
	for _, result := range conflicting {
		switch eventErrorCode(result.err) {
		case "":
			successCount++
		case domain.ErrorCodeAppendConflict:
			conflictCount++
		default:
			t.Fatalf("concurrent conflict unexpected code=%q err=%v", eventErrorCode(result.err), result.err)
		}
	}
	if successCount != 1 || conflictCount != 1 {
		t.Fatalf("concurrent conflict success=%d conflict=%d", successCount, conflictCount)
	}
}

type concurrentAppendResult struct {
	event    domain.ServerEvent
	replayed bool
	err      error
}

func runConcurrentAppends(t *testing.T, parent context.Context, pool *pgxpool.Pool, store *Store, requests ...domain.AppendRequest) []concurrentAppendResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan concurrentAppendResult, len(requests))
	transactions := make([]pgx.Tx, len(requests))
	for index := range requests {
		tx, err := pool.Begin(ctx)
		if err != nil {
			for _, opened := range transactions[:index] {
				_ = opened.Rollback(ctx)
			}
			t.Fatal(err)
		}
		transactions[index] = tx
	}
	for index, request := range requests {
		request := request
		tx := transactions[index]
		go func() {
			<-start
			event, replayed, err := store.AppendTx(ctx, tx, request)
			if err != nil {
				_ = tx.Rollback(ctx)
				results <- concurrentAppendResult{err: err}
				return
			}
			if err := tx.Commit(ctx); err != nil {
				results <- concurrentAppendResult{err: err}
				return
			}
			results <- concurrentAppendResult{event: event, replayed: replayed}
		}()
	}
	close(start)
	collected := make([]concurrentAppendResult, 0, len(requests))
	for range requests {
		select {
		case result := <-results:
			collected = append(collected, result)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	return collected
}

func TestGORMStorePostgresEquivalenceAndScopedTransactions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	stores := requireEventIntegrationStores(t)
	pool := stores.platform.DB()

	t.Run("paired reads preserve workspace retention pages and corrupt rows", func(t *testing.T) {
		workspaceA := foundation.ID("7e000000-0000-4000-8000-000000000001")
		workspaceB := foundation.ID("7e000000-0000-4000-8000-000000000002")
		emptyWorkspace := foundation.ID("7e000000-0000-4000-8000-000000000003")
		seedEventWorkspaces(t, ctx, pool, workspaceA, workspaceB, emptyWorkspace)

		now := time.Now().UTC().Truncate(time.Microsecond)
		expiredSeq := insertServerEvent(t, ctx, pool, workspaceA, "workflow.run.expired", "7e000000-0000-4000-8000-000000000011", now.Add(-25*time.Hour), `{}`)
		otherSeq := insertServerEvent(t, ctx, pool, workspaceB, "workflow.run.started", "7e000000-0000-4000-8000-000000000012", now.Add(-time.Hour), `{}`)
		firstSeq := insertServerEvent(t, ctx, pool, workspaceA, "workflow.run.started", "7e000000-0000-4000-8000-000000000013", now.Add(-time.Hour), `{"status":"running","candidate_count":0}`)
		secondSeq := insertServerEvent(t, ctx, pool, workspaceA, "workflow.run.progress", "7e000000-0000-4000-8000-000000000014", now.Add(-30*time.Minute), `{"status":"running","candidate_count":2}`)
		if !(expiredSeq < otherSeq && otherSeq < firstSeq && firstSeq < secondSeq) {
			t.Fatalf("fixture sequences are not globally monotonic: %d %d %d %d", expiredSeq, otherSeq, firstSeq, secondSeq)
		}
		assertEventStoreReadParity(t, ctx, stores, workspaceA, firstSeq, secondSeq)
		assertEventStoreReadParity(t, ctx, stores, workspaceB, otherSeq, otherSeq)
		assertEventStoreReadParity(t, ctx, stores, emptyWorkspace, 0, 0)
		assertEventReplayPlans(t, ctx, pool, workspaceA)

		triggerWorkspace := foundation.ID("7e000000-0000-4000-8000-000000000005")
		seedEventWorkspaces(t, ctx, pool, triggerWorkspace)
		if _, err := pool.Exec(ctx, `INSERT INTO ops.git_remote_config_revision(
			workspace_id,revision,configured,normalized_url,branch,auto_sync,token_configured,
			actor,config_created_at,created_at
		) VALUES($1,1,false,NULL,NULL,false,false,'test:events',$2,$2)`, string(triggerWorkspace), now); err != nil {
			t.Fatalf("seed Git remote revision: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO ops.git_remote_config(
			workspace_id,configured,normalized_url,branch,auto_sync,token_configured,revision,created_at,updated_at
		) VALUES($1,false,NULL,NULL,false,false,1,$2,$2)`, string(triggerWorkspace), now); err != nil {
			t.Fatalf("trigger Git remote Event projection: %v", err)
		}
		legacyTriggered, legacyErr := stores.legacy.ListAfter(ctx, triggerWorkspace, 0, domain.MaxReplayPageSize)
		gormTriggered, gormErr := stores.gorm.ListAfter(ctx, triggerWorkspace, 0, domain.MaxReplayPageSize)
		if legacyErr != nil || gormErr != nil || !reflect.DeepEqual(gormTriggered, legacyTriggered) || len(gormTriggered) != 1 || gormTriggered[0].Type != "git.remote.updated" {
			t.Fatalf("trigger Event replay legacy=%#v/%v gorm=%#v/%v", legacyTriggered, legacyErr, gormTriggered, gormErr)
		}

		corruptWorkspace := foundation.ID("7e000000-0000-4000-8000-000000000004")
		seedEventWorkspaces(t, ctx, pool, corruptWorkspace)
		insertServerEvent(t, ctx, pool, corruptWorkspace, "workflow.run.started", "7e000000-0000-4000-8000-000000000015", now.Add(-time.Minute), `{}`)
		insertServerEvent(t, ctx, pool, corruptWorkspace, "answer.completed", "7e000000-0000-4000-8000-000000000016", now, `{"answer_text":"private answer canary"}`)
		for name, store := range map[string]application.Store{"legacy": stores.legacy, "gorm": stores.gorm} {
			events, err := store.ListAfter(ctx, corruptWorkspace, 0, domain.MaxReplayPageSize)
			if len(events) != 0 || eventErrorCode(err) != domain.ErrorCodeEventCorrupt {
				t.Fatalf("%s corrupt replay events=%d code=%q err=%v", name, len(events), eventErrorCode(err), err)
			}
		}
		assertEventPoolReleased(t, pool, "GORM read and corrupt paths")
	})

	t.Run("caller unit of work owns append commit rollback and replay", func(t *testing.T) {
		workspaceID := foundation.ID("7e100000-0000-4000-8000-000000000001")
		conversationID := foundation.ID("7e100000-0000-4000-8000-000000000002")
		seedEventWorkspaces(t, ctx, pool, workspaceID)
		request := eventAppendRequest(workspaceID, conversationID, "scoped")
		var expiredScope foundation.TransactionScope
		if err := stores.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			expiredScope = scope
			if err := insertEventConversationScoped(callbackCtx, scope, workspaceID, conversationID, "gorm-scoped-commit"); err != nil {
				return err
			}
			created, replayed, appendErr := stores.gorm.AppendScoped(callbackCtx, scope, request)
			if appendErr != nil || replayed || created.ConversationID == nil || *created.ConversationID != conversationID || created.OccurredAt.Nanosecond() != 123456000 {
				t.Fatalf("GORM AppendScoped create=%#v replayed=%t err=%v", created, replayed, appendErr)
			}
			replayedEvent, replayed, appendErr := stores.gorm.AppendScoped(callbackCtx, scope, request)
			if appendErr != nil || !replayed || replayedEvent.Seq != created.Seq {
				t.Fatalf("GORM AppendScoped replay=%#v replayed=%t err=%v", replayedEvent, replayed, appendErr)
			}
			conflicting := request
			conflicting.ResourceVersion++
			if _, _, appendErr = stores.gorm.AppendScoped(callbackCtx, scope, conflicting); eventErrorCode(appendErr) != domain.ErrorCodeAppendConflict {
				t.Fatalf("GORM AppendScoped conflict code=%q err=%v", eventErrorCode(appendErr), appendErr)
			}
			return nil
		}); err != nil {
			t.Fatalf("GORM scoped commit: %v", err)
		}
		committed, err := stores.legacy.ListAfter(ctx, workspaceID, 0, domain.MaxReplayPageSize)
		if err != nil || len(committed) != 1 || committed[0].ConversationID == nil || *committed[0].ConversationID != conversationID {
			t.Fatalf("legacy read after GORM commit=%#v err=%v", committed, err)
		}

		rollbackConversationID := foundation.ID("7e100000-0000-4000-8000-000000000003")
		rollbackRequest := eventAppendRequest(workspaceID, rollbackConversationID, "rollback")
		rollback := errors.New("force Event scoped rollback")
		err = stores.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			if appendErr := insertEventConversationScoped(callbackCtx, scope, workspaceID, rollbackConversationID, "gorm-scoped-rollback"); appendErr != nil {
				return appendErr
			}
			if _, replayed, appendErr := stores.gorm.AppendScoped(callbackCtx, scope, rollbackRequest); appendErr != nil || replayed {
				t.Fatalf("GORM AppendScoped rollback replayed=%t err=%v", replayed, appendErr)
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("GORM scoped rollback error=%v", err)
		}
		var rolledBackRows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND source_event_ref=$2`, string(workspaceID), rollbackRequest.SourceEventRef).Scan(&rolledBackRows); err != nil || rolledBackRows != 0 {
			t.Fatalf("GORM scoped rollback rows=%d err=%v", rolledBackRows, err)
		}

		for name, scope := range map[string]foundation.TransactionScope{
			"nil":     nil,
			"foreign": foreignEventTransactionScope{},
			"stale":   expiredScope,
		} {
			if _, _, appendErr := stores.gorm.AppendScoped(ctx, scope, request); eventErrorCode(appendErr) != domain.ErrorCodeAppendTransactionUnavailable {
				t.Fatalf("%s scope append code=%q err=%v", name, eventErrorCode(appendErr), appendErr)
			}
		}

		lossRequest := eventAppendRequest(workspaceID, conversationID, "response-loss")
		loss := errors.New("injected Event commit response loss")
		lossUoW := eventPostCommitErrorUnitOfWork{inner: stores.unitOfWork, err: loss}
		err = lossUoW.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			_, replayed, appendErr := stores.gorm.AppendScoped(callbackCtx, scope, lossRequest)
			if appendErr != nil || replayed {
				t.Fatalf("GORM response-loss append replayed=%t err=%v", replayed, appendErr)
			}
			return nil
		})
		if !errors.Is(err, loss) {
			t.Fatalf("GORM response-loss error=%v", err)
		}
		if err := stores.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			_, replayed, appendErr := stores.gorm.AppendScoped(callbackCtx, scope, lossRequest)
			if appendErr != nil || !replayed {
				t.Fatalf("GORM replay after response loss replayed=%t err=%v", replayed, appendErr)
			}
			return nil
		}); err != nil {
			t.Fatalf("GORM replay after response loss: %v", err)
		}
		assertEventPoolReleased(t, pool, "GORM scoped transaction paths")
	})

	t.Run("concurrent claims SQLSTATE cancellation and resources", func(t *testing.T) {
		workspaceID := foundation.ID("7e200000-0000-4000-8000-000000000001")
		conversationID := foundation.ID("7e200000-0000-4000-8000-000000000002")
		seedEventWorkspaces(t, ctx, pool, workspaceID)
		conversationTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		insertEventConversation(t, ctx, conversationTx, workspaceID, conversationID, "gorm-concurrent")
		if err := conversationTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		request := eventAppendRequest(workspaceID, conversationID, "concurrent")
		exact := runConcurrentScopedAppends(t, ctx, stores, request, request)
		assertConcurrentEventClaims(t, exact, 1, 1)
		conflicting := request
		conflicting.Type = "conversation.updated"
		conflicting.SourceEventRef = "conversation.updated:" + string(conversationID) + ":concurrent"
		conflicting.ResourceVersion = 2
		conflictResults := runConcurrentScopedAppends(t, ctx, stores, conflicting, requestWithVersion(conflicting, 3))
		assertConcurrentEventClaims(t, conflictResults, 1, 0)
		if eventErrorCode(conflictResults[0].err) != domain.ErrorCodeAppendConflict && eventErrorCode(conflictResults[1].err) != domain.ErrorCodeAppendConflict {
			t.Fatalf("GORM concurrent conflict results=%#v", conflictResults)
		}

		missingConversationID := foundation.ID("7e200000-0000-4000-8000-000000000003")
		invalid := eventAppendRequest(workspaceID, missingConversationID, "foreign-key")
		failure := errors.New("rollback after expected append failure")
		err = stores.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			_, _, appendErr := stores.gorm.AppendScoped(callbackCtx, scope, invalid)
			assertEventFoundationError(t, appendErr, foundation.ErrorConsistencyViolation, domain.ErrorCodeAppendBindingInvalid, false)
			return failure
		})
		if !errors.Is(err, failure) {
			t.Fatalf("GORM binding failure transaction=%v", err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO ops.server_event(
			workspace_id,event_type,resource_ref,resource_version,payload_summary,schema_version,source_event_ref,occurred_at,expires_at
		) VALUES($1,'workflow.run.started','workflow.run:invalid',0,'{}'::jsonb,1,'workflow.outbox_event:invalid',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP + INTERVAL '24 hours')`, string(workspaceID))
		assertEventPostgresCode(t, err, "23514")

		cause := errors.New("caller canceled Event replay")
		canceledCtx, cancelReplay := context.WithCancelCause(ctx)
		cancelReplay(cause)
		_, err = stores.gorm.CurrentWatermark(canceledCtx, workspaceID)
		if eventErrorCode(err) != domain.ErrorCodeStoreUnavailable || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
			t.Fatalf("GORM canceled replay code=%q err=%v", eventErrorCode(err), err)
		}
		assertEventPoolReleased(t, pool, "GORM concurrent failure paths")
	})
}

const insertEventSQL = `
	INSERT INTO ops.server_event(
		workspace_id,event_type,resource_ref,resource_version,payload_summary,schema_version,
		source_event_ref,occurred_at,expires_at
	) VALUES($1,$2,$3,1,$6::jsonb,1,$4,$5::timestamptz,$5::timestamptz + INTERVAL '24 hours')
	RETURNING seq`

func insertServerEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, eventType, sourceID string, occurredAt time.Time, summary string) int64 {
	t.Helper()
	var sequence int64
	if err := pool.QueryRow(ctx, insertEventSQL, string(workspaceID), eventType, "workflow.run:"+sourceID, "workflow.outbox_event:"+sourceID, occurredAt, summary).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	return sequence
}

func seedEventWorkspaces(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceIDs ...foundation.ID) {
	t.Helper()
	for index, workspaceID := range workspaceIDs {
		_, err := pool.Exec(ctx, `INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES($1,$2,$3,$3,now(),'inactive',1,now(),now())`, string(workspaceID), fmt.Sprintf("events-%d", index), "/tmp/events-"+string(workspaceID))
		if err != nil {
			t.Fatal(err)
		}
	}
}

func insertEventConversation(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, conversationID foundation.ID, idempotencyKey string) {
	t.Helper()
	_, err := tx.Exec(ctx, `INSERT INTO agent.conversation(
		id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,archived_at,idempotency_key,request_hash
	) VALUES($1,$2,'open',NULL,1,now(),now(),now(),NULL,$3,repeat('a',64))`, string(conversationID), string(workspaceID), idempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
}

type eventIntegrationStores struct {
	platform   *platformpostgres.Pool
	legacy     *Store
	gorm       *GORMStore
	unitOfWork foundation.UnitOfWork
}

func requireEventIntegrationStores(t *testing.T) eventIntegrationStores {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
	platform := fixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("test database fixture did not provide a shared platform pool")
	}
	legacy, err := NewStore(platform.DB())
	if err != nil {
		t.Fatalf("NewStore from shared platform pool: %v", err)
	}
	gormStore, err := NewGORMStore(platform)
	if err != nil {
		t.Fatalf("NewGORMStore from shared platform pool: %v", err)
	}
	unitOfWork, err := platform.UnitOfWork()
	if err != nil {
		t.Fatalf("UnitOfWork from shared platform pool: %v", err)
	}
	return eventIntegrationStores{platform: platform, legacy: legacy, gorm: gormStore, unitOfWork: unitOfWork}
}

func assertEventStoreReadParity(t *testing.T, ctx context.Context, stores eventIntegrationStores, workspaceID foundation.ID, wantEarliest, wantWatermark int64) {
	t.Helper()
	legacyWatermark, legacyErr := stores.legacy.CurrentWatermark(ctx, workspaceID)
	gormWatermark, gormErr := stores.gorm.CurrentWatermark(ctx, workspaceID)
	if legacyErr != nil || gormErr != nil || legacyWatermark != wantWatermark || gormWatermark != legacyWatermark {
		t.Fatalf("watermark legacy=%d/%v gorm=%d/%v want=%d", legacyWatermark, legacyErr, gormWatermark, gormErr, wantWatermark)
	}
	legacyEarliest, legacyErr := stores.legacy.EarliestRetained(ctx, workspaceID)
	gormEarliest, gormErr := stores.gorm.EarliestRetained(ctx, workspaceID)
	if legacyErr != nil || gormErr != nil || !reflect.DeepEqual(gormEarliest, legacyEarliest) {
		t.Fatalf("earliest legacy=%v/%v gorm=%v/%v", legacyEarliest, legacyErr, gormEarliest, gormErr)
	}
	if wantEarliest == 0 {
		if legacyEarliest != nil {
			t.Fatalf("earliest=%v, want nil", legacyEarliest)
		}
	} else if legacyEarliest == nil || *legacyEarliest != wantEarliest {
		t.Fatalf("earliest=%v, want=%d", legacyEarliest, wantEarliest)
	}
	for _, limit := range []int{1, domain.MaxReplayPageSize} {
		legacyEvents, legacyErr := stores.legacy.ListAfter(ctx, workspaceID, 0, limit)
		gormEvents, gormErr := stores.gorm.ListAfter(ctx, workspaceID, 0, limit)
		if legacyErr != nil || gormErr != nil || !reflect.DeepEqual(gormEvents, legacyEvents) {
			t.Fatalf("page limit=%d legacy=%#v/%v gorm=%#v/%v", limit, legacyEvents, legacyErr, gormEvents, gormErr)
		}
	}
}

func insertEventConversationScoped(ctx context.Context, scope foundation.TransactionScope, workspaceID, conversationID foundation.ID, idempotencyKey string) error {
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	return transaction.WithContext(ctx).Exec(`INSERT INTO agent.conversation(
		id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,archived_at,idempotency_key,request_hash
	) VALUES(?,?,'open',NULL,1,now(),now(),now(),NULL,?,repeat('a',64))`, string(conversationID), string(workspaceID), idempotencyKey).Error
}

func eventAppendRequest(workspaceID, conversationID foundation.ID, suffix string) domain.AppendRequest {
	return domain.AppendRequest{
		WorkspaceID: workspaceID, ConversationID: &conversationID,
		Type: "conversation.created", ResourceRef: "conversation:" + string(conversationID), ResourceVersion: 1,
		PayloadSummary: domain.PayloadSummary{Status: "open"}, SchemaVersion: 1,
		SourceEventRef: "conversation.created:" + string(conversationID) + ":" + suffix,
		// 必须跟随当前时间：expires_at=occurred_at+24h 的保留窗口由 SQL 的
		// CURRENT_TIMESTAMP 过滤判定，硬编码日期会在 24 小时后让事件过期失效。
		// 固定纳秒尾数 123456789 用于验证微秒截断精度保留。
		OccurredAt:     time.Now().UTC().Truncate(time.Second).Add(123456789),
	}
}

func requestWithVersion(request domain.AppendRequest, version int64) domain.AppendRequest {
	request.ResourceVersion = version
	return request
}

type foreignEventTransactionScope struct{}

func (foreignEventTransactionScope) TransactionScope() {}

type eventPostCommitErrorUnitOfWork struct {
	inner foundation.UnitOfWork
	err   error
}

func (unitOfWork eventPostCommitErrorUnitOfWork) Within(ctx context.Context, options foundation.TransactionOptions, work foundation.TransactionFunc) error {
	if err := unitOfWork.inner.Within(ctx, options, work); err != nil {
		return err
	}
	return unitOfWork.err
}

func assertEventFoundationError(t *testing.T, err error, kind foundation.ErrorKind, code string, retryable bool) {
	t.Helper()
	var classified *foundation.Error
	if err == nil || !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code || classified.Retryable != retryable {
		t.Fatalf("error=%v classified=%#v want kind=%s code=%s retryable=%t", err, classified, kind, code, retryable)
	}
}

func assertEventPostgresCode(t *testing.T, err error, want string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if err == nil || !errors.As(err, &postgresError) || postgresError.Code != want {
		t.Fatalf("error=%v code=%q want=%s", err, eventPostgresErrorCode(postgresError), want)
	}
}

func eventPostgresErrorCode(err *pgconn.PgError) string {
	if err == nil {
		return ""
	}
	return err.Code
}

func assertEventPoolReleased(t *testing.T, pool *pgxpool.Pool, scope string) {
	t.Helper()
	if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("%s retained %d shared pool connections", scope, acquired)
	}
}

// TestEventReplayPlansAtVolumeUseDefaultPlanner 在目标数据量、冷 Workspace 布局与
// 默认 planner 下验证 watermark/earliest/ListAfter 使用
// idx_ops_server_event_workspace_seq，且 replay 单页结果有界。冷布局指目标
// Workspace 的事件集中在 seq 低端、10 万更新噪声事件在其后：这是复合索引不可
// 替代的场景（pkey 倒扫需过滤全部噪声），不依赖 enable_seqscan=off 的小数据量让步。
func TestEventReplayPlansAtVolumeUseDefaultPlanner(t *testing.T) {
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
	pool := fixture.Pool().DB()
	ctx := context.Background()
	targetWorkspace := foundation.ID("7c000000-0000-4000-8000-000000000001")
	seedEventWorkspaces(t, ctx, pool, targetWorkspace)

	// 目标 Workspace 的 2,000 事件先写入（低 seq），一半已过期。
	if _, err := pool.Exec(ctx, `INSERT INTO ops.server_event(
		workspace_id,event_type,resource_ref,resource_version,payload_summary,schema_version,
		source_event_ref,occurred_at,expires_at
	) SELECT $1,'seed','resource-' || s,1,'{}'::jsonb,1,'seed-target-' || s,
		now() - (s % 48) * INTERVAL '1 hour',
		now() - (s % 48) * INTERVAL '1 hour' + INTERVAL '24 hours'
	FROM generate_series(1, 2000) s`, string(targetWorkspace)); err != nil {
		t.Fatal(err)
	}
	// 50 个噪声 Workspace 各 2,000 事件随后写入（高 seq），目标占比约 2%。
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) SELECT gen_random_uuid(),'events-noise-' || g,'/tmp/events-noise-' || g,'/tmp/events-noise-' || g,
		now(),'inactive',1,now(),now()
	FROM generate_series(1, 50) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.server_event(
		workspace_id,event_type,resource_ref,resource_version,payload_summary,schema_version,
		source_event_ref,occurred_at,expires_at
	) SELECT w.id,'seed','resource-' || s,1,'{}'::jsonb,1,'seed-noise-' || w.id::text || '-' || s,
		now() - (s % 48) * INTERVAL '1 hour',
		now() - (s % 48) * INTERVAL '1 hour' + INTERVAL '24 hours'
	FROM core.workspace w CROSS JOIN generate_series(1, 2000) s
	WHERE w.name LIKE 'events-noise-%'`); err != nil {
		t.Fatal(err)
	}
	// VACUUM ANALYZE 对齐生产 autovacuum 后的统计与可见性状态，
	// 使 index-only scan 与 planner 成本估计可代表生产。
	if _, err := pool.Exec(ctx, `VACUUM ANALYZE ops.server_event`); err != nil {
		t.Fatal(err)
	}

	queries := map[string]struct {
		sql  string
		args []any
	}{
		"watermark": {`SELECT COALESCE(MAX(seq),0) FROM ops.server_event WHERE workspace_id=$1::uuid`,
			[]any{string(targetWorkspace)}},
		"earliest": {`SELECT MIN(seq) FROM ops.server_event WHERE workspace_id=$1::uuid AND expires_at>CURRENT_TIMESTAMP`,
			[]any{string(targetWorkspace)}},
		"replay": {`SELECT seq FROM ops.server_event WHERE workspace_id=$1::uuid AND seq>$2 AND expires_at>CURRENT_TIMESTAMP ORDER BY seq ASC LIMIT $3`,
			[]any{string(targetWorkspace), int64(0), domain.MaxReplayPageSize}},
	}
	for name, query := range queries {
		rows, err := pool.Query(ctx, `EXPLAIN (ANALYZE, COSTS OFF, SUMMARY OFF, TIMING OFF) `+query.sql, query.args...)
		if err != nil {
			t.Fatalf("Event %s volume EXPLAIN: %v", name, err)
		}
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatalf("scan Event %s volume EXPLAIN: %v", name, err)
			}
			plan = append(plan, line)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("Event %s volume EXPLAIN rows: %v", name, err)
		}
		rows.Close()
		joined := strings.Join(plan, "\n")
		if strings.Contains(joined, "Seq Scan") {
			t.Fatalf("Event %s volume EXPLAIN used Seq Scan at target volume: %s", name, joined)
		}
		if !strings.Contains(joined, "idx_ops_server_event_workspace_seq") {
			t.Fatalf("Event %s volume EXPLAIN did not use idx_ops_server_event_workspace_seq under default planner: %s", name, joined)
		}
	}

	// replay 单页结果有界：LIMIT 由 domain.MaxReplayPageSize 约束。
	var pageCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM (
		SELECT seq FROM ops.server_event
		WHERE workspace_id=$1::uuid AND seq>$2 AND expires_at>CURRENT_TIMESTAMP
		ORDER BY seq ASC LIMIT $3
	) page`, string(targetWorkspace), int64(0), domain.MaxReplayPageSize).Scan(&pageCount); err != nil {
		t.Fatal(err)
	}
	if pageCount > domain.MaxReplayPageSize {
		t.Fatalf("replay page returned %d rows, bound %d", pageCount, domain.MaxReplayPageSize)
	}
	assertEventPoolReleased(t, pool, "volume EXPLAIN")
}

func assertEventReplayPlans(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Event EXPLAIN transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatalf("disable seqscan for Event EXPLAIN: %v", err)
	}
	for name, query := range map[string]string{
		"watermark": `SELECT MAX(seq) FROM ops.server_event WHERE workspace_id=$1::uuid`,
		"earliest":  `SELECT MIN(seq) FROM ops.server_event WHERE workspace_id=$1::uuid AND expires_at>CURRENT_TIMESTAMP`,
		"replay":    `SELECT seq FROM ops.server_event WHERE workspace_id=$1::uuid AND seq>$2 AND expires_at>CURRENT_TIMESTAMP ORDER BY seq ASC LIMIT $3`,
	} {
		args := []any{string(workspaceID)}
		if name == "replay" {
			args = append(args, int64(0), domain.MaxReplayPageSize)
		}
		rows, err := tx.Query(ctx, `EXPLAIN (COSTS OFF) `+query, args...)
		if err != nil {
			t.Fatalf("Event %s EXPLAIN: %v", name, err)
		}
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatalf("scan Event %s EXPLAIN: %v", name, err)
			}
			plan = append(plan, line)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("Event %s EXPLAIN rows: %v", name, err)
		}
		rows.Close()
		if !strings.Contains(strings.Join(plan, "\n"), "idx_ops_server_event_workspace_seq") {
			t.Fatalf("Event %s EXPLAIN did not use idx_ops_server_event_workspace_seq: %s", name, strings.Join(plan, "\n"))
		}
	}
}

func runConcurrentScopedAppends(t *testing.T, parent context.Context, stores eventIntegrationStores, requests ...domain.AppendRequest) []concurrentAppendResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan concurrentAppendResult, len(requests))
	var workers sync.WaitGroup
	workers.Add(len(requests))
	for _, request := range requests {
		request := request
		go func() {
			defer workers.Done()
			<-start
			var result concurrentAppendResult
			err := stores.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
				event, replayed, appendErr := stores.gorm.AppendScoped(callbackCtx, scope, request)
				if appendErr != nil {
					return appendErr
				}
				result.event, result.replayed = event, replayed
				return nil
			})
			result.err = err
			results <- result
		}()
	}
	close(start)
	collected := make([]concurrentAppendResult, 0, len(requests))
	for range requests {
		select {
		case result := <-results:
			collected = append(collected, result)
		case <-ctx.Done():
			t.Fatalf("wait for concurrent GORM appends: %v", ctx.Err())
		}
	}
	workers.Wait()
	return collected
}

func assertConcurrentEventClaims(t *testing.T, results []concurrentAppendResult, wantCreated, wantReplayed int) {
	t.Helper()
	created, replayed := 0, 0
	var sequence int64
	for _, result := range results {
		if result.err != nil {
			if eventErrorCode(result.err) == domain.ErrorCodeAppendConflict {
				continue
			}
			t.Fatalf("concurrent GORM append err=%v", result.err)
		}
		if sequence != 0 && result.event.Seq != sequence {
			t.Fatalf("concurrent GORM sequences differ: %d and %d", sequence, result.event.Seq)
		}
		sequence = result.event.Seq
		if result.replayed {
			replayed++
		} else {
			created++
		}
	}
	if created != wantCreated || replayed != wantReplayed {
		t.Fatalf("concurrent GORM created=%d replayed=%d want=%d/%d results=%#v", created, replayed, wantCreated, wantReplayed, results)
	}
}

func eventErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
