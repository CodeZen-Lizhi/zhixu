-- +goose Up

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

CREATE INDEX IF NOT EXISTS idx_ops_export_attachment
    ON ops.export_job(workspace_id,created_at DESC,id DESC)
    WHERE scope_kind='WORKSPACE_ATTACHMENTS';

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ops.export_job WHERE scope_kind='WORKSPACE_ATTACHMENTS' LIMIT 1) THEN
        RAISE EXCEPTION 'attachment export history cannot be represented by the previous schema'
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
