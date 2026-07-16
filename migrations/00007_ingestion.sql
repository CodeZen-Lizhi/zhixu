-- +goose Up
CREATE SCHEMA IF NOT EXISTS ingestion;

CREATE TABLE IF NOT EXISTS ingestion.parse_projection (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    content_artifact_id uuid NOT NULL REFERENCES core.content_artifact(id) ON DELETE RESTRICT,
    parser_id text NOT NULL CHECK (btrim(parser_id) <> ''),
    parser_version text NOT NULL CHECK (btrim(parser_version) <> ''),
    parser_config_hash text NOT NULL CHECK (parser_config_hash ~ '^[0-9a-f]{64}$'),
    schema_version text NOT NULL CHECK (btrim(schema_version) <> ''),
    normalized_content_hash text NOT NULL CHECK (normalized_content_hash ~ '^[0-9a-f]{64}$'),
    warnings jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(warnings) = 'array'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_parse_projection_contract UNIQUE (
        content_artifact_id, parser_id, parser_version, parser_config_hash, schema_version
    )
);

CREATE TABLE IF NOT EXISTS ingestion.attempt (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    source_version_id uuid NOT NULL REFERENCES core.source_version(id) ON DELETE RESTRICT,
    workflow_run_id uuid REFERENCES workflow.run(id) ON DELETE RESTRICT,
    parse_projection_id uuid REFERENCES ingestion.parse_projection(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN ('validating','parsing','parsed','chunking','chunked','parse_failed','cancelled')),
    security_status text NOT NULL CHECK (security_status IN ('pending','passed','quarantined')),
    failure_stage text NOT NULL DEFAULT '',
    error_code text NOT NULL DEFAULT '',
    retryable boolean NOT NULL DEFAULT false,
    parser_id text NOT NULL CHECK (btrim(parser_id) <> ''),
    parser_version text NOT NULL CHECK (btrim(parser_version) <> ''),
    parser_config_hash text NOT NULL CHECK (parser_config_hash ~ '^[0-9a-f]{64}$'),
    chunk_strategy_version text NOT NULL CHECK (btrim(chunk_strategy_version) <> ''),
    schema_version text NOT NULL CHECK (btrim(schema_version) <> ''),
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key) <> ''),
    attempt_number integer NOT NULL CHECK (attempt_number > 0),
    warnings jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(warnings) = 'array'),
    started_at timestamptz NOT NULL,
    completed_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    CONSTRAINT ingestion_attempt_quarantine_state CHECK (security_status <> 'quarantined' OR status = 'validating'),
    CONSTRAINT ingestion_attempt_validating_security CHECK (status <> 'validating' OR security_status <> 'passed'),
    CONSTRAINT ingestion_attempt_processing_security CHECK (
        status NOT IN ('parsing','parsed','chunking','chunked') OR security_status = 'passed'
    ),
    CONSTRAINT ingestion_attempt_projection_state CHECK (
        status NOT IN ('parsed','chunking','chunked') OR parse_projection_id IS NOT NULL
    ),
    CONSTRAINT ingestion_attempt_completion CHECK (
        ((status IN ('chunked','parse_failed','cancelled') OR (status = 'validating' AND security_status = 'quarantined')) AND completed_at IS NOT NULL)
        OR (status NOT IN ('chunked','parse_failed','cancelled') AND NOT (status = 'validating' AND security_status = 'quarantined') AND completed_at IS NULL)
    ),
    CONSTRAINT uq_ingestion_attempt_idempotency UNIQUE (
        source_version_id, parser_id, parser_version, parser_config_hash,
        chunk_strategy_version, schema_version, idempotency_key
    )
);

CREATE TABLE IF NOT EXISTS ingestion.source_version_projection (
    source_version_id uuid NOT NULL REFERENCES core.source_version(id) ON DELETE RESTRICT,
    parse_projection_id uuid NOT NULL REFERENCES ingestion.parse_projection(id) ON DELETE RESTRICT,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    source_revision_id uuid,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (source_version_id, parse_projection_id)
);

CREATE TABLE IF NOT EXISTS ingestion.source_span (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    content_artifact_id uuid NOT NULL REFERENCES core.content_artifact(id) ON DELETE RESTRICT,
    parse_projection_id uuid NOT NULL REFERENCES ingestion.parse_projection(id) ON DELETE RESTRICT,
    span_type text NOT NULL CHECK (btrim(span_type) <> ''),
    start_line integer NOT NULL CHECK (start_line >= 1),
    end_line integer NOT NULL CHECK (end_line >= start_line),
    start_byte bigint NOT NULL CHECK (start_byte >= 0),
    end_byte bigint NOT NULL CHECK (end_byte >= start_byte),
    selector jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(selector) = 'object'),
    excerpt_hash text NOT NULL CHECK (excerpt_hash ~ '^[0-9a-f]{64}$'),
    parser_version text NOT NULL CHECK (btrim(parser_version) <> ''),
    schema_version text NOT NULL CHECK (btrim(schema_version) <> ''),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_source_span_range UNIQUE (
        parse_projection_id, span_type, start_byte, end_byte, schema_version
    )
);

