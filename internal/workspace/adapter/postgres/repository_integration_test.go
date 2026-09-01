//go:build integration

package workspacepostgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

type workspaceIntegrationRepository interface {
	domain.Repository
	domain.ActiveWorkspaceRepository
	domain.SourceMaterialRepository
	domain.SourceVersionListRepository
	domain.GitCaptureRepository
}

type workspaceIntegrationVariant struct {
	name string
	open func(*testing.T, *platformpostgres.Pool) workspaceIntegrationRepository
}

func runWorkspaceIntegrationVariants(t *testing.T, test func(*testing.T, *platformpostgres.Pool, context.Context, workspaceIntegrationRepository)) {
	t.Helper()
	variants := []workspaceIntegrationVariant{
		{name: "legacy", open: openLegacyWorkspaceIntegrationRepository},
		{name: "gorm", open: openGORMWorkspaceIntegrationRepository},
	}
	for _, variant := range variants {
		variant := variant
		t.Run(variant.name, func(t *testing.T) {
			fixture := testdb.Require(t, testdb.Config{
				ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
				Availability:     testdb.FailWhenUnavailable,
				MaxConns:         16,
			})
			platform := fixture.Pool()
			if platform == nil || platform.DB() == nil {
				t.Fatal("PostgreSQL fixture did not provide a shared platform pool")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			test(t, platform, ctx, variant.open(t, platform))
		})
	}
}

func openLegacyWorkspaceIntegrationRepository(t *testing.T, platform *platformpostgres.Pool) workspaceIntegrationRepository {
	t.Helper()
	repository, err := workspacepostgres.NewRepository(platform.DB())
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func openGORMWorkspaceIntegrationRepository(t *testing.T, platform *platformpostgres.Pool) workspaceIntegrationRepository {
	t.Helper()
	repository, err := workspacepostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func TestRepositoryWorkspaceAndSourceVersionLifecycle(t *testing.T) {
	runWorkspaceIntegrationVariants(t, testRepositoryWorkspaceAndSourceVersionLifecycle)
}

func testRepositoryWorkspaceAndSourceVersionLifecycle(t *testing.T, platform *platformpostgres.Pool, ctx context.Context, repository workspaceIntegrationRepository) {
	t.Helper()
	pool := platform.DB()

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
	active, err := repository.GetActiveWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != workspace.ID || active.Status != domain.WorkspaceStatusActive {
		t.Fatalf("active workspace = %#v", active)
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

	if _, err := pool.Exec(ctx, `UPDATE core.source_version SET byte_size = byte_size + 1 WHERE id = $1`, string(first.Version.ID)); err == nil {
		t.Fatal("immutable source version update succeeded")
	}
	if _, err := pool.Exec(ctx, `UPDATE core.content_artifact SET byte_size = byte_size + 1 WHERE id = $1`, string(first.Artifact.ID)); err == nil {
		t.Fatal("immutable content artifact update succeeded")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.source_version (
			id, source_id, workspace_id, content_artifact_id, content_hash, byte_size, mime_type,
			original_content_location, security_status, captured_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		"35000000-0000-4000-8000-000000000001", string(first.Source.ID), string(first.Source.WorkspaceID), string(first.Artifact.ID), strings.Repeat("c", 64), int64(12), "text/markdown", "sources/mismatch.md", "pending", now); err == nil {
		t.Fatal("source version accepted mismatched artifact metadata")
	}
}

func TestRepositoryGetSourceMaterialRejectsLegacyAndCrossScopeRows(t *testing.T) {
	runWorkspaceIntegrationVariants(t, testRepositoryGetSourceMaterialRejectsLegacyAndCrossScopeRows)
}

func testRepositoryGetSourceMaterialRejectsLegacyAndCrossScopeRows(t *testing.T, platform *platformpostgres.Pool, ctx context.Context, repository workspaceIntegrationRepository) {
	t.Helper()
	pool := platform.DB()
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
		`ALTER TABLE core.source_version DROP CONSTRAINT fk_source_version_artifact_workspace`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace (
			id,name,root_path,git_repository_path,git_branch,git_head,git_dirty,git_checked_at,status,version,created_at,updated_at
		) VALUES
			($1,'Material','/tmp/material-one','/tmp/material-one','','',false,$3,'active',1,$3,$3),
			($2,'Other','/tmp/material-two','/tmp/material-two','','',false,$3,'inactive',1,$3,$3)`, workspaceID, otherWorkspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.source (id,workspace_id,type,logical_name,original_location,created_at)
		VALUES ($1,$2,'markdown','legacy','legacy.md',$3)`, sourceID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	legacyHash := strings.Repeat("d", 64)
	crossScopeHash := strings.Repeat("e", 64)
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.content_artifact (id,workspace_id,content_hash,byte_size,managed_location,created_at)
		VALUES ($1,$2,$3,7,$4,$5)`, otherArtifactID, otherWorkspaceID, crossScopeHash, ".knowledge/sources/"+crossScopeHash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.source_version (
			id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at
		) VALUES
			($1,$3,$8,NULL,$5,7,'text/markdown','legacy.md','pending',$7),
			($2,$3,$8,$4,$6,7,'text/markdown','legacy.md','pending',$7)`, legacyVersionID, crossScopeVersionID, sourceID, otherArtifactID, legacyHash, crossScopeHash, now, workspaceID); err != nil {
		t.Fatal(err)
	}
	_, err := repository.GetSourceMaterial(ctx, mustID(t, legacyVersionID))
	requireRepositoryErrorCode(t, err, "SOURCE_VERSION_ARTIFACT_MISSING")
	_, err = repository.GetSourceMaterial(ctx, mustID(t, crossScopeVersionID))
	requireRepositoryErrorCode(t, err, "SOURCE_MATERIAL_SCOPE_INVALID")
	_, err = repository.GetSourceMaterial(ctx, mustID(t, "54000000-0000-4000-8000-000000000099"))
	requireRepositoryErrorCode(t, err, "SOURCE_VERSION_NOT_FOUND")
}

func TestRepositoryDatabaseConstraints(t *testing.T) {
	runWorkspaceIntegrationVariants(t, testRepositoryDatabaseConstraints)
}

func testRepositoryDatabaseConstraints(t *testing.T, platform *platformpostgres.Pool, ctx context.Context, repository workspaceIntegrationRepository) {
	t.Helper()
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
	_, err := repository.CreateWorkspace(ctx, base)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "ACTIVE_WORKSPACE_EXISTS" {
		t.Fatalf("active workspace error = %#v", err)
	}
}

func TestRepositorySourceRegistrationConflictsAndBatchRollback(t *testing.T) {
	runWorkspaceIntegrationVariants(t, testRepositorySourceRegistrationConflictsAndBatchRollback)
}

func testRepositorySourceRegistrationConflictsAndBatchRollback(t *testing.T, platform *platformpostgres.Pool, ctx context.Context, repository workspaceIntegrationRepository) {
	t.Helper()
	workspaceID := mustID(t, "61000000-0000-4000-8000-000000000001")
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	seedWorkspaceForSourceRegistration(t, ctx, repository, workspaceID, "/tmp/source-registration", now)

	registered := workspaceSourceRegistration(workspaceID, "62000000", "sources/conflict.md", strings.Repeat("a", 64), now)
	if result, err := repository.RegisterSourceVersion(ctx, registered); err != nil || !result.Created || !result.ArtifactCreated {
		t.Fatalf("initial registration = %#v, error = %v", result, err)
	}

	sourceConflict := registered
	sourceConflict.Source.ID = mustID(t, "62100000-0000-4000-8000-000000000001")
	sourceConflict.Source.Type = "html"
	_, err := repository.RegisterSourceVersion(ctx, sourceConflict)
	requireRepositoryErrorCode(t, err, "SOURCE_METADATA_CONFLICT")

	artifactConflict := registered
	artifactConflict.Source.ID = mustID(t, "62200000-0000-4000-8000-000000000001")
	artifactConflict.Artifact.ID = mustID(t, "62200000-0000-4000-8000-000000000002")
	artifactConflict.Artifact.ByteSize++
	artifactConflict.Version.ID = mustID(t, "62200000-0000-4000-8000-000000000003")
	_, err = repository.RegisterSourceVersion(ctx, artifactConflict)
	requireRepositoryErrorCode(t, err, "CONTENT_ARTIFACT_METADATA_CONFLICT")

	versionConflict := registered
	versionConflict.Source.ID = mustID(t, "62300000-0000-4000-8000-000000000001")
	versionConflict.Artifact.ID = mustID(t, "62300000-0000-4000-8000-000000000002")
	versionConflict.Version.ID = mustID(t, "62300000-0000-4000-8000-000000000003")
	versionConflict.Version.MediaType = "text/plain"
	_, err = repository.RegisterSourceVersion(ctx, versionConflict)
	requireRepositoryErrorCode(t, err, "SOURCE_VERSION_METADATA_CONFLICT")

	batchFirst := workspaceSourceRegistration(workspaceID, "63000000", "sources/batch-first.md", strings.Repeat("b", 64), now.Add(time.Minute))
	missingWorkspaceID := mustID(t, "61000000-0000-4000-8000-000000000099")
	batchSecond := workspaceSourceRegistration(missingWorkspaceID, "64000000", "sources/batch-second.md", strings.Repeat("c", 64), now.Add(2*time.Minute))
	results, err := repository.RegisterSourceVersions(ctx, []domain.SourceRegistration{batchFirst, batchSecond})
	if len(results) != 0 {
		t.Fatalf("failed batch returned partial results = %#v", results)
	}
	requireRepositoryErrorCode(t, err, "WORKSPACE_REFERENCE_INVALID")
	assertSourceRegistrationAbsent(t, ctx, platform, batchFirst)
}

func TestGORMScopedSourceWriterUsesCallerOwnedUnitOfWork(t *testing.T) {
	fixture := testdb.Require(t, testdb.Config{
		ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
		Availability:     testdb.FailWhenUnavailable,
		MaxConns:         16,
	})
	platform := fixture.Pool()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	repository, err := workspacepostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	writer := workspaceapplication.ScopedSourceWriter(repository)
	uow, err := platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := mustID(t, "65000000-0000-4000-8000-000000000001")
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	seedWorkspaceForSourceRegistration(t, ctx, repository, workspaceID, "/tmp/scoped-source-writer", now)

	committed := workspaceSourceRegistration(workspaceID, "66000000", "sources/committed.md", strings.Repeat("d", 64), now)
	var committedScope foundation.TransactionScope
	err = uow.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		committedScope = scope
		result, writeErr := writer.RegisterSourceVersionScoped(callbackCtx, scope, committed)
		if writeErr != nil {
			return writeErr
		}
		if !result.Created || !result.ArtifactCreated {
			t.Fatalf("scoped committed registration = %#v", result)
		}
		transaction, unwrapErr := platformpostgres.SQLTransaction(scope)
		if unwrapErr != nil {
			return unwrapErr
		}
		var transactionCount int
		if scanErr := transaction.QueryRowContext(callbackCtx, `SELECT count(*) FROM core.source_version WHERE id=$1`, string(committed.Version.ID)).Scan(&transactionCount); scanErr != nil {
			return scanErr
		}
		if transactionCount != 1 {
			t.Fatalf("caller transaction cannot see scoped write: count=%d", transactionCount)
		}
		var rootCount int
		if scanErr := platform.DB().QueryRow(callbackCtx, `SELECT count(*) FROM core.source_version WHERE id=$1`, string(committed.Version.ID)).Scan(&rootCount); scanErr != nil {
			return scanErr
		}
		if rootCount != 0 {
			t.Fatalf("scoped writer committed caller transaction: root count=%d", rootCount)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSourceRegistrationPresent(t, ctx, platform, committed)

	rollbackCause := errors.New("rollback scoped source registration")
	rolledBack := workspaceSourceRegistration(workspaceID, "67000000", "sources/rolled-back.md", strings.Repeat("e", 64), now.Add(time.Minute))
	err = uow.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		if _, writeErr := writer.RegisterSourceVersionScoped(callbackCtx, scope, rolledBack); writeErr != nil {
			return writeErr
		}
		return rollbackCause
	})
	if !errors.Is(err, rollbackCause) {
		t.Fatalf("scoped rollback error = %v", err)
	}
	assertSourceRegistrationAbsent(t, ctx, platform, rolledBack)

	stale := workspaceSourceRegistration(workspaceID, "68000000", "sources/stale.md", strings.Repeat("f", 64), now.Add(2*time.Minute))
	_, err = writer.RegisterSourceVersionScoped(ctx, committedScope, stale)
	requireRepositoryErrorCode(t, err, "WORKSPACE_DATABASE_UNAVAILABLE")
	_, err = writer.RegisterSourceVersionScoped(ctx, invalidWorkspaceTransactionScope{}, stale)
	requireRepositoryErrorCode(t, err, "WORKSPACE_DATABASE_UNAVAILABLE")
	_, err = writer.RegisterSourceVersionScoped(ctx, nil, stale)
	requireRepositoryErrorCode(t, err, "WORKSPACE_DATABASE_UNAVAILABLE")
	assertSourceRegistrationAbsent(t, ctx, platform, stale)
	requireWorkspacePoolReleased(t, platform)
}

func TestRepositoryPreservesContextCauseAndReleasesConnections(t *testing.T) {
	runWorkspaceIntegrationVariants(t, testRepositoryPreservesContextCauseAndReleasesConnections)
}

func testRepositoryPreservesContextCauseAndReleasesConnections(t *testing.T, platform *platformpostgres.Pool, ctx context.Context, repository workspaceIntegrationRepository) {
	t.Helper()
	workspaceID := mustID(t, "69000000-0000-4000-8000-000000000001")
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	seedWorkspaceForSourceRegistration(t, ctx, repository, workspaceID, "/tmp/workspace-context", now)

	deadlineCause := errors.New("workspace caller deadline budget expired")
	deadlineCtx, deadlineCancel := context.WithDeadlineCause(ctx, time.Unix(0, 0), deadlineCause)
	defer deadlineCancel()
	roots, err := repository.ListWorkspaceRoots(deadlineCtx)
	if err == nil || len(roots) != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline ListWorkspaceRoots() roots=%#v error=%v", roots, err)
	}
	if _, isGORM := repository.(*workspacepostgres.GORMRepository); isGORM && !errors.Is(err, deadlineCause) {
		t.Fatalf("GORM deadline error lost caller cause: %v", err)
	}

	blocker, err := platform.DB().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err := blocker.Exec(ctx, `LOCK TABLE core.source IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}

	registration := workspaceSourceRegistration(workspaceID, "69100000", "sources/canceled.md", strings.Repeat("1", 64), now)
	cancelCause := errors.New("workspace caller stopped waiting")
	canceledCtx, cancel := context.WithCancelCause(ctx)
	errorCh := make(chan error, 1)
	go func() {
		_, registerErr := repository.RegisterSourceVersions(canceledCtx, []domain.SourceRegistration{registration})
		errorCh <- registerErr
	}()
	deadline := time.Now().Add(5 * time.Second)
	for platform.DB().Stat().AcquiredConns() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired := platform.DB().Stat().AcquiredConns(); acquired < 2 {
		cancel(cancelCause)
		t.Fatalf("blocked Workspace registration acquired connections=%d want at least 2", acquired)
	}
	time.Sleep(50 * time.Millisecond)
	cancel(cancelCause)
	select {
	case err = <-errorCh:
	case <-time.After(5 * time.Second):
		_ = blocker.Rollback(context.Background())
		t.Fatal("blocked Workspace registration did not stop after cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled RegisterSourceVersions() error=%v", err)
	}
	if _, isGORM := repository.(*workspacepostgres.GORMRepository); isGORM && !errors.Is(err, cancelCause) {
		t.Fatalf("GORM canceled error lost caller cause: %v", err)
	}
	if err := blocker.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSourceRegistrationAbsent(t, ctx, platform, registration)
	var one int
	if err := platform.DB().QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("pool unusable after canceled Workspace transaction value=%d error=%v", one, err)
	}
	requireWorkspacePoolReleased(t, platform)
}

type invalidWorkspaceTransactionScope struct{}

func (invalidWorkspaceTransactionScope) TransactionScope() {}

func seedWorkspaceForSourceRegistration(t *testing.T, ctx context.Context, repository domain.Repository, workspaceID foundation.ID, root string, now time.Time) {
	t.Helper()
	_, err := repository.CreateWorkspace(ctx, domain.Workspace{
		ID: workspaceID, Name: "Source registration", RootPath: root,
		Git:    domain.GitBaseline{RepositoryPath: root, CheckedAt: now},
		Status: domain.WorkspaceStatusInactive, Version: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func workspaceSourceRegistration(workspaceID foundation.ID, prefix, location, contentHash string, now time.Time) domain.SourceRegistration {
	return domain.SourceRegistration{
		Source: domain.Source{
			ID: foundation.ID(prefix + "-0000-4000-8000-000000000001"), WorkspaceID: workspaceID,
			Type: "markdown", LogicalName: location, OriginalLocation: location, CreatedAt: now,
		},
		Artifact: domain.ContentArtifact{
			ID: foundation.ID(prefix + "-0000-4000-8000-000000000002"), WorkspaceID: workspaceID,
			ContentHash: contentHash, ByteSize: 12, ManagedLocation: ".knowledge/sources/" + contentHash, CreatedAt: now,
		},
		Version: domain.SourceVersion{
			ID: foundation.ID(prefix + "-0000-4000-8000-000000000003"), ContentHash: contentHash,
			ByteSize: 12, MediaType: "text/markdown", OriginalContentLocation: location,
			SecurityStatus: "pending", CapturedAt: now,
		},
	}
}

func assertSourceRegistrationPresent(t *testing.T, ctx context.Context, platform *platformpostgres.Pool, registration domain.SourceRegistration) {
	t.Helper()
	var count int
	err := platform.DB().QueryRow(ctx, `SELECT
		(SELECT count(*) FROM core.source WHERE id=$1) +
		(SELECT count(*) FROM core.content_artifact WHERE id=$2) +
		(SELECT count(*) FROM core.source_version WHERE id=$3)`,
		string(registration.Source.ID), string(registration.Artifact.ID), string(registration.Version.ID)).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("source registration persisted row count=%d want=3", count)
	}
}

func assertSourceRegistrationAbsent(t *testing.T, ctx context.Context, platform *platformpostgres.Pool, registration domain.SourceRegistration) {
	t.Helper()
	var count int
	err := platform.DB().QueryRow(ctx, `SELECT
		(SELECT count(*) FROM core.source WHERE id=$1) +
		(SELECT count(*) FROM core.content_artifact WHERE id=$2) +
		(SELECT count(*) FROM core.source_version WHERE id=$3)`,
		string(registration.Source.ID), string(registration.Artifact.ID), string(registration.Version.ID)).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("source registration leaked %d rows", count)
	}
}

func requireWorkspacePoolReleased(t *testing.T, platform *platformpostgres.Pool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for platform.DB().Stat().AcquiredConns() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired := platform.DB().Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("shared Workspace pool acquired connections=%d want=0", acquired)
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
