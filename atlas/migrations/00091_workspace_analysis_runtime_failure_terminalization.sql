LOCK TABLE workflow.run,
           workflow.node_run,
           workflow.node_attempt,
           agent.workspace_analysis_run,
           agent.workspace_analysis_operation,
           agent.workspace_analysis_budget_reservation,
           agent.workspace_analysis_termination_proof,
           agent.question,
           agent.answer
    IN ACCESS EXCLUSIVE MODE;

ALTER TABLE agent.workspace_analysis_run
    DROP CONSTRAINT workspace_analysis_run_reason_check,
    ADD CONSTRAINT workspace_analysis_run_reason_check CHECK (
        (status IN ('queued','running') AND termination_reason IS NULL)
        OR (status='succeeded' AND termination_reason='COMPLETED')
        OR (
            status='refused'
            AND termination_reason IN (
                'WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT',
                'WORKSPACE_ANALYSIS_CITATION_INVALID',
                'WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED',
                'WORKSPACE_ANALYSIS_MODEL_REFUSED'
            )
        )
        OR (
            status='clarification_required'
            AND termination_reason='WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED'
        )
        OR (
            status='failed'
            AND termination_reason IN (
                'WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED',
                'WORKSPACE_ANALYSIS_RECEIPT_INVALID',
                'WORKSPACE_ANALYSIS_RESULT_UNKNOWN',
                'WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED',
                'WORKSPACE_ANALYSIS_MODEL_FAILED',
                'WORKSPACE_ANALYSIS_TOOL_FAILED',
                'WORKSPACE_ANALYSIS_RUNTIME_FAILED'
            )
        )
        OR (status='cancelled' AND termination_reason='WORKSPACE_ANALYSIS_CANCELLED')
    );

ALTER TABLE agent.workspace_analysis_termination_proof
    DROP CONSTRAINT workspace_analysis_termination_proof_reason_check,
    ADD CONSTRAINT workspace_analysis_termination_proof_reason_check CHECK (reason IN (
        'WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT',
        'WORKSPACE_ANALYSIS_CITATION_INVALID',
        'WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED',
        'WORKSPACE_ANALYSIS_MODEL_REFUSED',
        'WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED',
        'WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED',
        'WORKSPACE_ANALYSIS_RECEIPT_INVALID',
        'WORKSPACE_ANALYSIS_RESULT_UNKNOWN',
        'WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED',
        'WORKSPACE_ANALYSIS_MODEL_FAILED',
        'WORKSPACE_ANALYSIS_TOOL_FAILED',
        'WORKSPACE_ANALYSIS_RUNTIME_FAILED',
        'WORKSPACE_ANALYSIS_CANCELLED'
    ));

ALTER TABLE agent.workspace_analysis_termination_proof
    DROP CONSTRAINT workspace_analysis_termination_proof_runtime_terminal,
    ADD CONSTRAINT workspace_analysis_termination_proof_runtime_terminal CHECK (
        (
            runtime_terminal_at IS NULL
            AND terminal_node_attempt_id IS NOT NULL
        )
        OR (
            runtime_terminal_at IS NOT NULL
            AND (
                reason='WORKSPACE_ANALYSIS_CANCELLED'
                OR (
                    reason='WORKSPACE_ANALYSIS_RUNTIME_FAILED'
                    AND terminal_node_attempt_id IS NOT NULL
                )
            )
        )
    );

DROP TRIGGER workspace_analysis_runtime_cancellation_guard_insert
    ON agent.workspace_analysis_termination_proof;
CREATE TRIGGER workspace_analysis_runtime_cancellation_guard_insert
    BEFORE INSERT ON agent.workspace_analysis_termination_proof
    FOR EACH ROW
    WHEN (
        NEW.runtime_terminal_at IS NOT NULL
        AND NEW.reason='WORKSPACE_ANALYSIS_CANCELLED'
    )
    EXECUTE FUNCTION agent.guard_workspace_analysis_runtime_cancellation_insert();

-- The legacy Answer guard owns every existing publication path. Runtime
-- failure has no authoring ModelRun or logical Operation, so it is routed to
-- an exact, mutually exclusive guard rather than weakening the legacy matrix.
CREATE FUNCTION agent.guard_workspace_analysis_runtime_failure_answer()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    question_mode_value text;
    analysis_run_id_value uuid;
    analysis_status_value text;
    analysis_reason_value text;
    proof_row agent.workspace_analysis_termination_proof%ROWTYPE;
    proof_json jsonb;
