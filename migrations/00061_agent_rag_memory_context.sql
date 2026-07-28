-- +goose Up

CREATE TABLE agent.rag_memory_snapshot (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    workflow_run_id uuid NOT NULL,
    node_run_id uuid NOT NULL,
    node_attempt_id uuid NOT NULL,
    claimant_id uuid NOT NULL UNIQUE,
    owner_kind text NOT NULL CHECK (
        owner_kind = btrim(owner_kind)
        AND owner_kind ~ '^[A-Z_]+$'
        AND octet_length(owner_kind) <= 32
    ),
    owner_id uuid NOT NULL,
    task_scope_id uuid NOT NULL,
    status text NOT NULL CHECK (status IN ('PREPARING', 'READY', 'FAILED')),
    context_schema_version text CHECK (
        context_schema_version IS NULL
        OR context_schema_version = 'agent-rag-memory-context/v1'
    ),
    context_digest text CHECK (context_digest IS NULL OR context_digest ~ '^[0-9a-f]{64}$'),
    context_item_count integer CHECK (context_item_count IS NULL OR context_item_count BETWEEN 0 AND 32),
    context_bytes bigint CHECK (context_bytes IS NULL OR context_bytes BETWEEN 1 AND 65536),
    model_run_id uuid UNIQUE,
    error_code text CHECK (
        error_code IS NULL
        OR (
            error_code = btrim(error_code)
            AND error_code ~ '^[A-Z0-9_]+$'
            AND octet_length(error_code) <= 128
        )
    ),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_agent_rag_memory_snapshot_attempt UNIQUE (workspace_id, node_attempt_id),
    CONSTRAINT fk_agent_rag_memory_snapshot_workflow_workspace
        FOREIGN KEY (workflow_run_id, workspace_id)
        REFERENCES workflow.run(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_agent_rag_memory_snapshot_node_workflow
        FOREIGN KEY (node_run_id, workflow_run_id)
        REFERENCES workflow.node_run(id, run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_agent_rag_memory_snapshot_attempt_node
        FOREIGN KEY (node_attempt_id, node_run_id)
        REFERENCES workflow.node_attempt(id, node_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_agent_rag_memory_snapshot_conversation_workspace
        FOREIGN KEY (task_scope_id, workspace_id)
        REFERENCES agent.conversation(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT agent_rag_memory_snapshot_lifecycle CHECK (
        updated_at >= created_at
        AND (
            (
                status = 'PREPARING'
                AND context_schema_version IS NULL
                AND context_digest IS NULL
                AND context_item_count IS NULL
                AND context_bytes IS NULL
                AND model_run_id IS NULL
                AND error_code IS NULL
            )
            OR (
                status = 'READY'
                AND context_schema_version IS NOT NULL
                AND context_digest IS NOT NULL
                AND context_item_count IS NOT NULL
                AND context_bytes IS NOT NULL
                AND model_run_id IS NOT NULL
                AND error_code IS NULL
            )
            OR (
                status = 'FAILED'
                AND context_schema_version IS NULL
                AND context_digest IS NULL
                AND context_item_count IS NULL
                AND context_bytes IS NULL
                AND model_run_id IS NULL
                AND error_code IS NOT NULL
            )
        )
    )
);

CREATE INDEX idx_agent_rag_memory_snapshot_status
    ON agent.rag_memory_snapshot (status, created_at, id)
    WHERE status IN ('PREPARING', 'FAILED');

ALTER TABLE agent.model_run
    ADD COLUMN memory_snapshot_id uuid,
    ADD COLUMN memory_context_schema_version text,
    ADD COLUMN memory_context_digest text,
    ADD COLUMN memory_context_item_count integer,
    ADD COLUMN memory_context_bytes bigint,
    ADD CONSTRAINT agent_model_run_memory_context_binding CHECK (
        (
            memory_snapshot_id IS NULL
            AND memory_context_schema_version IS NULL
            AND memory_context_digest IS NULL
            AND memory_context_item_count IS NULL
            AND memory_context_bytes IS NULL
        )
        OR (
            memory_snapshot_id IS NOT NULL
            AND memory_context_schema_version IS NOT NULL
            AND memory_context_digest IS NOT NULL
            AND memory_context_item_count IS NOT NULL
            AND memory_context_bytes IS NOT NULL
            AND memory_context_schema_version = 'agent-rag-memory-context/v1'
            AND memory_context_digest ~ '^[0-9a-f]{64}$'
            AND memory_context_item_count BETWEEN 0 AND 32
            AND memory_context_bytes BETWEEN 1 AND 65536
        )
    ),
    ADD CONSTRAINT fk_agent_model_run_memory_snapshot
        FOREIGN KEY (memory_snapshot_id)
        REFERENCES agent.rag_memory_snapshot(id) ON DELETE RESTRICT;

ALTER TABLE agent.rag_memory_snapshot
    ADD CONSTRAINT fk_agent_rag_memory_snapshot_model_run
        FOREIGN KEY (model_run_id)
        REFERENCES agent.model_run(id) ON DELETE RESTRICT;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agent.guard_rag_memory_snapshot_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent rag memory snapshot is retained audit history' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'PREPARING' THEN
            RAISE EXCEPTION 'agent rag memory snapshot must start preparing' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.status <> 'PREPARING'
       OR NEW.status NOT IN ('READY', 'FAILED')
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id
       OR NEW.node_attempt_id IS DISTINCT FROM OLD.node_attempt_id
       OR NEW.claimant_id IS DISTINCT FROM OLD.claimant_id
       OR NEW.owner_kind IS DISTINCT FROM OLD.owner_kind
       OR NEW.owner_id IS DISTINCT FROM OLD.owner_id
       OR NEW.task_scope_id IS DISTINCT FROM OLD.task_scope_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'agent rag memory snapshot transition or binding is invalid' USING ERRCODE = '55000';
    END IF;
    IF NEW.status = 'READY' AND NOT EXISTS (
        SELECT 1
          FROM agent.model_run run
         WHERE run.id = NEW.model_run_id
           AND run.workspace_id = NEW.workspace_id
           AND run.workflow_run_id = NEW.workflow_run_id
           AND run.node_run_id = NEW.node_run_id
           AND run.node_attempt_id = NEW.node_attempt_id
           AND run.memory_snapshot_id = NEW.id
           AND run.memory_context_schema_version = NEW.context_schema_version
           AND run.memory_context_digest = NEW.context_digest
           AND run.memory_context_item_count = NEW.context_item_count
           AND run.memory_context_bytes = NEW.context_bytes
    ) THEN
        RAISE EXCEPTION 'ready rag memory snapshot lacks its exact model run binding' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agent_rag_memory_snapshot_guard_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON agent.rag_memory_snapshot
    FOR EACH ROW EXECUTE FUNCTION agent.guard_rag_memory_snapshot_mutation();

-- +goose StatementBegin
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
        IF NEW.retrieval_index_version_id IS NULL AND NEW.output_schema_id <> 'agent.rag-answer' THEN
            RAISE EXCEPTION 'only running rag model run may defer retrieval binding' USING ERRCODE = '23514';
        END IF;
        IF NEW.memory_snapshot_id IS NULL AND EXISTS (
            SELECT 1
              FROM workflow.node_run node
             WHERE node.id = NEW.node_run_id
               AND node.run_id = NEW.workflow_run_id
               AND node.node_type = 'agent.rag-answer'
        ) THEN
            RAISE EXCEPTION 'conversation rag model run requires a memory snapshot binding' USING ERRCODE = '23514';
        END IF;
        IF NEW.memory_snapshot_id IS NOT NULL AND (
            NEW.output_schema_id <> 'agent.rag-answer'
            OR NOT EXISTS (
                SELECT 1
                  FROM workflow.node_run node
                 WHERE node.id = NEW.node_run_id
                   AND node.run_id = NEW.workflow_run_id
                   AND node.node_type = 'agent.rag-answer'
            )
        ) THEN
            RAISE EXCEPTION 'only conversation rag model run may bind a memory snapshot' USING ERRCODE = '23514';
        END IF;
        IF NEW.memory_snapshot_id IS NOT NULL AND NOT EXISTS (
            SELECT 1
              FROM agent.rag_memory_snapshot snapshot
             WHERE snapshot.id = NEW.memory_snapshot_id
               AND snapshot.workspace_id = NEW.workspace_id
               AND snapshot.workflow_run_id = NEW.workflow_run_id
               AND snapshot.node_run_id = NEW.node_run_id
               AND snapshot.node_attempt_id = NEW.node_attempt_id
               AND snapshot.status = 'PREPARING'
        ) THEN
            RAISE EXCEPTION 'rag model run memory snapshot is not preparing for this attempt' USING ERRCODE = '55000';
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
       OR NEW.memory_snapshot_id IS DISTINCT FROM OLD.memory_snapshot_id
       OR NEW.memory_context_schema_version IS DISTINCT FROM OLD.memory_context_schema_version
       OR NEW.memory_context_digest IS DISTINCT FROM OLD.memory_context_digest
       OR NEW.memory_context_item_count IS DISTINCT FROM OLD.memory_context_item_count
       OR NEW.memory_context_bytes IS DISTINCT FROM OLD.memory_context_bytes
       OR (OLD.retrieval_index_version_id IS NOT NULL AND (
            NEW.retrieval_index_version_id IS DISTINCT FROM OLD.retrieval_index_version_id
            OR NEW.embedding_version_id IS DISTINCT FROM OLD.embedding_version_id
            OR NEW.rerank_model_version IS DISTINCT FROM OLD.rerank_model_version
       ))
       OR (OLD.retrieval_index_version_id IS NULL AND (
            OLD.embedding_version_id IS NOT NULL
            OR OLD.rerank_model_version IS NOT NULL
            OR (NEW.retrieval_index_version_id IS NULL AND (
                NEW.embedding_version_id IS NOT NULL OR NEW.rerank_model_version IS NOT NULL
            ))
       ))
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
    IF NEW.status = 'SUCCEEDED' AND NOT EXISTS (
        SELECT 1 FROM agent.model_call
        WHERE model_run_id = NEW.id AND status = 'SUCCEEDED'
    ) THEN
        RAISE EXCEPTION 'agent successful model run requires a succeeded call' USING ERRCODE = '55000';
    END IF;
    IF NEW.status = 'REFUSED'
       AND EXISTS (SELECT 1 FROM agent.model_call WHERE model_run_id = NEW.id)
       AND NOT EXISTS (
            SELECT 1 FROM agent.model_call
            WHERE model_run_id = NEW.id AND status = 'SUCCEEDED'
       ) THEN
        RAISE EXCEPTION 'agent refused model run with calls requires a succeeded call' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

INSERT INTO core.schema_meta (key, value)
VALUES ('agent_rag_memory_context', 'm8')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    LOCK TABLE agent.rag_memory_snapshot, agent.model_run IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM agent.rag_memory_snapshot)
       OR EXISTS (SELECT 1 FROM agent.model_run WHERE memory_snapshot_id IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot remove Agent RAG Memory context while snapshot audit exists' USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DELETE FROM core.schema_meta WHERE key = 'agent_rag_memory_context';

ALTER TABLE agent.rag_memory_snapshot
    DROP CONSTRAINT IF EXISTS fk_agent_rag_memory_snapshot_model_run;

ALTER TABLE agent.model_run
    DROP CONSTRAINT IF EXISTS fk_agent_model_run_memory_snapshot,
    DROP CONSTRAINT IF EXISTS agent_model_run_memory_context_binding,
    DROP COLUMN IF EXISTS memory_context_bytes,
    DROP COLUMN IF EXISTS memory_context_item_count,
    DROP COLUMN IF EXISTS memory_context_digest,
    DROP COLUMN IF EXISTS memory_context_schema_version,
    DROP COLUMN IF EXISTS memory_snapshot_id;

DROP TRIGGER IF EXISTS agent_rag_memory_snapshot_guard_mutation ON agent.rag_memory_snapshot;
DROP FUNCTION IF EXISTS agent.guard_rag_memory_snapshot_mutation();
DROP INDEX IF EXISTS agent.idx_agent_rag_memory_snapshot_status;
DROP TABLE IF EXISTS agent.rag_memory_snapshot;

-- Restore the deferred-retrieval trigger installed by 00022.
-- +goose StatementBegin
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
        IF NEW.retrieval_index_version_id IS NULL AND NEW.output_schema_id <> 'agent.rag-answer' THEN
            RAISE EXCEPTION 'only running rag model run may defer retrieval binding' USING ERRCODE = '23514';
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
       OR (OLD.retrieval_index_version_id IS NOT NULL AND (
            NEW.retrieval_index_version_id IS DISTINCT FROM OLD.retrieval_index_version_id
            OR NEW.embedding_version_id IS DISTINCT FROM OLD.embedding_version_id
            OR NEW.rerank_model_version IS DISTINCT FROM OLD.rerank_model_version
       ))
       OR (OLD.retrieval_index_version_id IS NULL AND (
            OLD.embedding_version_id IS NOT NULL
            OR OLD.rerank_model_version IS NOT NULL
            OR (NEW.retrieval_index_version_id IS NULL AND (
                NEW.embedding_version_id IS NOT NULL OR NEW.rerank_model_version IS NOT NULL
            ))
       ))
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
    IF NEW.status = 'SUCCEEDED' AND NOT EXISTS (
        SELECT 1 FROM agent.model_call
        WHERE model_run_id = NEW.id AND status = 'SUCCEEDED'
    ) THEN
        RAISE EXCEPTION 'agent successful model run requires a succeeded call' USING ERRCODE = '55000';
    END IF;
    IF NEW.status = 'REFUSED'
       AND EXISTS (SELECT 1 FROM agent.model_call WHERE model_run_id = NEW.id)
       AND NOT EXISTS (
            SELECT 1 FROM agent.model_call
            WHERE model_run_id = NEW.id AND status = 'SUCCEEDED'
       ) THEN
        RAISE EXCEPTION 'agent refused model run with calls requires a succeeded call' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
