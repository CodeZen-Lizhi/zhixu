//go:build integration && testcontainers

package main

import (
	"context"
	"os"
	"testing"
	"time"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
)

func TestAuditCLIReadsIsolatedPostgresWithReadOnlyConnections(t *testing.T) {
	fixture := testdb.Require(t, testdb.Config{
		ExternalAdminURL: os.Getenv("ZHIXU_TEST_DATABASE_URL"), Availability: testdb.FailWhenUnavailable,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := fixture.Pool()
	database, err := pool.GORM()
	if err != nil {
		t.Fatal("test database is unavailable")
	}
	workspace := testWorkspace
	otherWorkspace := foundation.ID("22222222-2222-4222-8222-222222222222")
	for _, id := range []foundation.ID{workspace, otherWorkspace} {
		result := database.WithContext(ctx).Exec(`INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES(?,?,?,?,?,'inactive',1,?,?)`, string(id), "audit-cli-fixture",
			"/tmp/audit-cli-"+string(id), "/tmp/audit-cli-"+string(id), time.Now().UTC(), time.Now().UTC(), time.Now().UTC())
		if result.Error != nil {
			t.Fatal("could not seed isolated Workspace")
		}
	}
	store, err := auditpostgres.NewGORMStore(pool)
	if err != nil {
		t.Fatal("could not construct isolated Audit Store")
	}
	for _, event := range []domain.Event{
		testEvent(t, 1, &workspace), testEvent(t, 2, &workspace), testEvent(t, 3, &workspace),
		testEvent(t, 4, &otherWorkspace), testEvent(t, 5, nil),
	} {
		if _, _, err := store.Append(ctx, event); err != nil {
			t.Fatal("could not seed isolated Audit history")
		}
	}

	// The factory owns this disposable database. Its private URL is never printed
	// or passed in argv; the production non-API configuration loader consumes it.
	t.Setenv("ZHIXU_DATABASE_URL", pool.DB().Config().ConnString())
	t.Setenv("ZHIXU_AUTH_BOOTSTRAP_TOKEN", "not-a-valid-bootstrap-token")
	t.Setenv("ZHIXU_REVIEW_QUESTION_REF_KEY", "not-a-valid-api-key")
	first := runPage(t, []string{"--workspace", string(workspace), "--limit", "2"}, openAuditReader)
	if len(first.Items) != 2 || first.Items[0].ID != testEvent(t, 3, &workspace).ID || first.Items[1].ID != testEvent(t, 2, &workspace).ID || first.NextCursor == nil {
		t.Fatal("first PostgreSQL page is not in stable descending order")
	}
	second := runPage(t, []string{
		"--workspace", string(workspace), "--limit", "2",
		"--before", first.NextCursor.Before.Format(time.RFC3339Nano), "--before-id", string(first.NextCursor.BeforeID),
	}, openAuditReader)
	if len(second.Items) != 1 || second.Items[0].ID != testEvent(t, 1, &workspace).ID || second.NextCursor != nil {
		t.Fatal("same-microsecond events were lost or duplicated during pagination")
	}
	global := runPage(t, []string{"--global"}, openAuditReader)
	if len(global.Items) != 1 || global.Items[0].ID != testEvent(t, 5, nil).ID || global.WorkspaceID != nil {
		t.Fatal("global query included Workspace history")
	}
	other := runPage(t, []string{"--workspace", string(otherWorkspace)}, openAuditReader)
	if len(other.Items) != 1 || other.Items[0].ID != testEvent(t, 4, &otherWorkspace).ID {
		t.Fatal("Workspace query crossed its scope")
	}
	reader, closeReader, err := openAuditReader(ctx, "")
	if err != nil {
		t.Fatal("could not open production Audit reader")
	}
	defer closeReader()
	readOnlyStore, ok := reader.(*auditpostgres.GORMStore)
	if !ok {
		t.Fatal("production reader is not the shared Audit Store")
	}
	if _, _, err := readOnlyStore.Append(ctx, testEvent(t, 6, nil)); err == nil {
		t.Fatal("Audit CLI connection accepted a write")
	}
	var count int64
	if err := database.WithContext(ctx).Raw(`SELECT count(*) FROM ops.audit_event`).Scan(&count).Error; err != nil || count != 5 {
		t.Fatal("read-only queries changed Audit history")
	}
}
