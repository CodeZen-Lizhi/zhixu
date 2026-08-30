CREATE INDEX IF NOT EXISTS idx_workflow_outbox_reindex_unpublished
    ON workflow.outbox_event(occurred_at, id)
    WHERE published_at IS NULL
      AND event_type = 'retrieval.revision.reindex_requested';

ALTER TABLE core.source
    DROP CONSTRAINT IF EXISTS uq_source_id_workspace,
    ADD CONSTRAINT uq_source_id_workspace UNIQUE (id, workspace_id);

ALTER TABLE core.source_version
    DROP CONSTRAINT IF EXISTS uq_source_version_id_source,
    ADD CONSTRAINT uq_source_version_id_source UNIQUE (id, source_id);

ALTER TABLE ingestion.parse_projection
    DROP CONSTRAINT IF EXISTS uq_parse_projection_id_workspace,
    ADD CONSTRAINT uq_parse_projection_id_workspace UNIQUE (id, workspace_id);

ALTER TABLE ingestion.source_version_projection
    DROP CONSTRAINT IF EXISTS uq_source_version_projection_workspace,
    ADD CONSTRAINT uq_source_version_projection_workspace UNIQUE (source_version_id, parse_projection_id, workspace_id);

ALTER TABLE workflow.outbox_event
    DROP CONSTRAINT IF EXISTS uq_workflow_outbox_id_workspace,
    ADD CONSTRAINT uq_workflow_outbox_id_workspace UNIQUE (id, workspace_id);

ALTER TABLE change_control.writeback_execution
    DROP CONSTRAINT IF EXISTS uq_writeback_execution_id_workspace,
    ADD CONSTRAINT uq_writeback_execution_id_workspace UNIQUE (id, workspace_id);

ALTER TABLE ingestion.canonical_chunk
    DROP CONSTRAINT IF EXISTS uq_canonical_chunk_manifest_binding,
    ADD CONSTRAINT uq_canonical_chunk_manifest_binding UNIQUE (
        id, workspace_id, content_hash, sequence, parser_version, chunk_strategy_version, schema_version
    );

ALTER TABLE retrieval.index_version
    ADD COLUMN IF NOT EXISTS source_manifest_hash text,
    ADD COLUMN IF NOT EXISTS expected_source_count bigint,
    ADD COLUMN IF NOT EXISTS source_parser_id text,
    ADD COLUMN IF NOT EXISTS source_parser_version text,
    ADD COLUMN IF NOT EXISTS source_parser_config_hash text,
    ADD COLUMN IF NOT EXISTS source_chunk_strategy_version text,
    ADD COLUMN IF NOT EXISTS source_schema_version text;

ALTER TABLE retrieval.index_version
    DROP CONSTRAINT IF EXISTS retrieval_index_source_manifest_pair,
    ADD CONSTRAINT retrieval_index_source_manifest_pair CHECK (
        (
            num_nonnulls(
                source_manifest_hash, expected_source_count, source_parser_id, source_parser_version,
                source_parser_config_hash, source_chunk_strategy_version, source_schema_version
            ) = 0
            AND
            source_manifest_hash IS NULL AND expected_source_count IS NULL
            AND source_parser_id IS NULL AND source_parser_version IS NULL
            AND source_parser_config_hash IS NULL AND source_chunk_strategy_version IS NULL
            AND source_schema_version IS NULL
        )
        OR (
            num_nonnulls(
                source_manifest_hash, expected_source_count, source_parser_id, source_parser_version,
                source_parser_config_hash, source_chunk_strategy_version, source_schema_version
            ) = 7
            AND
            source_manifest_hash ~ '^[0-9a-f]{64}$'
            AND expected_source_count IS NOT NULL
            AND expected_source_count > 0
            AND btrim(source_parser_id) <> ''
            AND btrim(source_parser_version) <> ''
            AND source_parser_config_hash ~ '^[0-9a-f]{64}$'
            AND btrim(source_chunk_strategy_version) <> ''
            AND btrim(source_schema_version) <> ''
        )
    );

ALTER TABLE retrieval.index_manifest_chunk
    DROP CONSTRAINT IF EXISTS fk_retrieval_manifest_chunk_exact,
    ADD CONSTRAINT fk_retrieval_manifest_chunk_exact FOREIGN KEY (
        chunk_id, workspace_id, content_hash, sequence, parser_version, chunk_strategy_version, schema_version
    ) REFERENCES ingestion.canonical_chunk (
        id, workspace_id, content_hash, sequence, parser_version, chunk_strategy_version, schema_version
    ) ON DELETE RESTRICT;

