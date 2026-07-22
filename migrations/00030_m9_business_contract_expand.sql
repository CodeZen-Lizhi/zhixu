-- +goose Up

-- Expand first so the pre-contract application can continue inserting
-- Proposals while historical rows are backfilled in a later migration.
ALTER TABLE change_control.proposal
    ADD COLUMN IF NOT EXISTS risk_level text DEFAULT 'HIGH';

ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS ck_proposal_risk_level,
    ADD CONSTRAINT ck_proposal_risk_level CHECK (
        risk_level IN ('CRITICAL', 'HIGH', 'MEDIUM', 'LOW')
    ) NOT VALID;

ALTER TABLE core.source_version
    ADD COLUMN IF NOT EXISTS workspace_id uuid;

ALTER TABLE core.source_version
    DROP CONSTRAINT IF EXISTS ck_source_version_workspace_required,
    ADD CONSTRAINT ck_source_version_workspace_required CHECK (
        workspace_id IS NOT NULL
    ) NOT VALID;

-- Keep old writers compatible while making the derived Workspace binding
-- explicit. An explicitly supplied Workspace must always match the Source.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.bind_source_version_workspace()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    source_workspace_id uuid;
BEGIN
    SELECT workspace_id
      INTO source_workspace_id
      FROM core.source
     WHERE id = NEW.source_id;

    IF source_workspace_id IS NULL THEN
        RAISE EXCEPTION 'source version source workspace is missing' USING ERRCODE = '23514';
    END IF;
    IF NEW.workspace_id IS NULL THEN
        NEW.workspace_id := source_workspace_id;
    ELSIF NEW.workspace_id <> source_workspace_id THEN
        RAISE EXCEPTION 'source version workspace does not match source' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS source_version_bind_workspace ON core.source_version;
CREATE TRIGGER source_version_bind_workspace
    BEFORE INSERT ON core.source_version
    FOR EACH ROW EXECUTE FUNCTION core.bind_source_version_workspace();

-- Preserve the M5 Content Artifact legacy-binding allowance and temporarily
-- add the one exact mutation needed by the 00032 Workspace backfill.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.reject_source_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    source_workspace_id uuid;
BEGIN
    IF TG_OP = 'UPDATE'
       AND OLD.content_artifact_id IS NULL
       AND NEW.content_artifact_id IS NOT NULL
       AND (to_jsonb(NEW) - 'content_artifact_id') = (to_jsonb(OLD) - 'content_artifact_id') THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE'
       AND OLD.workspace_id IS NULL
       AND NEW.workspace_id IS NOT NULL
       AND (to_jsonb(NEW) - 'workspace_id') = (to_jsonb(OLD) - 'workspace_id') THEN
        SELECT workspace_id
          INTO source_workspace_id
          FROM core.source
         WHERE id = NEW.source_id;
        IF source_workspace_id IS NOT NULL AND NEW.workspace_id = source_workspace_id THEN
            RETURN NEW;
        END IF;
    END IF;

    RAISE EXCEPTION 'source_version is immutable' USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

-- +goose Down

-- The expanded columns already carry durable Proposal/Source Version facts at
-- every intermediate M9 migration version. Never silently discard them when
-- rolling back from 00030 directly or after 00031/00032 have been applied.
-- +goose StatementBegin
DO $$
BEGIN
    LOCK TABLE change_control.proposal IN ACCESS EXCLUSIVE MODE;
    LOCK TABLE core.source_version IN ACCESS EXCLUSIVE MODE;

    IF EXISTS (SELECT 1 FROM change_control.proposal)
       OR EXISTS (SELECT 1 FROM core.source_version) THEN
        RAISE EXCEPTION 'cannot downgrade M9 business contract hardening while Proposal or Source Version data exists'
            USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS source_version_bind_workspace ON core.source_version;
DROP FUNCTION IF EXISTS core.bind_source_version_workspace();

ALTER TABLE core.source_version
    DROP CONSTRAINT IF EXISTS ck_source_version_workspace_required,
    DROP COLUMN IF EXISTS workspace_id;

-- Restore the pre-M9 M5 Content Artifact legacy-binding allowance.
-- +goose StatementBegin
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
-- +goose StatementEnd

ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS ck_proposal_risk_level,
    DROP COLUMN IF EXISTS risk_level;
