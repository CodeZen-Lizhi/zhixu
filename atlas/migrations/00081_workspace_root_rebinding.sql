ALTER TABLE ops.audit_event
    ADD COLUMN transaction_id xid8 NOT NULL DEFAULT pg_current_xact_id();

CREATE TABLE ops.workspace_root_binding_history (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    controller_instance_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    canonical_root text NOT NULL,
    old_root_fingerprint text NOT NULL,
    new_root_fingerprint text NOT NULL,
    old_binding_version bigint NOT NULL,
    new_binding_version bigint NOT NULL,
    old_workspace_version bigint NOT NULL,
    new_workspace_version bigint NOT NULL,
    reason text NOT NULL DEFAULT 'WORKSPACE_ROOT_IDENTITY_CHANGED',
    transaction_id xid8 NOT NULL DEFAULT pg_current_xact_id(),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT workspace_root_binding_history_idempotency
        UNIQUE(controller_instance_id,idempotency_key),
    CONSTRAINT workspace_root_binding_history_transition
        UNIQUE(workspace_id,old_root_fingerprint,new_root_fingerprint,new_binding_version),
    CONSTRAINT workspace_root_binding_history_shape CHECK (
        octet_length(idempotency_key) BETWEEN 1 AND 256
        AND idempotency_key=btrim(idempotency_key)
        AND octet_length(canonical_root) BETWEEN 1 AND 4096
        AND canonical_root LIKE '/%'
        AND old_root_fingerprint ~ '^[0-9a-f]{64}$'
        AND new_root_fingerprint ~ '^[0-9a-f]{64}$'
        AND old_root_fingerprint<>new_root_fingerprint
        AND old_binding_version>0
        AND new_binding_version=old_binding_version+1
        AND old_workspace_version>0
        AND new_workspace_version=old_workspace_version+1
        AND reason='WORKSPACE_ROOT_IDENTITY_CHANGED'
    )
);

CREATE INDEX idx_workspace_root_binding_history_workspace
    ON ops.workspace_root_binding_history(workspace_id,created_at DESC,id DESC);

CREATE FUNCTION ops.enforce_workspace_root_binding_history()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP IN ('UPDATE','DELETE','TRUNCATE') THEN
        RAISE EXCEPTION 'workspace root binding history is append-only' USING ERRCODE='55000';
    END IF;
    IF NEW.transaction_id<>pg_current_xact_id() THEN
        RAISE EXCEPTION 'workspace root binding history must be created in the binding transaction'
            USING ERRCODE='55000';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.workspace_id::text,0));
    PERFORM pg_advisory_xact_lock(hashtextextended(LEAST(NEW.old_root_fingerprint,NEW.new_root_fingerprint),0));
    PERFORM pg_advisory_xact_lock(hashtextextended(GREATEST(NEW.old_root_fingerprint,NEW.new_root_fingerprint),0));
    PERFORM 1 FROM ops.workspace_control_state WHERE singleton=true FOR UPDATE;
    IF EXISTS (
        SELECT 1
        FROM ops.workspace_control_state AS state
        WHERE state.singleton=true
          AND (state.active_workspace_id IS NOT NULL
            OR state.resume_workspace_id IS NOT NULL
            OR state.operation_id IS NOT NULL
            OR state.operation_phase IS NOT NULL
            OR state.target_workspace_id IS NOT NULL
            OR state.previous_active_workspace_id IS NOT NULL
            OR state.controller_lease_owner_id IS NOT NULL
            OR state.controller_lease_expires_at IS NOT NULL)
    ) THEN
        RAISE EXCEPTION 'workspace root binding cannot change while control state retains a binding'
            USING ERRCODE='55P03';
    END IF;
    PERFORM 1 FROM ops.runtime_mutation_gate WHERE singleton=true FOR UPDATE;
    IF EXISTS (
        SELECT 1 FROM ops.runtime_mutation_gate
        WHERE singleton=true AND owner_id IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'workspace root binding cannot change while the runtime mutation gate is held'
            USING ERRCODE='55P03';
    END IF;
    PERFORM 1 FROM core.workspace WHERE id=NEW.workspace_id FOR UPDATE;
    IF EXISTS (
        SELECT 1 FROM core.workspace_git_capture_checkpoint
        WHERE workspace_id=NEW.workspace_id
    ) THEN
        RAISE EXCEPTION 'workspace Git capture lineage requires explicit rebaseline'
            USING ERRCODE='55000';
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM core.workspace AS workspace
        WHERE workspace.id=NEW.workspace_id
          AND workspace.root_path=NEW.canonical_root
          AND workspace.git_repository_path=NEW.canonical_root
          AND workspace.root_fingerprint=NEW.old_root_fingerprint
          AND workspace.binding_version=NEW.old_binding_version
          AND workspace.version=NEW.old_workspace_version
          AND workspace.status='inactive'
          AND workspace.availability='migration_required'
          AND workspace.availability_reason='WORKSPACE_ROOT_IDENTITY_CHANGED'
          AND workspace.removed_at IS NULL
    ) THEN
        RAISE EXCEPTION 'workspace root binding history does not match the migration-required identity'
            USING ERRCODE='55000';
    END IF;
    PERFORM 1 FROM ops.workspace_runtime
        WHERE workspace_id=NEW.workspace_id ORDER BY role FOR UPDATE;
    IF EXISTS (
        SELECT 1 FROM ops.workspace_runtime
        WHERE workspace_id=NEW.workspace_id AND phase<>'unavailable'
    ) THEN
        RAISE EXCEPTION 'workspace root binding cannot change while a runtime remains available'
            USING ERRCODE='55P03';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER workspace_root_binding_history_guard
    BEFORE INSERT OR UPDATE OR DELETE ON ops.workspace_root_binding_history
    FOR EACH ROW EXECUTE FUNCTION ops.enforce_workspace_root_binding_history();

