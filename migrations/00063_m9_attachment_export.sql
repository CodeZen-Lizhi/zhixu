-- +goose Up

-- Some installations recorded migration 00036 before its final export
-- hardening shape was published. Mark only that historical shape so the
-- compatibility backfill cannot rewrite already-hardened export facts.
ALTER TABLE ops.export_job
    ADD COLUMN IF NOT EXISTS migration_00063_needs_export_hardening boolean NOT NULL DEFAULT false;

-- +goose StatementBegin
DO $$
BEGIN
    IF (
        SELECT count(*)
        FROM information_schema.columns
        WHERE table_schema = 'ops'
          AND table_name = 'export_job'
          AND column_name IN (
              'request_hash','request_ttl_seconds','version','read_model_revision','exact_count',
              'prepared_at','prepared_staging_path','cleanup_status','cleanup_attempt_count',
              'cleanup_error','cleanup_updated_at','file_deleted_at'
          )
    ) < 12 THEN
        UPDATE ops.export_job
        SET migration_00063_needs_export_hardening = true;
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE ops.export_job
    ADD COLUMN IF NOT EXISTS request_hash text,
    ADD COLUMN IF NOT EXISTS request_ttl_seconds bigint,
    ADD COLUMN IF NOT EXISTS version bigint,
    ADD COLUMN IF NOT EXISTS read_model_revision text,
    ADD COLUMN IF NOT EXISTS exact_count bigint,
    ADD COLUMN IF NOT EXISTS prepared_at timestamptz,
    ADD COLUMN IF NOT EXISTS prepared_staging_path text,
    ADD COLUMN IF NOT EXISTS cleanup_status text NOT NULL DEFAULT 'NOT_REQUIRED',
    ADD COLUMN IF NOT EXISTS cleanup_attempt_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cleanup_error text,
    ADD COLUMN IF NOT EXISTS cleanup_updated_at timestamptz,
    ADD COLUMN IF NOT EXISTS file_deleted_at timestamptz;

UPDATE ops.export_job
SET expires_at = created_at + interval '24 hours'
WHERE migration_00063_needs_export_hardening
  AND (expires_at IS NULL OR expires_at <= created_at);

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
    query_hash = COALESCE(query_hash, NULLIF(query_definition->>'query_hash', ''))
WHERE migration_00063_needs_export_hardening;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM ops.export_job
        WHERE migration_00063_needs_export_hardening
          AND (
              kind NOT IN ('MARKDOWN', 'METADATA_JSON')
              OR collection_id IS NULL
              OR collection_version IS NULL
              OR collection_version < 1
              OR query_hash IS NULL
              OR query_hash !~ '^[0-9a-f]{64}$'
          )
    ) THEN
        RAISE EXCEPTION 'legacy export rows require a delivered kind and complete Collection binding before M9-03 hardening'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

UPDATE ops.export_job
SET fields = '["object_type","id","title","summary","status","topic","source","relations","health","confidence","created_at","updated_at"]'::jsonb
WHERE migration_00063_needs_export_hardening
  AND jsonb_array_length(fields) = 0;

UPDATE ops.export_job
SET request_ttl_seconds = COALESCE(
        request_ttl_seconds,
        LEAST(604800, GREATEST(1, CEIL(EXTRACT(EPOCH FROM (expires_at - created_at)))::bigint))
    ),
    request_hash = COALESCE(request_hash, md5('legacy-export:' || id::text) || md5(id::text || ':request')),
    version = COALESCE(version, 1),
    redaction_policy = CASE
        WHEN redaction_policy = 'FULL' AND NOT include_sensitive THEN 'MASKED'
        ELSE redaction_policy
    END
WHERE migration_00063_needs_export_hardening;

UPDATE ops.export_job
SET expires_at = created_at + make_interval(secs => request_ttl_seconds::double precision)
WHERE migration_00063_needs_export_hardening;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM ops.export_job
        WHERE migration_00063_needs_export_hardening
          AND (file_path IS NULL) <> (file_hash IS NULL)
    ) THEN
        RAISE EXCEPTION 'legacy export result has an incomplete path/hash binding'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

