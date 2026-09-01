package workspacepostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"gorm.io/gorm"
)

// RebindWorkspace performs the only supported root identity mutation. All
// control references are cleared first, stale runtime rows are made explicit,
// and the history row plus Registry update commit in one PostgreSQL transaction.
func (repository *GORMRepository) RebindWorkspace(ctx context.Context, migration domain.WorkspaceBindingMigration) (domain.WorkspaceBindingMigrationResult, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.WorkspaceBindingMigrationResult{}, err
	}
	if nilRepositoryDependency(repository.audit) {
		return domain.WorkspaceBindingMigrationResult{}, controlUnavailable(errors.New("workspace rebind persistence dependencies are unavailable"))
	}
	var result domain.WorkspaceBindingMigrationResult
	err := repository.within(ctx, foundation.TransactionOptions{}, func(transactionCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		if err := transaction.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, string(migration.WorkspaceID)).Error; err != nil {
			return classifyGORMControl(transactionCtx, err, domain.ErrorCodeControlDatabaseUnavailable)
		}
		firstFingerprint, secondFingerprint := migration.OldRootFingerprint, migration.NewRootFingerprint
		if secondFingerprint < firstFingerprint {
			firstFingerprint, secondFingerprint = secondFingerprint, firstFingerprint
		}
		for _, fingerprint := range []string{firstFingerprint, secondFingerprint} {
			if err := transaction.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, fingerprint).Error; err != nil {
				return classifyGORMControl(transactionCtx, err, domain.ErrorCodeControlDatabaseUnavailable)
			}
		}
		state, err := gormRegistryControlState(transactionCtx, transaction, `FOR UPDATE`)
		if err != nil {
			return err
		}
		gate, err := gormRegistryMutationGate(transactionCtx, transaction, `FOR UPDATE`)
		if err != nil {
			return err
		}
		workspace, err := gormRegistryWorkspaceForUpdate(transactionCtx, transaction, migration.WorkspaceID)
		if err != nil {
			return err
		}
		if gate.OwnerID != nil || state.ActiveWorkspaceID != nil || state.ResumeWorkspaceID != nil ||
			state.OperationID != nil || state.OperationPhase != "" || state.TargetWorkspaceID != nil ||
			state.PreviousActiveWorkspaceID != nil || state.ControllerLeaseOwnerID != nil ||
			state.ControllerLeaseExpiresAt != (time.Time{}) {
			return rebindConflict(errors.New("workspace control state is still bound"))
		}
		if workspace.RootPath == migration.CanonicalRoot && workspace.Git.RepositoryPath == migration.CanonicalRoot &&
			workspace.RootFingerprint == migration.NewRootFingerprint && workspace.BindingVersion > 1 {
			row, rowErr := gormWorkspaceRawRow(transaction, `SELECT EXISTS(
	SELECT 1
	FROM ops.workspace_root_binding_history AS history
	JOIN ops.audit_event AS audit ON audit.id=history.id
	WHERE history.workspace_id=? AND history.canonical_root=? AND history.old_root_fingerprint=?
	  AND history.new_root_fingerprint=? AND history.new_binding_version=?
	  AND audit.workspace_id=history.workspace_id
	  AND audit.actor_type='SYSTEM'
	  AND audit.actor_ref=history.controller_instance_id::text
	  AND audit.action='workspace.root.rebound'
	  AND audit.resource_type='workspace_root_binding')`,
				string(migration.WorkspaceID), migration.CanonicalRoot, migration.OldRootFingerprint,
				migration.NewRootFingerprint, workspace.BindingVersion)
			if rowErr != nil {
				return classifyGORMControl(transactionCtx, rowErr, domain.ErrorCodeControlDatabaseUnavailable)
			}
			var found bool
			if err := row.Scan(&found); err != nil {
				return classifyGORMControl(transactionCtx, err, domain.ErrorCodeControlDatabaseUnavailable)
			}
			if found {
				result = domain.WorkspaceBindingMigrationResult{Workspace: workspace, Changed: false}
				return nil
			}
		}
		if workspace.RootPath != migration.CanonicalRoot || workspace.Git.RepositoryPath != migration.CanonicalRoot ||
			workspace.RootFingerprint != migration.OldRootFingerprint || workspace.BindingVersion < 1 ||
			workspace.Status != domain.WorkspaceStatusInactive ||
			workspace.Availability != domain.WorkspaceAvailabilityMigrationRequired ||
			workspace.AvailabilityReason != workspaceRootRebindReason || !workspace.RemovedAt.IsZero() {
			return identityConflict(errors.New("workspace root identity is not awaiting explicit rebind"))
		}
		row, rowErr := gormWorkspaceRawRow(transaction, `SELECT EXISTS(
	SELECT 1 FROM core.workspace_git_capture_checkpoint WHERE workspace_id=?)`, string(migration.WorkspaceID))
		if rowErr != nil {
			return classifyGORMControl(transactionCtx, rowErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		var hasGitCaptureCheckpoint bool
		if err := row.Scan(&hasGitCaptureCheckpoint); err != nil {
			return classifyGORMControl(transactionCtx, err, domain.ErrorCodeControlDatabaseUnavailable)
		}
		if hasGitCaptureCheckpoint {
			return rebindConflict(errors.New("workspace Git capture lineage requires explicit rebaseline"))
		}
		now, err := gormRegistryDatabaseNow(transactionCtx, transaction)
		if err != nil {
			return err
		}
		if err := transaction.Exec(`UPDATE ops.workspace_runtime
SET operation_id=NULL,phase='unavailable',heartbeat_at=?,version=version+1
WHERE workspace_id=? AND phase<>'unavailable'`, now, string(migration.WorkspaceID)).Error; err != nil {
			return classifyGORMControl(transactionCtx, err, domain.ErrorCodeControlDatabaseUnavailable)
		}
		if err := transaction.Exec(`INSERT INTO ops.workspace_root_binding_history(
id,workspace_id,controller_instance_id,idempotency_key,canonical_root,
old_root_fingerprint,new_root_fingerprint,old_binding_version,new_binding_version,
old_workspace_version,new_workspace_version,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			string(migration.ID), string(migration.WorkspaceID), string(migration.ControllerInstanceID), migration.IdempotencyKey,
			migration.CanonicalRoot, migration.OldRootFingerprint, migration.NewRootFingerprint,
			workspace.BindingVersion, workspace.BindingVersion+1, workspace.Version, workspace.Version+1, now).Error; err != nil {
			return classifyGORMControl(transactionCtx, err, domain.ErrorCodeControlDatabaseUnavailable)
		}
		row, rowErr = gormWorkspaceRawRow(transaction, `UPDATE core.workspace
SET root_fingerprint=?,binding_version=binding_version+1,
    status='inactive',availability='available',availability_reason=NULL,
    availability_checked_at=?,removed_at=NULL,version=version+1,updated_at=?
WHERE id=? AND version=?
RETURNING `+workspaceColumns,
			migration.NewRootFingerprint, now, now, string(migration.WorkspaceID), workspace.Version)
		if rowErr != nil {
			return classifyGORMControl(transactionCtx, rowErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		persisted, scanErr := scanWorkspace(row)
		if gormWorkspaceNoRows(scanErr) {
			return workspaceVersionConflict(errors.New("workspace root binding version changed"))
		}
		if scanErr != nil {
			return classifyGORMControl(transactionCtx, scanErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		if err := repository.appendGORMWorkspaceRootRebindAudit(transactionCtx, scope, migration,
			workspace.BindingVersion, persisted.BindingVersion, workspace.Version, persisted.Version, now); err != nil {
			return err
		}
		if err := transaction.Exec(`UPDATE ops.workspace_control_state
SET last_error_code=CASE WHEN last_error_code='WORKSPACE_ROOT_IDENTITY_CHANGED' THEN NULL ELSE last_error_code END,
    state_version=state_version+1,updated_at=?
WHERE singleton=true AND state_version=?`, now, state.StateVersion).Error; err != nil {
			return classifyGORMControl(transactionCtx, err, domain.ErrorCodeControlDatabaseUnavailable)
		}
		result = domain.WorkspaceBindingMigrationResult{Workspace: persisted, Changed: true}
		return nil
	})
	if err != nil {
		return domain.WorkspaceBindingMigrationResult{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return result, nil
}

func (repository *GORMRepository) appendGORMWorkspaceRootRebindAudit(
	ctx context.Context,
	scope foundation.TransactionScope,
	migration domain.WorkspaceBindingMigration,
	oldBindingVersion, newBindingVersion, oldWorkspaceVersion, newWorkspaceVersion int64,
	occurredAt time.Time,
) error {
	if repository == nil || nilRepositoryDependency(repository.audit) {
		return controlUnavailable(errors.New("workspace rebind audit appender is unavailable"))
	}
	correlation, err := json.Marshal(struct {
		HistoryID string `json:"workspace_binding_history_id"`
	}{HistoryID: string(migration.ID)})
	if err != nil {
		return controlCorrupt(errors.New("workspace rebind audit correlation cannot be encoded"))
	}
	metadata, err := json.Marshal(struct {
		OldRootFingerprint  string `json:"old_root_fingerprint"`
		NewRootFingerprint  string `json:"new_root_fingerprint"`
		OldBindingVersion   int64  `json:"old_binding_version"`
		NewBindingVersion   int64  `json:"new_binding_version"`
		OldWorkspaceVersion int64  `json:"old_workspace_version"`
		NewWorkspaceVersion int64  `json:"new_workspace_version"`
		Reason              string `json:"reason"`
	}{
		OldRootFingerprint: migration.OldRootFingerprint, NewRootFingerprint: migration.NewRootFingerprint,
		OldBindingVersion: oldBindingVersion, NewBindingVersion: newBindingVersion,
		OldWorkspaceVersion: oldWorkspaceVersion, NewWorkspaceVersion: newWorkspaceVersion,
		Reason: workspaceRootRebindReason,
	})
	if err != nil {
		return controlCorrupt(errors.New("workspace rebind audit metadata cannot be encoded"))
	}
	workspaceID := migration.WorkspaceID
	event, err := auditdomain.NewEvent(auditdomain.Event{
		ID: migration.ID, WorkspaceID: &workspaceID,
		ActorType: auditdomain.ActorSystem, ActorRef: string(migration.ControllerInstanceID),
		Action: workspaceRootRebindAuditAction, ResourceType: workspaceRootRebindAuditResourceType,
		ResourceRef: "workspace_root_binding:" + string(migration.ID), Outcome: auditdomain.OutcomeSucceeded,
		IdempotencyKey: fmt.Sprintf("workspace.root.rebind:%s", migration.ID),
		Correlation:    correlation, Metadata: metadata, SchemaVersion: auditdomain.SchemaVersion,
		OccurredAt: occurredAt.UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		return controlCorrupt(errors.Join(errors.New("workspace rebind audit event is invalid"), err))
	}
	_, replayed, err := repository.audit.AppendScoped(ctx, scope, event)
	if err != nil {
		return err
	}
	if replayed {
		return controlCorrupt(errors.New("workspace rebind audit unexpectedly replayed before commit"))
	}
	return nil
}
