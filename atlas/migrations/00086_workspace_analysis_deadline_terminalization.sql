LOCK TABLE workflow.run,
           workflow.node_run,
           workflow.node_attempt,
           agent.workspace_analysis_run,
           agent.workspace_analysis_operation,
           agent.workspace_analysis_budget_reservation,
           agent.workspace_analysis_termination_proof
    IN ACCESS EXCLUSIVE MODE;

ALTER TABLE agent.workspace_analysis_termination_proof
    ADD COLUMN deadline_operation_kind text,
    ADD COLUMN deadline_operation_ordinal integer,
    ADD CONSTRAINT workspace_analysis_termination_proof_deadline_slot CHECK (
        (
            reason='WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED'
            AND operation_id IS NULL
        )=(
            deadline_operation_kind IS NOT NULL
            AND deadline_operation_ordinal IS NOT NULL
        )
        AND (
            deadline_operation_kind IS NULL
            OR (
                deadline_operation_kind='SOURCE_READ'
                AND deadline_operation_ordinal BETWEEN 1 AND 3
            )
            OR (
                deadline_operation_kind IN (
                    'GIT_STATUS',
                    'RETRIEVAL_PLAN',
                    'KNOWLEDGE_SEARCH',
                    'ANSWER_SYNTHESIS',
                    'CITATION_VALIDATION',
                    'FAITHFULNESS_REVIEW'
                )
                AND deadline_operation_ordinal=1
            )
        )
    );

-- Derive the single next v1 slot from immutable successful facts. Any gap,
-- terminal branch, active operation, or malformed prefix returns no row.
CREATE FUNCTION agent.workspace_analysis_deadline_next_slot(target_run_id uuid)
RETURNS TABLE(node_key text,operation_kind text,ordinal integer)
LANGUAGE plpgsql
VOLATILE
SECURITY INVOKER
SET search_path = pg_catalog, agent, workflow
AS $$
DECLARE
    total_count bigint;
    git_count bigint;
    plan_count bigint;
    search_count bigint;
    source_count bigint;
    synthesis_count bigint;
    validation_count bigint;
    review_count bigint;
    plan_json jsonb;
    selected_refs jsonb;
    selected_count integer;
BEGIN
    IF EXISTS (
        SELECT 1
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=target_run_id
           AND status<>'SUCCEEDED'
    ) THEN
        RETURN;
    END IF;

    SELECT count(*),
           count(*) FILTER (WHERE operation.operation_kind='GIT_STATUS'),
           count(*) FILTER (WHERE operation.operation_kind='RETRIEVAL_PLAN'),
           count(*) FILTER (WHERE operation.operation_kind='KNOWLEDGE_SEARCH'),
           count(*) FILTER (WHERE operation.operation_kind='SOURCE_READ'),
           count(*) FILTER (WHERE operation.operation_kind='ANSWER_SYNTHESIS'),
           count(*) FILTER (WHERE operation.operation_kind='CITATION_VALIDATION'),
           count(*) FILTER (WHERE operation.operation_kind='FAITHFULNESS_REVIEW')
      INTO total_count,git_count,plan_count,search_count,source_count,
           synthesis_count,validation_count,review_count
      FROM agent.workspace_analysis_operation AS operation
     WHERE operation.analysis_run_id=target_run_id;

    IF total_count=0 THEN
        RETURN QUERY SELECT 'inspect_workspace'::text,'GIT_STATUS'::text,1;
        RETURN;
    END IF;
    IF git_count<>1 THEN
        RETURN;
    END IF;

    IF plan_count=0 THEN
        IF total_count=1 THEN
            RETURN QUERY SELECT 'retrieve_evidence'::text,'RETRIEVAL_PLAN'::text,1;
        END IF;
        RETURN;
    ELSIF plan_count<>1 THEN
        RETURN;
    END IF;

    SELECT convert_from(result.document,'UTF8')::jsonb
      INTO plan_json
      FROM agent.workspace_analysis_operation AS plan
      JOIN agent.workspace_analysis_model_result AS result
        ON result.id=plan.result_id
       AND result.operation_id=plan.id
       AND result.analysis_run_id=plan.analysis_run_id
     WHERE plan.analysis_run_id=target_run_id
       AND plan.operation_kind='RETRIEVAL_PLAN'
       AND plan.status='SUCCEEDED';
    IF plan_json#>'{payload,requires_clarification}' IS NOT DISTINCT FROM 'true'::jsonb THEN
        RETURN;
    ELSIF plan_json#>'{payload,requires_clarification}' IS DISTINCT FROM 'false'::jsonb THEN
        RETURN;
    END IF;

    IF search_count=0 THEN
        IF total_count=2 THEN
            RETURN QUERY SELECT 'retrieve_evidence'::text,'KNOWLEDGE_SEARCH'::text,1;
        END IF;
        RETURN;
    ELSIF search_count<>1 THEN
        RETURN;
    END IF;

    selected_refs := agent.workspace_analysis_selected_refs(target_run_id);
    IF selected_refs IS NULL THEN
        RETURN;
    END IF;
    selected_count := jsonb_array_length(selected_refs);
    IF selected_count=0 THEN
        RETURN;
    END IF;
    IF source_count<selected_count THEN
        IF total_count=3+source_count
           AND agent.workspace_analysis_source_prefix_matches(
                target_run_id,selected_refs,source_count::integer
           ) THEN
            RETURN QUERY
                SELECT 'read_evidence'::text,'SOURCE_READ'::text,(source_count+1)::integer;
        END IF;
        RETURN;
    ELSIF source_count<>selected_count
       OR NOT agent.workspace_analysis_source_prefix_matches(
            target_run_id,selected_refs,selected_count
       ) THEN
        RETURN;
    END IF;

    IF synthesis_count=0 THEN
        IF total_count=3+source_count THEN
            RETURN QUERY SELECT 'synthesize_answer'::text,'ANSWER_SYNTHESIS'::text,1;
        END IF;
        RETURN;
    ELSIF synthesis_count<>1
       OR NOT EXISTS (
            SELECT 1
              FROM agent.workspace_analysis_operation AS synthesis
              JOIN agent.workspace_analysis_candidate AS candidate
                ON candidate.id=synthesis.result_id
               AND candidate.synthesis_operation_id=synthesis.id
               AND candidate.analysis_run_id=synthesis.analysis_run_id
             WHERE synthesis.analysis_run_id=target_run_id
               AND synthesis.operation_kind='ANSWER_SYNTHESIS'
               AND synthesis.status='SUCCEEDED'
               AND synthesis.result_hash=candidate.document_hash
       ) THEN
        RETURN;
    END IF;

    IF validation_count=0 THEN
        IF total_count=4+source_count THEN
            RETURN QUERY SELECT 'validate_citations'::text,'CITATION_VALIDATION'::text,1;
        END IF;
        RETURN;
    ELSIF validation_count<>1
       OR NOT agent.workspace_analysis_validation_all_valid(target_run_id) THEN
        RETURN;
    END IF;

    IF review_count=0 AND total_count=5+source_count THEN
        RETURN QUERY SELECT 'review_publish'::text,'FAITHFULNESS_REVIEW'::text,1;
    END IF;
    RETURN;
