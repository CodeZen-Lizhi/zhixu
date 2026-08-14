package workspacepostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5"
)

const (
	workspaceRootRebindAuditAction       = "workspace.root.rebound"
	workspaceRootRebindAuditResourceType = "workspace_root_binding"
	workspaceRootRebindReason            = "WORKSPACE_ROOT_IDENTITY_CHANGED"
)

func (r *Repository) appendWorkspaceRootRebindAudit(
	ctx context.Context,
	tx pgx.Tx,
	migration domain.WorkspaceBindingMigration,
	oldBindingVersion, newBindingVersion, oldWorkspaceVersion, newWorkspaceVersion int64,
	occurredAt time.Time,
) error {
	if r == nil || nilRepositoryDependency(r.audit) {
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
		OldRootFingerprint:  migration.OldRootFingerprint,
		NewRootFingerprint:  migration.NewRootFingerprint,
		OldBindingVersion:   oldBindingVersion,
		NewBindingVersion:   newBindingVersion,
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
		ResourceRef:    "workspace_root_binding:" + string(migration.ID),
		Outcome:        auditdomain.OutcomeSucceeded,
		IdempotencyKey: fmt.Sprintf("workspace.root.rebind:%s", migration.ID),
		Correlation:    correlation, Metadata: metadata,
		SchemaVersion: auditdomain.SchemaVersion,
		OccurredAt:    occurredAt.UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		return controlCorrupt(errors.Join(errors.New("workspace rebind audit event is invalid"), err))
	}
	_, replayed, err := r.audit.AppendTx(ctx, tx, event)
	if err != nil {
		return err
	}
	if replayed {
		return controlCorrupt(errors.New("workspace rebind audit unexpectedly replayed before commit"))
	}
	return nil
}
