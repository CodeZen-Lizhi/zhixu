-- +goose Up

LOCK TABLE workflow.run,
           workflow.node_run,
           workflow.node_attempt,
           agent.workspace_analysis_run,
           agent.workspace_analysis_operation,
           agent.workspace_analysis_budget_reservation,
           agent.workspace_analysis_termination_proof
    IN ACCESS EXCLUSIVE MODE;

ALTER TABLE agent.workspace_analysis_termination_proof
    ALTER COLUMN terminal_node_attempt_id DROP NOT NULL,
    ADD COLUMN runtime_terminal_at timestamptz,
    ADD CONSTRAINT workspace_analysis_termination_proof_runtime_terminal CHECK (
        (
            runtime_terminal_at IS NULL
            AND terminal_node_attempt_id IS NOT NULL
        )
        OR (
            runtime_terminal_at IS NOT NULL
            AND reason='WORKSPACE_ANALYSIS_CANCELLED'
        )
    );

DROP TRIGGER workspace_analysis_termination_proof_guard_insert
    ON agent.workspace_analysis_termination_proof;
CREATE TRIGGER workspace_analysis_termination_proof_guard_insert
    BEFORE INSERT ON agent.workspace_analysis_termination_proof
    FOR EACH ROW
    WHEN (
        NEW.runtime_terminal_at IS NULL
        AND (
            NEW.reason IS DISTINCT FROM 'WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED'
            OR NEW.operation_id IS NOT NULL
        )
    )
    EXECUTE FUNCTION agent.guard_workspace_analysis_termination_proof_insert();

-- Runtime invokes this guard after it has terminalized the Workflow, Node and
-- optional Attempt in the same transaction. This is deliberately separate
-- from the active-lease finalizer guard.
-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_runtime_cancellation_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_row agent.workspace_analysis_run%ROWTYPE;
    workflow_status_value text;
    workflow_cancel_requested_at timestamptz;
    workflow_completed_at timestamptz;
    node_key_value text;
    node_status_value text;
    node_attempt_no integer;
    node_lease_owner text;
    node_lease_until timestamptz;
    node_completed_at timestamptz;
    attempt_status_value text;
    attempt_number integer;
    attempt_lease_owner text;
    attempt_lease_until timestamptz;
    attempt_ended_at timestamptz;
    database_now timestamptz;
    published_json jsonb;
    published_payload jsonb;