BEGIN
    IF TG_OP<>'UPDATE'
       OR OLD.publication_status<>'pending'
       OR NEW.publication_status<>'failed'
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
       OR NEW.question_id IS DISTINCT FROM OLD.question_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.version<>OLD.version+1
       OR NEW.updated_at<OLD.updated_at
       OR NEW.updated_at IS DISTINCT FROM NEW.published_at
       OR NEW.model_run_id IS NOT NULL
       OR NEW.result_type<>'workspace_analysis_termination'
       OR NEW.result IS NULL
       OR NEW.result_hash IS NULL
       OR NEW.retrieval_summary IS NOT NULL THEN
        RAISE EXCEPTION 'workspace analysis runtime failure answer transition is invalid'
            USING ERRCODE='55000';
    END IF;

    SELECT question.mode,analysis.id,analysis.status,analysis.termination_reason
      INTO question_mode_value,analysis_run_id_value,analysis_status_value,analysis_reason_value
      FROM agent.question AS question
      JOIN agent.workspace_analysis_run AS analysis
        ON analysis.workspace_id=question.workspace_id
       AND analysis.conversation_id=question.conversation_id
       AND analysis.question_id=question.id
       AND analysis.answer_id=NEW.id
       AND analysis.workflow_run_id=NEW.workflow_run_id
     WHERE question.id=NEW.question_id
       AND question.workspace_id=NEW.workspace_id
       AND question.conversation_id=NEW.conversation_id
     FOR SHARE OF question,analysis;
    SELECT * INTO proof_row
      FROM agent.workspace_analysis_termination_proof
     WHERE workspace_id=NEW.workspace_id
       AND analysis_run_id=analysis_run_id_value
       AND answer_id=NEW.id
       AND workflow_run_id=NEW.workflow_run_id
     FOR SHARE;
    proof_json := convert_from(proof_row.published_document,'UTF8')::jsonb;

    IF question_mode_value IS DISTINCT FROM 'workspace_analysis'
       OR analysis_run_id_value IS NULL
       OR analysis_status_value IS DISTINCT FROM 'failed'
       OR analysis_reason_value IS DISTINCT FROM 'WORKSPACE_ANALYSIS_RUNTIME_FAILED'
       OR proof_row.id IS NULL
       OR proof_row.reason IS DISTINCT FROM 'WORKSPACE_ANALYSIS_RUNTIME_FAILED'
       OR proof_row.runtime_terminal_at IS NULL
       OR proof_row.operation_id IS NOT NULL
       OR proof_row.artifact_kind IS NOT NULL
       OR proof_row.artifact_id IS NOT NULL
       OR proof_row.artifact_hash IS NOT NULL
       OR proof_row.published_model_run_id IS NOT NULL
       OR NEW.result_hash IS DISTINCT FROM proof_row.published_result_hash
       OR NEW.result IS DISTINCT FROM proof_json
       OR proof_json->>'result_type' IS DISTINCT FROM 'workspace_analysis_termination'
       OR proof_json->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_termination'
       OR proof_json->>'schema_version' IS DISTINCT FROM 'v1'
       OR proof_json->'model_run_ref' IS DISTINCT FROM 'null'::jsonb
       OR proof_json->'payload'->>'termination_reason' IS DISTINCT FROM 'WORKSPACE_ANALYSIS_RUNTIME_FAILED'
       OR proof_json->'payload'->>'summary' IS DISTINCT FROM
          'The workspace analysis stopped because its runtime could not complete safely.' THEN
        RAISE EXCEPTION 'workspace analysis runtime failure answer proof is invalid'
            USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER agent_answer_guard_mutation ON agent.answer;
CREATE TRIGGER agent_answer_guard_mutation_insert_delete
    BEFORE INSERT OR DELETE ON agent.answer
    FOR EACH ROW EXECUTE FUNCTION agent.guard_answer_mutation();
CREATE TRIGGER agent_answer_guard_mutation_update
    BEFORE UPDATE ON agent.answer
    FOR EACH ROW
    WHEN (
        NEW.result->'payload'->>'termination_reason'
        IS DISTINCT FROM 'WORKSPACE_ANALYSIS_RUNTIME_FAILED'
    )
    EXECUTE FUNCTION agent.guard_answer_mutation();
CREATE TRIGGER workspace_analysis_runtime_failure_answer_guard_update
    BEFORE UPDATE ON agent.answer
    FOR EACH ROW
    WHEN (
        NEW.result->'payload'->>'termination_reason'
        IS NOT DISTINCT FROM 'WORKSPACE_ANALYSIS_RUNTIME_FAILED'
    )
    EXECUTE FUNCTION agent.guard_workspace_analysis_runtime_failure_answer();

-- A Runtime failure may close only a phase that failed before it created a
-- failed/unknown logical operation. Existing operation-backed terminal facts
-- remain authoritative and cannot be masked by this fallback.
CREATE FUNCTION agent.guard_workspace_analysis_runtime_failure_insert()
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
    node_failure_class text;
    node_error_code text;
    node_error_summary text;
    attempt_status_value text;
    attempt_number integer;
    attempt_lease_owner text;
    attempt_lease_until timestamptz;
    attempt_ended_at timestamptz;
    attempt_failure_class text;
    attempt_error_code text;
    attempt_error_summary text;
    expected_summary text;
    database_now timestamptz;
    published_json jsonb;
    published_payload jsonb;
