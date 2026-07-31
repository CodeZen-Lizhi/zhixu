package workspacepostgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5"
)

// ResolveWorkspace reuses only an exact root binding and never rewrites identity fields.
func (r *Repository) ResolveWorkspace(ctx context.Context, reservation domain.WorkspaceReservation) (domain.WorkspaceResolution, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.WorkspaceResolution{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, reservation.Binding.Fingerprint); err != nil {
		return domain.WorkspaceResolution{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	state, err := loadControlState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.WorkspaceResolution{}, err
	}
	if state.OperationID != nil {
		return domain.WorkspaceResolution{}, switchInProgress(errors.New("workspace Registry identity cannot change during a switch"))
	}
	now, err := workspaceDatabaseNow(ctx, tx)
	if err != nil {
		return domain.WorkspaceResolution{}, err
	}
	rows, err := tx.Query(ctx, `SELECT `+workspaceColumns+`
FROM core.workspace
WHERE root_path=$1 OR root_fingerprint=$2
ORDER BY (root_path=$1) DESC,id
FOR UPDATE`, reservation.Binding.CanonicalPath, reservation.Binding.Fingerprint)
	if err != nil {
		return domain.WorkspaceResolution{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	var matches []domain.Workspace
	for rows.Next() {
		workspace, scanErr := scanWorkspace(rows)
		if scanErr != nil {
			rows.Close()
			return domain.WorkspaceResolution{}, classifyControl(scanErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		matches = append(matches, workspace)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.WorkspaceResolution{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	rows.Close()

	for _, workspace := range matches {
		if workspace.RootPath != reservation.Binding.CanonicalPath {
			continue
		}
		if workspace.BindingVersion == 0 || workspace.RootFingerprint == "" {
			marked, markErr := markWorkspaceMigrationRequired(ctx, tx, state, workspace.ID, "WORKSPACE_BINDING_LEGACY_UNVERIFIED", now)
			if markErr != nil {
				return domain.WorkspaceResolution{}, markErr
			}
			if err := commitWorkspaceTx(ctx, tx); err != nil {
				return domain.WorkspaceResolution{}, err
			}
			return domain.WorkspaceResolution{Workspace: marked, Reused: true}, migrationRequired(errors.New("legacy workspace root identity is not verified"))
		}
		if workspace.RootFingerprint != reservation.Binding.Fingerprint || workspace.BindingVersion != reservation.Binding.BindingVersion {
			marked, markErr := markWorkspaceMigrationRequired(ctx, tx, state, workspace.ID, "WORKSPACE_ROOT_IDENTITY_CHANGED", now)
			if markErr != nil {
				return domain.WorkspaceResolution{}, markErr
			}
			if err := commitWorkspaceTx(ctx, tx); err != nil {
				return domain.WorkspaceResolution{}, err
			}
			return domain.WorkspaceResolution{Workspace: marked, Reused: true}, identityConflict(errors.New("canonical root now resolves to another physical identity"))
		}
		persisted, updateErr := scanWorkspace(tx.QueryRow(ctx, `UPDATE core.workspace
SET name=$2,git_branch=$3,git_head=$4,git_dirty=$5,git_checked_at=$6,
	availability='available',availability_reason=NULL,availability_checked_at=$7,
	removed_at=NULL,version=version+1,updated_at=$8
WHERE id=$1 AND version=$9
RETURNING `+workspaceColumns,
			string(workspace.ID), reservation.Name, reservation.Git.Branch, reservation.Git.Head,
			reservation.Git.Dirty, reservation.Git.CheckedAt.UTC(), now, now, workspace.Version))
		if errors.Is(updateErr, pgx.ErrNoRows) {
			return domain.WorkspaceResolution{}, workspaceVersionConflict(errors.New("workspace registry identity changed"))
		}
		if updateErr != nil {
			return domain.WorkspaceResolution{}, classifyControl(updateErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		if err := commitWorkspaceTx(ctx, tx); err != nil {
			return domain.WorkspaceResolution{}, err
		}
		return domain.WorkspaceResolution{Workspace: persisted, Reused: true}, nil
	}

	for _, workspace := range matches {
		if workspace.RootFingerprint != reservation.Binding.Fingerprint {
			continue
		}
		marked, markErr := markWorkspaceMigrationRequired(ctx, tx, state, workspace.ID, "WORKSPACE_ROOT_MOVED_REQUIRES_MIGRATION", now)
		if markErr != nil {
			return domain.WorkspaceResolution{}, markErr
		}
		if err := commitWorkspaceTx(ctx, tx); err != nil {
			return domain.WorkspaceResolution{}, err
		}
		return domain.WorkspaceResolution{Workspace: marked, Reused: true}, migrationRequired(errors.New("physical root identity is already bound at another canonical path"))
	}

	persisted, err := scanWorkspace(tx.QueryRow(ctx, `INSERT INTO core.workspace(
id,name,root_path,root_fingerprint,binding_version,
git_repository_path,git_branch,git_head,git_dirty,git_checked_at,
status,availability,availability_reason,availability_checked_at,last_opened_at,removed_at,
version,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'inactive','available',NULL,$11,NULL,NULL,1,$11,$11)
RETURNING `+workspaceColumns,
		string(reservation.ID), reservation.Name, reservation.Binding.CanonicalPath,
		reservation.Binding.Fingerprint, reservation.Binding.BindingVersion,
		reservation.Binding.GitRepositoryPath, reservation.Git.Branch, reservation.Git.Head,
		reservation.Git.Dirty, reservation.Git.CheckedAt.UTC(), now))
	if err != nil {
		return domain.WorkspaceResolution{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.WorkspaceResolution{}, err
	}
	return domain.WorkspaceResolution{Workspace: persisted}, nil
}

// ListRegistry returns recent identities without granting access to their roots.
func (r *Repository) ListRegistry(ctx context.Context, includeRemoved bool) ([]domain.Workspace, error) {
	rows, err := r.db.Query(ctx, `SELECT `+workspaceColumns+`
FROM core.workspace
WHERE $1::boolean OR removed_at IS NULL
ORDER BY (status='active') DESC,last_opened_at DESC NULLS LAST,created_at DESC,id`, includeRemoved)
	if err != nil {
		return nil, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer rows.Close()
	workspaces := make([]domain.Workspace, 0)
	for rows.Next() {
		workspace, scanErr := scanWorkspace(rows)
		if scanErr != nil {
			return nil, classifyControl(scanErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		workspaces = append(workspaces, workspace)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return workspaces, nil
}

// SetWorkspaceAvailability records a fresh host metadata check without rebinding.
func (r *Repository) SetWorkspaceAvailability(ctx context.Context, update domain.AvailabilityUpdate) (domain.Workspace, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Workspace{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadControlState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.Workspace{}, err
	}
	workspace, err := loadWorkspaceForUpdate(ctx, tx, update.WorkspaceID)
	if err != nil {
		return domain.Workspace{}, err
	}
	if workspace.Version != update.ExpectedVersion {
		return domain.Workspace{}, workspaceVersionConflict(errors.New("workspace registry version changed"))
	}
	if workspace.Availability == domain.WorkspaceAvailabilityMigrationRequired || workspace.BindingVersion == 0 {
		return domain.Workspace{}, migrationRequired(errors.New("workspace root binding requires explicit migration"))
	}
	if (workspace.Status == domain.WorkspaceStatusActive || sameOptionalID(state.ActiveWorkspaceID, workspace.ID)) &&
		update.Availability != domain.WorkspaceAvailabilityAvailable {
		return domain.Workspace{}, controlStateConflict(errors.New("active workspace availability cannot be downgraded outside a switch"))
	}
	if state.OperationID != nil && sameOptionalID(state.TargetWorkspaceID, workspace.ID) {
		return domain.Workspace{}, switchInProgress(errors.New("in-flight target availability is operation-owned"))
	}
	now, err := workspaceDatabaseNow(ctx, tx)
	if err != nil {
		return domain.Workspace{}, err
	}
	removedAt := nullableTime(workspace.RemovedAt)
	if update.Availability == domain.WorkspaceAvailabilityAvailable {
		removedAt = nil
	}
	persisted, err := scanWorkspace(tx.QueryRow(ctx, `UPDATE core.workspace
SET availability=$2,availability_reason=$3,availability_checked_at=$4,removed_at=$5,
	version=version+1,updated_at=$6
WHERE id=$1 AND version=$7
RETURNING `+workspaceColumns,
		string(update.WorkspaceID), string(update.Availability), nullableText(update.Reason),
		update.CheckedAt.UTC(), removedAt, now, update.ExpectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Workspace{}, workspaceVersionConflict(errors.New("workspace registry version changed"))
	}
	if err != nil {
		return domain.Workspace{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.Workspace{}, err
	}
	return persisted, nil
}

// RemoveWorkspace soft-removes only an inactive identity that is not operation-owned.
func (r *Repository) RemoveWorkspace(ctx context.Context, removal domain.WorkspaceRemoval) (domain.Workspace, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Workspace{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadControlState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.Workspace{}, err
	}
	workspace, err := loadWorkspaceForUpdate(ctx, tx, removal.WorkspaceID)
	if err != nil {
		return domain.Workspace{}, err
	}
	if workspace.Version != removal.ExpectedVersion {
		return domain.Workspace{}, workspaceVersionConflict(errors.New("workspace registry version changed"))
	}
	if !workspace.RemovedAt.IsZero() {
		return domain.Workspace{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeWorkspaceAlreadyRemoved, false, errors.New("workspace is already removed"))
	}
	if workspace.Status == domain.WorkspaceStatusActive || sameOptionalID(state.ActiveWorkspaceID, workspace.ID) ||
		sameOptionalID(state.ResumeWorkspaceID, workspace.ID) || sameOptionalID(state.TargetWorkspaceID, workspace.ID) {
		return domain.Workspace{}, activeRemoveConflict(errors.New("active, resumable, or in-flight workspace cannot be removed"))
	}
	now, err := workspaceDatabaseNow(ctx, tx)
	if err != nil {
		return domain.Workspace{}, err
	}
	persisted, err := scanWorkspace(tx.QueryRow(ctx, `UPDATE core.workspace
SET availability=CASE WHEN availability='migration_required' THEN availability ELSE 'unavailable' END,
    availability_reason=CASE WHEN availability='migration_required' THEN availability_reason ELSE 'WORKSPACE_REMOVED' END,
	availability_checked_at=$2,removed_at=$2,version=version+1,updated_at=$2
WHERE id=$1 AND version=$3 AND status='inactive'
RETURNING `+workspaceColumns, string(removal.WorkspaceID), now, removal.ExpectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Workspace{}, activeRemoveConflict(errors.New("workspace is no longer removable"))
	}
	if err != nil {
		return domain.Workspace{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.Workspace{}, err
	}
	return persisted, nil
}

func loadWorkspaceForUpdate(ctx context.Context, tx pgx.Tx, id foundation.ID) (domain.Workspace, error) {
	workspace, err := scanWorkspace(tx.QueryRow(ctx, `SELECT `+workspaceColumns+`
FROM core.workspace WHERE id=$1 FOR UPDATE`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Workspace{}, registryNotFound(err)
	}
	if err != nil {
		return domain.Workspace{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return workspace, nil
}

func markWorkspaceMigrationRequired(
	ctx context.Context,
	tx pgx.Tx,
	state domain.ControlState,
	id foundation.ID,
	reason string,
	now time.Time,
) (domain.Workspace, error) {
	workspace, err := scanWorkspace(tx.QueryRow(ctx, `UPDATE core.workspace
SET status='inactive',availability='migration_required',availability_reason=$2,
	availability_checked_at=$3,removed_at=NULL,
	version=version+1,updated_at=GREATEST(updated_at,$3)
WHERE id=$1
RETURNING `+workspaceColumns, string(id), reason, now))
	if err != nil {
		return domain.Workspace{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
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
		if _, updateErr := scanControlState(tx.QueryRow(ctx, `UPDATE ops.workspace_control_state
SET active_workspace_id=$1,resume_workspace_id=$2,last_error_code=$3,
	state_version=state_version+1,updated_at=$4
WHERE singleton=true AND state_version=$5 AND operation_id IS NULL
RETURNING `+controlStateColumns,
			activeID, resumeID, reason, now, state.StateVersion)); updateErr != nil {
			return domain.Workspace{}, classifyControl(updateErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
	}
	return workspace, nil
}

func commitWorkspaceTx(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return nil
}

func sameOptionalID(candidate *foundation.ID, expected foundation.ID) bool {
	return candidate != nil && *candidate == expected
}

var _ application.RegistryStore = (*Repository)(nil)