CREATE TABLE IF NOT EXISTS ingestion.canonical_chunk (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    parse_projection_id uuid NOT NULL REFERENCES ingestion.parse_projection(id) ON DELETE RESTRICT,
    sequence integer NOT NULL CHECK (sequence >= 0),
    heading_path jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(heading_path) = 'array'),
    content text NOT NULL,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    source_span_id uuid NOT NULL REFERENCES ingestion.source_span(id) ON DELETE RESTRICT,
    byte_count bigint NOT NULL CHECK (byte_count >= 0),
    rune_count bigint NOT NULL CHECK (rune_count >= 0),
    parser_version text NOT NULL CHECK (btrim(parser_version) <> ''),
    chunk_strategy_version text NOT NULL CHECK (btrim(chunk_strategy_version) <> ''),
    schema_version text NOT NULL CHECK (btrim(schema_version) <> ''),
    atomic_oversized boolean NOT NULL DEFAULT false,
    status text NOT NULL CHECK (status IN ('active','superseded')),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_canonical_chunk_sequence UNIQUE (
        parse_projection_id, chunk_strategy_version, schema_version, sequence
    )
);

CREATE OR REPLACE FUNCTION ingestion.verify_workspace_ownership()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_workspace uuid;
    artifact_id uuid;
    artifact_size bigint;
    projection_parser_version text;
    projection_schema_version text;
    projection_parser_id text;
    projection_config_hash text;
    span_parser_version text;
    span_schema_version text;
BEGIN
    IF TG_TABLE_NAME = 'parse_projection' THEN
        SELECT workspace_id INTO actual_workspace FROM core.content_artifact WHERE id = NEW.content_artifact_id;
    ELSIF TG_TABLE_NAME = 'attempt' THEN
        SELECT s.workspace_id, sv.content_artifact_id
          INTO actual_workspace, artifact_id
          FROM core.source_version sv JOIN core.source s ON s.id = sv.source_id
         WHERE sv.id = NEW.source_version_id;
        IF NEW.workflow_run_id IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM workflow.run WHERE id = NEW.workflow_run_id AND workspace_id = NEW.workspace_id
        ) THEN
            RAISE EXCEPTION 'attempt workflow workspace mismatch' USING ERRCODE = '23514';
        END IF;
        IF NEW.parse_projection_id IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM ingestion.parse_projection
             WHERE id = NEW.parse_projection_id
               AND workspace_id = NEW.workspace_id
               AND content_artifact_id = artifact_id
        ) THEN
            RAISE EXCEPTION 'attempt projection source mismatch' USING ERRCODE = '23514';
        END IF;
        IF NEW.parse_projection_id IS NOT NULL THEN
            SELECT parser_id, parser_version, parser_config_hash, schema_version
              INTO projection_parser_id, projection_parser_version, projection_config_hash, projection_schema_version
              FROM ingestion.parse_projection WHERE id = NEW.parse_projection_id;
            IF NEW.parser_id <> projection_parser_id OR NEW.parser_version <> projection_parser_version
               OR NEW.parser_config_hash <> projection_config_hash OR NEW.schema_version <> projection_schema_version THEN
                RAISE EXCEPTION 'attempt parser contract mismatch' USING ERRCODE = '23514';
            END IF;
        END IF;
    ELSIF TG_TABLE_NAME = 'source_version_projection' THEN
        SELECT s.workspace_id, sv.content_artifact_id
          INTO actual_workspace, artifact_id
          FROM core.source_version sv JOIN core.source s ON s.id = sv.source_id
         WHERE sv.id = NEW.source_version_id;
        IF NOT EXISTS (
            SELECT 1 FROM ingestion.parse_projection
             WHERE id = NEW.parse_projection_id
               AND workspace_id = NEW.workspace_id
               AND content_artifact_id = artifact_id
        ) THEN
            RAISE EXCEPTION 'source version projection mismatch' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'source_span' THEN
        SELECT workspace_id, content_artifact_id, parser_version, schema_version
          INTO actual_workspace, artifact_id, projection_parser_version, projection_schema_version
          FROM ingestion.parse_projection WHERE id = NEW.parse_projection_id;
        IF artifact_id <> NEW.content_artifact_id THEN
            RAISE EXCEPTION 'span artifact projection mismatch' USING ERRCODE = '23514';
        END IF;
        SELECT byte_size INTO artifact_size FROM core.content_artifact WHERE id = NEW.content_artifact_id;
        IF artifact_size IS NULL OR NEW.end_byte > artifact_size THEN
            RAISE EXCEPTION 'span exceeds content artifact' USING ERRCODE = '23514';
        END IF;
        IF NEW.parser_version <> projection_parser_version OR NEW.schema_version <> projection_schema_version THEN
            RAISE EXCEPTION 'span parser/schema version mismatch' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'canonical_chunk' THEN
        SELECT workspace_id, parser_version, schema_version
          INTO actual_workspace, projection_parser_version, projection_schema_version
          FROM ingestion.parse_projection WHERE id = NEW.parse_projection_id;
        IF NOT EXISTS (
            SELECT 1 FROM ingestion.source_span
             WHERE id = NEW.source_span_id
               AND workspace_id = NEW.workspace_id
               AND parse_projection_id = NEW.parse_projection_id
        ) THEN
            RAISE EXCEPTION 'chunk span projection mismatch' USING ERRCODE = '23514';
        END IF;
        SELECT parser_version, schema_version INTO span_parser_version, span_schema_version
          FROM ingestion.source_span WHERE id = NEW.source_span_id;
        IF NEW.parser_version <> projection_parser_version OR NEW.schema_version <> projection_schema_version
           OR NEW.parser_version <> span_parser_version OR NEW.schema_version <> span_schema_version THEN
            RAISE EXCEPTION 'chunk parser/schema version mismatch' USING ERRCODE = '23514';
        END IF;
    END IF;
    IF actual_workspace IS NULL OR actual_workspace <> NEW.workspace_id THEN
        RAISE EXCEPTION 'ingestion workspace mismatch' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION ingestion.reject_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'ingestion record is immutable' USING ERRCODE = '55000';