BEGIN
    IF NEW.reason<>'WORKSPACE_ANALYSIS_RUNTIME_FAILED'
       OR NEW.runtime_terminal_at IS NULL
       OR NEW.runtime_terminal_at IS DISTINCT FROM NEW.checked_at
       OR NEW.terminal_node_attempt_id IS NULL
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
        RAISE EXCEPTION 'workspace analysis runtime failure proof shape is invalid'
            USING ERRCODE='55000';
    END IF;

    SELECT status,cancel_requested_at,completed_at
      INTO workflow_status_value,workflow_cancel_requested_at,workflow_completed_at
      FROM workflow.run
     WHERE id=NEW.workflow_run_id
       AND workspace_id=NEW.workspace_id
     FOR UPDATE;
    SELECT node_key,status,attempt,lease_owner,lease_until,completed_at,
           failure_class,error_code,error_summary
      INTO node_key_value,node_status_value,node_attempt_no,node_lease_owner,node_lease_until,node_completed_at,
           node_failure_class,node_error_code,node_error_summary
      FROM workflow.node_run
     WHERE id=NEW.terminal_node_run_id
       AND run_id=NEW.workflow_run_id
     FOR UPDATE;
    SELECT status,attempt_no,lease_owner,lease_until,ended_at,
           failure_class,error_code,error_summary
      INTO attempt_status_value,attempt_number,attempt_lease_owner,attempt_lease_until,attempt_ended_at,
           attempt_failure_class,attempt_error_code,attempt_error_summary
      FROM workflow.node_attempt
     WHERE id=NEW.terminal_node_attempt_id
       AND node_run_id=NEW.terminal_node_run_id
     FOR UPDATE;
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
       OR workflow_status_value<>'failed'
       OR workflow_cancel_requested_at IS NOT NULL
       OR workflow_completed_at IS DISTINCT FROM NEW.runtime_terminal_at
       OR node_key_value NOT IN (
            'inspect_workspace','retrieve_evidence','read_evidence',
            'synthesize_answer','validate_citations','review_publish'
       )
       OR node_status_value<>'failed'
       OR node_lease_owner IS NOT NULL
       OR node_lease_until IS NOT NULL
       OR node_completed_at IS DISTINCT FROM NEW.runtime_terminal_at
       OR node_failure_class IS NULL
       OR node_failure_class='cancelled'
       OR node_error_code IS NULL OR btrim(node_error_code)=''
       OR node_error_summary IS NULL OR btrim(node_error_summary)=''
       OR attempt_status_value NOT IN ('failed','manual_recovery','lease_lost')
       OR attempt_number IS DISTINCT FROM node_attempt_no
       OR attempt_lease_owner IS NOT NULL
       OR attempt_lease_until IS NOT NULL
       OR attempt_ended_at IS DISTINCT FROM NEW.runtime_terminal_at
       OR attempt_failure_class IS NULL
       OR attempt_failure_class='cancelled'
       OR attempt_error_code IS NULL OR btrim(attempt_error_code)=''
       OR attempt_error_summary IS NULL OR btrim(attempt_error_summary)=''
       OR NEW.runtime_terminal_at>database_now
       OR NEW.created_at>database_now
       OR NEW.created_at<NEW.runtime_terminal_at
       OR NEW.created_at<run_row.updated_at THEN
        RAISE EXCEPTION 'workspace analysis runtime failure fence is invalid'
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
        RAISE EXCEPTION 'workspace analysis runtime failure cannot mask unfinished or failed work'
            USING ERRCODE='55000';
    END IF;

    expected_summary := 'The workspace analysis stopped because its runtime could not complete safely.';
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
       OR published_payload->>'summary' IS DISTINCT FROM expected_summary THEN
        RAISE EXCEPTION 'workspace analysis runtime failure publication is invalid'
            USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER workspace_analysis_runtime_failure_guard_insert
    BEFORE INSERT ON agent.workspace_analysis_termination_proof
    FOR EACH ROW
    WHEN (
        NEW.runtime_terminal_at IS NOT NULL
        AND NEW.reason='WORKSPACE_ANALYSIS_RUNTIME_FAILED'
    )
    EXECUTE FUNCTION agent.guard_workspace_analysis_runtime_failure_insert();

INSERT INTO core.schema_meta(key,value)
VALUES (
    'workspace_analysis_runtime_failure_terminalization',
    'workspace-analysis-runtime-failure-terminalization-v1'
)
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();
