package workspacepostgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"gorm.io/gorm"
)

// RegisterSourceVersion reuses a Source by stable location and a SourceVersion
// by source/content hash. New content at the same location creates a new version.
func (repository *GORMRepository) RegisterSourceVersion(ctx context.Context, registration domain.SourceRegistration) (domain.SourceRegistrationResult, error) {
	results, err := repository.RegisterSourceVersions(ctx, []domain.SourceRegistration{registration})
	if err != nil {
		return domain.SourceRegistrationResult{}, err
	}
	return results[0], nil
}

// RegisterSourceVersions idempotently registers a batch in one UnitOfWork.
func (repository *GORMRepository) RegisterSourceVersions(ctx context.Context, registrations []domain.SourceRegistration) ([]domain.SourceRegistrationResult, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if len(registrations) == 0 {
		return []domain.SourceRegistrationResult{}, nil
	}
	results := make([]domain.SourceRegistrationResult, 0, len(registrations))
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		for _, registration := range registrations {
			result, err := repository.registerSourceVersionGORM(callbackCtx, transaction, registration)
			if err != nil {
				return err
			}
			results = append(results, result)
		}
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		code := "SOURCE_VERSION_TRANSACTION_FAILED"
		if callbackSucceeded {
			code = "SOURCE_VERSION_COMMIT_FAILED"
		}
		return nil, classifyGORMWorkspace(ctx, err, code)
	}
	return results, nil
}

// RegisterSourceScoped creates or exactly replays a Source in the supplied
// caller-owned platform transaction. It never commits or rolls it back.
func (repository *GORMRepository) RegisterSourceScoped(ctx context.Context, scope foundation.TransactionScope, source domain.Source) (domain.Source, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Source{}, err
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return domain.Source{}, gormWorkspaceUnavailable(errors.New("workspace scoped transaction is unavailable"))
	}
	persisted, err := gormInsertOrGetSource(ctx, transaction.WithContext(ctx), source)
	if err != nil {
		return domain.Source{}, classifyGORMWorkspace(ctx, err, "SOURCE_REGISTER_FAILED")
	}
	return persisted, nil
}

// RegisterSourceVersionScoped atomically registers Source, Artifact and Source
// Version in the supplied caller-owned platform transaction.
func (repository *GORMRepository) RegisterSourceVersionScoped(ctx context.Context, scope foundation.TransactionScope, registration domain.SourceRegistration) (domain.SourceRegistrationResult, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SourceRegistrationResult{}, err
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return domain.SourceRegistrationResult{}, gormWorkspaceUnavailable(errors.New("workspace scoped transaction is unavailable"))
	}
	return repository.registerSourceVersionGORM(ctx, transaction.WithContext(ctx), registration)
}

func (repository *GORMRepository) registerSourceVersionGORM(ctx context.Context, transaction *gorm.DB, registration domain.SourceRegistration) (domain.SourceRegistrationResult, error) {
	source, err := gormInsertOrGetSource(ctx, transaction, registration.Source)
	if err != nil {
		return domain.SourceRegistrationResult{}, classifyGORMWorkspace(ctx, err, "SOURCE_REGISTER_FAILED")
	}
	artifact, artifactCreated, err := gormInsertOrGetContentArtifact(ctx, transaction, registration.Artifact)
	if err != nil {
		return domain.SourceRegistrationResult{}, classifyGORMWorkspace(ctx, err, "CONTENT_ARTIFACT_REGISTER_FAILED")
	}
	registration.Version.SourceID = source.ID
	registration.Version.ContentArtifactID = artifact.ID
	version, created, err := gormInsertOrGetSourceVersion(ctx, transaction, source.WorkspaceID, registration.Version)
	if err != nil {
		return domain.SourceRegistrationResult{}, classifyGORMWorkspace(ctx, err, "SOURCE_VERSION_REGISTER_FAILED")
	}
	return domain.SourceRegistrationResult{Source: source, Artifact: artifact, Version: version, ArtifactCreated: artifactCreated, Created: created}, nil
}

func gormInsertOrGetContentArtifact(ctx context.Context, transaction *gorm.DB, artifact domain.ContentArtifact) (domain.ContentArtifact, bool, error) {
	row, err := gormWorkspaceRawRow(transaction.WithContext(ctx), `INSERT INTO core.content_artifact (id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES (?,?,?,?,?,?) ON CONFLICT (workspace_id,content_hash) DO NOTHING RETURNING id::text,workspace_id::text,content_hash,byte_size,managed_location,created_at`, string(artifact.ID), string(artifact.WorkspaceID), artifact.ContentHash, artifact.ByteSize, artifact.ManagedLocation, artifact.CreatedAt.UTC())
	if err != nil {
		return domain.ContentArtifact{}, false, err
	}
	persisted, err := scanContentArtifact(row)
	if err == nil {
		return persisted, true, nil
	}
	if !gormWorkspaceNoRows(err) {
		return domain.ContentArtifact{}, false, err
	}
	row, err = gormWorkspaceRawRow(transaction.WithContext(ctx), `SELECT id::text,workspace_id::text,content_hash,byte_size,managed_location,created_at FROM core.content_artifact WHERE workspace_id=? AND content_hash=?`, string(artifact.WorkspaceID), artifact.ContentHash)
	if err != nil {
		return domain.ContentArtifact{}, false, err
	}
	persisted, err = scanContentArtifact(row)
	if err != nil {
		return domain.ContentArtifact{}, false, err
	}
	if persisted.ByteSize != artifact.ByteSize || persisted.ManagedLocation != artifact.ManagedLocation {
		return domain.ContentArtifact{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "CONTENT_ARTIFACT_METADATA_CONFLICT", false, errors.New("content artifact metadata does not match existing hash"))
	}
	return persisted, false, nil
}

