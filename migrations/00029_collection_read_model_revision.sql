-- +goose Up

-- Collection cursors and durable scans need an exact, Workspace-scoped change
-- token without rescanning every canonical table on each page.
CREATE TABLE IF NOT EXISTS core.workspace_read_model_revision (
    workspace_id uuid PRIMARY KEY REFERENCES core.workspace(id) ON DELETE CASCADE,
    knowledge_revision bigint NOT NULL DEFAULT 0 CHECK (knowledge_revision >= 0),
    conflict_revision bigint NOT NULL DEFAULT 0 CHECK (conflict_revision >= 0),
    health_revision bigint NOT NULL DEFAULT 0 CHECK (health_revision >= 0),
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO core.workspace_read_model_revision(workspace_id)
SELECT id FROM core.workspace
ON CONFLICT (workspace_id) DO NOTHING;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.bump_workspace_read_model_revision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    owner_workspace_id uuid;
    workspace_ids uuid[];
    knowledge_delta bigint := 0;
    conflict_delta bigint := 0;
    health_delta bigint := 0;
BEGIN
    IF TG_TABLE_SCHEMA = 'core' AND TG_TABLE_NAME = 'source_version' THEN
        SELECT workspace_id
          INTO owner_workspace_id
          FROM core.source
         WHERE id = CASE WHEN TG_OP = 'DELETE' THEN OLD.source_id ELSE NEW.source_id END;
    ELSIF TG_OP = 'UPDATE' AND OLD.workspace_id IS DISTINCT FROM NEW.workspace_id THEN
        workspace_ids := ARRAY[OLD.workspace_id, NEW.workspace_id];
    ELSE
        workspace_ids := ARRAY[CASE WHEN TG_OP = 'DELETE' THEN OLD.workspace_id ELSE NEW.workspace_id END];
    END IF;

    IF owner_workspace_id IS NOT NULL THEN
        workspace_ids := ARRAY[owner_workspace_id];
    END IF;

    IF workspace_ids IS NULL OR array_length(workspace_ids, 1) IS NULL THEN
        RAISE EXCEPTION 'read model revision workspace is missing'
            USING ERRCODE = '23514';
    END IF;

    SELECT array_agg(candidate.workspace_id ORDER BY candidate.workspace_id)
      INTO workspace_ids
      FROM (
          SELECT DISTINCT unnest(workspace_ids) AS workspace_id
      ) AS candidate;

    CASE TG_ARGV[0]
        WHEN 'knowledge' THEN knowledge_delta := 1;
        WHEN 'conflict' THEN conflict_delta := 1;
        WHEN 'health' THEN health_delta := 1;
        ELSE RAISE EXCEPTION 'read model revision kind is invalid' USING ERRCODE = '23514';
    END CASE;

    FOREACH owner_workspace_id IN ARRAY workspace_ids LOOP
        IF owner_workspace_id IS NULL THEN
            RAISE EXCEPTION 'read model revision workspace is missing' USING ERRCODE = '23514';
        END IF;
        INSERT INTO core.workspace_read_model_revision(
            workspace_id,knowledge_revision,conflict_revision,health_revision,updated_at
        ) VALUES (
            owner_workspace_id,knowledge_delta,conflict_delta,health_delta,CURRENT_TIMESTAMP
        )
        ON CONFLICT (workspace_id) DO UPDATE
        SET knowledge_revision = core.workspace_read_model_revision.knowledge_revision + EXCLUDED.knowledge_revision,
            conflict_revision = core.workspace_read_model_revision.conflict_revision + EXCLUDED.conflict_revision,
            health_revision = core.workspace_read_model_revision.health_revision + EXCLUDED.health_revision,
            updated_at = GREATEST(core.workspace_read_model_revision.updated_at, EXCLUDED.updated_at);
    END LOOP;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS collection_revision_topic_change ON core.topic;
CREATE TRIGGER collection_revision_topic_change
    AFTER INSERT OR UPDATE OR DELETE ON core.topic
    FOR EACH ROW EXECUTE FUNCTION core.bump_workspace_read_model_revision('knowledge');

DROP TRIGGER IF EXISTS collection_revision_topic_alias_change ON core.topic_alias;
CREATE TRIGGER collection_revision_topic_alias_change
    AFTER INSERT OR UPDATE OR DELETE ON core.topic_alias
    FOR EACH ROW EXECUTE FUNCTION core.bump_workspace_read_model_revision('knowledge');

DROP TRIGGER IF EXISTS collection_revision_claim_change ON core.claim;
CREATE TRIGGER collection_revision_claim_change
    AFTER INSERT OR UPDATE OR DELETE ON core.claim
    FOR EACH ROW EXECUTE FUNCTION core.bump_workspace_read_model_revision('knowledge');

DROP TRIGGER IF EXISTS collection_revision_claim_source_change ON core.claim_source;
CREATE TRIGGER collection_revision_claim_source_change
    AFTER INSERT OR UPDATE OR DELETE ON core.claim_source
    FOR EACH ROW EXECUTE FUNCTION core.bump_workspace_read_model_revision('knowledge');

DROP TRIGGER IF EXISTS collection_revision_source_change ON core.source;
CREATE TRIGGER collection_revision_source_change
    AFTER INSERT OR UPDATE OR DELETE ON core.source
    FOR EACH ROW EXECUTE FUNCTION core.bump_workspace_read_model_revision('knowledge');

DROP TRIGGER IF EXISTS collection_revision_source_version_change ON core.source_version;
CREATE TRIGGER collection_revision_source_version_change
    AFTER INSERT OR UPDATE OR DELETE ON core.source_version
    FOR EACH ROW EXECUTE FUNCTION core.bump_workspace_read_model_revision('knowledge');

DROP TRIGGER IF EXISTS collection_revision_relation_change ON core.relation;
CREATE TRIGGER collection_revision_relation_change
    AFTER INSERT OR UPDATE OR DELETE ON core.relation
    FOR EACH ROW EXECUTE FUNCTION core.bump_workspace_read_model_revision('knowledge');

DROP TRIGGER IF EXISTS collection_revision_conflict_change ON core.conflict;
CREATE TRIGGER collection_revision_conflict_change
    AFTER INSERT OR UPDATE OR DELETE ON core.conflict
    FOR EACH ROW EXECUTE FUNCTION core.bump_workspace_read_model_revision('conflict');

DROP TRIGGER IF EXISTS collection_revision_conflict_member_change ON core.conflict_member;
CREATE TRIGGER collection_revision_conflict_member_change
    AFTER INSERT OR UPDATE OR DELETE ON core.conflict_member
    FOR EACH ROW EXECUTE FUNCTION core.bump_workspace_read_model_revision('conflict');

DROP TRIGGER IF EXISTS collection_revision_health_issue_change ON ops.health_issue;
CREATE TRIGGER collection_revision_health_issue_change
    AFTER INSERT OR DELETE OR UPDATE OF target_type,target_id,type,severity,evidence_summary,status,updated_at
    ON ops.health_issue
    FOR EACH ROW EXECUTE FUNCTION core.bump_workspace_read_model_revision('health');

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM learning.smart_collection)
       OR EXISTS (SELECT 1 FROM ops.health_scan)
       OR EXISTS (SELECT 1 FROM ops.health_issue)
       OR EXISTS (SELECT 1 FROM ops.health_schedule) THEN
        RAISE EXCEPTION 'cannot remove collection read model revision while collection or health facts exist'
            USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS collection_revision_health_issue_change ON ops.health_issue;
DROP TRIGGER IF EXISTS collection_revision_conflict_member_change ON core.conflict_member;
DROP TRIGGER IF EXISTS collection_revision_conflict_change ON core.conflict;
DROP TRIGGER IF EXISTS collection_revision_relation_change ON core.relation;
DROP TRIGGER IF EXISTS collection_revision_source_version_change ON core.source_version;
DROP TRIGGER IF EXISTS collection_revision_source_change ON core.source;
DROP TRIGGER IF EXISTS collection_revision_claim_source_change ON core.claim_source;
DROP TRIGGER IF EXISTS collection_revision_claim_change ON core.claim;
DROP TRIGGER IF EXISTS collection_revision_topic_alias_change ON core.topic_alias;
DROP TRIGGER IF EXISTS collection_revision_topic_change ON core.topic;
DROP FUNCTION IF EXISTS core.bump_workspace_read_model_revision();
DROP TABLE IF EXISTS core.workspace_read_model_revision;
