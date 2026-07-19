-- +goose Up

ALTER TABLE agent.model_run
    DROP CONSTRAINT IF EXISTS model_run_final_result_type_check,
    DROP CONSTRAINT IF EXISTS agent_model_run_final_result_type_check,
    ADD CONSTRAINT agent_model_run_final_result_type_check CHECK (
        final_result_type IS NULL OR final_result_type IN (
            'relation_assessment', 'rag_answer', 'refusal', 'faithfulness_review', 'clarification'
        )
    );

ALTER TABLE agent.model_call
    DROP CONSTRAINT IF EXISTS model_call_phase_check,
    DROP CONSTRAINT IF EXISTS agent_model_call_phase_check,
    ADD CONSTRAINT agent_model_call_phase_check CHECK (
        phase IN ('PLAN', 'INITIAL', 'REPAIR', 'REDUCED', 'REVIEW')
    ),
    DROP CONSTRAINT IF EXISTS agent_model_call_phase_order,
    ADD CONSTRAINT agent_model_call_phase_order CHECK (
        (phase = 'PLAN' AND call_no = 1)
        OR (phase = 'INITIAL' AND call_no IN (1, 2))
        OR (phase = 'REPAIR' AND call_no IN (2, 3))
        OR (phase = 'REDUCED' AND call_no IN (3, 4))
        OR (phase = 'REVIEW' AND call_no >= 2)
    );

ALTER TABLE agent.model_run
    DROP CONSTRAINT IF EXISTS uq_agent_model_run_id_workspace_workflow,
    ADD CONSTRAINT uq_agent_model_run_id_workspace_workflow
        UNIQUE (id, workspace_id, workflow_run_id);