func gormInsertOrGetSource(ctx context.Context, transaction *gorm.DB, source domain.Source) (domain.Source, error) {
	row, err := gormWorkspaceRawRow(transaction.WithContext(ctx), `INSERT INTO core.source (id,workspace_id,type,logical_name,original_location,created_at) VALUES (?,?,?,?,?,?) ON CONFLICT (workspace_id,original_location) DO UPDATE SET removed_at=NULL WHERE core.source.removed_at IS NOT NULL RETURNING id::text,workspace_id::text,type,logical_name,original_location,created_at`, string(source.ID), string(source.WorkspaceID), source.Type, source.LogicalName, source.OriginalLocation, source.CreatedAt.UTC())
	if err != nil {
		return domain.Source{}, err
	}
	persisted, err := scanSource(row)
	if err == nil {
		return persisted, nil
	}
	if !gormWorkspaceNoRows(err) {
		return domain.Source{}, err
	}
	row, err = gormWorkspaceRawRow(transaction.WithContext(ctx), `SELECT id::text,workspace_id::text,type,logical_name,original_location,created_at FROM core.source WHERE workspace_id=? AND original_location=?`, string(source.WorkspaceID), source.OriginalLocation)
	if err != nil {
		return domain.Source{}, err
	}
	persisted, err = scanSource(row)
	if err == nil && (persisted.Type != source.Type || persisted.LogicalName != source.LogicalName) {
		return domain.Source{}, foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_METADATA_CONFLICT", false, errors.New("existing source metadata differs from the requested registration"))
	}
	return persisted, err
}

func gormInsertOrGetSourceVersion(ctx context.Context, transaction *gorm.DB, workspaceID foundation.ID, version domain.SourceVersion) (domain.SourceVersion, bool, error) {
	row, err := gormWorkspaceRawRow(transaction.WithContext(ctx), `INSERT INTO core.source_version (id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,parser_version,captured_at) VALUES (?,?,?,?,?,?,?,?,?,NULLIF(?,''),?) ON CONFLICT (source_id,content_hash) DO NOTHING RETURNING id::text,source_id::text,content_artifact_id::text,content_hash,byte_size,mime_type,original_content_location,security_status,COALESCE(parser_version,''),captured_at`, string(version.ID), string(version.SourceID), string(workspaceID), string(version.ContentArtifactID), version.ContentHash, version.ByteSize, version.MediaType, version.OriginalContentLocation, version.SecurityStatus, version.ParserVersion, version.CapturedAt.UTC())
	if err != nil {
		return domain.SourceVersion{}, false, err
	}
	persisted, err := scanSourceVersion(row)
	if err == nil {
		return persisted, true, nil
	}
	if !gormWorkspaceNoRows(err) {
		return domain.SourceVersion{}, false, err
	}
	row, err = gormWorkspaceRawRow(transaction.WithContext(ctx), `UPDATE core.source_version AS sv SET content_artifact_id=? WHERE sv.workspace_id=? AND sv.source_id=? AND sv.content_hash=? AND sv.content_artifact_id IS NULL RETURNING id::text,source_id::text,content_artifact_id::text,content_hash,byte_size,mime_type,original_content_location,security_status,COALESCE(parser_version,''),captured_at`, string(version.ContentArtifactID), string(workspaceID), string(version.SourceID), version.ContentHash)
	if err != nil {
		return domain.SourceVersion{}, false, err
	}
	persisted, err = scanSourceVersion(row)
	if err == nil {
		return persisted, false, nil
	}
	if !gormWorkspaceNoRows(err) {
		return domain.SourceVersion{}, false, err
	}
	row, err = gormWorkspaceRawRow(transaction.WithContext(ctx), `SELECT id::text,source_id::text,content_artifact_id::text,content_hash,byte_size,mime_type,original_content_location,security_status,COALESCE(parser_version,''),captured_at FROM core.source_version sv WHERE sv.workspace_id=? AND sv.source_id=? AND sv.content_hash=?`, string(workspaceID), string(version.SourceID), version.ContentHash)
	if err != nil {
		return domain.SourceVersion{}, false, err
	}
	persisted, err = scanSourceVersion(row)
	if err == nil && !sameSourceVersionMetadata(persisted, version) {
		return domain.SourceVersion{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_VERSION_METADATA_CONFLICT", false, errors.New("existing source version metadata differs from the requested registration"))
	}
	return persisted, false, err
}

var _ workspaceapplication.ScopedSourceWriter = (*GORMRepository)(nil)
