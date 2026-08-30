CREATE SCHEMA IF NOT EXISTS workflow;

CREATE TABLE IF NOT EXISTS workflow.definition (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    key text NOT NULL CHECK (btrim(key) <> ''),
    version bigint NOT NULL CHECK (version > 0),
    graph jsonb NOT NULL CHECK (jsonb_typeof(graph) = 'object'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_workflow_definition_key_version UNIQUE (workspace_id, key, version)
);

CREATE TABLE IF NOT EXISTS workflow.run (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    definition_id uuid NOT NULL REFERENCES workflow.definition(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN ('pending', 'running', 'waiting_for_human', 'succeeded', 'failed', 'cancelled')),
    input jsonb NOT NULL DEFAULT '{}'::jsonb,
    output jsonb,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT workflow_run_updated_after_created CHECK (updated_at >= created_at),
    CONSTRAINT workflow_run_completion_consistent CHECK (
        (status IN ('succeeded', 'failed', 'cancelled') AND completed_at IS NOT NULL)
        OR (status NOT IN ('succeeded', 'failed', 'cancelled') AND completed_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_workflow_run_workspace_created
    ON workflow.run (workspace_id, created_at DESC, id);

CREATE TABLE IF NOT EXISTS workflow.node_run (
    id uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES workflow.run(id) ON DELETE RESTRICT,
    node_key text NOT NULL CHECK (btrim(node_key) <> ''),
    node_type text NOT NULL CHECK (btrim(node_type) <> ''),
    status text NOT NULL CHECK (status IN ('pending', 'running', 'waiting_for_human', 'succeeded', 'failed', 'cancelled')),
    attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    input jsonb NOT NULL DEFAULT '{}'::jsonb,
    output jsonb,
    lease_owner text,
    lease_until timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT uq_workflow_node_run_key UNIQUE (run_id, node_key),
    CONSTRAINT workflow_node_lease_pair CHECK ((lease_owner IS NULL) = (lease_until IS NULL)),
    CONSTRAINT workflow_node_running_has_lease CHECK (status <> 'running' OR lease_until IS NOT NULL),
    CONSTRAINT workflow_node_completion_consistent CHECK (
        (status IN ('succeeded', 'failed', 'cancelled') AND completed_at IS NOT NULL)
        OR (status NOT IN ('succeeded', 'failed', 'cancelled') AND completed_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_workflow_node_claim
    ON workflow.node_run (status, lease_until, created_at, id);

CREATE TABLE IF NOT EXISTS workflow.human_task (
    id uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES workflow.run(id) ON DELETE RESTRICT,
    node_run_id uuid NOT NULL UNIQUE REFERENCES workflow.node_run(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN ('pending', 'submitted', 'expired', 'cancelled')),
    expected_input_schema jsonb NOT NULL DEFAULT '{}'::jsonb,
    target_version bigint NOT NULL CHECK (target_version > 0),
    decision jsonb,
    expires_at timestamptz,
    submitted_at timestamptz,
    created_at timestamptz NOT NULL,
    CONSTRAINT workflow_human_submission_consistent CHECK (
        (status = 'submitted' AND decision IS NOT NULL AND submitted_at IS NOT NULL)
        OR (status <> 'submitted' AND decision IS NULL AND submitted_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_workflow_human_run_status
    ON workflow.human_task (run_id, status, created_at, id);

CREATE TABLE IF NOT EXISTS workflow.outbox_event (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    run_id uuid REFERENCES workflow.run(id) ON DELETE RESTRICT,
    event_type text NOT NULL CHECK (btrim(event_type) <> ''),
    idempotency_key text NOT NULL UNIQUE CHECK (btrim(idempotency_key) <> ''),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    occurred_at timestamptz NOT NULL,
    published_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_workflow_outbox_unpublished
    ON workflow.outbox_event (occurred_at, id) WHERE published_at IS NULL;

CREATE OR REPLACE FUNCTION workflow.reject_definition_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'workflow definition is immutable' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS workflow_definition_reject_update_delete ON workflow.definition;
CREATE TRIGGER workflow_definition_reject_update_delete
    BEFORE UPDATE OR DELETE ON workflow.definition
    FOR EACH ROW EXECUTE FUNCTION workflow.reject_definition_mutation();

INSERT INTO core.schema_meta (key, value) VALUES ('workflow', 'm4')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
