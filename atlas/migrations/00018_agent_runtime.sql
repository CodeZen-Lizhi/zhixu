CREATE SCHEMA IF NOT EXISTS agent;

ALTER TABLE workflow.node_run
    DROP CONSTRAINT IF EXISTS uq_workflow_node_run_id_run,
    ADD CONSTRAINT uq_workflow_node_run_id_run UNIQUE (id, run_id);

ALTER TABLE workflow.node_attempt
    DROP CONSTRAINT IF EXISTS uq_workflow_node_attempt_id_node,
    ADD CONSTRAINT uq_workflow_node_attempt_id_node UNIQUE (id, node_run_id);

CREATE TABLE IF NOT EXISTS agent.model_run (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    workflow_run_id uuid NOT NULL,
    node_run_id uuid NOT NULL,
    node_attempt_id uuid NOT NULL,
    adapter_name text NOT NULL CHECK (btrim(adapter_name) <> '' AND octet_length(adapter_name) <= 128),
    adapter_version text NOT NULL CHECK (btrim(adapter_version) <> '' AND octet_length(adapter_version) <= 64),
    model_id text NOT NULL CHECK (btrim(model_id) <> '' AND octet_length(model_id) <= 128),
    model_version text NOT NULL CHECK (btrim(model_version) <> '' AND octet_length(model_version) <= 64),
    profile_id text NOT NULL CHECK (btrim(profile_id) <> '' AND octet_length(profile_id) <= 128),
    profile_version text NOT NULL CHECK (btrim(profile_version) <> '' AND octet_length(profile_version) <= 64),
    prompt_template_id text NOT NULL CHECK (btrim(prompt_template_id) <> '' AND octet_length(prompt_template_id) <= 128),
    prompt_template_version text NOT NULL CHECK (btrim(prompt_template_version) <> '' AND octet_length(prompt_template_version) <= 64),
    output_schema_id text NOT NULL CHECK (btrim(output_schema_id) <> '' AND octet_length(output_schema_id) <= 128),
    output_schema_version text NOT NULL CHECK (btrim(output_schema_version) <> '' AND octet_length(output_schema_version) <= 64),
    reduced_schema_id text NOT NULL CHECK (btrim(reduced_schema_id) <> '' AND octet_length(reduced_schema_id) <= 128),
    reduced_schema_version text NOT NULL CHECK (btrim(reduced_schema_version) <> '' AND octet_length(reduced_schema_version) <= 64),
    retrieval_index_version_id uuid NOT NULL,
    embedding_version_id uuid,
    rerank_model_version text CHECK (
        rerank_model_version IS NULL OR (btrim(rerank_model_version) <> '' AND octet_length(rerank_model_version) <= 128)
    ),
    status text NOT NULL CHECK (status IN ('RUNNING', 'SUCCEEDED', 'REFUSED', 'FAILED', 'UNKNOWN')),
    final_result_type text CHECK (
        final_result_type IS NULL OR final_result_type IN ('relation_assessment', 'rag_answer', 'refusal', 'faithfulness_review')
    ),
    error_code text CHECK (error_code IS NULL OR (btrim(error_code) <> '' AND error_code ~ '^[A-Z0-9_]+$' AND octet_length(error_code) <= 128)),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    started_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT uq_agent_model_run_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_agent_model_run_node_attempt UNIQUE (node_attempt_id),
    CONSTRAINT fk_agent_model_run_workflow_workspace
        FOREIGN KEY (workflow_run_id, workspace_id)
        REFERENCES workflow.run(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_agent_model_run_node_workflow
        FOREIGN KEY (node_run_id, workflow_run_id)
        REFERENCES workflow.node_run(id, run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_agent_model_run_attempt_node
        FOREIGN KEY (node_attempt_id, node_run_id)
        REFERENCES workflow.node_attempt(id, node_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_agent_model_run_retrieval_workspace
        FOREIGN KEY (retrieval_index_version_id, workspace_id)
        REFERENCES retrieval.index_version(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_agent_model_run_embedding
        FOREIGN KEY (embedding_version_id)
        REFERENCES retrieval.embedding_version(id) ON DELETE RESTRICT,
    CONSTRAINT fk_agent_model_run_retrieval_embedding
        FOREIGN KEY (retrieval_index_version_id, workspace_id, embedding_version_id)
        REFERENCES retrieval.index_version(id, workspace_id, embedding_version_id) ON DELETE RESTRICT,
    CONSTRAINT agent_model_run_lifecycle CHECK (
        updated_at >= started_at
        AND (completed_at IS NULL OR (completed_at >= started_at AND completed_at = updated_at))
        AND (
            (status = 'RUNNING' AND completed_at IS NULL AND final_result_type IS NULL AND error_code IS NULL)
            OR (status = 'SUCCEEDED' AND completed_at IS NOT NULL AND final_result_type IS NOT NULL AND error_code IS NULL)
            OR (status = 'REFUSED' AND completed_at IS NOT NULL AND final_result_type = 'refusal' AND error_code IS NOT NULL)
            OR (status IN ('FAILED', 'UNKNOWN') AND completed_at IS NOT NULL AND final_result_type IS NULL AND error_code IS NOT NULL)
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_agent_model_run_workspace_started
    ON agent.model_run (workspace_id, started_at DESC, id);

CREATE INDEX IF NOT EXISTS idx_agent_model_run_workflow_node
    ON agent.model_run (workflow_run_id, node_run_id, started_at DESC, id);

CREATE INDEX IF NOT EXISTS idx_agent_model_run_status
    ON agent.model_run (status, started_at, id)
    WHERE status IN ('RUNNING', 'UNKNOWN');

CREATE TABLE IF NOT EXISTS agent.model_call (
    id uuid PRIMARY KEY,
    model_run_id uuid NOT NULL REFERENCES agent.model_run(id) ON DELETE RESTRICT,
    call_no smallint NOT NULL CHECK (call_no > 0 AND call_no <= 32),
    phase text NOT NULL CHECK (phase IN ('INITIAL', 'REPAIR', 'REDUCED', 'REVIEW')),
    adapter_name text NOT NULL CHECK (btrim(adapter_name) <> '' AND octet_length(adapter_name) <= 128),
    adapter_version text NOT NULL CHECK (btrim(adapter_version) <> '' AND octet_length(adapter_version) <= 64),
    model_id text NOT NULL CHECK (btrim(model_id) <> '' AND octet_length(model_id) <= 128),
    model_version text NOT NULL CHECK (btrim(model_version) <> '' AND octet_length(model_version) <= 64),
    profile_id text NOT NULL CHECK (btrim(profile_id) <> '' AND octet_length(profile_id) <= 128),
    profile_version text NOT NULL CHECK (btrim(profile_version) <> '' AND octet_length(profile_version) <= 64),
    prompt_template_id text NOT NULL CHECK (btrim(prompt_template_id) <> '' AND octet_length(prompt_template_id) <= 128),
    prompt_template_version text NOT NULL CHECK (btrim(prompt_template_version) <> '' AND octet_length(prompt_template_version) <= 64),
    output_schema_id text NOT NULL CHECK (btrim(output_schema_id) <> '' AND octet_length(output_schema_id) <= 128),
    output_schema_version text NOT NULL CHECK (btrim(output_schema_version) <> '' AND octet_length(output_schema_version) <= 64),
    max_output_tokens integer NOT NULL CHECK (max_output_tokens > 0 AND max_output_tokens <= 131072),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    response_hash text CHECK (response_hash IS NULL OR response_hash ~ '^[0-9a-f]{64}$'),
    request_bytes bigint NOT NULL CHECK (request_bytes > 0 AND request_bytes <= 16777216),
    response_bytes bigint NOT NULL DEFAULT 0 CHECK (response_bytes >= 0 AND response_bytes <= 16777216),
    input_tokens bigint NOT NULL DEFAULT 0 CHECK (input_tokens >= 0 AND input_tokens <= 1000000),
    output_tokens bigint NOT NULL DEFAULT 0 CHECK (output_tokens >= 0 AND output_tokens <= 1000000),
    latency_ms bigint NOT NULL DEFAULT 0 CHECK (latency_ms >= 0 AND latency_ms <= 600000),
    status text NOT NULL CHECK (status IN ('STARTED', 'SUCCEEDED', 'FAILED', 'UNKNOWN')),
    error_code text CHECK (error_code IS NULL OR (btrim(error_code) <> '' AND error_code ~ '^[A-Z0-9_]+$' AND octet_length(error_code) <= 128)),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    started_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT uq_agent_model_call_run_no UNIQUE (model_run_id, call_no),
    CONSTRAINT agent_model_call_phase_order CHECK (
        (phase = 'INITIAL' AND call_no = 1)
        OR (phase = 'REPAIR' AND call_no = 2)
        OR (phase = 'REDUCED' AND call_no = 3)
        OR (phase = 'REVIEW' AND call_no >= 2)
    ),
    CONSTRAINT agent_model_call_lifecycle CHECK (
        (completed_at IS NULL OR completed_at >= started_at)
        AND (
            (status = 'STARTED' AND completed_at IS NULL AND response_hash IS NULL AND response_bytes = 0
                AND input_tokens = 0 AND output_tokens = 0 AND latency_ms = 0 AND error_code IS NULL)
            OR (status = 'SUCCEEDED' AND completed_at IS NOT NULL AND response_hash IS NOT NULL AND response_bytes > 0
                AND error_code IS NULL)
            OR (status = 'FAILED' AND completed_at IS NOT NULL AND error_code IS NOT NULL
                AND ((response_hash IS NULL AND response_bytes = 0) OR (response_hash IS NOT NULL AND response_bytes > 0)))
            OR (status = 'UNKNOWN' AND completed_at IS NOT NULL AND response_hash IS NULL AND response_bytes = 0
                AND input_tokens = 0 AND output_tokens = 0 AND error_code IS NOT NULL)
        )
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_model_call_generation_phase
    ON agent.model_call (model_run_id, phase)
    WHERE phase IN ('INITIAL', 'REPAIR', 'REDUCED');

CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_model_call_active
    ON agent.model_call (model_run_id)
    WHERE status = 'STARTED';

CREATE INDEX IF NOT EXISTS idx_agent_model_call_run_history
    ON agent.model_call (model_run_id, call_no, id);

CREATE INDEX IF NOT EXISTS idx_agent_model_call_status
    ON agent.model_call (status, started_at, id)
    WHERE status IN ('STARTED', 'UNKNOWN');

CREATE OR REPLACE FUNCTION agent.guard_model_run_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent model run is append-only' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'RUNNING' OR NEW.version <> 1 THEN
            RAISE EXCEPTION 'agent model run must start running at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.status <> 'RUNNING'
       OR NEW.status = 'RUNNING'
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id
       OR NEW.node_attempt_id IS DISTINCT FROM OLD.node_attempt_id
       OR NEW.adapter_name IS DISTINCT FROM OLD.adapter_name
       OR NEW.adapter_version IS DISTINCT FROM OLD.adapter_version
       OR NEW.model_id IS DISTINCT FROM OLD.model_id
       OR NEW.model_version IS DISTINCT FROM OLD.model_version
       OR NEW.profile_id IS DISTINCT FROM OLD.profile_id
       OR NEW.profile_version IS DISTINCT FROM OLD.profile_version
       OR NEW.prompt_template_id IS DISTINCT FROM OLD.prompt_template_id
       OR NEW.prompt_template_version IS DISTINCT FROM OLD.prompt_template_version
       OR NEW.output_schema_id IS DISTINCT FROM OLD.output_schema_id
       OR NEW.output_schema_version IS DISTINCT FROM OLD.output_schema_version
       OR NEW.reduced_schema_id IS DISTINCT FROM OLD.reduced_schema_id
       OR NEW.reduced_schema_version IS DISTINCT FROM OLD.reduced_schema_version
       OR NEW.retrieval_index_version_id IS DISTINCT FROM OLD.retrieval_index_version_id
       OR NEW.embedding_version_id IS DISTINCT FROM OLD.embedding_version_id
       OR NEW.rerank_model_version IS DISTINCT FROM OLD.rerank_model_version
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'agent model run transition or immutable binding is invalid' USING ERRCODE = '55000';
    END IF;
    IF EXISTS (
        SELECT 1 FROM agent.model_call
        WHERE model_run_id = NEW.id AND status = 'STARTED'
    ) THEN
        RAISE EXCEPTION 'agent model run cannot finalize with an active call' USING ERRCODE = '55000';
    END IF;
    IF NEW.status IN ('SUCCEEDED', 'REFUSED') AND NOT EXISTS (
        SELECT 1 FROM agent.model_call
        WHERE model_run_id = NEW.id AND status = 'SUCCEEDED'
    ) THEN
        RAISE EXCEPTION 'agent publishable model run requires a succeeded call' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS agent_model_run_guard_mutation ON agent.model_run;
CREATE TRIGGER agent_model_run_guard_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON agent.model_run
    FOR EACH ROW EXECUTE FUNCTION agent.guard_model_run_mutation();

CREATE OR REPLACE FUNCTION agent.guard_model_call_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    parent_status text;
    previous_status text;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent model call is append-only' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'STARTED' OR NEW.version <> 1 THEN
            RAISE EXCEPTION 'agent model call must start at version one' USING ERRCODE = '23514';
        END IF;
        SELECT status INTO parent_status
          FROM agent.model_run
         WHERE id = NEW.model_run_id
         FOR SHARE;
        IF parent_status IS DISTINCT FROM 'RUNNING' THEN
            RAISE EXCEPTION 'agent model call parent run is not running' USING ERRCODE = '55000';
        END IF;
        IF NEW.call_no > 1 THEN
            SELECT status INTO previous_status
              FROM agent.model_call
             WHERE model_run_id = NEW.model_run_id
               AND call_no = NEW.call_no - 1
             FOR SHARE;
            IF previous_status IS NULL OR previous_status = 'STARTED' THEN
                RAISE EXCEPTION 'agent model call sequence has a gap or active predecessor' USING ERRCODE = '55000';
            END IF;
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.status <> 'STARTED'
       OR NEW.status = 'STARTED'
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.model_run_id IS DISTINCT FROM OLD.model_run_id
       OR NEW.call_no IS DISTINCT FROM OLD.call_no
       OR NEW.phase IS DISTINCT FROM OLD.phase
       OR NEW.adapter_name IS DISTINCT FROM OLD.adapter_name
       OR NEW.adapter_version IS DISTINCT FROM OLD.adapter_version
       OR NEW.model_id IS DISTINCT FROM OLD.model_id
       OR NEW.model_version IS DISTINCT FROM OLD.model_version
       OR NEW.profile_id IS DISTINCT FROM OLD.profile_id
       OR NEW.profile_version IS DISTINCT FROM OLD.profile_version
       OR NEW.prompt_template_id IS DISTINCT FROM OLD.prompt_template_id
       OR NEW.prompt_template_version IS DISTINCT FROM OLD.prompt_template_version
       OR NEW.output_schema_id IS DISTINCT FROM OLD.output_schema_id
       OR NEW.output_schema_version IS DISTINCT FROM OLD.output_schema_version
       OR NEW.max_output_tokens IS DISTINCT FROM OLD.max_output_tokens
       OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
       OR NEW.request_bytes IS DISTINCT FROM OLD.request_bytes
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'agent model call transition or immutable binding is invalid' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS agent_model_call_guard_mutation ON agent.model_call;
CREATE TRIGGER agent_model_call_guard_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON agent.model_call
    FOR EACH ROW EXECUTE FUNCTION agent.guard_model_call_mutation();

INSERT INTO core.schema_meta (key, value)
VALUES ('agent_runtime', 'm6-02')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
