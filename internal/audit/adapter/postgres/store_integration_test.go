//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuditStorePostgresAppendOnlyAndIdempotency(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

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
	) VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, string(id), "audit-integration-"+string(id), root, now); err != nil {
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

func postgresErrorCode(err *pgconn.PgError) string {
	if err == nil {
		return ""
	}
	return err.Code
}