CREATE TABLE IF NOT EXISTS retrieval.index_manifest_source (
    index_version_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    source_id uuid NOT NULL,
    source_version_id uuid,
    parse_projection_id uuid,
    selection_status text NOT NULL CHECK (selection_status IN ('included', 'excluded')),
    exclusion_code text CHECK (
        exclusion_code IS NULL OR exclusion_code IN ('NO_CURRENT_SUCCESSFUL_PROJECTION')
    ),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (index_version_id, source_id),
    CONSTRAINT fk_retrieval_source_manifest_index FOREIGN KEY (index_version_id, workspace_id)
        REFERENCES retrieval.index_version(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_retrieval_source_manifest_source FOREIGN KEY (source_id, workspace_id)
        REFERENCES core.source(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_retrieval_source_manifest_source_version FOREIGN KEY (source_version_id, source_id)
        REFERENCES core.source_version(id, source_id) ON DELETE RESTRICT,
    CONSTRAINT fk_retrieval_source_manifest_projection FOREIGN KEY (source_version_id, parse_projection_id, workspace_id)
        REFERENCES ingestion.source_version_projection(source_version_id, parse_projection_id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT retrieval_source_manifest_selection CHECK (
        (
            selection_status = 'included'
            AND source_version_id IS NOT NULL
            AND parse_projection_id IS NOT NULL
            AND exclusion_code IS NULL
        ) OR (
            selection_status = 'excluded'
            AND source_version_id IS NULL
            AND parse_projection_id IS NULL
            AND exclusion_code IS NOT NULL
        )
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_retrieval_source_manifest_version
    ON retrieval.index_manifest_source(index_version_id, source_version_id)
    WHERE selection_status = 'included';

CREATE INDEX IF NOT EXISTS idx_retrieval_source_manifest_workspace_index_source
    ON retrieval.index_manifest_source(workspace_id, index_version_id, source_id);

CREATE INDEX IF NOT EXISTS idx_ingestion_attempt_reindex_selection
    ON ingestion.attempt(
        source_version_id, parser_id, parser_version, parser_config_hash,
        chunk_strategy_version, schema_version, started_at DESC, id DESC
    ) WHERE status = 'chunked' AND security_status = 'passed';

CREATE TABLE IF NOT EXISTS retrieval.reindex_delivery (
    id uuid PRIMARY KEY,
    consumer_name text NOT NULL CHECK (btrim(consumer_name) <> '' AND char_length(consumer_name) <= 128),
    outbox_event_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    writeback_execution_id uuid NOT NULL,
    status text NOT NULL CHECK (status IN (
        'pending', 'dispatched', 'processing', 'retry_wait', 'succeeded', 'failed', 'manual_recovery'
    )),
    dispatch_no integer NOT NULL DEFAULT 0 CHECK (dispatch_no >= 0),
    attempt_no integer NOT NULL DEFAULT 0 CHECK (attempt_no >= 0),
    current_attempt_id uuid,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    next_attempt_at timestamptz,
    source_version_id uuid REFERENCES core.source_version(id) ON DELETE RESTRICT,
    parse_projection_id uuid REFERENCES ingestion.parse_projection(id) ON DELETE RESTRICT,
    index_version_id uuid,
    activation_id uuid REFERENCES retrieval.index_activation(id) ON DELETE RESTRICT,
    excluded_source_count bigint CHECK (excluded_source_count IS NULL OR excluded_source_count >= 0),
    regression_code text CHECK (regression_code IS NULL OR regression_code = 'SNAPSHOT_STRUCTURE_V1'),
    regression_hash text CHECK (regression_hash IS NULL OR regression_hash ~ '^[0-9a-f]{64}$'),
    regression_passed_at timestamptz,
    failure_class text CHECK (failure_class IS NULL OR failure_class IN ('retryable', 'non_retryable', 'manual_recovery')),
    error_kind text CHECK (error_kind IS NULL OR (btrim(error_kind) <> '' AND char_length(error_kind) <= 128)),
    error_code text CHECK (error_code IS NULL OR (btrim(error_code) <> '' AND char_length(error_code) <= 128)),
    error_summary text CHECK (error_summary IS NULL OR (btrim(error_summary) <> '' AND char_length(error_summary) <= 512)),
    manual_recovery_required boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT uq_reindex_delivery_consumer_outbox UNIQUE (consumer_name, outbox_event_id),
    CONSTRAINT uq_reindex_delivery_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT fk_reindex_delivery_outbox FOREIGN KEY (outbox_event_id, workspace_id)
        REFERENCES workflow.outbox_event(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_reindex_delivery_writeback FOREIGN KEY (writeback_execution_id, workspace_id)
        REFERENCES change_control.writeback_execution(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_reindex_delivery_index FOREIGN KEY (index_version_id, workspace_id)
        REFERENCES retrieval.index_version(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT reindex_delivery_time_order CHECK (
        updated_at >= created_at
        AND (next_attempt_at IS NULL OR next_attempt_at >= created_at)
        AND (regression_passed_at IS NULL OR regression_passed_at >= created_at)
        AND (completed_at IS NULL OR completed_at >= created_at)
    ),
    CONSTRAINT reindex_delivery_retry_schedule CHECK ((status = 'retry_wait') = (next_attempt_at IS NOT NULL)),
    CONSTRAINT reindex_delivery_completion CHECK (
        (status IN ('succeeded', 'failed') AND completed_at IS NOT NULL)
        OR (status NOT IN ('succeeded', 'failed') AND completed_at IS NULL)
    ),
    CONSTRAINT reindex_delivery_manual_recovery CHECK (
        manual_recovery_required = (status = 'manual_recovery')
    ),
    CONSTRAINT reindex_delivery_activation_status CHECK (
        (activation_id IS NOT NULL) = (status = 'succeeded')
    ),
    CONSTRAINT reindex_delivery_regression_tuple CHECK (
        (regression_code IS NULL AND regression_hash IS NULL AND regression_passed_at IS NULL)
        OR (regression_code IS NOT NULL AND regression_hash IS NOT NULL AND regression_passed_at IS NOT NULL)
    ),
    CONSTRAINT reindex_delivery_checkpoint_prefix CHECK (
        (source_version_id IS NULL AND parse_projection_id IS NULL AND index_version_id IS NULL
            AND excluded_source_count IS NULL AND activation_id IS NULL
            AND regression_code IS NULL AND regression_hash IS NULL AND regression_passed_at IS NULL)
        OR (source_version_id IS NOT NULL AND parse_projection_id IS NULL AND index_version_id IS NULL
            AND excluded_source_count IS NULL AND activation_id IS NULL
            AND regression_code IS NULL AND regression_hash IS NULL AND regression_passed_at IS NULL)
        OR (source_version_id IS NOT NULL AND parse_projection_id IS NOT NULL AND index_version_id IS NULL
            AND excluded_source_count IS NULL AND activation_id IS NULL
            AND regression_code IS NULL AND regression_hash IS NULL AND regression_passed_at IS NULL)
        OR (source_version_id IS NOT NULL AND parse_projection_id IS NOT NULL AND index_version_id IS NOT NULL
            AND excluded_source_count IS NOT NULL AND activation_id IS NULL
            AND regression_code IS NULL AND regression_hash IS NULL AND regression_passed_at IS NULL)
        OR (source_version_id IS NOT NULL AND parse_projection_id IS NOT NULL AND index_version_id IS NOT NULL
            AND excluded_source_count IS NOT NULL
            AND regression_code IS NOT NULL AND regression_hash IS NOT NULL AND regression_passed_at IS NOT NULL)
    ),
    CONSTRAINT reindex_delivery_failure_tuple CHECK (
        (failure_class IS NULL AND error_kind IS NULL AND error_code IS NULL AND error_summary IS NULL)
        OR (failure_class IS NOT NULL AND error_kind IS NOT NULL AND error_code IS NOT NULL AND error_summary IS NOT NULL)
    ),
    CONSTRAINT reindex_delivery_failure_status CHECK (
        (status = 'retry_wait' AND failure_class = 'retryable')
        OR (status = 'failed' AND failure_class = 'non_retryable')
        OR (status = 'manual_recovery' AND failure_class = 'manual_recovery')
        OR (status NOT IN ('retry_wait', 'failed', 'manual_recovery') AND failure_class IS NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_reindex_delivery_workspace_blocking
    ON retrieval.reindex_delivery(workspace_id)
    WHERE status IN ('pending', 'dispatched', 'processing', 'retry_wait', 'manual_recovery');

CREATE INDEX IF NOT EXISTS idx_reindex_delivery_retry_due
    ON retrieval.reindex_delivery(next_attempt_at, id)
    WHERE status = 'retry_wait';

CREATE INDEX IF NOT EXISTS idx_reindex_delivery_writeback_execution
    ON retrieval.reindex_delivery(writeback_execution_id);

CREATE TABLE IF NOT EXISTS retrieval.reindex_delivery_attempt (
    id uuid PRIMARY KEY,
    delivery_id uuid NOT NULL REFERENCES retrieval.reindex_delivery(id) ON DELETE RESTRICT,
    attempt_no integer NOT NULL CHECK (attempt_no > 0),
    dispatch_no integer NOT NULL CHECK (dispatch_no > 0),
    river_job_id bigint NOT NULL CHECK (river_job_id > 0),
    river_attempt integer NOT NULL CHECK (river_attempt > 0),
    delivery_key text NOT NULL CHECK (btrim(delivery_key) <> '' AND char_length(delivery_key) <= 256),
    lease_owner text NOT NULL CHECK (btrim(lease_owner) <> '' AND char_length(lease_owner) <= 256),
    lease_until timestamptz NOT NULL,
    status text NOT NULL CHECK (status IN (
        'processing', 'succeeded', 'retry_wait', 'failed', 'manual_recovery', 'lease_lost'
    )),
    ingestion_attempt_id uuid REFERENCES ingestion.attempt(id) ON DELETE RESTRICT,
    failure_class text CHECK (failure_class IS NULL OR failure_class IN ('retryable', 'non_retryable', 'manual_recovery')),
    error_kind text CHECK (error_kind IS NULL OR (btrim(error_kind) <> '' AND char_length(error_kind) <= 128)),
    error_code text CHECK (error_code IS NULL OR (btrim(error_code) <> '' AND char_length(error_code) <= 128)),
    error_summary text CHECK (error_summary IS NULL OR (btrim(error_summary) <> '' AND char_length(error_summary) <= 512)),
    started_at timestamptz NOT NULL,
    heartbeat_at timestamptz NOT NULL,
    ended_at timestamptz,
    CONSTRAINT uq_reindex_delivery_attempt_no UNIQUE (delivery_id, attempt_no),
    CONSTRAINT uq_reindex_delivery_attempt_key UNIQUE (delivery_id, delivery_key),
    CONSTRAINT uq_reindex_delivery_attempt_id_delivery UNIQUE (id, delivery_id),
    CONSTRAINT reindex_delivery_attempt_time_order CHECK (
        heartbeat_at >= started_at AND lease_until >= heartbeat_at
        AND (ended_at IS NULL OR ended_at >= started_at)
    ),
    CONSTRAINT reindex_delivery_attempt_terminal CHECK (
        (status = 'processing' AND ended_at IS NULL)
        OR (status <> 'processing' AND ended_at IS NOT NULL)
    ),
    CONSTRAINT reindex_delivery_attempt_failure_tuple CHECK (
        (failure_class IS NULL AND error_kind IS NULL AND error_code IS NULL AND error_summary IS NULL)
        OR (failure_class IS NOT NULL AND error_kind IS NOT NULL AND error_code IS NOT NULL AND error_summary IS NOT NULL)
    ),
    CONSTRAINT reindex_delivery_attempt_failure_status CHECK (
        (status IN ('retry_wait', 'lease_lost') AND failure_class = 'retryable')
        OR (status = 'failed' AND failure_class = 'non_retryable')
        OR (status = 'manual_recovery' AND failure_class = 'manual_recovery')
        OR (status NOT IN ('retry_wait', 'failed', 'manual_recovery', 'lease_lost') AND failure_class IS NULL)
    )
);

ALTER TABLE retrieval.reindex_delivery
    DROP CONSTRAINT IF EXISTS fk_reindex_delivery_current_attempt,
    ADD CONSTRAINT fk_reindex_delivery_current_attempt FOREIGN KEY (current_attempt_id, id)
        REFERENCES retrieval.reindex_delivery_attempt(id, delivery_id) ON DELETE RESTRICT
        DEFERRABLE INITIALLY IMMEDIATE;

CREATE INDEX IF NOT EXISTS idx_reindex_delivery_attempt_delivery_started
    ON retrieval.reindex_delivery_attempt(delivery_id, started_at DESC, id DESC);

-- Replace the per-row Chunk lookup/Index lock with exact composite FK checks and
-- one statement-level status check so COPY remains bounded at large manifests.
DROP TRIGGER IF EXISTS retrieval_manifest_validate_insert ON retrieval.index_manifest_chunk;

CREATE OR REPLACE FUNCTION retrieval.validate_manifest_chunk_statement()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM new_manifest_rows rows
          JOIN retrieval.index_version index_version ON index_version.id = rows.index_version_id
         WHERE index_version.workspace_id <> rows.workspace_id
            OR index_version.status <> 'building'
    ) THEN
        RAISE EXCEPTION 'retrieval manifest index must be building and workspace-bound' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER retrieval_manifest_validate_insert
    AFTER INSERT ON retrieval.index_manifest_chunk
    REFERENCING NEW TABLE AS new_manifest_rows
    FOR EACH STATEMENT EXECUTE FUNCTION retrieval.validate_manifest_chunk_statement();

CREATE OR REPLACE FUNCTION retrieval.validate_manifest_source_statement()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM new_source_rows rows
          JOIN retrieval.index_version index_version ON index_version.id = rows.index_version_id
         WHERE index_version.workspace_id <> rows.workspace_id
            OR index_version.status <> 'building'
    ) THEN
        RAISE EXCEPTION 'retrieval source manifest index must be building and workspace-bound' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER retrieval_source_manifest_validate_insert
    AFTER INSERT ON retrieval.index_manifest_source
    REFERENCING NEW TABLE AS new_source_rows
    FOR EACH STATEMENT EXECUTE FUNCTION retrieval.validate_manifest_source_statement();

CREATE TRIGGER retrieval_source_manifest_reject_mutation
    BEFORE UPDATE OR DELETE ON retrieval.index_manifest_source
    FOR EACH ROW EXECUTE FUNCTION retrieval.reject_immutable_mutation();

CREATE OR REPLACE FUNCTION retrieval.validate_reindex_delivery_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'pending' OR NEW.dispatch_no <> 0 OR NEW.attempt_no <> 0
           OR NEW.current_attempt_id IS NOT NULL OR NEW.version <> 1
           OR NEW.next_attempt_at IS NOT NULL OR NEW.completed_at IS NOT NULL
           OR NEW.source_version_id IS NOT NULL OR NEW.parse_projection_id IS NOT NULL
           OR NEW.index_version_id IS NOT NULL OR NEW.activation_id IS NOT NULL
           OR NEW.excluded_source_count IS NOT NULL OR NEW.regression_code IS NOT NULL
           OR NEW.failure_class IS NOT NULL OR NEW.manual_recovery_required THEN
            RAISE EXCEPTION 'reindex delivery must be inserted as pending version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id <> OLD.id OR NEW.consumer_name <> OLD.consumer_name
       OR NEW.outbox_event_id <> OLD.outbox_event_id OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.writeback_execution_id <> OLD.writeback_execution_id OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'reindex delivery immutable field or version violation' USING ERRCODE = '23514';
    END IF;

    IF OLD.status IN ('succeeded', 'failed') THEN
        RAISE EXCEPTION 'reindex terminal delivery is immutable' USING ERRCODE = '55000';
    END IF;
    IF NOT (
        NEW.status = OLD.status
        OR (OLD.status = 'pending' AND NEW.status = 'dispatched')
        OR (OLD.status = 'dispatched' AND NEW.status = 'processing')
        OR (OLD.status = 'processing' AND NEW.status IN ('retry_wait', 'succeeded', 'failed', 'manual_recovery'))
        OR (OLD.status = 'processing' AND NEW.status = 'dispatched')
        OR (OLD.status = 'retry_wait' AND NEW.status = 'dispatched')
    ) THEN
        RAISE EXCEPTION 'reindex delivery status transition is invalid' USING ERRCODE = '23514';
    END IF;

    IF OLD.status = 'pending' AND NEW.status = 'dispatched' THEN
        IF OLD.dispatch_no <> 0 OR NEW.dispatch_no <> 1 THEN
            RAISE EXCEPTION 'first reindex dispatch must create generation one' USING ERRCODE = '23514';
        END IF;
    ELSIF OLD.status = 'retry_wait' AND NEW.status = 'dispatched' THEN
        IF NEW.dispatch_no <> OLD.dispatch_no + 1 THEN
            RAISE EXCEPTION 'reindex retry dispatch must increment generation' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.dispatch_no <> OLD.dispatch_no THEN
        RAISE EXCEPTION 'reindex dispatch generation changed outside dispatcher' USING ERRCODE = '23514';
    END IF;

    IF OLD.status = 'dispatched' AND NEW.status = 'processing' THEN
        IF NEW.attempt_no <> OLD.attempt_no + 1 OR NEW.current_attempt_id IS NULL THEN
            RAISE EXCEPTION 'reindex claim must bind the next delivery attempt' USING ERRCODE = '23514';
        END IF;
        IF NOT EXISTS (
            SELECT 1 FROM retrieval.reindex_delivery_attempt attempt
             WHERE attempt.id = NEW.current_attempt_id
               AND attempt.delivery_id = NEW.id
               AND attempt.attempt_no = NEW.attempt_no
               AND attempt.dispatch_no = NEW.dispatch_no
               AND attempt.status = 'processing'
        ) THEN
            RAISE EXCEPTION 'reindex claim attempt binding mismatch' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.attempt_no <> OLD.attempt_no OR NEW.current_attempt_id IS DISTINCT FROM OLD.current_attempt_id THEN
        RAISE EXCEPTION 'reindex attempt changed outside claim' USING ERRCODE = '23514';
    END IF;

    IF OLD.status = 'processing' AND NEW.status = 'dispatched'
       AND NOT EXISTS (
            SELECT 1 FROM retrieval.reindex_delivery_attempt attempt
             WHERE attempt.id = OLD.current_attempt_id
               AND attempt.delivery_id = NEW.id
               AND attempt.attempt_no = NEW.attempt_no
               AND attempt.dispatch_no = NEW.dispatch_no
               AND attempt.status = 'lease_lost'
       ) THEN
        RAISE EXCEPTION 'reindex transport reclaim requires a lease-lost attempt' USING ERRCODE = '23514';
    END IF;

    IF OLD.status = 'processing' AND NEW.status NOT IN ('processing', 'dispatched')
       AND NOT EXISTS (
            SELECT 1 FROM retrieval.reindex_delivery_attempt attempt
             WHERE attempt.id = OLD.current_attempt_id
               AND attempt.delivery_id = NEW.id
               AND attempt.attempt_no = NEW.attempt_no
               AND attempt.dispatch_no = NEW.dispatch_no
               AND attempt.status = NEW.status
       ) THEN
        RAISE EXCEPTION 'reindex delivery result does not match current attempt' USING ERRCODE = '23514';
    END IF;

    IF OLD.source_version_id IS NOT NULL AND NEW.source_version_id IS DISTINCT FROM OLD.source_version_id
       OR OLD.parse_projection_id IS NOT NULL AND NEW.parse_projection_id IS DISTINCT FROM OLD.parse_projection_id
       OR OLD.index_version_id IS NOT NULL AND NEW.index_version_id IS DISTINCT FROM OLD.index_version_id
       OR OLD.activation_id IS NOT NULL AND NEW.activation_id IS DISTINCT FROM OLD.activation_id
       OR OLD.excluded_source_count IS NOT NULL AND NEW.excluded_source_count IS DISTINCT FROM OLD.excluded_source_count
       OR OLD.regression_code IS NOT NULL AND (
            NEW.regression_code IS DISTINCT FROM OLD.regression_code
            OR NEW.regression_hash IS DISTINCT FROM OLD.regression_hash
            OR NEW.regression_passed_at IS DISTINCT FROM OLD.regression_passed_at
       ) THEN
        RAISE EXCEPTION 'reindex checkpoint is immutable after persistence' USING ERRCODE = '23514';
    END IF;
    IF NEW.source_version_id IS NOT NULL AND NEW.parse_projection_id IS NOT NULL
       AND NOT EXISTS (
            SELECT 1 FROM ingestion.source_version_projection
             WHERE source_version_id = NEW.source_version_id
               AND parse_projection_id = NEW.parse_projection_id
               AND workspace_id = NEW.workspace_id
       ) THEN
        RAISE EXCEPTION 'reindex source version and projection binding mismatch' USING ERRCODE = '23514';
    END IF;
    IF NEW.parse_projection_id IS NOT NULL
       AND NOT EXISTS (
            SELECT 1
              FROM retrieval.reindex_delivery_attempt delivery_attempt
              JOIN ingestion.attempt ingestion_attempt
                ON ingestion_attempt.id = delivery_attempt.ingestion_attempt_id
               AND ingestion_attempt.workspace_id = NEW.workspace_id
               AND ingestion_attempt.source_version_id = NEW.source_version_id
               AND ingestion_attempt.parse_projection_id = NEW.parse_projection_id
               AND ingestion_attempt.status = 'chunked'
               AND ingestion_attempt.security_status = 'passed'
             WHERE delivery_attempt.id = NEW.current_attempt_id
               AND delivery_attempt.delivery_id = NEW.id
       ) THEN
        RAISE EXCEPTION 'reindex ingestion checkpoint binding mismatch' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER reindex_delivery_validate_write
    BEFORE INSERT OR UPDATE ON retrieval.reindex_delivery
    FOR EACH ROW EXECUTE FUNCTION retrieval.validate_reindex_delivery_write();

CREATE OR REPLACE FUNCTION retrieval.validate_reindex_attempt_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'processing' OR NEW.ended_at IS NOT NULL THEN
            RAISE EXCEPTION 'reindex attempt must be inserted as processing' USING ERRCODE = '23514';
        END IF;
        IF NEW.lease_until <= clock_timestamp() OR NEW.heartbeat_at > clock_timestamp() THEN
            RAISE EXCEPTION 'reindex attempt requires a live database-time lease' USING ERRCODE = '23514';
        END IF;
        IF NOT EXISTS (
            SELECT 1 FROM retrieval.reindex_delivery delivery
             WHERE delivery.id = NEW.delivery_id
               AND delivery.status = 'dispatched'
               AND delivery.dispatch_no = NEW.dispatch_no
               AND delivery.attempt_no + 1 = NEW.attempt_no
        ) THEN
            RAISE EXCEPTION 'reindex attempt does not match dispatched delivery' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.status <> 'processing' THEN
        RAISE EXCEPTION 'reindex terminal attempt is append-only' USING ERRCODE = '55000';
    END IF;
    IF NEW.status = 'lease_lost' AND NEW.ended_at < OLD.lease_until THEN
        RAISE EXCEPTION 'reindex attempt lease has not expired' USING ERRCODE = '23514';
    END IF;
    IF NEW.status <> 'lease_lost' AND (
        (NEW.status <> 'processing' AND NEW.ended_at >= OLD.lease_until)
        OR (NEW.status = 'processing' AND NEW.heartbeat_at > OLD.heartbeat_at AND NEW.heartbeat_at >= OLD.lease_until)
        OR (NEW.status = 'processing' AND NEW.heartbeat_at = OLD.heartbeat_at
            AND NEW.ingestion_attempt_id IS DISTINCT FROM OLD.ingestion_attempt_id
            AND clock_timestamp() >= OLD.lease_until)
    ) THEN
        RAISE EXCEPTION 'expired reindex attempt cannot heartbeat or publish a result' USING ERRCODE = '23514';
    END IF;
    IF NEW.id <> OLD.id OR NEW.delivery_id <> OLD.delivery_id OR NEW.attempt_no <> OLD.attempt_no
       OR NEW.dispatch_no <> OLD.dispatch_no OR NEW.river_job_id <> OLD.river_job_id
       OR NEW.river_attempt <> OLD.river_attempt OR NEW.delivery_key <> OLD.delivery_key
       OR NEW.lease_owner <> OLD.lease_owner OR NEW.started_at <> OLD.started_at
       OR NEW.heartbeat_at < OLD.heartbeat_at OR NEW.lease_until < OLD.lease_until
       OR ((NEW.heartbeat_at IS DISTINCT FROM OLD.heartbeat_at)
           <> (NEW.lease_until IS DISTINCT FROM OLD.lease_until))
       OR (OLD.ingestion_attempt_id IS NOT NULL AND NEW.ingestion_attempt_id IS DISTINCT FROM OLD.ingestion_attempt_id)
       OR (NEW.status = 'processing' AND NEW.ended_at IS NOT NULL)
       OR (NEW.status <> 'processing' AND NEW.ended_at IS NULL) THEN
        RAISE EXCEPTION 'reindex attempt immutable field or transition violation' USING ERRCODE = '23514';
    END IF;
    IF NEW.status = 'succeeded' AND NEW.ingestion_attempt_id IS NULL THEN
        RAISE EXCEPTION 'successful reindex attempt requires ingestion evidence' USING ERRCODE = '23514';
    END IF;
    IF NEW.ingestion_attempt_id IS NOT NULL
       AND NOT EXISTS (
            SELECT 1
              FROM retrieval.reindex_delivery delivery
              JOIN ingestion.attempt ingestion_attempt ON ingestion_attempt.id = NEW.ingestion_attempt_id
             WHERE delivery.id = NEW.delivery_id
               AND delivery.workspace_id = ingestion_attempt.workspace_id
               AND delivery.source_version_id = ingestion_attempt.source_version_id
               AND ingestion_attempt.parse_projection_id IS NOT NULL
               AND ingestion_attempt.status = 'chunked'
               AND ingestion_attempt.security_status = 'passed'
       ) THEN
        RAISE EXCEPTION 'reindex attempt ingestion evidence mismatch' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER reindex_delivery_attempt_validate_write
    BEFORE INSERT OR UPDATE ON retrieval.reindex_delivery_attempt
    FOR EACH ROW EXECUTE FUNCTION retrieval.validate_reindex_attempt_write();

CREATE TRIGGER reindex_delivery_attempt_reject_delete
    BEFORE DELETE ON retrieval.reindex_delivery_attempt
    FOR EACH ROW EXECUTE FUNCTION retrieval.reject_immutable_mutation();

CREATE OR REPLACE FUNCTION retrieval.assert_reindex_completion(delivery_identity uuid)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM retrieval.reindex_delivery delivery
          JOIN workflow.outbox_event event
            ON event.id = delivery.outbox_event_id AND event.workspace_id = delivery.workspace_id
          JOIN change_control.writeback_execution execution
            ON execution.id = delivery.writeback_execution_id AND execution.workspace_id = delivery.workspace_id
          JOIN change_control.proposal proposal ON proposal.id = execution.proposal_id
          JOIN change_control.proposal_commit commit_mapping
            ON commit_mapping.writeback_execution_id = execution.id
          JOIN workflow.run workflow_run ON workflow_run.id = execution.workflow_run_id
          JOIN workflow.node_run workflow_node
            ON workflow_node.id = execution.node_run_id AND workflow_node.run_id = workflow_run.id
          JOIN core.source_version source_version ON source_version.id = delivery.source_version_id
          JOIN core.source source ON source.id = source_version.source_id AND source.workspace_id = delivery.workspace_id
          JOIN retrieval.index_version index_version
            ON index_version.id = delivery.index_version_id AND index_version.workspace_id = delivery.workspace_id
          JOIN retrieval.index_manifest_source source_manifest
            ON source_manifest.index_version_id = index_version.id
           AND source_manifest.source_id = source.id
           AND source_manifest.source_version_id = source_version.id
           AND source_manifest.parse_projection_id = delivery.parse_projection_id
           AND source_manifest.selection_status = 'included'
          JOIN retrieval.index_activation activation
            ON activation.id = delivery.activation_id
           AND activation.workspace_id = delivery.workspace_id
           AND activation.target_index_version_id = index_version.id
          JOIN retrieval.reindex_delivery_attempt delivery_attempt
            ON delivery_attempt.id = delivery.current_attempt_id
           AND delivery_attempt.delivery_id = delivery.id
           AND delivery_attempt.attempt_no = delivery.attempt_no
           AND delivery_attempt.dispatch_no = delivery.dispatch_no
           AND delivery_attempt.status = 'succeeded'
          JOIN ingestion.attempt ingestion_attempt
            ON ingestion_attempt.id = delivery_attempt.ingestion_attempt_id
           AND ingestion_attempt.workspace_id = delivery.workspace_id
           AND ingestion_attempt.source_version_id = delivery.source_version_id
           AND ingestion_attempt.parse_projection_id = delivery.parse_projection_id
         WHERE delivery.id = delivery_identity
           AND delivery.status = 'succeeded'
           AND delivery.completed_at IS NOT NULL
           AND delivery.regression_code = 'SNAPSHOT_STRUCTURE_V1'
           AND delivery.regression_hash IS NOT NULL
           AND delivery.regression_passed_at IS NOT NULL
           AND event.event_type = 'retrieval.revision.reindex_requested'
           AND event.published_at IS NOT NULL
           AND event.payload->>'workspace_id' = delivery.workspace_id::text
           AND event.payload->>'workflow_run_id' = execution.workflow_run_id::text
           AND event.payload->>'node_run_id' = execution.node_run_id::text
           AND event.payload->>'proposal_id' = execution.proposal_id::text
           AND event.payload->>'revision_id' = execution.revision_id::text
           AND event.payload->>'approval_id' = execution.approval_id::text
           AND event.payload->>'writeback_execution_id' = execution.id::text
           AND event.payload->>'target_path' = execution.target_path
           AND event.payload->>'result_hash' = execution.result_hash
           AND event.payload->>'git_commit' = execution.git_commit
           AND commit_mapping.proposal_id = execution.proposal_id
           AND commit_mapping.revision_id = execution.revision_id
           AND commit_mapping.approval_id = execution.approval_id
           AND commit_mapping.git_commit = execution.git_commit
           AND commit_mapping.target_path = execution.target_path
           AND commit_mapping.result_hash = execution.result_hash
           AND source_version.content_hash = execution.result_hash
           AND ingestion_attempt.status = 'chunked'
           AND ingestion_attempt.security_status = 'passed'
           AND execution.status = 'completed'
           AND execution.completed_at IS NOT NULL
           AND execution.cleanup_completed_at IS NOT NULL
           AND proposal.status = 'completed'
           AND proposal.workflow_run_id = workflow_run.id
           AND workflow_run.status = 'succeeded'
           AND workflow_node.status = 'succeeded'
           AND index_version.status = 'active'
           AND index_version.source_manifest_hash IS NOT NULL
           AND index_version.expected_source_count IS NOT NULL
           AND activation.kind = 'activate'
           AND activation.target_version = index_version.version
           AND delivery.excluded_source_count = (
                SELECT count(*) FROM retrieval.index_manifest_source excluded
                 WHERE excluded.index_version_id = index_version.id
                   AND excluded.selection_status = 'excluded'
           )
    ) THEN
        RAISE EXCEPTION 'reindex completion invariant is not closed' USING ERRCODE = '55000';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION retrieval.verify_reindex_completion_trigger()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    delivery_identity uuid;
BEGIN
    IF TG_TABLE_SCHEMA = 'retrieval' AND TG_TABLE_NAME = 'reindex_delivery' THEN
        IF NEW.status <> 'succeeded' THEN
            RETURN NEW;
        END IF;
        delivery_identity := NEW.id;
    ELSIF TG_TABLE_SCHEMA = 'change_control' AND TG_TABLE_NAME = 'writeback_execution' THEN
        IF NEW.status <> 'completed' THEN
            RETURN NEW;
        END IF;
        SELECT id INTO delivery_identity
          FROM retrieval.reindex_delivery
         WHERE writeback_execution_id = NEW.id;
    ELSE
        IF NEW.status <> 'completed' THEN
            RETURN NEW;
        END IF;
        SELECT delivery.id INTO delivery_identity
          FROM retrieval.reindex_delivery delivery
          JOIN change_control.writeback_execution execution
            ON execution.id = delivery.writeback_execution_id
         WHERE execution.proposal_id = NEW.id;
    END IF;

    IF delivery_identity IS NULL THEN
        RAISE EXCEPTION 'completed writeback facts require a reindex delivery' USING ERRCODE = '55000';
    END IF;
    PERFORM retrieval.assert_reindex_completion(delivery_identity);
    RETURN NEW;
END;
$$;

CREATE CONSTRAINT TRIGGER reindex_delivery_verify_completion
    AFTER INSERT OR UPDATE ON retrieval.reindex_delivery
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION retrieval.verify_reindex_completion_trigger();

CREATE CONSTRAINT TRIGGER writeback_execution_verify_reindex_completion
    AFTER UPDATE ON change_control.writeback_execution
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION retrieval.verify_reindex_completion_trigger();

CREATE CONSTRAINT TRIGGER proposal_verify_reindex_completion
    AFTER INSERT OR UPDATE ON change_control.proposal
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION retrieval.verify_reindex_completion_trigger();

CREATE OR REPLACE FUNCTION retrieval.validate_index_version_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    manifest_count bigint;
    projection_count bigint;
    source_count bigint;
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

    IF NEW.id <> OLD.id OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.embedding_version_id IS DISTINCT FROM OLD.embedding_version_id
       OR NEW.tokenizer_id <> OLD.tokenizer_id OR NEW.tokenizer_version <> OLD.tokenizer_version
       OR NEW.tokenizer_config_hash <> OLD.tokenizer_config_hash OR NEW.fusion_config <> OLD.fusion_config
       OR NEW.source_snapshot_ref <> OLD.source_snapshot_ref OR NEW.manifest_hash <> OLD.manifest_hash
       OR NEW.expected_chunk_count <> OLD.expected_chunk_count
       OR NEW.source_manifest_hash IS DISTINCT FROM OLD.source_manifest_hash
       OR NEW.expected_source_count IS DISTINCT FROM OLD.expected_source_count
       OR NEW.source_parser_id IS DISTINCT FROM OLD.source_parser_id
       OR NEW.source_parser_version IS DISTINCT FROM OLD.source_parser_version
       OR NEW.source_parser_config_hash IS DISTINCT FROM OLD.source_parser_config_hash
       OR NEW.source_chunk_strategy_version IS DISTINCT FROM OLD.source_chunk_strategy_version
       OR NEW.source_schema_version IS DISTINCT FROM OLD.source_schema_version
       OR NEW.idempotency_key <> OLD.idempotency_key OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'retrieval index immutable field or version violation' USING ERRCODE = '23514';
    END IF;
    IF OLD.status <> 'building' AND NEW.degraded_capabilities <> OLD.degraded_capabilities THEN
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
        SELECT 1 FROM retrieval.index_activation activation
         WHERE activation.workspace_id = NEW.workspace_id
           AND activation.target_index_version_id = NEW.id
           AND activation.target_version = NEW.version
    ) THEN
        RAISE EXCEPTION 'retrieval index may enter active only through activation' USING ERRCODE = '55000';
    END IF;

    IF NEW.status = 'ready' THEN
        SELECT count(*) INTO manifest_count FROM retrieval.index_manifest_chunk WHERE index_version_id = NEW.id;
        SELECT count(*) INTO projection_count FROM retrieval.chunk_projection WHERE index_version_id = NEW.id;
        IF manifest_count <> NEW.expected_chunk_count OR projection_count <> NEW.expected_chunk_count
           OR EXISTS (
                SELECT 1 FROM retrieval.index_manifest_chunk manifest
                LEFT JOIN retrieval.chunk_projection projection
                  ON projection.index_version_id = manifest.index_version_id AND projection.chunk_id = manifest.chunk_id
                WHERE manifest.index_version_id = NEW.id
                  AND (projection.chunk_id IS NULL OR projection.lexical_status <> 'ready')
           )
           OR (NEW.embedding_version_id IS NULL AND EXISTS (
                SELECT 1 FROM retrieval.chunk_projection WHERE index_version_id = NEW.id AND vector_status <> 'disabled'
           ))
           OR (NEW.embedding_version_id IS NOT NULL AND NEW.degraded_capabilities = '[]'::jsonb AND EXISTS (
                SELECT 1 FROM retrieval.chunk_projection WHERE index_version_id = NEW.id AND vector_status <> 'ready'
           ))
           OR (NEW.embedding_version_id IS NOT NULL AND NEW.degraded_capabilities = '["vector"]'::jsonb AND EXISTS (
                SELECT 1 FROM retrieval.chunk_projection
                 WHERE index_version_id = NEW.id AND vector_status NOT IN ('ready', 'skipped_oversized', 'failed')
           ))
           OR (NEW.embedding_version_id IS NOT NULL AND NEW.degraded_capabilities = '["vector"]'::jsonb AND NOT EXISTS (
                SELECT 1 FROM retrieval.chunk_projection WHERE index_version_id = NEW.id AND vector_status <> 'ready'
           )) THEN
            RAISE EXCEPTION 'retrieval index is not complete enough to become ready' USING ERRCODE = '23514';
        END IF;

        IF NEW.source_manifest_hash IS NOT NULL THEN
            SELECT count(*) INTO source_count FROM retrieval.index_manifest_source WHERE index_version_id = NEW.id;
            IF source_count <> NEW.expected_source_count
               OR EXISTS (
                    SELECT 1
                      FROM retrieval.index_manifest_source source_manifest
                     WHERE source_manifest.index_version_id = NEW.id
                       AND source_manifest.selection_status = 'included'
                       AND NOT EXISTS (
                            SELECT 1
                              FROM ingestion.parse_projection projection
                              JOIN ingestion.attempt attempt
                                ON attempt.parse_projection_id = projection.id
                               AND attempt.source_version_id = source_manifest.source_version_id
                               AND attempt.workspace_id = source_manifest.workspace_id
                               AND attempt.status = 'chunked'
                               AND attempt.security_status = 'passed'
                               AND attempt.parser_id = NEW.source_parser_id
                               AND attempt.parser_version = NEW.source_parser_version
                               AND attempt.parser_config_hash = NEW.source_parser_config_hash
                               AND attempt.chunk_strategy_version = NEW.source_chunk_strategy_version
                               AND attempt.schema_version = NEW.source_schema_version
                             WHERE projection.id = source_manifest.parse_projection_id
                               AND projection.workspace_id = source_manifest.workspace_id
                               AND projection.parser_id = NEW.source_parser_id
                               AND projection.parser_version = NEW.source_parser_version
                               AND projection.parser_config_hash = NEW.source_parser_config_hash
                               AND projection.schema_version = NEW.source_schema_version
                       )
               )
               OR EXISTS (
                    (SELECT chunk.id
                       FROM retrieval.index_manifest_source source_manifest
                       JOIN ingestion.canonical_chunk chunk
                         ON chunk.parse_projection_id = source_manifest.parse_projection_id
                       AND chunk.workspace_id = source_manifest.workspace_id
                        AND chunk.status = 'active'
                        AND chunk.parser_version = NEW.source_parser_version
                        AND chunk.chunk_strategy_version = NEW.source_chunk_strategy_version
                        AND chunk.schema_version = NEW.source_schema_version
                      JOIN ingestion.parse_projection projection
                        ON projection.id = source_manifest.parse_projection_id
                       AND projection.workspace_id = source_manifest.workspace_id
                       AND projection.parser_id = NEW.source_parser_id
                       AND projection.parser_version = NEW.source_parser_version
                       AND projection.parser_config_hash = NEW.source_parser_config_hash
                       AND projection.schema_version = NEW.source_schema_version
                      WHERE source_manifest.index_version_id = NEW.id
                        AND source_manifest.selection_status = 'included'
                     EXCEPT
                     SELECT chunk_id FROM retrieval.index_manifest_chunk WHERE index_version_id = NEW.id)
                    UNION ALL
                    (SELECT chunk_id FROM retrieval.index_manifest_chunk WHERE index_version_id = NEW.id
                     EXCEPT
                     SELECT chunk.id
                       FROM retrieval.index_manifest_source source_manifest
                       JOIN ingestion.canonical_chunk chunk
                         ON chunk.parse_projection_id = source_manifest.parse_projection_id
                       AND chunk.workspace_id = source_manifest.workspace_id
                        AND chunk.status = 'active'
                        AND chunk.parser_version = NEW.source_parser_version
                        AND chunk.chunk_strategy_version = NEW.source_chunk_strategy_version
                        AND chunk.schema_version = NEW.source_schema_version
                      JOIN ingestion.parse_projection projection
                        ON projection.id = source_manifest.parse_projection_id
                       AND projection.workspace_id = source_manifest.workspace_id
                       AND projection.parser_id = NEW.source_parser_id
                       AND projection.parser_version = NEW.source_parser_version
                       AND projection.parser_config_hash = NEW.source_parser_config_hash
                       AND projection.schema_version = NEW.source_schema_version
                      WHERE source_manifest.index_version_id = NEW.id
                        AND source_manifest.selection_status = 'included')
               ) THEN
                RAISE EXCEPTION 'retrieval source and chunk manifests are not a closed snapshot' USING ERRCODE = '23514';
            END IF;
        ELSIF EXISTS (SELECT 1 FROM retrieval.index_manifest_source WHERE index_version_id = NEW.id) THEN
            RAISE EXCEPTION 'legacy retrieval index cannot contain a source manifest' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

INSERT INTO core.schema_meta(key, value)
VALUES ('reindex_consumer', 'm6-b')
ON CONFLICT(key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