END;
$$;

CREATE OR REPLACE FUNCTION ingestion.validate_attempt_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.source_version_id <> OLD.source_version_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.parser_id <> OLD.parser_id
       OR NEW.parser_version <> OLD.parser_version
       OR NEW.parser_config_hash <> OLD.parser_config_hash
       OR NEW.chunk_strategy_version <> OLD.chunk_strategy_version
       OR NEW.schema_version <> OLD.schema_version
       OR NEW.idempotency_key <> OLD.idempotency_key
       OR NEW.attempt_number <> OLD.attempt_number
       OR NEW.started_at <> OLD.started_at
       OR (OLD.parse_projection_id IS NOT NULL AND NEW.parse_projection_id IS DISTINCT FROM OLD.parse_projection_id)
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'attempt immutable fields or version changed' USING ERRCODE = '23514';
    END IF;
    IF OLD.status IN ('chunked','parse_failed','cancelled') OR (OLD.status = 'validating' AND OLD.security_status = 'quarantined') THEN
        RAISE EXCEPTION 'terminal attempt cannot transition' USING ERRCODE = '23514';
    END IF;
    IF NOT (
        (OLD.status = 'validating' AND OLD.security_status = 'pending' AND NEW.status = 'validating' AND NEW.security_status = 'quarantined')
        OR
        (OLD.status = 'validating' AND NEW.status IN ('parsing','parse_failed','cancelled'))
        OR (OLD.status = 'parsing' AND NEW.status IN ('parsed','parse_failed','cancelled'))
        OR (OLD.status = 'parsed' AND NEW.status IN ('chunking','parse_failed','cancelled'))
        OR (OLD.status = 'chunking' AND NEW.status IN ('chunked','parse_failed','cancelled'))
    ) THEN
        RAISE EXCEPTION 'invalid attempt state transition' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS parse_projection_verify_workspace ON ingestion.parse_projection;
CREATE TRIGGER parse_projection_verify_workspace BEFORE INSERT ON ingestion.parse_projection
    FOR EACH ROW EXECUTE FUNCTION ingestion.verify_workspace_ownership();
DROP TRIGGER IF EXISTS attempt_verify_workspace ON ingestion.attempt;
CREATE TRIGGER attempt_verify_workspace BEFORE INSERT OR UPDATE OF parse_projection_id, workflow_run_id ON ingestion.attempt
    FOR EACH ROW EXECUTE FUNCTION ingestion.verify_workspace_ownership();
DROP TRIGGER IF EXISTS source_version_projection_verify_workspace ON ingestion.source_version_projection;
CREATE TRIGGER source_version_projection_verify_workspace BEFORE INSERT ON ingestion.source_version_projection
    FOR EACH ROW EXECUTE FUNCTION ingestion.verify_workspace_ownership();
