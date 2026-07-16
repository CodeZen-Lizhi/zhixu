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
		Artifact: domain.ContentArtifact{
			ID:              mustID(t, "25000000-0000-4000-8000-000000000001"),
			WorkspaceID:     workspace.ID,
			ContentHash:     strings.Repeat("a", 64),
			ByteSize:        12,
			ManagedLocation: ".knowledge/sources/" + strings.Repeat("a", 64),
			CreatedAt:       now,
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
	material, err := repository.GetSourceMaterial(ctx, first.Version.ID)
	if err != nil {
		t.Fatal(err)
	}
	if material.WorkspaceID != workspace.ID || material.WorkspaceRootPath != workspace.RootPath || material.SourceID != first.Source.ID || material.SourceVersion.ID != first.Version.ID || material.ContentArtifact.ID != first.Artifact.ID {
		t.Fatalf("source material = %#v", material)
	}

	registration.Source.ID = mustID(t, "20000000-0000-4000-8000-000000000002")
	registration.Artifact.ID = mustID(t, "25000000-0000-4000-8000-000000000002")
	registration.Version.ID = mustID(t, "30000000-0000-4000-8000-000000000002")
	reused, err := repository.RegisterSourceVersion(ctx, registration)
	if err != nil || reused.Created || reused.ArtifactCreated || reused.Source.ID != first.Source.ID || reused.Artifact.ID != first.Artifact.ID || reused.Version.ID != first.Version.ID {
		t.Fatalf("reused registration = %#v, error = %v", reused, err)
	}

	registration.Version.ID = mustID(t, "30000000-0000-4000-8000-000000000003")
	registration.Version.ContentHash = strings.Repeat("b", 64)
	registration.Artifact.ID = mustID(t, "25000000-0000-4000-8000-000000000003")
	registration.Artifact.ContentHash = strings.Repeat("b", 64)
	registration.Artifact.ManagedLocation = ".knowledge/sources/" + strings.Repeat("b", 64)
	changed, err := repository.RegisterSourceVersion(ctx, registration)
	if err != nil || !changed.Created || !changed.ArtifactCreated || changed.Source.ID != first.Source.ID || changed.Version.ID == first.Version.ID || changed.Artifact.ID == first.Artifact.ID {
		t.Fatalf("changed registration = %#v, error = %v", changed, err)
	}

	registration.Source.ID = mustID(t, "20000000-0000-4000-8000-000000000004")
	registration.Source.OriginalLocation = "sources/copy.md"
	registration.Source.LogicalName = "copy"
	registration.Artifact.ID = mustID(t, "25000000-0000-4000-8000-000000000004")
	registration.Version.ID = mustID(t, "30000000-0000-4000-8000-000000000004")
	registration.Version.OriginalContentLocation = "sources/copy.md"
	copyResult, err := repository.RegisterSourceVersion(ctx, registration)
	if err != nil || !copyResult.Created || copyResult.ArtifactCreated || copyResult.Source.ID == first.Source.ID || copyResult.Artifact.ID != changed.Artifact.ID {
		t.Fatalf("same-content different-source registration = %#v, error = %v", copyResult, err)
	}

	if _, err := tx.Exec(ctx, `UPDATE core.source_version SET byte_size = byte_size + 1 WHERE id = $1`, string(first.Version.ID)); err == nil {
		t.Fatal("immutable source version update succeeded")
	}
	if _, err := tx.Exec(ctx, `UPDATE core.content_artifact SET byte_size = byte_size + 1 WHERE id = $1`, string(first.Artifact.ID)); err == nil {
		t.Fatal("immutable content artifact update succeeded")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO core.source_version (
			id, source_id, content_artifact_id, content_hash, byte_size, mime_type,
			original_content_location, security_status, captured_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		"35000000-0000-4000-8000-000000000001", string(first.Source.ID), string(first.Artifact.ID), strings.Repeat("c", 64), int64(12), "text/markdown", "sources/mismatch.md", "pending", now); err == nil {
		t.Fatal("source version accepted mismatched artifact metadata")
	}
}

func TestRepositoryGetSourceMaterialRejectsLegacyAndCrossScopeRows(t *testing.T) {
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
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	workspaceID := "51000000-0000-4000-8000-000000000001"
	otherWorkspaceID := "51000000-0000-4000-8000-000000000002"
	sourceID := "52000000-0000-4000-8000-000000000001"
	otherArtifactID := "53000000-0000-4000-8000-000000000001"
	legacyVersionID := "54000000-0000-4000-8000-000000000001"
	crossScopeVersionID := "54000000-0000-4000-8000-000000000002"
	for _, statement := range []string{
		`ALTER TABLE core.source_version DISABLE TRIGGER source_version_verify_artifact_workspace`,
		`ALTER TABLE core.source_version DROP CONSTRAINT source_version_content_artifact_required`,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO core.workspace (
			id,name,root_path,git_repository_path,git_branch,git_head,git_dirty,git_checked_at,status,version,created_at,updated_at
		) VALUES
			($1,'Material','/tmp/material-one','/tmp/material-one','','',false,$3,'active',1,$3,$3),
			($2,'Other','/tmp/material-two','/tmp/material-two','','',false,$3,'inactive',1,$3,$3)`, workspaceID, otherWorkspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO core.source (id,workspace_id,type,logical_name,original_location,created_at)
		VALUES ($1,$2,'markdown','legacy','legacy.md',$3)`, sourceID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	legacyHash := strings.Repeat("d", 64)
	crossScopeHash := strings.Repeat("e", 64)
	if _, err := tx.Exec(ctx, `
		INSERT INTO core.content_artifact (id,workspace_id,content_hash,byte_size,managed_location,created_at)
		VALUES ($1,$2,$3,7,$4,$5)`, otherArtifactID, otherWorkspaceID, crossScopeHash, ".knowledge/sources/"+crossScopeHash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO core.source_version (
			id,source_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at
		) VALUES
			($1,$3,NULL,$5,7,'text/markdown','legacy.md','pending',$7),
			($2,$3,$4,$6,7,'text/markdown','legacy.md','pending',$7)`, legacyVersionID, crossScopeVersionID, sourceID, otherArtifactID, legacyHash, crossScopeHash, now); err != nil {
		t.Fatal(err)
	}
	_, err = repository.GetSourceMaterial(ctx, mustID(t, legacyVersionID))
	requireRepositoryErrorCode(t, err, "SOURCE_VERSION_ARTIFACT_MISSING")
	_, err = repository.GetSourceMaterial(ctx, mustID(t, crossScopeVersionID))
	requireRepositoryErrorCode(t, err, "SOURCE_MATERIAL_SCOPE_INVALID")
	_, err = repository.GetSourceMaterial(ctx, mustID(t, "54000000-0000-4000-8000-000000000099"))
	requireRepositoryErrorCode(t, err, "SOURCE_VERSION_NOT_FOUND")
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

func requireRepositoryErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error = %#v, want code %q", err, code)
	}
}