UPDATE ops.export_job
SET read_model_revision = COALESCE(read_model_revision, request_hash),
    exact_count = COALESCE(exact_count, 0),
    prepared_at = COALESCE(prepared_at, completed_at, updated_at),
    prepared_staging_path = COALESCE(
        prepared_staging_path,
        '.knowledge/exports/.staging/' || id::text || '-' || repeat('0', 32) ||
            CASE WHEN kind = 'MARKDOWN' THEN '.md.stage' ELSE '.json.stage' END
    ),
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
WHERE migration_00063_needs_export_hardening
  AND file_path IS NOT NULL
  AND file_hash IS NOT NULL;

UPDATE ops.export_job
SET status = 'PENDING', lease_owner = NULL, lease_expires_at = NULL,
    completed_at = NULL, error_code = NULL, error_message = NULL,
    cleanup_status = 'NOT_REQUIRED', cleanup_attempt_count = 0,
    cleanup_error = NULL, cleanup_updated_at = NULL, file_deleted_at = NULL
WHERE migration_00063_needs_export_hardening
  AND status = 'RUNNING'
  AND prepared_at IS NULL;

UPDATE ops.export_job
SET started_at = COALESCE(started_at, created_at),
    completed_at = COALESCE(completed_at, updated_at),
    error_code = COALESCE(NULLIF(btrim(error_code), ''), 'EXPORT_LEGACY_FAILED'),
    error_message = COALESCE(NULLIF(btrim(error_message), ''), 'legacy export failed'),
    lease_owner = NULL, lease_expires_at = NULL,
    cleanup_status = 'NOT_REQUIRED', cleanup_attempt_count = 0,
    cleanup_error = NULL, cleanup_updated_at = NULL, file_deleted_at = NULL
WHERE migration_00063_needs_export_hardening
  AND status = 'FAILED';

UPDATE ops.export_job
SET completed_at = COALESCE(completed_at, updated_at),
    error_code = NULL, error_message = NULL,
    lease_owner = NULL, lease_expires_at = NULL,
    cleanup_status = 'PENDING', cleanup_attempt_count = 0,
    cleanup_error = NULL, cleanup_updated_at = NULL, file_deleted_at = NULL
WHERE migration_00063_needs_export_hardening
  AND status = 'EXPIRED';

UPDATE ops.export_job
SET error_code = NULL, error_message = NULL,
    lease_owner = NULL, lease_expires_at = NULL,
    cleanup_status = 'NOT_REQUIRED', cleanup_attempt_count = 0,
    cleanup_error = NULL, cleanup_updated_at = NULL, file_deleted_at = NULL
WHERE migration_00063_needs_export_hardening
  AND status = 'CANCELLED';

UPDATE ops.export_job
SET completed_at = NULL, error_code = NULL, error_message = NULL,
    lease_owner = NULL, lease_expires_at = NULL,
    cleanup_status = 'NOT_REQUIRED', cleanup_attempt_count = 0,
    cleanup_error = NULL, cleanup_updated_at = NULL, file_deleted_at = NULL
WHERE migration_00063_needs_export_hardening
  AND status = 'PENDING';

ALTER TABLE ops.export_job
    ALTER COLUMN expires_at SET NOT NULL,
    ALTER COLUMN request_hash SET NOT NULL,
    ALTER COLUMN request_ttl_seconds SET NOT NULL,
    ALTER COLUMN version SET NOT NULL,
    DROP COLUMN migration_00063_needs_export_hardening;

-- M9 residual extends the durable Export union without rewriting 00036.
ALTER TABLE ops.export_job
    ADD COLUMN IF NOT EXISTS scope_kind text NOT NULL DEFAULT 'COLLECTION',
    ADD COLUMN IF NOT EXISTS attachment_root_contract_version text,
    ADD COLUMN IF NOT EXISTS manifest_sha256 text,
    ADD COLUMN IF NOT EXISTS entry_count bigint,
    ADD COLUMN IF NOT EXISTS total_uncompressed_bytes bigint;