DROP TRIGGER IF EXISTS source_span_verify_workspace ON ingestion.source_span;
CREATE TRIGGER source_span_verify_workspace BEFORE INSERT ON ingestion.source_span
    FOR EACH ROW EXECUTE FUNCTION ingestion.verify_workspace_ownership();
DROP TRIGGER IF EXISTS canonical_chunk_verify_workspace ON ingestion.canonical_chunk;
CREATE TRIGGER canonical_chunk_verify_workspace BEFORE INSERT ON ingestion.canonical_chunk
    FOR EACH ROW EXECUTE FUNCTION ingestion.verify_workspace_ownership();

DROP TRIGGER IF EXISTS parse_projection_reject_mutation ON ingestion.parse_projection;
CREATE TRIGGER parse_projection_reject_mutation BEFORE UPDATE OR DELETE ON ingestion.parse_projection
    FOR EACH ROW EXECUTE FUNCTION ingestion.reject_mutation();
DROP TRIGGER IF EXISTS attempt_validate_transition ON ingestion.attempt;
CREATE TRIGGER attempt_validate_transition BEFORE UPDATE ON ingestion.attempt
    FOR EACH ROW EXECUTE FUNCTION ingestion.validate_attempt_transition();
DROP TRIGGER IF EXISTS attempt_reject_delete ON ingestion.attempt;
CREATE TRIGGER attempt_reject_delete BEFORE DELETE ON ingestion.attempt
    FOR EACH ROW EXECUTE FUNCTION ingestion.reject_mutation();
DROP TRIGGER IF EXISTS source_version_projection_reject_mutation ON ingestion.source_version_projection;
CREATE TRIGGER source_version_projection_reject_mutation BEFORE UPDATE OR DELETE ON ingestion.source_version_projection
    FOR EACH ROW EXECUTE FUNCTION ingestion.reject_mutation();
DROP TRIGGER IF EXISTS source_span_reject_mutation ON ingestion.source_span;
CREATE TRIGGER source_span_reject_mutation BEFORE UPDATE OR DELETE ON ingestion.source_span
    FOR EACH ROW EXECUTE FUNCTION ingestion.reject_mutation();
DROP TRIGGER IF EXISTS canonical_chunk_reject_mutation ON ingestion.canonical_chunk;
CREATE TRIGGER canonical_chunk_reject_mutation BEFORE UPDATE OR DELETE ON ingestion.canonical_chunk
    FOR EACH ROW EXECUTE FUNCTION ingestion.reject_mutation();

CREATE INDEX IF NOT EXISTS idx_ingestion_attempt_workspace_started
    ON ingestion.attempt (workspace_id, started_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_source_span_projection_sequence
    ON ingestion.source_span (parse_projection_id, start_byte, id);
CREATE INDEX IF NOT EXISTS idx_canonical_chunk_workspace_status
    ON ingestion.canonical_chunk (workspace_id, status, parse_projection_id, sequence);

INSERT INTO core.schema_meta (key, value) VALUES ('ingestion_projection', 'm5-parser')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- +goose Down
DROP TRIGGER IF EXISTS canonical_chunk_reject_mutation ON ingestion.canonical_chunk;
DROP TRIGGER IF EXISTS source_span_reject_mutation ON ingestion.source_span;
DROP TRIGGER IF EXISTS source_version_projection_reject_mutation ON ingestion.source_version_projection;
DROP TRIGGER IF EXISTS attempt_reject_delete ON ingestion.attempt;
DROP TRIGGER IF EXISTS attempt_validate_transition ON ingestion.attempt;
DROP TRIGGER IF EXISTS parse_projection_reject_mutation ON ingestion.parse_projection;
DROP TRIGGER IF EXISTS canonical_chunk_verify_workspace ON ingestion.canonical_chunk;
DROP TRIGGER IF EXISTS source_span_verify_workspace ON ingestion.source_span;
DROP TRIGGER IF EXISTS attempt_verify_workspace ON ingestion.attempt;
DROP TRIGGER IF EXISTS source_version_projection_verify_workspace ON ingestion.source_version_projection;
DROP TRIGGER IF EXISTS parse_projection_verify_workspace ON ingestion.parse_projection;
DROP FUNCTION IF EXISTS ingestion.reject_mutation();
DROP FUNCTION IF EXISTS ingestion.validate_attempt_transition();
DROP FUNCTION IF EXISTS ingestion.verify_workspace_ownership();
DROP TABLE IF EXISTS ingestion.canonical_chunk;
DROP TABLE IF EXISTS ingestion.source_span;
DROP TABLE IF EXISTS ingestion.source_version_projection;
DROP TABLE IF EXISTS ingestion.attempt;
DROP TABLE IF EXISTS ingestion.parse_projection;
DROP SCHEMA IF EXISTS ingestion;
DELETE FROM core.schema_meta WHERE key = 'ingestion_projection';
