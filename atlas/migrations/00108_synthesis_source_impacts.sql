-- Source availability is a review signal, not a mutation of note content.
CREATE TABLE organizing.synthesis_source_impact (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    note_id uuid NOT NULL,
    source_id uuid NOT NULL,
    source_version_id uuid NOT NULL,
    reason text NOT NULL CHECK (reason IN ('SOURCE_REMOVED','SOURCE_QUARANTINED')),
    detected_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (note_id,workspace_id) REFERENCES organizing.synthesis_note(id,workspace_id) ON DELETE RESTRICT,
    FOREIGN KEY (source_id,workspace_id) REFERENCES core.source(id,workspace_id) ON DELETE RESTRICT,
    FOREIGN KEY (source_version_id,source_id) REFERENCES core.source_version(id,source_id) ON DELETE RESTRICT,
    UNIQUE (workspace_id,note_id,source_version_id,reason)
);
CREATE INDEX idx_synthesis_source_impact_note ON organizing.synthesis_source_impact(workspace_id,note_id);
CREATE FUNCTION organizing.validate_synthesis_source_impact()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM organizing.synthesis_revision_source ref
        WHERE ref.workspace_id=NEW.workspace_id AND ref.note_id=NEW.note_id
          AND ref.source_id=NEW.source_id AND ref.source_version_id=NEW.source_version_id) THEN
        RAISE EXCEPTION 'source impact must refer to saved note evidence' USING ERRCODE='23514';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM core.source s JOIN core.source_version v ON v.source_id=s.id
        WHERE s.workspace_id=NEW.workspace_id AND s.id=NEW.source_id AND v.id=NEW.source_version_id
        AND CASE NEW.reason
            WHEN 'SOURCE_REMOVED' THEN s.removed_at IS NOT NULL
            WHEN 'SOURCE_QUARANTINED' THEN v.security_status='quarantined' OR EXISTS (
                SELECT 1 FROM ingestion.attempt a WHERE a.workspace_id=s.workspace_id
                  AND a.source_version_id=v.id AND a.security_status='quarantined')
            ELSE false END) THEN
        RAISE EXCEPTION 'source impact lacks owner evidence' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_source_impact_validate BEFORE INSERT ON organizing.synthesis_source_impact
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_source_impact();
CREATE TRIGGER synthesis_source_impact_append_only BEFORE UPDATE OR DELETE ON organizing.synthesis_source_impact
    FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_source_impact_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_source_impact
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
