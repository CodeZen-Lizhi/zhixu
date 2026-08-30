ALTER TABLE workflow.node_attempt
    ADD COLUMN model_settings_revision bigint,
    ADD COLUMN model_runtime_instance_id uuid,
    ADD CONSTRAINT workflow_node_attempt_model_runtime_binding CHECK ((
        (model_settings_revision IS NULL AND model_runtime_instance_id IS NULL)
        OR (model_settings_revision IS NOT NULL AND model_settings_revision >= 0
            AND model_runtime_instance_id IS NOT NULL)
    ) IS TRUE);

-- A managed revision of zero is the canonical disabled runtime and has no
-- revision row. Positive revisions must point at immutable settings history.
CREATE FUNCTION workflow.enforce_node_attempt_model_runtime_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    worker_instance_id uuid;
    worker_revision bigint;
    worker_rollout_id uuid;
    worker_phase text;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.model_settings_revision IS DISTINCT FROM OLD.model_settings_revision
           OR NEW.model_runtime_instance_id IS DISTINCT FROM OLD.model_runtime_instance_id THEN
            RAISE EXCEPTION 'workflow node attempt model runtime binding is immutable'
                USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;

    -- Static deployments intentionally persist no runtime provenance.
    IF NEW.model_settings_revision IS NULL THEN
        RETURN NEW;
    END IF;

    IF NOT ops.model_settings_revision_exists(NEW.model_settings_revision) THEN
        RAISE EXCEPTION 'workflow node attempt references an unknown model settings revision'
            USING ERRCODE = '23503';
    END IF;

    -- Defense in depth only: the authoritative Claim transaction locks the
    -- singleton state before this runtime row to enforce the rollout phase.
    SELECT runtime.instance_id,
           runtime.applied_revision,
           runtime.rollout_id,
           runtime.phase
      INTO worker_instance_id,
           worker_revision,
           worker_rollout_id,
           worker_phase
      FROM ops.model_settings_runtime AS runtime
     WHERE runtime.role = 'worker'
     FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'workflow node attempt requires an active worker model runtime'
            USING ERRCODE = '23503';
    END IF;
    IF NEW.model_runtime_instance_id IS DISTINCT FROM worker_instance_id
       OR NEW.model_settings_revision IS DISTINCT FROM worker_revision
       OR worker_rollout_id IS NOT NULL
       OR worker_phase <> 'active' THEN
        RAISE EXCEPTION 'workflow node attempt model runtime binding does not match the active worker runtime'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER workflow_node_attempt_model_runtime_binding_guard
    BEFORE INSERT OR UPDATE ON workflow.node_attempt
    FOR EACH ROW EXECUTE FUNCTION workflow.enforce_node_attempt_model_runtime_binding();
