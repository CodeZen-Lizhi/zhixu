-- +goose Up
CREATE TABLE IF NOT EXISTS core.workspace (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (btrim(name) <> ''),
    root_path text NOT NULL UNIQUE CHECK (btrim(root_path) <> ''),
    git_repository_path text NOT NULL CHECK (btrim(git_repository_path) <> ''),
    git_branch text NOT NULL DEFAULT '',
    git_head text NOT NULL DEFAULT '',
    git_dirty boolean NOT NULL DEFAULT false,
    git_checked_at timestamptz NOT NULL,
    status text NOT NULL CHECK (btrim(status) <> ''),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT workspace_updated_after_created CHECK (updated_at >= created_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_workspace_single_active
    ON core.workspace ((status))
    WHERE status = 'active';

CREATE TABLE IF NOT EXISTS core.source (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    type text NOT NULL CHECK (btrim(type) <> ''),
    logical_name text NOT NULL CHECK (btrim(logical_name) <> ''),
    original_location text NOT NULL CHECK (btrim(original_location) <> ''),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_source_workspace_location UNIQUE (workspace_id, original_location)
);

CREATE INDEX IF NOT EXISTS idx_source_workspace_id
    ON core.source (workspace_id, id);

CREATE TABLE IF NOT EXISTS core.source_version (
    id uuid PRIMARY KEY,
    source_id uuid NOT NULL REFERENCES core.source(id) ON DELETE RESTRICT,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    byte_size bigint NOT NULL CHECK (byte_size >= 0),
    mime_type text NOT NULL CHECK (btrim(mime_type) <> ''),
    original_content_location text NOT NULL CHECK (btrim(original_content_location) <> ''),
    security_status text NOT NULL CHECK (btrim(security_status) <> ''),
    parser_version text,
    captured_at timestamptz NOT NULL,
    CONSTRAINT uq_source_version_source_hash UNIQUE (source_id, content_hash)
);

CREATE INDEX IF NOT EXISTS idx_source_version_content_hash
    ON core.source_version (content_hash, source_id);

CREATE INDEX IF NOT EXISTS idx_source_version_source_captured
    ON core.source_version (source_id, captured_at DESC, id);

CREATE OR REPLACE FUNCTION core.reject_source_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'source_version is immutable'
        USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS source_version_reject_update_delete ON core.source_version;
CREATE TRIGGER source_version_reject_update_delete
    BEFORE UPDATE OR DELETE ON core.source_version
    FOR EACH ROW EXECUTE FUNCTION core.reject_source_version_mutation();

INSERT INTO core.schema_meta (key, value)
VALUES ('workspace_sources', 'm3')
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_at = now();

-- +goose Down
DROP TRIGGER IF EXISTS source_version_reject_update_delete ON core.source_version;
DROP FUNCTION IF EXISTS core.reject_source_version_mutation();
DROP TABLE IF EXISTS core.source_version;
DROP TABLE IF EXISTS core.source;
DROP TABLE IF EXISTS core.workspace;
DELETE FROM core.schema_meta WHERE key = 'workspace_sources';
