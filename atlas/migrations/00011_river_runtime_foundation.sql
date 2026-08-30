-- M4-A 仅前向扩展 Runtime identity；历史行保持 NULL，交由新 Runtime 明确拒绝 active legacy。
ALTER TABLE workflow.run
    ADD COLUMN IF NOT EXISTS idempotency_key text,
    ADD COLUMN IF NOT EXISTS request_hash text;

ALTER TABLE workflow.run
    DROP CONSTRAINT IF EXISTS workflow_run_runtime_identity_pair,
    ADD CONSTRAINT workflow_run_runtime_identity_pair CHECK (
        (idempotency_key IS NULL AND request_hash IS NULL)
        OR (
            idempotency_key IS NOT NULL
            AND btrim(idempotency_key) <> ''
            AND char_length(idempotency_key) <= 128
            AND request_hash IS NOT NULL
            AND request_hash ~ '^[0-9a-f]{64}$'
        )
    );

CREATE UNIQUE INDEX IF NOT EXISTS uq_workflow_run_workspace_idempotency
    ON workflow.run (workspace_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

ALTER TABLE workflow.node_run
    ADD COLUMN IF NOT EXISTS idempotency_key text,
    ADD COLUMN IF NOT EXISTS input_schema_version integer,
    ADD COLUMN IF NOT EXISTS output_schema_version integer,
    ADD COLUMN IF NOT EXISTS dispatch_no integer;

ALTER TABLE workflow.node_run
    DROP CONSTRAINT IF EXISTS workflow_node_runtime_identity_complete,
    ADD CONSTRAINT workflow_node_runtime_identity_complete CHECK (
        (
            idempotency_key IS NULL
            AND input_schema_version IS NULL
            AND output_schema_version IS NULL
            AND dispatch_no IS NULL
        )
        OR (
            idempotency_key IS NOT NULL
            AND btrim(idempotency_key) <> ''
            AND char_length(idempotency_key) <= 128
            AND input_schema_version IS NOT NULL
            AND input_schema_version > 0
            AND output_schema_version IS NOT NULL
            AND output_schema_version > 0
            AND dispatch_no IS NOT NULL
            AND dispatch_no > 0
        )
    );

CREATE UNIQUE INDEX IF NOT EXISTS uq_workflow_node_run_idempotency
    ON workflow.node_run (run_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

ALTER TABLE workflow.outbox_event
    ADD COLUMN IF NOT EXISTS event_key text,
    ADD COLUMN IF NOT EXISTS schema_version integer,
    ADD COLUMN IF NOT EXISTS event_version bigint;

ALTER TABLE workflow.outbox_event
    DROP CONSTRAINT IF EXISTS workflow_outbox_runtime_identity_complete,
    ADD CONSTRAINT workflow_outbox_runtime_identity_complete CHECK (
        (
            event_key IS NULL
            AND schema_version IS NULL
            AND event_version IS NULL
        )
        OR (
            event_key IS NOT NULL
            AND btrim(event_key) <> ''
            AND char_length(event_key) <= 128
            AND schema_version IS NOT NULL
            AND schema_version > 0
            AND event_version IS NOT NULL
            AND event_version > 0
        )
    );

CREATE UNIQUE INDEX IF NOT EXISTS uq_workflow_outbox_event_identity
    ON workflow.outbox_event (workspace_id, event_key, event_version)
    WHERE event_key IS NOT NULL;

CREATE OR REPLACE FUNCTION workflow.guard_run_runtime_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.request_hash IS DISTINCT FROM OLD.request_hash THEN
        RAISE EXCEPTION 'workflow run runtime identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS workflow_run_guard_runtime_identity ON workflow.run;
CREATE TRIGGER workflow_run_guard_runtime_identity
BEFORE UPDATE ON workflow.run
FOR EACH ROW EXECUTE FUNCTION workflow.guard_run_runtime_identity_mutation();

CREATE OR REPLACE FUNCTION workflow.guard_node_runtime_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.input_schema_version IS DISTINCT FROM OLD.input_schema_version
       OR NEW.output_schema_version IS DISTINCT FROM OLD.output_schema_version
       OR NEW.dispatch_no IS DISTINCT FROM OLD.dispatch_no THEN
        RAISE EXCEPTION 'workflow node runtime identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS workflow_node_guard_runtime_identity ON workflow.node_run;
CREATE TRIGGER workflow_node_guard_runtime_identity
BEFORE UPDATE ON workflow.node_run
FOR EACH ROW EXECUTE FUNCTION workflow.guard_node_runtime_identity_mutation();

CREATE OR REPLACE FUNCTION workflow.guard_outbox_runtime_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.event_key IS DISTINCT FROM OLD.event_key
       OR NEW.schema_version IS DISTINCT FROM OLD.schema_version
       OR NEW.event_version IS DISTINCT FROM OLD.event_version THEN
        RAISE EXCEPTION 'workflow outbox runtime identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS workflow_outbox_guard_runtime_identity ON workflow.outbox_event;
CREATE TRIGGER workflow_outbox_guard_runtime_identity
BEFORE UPDATE ON workflow.outbox_event
FOR EACH ROW EXECUTE FUNCTION workflow.guard_outbox_runtime_identity_mutation();

INSERT INTO core.schema_meta (key, value)
VALUES ('river_runtime_foundation', 'm4-a')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
