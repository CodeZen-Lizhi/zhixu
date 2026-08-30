-- M9-03 extends the base export fact from 00034 without rewriting history.
-- PostgreSQL owns request identity, lease/TTL time, prepared result binding,
-- cleanup state and download statistics; River remains transport only.
ALTER TABLE ops.export_job
    ADD COLUMN IF NOT EXISTS collection_id uuid,
    ADD COLUMN IF NOT EXISTS collection_version bigint,
    ADD COLUMN IF NOT EXISTS query_hash text,
    ADD COLUMN IF NOT EXISTS redaction_policy text NOT NULL DEFAULT 'MASKED',
    ADD COLUMN IF NOT EXISTS include_sensitive boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS permission_scope text NOT NULL DEFAULT 'READ_LOCAL',
    ADD COLUMN IF NOT EXISTS requested_by text NOT NULL DEFAULT 'system',
    ADD COLUMN IF NOT EXISTS request_hash text,
    ADD COLUMN IF NOT EXISTS request_ttl_seconds bigint,
    ADD COLUMN IF NOT EXISTS version bigint,
    ADD COLUMN IF NOT EXISTS read_model_revision text,
    ADD COLUMN IF NOT EXISTS exact_count bigint,
    ADD COLUMN IF NOT EXISTS prepared_at timestamptz,
    ADD COLUMN IF NOT EXISTS prepared_staging_path text,
    ADD COLUMN IF NOT EXISTS file_size bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS error_message text,
    ADD COLUMN IF NOT EXISTS attempt_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS lease_owner text,
    ADD COLUMN IF NOT EXISTS lease_expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS started_at timestamptz,
    ADD COLUMN IF NOT EXISTS download_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_downloaded_at timestamptz,
    ADD COLUMN IF NOT EXISTS cleanup_status text NOT NULL DEFAULT 'NOT_REQUIRED',
    ADD COLUMN IF NOT EXISTS cleanup_attempt_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cleanup_error text,
    ADD COLUMN IF NOT EXISTS cleanup_updated_at timestamptz,
    ADD COLUMN IF NOT EXISTS file_deleted_at timestamptz;

UPDATE ops.export_job
SET expires_at = created_at + interval '24 hours'
WHERE expires_at IS NULL OR expires_at <= created_at;

UPDATE ops.export_job
SET collection_id = CASE
        WHEN collection_id IS NOT NULL THEN collection_id
        WHEN query_definition->>'collection_id' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
            THEN (query_definition->>'collection_id')::uuid
        ELSE NULL
    END,
    collection_version = CASE
        WHEN collection_version IS NOT NULL THEN collection_version
        WHEN query_definition->>'collection_version' ~ '^[1-9][0-9]*$'
            THEN (query_definition->>'collection_version')::bigint
        ELSE NULL
    END,
    query_hash = COALESCE(query_hash, NULLIF(query_definition->>'query_hash', ''));

-- A pre-hardening row without a Collection identity cannot be interpreted as
-- an M9-03 export request. Refuse migration instead of inventing a scope.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM ops.export_job
        WHERE kind NOT IN ('MARKDOWN', 'METADATA_JSON')
           OR collection_id IS NULL
           OR collection_version IS NULL
           OR collection_version < 1
           OR query_hash IS NULL
           OR query_hash !~ '^[0-9a-f]{64}$'
    ) THEN
        RAISE EXCEPTION 'legacy export rows require a delivered kind and complete Collection binding before M9-03 hardening'
            USING ERRCODE = '55000';
    END IF;
END
$$;

UPDATE ops.export_job
SET fields = CASE kind
        WHEN 'MARKDOWN' THEN '["object_type","id","title","summary","status","topic","source","relations","health","confidence","created_at","updated_at"]'::jsonb
        ELSE '["object_type","id","title","summary","status","topic","source","relations","health","confidence","created_at","updated_at"]'::jsonb
    END
WHERE jsonb_array_length(fields) = 0;

UPDATE ops.export_job
SET request_ttl_seconds = LEAST(
        604800,
        GREATEST(1, CEIL(EXTRACT(EPOCH FROM (expires_at - created_at)))::bigint)
    ),
    request_hash = COALESCE(request_hash, md5('legacy-export:' || id::text) || md5(id::text || ':request')),
    version = COALESCE(version, 1),
    redaction_policy = CASE
        WHEN redaction_policy = 'FULL' AND NOT include_sensitive THEN 'MASKED'
        ELSE redaction_policy
    END;