CREATE TABLE IF NOT EXISTS ops.export_capability (
    capability_key text PRIMARY KEY,
    contract_version text NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT ops_export_capability_key CHECK (capability_key='workspace-attachments'),
    CONSTRAINT ops_export_capability_contract CHECK (contract_version='workspace-attachments/v1')
);

INSERT INTO ops.export_capability(capability_key,contract_version,enabled)
VALUES('workspace-attachments','workspace-attachments/v1',false)
ON CONFLICT (capability_key) DO NOTHING;

ALTER TABLE ops.export_job
    DROP CONSTRAINT IF EXISTS export_job_kind_check,
    DROP CONSTRAINT IF EXISTS ops_export_job_success_binding,
    DROP CONSTRAINT IF EXISTS ops_export_job_attempt_count,
    DROP CONSTRAINT IF EXISTS ops_export_job_download_count,
    DROP CONSTRAINT IF EXISTS ops_export_job_file_size,
    DROP CONSTRAINT IF EXISTS ops_export_job_lease_binding,
    DROP CONSTRAINT IF EXISTS ops_export_job_path_safe,
    DROP CONSTRAINT IF EXISTS ops_export_job_query_hash,
    DROP CONSTRAINT IF EXISTS ops_export_job_supported_kind,
    ADD CONSTRAINT ops_export_job_supported_kind
        CHECK (kind IN ('MARKDOWN','METADATA_JSON','ATTACHMENTS_ZIP')),
    DROP CONSTRAINT IF EXISTS ops_export_job_redaction_policy,
    ADD CONSTRAINT ops_export_job_redaction_policy
        CHECK (redaction_policy IN ('MASKED','FULL','RAW_USER_OWNED')),
    DROP CONSTRAINT IF EXISTS ops_export_job_sensitive_policy,
    ADD CONSTRAINT ops_export_job_sensitive_policy CHECK (
        (scope_kind='COLLECTION' AND (redaction_policy='FULL')=include_sensitive)
        OR (scope_kind='WORKSPACE_ATTACHMENTS' AND redaction_policy='RAW_USER_OWNED' AND NOT include_sensitive)
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_request_hash,
    ADD CONSTRAINT ops_export_job_request_hash
        CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    DROP CONSTRAINT IF EXISTS ops_export_job_request_ttl,
    ADD CONSTRAINT ops_export_job_request_ttl CHECK (
        request_ttl_seconds BETWEEN 1 AND 604800
        AND expires_at=created_at+make_interval(secs => request_ttl_seconds::double precision)
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_version,
    ADD CONSTRAINT ops_export_job_version CHECK (version>0),
    DROP CONSTRAINT IF EXISTS ops_export_job_scope_binding,
    ADD CONSTRAINT ops_export_job_scope_binding CHECK (
        ((
            scope_kind='COLLECTION'
            AND kind IN ('MARKDOWN','METADATA_JSON')
            AND schema_version='export/v1'
            AND collection_id IS NOT NULL
            AND collection_version IS NOT NULL AND collection_version>0
            AND query_hash IS NOT NULL AND query_hash ~ '^[0-9a-f]{64}$'
            AND attachment_root_contract_version IS NULL
            AND jsonb_array_length(fields)>0
        ) OR (
            scope_kind='WORKSPACE_ATTACHMENTS'
            AND kind='ATTACHMENTS_ZIP'
            AND schema_version='attachment-export/v1'
            AND collection_id IS NULL AND collection_version IS NULL AND query_hash IS NULL
            AND attachment_root_contract_version IS NOT NULL
            AND attachment_root_contract_version='workspace-attachments/v1'
            AND fields='[]'::jsonb
        )) IS TRUE
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_collection_workspace,
    ADD CONSTRAINT ops_export_job_collection_workspace
        FOREIGN KEY (collection_id,workspace_id)
        REFERENCES learning.smart_collection(id,workspace_id)
        ON DELETE RESTRICT,
    DROP CONSTRAINT IF EXISTS ops_export_job_counters,
    ADD CONSTRAINT ops_export_job_counters CHECK (
        file_size>=0 AND attempt_count>=0 AND download_count>=0 AND cleanup_attempt_count>=0
		AND (exact_count IS NULL OR exact_count BETWEEN 0 AND 10000)
		AND (entry_count IS NULL OR entry_count BETWEEN 0 AND 10000)
		AND (total_uncompressed_bytes IS NULL OR total_uncompressed_bytes BETWEEN 0 AND 1073741824)
		AND (scope_kind<>'WORKSPACE_ATTACHMENTS' OR file_size<=1073741824)
	),
    DROP CONSTRAINT IF EXISTS ops_export_job_prepared_binding,
    ADD CONSTRAINT ops_export_job_prepared_binding CHECK (
        ((
            prepared_at IS NULL
            AND read_model_revision IS NULL AND exact_count IS NULL
            AND manifest_sha256 IS NULL AND entry_count IS NULL AND total_uncompressed_bytes IS NULL
            AND prepared_staging_path IS NULL AND file_path IS NULL AND file_hash IS NULL AND file_size=0
        ) OR (
            prepared_at IS NOT NULL AND prepared_staging_path IS NOT NULL AND file_path IS NOT NULL
            AND file_hash IS NOT NULL AND file_hash ~ '^[0-9a-f]{64}$' AND file_size>=0
            AND (
                (
                    scope_kind='COLLECTION'
                    AND read_model_revision IS NOT NULL AND read_model_revision ~ '^[0-9a-f]{64}$'
                    AND exact_count IS NOT NULL AND exact_count BETWEEN 0 AND 10000
                    AND manifest_sha256 IS NULL AND entry_count IS NULL AND total_uncompressed_bytes IS NULL
                ) OR (
                    scope_kind='WORKSPACE_ATTACHMENTS'
                    AND read_model_revision IS NULL AND exact_count IS NULL
                    AND manifest_sha256 IS NOT NULL AND manifest_sha256 ~ '^[0-9a-f]{64}$'
                    AND entry_count IS NOT NULL AND entry_count BETWEEN 0 AND 10000
                    AND total_uncompressed_bytes IS NOT NULL AND total_uncompressed_bytes BETWEEN 0 AND 1073741824
                )
            )
        )) IS TRUE
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_prepared_path_binding,
    ADD CONSTRAINT ops_export_job_prepared_path_binding CHECK (
        (prepared_at IS NULL
        OR (kind='MARKDOWN' AND file_path='.knowledge/exports/'||id::text||'.md'
            AND prepared_staging_path ~ ('^\.knowledge/exports/\.staging/'||id::text||'-[0-9a-f]{32}\.md\.stage$'))
        OR (kind='METADATA_JSON' AND file_path='.knowledge/exports/'||id::text||'.json'
            AND prepared_staging_path ~ ('^\.knowledge/exports/\.staging/'||id::text||'-[0-9a-f]{32}\.json\.stage$'))
        OR (kind='ATTACHMENTS_ZIP' AND file_path='.knowledge/exports/'||id::text||'.zip'
            AND prepared_staging_path ~ ('^\.knowledge/exports/\.staging/'||id::text||'-[0-9a-f]{32}\.zip\.stage$'))
        ) IS TRUE
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
        (status='EXPIRED')=(cleanup_status<>'NOT_REQUIRED')
        AND CASE cleanup_status
            WHEN 'NOT_REQUIRED' THEN
                cleanup_attempt_count=0 AND cleanup_error IS NULL
                AND cleanup_updated_at IS NULL AND file_deleted_at IS NULL
            WHEN 'PENDING' THEN
                cleanup_attempt_count=0 AND cleanup_error IS NULL
                AND cleanup_updated_at IS NULL AND file_deleted_at IS NULL
            WHEN 'FAILED' THEN
                cleanup_attempt_count>0 AND cleanup_error IS NOT NULL
                AND cleanup_updated_at IS NOT NULL AND file_deleted_at IS NULL
            WHEN 'SUCCEEDED' THEN
                cleanup_attempt_count>0 AND cleanup_error IS NULL
                AND cleanup_updated_at IS NOT NULL AND file_deleted_at IS NOT NULL
                AND file_deleted_at=cleanup_updated_at
            ELSE false
        END
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_download_binding,
    ADD CONSTRAINT ops_export_job_download_binding CHECK (
        (download_count=0 AND last_downloaded_at IS NULL)
        OR (download_count>0 AND last_downloaded_at IS NOT NULL AND status IN ('SUCCEEDED','EXPIRED'))
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_time_order,
    ADD CONSTRAINT ops_export_job_time_order CHECK (
        updated_at>=created_at
        AND expires_at>created_at
        AND (started_at IS NULL OR started_at BETWEEN created_at AND updated_at)
        AND (prepared_at IS NULL OR prepared_at BETWEEN created_at AND updated_at)
        AND (completed_at IS NULL OR completed_at BETWEEN created_at AND updated_at)
        AND (last_downloaded_at IS NULL OR last_downloaded_at BETWEEN created_at AND updated_at)
        AND (lease_expires_at IS NULL OR lease_expires_at>updated_at)
        AND (cleanup_updated_at IS NULL OR cleanup_updated_at BETWEEN created_at AND updated_at)
        AND (file_deleted_at IS NULL OR file_deleted_at BETWEEN created_at AND updated_at)
    ),
    DROP CONSTRAINT IF EXISTS ops_export_job_text_bounds,
    ADD CONSTRAINT ops_export_job_text_bounds CHECK (
        btrim(idempotency_key)<>'' AND octet_length(idempotency_key)<=128
        AND btrim(permission_scope)<>'' AND octet_length(permission_scope)<=128
        AND btrim(requested_by)<>'' AND octet_length(requested_by)<=256
        AND (attachment_root_contract_version IS NULL OR octet_length(attachment_root_contract_version)<=64)
        AND (error_code IS NULL OR (btrim(error_code)<>'' AND octet_length(error_code)<=128))
        AND (error_message IS NULL OR (btrim(error_message)<>'' AND octet_length(error_message)<=512))
        AND (lease_owner IS NULL OR (btrim(lease_owner)<>'' AND octet_length(lease_owner)<=128))
        AND (cleanup_error IS NULL OR (btrim(cleanup_error)<>'' AND octet_length(cleanup_error)<=128))
    );

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.enforce_export_job_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version<>OLD.version+1 THEN
        RAISE EXCEPTION 'export job version must advance by one' USING ERRCODE='55000';
    END IF;
    IF ROW(
        NEW.id,NEW.workspace_id,NEW.scope_kind,NEW.kind,NEW.schema_version,NEW.query_definition,NEW.fields,
        NEW.idempotency_key,NEW.request_hash,NEW.request_ttl_seconds,NEW.expires_at,NEW.created_at,
        NEW.collection_id,NEW.collection_version,NEW.query_hash,NEW.attachment_root_contract_version,
        NEW.redaction_policy,NEW.include_sensitive,NEW.permission_scope,NEW.requested_by
    ) IS DISTINCT FROM ROW(
        OLD.id,OLD.workspace_id,OLD.scope_kind,OLD.kind,OLD.schema_version,OLD.query_definition,OLD.fields,
        OLD.idempotency_key,OLD.request_hash,OLD.request_ttl_seconds,OLD.expires_at,OLD.created_at,
        OLD.collection_id,OLD.collection_version,OLD.query_hash,OLD.attachment_root_contract_version,
        OLD.redaction_policy,OLD.include_sensitive,OLD.permission_scope,OLD.requested_by
    ) THEN
        RAISE EXCEPTION 'export request binding is immutable' USING ERRCODE='55000';
    END IF;
    IF OLD.prepared_at IS NOT NULL AND ROW(
        NEW.read_model_revision,NEW.exact_count,NEW.manifest_sha256,NEW.entry_count,NEW.total_uncompressed_bytes,
        NEW.prepared_at,NEW.prepared_staging_path,NEW.file_path,NEW.file_hash,NEW.file_size
    ) IS DISTINCT FROM ROW(
        OLD.read_model_revision,OLD.exact_count,OLD.manifest_sha256,OLD.entry_count,OLD.total_uncompressed_bytes,
        OLD.prepared_at,OLD.prepared_staging_path,OLD.file_path,OLD.file_hash,OLD.file_size
    ) THEN
        RAISE EXCEPTION 'export prepared result binding is immutable' USING ERRCODE='55000';
    END IF;
    IF NOT (
        (OLD.status=NEW.status AND OLD.status IN ('RUNNING','SUCCEEDED','EXPIRED'))
        OR (OLD.status='PENDING' AND NEW.status IN ('RUNNING','EXPIRED'))
        OR (OLD.status='RUNNING' AND NEW.status IN ('PENDING','SUCCEEDED','FAILED','EXPIRED'))
        OR (OLD.status='SUCCEEDED' AND NEW.status='EXPIRED')
        OR (OLD.status='FAILED' AND NEW.status='EXPIRED')
    ) THEN
        RAISE EXCEPTION 'export job status transition is invalid' USING ERRCODE='55000';
    END IF;
    IF NEW.attempt_count<OLD.attempt_count OR NEW.attempt_count>OLD.attempt_count+1
       OR NEW.download_count<OLD.download_count OR NEW.download_count>OLD.download_count+1
       OR NEW.cleanup_attempt_count<OLD.cleanup_attempt_count OR NEW.cleanup_attempt_count>OLD.cleanup_attempt_count+1 THEN
        RAISE EXCEPTION 'export counter transition is invalid' USING ERRCODE='55000';
    END IF;
    IF NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'export updated_at cannot move backwards' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_ops_export_job_update ON ops.export_job;
CREATE TRIGGER trg_ops_export_job_update
BEFORE UPDATE ON ops.export_job
FOR EACH ROW EXECUTE FUNCTION ops.enforce_export_job_update();

CREATE INDEX IF NOT EXISTS idx_ops_export_pending_recovery
    ON ops.export_job(status,lease_expires_at,created_at,id)
    WHERE status IN ('PENDING','RUNNING');
CREATE INDEX IF NOT EXISTS idx_ops_export_collection
    ON ops.export_job(workspace_id,collection_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_ops_export_expiry_candidate
    ON ops.export_job(expires_at,id)
    WHERE status IN ('PENDING','RUNNING','SUCCEEDED','FAILED');
CREATE INDEX IF NOT EXISTS idx_ops_export_cleanup_candidate
    ON ops.export_job(cleanup_status,cleanup_updated_at,updated_at,id)
    WHERE status='EXPIRED' AND file_deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_ops_export_prepared_staging
    ON ops.export_job(workspace_id,prepared_staging_path)
    WHERE prepared_staging_path IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ops_export_attachment
    ON ops.export_job(workspace_id,created_at DESC,id DESC)
    WHERE scope_kind='WORKSPACE_ATTACHMENTS';

-- +goose Down

LOCK TABLE ops.export_job, ops.export_capability IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ops.export_job WHERE scope_kind='WORKSPACE_ATTACHMENTS' LIMIT 1)
       OR EXISTS (SELECT 1 FROM ops.export_capability WHERE enabled LIMIT 1) THEN
        RAISE EXCEPTION 'enabled attachment export capability or history cannot be represented by the previous schema'
            USING ERRCODE='55000';
    END IF;
END
$$;
-- +goose StatementEnd

DROP INDEX IF EXISTS ops.idx_ops_export_attachment;
DROP TABLE IF EXISTS ops.export_capability;

ALTER TABLE ops.export_job
    DROP CONSTRAINT IF EXISTS ops_export_job_text_bounds,
    DROP CONSTRAINT IF EXISTS ops_export_job_prepared_path_binding,
    DROP CONSTRAINT IF EXISTS ops_export_job_prepared_binding,
    DROP CONSTRAINT IF EXISTS ops_export_job_counters,
    DROP CONSTRAINT IF EXISTS ops_export_job_scope_binding,
    DROP CONSTRAINT IF EXISTS ops_export_job_sensitive_policy,
    DROP CONSTRAINT IF EXISTS ops_export_job_redaction_policy,
    DROP CONSTRAINT IF EXISTS ops_export_job_supported_kind,
	ADD CONSTRAINT export_job_kind_check CHECK (kind IN ('MARKDOWN','METADATA_JSON','EVALUATION_JSON','AUDIT_JSON')),
    ADD CONSTRAINT ops_export_job_supported_kind CHECK (kind IN ('MARKDOWN','METADATA_JSON')),
    ADD CONSTRAINT ops_export_job_redaction_policy CHECK (redaction_policy IN ('MASKED','FULL')),
    ADD CONSTRAINT ops_export_job_sensitive_policy CHECK ((redaction_policy='FULL')=include_sensitive),
    ADD CONSTRAINT ops_export_job_scope_binding CHECK (
        collection_id IS NOT NULL AND collection_version IS NOT NULL AND collection_version>0
        AND query_hash IS NOT NULL AND query_hash ~ '^[0-9a-f]{64}$'
    ),
    ADD CONSTRAINT ops_export_job_counters CHECK (
        file_size>=0 AND attempt_count>=0 AND download_count>=0 AND cleanup_attempt_count>=0
        AND (exact_count IS NULL OR exact_count BETWEEN 0 AND 10000)
    ),
    ADD CONSTRAINT ops_export_job_prepared_binding CHECK (
        (prepared_at IS NULL AND read_model_revision IS NULL AND exact_count IS NULL
         AND prepared_staging_path IS NULL AND file_path IS NULL AND file_hash IS NULL AND file_size=0)
        OR (prepared_at IS NOT NULL AND read_model_revision ~ '^[0-9a-f]{64}$'
            AND exact_count BETWEEN 0 AND 10000 AND prepared_staging_path IS NOT NULL
            AND file_path IS NOT NULL AND file_hash ~ '^[0-9a-f]{64}$' AND file_size>=0)
    ),
    ADD CONSTRAINT ops_export_job_prepared_path_binding CHECK (
        prepared_at IS NULL
        OR (kind='MARKDOWN' AND file_path='.knowledge/exports/'||id::text||'.md'
            AND prepared_staging_path ~ ('^\.knowledge/exports/\.staging/'||id::text||'-[0-9a-f]{32}\.md\.stage$'))
        OR (kind='METADATA_JSON' AND file_path='.knowledge/exports/'||id::text||'.json'
            AND prepared_staging_path ~ ('^\.knowledge/exports/\.staging/'||id::text||'-[0-9a-f]{32}\.json\.stage$'))
    ),
    ADD CONSTRAINT ops_export_job_text_bounds CHECK (
        btrim(idempotency_key)<>'' AND octet_length(idempotency_key)<=128
        AND btrim(permission_scope)<>'' AND octet_length(permission_scope)<=128
        AND btrim(requested_by)<>'' AND octet_length(requested_by)<=256
        AND (error_code IS NULL OR (btrim(error_code)<>'' AND octet_length(error_code)<=128))
        AND (error_message IS NULL OR (btrim(error_message)<>'' AND octet_length(error_message)<=512))
        AND (lease_owner IS NULL OR (btrim(lease_owner)<>'' AND octet_length(lease_owner)<=128))
        AND (cleanup_error IS NULL OR (btrim(cleanup_error)<>'' AND octet_length(cleanup_error)<=128))
    );

ALTER TABLE ops.export_job
    DROP COLUMN IF EXISTS total_uncompressed_bytes,
    DROP COLUMN IF EXISTS entry_count,
    DROP COLUMN IF EXISTS manifest_sha256,
    DROP COLUMN IF EXISTS attachment_root_contract_version,
    DROP COLUMN IF EXISTS scope_kind;

-- Restore the 00036 trigger after removing the additional immutable fields.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.enforce_export_job_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'export job version must advance by one' USING ERRCODE='55000'; END IF;
    IF ROW(NEW.id,NEW.workspace_id,NEW.kind,NEW.schema_version,NEW.query_definition,NEW.fields,
        NEW.idempotency_key,NEW.request_hash,NEW.request_ttl_seconds,NEW.expires_at,NEW.created_at,
        NEW.collection_id,NEW.collection_version,NEW.query_hash,NEW.redaction_policy,
        NEW.include_sensitive,NEW.permission_scope,NEW.requested_by)
       IS DISTINCT FROM ROW(OLD.id,OLD.workspace_id,OLD.kind,OLD.schema_version,OLD.query_definition,OLD.fields,
        OLD.idempotency_key,OLD.request_hash,OLD.request_ttl_seconds,OLD.expires_at,OLD.created_at,
        OLD.collection_id,OLD.collection_version,OLD.query_hash,OLD.redaction_policy,
        OLD.include_sensitive,OLD.permission_scope,OLD.requested_by) THEN
        RAISE EXCEPTION 'export request binding is immutable' USING ERRCODE='55000';
    END IF;
    IF OLD.prepared_at IS NOT NULL AND ROW(NEW.read_model_revision,NEW.exact_count,NEW.prepared_at,
        NEW.prepared_staging_path,NEW.file_path,NEW.file_hash,NEW.file_size)
       IS DISTINCT FROM ROW(OLD.read_model_revision,OLD.exact_count,OLD.prepared_at,
        OLD.prepared_staging_path,OLD.file_path,OLD.file_hash,OLD.file_size) THEN
        RAISE EXCEPTION 'export prepared result binding is immutable' USING ERRCODE='55000';
    END IF;
    IF NOT ((OLD.status=NEW.status AND OLD.status IN ('RUNNING','SUCCEEDED','EXPIRED'))
        OR (OLD.status='PENDING' AND NEW.status IN ('RUNNING','EXPIRED'))
        OR (OLD.status='RUNNING' AND NEW.status IN ('PENDING','SUCCEEDED','FAILED','EXPIRED'))
        OR (OLD.status='SUCCEEDED' AND NEW.status='EXPIRED')
        OR (OLD.status='FAILED' AND NEW.status='EXPIRED')) THEN
        RAISE EXCEPTION 'export job status transition is invalid' USING ERRCODE='55000';
    END IF;
    IF NEW.attempt_count<OLD.attempt_count OR NEW.attempt_count>OLD.attempt_count+1 THEN RAISE EXCEPTION 'export attempt count transition is invalid' USING ERRCODE='55000'; END IF;
    IF NEW.download_count<OLD.download_count OR NEW.download_count>OLD.download_count+1 THEN RAISE EXCEPTION 'export download count transition is invalid' USING ERRCODE='55000'; END IF;
    IF NEW.cleanup_attempt_count<OLD.cleanup_attempt_count OR NEW.cleanup_attempt_count>OLD.cleanup_attempt_count+1 THEN RAISE EXCEPTION 'export cleanup count transition is invalid' USING ERRCODE='55000'; END IF;
    IF NEW.updated_at<OLD.updated_at THEN RAISE EXCEPTION 'export updated_at cannot move backwards' USING ERRCODE='55000'; END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
