-- +goose Up

LOCK TABLE agent.question,
           agent.answer,
           agent.model_run,
           agent.model_call,
           workflow.tool_call
    IN ACCESS EXCLUSIVE MODE;

ALTER TABLE agent.model_call
    DROP CONSTRAINT IF EXISTS agent_model_call_phase_order,
    ADD CONSTRAINT agent_model_call_phase_order CHECK (
        (phase = 'PLAN' AND call_no = 1)
        OR (phase = 'AGENT' AND call_no >= 1)
        OR (phase = 'ANSWER' AND call_no >= 1)
        OR (phase = 'INITIAL' AND call_no >= 1)
        OR (phase = 'REPAIR' AND call_no >= 2)
        OR (phase = 'REDUCED' AND call_no >= 3)
        OR (phase = 'REVIEW' AND call_no >= 1)
    );

-- Workspace Analysis owns a dedicated one-call Review Model Run.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agent.guard_model_call_phase_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    previous_phase text;
    previous_status text;
BEGIN
    IF NEW.call_no = 1 THEN
        IF NEW.phase NOT IN ('PLAN', 'AGENT', 'ANSWER', 'INITIAL', 'REVIEW') THEN
            RAISE EXCEPTION 'agent model call first phase is invalid' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;

    SELECT phase, status
      INTO previous_phase, previous_status
      FROM agent.model_call
     WHERE model_run_id = NEW.model_run_id
       AND call_no = NEW.call_no - 1
     FOR SHARE;

    IF previous_status IS DISTINCT FROM 'SUCCEEDED'
       OR NOT (
            (previous_phase = 'PLAN' AND NEW.phase IN ('AGENT', 'ANSWER', 'INITIAL'))
            OR (previous_phase = 'AGENT' AND NEW.phase IN ('AGENT', 'ANSWER'))
            OR (previous_phase = 'ANSWER' AND NEW.phase = 'INITIAL')
            OR (previous_phase = 'INITIAL' AND NEW.phase IN ('REPAIR', 'REVIEW'))
            OR (previous_phase = 'REPAIR' AND NEW.phase IN ('REDUCED', 'REVIEW'))
            OR (previous_phase = 'REDUCED' AND NEW.phase = 'REVIEW')
       ) THEN
        RAISE EXCEPTION 'agent model call phase predecessor is invalid' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE agent.question
    ADD COLUMN IF NOT EXISTS mode text NOT NULL DEFAULT 'rag';

ALTER TABLE agent.question
    DROP CONSTRAINT IF EXISTS agent_question_mode_check,
    ADD CONSTRAINT agent_question_mode_check CHECK (mode IN ('rag','workspace_analysis'));

ALTER TABLE agent.answer
    DROP CONSTRAINT IF EXISTS answer_publication_status_check,
    DROP CONSTRAINT IF EXISTS agent_answer_publication_status_check,
    ADD CONSTRAINT agent_answer_publication_status_check CHECK (
        publication_status IN (
            'pending','completed','refused','clarification_required','failed','cancelled'
        )
    ),
    DROP CONSTRAINT IF EXISTS answer_result_type_check,
    DROP CONSTRAINT IF EXISTS agent_answer_result_type_check,
    ADD CONSTRAINT agent_answer_result_type_check CHECK (
        result_type IS NULL OR result_type IN (
            'rag_answer','refusal','clarification',
            'workspace_analysis','workspace_analysis_refusal','workspace_analysis_termination'
        )
    ),
    DROP CONSTRAINT IF EXISTS agent_answer_publication_bundle,
    ADD CONSTRAINT agent_answer_publication_bundle CHECK (
        (
            publication_status='pending'
            AND model_run_id IS NULL
            AND result_type IS NULL
            AND result IS NULL
            AND result_hash IS NULL
            AND retrieval_summary IS NULL
            AND published_at IS NULL
        )
        OR (
            publication_status='completed'
            AND model_run_id IS NOT NULL
            AND result_type='rag_answer'
            AND result IS NOT NULL
            AND result_hash IS NOT NULL
            AND retrieval_summary IS NOT NULL
            AND published_at IS NOT NULL
        )
        OR (
            publication_status='refused'
            AND model_run_id IS NOT NULL
            AND result_type='refusal'
            AND result IS NOT NULL
            AND result_hash IS NOT NULL
            AND retrieval_summary IS NOT NULL
            AND published_at IS NOT NULL
        )
        OR (
            publication_status='clarification_required'
            AND model_run_id IS NOT NULL
            AND result_type='clarification'
            AND result IS NOT NULL
            AND result_hash IS NOT NULL
            AND retrieval_summary IS NOT NULL
            AND published_at IS NOT NULL
        )
        OR (
            publication_status='completed'
            AND model_run_id IS NOT NULL
            AND result_type='workspace_analysis'
            AND result IS NOT NULL
            AND result_hash IS NOT NULL
            AND retrieval_summary IS NULL
            AND published_at IS NOT NULL
        )
        OR (
            publication_status='refused'
            AND result_type='workspace_analysis_refusal'
            AND result IS NOT NULL
            AND result_hash IS NOT NULL
            AND retrieval_summary IS NULL
            AND published_at IS NOT NULL
        )
        OR (
            publication_status='clarification_required'
            AND model_run_id IS NOT NULL
            AND result_type='clarification'
            AND result IS NOT NULL
            AND result_hash IS NOT NULL
            AND retrieval_summary IS NULL
            AND published_at IS NOT NULL
        )
        OR (
            publication_status IN ('failed','cancelled')
            AND result_type='workspace_analysis_termination'
            AND result IS NOT NULL
            AND result_hash IS NOT NULL
            AND retrieval_summary IS NULL
            AND published_at IS NOT NULL
        )
    );

ALTER TABLE agent.model_run
    DROP CONSTRAINT IF EXISTS agent_model_run_final_result_type_check,
    ADD CONSTRAINT agent_model_run_final_result_type_check CHECK (
        final_result_type IS NULL OR final_result_type IN (
            'relation_assessment','rag_answer','refusal','faithfulness_review',
            'tool_request','clarification','artifact_section','document_knowledge_profile',
            'organizing_outline','organizing_document',
            'workspace_analysis_plan','workspace_analysis_answer'
        )
    );

ALTER TABLE workflow.tool_call
    DROP CONSTRAINT IF EXISTS uq_workflow_tool_call_exact_identity,
    ADD CONSTRAINT uq_workflow_tool_call_exact_identity
        UNIQUE (id,workspace_id,workflow_run_id,node_run_id,node_attempt_id);

CREATE TABLE agent.workspace_analysis_run (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    conversation_id uuid NOT NULL,
    question_id uuid NOT NULL,
    answer_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    definition_key text NOT NULL,
    definition_version bigint NOT NULL,
    definition_hash text NOT NULL CHECK (definition_hash ~ '^[0-9a-f]{64}$'),
    tool_catalog_hash text NOT NULL CHECK (tool_catalog_hash ~ '^[0-9a-f]{64}$'),
    policy_version integer NOT NULL,
    config_revision bigint NOT NULL CHECK (config_revision >= 0),
    deadline_at timestamptz NOT NULL,
    plan_model_timeout_ms bigint NOT NULL CHECK (plan_model_timeout_ms BETWEEN 1 AND 3600000),
    synthesis_model_timeout_ms bigint NOT NULL CHECK (synthesis_model_timeout_ms BETWEEN 1 AND 3600000),
    review_model_timeout_ms bigint NOT NULL CHECK (review_model_timeout_ms BETWEEN 1 AND 3600000),
    git_tool_timeout_ms bigint NOT NULL CHECK (git_tool_timeout_ms BETWEEN 1 AND 3600000),
    search_tool_timeout_ms bigint NOT NULL CHECK (search_tool_timeout_ms BETWEEN 1 AND 3600000),
    source_read_tool_timeout_ms bigint NOT NULL CHECK (source_read_tool_timeout_ms BETWEEN 1 AND 3600000),
    validate_citation_tool_timeout_ms bigint NOT NULL CHECK (validate_citation_tool_timeout_ms BETWEEN 1 AND 3600000),
    durable_completion_margin_ms bigint NOT NULL CHECK (durable_completion_margin_ms=5000),
    max_nodes integer NOT NULL,
    max_model_calls integer NOT NULL,
    max_tool_calls integer NOT NULL,
    max_source_reads integer NOT NULL,
    max_tool_concurrency integer NOT NULL,
    max_input_tokens bigint NOT NULL,
    max_output_tokens bigint NOT NULL,
    max_cost_microunits bigint,
    reserved_model_calls integer NOT NULL DEFAULT 0 CHECK (reserved_model_calls >= 0),
    reserved_tool_calls integer NOT NULL DEFAULT 0 CHECK (reserved_tool_calls >= 0),
    reserved_source_reads integer NOT NULL DEFAULT 0 CHECK (reserved_source_reads >= 0),
    reserved_input_tokens bigint NOT NULL DEFAULT 0 CHECK (reserved_input_tokens >= 0),
    reserved_output_tokens bigint NOT NULL DEFAULT 0 CHECK (reserved_output_tokens >= 0),
    reserved_cost_microunits bigint,
    settled_model_calls integer NOT NULL DEFAULT 0 CHECK (settled_model_calls >= 0),
    settled_tool_calls integer NOT NULL DEFAULT 0 CHECK (settled_tool_calls >= 0),
    settled_source_reads integer NOT NULL DEFAULT 0 CHECK (settled_source_reads >= 0),
    settled_input_tokens bigint NOT NULL DEFAULT 0 CHECK (settled_input_tokens >= 0),
    settled_output_tokens bigint NOT NULL DEFAULT 0 CHECK (settled_output_tokens >= 0),
    settled_cost_microunits bigint,
    status text NOT NULL,
    termination_reason text,
    validation_receipt_id uuid,
    review_model_run_id uuid,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT uq_workspace_analysis_run_workspace UNIQUE (id,workspace_id),
    CONSTRAINT uq_workspace_analysis_run_scope UNIQUE (id,workspace_id,workflow_run_id),
    CONSTRAINT uq_workspace_analysis_run_answer_scope UNIQUE (id,workspace_id,answer_id),
    CONSTRAINT uq_workspace_analysis_run_question UNIQUE (workspace_id,question_id),
    CONSTRAINT uq_workspace_analysis_run_answer UNIQUE (workspace_id,answer_id),
    CONSTRAINT uq_workspace_analysis_run_workflow UNIQUE (workspace_id,workflow_run_id),
    CONSTRAINT fk_workspace_analysis_run_question
        FOREIGN KEY (question_id,conversation_id,workspace_id)
        REFERENCES agent.question(id,conversation_id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_run_answer
        FOREIGN KEY (answer_id,workspace_id,workflow_run_id)
        REFERENCES agent.answer(id,workspace_id,workflow_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_run_workflow
        FOREIGN KEY (workflow_run_id,workspace_id)
        REFERENCES workflow.run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT workspace_analysis_run_contract CHECK (
        definition_key='workspace-analysis'
        AND definition_version=1
        AND policy_version=1
        AND max_nodes=6
        AND max_model_calls=3
        AND max_tool_calls=6
        AND max_source_reads=3
        AND max_tool_concurrency=1
        AND max_input_tokens=196608
        AND max_output_tokens BETWEEN 1281 AND 5376
        AND max_cost_microunits IS NULL
    ),
    CONSTRAINT workspace_analysis_run_budget_bounds CHECK (
        reserved_model_calls+settled_model_calls <= max_model_calls
        AND reserved_tool_calls+settled_tool_calls <= max_tool_calls
        AND reserved_source_reads+settled_source_reads <= max_source_reads
        AND reserved_input_tokens+settled_input_tokens <= max_input_tokens
        AND reserved_output_tokens+settled_output_tokens <= max_output_tokens
        AND reserved_cost_microunits IS NULL
        AND settled_cost_microunits IS NULL
    ),
    CONSTRAINT workspace_analysis_run_status_check CHECK (
        status IN ('queued','running','succeeded','refused','clarification_required','failed','cancelled')
    ),
    CONSTRAINT workspace_analysis_run_reason_check CHECK (
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
                'WORKSPACE_ANALYSIS_TOOL_FAILED'
            )
        )
        OR (status='cancelled' AND termination_reason='WORKSPACE_ANALYSIS_CANCELLED')
    ),
    CONSTRAINT workspace_analysis_run_lifecycle CHECK (
        updated_at >= created_at
        AND deadline_at > created_at
        AND deadline_at=created_at+(
            git_tool_timeout_ms+5000
            +plan_model_timeout_ms+search_tool_timeout_ms+15000
            +3*source_read_tool_timeout_ms+10000
            +synthesis_model_timeout_ms+15000
            +validate_citation_tool_timeout_ms+10000
            +review_model_timeout_ms+15000
        )*interval '1 millisecond'
        AND deadline_at <= created_at+interval '1 hour'
        AND (
            (status IN ('queued','running') AND termination_reason IS NULL AND completed_at IS NULL)
            OR (
                status='succeeded'
                AND termination_reason='COMPLETED'
                AND validation_receipt_id IS NOT NULL
                AND review_model_run_id IS NOT NULL
                AND completed_at IS NOT NULL
                AND completed_at=updated_at
                AND completed_at<=deadline_at
            )
            OR (
                status IN ('refused','clarification_required','failed','cancelled')
                AND termination_reason IS NOT NULL
                AND completed_at IS NOT NULL
                AND completed_at=updated_at
            )
        )
    )
);

CREATE INDEX idx_workspace_analysis_run_answer
    ON agent.workspace_analysis_run (workspace_id,answer_id);
CREATE INDEX idx_workspace_analysis_run_recovery
    ON agent.workspace_analysis_run (status,deadline_at,id)
    WHERE status IN ('queued','running');

