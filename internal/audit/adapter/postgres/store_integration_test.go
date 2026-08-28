//go:build integration && testcontainers

package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuditStorePostgresAppendOnlyAndIdempotency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	stores := requireAuditIntegrationStores(t)
	pool := stores.platform.DB()
	store := stores.legacy

	t.Run("exact replay and conflict", func(t *testing.T) {
		event := integrationEvent(t, "exact")
		created, replayed, err := store.Append(ctx, event)
		if err != nil || replayed {
			t.Fatalf("first Append event=%#v replayed=%t err=%v", created, replayed, err)
		}
		retry := event
		retry.ID = integrationID(t)
		existing, replayed, err := store.Append(ctx, retry)
		if err != nil || !replayed || existing.ID != created.ID {
			t.Fatalf("replay event=%#v replayed=%t err=%v", existing, replayed, err)
		}
		conflicting := retry
		conflicting.Action = "security.block"
		_, _, err = store.Append(ctx, conflicting)
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Kind != foundation.ErrorVersionConflict || classified.Code != domain.ErrorCodeIdempotencyConflict {
			t.Fatalf("conflict error=%v classified=%#v", err, classified)
		}
		assertGlobalRowCount(t, ctx, pool, event.IdempotencyKey, 1)
	})

	t.Run("global workspace concurrent replay creates one row", func(t *testing.T) {
		event := integrationEvent(t, "concurrent")
		const callers = 12
		var wait sync.WaitGroup
		wait.Add(callers)
		errorsByCaller := make(chan error, callers)
		results := make(chan domain.Event, callers)
		createdCount := make(chan bool, callers)
		for range callers {
			go func() {
				defer wait.Done()
				got, replayed, appendErr := store.Append(ctx, event)
				if appendErr != nil {
					errorsByCaller <- appendErr
					return
				}
				results <- got
				createdCount <- !replayed
			}()
		}
		wait.Wait()
		close(errorsByCaller)
		close(results)
		close(createdCount)
		for appendErr := range errorsByCaller {
			t.Fatalf("concurrent Append: %v", appendErr)
		}
		var firstID foundation.ID
		for got := range results {
			if firstID == "" {
				firstID = got.ID
			}
			if got.ID != firstID {
				t.Fatalf("concurrent replay returned different IDs: %s and %s", firstID, got.ID)
			}
		}
		created := 0
		for wasCreated := range createdCount {
			if wasCreated {
				created++
			}
		}
		if created != 1 {
			t.Fatalf("created calls=%d, want 1", created)
		}
		assertGlobalRowCount(t, ctx, pool, event.IdempotencyKey, 1)
	})

	t.Run("workspace scoped replay creates one row", func(t *testing.T) {
		workspaceID := integrationWorkspace(t, ctx, pool)
		event := integrationEvent(t, "workspace")
		event.WorkspaceID = &workspaceID
		created, replayed, err := store.Append(ctx, event)
		if err != nil || replayed {
			t.Fatalf("first workspace Append event=%#v replayed=%t err=%v", created, replayed, err)
		}
		retry := event
		retry.ID = integrationID(t)
		existing, replayed, err := store.Append(ctx, retry)
		if err != nil || !replayed || existing.ID != created.ID {
			t.Fatalf("workspace replay event=%#v replayed=%t err=%v", existing, replayed, err)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.audit_event WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), event.IdempotencyKey).Scan(&count); err != nil {
			t.Fatalf("count workspace audit rows: %v", err)
		}
		if count != 1 {
			t.Fatalf("workspace audit rows=%d, want 1", count)
		}
	})

	t.Run("pre-existing duplicate idempotency rows fail closed", func(t *testing.T) {
		event := integrationEvent(t, "duplicate-corruption")
		redacted, err := event.Redacted()
		if err != nil {
			t.Fatalf("Redacted: %v", err)
		}
		correlation, payload, err := encodeAuditJSON(redacted)
		if err != nil {
			t.Fatalf("encodeAuditJSON: %v", err)
		}
		for _, id := range []foundation.ID{redacted.ID, integrationID(t)} {
			if _, err := pool.Exec(ctx, `INSERT INTO ops.audit_event(
				id,workspace_id,actor_type,actor_ref,action,resource_type,resource_ref,
				idempotency_key,correlation,payload,occurred_at
			) VALUES($1,NULL,$2,NULL,$3,NULL,NULL,$4,$5::jsonb,$6::jsonb,$7)`,
				string(id), string(redacted.ActorType), redacted.Action, redacted.IdempotencyKey,
				correlation, payload, redacted.OccurredAt,
			); err != nil {
				t.Fatalf("seed duplicate audit row: %v", err)
			}
		}
		_, _, err = store.Append(ctx, event)
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeCorrupt {
			t.Fatalf("error=%v classified=%#v, want corrupt duplicate", err, classified)
		}
		assertGlobalRowCount(t, ctx, pool, event.IdempotencyKey, 2)
	})

	t.Run("plaintext is redacted before persistence", func(t *testing.T) {
		event := integrationEvent(t, "redaction")
		event.Correlation = []byte(`{"cookie":"session=plain-cookie"}`)
		event.Metadata = []byte(`{"nested":{"token":"plain-token"},"content_hash":"sha256:safe"}`)
		created, _, err := store.Append(ctx, event)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		var correlation, payload string
		if err := pool.QueryRow(ctx, `SELECT correlation::text,payload::text FROM ops.audit_event WHERE id=$1`, string(created.ID)).Scan(&correlation, &payload); err != nil {
			t.Fatalf("read persisted JSON: %v", err)
		}
		for _, secret := range []string{"plain-cookie", "plain-token"} {
			if strings.Contains(correlation, secret) || strings.Contains(payload, secret) {
				t.Fatalf("database persisted plaintext %q: correlation=%s payload=%s", secret, correlation, payload)
			}
		}
		if !strings.Contains(correlation, domain.RedactedValue) || !strings.Contains(payload, domain.RedactedValue) {
			t.Fatalf("database did not persist redaction markers: correlation=%s payload=%s", correlation, payload)
		}
	})

	t.Run("update and delete are rejected", func(t *testing.T) {
		event := integrationEvent(t, "append-only")
		created, _, err := store.Append(ctx, event)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		for _, statement := range []string{
			`UPDATE ops.audit_event SET action='tampered' WHERE id=$1`,
			`DELETE FROM ops.audit_event WHERE id=$1`,
		} {
			_, err := pool.Exec(ctx, statement, string(created.ID))
			var postgresError *pgconn.PgError
			if !errors.As(err, &postgresError) || postgresError.Code != "55000" {
				t.Fatalf("statement %q error=%v code=%q, want 55000", statement, err, postgresErrorCode(postgresError))
			}
		}
	})
}