CREATE TABLE IF NOT EXISTS agent.conversation (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN ('open', 'archived')),
    title text CHECK (
        title IS NULL OR (
            title = btrim(title)
            AND
            btrim(title) <> ''
            AND octet_length(title) <= 512
        )
    ),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    last_activity_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    archived_at timestamptz,
    idempotency_key text NOT NULL CHECK (
        idempotency_key = btrim(idempotency_key)
        AND idempotency_key <> ''
        AND octet_length(idempotency_key) <= 128
        AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT uq_agent_conversation_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_agent_conversation_workspace_idempotency UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT agent_conversation_time_order CHECK (
        updated_at >= created_at
        AND last_activity_at >= created_at
        AND (
            (status = 'open' AND archived_at IS NULL)
            OR (status = 'archived' AND archived_at IS NOT NULL AND archived_at >= created_at)
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_agent_conversation_workspace_activity
    ON agent.conversation (workspace_id, last_activity_at DESC, id);

CREATE TABLE IF NOT EXISTS agent.question (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    conversation_id uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal > 0),
    question_text text NOT NULL CHECK (
        question_text = btrim(question_text)
        AND btrim(question_text) <> ''
        AND octet_length(question_text) <= 8192
    ),
    scope jsonb NOT NULL CHECK (
        jsonb_typeof(scope) = 'object'
        AND octet_length(scope::text) <= 16384
    ),
    answer_depth text NOT NULL CHECK (answer_depth IN ('concise', 'standard', 'detailed')),
    output_format text NOT NULL CHECK (output_format IN ('markdown', 'outline')),
    context_through_ordinal integer NOT NULL CHECK (context_through_ordinal >= 0),
    context_hash text NOT NULL CHECK (context_hash ~ '^[0-9a-f]{64}$'),
    idempotency_key text NOT NULL CHECK (
        idempotency_key = btrim(idempotency_key)
        AND idempotency_key <> ''
        AND octet_length(idempotency_key) <= 128
        AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_agent_question_identity UNIQUE (id, conversation_id, workspace_id),
    CONSTRAINT uq_agent_question_ordinal UNIQUE (conversation_id, ordinal),
    CONSTRAINT uq_agent_question_idempotency UNIQUE (conversation_id, idempotency_key),
    CONSTRAINT fk_agent_question_conversation_workspace
        FOREIGN KEY (conversation_id, workspace_id)
        REFERENCES agent.conversation(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT agent_question_context_bound CHECK (context_through_ordinal < ordinal)
);

CREATE INDEX IF NOT EXISTS idx_agent_question_conversation_ordinal
    ON agent.question (conversation_id, ordinal, id);

CREATE TABLE IF NOT EXISTS agent.answer (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    conversation_id uuid NOT NULL,
    question_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    model_run_id uuid,
    publication_status text NOT NULL CHECK (publication_status IN ('pending', 'completed', 'refused', 'clarification_required')),
    result_type text CHECK (result_type IS NULL OR result_type IN ('rag_answer', 'refusal', 'clarification')),
    result jsonb,
    result_hash text CHECK (result_hash IS NULL OR result_hash ~ '^[0-9a-f]{64}$'),
    retrieval_summary jsonb,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    published_at timestamptz,
    CONSTRAINT uq_agent_answer_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_agent_answer_question UNIQUE (question_id),
    CONSTRAINT uq_agent_answer_workflow_run UNIQUE (workflow_run_id),
    CONSTRAINT uq_agent_answer_model_run UNIQUE (model_run_id),
    CONSTRAINT fk_agent_answer_question_workspace
        FOREIGN KEY (question_id, conversation_id, workspace_id)
        REFERENCES agent.question(id, conversation_id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_agent_answer_workflow_workspace
        FOREIGN KEY (workflow_run_id, workspace_id)
        REFERENCES workflow.run(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_agent_answer_model_run_workspace
        FOREIGN KEY (model_run_id, workspace_id, workflow_run_id)
        REFERENCES agent.model_run(id, workspace_id, workflow_run_id) ON DELETE RESTRICT,
    CONSTRAINT agent_answer_payload_shape CHECK (
        (result IS NULL OR (
            jsonb_typeof(result) = 'object'
            AND octet_length(result::text) <= 262144
        ))
        AND (
            retrieval_summary IS NULL OR (
                jsonb_typeof(retrieval_summary) = 'object'
                AND octet_length(retrieval_summary::text) <= 16384
            )
        )
    ),
    CONSTRAINT agent_answer_time_order CHECK (
        updated_at >= created_at
        AND (published_at IS NULL OR (published_at >= created_at AND published_at = updated_at))
    ),
    CONSTRAINT agent_answer_publication_bundle CHECK (
        (
            publication_status = 'pending'
            AND model_run_id IS NULL
            AND result_type IS NULL
            AND result IS NULL
            AND result_hash IS NULL
            AND retrieval_summary IS NULL
            AND published_at IS NULL
        )
        OR (
            publication_status = 'completed'
            AND model_run_id IS NOT NULL
            AND result_type = 'rag_answer'
            AND result IS NOT NULL
            AND result_hash IS NOT NULL
            AND retrieval_summary IS NOT NULL
            AND published_at IS NOT NULL
        )
        OR (
            publication_status = 'refused'
            AND model_run_id IS NOT NULL
            AND result_type = 'refusal'
            AND result IS NOT NULL
            AND result_hash IS NOT NULL
            AND retrieval_summary IS NOT NULL
            AND published_at IS NOT NULL
        )
        OR (
            publication_status = 'clarification_required'
            AND model_run_id IS NOT NULL
            AND result_type = 'clarification'
            AND result IS NOT NULL
            AND result_hash IS NOT NULL
            AND retrieval_summary IS NOT NULL
            AND published_at IS NOT NULL
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_agent_answer_conversation_created
    ON agent.answer (conversation_id, created_at DESC, id);

CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_answer_conversation_pending
    ON agent.answer (conversation_id)
    WHERE publication_status = 'pending';

CREATE TABLE IF NOT EXISTS agent.answer_feedback (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    answer_id uuid NOT NULL,
    feedback_type text NOT NULL CHECK (
        feedback_type IN ('helpful', 'incorrect', 'irrelevant_citation', 'broken_citation', 'missing_source')
    ),
    citation_id text CHECK (
        citation_id IS NULL OR (
            citation_id = btrim(citation_id)
            AND citation_id <> ''
            AND octet_length(citation_id) <= 128
            AND citation_id !~ E'[\\r\\n]'
        )
    ),
    comment text CHECK (
        comment IS NULL OR (
            comment = btrim(comment)
            AND btrim(comment) <> ''
            AND octet_length(comment) <= 2048
        )
    ),
    idempotency_key text NOT NULL CHECK (
        idempotency_key = btrim(idempotency_key)
        AND idempotency_key <> ''
        AND octet_length(idempotency_key) <= 128
        AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_agent_answer_feedback_answer_idempotency UNIQUE (answer_id, idempotency_key),
    CONSTRAINT fk_agent_answer_feedback_answer_workspace
        FOREIGN KEY (answer_id, workspace_id)
        REFERENCES agent.answer(id, workspace_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_agent_answer_feedback_answer_created
    ON agent.answer_feedback (answer_id, created_at DESC, id);

CREATE TABLE IF NOT EXISTS ops.server_event (
    seq bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    conversation_id uuid,
    workflow_run_id uuid,
    event_type text NOT NULL CHECK (btrim(event_type) <> '' AND octet_length(event_type) <= 128),
    resource_ref text NOT NULL CHECK (
        resource_ref = btrim(resource_ref)
        AND resource_ref <> ''
        AND octet_length(resource_ref) <= 256
        AND resource_ref !~ E'[\\r\\n]'
    ),
    resource_version bigint NOT NULL CHECK (resource_version > 0),
    payload_summary jsonb NOT NULL CHECK (
        jsonb_typeof(payload_summary) = 'object'
        AND octet_length(payload_summary::text) <= 16384
    ),
    schema_version integer NOT NULL CHECK (schema_version > 0),
    source_event_ref text NOT NULL CHECK (
        source_event_ref = btrim(source_event_ref)
        AND source_event_ref <> ''
        AND octet_length(source_event_ref) <= 256
        AND source_event_ref !~ E'[\\r\\n]'
    ),
    occurred_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CONSTRAINT fk_ops_server_event_conversation_workspace
        FOREIGN KEY (conversation_id, workspace_id)
        REFERENCES agent.conversation(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_ops_server_event_workflow_workspace
        FOREIGN KEY (workflow_run_id, workspace_id)
        REFERENCES workflow.run(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT ops_server_event_expiry_window CHECK (expires_at = occurred_at + INTERVAL '24 hours')
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_ops_server_event_source
    ON ops.server_event (workspace_id, source_event_ref);

CREATE INDEX IF NOT EXISTS idx_ops_server_event_workspace_seq
    ON ops.server_event (workspace_id, seq);

CREATE INDEX IF NOT EXISTS idx_ops_server_event_conversation_seq
    ON ops.server_event (conversation_id, seq)
    WHERE conversation_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ops_server_event_expiry
    ON ops.server_event (expires_at, seq);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agent.guard_model_call_phase_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    previous_phase text;
BEGIN
    IF NEW.call_no = 1 THEN
        RETURN NEW;
    END IF;

    SELECT phase
      INTO previous_phase
     FROM agent.model_call
     WHERE model_run_id = NEW.model_run_id
       AND call_no = NEW.call_no - 1
     FOR SHARE;

    IF (NEW.phase = 'INITIAL' AND previous_phase IS DISTINCT FROM 'PLAN')
       OR (NEW.phase = 'REPAIR' AND previous_phase IS DISTINCT FROM 'INITIAL')
       OR (NEW.phase = 'REDUCED' AND previous_phase IS DISTINCT FROM 'REPAIR')
       OR (
            NEW.phase = 'REVIEW'
            AND (
                previous_phase IS NULL
                OR previous_phase NOT IN ('INITIAL', 'REPAIR', 'REDUCED')
            )
       ) THEN
        RAISE EXCEPTION 'agent model call phase predecessor is invalid' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agent.guard_conversation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent conversation cannot be deleted' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'open' OR NEW.version <> 1 OR NEW.archived_at IS NOT NULL THEN
            RAISE EXCEPTION 'agent conversation must start open at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.status <> 'open'
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.title IS DISTINCT FROM OLD.title
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
       OR NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at
       OR NEW.last_activity_at < OLD.last_activity_at
       OR (
            NEW.status = 'open'
            AND NEW.archived_at IS NOT NULL
       )
       OR (
            NEW.status = 'archived'
            AND (NEW.archived_at IS NULL OR NEW.archived_at <> NEW.updated_at)
       )
       OR NEW.status NOT IN ('open', 'archived') THEN
        RAISE EXCEPTION 'agent conversation transition or immutable binding is invalid' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agent.reject_append_only_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'agent conversation fact is append-only' USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

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
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent answer is append-only' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.publication_status <> 'pending'
           OR NEW.version <> 1
           OR NEW.model_run_id IS NOT NULL
           OR NEW.result_type IS NOT NULL
           OR NEW.result IS NOT NULL
           OR NEW.result_hash IS NOT NULL
           OR NEW.retrieval_summary IS NOT NULL
           OR NEW.published_at IS NOT NULL THEN
            RAISE EXCEPTION 'agent answer must be inserted pending at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.publication_status <> 'pending'
       OR NEW.publication_status = 'pending'
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
       OR NEW.question_id IS DISTINCT FROM OLD.question_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at
       OR NEW.model_run_id IS NULL
       OR NEW.result_type IS NULL
       OR NEW.result IS NULL
       OR NEW.result_hash IS NULL
       OR NEW.retrieval_summary IS NULL
       OR NEW.published_at IS NULL
       OR NEW.updated_at <> NEW.published_at THEN
        RAISE EXCEPTION 'agent answer transition or immutable binding is invalid' USING ERRCODE = '55000';
    END IF;
    SELECT status, final_result_type, workflow_run_id
      INTO model_status, model_result_type, model_workflow_run_id
      FROM agent.model_run
     WHERE id = NEW.model_run_id
       AND workspace_id = NEW.workspace_id
     FOR SHARE;
    IF model_workflow_run_id IS NULL
       OR model_workflow_run_id <> NEW.workflow_run_id
       OR NEW.result->>'result_type' IS DISTINCT FROM NEW.result_type
       OR NEW.result->>'model_run_ref' IS DISTINCT FROM NEW.model_run_id::text
       OR (
            NEW.publication_status = 'completed'
            AND (
                model_status <> 'SUCCEEDED'
                OR model_result_type <> 'rag_answer'
                OR NEW.result_type <> 'rag_answer'
                OR NEW.result->>'schema_id' IS DISTINCT FROM 'agent.rag-answer'
                OR NEW.result->>'schema_version' IS DISTINCT FROM 'v2'
            )
       )
       OR (
            NEW.publication_status = 'refused'
            AND (
                model_status <> 'REFUSED'
                OR model_result_type <> 'refusal'
                OR NEW.result_type <> 'refusal'
                OR NEW.result->>'schema_id' IS DISTINCT FROM 'agent.refusal'
                OR NEW.result->>'schema_version' IS DISTINCT FROM 'v1'
            )
       )
       OR (
            NEW.publication_status = 'clarification_required'
            AND (
                model_status <> 'SUCCEEDED'
                OR model_result_type <> 'clarification'
                OR NEW.result_type <> 'clarification'
                OR NEW.result->>'schema_id' IS DISTINCT FROM 'conversation.clarification'
                OR NEW.result->>'schema_version' IS DISTINCT FROM 'v1'
            )
       ) THEN
        RAISE EXCEPTION 'agent answer model result binding is invalid' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agent.verify_answer_feedback_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    answer_row agent.answer%ROWTYPE;
BEGIN
    SELECT *
      INTO answer_row
      FROM agent.answer
     WHERE id = NEW.answer_id
       AND workspace_id = NEW.workspace_id
     FOR SHARE;

    IF answer_row.id IS NULL THEN
        RETURN NEW;
    END IF;

    IF answer_row.publication_status NOT IN ('completed', 'refused') THEN
        RAISE EXCEPTION 'feedback requires a published answer or refusal' USING ERRCODE = '23514';
    END IF;

    IF NEW.feedback_type IN ('irrelevant_citation', 'broken_citation') THEN
        IF NEW.citation_id IS NULL THEN
            RAISE EXCEPTION 'citation feedback requires a citation id' USING ERRCODE = '23514';
        END IF;
        IF answer_row.result_type <> 'rag_answer'
           OR answer_row.result IS NULL
           OR NOT EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(COALESCE(answer_row.result->'payload'->'citations', '[]'::jsonb)) AS citation(value)
                 WHERE citation.value->>'id' = NEW.citation_id
           ) THEN
            RAISE EXCEPTION 'citation feedback must bind to an existing answer citation' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.citation_id IS NOT NULL THEN
        RAISE EXCEPTION 'non-citation feedback cannot bind a citation id' USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_workflow_outbox_server_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO ops.server_event(
        workspace_id, conversation_id, workflow_run_id, event_type, resource_ref, resource_version,
        payload_summary, schema_version, source_event_ref, occurred_at, expires_at
    ) VALUES(
        NEW.workspace_id,
        NULL,
        NEW.run_id,
        NEW.event_type,
        CASE
            WHEN NEW.run_id IS NOT NULL THEN 'workflow.run:' || NEW.run_id::text
            ELSE 'workflow.outbox_event:' || NEW.id::text
        END,
        COALESCE(NEW.event_version, 1),
        jsonb_strip_nulls(jsonb_build_object(
            'source_event_id', NEW.id::text,
            'workflow_run_id', CASE WHEN NEW.run_id IS NULL THEN NULL ELSE NEW.run_id::text END
        )),
        1,
        'workflow.outbox_event:' || NEW.id::text,
        NEW.occurred_at,
        NEW.occurred_at + INTERVAL '24 hours'
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS agent_question_reject_mutation ON agent.question;
CREATE TRIGGER agent_question_reject_mutation
    BEFORE UPDATE OR DELETE ON agent.question
    FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

DROP TRIGGER IF EXISTS agent_conversation_guard_mutation ON agent.conversation;
CREATE TRIGGER agent_conversation_guard_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON agent.conversation
    FOR EACH ROW EXECUTE FUNCTION agent.guard_conversation_mutation();

DROP TRIGGER IF EXISTS agent_answer_guard_mutation ON agent.answer;
CREATE TRIGGER agent_answer_guard_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON agent.answer
    FOR EACH ROW EXECUTE FUNCTION agent.guard_answer_mutation();

DROP TRIGGER IF EXISTS agent_answer_feedback_verify_binding ON agent.answer_feedback;
CREATE TRIGGER agent_answer_feedback_verify_binding
    BEFORE INSERT ON agent.answer_feedback
    FOR EACH ROW EXECUTE FUNCTION agent.verify_answer_feedback_binding();

DROP TRIGGER IF EXISTS agent_answer_feedback_reject_mutation ON agent.answer_feedback;
CREATE TRIGGER agent_answer_feedback_reject_mutation
    BEFORE UPDATE OR DELETE ON agent.answer_feedback
    FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

DROP TRIGGER IF EXISTS agent_model_call_guard_phase_sequence ON agent.model_call;
CREATE TRIGGER agent_model_call_guard_phase_sequence
    BEFORE INSERT ON agent.model_call
    FOR EACH ROW EXECUTE FUNCTION agent.guard_model_call_phase_sequence();

DROP TRIGGER IF EXISTS workflow_outbox_project_server_event ON workflow.outbox_event;
CREATE TRIGGER workflow_outbox_project_server_event
    AFTER INSERT ON workflow.outbox_event
    FOR EACH ROW EXECUTE FUNCTION ops.project_workflow_outbox_server_event();

INSERT INTO core.schema_meta (key, value)
VALUES ('rag_conversation_sse', 'm6-04')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    LOCK TABLE workflow.outbox_event, agent.answer_feedback, agent.answer, agent.question, agent.conversation, ops.server_event,
               agent.model_call, agent.model_run IN ACCESS EXCLUSIVE MODE;

    IF EXISTS (SELECT 1 FROM agent.answer_feedback)
       OR EXISTS (SELECT 1 FROM agent.answer)
       OR EXISTS (SELECT 1 FROM agent.question)
       OR EXISTS (SELECT 1 FROM agent.conversation)
       OR EXISTS (SELECT 1 FROM ops.server_event)
       OR EXISTS (SELECT 1 FROM agent.model_call WHERE phase = 'PLAN')
       OR EXISTS (SELECT 1 FROM agent.model_run WHERE final_result_type = 'clarification')
    THEN
        RAISE EXCEPTION 'cannot downgrade RAG Conversation SSE while conversation, event, or additive agent runtime facts exist'
            USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DELETE FROM core.schema_meta WHERE key = 'rag_conversation_sse';

DROP TRIGGER IF EXISTS workflow_outbox_project_server_event ON workflow.outbox_event;
DROP TRIGGER IF EXISTS agent_model_call_guard_phase_sequence ON agent.model_call;
DROP TRIGGER IF EXISTS agent_answer_feedback_reject_mutation ON agent.answer_feedback;
DROP TRIGGER IF EXISTS agent_answer_feedback_verify_binding ON agent.answer_feedback;
DROP TRIGGER IF EXISTS agent_answer_guard_mutation ON agent.answer;
DROP TRIGGER IF EXISTS agent_question_reject_mutation ON agent.question;
DROP TRIGGER IF EXISTS agent_conversation_guard_mutation ON agent.conversation;
DROP FUNCTION IF EXISTS ops.project_workflow_outbox_server_event();
DROP FUNCTION IF EXISTS agent.guard_model_call_phase_sequence();
DROP FUNCTION IF EXISTS agent.verify_answer_feedback_binding();
DROP FUNCTION IF EXISTS agent.guard_answer_mutation();
DROP FUNCTION IF EXISTS agent.reject_append_only_mutation();
DROP FUNCTION IF EXISTS agent.guard_conversation_mutation();

DROP INDEX IF EXISTS ops.idx_ops_server_event_expiry;
DROP INDEX IF EXISTS ops.idx_ops_server_event_conversation_seq;
DROP INDEX IF EXISTS ops.idx_ops_server_event_workspace_seq;
DROP INDEX IF EXISTS ops.uq_ops_server_event_source;
DROP TABLE IF EXISTS ops.server_event;

DROP INDEX IF EXISTS agent.idx_agent_answer_feedback_answer_created;
DROP TABLE IF EXISTS agent.answer_feedback;

DROP INDEX IF EXISTS agent.idx_agent_answer_conversation_created;
DROP INDEX IF EXISTS agent.uq_agent_answer_conversation_pending;
DROP TABLE IF EXISTS agent.answer;

DROP INDEX IF EXISTS agent.idx_agent_question_conversation_ordinal;
DROP TABLE IF EXISTS agent.question;

DROP INDEX IF EXISTS agent.idx_agent_conversation_workspace_activity;
DROP TABLE IF EXISTS agent.conversation;

ALTER TABLE agent.model_run
    DROP CONSTRAINT IF EXISTS uq_agent_model_run_id_workspace_workflow;

ALTER TABLE agent.model_call
    DROP CONSTRAINT IF EXISTS agent_model_call_phase_order,
    DROP CONSTRAINT IF EXISTS agent_model_call_phase_check,
    ADD CONSTRAINT model_call_phase_check CHECK (phase IN ('INITIAL', 'REPAIR', 'REDUCED', 'REVIEW')),
    ADD CONSTRAINT agent_model_call_phase_order CHECK (
        (phase = 'INITIAL' AND call_no = 1)
        OR (phase = 'REPAIR' AND call_no = 2)
        OR (phase = 'REDUCED' AND call_no = 3)
        OR (phase = 'REVIEW' AND call_no >= 2)
    );

ALTER TABLE agent.model_run
    DROP CONSTRAINT IF EXISTS agent_model_run_final_result_type_check,
    ADD CONSTRAINT model_run_final_result_type_check CHECK (
        final_result_type IS NULL OR final_result_type IN (
            'relation_assessment', 'rag_answer', 'refusal', 'faithfulness_review'
        )
    );
