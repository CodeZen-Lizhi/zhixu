-- +goose Up

CREATE TABLE IF NOT EXISTS workflow.tool_call (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    workflow_run_id uuid NOT NULL,
    node_run_id uuid NOT NULL,
    node_attempt_id uuid NOT NULL,
    call_no smallint NOT NULL CHECK (call_no > 0),
    requested_tool_name text NOT NULL CHECK (
        requested_tool_name ~ '^[A-Z][A-Za-z0-9]{0,63}$'
    ),
    tool_version bigint CHECK (tool_version IS NULL OR tool_version > 0),
    definition_hash text CHECK (
        definition_hash IS NULL OR definition_hash ~ '^[0-9a-f]{64}$'
    ),
    input_schema_id text CHECK (
        input_schema_id IS NULL OR input_schema_id ~ '^[a-z][a-z0-9_.-]{0,127}$'
    ),
    input_schema_version bigint CHECK (input_schema_version IS NULL OR input_schema_version > 0),
    output_schema_id text CHECK (
        output_schema_id IS NULL OR output_schema_id ~ '^[a-z][a-z0-9_.-]{0,127}$'
    ),
    output_schema_version bigint CHECK (output_schema_version IS NULL OR output_schema_version > 0),
    capability text CHECK (
        capability IS NULL OR capability IN (
            'READ_LOCAL',
            'READ_EXTERNAL',
            'WRITE_PROPOSAL',
            'WRITE_KNOWLEDGE',
            'GIT_WRITE',
            'INDEX_MAINTENANCE',
            'EVALUATION_RUN'
        )
    ),
    side_effect_level text CHECK (
        side_effect_level IS NULL OR side_effect_level IN (
            'NONE', 'EXTERNAL_READ', 'DOMAIN_WRITE', 'IRREVERSIBLE_OR_UNKNOWN'
        )
    ),
    invocation_policy text CHECK (
        invocation_policy IS NULL OR invocation_policy IN (
            'MODEL_REQUESTABLE', 'TRUSTED_WORKFLOW_ONLY'
        )
    ),
    idempotency_key text CHECK (
        idempotency_key IS NULL OR (
            idempotency_key = btrim(idempotency_key)
            AND idempotency_key <> ''
            AND octet_length(idempotency_key) <= 128
            AND idempotency_key !~ E'[\\r\\n]'
        )
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    request_bytes bigint NOT NULL CHECK (request_bytes > 0 AND request_bytes <= 16777216),
    request_summary jsonb NOT NULL CHECK (
        jsonb_typeof(request_summary) = 'object'
        AND octet_length(request_summary::text) <= 16384
    ),
    response_hash text CHECK (
        response_hash IS NULL OR response_hash ~ '^[0-9a-f]{64}$'
    ),
    response_bytes bigint NOT NULL DEFAULT 0 CHECK (
        response_bytes >= 0 AND response_bytes <= 16777216
    ),
    response_summary jsonb CHECK (
        response_summary IS NULL OR (
            jsonb_typeof(response_summary) = 'object'
            AND octet_length(response_summary::text) <= 16384
        )
    ),
    result_ref text CHECK (
        result_ref IS NULL OR (
            result_ref = btrim(result_ref)
            AND result_ref <> ''
            AND octet_length(result_ref) <= 512
            AND result_ref !~ E'[\\r\\n]'
            AND (
				result_ref ~ '^(index-version|source-span|writeback-execution|reindex-delivery|evaluation-run):[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
                OR result_ref ~ '^(citation-batch|web-content):[0-9a-f]{64}$'
                OR result_ref ~ '^git-head:([0-9a-f]{40}|[0-9a-f]{64})$'
                OR result_ref ~ '^diff:[0-9a-f]{64}:[0-9a-f]{64}$'
            )
        )
    ),
    side_effect_type text CHECK (
        side_effect_type IS NULL OR (
			side_effect_type IN ('writeback_execution','reindex_delivery','evaluation_run','web_fetch')
        )
    ),
    side_effect_id text CHECK (
        side_effect_id IS NULL OR (
            side_effect_id = btrim(side_effect_id)
			AND (
				(side_effect_type = 'web_fetch' AND side_effect_id ~ '^[0-9a-f]{64}$')
				OR (side_effect_type <> 'web_fetch' AND side_effect_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$')
			)
        )
    ),
    status text NOT NULL CHECK (
        status IN ('STARTED', 'SUCCEEDED', 'FAILED', 'REFUSED', 'UNKNOWN')
    ),
    error_code text CHECK (
        error_code IS NULL OR error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'
    ),
    retryable boolean NOT NULL DEFAULT false,
    started_at timestamptz NOT NULL,
    completed_at timestamptz,
    duration_ms bigint NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    CONSTRAINT uq_workflow_tool_call_attempt_no UNIQUE (node_attempt_id, call_no),
    CONSTRAINT fk_workflow_tool_call_run_workspace
        FOREIGN KEY (workflow_run_id, workspace_id)
        REFERENCES workflow.run(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workflow_tool_call_node_run
        FOREIGN KEY (node_run_id, workflow_run_id)
        REFERENCES workflow.node_run(id, run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workflow_tool_call_node_attempt
        FOREIGN KEY (node_attempt_id, node_run_id)
        REFERENCES workflow.node_attempt(id, node_run_id) ON DELETE RESTRICT,
    CONSTRAINT workflow_tool_call_contract_complete CHECK (
        (
            tool_version IS NULL
            AND definition_hash IS NULL
            AND input_schema_id IS NULL
            AND input_schema_version IS NULL
            AND output_schema_id IS NULL
            AND output_schema_version IS NULL
            AND capability IS NULL
            AND side_effect_level IS NULL
            AND invocation_policy IS NULL
            AND status = 'REFUSED'
        )
        OR (
            tool_version IS NOT NULL
            AND definition_hash IS NOT NULL
            AND input_schema_id IS NOT NULL
            AND input_schema_version IS NOT NULL
            AND output_schema_id IS NOT NULL
            AND output_schema_version IS NOT NULL
            AND side_effect_level IS NOT NULL
            AND invocation_policy IS NOT NULL
            AND (
                capability IS NOT NULL
                OR (requested_tool_name = 'CalculateDiff' AND side_effect_level = 'NONE')
            )
        )
    ),
    CONSTRAINT workflow_tool_call_side_effect_pair CHECK (
        (side_effect_type IS NULL) = (side_effect_id IS NULL)
    ),
    CONSTRAINT workflow_tool_call_idempotency_required CHECK (
        status = 'REFUSED'
        OR side_effect_level NOT IN ('DOMAIN_WRITE', 'IRREVERSIBLE_OR_UNKNOWN')
        OR idempotency_key IS NOT NULL
    ),
    CONSTRAINT workflow_tool_call_invocation_safety CHECK (
        invocation_policy IS NULL
        OR invocation_policy <> 'MODEL_REQUESTABLE'
        OR side_effect_level NOT IN ('DOMAIN_WRITE', 'IRREVERSIBLE_OR_UNKNOWN')
    ),
    CONSTRAINT workflow_tool_call_response_bundle CHECK (
        (response_hash IS NULL AND response_bytes = 0 AND response_summary IS NULL)
        OR (response_hash IS NOT NULL AND response_bytes > 0 AND response_summary IS NOT NULL)
    ),
    CONSTRAINT workflow_tool_call_lifecycle CHECK (
        (completed_at IS NULL OR completed_at >= started_at)
        AND (
            (
                status = 'STARTED'
                AND completed_at IS NULL
                AND duration_ms = 0
                AND error_code IS NULL
                AND NOT retryable
                AND response_hash IS NULL
                AND response_bytes = 0
                AND response_summary IS NULL
                AND result_ref IS NULL
                AND (
                    side_effect_type IS NULL
                    OR (
                        invocation_policy = 'TRUSTED_WORKFLOW_ONLY'
                        AND side_effect_level = 'DOMAIN_WRITE'
                        AND side_effect_type = 'writeback_execution'
                    )
                )
            )
            OR (
                status = 'SUCCEEDED'
                AND completed_at IS NOT NULL
                AND error_code IS NULL
                AND NOT retryable
                AND (
                    (response_hash IS NOT NULL AND response_bytes > 0 AND response_summary IS NOT NULL)
                    OR result_ref IS NOT NULL
                )
            )
            OR (
                status = 'FAILED'
                AND completed_at IS NOT NULL
                AND error_code IS NOT NULL
                AND response_hash IS NULL
                AND response_bytes = 0
                AND response_summary IS NULL
                AND result_ref IS NULL
            )
            OR (
                status = 'REFUSED'
                AND completed_at IS NOT NULL
                AND error_code IS NOT NULL
                AND NOT retryable
                AND response_hash IS NULL
                AND response_bytes = 0
                AND response_summary IS NULL
                AND result_ref IS NULL
                AND side_effect_type IS NULL
            )
            OR (
                status = 'UNKNOWN'
                AND completed_at IS NOT NULL
                AND error_code IS NOT NULL
                AND NOT retryable
                AND response_hash IS NULL
                AND response_bytes = 0
                AND response_summary IS NULL
                AND result_ref IS NULL
            )
        )
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_workflow_tool_call_attempt_active
    ON workflow.tool_call (node_attempt_id)
    WHERE status = 'STARTED';

CREATE UNIQUE INDEX IF NOT EXISTS uq_workflow_tool_call_side_effect_idempotency
    ON workflow.tool_call (workspace_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL
      AND status <> 'REFUSED'
      AND side_effect_level IN ('DOMAIN_WRITE', 'IRREVERSIBLE_OR_UNKNOWN');

CREATE INDEX IF NOT EXISTS idx_workflow_tool_call_run_node_timeline
	ON workflow.tool_call (workflow_run_id, node_run_id, started_at, call_no, id);

CREATE INDEX IF NOT EXISTS idx_workflow_tool_call_run_timeline
	ON workflow.tool_call (workflow_run_id, started_at, call_no, id);

CREATE INDEX IF NOT EXISTS idx_workflow_tool_call_workspace_status
    ON workflow.tool_call (workspace_id, status, started_at DESC, id);

CREATE INDEX IF NOT EXISTS idx_workflow_tool_call_started_recovery
    ON workflow.tool_call (started_at, call_no, id)
    WHERE status = 'STARTED';

CREATE INDEX IF NOT EXISTS idx_workflow_tool_call_side_effect_ref
    ON workflow.tool_call (workspace_id, side_effect_type, side_effect_id)
    WHERE side_effect_type IS NOT NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION workflow.guard_tool_call_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_status text;
    node_status text;
    node_attempt_no integer;
    node_lease_owner text;
    node_lease_until timestamptz;
    attempt_status text;
    attempt_no integer;
    attempt_lease_owner text;
    attempt_lease_until timestamptz;
    database_now timestamptz;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'workflow tool call is append-preserving' USING ERRCODE = '55000';
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1 OR NEW.status NOT IN ('STARTED', 'REFUSED') THEN
            RAISE EXCEPTION 'workflow tool call must start at version one' USING ERRCODE = '23514';
        END IF;

        SELECT r.status,n.status,n.attempt,n.lease_owner,n.lease_until,
               a.status,a.attempt_no,a.lease_owner,a.lease_until
          INTO run_status,node_status,node_attempt_no,node_lease_owner,node_lease_until,
               attempt_status,attempt_no,attempt_lease_owner,attempt_lease_until
          FROM workflow.run r
          JOIN workflow.node_run n ON n.run_id=r.id
          JOIN workflow.node_attempt a ON a.node_run_id=n.id
         WHERE a.id=NEW.node_attempt_id
         FOR SHARE OF r,n,a;

        IF NOT FOUND THEN
            -- 让后续外键以标准 23503 报告不存在的父事实。
            RETURN NEW;
        END IF;

        database_now := clock_timestamp();
        IF run_status IS DISTINCT FROM 'running'
           OR node_status IS DISTINCT FROM 'running'
           OR attempt_status IS DISTINCT FROM 'running'
           OR node_attempt_no IS DISTINCT FROM attempt_no
           OR node_lease_owner IS NULL
           OR node_lease_owner IS DISTINCT FROM btrim(node_lease_owner)
           OR node_lease_owner = ''
           OR attempt_lease_owner IS DISTINCT FROM node_lease_owner
           OR node_lease_until IS NULL
           OR attempt_lease_until IS NULL
           OR node_lease_until IS DISTINCT FROM attempt_lease_until
           OR node_lease_until <= database_now THEN
            RAISE EXCEPTION 'workflow tool call requires an active node attempt lease' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.status <> 'STARTED'
       OR NEW.status NOT IN ('SUCCEEDED', 'FAILED', 'UNKNOWN')
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id
       OR NEW.node_attempt_id IS DISTINCT FROM OLD.node_attempt_id
       OR NEW.call_no IS DISTINCT FROM OLD.call_no
       OR NEW.requested_tool_name IS DISTINCT FROM OLD.requested_tool_name
       OR NEW.tool_version IS DISTINCT FROM OLD.tool_version
       OR NEW.definition_hash IS DISTINCT FROM OLD.definition_hash
       OR NEW.input_schema_id IS DISTINCT FROM OLD.input_schema_id
       OR NEW.input_schema_version IS DISTINCT FROM OLD.input_schema_version
       OR NEW.output_schema_id IS DISTINCT FROM OLD.output_schema_id
       OR NEW.output_schema_version IS DISTINCT FROM OLD.output_schema_version
       OR NEW.capability IS DISTINCT FROM OLD.capability
       OR NEW.side_effect_level IS DISTINCT FROM OLD.side_effect_level
       OR NEW.invocation_policy IS DISTINCT FROM OLD.invocation_policy
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
       OR NEW.request_bytes IS DISTINCT FROM OLD.request_bytes
	       OR NEW.request_summary IS DISTINCT FROM OLD.request_summary
	       OR (
	           OLD.side_effect_type IS NOT NULL
	           AND (
	               NEW.side_effect_type IS DISTINCT FROM OLD.side_effect_type
	               OR NEW.side_effect_id IS DISTINCT FROM OLD.side_effect_id
	           )
	       )
	       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'workflow tool call transition or immutable binding is invalid' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS workflow_tool_call_guard_mutation ON workflow.tool_call;
CREATE TRIGGER workflow_tool_call_guard_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON workflow.tool_call
    FOR EACH ROW EXECUTE FUNCTION workflow.guard_tool_call_mutation();

INSERT INTO core.schema_meta (key, value)
VALUES ('tool_registry_security', 'm6-03')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    LOCK TABLE workflow.tool_call IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM workflow.tool_call) THEN
        RAISE EXCEPTION 'cannot downgrade Tool Registry Security while tool call history exists'
            USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DELETE FROM core.schema_meta WHERE key = 'tool_registry_security';

DROP TRIGGER IF EXISTS workflow_tool_call_guard_mutation ON workflow.tool_call;
DROP FUNCTION IF EXISTS workflow.guard_tool_call_mutation();
DROP TABLE IF EXISTS workflow.tool_call;
