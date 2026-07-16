package workspacepostgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryWorkspaceAndSourceVersionLifecycle(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	workspace := domain.Workspace{
		ID:       mustID(t, "10000000-0000-4000-8000-000000000001"),
		Name:     "Integration",
		RootPath: "/tmp/zhixu-integration",
		Git: domain.GitBaseline{
			RepositoryPath: "/tmp/zhixu-integration",
			Branch:         "main",
			Head:           strings.Repeat("a", 40),
			CheckedAt:      now,
		},
		Status:    domain.WorkspaceStatusActive,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	persisted, err := repository.CreateWorkspace(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ID != workspace.ID || persisted.RootPath != workspace.RootPath {
		t.Fatalf("workspace = %#v", persisted)
	}

	registration := domain.SourceRegistration{
		Source: domain.Source{
			ID:               mustID(t, "20000000-0000-4000-8000-000000000001"),
			WorkspaceID:      workspace.ID,
			Type:             "markdown",
			LogicalName:      "guide",
			OriginalLocation: "sources/guide.md",
			CreatedAt:        now,
		},
		Version: domain.SourceVersion{
			ID:                      mustID(t, "30000000-0000-4000-8000-000000000001"),
			ContentHash:             strings.Repeat("a", 64),
			ByteSize:                12,
			MediaType:               "text/markdown",
			OriginalContentLocation: "sources/guide.md",
			SecurityStatus:          "pending",
			CapturedAt:              now,
		},
	}
	first, err := repository.RegisterSourceVersion(ctx, registration)
	if err != nil || !first.Created {
		t.Fatalf("first registration = %#v, error = %v", first, err)
	}

	registration.Source.ID = mustID(t, "20000000-0000-4000-8000-000000000002")
	registration.Version.ID = mustID(t, "30000000-0000-4000-8000-000000000002")
	reused, err := repository.RegisterSourceVersion(ctx, registration)
	if err != nil || reused.Created || reused.Source.ID != first.Source.ID || reused.Version.ID != first.Version.ID {
		t.Fatalf("reused registration = %#v, error = %v", reused, err)
	}

	registration.Version.ID = mustID(t, "30000000-0000-4000-8000-000000000003")
	registration.Version.ContentHash = strings.Repeat("b", 64)
	changed, err := repository.RegisterSourceVersion(ctx, registration)
	if err != nil || !changed.Created || changed.Source.ID != first.Source.ID || changed.Version.ID == first.Version.ID {
		t.Fatalf("changed registration = %#v, error = %v", changed, err)
	}

	if _, err := tx.Exec(ctx, `UPDATE core.source_version SET byte_size = byte_size + 1 WHERE id = $1`, string(first.Version.ID)); err == nil {
		t.Fatal("immutable source version update succeeded")
	}
}

func TestRepositoryDatabaseConstraints(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	base := domain.Workspace{
		ID:       mustID(t, "40000000-0000-4000-8000-000000000001"),
		Name:     "First",
		RootPath: "/tmp/zhixu-constraint-one",
		Git:      domain.GitBaseline{RepositoryPath: "/tmp/zhixu-constraint-one", CheckedAt: now},
		Status:   domain.WorkspaceStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := repository.CreateWorkspace(ctx, base); err != nil {
		t.Fatal(err)
	}
	base.ID = mustID(t, "40000000-0000-4000-8000-000000000002")
	base.Name = "Second"
	base.RootPath = "/tmp/zhixu-constraint-two"
	base.Git.RepositoryPath = base.RootPath
	_, err = repository.CreateWorkspace(ctx, base)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "ACTIVE_WORKSPACE_EXISTS" {
		t.Fatalf("active workspace error = %#v", err)
	}
}

func mustID(t *testing.T, value string) foundation.ID {
	t.Helper()
	id, err := foundation.ParseID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