CREATE TRIGGER workspace_root_binding_history_reject_truncate
    BEFORE TRUNCATE ON ops.workspace_root_binding_history
    FOR EACH STATEMENT EXECUTE FUNCTION ops.enforce_workspace_root_binding_history();

CREATE FUNCTION ops.enforce_workspace_root_binding_history_commit()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM core.workspace AS workspace
        WHERE workspace.id=NEW.workspace_id
          AND workspace.root_path=NEW.canonical_root
          AND workspace.git_repository_path=NEW.canonical_root
          AND workspace.root_fingerprint=NEW.new_root_fingerprint
          AND workspace.binding_version=NEW.new_binding_version
          AND workspace.version=NEW.new_workspace_version
          AND workspace.status='inactive'
          AND workspace.availability='available'
          AND workspace.availability_reason IS NULL
          AND workspace.removed_at IS NULL
    ) THEN
        RAISE EXCEPTION 'workspace root binding history was not consumed by its registry transition'
            USING ERRCODE='55000';
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM ops.audit_event AS audit
        WHERE audit.id=NEW.id
          AND audit.workspace_id=NEW.workspace_id
          AND audit.actor_type='SYSTEM'
          AND audit.actor_ref=NEW.controller_instance_id::text
          AND audit.action='workspace.root.rebound'
          AND audit.resource_type='workspace_root_binding'
          AND audit.resource_ref='workspace_root_binding:'||NEW.id::text
          AND audit.idempotency_key='workspace.root.rebind:'||NEW.id::text
          AND audit.correlation=jsonb_build_object('workspace_binding_history_id',NEW.id::text)
          AND audit.payload @> jsonb_build_object(
            'old_root_fingerprint',NEW.old_root_fingerprint,
            'new_root_fingerprint',NEW.new_root_fingerprint,
            'old_binding_version',NEW.old_binding_version,
            'new_binding_version',NEW.new_binding_version,
            'old_workspace_version',NEW.old_workspace_version,
            'new_workspace_version',NEW.new_workspace_version,
            'reason',NEW.reason,
            'audit_outcome','SUCCEEDED',
            'audit_schema_version','audit-event/v1'
          )
          AND audit.occurred_at=NEW.created_at
          AND audit.transaction_id=NEW.transaction_id
    ) THEN
        RAISE EXCEPTION 'workspace root binding history has no exact Audit event'
            USING ERRCODE='55000';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER workspace_root_binding_history_commit_guard
    AFTER INSERT ON ops.workspace_root_binding_history
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ops.enforce_workspace_root_binding_history_commit();

CREATE OR REPLACE FUNCTION core.enforce_workspace_registry()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP IN ('DELETE','TRUNCATE') THEN
        RAISE EXCEPTION 'workspace registry identities must be soft removed' USING ERRCODE='55000';
    END IF;
    IF TG_OP='INSERT' THEN
        RETURN NEW;
    END IF;
    IF ROW(NEW.root_path,NEW.git_repository_path,NEW.root_fingerprint,NEW.binding_version)
       IS DISTINCT FROM ROW(OLD.root_path,OLD.git_repository_path,OLD.root_fingerprint,OLD.binding_version) THEN
        IF ROW(NEW.id,NEW.name,NEW.root_path,NEW.git_repository_path,
               NEW.git_branch,NEW.git_head,NEW.git_dirty,NEW.git_checked_at,
               NEW.status,NEW.last_opened_at,NEW.removed_at,NEW.created_at)
           IS DISTINCT FROM
           ROW(OLD.id,OLD.name,OLD.root_path,OLD.git_repository_path,
               OLD.git_branch,OLD.git_head,OLD.git_dirty,OLD.git_checked_at,
               OLD.status,OLD.last_opened_at,OLD.removed_at,OLD.created_at)
           OR OLD.status<>'inactive'
           OR OLD.availability<>'migration_required'
           OR OLD.availability_reason<>'WORKSPACE_ROOT_IDENTITY_CHANGED'
           OR OLD.removed_at IS NOT NULL
           OR NEW.root_path<>OLD.root_path
           OR NEW.git_repository_path<>OLD.git_repository_path
           OR NEW.root_fingerprint=OLD.root_fingerprint
           OR NEW.binding_version<>OLD.binding_version+1
           OR NEW.availability<>'available'
           OR NEW.availability_reason IS NOT NULL
           OR NEW.availability_checked_at IS NULL
           OR NEW.availability_checked_at<OLD.availability_checked_at
           OR NEW.version<>OLD.version+1
           OR NEW.updated_at<OLD.updated_at
           OR NOT EXISTS (
                SELECT 1
                FROM ops.workspace_root_binding_history AS history
                WHERE history.workspace_id=OLD.id
                  AND history.canonical_root=OLD.root_path
                  AND history.old_root_fingerprint=OLD.root_fingerprint
                  AND history.new_root_fingerprint=NEW.root_fingerprint
                  AND history.old_binding_version=OLD.binding_version
                  AND history.new_binding_version=NEW.binding_version
                  AND history.old_workspace_version=OLD.version
                  AND history.new_workspace_version=NEW.version
                  AND history.reason='WORKSPACE_ROOT_IDENTITY_CHANGED'
                  AND history.transaction_id=pg_current_xact_id()
           ) THEN
            RAISE EXCEPTION 'workspace root binding is immutable' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'workspace registry version is invalid' USING ERRCODE='40001';
    END IF;
    IF OLD.removed_at IS NOT NULL AND NEW.removed_at IS NULL
       AND NEW.availability<>'available' THEN
        RAISE EXCEPTION 'removed workspace can only be restored after availability verification' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;
