-- +goose Up

-- M4-B extends the M4-A runtime schema without rewriting any historical
-- migration.  Existing terminal rows remain readable; active rows without
-- runtime identity are rejected by the application as legacy data.
ALTER TABLE workflow.run
    ADD COLUMN IF NOT EXISTS pause_requested_at timestamptz,
    ADD COLUMN IF NOT EXISTS cancel_requested_at timestamptz;

ALTER TABLE workflow.run
    DROP CONSTRAINT IF EXISTS run_status_check,
    DROP CONSTRAINT IF EXISTS workflow_run_status_check,
    ADD CONSTRAINT workflow_run_status_check CHECK (
        status IN ('pending', 'running', 'waiting_for_human', 'retry_wait', 'paused', 'succeeded', 'failed', 'cancelled')
    ),
    DROP CONSTRAINT IF EXISTS workflow_run_completion_consistent,
    ADD CONSTRAINT workflow_run_completion_consistent CHECK (
        (status IN ('succeeded', 'failed', 'cancelled') AND completed_at IS NOT NULL)
        OR (status NOT IN ('succeeded', 'failed', 'cancelled') AND completed_at IS NULL)
    ),
    ADD CONSTRAINT workflow_run_control_requests_consistent CHECK (
        NOT (status IN ('succeeded', 'failed') AND (pause_requested_at IS NOT NULL OR cancel_requested_at IS NOT NULL))
        AND NOT (status = 'cancelled' AND pause_requested_at IS NOT NULL)
    );

ALTER TABLE workflow.node_run
    ADD COLUMN IF NOT EXISTS retry_no integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz,
    ADD COLUMN IF NOT EXISTS failure_class text,
    ADD COLUMN IF NOT EXISTS error_kind text,
    ADD COLUMN IF NOT EXISTS error_code text,
    ADD COLUMN IF NOT EXISTS error_summary text;

ALTER TABLE workflow.node_run
    DROP CONSTRAINT IF EXISTS node_run_status_check,
    DROP CONSTRAINT IF EXISTS workflow_node_status_check,
    DROP CONSTRAINT IF EXISTS workflow_node_running_has_lease,
    DROP CONSTRAINT IF EXISTS node_run_running_has_lease,
    DROP CONSTRAINT IF EXISTS workflow_node_completion_consistent;

ALTER TABLE workflow.node_run
    ADD CONSTRAINT workflow_node_status_check CHECK (
        status IN ('pending', 'running', 'waiting_for_human', 'retry_wait', 'paused', 'succeeded', 'failed', 'cancelled')
    ),
    ADD CONSTRAINT workflow_node_completion_consistent CHECK (
        (status IN ('succeeded', 'failed', 'cancelled') AND completed_at IS NOT NULL)
        OR (status NOT IN ('succeeded', 'failed', 'cancelled') AND completed_at IS NULL)
    ),
    ADD CONSTRAINT workflow_node_runtime_running_has_lease CHECK (status <> 'running' OR lease_until IS NOT NULL),
    ADD CONSTRAINT workflow_node_retry_no_valid CHECK (retry_no >= 0),
    ADD CONSTRAINT workflow_node_failure_class_valid CHECK (
        failure_class IS NULL OR failure_class IN ('retryable', 'non_retryable', 'manual_recovery', 'lease_lost', 'cancelled')
    ),
    ADD CONSTRAINT workflow_node_failure_summary_bounded CHECK (error_summary IS NULL OR char_length(error_summary) <= 2048),
    ADD CONSTRAINT workflow_node_retry_schedule_consistent CHECK (
        (status = 'retry_wait' AND next_attempt_at IS NOT NULL)
        OR (status <> 'retry_wait' AND next_attempt_at IS NULL)
    );

CREATE INDEX IF NOT EXISTS idx_workflow_node_runtime_claim
    ON workflow.node_run (status, next_attempt_at, lease_until, created_at, id)
    WHERE status IN ('pending', 'retry_wait', 'running');

CREATE INDEX IF NOT EXISTS idx_workflow_node_failure
    ON workflow.node_run (run_id, failure_class, error_code, updated_at DESC, id)
    WHERE failure_class IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_workflow_run_control
    ON workflow.run (status, pause_requested_at, cancel_requested_at, updated_at DESC, id);