END;
$$;

CREATE FUNCTION agent.guard_workspace_analysis_preoperation_deadline_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_row agent.workspace_analysis_run%ROWTYPE;
    workflow_status_value text;
    workflow_cancel_requested_at timestamptz;
    terminal_node_key_value text;
    node_status_value text;
    node_attempt_no integer;
    node_lease_owner text;
    node_lease_until timestamptz;
    attempt_status_value text;
    attempt_no integer;
    attempt_lease_owner text;
    attempt_lease_until timestamptz;
    database_now timestamptz;
    derived_node_key text;
    derived_operation_kind text;
    derived_ordinal integer;
    call_timeout_ms bigint;
    published_json jsonb;
    published_payload jsonb;
    published_summary text;
BEGIN
    IF NEW.reason<>'WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED'
       OR NEW.operation_id IS NOT NULL
       OR NEW.deadline_operation_kind IS NOT NULL
       OR NEW.deadline_operation_ordinal IS NOT NULL
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
       OR NEW.published_model_run_id IS NOT NULL THEN
        RAISE EXCEPTION 'workspace analysis pre-operation deadline proof shape is invalid'
            USING ERRCODE='55000';
    END IF;

    SELECT status,cancel_requested_at
      INTO workflow_status_value,workflow_cancel_requested_at
      FROM workflow.run
     WHERE id=NEW.workflow_run_id
       AND workspace_id=NEW.workspace_id
     FOR UPDATE;
    SELECT node_key,status,attempt,lease_owner,lease_until
      INTO terminal_node_key_value,node_status_value,node_attempt_no,node_lease_owner,node_lease_until
      FROM workflow.node_run
     WHERE id=NEW.terminal_node_run_id
       AND run_id=NEW.workflow_run_id
     FOR UPDATE;
    SELECT attempt.status,attempt.attempt_no,attempt.lease_owner,attempt.lease_until
      INTO attempt_status_value,attempt_no,attempt_lease_owner,attempt_lease_until
      FROM workflow.node_attempt AS attempt
     WHERE attempt.id=NEW.terminal_node_attempt_id
       AND attempt.node_run_id=NEW.terminal_node_run_id
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
       OR workflow_status_value<>'running'
       OR workflow_cancel_requested_at IS NOT NULL
       OR node_status_value<>'running'
       OR node_attempt_no IS DISTINCT FROM attempt_no
       OR node_lease_owner IS NULL
       OR node_lease_owner IS DISTINCT FROM btrim(node_lease_owner)
       OR node_lease_owner=''
       OR attempt_lease_owner IS DISTINCT FROM node_lease_owner
       OR node_lease_until IS NULL
       OR node_lease_until IS DISTINCT FROM attempt_lease_until
       OR attempt_status_value<>'running'
       OR attempt_lease_until IS NULL
       OR attempt_lease_until<=database_now
       OR NEW.checked_at<run_row.created_at
       OR NEW.checked_at<run_row.updated_at
       OR NEW.checked_at>database_now
       OR NEW.created_at>database_now THEN
        RAISE EXCEPTION 'workspace analysis pre-operation deadline fence is invalid'
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
        RAISE EXCEPTION 'workspace analysis pre-operation deadline has active or failed work'
            USING ERRCODE='55000';
    END IF;

    SELECT slot.node_key,slot.operation_kind,slot.ordinal
      INTO derived_node_key,derived_operation_kind,derived_ordinal
      FROM agent.workspace_analysis_deadline_next_slot(NEW.analysis_run_id) AS slot;
    IF derived_operation_kind IS NULL
       OR derived_node_key IS DISTINCT FROM terminal_node_key_value THEN
        RAISE EXCEPTION 'workspace analysis pre-operation deadline has no unique next v1 slot'
            USING ERRCODE='55000';
    END IF;
    IF run_row.status='queued'
       AND (
            derived_node_key<>'inspect_workspace'
            OR derived_operation_kind<>'GIT_STATUS'
            OR derived_ordinal<>1
            OR EXISTS (
                SELECT 1
                  FROM agent.workspace_analysis_operation
                 WHERE analysis_run_id=NEW.analysis_run_id
            )
            OR EXISTS (
                SELECT 1
                  FROM agent.workspace_analysis_budget_reservation
                 WHERE analysis_run_id=NEW.analysis_run_id
            )
       ) THEN
        RAISE EXCEPTION 'queued workspace analysis deadline is not the pristine initial slot'
            USING ERRCODE='55000';
    END IF;

    call_timeout_ms := CASE derived_operation_kind
        WHEN 'GIT_STATUS' THEN run_row.git_tool_timeout_ms
        WHEN 'RETRIEVAL_PLAN' THEN run_row.plan_model_timeout_ms
        WHEN 'KNOWLEDGE_SEARCH' THEN run_row.search_tool_timeout_ms
        WHEN 'SOURCE_READ' THEN run_row.source_read_tool_timeout_ms
        WHEN 'ANSWER_SYNTHESIS' THEN run_row.synthesis_model_timeout_ms
        WHEN 'CITATION_VALIDATION' THEN run_row.validate_citation_tool_timeout_ms
        WHEN 'FAITHFULNESS_REVIEW' THEN run_row.review_model_timeout_ms
    END;
    IF call_timeout_ms IS NULL
       OR database_now+(call_timeout_ms+run_row.durable_completion_margin_ms)*interval '1 millisecond'
            <=run_row.deadline_at THEN
        RAISE EXCEPTION 'workspace analysis pre-operation deadline still has execution time'
            USING ERRCODE='55000';
    END IF;

    published_json := convert_from(NEW.published_document,'UTF8')::jsonb;
    published_payload := published_json->'payload';
    published_summary := published_payload->>'summary';
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
       OR jsonb_typeof(published_payload->'summary') IS DISTINCT FROM 'string'
       OR published_summary=''
       OR published_summary IS DISTINCT FROM btrim(published_summary)
       OR octet_length(convert_to(published_summary,'UTF8'))>4096 THEN
        RAISE EXCEPTION 'workspace analysis pre-operation deadline publication is invalid'
            USING ERRCODE='55000';
    END IF;

    NEW.deadline_operation_kind := derived_operation_kind;
    NEW.deadline_operation_ordinal := derived_ordinal;
    RETURN NEW;
END;
$$;

DROP TRIGGER workspace_analysis_termination_proof_guard_insert
    ON agent.workspace_analysis_termination_proof;
CREATE TRIGGER workspace_analysis_termination_proof_guard_insert
    BEFORE INSERT ON agent.workspace_analysis_termination_proof
    FOR EACH ROW
    WHEN (
        NEW.reason IS DISTINCT FROM 'WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED'
        OR NEW.operation_id IS NOT NULL
    )
    EXECUTE FUNCTION agent.guard_workspace_analysis_termination_proof_insert();
CREATE TRIGGER workspace_analysis_preoperation_deadline_guard_insert
    BEFORE INSERT ON agent.workspace_analysis_termination_proof
    FOR EACH ROW
    WHEN (
        NEW.reason='WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED'
        AND NEW.operation_id IS NULL
    )
    EXECUTE FUNCTION agent.guard_workspace_analysis_preoperation_deadline_insert();

INSERT INTO core.schema_meta(key,value)
VALUES (
    'workspace_analysis_deadline_terminalization',
    'workspace-analysis-deadline-terminalization-v1'
)
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();