BEGIN
    IF NEW.reason<>'WORKSPACE_ANALYSIS_CANCELLED'
       OR NEW.runtime_terminal_at IS NULL
       OR NEW.runtime_terminal_at IS DISTINCT FROM NEW.checked_at
       OR NEW.operation_id IS NOT NULL
       OR NEW.artifact_kind IS NOT NULL
       OR NEW.artifact_id IS NOT NULL
       OR NEW.artifact_hash IS NOT NULL
       OR NEW.requested_model_calls IS NOT NULL
       OR NEW.requested_tool_calls IS NOT NULL
       OR NEW.requested_source_reads IS NOT NULL
       OR NEW.requested_input_tokens IS NOT NULL
       OR NEW.requested_output_tokens IS NOT NULL
       OR NEW.requested_cost_microunits IS NOT NULL
       OR NEW.receipt_failure_code IS NOT NULL
       OR NEW.receipt_failure_id IS NOT NULL
       OR NEW.expected_hash IS NOT NULL
       OR NEW.actual_hash IS NOT NULL
       OR NEW.published_model_run_id IS NOT NULL
       OR NEW.deadline_operation_kind IS NOT NULL
       OR NEW.deadline_operation_ordinal IS NOT NULL THEN
        RAISE EXCEPTION 'workspace analysis runtime cancellation proof shape is invalid'
            USING ERRCODE='55000';
    END IF;

    SELECT status,cancel_requested_at,completed_at
      INTO workflow_status_value,workflow_cancel_requested_at,workflow_completed_at
      FROM workflow.run
     WHERE id=NEW.workflow_run_id
       AND workspace_id=NEW.workspace_id
     FOR UPDATE;
    SELECT node_key,status,attempt,lease_owner,lease_until,completed_at
      INTO node_key_value,node_status_value,node_attempt_no,node_lease_owner,node_lease_until,node_completed_at
      FROM workflow.node_run
     WHERE id=NEW.terminal_node_run_id
       AND run_id=NEW.workflow_run_id
     FOR UPDATE;
    IF NEW.terminal_node_attempt_id IS NOT NULL THEN
        SELECT status,attempt_no,lease_owner,lease_until,ended_at
          INTO attempt_status_value,attempt_number,attempt_lease_owner,attempt_lease_until,attempt_ended_at
          FROM workflow.node_attempt
         WHERE id=NEW.terminal_node_attempt_id
           AND node_run_id=NEW.terminal_node_run_id
         FOR UPDATE;
    END IF;
    SELECT * INTO run_row
      FROM agent.workspace_analysis_run
     WHERE id=NEW.analysis_run_id
       AND workspace_id=NEW.workspace_id
       AND answer_id=NEW.answer_id
       AND workflow_run_id=NEW.workflow_run_id
     FOR UPDATE;

    PERFORM id
      FROM agent.workspace_analysis_operation
     WHERE analysis_run_id=NEW.analysis_run_id
     ORDER BY id
     FOR UPDATE;
    PERFORM id
      FROM agent.workspace_analysis_budget_reservation
     WHERE analysis_run_id=NEW.analysis_run_id
     ORDER BY id
     FOR UPDATE;
    database_now := clock_timestamp();

    IF run_row.id IS NULL
       OR run_row.status NOT IN ('queued','running')
       OR run_row.termination_reason IS NOT NULL
       OR workflow_status_value<>'cancelled'
       OR workflow_cancel_requested_at IS NULL
       OR workflow_cancel_requested_at>NEW.runtime_terminal_at
       OR workflow_completed_at IS DISTINCT FROM NEW.runtime_terminal_at
       OR node_key_value NOT IN (
            'inspect_workspace','retrieve_evidence','read_evidence',
            'synthesize_answer','validate_citations','review_publish'
       )
       OR node_status_value<>'cancelled'
       OR node_lease_owner IS NOT NULL
       OR node_lease_until IS NOT NULL
       OR node_completed_at IS DISTINCT FROM NEW.runtime_terminal_at
       OR NEW.runtime_terminal_at>database_now
       OR NEW.created_at>database_now
       OR NEW.created_at<NEW.runtime_terminal_at
       OR NEW.created_at<run_row.updated_at THEN
        RAISE EXCEPTION 'workspace analysis runtime cancellation fence is invalid'
            USING ERRCODE='55000';
    END IF;

    IF NEW.terminal_node_attempt_id IS NULL THEN
        IF node_attempt_no<>0 OR EXISTS (
            SELECT 1
              FROM workflow.node_attempt
             WHERE node_run_id=NEW.terminal_node_run_id
        ) THEN
            RAISE EXCEPTION 'workspace analysis direct cancellation attempt binding is invalid'
                USING ERRCODE='55000';
        END IF;
    ELSIF attempt_status_value IS NULL
       OR attempt_status_value NOT IN (
            'succeeded','waiting_for_human','retry_scheduled','failed',
            'manual_recovery','lease_lost','cancelled'
       )
       OR attempt_number IS DISTINCT FROM node_attempt_no
       OR attempt_lease_owner IS NOT NULL
       OR attempt_lease_until IS NOT NULL
       OR attempt_ended_at IS NULL
       OR attempt_ended_at>NEW.runtime_terminal_at THEN
        RAISE EXCEPTION 'workspace analysis cancellation attempt binding is invalid'
            USING ERRCODE='55000';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=NEW.analysis_run_id
           AND status<>'SUCCEEDED'
    ) OR EXISTS (
        SELECT 1
          FROM agent.workspace_analysis_budget_reservation
         WHERE analysis_run_id=NEW.analysis_run_id
           AND status='RESERVED'
    ) THEN
        RAISE EXCEPTION 'workspace analysis cancellation cannot mask unfinished or failed work'
            USING ERRCODE='55000';
    END IF;

    published_json := convert_from(NEW.published_document,'UTF8')::jsonb;
    published_payload := published_json->'payload';
    IF jsonb_typeof(published_json) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_json))<>5
       OR published_json->>'result_type' IS DISTINCT FROM 'workspace_analysis_termination'
       OR published_json->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_termination'
       OR published_json->>'schema_version' IS DISTINCT FROM 'v1'
       OR NOT (published_json ? 'model_run_ref')
       OR jsonb_typeof(published_json->'model_run_ref') IS DISTINCT FROM 'null'
       OR jsonb_typeof(published_payload) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_payload))<>2
       OR published_payload->>'termination_reason' IS DISTINCT FROM NEW.reason
       OR published_payload->>'summary' IS DISTINCT FROM 'The workspace analysis was cancelled.' THEN
        RAISE EXCEPTION 'workspace analysis runtime cancellation publication is invalid'
            USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER workspace_analysis_runtime_cancellation_guard_insert
    BEFORE INSERT ON agent.workspace_analysis_termination_proof
    FOR EACH ROW
    WHEN (NEW.runtime_terminal_at IS NOT NULL)
    EXECUTE FUNCTION agent.guard_workspace_analysis_runtime_cancellation_insert();

INSERT INTO core.schema_meta(key,value)
VALUES (
    'workspace_analysis_cancellation_terminal_hook',
    'workspace-analysis-cancellation-terminal-hook-v1'
)
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();

-- +goose Down

LOCK TABLE workflow.run,
           workflow.node_run,
           workflow.node_attempt,
           agent.workspace_analysis_run,
           agent.workspace_analysis_operation,
           agent.workspace_analysis_budget_reservation,
           agent.workspace_analysis_termination_proof
    IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM agent.workspace_analysis_termination_proof
         WHERE runtime_terminal_at IS NOT NULL
         LIMIT 1
    ) THEN
        RAISE EXCEPTION 'runtime workspace analysis cancellation facts exist; rollback is unsafe'
            USING ERRCODE='55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS workspace_analysis_runtime_cancellation_guard_insert
    ON agent.workspace_analysis_termination_proof;
DROP TRIGGER IF EXISTS workspace_analysis_termination_proof_guard_insert
    ON agent.workspace_analysis_termination_proof;
CREATE TRIGGER workspace_analysis_termination_proof_guard_insert
    BEFORE INSERT ON agent.workspace_analysis_termination_proof
    FOR EACH ROW
    WHEN (
        NEW.reason IS DISTINCT FROM 'WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED'
        OR NEW.operation_id IS NOT NULL
    )
    EXECUTE FUNCTION agent.guard_workspace_analysis_termination_proof_insert();

DROP FUNCTION agent.guard_workspace_analysis_runtime_cancellation_insert();

ALTER TABLE agent.workspace_analysis_termination_proof
    DROP CONSTRAINT IF EXISTS workspace_analysis_termination_proof_runtime_terminal,
    ALTER COLUMN terminal_node_attempt_id SET NOT NULL,
    DROP COLUMN runtime_terminal_at;

DELETE FROM core.schema_meta
WHERE key='workspace_analysis_cancellation_terminal_hook';
