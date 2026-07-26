-- +goose Up

ALTER TABLE agent.model_run
    DROP CONSTRAINT IF EXISTS agent_model_run_final_result_type_check,
    ADD CONSTRAINT agent_model_run_final_result_type_check CHECK (
        final_result_type IS NULL OR final_result_type IN (
            'relation_assessment', 'rag_answer', 'refusal', 'faithfulness_review',
            'tool_request', 'clarification', 'artifact_section'
        )
    );

CREATE TABLE IF NOT EXISTS learning.artifact_section_generation (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    artifact_id uuid NOT NULL,
    source_revision_id uuid NOT NULL,
    source_revision_no integer NOT NULL CHECK (source_revision_no > 0),
    source_artifact_version bigint NOT NULL CHECK (source_artifact_version > 0),
    section_key text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    workflow_run_id uuid NOT NULL,
    node_run_id uuid NOT NULL,
    status text NOT NULL CHECK (status IN ('PENDING', 'COMPLETED')),
    model_run_id uuid,
    recorded_base_revision_id uuid,
    recorded_revision_id uuid,
    recorded_artifact_version bigint CHECK (recorded_artifact_version > 0),
    content_hash text CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT uq_learning_artifact_section_generation_workspace
        UNIQUE (id, workspace_id),
    CONSTRAINT uq_learning_artifact_section_generation_key
        UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT uq_learning_artifact_section_generation_source
        UNIQUE (workspace_id, artifact_id, source_revision_id, section_key),
    CONSTRAINT uq_learning_artifact_section_generation_workflow
        UNIQUE (workflow_run_id),
    CONSTRAINT uq_learning_artifact_section_generation_node
        UNIQUE (node_run_id),
    CONSTRAINT uq_learning_artifact_section_generation_model
        UNIQUE (model_run_id),
    CONSTRAINT uq_learning_artifact_section_generation_revision
        UNIQUE (recorded_revision_id),
    CONSTRAINT learning_artifact_section_generation_text_check
        CHECK (
            section_key = btrim(section_key)
            AND btrim(section_key) <> ''
            AND octet_length(section_key) <= 128
            AND idempotency_key = btrim(idempotency_key)
            AND btrim(idempotency_key) <> ''
            AND octet_length(idempotency_key) <= 128
        ),
    CONSTRAINT learning_artifact_section_generation_time_check
        CHECK (
            updated_at >= created_at
            AND (completed_at IS NULL OR completed_at = updated_at)
        ),
    CONSTRAINT learning_artifact_section_generation_terminal_check
        CHECK (
            (
                status = 'PENDING'
                AND model_run_id IS NULL
                AND recorded_base_revision_id IS NULL
                AND recorded_revision_id IS NULL
                AND recorded_artifact_version IS NULL
                AND content_hash IS NULL
                AND completed_at IS NULL
            )
            OR (
                status = 'COMPLETED'
                AND model_run_id IS NOT NULL
                AND recorded_base_revision_id IS NOT NULL
                AND recorded_revision_id IS NOT NULL
                AND recorded_artifact_version IS NOT NULL
                AND recorded_artifact_version > source_artifact_version
                AND content_hash IS NOT NULL
                AND completed_at IS NOT NULL
            )
        ),
    CONSTRAINT fk_learning_artifact_section_generation_artifact
        FOREIGN KEY (artifact_id, workspace_id)
        REFERENCES learning.artifact(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_artifact_section_generation_source_revision
        FOREIGN KEY (source_revision_id, workspace_id, artifact_id)
        REFERENCES learning.artifact_revision(id, workspace_id, artifact_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_artifact_section_generation_recorded_revision
        FOREIGN KEY (recorded_revision_id, workspace_id, artifact_id)
        REFERENCES learning.artifact_revision(id, workspace_id, artifact_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_artifact_section_generation_recorded_base_revision
        FOREIGN KEY (recorded_base_revision_id, workspace_id, artifact_id)
        REFERENCES learning.artifact_revision(id, workspace_id, artifact_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_artifact_section_generation_workflow
        FOREIGN KEY (workflow_run_id, workspace_id)
        REFERENCES workflow.run(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_artifact_section_generation_node
        FOREIGN KEY (node_run_id, workflow_run_id)
        REFERENCES workflow.node_run(id, run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_artifact_section_generation_model
        FOREIGN KEY (model_run_id, workspace_id, workflow_run_id)
        REFERENCES agent.model_run(id, workspace_id, workflow_run_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_learning_artifact_section_generation_pending
    ON learning.artifact_section_generation (created_at, id)
    WHERE status = 'PENDING';

CREATE INDEX IF NOT EXISTS idx_learning_artifact_section_generation_artifact
    ON learning.artifact_section_generation (
        workspace_id, artifact_id, source_artifact_version, created_at DESC, id
    );

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.guard_artifact_section_generation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_revision_no integer;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'artifact section generation is append-only'
            USING ERRCODE = '55000';
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'PENDING' OR NEW.version <> 1 THEN
            RAISE EXCEPTION 'artifact section generation must start pending at version one'
                USING ERRCODE = '23514';
        END IF;
        SELECT revision_no
          INTO actual_revision_no
          FROM learning.artifact_revision
         WHERE id = NEW.source_revision_id
           AND workspace_id = NEW.workspace_id
           AND artifact_id = NEW.artifact_id;
        IF actual_revision_no IS NULL OR actual_revision_no <> NEW.source_revision_no THEN
            RAISE EXCEPTION 'artifact section generation source revision binding is invalid'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.status <> 'PENDING'
       OR NEW.status <> 'COMPLETED'
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.artifact_id IS DISTINCT FROM OLD.artifact_id
       OR NEW.source_revision_id IS DISTINCT FROM OLD.source_revision_id
       OR NEW.source_revision_no IS DISTINCT FROM OLD.source_revision_no
       OR NEW.source_artifact_version IS DISTINCT FROM OLD.source_artifact_version
       OR NEW.section_key IS DISTINCT FROM OLD.section_key
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'artifact section generation transition is invalid'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_artifact_section_generation_guard
    ON learning.artifact_section_generation;
CREATE TRIGGER trg_learning_artifact_section_generation_guard
    BEFORE INSERT OR UPDATE OR DELETE ON learning.artifact_section_generation
    FOR EACH ROW EXECUTE FUNCTION learning.guard_artifact_section_generation_mutation();

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM learning.artifact_section_generation LIMIT 1)
       OR EXISTS (
            SELECT 1 FROM agent.model_run
            WHERE final_result_type = 'artifact_section'
            LIMIT 1
       ) THEN
        RAISE EXCEPTION 'cannot remove artifact section generation with persisted data'
            USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_artifact_section_generation_guard
    ON learning.artifact_section_generation;
DROP FUNCTION IF EXISTS learning.guard_artifact_section_generation_mutation();
DROP INDEX IF EXISTS learning.idx_learning_artifact_section_generation_artifact;
DROP INDEX IF EXISTS learning.idx_learning_artifact_section_generation_pending;
DROP TABLE IF EXISTS learning.artifact_section_generation;

ALTER TABLE agent.model_run
    DROP CONSTRAINT IF EXISTS agent_model_run_final_result_type_check,
    ADD CONSTRAINT agent_model_run_final_result_type_check CHECK (
        final_result_type IS NULL OR final_result_type IN (
            'relation_assessment', 'rag_answer', 'refusal', 'faithfulness_review',
            'tool_request', 'clarification'
        )
    );
