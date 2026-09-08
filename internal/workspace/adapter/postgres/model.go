package workspacepostgres

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const workspaceColumns = `id::text,name,root_path,root_fingerprint,binding_version,
	git_repository_path,git_branch,git_head,git_dirty,git_checked_at,
	status,availability,availability_reason,availability_checked_at,last_opened_at,removed_at,
	version,created_at,updated_at`

func parseMaterialID(value, field string) (foundation.ID, error) {
	parsed, err := foundation.ParseID(value)
	if err != nil {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_MATERIAL_ID_INVALID", false, fmt.Errorf("parse %s id: %w", field, err))
	}
	return parsed, nil
}

func sameSourceVersionMetadata(existing, requested domain.SourceVersion) bool {
	return existing.SourceID == requested.SourceID &&
		existing.ContentArtifactID == requested.ContentArtifactID &&
		existing.ContentHash == requested.ContentHash &&
		existing.ByteSize == requested.ByteSize &&
		existing.MediaType == requested.MediaType &&
		existing.OriginalContentLocation == requested.OriginalContentLocation &&
		existing.SecurityStatus == requested.SecurityStatus &&
		existing.ParserVersion == requested.ParserVersion
}

func scanContentArtifact(row rowScanner) (domain.ContentArtifact, error) {
	var artifact domain.ContentArtifact
	var id, workspaceID string
	if err := row.Scan(&id, &workspaceID, &artifact.ContentHash, &artifact.ByteSize, &artifact.ManagedLocation, &artifact.CreatedAt); err != nil {
		return domain.ContentArtifact{}, err
	}
	parsedID, err := foundation.ParseID(id)
	if err != nil {
		return domain.ContentArtifact{}, fmt.Errorf("parse content artifact id: %w", err)
	}
	parsedWorkspaceID, err := foundation.ParseID(workspaceID)
	if err != nil {
		return domain.ContentArtifact{}, fmt.Errorf("parse content artifact workspace id: %w", err)
	}
	artifact.ID = parsedID
	artifact.WorkspaceID = parsedWorkspaceID
	return artifact, nil
}

type rowScanner interface{ Scan(...any) error }

func scanWorkspace(row rowScanner) (domain.Workspace, error) {
	var workspace domain.Workspace
	var id, status, availability string
	var fingerprint, availabilityReason sql.NullString
	var availabilityCheckedAt, lastOpenedAt, removedAt sql.NullTime
	err := row.Scan(&id, &workspace.Name, &workspace.RootPath, &fingerprint, &workspace.BindingVersion,
		&workspace.Git.RepositoryPath, &workspace.Git.Branch, &workspace.Git.Head,
		&workspace.Git.Dirty, &workspace.Git.CheckedAt, &status, &availability,
		&availabilityReason, &availabilityCheckedAt, &lastOpenedAt, &removedAt, &workspace.Version,
		&workspace.CreatedAt, &workspace.UpdatedAt)
	if err != nil {
		return domain.Workspace{}, err
	}
	parsed, err := foundation.ParseID(id)
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("parse workspace id: %w", err)
	}
	workspace.ID = parsed
	workspace.Status = domain.WorkspaceStatus(status)
	workspace.Availability = domain.WorkspaceAvailability(availability)
	workspace.RootFingerprint = fingerprint.String
	workspace.AvailabilityReason = availabilityReason.String
	if availabilityCheckedAt.Valid {
		workspace.AvailabilityCheckedAt = availabilityCheckedAt.Time
	}
	if lastOpenedAt.Valid {
		workspace.LastOpenedAt = lastOpenedAt.Time
	}
	if removedAt.Valid {
		workspace.RemovedAt = removedAt.Time
	}
	return workspace, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func scanSource(row rowScanner) (domain.Source, error) {
	var source domain.Source
	var id, workspaceID string
	if err := row.Scan(&id, &workspaceID, &source.Type, &source.LogicalName, &source.OriginalLocation, &source.CreatedAt); err != nil {
		return domain.Source{}, err
	}
	parsedID, err := foundation.ParseID(id)
	if err != nil {
		return domain.Source{}, fmt.Errorf("parse source id: %w", err)
	}
	parsedWorkspaceID, err := foundation.ParseID(workspaceID)
	if err != nil {
		return domain.Source{}, fmt.Errorf("parse source workspace id: %w", err)
	}
	source.ID = parsedID
	source.WorkspaceID = parsedWorkspaceID
	return source, nil
}

func scanSourceVersion(row rowScanner) (domain.SourceVersion, error) {
	var version domain.SourceVersion
	var id, sourceID, artifactID string
	if err := row.Scan(&id, &sourceID, &artifactID, &version.ContentHash, &version.ByteSize,
		&version.MediaType, &version.OriginalContentLocation, &version.SecurityStatus,
		&version.ParserVersion, &version.CapturedAt); err != nil {
		return domain.SourceVersion{}, err
	}
	parsedID, err := foundation.ParseID(id)
	if err != nil {
		return domain.SourceVersion{}, fmt.Errorf("parse source version id: %w", err)
	}
	parsedSourceID, err := foundation.ParseID(sourceID)
	if err != nil {
		return domain.SourceVersion{}, fmt.Errorf("parse source version source id: %w", err)
	}
	version.ID = parsedID
	version.SourceID = parsedSourceID
	parsedArtifactID, err := foundation.ParseID(artifactID)
	if err != nil {
		return domain.SourceVersion{}, fmt.Errorf("parse source version content artifact id: %w", err)
	}
	version.ContentArtifactID = parsedArtifactID
	return version, nil
}
