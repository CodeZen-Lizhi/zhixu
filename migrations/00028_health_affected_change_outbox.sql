-- +goose Up

CREATE TABLE IF NOT EXISTS ops.health_affected_change_outbox (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    schema_version integer NOT NULL CHECK (schema_version > 0),
    event_version integer NOT NULL CHECK (event_version > 0),
    event_type text NOT NULL CHECK (event_type IN (
		'health.affected.knowledge_changed',
		'health.affected.index_failed',
		'health.affected.vector_degraded',
		'health.affected.lexical_degraded'
    )),
    event_key text NOT NULL CHECK (btrim(event_key) <> '' AND octet_length(event_key) <= 512),
    source_kind text NOT NULL CHECK (source_kind IN (
        'KNOWLEDGE_COMMAND_RECEIPT', 'INDEX_VERSION', 'CHUNK_PROJECTION'
    )),
    source_key text NOT NULL CHECK (btrim(source_key) <> '' AND octet_length(source_key) <= 512),
    source_hash text CHECK (source_hash IS NULL OR source_hash ~ '^[0-9a-f]{64}$'),
    aggregate_type text NOT NULL CHECK (btrim(aggregate_type) <> '' AND octet_length(aggregate_type) <= 64),
    aggregate_id uuid NOT NULL,
    aggregate_sub_id uuid,
    aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
    change_status text NOT NULL CHECK (btrim(change_status) <> '' AND octet_length(change_status) <= 128),
    change_code text CHECK (change_code IS NULL OR (btrim(change_code) <> '' AND octet_length(change_code) <= 128)),
    occurred_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL,
    attempt_count bigint NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_error_code text CHECK (
        last_error_code IS NULL OR (btrim(last_error_code) <> '' AND octet_length(last_error_code) <= 128)
    ),
    bound_scan_id uuid,
    bound_workflow_run_id uuid,
    published_at timestamptz,
    poisoned_at timestamptz,
    manual_recovery_required boolean NOT NULL DEFAULT false,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_ops_health_affected_change_event_key UNIQUE (event_key),
    CONSTRAINT fk_ops_health_affected_change_scan
        FOREIGN KEY (bound_scan_id, workspace_id)
        REFERENCES ops.health_scan(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_ops_health_affected_change_workflow
        FOREIGN KEY (bound_workflow_run_id, workspace_id)
        REFERENCES workflow.run(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT ops_health_affected_change_source_binding CHECK (
        (
            source_kind = 'KNOWLEDGE_COMMAND_RECEIPT'
            AND event_type = 'health.affected.knowledge_changed'
            AND aggregate_type IN ('TOPIC', 'CLAIM', 'RELATION', 'CONFLICT')
            AND aggregate_sub_id IS NULL
            AND source_hash IS NOT NULL
            AND change_code IS NULL
            AND (
                (change_status = 'topic.create' AND aggregate_type = 'TOPIC')
                OR (change_status IN ('claim.suggest', 'claim.confirm', 'claim.transition') AND aggregate_type = 'CLAIM')
                OR (change_status IN ('relation.suggest', 'relation.confirm', 'relation.transition') AND aggregate_type = 'RELATION')
                OR (change_status IN ('conflict.open', 'conflict.transition') AND aggregate_type = 'CONFLICT')
            )
        ) OR (
            source_kind = 'INDEX_VERSION'
            AND event_type = 'health.affected.index_failed'
            AND aggregate_type = 'INDEX_VERSION'
            AND aggregate_sub_id IS NULL
            AND source_key = aggregate_id::text
            AND source_hash IS NULL
            AND change_status = 'failed'
            AND change_code IS NOT NULL
        ) OR (
            source_kind = 'CHUNK_PROJECTION'
			AND event_type IN ('health.affected.vector_degraded', 'health.affected.lexical_degraded')
            AND aggregate_type = 'CHUNK_PROJECTION'
            AND aggregate_sub_id IS NOT NULL
            AND source_key = aggregate_id::text || ':' || aggregate_sub_id::text
            AND source_hash IS NULL
			AND (
				(event_type = 'health.affected.vector_degraded' AND change_status IN ('failed', 'skipped_oversized'))
				OR (event_type = 'health.affected.lexical_degraded' AND change_status = 'failed')
			)
            AND change_code IS NOT NULL
        )
    ),
    CONSTRAINT ops_health_affected_change_lifecycle CHECK (
        (
            published_at IS NULL
            AND poisoned_at IS NULL
            AND NOT manual_recovery_required
            AND bound_scan_id IS NULL
            AND bound_workflow_run_id IS NULL
        ) OR (
            published_at IS NOT NULL
            AND poisoned_at IS NULL
            AND NOT manual_recovery_required
            AND bound_scan_id IS NOT NULL
            AND bound_workflow_run_id IS NOT NULL
            AND last_error_code IS NULL
        ) OR (
            published_at IS NULL
            AND poisoned_at IS NOT NULL
            AND manual_recovery_required
            AND bound_scan_id IS NULL
            AND bound_workflow_run_id IS NULL
            AND last_error_code IS NOT NULL
        )
    ),
    CONSTRAINT ops_health_affected_change_time_order CHECK (
        available_at >= occurred_at
        AND updated_at >= created_at
        AND (published_at IS NULL OR published_at >= occurred_at)
        AND (poisoned_at IS NULL OR poisoned_at >= occurred_at)
    )
);

CREATE INDEX IF NOT EXISTS idx_ops_health_affected_change_due
    ON ops.health_affected_change_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL AND NOT manual_recovery_required;

CREATE INDEX IF NOT EXISTS idx_ops_health_affected_change_workspace
    ON ops.health_affected_change_outbox (workspace_id, occurred_at DESC, id);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.validate_health_affected_change_outbox_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'health affected-change is append-only' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.attempt_count <> 0
           OR NEW.version <> 1
           OR NEW.last_error_code IS NOT NULL
           OR NEW.bound_scan_id IS NOT NULL
           OR NEW.bound_workflow_run_id IS NOT NULL
           OR NEW.published_at IS NOT NULL
           OR NEW.poisoned_at IS NOT NULL
           OR NEW.manual_recovery_required
           OR NEW.available_at <> NEW.occurred_at
           OR NEW.created_at <> NEW.occurred_at
           OR NEW.updated_at <> NEW.occurred_at THEN
            RAISE EXCEPTION 'health affected-change has an invalid initial lifecycle' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.published_at IS NOT NULL OR OLD.manual_recovery_required THEN
        RAISE EXCEPTION 'terminal health affected-change is immutable' USING ERRCODE = '55000';
    END IF;
    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.schema_version <> OLD.schema_version
       OR NEW.event_version <> OLD.event_version
       OR NEW.event_type <> OLD.event_type
       OR NEW.event_key <> OLD.event_key
       OR NEW.source_kind <> OLD.source_kind
       OR NEW.source_key <> OLD.source_key
       OR NEW.source_hash IS DISTINCT FROM OLD.source_hash
       OR NEW.aggregate_type <> OLD.aggregate_type
       OR NEW.aggregate_id <> OLD.aggregate_id
       OR NEW.aggregate_sub_id IS DISTINCT FROM OLD.aggregate_sub_id
       OR NEW.aggregate_version <> OLD.aggregate_version
       OR NEW.change_status <> OLD.change_status
       OR NEW.change_code IS DISTINCT FROM OLD.change_code
       OR NEW.occurred_at <> OLD.occurred_at
       OR NEW.created_at <> OLD.created_at
       OR NEW.attempt_count <> OLD.attempt_count + 1
       OR NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at
       OR NEW.available_at < OLD.available_at THEN
        RAISE EXCEPTION 'health affected-change immutable binding or version changed' USING ERRCODE = '23514';
    END IF;

    IF NEW.published_at IS NOT NULL THEN
        IF NEW.last_error_code IS NOT NULL
           OR NEW.available_at <> OLD.available_at
           OR NOT EXISTS (
                SELECT 1
                  FROM ops.health_scan scan
                 WHERE scan.id = NEW.bound_scan_id
                   AND scan.workspace_id = NEW.workspace_id
                   AND scan.workflow_run_id = NEW.bound_workflow_run_id
                   AND scan.scope_type = 'WORKSPACE'
                   AND scan.scope_ref = NEW.workspace_id
                   AND scan.idempotency_key = 'health-affected-change:' || NEW.id::text
           ) THEN
            RAISE EXCEPTION 'published health affected-change scan binding is invalid' USING ERRCODE = '55000';
        END IF;
    ELSIF NEW.manual_recovery_required THEN
        IF NEW.poisoned_at IS NULL OR NEW.last_error_code IS NULL OR NEW.available_at <> OLD.available_at THEN
            RAISE EXCEPTION 'poisoned health affected-change binding is invalid' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.last_error_code IS NULL OR NEW.available_at <= OLD.available_at THEN
        RAISE EXCEPTION 'pending health affected-change retry must persist backoff' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS health_affected_change_outbox_validate_write ON ops.health_affected_change_outbox;
CREATE TRIGGER health_affected_change_outbox_validate_write
    BEFORE INSERT OR UPDATE OR DELETE ON ops.health_affected_change_outbox
    FOR EACH ROW EXECUTE FUNCTION ops.validate_health_affected_change_outbox_write();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.enqueue_health_affected_knowledge_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    event_key_value text;
    event_id_value uuid;
BEGIN
    event_key_value := 'health-affected-change:v1:knowledge:' || NEW.workspace_id::text || ':' || NEW.idempotency_key;
    event_id_value := md5(event_key_value)::uuid;
    INSERT INTO ops.health_affected_change_outbox (
        id, workspace_id, schema_version, event_version, event_type, event_key,
        source_kind, source_key, source_hash, aggregate_type, aggregate_id,
        aggregate_sub_id, aggregate_version, change_status, change_code,
        occurred_at, available_at, attempt_count, last_error_code,
        bound_scan_id, bound_workflow_run_id, published_at, poisoned_at,
        manual_recovery_required, version, created_at, updated_at
    ) VALUES (
        event_id_value, NEW.workspace_id, 1, 1, 'health.affected.knowledge_changed', event_key_value,
        'KNOWLEDGE_COMMAND_RECEIPT', NEW.idempotency_key, NEW.request_hash, NEW.aggregate_type, NEW.aggregate_id,
        NULL, NEW.aggregate_version, NEW.command_type, NULL,
        NEW.created_at, NEW.created_at, 0, NULL,
        NULL, NULL, NULL, NULL, false, 1, NEW.created_at, NEW.created_at
    ) ON CONFLICT (event_key) DO NOTHING;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS health_affected_knowledge_change_enqueue ON core.knowledge_command_receipt;
CREATE TRIGGER health_affected_knowledge_change_enqueue
    AFTER INSERT ON core.knowledge_command_receipt
    FOR EACH ROW EXECUTE FUNCTION ops.enqueue_health_affected_knowledge_change();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.enqueue_health_affected_index_failure()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    event_key_value text;
    event_id_value uuid;
BEGIN
    event_key_value := 'health-affected-change:v1:index:' || NEW.id::text || ':' || NEW.version::text;
    event_id_value := md5(event_key_value)::uuid;
    INSERT INTO ops.health_affected_change_outbox (
        id, workspace_id, schema_version, event_version, event_type, event_key,
        source_kind, source_key, source_hash, aggregate_type, aggregate_id,
        aggregate_sub_id, aggregate_version, change_status, change_code,
        occurred_at, available_at, attempt_count, last_error_code,
        bound_scan_id, bound_workflow_run_id, published_at, poisoned_at,
        manual_recovery_required, version, created_at, updated_at
    ) VALUES (
        event_id_value, NEW.workspace_id, 1, 1, 'health.affected.index_failed', event_key_value,
        'INDEX_VERSION', NEW.id::text, NULL, 'INDEX_VERSION', NEW.id,
        NULL, NEW.version, NEW.status, NEW.failure_code,
        NEW.updated_at, NEW.updated_at, 0, NULL,
        NULL, NULL, NULL, NULL, false, 1, NEW.updated_at, NEW.updated_at
    ) ON CONFLICT (event_key) DO NOTHING;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS health_affected_index_failure_enqueue ON retrieval.index_version;
CREATE TRIGGER health_affected_index_failure_enqueue
    AFTER UPDATE OF status ON retrieval.index_version
    FOR EACH ROW
    WHEN (OLD.status IS DISTINCT FROM NEW.status AND NEW.status = 'failed')
    EXECUTE FUNCTION ops.enqueue_health_affected_index_failure();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.enqueue_health_affected_vector_degradation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    event_key_value text;
    event_id_value uuid;
    index_version_value bigint;
BEGIN
    SELECT version
      INTO STRICT index_version_value
      FROM retrieval.index_version
     WHERE id = NEW.index_version_id AND workspace_id = NEW.workspace_id;

    event_key_value := 'health-affected-change:v1:vector:' || NEW.index_version_id::text || ':' || NEW.chunk_id::text || ':' || NEW.vector_status;
    event_id_value := md5(event_key_value)::uuid;
    INSERT INTO ops.health_affected_change_outbox (
        id, workspace_id, schema_version, event_version, event_type, event_key,
        source_kind, source_key, source_hash, aggregate_type, aggregate_id,
        aggregate_sub_id, aggregate_version, change_status, change_code,
        occurred_at, available_at, attempt_count, last_error_code,
        bound_scan_id, bound_workflow_run_id, published_at, poisoned_at,
        manual_recovery_required, version, created_at, updated_at
    ) VALUES (
        event_id_value, NEW.workspace_id, 1, 1, 'health.affected.vector_degraded', event_key_value,
        'CHUNK_PROJECTION', NEW.index_version_id::text || ':' || NEW.chunk_id::text, NULL,
        'CHUNK_PROJECTION', NEW.index_version_id, NEW.chunk_id, index_version_value,
        NEW.vector_status, NEW.failure_code,
        NEW.updated_at, NEW.updated_at, 0, NULL,
        NULL, NULL, NULL, NULL, false, 1, NEW.updated_at, NEW.updated_at
    ) ON CONFLICT (event_key) DO NOTHING;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS health_affected_vector_degradation_enqueue ON retrieval.chunk_projection;
CREATE TRIGGER health_affected_vector_degradation_enqueue
    AFTER UPDATE OF vector_status ON retrieval.chunk_projection
    FOR EACH ROW
    WHEN (OLD.vector_status = 'pending' AND NEW.vector_status IN ('failed', 'skipped_oversized'))
	EXECUTE FUNCTION ops.enqueue_health_affected_vector_degradation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.enqueue_health_affected_lexical_degradation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
	event_key_value text;
	event_id_value uuid;
	index_version_value bigint;
BEGIN
	SELECT version
	  INTO STRICT index_version_value
	  FROM retrieval.index_version
	 WHERE id = NEW.index_version_id AND workspace_id = NEW.workspace_id;

	event_key_value := 'health-affected-change:v1:lexical:' || NEW.index_version_id::text || ':' || NEW.chunk_id::text || ':' || NEW.lexical_status;
	event_id_value := md5(event_key_value)::uuid;
	INSERT INTO ops.health_affected_change_outbox (
		id, workspace_id, schema_version, event_version, event_type, event_key,
		source_kind, source_key, source_hash, aggregate_type, aggregate_id,
		aggregate_sub_id, aggregate_version, change_status, change_code,
		occurred_at, available_at, attempt_count, last_error_code,
		bound_scan_id, bound_workflow_run_id, published_at, poisoned_at,
		manual_recovery_required, version, created_at, updated_at
	) VALUES (
		event_id_value, NEW.workspace_id, 1, 1, 'health.affected.lexical_degraded', event_key_value,
		'CHUNK_PROJECTION', NEW.index_version_id::text || ':' || NEW.chunk_id::text, NULL,
		'CHUNK_PROJECTION', NEW.index_version_id, NEW.chunk_id, index_version_value,
		NEW.lexical_status, NEW.failure_code,
		NEW.updated_at, NEW.updated_at, 0, NULL,
		NULL, NULL, NULL, NULL, false, 1, NEW.updated_at, NEW.updated_at
	) ON CONFLICT (event_key) DO NOTHING;
	RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS health_affected_lexical_degradation_enqueue ON retrieval.chunk_projection;
CREATE TRIGGER health_affected_lexical_degradation_enqueue
	AFTER UPDATE OF lexical_status ON retrieval.chunk_projection
	FOR EACH ROW
	WHEN (OLD.lexical_status = 'pending' AND NEW.lexical_status = 'failed')
	EXECUTE FUNCTION ops.enqueue_health_affected_lexical_degradation();

INSERT INTO core.schema_meta (key, value)
VALUES ('health_affected_change_outbox', 'm7-03')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = CURRENT_TIMESTAMP;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ops.health_affected_change_outbox) THEN
        RAISE EXCEPTION 'cannot drop health affected-change outbox while events exist' USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS health_affected_lexical_degradation_enqueue ON retrieval.chunk_projection;
DROP TRIGGER IF EXISTS health_affected_vector_degradation_enqueue ON retrieval.chunk_projection;
DROP TRIGGER IF EXISTS health_affected_index_failure_enqueue ON retrieval.index_version;
DROP TRIGGER IF EXISTS health_affected_knowledge_change_enqueue ON core.knowledge_command_receipt;
DROP FUNCTION IF EXISTS ops.enqueue_health_affected_lexical_degradation();
DROP FUNCTION IF EXISTS ops.enqueue_health_affected_vector_degradation();
DROP FUNCTION IF EXISTS ops.enqueue_health_affected_index_failure();
DROP FUNCTION IF EXISTS ops.enqueue_health_affected_knowledge_change();
DROP TRIGGER IF EXISTS health_affected_change_outbox_validate_write ON ops.health_affected_change_outbox;
DROP FUNCTION IF EXISTS ops.validate_health_affected_change_outbox_write();
DROP INDEX IF EXISTS ops.idx_ops_health_affected_change_workspace;
DROP INDEX IF EXISTS ops.idx_ops_health_affected_change_due;
DROP TABLE IF EXISTS ops.health_affected_change_outbox;
DELETE FROM core.schema_meta WHERE key = 'health_affected_change_outbox';
