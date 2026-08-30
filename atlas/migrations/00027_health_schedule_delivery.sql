ALTER TABLE ops.health_schedule
    ADD COLUMN IF NOT EXISTS pending_due_at timestamptz,
    ADD COLUMN IF NOT EXISTS dispatch_lease_until timestamptz;

-- last_run_at records the logical due that was dispatched. A missed run may
-- legitimately predate the schedule row that first claimed it.
ALTER TABLE ops.health_schedule
    DROP CONSTRAINT IF EXISTS ops_health_schedule_time_order,
    ADD CONSTRAINT ops_health_schedule_time_order CHECK (
        updated_at >= created_at
        AND (next_run_at IS NULL OR last_run_at IS NULL OR next_run_at >= last_run_at)
    );

ALTER TABLE ops.health_schedule
    DROP CONSTRAINT IF EXISTS ops_health_schedule_dispatch_binding;
ALTER TABLE ops.health_schedule
    ADD CONSTRAINT ops_health_schedule_dispatch_binding CHECK (
        (pending_due_at IS NULL AND dispatch_lease_until IS NULL)
        OR (
            cadence <> 'DISABLED'
            AND pending_due_at IS NOT NULL
            AND dispatch_lease_until IS NOT NULL
            AND dispatch_lease_until >= pending_due_at
        )
    );

CREATE INDEX IF NOT EXISTS idx_ops_health_schedule_pending_lease
    ON ops.health_schedule (dispatch_lease_until, pending_due_at, id)
    WHERE pending_due_at IS NOT NULL;

CREATE OR REPLACE FUNCTION ops.validate_health_schedule_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NOT ops.validate_health_scan_scope(
            NEW.workspace_id,
            NEW.scope_type,
            NEW.scope_ref,
            NEW.scope_version,
            NEW.scope_hash
        ) THEN
            RAISE EXCEPTION 'health schedule scope binding is invalid' USING ERRCODE = '23514';
        END IF;
        IF NEW.version <> 1 THEN
            RAISE EXCEPTION 'health schedule must start at version one' USING ERRCODE = '23514';
        END IF;
        IF NEW.pending_due_at IS NOT NULL OR NEW.dispatch_lease_until IS NOT NULL THEN
            RAISE EXCEPTION 'health schedule cannot start with a pending delivery' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.scope_type <> OLD.scope_type
       OR NEW.scope_ref <> OLD.scope_ref
       OR NEW.scope_version <> OLD.scope_version
       OR NEW.scope_schema_version <> OLD.scope_schema_version
       OR NEW.scope_hash IS DISTINCT FROM OLD.scope_hash
       OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at
       OR (OLD.last_run_at IS NOT NULL AND NEW.last_run_at IS NULL)
       OR (OLD.last_run_at IS NOT NULL AND NEW.last_run_at < OLD.last_run_at)
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'health schedule immutable binding, time, or version changed' USING ERRCODE = '23514';
    END IF;

    IF OLD.pending_due_at IS NULL AND NEW.pending_due_at IS NOT NULL THEN
        IF OLD.next_run_at IS NULL
           OR NEW.pending_due_at IS DISTINCT FROM OLD.next_run_at
           OR NEW.next_run_at IS NULL
           OR NEW.next_run_at <= NEW.pending_due_at
           OR NEW.dispatch_lease_until <= NEW.updated_at
           OR NEW.last_run_at IS DISTINCT FROM OLD.last_run_at
           OR NEW.cadence IS DISTINCT FROM OLD.cadence
           OR NEW.cron_expression IS DISTINCT FROM OLD.cron_expression
           OR NEW.timezone IS DISTINCT FROM OLD.timezone
           OR NEW.max_items IS DISTINCT FROM OLD.max_items THEN
            RAISE EXCEPTION 'health schedule initial delivery claim is invalid' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.pending_due_at IS NOT NULL AND NEW.pending_due_at IS NOT NULL THEN
        IF NEW.pending_due_at IS DISTINCT FROM OLD.pending_due_at
           OR NEW.next_run_at IS DISTINCT FROM OLD.next_run_at
           OR NEW.last_run_at IS DISTINCT FROM OLD.last_run_at
           OR NEW.dispatch_lease_until < NEW.updated_at
           OR NEW.cadence IS DISTINCT FROM OLD.cadence
           OR NEW.cron_expression IS DISTINCT FROM OLD.cron_expression
           OR NEW.timezone IS DISTINCT FROM OLD.timezone
           OR NEW.max_items IS DISTINCT FROM OLD.max_items THEN
            RAISE EXCEPTION 'health schedule pending delivery mutation is invalid' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.pending_due_at IS NOT NULL AND NEW.pending_due_at IS NULL THEN
        IF NEW.dispatch_lease_until IS NOT NULL
           OR NEW.last_run_at IS DISTINCT FROM OLD.pending_due_at
           OR NEW.next_run_at IS DISTINCT FROM OLD.next_run_at
           OR NEW.cadence IS DISTINCT FROM OLD.cadence
           OR NEW.cron_expression IS DISTINCT FROM OLD.cron_expression
           OR NEW.timezone IS DISTINCT FROM OLD.timezone
           OR NEW.max_items IS DISTINCT FROM OLD.max_items THEN
            RAISE EXCEPTION 'health schedule delivery acknowledgement is invalid' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.dispatch_lease_until IS NOT NULL THEN
        RAISE EXCEPTION 'health schedule lease requires a pending due' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

INSERT INTO core.schema_meta (key, value)
VALUES ('health_schedule_delivery', 'm7-03')
ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now();
