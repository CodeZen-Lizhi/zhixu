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
		// The path fingerprint schema and persisted Workspace binding generation
		// are independent; the latter may be >1 after an explicit rebind.
		if workspace.RootFingerprint != reservation.Binding.Fingerprint {
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

// RebindWorkspace performs the only supported root identity mutation. All
// control references are cleared first, stale runtime rows are made explicit,
// and the history row plus Registry update commit in one PostgreSQL transaction.
func (r *Repository) RebindWorkspace(ctx context.Context, migration domain.WorkspaceBindingMigration) (domain.WorkspaceBindingMigrationResult, error) {
	if r == nil || r.db == nil || nilRepositoryDependency(r.audit) {
		return domain.WorkspaceBindingMigrationResult{}, controlUnavailable(errors.New("workspace rebind persistence dependencies are unavailable"))
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.WorkspaceBindingMigrationResult{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(migration.WorkspaceID)); err != nil {
		return domain.WorkspaceBindingMigrationResult{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	firstFingerprint, secondFingerprint := migration.OldRootFingerprint, migration.NewRootFingerprint
	if secondFingerprint < firstFingerprint {
		firstFingerprint, secondFingerprint = secondFingerprint, firstFingerprint
	}
	for _, fingerprint := range []string{firstFingerprint, secondFingerprint} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fingerprint); err != nil {
			return domain.WorkspaceBindingMigrationResult{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
		}
	}
	state, err := loadControlState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.WorkspaceBindingMigrationResult{}, err
	}
	gate, err := loadMutationGate(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.WorkspaceBindingMigrationResult{}, err
	}
	workspace, err := loadWorkspaceForUpdate(ctx, tx, migration.WorkspaceID)
	if err != nil {
		return domain.WorkspaceBindingMigrationResult{}, err
	}
	if gate.OwnerID != nil || state.ActiveWorkspaceID != nil || state.ResumeWorkspaceID != nil ||
		state.OperationID != nil || state.OperationPhase != "" || state.TargetWorkspaceID != nil ||
		state.PreviousActiveWorkspaceID != nil || state.ControllerLeaseOwnerID != nil ||
		state.ControllerLeaseExpiresAt != (time.Time{}) {
		return domain.WorkspaceBindingMigrationResult{}, rebindConflict(errors.New("workspace control state is still bound"))
	}
	// A launcher can lose its response after the database commit. A precise
	// old->new history record makes the retry a read-only exact replay, but only
	// after the global gate and control state have both proved idle.
	if workspace.RootPath == migration.CanonicalRoot && workspace.Git.RepositoryPath == migration.CanonicalRoot &&
		workspace.RootFingerprint == migration.NewRootFingerprint && workspace.BindingVersion > 1 {
		var found bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(
	SELECT 1
	FROM ops.workspace_root_binding_history AS history
	JOIN ops.audit_event AS audit ON audit.id=history.id
	WHERE history.workspace_id=$1 AND history.canonical_root=$2 AND history.old_root_fingerprint=$3
	  AND history.new_root_fingerprint=$4 AND history.new_binding_version=$5
		  AND audit.workspace_id=history.workspace_id
		  AND audit.actor_type='SYSTEM'
		  AND audit.actor_ref=history.controller_instance_id::text
		  AND audit.action='workspace.root.rebound'
		  AND audit.resource_type='workspace_root_binding')`,
			string(migration.WorkspaceID), migration.CanonicalRoot, migration.OldRootFingerprint,
			migration.NewRootFingerprint, workspace.BindingVersion).Scan(&found)
		if err != nil {
			return domain.WorkspaceBindingMigrationResult{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
		}
		if found {
			if err := commitWorkspaceTx(ctx, tx); err != nil {
				return domain.WorkspaceBindingMigrationResult{}, err
			}
			return domain.WorkspaceBindingMigrationResult{Workspace: workspace, Changed: false}, nil
		}
	}
	if workspace.RootPath != migration.CanonicalRoot || workspace.Git.RepositoryPath != migration.CanonicalRoot ||
		workspace.RootFingerprint != migration.OldRootFingerprint ||
		workspace.BindingVersion < 1 || workspace.Status != domain.WorkspaceStatusInactive ||
		workspace.Availability != domain.WorkspaceAvailabilityMigrationRequired ||
		workspace.AvailabilityReason != "WORKSPACE_ROOT_IDENTITY_CHANGED" || !workspace.RemovedAt.IsZero() {
		return domain.WorkspaceBindingMigrationResult{}, identityConflict(errors.New("workspace root identity is not awaiting explicit rebind"))
	}
	var hasGitCaptureCheckpoint bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
	SELECT 1 FROM core.workspace_git_capture_checkpoint WHERE workspace_id=$1)`, string(migration.WorkspaceID)).Scan(&hasGitCaptureCheckpoint); err != nil {
		return domain.WorkspaceBindingMigrationResult{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if hasGitCaptureCheckpoint {
		return domain.WorkspaceBindingMigrationResult{}, rebindConflict(errors.New("workspace Git capture lineage requires explicit rebaseline"))
	}
	now, err := workspaceDatabaseNow(ctx, tx)
	if err != nil {
		return domain.WorkspaceBindingMigrationResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.workspace_runtime
SET operation_id=NULL,phase='unavailable',heartbeat_at=$2,version=version+1
WHERE workspace_id=$1 AND phase<>'unavailable'`, string(migration.WorkspaceID), now); err != nil {
		return domain.WorkspaceBindingMigrationResult{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.workspace_root_binding_history(
id,workspace_id,controller_instance_id,idempotency_key,canonical_root,
old_root_fingerprint,new_root_fingerprint,old_binding_version,new_binding_version,
old_workspace_version,new_workspace_version,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		string(migration.ID), string(migration.WorkspaceID), string(migration.ControllerInstanceID), migration.IdempotencyKey,
		migration.CanonicalRoot, migration.OldRootFingerprint, migration.NewRootFingerprint,
		workspace.BindingVersion, workspace.BindingVersion+1, workspace.Version, workspace.Version+1, now); err != nil {
		return domain.WorkspaceBindingMigrationResult{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	persisted, err := scanWorkspace(tx.QueryRow(ctx, `UPDATE core.workspace
SET root_fingerprint=$2,binding_version=binding_version+1,
    status='inactive',availability='available',availability_reason=NULL,
    availability_checked_at=$3,removed_at=NULL,version=version+1,updated_at=$3
WHERE id=$1 AND version=$4
RETURNING `+workspaceColumns,
		string(migration.WorkspaceID), migration.NewRootFingerprint, now, workspace.Version))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkspaceBindingMigrationResult{}, workspaceVersionConflict(errors.New("workspace root binding version changed"))
	}
	if err != nil {
		return domain.WorkspaceBindingMigrationResult{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if err := r.appendWorkspaceRootRebindAudit(ctx, tx, migration,
		workspace.BindingVersion, persisted.BindingVersion, workspace.Version, persisted.Version, now); err != nil {
		return domain.WorkspaceBindingMigrationResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.workspace_control_state
SET last_error_code=CASE WHEN last_error_code='WORKSPACE_ROOT_IDENTITY_CHANGED' THEN NULL ELSE last_error_code END,
    state_version=state_version+1,updated_at=$1
WHERE singleton=true AND state_version=$2`, now, state.StateVersion); err != nil {
		return domain.WorkspaceBindingMigrationResult{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.WorkspaceBindingMigrationResult{}, err
	}
	return domain.WorkspaceBindingMigrationResult{Workspace: persisted, Changed: true}, nil
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