CREATE TABLE agent.workspace_analysis_operation (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    analysis_run_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    node_run_id uuid NOT NULL,
    node_key text NOT NULL,
    operation_kind text NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal BETWEEN 1 AND 3),
    call_kind text NOT NULL CHECK (call_kind IN ('MODEL','TOOL')),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    status text NOT NULL CHECK (status IN ('PENDING','STARTED','SUCCEEDED','FAILED','UNKNOWN')),
    first_node_attempt_id uuid,
    latest_node_attempt_id uuid,
    model_call_id uuid REFERENCES agent.model_call(id) ON DELETE RESTRICT,
    tool_call_id uuid REFERENCES workflow.tool_call(id) ON DELETE RESTRICT,
    result_kind text CHECK (
        result_kind IS NULL OR result_kind IN (
            'MODEL_RESULT_RECEIPT','TOOL_RESULT_RECEIPT','SYNTHESIS_CANDIDATE'
        )
    ),
    result_id uuid,
    result_hash text CHECK (result_hash IS NULL OR result_hash ~ '^[0-9a-f]{64}$'),
    error_code text CHECK (
        error_code IS NULL OR error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'
    ),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    started_at timestamptz,
    completed_at timestamptz,
    CONSTRAINT uq_workspace_analysis_operation_scope UNIQUE (id,analysis_run_id),
    CONSTRAINT uq_workspace_analysis_operation_key
        UNIQUE (analysis_run_id,node_key,operation_kind,ordinal),
    CONSTRAINT uq_workspace_analysis_operation_model_call UNIQUE (model_call_id),
    CONSTRAINT uq_workspace_analysis_operation_tool_call UNIQUE (tool_call_id),
    CONSTRAINT fk_workspace_analysis_operation_run
        FOREIGN KEY (analysis_run_id,workspace_id,workflow_run_id)
        REFERENCES agent.workspace_analysis_run(id,workspace_id,workflow_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_operation_node_run
        FOREIGN KEY (node_run_id,workflow_run_id)
        REFERENCES workflow.node_run(id,run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_operation_first_attempt
        FOREIGN KEY (first_node_attempt_id,node_run_id)
        REFERENCES workflow.node_attempt(id,node_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_operation_latest_attempt
        FOREIGN KEY (latest_node_attempt_id,node_run_id)
        REFERENCES workflow.node_attempt(id,node_run_id) ON DELETE RESTRICT,
    CONSTRAINT workspace_analysis_operation_slot CHECK (
        (node_key='inspect_workspace' AND operation_kind='GIT_STATUS' AND ordinal=1 AND call_kind='TOOL')
        OR (node_key='retrieve_evidence' AND operation_kind='RETRIEVAL_PLAN' AND ordinal=1 AND call_kind='MODEL')
        OR (node_key='retrieve_evidence' AND operation_kind='KNOWLEDGE_SEARCH' AND ordinal=1 AND call_kind='TOOL')
        OR (node_key='read_evidence' AND operation_kind='SOURCE_READ' AND ordinal BETWEEN 1 AND 3 AND call_kind='TOOL')
        OR (node_key='synthesize_answer' AND operation_kind='ANSWER_SYNTHESIS' AND ordinal=1 AND call_kind='MODEL')
        OR (node_key='validate_citations' AND operation_kind='CITATION_VALIDATION' AND ordinal=1 AND call_kind='TOOL')
        OR (node_key='review_publish' AND operation_kind='FAITHFULNESS_REVIEW' AND ordinal=1 AND call_kind='MODEL')
    ),
    CONSTRAINT workspace_analysis_operation_call_pair CHECK (
        (call_kind='MODEL' AND tool_call_id IS NULL)
        OR (call_kind='TOOL' AND model_call_id IS NULL)
    ),
    CONSTRAINT workspace_analysis_operation_result_kind CHECK (
        result_kind IS NULL
        OR (call_kind='TOOL' AND result_kind='TOOL_RESULT_RECEIPT')
        OR (operation_kind IN ('RETRIEVAL_PLAN','FAITHFULNESS_REVIEW') AND result_kind='MODEL_RESULT_RECEIPT')
        OR (operation_kind='ANSWER_SYNTHESIS' AND result_kind='SYNTHESIS_CANDIDATE')
    ),
    CONSTRAINT workspace_analysis_operation_lifecycle CHECK (
        updated_at >= created_at
        AND (started_at IS NULL OR (started_at >= created_at AND started_at <= updated_at))
        AND (completed_at IS NULL OR (started_at IS NOT NULL AND completed_at >= started_at AND completed_at <= updated_at))
        AND (
            (
                status='PENDING'
                AND first_node_attempt_id IS NULL
                AND latest_node_attempt_id IS NULL
                AND model_call_id IS NULL
                AND tool_call_id IS NULL
                AND started_at IS NULL
                AND completed_at IS NULL
                AND result_kind IS NULL
                AND result_id IS NULL
                AND result_hash IS NULL
                AND error_code IS NULL
            )
            OR (
                status='STARTED'
                AND first_node_attempt_id IS NOT NULL
                AND latest_node_attempt_id IS NOT NULL
                AND ((call_kind='MODEL' AND model_call_id IS NOT NULL) OR (call_kind='TOOL' AND tool_call_id IS NOT NULL))
                AND started_at IS NOT NULL
                AND completed_at IS NULL
                AND result_kind IS NULL
                AND result_id IS NULL
                AND result_hash IS NULL
                AND error_code IS NULL
            )
            OR (
                status='SUCCEEDED'
                AND first_node_attempt_id IS NOT NULL
                AND latest_node_attempt_id IS NOT NULL
                AND ((call_kind='MODEL' AND model_call_id IS NOT NULL) OR (call_kind='TOOL' AND tool_call_id IS NOT NULL))
                AND started_at IS NOT NULL
                AND completed_at IS NOT NULL
                AND result_kind IS NOT NULL
                AND result_id IS NOT NULL
                AND result_hash IS NOT NULL
                AND error_code IS NULL
            )
            OR (
                status IN ('FAILED','UNKNOWN')
                AND first_node_attempt_id IS NOT NULL
                AND latest_node_attempt_id IS NOT NULL
                AND ((call_kind='MODEL' AND model_call_id IS NOT NULL) OR (call_kind='TOOL' AND tool_call_id IS NOT NULL))
                AND started_at IS NOT NULL
                AND completed_at IS NOT NULL
                AND result_kind IS NULL
                AND result_id IS NULL
                AND result_hash IS NULL
                AND error_code IS NOT NULL
            )
        )
    )
);

CREATE INDEX idx_workspace_analysis_operation_recovery
    ON agent.workspace_analysis_operation (status,updated_at,id)
    WHERE status='STARTED';
CREATE UNIQUE INDEX uq_workspace_analysis_active_tool_operation
    ON agent.workspace_analysis_operation (analysis_run_id)
    WHERE call_kind='TOOL' AND status='STARTED';

CREATE TABLE agent.workspace_analysis_budget_reservation (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    analysis_run_id uuid NOT NULL,
    operation_id uuid NOT NULL UNIQUE,
    call_kind text NOT NULL CHECK (call_kind IN ('MODEL','TOOL')),
    model_call_id uuid UNIQUE REFERENCES agent.model_call(id) ON DELETE RESTRICT,
    tool_call_id uuid UNIQUE REFERENCES workflow.tool_call(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN ('RESERVED','SETTLED','UNKNOWN_CHARGED')),
    reserved_model_calls integer NOT NULL CHECK (reserved_model_calls >= 0),
    reserved_tool_calls integer NOT NULL CHECK (reserved_tool_calls >= 0),
    reserved_source_reads integer NOT NULL CHECK (reserved_source_reads >= 0),
    reserved_input_tokens bigint NOT NULL CHECK (reserved_input_tokens >= 0),
    reserved_output_tokens bigint NOT NULL CHECK (reserved_output_tokens >= 0),
    reserved_cost_microunits bigint,
    settled_model_calls integer NOT NULL DEFAULT 0 CHECK (settled_model_calls >= 0),
    settled_tool_calls integer NOT NULL DEFAULT 0 CHECK (settled_tool_calls >= 0),
    settled_source_reads integer NOT NULL DEFAULT 0 CHECK (settled_source_reads >= 0),
    settled_input_tokens bigint NOT NULL DEFAULT 0 CHECK (settled_input_tokens >= 0),
    settled_output_tokens bigint NOT NULL DEFAULT 0 CHECK (settled_output_tokens >= 0),
    settled_cost_microunits bigint,
    created_at timestamptz NOT NULL,
    settled_at timestamptz,
    CONSTRAINT fk_workspace_analysis_reservation_run
        FOREIGN KEY (analysis_run_id,workspace_id)
        REFERENCES agent.workspace_analysis_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_reservation_operation
        FOREIGN KEY (operation_id,analysis_run_id)
        REFERENCES agent.workspace_analysis_operation(id,analysis_run_id) ON DELETE RESTRICT,
    CONSTRAINT workspace_analysis_reservation_call CHECK (
        (call_kind='MODEL' AND model_call_id IS NOT NULL AND tool_call_id IS NULL)
        OR (call_kind='TOOL' AND tool_call_id IS NOT NULL AND model_call_id IS NULL)
    ),
    CONSTRAINT workspace_analysis_reservation_shape CHECK (
        (
            call_kind='MODEL'
            AND reserved_model_calls=1
            AND reserved_tool_calls=0
            AND reserved_source_reads=0
            AND reserved_input_tokens=65536
            AND reserved_output_tokens BETWEEN 1 AND 4096
            AND reserved_cost_microunits IS NULL
        )
        OR (
            call_kind='TOOL'
            AND reserved_model_calls=0
            AND reserved_tool_calls=1
            AND reserved_source_reads IN (0,1)
            AND reserved_input_tokens=0
            AND reserved_output_tokens=0
            AND reserved_cost_microunits IS NULL
        )
    ),
    CONSTRAINT workspace_analysis_reservation_settlement CHECK (
        settled_model_calls <= reserved_model_calls
        AND settled_tool_calls <= reserved_tool_calls
        AND settled_source_reads <= reserved_source_reads
        AND settled_input_tokens <= reserved_input_tokens
        AND settled_output_tokens <= reserved_output_tokens
        AND (
            (reserved_cost_microunits IS NULL AND settled_cost_microunits IS NULL)
            OR (
                reserved_cost_microunits IS NOT NULL
                AND reserved_cost_microunits >= 0
                AND settled_cost_microunits IS NOT NULL
                AND settled_cost_microunits BETWEEN 0 AND reserved_cost_microunits
            )
        )
        AND (
            (
                status='RESERVED'
                AND settled_at IS NULL
                AND settled_model_calls=0
                AND settled_tool_calls=0
                AND settled_source_reads=0
                AND settled_input_tokens=0
                AND settled_output_tokens=0
                AND (settled_cost_microunits IS NULL OR settled_cost_microunits=0)
            )
            OR (status='SETTLED' AND settled_at IS NOT NULL AND settled_at >= created_at)
            OR (
                status='UNKNOWN_CHARGED'
                AND settled_at IS NOT NULL
                AND settled_at >= created_at
                AND settled_model_calls=reserved_model_calls
                AND settled_tool_calls=reserved_tool_calls
                AND settled_source_reads=reserved_source_reads
                AND settled_input_tokens=reserved_input_tokens
                AND settled_output_tokens=reserved_output_tokens
                AND settled_cost_microunits IS NOT DISTINCT FROM reserved_cost_microunits
            )
        )
    )
);

CREATE INDEX idx_workspace_analysis_reservation_run
    ON agent.workspace_analysis_budget_reservation (analysis_run_id,status,id);

CREATE TABLE workflow.tool_result_receipt (
    id uuid PRIMARY KEY,
    tool_call_id uuid NOT NULL UNIQUE,
    workspace_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    node_run_id uuid NOT NULL,
    node_attempt_id uuid NOT NULL,
    tool_name text NOT NULL,
    tool_version bigint NOT NULL,
    output_schema_id text NOT NULL,
    output_schema_version bigint NOT NULL,
    definition_hash text NOT NULL CHECK (definition_hash ~ '^[0-9a-f]{64}$'),
    persistence_policy text NOT NULL CHECK (persistence_policy='PERSIST_CANONICAL'),
    max_output_bytes bigint NOT NULL,
    max_private_binding_bytes bigint NOT NULL,
    output_document bytea NOT NULL,
    output_hash text NOT NULL CHECK (output_hash ~ '^[0-9a-f]{64}$'),
    output_bytes bigint NOT NULL,
    server_binding_schema_id text,
    server_binding_schema_version bigint,
    server_binding_document bytea,
    server_binding_hash text CHECK (server_binding_hash IS NULL OR server_binding_hash ~ '^[0-9a-f]{64}$'),
    server_binding_bytes bigint,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_tool_result_receipt_workspace UNIQUE (id,workspace_id),
    CONSTRAINT fk_tool_result_receipt_call
        FOREIGN KEY (tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id)
        REFERENCES workflow.tool_call(id,workspace_id,workflow_run_id,node_run_id,node_attempt_id)
        ON DELETE RESTRICT,
    CONSTRAINT tool_result_receipt_output_integrity CHECK (
        output_bytes=octet_length(output_document)
        AND output_bytes BETWEEN 1 AND max_output_bytes
        AND output_hash=encode(sha256(output_document),'hex')
    ),
    CONSTRAINT tool_result_receipt_binding_integrity CHECK (
        (
            server_binding_schema_id IS NULL
            AND server_binding_schema_version IS NULL
            AND server_binding_document IS NULL
            AND server_binding_hash IS NULL
            AND server_binding_bytes IS NULL
        )
        OR (
            server_binding_schema_id IS NOT NULL
            AND server_binding_schema_version IS NOT NULL
            AND server_binding_document IS NOT NULL
            AND server_binding_hash IS NOT NULL
            AND server_binding_bytes IS NOT NULL
            AND server_binding_bytes=octet_length(server_binding_document)
            AND server_binding_bytes BETWEEN 1 AND max_private_binding_bytes
            AND server_binding_hash=encode(sha256(server_binding_document),'hex')
        )
    ),
    CONSTRAINT tool_result_receipt_exact_contract CHECK (
        (
            tool_name='ReadGitStatus'
            AND tool_version=2
            AND definition_hash='b5dd1fcca72d5bb41fd9ad3f74006d3706e4184ad39e2a3409b565d1ff896bbd'
            AND output_schema_id='tool.read_git_status.output'
            AND output_schema_version=1
            AND max_output_bytes=4096
            AND max_private_binding_bytes=1024
            AND (
                server_binding_schema_id IS NULL
                OR (
                    server_binding_schema_id='tool.read_git_status.private_binding'
                    AND server_binding_schema_version=1
                )
            )
        )
        OR (
            tool_name='SearchKnowledge'
            AND tool_version=2
            AND definition_hash='db7180086adb06a18a4be8d1eb80a208fa6385c70f1a71d6d67c4f263fbd1807'
            AND output_schema_id='tool.search_knowledge.output'
            AND output_schema_version=2
            AND max_output_bytes=32768
            AND max_private_binding_bytes=16384
            AND server_binding_schema_id='tool.search_knowledge.private_binding'
            AND server_binding_schema_version=1
        )
        OR (
            tool_name='ReadSource'
            AND tool_version=3
            AND definition_hash='d41dabac535261b885e453b64e11fe5bb779838e31a255aea4798e27ead54d32'
            AND output_schema_id='tool.read_source.output'
            AND output_schema_version=2
            AND max_output_bytes=8192
            AND max_private_binding_bytes=4096
            AND server_binding_schema_id='tool.read_source.private_binding'
            AND server_binding_schema_version=1
        )
        OR (
            tool_name='ValidateCitation'
            AND tool_version=3
            AND definition_hash='bf5e643c47d57478b46d258eca250dc30e810ee7a0693569f44edaab19037d54'
            AND output_schema_id='tool.validate_citation.output'
            AND output_schema_version=2
            AND max_output_bytes=16384
            AND max_private_binding_bytes=16384
            AND server_binding_schema_id='tool.validate_citation.private_binding'
            AND server_binding_schema_version=1
        )
    )
);

CREATE INDEX idx_tool_result_receipt_attempt
    ON workflow.tool_result_receipt (workflow_run_id,node_run_id,node_attempt_id,created_at,id);

CREATE TABLE workflow.tool_result_receipt_failure (
    id uuid PRIMARY KEY,
    tool_call_id uuid NOT NULL UNIQUE,
    operation_id uuid NOT NULL UNIQUE,
    analysis_run_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    node_run_id uuid NOT NULL,
    node_attempt_id uuid NOT NULL,
    failure_code text NOT NULL CHECK (failure_code IN (
        'MISSING','HASH_MISMATCH','CONTRACT_INVALID','BINDING_INVALID'
    )),
    expected_output_hash text NOT NULL CHECK (expected_output_hash ~ '^[0-9a-f]{64}$'),
    observed_output_hash text CHECK (observed_output_hash IS NULL OR observed_output_hash ~ '^[0-9a-f]{64}$'),
    observed_output_bytes bigint CHECK (observed_output_bytes IS NULL OR observed_output_bytes>=0),
    observed_binding_hash text CHECK (observed_binding_hash IS NULL OR observed_binding_hash ~ '^[0-9a-f]{64}$'),
    observed_binding_bytes bigint CHECK (observed_binding_bytes IS NULL OR observed_binding_bytes>=0),
    validator_version bigint NOT NULL CHECK (validator_version=1),
    checked_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL CHECK (created_at>=checked_at),
    CONSTRAINT uq_tool_result_receipt_failure_scope UNIQUE (id,analysis_run_id),
    CONSTRAINT fk_tool_result_receipt_failure_call
        FOREIGN KEY (tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id)
        REFERENCES workflow.tool_call(id,workspace_id,workflow_run_id,node_run_id,node_attempt_id)
        ON DELETE RESTRICT,
    CONSTRAINT fk_tool_result_receipt_failure_operation
        FOREIGN KEY (operation_id,analysis_run_id)
        REFERENCES agent.workspace_analysis_operation(id,analysis_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_tool_result_receipt_failure_run
        FOREIGN KEY (analysis_run_id,workspace_id,workflow_run_id)
        REFERENCES agent.workspace_analysis_run(id,workspace_id,workflow_run_id) ON DELETE RESTRICT,
    CONSTRAINT tool_result_receipt_failure_shape CHECK (
        (
            failure_code='MISSING'
            AND observed_output_hash IS NULL
            AND observed_output_bytes IS NULL
            AND observed_binding_hash IS NULL
            AND observed_binding_bytes IS NULL
        )
        OR (
            failure_code='HASH_MISMATCH'
            AND observed_output_hash IS NOT NULL
            AND observed_output_hash<>expected_output_hash
            AND observed_output_bytes IS NOT NULL
            AND observed_binding_hash IS NULL
            AND observed_binding_bytes IS NULL
        )
        OR (
            failure_code='CONTRACT_INVALID'
            AND observed_output_hash=expected_output_hash
            AND observed_output_bytes IS NOT NULL
            AND observed_binding_hash IS NULL
            AND observed_binding_bytes IS NULL
        )
        OR (
            failure_code='BINDING_INVALID'
            AND observed_output_hash=expected_output_hash
            AND observed_output_bytes IS NOT NULL
            AND observed_binding_hash IS NOT NULL
            AND observed_binding_bytes IS NOT NULL
        )
    )
);

CREATE TABLE agent.workspace_analysis_candidate (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    analysis_run_id uuid NOT NULL UNIQUE,
    answer_id uuid NOT NULL UNIQUE,
    synthesis_operation_id uuid NOT NULL UNIQUE,
    node_attempt_id uuid NOT NULL,
    synthesis_model_run_id uuid NOT NULL UNIQUE,
    schema_id text NOT NULL CHECK (schema_id='agent.workspace-analysis-candidate'),
    schema_version bigint NOT NULL CHECK (schema_version=1),
    document bytea NOT NULL,
    document_hash text NOT NULL CHECK (document_hash ~ '^[0-9a-f]{64}$'),
    document_bytes bigint NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_workspace_analysis_candidate_scope UNIQUE (id,analysis_run_id),
    CONSTRAINT fk_workspace_analysis_candidate_run
        FOREIGN KEY (analysis_run_id,workspace_id,answer_id)
        REFERENCES agent.workspace_analysis_run(id,workspace_id,answer_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_candidate_answer
        FOREIGN KEY (answer_id,workspace_id)
        REFERENCES agent.answer(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_candidate_operation
        FOREIGN KEY (synthesis_operation_id,analysis_run_id)
        REFERENCES agent.workspace_analysis_operation(id,analysis_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_candidate_attempt
        FOREIGN KEY (node_attempt_id)
        REFERENCES workflow.node_attempt(id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_candidate_model_run
        FOREIGN KEY (synthesis_model_run_id,workspace_id)
        REFERENCES agent.model_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT workspace_analysis_candidate_integrity CHECK (
        document_bytes=octet_length(document)
        AND document_bytes BETWEEN 1 AND 262144
        AND document_hash=encode(sha256(document),'hex')
    )
);

CREATE INDEX idx_workspace_analysis_candidate_answer
    ON agent.workspace_analysis_candidate (workspace_id,answer_id);

CREATE TABLE agent.workspace_analysis_model_result (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    analysis_run_id uuid NOT NULL,
    operation_id uuid NOT NULL UNIQUE,
    node_attempt_id uuid NOT NULL,
    model_run_id uuid NOT NULL UNIQUE,
    model_call_id uuid NOT NULL UNIQUE,
    operation_kind text NOT NULL CHECK (
        operation_kind IN ('RETRIEVAL_PLAN','FAITHFULNESS_REVIEW')
    ),
    schema_id text NOT NULL,
    schema_version text NOT NULL,
    subject_candidate_id uuid,
    subject_candidate_hash text CHECK (
        subject_candidate_hash IS NULL OR subject_candidate_hash ~ '^[0-9a-f]{64}$'
    ),
    document bytea NOT NULL,
    document_hash text NOT NULL CHECK (document_hash ~ '^[0-9a-f]{64}$'),
    document_bytes bigint NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_workspace_analysis_model_result_scope UNIQUE (id,analysis_run_id),
    CONSTRAINT fk_workspace_analysis_model_result_run
        FOREIGN KEY (analysis_run_id,workspace_id)
        REFERENCES agent.workspace_analysis_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_model_result_operation
        FOREIGN KEY (operation_id,analysis_run_id)
        REFERENCES agent.workspace_analysis_operation(id,analysis_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_model_result_attempt
        FOREIGN KEY (node_attempt_id)
        REFERENCES workflow.node_attempt(id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_model_result_model_run
        FOREIGN KEY (model_run_id,workspace_id)
        REFERENCES agent.model_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_model_result_model_call
        FOREIGN KEY (model_call_id)
        REFERENCES agent.model_call(id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_model_result_candidate
        FOREIGN KEY (subject_candidate_id,analysis_run_id)
        REFERENCES agent.workspace_analysis_candidate(id,analysis_run_id) ON DELETE RESTRICT,
    CONSTRAINT workspace_analysis_model_result_contract CHECK (
        (
            operation_kind='RETRIEVAL_PLAN'
            AND schema_id='agent.workspace-analysis-plan'
            AND schema_version='v1'
            AND subject_candidate_id IS NULL
            AND subject_candidate_hash IS NULL
        )
        OR (
            operation_kind='FAITHFULNESS_REVIEW'
            AND schema_id='agent.faithfulness-review'
            AND schema_version='v1'
            AND subject_candidate_id IS NOT NULL
            AND subject_candidate_hash IS NOT NULL
        )
    ),
    CONSTRAINT workspace_analysis_model_result_integrity CHECK (
        document_bytes=octet_length(document)
        AND document_bytes BETWEEN 1 AND 65536
        AND document_hash=encode(sha256(document),'hex')
    )
);

CREATE INDEX idx_workspace_analysis_model_result_run
    ON agent.workspace_analysis_model_result (analysis_run_id,operation_kind,id);

CREATE TABLE agent.workspace_analysis_publication_proof (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    analysis_run_id uuid NOT NULL UNIQUE,
    answer_id uuid NOT NULL UNIQUE,
    workflow_run_id uuid NOT NULL,
    finalization_node_run_id uuid NOT NULL,
    finalization_node_attempt_id uuid NOT NULL,
    candidate_id uuid NOT NULL UNIQUE,
    candidate_hash text NOT NULL CHECK (candidate_hash ~ '^[0-9a-f]{64}$'),
    git_receipt_id uuid NOT NULL UNIQUE,
    git_receipt_hash text NOT NULL CHECK (git_receipt_hash ~ '^[0-9a-f]{64}$'),
    validation_receipt_id uuid NOT NULL UNIQUE,
    validation_receipt_hash text NOT NULL CHECK (validation_receipt_hash ~ '^[0-9a-f]{64}$'),
    review_model_result_id uuid NOT NULL UNIQUE,
    review_document_hash text NOT NULL CHECK (review_document_hash ~ '^[0-9a-f]{64}$'),
    published_document bytea NOT NULL,
    published_result_hash text NOT NULL CHECK (published_result_hash ~ '^[0-9a-f]{64}$'),
    published_bytes bigint NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_workspace_analysis_publication_proof_scope UNIQUE (id,analysis_run_id),
    CONSTRAINT fk_workspace_analysis_publication_proof_run
        FOREIGN KEY (analysis_run_id,workspace_id,answer_id)
        REFERENCES agent.workspace_analysis_run(id,workspace_id,answer_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_publication_proof_workflow
        FOREIGN KEY (analysis_run_id,workspace_id,workflow_run_id)
        REFERENCES agent.workspace_analysis_run(id,workspace_id,workflow_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_publication_proof_node_run
        FOREIGN KEY (finalization_node_run_id,workflow_run_id)
        REFERENCES workflow.node_run(id,run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_publication_proof_node_attempt
        FOREIGN KEY (finalization_node_attempt_id,finalization_node_run_id)
        REFERENCES workflow.node_attempt(id,node_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_publication_proof_candidate
        FOREIGN KEY (candidate_id,analysis_run_id)
        REFERENCES agent.workspace_analysis_candidate(id,analysis_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_publication_proof_git_receipt
        FOREIGN KEY (git_receipt_id,workspace_id)
        REFERENCES workflow.tool_result_receipt(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_publication_proof_validation_receipt
        FOREIGN KEY (validation_receipt_id,workspace_id)
        REFERENCES workflow.tool_result_receipt(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_publication_proof_review_result
        FOREIGN KEY (review_model_result_id,analysis_run_id)
        REFERENCES agent.workspace_analysis_model_result(id,analysis_run_id) ON DELETE RESTRICT,
    CONSTRAINT workspace_analysis_publication_proof_integrity CHECK (
        published_bytes=octet_length(published_document)
        AND published_bytes BETWEEN 1 AND 262144
        AND published_result_hash=encode(sha256(published_document),'hex')
    )
);

CREATE INDEX idx_workspace_analysis_publication_proof_answer
    ON agent.workspace_analysis_publication_proof (workspace_id,answer_id);

CREATE TABLE agent.workspace_analysis_termination_proof (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    analysis_run_id uuid NOT NULL UNIQUE,
    answer_id uuid NOT NULL UNIQUE,
    workflow_run_id uuid NOT NULL,
    terminal_node_run_id uuid NOT NULL,
    terminal_node_attempt_id uuid NOT NULL,
    reason text NOT NULL CHECK (reason IN (
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
        'WORKSPACE_ANALYSIS_CANCELLED'
    )),
    operation_id uuid,
    artifact_kind text CHECK (artifact_kind IS NULL OR artifact_kind IN ('TOOL_RECEIPT','MODEL_RESULT')),
    artifact_id uuid,
    artifact_hash text CHECK (artifact_hash IS NULL OR artifact_hash ~ '^[0-9a-f]{64}$'),
    requested_model_calls integer CHECK (requested_model_calls IS NULL OR requested_model_calls>=0),
    requested_tool_calls integer CHECK (requested_tool_calls IS NULL OR requested_tool_calls>=0),
    requested_source_reads integer CHECK (requested_source_reads IS NULL OR requested_source_reads>=0),
    requested_input_tokens bigint CHECK (requested_input_tokens IS NULL OR requested_input_tokens>=0),
    requested_output_tokens bigint CHECK (requested_output_tokens IS NULL OR requested_output_tokens>=0),
    requested_cost_microunits bigint CHECK (requested_cost_microunits IS NULL OR requested_cost_microunits>=0),
    receipt_failure_code text CHECK (receipt_failure_code IS NULL OR receipt_failure_code IN (
        'MISSING','HASH_MISMATCH','CONTRACT_INVALID','BINDING_INVALID'
    )),
    receipt_failure_id uuid,
    expected_hash text CHECK (expected_hash IS NULL OR expected_hash ~ '^[0-9a-f]{64}$'),
    actual_hash text CHECK (actual_hash IS NULL OR actual_hash ~ '^[0-9a-f]{64}$'),
    published_model_run_id uuid,
    published_document bytea NOT NULL,
    published_result_hash text NOT NULL CHECK (published_result_hash ~ '^[0-9a-f]{64}$'),
    published_bytes bigint NOT NULL,
    checked_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL CHECK (created_at>=checked_at),
    CONSTRAINT uq_workspace_analysis_termination_proof_scope UNIQUE (id,analysis_run_id),
    CONSTRAINT fk_workspace_analysis_termination_proof_run
        FOREIGN KEY (analysis_run_id,workspace_id,answer_id)
        REFERENCES agent.workspace_analysis_run(id,workspace_id,answer_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_termination_proof_workflow
        FOREIGN KEY (analysis_run_id,workspace_id,workflow_run_id)
        REFERENCES agent.workspace_analysis_run(id,workspace_id,workflow_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_termination_proof_node_run
        FOREIGN KEY (terminal_node_run_id,workflow_run_id)
        REFERENCES workflow.node_run(id,run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_termination_proof_node_attempt
        FOREIGN KEY (terminal_node_attempt_id,terminal_node_run_id)
        REFERENCES workflow.node_attempt(id,node_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_termination_proof_operation
        FOREIGN KEY (operation_id,analysis_run_id)
        REFERENCES agent.workspace_analysis_operation(id,analysis_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_termination_proof_receipt_failure
        FOREIGN KEY (receipt_failure_id,analysis_run_id)
        REFERENCES workflow.tool_result_receipt_failure(id,analysis_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_termination_proof_model_run
        FOREIGN KEY (published_model_run_id,workspace_id,workflow_run_id)
        REFERENCES agent.model_run(id,workspace_id,workflow_run_id) ON DELETE RESTRICT,
    CONSTRAINT workspace_analysis_termination_proof_artifact_pair CHECK (
        (artifact_kind IS NULL AND artifact_id IS NULL AND artifact_hash IS NULL)
        OR (artifact_kind IS NOT NULL AND artifact_id IS NOT NULL AND artifact_hash IS NOT NULL)
    ),
    CONSTRAINT workspace_analysis_termination_proof_receipt_failure_shape CHECK (
        (reason='WORKSPACE_ANALYSIS_RECEIPT_INVALID')=(receipt_failure_code IS NOT NULL AND receipt_failure_id IS NOT NULL)
    ),
    CONSTRAINT workspace_analysis_termination_proof_publication_integrity CHECK (
        published_bytes=octet_length(published_document)
        AND published_bytes BETWEEN 1 AND 262144
        AND published_result_hash=encode(sha256(published_document),'hex')
    )
);

CREATE INDEX idx_workspace_analysis_termination_proof_answer
    ON agent.workspace_analysis_termination_proof (workspace_id,answer_id);

ALTER TABLE agent.workspace_analysis_run
    ADD CONSTRAINT fk_workspace_analysis_run_validation_receipt
        FOREIGN KEY (validation_receipt_id,workspace_id)
        REFERENCES workflow.tool_result_receipt(id,workspace_id) ON DELETE RESTRICT,
    ADD CONSTRAINT fk_workspace_analysis_run_review_model
        FOREIGN KEY (review_model_run_id,workspace_id,workflow_run_id)
        REFERENCES agent.model_run(id,workspace_id,workflow_run_id) ON DELETE RESTRICT;

-- +goose StatementBegin
CREATE FUNCTION agent.reject_workspace_analysis_fact_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE='55000';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION workflow.workspace_analysis_receipt_exact_object_keys(document jsonb, keys text[])
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT jsonb_typeof(document)='object'
       AND document ?& keys
       AND document-keys='{}'::jsonb
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION workflow.workspace_analysis_receipt_json_string(value text)
RETURNS text
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT replace(
        replace(
            replace(
                replace(
                    replace(to_json(value)::text, '&', chr(92)||'u0026'),
                    '<', chr(92)||'u003c'
                ),
                '>', chr(92)||'u003e'
            ),
            chr(8232), chr(92)||'u2028'
        ),
        chr(8233), chr(92)||'u2029'
    )
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION workflow.workspace_analysis_receipt_canonical_json(document json)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    document_type text;
    result text;
    item record;
    previous_key text;
    has_previous boolean := false;
BEGIN
    document_type := json_typeof(document);
    IF document_type='object' THEN
        result := '{';
        FOR item IN
            SELECT entry.key,entry.value
              FROM json_each(document) AS entry(key,value)
             ORDER BY entry.key COLLATE "C"
        LOOP
            IF has_previous AND item.key=previous_key THEN
                RAISE EXCEPTION 'tool result receipt JSON contains a duplicate key' USING ERRCODE='23514';
            END IF;
            IF has_previous THEN
                result := result||',';
            END IF;
            result := result
                ||workflow.workspace_analysis_receipt_json_string(item.key)
                ||':'
                ||workflow.workspace_analysis_receipt_canonical_json(item.value);
            previous_key := item.key;
            has_previous := true;
        END LOOP;
        RETURN result||'}';
    ELSIF document_type='array' THEN
        result := '[';
        FOR item IN
            SELECT entry.value,entry.ordinal
              FROM json_array_elements(document) WITH ORDINALITY AS entry(value,ordinal)
             ORDER BY entry.ordinal
        LOOP
            IF has_previous THEN
                result := result||',';
            END IF;
            result := result||workflow.workspace_analysis_receipt_canonical_json(item.value);
            has_previous := true;
        END LOOP;
        RETURN result||']';
    ELSIF document_type='string' THEN
        RETURN workflow.workspace_analysis_receipt_json_string(document #>> '{}');
    ELSIF document_type IN ('number','boolean','null') THEN
        RETURN btrim(document::text, E' \t\n\r\f\v');
    END IF;
    RAISE EXCEPTION 'tool result receipt JSON type is invalid' USING ERRCODE='23514';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION workflow.workspace_analysis_receipt_canonical_uuid(value text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT value ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION workflow.workspace_analysis_receipt_hash(value text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT value ~ '^[0-9a-f]{64}$'
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION workflow.workspace_analysis_receipt_reference(value text, max_bytes integer)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT value<>''
       AND value=btrim(value, E' \t\n\r\f\v')
       AND octet_length(value)<=max_bytes
       AND value !~ E'[\r\n]'
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION workflow.workspace_analysis_receipt_text(value text, max_bytes integer)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT octet_length(value) BETWEEN 1 AND max_bytes
       AND value ~ '[^[:space:]]'
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION workflow.workspace_analysis_receipt_identity_valid(document jsonb, maximum_evidence_ref integer)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    evidence_ref text;
BEGIN
    IF NOT workflow.workspace_analysis_receipt_exact_object_keys(document, ARRAY[
        'evidence_ref','citation_id','index_version_id','chunk_id','source_version_id','source_span_id','content_hash'
    ])
       OR jsonb_typeof(document->'evidence_ref')<>'string'
       OR jsonb_typeof(document->'citation_id')<>'string'
       OR jsonb_typeof(document->'index_version_id')<>'string'
       OR jsonb_typeof(document->'chunk_id')<>'string'
       OR jsonb_typeof(document->'source_version_id')<>'string'
       OR jsonb_typeof(document->'source_span_id')<>'string'
       OR jsonb_typeof(document->'content_hash')<>'string' THEN
        RETURN false;
    END IF;

    evidence_ref := document->>'evidence_ref';
    IF evidence_ref !~ '^E[1-9]$' OR substring(evidence_ref FROM 2)::integer>maximum_evidence_ref THEN
        RETURN false;
    END IF;
    RETURN (document->>'citation_id') ~ '^cite-[0-9a-f]{64}$'
       AND workflow.workspace_analysis_receipt_canonical_uuid(document->>'index_version_id')
       AND workflow.workspace_analysis_receipt_canonical_uuid(document->>'chunk_id')
       AND workflow.workspace_analysis_receipt_canonical_uuid(document->>'source_version_id')
       AND workflow.workspace_analysis_receipt_canonical_uuid(document->>'source_span_id')
       AND (document->>'index_version_id')<>(document->>'chunk_id')
       AND (document->>'index_version_id')<>(document->>'source_version_id')
       AND (document->>'index_version_id')<>(document->>'source_span_id')
       AND (document->>'chunk_id')<>(document->>'source_version_id')
       AND (document->>'chunk_id')<>(document->>'source_span_id')
       AND (document->>'source_version_id')<>(document->>'source_span_id')
       AND workflow.workspace_analysis_receipt_hash(document->>'content_hash');
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION workflow.guard_tool_result_receipt_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    call_row workflow.tool_call%ROWTYPE;
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    analysis_status_value text;
    output_document json;
    binding_document json;
    output_text text;
    binding_text text;
    output_json jsonb;
    binding_json jsonb;
    ordered_values text[];
    sorted_values text[];
    value_count bigint;
    distinct_value_count bigint;
BEGIN
    SELECT analysis.status
      INTO analysis_status_value
      FROM agent.workspace_analysis_operation AS operation
      JOIN agent.workspace_analysis_run AS analysis
        ON analysis.id=operation.analysis_run_id
       AND analysis.workspace_id=operation.workspace_id
       AND analysis.workflow_run_id=operation.workflow_run_id
     WHERE operation.tool_call_id=NEW.tool_call_id
       AND operation.workspace_id=NEW.workspace_id
       AND operation.workflow_run_id=NEW.workflow_run_id
       AND operation.node_run_id=NEW.node_run_id
       AND operation.first_node_attempt_id=NEW.node_attempt_id
     FOR UPDATE OF analysis;
    SELECT operation.*
      INTO operation_row
      FROM agent.workspace_analysis_operation AS operation
     WHERE operation.tool_call_id=NEW.tool_call_id
       AND operation.workspace_id=NEW.workspace_id
       AND operation.workflow_run_id=NEW.workflow_run_id
       AND operation.node_run_id=NEW.node_run_id
       AND operation.first_node_attempt_id=NEW.node_attempt_id
     FOR UPDATE;
    SELECT * INTO call_row
      FROM workflow.tool_call
     WHERE id=NEW.tool_call_id
       AND workspace_id=NEW.workspace_id
       AND workflow_run_id=NEW.workflow_run_id
       AND node_run_id=NEW.node_run_id
       AND node_attempt_id=NEW.node_attempt_id
     FOR UPDATE;

    IF operation_row.id IS NULL
       OR operation_row.call_kind<>'TOOL'
       OR operation_row.status<>'STARTED'
       OR analysis_status_value<>'running'
       OR EXISTS (
            SELECT 1 FROM workflow.tool_result_receipt_failure
             WHERE tool_call_id=NEW.tool_call_id
       )
       OR call_row.id IS NULL
       OR call_row.status<>'SUCCEEDED'
       OR call_row.requested_tool_name IS DISTINCT FROM NEW.tool_name
       OR call_row.tool_version IS DISTINCT FROM NEW.tool_version
       OR call_row.output_schema_id IS DISTINCT FROM NEW.output_schema_id
       OR call_row.output_schema_version IS DISTINCT FROM NEW.output_schema_version
       OR call_row.definition_hash IS DISTINCT FROM NEW.definition_hash
       OR call_row.capability IS DISTINCT FROM 'READ_LOCAL'
       OR call_row.side_effect_level IS DISTINCT FROM 'NONE'
       OR call_row.invocation_policy IS DISTINCT FROM 'TRUSTED_WORKFLOW_ONLY'
       OR call_row.response_hash IS DISTINCT FROM NEW.output_hash
       OR call_row.response_bytes IS DISTINCT FROM NEW.output_bytes
       OR call_row.result_ref IS NOT NULL
       OR call_row.side_effect_type IS NOT NULL
       OR call_row.side_effect_id IS NOT NULL
       OR call_row.completed_at IS NULL
       OR NEW.created_at<call_row.completed_at THEN
        RAISE EXCEPTION 'tool result receipt call binding is invalid' USING ERRCODE='55000';
    END IF;

    IF (NEW.tool_name='ReadGitStatus' AND (
            call_row.input_schema_id IS DISTINCT FROM 'tool.read_git_status.input'
            OR call_row.input_schema_version IS DISTINCT FROM 1
       )) OR (NEW.tool_name='SearchKnowledge' AND (
            call_row.input_schema_id IS DISTINCT FROM 'tool.search_knowledge.input'
            OR call_row.input_schema_version IS DISTINCT FROM 2
       )) OR (NEW.tool_name='ReadSource' AND (
            call_row.input_schema_id IS DISTINCT FROM 'tool.read_source.input'
            OR call_row.input_schema_version IS DISTINCT FROM 2
       )) OR (NEW.tool_name='ValidateCitation' AND (
            call_row.input_schema_id IS DISTINCT FROM 'tool.validate_citation.input'
            OR call_row.input_schema_version IS DISTINCT FROM 2
       )) THEN
        RAISE EXCEPTION 'tool result receipt input contract is invalid' USING ERRCODE='55000';
    END IF;

    output_text := convert_from(NEW.output_document,'UTF8');
    BEGIN
        output_document := output_text::json;
    EXCEPTION WHEN OTHERS THEN
        RAISE EXCEPTION 'tool result receipt output is not valid JSON' USING ERRCODE='23514';
    END;
    IF workflow.workspace_analysis_receipt_canonical_json(output_document)<>output_text THEN
        RAISE EXCEPTION 'tool result receipt output is not canonical JSON' USING ERRCODE='23514';
    END IF;
    output_json := output_document::jsonb;
    IF jsonb_typeof(output_json)<>'object' THEN
        RAISE EXCEPTION 'tool result receipt output is not an object' USING ERRCODE='23514';
    END IF;
    IF NEW.server_binding_document IS NOT NULL THEN
        binding_text := convert_from(NEW.server_binding_document,'UTF8');
        BEGIN
            binding_document := binding_text::json;
        EXCEPTION WHEN OTHERS THEN
            RAISE EXCEPTION 'tool result receipt binding is not valid JSON' USING ERRCODE='23514';
        END;
        IF workflow.workspace_analysis_receipt_canonical_json(binding_document)<>binding_text THEN
            RAISE EXCEPTION 'tool result receipt binding is not canonical JSON' USING ERRCODE='23514';
        END IF;
        binding_json := binding_document::jsonb;
        IF jsonb_typeof(binding_json)<>'object' THEN
            RAISE EXCEPTION 'tool result receipt binding is not an object' USING ERRCODE='23514';
        END IF;
    END IF;

    IF NEW.tool_name='ReadGitStatus' THEN
        IF NOT workflow.workspace_analysis_receipt_exact_object_keys(output_json, ARRAY[
            'branch','head','object_format','clean','staged_count','unstaged_count','untracked_count','conflict_count'
        ])
           OR jsonb_typeof(output_json->'branch')<>'string'
           OR jsonb_typeof(output_json->'head')<>'string'
           OR jsonb_typeof(output_json->'object_format')<>'string'
           OR jsonb_typeof(output_json->'clean')<>'boolean'
           OR jsonb_typeof(output_json->'staged_count')<>'number'
           OR jsonb_typeof(output_json->'unstaged_count')<>'number'
           OR jsonb_typeof(output_json->'untracked_count')<>'number'
           OR jsonb_typeof(output_json->'conflict_count')<>'number'
           OR NOT workflow.workspace_analysis_receipt_reference(output_json->>'branch', 255)
           OR (output_json->>'object_format') NOT IN ('sha1','sha256')
           OR ((output_json->>'object_format')='sha1' AND (output_json->>'head') !~ '^[0-9a-f]{40}$')
           OR ((output_json->>'object_format')='sha256' AND (output_json->>'head') !~ '^[0-9a-f]{64}$')
           OR (output_json->>'staged_count') !~ '^(0|[1-9][0-9]*)$'
           OR (output_json->>'unstaged_count') !~ '^(0|[1-9][0-9]*)$'
           OR (output_json->>'untracked_count') !~ '^(0|[1-9][0-9]*)$'
           OR (output_json->>'conflict_count') !~ '^(0|[1-9][0-9]*)$'
           OR (output_json->>'staged_count')::bigint>100000
           OR (output_json->>'unstaged_count')::bigint>100000
           OR (output_json->>'untracked_count')::bigint>100000
           OR (output_json->>'conflict_count')::bigint>100000
           OR (output_json->>'clean')::boolean<>(
                (output_json->>'staged_count')::bigint
                +(output_json->>'unstaged_count')::bigint
                +(output_json->>'untracked_count')::bigint
                +(output_json->>'conflict_count')::bigint=0
           ) THEN
            RAISE EXCEPTION 'ReadGitStatus receipt document is invalid' USING ERRCODE='23514';
        END IF;
        IF binding_json IS NOT NULL AND (
            NOT workflow.workspace_analysis_receipt_exact_object_keys(binding_json, ARRAY['workspace_root_hash','repository_identity_hash'])
            OR jsonb_typeof(binding_json->'workspace_root_hash')<>'string'
            OR jsonb_typeof(binding_json->'repository_identity_hash')<>'string'
            OR NOT workflow.workspace_analysis_receipt_hash(binding_json->>'workspace_root_hash')
            OR NOT workflow.workspace_analysis_receipt_hash(binding_json->>'repository_identity_hash')
        ) THEN
            RAISE EXCEPTION 'ReadGitStatus receipt binding is invalid' USING ERRCODE='23514';
        END IF;
    ELSIF NEW.tool_name='SearchKnowledge' THEN
        IF NOT workflow.workspace_analysis_receipt_exact_object_keys(output_json, ARRAY['effective_mode','items','degradations'])
           OR jsonb_typeof(output_json->'effective_mode')<>'string'
           OR jsonb_typeof(output_json->'items')<>'array'
           OR jsonb_typeof(output_json->'degradations')<>'array'
           OR (output_json->>'effective_mode') NOT IN ('keyword','semantic','hybrid')
           OR jsonb_array_length(output_json->'items')>5
           OR jsonb_array_length(output_json->'degradations')>16
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(output_json->'items') WITH ORDINALITY AS item(document, ordinal)
                 WHERE NOT workflow.workspace_analysis_receipt_exact_object_keys(item.document, ARRAY['evidence_ref','rank','snippet'])
                    OR jsonb_typeof(item.document->'evidence_ref')<>'string'
                    OR jsonb_typeof(item.document->'rank')<>'number'
                    OR jsonb_typeof(item.document->'snippet')<>'string'
                    OR item.document->>'evidence_ref'<>'E'||item.ordinal::text
                    OR item.document->>'rank'<>item.ordinal::text
                    OR NOT workflow.workspace_analysis_receipt_text(item.document->>'snippet', 4096)
           )
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(output_json->'degradations') AS degradation(value)
                 WHERE jsonb_typeof(degradation.value)<>'string'
                    OR degradation.value #>> '{}' !~ '^[A-Z][A-Z0-9_]{0,127}$'
           ) THEN
            RAISE EXCEPTION 'SearchKnowledge receipt output is invalid' USING ERRCODE='23514';
        END IF;
        SELECT array_agg(value #>> '{}' ORDER BY ordinal), array_agg(value #>> '{}' ORDER BY value #>> '{}'), count(*), count(DISTINCT value #>> '{}')
          INTO ordered_values, sorted_values, value_count, distinct_value_count
          FROM jsonb_array_elements(output_json->'degradations') WITH ORDINALITY AS degradation(value, ordinal);
        IF ordered_values IS DISTINCT FROM sorted_values OR value_count<>distinct_value_count THEN
            RAISE EXCEPTION 'SearchKnowledge receipt degradations are invalid' USING ERRCODE='23514';
        END IF;
        IF NOT workflow.workspace_analysis_receipt_exact_object_keys(binding_json, ARRAY['items','selected_refs'])
           OR jsonb_typeof(binding_json->'items')<>'array'
           OR jsonb_typeof(binding_json->'selected_refs')<>'array'
           OR jsonb_array_length(binding_json->'items')<>jsonb_array_length(output_json->'items')
           OR jsonb_array_length(binding_json->'items')>5
           OR jsonb_array_length(binding_json->'selected_refs')<>LEAST(3,jsonb_array_length(binding_json->'items'))
           OR EXISTS (
                SELECT 1 FROM jsonb_array_elements(binding_json->'items') WITH ORDINALITY AS item(document, ordinal)
                 WHERE NOT workflow.workspace_analysis_receipt_identity_valid(item.document, 5)
                    OR item.document->>'evidence_ref'<>'E'||item.ordinal::text
           )
           OR EXISTS (
                SELECT 1 FROM jsonb_array_elements(binding_json->'selected_refs') WITH ORDINALITY AS selection(value, ordinal)
                 WHERE jsonb_typeof(selection.value)<>'string'
                    OR selection.value #>> '{}' <>'E'||selection.ordinal::text
           )
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(output_json->'items') WITH ORDINALITY AS output_item(document, ordinal)
                  JOIN jsonb_array_elements(binding_json->'items') WITH ORDINALITY AS binding_item(document, ordinal)
                    USING (ordinal)
                 WHERE output_item.document->>'evidence_ref'<>binding_item.document->>'evidence_ref'
           ) THEN
            RAISE EXCEPTION 'SearchKnowledge receipt binding is invalid' USING ERRCODE='23514';
        END IF;
        SELECT count(*), count(DISTINCT document->>'citation_id')
          INTO value_count, distinct_value_count
          FROM jsonb_array_elements(binding_json->'items') AS item(document);
        IF value_count<>distinct_value_count THEN
            RAISE EXCEPTION 'SearchKnowledge receipt citation identities are duplicated' USING ERRCODE='23514';
        END IF;
    ELSIF NEW.tool_name='ReadSource' THEN
        IF NOT workflow.workspace_analysis_receipt_exact_object_keys(output_json, ARRAY['evidence_ref','content_hash','truncated','excerpt'])
           OR jsonb_typeof(output_json->'evidence_ref')<>'string'
           OR jsonb_typeof(output_json->'content_hash')<>'string'
           OR jsonb_typeof(output_json->'truncated')<>'boolean'
           OR jsonb_typeof(output_json->'excerpt')<>'string'
           OR (output_json->>'evidence_ref') !~ '^E[1-3]$'
           OR NOT workflow.workspace_analysis_receipt_hash(output_json->>'content_hash')
           OR NOT workflow.workspace_analysis_receipt_text(output_json->>'excerpt', 4096) THEN
            RAISE EXCEPTION 'ReadSource receipt output is invalid' USING ERRCODE='23514';
        END IF;
        IF NOT workflow.workspace_analysis_receipt_exact_object_keys(binding_json, ARRAY[
            'search_receipt_id','search_receipt_hash','evidence_ref','citation_id','index_version_id','chunk_id','source_version_id','source_span_id','content_hash'
        ])
           OR jsonb_typeof(binding_json->'search_receipt_id')<>'string'
           OR jsonb_typeof(binding_json->'search_receipt_hash')<>'string'
           OR NOT workflow.workspace_analysis_receipt_canonical_uuid(binding_json->>'search_receipt_id')
           OR NOT workflow.workspace_analysis_receipt_hash(binding_json->>'search_receipt_hash')
           OR NOT workflow.workspace_analysis_receipt_identity_valid(binding_json-ARRAY['search_receipt_id','search_receipt_hash'], 3)
           OR output_json->>'evidence_ref'<>binding_json->>'evidence_ref'
           OR output_json->>'content_hash'<>binding_json->>'content_hash'
           OR NOT EXISTS (
                SELECT 1
                  FROM workflow.tool_result_receipt AS search_receipt
                  JOIN agent.workspace_analysis_operation AS search_operation
                    ON search_operation.result_id=search_receipt.id
                   AND search_operation.result_hash=search_receipt.output_hash
                   AND search_operation.tool_call_id=search_receipt.tool_call_id
                 WHERE search_receipt.id=(binding_json->>'search_receipt_id')::uuid
                   AND search_receipt.workspace_id=NEW.workspace_id
                   AND search_receipt.workflow_run_id=NEW.workflow_run_id
                   AND search_receipt.tool_name='SearchKnowledge'
                   AND search_receipt.tool_version=2
                   AND search_receipt.output_hash=binding_json->>'search_receipt_hash'
                   AND search_operation.analysis_run_id=operation_row.analysis_run_id
                   AND search_operation.workspace_id=NEW.workspace_id
                   AND search_operation.workflow_run_id=NEW.workflow_run_id
                   AND search_operation.operation_kind='KNOWLEDGE_SEARCH'
                   AND search_operation.ordinal=1
                   AND search_operation.call_kind='TOOL'
                   AND search_operation.status='SUCCEEDED'
                   AND search_operation.result_kind='TOOL_RESULT_RECEIPT'
           ) THEN
            RAISE EXCEPTION 'ReadSource receipt binding is invalid' USING ERRCODE='23514';
        END IF;
    ELSIF NEW.tool_name='ValidateCitation' THEN
        IF NOT workflow.workspace_analysis_receipt_exact_object_keys(output_json, ARRAY['results'])
           OR jsonb_typeof(output_json->'results')<>'array'
           OR jsonb_array_length(output_json->'results') NOT BETWEEN 1 AND 3
           OR EXISTS (
                SELECT 1 FROM jsonb_array_elements(output_json->'results') AS result(document)
                 WHERE NOT workflow.workspace_analysis_receipt_exact_object_keys(result.document, ARRAY['evidence_ref','valid','reason_code'])
                    OR jsonb_typeof(result.document->'evidence_ref')<>'string'
                    OR jsonb_typeof(result.document->'valid')<>'boolean'
                    OR jsonb_typeof(result.document->'reason_code')<>'string'
                    OR result.document->>'evidence_ref' !~ '^E[1-3]$'
                    OR result.document->>'reason_code' NOT IN ('OK','CITATION_UNRESOLVABLE','EVIDENCE_INELIGIBLE','BINDING_MISMATCH')
                    OR (result.document->>'valid')::boolean<>(result.document->>'reason_code'='OK')
           ) THEN
            RAISE EXCEPTION 'ValidateCitation receipt output is invalid' USING ERRCODE='23514';
        END IF;
        SELECT count(*), count(DISTINCT document->>'evidence_ref')
          INTO value_count, distinct_value_count
          FROM jsonb_array_elements(output_json->'results') AS result(document);
        IF value_count<>distinct_value_count THEN
            RAISE EXCEPTION 'ValidateCitation receipt output references are duplicated' USING ERRCODE='23514';
        END IF;
        IF NOT workflow.workspace_analysis_receipt_exact_object_keys(binding_json, ARRAY['candidate_id','candidate_hash','results'])
           OR jsonb_typeof(binding_json->'candidate_id')<>'string'
           OR jsonb_typeof(binding_json->'candidate_hash')<>'string'
           OR jsonb_typeof(binding_json->'results')<>'array'
           OR NOT workflow.workspace_analysis_receipt_canonical_uuid(binding_json->>'candidate_id')
           OR NOT workflow.workspace_analysis_receipt_hash(binding_json->>'candidate_hash')
           OR jsonb_array_length(binding_json->'results')<>jsonb_array_length(output_json->'results')
           OR EXISTS (
                SELECT 1 FROM jsonb_array_elements(binding_json->'results') AS result(document)
                 WHERE NOT workflow.workspace_analysis_receipt_identity_valid(result.document, 3)
           )
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(output_json->'results') WITH ORDINALITY AS output_result(document, ordinal)
                  JOIN jsonb_array_elements(binding_json->'results') WITH ORDINALITY AS binding_result(document, ordinal)
                    USING (ordinal)
                 WHERE output_result.document->>'evidence_ref'<>binding_result.document->>'evidence_ref'
           )
           OR NOT EXISTS (
                SELECT 1 FROM agent.workspace_analysis_candidate AS candidate
                 WHERE candidate.id=(binding_json->>'candidate_id')::uuid
                   AND candidate.workspace_id=NEW.workspace_id
                   AND candidate.analysis_run_id=operation_row.analysis_run_id
                   AND candidate.document_hash=binding_json->>'candidate_hash'
           ) THEN
            RAISE EXCEPTION 'ValidateCitation receipt binding is invalid' USING ERRCODE='23514';
        END IF;
        SELECT count(*), count(DISTINCT document->>'evidence_ref')
          INTO value_count, distinct_value_count
          FROM jsonb_array_elements(binding_json->'results') AS result(document);
        IF value_count<>distinct_value_count THEN
            RAISE EXCEPTION 'ValidateCitation receipt binding references are duplicated' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION workflow.guard_tool_result_receipt_failure_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    analysis_status_value text;
    call_row workflow.tool_call%ROWTYPE;
BEGIN
    SELECT analysis.status
      INTO analysis_status_value
      FROM agent.workspace_analysis_operation AS operation
      JOIN agent.workspace_analysis_run AS analysis
        ON analysis.id=operation.analysis_run_id
       AND analysis.workspace_id=operation.workspace_id
       AND analysis.workflow_run_id=operation.workflow_run_id
     WHERE operation.id=NEW.operation_id
       AND operation.analysis_run_id=NEW.analysis_run_id
       AND operation.tool_call_id=NEW.tool_call_id
       AND operation.workspace_id=NEW.workspace_id
       AND operation.workflow_run_id=NEW.workflow_run_id
       AND operation.node_run_id=NEW.node_run_id
       AND operation.first_node_attempt_id=NEW.node_attempt_id
     FOR UPDATE OF analysis;
    SELECT operation.*
      INTO operation_row
      FROM agent.workspace_analysis_operation AS operation
     WHERE operation.id=NEW.operation_id
       AND operation.analysis_run_id=NEW.analysis_run_id
       AND operation.tool_call_id=NEW.tool_call_id
       AND operation.workspace_id=NEW.workspace_id
       AND operation.workflow_run_id=NEW.workflow_run_id
       AND operation.node_run_id=NEW.node_run_id
       AND operation.first_node_attempt_id=NEW.node_attempt_id
     FOR UPDATE;
    SELECT * INTO call_row
      FROM workflow.tool_call
     WHERE id=NEW.tool_call_id
       AND workspace_id=NEW.workspace_id
       AND workflow_run_id=NEW.workflow_run_id
       AND node_run_id=NEW.node_run_id
       AND node_attempt_id=NEW.node_attempt_id
     FOR UPDATE;

    IF operation_row.id IS NULL
       OR operation_row.call_kind<>'TOOL'
       OR operation_row.status<>'STARTED'
       OR analysis_status_value<>'running'
       OR call_row.id IS NULL
       OR call_row.status<>'SUCCEEDED'
       OR call_row.response_hash IS NULL
       OR call_row.response_bytes IS NULL
       OR call_row.completed_at IS NULL
       OR NEW.expected_output_hash IS DISTINCT FROM call_row.response_hash
       OR NEW.checked_at<call_row.completed_at
       OR NEW.checked_at>clock_timestamp()
       OR NEW.created_at>clock_timestamp()
       OR NEW.observed_output_bytes>1048576
       OR NEW.observed_binding_bytes>65536
       OR (
            NEW.failure_code IN ('CONTRACT_INVALID','BINDING_INVALID')
            AND NEW.observed_output_bytes IS DISTINCT FROM call_row.response_bytes
       )
       OR EXISTS (
            SELECT 1 FROM workflow.tool_result_receipt
             WHERE tool_call_id=NEW.tool_call_id
       ) THEN
        RAISE EXCEPTION 'tool result receipt failure binding is invalid' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_candidate_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    model_run_row agent.model_run%ROWTYPE;
    model_call_row agent.model_call%ROWTYPE;
    candidate_json jsonb;
    candidate_payload jsonb;
    proposal_json jsonb;
BEGIN
    SELECT * INTO operation_row
     FROM agent.workspace_analysis_operation
     WHERE id=NEW.synthesis_operation_id
       AND analysis_run_id=NEW.analysis_run_id
     FOR SHARE;
    SELECT * INTO model_run_row
     FROM agent.model_run
     WHERE id=NEW.synthesis_model_run_id
       AND workspace_id=NEW.workspace_id
     FOR SHARE;
    SELECT * INTO model_call_row
      FROM agent.model_call
     WHERE id=operation_row.model_call_id
       AND model_run_id=NEW.synthesis_model_run_id
     FOR SHARE;

    IF operation_row.id IS NULL
       OR operation_row.operation_kind<>'ANSWER_SYNTHESIS'
       OR operation_row.status<>'STARTED'
       OR operation_row.first_node_attempt_id<>NEW.node_attempt_id
       OR model_run_row.id IS NULL
       OR model_run_row.workflow_run_id<>operation_row.workflow_run_id
       OR model_run_row.node_run_id<>operation_row.node_run_id
       OR model_run_row.node_attempt_id<>NEW.node_attempt_id
       OR model_run_row.status<>'SUCCEEDED'
       OR model_run_row.final_result_type<>'workspace_analysis_answer'
       OR model_run_row.completed_at IS NULL
       OR model_run_row.output_schema_id<>NEW.schema_id
       OR model_run_row.output_schema_version<>NEW.schema_version::text
       OR model_call_row.id IS NULL
       OR model_call_row.status<>'SUCCEEDED'
       OR model_call_row.response_hash<>NEW.document_hash
       OR model_call_row.response_bytes<>NEW.document_bytes
       OR model_call_row.completed_at IS NULL
       OR NEW.created_at<model_run_row.completed_at
       OR NEW.created_at<model_call_row.completed_at THEN
        RAISE EXCEPTION 'workspace analysis candidate binding is invalid' USING ERRCODE='55000';
    END IF;

    candidate_json := convert_from(NEW.document,'UTF8')::jsonb;
    candidate_payload := candidate_json->'payload';
    proposal_json := candidate_payload->'proposal_suggestion';
    IF jsonb_typeof(candidate_json) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(candidate_json))<>5
       OR candidate_json->>'result_type' IS DISTINCT FROM 'workspace_analysis_candidate'
       OR candidate_json->>'schema_id' IS DISTINCT FROM NEW.schema_id
       OR candidate_json->>'schema_version' IS DISTINCT FROM NEW.schema_version::text
       OR candidate_json->>'model_run_ref' IS DISTINCT FROM NEW.synthesis_model_run_id::text
       OR jsonb_typeof(candidate_payload) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(candidate_payload))<>3
       OR jsonb_typeof(candidate_payload->'answer_markdown') IS DISTINCT FROM 'string'
       OR octet_length(convert_to(candidate_payload->>'answer_markdown','UTF8')) NOT BETWEEN 1 AND 65536
       OR candidate_payload->>'answer_markdown' IS DISTINCT FROM btrim(candidate_payload->>'answer_markdown')
       OR jsonb_typeof(candidate_payload->'citation_refs') IS DISTINCT FROM 'array'
       OR jsonb_array_length(candidate_payload->'citation_refs') NOT BETWEEN 1 AND 3
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(candidate_payload->'citation_refs') AS item(value)
             WHERE jsonb_typeof(value) IS DISTINCT FROM 'string'
                OR value#>>'{}' !~ '^E[1-3]$'
       )
       OR (
            SELECT count(DISTINCT value#>>'{}')
              FROM jsonb_array_elements(candidate_payload->'citation_refs') AS item(value)
       )<>jsonb_array_length(candidate_payload->'citation_refs')
       OR proposal_json IS NULL
       OR jsonb_typeof(proposal_json) NOT IN ('null','object') THEN
        RAISE EXCEPTION 'workspace analysis candidate document is invalid' USING ERRCODE='23514';
    END IF;
    IF jsonb_typeof(proposal_json)='object'
       AND (
            (SELECT count(*) FROM jsonb_object_keys(proposal_json))<>2
            OR jsonb_typeof(proposal_json->'summary') IS DISTINCT FROM 'string'
            OR octet_length(convert_to(proposal_json->>'summary','UTF8')) NOT BETWEEN 1 AND 4096
            OR proposal_json->>'summary' IS DISTINCT FROM btrim(proposal_json->>'summary')
            OR jsonb_typeof(proposal_json->'citation_refs') IS DISTINCT FROM 'array'
            OR jsonb_array_length(proposal_json->'citation_refs') NOT BETWEEN 1 AND 3
            OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(proposal_json->'citation_refs') AS item(value)
                 WHERE jsonb_typeof(value) IS DISTINCT FROM 'string'
                    OR value#>>'{}' !~ '^E[1-3]$'
                    OR NOT (candidate_payload->'citation_refs' @> jsonb_build_array(value))
            )
            OR (
                SELECT count(DISTINCT value#>>'{}')
                  FROM jsonb_array_elements(proposal_json->'citation_refs') AS item(value)
            )<>jsonb_array_length(proposal_json->'citation_refs')
       ) THEN
        RAISE EXCEPTION 'workspace analysis candidate proposal is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_model_result_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    model_run_row agent.model_run%ROWTYPE;
    model_call_row agent.model_call%ROWTYPE;
    candidate_row agent.workspace_analysis_candidate%ROWTYPE;
    candidate_json jsonb;
    result_json jsonb;
    expected_final_result_type text;
BEGIN
    SELECT * INTO operation_row
      FROM agent.workspace_analysis_operation
     WHERE id=NEW.operation_id
       AND analysis_run_id=NEW.analysis_run_id
     FOR SHARE;
    SELECT * INTO model_run_row
      FROM agent.model_run
     WHERE id=NEW.model_run_id
       AND workspace_id=NEW.workspace_id
     FOR SHARE;
    SELECT * INTO model_call_row
      FROM agent.model_call
     WHERE id=NEW.model_call_id
       AND model_run_id=NEW.model_run_id
     FOR SHARE;

    expected_final_result_type := CASE NEW.operation_kind
        WHEN 'RETRIEVAL_PLAN' THEN 'workspace_analysis_plan'
        WHEN 'FAITHFULNESS_REVIEW' THEN 'faithfulness_review'
        ELSE NULL
    END;
    IF operation_row.id IS NULL
       OR operation_row.operation_kind<>NEW.operation_kind
       OR operation_row.status<>'STARTED'
       OR operation_row.model_call_id<>NEW.model_call_id
       OR operation_row.first_node_attempt_id<>NEW.node_attempt_id
       OR operation_row.latest_node_attempt_id<>NEW.node_attempt_id
       OR model_run_row.id IS NULL
       OR model_run_row.workflow_run_id<>operation_row.workflow_run_id
       OR model_run_row.node_run_id<>operation_row.node_run_id
       OR model_run_row.node_attempt_id<>NEW.node_attempt_id
       OR model_run_row.status<>'SUCCEEDED'
       OR model_run_row.final_result_type<>expected_final_result_type
       OR model_run_row.output_schema_id<>NEW.schema_id
       OR model_run_row.output_schema_version<>NEW.schema_version
       OR model_run_row.completed_at IS NULL
       OR model_call_row.id IS NULL
       OR model_call_row.status<>'SUCCEEDED'
       OR model_call_row.output_schema_id<>NEW.schema_id
       OR model_call_row.output_schema_version<>NEW.schema_version
       OR model_call_row.response_hash<>NEW.document_hash
       OR model_call_row.response_bytes<>NEW.document_bytes
       OR model_call_row.completed_at IS NULL
       OR NEW.created_at<model_run_row.completed_at
       OR NEW.created_at<model_call_row.completed_at THEN
        RAISE EXCEPTION 'workspace analysis model result binding is invalid' USING ERRCODE='55000';
    END IF;

    IF NEW.operation_kind='FAITHFULNESS_REVIEW' THEN
        SELECT * INTO candidate_row
          FROM agent.workspace_analysis_candidate
         WHERE id=NEW.subject_candidate_id
           AND analysis_run_id=NEW.analysis_run_id
         FOR SHARE;
        IF candidate_row.id IS NULL
           OR candidate_row.document_hash<>NEW.subject_candidate_hash
           OR candidate_row.created_at>NEW.created_at THEN
            RAISE EXCEPTION 'workspace analysis review candidate binding is invalid' USING ERRCODE='55000';
        END IF;
    END IF;

    result_json := convert_from(NEW.document,'UTF8')::jsonb;
    IF jsonb_typeof(result_json)<>'object'
       OR result_json->>'schema_id' IS DISTINCT FROM NEW.schema_id
       OR result_json->>'schema_version' IS DISTINCT FROM NEW.schema_version
       OR result_json->>'model_run_ref' IS DISTINCT FROM NEW.model_run_id::text
       OR jsonb_typeof(result_json->'payload') IS DISTINCT FROM 'object'
       OR (
            NEW.operation_kind='RETRIEVAL_PLAN'
            AND result_json->>'result_type' IS DISTINCT FROM 'workspace_analysis_plan'
       )
       OR (
            NEW.operation_kind='FAITHFULNESS_REVIEW'
            AND (
                result_json->>'result_type' IS DISTINCT FROM 'faithfulness_review'
                OR jsonb_typeof(result_json->'payload'->'passed') IS DISTINCT FROM 'boolean'
            )
       ) THEN
        RAISE EXCEPTION 'workspace analysis model result document is invalid' USING ERRCODE='23514';
    END IF;

    IF NEW.operation_kind='FAITHFULNESS_REVIEW' THEN
        candidate_json := convert_from(candidate_row.document,'UTF8')::jsonb;
        IF jsonb_typeof(candidate_json#>'{payload,citation_refs}') IS DISTINCT FROM 'array'
           OR jsonb_typeof(result_json#>'{payload,items}') IS DISTINCT FROM 'array'
           OR jsonb_array_length(result_json#>'{payload,items}')<>1
           OR result_json#>>'{payload,items,0,assertion_id}' IS DISTINCT FROM '@answer/conclusion'
           OR jsonb_typeof(result_json#>'{payload,items,0,citation_ids}') IS DISTINCT FROM 'array'
           OR jsonb_array_length(result_json#>'{payload,items,0,citation_ids}')<>
              jsonb_array_length(candidate_json#>'{payload,citation_refs}')
           OR NOT ((result_json#>'{payload,items,0,citation_ids}') @> (candidate_json#>'{payload,citation_refs}'))
           OR NOT ((candidate_json#>'{payload,citation_refs}') @> (result_json#>'{payload,items,0,citation_ids}'))
           OR (
                result_json#>'{payload,passed}' IS NOT DISTINCT FROM 'true'::jsonb
                AND result_json#>>'{payload,items,0,verdict}' IS DISTINCT FROM 'SUPPORTED'
           )
           OR (
                result_json#>'{payload,passed}' IS NOT DISTINCT FROM 'false'::jsonb
                AND result_json#>>'{payload,items,0,verdict}' IS DISTINCT FROM 'UNSUPPORTED'
           ) THEN
            RAISE EXCEPTION 'workspace analysis review semantic closure is invalid' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- The v1 graph is fixed. These predicates make the persisted predecessor
-- facts authoritative for both call authorization and pre-call termination.
-- +goose StatementBegin
CREATE FUNCTION agent.workspace_analysis_selected_refs(target_run_id uuid)
RETURNS jsonb
LANGUAGE plpgsql
VOLATILE
SECURITY INVOKER
SET search_path = pg_catalog, agent, workflow
AS $$
DECLARE
    output_json jsonb;
    binding_json jsonb;
    selected_refs jsonb;
    expected_refs jsonb;
BEGIN
    SELECT convert_from(receipt.output_document,'UTF8')::jsonb,
           convert_from(receipt.server_binding_document,'UTF8')::jsonb
      INTO output_json,binding_json
      FROM agent.workspace_analysis_operation AS operation
      JOIN workflow.tool_result_receipt AS receipt
        ON receipt.id=operation.result_id
       AND receipt.tool_call_id=operation.tool_call_id
     WHERE operation.analysis_run_id=target_run_id
       AND operation.operation_kind='KNOWLEDGE_SEARCH'
       AND operation.status='SUCCEEDED'
       AND receipt.tool_name='SearchKnowledge'
       AND receipt.tool_version=2;
    IF NOT FOUND
       OR jsonb_typeof(output_json->'items') IS DISTINCT FROM 'array'
       OR jsonb_typeof(binding_json->'items') IS DISTINCT FROM 'array'
       OR jsonb_typeof(binding_json->'selected_refs') IS DISTINCT FROM 'array' THEN
        RETURN NULL;
    END IF;

    selected_refs := binding_json->'selected_refs';
    IF jsonb_array_length(binding_json->'items') NOT BETWEEN 1 AND 5
       OR jsonb_array_length(output_json->'items')<>jsonb_array_length(binding_json->'items')
       OR jsonb_array_length(selected_refs)<>LEAST(3,jsonb_array_length(binding_json->'items'))
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(binding_json->'items') AS entry(item)
             WHERE jsonb_typeof(item->'evidence_ref') IS DISTINCT FROM 'string'
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(selected_refs) AS entry(value)
             WHERE jsonb_typeof(value) IS DISTINCT FROM 'string'
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(output_json->'items') WITH ORDINALITY AS output_entry(item,ordinal)
              JOIN jsonb_array_elements(binding_json->'items') WITH ORDINALITY AS binding_entry(item,ordinal)
                USING (ordinal)
             WHERE output_entry.item->>'evidence_ref' IS DISTINCT FROM binding_entry.item->>'evidence_ref'
       ) THEN
        RETURN NULL;
    END IF;
    SELECT COALESCE(jsonb_agg(item->'evidence_ref' ORDER BY ordinal),'[]'::jsonb)
      INTO expected_refs
      FROM jsonb_array_elements(binding_json->'items') WITH ORDINALITY AS entry(item,ordinal)
     WHERE ordinal<=3;
    IF selected_refs IS DISTINCT FROM expected_refs THEN
        RETURN NULL;
    END IF;
    RETURN selected_refs;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.workspace_analysis_source_prefix_matches(
    target_run_id uuid,
    selected_refs jsonb,
    prefix_length integer
)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY INVOKER
SET search_path = pg_catalog, agent, workflow
AS $$
DECLARE
    canonical_refs jsonb;
    search_receipt_id uuid;
    search_receipt_hash text;
    search_binding_json jsonb;
    source_ordinal integer;
    expected_ref text;
    search_identity jsonb;
    match_count integer;
    succeeded_count integer;
BEGIN
    canonical_refs := agent.workspace_analysis_selected_refs(target_run_id);
    IF canonical_refs IS NULL
       OR canonical_refs IS DISTINCT FROM selected_refs
       OR prefix_length<0
       OR prefix_length>jsonb_array_length(canonical_refs) THEN
        RETURN false;
    END IF;
    SELECT receipt.id,receipt.output_hash,
           convert_from(receipt.server_binding_document,'UTF8')::jsonb
      INTO search_receipt_id,search_receipt_hash,search_binding_json
      FROM agent.workspace_analysis_operation AS operation
      JOIN workflow.tool_result_receipt AS receipt
        ON receipt.id=operation.result_id
       AND receipt.tool_call_id=operation.tool_call_id
     WHERE operation.analysis_run_id=target_run_id
       AND operation.operation_kind='KNOWLEDGE_SEARCH'
       AND operation.status='SUCCEEDED';
    IF NOT FOUND THEN
        RETURN false;
    END IF;

    SELECT count(*) INTO succeeded_count
      FROM agent.workspace_analysis_operation
     WHERE analysis_run_id=target_run_id
       AND operation_kind='SOURCE_READ'
       AND status='SUCCEEDED';
    IF succeeded_count<>prefix_length THEN
        RETURN false;
    END IF;

    FOR source_ordinal IN
        SELECT generate_series(1,prefix_length)
    LOOP
        expected_ref := canonical_refs->>(source_ordinal-1);
        search_identity := search_binding_json->'items'->(source_ordinal-1);
        SELECT count(*) INTO match_count
          FROM agent.workspace_analysis_operation AS operation
          JOIN workflow.tool_result_receipt AS receipt
            ON receipt.id=operation.result_id
           AND receipt.tool_call_id=operation.tool_call_id
         WHERE operation.analysis_run_id=target_run_id
           AND operation.operation_kind='SOURCE_READ'
           AND operation.ordinal=source_ordinal
           AND operation.status='SUCCEEDED'
           AND receipt.tool_name='ReadSource'
           AND receipt.tool_version=3
           AND convert_from(receipt.output_document,'UTF8')::jsonb->>'evidence_ref'=expected_ref
           AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'evidence_ref'=expected_ref
           AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'search_receipt_id'=search_receipt_id::text
           AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'search_receipt_hash'=search_receipt_hash
           AND (
                convert_from(receipt.server_binding_document,'UTF8')::jsonb
                - 'search_receipt_id' - 'search_receipt_hash'
           )=search_identity;
        IF expected_ref IS NULL OR search_identity IS NULL OR match_count<>1 THEN
            RETURN false;
        END IF;
    END LOOP;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.workspace_analysis_validation_all_valid(target_run_id uuid)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY INVOKER
SET search_path = pg_catalog, agent, workflow
AS $$
DECLARE
    candidate_row agent.workspace_analysis_candidate%ROWTYPE;
    validation_receipt workflow.tool_result_receipt%ROWTYPE;
    candidate_json jsonb;
    output_json jsonb;
    binding_json jsonb;
    search_binding_json jsonb;
    candidate_refs jsonb;
    selected_refs jsonb;
BEGIN
    SELECT * INTO candidate_row
      FROM agent.workspace_analysis_candidate
     WHERE analysis_run_id=target_run_id;
    SELECT receipt.* INTO validation_receipt
      FROM agent.workspace_analysis_operation AS operation
      JOIN workflow.tool_result_receipt AS receipt
        ON receipt.id=operation.result_id
       AND receipt.tool_call_id=operation.tool_call_id
     WHERE operation.analysis_run_id=target_run_id
       AND operation.operation_kind='CITATION_VALIDATION'
       AND operation.status='SUCCEEDED'
       AND receipt.tool_name='ValidateCitation'
       AND receipt.tool_version=3;
    SELECT convert_from(receipt.server_binding_document,'UTF8')::jsonb
      INTO search_binding_json
      FROM agent.workspace_analysis_operation AS operation
      JOIN workflow.tool_result_receipt AS receipt
        ON receipt.id=operation.result_id
       AND receipt.tool_call_id=operation.tool_call_id
     WHERE operation.analysis_run_id=target_run_id
       AND operation.operation_kind='KNOWLEDGE_SEARCH'
       AND operation.status='SUCCEEDED';
    IF candidate_row.id IS NULL OR validation_receipt.id IS NULL OR search_binding_json IS NULL
       OR NOT EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation
             WHERE id=candidate_row.synthesis_operation_id
               AND analysis_run_id=target_run_id
               AND operation_kind='ANSWER_SYNTHESIS'
               AND status='SUCCEEDED'
               AND result_id=candidate_row.id
               AND result_hash=candidate_row.document_hash
       ) THEN
        RETURN false;
    END IF;

    selected_refs := agent.workspace_analysis_selected_refs(target_run_id);
    IF selected_refs IS NULL
       OR NOT agent.workspace_analysis_source_prefix_matches(
            target_run_id,selected_refs,jsonb_array_length(selected_refs)
       ) THEN
        RETURN false;
    END IF;
    candidate_json := convert_from(candidate_row.document,'UTF8')::jsonb;
    output_json := convert_from(validation_receipt.output_document,'UTF8')::jsonb;
    binding_json := convert_from(validation_receipt.server_binding_document,'UTF8')::jsonb;
    candidate_refs := candidate_json#>'{payload,citation_refs}';
    IF jsonb_typeof(candidate_refs) IS DISTINCT FROM 'array'
       OR jsonb_typeof(output_json->'results') IS DISTINCT FROM 'array'
       OR jsonb_typeof(binding_json->'results') IS DISTINCT FROM 'array'
       OR binding_json->>'candidate_id' IS DISTINCT FROM candidate_row.id::text
       OR binding_json->>'candidate_hash' IS DISTINCT FROM candidate_row.document_hash THEN
        RETURN false;
    END IF;
    IF jsonb_array_length(candidate_refs)<1
       OR jsonb_array_length(candidate_refs)>jsonb_array_length(selected_refs)
       OR jsonb_array_length(output_json->'results')<>jsonb_array_length(candidate_refs)
       OR jsonb_array_length(binding_json->'results')<>jsonb_array_length(candidate_refs)
       OR candidate_refs IS DISTINCT FROM (
            SELECT COALESCE(jsonb_agg(item->'evidence_ref' ORDER BY ordinal),'[]'::jsonb)
              FROM jsonb_array_elements(output_json->'results') WITH ORDINALITY AS entry(item,ordinal)
       )
       OR candidate_refs IS DISTINCT FROM (
            SELECT COALESCE(jsonb_agg(item->'evidence_ref' ORDER BY ordinal),'[]'::jsonb)
              FROM jsonb_array_elements(binding_json->'results') WITH ORDINALITY AS entry(item,ordinal)
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(candidate_refs) AS candidate_ref(value)
             WHERE NOT (selected_refs @> jsonb_build_array(value))
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(output_json->'results') AS entry(item)
             WHERE item->'valid' IS DISTINCT FROM 'true'::jsonb
                OR item->>'reason_code' IS DISTINCT FROM 'OK'
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(binding_json->'results') AS validation_entry(item)
             WHERE NOT EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(search_binding_json->'items') AS search_entry(item)
                 WHERE search_entry.item=validation_entry.item
             )
       ) THEN
        RETURN false;
    END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.is_workspace_analysis_operation_next(target_operation_id uuid)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY INVOKER
SET search_path = pg_catalog, agent, workflow
AS $$
DECLARE
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    plan_json jsonb;
    selected_refs jsonb;
BEGIN
    SELECT * INTO operation_row
      FROM agent.workspace_analysis_operation
     WHERE id=target_operation_id
       AND status='PENDING'
       AND first_node_attempt_id IS NULL
       AND latest_node_attempt_id IS NULL;
    IF operation_row.id IS NULL
       OR EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation AS terminal
             WHERE terminal.analysis_run_id=operation_row.analysis_run_id
               AND terminal.id<>operation_row.id
               AND terminal.status IN ('FAILED','UNKNOWN')
       ) THEN
        RETURN false;
    END IF;

    CASE operation_row.operation_kind
        WHEN 'GIT_STATUS' THEN
            RETURN NOT EXISTS (
                SELECT 1 FROM agent.workspace_analysis_operation AS predecessor
                 WHERE predecessor.analysis_run_id=operation_row.analysis_run_id
                   AND predecessor.id<>operation_row.id
                   AND predecessor.status<>'PENDING'
            );
        WHEN 'RETRIEVAL_PLAN' THEN
            RETURN EXISTS (
                SELECT 1 FROM agent.workspace_analysis_operation
                 WHERE analysis_run_id=operation_row.analysis_run_id
                   AND operation_kind='GIT_STATUS'
                   AND status='SUCCEEDED'
            );
        WHEN 'KNOWLEDGE_SEARCH' THEN
            SELECT convert_from(result.document,'UTF8')::jsonb INTO plan_json
              FROM agent.workspace_analysis_operation AS plan
              JOIN agent.workspace_analysis_model_result AS result
                ON result.id=plan.result_id
               AND result.operation_id=plan.id
             WHERE plan.analysis_run_id=operation_row.analysis_run_id
               AND plan.operation_kind='RETRIEVAL_PLAN'
               AND plan.status='SUCCEEDED';
            RETURN plan_json#>'{payload,requires_clarification}' IS NOT DISTINCT FROM 'false'::jsonb;
        WHEN 'SOURCE_READ' THEN
            selected_refs := agent.workspace_analysis_selected_refs(operation_row.analysis_run_id);
            RETURN selected_refs IS NOT NULL
               AND operation_row.ordinal<=jsonb_array_length(selected_refs)
               AND agent.workspace_analysis_source_prefix_matches(
                    operation_row.analysis_run_id,selected_refs,operation_row.ordinal-1
               );
        WHEN 'ANSWER_SYNTHESIS' THEN
            selected_refs := agent.workspace_analysis_selected_refs(operation_row.analysis_run_id);
            RETURN selected_refs IS NOT NULL
               AND agent.workspace_analysis_source_prefix_matches(
                    operation_row.analysis_run_id,selected_refs,jsonb_array_length(selected_refs)
               );
        WHEN 'CITATION_VALIDATION' THEN
            RETURN EXISTS (
                SELECT 1
                  FROM agent.workspace_analysis_operation AS synthesis
                  JOIN agent.workspace_analysis_candidate AS candidate
                    ON candidate.id=synthesis.result_id
                   AND candidate.synthesis_operation_id=synthesis.id
                   AND candidate.analysis_run_id=synthesis.analysis_run_id
                 WHERE synthesis.analysis_run_id=operation_row.analysis_run_id
                   AND synthesis.operation_kind='ANSWER_SYNTHESIS'
                   AND synthesis.status='SUCCEEDED'
                   AND synthesis.result_hash=candidate.document_hash
            );
        WHEN 'FAITHFULNESS_REVIEW' THEN
            RETURN agent.workspace_analysis_validation_all_valid(operation_row.analysis_run_id);
        ELSE
            RETURN false;
    END CASE;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_publication_proof_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_row agent.workspace_analysis_run%ROWTYPE;
    candidate_row agent.workspace_analysis_candidate%ROWTYPE;
    git_receipt_row workflow.tool_result_receipt%ROWTYPE;
    search_receipt_row workflow.tool_result_receipt%ROWTYPE;
    validation_receipt_row workflow.tool_result_receipt%ROWTYPE;
    review_result_row agent.workspace_analysis_model_result%ROWTYPE;
    candidate_json jsonb;
    git_json jsonb;
    search_output_json jsonb;
    search_binding_json jsonb;
    validation_output_json jsonb;
    validation_binding_json jsonb;
    review_json jsonb;
    published_json jsonb;
    candidate_refs jsonb;
    selected_refs jsonb;
    expected_citations jsonb;
    expected_proposal jsonb;
    expected_git jsonb;
    expected_budget jsonb;
    evidence_ref_value text;
    search_identity jsonb;
    source_match_count integer;
    source_operation_count integer;
    workflow_status_value text;
    workflow_cancel_requested_at timestamptz;
    node_status_value text;
    node_key_value text;
    node_attempt_no integer;
    node_lease_owner text;
    node_lease_until timestamptz;
    attempt_status_value text;
    attempt_no integer;
    attempt_lease_owner text;
    attempt_lease_until timestamptz;
BEGIN
    SELECT status,cancel_requested_at
      INTO workflow_status_value,workflow_cancel_requested_at
      FROM workflow.run
     WHERE id=NEW.workflow_run_id
       AND workspace_id=NEW.workspace_id
     FOR UPDATE;
    SELECT status,node_key,attempt,lease_owner,lease_until
      INTO node_status_value,node_key_value,node_attempt_no,node_lease_owner,node_lease_until
      FROM workflow.node_run
     WHERE id=NEW.finalization_node_run_id
       AND run_id=NEW.workflow_run_id
     FOR UPDATE;
    SELECT attempt.status,attempt.attempt_no,attempt.lease_owner,attempt.lease_until
      INTO attempt_status_value,attempt_no,attempt_lease_owner,attempt_lease_until
      FROM workflow.node_attempt AS attempt
     WHERE attempt.id=NEW.finalization_node_attempt_id
       AND attempt.node_run_id=NEW.finalization_node_run_id
     FOR UPDATE;
    SELECT * INTO run_row
      FROM agent.workspace_analysis_run
     WHERE id=NEW.analysis_run_id
       AND workspace_id=NEW.workspace_id
       AND answer_id=NEW.answer_id
     FOR UPDATE;
    SELECT * INTO candidate_row
      FROM agent.workspace_analysis_candidate
     WHERE id=NEW.candidate_id
       AND analysis_run_id=NEW.analysis_run_id
     FOR SHARE;
    SELECT * INTO git_receipt_row
      FROM workflow.tool_result_receipt
     WHERE id=NEW.git_receipt_id
       AND workspace_id=NEW.workspace_id
     FOR SHARE;
    SELECT receipt.* INTO search_receipt_row
      FROM agent.workspace_analysis_operation AS operation
      JOIN workflow.tool_result_receipt AS receipt
        ON receipt.id=operation.result_id
       AND receipt.tool_call_id=operation.tool_call_id
     WHERE operation.analysis_run_id=NEW.analysis_run_id
       AND operation.operation_kind='KNOWLEDGE_SEARCH'
       AND operation.status='SUCCEEDED'
     FOR SHARE OF operation,receipt;
    SELECT * INTO validation_receipt_row
      FROM workflow.tool_result_receipt
     WHERE id=NEW.validation_receipt_id
       AND workspace_id=NEW.workspace_id
     FOR SHARE;
    SELECT * INTO review_result_row
      FROM agent.workspace_analysis_model_result
     WHERE id=NEW.review_model_result_id
       AND analysis_run_id=NEW.analysis_run_id
     FOR SHARE;

    IF run_row.id IS NULL
       OR run_row.status<>'running'
       OR run_row.workflow_run_id<>NEW.workflow_run_id
       OR workflow_status_value<>'running'
       OR workflow_cancel_requested_at IS NOT NULL
       OR node_status_value<>'running'
       OR node_key_value<>'review_publish'
       OR node_attempt_no IS DISTINCT FROM attempt_no
       OR node_lease_owner IS NULL
       OR node_lease_owner IS DISTINCT FROM btrim(node_lease_owner)
       OR node_lease_owner=''
       OR attempt_lease_owner IS DISTINCT FROM node_lease_owner
       OR node_lease_until IS NULL
       OR node_lease_until IS DISTINCT FROM attempt_lease_until
       OR attempt_status_value<>'running'
       OR attempt_lease_until IS NULL
       OR attempt_lease_until<=clock_timestamp()
       OR NEW.created_at>clock_timestamp()
       OR run_row.deadline_at<NEW.created_at
       OR run_row.deadline_at<=clock_timestamp()
       OR candidate_row.id IS NULL
       OR candidate_row.answer_id<>NEW.answer_id
       OR candidate_row.document_hash<>NEW.candidate_hash
       OR git_receipt_row.id IS NULL
       OR git_receipt_row.output_hash<>NEW.git_receipt_hash
       OR git_receipt_row.tool_name<>'ReadGitStatus'
       OR git_receipt_row.tool_version<>2
       OR search_receipt_row.id IS NULL
       OR search_receipt_row.tool_name<>'SearchKnowledge'
       OR search_receipt_row.tool_version<>2
       OR validation_receipt_row.id IS NULL
       OR validation_receipt_row.output_hash<>NEW.validation_receipt_hash
       OR validation_receipt_row.tool_name<>'ValidateCitation'
       OR validation_receipt_row.tool_version<>3
       OR review_result_row.id IS NULL
       OR review_result_row.operation_kind<>'FAITHFULNESS_REVIEW'
       OR review_result_row.document_hash<>NEW.review_document_hash
       OR review_result_row.subject_candidate_id<>NEW.candidate_id
       OR review_result_row.subject_candidate_hash<>NEW.candidate_hash
       OR NEW.created_at<candidate_row.created_at
       OR NEW.created_at<git_receipt_row.created_at
       OR NEW.created_at<search_receipt_row.created_at
       OR NEW.created_at<validation_receipt_row.created_at
       OR NEW.created_at<review_result_row.created_at
       OR NOT EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation
             WHERE analysis_run_id=NEW.analysis_run_id
               AND operation_kind='ANSWER_SYNTHESIS'
               AND status='SUCCEEDED'
               AND result_id=NEW.candidate_id
               AND result_hash=NEW.candidate_hash
       )
       OR NOT EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation
             WHERE analysis_run_id=NEW.analysis_run_id
               AND operation_kind='GIT_STATUS'
               AND status='SUCCEEDED'
               AND result_id=NEW.git_receipt_id
               AND result_hash=NEW.git_receipt_hash
       )
       OR NOT EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation
             WHERE analysis_run_id=NEW.analysis_run_id
               AND operation_kind='CITATION_VALIDATION'
               AND status='SUCCEEDED'
               AND result_id=NEW.validation_receipt_id
               AND result_hash=NEW.validation_receipt_hash
       )
       OR NOT EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation
             WHERE id=review_result_row.operation_id
               AND analysis_run_id=NEW.analysis_run_id
               AND operation_kind='FAITHFULNESS_REVIEW'
               AND status='SUCCEEDED'
               AND node_run_id=NEW.finalization_node_run_id
               AND result_id=NEW.review_model_result_id
               AND result_hash=NEW.review_document_hash
       ) THEN
        RAISE EXCEPTION 'workspace analysis publication authority binding is invalid' USING ERRCODE='55000';
    END IF;

    candidate_json := convert_from(candidate_row.document,'UTF8')::jsonb;
    git_json := convert_from(git_receipt_row.output_document,'UTF8')::jsonb;
    search_output_json := convert_from(search_receipt_row.output_document,'UTF8')::jsonb;
    search_binding_json := convert_from(search_receipt_row.server_binding_document,'UTF8')::jsonb;
    validation_output_json := convert_from(validation_receipt_row.output_document,'UTF8')::jsonb;
    validation_binding_json := convert_from(validation_receipt_row.server_binding_document,'UTF8')::jsonb;
    review_json := convert_from(review_result_row.document,'UTF8')::jsonb;
    published_json := convert_from(NEW.published_document,'UTF8')::jsonb;
    candidate_refs := candidate_json#>'{payload,citation_refs}';
    selected_refs := search_binding_json->'selected_refs';

    IF jsonb_typeof(candidate_refs) IS DISTINCT FROM 'array'
       OR jsonb_typeof(search_output_json->'items') IS DISTINCT FROM 'array'
       OR jsonb_typeof(search_binding_json->'items') IS DISTINCT FROM 'array'
       OR jsonb_typeof(selected_refs) IS DISTINCT FROM 'array'
       OR jsonb_array_length(selected_refs) NOT BETWEEN 1 AND 3
       OR jsonb_array_length(selected_refs)<>LEAST(3,jsonb_array_length(search_binding_json->'items'))
       OR jsonb_array_length(search_output_json->'items')<>jsonb_array_length(search_binding_json->'items')
       OR selected_refs IS DISTINCT FROM (
            SELECT COALESCE(jsonb_agg(item->'evidence_ref' ORDER BY ordinal),'[]'::jsonb)
              FROM jsonb_array_elements(search_binding_json->'items') WITH ORDINALITY AS entry(item,ordinal)
             WHERE ordinal<=3
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(candidate_refs) AS candidate_ref(value)
             WHERE NOT (selected_refs @> jsonb_build_array(value))
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(search_output_json->'items') WITH ORDINALITY AS output_entry(item,ordinal)
              JOIN jsonb_array_elements(search_binding_json->'items') WITH ORDINALITY AS binding_entry(item,ordinal)
                USING (ordinal)
             WHERE output_entry.item->>'evidence_ref' IS DISTINCT FROM binding_entry.item->>'evidence_ref'
       ) THEN
        RAISE EXCEPTION 'workspace analysis search evidence binding is invalid' USING ERRCODE='55000';
    END IF;

    SELECT count(*) INTO source_operation_count
      FROM agent.workspace_analysis_operation
     WHERE analysis_run_id=NEW.analysis_run_id
       AND operation_kind='SOURCE_READ'
       AND status='SUCCEEDED';
    IF source_operation_count<>jsonb_array_length(selected_refs)
       OR run_row.settled_source_reads<>source_operation_count THEN
        RAISE EXCEPTION 'workspace analysis source read count is invalid' USING ERRCODE='55000';
    END IF;

    FOR evidence_ref_value IN
        SELECT value#>>'{}' FROM jsonb_array_elements(selected_refs) AS selected(value)
    LOOP
        SELECT item INTO search_identity
          FROM jsonb_array_elements(search_binding_json->'items') AS entry(item)
         WHERE item->>'evidence_ref'=evidence_ref_value;
        SELECT count(*) INTO source_match_count
          FROM agent.workspace_analysis_operation AS operation
          JOIN workflow.tool_result_receipt AS receipt
            ON receipt.id=operation.result_id
           AND receipt.tool_call_id=operation.tool_call_id
         WHERE operation.analysis_run_id=NEW.analysis_run_id
           AND operation.operation_kind='SOURCE_READ'
           AND operation.status='SUCCEEDED'
           AND receipt.tool_name='ReadSource'
           AND receipt.tool_version=3
           AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'evidence_ref'=evidence_ref_value
           AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'search_receipt_id'=search_receipt_row.id::text
           AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'search_receipt_hash'=search_receipt_row.output_hash
           AND (
                convert_from(receipt.server_binding_document,'UTF8')::jsonb
                - 'search_receipt_id' - 'search_receipt_hash'
           )=search_identity;
        IF search_identity IS NULL OR source_match_count<>1 THEN
            RAISE EXCEPTION 'workspace analysis source evidence binding is invalid' USING ERRCODE='55000';
        END IF;
    END LOOP;

    IF jsonb_typeof(validation_output_json->'results') IS DISTINCT FROM 'array'
       OR jsonb_typeof(validation_binding_json->'results') IS DISTINCT FROM 'array'
       OR validation_binding_json->>'candidate_id' IS DISTINCT FROM NEW.candidate_id::text
       OR validation_binding_json->>'candidate_hash' IS DISTINCT FROM NEW.candidate_hash
       OR jsonb_array_length(validation_output_json->'results')<>jsonb_array_length(candidate_refs)
       OR jsonb_array_length(validation_binding_json->'results')<>jsonb_array_length(candidate_refs)
       OR candidate_refs IS DISTINCT FROM (
            SELECT jsonb_agg(item->'evidence_ref' ORDER BY ordinal)
              FROM jsonb_array_elements(validation_output_json->'results') WITH ORDINALITY AS entry(item,ordinal)
       )
       OR candidate_refs IS DISTINCT FROM (
            SELECT jsonb_agg(item->'evidence_ref' ORDER BY ordinal)
              FROM jsonb_array_elements(validation_binding_json->'results') WITH ORDINALITY AS entry(item,ordinal)
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(validation_output_json->'results') AS entry(item)
             WHERE item->'valid' IS DISTINCT FROM 'true'::jsonb
                OR item->>'reason_code' IS DISTINCT FROM 'OK'
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(validation_binding_json->'results') AS validation_entry(item)
             WHERE NOT EXISTS (
                SELECT 1 FROM jsonb_array_elements(search_binding_json->'items') AS search_entry(item)
                 WHERE search_entry.item->>'evidence_ref'=validation_entry.item->>'evidence_ref'
                   AND search_entry.item=validation_entry.item
             )
       )
       OR review_json#>'{payload,passed}' IS DISTINCT FROM 'true'::jsonb THEN
        RAISE EXCEPTION 'workspace analysis validation or review proof is invalid' USING ERRCODE='55000';
    END IF;

    SELECT jsonb_agg(jsonb_build_object(
               'id',item->>'citation_id',
               'workspace_id',NEW.workspace_id::text,
               'index_version_id',item->>'index_version_id',
               'chunk_id',item->>'chunk_id',
               'source_version_id',item->>'source_version_id',
               'source_span_id',item->>'source_span_id'
           ) ORDER BY ordinal)
      INTO expected_citations
      FROM jsonb_array_elements(validation_binding_json->'results') WITH ORDINALITY AS entry(item,ordinal);

    IF jsonb_typeof(candidate_json#>'{payload,proposal_suggestion}')='null' THEN
        expected_proposal := 'null'::jsonb;
    ELSE
        SELECT jsonb_build_object(
            'summary',candidate_json#>>'{payload,proposal_suggestion,summary}',
            'citation_ids',jsonb_agg(binding_item->'citation_id' ORDER BY proposal_entry.ordinal),
            'href','/proposals'
        ) INTO expected_proposal
          FROM jsonb_array_elements(candidate_json#>'{payload,proposal_suggestion,citation_refs}')
               WITH ORDINALITY AS proposal_entry(reference,ordinal)
          JOIN jsonb_array_elements(validation_binding_json->'results') AS validation_entry(binding_item)
            ON binding_item->>'evidence_ref'=proposal_entry.reference#>>'{}';
    END IF;

    expected_git := jsonb_build_object(
        'branch',git_json->>'branch','head',git_json->>'head','clean',git_json->'clean',
        'staged_count',git_json->'staged_count','unstaged_count',git_json->'unstaged_count',
        'untracked_count',git_json->'untracked_count','conflict_count',git_json->'conflict_count'
    );
    expected_budget := jsonb_build_object(
        'model_calls',run_row.settled_model_calls,'tool_calls',run_row.settled_tool_calls,
        'input_tokens',run_row.settled_input_tokens,'output_tokens',run_row.settled_output_tokens,
        'estimated_cost_microunits',run_row.settled_cost_microunits
    );

    IF jsonb_typeof(published_json) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_json))<>5
       OR published_json->>'result_type' IS DISTINCT FROM 'workspace_analysis'
       OR published_json->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_answer'
       OR published_json->>'schema_version' IS DISTINCT FROM 'v1'
       OR published_json->>'model_run_ref' IS DISTINCT FROM candidate_row.synthesis_model_run_id::text
       OR jsonb_typeof(published_json->'payload') IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_json->'payload'))<>6
       OR published_json#>>'{payload,answer_markdown}' IS DISTINCT FROM candidate_json#>>'{payload,answer_markdown}'
       OR published_json#>'{payload,citations}' IS DISTINCT FROM expected_citations
       OR published_json#>'{payload,git_status}' IS DISTINCT FROM expected_git
       OR published_json#>'{payload,budget}' IS DISTINCT FROM expected_budget
       OR published_json#>'{payload,proposal_suggestion}' IS DISTINCT FROM expected_proposal
       OR published_json#>>'{payload,termination_reason}' IS DISTINCT FROM 'COMPLETED' THEN
        RAISE EXCEPTION 'workspace analysis published document is not the authoritative projection' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_termination_proof_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_row agent.workspace_analysis_run%ROWTYPE;
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    reservation_row agent.workspace_analysis_budget_reservation%ROWTYPE;
    model_call_row agent.model_call%ROWTYPE;
    model_run_row agent.model_run%ROWTYPE;
    tool_call_row workflow.tool_call%ROWTYPE;
    tool_receipt_row workflow.tool_result_receipt%ROWTYPE;
    receipt_failure_row workflow.tool_result_receipt_failure%ROWTYPE;
    model_result_row agent.workspace_analysis_model_result%ROWTYPE;
    candidate_row agent.workspace_analysis_candidate%ROWTYPE;
    search_receipt_row workflow.tool_result_receipt%ROWTYPE;
    artifact_json jsonb;
    candidate_json jsonb;
    search_output_json jsonb;
    search_binding_json jsonb;
    validation_binding_json jsonb;
    candidate_refs jsonb;
    selected_refs jsonb;
    evidence_ref_value text;
    search_identity jsonb;
    source_match_count integer;
    source_operation_count integer;
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
    expected_model_calls integer;
    expected_tool_calls integer;
    expected_source_reads integer;
    expected_input_tokens bigint;
    expected_output_tokens bigint;
    call_timeout_ms bigint;
    published_json jsonb;
    published_payload jsonb;
    published_summary text;
BEGIN
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

    IF run_row.id IS NULL
       OR run_row.status<>'running'
       OR workflow_status_value<>'running'
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
       OR attempt_lease_until<=clock_timestamp()
       OR NEW.checked_at>clock_timestamp()
       OR NEW.created_at>clock_timestamp()
       OR (NEW.reason<>'WORKSPACE_ANALYSIS_CANCELLED' AND workflow_cancel_requested_at IS NOT NULL)
       OR (NEW.reason='WORKSPACE_ANALYSIS_CANCELLED' AND (
            workflow_cancel_requested_at IS NULL OR workflow_cancel_requested_at>NEW.checked_at
       ))
       OR (NEW.reason NOT IN ('WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED','WORKSPACE_ANALYSIS_CANCELLED')
           AND NEW.checked_at>run_row.deadline_at) THEN
        RAISE EXCEPTION 'workspace analysis termination fence is invalid' USING ERRCODE='55000';
    END IF;
    IF (NEW.reason='WORKSPACE_ANALYSIS_CANCELLED')<>(NEW.operation_id IS NULL)
       OR (NEW.reason<>'WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED' AND (
            NEW.requested_model_calls IS NOT NULL OR NEW.requested_tool_calls IS NOT NULL
            OR NEW.requested_source_reads IS NOT NULL OR NEW.requested_input_tokens IS NOT NULL
            OR NEW.requested_output_tokens IS NOT NULL OR NEW.requested_cost_microunits IS NOT NULL
       ))
       OR (NEW.reason<>'WORKSPACE_ANALYSIS_RECEIPT_INVALID' AND (
            NEW.expected_hash IS NOT NULL OR NEW.actual_hash IS NOT NULL
       )) THEN
        RAISE EXCEPTION 'workspace analysis termination proof shape is invalid' USING ERRCODE='55000';
    END IF;

    IF NEW.operation_id IS NOT NULL THEN
        SELECT * INTO operation_row
         FROM agent.workspace_analysis_operation
         WHERE id=NEW.operation_id
           AND analysis_run_id=NEW.analysis_run_id
           AND workspace_id=NEW.workspace_id
           AND workflow_run_id=NEW.workflow_run_id
           AND (
                (
                    NEW.reason='WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT'
                    AND terminal_node_key_value='read_evidence'
                    AND node_key='retrieve_evidence'
                    AND operation_kind='KNOWLEDGE_SEARCH'
                    AND ordinal=1
                )
                OR (
                    NEW.reason='WORKSPACE_ANALYSIS_CITATION_INVALID'
                    AND terminal_node_key_value='review_publish'
                    AND node_key='validate_citations'
                    AND operation_kind='CITATION_VALIDATION'
                    AND ordinal=1
                )
                OR (
                    NEW.reason NOT IN (
                        'WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT',
                        'WORKSPACE_ANALYSIS_CITATION_INVALID'
                    )
                    AND node_run_id=NEW.terminal_node_run_id
                )
           )
           AND (
                (
                    NEW.reason IN ('WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED','WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED')
                    AND status='PENDING'
                    AND first_node_attempt_id IS NULL
                    AND latest_node_attempt_id IS NULL
                )
                OR (
                    NEW.reason='WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED'
                    AND status='SUCCEEDED'
                    AND completed_at IS NOT NULL
                )
                OR (
                    NEW.reason NOT IN ('WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED','WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED')
                    AND status IN ('SUCCEEDED','FAILED','UNKNOWN')
                )
           )
         FOR SHARE;
        SELECT * INTO reservation_row
          FROM agent.workspace_analysis_budget_reservation
         WHERE operation_id=NEW.operation_id
         FOR SHARE;
        IF operation_row.id IS NULL THEN
            RAISE EXCEPTION 'workspace analysis termination operation binding is invalid' USING ERRCODE='55000';
        END IF;
        IF NEW.reason IN ('WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED','WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED')
           AND operation_row.status='PENDING'
           AND NOT agent.is_workspace_analysis_operation_next(NEW.operation_id) THEN
            RAISE EXCEPTION 'workspace analysis termination proof does not target the next v1 operation' USING ERRCODE='55000';
        END IF;
        IF (
                operation_row.status='PENDING'
                AND NEW.checked_at<operation_row.created_at
           ) OR (
                operation_row.status<>'PENDING'
                AND (
                    operation_row.completed_at IS NULL
                    OR NEW.checked_at<operation_row.completed_at
                )
           ) THEN
            RAISE EXCEPTION 'workspace analysis termination operation chronology is invalid' USING ERRCODE='55000';
        END IF;
        IF operation_row.call_kind='MODEL' THEN
            SELECT * INTO model_call_row
              FROM agent.model_call
             WHERE id=operation_row.model_call_id
             FOR SHARE;
            SELECT * INTO model_run_row
              FROM agent.model_run
             WHERE id=model_call_row.model_run_id
             FOR SHARE;
        ELSE
            SELECT * INTO tool_call_row
              FROM workflow.tool_call
             WHERE id=operation_row.tool_call_id
             FOR SHARE;
        END IF;
    END IF;

    IF NEW.artifact_kind='TOOL_RECEIPT' THEN
        SELECT * INTO tool_receipt_row
          FROM workflow.tool_result_receipt
         WHERE id=NEW.artifact_id
           AND workspace_id=NEW.workspace_id
         FOR SHARE;
        IF tool_receipt_row.id IS NULL
           OR tool_receipt_row.output_hash<>NEW.artifact_hash
           OR operation_row.result_id<>NEW.artifact_id
           OR operation_row.result_hash<>NEW.artifact_hash
           OR NEW.checked_at<tool_receipt_row.created_at THEN
            RAISE EXCEPTION 'workspace analysis termination receipt binding is invalid' USING ERRCODE='55000';
        END IF;
        artifact_json := convert_from(tool_receipt_row.output_document,'UTF8')::jsonb;
    ELSIF NEW.artifact_kind='MODEL_RESULT' THEN
        SELECT * INTO model_result_row
          FROM agent.workspace_analysis_model_result
         WHERE id=NEW.artifact_id
           AND analysis_run_id=NEW.analysis_run_id
         FOR SHARE;
        IF model_result_row.id IS NULL
           OR model_result_row.document_hash<>NEW.artifact_hash
           OR operation_row.result_id<>NEW.artifact_id
           OR operation_row.result_hash<>NEW.artifact_hash
           OR NEW.checked_at<model_result_row.created_at THEN
            RAISE EXCEPTION 'workspace analysis termination model result binding is invalid' USING ERRCODE='55000';
        END IF;
        artifact_json := convert_from(model_result_row.document,'UTF8')::jsonb;
    END IF;

    IF NEW.reason='WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT' THEN
        IF operation_row.node_key<>'retrieve_evidence'
           OR operation_row.operation_kind<>'KNOWLEDGE_SEARCH'
           OR operation_row.ordinal<>1
           OR operation_row.call_kind<>'TOOL'
           OR operation_row.status<>'SUCCEEDED'
           OR operation_row.result_kind<>'TOOL_RESULT_RECEIPT'
           OR NEW.artifact_kind<>'TOOL_RECEIPT'
           OR tool_receipt_row.tool_name<>'SearchKnowledge'
           OR tool_receipt_row.tool_version<>2
           OR jsonb_typeof(artifact_json->'items') IS DISTINCT FROM 'array'
           OR jsonb_array_length(artifact_json->'items')<>0 THEN
            RAISE EXCEPTION 'workspace analysis evidence insufficiency proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_CITATION_INVALID' THEN
        IF operation_row.node_key<>'validate_citations'
           OR operation_row.operation_kind<>'CITATION_VALIDATION'
           OR operation_row.ordinal<>1
           OR operation_row.call_kind<>'TOOL'
           OR operation_row.status<>'SUCCEEDED'
           OR operation_row.result_kind<>'TOOL_RESULT_RECEIPT'
           OR NEW.artifact_kind<>'TOOL_RECEIPT'
           OR tool_receipt_row.tool_name<>'ValidateCitation'
           OR tool_receipt_row.tool_version<>3 THEN
            RAISE EXCEPTION 'workspace analysis citation rejection proof is invalid' USING ERRCODE='55000';
        END IF;
        SELECT * INTO candidate_row
          FROM agent.workspace_analysis_candidate
         WHERE analysis_run_id=NEW.analysis_run_id
           AND answer_id=NEW.answer_id
         FOR SHARE;
        SELECT receipt.* INTO search_receipt_row
          FROM agent.workspace_analysis_operation AS operation
          JOIN workflow.tool_result_receipt AS receipt
            ON receipt.id=operation.result_id
           AND receipt.tool_call_id=operation.tool_call_id
         WHERE operation.analysis_run_id=NEW.analysis_run_id
           AND operation.operation_kind='KNOWLEDGE_SEARCH'
           AND operation.status='SUCCEEDED'
         FOR SHARE OF operation,receipt;
        IF candidate_row.id IS NULL
           OR search_receipt_row.id IS NULL
           OR search_receipt_row.tool_name<>'SearchKnowledge'
           OR search_receipt_row.tool_version<>2 THEN
            RAISE EXCEPTION 'workspace analysis citation rejection authority is invalid' USING ERRCODE='55000';
        END IF;

        candidate_json := convert_from(candidate_row.document,'UTF8')::jsonb;
        search_output_json := convert_from(search_receipt_row.output_document,'UTF8')::jsonb;
        search_binding_json := convert_from(search_receipt_row.server_binding_document,'UTF8')::jsonb;
        validation_binding_json := convert_from(tool_receipt_row.server_binding_document,'UTF8')::jsonb;
        candidate_refs := candidate_json#>'{payload,citation_refs}';
        selected_refs := search_binding_json->'selected_refs';
        IF jsonb_typeof(candidate_refs) IS DISTINCT FROM 'array'
           OR jsonb_typeof(search_output_json->'items') IS DISTINCT FROM 'array'
           OR jsonb_typeof(search_binding_json->'items') IS DISTINCT FROM 'array'
           OR jsonb_typeof(selected_refs) IS DISTINCT FROM 'array'
           OR jsonb_array_length(selected_refs) NOT BETWEEN 1 AND 3
           OR jsonb_array_length(selected_refs)<>LEAST(3,jsonb_array_length(search_binding_json->'items'))
           OR jsonb_array_length(search_output_json->'items')<>jsonb_array_length(search_binding_json->'items')
           OR selected_refs IS DISTINCT FROM (
                SELECT COALESCE(jsonb_agg(item->'evidence_ref' ORDER BY ordinal),'[]'::jsonb)
                  FROM jsonb_array_elements(search_binding_json->'items') WITH ORDINALITY AS entry(item,ordinal)
                 WHERE ordinal<=3
           )
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(candidate_refs) AS candidate_ref(value)
                 WHERE NOT (selected_refs @> jsonb_build_array(value))
           )
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(search_output_json->'items') WITH ORDINALITY AS output_entry(item,ordinal)
                  JOIN jsonb_array_elements(search_binding_json->'items') WITH ORDINALITY AS binding_entry(item,ordinal)
                    USING (ordinal)
                 WHERE output_entry.item->>'evidence_ref' IS DISTINCT FROM binding_entry.item->>'evidence_ref'
           ) THEN
            RAISE EXCEPTION 'workspace analysis citation rejection search binding is invalid' USING ERRCODE='55000';
        END IF;

        SELECT count(*) INTO source_operation_count
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=NEW.analysis_run_id
           AND operation_kind='SOURCE_READ'
           AND status='SUCCEEDED';
        IF source_operation_count<>jsonb_array_length(selected_refs)
           OR run_row.settled_source_reads<>source_operation_count THEN
            RAISE EXCEPTION 'workspace analysis citation rejection source count is invalid' USING ERRCODE='55000';
        END IF;
        FOR evidence_ref_value IN
            SELECT value#>>'{}' FROM jsonb_array_elements(selected_refs) AS selected(value)
        LOOP
            SELECT item INTO search_identity
              FROM jsonb_array_elements(search_binding_json->'items') AS entry(item)
             WHERE item->>'evidence_ref'=evidence_ref_value;
            SELECT count(*) INTO source_match_count
              FROM agent.workspace_analysis_operation AS operation
              JOIN workflow.tool_result_receipt AS receipt
                ON receipt.id=operation.result_id
               AND receipt.tool_call_id=operation.tool_call_id
             WHERE operation.analysis_run_id=NEW.analysis_run_id
               AND operation.operation_kind='SOURCE_READ'
               AND operation.status='SUCCEEDED'
               AND receipt.tool_name='ReadSource'
               AND receipt.tool_version=3
               AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'evidence_ref'=evidence_ref_value
               AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'search_receipt_id'=search_receipt_row.id::text
               AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'search_receipt_hash'=search_receipt_row.output_hash
               AND (
                    convert_from(receipt.server_binding_document,'UTF8')::jsonb
                    - 'search_receipt_id' - 'search_receipt_hash'
               )=search_identity;
            IF search_identity IS NULL OR source_match_count<>1 THEN
                RAISE EXCEPTION 'workspace analysis citation rejection source binding is invalid' USING ERRCODE='55000';
            END IF;
        END LOOP;

        IF jsonb_typeof(artifact_json->'results') IS DISTINCT FROM 'array'
           OR jsonb_typeof(validation_binding_json->'results') IS DISTINCT FROM 'array'
           OR validation_binding_json->>'candidate_id' IS DISTINCT FROM candidate_row.id::text
           OR validation_binding_json->>'candidate_hash' IS DISTINCT FROM candidate_row.document_hash
           OR jsonb_array_length(artifact_json->'results')<>jsonb_array_length(candidate_refs)
           OR jsonb_array_length(validation_binding_json->'results')<>jsonb_array_length(candidate_refs)
           OR candidate_refs IS DISTINCT FROM (
                SELECT jsonb_agg(item->'evidence_ref' ORDER BY ordinal)
                  FROM jsonb_array_elements(artifact_json->'results') WITH ORDINALITY AS entry(item,ordinal)
           )
           OR candidate_refs IS DISTINCT FROM (
                SELECT jsonb_agg(item->'evidence_ref' ORDER BY ordinal)
                  FROM jsonb_array_elements(validation_binding_json->'results') WITH ORDINALITY AS entry(item,ordinal)
           )
           OR NOT EXISTS (
                SELECT 1 FROM jsonb_array_elements(artifact_json->'results') AS entry(item)
                 WHERE item->'valid' IS NOT DISTINCT FROM 'false'::jsonb
           )
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(validation_binding_json->'results') AS validation_entry(item)
                 WHERE NOT EXISTS (
                    SELECT 1 FROM jsonb_array_elements(search_binding_json->'items') AS search_entry(item)
                     WHERE search_entry.item->>'evidence_ref'=validation_entry.item->>'evidence_ref'
                       AND search_entry.item=validation_entry.item
                 )
           ) THEN
            RAISE EXCEPTION 'workspace analysis citation rejection binding is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED' THEN
        IF operation_row.operation_kind<>'FAITHFULNESS_REVIEW'
           OR operation_row.status<>'SUCCEEDED'
           OR NEW.artifact_kind<>'MODEL_RESULT'
           OR model_result_row.operation_kind<>'FAITHFULNESS_REVIEW'
           OR artifact_json#>'{payload,passed}' IS DISTINCT FROM 'false'::jsonb THEN
            RAISE EXCEPTION 'workspace analysis faithfulness rejection proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_MODEL_REFUSED' THEN
        IF operation_row.call_kind<>'MODEL'
           OR operation_row.status<>'FAILED'
           OR operation_row.error_code<>'WORKSPACE_ANALYSIS_MODEL_REFUSED'
           OR model_call_row.status<>'SUCCEEDED'
           OR model_run_row.status<>'REFUSED'
           OR model_run_row.final_result_type<>'refusal'
           OR NEW.artifact_kind IS NOT NULL THEN
            RAISE EXCEPTION 'workspace analysis model refusal termination proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED' THEN
        IF operation_row.operation_kind<>'RETRIEVAL_PLAN'
           OR operation_row.status<>'SUCCEEDED'
           OR NEW.artifact_kind<>'MODEL_RESULT'
           OR model_result_row.operation_kind<>'RETRIEVAL_PLAN'
           OR artifact_json#>'{payload,requires_clarification}' IS DISTINCT FROM 'true'::jsonb THEN
            RAISE EXCEPTION 'workspace analysis clarification proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED' THEN
        CASE operation_row.operation_kind
            WHEN 'RETRIEVAL_PLAN' THEN
                expected_model_calls := 1;
                expected_tool_calls := 0;
                expected_source_reads := 0;
                expected_input_tokens := 65536;
                expected_output_tokens := 256;
            WHEN 'ANSWER_SYNTHESIS' THEN
                expected_model_calls := 1;
                expected_tool_calls := 0;
                expected_source_reads := 0;
                expected_input_tokens := 65536;
                expected_output_tokens := run_row.max_output_tokens-1280;
            WHEN 'FAITHFULNESS_REVIEW' THEN
                expected_model_calls := 1;
                expected_tool_calls := 0;
                expected_source_reads := 0;
                expected_input_tokens := 65536;
                expected_output_tokens := 1024;
            WHEN 'SOURCE_READ' THEN
                expected_model_calls := 0;
                expected_tool_calls := 1;
                expected_source_reads := 1;
                expected_input_tokens := 0;
                expected_output_tokens := 0;
            ELSE
                expected_model_calls := 0;
                expected_tool_calls := 1;
                expected_source_reads := 0;
                expected_input_tokens := 0;
                expected_output_tokens := 0;
        END CASE;
        IF NEW.artifact_kind IS NOT NULL
           OR NEW.requested_model_calls IS DISTINCT FROM expected_model_calls
           OR NEW.requested_tool_calls IS DISTINCT FROM expected_tool_calls
           OR NEW.requested_source_reads IS DISTINCT FROM expected_source_reads
           OR NEW.requested_input_tokens IS DISTINCT FROM expected_input_tokens
           OR NEW.requested_output_tokens IS DISTINCT FROM expected_output_tokens
           OR NEW.requested_cost_microunits IS NOT NULL
           OR NEW.expected_hash IS NOT NULL OR NEW.actual_hash IS NOT NULL
           OR EXISTS (
                SELECT 1
                  FROM agent.workspace_analysis_operation AS prior
                 WHERE prior.analysis_run_id=NEW.analysis_run_id
                   AND prior.id<>NEW.operation_id
                   AND prior.status IN ('FAILED','UNKNOWN')
           )
           OR (
                NEW.requested_model_calls<=run_row.max_model_calls-run_row.reserved_model_calls-run_row.settled_model_calls
                AND NEW.requested_tool_calls<=run_row.max_tool_calls-run_row.reserved_tool_calls-run_row.settled_tool_calls
                AND NEW.requested_source_reads<=run_row.max_source_reads-run_row.reserved_source_reads-run_row.settled_source_reads
                AND NEW.requested_input_tokens<=run_row.max_input_tokens-run_row.reserved_input_tokens-run_row.settled_input_tokens
                AND NEW.requested_output_tokens<=run_row.max_output_tokens-run_row.reserved_output_tokens-run_row.settled_output_tokens
           ) THEN
            RAISE EXCEPTION 'workspace analysis budget request proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_RECEIPT_INVALID' THEN
        SELECT * INTO receipt_failure_row
          FROM workflow.tool_result_receipt_failure
         WHERE id=NEW.receipt_failure_id
           AND analysis_run_id=NEW.analysis_run_id
           AND operation_id=NEW.operation_id
           AND tool_call_id=operation_row.tool_call_id
         FOR SHARE;
        IF operation_row.call_kind<>'TOOL'
           OR operation_row.status<>'FAILED'
           OR operation_row.error_code<>'WORKSPACE_ANALYSIS_RECEIPT_INVALID'
           OR tool_call_row.status<>'SUCCEEDED'
           OR NEW.artifact_kind IS NOT NULL
           OR receipt_failure_row.id IS NULL
           OR receipt_failure_row.failure_code IS DISTINCT FROM NEW.receipt_failure_code
           OR receipt_failure_row.expected_output_hash IS DISTINCT FROM NEW.expected_hash
           OR receipt_failure_row.observed_output_hash IS DISTINCT FROM NEW.actual_hash
           OR NEW.checked_at<receipt_failure_row.created_at
           OR EXISTS (
                SELECT 1 FROM workflow.tool_result_receipt
                 WHERE tool_call_id=operation_row.tool_call_id
           ) THEN
            RAISE EXCEPTION 'workspace analysis receipt invalid proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_RESULT_UNKNOWN' THEN
        IF operation_row.status<>'UNKNOWN'
           OR reservation_row.status<>'UNKNOWN_CHARGED'
           OR COALESCE(model_call_row.status,tool_call_row.status)<>'UNKNOWN'
           OR NEW.artifact_kind IS NOT NULL THEN
            RAISE EXCEPTION 'workspace analysis unknown result proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED' THEN
        call_timeout_ms := CASE operation_row.operation_kind
            WHEN 'GIT_STATUS' THEN run_row.git_tool_timeout_ms
            WHEN 'RETRIEVAL_PLAN' THEN run_row.plan_model_timeout_ms
            WHEN 'KNOWLEDGE_SEARCH' THEN run_row.search_tool_timeout_ms
            WHEN 'SOURCE_READ' THEN run_row.source_read_tool_timeout_ms
            WHEN 'ANSWER_SYNTHESIS' THEN run_row.synthesis_model_timeout_ms
            WHEN 'CITATION_VALIDATION' THEN run_row.validate_citation_tool_timeout_ms
            WHEN 'FAITHFULNESS_REVIEW' THEN run_row.review_model_timeout_ms
        END;
        IF NEW.artifact_kind IS NOT NULL
           OR call_timeout_ms IS NULL
           OR EXISTS (
                SELECT 1 FROM agent.workspace_analysis_operation AS prior
                 WHERE prior.analysis_run_id=NEW.analysis_run_id
                   AND prior.id<>NEW.operation_id
                   AND prior.status IN ('FAILED','UNKNOWN')
           )
           OR (
                operation_row.status='PENDING'
                AND NEW.checked_at+(call_timeout_ms+run_row.durable_completion_margin_ms)*interval '1 millisecond'<=run_row.deadline_at
           )
           OR (
                operation_row.status='SUCCEEDED'
                AND (
                    NEW.checked_at<run_row.deadline_at
                    OR EXISTS (
                        SELECT 1
                          FROM agent.workspace_analysis_operation AS later
                         WHERE later.analysis_run_id=NEW.analysis_run_id
                           AND later.status='SUCCEEDED'
                           AND ROW(later.completed_at,later.id)>ROW(operation_row.completed_at,operation_row.id)
                    )
                )
           ) THEN
            RAISE EXCEPTION 'workspace analysis deadline proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_MODEL_FAILED' THEN
        IF operation_row.call_kind<>'MODEL'
           OR operation_row.status<>'FAILED'
           OR model_call_row.status<>'FAILED'
           OR model_run_row.status<>'FAILED'
           OR NEW.artifact_kind IS NOT NULL THEN
            RAISE EXCEPTION 'workspace analysis model failure proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_TOOL_FAILED' THEN
        IF operation_row.call_kind<>'TOOL'
           OR operation_row.status<>'FAILED'
           OR tool_call_row.status<>'FAILED'
           OR NEW.artifact_kind IS NOT NULL THEN
            RAISE EXCEPTION 'workspace analysis tool failure proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_CANCELLED' THEN
        IF NEW.operation_id IS NOT NULL OR NEW.artifact_kind IS NOT NULL
           OR NEW.expected_hash IS NOT NULL OR NEW.actual_hash IS NOT NULL
           OR NEW.requested_model_calls IS NOT NULL OR NEW.requested_tool_calls IS NOT NULL
           OR NEW.requested_source_reads IS NOT NULL OR NEW.requested_input_tokens IS NOT NULL
           OR NEW.requested_output_tokens IS NOT NULL OR NEW.requested_cost_microunits IS NOT NULL THEN
            RAISE EXCEPTION 'workspace analysis cancellation proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSE
        RAISE EXCEPTION 'workspace analysis termination reason is unsupported' USING ERRCODE='55000';
    END IF;

    published_json := convert_from(NEW.published_document,'UTF8')::jsonb;
    published_payload := published_json->'payload';
    IF jsonb_typeof(published_json) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_json))<>5
       OR published_json->>'schema_version' IS DISTINCT FROM 'v1'
       OR NOT (published_json ? 'model_run_ref')
       OR jsonb_typeof(published_payload) IS DISTINCT FROM 'object'
       OR (
            NEW.published_model_run_id IS NULL
            AND jsonb_typeof(published_json->'model_run_ref') IS DISTINCT FROM 'null'
       )
       OR (
            NEW.published_model_run_id IS NOT NULL
            AND (
                jsonb_typeof(published_json->'model_run_ref') IS DISTINCT FROM 'string'
                OR published_json->>'model_run_ref' IS DISTINCT FROM NEW.published_model_run_id::text
            )
       ) THEN
        RAISE EXCEPTION 'workspace analysis termination publication envelope is invalid' USING ERRCODE='55000';
    END IF;

    IF NEW.reason IN (
        'WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT',
        'WORKSPACE_ANALYSIS_CITATION_INVALID',
        'WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED',
        'WORKSPACE_ANALYSIS_MODEL_REFUSED'
    ) THEN
        published_summary := published_payload->>'summary';
        IF published_json->>'result_type' IS DISTINCT FROM 'workspace_analysis_refusal'
           OR published_json->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_refusal'
           OR (SELECT count(*) FROM jsonb_object_keys(published_payload))<>2
           OR published_payload->>'reason_code' IS DISTINCT FROM NEW.reason
           OR jsonb_typeof(published_payload->'summary') IS DISTINCT FROM 'string'
           OR published_summary=''
           OR published_summary IS DISTINCT FROM btrim(published_summary)
           OR octet_length(convert_to(published_summary,'UTF8'))>4096
           OR (
                NEW.reason='WORKSPACE_ANALYSIS_MODEL_REFUSED'
                AND NEW.published_model_run_id IS DISTINCT FROM model_run_row.id
           )
           OR (
                NEW.reason<>'WORKSPACE_ANALYSIS_MODEL_REFUSED'
                AND NEW.published_model_run_id IS NOT NULL
           ) THEN
            RAISE EXCEPTION 'workspace analysis refusal publication is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED' THEN
        IF published_json->>'result_type' IS DISTINCT FROM 'clarification'
           OR published_json->>'schema_id' IS DISTINCT FROM 'conversation.clarification'
           OR NEW.published_model_run_id IS DISTINCT FROM model_result_row.model_run_id
           OR (SELECT count(*) FROM jsonb_object_keys(published_payload))<>3
           OR published_payload->'reason' IS DISTINCT FROM artifact_json#>'{payload,clarification_reason}'
           OR published_payload->'question' IS DISTINCT FROM artifact_json#>'{payload,clarification_question}'
           OR published_payload->'suggested_scopes' IS DISTINCT FROM artifact_json#>'{payload,suggested_scopes}' THEN
            RAISE EXCEPTION 'workspace analysis clarification publication is invalid' USING ERRCODE='55000';
        END IF;
    ELSE
        published_summary := published_payload->>'summary';
        IF published_json->>'result_type' IS DISTINCT FROM 'workspace_analysis_termination'
           OR published_json->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_termination'
           OR (SELECT count(*) FROM jsonb_object_keys(published_payload))<>2
           OR published_payload->>'termination_reason' IS DISTINCT FROM NEW.reason
           OR jsonb_typeof(published_payload->'summary') IS DISTINCT FROM 'string'
           OR published_summary=''
           OR published_summary IS DISTINCT FROM btrim(published_summary)
           OR octet_length(convert_to(published_summary,'UTF8'))>4096
           OR (
                NEW.published_model_run_id IS NOT NULL
                AND (
                    NEW.reason NOT IN ('WORKSPACE_ANALYSIS_RESULT_UNKNOWN','WORKSPACE_ANALYSIS_MODEL_FAILED')
                    OR operation_row.call_kind<>'MODEL'
                    OR NEW.published_model_run_id IS DISTINCT FROM model_run_row.id
                )
           ) THEN
            RAISE EXCEPTION 'workspace analysis failure publication is invalid' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_operation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_row agent.workspace_analysis_run%ROWTYPE;
    run_status_value text;
    node_key_value text;
    node_status text;
    node_attempt_no integer;
    node_lease_owner text;
    node_lease_until timestamptz;
    workflow_status text;
    workflow_cancel_requested_at timestamptz;
    attempt_status text;
    attempt_no integer;
    attempt_lease_owner text;
    attempt_lease_until timestamptz;
    call_status text;
    model_run_status text;
    model_run_final_result_type text;
    call_request_hash text;
    call_response_hash text;
    call_workspace_id uuid;
    call_workflow_run_id uuid;
    call_node_run_id uuid;
    call_node_attempt_id uuid;
    tool_name_value text;
    tool_version_value bigint;
    model_phase_value text;
    model_result_hash text;
    receipt_hash text;
    candidate_hash text;
    reservation_status text;
    terminal_replay boolean;
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'workspace analysis operation is append-only' USING ERRCODE='55000';
    END IF;
    IF TG_OP='INSERT' THEN
        IF NEW.status<>'PENDING' OR NEW.version<>1 THEN
            RAISE EXCEPTION 'workspace analysis operation must start pending at version one' USING ERRCODE='23514';
        END IF;
        SELECT analysis.status,node.node_key,node.status,workflow.status,workflow.cancel_requested_at
          INTO run_status_value,node_key_value,node_status,workflow_status,workflow_cancel_requested_at
          FROM agent.workspace_analysis_run AS analysis
          JOIN workflow.run AS workflow
            ON workflow.id=analysis.workflow_run_id
           AND workflow.workspace_id=analysis.workspace_id
          JOIN workflow.node_run AS node
            ON node.run_id=workflow.id
         WHERE analysis.id=NEW.analysis_run_id
           AND analysis.workspace_id=NEW.workspace_id
           AND analysis.workflow_run_id=NEW.workflow_run_id
           AND node.id=NEW.node_run_id
         FOR SHARE OF analysis,workflow,node;
        IF run_status_value IS NULL
           OR run_status_value NOT IN ('queued','running')
           OR workflow_status NOT IN ('pending','running','retry_wait')
           OR workflow_cancel_requested_at IS NOT NULL
           OR node_status NOT IN ('pending','running','retry_wait')
           OR node_key_value IS DISTINCT FROM NEW.node_key THEN
            RAISE EXCEPTION 'workspace analysis operation node binding is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;

    terminal_replay := OLD.status IN ('SUCCEEDED','FAILED','UNKNOWN') AND NEW.status=OLD.status;
    IF (OLD.status NOT IN ('PENDING','STARTED') AND NOT terminal_replay)
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.analysis_run_id IS DISTINCT FROM OLD.analysis_run_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id
       OR NEW.node_key IS DISTINCT FROM OLD.node_key
       OR NEW.operation_kind IS DISTINCT FROM OLD.operation_kind
       OR NEW.ordinal IS DISTINCT FROM OLD.ordinal
       OR NEW.call_kind IS DISTINCT FROM OLD.call_kind
       OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.version<>OLD.version+1
       OR NEW.updated_at<OLD.updated_at
       OR (OLD.first_node_attempt_id IS NOT NULL AND NEW.first_node_attempt_id IS DISTINCT FROM OLD.first_node_attempt_id)
       OR (OLD.model_call_id IS NOT NULL AND NEW.model_call_id IS DISTINCT FROM OLD.model_call_id)
       OR (OLD.tool_call_id IS NOT NULL AND NEW.tool_call_id IS DISTINCT FROM OLD.tool_call_id) THEN
        RAISE EXCEPTION 'workspace analysis operation immutable binding changed' USING ERRCODE='55000';
    END IF;
    IF terminal_replay AND (
            NEW.latest_node_attempt_id IS NOT DISTINCT FROM OLD.latest_node_attempt_id
            OR NEW.result_kind IS DISTINCT FROM OLD.result_kind
            OR NEW.result_id IS DISTINCT FROM OLD.result_id
            OR NEW.result_hash IS DISTINCT FROM OLD.result_hash
            OR NEW.error_code IS DISTINCT FROM OLD.error_code
            OR NEW.started_at IS DISTINCT FROM OLD.started_at
            OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
       ) THEN
        RAISE EXCEPTION 'workspace analysis terminal replay changed durable result facts' USING ERRCODE='55000';
    END IF;

    IF OLD.status='PENDING' AND NEW.status<>'STARTED' THEN
        RAISE EXCEPTION 'workspace analysis operation must authorize before completion' USING ERRCODE='55000';
    END IF;
    IF OLD.status='PENDING'
       AND NOT agent.is_workspace_analysis_operation_next(NEW.id) THEN
        RAISE EXCEPTION 'workspace analysis operation is not the next v1 graph step' USING ERRCODE='55000';
    END IF;
    IF OLD.status='PENDING'
       AND NEW.first_node_attempt_id IS DISTINCT FROM NEW.latest_node_attempt_id THEN
        RAISE EXCEPTION 'workspace analysis first authorization attempt is inconsistent' USING ERRCODE='55000';
    END IF;
    IF OLD.status='STARTED' AND NEW.status NOT IN ('SUCCEEDED','FAILED','UNKNOWN') THEN
        RAISE EXCEPTION 'workspace analysis operation terminal transition is invalid' USING ERRCODE='55000';
    END IF;

    IF NEW.status IN ('STARTED','SUCCEEDED','FAILED','UNKNOWN') THEN
        SELECT workflow.status,workflow.cancel_requested_at,
               node.status,node.attempt,node.lease_owner,node.lease_until,
               attempt.status,attempt.attempt_no,attempt.lease_owner,attempt.lease_until
          INTO workflow_status,workflow_cancel_requested_at,
               node_status,node_attempt_no,node_lease_owner,node_lease_until,
               attempt_status,attempt_no,attempt_lease_owner,attempt_lease_until
          FROM workflow.run AS workflow
          JOIN workflow.node_run AS node
            ON node.run_id=workflow.id
           AND node.id=NEW.node_run_id
          JOIN workflow.node_attempt AS attempt
            ON attempt.node_run_id=node.id
           AND attempt.id=NEW.latest_node_attempt_id
         WHERE workflow.id=NEW.workflow_run_id
           AND workflow.workspace_id=NEW.workspace_id
         FOR SHARE OF workflow,node,attempt;
        SELECT * INTO run_row
          FROM agent.workspace_analysis_run
         WHERE id=NEW.analysis_run_id
           AND workspace_id=NEW.workspace_id
           AND workflow_run_id=NEW.workflow_run_id
         FOR SHARE;
        IF run_row.status<>'running'
           OR workflow_status<>'running'
           OR node_status<>'running'
           OR node_attempt_no IS DISTINCT FROM attempt_no
           OR node_lease_owner IS NULL
           OR node_lease_owner IS DISTINCT FROM btrim(node_lease_owner)
           OR node_lease_owner=''
           OR attempt_lease_owner IS DISTINCT FROM node_lease_owner
           OR node_lease_until IS NULL
           OR node_lease_until IS DISTINCT FROM attempt_lease_until
           OR attempt_status<>'running'
           OR attempt_lease_until IS NULL
           OR attempt_lease_until<=clock_timestamp() THEN
            RAISE EXCEPTION 'workspace analysis operation attempt fence is not active' USING ERRCODE='55000';
        END IF;
        IF NEW.status='STARTED'
           AND (run_row.deadline_at<=clock_timestamp() OR workflow_cancel_requested_at IS NOT NULL) THEN
            RAISE EXCEPTION 'workspace analysis operation authorization is not active' USING ERRCODE='55000';
        END IF;
    END IF;

    IF NEW.call_kind='MODEL' THEN
        SELECT call.status,model.status,model.final_result_type,call.request_hash,call.response_hash,call.phase,
               model.workspace_id,model.workflow_run_id,model.node_run_id,model.node_attempt_id
          INTO call_status,model_run_status,model_run_final_result_type,call_request_hash,call_response_hash,model_phase_value,
               call_workspace_id,call_workflow_run_id,call_node_run_id,call_node_attempt_id
          FROM agent.model_call AS call
          JOIN agent.model_run AS model ON model.id=call.model_run_id
         WHERE call.id=NEW.model_call_id
         FOR SHARE OF call,model;
    ELSE
        SELECT status,request_hash,response_hash,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
               requested_tool_name,tool_version
          INTO call_status,call_request_hash,call_response_hash,
               call_workspace_id,call_workflow_run_id,call_node_run_id,call_node_attempt_id,
               tool_name_value,tool_version_value
          FROM workflow.tool_call
         WHERE id=NEW.tool_call_id
         FOR SHARE;
    END IF;
    IF call_request_hash IS DISTINCT FROM NEW.request_hash
       OR call_workspace_id IS DISTINCT FROM NEW.workspace_id
       OR call_workflow_run_id IS DISTINCT FROM NEW.workflow_run_id
       OR call_node_run_id IS DISTINCT FROM NEW.node_run_id
       OR call_node_attempt_id IS DISTINCT FROM NEW.first_node_attempt_id
       OR (
            NEW.call_kind='MODEL'
            AND NOT (
                (NEW.operation_kind='RETRIEVAL_PLAN' AND model_phase_value='PLAN')
                OR (NEW.operation_kind='ANSWER_SYNTHESIS' AND model_phase_value='ANSWER')
                OR (NEW.operation_kind='FAITHFULNESS_REVIEW' AND model_phase_value='REVIEW')
            )
       )
       OR (
            NEW.call_kind='TOOL'
            AND NOT (
                (NEW.operation_kind='GIT_STATUS' AND tool_name_value='ReadGitStatus' AND tool_version_value=2)
                OR (NEW.operation_kind='KNOWLEDGE_SEARCH' AND tool_name_value='SearchKnowledge' AND tool_version_value=2)
                OR (NEW.operation_kind='SOURCE_READ' AND tool_name_value='ReadSource' AND tool_version_value=3)
                OR (NEW.operation_kind='CITATION_VALIDATION' AND tool_name_value='ValidateCitation' AND tool_version_value=3)
            )
       ) THEN
        RAISE EXCEPTION 'workspace analysis operation call binding is invalid' USING ERRCODE='55000';
    END IF;

    IF NEW.status='STARTED' AND call_status<>'STARTED' THEN
        RAISE EXCEPTION 'workspace analysis operation requires a started call' USING ERRCODE='55000';
    ELSIF NEW.status='FAILED' THEN
        IF NEW.call_kind='MODEL' AND NEW.error_code='WORKSPACE_ANALYSIS_MODEL_REFUSED' THEN
            IF call_status<>'SUCCEEDED'
               OR model_run_status<>'REFUSED'
               OR model_run_final_result_type<>'refusal' THEN
                RAISE EXCEPTION 'workspace analysis model refusal proof is invalid' USING ERRCODE='55000';
            END IF;
        ELSIF NEW.error_code='WORKSPACE_ANALYSIS_RECEIPT_INVALID' THEN
            IF NEW.call_kind<>'TOOL'
               OR call_status<>'SUCCEEDED'
               OR NOT EXISTS (
                    SELECT 1
                      FROM workflow.tool_result_receipt_failure
                     WHERE operation_id=NEW.id
                       AND tool_call_id=NEW.tool_call_id
               )
               OR EXISTS (
                    SELECT 1
                      FROM workflow.tool_result_receipt
                     WHERE tool_call_id=NEW.tool_call_id
               ) THEN
                RAISE EXCEPTION 'workspace analysis receipt failure has no completed call' USING ERRCODE='55000';
            END IF;
        ELSIF call_status NOT IN ('FAILED','REFUSED') THEN
            RAISE EXCEPTION 'workspace analysis failed operation has no failed call' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.status='UNKNOWN' AND call_status<>'UNKNOWN' THEN
        RAISE EXCEPTION 'workspace analysis unknown operation has no unknown call' USING ERRCODE='55000';
    ELSIF NEW.status='SUCCEEDED' THEN
        IF call_status<>'SUCCEEDED' THEN
            RAISE EXCEPTION 'workspace analysis successful operation has no successful call' USING ERRCODE='55000';
        END IF;
        IF NEW.result_kind='MODEL_RESULT_RECEIPT' THEN
            SELECT document_hash INTO model_result_hash
              FROM agent.workspace_analysis_model_result
             WHERE id=NEW.result_id
               AND operation_id=NEW.id
               AND model_call_id=NEW.model_call_id
             FOR SHARE;
            IF model_result_hash IS DISTINCT FROM NEW.result_hash THEN
                RAISE EXCEPTION 'workspace analysis model result binding is invalid' USING ERRCODE='55000';
            END IF;
        ELSIF NEW.result_kind='TOOL_RESULT_RECEIPT' THEN
            SELECT output_hash INTO receipt_hash
              FROM workflow.tool_result_receipt
             WHERE id=NEW.result_id AND tool_call_id=NEW.tool_call_id
             FOR SHARE;
            IF receipt_hash IS DISTINCT FROM NEW.result_hash THEN
                RAISE EXCEPTION 'workspace analysis receipt result binding is invalid' USING ERRCODE='55000';
            END IF;
        ELSIF NEW.result_kind='SYNTHESIS_CANDIDATE' THEN
            SELECT document_hash INTO candidate_hash
              FROM agent.workspace_analysis_candidate
             WHERE id=NEW.result_id AND synthesis_operation_id=NEW.id
             FOR SHARE;
            IF candidate_hash IS DISTINCT FROM NEW.result_hash THEN
                RAISE EXCEPTION 'workspace analysis candidate result binding is invalid' USING ERRCODE='55000';
            END IF;
        END IF;
    END IF;

    IF NEW.status IN ('SUCCEEDED','FAILED','UNKNOWN') THEN
        SELECT status INTO reservation_status
          FROM agent.workspace_analysis_budget_reservation
         WHERE operation_id=NEW.id
         FOR SHARE;
        IF reservation_status IS NULL OR reservation_status='RESERVED'
           OR (NEW.status='UNKNOWN' AND reservation_status<>'UNKNOWN_CHARGED') THEN
            RAISE EXCEPTION 'workspace analysis operation budget is not settled' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_reservation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    run_row agent.workspace_analysis_run%ROWTYPE;
    call_status_value text;
    call_input_tokens bigint;
    call_output_tokens bigint;
    call_max_output_tokens integer;
    call_timeout_ms bigint;
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'workspace analysis reservation is append-only' USING ERRCODE='55000';
    END IF;

    IF TG_OP='INSERT' THEN
        IF NEW.status<>'RESERVED' THEN
            RAISE EXCEPTION 'workspace analysis reservation must start reserved' USING ERRCODE='23514';
        END IF;
        SELECT * INTO operation_row
          FROM agent.workspace_analysis_operation
         WHERE id=NEW.operation_id AND analysis_run_id=NEW.analysis_run_id
         FOR SHARE;
        SELECT * INTO run_row
          FROM agent.workspace_analysis_run
         WHERE id=NEW.analysis_run_id AND workspace_id=NEW.workspace_id
         FOR SHARE;
        IF NEW.call_kind='MODEL' THEN
            SELECT max_output_tokens INTO call_max_output_tokens
              FROM agent.model_call
             WHERE id=NEW.model_call_id
             FOR SHARE;
        END IF;
        call_timeout_ms := CASE operation_row.operation_kind
            WHEN 'GIT_STATUS' THEN run_row.git_tool_timeout_ms
            WHEN 'RETRIEVAL_PLAN' THEN run_row.plan_model_timeout_ms
            WHEN 'KNOWLEDGE_SEARCH' THEN run_row.search_tool_timeout_ms
            WHEN 'SOURCE_READ' THEN run_row.source_read_tool_timeout_ms
            WHEN 'ANSWER_SYNTHESIS' THEN run_row.synthesis_model_timeout_ms
            WHEN 'CITATION_VALIDATION' THEN run_row.validate_citation_tool_timeout_ms
            WHEN 'FAITHFULNESS_REVIEW' THEN run_row.review_model_timeout_ms
        END;
        IF operation_row.id IS NULL
           OR operation_row.status<>'STARTED'
           OR operation_row.call_kind<>NEW.call_kind
           OR operation_row.model_call_id IS DISTINCT FROM NEW.model_call_id
           OR operation_row.tool_call_id IS DISTINCT FROM NEW.tool_call_id
           OR run_row.id IS NULL
           OR run_row.status<>'running'
           OR call_timeout_ms IS NULL
           OR clock_timestamp()+(call_timeout_ms+run_row.durable_completion_margin_ms)*interval '1 millisecond'>run_row.deadline_at
           OR (NEW.call_kind='MODEL' AND NEW.reserved_output_tokens IS DISTINCT FROM call_max_output_tokens)
           OR NEW.reserved_cost_microunits IS NOT NULL
           OR (NEW.call_kind='MODEL' AND NEW.reserved_input_tokens<>65536)
           OR (operation_row.operation_kind='RETRIEVAL_PLAN' AND NEW.reserved_output_tokens<>256)
           OR (operation_row.operation_kind='ANSWER_SYNTHESIS' AND NEW.reserved_output_tokens<>run_row.max_output_tokens-1280)
           OR (operation_row.operation_kind='FAITHFULNESS_REVIEW' AND NEW.reserved_output_tokens<>1024)
           OR (NEW.reserved_source_reads=1)<>(operation_row.operation_kind='SOURCE_READ') THEN
            RAISE EXCEPTION 'workspace analysis reservation binding is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.status<>'RESERVED'
       OR NEW.status NOT IN ('SETTLED','UNKNOWN_CHARGED')
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.analysis_run_id IS DISTINCT FROM OLD.analysis_run_id
       OR NEW.operation_id IS DISTINCT FROM OLD.operation_id
       OR NEW.call_kind IS DISTINCT FROM OLD.call_kind
       OR NEW.model_call_id IS DISTINCT FROM OLD.model_call_id
       OR NEW.tool_call_id IS DISTINCT FROM OLD.tool_call_id
       OR NEW.reserved_model_calls IS DISTINCT FROM OLD.reserved_model_calls
       OR NEW.reserved_tool_calls IS DISTINCT FROM OLD.reserved_tool_calls
       OR NEW.reserved_source_reads IS DISTINCT FROM OLD.reserved_source_reads
       OR NEW.reserved_input_tokens IS DISTINCT FROM OLD.reserved_input_tokens
       OR NEW.reserved_output_tokens IS DISTINCT FROM OLD.reserved_output_tokens
       OR NEW.reserved_cost_microunits IS DISTINCT FROM OLD.reserved_cost_microunits
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'workspace analysis reservation transition is invalid' USING ERRCODE='55000';
    END IF;
    IF NEW.status='SETTLED' AND NEW.call_kind='MODEL' THEN
        SELECT status,input_tokens,output_tokens
          INTO call_status_value,call_input_tokens,call_output_tokens
          FROM agent.model_call
         WHERE id=NEW.model_call_id
         FOR SHARE;
        IF call_status_value NOT IN ('SUCCEEDED','FAILED')
           OR NEW.settled_model_calls<>1
           OR NEW.settled_tool_calls<>0
           OR NEW.settled_source_reads<>0
           OR NEW.settled_input_tokens IS DISTINCT FROM call_input_tokens
           OR NEW.settled_output_tokens IS DISTINCT FROM call_output_tokens THEN
            RAISE EXCEPTION 'workspace analysis model settlement does not match actual call usage' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.status='SETTLED' AND NEW.call_kind='TOOL' THEN
        SELECT status INTO call_status_value
          FROM workflow.tool_call
         WHERE id=NEW.tool_call_id
         FOR SHARE;
        IF call_status_value NOT IN ('SUCCEEDED','FAILED')
           OR NEW.settled_model_calls<>0
           OR NEW.settled_tool_calls<>1
           OR NEW.settled_source_reads<>NEW.reserved_source_reads
           OR NEW.settled_input_tokens<>0
           OR NEW.settled_output_tokens<>0 THEN
            RAISE EXCEPTION 'workspace analysis tool settlement does not match actual call usage' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.verify_workspace_analysis_budget_totals(target_run_id uuid)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    run_row agent.workspace_analysis_run%ROWTYPE;
    active_model_calls bigint;
    active_tool_calls bigint;
    active_source_reads bigint;
    active_input_tokens bigint;
    active_output_tokens bigint;
    active_cost bigint;
    used_model_calls bigint;
    used_tool_calls bigint;
    used_source_reads bigint;
    used_input_tokens bigint;
    used_output_tokens bigint;
    used_cost bigint;
BEGIN
    SELECT * INTO run_row
      FROM agent.workspace_analysis_run
     WHERE id=target_run_id;
    IF run_row.id IS NULL THEN
        RETURN;
    END IF;

    SELECT
        COALESCE(sum(reserved_model_calls) FILTER (WHERE status='RESERVED'),0),
        COALESCE(sum(reserved_tool_calls) FILTER (WHERE status='RESERVED'),0),
        COALESCE(sum(reserved_source_reads) FILTER (WHERE status='RESERVED'),0),
        COALESCE(sum(reserved_input_tokens) FILTER (WHERE status='RESERVED'),0),
        COALESCE(sum(reserved_output_tokens) FILTER (WHERE status='RESERVED'),0),
        COALESCE(sum(reserved_cost_microunits) FILTER (WHERE status='RESERVED'),0),
        COALESCE(sum(settled_model_calls) FILTER (WHERE status<>'RESERVED'),0),
        COALESCE(sum(settled_tool_calls) FILTER (WHERE status<>'RESERVED'),0),
        COALESCE(sum(settled_source_reads) FILTER (WHERE status<>'RESERVED'),0),
        COALESCE(sum(settled_input_tokens) FILTER (WHERE status<>'RESERVED'),0),
        COALESCE(sum(settled_output_tokens) FILTER (WHERE status<>'RESERVED'),0),
        COALESCE(sum(settled_cost_microunits) FILTER (WHERE status<>'RESERVED'),0)
      INTO active_model_calls,active_tool_calls,active_source_reads,
           active_input_tokens,active_output_tokens,active_cost,
           used_model_calls,used_tool_calls,used_source_reads,
           used_input_tokens,used_output_tokens,used_cost
      FROM agent.workspace_analysis_budget_reservation
     WHERE analysis_run_id=target_run_id;

    IF run_row.reserved_model_calls<>active_model_calls
       OR run_row.reserved_tool_calls<>active_tool_calls
       OR run_row.reserved_source_reads<>active_source_reads
       OR run_row.reserved_input_tokens<>active_input_tokens
       OR run_row.reserved_output_tokens<>active_output_tokens
       OR run_row.settled_model_calls<>used_model_calls
       OR run_row.settled_tool_calls<>used_tool_calls
       OR run_row.settled_source_reads<>used_source_reads
       OR run_row.settled_input_tokens<>used_input_tokens
       OR run_row.settled_output_tokens<>used_output_tokens
       OR (run_row.max_cost_microunits IS NOT NULL AND (
            run_row.reserved_cost_microunits<>active_cost
            OR run_row.settled_cost_microunits<>used_cost
       )) THEN
        RAISE EXCEPTION 'workspace analysis budget totals drifted' USING ERRCODE='55000';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_budget_totals()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM agent.verify_workspace_analysis_budget_totals(NEW.analysis_run_id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_run_budget_totals()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM agent.verify_workspace_analysis_budget_totals(NEW.id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.verify_workspace_analysis_operation_closure(target_operation_id uuid)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    reservation_row agent.workspace_analysis_budget_reservation%ROWTYPE;
BEGIN
    SELECT * INTO operation_row
      FROM agent.workspace_analysis_operation
     WHERE id=target_operation_id;
    IF operation_row.id IS NULL THEN
        RETURN;
    END IF;
    SELECT * INTO reservation_row
      FROM agent.workspace_analysis_budget_reservation
     WHERE operation_id=target_operation_id;

    IF operation_row.status='PENDING' THEN
        IF reservation_row.id IS NOT NULL THEN
            RAISE EXCEPTION 'pending workspace analysis operation has a reservation' USING ERRCODE='55000';
        END IF;
        RETURN;
    END IF;
    IF reservation_row.id IS NULL
       OR reservation_row.analysis_run_id<>operation_row.analysis_run_id
       OR reservation_row.workspace_id<>operation_row.workspace_id
       OR reservation_row.call_kind<>operation_row.call_kind
       OR reservation_row.model_call_id IS DISTINCT FROM operation_row.model_call_id
       OR reservation_row.tool_call_id IS DISTINCT FROM operation_row.tool_call_id
       OR (operation_row.status='STARTED' AND reservation_row.status<>'RESERVED')
       OR (operation_row.status IN ('SUCCEEDED','FAILED') AND reservation_row.status<>'SETTLED')
       OR (operation_row.status='UNKNOWN' AND reservation_row.status<>'UNKNOWN_CHARGED') THEN
        RAISE EXCEPTION 'workspace analysis operation reservation closure is invalid' USING ERRCODE='55000';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_operation_closure()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM agent.verify_workspace_analysis_operation_closure(NEW.id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_reservation_closure()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM agent.verify_workspace_analysis_operation_closure(NEW.operation_id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.verify_workspace_analysis_model_call_closure(target_call_id uuid)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    analysis_run_id_value uuid;
    operation_id_value uuid;
    call_status_value text;
    operation_status_value text;
    operation_error_code_value text;
    reservation_status_value text;
BEGIN
    SELECT analysis.id,operation.id,model_call.status,operation.status,operation.error_code,reservation.status
      INTO analysis_run_id_value,operation_id_value,call_status_value,operation_status_value,
           operation_error_code_value,reservation_status_value
      FROM agent.model_call AS model_call
      JOIN agent.model_run AS model_run ON model_run.id=model_call.model_run_id
      JOIN agent.workspace_analysis_run AS analysis
        ON analysis.workspace_id=model_run.workspace_id
       AND analysis.workflow_run_id=model_run.workflow_run_id
      LEFT JOIN agent.workspace_analysis_operation AS operation
        ON operation.analysis_run_id=analysis.id
       AND operation.model_call_id=model_call.id
      LEFT JOIN agent.workspace_analysis_budget_reservation AS reservation
        ON reservation.operation_id=operation.id
     WHERE model_call.id=target_call_id;
    IF analysis_run_id_value IS NULL THEN
        RETURN;
    END IF;
    IF operation_id_value IS NULL THEN
        RAISE EXCEPTION 'workspace analysis model call has no logical operation' USING ERRCODE='55000';
    END IF;
    PERFORM agent.verify_workspace_analysis_operation_closure(operation_id_value);
    IF NOT (
        (call_status_value='STARTED' AND operation_status_value='STARTED' AND reservation_status_value='RESERVED')
        OR (
            call_status_value='SUCCEEDED'
            AND (
                (operation_status_value='SUCCEEDED' AND reservation_status_value='SETTLED')
                OR (
                    operation_status_value='FAILED'
                    AND operation_error_code_value='WORKSPACE_ANALYSIS_MODEL_REFUSED'
                    AND reservation_status_value='SETTLED'
                )
            )
        )
        OR (call_status_value='FAILED' AND operation_status_value='FAILED' AND reservation_status_value='SETTLED')
        OR (call_status_value='UNKNOWN' AND operation_status_value='UNKNOWN' AND reservation_status_value='UNKNOWN_CHARGED')
    ) THEN
        RAISE EXCEPTION 'workspace analysis model call transaction closure is invalid' USING ERRCODE='55000';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.verify_workspace_analysis_tool_call_closure(target_call_id uuid)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    analysis_run_id_value uuid;
    operation_id_value uuid;
    call_status_value text;
    workflow_run_id_value uuid;
    operation_status_value text;
    operation_error_code_value text;
    reservation_status_value text;
BEGIN
    SELECT analysis.id,operation.id,tool_call.status,tool_call.workflow_run_id,
           operation.status,operation.error_code,reservation.status
      INTO analysis_run_id_value,operation_id_value,call_status_value,workflow_run_id_value,
           operation_status_value,operation_error_code_value,reservation_status_value
      FROM workflow.tool_call AS tool_call
      JOIN agent.workspace_analysis_run AS analysis
        ON analysis.workspace_id=tool_call.workspace_id
       AND analysis.workflow_run_id=tool_call.workflow_run_id
      LEFT JOIN agent.workspace_analysis_operation AS operation
        ON operation.analysis_run_id=analysis.id
       AND operation.tool_call_id=tool_call.id
      LEFT JOIN agent.workspace_analysis_budget_reservation AS reservation
        ON reservation.operation_id=operation.id
     WHERE tool_call.id=target_call_id;
    IF analysis_run_id_value IS NULL OR call_status_value='REFUSED' THEN
        RETURN;
    END IF;
    IF operation_id_value IS NULL THEN
        RAISE EXCEPTION 'workspace analysis tool call has no logical operation' USING ERRCODE='55000';
    END IF;
    IF call_status_value='STARTED' AND EXISTS (
        SELECT 1
          FROM workflow.tool_call
         WHERE workflow_run_id=workflow_run_id_value
           AND status='STARTED'
           AND id<>target_call_id
    ) THEN
        RAISE EXCEPTION 'workspace analysis tool calls must remain serial' USING ERRCODE='55000';
    END IF;
    PERFORM agent.verify_workspace_analysis_operation_closure(operation_id_value);
    IF NOT (
        (call_status_value='STARTED' AND operation_status_value='STARTED' AND reservation_status_value='RESERVED')
        OR (
            call_status_value='SUCCEEDED'
            AND reservation_status_value='SETTLED'
            AND (
                operation_status_value='SUCCEEDED'
                AND EXISTS (
                    SELECT 1 FROM workflow.tool_result_receipt
                     WHERE tool_call_id=target_call_id
                )
                AND NOT EXISTS (
                    SELECT 1 FROM workflow.tool_result_receipt_failure
                     WHERE tool_call_id=target_call_id
                )
                OR (
                    operation_status_value='FAILED'
                    AND operation_error_code_value='WORKSPACE_ANALYSIS_RECEIPT_INVALID'
                    AND EXISTS (
                        SELECT 1 FROM workflow.tool_result_receipt_failure
                         WHERE tool_call_id=target_call_id
                    )
                    AND NOT EXISTS (
                        SELECT 1 FROM workflow.tool_result_receipt
                         WHERE tool_call_id=target_call_id
                    )
                )
            )
        )
        OR (call_status_value='FAILED' AND operation_status_value='FAILED' AND reservation_status_value='SETTLED')
        OR (call_status_value='UNKNOWN' AND operation_status_value='UNKNOWN' AND reservation_status_value='UNKNOWN_CHARGED')
    ) THEN
        RAISE EXCEPTION 'workspace analysis tool call transaction closure is invalid' USING ERRCODE='55000';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_model_result_closure()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM agent.verify_workspace_analysis_model_call_closure(NEW.model_call_id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_candidate_closure()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    model_call_id_value uuid;
BEGIN
    SELECT model_call_id INTO model_call_id_value
      FROM agent.workspace_analysis_operation
     WHERE id=NEW.synthesis_operation_id;
    IF model_call_id_value IS NULL THEN
        RAISE EXCEPTION 'workspace analysis candidate has no model call' USING ERRCODE='55000';
    END IF;
    PERFORM agent.verify_workspace_analysis_model_call_closure(model_call_id_value);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION workflow.guard_workspace_analysis_tool_receipt_closure()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM agent.verify_workspace_analysis_tool_call_closure(NEW.tool_call_id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_model_call_closure()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM agent.verify_workspace_analysis_model_call_closure(NEW.id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_tool_call_closure()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM agent.verify_workspace_analysis_tool_call_closure(NEW.id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.verify_workspace_analysis_publication_closure(answer_id_value uuid)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    question_mode_value text;
    answer_status_value text;
    answer_result_type_value text;
    answer_result_value jsonb;
    answer_result_hash_value text;
    answer_model_run_id_value uuid;
    analysis_status_value text;
    analysis_reason_value text;
    analysis_validation_receipt_id uuid;
    analysis_review_model_run_id uuid;
    expected_answer_status text;
    proof_row agent.workspace_analysis_publication_proof%ROWTYPE;
    termination_proof_row agent.workspace_analysis_termination_proof%ROWTYPE;
    proof_review_model_run_id uuid;
BEGIN
    SELECT question.mode,answer.publication_status,answer.result_type,answer.result,answer.result_hash,answer.model_run_id
      INTO question_mode_value,answer_status_value,answer_result_type_value,answer_result_value,answer_result_hash_value,answer_model_run_id_value
      FROM agent.answer AS answer
      JOIN agent.question AS question
        ON question.id=answer.question_id
       AND question.workspace_id=answer.workspace_id
       AND question.conversation_id=answer.conversation_id
     WHERE answer.id=answer_id_value;
    IF answer_status_value IS NULL OR question_mode_value<>'workspace_analysis' THEN
        RETURN;
    END IF;

    SELECT status,termination_reason,validation_receipt_id,review_model_run_id
      INTO analysis_status_value,analysis_reason_value,analysis_validation_receipt_id,analysis_review_model_run_id
      FROM agent.workspace_analysis_run
     WHERE answer_id=answer_id_value;
    IF analysis_status_value IS NULL THEN
        RAISE EXCEPTION 'workspace analysis answer has no durable analysis run' USING ERRCODE='55000';
    END IF;
    expected_answer_status := CASE analysis_status_value
        WHEN 'queued' THEN 'pending'
        WHEN 'running' THEN 'pending'
        WHEN 'succeeded' THEN 'completed'
        WHEN 'refused' THEN 'refused'
        WHEN 'clarification_required' THEN 'clarification_required'
        WHEN 'failed' THEN 'failed'
        WHEN 'cancelled' THEN 'cancelled'
        ELSE NULL
    END;
    IF answer_status_value IS DISTINCT FROM expected_answer_status THEN
        RAISE EXCEPTION 'workspace analysis run and answer terminal states are not closed' USING ERRCODE='55000';
    END IF;
    SELECT * INTO proof_row
      FROM agent.workspace_analysis_publication_proof
     WHERE answer_id=answer_id_value;
    SELECT * INTO termination_proof_row
      FROM agent.workspace_analysis_termination_proof
     WHERE answer_id=answer_id_value;
    IF analysis_status_value='succeeded' THEN
        SELECT model_run_id INTO proof_review_model_run_id
          FROM agent.workspace_analysis_model_result
         WHERE id=proof_row.review_model_result_id;
        IF proof_row.id IS NULL
           OR answer_result_type_value<>'workspace_analysis'
           OR answer_result_hash_value IS DISTINCT FROM proof_row.published_result_hash
           OR answer_result_value IS DISTINCT FROM convert_from(proof_row.published_document,'UTF8')::jsonb
           OR analysis_validation_receipt_id IS DISTINCT FROM proof_row.validation_receipt_id
           OR analysis_review_model_run_id IS DISTINCT FROM proof_review_model_run_id
           OR termination_proof_row.id IS NOT NULL THEN
            RAISE EXCEPTION 'workspace analysis completed publication proof is not closed' USING ERRCODE='55000';
        END IF;
    ELSIF proof_row.id IS NOT NULL THEN
        RAISE EXCEPTION 'non-success workspace analysis cannot retain a success publication proof' USING ERRCODE='55000';
    ELSIF analysis_status_value IN ('refused','clarification_required','failed','cancelled') THEN
        IF termination_proof_row.id IS NULL
           OR termination_proof_row.reason IS DISTINCT FROM analysis_reason_value
           OR answer_result_hash_value IS DISTINCT FROM termination_proof_row.published_result_hash
           OR answer_result_value IS DISTINCT FROM convert_from(termination_proof_row.published_document,'UTF8')::jsonb
           OR answer_model_run_id_value IS DISTINCT FROM termination_proof_row.published_model_run_id THEN
            RAISE EXCEPTION 'workspace analysis termination proof is not closed' USING ERRCODE='55000';
        END IF;
    ELSIF termination_proof_row.id IS NOT NULL THEN
        RAISE EXCEPTION 'active workspace analysis cannot retain a termination proof' USING ERRCODE='55000';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_publication_from_answer()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM agent.verify_workspace_analysis_publication_closure(NEW.id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_publication_from_run()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM agent.verify_workspace_analysis_publication_closure(NEW.answer_id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_publication_from_proof()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM agent.verify_workspace_analysis_publication_closure(NEW.answer_id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_publication_from_termination_proof()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM agent.verify_workspace_analysis_publication_closure(NEW.answer_id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_run_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    question_mode text;
    answer_status text;
    answer_question_id uuid;
    answer_workflow_run_id uuid;
    definition_key_value text;
    definition_version_value bigint;
    active_operation_count bigint;
    active_reservation_count bigint;
    candidate_row agent.workspace_analysis_candidate%ROWTYPE;
    review_model_result_row agent.workspace_analysis_model_result%ROWTYPE;
    receipt_row workflow.tool_result_receipt%ROWTYPE;
    validation_json jsonb;
    validation_binding_json jsonb;
    review_json jsonb;
    review_status text;
    review_result_type text;
    git_operation_count bigint;
    plan_operation_count bigint;
    search_operation_count bigint;
    source_operation_count bigint;
    synthesis_operation_count bigint;
    validation_operation_count bigint;
    review_operation_count bigint;
    invalid_operation_count bigint;
    model_call_count bigint;
    tool_call_count bigint;
    source_min_ordinal integer;
    source_max_ordinal integer;
    synthesis_operation_id_value uuid;
    validation_operation_receipt_id uuid;
    review_operation_id_value uuid;
    review_operation_result_id uuid;
    review_operation_model_run_id uuid;
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'workspace analysis run is append-only' USING ERRCODE='55000';
    END IF;

    IF TG_OP='INSERT' THEN
        IF NEW.status<>'queued'
           OR NEW.version<>1
           OR NEW.reserved_model_calls<>0
           OR NEW.reserved_tool_calls<>0
           OR NEW.reserved_source_reads<>0
           OR NEW.reserved_input_tokens<>0
           OR NEW.reserved_output_tokens<>0
           OR COALESCE(NEW.reserved_cost_microunits,0)<>0
           OR NEW.settled_model_calls<>0
           OR NEW.settled_tool_calls<>0
           OR NEW.settled_source_reads<>0
           OR NEW.settled_input_tokens<>0
           OR NEW.settled_output_tokens<>0
           OR COALESCE(NEW.settled_cost_microunits,0)<>0 THEN
            RAISE EXCEPTION 'workspace analysis run must start queued with an empty budget' USING ERRCODE='23514';
        END IF;
        SELECT mode INTO question_mode
          FROM agent.question
         WHERE id=NEW.question_id
           AND conversation_id=NEW.conversation_id
           AND workspace_id=NEW.workspace_id
         FOR SHARE;
        SELECT publication_status,question_id,workflow_run_id
          INTO answer_status,answer_question_id,answer_workflow_run_id
          FROM agent.answer
         WHERE id=NEW.answer_id AND workspace_id=NEW.workspace_id
         FOR SHARE;
        SELECT definition.key,definition.version
          INTO definition_key_value,definition_version_value
          FROM workflow.run AS run
          JOIN workflow.definition AS definition ON definition.id=run.definition_id
         WHERE run.id=NEW.workflow_run_id AND run.workspace_id=NEW.workspace_id
         FOR SHARE OF run,definition;
        IF question_mode IS DISTINCT FROM 'workspace_analysis'
           OR answer_status IS DISTINCT FROM 'pending'
           OR answer_question_id IS DISTINCT FROM NEW.question_id
           OR answer_workflow_run_id IS DISTINCT FROM NEW.workflow_run_id
           OR definition_key_value IS DISTINCT FROM NEW.definition_key
           OR definition_version_value IS DISTINCT FROM NEW.definition_version THEN
            RAISE EXCEPTION 'workspace analysis run dispatch binding is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.status NOT IN ('queued','running')
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
       OR NEW.question_id IS DISTINCT FROM OLD.question_id
       OR NEW.answer_id IS DISTINCT FROM OLD.answer_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.definition_key IS DISTINCT FROM OLD.definition_key
       OR NEW.definition_version IS DISTINCT FROM OLD.definition_version
       OR NEW.definition_hash IS DISTINCT FROM OLD.definition_hash
       OR NEW.tool_catalog_hash IS DISTINCT FROM OLD.tool_catalog_hash
       OR NEW.policy_version IS DISTINCT FROM OLD.policy_version
       OR NEW.config_revision IS DISTINCT FROM OLD.config_revision
       OR NEW.deadline_at IS DISTINCT FROM OLD.deadline_at
       OR NEW.plan_model_timeout_ms IS DISTINCT FROM OLD.plan_model_timeout_ms
       OR NEW.synthesis_model_timeout_ms IS DISTINCT FROM OLD.synthesis_model_timeout_ms
       OR NEW.review_model_timeout_ms IS DISTINCT FROM OLD.review_model_timeout_ms
       OR NEW.git_tool_timeout_ms IS DISTINCT FROM OLD.git_tool_timeout_ms
       OR NEW.search_tool_timeout_ms IS DISTINCT FROM OLD.search_tool_timeout_ms
       OR NEW.source_read_tool_timeout_ms IS DISTINCT FROM OLD.source_read_tool_timeout_ms
       OR NEW.validate_citation_tool_timeout_ms IS DISTINCT FROM OLD.validate_citation_tool_timeout_ms
       OR NEW.durable_completion_margin_ms IS DISTINCT FROM OLD.durable_completion_margin_ms
       OR NEW.max_nodes IS DISTINCT FROM OLD.max_nodes
       OR NEW.max_model_calls IS DISTINCT FROM OLD.max_model_calls
       OR NEW.max_tool_calls IS DISTINCT FROM OLD.max_tool_calls
       OR NEW.max_source_reads IS DISTINCT FROM OLD.max_source_reads
       OR NEW.max_tool_concurrency IS DISTINCT FROM OLD.max_tool_concurrency
       OR NEW.max_input_tokens IS DISTINCT FROM OLD.max_input_tokens
       OR NEW.max_output_tokens IS DISTINCT FROM OLD.max_output_tokens
       OR NEW.max_cost_microunits IS DISTINCT FROM OLD.max_cost_microunits
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.version<>OLD.version+1
       OR NEW.updated_at<OLD.updated_at
       OR NEW.settled_model_calls<OLD.settled_model_calls
       OR NEW.settled_tool_calls<OLD.settled_tool_calls
       OR NEW.settled_source_reads<OLD.settled_source_reads
       OR NEW.settled_input_tokens<OLD.settled_input_tokens
       OR NEW.settled_output_tokens<OLD.settled_output_tokens
       OR COALESCE(NEW.settled_cost_microunits,0)<COALESCE(OLD.settled_cost_microunits,0) THEN
        RAISE EXCEPTION 'workspace analysis run immutable binding or counters changed' USING ERRCODE='55000';
    END IF;

    IF NOT (
        NEW.status=OLD.status
        OR (OLD.status='queued' AND NEW.status='running')
        OR (NEW.status IN ('succeeded','refused','clarification_required','failed','cancelled'))
    ) THEN
        RAISE EXCEPTION 'workspace analysis run transition is invalid' USING ERRCODE='55000';
    END IF;

    IF NEW.status IN ('succeeded','refused','clarification_required','failed','cancelled') THEN
        SELECT count(*) INTO active_operation_count
          FROM agent.workspace_analysis_operation AS operation
         WHERE operation.analysis_run_id=NEW.id
           AND (
                operation.status='STARTED'
                OR (
                    operation.status='PENDING'
                    AND NOT EXISTS (
                        SELECT 1
                          FROM agent.workspace_analysis_termination_proof AS proof
                         WHERE proof.analysis_run_id=NEW.id
                           AND proof.operation_id=operation.id
                           AND proof.reason IN (
                                'WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED',
                                'WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED'
                           )
                    )
                )
           );
        SELECT count(*) INTO active_reservation_count
          FROM agent.workspace_analysis_budget_reservation
         WHERE analysis_run_id=NEW.id AND status='RESERVED';
        IF active_operation_count<>0 OR active_reservation_count<>0 THEN
            RAISE EXCEPTION 'workspace analysis run cannot terminate with active work' USING ERRCODE='55000';
        END IF;
    END IF;

    IF NEW.status='succeeded' THEN
        SELECT
            count(*) FILTER (WHERE operation_kind='GIT_STATUS'),
            count(*) FILTER (WHERE operation_kind='RETRIEVAL_PLAN'),
            count(*) FILTER (WHERE operation_kind='KNOWLEDGE_SEARCH'),
            count(*) FILTER (WHERE operation_kind='SOURCE_READ'),
            count(*) FILTER (WHERE operation_kind='ANSWER_SYNTHESIS'),
            count(*) FILTER (WHERE operation_kind='CITATION_VALIDATION'),
            count(*) FILTER (WHERE operation_kind='FAITHFULNESS_REVIEW'),
            count(*) FILTER (WHERE status<>'SUCCEEDED'),
            min(ordinal) FILTER (WHERE operation_kind='SOURCE_READ'),
            max(ordinal) FILTER (WHERE operation_kind='SOURCE_READ')
          INTO git_operation_count,plan_operation_count,search_operation_count,
               source_operation_count,synthesis_operation_count,validation_operation_count,
               review_operation_count,invalid_operation_count,source_min_ordinal,source_max_ordinal
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=NEW.id;
        SELECT id INTO synthesis_operation_id_value
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=NEW.id
           AND operation_kind='ANSWER_SYNTHESIS'
           AND status='SUCCEEDED';
        SELECT result_id INTO validation_operation_receipt_id
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=NEW.id
           AND operation_kind='CITATION_VALIDATION'
           AND status='SUCCEEDED';
        SELECT operation.id,operation.result_id,model_call.model_run_id
          INTO review_operation_id_value,review_operation_result_id,review_operation_model_run_id
          FROM agent.workspace_analysis_operation AS operation
          JOIN agent.model_call AS model_call ON model_call.id=operation.model_call_id
         WHERE operation.analysis_run_id=NEW.id
           AND operation.operation_kind='FAITHFULNESS_REVIEW'
           AND operation.status='SUCCEEDED';
        SELECT * INTO candidate_row
          FROM agent.workspace_analysis_candidate
         WHERE analysis_run_id=NEW.id AND answer_id=NEW.answer_id
         FOR SHARE;
        SELECT * INTO review_model_result_row
          FROM agent.workspace_analysis_model_result
         WHERE id=review_operation_result_id
           AND workspace_id=NEW.workspace_id
           AND analysis_run_id=NEW.id
           AND operation_kind='FAITHFULNESS_REVIEW'
         FOR SHARE;
        SELECT * INTO receipt_row
          FROM workflow.tool_result_receipt
         WHERE id=NEW.validation_receipt_id
           AND workspace_id=NEW.workspace_id
           AND workflow_run_id=NEW.workflow_run_id
         FOR SHARE;
        SELECT status,final_result_type INTO review_status,review_result_type
          FROM agent.model_run
         WHERE id=NEW.review_model_run_id
           AND workspace_id=NEW.workspace_id
           AND workflow_run_id=NEW.workflow_run_id
         FOR SHARE;
        SELECT count(*) INTO model_call_count
          FROM agent.model_call AS model_call
          JOIN agent.model_run AS model_run ON model_run.id=model_call.model_run_id
         WHERE model_run.workspace_id=NEW.workspace_id
           AND model_run.workflow_run_id=NEW.workflow_run_id;
        SELECT count(*) INTO tool_call_count
          FROM workflow.tool_call
         WHERE workspace_id=NEW.workspace_id
           AND workflow_run_id=NEW.workflow_run_id;
        IF candidate_row.id IS NULL
           OR receipt_row.id IS NULL
           OR receipt_row.tool_name<>'ValidateCitation'
           OR receipt_row.tool_version<>3
           OR candidate_row.synthesis_operation_id IS DISTINCT FROM synthesis_operation_id_value
           OR validation_operation_receipt_id IS DISTINCT FROM NEW.validation_receipt_id
           OR review_model_result_row.id IS NULL
           OR review_model_result_row.operation_id IS DISTINCT FROM review_operation_id_value
           OR review_model_result_row.model_run_id IS DISTINCT FROM NEW.review_model_run_id
           OR review_model_result_row.subject_candidate_id IS DISTINCT FROM candidate_row.id
           OR review_model_result_row.subject_candidate_hash IS DISTINCT FROM candidate_row.document_hash
           OR review_operation_model_run_id IS DISTINCT FROM NEW.review_model_run_id
           OR review_status IS DISTINCT FROM 'SUCCEEDED'
           OR review_result_type IS DISTINCT FROM 'faithfulness_review'
           OR git_operation_count<>1
           OR plan_operation_count<>1
           OR search_operation_count<>1
           OR source_operation_count NOT BETWEEN 1 AND 3
           OR synthesis_operation_count<>1
           OR validation_operation_count<>1
           OR review_operation_count<>1
           OR invalid_operation_count<>0
           OR model_call_count<>3
           OR tool_call_count<>source_operation_count+3
           OR source_min_ordinal<>1
           OR source_max_ordinal<>source_operation_count
           OR NEW.reserved_model_calls<>0
           OR NEW.reserved_tool_calls<>0
           OR NEW.reserved_source_reads<>0
           OR NEW.reserved_input_tokens<>0
           OR NEW.reserved_output_tokens<>0
           OR COALESCE(NEW.reserved_cost_microunits,0)<>0
           OR NEW.settled_model_calls<>3
           OR NEW.settled_tool_calls<>source_operation_count+3
           OR NEW.settled_source_reads<>source_operation_count THEN
            RAISE EXCEPTION 'workspace analysis success proof is incomplete' USING ERRCODE='55000';
        END IF;
        validation_json := convert_from(receipt_row.output_document,'UTF8')::jsonb;
        validation_binding_json := convert_from(receipt_row.server_binding_document,'UTF8')::jsonb;
        review_json := convert_from(review_model_result_row.document,'UTF8')::jsonb;
        IF jsonb_typeof(validation_json->'results') IS DISTINCT FROM 'array'
           OR jsonb_array_length(validation_json->'results')=0
           OR validation_binding_json->>'candidate_id' IS DISTINCT FROM candidate_row.id::text
           OR validation_binding_json->>'candidate_hash' IS DISTINCT FROM candidate_row.document_hash
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(validation_json->'results') AS result(value)
                 WHERE result.value->'valid' IS DISTINCT FROM 'true'::jsonb
           ) THEN
            RAISE EXCEPTION 'workspace analysis citation validation is not all-valid' USING ERRCODE='55000';
        END IF;
        IF review_json->'payload'->'passed' IS DISTINCT FROM 'true'::jsonb THEN
            RAISE EXCEPTION 'workspace analysis faithfulness review did not pass' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agent.guard_answer_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    question_mode text;
    model_status text;
    model_result_type text;
    model_workflow_run_id uuid;
    analysis_run_id_value uuid;
    analysis_status text;
    analysis_reason text;
    refusal_reason text;
    termination_reason_value text;
    candidate_model_run_id uuid;
    planner_model_run_id uuid;
    planner_result_json jsonb;
    termination_proof_row agent.workspace_analysis_termination_proof%ROWTYPE;
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'agent answer is append-only' USING ERRCODE='55000';
    END IF;
    IF TG_OP='INSERT' THEN
        IF NEW.publication_status<>'pending'
           OR NEW.version<>1
           OR NEW.model_run_id IS NOT NULL
           OR NEW.result_type IS NOT NULL
           OR NEW.result IS NOT NULL
           OR NEW.result_hash IS NOT NULL
           OR NEW.retrieval_summary IS NOT NULL
           OR NEW.published_at IS NOT NULL THEN
            RAISE EXCEPTION 'agent answer must be inserted pending at version one' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.publication_status<>'pending'
       OR NEW.publication_status='pending'
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
       OR NEW.question_id IS DISTINCT FROM OLD.question_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.version<>OLD.version+1
       OR NEW.updated_at<OLD.updated_at
       OR NEW.result_type IS NULL
       OR NEW.result IS NULL
       OR NEW.result_hash IS NULL
       OR NEW.published_at IS NULL
       OR NEW.updated_at<>NEW.published_at THEN
        RAISE EXCEPTION 'agent answer transition or immutable binding is invalid' USING ERRCODE='55000';
    END IF;

    SELECT mode INTO question_mode
      FROM agent.question
     WHERE id=NEW.question_id
       AND workspace_id=NEW.workspace_id
       AND conversation_id=NEW.conversation_id
     FOR SHARE;
    IF question_mode IS NULL THEN
        RAISE EXCEPTION 'agent answer question mode binding is missing' USING ERRCODE='55000';
    END IF;

    IF question_mode='rag' THEN
        IF NEW.model_run_id IS NULL OR NEW.retrieval_summary IS NULL THEN
            RAISE EXCEPTION 'rag answer publication bundle is incomplete' USING ERRCODE='55000';
        END IF;
        SELECT status,final_result_type,workflow_run_id
          INTO model_status,model_result_type,model_workflow_run_id
          FROM agent.model_run
         WHERE id=NEW.model_run_id
           AND workspace_id=NEW.workspace_id
         FOR SHARE;
        IF model_workflow_run_id IS NULL
           OR model_workflow_run_id<>NEW.workflow_run_id
           OR NEW.result->>'result_type' IS DISTINCT FROM NEW.result_type
           OR NEW.result->>'model_run_ref' IS DISTINCT FROM NEW.model_run_id::text
           OR (
                NEW.publication_status='completed'
                AND (
                    model_status<>'SUCCEEDED'
                    OR model_result_type<>'rag_answer'
                    OR NEW.result_type<>'rag_answer'
                    OR NEW.result->>'schema_id' IS DISTINCT FROM 'agent.rag-answer'
                    OR NEW.result->>'schema_version' IS DISTINCT FROM 'v2'
                )
           )
           OR (
                NEW.publication_status='refused'
                AND (
                    model_status<>'REFUSED'
                    OR model_result_type<>'refusal'
                    OR NEW.result_type<>'refusal'
                    OR NEW.result->>'schema_id' IS DISTINCT FROM 'agent.refusal'
                    OR NEW.result->>'schema_version' IS DISTINCT FROM 'v1'
                )
           )
           OR (
                NEW.publication_status='clarification_required'
                AND (
                    model_status<>'SUCCEEDED'
                    OR model_result_type<>'clarification'
                    OR NEW.result_type<>'clarification'
                    OR NEW.result->>'schema_id' IS DISTINCT FROM 'conversation.clarification'
                    OR NEW.result->>'schema_version' IS DISTINCT FROM 'v1'
                )
           ) THEN
            RAISE EXCEPTION 'agent answer model result binding is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;

    IF question_mode<>'workspace_analysis'
       OR NEW.retrieval_summary IS NOT NULL
       OR NOT (NEW.result ? 'model_run_ref')
       OR NEW.result->>'result_type' IS DISTINCT FROM NEW.result_type
       OR NEW.result->>'schema_version' IS DISTINCT FROM 'v1'
       OR (
            NEW.model_run_id IS NULL
            AND NEW.result->'model_run_ref' IS DISTINCT FROM 'null'::jsonb
       )
       OR (
            NEW.model_run_id IS NOT NULL
            AND NEW.result->>'model_run_ref' IS DISTINCT FROM NEW.model_run_id::text
       ) THEN
        RAISE EXCEPTION 'workspace analysis answer envelope binding is invalid' USING ERRCODE='55000';
    END IF;

    IF NEW.model_run_id IS NOT NULL THEN
        SELECT status,final_result_type,workflow_run_id
          INTO model_status,model_result_type,model_workflow_run_id
          FROM agent.model_run
         WHERE id=NEW.model_run_id
           AND workspace_id=NEW.workspace_id
         FOR SHARE;
        IF model_workflow_run_id IS NULL
           OR model_workflow_run_id<>NEW.workflow_run_id
           OR model_status='RUNNING' THEN
            RAISE EXCEPTION 'workspace analysis answer model binding is invalid' USING ERRCODE='55000';
        END IF;
    END IF;

    SELECT id,status,termination_reason
      INTO analysis_run_id_value,analysis_status,analysis_reason
      FROM agent.workspace_analysis_run
     WHERE workspace_id=NEW.workspace_id
       AND answer_id=NEW.id
       AND question_id=NEW.question_id
       AND workflow_run_id=NEW.workflow_run_id
     FOR SHARE;
    IF analysis_run_id_value IS NULL THEN
        RAISE EXCEPTION 'workspace analysis answer has no analysis run' USING ERRCODE='55000';
    END IF;

    IF NEW.publication_status<>'completed' THEN
        SELECT * INTO termination_proof_row
          FROM agent.workspace_analysis_termination_proof
         WHERE analysis_run_id=analysis_run_id_value
           AND answer_id=NEW.id
         FOR SHARE;
        IF termination_proof_row.id IS NULL
           OR NEW.result_hash IS DISTINCT FROM termination_proof_row.published_result_hash
           OR NEW.result IS DISTINCT FROM convert_from(termination_proof_row.published_document,'UTF8')::jsonb
           OR NEW.model_run_id IS DISTINCT FROM termination_proof_row.published_model_run_id THEN
            RAISE EXCEPTION 'workspace analysis terminal answer does not match its publication proof' USING ERRCODE='55000';
        END IF;
    END IF;

    IF NEW.publication_status='completed' THEN
        SELECT synthesis_model_run_id INTO candidate_model_run_id
          FROM agent.workspace_analysis_candidate
         WHERE analysis_run_id=analysis_run_id_value AND answer_id=NEW.id
         FOR SHARE;
        IF NEW.result_type<>'workspace_analysis'
           OR NEW.result->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_answer'
           OR NEW.model_run_id IS NULL
           OR model_status<>'SUCCEEDED'
           OR model_result_type<>'workspace_analysis_answer'
           OR candidate_model_run_id IS DISTINCT FROM NEW.model_run_id
           OR analysis_status IS DISTINCT FROM 'succeeded'
           OR analysis_reason IS DISTINCT FROM 'COMPLETED' THEN
            RAISE EXCEPTION 'workspace analysis completed answer proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.publication_status='refused' THEN
        refusal_reason := NEW.result->'payload'->>'reason_code';
        IF NEW.result_type<>'workspace_analysis_refusal'
           OR NEW.result->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_refusal'
           OR refusal_reason NOT IN (
                'WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT',
                'WORKSPACE_ANALYSIS_CITATION_INVALID',
                'WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED',
                'WORKSPACE_ANALYSIS_MODEL_REFUSED'
           )
           OR analysis_status IS DISTINCT FROM 'refused'
           OR analysis_reason IS DISTINCT FROM refusal_reason
           OR (
                refusal_reason='WORKSPACE_ANALYSIS_MODEL_REFUSED'
                AND (
                    NEW.model_run_id IS NULL
                    OR model_status<>'REFUSED'
                    OR model_result_type<>'refusal'
                    OR NOT EXISTS (
                        SELECT 1
                          FROM agent.workspace_analysis_operation AS operation
                          JOIN agent.model_call AS model_call
                            ON model_call.id=operation.model_call_id
                           AND model_call.model_run_id=NEW.model_run_id
                         WHERE operation.analysis_run_id=analysis_run_id_value
                           AND operation.call_kind='MODEL'
                           AND operation.status='FAILED'
                           AND operation.error_code='WORKSPACE_ANALYSIS_MODEL_REFUSED'
                           AND model_call.status='SUCCEEDED'
                    )
                )
           )
           OR (
                refusal_reason<>'WORKSPACE_ANALYSIS_MODEL_REFUSED'
                AND NEW.model_run_id IS NOT NULL
           ) THEN
            RAISE EXCEPTION 'workspace analysis refusal proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.publication_status='clarification_required' THEN
        SELECT model_result.model_run_id,convert_from(model_result.document,'UTF8')::jsonb
          INTO planner_model_run_id,planner_result_json
          FROM agent.workspace_analysis_operation AS operation
          JOIN agent.workspace_analysis_model_result AS model_result
            ON model_result.id=operation.result_id
           AND model_result.operation_id=operation.id
           AND model_result.analysis_run_id=operation.analysis_run_id
         WHERE operation.analysis_run_id=analysis_run_id_value
           AND operation.operation_kind='RETRIEVAL_PLAN'
           AND operation.status='SUCCEEDED'
           AND operation.result_kind='MODEL_RESULT_RECEIPT'
         FOR SHARE OF operation,model_result;
        IF NEW.result_type<>'clarification'
           OR NEW.result->>'schema_id' IS DISTINCT FROM 'conversation.clarification'
           OR NEW.model_run_id IS NULL
           OR model_status<>'SUCCEEDED'
           OR model_result_type<>'workspace_analysis_plan'
           OR planner_model_run_id IS DISTINCT FROM NEW.model_run_id
           OR planner_result_json->'payload'->'requires_clarification' IS DISTINCT FROM 'true'::jsonb
           OR NEW.result->'payload'->>'reason' IS DISTINCT FROM planner_result_json->'payload'->>'clarification_reason'
           OR NEW.result->'payload'->>'question' IS DISTINCT FROM planner_result_json->'payload'->>'clarification_question'
           OR NEW.result->'payload'->'suggested_scopes' IS DISTINCT FROM planner_result_json->'payload'->'suggested_scopes'
           OR analysis_status IS DISTINCT FROM 'clarification_required'
           OR analysis_reason IS DISTINCT FROM 'WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED' THEN
            RAISE EXCEPTION 'workspace analysis clarification proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.publication_status IN ('failed','cancelled') THEN
        termination_reason_value := NEW.result->'payload'->>'termination_reason';
        IF NEW.result_type<>'workspace_analysis_termination'
           OR NEW.result->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_termination'
           OR analysis_status IS DISTINCT FROM NEW.publication_status
           OR analysis_reason IS DISTINCT FROM termination_reason_value
           OR (
                NEW.publication_status='cancelled'
                AND termination_reason_value<>'WORKSPACE_ANALYSIS_CANCELLED'
           )
           OR (
                NEW.publication_status='failed'
                AND termination_reason_value NOT IN (
                    'WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED',
                    'WORKSPACE_ANALYSIS_RECEIPT_INVALID',
                    'WORKSPACE_ANALYSIS_RESULT_UNKNOWN',
                    'WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED',
                    'WORKSPACE_ANALYSIS_MODEL_FAILED',
                    'WORKSPACE_ANALYSIS_TOOL_FAILED'
                )
           ) THEN
            RAISE EXCEPTION 'workspace analysis termination proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSE
        RAISE EXCEPTION 'workspace analysis publication status is invalid' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS agent_answer_guard_mutation ON agent.answer;
CREATE TRIGGER agent_answer_guard_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON agent.answer
    FOR EACH ROW EXECUTE FUNCTION agent.guard_answer_mutation();

CREATE TRIGGER workspace_analysis_run_guard_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON agent.workspace_analysis_run
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_run_mutation();
CREATE TRIGGER workspace_analysis_run_reject_truncate
    BEFORE TRUNCATE ON agent.workspace_analysis_run
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();

CREATE TRIGGER workspace_analysis_operation_guard_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON agent.workspace_analysis_operation
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_operation_mutation();
CREATE TRIGGER workspace_analysis_operation_reject_truncate
    BEFORE TRUNCATE ON agent.workspace_analysis_operation
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();

CREATE TRIGGER workspace_analysis_reservation_guard_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON agent.workspace_analysis_budget_reservation
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_reservation_mutation();
CREATE TRIGGER workspace_analysis_reservation_reject_truncate
    BEFORE TRUNCATE ON agent.workspace_analysis_budget_reservation
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();

CREATE TRIGGER workspace_analysis_candidate_guard_insert
    BEFORE INSERT ON agent.workspace_analysis_candidate
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_candidate_insert();
CREATE TRIGGER workspace_analysis_candidate_append_only
    BEFORE UPDATE OR DELETE ON agent.workspace_analysis_candidate
    FOR EACH ROW EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();
CREATE TRIGGER workspace_analysis_candidate_reject_truncate
    BEFORE TRUNCATE ON agent.workspace_analysis_candidate
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();

CREATE TRIGGER workspace_analysis_model_result_guard_insert
    BEFORE INSERT ON agent.workspace_analysis_model_result
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_model_result_insert();
CREATE TRIGGER workspace_analysis_model_result_append_only
    BEFORE UPDATE OR DELETE ON agent.workspace_analysis_model_result
    FOR EACH ROW EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();
CREATE TRIGGER workspace_analysis_model_result_reject_truncate
    BEFORE TRUNCATE ON agent.workspace_analysis_model_result
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();

CREATE TRIGGER workspace_analysis_publication_proof_guard_insert
    BEFORE INSERT ON agent.workspace_analysis_publication_proof
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_publication_proof_insert();
CREATE TRIGGER workspace_analysis_publication_proof_append_only
    BEFORE UPDATE OR DELETE ON agent.workspace_analysis_publication_proof
    FOR EACH ROW EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();
CREATE TRIGGER workspace_analysis_publication_proof_reject_truncate
    BEFORE TRUNCATE ON agent.workspace_analysis_publication_proof
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();

CREATE TRIGGER workspace_analysis_termination_proof_guard_insert
    BEFORE INSERT ON agent.workspace_analysis_termination_proof
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_termination_proof_insert();
CREATE TRIGGER workspace_analysis_termination_proof_append_only
    BEFORE UPDATE OR DELETE ON agent.workspace_analysis_termination_proof
    FOR EACH ROW EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();
CREATE TRIGGER workspace_analysis_termination_proof_reject_truncate
    BEFORE TRUNCATE ON agent.workspace_analysis_termination_proof
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();

CREATE TRIGGER tool_result_receipt_guard_insert
    BEFORE INSERT ON workflow.tool_result_receipt
    FOR EACH ROW EXECUTE FUNCTION workflow.guard_tool_result_receipt_insert();
CREATE TRIGGER tool_result_receipt_append_only
    BEFORE UPDATE OR DELETE ON workflow.tool_result_receipt
    FOR EACH ROW EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();
CREATE TRIGGER tool_result_receipt_reject_truncate
    BEFORE TRUNCATE ON workflow.tool_result_receipt
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();

CREATE TRIGGER tool_result_receipt_failure_guard_insert
    BEFORE INSERT ON workflow.tool_result_receipt_failure
    FOR EACH ROW EXECUTE FUNCTION workflow.guard_tool_result_receipt_failure_insert();
CREATE TRIGGER tool_result_receipt_failure_append_only
    BEFORE UPDATE OR DELETE ON workflow.tool_result_receipt_failure
    FOR EACH ROW EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();
CREATE TRIGGER tool_result_receipt_failure_reject_truncate
    BEFORE TRUNCATE ON workflow.tool_result_receipt_failure
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();

CREATE CONSTRAINT TRIGGER workspace_analysis_model_result_operation_closure
    AFTER INSERT ON agent.workspace_analysis_model_result
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_model_result_closure();

CREATE CONSTRAINT TRIGGER workspace_analysis_candidate_operation_closure
    AFTER INSERT ON agent.workspace_analysis_candidate
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_candidate_closure();

CREATE CONSTRAINT TRIGGER workspace_analysis_tool_receipt_operation_closure
    AFTER INSERT ON workflow.tool_result_receipt
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION workflow.guard_workspace_analysis_tool_receipt_closure();

CREATE CONSTRAINT TRIGGER workspace_analysis_tool_receipt_failure_operation_closure
    AFTER INSERT ON workflow.tool_result_receipt_failure
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION workflow.guard_workspace_analysis_tool_receipt_closure();

CREATE CONSTRAINT TRIGGER workspace_analysis_publication_proof_closure
    AFTER INSERT ON agent.workspace_analysis_publication_proof
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_publication_from_proof();

CREATE CONSTRAINT TRIGGER workspace_analysis_termination_proof_closure
    AFTER INSERT ON agent.workspace_analysis_termination_proof
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_publication_from_termination_proof();

CREATE CONSTRAINT TRIGGER workspace_analysis_reservation_budget_totals
    AFTER INSERT OR UPDATE ON agent.workspace_analysis_budget_reservation
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_budget_totals();

CREATE CONSTRAINT TRIGGER workspace_analysis_run_budget_totals
    AFTER INSERT OR UPDATE ON agent.workspace_analysis_run
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_run_budget_totals();

CREATE CONSTRAINT TRIGGER workspace_analysis_operation_reservation_closure
    AFTER INSERT OR UPDATE ON agent.workspace_analysis_operation
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_operation_closure();

CREATE CONSTRAINT TRIGGER workspace_analysis_reservation_operation_closure
    AFTER INSERT OR UPDATE ON agent.workspace_analysis_budget_reservation
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_reservation_closure();

CREATE CONSTRAINT TRIGGER workspace_analysis_answer_publication_closure
    AFTER INSERT OR UPDATE ON agent.answer
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_publication_from_answer();

CREATE CONSTRAINT TRIGGER workspace_analysis_run_publication_closure
    AFTER INSERT OR UPDATE ON agent.workspace_analysis_run
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_publication_from_run();

CREATE CONSTRAINT TRIGGER workspace_analysis_model_call_operation_closure
    AFTER INSERT OR UPDATE ON agent.model_call
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_model_call_closure();

CREATE CONSTRAINT TRIGGER workspace_analysis_tool_call_operation_closure
    AFTER INSERT OR UPDATE ON workflow.tool_call
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_tool_call_closure();

INSERT INTO core.schema_meta(key,value)
VALUES ('workspace_analysis_persistence','workspace-analysis-persistence-v1')
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();

-- +goose Down

LOCK TABLE agent.question,
           agent.answer,
           agent.model_run,
           agent.model_call,
           agent.workspace_analysis_termination_proof,
           agent.workspace_analysis_publication_proof,
           agent.workspace_analysis_model_result,
           agent.workspace_analysis_candidate,
           agent.workspace_analysis_budget_reservation,
           agent.workspace_analysis_operation,
           agent.workspace_analysis_run,
           workflow.tool_result_receipt_failure,
           workflow.tool_result_receipt,
           workflow.tool_call
    IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent.question WHERE mode<>'rag' LIMIT 1)
       OR EXISTS (
            SELECT 1 FROM agent.answer
             WHERE publication_status IN ('failed','cancelled')
                OR result_type IN (
                    'workspace_analysis','workspace_analysis_refusal','workspace_analysis_termination'
                )
            LIMIT 1
       )
       OR EXISTS (
            SELECT 1 FROM agent.model_run
             WHERE final_result_type IN ('workspace_analysis_plan','workspace_analysis_answer')
            LIMIT 1
       )
       OR EXISTS (
            SELECT 1 FROM agent.model_call
             WHERE phase='REVIEW' AND call_no=1
            LIMIT 1
       )
       OR EXISTS (SELECT 1 FROM agent.workspace_analysis_model_result LIMIT 1)
       OR EXISTS (SELECT 1 FROM agent.workspace_analysis_publication_proof LIMIT 1)
       OR EXISTS (SELECT 1 FROM agent.workspace_analysis_termination_proof LIMIT 1)
       OR EXISTS (SELECT 1 FROM agent.workspace_analysis_candidate LIMIT 1)
       OR EXISTS (SELECT 1 FROM agent.workspace_analysis_budget_reservation LIMIT 1)
       OR EXISTS (SELECT 1 FROM agent.workspace_analysis_operation LIMIT 1)
       OR EXISTS (SELECT 1 FROM agent.workspace_analysis_run LIMIT 1)
       OR EXISTS (SELECT 1 FROM workflow.tool_result_receipt_failure LIMIT 1)
       OR EXISTS (SELECT 1 FROM workflow.tool_result_receipt LIMIT 1) THEN
        RAISE EXCEPTION 'workspace analysis facts exist; rollback is unsafe' USING ERRCODE='55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS workspace_analysis_reservation_budget_totals
    ON agent.workspace_analysis_budget_reservation;
DROP TRIGGER IF EXISTS workspace_analysis_run_budget_totals
    ON agent.workspace_analysis_run;
DROP TRIGGER IF EXISTS workspace_analysis_operation_reservation_closure
    ON agent.workspace_analysis_operation;
DROP TRIGGER IF EXISTS workspace_analysis_reservation_operation_closure
    ON agent.workspace_analysis_budget_reservation;
DROP TRIGGER IF EXISTS workspace_analysis_answer_publication_closure
    ON agent.answer;
DROP TRIGGER IF EXISTS workspace_analysis_run_publication_closure
    ON agent.workspace_analysis_run;
DROP TRIGGER IF EXISTS workspace_analysis_publication_proof_closure
    ON agent.workspace_analysis_publication_proof;
DROP TRIGGER IF EXISTS workspace_analysis_termination_proof_closure
    ON agent.workspace_analysis_termination_proof;
DROP TRIGGER IF EXISTS workspace_analysis_model_call_operation_closure
    ON agent.model_call;
DROP TRIGGER IF EXISTS workspace_analysis_tool_call_operation_closure
    ON workflow.tool_call;
DROP TRIGGER IF EXISTS workspace_analysis_model_result_operation_closure
    ON agent.workspace_analysis_model_result;
DROP TRIGGER IF EXISTS workspace_analysis_candidate_operation_closure
    ON agent.workspace_analysis_candidate;
DROP TRIGGER IF EXISTS workspace_analysis_tool_receipt_operation_closure
    ON workflow.tool_result_receipt;
DROP TRIGGER IF EXISTS workspace_analysis_tool_receipt_failure_operation_closure
    ON workflow.tool_result_receipt_failure;
DROP TRIGGER IF EXISTS workspace_analysis_run_reject_truncate
    ON agent.workspace_analysis_run;
DROP TRIGGER IF EXISTS workspace_analysis_operation_reject_truncate
    ON agent.workspace_analysis_operation;
DROP TRIGGER IF EXISTS workspace_analysis_reservation_reject_truncate
    ON agent.workspace_analysis_budget_reservation;
DROP TRIGGER IF EXISTS tool_result_receipt_reject_truncate
    ON workflow.tool_result_receipt;
DROP TRIGGER IF EXISTS tool_result_receipt_append_only
    ON workflow.tool_result_receipt;
DROP TRIGGER IF EXISTS tool_result_receipt_guard_insert
    ON workflow.tool_result_receipt;
DROP TRIGGER IF EXISTS tool_result_receipt_failure_reject_truncate
    ON workflow.tool_result_receipt_failure;
DROP TRIGGER IF EXISTS tool_result_receipt_failure_append_only
    ON workflow.tool_result_receipt_failure;
DROP TRIGGER IF EXISTS tool_result_receipt_failure_guard_insert
    ON workflow.tool_result_receipt_failure;
DROP TRIGGER IF EXISTS workspace_analysis_model_result_reject_truncate
    ON agent.workspace_analysis_model_result;
DROP TRIGGER IF EXISTS workspace_analysis_model_result_append_only
    ON agent.workspace_analysis_model_result;
DROP TRIGGER IF EXISTS workspace_analysis_model_result_guard_insert
    ON agent.workspace_analysis_model_result;
DROP TRIGGER IF EXISTS workspace_analysis_publication_proof_reject_truncate
    ON agent.workspace_analysis_publication_proof;
DROP TRIGGER IF EXISTS workspace_analysis_publication_proof_append_only
    ON agent.workspace_analysis_publication_proof;
DROP TRIGGER IF EXISTS workspace_analysis_publication_proof_guard_insert
    ON agent.workspace_analysis_publication_proof;
DROP TRIGGER IF EXISTS workspace_analysis_termination_proof_reject_truncate
    ON agent.workspace_analysis_termination_proof;
DROP TRIGGER IF EXISTS workspace_analysis_termination_proof_append_only
    ON agent.workspace_analysis_termination_proof;
DROP TRIGGER IF EXISTS workspace_analysis_termination_proof_guard_insert
    ON agent.workspace_analysis_termination_proof;
DROP TRIGGER IF EXISTS workspace_analysis_candidate_reject_truncate
    ON agent.workspace_analysis_candidate;
DROP TRIGGER IF EXISTS workspace_analysis_candidate_append_only
    ON agent.workspace_analysis_candidate;
DROP TRIGGER IF EXISTS workspace_analysis_candidate_guard_insert
    ON agent.workspace_analysis_candidate;
DROP TRIGGER IF EXISTS workspace_analysis_reservation_guard_mutation
    ON agent.workspace_analysis_budget_reservation;
DROP TRIGGER IF EXISTS workspace_analysis_operation_guard_mutation
    ON agent.workspace_analysis_operation;
DROP TRIGGER IF EXISTS workspace_analysis_run_guard_mutation
    ON agent.workspace_analysis_run;

DROP TABLE agent.workspace_analysis_termination_proof;
DROP TABLE agent.workspace_analysis_publication_proof;
DROP TABLE agent.workspace_analysis_model_result;
DROP TABLE agent.workspace_analysis_candidate;
DROP TABLE agent.workspace_analysis_budget_reservation;
DROP TABLE workflow.tool_result_receipt_failure;
DROP TABLE agent.workspace_analysis_operation;
DROP TABLE agent.workspace_analysis_run;
DROP TABLE workflow.tool_result_receipt;

DROP FUNCTION agent.guard_workspace_analysis_run_mutation();
DROP FUNCTION agent.guard_workspace_analysis_publication_from_run();
DROP FUNCTION agent.guard_workspace_analysis_publication_from_answer();
DROP FUNCTION agent.guard_workspace_analysis_publication_from_proof();
DROP FUNCTION agent.guard_workspace_analysis_publication_from_termination_proof();
DROP FUNCTION agent.verify_workspace_analysis_publication_closure(uuid);
DROP FUNCTION agent.guard_workspace_analysis_tool_call_closure();
DROP FUNCTION agent.guard_workspace_analysis_model_call_closure();
DROP FUNCTION workflow.guard_workspace_analysis_tool_receipt_closure();
DROP FUNCTION agent.guard_workspace_analysis_candidate_closure();
DROP FUNCTION agent.guard_workspace_analysis_model_result_closure();
DROP FUNCTION agent.verify_workspace_analysis_tool_call_closure(uuid);
DROP FUNCTION agent.verify_workspace_analysis_model_call_closure(uuid);
DROP FUNCTION agent.guard_workspace_analysis_reservation_closure();
DROP FUNCTION agent.guard_workspace_analysis_operation_closure();
DROP FUNCTION agent.verify_workspace_analysis_operation_closure(uuid);
DROP FUNCTION agent.guard_workspace_analysis_run_budget_totals();
DROP FUNCTION agent.guard_workspace_analysis_budget_totals();
DROP FUNCTION agent.verify_workspace_analysis_budget_totals(uuid);
DROP FUNCTION agent.guard_workspace_analysis_reservation_mutation();
DROP FUNCTION agent.guard_workspace_analysis_operation_mutation();
DROP FUNCTION agent.is_workspace_analysis_operation_next(uuid);
DROP FUNCTION agent.workspace_analysis_validation_all_valid(uuid);
DROP FUNCTION agent.workspace_analysis_source_prefix_matches(uuid,jsonb,integer);
DROP FUNCTION agent.workspace_analysis_selected_refs(uuid);
DROP FUNCTION agent.guard_workspace_analysis_model_result_insert();
DROP FUNCTION agent.guard_workspace_analysis_publication_proof_insert();
DROP FUNCTION agent.guard_workspace_analysis_termination_proof_insert();
DROP FUNCTION agent.guard_workspace_analysis_candidate_insert();
DROP FUNCTION workflow.guard_tool_result_receipt_failure_insert();
DROP FUNCTION workflow.guard_tool_result_receipt_insert();
DROP FUNCTION workflow.workspace_analysis_receipt_identity_valid(jsonb,integer);
DROP FUNCTION workflow.workspace_analysis_receipt_text(text,integer);
DROP FUNCTION workflow.workspace_analysis_receipt_reference(text,integer);
DROP FUNCTION workflow.workspace_analysis_receipt_hash(text);
DROP FUNCTION workflow.workspace_analysis_receipt_canonical_uuid(text);
DROP FUNCTION workflow.workspace_analysis_receipt_canonical_json(json);
DROP FUNCTION workflow.workspace_analysis_receipt_json_string(text);
DROP FUNCTION workflow.workspace_analysis_receipt_exact_object_keys(jsonb,text[]);
DROP FUNCTION agent.reject_workspace_analysis_fact_mutation();

ALTER TABLE workflow.tool_call
    DROP CONSTRAINT IF EXISTS uq_workflow_tool_call_exact_identity;

ALTER TABLE agent.model_call
    DROP CONSTRAINT IF EXISTS agent_model_call_phase_order,
    ADD CONSTRAINT agent_model_call_phase_order CHECK (
        (phase = 'PLAN' AND call_no = 1)
        OR (phase = 'AGENT' AND call_no >= 1)
        OR (phase = 'ANSWER' AND call_no >= 1)
        OR (phase = 'INITIAL' AND call_no >= 1)
        OR (phase = 'REPAIR' AND call_no >= 2)
        OR (phase = 'REDUCED' AND call_no >= 3)
        OR (phase = 'REVIEW' AND call_no >= 2)
    );

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agent.guard_model_call_phase_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    previous_phase text;
    previous_status text;
BEGIN
    IF NEW.call_no = 1 THEN
        IF NEW.phase NOT IN ('PLAN', 'AGENT', 'ANSWER', 'INITIAL') THEN
            RAISE EXCEPTION 'agent model call first phase is invalid' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;

    SELECT phase, status
      INTO previous_phase, previous_status
      FROM agent.model_call
     WHERE model_run_id = NEW.model_run_id
       AND call_no = NEW.call_no - 1
     FOR SHARE;

    IF previous_status IS DISTINCT FROM 'SUCCEEDED'
       OR NOT (
            (previous_phase = 'PLAN' AND NEW.phase IN ('AGENT', 'ANSWER', 'INITIAL'))
            OR (previous_phase = 'AGENT' AND NEW.phase IN ('AGENT', 'ANSWER'))
            OR (previous_phase = 'ANSWER' AND NEW.phase = 'INITIAL')
            OR (previous_phase = 'INITIAL' AND NEW.phase IN ('REPAIR', 'REVIEW'))
            OR (previous_phase = 'REPAIR' AND NEW.phase IN ('REDUCED', 'REVIEW'))
            OR (previous_phase = 'REDUCED' AND NEW.phase = 'REVIEW')
       ) THEN
        RAISE EXCEPTION 'agent model call phase predecessor is invalid' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE agent.model_run
    DROP CONSTRAINT IF EXISTS agent_model_run_final_result_type_check,
    ADD CONSTRAINT agent_model_run_final_result_type_check CHECK (
        final_result_type IS NULL OR final_result_type IN (
            'relation_assessment','rag_answer','refusal','faithfulness_review',
            'tool_request','clarification','artifact_section','document_knowledge_profile',
            'organizing_outline','organizing_document'
        )
    );

ALTER TABLE agent.answer
    DROP CONSTRAINT IF EXISTS agent_answer_publication_status_check,
    ADD CONSTRAINT agent_answer_publication_status_check CHECK (
        publication_status IN ('pending','completed','refused','clarification_required')
    ),
    DROP CONSTRAINT IF EXISTS agent_answer_result_type_check,
    ADD CONSTRAINT agent_answer_result_type_check CHECK (
        result_type IS NULL OR result_type IN ('rag_answer','refusal','clarification')
    ),
    DROP CONSTRAINT IF EXISTS agent_answer_publication_bundle,
    ADD CONSTRAINT agent_answer_publication_bundle CHECK (
        (
            publication_status='pending'
            AND model_run_id IS NULL
            AND result_type IS NULL
            AND result IS NULL
            AND result_hash IS NULL
            AND retrieval_summary IS NULL
            AND published_at IS NULL
        )
        OR (
            publication_status='completed'
            AND model_run_id IS NOT NULL
            AND result_type='rag_answer'
            AND result IS NOT NULL
            AND result_hash IS NOT NULL
            AND retrieval_summary IS NOT NULL
            AND published_at IS NOT NULL
        )
        OR (
            publication_status='refused'
            AND model_run_id IS NOT NULL
            AND result_type='refusal'
            AND result IS NOT NULL
            AND result_hash IS NOT NULL
            AND retrieval_summary IS NOT NULL
            AND published_at IS NOT NULL
        )
        OR (
            publication_status='clarification_required'
            AND model_run_id IS NOT NULL
            AND result_type='clarification'
            AND result IS NOT NULL
            AND result_hash IS NOT NULL
            AND retrieval_summary IS NOT NULL
            AND published_at IS NOT NULL
        )
    );

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agent.guard_answer_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    model_status text;
    model_result_type text;
    model_workflow_run_id uuid;
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'agent answer is append-only' USING ERRCODE='55000';
    END IF;
    IF TG_OP='INSERT' THEN
        IF NEW.publication_status<>'pending'
           OR NEW.version<>1
           OR NEW.model_run_id IS NOT NULL
           OR NEW.result_type IS NOT NULL
           OR NEW.result IS NOT NULL
           OR NEW.result_hash IS NOT NULL
           OR NEW.retrieval_summary IS NOT NULL
           OR NEW.published_at IS NOT NULL THEN
            RAISE EXCEPTION 'agent answer must be inserted pending at version one' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.publication_status<>'pending'
       OR NEW.publication_status='pending'
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
       OR NEW.question_id IS DISTINCT FROM OLD.question_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.version<>OLD.version+1
       OR NEW.updated_at<OLD.updated_at
       OR NEW.model_run_id IS NULL
       OR NEW.result_type IS NULL
       OR NEW.result IS NULL
       OR NEW.result_hash IS NULL
       OR NEW.retrieval_summary IS NULL
       OR NEW.published_at IS NULL
       OR NEW.updated_at<>NEW.published_at THEN
        RAISE EXCEPTION 'agent answer transition or immutable binding is invalid' USING ERRCODE='55000';
    END IF;
    SELECT status,final_result_type,workflow_run_id
      INTO model_status,model_result_type,model_workflow_run_id
      FROM agent.model_run
     WHERE id=NEW.model_run_id
       AND workspace_id=NEW.workspace_id
     FOR SHARE;
    IF model_workflow_run_id IS NULL
       OR model_workflow_run_id<>NEW.workflow_run_id
       OR NEW.result->>'result_type' IS DISTINCT FROM NEW.result_type
       OR NEW.result->>'model_run_ref' IS DISTINCT FROM NEW.model_run_id::text
       OR (
            NEW.publication_status='completed'
            AND (
                model_status<>'SUCCEEDED'
                OR model_result_type<>'rag_answer'
                OR NEW.result_type<>'rag_answer'
                OR NEW.result->>'schema_id' IS DISTINCT FROM 'agent.rag-answer'
                OR NEW.result->>'schema_version' IS DISTINCT FROM 'v2'
            )
       )
       OR (
            NEW.publication_status='refused'
            AND (
                model_status<>'REFUSED'
                OR model_result_type<>'refusal'
                OR NEW.result_type<>'refusal'
                OR NEW.result->>'schema_id' IS DISTINCT FROM 'agent.refusal'
                OR NEW.result->>'schema_version' IS DISTINCT FROM 'v1'
            )
       )
       OR (
            NEW.publication_status='clarification_required'
            AND (
                model_status<>'SUCCEEDED'
                OR model_result_type<>'clarification'
                OR NEW.result_type<>'clarification'
                OR NEW.result->>'schema_id' IS DISTINCT FROM 'conversation.clarification'
                OR NEW.result->>'schema_version' IS DISTINCT FROM 'v1'
            )
       ) THEN
        RAISE EXCEPTION 'agent answer model result binding is invalid' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE agent.question
    DROP CONSTRAINT IF EXISTS agent_question_mode_check,
    DROP COLUMN mode;

DELETE FROM core.schema_meta WHERE key='workspace_analysis_persistence';
