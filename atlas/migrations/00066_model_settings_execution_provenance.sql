ALTER TABLE retrieval.embedding_version
    ADD COLUMN IF NOT EXISTS model_settings_revision bigint;

ALTER TABLE retrieval.index_version
    ADD COLUMN IF NOT EXISTS model_settings_revision bigint;

ALTER TABLE agent.model_run
    ADD COLUMN IF NOT EXISTS model_settings_revision bigint;

ALTER TABLE retrieval.embedding_version
    DROP CONSTRAINT IF EXISTS retrieval_embedding_version_model_settings_revision,
    DROP CONSTRAINT IF EXISTS uq_retrieval_embedding_version_contract,
    ADD CONSTRAINT retrieval_embedding_version_model_settings_revision CHECK (
        model_settings_revision IS NULL OR model_settings_revision >= 0
    ),
    ADD CONSTRAINT uq_retrieval_embedding_version_contract
        UNIQUE NULLS NOT DISTINCT (provider, model, dimensions, config_hash, model_settings_revision);

ALTER TABLE retrieval.index_version
    DROP CONSTRAINT IF EXISTS retrieval_index_version_model_settings_revision,
    ADD CONSTRAINT retrieval_index_version_model_settings_revision CHECK ((
        (embedding_version_id IS NULL AND model_settings_revision IS NULL)
        OR (embedding_version_id IS NOT NULL
            AND (model_settings_revision IS NULL OR model_settings_revision >= 0))
    ) IS TRUE);

ALTER TABLE agent.model_run
    DROP CONSTRAINT IF EXISTS agent_model_run_model_settings_revision,
    ADD CONSTRAINT agent_model_run_model_settings_revision CHECK (
        model_settings_revision IS NULL OR model_settings_revision >= 0
    );

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM retrieval.embedding_version AS embedding
         WHERE embedding.model_settings_revision IS NOT NULL
           AND NOT ops.model_settings_revision_exists(embedding.model_settings_revision)
         LIMIT 1
    ) THEN
        RAISE EXCEPTION 'retrieval embedding contains an unknown model settings revision'
            USING ERRCODE = '23503';
    END IF;
    IF EXISTS (
        SELECT 1
          FROM retrieval.index_version AS index_version
          LEFT JOIN retrieval.embedding_version AS embedding
            ON embedding.id = index_version.embedding_version_id
         WHERE (index_version.embedding_version_id IS NULL
                AND index_version.model_settings_revision IS NOT NULL)
            OR (index_version.embedding_version_id IS NOT NULL
                AND (embedding.id IS NULL
                     OR index_version.model_settings_revision IS DISTINCT FROM embedding.model_settings_revision))
         LIMIT 1
    ) THEN
        RAISE EXCEPTION 'retrieval index contains inconsistent embedding model settings provenance'
            USING ERRCODE = '23514';
    END IF;
    IF EXISTS (
        SELECT 1
          FROM agent.model_run AS model_run
          LEFT JOIN workflow.node_attempt AS attempt
            ON attempt.id = model_run.node_attempt_id
           AND attempt.node_run_id = model_run.node_run_id
         WHERE attempt.id IS NULL
            OR model_run.model_settings_revision IS DISTINCT FROM attempt.model_settings_revision
         LIMIT 1
    ) THEN
        RAISE EXCEPTION 'agent model run contains inconsistent workflow model settings provenance'
            USING ERRCODE = '23514';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION retrieval.enforce_embedding_model_settings_revision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.model_settings_revision IS DISTINCT FROM OLD.model_settings_revision THEN
        RAISE EXCEPTION 'retrieval embedding model settings revision is immutable'
            USING ERRCODE = '55000';
    END IF;
    IF NEW.model_settings_revision IS NOT NULL
       AND NOT ops.model_settings_revision_exists(NEW.model_settings_revision) THEN
        RAISE EXCEPTION 'retrieval embedding references an unknown model settings revision'
            USING ERRCODE = '23503';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS retrieval_embedding_model_settings_revision_guard
    ON retrieval.embedding_version;
CREATE TRIGGER retrieval_embedding_model_settings_revision_guard
    BEFORE INSERT OR UPDATE ON retrieval.embedding_version
    FOR EACH ROW EXECUTE FUNCTION retrieval.enforce_embedding_model_settings_revision();

CREATE OR REPLACE FUNCTION retrieval.enforce_index_model_settings_revision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    embedding_revision bigint;
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.model_settings_revision IS DISTINCT FROM OLD.model_settings_revision THEN
        RAISE EXCEPTION 'retrieval index model settings revision is immutable'
            USING ERRCODE = '55000';
    END IF;

    IF NEW.embedding_version_id IS NULL THEN
        IF NEW.model_settings_revision IS NOT NULL THEN
            RAISE EXCEPTION 'retrieval FTS-only index cannot carry an embedding model settings revision'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    SELECT model_settings_revision
      INTO embedding_revision
      FROM retrieval.embedding_version
     WHERE id = NEW.embedding_version_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'retrieval index references an unknown embedding version'
            USING ERRCODE = '23503';
    END IF;
    IF NEW.model_settings_revision IS DISTINCT FROM embedding_revision THEN
        RAISE EXCEPTION 'retrieval index model settings revision differs from embedding version'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS retrieval_index_model_settings_revision_guard
    ON retrieval.index_version;
CREATE TRIGGER retrieval_index_model_settings_revision_guard
    BEFORE INSERT OR UPDATE ON retrieval.index_version
    FOR EACH ROW EXECUTE FUNCTION retrieval.enforce_index_model_settings_revision();

CREATE OR REPLACE FUNCTION agent.enforce_model_run_model_settings_revision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    attempt_revision bigint;
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.model_settings_revision IS DISTINCT FROM OLD.model_settings_revision THEN
        RAISE EXCEPTION 'agent model run model settings revision is immutable'
            USING ERRCODE = '55000';
    END IF;

    SELECT model_settings_revision
      INTO attempt_revision
      FROM workflow.node_attempt
     WHERE id = NEW.node_attempt_id
       AND node_run_id = NEW.node_run_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'agent model run references an unknown workflow node attempt'
            USING ERRCODE = '23503';
    END IF;
    IF NEW.model_settings_revision IS DISTINCT FROM attempt_revision THEN
        RAISE EXCEPTION 'agent model run model settings revision differs from workflow node attempt'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS agent_model_run_model_settings_revision_guard
    ON agent.model_run;
CREATE TRIGGER agent_model_run_model_settings_revision_guard
    BEFORE INSERT OR UPDATE ON agent.model_run
    FOR EACH ROW EXECUTE FUNCTION agent.enforce_model_run_model_settings_revision();

INSERT INTO core.schema_meta (key, value)
VALUES ('model_settings_execution_provenance', 'w3')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
