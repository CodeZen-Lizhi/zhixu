ALTER TABLE core.source ADD COLUMN IF NOT EXISTS removed_at timestamptz;

CREATE INDEX IF NOT EXISTS idx_source_workspace_active_id
    ON core.source(workspace_id,id) WHERE removed_at IS NULL;

CREATE TABLE IF NOT EXISTS core.workspace_git_capture_checkpoint (
    workspace_id uuid PRIMARY KEY REFERENCES core.workspace(id) ON DELETE RESTRICT,
    completed_head_oid text NOT NULL CHECK (completed_head_oid ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    inflight_run_id uuid,
    inflight_before_oid text,
    inflight_after_oid text,
    inflight_request_hash text,
    last_run_id uuid,
    last_before_oid text,
    last_after_oid text,
    last_request_hash text,
    version bigint NOT NULL CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT git_capture_checkpoint_inflight_shape CHECK (
        (inflight_run_id IS NULL AND inflight_before_oid IS NULL AND inflight_after_oid IS NULL AND inflight_request_hash IS NULL)
        OR (
            inflight_run_id IS NOT NULL
            AND inflight_before_oid ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'
            AND inflight_after_oid ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'
            AND inflight_before_oid<>inflight_after_oid
            AND inflight_request_hash ~ '^[0-9a-f]{64}$'
            AND completed_head_oid=inflight_before_oid
        )
    ),
    CONSTRAINT git_capture_checkpoint_last_shape CHECK (
        (last_run_id IS NULL AND last_before_oid IS NULL AND last_after_oid IS NULL AND last_request_hash IS NULL)
        OR (
            last_run_id IS NOT NULL
            AND last_before_oid ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'
            AND last_after_oid ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'
            AND last_before_oid<>last_after_oid
            AND last_request_hash ~ '^[0-9a-f]{64}$'
            AND completed_head_oid=last_after_oid
        )
    ),
    CONSTRAINT fk_git_capture_checkpoint_inflight_run
        FOREIGN KEY (inflight_run_id,workspace_id) REFERENCES ops.git_sync_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_git_capture_checkpoint_last_run
        FOREIGN KEY (last_run_id,workspace_id) REFERENCES ops.git_sync_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT git_capture_checkpoint_timestamps CHECK (updated_at>=created_at)
);

CREATE OR REPLACE FUNCTION core.validate_workspace_git_capture_checkpoint_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.workspace_id<>OLD.workspace_id OR NEW.created_at<>OLD.created_at OR NEW.version<>OLD.version+1 THEN
        RAISE EXCEPTION 'Git capture checkpoint immutable binding or version changed' USING ERRCODE='55000';
    END IF;
    IF OLD.inflight_run_id IS NULL THEN
        IF NEW.completed_head_oid<>OLD.completed_head_oid OR NEW.inflight_run_id IS NULL
           OR NEW.last_run_id IS DISTINCT FROM OLD.last_run_id
           OR NEW.last_before_oid IS DISTINCT FROM OLD.last_before_oid
           OR NEW.last_after_oid IS DISTINCT FROM OLD.last_after_oid
           OR NEW.last_request_hash IS DISTINCT FROM OLD.last_request_hash THEN
            RAISE EXCEPTION 'Git capture checkpoint begin transition is invalid' USING ERRCODE='55000';
        END IF;
    ELSE
        IF NEW.inflight_run_id IS NOT NULL OR NEW.completed_head_oid<>OLD.inflight_after_oid
           OR NEW.last_run_id<>OLD.inflight_run_id OR NEW.last_before_oid<>OLD.inflight_before_oid
           OR NEW.last_after_oid<>OLD.inflight_after_oid OR NEW.last_request_hash<>OLD.inflight_request_hash THEN
            RAISE EXCEPTION 'Git capture checkpoint completion transition is invalid' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS workspace_git_capture_checkpoint_update_guard ON core.workspace_git_capture_checkpoint;
CREATE TRIGGER workspace_git_capture_checkpoint_update_guard
    BEFORE UPDATE ON core.workspace_git_capture_checkpoint
    FOR EACH ROW EXECUTE FUNCTION core.validate_workspace_git_capture_checkpoint_update();
