package workspacepostgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"gorm.io/gorm"
)

// ResolveWorkspace reuses only an exact root binding and never rewrites identity fields.
func (repository *GORMRepository) ResolveWorkspace(ctx context.Context, reservation domain.WorkspaceReservation) (domain.WorkspaceResolution, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.WorkspaceResolution{}, err
	}
	var resolution domain.WorkspaceResolution
	var outcomeErr error
	err := repository.within(ctx, foundation.TransactionOptions{}, func(transactionCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		if err := transaction.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, reservation.Binding.Fingerprint).Error; err != nil {
			return classifyGORMControl(transactionCtx, err, domain.ErrorCodeControlDatabaseUnavailable)
		}
		state, err := gormRegistryControlState(transactionCtx, transaction, `FOR UPDATE`)
		if err != nil {
			return err
		}
		if state.OperationID != nil {
			return switchInProgress(errors.New("workspace Registry identity cannot change during a switch"))
		}
		now, err := gormRegistryDatabaseNow(transactionCtx, transaction)
		if err != nil {
			return err
		}
		rows, err := gormWorkspaceRows(transaction, `SELECT `+workspaceColumns+`
FROM core.workspace
WHERE root_path=? OR root_fingerprint=?
ORDER BY (root_path=?) DESC,id
FOR UPDATE`, reservation.Binding.CanonicalPath, reservation.Binding.Fingerprint, reservation.Binding.CanonicalPath)
		if err != nil {
			return classifyGORMControl(transactionCtx, err, domain.ErrorCodeControlDatabaseUnavailable)
		}
		defer rows.Close()
		matches := make([]domain.Workspace, 0)
		for rows.Next() {
			workspace, scanErr := scanWorkspace(rows)
			if scanErr != nil {
				return classifyGORMControl(transactionCtx, scanErr, domain.ErrorCodeControlDatabaseUnavailable)
			}
			matches = append(matches, workspace)
		}
		if err := rows.Err(); err != nil {
			return classifyGORMControl(transactionCtx, err, domain.ErrorCodeControlDatabaseUnavailable)
		}
		rows.Close()

		for _, workspace := range matches {
			if workspace.RootPath != reservation.Binding.CanonicalPath {
				continue
			}
			if workspace.BindingVersion == 0 || workspace.RootFingerprint == "" {
				marked, markErr := gormMarkWorkspaceMigrationRequired(transactionCtx, transaction, state, workspace.ID, "WORKSPACE_BINDING_LEGACY_UNVERIFIED", now)
				if markErr != nil {
					return markErr
				}
				resolution = domain.WorkspaceResolution{Workspace: marked, Reused: true}
				outcomeErr = migrationRequired(errors.New("legacy workspace root identity is not verified"))
				return nil
			}
			if workspace.RootFingerprint != reservation.Binding.Fingerprint {
				marked, markErr := gormMarkWorkspaceMigrationRequired(transactionCtx, transaction, state, workspace.ID, "WORKSPACE_ROOT_IDENTITY_CHANGED", now)
				if markErr != nil {
					return markErr
				}
				resolution = domain.WorkspaceResolution{Workspace: marked, Reused: true}
				outcomeErr = identityConflict(errors.New("canonical root now resolves to another physical identity"))
				return nil
			}
			row, rowErr := gormWorkspaceRawRow(transaction, `UPDATE core.workspace
SET name=?,git_branch=?,git_head=?,git_dirty=?,git_checked_at=?,
	availability='available',availability_reason=NULL,availability_checked_at=?,
	removed_at=NULL,version=version+1,updated_at=?
WHERE id=? AND version=?
RETURNING `+workspaceColumns,
				reservation.Name, reservation.Git.Branch, reservation.Git.Head, reservation.Git.Dirty,
				reservation.Git.CheckedAt.UTC(), now, now, string(workspace.ID), workspace.Version)
			if rowErr != nil {
				return classifyGORMControl(transactionCtx, rowErr, domain.ErrorCodeControlDatabaseUnavailable)
			}
			persisted, scanErr := scanWorkspace(row)
			if gormWorkspaceNoRows(scanErr) {
				return workspaceVersionConflict(errors.New("workspace registry identity changed"))
			}
			if scanErr != nil {
				return classifyGORMControl(transactionCtx, scanErr, domain.ErrorCodeControlDatabaseUnavailable)
			}
			resolution = domain.WorkspaceResolution{Workspace: persisted, Reused: true}
			return nil
		}

		for _, workspace := range matches {
			if workspace.RootFingerprint != reservation.Binding.Fingerprint {
				continue
			}
			marked, markErr := gormMarkWorkspaceMigrationRequired(transactionCtx, transaction, state, workspace.ID, "WORKSPACE_ROOT_MOVED_REQUIRES_MIGRATION", now)
			if markErr != nil {
				return markErr
			}
			resolution = domain.WorkspaceResolution{Workspace: marked, Reused: true}
			outcomeErr = migrationRequired(errors.New("physical root identity is already bound at another canonical path"))
			return nil
		}

		row, rowErr := gormWorkspaceRawRow(transaction, `INSERT INTO core.workspace(
id,name,root_path,root_fingerprint,binding_version,
git_repository_path,git_branch,git_head,git_dirty,git_checked_at,
status,availability,availability_reason,availability_checked_at,last_opened_at,removed_at,
version,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,'inactive','available',NULL,?,NULL,NULL,1,?,?)
RETURNING `+workspaceColumns,
			string(reservation.ID), reservation.Name, reservation.Binding.CanonicalPath,
			reservation.Binding.Fingerprint, reservation.Binding.BindingVersion,
			reservation.Binding.GitRepositoryPath, reservation.Git.Branch, reservation.Git.Head,
			reservation.Git.Dirty, reservation.Git.CheckedAt.UTC(), now, now, now)
		if rowErr != nil {
			return classifyGORMControl(transactionCtx, rowErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		persisted, scanErr := scanWorkspace(row)
		if scanErr != nil {
			return classifyGORMControl(transactionCtx, scanErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		resolution = domain.WorkspaceResolution{Workspace: persisted}
		return nil
	})
	if err != nil {
		return domain.WorkspaceResolution{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if outcomeErr != nil {
		return resolution, outcomeErr
	}
	return resolution, nil
}

// ListRegistry returns recent identities without granting access to their roots.
func (repository *GORMRepository) ListRegistry(ctx context.Context, includeRemoved bool) ([]domain.Workspace, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	rows, err := gormWorkspaceRows(repository.database.WithContext(ctx), `SELECT `+workspaceColumns+`
FROM core.workspace
WHERE ?::boolean OR removed_at IS NULL
ORDER BY (status='active') DESC,last_opened_at DESC NULLS LAST,created_at DESC,id`, includeRemoved)
	if err != nil {
		return nil, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer rows.Close()
	workspaces := make([]domain.Workspace, 0)
	for rows.Next() {
		workspace, scanErr := scanWorkspace(rows)
		if scanErr != nil {
			return nil, classifyGORMControl(ctx, scanErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		workspaces = append(workspaces, workspace)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return workspaces, nil
}

// SetWorkspaceAvailability records a fresh host metadata check without rebinding.
func (repository *GORMRepository) SetWorkspaceAvailability(ctx context.Context, update domain.AvailabilityUpdate) (domain.Workspace, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Workspace{}, err
	}
	var persisted domain.Workspace
	err := repository.within(ctx, foundation.TransactionOptions{}, func(transactionCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		state, err := gormRegistryControlState(transactionCtx, transaction, `FOR UPDATE`)
		if err != nil {
			return err
		}
		workspace, err := gormRegistryWorkspaceForUpdate(transactionCtx, transaction, update.WorkspaceID)
		if err != nil {
			return err
		}
		if workspace.Version != update.ExpectedVersion {
			return workspaceVersionConflict(errors.New("workspace registry version changed"))
		}
		if workspace.Availability == domain.WorkspaceAvailabilityMigrationRequired || workspace.BindingVersion == 0 {
			return migrationRequired(errors.New("workspace root binding requires explicit migration"))
		}
		if (workspace.Status == domain.WorkspaceStatusActive || sameOptionalID(state.ActiveWorkspaceID, workspace.ID)) &&
			update.Availability != domain.WorkspaceAvailabilityAvailable {
			return controlStateConflict(errors.New("active workspace availability cannot be downgraded outside a switch"))
		}
		if state.OperationID != nil && sameOptionalID(state.TargetWorkspaceID, workspace.ID) {
			return switchInProgress(errors.New("in-flight target availability is operation-owned"))
		}
		now, err := gormRegistryDatabaseNow(transactionCtx, transaction)
		if err != nil {
			return err
		}
		removedAt := nullableTime(workspace.RemovedAt)
		if update.Availability == domain.WorkspaceAvailabilityAvailable {
			removedAt = nil
		}
		row, rowErr := gormWorkspaceRawRow(transaction, `UPDATE core.workspace
SET availability=?,availability_reason=?,availability_checked_at=?,removed_at=?,
	version=version+1,updated_at=?
WHERE id=? AND version=?
RETURNING `+workspaceColumns,
			string(update.Availability), nullableText(update.Reason), update.CheckedAt.UTC(), removedAt, now,
			string(update.WorkspaceID), update.ExpectedVersion)
		if rowErr != nil {
			return classifyGORMControl(transactionCtx, rowErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		persisted, err = scanWorkspace(row)
		if gormWorkspaceNoRows(err) {
			return workspaceVersionConflict(errors.New("workspace registry version changed"))
		}
		if err != nil {
			return classifyGORMControl(transactionCtx, err, domain.ErrorCodeControlDatabaseUnavailable)
		}
		return nil
	})
	if err != nil {
		return domain.Workspace{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return persisted, nil
}

// RemoveWorkspace soft-removes only an inactive identity that is not operation-owned.
func (repository *GORMRepository) RemoveWorkspace(ctx context.Context, removal domain.WorkspaceRemoval) (domain.Workspace, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Workspace{}, err
	}
	var persisted domain.Workspace
	err := repository.within(ctx, foundation.TransactionOptions{}, func(transactionCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		state, err := gormRegistryControlState(transactionCtx, transaction, `FOR UPDATE`)
		if err != nil {
			return err
		}
		workspace, err := gormRegistryWorkspaceForUpdate(transactionCtx, transaction, removal.WorkspaceID)
		if err != nil {
			return err
		}
		if workspace.Version != removal.ExpectedVersion {
			return workspaceVersionConflict(errors.New("workspace registry version changed"))
		}
		if !workspace.RemovedAt.IsZero() {
			return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeWorkspaceAlreadyRemoved, false, errors.New("workspace is already removed"))
		}
		if workspace.Status == domain.WorkspaceStatusActive || sameOptionalID(state.ActiveWorkspaceID, workspace.ID) ||
			sameOptionalID(state.ResumeWorkspaceID, workspace.ID) || sameOptionalID(state.TargetWorkspaceID, workspace.ID) {
			return activeRemoveConflict(errors.New("active, resumable, or in-flight workspace cannot be removed"))
		}
		now, err := gormRegistryDatabaseNow(transactionCtx, transaction)
		if err != nil {
			return err
		}
		row, rowErr := gormWorkspaceRawRow(transaction, `UPDATE core.workspace
SET availability=CASE WHEN availability='migration_required' THEN availability ELSE 'unavailable' END,
    availability_reason=CASE WHEN availability='migration_required' THEN availability_reason ELSE 'WORKSPACE_REMOVED' END,
	availability_checked_at=?,removed_at=?,version=version+1,updated_at=?
WHERE id=? AND version=? AND status='inactive'
RETURNING `+workspaceColumns, now, now, now, string(removal.WorkspaceID), removal.ExpectedVersion)
		if rowErr != nil {
			return classifyGORMControl(transactionCtx, rowErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		persisted, err = scanWorkspace(row)
		if gormWorkspaceNoRows(err) {
			return activeRemoveConflict(errors.New("workspace is no longer removable"))
		}
		if err != nil {
			return classifyGORMControl(transactionCtx, err, domain.ErrorCodeControlDatabaseUnavailable)
		}
		return nil
	})
	if err != nil {
		return domain.Workspace{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return persisted, nil
}

func gormRegistryWorkspaceForUpdate(ctx context.Context, transaction *gorm.DB, id foundation.ID) (domain.Workspace, error) {
	row, err := gormWorkspaceRawRow(transaction, `SELECT `+workspaceColumns+`
FROM core.workspace WHERE id=? FOR UPDATE`, string(id))
	if err != nil {
		return domain.Workspace{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	workspace, err := scanWorkspace(row)
	if gormWorkspaceNoRows(err) {
		return domain.Workspace{}, registryNotFound(err)
	}
	if err != nil {
		return domain.Workspace{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return workspace, nil
}

func gormMarkWorkspaceMigrationRequired(ctx context.Context, transaction *gorm.DB, state domain.ControlState, id foundation.ID, reason string, now time.Time) (domain.Workspace, error) {
	row, err := gormWorkspaceRawRow(transaction, `UPDATE core.workspace
SET status='inactive',availability='migration_required',availability_reason=?,
	availability_checked_at=?,removed_at=NULL,
	version=version+1,updated_at=GREATEST(updated_at,?)
WHERE id=?
RETURNING `+workspaceColumns, reason, now, now, string(id))
	if err != nil {
		return domain.Workspace{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	workspace, err := scanWorkspace(row)
	if err != nil {
		return domain.Workspace{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if sameOptionalID(state.ActiveWorkspaceID, id) || sameOptionalID(state.ResumeWorkspaceID, id) {
		activeID := nullableFoundationID(state.ActiveWorkspaceID)
		resumeID := nullableFoundationID(state.ResumeWorkspaceID)
		if sameOptionalID(state.ActiveWorkspaceID, id) {
			activeID = nil
		}
		if sameOptionalID(state.ResumeWorkspaceID, id) {
			resumeID = nil
		}
		row, rowErr := gormWorkspaceRawRow(transaction, `UPDATE ops.workspace_control_state
SET active_workspace_id=?,resume_workspace_id=?,last_error_code=?,
	state_version=state_version+1,updated_at=?
WHERE singleton=true AND state_version=? AND operation_id IS NULL
RETURNING `+controlStateColumns, activeID, resumeID, reason, now, state.StateVersion)
		if rowErr != nil {
			return domain.Workspace{}, classifyGORMControl(ctx, rowErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		if _, scanErr := scanControlState(row); scanErr != nil {
			return domain.Workspace{}, classifyGORMControl(ctx, scanErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
	}
	return workspace, nil
}

func gormRegistryControlState(ctx context.Context, transaction *gorm.DB, suffix string) (domain.ControlState, error) {
	row, err := gormWorkspaceRawRow(transaction, `SELECT `+controlStateColumns+`
FROM ops.workspace_control_state WHERE singleton=true `+suffix)
	if err != nil {
		return domain.ControlState{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	state, err := scanControlState(row)
	if gormWorkspaceNoRows(err) {
		return domain.ControlState{}, controlCorrupt(errors.New("workspace control singleton is missing"))
	}
	if err != nil {
		return domain.ControlState{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return state, nil
}

func gormRegistryMutationGate(ctx context.Context, transaction *gorm.DB, suffix string) (mutationGate, error) {
	row, err := gormWorkspaceRawRow(transaction, `SELECT owner_kind,owner_id::text,
lease_expires_at,version,updated_at
FROM ops.runtime_mutation_gate WHERE singleton=true `+suffix)
	if err != nil {
		return mutationGate{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	gate, err := scanMutationGate(row)
	if gormWorkspaceNoRows(err) {
		return mutationGate{}, controlCorrupt(errors.New("runtime mutation singleton is missing"))
	}
	if err != nil {
		return mutationGate{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return gate, nil
}

func gormRegistryDatabaseNow(ctx context.Context, transaction *gorm.DB) (time.Time, error) {
	row, err := gormWorkspaceRawRow(transaction, `SELECT clock_timestamp()`)
	if err != nil {
		return time.Time{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	var now time.Time
	if err := row.Scan(&now); err != nil {
		return time.Time{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return now.UTC(), nil
}

var _ application.RegistryStore = (*GORMRepository)(nil)