UPDATE ops.export_job
SET expires_at = created_at + make_interval(secs => request_ttl_seconds::double precision);

-- Base rows can only carry a trustworthy result when path and hash are both
-- present. A partial pair is not recoverable and must not be normalized.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM ops.export_job
        WHERE (file_path IS NULL) <> (file_hash IS NULL)
    ) THEN
        RAISE EXCEPTION 'legacy export result has an incomplete path/hash binding'
            USING ERRCODE = '55000';
    END IF;
END
$$;

UPDATE ops.export_job
SET read_model_revision = request_hash,
    exact_count = 0,
    prepared_at = COALESCE(completed_at, updated_at),
    prepared_staging_path = '.knowledge/exports/.staging/' || id::text || '-' || repeat('0', 32) ||
        CASE WHEN kind = 'MARKDOWN' THEN '.md.stage' ELSE '.json.stage' END,
    started_at = COALESCE(started_at, created_at),
    completed_at = COALESCE(completed_at, updated_at),
    status = CASE WHEN status = 'SUCCEEDED' THEN 'SUCCEEDED' ELSE 'EXPIRED' END,
    lease_owner = NULL,
    lease_expires_at = NULL,
    error_code = NULL,
    error_message = NULL,
    cleanup_status = CASE WHEN status = 'SUCCEEDED' THEN 'NOT_REQUIRED' ELSE 'PENDING' END,
    cleanup_attempt_count = 0,
    cleanup_error = NULL,
    cleanup_updated_at = NULL,
    file_deleted_at = NULL
WHERE file_path IS NOT NULL AND file_hash IS NOT NULL;

UPDATE ops.export_job
SET status = 'PENDING', lease_owner = NULL, lease_expires_at = NULL,
    completed_at = NULL, error_code = NULL, error_message = NULL,
    cleanup_status = 'NOT_REQUIRED', cleanup_attempt_count = 0,
    cleanup_error = NULL, cleanup_updated_at = NULL, file_deleted_at = NULL
WHERE status = 'RUNNING' AND prepared_at IS NULL;

UPDATE ops.export_job
SET started_at = COALESCE(started_at, created_at),
    completed_at = COALESCE(completed_at, updated_at),
    error_code = COALESCE(NULLIF(btrim(error_code), ''), 'EXPORT_LEGACY_FAILED'),
    error_message = COALESCE(NULLIF(btrim(error_message), ''), 'legacy export failed'),
    lease_owner = NULL, lease_expires_at = NULL,
    cleanup_status = 'NOT_REQUIRED', cleanup_attempt_count = 0,
    cleanup_error = NULL, cleanup_updated_at = NULL, file_deleted_at = NULL
WHERE status = 'FAILED';

UPDATE ops.export_job
SET completed_at = COALESCE(completed_at, updated_at),
    error_code = NULL, error_message = NULL,
    lease_owner = NULL, lease_expires_at = NULL,
    cleanup_status = 'PENDING', cleanup_attempt_count = 0,
    cleanup_error = NULL, cleanup_updated_at = NULL, file_deleted_at = NULL
WHERE status = 'EXPIRED';

UPDATE ops.export_job
SET error_code = NULL, error_message = NULL,
    lease_owner = NULL, lease_expires_at = NULL,
    cleanup_status = 'NOT_REQUIRED', cleanup_attempt_count = 0,
    cleanup_error = NULL, cleanup_updated_at = NULL, file_deleted_at = NULL
WHERE status = 'CANCELLED';

UPDATE ops.export_job
SET completed_at = NULL, error_code = NULL, error_message = NULL,
    lease_owner = NULL, lease_expires_at = NULL,
    cleanup_status = 'NOT_REQUIRED', cleanup_attempt_count = 0,
    cleanup_error = NULL, cleanup_updated_at = NULL, file_deleted_at = NULL
WHERE status = 'PENDING';

ALTER TABLE ops.export_job
    ALTER COLUMN expires_at SET NOT NULL,
    ALTER COLUMN request_hash SET NOT NULL,
    ALTER COLUMN request_ttl_seconds SET NOT NULL,
    ALTER COLUMN version SET NOT NULL;