func TestAuditGORMStorePostgresEquivalenceAndScopedTransaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	stores := requireAuditIntegrationStores(t)
	pool := stores.platform.DB()

	t.Run("legacy and GORM paths exactly replay the same persisted facts", func(t *testing.T) {
		legacyFirst := integrationEvent(t, "cross-legacy-first")
		legacyCreated, replayed, err := stores.legacy.Append(ctx, legacyFirst)
		if err != nil || replayed || legacyCreated.ID != legacyFirst.ID {
			t.Fatalf("legacy initial append event=%#v replayed=%t err=%v", legacyCreated, replayed, err)
		}
		gormRead, err := stores.gorm.Get(ctx, legacyFirst.ID)
		if err != nil || !domain.EqualBinding(gormRead, legacyFirst) {
			t.Fatalf("GORM read legacy event=%#v err=%v", gormRead, err)
		}
		legacyRetry := legacyFirst
		legacyRetry.ID = integrationID(t)
		gormReplayed, replayed, err := stores.gorm.Append(ctx, legacyRetry)
		if err != nil || !replayed || gormReplayed.ID != legacyFirst.ID || !domain.EqualBinding(gormReplayed, legacyFirst) {
			t.Fatalf("GORM replay legacy event=%#v replayed=%t err=%v", gormReplayed, replayed, err)
		}

		gormFirst := integrationEvent(t, "cross-gorm-first")
		gormCreated, replayed, err := stores.gorm.Append(ctx, gormFirst)
		if err != nil || replayed || gormCreated.ID != gormFirst.ID {
			t.Fatalf("GORM initial append event=%#v replayed=%t err=%v", gormCreated, replayed, err)
		}
		legacyRead, err := stores.legacy.Get(ctx, gormFirst.ID)
		if err != nil || !domain.EqualBinding(legacyRead, gormFirst) {
			t.Fatalf("legacy read GORM event=%#v err=%v", legacyRead, err)
		}
		gormRetry := gormFirst
		gormRetry.ID = integrationID(t)
		legacyReplayed, replayed, err := stores.legacy.Append(ctx, gormRetry)
		if err != nil || !replayed || legacyReplayed.ID != gormFirst.ID || !domain.EqualBinding(legacyReplayed, gormFirst) {
			t.Fatalf("legacy replay GORM event=%#v replayed=%t err=%v", legacyReplayed, replayed, err)
		}
	})

	t.Run("exact replay conflict and workspace isolation", func(t *testing.T) {
		event := integrationEvent(t, "gorm-exact")
		created, replayed, err := stores.gorm.Append(ctx, event)
		if err != nil || replayed || created.ID != event.ID || !domain.EqualBinding(created, event) {
			t.Fatalf("first GORM Append event=%#v replayed=%t err=%v", created, replayed, err)
		}
		retry := event
		retry.ID = integrationID(t)
		existing, replayed, err := stores.gorm.Append(ctx, retry)
		if err != nil || !replayed || existing.ID != created.ID || !domain.EqualBinding(existing, event) {
			t.Fatalf("GORM replay event=%#v replayed=%t err=%v", existing, replayed, err)
		}
		conflicting := retry
		conflicting.Action = "security.block"
		assertAuditFoundationError(t, mustGORMAppendError(ctx, stores.gorm, conflicting), foundation.ErrorVersionConflict, domain.ErrorCodeIdempotencyConflict)
		assertGlobalRowCount(t, ctx, pool, event.IdempotencyKey, 1)

		workspaceID := integrationWorkspace(t, ctx, pool)
		workspaceEvent := integrationEvent(t, "gorm-workspace")
		workspaceEvent.WorkspaceID = &workspaceID
		workspaceCreated, replayed, err := stores.gorm.Append(ctx, workspaceEvent)
		if err != nil || replayed || workspaceCreated.ID != workspaceEvent.ID {
			t.Fatalf("first workspace GORM Append event=%#v replayed=%t err=%v", workspaceCreated, replayed, err)
		}
		workspaceRetry := workspaceEvent
		workspaceRetry.ID = integrationID(t)
		workspaceExisting, replayed, err := stores.gorm.Append(ctx, workspaceRetry)
		if err != nil || !replayed || workspaceExisting.ID != workspaceCreated.ID {
			t.Fatalf("workspace GORM replay event=%#v replayed=%t err=%v", workspaceExisting, replayed, err)
		}
		assertAuditRowCount(t, ctx, pool, workspaceID, workspaceEvent.IdempotencyKey, 1)
	})

	t.Run("concurrent global replay creates one row", func(t *testing.T) {
		event := integrationEvent(t, "gorm-concurrent")
		const callers = 12
		var wait sync.WaitGroup
		wait.Add(callers)
		errorsByCaller := make(chan error, callers)
		results := make(chan domain.Event, callers)
		createdCount := make(chan bool, callers)
		for range callers {
			go func() {
				defer wait.Done()
				got, replayed, appendErr := stores.gorm.Append(ctx, event)
				if appendErr != nil {
					errorsByCaller <- appendErr
					return
				}
				results <- got
				createdCount <- !replayed
			}()
		}
		wait.Wait()
		close(errorsByCaller)
		close(results)
		close(createdCount)
		for appendErr := range errorsByCaller {
			t.Fatalf("concurrent GORM Append: %v", appendErr)
		}
		var firstID foundation.ID
		for got := range results {
			if firstID == "" {
				firstID = got.ID
			}
			if got.ID != firstID {
				t.Fatalf("concurrent GORM replay returned different IDs: %s and %s", firstID, got.ID)
			}
		}
		created := 0
		for wasCreated := range createdCount {
			if wasCreated {
				created++
			}
		}
		if created != 1 {
			t.Fatalf("created calls=%d, want 1", created)
		}
		assertGlobalRowCount(t, ctx, pool, event.IdempotencyKey, 1)
	})

	t.Run("duplicate corruption and plaintext persistence fail closed", func(t *testing.T) {
		event := integrationEvent(t, "gorm-duplicate-corruption")
		redacted, err := event.Redacted()
		if err != nil {
			t.Fatalf("Redacted: %v", err)
		}
		correlation, payload, err := encodeAuditJSON(redacted)
		if err != nil {
			t.Fatalf("encodeAuditJSON: %v", err)
		}
		for _, id := range []foundation.ID{redacted.ID, integrationID(t)} {
			if _, err := pool.Exec(ctx, `INSERT INTO ops.audit_event(
				id,workspace_id,actor_type,actor_ref,action,resource_type,resource_ref,
				idempotency_key,correlation,payload,occurred_at
			) VALUES($1,NULL,$2,NULL,$3,NULL,NULL,$4,$5::jsonb,$6::jsonb,$7)`,
				string(id), string(redacted.ActorType), redacted.Action, redacted.IdempotencyKey,
				correlation, payload, redacted.OccurredAt,
			); err != nil {
				t.Fatalf("seed duplicate audit row: %v", err)
			}
		}
		_, _, err = stores.gorm.Append(ctx, event)
		assertAuditFoundationError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt)
		assertGlobalRowCount(t, ctx, pool, event.IdempotencyKey, 2)

		corrupt := integrationEvent(t, "gorm-plaintext-corruption")
		if _, err := pool.Exec(ctx, `INSERT INTO ops.audit_event(
			id,workspace_id,actor_type,actor_ref,action,resource_type,resource_ref,
			idempotency_key,correlation,payload,occurred_at
		) VALUES($1,NULL,$2,NULL,$3,NULL,NULL,$4,$5::jsonb,$6::jsonb,$7)`,
			string(corrupt.ID), string(corrupt.ActorType), corrupt.Action, corrupt.IdempotencyKey,
			`{"request_id":"safe-request"}`,
			`{"audit_outcome":"SUCCEEDED","audit_schema_version":"audit-event/v1","token":"plaintext-secret"}`,
			corrupt.OccurredAt,
		); err != nil {
			t.Fatalf("seed corrupt audit row: %v", err)
		}
		_, err = stores.gorm.Get(ctx, corrupt.ID)
		assertAuditFoundationError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt)
		if strings.Contains(err.Error(), "plaintext-secret") {
			t.Fatalf("corrupt GORM read leaked plaintext: %v", err)
		}
	})

	t.Run("get list stable cursor and resources", func(t *testing.T) {
		globalEvent := integrationEvent(t, "gorm-global-list")
		if _, replayed, err := stores.gorm.Append(ctx, globalEvent); err != nil || replayed {
			t.Fatalf("seed global GORM list event replayed=%t err=%v", replayed, err)
		}
		globalPage, err := stores.gorm.List(ctx, domain.ListQuery{Limit: 1})
		if err != nil || len(globalPage) != 1 || globalPage[0].WorkspaceID != nil || globalPage[0].ID != globalEvent.ID {
			t.Fatalf("GORM global List items=%#v err=%v", globalPage, err)
		}

		workspaceID := integrationWorkspace(t, ctx, pool)
		occurredAt := time.Date(2026, 8, 27, 10, 11, 12, 123456000, time.UTC)
		for index := range 3 {
			event := integrationEvent(t, "gorm-keyset-"+string(rune('a'+index)))
			event.WorkspaceID = &workspaceID
			event.OccurredAt = occurredAt
			if _, replayed, err := stores.gorm.Append(ctx, event); err != nil || replayed {
				t.Fatalf("seed GORM keyset event replayed=%t err=%v", replayed, err)
			}
		}
		page, err := stores.gorm.List(ctx, domain.ListQuery{WorkspaceID: &workspaceID, Limit: 2})
		if err != nil || len(page) != 2 {
			t.Fatalf("first GORM List items=%#v err=%v", page, err)
		}
		for _, event := range page {
			if event.WorkspaceID == nil || *event.WorkspaceID != workspaceID || !event.OccurredAt.Equal(occurredAt) {
				t.Fatalf("GORM List crossed workspace or timestamp boundary: %#v", event)
			}
		}
		next, err := stores.gorm.List(ctx, domain.ListQuery{
			WorkspaceID: &workspaceID,
			Before:      page[1].OccurredAt,
			BeforeID:    page[1].ID,
			Limit:       2,
		})
		if err != nil || len(next) != 1 || next[0].ID == page[0].ID || next[0].ID == page[1].ID {
			t.Fatalf("GORM keyset next=%#v err=%v", next, err)
		}
		got, err := stores.gorm.Get(ctx, page[0].ID)
		if err != nil || got.ID != page[0].ID || !domain.EqualBinding(got, page[0]) {
			t.Fatalf("GORM Get event=%#v err=%v", got, err)
		}
		_, err = stores.gorm.Get(ctx, integrationID(t))
		assertAuditFoundationError(t, err, foundation.ErrorNotFound, domain.ErrorCodeNotFound)
		assertAuditListIndex(t, ctx, pool, workspaceID)
		if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
			t.Fatalf("GORM read leaked shared pool connections: acquired=%d", acquired)
		}
	})

	t.Run("scoped transaction commit rollback lifecycle and transaction id", func(t *testing.T) {
		committed := integrationEvent(t, "gorm-scoped-commit")
		var expiredScope foundation.TransactionScope
		if err := stores.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			expiredScope = scope
			created, replayed, appendErr := stores.gorm.AppendScoped(callbackCtx, scope, committed)
			if appendErr != nil || replayed || created.ID != committed.ID {
				t.Fatalf("GORM AppendScoped event=%#v replayed=%t err=%v", created, replayed, appendErr)
			}
			read, readErr := stores.gorm.GetScoped(callbackCtx, scope, committed.ID)
			if readErr != nil || read.ID != committed.ID || !domain.EqualBinding(read, committed) {
				t.Fatalf("GORM GetScoped event=%#v err=%v", read, readErr)
			}
			transaction, transactionErr := platformpostgres.GORMTransaction(scope)
			if transactionErr != nil {
				return transactionErr
			}
			var sameTransaction bool
			if rowErr := transaction.WithContext(callbackCtx).Raw(
				`SELECT transaction_id=pg_current_xact_id() FROM ops.audit_event WHERE id=?`, string(committed.ID),
			).Row().Scan(&sameTransaction); rowErr != nil {
				return rowErr
			}
			if !sameTransaction {
				t.Fatal("Audit transaction_id did not use the caller UnitOfWork transaction")
			}
			return nil
		}); err != nil {
			t.Fatalf("GORM scoped commit: %v", err)
		}
		if got, err := stores.gorm.Get(ctx, committed.ID); err != nil || got.ID != committed.ID {
			t.Fatalf("committed GORM scoped event=%#v err=%v", got, err)
		}
		assertGlobalRowCount(t, ctx, pool, committed.IdempotencyKey, 1)
		_, _, err := stores.gorm.AppendScoped(ctx, expiredScope, integrationEvent(t, "gorm-expired-scope"))
		assertAuditFoundationError(t, err, foundation.ErrorDependencyUnavailable, domain.ErrorCodeTransactionUnavailable)

		rolledBack := integrationEvent(t, "gorm-scoped-rollback")
		rollback := errors.New("force audit scoped rollback")
		err = stores.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			if _, replayed, appendErr := stores.gorm.AppendScoped(callbackCtx, scope, rolledBack); appendErr != nil || replayed {
				t.Fatalf("GORM rollback append replayed=%t err=%v", replayed, appendErr)
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("GORM scoped rollback error=%v", err)
		}
		_, err = stores.gorm.Get(ctx, rolledBack.ID)
		assertAuditFoundationError(t, err, foundation.ErrorNotFound, domain.ErrorCodeNotFound)
		assertGlobalRowCount(t, ctx, pool, rolledBack.IdempotencyKey, 0)
	})

	t.Run("cancellation append-only SQLSTATE and resources", func(t *testing.T) {
		cancelledContext, cancel := context.WithCancelCause(ctx)
		cancelCause := errors.New("audit integration cancellation")
		cancel(cancelCause)
		_, _, err := stores.gorm.Append(cancelledContext, integrationEvent(t, "gorm-cancelled"))
		assertAuditFoundationError(t, err, foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cancelCause) {
			t.Fatalf("GORM cancellation error lost context cause: %v", err)
		}

		deadlineContext, deadlineCancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
		defer deadlineCancel()
		_, _, err = stores.gorm.Append(deadlineContext, integrationEvent(t, "gorm-deadline"))
		assertAuditFoundationError(t, err, foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("GORM deadline error=%v", err)
		}

		event := integrationEvent(t, "gorm-append-only")
		created, _, err := stores.gorm.Append(ctx, event)
		if err != nil {
			t.Fatalf("GORM Append: %v", err)
		}
		for _, statement := range []string{
			`UPDATE ops.audit_event SET action='tampered' WHERE id=$1`,
			`DELETE FROM ops.audit_event WHERE id=$1`,
		} {
			_, err := pool.Exec(ctx, statement, string(created.ID))
			assertAuditPostgresCode(t, err, "55000")
		}
		if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
			t.Fatalf("GORM failure path leaked shared pool connections: acquired=%d", acquired)
		}
	})
}

