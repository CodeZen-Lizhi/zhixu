-- +goose Up
CREATE TABLE IF NOT EXISTS core.content_artifact (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    byte_size bigint NOT NULL CHECK (byte_size >= 0),
    managed_location text NOT NULL CHECK (managed_location = '.knowledge/sources/' || content_hash),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_content_artifact_workspace_hash UNIQUE (workspace_id, content_hash),
    CONSTRAINT uq_content_artifact_workspace_id UNIQUE (workspace_id, id)
);

ALTER TABLE core.source_version
    ADD COLUMN IF NOT EXISTS content_artifact_id uuid;

ALTER TABLE core.source_version
    DROP CONSTRAINT IF EXISTS source_version_content_artifact_id_fkey;
ALTER TABLE core.source_version
    ADD CONSTRAINT source_version_content_artifact_id_fkey
    FOREIGN KEY (content_artifact_id) REFERENCES core.content_artifact(id) ON DELETE RESTRICT;

-- Expand phase: existing immutable Source Versions cannot be safely backfilled
-- from mutable original paths inside a schema-only migration. NOT VALID keeps
-- historical rows readable while enforcing the artifact reference for all new rows.
ALTER TABLE core.source_version
    DROP CONSTRAINT IF EXISTS source_version_content_artifact_required;
ALTER TABLE core.source_version
    ADD CONSTRAINT source_version_content_artifact_required
    CHECK (content_artifact_id IS NOT NULL) NOT VALID;

-- Legacy rows are backfilled only after the application has re-read the original
-- path and verified its recorded hash and size. No other Source Version field may change.
CREATE OR REPLACE FUNCTION core.reject_source_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE'
       AND OLD.content_artifact_id IS NULL
       AND NEW.content_artifact_id IS NOT NULL
       AND (to_jsonb(NEW) - 'content_artifact_id') = (to_jsonb(OLD) - 'content_artifact_id') THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'source_version is immutable' USING ERRCODE = '55000';
END;
$$;

CREATE OR REPLACE FUNCTION core.verify_source_version_artifact_workspace()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    source_workspace uuid;
    artifact_workspace uuid;
BEGIN
    SELECT workspace_id INTO source_workspace FROM core.source WHERE id = NEW.source_id;
    SELECT workspace_id INTO artifact_workspace FROM core.content_artifact WHERE id = NEW.content_artifact_id;
    IF source_workspace IS NULL OR artifact_workspace IS NULL OR source_workspace <> artifact_workspace THEN
        RAISE EXCEPTION 'source version artifact workspace mismatch' USING ERRCODE = '23514';
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM core.content_artifact
        WHERE id = NEW.content_artifact_id
          AND content_hash = NEW.content_hash
          AND byte_size = NEW.byte_size
    ) THEN
        RAISE EXCEPTION 'source version artifact content metadata mismatch' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS source_version_verify_artifact_workspace ON core.source_version;
CREATE TRIGGER source_version_verify_artifact_workspace
    BEFORE INSERT OR UPDATE OF content_artifact_id ON core.source_version
    FOR EACH ROW EXECUTE FUNCTION core.verify_source_version_artifact_workspace();

CREATE OR REPLACE FUNCTION core.reject_content_artifact_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'content_artifact is immutable' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS content_artifact_reject_update_delete ON core.content_artifact;
CREATE TRIGGER content_artifact_reject_update_delete
    BEFORE UPDATE OR DELETE ON core.content_artifact
    FOR EACH ROW EXECUTE FUNCTION core.reject_content_artifact_mutation();

CREATE INDEX IF NOT EXISTS idx_content_artifact_workspace_created
    ON core.content_artifact (workspace_id, created_at DESC, id);

INSERT INTO core.schema_meta (key, value)
VALUES ('content_artifact', 'm5-ingestion')
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_at = now();

-- +goose Down
DROP TRIGGER IF EXISTS source_version_verify_artifact_workspace ON core.source_version;
DROP FUNCTION IF EXISTS core.verify_source_version_artifact_workspace();
DROP TRIGGER IF EXISTS content_artifact_reject_update_delete ON core.content_artifact;
DROP FUNCTION IF EXISTS core.reject_content_artifact_mutation();
ALTER TABLE core.source_version DROP CONSTRAINT IF EXISTS source_version_content_artifact_required;
ALTER TABLE core.source_version DROP CONSTRAINT IF EXISTS source_version_content_artifact_id_fkey;
ALTER TABLE core.source_version DROP COLUMN IF EXISTS content_artifact_id;
CREATE OR REPLACE FUNCTION core.reject_source_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'source_version is immutable' USING ERRCODE = '55000';
END;
$$;
DROP TABLE IF EXISTS core.content_artifact;
DELETE FROM core.schema_meta WHERE key = 'content_artifact';
