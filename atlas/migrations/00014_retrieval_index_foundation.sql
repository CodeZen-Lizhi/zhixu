CREATE EXTENSION IF NOT EXISTS pg_trgm;

ALTER TABLE ingestion.canonical_chunk
    DROP CONSTRAINT IF EXISTS uq_canonical_chunk_id_workspace,
    ADD CONSTRAINT uq_canonical_chunk_id_workspace UNIQUE (id, workspace_id);

CREATE TABLE IF NOT EXISTS retrieval.embedding_version (
    id uuid PRIMARY KEY,
    provider text NOT NULL CHECK (btrim(provider) <> '' AND char_length(provider) <= 128),
    adapter_name text NOT NULL CHECK (btrim(adapter_name) <> '' AND char_length(adapter_name) <= 128),
    adapter_version text NOT NULL CHECK (btrim(adapter_version) <> '' AND char_length(adapter_version) <= 128),
    model text NOT NULL CHECK (btrim(model) <> '' AND char_length(model) <= 256),
    dimensions integer NOT NULL CHECK (dimensions > 0),
    normalization text NOT NULL CHECK (normalization IN ('none', 'l2')),
    distance_metric text NOT NULL CHECK (distance_metric IN ('cosine', 'inner_product', 'euclidean')),
    config_hash text NOT NULL CHECK (config_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_retrieval_embedding_version_contract UNIQUE (provider, model, dimensions, config_hash)
);

CREATE TABLE IF NOT EXISTS retrieval.index_version (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    embedding_version_id uuid REFERENCES retrieval.embedding_version(id) ON DELETE RESTRICT,
    tokenizer_id text NOT NULL CHECK (btrim(tokenizer_id) <> '' AND char_length(tokenizer_id) <= 128),
    tokenizer_version text NOT NULL CHECK (btrim(tokenizer_version) <> '' AND char_length(tokenizer_version) <= 128),
    tokenizer_config_hash text NOT NULL CHECK (tokenizer_config_hash ~ '^[0-9a-f]{64}$'),
    fusion_config jsonb NOT NULL CHECK (jsonb_typeof(fusion_config) = 'object'),
    source_snapshot_ref text NOT NULL CHECK (btrim(source_snapshot_ref) <> '' AND char_length(source_snapshot_ref) <= 512),
    manifest_hash text NOT NULL CHECK (manifest_hash ~ '^[0-9a-f]{64}$'),
    expected_chunk_count bigint NOT NULL CHECK (expected_chunk_count >= 0),
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key) <> '' AND char_length(idempotency_key) <= 128),
    status text NOT NULL CHECK (status IN ('building', 'ready', 'active', 'retiring', 'archived', 'failed')),
    degraded_capabilities jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (
        degraded_capabilities = '[]'::jsonb OR degraded_capabilities = '["vector"]'::jsonb
    ),
    failure_code text CHECK (failure_code IS NULL OR (btrim(failure_code) <> '' AND char_length(failure_code) <= 128)),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    built_at timestamptz,
    activated_at timestamptz,
    retired_at timestamptz,
    archived_at timestamptz,
    failed_at timestamptz,
    CONSTRAINT uq_retrieval_index_version_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_retrieval_index_version_embedding_binding UNIQUE (id, workspace_id, embedding_version_id),
    CONSTRAINT uq_retrieval_index_version_idempotency UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT retrieval_index_version_fts_only_degraded CHECK (
        embedding_version_id IS NOT NULL OR degraded_capabilities = '["vector"]'::jsonb
    ),
    CONSTRAINT retrieval_index_version_failure_consistent CHECK (
        (status = 'failed' AND failure_code IS NOT NULL AND failed_at IS NOT NULL)
        OR (status <> 'failed' AND failure_code IS NULL)
    ),
    CONSTRAINT retrieval_index_version_lifecycle_times CHECK (
        updated_at >= created_at
        AND (built_at IS NULL OR built_at >= created_at)
        AND (activated_at IS NULL OR activated_at >= created_at)
        AND (retired_at IS NULL OR retired_at >= created_at)
        AND (archived_at IS NULL OR archived_at >= created_at)
        AND (failed_at IS NULL OR failed_at >= created_at)
        AND (status NOT IN ('ready', 'active', 'retiring', 'archived') OR built_at IS NOT NULL)
        AND (status NOT IN ('active', 'retiring', 'archived') OR activated_at IS NOT NULL)
        AND (status NOT IN ('retiring', 'archived') OR retired_at IS NOT NULL)
        AND (status <> 'archived' OR archived_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_retrieval_index_version_active
    ON retrieval.index_version (workspace_id)
    WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_retrieval_index_version_workspace_status
    ON retrieval.index_version (workspace_id, status, created_at DESC, id);

CREATE TABLE IF NOT EXISTS retrieval.index_manifest_chunk (
    index_version_id uuid NOT NULL,
    chunk_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    sequence integer NOT NULL CHECK (sequence >= 0),
    parser_version text NOT NULL CHECK (btrim(parser_version) <> ''),
    chunk_strategy_version text NOT NULL CHECK (btrim(chunk_strategy_version) <> ''),
    schema_version text NOT NULL CHECK (btrim(schema_version) <> ''),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (index_version_id, chunk_id),
    CONSTRAINT fk_retrieval_manifest_index_workspace
        FOREIGN KEY (index_version_id, workspace_id)
        REFERENCES retrieval.index_version(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_retrieval_manifest_chunk_workspace
        FOREIGN KEY (chunk_id, workspace_id)
        REFERENCES ingestion.canonical_chunk(id, workspace_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_retrieval_manifest_workspace_index_sequence
    ON retrieval.index_manifest_chunk (workspace_id, index_version_id, sequence, chunk_id);

CREATE TABLE IF NOT EXISTS retrieval.chunk_projection (
    index_version_id uuid NOT NULL,
    chunk_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    embedding_version_id uuid REFERENCES retrieval.embedding_version(id) ON DELETE RESTRICT,
    search_vector tsvector,
    embedding vector,
    token_count integer NOT NULL CHECK (token_count >= 0),
    lexical_status text NOT NULL CHECK (lexical_status IN ('pending', 'ready', 'failed')),
    vector_status text NOT NULL CHECK (vector_status IN ('disabled', 'pending', 'ready', 'skipped_oversized', 'failed')),
    failure_code text CHECK (failure_code IS NULL OR (btrim(failure_code) <> '' AND char_length(failure_code) <= 128)),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (index_version_id, chunk_id),
    CONSTRAINT fk_retrieval_projection_manifest
        FOREIGN KEY (index_version_id, chunk_id)
        REFERENCES retrieval.index_manifest_chunk(index_version_id, chunk_id) ON DELETE RESTRICT,
    CONSTRAINT fk_retrieval_projection_index_workspace
        FOREIGN KEY (index_version_id, workspace_id)
        REFERENCES retrieval.index_version(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_retrieval_projection_index_embedding
        FOREIGN KEY (index_version_id, workspace_id, embedding_version_id)
        REFERENCES retrieval.index_version(id, workspace_id, embedding_version_id) ON DELETE RESTRICT,
    CONSTRAINT fk_retrieval_projection_chunk_workspace
        FOREIGN KEY (chunk_id, workspace_id)
        REFERENCES ingestion.canonical_chunk(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT retrieval_projection_time_order CHECK (updated_at >= created_at),
    CONSTRAINT retrieval_projection_lexical_payload CHECK (
        (lexical_status = 'ready' AND search_vector IS NOT NULL)
        OR (lexical_status <> 'ready' AND search_vector IS NULL)
    ),
    CONSTRAINT retrieval_projection_vector_payload CHECK (
        (vector_status = 'ready' AND embedding_version_id IS NOT NULL AND embedding IS NOT NULL)
        OR (vector_status <> 'ready' AND embedding IS NULL)
    ),
    CONSTRAINT retrieval_projection_failure_consistent CHECK (
        ((lexical_status = 'failed' OR vector_status IN ('skipped_oversized', 'failed')) AND failure_code IS NOT NULL)
        OR (
            lexical_status <> 'failed'
            AND vector_status NOT IN ('skipped_oversized', 'failed')
            AND failure_code IS NULL
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_retrieval_projection_workspace_index_chunk
    ON retrieval.chunk_projection (workspace_id, index_version_id, chunk_id);

CREATE INDEX IF NOT EXISTS idx_retrieval_projection_search_vector
    ON retrieval.chunk_projection USING gin (search_vector);

CREATE INDEX IF NOT EXISTS idx_ingestion_canonical_chunk_content_trgm
    ON ingestion.canonical_chunk USING gin (content gin_trgm_ops);

CREATE TABLE IF NOT EXISTS retrieval.index_activation (
    id uuid PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('activate', 'rollback')),
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    target_index_version_id uuid NOT NULL,
    previous_index_version_id uuid,
    target_version bigint NOT NULL CHECK (target_version > 1),
    previous_version bigint CHECK (previous_version IS NULL OR previous_version > 1),
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key) <> '' AND char_length(idempotency_key) <= 128),
    reason_code text NOT NULL CHECK (btrim(reason_code) <> '' AND char_length(reason_code) <= 128),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_retrieval_index_activation_idempotency UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT uq_retrieval_index_activation_target_version UNIQUE (target_index_version_id, target_version),
    CONSTRAINT fk_retrieval_activation_target_workspace
        FOREIGN KEY (target_index_version_id, workspace_id)
        REFERENCES retrieval.index_version(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_retrieval_activation_previous_workspace
        FOREIGN KEY (previous_index_version_id, workspace_id)
        REFERENCES retrieval.index_version(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT retrieval_index_activation_previous_pair CHECK (
        (previous_index_version_id IS NULL AND previous_version IS NULL)
        OR (previous_index_version_id IS NOT NULL AND previous_version IS NOT NULL)
    ),
    CONSTRAINT retrieval_index_activation_distinct_indexes CHECK (
        previous_index_version_id IS NULL OR previous_index_version_id <> target_index_version_id
    )
);

CREATE INDEX IF NOT EXISTS idx_retrieval_index_activation_workspace_created
    ON retrieval.index_activation (workspace_id, created_at DESC, id);

-- Retrieval version and manifest facts are immutable; correction creates a new version.
CREATE OR REPLACE FUNCTION retrieval.reject_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'retrieval record is immutable' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS retrieval_embedding_version_reject_mutation ON retrieval.embedding_version;
CREATE TRIGGER retrieval_embedding_version_reject_mutation
    BEFORE UPDATE OR DELETE ON retrieval.embedding_version
    FOR EACH ROW EXECUTE FUNCTION retrieval.reject_immutable_mutation();

CREATE OR REPLACE FUNCTION retrieval.validate_manifest_chunk_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    existing_row retrieval.index_manifest_chunk%ROWTYPE;
    index_workspace_id uuid;
    index_status text;
    chunk_workspace_id uuid;
    chunk_content_hash text;
    chunk_sequence integer;
    chunk_parser_version text;
    chunk_strategy_version text;
    chunk_schema_version text;
BEGIN
    SELECT manifest.index_version_id, manifest.chunk_id, manifest.workspace_id,
           manifest.content_hash, manifest.sequence, manifest.parser_version,
           manifest.chunk_strategy_version, manifest.schema_version, manifest.created_at
      INTO existing_row
      FROM retrieval.index_manifest_chunk manifest
     WHERE manifest.index_version_id = NEW.index_version_id
       AND manifest.chunk_id = NEW.chunk_id;
    IF FOUND THEN
        IF existing_row.workspace_id = NEW.workspace_id
           AND existing_row.content_hash = NEW.content_hash
           AND existing_row.sequence = NEW.sequence
           AND existing_row.parser_version = NEW.parser_version
           AND existing_row.chunk_strategy_version = NEW.chunk_strategy_version
           AND existing_row.schema_version = NEW.schema_version THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'retrieval manifest replay binding mismatch' USING ERRCODE = '23514';
    END IF;

    SELECT index_version.workspace_id, index_version.status
      INTO index_workspace_id, index_status
      FROM retrieval.index_version index_version
     WHERE index_version.id = NEW.index_version_id
     FOR UPDATE;
    SELECT chunk.workspace_id, chunk.content_hash, chunk.sequence, chunk.parser_version,
           chunk.chunk_strategy_version, chunk.schema_version
      INTO chunk_workspace_id, chunk_content_hash, chunk_sequence, chunk_parser_version,
           chunk_strategy_version, chunk_schema_version
      FROM ingestion.canonical_chunk chunk
     WHERE chunk.id = NEW.chunk_id;

    IF index_workspace_id IS NULL OR index_workspace_id <> NEW.workspace_id OR index_status <> 'building' THEN
        RAISE EXCEPTION 'retrieval manifest index binding mismatch' USING ERRCODE = '23514';
    END IF;
    IF chunk_workspace_id IS NULL OR chunk_workspace_id <> NEW.workspace_id
       OR chunk_content_hash <> NEW.content_hash
       OR chunk_sequence <> NEW.sequence
       OR chunk_parser_version <> NEW.parser_version
       OR chunk_strategy_version <> NEW.chunk_strategy_version
       OR chunk_schema_version <> NEW.schema_version THEN
        RAISE EXCEPTION 'retrieval manifest canonical chunk binding mismatch' USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS retrieval_manifest_validate_insert ON retrieval.index_manifest_chunk;
CREATE TRIGGER retrieval_manifest_validate_insert
    BEFORE INSERT ON retrieval.index_manifest_chunk
    FOR EACH ROW EXECUTE FUNCTION retrieval.validate_manifest_chunk_insert();

DROP TRIGGER IF EXISTS retrieval_manifest_reject_mutation ON retrieval.index_manifest_chunk;
CREATE TRIGGER retrieval_manifest_reject_mutation
    BEFORE UPDATE OR DELETE ON retrieval.index_manifest_chunk
    FOR EACH ROW EXECUTE FUNCTION retrieval.reject_immutable_mutation();

CREATE OR REPLACE FUNCTION retrieval.validate_projection_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    existing_row retrieval.chunk_projection%ROWTYPE;
    index_workspace_id uuid;
    index_status text;
    index_embedding_version_id uuid;
    registered_dimensions integer;
    chunk_oversized boolean;
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT index_version_id, chunk_id, workspace_id, embedding_version_id,
               search_vector, embedding, token_count, lexical_status, vector_status,
               failure_code, created_at, updated_at
          INTO existing_row
          FROM retrieval.chunk_projection
         WHERE index_version_id = NEW.index_version_id
           AND chunk_id = NEW.chunk_id;
        IF FOUND THEN
            IF existing_row.workspace_id = NEW.workspace_id
               AND existing_row.embedding_version_id IS NOT DISTINCT FROM NEW.embedding_version_id
               AND existing_row.search_vector IS NOT DISTINCT FROM NEW.search_vector
               AND existing_row.embedding IS NOT DISTINCT FROM NEW.embedding
               AND existing_row.token_count = NEW.token_count
               AND existing_row.lexical_status = NEW.lexical_status
               AND existing_row.vector_status = NEW.vector_status
               AND existing_row.failure_code IS NOT DISTINCT FROM NEW.failure_code THEN
                RETURN NEW;
            END IF;
            RAISE EXCEPTION 'retrieval projection replay binding mismatch' USING ERRCODE = '23514';
        END IF;
    END IF;

    SELECT workspace_id, status, embedding_version_id
      INTO index_workspace_id, index_status, index_embedding_version_id
      FROM retrieval.index_version
     WHERE id = NEW.index_version_id
     FOR UPDATE;
    IF index_workspace_id IS NULL OR index_workspace_id <> NEW.workspace_id OR index_status <> 'building' THEN
        RAISE EXCEPTION 'retrieval projection index binding mismatch' USING ERRCODE = '23514';
    END IF;
    IF NEW.embedding_version_id IS DISTINCT FROM index_embedding_version_id THEN
        RAISE EXCEPTION 'retrieval projection embedding version mismatch' USING ERRCODE = '23514';
    END IF;
    IF index_embedding_version_id IS NULL AND NEW.vector_status <> 'disabled' THEN
        RAISE EXCEPTION 'retrieval FTS-only projection must disable vector capability' USING ERRCODE = '23514';
    END IF;
    IF index_embedding_version_id IS NOT NULL AND NEW.vector_status = 'disabled' THEN
        RAISE EXCEPTION 'retrieval vector projection cannot be disabled' USING ERRCODE = '23514';
    END IF;

    IF NEW.vector_status = 'ready' THEN
        SELECT dimensions INTO registered_dimensions
          FROM retrieval.embedding_version
         WHERE id = NEW.embedding_version_id;
        IF registered_dimensions IS NULL
           OR vector_dims(NEW.embedding) <> registered_dimensions
           OR vector_norm(NEW.embedding) <= 0 THEN
            RAISE EXCEPTION 'retrieval projection vector is invalid' USING ERRCODE = '23514';
        END IF;
    END IF;

    IF NEW.vector_status = 'skipped_oversized' THEN
        SELECT atomic_oversized INTO chunk_oversized
          FROM ingestion.canonical_chunk
         WHERE id = NEW.chunk_id
           AND workspace_id = NEW.workspace_id;
        IF chunk_oversized IS DISTINCT FROM true THEN
            RAISE EXCEPTION 'retrieval projection may skip only an oversized canonical chunk' USING ERRCODE = '23514';
        END IF;
    END IF;

    IF TG_OP = 'UPDATE' THEN
        IF NEW.index_version_id <> OLD.index_version_id
           OR NEW.chunk_id <> OLD.chunk_id
           OR NEW.workspace_id <> OLD.workspace_id
           OR NEW.embedding_version_id IS DISTINCT FROM OLD.embedding_version_id
           OR NEW.token_count <> OLD.token_count
           OR NEW.created_at <> OLD.created_at
           OR NEW.updated_at < OLD.updated_at THEN
            RAISE EXCEPTION 'retrieval projection immutable binding changed' USING ERRCODE = '23514';
        END IF;
        IF NEW.lexical_status <> OLD.lexical_status
           AND NOT (OLD.lexical_status = 'pending' AND NEW.lexical_status IN ('ready', 'failed')) THEN
            RAISE EXCEPTION 'retrieval lexical projection transition is invalid' USING ERRCODE = '23514';
        END IF;
        IF NEW.vector_status <> OLD.vector_status
           AND NOT (OLD.vector_status = 'pending' AND NEW.vector_status IN ('ready', 'skipped_oversized', 'failed')) THEN
            RAISE EXCEPTION 'retrieval vector projection transition is invalid' USING ERRCODE = '23514';
        END IF;
        IF OLD.search_vector IS NOT NULL AND NEW.search_vector IS DISTINCT FROM OLD.search_vector THEN
            RAISE EXCEPTION 'retrieval lexical projection result is immutable' USING ERRCODE = '23514';
        END IF;
        IF OLD.embedding IS NOT NULL AND NEW.embedding IS DISTINCT FROM OLD.embedding THEN
            RAISE EXCEPTION 'retrieval vector projection result is immutable' USING ERRCODE = '23514';
        END IF;
        IF OLD.failure_code IS NOT NULL AND NEW.failure_code IS DISTINCT FROM OLD.failure_code THEN
            RAISE EXCEPTION 'retrieval projection failure code is immutable' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS retrieval_projection_validate_write ON retrieval.chunk_projection;
CREATE TRIGGER retrieval_projection_validate_write
    BEFORE INSERT OR UPDATE ON retrieval.chunk_projection
    FOR EACH ROW EXECUTE FUNCTION retrieval.validate_projection_write();

DROP TRIGGER IF EXISTS retrieval_projection_reject_delete ON retrieval.chunk_projection;
CREATE TRIGGER retrieval_projection_reject_delete
    BEFORE DELETE ON retrieval.chunk_projection
    FOR EACH ROW EXECUTE FUNCTION retrieval.reject_immutable_mutation();

CREATE OR REPLACE FUNCTION retrieval.validate_index_version_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    manifest_count bigint;
    projection_count bigint;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'building' OR NEW.version <> 1
           OR NEW.built_at IS NOT NULL OR NEW.activated_at IS NOT NULL
           OR NEW.retired_at IS NOT NULL OR NEW.archived_at IS NOT NULL
           OR NEW.failed_at IS NOT NULL OR NEW.failure_code IS NOT NULL THEN
            RAISE EXCEPTION 'retrieval index must be inserted as building version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.embedding_version_id IS DISTINCT FROM OLD.embedding_version_id
       OR NEW.tokenizer_id <> OLD.tokenizer_id
       OR NEW.tokenizer_version <> OLD.tokenizer_version
       OR NEW.tokenizer_config_hash <> OLD.tokenizer_config_hash
       OR NEW.fusion_config <> OLD.fusion_config
       OR NEW.source_snapshot_ref <> OLD.source_snapshot_ref
       OR NEW.manifest_hash <> OLD.manifest_hash
       OR NEW.expected_chunk_count <> OLD.expected_chunk_count
       OR NEW.idempotency_key <> OLD.idempotency_key
       OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'retrieval index immutable field or version violation' USING ERRCODE = '23514';
    END IF;
    IF OLD.status <> 'building'
       AND NEW.degraded_capabilities <> OLD.degraded_capabilities THEN
        RAISE EXCEPTION 'retrieval index degraded capabilities are immutable after build' USING ERRCODE = '23514';
    END IF;

    IF NOT (
        (OLD.status = 'building' AND NEW.status IN ('ready', 'failed'))
        OR (OLD.status = 'ready' AND NEW.status = 'active')
        OR (OLD.status = 'active' AND NEW.status = 'retiring')
        OR (OLD.status = 'retiring' AND NEW.status IN ('active', 'archived'))
    ) THEN
        RAISE EXCEPTION 'retrieval index status transition is invalid' USING ERRCODE = '23514';
    END IF;

    IF NEW.status = 'active' AND NOT EXISTS (
        SELECT 1
          FROM retrieval.index_activation activation
         WHERE activation.workspace_id = NEW.workspace_id
           AND activation.target_index_version_id = NEW.id
           AND activation.target_version = NEW.version
    ) THEN
        RAISE EXCEPTION 'retrieval index may enter active only through activation' USING ERRCODE = '55000';
    END IF;

    IF NEW.status = 'ready' THEN
        SELECT count(*) INTO manifest_count
          FROM retrieval.index_manifest_chunk
         WHERE index_version_id = NEW.id;
        SELECT count(*) INTO projection_count
          FROM retrieval.chunk_projection
         WHERE index_version_id = NEW.id;
        IF manifest_count <> NEW.expected_chunk_count OR projection_count <> NEW.expected_chunk_count
           OR EXISTS (
                SELECT 1
                  FROM retrieval.index_manifest_chunk manifest
                  LEFT JOIN retrieval.chunk_projection projection
                    ON projection.index_version_id = manifest.index_version_id
                   AND projection.chunk_id = manifest.chunk_id
                 WHERE manifest.index_version_id = NEW.id
                   AND (projection.chunk_id IS NULL OR projection.lexical_status <> 'ready')
           )
           OR (
                NEW.embedding_version_id IS NULL
                AND EXISTS (
                    SELECT 1 FROM retrieval.chunk_projection
                     WHERE index_version_id = NEW.id AND vector_status <> 'disabled'
                )
           )
           OR (
                NEW.embedding_version_id IS NOT NULL
                AND NEW.degraded_capabilities = '[]'::jsonb
                AND EXISTS (
                    SELECT 1 FROM retrieval.chunk_projection
                     WHERE index_version_id = NEW.id AND vector_status <> 'ready'
                )
           )
           OR (
                NEW.embedding_version_id IS NOT NULL
                AND NEW.degraded_capabilities = '["vector"]'::jsonb
                AND EXISTS (
                    SELECT 1 FROM retrieval.chunk_projection
                     WHERE index_version_id = NEW.id AND vector_status NOT IN ('ready', 'skipped_oversized', 'failed')
                )
           )
           OR (
                NEW.embedding_version_id IS NOT NULL
                AND NEW.degraded_capabilities = '["vector"]'::jsonb
                AND NOT EXISTS (
                    SELECT 1 FROM retrieval.chunk_projection
                     WHERE index_version_id = NEW.id AND vector_status <> 'ready'
                )
           ) THEN
            RAISE EXCEPTION 'retrieval index is not complete enough to become ready' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS retrieval_index_version_validate_write ON retrieval.index_version;
CREATE TRIGGER retrieval_index_version_validate_write
    BEFORE INSERT OR UPDATE ON retrieval.index_version
    FOR EACH ROW EXECUTE FUNCTION retrieval.validate_index_version_write();

DROP TRIGGER IF EXISTS retrieval_index_version_reject_delete ON retrieval.index_version;
CREATE TRIGGER retrieval_index_version_reject_delete
    BEFORE DELETE ON retrieval.index_version
    FOR EACH ROW EXECUTE FUNCTION retrieval.reject_immutable_mutation();

-- Activation rows are inserted before status updates and proven at transaction end.
CREATE OR REPLACE FUNCTION retrieval.validate_index_activation_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    existing_activation retrieval.index_activation%ROWTYPE;
    target_status text;
    target_current_version bigint;
    previous_status text;
    previous_current_version bigint;
BEGIN
    SELECT id, kind, workspace_id, target_index_version_id, previous_index_version_id,
           target_version, previous_version, idempotency_key, reason_code, created_at
      INTO existing_activation
      FROM retrieval.index_activation
     WHERE workspace_id = NEW.workspace_id
       AND idempotency_key = NEW.idempotency_key;
    IF FOUND THEN
        IF existing_activation.kind = NEW.kind
           AND existing_activation.target_index_version_id = NEW.target_index_version_id
           AND existing_activation.previous_index_version_id IS NOT DISTINCT FROM NEW.previous_index_version_id
           AND existing_activation.target_version = NEW.target_version
           AND existing_activation.previous_version IS NOT DISTINCT FROM NEW.previous_version
           AND existing_activation.reason_code = NEW.reason_code THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'retrieval activation replay binding mismatch' USING ERRCODE = '23514';
    END IF;

    PERFORM id
      FROM retrieval.index_version
     WHERE id IN (NEW.target_index_version_id, NEW.previous_index_version_id)
     ORDER BY id
     FOR UPDATE;

    SELECT status, version INTO target_status, target_current_version
      FROM retrieval.index_version
     WHERE id = NEW.target_index_version_id
       AND workspace_id = NEW.workspace_id;
    IF target_status IS NULL
       OR target_status NOT IN ('ready', 'retiring')
       OR NEW.target_version <> target_current_version + 1 THEN
        RAISE EXCEPTION 'retrieval activation target binding mismatch' USING ERRCODE = '23514';
    END IF;

    IF NEW.previous_index_version_id IS NULL THEN
        IF NEW.kind <> 'activate' OR target_status <> 'ready' THEN
            RAISE EXCEPTION 'retrieval first activation target must be ready' USING ERRCODE = '23514';
        END IF;
        PERFORM id
          FROM retrieval.index_version
         WHERE workspace_id = NEW.workspace_id
           AND status = 'active'
         FOR UPDATE;
        IF FOUND THEN
            RAISE EXCEPTION 'retrieval first activation requires no current active index' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.kind = 'activate' AND target_status <> 'ready' THEN
        RAISE EXCEPTION 'retrieval activation target must be ready' USING ERRCODE = '23514';
    END IF;
    IF NEW.kind = 'rollback' AND target_status <> 'retiring' THEN
        RAISE EXCEPTION 'retrieval rollback target must be retiring' USING ERRCODE = '23514';
    END IF;

    SELECT status, version INTO previous_status, previous_current_version
      FROM retrieval.index_version
     WHERE id = NEW.previous_index_version_id
       AND workspace_id = NEW.workspace_id;
    IF previous_status IS NULL
       OR previous_status <> 'active'
       OR NEW.previous_version <> previous_current_version + 1 THEN
        RAISE EXCEPTION 'retrieval activation previous index binding mismatch' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION retrieval.verify_index_activation_commit()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM retrieval.index_version
         WHERE id = NEW.target_index_version_id
           AND workspace_id = NEW.workspace_id
           AND status = 'active'
           AND version = NEW.target_version
    ) OR (
        NEW.previous_index_version_id IS NOT NULL
        AND NOT EXISTS (
            SELECT 1
              FROM retrieval.index_version
             WHERE id = NEW.previous_index_version_id
               AND workspace_id = NEW.workspace_id
               AND status = 'retiring'
               AND version = NEW.previous_version
        )
    ) THEN
        RAISE EXCEPTION 'retrieval activation did not commit an atomic index switch' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS retrieval_index_activation_validate_insert ON retrieval.index_activation;
CREATE TRIGGER retrieval_index_activation_validate_insert
    BEFORE INSERT ON retrieval.index_activation
    FOR EACH ROW EXECUTE FUNCTION retrieval.validate_index_activation_insert();

DROP TRIGGER IF EXISTS retrieval_index_activation_verify_commit ON retrieval.index_activation;
CREATE CONSTRAINT TRIGGER retrieval_index_activation_verify_commit
    AFTER INSERT ON retrieval.index_activation
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION retrieval.verify_index_activation_commit();

DROP TRIGGER IF EXISTS retrieval_index_activation_reject_mutation ON retrieval.index_activation;
CREATE TRIGGER retrieval_index_activation_reject_mutation
    BEFORE UPDATE OR DELETE ON retrieval.index_activation
    FOR EACH ROW EXECUTE FUNCTION retrieval.reject_immutable_mutation();

INSERT INTO core.schema_meta (key, value)
VALUES ('retrieval_index_foundation', 'm6-a')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