type auditIntegrationStores struct {
	platform   *platformpostgres.Pool
	legacy     *Store
	gorm       *GORMStore
	unitOfWork foundation.UnitOfWork
}

func requireAuditIntegrationStores(t *testing.T) auditIntegrationStores {
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
	return auditIntegrationStores{platform: platform, legacy: legacy, gorm: gormStore, unitOfWork: unitOfWork}
}

func mustGORMAppendError(ctx context.Context, store *GORMStore, event domain.Event) error {
	_, _, err := store.Append(ctx, event)
	return err
}

func assertAuditFoundationError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if err == nil || !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error=%v classified=%#v, want kind=%s code=%s", err, classified, kind, code)
	}
}

func assertAuditPostgresCode(t *testing.T, err error, want string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if err == nil || !errors.As(err, &postgresError) || postgresError.Code != want {
		t.Fatalf("error=%v code=%q, want %s", err, postgresErrorCode(postgresError), want)
	}
}

func assertAuditListIndex(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin EXPLAIN transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatalf("disable seqscan for Audit EXPLAIN: %v", err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN (COSTS OFF)
		SELECT id FROM ops.audit_event
		WHERE workspace_id IS NOT DISTINCT FROM $1::uuid
		ORDER BY occurred_at DESC,id DESC
		LIMIT $2`, string(workspaceID), 2)
	if err != nil {
		t.Fatalf("Audit list EXPLAIN: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan Audit EXPLAIN: %v", err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Audit EXPLAIN rows: %v", err)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "idx_ops_audit_workspace_time") {
		t.Fatalf("Audit List did not use idx_ops_audit_workspace_time: %s", strings.Join(plan, "\n"))
	}
}

func integrationEvent(t *testing.T, suffix string) domain.Event {
	t.Helper()
	return domain.Event{
		ID:             integrationID(t),
		ActorType:      domain.ActorSystem,
		Action:         "security.check",
		Outcome:        domain.OutcomeSucceeded,
		IdempotencyKey: "audit-integration-" + suffix + "-" + string(integrationID(t)),
		Correlation:    []byte(`{"request_id":"request-integration"}`),
		Metadata:       []byte(`{"summary_hash":"sha256:safe"}`),
		SchemaVersion:  domain.SchemaVersion,
		OccurredAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
}

func integrationID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatalf("generate ID: %v", err)
	}
	return id
}

func integrationWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool) foundation.ID {
	t.Helper()
	id := integrationID(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	root := "/tmp/audit-integration-" + string(id)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,$4,'inactive',1,$4,$4)`, string(id), "audit-integration-"+string(id), root, now); err != nil {
		t.Fatalf("seed audit workspace: %v", err)
	}
	return id
}

func assertGlobalRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, key string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.audit_event WHERE workspace_id IS NULL AND idempotency_key=$1`, key).Scan(&got); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if got != want {
		t.Fatalf("audit rows=%d, want %d", got, want)
	}
}

func assertAuditRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, key string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.audit_event WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), key).Scan(&got); err != nil {
		t.Fatalf("count workspace audit rows: %v", err)
	}
	if got != want {
		t.Fatalf("workspace audit rows=%d, want %d", got, want)
	}
}

func postgresErrorCode(err *pgconn.PgError) string {
	if err == nil {
		return ""
	}
	return err.Code
}