ALTER TABLE ops.export_job
    DROP CONSTRAINT IF EXISTS ops_export_job_success_binding,
    DROP CONSTRAINT IF EXISTS ops_export_job_supported_kind,
    ADD CONSTRAINT ops_export_job_supported_kind
        CHECK (kind IN ('MARKDOWN', 'METADATA_JSON')),
    DROP CONSTRAINT IF EXISTS ops_export_job_redaction_policy,
    ADD CONSTRAINT ops_export_job_redaction_policy
        CHECK (redaction_policy IN ('MASKED', 'FULL')),
    DROP CONSTRAINT IF EXISTS ops_export_job_sensitive_policy,
    ADD CONSTRAINT ops_export_job_sensitive_policy
        CHECK ((redaction_policy = 'FULL') = include_sensitive),
    DROP CONSTRAINT IF EXISTS ops_export_job_request_hash,
    ADD CONSTRAINT ops_export_job_request_hash
        CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    DROP CONSTRAINT IF EXISTS ops_export_job_request_ttl,
    ADD CONSTRAINT ops_export_job_request_ttl
        CHECK (
            request_ttl_seconds BETWEEN 1 AND 604800
            AND expires_at = created_at + make_interval(secs => request_ttl_seconds::double precision)
        ),
    DROP CONSTRAINT IF EXISTS ops_export_job_version,
    ADD CONSTRAINT ops_export_job_version CHECK (version > 0),
    DROP CONSTRAINT IF EXISTS ops_export_job_scope_binding,
    ADD CONSTRAINT ops_export_job_scope_binding CHECK (
        collection_id IS NOT NULL
        AND collection_version IS NOT NULL
        AND collection_version > 0
        AND query_hash IS NOT NULL
        AND query_hash ~ '^[0-9a-f]{64}$'
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_collection_workspace,
    ADD CONSTRAINT ops_export_job_collection_workspace
        FOREIGN KEY (collection_id, workspace_id)
        REFERENCES learning.smart_collection(id, workspace_id)
        ON DELETE RESTRICT,
    DROP CONSTRAINT IF EXISTS ops_export_job_counters,
    ADD CONSTRAINT ops_export_job_counters CHECK (
        file_size >= 0
        AND attempt_count >= 0
        AND download_count >= 0
        AND cleanup_attempt_count >= 0
        AND (exact_count IS NULL OR exact_count BETWEEN 0 AND 10000)
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_prepared_binding,
    ADD CONSTRAINT ops_export_job_prepared_binding CHECK (
        (
            prepared_at IS NULL
            AND read_model_revision IS NULL
            AND exact_count IS NULL
            AND prepared_staging_path IS NULL
            AND file_path IS NULL
            AND file_hash IS NULL
            AND file_size = 0
        )
        OR (
            prepared_at IS NOT NULL
            AND read_model_revision ~ '^[0-9a-f]{64}$'
            AND exact_count BETWEEN 0 AND 10000
            AND prepared_staging_path IS NOT NULL
            AND file_path IS NOT NULL
            AND file_hash ~ '^[0-9a-f]{64}$'
            AND file_size >= 0
        )
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_prepared_path_binding,
    ADD CONSTRAINT ops_export_job_prepared_path_binding CHECK (
        prepared_at IS NULL
        OR (
            kind = 'MARKDOWN'
            AND file_path = '.knowledge/exports/' || id::text || '.md'
            AND prepared_staging_path ~ ('^\.knowledge/exports/\.staging/' || id::text || '-[0-9a-f]{32}\.md\.stage$')
        )
        OR (
            kind = 'METADATA_JSON'
            AND file_path = '.knowledge/exports/' || id::text || '.json'
            AND prepared_staging_path ~ ('^\.knowledge/exports/\.staging/' || id::text || '-[0-9a-f]{32}\.json\.stage$')
        )
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_lifecycle_binding,
    ADD CONSTRAINT ops_export_job_lifecycle_binding CHECK (
        CASE status
            WHEN 'PENDING' THEN
                prepared_at IS NULL AND completed_at IS NULL
                AND lease_owner IS NULL AND lease_expires_at IS NULL
                AND error_code IS NULL AND error_message IS NULL
            WHEN 'RUNNING' THEN
                started_at IS NOT NULL AND completed_at IS NULL
                AND lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL
                AND error_code IS NULL AND error_message IS NULL
            WHEN 'SUCCEEDED' THEN
                prepared_at IS NOT NULL AND started_at IS NOT NULL AND completed_at IS NOT NULL
                AND lease_owner IS NULL AND lease_expires_at IS NULL
                AND error_code IS NULL AND error_message IS NULL
            WHEN 'FAILED' THEN
                started_at IS NOT NULL AND completed_at IS NOT NULL
                AND lease_owner IS NULL AND lease_expires_at IS NULL
                AND error_code IS NOT NULL AND error_message IS NOT NULL
            WHEN 'EXPIRED' THEN
                completed_at IS NOT NULL
                AND lease_owner IS NULL AND lease_expires_at IS NULL
                AND error_code IS NULL AND error_message IS NULL
            WHEN 'CANCELLED' THEN
                prepared_at IS NULL
                AND lease_owner IS NULL AND lease_expires_at IS NULL
                AND error_code IS NULL AND error_message IS NULL
            ELSE false
        END
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_cleanup_binding,
    ADD CONSTRAINT ops_export_job_cleanup_binding CHECK (
        (status = 'EXPIRED') = (cleanup_status <> 'NOT_REQUIRED')
        AND CASE cleanup_status
            WHEN 'NOT_REQUIRED' THEN
                cleanup_attempt_count = 0 AND cleanup_error IS NULL
                AND cleanup_updated_at IS NULL AND file_deleted_at IS NULL
            WHEN 'PENDING' THEN
                cleanup_attempt_count = 0 AND cleanup_error IS NULL
                AND cleanup_updated_at IS NULL AND file_deleted_at IS NULL
            WHEN 'FAILED' THEN
                cleanup_attempt_count > 0 AND cleanup_error IS NOT NULL
                AND cleanup_updated_at IS NOT NULL AND file_deleted_at IS NULL
            WHEN 'SUCCEEDED' THEN
                cleanup_attempt_count > 0 AND cleanup_error IS NULL
                AND cleanup_updated_at IS NOT NULL AND file_deleted_at IS NOT NULL
                AND file_deleted_at = cleanup_updated_at
            ELSE false
        END
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_download_binding,
    ADD CONSTRAINT ops_export_job_download_binding CHECK (
        (download_count = 0 AND last_downloaded_at IS NULL)
        OR (
            download_count > 0
            AND last_downloaded_at IS NOT NULL
            AND status IN ('SUCCEEDED', 'EXPIRED')
        )
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_time_order,
    ADD CONSTRAINT ops_export_job_time_order CHECK (
        updated_at >= created_at
        AND expires_at > created_at
        AND (started_at IS NULL OR started_at BETWEEN created_at AND updated_at)
        AND (prepared_at IS NULL OR prepared_at BETWEEN created_at AND updated_at)
        AND (completed_at IS NULL OR completed_at BETWEEN created_at AND updated_at)
        AND (last_downloaded_at IS NULL OR last_downloaded_at BETWEEN created_at AND updated_at)
        AND (lease_expires_at IS NULL OR lease_expires_at > updated_at)
        AND (cleanup_updated_at IS NULL OR cleanup_updated_at BETWEEN created_at AND updated_at)
        AND (file_deleted_at IS NULL OR file_deleted_at BETWEEN created_at AND updated_at)
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_text_bounds,
    ADD CONSTRAINT ops_export_job_text_bounds CHECK (
        btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 128
        AND btrim(permission_scope) <> '' AND octet_length(permission_scope) <= 128
        AND btrim(requested_by) <> '' AND octet_length(requested_by) <= 256
        AND (error_code IS NULL OR (btrim(error_code) <> '' AND octet_length(error_code) <= 128))
        AND (error_message IS NULL OR (btrim(error_message) <> '' AND octet_length(error_message) <= 512))
        AND (lease_owner IS NULL OR (btrim(lease_owner) <> '' AND octet_length(lease_owner) <= 128))
        AND (cleanup_error IS NULL OR (btrim(cleanup_error) <> '' AND octet_length(cleanup_error) <= 128))
    );

CREATE OR REPLACE FUNCTION ops.enforce_export_job_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'export job version must advance by one' USING ERRCODE = '55000';
    END IF;
    IF ROW(
        NEW.id,NEW.workspace_id,NEW.kind,NEW.schema_version,NEW.query_definition,NEW.fields,
        NEW.idempotency_key,NEW.request_hash,NEW.request_ttl_seconds,NEW.expires_at,NEW.created_at,
        NEW.collection_id,NEW.collection_version,NEW.query_hash,NEW.redaction_policy,
        NEW.include_sensitive,NEW.permission_scope,NEW.requested_by
    ) IS DISTINCT FROM ROW(
        OLD.id,OLD.workspace_id,OLD.kind,OLD.schema_version,OLD.query_definition,OLD.fields,
        OLD.idempotency_key,OLD.request_hash,OLD.request_ttl_seconds,OLD.expires_at,OLD.created_at,
        OLD.collection_id,OLD.collection_version,OLD.query_hash,OLD.redaction_policy,
        OLD.include_sensitive,OLD.permission_scope,OLD.requested_by
    ) THEN
        RAISE EXCEPTION 'export request binding is immutable' USING ERRCODE = '55000';
    END IF;
    IF OLD.prepared_at IS NOT NULL AND ROW(
        NEW.read_model_revision,NEW.exact_count,NEW.prepared_at,NEW.prepared_staging_path,
        NEW.file_path,NEW.file_hash,NEW.file_size
    ) IS DISTINCT FROM ROW(
        OLD.read_model_revision,OLD.exact_count,OLD.prepared_at,OLD.prepared_staging_path,
        OLD.file_path,OLD.file_hash,OLD.file_size
    ) THEN
        RAISE EXCEPTION 'export prepared result binding is immutable' USING ERRCODE = '55000';
    END IF;
    IF NOT (
        (OLD.status = NEW.status AND OLD.status IN ('RUNNING','SUCCEEDED','EXPIRED'))
        OR (OLD.status = 'PENDING' AND NEW.status IN ('RUNNING','EXPIRED'))
        OR (OLD.status = 'RUNNING' AND NEW.status IN ('PENDING','SUCCEEDED','FAILED','EXPIRED'))
        OR (OLD.status = 'SUCCEEDED' AND NEW.status = 'EXPIRED')
        OR (OLD.status = 'FAILED' AND NEW.status = 'EXPIRED')
    ) THEN
        RAISE EXCEPTION 'export job status transition is invalid' USING ERRCODE = '55000';
    END IF;
    IF NEW.attempt_count < OLD.attempt_count OR NEW.attempt_count > OLD.attempt_count + 1 THEN
        RAISE EXCEPTION 'export attempt count transition is invalid' USING ERRCODE = '55000';
    END IF;
    IF NEW.download_count < OLD.download_count OR NEW.download_count > OLD.download_count + 1 THEN
        RAISE EXCEPTION 'export download count transition is invalid' USING ERRCODE = '55000';
    END IF;
    IF NEW.cleanup_attempt_count < OLD.cleanup_attempt_count OR NEW.cleanup_attempt_count > OLD.cleanup_attempt_count + 1 THEN
        RAISE EXCEPTION 'export cleanup count transition is invalid' USING ERRCODE = '55000';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'export updated_at cannot move backwards' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS trg_ops_export_job_update ON ops.export_job;
CREATE TRIGGER trg_ops_export_job_update
BEFORE UPDATE ON ops.export_job
FOR EACH ROW EXECUTE FUNCTION ops.enforce_export_job_update();

CREATE INDEX IF NOT EXISTS idx_ops_export_pending_recovery
    ON ops.export_job (status, lease_expires_at, created_at, id)
    WHERE status IN ('PENDING', 'RUNNING');
CREATE INDEX IF NOT EXISTS idx_ops_export_collection
    ON ops.export_job (workspace_id, collection_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_ops_export_expiry_candidate
    ON ops.export_job (expires_at, id)
    WHERE status IN ('PENDING', 'RUNNING', 'SUCCEEDED', 'FAILED');
CREATE INDEX IF NOT EXISTS idx_ops_export_cleanup_candidate
    ON ops.export_job (cleanup_status, cleanup_updated_at, updated_at, id)
    WHERE status = 'EXPIRED' AND file_deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_ops_export_prepared_staging
    ON ops.export_job (workspace_id, prepared_staging_path)
    WHERE prepared_staging_path IS NOT NULL;