-- M4-A froze the initial dispatch identity. M4-B extends that contract to
-- permit exactly one monotonic generation increment per transactional enqueue.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION workflow.guard_node_runtime_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.input_schema_version IS DISTINCT FROM OLD.input_schema_version
       OR NEW.output_schema_version IS DISTINCT FROM OLD.output_schema_version
       OR NEW.dispatch_no IS NULL OR OLD.dispatch_no IS NULL
       OR NEW.dispatch_no < OLD.dispatch_no OR NEW.dispatch_no > OLD.dispatch_no + 1 THEN
        RAISE EXCEPTION 'workflow node runtime identity or dispatch generation is invalid' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS workflow.node_attempt (
    id uuid PRIMARY KEY,
    node_run_id uuid NOT NULL REFERENCES workflow.node_run(id) ON DELETE RESTRICT,
    attempt_no integer NOT NULL CHECK (attempt_no > 0),
    dispatch_no integer NOT NULL CHECK (dispatch_no > 0),
    retry_no integer NOT NULL CHECK (retry_no >= 0),
    river_job_id bigint,
    river_job_attempt integer CHECK (river_job_attempt IS NULL OR river_job_attempt > 0),
    delivery_id text NOT NULL CHECK (btrim(delivery_id) <> '' AND char_length(delivery_id) <= 256),
    lease_owner text,
    lease_until timestamptz,
    status text NOT NULL CHECK (status IN ('running', 'succeeded', 'waiting_for_human', 'retry_scheduled', 'failed', 'manual_recovery', 'lease_lost', 'cancelled')),
    output_schema_version integer CHECK (output_schema_version IS NULL OR output_schema_version > 0),
    output_hash text CHECK (output_hash IS NULL OR output_hash ~ '^[0-9a-f]{64}$'),
    failure_class text CHECK (failure_class IS NULL OR failure_class IN ('retryable', 'non_retryable', 'manual_recovery', 'lease_lost', 'cancelled')),
    error_kind text,
    error_code text,
    error_summary text CHECK (error_summary IS NULL OR char_length(error_summary) <= 2048),
    next_attempt_at timestamptz,
    started_at timestamptz NOT NULL,
    heartbeat_at timestamptz,
    ended_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT workflow_node_attempt_lease_pair CHECK ((lease_owner IS NULL) = (lease_until IS NULL)),
    CONSTRAINT workflow_node_attempt_status_times CHECK (
        (status = 'running' AND ended_at IS NULL)
        OR (status <> 'running' AND ended_at IS NOT NULL)
    ),
    CONSTRAINT workflow_node_attempt_running_has_lease CHECK (status <> 'running' OR lease_until IS NOT NULL),
    CONSTRAINT workflow_node_attempt_terminal_lease_cleared CHECK (
        status = 'running' OR (lease_owner IS NULL AND lease_until IS NULL)
    ),
    CONSTRAINT workflow_node_attempt_failure_consistent CHECK (
        (status IN ('failed', 'manual_recovery', 'lease_lost', 'cancelled', 'retry_scheduled') AND failure_class IS NOT NULL)
        OR (status IN ('running', 'succeeded', 'waiting_for_human') AND failure_class IS NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_workflow_node_attempt_no
    ON workflow.node_attempt (node_run_id, attempt_no);

CREATE UNIQUE INDEX IF NOT EXISTS uq_workflow_node_attempt_delivery
    ON workflow.node_attempt (node_run_id, dispatch_no, delivery_id)
    WHERE status = 'running';

CREATE UNIQUE INDEX IF NOT EXISTS uq_workflow_node_attempt_active
    ON workflow.node_attempt (node_run_id)
    WHERE status = 'running';

CREATE INDEX IF NOT EXISTS idx_workflow_node_attempt_history
    ON workflow.node_attempt (node_run_id, attempt_no DESC, id);

CREATE INDEX IF NOT EXISTS idx_workflow_node_attempt_lease
    ON workflow.node_attempt (status, lease_until, node_run_id)
    WHERE status = 'running';

-- Attempt rows are append-only facts.  Corrections must be represented by a
-- new domain event rather than mutating a completed attempt.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION workflow.reject_node_attempt_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status <> 'running'
       OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id
       OR NEW.attempt_no IS DISTINCT FROM OLD.attempt_no
       OR NEW.dispatch_no IS DISTINCT FROM OLD.dispatch_no
       OR NEW.retry_no IS DISTINCT FROM OLD.retry_no
       OR NEW.river_job_id IS DISTINCT FROM OLD.river_job_id
       OR NEW.river_job_attempt IS DISTINCT FROM OLD.river_job_attempt
       OR NEW.delivery_id IS DISTINCT FROM OLD.delivery_id
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
    THEN
        RAISE EXCEPTION 'workflow node attempt is append-only' USING ERRCODE = '55000';
    END IF;
    IF OLD.status = 'running' AND NEW.status = 'running' THEN
        IF NEW.lease_owner IS DISTINCT FROM OLD.lease_owner
           OR NEW.river_job_id IS DISTINCT FROM OLD.river_job_id
           OR NEW.river_job_attempt IS DISTINCT FROM OLD.river_job_attempt
           OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id
           OR NEW.attempt_no IS DISTINCT FROM OLD.attempt_no
           OR NEW.dispatch_no IS DISTINCT FROM OLD.dispatch_no
           OR NEW.retry_no IS DISTINCT FROM OLD.retry_no
           OR NEW.delivery_id IS DISTINCT FROM OLD.delivery_id
           OR NEW.started_at IS DISTINCT FROM OLD.started_at
           OR NEW.output_schema_version IS DISTINCT FROM OLD.output_schema_version
           OR NEW.output_hash IS DISTINCT FROM OLD.output_hash
           OR NEW.failure_class IS DISTINCT FROM OLD.failure_class
           OR NEW.error_kind IS DISTINCT FROM OLD.error_kind
           OR NEW.error_code IS DISTINCT FROM OLD.error_code
           OR NEW.error_summary IS DISTINCT FROM OLD.error_summary
           OR NEW.next_attempt_at IS DISTINCT FROM OLD.next_attempt_at
           OR NEW.ended_at IS DISTINCT FROM OLD.ended_at
           OR NEW.created_at IS DISTINCT FROM OLD.created_at
        THEN
            RAISE EXCEPTION 'workflow running attempt identity is immutable' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.status = 'running' OR NEW.ended_at IS NULL THEN
        RAISE EXCEPTION 'workflow node attempt may only transition running to a terminal status' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS workflow_node_attempt_reject_update_delete ON workflow.node_attempt;
CREATE TRIGGER workflow_node_attempt_reject_update_delete
    BEFORE UPDATE OR DELETE ON workflow.node_attempt
    FOR EACH ROW EXECUTE FUNCTION workflow.reject_node_attempt_mutation();

CREATE TABLE IF NOT EXISTS workflow.control_command (
    id uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES workflow.run(id) ON DELETE RESTRICT,
    command text NOT NULL CHECK (command IN ('pause', 'resume', 'cancel')),
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key) <> '' AND char_length(idempotency_key) <= 128),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    expected_version bigint NOT NULL CHECK (expected_version > 0),
    result jsonb NOT NULL CHECK (jsonb_typeof(result) = 'object'),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at timestamptz,
    CONSTRAINT workflow_control_command_completion_consistent CHECK (completed_at IS NULL OR completed_at >= created_at),
    CONSTRAINT uq_workflow_control_command_identity UNIQUE (run_id, command, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_workflow_control_command_run
    ON workflow.control_command (run_id, created_at DESC, id);

CREATE INDEX IF NOT EXISTS idx_workflow_control_command_hash
    ON workflow.control_command (run_id, request_hash);

INSERT INTO core.schema_meta (key, value)
VALUES ('workflow_runtime_state_machine', 'm4-b')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    LOCK TABLE workflow.control_command, workflow.node_attempt, workflow.node_run, workflow.run IN ACCESS EXCLUSIVE MODE;

    IF EXISTS (SELECT 1 FROM workflow.node_attempt)
       OR EXISTS (SELECT 1 FROM workflow.control_command)
       OR EXISTS (SELECT 1 FROM workflow.run WHERE pause_requested_at IS NOT NULL OR cancel_requested_at IS NOT NULL OR status IN ('retry_wait', 'paused'))
       OR EXISTS (SELECT 1 FROM workflow.node_run WHERE status IN ('retry_wait', 'paused') OR retry_no <> 0 OR next_attempt_at IS NOT NULL OR failure_class IS NOT NULL OR error_kind IS NOT NULL OR error_code IS NOT NULL OR error_summary IS NOT NULL)
    THEN
        RAISE EXCEPTION 'cannot downgrade Workflow Runtime State Machine while runtime state exists' USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DELETE FROM core.schema_meta WHERE key = 'workflow_runtime_state_machine';

-- Restore the M4-A immutable initial dispatch contract after a safe Down.
-- +goose StatementBegin
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
-- +goose StatementEnd

DROP TRIGGER IF EXISTS workflow_node_attempt_reject_update_delete ON workflow.node_attempt;
DROP FUNCTION IF EXISTS workflow.reject_node_attempt_mutation();
DROP TABLE IF EXISTS workflow.control_command;
DROP TABLE IF EXISTS workflow.node_attempt;

DROP INDEX IF EXISTS workflow.idx_workflow_run_control;
DROP INDEX IF EXISTS workflow.idx_workflow_node_failure;
DROP INDEX IF EXISTS workflow.idx_workflow_node_runtime_claim;

ALTER TABLE workflow.node_run
    DROP CONSTRAINT IF EXISTS workflow_node_retry_schedule_consistent,
    DROP CONSTRAINT IF EXISTS workflow_node_failure_summary_bounded,
    DROP CONSTRAINT IF EXISTS workflow_node_failure_class_valid,
    DROP CONSTRAINT IF EXISTS workflow_node_retry_no_valid,
    DROP CONSTRAINT IF EXISTS workflow_node_completion_consistent,
    DROP CONSTRAINT IF EXISTS node_run_status_check,
    DROP CONSTRAINT IF EXISTS workflow_node_status_check,
    DROP CONSTRAINT IF EXISTS workflow_node_runtime_running_has_lease,
    DROP CONSTRAINT IF EXISTS workflow_node_running_has_lease,
    DROP CONSTRAINT IF EXISTS node_run_running_has_lease,
    DROP COLUMN IF EXISTS error_summary,
    DROP COLUMN IF EXISTS error_code,
    DROP COLUMN IF EXISTS error_kind,
    DROP COLUMN IF EXISTS failure_class,
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS retry_no;

ALTER TABLE workflow.node_run
    ADD CONSTRAINT workflow_node_completion_consistent CHECK (
        (status IN ('succeeded', 'failed', 'cancelled') AND completed_at IS NOT NULL)
        OR (status NOT IN ('succeeded', 'failed', 'cancelled') AND completed_at IS NULL)
    ),
    ADD CONSTRAINT workflow_node_running_has_lease CHECK (status <> 'running' OR lease_until IS NOT NULL),
    ADD CONSTRAINT workflow_node_status_check CHECK (
        status IN ('pending', 'running', 'waiting_for_human', 'succeeded', 'failed', 'cancelled')
    );

ALTER TABLE workflow.run
    DROP CONSTRAINT IF EXISTS workflow_run_control_requests_consistent,
    DROP CONSTRAINT IF EXISTS workflow_run_completion_consistent,
    DROP CONSTRAINT IF EXISTS run_status_check,
    DROP CONSTRAINT IF EXISTS workflow_run_status_check,
    DROP COLUMN IF EXISTS cancel_requested_at,
    DROP COLUMN IF EXISTS pause_requested_at;

ALTER TABLE workflow.run
    ADD CONSTRAINT workflow_run_completion_consistent CHECK (
        (status IN ('succeeded', 'failed', 'cancelled') AND completed_at IS NOT NULL)
        OR (status NOT IN ('succeeded', 'failed', 'cancelled') AND completed_at IS NULL)
    ),
    ADD CONSTRAINT workflow_run_status_check CHECK (
        status IN ('pending', 'running', 'waiting_for_human', 'succeeded', 'failed', 'cancelled')
    );
